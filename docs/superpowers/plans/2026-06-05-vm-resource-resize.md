# VM Resource Resize Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow existing yeet VMs to change CPU, memory, grow disk, and replace networking through `yeet service set`, while also improving the default VM SSH shell experience.

**Architecture:** Extend the existing `service set` command with VM-only flags, then add a stopped-only catch-side VM mutation path that reuses existing VM shape parsing, disk command runners, network plans, metadata rendering, and Firecracker rendering. VM metadata injection also writes an idempotent yeet-managed Bash defaults block, and client-side config sync rewrites stored VM run args after the remote mutation succeeds.

**Tech Stack:** Go, yargs CLI metadata, catch TTY command execution, Firecracker JSON, Linux `qemu-img`/`e2fsck`/`resize2fs`/`zfs`, existing yeet project config, README and website docs.

---

## Scope

This plan implements stopped-only post-create VM changes. It does not add
hotplug, auto-restart, disk shrink, backend migration, image replacement, or a
new `yeet vm resize` alias.

Run Go and git hooks with `mise exec -- ...` in this workspace. The ambient
shell currently resolves the Nix Go binary while `GOROOT` points at the mise Go
install, which breaks hook compilation.

## File Structure

- Modify `pkg/cli/cli.go`: add VM flags to `ServiceSetFlags`, parser structs,
  change detection, remote group flag specs, help usage, and examples.
- Modify `pkg/cli/cli_test.go`: cover VM service-set parsing, help metadata,
  and flag-spec consumption.
- Modify `cmd/yeet/cli_bridge_test.go`: cover bridge handling for service-set
  VM flags before and after the service argument.
- Create `pkg/catch/vm_resize.go`: VM setting change model, stopped-service
  validation, disk resize command plans, network reconstruction, Firecracker
  config rendering, metadata rewrite orchestration, and DB commit helpers.
- Create `pkg/catch/vm_resize_test.go`: focused catch-side tests for VM-only
  validation, stopped-only enforcement, shape updates, disk growth, network
  replacement, and artifact writes.
- Modify `pkg/catch/tty_service_set.go`: include VM settings in change
  detection and dispatch VM mutations before snapshot-only completion.
- Modify `pkg/catch/vm_storage.go`: add reusable raw and ZVOL disk growth step
  planners if keeping them beside existing disk provisioning commands is
  clearer than defining them only in `vm_resize.go`.
- Modify `pkg/catch/vm_storage_test.go`: cover grow-only raw and ZVOL disk
  resize command generation.
- Modify `pkg/yeet/svc_cmd.go`: rewrite stored VM run args for successful
  VM-specific `service set` flags.
- Modify `pkg/yeet/svc_cmd_branch_test.go`: cover local config updates for VM
  resource flags and payload-arg preservation.
- Modify `pkg/catch/vm_metadata.go`: inject idempotent yeet-managed `.profile`
  and `.bashrc` blocks for the VM login user.
- Modify `pkg/catch/vm_metadata_test.go`: cover shell defaults, welcome text,
  and idempotent managed-block replacement.
- Modify `cmd/yeet/cli_test.go`: update service-set help text expectations.
- Modify `README.md` and website docs under `website/docs`: document VM
  resource resize workflow, stopped-only behavior, grow-only disk, ZFS disk
  support, network replacement, and the default SSH shell experience.

## Task 1: CLI Parser, Help, And Bridge

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`

- [ ] **Step 1: Write failing parser tests**

In `pkg/cli/cli_test.go`, extend `TestParseServiceSetFlags` with:

```go
{
	name: "vm shape flags",
	args: []string{"svc-a", "--cpus=8", "--memory", "8g", "--disk=128g"},
	want: ServiceSetFlags{CPUs: 8, Memory: "8g", Disk: "128g"},
	wantOut: []string{"svc-a"},
},
{
	name: "vm network flags",
	args: []string{"--net", "svc,lan", "--macvlan-parent=vmbr0", "--macvlan-vlan=42", "--macvlan-mac=02:00:00:00:00:42", "svc-a"},
	want: ServiceSetFlags{
		Net: "svc,lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanVlan: 42,
		MacvlanMac: "02:00:00:00:00:42",
	},
	wantOut: []string{"svc-a"},
},
{name: "negative cpus", args: []string{"svc-a", "--cpus=-1"}, wantErr: "VM CPU count must be positive"},
```

Add a no-change case that confirms a service name alone still fails, but the
message now includes VM settings:

```go
{name: "missing change", args: []string{"svc-a"}, wantErr: "service set requires settings to change"}
```

- [ ] **Step 2: Write failing bridge tests**

In `cmd/yeet/cli_bridge_test.go`, add cases to `TestBridgeServiceArgsServiceSet`:

```go
{
	name:        "vm shape flags after service",
	args:        []string{"service", "set", "devbox", "--cpus=8", "--memory", "8g", "--disk=128g"},
	wantService: "devbox",
	wantBridged: "service set --cpus=8 --memory 8g --disk=128g",
	wantOK:      true,
},
{
	name:        "vm net flags before service",
	args:        []string{"service", "set", "--net", "lan", "--macvlan-parent=vmbr0", "devbox"},
	wantService: "devbox",
	wantBridged: "service set --net lan --macvlan-parent=vmbr0",
	wantOK:      true,
},
```

In `cmd/yeet/cli_test.go`, update the service-set help assertion so the usage
contains `--cpus=N`, `--memory=SIZE`, `--disk=SIZE`, and `--net=svc|lan`.

- [ ] **Step 3: Run tests to verify failure**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseServiceSetFlags|TestRemoteRegistry|TestRemoteGroupFlagSpecs|TestBridgeServiceArgsServiceSet|TestRunServiceSetHelpShowsLeafCommand' -count=1
```

