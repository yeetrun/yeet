// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestReconcileVMRuntimeStateRepairsCustomRootDescriptorsAndUnitsWithOneReload(t *testing.T) {
	server := newTestServer(t)
	unitDir := filepath.Join(server.cfg.RootDir, "systemd")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldUnitDir := vmSystemdSystemDir
	vmSystemdSystemDir = unitDir
	t.Cleanup(func() { vmSystemdSystemDir = oldUnitDir })

	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	services := make(map[string]*db.Service)
	for _, name := range []string{"beta", "alpha"} {
		root := filepath.Join(server.cfg.RootDir, "custom", name)
		if err := os.MkdirAll(serviceDataDirForRoot(root), 0o755); err != nil {
			t.Fatal(err)
		}
		services[name] = reconcileVMRuntimeTestService(name, root, artifact)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: services}); err != nil {
		t.Fatal(err)
	}

	var systemctlCalls [][]string
	server.vmRuntimeReconcileDeps = &vmRuntimeReconcileDeps{
		descriptor: vmRuntimeDescriptorFileDeps{uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())},
		units: vmUnitTransactionDeps{
			unitUID: uint32(os.Geteuid()), unitGID: uint32(os.Getegid()),
			systemctl: func(args ...string) error {
				systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
				return nil
			},
		},
		runner: "/usr/local/bin/catch",
	}
	if err := server.reconcileVMRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(systemctlCalls, [][]string{{"daemon-reload"}}, slices.Equal) {
		t.Fatalf("systemctl calls = %#v", systemctlCalls)
	}
	for _, name := range []string{"alpha", "beta"} {
		service := services[name]
		path := filepath.Join(serviceDataDirForRoot(service.ServiceRoot), vmRuntimeDescriptorFileName)
		descriptor, err := readVMRuntimeDescriptorWithOwner(path, name, uint32(os.Geteuid()), uint32(os.Getegid()))
		if err != nil {
			t.Fatalf("read %s descriptor: %v", name, err)
		}
		if descriptor.Configured.ID != artifact.ID || descriptor.Trial || descriptor.Staged != nil {
			t.Fatalf("%s descriptor = %#v", name, descriptor)
		}
		unitPath := filepath.Join(unitDir, vmSystemdUnitName(name))
		unit, err := os.ReadFile(unitPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--runtime-descriptor", "--runtime-running-marker", "--runtime-trial-result", service.ServiceRoot} {
			if !strings.Contains(string(unit), want) {
				t.Fatalf("unit %s missing %q:\n%s", name, want, unit)
			}
		}
		if strings.Contains(string(unit), "--firecracker ") || strings.Contains(string(unit), "--jailer ") {
			t.Fatalf("unit %s retained explicit runtime binaries:\n%s", name, unit)
		}
	}

	// Exact derived state is a no-op and does not reload or restart anything.
	if err := server.reconcileVMRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(systemctlCalls) != 1 {
		t.Fatalf("exact reconcile called systemctl again: %#v", systemctlCalls)
	}

	wrongModeUnit := filepath.Join(unitDir, vmSystemdUnitName("beta"))
	if err := os.Chmod(wrongModeUnit, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.reconcileVMRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(systemctlCalls) != 2 {
		t.Fatalf("wrong-mode unit was not repaired with one reload: %#v", systemctlCalls)
	}
	info, err := os.Stat(wrongModeUnit)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("repaired unit mode = %04o, want 0644", info.Mode().Perm())
	}
}

