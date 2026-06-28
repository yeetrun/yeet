# VM LAN Bridge Prep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet run <vm> ... --net=lan` work on fresh Debian/Ubuntu VM hosts by discovering, preparing, persisting, and reusing the correct LAN bridge without requiring the user to know host bridge setup.

**Architecture:** Treat VM LAN networking as a host capability: catch discovers the real LAN uplink, reuses an existing bridge when one already carries the default route, or prepares a durable `br0` bridge after explicit operator consent. Host-network mutation runs as a resumable detached operation with rollback and status polling so `yeet init` and `yeet run --net=lan` can tolerate a short SSH/RPC disconnect and can be rerun safely.

**Tech Stack:** Go, Linux `/sys` and `ip -json` discovery, Ubuntu/Debian netplan with `gopkg.in/yaml.v3`, catch local commands/RPC, Firecracker TAP networking, GitButler, website MDX docs.

---

## Scope And Invariants

- This is one cohesive feature because host discovery, persistence, init prompting, run prompting, and VM network plan replay all need the same bridge decision and state model.
- Mutate host networking only after an explicit yes from an interactive prompt or an explicit opt-in env/flag. A normal read-only plan must not write files, run `netplan apply`, or change links.
- Use an existing bridge when it is already the default-route device or the master of the default-route uplink. On Proxmox this should keep using `vmbr0`; on a host with a meaningful `br0`, use `br0`.
- Prefer creating `br0` when no usable bridge exists and `br0` is free. Do not create a separate `yeetbr0` for the normal fresh-host path.
- Do not hijack an unrelated `br0`. If `br0` exists but does not own the selected LAN uplink, fail before mutation with an error that names the current `br0` state and the selected uplink.
- Select the LAN uplink dynamically from host state. Do not hardcode `eno1`, `eth0`, or any other interface name.
- For the first implementation pass, support netplan configurations rendered by `networkd` on Debian/Ubuntu-like systems. Detect NetworkManager, ifupdown, wireless uplinks, bonds/VLANs that cannot be represented, and ambiguous multiple candidates, then fail before mutation with a specific message.
- Persist the bridge through reboot. Runtime-only `ip link` changes are not enough.
- DB-backed VM network state must store the bridge name used for Firecracker TAP attachment, not the physical uplink. Cleanup and reconciliation must never delete the host bridge or physical uplink.
- When bridge prep can momentarily interrupt connectivity, the mutating process must outlive the initiating SSH/RPC connection, keep a status file, and install a rollback timer before applying the config.

## File Structure

- Create `pkg/catch/vm_lan_bridge.go`: host LAN discovery model, candidate scoring, bridge plan decision, and small command runner hooks.
- Create `pkg/catch/vm_lan_bridge_test.go`: pure planner tests with fake links, routes, addresses, bridge masters, and renderer metadata.
- Create `pkg/catch/vm_lan_bridge_netplan.go`: netplan reader/writer for the supported networkd renderer and dry-run validation.
- Create `pkg/catch/vm_lan_bridge_netplan_test.go`: fixture tests for DHCP, static address, unrelated `br0`, unsupported renderer, and non-representable config.
- Create `pkg/catch/vm_lan_bridge_prepare.go`: detached bridge preparation operation, status JSON, backup files, rollback scheduling, verification, and idempotent resume.
- Create `pkg/catch/vm_lan_bridge_prepare_test.go`: runner tests for status transitions, rollback, failed validation, and idempotent ready state.
- Modify `cmd/catch/catch.go`: add catch local commands for `vm-lan-bridge-plan`, `vm-lan-bridge-prepare`, and `vm-lan-bridge-status`.
- Modify `cmd/catch/catch_test.go`: cover the new local commands and argument validation.
- Modify `cmd/catch/vm_prereqs.go`: include bridge planning and optional preparation in VM host setup after VM package readiness.
- Modify `cmd/catch/vm_prereqs_test.go`: cover init-time bridge prompts, env opt-in, env skip, unsupported host behavior, and already-ready bridge behavior.
- Modify `pkg/yeet/init.go`: pass bridge-prep intent to remote catch install and make detached install polling tolerant of transient SSH failures during bridge apply.
- Modify `pkg/yeet/init_test.go`: cover remote install env construction and reconnect/poll behavior.
- Modify `pkg/catch/vm_provision.go`: ensure the LAN bridge before reserving durable VM artifacts for `yeet run --net=lan`.
- Modify `pkg/catch/vm_network.go`: resolve LAN parent to a usable bridge and keep validation focused on executable bridge-backed plans.
- Modify `pkg/catch/vm_network_test.go`: update old non-bridge rejection tests and add bridge-prepared resolution tests.
- Modify `pkg/catch/vm_resize.go`: replay stored bridge-backed LAN config when resizing/replanning.
- Modify `pkg/catch/vm_network_reconcile.go`: preserve host bridges during reconciliation and surface repair guidance for legacy non-bridge DB state.
- Modify `README.md`: mention that `yeet init` can prepare VM LAN bridge support on fresh Debian/Ubuntu hosts.
- Modify `website/docs/concepts/networking.mdx`: explain VM LAN bridge behavior and contrast it with Docker/service LAN networking.
- Modify `website/docs/getting-started/first-run-validation.mdx`: document init/run prompts and recovery after a temporary network drop.
- Modify `website/docs/changelog.mdx`: add release-facing notes only when this feature is being prepared for release.

## Task 1: Add Dynamic Host LAN Bridge Planning

