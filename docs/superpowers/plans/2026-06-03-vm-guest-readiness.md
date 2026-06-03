# VM Guest Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet run ... vm://...` wait for a fresh guest readiness marker before it reports the VM as running, so immediate `yeet ssh <svc>` works in the normal case.

**Architecture:** Keep readiness on the catch side. The guest emits a stronger serial marker after SSH ordering and IPv4 assignment, catch waits for a fresh journaled marker after VM restart, and `ServiceInfo` uses the same marker for VM SSH address discovery. No TCP or SSH probing is added.

**Tech Stack:** Go, catch VM provisioning, Firecracker serial output, systemd/journalctl, existing VM metadata injection, README and website docs.

---

## Scope

This plan implements deploy-time readiness only. It does not add SSH probing,
new CLI flags, VM health monitoring, or migration for already-running VMs.

Use `TMPDIR=/tmp/yeet-tests` for full test and pre-commit runs on macOS. The VM
console socket tests can exceed Unix socket path limits under long default
macOS temp roots.

## File Structure

- Modify `pkg/catch/vm_metadata.go`: strengthen the injected guest readiness
  unit and script so `yeet-ready <iface> <ip>` is emitted only after a global
  IPv4 address is available.
- Modify `pkg/catch/vm_metadata_test.go`: update metadata assertions for
  `network-online.target` and the new marker.
- Modify `pkg/catch/service_info.go`: parse `yeet-ready` as an IP report.
- Modify `pkg/catch/vm_lan_test.go`: cover `yeet-ready` parsing.
- Create `pkg/catch/vm_readiness.go`: capture a journal freshness boundary,
  parse readiness markers, and poll current VM journal output.
- Create `pkg/catch/vm_readiness_test.go`: cover marker parsing, cursor use,
  timestamp fallback, timeout, and interface validation.
- Modify `pkg/catch/vm_provision.go`: capture readiness boundary before VM
  restart and wait for readiness after restart.
- Modify `pkg/catch/vm_provision_test.go`: cover provision wait ordering,
  timeout behavior, and `--restart=false` skip behavior.
- Modify `README.md`: document that VM deploy waits for guest readiness.
- Modify `website/docs/concepts/service-types.mdx`: document VM readiness and
  console recovery.

## Task 1: Strengthen Guest Marker and ServiceInfo Parsing

**Files:**
- Modify: `pkg/catch/vm_metadata.go`
- Modify: `pkg/catch/vm_metadata_test.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/vm_lan_test.go`

- [ ] **Step 1: Write failing parser and metadata tests**

In `pkg/catch/vm_lan_test.go`, update `TestParseVMGuestIPReports` so the input
contains `yeet-ready` and asserts it is treated as the current IP report:

```go
func TestParseVMGuestIPReports(t *testing.T) {
	got := parseVMGuestIPReports([]byte(`
guest-ready
yeet-ip eth0 10.0.4.123
yeet-ready eth0 10.0.4.178
yeet-ip eth1 192.168.100.12
`))
	if got["eth0"] != "10.0.4.178" || got["eth1"] != "192.168.100.12" {
		t.Fatalf("reports = %#v, want eth0 ready IP and eth1 IP", got)
	}
}
```

In `pkg/catch/vm_metadata_test.go`, update
`TestWriteVMGuestMetadataFiles` assertions around the guest-ready files:

```go
assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "yeet-ready")
assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "network-online.target")
assertFileContains(t, filepath.Join(root, "usr", "local", "lib", "yeet-vm", "guest-ready"), "yeet-ready")
assertFileContains(t, filepath.Join(root, "usr", "local", "lib", "yeet-vm", "guest-ready"), "ip -o -4 addr show scope global")
```

