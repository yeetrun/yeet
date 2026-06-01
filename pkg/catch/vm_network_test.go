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
