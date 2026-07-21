// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmKernelSyncRunner              vmCommandRunner
	vmKernelSyncSystemctlFunc       func(args ...string) error
	isServiceRunningForVMKernelSync = (*Server).IsServiceRunning
	syncVMGuestKernelFunc           = syncVMGuestKernelDefault
	fetchVMKernelSyncCatalog        = fetchTrustedVMKernelSyncCatalog
	fetchVMKernelSyncManifest       = fetchTrustedVMKernelSyncManifest
)

var errVMGuestKernelSelectorMissing = errors.New("VM guest kernel selector is missing")

type vmKernelSyncResult struct {
	Version        string
	HostKernelPath string
	HostConfigPath string
	Component      *db.VMKernelArtifactConfig
}

type vmKernelSyncTarget struct {
	service     *db.Service
	serviceRoot string
}

type vmKernelSyncRestart struct {
	systemctl func(args ...string) error
	unit      string
	restart   bool
	stopped   bool
}

func (s *Server) syncVMGuestKernel(ctx context.Context, name string, flags cli.VMKernelFlags) error {
	return syncVMGuestKernelFunc(ctx, s, name, flags)
}

func syncVMGuestKernelDefault(ctx context.Context, s *Server, name string, flags cli.VMKernelFlags) (retErr error) {
	var restart *vmKernelSyncRestart
	err := WithVMRuntimeTransactionLock(ctx, &s.cfg, func() error {
		target, err := s.vmKernelSyncTarget(name)
		if err != nil {
			return err
		}
		if target.service.VM.Components != nil {
			return fmt.Errorf("cannot sync the kernel for adopted VM %q until component-aware kernel reconciliation is available", name)
		}
		restart, err = s.prepareVMKernelSyncRestart(name, flags)
		if err != nil {
			return err
		}
		result, err := syncVMGuestKernelToHost(ctx, target)
		if err != nil {
			return err
		}
		return s.updateVMKernelDBPath(name, result.HostKernelPath)
	})
	if restart != nil {
		defer restart.restoreOnError(&retErr)
	}
	if err != nil {
		return err
	}
	return restart.finish(name)
}

func (s *Server) vmKernelSyncTarget(name string) (vmKernelSyncTarget, error) {
	dv, err := s.getDB()
	if err != nil {
		return vmKernelSyncTarget{}, err
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		return vmKernelSyncTarget{}, fmt.Errorf("service %q not found", name)
	}
	service := sv.AsStruct()
	if service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return vmKernelSyncTarget{}, fmt.Errorf("service %q is not a VM service", name)
	}
	return vmKernelSyncTarget{
		service:     service,
		serviceRoot: s.serviceRootFromView(sv),
	}, nil
}

func (s *Server) prepareVMKernelSyncRestart(name string, flags cli.VMKernelFlags) (*vmKernelSyncRestart, error) {
	runningCheck := isServiceRunningForVMKernelSync
	if runningCheck == nil {
		runningCheck = (*Server).IsServiceRunning
	}
	running, err := runningCheck(s, name)
	if err != nil {
		return nil, err
	}
	if running && !flags.Restart {
		return nil, fmt.Errorf("cannot sync VM kernel while %q is running; stop it first or pass --restart", name)
	}

	systemctl := vmKernelSyncSystemctlFunc
	if systemctl == nil {
		systemctl = runVMSystemctl
	}
	restart := &vmKernelSyncRestart{systemctl: systemctl, unit: vmSystemdUnitName(name), restart: flags.Restart}
	if running && flags.Restart {
		if err := systemctl("stop", restart.unit); err != nil {
			return nil, fmt.Errorf("stop VM %s before kernel sync: %w", name, err)
		}
		restart.stopped = true
	}
	return restart, nil
}

func (r *vmKernelSyncRestart) restoreOnError(retErr *error) {
	if *retErr != nil && r.stopped {
		*retErr = errors.Join(*retErr, r.systemctl("start", r.unit))
	}
}

