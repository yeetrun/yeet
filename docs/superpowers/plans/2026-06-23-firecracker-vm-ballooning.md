# Firecracker VM Ballooning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Firecracker virtio-balloon support to yeet VMs by default, with per-VM memory floors and a host-level memory policy that can safely opt into VM memory overcommit.

**Architecture:** Keep Firecracker as the VM runtime and treat existing `--memory` as maximum guest memory. Add a per-VM balloon configuration with `auto|off` mode and a minimum memory floor, render a Firecracker balloon device with target `0` at boot, then add a catch-side memory controller that inflates balloons only when host memory pressure requires it. Host policy defaults to `safe` with no admission overcommit; `balanced` and `aggressive` allow controlled overcommit only when every VM's minimum floor still fits the host budget.

**Tech Stack:** Go, catch JSON DB migrations/generated views, yargs CLI metadata, Firecracker JSON config, Firecracker Unix-socket HTTP API, Linux `/proc/meminfo`, existing VM provisioning/settings/readiness paths, website docs.

---

## Scope

This plan adds Firecracker ballooning to the current VM runtime. It does not add QEMU support, live CPU hotplug, live maximum-memory resize, host swap management, cgroup memory enforcement, or automatic conversion of non-yeet VMs.

Default behavior:

- New VMs get `--balloon=auto` and a Firecracker balloon device with boot target `0`.
- Existing no-overcommit admission stays the default under host policy `safe`.
- `--memory` remains maximum guest RAM.
- `--memory-min` becomes the floor yeet will not intentionally balloon below.
- `--balloon=off` makes the VM fully reserved: floor equals max memory and no balloon device is rendered.

Host memory policies:

```text
safe:
  max commit ratio: 1.0x
  require sum(max memory) <= host budget
  pressure reclaim allowed for auto-balloon VMs

balanced:
  max commit ratio: 1.5x
  require sum(min memory) <= host budget
  pressure reclaim allowed

aggressive:
  max commit ratio: 2.0x
  require sum(min memory) <= host budget
  explicit CLI warning
```

Host budget:

```text
reserve = max(2 GiB, min(8 GiB, 10% host RAM))
budget  = host RAM - reserve
```

Default VM floor:

```text
min = max(512 MiB, 25% of max memory)
```

For VMs with max memory below 512 MiB, use `min = max memory` because the floor must never exceed the VM's maximum.

## File Structure

- Modify `pkg/db/db.go`: add `VMHostConfig`, `VMBalloonConfig`, `Data.VMHost`, and `VMConfig.Balloon`; update `go:generate` type list.
- Modify `pkg/db/migrate.go`: bump `CurrentDataVersion` and add a no-op migration from the current version to the balloon-aware schema.
- Regenerate `pkg/db/db_view.go` and `pkg/db/db_clone.go`.
- Modify `pkg/cli/cli.go`: add `--balloon` and `--memory-min` to `run` and `vm set`; add `vm memory` command metadata and parser.
- Modify `pkg/cli/cli_test.go`: parser, help, and remote-flag metadata coverage.
- Modify `cmd/yeet/cli_bridge_test.go`: bridge coverage for the new `run`, `vm set`, and `vm memory` flags.
- Modify `pkg/yeet/run_draft.go`, `pkg/yeet/run_draft_validate.go`, `pkg/yeet/run_draft_test.go`: web draft model and validation for VM balloon fields.
- Modify `pkg/yeet/web_run_assets/app.js` and `pkg/yeet/web_run_assets_test.go`: default hidden/advanced VM balloon controls.
- Modify `pkg/yeet/svc_cmd.go` and `pkg/yeet/svc_cmd_branch_test.go`: persist `vm set --balloon/--memory-min` changes in `yeet.toml` VM run args.
- Modify `pkg/catch/vm_types.go`: add shared VM memory policy and balloon parsing/validation helpers.
- Modify `pkg/catch/vm_firecracker.go` and `pkg/catch/vm_firecracker_test.go`: render Firecracker `balloon` config.
- Create `pkg/catch/vm_balloon_api.go`: Firecracker balloon API client over Unix sockets.
- Create `pkg/catch/vm_balloon_api_test.go`: Unix HTTP API tests for `GET /balloon/statistics` and `PATCH /balloon`.
- Create `pkg/catch/vm_balloon_policy.go`: host policy admission and reclaim target calculation.
- Create `pkg/catch/vm_balloon_policy_test.go`: unit tests for floors, ratios, and reclaim ordering.
- Create `pkg/catch/vm_balloon_controller.go`: periodic host-memory pressure loop that adjusts balloon targets.
- Create `pkg/catch/vm_balloon_controller_test.go`: pure unit tests for controller decisions using fake API and fake memory.
- Modify `pkg/catch/vm_host.go` and tests: include max/floor accounting in `vmHostProfile`; preserve safe defaults.
- Modify `pkg/catch/vm_provision.go`, `pkg/catch/vm_provision_test.go`, and `pkg/catch/vm_provision_ui.go`: persist/render balloon defaults and show max/floor in output.
- Modify `pkg/catch/vm_resize.go` and `pkg/catch/vm_resize_test.go`: stopped-only balloon setting changes and Firecracker config rewrite.
- Modify `pkg/catch/tty_vm.go` and `pkg/catch/tty_ops_test.go`: implement `yeet vm memory` and `yeet vm memory set --policy=...`.
- Modify `pkg/catch/service_info.go`, `pkg/catch/rpc.go`, `pkg/catchrpc/types.go`, and corresponding tests: expose VM balloon config and host memory policy/status.
- Modify `pkg/catch/recovery_vm.go`, `pkg/catch/vm_checkpoint_metadata.go`, and tests: include balloon config in full-checkpoint compatibility.
- Modify `website/docs` and README/help text: document VM max memory, minimum floor, balloon defaults, and host policies.

## Task 1: DB Schema And Shared Balloon Model

**Files:**
- Modify: `pkg/db/db.go`
- Modify: `pkg/db/migrate.go`
- Generate: `pkg/db/db_view.go`
- Generate: `pkg/db/db_clone.go`
- Modify: `pkg/db/db_test.go`
- Create: `pkg/catch/vm_balloon_policy.go`
- Create: `pkg/catch/vm_balloon_policy_test.go`
- Modify: `pkg/catch/vm_types.go`

- [ ] **Step 1: Write failing DB JSON coverage**

In `pkg/db/db_test.go`, add:

```go
func TestDBRoundTripsVMHostAndBalloonConfig(t *testing.T) {
	data := &Data{
		DataVersion: CurrentDataVersion,
		VMHost: &VMHostConfig{
			MemoryPolicy: "balanced",
		},
		Services: map[string]*Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: ServiceTypeVM,
				VM: &VMConfig{
					Runtime:     "firecracker",
					CPUs:        2,
					MemoryBytes: 4 << 30,
					Balloon: VMBalloonConfig{
						Mode:                 "auto",
						MinBytes:             1 << 30,
						StatsIntervalSeconds: 5,
						LastTargetBytes:       512 << 20,
					},
				},
			},
		},
	}

	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Data
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.VMHost == nil || got.VMHost.MemoryPolicy != "balanced" {
		t.Fatalf("VMHost = %#v, want balanced policy", got.VMHost)
	}
	vm := got.Services["devbox"].VM
	if vm.Balloon.Mode != "auto" || vm.Balloon.MinBytes != 1<<30 || vm.Balloon.StatsIntervalSeconds != 5 || vm.Balloon.LastTargetBytes != 512<<20 {
		t.Fatalf("Balloon = %#v, want persisted config", vm.Balloon)
	}
}
```

- [ ] **Step 2: Run DB tests to verify failure**

Run:

```bash
mise exec -- go test ./pkg/db -run TestDBRoundTripsVMHostAndBalloonConfig -count=1
```

Expected: FAIL because `VMHostConfig`, `VMBalloonConfig`, and `VMConfig.Balloon` do not exist.

- [ ] **Step 3: Add DB structs**

In `pkg/db/db.go`, update the generator line:

```go
//go:generate go run tailscale.com/cmd/viewer -type=Data,Service,SnapshotPolicy,Volume,ImageRepo,Artifact,DockerNetwork,DockerEndpoint,TailscaleNetwork,EndpointPort,VMConfig,VMImageConfig,VMDiskConfig,VMNetworkConfig,VMSSHConfig,VMConsoleConfig,VMSocketConfig,VMBalloonConfig,VMHostConfig --copyright=false
```

Add `VMHost` to `Data`:

```go
type Data struct {
	DataVersion int `json:",omitempty"`

	SnapshotDefaults *SnapshotPolicy `json:",omitempty"`
	VMHost           *VMHostConfig   `json:",omitempty"`

	Services map[string]*Service

	Images map[ImageRepoName]*ImageRepo

	Volumes map[string]*Volume

	DockerNetworks map[string]*DockerNetwork
}
```

Add the new config structs:

```go
type VMHostConfig struct {
	MemoryPolicy string `json:",omitempty"`
}

type VMBalloonConfig struct {
	Mode                 string
	MinBytes             int64
	StatsIntervalSeconds int   `json:",omitempty"`
	LastTargetBytes       int64 `json:",omitempty"`
}
```

Add `Balloon` to `VMConfig`:

```go
type VMConfig struct {
	Runtime string
	Image   VMImageConfig
	CPUs    int

	MemoryBytes int64
	Balloon     VMBalloonConfig
	Disk        VMDiskConfig

	Networks []VMNetworkConfig
	SSH      VMSSHConfig
	Console  VMConsoleConfig
	Sockets  VMSocketConfig

	PIDFile    string `json:",omitempty"`
	SetupState string `json:",omitempty"`
}
```

- [ ] **Step 4: Add no-op migration**

In `pkg/db/migrate.go`, change:

```go
const CurrentDataVersion = 11
```

Add the migrator:

```go
var migrators = map[int]func(*Data) error{ // Start DataVersion -> NextStep
	3:  reinit,
	4:  addDockerEndpoints,
	5:  addServiceRoot,
	6:  addServiceRootZFS,
	7:  addSnapshotPolicy,
	8:  addVMServiceConfig,
	9:  addVMVsockConfig,
	10: addVMBalloonConfig,
}

func addVMBalloonConfig(d *Data) error {
	return nil
}
```

- [ ] **Step 5: Generate DB view and clone code**

Run:

```bash
mise exec -- go generate ./pkg/db
```

Expected: `pkg/db/db_view.go` and `pkg/db/db_clone.go` update with view/clone support for `VMHostConfig` and `VMBalloonConfig`.

- [ ] **Step 6: Add balloon constants and helpers**

In `pkg/catch/vm_types.go`, add:

```go
const (
	vmBalloonModeAuto = "auto"
	vmBalloonModeOff  = "off"

	vmHostMemoryPolicySafe       = "safe"
	vmHostMemoryPolicyBalanced   = "balanced"
	vmHostMemoryPolicyAggressive = "aggressive"
)
```

Create `pkg/catch/vm_balloon_policy.go` with:

```go
package catch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmBalloonDefaultStatsIntervalSeconds = 5
	vmBalloonDefaultFloorFraction        = 4
)

type vmMemoryPolicy struct {
	Name             string
	RatioNumerator   int64
	RatioDenominator int64
}

type vmMemoryAdmissionInput struct {
	Policy          vmMemoryPolicy
	HostBytes       int64
	RunningMaxBytes int64
	RunningMinBytes int64
	RequestMaxBytes int64
	RequestMinBytes int64
}

type vmBalloonReclaimCandidate struct {
	Service       string
	MaxBytes      int64
	MinBytes      int64
	CurrentTarget int64
	FreeBytes     int64
}

func normalizeVMBalloonMode(raw string) (string, error) {
	mode := strings.TrimSpace(strings.ToLower(raw))
	if mode == "" {
		return vmBalloonModeAuto, nil
	}
	switch mode {
	case vmBalloonModeAuto, vmBalloonModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported VM balloon mode %q; use auto or off", raw)
	}
}

func normalizeVMHostMemoryPolicy(raw string) (vmMemoryPolicy, error) {
	name := strings.TrimSpace(strings.ToLower(raw))
	if name == "" {
		name = vmHostMemoryPolicySafe
	}
	switch name {
	case vmHostMemoryPolicySafe:
		return vmMemoryPolicy{Name: name, RatioNumerator: 1, RatioDenominator: 1}, nil
	case vmHostMemoryPolicyBalanced:
		return vmMemoryPolicy{Name: name, RatioNumerator: 3, RatioDenominator: 2}, nil
	case vmHostMemoryPolicyAggressive:
		return vmMemoryPolicy{Name: name, RatioNumerator: 2, RatioDenominator: 1}, nil
	default:
		return vmMemoryPolicy{}, fmt.Errorf("unsupported VM memory policy %q; use safe, balanced, or aggressive", raw)
	}
}

func defaultVMBalloonConfig(memoryBytes int64) db.VMBalloonConfig {
	return db.VMBalloonConfig{
		Mode:                 vmBalloonModeAuto,
		MinBytes:             defaultVMBalloonMinBytes(memoryBytes),
		StatsIntervalSeconds: vmBalloonDefaultStatsIntervalSeconds,
	}
}

func effectiveVMBalloonConfig(memoryBytes int64, cfg db.VMBalloonConfig) (db.VMBalloonConfig, error) {
	mode, err := normalizeVMBalloonMode(cfg.Mode)
	if err != nil {
		return db.VMBalloonConfig{}, err
	}
	out := cfg
	out.Mode = mode
	if out.StatsIntervalSeconds <= 0 {
		out.StatsIntervalSeconds = vmBalloonDefaultStatsIntervalSeconds
	}
	if mode == vmBalloonModeOff {
		out.MinBytes = memoryBytes
		out.LastTargetBytes = 0
		return out, nil
	}
	if out.MinBytes == 0 {
		out.MinBytes = defaultVMBalloonMinBytes(memoryBytes)
	}
	if out.MinBytes < 0 {
		return db.VMBalloonConfig{}, fmt.Errorf("VM minimum memory must not be negative")
	}
	if out.MinBytes > memoryBytes {
		return db.VMBalloonConfig{}, fmt.Errorf("VM minimum memory %s exceeds maximum memory %s", formatBytesInt(out.MinBytes), formatBytesInt(memoryBytes))
	}
	return out, nil
}

func defaultVMBalloonMinBytes(memoryBytes int64) int64 {
	if memoryBytes <= 0 {
		return 0
	}
	floor := memoryBytes / vmBalloonDefaultFloorFraction
	if floor < 512<<20 {
		floor = 512 << 20
	}
	if floor > memoryBytes {
		return memoryBytes
	}
	return floor
}

func vmHostMemoryBudget(hostBytes int64) int64 {
	reserve := vmHostMemoryReserve(hostBytes)
	budget := hostBytes - reserve
	if budget < 0 {
		return 0
	}
	return budget
}

func vmHostMemoryReserve(total int64) int64 {
	const gib = int64(1 << 30)
	tenPercent := total / 10
	if tenPercent < 2*gib {
		return 2 * gib
	}
	if tenPercent > 8*gib {
		return 8 * gib
	}
	return tenPercent
}

func admitVMMemoryWithPolicy(input vmMemoryAdmissionInput) error {
	budget := vmHostMemoryBudget(input.HostBytes)
	if budget <= 0 {
		return fmt.Errorf("not enough memory to start VM: host budget is 0")
	}
	committable := budget * input.Policy.RatioNumerator / input.Policy.RatioDenominator
	maxTotal := input.RunningMaxBytes + input.RequestMaxBytes
	minTotal := input.RunningMinBytes + input.RequestMinBytes
	if maxTotal > committable {
		return fmt.Errorf("not enough memory to start VM: requested max commit %s, available max commit %s under %s policy", formatBytesInt(maxTotal), formatBytesInt(committable), input.Policy.Name)
	}
	if minTotal > budget {
		return fmt.Errorf("not enough memory to start VM: requested minimum commit %s, available budget %s", formatBytesInt(minTotal), formatBytesInt(budget))
	}
	return nil
}

func vmBalloonTargetForPressure(maxBytes, minBytes, desiredReclaimBytes int64) int64 {
	limit := maxBytes - minBytes
	if limit <= 0 || desiredReclaimBytes <= 0 {
		return 0
	}
	if desiredReclaimBytes > limit {
		return limit
	}
	return desiredReclaimBytes
}

func sortVMBalloonReclaimCandidates(candidates []vmBalloonReclaimCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		iHeadroom := candidates[i].MaxBytes - candidates[i].MinBytes - candidates[i].CurrentTarget
		jHeadroom := candidates[j].MaxBytes - candidates[j].MinBytes - candidates[j].CurrentTarget
		if candidates[i].FreeBytes != candidates[j].FreeBytes {
			return candidates[i].FreeBytes > candidates[j].FreeBytes
		}
		if iHeadroom != jHeadroom {
			return iHeadroom > jHeadroom
		}
		if candidates[i].MaxBytes != candidates[j].MaxBytes {
			return candidates[i].MaxBytes > candidates[j].MaxBytes
		}
		return candidates[i].Service < candidates[j].Service
	})
}
```

