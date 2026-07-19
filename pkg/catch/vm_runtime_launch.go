// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmRuntimeLaunchSelection struct {
	Service          string
	DescriptorSHA256 string
	Selected         db.VMRuntimeArtifactConfig
	Fallback         *db.VMRuntimeArtifactConfig
	Trial            bool
}

type vmRuntimeLaunchDeps struct {
	readDescriptor func(string, string, uint32, uint32) (vmRuntimeDescriptorSnapshot, error)
	evidence       vmRuntimeAdoptionEvidenceDeps
	architecture   func(string) (string, error)
	runtimePair    func(context.Context, string, string) (string, error)
}

func defaultVMRuntimeLaunchDeps() vmRuntimeLaunchDeps {
	return vmRuntimeLaunchDeps{
		readDescriptor: readVMRuntimeDescriptorSnapshotWithOwner,
		evidence:       defaultVMRuntimeAdoptionEvidenceDeps(),
		architecture:   inspectVMRuntimeBinaryArchitecture,
		runtimePair:    probeMatchingVMRuntimePair,
	}
}

func completeVMRuntimeLaunchDeps(deps vmRuntimeLaunchDeps) vmRuntimeLaunchDeps {
	defaults := defaultVMRuntimeLaunchDeps()
	if deps.readDescriptor == nil {
		deps.readDescriptor = defaults.readDescriptor
	}
	deps.evidence = completeVMRuntimeAdoptionEvidenceDeps(deps.evidence)
	if deps.architecture == nil {
		deps.architecture = defaults.architecture
	}
	if deps.runtimePair == nil {
		deps.runtimePair = defaults.runtimePair
	}
	return deps
}

func selectVMRuntimeLaunch(descriptorPath, service string, uid, gid uint32, deps vmRuntimeLaunchDeps) (vmRuntimeLaunchSelection, error) {
	deps = completeVMRuntimeLaunchDeps(deps)
	snapshot, err := deps.readDescriptor(descriptorPath, service, uid, gid)
	if err != nil {
		return vmRuntimeLaunchSelection{}, err
	}
	descriptor := snapshot.Descriptor
	selection := vmRuntimeLaunchSelection{
		Service: service, DescriptorSHA256: snapshot.SHA256,
		Selected: descriptor.Configured,
	}
	if descriptor.Trial {
		if descriptor.Staged == nil {
			return vmRuntimeLaunchSelection{}, fmt.Errorf("VM runtime trial descriptor has no staged candidate")
		}
		selection.Selected = *descriptor.Staged
		selection.Fallback = cloneVMRuntimeArtifact(&descriptor.Configured)
		selection.Trial = true
		if selection.Selected == *selection.Fallback {
			return vmRuntimeLaunchSelection{}, fmt.Errorf("VM runtime trial candidate matches configured fallback")
		}
	}
	return selection, nil
}

// verifyVMRuntimeLaunchArtifact is the final immutable-artifact gate before a
// jailer command is constructed. It hashes trusted no-follow file handles,
// verifies both ELF architectures and the matching Firecracker/jailer version,
// then re-collects the exact inode/digest evidence to close replacement races.
func verifyVMRuntimeLaunchArtifact(ctx context.Context, artifact db.VMRuntimeArtifactConfig, deps vmRuntimeLaunchDeps) (string, error) {
	deps = completeVMRuntimeLaunchDeps(deps)
	if err := validateVMRuntimeArtifact(artifact, "launch"); err != nil {
		return "", err
	}
	before, err := collectVMRuntimeLaunchEvidence(artifact, deps.evidence)
	if err != nil {
		return "", err
	}
	for _, component := range []struct {
		name string
		path string
	}{
		{name: "Firecracker", path: artifact.Firecracker},
		{name: "jailer", path: artifact.Jailer},
	} {
		architecture, err := deps.architecture(component.path)
		if err != nil {
			return "", fmt.Errorf("inspect VM runtime %s architecture: %w", component.name, err)
		}
		if architecture != "amd64" {
			return "", fmt.Errorf("VM runtime %s architecture is %q, want amd64", component.name, architecture)
		}
	}
	version, err := deps.runtimePair(ctx, artifact.Firecracker, artifact.Jailer)
	if err != nil {
		return "", err
	}
	after, err := collectVMRuntimeLaunchEvidence(artifact, deps.evidence)
	if err != nil {
		return "", err
	}
	if before.firecracker != after.firecracker || before.jailer != after.jailer {
		return "", fmt.Errorf("VM runtime %s binaries changed during launch verification", artifact.ID)
	}
	return version, nil
}

func probeMatchingVMRuntimePair(ctx context.Context, firecracker, jailer string) (string, error) {
	for _, path := range []string{firecracker, jailer} {
		if err := vmJailValidateTrustedInput(path); err != nil {
			return "", err
		}
	}
	firecrackerVersion, err := vmJailProbeVersion(ctx, firecracker)
	if err != nil {
		return "", err
	}
	jailerVersion, err := vmJailProbeVersion(ctx, jailer)
	if err != nil {
		return "", err
	}
	if err := validateVMJailerPairVersion(firecrackerVersion, jailerVersion); err != nil {
		return "", err
	}
	version := vmJailerVersion(firecrackerVersion)
	if version == "" {
		return "", fmt.Errorf("matching VM runtime version is empty")
	}
	return version, nil
}

type vmRuntimeLaunchEvidence struct {
	firecracker vmRuntimeAdoptionFileEvidence
	jailer      vmRuntimeAdoptionFileEvidence
}

func collectVMRuntimeLaunchEvidence(artifact db.VMRuntimeArtifactConfig, deps vmRuntimeAdoptionEvidenceDeps) (vmRuntimeLaunchEvidence, error) {
	var result vmRuntimeLaunchEvidence
	checks := []struct {
		name   string
		path   string
		digest string
		set    func(vmRuntimeAdoptionFileEvidence)
	}{
		{name: "Firecracker", path: artifact.Firecracker, digest: artifact.FirecrackerSHA256, set: func(value vmRuntimeAdoptionFileEvidence) { result.firecracker = value }},
		{name: "jailer", path: artifact.Jailer, digest: artifact.JailerSHA256, set: func(value vmRuntimeAdoptionFileEvidence) { result.jailer = value }},
	}
	for _, check := range checks {
		evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(check.path, true, deps)
		if err != nil {
			return vmRuntimeLaunchEvidence{}, fmt.Errorf("open trusted VM runtime %s: %w", check.name, err)
		}
		if evidence.SHA256 != check.digest {
			return vmRuntimeLaunchEvidence{}, fmt.Errorf("VM runtime %s %s digest mismatch", artifact.ID, check.name)
		}
		check.set(evidence)
	}
	if result.firecracker.Path == result.jailer.Path || result.firecracker.Device == result.jailer.Device && result.firecracker.Inode == result.jailer.Inode {
		return vmRuntimeLaunchEvidence{}, errors.New("firecracker and jailer must be distinct trusted binaries")
	}
	return result, nil
}
