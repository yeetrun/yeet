// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMSvcNetworkPlanUsesHostBridgeAndYeetNSPeer(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{
		ServiceIP: "192.168.100.12",
	})

	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(plan.Interfaces))
	}
	iface := plan.Interfaces[0]
	if iface.Mode != "svc" {
		t.Fatalf("mode = %q, want svc", iface.Mode)
	}
	if iface.Tap == "" {
		t.Fatal("tap is empty")
	}
	if iface.GuestIP != "192.168.100.12/24" {
		t.Fatalf("guest IP = %q, want 192.168.100.12/24", iface.GuestIP)
	}
	if iface.Gateway != "192.168.100.254" {
		t.Fatalf("gateway = %q, want 192.168.100.254", iface.Gateway)
	}
	if iface.MAC == "" {
		t.Fatal("guest MAC is empty")
	}
	if got := plan.FirecrackerInterfaces()[0].GuestMAC; got != iface.MAC {
		t.Fatalf("firecracker guest MAC = %q, want %q", got, iface.MAC)
	}

	cmds := plan.SetupCommands()
	wantPrefix := [][]string{
		{"ip", "link", "add", iface.Bridge, "type", "bridge"},
		{"ip", "tuntap", "add", iface.Tap, "mode", "tap"},
	}
	if len(cmds) < len(wantPrefix) {
		t.Fatalf("setup commands = %#v, want prefix %#v", cmds, wantPrefix)
	}
	if got := cmds[:len(wantPrefix)]; !reflect.DeepEqual(got, wantPrefix) {
		t.Fatalf("setup command prefix = %#v, want %#v", got, wantPrefix)
	}
	if containsCommand(cmds, []string{"ip", "addr", "add", vmSvcGateway + "/24", "dev", iface.Bridge}) {
		t.Fatalf("setup commands = %#v, want no broad service network route on VM bridge", cmds)
	}
	for _, command := range [][]string{
		{"ip", "addr", "add", vmSvcGateway + "/32", "dev", iface.Bridge},
		{"ip", "route", "replace", "192.168.100.12/32", "dev", iface.Bridge, "src", vmSvcGateway},
	} {
		if !containsCommand(cmds, command) {
			t.Fatalf("setup commands = %#v, missing %#v", cmds, command)
		}
	}

	cleanup := plan.CleanupCommands()
	if !containsCommand(cleanup, []string{"ip", "route", "del", "192.168.100.12/32", "dev", iface.Bridge}) {
		t.Fatalf("cleanup commands = %#v, want guest route deletion", cleanup)
	}
}

func TestVMLANNetworkPlanUsesParentBridgeWhenAvailable(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{
		LANParent:         "vmbr0",
		LANParentIsBridge: true,
		LANMAC:            "02:fc:00:00:00:34",
	})

	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(plan.Interfaces))
	}
	iface := plan.Interfaces[0]
	if iface.Mode != "lan" {
		t.Fatalf("mode = %q, want lan", iface.Mode)
	}
	if iface.Tap == "" {
		t.Fatal("tap is empty")
	}
	if iface.Parent != "vmbr0" {
		t.Fatalf("parent = %q, want vmbr0", iface.Parent)
	}
	if !iface.DHCP {
		t.Fatal("DHCP = false, want true")
	}
}

func TestVMLANNetworkPlanBuildsTaggedVLANBridge(t *testing.T) {
	oldDefaultRoute := hostDefaultRouteInterfaceFn
	oldExistingVLANBridge := vmLANExistingVLANBridgeFn
	t.Cleanup(func() {
		hostDefaultRouteInterfaceFn = oldDefaultRoute
		vmLANExistingVLANBridgeFn = oldExistingVLANBridge
	})
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "eth0", nil
	}
	vmLANExistingVLANBridgeFn = func(string, int) (string, bool, error) {
		return "", false, nil
	}

	execer := &ttyExecer{sn: "devbox"}
	plan, err := execer.vmNetworkPlanFromFlags(cli.RunFlags{
		Net:         "lan",
		MacvlanVlan: 4,
	}, nil)
	if err != nil {
		t.Fatalf("vmNetworkPlanFromFlags: %v", err)
	}
	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(plan.Interfaces))
	}
	iface := plan.Interfaces[0]
	if iface.Parent != "eth0" || iface.VLAN != 4 {
		t.Fatalf("lan interface = %#v, want parent eth0 with VLAN 4", iface)
	}
	if iface.Bridge == "" || iface.VLANDevice == "" {
		t.Fatalf("lan interface = %#v, want VLAN bridge and device", iface)
	}

	want := [][]string{
		{"ip", "link", "add", "link", "eth0", "name", iface.VLANDevice, "type", "vlan", "id", "4"},
		{"ip", "link", "add", iface.Bridge, "type", "bridge"},
		{"ip", "link", "set", iface.VLANDevice, "master", iface.Bridge},
		{"ip", "tuntap", "add", iface.Tap, "mode", "tap"},
		{"ip", "link", "set", iface.Tap, "master", iface.Bridge},
	}
	for _, command := range want {
		if !containsCommand(plan.SetupCommands(), command) {
			t.Fatalf("setup commands = %#v, missing %#v", plan.SetupCommands(), command)
		}
	}
	for _, command := range [][]string{
		{"ip", "link", "del", iface.Tap},
		{"ip", "link", "del", iface.VLANDevice},
		{"ip", "link", "del", iface.Bridge},
	} {
		if !containsCommand(plan.CleanupCommands(), command) {
			t.Fatalf("cleanup commands = %#v, missing %#v", plan.CleanupCommands(), command)
		}
	}
}

