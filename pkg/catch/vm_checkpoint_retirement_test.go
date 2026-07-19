// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestValidateNoRetiredVMCheckpoints(t *testing.T) {
	tests := []struct {
		name      string
		items     []retiredVMCheckpoint
		wantError bool
	}{
		{name: "empty inventory"},
		{
			name: "full snapshot and checkpoint directory",
			items: []retiredVMCheckpoint{
				{Service: "zeta", Directory: "/srv/zeta/data/checkpoints/yeet-old"},
				{Service: "alpha", Snapshot: "pool/vms/alpha@yeet-old"},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoRetiredVMCheckpoints(tt.items)
			if (err != nil) != tt.wantError {
				t.Fatalf("validateNoRetiredVMCheckpoints error = %v, want error %t", err, tt.wantError)
			}
			if !tt.wantError {
				return
			}
			for _, want := range []string{
				"alpha: ZFS snapshot pool/vms/alpha@yeet-old is tagged full",
				"zeta: memory checkpoint directory /srv/zeta/data/checkpoints/yeet-old exists",
				"archive or remove memory/VMM-state files",
				"set com.yeetrun:checkpoint=disk",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err, want)
				}
			}
			alpha := strings.Index(err.Error(), "alpha: ZFS snapshot")
			zeta := strings.Index(err.Error(), "zeta: memory checkpoint")
			if alpha < 0 || zeta < 0 || alpha > zeta {
				t.Fatalf("error is not deterministically sorted: %q", err)
			}
		})
	}
}

func TestInventoryRetiredVMCheckpoints(t *testing.T) {
	dataRoot := t.TempDir()
	servicesRoot := filepath.Join(dataRoot, "services")
	alphaSnapshot := "pool/vms/alpha/root@yeet-full"
	server := &Server{cfg: Config{RootDir: dataRoot, ServicesRoot: servicesRoot}}
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch args[len(args)-1] {
		case "pool/vms/alpha/root":
			return alphaSnapshot + "\t1\tcatch\talpha\tvm-manual\t0\tfull checkpoint\tfull\tfalse\n", "", nil
		case "pool/vms/beta/root":
			return "pool/vms/beta/root@yeet-disk\t1\tcatch\tbeta\tvm-manual\t0\tdisk checkpoint\tdisk\tfalse\n", "", nil
		case "pool/vms/zeta/root":
			return "", "", nil
		default:
			t.Fatalf("unexpected ZFS dataset %q", args[len(args)-1])
			return "", "", nil
		}
	}
	zetaRoot := filepath.Join(dataRoot, "custom", "zeta")
	zetaCheckpoint := filepath.Join(serviceDataDirForRoot(zetaRoot), "checkpoints", "yeet-old")
	if err := os.MkdirAll(zetaCheckpoint, 0o700); err != nil {
		t.Fatalf("MkdirAll checkpoint directory: %v", err)
	}
	services := map[string]*db.Service{
		"alpha": vmCheckpointRetirementTestService("alpha", "", "/dev/zvol/pool/vms/alpha/root"),
		"beta":  vmCheckpointRetirementTestService("beta", "", "/dev/zvol/pool/vms/beta/root"),
		"zeta":  vmCheckpointRetirementTestService("zeta", zetaRoot, "/dev/zvol/pool/vms/zeta/root"),
		"web":   {Name: "web", ServiceType: db.ServiceTypeSystemd},
	}

	got, err := inventoryRetiredVMCheckpoints(context.Background(), server, services)
	if err != nil {
		t.Fatalf("inventoryRetiredVMCheckpoints: %v", err)
	}
	want := []retiredVMCheckpoint{
		{Service: "alpha", Snapshot: alphaSnapshot},
		{Service: "zeta", Directory: zetaCheckpoint},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retired checkpoints = %#v, want %#v", got, want)
	}
}

func vmCheckpointRetirementTestService(name, root, disk string) *db.Service {
	return &db.Service{
		Name:        name,
		ServiceType: db.ServiceTypeVM,
		ServiceRoot: root,
		VM:          &db.VMConfig{Disk: db.VMDiskConfig{Backend: vmDiskBackendZVOL, Path: disk}},
	}
}
