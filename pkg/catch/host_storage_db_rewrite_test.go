// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestHostStorageRewriteTargetDataStoreShieldsLegacyPreservedRoots(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "legacy-data")
	oldServicesRoot := filepath.Join(root, "operator-services")
	newDataDir := filepath.Join(root, "managed-data")
	newServicesRoot := filepath.Join(newDataDir, "services")
	apiRoot := filepath.Join(oldServicesRoot, "api")
	catchRoot := filepath.Join(oldServicesRoot, CatchService)
	customRoot := filepath.Join(oldDataDir, "custom", "database")
	zfsRoot := filepath.Join(oldDataDir, "mounts", "media")
	data := &db.Data{
		DataVersion: db.CurrentDataVersion,
		Services: map[string]*db.Service{
			"api": legacyPreservedRewriteTestService("api", apiRoot, ""),
			CatchService: {
				Name:        CatchService,
				ServiceType: db.ServiceTypeSystemd,
				ServiceRoot: catchRoot,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{db.Gen(1): filepath.Join(catchRoot, "bin", CatchService)}},
				},
			},
			"custom": legacyPreservedRewriteTestService("custom", customRoot, ""),
			"media":  legacyPreservedRewriteTestService("media", zfsRoot, "tank/apps/media"),
		},
	}
	api := data.Services["api"]
	api.Artifacts[db.ArtifactTSBinary] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(1): filepath.Join(oldDataDir, "tsd", "tailscaled"),
	}}
	api.VM.Image.RootFS = filepath.Join(oldDataDir, "vm-images", "ubuntu", "rootfs.ext4")
	targetStore := db.NewStore(filepath.Join(newDataDir, "db.json"), newServicesRoot)
	if err := targetStore.Set(data); err != nil {
		t.Fatalf("targetStore.Set: %v", err)
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: oldServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: newServicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		Legacy: catchrpc.HostStorageLegacyPlan{
			Eligible:       true,
			SourceRoot:     oldDataDir,
			TargetRoot:     newDataDir,
			PreservedRoots: []string{apiRoot, catchRoot, customRoot, zfsRoot},
		},
	}
	applier := &hostStorageApplier{config: Config{RootDir: oldDataDir, ServicesRoot: oldServicesRoot}}

	if err := applier.rewriteTargetDataStore(context.Background(), plan, io.Discard); err != nil {
		t.Fatalf("rewriteTargetDataStore: %v", err)
	}
	rewrittenStore := db.NewStore(filepath.Join(newDataDir, "db.json"), newServicesRoot)
	dv, err := rewrittenStore.Get()
	if err != nil {
		t.Fatalf("targetStore.Get: %v", err)
	}
	for name, preservedRoot := range map[string]string{
		"api":        apiRoot,
		CatchService: catchRoot,
		"custom":     customRoot,
		"media":      zfsRoot,
	} {
		service := dv.Services().Get(name).AsStruct()
		if service == nil {
			t.Fatalf("service %q missing after rewrite", name)
		}
		if service.ServiceRoot != preservedRoot {
			t.Fatalf("%s ServiceRoot = %q, want preserved %q", name, service.ServiceRoot, preservedRoot)
		}
		if got := service.Artifacts[db.ArtifactBinary].Refs[db.Gen(1)]; got != filepath.Join(preservedRoot, "bin", name) {
			t.Fatalf("%s binary ref = %q, want preserved root", name, got)
		}
		if service.VM != nil {
			if got := service.VM.Image.Kernel; got != filepath.Join(preservedRoot, "run", "vmlinux") {
				t.Fatalf("%s VM kernel = %q, want preserved root", name, got)
			}
			if got := service.VM.Disk.Path; got != filepath.Join(preservedRoot, "data", "rootfs.raw") {
				t.Fatalf("%s VM disk = %q, want preserved root", name, got)
			}
		}
	}
	api = dv.Services().Get("api").AsStruct()
	if got := api.Artifacts[db.ArtifactTSBinary].Refs[db.Gen(1)]; got != filepath.Join(newDataDir, "tsd", "tailscaled") {
		t.Fatalf("api non-preserved artifact ref = %q, want managed data dir", got)
	}
	if got := api.VM.Image.RootFS; got != filepath.Join(newDataDir, "vm-images", "ubuntu", "rootfs.ext4") {
		t.Fatalf("api non-preserved VM rootfs = %q, want managed data dir", got)
	}
}