Expected before implementation: FAIL because the new flags are unknown and help
text is unchanged.

- [ ] **Step 4: Implement parser and metadata changes**

In `pkg/cli/cli.go`, extend `ServiceSetFlags`:

```go
type ServiceSetFlags struct {
	ServiceRoot      string
	ZFS              bool
	Copy             bool
	Empty            bool
	Publish          []string
	PublishReset     bool
	CPUs             int
	Memory           string
	Disk             string
	Net              string
	NetworkChange    bool
	MacvlanMac       string
	MacvlanVlan      int
	MacvlanParent    string
	Snapshots        string
	SnapshotKeepLast string
	SnapshotMaxAge   string
	SnapshotRequired string
	SnapshotEvents   string
	SnapshotChange   bool
}
```

Extend `serviceSetFlagsParsed`:

```go
type serviceSetFlagsParsed struct {
	ServiceRoot      string   `flag:"service-root"`
	ZFS              bool     `flag:"zfs"`
	Copy             bool     `flag:"copy"`
	Empty            bool     `flag:"empty"`
	Publish          []string `flag:"publish" short:"p"`
	PublishReset     bool     `flag:"publish-reset"`
	CPUs             int      `flag:"cpus"`
	Memory           string   `flag:"memory"`
	Disk             string   `flag:"disk"`
	Net              string   `flag:"net"`
	MacvlanMac       string   `flag:"macvlan-mac"`
	MacvlanVlan      int      `flag:"macvlan-vlan"`
	MacvlanParent    string   `flag:"macvlan-parent"`
	Snapshots        string   `flag:"snapshots"`
	SnapshotKeepLast string   `flag:"snapshot-keep-last"`
	SnapshotMaxAge   string   `flag:"snapshot-max-age"`
	SnapshotRequired string   `flag:"snapshot-required"`
	SnapshotEvents   string   `flag:"snapshot-events"`
}
```

In `serviceSetFlagsFromParsed`, populate the new fields:

```go
flags := ServiceSetFlags{
	ServiceRoot:      strings.TrimSpace(parsed.ServiceRoot),
	ZFS:              parsed.ZFS,
	Copy:             parsed.Copy,
	Empty:            parsed.Empty,
	Publish:          orderedFlagValues(parseArgs, "--publish", "-p"),
	PublishReset:     parsed.PublishReset,
	CPUs:             parsed.CPUs,
	Memory:           strings.TrimSpace(parsed.Memory),
	Disk:             strings.TrimSpace(parsed.Disk),
	Net:              strings.TrimSpace(parsed.Net),
	NetworkChange:    hasNamedFlag(parseArgs, "--net"),
	MacvlanMac:       strings.TrimSpace(parsed.MacvlanMac),
	MacvlanVlan:      parsed.MacvlanVlan,
	MacvlanParent:    strings.TrimSpace(parsed.MacvlanParent),
	Snapshots:        snapshotMode,
	SnapshotKeepLast: strings.TrimSpace(parsed.SnapshotKeepLast),
	SnapshotMaxAge:   strings.TrimSpace(parsed.SnapshotMaxAge),
	SnapshotRequired: strings.TrimSpace(parsed.SnapshotRequired),
	SnapshotEvents:   strings.TrimSpace(parsed.SnapshotEvents),
	SnapshotChange:   hasAnySnapshotServiceSetFlag(parsed),
}
```

Add helpers:

```go
func hasServiceSetVMChange(flags ServiceSetFlags) bool {
	return flags.CPUs != 0 ||
		strings.TrimSpace(flags.Memory) != "" ||
		strings.TrimSpace(flags.Disk) != "" ||
		flags.NetworkChange ||
		strings.TrimSpace(flags.MacvlanMac) != "" ||
		flags.MacvlanVlan != 0 ||
		strings.TrimSpace(flags.MacvlanParent) != ""
}

func hasNamedFlag(args []string, name string) bool {
	for _, arg := range args {
		flagName, _ := splitInlineFlagValue(arg)
		if flagName == name {
			return true
		}
	}
	return false
}
```

Update validation:

```go
func validateServiceSetVMFlags(flags ServiceSetFlags) error {
	if flags.CPUs < 0 {
		return fmt.Errorf("VM CPU count must be positive")
	}
	if flags.MacvlanVlan < 0 {
		return fmt.Errorf("--macvlan-vlan must not be negative")
	}
	return nil
}

func serviceSetHasChange(flags ServiceSetFlags, rootChange bool) bool {
	return rootChange || flags.SnapshotChange || hasServiceSetPublishChange(flags) || hasServiceSetVMChange(flags)
}
```

Call `validateServiceSetVMFlags` from `validateServiceSetFlags`. Update the
missing-change error to:

```go
return fmt.Errorf("service set requires settings to change")
```

Update `remoteGroupInfos["service"].Commands["set"]` usage and examples to
include VM resource examples.

- [ ] **Step 5: Verify parser and bridge tests pass**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseServiceSetFlags|TestRemoteRegistry|TestRemoteGroupFlagSpecs|TestBridgeServiceArgsServiceSet|TestRunServiceSetHelpShowsLeafCommand' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 1**

