# Yeet Init Fast VM Boot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and publish a faster Ubuntu 26.04 VM image path with Rust `yeet-init`, kernel-level first-interface networking, early systemd-managed SSH readiness, and correct guest reboot behavior, then prove the improvement on `lab-host`.

**Architecture:** Keep `catch` authoritative for VM orchestration and systemd unit rendering. Put the guest shim in a small Rust crate under `guest/yeet-init`, embed it into the image bundle through the `yeet-vm-images` GitHub workflow, and keep guest readiness as a serial `yeet-ready <iface> <ip>` marker emitted by a systemd service ordered after a yeet-managed SSH service. Avoid a prewarm command and avoid TCP probing in `yeet run`.

**Tech Stack:** Go, Rust, Cargo, mise, Bash, systemd, Firecracker, Linux direct kernel boot, GitHub Actions, ZFS live validation on `lab-host`.

---

## Current Baseline

- Current fast image bundle: `ubuntu-26.04-amd64-v3`.
- Measured yeet VM boot profile on the current fast image: `901ms kernel + 3.277s userspace = 4.178s`, `graphical.target` at `3.276s`, host readiness around `4.668s`.
- Slower units observed in the guest include `pollinate.service`, `netplan-configure.service`, cloud-init-related units, and stock SSH ordering through `network-online.target`.
- The exe.dev comparison VM boots around `494ms kernel + 493ms userspace = 988ms`; it uses direct kernel networking and a custom init path, but yeet should keep SSH systemd-managed for long-lived Ubuntu VMs.
- Current reboot bug: `sudo reboot` inside a guest makes Firecracker exit cleanly and leaves the host `yeet-vm-$svc.service` inactive instead of restarting the VM service.

## Success Criteria

- `yeet run "$svc@yeet-lab" vm://ubuntu/26.04 --service-root="flash/yeet/vms/$svc" --zfs --disk=128g --net=lan` returns only after `yeet-ready <iface> <ip>` has appeared after the current unit start.
- An immediate `yeet ssh "$svc@yeet-lab" -- true` succeeds after `yeet run` returns.
- `sudo reboot` inside the VM restarts the host VM unit instead of leaving it inactive.
- The new live measurement report includes:
  - wall time from command start to first output;
  - wall time for full `yeet run`;
  - host readiness wait duration from `YEET_TRACE=1`;
  - `systemd-analyze` output from inside the guest;
  - `systemd-analyze blame` top entries;
  - `dmesg` boot-time tail;
  - `yeet rm --clean-data` wall time and no leftover service ZFS dataset.
- Target: guest `systemd-analyze` stays under 5s and trends toward 2s. If the live result misses the target, keep the implementation and commit the measurement explaining the remaining top blockers.

## File Structure

### Root repo: `/Users/shayne/code/yeet`

- Modify `.mise.toml`: add Rust tooling and guest-init tasks.
- Create `guest/yeet-init/Cargo.toml`: Rust crate manifest.
- Create `guest/yeet-init/src/lib.rs`: testable init parsing, hostname, first-interface IP reporting, and systemd exec helpers.
- Create `guest/yeet-init/src/main.rs`: thin binary wrapper.
- Modify `pkg/catch/vm_console_proxy.go`: classify guest halt versus reboot from serial output.
- Modify `pkg/catch/vm_console_test.go`: cover halt, poweroff, reboot, and chunked serial output.
- Modify `cmd/catch/catch.go`: make `vm-run` exit with a stable reboot code when the guest requests reboot.
- Modify `cmd/catch/catch_test.go`: cover `vm-run` reboot exit handling without exiting the test process.
- Modify `pkg/catch/vm_systemd.go`: document and force restart on the reboot exit code.
- Modify `pkg/catch/vm_systemd_test.go`: assert the restart policy includes the reboot exit code.
- Create `pkg/catch/vm_boot.go`: render VM kernel boot args, including `init=/usr/local/lib/yeet-vm/yeet-init` and first-interface `ip=` args.
- Create `pkg/catch/vm_boot_test.go`: cover LAN DHCP and svc static kernel args.
- Modify `pkg/catch/vm_provision.go`: use `vmKernelBootArgs`.
- Modify `pkg/catch/vm_provision_test.go`: assert generated Firecracker config contains the new init and network boot args.
- Modify `pkg/catch/vm_metadata.go`: switch guest networking from netplan injection to systemd-networkd files, add `yeet-sshd.service`, shorten readiness ordering, and mask stock SSH units.
- Modify `pkg/catch/vm_metadata_test.go`: assert networkd, `yeet-sshd`, readiness, stock SSH masks, and removal of `network-online.target` dependency.
- Modify `tools/vm-image/build-linux-kernel.sh`: enable kernel IP autoconfiguration and DHCP.
- Modify `tools/vm-image/build-ubuntu-26.04.sh`: require and install `YEET_VM_INIT_PATH`, prune slow guest units/packages, enable networkd/resolved, default to `ubuntu-26.04-amd64-v4`.
- Modify `tools/vm-image/README.md`: document v4, Rust init embedding, no snap support, and yeet-managed kernel policy.
- Modify `pkg/catch/vm_image.go` and tests that assert `ubuntu-26.04-amd64-v3`: default to `ubuntu-26.04-amd64-v4`.
- Modify `README.md` and `website/docs/cli/yeet-cli.mdx` or `website/docs/concepts/service-types.mdx`: document that fast VMs boot a yeet-managed kernel/init path and do not support snap packages.

### Image builder repo: `/Users/shayne/code/yeet-vm-images`

- Modify `.github/workflows/build-ubuntu-26.04.yml`: add `yeet_ref`, check out `yeetrun/yeet`, install mise/Rust, build `guest/yeet-init`, pass `YEET_VM_INIT_PATH`, and verify v4 manifest/rootfs.
- Modify `scripts/build-linux-kernel.sh`: mirror the root kernel script changes.
- Modify `scripts/build-ubuntu-26.04.sh`: mirror the root image script changes.
- Modify `README.md`: mirror the root image README behavior notes.

## Task 1: Fix Guest Reboot Handling

**Files:**
- Modify: `pkg/catch/vm_console_proxy.go`
- Modify: `pkg/catch/vm_console_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`
- Modify: `pkg/catch/vm_systemd.go`
- Modify: `pkg/catch/vm_systemd_test.go`

