# Complete Snapshot Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a coherent snapshot and generation recovery model for yeet: service definitions roll back through `yeet service`, persistent state restores and clones through `yeet snapshots`, and VM full-state checkpoints are restored only when Firecracker compatibility can be proven.

**Architecture:** Keep generation commands separate from state recovery commands. Add focused catch-side recovery helpers for ZFS operations, VM disk clone/restore, service-root clone/restore, and Firecracker full-state restore checks. Preserve existing snapshot list/create/protect/remove behavior while cleaning up VM generation leakage and extending the lifecycle with clone and restore.

**Tech Stack:** Go, yargs metadata in `pkg/cli`, yeet client bridge in `cmd/yeet` and `pkg/yeet`, catch TTY handlers in `pkg/catch`, ZFS CLI, systemd, Firecracker snapshot APIs, website MDX docs in the `website/` submodule, GitButler `but`.

---

## File Structure

Modify these existing files:

- `pkg/cli/cli.go`: command metadata, flag schemas, parsers for `service rollback`, `service generations`, `snapshots clone`, and `snapshots restore`; remove top-level `rollback`.
- `pkg/cli/cli_test.go`: parser and registry tests.
- `cmd/yeet/cli.go`: snapshots group subcommand map.
- `cmd/yeet/cli_test.go`: help and routing tests.
- `cmd/yeet/cli_bridge.go`: service-argument bridge skip list for unscoped snapshot lifecycle commands.
- `cmd/yeet/cli_bridge_test.go`: bridge tests for clone/restore.
- `pkg/yeet/snapshots_cmd.go`: client-side snapshot lifecycle validation and forwarding.
- `pkg/yeet/snapshots_cmd_test.go`: client-side validation tests.
- `pkg/catch/tty_exec.go`: remove top-level rollback route.
- `pkg/catch/tty_service_set.go`: add `service rollback` and `service generations` dispatch.
- `pkg/catch/tty_service.go`: reuse existing rollback implementation under `service rollback`; add generation listing.
- `pkg/catch/tty_service_test.go`: service generation command tests.
- `pkg/catch/tty_ops.go`: add snapshot clone/restore parsing and command dispatch.
- `pkg/catch/tty_ops_test.go`: snapshot command rendering and prompt behavior tests.
- `pkg/catch/recovery_points.go`: add clone/restore actions, optional generation, VM generation cleanup.
- `pkg/catch/recovery_points_render.go`: hide VM generation in human output; expose nullable generation in JSON.
- `pkg/catch/recovery_points_test.go`: snapshot model tests.
- `pkg/catch/service_snapshots.go`: optional generation suffix support for new VM snapshot names while keeping old selectors.
- `pkg/catch/service_snapshots_test.go`: snapshot name and listed snapshot parsing tests.
- `pkg/catch/vm_snapshot.go`: write compatibility metadata for full VM checkpoints.
- `pkg/catch/vm_snapshot_test.go`: full metadata tests.
- `pkg/catch/vm_runner.go`: expose any missing stopped/running helpers through focused functions instead of duplicating systemd logic.
- `pkg/db/service.go` or the current DB service model file if named differently: add clone helper only if existing copy logic is not sufficient.
- `website/docs/cli/yeet-cli.mdx`: user-facing CLI docs.
- `website/docs/concepts/zfs.mdx`: state recovery docs.
- `website/docs/payloads/vms.mdx`: VM snapshot restore docs.
- `website/docs/operations/workflows.mdx`: safe clone-first recovery workflow.

Create these new focused files:

- `pkg/catch/recovery_zfs.go`: ZFS clone, rollback, destroy, mountpoint, and exact target validation helpers.
- `pkg/catch/recovery_zfs_test.go`: argv-level ZFS tests.
- `pkg/catch/recovery_clone.go`: catch-side `snapshots clone` orchestration shared by VM zvol and service-root paths.
- `pkg/catch/recovery_clone_test.go`: clone validation and happy-path tests.
- `pkg/catch/recovery_restore.go`: catch-side `snapshots restore` orchestration shared by VM zvol and service-root paths.
- `pkg/catch/recovery_restore_test.go`: restore safety, pre-restore snapshot, prompt, and start/stop tests.
- `pkg/catch/recovery_vm.go`: VM zvol clone, VM disk restore, and VM full-state restore preparation.
- `pkg/catch/recovery_vm_test.go`: VM clone/restore tests.
- `pkg/catch/recovery_service_root.go`: ZFS service-root clone and restore.
- `pkg/catch/recovery_service_root_test.go`: service-root clone/restore tests.
- `pkg/catch/firecracker_snapshot_restore.go`: Firecracker `LoadSnapshot` client method and full-state restore request construction.
- `pkg/catch/firecracker_snapshot_restore_test.go`: Firecracker restore request tests.

Read local subsystem instructions before editing:

- `pkg/cli/AGENTS.md`
- `cmd/yeet/AGENTS.md`
- `pkg/yeet/AGENTS.md`
- `pkg/catch/AGENTS.md`
- `website/AGENTS.md` before website docs changes

## Task 0: Preflight And Branch Hygiene

**Files:**
- Read: `AGENTS.md`
- Read: `AGENTS.local.md`
- Read: `docs/agent/codebase-map.md`
- Read: subsystem `AGENTS.md` files listed above

- [ ] **Step 1: Confirm workspace and target state**

Run:

```bash
but status -fv
but pull --check
```

Expected:

- Applied work is on `codex/snapshot-recovery-complete-design` or a new dedicated GitButler branch stacked on it.
- No unassigned changes except files created by this plan execution.
- `but pull --check` reports no conflicts.

- [ ] **Step 2: If the implementation branch does not exist, create it**

Run only if `but status -fv` does not show an implementation branch:

```bash
but branch new codex/complete-snapshot-recovery
```

Expected: GitButler creates and applies `codex/complete-snapshot-recovery`.

- [ ] **Step 3: Load local subsystem instructions**

Run:

```bash
sed -n '1,220p' pkg/cli/AGENTS.md
sed -n '1,220p' cmd/yeet/AGENTS.md
sed -n '1,220p' pkg/yeet/AGENTS.md
sed -n '1,220p' pkg/catch/AGENTS.md
```

Expected: no hidden subsystem instruction conflicts with this plan. If a conflict exists, follow the more local instruction and record the conflict in the final summary.

## Task 1: Move Generation Rollback Under `yeet service`

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_service.go`
- Modify: `pkg/catch/tty_service_test.go`
- Modify: `website/docs/cli/yeet-cli.mdx` in Task 8, not here

- [ ] **Step 1: Write CLI registry and parser tests**

Add tests in `pkg/cli/cli_test.go` that assert:

```go
func TestRegistryMovesRollbackUnderService(t *testing.T) {
	reg := RemoteRegistry()
	if _, ok := reg.SubCommands["rollback"]; ok {
		t.Fatal("top-level rollback command should be removed")
	}
	if _, ok := reg.Groups["service"].Commands["rollback"]; !ok {
		t.Fatal("service rollback command missing")
	}
	if _, ok := reg.Groups["service"].Commands["generations"]; !ok {
		t.Fatal("service generations command missing")
	}
	if _, ok := RemoteGroupFlagSpecs()["service"]["rollback"]; !ok {
		t.Fatal("service rollback flag spec missing")
	}
	if _, ok := RemoteGroupFlagSpecs()["service"]["generations"]; !ok {
		t.Fatal("service generations flag spec missing")
	}
}

func TestParseServiceGenerationCommands(t *testing.T) {
	rollback, err := ParseServiceRollback([]string{"plex"})
	if err != nil {
		t.Fatalf("ParseServiceRollback: %v", err)
	}
	if len(rollback) != 1 || rollback[0] != "plex" {
		t.Fatalf("rollback args = %#v, want plex", rollback)
	}

	flags, args, err := ParseServiceGenerations([]string{"plex", "--format=json"})
	if err != nil {
		t.Fatalf("ParseServiceGenerations: %v", err)
	}
	if flags.Format != "json" || len(args) != 1 || args[0] != "plex" {
		t.Fatalf("generations parse = flags %#v args %#v", flags, args)
	}
}

func TestParseServiceGenerationCommandsRejectBadInput(t *testing.T) {
	if _, err := ParseServiceRollback([]string{}); err == nil || !strings.Contains(err.Error(), "service rollback requires a service") {
		t.Fatalf("ParseServiceRollback error = %v, want service arity error", err)
	}
	if _, err := ParseServiceRollback([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "service rollback requires exactly one service") {
		t.Fatalf("ParseServiceRollback error = %v, want exact arity error", err)
	}
	if _, _, err := ParseServiceGenerations([]string{"plex", "--format=yaml"}); err == nil || !strings.Contains(err.Error(), "--format must be table, json, or json-pretty") {
		t.Fatalf("ParseServiceGenerations error = %v, want format error", err)
	}
}
```

Run:

```bash
go test ./pkg/cli -run 'TestRegistryMovesRollbackUnderService|TestParseServiceGenerationCommands' -count=1
```

Expected: FAIL because parsers and registry entries do not exist yet.

- [ ] **Step 2: Implement CLI metadata and parsers**

In `pkg/cli/cli.go`:

- Remove `"rollback"` from `remoteCommandInfos`.
- Remove `"rollback"` from `remoteFlagSpecs`.
- Add these public/parser types near the snapshot lifecycle flag types:

```go
type ServiceGenerationsFlags struct {
	Format string
}

type serviceGenerationsFlagsParsed struct {
	Format string `flag:"format"`
}
```

- Add service group commands:

```go
"rollback": {
	Name:        "rollback",
	Description: "Roll back a service to the previous deployed generation",
	Usage:       "service rollback <svc>",
	Examples:    []string{"yeet service rollback plex"},
},
"generations": {
	Name:        "generations",
	Description: "Show deployed generations for a service",
	Usage:       "service generations <svc> [--format=table|json|json-pretty]",
	Examples:    []string{"yeet service generations plex", "yeet service generations plex --format=json"},
	FlagsSchema: serviceGenerationsFlagsParsed{},
},
```

- Add flag specs under `remoteGroupFlagSpecs["service"]`:

```go
"rollback":    {},
"generations": flagSpecsFromStruct(serviceGenerationsFlagsParsed{}),
```

- Add parsers:

```go
func ParseServiceRollback(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("service rollback requires a service")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("service rollback requires exactly one service")
	}
	if strings.TrimSpace(args[0]) == "" {
		return nil, fmt.Errorf("service rollback requires a service")
	}
	return args, nil
}

