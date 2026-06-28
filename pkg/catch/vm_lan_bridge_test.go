// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestPlanHostLANBridgeUsesExistingDefaultRouteBridge(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "br0", Kind: "bridge", OperState: "up"},
		},
		routes:   []vmLANRoute{{Default: true, Iface: "br0", Gateway: "192.168.1.1"}},
		addrs:    []vmLANAddress{{Iface: "br0", Prefix: "192.168.1.44/24", Scope: "global"}},
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
		routes:   []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "10.0.0.1"}},
		addrs:    []vmLANAddress{{Iface: "vmbr0", Prefix: "10.0.0.20/24", Scope: "global"}},
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
		links:    []vmLANLink{{Name: "eno1", Kind: "ether", OperState: "up", HasHardware: true}},
		routes:   []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "192.168.50.1"}},
		addrs:    []vmLANAddress{{Iface: "eno1", Prefix: "192.168.50.22/24", Scope: "global"}},
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
			links:    []vmLANLink{{Name: "tailscale0", Kind: "tun", OperState: "up"}},
			routes:   []vmLANRoute{{Default: true, Iface: "tailscale0", Gateway: ""}},
			renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
		},
		{
			links:    []vmLANLink{{Name: "wlan0", Kind: "wlan", OperState: "up", HasHardware: true}},
			routes:   []vmLANRoute{{Default: true, Iface: "wlan0", Gateway: "192.168.1.1"}},
			addrs:    []vmLANAddress{{Iface: "wlan0", Prefix: "192.168.1.19/24", Scope: "global"}},
			renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
		},
	} {
		_, err := planHostLANBridge(state)
		if err == nil || !strings.Contains(err.Error(), "no supported LAN uplink") {
			t.Fatalf("planHostLANBridge error = %v, want no supported LAN uplink", err)
		}
	}
}

func TestPlanHostLANBridgeRejectsBondAndVLANDefaultRoutes(t *testing.T) {
	for _, state := range []fakeVMLANHostState{
		{
			links:    []vmLANLink{{Name: "bond0", Kind: "bond", OperState: "up", HasHardware: true}},
			routes:   []vmLANRoute{{Default: true, Iface: "bond0", Gateway: "192.168.20.1"}},
			addrs:    []vmLANAddress{{Iface: "bond0", Prefix: "192.168.20.10/24", Scope: "global"}},
			renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
		},
		{
			links:    []vmLANLink{{Name: "eno1.4", Kind: "vlan", OperState: "up", HasHardware: true}},
			routes:   []vmLANRoute{{Default: true, Iface: "eno1.4", Gateway: "192.168.4.1"}},
			addrs:    []vmLANAddress{{Iface: "eno1.4", Prefix: "192.168.4.10/24", Scope: "global"}},
			renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
		},
	} {
		_, err := planHostLANBridge(state)
		if err == nil || !strings.Contains(err.Error(), "no supported LAN uplink") {
			t.Fatalf("planHostLANBridge error = %v, want no supported LAN uplink", err)
		}
	}
}

func TestPlanHostLANBridgeRejectsAmbiguousDefaultRoutes(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "eno1", Kind: "ether", OperState: "up", HasHardware: true},
			{Name: "enp2s0", Kind: "ether", OperState: "up", HasHardware: true},
		},
		routes: []vmLANRoute{
			{Default: true, Iface: "eno1", Gateway: "192.168.20.1", Metric: 100},
			{Default: true, Iface: "enp2s0", Gateway: "192.168.30.1", Metric: 100},
		},
		addrs: []vmLANAddress{
			{Iface: "eno1", Prefix: "192.168.20.10/24", Scope: "global"},
			{Iface: "enp2s0", Prefix: "192.168.30.10/24", Scope: "global"},
		},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	_, err := planHostLANBridge(state)
	if err == nil || !strings.Contains(err.Error(), "ambiguous default IPv4 routes") {
		t.Fatalf("planHostLANBridge error = %v, want ambiguous default IPv4 routes", err)
	}
}

func TestPlanHostLANBridgePreparesExistingEmptyBr0Bridge(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "br0", Kind: "bridge", OperState: "down", HasHardware: true},
			{Name: "eno1", Kind: "ether", OperState: "up", HasHardware: true},
		},
		routes:   []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "192.168.20.1"}},
		addrs:    []vmLANAddress{{Iface: "eno1", Prefix: "192.168.20.10/24", Scope: "global"}},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	plan, err := planHostLANBridge(state)
	if err != nil {
		t.Fatalf("planHostLANBridge: %v", err)
	}
	if plan.Ready || !plan.NeedsPrepare || plan.Bridge != "br0" || plan.Parent != "eno1" {
		t.Fatalf("plan = %#v, want prepare existing empty br0 from eno1", plan)
	}
}

