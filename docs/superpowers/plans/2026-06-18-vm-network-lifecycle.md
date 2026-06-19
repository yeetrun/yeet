# VM Network Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Firecracker VM networking create-only, DB-derived, self-healing before VM start, and conservatively cleaned when runtime state is no longer owned.

**Architecture:** Add a VM network lifecycle layer around the existing `vmNetworkPlan` command model. The lifecycle reads durable VM intent from the catch DB, collects live reserved `yvm-*` links and VM service routes, runs idempotent ensure commands for DB-owned VMs, and cleans only unowned reserved VM state. VM provisioning and `yeet vm set` get rollback hooks so DB intent and host runtime state end in agreement after success or failure.

**Tech Stack:** Go, Firecracker systemd unit rendering, Linux `ip` command parsing, catch DB views, `mise exec -- go test`, GitButler (`but`), website manual submodule.

---

## Execution Notes

- Read `AGENTS.md`, `AGENTS.local.md`, `docs/agent/codebase-map.md`, `pkg/catch/AGENTS.md`, and `website/AGENTS.md` before editing the listed areas.
- Use the current GitButler branch for this work unless the user asks to change it.
- Use `but status -fv` before each commit. Set `CHANGE_IDS` to the comma-separated GitButler change IDs for files named in the current task.
- Use `mise exec -- go ...` for Go commands.
- Commit after each coherent task when that task's tests pass. Avoid micro-commits for one-line follow-ups when they belong to the same task.
- Do not push until the user asks.
- For live smoke tests, use the VM-capable live catch host described in `AGENTS.local.md`. Use unique disposable VM names and remove them at the end.

## File Structure

- Modify `pkg/catch/vm_provision.go`: reject existing VM names during `yeet run vm://...`; track network setup during provision and clean newly-created VM runtime network state on failure before DB commit.
- Modify `pkg/catch/vm_provision_test.go`: cover create-only VM semantics and failed provision network cleanup.
- Modify `pkg/catch/vm_network.go`: broaden reserved VM link parsing from service-only `b/s/v/n` to all VM-owned kinds `b/s/v/n/l`; make setup replay tolerant for generated VLAN devices; keep command construction in one place.
- Modify `pkg/catch/vm_network_reconcile.go`: replace orphan-only cleanup with a desired/live VM network lifecycle reconciler that can ensure DB-owned plans, remove stale links, remove stale VM service routes, and check drift without mutation.
- Modify `pkg/catch/vm_network_test.go`: cover reserved-name parsing, LAN tap cleanup, route parsing, startup ensure, cleanup, and check report behavior.
- Modify `pkg/catch/catch.go`: call the new VM network reconciliation from startup and reuse the lifecycle cleanup for VM removal.
- Modify `pkg/catch/vm_resize.go`: apply stopped-VM network transitions with rollback if metadata, Firecracker config, or DB commit fails.
- Modify `pkg/catch/vm_resize_test.go`: cover rollback of `yeet vm set --net` after post-network failure.
- Modify `pkg/catch/vm_systemd.go`: add a `vm-network-ensure` pre-start command with the catch data dir to every VM systemd unit.
- Modify `pkg/catch/vm_systemd_test.go`: assert the network ensure pre-start line is rendered.
- Modify `cmd/catch/catch.go`: add a local `vm-network-ensure <service>` command that loads the DB and ensures only the named VM's network state.
- Modify `cmd/catch/catch_test.go`: cover local command dispatch for `vm-network-ensure`.
- Modify `website/docs/payloads/vms.mdx`: document that `yeet run vm://...` creates VMs only once and `yeet vm set` is the change path.
- Modify `website/docs/operations/workflows.mdx`: keep VM workflow docs aligned with the create/change split.
- Modify `README.md`: add the same short user-facing note in the VM quickstart section.

### Task 1: Make VM Runs Create-Only

**Files:**
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write the failing existing-VM test**

Add this test near the other early validation tests in `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMRejectsExistingVMBeforeProvisionWork(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")
	addTestServices(t, server, db.Service{
		Name:        "svc",
		ServiceType: db.ServiceTypeVM,
		VM:          &db.VMConfig{SetupState: "ready"},
	})

	var profiled bool
	vmProvisionHostProfileFunc = func(_ *ttyExecer, _ resolvedServiceRoot, _ int64) (vmHostProfile, error) {
		profiled = true
		return vmHostProfile{}, nil
	}
	var inspected bool
	vmImageInspectFunc = func(context.Context, vmImageCache, string) (vmImageCacheState, vmImageManifest, error) {
		inspected = true
		return vmImageCacheState{}, vmImageManifest{}, nil
	}
	var networkCommands [][]string
	vmProvisionNetworkRunner = func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		return nil
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g"}, testUbuntuVMPayload)
	if err == nil || !strings.Contains(err.Error(), `VM "svc" already exists`) || !strings.Contains(err.Error(), "yeet vm set") {
		t.Fatalf("runVM error = %v, want existing VM guidance", err)
	}
	if profiled || inspected || len(networkCommands) != 0 {
		t.Fatalf("existing VM performed work: profiled=%v inspected=%v network=%#v", profiled, inspected, networkCommands)
	}
	if _, statErr := os.Stat(serviceRoot); !os.IsNotExist(statErr) {
		t.Fatalf("service root stat after existing VM = %v, want not exists", statErr)
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMRejectsExistingVMBeforeProvisionWork -count=1
```

Expected: FAIL because the current create path treats an existing VM as an update attempt.

- [ ] **Step 3: Implement create-only validation**

In `pkg/catch/vm_provision.go`, change `validateAndCheckVMProvisionService` so an existing VM returns an actionable error before service-root, image, disk, or network work:

```go
func (e *ttyExecer) validateAndCheckVMProvisionService(flags cli.RunFlags) (bool, error) {
	if err := validateVMProvisionFlags(flags); err != nil {
		return false, err
	}
	doneServiceExists := e.traceBlock("vm service exists")
	serviceExisted, err := e.serviceExists()
	doneServiceExists()
	if err != nil || !serviceExisted {
		return serviceExisted, err
	}
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return true, err
	}
	if sv.ServiceType() == db.ServiceTypeVM {
		return true, fmt.Errorf("VM %q already exists; use yeet vm set for CPU, memory, disk, or network changes, or remove and recreate it", e.sn)
	}
	return true, nil
}
```

