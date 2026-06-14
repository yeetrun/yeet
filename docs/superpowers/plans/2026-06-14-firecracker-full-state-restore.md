# Firecracker Full-State Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet snapshots restore <vm> <snapshot> --mode=full` restore the VM disk plus Firecracker memory/state and resume the VM under systemd.

**Architecture:** Full restore uses the existing zvol restore path for disk state, then schedules a one-shot restore request consumed by `catch vm-run` on the next systemd start. The VM runner starts a fresh Firecracker process without `--config-file`, calls `/snapshot/load` with `resume_vm: true`, and keeps the existing console proxy and systemd ownership model. The systemd unit prevents cold-boot restart after restore-load failure.

**Tech Stack:** Go, Firecracker Unix-socket API, systemd services, ZFS zvol snapshots, yeet/catch RPC, GitButler.

---

## External Constraints

- Firecracker `/snapshot/load` is pre-boot only and accepted only on a fresh Firecracker process before normal resources are configured, except logger and metrics:
  `https://github.com/firecracker-microvm/firecracker/blob/main/src/firecracker/swagger/firecracker.yaml`.
- Firecracker full snapshots contain guest memory and emulated hardware state, but disk files are user-managed. Yeet must keep restoring the zvol before loading VM state:
  `https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md?plain=1`.
- Firecracker state references external resources by exact names and paths, including tap devices and block device paths. Yeet should implement in-place restore for the same service, not full-state clone:
  `https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/versioning.md`.

## File Structure

- Modify `pkg/catch/vm_console_proxy.go:23-64,123-137`
  - Own one-shot full restore request consumption inside the systemd-owned VM runner.
  - Start Firecracker normally when no request exists.
  - Start Firecracker fresh and call `/snapshot/load` before guest execution when a request exists.
- Modify `pkg/catch/firecracker_snapshot_restore.go:12-33`
  - Keep the existing `/snapshot/load` API client and use it from the VM runner.
- Modify `pkg/catch/vm_systemd.go:9-38`
  - Add `RestartPreventExitStatus` for restore-load failures.
  - Add helpers for restore request path and existing unit upgrade.
- Modify `cmd/catch/catch.go:55-57,325-345`
  - Map restore-load failures to a non-restarting process exit code.
- Modify `pkg/catch/recovery_vm.go:155-184,250-292`
  - Replace the current validation-only full restore branch with disk restore plus scheduled Firecracker state load.
- Modify `pkg/catch/recovery_vm_test.go:1226-1268,1417-1498`
  - Replace unsupported-full tests with success and failure tests.
- Modify `pkg/catch/vm_console_test.go:172-293`
  - Add VM runner tests for one-shot restore request behavior.
- Modify `cmd/catch/catch_test.go:238-291`
  - Cover new process exit behavior and unchanged normal `vm-run` parsing.
- Modify `pkg/cli/cli.go:738-742` and `pkg/cli/cli_test.go:490-502`
  - Stop saying full restore is validation-only/refused.
- Regenerate `.codex/skills/yeet-cli/references/yeet-help-llm.md`
  - Use `tools/generate-yeet-help-llm.sh`.
- Modify `website/docs/cli/yeet-cli.mdx:570-592` and `website/docs/payloads/vms.mdx:116-165`
  - Document disk restore, full restore, and live RAM-state expectations.

---

### Task 1: VM Runner One-Shot Restore Request

**Files:**
- Modify: `pkg/catch/vm_console_proxy.go:23-64,123-137`
- Modify: `pkg/catch/firecracker_snapshot_restore.go:23-33`
- Test: `pkg/catch/vm_console_test.go:172-293`

- [ ] **Step 1: Write the failing runner test**

Add this test to `pkg/catch/vm_console_test.go` after `TestRunVMConsoleProxyBridgesPTYToSocket`:

```go
func TestRunVMConsoleProxyLoadsOneShotSnapshotRequestBeforeGuestBoot(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	fakeFirecracker := filepath.Join(dir, "firecracker")
	argvPath := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + strconv.Quote(argvPath) + "\nprintf 'restore-ready\\n'\nwhile IFS= read -r line; do\n\tif [ \"$line\" = \"quit\" ]; then exit 0; fi\ndone\n"
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	apiSocket := filepath.Join(dir, "firecracker.sock")
	requestPath := vmFullRestoreRequestPath(apiSocket)
	request := vmFullRestoreRequest{
		StatePath:  filepath.Join(dir, "state.bin"),
		MemoryPath: filepath.Join(dir, "memory.bin"),
		Resume:     true,
	}
	if err := writeVMFullRestoreRequest(requestPath, request); err != nil {
		t.Fatalf("write restore request: %v", err)
	}

	oldLoader := vmConsoleSnapshotLoader
	oldWait := vmConsoleWaitForAPISocket
	t.Cleanup(func() {
		vmConsoleSnapshotLoader = oldLoader
		vmConsoleWaitForAPISocket = oldWait
	})

	var loaded []string
	vmConsoleWaitForAPISocket = func(context.Context, string) error {
		loaded = append(loaded, "wait")
		return nil
	}
	vmConsoleSnapshotLoader = vmSnapshotLoaderFunc(func(_ context.Context, socket, statePath, memoryPath string, resume bool) error {
		loaded = append(loaded, socket, statePath, memoryPath, strconv.FormatBool(resume))
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	consoleSocket := filepath.Join(dir, "serial.sock")
	done := make(chan error, 1)
	go func() {
		done <- RunVMConsoleProxy(ctx, VMConsoleProxyConfig{
			Firecracker:   fakeFirecracker,
			APISocket:     apiSocket,
			ConfigFile:    filepath.Join(dir, "firecracker.json"),
			ConsoleSocket: consoleSocket,
		})
	}()

	conn := dialUnixSocketForTest(t, consoleSocket)
	defer conn.Close()
	if _, err := conn.Write([]byte("quit\n")); err != nil {
		t.Fatalf("write console input: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunVMConsoleProxy: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunVMConsoleProxy did not return after fake Firecracker exited")
	}

	rawArgs, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read fake firecracker argv: %v", err)
	}
	if strings.Contains(string(rawArgs), "--config-file") {
		t.Fatalf("restore launch args = %q, must not pass --config-file before snapshot/load", string(rawArgs))
	}
	wantLoaded := []string{"wait", apiSocket, request.StatePath, request.MemoryPath, "true"}
	if !reflect.DeepEqual(loaded, wantLoaded) {
		t.Fatalf("load sequence = %#v, want %#v", loaded, wantLoaded)
	}
	if _, err := os.Stat(requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore request still exists after consume: %v", err)
	}
}
```

Add the imports used by this test:

```go
import (
	"reflect"
	"strconv"
)
```

If those imports already exist after nearby edits, keep one import entry.

- [ ] **Step 2: Run the runner test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMConsoleProxyLoadsOneShotSnapshotRequestBeforeGuestBoot -count=1
```

Expected: FAIL with undefined symbols such as `vmFullRestoreRequestPath`, `vmFullRestoreRequest`, `writeVMFullRestoreRequest`, `vmConsoleSnapshotLoader`, `vmConsoleWaitForAPISocket`, and `vmSnapshotLoaderFunc`.

- [ ] **Step 3: Add restore request helpers and loader hooks**

In `pkg/catch/vm_console_proxy.go`, add these declarations near `VMConsoleProxyConfig`:

```go
type vmFullRestoreRequest struct {
	StatePath  string `json:"statePath"`
	MemoryPath string `json:"memoryPath"`
	Resume     bool   `json:"resume"`
}

type vmSnapshotLoader interface {
	LoadSnapshot(context.Context, string, string, string, bool) error
}

type vmSnapshotLoaderFunc func(context.Context, string, string, string, bool) error

func (f vmSnapshotLoaderFunc) LoadSnapshot(ctx context.Context, socket, statePath, memoryPath string, resume bool) error {
	return f(ctx, socket, statePath, memoryPath, resume)
}

var (
	vmConsoleSnapshotLoader     vmSnapshotLoader = firecrackerSnapshotAPI{}
	vmConsoleWaitForAPISocket                  = waitForUnixSocket
)
```

Add these helpers in the same file:

```go
func vmFullRestoreRequestPath(apiSocket string) string {
	return filepath.Join(filepath.Dir(strings.TrimSpace(apiSocket)), "firecracker-restore.json")
}

func writeVMFullRestoreRequest(path string, request vmFullRestoreRequest) error {
	if strings.TrimSpace(request.StatePath) == "" {
		return fmt.Errorf("full restore state path is required")
	}
	if strings.TrimSpace(request.MemoryPath) == "" {
		return fmt.Errorf("full restore memory path is required")
	}
	raw, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create full restore request directory: %w", err)
	}
	return os.WriteFile(path, raw, 0o600)
}