**Files:**
- Create: `pkg/catch/vm_lan_bridge.go`
- Create: `pkg/catch/vm_lan_bridge_test.go`

- [ ] **Step 1: Write failing planner tests**

Add `pkg/catch/vm_lan_bridge_test.go` with these table cases:

```go
func TestPlanHostLANBridgeUsesExistingDefaultRouteBridge(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "br0", Kind: "bridge", OperState: "up"},
		},
		routes: []vmLANRoute{{Default: true, Iface: "br0", Gateway: "192.168.1.1"}},
		addrs:  []vmLANAddress{{Iface: "br0", Prefix: "192.168.1.44/24", Scope: "global"}},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	plan, err := planHostLANBridge(state)
	if err != nil {
		t.Fatalf("planHostLANBridge: %v", err)
	}
	if !plan.Ready || plan.Bridge != "br0" || plan.NeedsPrepare {
		t.Fatalf("plan = %#v, want ready br0 without prepare", plan)
	}
}

func TestPlanHostLANBridgeUsesBridgeMasterOfDefaultRoutePort(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "vmbr0", Kind: "bridge", OperState: "up"},
			{Name: "eno1", Kind: "ether", OperState: "up", Master: "vmbr0", HasHardware: true},
		},
		routes: []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "10.0.0.1"}},
		addrs:  []vmLANAddress{{Iface: "vmbr0", Prefix: "10.0.0.20/24", Scope: "global"}},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	plan, err := planHostLANBridge(state)
	if err != nil {
		t.Fatalf("planHostLANBridge: %v", err)
	}
	if !plan.Ready || plan.Bridge != "vmbr0" || plan.Parent != "eno1" || plan.NeedsPrepare {
		t.Fatalf("plan = %#v, want ready vmbr0 through eno1", plan)
	}
}

func TestPlanHostLANBridgeProposesBr0ForPhysicalDefaultRoute(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{{Name: "eno1", Kind: "ether", OperState: "up", HasHardware: true}},
		routes: []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "192.168.50.1"}},
		addrs:  []vmLANAddress{{Iface: "eno1", Prefix: "192.168.50.22/24", Scope: "global"}},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	plan, err := planHostLANBridge(state)
	if err != nil {
		t.Fatalf("planHostLANBridge: %v", err)
	}
	if plan.Ready || !plan.NeedsPrepare || plan.Bridge != "br0" || plan.Parent != "eno1" {
		t.Fatalf("plan = %#v, want prepare br0 from eno1", plan)
	}
}

func TestPlanHostLANBridgeRejectsVirtualAndWirelessDefaultRoutes(t *testing.T) {
	for _, state := range []fakeVMLANHostState{
		{
			links: []vmLANLink{{Name: "tailscale0", Kind: "tun", OperState: "up"}},
			routes: []vmLANRoute{{Default: true, Iface: "tailscale0", Gateway: ""}},
			renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
		},
		{
			links: []vmLANLink{{Name: "wlan0", Kind: "wlan", OperState: "up", HasHardware: true}},
			routes: []vmLANRoute{{Default: true, Iface: "wlan0", Gateway: "192.168.1.1"}},
			addrs:  []vmLANAddress{{Iface: "wlan0", Prefix: "192.168.1.19/24", Scope: "global"}},
			renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
		},
	} {
		_, err := planHostLANBridge(state)
		if err == nil || !strings.Contains(err.Error(), "no supported LAN uplink") {
			t.Fatalf("planHostLANBridge error = %v, want no supported LAN uplink", err)
		}
	}
}

func TestPlanHostLANBridgeRejectsUnrelatedBr0(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "br0", Kind: "bridge", OperState: "up"},
			{Name: "eno1", Kind: "ether", OperState: "up", HasHardware: true},
		},
		routes: []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "192.168.20.1"}},
		addrs:  []vmLANAddress{{Iface: "eno1", Prefix: "192.168.20.10/24", Scope: "global"}},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	_, err := planHostLANBridge(state)
	if err == nil || !strings.Contains(err.Error(), `br0 already exists but is not the LAN bridge for eno1`) {
		t.Fatalf("planHostLANBridge error = %v, want unrelated br0 rejection", err)
	}
}
```

- [ ] **Step 2: Run the failing planner tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPlanHostLANBridge' -count=1
```

Expected: fail because the new planner types and `planHostLANBridge` do not exist.

- [ ] **Step 3: Add planner types and pure decision logic**

Create `pkg/catch/vm_lan_bridge.go` with these public-to-package types and helpers:

```go
type vmLANLink struct {
	Name        string
	Kind        string
	OperState   string
	Master      string
	HasHardware bool
}

type vmLANRoute struct {
	Default bool
	Iface   string
	Gateway string
	Metric  int
}

type vmLANAddress struct {
	Iface  string
	Prefix string
	Scope  string
}

type vmLANRenderer struct {
	Name      string
	Supported bool
	Reason    string
}

type vmLANBridgePlan struct {
	Ready        bool
	NeedsPrepare bool
	Bridge       string
	Parent       string
	Renderer     vmLANRenderer
	Reason       string
}

