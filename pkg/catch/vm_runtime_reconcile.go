// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

type vmRuntimeReconcileDeps struct {
	descriptor vmRuntimeDescriptorFileDeps
	units      vmUnitTransactionDeps
	runner     string
}

func (s *Server) runtimeReconcileDependencies() vmRuntimeReconcileDeps {
	deps := vmRuntimeReconcileDeps{
		descriptor: defaultVMRuntimeDescriptorFileDeps(),
		units:      completeVMUnitTransactionDeps(vmUnitTransactionDeps{}),
		runner:     s.catchRunnerPath(),
	}
	if s.vmRuntimeReconcileDeps == nil {
		return deps
	}
	override := *s.vmRuntimeReconcileDeps
	deps.descriptor = completeVMRuntimeDescriptorFileDeps(override.descriptor)
	deps.units = completeVMUnitTransactionDeps(override.units)
	if override.runner != "" {
		deps.runner = override.runner
	}
	return deps
}

// reconcileVMRuntimeState repairs descriptor-mode launch inputs from the
// authoritative database. It never downloads artifacts or changes service
// activity. Trial-result mutation is deliberately owned by the trial schema
// and launcher transaction introduced with trial execution.
func (s *Server) reconcileVMRuntimeState(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	if len(adoptedVMRuntimeServices(dv.AsStruct())) == 0 {
		return nil
	}
	return WithVMRuntimeTransactionLock(ctx, &s.cfg, func() error {
		return s.reconcileVMRuntimeStateLocked(ctx)
	})
}

func (s *Server) reconcileVMRuntimeStateLocked(ctx context.Context) error {
	latest, err := s.getDB()
	if err != nil {
		return err
	}
	services := adoptedVMRuntimeServices(latest.AsStruct())
	if err := s.reconcileVMComponentKernelCompatibilityLocked(services); err != nil {
		return err
	}
	latest, err = s.getDB()
	if err != nil {
		return err
	}
	services = adoptedVMRuntimeServices(latest.AsStruct())
	deps := s.runtimeReconcileDependencies()
	if err := reconcileVMRuntimeDescriptors(services, s.cfg, deps.descriptor); err != nil {
		return err
	}
	specs, err := collectVMRuntimeUnitRepairs(services, s.cfg, deps)
	if err != nil || len(specs) == 0 {
		return err
	}
	tx, err := prepareVMUnitTransaction(ctx, specs, deps.units)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Close() }()
	if err := tx.Commit(); err != nil {
		return err
	}
	return tx.Close()
}

func (s *Server) reconcileVMComponentKernelCompatibility() error {
	return WithVMRuntimeTransactionLock(context.Background(), &s.cfg, func() error {
		latest, err := s.getDB()
		if err != nil {
			return err
		}
		return s.reconcileVMComponentKernelCompatibilityLocked(adoptedVMRuntimeServices(latest.AsStruct()))
	})
}

func (s *Server) reconcileVMComponentKernelCompatibilityLocked(services []*db.Service) error {
	repairs := make(map[string]db.VMKernelArtifactConfig)
	for _, service := range services {
		if strings.TrimSpace(service.VM.Image.Kernel) != "" || !vmKernelComponentConfigured(service.VM.Components.Kernel) {
			continue
		}
		if err := validateVMComponentKernelForCompatibility(s.cfg, service, service.VM.Components.Kernel); err != nil {
			return fmt.Errorf("verify VM component kernel for %s: %w", service.Name, err)
		}
		repairs[service.Name] = service.VM.Components.Kernel
	}
	if len(repairs) == 0 {
		return nil
	}
	_, err := s.cfg.DB.MutateData(func(data *db.Data) error {
		for name, kernel := range repairs {
			service, ok := data.Services[name]
			if !ok || service == nil || service.VM == nil || service.VM.Components == nil {
				return fmt.Errorf("adopted VM %q disappeared during kernel compatibility repair", name)
			}
			if strings.TrimSpace(service.VM.Image.Kernel) != "" {
				continue
			}
			if service.VM.Components.Kernel != kernel {
				return fmt.Errorf("VM %q kernel component changed during compatibility repair", name)
			}
			service.VM.Image.Kernel = kernel.Path
		}
		return nil
	})
	return err
}

