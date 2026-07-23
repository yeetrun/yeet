// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type vmJailerUpgradeArtifactSpec struct {
	path     string
	name     string
	checksum string
	mode     os.FileMode
}

type vmJailerUpgradeOpenArtifact struct {
	file   *os.File
	parent *os.File
	entry  string
	path   string
	name   string
	mode   os.FileMode
}

func shouldNormalizeManagedVMJailerUpgradeArtifacts(
	dataRoot, payload, firecracker string,
	manifest vmImageManifest,
	localPayload func(string) bool,
) bool {
	payload = strings.TrimSpace(payload)
	if !strings.HasPrefix(payload, vmImagePayloadPrefix) ||
		strings.TrimSpace(manifest.Kernel) == "" ||
		localPayload == nil ||
		localPayload(payload) {
		return false
	}
	cacheRoot := filepath.Join(filepath.Clean(strings.TrimSpace(dataRoot)), "vm-images")
	imageDir := filepath.Dir(filepath.Clean(strings.TrimSpace(firecracker)))
	return filepath.IsAbs(cacheRoot) && vmJailerTransitionStrictlyWithin(cacheRoot, imageDir)
}

func normalizeManagedVMJailerUpgradeArtifacts(vm vmJailerUpgradeVM) (retErr error) {
	if !vm.NormalizeManagedArtifacts {
		return nil
	}
	specs, err := managedVMJailerUpgradeArtifactSpecs(vm)
	if err != nil {
		return err
	}
	opened := make([]*vmJailerUpgradeOpenArtifact, 0, len(specs))
	defer func() {
		for _, artifact := range opened {
			retErr = errors.Join(retErr, artifact.close())
		}
	}()
	for _, spec := range specs {
		artifact, err := openVerifiedVMJailerUpgradeArtifact(spec)
		if err != nil {
			return err
		}
		opened = append(opened, artifact)
	}
	for _, artifact := range opened {
		if err := artifact.normalize(); err != nil {
			return err
		}
	}
	return nil
}

func validateVMJailerUpgradeNextStart(
	ctx context.Context,
	cfg *Config,
	vm vmJailerUpgradeVM,
	identity vmRuntimeIdentity,
) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("catch configuration and database are required for VM jailer next-start validation")
	}
	dv, err := cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("read VM jailer next-start database: %w", err)
	}
	if !dv.Valid() {
		return fmt.Errorf("read VM jailer next-start database: database is invalid")
	}
	transition, err := newVMJailerTransitionPlan(&dv, vmJailerTransitionInput{
		DataRoot: cfg.RootDir, Service: vm.Service, ServiceRoot: vm.ServiceRoot,
	}, identity)
	if err != nil {
		return err
	}
	return validateVMJailerTransition(ctx, transition)
}

func managedVMJailerUpgradeArtifactSpecs(vm vmJailerUpgradeVM) ([]vmJailerUpgradeArtifactSpec, error) {
	manifest := vm.Manifest
	if err := manifest.validate(); err != nil {
		return nil, fmt.Errorf("validate managed VM image manifest: %w", err)
	}
	dir := filepath.Dir(filepath.Clean(strings.TrimSpace(vm.Firecracker)))
	specs := make([]vmJailerUpgradeArtifactSpec, 0, 4)
	kernel, err := managedVMJailerUpgradeManifestArtifactSpec(dir, manifest.Kernel, 0o644, manifest)
	if err != nil {
		return nil, err
	}
	specs = append(specs, kernel)
	if strings.TrimSpace(manifest.Initrd) != "" {
		initrd, err := managedVMJailerUpgradeManifestArtifactSpec(dir, manifest.Initrd, 0o644, manifest)
		if err != nil {
			return nil, err
		}
		specs = append(specs, initrd)
	}
	firecracker, err := managedVMJailerUpgradeManifestArtifactSpec(dir, manifest.Firecracker, 0o755, manifest)
	if err != nil {
		return nil, err
	}
	specs = append(specs, firecracker)
	if filepath.Clean(vm.Firecracker) != filepath.Join(dir, manifest.Firecracker) {
		return nil, fmt.Errorf("managed VM image manifest Firecracker does not match configured runtime")
	}
	jailer, err := managedVMJailerUpgradeJailerSpec(dir, vm.Jailer, manifest)
	if err != nil {
		return nil, err
	}
	return append(specs, jailer), nil
}

