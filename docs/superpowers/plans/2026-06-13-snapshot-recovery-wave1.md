# Snapshot Recovery Wave 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the first usable snapshot recovery lifecycle: list, inspect, create, remove, protect, and unprotect yeet-owned recovery points.

**Architecture:** Add a shared catch-side `RecoveryPoint` model derived from ZFS snapshot properties and current service DB state. Extend the existing `yeet snapshots` command group beyond defaults, while keeping `yeet vm snapshot` as a convenience alias for VM creation. Protection is a ZFS user property, and retention pruning skips protected snapshots.

**Tech Stack:** Go, yargs CLI metadata, catch TTY remote command routing, ZFS user properties, JSON/table output, GitButler.

---

## Scope

This plan implements Wave 1 from `docs/superpowers/specs/2026-06-13-snapshot-recovery-lifecycle-design.md`.

Included:

- `yeet snapshots list [svc] [--format=table|json|json-pretty]`
- `yeet snapshots inspect <svc> <snapshot> [--format=table|json|json-pretty]`
- `yeet snapshots create <svc> [--comment=TEXT] [--full]`
- `yeet snapshots rm <svc> <snapshot> [--yes]`
- `yeet snapshots protect <svc> <snapshot>`
- `yeet snapshots unprotect <svc> <snapshot>`
- protected snapshots are skipped by retention pruning
- docs and live smoke checks

Not included:

- `yeet snapshots clone`
- `yeet snapshots restore`
- full Firecracker memory/state restore
- browser UI

## File Structure

- Modify `pkg/cli/cli.go`: add snapshot lifecycle flag/arg structs, parsers, registry entries, and remote flag specs.
- Modify `pkg/cli/cli_test.go`: parser and registry tests for the new commands.
- Modify `cmd/yeet/cli.go`: expose the new snapshots group commands to yargs.
- Modify `cmd/yeet/cli_test.go`: help/routing tests for the expanded snapshots group.
- Modify `pkg/yeet/snapshots_cmd.go`: client-side validation for all snapshots subcommands before forwarding to catch.
- Add `pkg/yeet/snapshots_cmd_test.go`: validation and forwarding tests for top-level snapshot lifecycle commands.
- Add `pkg/catch/recovery_points.go`: service target discovery, ZFS snapshot listing, selector resolution, protect/delete helpers, and create dispatch.
- Add `pkg/catch/recovery_points_render.go`: table/JSON output for list and inspect.
- Add `pkg/catch/recovery_points_test.go`: focused tests for recovery point parsing, selector resolution, listing, protection, and deletion.
- Modify `pkg/catch/service_snapshots.go`: add manual event support, richer snapshot metadata listing, and protected-retention behavior.
- Modify `pkg/catch/service_snapshots_test.go` or `pkg/catch/recovery_points_test.go`: retention tests for protected snapshots.
- Modify `pkg/catch/tty_ops.go`: route `snapshots list/inspect/create/rm/protect/unprotect`.
- Modify `pkg/catch/tty_ops_test.go`: command routing/render/prompt tests.
- Modify `website/docs/concepts/zfs.mdx`, `website/docs/payloads/vms.mdx`, and `website/docs/cli/yeet-cli.mdx`: document recovery point lifecycle.
- Modify root `website` submodule pointer after committing and pushing website docs.

## Task 1: CLI Schema and Client Routing

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `pkg/yeet/snapshots_cmd.go`
- Create: `pkg/yeet/snapshots_cmd_test.go`

- [ ] **Step 1: Write failing CLI parser and registry tests**

Add tests to `pkg/cli/cli_test.go`:

```go
func TestParseSnapshotsLifecycleCommands(t *testing.T) {
	listFlags, listArgs, err := ParseSnapshotsList([]string{"svc-a", "--format=json-pretty"})
	if err != nil {
		t.Fatalf("ParseSnapshotsList: %v", err)
	}
	if listFlags.Format != "json-pretty" || len(listArgs) != 1 || listArgs[0] != "svc-a" {
		t.Fatalf("list flags=%#v args=%#v", listFlags, listArgs)
	}

	inspectFlags, inspectArgs, err := ParseSnapshotsInspect([]string{"svc-a", "yeet-abc", "--format=json"})
	if err != nil {
		t.Fatalf("ParseSnapshotsInspect: %v", err)
	}
	if inspectFlags.Format != "json" || len(inspectArgs) != 2 || inspectArgs[0] != "svc-a" || inspectArgs[1] != "yeet-abc" {
		t.Fatalf("inspect flags=%#v args=%#v", inspectFlags, inspectArgs)
	}

	createFlags, createArgs, err := ParseSnapshotsCreate([]string{"devbox", "--comment", " before upgrade ", "--full"})
	if err != nil {
		t.Fatalf("ParseSnapshotsCreate: %v", err)
	}
	if createFlags.Comment != "before upgrade" || !createFlags.Full || len(createArgs) != 1 || createArgs[0] != "devbox" {
		t.Fatalf("create flags=%#v args=%#v", createFlags, createArgs)
	}

	rmFlags, rmArgs, err := ParseSnapshotsRemove([]string{"svc-a", "yeet-abc", "--yes"})
	if err != nil {
		t.Fatalf("ParseSnapshotsRemove: %v", err)
	}
	if !rmFlags.Yes || len(rmArgs) != 2 || rmArgs[1] != "yeet-abc" {
		t.Fatalf("rm flags=%#v args=%#v", rmFlags, rmArgs)
	}
}

func TestParseSnapshotsLifecycleRejectsBadInput(t *testing.T) {
	if _, _, err := ParseSnapshotsList([]string{"--format=yaml"}); err == nil || !strings.Contains(err.Error(), "--format must be table, json, or json-pretty") {
		t.Fatalf("ParseSnapshotsList error = %v, want format error", err)
	}
	if _, _, err := ParseSnapshotsInspect([]string{"svc-a"}); err == nil || !strings.Contains(err.Error(), "snapshots inspect requires service and snapshot") {
		t.Fatalf("ParseSnapshotsInspect error = %v, want arity error", err)
	}
	if _, _, err := ParseSnapshotsCreate([]string{}); err == nil || !strings.Contains(err.Error(), "snapshots create requires a service") {
		t.Fatalf("ParseSnapshotsCreate error = %v, want service error", err)
	}
	if _, _, err := ParseSnapshotsRemove([]string{"svc-a"}); err == nil || !strings.Contains(err.Error(), "snapshots rm requires service and snapshot") {
		t.Fatalf("ParseSnapshotsRemove error = %v, want arity error", err)
	}
}
```

