# ZFS VM Image Bases Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make ZFS VM deploys reuse shared same-pool image bases so repeated VM creation skips rootfs writes while preserving existing VM image-version behavior.

**Architecture:** Keep the raw disk path unchanged and change only the ZFS zvol base layout. Catch derives a shared base zvol from the service-root pool and image version, lazily creates the base snapshot on first use, then clones per-service VM disks from it. Cleanup remains service-root scoped, so new shared bases live outside the data removed by `yeet rm --clean-data`.

**Tech Stack:** Go, catch VM provisioning, ZFS zvol commands, Firecracker VM service roots, existing progress UI, README and website docs.

---

## Scope

This plan implements the first shared-base pass. It does not add DB schema
fields or migrations. The VM image version and runtime disk path already remain
persisted, and the shared base dataset is deterministic from the ZFS service
root pool plus the VM image version.

Use `TMPDIR=/tmp/yeet-tests` for full test and pre-commit runs on macOS. The VM
console socket tests can exceed Unix socket path limits under long default
macOS temp roots.

## File Structure

- Modify `pkg/catch/vm_provision.go`: derive same-pool shared ZFS image bases and route disk progress through VM provisioning output.
- Modify `pkg/catch/vm_provision_test.go`: cover shared base derivation through a real VM provision plan and progress output.
- Modify `pkg/catch/vm_storage.go`: split ZFS disk steps into user-facing phases, report progress once per phase, and include phase labels in setup failures.
- Modify `pkg/catch/vm_storage_test.go`: cover shared snapshot clone commands, base-skip behavior, base-create behavior, and phase labels.
- Modify `pkg/catch/catch_test.go`: assert clean-data destroys only the service-root dataset and not the shared image base dataset.
- Modify `pkg/catch/vm_image_test.go` and `pkg/catch/vm_provision_test.go`: keep image download progress from appearing when the cached image is current.
- Modify `README.md`: document ZFS shared image bases in the experimental VM section.
- Modify `website/docs/concepts/service-types.mdx`: document host image cache behavior for ZFS VM bases.
- Modify `website/docs/cli/yeet-cli.mdx`: document `vm images` file-cache behavior and first ZFS base preparation.

## Task 1: Derive Shared Same-Pool ZFS Base Datasets

**Files:**
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write failing base-derivation tests**

Add these tests near the existing VM ZFS provision tests in
`pkg/catch/vm_provision_test.go`:

```go
func TestVMZVOLBaseDatasetUsesServiceRootPool(t *testing.T) {
	root := resolvedServiceRoot{
		Root:    "/flash/yeet/vms/devbox",
		Dataset: "flash/yeet/vms/devbox",
		ZFS:     true,
	}

	got := vmZVOLBaseDataset(root, "ubuntu-26.04-amd64-v1")
	want := "flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root"
	if got != want {
		t.Fatalf("vmZVOLBaseDataset = %q, want %q", got, want)
	}
}

func TestVMZVOLBaseDatasetUsesTargetPoolForDifferentServiceRoot(t *testing.T) {
	root := resolvedServiceRoot{
		Root:    "/tank/apps/devbox",
		Dataset: "tank/apps/devbox",
		ZFS:     true,
	}

	got := vmZVOLBaseDataset(root, "ubuntu-26.04-amd64-v3")
	want := "tank/yeet/vm-images/ubuntu-26.04-amd64-v3/root"
	if got != want {
		t.Fatalf("vmZVOLBaseDataset = %q, want %q", got, want)
	}
}

func TestVMZVOLBaseDatasetFallbackForMissingDataset(t *testing.T) {
	root := resolvedServiceRoot{Root: "/srv/yeet/services/devbox"}

	got := vmZVOLBaseDataset(root, "ubuntu-26.04-amd64-v1")
	want := "yeet/vm-images/ubuntu-26.04-amd64-v1/root"
	if got != want {
		t.Fatalf("vmZVOLBaseDataset = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the failing tests**

Run:

```bash
go test ./pkg/catch -run 'TestVMZVOLBaseDataset' -count=1
```

Expected before implementation: FAIL because `vmZVOLBaseDataset` returns a
legacy path like `flash/yeet/vms/devbox/base/ubuntu-26.04-amd64-v1`.

- [ ] **Step 3: Implement the same-pool base helper**

Replace `vmZVOLBaseDataset` in `pkg/catch/vm_provision.go` with this helper set:

```go
func vmZVOLBaseDataset(root resolvedServiceRoot, version string) string {
	dataset := strings.Trim(root.Dataset, "/")
	pool := vmZVOLPoolName(dataset)
	if pool == "" {
		pool = "yeet"
	}
	return pool + "/yeet/vm-images/" + version + "/root"
}