- [ ] **Step 1: Add failing console classification tests**

Add these tests to `pkg/catch/vm_console_test.go`:

```go
func TestVMGuestShutdownLogClassifiesShutdownKinds(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  vmGuestStopKind
	}{
		{name: "halt", input: []string{"[ 1.0] reboot: System halted\n"}, want: vmGuestStopHalt},
		{name: "power down", input: []string{"[ 1.0] reboot: Power down\n"}, want: vmGuestStopHalt},
		{name: "reboot", input: []string{"[ 1.0] reboot: Restarting system\n"}, want: vmGuestStopReboot},
		{name: "chunked reboot", input: []string{"[ 1.0] reboot: Restart", "ing system\n"}, want: vmGuestStopReboot},
		{name: "ordinary output", input: []string{"Welcome to Ubuntu\n"}, want: vmGuestStopNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log vmGuestShutdownLog
			got := vmGuestStopNone
			for _, chunk := range tt.input {
				got = log.observe([]byte(chunk))
			}
			if got != tt.want {
				t.Fatalf("shutdown kind = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunVMConsoleProxyReturnsRebootErrorWhenGuestRestarts(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf '[ 1.0] reboot: Restarting system\n'
sleep 30
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := RunVMConsoleProxy(ctx, VMConsoleProxyConfig{
		Firecracker:   fakeFirecracker,
		APISocket:     filepath.Join(dir, "firecracker.sock"),
		ConfigFile:    filepath.Join(dir, "firecracker.json"),
		ConsoleSocket: filepath.Join(dir, "serial.sock"),
	})
	if !errors.Is(err, ErrVMGuestReboot) {
		t.Fatalf("RunVMConsoleProxy error = %v, want ErrVMGuestReboot", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestVMGuestShutdownLogClassifiesShutdownKinds|TestRunVMConsoleProxyReturnsRebootErrorWhenGuestRestarts' -count=1
```

Expected: fail because `vmGuestStopKind` and `ErrVMGuestReboot` do not exist and reboot text is not classified.

- [ ] **Step 3: Implement reboot classification**

In `pkg/catch/vm_console_proxy.go`, add the error and kind constants near the config type:

```go
var ErrVMGuestReboot = errors.New("VM guest requested reboot")

const VMGuestRebootExitCode = 75

type vmGuestStopKind int

const (
	vmGuestStopNone vmGuestStopKind = iota
	vmGuestStopHalt
	vmGuestStopReboot
)
```

Update the imports to include `errors`.

Change the guest stop channel through `RunVMConsoleProxy`, `newVMConsoleBroker`, and `waitVMConsoleProcess` from `chan struct{}` to buffered `chan vmGuestStopKind` with capacity 1. Replace `waitVMConsoleProcess`, `stopVMConsoleProcessOnGuestStop`, and `vmGuestStopped` so the waiter owns the observed shutdown kind:

```go
func waitVMConsoleProcess(cmd *exec.Cmd, guestStopped <-chan vmGuestStopKind) error {
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case kind := <-guestStopped:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		err := <-waitDone
		return vmGuestStopError(kind, err)
	case err := <-waitDone:
		select {
		case kind := <-guestStopped:
			return vmGuestStopError(kind, err)
		default:
		}
		if err != nil {
			return fmt.Errorf("wait for Firecracker: %w", err)
		}
		return nil
	}
}

func vmGuestStopError(kind vmGuestStopKind, err error) error {
	switch kind {
	case vmGuestStopReboot:
		return ErrVMGuestReboot
	case vmGuestStopHalt:
		return nil
	}
	if err != nil {
		return fmt.Errorf("wait for Firecracker: %w", err)
	}
	return nil
}
```

Create the channel in `RunVMConsoleProxy` with capacity 1:

```go
guestStopped := make(chan vmGuestStopKind, 1)
```

Update `newVMConsoleBroker` to accept `chan vmGuestStopKind`, and update the broker field type:

```go
guestStopped chan vmGuestStopKind
```

Change `vmConsoleBroker.write` to send the observed kind and close the channel:

```go
func (b *vmConsoleBroker) write(p []byte) {
	_, _ = b.output.Write(p)
	if kind := b.shutdownLog.observe(p); kind != vmGuestStopNone {
		b.guestStopOnce.Do(func() {
			b.guestStopped <- kind
			close(b.guestStopped)
		})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for conn := range b.clients {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(p); err != nil {
			delete(b.clients, conn)
			_ = conn.Close()
		}
	}
}
```

Replace shutdown text detection with:

```go
func (l *vmGuestShutdownLog) observe(p []byte) vmGuestStopKind {
	text := l.tail + string(p)
	if kind := vmGuestShutdownKind(text); kind != vmGuestStopNone {
		return kind
	}
	l.tail = vmGuestShutdownTail(text)
	return vmGuestStopNone
}

func vmGuestShutdownKind(text string) vmGuestStopKind {
	text = strings.ToLower(text)
	switch {
	case strings.Contains(text, "reboot: restarting system"):
		return vmGuestStopReboot
	case strings.Contains(text, "reboot: system halted"),
		strings.Contains(text, "reboot: power down"):
		return vmGuestStopHalt
	default:
		return vmGuestStopNone
	}
}
```

- [ ] **Step 4: Add failing `cmd/catch` reboot exit test**

Add this test to `cmd/catch/catch_test.go` after `TestHandleSpecialCommandVMRun`:

```go
func TestHandleSpecialCommandVMRunExitsWithRebootCode(t *testing.T) {
	oldRun := runVMConsoleProxy
	oldExit := exitProcess
	t.Cleanup(func() {
		runVMConsoleProxy = oldRun
		exitProcess = oldExit
	})

	var exitCode int
	runVMConsoleProxy = func(context.Context, catch.VMConsoleProxyConfig) error {
		return catch.ErrVMGuestReboot
	}
	exitProcess = func(code int) {
		exitCode = code
		panic("exit intercepted")
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected intercepted exit")
		}
		if recovered != "exit intercepted" {
			t.Fatalf("panic = %#v, want exit intercepted", recovered)
		}
		if exitCode != catch.VMGuestRebootExitCode {
			t.Fatalf("exit code = %d, want %d", exitCode, catch.VMGuestRebootExitCode)
		}
	}()

	_, _ = handleSpecialCommand([]string{"vm-run", "--firecracker", "/fc", "--api-sock", "/api", "--config-file", "/cfg", "--console-sock", "/serial"}, io.Discard)
}
```

