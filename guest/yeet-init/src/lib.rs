use std::collections::HashMap;
use std::ffi::CString;
use std::fs::{self, OpenOptions};
use std::io::{self, Write};
use std::os::unix::process::CommandExt;
use std::path::Path;
use std::process::Command;
use std::thread;
use std::time::{Duration, Instant};

pub const DEFAULT_INTERFACE: &str = "eth0";
pub const DEFAULT_SYSTEMD: &str = "/usr/lib/systemd/systemd";
pub const SERIAL_TTY: &str = "/dev/ttyS0";

#[derive(Debug, Clone, Eq, PartialEq)]
pub struct BootConfig {
    pub hostname: Option<String>,
    pub interface: String,
}

impl BootConfig {
    pub fn from_cmdline(input: &str) -> Self {
        let args = parse_cmdline(input);
        Self {
            hostname: args.get("yeet.hostname").cloned(),
            interface: args
                .get("yeet.iface")
                .cloned()
                .unwrap_or_else(|| DEFAULT_INTERFACE.to_string()),
        }
    }
}

pub fn parse_cmdline(input: &str) -> HashMap<String, String> {
    let mut out = HashMap::new();
    for part in input.split_whitespace() {
        if let Some((key, value)) = part.split_once('=') {
            out.insert(key.to_string(), value.to_string());
        } else {
            out.insert(part.to_string(), String::new());
        }
    }
    out
}

pub fn parse_ip_addr_output(input: &str, iface: &str) -> Option<String> {
    for line in input.lines() {
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() < 4 {
            continue;
        }
        let line_iface = fields[1].trim_end_matches(':');
        if line_iface != iface {
            continue;
        }
        for pair in fields.windows(2) {
            if pair[0] == "inet" {
                return pair[1].split('/').next().map(str::to_string);
            }
        }
    }
    None
}

pub fn serial_ip_line(iface: &str, ip: &str) -> String {
    format!("yeet-ip {iface} {ip}\n")
}

pub fn wait_for_ipv4(iface: &str, timeout: Duration) -> Option<String> {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if let Some(ip) = current_ipv4(iface) {
            return Some(ip);
        }
        thread::sleep(Duration::from_millis(25));
    }
    current_ipv4(iface)
}

pub fn current_ipv4(iface: &str) -> Option<String> {
    let output = Command::new("/usr/bin/ip")
        .args(["-o", "-4", "addr", "show", "dev", iface, "scope", "global"])
        .output()
        .or_else(|_| {
            Command::new("/usr/sbin/ip")
                .args(["-o", "-4", "addr", "show", "dev", iface, "scope", "global"])
                .output()
        })
        .or_else(|_| {
            Command::new("/sbin/ip")
                .args(["-o", "-4", "addr", "show", "dev", iface, "scope", "global"])
                .output()
        })
        .ok()?;
    if !output.status.success() {
        return None;
    }
    parse_ip_addr_output(&String::from_utf8_lossy(&output.stdout), iface)
}

pub fn set_hostname(name: &str) -> io::Result<()> {
    let c_name = CString::new(name)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "hostname contains a nul byte"))?;
    #[cfg(target_os = "linux")]
    let rc = unsafe { libc::sethostname(c_name.as_ptr(), name.len()) };
    #[cfg(not(target_os = "linux"))]
    let rc = unsafe {
        libc::sethostname(
            c_name.as_ptr(),
            libc::c_int::try_from(name.len())
                .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "hostname is too long"))?,
        )
    };
    if rc == 0 {
        Ok(())
    } else {
        Err(io::Error::last_os_error())
    }
}

pub fn write_serial(path: &Path, line: &str) -> io::Result<()> {
    let mut file = OpenOptions::new().write(true).open(path)?;
    file.write_all(line.as_bytes())
}

pub fn prepare_runtime_dirs() -> io::Result<()> {
    fs::create_dir_all("/run/sshd")?;
    fs::create_dir_all("/run/yeet-vm")?;
    Ok(())
}

pub fn run_before_systemd(cmdline: &str) -> io::Result<()> {
    run_before_systemd_with(
        cmdline,
        Path::new(SERIAL_TTY),
        prepare_runtime_dirs,
        set_hostname,
        wait_for_ipv4,
        write_serial,
    )
}

fn run_before_systemd_with<Prepare, SetHostname, WaitForIPv4, WriteSerial>(
    cmdline: &str,
    serial_path: &Path,
    mut prepare_runtime_dirs: Prepare,
    mut set_hostname: SetHostname,
    mut wait_for_ipv4: WaitForIPv4,
    mut write_serial: WriteSerial,
) -> io::Result<()>
where
    Prepare: FnMut() -> io::Result<()>,
    SetHostname: FnMut(&str) -> io::Result<()>,
    WaitForIPv4: FnMut(&str, Duration) -> Option<String>,
    WriteSerial: FnMut(&Path, &str) -> io::Result<()>,
{
    let cfg = BootConfig::from_cmdline(cmdline);
    prepare_runtime_dirs()?;
    if let Some(hostname) = cfg.hostname.as_deref()
        && !hostname.is_empty()
        && let Err(err) = set_hostname(hostname)
    {
        let _ = write_serial(
            serial_path,
            &format!("yeet-init-error set hostname: {err}\n"),
        );
    }
    if let Some(ip) = wait_for_ipv4(&cfg.interface, Duration::from_millis(1500)) {
        let _ = write_serial(serial_path, &serial_ip_line(&cfg.interface, &ip));
    }
    Ok(())
}