func vmZVOLPoolName(dataset string) string {
	dataset = strings.Trim(dataset, "/")
	if dataset == "" {
		return ""
	}
	if idx := strings.Index(dataset, "/"); idx > 0 {
		return dataset[:idx]
	}
	return dataset
}
```

Leave `vmZVOLRootDataset` unchanged so VM clone datasets still live under the
service root:

```go
func vmZVOLRootDataset(root resolvedServiceRoot, service string) string {
	dataset := strings.Trim(root.Dataset, "/")
	if dataset == "" {
		dataset = "yeet/vms"
	}
	return dataset + "/vm/" + shortVMName(service) + "/root"
}
```

- [ ] **Step 4: Update the provision integration assertion**

In `TestRunVMZVOLProvisionUsesDevicePathForFirecracker`, replace the clone
command expectation with the shared base snapshot:

```go
wantDataset := serviceDataset + "/vm/" + shortVMName("svc") + "/root"
wantDevice := "/dev/zvol/" + wantDataset
wantBase := "flash/yeet/vm-images/" + defaultVMImageVersion + "/root"
wantSnapshot := wantBase + "@" + defaultVMImageVersion
if svc.VM.Disk.Path != wantDevice {
	t.Fatalf("db disk path = %q, want %q", svc.VM.Disk.Path, wantDevice)
}
assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), `"path_on_host": "`+wantDevice+`"`)
foundClone := false
for _, command := range diskCommands {
	if reflect.DeepEqual(command, []string{"zfs", "clone", wantSnapshot, wantDataset}) {
		foundClone = true
	}
	if len(command) >= 3 && strings.Contains(strings.Join(command, " "), serviceDataset+"/base/") {
		t.Fatalf("disk command used legacy per-service base: %#v", command)
	}
}
if !foundClone {
	t.Fatalf("clone command from %q to %q not found in %#v", wantSnapshot, wantDataset, diskCommands)
}
```

- [ ] **Step 5: Verify shared base derivation passes**

Run:

```bash
go test ./pkg/catch -run 'TestVMZVOLBaseDataset|TestRunVMZVOLProvisionUsesDevicePathForFirecracker' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 1**

Run:

```bash
git add pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go
git commit -m "pkg/catch: share zfs vm image bases by pool"
```

## Task 2: Split ZFS Disk Preparation Into Progress Phases

**Files:**
- Modify: `pkg/catch/vm_storage.go`
- Modify: `pkg/catch/vm_storage_test.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write failing storage progress tests**

Add these tests to `pkg/catch/vm_storage_test.go`:

```go
func TestRunVMProvisionDiskPlanReportsOnlyCloneProgressWhenBaseExists(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	var labels []string
	var commands [][]string

	err := runVMProvisionDiskPlanWithProgress(context.Background(), plan, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}, func(label string) {
		labels = append(labels, label)
	})
	if err != nil {
		t.Fatalf("runVMProvisionDiskPlanWithProgress: %v", err)
	}

	wantLabels := []string{"Cloning VM disk", "Expanding filesystem"}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", labels, wantLabels)
	}
	for _, command := range commands {
		if len(command) > 0 && command[0] == "dd" {
			t.Fatalf("base image write should be skipped when snapshot exists: %#v", commands)
		}
	}
}

func TestRunVMProvisionDiskPlanReportsBaseProgressWhenSnapshotMissing(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	var labels []string
	var commands [][]string

	err := runVMProvisionDiskPlanWithProgress(context.Background(), plan, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		if len(commands) == 1 {
			return errors.New("snapshot missing")
		}
		return nil
	}, func(label string) {
		labels = append(labels, label)
	})
	if err != nil {
		t.Fatalf("runVMProvisionDiskPlanWithProgress: %v", err)
	}

	wantLabels := []string{
		"Preparing ZFS image base",
		"Writing image to ZFS base",
		"Cloning VM disk",
		"Expanding filesystem",
	}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", labels, wantLabels)
	}
}