func (r *vmKernelSyncRestart) finish(name string) error {
	if !r.restart {
		return nil
	}
	if err := r.systemctl("restart", r.unit); err != nil {
		return fmt.Errorf("restart VM %s after kernel sync: %w", name, err)
	}
	r.stopped = false
	return nil
}

func syncVMGuestKernelToHost(ctx context.Context, target vmKernelSyncTarget) (vmKernelSyncResult, error) {
	configPath := filepath.Join(serviceRunDirForRoot(target.serviceRoot), "firecracker.json")
	return syncVMGuestKernelSelectionToHost(ctx, target.serviceRoot, target.service.Name, target.service.VM.Disk.Path, configPath)
}

func AutoSyncVMGuestKernelOnReboot(ctx context.Context, cfg VMConsoleProxyConfig) error {
	service, serviceRoot, diskPath, configPath, err := resolveVMKernelAutoSyncConfig(cfg)
	if err != nil {
		return err
	}
	if service == "" || serviceRoot == "" || diskPath == "" || configPath == "" {
		return nil
	}
	dataRoot, err := vmKernelSyncDataRoot(cfg.JailerBase)
	if err != nil {
		return err
	}
	servicesRoot, err := vmKernelSyncServicesRoot(dataRoot, cfg.ServicesRoot)
	if err != nil {
		return err
	}
	err = autoSyncVMGuestKernelLocked(ctx, dataRoot, servicesRoot, service, serviceRoot, diskPath, configPath, strings.TrimSpace(cfg.RuntimeDescriptor) != "")
	if err != nil && errors.Is(err, errVMGuestKernelSelectorMissing) {
		return nil
	}
	return err
}

func autoSyncVMGuestKernelLocked(ctx context.Context, dataRoot, servicesRoot, service, serviceRoot, diskPath, configPath string, descriptorMode bool) error {
	return WithVMRuntimeRootLock(ctx, dataRoot, func() error {
		stored, err := vmKernelSyncStoredService(dataRoot, servicesRoot, service)
		if err != nil {
			return err
		}
		if stored == nil {
			if descriptorMode {
				return fmt.Errorf("automatic kernel sync for descriptor-managed VMs requires component-aware kernel reconciliation")
			}
			_, err := syncVMGuestKernelSelectionToHost(ctx, serviceRoot, service, diskPath, configPath)
			return err
		}
		if stored.VM == nil || stored.VM.Components == nil {
			_, err := syncVMGuestKernelSelectionToHost(ctx, serviceRoot, service, diskPath, configPath)
			return err
		}
		if !descriptorMode {
			return fmt.Errorf("automatic legacy kernel sync is disabled for adopted VM %q", service)
		}
		return syncVMComponentGuestKernelToHost(ctx, dataRoot, servicesRoot, serviceRoot, service, diskPath, configPath, *stored.VM.Components)
	})
}

func vmKernelSyncStoredService(dataRoot, servicesRoot, service string) (*db.Service, error) {
	store := db.NewStore(filepath.Join(dataRoot, "db.json"), servicesRoot)
	dv, err := store.Get()
	if err != nil {
		return nil, fmt.Errorf("read host VM state before automatic kernel sync: %w", err)
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return nil, nil
	}
	stored := sv.AsStruct()
	if stored.ServiceType != db.ServiceTypeVM || stored.VM == nil {
		return nil, fmt.Errorf("service %q is not a VM service", service)
	}
	return stored, nil
}

func vmKernelSyncDataRoot(jailerBase string) (string, error) {
	jailerBase = filepath.Clean(strings.TrimSpace(jailerBase))
	if !filepath.IsAbs(jailerBase) || filepath.Base(jailerBase) != "vm-jailer" {
		return "", fmt.Errorf("automatic VM kernel sync requires the canonical jailer base under the Catch data root")
	}
	return filepath.Dir(jailerBase), nil
}

func vmKernelSyncServicesRoot(dataRoot, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return filepath.Join(dataRoot, "services"), nil
	}
	if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured {
		return "", fmt.Errorf("automatic VM kernel sync requires a clean absolute services root")
	}
	return configured, nil
}