func TestVMLANNetworkPlanUsesBridgeUplinkForVLANParent(t *testing.T) {
	oldDefaultRoute := hostDefaultRouteInterfaceFn
	oldBridgeUplink := vmLANBridgeUplinkFn
	oldExistingVLANBridge := vmLANExistingVLANBridgeFn
	t.Cleanup(func() {
		hostDefaultRouteInterfaceFn = oldDefaultRoute
		vmLANBridgeUplinkFn = oldBridgeUplink
		vmLANExistingVLANBridgeFn = oldExistingVLANBridge
	})
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "br0", nil
	}
	vmLANBridgeUplinkFn = func(parent string) (string, error) {
		if parent != "br0" {
			t.Fatalf("bridge uplink parent = %q, want br0", parent)
		}
		return "eno1", nil
	}
	vmLANExistingVLANBridgeFn = func(string, int) (string, bool, error) {
		return "", false, nil
	}

	execer := &ttyExecer{sn: "devbox"}
	plan, err := execer.vmNetworkPlanFromFlags(cli.RunFlags{
		Net:         "lan",
		MacvlanVlan: 4,
	}, nil)
	if err != nil {
		t.Fatalf("vmNetworkPlanFromFlags: %v", err)
	}
	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(plan.Interfaces))
	}
	iface := plan.Interfaces[0]
	if iface.Parent != "eno1" {
		t.Fatalf("lan interface parent = %q, want bridge uplink eno1", iface.Parent)
	}
	if !containsCommand(plan.SetupCommands(), []string{"ip", "link", "add", "link", "eno1", "name", iface.VLANDevice, "type", "vlan", "id", "4"}) {
		t.Fatalf("setup commands = %#v, want VLAN device on eno1", plan.SetupCommands())
	}
}

func TestVMLANNetworkPlanUsesExistingVLANBridge(t *testing.T) {
	oldDefaultRoute := hostDefaultRouteInterfaceFn
	oldBridgeUplink := vmLANBridgeUplinkFn
	oldExistingVLANBridge := vmLANExistingVLANBridgeFn
	t.Cleanup(func() {
		hostDefaultRouteInterfaceFn = oldDefaultRoute
		vmLANBridgeUplinkFn = oldBridgeUplink
		vmLANExistingVLANBridgeFn = oldExistingVLANBridge
	})
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "br0", nil
	}
	vmLANBridgeUplinkFn = func(string) (string, error) {
		return "eno1", nil
	}
	vmLANExistingVLANBridgeFn = func(parent string, vlan int) (string, bool, error) {
		if parent != "eno1" || vlan != 4 {
			t.Fatalf("existing VLAN bridge lookup = parent %q vlan %d, want eno1 vlan 4", parent, vlan)
		}
		return "br0v4", true, nil
	}

	execer := &ttyExecer{sn: "devbox"}
	plan, err := execer.vmNetworkPlanFromFlags(cli.RunFlags{
		Net:         "lan",
		MacvlanVlan: 4,
	}, nil)
	if err != nil {
		t.Fatalf("vmNetworkPlanFromFlags: %v", err)
	}
	iface := plan.Interfaces[0]
	if iface.Parent != "br0v4" || iface.Bridge != "br0v4" || iface.VLANDevice != "" {
		t.Fatalf("lan interface = %#v, want existing bridge br0v4 and no owned VLAN device", iface)
	}
	if containsCommandPrefix(plan.SetupCommands(), []string{"ip", "link", "add", "link"}) {
		t.Fatalf("setup commands = %#v, should not create a VLAN device", plan.SetupCommands())
	}
	if !containsCommand(plan.SetupCommands(), []string{"ip", "link", "set", iface.Tap, "master", "br0v4"}) {
		t.Fatalf("setup commands = %#v, want tap attached to br0v4", plan.SetupCommands())
	}
	if containsCommand(plan.CleanupCommands(), []string{"ip", "link", "del", "br0v4"}) {
		t.Fatalf("cleanup commands = %#v, should not delete existing bridge", plan.CleanupCommands())
	}
}