pub fn exec_systemd() -> io::Error {
    Command::new(DEFAULT_SYSTEMD)
        .arg("--unit=multi-user.target")
        .exec()
}

pub fn run() -> io::Result<()> {
    let cmdline = read_cmdline_or_empty(Path::new("/proc/cmdline"), Path::new(SERIAL_TTY));
    if let Err(err) = run_before_systemd(&cmdline) {
        let _ = write_serial(Path::new(SERIAL_TTY), &format!("yeet-init-error {err}\n"));
    }
    Err(exec_systemd())
}

fn read_cmdline_or_empty(cmdline_path: &Path, serial_path: &Path) -> String {
    match fs::read_to_string(cmdline_path) {
        Ok(cmdline) => cmdline,
        Err(err) => {
            let _ = write_serial(
                serial_path,
                &format!("yeet-init-error read cmdline: {err}\n"),
            );
            String::new()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;

    #[test]
    fn parses_cmdline_values() {
        let args = parse_cmdline(
            "console=ttyS0 rw init=/usr/local/lib/yeet-vm/yeet-init yeet.hostname=devbox flag",
        );
        assert_eq!(args.get("console"), Some(&"ttyS0".to_string()));
        assert_eq!(args.get("yeet.hostname"), Some(&"devbox".to_string()));
        assert_eq!(args.get("flag"), Some(&String::new()));
    }

    #[test]
    fn builds_boot_config_defaults() {
        let cfg = BootConfig::from_cmdline("console=ttyS0 yeet.hostname=devbox");
        assert_eq!(cfg.hostname.as_deref(), Some("devbox"));
        assert_eq!(cfg.interface, "eth0");
    }

    #[test]
    fn builds_boot_config_with_interface() {
        let cfg = BootConfig::from_cmdline("yeet.hostname=devbox yeet.iface=eth1");
        assert_eq!(cfg.hostname.as_deref(), Some("devbox"));
        assert_eq!(cfg.interface, "eth1");
    }

    #[test]
    fn parses_ip_addr_output_for_matching_interface() {
        let raw = "2: eth0    inet 10.0.4.178/24 brd 10.0.4.255 scope global eth0\n3: eth1    inet 192.168.1.10/24 scope global eth1\n";
        assert_eq!(
            parse_ip_addr_output(raw, "eth0").as_deref(),
            Some("10.0.4.178")
        );
        assert_eq!(
            parse_ip_addr_output(raw, "eth1").as_deref(),
            Some("192.168.1.10")
        );
        assert_eq!(parse_ip_addr_output(raw, "eth2"), None);
    }

    #[test]
    fn formats_serial_ip_line() {
        assert_eq!(
            serial_ip_line("eth0", "10.0.4.178"),
            "yeet-ip eth0 10.0.4.178\n"
        );
    }

    #[test]
    fn missing_cmdline_falls_back_to_empty_and_logs_error() {
        let dir = std::env::temp_dir().join(format!("yeet-init-test-{}", std::process::id()));
        fs::create_dir_all(&dir).expect("create temp dir");
        let missing_cmdline = dir.join("missing-cmdline");
        let serial = dir.join("serial");
        fs::write(&serial, "").expect("create serial file");

        let got = read_cmdline_or_empty(&missing_cmdline, &serial);

        assert_eq!(got, "");
        let log = fs::read_to_string(&serial).expect("read serial file");
        assert!(log.contains("yeet-init-error read cmdline:"));
    }

    #[test]
    fn hostname_failure_logs_and_still_reports_ip() {
        let writes = RefCell::new(Vec::new());
        let set_hostname_calls = RefCell::new(Vec::new());

        run_before_systemd_with(
            "yeet.hostname=bad-host yeet.iface=eth9",
            Path::new("/dev/test-serial"),
            || Ok(()),
            |name| {
                set_hostname_calls.borrow_mut().push(name.to_string());
                Err(io::Error::new(io::ErrorKind::PermissionDenied, "no cap"))
            },
            |iface, timeout| {
                assert_eq!(iface, "eth9");
                assert_eq!(timeout, Duration::from_millis(1500));
                Some("10.0.4.9".to_string())
            },
            |path, line| {
                assert_eq!(path, Path::new("/dev/test-serial"));
                writes.borrow_mut().push(line.to_string());
                Ok(())
            },
        )
        .expect("run before systemd");

        assert_eq!(set_hostname_calls.borrow().as_slice(), ["bad-host"]);
        let writes = writes.borrow();
        assert_eq!(writes.len(), 2);
        assert!(writes[0].contains("yeet-init-error set hostname:"));
        assert_eq!(writes[1], "yeet-ip eth9 10.0.4.9\n");
    }
}