func resolveVMKernelAutoSyncConfig(cfg VMConsoleProxyConfig) (service, serviceRoot, diskPath, configPath string, err error) {
	configPath = strings.TrimSpace(cfg.ConfigFile)
	serviceRoot = strings.TrimSpace(cfg.ServiceRoot)
	if serviceRoot == "" {
		serviceRoot = inferVMServiceRootFromConfigPath(configPath)
	}
	service = strings.TrimSpace(cfg.Service)
	if service == "" && serviceRoot != "" {
		service = filepath.Base(serviceRoot)
	}
	if service != "" {
		if err := validateVMKernelBootHostname(service); err != nil {
			return "", "", "", "", err
		}
	}
	diskPath = strings.TrimSpace(cfg.DiskPath)
	if diskPath == "" && configPath != "" {
		diskPath, err = firecrackerRootDrivePath(configPath)
		if err != nil {
			return "", "", "", "", err
		}
	}
	return service, serviceRoot, diskPath, configPath, nil
}

func inferVMServiceRootFromConfigPath(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if filepath.Base(configPath) != "firecracker.json" {
		return ""
	}
	runDir := filepath.Dir(configPath)
	if filepath.Base(runDir) != "run" {
		return ""
	}
	return filepath.Dir(runDir)
}

func firecrackerRootDrivePath(configPath string) (string, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read Firecracker config for VM kernel auto-sync: %w", err)
	}
	var cfg firecrackerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("decode Firecracker config for VM kernel auto-sync: %w", err)
	}
	for _, drive := range cfg.Drives {
		if drive.IsRootDevice || drive.DriveID == "rootfs" {
			diskPath := strings.TrimSpace(drive.PathOnHost)
			if diskPath == "" {
				return "", fmt.Errorf("firecracker root drive path is empty")
			}
			return diskPath, nil
		}
	}
	return "", fmt.Errorf("firecracker config has no root drive")
}

func syncVMGuestKernelSelectionToHost(ctx context.Context, serviceRoot, service, diskPath, configPath string) (vmKernelSyncResult, error) {
	result, err := syncVMGuestKernelFromRootFS(ctx, serviceRoot, service, diskPath)
	if err != nil {
		return vmKernelSyncResult{}, err
	}
	if err := updateVMKernelFirecrackerConfig(configPath, result.HostKernelPath); err != nil {
		return vmKernelSyncResult{}, err
	}
	return result, nil
}

func syncVMGuestKernelFromRootFS(ctx context.Context, root, service, diskPath string) (result vmKernelSyncResult, retErr error) {
	return withMountedVMGuestRootFS(ctx, diskPath, func(mountRoot string) (vmKernelSyncResult, error) {
		return syncGuestSelectedKernelFromMountedRoot(ctx, root, service, mountRoot)
	})
}

func withMountedVMGuestRootFS(ctx context.Context, diskPath string, sync func(string) (vmKernelSyncResult, error)) (result vmKernelSyncResult, retErr error) {
	mountRoot, err := os.MkdirTemp("", "yeet-vm-kernel-rootfs-*")
	if err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("create VM rootfs mount dir: %w", err)
	}
	defer func() {
		retErr = joinVMMetadataDeferredError(retErr, os.RemoveAll(mountRoot), "remove VM rootfs mount dir")
	}()

	runner := vmKernelSyncRunner
	if runner == nil {
		runner = runVMCommand
	}
	replayRoot, err := os.MkdirTemp("", "yeet-vm-kernel-journal-*")
	if err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("create VM rootfs journal replay dir: %w", err)
	}
	defer func() {
		retErr = joinVMMetadataDeferredError(retErr, os.RemoveAll(replayRoot), "remove VM rootfs journal replay dir")
	}()
	if err := runner(ctx, vmRootFSJournalReplayCommand(diskPath, replayRoot)); err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("replay VM rootfs journal: %w", err)
	}
	if err := runner(ctx, vmRootFSReadOnlyMountCommand(diskPath, mountRoot)); err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("mount VM rootfs: %w", err)
	}
	defer func() {
		retErr = joinVMMetadataDeferredError(retErr, runner(ctx, []string{"umount", mountRoot}), "unmount VM rootfs")
	}()

	return sync(mountRoot)
}