Extend `TestRemoteCommandRegistryAndFlagSpecs` to assert `snapshots` group commands and flags:

```go
for _, cmd := range []string{"list", "inspect", "create", "rm", "protect", "unprotect", "defaults"} {
	if _, ok := reg.Groups["snapshots"].Commands[cmd]; !ok {
		t.Fatalf("snapshots %s command missing", cmd)
	}
	if _, ok := RemoteGroupFlagSpecs()["snapshots"][cmd]; !ok {
		t.Fatalf("snapshots %s flag spec missing", cmd)
	}
}
if !RemoteGroupFlagSpecs()["snapshots"]["list"]["--format"].ConsumesValue {
	t.Fatal("snapshots list --format should consume a value")
}
if !RemoteGroupFlagSpecs()["snapshots"]["inspect"]["--format"].ConsumesValue {
	t.Fatal("snapshots inspect --format should consume a value")
}
if !RemoteGroupFlagSpecs()["snapshots"]["create"]["--comment"].ConsumesValue {
	t.Fatal("snapshots create --comment should consume a value")
}
if RemoteGroupFlagSpecs()["snapshots"]["create"]["--full"].ConsumesValue {
	t.Fatal("snapshots create --full should not consume a value")
}
if RemoteGroupFlagSpecs()["snapshots"]["rm"]["--yes"].ConsumesValue {
	t.Fatal("snapshots rm --yes should not consume a value")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/cli -run 'TestParseSnapshotsLifecycle|TestRemoteCommandRegistryAndFlagSpecs' -count=1
```

Expected: fail because parser functions and registry entries do not exist yet.

- [ ] **Step 3: Add lifecycle flag/arg structs and parsers**

In `pkg/cli/cli.go`, add exported flag structs near the existing snapshot flags:

```go
type SnapshotsListFlags struct {
	Format string
}

type SnapshotsInspectFlags struct {
	Format string
}

type SnapshotsCreateFlags struct {
	Comment string
	Full    bool
}

type SnapshotsRemoveFlags struct {
	Yes bool
}
```

Add parsed structs near `snapshotDefaultsSetFlagsParsed`:

```go
type snapshotsListFlagsParsed struct {
	Format string `flag:"format" help:"Output format: table, json, json-pretty"`
	Output string `flag:"output" help:"Alias for --format"`
}

type snapshotsInspectFlagsParsed struct {
	Format string `flag:"format" help:"Output format: table, json, json-pretty"`
	Output string `flag:"output" help:"Alias for --format"`
}

type snapshotsCreateFlagsParsed struct {
	Comment string `flag:"comment" help:"Human note stored with the recovery point"`
	Full    bool   `flag:"full" help:"For VMs, also write Firecracker state and memory checkpoint files"`
}

type snapshotsRemoveFlagsParsed struct {
	Yes bool `flag:"yes" short:"y" help:"Skip the removal prompt"`
}
```

Add parser functions after `ParseSnapshotDefaultsSet`:

```go
func ParseSnapshotsList(args []string) (SnapshotsListFlags, []string, error) {
	parsed, err := parseFlags[snapshotsListFlagsParsed](args)
	if err != nil {
		return SnapshotsListFlags{}, nil, err
	}
	formatRaw := strings.TrimSpace(parsed.Flags.Format)
	if strings.TrimSpace(parsed.Flags.Output) != "" {
		formatRaw = strings.TrimSpace(parsed.Flags.Output)
	}
	format, err := normalizeOutputFormat("--format", formatRaw)
	if err != nil {
		return SnapshotsListFlags{}, nil, err
	}
	if len(parsed.Args) > 1 {
		return SnapshotsListFlags{}, nil, fmt.Errorf("snapshots list accepts at most one service")
	}
	return SnapshotsListFlags{Format: format}, parsed.Args, nil
}

func ParseSnapshotsInspect(args []string) (SnapshotsInspectFlags, []string, error) {
	parsed, err := parseFlags[snapshotsInspectFlagsParsed](args)
	if err != nil {
		return SnapshotsInspectFlags{}, nil, err
	}
	formatRaw := strings.TrimSpace(parsed.Flags.Format)
	if strings.TrimSpace(parsed.Flags.Output) != "" {
		formatRaw = strings.TrimSpace(parsed.Flags.Output)
	}
	format, err := normalizeOutputFormat("--format", formatRaw)
	if err != nil {
		return SnapshotsInspectFlags{}, nil, err
	}
	if len(parsed.Args) != 2 {
		return SnapshotsInspectFlags{}, nil, fmt.Errorf("snapshots inspect requires service and snapshot")
	}
	return SnapshotsInspectFlags{Format: format}, parsed.Args, nil
}

func ParseSnapshotsCreate(args []string) (SnapshotsCreateFlags, []string, error) {
	parsed, err := parseFlags[snapshotsCreateFlagsParsed](args)
	if err != nil {
		return SnapshotsCreateFlags{}, nil, err
	}
	if len(parsed.Args) != 1 {
		return SnapshotsCreateFlags{}, nil, fmt.Errorf("snapshots create requires a service")
	}
	return SnapshotsCreateFlags{Comment: strings.TrimSpace(parsed.Flags.Comment), Full: parsed.Flags.Full}, parsed.Args, nil
}

func ParseSnapshotsRemove(args []string) (SnapshotsRemoveFlags, []string, error) {
	parsed, err := parseFlags[snapshotsRemoveFlagsParsed](args)
	if err != nil {
		return SnapshotsRemoveFlags{}, nil, err
	}
	if len(parsed.Args) != 2 {
		return SnapshotsRemoveFlags{}, nil, fmt.Errorf("snapshots rm requires service and snapshot")
	}
	return SnapshotsRemoveFlags{Yes: parsed.Flags.Yes}, parsed.Args, nil
}

func ParseSnapshotsProtect(args []string, action string) ([]string, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("snapshots %s requires service and snapshot", action)
	}
	return args, nil
}
```

- [ ] **Step 4: Expand CLI registry and help**