func legacyPreservedRewriteTestService(name, root, dataset string) *db.Service {
	return &db.Service{
		Name:           name,
		ServiceType:    db.ServiceTypeVM,
		ServiceRoot:    root,
		ServiceRootZFS: dataset,
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{db.Gen(1): filepath.Join(root, "bin", name)}},
		},
		VM: &db.VMConfig{
			Image: db.VMImageConfig{Kernel: filepath.Join(root, "run", "vmlinux")},
			Disk:  db.VMDiskConfig{Path: filepath.Join(root, "data", "rootfs.raw")},
		},
	}
}

func TestRewriteHostStorageDataPaths(t *testing.T) {
	data := &db.Data{
		Services: map[string]*db.Service{
			"catch": {
				Name:       "catch",
				Generation: 7,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(7): "/root/data/services/catch/bin/catch",
						"latest":  "/root/data/services/catch/bin/catch.latest",
					}},
					db.ArtifactEnvFile: {Refs: map[db.ArtifactRef]string{
						"latest": "/root/database/file",
						"staged": "relative/env",
					}},
				},
			},
			"devbox": {
				Name: "devbox",
				VM: &db.VMConfig{
					Image: db.VMImageConfig{
						Kernel: "/root/data/services/devbox/run/kernels/vmlinux",
						RootFS: "/root/data/vm-images/ubuntu/rootfs.ext4",
					},
					Disk: db.VMDiskConfig{
						Path: "/root/data/services/devbox/data/rootfs.raw",
					},
					Console: db.VMConsoleConfig{
						SocketPath: "/root/data/services/devbox/run/serial.sock",
						LogPath:    "/root/data/services/devbox/log/serial.log",
					},
					Sockets: db.VMSocketConfig{
						APISocketPath:   "/root/data/services/devbox/run/api.sock",
						VsockSocketPath: "/root/data/services/devbox/run/vsock.sock",
					},
					PIDFile: "/root/data/services/devbox/run/firecracker.pid",
				},
			},
		},
	}

	result, err := rewriteHostStorageDataPaths(data, hostStoragePathMappings{
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
		{From: "/root/data/services", To: "/flash/yeet/services", Reason: hostStoragePathReasonServicesDir},
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	})
	if err != nil {
		t.Fatalf("rewriteHostStorageDataPaths error: %v", err)
	}
	if result.Changed != 10 {
		t.Fatalf("Changed = %d, want 10", result.Changed)
	}

	if got := data.Services["catch"].Artifacts[db.ArtifactBinary].Refs[db.Gen(7)]; got != "/flash/yeet/services/catch/bin/catch" {
		t.Fatalf("catch binary gen ref = %q", got)
	}
	if got := data.Services["catch"].Artifacts[db.ArtifactBinary].Refs["latest"]; got != "/flash/yeet/services/catch/bin/catch.latest" {
		t.Fatalf("catch binary latest ref = %q", got)
	}
	if got := data.Services["catch"].Artifacts[db.ArtifactEnvFile].Refs["latest"]; got != "/root/database/file" {
		t.Fatalf("sibling artifact ref = %q, want unchanged sibling path", got)
	}
	if got := data.Services["catch"].Artifacts[db.ArtifactEnvFile].Refs["staged"]; got != "relative/env" {
		t.Fatalf("relative artifact ref = %q, want unchanged relative path", got)
	}

	vm := data.Services["devbox"].VM
	if got := vm.Image.Kernel; got != "/flash/yeet/services/devbox/run/kernels/vmlinux" {
		t.Fatalf("VM kernel = %q", got)
	}
	if got := vm.Image.RootFS; got != "/flash/yeet/data/vm-images/ubuntu/rootfs.ext4" {
		t.Fatalf("VM rootfs = %q", got)
	}
	if got := vm.Disk.Path; got != "/flash/yeet/services/devbox/data/rootfs.raw" {
		t.Fatalf("VM disk = %q", got)
	}
	if got := vm.Console.SocketPath; got != "/flash/yeet/services/devbox/run/serial.sock" {
		t.Fatalf("VM console socket = %q", got)
	}
	if got := vm.Console.LogPath; got != "/flash/yeet/services/devbox/log/serial.log" {
		t.Fatalf("VM console log = %q", got)
	}
	if got := vm.Sockets.APISocketPath; got != "/flash/yeet/services/devbox/run/api.sock" {
		t.Fatalf("VM API socket = %q", got)
	}
	if got := vm.Sockets.VsockSocketPath; got != "/flash/yeet/services/devbox/run/vsock.sock" {
		t.Fatalf("VM vsock socket = %q", got)
	}
	if got := vm.PIDFile; got != "/flash/yeet/services/devbox/run/firecracker.pid" {
		t.Fatalf("VM PID file = %q", got)
	}

	systemdDir := t.TempDir()
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })
	cfg := Config{RootDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"}
	units, err := regenerateHostStorageVMSystemdUnit(
		context.Background(),
		cfg,
		data.Services["devbox"],
		"/flash/yeet/services/catch/run/catch",
	)
	if err != nil {
		t.Fatalf("regenerate host-storage VM unit: %v", err)
	}
	if len(units) != 1 || units[0] != vmSystemdUnitName("devbox") {
		t.Fatalf("regenerated units = %#v, want %q", units, vmSystemdUnitName("devbox"))
	}
	unitRaw, err := os.ReadFile(filepath.Join(systemdDir, vmSystemdUnitName("devbox")))
	if err != nil {
		t.Fatalf("read regenerated host-storage VM unit: %v", err)
	}
	unit := string(unitRaw)
	assertJailerOnlyVMUnit(t, unit)
	for _, want := range []string{
		"-data-dir /flash/yeet/data",
		"-services-root /flash/yeet/services",
		"--service-root /flash/yeet/services/devbox",
		"--disk-path " + vm.Disk.Path,
		"--firecracker /flash/yeet/data/vm-images/ubuntu/firecracker",
		"--jailer /flash/yeet/data/vm-images/ubuntu/jailer",
		"--jailer-base /flash/yeet/data/vm-jailer",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("regenerated host-storage VM unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRewriteHostStorageDataPathsDataDirOnlyLeavesServiceRootPaths(t *testing.T) {
	data := &db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:       "api",
				Generation: 3,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(3): "/old-data/services/api/bin/api",
					}},
					db.ArtifactTSBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(3): "/old-data/tsd/tailscaled",
					}},
				},
				VM: &db.VMConfig{
					Image: db.VMImageConfig{
						Kernel: "/old-data/services/api/run/kernels/vmlinux",
						RootFS: "/old-data/vm-images/ubuntu/rootfs.ext4",
					},
					Disk: db.VMDiskConfig{
						Path: "/old-data/services/api/data/rootfs.raw",
					},
				},
			},
			"catalog": {
				Name: "catalog",
				VM: &db.VMConfig{
					Image: db.VMImageConfig{
						Kernel: "/old-data/vm-images/catalog/vmlinux",
						RootFS: "/old-data/vm-images/catalog/rootfs.ext4",
					},
				},
			},
		},
	}
	mappings := hostStorageDBRewriteMappingsFromPlan(catchrpc.HostStoragePlan{
		Current:       catchrpc.HostStorageState{DataDir: "/old-data", ServicesRoot: "/old-data/services"},
		Desired:       catchrpc.HostStorageState{DataDir: "/new-data", ServicesRoot: "/old-data/services"},
		DataDirAction: catchrpc.HostStorageDataDirAction{Move: true, From: "/old-data", To: "/new-data"},
	})

	result, err := rewriteHostStorageDataPaths(data, mappings)
	if err != nil {
		t.Fatalf("rewriteHostStorageDataPaths error: %v", err)
	}
	if result.Changed != 4 {
		t.Fatalf("Changed = %d, want 4", result.Changed)
	}
	service := data.Services["api"]
	if got := service.Artifacts[db.ArtifactBinary].Refs[db.Gen(3)]; got != "/old-data/services/api/bin/api" {
		t.Fatalf("artifact ref = %q, want unchanged service-root path", got)
	}
	if got := service.Artifacts[db.ArtifactTSBinary].Refs[db.Gen(3)]; got != "/new-data/tsd/tailscaled" {
		t.Fatalf("tailscaled artifact ref = %q", got)
	}
	if got := service.VM.Image.Kernel; got != "/old-data/services/api/run/kernels/vmlinux" {
		t.Fatalf("VM kernel = %q, want unchanged service-root path", got)
	}
	if got := service.VM.Disk.Path; got != "/old-data/services/api/data/rootfs.raw" {
		t.Fatalf("VM disk = %q, want unchanged service-root path", got)
	}
	if got := service.VM.Image.RootFS; got != "/new-data/vm-images/ubuntu/rootfs.ext4" {
		t.Fatalf("VM rootfs = %q", got)
	}
	catalog := data.Services["catalog"]
	if got := catalog.VM.Image.Kernel; got != "/new-data/vm-images/catalog/vmlinux" {
		t.Fatalf("catalog VM kernel = %q", got)
	}
	if got := catalog.VM.Image.RootFS; got != "/new-data/vm-images/catalog/rootfs.ext4" {
		t.Fatalf("catalog VM rootfs = %q", got)
	}
}