func vmKernelComponentConfigured(kernel db.VMKernelArtifactConfig) bool {
	return strings.TrimSpace(kernel.ID) != "" || strings.TrimSpace(kernel.ManifestSHA256) != "" ||
		strings.TrimSpace(kernel.SHA256) != "" || strings.TrimSpace(kernel.Path) != "" || strings.TrimSpace(kernel.Source) != ""
}

func validateVMComponentKernelForCompatibility(cfg Config, service *db.Service, kernel db.VMKernelArtifactConfig) error {
	if strings.TrimSpace(kernel.ID) == "" || !vmRuntimeSHA256Pattern.MatchString(kernel.ManifestSHA256) ||
		!vmRuntimeSHA256Pattern.MatchString(kernel.SHA256) || !filepath.IsAbs(kernel.Path) || filepath.Clean(kernel.Path) != kernel.Path {
		return fmt.Errorf("kernel component lock is incomplete or invalid")
	}
	if kernel.Source == "official" {
		root := serviceRootFromConfig(cfg, *service)
		want := filepath.Join(serviceDataDirForRoot(root), "kernels", kernel.ID, kernel.ManifestSHA256, vmKernelFilename)
		if kernel.Path != want {
			return fmt.Errorf("official kernel path %q does not match immutable service path %q", kernel.Path, want)
		}
	} else if !isVMLegacyKernelSource(kernel.Source) {
		return fmt.Errorf("unsupported kernel component source %q", kernel.Source)
	}
	info, err := os.Lstat(kernel.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("kernel component path is not a regular file")
	}
	if err := verifyFileSHA256(kernel.Path, kernel.SHA256); err != nil {
		return err
	}
	return nil
}

func reconcileVMRuntimeDescriptors(services []*db.Service, cfg Config, deps vmRuntimeDescriptorFileDeps) error {
	for _, service := range services {
		if err := reconcileVMRuntimeDescriptor(service, cfg, deps); err != nil {
			return fmt.Errorf("repair VM runtime descriptor for %s: %w", service.Name, err)
		}
	}
	return nil
}

func collectVMRuntimeUnitRepairs(services []*db.Service, cfg Config, deps vmRuntimeReconcileDeps) ([]vmUnitSpec, error) {
	specs := make([]vmUnitSpec, 0, len(services))
	for _, service := range services {
		spec, err := renderVMRuntimeUnitSpec(cfg, service, deps.runner)
		if err != nil {
			return nil, fmt.Errorf("render VM runtime unit for %s: %w", service.Name, err)
		}
		needsRepair, err := vmRuntimeUnitNeedsRepair(spec, deps.units.unitUID, deps.units.unitGID)
		if err != nil {
			return nil, fmt.Errorf("inspect VM runtime unit for %s: %w", service.Name, err)
		}
		if needsRepair {
			specs = append(specs, spec)
		}
	}
	return specs, nil
}

func adoptedVMRuntimeServices(data *db.Data) []*db.Service {
	if data == nil {
		return nil
	}
	services := make([]*db.Service, 0, len(data.Services))
	for _, service := range data.Services {
		if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil || service.VM.Components == nil {
			continue
		}
		services = append(services, service)
	}
	slices.SortFunc(services, func(left, right *db.Service) int { return strings.Compare(left.Name, right.Name) })
	return services
}

