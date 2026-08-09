// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRenderFirecrackerConfig(t *testing.T) {
	cfg := firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: "/srv/images/vmlinux",
			InitrdPath:      "/srv/images/initrd.img",
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
	for _, want := range []string{"kernel_image_path", "vmlinux", "initrd_path", "initrd.img", "vcpu_count", "mem_size_mib", "yvm-abcd-s0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestRenderFirecrackerConfigDisablesNestedVirtualization(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource:    firecrackerBootSource{KernelImagePath: "/vmlinux"},
		Drives:        []firecrackerDrive{{DriveID: "rootfs", PathOnHost: "/rootfs", IsRootDevice: true}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 1024},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	var got firecrackerConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode rendered config: %v", err)
	}
	want := firecrackerCPUConfig{CPUIDModifiers: []firecrackerCPUIDLeafModifier{
		{
			Leaf: "0x1", Subleaf: "0x0", Flags: 0,
			Modifiers: []firecrackerCPUIDRegisterModifier{{
				Register: "ecx", Bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx",
			}},
		},
		{
			Leaf: "0x80000001", Subleaf: "0x0", Flags: 0,
			Modifiers: []firecrackerCPUIDRegisterModifier{{
				Register: "ecx", Bitmap: "0bxxxxxxxxxxxxxxxxxxxxxxxxxxxxx0xx",
			}},
		},
	}}
	if !reflect.DeepEqual(got.CPUConfig, want) {
		t.Fatalf("CPUConfig = %#v, want %#v", got.CPUConfig, want)
	}
	text := string(raw)
	if strings.Contains(text, "cpu_template") || strings.Contains(text, "msr_modifiers") {
		t.Fatalf("config contains broad CPU-template state:\n%s", raw)
	}
}

func TestRenderFirecrackerConfigOmitsEmptyInitrd(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: "/srv/images/vmlinux",
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw",
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   "/srv/vms/devbox/rootfs.raw",
			IsRootDevice: true,
		}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 2048},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	if strings.Contains(string(raw), "initrd_path") {
		t.Fatalf("config includes empty initrd_path:\n%s", raw)
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

func TestRenderFirecrackerConfigIncludesVsock(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: "/srv/images/vmlinux",
			BootArgs:        "console=ttyS0",
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   "/srv/vms/devbox/rootfs.raw",
			IsRootDevice: true,
		}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 2048},
		Vsock: &firecrackerVsock{
			VsockID:  "agent",
			GuestCID: 3,
			UDSPath:  "/srv/vms/devbox/run/vsock.sock",
		},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`"vsock"`,
		`"vsock_id": "agent"`,
		`"guest_cid": 3`,
		`"uds_path": "/srv/vms/devbox/run/vsock.sock"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestRenderFirecrackerConfigIncludesBalloon(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: "/srv/images/vmlinux",
			BootArgs:        "console=ttyS0",
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   "/srv/vms/devbox/rootfs.raw",
			IsRootDevice: true,
		}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 2048},
		Balloon: &firecrackerBalloon{
			AmountMib:             0,
			DeflateOnOOM:          true,
			StatsPollingIntervalS: 5,
		},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`"balloon"`,
		`"amount_mib": 0`,
		`"deflate_on_oom": true`,
		`"stats_polling_interval_s": 5`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}