Run:

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go
mise exec -- git commit -m "cli: parse VM service settings"
```

## Task 2: Disk Resize Command Planning

**Files:**
- Modify: `pkg/catch/vm_storage.go`
- Modify: `pkg/catch/vm_storage_test.go`

- [ ] **Step 1: Write failing disk resize tests**

In `pkg/catch/vm_storage_test.go`, add:

```go
func TestRawVMDiskResizeStepsGrowFilesystem(t *testing.T) {
	steps, err := rawVMDiskResizeSteps("/srv/devbox/data/rootfs.raw", 16<<30, 32<<30)
	if err != nil {
		t.Fatalf("rawVMDiskResizeSteps: %v", err)
	}
	want := []vmDiskPlanStep{
		{Phase: vmDiskPhaseRawResize, Command: []string{"qemu-img", "resize", "/srv/devbox/data/rootfs.raw", "34359738368"}},
		{Phase: vmDiskPhaseRawResize, Command: []string{"e2fsck", "-pf", "/srv/devbox/data/rootfs.raw"}},
		{Phase: vmDiskPhaseRawResize, Command: []string{"resize2fs", "/srv/devbox/data/rootfs.raw"}},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestZVOLVMDiskResizeStepsGrowFilesystem(t *testing.T) {
	steps, err := zvolVMDiskResizeSteps("flash/yeet/vms/devbox/vm/d-abc/root", 16<<30, 32<<30)
	if err != nil {
		t.Fatalf("zvolVMDiskResizeSteps: %v", err)
	}
	want := []vmDiskPlanStep{
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"zfs", "set", "volsize=34359738368", "flash/yeet/vms/devbox/vm/d-abc/root"}},
		{Phase: vmDiskPhaseZVOLResize, Command: vmZVOLSettleCommand()},
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"e2fsck", "-pf", "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root"}},
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"resize2fs", "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root"}},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestVMDiskResizeStepsRejectShrinkAndNoop(t *testing.T) {
	if _, err := rawVMDiskResizeSteps("/srv/rootfs.raw", 32<<30, 16<<30); err == nil || !strings.Contains(err.Error(), "VM disk shrink is not supported") {
		t.Fatalf("raw shrink error = %v", err)
	}
	steps, err := rawVMDiskResizeSteps("/srv/rootfs.raw", 16<<30, 16<<30)
	if err != nil {
		t.Fatalf("raw noop: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("noop steps = %#v, want none", steps)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRawVMDiskResizeSteps|TestZVOLVMDiskResizeSteps|TestVMDiskResizeStepsRejectShrinkAndNoop' -count=1
```

Expected before implementation: FAIL because the helpers and phases do not
exist.

- [ ] **Step 3: Implement disk resize phases and helpers**

In `pkg/catch/vm_storage.go`, add phases:

```go
const (
	vmDiskPhaseRawResize  = "raw-resize"
	vmDiskPhaseZVOLResize = "zvol-resize"
)
```

Extend `vmDiskProgressLabel`:

```go
case vmDiskPhaseRawResize, vmDiskPhaseZVOLResize:
	return "Expanding filesystem"
```

Add helpers:

```go
func rawVMDiskResizeSteps(path string, currentBytes, requestedBytes int64) ([]vmDiskPlanStep, error) {
	if err := validateVMDiskResize(currentBytes, requestedBytes); err != nil {
		return nil, err
	}
	if currentBytes == 0 || requestedBytes == 0 || requestedBytes == currentBytes {
		return nil, nil
	}
	size := fmt.Sprintf("%d", requestedBytes)
	return []vmDiskPlanStep{
		{Phase: vmDiskPhaseRawResize, Command: []string{"qemu-img", "resize", path, size}},
		{Phase: vmDiskPhaseRawResize, Command: []string{"e2fsck", "-pf", path}},
		{Phase: vmDiskPhaseRawResize, Command: []string{"resize2fs", path}},
	}, nil
}

func zvolVMDiskResizeSteps(dataset string, currentBytes, requestedBytes int64) ([]vmDiskPlanStep, error) {
	if err := validateVMDiskResize(currentBytes, requestedBytes); err != nil {
		return nil, err
	}
	if currentBytes == 0 || requestedBytes == 0 || requestedBytes == currentBytes {
		return nil, nil
	}
	if err := validateZFSName("target dataset", dataset, true); err != nil {
		return nil, err
	}
	size := fmt.Sprintf("%d", requestedBytes)
	disk := vmDiskPathForRuntime(vmDiskPlan{Backend: vmDiskBackendZVOL, Path: dataset})
	return []vmDiskPlanStep{
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"zfs", "set", "volsize=" + size, dataset}},
		{Phase: vmDiskPhaseZVOLResize, Command: vmZVOLSettleCommand()},
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"e2fsck", "-pf", disk}},
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"resize2fs", disk}},
	}, nil
}

func vmDiskResizeSteps(disk db.VMDiskConfig, requestedBytes int64) ([]vmDiskPlanStep, error) {
	switch disk.Backend {
	case vmDiskBackendZVOL:
		return zvolVMDiskResizeSteps(disk.Path, disk.Bytes, requestedBytes)
	default:
		return rawVMDiskResizeSteps(disk.Path, disk.Bytes, requestedBytes)
	}
}
```

Add `github.com/yeetrun/yeet/pkg/db` import if the file needs it; otherwise
move `vmDiskResizeSteps` to `vm_resize.go` to avoid adding a DB dependency to
storage planning.

- [ ] **Step 4: Verify disk tests pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRawVMDiskResizeSteps|TestZVOLVMDiskResizeSteps|TestVMDiskResizeStepsRejectShrinkAndNoop|TestRejectVMDiskShrink' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

Run:

```bash
git add pkg/catch/vm_storage.go pkg/catch/vm_storage_test.go
mise exec -- git commit -m "pkg/catch: plan VM disk growth"
```

## Task 3: Catch VM Service-Set Mutation

**Files:**
- Create: `pkg/catch/vm_resize.go`
- Create: `pkg/catch/vm_resize_test.go`
- Modify: `pkg/catch/tty_service_set.go`

- [ ] **Step 1: Write failing VM mutation tests**

Create `pkg/catch/vm_resize_test.go` with tests that seed a stopped VM service
and assert:

```go
func TestServiceSetVMRejectsNonVMService(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeDockerCompose},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	err := server.updateVMServiceSettings(context.Background(), "api", cli.ServiceSetFlags{CPUs: 2})
	if err == nil || !strings.Contains(err.Error(), `service "api" is not a VM service`) {
		t.Fatalf("error = %v, want non-VM service", err)
	}
}

