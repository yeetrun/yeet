# Disk-Only VM Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove all Firecracker memory/VMM-state checkpoint code and retain only crash-consistent ZFS disk recovery points for VMs.

**Architecture:** Catch pauses a running VM, takes one atomic ZFS snapshot of its zvol, and resumes the VM through a cancellation-resistant recovery context. A pre-install inventory blocks rollout when legacy memory files or `com.yeetrun:checkpoint=full` snapshots exist; there is no reader or restore compatibility path after rollout.

**Tech Stack:** Go, Firecracker HTTP API, systemd, ZFS, shared Yeet CLI parser, website MDX.

## Global Constraints

- Do not call Firecracker `CreateSnapshot` or `LoadSnapshot`, even as a temporary disk-flush mechanism.
- VM disk recovery points are crash-consistent, not application-consistent.
- ZFS zvol snapshots remain supported; raw VM disks remain unsupported for snapshot creation.
- Snapshot create/list/inspect/protect/unprotect/clone/restore/remove/retention must remain supported for disk recovery points.
- A running VM must always receive a best-effort resume through `context.WithoutCancel` plus the existing 30-second timeout.
- Rollout must fail before Catch replacement when a legacy memory checkpoint directory or a ZFS `full` tag exists.
- Catch must not automatically delete, archive, or retag legacy operator data.
- Snapshot observation remains `read`; mutation remains `manage`.
- Do not change the unrelated service snapshot `--snapshots=inherit|on|off` policy.

---

### Task 1: Add the legacy checkpoint rollout gate

**Files:**
- Create: `pkg/catch/vm_checkpoint_retirement.go`
- Create: `pkg/catch/vm_checkpoint_retirement_test.go`
- Modify: `pkg/catch/vm_jailer_upgrade.go:710-744`
- Modify: `pkg/catch/vm_jailer_upgrade_test.go:1289-1437`

**Interfaces:**
- Consumes: `Server.listRecoveryPointsForService`, `Server.serviceRootFromService`, the VM inventory already loaded by `planVMJailerUpgrade`.
- Produces: `inventoryRetiredVMCheckpoints(context.Context, *Server, map[string]*db.Service) ([]retiredVMCheckpoint, error)` and `validateNoRetiredVMCheckpoints([]retiredVMCheckpoint) error`.

- [ ] **Step 1: Write the pure validation tests**

Add table-driven tests covering an empty inventory, a `full` ZFS property, a checkpoint directory, deterministic sorting, and an error containing exact operator actions:

```go
func TestValidateNoRetiredVMCheckpoints(t *testing.T) {
	items := []retiredVMCheckpoint{
		{Service: "zeta", Directory: "/srv/zeta/data/checkpoints/yeet-old"},
		{Service: "alpha", Snapshot: "pool/vms/alpha@yeet-old"},
	}
	err := validateNoRetiredVMCheckpoints(items)
	if err == nil {
		t.Fatal("validateNoRetiredVMCheckpoints returned nil")
	}
	for _, want := range []string{
		"alpha: ZFS snapshot pool/vms/alpha@yeet-old is tagged full",
		"zeta: memory checkpoint directory /srv/zeta/data/checkpoints/yeet-old exists",
		"archive or remove memory/VMM-state files",
		"set com.yeetrun:checkpoint=disk",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}
```

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestValidateNoRetiredVMCheckpoints|TestInventoryRetiredVMCheckpoints' -count=1
```

Expected: FAIL because `retiredVMCheckpoint` and both functions do not exist.

- [ ] **Step 3: Implement read-only inventory and validation**

Use this exact data boundary:

```go
const retiredVMCheckpointMode = "full"

type retiredVMCheckpoint struct {
	Service   string
	Snapshot  string
	Directory string
}

