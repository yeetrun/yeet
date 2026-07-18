// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"slices"
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
	if iface.Gateway != vmSvcGuestGateway {
		t.Fatalf("gateway = %q, want %s", iface.Gateway, vmSvcGuestGateway)
	}
	if vmSvcGuestGateway != "192.168.100.1" {
		t.Fatalf("vmSvcGuestGateway = %q, want 192.168.100.1", vmSvcGuestGateway)
	}
	if vmSvcBridgeAddress != "192.168.100.254" {
		t.Fatalf("vmSvcBridgeAddress = %q, want 192.168.100.254", vmSvcBridgeAddress)
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
	if containsCommand(cmds, []string{"ip", "addr", "add", vmSvcBridgeAddress + "/24", "dev", iface.Bridge}) {
		t.Fatalf("setup commands = %#v, want no broad service network route on VM bridge", cmds)
	}
	if containsCommand(cmds, []string{"ip", "addr", "add", vmSvcBridgeAddress + "/32", "dev", iface.Bridge}) {
		t.Fatalf("setup commands = %#v, want service gateway only in %s", cmds, vmSvcNetNS)
	}
	if containsCommand(cmds, []string{"ip", "route", "replace", "192.168.100.12/32", "dev", iface.Bridge, "src", vmSvcBridgeAddress}) {
		t.Fatalf("setup commands = %#v, want host route through %s", cmds, vmSvcNetNS)
	}
	for _, command := range [][]string{
		{"ip", "addr", "del", vmSvcBridgeAddress + "/24", "dev", iface.Bridge},
		{"ip", "addr", "del", vmSvcBridgeAddress + "/32", "dev", iface.Bridge},
		{"ip", "route", "del", "192.168.100.12/32", "dev", iface.Bridge},
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

func TestVMNetworkPlanWithTapOwnerDelegatesEveryTap(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc", "lan"}, vmNetworkInputs{
		ServiceIP:         "192.168.100.12",
		LANParent:         "vmbr0",
		LANParentIsBridge: true,
	}).WithTapOwner(vmRuntimeIdentity{UID: 812, GID: 813})

	seen := 0
	for _, command := range plan.SetupCommands() {
		if len(command) < 3 || !reflect.DeepEqual(command[:3], []string{"ip", "tuntap", "add"}) {
			continue
		}
		seen++
		if !reflect.DeepEqual(command[len(command)-4:], []string{"user", "812", "group", "813"}) {
			t.Fatalf("tap command = %#v, want delegated UID/GID", command)
		}
	}
	if seen != len(plan.Interfaces) {
		t.Fatalf("delegated tap commands = %d, want %d", seen, len(plan.Interfaces))
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
	if containsCommand(plan.CleanupCommands(), []string{"ip", "link", "del", "vmbr0"}) {
		t.Fatalf("cleanup commands = %#v, should not delete host bridge", plan.CleanupCommands())
	}
}

func TestResolveVMLANNetworkInputUsesPreparedHostBridge(t *testing.T) {
	oldResolve := resolveHostVMLANBridgeFn
	t.Cleanup(func() { resolveHostVMLANBridgeFn = oldResolve })
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
	t.Cleanup(func() { resolveHostVMLANBridgeFn = oldResolve })
	resolveHostVMLANBridgeFn = func() (vmLANBridgePlan, error) {
		return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1"}, errVMLANBridgePreparationRequired
	}

	input := vmNetworkInputs{}
	err := resolveVMLANNetworkInput(&input)
	if !errors.Is(err, errVMLANBridgePreparationRequired) {
		t.Fatalf("error = %v, want errVMLANBridgePreparationRequired", err)
	}
}

func TestVMLANNetworkPlanBuildsTaggedVLANBridge(t *testing.T) {
	oldResolve := resolveHostVMLANBridgeFn
	oldBridgeUplink := vmLANBridgeUplinkFn
	oldExistingVLANBridge := vmLANExistingVLANBridgeFn
	t.Cleanup(func() {
		resolveHostVMLANBridgeFn = oldResolve
		vmLANBridgeUplinkFn = oldBridgeUplink
		vmLANExistingVLANBridgeFn = oldExistingVLANBridge
	})
	resolveHostVMLANBridgeFn = func() (vmLANBridgePlan, error) {
		return vmLANBridgePlan{Ready: true, Bridge: "br0", Parent: "eth0"}, nil
	}
	vmLANBridgeUplinkFn = func(parent string) (string, error) {
		if parent != "br0" {
			t.Fatalf("bridge uplink parent = %q, want br0", parent)
		}
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
	} {
		if !containsCommand(plan.CleanupCommands(), command) {
			t.Fatalf("cleanup commands = %#v, missing %#v", plan.CleanupCommands(), command)
		}
	}
	for _, command := range [][]string{
		{"ip", "link", "del", iface.VLANDevice},
		{"ip", "link", "del", iface.Bridge},
	} {
		if containsCommand(plan.CleanupCommands(), command) {
			t.Fatalf("cleanup commands = %#v, should not delete shared VLAN link %#v", plan.CleanupCommands(), command)
		}
	}
}

func TestVMLANNetworkPlanSharesGeneratedTaggedVLANBridge(t *testing.T) {
	left := newVMNetworkPlan("devbox-a", []string{"lan"}, vmNetworkInputs{
		LANParent: "eth0",
		LANVLAN:   4,
	})
	right := newVMNetworkPlan("devbox-b", []string{"lan"}, vmNetworkInputs{
		LANParent: "eth0",
		LANVLAN:   4,
	})
	otherVLAN := newVMNetworkPlan("devbox-c", []string{"lan"}, vmNetworkInputs{
		LANParent: "eth0",
		LANVLAN:   5,
	})

	leftIface := left.Interfaces[0]
	rightIface := right.Interfaces[0]
	if leftIface.Tap == rightIface.Tap {
		t.Fatalf("tap = %q for both VMs, want per-VM taps", leftIface.Tap)
	}
	if leftIface.Bridge == "" || leftIface.VLANDevice == "" {
		t.Fatalf("left LAN interface = %#v, want generated bridge and VLAN device", leftIface)
	}
	if leftIface.Bridge != rightIface.Bridge || leftIface.VLANDevice != rightIface.VLANDevice {
		t.Fatalf("left LAN = %#v, right LAN = %#v, want shared bridge and VLAN device", leftIface, rightIface)
	}
	if leftIface.Bridge == otherVLAN.Interfaces[0].Bridge || leftIface.VLANDevice == otherVLAN.Interfaces[0].VLANDevice {
		t.Fatalf("VLAN 4 LAN = %#v, VLAN 5 LAN = %#v, want different shared links", leftIface, otherVLAN.Interfaces[0])
	}
	for _, name := range []string{leftIface.Bridge, leftIface.VLANDevice} {
		if len(name) > 15 {
			t.Fatalf("generated VLAN link %q length = %d, want <= 15", name, len(name))
		}
	}
	if !containsCommand(left.SetupCommands(), []string{"ip", "link", "add", "link", "eth0", "name", leftIface.VLANDevice, "type", "vlan", "id", "4"}) {
		t.Fatalf("setup commands = %#v, want shared VLAN device add", left.SetupCommands())
	}
	if !containsCommand(right.SetupCommands(), []string{"ip", "link", "set", rightIface.Tap, "master", leftIface.Bridge}) {
		t.Fatalf("right setup commands = %#v, want tap attached to shared bridge %q", right.SetupCommands(), leftIface.Bridge)
	}
	if containsCommand(left.CleanupCommands(), []string{"ip", "link", "del", leftIface.VLANDevice}) ||
		containsCommand(left.CleanupCommands(), []string{"ip", "link", "del", leftIface.Bridge}) {
		t.Fatalf("cleanup commands = %#v, should not delete shared VLAN links", left.CleanupCommands())
	}
}

func TestVMLANNetworkPlanUsesBridgeUplinkForVLANParent(t *testing.T) {
	oldResolve := resolveHostVMLANBridgeFn
	oldBridgeUplink := vmLANBridgeUplinkFn
	oldExistingVLANBridge := vmLANExistingVLANBridgeFn
	t.Cleanup(func() {
		resolveHostVMLANBridgeFn = oldResolve
		vmLANBridgeUplinkFn = oldBridgeUplink
		vmLANExistingVLANBridgeFn = oldExistingVLANBridge
	})
	resolveHostVMLANBridgeFn = func() (vmLANBridgePlan, error) {
		return vmLANBridgePlan{Ready: true, Bridge: "br0", Parent: "eno1"}, nil
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
	oldResolve := resolveHostVMLANBridgeFn
	oldBridgeUplink := vmLANBridgeUplinkFn
	oldExistingVLANBridge := vmLANExistingVLANBridgeFn
	t.Cleanup(func() {
		resolveHostVMLANBridgeFn = oldResolve
		vmLANBridgeUplinkFn = oldBridgeUplink
		vmLANExistingVLANBridgeFn = oldExistingVLANBridge
	})
	resolveHostVMLANBridgeFn = func() (vmLANBridgePlan, error) {
		return vmLANBridgePlan{Ready: true, Bridge: "br0", Parent: "eno1"}, nil
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

func TestVMLANNetworkPlanKeepsGeneratedVLANParentWhenSharedBridgeExists(t *testing.T) {
	oldExistingVLANBridge := vmLANExistingVLANBridgeFn
	t.Cleanup(func() { vmLANExistingVLANBridgeFn = oldExistingVLANBridge })
	vmLANExistingVLANBridgeFn = func(parent string, vlan int) (string, bool, error) {
		if parent != "eth0" || vlan != 4 {
			t.Fatalf("existing VLAN bridge lookup = parent %q vlan %d, want eth0 vlan 4", parent, vlan)
		}
		return vmGeneratedVLANBridgeName(parent, vlan), true, nil
	}

	execer := &ttyExecer{sn: "devbox"}
	plan, err := execer.vmNetworkPlanFromFlags(cli.RunFlags{
		Net:           "lan",
		MacvlanParent: "eth0",
		MacvlanVlan:   4,
	}, nil)
	if err != nil {
		t.Fatalf("vmNetworkPlanFromFlags: %v", err)
	}
	iface := plan.Interfaces[0]
	if iface.Parent != "eth0" || iface.Bridge != vmGeneratedVLANBridgeName("eth0", 4) || iface.VLANDevice != vmGeneratedVLANDeviceName("eth0", 4) {
		t.Fatalf("lan interface = %#v, want generated VLAN links on parent eth0", iface)
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

func TestVMNetworkPlanFromDBReplaysPreparedBridgeLAN(t *testing.T) {
	plan := vmNetworkPlanFromDB("devbox", []db.VMNetworkConfig{{
		Mode:      "lan",
		Interface: "eth0",
		Tap:       "yvm-devbox-l0",
		Parent:    "br0",
		MAC:       "02:fc:00:00:00:34",
	}})
	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(plan.Interfaces))
	}
	iface := plan.Interfaces[0]
	if iface.Parent != "br0" || iface.Bridge != "br0" || iface.VLANDevice != "" {
		t.Fatalf("lan interface = %#v, want prepared bridge br0", iface)
	}
	if !iface.DHCP {
		t.Fatal("DHCP = false, want true")
	}
	if containsCommand(plan.SetupCommands(), []string{"ip", "link", "add", "br0", "type", "bridge"}) {
		t.Fatalf("setup commands = %#v, should not create prepared host bridge", plan.SetupCommands())
	}
	if containsCommand(plan.CleanupCommands(), []string{"ip", "link", "del", "br0"}) {
		t.Fatalf("cleanup commands = %#v, should not delete prepared host bridge", plan.CleanupCommands())
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

func TestVMNetworkPlanFromDBRebuildsDisappearedDerivedVLANBridge(t *testing.T) {
	oldBridgeUplink := vmLANBridgeUplinkFn
	oldLiveBridgeExists := vmLANLiveBridgeExistsFn
	t.Cleanup(func() {
		vmLANBridgeUplinkFn = oldBridgeUplink
		vmLANLiveBridgeExistsFn = oldLiveBridgeExists
	})
	vmLANLiveBridgeExistsFn = func(bridge string) bool {
		return bridge == "vmbr0"
	}
	vmLANBridgeUplinkFn = func(parent string) (string, error) {
		if parent != "vmbr0" {
			t.Fatalf("bridge uplink parent = %q, want vmbr0", parent)
		}
		return "enp5s0", nil
	}

	plan := vmNetworkPlanFromDB("tyler-exit-node", []db.VMNetworkConfig{{
		Mode:      "lan",
		Interface: "eth0",
		Tap:       "yvm-t-c89e9b-l0",
		Parent:    "vmbr0v4",
		VLAN:      4,
	}})
	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(plan.Interfaces))
	}
	iface := plan.Interfaces[0]
	if iface.Parent != "enp5s0" || iface.Bridge == "vmbr0v4" || iface.VLANDevice == "" {
		t.Fatalf("lan interface = %#v, want generated VLAN bridge on enp5s0", iface)
	}
	if !containsCommand(plan.SetupCommands(), []string{"ip", "link", "add", "link", "enp5s0", "name", iface.VLANDevice, "type", "vlan", "id", "4"}) {
		t.Fatalf("setup commands = %#v, want VLAN device on enp5s0", plan.SetupCommands())
	}
	if !containsCommand(plan.SetupCommands(), []string{"ip", "link", "set", "yvm-t-c89e9b-l0", "master", iface.Bridge}) {
		t.Fatalf("setup commands = %#v, want tap attached to generated bridge", plan.SetupCommands())
	}
}

func TestVMNetworkPlanFromDBRebuildsGenericDotVLANBridge(t *testing.T) {
	oldBridgeUplink := vmLANBridgeUplinkFn
	oldLiveBridgeExists := vmLANLiveBridgeExistsFn
	t.Cleanup(func() {
		vmLANBridgeUplinkFn = oldBridgeUplink
		vmLANLiveBridgeExistsFn = oldLiveBridgeExists
	})
	vmLANLiveBridgeExistsFn = func(bridge string) bool {
		return bridge == "tenant"
	}
	vmLANBridgeUplinkFn = func(parent string) (string, error) {
		if parent != "tenant" {
			t.Fatalf("bridge uplink parent = %q, want tenant", parent)
		}
		return "eno2", nil
	}

	plan := vmNetworkPlanFromDB("devbox", []db.VMNetworkConfig{{
		Mode:      "lan",
		Interface: "eth0",
		Tap:       "yvm-devbox-l0",
		Parent:    "tenant.4",
		VLAN:      4,
	}})
	iface := plan.Interfaces[0]
	if iface.Parent != "eno2" || iface.Bridge == "tenant.4" || iface.VLANDevice == "" {
		t.Fatalf("lan interface = %#v, want generated VLAN bridge on eno2", iface)
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
	metadata := plan.MetadataNetworks()
	if len(metadata) != 2 {
		t.Fatalf("metadata networks = %d, want 2", len(metadata))
	}
	if metadata[0].Gateway != "" {
		t.Fatalf("svc gateway = %q, want no default gateway when LAN is present", metadata[0].Gateway)
	}
	if metadata[0].DNSDefaultRoute == nil || *metadata[0].DNSDefaultRoute {
		t.Fatalf("svc DNSDefaultRoute = %v, want false", metadata[0].DNSDefaultRoute)
	}
	if metadata[1].DNSDefaultRoute != nil {
		t.Fatalf("lan DNSDefaultRoute = %v, want nil", *metadata[1].DNSDefaultRoute)
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
		case reflect.DeepEqual(cmd, []string{"ip", "addr", "del", vmSvcBridgeAddress + "/24", "dev", plan.Interfaces[0].Bridge}):
			return errors.New("RTNETLINK answers: Cannot assign requested address")
		case reflect.DeepEqual(cmd, []string{"ip", "addr", "del", vmSvcBridgeAddress + "/32", "dev", plan.Interfaces[0].Bridge}):
			return errors.New("RTNETLINK answers: Cannot assign requested address")
		case reflect.DeepEqual(cmd, []string{"ip", "route", "del", "192.168.100.12/32", "dev", plan.Interfaces[0].Bridge}):
			return errors.New("RTNETLINK answers: No such process")
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

func TestVMNetworkExecuteSetupToleratesExistingGeneratedVLAN(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "eth0", LANVLAN: 42})
	oldMatches := vmNetworkExistingVLANDeviceMatchesFn
	vmNetworkExistingVLANDeviceMatchesFn = func(parent, name string, vlan int) (bool, error) {
		if parent != "eth0" || name != plan.Interfaces[0].VLANDevice || vlan != 42 {
			t.Fatalf("validator = parent %q name %q vlan %d", parent, name, vlan)
		}
		return true, nil
	}
	t.Cleanup(func() {
		vmNetworkExistingVLANDeviceMatchesFn = oldMatches
	})

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

func TestVMNetworkExecuteSetupRejectsExistingGeneratedVLANMismatch(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "eth0", LANVLAN: 42})
	oldMatches := vmNetworkExistingVLANDeviceMatchesFn
	vmNetworkExistingVLANDeviceMatchesFn = func(string, string, int) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() {
		vmNetworkExistingVLANDeviceMatchesFn = oldMatches
	})

	err := plan.ExecuteSetup(func(cmd []string) error {
		if isVMNetworkVLANAddCommand(cmd) {
			return errors.New("RTNETLINK answers: File exists")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "RTNETLINK answers: File exists") {
		t.Fatalf("ExecuteSetup error = %v, want original VLAN exists error", err)
	}
}

func TestVMNetworkExecuteSetupRejectsExistingGeneratedVLANValidationError(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "eth0", LANVLAN: 42})
	oldMatches := vmNetworkExistingVLANDeviceMatchesFn
	vmNetworkExistingVLANDeviceMatchesFn = func(string, string, int) (bool, error) {
		return false, errors.New("read vlan config")
	}
	t.Cleanup(func() {
		vmNetworkExistingVLANDeviceMatchesFn = oldMatches
	})

	err := plan.ExecuteSetup(func(cmd []string) error {
		if isVMNetworkVLANAddCommand(cmd) {
			return errors.New("RTNETLINK answers: File exists")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "RTNETLINK answers: File exists") {
		t.Fatalf("ExecuteSetup error = %v, want original VLAN exists error", err)
	}
}

func TestVMNetworkExecuteSetupNonVLANToleranceSkipsExistingVLANValidation(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	oldMatches := vmNetworkExistingVLANDeviceMatchesFn
	vmNetworkExistingVLANDeviceMatchesFn = func(string, string, int) (bool, error) {
		t.Fatal("VLAN validator should not be called for non-VLAN setup commands")
		return false, nil
	}
	t.Cleanup(func() {
		vmNetworkExistingVLANDeviceMatchesFn = oldMatches
	})

	err := plan.ExecuteSetup(func(cmd []string) error {
		if reflect.DeepEqual(cmd[:4], []string{"ip", "link", "add", plan.Interfaces[0].Bridge}) {
			return errors.New("RTNETLINK answers: File exists")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteSetup: %v", err)
	}
}

func TestVMNetworkVLANAddCommandDetails(t *testing.T) {
	cmd := []string{"ip", "link", "add", "link", "eth0", "name", "yvm-devbox-v0", "type", "vlan", "id", "42"}

	parent, name, vlan, ok := vmNetworkVLANAddCommandDetails(cmd)
	if !ok || parent != "eth0" || name != "yvm-devbox-v0" || vlan != 42 {
		t.Fatalf("details = %q/%q/%d/%v, want eth0/yvm-devbox-v0/42/true", parent, name, vlan, ok)
	}
}

func TestVMNetworkExistingVLANDeviceMatchesFromConfig(t *testing.T) {
	config := strings.NewReader(`VLAN Dev name	 | VLAN ID
Name-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD
yvm-devbox-v0   | 42  | eth0
`)

	ok, err := vmNetworkExistingVLANDeviceMatchesFromConfig("eth0", "yvm-devbox-v0", 42, config)
	if err != nil {
		t.Fatalf("vmNetworkExistingVLANDeviceMatchesFromConfig: %v", err)
	}
	if !ok {
		t.Fatal("existing VLAN match = false, want true")
	}
}

func TestVMNetworkExistingVLANDeviceMatchesFromCompactConfig(t *testing.T) {
	config := strings.NewReader(`VLAN Dev name	 | VLAN ID
Name-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD
yvm-devbox-v0| 42  | eth0
`)

	ok, err := vmNetworkExistingVLANDeviceMatchesFromConfig("eth0", "yvm-devbox-v0", 42, config)
	if err != nil {
		t.Fatalf("vmNetworkExistingVLANDeviceMatchesFromConfig: %v", err)
	}
	if !ok {
		t.Fatal("existing VLAN match = false, want true")
	}
}

func TestVMNetworkExecuteSetupToleratesAlreadyAssignedAddress(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	err := plan.ExecuteSetup(func(cmd []string) error {
		if reflect.DeepEqual(cmd[:4], []string{"ip", "addr", "add", vmSvcBridgeAddress + "/24"}) {
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

func TestVMNetworkCleanupToleratesMissingNetNSPeer(t *testing.T) {
	err := runVMNetworkCommands(func([]string) error {
		return errors.New("Cannot find device \"yvm-old-123456-n0\"")
	}, [][]string{
		{"ip", "netns", "exec", vmSvcNetNS, "ip", "link", "del", "yvm-old-123456-n0"},
	}, vmNetworkCommandModeCleanup)
	if err != nil {
		t.Fatalf("runVMNetworkCommands: %v", err)
	}
}

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
		{
			line: "192.168.100.0/24 dev yvm-old-123456-b0 src 192.168.100.254",
			want: vmNetworkRoute{Destination: "192.168.100.0/24", Device: "yvm-old-123456-b0"},
			ok:   true,
		},
		{
			line: "2001:db8::1/128 dev yvm-old-123456-b0",
			want: vmNetworkRoute{Destination: "2001:db8::1/128", Device: "yvm-old-123456-b0"},
			ok:   true,
		},
		{
			line: "192.168.100.15/32 dev yvm-old-123456-l0",
			want: vmNetworkRoute{Destination: "192.168.100.15/32", Device: "yvm-old-123456-l0"},
			ok:   true,
		},
		{line: "default via 10.0.0.1 dev eth0", ok: false},
		{line: "192.168.100.14 dev eth0", ok: false},
		{line: "192.168.100.15/32 dev yvm-old-123456-x0", ok: false},
	}
	for _, tt := range tests {
		got, ok := vmNetworkRouteFromIPRouteLine(tt.line)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("vmNetworkRouteFromIPRouteLine(%q) = %#v/%v, want %#v/%v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestVMNetworkRoutesFromIPRouteOutputParsesReservedDeviceRoutes(t *testing.T) {
	output := strings.Join([]string{
		"192.168.100.12 dev yvm-old-123456-b0 src 192.168.100.254",
		"default via 10.0.0.1 dev eth0",
		"192.168.100.13/32 dev yvm-live-abcdef-b1 src 192.168.100.254",
		"192.168.100.0/24 dev yvm-old-123456-l0",
		"192.168.100.14 dev eth0",
		"",
	}, "\n")

	got := vmNetworkRoutesFromIPRouteOutput([]byte(output))
	want := []vmNetworkRoute{
		{Destination: "192.168.100.12/32", Device: "yvm-old-123456-b0"},
		{Destination: "192.168.100.13/32", Device: "yvm-live-abcdef-b1"},
		{Destination: "192.168.100.0/24", Device: "yvm-old-123456-l0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %#v, want %#v", got, want)
	}
}

func TestCollectVMNetworkLiveStateCollectsLinksAndRoutes(t *testing.T) {
	oldLinkLister := vmNetworkLinkLister
	oldRouteLister := vmNetworkRouteLister
	vmNetworkLinkLister = func(context.Context) ([]string, error) {
		return []string{"yvm-old-123456-b0"}, nil
	}
	vmNetworkRouteLister = func(context.Context) ([]vmNetworkRoute, error) {
		return []vmNetworkRoute{{Destination: "192.168.100.12/32", Device: "yvm-old-123456-b0"}}, nil
	}
	t.Cleanup(func() {
		vmNetworkLinkLister = oldLinkLister
		vmNetworkRouteLister = oldRouteLister
	})

	got, err := collectVMNetworkLiveState(context.Background())
	if err != nil {
		t.Fatalf("collectVMNetworkLiveState: %v", err)
	}
	want := vmNetworkLiveState{
		Links:  []string{"yvm-old-123456-b0"},
		Routes: []vmNetworkRoute{{Destination: "192.168.100.12/32", Device: "yvm-old-123456-b0"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("live state = %#v, want %#v", got, want)
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
			{Destination: "192.168.100.0/24", Device: "yvm-old-123456-l0"},
		},
	}

	report := desired.Check(live)
	if !slices.IsSorted(report.Findings) {
		t.Fatalf("findings = %#v, want sorted findings", report.Findings)
	}
	for _, want := range []string{
		"missing link " + plan.Interfaces[0].Tap,
		"stale link yvm-old-123456-l0",
		"stale route 192.168.100.99/32 dev yvm-old-123456-b0",
		"stale route 192.168.100.0/24 dev yvm-old-123456-l0",
	} {
		if !slices.Contains(report.Findings, want) {
			t.Fatalf("findings = %#v, missing %q", report.Findings, want)
		}
	}
}

func TestVMNetworkRouteCleanupCommandsRejectsBroadAndNonBridgeTargets(t *testing.T) {
	routes := []vmNetworkRoute{
		{Destination: "192.168.100.12/32", Device: "yvm-old-123456-b0"},
		{Destination: "192.168.100.0/24", Device: "yvm-old-123456-b0"},
		{Destination: "192.168.100.14/32", Device: "yvm-old-123456-l0"},
		{Destination: "192.168.100.15/32", Device: "yvm-old-123456-s0"},
		{Destination: "2001:db8::1/128", Device: "yvm-old-123456-b0"},
	}

	got := unownedVMNetworkRouteCleanupCommands(routes, nil)
	want := [][]string{
		{"ip", "route", "del", "192.168.100.12/32", "dev", "yvm-old-123456-b0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route cleanup commands = %#v, want %#v", got, want)
	}
}

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
		{"ip", "route", "del", "192.168.100.12/32", "dev", live.Interfaces[0].Bridge},
		{"ip", "route", "del", "192.168.100.99/32", "dev", "yvm-old-123456-b0"},
		{"ip", "link", "del", "yvm-old-123456-l1"},
	} {
		if !containsCommand(commands, want) {
			t.Fatalf("commands missing %#v in %#v", want, commands)
		}
	}
}

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

func TestEnsureVMNetworkDelegatesTapForJailedVM(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "services", "target")
	if err := writeVMIsolationMode(root, vmIsolationJailer); err != nil {
		t.Fatal(err)
	}
	plan := newVMNetworkPlan("target", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"target": {Name: "target", ServiceType: db.ServiceTypeVM, ServiceRoot: root, VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Networks: plan.DBNetworks()}},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	oldRunner := vmNetworkReconcileRunner
	oldIdentity := vmNetworkEnsureRuntimeIdentity
	var commands [][]string
	vmNetworkReconcileRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	vmNetworkEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	}
	t.Cleanup(func() {
		vmNetworkReconcileRunner = oldRunner
		vmNetworkEnsureRuntimeIdentity = oldIdentity
	})

	if err := server.EnsureVMNetwork(context.Background(), "target"); err != nil {
		t.Fatalf("EnsureVMNetwork: %v", err)
	}
	want := []string{"ip", "tuntap", "add", plan.Interfaces[0].Tap, "mode", "tap", "user", "812", "group", "813"}
	if !containsCommand(commands, want) {
		t.Fatalf("delegated target setup missing %#v in %#v", want, commands)
	}
}

func TestReconcileVMNetworksDelegatesTapForJailedVM(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "services", "target")
	if err := writeVMIsolationMode(root, vmIsolationJailer); err != nil {
		t.Fatal(err)
	}
	plan := newVMNetworkPlan("target", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"target": {Name: "target", ServiceType: db.ServiceTypeVM, ServiceRoot: root, VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Networks: plan.DBNetworks()}},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	oldCollector := vmNetworkLiveStateCollector
	oldRunner := vmNetworkReconcileRunner
	oldIdentity := vmNetworkEnsureRuntimeIdentity
	vmNetworkLiveStateCollector = func(context.Context) (vmNetworkLiveState, error) { return vmNetworkLiveState{}, nil }
	var commands [][]string
	vmNetworkReconcileRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	vmNetworkEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	}
	t.Cleanup(func() {
		vmNetworkLiveStateCollector = oldCollector
		vmNetworkReconcileRunner = oldRunner
		vmNetworkEnsureRuntimeIdentity = oldIdentity
	})

	if err := server.reconcileVMNetworks(context.Background()); err != nil {
		t.Fatalf("reconcileVMNetworks: %v", err)
	}
	want := []string{"ip", "tuntap", "add", plan.Interfaces[0].Tap, "mode", "tap", "user", "812", "group", "813"}
	if !containsCommand(commands, want) {
		t.Fatalf("delegated reconcile missing %#v in %#v", want, commands)
	}
}

func TestPackageEnsureVMNetworkUsesMinimalDBConfig(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		DB: db.NewStore(root+"/db.json", root+"/services"),
	}
	target := newVMNetworkPlan("target", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	other := newVMNetworkPlan("other", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.13"})
	if err := cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
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
	t.Cleanup(func() {
		vmNetworkReconcileRunner = oldRunner
	})

	if err := EnsureVMNetwork(context.Background(), &cfg, "target"); err != nil {
		t.Fatalf("EnsureVMNetwork: %v", err)
	}
	if !containsCommand(commands, []string{"ip", "tuntap", "add", target.Interfaces[0].Tap, "mode", "tap"}) {
		t.Fatalf("target setup missing: %#v", commands)
	}
	if containsCommand(commands, []string{"ip", "tuntap", "add", other.Interfaces[0].Tap, "mode", "tap"}) {
		t.Fatalf("other VM was modified: %#v", commands)
	}
}

func TestEnsureVMNetworkReportsMissingService(t *testing.T) {
	server := newTestServer(t)

	err := server.EnsureVMNetwork(context.Background(), "missing")
	if err == nil || err.Error() != `service "missing" not found` {
		t.Fatalf("EnsureVMNetwork error = %v, want missing service", err)
	}
}

func TestEnsureVMNetworkReportsNonVMService(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"web": {Name: "web", ServiceType: db.ServiceTypeDockerCompose},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	err := server.EnsureVMNetwork(context.Background(), "web")
	if err == nil || err.Error() != `service "web" is not a VM service` {
		t.Fatalf("EnsureVMNetwork error = %v, want non-VM service", err)
	}
}

func TestEnsureVMNetworkRejectsUnsupportedLANState(t *testing.T) {
	server := newTestServer(t)
	plan := newVMNetworkPlan("badlan", []string{"lan"}, vmNetworkInputs{})
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"badlan": {Name: "badlan", ServiceType: db.ServiceTypeVM, VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Networks: plan.DBNetworks()}},
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

	err := server.EnsureVMNetwork(context.Background(), "badlan")
	if err == nil || !strings.Contains(err.Error(), "VM LAN network parent is required") {
		t.Fatalf("EnsureVMNetwork error = %v, want LAN validation error", err)
	}
	if len(commands) != 0 {
		t.Fatalf("commands = %#v, want no setup commands after validation failure", commands)
	}
}

func TestEnsureVMNetworkRejectsDBBackedNonBridgeLANParent(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"badlan": {
			Name:        "badlan",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Runtime: vmRuntimeFirecracker,
				Networks: []db.VMNetworkConfig{{
					Mode:      "lan",
					Interface: "eth0",
					Tap:       "yvm-badlan-l0",
					Parent:    "eth0",
				}},
			},
		},
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

	err := server.EnsureVMNetwork(context.Background(), "badlan")
	want := `VM LAN network parent "eth0" is not a bridge; prepare a host bridge with`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("EnsureVMNetwork error = %v, want %q", err, want)
	}
	if len(commands) != 0 {
		t.Fatalf("commands = %#v, want no setup commands after validation failure", commands)
	}
}

func TestReconcileVMNetworksRejectsUnsupportedLANStateBeforeCleanup(t *testing.T) {
	server := newTestServer(t)
	plan := newVMNetworkPlan("badlan", []string{"lan"}, vmNetworkInputs{})
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"badlan": {Name: "badlan", ServiceType: db.ServiceTypeVM, VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Networks: plan.DBNetworks()}},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	oldCollector := vmNetworkLiveStateCollector
	oldRunner := vmNetworkReconcileRunner
	vmNetworkLiveStateCollector = func(context.Context) (vmNetworkLiveState, error) {
		return vmNetworkLiveState{
			Links:  []string{"yvm-old-123456-b0"},
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

	err := server.reconcileVMNetworks(context.Background())
	if err == nil || !strings.Contains(err.Error(), "VM LAN network parent is required") {
		t.Fatalf("reconcileVMNetworks error = %v, want LAN validation error", err)
	}
	if len(commands) != 0 {
		t.Fatalf("commands = %#v, want validation to block setup and cleanup", commands)
	}
}

func TestReconcileOrphanedVMServiceNetworksIgnoresRouteListerFailure(t *testing.T) {
	server := newTestServer(t)
	live := newVMNetworkPlan("livebox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	liveBase, _, ok := vmServiceNetworkLinkBase(live.Interfaces[0].Tap)
	if !ok {
		t.Fatalf("failed to parse live tap %q", live.Interfaces[0].Tap)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"livebox": {
			Name:        "livebox",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Runtime:  vmRuntimeFirecracker,
				Networks: live.DBNetworks(),
			},
		},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	oldLister := vmNetworkLinkLister
	oldRouteLister := vmNetworkRouteLister
	oldRunner := vmNetworkReconcileRunner
	vmNetworkLinkLister = func(context.Context) ([]string, error) {
		return []string{
			liveBase + "-b0",
			"yvm-old-123456-b0",
			"yvm-old-123456-s0",
		}, nil
	}
	vmNetworkRouteLister = func(context.Context) ([]vmNetworkRoute, error) {
		return nil, errors.New("route listing unavailable")
	}
	var commands [][]string
	vmNetworkReconcileRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	t.Cleanup(func() {
		vmNetworkLinkLister = oldLister
		vmNetworkRouteLister = oldRouteLister
		vmNetworkReconcileRunner = oldRunner
	})

	if err := server.reconcileOrphanedVMServiceNetworks(context.Background()); err != nil {
		t.Fatalf("reconcileOrphanedVMServiceNetworks: %v", err)
	}
	want := [][]string{
		{"ip", "link", "del", "yvm-old-123456-s0"},
		{"ip", "link", "del", "yvm-old-123456-b0"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestReconcileOrphanedVMServiceNetworksKeepsOwnedSharedVLANLinks(t *testing.T) {
	server := newTestServer(t)
	live := newVMNetworkPlan("livebox", []string{"lan"}, vmNetworkInputs{LANParent: "eth0", LANVLAN: 4})
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"livebox": {
			Name:        "livebox",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Runtime:  vmRuntimeFirecracker,
				Networks: live.DBNetworks(),
			},
		},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	oldLister := vmNetworkLinkLister
	oldRunner := vmNetworkReconcileRunner
	vmNetworkLinkLister = func(context.Context) ([]string, error) {
		return []string{
			live.Interfaces[0].VLANDevice,
			live.Interfaces[0].Bridge,
			live.Interfaces[0].Tap,
			"yvm-old-123456-v0",
			"yvm-old-123456-b0",
		}, nil
	}
	var commands [][]string
	vmNetworkReconcileRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	t.Cleanup(func() {
		vmNetworkLinkLister = oldLister
		vmNetworkReconcileRunner = oldRunner
	})

	if err := server.reconcileOrphanedVMServiceNetworks(context.Background()); err != nil {
		t.Fatalf("reconcileOrphanedVMServiceNetworks: %v", err)
	}
	for _, command := range [][]string{
		{"ip", "link", "del", live.Interfaces[0].VLANDevice},
		{"ip", "link", "del", live.Interfaces[0].Bridge},
	} {
		if containsCommand(commands, command) {
			t.Fatalf("commands = %#v, should not delete owned shared VLAN link %#v", commands, command)
		}
	}
	for _, command := range [][]string{
		{"ip", "link", "del", "yvm-old-123456-v0"},
		{"ip", "link", "del", "yvm-old-123456-b0"},
	} {
		if !containsCommand(commands, command) {
			t.Fatalf("commands = %#v, missing stale cleanup %#v", commands, command)
		}
	}
}

func TestVMNetworkUnsupportedLANIsExplicit(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "eth0"})
	if err := plan.ExecuteSetup(func([]string) error {
		t.Fatal("runner should not be called for unsupported LAN parent")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "prepare a host bridge with") {
		t.Fatalf("ExecuteSetup error = %v, want host bridge preparation guidance", err)
	}

	cmds := plan.SetupCommands()
	if len(cmds) != 1 || cmds[0][0] != "yeet-vm-network-unsupported" {
		t.Fatalf("SetupCommands = %#v, want explicit unsupported command", cmds)
	}
}
