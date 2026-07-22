// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMKernelSyncManuallyReconcilesComponentLockWithRestart(t *testing.T) {
	fixture := newVMKernelComponentSyncFixture(t)
	server := newTestServer(t)
	server.cfg.RootDir = fixture.dataRoot
	server.cfg.ServicesRoot = filepath.Join(fixture.dataRoot, "services")
	server.cfg.DB = db.NewStore(filepath.Join(fixture.dataRoot, "db.json"), server.cfg.ServicesRoot)
	withVMKernelComponentCatalog(t, fixture.catalog, fixture.manifest, nil)
	withVMKernelSyncRunner(t, mountedGuestKernelComponentRunner(t, fixture.selector(false), fixture.kernel, fixture.config))
	withVMKernelSyncRunningCheck(t, func(*Server, string) (bool, error) { return true, nil })
	var systemctlCalls [][]string
	withVMKernelSyncSystemctl(t, func(args ...string) error {
		systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
		return nil
	})

	if err := server.syncVMGuestKernel(context.Background(), "devbox", cli.VMKernelFlags{Restart: true}); err != nil {
		t.Fatalf("syncVMGuestKernel: %v", err)
	}

	wantCalls := [][]string{{"stop", vmSystemdUnitName("devbox")}, {"restart", vmSystemdUnitName("devbox")}}
	if !reflect.DeepEqual(systemctlCalls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, wantCalls)
	}
	service := readVMKernelComponentSyncService(t, fixture.dataRoot)
	got := service.VM.Components.Kernel
	if got.ID != fixture.ref.KernelID || got.ManifestSHA256 != fixture.ref.ManifestSHA256 || got.SHA256 != fixture.manifest.VMLinux.SHA256 || got.Source != "official" {
		t.Fatalf("component kernel = %#v, want canonical catalog selection", got)
	}
	if service.VM.Components.Runtime.Configured != fixture.runtime.Configured {
		t.Fatalf("runtime changed during manual guest kernel sync: got %#v want %#v", service.VM.Components.Runtime, fixture.runtime)
	}
}

func TestAutoSyncVMGuestKernelComponentLock(t *testing.T) {
	fixture := newVMKernelComponentSyncFixture(t)
	withVMKernelComponentCatalog(t, fixture.catalog, fixture.manifest, nil)
	withVMKernelSyncRunner(t, mountedGuestKernelComponentRunner(t, fixture.selector(false), fixture.kernel, fixture.config))

	if err := AutoSyncVMGuestKernelOnReboot(context.Background(), fixture.consoleConfig()); err != nil {
		t.Fatalf("AutoSyncVMGuestKernelOnReboot: %v", err)
	}

	service := readVMKernelComponentSyncService(t, fixture.dataRoot)
	got := service.VM.Components.Kernel
	if got.ID != fixture.ref.KernelID || got.ManifestSHA256 != fixture.ref.ManifestSHA256 || got.SHA256 != fixture.manifest.VMLinux.SHA256 || got.Source != "official" {
		t.Fatalf("component kernel = %#v, want canonical catalog selection", got)
	}
	wantPath := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), "kernels", fixture.ref.KernelID, fixture.ref.ManifestSHA256, vmKernelFilename)
	if got.Path != wantPath || service.VM.Image.Kernel != wantPath {
		t.Fatalf("kernel paths = component %q image %q, want %q", got.Path, service.VM.Image.Kernel, wantPath)
	}
	if err := verifyFileSHA256(wantPath, fixture.manifest.VMLinux.SHA256); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, fixture.configPath, wantPath)
	assertFileContains(t, fixture.descriptorPath, fixture.runtime.Configured.ID)
	if service.VM.Components.Runtime.Configured != fixture.runtime.Configured {
		t.Fatalf("runtime changed during guest kernel sync: got %#v want %#v", service.VM.Components.Runtime, fixture.runtime)
	}
}