type fakeVMLANHostState struct {
	links    []vmLANLink
	routes   []vmLANRoute
	addrs    []vmLANAddress
	renderer vmLANRenderer
}
```

Implement `planHostLANBridge(state fakeVMLANHostState) (vmLANBridgePlan, error)` so it:

```go
func planHostLANBridge(state fakeVMLANHostState) (vmLANBridgePlan, error) {
	defaultRoute, ok := chooseDefaultIPv4Route(state.routes)
	if !ok {
		return vmLANBridgePlan{}, fmt.Errorf("no default IPv4 route found for VM LAN bridge planning")
	}
	links := indexVMLANLinks(state.links)
	link, ok := links[defaultRoute.Iface]
	if !ok {
		return vmLANBridgePlan{}, fmt.Errorf("default route interface %q was not found", defaultRoute.Iface)
	}
	if link.Kind == "bridge" {
		return vmLANBridgePlan{Ready: true, Bridge: link.Name, Renderer: state.renderer, Reason: "default route is already on a bridge"}, nil
	}
	if link.Master != "" {
		master, ok := links[link.Master]
		if ok && master.Kind == "bridge" {
			return vmLANBridgePlan{Ready: true, Bridge: master.Name, Parent: link.Name, Renderer: state.renderer, Reason: "default route interface is attached to a bridge"}, nil
		}
	}
	if !isSupportedVMLANUplink(link, state.addrs) {
		return vmLANBridgePlan{}, fmt.Errorf("no supported LAN uplink found for VM LAN bridge planning; default route uses %q", link.Name)
	}
	if !state.renderer.Supported {
		return vmLANBridgePlan{}, fmt.Errorf("VM LAN bridge preparation is not supported for %s: %s", state.renderer.Name, state.renderer.Reason)
	}
	if existing, ok := links["br0"]; ok && existing.Kind == "bridge" {
		return vmLANBridgePlan{}, fmt.Errorf("br0 already exists but is not the LAN bridge for %s", link.Name)
	}
	return vmLANBridgePlan{
		Ready:        false,
		NeedsPrepare: true,
		Bridge:       "br0",
		Parent:       link.Name,
		Renderer:     state.renderer,
		Reason:       "default route is on a supported physical LAN uplink",
	}, nil
}
```

Keep helper functions deterministic and testable:

```go
func chooseDefaultIPv4Route(routes []vmLANRoute) (vmLANRoute, bool)
func indexVMLANLinks(links []vmLANLink) map[string]vmLANLink
func isSupportedVMLANUplink(link vmLANLink, addrs []vmLANAddress) bool
func vmLANLinkHasRFC1918Address(name string, addrs []vmLANAddress) bool
```

Reject interface kinds or names for `lo`, `docker*`, `br-*`, `veth*`, `tap*`, `tun*`, `tailscale*`, `yvm-*`, `cni*`, `virbr*`, `wlan*`, and `wl*`.

- [ ] **Step 4: Run planner tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPlanHostLANBridge' -count=1
```

Expected: pass.

## Task 2: Add Netplan Networkd Renderer Support

**Files:**
- Create: `pkg/catch/vm_lan_bridge_netplan.go`
- Create: `pkg/catch/vm_lan_bridge_netplan_test.go`

- [ ] **Step 1: Write failing netplan renderer tests**

Add tests covering these exact fixtures:

```go
const eno1DHCPNetplan = `
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      dhcp4: true
      dhcp6: true
      optional: true
`

const eno1StaticNetplan = `
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      addresses:
        - 192.168.50.22/24
      routes:
        - to: default
          via: 192.168.50.1
      nameservers:
        addresses:
          - 192.168.50.1
          - 1.1.1.1
`

func TestRenderVMLANBridgeNetplanMovesDHCPToBridge(t *testing.T) {
	out, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(eno1DHCPNetplan))
	if err != nil {
		t.Fatalf("renderVMLANBridgeNetplan: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"bridges:",
		"br0:",
		"interfaces:",
		"- eno1",
		"dhcp4: true",
		"dhcp6: true",
		"stp: false",
		"forward-delay: 0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered netplan missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "eno1:\n      dhcp4: true") {
		t.Fatalf("uplink still owns dhcp4:\n%s", text)
	}
}

func TestRenderVMLANBridgeNetplanMovesStaticConfigToBridge(t *testing.T) {
	out, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(eno1StaticNetplan))
	if err != nil {
		t.Fatalf("renderVMLANBridgeNetplan: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"addresses:",
		"- 192.168.50.22/24",
		"routes:",
		"via: 192.168.50.1",
		"nameservers:",
		"- 1.1.1.1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered netplan missing %q:\n%s", want, text)
		}
	}
}

func TestRenderVMLANBridgeNetplanRejectsNetworkManager(t *testing.T) {
	_, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(`
network:
  version: 2
  renderer: NetworkManager
  ethernets:
    eno1:
      dhcp4: true
`))
	if err == nil || !strings.Contains(err.Error(), "NetworkManager renderer is not supported") {
		t.Fatalf("error = %v, want NetworkManager unsupported", err)
	}
}
```

- [ ] **Step 2: Run the failing netplan tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRenderVMLANBridgeNetplan' -count=1
```

Expected: fail because `renderVMLANBridgeNetplan` does not exist.

- [ ] **Step 3: Implement netplan parsing and rendering**

Create `pkg/catch/vm_lan_bridge_netplan.go` using `gopkg.in/yaml.v3`. Define small structs that preserve the fields yeet supports:

```go
type netplanDocument struct {
	Network netplanNetwork `yaml:"network"`
}

type netplanNetwork struct {
	Version   int                         `yaml:"version"`
	Renderer string                      `yaml:"renderer,omitempty"`
	Ethernets map[string]netplanIface    `yaml:"ethernets,omitempty"`
	Bridges   map[string]netplanIface    `yaml:"bridges,omitempty"`
}

