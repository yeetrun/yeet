# VM Run Output TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet run <name> vm://...` show polished interactive VM provisioning progress while keeping non-TTY output structured and compatible with Docker deploy behavior.

**Architecture:** Keep catch as the source of deploy progress and continue streaming output through the existing exec path. Extend catch's existing `runUI` with timed steps and footer/info rendering, add a small VM provisioning presenter around it, then wire VM provisioning phases into structured progress calls instead of direct `vmProgressf` lines.

**Tech Stack:** Go, catch TTY exec path, existing `pkg/tui` spinner/color helpers, existing `catchrpc.ProgressMode`, GitButler for version-control writes.

---

## File Structure

- Modify `pkg/catch/run_ui.go`
  - Add timed step support for TTY mode.
  - Add block/info rendering that prints aligned human output in TTY mode and structured `status=info` lines in plain mode.
  - Preserve existing `StartStep`, `DoneStep`, `FailStep`, and Docker deploy behavior.
- Modify `pkg/catch/run_ui_test.go`
  - Cover timed step detail formatting.
  - Cover `PrintBlock` behavior in TTY, plain, and quiet modes.
- Create `pkg/catch/vm_provision_ui.go`
  - Own VM-specific step names, summary formatting, footer rendering, and recovery hints.
  - Provide a `ProgressUI` adapter for VM image download that delegates into the shared VM run UI without stopping the parent UI.
- Create `pkg/catch/vm_provision_ui_test.go`
  - Unit-test VM footer formatting, image detail formatting, and recovery footer lines.
- Modify `pkg/catch/vm_provision.go`
  - Create one VM run UI at the start of VM provisioning.
  - Pass the VM image progress adapter into image selection.
  - Replace ad hoc VM progress prints with structured step calls.
  - Return readiness details from `startVMAfterProvision` so the final footer can show the guest IP.
- Modify `pkg/catch/vm_provision_test.go`
  - Update existing VM output tests.
  - Add TTY, plain, quiet, image download, and readiness-failure coverage.
- Leave `pkg/yeet/exec_remote.go`, `pkg/catchrpc`, and Docker Compose execution unchanged.
  - The local client continues to stream remote output.
  - No new RPC progress protocol is introduced in this pass.

---

### Task 1: Add Timed And Block Rendering To `runUI`

**Files:**
- Modify: `pkg/catch/run_ui.go`
- Modify: `pkg/catch/run_ui_test.go`

- [ ] **Step 1: Write failing tests for timed TTY detail formatting**

Add these tests to `pkg/catch/run_ui_test.go`:

```go
func TestRunUITimedDetailText(t *testing.T) {
	tests := []struct {
		name    string
		step    string
		detail  string
		elapsed time.Duration
		want    string
	}{
		{name: "step only", step: "Wait for guest readiness", elapsed: 1200 * time.Millisecond, want: "Wait for guest readiness 1.2s"},
		{name: "step and detail", step: "Prepare disk", detail: "64 GB", elapsed: 2500 * time.Millisecond, want: "Prepare disk 64 GB 2.5s"},
		{name: "subsecond", step: "Start VM", elapsed: 80 * time.Millisecond, want: "Start VM 0.1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runUITimedDetailText(tt.step, tt.detail, tt.elapsed); got != tt.want {
				t.Fatalf("runUITimedDetailText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatRunUIElapsed(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 80 * time.Millisecond, want: "0.1s"},
		{elapsed: 1250 * time.Millisecond, want: "1.3s"},
		{elapsed: 12*time.Second + 340*time.Millisecond, want: "12.3s"},
		{elapsed: 70*time.Second + 100*time.Millisecond, want: "1m10s"},
	}
	for _, tt := range tests {
		if got := formatRunUIElapsed(tt.elapsed); got != tt.want {
			t.Fatalf("formatRunUIElapsed(%s) = %q, want %q", tt.elapsed, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunUITimedDetailText|TestFormatRunUIElapsed' -count=1
```

Expected: FAIL because `runUITimedDetailText` and `formatRunUIElapsed` do not exist.

- [ ] **Step 3: Write failing tests for `PrintBlock`**

Add these tests to `pkg/catch/run_ui_test.go`:

```go
func TestRunUIPrintBlockTTYWritesHumanLines(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, true, false, "run", "devbox@yeet-pve1")

	ui.PrintBlock([]string{
		"",
		"devbox@yeet-pve1",
		"SSH      yeet ssh devbox",
	})

	got := buf.String()
	for _, want := range []string{
		"\ndevbox@yeet-pve1\n",
		"SSH      yeet ssh devbox\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PrintBlock output missing %q in %q", want, got)
		}
	}
}

func TestRunUIPrintBlockPlainEmitsInfoLines(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "devbox@yeet-pve1")

	ui.PrintBlock([]string{
		"",
		"devbox@yeet-pve1",
		"SSH      yeet ssh devbox",
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		`action=run service=devbox@yeet-pve1 status=info detail=devbox@yeet-pve1`,
		`action=run service=devbox@yeet-pve1 status=info detail="SSH      yeet ssh devbox"`,
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("PrintBlock plain lines = %#v, want %#v", lines, want)
	}
}

func TestRunUIPrintBlockQuietSuppressesOutput(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, true, "run", "devbox@yeet-pve1")

	ui.PrintBlock([]string{"devbox@yeet-pve1"})

	if got := buf.String(); got != "" {
		t.Fatalf("PrintBlock quiet output = %q, want empty", got)
	}
}
```

Update the import list in `pkg/catch/run_ui_test.go` to include:

```go
import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)
```

- [ ] **Step 4: Run the focused test and confirm it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunUIPrintBlock|TestRunUITimedDetailText|TestFormatRunUIElapsed' -count=1
```

Expected: FAIL because `PrintBlock`, `runUITimedDetailText`, and `formatRunUIElapsed` do not exist.

- [ ] **Step 5: Implement elapsed formatting and `PrintBlock`**

In `pkg/catch/run_ui.go`, add the elapsed formatter helpers near `runUIDetailText`:

```go
func runUITimedDetailText(name, detail string, elapsed time.Duration) string {
	base := runUIDetailText(name, detail)
	elapsedText := formatRunUIElapsed(elapsed)
	if elapsedText == "" {
		return base
	}
	if base == "" {
		return elapsedText
	}
	return base + " " + elapsedText
}

func formatRunUIElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		tenths := int64((d + 50*time.Millisecond) / (100 * time.Millisecond))
		return fmt.Sprintf("%d.%ds", tenths/10, tenths%10)
	}
	seconds := int64((d + 500*time.Millisecond) / time.Second)
	minutes := seconds / 60
	seconds %= 60
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}
```

Add this method to `runUI`:

```go
func (u *runUI) PrintBlock(lines []string) {
	if u.quiet {
		return
	}
	u.stopSpinner(true)
	if u.enabled {
		for _, line := range lines {
			_, _ = fmt.Fprintln(u.out, line)
		}
		return
	}
	for _, line := range lines {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		u.plain.Info(line)
	}
}
```

- [ ] **Step 6: Implement timed step lifecycle without changing existing `StartStep` callers**

In `pkg/catch/run_ui.go`, add fields to `runUI`:

```go
	stepStartedAt time.Time
	stepDetail    string
	timedStop     chan struct{}
	timedDone     chan struct{}
```

Add these methods:

```go
func (u *runUI) StartTimedStep(name string) {
	u.StartStep(name)
	if u.quiet || !u.enabled {
		return
	}
	u.mu.Lock()
	u.stepStartedAt = time.Now()
	u.stepDetail = ""
	u.stopTimedStepLocked()
	stop := make(chan struct{})
	done := make(chan struct{})
	u.timedStop = stop
	u.timedDone = done
	u.mu.Unlock()

	go u.updateTimedStep(name, stop, done)
}

func (u *runUI) updateTimedStep(name string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			u.mu.Lock()
			current := u.current
			started := u.stepStartedAt
			detail := u.stepDetail
			sp := u.spinner
			u.mu.Unlock()
			if current != name || sp == nil || started.IsZero() {
				continue
			}
			sp.Update(runUITimedDetailText(name, detail, time.Since(started)))
		}
	}
}

func (u *runUI) stopTimedStep() {
	u.mu.Lock()
	u.stopTimedStepLocked()
	u.mu.Unlock()
}