func TestPlanHostLANBridgeRejectsUnrelatedBr0(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "br0", Kind: "bridge", OperState: "up"},
			{Name: "eno1", Kind: "ether", OperState: "up", HasHardware: true},
		},
		routes:   []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "192.168.20.1"}},
		addrs:    []vmLANAddress{{Iface: "br0", Prefix: "192.168.99.10/24", Scope: "global"}, {Iface: "eno1", Prefix: "192.168.20.10/24", Scope: "global"}},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	_, err := planHostLANBridge(state)
	if err == nil || !strings.Contains(err.Error(), `br0 already exists but is not the LAN bridge for eno1`) {
		t.Fatalf("planHostLANBridge error = %v, want unrelated br0 rejection", err)
	}
}

func TestPlanHostLANBridgeRejectsExistingNonBridgeBr0(t *testing.T) {
	state := fakeVMLANHostState{
		links: []vmLANLink{
			{Name: "br0", Kind: "ether", OperState: "down", HasHardware: true},
			{Name: "eno1", Kind: "ether", OperState: "up", HasHardware: true},
		},
		routes:   []vmLANRoute{{Default: true, Iface: "eno1", Gateway: "192.168.20.1"}},
		addrs:    []vmLANAddress{{Iface: "eno1", Prefix: "192.168.20.10/24", Scope: "global"}},
		renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true},
	}

	_, err := planHostLANBridge(state)
	if err == nil || !strings.Contains(err.Error(), `br0 already exists but is not the LAN bridge for eno1`) {
		t.Fatalf("planHostLANBridge error = %v, want existing non-bridge br0 rejection", err)
	}
}

func TestPlanVMLANBridgeDiscoversPhysicalDefaultRouteAndProposesBr0(t *testing.T) {
	commands := stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"eno1","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether"}
		]`),
		route: []byte(`[
			{"dst":"default","gateway":"192.168.50.1","dev":"eno1","metric":100}
		]`),
		address: []byte(`[
			{"ifname":"eno1","addr_info":[{"family":"inet","local":"192.168.50.22","prefixlen":24,"scope":"global"}]}
		]`),
		netplan: map[string][]byte{"/etc/netplan/50-cloud-init.yaml": []byte(eno1DHCPNetplan)},
	})

	plan, err := PlanVMLANBridge(t.TempDir())
	if err != nil {
		t.Fatalf("PlanVMLANBridge: %v", err)
	}
	if plan.Ready || !plan.NeedsPrepare || plan.Bridge != "br0" || plan.Parent != "eno1" {
		t.Fatalf("plan = %#v, want prepare br0 from eno1", plan)
	}
	if plan.Renderer.Name != "netplan-networkd" || !plan.Renderer.Supported {
		t.Fatalf("renderer = %#v, want supported netplan-networkd", plan.Renderer)
	}
	for _, want := range []string{
		"ip -details -json link show",
		"ip -json route show default",
		"ip -json address show",
	} {
		if !slices.Contains(*commands, want) {
			t.Fatalf("commands = %#v, missing %q", *commands, want)
		}
	}
}

func TestDiscoverVMLANLinksRequestsDetailsForBridgeKind(t *testing.T) {
	commands := stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"br0","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether","linkinfo":{"info_kind":"bridge"}}
		]`),
	})

	links, err := discoverVMLANLinks()
	if err != nil {
		t.Fatalf("discoverVMLANLinks: %v", err)
	}
	if len(links) != 1 || links[0].Kind != "bridge" {
		t.Fatalf("links = %#v, want br0 bridge", links)
	}
	if !slices.Contains(*commands, "ip -details -json link show") {
		t.Fatalf("commands = %#v, want detailed ip link probe", *commands)
	}
}

func TestPlanVMLANBridgeReusesExistingBridgeWithoutNetplan(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"br0","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether","linkinfo":{"info_kind":"bridge"}}
		]`),
		route: []byte(`[
			{"dst":"default","gateway":"192.168.50.1","dev":"br0","metric":100}
		]`),
		address: []byte(`[
			{"ifname":"br0","addr_info":[{"family":"inet","local":"192.168.50.22","prefixlen":24,"scope":"global"}]}
		]`),
	})

	plan, err := PlanVMLANBridge(t.TempDir())
	if err != nil {
		t.Fatalf("PlanVMLANBridge: %v", err)
	}
	if !plan.Ready || plan.NeedsPrepare || plan.Bridge != "br0" {
		t.Fatalf("plan = %#v, want ready br0", plan)
	}
	if plan.Renderer.Supported {
		t.Fatalf("renderer = %#v, want bridge-ready plan to tolerate unavailable netplan", plan.Renderer)
	}
}

func TestPlanVMLANBridgeRejectsPhysicalDefaultRouteWithoutNetplan(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"eno1","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether"}
		]`),
		route: []byte(`[
			{"dst":"default","gateway":"192.168.50.1","dev":"eno1","metric":100}
		]`),
		address: []byte(`[
			{"ifname":"eno1","addr_info":[{"family":"inet","local":"192.168.50.22","prefixlen":24,"scope":"global"}]}
		]`),
	})

	_, err := PlanVMLANBridge(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no supported netplan networkd config") {
		t.Fatalf("PlanVMLANBridge error = %v, want no supported netplan networkd config", err)
	}
}