- [ ] **Step 7: Add policy unit tests**

In `pkg/catch/vm_balloon_policy_test.go`, add:

```go
package catch

import (
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestDefaultVMBalloonConfigUsesAutoAndFloor(t *testing.T) {
	got := defaultVMBalloonConfig(4 << 30)
	if got.Mode != vmBalloonModeAuto || got.MinBytes != 1<<30 || got.StatsIntervalSeconds != vmBalloonDefaultStatsIntervalSeconds {
		t.Fatalf("defaultVMBalloonConfig = %#v, want auto 1GiB floor", got)
	}
}

func TestDefaultVMBalloonConfigCapsTinyVMFloor(t *testing.T) {
	got := defaultVMBalloonConfig(256 << 20)
	if got.MinBytes != 256<<20 {
		t.Fatalf("MinBytes = %d, want tiny VM max", got.MinBytes)
	}
}

func TestEffectiveVMBalloonConfigOffReservesMax(t *testing.T) {
	got, err := effectiveVMBalloonConfig(2<<30, db.VMBalloonConfig{Mode: "off", MinBytes: 512 << 20})
	if err != nil {
		t.Fatalf("effectiveVMBalloonConfig: %v", err)
	}
	if got.Mode != vmBalloonModeOff || got.MinBytes != 2<<30 || got.LastTargetBytes != 0 {
		t.Fatalf("off config = %#v, want floor=max and no target", got)
	}
}

func TestEffectiveVMBalloonConfigRejectsFloorAboveMax(t *testing.T) {
	_, err := effectiveVMBalloonConfig(1<<30, db.VMBalloonConfig{Mode: "auto", MinBytes: 2 << 30})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("error = %v, want floor above max rejection", err)
	}
}

func TestNormalizeVMHostMemoryPolicyRatios(t *testing.T) {
	cases := map[string][2]int64{
		"":           {1, 1},
		"safe":       {1, 1},
		"balanced":   {3, 2},
		"aggressive": {2, 1},
	}
	for raw, want := range cases {
		got, err := normalizeVMHostMemoryPolicy(raw)
		if err != nil {
			t.Fatalf("normalizeVMHostMemoryPolicy(%q): %v", raw, err)
		}
		if got.RatioNumerator != want[0] || got.RatioDenominator != want[1] {
			t.Fatalf("policy %q ratio = %d/%d, want %d/%d", raw, got.RatioNumerator, got.RatioDenominator, want[0], want[1])
		}
	}
}

func TestAdmitVMMemoryWithPolicyBalancedAllowsMaxOvercommitWhenFloorsFit(t *testing.T) {
	policy, err := normalizeVMHostMemoryPolicy("balanced")
	if err != nil {
		t.Fatal(err)
	}
	err = admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		Policy:          policy,
		HostBytes:       16 << 30,
		RunningMaxBytes: 12 << 30,
		RunningMinBytes: 3 << 30,
		RequestMaxBytes: 4 << 30,
		RequestMinBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("admit balanced overcommit: %v", err)
	}
}

func TestAdmitVMMemoryWithPolicyRejectsFloorsPastBudget(t *testing.T) {
	policy, err := normalizeVMHostMemoryPolicy("aggressive")
	if err != nil {
		t.Fatal(err)
	}
	err = admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		Policy:          policy,
		HostBytes:       8 << 30,
		RunningMaxBytes: 4 << 30,
		RunningMinBytes: 4 << 30,
		RequestMaxBytes: 4 << 30,
		RequestMinBytes: 3 << 30,
	})
	if err == nil || !strings.Contains(err.Error(), "minimum commit") {
		t.Fatalf("error = %v, want minimum commit rejection", err)
	}
}

func TestSortVMBalloonReclaimCandidates(t *testing.T) {
	candidates := []vmBalloonReclaimCandidate{
		{Service: "small", MaxBytes: 1 << 30, MinBytes: 512 << 20, FreeBytes: 768 << 20},
		{Service: "large-low-free", MaxBytes: 8 << 30, MinBytes: 2 << 30, FreeBytes: 1 << 30},
		{Service: "large-free", MaxBytes: 8 << 30, MinBytes: 2 << 30, FreeBytes: 4 << 30},
	}
	sortVMBalloonReclaimCandidates(candidates)
	if candidates[0].Service != "large-free" {
		t.Fatalf("first candidate = %q, want large-free", candidates[0].Service)
	}
}
```

- [ ] **Step 8: Run DB and policy tests**

Run:

```bash
mise exec -- go test ./pkg/db ./pkg/catch -run 'TestDBRoundTripsVMHostAndBalloonConfig|TestDefaultVMBalloon|TestEffectiveVMBalloon|TestNormalizeVMHostMemoryPolicy|TestAdmitVMMemoryWithPolicy|TestSortVMBalloon' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

Use GitButler:

```bash
but status
but add pkg/db/db.go pkg/db/migrate.go pkg/db/db_view.go pkg/db/db_clone.go pkg/db/db_test.go pkg/catch/vm_types.go pkg/catch/vm_balloon_policy.go pkg/catch/vm_balloon_policy_test.go
but commit -m "vm: add balloon memory model"
```

## Task 2: CLI And Web Draft Surface

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `pkg/yeet/run_draft.go`
- Modify: `pkg/yeet/run_draft_validate.go`
- Modify: `pkg/yeet/run_draft_test.go`
- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/web_run_assets_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Write failing CLI parser tests**

In `pkg/cli/cli_test.go`, extend VM run and set parser tests with:

```go
func TestParseRunFlagsVMBalloon(t *testing.T) {
	flags, rest, err := ParseRun([]string{"devbox", "vm://ubuntu/26.04", "--memory=4g", "--memory-min=1g", "--balloon=auto"})
	if err != nil {
		t.Fatalf("ParseRun: %v", err)
	}
	if strings.Join(rest, " ") != "devbox vm://ubuntu/26.04" {
		t.Fatalf("rest = %#v, want service payload", rest)
	}
	if flags.Memory != "4g" || flags.MemoryMin != "1g" || flags.Balloon != "auto" {
		t.Fatalf("flags = %#v, want memory balloon fields", flags)
	}
}

func TestParseVMSetBalloonFlags(t *testing.T) {
	flags, rest, err := ParseVMSet([]string{"--memory-min=2g", "--balloon=off"})
	if err != nil {
		t.Fatalf("ParseVMSet: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %#v, want none", rest)
	}
	if flags.MemoryMin != "2g" || flags.Balloon != "off" {
		t.Fatalf("flags = %#v, want balloon fields", flags)
	}
}

func TestParseVMSetRejectsInvalidBalloonMode(t *testing.T) {
	_, _, err := ParseVMSet([]string{"--balloon=maybe"})
	if err == nil || !strings.Contains(err.Error(), "use auto or off") {
		t.Fatalf("error = %v, want invalid balloon mode", err)
	}
}

func TestParseVMMemoryCommand(t *testing.T) {
	flags, rest, err := ParseVMMemory([]string{"set", "--policy=balanced"})
	if err != nil {
		t.Fatalf("ParseVMMemory: %v", err)
	}
	if flags.Policy != "balanced" || strings.Join(rest, " ") != "set" {
		t.Fatalf("flags=%#v rest=%#v, want set balanced", flags, rest)
	}
}
```

- [ ] **Step 2: Implement CLI structs and parsers**

In `pkg/cli/cli.go`, add fields:

```go
type RunFlags struct {
	CPUs             int
	Memory           string
	MemoryMin        string
	Balloon          string
	Disk             string
	Net              string
	// existing fields remain
}

type VMSetFlags struct {
	CPUs          int
	Memory        string
	MemoryMin     string
	Balloon       string
	Disk          string
	Net           string
	NetworkChange bool
	MacvlanMac    string
	MacvlanVlan   int
	MacvlanParent string
}