func validateNoRetiredVMCheckpoints(items []retiredVMCheckpoint) error {
	if len(items) == 0 {
		return nil
	}
	slices.SortFunc(items, func(a, b retiredVMCheckpoint) int {
		return cmp.Compare(a.Service+"\x00"+a.Snapshot+a.Directory, b.Service+"\x00"+b.Snapshot+b.Directory)
	})
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if item.Snapshot != "" {
			lines = append(lines, fmt.Sprintf("%s: ZFS snapshot %s is tagged full", item.Service, item.Snapshot))
		}
		if item.Directory != "" {
			lines = append(lines, fmt.Sprintf("%s: memory checkpoint directory %s exists", item.Service, item.Directory))
		}
	}
	return fmt.Errorf("retired Firecracker memory checkpoints block this Catch upgrade:\n%s\narchive or remove memory/VMM-state files, then remove the recovery point or set com.yeetrun:checkpoint=disk on the retained ZFS snapshot", strings.Join(lines, "\n"))
}
```

Inventory only VM services. List existing recovery points and record entries whose raw mode is `full`. Read `<service-data>/checkpoints` with `os.ReadDir`; record each child and treat unreadable directories as an error. `os.ErrNotExist` means no legacy directory. Do not mutate the filesystem or ZFS.

- [ ] **Step 4: Call the gate before staging any Catch upgrade state**

In `planVMJailerUpgrade`, run the inventory immediately after `readVMJailerUpgradeServices` and before resolving runtimes or opening unit directories:

```go
retired, err := inventoryRetiredVMCheckpoints(ctx, server, services)
if err != nil {
	return vmJailerUpgradePlan{}, fmt.Errorf("inventory retired VM checkpoints: %w", err)
}
if err := validateNoRetiredVMCheckpoints(retired); err != nil {
	return vmJailerUpgradePlan{}, err
}
```

Extend `TestPrepareVMJailerUpgradeResolvesEveryVMBeforeStaging` with assertions that neither the runtime resolver nor unit staging runs after this error.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestValidateNoRetiredVMCheckpoints|TestInventoryRetiredVMCheckpoints|TestPrepareVMJailerUpgrade' -count=1
```

Expected: PASS.

Commit:

```bash
but commit disk-only-vm-recovery -m "catch: block upgrades with legacy VM checkpoints"
```

### Task 2: Remove memory-checkpoint CLI syntax

**Files:**
- Modify: `pkg/cli/cli.go:265-285,786-828,1400-1475`
- Modify: `pkg/cli/cli_test.go:540-700,1480-1510`
- Modify: `pkg/yeet/snapshots_cmd.go:15-75`
- Modify: `pkg/yeet/snapshots_cmd_test.go`
- Modify: `cmd/yeet/cli_test.go`

**Interfaces:**
- Consumes: existing `ParseSnapshotsCreate` and `ParseSnapshotsRestore` entry points.
- Produces: `SnapshotsCreateFlags{Comment string}` and `SnapshotsRestoreFlags{Stop, Start, Yes bool; Generation string}` with no VM restore mode.

- [ ] **Step 1: Rewrite parser expectations first**

Add these assertions and remove tests that expect `--full` or `--mode` to parse:

```go
func TestSnapshotsCommandsRejectRetiredMemoryCheckpointFlags(t *testing.T) {
	if _, _, err := ParseSnapshotsCreate([]string{"vm-a", "--full"}); err == nil || !strings.Contains(err.Error(), "unknown flag --full") {
		t.Fatalf("ParseSnapshotsCreate error = %v", err)
	}
	if _, _, err := ParseSnapshotsRestore([]string{"vm-a", "yeet-a", "--mode=full"}); err == nil || !strings.Contains(err.Error(), "unknown flag --mode") {
		t.Fatalf("ParseSnapshotsRestore error = %v", err)
	}
}
```

Update command-info tests to require:

```text
snapshots create <svc> [--comment=TEXT]
snapshots restore <svc> <snapshot> [--stop] [--start] [--yes] [--generation=current|snapshot]
```

- [ ] **Step 2: Run parser and bridge tests to verify failure**

Run:

```bash
mise exec -- go test ./pkg/cli ./pkg/yeet ./cmd/yeet -run 'Snapshots|Snapshot' -count=1
```

Expected: FAIL while the old fields and command metadata remain.

- [ ] **Step 3: Remove only the memory-checkpoint syntax**

The remaining parsed structs must be:

```go
type snapshotsCreateFlagsParsed struct {
	Comment string `flag:"comment" help:"Human note stored with the recovery point"`
}

type snapshotsRestoreFlagsParsed struct {
	Stop       bool   `flag:"stop" help:"Stop the service before restoring"`
	Start      bool   `flag:"start" help:"Start the service after restoring"`
	Yes        bool   `flag:"yes" short:"y" help:"Skip the restore confirmation prompt"`
	Generation string `flag:"generation" help:"Service generation source: current, snapshot"`
}
```