func TestPlanVMLANBridgeRejectsPhysicalDefaultRouteWithNetworkManager(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"eno1","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether"}
		]`),
		route: []byte(`[
			{"dst":"default","gateway":"192.168.50.1","dev":"eno1","metric":100}
		]`),
		address: []byte(`[
			{"ifname":"eno1","addr_info":[{"family":"inet","local":"192.168.50.22","prefixlen":24,"scope":"global"}]}
		]`),
		netplan: map[string][]byte{"/etc/netplan/01-network-manager.yaml": []byte(`
network:
  version: 2
  renderer: NetworkManager
  ethernets:
    eno1:
      dhcp4: true
`)},
	})

	_, err := PlanVMLANBridge(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "NetworkManager") {
		t.Fatalf("PlanVMLANBridge error = %v, want NetworkManager unsupported", err)
	}
}

func TestPlanVMLANBridgeRejectsDuplicateNetplanParentDefinitions(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"eno1","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether"}
		]`),
		route: []byte(`[
			{"dst":"default","gateway":"192.168.50.1","dev":"eno1","metric":100}
		]`),
		address: []byte(`[
			{"ifname":"eno1","addr_info":[{"family":"inet","local":"192.168.50.22","prefixlen":24,"scope":"global"}]}
		]`),
		netplan: map[string][]byte{
			"/etc/netplan/50-cloud-init.yaml": []byte(eno1DHCPNetplan),
			"/run/netplan/90-runtime.yaml": []byte(`
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      dhcp4: true
`),
		},
	})

	_, err := PlanVMLANBridge(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "multiple supported netplan configs define network.ethernets.eno1") {
		t.Fatalf("PlanVMLANBridge error = %v, want duplicate parent rejection", err)
	}
}

func TestPlanVMLANBridgeRejectsUnsupportedNetplanParentDefinition(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"eno1","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether"}
		]`),
		route: []byte(`[
			{"dst":"default","gateway":"192.168.50.1","dev":"eno1","metric":100}
		]`),
		address: []byte(`[
			{"ifname":"eno1","addr_info":[{"family":"inet","local":"192.168.50.22","prefixlen":24,"scope":"global"}]}
		]`),
		netplan: map[string][]byte{
			"/etc/netplan/50-cloud-init.yaml": []byte(eno1DHCPNetplan),
			"/etc/netplan/90-unsupported.yaml": []byte(`
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      gateway4: 192.168.50.1
`),
		},
	})

	_, err := PlanVMLANBridge(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsupported netplan config defines network.ethernets.eno1") {
		t.Fatalf("PlanVMLANBridge error = %v, want unsupported parent rejection", err)
	}
}

type vmLANBridgeCommandFixtures struct {
	link    []byte
	route   []byte
	address []byte
	netplan map[string][]byte
}

func stubVMLANBridgeSystem(t *testing.T, fixtures vmLANBridgeCommandFixtures) *[]string {
	t.Helper()
	oldCommandOutput := vmLANBridgeCommandOutputFn
	oldGlob := vmLANBridgeNetplanGlobFn
	oldReadFile := vmLANBridgeReadFileFn
	commands := []string{}
	t.Cleanup(func() {
		vmLANBridgeCommandOutputFn = oldCommandOutput
		vmLANBridgeNetplanGlobFn = oldGlob
		vmLANBridgeReadFileFn = oldReadFile
	})
	vmLANBridgeCommandOutputFn = func(name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		commands = append(commands, key)
		switch key {
		case "ip -details -json link show", "ip -json link show":
			return fixtures.link, nil
		case "ip -json route show default":
			return fixtures.route, nil
		case "ip -json address show":
			return fixtures.address, nil
		default:
			return nil, fmt.Errorf("unexpected command %q", key)
		}
	}
	vmLANBridgeNetplanGlobFn = func(pattern string) ([]string, error) {
		if pattern != "/etc/netplan/*.yaml" && pattern != "/run/netplan/*.yaml" && pattern != "/lib/netplan/*.yaml" {
			return nil, fmt.Errorf("unexpected glob %q", pattern)
		}
		paths := make([]string, 0, len(fixtures.netplan))
		for path := range fixtures.netplan {
			if vmLANBridgeNetplanPathMatchesGlob(path, pattern) {
				paths = append(paths, path)
			}
		}
		slices.Sort(paths)
		return paths, nil
	}
	vmLANBridgeReadFileFn = func(path string) ([]byte, error) {
		raw, ok := fixtures.netplan[path]
		if !ok {
			return nil, fmt.Errorf("unexpected read %q", path)
		}
		return append([]byte(nil), raw...), nil
	}
	return &commands
}

func vmLANBridgeNetplanPathMatchesGlob(path, pattern string) bool {
	switch pattern {
	case "/etc/netplan/*.yaml":
		return strings.HasPrefix(path, "/etc/netplan/") && strings.HasSuffix(path, ".yaml")
	case "/run/netplan/*.yaml":
		return strings.HasPrefix(path, "/run/netplan/") && strings.HasSuffix(path, ".yaml")
	case "/lib/netplan/*.yaml":
		return strings.HasPrefix(path, "/lib/netplan/") && strings.HasSuffix(path, ".yaml")
	default:
		return false
	}
}