func (u *runUI) stopTimedStepLocked() {
	stop := u.timedStop
	done := u.timedDone
	u.timedStop = nil
	u.timedDone = nil
	if stop == nil {
		return
	}
	close(stop)
	u.mu.Unlock()
	<-done
	u.mu.Lock()
}
```

Update `UpdateDetail` so it stores the latest detail before rendering:

```go
u.mu.Lock()
name := u.current
u.stepDetail = detail
sp := u.spinner
suspended := u.suspended
u.mu.Unlock()
```

Call `u.stopTimedStep()` at the start of `DoneStep`, `FailStep`, `Suspend`, and `Stop` before stopping the spinner.

- [ ] **Step 7: Run focused `runUI` tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunUI' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 1**

Run:

```bash
but status -fv
```

Commit only `pkg/catch/run_ui.go` and `pkg/catch/run_ui_test.go` to the current session branch with message:

```text
catch: add timed run ui blocks
```

Use the GitButler change IDs shown by `but status -fv`.

---

### Task 2: Add VM Provisioning Output Presenter

**Files:**
- Create: `pkg/catch/vm_provision_ui.go`
- Create: `pkg/catch/vm_provision_ui_test.go`

- [ ] **Step 1: Write tests for VM footer and detail formatting**

Create `pkg/catch/vm_provision_ui_test.go`:

```go
package catch

import (
	"reflect"
	"testing"
	"time"
)

func TestVMProvisionSuccessBlockLines(t *testing.T) {
	plan := vmProvisionPlan{
		Service: "devbox",
		Shape: vmShape{
			CPUs:        2,
			MemoryBytes: 2 << 30,
			DiskBytes:   64 << 30,
		},
		Image: vmImageAsset{
			Manifest: vmImageManifest{
				Name:    "Ubuntu 26.04",
				Version: "ubuntu-26.04-amd64-v15",
			},
		},
		Network: vmNetworkPlan{
			Interfaces: []vmNetworkInterface{
				{Mode: "svc"},
				{Mode: "lan"},
			},
		},
	}

	got := vmProvisionSuccessBlockLines(vmProvisionSuccess{
		ServiceLabel: "devbox@yeet-pve1",
		Plan:         plan,
		Payload:      testUbuntuVMPayload,
		Elapsed:      12400 * time.Millisecond,
	})
	want := []string{
		"✔ VM ready in 12.4s",
		"",
		"devbox@yeet-pve1",
		"Image    Ubuntu 26.04",
		"Shape    2 vCPU, 2.0 GB memory, 64.0 GB disk",
		"Network  svc,lan",
		"",
		"SSH      yeet ssh devbox",
		"Console  yeet vm console devbox",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("success block = %#v, want %#v", got, want)
	}
}

func TestVMProvisionReadinessFailureBlockLines(t *testing.T) {
	got := vmProvisionReadinessFailureBlockLines("devbox")
	want := []string{
		"",
		"VM service was created, but readiness did not complete.",
		"",
		"Console  yeet vm console devbox",
		"Logs     yeet logs devbox",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failure block = %#v, want %#v", got, want)
	}
}

