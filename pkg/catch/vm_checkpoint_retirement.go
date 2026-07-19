// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

const retiredVMCheckpointMode = "full"

type retiredVMCheckpoint struct {
	Service   string
	Snapshot  string
	Directory string
}

func inventoryRetiredVMCheckpoints(ctx context.Context, server *Server, services map[string]*db.Service) ([]retiredVMCheckpoint, error) {
	names := retiredVMCheckpointServiceNames(services)
	retired := make([]retiredVMCheckpoint, 0)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := inventoryRetiredVMCheckpointsForService(ctx, server, services[name])
		if err != nil {
			return nil, err
		}
		retired = append(retired, items...)
	}
	slices.SortFunc(retired, func(a, b retiredVMCheckpoint) int {
		return cmp.Compare(a.Service+"\x00"+a.Snapshot+a.Directory, b.Service+"\x00"+b.Snapshot+b.Directory)
	})
	return retired, nil
}

func retiredVMCheckpointServiceNames(services map[string]*db.Service) []string {
	names := make([]string, 0, len(services))
	for name, service := range services {
		if isVMJailerUpgradeService(service) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func inventoryRetiredVMCheckpointsForService(ctx context.Context, server *Server, service *db.Service) ([]retiredVMCheckpoint, error) {
	retired := make([]retiredVMCheckpoint, 0)
	points, err := server.listRecoveryPointsForService(ctx, service, false)
	if err != nil {
		return nil, fmt.Errorf("list recovery points for VM %q: %w", service.Name, err)
	}
	for _, point := range points {
		if point.Mode == retiredVMCheckpointMode {
			retired = append(retired, retiredVMCheckpoint{Service: service.Name, Snapshot: point.Name})
		}
	}

	checkpointDir := filepath.Join(serviceDataDirForRoot(server.serviceRootFromService(service)), "checkpoints")
	entries, err := os.ReadDir(checkpointDir)
	if errors.Is(err, os.ErrNotExist) {
		return retired, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read VM checkpoint directory %s: %w", checkpointDir, err)
	}
	for _, entry := range entries {
		retired = append(retired, retiredVMCheckpoint{
			Service:   service.Name,
			Directory: filepath.Join(checkpointDir, entry.Name()),
		})
	}
	return retired, nil
}

func validateNoRetiredVMCheckpoints(items []retiredVMCheckpoint) error {
	if len(items) == 0 {
		return nil
	}
	slices.SortFunc(items, func(a, b retiredVMCheckpoint) int {
		return cmp.Compare(a.Service+"\x00"+a.Snapshot+a.Directory, b.Service+"\x00"+b.Snapshot+b.Directory)
	})
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if item.Snapshot != "" {
			lines = append(lines, fmt.Sprintf("%s: ZFS snapshot %s is tagged full", item.Service, item.Snapshot))
		}
		if item.Directory != "" {
			lines = append(lines, fmt.Sprintf("%s: memory checkpoint directory %s exists", item.Service, item.Directory))
		}
	}
	return fmt.Errorf("retired Firecracker memory checkpoints block this Catch upgrade:\n%s\narchive or remove memory/VMM-state files, then remove the recovery point or set com.yeetrun:checkpoint=disk on the retained ZFS snapshot", strings.Join(lines, "\n"))
}