func consumeVMFullRestoreRequest(path string) (vmFullRestoreRequest, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return vmFullRestoreRequest{}, false, nil
	}
	if err != nil {
		return vmFullRestoreRequest{}, false, fmt.Errorf("read full restore request: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return vmFullRestoreRequest{}, false, fmt.Errorf("consume full restore request: %w", err)
	}
	var request vmFullRestoreRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return vmFullRestoreRequest{}, false, fmt.Errorf("decode full restore request: %w", err)
	}
	if strings.TrimSpace(request.StatePath) == "" {
		return vmFullRestoreRequest{}, false, fmt.Errorf("full restore state path is required")
	}
	if strings.TrimSpace(request.MemoryPath) == "" {
		return vmFullRestoreRequest{}, false, fmt.Errorf("full restore memory path is required")
	}
	return request, true, nil
}
```

Add missing imports to `pkg/catch/vm_console_proxy.go`:

```go
import (
	"encoding/json"
)
```

- [ ] **Step 4: Add API socket wait and restore launch**

In `pkg/catch/vm_console_proxy.go`, replace the direct command creation in `RunVMConsoleProxy` with a plan-based launch:

```go
func RunVMConsoleProxy(ctx context.Context, cfg VMConsoleProxyConfig) error {
	if err := validateVMConsoleProxyConfig(cfg); err != nil {
		return err
	}
	listener, err := listenVMConsoleSocket(cfg.ConsoleSocket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	restoreRequest, restoreMode, err := consumeVMFullRestoreRequest(vmFullRestoreRequestPath(cfg.APISocket))
	if err != nil {
		return err
	}
	cmd := vmFirecrackerCommand(ctx, cfg, restoreMode)
	console, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start Firecracker console PTY: %w", err)
	}
	defer func() { _ = console.Close() }()

	guestStopped := make(chan vmGuestStopKind, 1)
	broker := newVMConsoleBroker(console, os.Stdout, guestStopped)
	go broker.accept(listener)
	go broker.copyOutput()

	if restoreMode {
		if err := loadFullVMSnapshot(ctx, cfg.APISocket, restoreRequest); err != nil {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			return fmt.Errorf("%w: %v", ErrVMRestoreLoadFailed, err)
		}
	}
	return waitVMConsoleProcess(cmd, guestStopped)
}

func vmFirecrackerCommand(ctx context.Context, cfg VMConsoleProxyConfig, restoreMode bool) *exec.Cmd {
	if restoreMode {
		return exec.CommandContext(ctx, cfg.Firecracker, "--api-sock", cfg.APISocket)
	}
	return exec.CommandContext(ctx, cfg.Firecracker, "--api-sock", cfg.APISocket, "--config-file", cfg.ConfigFile)
}

func loadFullVMSnapshot(ctx context.Context, apiSocket string, request vmFullRestoreRequest) error {
	if err := vmConsoleWaitForAPISocket(ctx, apiSocket); err != nil {
		return err
	}
	return vmConsoleSnapshotLoader.LoadSnapshot(ctx, apiSocket, request.StatePath, request.MemoryPath, request.Resume)
}
```

Add this wait helper:

```go
const vmAPISocketWaitTimeout = 10 * time.Second

func waitForUnixSocket(ctx context.Context, socketPath string) error {
	deadline := time.Now().Add(vmAPISocketWaitTimeout)
	var lastErr error
	for {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for Firecracker API socket %s: %w", socketPath, lastErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
```

- [ ] **Step 5: Add restore failure sentinel**

In `pkg/catch/vm_console_proxy.go`, change the error constants:

```go
var (
	ErrVMGuestReboot       = errors.New("VM guest requested reboot")
	ErrVMRestoreLoadFailed = errors.New("VM full restore load failed")
)

const (
	VMGuestRebootExitCode       = 75
	VMRestoreLoadFailedExitCode = 76
)
```

- [ ] **Step 6: Run runner tests to verify they pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVMConsoleProxy|TestFirecrackerLoadSnapshotRequest' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the runner work**

Run:

```bash
but status -fv
but commit -m "vm: load full restore requests in runner"
```

Expected: GitButler creates one commit containing only `pkg/catch/vm_console_proxy.go`, `pkg/catch/firecracker_snapshot_restore.go` if changed, and `pkg/catch/vm_console_test.go`.

---

### Task 2: Prevent Failed Full Restore From Cold Booting

**Files:**
- Modify: `pkg/catch/vm_systemd.go:9-38`
- Modify: `cmd/catch/catch.go:55-57,325-345`
- Test: `pkg/catch/vm_systemd_test.go:9-31`
- Test: `cmd/catch/catch_test.go:238-291`

- [ ] **Step 1: Write the failing systemd unit test**

Update `TestRenderVMSystemdUnit` in `pkg/catch/vm_systemd_test.go` so the expected substrings include:

```go
"RestartPreventExitStatus=76",
```

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRenderVMSystemdUnit -count=1
```

Expected: FAIL because the rendered VM unit does not include `RestartPreventExitStatus=76`.

- [ ] **Step 2: Add the systemd prevent line**

In `pkg/catch/vm_systemd.go`, add the line after `RestartForceExitStatus=75`:

```go
RestartPreventExitStatus=%d
```

Update the `fmt.Sprintf` arguments to pass `VMRestoreLoadFailedExitCode`:

```go
`, cfg.Service, cfg.WorkingDirectory, cfg.APISocket, cfg.ConsoleSocket, cfg.Runner, cfg.Firecracker, cfg.APISocket, cfg.ConfigPath, cfg.ConsoleSocket, VMRestoreLoadFailedExitCode)
```

The rendered block should read:

```ini
Restart=on-failure
RestartForceExitStatus=75
RestartPreventExitStatus=76
RestartSec=1
```

- [ ] **Step 3: Write the failing catch exit mapping test**

Add this test to `cmd/catch/catch_test.go` after `TestHandleSpecialCommandVMRunExitsWithRebootCode`:

```go
func TestHandleSpecialCommandVMRunExitsWithRestoreLoadFailureCode(t *testing.T) {
	oldRun := runVMConsoleProxy
	oldExit := exitProcess
	t.Cleanup(func() {
		runVMConsoleProxy = oldRun
		exitProcess = oldExit
	})

	var exitCode int
	runVMConsoleProxy = func(context.Context, catch.VMConsoleProxyConfig) error {
		return catch.ErrVMRestoreLoadFailed
	}
	exitProcess = func(code int) {
		exitCode = code
		panic("exit intercepted")
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("exit was not intercepted")
		}
		if exitCode != catch.VMRestoreLoadFailedExitCode {
			t.Fatalf("exit code = %d, want %d", exitCode, catch.VMRestoreLoadFailedExitCode)
		}
	}()
	_, _ = handleSpecialCommand([]string{"vm-run", "--firecracker", "/fc", "--api-sock", "/api", "--config-file", "/cfg", "--console-sock", "/serial"}, io.Discard)
}
```

Run:

```bash
mise exec -- go test ./cmd/catch -run TestHandleSpecialCommandVMRunExitsWithRestoreLoadFailureCode -count=1
```

Expected: FAIL because `handleVMRunCommand` does not map `ErrVMRestoreLoadFailed`.

- [ ] **Step 4: Map restore load failure to exit code 76**

In `cmd/catch/catch.go`, after the reboot branch in `handleVMRunCommand`, add:

```go
if errors.Is(err, catch.ErrVMRestoreLoadFailed) {
	exitProcess(catch.VMRestoreLoadFailedExitCode)
	return nil
}
```

- [ ] **Step 5: Add existing unit upgrade helper**

In `pkg/catch/vm_systemd.go`, add:

```go
func ensureVMSystemdRestorePrevent(name string) error {
	unitPath := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(name))
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		return fmt.Errorf("read VM systemd unit %s: %w", unitPath, err)
	}
	unit := string(raw)
	line := fmt.Sprintf("RestartPreventExitStatus=%d", VMRestoreLoadFailedExitCode)
	if strings.Contains(unit, line) {
		return nil
	}
	insertAfter := "RestartForceExitStatus=75\n"
	if !strings.Contains(unit, insertAfter) {
		return fmt.Errorf("VM systemd unit %s does not contain RestartForceExitStatus=75", unitPath)
	}
	unit = strings.Replace(unit, insertAfter, insertAfter+line+"\n", 1)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write VM systemd unit %s: %w", unitPath, err)
	}
	cmd := exec.Command("systemctl", "daemon-reload")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reload systemd after updating VM unit %s: %w: %s", unitPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}