- [ ] **Step 5: Run test to verify it fails**

Run:

```bash
go test ./cmd/catch -run TestHandleSpecialCommandVMRunExitsWithRebootCode -count=1
```

Expected: fail because `exitProcess` does not exist and reboot is returned as a normal error.

- [ ] **Step 6: Implement reboot exit handling**

In `cmd/catch/catch.go`, change the globals to:

```go
var (
	runVMConsoleProxy = catch.RunVMConsoleProxy
	exitProcess       = os.Exit
)
```

Change the tail of `handleVMRunCommand` to:

```go
err := runVMConsoleProxy(context.Background(), catch.VMConsoleProxyConfig{
	Firecracker:   *firecracker,
	APISocket:     *apiSock,
	ConfigFile:    *configFile,
	ConsoleSocket: *consoleSock,
})
if errors.Is(err, catch.ErrVMGuestReboot) {
	exitProcess(catch.VMGuestRebootExitCode)
	return nil
}
return err
```

- [ ] **Step 7: Force systemd restart for the reboot exit code**

In `pkg/catch/vm_systemd.go`, add this line after `Restart=on-failure`:

```ini
RestartForceExitStatus=75
```

In `pkg/catch/vm_systemd_test.go`, add `"RestartForceExitStatus=75"` to the `want` list.

- [ ] **Step 8: Run reboot tests**

Run:

```bash
go test ./pkg/catch ./cmd/catch -run 'TestVMGuestShutdownLogClassifiesShutdownKinds|TestRunVMConsoleProxyStopsWhenGuestHalts|TestRunVMConsoleProxyReturnsRebootErrorWhenGuestRestarts|TestHandleSpecialCommandVMRun|TestHandleSpecialCommandVMRunExitsWithRebootCode|TestRenderVMSystemdUnit' -count=1
```

Expected: pass.

- [ ] **Step 9: Commit**

```bash
git add pkg/catch/vm_console_proxy.go pkg/catch/vm_console_test.go cmd/catch/catch.go cmd/catch/catch_test.go pkg/catch/vm_systemd.go pkg/catch/vm_systemd_test.go
git commit -m "vm: restart guests after reboot requests"
```

## Task 2: Add Rust Tooling and `yeet-init`

**Files:**
- Modify: `.mise.toml`
- Create: `guest/yeet-init/Cargo.toml`
- Create: `guest/yeet-init/src/lib.rs`
- Create: `guest/yeet-init/src/main.rs`

- [ ] **Step 1: Add Rust tooling to mise**

Modify `.mise.toml` `[tools]` to include Rust:

```toml
[tools]
go = "1.26.4"
rust = "stable"
"go:golang.org/x/vuln/cmd/govulncheck" = "latest"
golangci-lint = "2.12.2"
pre-commit = "latest"
staticcheck = "latest"
```

Add these tasks after `tasks.test`:

```toml
[tasks."guest:init:test"]
description = "Run yeet-init Rust tests"
run = "cargo test --manifest-path guest/yeet-init/Cargo.toml"

[tasks."guest:init:build"]
description = "Build static yeet-init for the VM image"
run = '''
#!/usr/bin/env bash
set -euo pipefail

rustup target add x86_64-unknown-linux-musl
cargo build --manifest-path guest/yeet-init/Cargo.toml --release --target x86_64-unknown-linux-musl
'''
```

- [ ] **Step 2: Create the crate manifest**

Create `guest/yeet-init/Cargo.toml`:

```toml
[package]
name = "yeet-init"
version = "0.1.0"
edition = "2024"
license = "BSD-2-Clause"

[dependencies]
libc = "0.2"
```

- [ ] **Step 3: Create failing Rust tests and initial library**

Create `guest/yeet-init/src/lib.rs` with this content:

```rust
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
    let c_name = CString::new(name).map_err(|_| {
        io::Error::new(io::ErrorKind::InvalidInput, "hostname contains a nul byte")
    })?;
    let rc = unsafe { libc::sethostname(c_name.as_ptr(), name.len()) };
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
    let cfg = BootConfig::from_cmdline(cmdline);
    prepare_runtime_dirs()?;
    if let Some(hostname) = cfg.hostname.as_deref() {
        if !hostname.is_empty() {
            set_hostname(hostname)?;
        }
    }
    if let Some(ip) = wait_for_ipv4(&cfg.interface, Duration::from_millis(1500)) {
        let _ = write_serial(Path::new(SERIAL_TTY), &serial_ip_line(&cfg.interface, &ip));
    }
    Ok(())
}

pub fn exec_systemd() -> io::Error {
    Command::new(DEFAULT_SYSTEMD)
        .arg("--unit=multi-user.target")
        .exec()
}

pub fn run() -> io::Result<()> {
    let cmdline = fs::read_to_string("/proc/cmdline")?;
    if let Err(err) = run_before_systemd(&cmdline) {
        let _ = write_serial(Path::new(SERIAL_TTY), &format!("yeet-init-error {err}\n"));
    }
    Err(exec_systemd())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_cmdline_values() {
        let args = parse_cmdline("console=ttyS0 rw init=/usr/local/lib/yeet-vm/yeet-init yeet.hostname=devbox flag");
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
        assert_eq!(parse_ip_addr_output(raw, "eth0").as_deref(), Some("10.0.4.178"));
        assert_eq!(parse_ip_addr_output(raw, "eth1").as_deref(), Some("192.168.1.10"));
        assert_eq!(parse_ip_addr_output(raw, "eth2"), None);
    }

    #[test]
    fn formats_serial_ip_line() {
        assert_eq!(serial_ip_line("eth0", "10.0.4.178"), "yeet-ip eth0 10.0.4.178\n");
    }
}
```

Create `guest/yeet-init/src/main.rs`:

```rust
fn main() {
    if let Err(err) = yeet_init::run() {
        eprintln!("yeet-init: {err}");
        std::process::exit(1);
    }
}
```

- [ ] **Step 4: Run Rust tests**