func TestVMLANExistingVLANBridgeFromConfig(t *testing.T) {
	config := strings.NewReader(`VLAN Dev name	 | VLAN ID
Name-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD
enp5s0.4       | 4  | enp5s0
`)
	bridge, ok, err := vmLANExistingVLANBridgeFromConfig("enp5s0", 4, config, func(name string) string {
		if name != "enp5s0.4" {
			t.Fatalf("master lookup device = %q, want enp5s0.4", name)
		}
		return "vmbr0v4"
	})
	if err != nil {
		t.Fatalf("vmLANExistingVLANBridgeFromConfig: %v", err)
	}
	if !ok || bridge != "vmbr0v4" {
		t.Fatalf("bridge = %q ok %v, want vmbr0v4 true", bridge, ok)
	}
}

func TestVMLANBridgeUplinkFromNamesPrefersHardware(t *testing.T) {
	got, err := vmLANBridgeUplinkFromNames([]string{
		"tap100i0",
		"veth1234",
		"bond0",
		"eno1",
		"eno2",
	}, func(name string) bool {
		return name == "eno2" || name == "eno1"
	})
	if err != nil {
		t.Fatalf("vmLANBridgeUplinkFromNames: %v", err)
	}
	if got != "eno1" {
		t.Fatalf("uplink = %q, want sorted hardware candidate eno1", got)
	}
}

func TestVMLANBridgeUplinkFromNamesFallsBackToNonVirtualCandidate(t *testing.T) {
	got, err := vmLANBridgeUplinkFromNames([]string{"tap100i0", "bond0", "yvm-test-l0"}, func(string) bool {
		return false
	})
	if err != nil {
		t.Fatalf("vmLANBridgeUplinkFromNames: %v", err)
	}
	if got != "bond0" {
		t.Fatalf("uplink = %q, want fallback bond0", got)
	}
}

func TestVMLANBridgeUplinkFromNamesRejectsOnlyVirtualCandidates(t *testing.T) {
	_, err := vmLANBridgeUplinkFromNames([]string{"tap100i0", "veth1234", "yvm-test-l0"}, func(string) bool {
		return false
	})
	if err == nil || !strings.Contains(err.Error(), "no suitable bridge uplink") {
		t.Fatalf("vmLANBridgeUplinkFromNames error = %v, want no suitable uplink", err)
	}
}

func TestVMNetworkPlanFromDBRebuildsTaggedVLANDevices(t *testing.T) {
	original := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{
		LANParent: "eth0",
		LANVLAN:   4,
		LANMAC:    "02:fc:00:00:00:34",
	})

	replayed := vmNetworkPlanFromDB("devbox", original.DBNetworks())
	if !reflect.DeepEqual(replayed.SetupCommands(), original.SetupCommands()) {
		t.Fatalf("replayed setup commands = %#v, want %#v", replayed.SetupCommands(), original.SetupCommands())
	}
	if !reflect.DeepEqual(replayed.CleanupCommands(), original.CleanupCommands()) {
		t.Fatalf("replayed cleanup commands = %#v, want %#v", replayed.CleanupCommands(), original.CleanupCommands())
	}
}

func TestVMNetworkPlanFromDBKeepsExistingTaggedBridgeExternal(t *testing.T) {
	plan := vmNetworkPlanFromDB("devbox", []db.VMNetworkConfig{{
		Mode:      "lan",
		Interface: "eth0",
		Tap:       "yvm-d-ea1055-l0",
		Parent:    "vmbr0v4",
		VLAN:      4,
	}})
	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(plan.Interfaces))
	}
	iface := plan.Interfaces[0]
	if iface.Bridge != "vmbr0v4" || iface.VLANDevice != "" {
		t.Fatalf("lan interface = %#v, want external bridge vmbr0v4", iface)
	}
	if containsCommand(plan.CleanupCommands(), []string{"ip", "link", "del", "vmbr0v4"}) {
		t.Fatalf("cleanup commands = %#v, should not delete external bridge", plan.CleanupCommands())
	}
}