func TestAutoSyncVMGuestKernelRejectsGuestAuthority(t *testing.T) {
	fixture := newVMKernelComponentSyncFixture(t)
	catalogCalls := 0
	withVMKernelComponentCatalog(t, fixture.catalog, fixture.manifest, func() { catalogCalls++ })
	withVMKernelSyncRunner(t, mountedGuestKernelComponentRunner(t, fixture.selector(true), fixture.kernel, fixture.config))
	before := readVMKernelComponentSyncService(t, fixture.dataRoot)

	err := AutoSyncVMGuestKernelOnReboot(context.Background(), fixture.consoleConfig())
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("AutoSync error = %v, want guest authority rejection", err)
	}
	if catalogCalls != 0 {
		t.Fatalf("trusted catalog calls = %d, want none before rejecting guest authority", catalogCalls)
	}
	after := readVMKernelComponentSyncService(t, fixture.dataRoot)
	if !reflect.DeepEqual(before.VM.Components.Kernel, after.VM.Components.Kernel) || before.VM.Image.Kernel != after.VM.Image.Kernel {
		t.Fatalf("kernel state changed after guest authority rejection: before %#v after %#v", before.VM, after.VM)
	}
	assertFileContains(t, fixture.configPath, fixture.oldKernel.Path)
}

func TestAutoSyncVMGuestKernelAtomicFailure(t *testing.T) {
	fixture := newVMKernelComponentSyncFixture(t)
	withVMKernelComponentCatalog(t, fixture.catalog, fixture.manifest, nil)
	withVMKernelSyncRunner(t, mountedGuestKernelComponentRunner(t, fixture.selector(false), fixture.kernel, fixture.config))
	before := readVMKernelComponentSyncService(t, fixture.dataRoot)
	wantTarget := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), "kernels", fixture.ref.KernelID, fixture.ref.ManifestSHA256, vmKernelFilename)
	if err := os.MkdirAll(filepath.Dir(wantTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wantTarget, []byte("conflicting host file"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := AutoSyncVMGuestKernelOnReboot(context.Background(), fixture.consoleConfig())
	if err == nil || !strings.Contains(err.Error(), "already exists with different contents") {
		t.Fatalf("AutoSync error = %v, want atomic publication conflict", err)
	}
	after := readVMKernelComponentSyncService(t, fixture.dataRoot)
	if !reflect.DeepEqual(before.VM.Components.Kernel, after.VM.Components.Kernel) || before.VM.Image.Kernel != after.VM.Image.Kernel {
		t.Fatalf("kernel state changed after failed validation: before %#v after %#v", before.VM, after.VM)
	}
	assertFileContains(t, fixture.configPath, fixture.oldKernel.Path)
	assertFileContains(t, wantTarget, "conflicting host file")
}

func TestAutoSyncVMGuestKernelRejectsCatalogManifestMismatch(t *testing.T) {
	fixture := newVMKernelComponentSyncFixture(t)
	manifest := fixture.manifest
	manifest.VMLinux.SHA256 = strings.Repeat("f", 64)
	withVMKernelComponentCatalog(t, fixture.catalog, manifest, nil)
	withVMKernelSyncRunner(t, mountedGuestKernelComponentRunner(t, fixture.selector(false), fixture.kernel, fixture.config))
	before := readVMKernelComponentSyncService(t, fixture.dataRoot)

	err := AutoSyncVMGuestKernelOnReboot(context.Background(), fixture.consoleConfig())
	if err == nil || !strings.Contains(err.Error(), "does not match the trusted manifest") {
		t.Fatalf("AutoSync error = %v, want trusted manifest mismatch", err)
	}
	after := readVMKernelComponentSyncService(t, fixture.dataRoot)
	if !reflect.DeepEqual(before.VM.Components.Kernel, after.VM.Components.Kernel) || before.VM.Image.Kernel != after.VM.Image.Kernel {
		t.Fatalf("kernel state changed after catalog/manifest mismatch: before %#v after %#v", before.VM, after.VM)
	}
	assertFileContains(t, fixture.configPath, fixture.oldKernel.Path)
	wantTarget := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), "kernels", fixture.ref.KernelID, fixture.ref.ManifestSHA256, vmKernelFilename)
	if _, statErr := os.Lstat(wantTarget); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("kernel target exists after failed validation: %v", statErr)
	}
}

