// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"
)

func TestDefaultVMShapeLabHostZFS(t *testing.T) {
	profile := vmHostProfile{
		Arch:           "x86_64",
		HasKVM:         true,
		LogicalCPUs:    12,
		MemoryBytes:    31 << 30,
		StorageBytes:   897 << 30,
		StorageZFS:     true,
		RunningVMBytes: 0,
	}
	shape, err := defaultVMShape(profile)
	if err != nil {
		t.Fatalf("defaultVMShape: %v", err)
	}
	if shape.CPUs != 4 {
		t.Fatalf("CPUs = %d, want 4", shape.CPUs)
	}
	if shape.MemoryBytes != 4<<30 {
		t.Fatalf("MemoryBytes = %d, want 4 GiB", shape.MemoryBytes)
	}
	if shape.DiskBytes != 128<<30 {
		t.Fatalf("DiskBytes = %d, want 128 GiB", shape.DiskBytes)
	}
	if shape.DiskBackend != vmDiskBackendZVOL {
		t.Fatalf("DiskBackend = %q, want zvol", shape.DiskBackend)
	}
}

func TestDefaultVMShapeSmallKVMHost(t *testing.T) {
	profile := vmHostProfile{
		Arch:         "x86_64",
		HasKVM:       true,
		LogicalCPUs:  2,
		MemoryBytes:  1900 << 20,
		StorageBytes: 11 << 30,
	}
	shape, err := defaultVMShape(profile)
	if err != nil {
		t.Fatalf("defaultVMShape: %v", err)
	}
	if shape.CPUs != 2 || shape.MemoryBytes != 512<<20 || shape.DiskBytes != 8<<30 || shape.DiskBackend != vmDiskBackendRaw {
		t.Fatalf("shape = %#v", shape)
	}
}

func TestDefaultVMShapeRejectsNoKVM(t *testing.T) {
	_, err := defaultVMShape(vmHostProfile{Arch: "x86_64", LogicalCPUs: 2, MemoryBytes: 2 << 30, StorageBytes: 20 << 30})
	if err == nil || !strings.Contains(err.Error(), "/dev/kvm is missing") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMMemoryAdmissionReservesHostMemory(t *testing.T) {
	err := admitVMMemory(vmHostProfile{
		HasKVM:         true,
		LogicalCPUs:    12,
		MemoryBytes:    31 << 30,
		RunningVMBytes: 23 << 30,
	}, 4<<30)
	if err != nil {
		t.Fatalf("admitVMMemory: %v", err)
	}
	err = admitVMMemory(vmHostProfile{
		HasKVM:         true,
		LogicalCPUs:    12,
		MemoryBytes:    31 << 30,
		RunningVMBytes: 28 << 30,
	}, 4<<30)
	if err == nil || !strings.Contains(err.Error(), "not enough memory") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMHostMemoryReserve(t *testing.T) {
	tests := []struct {
		name  string
		total int64
		want  int64
	}{
		{name: "minimum", total: 512 << 20, want: 1 << 30},
		{name: "exact tenth", total: 31 << 30, want: (31 << 30) / 10},
		{name: "maximum", total: 64 << 30, want: 4 << 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vmHostMemoryReserve(tt.total)
			if got != tt.want {
				t.Fatalf("vmHostMemoryReserve(%d) = %d, want %d", tt.total, got, tt.want)
			}
		})
	}
}

func TestParseVMSize(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "empty", raw: "", want: 0},
		{name: "megabytes", raw: "512mb", want: 512 << 20},
		{name: "m", raw: "1024m", want: 1024 << 20},
		{name: "gigabytes", raw: "4gb", want: 4 << 30},
		{name: "g", raw: "8g", want: 8 << 30},
		{name: "bytes", raw: "4096", want: 4096},
		{name: "spaces", raw: " 2 GB ", want: 2 << 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVMSize(tt.raw)
			if err != nil {
				t.Fatalf("parseVMSize(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("parseVMSize(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseVMSizeRejectsInvalidInputWithOriginalValue(t *testing.T) {
	_, err := parseVMSize(" gb ")
	if err == nil {
		t.Fatal("parseVMSize returned nil error")
	}
	if !strings.Contains(err.Error(), `invalid size "gb"`) {
		t.Fatalf("error = %v, want original input", err)
	}
}

func TestParseVMSizeRejectsOverflow(t *testing.T) {
	_, err := parseVMSize("9223372036854775807g")
	if err == nil {
		t.Fatal("parseVMSize returned nil error")
	}
	if !strings.Contains(err.Error(), "size overflows int64") {
		t.Fatalf("error = %v, want overflow", err)
	}
}

func TestLinuxMemTotalBytesFromReader(t *testing.T) {
	got, err := linuxMemTotalBytesFromReader(strings.NewReader("MemFree: 10 kB\nMemTotal: 2048 kB\n"))
	if err != nil {
		t.Fatalf("linuxMemTotalBytesFromReader: %v", err)
	}
	if got != 2048<<10 {
		t.Fatalf("MemTotal bytes = %d, want %d", got, int64(2048<<10))
	}
}
