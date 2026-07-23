// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmJailerUpgradePermissionFixture struct {
	vm      vmJailerUpgradeVM
	kernel  string
	initrd  string
	runtime string
	jailer  string
}

type vmJailerUpgradeDefaultPreflightFixture struct {
	cfg        *Config
	deps       vmJailerUpgradeDeps
	systemdDir string
	unitPath   string
	kernel     string
	configFile string
}

func TestNormalizeManagedVMJailerUpgradeArtifactsRepairsV09Modes(t *testing.T) {
	fixture := newVMJailerUpgradePermissionFixture(t)

	if err := normalizeManagedVMJailerUpgradeArtifacts(fixture.vm); err != nil {
		t.Fatalf("normalizeManagedVMJailerUpgradeArtifacts: %v", err)
	}

	assertVMJailerUpgradeMode(t, fixture.kernel, 0o644)
	assertVMJailerUpgradeMode(t, fixture.initrd, 0o644)
	assertVMJailerUpgradeMode(t, fixture.runtime, 0o755)
	assertVMJailerUpgradeMode(t, fixture.jailer, 0o755)
}

func TestNormalizeManagedVMJailerUpgradeArtifactsVerifiesBeforeMutation(t *testing.T) {
	fixture := newVMJailerUpgradePermissionFixture(t)
	if err := os.WriteFile(fixture.initrd, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper initrd: %v", err)
	}

	err := normalizeManagedVMJailerUpgradeArtifacts(fixture.vm)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want checksum mismatch", err)
	}
	assertVMJailerUpgradeMode(t, fixture.kernel, 0o600)
	assertVMJailerUpgradeMode(t, fixture.initrd, 0o600)
}

func TestNormalizeManagedVMJailerUpgradeArtifactsSkipsUnmanagedBundle(t *testing.T) {
	fixture := newVMJailerUpgradePermissionFixture(t)
	fixture.vm.NormalizeManagedArtifacts = false

	if err := normalizeManagedVMJailerUpgradeArtifacts(fixture.vm); err != nil {
		t.Fatalf("normalizeManagedVMJailerUpgradeArtifacts: %v", err)
	}

	assertVMJailerUpgradeMode(t, fixture.kernel, 0o600)
	assertVMJailerUpgradeMode(t, fixture.initrd, 0o600)
}