func ParseServiceGenerations(args []string) (ServiceGenerationsFlags, []string, error) {
	parsed, err := parseFlags[serviceGenerationsFlagsParsed](args)
	if err != nil {
		return ServiceGenerationsFlags{}, nil, err
	}
	format := strings.TrimSpace(parsed.Flags.Format)
	if format == "" {
		format = "table"
	}
	if err := validateOutputFormat(format); err != nil {
		return ServiceGenerationsFlags{}, nil, err
	}
	if len(parsed.Args) == 0 {
		return ServiceGenerationsFlags{}, nil, fmt.Errorf("service generations requires a service")
	}
	if len(parsed.Args) != 1 {
		return ServiceGenerationsFlags{}, nil, fmt.Errorf("service generations requires exactly one service")
	}
	return ServiceGenerationsFlags{Format: format}, parsed.Args, nil
}
```

Run:

```bash
go test ./pkg/cli -run 'TestRegistryMovesRollbackUnderService|TestParseServiceGenerationCommands' -count=1
```

Expected: PASS.

- [ ] **Step 3: Write CLI help and catch routing tests**

Add a `cmd/yeet/cli_test.go` test that asserts:

```go
func TestServiceHelpShowsGenerationCommands(t *testing.T) {
	stdout, _, err := runCLIForTest([]string{"yeet", "service", "--help"})
	if err != nil {
		t.Fatalf("service help: %v", err)
	}
	for _, want := range []string{"service rollback <svc>", "service generations <svc>"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("service help missing %q in %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "yeet rollback") {
		t.Fatalf("service help should not advertise top-level rollback: %q", stdout)
	}
}
```

Add `pkg/catch/tty_service_test.go` tests that assert:

```go
func TestServiceCommandDispatchesRollbackAndGenerations(t *testing.T) {
	// Seed a non-VM service with generation 2 and latest generation 3.
	// `service rollback svc-a` should call the existing rollback path.
	// `service generations svc-a --format=json` should print current/latest generation and supported=true.
}

func TestServiceRollbackRejectsVM(t *testing.T) {
	// Seed a VM service.
	// `service rollback vm-a` should fail with "VM services do not support generation rollback".
}
```

Use the existing test server helpers in `pkg/catch/tty_service_test.go`; do not create a second DB harness.

Run:

```bash
go test ./cmd/yeet -run TestServiceHelpShowsGenerationCommands -count=1
go test ./pkg/catch -run 'TestServiceCommandDispatchesRollbackAndGenerations|TestServiceRollbackRejectsVM' -count=1
```

Expected: FAIL because catch routing still uses top-level rollback and `serviceCmdFunc` only knows `set`.

- [ ] **Step 4: Implement catch service routing**

In `pkg/catch/tty_exec.go`, remove the top-level command entry that calls `rollbackCmdFunc`.

In `pkg/catch/tty_service_set.go`, extend `serviceCmdFunc`:

```go
switch args[0] {
case "set":
	return e.serviceSetCmdFunc(args[1:])
case "rollback":
	rest, err := cli.ParseServiceRollback(args[1:])
	if err != nil {
		return err
	}
	return e.rollbackCmdFunc(rest[0])
case "generations":
	flags, rest, err := cli.ParseServiceGenerations(args[1:])
	if err != nil {
		return err
	}
	return e.serviceGenerationsCmdFunc(rest[0], flags)
default:
	return fmt.Errorf("unknown service command %q", args[0])
}
```

In `pkg/catch/tty_service.go`, change `rollbackCmdFunc` to accept a service name:

```go
func (e *ttyExecer) rollbackCmdFunc(name string) error {
	service, err := e.server.db.GetService(name)
	if err != nil {
		return err
	}
	if service.IsVM() {
		return fmt.Errorf("VM services do not support generation rollback; use yeet snapshots restore for VM disk or checkpoint recovery")
	}
	return e.rollbackGeneration(service)
}
```

Add:

```go
type serviceGenerationView struct {
	Service           string `json:"service"`
	Type              string `json:"type"`
	CurrentGeneration int    `json:"currentGeneration"`
	LatestGeneration  int    `json:"latestGeneration"`
	RollbackSupported bool   `json:"rollbackSupported"`
}

func (e *ttyExecer) serviceGenerationsCmdFunc(name string, flags cli.ServiceGenerationsFlags) error {
	service, err := e.server.db.GetService(name)
	if err != nil {
		return err
	}
	view := serviceGenerationView{
		Service:           service.Name,
		Type:              service.Type,
		CurrentGeneration: service.Generation,
		LatestGeneration:  service.LatestGeneration,
		RollbackSupported: !service.IsVM(),
	}
	return renderServiceGenerations(e.stdout, view, flags.Format)
}
```

Implement `renderServiceGenerations` in the same file using the existing table/JSON render style from `recovery_points_render.go`.

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/catch -run 'Rollback|Generation|ServiceHelpShowsGenerationCommands' -count=1
```

Expected: PASS.

- [ ] **Step 5: Checkpoint commit**

Run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "cli: move rollback under service" --changes <ids-for-task-1-files>
```

Expected: one local GitButler checkpoint commit containing only Task 1 files.

## Task 2: Add `snapshots clone` And `snapshots restore` CLI Routing

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `cmd/yeet/cli_bridge.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `pkg/yeet/snapshots_cmd.go`
- Modify: `pkg/yeet/snapshots_cmd_test.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_ops_test.go`

- [ ] **Step 1: Write parser tests for clone and restore**

Add to `pkg/cli/cli_test.go`:

```go
func TestParseSnapshotsCloneAndRestore(t *testing.T) {
	cloneFlags, cloneArgs, err := ParseSnapshotsClone([]string{"vm-a", "yeet-abc", "vm-copy", "--start"})
	if err != nil {
		t.Fatalf("ParseSnapshotsClone: %v", err)
	}
	if !cloneFlags.Start || !reflect.DeepEqual(cloneArgs, []string{"vm-a", "yeet-abc", "vm-copy"}) {
		t.Fatalf("clone parse = flags %#v args %#v", cloneFlags, cloneArgs)
	}

	restoreFlags, restoreArgs, err := ParseSnapshotsRestore([]string{"vm-a", "yeet-abc", "--stop", "--start", "--yes", "--mode=full", "--generation=snapshot"})
	if err != nil {
		t.Fatalf("ParseSnapshotsRestore: %v", err)
	}
	if !restoreFlags.Stop || !restoreFlags.Start || !restoreFlags.Yes || restoreFlags.Mode != "full" || restoreFlags.Generation != "snapshot" {
		t.Fatalf("restore flags = %#v", restoreFlags)
	}
	if !reflect.DeepEqual(restoreArgs, []string{"vm-a", "yeet-abc"}) {
		t.Fatalf("restore args = %#v", restoreArgs)
	}
}

func TestParseSnapshotsCloneAndRestoreRejectBadInput(t *testing.T) {
	if _, _, err := ParseSnapshotsClone([]string{"svc", "snap"}); err == nil || !strings.Contains(err.Error(), "snapshots clone requires service, snapshot, and new service") {
		t.Fatalf("ParseSnapshotsClone error = %v, want arity error", err)
	}
	if _, _, err := ParseSnapshotsRestore([]string{"svc"}); err == nil || !strings.Contains(err.Error(), "snapshots restore requires service and snapshot") {
		t.Fatalf("ParseSnapshotsRestore error = %v, want arity error", err)
	}
	if _, _, err := ParseSnapshotsRestore([]string{"svc", "snap", "--mode=memory"}); err == nil || !strings.Contains(err.Error(), "--mode must be disk or full") {
		t.Fatalf("ParseSnapshotsRestore error = %v, want mode error", err)
	}
	if _, _, err := ParseSnapshotsRestore([]string{"svc", "snap", "--generation=latest"}); err == nil || !strings.Contains(err.Error(), "--generation must be current or snapshot") {
		t.Fatalf("ParseSnapshotsRestore error = %v, want generation error", err)
	}
}
```

Run:

```bash
go test ./pkg/cli -run 'TestParseSnapshotsCloneAndRestore' -count=1
```

Expected: FAIL because parser types and functions do not exist.

- [ ] **Step 2: Implement parser types and validation**

In `pkg/cli/cli.go`, add:

```go
type SnapshotsCloneFlags struct {
	Start bool
}

type SnapshotsRestoreFlags struct {
	Stop       bool
	Start      bool
	Yes        bool
	Mode       string
	Generation string
}

type snapshotsCloneFlagsParsed struct {
	Start bool `flag:"start"`
}

type snapshotsRestoreFlagsParsed struct {
	Stop       bool   `flag:"stop"`
	Start      bool   `flag:"start"`
	Yes        bool   `flag:"yes"`
	Mode       string `flag:"mode"`
	Generation string `flag:"generation"`
}
```

Add `remoteGroupInfos["snapshots"].Commands` entries:

```go
"clone": {
	Name:        "clone",
	Description: "Clone a recovery point into a new stopped service",
	Usage:       "snapshots clone <svc> <snapshot> <new-svc> [--start]",
	Examples: []string{
		"yeet snapshots clone plex yeet-20260613T203100Z-run-g4 plex-restore",
		"yeet snapshots clone devbox yeet-20260613T203100Z-vm-manual devbox-copy --start",
	},
	FlagsSchema: snapshotsCloneFlagsParsed{},
},
"restore": {
	Name:        "restore",
	Description: "Restore a recovery point in place",
	Usage:       "snapshots restore <svc> <snapshot> [--stop] [--start] [--yes] [--mode=disk|full] [--generation=current|snapshot]",
	Examples: []string{
		"yeet snapshots restore plex yeet-20260613T203100Z-run-g4 --stop --yes",
		"yeet snapshots restore devbox yeet-20260613T203100Z-vm-manual --mode=full --stop",
	},
	FlagsSchema: snapshotsRestoreFlagsParsed{},
},
```

Add flag specs:

```go
"clone":   flagSpecsFromStruct(snapshotsCloneFlagsParsed{}),
"restore": flagSpecsFromStruct(snapshotsRestoreFlagsParsed{}),
```

Add parsers:

```go
func ParseSnapshotsClone(args []string) (SnapshotsCloneFlags, []string, error) {
	parsed, err := parseFlags[snapshotsCloneFlagsParsed](args)
	if err != nil {
		return SnapshotsCloneFlags{}, nil, err
	}
	if len(parsed.Args) != 3 {
		return SnapshotsCloneFlags{}, nil, fmt.Errorf("snapshots clone requires service, snapshot, and new service")
	}
	return SnapshotsCloneFlags{Start: parsed.Flags.Start}, parsed.Args, nil
}

func ParseSnapshotsRestore(args []string) (SnapshotsRestoreFlags, []string, error) {
	parsed, err := parseFlags[snapshotsRestoreFlagsParsed](args)
	if err != nil {
		return SnapshotsRestoreFlags{}, nil, err
	}
	if len(parsed.Args) != 2 {
		return SnapshotsRestoreFlags{}, nil, fmt.Errorf("snapshots restore requires service and snapshot")
	}
	mode := strings.TrimSpace(parsed.Flags.Mode)
	if mode == "" {
		mode = "disk"
	}
	if mode != "disk" && mode != "full" {
		return SnapshotsRestoreFlags{}, nil, fmt.Errorf("--mode must be disk or full")
	}
	generation := strings.TrimSpace(parsed.Flags.Generation)
	if generation == "" {
		generation = "current"
	}
	if generation != "current" && generation != "snapshot" {
		return SnapshotsRestoreFlags{}, nil, fmt.Errorf("--generation must be current or snapshot")
	}
	return SnapshotsRestoreFlags{
		Stop:       parsed.Flags.Stop,
		Start:      parsed.Flags.Start,
		Yes:        parsed.Flags.Yes,
		Mode:       mode,
		Generation: generation,
	}, parsed.Args, nil
}
```

Run:

```bash
go test ./pkg/cli -run 'TestParseSnapshotsCloneAndRestore' -count=1
```

Expected: PASS.

- [ ] **Step 3: Wire local CLI and service bridge**

In `cmd/yeet/cli.go`, add `clone` and `restore` to the snapshots group handler map:

```go
"clone":     handleSnapshotsGroup,
"restore":   handleSnapshotsGroup,
```

In `cmd/yeet/cli_bridge.go`, add `clone` and `restore` to `serviceBridgeSkippedGroupCommands["snapshots"]` so their first positional argument remains the target service inside the command payload.

Add `cmd/yeet/cli_bridge_test.go` cases:

```go
{
	name:     "snapshots clone remains unscoped",
	args:     []string{"snapshots@catch-a", "clone", "svc-a", "yeet-abc", "svc-copy"},
	wantHost: "catch-a",
	wantArgs: []string{"snapshots", "clone", "svc-a", "yeet-abc", "svc-copy"},
},
{
	name:     "snapshots restore remains unscoped",
	args:     []string{"snapshots@catch-a", "restore", "svc-a", "yeet-abc", "--stop", "--yes"},
	wantHost: "catch-a",
	wantArgs: []string{"snapshots", "restore", "svc-a", "yeet-abc", "--stop", "--yes"},
},
```

Run:

```bash
go test ./cmd/yeet -run 'TestBridge|TestSnapshots' -count=1
```

Expected: PASS after code is wired.

- [ ] **Step 4: Wire yeet client validation and catch command dispatch**

In `pkg/yeet/snapshots_cmd.go`, extend `validateSnapshotLifecycleCommand` with:

```go
case "clone":
	_, _, err := cli.ParseSnapshotsClone(args[1:])
	return err
case "restore":
	_, _, err := cli.ParseSnapshotsRestore(args[1:])
	return err
```

In `pkg/catch/tty_ops.go`, extend the snapshots switch with:

```go
case "clone":
	flags, rest, err := cli.ParseSnapshotsClone(args[1:])
	if err != nil {
		return err
	}
	return e.snapshotsCloneCmdFunc(rest[0], rest[1], rest[2], flags)
case "restore":
	flags, rest, err := cli.ParseSnapshotsRestore(args[1:])
	if err != nil {
		return err
	}
	return e.snapshotsRestoreCmdFunc(rest[0], rest[1], flags)
```

For this task, add temporary functions in `pkg/catch/recovery_clone.go` and `pkg/catch/recovery_restore.go` that return deterministic feature-gate errors:

```go
func (e *ttyExecer) snapshotsCloneCmdFunc(serviceName, selector, newServiceName string, flags cli.SnapshotsCloneFlags) error {
	return fmt.Errorf("snapshots clone implementation is not linked")
}

func (e *ttyExecer) snapshotsRestoreCmdFunc(serviceName, selector string, flags cli.SnapshotsRestoreFlags) error {
	return fmt.Errorf("snapshots restore implementation is not linked")
}
```

These temporary errors must be removed by Tasks 5 and 6 before final verification.

Run:

```bash
go test ./pkg/yeet ./pkg/catch -run 'Snapshots.*(Clone|Restore|Lifecycle)' -count=1
```

Expected: PASS for parser/routing tests. Task 5 replaces the temporary VM clone/restore errors, and Task 6 replaces the temporary service-root clone/restore errors before final verification.

- [ ] **Step 5: Checkpoint commit**

Run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "cli: route snapshot clone and restore" --changes <ids-for-task-2-files>
```

Expected: one checkpoint commit containing parser, help, bridge, and dispatch changes.

## Task 3: Clean Up Recovery Point Generation Semantics

**Files:**
- Modify: `pkg/catch/recovery_points.go`
- Modify: `pkg/catch/recovery_points_render.go`
- Modify: `pkg/catch/recovery_points_test.go`
- Modify: `pkg/catch/service_snapshots.go`
- Modify: `pkg/catch/service_snapshots_test.go`
- Modify: `pkg/catch/vm_snapshot.go`
- Modify: `pkg/catch/vm_snapshot_test.go`

- [ ] **Step 1: Write tests for VM generation hiding and new names**

Add tests that assert:

```go
func TestVMRecoveryPointOmitsGenerationInJSON(t *testing.T) {
	point := recoveryPoint{
		Service:        "devbox",
		ServiceType:    db.ServiceTypeVM,
		ShortName:      "yeet-20260613T203100Z-vm-manual",
		CheckpointMode: recoveryCheckpointDisk,
		Generation:     nil,
	}
	got := renderRecoveryPointJSONForTest(t, point)
	if strings.Contains(got, `"generation":0`) {
		t.Fatalf("VM recovery point should omit generation: %s", got)
	}
}

func TestServiceRootRecoveryPointKeepsGenerationInJSON(t *testing.T) {
	gen := 4
	point := recoveryPoint{
		Service:        "plex",
		ServiceType:    db.ServiceTypeDockerCompose,
		ShortName:      "yeet-20260613T203100Z-run-g4",
		CheckpointMode: recoveryCheckpointServiceRoot,
		Generation:     &gen,
	}
	got := renderRecoveryPointJSONForTest(t, point)
	if !strings.Contains(got, `"generation":4`) {
		t.Fatalf("service-root recovery point should include generation: %s", got)
	}
}

func TestSnapshotShortNameCanOmitGeneration(t *testing.T) {
	now := time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC)
	got := snapshotShortName(snapshotCreateRequest{
		CreatedAt: now,
		Event:     snapshotEventVMManual,
	})
	if got != "yeet-20260613T203100Z-vm-manual" {
		t.Fatalf("snapshotShortName = %q", got)
	}
}