Remove the old assertion that `yeet-guest-ready.service` does not contain
`network-online.target`.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestParseVMGuestIPReports|TestWriteVMGuestMetadataFiles' -count=1
```

Expected before implementation: FAIL because `parseVMGuestIPReports` ignores
`yeet-ready` and the injected service still avoids `network-online.target`.

- [ ] **Step 3: Update service-info parser**

In `pkg/catch/service_info.go`, replace the marker check in
`parseVMGuestIPReports` with:

```go
func parseVMGuestIPReports(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || (fields[0] != "yeet-ip" && fields[0] != "yeet-ready") {
			continue
		}
		if _, err := netip.ParseAddr(fields[2]); err != nil {
			continue
		}
		out[fields[1]] = fields[2]
	}
	return out
}
```

- [ ] **Step 4: Update guest readiness unit**

In `pkg/catch/vm_metadata.go`, replace `vmGuestReadyService` with:

```go
const vmGuestReadyService = `[Unit]
Description=yeet guest ready marker
After=network-online.target ssh.service serial-getty@ttyS0.service
Wants=network-online.target ssh.service serial-getty@ttyS0.service

[Service]
Type=oneshot
ExecStart=/usr/local/lib/yeet-vm/guest-ready
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`
```

- [ ] **Step 5: Update guest readiness script**

In `pkg/catch/vm_metadata.go`, replace `vmGuestReadyScript` with:

```go
const vmGuestReadyScript = `#!/bin/sh
i=0
while [ "$i" -lt 60 ]; do
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
	i=$((i + 1))
	sleep 1
done
echo yeet-ready-timeout >/dev/ttyS0
exit 1
`
```

Keep the existing `systemd-networkd-wait-online.service` mask. The script now
does the concrete IP wait, so this change does not rely on a distro wait-online
unit behaving perfectly.

- [ ] **Step 6: Verify Task 1 tests pass**

Run:

```bash
go test ./pkg/catch -run 'TestParseVMGuestIPReports|TestWriteVMGuestMetadataFiles' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit Task 1**

Run:

```bash
git add pkg/catch/vm_metadata.go pkg/catch/vm_metadata_test.go pkg/catch/service_info.go pkg/catch/vm_lan_test.go
git commit -m "pkg/catch: strengthen vm guest readiness marker"
```

## Task 2: Add VM Guest Readiness Journal Waiter

**Files:**
- Create: `pkg/catch/vm_readiness.go`
- Create: `pkg/catch/vm_readiness_test.go`

- [ ] **Step 1: Write failing readiness tests**

Create `pkg/catch/vm_readiness_test.go` with:

```go
package catch

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseVMGuestReadyReportAcceptsConfiguredInterface(t *testing.T) {
	allowed := map[string]struct{}{"eth0": {}}
	got, ok := parseVMGuestReadyReport([]byte("yeet-ready eth0 10.0.4.178\n"), allowed)
	if !ok {
		t.Fatal("parseVMGuestReadyReport ok = false, want true")
	}
	if got.Interface != "eth0" || got.IP != netip.MustParseAddr("10.0.4.178") {
		t.Fatalf("report = %#v", got)
	}
}

func TestParseVMGuestReadyReportRejectsMalformedOrUnknownInterface(t *testing.T) {
	allowed := map[string]struct{}{"eth0": {}}
	for _, raw := range []string{
		"yeet-ready eth0 not-an-ip\n",
		"yeet-ready eth9 10.0.4.178\n",
		"yeet-ip eth0 10.0.4.178\n",
	} {
		if got, ok := parseVMGuestReadyReport([]byte(raw), allowed); ok {
			t.Fatalf("parseVMGuestReadyReport(%q) = %#v, true; want false", raw, got)
		}
	}
}

func TestCaptureVMGuestReadyBoundaryUsesJournalCursor(t *testing.T) {
	stubVMGuestReadyJournal(t, func(ctx context.Context, args []string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"journalctl", "-u", "yeet-vm-devbox.service", "-n", "1", "-o", "export", "--no-pager"}) {
			t.Fatalf("args = %#v", args)
		}
		return []byte("__CURSOR=s/abc\nMESSAGE=old\n"), nil
	})

	boundary, err := captureVMGuestReadyBoundary(context.Background(), "devbox")
	if err != nil {
		t.Fatalf("captureVMGuestReadyBoundary: %v", err)
	}
	if boundary.Cursor != "s/abc" {
		t.Fatalf("cursor = %q, want s/abc", boundary.Cursor)
	}
}

func TestCaptureVMGuestReadyBoundaryFallsBackToTimestampWhenJournalHasNoCursor(t *testing.T) {
	now := time.Unix(1234, 0).UTC()
	oldNow := vmGuestReadyNow
	vmGuestReadyNow = func() time.Time { return now }
	t.Cleanup(func() { vmGuestReadyNow = oldNow })
	stubVMGuestReadyJournal(t, func(context.Context, []string) ([]byte, error) {
		return nil, nil
	})

	boundary, err := captureVMGuestReadyBoundary(context.Background(), "devbox")
	if err != nil {
		t.Fatalf("captureVMGuestReadyBoundary: %v", err)
	}
	if !boundary.Since.Equal(now) || boundary.Cursor != "" {
		t.Fatalf("boundary = %#v, want timestamp fallback", boundary)
	}
}

func TestWaitVMGuestReadyUsesCursorAndReturnsFreshMarker(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyPollInterval
	vmGuestReadyTimeout = time.Second
	vmGuestReadyPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyPollInterval = oldPoll
	})
	calls := 0
	stubVMGuestReadyJournal(t, func(ctx context.Context, args []string) ([]byte, error) {
		calls++
		if !strings.Contains(strings.Join(args, " "), "--after-cursor s/abc") {
			t.Fatalf("args missing cursor: %#v", args)
		}
		if calls == 1 {
			return []byte("old boot\n"), nil
		}
		return []byte("yeet-ready eth0 10.0.4.178\n"), nil
	})

	report, err := waitVMGuestReady(context.Background(), "devbox", testVMReadyNetworkPlan(), vmGuestReadyBoundary{Cursor: "s/abc"})
	if err != nil {
		t.Fatalf("waitVMGuestReady: %v", err)
	}
	if report.Interface != "eth0" || report.IP.String() != "10.0.4.178" {
		t.Fatalf("report = %#v", report)
	}
}

func TestWaitVMGuestReadyTimeoutIncludesConsoleHint(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyPollInterval = oldPoll
	})
	stubVMGuestReadyJournal(t, func(context.Context, []string) ([]byte, error) {
		return nil, nil
	})

	_, err := waitVMGuestReady(context.Background(), "devbox", testVMReadyNetworkPlan(), vmGuestReadyBoundary{})
	if err == nil || !strings.Contains(err.Error(), "yeet vm console devbox") {
		t.Fatalf("timeout error = %v, want console hint", err)
	}
}

func TestWaitVMGuestReadyReportsJournalErrors(t *testing.T) {
	oldTimeout, oldPoll := vmGuestReadyTimeout, vmGuestReadyPollInterval
	vmGuestReadyTimeout = time.Millisecond
	vmGuestReadyPollInterval = time.Millisecond
	t.Cleanup(func() {
		vmGuestReadyTimeout = oldTimeout
		vmGuestReadyPollInterval = oldPoll
	})
	stubVMGuestReadyJournal(t, func(context.Context, []string) ([]byte, error) {
		return nil, errors.New("journal unavailable")
	})

	_, err := waitVMGuestReady(context.Background(), "devbox", testVMReadyNetworkPlan(), vmGuestReadyBoundary{})
	if err == nil || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("waitVMGuestReady error = %v, want journal error", err)
	}
}

func testVMReadyNetworkPlan() vmNetworkPlan {
	return vmNetworkPlan{
		Service: "devbox",
		Interfaces: []vmNetworkInterfacePlan{{
			Mode:      "lan",
			GuestName: "eth0",
		}},
	}
}

func stubVMGuestReadyJournal(t *testing.T, fn vmGuestReadyJournalRunner) {
	t.Helper()
	old := vmGuestReadyJournalOutput
	vmGuestReadyJournalOutput = fn
	t.Cleanup(func() { vmGuestReadyJournalOutput = old })
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestParseVMGuestReadyReport|TestCaptureVMGuestReadyBoundary|TestWaitVMGuestReady' -count=1
```

Expected before implementation: FAIL because the new readiness helper does not
exist.

- [ ] **Step 3: Implement readiness helper**

Create `pkg/catch/vm_readiness.go` with:

```go
package catch

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type vmGuestReadyReport struct {
	Interface string
	IP        netip.Addr
}

type vmGuestReadyBoundary struct {
	Cursor string
	Since  time.Time
}

type vmGuestReadyJournalRunner func(context.Context, []string) ([]byte, error)

var (
	vmGuestReadyJournalOutput = runVMGuestReadyJournalOutput
	vmGuestReadyNow           = time.Now
	vmGuestReadyPollInterval  = 500 * time.Millisecond
	vmGuestReadyTimeout       = 60 * time.Second
)

func captureVMGuestReadyBoundary(ctx context.Context, service string) (vmGuestReadyBoundary, error) {
	boundary := vmGuestReadyBoundary{Since: vmGuestReadyNow().UTC()}
	args := []string{"journalctl", "-u", vmSystemdUnitName(service), "-n", "1", "-o", "export", "--no-pager"}
	raw, err := vmGuestReadyJournalOutput(ctx, args)
	if err != nil {
		return boundary, nil
	}
	if cursor := parseJournalCursor(raw); cursor != "" {
		boundary.Cursor = cursor
	}
	return boundary, nil
}

func waitVMGuestReady(ctx context.Context, service string, network vmNetworkPlan, boundary vmGuestReadyBoundary) (vmGuestReadyReport, error) {
	allowed := vmGuestReadyInterfaces(network)
	ctx, cancel := context.WithTimeout(ctx, vmGuestReadyTimeout)
	defer cancel()
	var lastErr error
	for {
		report, ok, err := readVMGuestReady(ctx, service, boundary, allowed)
		if err != nil {
			lastErr = err
		} else if ok {
			return report, nil
		}
		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("VM %s started, but guest readiness was not reported within %s; use `yeet vm console %s`", service, vmGuestReadyTimeout, service)
			if lastErr != nil {
				return vmGuestReadyReport{}, fmt.Errorf("%s: %w", msg, lastErr)
			}
			return vmGuestReadyReport{}, fmt.Errorf("%s", msg)
		case <-time.After(vmGuestReadyPollInterval):
		}
	}
}

func readVMGuestReady(ctx context.Context, service string, boundary vmGuestReadyBoundary, allowed map[string]struct{}) (vmGuestReadyReport, bool, error) {
	args := []string{"journalctl", "-u", vmSystemdUnitName(service), "-o", "cat", "--no-pager"}
	if boundary.Cursor != "" {
		args = append(args, "--after-cursor", boundary.Cursor)
	} else if !boundary.Since.IsZero() {
		args = append(args, "--since", "@"+strconv.FormatInt(boundary.Since.Unix(), 10))
	}
	raw, err := vmGuestReadyJournalOutput(ctx, args)
	if err != nil {
		return vmGuestReadyReport{}, false, fmt.Errorf("read VM journal: %w", err)
	}
	report, ok := parseVMGuestReadyReport(raw, allowed)
	return report, ok, nil
}

func parseVMGuestReadyReport(raw []byte, allowed map[string]struct{}) (vmGuestReadyReport, bool) {
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "yeet-ready" {
			continue
		}
		if _, ok := allowed[fields[1]]; !ok {
			continue
		}
		ip, err := netip.ParseAddr(fields[2])
		if err != nil {
			continue
		}
		return vmGuestReadyReport{Interface: fields[1], IP: ip}, true
	}
	return vmGuestReadyReport{}, false
}

func vmGuestReadyInterfaces(network vmNetworkPlan) map[string]struct{} {
	out := make(map[string]struct{}, len(network.Interfaces))
	for _, iface := range network.Interfaces {
		name := strings.TrimSpace(iface.GuestName)
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

func parseJournalCursor(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		if cursor, ok := strings.CutPrefix(line, "__CURSOR="); ok {
			return strings.TrimSpace(cursor)
		}
	}
	return ""
}

func runVMGuestReadyJournalOutput(ctx context.Context, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("journal command is empty")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	return cmd.Output()
}
```

- [ ] **Step 4: Verify Task 2 tests pass**

Run:

```bash
go test ./pkg/catch -run 'TestParseVMGuestReadyReport|TestCaptureVMGuestReadyBoundary|TestWaitVMGuestReady' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

Run:

```bash
git add pkg/catch/vm_readiness.go pkg/catch/vm_readiness_test.go
git commit -m "pkg/catch: wait for vm guest readiness marker"
```

## Task 3: Wire Readiness Wait Into VM Provisioning

**Files:**
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write failing provision wait tests**

Add these tests near the restart tests in `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMWaitsForGuestReadinessBeforeNextCommands(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	var captured bool
	var waited bool
	vmProvisionGuestReadyBoundaryFunc = func(ctx context.Context, service string) (vmGuestReadyBoundary, error) {
		if service != "devbox" {
			t.Fatalf("boundary service = %q, want devbox", service)
		}
		captured = true
		return vmGuestReadyBoundary{Cursor: "s/abc"}, nil
	}
	vmProvisionGuestReadyWaitFunc = func(ctx context.Context, service string, network vmNetworkPlan, boundary vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		waited = true
		if !captured {
			t.Fatal("wait called before boundary capture")
		}
		if boundary.Cursor != "s/abc" {
			t.Fatalf("boundary = %#v, want cursor", boundary)
		}
		return vmGuestReadyReport{Interface: "eth0", IP: netip.MustParseAddr("192.168.100.4")}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !captured || !waited {
		t.Fatalf("captured=%v waited=%v, want both true", captured, waited)
	}
	text := out.String()
	waitIdx := strings.Index(text, "Waiting for guest readiness")
	runIdx := strings.Index(text, "VM devbox is running")
	if waitIdx < 0 || runIdx < 0 || waitIdx > runIdx {
		t.Fatalf("output order wrong:\n%s", text)
	}
}

func TestRunVMSkipsGuestReadinessWhenRestartFalse(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	vmProvisionGuestReadyBoundaryFunc = func(context.Context, string) (vmGuestReadyBoundary, error) {
		t.Fatal("boundary should not be captured when restart=false")
		return vmGuestReadyBoundary{}, nil
	}
	vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		t.Fatal("readiness should not be waited when restart=false")
		return vmGuestReadyReport{}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
}

func TestRunVMGuestReadinessFailureKeepsCommittedVM(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	vmProvisionGuestReadyBoundaryFunc = func(context.Context, string) (vmGuestReadyBoundary, error) {
		return vmGuestReadyBoundary{}, nil
	}
	vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		return vmGuestReadyReport{}, errors.New("guest readiness timeout")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "guest readiness timeout") {
		t.Fatalf("runVM error = %v, want guest readiness timeout", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM == nil || svc.VM.SetupState != "ready" {
		t.Fatalf("VM after readiness failure = %#v, want committed ready VM for console recovery", svc.VM)
	}
}
```

Add `net/netip` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestRunVMWaitsForGuestReadinessBeforeNextCommands|TestRunVMSkipsGuestReadinessWhenRestartFalse|TestRunVMGuestReadinessFailureKeepsCommittedVM' -count=1
```

Expected before implementation: FAIL because the provision hooks do not exist
and `finishVMProvision` does not wait after restart.

- [ ] **Step 3: Add provision hook variables**

In the `var` block near the top of `pkg/catch/vm_provision.go`, add:

```go
	vmProvisionGuestReadyBoundaryFunc = captureVMGuestReadyBoundary
	vmProvisionGuestReadyWaitFunc     = waitVMGuestReady
```

In `newVMProvisionTestExecer`, save and restore these globals:

```go
oldGuestReadyBoundary := vmProvisionGuestReadyBoundaryFunc
oldGuestReadyWait := vmProvisionGuestReadyWaitFunc
```

and in `t.Cleanup`:

```go
vmProvisionGuestReadyBoundaryFunc = oldGuestReadyBoundary
vmProvisionGuestReadyWaitFunc = oldGuestReadyWait
```

Set a default test stub after `vmProvisionSystemctlFunc`:

```go
vmProvisionGuestReadyBoundaryFunc = func(context.Context, string) (vmGuestReadyBoundary, error) {
	return vmGuestReadyBoundary{}, nil
}
vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
	return vmGuestReadyReport{}, nil
}
```

- [ ] **Step 4: Wire wait into `finishVMProvision`**

Replace the restart block in `finishVMProvision` with:

```go
	var readyBoundary vmGuestReadyBoundary
	if restart {
		captureBoundary := vmProvisionGuestReadyBoundaryFunc
		if captureBoundary == nil {
			captureBoundary = captureVMGuestReadyBoundary
		}
		readyBoundary, err = captureBoundary(ctx, plan.Service)
		if err != nil {
			return err
		}
	}
	if restart {
		e.vmProgressf("Starting VM...\n")
		if err := e.restartVMSystemdUnit(plan); err != nil {
			return err
		}
		e.vmProgressf("Waiting for guest readiness...\n")
		waitReady := vmProvisionGuestReadyWaitFunc
		if waitReady == nil {
			waitReady = waitVMGuestReady
		}
		if _, err := waitReady(ctx, plan.Service, plan.Network, readyBoundary); err != nil {
			return err
		}
	}
```

- [ ] **Step 5: Verify provision wait tests pass**

Run:

```bash
go test ./pkg/catch -run 'TestRunVMPrintsProgressAndNextCommands|TestRunVMWaitsForGuestReadinessBeforeNextCommands|TestRunVMSkipsGuestReadinessWhenRestartFalse|TestRunVMGuestReadinessFailureKeepsCommittedVM' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 3**

Run:

```bash
git add pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go
git commit -m "pkg/catch: gate vm deploy on guest readiness"
```

## Task 4: Document VM Deploy Readiness

**Files:**
- Modify: `README.md`
- Modify: `website/docs/concepts/service-types.mdx`

- [ ] **Step 1: Update README VM section**

In `README.md`, add this sentence after the `yeet ssh devbox` example in the
experimental VM section:

```markdown
When `yeet run` starts a VM, it waits for the guest to report SSH-era readiness
and an IPv4 address before printing the next `yeet ssh` command. If the guest
does not report readiness, use `yeet vm console <svc>` for boot diagnostics.
```

- [ ] **Step 2: Update website VM concept**

In `website/docs/concepts/service-types.mdx`, add this paragraph after the
`yeet ssh <svc>` example in the experimental VM section:

```mdx
When `yeet run` starts a VM, it waits for the guest to report readiness and an
IPv4 address before printing the next `yeet ssh` command. If the guest does not
report readiness, use `yeet vm console <svc>` for boot diagnostics.
```

- [ ] **Step 3: Commit website docs inside submodule if changed**

Run:

```bash
git -C website status --short
git -C website add docs/concepts/service-types.mdx
git -C website commit -m "docs: document vm guest readiness"
```

If the website submodule is detached, commit on the detached HEAD as the repo's
existing instructions allow, then commit the updated submodule pointer in the
root repo in the next step.

- [ ] **Step 4: Commit root docs changes**

Run:

```bash
git add README.md website
git commit -m "docs: document vm guest readiness"
```

## Task 5: Verification and Live E2E

**Files:**
- No code changes expected.

- [ ] **Step 1: Run targeted package tests**

Run:

```bash
go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run broader local tests**

Run:

```bash
go test ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit gate**

Run:

```bash
TMPDIR=/tmp/yeet-tests mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Update catch on lab-host**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet init root@yeet-lab
```

Expected: install completes without hanging and reports catch installed.

- [ ] **Step 5: Recreate a lab-host LAN VM and immediately SSH**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet rm devbox --clean-data
CATCH_HOST=yeet-lab go run ./cmd/yeet run devbox@yeet-lab vm://ubuntu/26.04 --service-root=flash/yeet/vms/devbox --zfs --disk=128g --net=lan
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh devbox -- true
```

For the remove command, answer yes to confirmations if prompted. Expected:
`yeet run` prints `Waiting for guest readiness...` before `VM devbox is
running`, and the immediate SSH command exits successfully without retry.

- [ ] **Step 6: Commit any final test or docs corrections**

If verification required edits, commit them with a focused message. If no edits
were required, no commit is needed.