func TestVMSvcLANNetworkPlanHasTwoInterfaces(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc,lan"}, vmNetworkInputs{
		ServiceIP:         "192.168.100.12",
		LANParent:         "vmbr0",
		LANParentIsBridge: true,
	})

	if len(plan.Interfaces) != 2 {
		t.Fatalf("interfaces = %d, want 2", len(plan.Interfaces))
	}
	if plan.Interfaces[0].GuestName != "eth0" {
		t.Fatalf("first guest name = %q, want eth0", plan.Interfaces[0].GuestName)
	}
	if plan.Interfaces[1].GuestName != "eth1" {
		t.Fatalf("second guest name = %q, want eth1", plan.Interfaces[1].GuestName)
	}
}

func TestVMNetworkPlanRejectsUnsupportedMode(t *testing.T) {
	execer := &ttyExecer{sn: "devbox"}
	_, err := execer.vmNetworkPlanFromFlags(cli.RunFlags{Net: "ts"}, nil)
	if err == nil || !strings.Contains(err.Error(), `unsupported VM network mode "ts"`) {
		t.Fatalf("vmNetworkPlanFromFlags error = %v, want unsupported ts", err)
	}
}

func TestVMNetworkDeviceNamesAreCollisionResistantAndShort(t *testing.T) {
	left := newVMNetworkPlan("devbox-a", []string{"svc", "lan"}, vmNetworkInputs{LANParent: "vmbr0", LANParentIsBridge: true})
	right := newVMNetworkPlan("devbox-b", []string{"svc", "lan"}, vmNetworkInputs{LANParent: "vmbr0", LANParentIsBridge: true})

	leftNames := vmNetworkDeviceNames(left)
	rightNames := vmNetworkDeviceNames(right)
	for name := range leftNames {
		if rightNames[name] {
			t.Fatalf("device name %q reused between %#v and %#v", name, leftNames, rightNames)
		}
	}
	for name := range leftNames {
		if len(name) > 15 {
			t.Fatalf("device name %q length = %d, want <= 15", name, len(name))
		}
	}
	for name := range rightNames {
		if len(name) > 15 {
			t.Fatalf("device name %q length = %d, want <= 15", name, len(name))
		}
	}
}

func TestVMNetworkExecuteSetupToleratesAlreadyExistingLinks(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	short := shortVMName("devbox")
	nsPeer := "yvm-" + short + "-n0"
	err := plan.ExecuteSetup(func(cmd []string) error {
		switch {
		case reflect.DeepEqual(cmd[:4], []string{"ip", "link", "add", plan.Interfaces[0].Bridge}):
			return errors.New("RTNETLINK answers: File exists")
		case reflect.DeepEqual(cmd[:4], []string{"ip", "tuntap", "add", plan.Interfaces[0].Tap}):
			return errors.New("ioctl(TUNSETIFF): Device or resource busy")
		case reflect.DeepEqual(cmd[:4], []string{"ip", "addr", "add", vmSvcGateway + "/24"}):
			return errors.New("RTNETLINK answers: File exists")
		case reflect.DeepEqual(cmd, []string{"ip", "link", "set", nsPeer, "netns", vmSvcNetNS}):
			return errors.New("Cannot find device")
		default:
			return nil
		}
	})
	if err != nil {
		t.Fatalf("ExecuteSetup: %v", err)
	}
}

func TestVMNetworkExecuteSetupToleratesAlreadyAssignedAddress(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	err := plan.ExecuteSetup(func(cmd []string) error {
		if reflect.DeepEqual(cmd[:4], []string{"ip", "addr", "add", vmSvcGateway + "/24"}) {
			return errors.New("Error: ipv4: Address already assigned.")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteSetup: %v", err)
	}
}

func TestVMNetworkExecuteCleanupToleratesMissingLinks(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	if err := plan.ExecuteCleanup(func([]string) error {
		return errors.New("Cannot find device")
	}); err != nil {
		t.Fatalf("ExecuteCleanup: %v", err)
	}
}

func TestVMNetworkUnsupportedLANIsExplicit(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "eth0"})
	if err := plan.ExecuteSetup(func([]string) error {
		t.Fatal("runner should not be called for unsupported LAN parent")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "non-bridge LAN parents are unsupported") {
		t.Fatalf("ExecuteSetup error = %v, want unsupported LAN parent", err)
	}

	cmds := plan.SetupCommands()
	if len(cmds) != 1 || cmds[0][0] != "yeet-vm-network-unsupported" {
		t.Fatalf("SetupCommands = %#v, want explicit unsupported command", cmds)
	}
}

func vmNetworkDeviceNames(plan vmNetworkPlan) map[string]bool {
	names := make(map[string]bool)
	for _, iface := range plan.Interfaces {
		for _, name := range []string{iface.Tap, iface.Bridge} {
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