Run:

```bash
mise install
mise run guest:init:test
```

Expected: pass.

- [ ] **Step 5: Build static init binary**

Run:

```bash
mise run guest:init:build
file guest/yeet-init/target/x86_64-unknown-linux-musl/release/yeet-init
```

Expected: `file` reports an x86-64 ELF executable.

- [ ] **Step 6: Commit**

```bash
git add .mise.toml guest/yeet-init
git commit -m "guest: add rust yeet init shim"
```

## Task 3: Wire Fast Guest Boot Args and Systemd Readiness

**Files:**
- Create: `pkg/catch/vm_boot.go`
- Create: `pkg/catch/vm_boot_test.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `pkg/catch/vm_metadata.go`
- Modify: `pkg/catch/vm_metadata_test.go`

- [ ] **Step 1: Add failing VM boot arg tests**

Create `pkg/catch/vm_boot_test.go`:

```go
package catch

import (
	"strings"
	"testing"
)

func TestVMKernelBootArgsIncludesInitAndDHCPForLAN(t *testing.T) {
	network := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "br0", LANParentIsBridge: true})
	got := vmKernelBootArgs("devbox", network)
	for _, want := range []string{
		"console=ttyS0",
		"root=/dev/vda",
		"rw",
		"init=/usr/local/lib/yeet-vm/yeet-init",
		"ip=dhcp",
		"yeet.hostname=devbox",
		"yeet.iface=eth0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("boot args missing %q: %s", want, got)
		}
	}
}

func TestVMKernelBootArgsIncludesStaticSvcIP(t *testing.T) {
	network := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	got := vmKernelBootArgs("devbox", network)
	want := "ip=192.168.100.12::192.168.100.254:255.255.255.0:devbox:eth0:none"
	if !strings.Contains(got, want) {
		t.Fatalf("boot args = %s, want %s", got, want)
	}
}

func TestIPv4PrefixMask(t *testing.T) {
	tests := map[int]string{
		8:  "255.0.0.0",
		16: "255.255.0.0",
		24: "255.255.255.0",
		30: "255.255.255.252",
		32: "255.255.255.255",
	}
	for bits, want := range tests {
		got, ok := ipv4PrefixMask(bits)
		if !ok {
			t.Fatalf("ipv4PrefixMask(%d) not ok", bits)
		}
		if got != want {
			t.Fatalf("ipv4PrefixMask(%d) = %s, want %s", bits, got, want)
		}
	}
	if _, ok := ipv4PrefixMask(33); ok {
		t.Fatal("ipv4PrefixMask(33) ok, want false")
	}
}
```

- [ ] **Step 2: Run boot arg tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestVMKernelBootArgs|TestIPv4PrefixMask' -count=1
```

Expected: fail because `vmKernelBootArgs` and `ipv4PrefixMask` do not exist.

- [ ] **Step 3: Implement VM boot arg renderer**

Create `pkg/catch/vm_boot.go`:

```go
package catch

import (
	"fmt"
	"net/netip"
	"strings"
)

const vmGuestInitPath = "/usr/local/lib/yeet-vm/yeet-init"

func vmKernelBootArgs(service string, network vmNetworkPlan) string {
	args := []string{
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"pci=off",
		"root=/dev/vda",
		"rw",
		"init=" + vmGuestInitPath,
	}
	if ipArg := vmKernelIPArg(service, network); ipArg != "" {
		args = append(args, ipArg)
	}
	if service != "" {
		args = append(args, "yeet.hostname="+service)
	}
	if len(network.Interfaces) > 0 && network.Interfaces[0].GuestName != "" {
		args = append(args, "yeet.iface="+network.Interfaces[0].GuestName)
	}
	return strings.Join(args, " ")
}

func vmKernelIPArg(service string, network vmNetworkPlan) string {
	if len(network.Interfaces) == 0 {
		return ""
	}
	iface := network.Interfaces[0]
	if iface.DHCP {
		return "ip=dhcp"
	}
	if iface.GuestIP == "" {
		return ""
	}
	prefix, err := netip.ParsePrefix(iface.GuestIP)
	if err != nil || !prefix.Addr().Is4() {
		return ""
	}
	mask, ok := ipv4PrefixMask(prefix.Bits())
	if !ok {
		return ""
	}
	return fmt.Sprintf("ip=%s::%s:%s:%s:%s:none", prefix.Addr(), iface.Gateway, mask, service, iface.GuestName)
}

func ipv4PrefixMask(bits int) (string, bool) {
	if bits < 0 || bits > 32 {
		return "", false
	}
	var mask uint32
	if bits == 0 {
		mask = 0
	} else {
		mask = ^uint32(0) << uint(32-bits)
	}
	return fmt.Sprintf("%d.%d.%d.%d", byte(mask>>24), byte(mask>>16), byte(mask>>8), byte(mask)), true
}
```

- [ ] **Step 4: Use the renderer in provisioning**

In `pkg/catch/vm_provision.go`, replace:

```go
BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw",
```

with:

```go
BootArgs:        vmKernelBootArgs(e.sn, networkPlan),
```

In `pkg/catch/vm_provision_test.go`, add these assertions to `TestRunVMProvisionSuccessWritesArtifactsAndDB` after the Firecracker config assertions:

```go
assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "init=/usr/local/lib/yeet-vm/yeet-init")
assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "ip=192.168.100.")
assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "yeet.hostname=svc")
```

- [ ] **Step 5: Add failing metadata tests for networkd and yeet SSH**

Update `TestWriteVMGuestMetadataFiles` in `pkg/catch/vm_metadata_test.go`:

```go
assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "[Match]")
assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "Name=eth0")
assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "Address=192.168.100.12/24")
assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-sshd.service"), "ExecStart=/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config")
assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "After=yeet-sshd.service")
assertFileNotContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "network-online.target")
```

Replace the existing `ssh.service` enable symlink assertion with:

```go
if _, err := os.Lstat(filepath.Join(root, "etc", "systemd", "system", "multi-user.target.wants", "yeet-sshd.service")); err != nil {
	t.Fatalf("yeet-sshd enable symlink missing: %v", err)
}
for _, unit := range []string{"ssh.service", "ssh.socket"} {
	target, err := os.Readlink(filepath.Join(root, "etc", "systemd", "system", unit))
	if err != nil || target != "/dev/null" {
		t.Fatalf("%s mask = %q, %v; want /dev/null", unit, target, err)
	}
}
```