Remove `Full` from `SnapshotsCreateFlags`, remove `Mode` from `SnapshotsRestoreFlags`, and delete snapshot-restore mode normalization/missing-value helpers that have no other callers. Keep service configuration snapshot-mode parsing (`run --snapshots` and `service set --snapshots`) unchanged.

- [ ] **Step 4: Run all affected routing tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./pkg/yeet ./cmd/yeet -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
but commit disk-only-vm-recovery -m "cli: remove VM memory checkpoint flags"
```

### Task 3: Collapse VM snapshot creation to pause, ZFS snapshot, resume

**Files:**
- Modify: `pkg/catch/vm_snapshot.go:20-460`
- Modify: `pkg/catch/vm_snapshot_test.go`
- Modify: `pkg/catch/recovery_points.go:25-410`
- Modify: `pkg/catch/recovery_points_render.go:1-95`
- Modify: `pkg/catch/recovery_points_test.go`
- Modify: `pkg/catch/recovery_clone.go`
- Modify: `pkg/catch/recovery_vm.go:1-175`
- Modify: `pkg/catch/recovery_vm_test.go`

**Interfaces:**
- Consumes: Firecracker pause/resume API, `createServiceSnapshot`, snapshot retention.
- Produces: a disk-only `vmFirecrackerPauser`, a `vmSnapshotResult{Name string}`, and disk-only VM restore.

- [ ] **Step 1: Add disk-only behavior tests**

Keep and tighten tests for the sequence `Pause -> zfs snapshot -> Resume`, cancellation-resistant resume, stopped-VM snapshot, ZFS failure, and resume failure. Add a guard test whose fake exposes only pause/resume:

```go
type recordingVMFirecrackerPauser struct {
	calls []string
}

func (r *recordingVMFirecrackerPauser) Pause(context.Context, string) error {
	r.calls = append(r.calls, "pause")
	return nil
}

func (r *recordingVMFirecrackerPauser) Resume(context.Context, string) error {
	r.calls = append(r.calls, "resume")
	return nil
}
```

Assert the created ZFS command contains `com.yeetrun:checkpoint=disk` and that no checkpoint directory is created under the service data root.

- [ ] **Step 2: Run focused tests and verify they fail against the old interface**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestCreateVMSnapshot|TestExecuteVMSnapshot|TestRecoveryPoint|TestSnapshotsRestoreVM' -count=1
```

Expected: FAIL because the production interface still requires `CreateFullSnapshot` and enriches full checkpoints.

- [ ] **Step 3: Reduce the snapshot implementation to the disk path**

The surviving types and execution path must be:

```go
type vmFirecrackerPauser interface {
	Pause(context.Context, string) error
	Resume(context.Context, string) error
}

type vmSnapshotResult struct {
	Name string
}

func (s *Server) createPausedVMSnapshot(ctx context.Context, service *db.Service, dataset string, flags cli.SnapshotsCreateFlags) (vmSnapshotResult, error) {
	name, err := createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service: service.Name, Dataset: dataset, Event: snapshotEventVMManual,
		Now: time.Now(), Comment: flags.Comment, Checkpoint: recoveryModeDisk,
	})
	if err != nil {
		return vmSnapshotResult{}, err
	}
	return vmSnapshotResult{Name: name}, nil
}
```

Remove `FullCompatibility`, `createPausedFullVMSnapshot`, `flushPausedVMDiskForSnapshot`, `createTemporaryFullVMCheckpoint`, failure cleanup for memory workspaces, and checkpoint-directory pruning. Do not replace them with a Firecracker snapshot call.

- [ ] **Step 4: Remove memory paths from recovery-point data and make VM restore disk-only**

The user-visible record must end at `Retention`:

```go
type recoveryPoint struct {
	Service     string    `json:"service"`
	ServiceType string    `json:"serviceType"`
	StorageKind string    `json:"storageKind"`
	Dataset     string    `json:"dataset"`
	Name        string    `json:"name"`
	ShortName   string    `json:"shortName"`
	Created     time.Time `json:"created"`
	CreatedBy   string    `json:"createdBy"`
	Event       string    `json:"event"`
	Generation  *int      `json:"generation,omitempty"`
	Comment     string    `json:"comment,omitempty"`
	Mode        string    `json:"mode"`
	Protected   bool      `json:"protected"`
	Actions     []string  `json:"actions"`
	Retention   string    `json:"retention"`
}
```

Delete full enrichment and metadata readers. `restoreVMRecoveryPoint` must validate the VM target and call `restoreDiskVMRecoveryPoint` directly. A raw ZFS property value of `full` must return an explicit retired-format error rather than being treated as restorable.

- [ ] **Step 5: Run Catch tests and commit**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestCreateVMSnapshot|TestExecuteVMSnapshot|TestRecoveryPoint|TestSnapshotsRestoreVM|TestCloneVMRecoveryPoint' -count=1
```

Expected: PASS.

Commit:

```bash
but commit disk-only-vm-recovery -m "catch: keep VM recovery points disk-only"
```

### Task 4: Delete Firecracker snapshot restore and jail checkpoint plumbing

**Files:**
- Delete: `pkg/catch/firecracker_snapshot_restore.go`
- Delete: `pkg/catch/firecracker_snapshot_restore_test.go`
- Delete: `pkg/catch/vm_checkpoint_metadata.go`
- Delete: `pkg/catch/vm_checkpoint_metadata_test.go`
- Delete: `pkg/catch/vm_checkpoint_workspace.go`
- Delete: `pkg/catch/vm_checkpoint_workspace_darwin.go`
- Delete: `pkg/catch/vm_checkpoint_workspace_linux.go`
- Modify: `pkg/catch/vm_console_proxy.go:20-170,240-420`
- Modify: `pkg/catch/vm_console_test.go`
- Modify: `pkg/catch/vm_jailer.go:112-180,248-295,580-710`
- Modify: `pkg/catch/vm_jailer_test.go`
- Modify: `pkg/catch/vm_jailer_transition.go:264-330,475-503`
- Modify: `pkg/catch/vm_jailer_transition_test.go`
- Modify: `pkg/catch/vm_systemd.go:35-110`
- Modify: `pkg/catch/vm_systemd_test.go`
- Modify: `cmd/catch/catch.go:345-385`
- Modify: `cmd/catch/catch_test.go:470-620`

**Interfaces:**
- Consumes: disk-only snapshot code from Task 3.
- Produces: one normal jailer launch mode using `--config-file`; no restore request/result files or restore-specific systemd status.

- [ ] **Step 1: Rewrite launch tests around one mode**

Change process-constructor tests to this signature:

```go
type vmConsoleProcessConstructor func(context.Context, VMConsoleProxyConfig) (*exec.Cmd, func(), error)
```

Require every `vmJailerCommandArgs` result to contain `--config-file` and reject any `restoreMode` argument in compile-time call sites. Remove tests for restore request/result JSON and `VMRestoreLoadFailedExitCode`.

- [ ] **Step 2: Run focused launch tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestRunVMConsoleProxy|TestVMJailerCommandArgs|TestRenderVMSystemdUnit|TestHandleSpecialCommandVMRun|TestVMJailerTransition' -count=1
```

Expected: FAIL while the restore-mode signatures and checkpoint bind remain.

- [ ] **Step 3: Remove restore state from the console proxy and systemd**

The launch core must be:

```go
cmd, cleanupProcess, err := constructProcess(ctx, cfg)
if err != nil {
	return err
}
defer cleanupProcess()
console, err := pty.Start(cmd)
```

Delete `vmFullRestoreRequest`, `vmFullRestoreResult`, snapshot loader types, request/result path helpers, load/wait logic, `ErrVMRestoreLoadFailed`, and exit code 76. Remove `RestartPreventExitStatus` and `ensureVMSystemdRestorePrevent`; preserve `RestartForceExitStatus=75` for guest reboot.

- [ ] **Step 4: Remove checkpoint storage from the jail**

Delete the checkpoint bind from `addVMJailConfigResources`, remove delegation helpers rooted at `data/checkpoints`, and remove checkpoint-directory validation from `vmJailerTransitionPlanningTrustedPaths`. The jail still binds the Firecracker config, kernel, initrd when present, drives, API socket, and vsock resources.