func TestServiceSetVMRejectsRunningVM(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return true, nil })
	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.ServiceSetFlags{CPUs: 2})
	if err == nil || !strings.Contains(err.Error(), `cannot change VM settings while "devbox" is running`) {
		t.Fatalf("error = %v, want running VM error", err)
	}
}

func TestServiceSetVMUpdatesShapeAndFirecrackerConfig(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.ServiceSetFlags{CPUs: 6, Memory: "6g"}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}
	sv := mustServiceView(t, server, "devbox")
	if sv.VM().CPUs() != 6 || sv.VM().MemoryBytes() != 6<<30 {
		t.Fatalf("vm shape = %d/%d", sv.VM().CPUs(), sv.VM().MemoryBytes())
	}
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(root), "firecracker.json"), `"vcpu_count": 6`)
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(root), "firecracker.json"), `"mem_size_mib": 6144`)
}
```

Also add tests for:

- disk grow runs `qemu-img resize`, `e2fsck`, `resize2fs` and updates DB only
  after success.
- ZVOL grow runs `zfs set volsize=...`, `udevadm settle`, `e2fsck`, `resize2fs`.
- disk shrink leaves DB unchanged.
- network replacement cleans old commands, sets up new commands, rewrites
  Firecracker interfaces, and updates `VM.Networks`.

Use package-level stubs:

```go
var vmServiceSetDiskRunner vmCommandRunner
var vmServiceSetNetworkRunner vmNetworkCommandRunner
var vmServiceSetMetadataInjector func(context.Context, string, vmMetadataConfig) error
var isServiceRunningForVMSettings = (*Server).IsServiceRunning
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestServiceSetVM' -count=1
```

Expected before implementation: FAIL because `updateVMServiceSettings` and
helpers do not exist.

- [ ] **Step 3: Add service-set change dispatch**

In `pkg/catch/tty_service_set.go`, extend `serviceSetChanges`:

```go
type serviceSetChanges struct {
	root     bool
	publish  bool
	vm       bool
	snapshot bool
}
```

Update `serviceSetChangesFromFlags`:

```go
vm: hasCatchServiceSetVMChange(flags),
```

Add:

```go
func hasCatchServiceSetVMChange(flags cli.ServiceSetFlags) bool {
	return flags.CPUs != 0 ||
		strings.TrimSpace(flags.Memory) != "" ||
		strings.TrimSpace(flags.Disk) != "" ||
		flags.NetworkChange ||
		strings.TrimSpace(flags.MacvlanMac) != "" ||
		flags.MacvlanVlan != 0 ||
		strings.TrimSpace(flags.MacvlanParent) != ""
}
```

Update `any()`:

```go
return c.root || c.publish || c.vm || c.snapshot
```

In `serviceSetCmdFunc`, after publish changes and before snapshot return:

```go
if changes.vm {
	if err := e.s.updateVMServiceSettings(e.vmProvisionContext(), e.sn, flags); err != nil {
		return err
	}
}
```

- [ ] **Step 4: Implement VM settings validation and plan**

Create `pkg/catch/vm_resize.go`:

```go
package catch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmServiceSetDiskRunner       vmCommandRunner
	vmServiceSetNetworkRunner    vmNetworkCommandRunner
	vmServiceSetMetadataInjector func(context.Context, string, vmMetadataConfig) error
	isServiceRunningForVMSettings = (*Server).IsServiceRunning
)

