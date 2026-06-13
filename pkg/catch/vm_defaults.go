// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

var vmDefaultsHostProfileFunc func(*Server, catchrpc.VMDefaultsRequest, int64) (vmHostProfile, []string, error)

func (s *Server) vmDefaults(ctx context.Context, req catchrpc.VMDefaultsRequest) (catchrpc.VMDefaultsResponse, error) {
	req.Service = strings.TrimSpace(req.Service)
	req.ServiceRoot = strings.TrimSpace(req.ServiceRoot)
	runningVMBytes, err := s.runningVMBytesExcluding("")
	if err != nil {
		return catchrpc.VMDefaultsResponse{}, err
	}
	profile, warnings, err := s.vmDefaultsHostProfile(ctx, req, runningVMBytes)
	if err != nil {
		return catchrpc.VMDefaultsResponse{}, err
	}
	shape, err := defaultVMShape(profile)
	if err != nil {
		return catchrpc.VMDefaultsResponse{}, err
	}
	return catchrpc.VMDefaultsResponse{
		CPUs:        shape.CPUs,
		Memory:      formatVMSizeFlag(shape.MemoryBytes),
		MemoryBytes: shape.MemoryBytes,
		Disk:        formatVMSizeFlag(shape.DiskBytes),
		DiskBytes:   shape.DiskBytes,
		DiskBackend: shape.DiskBackend,
		Warnings:    warnings,
	}, nil
}

func (s *Server) vmDefaultsHostProfile(ctx context.Context, req catchrpc.VMDefaultsRequest, runningVMBytes int64) (vmHostProfile, []string, error) {
	if vmDefaultsHostProfileFunc != nil {
		return vmDefaultsHostProfileFunc(s, req, runningVMBytes)
	}
	storageBytes, storageZFS, warnings, err := s.vmDefaultsStorage(ctx, req)
	if err != nil {
		return vmHostProfile{}, nil, err
	}
	return localVMHostProfile(storageBytes, storageZFS, runningVMBytes), warnings, nil
}

func (s *Server) vmDefaultsStorage(ctx context.Context, req catchrpc.VMDefaultsRequest) (int64, bool, []string, error) {
	if req.ZFS {
		bytes, matched, err := zfsAvailableBytesForDataset(ctx, s.zfsRunner, req.ServiceRoot)
		if err != nil {
			return 0, true, nil, err
		}
		var warnings []string
		requested := strings.Trim(strings.TrimSpace(req.ServiceRoot), "/")
		if requested != "" && matched != requested {
			warnings = append(warnings, fmt.Sprintf("using available space from nearest ZFS parent %q", matched))
		}
		return bytes, true, warnings, nil
	}

	root, err := s.vmDefaultsFilesystemRoot(req)
	if err != nil {
		return 0, false, nil, err
	}
	path, warnings, err := nearestExistingStoragePath(root)
	if err != nil {
		return 0, false, nil, err
	}
	return availableStorageBytes(path), false, warnings, nil
}

func (s *Server) vmDefaultsFilesystemRoot(req catchrpc.VMDefaultsRequest) (string, error) {
	if req.ServiceRoot == "" {
		return s.defaultServiceRootDir(req.Service), nil
	}
	return validateRequestedServiceRoot(req.ServiceRoot)
}

func nearestExistingStoragePath(root string) (string, []string, error) {
	root = filepath.Clean(root)
	current := root
	for {
		if _, err := os.Stat(current); err == nil {
			if current != root {
				return current, []string{fmt.Sprintf("using available space from nearest existing path %q", current)}, nil
			}
			return current, nil, nil
		} else if !os.IsNotExist(err) {
			return "", nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("no existing parent path for %q", root)
		}
		current = parent
	}
}

func zfsAvailableBytesForDataset(ctx context.Context, runner zfsCommandRunner, dataset string) (int64, string, error) {
	dataset = strings.Trim(strings.TrimSpace(dataset), "/")
	if dataset == "" {
		return 0, "", fmt.Errorf("--service-root is required when --zfs is set")
	}
	if runner == nil {
		runner = runZFSCommand
	}
	for current := dataset; current != ""; current = zfsDatasetParent(current) {
		stdout, stderr, err := runner(ctx, "list", "-H", "-p", "-o", "available", current)
		if err == nil {
			available, parseErr := strconv.ParseInt(strings.TrimSpace(stdout), 10, 64)
			if parseErr != nil {
				return 0, "", fmt.Errorf("parse ZFS available bytes for %q: %w", current, parseErr)
			}
			return available, current, nil
		}
		if strings.Contains(stderr, "dataset does not exist") || strings.Contains(stderr, "does not exist") {
			continue
		}
		return 0, "", formatZFSCommandError("zfs list "+current, stderr, err)
	}
	return 0, "", fmt.Errorf("no existing ZFS parent found for %q", dataset)
}

func formatVMSizeFlag(bytes int64) string {
	switch {
	case bytes <= 0:
		return ""
	case bytes%(1<<30) == 0:
		return strconv.FormatInt(bytes>>30, 10) + "g"
	case bytes%(1<<20) == 0:
		return strconv.FormatInt(bytes>>20, 10) + "m"
	default:
		return strconv.FormatInt(bytes, 10)
	}
}