type netplanIface struct {
	Interfaces []string                 `yaml:"interfaces,omitempty"`
	DHCP4      *bool                    `yaml:"dhcp4,omitempty"`
	DHCP6      *bool                    `yaml:"dhcp6,omitempty"`
	Optional   *bool                    `yaml:"optional,omitempty"`
	Addresses  []string                 `yaml:"addresses,omitempty"`
	Routes     []map[string]interface{} `yaml:"routes,omitempty"`
	Nameservers map[string]interface{}  `yaml:"nameservers,omitempty"`
	MTU        int                      `yaml:"mtu,omitempty"`
	Parameters map[string]interface{}   `yaml:"parameters,omitempty"`
}
```

Implement:

```go
func renderVMLANBridgeNetplan(bridge, parent string, input []byte) ([]byte, error)
func netplanBool(v bool) *bool
func validateNetplanRenderer(renderer string) error
```

The renderer must:
- require `network.version == 2`;
- accept empty renderer or `networkd`;
- reject `NetworkManager` with `NetworkManager renderer is not supported for automatic VM LAN bridge preparation`;
- require the selected parent under `network.ethernets`;
- move `dhcp4`, `dhcp6`, `addresses`, `routes`, `nameservers`, `mtu`, and `optional` from the parent to the bridge;
- leave the parent defined with no addresses/routes/DHCP and with `optional` preserved when set;
- set `bridges.<bridge>.interfaces` to the selected parent;
- set bridge `parameters.stp` to `false` and `parameters.forward-delay` to `0`;
- reject an existing bridge entry with a different `interfaces` list.

- [ ] **Step 4: Run netplan renderer tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRenderVMLANBridgeNetplan' -count=1
```

Expected: pass.

## Task 3: Add Detached Prepare Operation With Rollback

**Files:**
- Create: `pkg/catch/vm_lan_bridge_prepare.go`
- Create: `pkg/catch/vm_lan_bridge_prepare_test.go`

- [ ] **Step 1: Write failing prepare status tests**

Add tests that use a fake file root and fake runner:

```go
func TestPrepareVMLANBridgeWritesReadyStatusAfterValidation(t *testing.T) {
	root := t.TempDir()
	runner := newFakeVMLANBridgeRunner()
	runner.plan = vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}
	runner.netplan = []byte(eno1DHCPNetplan)

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err != nil {
		t.Fatalf("prepareVMLANBridge: %v", err)
	}
	if status.Phase != vmLANBridgePhaseReady || status.Bridge != "br0" || status.Parent != "eno1" {
		t.Fatalf("status = %#v, want ready br0 eno1", status)
	}
	if !runner.scheduledRollback || runner.rollbackCanceled != true || !runner.applied {
		t.Fatalf("runner = %#v, want scheduled rollback, applied, canceled rollback", runner)
	}
}

func TestPrepareVMLANBridgeRollsBackWhenValidationFails(t *testing.T) {
	root := t.TempDir()
	runner := newFakeVMLANBridgeRunner()
	runner.plan = vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}
	runner.netplan = []byte(eno1DHCPNetplan)
	runner.validateErr = errors.New("default route missing")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "default route missing") {
		t.Fatalf("error = %v, want validation failure", err)
	}
	if status.Phase != vmLANBridgePhaseRolledBack || !runner.rolledBack {
		t.Fatalf("status = %#v runner = %#v, want rolled back", status, runner)
	}
}
```

- [ ] **Step 2: Run the failing prepare tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPrepareVMLANBridge' -count=1
```

Expected: fail because prepare types and `prepareVMLANBridge` do not exist.

- [ ] **Step 3: Implement prepare state and operation**

Create these types:

```go
type vmLANBridgePreparePhase string

const (
	vmLANBridgePhasePlanned    vmLANBridgePreparePhase = "planned"
	vmLANBridgePhaseRunning    vmLANBridgePreparePhase = "running"
	vmLANBridgePhaseApplying   vmLANBridgePreparePhase = "applying"
	vmLANBridgePhaseValidating vmLANBridgePreparePhase = "validating"
	vmLANBridgePhaseReady      vmLANBridgePreparePhase = "ready"
	vmLANBridgePhaseSkipped    vmLANBridgePreparePhase = "skipped"
	vmLANBridgePhaseRolledBack vmLANBridgePreparePhase = "rolled-back"
	vmLANBridgePhaseFailed     vmLANBridgePreparePhase = "failed"
)