```

Add imports to `pkg/catch/vm_systemd.go`:

```go
import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)
```

Keep `fmt` in the same import block.

- [ ] **Step 6: Add helper test**

Add this test to `pkg/catch/vm_systemd_test.go`:

```go
func TestEnsureVMSystemdRestorePreventUpdatesExistingUnit(t *testing.T) {
	dir := t.TempDir()
	oldDir := vmSystemdSystemDir
	vmSystemdSystemDir = dir
	t.Cleanup(func() { vmSystemdSystemDir = oldDir })

	fakeBin := t.TempDir()
	systemctlLog := filepath.Join(fakeBin, "systemctl.log")
	systemctl := filepath.Join(fakeBin, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(systemctlLog) + "\n"
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	unitPath := filepath.Join(dir, vmSystemdUnitName("devbox"))
	unit := "[Service]\nRestart=on-failure\nRestartForceExitStatus=75\nRestartSec=1\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	if err := ensureVMSystemdRestorePrevent("devbox"); err != nil {
		t.Fatalf("ensureVMSystemdRestorePrevent: %v", err)
	}
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if !strings.Contains(string(raw), "RestartPreventExitStatus=76\nRestartSec=1") {
		t.Fatalf("unit = %q, want restore prevent before RestartSec", string(raw))
	}
	logRaw, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatalf("read systemctl log: %v", err)
	}
	if strings.TrimSpace(string(logRaw)) != "daemon-reload" {
		t.Fatalf("systemctl log = %q, want daemon-reload", string(logRaw))
	}
}
```

Add imports used by the test:

```go
import (
	"os"
	"path/filepath"
	"strconv"
)
```

- [ ] **Step 7: Run task tests**

Run:

```bash
mise exec -- go test ./cmd/catch ./pkg/catch -run 'TestHandleSpecialCommandVMRun|TestRenderVMSystemdUnit|TestEnsureVMSystemdRestorePrevent' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the restart safety work**

Run:

```bash
but status -fv
but commit -m "vm: prevent cold boot after failed full restore"
```

Expected: GitButler creates one commit containing the systemd renderer, command exit mapping, and tests.

---

### Task 3: Full VM Restore Orchestration

**Files:**
- Modify: `pkg/catch/recovery_vm.go:155-184,250-292`
- Modify: `pkg/catch/recovery_vm_test.go:1226-1268,1417-1498`
- Test: `pkg/catch/recovery_vm_test.go`

- [ ] **Step 1: Replace the unsupported success tests with a failing real-restore test**