type VMMemoryFlags struct {
	Policy string
	Format string
}
```

Add parsed structs:

```go
type vmMemoryFlagsParsed struct {
	Policy string `flag:"policy" help:"Host VM memory policy: safe, balanced, aggressive"`
	Format string `flag:"format" help:"Output format: table, json, json-pretty"`
	Output string `flag:"output" help:"Alias for --format"`
}
```

Add parser:

```go
func ParseVMMemory(args []string) (VMMemoryFlags, []string, error) {
	result, err := yargs.ParseFlags[vmMemoryFlagsParsed](args)
	if err != nil {
		return VMMemoryFlags{}, nil, err
	}
	flags := VMMemoryFlags{
		Policy: strings.TrimSpace(result.Flags.Policy),
		Format: firstNonEmpty(strings.TrimSpace(result.Flags.Format), strings.TrimSpace(result.Flags.Output)),
	}
	if flags.Format == "" {
		flags.Format = "table"
	}
	if flags.Policy != "" {
		switch strings.ToLower(flags.Policy) {
		case "safe", "balanced", "aggressive":
		default:
			return VMMemoryFlags{}, nil, fmt.Errorf("unsupported VM memory policy %q; use safe, balanced, or aggressive", flags.Policy)
		}
	}
	return flags, result.Args, nil
}
```

Extend run and VM set parsed structs with:

```go
MemoryMin string `flag:"memory-min" help:"Minimum memory floor for VM ballooning"`
Balloon   string `flag:"balloon" help:"VM balloon mode: auto or off"`
```

In `validateVMSetFlags`, reject unsupported balloon values:

```go
if flags.Balloon != "" {
	switch strings.ToLower(strings.TrimSpace(flags.Balloon)) {
	case "auto", "off":
	default:
		return fmt.Errorf("unsupported VM balloon mode %q; use auto or off", flags.Balloon)
	}
}
```

Update `hasVMSetChange`:

```go
return flags.CPUs > 0 ||
	strings.TrimSpace(flags.Memory) != "" ||
	strings.TrimSpace(flags.MemoryMin) != "" ||
	strings.TrimSpace(flags.Balloon) != "" ||
	strings.TrimSpace(flags.Disk) != "" ||
	hasVMSetNetworkChange(flags)
```

- [ ] **Step 3: Add command metadata and bridge tests**

In the VM group command map, add:

```go
"memory": {
	Name:        "memory",
	Description: "Show or set host VM memory policy",
	Usage:       "vm memory [set --policy=safe|balanced|aggressive] [--format=table|json|json-pretty]",
	FlagsSchema: vmMemoryFlagsParsed{},
	Examples: []string{
		"yeet vm memory",
		"yeet vm memory set --policy=balanced",
	},
},
```

In remote group flag specs, add:

```go
"memory": flagSpecsFromStruct(vmMemoryFlagsParsed{}),
```

In `cmd/yeet/cli_bridge_test.go`, add:

```go
func TestBridgeServiceArgsVMSetBalloonFlags(t *testing.T) {
	service, host, bridged, ok := BridgeServiceArgs([]string{"vm", "set", "devbox", "--memory-min=1g", "--balloon=auto"})
	if !ok || service != "devbox" || host != "" {
		t.Fatalf("bridge ok=%v service=%q host=%q", ok, service, host)
	}
	if got := strings.Join(bridged, " "); got != "vm set --memory-min=1g --balloon=auto" {
		t.Fatalf("bridged = %q", got)
	}
}

