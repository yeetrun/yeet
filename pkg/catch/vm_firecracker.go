// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "encoding/json"

type firecrackerConfig struct {
	BootSource        firecrackerBootSource         `json:"boot-source"`
	Drives            []firecrackerDrive            `json:"drives"`
	NetworkInterfaces []firecrackerNetworkInterface `json:"network-interfaces"`
	MachineConfig     firecrackerMachineConfig      `json:"machine-config"`
	CPUConfig         firecrackerCPUConfig          `json:"cpu-config"`
	Vsock             *firecrackerVsock             `json:"vsock,omitempty"`
	Balloon           *firecrackerBalloon           `json:"balloon,omitempty"`
}

type firecrackerBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	InitrdPath      string `json:"initrd_path,omitempty"`
	BootArgs        string `json:"boot_args"`
}

type firecrackerDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type firecrackerNetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac"`
}

type firecrackerMachineConfig struct {
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMib int `json:"mem_size_mib"`
}

type firecrackerCPUConfig struct {
	CPUIDModifiers []firecrackerCPUIDLeafModifier `json:"cpuid_modifiers"`
}

type firecrackerCPUIDLeafModifier struct {
	Leaf      string                             `json:"leaf"`
	Subleaf   string                             `json:"subleaf"`
	Flags     uint32                             `json:"flags"`
	Modifiers []firecrackerCPUIDRegisterModifier `json:"modifiers"`
}

type firecrackerCPUIDRegisterModifier struct {
	Register string `json:"register"`
	Bitmap   string `json:"bitmap"`
}

func firecrackerNestedVirtualizationDisabledCPUConfig() firecrackerCPUConfig {
	return firecrackerCPUConfig{CPUIDModifiers: []firecrackerCPUIDLeafModifier{
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
}

type firecrackerVsock struct {
	VsockID  string `json:"vsock_id"`
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type firecrackerBalloon struct {
	AmountMib             int  `json:"amount_mib"`
	DeflateOnOOM          bool `json:"deflate_on_oom"`
	StatsPollingIntervalS int  `json:"stats_polling_interval_s,omitempty"`
}

func renderFirecrackerConfig(cfg firecrackerConfig) ([]byte, error) {
	cfg.CPUConfig = firecrackerNestedVirtualizationDisabledCPUConfig()
	return json.MarshalIndent(cfg, "", "  ")
}
