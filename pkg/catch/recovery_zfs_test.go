// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type recordingZFSRunner struct {
	calls  [][]string
	stdout string
	stderr string
	err    error
}

func newRecordingZFSRunner() *recordingZFSRunner {
	return &recordingZFSRunner{}
}

func (r *recordingZFSRunner) run(ctx context.Context, args ...string) (string, string, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	return r.stdout, r.stderr, r.err
}

func (r *recordingZFSRunner) assertCommand(t *testing.T, want []string) {
	t.Helper()
	if !reflect.DeepEqual(r.calls, [][]string{want}) {
		t.Fatalf("zfs calls = %#v, want %#v", r.calls, [][]string{want})
	}
}

func (r *recordingZFSRunner) assertNoCommand(t *testing.T) {
	t.Helper()
	if len(r.calls) != 0 {
		t.Fatalf("zfs calls = %#v, want no calls", r.calls)
	}
}

func TestZFSCloneSnapshotRunsExactArgv(t *testing.T) {
	runner := newRecordingZFSRunner()
	err := zfsCloneSnapshot(context.Background(), runner.run, "tank/app@yeet-a", "tank/app-copy")
	if err != nil {
		t.Fatalf("zfsCloneSnapshot: %v", err)
	}
	runner.assertCommand(t, []string{"clone", "tank/app@yeet-a", "tank/app-copy"})
}

func TestZFSRollbackSnapshotRunsExactArgv(t *testing.T) {
	runner := newRecordingZFSRunner()
	err := zfsRollbackSnapshot(context.Background(), runner.run, "tank/app@yeet-a")
	if err != nil {
		t.Fatalf("zfsRollbackSnapshot: %v", err)
	}
	runner.assertCommand(t, []string{"rollback", "tank/app@yeet-a"})
}

func TestZFSDestroyDatasetRunsExactArgv(t *testing.T) {
	runner := newRecordingZFSRunner()
	err := zfsDestroyDataset(context.Background(), runner.run, "tank/app-copy")
	if err != nil {
		t.Fatalf("zfsDestroyDataset: %v", err)
	}
	runner.assertCommand(t, []string{"destroy", "-r", "tank/app-copy"})
}

func TestZFSDatasetMountpointRunsExactArgv(t *testing.T) {
	runner := newRecordingZFSRunner()
	runner.stdout = "/flash/yeet/app\n"
	got, err := zfsDatasetMountpoint(context.Background(), runner.run, "flash/yeet/app")
	if err != nil {
		t.Fatalf("zfsDatasetMountpoint: %v", err)
	}
	if got != "/flash/yeet/app" {
		t.Fatalf("mountpoint = %q, want /flash/yeet/app", got)
	}
	runner.assertCommand(t, []string{"get", "-H", "-o", "value", "mountpoint", "flash/yeet/app"})
}

func TestZFSHelpersRejectUnsafeNames(t *testing.T) {
	tests := []struct {
		name    string
		call    func(context.Context, zfsCommandRunner) error
		wantErr string
	}{
		{
			name: "clone snapshot without at",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsCloneSnapshot(ctx, runner, "tank/app", "tank/app-copy")
			},
			wantErr: "snapshot name must include @",
		},
		{
			name: "clone blank target dataset",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsCloneSnapshot(ctx, runner, "tank/app@yeet-a", " ")
			},
			wantErr: "dataset name must not be blank",
		},
		{
			name: "clone target snapshot name",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsCloneSnapshot(ctx, runner, "tank/app@yeet-a", "tank/app@snap")
			},
			wantErr: "dataset name must not include @",
		},
		{
			name: "rollback snapshot without at",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsRollbackSnapshot(ctx, runner, "tank/app")
			},
			wantErr: "snapshot name must include @",
		},
		{
			name: "destroy snapshot name",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsDestroyDataset(ctx, runner, "tank/app@yeet-a")
			},
			wantErr: "dataset name must not include @",
		},
		{
			name: "destroy blank dataset",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsDestroyDataset(ctx, runner, " ")
			},
			wantErr: "dataset name must not be blank",
		},
		{
			name: "mountpoint blank dataset",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				_, err := zfsDatasetMountpoint(ctx, runner, " ")
				return err
			},
			wantErr: "dataset name must not be blank",
		},
		{
			name: "mountpoint snapshot name",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				_, err := zfsDatasetMountpoint(ctx, runner, "tank/app@snap")
				return err
			},
			wantErr: "dataset name must not include @",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newRecordingZFSRunner()
			err := tt.call(context.Background(), runner.run)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			runner.assertNoCommand(t)
		})
	}
}

func TestZFSDatasetMountpointRejectsUnusableMountpoints(t *testing.T) {
	tests := []string{"", "  \n", "-\n", "none\n", "legacy\n"}
	for _, stdout := range tests {
		t.Run(strings.TrimSpace(stdout), func(t *testing.T) {
			runner := newRecordingZFSRunner()
			runner.stdout = stdout
			_, err := zfsDatasetMountpoint(context.Background(), runner.run, "flash/yeet/app")
			if err == nil || !strings.Contains(err.Error(), "unusable ZFS mountpoint") {
				t.Fatalf("zfsDatasetMountpoint error = %v, want unusable mountpoint", err)
			}
			runner.assertCommand(t, []string{"get", "-H", "-o", "value", "mountpoint", "flash/yeet/app"})
		})
	}
}

func TestZFSHelperErrorsPreserveStderr(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, zfsCommandRunner) error
	}{
		{
			name: "clone",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsCloneSnapshot(ctx, runner, "tank/app@yeet-a", "tank/app-copy")
			},
		},
		{
			name: "rollback",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsRollbackSnapshot(ctx, runner, "tank/app@yeet-a")
			},
		},
		{
			name: "destroy",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				return zfsDestroyDataset(ctx, runner, "tank/app-copy")
			},
		},
		{
			name: "mountpoint",
			call: func(ctx context.Context, runner zfsCommandRunner) error {
				_, err := zfsDatasetMountpoint(ctx, runner, "tank/app")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newRecordingZFSRunner()
			runner.stderr = "zfs said no\n"
			runner.err = errors.New("exit status 1")
			err := tt.call(context.Background(), runner.run)
			if err == nil || !strings.Contains(err.Error(), "zfs said no") {
				t.Fatalf("error = %v, want stderr preserved", err)
			}
		})
	}
}
