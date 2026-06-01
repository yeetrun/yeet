// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderFirecrackerConfig(t *testing.T) {
	cfg := firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: "/srv/images/vmlinux",
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw",
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   "/srv/vms/devbox/rootfs.raw",
			IsRootDevice: true,
			IsReadOnly:   false,
		}},
		NetworkInterfaces: []firecrackerNetworkInterface{{
			IfaceID:     "eth0",
			HostDevName: "yvm-abcd-s0",
			GuestMAC:    "02:fc:00:00:00:12",
		}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 4, MemSizeMib: 4096},
	}
	raw, err := renderFirecrackerConfig(cfg)
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("invalid JSON: %s", string(raw))
	}
	text := string(raw)
	for _, want := range []string{"kernel_image_path", "vmlinux", "vcpu_count", "mem_size_mib", "yvm-abcd-s0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestRenderFirecrackerConfigIncludesNetworkFields(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{KernelImagePath: "/srv/images/vmlinux"},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   "/srv/vms/devbox/rootfs.raw",
			IsRootDevice: true,
		}},
		NetworkInterfaces: []firecrackerNetworkInterface{{IfaceID: "eth0", HostDevName: "yvm-abcd-s0", GuestMAC: "02:fc:00:00:00:12"}},
		MachineConfig:     firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 2048},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"network-interfaces"`, `"guest_mac": "02:fc:00:00:00:12"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}