Add this helper near the other file assertion helpers:

```go
func assertFileNotContains(t *testing.T, path, needle string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(raw), needle) {
		t.Fatalf("%s unexpectedly contains %q:\n%s", path, needle, string(raw))
	}
}
```

- [ ] **Step 6: Run metadata test to verify it fails**

Run:

```bash
go test ./pkg/catch -run TestWriteVMGuestMetadataFiles -count=1
```

Expected: fail because metadata still writes netplan, stock `ssh.service`, and `network-online.target` readiness ordering.

- [ ] **Step 7: Implement networkd, yeet SSH, and faster readiness ordering**

In `pkg/catch/vm_metadata.go`, change `writeVMGuestBaseFiles` to write hostname plus systemd-networkd files:

```go
func writeVMGuestBaseFiles(root string, cfg vmMetadataConfig) error {
	if err := writeVMGuestFile(root, "etc/hostname", []byte(cfg.Hostname+"\n"), 0o644); err != nil {
		return err
	}
	for _, network := range cfg.Networks {
		rel := filepath.Join("etc", "systemd", "network", "10-yeet-"+network.Name+".network")
		if err := writeVMGuestFile(root, rel, []byte(renderVMNetworkdUnit(network)), 0o644); err != nil {
			return err
		}
	}
	return nil
}
```

Add:

```go
func renderVMNetworkdUnit(network vmGuestNetwork) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Match]\nName=%s\n\n[Network]\n", network.Name)
	if network.DHCP {
		b.WriteString("DHCP=ipv4\n")
	} else {
		fmt.Fprintf(&b, "Address=%s\n", network.Address)
		if network.Gateway != "" {
			fmt.Fprintf(&b, "Gateway=%s\n", network.Gateway)
		}
	}
	for _, ns := range vmGuestNetworkNameservers(network) {
		fmt.Fprintf(&b, "DNS=%s\n", ns)
	}
	return b.String()
}
```

Change `writeVMGuestReadyUnit` to write and enable `yeet-sshd.service`, mask stock SSH, and enable readiness:

```go
func writeVMGuestReadyUnit(root string) error {
	if err := writeVMGuestFile(root, "usr/local/lib/yeet-vm/guest-ready", []byte(vmGuestReadyScript), 0o755); err != nil {
		return err
	}
	if err := writeVMGuestFile(root, "etc/systemd/system/yeet-sshd.service", []byte(vmGuestSSHDService), 0o644); err != nil {
		return err
	}
	if err := writeVMGuestFile(root, "etc/systemd/system/yeet-guest-ready.service", []byte(vmGuestReadyService), 0o644); err != nil {
		return err
	}
	if err := writeVMGuestSystemdSymlink(root, "multi-user.target.wants/yeet-sshd.service", "../yeet-sshd.service"); err != nil {
		return err
	}
	if err := writeVMGuestSystemdSymlink(root, "multi-user.target.wants/yeet-guest-ready.service", "../yeet-guest-ready.service"); err != nil {
		return err
	}
	if err := maskVMGuestSystemdUnit(root, "ssh.service"); err != nil {
		return err
	}
	return maskVMGuestSystemdUnit(root, "ssh.socket")
}
```

Replace `vmGuestReadyService` with:

```go
const vmGuestReadyService = `[Unit]
Description=yeet-ready guest marker
After=yeet-sshd.service
Wants=yeet-sshd.service

[Service]
Type=oneshot
ExecStart=/usr/local/lib/yeet-vm/guest-ready
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`
```

Add:

```go
const vmGuestSSHDService = `[Unit]
Description=yeet early SSH daemon
DefaultDependencies=no
After=local-fs.target systemd-sysusers.service network.target
Before=multi-user.target
Wants=network.target
ConditionPathExists=/usr/sbin/sshd

[Service]
Type=exec
RuntimeDirectory=sshd
ExecStartPre=/usr/sbin/sshd -t
ExecStart=/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`
```

Replace the top of `vmGuestReadyScript` with a faster first loop:

```sh
for _ in $(seq 1 100); do
	report="$(ip -o -4 addr show scope global 2>/dev/null | awk '{ split($4, a, "/"); print $2 " " a[1] }')"
	if [ -n "$report" ]; then
		printf '%s\n' "$report" | while read -r iface ip; do
			printf 'yeet-ip %s %s\n' "$iface" "$ip" >/dev/ttyS0
		done
		first="$(printf '%s\n' "$report" | head -n 1)"
		set -- $first
		printf 'yeet-ready %s %s\n' "$1" "$2" >/dev/ttyS0
		command -v logger >/dev/null && logger "yeet-ready $1 $2" || true
		exit 0
	fi
	sleep 0.05
done
i=0
while [ "$i" -lt 55 ]; do
```

Keep the existing report body inside the second loop and keep `yeet-ready-timeout`.

- [ ] **Step 8: Run focused Go tests**

Run:

```bash
gofmt -w pkg/catch/vm_boot.go pkg/catch/vm_boot_test.go pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go pkg/catch/vm_metadata.go pkg/catch/vm_metadata_test.go
go test ./pkg/catch -run 'TestVMKernelBootArgs|TestIPv4PrefixMask|TestRunVMProvisionSuccessWritesArtifactsAndDB|TestWriteVMGuestMetadataFiles' -count=1
```

Expected: pass.

- [ ] **Step 9: Commit**

```bash
git add pkg/catch/vm_boot.go pkg/catch/vm_boot_test.go pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go pkg/catch/vm_metadata.go pkg/catch/vm_metadata_test.go
git commit -m "vm: boot guests with yeet init readiness"
```

## Task 4: Update Kernel and Image Build Scripts

**Files:**
- Modify: `tools/vm-image/build-linux-kernel.sh`
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`
- Modify: `tools/vm-image/README.md`
- Modify mirror files under `/Users/shayne/code/yeet-vm-images/scripts/`

- [ ] **Step 1: Enable kernel IP autoconfiguration**

In both kernel scripts, add these `scripts/config` lines after `--enable VIRTIO_NET`:

```bash
		--enable IP_PNP \
		--enable IP_PNP_DHCP \