func TestSnapshotShortNameKeepsServiceGeneration(t *testing.T) {
	now := time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC)
	gen := 4
	got := snapshotShortName(snapshotCreateRequest{
		CreatedAt:  now,
		Event:      snapshotEventRun,
		Generation: &gen,
	})
	if got != "yeet-20260613T203100Z-run-g4" {
		t.Fatalf("snapshotShortName = %q", got)
	}
}
```

Run:

```bash
go test ./pkg/catch -run 'TestVMRecoveryPointOmitsGeneration|TestServiceRootRecoveryPointKeepsGeneration|TestSnapshotShortName' -count=1
```

Expected: FAIL because generation is currently a required int and snapshot names always add `-gN`.

- [ ] **Step 2: Change recovery point generation to optional**

In `pkg/catch/recovery_points.go`, change:

```go
Generation int `json:"generation"`
```

to:

```go
Generation *int `json:"generation,omitempty"`
```

When building service-root recovery points, assign:

```go
gen := snap.Generation
point.Generation = &gen
```

When building VM recovery points, set `Generation: nil`.

Add helper if multiple call sites need it:

```go
func intPtr(v int) *int {
	return &v
}
```

In human render output, do not print generation for VM rows. If a generation column already exists in a table, replace VM values with `-`; do not add a new visible generation column for VMs.

Run:

```bash
go test ./pkg/catch -run 'RecoveryPoint|SnapshotsList|SnapshotsInspect' -count=1
```

Expected: PASS after existing render tests are updated to the optional field.

- [ ] **Step 3: Change snapshot creation request to optional generation**

In `pkg/catch/service_snapshots.go`, change `snapshotCreateRequest` to:

```go
type snapshotCreateRequest struct {
	Dataset    string
	Service    string
	Event      snapshotEvent
	Generation *int
	Comment    string
	CreatedAt  time.Time
	Properties map[string]string
}
```

In `snapshotShortName`:

```go
base := fmt.Sprintf("yeet-%s-%s", req.CreatedAt.UTC().Format("20060102T150405Z"), req.Event)
if req.Generation == nil {
	return base
}
return fmt.Sprintf("%s-g%d", base, *req.Generation)
```

In service-root snapshot callers, pass:

```go
Generation: &service.Generation,
```

In VM snapshot callers, pass `Generation: nil`.

When writing ZFS properties, keep `yeet:generation` only when `Generation != nil`. Existing listed snapshots with a generation property still parse and select normally.

Run:

```bash
go test ./pkg/catch -run 'SnapshotShortName|VM.*Snapshot|ServiceSnapshot' -count=1
```

Expected: PASS.

- [ ] **Step 4: Add clone/restore actions**

In `pkg/catch/recovery_points.go`, append actions based on checkpoint/storage support:

```go
actions := []string{"inspect", "protect", "rm", "clone", "restore"}
if point.Protected {
	actions = []string{"inspect", "unprotect", "clone", "restore"}
}
```

For non-ZFS or raw-disk VM points, omit `clone` and `restore`. Tasks 5 and 6 add clone/restore support only for VM zvol snapshots and ZFS service-root snapshots:

```go
if !point.Restorable {
	actions = removeAction(actions, "restore")
}
if !point.Cloneable {
	actions = removeAction(actions, "clone")
}
```

If the current model does not have `Restorable` and `Cloneable`, add unexported booleans with JSON `omitempty` only when they are useful to CLI users:

```go
Cloneable   bool `json:"cloneable"`
Restorable  bool `json:"restorable"`
```

Run:

```bash
go test ./pkg/catch -run 'SnapshotsListCommandRendersRecoveryPoints|RecoveryPoint' -count=1
```

Expected: PASS with updated expected actions.

- [ ] **Step 5: Checkpoint commit**

Run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "snapshots: clarify generation metadata" --changes <ids-for-task-3-files>
```