func fetchTrustedVMKernelSyncCatalog(ctx context.Context) (vmKernelCatalog, error) {
	raw, err := fetchVMComponentCatalogRaw(ctx, nil, defaultVMKernelCatalogURL, "kernel", true)
	if err != nil {
		return vmKernelCatalog{}, err
	}
	return decodeVMKernelCatalog(raw, true)
}

func fetchTrustedVMKernelSyncManifest(ctx context.Context, dataRoot string, ref vmKernelCatalogRef) (vmKernelManifest, error) {
	cache := vmKernelArtifactCache{Root: filepath.Join(dataRoot, "vm-kernels")}
	manifest, _, err := cache.fetchValidatedManifest(ctx, ref, true)
	return manifest, err
}

func syncVMComponentGuestKernelToHost(ctx context.Context, dataRoot, servicesRoot, serviceRoot, service, diskPath, configPath string, components db.VMComponentsConfig) error {
	result, err := withMountedVMGuestRootFS(ctx, diskPath, func(mountRoot string) (vmKernelSyncResult, error) {
		return syncVMComponentGuestKernelFromMountedRoot(ctx, dataRoot, serviceRoot, mountRoot, components)
	})
	if err != nil {
		return err
	}
	if result.Component == nil {
		return fmt.Errorf("component-aware VM kernel sync did not resolve an authoritative kernel")
	}
	return commitVMComponentKernelSync(dataRoot, servicesRoot, service, configPath, components.Kernel, *result.Component)
}

func syncVMComponentGuestKernelFromMountedRoot(ctx context.Context, dataRoot, serviceRoot, mountRoot string, components db.VMComponentsConfig) (vmKernelSyncResult, error) {
	selection, err := readGuestKernelSelection(mountRoot)
	if err != nil {
		return vmKernelSyncResult{}, err
	}
	if selection.SchemaVersion == 1 {
		return syncVMLegacyComponentKernelFromMountedRoot(dataRoot, serviceRoot, mountRoot, selection, components)
	}
	ref, manifest, err := resolveTrustedVMKernelSelection(ctx, dataRoot, selection)
	if err != nil {
		return vmKernelSyncResult{}, err
	}

	srcKernel, err := resolveGuestRootPath(mountRoot, selection.Kernel)
	if err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("resolve guest kernel path: %w", err)
	}
	srcConfig, err := resolveGuestRootPath(mountRoot, selection.KernelConfig)
	if err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("resolve guest kernel config path: %w", err)
	}
	dstDir := filepath.Join(serviceDataDirForRoot(serviceRoot), "kernels", ref.KernelID, ref.ManifestSHA256)
	dstKernel := filepath.Join(dstDir, vmKernelFilename)
	dstConfig := filepath.Join(dstDir, vmKernelConfigFilename)
	if err := publishVMProvisionVerifiedFile(srcKernel, dstKernel, manifest.VMLinux.SHA256, "guest-selected kernel"); err != nil {
		return vmKernelSyncResult{}, err
	}
	if err := publishVMProvisionVerifiedFile(srcConfig, dstConfig, manifest.Config.SHA256, "guest-selected kernel config"); err != nil {
		return vmKernelSyncResult{}, err
	}
	component := db.VMKernelArtifactConfig{
		ID: ref.KernelID, ManifestSHA256: ref.ManifestSHA256,
		SHA256: manifest.VMLinux.SHA256, Path: dstKernel, Source: "official",
	}
	return vmKernelSyncResult{
		Version: selection.Version, HostKernelPath: dstKernel, HostConfigPath: dstConfig, Component: &component,
	}, nil
}

