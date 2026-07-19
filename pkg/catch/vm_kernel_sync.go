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
)

type vmKernelSyncResult struct {
	Version        string
	HostKernelPath string
	HostConfigPath string
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
	if strings.TrimSpace(cfg.RuntimeDescriptor) != "" {
		return fmt.Errorf("automatic kernel sync for descriptor-managed VMs requires component-aware kernel reconciliation")
	}
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
	err = autoSyncVMGuestKernelLocked(ctx, dataRoot, service, serviceRoot, diskPath, configPath)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func autoSyncVMGuestKernelLocked(ctx context.Context, dataRoot, service, serviceRoot, diskPath, configPath string) error {
	return WithVMRuntimeRootLock(ctx, dataRoot, func() error {
		if err := refuseAdoptedVMLegacyKernelAutoSync(dataRoot, service); err != nil {
			return err
		}
		_, err := syncVMGuestKernelSelectionToHost(ctx, serviceRoot, service, diskPath, configPath)
		return err
	})
}

func refuseAdoptedVMLegacyKernelAutoSync(dataRoot, service string) error {
	store := db.NewStore(filepath.Join(dataRoot, "db.json"), filepath.Join(dataRoot, "services"))
	dv, err := store.Get()
	if err != nil {
		return fmt.Errorf("read host VM state before automatic kernel sync: %w", err)
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return nil
	}
	stored := sv.AsStruct()
	if stored.ServiceType == db.ServiceTypeVM && stored.VM != nil && stored.VM.Components != nil {
		return fmt.Errorf("automatic legacy kernel sync is disabled for adopted VM %q", service)
	}
	return nil
}

func vmKernelSyncDataRoot(jailerBase string) (string, error) {
	jailerBase = filepath.Clean(strings.TrimSpace(jailerBase))
	if !filepath.IsAbs(jailerBase) || filepath.Base(jailerBase) != "vm-jailer" {
		return "", fmt.Errorf("automatic VM kernel sync requires the canonical jailer base under the Catch data root")
	}
	return filepath.Dir(jailerBase), nil
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

	return syncGuestSelectedKernelFromMountedRoot(ctx, root, service, mountRoot)
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
		return vmGuestKernelSelection{}, fmt.Errorf("resolve guest kernel selector: %w", err)
	}
	raw, err := os.ReadFile(selectorPath)
	if err != nil {
		return vmGuestKernelSelection{}, fmt.Errorf("read guest kernel selector: %w", err)
	}
	var selection vmGuestKernelSelection
	if err := json.Unmarshal(raw, &selection); err != nil {
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
