// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveZFSServiceRootExistingDataset(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "apps")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mountpoint := filepath.Join(parent, "svc")

	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		switch strings.Join(args, " ") {
		case "list -H -o name tank/apps/svc":
			return "tank/apps/svc\n", "", nil
		case "get -H -o value mountpoint tank/apps/svc":
			return mountpoint + "\n", "", nil
		default:
			return "", "", errors.New("unexpected zfs command")
		}
	}

	got, err := resolveZFSServiceRoot(context.Background(), runner, "tank/apps/svc", zfsServiceRootTarget)
	if err != nil {
		t.Fatalf("resolveZFSServiceRoot: %v", err)
	}
	if got.Dataset != "tank/apps/svc" || got.Root != mountpoint || !got.ZFS {
		t.Fatalf("resolved = %#v, want dataset tank/apps/svc root %s ZFS true", got, mountpoint)
	}
	wantCalls := [][]string{
		{"list", "-H", "-o", "name", "tank/apps/svc"},
		{"get", "-H", "-o", "value", "mountpoint", "tank/apps/svc"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestResolveZFSServiceRootCreatesMissingDataset(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "apps")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mountpoint := filepath.Join(parent, "svc")

	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		switch strings.Join(args, " ") {
		case "list -H -o name tank/apps/svc":
			return "", "cannot open 'tank/apps/svc': dataset does not exist\n", errZFSCommandFailed
		case "create tank/apps/svc":
			return "", "", nil
		case "get -H -o value mountpoint tank/apps/svc":
			return mountpoint + "\n", "", nil
		default:
			return "", "", errors.New("unexpected zfs command")
		}
	}

	got, err := resolveZFSServiceRoot(context.Background(), runner, "tank/apps/svc", zfsServiceRootTarget)
	if err != nil {
		t.Fatalf("resolveZFSServiceRoot: %v", err)
	}
	if got.Root != mountpoint {
		t.Fatalf("Root = %q, want %s", got.Root, mountpoint)
	}
	wantCalls := [][]string{
		{"list", "-H", "-o", "name", "tank/apps/svc"},
		{"create", "tank/apps/svc"},
		{"get", "-H", "-o", "value", "mountpoint", "tank/apps/svc"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestResolveZFSServiceRootExistingModeAllowsNonEmptyDirectory(t *testing.T) {
	mountpoint := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountpoint, "existing.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runner := func(ctx context.Context, args ...string) (string, string, error) {
		switch strings.Join(args, " ") {
		case "list -H -o name tank/apps/svc":
			return "tank/apps/svc\n", "", nil
		case "get -H -o value mountpoint tank/apps/svc":
			return mountpoint + "\n", "", nil
		default:
			return "", "", errors.New("unexpected zfs command")
		}
	}

	got, err := resolveZFSServiceRoot(context.Background(), runner, "tank/apps/svc", zfsServiceRootExisting)
	if err != nil {
		t.Fatalf("resolveZFSServiceRoot: %v", err)
	}
	if got.Root != mountpoint {
		t.Fatalf("Root = %q, want %s", got.Root, mountpoint)
	}
}

func TestResolveZFSServiceRootErrors(t *testing.T) {
	tests := []struct {
		name    string
		runner  zfsCommandRunner
		mode    zfsServiceRootMode
		wantErr string
	}{
		{
			name: "create fails",
			runner: func(ctx context.Context, args ...string) (string, string, error) {
				if strings.Join(args, " ") == "list -H -o name tank/apps/svc" {
					return "", "dataset does not exist", errZFSCommandFailed
				}
				return "", "parent does not exist", errZFSCommandFailed
			},
			mode:    zfsServiceRootTarget,
			wantErr: "zfs create tank/apps/svc failed: parent does not exist",
		},
		{
			name: "legacy mountpoint",
			runner: func(ctx context.Context, args ...string) (string, string, error) {
				if strings.Join(args, " ") == "list -H -o name tank/apps/svc" {
					return "tank/apps/svc\n", "", nil
				}
				return "legacy\n", "", nil
			},
			mode:    zfsServiceRootTarget,
			wantErr: "unsupported ZFS mountpoint",
		},
		{
			name: "relative mountpoint",
			runner: func(ctx context.Context, args ...string) (string, string, error) {
				if strings.Join(args, " ") == "list -H -o name tank/apps/svc" {
					return "tank/apps/svc\n", "", nil
				}
				return "relative/path\n", "", nil
			},
			mode:    zfsServiceRootTarget,
			wantErr: "ZFS mountpoint",
		},
		{
			name: "existing mode missing mountpoint",
			runner: func(ctx context.Context, args ...string) (string, string, error) {
				if strings.Join(args, " ") == "list -H -o name tank/apps/svc" {
					return "tank/apps/svc\n", "", nil
				}
				return filepath.Join(t.TempDir(), "missing") + "\n", "", nil
			},
			mode:    zfsServiceRootExisting,
			wantErr: "failed to stat ZFS mountpoint",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveZFSServiceRoot(context.Background(), tt.runner, "tank/apps/svc", tt.mode)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("resolveZFSServiceRoot error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