```

Add these checks after `require_config CONFIG_VIRTIO_NET y`:

```bash
require_config CONFIG_IP_PNP y
require_config CONFIG_IP_PNP_DHCP y
```

- [ ] **Step 2: Require the init binary in the fast image script**

In both `build-ubuntu-26.04.sh` scripts, change the default version:

```bash
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v4}"
```

Add after `kernel_version_override`:

```bash
guest_init_path="${YEET_VM_INIT_PATH:-}"
```

In the fast-profile validation block, add:

```bash
	if [ -z "$guest_init_path" ]; then
		echo "YEET_VM_INIT_PATH is required for the fast profile" >&2
		exit 1
	fi
	if [ ! -x "$guest_init_path" ]; then
		echo "YEET_VM_INIT_PATH is not executable: $guest_init_path" >&2
		exit 1
	fi
```

- [ ] **Step 3: Install init and slim the fast rootfs**

In `write_fast_rootfs_policy_files`, keep the existing kernel/no-snap policy and append this file:

```bash
	cat >"$root/usr/share/doc/yeet-vm-image/init.md" <<'EOF'
# Yeet VM Init

Fast yeet VM images boot through `/usr/local/lib/yeet-vm/yeet-init`.
The init shim performs small pre-systemd setup, reports the first kernel
configured IPv4 address on the serial console, and then execs systemd as PID 1.

SSH remains managed by systemd through `yeet-sshd.service`; `yeet run` waits for
the systemd-backed `yeet-guest-ready.service` marker before returning.
EOF
```

After mounting the rootfs and before the chroot block, install the init:

```bash
	install -d -m 0755 "$rootfs_mount/usr/local/lib/yeet-vm"
	install -m 0755 "$guest_init_path" "$rootfs_mount/usr/local/lib/yeet-vm/yeet-init"
```

Replace the package purge expression in the chroot block with:

```sh
packages="$(dpkg-query -W -f='${binary:Package}\n' 2>/dev/null | awk '/^(linux-image-|linux-modules-|linux-modules-extra-|linux-headers-|linux-generic|linux-virtual|grub-|shim-signed$|initramfs-tools|snapd$|snap-confine$|squashfs-tools$|cloud-init$|pollinate$|apport$|apport-symptoms$|modemmanager$|udisks2$|multipath-tools$|lvm2$|rsyslog$|ufw$|unattended-upgrades$|open-vm-tools$|open-vm-tools-desktop$|vgauth$)/ { print }')"
```

After the snap masks, add:

```sh
rm -rf /etc/netplan
mkdir -p /etc/systemd/system/multi-user.target.wants /etc/systemd/system/timers.target.wants /etc/systemd/network
ln -sf /usr/lib/systemd/system/systemd-networkd.service /etc/systemd/system/multi-user.target.wants/systemd-networkd.service
ln -sf /usr/lib/systemd/system/systemd-resolved.service /etc/systemd/system/multi-user.target.wants/systemd-resolved.service
ln -sf /usr/lib/systemd/system/multi-user.target /etc/systemd/system/default.target
for unit in \
	apt-daily.timer \
	apt-daily-upgrade.timer \
	e2scrub_all.timer \
	fstrim.timer \
	man-db.timer \
	motd-news.timer \
	pollinate.service \
	cloud-init.service \
	cloud-config.service \
	cloud-final.service \
	NetworkManager.service \
	NetworkManager-wait-online.service \
	systemd-networkd-wait-online.service
do
	ln -sf /dev/null "/etc/systemd/system/$unit"
done
ldconfig
```

- [ ] **Step 4: Add manifest fields for init provenance**

In both image scripts, set:

```bash
guest_init_sha="$(sha256sum "$guest_init_path" | awk '{ print $1 }')"
```

Add these manifest fields after `"snap_support": $snap_support,`:

```json
  "guest_init": "/usr/local/lib/yeet-vm/yeet-init",
  "guest_init_sha256": "$guest_init_sha",
```

- [ ] **Step 5: Update image README**

Update `tools/vm-image/README.md` and `/Users/shayne/code/yeet-vm-images/README.md`:

```md
The current fast bundle version is `ubuntu-26.04-amd64-v4`. It is built from
the official Ubuntu 26.04 cloud image, boots a yeet-managed kernel under
Firecracker direct kernel boot, uses `/usr/local/lib/yeet-vm/yeet-init` as the
pre-systemd init shim, and omits `initrd.img`.
```

Add to the Fast Profile list:

```md
- installs the Rust `yeet-init` binary into `/usr/local/lib/yeet-vm/yeet-init`;
- enables kernel IP autoconfiguration for the first VM interface;
- uses systemd-networkd and `yeet-sshd.service` instead of netplan and the
  stock `ssh.service` for VM readiness;
- purges cloud-init, pollinate, and other server-image services that do not
  contribute to yeet VM boot.
```

- [ ] **Step 6: Validate shell scripts**

Run:

```bash
bash -n tools/vm-image/build-linux-kernel.sh tools/vm-image/build-ubuntu-26.04.sh
bash -n /Users/shayne/code/yeet-vm-images/scripts/build-linux-kernel.sh /Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh
```

Expected: no output and exit code 0.

- [ ] **Step 7: Commit root script changes**

```bash
git add tools/vm-image/build-linux-kernel.sh tools/vm-image/build-ubuntu-26.04.sh tools/vm-image/README.md
git commit -m "vm-image: embed yeet init in fast image"
```

## Task 5: Update Image Workflow and Version Defaults

**Files:**
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `pkg/catch/vm_images_cmd_test.go`
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`

- [ ] **Step 1: Bump default image version in catch**

In `pkg/catch/vm_image.go`, change:

```go
defaultVMImageVersion = "ubuntu-26.04-amd64-v4"
```

Update tests that hard-code `ubuntu-26.04-amd64-v3` only when they are asserting the default or latest fast image version. Leave tests that intentionally use v1/v3 as stale-version fixtures unchanged.

- [ ] **Step 2: Run version tests**

Run:

```bash
go test ./pkg/catch -run 'TestVMImage|TestVMImages|TestRunVM.*Image|TestRunVMProvisionSuccessWritesArtifactsAndDB' -count=1
```