func TestAutoSyncVMGuestKernelLegacySelectorUsesPinnedComposition(t *testing.T) {
	fixture := newVMKernelComponentSyncFixture(t)
	record := vmLegacyCompositionRecord{
		Schema: vmLegacyCompositionSchema, SchemaVersion: vmLegacyCompositionSchemaVersion, Architecture: "amd64",
		GuestBase: vmLegacyGuestBaseIdentity{Name: "ubuntu", Version: "26-04", RootFSSHA256: strings.Repeat("1", 64)},
		Kernel:    vmLegacyKernelIdentity{Version: "linux-7-1-1-yeet", KernelSHA256: vmRuntimeSHA256Bytes(fixture.kernel), ConfigSHA256: vmRuntimeSHA256Bytes(fixture.config)},
		Runtime:   vmLegacyRuntimeIdentity{Version: "v1-16-1", FirecrackerSHA256: fixture.runtime.Configured.FirecrackerSHA256, JailerSHA256: fixture.runtime.Configured.JailerSHA256},
	}
	raw, provenanceSHA, err := canonicalVMLegacyComposition(record)
	if err != nil {
		t.Fatal(err)
	}
	guestID, kernelID, _, err := vmLegacyCompositionIDs(record, provenanceSHA)
	if err != nil {
		t.Fatal(err)
	}
	provenanceDir := filepath.Join(fixture.dataRoot, vmLegacyProvenanceDirName, vmLegacyProvenanceDigestDirName)
	if err := os.MkdirAll(provenanceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(provenanceDir, provenanceSHA+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyKernel := db.VMKernelArtifactConfig{
		ID: kernelID, ManifestSHA256: strings.Repeat("2", 64), SHA256: record.Kernel.KernelSHA256,
		Path: fixture.oldKernel.Path, Source: string(vmRuntimeAdoptionOfficialLegacy),
	}
	store := db.NewStore(filepath.Join(fixture.dataRoot, "db.json"), filepath.Join(fixture.dataRoot, "services"))
	if _, _, err := store.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.GuestBase = db.VMGuestBaseConfig{
			ID: guestID, ManifestSHA256: strings.Repeat("3", 64), Source: string(vmRuntimeAdoptionOfficialLegacy), RootFSProvenance: provenanceSHA,
		}
		service.VM.Components.Kernel = legacyKernel
		service.VM.Image.Kernel = legacyKernel.Path
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	catalogCalls := 0
	withVMKernelComponentCatalog(t, fixture.catalog, fixture.manifest, func() { catalogCalls++ })
	selector := map[string]any{
		"schema_version": 1, "version": "linux-7.1.1-yeet",
		"kernel":        "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config": "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":        map[string]string{"vmlinux": record.Kernel.KernelSHA256, "kernel.config": record.Kernel.ConfigSHA256},
	}
	selectorRaw, err := json.Marshal(selector)
	if err != nil {
		t.Fatal(err)
	}
	withVMKernelSyncRunner(t, mountedGuestKernelComponentRunner(t, selectorRaw, fixture.kernel, fixture.config))

	if err := AutoSyncVMGuestKernelOnReboot(context.Background(), fixture.consoleConfig()); err != nil {
		t.Fatalf("AutoSyncVMGuestKernelOnReboot: %v", err)
	}
	if catalogCalls != 0 {
		t.Fatalf("mutable kernel catalog was consulted %d times for a pinned legacy selector", catalogCalls)
	}
	service := readVMKernelComponentSyncService(t, fixture.dataRoot)
	got := service.VM.Components.Kernel
	if got.ID != legacyKernel.ID || got.ManifestSHA256 != legacyKernel.ManifestSHA256 || got.Source != legacyKernel.Source {
		t.Fatalf("legacy kernel identity changed: got %#v want %#v", got, legacyKernel)
	}
	wantPath := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), "kernels", legacyKernel.ID, legacyKernel.ManifestSHA256, vmKernelFilename)
	if got.Path != wantPath || service.VM.Image.Kernel != wantPath {
		t.Fatalf("legacy kernel paths = component %q image %q, want %q", got.Path, service.VM.Image.Kernel, wantPath)
	}
}