func syncVMLegacyComponentKernelFromMountedRoot(dataRoot, serviceRoot, mountRoot string, selection vmGuestKernelSelection, components db.VMComponentsConfig) (vmKernelSyncResult, error) {
	if !isVMLegacyKernelSource(components.Kernel.Source) {
		return vmKernelSyncResult{}, fmt.Errorf("VM guest kernel selector schema_version 1 is not valid for non-legacy component source %q", components.Kernel.Source)
	}
	record, provenanceSHA, err := readPinnedVMLegacyKernelComposition(dataRoot, components.GuestBase.RootFSProvenance)
	if err != nil {
		return vmKernelSyncResult{}, err
	}
	_, kernelID, _, err := vmLegacyCompositionIDs(record, provenanceSHA)
	if err != nil {
		return vmKernelSyncResult{}, err
	}
	if kernelID != components.Kernel.ID || record.Kernel.KernelSHA256 != components.Kernel.SHA256 {
		return vmKernelSyncResult{}, fmt.Errorf("pinned legacy VM composition does not match the configured kernel component")
	}
	if selection.SHA256["vmlinux"] != record.Kernel.KernelSHA256 ||
		(strings.TrimSpace(selection.KernelConfig) != "" && selection.SHA256["kernel.config"] != record.Kernel.ConfigSHA256) {
		return vmKernelSyncResult{}, fmt.Errorf("VM guest kernel selector does not match the pinned legacy composition")
	}

	srcKernel, err := resolveGuestRootPath(mountRoot, selection.Kernel)
	if err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("resolve guest kernel path: %w", err)
	}
	if !vmRuntimeSHA256Pattern.MatchString(components.Kernel.ManifestSHA256) {
		return vmKernelSyncResult{}, fmt.Errorf("configured legacy VM kernel has invalid manifest SHA-256")
	}
	dstDir := filepath.Join(serviceDataDirForRoot(serviceRoot), "kernels", components.Kernel.ID, components.Kernel.ManifestSHA256)
	dstKernel := filepath.Join(dstDir, vmKernelFilename)
	if err := publishVMProvisionVerifiedFile(srcKernel, dstKernel, record.Kernel.KernelSHA256, "guest-selected legacy kernel"); err != nil {
		return vmKernelSyncResult{}, err
	}
	dstConfig := ""
	if strings.TrimSpace(selection.KernelConfig) != "" {
		srcConfig, err := resolveGuestRootPath(mountRoot, selection.KernelConfig)
		if err != nil {
			return vmKernelSyncResult{}, fmt.Errorf("resolve guest kernel config path: %w", err)
		}
		dstConfig = filepath.Join(dstDir, vmKernelConfigFilename)
		if err := publishVMProvisionVerifiedFile(srcConfig, dstConfig, record.Kernel.ConfigSHA256, "guest-selected legacy kernel config"); err != nil {
			return vmKernelSyncResult{}, err
		}
	}
	next := components.Kernel
	next.Path = dstKernel
	return vmKernelSyncResult{
		Version: selection.Version, HostKernelPath: dstKernel, HostConfigPath: dstConfig, Component: &next,
	}, nil
}

func isVMLegacyKernelSource(source string) bool {
	switch vmRuntimeAdoptionClassification(source) {
	case vmRuntimeAdoptionOfficialLegacy, vmRuntimeAdoptionLocalLegacy, vmRuntimeAdoptionCustomLegacy:
		return true
	default:
		return false
	}
}

