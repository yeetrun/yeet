// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"path/filepath"

	"github.com/yeetrun/yeet/pkg/db"
)

// recoverDescriptorUnit handles a lost database projection of an already
// adopted VM. Host-owned launch inputs are evidence, not unconditional authority:
// every path, digest and loaded unit is verified and bound to the transaction.
func (inventory *vmRuntimeAdoptionServiceInventory) recoverDescriptorUnit() (bool, error) {
	unit := inventory.unit
	if err := validateVMRuntimeAdoptionLoadedUnitEvidence(unit, inventory.service.Name); err != nil {
		return false, err
	}
	runner, flags, err := validateVMRuntimeAdoptionLoadedCommand(unit.ExecStart)
	if err != nil {
		return false, err
	}
	if _, present := flags["--runtime-descriptor"]; !present {
		return false, nil
	}
	preparation := inventory.preparation
	unit.Runner = runner
	unit.JailerBase = vmJailerBaseForDataRoot(inventory.cfg.RootDir)
	preparation.EffectiveUnit = unit
	preparation.Evidence.ActiveDisk = inventory.activeDisk
	if err := validateDescriptorVMRuntimeAdoptionLoadedState(*preparation, unit); err != nil {
		return true, err
	}
	if flags["--config-file"] != inventory.configPath {
		return true, fmt.Errorf("descriptor VM config path does not match service root")
	}
	descriptor, err := inventory.readRecoveryDescriptor()
	if err != nil {
		return true, err
	}
	inventory.unit = unit
	inventory.unitArgs = vmRuntimeAdoptionUnitArgs{
		runner: runner, jailerBase: unit.JailerBase,
		firecracker: descriptor.Configured.Firecracker, jailer: descriptor.Configured.Jailer,
	}
	preparation.RecoveredDescriptor = &descriptor
	return true, nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) readRecoveryDescriptor() (vmRuntimeDescriptor, error) {
	path := filepath.Join(serviceDataDirForRoot(inventory.root), vmRuntimeDescriptorFileName)
	raw, evidence, err := readTrustedVMRuntimeAdoptionFile(path, vmRuntimeDescriptorMaxSize, inventory.deps.evidence)
	if err != nil {
		return vmRuntimeDescriptor{}, fmt.Errorf("read recovery descriptor: %w", err)
	}
	descriptor, err := decodeVMRuntimeDescriptor(raw, inventory.service.Name)
	if err != nil {
		return vmRuntimeDescriptor{}, err
	}
	// The descriptor does not contain the complete trial state or policy. Do not
	// guess how to resume a staged/in-flight transition after metadata loss.
	if descriptor.Staged != nil || descriptor.Trial {
		return vmRuntimeDescriptor{}, fmt.Errorf("runtime metadata recovery requires no staged trial; retain descriptor for operator recovery")
	}
	inventory.sourceEvidence = append(inventory.sourceEvidence, evidence)
	artifacts := []db.VMRuntimeArtifactConfig{descriptor.Configured}
	if descriptor.Previous != nil {
		artifacts = append(artifacts, *descriptor.Previous)
	}
	for _, artifact := range artifacts {
		if err := inventory.verifyRecoveryArtifact(artifact); err != nil {
			return vmRuntimeDescriptor{}, err
		}
	}
	return descriptor, nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) verifyRecoveryArtifact(artifact db.VMRuntimeArtifactConfig) error {
	evidence, err := collectVMRuntimeLaunchEvidence(artifact, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("verify recovery runtime %s: %w", artifact.ID, err)
	}
	if _, err := inventory.deps.runtimePair(inventory.ctx, artifact.Firecracker, artifact.Jailer); err != nil {
		return fmt.Errorf("verify recovery runtime pair %s: %w", artifact.ID, err)
	}
	inventory.sourceEvidence = append(inventory.sourceEvidence, evidence.firecracker, evidence.jailer)
	return nil
}