In `pkg/catch/recovery_vm_test.go`, delete these tests:

```go
func TestSnapshotsRestoreVMFullCompatibleFirecrackerIdentityFailsUnsupportedBeforeMutation(t *testing.T)
func TestSnapshotsRestoreVMFullCompatibleCheckpointFailsUnsupportedBeforeMutation(t *testing.T)
```

Replace them with:

```go
func TestSnapshotsRestoreVMFullCompatibleCheckpointRestoresDiskSchedulesStateLoadAndStarts(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	identity := installVMRecoveryFirecrackerLauncher(t, root, "Firecracker v1.7.0-test")
	statePath, memoryPath := seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		metadata["firecrackerSha256"] = identity.SHA256
		metadata["firecrackerVersion"] = identity.Version
	})
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	var out bytes.Buffer
	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Stop: true, Yes: true, Mode: recoveryModeFull}, &out)
	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v; zfs calls = %#v; system calls = %q", err, calls, readRecoveryLog(t, logPath))
	}

	lines := readRecoveryLogLines(t, logPath)
	assertCallOrder(t, lines, "systemctl stop yeet-vm-devbox.service", "zfs snapshot", "dd ", "systemctl daemon-reload", "systemctl start yeet-vm-devbox.service")
	requestPath := vmFullRestoreRequestPath(filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"))
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatalf("read full restore request: %v", err)
	}
	var request vmFullRestoreRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatalf("decode full restore request: %v", err)
	}
	if request.StatePath != statePath || request.MemoryPath != memoryPath || !request.Resume {
		t.Fatalf("restore request = %#v, want state=%q memory=%q resume=true", request, statePath, memoryPath)
	}
	output := out.String()
	for _, want := range []string{
		"Pre-restore recovery point:",
		"Restored VM disk: " + vmRecoverySnapshot,
		"Scheduled full VM state restore: yeet-20260613T203100Z",
		"Started service: devbox",
		"Restored full VM state: yeet-20260613T203100Z",
		"Restore complete.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestSnapshotsRestoreVMFullRequiresFullRecoveryPointBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, nil)
	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})
	if err == nil || !strings.Contains(err.Error(), "is not a full VM checkpoint") {
		t.Fatalf("restoreRecoveryPoint error = %v, want full checkpoint rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}
```

- [ ] **Step 2: Run the new restore test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestSnapshotsRestoreVMFullCompatibleCheckpointRestoresDiskSchedulesStateLoadAndStarts|TestSnapshotsRestoreVMFullRequiresFullRecoveryPointBeforeMutation' -count=1
```

Expected: FAIL because full restore still returns the unsupported error and does not write a restore request.

- [ ] **Step 3: Return metadata from full compatibility planning**

In `pkg/catch/recovery_vm.go`, replace `validateFullVMRestoreCompatibility` with:

```go
func (s *Server) planFullVMRestore(service *db.Service, point recoveryPoint) (vmCheckpointMetadata, error) {
	if point.Mode != recoveryModeFull {
		return vmCheckpointMetadata{}, fmt.Errorf("recovery point %s is not a full VM checkpoint", point.ShortName)
	}
	metadata, err := s.fullVMRestoreMetadata(service, point)
	if err != nil {
		return vmCheckpointMetadata{}, err
	}
	current, err := s.vmCheckpointCompatibility(service, *service.VM.Clone())
	if err != nil {
		return vmCheckpointMetadata{}, err
	}
	if err := validateFullVMCheckpointCompatibility(metadata, current); err != nil {
		return vmCheckpointMetadata{}, err
	}
	return metadata, nil
}
```

Keep this compatibility wrapper if existing tests still call it:

```go
func (s *Server) validateFullVMRestoreCompatibility(service *db.Service, point recoveryPoint) error {
	_, err := s.planFullVMRestore(service, point)
	return err
}
```

- [ ] **Step 4: Implement full restore orchestration**

Replace `restoreFullVMRecoveryPoint` in `pkg/catch/recovery_vm.go` with:

```go
func (s *Server) restoreFullVMRecoveryPoint(ctx context.Context, service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
	metadata, err := s.planFullVMRestore(service, point)
	if err != nil {
		return err
	}
	confirmed, err := confirmFullVMRestore(service, point, flags, rw)
	if err != nil || !confirmed {
		return err
	}
	running, err := s.vmRestoreRunningState(service.Name, flags.Stop)
	if err != nil {
		return err
	}
	if err := stopVMForRestore(service.Name, running, flags.Stop, rw); err != nil {
		return err
	}

	preRestore, err := s.createPreRestoreVMSnapshot(ctx, service, point, rw)
	if err != nil {
		return err
	}
	writef(rw, "Pre-restore recovery point: %s\n", preRestore)
	if err := s.restoreVMZVOLFromSnapshot(ctx, point); err != nil {
		return err
	}
	writef(rw, "Restored VM disk: %s\n", point.Name)
	if err := ensureVMSystemdRestorePrevent(service.Name); err != nil {
		return err
	}
	if err := scheduleFullVMStateRestore(service, metadata); err != nil {
		return err
	}
	writef(rw, "Scheduled full VM state restore: %s\n", point.ShortName)
	if err := startVMAfterRestore(service.Name, true, rw); err != nil {
		return fmt.Errorf("start VM %s for full restore from %s failed after disk restore; pre-restore recovery point: %s: %w", service.Name, point.ShortName, preRestore, err)
	}
	writef(rw, "Restored full VM state: %s\n", point.ShortName)
	writef(rw, "Restore complete.\n")
	return nil
}
```

Add:

```go
func scheduleFullVMStateRestore(service *db.Service, metadata vmCheckpointMetadata) error {
	if service == nil || service.VM == nil {
		return fmt.Errorf("VM service is required for full restore")
	}
	apiSocket := strings.TrimSpace(service.VM.Sockets.APISocketPath)
	if apiSocket == "" {
		return fmt.Errorf("VM %s has no Firecracker API socket", service.Name)
	}
	request := vmFullRestoreRequest{
		StatePath:  metadata.FirecrackerState,
		MemoryPath: metadata.FirecrackerMemory,
		Resume:     true,
	}
	return writeVMFullRestoreRequest(vmFullRestoreRequestPath(apiSocket), request)
}