func TestVMProvisionPlanDetail(t *testing.T) {
	plan := vmProvisionPlan{
		Image: vmImageAsset{Manifest: vmImageManifest{Name: "Ubuntu 26.04", Version: "ubuntu-26.04-amd64-v15"}},
	}
	if got := vmProvisionPlanDetail(plan); got != "Ubuntu 26.04" {
		t.Fatalf("vmProvisionPlanDetail = %q", got)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMProvision.*BlockLines|TestVMProvisionPlanDetail' -count=1
```

Expected: FAIL because the new presenter helpers do not exist.

- [ ] **Step 3: Implement the VM presenter file**

Create `pkg/catch/vm_provision_ui.go` with:

```go
package catch

import (
	"fmt"
	"strings"
	"time"
)

const (
	vmRunStepResolve   = "Resolve VM plan"
	vmRunStepDisk      = "Prepare disk"
	vmRunStepMetadata  = "Inject guest metadata"
	vmRunStepConfig    = "Write Firecracker config"
	vmRunStepNetwork   = "Configure network"
	vmRunStepInstall   = "Install VM service"
	vmRunStepStart     = "Start VM"
	vmRunStepReadiness = "Wait for guest readiness"
)

type vmProvisionUI struct {
	ui           *runUI
	startedAt    time.Time
	serviceLabel string
}

type vmProvisionSuccess struct {
	ServiceLabel string
	Plan         vmProvisionPlan
	Payload      string
	Elapsed      time.Duration
	Ready        vmGuestReadyReport
}

func newVMProvisionUI(e *ttyExecer) *vmProvisionUI {
	ui := e.newProgressUI("run")
	serviceLabel := e.sn
	if e.hostLabel != "" {
		serviceLabel = e.sn + "@" + e.hostLabel
	}
	return &vmProvisionUI{
		ui:           ui,
		startedAt:    time.Now(),
		serviceLabel: serviceLabel,
	}
}

func (v *vmProvisionUI) Start() {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.Start()
}

func (v *vmProvisionUI) Stop() {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.Stop()
}

func (v *vmProvisionUI) StartStep(name string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.StartTimedStep(name)
}

func (v *vmProvisionUI) DoneStep(detail string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.DoneStep(detail)
}

func (v *vmProvisionUI) FailStep(detail string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.FailStep(detail)
}

func (v *vmProvisionUI) ImageProgress() ProgressUI {
	if v == nil || v.ui == nil {
		return nil
	}
	return vmProvisionImageProgressUI{parent: v}
}

func (v *vmProvisionUI) PrintSuccess(plan vmProvisionPlan, payload string, ready vmGuestReadyReport) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.PrintBlock(vmProvisionSuccessBlockLines(vmProvisionSuccess{
		ServiceLabel: v.serviceLabel,
		Plan:         plan,
		Payload:      payload,
		Elapsed:      time.Since(v.startedAt),
		Ready:        ready,
	}))
}

func (v *vmProvisionUI) PrintReadinessFailure(service string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.PrintBlock(vmProvisionReadinessFailureBlockLines(service))
}

type vmProvisionImageProgressUI struct {
	parent *vmProvisionUI
}

func (u vmProvisionImageProgressUI) Start() {}
func (u vmProvisionImageProgressUI) Stop()  {}
func (u vmProvisionImageProgressUI) Suspend() {
	if u.parent != nil && u.parent.ui != nil {
		u.parent.ui.Suspend()
	}
}
func (u vmProvisionImageProgressUI) StartStep(name string) {
	if u.parent != nil {
		u.parent.StartStep(name)
	}
}
func (u vmProvisionImageProgressUI) UpdateDetail(detail string) {
	if u.parent != nil && u.parent.ui != nil {
		u.parent.ui.UpdateDetail(detail)
	}
}
func (u vmProvisionImageProgressUI) DoneStep(detail string) {
	if u.parent != nil {
		u.parent.DoneStep(detail)
	}
}
func (u vmProvisionImageProgressUI) FailStep(detail string) {
	if u.parent != nil {
		u.parent.FailStep(detail)
	}
}
func (u vmProvisionImageProgressUI) Printer(format string, args ...any) {}

func vmProvisionSuccessBlockLines(s vmProvisionSuccess) []string {
	service := s.Plan.Service
	if service == "" {
		service = strings.TrimSpace(strings.Split(s.ServiceLabel, "@")[0])
	}
	return []string{
		fmt.Sprintf("✔ VM ready in %s", formatRunUIElapsed(s.Elapsed)),
		"",
		s.ServiceLabel,
		"Image    " + vmProvisionImageDisplayName(s.Plan, s.Payload),
		"Shape    " + vmProvisionShapeLine(s.Plan.Shape),
		"Network  " + formatVMProvisionNetwork(s.Plan.Network),
		"",
		"SSH      yeet ssh " + service,
		"Console  yeet vm console " + service,
	}
}

func vmProvisionReadinessFailureBlockLines(service string) []string {
	return []string{
		"",
		"VM service was created, but readiness did not complete.",
		"",
		"Console  yeet vm console " + service,
		"Logs     yeet logs " + service,
	}
}

func vmProvisionPlanDetail(plan vmProvisionPlan) string {
	return vmProvisionImageDisplayName(plan, "")
}

func vmProvisionImageDisplayName(plan vmProvisionPlan, payload string) string {
	name := strings.TrimSpace(plan.Image.Manifest.Name)
	if name != "" {
		return name
	}
	if payload != "" {
		return payload
	}
	if plan.Image.Manifest.Version != "" {
		return plan.Image.Manifest.Version
	}
	return "VM image"
}

func vmProvisionShapeLine(shape vmShape) string {
	return fmt.Sprintf("%d vCPU, %s memory, %s disk",
		shape.CPUs,
		formatVMProvisionBytes(shape.MemoryBytes),
		formatVMProvisionBytes(shape.DiskBytes),
	)
}
```

- [ ] **Step 4: Run presenter tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMProvision.*BlockLines|TestVMProvisionPlanDetail' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

Run:

```bash
but status -fv
```

Commit only `pkg/catch/vm_provision_ui.go` and `pkg/catch/vm_provision_ui_test.go` to the current session branch with message:

```text
catch: add vm provision output presenter
```

Use the GitButler change IDs shown by `but status -fv`.

---

### Task 3: Wire VM Provisioning Into Structured UI

**Files:**
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write failing plain-output VM run test**

Replace `TestRunVMPrintsProgressAndNextCommands` in `pkg/catch/vm_provision_test.go` with:

```go
func TestRunVMPlainProgressAndNextCommands(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	execer.hostLabel = "yeet-pve1"

	if err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g", Restart: true}, testUbuntuVMPayload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		`action=run service=devbox@yeet-pve1 status=running step="Resolve VM plan"`,
		`action=run service=devbox@yeet-pve1 status=ok step="Resolve VM plan" detail=ubuntu`,
		`action=run service=devbox@yeet-pve1 status=running step="Prepare disk"`,
		`action=run service=devbox@yeet-pve1 status=ok step="Prepare disk" detail="16.0 GB"`,
		`action=run service=devbox@yeet-pve1 status=running step="Wait for guest readiness"`,
		`action=run service=devbox@yeet-pve1 status=ok step="Wait for guest readiness"`,
		`action=run service=devbox@yeet-pve1 status=info detail="SSH      yeet ssh devbox"`,
		`action=run service=devbox@yeet-pve1 status=info detail="Console  yeet vm console devbox"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}
```

- [ ] **Step 2: Run the failing plain-output test**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMPlainProgressAndNextCommands -count=1
```

Expected: FAIL because VM provisioning still prints ad hoc plain text.

- [ ] **Step 3: Update VM provisioning signatures**

In `pkg/catch/vm_provision.go`, change these function signatures:

```go
func (e *ttyExecer) vmProvisionInputs(flags cli.RunFlags, payload string, ui ProgressUI) (vmProvisionInputs, error)
func (e *ttyExecer) finishVMProvision(ctx context.Context, plan vmProvisionPlan, payload string, restart bool, snapshotPolicyFlags *cli.ServiceSetFlags, ui *vmProvisionUI) (committed bool, ready vmGuestReadyReport, retErr error)
func (e *ttyExecer) applyVMProvisionArtifacts(ctx context.Context, plan vmProvisionPlan, ui *vmProvisionUI) (networkTouched bool, err error)
func (e *ttyExecer) installVMSystemdUnit(plan vmProvisionPlan, ui *vmProvisionUI) (unitTouched bool, err error)
func (e *ttyExecer) startVMAfterProvision(ctx context.Context, plan vmProvisionPlan, ui *vmProvisionUI) (vmGuestReadyReport, error)
```

In `vmProvisionInputs`, replace:

```go
image, err := e.selectVMProvisionImage(ctx, flags, payload, e.newProgressUI("vm image"))
```

with:

```go
image, err := e.selectVMProvisionImage(ctx, flags, payload, ui)
```

- [ ] **Step 4: Create and use one VM run UI in `provisionVM`**

In `provisionVM`, after validation succeeds, create and start the UI:

```go
ui := newVMProvisionUI(e)
ui.Start()
defer ui.Stop()
```

Wrap input and plan creation with the resolve step:

```go
ui.StartStep(vmRunStepResolve)
inputs, err = e.vmProvisionInputs(flags, payload, ui.ImageProgress())
if err != nil {
	ui.FailStep(err.Error())
	return err
}
plan, err := e.newVMProvisionPlan(flags, payload, inputs.ServiceRoot, inputs.Shape, inputs.Image, svcNet, inputs.SSHKey)
if err != nil {
	ui.FailStep(err.Error())
	return err
}
ui.DoneStep(vmProvisionPlanDetail(plan))
```

Keep the existing tracing blocks around the same operations. Remove the old `e.printVMProvisionSummary(plan, payload)` call from `provisionVM`.

- [ ] **Step 5: Replace artifact progress prints with structured steps**

In `applyVMProvisionArtifacts`, replace the existing disk progress branch with:

```go
ui.StartStep(vmRunStepDisk)
if plan.Disk.Backend == vmDiskBackendZVOL {
	if err := runVMProvisionDiskPlanWithProgress(ctx, plan.Disk, vmProvisionDiskRunner, e.vmDiskProgressf); err != nil {
		ui.FailStep(err.Error())
		doneDisk()
		return networkTouched, err
	}
} else {
	if err := runVMProvisionDiskPlan(ctx, plan.Disk, vmProvisionDiskRunner); err != nil {
		ui.FailStep(err.Error())
		doneDisk()
		return networkTouched, err
	}
}
ui.DoneStep(formatVMProvisionBytes(plan.Shape.DiskBytes))
```

Use this mapping:

- Disk provisioning: `vmRunStepDisk`, detail `formatVMProvisionBytes(plan.Shape.DiskBytes)`.
- Metadata injection: `vmRunStepMetadata`, no success detail.
- Firecracker config: `vmRunStepConfig`, no success detail.
- Network setup: `vmRunStepNetwork`, detail `formatVMProvisionNetwork(plan.Network)`.

On each error path after a step starts, call `ui.FailStep(err.Error())` before returning the wrapped error.

Remove these direct progress calls from VM provisioning:

```go
e.vmProgressf("Preparing disk...\n")
e.vmProgressf("Injecting guest metadata...\n")
e.vmProgressf("Writing Firecracker config...\n")
e.vmProgressf("Configuring network...\n")
```

- [ ] **Step 6: Replace install/start/readiness progress prints with structured steps**

In `installVMSystemdUnit`, start and finish the install step around the existing unit-file and systemctl work:

```go
ui.StartStep(vmRunStepInstall)
if err := writeVMFile(plan.SystemdUnitInstallPath, []byte(plan.SystemdUnitContent), 0o644); err != nil {
	ui.FailStep(err.Error())
	doneWriteUnit()
	return unitTouched, fmt.Errorf("install VM systemd unit: %w", err)
}
unitTouched = true
doneWriteUnit()
systemctl := vmProvisionSystemctlFunc
if systemctl == nil {
	systemctl = runVMSystemctl
}
unit := filepath.Base(plan.SystemdUnitInstallPath)
doneReload := e.traceBlock("vm systemd daemon-reload")
if err := systemctl("daemon-reload"); err != nil {
	ui.FailStep(err.Error())
	doneReload()
	return unitTouched, err
}
doneReload()
doneEnable := e.traceBlock("vm systemd enable")
if err := systemctl("enable", unit); err != nil {
	ui.FailStep(err.Error())
	doneEnable()
	return unitTouched, err
}
doneEnable()
ui.DoneStep("")
```

In `startVMAfterProvision`, wrap start and readiness separately:

```go
ui.StartStep(vmRunStepStart)
if err := e.restartVMSystemdUnit(plan); err != nil {
	ui.FailStep(err.Error())
	return vmGuestReadyReport{}, err
}
ui.DoneStep("")

ui.StartStep(vmRunStepReadiness)
report, err := waitReady(ctx, plan.Service, plan.Network, readyBoundary)
if err != nil {
	ui.FailStep(err.Error())
	return vmGuestReadyReport{}, err
}
detail := ""
if report.IP.IsValid() {
	detail = report.IP.String()
}
ui.DoneStep(detail)
return report, nil
```

Remove these direct progress calls:

```go
e.vmProgressf("Installing VM service...\n")
e.vmProgressf("Starting VM...\n")
e.vmProgressf("Waiting for guest readiness...\n")
```

- [ ] **Step 7: Print final success footer from `finishVMProvision`**

In `finishVMProvision`, capture the readiness report:

```go
var ready vmGuestReadyReport
if err := e.commitVMProvisionForFinish(plan, payload, snapshotPolicyFlags); err != nil {
	return committed, ready, err
}
committed = true
if restart {
	ready, err = e.startVMAfterProvision(ctx, plan, ui)
	if err != nil {
		return committed, ready, err
	}
}
ui.PrintSuccess(plan, payload, ready)
return committed, ready, nil
```

Remove the old `e.printVMNextCommands(plan, restart)` call.

- [ ] **Step 8: Run the focused plain-output test**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMPlainProgressAndNextCommands -count=1
```

Expected: PASS.

- [ ] **Step 9: Run existing VM provision tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVM|TestVMZVOL|TestCleanupFailedNewVMProvisionRoot' -count=1
```

Expected: PASS or only failures directly caused by exact output strings that this task intentionally changed. Update those expected strings to the new structured output only when the behavior is correct.

- [ ] **Step 10: Commit Task 3**

Run:

```bash
but status -fv
```

Commit only `pkg/catch/vm_provision.go`, `pkg/catch/vm_provision_test.go`, and `pkg/catch/vm_provision_ui.go` if it changed with message:

```text
catch: route vm run through progress ui
```

Use the GitButler change IDs shown by `but status -fv`.

---

### Task 4: Add TTY, Quiet, Image Download, And Failure Regressions

**Files:**
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `pkg/catch/vm_image_test.go` if image progress expectations need a parent-UI assertion.

- [ ] **Step 1: Add TTY output test**

Add to `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMTTYProgressFooter(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	execer.isPty = true
	execer.hostLabel = "yeet-pve1"
	vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		return vmGuestReadyReport{IP: netip.MustParseAddr("10.0.4.80"), Interface: "eth1"}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc,lan", CPUs: 2, Memory: "2g", Disk: "16g", Restart: true}, testUbuntuVMPayload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"[+] yeet run devbox@yeet-pve1",
		"✔ Resolve VM plan",
		"✔ Prepare disk (16.0 GB)",
		"✔ Configure network (svc,lan)",
		"✔ Wait for guest readiness (10.0.4.80)",
		"✔ VM ready in ",
		"devbox@yeet-pve1",
		"SSH      yeet ssh devbox",
		"Console  yeet vm console devbox",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("TTY output missing %q:\n%s", want, text)
		}
	}
}
```

- [ ] **Step 2: Add quiet output test**

Add to `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMQuietSuppressesProgress(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	execer.progress = catchrpc.ProgressQuiet

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, testUbuntuVMPayload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("quiet VM output = %q, want empty", got)
	}
}
```

Add `github.com/yeetrun/yeet/pkg/catchrpc` to the imports if it is not present.

- [ ] **Step 3: Add readiness failure footer test**

Add to `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMReadinessFailurePrintsRecoveryCommands(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		return vmGuestReadyReport{}, errors.New("guest readiness timeout")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, testUbuntuVMPayload)
	if err == nil || !strings.Contains(err.Error(), "guest readiness timeout") {
		t.Fatalf("runVM error = %v, want guest readiness timeout", err)
	}

	text := out.String()
	for _, want := range []string{
		`status=err step="Wait for guest readiness" detail="guest readiness timeout"`,
		`status=info detail="VM service was created, but readiness did not complete."`,
		`status=info detail="Console  yeet vm console devbox"`,
		`status=info detail="Logs     yeet logs devbox"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("failure output missing %q:\n%s", want, text)
		}
	}
}
```

- [ ] **Step 4: Wire readiness failure footer**

In `provisionVM`, after `finishVMProvision` returns an error, print recovery commands when the VM was committed and the error came from start/readiness:

```go
committed, _, err := e.finishVMProvision(inputs.Context, plan, payload, flags.Restart, snapshotPolicyFlags, ui)
rollbackNewService = vmProvisionRollbackPendingAfterFinish(rollbackNewService, committed)
doneFinish()
if err != nil {
	if committed && flags.Restart {
		ui.PrintReadinessFailure(plan.Service)
	}
	return err
}
```

This is intentionally conservative: it prints recovery commands only when the service was committed and the caller asked to start the VM.

- [ ] **Step 5: Add image download parent-UI regression**

Add a test that proves VM image progress does not stop the parent run UI. Put it in `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMImageDownloadUsesRunProgressUI(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	vmImageEnsureFunc = func(_ context.Context, _ vmImageCache, _ string, ui ProgressUI) (vmImageAsset, error) {
		ui.Start()
		ui.StartStep("Download VM image")
		ui.UpdateDetail("50%")
		ui.DoneStep("10.0 MB @ 20.0 MB/s")
		ui.Stop()
		return fakeVMImageAsset(t)
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, testUbuntuVMPayload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		`status=running step="Download VM image"`,
		`status=ok step="Download VM image" detail="10.0 MB @ 20.0 MB/s"`,
		`status=running step="Prepare disk"`,
		`status=info detail="SSH      yeet ssh devbox"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("image progress output missing %q:\n%s", want, text)
		}
	}
}
```

- [ ] **Step 6: Run focused regression tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVM(TTYProgressFooter|QuietSuppressesProgress|ReadinessFailurePrintsRecoveryCommands|ImageDownloadUsesRunProgressUI|PlainProgressAndNextCommands)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Run broader catch tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 4**

Run:

```bash
but status -fv
```

Commit only the regression-test and implementation files touched by this task with message:

```text
catch: cover vm run progress output
```

Use the GitButler change IDs shown by `but status -fv`.

---

### Task 5: Verify Client Routing And Docker Output Are Unchanged

**Files:**
- Modify tests only if an assertion needs to be added:
  - `pkg/yeet/svc_cmd_payload_test.go`
  - `pkg/yeet/handle_svc_cmd_test.go`
  - `pkg/catch/run_ui_test.go`

- [ ] **Step 1: Add client-side guard for VM TTY routing if missing**

Check `pkg/yeet/svc_cmd_payload_test.go` for `TestTryRunVMPayloadExecsRemoteRunWithPayloadArgument`. If it already asserts `tty=true` for VM payloads, leave it unchanged. If the assertion is missing, add:

```go
if !tty {
	t.Fatal("tty = false, want true")
}
```

- [ ] **Step 2: Add Docker runUI unchanged regression**

If there is no test proving plain Docker deploy output remains structured, add this to `pkg/catch/run_ui_test.go`:

```go
func TestRunUIDockerPlainOutputRemainsStructured(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "api@yeet-pve1")

	ui.Start()
	ui.StartStep(runStepUpload)
	ui.DoneStep("701.00 B @ 1.90 KB/s")
	ui.StartStep(runStepDetect)
	ui.DoneStep("docker compose")
	ui.StartStep(runStepInstall)
	ui.DoneStep("")
	ui.Stop()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		`action=run service=api@yeet-pve1 status=running step="Upload payload"`,
		`action=run service=api@yeet-pve1 status=ok step="Upload payload" detail="701.00 B @ 1.90 KB/s"`,
		`action=run service=api@yeet-pve1 status=running step="Detect payload"`,
		`action=run service=api@yeet-pve1 status=ok step="Detect payload" detail="docker compose"`,
		`action=run service=api@yeet-pve1 status=running step="Install service"`,
		`action=run service=api@yeet-pve1 status=ok step="Install service"`,
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("plain docker output = %#v, want %#v", lines, want)
	}
}
```

- [ ] **Step 3: Run client and catch routing tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet ./pkg/catch -run 'TestTryRunVMPayloadExecsRemoteRunWithPayloadArgument|TestRunComposeTTYDependsOnTerminal|TestRunUIDockerPlainOutputRemainsStructured|TestRunVM' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit Task 5 if tests changed**

If this task changed test files, run:

```bash
but status -fv
```

Commit only those test files with message:

```text
test: guard run progress routing
```

Use the GitButler change IDs shown by `but status -fv`. If no files changed, do not create a commit.

---

### Task 6: Final Verification And Live Smoke

**Files:**
- No source changes expected.

- [ ] **Step 1: Run package tests**

Run:

```bash
mise exec -- go test ./pkg/tui ./pkg/catch ./pkg/yeet ./cmd/yeet -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full test suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Install local catch on `yeet-pve1` for smoke testing**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
```

Expected: install succeeds and reports the catch service installed on `root@pve1`.

- [ ] **Step 5: Run a disposable VM smoke test**

Run:

```bash
svc=codex-vm-output-$(date +%Y%m%d%H%M%S)
printf '%s\n' "$svc" > /tmp/yeet-vm-output-smoke-name
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet run "$svc" vm://ubuntu/26.04 --net=svc,lan --image-policy=cached --cpus=1 --memory=1g --disk=8g
```

Expected:

- TTY output shows the yeet header.
- VM steps render with checkmarks.
- The final footer includes `SSH      yeet ssh <svc>` and `Console  yeet vm console <svc>`.
- No raw legacy lines such as `Preparing disk...` or `VM <svc> is running.` appear.

- [ ] **Step 6: Verify disposable VM is usable**

Run:

```bash
svc=$(cat /tmp/yeet-vm-output-smoke-name)
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh "$svc" -- sh -lc 'hostname; ip route get 1.1.1.1'
```

Expected: command exits 0 and prints the VM hostname plus a valid route.

- [ ] **Step 7: Clean up disposable VM**

Run:

```bash
svc=$(cat /tmp/yeet-vm-output-smoke-name)
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet remove "$svc" --yes --clean-data --clean-config
rm -f /tmp/yeet-vm-output-smoke-name yeet.toml
ssh root@pve1 "test ! -e /root/data/services/$svc && echo remote-service-root-removed"
```

Expected: removal succeeds and the final line is `remote-service-root-removed`.

- [ ] **Step 8: Final GitButler status**

Run:

```bash
but status -fv
git status --short --branch
```

Expected: no unassigned changes except intentional committed branch changes on the current session branch.

---

## Self-Review Notes

- Spec coverage: The plan covers TTY polish, non-TTY structured output, quiet mode, failure recovery output, image progress integration, unchanged Docker behavior, and live VM smoke testing.
- Scope: The plan intentionally does not add typed RPC progress events or browser deploy rendering. Those remain future work built on the structured catch-side model.
- Type consistency: The plan uses existing `runUI`, `ProgressUI`, `vmProvisionPlan`, `vmShape`, `vmImageAsset`, `vmGuestReadyReport`, and `catchrpc.ProgressMode` names from the repo.
