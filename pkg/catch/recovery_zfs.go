// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"strings"
)

func zfsCloneSnapshot(ctx context.Context, runner zfsCommandRunner, snapshotName, targetDataset string) error {
	snapshotName = strings.TrimSpace(snapshotName)
	targetDataset = strings.TrimSpace(targetDataset)
	if err := requireZFSSnapshotName(snapshotName); err != nil {
		return err
	}
	if err := requireZFSDatasetName(targetDataset); err != nil {
		return err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "clone", snapshotName, targetDataset)
	if err != nil {
		return formatZFSCommandError("zfs clone "+snapshotName+" "+targetDataset, stderr, err)
	}
	return nil
}

func zfsRollbackSnapshot(ctx context.Context, runner zfsCommandRunner, snapshotName string) error {
	snapshotName = strings.TrimSpace(snapshotName)
	if err := requireZFSSnapshotName(snapshotName); err != nil {
		return err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "rollback", snapshotName)
	if err != nil {
		return formatZFSCommandError("zfs rollback "+snapshotName, stderr, err)
	}
	return nil
}

func zfsDestroyDataset(ctx context.Context, runner zfsCommandRunner, dataset string) error {
	dataset = strings.TrimSpace(dataset)
	if err := requireZFSDatasetName(dataset); err != nil {
		return err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "destroy", "-r", dataset)
	if err != nil {
		return formatZFSCommandError("zfs destroy -r "+dataset, stderr, err)
	}
	return nil
}

func zfsDestroyDatasetNonRecursive(ctx context.Context, runner zfsCommandRunner, dataset string) error {
	dataset = strings.TrimSpace(dataset)
	if err := requireZFSDatasetName(dataset); err != nil {
		return err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "destroy", dataset)
	if err != nil {
		return formatZFSCommandError("zfs destroy "+dataset, stderr, err)
	}
	return nil
}

func zfsDatasetMountpoint(ctx context.Context, runner zfsCommandRunner, dataset string) (string, error) {
	dataset = strings.TrimSpace(dataset)
	if err := requireZFSDatasetName(dataset); err != nil {
		return "", err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	stdout, stderr, err := runner(ctx, "get", "-H", "-o", "value", "mountpoint", dataset)
	if err != nil {
		return "", formatZFSCommandError("zfs get mountpoint "+dataset, stderr, err)
	}
	mountpoint := strings.TrimSpace(stdout)
	if mountpoint == "" || mountpoint == "-" || mountpoint == "none" || mountpoint == "legacy" {
		return "", fmt.Errorf("unusable ZFS mountpoint %q for dataset %q", mountpoint, dataset)
	}
	return mountpoint, nil
}

func requireZFSSnapshotName(snapshotName string) error {
	if !strings.Contains(snapshotName, "@") {
		return fmt.Errorf("snapshot name must include @")
	}
	return nil
}

func requireZFSDatasetName(dataset string) error {
	if dataset == "" {
		return fmt.Errorf("dataset name must not be blank")
	}
	if strings.Contains(dataset, "@") {
		return fmt.Errorf("dataset name must not include @")
	}
	return nil
}