func confirmFullVMRestore(service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) (bool, error) {
	if flags.Yes {
		return true, nil
	}
	ok, err := cmdutil.Confirm(rw, rw, fmt.Sprintf("Restore full VM state %s from %s?", service.Name, point.ShortName))
	if err != nil {
		return false, fmt.Errorf("failed to confirm full VM restore: %w", err)
	}
	if !ok {
		writef(rw, "Restore cancelled.\n")
		return false, nil
	}
	return true, nil
}
```

- [ ] **Step 5: Run restore tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestSnapshotsRestoreVMFull|TestSnapshotsRestoreVMPreRestore|TestSnapshotsRestoreVMRejects|TestSnapshotsRestoreVMSize' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run full catch package tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit restore orchestration**

Run:

```bash
but status -fv
but commit -m "snapshots: restore full VM checkpoints"
```

Expected: GitButler creates one commit containing `pkg/catch/recovery_vm.go`, `pkg/catch/recovery_vm_test.go`, and any helper files touched by this task.

---

### Task 4: CLI Help And User Docs

**Files:**
- Modify: `pkg/cli/cli.go:738-742`
- Modify: `pkg/cli/cli_test.go:490-502`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-llm.md`
- Modify: `website/docs/cli/yeet-cli.mdx:570-592`
- Modify: `website/docs/payloads/vms.mdx:116-165`

- [ ] **Step 1: Write the failing CLI metadata test**

Replace `TestSnapshotsRestoreHelpDoesNotAdvertiseUnsupportedFullRestore` in `pkg/cli/cli_test.go` with:

```go
func TestSnapshotsRestoreHelpAdvertisesFullRestoreAsSupported(t *testing.T) {
	restore, ok := RemoteGroupInfos()["snapshots"].Commands["restore"]
	if !ok {
		t.Fatal("snapshots restore command missing")
	}
	if !strings.Contains(restore.Usage, "[--mode=disk|full]") {
		t.Fatalf("snapshots restore usage %q should present full restore mode", restore.Usage)
	}
	if strings.Contains(restore.Description, "validation-only") || strings.Contains(restore.Description, "refused") {
		t.Fatalf("snapshots restore description %q should not mark full restore as refused", restore.Description)
	}
	foundFullExample := false
	for _, example := range restore.Examples {
		if strings.Contains(example, "--mode=full") {
			foundFullExample = true
		}
	}
	if !foundFullExample {
		t.Fatalf("snapshots restore examples %#v should include --mode=full", restore.Examples)
	}
}
```

Run:

```bash
mise exec -- go test ./pkg/cli -run TestSnapshotsRestoreHelpAdvertisesFullRestoreAsSupported -count=1
```

Expected: FAIL because CLI help still describes full restore as refused.

- [ ] **Step 2: Update CLI metadata**

In `pkg/cli/cli.go`, change the snapshots restore command to:

```go
Description: "Restore disk state, service-root state, or full VM state from a recovery point",
Usage:       "snapshots restore <svc> <snapshot> [--stop] [--start] [--yes] [--mode=disk|full] [--generation=current|snapshot]",
Examples: []string{
	"yeet snapshots restore <svc> yeet-20260613T203100Z-vm-manual-g0 --yes",
	"yeet snapshots restore <svc> yeet-20260613 --stop --yes",
	"yeet snapshots restore <vm> yeet-20260613T203100Z-vm-manual --mode=full --stop --yes",
},
```

- [ ] **Step 3: Regenerate help reference**

Run:

```bash
tools/generate-yeet-help-llm.sh
```

Expected: `.codex/skills/yeet-cli/references/yeet-help-llm.md` no longer says full restore is validation-only or refused.

- [ ] **Step 4: Update website CLI docs**

