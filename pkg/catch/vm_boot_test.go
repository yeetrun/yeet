// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"
)

func TestVMKernelBootArgsIncludesInitAndDHCPForLAN(t *testing.T) {
	network := newVMNetworkPlan("devbox", []string{"lan"}, vmNetworkInputs{LANParent: "br0", LANParentIsBridge: true})

	got, err := vmKernelBootArgs("devbox", network, vmImageManifest{})
	if err != nil {
		t.Fatalf("vmKernelBootArgs: %v", err)
	}

	for _, want := range []string{
		"console=ttyS0",
		"init=/usr/local/lib/yeet-vm/yeet-init",
		"ip=dhcp",
		"yeet.hostname=devbox",
		"yeet.iface=eth0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("boot args missing %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"pci=off", "root=/dev/vda", " rw"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("boot args include %q, want Firecracker/kernel-owned root args omitted: %s", unwanted, got)
		}
	}
}

func TestVMKernelBootArgsIncludesStaticSvcIP(t *testing.T) {
	network := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})

	got, err := vmKernelBootArgs("devbox", network, vmImageManifest{})
	if err != nil {
		t.Fatalf("vmKernelBootArgs: %v", err)
	}

	want := "ip=192.168.100.12::192.168.100.254:255.255.255.0:devbox:eth0:none"
	if !strings.Contains(got, want) {
		t.Fatalf("boot args = %s, want %s", got, want)
	}
}

func TestVMKernelBootArgsRejectsUnsafeServiceName(t *testing.T) {
	network := newVMNetworkPlan("bad name", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})

	_, err := vmKernelBootArgs("bad name", network, vmImageManifest{})

	if err == nil || !strings.Contains(err.Error(), "invalid VM hostname") {
		t.Fatalf("vmKernelBootArgs error = %v, want invalid hostname", err)
	}
}

func TestVMKernelBootArgsIncludesGuestSystemInit(t *testing.T) {
	network := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	manifest := vmImageManifest{
		GuestInit:       vmGuestInitPath,
		GuestSystemInit: "/run/current-system/init",
	}

	got, err := vmKernelBootArgs("devbox", network, manifest)
	if err != nil {
		t.Fatalf("vmKernelBootArgs: %v", err)
	}
	if !strings.Contains(got, "yeet.system_init=/run/current-system/init") {
		t.Fatalf("boot args missing NixOS system init: %s", got)
	}
}

func TestVMKernelBootArgsOmitsGuestSystemInitWhenManifestDoesNotDeclareIt(t *testing.T) {
	network := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})

	got, err := vmKernelBootArgs("devbox", network, vmImageManifest{GuestInit: vmGuestInitPath})
	if err != nil {
		t.Fatalf("vmKernelBootArgs: %v", err)
	}
	if strings.Contains(got, "yeet.system_init=") {
		t.Fatalf("boot args include unexpected system init: %s", got)
	}
}

func TestVMKernelBootArgsRejectsUnsafeGuestSystemInit(t *testing.T) {
	network := newVMNetworkPlan("devbox", []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})

	_, err := vmKernelBootArgs("devbox", network, vmImageManifest{GuestSystemInit: "/run/current-system/../init"})

	if err == nil || !strings.Contains(err.Error(), "invalid VM guest system init path") {
		t.Fatalf("vmKernelBootArgs error = %v, want invalid guest system init", err)
	}
}

func TestIPv4PrefixMask(t *testing.T) {
	tests := map[int]string{
		8:  "255.0.0.0",
		16: "255.255.0.0",
		24: "255.255.255.0",
		30: "255.255.255.252",
		32: "255.255.255.255",
	}
	for bits, want := range tests {
		got, ok := ipv4PrefixMask(bits)
		if !ok {
			t.Fatalf("ipv4PrefixMask(%d) not ok", bits)
		}
		if got != want {
			t.Fatalf("ipv4PrefixMask(%d) = %s, want %s", bits, got, want)
		}
	}
	if _, ok := ipv4PrefixMask(33); ok {
		t.Fatal("ipv4PrefixMask(33) ok, want false")
	}
}