func readPinnedVMLegacyKernelComposition(dataRoot, digest string) (vmLegacyCompositionRecord, string, error) {
	digest = strings.TrimSpace(digest)
	if !vmRuntimeSHA256Pattern.MatchString(digest) {
		return vmLegacyCompositionRecord{}, "", fmt.Errorf("adopted VM has invalid legacy composition provenance")
	}
	dirPath := filepath.Join(dataRoot, vmLegacyProvenanceDirName, vmLegacyProvenanceDigestDirName)
	info, err := os.Lstat(dirPath)
	if err != nil {
		return vmLegacyCompositionRecord{}, "", fmt.Errorf("inspect pinned legacy VM composition directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return vmLegacyCompositionRecord{}, "", fmt.Errorf("pinned legacy VM composition directory is not a directory")
	}
	dir, err := os.Open(dirPath)
	if err != nil {
		return vmLegacyCompositionRecord{}, "", fmt.Errorf("open pinned legacy VM composition directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	raw, err := readVMLegacyCompositionEntry(dir, digest+".json", uint32(os.Geteuid()))
	if err != nil {
		return vmLegacyCompositionRecord{}, "", fmt.Errorf("read pinned legacy VM composition: %w", err)
	}
	var record vmLegacyCompositionRecord
	if err := decodeStrictVMRuntimeJSON(raw, &record, "legacy VM composition"); err != nil {
		return vmLegacyCompositionRecord{}, "", err
	}
	_, got, err := canonicalVMLegacyComposition(record)
	if err != nil {
		return vmLegacyCompositionRecord{}, "", err
	}
	if got != digest {
		return vmLegacyCompositionRecord{}, "", fmt.Errorf("pinned legacy VM composition digest mismatch")
	}
	return record, digest, nil
}

func resolveTrustedVMKernelSelection(ctx context.Context, dataRoot string, selection vmGuestKernelSelection) (vmKernelCatalogRef, vmKernelManifest, error) {
	catalog, err := fetchVMKernelSyncCatalog(ctx)
	if err != nil {
		return vmKernelCatalogRef{}, vmKernelManifest{}, err
	}
	if err := catalog.validate(true); err != nil {
		return vmKernelCatalogRef{}, vmKernelManifest{}, fmt.Errorf("validate trusted VM kernel catalog: %w", err)
	}
	ref, ok := catalog.KernelByID(selection.ReleaseID)
	if !ok || ref.ManifestSHA256 != selection.ManifestSHA256 {
		return vmKernelCatalogRef{}, vmKernelManifest{}, fmt.Errorf("VM guest kernel selection does not resolve to one immutable trusted catalog entry")
	}
	manifest, err := fetchVMKernelSyncManifest(ctx, dataRoot, ref)
	if err != nil {
		return vmKernelCatalogRef{}, vmKernelManifest{}, err
	}
	if err := manifest.validate(true); err != nil {
		return vmKernelCatalogRef{}, vmKernelManifest{}, fmt.Errorf("validate trusted VM kernel manifest: %w", err)
	}
	if err := validateVMKernelManifestRef(manifest, ref); err != nil {
		return vmKernelCatalogRef{}, vmKernelManifest{}, err
	}
	wantVersion := "linux-" + manifest.UpstreamVersion + "-yeet"
	if selection.Version != wantVersion ||
		selection.SHA256["vmlinux"] != manifest.VMLinux.SHA256 ||
		selection.SHA256["kernel.config"] != manifest.Config.SHA256 {
		return vmKernelCatalogRef{}, vmKernelManifest{}, fmt.Errorf("VM guest kernel selector does not match the trusted manifest for %s", ref.KernelID)
	}
	return ref, manifest, nil
}

func commitVMComponentKernelSync(dataRoot, servicesRoot, service, configPath string, current, next db.VMKernelArtifactConfig) error {
	oldConfig, nextConfig, err := renderVMKernelFirecrackerConfigUpdate(configPath, next.Path)
	if err != nil {
		return err
	}
	store := db.NewStore(filepath.Join(dataRoot, "db.json"), servicesRoot)
	_, err = store.MutateDataWithPrePublicationCompensation(func(data *db.Data) (func() error, error) {
		stored, ok := data.Services[service]
		if !ok || stored == nil || stored.ServiceType != db.ServiceTypeVM || stored.VM == nil || stored.VM.Components == nil {
			return nil, fmt.Errorf("adopted VM %q no longer exists", service)
		}
		if stored.VM.Components.Kernel != current {
			return nil, fmt.Errorf("VM %q kernel selection changed concurrently", service)
		}
		if err := writeVMFileAtomic(configPath, nextConfig, 0o644); err != nil {
			return nil, fmt.Errorf("write Firecracker config: %w", err)
		}
		stored.VM.Components.Kernel = next
		stored.VM.Image.Kernel = next.Path
		return func() error { return writeVMFileAtomic(configPath, oldConfig, 0o644) }, nil
	})
	return err
}

func renderVMKernelFirecrackerConfigUpdate(configPath, kernelPath string) ([]byte, []byte, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read Firecracker config: %w", err)
	}
	var cfg firecrackerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, fmt.Errorf("decode Firecracker config: %w", err)
	}
	cfg.BootSource.KernelImagePath = kernelPath
	cfg.BootSource.InitrdPath = ""
	rendered, err := renderFirecrackerConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return raw, rendered, nil
}

func syncGuestSelectedKernelFromMountedRoot(_ context.Context, serviceRoot, service, mountRoot string) (vmKernelSyncResult, error) {
	selection, err := readGuestKernelSelection(mountRoot)
	if err != nil {
		return vmKernelSyncResult{}, err
	}

	srcKernel, err := resolveGuestRootPath(mountRoot, selection.Kernel)
	if err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("resolve guest kernel path: %w", err)
	}
	if err := verifyFileSHA256(srcKernel, selection.SHA256["vmlinux"]); err != nil {
		return vmKernelSyncResult{}, err
	}
	dstDir := filepath.Join(serviceRunDirForRoot(serviceRoot), "kernels", service, selection.Version)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("create host kernel cache: %w", err)
	}
	dstKernel := filepath.Join(dstDir, "vmlinux")
	if err := copyFileMode(srcKernel, dstKernel, 0o644); err != nil {
		return vmKernelSyncResult{}, err
	}

	dstConfig, err := syncGuestKernelConfig(mountRoot, dstDir, selection)
	if err != nil {
		return vmKernelSyncResult{}, err
	}
	return vmKernelSyncResult{Version: selection.Version, HostKernelPath: dstKernel, HostConfigPath: dstConfig}, nil
}