func TestShouldNormalizeManagedVMJailerUpgradeArtifacts(t *testing.T) {
	dataRoot := t.TempDir()
	managedDir := filepath.Join(dataRoot, "vm-images", "ubuntu-v1")
	manifest := vmImageManifest{Kernel: "vmlinux"}
	tests := []struct {
		name         string
		payload      string
		firecracker  string
		manifest     vmImageManifest
		localPayload bool
		want         bool
	}{
		{
			name: "managed official image", payload: "vm://ubuntu/26.04",
			firecracker: filepath.Join(managedDir, "firecracker"), manifest: manifest, want: true,
		},
		{
			name: "local import", payload: "vm://custom/image",
			firecracker: filepath.Join(managedDir, "firecracker"), manifest: manifest, localPayload: true,
		},
		{
			name: "manifestless image", payload: "vm://ubuntu/26.04",
			firecracker: filepath.Join(managedDir, "firecracker"),
		},
		{
			name: "outside managed cache", payload: "vm://ubuntu/26.04",
			firecracker: filepath.Join(dataRoot, "custom", "firecracker"), manifest: manifest,
		},
		{
			name: "non image payload", payload: "/tmp/image",
			firecracker: filepath.Join(managedDir, "firecracker"), manifest: manifest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldNormalizeManagedVMJailerUpgradeArtifacts(
				dataRoot,
				tt.payload,
				tt.firecracker,
				tt.manifest,
				func(string) bool { return tt.localPayload },
			)
			if got != tt.want {
				t.Fatalf("shouldNormalizeManagedVMJailerUpgradeArtifacts() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateVMJailerUpgradeNextStartRejectsUnreadableResource(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	restore := stubVMJailerTransitionValidation(t)
	t.Cleanup(restore)
	if err := os.Chmod(fixture.ConfigFile, 0o600); err != nil {
		t.Fatalf("chmod Firecracker config: %v", err)
	}
	store := db.NewStore(filepath.Join(t.TempDir(), "db.json"), filepath.Join(fixture.DataRoot, "services"))
	if err := store.Set(fixture.Data.AsStruct()); err != nil {
		t.Fatalf("write transition fixture database: %v", err)
	}
	cfg := &Config{RootDir: fixture.DataRoot, ServicesRoot: filepath.Join(fixture.DataRoot, "services"), DB: store}
	vm := vmJailerUpgradeVM{Service: fixture.Service, ServiceRoot: fixture.ServiceRoot}

	err := validateVMJailerUpgradeNextStart(
		context.Background(), cfg, vm, vmRuntimeIdentity{UID: 812, GID: 813},
	)
	if err == nil || !strings.Contains(err.Error(), "is not readable by the VM runtime") {
		t.Fatalf("error = %v, want unreadable resource failure", err)
	}
}

func TestPrepareVMJailerUpgradeDefaultPreflightRepairsManagedBundle(t *testing.T) {
	restore := stubVMJailerTransitionValidation(t)
	t.Cleanup(restore)
	fixture := newVMJailerUpgradeDefaultPreflightFixture(t)

	tx, err := prepareVMJailerUpgradeWithDeps(context.Background(), fixture.cfg, fixture.deps)
	if err != nil {
		t.Fatalf("prepareVMJailerUpgradeWithDeps: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })

	assertVMJailerUpgradeMode(t, fixture.kernel, 0o644)
	if len(tx.units) != 1 {
		t.Fatalf("staged unit count = %d, want 1", len(tx.units))
	}
	if raw, readErr := os.ReadFile(fixture.unitPath); readErr != nil || string(raw) != "old-devbox" {
		t.Fatalf("live unit = %q, %v; want old-devbox", raw, readErr)
	}
}

func TestPrepareVMJailerUpgradeDefaultPreflightRepairsExplicitUnadoptedBundle(t *testing.T) {
	restore := stubVMJailerTransitionValidation(t)
	t.Cleanup(restore)
	fixture := newVMJailerUpgradeDefaultPreflightFixture(t)
	fixture.deps.preJailerUnit = func(context.Context, string) (bool, error) { return false, nil }

	tx, err := prepareVMJailerUpgradeWithDeps(context.Background(), fixture.cfg, fixture.deps)
	if err != nil {
		t.Fatalf("prepareVMJailerUpgradeWithDeps: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })

	assertVMJailerUpgradeMode(t, fixture.kernel, 0o644)
	if len(tx.units) != 0 {
		t.Fatalf("staged unit count = %d, want 0 for explicit unit", len(tx.units))
	}
	if raw, readErr := os.ReadFile(fixture.unitPath); readErr != nil || string(raw) != "old-devbox" {
		t.Fatalf("live unit = %q, %v; want old-devbox", raw, readErr)
	}
}

func TestPrepareVMJailerUpgradeDefaultPreflightFailureLeavesUnitUnstaged(t *testing.T) {
	restore := stubVMJailerTransitionValidation(t)
	t.Cleanup(restore)
	fixture := newVMJailerUpgradeDefaultPreflightFixture(t)
	if err := os.Chmod(fixture.configFile, 0o600); err != nil {
		t.Fatalf("chmod Firecracker config: %v", err)
	}

	tx, err := prepareVMJailerUpgradeWithDeps(context.Background(), fixture.cfg, fixture.deps)
	if tx != nil {
		_ = tx.Close()
		t.Fatal("preflight failure returned a unit transaction")
	}
	if err == nil || !strings.Contains(err.Error(), "is not readable by the VM runtime") {
		t.Fatalf("error = %v, want unreadable resource failure", err)
	}
	assertVMJailerUpgradeMode(t, fixture.kernel, 0o644)
	if raw, readErr := os.ReadFile(fixture.unitPath); readErr != nil || string(raw) != "old-devbox" {
		t.Fatalf("live unit = %q, %v; want old-devbox", raw, readErr)
	}
	entries, readErr := os.ReadDir(fixture.systemdDir)
	if readErr != nil {
		t.Fatalf("read systemd directory: %v", readErr)
	}
	if got := vmJailerUpgradeEntryNames(entries); len(got) != 1 || got[0] != filepath.Base(fixture.unitPath) {
		t.Fatalf("systemd entries = %v, want live unit only", got)
	}
}

func newVMJailerUpgradePermissionFixture(t *testing.T) vmJailerUpgradePermissionFixture {
	t.Helper()
	dir := t.TempDir()
	contents := vmImageTestContents()
	contents["initrd.img"] = []byte("initrd")
	contents["jailer"] = []byte("jailer")
	manifest := vmImageTestManifest("ubuntu-test-v1", contents)
	manifest.Initrd = "initrd.img"
	manifest.Jailer = "jailer"
	manifest.Checksums[manifest.Initrd] = testSHA256Hex(contents[manifest.Initrd])
	manifest.Checksums[manifest.Jailer] = testSHA256Hex(contents[manifest.Jailer])
	if err := writeManifestFile(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	modes := map[string]os.FileMode{
		manifest.Kernel:      0o600,
		manifest.Initrd:      0o600,
		manifest.Firecracker: 0o700,
		manifest.Jailer:      0o700,
	}
	for name, mode := range modes {
		if err := os.WriteFile(filepath.Join(dir, name), contents[name], mode); err != nil {
			t.Fatalf("write artifact %s: %v", name, err)
		}
	}
	return vmJailerUpgradePermissionFixture{
		vm: vmJailerUpgradeVM{
			Firecracker:               filepath.Join(dir, manifest.Firecracker),
			Jailer:                    filepath.Join(dir, manifest.Jailer),
			Manifest:                  manifest,
			NormalizeManagedArtifacts: true,
		},
		kernel:  filepath.Join(dir, manifest.Kernel),
		initrd:  filepath.Join(dir, manifest.Initrd),
		runtime: filepath.Join(dir, manifest.Firecracker),
		jailer:  filepath.Join(dir, manifest.Jailer),
	}
}

func newVMJailerUpgradeDefaultPreflightFixture(t *testing.T) vmJailerUpgradeDefaultPreflightFixture {
	t.Helper()
	transition := newVMJailerTransitionFixture(t)
	contents := make(map[string][]byte)
	for _, name := range []string{"kernel", "rootfs.ext4", "firecracker", "jailer"} {
		raw, err := os.ReadFile(filepath.Join(transition.ImageDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		contents[name] = raw
	}
	manifest := vmImageManifest{
		Name:         "managed-test-image",
		Version:      "managed-test-v1",
		Architecture: "amd64",
		Kernel:       "kernel",
		RootFS:       "rootfs.ext4",
		Firecracker:  "firecracker",
		RootFSSize:   1,
		Checksums: map[string]string{
			"kernel":      testSHA256Hex(contents["kernel"]),
			"rootfs.ext4": testSHA256Hex(contents["rootfs.ext4"]),
			"firecracker": testSHA256Hex(contents["firecracker"]),
		},
	}
	if err := writeManifestFile(filepath.Join(transition.ImageDir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write managed manifest: %v", err)
	}
	if err := os.Chmod(transition.Kernel, 0o600); err != nil {
		t.Fatalf("chmod legacy kernel: %v", err)
	}

	data := transition.Data.AsStruct()
	service := data.Services[transition.Service]
	service.VM.Image.Payload = testUbuntuVMPayload
	service.VM.Image.Version = manifest.Version
	service.VM.Image.RootFS = transition.RootFS
	store := db.NewStore(filepath.Join(t.TempDir(), "db.json"), filepath.Join(transition.DataRoot, "services"))
	if err := store.Set(data); err != nil {
		t.Fatalf("write legacy VM database: %v", err)
	}
	cfg := &Config{
		RootDir: transition.DataRoot, ServicesRoot: filepath.Join(transition.DataRoot, "services"), DB: store,
	}

	systemdDir := t.TempDir()
	unitPath := filepath.Join(systemdDir, vmSystemdUnitName(transition.Service))
	if err := os.WriteFile(unitPath, []byte("old-devbox"), 0o644); err != nil {
		t.Fatalf("write legacy unit: %v", err)
	}
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })

	deps := defaultVMJailerUpgradeDeps()
	deps.preJailerUnit = func(context.Context, string) (bool, error) { return true, nil }
	deps.sibling = func(_ context.Context, vm vmJailerUpgradeVM) (string, bool, error) {
		return vm.Jailer, true, nil
	}
	deps.readiness = func(string) (vmJailerReadiness, error) { return vmJailerPendingRestart, nil }
	deps.isRunning = func(*Server, string) (bool, error) { return false, nil }
	deps.renderUnit = func(vmSystemdConfig) (string, error) { return "new-devbox", nil }
	deps.ensureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	}
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	return vmJailerUpgradeDefaultPreflightFixture{
		cfg: cfg, deps: deps, systemdDir: systemdDir, unitPath: unitPath,
		kernel: transition.Kernel, configFile: transition.ConfigFile,
	}
}

func assertVMJailerUpgradeMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