- [ ] **Step 4: Run the targeted validation tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVMRejects(ExistingVMBeforeProvisionWork|InvalidNetworkBeforeImageSelection|MacvlanFlagsWithoutLANBeforeImageSelection)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run `but status -fv`, identify the change IDs for `pkg/catch/vm_provision.go` and `pkg/catch/vm_provision_test.go`, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "catch: make VM runs create-only" --changes "$CHANGE_IDS"
```

### Task 2: Add VM Network Desired and Live State

**Files:**
- Modify: `pkg/catch/vm_network.go`
- Modify: `pkg/catch/vm_network_reconcile.go`
- Modify: `pkg/catch/vm_network_test.go`

- [ ] **Step 1: Write reserved VM name parser tests**

In `pkg/catch/vm_network_test.go`, replace `TestOrphanedVMServiceNetworkCleanupCommandsDeletesOnlyUnownedSvcLinks` with these tests:

```go
func TestVMNetworkLinkBaseAcceptsAllReservedKinds(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		suffix string
		ok     bool
	}{
		{name: "yvm-old-123456-b0", base: "yvm-old-123456", suffix: "b0", ok: true},
		{name: "yvm-old-123456-s0", base: "yvm-old-123456", suffix: "s0", ok: true},
		{name: "yvm-old-123456-v0", base: "yvm-old-123456", suffix: "v0", ok: true},
		{name: "yvm-old-123456-n0", base: "yvm-old-123456", suffix: "n0", ok: true},
		{name: "yvm-old-123456-l0", base: "yvm-old-123456", suffix: "l0", ok: true},
		{name: "yvm-old-123456-x0", ok: false},
		{name: "eth0", ok: false},
		{name: "yvm-old-123456-lx", ok: false},
	}
	for _, tt := range tests {
		base, suffix, ok := vmNetworkLinkBase(tt.name)
		if ok != tt.ok || base != tt.base || suffix != tt.suffix {
			t.Fatalf("vmNetworkLinkBase(%q) = %q/%q/%v, want %q/%q/%v", tt.name, base, suffix, ok, tt.base, tt.suffix, tt.ok)
		}
	}
}