type vmKernelComponentSyncFixture struct {
	dataRoot       string
	serviceRoot    string
	configPath     string
	descriptorPath string
	kernel         []byte
	config         []byte
	ref            vmKernelCatalogRef
	catalog        vmKernelCatalog
	manifest       vmKernelManifest
	oldKernel      db.VMKernelArtifactConfig
	runtime        db.VMRuntimeLifecycleConfig
}

func newVMKernelComponentSyncFixture(t *testing.T) vmKernelComponentSyncFixture {
	t.Helper()
	dataRoot := t.TempDir()
	serviceRoot := filepath.Join(dataRoot, "services", "devbox")
	configPath := filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json")
	descriptorPath := filepath.Join(serviceDataDirForRoot(serviceRoot), vmRuntimeDescriptorFileName)
	kernel := []byte("trusted component kernel")
	config := []byte("CONFIG_VIRTIO=y\n")
	manifestDigest := strings.Repeat("b", 64)
	ref := vmKernelCatalogRef{
		KernelID: "kernel-linux-7.1.1-yeet-v1", UpstreamVersion: "7.1.1", PackagingRevision: 1,
		Architecture: "amd64", ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/kernel-linux-7.1.1-yeet-v1/kernel-manifest.json",
		ManifestSHA256: manifestDigest,
	}
	manifest := vmKernelManifest{
		SchemaVersion: 1, KernelID: ref.KernelID, UpstreamVersion: ref.UpstreamVersion,
		PackagingRevision: ref.PackagingRevision, Architecture: ref.Architecture,
		VMLinux:       vmKernelManifestAsset{URL: "https://github.com/yeetrun/yeet-vm-images/releases/download/kernel-linux-7.1.1-yeet-v1/vmlinux", SHA256: vmRuntimeSHA256Bytes(kernel)},
		Config:        vmKernelManifestAsset{URL: "https://github.com/yeetrun/yeet-vm-images/releases/download/kernel-linux-7.1.1-yeet-v1/kernel.config", SHA256: vmRuntimeSHA256Bytes(config)},
		GuestPackages: vmKernelManifestGuestPackages{CatalogURL: vmKernelPackageCatalogURL, SelectorSchemaVersion: 2, ReleaseID: ref.KernelID},
		Provenance:    vmComponentManifestProvenance{SourceCommit: strings.Repeat("d", 40), WorkflowRunURL: "https://github.com/yeetrun/yeet-vm-images/actions/runs/124"},
	}
	catalog := vmKernelCatalog{
		SchemaVersion: 1, Kernels: []vmKernelCatalogRef{ref},
		Channels: map[string]vmKernelCatalogChannels{"amd64": {Stable: &vmKernelCatalogIdentity{KernelID: ref.KernelID, ManifestSHA256: ref.ManifestSHA256}}},
	}
	oldPath := filepath.Join(serviceDataDirForRoot(serviceRoot), "kernels", "kernel-linux-6.1.1-yeet-v1", strings.Repeat("a", 64), vmKernelFilename)
	oldKernel := db.VMKernelArtifactConfig{ID: "kernel-linux-6.1.1-yeet-v1", ManifestSHA256: strings.Repeat("a", 64), SHA256: strings.Repeat("c", 64), Path: oldPath, Source: "official"}
	runtimeArtifact := vmRuntimeLaunchTestArtifact("v1.16.1", filepath.Join(dataRoot, "runtime"))
	runtime := db.VMRuntimeLifecycleConfig{Policy: "manual", Channel: "stable", Configured: runtimeArtifact}

	writeKernelSyncFirecrackerConfig(t, serviceRoot, oldPath, "")
	writeVMKernelSyncHostDB(t, dataRoot, serviceRoot, &db.VMComponentsConfig{
		GuestBase: db.VMGuestBaseConfig{ID: "guest-ubuntu-26.04-amd64-v1", ManifestSHA256: strings.Repeat("e", 64), Source: "official"},
		Kernel:    oldKernel, Runtime: runtime,
	})
	if _, _, err := db.NewStore(filepath.Join(dataRoot, "db.json"), filepath.Join(dataRoot, "services")).MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Image.Kernel = oldPath
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(descriptorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(vmRuntimeDescriptor{SchemaVersion: vmRuntimeDescriptorSchemaVersion, Service: "devbox", Configured: runtimeArtifact})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(descriptorPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return vmKernelComponentSyncFixture{
		dataRoot: dataRoot, serviceRoot: serviceRoot, configPath: configPath, descriptorPath: descriptorPath,
		kernel: kernel, config: config, ref: ref, catalog: catalog, manifest: manifest, oldKernel: oldKernel, runtime: runtime,
	}
}

func (f vmKernelComponentSyncFixture) selector(includeAuthority bool) []byte {
	selector := map[string]any{
		"schema_version":  2,
		"release_id":      f.ref.KernelID,
		"manifest_sha256": f.ref.ManifestSHA256,
		"version":         "linux-7.1.1-yeet",
		"kernel":          "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config":   "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":          map[string]string{"vmlinux": f.manifest.VMLinux.SHA256, "kernel.config": f.manifest.Config.SHA256},
	}
	if includeAuthority {
		selector["catalog_url"] = "https://guest.invalid/kernel-catalog.json"
		selector["channel"] = "candidate"
	}
	raw, err := json.Marshal(selector)
	if err != nil {
		panic(err)
	}
	return raw
}

func (f vmKernelComponentSyncFixture) consoleConfig() VMConsoleProxyConfig {
	return VMConsoleProxyConfig{
		Service: "devbox", ServiceRoot: f.serviceRoot, DiskPath: filepath.Join(f.serviceRoot, "rootfs.ext4"),
		ConfigFile: f.configPath, RuntimeDescriptor: f.descriptorPath, JailerBase: vmJailerBaseForDataRoot(f.dataRoot),
	}
}

func withVMKernelComponentCatalog(t *testing.T, catalog vmKernelCatalog, manifest vmKernelManifest, onCatalog func()) {
	t.Helper()
	oldCatalog := fetchVMKernelSyncCatalog
	oldManifest := fetchVMKernelSyncManifest
	fetchVMKernelSyncCatalog = func(context.Context) (vmKernelCatalog, error) {
		if onCatalog != nil {
			onCatalog()
		}
		return catalog, nil
	}
	fetchVMKernelSyncManifest = func(context.Context, string, vmKernelCatalogRef) (vmKernelManifest, error) {
		return manifest, nil
	}
	t.Cleanup(func() {
		fetchVMKernelSyncCatalog = oldCatalog
		fetchVMKernelSyncManifest = oldManifest
	})
}

func mountedGuestKernelComponentRunner(t *testing.T, selector, kernel, config []byte) vmCommandRunner {
	t.Helper()
	return func(_ context.Context, command []string) error {
		switch {
		case len(command) > 0 && command[0] == "sh":
			return nil
		case len(command) > 0 && command[0] == "mount":
			mountRoot := command[len(command)-1]
			kernelDir := filepath.Join(mountRoot, "usr/lib/yeet-vm/kernels/linux-7.1.1-yeet")
			if err := os.MkdirAll(kernelDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(kernelDir, vmKernelFilename), kernel, 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(kernelDir, vmKernelConfigFilename), config, 0o644); err != nil {
				return err
			}
			selectorDir := filepath.Join(mountRoot, "etc/yeet-vm/kernel")
			if err := os.MkdirAll(selectorDir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(selectorDir, "selected.json"), selector, 0o644)
		case len(command) == 2 && command[0] == "umount":
			return nil
		default:
			return errors.New("unexpected kernel sync command: " + strings.Join(command, " "))
		}
	}
}

func readVMKernelComponentSyncService(t *testing.T, dataRoot string) *db.Service {
	t.Helper()
	dv, err := db.NewStore(filepath.Join(dataRoot, "db.json"), filepath.Join(dataRoot, "services")).Get()
	if err != nil {
		t.Fatal(err)
	}
	return dv.Services().Get("devbox").AsStruct()
}