func readGuestKernelSelection(mountRoot string) (vmGuestKernelSelection, error) {
	selectorPath, err := resolveGuestRootPath(mountRoot, vmGuestKernelSelectionPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return vmGuestKernelSelection{}, fmt.Errorf("%w: %v", errVMGuestKernelSelectorMissing, err)
		}
		return vmGuestKernelSelection{}, fmt.Errorf("resolve guest kernel selector: %w", err)
	}
	raw, err := os.ReadFile(selectorPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return vmGuestKernelSelection{}, fmt.Errorf("%w: %v", errVMGuestKernelSelectorMissing, err)
		}
		return vmGuestKernelSelection{}, fmt.Errorf("read guest kernel selector: %w", err)
	}
	var selection vmGuestKernelSelection
	if err := decodeStrictVMRuntimeJSON(raw, &selection, "VM guest kernel selector"); err != nil {
		return vmGuestKernelSelection{}, fmt.Errorf("decode guest kernel selector: %w", err)
	}
	if err := selection.validate(); err != nil {
		return vmGuestKernelSelection{}, err
	}
	return selection, nil
}

func syncGuestKernelConfig(mountRoot, dstDir string, selection vmGuestKernelSelection) (string, error) {
	if strings.TrimSpace(selection.KernelConfig) == "" {
		return "", nil
	}
	srcConfig, err := resolveGuestRootPath(mountRoot, selection.KernelConfig)
	if err != nil {
		return "", fmt.Errorf("resolve guest kernel config path: %w", err)
	}
	if err := verifyFileSHA256(srcConfig, selection.SHA256["kernel.config"]); err != nil {
		return "", err
	}
	dstConfig := filepath.Join(dstDir, "kernel.config")
	if err := copyFileMode(srcConfig, dstConfig, 0o644); err != nil {
		return "", err
	}
	return dstConfig, nil
}