Expected: one checkpoint commit with recovery point model cleanup only.

## Task 4: Add Safe ZFS Operation Helpers

**Files:**
- Create: `pkg/catch/recovery_zfs.go`
- Create: `pkg/catch/recovery_zfs_test.go`

- [ ] **Step 1: Write argv-level ZFS tests**

Create `pkg/catch/recovery_zfs_test.go` with tests:

```go
func TestZFSCloneSnapshotRunsExactArgv(t *testing.T) {
	runner := newRecordingZFSRunner()
	err := zfsCloneSnapshot(context.Background(), runner, "tank/app@yeet-a", "tank/app-copy")
	if err != nil {
		t.Fatalf("zfsCloneSnapshot: %v", err)
	}
	runner.assertCommand(t, []string{"zfs", "clone", "tank/app@yeet-a", "tank/app-copy"})
}

func TestZFSRollbackSnapshotRunsExactArgv(t *testing.T) {
	runner := newRecordingZFSRunner()
	err := zfsRollbackSnapshot(context.Background(), runner, "tank/app@yeet-a")
	if err != nil {
		t.Fatalf("zfsRollbackSnapshot: %v", err)
	}
	runner.assertCommand(t, []string{"zfs", "rollback", "tank/app@yeet-a"})
}

func TestZFSHelpersRejectUnsafeNames(t *testing.T) {
	runner := newRecordingZFSRunner()
	if err := zfsCloneSnapshot(context.Background(), runner, "tank/app", "tank/copy"); err == nil || !strings.Contains(err.Error(), "snapshot name must include @") {
		t.Fatalf("clone unsafe error = %v", err)
	}
	if err := zfsRollbackSnapshot(context.Background(), runner, "tank/app"); err == nil || !strings.Contains(err.Error(), "snapshot name must include @") {
		t.Fatalf("rollback unsafe error = %v", err)
	}
	if err := zfsDestroyDataset(context.Background(), runner, "tank/app@snap"); err == nil || !strings.Contains(err.Error(), "dataset name must not include @") {
		t.Fatalf("destroy unsafe error = %v", err)
	}
}
```

Use the existing fake ZFS runner pattern from `service_snapshots_test.go`. If no reusable fake exists, create a local `recordingZFSRunner` in this test file.

Run:

```bash
go test ./pkg/catch -run TestZFS -count=1
```

Expected: FAIL because helpers do not exist.

- [ ] **Step 2: Implement ZFS helpers**

Create `pkg/catch/recovery_zfs.go`:

```go
package catch

import (
	"context"
	"fmt"
	"strings"
)

func zfsCloneSnapshot(ctx context.Context, runner zfsCommandRunner, snapshotName, targetDataset string) error {
	if !strings.Contains(snapshotName, "@") {
		return fmt.Errorf("snapshot name must include @")
	}
	if strings.Contains(targetDataset, "@") || strings.TrimSpace(targetDataset) == "" {
		return fmt.Errorf("target dataset name is invalid")
	}
	_, stderr, err := runner.RunZFS(ctx, "clone", snapshotName, targetDataset)
	if err != nil {
		return fmt.Errorf("zfs clone %s %s: %w: %s", snapshotName, targetDataset, err, strings.TrimSpace(stderr))
	}
	return nil
}

func zfsRollbackSnapshot(ctx context.Context, runner zfsCommandRunner, snapshotName string) error {
	if !strings.Contains(snapshotName, "@") {
		return fmt.Errorf("snapshot name must include @")
	}
	_, stderr, err := runner.RunZFS(ctx, "rollback", snapshotName)
	if err != nil {
		return fmt.Errorf("zfs rollback %s: %w: %s", snapshotName, err, strings.TrimSpace(stderr))
	}
	return nil
}

func zfsDestroyDataset(ctx context.Context, runner zfsCommandRunner, dataset string) error {
	if strings.Contains(dataset, "@") {
		return fmt.Errorf("dataset name must not include @")
	}
	if strings.TrimSpace(dataset) == "" {
		return fmt.Errorf("dataset name is required")
	}
	_, stderr, err := runner.RunZFS(ctx, "destroy", "-r", dataset)
	if err != nil {
		return fmt.Errorf("zfs destroy %s: %w: %s", dataset, err, strings.TrimSpace(stderr))
	}
	return nil
}

func zfsDatasetMountpoint(ctx context.Context, runner zfsCommandRunner, dataset string) (string, error) {
	if strings.Contains(dataset, "@") || strings.TrimSpace(dataset) == "" {
		return "", fmt.Errorf("dataset name is invalid")
	}
	stdout, stderr, err := runner.RunZFS(ctx, "get", "-H", "-o", "value", "mountpoint", dataset)
	if err != nil {
		return "", fmt.Errorf("zfs get mountpoint %s: %w: %s", dataset, err, strings.TrimSpace(stderr))
	}
	mountpoint := strings.TrimSpace(stdout)
	if mountpoint == "" || mountpoint == "-" || mountpoint == "none" {
		return "", fmt.Errorf("dataset %s has no usable mountpoint", dataset)
	}
	return mountpoint, nil
}
```

Run:

```bash
go test ./pkg/catch -run TestZFS -count=1
```

Expected: PASS.

- [ ] **Step 3: Checkpoint commit**

Run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "snapshots: add zfs recovery helpers" --changes <ids-for-task-4-files>
```

Expected: one checkpoint commit with the focused ZFS helper file and tests.

## Task 5: Implement VM Zvol Clone And Disk Restore

**Files:**
- Create: `pkg/catch/recovery_vm.go`
- Create: `pkg/catch/recovery_vm_test.go`
- Modify: `pkg/catch/recovery_clone.go`
- Modify: `pkg/catch/recovery_restore.go`
- Modify: `pkg/catch/vm_runner.go` only if a small status helper is required
- Modify: `pkg/catch/tty_ops_test.go`

- [ ] **Step 1: Write VM clone tests**

Add tests in `pkg/catch/recovery_vm_test.go`:

```go
func TestSnapshotsCloneVMZvolCreatesStoppedService(t *testing.T) {
	server, runner := newSnapshotRecoveryTestServer(t)
	seedVMServiceWithZvolSnapshot(t, server, "devbox", "flash/yeet/vms/devbox/root", "yeet-20260613T203100Z-vm-manual")

	execer := newTTYExecerForTest(server)
	err := execer.snapshotsCloneCmdFunc("devbox", "yeet-20260613T203100Z-vm-manual", "devbox-copy", cli.SnapshotsCloneFlags{})
	if err != nil {
		t.Fatalf("snapshots clone vm: %v", err)
	}

	runner.assertCommand(t, []string{"zfs", "clone", "flash/yeet/vms/devbox/root@yeet-20260613T203100Z-vm-manual", "flash/yeet/vms/devbox-copy/root"})
	copy := mustGetService(t, server, "devbox-copy")
	if !copy.IsVM() {
		t.Fatalf("clone type = %q, want VM", copy.Type)
	}
	if copy.Name != "devbox-copy" || strings.Contains(copy.VMConfig.FirecrackerSocket, "devbox/") {
		t.Fatalf("clone did not get fresh runtime identity: %#v", copy.VMConfig)
	}
	assertSystemdNotStarted(t, "devbox-copy")
}

