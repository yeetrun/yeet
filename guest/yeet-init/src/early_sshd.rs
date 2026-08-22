use std::fs;
use std::io;
use std::net::{SocketAddr, TcpStream};
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::process::{Child, Command};
use std::thread;
use std::time::{Duration, Instant};

const EARLY_SSHD_READY_TIMEOUT: Duration = Duration::from_millis(750);
const EARLY_SSHD_PID_FILE: &str = "/run/yeet-vm/early-sshd.pid";
const EARLY_SSHD_PAM_PATH: &str = "/etc/pam.d/yeet-early-sshd";
const EARLY_SESSION_PAM_PATH: &str = "/etc/pam.d/yeet-early-session";

pub fn prepare_runtime() -> io::Result<()> {
    mount_run_tmpfs()?;
    fs::create_dir_all("/run/sshd")?;
    fs::create_dir_all("/run/yeet-vm")?;
    install_early_pam_config()
}

#[cfg(target_os = "linux")]
fn mount_run_tmpfs() -> io::Result<()> {
    let source = c"tmpfs";
    let target = c"/run";
    let fstype = c"tmpfs";
    let data = c"mode=755";
    let flags = libc::MS_NOSUID | libc::MS_NODEV | libc::MS_STRICTATIME;
    let rc = unsafe {
        libc::mount(
            source.as_ptr(),
            target.as_ptr(),
            fstype.as_ptr(),
            flags,
            data.as_ptr().cast(),
        )
    };
    if rc == 0 {
        return Ok(());
    }
    let err = io::Error::last_os_error();
    if err.raw_os_error() == Some(libc::EBUSY) {
        Ok(())
    } else {
        Err(err)
    }
}

#[cfg(not(target_os = "linux"))]
fn mount_run_tmpfs() -> io::Result<()> {
    Ok(())
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
struct CommandLine {
    program: &'static str,
    args: &'static [&'static str],
}

fn launch_command() -> CommandLine {
    CommandLine {
        program: "/usr/sbin/sshd",
        args: &[
            "-D",
            "-e",
            "-f",
            "/etc/ssh/sshd_config",
            "-o",
            "PAMServiceName=yeet-early-sshd",
        ],
    }
}

fn install_early_pam_config() -> io::Result<()> {
    let sshd = fs::read_to_string("/etc/pam.d/sshd")?;
    let common_session = fs::read_to_string("/etc/pam.d/common-session")?;
    let (early_sshd, early_session) = render_early_pam_config(&sshd, &common_session)?;
    write_atomic(EARLY_SSHD_PAM_PATH, early_sshd.as_bytes())?;
    write_atomic(EARLY_SESSION_PAM_PATH, early_session.as_bytes())
}

fn render_early_pam_config(sshd: &str, common_session: &str) -> io::Result<(String, String)> {
    let mut replaced_common_session = false;
    let mut early_sshd = String::new();
    for line in sshd.lines() {
        if line.trim() == "@include common-session" {
            early_sshd.push_str("@include yeet-early-session\n");
            replaced_common_session = true;
        } else {
            early_sshd.push_str(line);
            early_sshd.push('\n');
        }
    }
    if !replaced_common_session {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "/etc/pam.d/sshd does not include common-session",
        ));
    }

    let mut early_session = String::new();
    for line in common_session.lines() {
        let trimmed = line.trim_start();
        let uses_pam_systemd = !trimmed.starts_with('#')
            && trimmed
                .split_whitespace()
                .any(|field| field.ends_with("/pam_systemd.so") || field == "pam_systemd.so");
        if !uses_pam_systemd {
            early_session.push_str(line);
            early_session.push('\n');
        }
    }
    Ok((early_sshd, early_session))
}

fn write_atomic(path: &str, contents: &[u8]) -> io::Result<()> {
    let temporary = format!("{path}.tmp");
    fs::write(&temporary, contents)?;
    fs::set_permissions(&temporary, fs::Permissions::from_mode(0o644))?;
    fs::rename(temporary, path)
}

pub fn start_early_sshd(enabled: bool) -> io::Result<Option<Child>> {
    let mut child = start_early_sshd_with(enabled, spawn_command, wait_for_listener, stop_child)?;
    if let Some(child) = child.as_mut()
        && let Err(err) = write_pid_file(Path::new(EARLY_SSHD_PID_FILE), child.id())
    {
        let _ = stop_child(child);
        return Err(err);
    }
    Ok(child)
}

pub fn stop_early_sshd(child: &mut Child) -> io::Result<()> {
    let result = stop_child(child);
    match fs::remove_file(EARLY_SSHD_PID_FILE) {
        Ok(()) => result,
        Err(err) if err.kind() == io::ErrorKind::NotFound => result,
        Err(err) => Err(err),
    }
}

fn write_pid_file(path: &Path, pid: u32) -> io::Result<()> {
    fs::write(path, format!("{pid}\n"))
}

fn start_early_sshd_with<ChildHandle, Spawn, Wait, Stop>(
    enabled: bool,
    mut spawn: Spawn,
    mut wait: Wait,
    mut stop: Stop,
) -> io::Result<Option<ChildHandle>>
where
    Spawn: FnMut(&CommandLine) -> io::Result<ChildHandle>,
    Wait: FnMut(Duration) -> bool,
    Stop: FnMut(&mut ChildHandle) -> io::Result<()>,
{
    if !enabled {
        return Ok(None);
    }
    let mut child = spawn(&launch_command())?;
    if wait(EARLY_SSHD_READY_TIMEOUT) {
        return Ok(Some(child));
    }
    let _ = stop(&mut child);
    Err(io::Error::new(
        io::ErrorKind::TimedOut,
        "early sshd did not listen on port 22",
    ))
}