In `website/docs/cli/yeet-cli.mdx`, replace the paragraph that says the release refuses full restore with:

```mdx
The default restore mode is `--mode=disk`, which restores VM zvol data without
resuming Firecracker memory. Use `--mode=full` for a full VM checkpoint created
with `yeet snapshots create <vm> --full`; yeet restores the zvol first, then
loads Firecracker state and memory and resumes the VM. Full restore is an
in-place operation for the same VM service and requires the VM's shape,
Firecracker binary, disk path, and network device names to remain compatible.
```

- [ ] **Step 5: Update VM payload docs**

In `website/docs/payloads/vms.mdx`, update the VM snapshots section so it includes:

````mdx
For routine VM recovery, disk restore is usually enough:

```bash
yeet snapshots restore <vm> <snapshot> --mode=disk --stop --yes
```

For a full checkpoint, restore the same recovery point with `--mode=full`:

```bash
yeet snapshots restore <vm> <snapshot> --mode=full --stop --yes
```

Full restore resumes the VM from Firecracker state and memory. It is stricter
than disk restore: the VM must still use the same zvol path, CPU and memory
shape, Firecracker binary, and network device names that were present when the
checkpoint was captured.
````

When editing markdown, ensure fenced code blocks are balanced.

- [ ] **Step 6: Run docs and CLI tests**

Run:

```bash
mise exec -- go test ./pkg/cli -count=1
mise exec -- go test ./cmd/yeet -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit help and docs**

Commit inside `website/` first if website files changed:

```bash
git -C website status --short --branch
git -C website add docs/cli/yeet-cli.mdx docs/payloads/vms.mdx
git -C website commit -m "docs: explain full VM checkpoint restore"
git -C website push origin main
```

Then commit the root repo changes, including the website gitlink:

```bash
but status -fv
but commit -m "docs: document full VM checkpoint restore"
```

Expected: website commit is pushed in the website repository, and root commit includes CLI metadata, regenerated help reference, and the website submodule pointer.

---

### Task 5: Local Verification And Live lab-host Smoke Tests

**Files:**
- No production code changes unless verification exposes a bug.
- Modify the implementation files from earlier tasks only when a real failure is found.

- [ ] **Step 1: Run targeted local tests**

Run:

```bash
mise exec -- go test ./cmd/catch ./cmd/yeet ./pkg/cli ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run the full Go test suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Update catch on lab-host**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet version
```

Expected: the reported catch version matches the local commit under test.

- [ ] **Step 4: Smoke test service-root snapshot and restore**

Run with a unique service name:

```bash
svc="codex-svc-root-$(date +%Y%m%d%H%M%S)"
dataset="flash/yeet/${svc}"
root="/flash/yeet/${svc}"
payload="/tmp/${svc}.sh"
printf '#!/bin/sh\nwhile true; do sleep 60; done\n' > "$payload"
chmod +x "$payload"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$svc" "$payload" --service-root="$dataset" --zfs --snapshots=on --progress=plain
ssh root@lab-host "sh -lc 'set -e; mkdir -p \"$root/data\"; printf snapshot > \"$root/data/smoke.txt\"'"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots create "$svc" --comment="codex service-root restore smoke"
snap="$(CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots list "$svc" --format=json | jq -r --arg svc "$svc" '[.[] | select(.service == $svc and .comment == "codex service-root restore smoke")] | sort_by(.created) | last | .shortName')"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots inspect "$svc" "$snap"
ssh root@lab-host "sh -lc 'set -e; printf mutated > \"$root/data/smoke.txt\"; printf stale > \"$root/data/stale-after-snapshot.txt\"'"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$svc" "$snap" --stop --yes --progress=plain
ssh root@lab-host "sh -lc 'set -e; test \"$(cat \"$root/data/smoke.txt\")\" = snapshot; test ! -e \"$root/data/stale-after-snapshot.txt\"; test \"$(zfs get -H -o value mountpoint \"$dataset\")\" = \"$root\"'"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$svc" --yes --clean-data --clean-config
ssh root@lab-host "sh -lc 'zfs destroy -r \"$dataset\" >/dev/null 2>&1 || true; rm -rf \"$root\" >/dev/null 2>&1 || true'"
rm -f "$payload" yeet.toml
```

Expected: the file content returns to `snapshot`, the stale file disappears, and inspect output is concise with no panic, trace, or debug noise.

- [ ] **Step 5: Smoke test VM disk snapshot and restore**

Run with a unique VM name:

```bash
vm="codex-vm-disk-$(date +%Y%m%d%H%M%S)"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$vm" vm://nixos/26.05 --net=svc --image-policy=cached --cpus=1 --memory=1g --disk=16g --snapshots=on --progress=plain
for i in $(seq 1 60); do
	if CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- true >/dev/null 2>&1; then break; fi
	sleep 2