func TestSnapshotsCloneVMRejectsExistingTarget(t *testing.T) {
	server, _ := newSnapshotRecoveryTestServer(t)
	seedVMServiceWithZvolSnapshot(t, server, "devbox", "flash/yeet/vms/devbox/root", "yeet-a")
	seedVMService(t, server, "devbox-copy")

	err := newTTYExecerForTest(server).snapshotsCloneCmdFunc("devbox", "yeet-a", "devbox-copy", cli.SnapshotsCloneFlags{})
	if err == nil || !strings.Contains(err.Error(), "service devbox-copy already exists") {
		t.Fatalf("clone existing target error = %v", err)
	}
}
```

Use existing VM provisioning test helpers where possible. If the helper does not exist, create local seed helpers in this test file that only populate DB and fake ZFS snapshot output.

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsCloneVM' -count=1
```

Expected: FAIL because VM clone is not implemented.

- [ ] **Step 2: Implement VM clone**

In `pkg/catch/recovery_vm.go`, add:

```go
func (e *ttyExecer) cloneVMRecoveryPoint(ctx context.Context, service db.Service, point recoveryPoint, newName string, flags cli.SnapshotsCloneFlags) error {
	if !service.IsVM() {
		return fmt.Errorf("service %s is not a VM", service.Name)
	}
	if point.StorageKind != recoveryStorageVMZvol {
		return fmt.Errorf("VM snapshot %s is not backed by a ZFS zvol", point.ShortName)
	}
	if _, err := e.server.db.GetService(newName); err == nil {
		return fmt.Errorf("service %s already exists", newName)
	}

	targetZvol := vmCloneZvolName(service, newName, point)
	if err := zfsCloneSnapshot(ctx, e.server.zfsRunner, point.FullName, targetZvol); err != nil {
		return err
	}

	clone, err := cloneVMServiceRecord(service, newName, targetZvol)
	if err != nil {
		_ = zfsDestroyDataset(ctx, e.server.zfsRunner, targetZvol)
		return err
	}
	if err := e.server.db.PutService(clone); err != nil {
		_ = zfsDestroyDataset(ctx, e.server.zfsRunner, targetZvol)
		return err
	}
	if err := e.server.installVMService(ctx, clone); err != nil {
		_ = e.server.db.DeleteService(newName)
		_ = zfsDestroyDataset(ctx, e.server.zfsRunner, targetZvol)
		return err
	}
	if flags.Start {
		return e.server.startService(ctx, newName)
	}
	return e.server.stopService(ctx, newName)
}
```

If `installVMService`, `startService`, or `stopService` do not exist with those names, implement thin wrappers in `recovery_vm.go` that call the existing VM/systemd helper functions. Do not duplicate systemd command construction.

Implement `cloneVMServiceRecord` so it copies only stable VM config and regenerates:

- `Name`
- `ServiceRoot`
- `VMConfig.Zvol`
- `VMConfig.FirecrackerSocket`
- `VMConfig.FirecrackerConfigPath`
- `VMConfig.PIDFile`
- systemd unit name
- tap/macvlan interface names
- MAC address if a generated MAC exists on the source
- service network identity if the source has one

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsCloneVM' -count=1
```

Expected: PASS.

- [ ] **Step 3: Write VM disk restore tests**

Add tests:

```go
func TestSnapshotsRestoreVMRequiresStoppedOrStopFlag(t *testing.T) {
	server, _ := newSnapshotRecoveryTestServer(t)
	seedRunningVMServiceWithZvolSnapshot(t, server, "devbox", "flash/yeet/vms/devbox/root", "yeet-a")

	err := newTTYExecerForTest(server).snapshotsRestoreCmdFunc("devbox", "yeet-a", cli.SnapshotsRestoreFlags{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "VM devbox is running; pass --stop to stop it before restore") {
		t.Fatalf("restore running vm error = %v", err)
	}
}

func TestSnapshotsRestoreVMCreatesPreRestoreSnapshotThenRollsBack(t *testing.T) {
	server, runner := newSnapshotRecoveryTestServer(t)
	seedRunningVMServiceWithZvolSnapshot(t, server, "devbox", "flash/yeet/vms/devbox/root", "yeet-a")

	err := newTTYExecerForTest(server).snapshotsRestoreCmdFunc("devbox", "yeet-a", cli.SnapshotsRestoreFlags{Stop: true, Yes: true})
	if err != nil {
		t.Fatalf("restore vm disk: %v", err)
	}

	runner.assertContainsCommand(t, []string{"zfs", "snapshot"})
	runner.assertContainsCommand(t, []string{"zfs", "rollback", "flash/yeet/vms/devbox/root@yeet-a"})
	assertSystemdStopped(t, "devbox")
}
```

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsRestoreVM' -count=1
```

Expected: FAIL because VM restore is not implemented.

- [ ] **Step 4: Implement VM disk restore**

In `pkg/catch/recovery_vm.go`, add:

```go
func (e *ttyExecer) restoreVMRecoveryPoint(ctx context.Context, service db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags) error {
	if !service.IsVM() {
		return fmt.Errorf("service %s is not a VM", service.Name)
	}
	if point.StorageKind != recoveryStorageVMZvol {
		return fmt.Errorf("VM snapshot %s is not backed by a ZFS zvol", point.ShortName)
	}
	running, err := e.server.isServiceRunning(ctx, service.Name)
	if err != nil {
		return err
	}
	if running && !flags.Stop {
		return fmt.Errorf("VM %s is running; pass --stop to stop it before restore", service.Name)
	}
	preRestore, err := e.server.createManualRecoveryPoint(ctx, service, "pre-restore before "+point.ShortName, false)
	if err != nil {
		return fmt.Errorf("create pre-restore recovery point: %w", err)
	}
	fmt.Fprintf(e.stdout, "Created pre-restore recovery point: %s\n", preRestore)
	if running {
		if err := e.server.stopService(ctx, service.Name); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Stopped service: %s\n", service.Name)
	}
	if flags.Mode == "full" {
		return e.restoreVMFullCheckpoint(ctx, service, point, flags)
	}
	if err := zfsRollbackSnapshot(ctx, e.server.zfsRunner, point.FullName); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Rolled back VM disk: %s\n", point.FullName)
	if flags.Start {
		if err := e.server.startService(ctx, service.Name); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Started service: %s\n", service.Name)
	}
	fmt.Fprintln(e.stdout, "Restore complete.")
	return nil
}
```

For `restoreVMFullCheckpoint`, return the compatibility error from Task 7 until Task 7 replaces it:

```go
func (e *ttyExecer) restoreVMFullCheckpoint(ctx context.Context, service db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags) error {
	return fmt.Errorf("full VM state restore is not implemented yet; use --mode=disk")
}
```

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsRestoreVM' -count=1
```

Expected: PASS.

- [ ] **Step 5: Connect top-level clone/restore dispatch for VM paths**

In `pkg/catch/recovery_clone.go`, implement:

```go
func (e *ttyExecer) snapshotsCloneCmdFunc(serviceName, selector, newServiceName string, flags cli.SnapshotsCloneFlags) error {
	ctx := context.Background()
	service, point, err := e.loadRecoveryTarget(ctx, serviceName, selector)
	if err != nil {
		return err
	}
	if service.IsVM() {
		return e.cloneVMRecoveryPoint(ctx, service, point, newServiceName, flags)
	}
	return e.cloneServiceRootRecoveryPoint(ctx, service, point, newServiceName, flags)
}
```

In `pkg/catch/recovery_restore.go`, implement:

```go
func (e *ttyExecer) snapshotsRestoreCmdFunc(serviceName, selector string, flags cli.SnapshotsRestoreFlags) error {
	ctx := context.Background()
	if !flags.Yes && !confirmRestore(e.stdin, e.stdout, serviceName, selector) {
		fmt.Fprintln(e.stdout, "Restore cancelled.")
		return nil
	}
	service, point, err := e.loadRecoveryTarget(ctx, serviceName, selector)
	if err != nil {
		return err
	}
	if service.IsVM() {
		return e.restoreVMRecoveryPoint(ctx, service, point, flags)
	}
	return e.restoreServiceRootRecoveryPoint(ctx, service, point, flags)
}
```

Add `loadRecoveryTarget` to validate:

- service exists
- selector resolves to exactly one recovery point for that service
- point is yeet-owned
- point full snapshot name targets the current service zvol/dataset

Run:

```bash
go test ./pkg/catch -run 'TestSnapshots(Clone|Restore)VM|TestSnapshotsProtectAndRemoveCommands' -count=1
```

Expected: PASS.

- [ ] **Step 6: Checkpoint commit**

Run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "snapshots: clone and restore vm disks" --changes <ids-for-task-5-files>
```

Expected: one checkpoint commit with VM clone and disk restore.

## Task 6: Implement ZFS Service-Root Clone And Restore

**Files:**
- Create: `pkg/catch/recovery_service_root.go`
- Create: `pkg/catch/recovery_service_root_test.go`
- Modify: `pkg/catch/recovery_clone.go`
- Modify: `pkg/catch/recovery_restore.go`
- Modify: `pkg/catch/tty_ops_test.go`

- [ ] **Step 1: Write service-root clone tests**

Add `pkg/catch/recovery_service_root_test.go`:

```go
func TestSnapshotsCloneServiceRootClonesDatasetAndCurrentDefinition(t *testing.T) {
	server, runner := newSnapshotRecoveryTestServer(t)
	seedComposeServiceWithRootSnapshot(t, server, "app", "flash/yeet/services/app", "/flash/yeet/services/app", "yeet-20260613T203100Z-run-g3", 3)

	err := newTTYExecerForTest(server).snapshotsCloneCmdFunc("app", "yeet-20260613T203100Z-run-g3", "app-restore", cli.SnapshotsCloneFlags{})
	if err != nil {
		t.Fatalf("clone service root: %v", err)
	}

	runner.assertContainsCommand(t, []string{"zfs", "clone", "flash/yeet/services/app@yeet-20260613T203100Z-run-g3", "flash/yeet/services/app-restore"})
	clone := mustGetService(t, server, "app-restore")
	if clone.ServiceRootZFS != "flash/yeet/services/app-restore" {
		t.Fatalf("clone dataset = %q", clone.ServiceRootZFS)
	}
	if clone.ServiceRoot != "/flash/yeet/services/app-restore" {
		t.Fatalf("clone root = %q", clone.ServiceRoot)
	}
}

func TestSnapshotsCloneServiceRootRejectsNonZFSRoot(t *testing.T) {
	server, _ := newSnapshotRecoveryTestServer(t)
	seedFileRootService(t, server, "app", "/srv/app")

	err := newTTYExecerForTest(server).snapshotsCloneCmdFunc("app", "yeet-a", "app-restore", cli.SnapshotsCloneFlags{})
	if err == nil || !strings.Contains(err.Error(), "service app is not backed by a ZFS service root") {
		t.Fatalf("clone non-zfs error = %v", err)
	}
}
```

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsCloneServiceRoot' -count=1
```

Expected: FAIL because service-root clone is not implemented.

- [ ] **Step 2: Implement service-root clone**

In `pkg/catch/recovery_service_root.go`, add:

```go
func (e *ttyExecer) cloneServiceRootRecoveryPoint(ctx context.Context, service db.Service, point recoveryPoint, newName string, flags cli.SnapshotsCloneFlags) error {
	if service.ServiceRootZFS == "" {
		return fmt.Errorf("service %s is not backed by a ZFS service root", service.Name)
	}
	if _, err := e.server.db.GetService(newName); err == nil {
		return fmt.Errorf("service %s already exists", newName)
	}
	targetDataset := serviceRootCloneDataset(service.ServiceRootZFS, service.Name, newName)
	if err := zfsCloneSnapshot(ctx, e.server.zfsRunner, point.FullName, targetDataset); err != nil {
		return err
	}
	targetRoot, err := zfsDatasetMountpoint(ctx, e.server.zfsRunner, targetDataset)
	if err != nil {
		_ = zfsDestroyDataset(ctx, e.server.zfsRunner, targetDataset)
		return err
	}
	clone, err := cloneServiceRootRecord(service, newName, targetDataset, targetRoot)
	if err != nil {
		_ = zfsDestroyDataset(ctx, e.server.zfsRunner, targetDataset)
		return err
	}
	if err := e.server.db.PutService(clone); err != nil {
		_ = zfsDestroyDataset(ctx, e.server.zfsRunner, targetDataset)
		return err
	}
	if err := e.server.installCurrentServiceDefinition(ctx, clone); err != nil {
		_ = e.server.db.DeleteService(newName)
		_ = zfsDestroyDataset(ctx, e.server.zfsRunner, targetDataset)
		return err
	}
	if !flags.Start {
		return e.server.stopService(ctx, newName)
	}
	return nil
}
```

`cloneServiceRootRecord` must:

- copy service type and deploy definition
- replace service name
- replace `ServiceRootZFS`
- replace `ServiceRoot`
- rewrite artifact paths only when they start with the old service root
- leave external image references and non-root paths unchanged
- reset runtime-only fields that must be unique per service

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsCloneServiceRoot' -count=1
```

Expected: PASS.

- [ ] **Step 3: Write service-root restore tests**

Add tests:

```go
func TestSnapshotsRestoreServiceRootCreatesPreRestoreAndRollsBack(t *testing.T) {
	server, runner := newSnapshotRecoveryTestServer(t)
	seedStoppedComposeServiceWithRootSnapshot(t, server, "app", "flash/yeet/services/app", "yeet-a", 3)

	err := newTTYExecerForTest(server).snapshotsRestoreCmdFunc("app", "yeet-a", cli.SnapshotsRestoreFlags{Yes: true})
	if err != nil {
		t.Fatalf("restore service root: %v", err)
	}

	runner.assertContainsCommand(t, []string{"zfs", "snapshot"})
	runner.assertContainsCommand(t, []string{"zfs", "rollback", "flash/yeet/services/app@yeet-a"})
}

func TestSnapshotsRestoreServiceRootGenerationSnapshotRunsServiceRollback(t *testing.T) {
	server, _ := newSnapshotRecoveryTestServer(t)
	seedStoppedComposeServiceWithRootSnapshot(t, server, "app", "flash/yeet/services/app", "yeet-a", 2)
	setCurrentGeneration(t, server, "app", 4, 4)

	err := newTTYExecerForTest(server).snapshotsRestoreCmdFunc("app", "yeet-a", cli.SnapshotsRestoreFlags{Yes: true, Generation: "snapshot"})
	if err != nil {
		t.Fatalf("restore service root with generation: %v", err)
	}
	got := mustGetService(t, server, "app")
	if got.Generation != 2 {
		t.Fatalf("generation = %d, want 2", got.Generation)
	}
}
```

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsRestoreServiceRoot' -count=1
```

Expected: FAIL because service-root restore is not implemented.

- [ ] **Step 4: Implement service-root restore**

In `pkg/catch/recovery_service_root.go`, add:

```go
func (e *ttyExecer) restoreServiceRootRecoveryPoint(ctx context.Context, service db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags) error {
	if service.ServiceRootZFS == "" {
		return fmt.Errorf("service %s is not backed by a ZFS service root", service.Name)
	}
	running, err := e.server.isServiceRunning(ctx, service.Name)
	if err != nil {
		return err
	}
	if running && !flags.Stop {
		return fmt.Errorf("service %s is running; pass --stop to stop it before restore", service.Name)
	}
	preRestore, err := e.server.createManualRecoveryPoint(ctx, service, "pre-restore before "+point.ShortName, false)
	if err != nil {
		return fmt.Errorf("create pre-restore recovery point: %w", err)
	}
	fmt.Fprintf(e.stdout, "Created pre-restore recovery point: %s\n", preRestore)
	if running {
		if err := e.server.stopService(ctx, service.Name); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Stopped service: %s\n", service.Name)
	}
	if err := zfsRollbackSnapshot(ctx, e.server.zfsRunner, point.FullName); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Rolled back service root: %s\n", point.FullName)
	if flags.Generation == "snapshot" {
		if point.Generation == nil {
			return fmt.Errorf("snapshot %s does not record a service generation", point.ShortName)
		}
		if err := e.rollbackToGeneration(service.Name, *point.Generation); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Rolled back service definition generation: %d\n", *point.Generation)
	}
	if flags.Start {
		if err := e.server.startService(ctx, service.Name); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Started service: %s\n", service.Name)
	}
	fmt.Fprintln(e.stdout, "Restore complete.")
	return nil
}
```

Run:

```bash
go test ./pkg/catch -run 'TestSnapshotsRestoreServiceRoot' -count=1
```

Expected: PASS.

- [ ] **Step 5: Checkpoint commit**

Run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "snapshots: clone and restore service roots" --changes <ids-for-task-6-files>
```

Expected: one checkpoint commit with service-root clone/restore.

## Task 7: Implement Full VM Checkpoint Metadata And Restore Gate

**Files:**
- Modify: `pkg/catch/vm_snapshot.go`
- Modify: `pkg/catch/vm_snapshot_test.go`
- Create: `pkg/catch/firecracker_snapshot_restore.go`
- Create: `pkg/catch/firecracker_snapshot_restore_test.go`
- Modify: `pkg/catch/recovery_vm.go`
- Modify: `pkg/catch/recovery_vm_test.go`
- Modify: `website/docs/payloads/vms.mdx` in Task 8

- [ ] **Step 1: Write metadata tests for new full checkpoints**

Add to `pkg/catch/vm_snapshot_test.go`:

```go
func TestWriteVMCheckpointMetadataIncludesCompatibilityFields(t *testing.T) {
	meta := vmCheckpointMetadata{
		Service:            "devbox",
		Mode:               "full",
		ZvolSnapshot:       "flash/yeet/vms/devbox/root@yeet-a",
		FirecrackerState:   "/var/lib/yeet/devbox/state",
		FirecrackerMemory:  "/var/lib/yeet/devbox/mem",
		FirecrackerVersion: "v1.12.1",
		MachineConfigHash:  "sha256:machine",
		NetworkConfigHash:  "sha256:network",
		DiskPath:           "/dev/zvol/flash/yeet/vms/devbox/root",
		VCPU:               2,
		MemoryMiB:          2048,
		VMConfigHash:       "sha256:vm",
	}
	got := marshalVMCheckpointMetadataForTest(t, meta)
	for _, want := range []string{"firecrackerVersion", "machineConfigHash", "networkConfigHash", "diskPath", "vcpu", "memoryMiB", "vmConfigHash"} {
		if !strings.Contains(got, want) {
			t.Fatalf("metadata missing %s: %s", want, got)
		}
	}
}
```

Run:

```bash
go test ./pkg/catch -run TestWriteVMCheckpointMetadataIncludesCompatibilityFields -count=1
```

Expected: FAIL because metadata does not contain these fields.

- [ ] **Step 2: Extend full checkpoint metadata**

In `pkg/catch/vm_snapshot.go`, extend `vmCheckpointMetadata`:

```go
FirecrackerVersion string `json:"firecrackerVersion,omitempty"`
FirecrackerSHA256  string `json:"firecrackerSha256,omitempty"`
MachineConfigHash  string `json:"machineConfigHash,omitempty"`
NetworkConfigHash  string `json:"networkConfigHash,omitempty"`
DiskPath           string `json:"diskPath,omitempty"`
VCPU               int    `json:"vcpu,omitempty"`
MemoryMiB          int    `json:"memoryMiB,omitempty"`
VMConfigHash       string `json:"vmConfigHash,omitempty"`
```

When creating a full checkpoint, populate these fields from current VM config and rendered Firecracker config before writing metadata. Hash canonical JSON for machine, network, and VM config:

```go
func stableJSONSHA256(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
```

Run:

```bash
go test ./pkg/catch -run 'TestWriteVMCheckpointMetadataIncludesCompatibilityFields|TestCreatePausedVMSnapshot' -count=1
```

Expected: PASS.

- [ ] **Step 3: Write Firecracker load snapshot request tests**

Create `pkg/catch/firecracker_snapshot_restore_test.go`:

```go
func TestFirecrackerLoadSnapshotRequest(t *testing.T) {
	req := firecrackerLoadSnapshotRequest("/state", "/memory", true)
	got := marshalJSONForTest(t, req)
	for _, want := range []string{`"snapshot_path":"/state"`, `"backend_path":"/memory"`, `"backend_type":"File"`, `"resume_vm":true`} {
		if !strings.Contains(got, want) {
			t.Fatalf("load snapshot request missing %s: %s", want, got)
		}
	}
}
```

Run:

```bash
go test ./pkg/catch -run TestFirecrackerLoadSnapshotRequest -count=1
```

Expected: FAIL because helper does not exist.

- [ ] **Step 4: Implement Firecracker snapshot load request**

Create `pkg/catch/firecracker_snapshot_restore.go`:

```go
package catch

type firecrackerLoadSnapshotBody struct {
	SnapshotPath    string                     `json:"snapshot_path"`
	MemBackend      firecrackerMemoryBackend   `json:"mem_backend"`
	EnableDiff      bool                       `json:"enable_diff_snapshots"`
	ResumeVM        bool                       `json:"resume_vm"`
}

type firecrackerMemoryBackend struct {
	BackendPath string `json:"backend_path"`
	BackendType string `json:"backend_type"`
}

func firecrackerLoadSnapshotRequest(statePath, memoryPath string, resume bool) firecrackerLoadSnapshotBody {
	return firecrackerLoadSnapshotBody{
		SnapshotPath: statePath,
		MemBackend: firecrackerMemoryBackend{
			BackendPath: memoryPath,
			BackendType: "File",
		},
		ResumeVM: resume,
	}
}
```

Add a method to the existing Firecracker API client:

```go
func (c *firecrackerClient) LoadSnapshot(ctx context.Context, statePath, memoryPath string, resume bool) error {
	return c.putJSON(ctx, "/snapshot/load", firecrackerLoadSnapshotRequest(statePath, memoryPath, resume))
}
```

Use the existing `putJSON` or HTTP request helper. If the existing client uses another method name, add the new method beside the existing pause/snapshot APIs without changing their call sites.

Run:

```bash
go test ./pkg/catch -run 'Firecracker.*Snapshot' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write full restore compatibility tests**

Add to `pkg/catch/recovery_vm_test.go`:

```go
func TestRestoreVMFullRejectsOldCheckpointMetadataBeforeMutation(t *testing.T) {
	server, runner := newSnapshotRecoveryTestServer(t)
	seedFullVMCheckpointWithoutCompatibilityMetadata(t, server, "devbox", "yeet-full-old")

	err := newTTYExecerForTest(server).snapshotsRestoreCmdFunc("devbox", "yeet-full-old", cli.SnapshotsRestoreFlags{Yes: true, Stop: true, Mode: "full"})
	if err == nil || !strings.Contains(err.Error(), "full checkpoint metadata is missing compatibility fields") {
		t.Fatalf("full restore old metadata error = %v", err)
	}
	runner.assertNoCommand(t, "rollback")
}

func TestRestoreVMFullRejectsCompatibilityMismatchBeforeMutation(t *testing.T) {
	server, runner := newSnapshotRecoveryTestServer(t)
	seedFullVMCheckpointWithMetadata(t, server, "devbox", "yeet-full", vmCheckpointMetadata{VCPU: 4, MemoryMiB: 4096})
	setCurrentVMShape(t, server, "devbox", 2, 2048)

	err := newTTYExecerForTest(server).snapshotsRestoreCmdFunc("devbox", "yeet-full", cli.SnapshotsRestoreFlags{Yes: true, Stop: true, Mode: "full"})
	if err == nil || !strings.Contains(err.Error(), "checkpoint CPU or memory does not match current VM config") {
		t.Fatalf("full restore mismatch error = %v", err)
	}
	runner.assertNoCommand(t, "rollback")
}
```

Run:

```bash
go test ./pkg/catch -run 'TestRestoreVMFullRejects' -count=1
```

Expected: FAIL because `restoreVMFullCheckpoint` still returns the temporary error.

- [ ] **Step 6: Implement full restore gate and disk rollback sequencing**

In `pkg/catch/recovery_vm.go`, implement:

```go
func (e *ttyExecer) restoreVMFullCheckpoint(ctx context.Context, service db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags) error {
	meta, err := readVMCheckpointMetadata(point)
	if err != nil {
		return err
	}
	if err := validateFullCheckpointCompatibility(service, point, meta); err != nil {
		return err
	}
	if err := zfsRollbackSnapshot(ctx, e.server.zfsRunner, point.FullName); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Rolled back VM disk: %s\n", point.FullName)
	if err := e.server.startFirecrackerForSnapshotLoad(ctx, service); err != nil {
		return err
	}
	client := e.server.firecrackerClientFor(service)
	if err := client.LoadSnapshot(ctx, meta.FirecrackerState, meta.FirecrackerMemory, true); err != nil {
		return fmt.Errorf("load Firecracker snapshot: %w", err)
	}
	fmt.Fprintln(e.stdout, "Restored Firecracker state.")
	if flags.Start {
		fmt.Fprintf(e.stdout, "Started service: %s\n", service.Name)
	}
	return nil
}
```

`validateFullCheckpointCompatibility` must fail before mutation when:

- checkpoint mode is not `full`
- state path is empty or missing on disk
- memory path is empty or missing on disk
- metadata lacks `FirecrackerVersion` or `FirecrackerSHA256`
- metadata lacks config hashes
- CPU count differs
- memory size differs
- disk path differs
- current VM config hash differs

If the existing VM systemd launcher cannot start Firecracker in a pre-boot API state needed for `LoadSnapshot`, replace the restore body after compatibility validation with:

```go
return fmt.Errorf("full VM state restore is not supported by the current yeet Firecracker launcher; use --mode=disk")
```

and keep the tests asserting no mutation on unsupported launchers. Also update docs in Task 8 to state that full checkpoints can be captured and inspected, but first-pass restore is disk-only until the launcher supports pre-boot snapshot loading.

Run:

```bash
go test ./pkg/catch -run 'TestRestoreVMFull|Firecracker.*Snapshot|VMCheckpoint' -count=1
```

Expected:

- PASS if full restore is implemented with a launch path.
- PASS with explicit unsupported-launcher behavior if the launcher cannot safely support `/snapshot/load` yet.

- [ ] **Step 7: Checkpoint commit**

Run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "snapshots: gate full vm checkpoint restore" --changes <ids-for-task-7-files>
```

Expected: one checkpoint commit with full metadata and either implemented full restore or a proven unsupported-launcher error.

## Task 8: Update User Docs And Website Manual

**Files:**
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/concepts/zfs.mdx`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/changelog.mdx` only when preparing an actual release, not for this implementation checkpoint
- Modify: root submodule pointer after committing inside `website/`

- [ ] **Step 1: Read website instructions**

Run:

```bash
sed -n '1,240p' website/AGENTS.md
```

Expected: website-specific docs and screenshot instructions are loaded.

- [ ] **Step 2: Update CLI reference**

In `website/docs/cli/yeet-cli.mdx`, document:

```mdx
### Service generations

Use service generations to move a deployed definition back to an earlier build:

```bash
yeet service generations <svc>
yeet service rollback <svc>
```

Generations are for service definitions and install artifacts. They are not a
VM disk or memory recovery mechanism.

### Snapshots

Use snapshots to recover persistent state:

```bash
yeet snapshots list [svc]
yeet snapshots inspect <svc> <snapshot>
yeet snapshots create <svc> [--comment=TEXT] [--full]
yeet snapshots clone <svc> <snapshot> <new-svc> [--start]
yeet snapshots restore <svc> <snapshot> [--stop] [--start] [--yes]
yeet snapshots protect <svc> <snapshot>
yeet snapshots unprotect <svc> <snapshot>
yeet snapshots rm <svc> <snapshot> [--yes]
```
```

Run:

```bash
rg -n "yeet rollback|service rollback|snapshots clone|snapshots restore" website/docs/cli/yeet-cli.mdx
```

Expected: old top-level `yeet rollback` text is gone; new commands appear.

- [ ] **Step 3: Update ZFS concept docs**

In `website/docs/concepts/zfs.mdx`, add a user-facing section:

```mdx
## Recovery workflow

Use `clone` first when you want to inspect or recover files without changing the
running service:

```bash
yeet snapshots clone plex yeet-20260613T203100Z-run-g4 plex-recover
```

Use `restore` when you want to roll the original service data back in place:

```bash
yeet snapshots restore plex yeet-20260613T203100Z-run-g4 --stop --yes
```

`restore` creates a pre-restore recovery point before it mutates ZFS state. If
the selected snapshot recorded a service generation and you also want the old
deployed definition, pass `--generation=snapshot`.
```

Run:

```bash
rg -n "Recovery workflow|snapshots clone|snapshots restore|generation=snapshot" website/docs/concepts/zfs.mdx
```

Expected: new recovery section appears and distinguishes state from definition rollback.

- [ ] **Step 4: Update VM docs**

In `website/docs/payloads/vms.mdx`, document:

```mdx
## VM snapshots and restore

VM snapshots protect the VM disk zvol. A full checkpoint also captures
Firecracker state and memory, but disk restore remains the default:

```bash
yeet snapshots create devbox --comment "before upgrade"
yeet snapshots restore devbox yeet-20260613T203100Z-vm-manual --stop --yes
```

Use `--mode=full` only for a full checkpoint that yeet can prove is compatible
with the current Firecracker runtime and VM shape:

```bash
yeet snapshots restore devbox yeet-20260613T203100Z-vm-manual --mode=full --stop
```

If compatibility cannot be proven, yeet refuses full restore before mutating the
VM disk and tells you to use `--mode=disk`.
```

If Task 7 ended with unsupported launcher behavior, replace the `--mode=full` paragraph with:

```mdx
Full checkpoints can be captured and inspected, but this release restores VM
disks only. `--mode=full` is refused before disk mutation until yeet's
Firecracker launcher can safely start a microVM in the pre-boot state required
by Firecracker snapshot loading.
```

Run:

```bash
rg -n "VM snapshots and restore|mode=full|mode=disk|Firecracker" website/docs/payloads/vms.mdx
```

Expected: VM docs match the actual Task 7 behavior.

- [ ] **Step 5: Update operations workflow docs**

In `website/docs/operations/workflows.mdx`, add:

```mdx
## Clone-first recovery

When you are unsure what a snapshot contains, clone it into a new service first:

```bash
yeet snapshots clone app yeet-20260613T203100Z-run-g4 app-recover
yeet info app-recover
```

After you verify the clone, either copy the recovered data out or restore the
original service in place:

```bash
yeet snapshots restore app yeet-20260613T203100Z-run-g4 --stop --yes
```
```

Run:

```bash
rg -n "Clone-first recovery|app-recover" website/docs/operations/workflows.mdx
```

Expected: clone-first recovery workflow appears.

- [ ] **Step 6: Commit website repo, then root submodule pointer**

Inside `website/`, run the website's documented verification command from `website/AGENTS.md`. If no website-specific command exists, run the repo's docs lint/build command shown there.

Then use GitButler in the website repo if configured; otherwise follow that repo's AGENTS instructions. The required outcome is:

- website docs changes committed and pushed inside `/Users/shayne/code/yeet/website`
- root repo records the updated `website` submodule pointer

For the root repo, run:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "docs: explain snapshot recovery lifecycle" --changes <ids-for-root-doc-or-submodule-pointer-files>
```

Expected: website docs commit exists in the website repo and the root repo points at it.

## Task 9: Verification, Live Smoke, And Final Cleanup

**Files:**
- Modify only if verification finds real bugs.

- [ ] **Step 1: Run targeted tests**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go suite**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: PASS. If a hook fixes files, inspect the diff and commit the hook output with the relevant implementation commit or a single cleanup commit.

- [ ] **Step 4: Install catch on live VM-capable host**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet version
```

Expected: catch installs and reports the local build version.

- [ ] **Step 5: Live VM zvol clone and restore smoke**

Use a unique disposable service name:

```bash
export TEST_VM="codex-vm-snap-$(date +%Y%m%d%H%M%S)"
export TEST_VM_CLONE="${TEST_VM}-clone"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$TEST_VM" vm://nixos/26.05 --image-policy=cached --net=svc --service-root="flash/yeet/vms/${TEST_VM}" --zfs
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm snapshot "$TEST_VM" --comment "codex recovery smoke"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots list "$TEST_VM"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots clone "$TEST_VM" <snapshot-short-name> "$TEST_VM_CLONE"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet info "$TEST_VM_CLONE"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$TEST_VM" <snapshot-short-name> --stop --yes
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$TEST_VM_CLONE" --yes --clean-data --clean-config
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$TEST_VM" --yes --clean-data --clean-config
```

Expected:

- snapshot list shows the VM recovery point without a misleading generation
- clone creates a stopped VM service
- restore creates a pre-restore recovery point before rollback
- cleanup removes both services

- [ ] **Step 6: Live service-root clone and restore smoke**

Use a small compose or binary payload with a ZFS service root:

```bash
export TEST_SVC="codex-svc-snap-$(date +%Y%m%d%H%M%S)"
export TEST_SVC_CLONE="${TEST_SVC}-clone"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$TEST_SVC" ./bin/helloworld --service-root="flash/yeet/${TEST_SVC}" --zfs --snapshots=on
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots create "$TEST_SVC" --comment "codex service-root smoke"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots list "$TEST_SVC"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots clone "$TEST_SVC" <snapshot-short-name> "$TEST_SVC_CLONE"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet info "$TEST_SVC_CLONE"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$TEST_SVC" <snapshot-short-name> --stop --yes
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$TEST_SVC_CLONE" --yes --clean-data --clean-config
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$TEST_SVC" --yes --clean-data --clean-config
```

Expected:

- service-root snapshot includes service generation metadata
- clone creates a distinct service rooted at a distinct ZFS dataset
- restore creates a pre-restore recovery point and rolls back the dataset
- cleanup removes both services

- [ ] **Step 7: Full checkpoint live smoke or documented refusal**

If Task 7 implemented full restore:

```bash
export TEST_FULL_VM="codex-vm-full-$(date +%Y%m%d%H%M%S)"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$TEST_FULL_VM" vm://nixos/26.05 --image-policy=cached --net=svc --service-root="flash/yeet/vms/${TEST_FULL_VM}" --zfs
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots create "$TEST_FULL_VM" --full --comment "codex full checkpoint smoke"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$TEST_FULL_VM" <snapshot-short-name> --mode=full --stop --yes
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$TEST_FULL_VM" --yes --clean-data --clean-config
```

Expected: full restore succeeds and guest access works after restore.

If Task 7 produced unsupported-launcher behavior:

```bash
export TEST_FULL_VM="codex-vm-full-$(date +%Y%m%d%H%M%S)"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$TEST_FULL_VM" vm://nixos/26.05 --image-policy=cached --net=svc --service-root="flash/yeet/vms/${TEST_FULL_VM}" --zfs
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots create "$TEST_FULL_VM" --full --comment "codex full checkpoint refusal smoke"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$TEST_FULL_VM" <snapshot-short-name> --mode=full --stop --yes
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet snapshots restore "$TEST_FULL_VM" <snapshot-short-name> --mode=disk --stop --yes
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$TEST_FULL_VM" --yes --clean-data --clean-config
```

Expected:

- `--mode=full` fails before ZFS rollback with the documented unsupported-launcher message
- `--mode=disk` still restores the same snapshot successfully

- [ ] **Step 8: Final cleanup commit if verification changed files**

If verification exposed real bugs and fixes are uncommitted:

```bash
but status -fv
but commit codex/complete-snapshot-recovery -m "snapshots: harden recovery verification" --changes <ids-for-fix-files>
```

Expected: no unassigned implementation changes remain.

- [ ] **Step 9: Final branch status**

Run:

```bash
but status -fv
```

Expected:

- no unassigned changes
- implementation branch contains coherent commits
- no work from another branch is included
- website submodule pointer is committed if website docs changed

## Plan Self-Review

Spec coverage:

- Service generation commands: Task 1.
- Removal of top-level rollback: Task 1.
- VM generation rollback non-goal: Task 1 and Task 8.
- Snapshot clone/restore CLI: Task 2.
- VM generation display/name cleanup: Task 3.
- ZFS safety helpers and exact argv testing: Task 4.
- VM zvol clone and disk restore: Task 5.
- Service-root clone and restore: Task 6.
- Full VM state restore compatibility gate: Task 7.
- Docs: Task 8.
- Local and live verification: Task 9.

Placeholder scan:

- This plan contains no unresolved placeholder steps.
- Temporary clone/restore errors introduced in Task 2 are explicitly removed by Tasks 5 and 6 before final verification.
- Full-state restore has a concrete implementation path and a concrete unsupported-launcher behavior if the current runtime cannot safely support Firecracker `/snapshot/load`.

Type consistency:

- `SnapshotsCloneFlags`, `SnapshotsRestoreFlags`, and `ServiceGenerationsFlags` are introduced before use.
- `recoveryPoint.Generation` is consistently `*int`.
- `restoreVMFullCheckpoint`, `cloneVMRecoveryPoint`, and `restoreServiceRootRecoveryPoint` signatures match their dispatch call sites.