func resolveGuestRootPath(mountRoot, guestPath string) (string, error) {
	original := guestPath
	guestPath = path.Clean(strings.TrimSpace(guestPath))
	if !path.IsAbs(guestPath) {
		return "", fmt.Errorf("guest path %q is not absolute", original)
	}
	parts := splitGuestPath(guestPath)
	resolved := make([]string, 0, len(parts))
	symlinks := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		candidateParts := append(append([]string(nil), resolved...), part)
		candidateGuestPath := guestPathFromParts(candidateParts)
		candidateHostPath := guestHostPath(mountRoot, candidateParts)
		info, err := os.Lstat(candidateHostPath)
		if err != nil {
			return "", fmt.Errorf("%s: %w", candidateGuestPath, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, part)
			continue
		}

		symlinks++
		if symlinks > 40 {
			return "", fmt.Errorf("too many symlinks resolving guest path %q", original)
		}
		targetParts, err := guestSymlinkTargetParts(candidateHostPath, candidateGuestPath, resolved)
		if err != nil {
			return "", err
		}
		parts = append(targetParts, parts...)
		resolved = resolved[:0]
	}
	return guestHostPath(mountRoot, resolved), nil
}

func guestSymlinkTargetParts(hostPath, guestPath string, resolved []string) ([]string, error) {
	target, err := os.Readlink(hostPath)
	if err != nil {
		return nil, fmt.Errorf("readlink %s: %w", guestPath, err)
	}
	if path.IsAbs(target) {
		return splitGuestPath(target), nil
	}
	parentGuestPath := guestPathFromParts(resolved)
	return splitGuestPath(path.Join(parentGuestPath, target)), nil
}

func guestHostPath(mountRoot string, parts []string) string {
	if len(parts) == 0 {
		return mountRoot
	}
	return filepath.Join(mountRoot, filepath.FromSlash(path.Join(parts...)))
}

func guestPathFromParts(parts []string) string {
	if len(parts) == 0 {
		return "/"
	}
	return "/" + path.Join(parts...)
}

func splitGuestPath(p string) []string {
	p = strings.TrimPrefix(path.Clean(p), "/")
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}

func verifyFileSHA256(path, want string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s", path, got, want)
	}
	return nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, raw, mode); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

func vmRootFSReadOnlyMountCommand(diskPath, mountRoot string) []string {
	if strings.HasPrefix(diskPath, "/dev/") {
		return []string{"mount", "-o", "ro,noload", diskPath, mountRoot}
	}
	return []string{"mount", "-o", "loop,ro,noload", diskPath, mountRoot}
}

func vmRootFSJournalReplayCommand(diskPath, mountRoot string) []string {
	options := "rw"
	if !strings.HasPrefix(diskPath, "/dev/") {
		options = "loop,rw"
	}
	return []string{
		"sh", "-c", `set -eu
mounted=0
cleanup() {
	if [ "$mounted" = 1 ]; then
		umount "$3"
	fi
}
trap cleanup EXIT
mount -o "$1" "$2" "$3"
mounted=1
umount "$3"
mounted=0
`, "yeet-vm-rootfs-journal-replay", options, diskPath, mountRoot}
}

func updateVMKernelFirecrackerConfig(configPath, kernelPath string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read Firecracker config: %w", err)
	}
	var cfg firecrackerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("decode Firecracker config: %w", err)
	}
	cfg.BootSource.KernelImagePath = kernelPath
	cfg.BootSource.InitrdPath = ""
	rendered, err := renderFirecrackerConfig(cfg)
	if err != nil {
		return err
	}
	return writeVMFile(configPath, rendered, 0o644)
}

func (s *Server) updateVMKernelDBPath(name, kernelPath string) error {
	_, _, err := s.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		if service.VM == nil {
			return fmt.Errorf("service %q is not a VM service", name)
		}
		service.VM.Image.Kernel = kernelPath
		return nil
	})
	return err
}