func TestVMNetworkCleanupCommandsDeletesOnlyUnownedReservedLinks(t *testing.T) {
	links := []string{
		"yvm-old-123456-b0",
		"yvm-old-123456-s0",
		"yvm-old-123456-v0",
		"yvm-old-123456-n0",
		"yvm-old-123456-l1",
		"yvm-live-abcdef-b0",
		"yvm-live-abcdef-s0",
		"yvm-live-abcdef-v0",
		"yvm-live-abcdef-n0",
		"yvm-live-abcdef-l1",
		"eth0",
		"vmbr0",
	}
	owned := map[string]bool{"yvm-live-abcdef": true}

	got := unownedVMNetworkLinkCleanupCommands(links, owned)
	want := [][]string{
		{"ip", "link", "del", "yvm-old-123456-v0"},
		{"ip", "netns", "exec", vmSvcNetNS, "ip", "link", "del", "yvm-old-123456-n0"},
		{"ip", "link", "del", "yvm-old-123456-s0"},
		{"ip", "link", "del", "yvm-old-123456-b0"},
		{"ip", "link", "del", "yvm-old-123456-l1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup commands = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 2: Write live route parser and cleanup tests**

Add these tests in `pkg/catch/vm_network_test.go`:

```go
func TestVMNetworkRouteFromIPRouteLine(t *testing.T) {
	tests := []struct {
		line string
		want vmNetworkRoute
		ok   bool
	}{
		{
			line: "192.168.100.12 dev yvm-old-123456-b0 src 192.168.100.254",
			want: vmNetworkRoute{Destination: "192.168.100.12/32", Device: "yvm-old-123456-b0"},
			ok:   true,
		},
		{
			line: "192.168.100.13/32 dev yvm-old-123456-b1 src 192.168.100.254",
			want: vmNetworkRoute{Destination: "192.168.100.13/32", Device: "yvm-old-123456-b1"},
			ok:   true,
		},
		{line: "default via 10.0.0.1 dev eth0", ok: false},
		{line: "192.168.100.14 dev eth0", ok: false},
	}
	for _, tt := range tests {
		got, ok := vmNetworkRouteFromIPRouteLine(tt.line)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("vmNetworkRouteFromIPRouteLine(%q) = %#v/%v, want %#v/%v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestVMNetworkCleanupCommandsDeletesStaleRoutesForUnownedBridges(t *testing.T) {
	live := vmNetworkLiveState{
		Links: []string{"yvm-old-123456-b0", "yvm-live-abcdef-b0"},
		Routes: []vmNetworkRoute{
			{Destination: "192.168.100.12/32", Device: "yvm-old-123456-b0"},
			{Destination: "192.168.100.13/32", Device: "yvm-live-abcdef-b0"},
		},
	}
	owned := map[string]bool{"yvm-live-abcdef": true}

	got := unownedVMNetworkCleanupCommands(live, owned)
	want := [][]string{
		{"ip", "route", "del", "192.168.100.12/32", "dev", "yvm-old-123456-b0"},
		{"ip", "link", "del", "yvm-old-123456-b0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup commands = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 3: Run the failing parser tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMNetwork(LinkBaseAcceptsAllReservedKinds|CleanupCommandsDeletesOnlyUnownedReservedLinks|RouteFromIPRouteLine|CleanupCommandsDeletesStaleRoutesForUnownedBridges)' -count=1
```

Expected: FAIL because `l` links, route parsing, and the new cleanup helper do not exist yet.

- [ ] **Step 4: Implement desired/live state and cleanup helpers**

In `pkg/catch/vm_network.go`, rename `vmServiceNetworkLinkBase` to `vmNetworkLinkBase` and accept all reserved VM kinds:

```go
func vmNetworkLinkBase(name string) (string, string, bool) {
	name = strings.TrimSpace(strings.SplitN(name, "@", 2)[0])
	if !strings.HasPrefix(name, "yvm-") {
		return "", "", false
	}
	lastDash := strings.LastIndex(name, "-")
	if lastDash <= len("yvm-") || lastDash == len(name)-1 {
		return "", "", false
	}
	suffix := name[lastDash+1:]
	if len(suffix) < 2 || !strings.ContainsRune("bsvnl", rune(suffix[0])) {
		return "", "", false
	}
	for _, r := range suffix[1:] {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return name[:lastDash], suffix, true
}
```

Keep a short compatibility wrapper while migrating call sites in the same task:

```go
func vmServiceNetworkLinkBase(name string) (string, string, bool) {
	return vmNetworkLinkBase(name)
}
```

In `pkg/catch/vm_network_reconcile.go`, add live route state and cleanup helpers:

```go
type vmNetworkRoute struct {
	Destination string
	Device      string
}

type vmNetworkLiveState struct {
	Links  []string
	Routes []vmNetworkRoute
}

type vmNetworkDesiredState struct {
	Plans []vmNetworkPlan
	Owned map[string]bool
}

func unownedVMNetworkCleanupCommands(live vmNetworkLiveState, owned map[string]bool) [][]string {
	cmds := unownedVMNetworkRouteCleanupCommands(live.Routes, owned)
	cmds = append(cmds, unownedVMNetworkLinkCleanupCommands(live.Links, owned)...)
	return cmds
}

func unownedVMNetworkRouteCleanupCommands(routes []vmNetworkRoute, owned map[string]bool) [][]string {
	var cmds [][]string
	for _, route := range routes {
		base, _, ok := vmNetworkLinkBase(route.Device)
		if !ok || owned[base] || route.Destination == "" {
			continue
		}
		cmds = append(cmds, []string{"ip", "route", "del", route.Destination, "dev", route.Device})
	}
	return cmds
}

func unownedVMNetworkLinkCleanupCommands(links []string, owned map[string]bool) [][]string {
	byBase := unownedVMNetworkLinksByBase(links, owned)
	bases := make([]string, 0, len(byBase))
	for base := range byBase {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	var cmds [][]string
	for _, base := range bases {
		cmds = append(cmds, unownedVMNetworkBaseCleanupCommands(base, byBase[base])...)
	}
	return cmds
}
```

Move the existing `orphanedVMServiceNetwork*` helpers to the new names and include `l` in cleanup ordering:

```go
func unownedVMNetworkBaseCleanupCommands(base string, suffixes map[string]bool) [][]string {
	var cmds [][]string
	for _, idx := range vmNetworkLinkIndexes(suffixes) {
		for _, kind := range []byte{'v', 'n', 's', 'b', 'l'} {
			if cmd := vmNetworkLinkCleanupCommand(base, kind, idx, suffixes); len(cmd) > 0 {
				cmds = append(cmds, cmd)
			}
		}
	}
	return cmds
}
```

Add route parsing in `pkg/catch/vm_network_reconcile.go`:

```go
func vmNetworkRouteFromIPRouteLine(line string) (vmNetworkRoute, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[1] != "dev" {
		return vmNetworkRoute{}, false
	}
	dest := fields[0]
	dev := fields[2]
	if base, _, ok := vmNetworkLinkBase(dev); !ok || base == "" {
		return vmNetworkRoute{}, false
	}
	if !strings.Contains(dest, "/") {
		addr, err := netip.ParseAddr(dest)
		if err != nil {
			return vmNetworkRoute{}, false
		}
		dest = addr.String() + "/32"
	}
	if pfx, err := netip.ParsePrefix(dest); err != nil || !pfx.Addr().Is4() || pfx.Bits() != 32 {
		return vmNetworkRoute{}, false
	}
	return vmNetworkRoute{Destination: dest, Device: dev}, true
}
```

When implementing, simplify the repeated `vmNetworkLinkBase` calls if desired, but keep the behavior from the tests.

- [ ] **Step 5: Make generated VLAN setup replay-tolerant**

In `pkg/catch/vm_network.go`, change `isIdempotentVMNetworkSetupCommand` so generated VLAN add commands can replay when Linux returns an already-exists error:

```go
func isIdempotentVMNetworkSetupCommand(cmd []string) bool {
	if len(cmd) < 4 || cmd[0] != "ip" {
		return false
	}
	return len(cmd) >= 5 && vmNetworkSetupVerb(cmd[1], cmd[2])
}
```

Add this test in `pkg/catch/vm_network_test.go`:

```go
func TestVMNetworkExecuteSetupToleratesExistingGeneratedVLAN(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "eth0", LANVLAN: 42})
	err := plan.ExecuteSetup(func(cmd []string) error {
		if isVMNetworkVLANAddCommand(cmd) {
			return errors.New("RTNETLINK answers: File exists")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteSetup: %v", err)
	}
}
```

- [ ] **Step 6: Run network unit tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMNetwork|TestOrphaned|TestReconcile' -count=1
```

Expected: PASS after old test names are updated or removed.

- [ ] **Step 7: Commit**

Run `but status -fv`, identify the change IDs for `pkg/catch/vm_network.go`, `pkg/catch/vm_network_reconcile.go`, and `pkg/catch/vm_network_test.go`, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "catch: model VM network runtime state" --changes "$CHANGE_IDS"
```

### Task 3: Reconcile VM Networks on Startup and Removal

**Files:**
- Modify: `pkg/catch/vm_network_reconcile.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/vm_network_test.go`

- [ ] **Step 1: Write startup reconcile tests**

Replace `TestReconcileOrphanedVMServiceNetworksDeletesOnlyLinksMissingFromDB` in `pkg/catch/vm_network_test.go` with:

```go
func TestReconcileVMNetworksEnsuresOwnedStateAndDeletesUnownedState(t *testing.T) {
	server := newTestServer(t)
	live := newVMNetworkPlan("livebox", []string{"svc", "lan"}, vmNetworkInputs{
		ServiceIP:         "192.168.100.12",
		LANParent:         "vmbr0",
		LANParentIsBridge: true,
	})
	liveBase, _, ok := vmNetworkLinkBase(live.Interfaces[0].Tap)
	if !ok {
		t.Fatalf("failed to parse live tap %q", live.Interfaces[0].Tap)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"livebox": {
			Name:        "livebox",
			ServiceType: db.ServiceTypeVM,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.12")},
			VM: &db.VMConfig{
				Runtime:  vmRuntimeFirecracker,
				Networks: live.DBNetworks(),
			},
		},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	oldCollector := vmNetworkLiveStateCollector
	oldRunner := vmNetworkReconcileRunner
	vmNetworkLiveStateCollector = func(context.Context) (vmNetworkLiveState, error) {
		return vmNetworkLiveState{
			Links: []string{
				liveBase + "-b0",
				liveBase + "-s0",
				liveBase + "-v0",
				liveBase + "-n0",
				live.Interfaces[1].Tap,
				"yvm-old-123456-b0",
				"yvm-old-123456-s0",
				"yvm-old-123456-v0",
				"yvm-old-123456-n0",
				"yvm-old-123456-l1",
			},
			Routes: []vmNetworkRoute{{Destination: "192.168.100.99/32", Device: "yvm-old-123456-b0"}},
		}, nil
	}
	var commands [][]string
	vmNetworkReconcileRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	t.Cleanup(func() {
		vmNetworkLiveStateCollector = oldCollector
		vmNetworkReconcileRunner = oldRunner
	})

	if err := server.reconcileVMNetworks(context.Background()); err != nil {
		t.Fatalf("reconcileVMNetworks: %v", err)
	}
	for _, want := range [][]string{
		{"ip", "route", "replace", "192.168.100.12/32", "dev", live.Interfaces[0].Bridge, "src", vmSvcGateway},
		{"ip", "route", "del", "192.168.100.99/32", "dev", "yvm-old-123456-b0"},
		{"ip", "link", "del", "yvm-old-123456-l1"},
	} {
		if !containsCommand(commands, want) {
			t.Fatalf("commands missing %#v in %#v", want, commands)
		}
	}
}
```

- [ ] **Step 2: Write named ensure tests**

Add this test in `pkg/catch/vm_network_test.go`:

```go
func TestEnsureVMNetworkEnsuresOnlyNamedVM(t *testing.T) {
	server := newTestServer(t)
	target := newVMNetworkPlan("target", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	other := newVMNetworkPlan("other", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.13"})
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"target": {Name: "target", ServiceType: db.ServiceTypeVM, VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Networks: target.DBNetworks()}},
		"other":  {Name: "other", ServiceType: db.ServiceTypeVM, VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Networks: other.DBNetworks()}},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	oldRunner := vmNetworkReconcileRunner
	var commands [][]string
	vmNetworkReconcileRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	t.Cleanup(func() { vmNetworkReconcileRunner = oldRunner })

	if err := server.EnsureVMNetwork(context.Background(), "target"); err != nil {
		t.Fatalf("EnsureVMNetwork: %v", err)
	}
	if !containsCommand(commands, []string{"ip", "tuntap", "add", target.Interfaces[0].Tap, "mode", "tap"}) {
		t.Fatalf("target setup missing: %#v", commands)
	}
	if containsCommand(commands, []string{"ip", "tuntap", "add", other.Interfaces[0].Tap, "mode", "tap"}) {
		t.Fatalf("other VM was modified: %#v", commands)
	}
}
```

- [ ] **Step 3: Run failing reconcile tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestReconcileVMNetworksEnsuresOwnedStateAndDeletesUnownedState|TestEnsureVMNetworkEnsuresOnlyNamedVM' -count=1
```

Expected: FAIL because `reconcileVMNetworks`, `EnsureVMNetwork`, and `vmNetworkLiveStateCollector` are not implemented.

- [ ] **Step 4: Implement lifecycle reconciliation**

In `pkg/catch/vm_network_reconcile.go`, replace the orphan-only server method with desired/live lifecycle methods:

```go
var (
	vmNetworkLiveStateCollector = collectVMNetworkLiveState
	vmNetworkReconcileRunner    vmNetworkCommandRunner
)

func (s *Server) reconcileVMNetworks(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	desired := vmNetworkDesiredStateFromDB(dv)
	live, err := vmNetworkLiveStateCollector(ctx)
	if err != nil {
		return err
	}
	ensureCmds := desired.ensureCommands()
	cleanupCmds := unownedVMNetworkCleanupCommands(live, desired.Owned)
	return runVMNetworkLifecycleCommands(ensureCmds, cleanupCmds, "reconcile VM networks")
}

func (s *Server) EnsureVMNetwork(ctx context.Context, service string) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	plan, ok := vmNetworkPlanForVMService(dv, service)
	if !ok {
		return fmt.Errorf("service %q is not a VM service", service)
	}
	return runVMNetworkLifecycleCommands(plan.SetupCommands(), nil, fmt.Sprintf("ensure VM network for %q", service))
}

func EnsureVMNetwork(ctx context.Context, cfg *Config, service string) error {
	return NewServer(cfg).EnsureVMNetwork(ctx, service)
}
```

Add helper implementations:

```go
func vmNetworkDesiredStateFromDB(dv *db.DataView) vmNetworkDesiredState {
	desired := vmNetworkDesiredState{Owned: map[string]bool{}}
	if dv == nil {
		return desired
	}
	for _, sv := range dv.Services().All() {
		if sv.ServiceType() != db.ServiceTypeVM {
			continue
		}
		vm := sv.VM()
		if !vm.Valid() {
			continue
		}
		name := sv.Name()
		plan := vmNetworkPlanFromDB(name, vm.Networks().AsSlice())
		desired.Plans = append(desired.Plans, plan)
		for base := range vmNetworkOwnedBases(plan) {
			desired.Owned[base] = true
		}
	}
	return desired
}

func vmNetworkPlanForVMService(dv *db.DataView, service string) (vmNetworkPlan, bool) {
	if dv == nil {
		return vmNetworkPlan{}, false
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok || sv.ServiceType() != db.ServiceTypeVM {
		return vmNetworkPlan{}, false
	}
	vm := sv.VM()
	if !vm.Valid() {
		return vmNetworkPlan{}, false
	}
	return vmNetworkPlanFromDB(service, vm.Networks().AsSlice()), true
}

func vmNetworkOwnedBases(plan vmNetworkPlan) map[string]bool {
	owned := map[string]bool{}
	for _, iface := range plan.Interfaces {
		for _, name := range []string{iface.Tap, iface.Bridge, iface.VLANDevice} {
			if base, _, ok := vmNetworkLinkBase(name); ok {
				owned[base] = true
			}
		}
	}
	return owned
}

func (d vmNetworkDesiredState) ensureCommands() [][]string {
	var cmds [][]string
	for _, plan := range d.Plans {
		cmds = append(cmds, plan.SetupCommands()...)
	}
	return cmds
}
```

Add live collection:

```go
func collectVMNetworkLiveState(ctx context.Context) (vmNetworkLiveState, error) {
	links, err := listVMNetworkLinks(ctx)
	if err != nil {
		return vmNetworkLiveState{}, err
	}
	routes, err := listVMNetworkRoutes(ctx)
	if err != nil {
		return vmNetworkLiveState{}, err
	}
	return vmNetworkLiveState{Links: links, Routes: routes}, nil
}
```

Use the existing `runVMNetworkCommands` helper:

```go
func runVMNetworkLifecycleCommands(ensureCmds, cleanupCmds [][]string, label string) error {
	if len(ensureCmds) == 0 && len(cleanupCmds) == 0 {
		return nil
	}
	runner := vmNetworkReconcileRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	if err := runVMNetworkCommands(runner, ensureCmds, vmNetworkCommandModeSetup); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := runVMNetworkCommands(runner, cleanupCmds, vmNetworkCommandModeCleanup); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
```

- [ ] **Step 5: Wire startup and removal to the lifecycle**

In `pkg/catch/catch.go`, update startup reconciliation:

```go
if err := s.reconcileVMNetworks(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
	log.Printf("VM network reconciliation failed: %v", err)
}
```

In `cleanupVMNetworkForRemoval`, keep the DB-derived `plan.ExecuteCleanup` behavior but update any function names affected by Task 2. Do not remove the warning behavior:

```go
if err := plan.ExecuteCleanup(runner); err != nil {
	report.addWarning(fmt.Errorf("failed to clean up VM network for %q: %w", name, err))
}
```

- [ ] **Step 6: Run startup and removal tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestReconcileVMNetworks|TestEnsureVMNetwork|TestRemove.*VM|TestVMNetwork' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

Run `but status -fv`, identify the change IDs for `pkg/catch/vm_network_reconcile.go`, `pkg/catch/catch.go`, and `pkg/catch/vm_network_test.go`, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "catch: reconcile VM network lifecycle" --changes "$CHANGE_IDS"
```

### Task 4: Roll Back Failed VM Provision Networks

**Files:**
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write the failing provision rollback test**

Add this test near `TestRunVMDoesNotCommitReadyWhenSystemdEnableFails` in `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMCleansNetworkWhenInstallFailsAfterNetworkSetup(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, systemctlCalls := newVMProvisionTestExecer(t, server, "svc")
	var networkCommands [][]string
	vmProvisionNetworkRunner = func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		return nil
	}
	vmProvisionSystemctlFunc = func(args ...string) error {
		*systemctlCalls = append(*systemctlCalls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"enable", vmSystemdUnitName("svc")}) {
			return errors.New("enable failed")
		}
		return nil
	}

	err := execer.runVM(cli.RunFlags{Net: "svc"}, testUbuntuVMPayload)
	if err == nil || !strings.Contains(err.Error(), "enable failed") {
		t.Fatalf("runVM error = %v, want enable failure", err)
	}
	if !containsCommandPrefix(networkCommands, []string{"ip", "tuntap", "add"}) {
		t.Fatalf("network setup missing: %#v", networkCommands)
	}
	if !containsCommandPrefix(networkCommands, []string{"ip", "link", "del"}) {
		t.Fatalf("network cleanup missing after failed install: %#v", networkCommands)
	}
	assertNoReadyVM(t, server, "svc")
}
```

- [ ] **Step 2: Run the failing test**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMCleansNetworkWhenInstallFailsAfterNetworkSetup -count=1
```

Expected: FAIL because failed install does not clean the network created by `applyVMProvisionArtifacts`.

- [ ] **Step 3: Track network setup from artifact application**

Change `applyVMProvisionArtifacts` to return whether it touched the network:

```go
func (e *ttyExecer) applyVMProvisionArtifacts(ctx context.Context, plan vmProvisionPlan) (networkTouched bool, err error) {
	// existing disk, metadata, injection, and config steps remain unchanged
	e.vmProgressf("Configuring network...\n")
	doneNetwork := e.traceBlock("vm network setup")
	networkTouched = true
	if err := plan.Network.ExecuteSetup(vmProvisionNetworkRunner); err != nil {
		doneNetwork()
		return networkTouched, fmt.Errorf("set up VM network: %w", err)
	}
	doneNetwork()
	// existing staged unit write remains unchanged
	return networkTouched, nil
}
```

- [ ] **Step 4: Clean network on failed finish before DB commit**

In `finishVMProvision`, add a deferred cleanup that runs only if the finish returns an error before the VM is committed:

```go
func (e *ttyExecer) finishVMProvision(ctx context.Context, plan vmProvisionPlan, payload string, restart bool, snapshotPolicyFlags *cli.ServiceSetFlags) (retErr error) {
	networkTouched := false
	committed := false
	defer func() {
		if retErr == nil || committed || !networkTouched {
			return
		}
		if err := plan.Network.ExecuteCleanup(vmProvisionNetworkRunner); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleanup failed VM network: %w", err))
		}
	}()

	doneArtifacts := e.traceBlock("vm artifacts")
	touched, err := e.applyVMProvisionArtifacts(ctx, plan)
	networkTouched = touched
	if err != nil {
		doneArtifacts()
		return err
	}
	doneArtifacts()
	// install systemd as before
	// commit as before
	committed = true
	// restart and next-command printing as before
	return nil
}
```

- [ ] **Step 5: Run provision tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVM(CleansNetworkWhenInstallFailsAfterNetworkSetup|DoesNotCommitReadyWhenSystemdEnableFails|RollsBackNewServiceReservationOnProvisionFailure|RemovesNewServiceRootOnArtifactFailure)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run `but status -fv`, identify the change IDs for `pkg/catch/vm_provision.go` and `pkg/catch/vm_provision_test.go`, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "catch: clean failed VM provision networks" --changes "$CHANGE_IDS"
```

### Task 5: Roll Back Failed VM Network Settings

**Files:**
- Modify: `pkg/catch/vm_resize.go`
- Modify: `pkg/catch/vm_resize_test.go`

- [ ] **Step 1: Write the failing `vm set` rollback test**

Add this test after `TestVMSetReplacesNetworkAndMetadata` in `pkg/catch/vm_resize_test.go`:

```go
func TestVMSetRestoresOldNetworkWhenMetadataRewriteFails(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	var networkCommands [][]string
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		return nil
	})
	withServiceSetVMMetadataInjector(t, func(context.Context, string, vmMetadataConfig) error {
		return errors.New("metadata rewrite failed")
	})

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanMac:    "02:fc:00:00:00:44",
	})
	if err == nil || !strings.Contains(err.Error(), "metadata rewrite failed") {
		t.Fatalf("updateVMServiceSettings error = %v, want metadata failure", err)
	}
	svc := getTestService(t, server, "devbox")
	if len(svc.VM.Networks) != 1 || svc.VM.Networks[0].Mode != "svc" {
		t.Fatalf("DB networks = %#v, want original svc network", svc.VM.Networks)
	}
	oldTap := "yvm-d-ea1055-s0"
	newTap := "yvm-d-ea1055-l0"
	if !containsCommand(networkCommands, []string{"ip", "tuntap", "add", newTap, "mode", "tap"}) {
		t.Fatalf("new network setup missing: %#v", networkCommands)
	}
	if !containsCommand(networkCommands, []string{"ip", "link", "del", newTap}) {
		t.Fatalf("new network rollback cleanup missing: %#v", networkCommands)
	}
	if !containsCommand(networkCommands, []string{"ip", "tuntap", "add", oldTap, "mode", "tap"}) {
		t.Fatalf("old network restore missing: %#v", networkCommands)
	}
}
```

Add `errors` to the imports in `pkg/catch/vm_resize_test.go`.

- [ ] **Step 2: Run the failing rollback test**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestVMSetRestoresOldNetworkWhenMetadataRewriteFails -count=1
```

Expected: FAIL because the current `vm set` path does not restore old network state after a later failure.

- [ ] **Step 3: Add a network transition result**

In `pkg/catch/vm_resize.go`, add a small transition helper type:

```go
type vmNetworkTransitionResult struct {
	applied bool
	old     vmNetworkPlan
	new     vmNetworkPlan
	runner  vmNetworkCommandRunner
}

func (r vmNetworkTransitionResult) rollback() error {
	if !r.applied {
		return nil
	}
	if err := r.new.ExecuteCleanup(r.runner); err != nil {
		return fmt.Errorf("clean up new VM network: %w", err)
	}
	if err := r.old.ExecuteSetup(r.runner); err != nil {
		return fmt.Errorf("restore old VM network: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Return rollback state from network application**

Change `applyVMServiceNetworkSettings`:

```go
func applyVMServiceNetworkSettings(plan vmSettingsPlan) (vmNetworkTransitionResult, error) {
	result := vmNetworkTransitionResult{old: plan.OldNetwork, new: plan.NewNetwork}
	if !plan.NetworkChanged {
		return result, nil
	}
	runner := vmServiceSetNetworkRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	result.runner = runner
	if err := plan.OldNetwork.ExecuteCleanup(runner); err != nil {
		return result, fmt.Errorf("clean up VM network: %w", err)
	}
	if err := plan.NewNetwork.ExecuteSetup(runner); err != nil {
		_ = plan.OldNetwork.ExecuteSetup(runner)
		return result, fmt.Errorf("set up VM network: %w", err)
	}
	result.applied = true
	return result, nil
}
```

- [ ] **Step 5: Move commit into the rollback window**

Change `updateVMServiceSettings` so DB commit failures also roll back network runtime state:

```go
func (s *Server) updateVMServiceSettings(ctx context.Context, name string, flags cli.VMSetFlags) (retErr error) {
	plan, err := s.planVMServiceSettings(name, flags)
	if err != nil {
		return err
	}
	transition, err := s.applyVMServiceSettingsPlan(ctx, plan)
	if err != nil {
		return err
	}
	defer func() {
		if retErr == nil {
			return
		}
		if err := transition.rollback(); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	if err := s.commitVMServiceSettingsPlan(name, plan); err != nil {
		return err
	}
	return nil
}
```

Change `applyVMServiceSettingsPlan` to return the transition result:

```go
func (s *Server) applyVMServiceSettingsPlan(ctx context.Context, plan vmSettingsPlan) (vmNetworkTransitionResult, error) {
	if err := applyVMServiceDiskSettings(ctx, plan); err != nil {
		return vmNetworkTransitionResult{}, err
	}
	transition, err := applyVMServiceNetworkSettings(plan)
	if err != nil {
		return transition, err
	}
	if err := applyVMServiceMetadataSettings(ctx, plan); err != nil {
		return transition, err
	}
	if err := writeVMFile(plan.FirecrackerConfigPath, plan.FirecrackerConfig, 0o644); err != nil {
		return transition, err
	}
	return transition, nil
}
```

Add `errors` to the imports in `pkg/catch/vm_resize.go`.

- [ ] **Step 6: Run VM settings tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMSet|TestVMCmdSet' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

Run `but status -fv`, identify the change IDs for `pkg/catch/vm_resize.go` and `pkg/catch/vm_resize_test.go`, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "catch: roll back failed VM network changes" --changes "$CHANGE_IDS"
```

### Task 6: Ensure VM Network Before Firecracker Starts

**Files:**
- Modify: `pkg/catch/vm_systemd.go`
- Modify: `pkg/catch/vm_systemd_test.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

- [ ] **Step 1: Write the systemd rendering test**

In `pkg/catch/vm_systemd_test.go`, update `TestRenderVMSystemdUnit` config to include `DataDir`:

```go
DataDir: "/srv/catch/data",
```

Add this string to the `want` list:

```go
"ExecStartPre=/srv/catch/run/catch -data-dir /srv/catch/data vm-network-ensure devbox",
```

- [ ] **Step 2: Write the catch local command test**

In `cmd/catch/catch_test.go`, add this test near `TestHandleLocalCommandVersionAndDefault`:

```go
func TestHandleLocalCommandVMNetworkEnsure(t *testing.T) {
	oldEnsure := ensureVMNetworkFn
	t.Cleanup(func() { ensureVMNetworkFn = oldEnsure })
	var gotService string
	ensureVMNetworkFn = func(_ context.Context, _ *catch.Config, service string) error {
		gotService = service
		return nil
	}

	handled, err := handleLocalCommand([]string{"vm-network-ensure", "devbox"}, &catch.Config{}, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatalf("handleLocalCommand vm-network-ensure: %v", err)
	}
	if !handled {
		t.Fatal("vm-network-ensure was not handled")
	}
	if gotService != "devbox" {
		t.Fatalf("service = %q, want devbox", gotService)
	}
}
```

- [ ] **Step 3: Run failing systemd and command tests**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestRenderVMSystemdUnit|TestHandleLocalCommandVMNetworkEnsure' -count=1
```

Expected: FAIL because the pre-start line, `DataDir`, and local command do not exist.

- [ ] **Step 4: Add `DataDir` and pre-start ensure to VM units**

In `pkg/catch/vm_systemd.go`, add a field:

```go
type vmSystemdConfig struct {
	Service          string
	DataDir          string
	Runner           string
	Firecracker      string
	ConfigPath       string
	APISocket        string
	ConsoleSocket    string
	VsockSocket      string
	WorkingDirectory string
}
```

Render the network ensure command between socket cleanup and `ExecStart`:

```go
ExecStartPre=/bin/rm -f %s
ExecStartPre=%s -data-dir %s vm-network-ensure %s
ExecStart=%s vm-run --firecracker %s --api-sock %s --config-file %s --console-sock %s
```

Update the `fmt.Sprintf` arguments in this order:

```go
cfg.Service,
cfg.WorkingDirectory,
strings.Join(cleanupSockets, " "),
cfg.Runner,
cfg.DataDir,
cfg.Service,
cfg.Runner,
cfg.Firecracker,
cfg.APISocket,
cfg.ConfigPath,
cfg.ConsoleSocket,
VMRestoreLoadFailedExitCode,
```

In `pkg/catch/vm_provision.go`, pass `DataDir` when rendering the unit:

```go
unit := renderVMSystemdUnit(vmSystemdConfig{
	Service:          e.sn,
	DataDir:          e.s.cfg.RootDir,
	Runner:           e.s.catchRunnerPath(),
	Firecracker:      image.Paths.FirecrackerPath,
	ConfigPath:       firecrackerPath,
	APISocket:        apiSocket,
	ConsoleSocket:    filepath.Join(runDir, "serial.sock"),
	VsockSocket:      vsockSocket,
	WorkingDirectory: resolvedRoot.Root,
})
```

Update other test fixtures that call `renderVMSystemdUnit` by adding `DataDir: "/srv/catch/data"`.

- [ ] **Step 5: Add the local command**

In `cmd/catch/catch.go`, add an injectable function:

```go
ensureVMNetworkFn = catch.EnsureVMNetwork
```

Change `handleLocalCommand` to accept the two-argument network command before the one-argument switch:

```go
func handleLocalCommand(args []string, scfg *catch.Config, dataDir string, out io.Writer) (bool, error) {
	if len(args) == 2 && args[0] == "vm-network-ensure" {
		if strings.TrimSpace(args[1]) == "" {
			return true, fmt.Errorf("VM service name is required")
		}
		return true, ensureVMNetworkFn(context.Background(), scfg, args[1])
	}
	if len(args) != 1 {
		return false, nil
	}
	switch args[0] {
	case "version":
		return true, writeLine(out, catch.VersionCommit())
	case "dns":
		return true, runDNSFn(context.Background(), scfg)
	case "install":
		// existing install body remains unchanged
	default:
		return false, nil
	}
}
```

- [ ] **Step 6: Run systemd and command tests**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestRenderVMSystemdUnit|TestHandleLocalCommandVMNetworkEnsure|TestRunCatchProcessHandlesLocalCommand|TestHandleLocalCommandVersionAndDefault' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

Run `but status -fv`, identify the change IDs for `pkg/catch/vm_systemd.go`, `pkg/catch/vm_systemd_test.go`, `pkg/catch/vm_provision.go`, `cmd/catch/catch.go`, and `cmd/catch/catch_test.go`, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "catch: ensure VM networks before start" --changes "$CHANGE_IDS"
```

### Task 7: Add Drift Check Report Internals

**Files:**
- Modify: `pkg/catch/vm_network_reconcile.go`
- Modify: `pkg/catch/vm_network_test.go`

- [ ] **Step 1: Write check report tests**

Add this test in `pkg/catch/vm_network_test.go`:

```go
func TestVMNetworkCheckReportClassifiesMissingAndStaleState(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	desired := vmNetworkDesiredState{
		Plans: []vmNetworkPlan{plan},
		Owned: vmNetworkOwnedBases(plan),
	}
	live := vmNetworkLiveState{
		Links: []string{"yvm-old-123456-l0"},
		Routes: []vmNetworkRoute{
			{Destination: "192.168.100.99/32", Device: "yvm-old-123456-b0"},
		},
	}

	report := desired.Check(live)
	for _, want := range []string{
		"missing link " + plan.Interfaces[0].Tap,
		"stale link yvm-old-123456-l0",
		"stale route 192.168.100.99/32 dev yvm-old-123456-b0",
	} {
		if !slices.Contains(report.Findings, want) {
			t.Fatalf("report findings = %#v, missing %q", report.Findings, want)
		}
	}
}
```

Add `slices` to the imports if it is not already present.

- [ ] **Step 2: Run the failing check test**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestVMNetworkCheckReportClassifiesMissingAndStaleState -count=1
```

Expected: FAIL because `Check` and the report type do not exist.

- [ ] **Step 3: Implement the internal check report**

In `pkg/catch/vm_network_reconcile.go`, add:

```go
type vmNetworkCheckReport struct {
	Findings []string
}

func (d vmNetworkDesiredState) Check(live vmNetworkLiveState) vmNetworkCheckReport {
	report := vmNetworkCheckReport{}
	liveLinks := map[string]bool{}
	for _, link := range live.Links {
		liveLinks[link] = true
	}
	for _, plan := range d.Plans {
		for name := range vmNetworkDeviceNames(plan) {
			if !liveLinks[name] {
				report.Findings = append(report.Findings, "missing link "+name)
			}
		}
	}
	for _, link := range live.Links {
		base, _, ok := vmNetworkLinkBase(link)
		if ok && !d.Owned[base] {
			report.Findings = append(report.Findings, "stale link "+link)
		}
	}
	for _, route := range live.Routes {
		base, _, ok := vmNetworkLinkBase(route.Device)
		if ok && !d.Owned[base] {
			report.Findings = append(report.Findings, "stale route "+route.Destination+" dev "+route.Device)
		}
	}
	sort.Strings(report.Findings)
	return report
}
```

Move `vmNetworkDeviceNames` from the test file into production code if needed:

```go
func vmNetworkDeviceNames(plan vmNetworkPlan) map[string]bool {
	names := make(map[string]bool)
	for _, iface := range plan.Interfaces {
		for _, name := range []string{iface.Tap, iface.Bridge, iface.VLANDevice} {
			if strings.HasPrefix(name, "yvm-") {
				names[name] = true
			}
		}
	}
	for _, command := range plan.SetupCommands() {
		for _, arg := range command {
			if strings.HasPrefix(arg, "yvm-") {
				names[arg] = true
			}
		}
	}
	return names
}
```

- [ ] **Step 4: Run check tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMNetworkCheckReport|TestVMNetwork' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run `but status -fv`, identify the change IDs for `pkg/catch/vm_network_reconcile.go` and `pkg/catch/vm_network_test.go`, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "catch: report VM network drift" --changes "$CHANGE_IDS"
```

### Task 8: Update User-Facing Docs

**Files:**
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `README.md`

- [ ] **Step 1: Update the VM payload manual**

In `website/docs/payloads/vms.mdx`, add this paragraph after the first create examples and before "Use `lan`":

```md
`yeet run` creates the VM service. It is not the update path for an existing
VM with the same name. To change CPU, memory, disk size, or networking after
creation, stop the VM and use `yeet vm set`.
```

- [ ] **Step 2: Update the workflows page**

In `website/docs/operations/workflows.mdx`, add this paragraph after the first VM create example:

```md
VM creates are one-time operations for a service name. Use `yeet vm set` for
supported VM changes after creation; remove and recreate the VM when you want a
fresh guest from an image.
```

- [ ] **Step 3: Update the README quickstart**

In `README.md`, add this sentence after the VM catalog paragraph in the VM quickstart section:

```md
Run a `vm://` payload once per VM name; change an existing VM with `yeet vm set`
or remove and recreate it for a fresh guest.
```

- [ ] **Step 4: Review rendered docs text**

Run:

```bash
rg -n "yeet run.*vm://|yeet vm set|creates the VM|one-time" README.md website/docs/payloads/vms.mdx website/docs/operations/workflows.mdx
```

Expected: output shows the new create-only language and existing `yeet vm set` examples.

- [ ] **Step 5: Commit website docs**

Inside the website submodule, run:

```bash
git -C website status --short
git -C website add docs/payloads/vms.mdx docs/operations/workflows.mdx
git -C website commit -m "docs: clarify VM create and change flow"
git -C website push
```

Expected: the website commit is pushed and `git -C website status --short --branch` is clean.

- [ ] **Step 6: Commit README and submodule pointer**

Run:

```bash
git diff --submodule=log -- website
but status -fv
```

Identify the change IDs for `README.md` and the `website` gitlink, then commit:

```bash
but commit codex/vm-network-orphan-reconcile -m "docs: clarify VM create-only runs" --changes "$CHANGE_IDS"
```

### Task 9: Run Full Verification and Live Smoke

**Files:**
- No new source files expected.
- Possible follow-up fixes in files touched by Tasks 1-8 if tests reveal real bugs.

- [ ] **Step 1: Run focused Go verification**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go verification**

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

- [ ] **Step 4: Install the test build on the VM-capable live host**

Use the VM-capable host from `AGENTS.local.md`:

```bash
: "${YEET_VM_SMOKE_CATCH_HOST:?set to the VM-capable catch alias from AGENTS.local.md}"
: "${YEET_VM_SMOKE_MACHINE_HOST:?set to the VM-capable machine host from AGENTS.local.md}"
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet init "$YEET_VM_SMOKE_MACHINE_HOST"
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet version
```

Expected: `yeet version` reports the current working-tree build.

- [ ] **Step 5: Smoke test VM create-only behavior**

Run with a unique disposable name:

```bash
VM_NAME=codex-net-$(date +%Y%m%d%H%M%S)
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet run "$VM_NAME" vm://ubuntu/26.04 --image-policy=cached --net=svc
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet run "$VM_NAME" vm://ubuntu/26.04 --image-policy=cached --net=svc
```

Expected: the first command creates the VM; the second command fails with existing-VM guidance mentioning `yeet vm set`.

- [ ] **Step 6: Smoke test service-network reachability**

Create a disposable service-network HTTP endpoint on the same live host:

```bash
SVC_NAME=codex-http-$(date +%Y%m%d%H%M%S)
HTTP_PORT=18080
COMPOSE_FILE=$(mktemp)
printf '%s\n' \
  'services:' \
  '  web:' \
  '    image: nginx:alpine' \
  '    ports:' \
  "      - \"$HTTP_PORT:80\"" > "$COMPOSE_FILE"
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet run "$SVC_NAME" "$COMPOSE_FILE" --net=svc
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet ssh "$VM_NAME" -- curl -fsS --retry 10 --retry-delay 1 --retry-connrefused --max-time 5 "http://$SVC_NAME:$HTTP_PORT/"
```

Expected: curl exits instead of hanging and returns the nginx page.

- [ ] **Step 7: Smoke test pre-start repair**

Stop the disposable VM, delete only its reserved `yvm-*` runtime links on the machine host, then start it:

```bash
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet stop "$VM_NAME"
VM_TAPS=$(ssh "$YEET_VM_SMOKE_MACHINE_HOST" "jq -r '.\"network-interfaces\"[].host_dev_name' /root/data/services/$VM_NAME/run/firecracker.json")
printf '%s\n' "$VM_TAPS"
ssh "$YEET_VM_SMOKE_MACHINE_HOST" "sh -lc 'for tap in $VM_TAPS; do ip link del \"\$tap\" 2>/dev/null || true; done'"
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet start "$VM_NAME"
for i in $(seq 1 20); do
  if CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet ssh "$VM_NAME" -- true; then
    break
  fi
  test "$i" -lt 20
  sleep 2
done
```

Expected: `yeet start` recreates the VM network through the systemd pre-start ensure, and `yeet ssh` succeeds.

- [ ] **Step 8: Smoke test orphan cleanup**

Plant fake reserved VM links that do not belong to any DB VM, restart catch, and verify they are gone:

```bash
FAKE_VM_NET=yvm-t-123456
ssh "$YEET_VM_SMOKE_MACHINE_HOST" "sh -lc 'ip link add $FAKE_VM_NET-b0 type bridge && ip tuntap add $FAKE_VM_NET-l0 mode tap && ip link show $FAKE_VM_NET-b0 >/dev/null && ip link show $FAKE_VM_NET-l0 >/dev/null'"
ssh "$YEET_VM_SMOKE_MACHINE_HOST" "systemctl restart catch"
for i in $(seq 1 30); do
  if ssh "$YEET_VM_SMOKE_MACHINE_HOST" "sh -lc '! ip link show $FAKE_VM_NET-b0 >/dev/null 2>&1 && ! ip link show $FAKE_VM_NET-l0 >/dev/null 2>&1'"; then
    break
  fi
  test "$i" -lt 30
  sleep 1
done
```

Expected: fake links are removed and existing non-VM network devices are untouched.

- [ ] **Step 9: Clean up the disposable VM**

Run:

```bash
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet rm "$VM_NAME" --yes --clean-data --clean-config
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet rm "$SVC_NAME" --yes --clean-data --clean-config
CATCH_HOST="$YEET_VM_SMOKE_CATCH_HOST" mise exec -- go run ./cmd/yeet status
```

Expected: the disposable VM is absent from status. Verify no remaining links for that VM on the machine host:

```bash
ssh "$YEET_VM_SMOKE_MACHINE_HOST" "sh -lc 'for tap in $VM_TAPS; do ip link show \"\$tap\" 2>/dev/null && exit 1 || true; done'"
```

Expected: no output.

- [ ] **Step 10: Commit any verification fixes**

If verification reveals real bugs, fix them with targeted tests first, rerun the failing verification, then commit with:

```bash
but status -fv
but commit codex/vm-network-orphan-reconcile -m "catch: harden VM network lifecycle" --changes "$CHANGE_IDS"
```

### Task 10: Final Integration Readiness

**Files:**
- No new source files expected.

- [ ] **Step 1: Inspect branch state**

Run:

```bash
but status -fv
```

Expected: only the intended branch commits are present; unassigned changes are empty unless they are generated local artifacts that should not be committed.

- [ ] **Step 2: Confirm test evidence**

Record the successful outputs for:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -count=1
mise exec -- go test ./... -count=1
pre-commit run --all-files
```

Expected: all pass.

- [ ] **Step 3: Confirm docs publication state**

Run:

```bash
git -C website status --short --branch
git diff --submodule=log -- website
```

Expected: website submodule is clean on its branch, and the root repo points at the pushed website commit.

- [ ] **Step 4: Report integration options**

Summarize:

- VM create-only behavior.
- Startup and pre-start network repair.
- Conservative cleanup of `yvm-*` links and stale service routes.
- Rollback behavior for provision and `vm set`.
- Docs updated.
- Tests and live smoke commands run.

Then wait for the user's finish-to-main or push instruction.

## Self-Review

**Spec coverage:** The plan covers create-only `yeet run` semantics in Task 1, durable DB-derived desired state in Tasks 2-3, conservative `yvm-*` ownership including `l` links in Task 2, startup reconciliation in Task 3, systemd pre-start ensure in Task 6, provision cleanup in Task 4, `vm set` rollback in Task 5, internal diagnostics in Task 7, docs in Task 8, and live smoke coverage in Task 9. The non-VM service networking audit remains a follow-up as specified.

**Placeholder scan:** The plan avoids deferred-work markers and unresolved file names. Live smoke commands use environment variables populated from `AGENTS.local.md` so private host aliases stay out of the committed plan.

**Type consistency:** The plan uses `vmNetworkLiveState`, `vmNetworkDesiredState`, `vmNetworkRoute`, `vmNetworkCheckReport`, `vmNetworkLinkBase`, `unownedVMNetworkCleanupCommands`, `reconcileVMNetworks`, and `EnsureVMNetwork` consistently across tests and implementation steps.