func TestBridgeServiceArgsSkipsVMMemory(t *testing.T) {
	service, host, bridged, ok := BridgeServiceArgs([]string{"vm", "memory", "set", "--policy=balanced"})
	if !ok || service != "" || host != "" {
		t.Fatalf("bridge ok=%v service=%q host=%q", ok, service, host)
	}
	if got := strings.Join(bridged, " "); got != "vm memory set --policy=balanced" {
		t.Fatalf("bridged = %q", got)
	}
}
```

- [ ] **Step 4: Add run draft model tests**

In `pkg/yeet/run_draft_test.go`, add:

```go
func TestRunDraftIncludesVMBalloonFlags(t *testing.T) {
	draft, err := NewRunDraft([]string{"devbox", "vm://ubuntu/26.04", "--memory=4g", "--memory-min=1g", "--balloon=auto"})
	if err != nil {
		t.Fatalf("NewRunDraft: %v", err)
	}
	if draft.VM.Memory != "4g" || draft.VM.MemoryMin != "1g" || draft.VM.Balloon != "auto" {
		t.Fatalf("draft VM = %#v, want balloon settings", draft.VM)
	}
	args := draft.RunArgs()
	for _, want := range []string{"--memory=4g", "--memory-min=1g", "--balloon=auto"} {
		if !containsString(args, want) {
			t.Fatalf("RunArgs missing %q: %#v", want, args)
		}
	}
}
```

In `pkg/yeet/run_draft_validate_test.go`, add:

```go
func TestValidateRunDraftRejectsVMBalloonForNonVM(t *testing.T) {
	draft, err := NewRunDraft([]string{"app", "nginx:latest", "--memory-min=1g", "--balloon=auto"})
	if err != nil {
		t.Fatalf("NewRunDraft: %v", err)
	}
	result := ValidateRunDraft(draft)
	if result.Valid {
		t.Fatal("result.Valid = true, want invalid")
	}
	if !result.HasError("vm.balloon") || !result.HasError("vm.memoryMin") {
		t.Fatalf("errors = %#v, want vm balloon/memoryMin errors", result.Errors)
	}
}
```

- [ ] **Step 5: Implement draft fields**

In `pkg/yeet/run_draft.go`, extend the VM draft struct:

```go
type RunDraftVM struct {
	VCPUs     int    `json:"vcpus,omitempty"`
	Memory    string `json:"memory,omitempty"`
	MemoryMin string `json:"memoryMin,omitempty"`
	Balloon   string `json:"balloon,omitempty"`
	Disk      string `json:"disk,omitempty"`
}
```

When building run args, append:

```go
if strings.TrimSpace(draft.VM.MemoryMin) != "" {
	args = append(args, "--memory-min="+strings.TrimSpace(draft.VM.MemoryMin))
}
if strings.TrimSpace(draft.VM.Balloon) != "" {
	args = append(args, "--balloon="+strings.TrimSpace(draft.VM.Balloon))
}
```

In `pkg/yeet/run_draft_validate.go`, add VM-only validation:

```go
if strings.TrimSpace(draft.VM.MemoryMin) != "" && draft.Payload.Kind != payloadKindVM {
	result.addError("vm.memoryMin", "--memory-min is only valid for VM payloads")
}
if strings.TrimSpace(draft.VM.Balloon) != "" && draft.Payload.Kind != payloadKindVM {
	result.addError("vm.balloon", "--balloon is only valid for VM payloads")
}
if strings.TrimSpace(draft.VM.Balloon) != "" {
	switch strings.ToLower(strings.TrimSpace(draft.VM.Balloon)) {
	case "auto", "off":
	default:
		result.addError("vm.balloon", "VM balloon mode must be auto or off")
	}
}
```

- [ ] **Step 6: Update local config rewrite**

In `pkg/yeet/svc_cmd_branch_test.go`, add:

```go
func TestVMSetConfigRewritesBalloonRunFlags(t *testing.T) {
	entry := ServiceEntry{
		Name: "devbox",
		Type: serviceTypeVM,
		Args: []string{"--memory=4g", "--memory-min=512m", "--balloon=off", "vm://ubuntu/26.04"},
	}
	applyVMSetConfigFlags(&entry, cli.VMSetFlags{MemoryMin: "1g", Balloon: "auto"})
	got := strings.Join(entry.Args, " ")
	for _, want := range []string{"--memory=4g", "--memory-min=1g", "--balloon=auto", "vm://ubuntu/26.04"} {
		if !strings.Contains(got, want) {
			t.Fatalf("args = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "--memory-min=512m") || strings.Contains(got, "--balloon=off") {
		t.Fatalf("args = %q, old flags remain", got)
	}
}
```

In `pkg/yeet/svc_cmd.go`, update `vmSetRunFlagChanges`:

```go
if value := strings.TrimSpace(flags.MemoryMin); value != "" {
	add("--memory-min", value)
}
if value := strings.TrimSpace(flags.Balloon); value != "" {
	add("--balloon", value)
}
```

- [ ] **Step 7: Add minimal web UI controls**

In `pkg/yeet/web_run_assets_test.go`, add assertions that VM deploy includes `memoryMin` and `balloon`, and that non-VM payloads do not render those controls.

In `pkg/yeet/web_run_assets/app.js`, add an advanced VM memory row near the VM shape controls:

```js
const memoryMinInput = field("Memory floor", "memoryMin", state.vm.memoryMin || "", "1g");
const balloonSelect = selectField("Balloon", "balloon", state.vm.balloon || "auto", [
  ["auto", "Auto"],
  ["off", "Off"],
]);
```

When serializing VM args:

```js
if (state.vm.memoryMin) args.push(`--memory-min=${state.vm.memoryMin}`);
if (state.vm.balloon && state.vm.balloon !== "auto") args.push(`--balloon=${state.vm.balloon}`);
```

Keep the visible default minimal: show `Memory floor` only inside the VM advanced/settings section, not in the first-run critical path.

- [ ] **Step 8: Run CLI and draft tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet -run 'TestParseRunFlagsVMBalloon|TestParseVMSetBalloonFlags|TestParseVMSetRejectsInvalidBalloonMode|TestParseVMMemoryCommand|TestBridgeServiceArgsVMSetBalloonFlags|TestBridgeServiceArgsSkipsVMMemory|TestRunDraftIncludesVMBalloonFlags|TestValidateRunDraftRejectsVMBalloonForNonVM|TestVMSetConfigRewritesBalloonRunFlags|TestWebRun' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

Use GitButler:

```bash
but status
but add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge_test.go pkg/yeet/run_draft.go pkg/yeet/run_draft_validate.go pkg/yeet/run_draft_test.go pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go
but commit -m "vm: expose balloon memory settings"
```

## Task 3: Firecracker Balloon Config And API Client

**Files:**
- Modify: `pkg/catch/vm_firecracker.go`
- Modify: `pkg/catch/vm_firecracker_test.go`
- Create: `pkg/catch/vm_balloon_api.go`
- Create: `pkg/catch/vm_balloon_api_test.go`

- [ ] **Step 1: Write failing Firecracker config test**

In `pkg/catch/vm_firecracker_test.go`, add:

```go
func TestRenderFirecrackerConfigIncludesBalloon(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource:    firecrackerBootSource{KernelImagePath: "/srv/images/vmlinux"},
		Drives:        []firecrackerDrive{{DriveID: "rootfs", PathOnHost: "/srv/rootfs.raw", IsRootDevice: true}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 4096},
		Balloon: &firecrackerBalloon{
			AmountMib:             0,
			DeflateOnOOM:          true,
			StatsPollingIntervalS: 5,
		},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"balloon"`,
		`"amount_mib": 0`,
		`"deflate_on_oom": true`,
		`"stats_polling_interval_s": 5`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Implement Firecracker balloon JSON**

In `pkg/catch/vm_firecracker.go`, add field and struct:

```go
type firecrackerConfig struct {
	BootSource        firecrackerBootSource         `json:"boot-source"`
	Drives            []firecrackerDrive            `json:"drives"`
	NetworkInterfaces []firecrackerNetworkInterface `json:"network-interfaces"`
	MachineConfig     firecrackerMachineConfig      `json:"machine-config"`
	Vsock             *firecrackerVsock             `json:"vsock,omitempty"`
	Balloon           *firecrackerBalloon            `json:"balloon,omitempty"`
}

type firecrackerBalloon struct {
	AmountMib             int  `json:"amount_mib"`
	DeflateOnOOM          bool `json:"deflate_on_oom"`
	StatsPollingIntervalS int  `json:"stats_polling_interval_s,omitempty"`
}
```

- [ ] **Step 3: Write failing API client tests**

Create `pkg/catch/vm_balloon_api_test.go`:

```go
package catch

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirecrackerBalloonPatchUsesUnixHTTP(t *testing.T) {
	socket := newBalloonUnixHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/balloon" {
			t.Fatalf("request = %s %s, want PATCH /balloon", r.Method, r.URL.Path)
		}
		var body firecrackerBalloonPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.AmountMib != 512 {
			t.Fatalf("amount_mib = %d, want 512", body.AmountMib)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := firecrackerBalloonAPI{}.SetTarget(context.Background(), socket, 512<<20); err != nil {
		t.Fatalf("SetTarget: %v", err)
	}
}

func TestFirecrackerBalloonStatsUsesUnixHTTP(t *testing.T) {
	socket := newBalloonUnixHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/balloon/statistics" {
			t.Fatalf("request = %s %s, want GET /balloon/statistics", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"target_pages":131072,"actual_pages":65536,"free_memory":2147483648,"available_memory":3221225472}`))
	})
	stats, err := firecrackerBalloonAPI{}.Stats(context.Background(), socket)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TargetBytes != 512<<20 || stats.ActualBytes != 256<<20 || stats.FreeBytes != 2<<30 || stats.AvailableBytes != 3<<30 {
		t.Fatalf("stats = %#v, want converted bytes", stats)
	}
}

func newBalloonUnixHTTPServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir := t.TempDir()
	socket := filepath.Join(dir, "firecracker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		if err := server.Serve(listener); err != nil && !strings.Contains(err.Error(), "closed") {
			t.Errorf("server: %v", err)
		}
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = os.Remove(socket)
	})
	return socket
}
```

- [ ] **Step 4: Implement API client**

Create `pkg/catch/vm_balloon_api.go`:

```go
package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const firecrackerBalloonPageSize = int64(4096)

type vmBalloonAPI interface {
	SetTarget(context.Context, string, int64) error
	Stats(context.Context, string) (vmBalloonStats, error)
}

type firecrackerBalloonAPI struct{}

type vmBalloonStats struct {
	TargetBytes    int64
	ActualBytes    int64
	FreeBytes      int64
	AvailableBytes int64
}

type firecrackerBalloonPatchRequest struct {
	AmountMib int `json:"amount_mib"`
}

type firecrackerBalloonStatsResponse struct {
	TargetPages     int64 `json:"target_pages"`
	ActualPages     int64 `json:"actual_pages"`
	FreeMemory      int64 `json:"free_memory"`
	AvailableMemory int64 `json:"available_memory"`
}

func (firecrackerBalloonAPI) SetTarget(ctx context.Context, socket string, targetBytes int64) error {
	if targetBytes < 0 {
		return fmt.Errorf("VM balloon target must not be negative")
	}
	body, err := json.Marshal(firecrackerBalloonPatchRequest{AmountMib: int(targetBytes >> 20)})
	if err != nil {
		return err
	}
	req, err := firecrackerUnixRequest(ctx, http.MethodPatch, socket, "/balloon", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := firecrackerUnixHTTPClient(socket).Do(req)
	if err != nil {
		return fmt.Errorf("set Firecracker balloon target: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("set Firecracker balloon target: %s", resp.Status)
	}
	return nil
}

func (firecrackerBalloonAPI) Stats(ctx context.Context, socket string) (vmBalloonStats, error) {
	req, err := firecrackerUnixRequest(ctx, http.MethodGet, socket, "/balloon/statistics", nil)
	if err != nil {
		return vmBalloonStats{}, err
	}
	resp, err := firecrackerUnixHTTPClient(socket).Do(req)
	if err != nil {
		return vmBalloonStats{}, fmt.Errorf("read Firecracker balloon stats: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vmBalloonStats{}, fmt.Errorf("read Firecracker balloon stats: %s", resp.Status)
	}
	var raw firecrackerBalloonStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return vmBalloonStats{}, fmt.Errorf("decode Firecracker balloon stats: %w", err)
	}
	return vmBalloonStats{
		TargetBytes:    raw.TargetPages * firecrackerBalloonPageSize,
		ActualBytes:    raw.ActualPages * firecrackerBalloonPageSize,
		FreeBytes:      raw.FreeMemory,
		AvailableBytes: raw.AvailableMemory,
	}, nil
}

func firecrackerUnixRequest(ctx context.Context, method, socket, path string, body io.Reader) (*http.Request, error) {
	if strings.TrimSpace(socket) == "" {
		return nil, fmt.Errorf("Firecracker API socket is required")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return nil, err
	}
	return req, nil
}

func firecrackerUnixHTTPClient(socket string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}
}
```

The snapshot code already has Unix-socket Firecracker test helpers in `pkg/catch/vm_snapshot_test.go`; keep those test helpers there for this task and add the production Unix HTTP helpers above in `pkg/catch/vm_balloon_api.go`. If a later implementation wants the snapshot code to share the production helper too, move only the two production helper functions into `pkg/catch/firecracker_api.go` and update both call sites in the same commit.

- [ ] **Step 5: Fix imports and run tests**

Run:

```bash
mise exec -- gofmt -w pkg/catch/vm_firecracker.go pkg/catch/vm_firecracker_test.go pkg/catch/vm_balloon_api.go pkg/catch/vm_balloon_api_test.go
mise exec -- go test ./pkg/catch -run 'TestRenderFirecrackerConfigIncludesBalloon|TestFirecrackerBalloon' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Use GitButler:

```bash
but status
but add pkg/catch/vm_firecracker.go pkg/catch/vm_firecracker_test.go pkg/catch/vm_balloon_api.go pkg/catch/vm_balloon_api_test.go
but commit -m "vm: add Firecracker balloon support"
```

## Task 4: Provisioning, Settings, And Admission

**Files:**
- Modify: `pkg/catch/vm_host.go`
- Modify: `pkg/catch/vm_defaults.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `pkg/catch/vm_provision_ui.go`
- Modify: `pkg/catch/vm_resize.go`
- Modify: `pkg/catch/vm_resize_test.go`

- [ ] **Step 1: Write failing provisioning tests**

In `pkg/catch/vm_provision_test.go`, add:

```go
func TestRunVMProvisionPersistsAndRendersDefaultBalloon(t *testing.T) {
	server := newTestServer(t)
	execer, root, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	if err := execer.runVM(cli.RunFlags{Net: "svc", Memory: "4g", Restart: false}, testUbuntuVMPayload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.Balloon.Mode != vmBalloonModeAuto || svc.VM.Balloon.MinBytes != 1<<30 {
		t.Fatalf("Balloon = %#v, want auto 1GiB floor", svc.VM.Balloon)
	}
	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(root), "firecracker.json"))
	if err != nil {
		t.Fatalf("read firecracker config: %v", err)
	}
	for _, want := range []string{`"balloon"`, `"amount_mib": 0`, `"deflate_on_oom": true`, `"stats_polling_interval_s": 5`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("firecracker config missing %q:\n%s", want, raw)
		}
	}
}

func TestRunVMProvisionBalloonOffOmitsBalloonDevice(t *testing.T) {
	server := newTestServer(t)
	execer, root, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	if err := execer.runVM(cli.RunFlags{Net: "svc", Memory: "4g", Balloon: "off", Restart: false}, testUbuntuVMPayload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.Balloon.Mode != vmBalloonModeOff || svc.VM.Balloon.MinBytes != 4<<30 {
		t.Fatalf("Balloon = %#v, want off floor=max", svc.VM.Balloon)
	}
	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(root), "firecracker.json"))
	if err != nil {
		t.Fatalf("read firecracker config: %v", err)
	}
	if strings.Contains(string(raw), `"balloon"`) {
		t.Fatalf("firecracker config includes balloon for off mode:\n%s", raw)
	}
}
```

- [ ] **Step 2: Extend shape/provision planning**

In `pkg/catch/vm_types.go`, extend:

```go
type vmShape struct {
	CPUs        int
	MemoryBytes int64
	MinMemoryBytes int64
	BalloonMode string
	DiskBytes   int64
	DiskBackend string
}
```

In `pkg/catch/vm_provision.go`, add shape override parsing:

```go
func applyVMBalloonOverrides(shape *vmShape, flags cli.RunFlags) error {
	if mode := strings.TrimSpace(flags.Balloon); mode != "" {
		normalized, err := normalizeVMBalloonMode(mode)
		if err != nil {
			return err
		}
		shape.BalloonMode = normalized
	}
	if value := strings.TrimSpace(flags.MemoryMin); value != "" {
		minBytes, err := parseVMSize(value)
		if err != nil {
			return fmt.Errorf("invalid --memory-min: %w", err)
		}
		shape.MinMemoryBytes = minBytes
	}
	cfg, err := effectiveVMBalloonConfig(shape.MemoryBytes, db.VMBalloonConfig{Mode: shape.BalloonMode, MinBytes: shape.MinMemoryBytes})
	if err != nil {
		return err
	}
	shape.BalloonMode = cfg.Mode
	shape.MinMemoryBytes = cfg.MinBytes
	return nil
}
```

Call `applyVMBalloonOverrides` after memory overrides and before admission.

- [ ] **Step 3: Render balloon config from shape**

In `newVMProvisionPlan`, build:

```go
balloonConfig, err := effectiveVMBalloonConfig(shape.MemoryBytes, db.VMBalloonConfig{
	Mode:     shape.BalloonMode,
	MinBytes: shape.MinMemoryBytes,
})
if err != nil {
	return vmProvisionPlan{}, err
}
var balloon *firecrackerBalloon
if balloonConfig.Mode == vmBalloonModeAuto {
	balloon = &firecrackerBalloon{
		AmountMib:             0,
		DeflateOnOOM:          true,
		StatsPollingIntervalS: balloonConfig.StatsIntervalSeconds,
	}
}
```

Pass `Balloon: balloon` into `firecrackerConfig`.

Add `Balloon db.VMBalloonConfig` to `vmProvisionPlan` and set it to `balloonConfig`.

In `commitVMProvision`, persist:

```go
Balloon: plan.Balloon,
```

- [ ] **Step 4: Add admission accounting tests**

In `pkg/catch/vm_provision_test.go`, add:

```go
func TestRunVMBalancedPolicyAllowsMaxOvercommitWhenFloorsFit(t *testing.T) {
	server := newTestServer(t)
	_, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.VMHost = &db.VMHostConfig{MemoryPolicy: "balanced"}
		d.Services = map[string]*db.Service{
			"existing": {Name: "existing", ServiceType: db.ServiceTypeVM, VM: &db.VMConfig{
				Runtime: vmRuntimeFirecracker, MemoryBytes: 12 << 30,
				Balloon: db.VMBalloonConfig{Mode: vmBalloonModeAuto, MinBytes: 3 << 30},
			}},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	vmProvisionHostProfileFunc = func(_ *ttyExecer, _ resolvedServiceRoot, runningMaxBytes int64) (vmHostProfile, error) {
		return vmHostProfile{Arch: "amd64", HasKVM: true, LogicalCPUs: 16, MemoryBytes: 16 << 30, StorageBytes: 128 << 30, RunningVMBytes: runningMaxBytes, RunningVMMinBytes: 3 << 30}, nil
	}
	t.Cleanup(func() { vmProvisionHostProfileFunc = nil })
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	if err := execer.runVM(cli.RunFlags{Net: "svc", Memory: "4g", MemoryMin: "1g", Restart: false}, testUbuntuVMPayload); err != nil {
		t.Fatalf("runVM balanced overcommit: %v", err)
	}
}
```

- [ ] **Step 5: Update host profile and admission**

In `pkg/catch/vm_host.go`, extend `vmHostProfile`:

```go
RunningVMBytes    int64
RunningVMMinBytes int64
```

Replace `admitVMMemory(profile, requestBytes)` with:

```go
func admitVMMemory(profile vmHostProfile, requestMaxBytes, requestMinBytes int64, policy vmMemoryPolicy) error {
	return admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		Policy:          policy,
		HostBytes:       profile.MemoryBytes,
		RunningMaxBytes: profile.RunningVMBytes,
		RunningMinBytes: profile.RunningVMMinBytes,
		RequestMaxBytes: requestMaxBytes,
		RequestMinBytes: requestMinBytes,
	})
}
```

Add `(*Server).vmHostMemoryPolicy()`:

```go
func (s *Server) vmHostMemoryPolicy() (vmMemoryPolicy, error) {
	dv, err := s.getDB()
	if err != nil {
		return vmMemoryPolicy{}, err
	}
	raw := ""
	if host := dv.VMHost(); host.Valid() {
		raw = host.MemoryPolicy()
	}
	return normalizeVMHostMemoryPolicy(raw)
}
```

If generated views make `dv.VMHost()` unavailable until generation is complete, use `s.cfg.DB.MutateData` only for writes and `s.getDB().AsStruct()` style access already used in this repo.

- [ ] **Step 6: Update `vm set` behavior**

In `pkg/catch/vm_resize_test.go`, add:

```go
func TestVMSetUpdatesBalloonConfigAndFirecrackerConfig(t *testing.T) {
	server := newTestServer(t)
	root := seedVMForResize(t, server, "devbox", vmDiskBackendRaw, "")
	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{MemoryMin: "2g", Balloon: "auto"}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.Balloon.Mode != vmBalloonModeAuto || svc.VM.Balloon.MinBytes != 2<<30 {
		t.Fatalf("Balloon = %#v, want auto 2GiB floor", svc.VM.Balloon)
	}
	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(root), "firecracker.json"))
	if err != nil {
		t.Fatalf("read Firecracker config: %v", err)
	}
	if !strings.Contains(string(raw), `"balloon"`) {
		t.Fatalf("Firecracker config missing balloon:\n%s", raw)
	}
}
```

In `pkg/catch/vm_resize.go`, extend `vmSettingsPlan`:

```go
NewBalloon db.VMBalloonConfig
```

Initialize from `oldVM.Balloon`, apply `MemoryMin` and `Balloon`, and render it into Firecracker config. Keep the existing stopped-only restriction.

- [ ] **Step 7: Update output**

In `pkg/catch/vm_provision_ui.go`, change shape line from:

```go
return fmt.Sprintf("%d vCPU, %s memory, %s disk", shape.CPUs, formatVMProvisionBytes(shape.MemoryBytes), formatVMProvisionBytes(shape.DiskBytes))
```

to:

```go
line := fmt.Sprintf("%d vCPU, %s memory, %s disk", shape.CPUs, formatVMProvisionBytes(shape.MemoryBytes), formatVMProvisionBytes(shape.DiskBytes))
if shape.BalloonMode == vmBalloonModeAuto && shape.MinMemoryBytes > 0 && shape.MinMemoryBytes < shape.MemoryBytes {
	line += fmt.Sprintf(" (%s floor)", formatVMProvisionBytes(shape.MinMemoryBytes))
}
return line
```

- [ ] **Step 8: Run provisioning and resize tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVMProvision.*Balloon|TestRunVMBalancedPolicyAllowsMaxOvercommit|TestVMSetUpdatesBalloonConfig|TestVMSetRejects|TestVMSetUpdatesShape' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

Use GitButler:

```bash
but status
but add pkg/catch/vm_host.go pkg/catch/vm_defaults.go pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go pkg/catch/vm_provision_ui.go pkg/catch/vm_resize.go pkg/catch/vm_resize_test.go
but commit -m "vm: apply balloon settings to VMs"
```

## Task 5: Host Memory Command And Controller

**Files:**
- Create: `pkg/catch/vm_balloon_controller.go`
- Create: `pkg/catch/vm_balloon_controller_test.go`
- Modify: `pkg/catch/tty_vm.go`
- Modify: `pkg/catch/tty_ops_test.go`
- Modify: `pkg/catch/catch.go`

- [ ] **Step 1: Write controller calculation tests**

Create `pkg/catch/vm_balloon_controller_test.go`:

```go
package catch

import "testing"

func TestVMBalloonControllerPlansNoTargetsWhenMemoryHealthy(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:        16 << 30,
		MemAvailable:     8 << 30,
		CurrentPolicyName: vmHostMemoryPolicyBalanced,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 2 << 30},
		},
	})
	if len(plan.Targets) != 0 {
		t.Fatalf("targets = %#v, want none", plan.Targets)
	}
}

func TestVMBalloonControllerReclaimsUnderPressureWithoutPassingFloor(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:        16 << 30,
		MemAvailable:     1 << 30,
		CurrentPolicyName: vmHostMemoryPolicyBalanced,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 3 << 30},
		},
	})
	if len(plan.Targets) != 1 {
		t.Fatalf("targets = %#v, want one target", plan.Targets)
	}
	if plan.Targets["a"] > 3<<30 {
		t.Fatalf("target = %d, exceeds max-floor", plan.Targets["a"])
	}
}
```

- [ ] **Step 2: Implement controller planning**

Create `pkg/catch/vm_balloon_controller.go`:

```go
package catch

import (
	"context"
	"time"
)

type vmBalloonControllerInput struct {
	HostBytes         int64
	MemAvailable     int64
	CurrentPolicyName string
	Candidates        []vmBalloonReclaimCandidate
}

type vmBalloonControllerPlan struct {
	Targets map[string]int64
}

func planVMBalloonTargets(input vmBalloonControllerInput) vmBalloonControllerPlan {
	reserve := vmHostMemoryReserve(input.HostBytes)
	startReclaim := reserve + input.HostBytes/10
	stopReclaim := reserve + (input.HostBytes*15)/100
	if input.MemAvailable >= startReclaim {
		return vmBalloonControllerPlan{Targets: nil}
	}
	desired := stopReclaim - input.MemAvailable
	if desired <= 0 {
		return vmBalloonControllerPlan{Targets: nil}
	}
	candidates := append([]vmBalloonReclaimCandidate(nil), input.Candidates...)
	sortVMBalloonReclaimCandidates(candidates)
	targets := map[string]int64{}
	remaining := desired
	for _, candidate := range candidates {
		headroom := candidate.MaxBytes - candidate.MinBytes - candidate.CurrentTarget
		if headroom <= 0 {
			continue
		}
		reclaim := headroom
		if candidate.FreeBytes > 0 && candidate.FreeBytes < reclaim {
			reclaim = candidate.FreeBytes
		}
		if reclaim > remaining {
			reclaim = remaining
		}
		if reclaim <= 0 {
			continue
		}
		targets[candidate.Service] = candidate.CurrentTarget + reclaim
		remaining -= reclaim
		if remaining <= 0 {
			break
		}
	}
	return vmBalloonControllerPlan{Targets: targets}
}

func (s *Server) runVMBalloonController(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.reconcileVMBalloons(ctx, firecrackerBalloonAPI{})
		}
	}
}
```

Add `reconcileVMBalloons` after the planning function. It should:

1. Read DB services.
2. Skip non-VMs, stopped VMs, `Balloon.Mode == off`, and VMs without API sockets.
3. Read each VM's balloon stats.
4. Read host memory with existing `linuxMemTotalBytes` plus a new `linuxMemAvailableBytes`.
5. Plan targets.
6. Apply only changed targets with `SetTarget`.
7. Persist `LastTargetBytes` best-effort after successful API calls.

- [ ] **Step 3: Write host command tests**

In `pkg/catch/tty_ops_test.go`, add:

```go
func TestVMMemorySetPolicyPersistsHostConfig(t *testing.T) {
	server := newTestServer(t)
	execer := newTestTTYExecer(t, server, "")
	if err := execer.vmCmdFunc([]string{"memory", "set", "--policy=balanced"}); err != nil {
		t.Fatalf("vm memory set: %v", err)
	}
	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	if !dv.VMHost().Valid() || dv.VMHost().MemoryPolicy() != "balanced" {
		t.Fatalf("VMHost = %#v, want balanced", dv.VMHost().AsStruct())
	}
}

func TestVMMemoryRejectsAggressiveWithoutSet(t *testing.T) {
	server := newTestServer(t)
	execer := newTestTTYExecer(t, server, "")
	err := execer.vmCmdFunc([]string{"memory", "--policy=aggressive"})
	if err == nil || !strings.Contains(err.Error(), "use vm memory set") {
		t.Fatalf("error = %v, want set-only policy change", err)
	}
}
```

- [ ] **Step 4: Implement `yeet vm memory`**

In `pkg/catch/tty_vm.go`, add:

```go
case "memory":
	return e.vmMemoryRemoteCmdFunc(args[1:])
```

Add:

```go
func (e *ttyExecer) vmMemoryRemoteCmdFunc(args []string) error {
	flags, rest, err := cli.ParseVMMemory(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		if strings.TrimSpace(flags.Policy) != "" {
			return fmt.Errorf("use vm memory set --policy=%s to change host VM memory policy", flags.Policy)
		}
		return e.s.printVMMemoryStatus(e.rw, flags.Format)
	}
	if len(rest) == 1 && rest[0] == "set" {
		return e.s.setVMMemoryPolicy(flags.Policy)
	}
	return fmt.Errorf("unexpected vm memory args: %s", strings.Join(rest, " "))
}
```

Create `pkg/catch/vm_memory_cmd.go` with `setVMMemoryPolicy` and `printVMMemoryStatus` so command formatting stays separate from the controller loop.

- [ ] **Step 5: Start controller with catch**

In `pkg/catch/catch.go`, add the controller to `(*Server).Start` next to the existing monitor goroutines:

```go
s.waitGroup.Go(func() {
	s.runVMBalloonController(s.ctx)
})
```

Keep daemon wiring out of `cmd/catch/catch.go`; `cmd/catch` already starts `catch.NewServer(scfg)`, and `NewServer` calls `Start`.

- [ ] **Step 6: Run controller and command tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMBalloonController|TestVMMemory' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

Use GitButler:

```bash
but status
but add pkg/catch/vm_balloon_controller.go pkg/catch/vm_balloon_controller_test.go pkg/catch/tty_vm.go pkg/catch/tty_ops_test.go pkg/catch/catch.go
but commit -m "vm: manage balloon memory policy"
```

## Task 6: Info, RPC, Snapshot Compatibility, And Docs

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`
- Modify: `pkg/catch/rpc.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`
- Modify: `pkg/catch/vm_checkpoint_metadata.go`
- Modify: `pkg/catch/recovery_vm.go`
- Modify: `pkg/catch/recovery_vm_test.go`
- Modify: `README.md`
- Modify: `website/docs`

- [ ] **Step 1: Add service info tests**

In `pkg/catch/service_info_test.go`, extend VM info expectations:

```go
if got.VM.Balloon.Mode != "auto" || got.VM.Balloon.MinMemory != "1g" {
	t.Fatalf("VM balloon info = %#v, want auto 1g", got.VM.Balloon)
}
```

In `pkg/yeet/info_cmd_test.go`, add an assertion:

```go
assertPlainRow(t, text, "Balloon", "auto, floor 1 GB")
```

- [ ] **Step 2: Extend RPC types**

In `pkg/catchrpc/types.go`, add:

```go
type ServiceVMBalloon struct {
	Mode       string `json:"mode,omitempty"`
	MinBytes   int64  `json:"minBytes,omitempty"`
	MinMemory  string `json:"minMemory,omitempty"`
	LastTarget int64  `json:"lastTargetBytes,omitempty"`
}
```

Add `Balloon ServiceVMBalloon` to `ServiceVM`.

Populate it in `pkg/catch/service_info.go`:

```go
Balloon: catchrpc.ServiceVMBalloon{
	Mode:       vm.Balloon.Mode,
	MinBytes:   vm.Balloon.MinBytes,
	MinMemory:  formatVMSizeFlag(vm.Balloon.MinBytes),
	LastTarget: vm.Balloon.LastTargetBytes,
},
```

- [ ] **Step 3: Include balloon config in checkpoint compatibility**

In `pkg/catch/vm_checkpoint_metadata.go`, add:

```go
BalloonConfigHash string `json:"balloonConfigHash,omitempty"`
```

In `vmCheckpointCompatibility`, add:

```go
BalloonConfigHash string
```

Hash the effective balloon config:

```go
balloonHash, err := canonicalJSONSHA256(vm.Balloon)
if err != nil {
	return vmCheckpointCompatibility{}, err
}
compat.BalloonConfigHash = balloonHash
```

In `pkg/catch/recovery_vm.go`, reject full-state restore when the snapshot balloon config hash differs from the current VM config:

```go
if strings.TrimSpace(metadata.BalloonConfigHash) != "" && strings.TrimSpace(metadata.BalloonConfigHash) != strings.TrimSpace(current.BalloonConfigHash) {
	return fmt.Errorf("checkpoint balloon config does not match current VM config")
}
```

- [ ] **Step 4: Update user docs**

In `README.md` and the VM docs under `website/docs`, add a concise user-facing section:

```md
### VM memory and ballooning

`--memory` is the maximum RAM a VM can use. New VMs enable Firecracker
ballooning by default so yeet can reclaim unused guest memory when the host is
under pressure.

Use `--memory-min` to set the floor yeet will not intentionally reclaim below:

```bash
yeet run devbox vm://ubuntu/26.04 --memory=4g --memory-min=1g
yeet vm set devbox --memory-min=2g
```

Disable ballooning for a VM when it must keep all configured memory reserved:

```bash
yeet vm set devbox --balloon=off
```

Host policy defaults to `safe`, which enables ballooning but does not admit
memory overcommit. Homelab hosts that usually run idle VMs can opt into
controlled overcommit:

```bash
yeet vm memory set --policy=balanced
```
```

Keep release/changelog wording user-facing if this ships in a release: "VMs now support memory ballooning and optional host memory overcommit policies."

- [ ] **Step 5: Run tests**

Run:

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -run 'TestServiceInfoIncludesVMConfig|TestInfoRender|TestFullVMCheckpoint|TestRestoreRecoveryPoint' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit docs and info work**

Commit website changes inside the submodule first if website docs changed:

```bash
git -C website status --short
git -C website add docs
git -C website commit -m "docs: explain VM ballooning"
git -C website push
```

Then commit root changes with GitButler:

```bash
but status
but add pkg/catchrpc/types.go pkg/catch/service_info.go pkg/catch/service_info_test.go pkg/catch/rpc.go pkg/yeet/info_cmd.go pkg/yeet/info_cmd_test.go pkg/catch/vm_checkpoint_metadata.go pkg/catch/recovery_vm.go pkg/catch/recovery_vm_test.go README.md website
but commit -m "vm: document balloon memory behavior"
```

## Task 7: Live Verification On `yeet-lab`

**Files:**
- No source files changed unless live smoke reveals a bug.

- [ ] **Step 1: Install current catch build on lab-host**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
```

Expected: install succeeds and `CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet version` reports the workspace build.

- [ ] **Step 2: Create disposable VM with default balloon**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run codex-balloon-default vm://ubuntu/26.04 --net=svc --memory=2g --disk=8g --image-policy=cached
```

Expected: VM reaches readiness.

- [ ] **Step 3: Verify Firecracker config and API**

Run on host:

```bash
ssh root@lab-host 'python3 - <<'"'"'PY'"'"'
import json
from pathlib import Path
cfg = json.loads(Path("/root/data/services/codex-balloon-default/run/firecracker.json").read_text())
print(cfg.get("balloon"))
PY'
```

Expected: output contains `amount_mib: 0`, `deflate_on_oom: true`, and a stats interval.

Run:

```bash
ssh root@lab-host 'curl --unix-socket /root/data/services/codex-balloon-default/run/firecracker.sock http://localhost/balloon/statistics'
```

Expected: JSON statistics or a clear Firecracker API response. If stats are unavailable, inspect Firecracker version and adjust the API path or feature gating before continuing.

- [ ] **Step 4: Test manual host policy**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm memory
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm memory set --policy=balanced
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm memory --format=json
```

Expected: policy changes to `balanced` and JSON reports it.

- [ ] **Step 5: Test memory floor update**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet stop codex-balloon-default
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm set codex-balloon-default --memory-min=1g
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet start codex-balloon-default
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet info codex-balloon-default
```

Expected: info shows `Balloon auto, floor 1 GB`.

- [ ] **Step 6: Clean up disposable VM**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet rm codex-balloon-default --yes --clean-data --clean-config
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm memory set --policy=safe
```

Expected: disposable service and config are removed; host policy is back to safe unless the user explicitly wants to leave `balanced` enabled.

- [ ] **Step 7: Commit any live-smoke fixes**

If any code fixes were required:

```bash
but status
but add <changed-files>
but commit -m "vm: fix balloon live smoke issue"
```

## Task 8: Final Quality Gate

**Files:**
- No planned source edits.

- [ ] **Step 1: Run targeted tests**

Run:

```bash
mise exec -- go test ./pkg/db ./pkg/cli ./cmd/yeet ./pkg/catchrpc ./pkg/yeet ./pkg/catch -count=1
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
pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Check GitButler state**

Run:

```bash
but status -fv
```

Expected: only this session's branch has unpublished ballooning commits.

- [ ] **Step 5: Stop for integration decision**

Do not push or land on `main` unless the user explicitly asks. Report:

- summary of user-facing behavior
- tests run
- live `yeet-lab` smoke result
- whether website submodule was committed/pushed

## Self-Review

Spec coverage:

- Firecracker ballooning by default: Task 3 and Task 4.
- Per-VM opt out: Task 2 and Task 4 via `--balloon=off`.
- Max memory versus minimum floor: Task 1, Task 2, Task 4, docs in Task 6.
- Host-level policy: Task 1 and Task 5.
- Controlled overcommit: Task 1 admission helpers and Task 4 provisioning admission.
- Pressure-driven reclaim loop: Task 5.
- Observability/docs: Task 5 command, Task 6 info/RPC/docs.
- Live lab-host validation: Task 7.

Placeholder scan:

- No `TBD`, `TODO`, or "implement later" placeholders are present.
- Every code-changing task names exact files, snippets, tests, commands, and expected outcomes.

Type consistency:

- `VMBalloonConfig` uses `Mode`, `MinBytes`, `StatsIntervalSeconds`, and `LastTargetBytes`.
- `RunFlags`, `VMSetFlags`, and run draft all use `MemoryMin` and `Balloon`.
- Host policy strings are `safe`, `balanced`, and `aggressive`.
- Firecracker JSON field names match the documented API fields: `amount_mib`, `deflate_on_oom`, and `stats_polling_interval_s`.
