// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVMGuestKernelSelectionValidates(t *testing.T) {
	raw := []byte(`{
		"schema_version":1,
		"version":"linux-7.1.1-yeet",
		"kernel":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":{
			"vmlinux":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"kernel.config":"1111111111111111111111111111111111111111111111111111111111111111"
		}
	}`)
	var selection vmGuestKernelSelection
	if err := json.Unmarshal(raw, &selection); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := selection.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestVMGuestKernelSelectionRejectsUnsafePaths(t *testing.T) {
	selection := vmGuestKernelSelection{
		SchemaVersion: 1,
		Version:       "linux-7.1.1-yeet",
		Kernel:        "/tmp/vmlinux",
		KernelConfig:  "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		SHA256: map[string]string{
			"vmlinux":       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"kernel.config": "1111111111111111111111111111111111111111111111111111111111111111",
		},
	}

	err := selection.validate()
	if err == nil || !strings.Contains(err.Error(), "kernel path") {
		t.Fatalf("validate error = %v, want kernel path error", err)
	}
}

func TestVMGuestKernelSelectionAllowsNixStorePackagePaths(t *testing.T) {
	selection := vmGuestKernelSelection{
		SchemaVersion: 1,
		Version:       "linux-7.1.1-yeet",
		Kernel:        "/nix/store/abc123-yeet-vm-kernel/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		KernelConfig:  "/nix/store/abc123-yeet-vm-kernel/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		SHA256: map[string]string{
			"vmlinux":       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"kernel.config": "1111111111111111111111111111111111111111111111111111111111111111",
		},
	}

	if err := selection.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