func testZVOLProgressDiskPlan() vmDiskPlan {
	return vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/vm/d-ea1055/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
}
```

- [ ] **Step 2: Run the failing storage progress tests**

Run:

```bash
go test ./pkg/catch -run 'TestRunVMProvisionDiskPlanReports' -count=1
```

Expected before implementation: FAIL to compile because
`runVMProvisionDiskPlanWithProgress` does not exist.

- [ ] **Step 3: Add phase constants and progress labels**

In `pkg/catch/vm_storage.go`, replace the phase constants with:

```go
const (
	vmDiskPhaseRaw             = "raw"
	vmDiskPhaseZVOLBasePrepare = "zvol-base-prepare"
	vmDiskPhaseZVOLBaseWrite   = "zvol-base-write"
	vmDiskPhaseZVOLClone       = "zvol-clone"
	vmDiskPhaseZVOLResize      = "zvol-resize"
)
```

Add this label helper:

```go
func vmDiskProgressLabel(phase string) string {
	switch phase {
	case vmDiskPhaseRaw:
		return "Preparing disk"
	case vmDiskPhaseZVOLBasePrepare:
		return "Preparing ZFS image base"
	case vmDiskPhaseZVOLBaseWrite:
		return "Writing image to ZFS base"
	case vmDiskPhaseZVOLClone:
		return "Cloning VM disk"
	case vmDiskPhaseZVOLResize:
		return "Expanding filesystem"
	default:
		return ""
	}
}
```

- [ ] **Step 4: Assign precise phases to ZFS commands**

Update `ZVOLBaseSteps`:

```go
func (p vmDiskPlan) ZVOLBaseSteps() ([]vmDiskPlanStep, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Backend != vmDiskBackendZVOL {
		return nil, fmt.Errorf("VM disk backend %q does not use zvol base setup", p.Backend)
	}
	snap := p.ZVOLSnapshotName()
	size := fmt.Sprintf("%d", p.zvolBaseBytes())
	return append(zfsParentDatasetSteps(vmDiskPhaseZVOLBasePrepare, p.BaseDataset),
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBasePrepare, Command: []string{"zfs", "create", "-s", "-V", size, p.BaseDataset}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBasePrepare, Command: vmZVOLSettleCommand()},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBaseWrite, Command: []string{"dd", "if=" + p.BaseRootFS, "of=/dev/zvol/" + p.BaseDataset, "bs=16M", "status=none"}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBaseWrite, Command: []string{"zfs", "snapshot", snap}},
	), nil
}
```

Update `ZVOLCloneSteps`:

```go
func (p vmDiskPlan) ZVOLCloneSteps() ([]vmDiskPlanStep, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Backend != vmDiskBackendZVOL {
		return nil, fmt.Errorf("VM disk backend %q does not use zvol clone setup", p.Backend)
	}
	snap := p.ZVOLSnapshotName()
	size := fmt.Sprintf("%d", p.Bytes)
	return append(zfsParentDatasetSteps(vmDiskPhaseZVOLClone, p.Path),
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLClone, Command: []string{"zfs", "clone", snap, p.Path}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLClone, Command: []string{"zfs", "set", "volsize=" + size, p.Path}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLClone, Command: vmZVOLSettleCommand()},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLResize, Command: []string{"resize2fs", vmDiskPathForRuntime(p)}},
	), nil
}
```

- [ ] **Step 5: Add progress-aware disk execution**

Add this wrapper and update `runVMDiskStepsWithRunner` in
`pkg/catch/vm_storage.go`:

```go
func runVMProvisionDiskPlanWithProgress(ctx context.Context, plan vmDiskPlan, runner vmCommandRunner, progress func(string)) error {
	if runner == nil {
		runner = runVMCommand
	}
	if plan.Backend != vmDiskBackendZVOL {
		steps, err := plan.Steps()
		if err != nil {
			return err
		}
		return runVMDiskStepsWithRunner(ctx, plan, steps, runner, progress)
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	check := []string{"zfs", "list", "-H", "-o", "name", plan.ZVOLSnapshotName()}
	baseExists := runner(ctx, check) == nil
	var steps []vmDiskPlanStep
	var err error
	if !baseExists {
		steps, err = plan.ZVOLBaseSteps()
		if err != nil {
			return err
		}
	}
	clone, err := plan.ZVOLCloneSteps()
	if err != nil {
		return err
	}
	steps = append(steps, clone...)
	return runVMDiskStepsWithRunner(ctx, plan, steps, runner, progress)
}

func runVMProvisionDiskPlan(ctx context.Context, plan vmDiskPlan, runner vmCommandRunner) error {
	return runVMProvisionDiskPlanWithProgress(ctx, plan, runner, nil)
}

func runVMDiskStepsWithRunner(ctx context.Context, plan vmDiskPlan, steps []vmDiskPlanStep, runner vmCommandRunner, progress func(string)) error {
	lastLabel := ""
	for _, step := range steps {
		label := vmDiskProgressLabel(step.Phase)
		if progress != nil && label != "" && label != lastLabel {
			progress(label)
			lastLabel = label
		}
		command := step.Command
		if err := runner(ctx, command); err != nil {
			return vmSetupIncompleteError{DiskPath: plan.Path, Phase: step.Phase, Command: append([]string(nil), command...), Err: err}
		}
	}
	return nil
}
```

Update `runVMDiskPlanWithRunner` to pass nil progress:

```go
func runVMDiskPlanWithRunner(ctx context.Context, plan vmDiskPlan, runner vmCommandRunner) error {
	if runner == nil {
		runner = runVMCommand
	}
	steps, err := plan.Steps()
	if err != nil {
		return err
	}
	return runVMDiskStepsWithRunner(ctx, plan, steps, runner, nil)
}
```

- [ ] **Step 6: Route provision output through progress labels**

In `pkg/catch/vm_provision.go`, update `applyVMProvisionArtifacts`:

```go
func (e *ttyExecer) applyVMProvisionArtifacts(ctx context.Context, plan vmProvisionPlan) error {
	if plan.Disk.Backend == vmDiskBackendZVOL {
		if err := runVMProvisionDiskPlanWithProgress(ctx, plan.Disk, vmProvisionDiskRunner, e.vmDiskProgressf); err != nil {
			return err
		}
	} else {
		e.vmProgressf("Preparing disk...\n")
		if err := runVMProvisionDiskPlan(ctx, plan.Disk, vmProvisionDiskRunner); err != nil {
			return err
		}
	}
	if err := writeVMMetadata(plan.ServiceRoot.Root, plan.Metadata); err != nil {
		return fmt.Errorf("write VM metadata: %w", err)
	}
	injectMetadata := vmProvisionMetadataInjector
	if injectMetadata == nil {
		injectMetadata = injectVMMetadataIntoRootFS
	}
	e.vmProgressf("Injecting guest metadata...\n")
	if err := injectMetadata(ctx, plan.DiskPath, plan.Metadata); err != nil {
		return fmt.Errorf("inject VM metadata: %w", err)
	}
	e.vmProgressf("Writing Firecracker config...\n")
	if err := writeVMFile(plan.FirecrackerConfigPath, plan.FirecrackerConfig, 0o644); err != nil {
		return fmt.Errorf("write Firecracker config: %w", err)
	}
	e.vmProgressf("Configuring network...\n")
	if err := plan.Network.ExecuteSetup(vmProvisionNetworkRunner); err != nil {
		return fmt.Errorf("set up VM network: %w", err)
	}
	if err := writeVMFile(plan.SystemdUnitStagePath, []byte(plan.SystemdUnitContent), 0o644); err != nil {
		return fmt.Errorf("stage VM systemd unit: %w", err)
	}
	return nil
}

func (e *ttyExecer) vmDiskProgressf(label string) {
	e.vmProgressf("%s...\n", label)
}
```

- [ ] **Step 7: Update phase expectations in storage tests**

Update existing `pkg/catch/vm_storage_test.go` expectations that reference
`vmDiskPhaseZVOLBase` so base parent/create/settle use
`vmDiskPhaseZVOLBasePrepare`, base dd/snapshot use
`vmDiskPhaseZVOLBaseWrite`, clone commands use `vmDiskPhaseZVOLClone`, and
resize uses `vmDiskPhaseZVOLResize`.

- [ ] **Step 8: Add VM provision output coverage**

Add this test to `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMZVOLProvisionPrintsDiskSubsteps(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	serviceDataset := "flash/yeet/vms/devbox"
	if err := os.MkdirAll(serviceRoot, 0o755); err != nil {
		t.Fatalf("mkdir service root: %v", err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		serviceDataset: {Mountpoint: serviceRoot, Exists: true},
	}).Run
	var diskCommands [][]string
	vmProvisionDiskRunner = func(_ context.Context, cmd []string) error {
		diskCommands = append(diskCommands, append([]string(nil), cmd...))
		if len(diskCommands) == 1 {
			return errors.New("snapshot missing")
		}
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", ZFS: true, ServiceRoot: serviceDataset}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"Preparing ZFS image base",
		"Writing image to ZFS base",
		"Cloning VM disk",
		"Expanding filesystem",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}
```

- [ ] **Step 9: Verify progress behavior**

Run:

```bash
go test ./pkg/catch -run 'TestRunVMProvisionDiskPlanReports|TestVMZVOLPlanCreatesSparseClone|TestVMZVOLCloneStepsResizeFilesystemAfterExpandingVolume|TestRunVMZVOLProvisionPrintsDiskSubsteps|TestRunVMPrintsProgressAndNextCommands' -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit Task 2**

Run:

```bash
git add pkg/catch/vm_storage.go pkg/catch/vm_storage_test.go pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go
git commit -m "pkg/catch: show zfs vm disk phases"
```

## Task 3: Include Disk Phase Context in Setup Failures

**Files:**
- Modify: `pkg/catch/vm_storage.go`
- Modify: `pkg/catch/vm_storage_test.go`

- [ ] **Step 1: Write the failing phase-error test**

Add this test to `pkg/catch/vm_storage_test.go`:

```go
func TestRunVMProvisionDiskPlanIncludesPhaseInSetupError(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	wantErr := errors.New("write failed")

	err := runVMProvisionDiskPlanWithProgress(context.Background(), plan, func(_ context.Context, command []string) error {
		if len(command) > 0 && command[0] == "dd" {
			return wantErr
		}
		if len(command) == 5 && command[0] == "zfs" && command[1] == "list" {
			return errors.New("snapshot missing")
		}
		return nil
	}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped write failure", err)
	}
	if !strings.Contains(err.Error(), "Writing image to ZFS base") {
		t.Fatalf("error missing phase label: %v", err)
	}
}
```

- [ ] **Step 2: Run the failing phase-error test**

Run:

```bash
go test ./pkg/catch -run TestRunVMProvisionDiskPlanIncludesPhaseInSetupError -count=1
```

Expected before implementation: FAIL because the error includes the command and
disk path but not the user-facing phase label.

- [ ] **Step 3: Extend `vmSetupIncompleteError`**

Update the struct and `Error` method in `pkg/catch/vm_storage.go`:

```go
type vmSetupIncompleteError struct {
	DiskPath string
	Phase    string
	Command  []string
	Err      error
}

func (e vmSetupIncompleteError) Error() string {
	command := formatVMCommandArgv(e.Command)
	phase := vmDiskProgressLabel(e.Phase)
	if phase != "" {
		if e.DiskPath != "" && command != "" {
			return fmt.Sprintf("VM setup incomplete during %s for disk %s after %s: %v", phase, e.DiskPath, command, e.Err)
		}
		if e.DiskPath != "" {
			return fmt.Sprintf("VM setup incomplete during %s for disk %s: %v", phase, e.DiskPath, e.Err)
		}
		if command != "" {
			return fmt.Sprintf("VM setup incomplete during %s after %s: %v", phase, command, e.Err)
		}
		return fmt.Sprintf("VM setup incomplete during %s: %v", phase, e.Err)
	}
	if e.DiskPath == "" {
		if command == "" {
			return fmt.Sprintf("VM setup incomplete: %v", e.Err)
		}
		return fmt.Sprintf("VM setup incomplete after %s: %v", command, e.Err)
	}
	if command == "" {
		return fmt.Sprintf("VM setup incomplete for disk %s: %v", e.DiskPath, e.Err)
	}
	return fmt.Sprintf("VM setup incomplete for disk %s after %s: %v", e.DiskPath, command, e.Err)
}
```

The `runVMDiskStepsWithRunner` change from Task 2 already populates `Phase`.

- [ ] **Step 4: Verify phase-error behavior**

Run:

```bash
go test ./pkg/catch -run 'TestRunVMProvisionDiskPlanIncludesPhaseInSetupError|TestRunVMDiskPlanPreservesDiskOnSetupError' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

Run:

```bash
git add pkg/catch/vm_storage.go pkg/catch/vm_storage_test.go
git commit -m "pkg/catch: include vm disk phase in setup errors"
```

## Task 4: Lock Cleanup Ownership Boundaries

**Files:**
- Modify: `pkg/catch/catch_test.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Add cleanup boundary regression test**

Add this test near the existing ZFS remove tests in `pkg/catch/catch_test.go`:

```go
func TestRemoveVMCleanDataDestroysServiceRootNotSharedImageBase(t *testing.T) {
	server := newTestServer(t)
	name := "devbox"
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, name)
	if err := os.MkdirAll(serviceRoot, 0o755); err != nil {
		t.Fatalf("mkdir service root: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:           name,
			ServiceType:    db.ServiceTypeVM,
			ServiceRoot:    serviceRoot,
			ServiceRootZFS: "flash/yeet/vms/devbox",
			VM: &db.VMConfig{
				Image: db.VMImageConfig{Payload: vmUbuntu2604Payload, Version: defaultVMImageVersion},
				Disk: db.VMDiskConfig{
					Backend: vmDiskBackendZVOL,
					Bytes:   128 << 30,
					Path:    "/dev/zvol/flash/yeet/vms/devbox/vm/d-ea1055/root",
				},
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	var calls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", "", nil
	}

	if _, err := server.RemoveServiceWithOptions(name, RemoveOptions{CleanData: true}); err != nil {
		t.Fatalf("RemoveServiceWithOptions: %v", err)
	}

	want := [][]string{{"destroy", "-R", "flash/yeet/vms/devbox"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("zfs calls = %#v, want %#v", calls, want)
	}
	for _, call := range calls {
		if strings.Contains(strings.Join(call, " "), "flash/yeet/vm-images") {
			t.Fatalf("cleanup touched shared image base: %#v", calls)
		}
	}
}
```

- [ ] **Step 2: Add provision boundary assertion**

In `TestRunVMZVOLProvisionUsesDevicePathForFirecracker`, add this assertion
after `wantBase` is computed:

```go
if strings.HasPrefix(wantBase, serviceDataset+"/") {
	t.Fatalf("shared base %q must not be under service root dataset %q", wantBase, serviceDataset)
}
```

- [ ] **Step 3: Run cleanup boundary tests**

Run:

```bash
go test ./pkg/catch -run 'TestRemoveVMCleanDataDestroysServiceRootNotSharedImageBase|TestRunVMZVOLProvisionUsesDevicePathForFirecracker|TestRemoveServiceCleanDataDestroysZFSServiceRoot|TestRemoveServiceCleanDataFailsBeforeDBRemovalWhenZFSDestroyFails' -count=1
```

Expected: PASS after Task 1. If the new cleanup test fails, keep
`RemoveServiceWithOptions` destroying only `ServiceRootZFS`; do not add any
cleanup call for the shared base dataset.

- [ ] **Step 4: Commit Task 4**

Run:

```bash
git add pkg/catch/catch_test.go pkg/catch/vm_provision_test.go
git commit -m "pkg/catch: preserve shared vm image bases on remove"
```

## Task 5: Guard Image Download Progress and Update Docs

**Files:**
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `README.md`
- Modify: `website/docs/concepts/service-types.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`

- [ ] **Step 1: Add current-cache progress regression test**

Add this test near `TestRunVMCurrentImageUsesCachedAssetWithoutEnsuring` in
`pkg/catch/vm_provision_test.go`:

```go
func TestRunVMCurrentImageDoesNotPrintDownloadProgress(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	seedCachedVMProvisionImage(t, server, defaultVMImageVersion)
	stubVMProvisionImageState(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: defaultVMImageVersion,
		LatestVersion: defaultVMImageVersion,
		State:         vmImageCacheCurrent,
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion),
		ManifestURL:   defaultVMImageManifestURL,
	})
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		t.Fatal("vmImageEnsureFunc called for current VM image cache")
		return vmImageAsset{}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if strings.Contains(out.String(), "Download VM image") {
		t.Fatalf("current cache printed download progress:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run the image progress regression test**

Run:

```bash
go test ./pkg/catch -run 'TestRunVMCurrentImageDoesNotPrintDownloadProgress|TestRunVMCurrentImageUsesCachedAssetWithoutEnsuring|TestRunVMMissingImageAutomaticallyEnsures' -count=1
```

Expected: PASS. If it fails, fix `selectVMProvisionImage` so the current-cache
branch uses `cachedVMImageAsset` and does not call `vmImageEnsureFunc`.

- [ ] **Step 3: Update README VM cache wording**

In `README.md`, replace the VM image cache paragraph with:

```markdown
VM image bundles are cached on each catch host. `yeet vm images` shows whether
the cached image is current, stale, or missing; `yeet vm images update`
refreshes the host file cache used for future VM creates. A missing image is
downloaded automatically on the first VM create. Existing VM disks are not
rewritten. When creating a VM with a stale cached image, interactive runs prompt
by default; non-interactive runs require `--image-policy=update` or
`--image-policy=cached`.

For ZFS-backed VMs, the first VM created on a pool for an image version prepares
a shared ZFS image base on that pool. Later VMs on the same pool and image
version clone that shared base instead of writing the root filesystem again.
`yeet rm --clean-data devbox` removes the VM's service data and clone, not the
shared image base.
```

- [ ] **Step 4: Update website service type docs**

In `website/docs/concepts/service-types.mdx`, replace the VM image cache
paragraph with:

```markdown
Use `--cpus`, `--memory`, and `--disk` to override the host-based defaults.
VM image bundles are cached per host. Use `yeet vm images` to inspect the cache
and `yeet vm images update` to refresh the image used for future VM creates.
Refreshing the cache does not mutate existing VM disks. A missing image is
downloaded automatically on the first VM create. When creating a VM with a stale
cached image, interactive runs prompt by default; non-interactive runs require
`--image-policy=update` or `--image-policy=cached`.

For ZFS-backed VMs, the first VM on a pool and image version prepares a shared
ZFS image base. Later VMs on that pool clone the shared base, and
`yeet rm --clean-data devbox` removes the VM clone without deleting the shared
base.
```

- [ ] **Step 5: Update CLI docs for `vm images`**

In `website/docs/cli/yeet-cli.mdx`, replace the paragraph under `### vm images`
with:

```markdown
`yeet vm images` shows the host file-cache state for `vm://ubuntu/26.04`.
`yeet vm images update` downloads and verifies the latest image bundle used for
future VM creates. A missing image is downloaded automatically on the first VM
create. Existing VMs are not rebuilt or modified.

For ZFS-backed VMs, the first create on a pool and image version prepares a
shared ZFS image base. Later VMs on the same pool clone that base, so normal
creates skip the base-image write.
```

- [ ] **Step 6: Verify docs diffs**

Run:

```bash
git diff --check
git -C website diff --check
```

Expected: both commands exit 0.

- [ ] **Step 7: Commit Task 5**

Commit website changes inside the submodule first:

```bash
git -C website add docs/concepts/service-types.mdx docs/cli/yeet-cli.mdx
git -C website commit -m "docs: explain zfs vm image bases"
```

Then commit the root README and updated website pointer:

```bash
git add README.md pkg/catch/vm_provision_test.go website
git commit -m "docs: document zfs vm image base reuse"
```

## Task 6: Local Quality Gate

**Files:**
- Verify all touched files.

- [ ] **Step 1: Run focused catch tests**

Run:

```bash
go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go tests with a short temp root**

Run:

```bash
mkdir -p /tmp/yeet-tests
TMPDIR=/tmp/yeet-tests go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit with a short temp root**

Run:

```bash
TMPDIR=/tmp/yeet-tests pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Commit any deterministic formatting or docs-hook changes**

Run:

```bash
git status --short
```

If the status output shows tracked hook changes inside `pkg/catch`, `README.md`,
or `website`, commit only those tracked updates:

```bash
git add -u pkg/catch README.md website
git commit -m "chore: apply zfs vm base formatting"
```

If `git status --short` is clean after the hooks, skip this step.

## Task 7: Live ZFS VM End-to-End Validation on `yeet-lab`

**Files:**
- No source edits in this task. If live validation reveals a bug, return to the
  task that owns the failing behavior, add or adjust the focused test there,
  make the fix, rerun Tasks 6 and 7, and commit through that task's commit
  step.

- [ ] **Step 1: Deploy the updated catch binary to lab-host**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet init yeet-lab
```

Expected: catch installs successfully and `go run ./cmd/yeet status` can reach
the host.

- [ ] **Step 2: Create the first ZFS VM on the `flash` pool**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet run vmzfs-a vm://ubuntu/26.04 --service-root=flash/yeet/vms/vmzfs-a --zfs --disk=128g --net=lan
```

Expected output includes:

```text
Preparing ZFS image base...
Writing image to ZFS base...
Cloning VM disk...
Expanding filesystem...
VM vmzfs-a is running.
```

- [ ] **Step 3: Verify the shared base and clone on the host**

Run:

```bash
ssh yeet-lab zfs list -H -o name,origin,volsize flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root flash/yeet/vms/vmzfs-a/vm
```

Expected: output includes the shared base
`flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root` and a VM clone whose origin is
`flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root@ubuntu-26.04-amd64-v1`.

- [ ] **Step 4: Create a second ZFS VM on the same pool**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet run vmzfs-b vm://ubuntu/26.04 --service-root=flash/yeet/vms/vmzfs-b --zfs --disk=128g --net=lan
```

Expected output includes `Cloning VM disk...` and `Expanding filesystem...`.
Expected output does not include `Writing image to ZFS base...`.

- [ ] **Step 5: Remove both VMs with clean data**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet rm vmzfs-a --clean-data -y
CATCH_HOST=yeet-lab go run ./cmd/yeet rm vmzfs-b --clean-data -y
```

Expected: both commands remove the VM services without ZFS busy errors.

- [ ] **Step 6: Verify the shared base remains**

Run:

```bash
ssh yeet-lab zfs list -H -o name flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root
```

Expected: the shared base zvol still exists.

- [ ] **Step 7: Recreate one VM and confirm the base write is skipped**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet run vmzfs-a vm://ubuntu/26.04 --service-root=flash/yeet/vms/vmzfs-a --zfs --disk=128g --net=lan
```

Expected output includes `Cloning VM disk...` and `Expanding filesystem...`.
Expected output does not include `Download VM image` or
`Writing image to ZFS base...`.

- [ ] **Step 8: Final cleanup**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet rm vmzfs-a --clean-data -y
```

Expected: VM service and clone are removed. The shared image base remains.

## Task 8: Push Main

**Files:**
- Verify repository state only.

- [ ] **Step 1: Check status and recent commits**

Run:

```bash
git status --short
git log --oneline -5
git -C website status --short --branch
```

Expected: root working tree is clean. Website status is clean on its branch
after Task 5 pushed or ready to push.

- [ ] **Step 2: Push website docs if Task 5 changed website**

Run:

```bash
git -C website push
```

Expected: website docs commit is pushed.

- [ ] **Step 3: Push root main**

Run:

```bash
git push origin main
```

Expected: root commits are pushed to `origin/main`.

## Self-Review

- Spec coverage: Tasks 1 and 2 implement same-pool shared bases, lazy base
  creation, and progress for base, clone, and resize. Task 3 covers recoverable
  phase-specific errors. Task 4 covers cleanup ownership and legacy-safe remove
  behavior. Task 5 covers image download progress and docs. Tasks 6 and 7 cover
  local and live validation.
- Placeholder scan: The plan has no placeholder markers, omitted test names, or
  undefined task references.
- Type consistency: The plan uses existing `resolvedServiceRoot`, `vmDiskPlan`,
  `vmDiskPlanStep`, `vmCommandRunner`, `vmSetupIncompleteError`,
  `vmDiskBackendZVOL`, `defaultVMImageVersion`, and `RemoveOptions` names.
  The new helper names are introduced before later tasks reference them.