func TestReconcileVMRuntimeStateRepairsDescriptorFromDBBeforeUnitPreparation(t *testing.T) {
	server := newTestServer(t)
	unitDir := filepath.Join(server.cfg.RootDir, "systemd")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldUnitDir := vmSystemdSystemDir
	vmSystemdSystemDir = unitDir
	t.Cleanup(func() { vmSystemdSystemDir = oldUnitDir })

	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	service := reconcileVMRuntimeTestService("devbox", filepath.Join(server.cfg.RootDir, "zfs-mounted-devbox"), artifact)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"devbox": service}}); err != nil {
		t.Fatal(err)
	}
	dataDir := serviceDataDirForRoot(service.ServiceRoot)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, vmRuntimeDescriptorFileName), []byte("corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.vmRuntimeReconcileDeps = &vmRuntimeReconcileDeps{
		descriptor: vmRuntimeDescriptorFileDeps{uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())},
		units: vmUnitTransactionDeps{
			unitUID: uint32(os.Geteuid()), unitGID: uint32(os.Getegid()),
			systemctl: func(args ...string) error {
				path := filepath.Join(dataDir, vmRuntimeDescriptorFileName)
				if _, err := readVMRuntimeDescriptorWithOwner(path, "devbox", uint32(os.Geteuid()), uint32(os.Getegid())); err != nil {
					t.Fatalf("descriptor was not repaired before unit publication: %v", err)
				}
				return nil
			},
		},
		runner: "/usr/local/bin/catch",
	}
	if err := server.reconcileVMRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileVMRuntimeStateWithoutAdoptedVMsIsNoOp(t *testing.T) {
	server := newTestServer(t)
	if err := server.reconcileVMRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileVMComponentKernelCompatibilityRepairsOnlyMissingImagePath(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(server.cfg.RootDir, "custom", "devbox")
	kernelID := "kernel-linux-7.1.1-yeet-v1"
	manifestSHA := strings.Repeat("a", 64)
	kernelPath := filepath.Join(serviceDataDirForRoot(root), "kernels", kernelID, manifestSHA, vmKernelFilename)
	if err := os.MkdirAll(filepath.Dir(kernelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("verified kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{
		Name: "devbox", ServiceType: db.ServiceTypeVM, ServiceRoot: root,
		VM: &db.VMConfig{Components: &db.VMComponentsConfig{Kernel: db.VMKernelArtifactConfig{
			ID: kernelID, ManifestSHA256: manifestSHA, SHA256: vmRuntimeSHA256Bytes([]byte("verified kernel")),
			Path: kernelPath, Source: "official",
		}}},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"devbox": service}}); err != nil {
		t.Fatal(err)
	}

	if err := server.reconcileVMComponentKernelCompatibility(); err != nil {
		t.Fatal(err)
	}
	stored, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Services().Get("devbox").VM().Image().Kernel; got != kernelPath {
		t.Fatalf("compatibility kernel = %q, want %q", got, kernelPath)
	}

	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Image.Kernel = "/operator/pinned/vmlinux"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.reconcileVMComponentKernelCompatibility(); err != nil {
		t.Fatal(err)
	}
	stored, err = server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Services().Get("devbox").VM().Image().Kernel; got != "/operator/pinned/vmlinux" {
		t.Fatalf("non-empty compatibility kernel was overwritten with %q", got)
	}
}

func TestReconcileVMComponentKernelCompatibilityRejectsUnverifiedComponent(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(server.cfg.RootDir, "services", "devbox")
	kernelID := "kernel-linux-7.1.1-yeet-v1"
	manifestSHA := strings.Repeat("a", 64)
	kernelPath := filepath.Join(serviceDataDirForRoot(root), "kernels", kernelID, manifestSHA, vmKernelFilename)
	if err := os.MkdirAll(filepath.Dir(kernelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("tampered kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{Name: "devbox", ServiceType: db.ServiceTypeVM, ServiceRoot: root, VM: &db.VMConfig{
		Components: &db.VMComponentsConfig{Kernel: db.VMKernelArtifactConfig{
			ID: kernelID, ManifestSHA256: manifestSHA, SHA256: strings.Repeat("b", 64), Path: kernelPath, Source: "official",
		}},
	}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"devbox": service}}); err != nil {
		t.Fatal(err)
	}

	err := server.reconcileVMComponentKernelCompatibility()
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("reconcile error = %v, want digest rejection", err)
	}
	stored, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got := stored.Services().Get("devbox").VM().Image().Kernel; got != "" {
		t.Fatalf("unverified compatibility kernel was persisted as %q", got)
	}
}

func reconcileVMRuntimeTestService(name, root string, artifact db.VMRuntimeArtifactConfig) *db.Service {
	runDir := serviceRunDirForRoot(root)
	return &db.Service{
		Name: name, ServiceType: db.ServiceTypeVM, ServiceRoot: root,
		VM: &db.VMConfig{
			Runtime: vmRuntimeFirecracker,
			Image:   db.VMImageConfig{RootFS: filepath.Join(root, "data", "rootfs.ext4")},
			Disk:    db.VMDiskConfig{Path: filepath.Join(root, "data", "disk.ext4")},
			Console: db.VMConsoleConfig{SocketPath: filepath.Join(runDir, "serial.sock")},
			Sockets: db.VMSocketConfig{APISocketPath: filepath.Join(runDir, "firecracker.sock"), VsockSocketPath: filepath.Join(runDir, "vsock.sock")},
			Components: &db.VMComponentsConfig{
				Runtime: db.VMRuntimeLifecycleConfig{Configured: artifact},
			},
		},
	}
}