func vmRuntimeDescriptorFromService(service *db.Service) (vmRuntimeDescriptor, error) {
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return vmRuntimeDescriptor{}, fmt.Errorf("adopted VM runtime state is required")
	}
	runtimeState := service.VM.Components.Runtime
	descriptor := vmRuntimeDescriptor{
		SchemaVersion: vmRuntimeDescriptorSchemaVersion,
		Service:       service.Name,
		Configured:    runtimeState.Configured,
		Staged:        runtimeState.Staged,
		Previous:      runtimeState.Previous,
		Trial:         runtimeState.Staged != nil,
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return vmRuntimeDescriptor{}, err
	}
	if _, err := decodeVMRuntimeDescriptor(raw, service.Name); err != nil {
		return vmRuntimeDescriptor{}, err
	}
	return descriptor, nil
}

func reconcileVMRuntimeDescriptor(service *db.Service, cfg Config, deps vmRuntimeDescriptorFileDeps) error {
	descriptor, err := vmRuntimeDescriptorFromService(service)
	if err != nil {
		return err
	}
	root := serviceRootFromConfig(cfg, *service)
	dir := serviceDataDirForRoot(root)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect VM runtime descriptor directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("VM runtime descriptor directory is not a directory")
	}
	path := filepath.Join(dir, vmRuntimeDescriptorFileName)
	current, err := readVMRuntimeDescriptorWithOwner(path, service.Name, deps.uid, deps.gid)
	if err == nil && equalVMRuntimeDescriptors(current, descriptor) {
		return nil
	}
	return writeVMRuntimeDescriptorWithDeps(path, descriptor, deps)
}

func equalVMRuntimeDescriptors(left, right vmRuntimeDescriptor) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func renderVMRuntimeUnitSpec(cfg Config, service *db.Service, runner string) (vmUnitSpec, error) {
	root := serviceRootFromConfig(cfg, *service)
	runDir := serviceRunDirForRoot(root)
	diskPath := service.VM.Disk.Path
	if diskPath == "" {
		diskPath = service.VM.Image.RootFS
	}
	apiSocket := service.VM.Sockets.APISocketPath
	if apiSocket == "" {
		apiSocket = filepath.Join(runDir, "firecracker.sock")
	}
	consoleSocket := service.VM.Console.SocketPath
	if consoleSocket == "" {
		consoleSocket = filepath.Join(runDir, "serial.sock")
	}
	vsockSocket := service.VM.Sockets.VsockSocketPath
	if vsockSocket == "" {
		vsockSocket = filepath.Join(runDir, "vsock.sock")
	}
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service: service.Name, Runner: runner,
		DataDir: cfg.RootDir, ServicesRoot: cfg.ServicesRoot, ServiceRoot: root,
		DiskPath:             diskPath,
		RuntimeDescriptor:    filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName),
		RuntimeRunningMarker: filepath.Join(runDir, vmRuntimeRunningMarkerFileName),
		RuntimeTrialResult:   filepath.Join(runDir, vmRuntimeTrialResultFileName),
		JailerBase:           vmJailerBaseForDataRoot(cfg.RootDir),
		ConfigPath:           filepath.Join(runDir, "firecracker.json"),
		APISocket:            apiSocket,
		ConsoleSocket:        consoleSocket,
		VsockSocket:          vsockSocket,
		WorkingDirectory:     root,
	})
	if err != nil {
		return vmUnitSpec{}, err
	}
	return vmUnitSpec{
		Service: service.Name,
		Path:    filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service.Name)),
		Content: []byte(unit),
	}, nil
}

func vmRuntimeUnitNeedsRepair(spec vmUnitSpec, uid, gid uint32) (bool, error) {
	dir, err := openValidatedVMUnitDir(filepath.Dir(spec.Path), uid)
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	raw, exists, _, stat, err := readVMUnitStateAt(dir, filepath.Base(spec.Path), uid)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	exactMode := uint32(stat.Mode)&unix.S_IFMT == unix.S_IFREG && uint32(stat.Mode)&0o7777 == 0o644
	exactOwner := stat.Uid == uid && stat.Gid == gid
	return !exactMode || !exactOwner || !bytes.Equal(raw, spec.Content), nil
}