func managedVMJailerUpgradeManifestArtifactSpec(
	dir, name string,
	mode os.FileMode,
	manifest vmImageManifest,
) (vmJailerUpgradeArtifactSpec, error) {
	if err := validateVMImageArtifactName(name); err != nil {
		return vmJailerUpgradeArtifactSpec{}, err
	}
	checksum := strings.TrimSpace(manifest.Checksums[name])
	if checksum == "" {
		return vmJailerUpgradeArtifactSpec{}, fmt.Errorf("managed VM image artifact %q has no checksum", name)
	}
	return vmJailerUpgradeArtifactSpec{
		path: filepath.Join(dir, name), name: name, checksum: checksum, mode: mode,
	}, nil
}

func managedVMJailerUpgradeJailerSpec(
	dir, configured string,
	manifest vmImageManifest,
) (vmJailerUpgradeArtifactSpec, error) {
	jailerName := strings.TrimSpace(manifest.Jailer)
	jailerChecksum := ""
	if jailerName == "" {
		jailerName = "jailer"
	} else {
		if err := validateVMImageArtifactName(jailerName); err != nil {
			return vmJailerUpgradeArtifactSpec{}, err
		}
		jailerChecksum = strings.TrimSpace(manifest.Checksums[jailerName])
		if jailerChecksum == "" {
			return vmJailerUpgradeArtifactSpec{}, fmt.Errorf("managed VM image artifact %q has no checksum", jailerName)
		}
	}
	if filepath.Clean(strings.TrimSpace(configured)) != filepath.Join(dir, jailerName) {
		return vmJailerUpgradeArtifactSpec{}, fmt.Errorf("managed VM image jailer does not match configured runtime")
	}
	return vmJailerUpgradeArtifactSpec{
		path: filepath.Join(dir, jailerName), name: jailerName, checksum: jailerChecksum, mode: 0o755,
	}, nil
}

func openVerifiedVMJailerUpgradeArtifact(spec vmJailerUpgradeArtifactSpec) (*vmJailerUpgradeOpenArtifact, error) {
	file, parent, entry, err := vmImageRuntimePermissionOpen(spec.path)
	if err != nil {
		return nil, fmt.Errorf("open managed VM image artifact %s: %w", spec.name, err)
	}
	artifact := &vmJailerUpgradeOpenArtifact{
		file: file, parent: parent, entry: entry,
		path: spec.path, name: spec.name, mode: spec.mode,
	}
	info, err := file.Stat()
	if err != nil {
		return nil, closeVMJailerUpgradeArtifactOnError(artifact, fmt.Errorf("inspect managed VM image artifact %s: %w", spec.name, err))
	}
	if !info.Mode().IsRegular() {
		return nil, closeVMJailerUpgradeArtifactOnError(artifact, fmt.Errorf("managed VM image artifact %s must be a regular file", spec.name))
	}
	if spec.checksum != "" {
		if err := verifyOpenVMImageArtifactChecksum(file, spec.name, spec.checksum); err != nil {
			return nil, closeVMJailerUpgradeArtifactOnError(artifact, err)
		}
	}
	return artifact, nil
}

func closeVMJailerUpgradeArtifactOnError(artifact *vmJailerUpgradeOpenArtifact, cause error) error {
	return errors.Join(cause, artifact.close())
}

func (a *vmJailerUpgradeOpenArtifact) normalize() error {
	if err := a.file.Chmod(a.mode); err != nil {
		return fmt.Errorf("chmod managed VM image artifact %s: %w", a.name, err)
	}
	if err := verifyVMJailStorageEntryUnchanged(a.parent, a.entry, a.file, a.path); err != nil {
		return fmt.Errorf("verify managed VM image artifact %s after chmod: %w", a.name, err)
	}
	info, err := a.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect managed VM image artifact %s after chmod: %w", a.name, err)
	}
	if info.Mode().Perm() != a.mode.Perm() {
		return fmt.Errorf("managed VM image artifact %s mode is %04o, want %04o", a.name, info.Mode().Perm(), a.mode.Perm())
	}
	return nil
}

func (a *vmJailerUpgradeOpenArtifact) close() error {
	if a == nil {
		return nil
	}
	var err error
	if a.file != nil {
		err = errors.Join(err, a.file.Close())
		a.file = nil
	}
	if a.parent != nil {
		err = errors.Join(err, a.parent.Close())
		a.parent = nil
	}
	return err
}
