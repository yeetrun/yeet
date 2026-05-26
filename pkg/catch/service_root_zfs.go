// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errZFSCommandFailed = errors.New("zfs command failed")
var osStat = os.Stat

type zfsCommandRunner func(context.Context, ...string) (stdout string, stderr string, err error)

type zfsServiceRootMode int

const (
	zfsServiceRootTarget zfsServiceRootMode = iota
	zfsServiceRootExisting
)

type resolvedServiceRoot struct {
	Root     string
	Dataset  string
	ZFS      bool
	Warnings []string
}

func runZFSCommand(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), err
	}
	return stdout.String(), stderr.String(), nil
}

func resolveZFSServiceRoot(ctx context.Context, runner zfsCommandRunner, dataset string, mode zfsServiceRootMode) (resolvedServiceRoot, error) {
	dataset = strings.TrimSpace(dataset)
	if dataset == "" {
		return resolvedServiceRoot{}, fmt.Errorf("--service-root is required when --zfs is set")
	}
	if runner == nil {
		runner = runZFSCommand
	}

	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	existingDataset := exists
	if !existingDataset {
		if err := zfsCreateDataset(ctx, runner, dataset); err != nil {
			return resolvedServiceRoot{}, err
		}
	}

	mountpoint, err := zfsDatasetMountpoint(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	root, warnings, err := validateZFSMountpoint(mountpoint, mode, existingDataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	if mode == zfsServiceRootTarget && existingDataset {
		warnings = append([]string{fmt.Sprintf("ZFS dataset %q already exists; using existing dataset", dataset)}, warnings...)
	}
	return resolvedServiceRoot{Root: root, Dataset: dataset, ZFS: true, Warnings: warnings}, nil
}

func zfsDatasetExists(ctx context.Context, runner zfsCommandRunner, dataset string) (bool, error) {
	stdout, stderr, err := runner(ctx, "list", "-H", "-o", "name", dataset)
	if err == nil {
		if strings.TrimSpace(stdout) == "" {
			return false, fmt.Errorf("zfs list %s returned no dataset name", dataset)
		}
		return true, nil
	}
	if strings.Contains(stderr, "dataset does not exist") || strings.Contains(stderr, "does not exist") {
		return false, nil
	}
	return false, formatZFSCommandError("zfs list "+dataset, stderr, err)
}

func zfsCreateDataset(ctx context.Context, runner zfsCommandRunner, dataset string) error {
	_, stderr, err := runner(ctx, "create", dataset)
	if err != nil {
		return formatZFSCommandError("zfs create "+dataset, stderr, err)
	}
	return nil
}

func zfsDatasetMountpoint(ctx context.Context, runner zfsCommandRunner, dataset string) (string, error) {
	stdout, stderr, err := runner(ctx, "get", "-H", "-o", "value", "mountpoint", dataset)
	if err != nil {
		return "", formatZFSCommandError("zfs get mountpoint "+dataset, stderr, err)
	}
	return strings.TrimSpace(stdout), nil
}

func validateZFSMountpoint(mountpoint string, mode zfsServiceRootMode, existingDataset bool) (string, []string, error) {
	mountpoint = strings.TrimSpace(mountpoint)
	if mountpoint == "" || mountpoint == "-" || mountpoint == "legacy" {
		return "", nil, fmt.Errorf("unsupported ZFS mountpoint %q; set a normal mounted mountpoint before using --zfs", mountpoint)
	}
	if !filepath.IsAbs(mountpoint) {
		return "", nil, fmt.Errorf("ZFS mountpoint %q must be absolute", mountpoint)
	}

	cleaned := filepath.Clean(mountpoint)
	if mode == zfsServiceRootExisting {
		root, err := validateExistingZFSMountpoint(cleaned)
		return root, nil, err
	}
	if existingDataset {
		root, err := validateExistingZFSMountpoint(cleaned)
		if err != nil {
			return "", nil, err
		}
		empty, err := rootIsMissingOrEmpty(cleaned)
		if err != nil {
			return "", nil, err
		}
		if !empty {
			return root, []string{fmt.Sprintf("ZFS service root %q is not empty; deploying into existing contents", root)}, nil
		}
		return root, nil, nil
	}
	root, err := validateRequestedServiceRoot(cleaned)
	return root, nil, err
}

func validateExistingZFSMountpoint(cleaned string) (string, error) {
	info, err := osStat(cleaned)
	if err != nil {
		return "", fmt.Errorf("failed to stat ZFS mountpoint %q: %w", cleaned, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("ZFS mountpoint %q is not a directory", cleaned)
	}
	return cleaned, nil
}

func formatZFSCommandError(command string, stderr string, err error) error {
	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		return fmt.Errorf("%s failed: %s", command, stderr)
	}
	return fmt.Errorf("%s failed: %w", command, err)
}
