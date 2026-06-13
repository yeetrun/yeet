// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMDefaultsUseProvisioningShapeHeuristics(t *testing.T) {
	server := newTestServer(t)
	oldProfile := vmDefaultsHostProfileFunc
	defer func() { vmDefaultsHostProfileFunc = oldProfile }()

	if _, _, err := server.cfg.DB.MutateService("existing-vm", func(_ *db.Data, service *db.Service) error {
		service.VM = &db.VMConfig{
			MemoryBytes: 2 << 30,
			SetupState:  "ready",
		}
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}

	var gotRunning int64
	vmDefaultsHostProfileFunc = func(_ *Server, req catchrpc.VMDefaultsRequest, runningVMBytes int64) (vmHostProfile, []string, error) {
		gotRunning = runningVMBytes
		if req.Service != "devbox" || req.ServiceRoot != "flash/yeet/vms/devbox" || !req.ZFS {
			t.Fatalf("request = %#v, want devbox ZFS request", req)
		}
		return vmHostProfile{
			Arch:           "x86_64",
			HasKVM:         true,
			LogicalCPUs:    12,
			MemoryBytes:    31 << 30,
			StorageBytes:   897 << 30,
			StorageZFS:     true,
			RunningVMBytes: runningVMBytes,
		}, []string{"using nearest ZFS parent"}, nil
	}

	got, err := server.vmDefaults(context.Background(), catchrpc.VMDefaultsRequest{
		Service:     "devbox",
		ServiceRoot: "flash/yeet/vms/devbox",
		ZFS:         true,
	})
	if err != nil {
		t.Fatalf("vmDefaults: %v", err)
	}
	if gotRunning != 2<<30 {
		t.Fatalf("runningVMBytes = %d, want 2 GiB", gotRunning)
	}
	if got.CPUs != 4 || got.Memory != "4g" || got.MemoryBytes != 4<<30 || got.Disk != "128g" || got.DiskBytes != 128<<30 || got.DiskBackend != vmDiskBackendZVOL {
		t.Fatalf("defaults = %#v, want 4/4g/128g zvol", got)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "nearest ZFS parent") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
}

func TestVMDefaultsRejectUnsupportedHost(t *testing.T) {
	server := newTestServer(t)
	oldProfile := vmDefaultsHostProfileFunc
	defer func() { vmDefaultsHostProfileFunc = oldProfile }()

	vmDefaultsHostProfileFunc = func(_ *Server, _ catchrpc.VMDefaultsRequest, _ int64) (vmHostProfile, []string, error) {
		return vmHostProfile{
			Arch:         "arm64",
			HasKVM:       true,
			LogicalCPUs:  4,
			MemoryBytes:  8 << 30,
			StorageBytes: 128 << 30,
		}, nil, nil
	}

	_, err := server.vmDefaults(context.Background(), catchrpc.VMDefaultsRequest{Service: "devbox"})
	if err == nil || !strings.Contains(err.Error(), "x86_64/amd64") {
		t.Fatalf("error = %v, want architecture rejection", err)
	}
}

func TestVMDefaultsStorageUsesNearestExistingFilesystemPath(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	requestedRoot := filepath.Join(parent, "devbox")

	bytes, zfs, warnings, err := server.vmDefaultsStorage(context.Background(), catchrpc.VMDefaultsRequest{
		ServiceRoot: requestedRoot,
	})
	if err != nil {
		t.Fatalf("vmDefaultsStorage: %v", err)
	}
	if zfs {
		t.Fatalf("zfs = true, want false")
	}
	if bytes <= 0 {
		t.Fatalf("bytes = %d, want positive available storage", bytes)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], parent) {
		t.Fatalf("warnings = %#v, want nearest existing path %q", warnings, parent)
	}
}

func TestVMDefaultsStorageUsesNearestZFSParentAvailability(t *testing.T) {
	server := newTestServer(t)
	var calls []string
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "list -H -p -o available flash/yeet/vms/devbox":
			return "", "cannot open 'flash/yeet/vms/devbox': dataset does not exist", errZFSCommandFailed
		case "list -H -p -o available flash/yeet/vms":
			return "1457357299712\n", "", nil
		default:
			return "", "unexpected zfs command: " + call, errZFSCommandFailed
		}
	}

	bytes, zfs, warnings, err := server.vmDefaultsStorage(context.Background(), catchrpc.VMDefaultsRequest{
		ServiceRoot: "flash/yeet/vms/devbox",
		ZFS:         true,
	})
	if err != nil {
		t.Fatalf("vmDefaultsStorage: %v", err)
	}
	if !zfs {
		t.Fatalf("zfs = false, want true")
	}
	if bytes != 1457357299712 {
		t.Fatalf("bytes = %d, want 1457357299712", bytes)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "flash/yeet/vms") {
		t.Fatalf("warnings = %#v, want nearest ZFS parent", warnings)
	}
	wantCalls := []string{
		"list -H -p -o available flash/yeet/vms/devbox",
		"list -H -p -o available flash/yeet/vms",
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestZFSAvailableBytesForDatasetReportsCommandErrors(t *testing.T) {
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		return "", "permission denied", errZFSCommandFailed
	}

	_, _, err := zfsAvailableBytesForDataset(context.Background(), runner, "flash/yeet/vms")
	if err == nil || !strings.Contains(err.Error(), "zfs list flash/yeet/vms failed: permission denied") {
		t.Fatalf("error = %v, want permission denied command error", err)
	}
}

func TestZFSAvailableBytesForDatasetRequiresDataset(t *testing.T) {
	_, _, err := zfsAvailableBytesForDataset(context.Background(), nil, " / ")
	if err == nil || !strings.Contains(err.Error(), "--service-root is required") {
		t.Fatalf("error = %v, want service-root required", err)
	}
}