The launcher signature must become:

```go
func prepareVMConsoleProcess(ctx context.Context, cfg VMConsoleProxyConfig) (*exec.Cmd, func(), error)

func vmJailerCommandArgs(cfg VMConsoleProxyConfig, identity vmRuntimeIdentity) []string
```

and must always append:

```go
"--", "--api-sock", cfg.APISocket, "--config-file", cfg.ConfigFile
```

- [ ] **Step 5: Prove the snapshot API is gone and commit**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -count=1
rg -n 'CreateFullSnapshot|CreateSnapshot|LoadSnapshot|mem_file_path|snapshot_path|firecracker-restore|VMRestoreLoadFailed|checkpoints' pkg/catch cmd/catch
```

Expected: tests PASS; `rg` returns no Firecracker memory snapshot/restore implementation. Any remaining `checkpoints` match must be only the rollout inventory and its tests.

Commit:

```bash
but commit disk-only-vm-recovery -m "catch: remove Firecracker state restore plumbing"
```

### Task 5: Update operator documentation and complete validation

**Files:**
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `pkg/catch/tty_service.go:55-70`
- Modify: `pkg/catch/tty_service_test.go:250-280`

**Interfaces:**
- Consumes: final CLI and recovery semantics from Tasks 2-4.
- Produces: evergreen disk-only docs and a public compatibility warning for legacy data.

- [ ] **Step 1: Add documentation assertions before editing prose**

Update CLI/help tests to require disk-only wording and change the VM generation rollback message to:

```text
VM services do not support generation rollback; use yeet snapshots restore for VM disk recovery
```

Add a docs scan command to the task checklist:

```bash
rg -n -- '--full|--mode=full|Firecracker checkpoint|memory checkpoint|full VM state' README.md website/docs pkg/cli
```

- [ ] **Step 2: Rewrite docs as current behavior**

Document that a running VM is paused around one atomic ZFS disk snapshot, that the result is crash-consistent, that raw disks cannot be snapshotted, and that restore replaces disk state only. Put legacy cleanup instructions in a dedicated compatibility note, not in the evergreen command examples.

- [ ] **Step 3: Run the complete local gate**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch ./cmd/catch -count=1
mise exec -- go test ./... -count=1
git diff --check
mise exec -- pre-commit run --all-files
```

Expected: all tests and diff checks PASS. If the known repository-wide private-info baseline still fails, verify this plan's changed files are absent from its findings and report the baseline separately; do not weaken the scanner.

- [ ] **Step 4: Run generic live validation on an approved KVM host**

Require the operator to set the target through the normal private environment, then run:

```bash
test -n "${YEET_VALIDATION_HOST:?set YEET_VALIDATION_HOST}"
test -n "${YEET_VALIDATION_VM:?set YEET_VALIDATION_VM to a disposable ZFS-backed VM}"
yeet --host "$YEET_VALIDATION_HOST" snapshots create "$YEET_VALIDATION_VM" --comment "disk-only validation"
yeet --host "$YEET_VALIDATION_HOST" snapshots list "$YEET_VALIDATION_VM" --format=json
yeet --host "$YEET_VALIDATION_HOST" snapshots restore "$YEET_VALIDATION_VM" "$(yeet --host "$YEET_VALIDATION_HOST" snapshots list "$YEET_VALIDATION_VM" --format=json | jq -r '.[0].shortName')" --stop --start --yes
```

Verify on the host that no `memory.bin`, `firecracker-state.bin`, or Firecracker restore request exists beneath the service root; verify the VM resumes after create and boots after disk restore.

- [ ] **Step 5: Commit the root docs and website pointer through their repository workflows**

After explicit authorization to push the website submodule, commit and push it first; the website repository requires that commit to be reachable before the root gitlink is committed. Then commit the root documentation, tests, and gitlink on the GitButler branch. Do not push the root repository or release.

```bash
git -C website diff --check
git -C website add docs/cli/yeet-cli.mdx docs/payloads/vms.mdx
git -C website commit -m "docs: describe disk-only VM recovery"
git -C website push origin HEAD
but commit disk-only-vm-recovery -m "docs: describe disk-only VM recovery"
```