Expected: pass.

- [ ] **Step 3: Add image workflow checkout and Rust build**

In `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`:

Change the `version` default to:

```yaml
default: ubuntu-26.04-amd64-v4
```

Add workflow input:

```yaml
      yeet_ref:
        description: yeet repository ref used to build guest/yeet-init.
        required: true
        default: main
```

Add `musl-tools` to the apt install list.

After the existing checkout step, add:

```yaml
      - name: Checkout yeet source
        uses: actions/checkout@v4
        with:
          repository: yeetrun/yeet
          ref: ${{ inputs.yeet_ref }}
          path: yeet-src

      - name: Install mise
        run: |
          curl https://mise.run | sh
          echo "$HOME/.local/bin" >> "$GITHUB_PATH"

      - name: Build yeet init
        run: |
          cd yeet-src
          "$HOME/.local/bin/mise" install
          rustup target add x86_64-unknown-linux-musl
          "$HOME/.local/bin/mise" exec -- cargo build --manifest-path guest/yeet-init/Cargo.toml --release --target x86_64-unknown-linux-musl
          file guest/yeet-init/target/x86_64-unknown-linux-musl/release/yeet-init
```

In the `Build image bundle` step, add:

```bash
            YEET_VM_INIT_PATH="$GITHUB_WORKSPACE/yeet-src/guest/yeet-init/target/x86_64-unknown-linux-musl/release/yeet-init" \
```

In the `Verify bundle` step, add:

```bash
          jq -e '
            .guest_init == "/usr/local/lib/yeet-vm/yeet-init" and
            (.guest_init_sha256 | type == "string" and length == 64)
          ' "$OUT_DIR/manifest.json"
          grep -q '^CONFIG_IP_PNP=y$' "$OUT_DIR/kernel.config"
          grep -q '^CONFIG_IP_PNP_DHCP=y$' "$OUT_DIR/kernel.config"
          zstd -d -f --no-progress -o "$RUNNER_TEMP/rootfs.ext4" "$OUT_DIR/rootfs.ext4.zst"
          debugfs -R 'stat /usr/local/lib/yeet-vm/yeet-init' "$RUNNER_TEMP/rootfs.ext4" >/dev/null
          debugfs -R 'stat /usr/share/doc/yeet-vm-image/init.md' "$RUNNER_TEMP/rootfs.ext4" >/dev/null
```

- [ ] **Step 4: Validate image workflow YAML**

Run:

```bash
git -C /Users/shayne/code/yeet-vm-images diff -- .github/workflows/build-ubuntu-26.04.yml
```

Expected: diff shows `yeet_ref`, yeet checkout, mise install, Rust build, `YEET_VM_INIT_PATH`, and v4 verification.

- [ ] **Step 5: Commit image repo changes**

```bash
git -C /Users/shayne/code/yeet-vm-images add .github/workflows/build-ubuntu-26.04.yml scripts/build-linux-kernel.sh scripts/build-ubuntu-26.04.sh README.md
git -C /Users/shayne/code/yeet-vm-images commit -m "image: build v4 with yeet init"
```

- [ ] **Step 6: Commit root version bump**

```bash
git add pkg/catch/vm_image.go pkg/catch/vm_image_test.go pkg/catch/vm_images_cmd_test.go pkg/catch/vm_provision_test.go
git commit -m "vm: default ubuntu image to v4"
```

## Task 6: Update User-Facing Docs

**Files:**
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/concepts/service-types.mdx`

- [ ] **Step 1: Update root README VM section**

Add this paragraph near the VM example in `README.md`:

```md
The default Ubuntu VM image is optimized for Firecracker direct kernel boot. It
uses a yeet-managed kernel and init shim, starts SSH through a yeet-managed
systemd unit, and intentionally does not support snap packages in the fast
profile. Publish a new yeet VM image bundle to update the guest boot kernel or
init path.
```

- [ ] **Step 2: Update CLI docs**

In `website/docs/cli/yeet-cli.mdx`, add this note in the `vm://ubuntu/26.04` section:

```md
The fast Ubuntu VM image uses a yeet-managed kernel and init shim for
Firecracker direct boot. Snap packages are not supported in this fast image
profile. Kernel and init updates are delivered by publishing a new yeet VM image
bundle and updating the host image cache.
```

- [ ] **Step 3: Update service type docs**

In `website/docs/concepts/service-types.mdx`, add this note below the VM service example:

```md
VM services use the fast yeet Ubuntu image by default: direct kernel boot,
systemd-managed SSH readiness, and no snap support. Use Docker services for
workloads that need snap-like packaging behavior.
```

- [ ] **Step 4: Validate docs**

Run:

```bash
git diff --check README.md
git -C website diff --check
```

Expected: no output and exit code 0.

- [ ] **Step 5: Commit website docs first**

```bash
git -C website add docs/cli/yeet-cli.mdx docs/concepts/service-types.mdx
git -C website commit -m "docs: describe fast vm boot profile"
```

- [ ] **Step 6: Commit root docs and website pointer**

```bash
git add README.md website
git commit -m "docs: document fast vm boot profile"
```

## Task 7: Full Verification, GitHub Build, and Live lab-host Measurements

**Files:**
- Read: all changed files
- Write: `.tmp/vm-v4-measurements.md`

- [ ] **Step 1: Run local verification**

Run:

```bash
mise run guest:init:test
mise run guest:init:build
go test ./pkg/catch ./cmd/catch -count=1
go test ./... -count=1
pre-commit run --all-files
```

Expected: all pass.

- [ ] **Step 2: Push the image repo workflow change**

```bash
git -C /Users/shayne/code/yeet-vm-images status --short --branch
git -C /Users/shayne/code/yeet-vm-images push origin main
```

Expected: branch reports `main...origin/main` before push or only the committed local change ahead; push succeeds.

- [ ] **Step 3: Trigger the v4 image build**

From `/Users/shayne/code/yeet`, get the yeet commit:

```bash
yeet_ref="$(git rev-parse HEAD)"
gh workflow run build-ubuntu-26.04.yml \
  -R yeetrun/yeet-vm-images \
  -f version=ubuntu-26.04-amd64-v4 \
  -f yeet_ref="$yeet_ref" \
  -f overwrite_release=true
```

Then watch the latest run:

```bash
run_id="$(gh run list -R yeetrun/yeet-vm-images --workflow build-ubuntu-26.04.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch -R yeetrun/yeet-vm-images "$run_id"
```

Expected: workflow succeeds and publishes release `ubuntu-26.04-amd64-v4`.

- [ ] **Step 4: Push the root repo commits**

```bash
git status --short --branch
git push origin main
```

Expected: branch is ahead only by the commits from this plan; push succeeds.

- [ ] **Step 5: Build local yeet and install catch on lab-host**

```bash
go build -o /tmp/yeet-vmfast ./cmd/yeet
/tmp/yeet-vmfast init root@lab-host
```

Expected: catch installs successfully on `lab-host`.

- [ ] **Step 6: Run a live ZFS VM measurement**

```bash
mkdir -p .tmp
svc="bootv4-$(date +%H%M%S)"
log=".tmp/${svc}-run.log"
start="$(date +%s.%N)"
YEET_TRACE=1 /tmp/yeet-vmfast run "$svc@yeet-lab" vm://ubuntu/26.04 \
  --service-root="flash/yeet/vms/$svc" \
  --zfs \
  --disk=128g \
  --net=lan \
  --image-policy=update 2>&1 | tee "$log"
end="$(date +%s.%N)"
python3 - <<PY
start = float("$start")
end = float("$end")
print(f"run_wall_seconds={end-start:.3f}")
PY
```

Expected:

- Output shows `VM $svc is running`.
- Output does not get stuck at `Preparing disk...`.
- Trace log includes image cache/update, disk clone, network setup, start, and guest readiness timings.
- The image shown in output is `ubuntu-26.04-amd64-v4`.

- [ ] **Step 7: Verify immediate SSH and boot profile**

```bash
/tmp/yeet-vmfast ssh "$svc@yeet-lab" -- true
/tmp/yeet-vmfast ssh "$svc@yeet-lab" -- systemd-analyze | tee ".tmp/${svc}-systemd-analyze.txt"
/tmp/yeet-vmfast ssh "$svc@yeet-lab" -- systemd-analyze blame | head -n 30 | tee ".tmp/${svc}-systemd-blame.txt"
/tmp/yeet-vmfast ssh "$svc@yeet-lab" -- dmesg | tail -n 80 | tee ".tmp/${svc}-dmesg-tail.txt"
/tmp/yeet-vmfast ssh "$svc@yeet-lab" -- systemctl status yeet-sshd.service yeet-guest-ready.service --no-pager | tee ".tmp/${svc}-guest-services.txt"
```

Expected:

- First SSH command succeeds immediately.
- `systemd-analyze` is under 5s.
- `yeet-sshd.service` is active.
- `yeet-guest-ready.service` is active/exited.

- [ ] **Step 8: Verify guest reboot recovery**

```bash
/tmp/yeet-vmfast ssh "$svc@yeet-lab" -- sudo reboot || true
sleep 3
ssh root@lab-host "systemctl is-active yeet-vm-$svc.service"
timeout 45 sh -c 'until /tmp/yeet-vmfast ssh "'"$svc@yeet-lab"'" -- true >/dev/null 2>&1; do sleep 1; done'
```

Expected:

- Host unit is `active`.
- SSH returns within 45 seconds after reboot.
- `systemctl status yeet-vm-$svc.service` does not show inactive/dead after the reboot.

- [ ] **Step 9: Measure clean removal**

```bash
remove_start="$(date +%s.%N)"
printf 'y\ny\n' | /tmp/yeet-vmfast rm "$svc@yeet-lab" --clean-data
remove_end="$(date +%s.%N)"
python3 - <<PY
start = float("$remove_start")
end = float("$remove_end")
print(f"remove_wall_seconds={end-start:.3f}")
PY
ssh root@lab-host "zfs list -H -o name | grep -F 'flash/yeet/vms/$svc' || true"
```

Expected:

- Remove completes without dataset busy errors.
- Final `zfs list` command prints nothing for the service dataset.

- [ ] **Step 10: Write measurement summary**

Create `.tmp/vm-v4-measurements.md` from the evidence collected in Steps 6 through 9. The summary must include these headings and must cite the exact log file path that supplied each value:

```md
# VM v4 Measurements

## Command

## Run Timing

## Guest Boot Timing

## Immediate SSH

## Reboot Recovery

## Clean Removal

## Top Remaining Boot Costs

## Decision
```

Use `.tmp/${svc}-run.log` for the image version, trace timings, and run output. Use `.tmp/${svc}-systemd-analyze.txt`, `.tmp/${svc}-systemd-blame.txt`, `.tmp/${svc}-dmesg-tail.txt`, and `.tmp/${svc}-guest-services.txt` for guest boot and service state. Use the terminal output from Step 9 for remove wall seconds and the final `zfs list` cleanup result.

- [ ] **Step 11: Commit measurement summary if useful**

If the summary should be retained in repo history, move it to `docs/superpowers/specs/2026-06-04-vm-v4-measurements.md` and commit:

```bash
mv .tmp/vm-v4-measurements.md docs/superpowers/specs/2026-06-04-vm-v4-measurements.md
git add docs/superpowers/specs/2026-06-04-vm-v4-measurements.md
git commit -m "docs: record vm v4 boot measurements"
```

If the summary is only local work evidence, leave it in `.tmp/` and do not commit it.

## Self-Review

- Spec coverage: The plan covers Rust via mise, a Rust `yeet-init`, kernel first-interface networking, systemd-managed SSH readiness, the guest reboot fix, image workflow building through GitHub, lab-host live testing, clean removal measurement, no snap support documentation, and yeet-managed kernel documentation.
- Placeholder scan: The plan contains concrete file paths, commands, code snippets, expected results, and commit commands. It does not require a prewarming command and does not use TCP probing as the readiness mechanism.
- Type consistency: Go names introduced in tests match implementation names: `ErrVMGuestReboot`, `VMGuestRebootExitCode`, `vmGuestStopKind`, `vmKernelBootArgs`, `ipv4PrefixMask`, `vmGuestInitPath`, and `vmGuestSSHDService`. Rust names introduced in tests match implementation names: `BootConfig`, `parse_cmdline`, `parse_ip_addr_output`, and `serial_ip_line`.