type vmSettingsPlan struct {
	Service               string
	Root                  string
	OldVM                 db.VMConfig
	NewCPUs               int
	NewMemoryBytes        int64
	NewDiskBytes          int64
	DiskSteps             []vmDiskPlanStep
	OldNetwork            vmNetworkPlan
	NewNetwork            vmNetworkPlan
	NetworkChanged        bool
	Metadata              vmMetadataConfig
	RewriteMetadata       bool
	FirecrackerConfigPath string
	FirecrackerConfig     []byte
}
```

Implement:

```go
func (s *Server) updateVMServiceSettings(ctx context.Context, name string, flags cli.ServiceSetFlags) error {
	plan, err := s.planVMServiceSettings(ctx, name, flags)
	if err != nil {
		return err
	}
	if err := s.applyVMServiceSettingsPlan(ctx, plan); err != nil {
		return err
	}
	return s.commitVMServiceSettingsPlan(name, plan)
}
```

`planVMServiceSettings` must:

- load service and reject missing/non-VM.
- call `isServiceRunningForVMSettings` and reject running VM.
- parse requested memory/disk with `parseVMSize`.
- validate requested CPU and memory with `validateVMShape`.
- use `runningVMBytes` style admission with the current VM's memory excluded
  from the budget.
- plan disk steps using Task 2 helpers.
- build network plan only when `flags.NetworkChange` or LAN flags are present.
- render Firecracker config using current VM image kernel/initrd, current or
  new network plan, and new CPU/memory values.

- [ ] **Step 5: Implement network reconstruction**

In `vm_resize.go`, add:

```go
func vmNetworkPlanFromDB(service string, networks []db.VMNetworkConfig) vmNetworkPlan {
	plan := vmNetworkPlan{Service: service}
	for _, n := range networks {
		iface := vmNetworkInterfacePlan{
			Mode:      n.Mode,
			GuestName: n.Interface,
			Tap:       n.Tap,
			MAC:       n.MAC,
			Parent:    n.Parent,
			VLAN:      n.VLAN,
			DHCP:      n.Mode == "lan",
		}
		if n.IP.IsValid() {
			iface.GuestIP = n.IP.String() + "/24"
			iface.Gateway = vmSvcGateway
		}
		if n.Mode == "lan" {
			iface.Bridge = n.Parent
		}
		if n.Mode == "svc" {
			iface.Bridge = strings.TrimPrefix(n.Tap, "tap")
		}
		plan.Interfaces = append(plan.Interfaces, iface)
	}
	return plan
}
```

If deriving the old `svc` bridge from TAP is not reliable enough, add
`CleanupCommandsFromDB` that deletes stored TAP names and the deterministic
service bridge names generated from service/index. Tests should lock whichever
approach is implemented.

For the new network, adapt `ttyExecer.vmNetworkPlanFromFlags` logic into a
server helper that accepts `cli.ServiceSetFlags` and an existing or newly
reserved `db.SvcNetwork`.

- [ ] **Step 6: Implement artifact and DB updates**

`applyVMServiceSettingsPlan` must:

1. Run disk steps with `runVMDiskStepsWithRunner`.
2. If network changed, execute old network cleanup.
3. If network changed, execute new network setup.
4. If metadata changed, write root metadata and inject into rootfs.
5. Write the new Firecracker config.

`commitVMServiceSettingsPlan` must mutate DB after all external steps pass:

```go
_, err := s.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
	service.VM.CPUs = plan.NewCPUs
	service.VM.MemoryBytes = plan.NewMemoryBytes
	service.VM.Disk.Bytes = plan.NewDiskBytes
	if plan.NetworkChanged {
		service.VM.Networks = plan.NewNetwork.DBNetworks()
	}
	return nil
})
return err
```

- [ ] **Step 7: Verify VM mutation tests pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestServiceSetVM|TestRawVMDiskResizeSteps|TestZVOLVMDiskResizeSteps|TestVMDiskResizeStepsRejectShrinkAndNoop' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 3**

Run:

```bash
git add pkg/catch/tty_service_set.go pkg/catch/vm_resize.go pkg/catch/vm_resize_test.go pkg/catch/vm_storage.go pkg/catch/vm_storage_test.go
mise exec -- git commit -m "pkg/catch: apply VM service settings"
```

## Task 4: Local yeet.toml VM Args Sync

**Files:**
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Write failing local config tests**

In `pkg/yeet/svc_cmd_branch_test.go`, add tests:

```go
func TestSaveServiceSetConfigUpdatesVMRunArgs(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name: "devbox",
		Host: "host-a",
		Type: serviceTypeVM,
		Payload: "vm://ubuntu/26.04",
		Args: []string{"--cpus=4", "--memory=4g", "--disk=16g", "--net=svc"},
	})
	loc := &projectConfigLocation{Config: cfg, Path: filepath.Join(t.TempDir(), projectConfigName)}
	serviceOverride = "devbox"
	t.Cleanup(func() { serviceOverride = "" })

	updated, err := saveServiceSetConfig(loc, "host-a", cli.ServiceSetFlags{CPUs: 8, Memory: "8g", Disk: "32g", Net: "lan", NetworkChange: true})
	if err != nil {
		t.Fatalf("saveServiceSetConfig: %v", err)
	}
	if !updated {
		t.Fatal("updated = false")
	}
	entry, _ := cfg.ServiceEntry("devbox", "host-a")
	want := []string{"--cpus=8", "--memory=8g", "--disk=32g", "--net=lan"}
	if !reflect.DeepEqual(entry.Args, want) {
		t.Fatalf("args = %#v, want %#v", entry.Args, want)
	}
}