fn spawn_command(command: &CommandLine) -> io::Result<Child> {
    Command::new(command.program).args(command.args).spawn()
}

fn wait_for_listener(timeout: Duration) -> bool {
    let address = SocketAddr::from(([127, 0, 0, 1], 22));
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if TcpStream::connect_timeout(&address, Duration::from_millis(25)).is_ok() {
            return true;
        }
        thread::sleep(Duration::from_millis(10));
    }
    false
}

fn stop_child(child: &mut Child) -> io::Result<()> {
    if child.try_wait()?.is_some() {
        return Ok(());
    }
    child.kill()?;
    child.wait()?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;
    use std::io;

    #[test]
    fn launches_the_distro_sshd_without_auth_overrides() {
        assert_eq!(
            launch_command(),
            CommandLine {
                program: "/usr/sbin/sshd",
                args: &[
                    "-D",
                    "-e",
                    "-f",
                    "/etc/ssh/sshd_config",
                    "-o",
                    "PAMServiceName=yeet-early-sshd",
                ],
            }
        );
        for forbidden in [
            "PasswordAuthentication=yes",
            "KbdInteractiveAuthentication=yes",
            "PermitRootLogin=yes",
        ] {
            assert!(
                !launch_command()
                    .args
                    .iter()
                    .any(|arg| arg.contains(forbidden))
            );
        }
    }

    #[test]
    fn early_pam_stack_omits_only_pam_systemd() {
        let sshd = "@include common-auth\n@include common-account\n@include common-session\n";
        let common_session = "# session optional pam_systemd.so\nsession required pam_unix.so\nsession optional pam_systemd.so\nsession optional pam_umask.so\n";

        let (early_sshd, early_session) =
            render_early_pam_config(sshd, common_session).expect("render PAM config");

        assert_eq!(
            early_sshd,
            "@include common-auth\n@include common-account\n@include yeet-early-session\n"
        );
        assert_eq!(
            early_session,
            "# session optional pam_systemd.so\nsession required pam_unix.so\nsession optional pam_umask.so\n"
        );
        assert!(!early_sshd.contains("session optional pam_systemd.so"));
        assert_eq!(early_session.matches("pam_systemd.so").count(), 1);
    }

    #[test]
    fn early_pam_stack_requires_the_distro_session_include() {
        let err =
            render_early_pam_config("@include common-auth\n", "session required pam_unix.so\n")
                .expect_err("missing common-session include");
        assert_eq!(err.kind(), io::ErrorKind::InvalidData);
    }

    #[test]
    fn atomic_pam_write_uses_non_writable_permissions() {
        let path = std::env::temp_dir().join(format!("yeet-early-pam-{}", std::process::id()));
        write_atomic(
            path.to_str().expect("temporary path"),
            b"session required pam_unix.so\n",
        )
        .expect("write PAM file");

        let metadata = fs::metadata(&path).expect("PAM file metadata");
        assert_eq!(metadata.permissions().mode() & 0o777, 0o644);
        assert_eq!(
            fs::read_to_string(&path).expect("read PAM file"),
            "session required pam_unix.so\n"
        );
        fs::remove_file(path).expect("remove PAM file");
    }

    #[test]
    fn disabled_spike_does_not_run_commands() {
        let result: Option<()> = start_early_sshd_with(
            false,
            |_| panic!("spawn must not run"),
            |_| panic!("listener check must not run"),
            |_: &mut ()| panic!("stop must not run"),
        )
        .expect("disabled spike");
        assert!(result.is_none());
    }

    #[test]
    fn launches_before_waiting_for_listener() {
        let events = RefCell::new(Vec::new());
        let result = start_early_sshd_with(
            true,
            |command| {
                assert_eq!(command, &launch_command());
                events.borrow_mut().push("spawn");
                Ok(42)
            },
            |timeout| {
                assert_eq!(timeout, EARLY_SSHD_READY_TIMEOUT);
                events.borrow_mut().push("wait");
                true
            },
            |_| panic!("healthy child must not stop"),
        )
        .expect("start early sshd");

        assert_eq!(result, Some(42));
        assert_eq!(events.into_inner(), ["spawn", "wait"]);
    }

    #[test]
    fn listener_failure_stops_the_child() {
        let stopped = RefCell::new(Vec::new());
        let err = start_early_sshd_with(
            true,
            |_| Ok(42),
            |_| false,
            |child| {
                stopped.borrow_mut().push(*child);
                Ok(())
            },
        )
        .expect_err("listener failure");

        assert_eq!(err.kind(), io::ErrorKind::TimedOut);
        assert_eq!(stopped.into_inner(), [42]);
    }

    #[test]
    fn writes_listener_pid_for_systemd_handoff() {
        let path = std::env::temp_dir().join(format!("yeet-early-sshd-{}.pid", std::process::id()));
        write_pid_file(&path, 42).expect("write pid file");
        assert_eq!(
            std::fs::read_to_string(&path).expect("read pid file"),
            "42\n"
        );
        std::fs::remove_file(Path::new(&path)).expect("remove pid file");
    }
}