In `remoteGroupInfos["snapshots"]`, change the description to `Manage service recovery points and snapshot defaults`, keep `defaults`, and add:

```go
"list": {
	Name:        "list",
	Description: "List yeet recovery points",
	Usage:       "snapshots list [svc] [--format=table|json|json-pretty]",
	Examples: []string{
		"yeet snapshots list",
		"yeet snapshots list <svc>",
		"yeet snapshots list <svc> --format=json",
	},
	FlagsSchema: snapshotsListFlagsParsed{},
},
"inspect": {
	Name:        "inspect",
	Description: "Inspect one recovery point",
	Usage:       "snapshots inspect <svc> <snapshot> [--format=table|json|json-pretty]",
	Examples: []string{
		"yeet snapshots inspect <svc> yeet-20260613T203100Z-vm-manual-g0",
		"yeet snapshots inspect <svc> yeet-20260613 --format=json",
	},
	FlagsSchema: snapshotsInspectFlagsParsed{},
},
"create": {
	Name:        "create",
	Description: "Create a manual recovery point",
	Usage:       "snapshots create <svc> [--comment=TEXT] [--full]",
	Examples: []string{
		"yeet snapshots create <svc>",
		"yeet snapshots create <svc> --comment=\"before upgrade\"",
		"yeet snapshots create <vm> --full --comment=\"checkpoint before risky change\"",
	},
	FlagsSchema: snapshotsCreateFlagsParsed{},
},
"rm": {
	Name:        "rm",
	Description: "Delete a yeet recovery point",
	Usage:       "snapshots rm <svc> <snapshot> [--yes]",
	Examples: []string{
		"yeet snapshots rm <svc> yeet-20260613T203100Z-vm-manual-g0",
	},
	FlagsSchema: snapshotsRemoveFlagsParsed{},
},
"protect": {
	Name:        "protect",
	Description: "Protect a recovery point from retention pruning",
	Usage:       "snapshots protect <svc> <snapshot>",
	Examples:    []string{"yeet snapshots protect <svc> yeet-20260613T203100Z-vm-manual-g0"},
},
"unprotect": {
	Name:        "unprotect",
	Description: "Allow retention pruning for a recovery point",
	Usage:       "snapshots unprotect <svc> <snapshot>",
	Examples:    []string{"yeet snapshots unprotect <svc> yeet-20260613T203100Z-vm-manual-g0"},
},
```

Update `remoteGroupFlagSpecs["snapshots"]`:

```go
"snapshots": {
	"list":      flagSpecsFromStruct(snapshotsListFlagsParsed{}),
	"inspect":   flagSpecsFromStruct(snapshotsInspectFlagsParsed{}),
	"create":    flagSpecsFromStruct(snapshotsCreateFlagsParsed{}),
	"rm":        flagSpecsFromStruct(snapshotsRemoveFlagsParsed{}),
	"protect":   {},
	"unprotect": {},
	"defaults":  flagSpecsFromStruct(snapshotDefaultsSetFlagsParsed{}),
},
```

In `cmd/yeet/cli.go`, expand `buildGroupHandlers()["snapshots"].Commands` with the new commands mapped to `handleSnapshotsGroup`.

- [ ] **Step 5: Expand client-side snapshots command validation**

In `pkg/yeet/snapshots_cmd.go`, replace the defaults-only branch with:

```go
func handleSvcSnapshots(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) == 0 {
		return fmt.Errorf("snapshots requires a command")
	}
	switch req.Command.Args[0] {
	case "defaults":
		return handleSnapshotDefaults(ctx, req)
	case "list":
		if _, _, err := cli.ParseSnapshotsList(req.Command.Args[1:]); err != nil {
			return err
		}
	case "inspect":
		if _, _, err := cli.ParseSnapshotsInspect(req.Command.Args[1:]); err != nil {
			return err
		}
	case "create":
		if _, _, err := cli.ParseSnapshotsCreate(req.Command.Args[1:]); err != nil {
			return err
		}
	case "rm":
		if _, _, err := cli.ParseSnapshotsRemove(req.Command.Args[1:]); err != nil {
			return err
		}
	case "protect", "unprotect":
		if _, err := cli.ParseSnapshotsProtect(req.Command.Args[1:], req.Command.Args[0]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown snapshots command %q", req.Command.Args[0])
	}
	return execRemoteFn(ctx, systemServiceName, req.Command.RawArgs, nil, false)
}
```

Move the existing defaults logic into `handleSnapshotDefaults`.

- [ ] **Step 6: Run focused CLI/client tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet -run 'TestParseSnapshotsLifecycle|TestRemoteCommandRegistryAndFlagSpecs|TestSnapshots' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit Task 1**

Run `but status -fv`, copy the current GitButler file IDs for only the Task 1 files listed above, then commit those IDs:

```bash
but status -fv
but commit codex/snapshot-recovery-wave1 -c -m "cli: add snapshot lifecycle commands" --changes pq,rs
```

In the commit command, replace `pq,rs` with the actual comma-separated IDs shown by the immediately preceding status output. Do not include files from later tasks.

## Task 2: Recovery Point Model and Snapshot Listing

**Files:**
- Create: `pkg/catch/recovery_points.go`
- Create: `pkg/catch/recovery_points_test.go`
- Modify: `pkg/catch/service_snapshots.go`

- [ ] **Step 1: Write failing recovery point tests**

Create `pkg/catch/recovery_points_test.go` with:

```go
package catch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRecoveryPointsListVMAndServiceRootSnapshots(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, svc *db.Service) error {
		svc.Name = "app"
		svc.ServiceType = db.ServiceTypeDockerCompose
		svc.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] != "list" {
			t.Fatalf("unexpected zfs args: %v", args)
		}
		switch args[len(args)-1] {
		case "flash/yeet/vms/devbox/vm/d-abc/root":
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tbefore upgrade\tdisk\ttrue\n", "", nil
		case "tank/apps/app":
			return "tank/apps/app@yeet-20260613T203200Z-manual-g3\t1781382720\tcatch\tapp\tmanual\t3\tbefore deploy\tservice-root\tfalse\n", "", nil
		default:
			return "", "", nil
		}
	}

	points, err := server.listRecoveryPoints(context.Background(), "")
	if err != nil {
		t.Fatalf("listRecoveryPoints: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %#v, want two recovery points", points)
	}
	if points[0].Service == "" || points[0].ShortName == "" || points[0].StorageKind == "" {
		t.Fatalf("point missing core fields: %#v", points[0])
	}
}

func TestResolveRecoveryPointSelector(t *testing.T) {
	points := []recoveryPoint{
		{Service: "devbox", Name: "tank/root@yeet-20260613T203100Z-vm-manual-g0", ShortName: "yeet-20260613T203100Z-vm-manual-g0"},
		{Service: "devbox", Name: "tank/root@yeet-20260613T203200Z-vm-manual-g0", ShortName: "yeet-20260613T203200Z-vm-manual-g0"},
	}
	got, err := resolveRecoveryPointSelector(points, "yeet-20260613T2031")
	if err != nil {
		t.Fatalf("resolveRecoveryPointSelector: %v", err)
	}
	if got.Name != points[0].Name {
		t.Fatalf("resolved = %#v, want first point", got)
	}
	if _, err := resolveRecoveryPointSelector(points, "yeet-20260613"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRecoveryPoints|TestResolveRecoveryPointSelector' -count=1
```

Expected: fail because `recoveryPoint`, `listRecoveryPoints`, and selector helpers do not exist.

- [ ] **Step 3: Add manual snapshot event and richer listed snapshot metadata**

In `pkg/catch/service_snapshots.go`, add:

```go
snapshotEventManual snapshotEvent = "manual"
```

Extend `listedSnapshot`:

```go
type listedSnapshot struct {
	Name       string
	Created    time.Time
	CreatedBy  string
	Service    string
	Event      string
	Generation int
	Comment    string
	Checkpoint string
	Protected  bool
}
```

Change `listServiceSnapshots` to request these properties:

```go
stdout, stderr, err := runner(ctx, "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,com.yeetrun:created-by,com.yeetrun:service,com.yeetrun:event,com.yeetrun:generation,com.yeetrun:comment,com.yeetrun:checkpoint,com.yeetrun:protected", "-s", "creation", dataset)
```

Update `parseListedSnapshots` to accept nine fields. Treat legacy rows with four fields from older tests by filling the new fields with zero values, so existing unit tests stay focused:

```go
fields := strings.Split(line, "\t")
if len(fields) != 4 && len(fields) != 9 {
	return nil, fmt.Errorf("invalid zfs snapshot row %q", line)
}
```

Parse generation with `strconv.Atoi` when present and not `-`. Parse protected as true only when the property value equals `true`.

- [ ] **Step 4: Add recovery point model and service target discovery**

Create `pkg/catch/recovery_points.go`:

```go
package catch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	recoveryStorageServiceRoot = "service-root-dataset"
	recoveryStorageVMZVOL      = "vm-zvol"
	recoveryModeServiceRoot    = "service-root"
	recoveryModeDisk           = "disk"
	recoveryModeFull           = "full"
)

type recoveryPoint struct {
	Service       string    `json:"service"`
	ServiceType   string    `json:"serviceType"`
	StorageKind   string    `json:"storageKind"`
	Dataset       string    `json:"dataset"`
	Name          string    `json:"name"`
	ShortName     string    `json:"shortName"`
	Created       time.Time `json:"created"`
	CreatedBy     string    `json:"createdBy"`
	Event         string    `json:"event"`
	Generation    int       `json:"generation"`
	Comment       string    `json:"comment,omitempty"`
	Mode          string    `json:"mode"`
	Protected     bool      `json:"protected"`
	StatePath     string    `json:"statePath,omitempty"`
	MemoryPath    string    `json:"memoryPath,omitempty"`
	Actions       []string  `json:"actions"`
	Retention     string    `json:"retention"`
}

type recoveryTarget struct {
	Service     *db.Service
	ServiceType string
	StorageKind string
	Dataset     string
}
```

Add `recoveryTargetForService`:

```go
func recoveryTargetForService(service *db.Service) (recoveryTarget, bool, error) {
	if service == nil {
		return recoveryTarget{}, false, nil
	}
	if service.ServiceType == db.ServiceTypeVM && service.VM != nil {
		dataset, err := vmSnapshotDataset(service.VM.Disk)
		if err != nil {
			return recoveryTarget{}, false, nil
		}
		return recoveryTarget{Service: service, ServiceType: string(db.ServiceTypeVM), StorageKind: recoveryStorageVMZVOL, Dataset: dataset}, true, nil
	}
	if strings.TrimSpace(service.ServiceRootZFS) != "" {
		return recoveryTarget{Service: service, ServiceType: string(service.ServiceType), StorageKind: recoveryStorageServiceRoot, Dataset: service.ServiceRootZFS}, true, nil
	}
	return recoveryTarget{}, false, nil
}
```

Add `listRecoveryPoints(ctx, serviceName)`:

```go
func (s *Server) listRecoveryPoints(ctx context.Context, serviceName string) ([]recoveryPoint, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	var services []*db.Service
	if strings.TrimSpace(serviceName) != "" {
		sv, ok := dv.Services().GetOk(serviceName)
		if !ok {
			return nil, fmt.Errorf("service %q not found", serviceName)
		}
		services = append(services, sv.AsStruct())
	} else {
		for _, sv := range dv.Services().All() {
			services = append(services, sv.AsStruct())
		}
		sort.SliceStable(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	}
	var points []recoveryPoint
	for _, service := range services {
		target, ok, err := recoveryTargetForService(service)
		if err != nil || !ok {
			continue
		}
		snaps, err := listServiceSnapshots(ctx, s.zfsRunner, target.Dataset)
		if err != nil {
			return nil, err
		}
		for _, snap := range snaps {
			if snap.CreatedBy != "catch" || snap.Service != service.Name || !strings.Contains(snap.Name, "@yeet-") {
				continue
			}
			points = append(points, recoveryPointFromSnapshot(target, snap))
		}
	}
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].Created.Equal(points[j].Created) {
			return points[i].Name < points[j].Name
		}
		return points[i].Created.After(points[j].Created)
	})
	return points, nil
}
```

- [ ] **Step 5: Add point conversion and selector resolution**

In `pkg/catch/recovery_points.go`:

```go
func recoveryPointFromSnapshot(target recoveryTarget, snap listedSnapshot) recoveryPoint {
	mode := zfsPropertyValue(snap.Checkpoint)
	if mode == "" {
		if target.StorageKind == recoveryStorageVMZVOL {
			mode = recoveryModeDisk
		} else {
			mode = recoveryModeServiceRoot
		}
	}
	point := recoveryPoint{
		Service:     target.Service.Name,
		ServiceType: target.ServiceType,
		StorageKind: target.StorageKind,
		Dataset:     target.Dataset,
		Name:        snap.Name,
		ShortName:   vmSnapshotShortName(snap.Name),
		Created:     snap.Created,
		CreatedBy:   snap.CreatedBy,
		Event:       snap.Event,
		Generation:  snap.Generation,
		Comment:     snap.Comment,
		Mode:        mode,
		Protected:   snap.Protected,
		Retention:   recoveryRetentionLabel(snap.Protected),
	}
	point.Actions = recoveryPointActions(point)
	return point
}

func recoveryRetentionLabel(protected bool) string {
	if protected {
		return "protected"
	}
	return "managed"
}

func recoveryPointActions(point recoveryPoint) []string {
	actions := []string{"inspect"}
	if !point.Protected {
		actions = append(actions, "protect", "rm")
	} else {
		actions = append(actions, "unprotect")
	}
	return actions
}
```

Add selector:

```go
func resolveRecoveryPointSelector(points []recoveryPoint, selector string) (recoveryPoint, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return recoveryPoint{}, fmt.Errorf("snapshot selector is required")
	}
	var matches []recoveryPoint
	for _, point := range points {
		switch {
		case point.Name == selector:
			return point, nil
		case point.ShortName == selector:
			return point, nil
		case strings.HasPrefix(point.ShortName, selector):
			matches = append(matches, point)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, point := range matches {
			names = append(names, point.ShortName)
		}
		return recoveryPoint{}, fmt.Errorf("ambiguous snapshot %q; matches: %s", selector, strings.Join(names, ", "))
	}
	return recoveryPoint{}, fmt.Errorf("snapshot %q not found", selector)
}
```

