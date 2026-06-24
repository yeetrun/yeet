// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

var (
	linuxMemTotalBytesFunc     = linuxMemTotalBytesDefault
	linuxMemAvailableBytesFunc = linuxMemAvailableBytesDefault
)

type vmHostProfile struct {
	Arch              string
	HasKVM            bool
	LogicalCPUs       int
	MemoryBytes       int64
	StorageBytes      int64
	StorageZFS        bool
	RunningVMBytes    int64
	RunningVMMinBytes int64
}

func validateVMHost(profile vmHostProfile) error {
	if !profile.HasKVM {
		return fmt.Errorf("VMs require Linux KVM; /dev/kvm is missing")
	}
	if profile.Arch != "x86_64" && profile.Arch != "amd64" {
		return fmt.Errorf("VMs require x86_64/amd64 in v0; got %s", profile.Arch)
	}
	return nil
}

func defaultVMShape(profile vmHostProfile) (vmShape, error) {
	if err := validateVMHost(profile); err != nil {
		return vmShape{}, err
	}
	shape := vmShape{
		CPUs:        defaultVMCPUs(profile.LogicalCPUs),
		MemoryBytes: defaultVMMemory(profile.MemoryBytes),
		BalloonMode: vmBalloonModeAuto,
		DiskBytes:   defaultVMDisk(profile.StorageBytes, profile.StorageZFS),
		DiskBackend: vmDiskBackendRaw,
	}
	if profile.StorageZFS {
		shape.DiskBackend = vmDiskBackendZVOL
	}
	if shape.DiskBytes == 0 {
		return vmShape{}, fmt.Errorf("not enough storage for VM disk")
	}
	return shape, nil
}

func defaultVMCPUs(hostCPUs int) int {
	switch {
	case hostCPUs <= 1:
		return 1
	case hostCPUs < 8:
		return 2
	default:
		return 4
	}
}

func defaultVMMemory(hostBytes int64) int64 {
	switch {
	case hostBytes <= 2<<30:
		return 512 << 20
	case hostBytes <= 4<<30:
		return 1 << 30
	case hostBytes < 16<<30:
		return 2 << 30
	default:
		return 4 << 30
	}
}

func defaultVMDisk(availableBytes int64, zfs bool) int64 {
	if zfs {
		return diskSizeForThresholds(availableBytes, []vmDiskThreshold{
			{min: 512 << 30, size: 128 << 30},
			{min: 128 << 30, size: 64 << 30},
			{min: 64 << 30, size: 32 << 30},
			{min: 32 << 30, size: 16 << 30},
			{min: 12 << 30, size: 8 << 30},
		})
	}
	return diskSizeForThresholds(availableBytes, []vmDiskThreshold{
		{min: 48 << 30, size: 32 << 30},
		{min: 24 << 30, size: 16 << 30},
		{min: 12 << 30, size: 8 << 30},
		{min: 8 << 30, size: 8 << 30},
	})
}

type vmDiskThreshold struct {
	min  int64
	size int64
}

func diskSizeForThresholds(availableBytes int64, thresholds []vmDiskThreshold) int64 {
	for _, threshold := range thresholds {
		if availableBytes >= threshold.min {
			return threshold.size
		}
	}
	return 0
}

func admitVMMemory(profile vmHostProfile, requestMaxBytes, requestMinBytes int64, policy vmMemoryPolicy) error {
	return admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		Policy:          policy,
		HostBytes:       profile.MemoryBytes,
		RunningMaxBytes: profile.RunningVMBytes,
		RunningMinBytes: profile.RunningVMMinBytes,
		RequestMaxBytes: requestMaxBytes,
		RequestMinBytes: requestMinBytes,
	})
}

func vmHostMemoryReserve(total int64) int64 {
	return vmBalloonHostMemoryReserve(total)
}

func localVMHostProfile(storageBytes int64, storageZFS bool, runningVMBytes int64) vmHostProfile {
	_, kvmErr := os.Stat("/dev/kvm")
	return vmHostProfile{
		Arch:           runtime.GOARCH,
		HasKVM:         kvmErr == nil,
		LogicalCPUs:    runtime.NumCPU(),
		MemoryBytes:    linuxMemTotalBytes(),
		StorageBytes:   storageBytes,
		StorageZFS:     storageZFS,
		RunningVMBytes: runningVMBytes,
	}
}

func (s *Server) vmHostMemoryPolicy() (vmMemoryPolicy, error) {
	dv, err := s.getDB()
	if err != nil {
		return vmMemoryPolicy{}, err
	}
	raw := ""
	if host := dv.VMHost(); host.Valid() {
		raw = host.MemoryPolicy()
	}
	return normalizeVMHostMemoryPolicy(raw)
}

func availableStorageBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

func linuxMemTotalBytes() int64 {
	return linuxMemTotalBytesFunc()
}

func linuxMemTotalBytesDefault() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	total, err := linuxMemTotalBytesFromReader(f)
	if err != nil {
		return 0
	}
	return total
}

func linuxMemTotalBytesFromReader(r io.Reader) (int64, error) {
	return linuxMeminfoValueBytesFromReader(r, "MemTotal:")
}

func linuxMemAvailableBytes() int64 {
	return linuxMemAvailableBytesFunc()
}

func linuxMemAvailableBytesDefault() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	available, err := linuxMemAvailableBytesFromReader(f)
	if err != nil {
		return 0
	}
	return available
}

func linuxMemAvailableBytesFromReader(r io.Reader) (int64, error) {
	return linuxMeminfoValueBytesFromReader(r, "MemAvailable:")
}

func linuxMeminfoValueBytesFromReader(r io.Reader, key string) (int64, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != key {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb < 0 {
			return 0, fmt.Errorf("invalid %s value %q", strings.TrimSuffix(key, ":"), fields[1])
		}
		return kb << 10, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%s not found", strings.TrimSuffix(key, ":"))
}

func formatBytesInt(bytes int64) string {
	const (
		gib = int64(1 << 30)
		mib = int64(1 << 20)
	)
	switch {
	case bytes%gib == 0:
		return fmt.Sprintf("%d GiB", bytes/gib)
	case bytes%mib == 0:
		return fmt.Sprintf("%d MiB", bytes/mib)
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}