type vmLANBridgePrepareStatus struct {
	ID        string                  `json:"id"`
	Phase     vmLANBridgePreparePhase `json:"phase"`
	Bridge    string                  `json:"bridge,omitempty"`
	Parent    string                  `json:"parent,omitempty"`
	Message   string                  `json:"message,omitempty"`
	Error     string                  `json:"error,omitempty"`
	StartedAt time.Time              `json:"started_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type vmLANBridgePrepareOptions struct {
	Yes bool
}

type vmLANBridgePrepareRunner interface {
	Plan() (vmLANBridgePlan, error)
	ReadNetplan(parent string) ([]byte, error)
	WriteNetplanBackup(path string, content []byte) error
	WriteNetplanOverlay(path string, content []byte) error
	Generate() error
	Apply() error
	Validate(bridge, parent string) error
	ScheduleRollback(id string, after time.Duration) error
	CancelRollback(id string) error
	Rollback(id string) error
}
```

Implement `prepareVMLANBridge(root string, runner vmLANBridgePrepareRunner, opts vmLANBridgePrepareOptions) (vmLANBridgePrepareStatus, error)` so it:
- stores state under `filepath.Join(root, "host-network", "vm-lan-bridge")`;
- writes `status.json` atomically for every phase change;
- exits ready without mutation when the plan is already ready;
- returns skipped when `opts.Yes` is false and preparation is needed;
- reads the current netplan, writes a timestamped backup, renders `/etc/netplan/99-yeet-vm-lan-bridge.yaml`, and runs `netplan generate` before `netplan apply`;
- schedules rollback before `netplan apply`;
- validates that the default route remains usable and the bridge exists after apply;
- cancels rollback only after validation succeeds;
- rolls back and writes `rolled-back` status when apply or validation fails.

- [ ] **Step 4: Run prepare tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPrepareVMLANBridge' -count=1
```

Expected: pass.

## Task 4: Add Catch Local Commands And Init-Time Bridge Prompting

**Files:**
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`
- Modify: `cmd/catch/vm_prereqs.go`
- Modify: `cmd/catch/vm_prereqs_test.go`

- [ ] **Step 1: Write failing local command tests**

Add `cmd/catch/catch_test.go` cases:

```go
func TestHandleLocalCommandVMLANBridgeStatus(t *testing.T) {
	var out bytes.Buffer
	cfg := &catch.Config{RootDir: t.TempDir()}
	handled, err := handleLocalCommand([]string{"vm-lan-bridge-status"}, cfg, cfg.RootDir, &out)
	if err != nil {
		t.Fatalf("handleLocalCommand vm-lan-bridge-status: %v", err)
	}
	if !handled {
		t.Fatal("vm-lan-bridge-status was not handled")
	}
	if !strings.Contains(out.String(), "VM LAN bridge") {
		t.Fatalf("output = %q, want VM LAN bridge summary", out.String())
	}
}

func TestHandleLocalCommandVMLANBridgePrepareRequiresYes(t *testing.T) {
	var out bytes.Buffer
	cfg := &catch.Config{RootDir: t.TempDir()}
	handled, err := handleLocalCommand([]string{"vm-lan-bridge-prepare"}, cfg, cfg.RootDir, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes is required") {
		t.Fatalf("error = %v, want --yes is required", err)
	}
	if !handled {
		t.Fatal("vm-lan-bridge-prepare was not handled")
	}
}
```

- [ ] **Step 2: Write failing VM prereq prompt tests**

Add `cmd/catch/vm_prereqs_test.go` cases:

```go
func TestSetupVMHostPromptsForLANBridgeWhenVMToolsReady(t *testing.T) {
	var prompted bool
	var prepared bool
	err := setupVMHostWith(vmSetupDeps{
		stderr: io.Discard,
		stat: func(path string) error {
			return nil
		},
		lookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		commandExists: func(name string) bool { return name == "apt-get" },
		getenv: func(name string) string { return "" },
		confirm: func(_ io.Reader, _ io.Writer, msg string) (bool, error) {
			prompted = strings.Contains(msg, "Prepare br0 for VM LAN networking")
			return true, nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}, nil
		},
		prepareLANBridge: func() error {
			prepared = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if !prompted || !prepared {
		t.Fatalf("prompted=%v prepared=%v, want both true", prompted, prepared)
	}
}

func TestSetupVMHostSkipsLANBridgeWhenOperatorDeclines(t *testing.T) {
	var prepared bool
	err := setupVMHostWith(vmSetupDeps{
		stderr: io.Discard,
		stat: func(path string) error { return nil },
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		commandExists: func(name string) bool { return name == "apt-get" },
		getenv: func(name string) string { return "" },
		confirm: func(_ io.Reader, _ io.Writer, _ string) (bool, error) { return false, nil },
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}, nil
		},
		prepareLANBridge: func() error {
			prepared = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if prepared {
		t.Fatal("prepareLANBridge ran after decline")
	}
}
```

- [ ] **Step 3: Run the failing command and prereq tests**

Run:

```bash
mise exec -- go test ./cmd/catch -run 'VMLANBridge|SetupVMHost.*LANBridge' -count=1
```

Expected: fail because command cases and VM setup dependency fields do not exist.

- [ ] **Step 4: Add local command handling**

In `cmd/catch/catch.go`, add cases inside `handleLocalCommand`:

```go
case "vm-lan-bridge-plan":
	return true, handleVMLANBridgePlanCommand(scfg, dataDir, out)
case "vm-lan-bridge-status":
	return true, handleVMLANBridgeStatusCommand(scfg, dataDir, out)
case "vm-lan-bridge-prepare":
	yes := slices.Contains(args[1:], "--yes")
	if !yes {
		return true, fmt.Errorf("vm-lan-bridge-prepare --yes is required because this can momentarily change host networking")
	}
	return true, handleVMLANBridgePrepareCommand(scfg, dataDir, out)
```

Implement the handlers as thin wrappers over `pkg/catch` functions so the command file does not own network logic.

- [ ] **Step 5: Extend VM setup dependencies and prompting**

In `cmd/catch/vm_prereqs.go`, add these dependency fields:

```go
confirm func(io.Reader, io.Writer, string) (bool, error)
planLANBridge func() (vmLANBridgePlan, error)
prepareLANBridge func() error
```

Set defaults in `normalizeVMSetupDeps`. After VM host package checks complete, call a new helper:

```go
func maybePrepareVMLANBridgeDuringSetup(deps vmSetupDeps) error
```

That helper must:
- return nil when `CATCH_SKIP_VM_LAN_BRIDGE=1`;
- prepare without prompting when `CATCH_PREPARE_VM_LAN_BRIDGE=1`;
- return nil when the bridge plan is ready;
- warn and return nil for unsupported planning errors during init;
- ask `Prepare br0 for VM LAN networking?` when interactive confirmation is available and the plan needs preparation;
- call `prepareLANBridge` only after yes.

- [ ] **Step 6: Run catch command and prereq tests**

Run:

```bash
mise exec -- go test ./cmd/catch -run 'VMLANBridge|SetupVMHost.*LANBridge' -count=1
```

Expected: pass.

## Task 5: Make `yeet init` Pass Intent And Survive A Short Network Drop

**Files:**
- Modify: `pkg/yeet/init.go`
- Modify: `pkg/yeet/init_test.go`

- [ ] **Step 1: Write failing remote install command tests**

Add tests in `pkg/yeet/init_test.go`:

```go
func TestRemoteCatchInstallCommandCanRequestVMLANBridgePrep(t *testing.T) {
	args := remoteCatchInstallCommand("root@example.com", false, true, true, "", "", nil, true)
	got := strings.Join(args, " ")
	if !strings.Contains(got, "CATCH_PREPARE_VM_LAN_BRIDGE=1") {
		t.Fatalf("remoteCatchInstallCommand = %q, want CATCH_PREPARE_VM_LAN_BRIDGE=1", got)
	}
}

func TestWaitDetachedInitCatchInstallToleratesBridgePrepReadFailures(t *testing.T) {
	oldRead := readRemoteInitInstallFileFn
	oldStream := streamRemoteInitInstallLogFn
	defer func() {
		readRemoteInitInstallFileFn = oldRead
		streamRemoteInitInstallLogFn = oldStream
	}()
	var reads int
	readRemoteInitInstallFileFn = func(_ string, path string) (string, error) {
		reads++
		if reads < 3 {
			return "", errors.New("ssh: connection reset")
		}
		if strings.HasSuffix(path, ".status") {
			return "0", nil
		}
		return "Preparing VM LAN bridge\nVM LAN bridge ready\n", nil
	}
	streamRemoteInitInstallLogFn = func(_ string, _ initInstallSession, _ *initInstallFilter, lastLog *string) error {
		*lastLog = "Preparing VM LAN bridge\n"
		return nil
	}

	status, err := waitDetachedInitCatchInstall("root@example.com", initInstallSession{LogPath: "/tmp/x.log", StatusPath: "/tmp/x.status"}, newInitInstallFilter(io.Discard))
	if err != nil {
		t.Fatalf("waitDetachedInitCatchInstall: %v", err)
	}
	if status != "0" {
		t.Fatalf("status = %q, want 0", status)
	}
}
```

- [ ] **Step 2: Run failing init tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'RemoteCatchInstallCommandCanRequestVMLANBridgePrep|WaitDetachedInitCatchInstallToleratesBridgePrep' -count=1
```

Expected: fail because the command signature and injectable file readers do not yet support this.

- [ ] **Step 3: Thread bridge prep intent through init**

Change the install helpers from:

```go
remoteCatchInstallCommand(userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags)
```

to:

```go
remoteCatchInstallCommand(userAtRemote, useSudo, installDocker, installVMTools, tsAuthKey, tsClientSecret, tsCatchTags, prepareVMLANBridge)
```

Set `CATCH_PREPARE_VM_LAN_BRIDGE=1` only when the user has confirmed VM host setup and bridge prep during `yeet init`. Keep `CATCH_SKIP_VM_LAN_BRIDGE=1` for an explicit decline so catch install does not reprompt remotely.

- [ ] **Step 4: Add polling seams and retry behavior**

Wrap the existing remote file helpers in package variables:

```go
var readRemoteInitInstallFileFn = readRemoteInitInstallFile
var streamRemoteInitInstallLogFn = streamRemoteInitInstallLog
```

Use those variables inside `waitDetachedInitCatchInstall`. Keep the normal install timeout, but treat repeated SSH read failures as retryable until the timeout expires. When the timeout expires after bridge preparation started, return:

```text
VM LAN bridge preparation may still be finishing; rerun `yeet init <host>` to verify or resume setup
```

- [ ] **Step 5: Run init tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'RemoteCatchInstallCommandCanRequestVMLANBridgePrep|WaitDetachedInitCatchInstallToleratesBridgePrep' -count=1
```

Expected: pass.

## Task 6: Ensure Bridge Before VM LAN Provisioning Creates Artifacts

**Files:**
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_network.go`
- Modify: `pkg/catch/vm_network_test.go`

- [ ] **Step 1: Write failing network resolution tests**

Add tests in `pkg/catch/vm_network_test.go`:

```go
func TestResolveVMLANNetworkInputUsesPreparedHostBridge(t *testing.T) {
	oldResolve := resolveHostVMLANBridgeFn
	defer func() { resolveHostVMLANBridgeFn = oldResolve }()
	resolveHostVMLANBridgeFn = func() (vmLANBridgePlan, error) {
		return vmLANBridgePlan{Ready: true, Bridge: "br0", Parent: "eno1"}, nil
	}

	input := vmNetworkInputs{}
	if err := resolveVMLANNetworkInput(&input); err != nil {
		t.Fatalf("resolveVMLANNetworkInput: %v", err)
	}
	if input.LANParent != "br0" || !input.LANParentIsBridge {
		t.Fatalf("input = %#v, want LANParent br0 bridge", input)
	}
}

func TestResolveVMLANNetworkInputFailsBeforeArtifactsWhenBridgeMissing(t *testing.T) {
	oldResolve := resolveHostVMLANBridgeFn
	defer func() { resolveHostVMLANBridgeFn = oldResolve }()
	resolveHostVMLANBridgeFn = func() (vmLANBridgePlan, error) {
		return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1"}, errVMLANBridgePreparationRequired
	}

	input := vmNetworkInputs{}
	err := resolveVMLANNetworkInput(&input)
	if !errors.Is(err, errVMLANBridgePreparationRequired) {
		t.Fatalf("error = %v, want errVMLANBridgePreparationRequired", err)
	}
}
```

- [ ] **Step 2: Run failing VM network tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'ResolveVMLANNetworkInput.*Bridge' -count=1
```

Expected: fail because `resolveHostVMLANBridgeFn` and `errVMLANBridgePreparationRequired` do not exist.

- [ ] **Step 3: Add bridge resolution hook**

In `pkg/catch/vm_network.go`, define:

```go
var errVMLANBridgePreparationRequired = errors.New("VM LAN bridge preparation required")
var resolveHostVMLANBridgeFn = resolveHostVMLANBridge
```

Implement `resolveHostVMLANBridge() (vmLANBridgePlan, error)` as a read-only planner call. Update `resolveVMLANNetworkInput` so an empty `LANParent` asks the bridge planner for the correct bridge. If the plan is ready, set:

```go
input.LANParent = plan.Bridge
input.LANParentIsBridge = true
```

If the plan needs preparation, return `errVMLANBridgePreparationRequired` wrapped with bridge and parent details.

- [ ] **Step 4: Move run-time ensure before durable VM artifacts**

In `pkg/catch/vm_provision.go`, call a new helper before disk creation, image download, DB service commit, or VM service staging:

```go
func (e *ttyExecer) ensureVMLANBridgeForRun(flags cli.RunFlags) error
```

The helper must:
- return nil when the requested network modes do not include `lan`;
- return nil when the planner reports a ready bridge;
- in an interactive TTY, prompt to prepare `br0` when the planner needs preparation;
- abort with `VM LAN bridge preparation is required before creating VM service artifacts` when the user declines;
- abort with the same message in non-interactive mode unless an explicit catch-side yes option is present;
- call the detached prepare operation and wait for ready status before provisioning continues.

- [ ] **Step 5: Run VM network tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'ResolveVMLANNetworkInput.*Bridge|VMNetworkUnsupportedLAN|DBBackedNonBridge' -count=1
```

Expected: pass after updating old tests so non-bridge legacy state still fails, but a prepared `br0` succeeds.

## Task 7: Keep Reconciliation And Resize Bridge-Backed

**Files:**
- Modify: `pkg/catch/vm_resize.go`
- Modify: `pkg/catch/vm_network_reconcile.go`
- Modify: `pkg/catch/vm_network_test.go`

- [ ] **Step 1: Write failing replay tests**

Add tests in `pkg/catch/vm_network_test.go`:

```go
func TestVMNetworkPlanFromDBReplaysPreparedBridgeLAN(t *testing.T) {
	plan := vmNetworkPlanFromDB("ubuntu", []db.VMNetworkConfig{{
		Mode:   "lan",
		Parent: "br0",
		MAC:    "02:fc:00:00:00:10",
	}})
	if err := plan.validateExecutable(); err != nil {
		t.Fatalf("validateExecutable: %v", err)
	}
	if got := plan.Interfaces[0].Bridge; got != "br0" {
		t.Fatalf("Bridge = %q, want br0", got)
	}
}

func TestVMNetworkCleanupDoesNotDeleteHostBridge(t *testing.T) {
	plan := newVMNetworkPlan("ubuntu", []string{"lan"}, vmNetworkInputs{LANParent: "br0", LANParentIsBridge: true})
	for _, cmd := range plan.CleanupCommands() {
		joined := strings.Join(cmd, " ")
		if strings.Contains(joined, "link delete br0") {
			t.Fatalf("cleanup deletes host bridge: %v", plan.CleanupCommands())
		}
	}
}
```

- [ ] **Step 2: Run failing replay tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'VMNetworkPlanFromDBReplaysPreparedBridgeLAN|VMNetworkCleanupDoesNotDeleteHostBridge' -count=1
```

Expected: fail if DB replay does not mark `br0` as the executable bridge.

- [ ] **Step 3: Update DB replay and reconciliation**

In `pkg/catch/vm_resize.go` and `pkg/catch/vm_network_reconcile.go`:
- treat stored `Parent` values that are bridges as bridge-backed LAN config;
- continue rejecting stored physical parents such as `eno1` with a repair message that says to rerun `yeet init` or `yeet run <service> ... --net=lan` interactively to prepare the bridge;
- never call cleanup commands that remove the prepared host bridge;
- keep VLAN bridge behavior unchanged.

- [ ] **Step 4: Run replay and reconciliation tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'VMNetworkPlanFromDBReplaysPreparedBridgeLAN|VMNetworkCleanupDoesNotDeleteHostBridge|ReconcileVMNetworks' -count=1
```

Expected: pass.

## Task 8: Update User Docs After Behavior Is Implemented

**Files:**
- Modify: `README.md`
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/getting-started/first-run-validation.mdx`
- Modify: `website/docs/changelog.mdx` only if preparing a release in the same execution session

- [ ] **Step 1: Read docs instructions**

Read:

```bash
sed -n '1,240p' .codex/skills/yeet-docs/SKILL.md
sed -n '1,240p' website/AGENTS.md
test -f website/STYLYGUIDE.md && sed -n '1,240p' website/STYLYGUIDE.md
```

Expected: use the website style guide and keep examples homelab-focused without leaking local service names.

- [ ] **Step 2: Update docs copy**

Add docs that say:
- VM `--net=lan` uses a host bridge because Firecracker attaches VM TAP devices to the host network.
- `yeet init` checks VM host readiness and may ask to prepare `br0` when the host has no bridge.
- Existing host bridges such as Proxmox `vmbr0` are reused.
- On supported Ubuntu/Debian netplan-networkd hosts, yeet moves the LAN IP/default route from the physical uplink to `br0`, attaches the uplink to the bridge, validates reachability, and rolls back on failure.
- If the connection drops during bridge apply, rerun `yeet init <host>` or rerun the original `yeet run ... --net=lan`; both paths inspect the saved status and resume or report the final state.
- Unsupported renderers fail before network mutation.

Use harmless homelab examples such as:

```bash
yeet init root@nuc
yeet run homeassistant vm://ubuntu/26.04 --net=lan
yeet run paperless compose.yml --net=svc,ts
```

- [ ] **Step 3: Run docs checks**

Run:

```bash
npm --prefix website run generate:content-audit
```

Expected: pass with no private hostnames, private service names, or stale LAN bridge wording.

## Task 9: Run Targeted And Broad Verification

**Files:**
- No new files.

- [ ] **Step 1: Run focused Go suites**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch ./pkg/yeet -count=1
```

Expected: pass.

- [ ] **Step 2: Run full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: pass.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: pass.

- [ ] **Step 4: Run quality gate for host-network changes**

Run:

```bash
mise run quality
```

Expected: pass. If this is being prepared for a release or the bridge operation touches shared concurrency/RPC paths substantially, run:

```bash
mise run quality:goal
```

Expected: pass.

## Task 10: Live Validation With Explicit Operator Consent

**Files:**
- No repository files unless the validation notes reveal docs or code fixes.

- [ ] **Step 1: Verify existing-bridge behavior on Proxmox**

Use the VM-capable host alias from `AGENTS.local.md`:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run lanbridge-smoke-lab-host vm://ubuntu/26.04 --net=lan --image-policy=cached
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet status
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet rm lanbridge-smoke-lab-host --yes --clean
```

Expected: `yeet run` uses the existing bridge and does not create or alter `br0` on Proxmox.

- [ ] **Step 2: Ask before mutating a fresh Ubuntu/Debian host**

Before applying bridge prep to a fresh NUC or VM, ask the user for the exact host and confirm that yeet may rewrite netplan and briefly interrupt connectivity. Use this wording:

```text
This will prepare br0 on the confirmed host for VM LAN networking, write a netplan overlay, run netplan apply, validate reachability, and roll back on failure. Confirm the exact catch alias and SSH host, then say yes before I run it.
```

- [ ] **Step 3: Validate bridge prep and rerun behavior on the fresh host**

After confirmation, run this command set only when the confirmed values match the actual host. The `yeet-nuc` and `root@nuc` values below are harmless example values for the plan; replace them during execution with the values approved in Step 2 before running any command.

```bash
confirmed_catch_host=yeet-nuc
confirmed_ssh_host=root@nuc
CATCH_HOST="$confirmed_catch_host" mise exec -- go run ./cmd/yeet init "$confirmed_ssh_host"
CATCH_HOST="$confirmed_catch_host" mise exec -- go run ./cmd/yeet run lanbridge-smoke-nuc vm://ubuntu/26.04 --net=lan --image-policy=cached
CATCH_HOST="$confirmed_catch_host" mise exec -- go run ./cmd/yeet ssh lanbridge-smoke-nuc -- true
CATCH_HOST="$confirmed_catch_host" mise exec -- go run ./cmd/yeet rm lanbridge-smoke-nuc --yes --clean
```

Expected: init or run prompts once, the bridge status becomes ready, the VM boots with LAN networking, and cleanup removes only VM/service artifacts.

## Checkpoint And Publication Guidance

- At every task boundary, run `but diff` and verify that only files listed for that task changed.
- Use GitButler for checkpoint commits during execution. Do not use raw `git commit`.
- Do not include unrelated dirty files in any checkpoint.
- Do not push, tag, or update `origin/main` until the user explicitly asks for landing or release work.
- If `website/` changes, commit and push inside the `website/` repository before committing the root submodule pointer.

## Self-Review

- Spec coverage: Tasks cover dynamic uplink selection, existing bridge reuse, default `br0` creation, netplan persistence, rollback, `yeet init` prompting, `yeet run --net=lan` prompting, reconnect/rerun behavior, DB replay, docs, and live validation.
- Unsupported cases are deliberate: NetworkManager, ifupdown, wireless uplinks, ambiguous multiple physical default-route candidates, and unrelated `br0` fail before mutation with specific messages.
- No unresolved markers: this plan avoids open-ended work items and names the files, functions, commands, and expected outcomes needed for execution.
- Type consistency: `vmLANBridgePlan`, `vmLANRenderer`, `vmLANBridgePrepareStatus`, `vmLANBridgePreparePhase`, `prepareVMLANBridge`, and `resolveHostVMLANBridgeFn` are named consistently across the tasks.