- [ ] **Step 6: Run focused recovery model tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRecoveryPoints|TestResolveRecoveryPointSelector|TestVMSnapshot' -count=1
```

Expected: pass after updating existing tests for richer ZFS list rows where needed.

- [ ] **Step 7: Commit Task 2**

Run `but status -fv`, copy the current GitButler file IDs for only the Task 2 files listed above, then commit those IDs:

```bash
but status -fv
but commit codex/snapshot-recovery-wave1 -m "catch: model snapshot recovery points" --changes pq,rs
```

In the commit command, replace `pq,rs` with the actual comma-separated IDs shown by the immediately preceding status output. Do not include files from later tasks.

## Task 3: List and Inspect Output

**Files:**
- Create: `pkg/catch/recovery_points_render.go`
- Modify: `pkg/catch/recovery_points_test.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_ops_test.go`

- [ ] **Step 1: Write failing render and command tests**

Add tests to `pkg/catch/recovery_points_test.go`:

```go
func TestRenderRecoveryPointsTableAndJSON(t *testing.T) {
	points := []recoveryPoint{{
		Service: "devbox", ServiceType: "vm", StorageKind: recoveryStorageVMZVOL,
		Name: "tank/root@yeet-20260613T203100Z-vm-manual-g0", ShortName: "yeet-20260613T203100Z-vm-manual-g0",
		Created: time.Unix(1781382660, 0).UTC(), Event: "vm-manual", Mode: "disk",
		Protected: true, Comment: "before upgrade",
	}}
	var table bytes.Buffer
	if err := renderRecoveryPoints(&table, "table", points); err != nil {
		t.Fatalf("render table: %v", err)
	}
	for _, want := range []string{"SERVICE", "devbox", "disk", "protected", "before upgrade"} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("table output missing %q:\n%s", want, table.String())
		}
	}
	var jsonOut bytes.Buffer
	if err := renderRecoveryPoints(&jsonOut, "json", points); err != nil {
		t.Fatalf("render json: %v", err)
	}
	if !strings.Contains(jsonOut.String(), `"service":"devbox"`) {
		t.Fatalf("json output = %s", jsonOut.String())
	}
}
```

Add command tests to `pkg/catch/tty_ops_test.go`:

```go
func TestSnapshotsListCommandRendersRecoveryPoints(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tbefore upgrade\tdisk\tfalse\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"list", "devbox"}); err != nil {
		t.Fatalf("snapshots list: %v", err)
	}
	if !strings.Contains(out.String(), "devbox") || !strings.Contains(out.String(), "before upgrade") {
		t.Fatalf("output = %q, want recovery point row", out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRenderRecoveryPoints|TestSnapshotsListCommand' -count=1
```

Expected: fail because rendering and command routing are not implemented.

- [ ] **Step 3: Add render helpers**

Create `pkg/catch/recovery_points_render.go`:

```go
package catch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

func renderRecoveryPoints(w io.Writer, formatOut string, points []recoveryPoint) error {
	switch formatOut {
	case "json":
		return json.NewEncoder(w).Encode(points)
	case "json-pretty":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(points)
	default:
		return renderRecoveryPointsTable(w, points)
	}
}

func renderRecoveryPointsTable(w io.Writer, points []recoveryPoint) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SERVICE\tTYPE\tCREATED\tMODE\tEVENT\tRETENTION\tCOMMENT"); err != nil {
		return err
	}
	for _, point := range points {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			point.Service,
			point.ServiceType,
			formatRecoveryPointCreated(point.Created),
			point.Mode,
			point.Event,
			point.Retention,
			strings.TrimSpace(point.Comment),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func formatRecoveryPointCreated(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
```

Add `renderRecoveryPointInspect(w, formatOut, point)` in the same file. Table/plain output should be key/value lines:

```go
func renderRecoveryPointInspect(w io.Writer, formatOut string, point recoveryPoint) error {
	switch formatOut {
	case "json":
		return json.NewEncoder(w).Encode(point)
	case "json-pretty":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(point)
	default:
		writef(w, "Service: %s\n", point.Service)
		writef(w, "Type: %s\n", point.ServiceType)
		writef(w, "Snapshot: %s\n", point.Name)
		writef(w, "Short name: %s\n", point.ShortName)
		writef(w, "Created: %s\n", formatRecoveryPointCreated(point.Created))
		writef(w, "Mode: %s\n", point.Mode)
		writef(w, "Event: %s\n", point.Event)
		writef(w, "Retention: %s\n", point.Retention)
		if point.Comment != "" {
			writef(w, "Comment: %s\n", point.Comment)
		}
		if point.StatePath != "" {
			writef(w, "Firecracker state: %s\n", point.StatePath)
			writef(w, "Firecracker memory: %s\n", point.MemoryPath)
		}
		writef(w, "Actions: %s\n", strings.Join(point.Actions, ", "))
		return nil
	}
}
```

- [ ] **Step 4: Route list and inspect on catch**

In `pkg/catch/tty_ops.go`, update `snapshotsCmdFunc`:

```go
switch args[0] {
case "defaults":
	return e.snapshotsDefaultsCmdFunc(args)
case "list":
	flags, rest, err := cli.ParseSnapshotsList(args[1:])
	if err != nil {
		return err
	}
	service := ""
	if len(rest) == 1 {
		service = rest[0]
	}
	points, err := e.s.listRecoveryPoints(e.ctx, service)
	if err != nil {
		return err
	}
	return renderRecoveryPoints(e.rw, flags.Format, points)
case "inspect":
	flags, rest, err := cli.ParseSnapshotsInspect(args[1:])
	if err != nil {
		return err
	}
	points, err := e.s.listRecoveryPoints(e.ctx, rest[0])
	if err != nil {
		return err
	}
	point, err := resolveRecoveryPointSelector(points, rest[1])
	if err != nil {
		return err
	}
	return renderRecoveryPointInspect(e.rw, flags.Format, point)
...
}
```

Move the existing defaults logic into `snapshotsDefaultsCmdFunc(args []string)`.

- [ ] **Step 5: Run focused list/inspect tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRenderRecoveryPoints|TestSnapshotsListCommand|TestSnapshotsInspect' -count=1
```

Expected: pass after adding any missing inspect test alongside list.

- [ ] **Step 6: Commit Task 3**

Run `but status -fv`, copy the current GitButler file IDs for only the Task 3 files listed above, then commit those IDs:

```bash
but status -fv
but commit codex/snapshot-recovery-wave1 -m "catch: list and inspect recovery points" --changes pq,rs
```

In the commit command, replace `pq,rs` with the actual comma-separated IDs shown by the immediately preceding status output. Do not include files from later tasks.

## Task 4: Create, Protect, Unprotect, Remove, and Protected Retention

**Files:**
- Modify: `pkg/catch/recovery_points.go`
- Modify: `pkg/catch/service_snapshots.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/recovery_points_test.go`
- Modify: `pkg/catch/tty_ops_test.go`

- [ ] **Step 1: Write failing lifecycle action tests**

Add tests:

```go
func TestSnapshotsCreateServiceRootManualSnapshot(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, svc *db.Service) error {
		svc.Name = "app"
		svc.ServiceType = db.ServiceTypeDockerCompose
		svc.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"create", "app", "--comment=manual note"}); err != nil {
		t.Fatalf("snapshots create: %v", err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "snapshot ") || !strings.Contains(joined, "com.yeetrun:event=manual") || !strings.Contains(joined, "com.yeetrun:comment=manual note") {
		t.Fatalf("zfs calls = %#v, want manual service-root snapshot", calls)
	}
	if !strings.Contains(out.String(), "Recovery point: tank/apps/app@yeet-") {
		t.Fatalf("output = %q, want recovery point", out.String())
	}
}

func TestSnapshotsProtectSkipsRetentionPrune(t *testing.T) {
	now := time.Unix(1781383000, 0).UTC()
	snaps := []listedSnapshot{
		{Name: "tank/app@yeet-old", Created: now.Add(-48 * time.Hour), CreatedBy: "catch", Service: "app", Protected: true},
		{Name: "tank/app@yeet-new", Created: now, CreatedBy: "catch", Service: "app"},
	}
	policy := effectivePolicy{Enabled: true, KeepLast: 1, MaxAge: 24 * time.Hour}
	prune := snapshotsToPrune(snaps, "app", policy, now, "")
	if len(prune) != 0 {
		t.Fatalf("prune = %#v, want protected old snapshot skipped", prune)
	}
}
```

Add delete/protect command tests:

```go
func TestSnapshotsProtectAndRemoveCommands(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tnote\tdisk\tfalse\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"protect", "devbox", "yeet-20260613T203100Z"}); err != nil {
		t.Fatalf("snapshots protect: %v", err)
	}
	if err := execer.snapshotsCmdFunc([]string{"rm", "devbox", "yeet-20260613T203100Z", "--yes"}); err != nil {
		t.Fatalf("snapshots rm: %v", err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "set com.yeetrun:protected=true") || !strings.Contains(joined, "destroy flash/yeet/vms") {
		t.Fatalf("calls = %#v, want protect and destroy", calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestSnapshotsCreate|TestSnapshotsProtect|TestSnapshotsProtectAndRemove' -count=1
```

Expected: fail because action commands and protected retention do not exist.

- [ ] **Step 3: Implement manual create dispatch**

In `pkg/catch/recovery_points.go`, add:

```go
func (s *Server) createRecoveryPoint(ctx context.Context, serviceName string, flags cli.SnapshotsCreateFlags, w io.Writer) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	sv, ok := dv.Services().GetOk(serviceName)
	if !ok {
		return fmt.Errorf("service %q not found", serviceName)
	}
	service := sv.AsStruct()
	if service.ServiceType == db.ServiceTypeVM {
		return s.createVMSnapshot(ctx, serviceName, cli.VMSnapshotFlags{Comment: flags.Comment, Full: flags.Full}, w)
	}
	if flags.Full {
		return fmt.Errorf("--full is only supported for VM recovery points")
	}
	target, ok, _ := recoveryTargetForService(service)
	if !ok || target.StorageKind != recoveryStorageServiceRoot {
		return fmt.Errorf("service %q does not have a ZFS-backed service root", serviceName)
	}
	policy, err := s.serviceSnapshotPolicy(service)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		return fmt.Errorf("snapshots are disabled for %q; enable snapshots for the service or inherit enabled defaults", serviceName)
	}
	name, err := createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service:    service.Name,
		Dataset:    target.Dataset,
		Event:      snapshotEventManual,
		Generation: service.Generation,
		Now:        time.Now(),
		Comment:    flags.Comment,
		Checkpoint: recoveryModeServiceRoot,
	})
	if err != nil {
		return err
	}
	if _, err := s.pruneServiceSnapshotsForDataset(ctx, target.Dataset, service, policy, time.Now(), name); err != nil {
		writeSnapshotWarning(w, "warning: failed to prune snapshots for %q: %v\n", serviceName, err)
	}
	writef(w, "Recovery point: %s\n", name)
	return nil
}
```

Import `io` and `github.com/yeetrun/yeet/pkg/cli` in `recovery_points.go`.

- [ ] **Step 4: Implement protect/unprotect/remove helpers**

In `pkg/catch/recovery_points.go`:

```go
func (s *Server) setRecoveryPointProtected(ctx context.Context, serviceName, selector string, protected bool, w io.Writer) error {
	point, err := s.resolveRecoveryPoint(ctx, serviceName, selector)
	if err != nil {
		return err
	}
	value := "false"
	if protected {
		value = "true"
	}
	if err := setSnapshotProperty(ctx, s.zfsRunner, point.Name, "com.yeetrun:protected", value); err != nil {
		return err
	}
	if protected {
		writef(w, "Protected recovery point: %s\n", point.Name)
	} else {
		writef(w, "Unprotected recovery point: %s\n", point.Name)
	}
	return nil
}

func (s *Server) removeRecoveryPoint(ctx context.Context, serviceName, selector string, yes bool, rw io.ReadWriter) error {
	point, err := s.resolveRecoveryPoint(ctx, serviceName, selector)
	if err != nil {
		return err
	}
	if point.Protected {
		return fmt.Errorf("recovery point %s is protected; unprotect it before removing", point.ShortName)
	}
	if !yes {
		ok, err := cmdutil.Confirm(rw, rw, fmt.Sprintf("Remove recovery point %s for %q?", point.ShortName, serviceName))
		if err != nil {
			return err
		}
		if !ok {
			writef(rw, "Skipped removing recovery point %s\n", point.ShortName)
			return nil
		}
	}
	if err := destroySnapshot(ctx, s.zfsRunner, point.Name); err != nil {
		return err
	}
	if point.StorageKind == recoveryStorageVMZVOL && point.Mode == recoveryModeFull {
		if service, err := s.recoveryService(serviceName); err == nil {
			_ = s.pruneVMCheckpointDirsForSnapshots(service, []string{point.Name})
		}
	}
	writef(rw, "Removed recovery point: %s\n", point.Name)
	return nil
}
```

Add helpers:

```go
func (s *Server) resolveRecoveryPoint(ctx context.Context, serviceName, selector string) (recoveryPoint, error) {
	points, err := s.listRecoveryPoints(ctx, serviceName)
	if err != nil {
		return recoveryPoint{}, err
	}
	return resolveRecoveryPointSelector(points, selector)
}

func setSnapshotProperty(ctx context.Context, runner zfsCommandRunner, snapshot, property, value string) error {
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "set", property+"="+value, snapshot)
	if err != nil {
		return formatZFSCommandError("zfs set "+property+" "+snapshot, stderr, err)
	}
	return nil
}

func (s *Server) recoveryService(serviceName string) (*db.Service, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	sv, ok := dv.Services().GetOk(serviceName)
	if !ok {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}
	return sv.AsStruct(), nil
}
```

Import `github.com/yeetrun/yeet/pkg/cmdutil` in `recovery_points.go`.

- [ ] **Step 5: Make retention skip protected snapshots**

In `snapshotsToPrune`, skip protected snapshots before age/keep checks:

```go
if snap.Protected {
	continue
}
```

This must happen after current-snapshot protection and before `shouldPruneSnapshot`.

- [ ] **Step 6: Route lifecycle actions in `snapshotsCmdFunc`**

In `pkg/catch/tty_ops.go`, add cases:

```go
case "create":
	flags, rest, err := cli.ParseSnapshotsCreate(args[1:])
	if err != nil {
		return err
	}
	return e.s.createRecoveryPoint(e.ctx, rest[0], flags, e.rw)
case "rm":
	flags, rest, err := cli.ParseSnapshotsRemove(args[1:])
	if err != nil {
		return err
	}
	return e.s.removeRecoveryPoint(e.ctx, rest[0], rest[1], flags.Yes, e.rw)
case "protect":
	rest, err := cli.ParseSnapshotsProtect(args[1:], "protect")
	if err != nil {
		return err
	}
	return e.s.setRecoveryPointProtected(e.ctx, rest[0], rest[1], true, e.rw)
case "unprotect":
	rest, err := cli.ParseSnapshotsProtect(args[1:], "unprotect")
	if err != nil {
		return err
	}
	return e.s.setRecoveryPointProtected(e.ctx, rest[0], rest[1], false, e.rw)
```

- [ ] **Step 7: Run focused action tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestSnapshotsCreate|TestSnapshotsProtect|TestSnapshotsProtectAndRemove|TestVMSnapshot' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit Task 4**

Run `but status -fv`, copy the current GitButler file IDs for only the Task 4 files listed above, then commit those IDs:

```bash
but status -fv
but commit codex/snapshot-recovery-wave1 -m "catch: manage recovery point lifecycle" --changes pq,rs
```

In the commit command, replace `pq,rs` with the actual comma-separated IDs shown by the immediately preceding status output. Do not include files from later tasks.

## Task 5: Docs

**Files:**
- Modify: `website/docs/concepts/zfs.mdx`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: root `website` submodule pointer after website commit is pushed

- [ ] **Step 1: Update user docs inside the website submodule**

In `website/docs/concepts/zfs.mdx`, add a "Recovery points" section that explains:

```md
## Recovery points

Yeet-created ZFS snapshots are recovery points. Use `yeet snapshots list` to
see them, `yeet snapshots inspect <svc> <snapshot>` to see exactly what a
recovery point contains, and `yeet snapshots protect <svc> <snapshot>` to keep
important points out of retention pruning.

Manual recovery points:

```bash
yeet snapshots create <svc> --comment "before upgrade"
yeet snapshots list <svc>
yeet snapshots inspect <svc> yeet-20260613
yeet snapshots protect <svc> yeet-20260613
yeet snapshots rm <svc> yeet-20260613 --yes
```
```

In `website/docs/payloads/vms.mdx`, update "VM snapshots" to point users at the top-level lifecycle:

```md
`yeet vm snapshot <vm>` remains a VM-focused shortcut for
`yeet snapshots create <vm>`. Use `yeet snapshots list <vm>` and
`yeet snapshots inspect <vm> <snapshot>` to find and inspect VM disk recovery
points.
```

In `website/docs/cli/yeet-cli.mdx`, add/refresh entries for the lifecycle commands.

- [ ] **Step 2: Run website checks**

Run:

```bash
git -C website diff --check
npm --prefix website run test:public-docs
```

Expected: both pass.

- [ ] **Step 3: Commit and push website docs**

Because `but` from `website/` resolves to the parent workspace in this repo, use the documented submodule exception:

```bash
git -C website status --short --branch
git -C website add docs/concepts/zfs.mdx docs/payloads/vms.mdx docs/cli/yeet-cli.mdx
git -C website commit -m "docs: document snapshot recovery points"
git -C website push origin main
git -C website status --short --branch
```

Expected: website `main...origin/main` is clean.

- [ ] **Step 4: Commit Task 5 root pointer**

Back in root:

```bash
git diff --submodule=log -- website
but status -fv
```

First try GitButler for the root gitlink:

```bash
but commit codex/snapshot-recovery-wave1 -m "docs: update snapshot recovery manual" --changes xy
```

In the commit command, replace `xy` with the actual GitButler file ID shown for the root `website` gitlink by the immediately preceding `but status -fv`. If GitButler repeats the known `Created commit unknown` / `Some selected changes could not be committed` behavior for the gitlink, leave the pointer for the documented finish-to-main submodule exception and do not loop.

## Task 6: Verification and Live Smoke

**Files:**
- No source edits expected

- [ ] **Step 1: Run local focused tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -run 'TestParseSnapshots|TestRemoteCommandRegistryAndFlagSpecs|TestSnapshots|TestRecoveryPoints|TestVMSnapshot' -count=1
```

Expected: pass.

- [ ] **Step 2: Run full local gates**

Run:

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
```

Expected: both pass.

- [ ] **Step 3: Live smoke on `yeet-lab`**

Build a temp binary and run from a temp directory:

```bash
tmpbin=$(mktemp /tmp/yeet-snapshots-wave1.XXXXXX)
mise exec -- go build -o "$tmpbin" ./cmd/yeet
tmpdir=$(mktemp -d /tmp/yeet-snapshots-wave1.XXXXXX)
svc=codex-snap-wave1-0613
cd "$tmpdir"
CATCH_HOST=yeet-lab "$tmpbin" --progress=plain run "$svc" vm://ubuntu/26.04 --net=svc --service-root="flash/yeet/vms/$svc" --zfs --snapshots=on --snapshot-keep-last=2 --snapshot-max-age=24h --image-policy=cached --cpus=1 --memory=1g --disk=8g
CATCH_HOST=yeet-lab "$tmpbin" --progress=plain snapshots create "$svc" --comment "wave1 smoke"
CATCH_HOST=yeet-lab "$tmpbin" snapshots list "$svc"
CATCH_HOST=yeet-lab "$tmpbin" snapshots inspect "$svc" yeet-
CATCH_HOST=yeet-lab "$tmpbin" snapshots protect "$svc" yeet-
CATCH_HOST=yeet-lab "$tmpbin" snapshots list "$svc"
CATCH_HOST=yeet-lab "$tmpbin" snapshots unprotect "$svc" yeet-
CATCH_HOST=yeet-lab "$tmpbin" snapshots rm "$svc" yeet- --yes
CATCH_HOST=yeet-lab "$tmpbin" remove "$svc" --yes --clean-data --clean-config
```

Expected:

- list shows the VM recovery point with comment
- inspect shows disk mode and zvol snapshot name
- protect flips retention/protected status
- rm deletes the recovery point
- cleanup removes the disposable VM and ZFS data

- [ ] **Step 4: Verify cleanup**

Run:

```bash
ssh root@lab-host 'bash -lc '\''zfs list -H -t filesystem,volume,snapshot -o name | grep "flash/yeet/vms/codex-snap-wave1-0613" || true'\'''
rm -rf "$tmpdir" "$tmpbin"
```

Expected: no ZFS rows for the disposable service.

- [ ] **Step 5: Commit verification-only doc adjustments if needed**

If live testing exposes docs wording that needs correction, update website docs and repeat Task 5. If no files changed, do not create a commit.

## Task 7: Finish and Publish

**Files:**
- Root repo and website repo refs only

- [ ] **Step 1: Verify branch state**

Run:

```bash
but status -fv
git status --short --branch
git -C website status --short --branch
but pull --check
```

Expected:

- no unassigned root changes except possibly the known `website` gitlink
- website clean and pushed
- `but pull --check` reports no upstream commits

- [ ] **Step 2: Land on main with the repo workflow**

If all branch changes are ordinary GitButler commits and no submodule gitlink is pending, follow the normal finish-to-main workflow.

If the root `website` gitlink is pending and GitButler cannot commit it, use the documented narrow exception:

1. Confirm website commit is pushed with `git -C website rev-parse main origin/main`.
2. Build a final root commit/tree that includes only the session branch plus the pushed `website` pointer.
3. Update local `main` with an expected-base guard.
4. Push `main` to `origin/main`.

- [ ] **Step 3: Clean GitButler session branch**

After `origin/main` contains the final commit:

```bash
but pull
but status -fv
```

If the applied session branch conflicts because its checkpoint commits duplicate the squash commit on `main`, verify raw `git status --short --branch` is clean and then delete only this session's branch:

```bash
printf 'y\n' | but branch delete codex/snapshot-recovery-wave1
but pull
but status -fv
```

- [ ] **Step 4: Final verification**

Run:

```bash
git rev-parse main origin/main
git ls-remote origin refs/heads/main
git -C website rev-parse main origin/main
git -C website ls-remote origin refs/heads/main
but status -fv
```

Expected:

- root `main`, `origin/main`, and remote `refs/heads/main` match
- website `main`, `origin/main`, and remote `refs/heads/main` match
- GitButler shows no unassigned changes and no active session branch