done
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- sh -lc 'printf snapshot > /root/disk-smoke; sync'
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots create "$vm" --comment="codex vm disk restore smoke"
snap="$(CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots list "$vm" --format=json | jq -r --arg vm "$vm" '[.[] | select(.service == $vm and .comment == "codex vm disk restore smoke")] | sort_by(.created) | last | .shortName')"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots list "$vm"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots inspect "$vm" "$snap"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- sh -lc 'printf mutated > /root/disk-smoke; printf stale > /root/stale-after-snapshot; sync'
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$vm" "$snap" --mode=disk --stop --start --yes --progress=plain
for i in $(seq 1 60); do
	if CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- true >/dev/null 2>&1; then break; fi
	sleep 2
done
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- sh -lc 'test "$(cat /root/disk-smoke)" = snapshot; test ! -e /root/stale-after-snapshot'
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$vm" --yes --clean-data --clean-config
rm -f yeet.toml
```

Expected: disk state returns to the snapshot and human list/inspect output stays short and user-facing.

- [ ] **Step 6: Smoke test VM full snapshot and RAM restore**

Run with a unique VM name:

```bash
vm="codex-vm-full-$(date +%Y%m%d%H%M%S)"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$vm" vm://nixos/26.05 --net=svc --image-policy=cached --cpus=1 --memory=1g --disk=16g --snapshots=on --progress=plain
for i in $(seq 1 60); do
	if CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- true >/dev/null 2>&1; then break; fi
	sleep 2
done
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- sh -lc 'printf disk-before > /root/full-disk-smoke; printf ram-before > /run/full-ram-smoke; nohup sh -c "while true; do date +%s > /run/full-ram-heartbeat; sleep 1; done" >/dev/null 2>&1 & sync'
sleep 3
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots create "$vm" --full --comment="codex vm full restore smoke"
snap="$(CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots list "$vm" --format=json | jq -r --arg vm "$vm" '[.[] | select(.service == $vm and .comment == "codex vm full restore smoke" and .mode == "full")] | sort_by(.created) | last | .shortName')"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots inspect "$vm" "$snap"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- sh -lc 'printf disk-after > /root/full-disk-smoke; printf ram-after > /run/full-ram-smoke; rm -f /run/full-ram-heartbeat; sync'
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$vm" "$snap" --mode=full --stop --yes --progress=plain
for i in $(seq 1 60); do
	if CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- true >/dev/null 2>&1; then break; fi
	sleep 2
done
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ssh "$vm" -- sh -lc 'test "$(cat /root/full-disk-smoke)" = disk-before; test "$(cat /run/full-ram-smoke)" = ram-before; test -s /run/full-ram-heartbeat; pgrep -f full-ram-heartbeat >/dev/null'
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$vm" --yes --clean-data --clean-config
rm -f yeet.toml
```

Expected: `/root/full-disk-smoke` returns to `disk-before`, `/run/full-ram-smoke` returns to `ram-before`, the heartbeat file exists in tmpfs, and the pre-snapshot heartbeat process is still present. If the VM cold boots, this check fails because `/run/full-ram-smoke` and the process do not survive.

- [ ] **Step 7: Check output noise explicitly**

For each smoke service or VM before cleanup, capture list and inspect output:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots list "$vm" > /tmp/yeet-list.txt
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots inspect "$vm" "$snap" > /tmp/yeet-inspect.txt
test "$(wc -l < /tmp/yeet-list.txt)" -le 20
test "$(wc -l < /tmp/yeet-inspect.txt)" -le 20
! grep -Eiq 'panic|trace|debug|broken pipe|goroutine' /tmp/yeet-list.txt /tmp/yeet-inspect.txt
```

Expected: both commands are concise and contain no implementation noise.

- [ ] **Step 8: Clean up live leftovers**

Run:

```bash
rm -f yeet.toml
ssh root@lab-host "sh -lc 'systemctl list-units \"yeet-vm-codex-*\" --no-legend --all || true; zfs list -H -o name | grep \"^flash/yeet/codex-\" || true'"
but status -fv
```

Expected: no disposable services or datasets remain, no local `yeet.toml` remains, and the root repo has only intentional implementation/docs changes.

- [ ] **Step 9: Commit verification fixes only if needed**

If live testing exposes a real bug, fix it with a failing local test first, rerun the relevant smoke command, then commit:

```bash
but status -fv
but commit -m "snapshots: fix full VM restore smoke issue"
```

Expected: this commit exists only if a real bug was found during live verification.

---

## Self-Review Checklist

- [ ] The plan implements real full restore instead of validation-only refusal.
- [ ] The runner honors Firecracker's pre-boot `/snapshot/load` requirement by not passing `--config-file` in restore mode.
- [ ] The restored VM remains owned by the existing systemd unit.
- [ ] Failed snapshot loads cannot cold boot the VM because exit code 76 is protected by systemd.
- [ ] Disk restore, restore request scheduling, and VM start order are covered by tests.
- [ ] CLI help and website docs no longer say full restore is unsupported.
- [ ] Live smoke verifies disk state and RAM-only state on `yeet-lab`.
- [ ] Cleanup removes disposable services, datasets, and local `yeet.toml`.