func TestSaveServiceSetConfigPreservesVMPayloadArgs(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name: "devbox",
		Host: "host-a",
		Type: serviceTypeVM,
		Payload: "vm://ubuntu/26.04",
		Args: []string{"--cpus=4", "--", "--guest-arg"},
	})
	loc := &projectConfigLocation{Config: cfg, Path: filepath.Join(t.TempDir(), projectConfigName)}
	serviceOverride = "devbox"
	t.Cleanup(func() { serviceOverride = "" })

	_, err := saveServiceSetConfig(loc, "host-a", cli.ServiceSetFlags{Memory: "8g"})
	if err != nil {
		t.Fatalf("saveServiceSetConfig: %v", err)
	}
	entry, _ := cfg.ServiceEntry("devbox", "host-a")
	want := []string{"--cpus=4", "--memory=8g", "--", "--guest-arg"}
	if !reflect.DeepEqual(rehydrateRunArgs(entry.Args), want) {
		t.Fatalf("rehydrated args = %#v, want %#v", rehydrateRunArgs(entry.Args), want)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestSaveServiceSetConfigUpdatesVMRunArgs|TestSaveServiceSetConfigPreservesVMPayloadArgs' -count=1
```

Expected before implementation: FAIL because VM flags are not rewritten.

- [ ] **Step 3: Implement VM run arg rewriting**

In `pkg/yeet/svc_cmd.go`, extend `applyServiceSetConfigFlags`:

```go
if err := applyServiceSetVMConfigFlags(entry, flags); err != nil {
	return err
}
return applyServiceSetSnapshotFlags(entry, flags)
```

Add:

```go
func applyServiceSetVMConfigFlags(entry *ServiceEntry, flags cli.ServiceSetFlags) error {
	if entry.Type != serviceTypeVM || !serviceSetVMChanged(flags) {
		return nil
	}
	entry.Args = rewriteVMRunArgsForServiceSet(entry.Args, flags)
	return nil
}

func serviceSetVMChanged(flags cli.ServiceSetFlags) bool {
	return flags.CPUs != 0 ||
		strings.TrimSpace(flags.Memory) != "" ||
		strings.TrimSpace(flags.Disk) != "" ||
		flags.NetworkChange ||
		strings.TrimSpace(flags.MacvlanMac) != "" ||
		flags.MacvlanVlan != 0 ||
		strings.TrimSpace(flags.MacvlanParent) != ""
}
```

Implement `rewriteVMRunArgsForServiceSet` by splitting stored args with
`splitRunArgsForParsing`, removing existing occurrences of the VM control flags
that are being changed, appending normalized replacements, then joining the
payload args through `normalizeRunArgs`:

```go
func rewriteVMRunArgsForServiceSet(args []string, flags cli.ServiceSetFlags) []string {
	flagArgs, payloadArgs := splitRunArgsForParsing(rehydrateRunArgs(args))
	flagArgs = removeRunFlags(flagArgs, vmServiceSetArgNames(flags))
	flagArgs = appendServiceSetVMArgs(flagArgs, flags)
	out := append([]string{}, flagArgs...)
	if len(payloadArgs) != 0 {
		out = append(out, "--")
		out = append(out, payloadArgs...)
	}
	return normalizeRunArgs(out)
}
```

Add `removeRunFlags`, `vmServiceSetArgNames`, and `appendServiceSetVMArgs`
using `cli.RemoteFlagSpecs()["run"]` so flags with separate values are removed
correctly.

- [ ] **Step 4: Verify local config tests pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestSaveServiceSetConfigUpdatesVMRunArgs|TestSaveServiceSetConfigPreservesVMPayloadArgs|TestHandleSvcServiceSet' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 4**

Run:

```bash
git add pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go
mise exec -- git commit -m "pkg/yeet: sync VM service settings"
```

## Task 5: VM Guest Bash Defaults

**Files:**
- Modify: `pkg/catch/vm_metadata.go`
- Modify: `pkg/catch/vm_metadata_test.go`

- [ ] **Step 1: Write failing metadata tests**

In `pkg/catch/vm_metadata_test.go`, extend `TestWriteVMGuestMetadataFiles`:

```go
profile := filepath.Join(root, "home", "ubuntu", ".profile")
assertFileContains(t, profile, "# >>> yeet VM profile >>>")
assertFileContains(t, profile, `. "$HOME/.bashrc"`)
assertFileMode(t, profile, 0o644)
bashrc := filepath.Join(root, "home", "ubuntu", ".bashrc")
assertFileContains(t, bashrc, "# >>> yeet VM defaults >>>")
assertFileContains(t, bashrc, `export PATH="$HOME/.local/bin:$PATH"`)
assertFileContains(t, bashrc, "XDG_RUNTIME_DIR")
assertFileContains(t, bashrc, "The disk is persistent. You have sudo.")
assertFileContains(t, bashrc, "yeet vm console")
assertFileMode(t, bashrc, 0o644)
```

Add a dedicated idempotence test for both files:

```go
func TestWriteVMGuestShellDefaultsPreservesUserShellFiles(t *testing.T) {
	root := t.TempDir()
	stubVMGuestChown(t)
	seedVMGuestAccountFiles(t, root)
	if err := ensureVMGuestLoginUser(root, "ubuntu"); err != nil {
		t.Fatalf("ensureVMGuestLoginUser: %v", err)
	}
	profile := filepath.Join(root, "home", "ubuntu", ".profile")
	if err := os.WriteFile(profile, []byte("export MINE=1\n# >>> yeet VM profile >>>\nold\n# <<< yeet VM profile <<<\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	bashrc := filepath.Join(root, "home", "ubuntu", ".bashrc")
	if err := os.WriteFile(bashrc, []byte("alias mine='echo mine'\n# >>> yeet VM defaults >>>\nold\n# <<< yeet VM defaults <<<\n"), 0o644); err != nil {
		t.Fatalf("write bashrc: %v", err)
	}
	if err := writeVMGuestShellDefaults(root, "ubuntu"); err != nil {
		t.Fatalf("writeVMGuestShellDefaults: %v", err)
	}
	raw, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatalf("read bashrc: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "alias mine='echo mine'") {
		t.Fatalf("bashrc lost user content:\n%s", text)
	}
	if strings.Contains(text, "\nold\n") {
		t.Fatalf("bashrc kept stale managed block:\n%s", text)
	}
	if strings.Count(text, "# >>> yeet VM defaults >>>") != 1 {
		t.Fatalf("managed block count in bashrc = %d, want 1:\n%s", strings.Count(text, "# >>> yeet VM defaults >>>"), text)
	}
	raw, err = os.ReadFile(profile)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	text = string(raw)
	if !strings.Contains(text, "export MINE=1") || strings.Contains(text, "\nold\n") {
		t.Fatalf("profile was not preserved and refreshed:\n%s", text)
	}
	if strings.Count(text, "# >>> yeet VM profile >>>") != 1 {
		t.Fatalf("managed block count in profile = %d, want 1:\n%s", strings.Count(text, "# >>> yeet VM profile >>>"), text)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestWriteVMGuestMetadataFiles|TestWriteVMGuestShellDefaultsPreservesUserShellFiles' -count=1
```

Expected before implementation: FAIL because the shell defaults writer does not
exist and shell startup files are not managed.

- [ ] **Step 3: Implement managed Bash defaults**

In `pkg/catch/vm_metadata.go`, call the writer from `writeVMGuestMetadataFiles`
after `writeVMGuestSSHAccess`:

```go
if err := writeVMGuestShellDefaults(root, cfg.User); err != nil {
	return err
}
```

Add constants:

```go
const vmGuestProfileBegin = "# >>> yeet VM profile >>>"
const vmGuestProfileEnd = "# <<< yeet VM profile <<<"
const vmGuestBashDefaultsBegin = "# >>> yeet VM defaults >>>"
const vmGuestBashDefaultsEnd = "# <<< yeet VM defaults <<<"

const vmGuestProfileDefaults = `
# Source yeet-managed Bash defaults for SSH login shells.
if [ -n "${BASH_VERSION:-}" ] && [ -f "$HOME/.bashrc" ]; then
    . "$HOME/.bashrc"
fi
`

const vmGuestBashDefaults = `
# yeet-managed VM defaults. User changes outside this block are preserved.
export PATH="$HOME/.local/bin:$PATH"
if [ -d "/run/user/$(id -u)" ]; then
    export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
fi

if [[ $- == *i* ]]; then
    shopt -s histappend checkwinsize 2>/dev/null || true
    export HISTCONTROL="${HISTCONTROL:-ignoreboth}"
    export HISTSIZE="${HISTSIZE:-5000}"
    export HISTFILESIZE="${HISTFILESIZE:-10000}"

    if command -v dircolors >/dev/null 2>&1; then
        eval "$(dircolors -b)" 2>/dev/null || true
        alias ls='ls --color=auto'
        alias grep='grep --color=auto'
        alias fgrep='fgrep --color=auto'
        alias egrep='egrep --color=auto'
    fi
    alias ll='ls -alF'
    alias la='ls -A'
    alias l='ls -CF'

    if [ -z "${YEET_VM_WELCOME_SHOWN:-}" ]; then
        export YEET_VM_WELCOME_SHOWN=1
        echo ""
        echo "You are on $(hostname -f 2>/dev/null || hostname). The disk is persistent. You have sudo."
        hints=(
          "Use 'yeet vm console <name>' from your workstation if SSH is not reachable."
          "Grow a stopped VM with 'yeet service set <name> --disk=SIZE'."
          "Try a quick web server with 'python3 -m http.server 8080'."
          "Use 'sudo systemctl status' to inspect guest services."
        )
        printf '%s\n' "${hints[$((RANDOM % ${#hints[@]}))]}"
        echo ""
    fi
fi
`
```

Add:

```go
func writeVMGuestShellDefaults(root, user string) error {
	uid, gid, ok := vmGuestUserIDs(root, user)
	if !ok {
		return fmt.Errorf("VM guest user %q not found", user)
	}
	home := filepath.Join(root, "home", user)
	if err := writeVMGuestManagedShellFile(filepath.Join(home, ".profile"), uid, gid, vmGuestProfileBegin, vmGuestProfileEnd, strings.TrimSpace(vmGuestProfileDefaults)+"\n"); err != nil {
		return err
	}
	return writeVMGuestManagedShellFile(filepath.Join(home, ".bashrc"), uid, gid, vmGuestBashDefaultsBegin, vmGuestBashDefaultsEnd, strings.TrimSpace(vmGuestBashDefaults)+"\n")
}

func writeVMGuestManagedShellFile(path string, uid, gid int, begin, end, body string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := replaceVMGuestManagedBlock(string(raw), begin, end, body)
	home := filepath.Dir(path)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return err
	}
	if err := vmGuestChown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}
```

Add managed-block helper:

```go
func replaceVMGuestManagedBlock(existing, begin, end, body string) string {
	block := begin + "\n" + body + end + "\n"
	start := strings.Index(existing, begin)
	if start == -1 {
		if strings.TrimSpace(existing) == "" {
			return block
		}
		if !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		return existing + "\n" + block
	}
	stopRel := strings.Index(existing[start:], end)
	if stopRel == -1 {
		if !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		return existing + "\n" + block
	}
	stop := start + stopRel + len(end)
	for stop < len(existing) && (existing[stop] == '\n' || existing[stop] == '\r') {
		stop++
	}
	return existing[:start] + block + existing[stop:]
}
```

- [ ] **Step 4: Verify metadata tests pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestWriteVMGuestMetadataFiles|TestWriteVMGuestShellDefaultsPreservesUserShellFiles' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 5**

Run:

```bash
git add pkg/catch/vm_metadata.go pkg/catch/vm_metadata_test.go
mise exec -- git commit -m "pkg/catch: add VM shell defaults"
```

## Task 6: Docs And User-Facing Help

**Files:**
- Modify: `README.md`
- Modify: `website/docs/vms.mdx`
- Modify: `website/docs/zfs.mdx` if the ZFS disk resize detail belongs there
- Modify: `website/docs/changelog.mdx` only if this work is immediately paired
  with a release bump.

- [ ] **Step 1: Read website local instructions**

Run:

```bash
sed -n '1,220p' website/AGENTS.md
```

- [ ] **Step 2: Update README VM section**

Add an example near the VM docs:

```markdown
Resize an existing VM while it is stopped:

```bash
yeet stop devbox
yeet service set devbox --cpus=8 --memory=8g --disk=128g
yeet service set devbox --net=lan
yeet start devbox
```

VM disk changes are grow-only. CPU, memory, disk, and network changes require
the VM to be stopped.
```

Also mention the default SSH shell:

```markdown
Yeet VMs include managed `.profile` and Bash defaults blocks for the login user
with `~/.local/bin` on PATH, common color aliases, and a short interactive
welcome. User-authored content outside the managed blocks is preserved.
```

- [ ] **Step 3: Update website VM docs**

In `website/docs/vms.mdx`, add a "Resize VM Resources" section with the same
stopped-only workflow, plus:

```mdx
Disk growth works for both raw-file VMs and ZFS-backed VMs. Shrinking disks is
not supported.
```

If the page discusses LAN networking, add:

```mdx
Network changes replace the VM interface plan on the next start. Use
`--net=svc`, `--net=lan`, or `--net=svc,lan`.
```

Add a "Default Shell" note:

```mdx
New yeet VMs write managed `.profile` and Bash defaults blocks for the login
user. They add `~/.local/bin` to PATH, enable common aliases, and print a
concise welcome with VM hints. Yeet replaces only those managed blocks during
metadata updates.
```

- [ ] **Step 4: Update ZFS docs if present**

If `website/docs/zfs.mdx` has a VM section, add:

```mdx
ZFS-backed VM disks can be grown with `yeet service set <vm> --disk=SIZE` while
the VM is stopped. Yeet grows the zvol and expands the guest filesystem; disk
shrink is intentionally unsupported.
```

- [ ] **Step 5: Commit docs**

Run targeted docs-free tests if only docs changed:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -run TestNonExistent -count=1
```

Then commit website changes inside the submodule first if website files changed:

```bash
cd website
git add docs/vms.mdx docs/zfs.mdx
git commit -m "docs: document VM resource resizing"
git push
cd ..
git add README.md website
mise exec -- git commit -m "docs: document VM resource resizing"
```

If no website files exist with those exact names, update the nearest existing
VM and ZFS docs pages and commit those paths instead.

## Task 7: Verification And Live pve1 Test

**Files:**
- No expected source edits unless verification exposes a bug.

- [ ] **Step 1: Run targeted tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full test suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Build yeet and catch**

Run:

```bash
mise exec -- go build ./cmd/yeet
mise exec -- go build ./cmd/catch
```

Expected: PASS.

- [ ] **Step 4: Install updated catch on pve1**

Run:

```bash
mise exec -- go run ./cmd/yeet init pve1
```

Expected: catch installs successfully.

- [ ] **Step 5: Run live VM resize workflow**

Use a disposable VM name that does not collide with user services:

```bash
mise exec -- go run ./cmd/yeet run resize-trash@yeet-pve1 vm://ubuntu/26.04 --service-root=flash/yeet/vms/resize-trash --zfs --disk=8g --net=svc
mise exec -- go run ./cmd/yeet stop resize-trash@yeet-pve1
mise exec -- go run ./cmd/yeet service set resize-trash@yeet-pve1 --cpus=2 --memory=2g --disk=16g
mise exec -- go run ./cmd/yeet start resize-trash@yeet-pve1
mise exec -- go run ./cmd/yeet info resize-trash@yeet-pve1
mise exec -- go run ./cmd/yeet ssh resize-trash@yeet-pve1 -- df -h /
mise exec -- go run ./cmd/yeet ssh resize-trash@yeet-pve1 -- bash -lc 'grep -n "yeet VM profile" ~/.profile && grep -n "yeet VM defaults" ~/.bashrc && echo "$PATH"'
mise exec -- go run ./cmd/yeet stop resize-trash@yeet-pve1
mise exec -- go run ./cmd/yeet service set resize-trash@yeet-pve1 --net=lan
mise exec -- go run ./cmd/yeet start resize-trash@yeet-pve1
mise exec -- go run ./cmd/yeet info resize-trash@yeet-pve1
mise exec -- go run ./cmd/yeet rm resize-trash@yeet-pve1 --clean-data --clean-config --yes
```

Expected:

- `info` reports 2 vCPU, 2 GiB memory, 16 GiB disk after shape resize.
- `df -h /` inside the guest shows the expanded root filesystem.
- after `--net=lan`, `info` reports LAN networking and a LAN address when DHCP
  completes.
- `.profile` and `.bashrc` contain the managed yeet blocks and `~/.local/bin`
  is on PATH in an interactive/login-compatible shell.
- cleanup succeeds without leaving the VM service configured.

- [ ] **Step 6: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 7: Final commit if verification fixes were needed**

If verification exposed fixes, return to the task that owns the failing area,
make the focused fix, rerun that task's targeted tests, then commit with that
task's exact file list. For example, a catch-side resize fix should use:

```bash
git add pkg/catch/tty_service_set.go pkg/catch/vm_resize.go pkg/catch/vm_resize_test.go pkg/catch/vm_storage.go pkg/catch/vm_storage_test.go
mise exec -- git commit -m "fix: harden VM resource resizing"
```

## Self-Review Checklist

- Spec coverage: parser, stopped-only mutation, CPU, memory, grow-only raw and
  ZVOL disks, network replacement, artifact rewrite, guest profile/Bash
  defaults, local config sync, docs, unit tests, and pve1 verification are all
  assigned to tasks.
- Placeholder scan: the plan uses concrete commands, file paths, and snippets.
- Type consistency: the plan consistently uses `cli.ServiceSetFlags`,
  `NetworkChange`, `vmSettingsPlan`, `db.VMConfig`, and existing VM helper names.
