// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmRuntimeTargetSelectionKind string

const (
	vmRuntimeTargetSelectionChannel         vmRuntimeTargetSelectionKind = "channel"
	vmRuntimeTargetSelectionOfficialID      vmRuntimeTargetSelectionKind = "official-id"
	vmRuntimeTargetSelectionUpstreamVersion vmRuntimeTargetSelectionKind = "upstream-version"
	vmRuntimeTargetSelectionLocalAlias      vmRuntimeTargetSelectionKind = "local-alias"
	vmRuntimeTargetSelectionPrevious        vmRuntimeTargetSelectionKind = "previous"
)

// vmRuntimeResolvedTarget is the exact immutable runtime selected for a
// transition. CatalogRef is present only for catalog-owned selections; local
// aliases are already bound to an immutable manifest by resolveLocalVMRuntime.
type vmRuntimeResolvedTarget struct {
	Artifact   db.VMRuntimeArtifactConfig
	CatalogRef *vmRuntimeCatalogRef
	Selection  vmRuntimeTargetSelectionKind
	Channel    string
	// ChannelFromPolicy requires the transaction to recheck the VM's effective
	// policy channel. Explicit --channel selections are already operator-bound
	// and must not be rejected merely because policy names another channel.
	ChannelFromPolicy bool
	LocalAlias        string
	PolicyMutation    *vmRuntimePolicyMutation
	ExpectedRuntime   *db.VMRuntimeLifecycleConfig
}

// resolveVMRuntimeUpgradeTarget resolves one operator request without changing
// VM state. Official selections use one verified catalog snapshot and one cache
// ensure. Explicit local aliases remain offline and are revalidated from their
// durable alias record and immutable cache entry.
func (s *Server) resolveVMRuntimeUpgradeTarget(ctx context.Context, serviceName, target, requestedChannel string) (vmRuntimeResolvedTarget, error) {
	service, policy, channel, target, err := s.prepareVMRuntimeUpgradeTarget(serviceName, target, requestedChannel)
	if err != nil {
		return vmRuntimeResolvedTarget{}, err
	}
	if strings.HasPrefix(target, "local:") {
		return s.resolveLocalVMRuntimeUpgradeTarget(ctx, target, channel)
	}
	return s.resolveOfficialVMRuntimeUpgradeTarget(ctx, serviceName, target, channel, policy, service.VM.Components.Runtime.Configured)
}

func (s *Server) prepareVMRuntimeUpgradeTarget(serviceName, target, requestedChannel string) (*db.Service, effectiveVMRuntimePolicy, string, string, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, effectiveVMRuntimePolicy{}, "", "", err
	}
	data := dv.AsStruct()
	service := data.Services[serviceName]
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil || service.VM.Components == nil {
		return nil, effectiveVMRuntimePolicy{}, "", "", fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	policy, err := effectiveVMRuntimePolicyFor(data.VMHost, &service.VM.Components.Runtime)
	if err != nil {
		return nil, effectiveVMRuntimePolicy{}, "", "", fmt.Errorf("VM runtime policy for %s: %w", serviceName, err)
	}
	channel, err := normalizeStoredVMRuntimeChannel(requestedChannel, true)
	if err != nil {
		return nil, effectiveVMRuntimePolicy{}, "", "", err
	}
	return service, policy, channel, strings.TrimSpace(target), nil
}

func (s *Server) resolveLocalVMRuntimeUpgradeTarget(ctx context.Context, target, channel string) (vmRuntimeResolvedTarget, error) {
	if channel != "" {
		return vmRuntimeResolvedTarget{}, fmt.Errorf("--channel cannot be used with local VM runtime target %q", target)
	}
	name := strings.TrimPrefix(target, "local:")
	artifact, err := resolveLocalVMRuntime(ctx, filepath.Join(s.cfg.RootDir, "vm-runtimes"), name)
	if err != nil {
		return vmRuntimeResolvedTarget{}, fmt.Errorf("resolve local VM runtime %q: %w", name, err)
	}
	return vmRuntimeResolvedTarget{Artifact: artifact, Selection: vmRuntimeTargetSelectionLocalAlias, LocalAlias: name}, nil
}

func (s *Server) resolveOfficialVMRuntimeUpgradeTarget(ctx context.Context, serviceName, target, channel string, policy effectiveVMRuntimePolicy, configured db.VMRuntimeArtifactConfig) (vmRuntimeResolvedTarget, error) {
	explicitOfficialID, selectedChannel, err := classifyOfficialVMRuntimeTargetRequest(serviceName, target, channel, policy.Channel, configured.Source)
	if err != nil {
		return vmRuntimeResolvedTarget{}, err
	}
	deps := s.runtimeCommandDependencies()
	catalog, err := deps.fetchCatalog(ctx)
	if err != nil {
		return vmRuntimeResolvedTarget{}, fmt.Errorf("fetch VM runtime catalog: %w", err)
	}

	ref, selection, err := resolveOfficialVMRuntimeCatalogTarget(catalog, target, selectedChannel, explicitOfficialID)
	if err != nil {
		return vmRuntimeResolvedTarget{}, err
	}
	if ref.Support == "revoked" {
		return vmRuntimeResolvedTarget{}, fmt.Errorf("VM runtime %s is revoked", ref.RuntimeID)
	}
	artifact, err := deps.ensureRuntime(ctx, ref)
	if err != nil {
		return vmRuntimeResolvedTarget{}, fmt.Errorf("cache VM runtime %s: %w", ref.RuntimeID, err)
	}
	if err := validateResolvedOfficialVMRuntimeArtifact(artifact, ref); err != nil {
		return vmRuntimeResolvedTarget{}, err
	}
	resolved := vmRuntimeResolvedTarget{
		Artifact:  artifact,
		Selection: selection,
	}
	refCopy := ref
	resolved.CatalogRef = &refCopy
	if selection != vmRuntimeTargetSelectionOfficialID {
		resolved.Channel = selectedChannel
		resolved.ChannelFromPolicy = channel == ""
	}
	return resolved, nil
}

func classifyOfficialVMRuntimeTargetRequest(serviceName, target, channel, policyChannel, configuredSource string) (bool, string, error) {
	explicitOfficialID := false
	if target != "" {
		_, targetIDErr := vmRuntimeVersionFromID(target)
		explicitOfficialID = targetIDErr == nil
	}
	if !explicitOfficialID && configuredSource != "official" {
		return false, "", fmt.Errorf("VM %s uses non-official runtime source %q; select an exact local:<name> target or full official runtime ID", serviceName, configuredSource)
	}
	if channel == "" {
		channel = policyChannel
	}
	return explicitOfficialID, channel, nil
}

func resolveOfficialVMRuntimeCatalogTarget(catalog vmRuntimeCatalog, target, channel string, explicitID bool) (vmRuntimeCatalogRef, vmRuntimeTargetSelectionKind, error) {
	if explicitID {
		ref, ok := catalog.RuntimeByID("amd64", target)
		if !ok {
			return vmRuntimeCatalogRef{}, "", fmt.Errorf("official VM runtime %q is absent from the trusted catalog", target)
		}
		return ref, vmRuntimeTargetSelectionOfficialID, nil
	}

	ref, ok := catalog.RuntimeForChannel("amd64", channel)
	if !ok {
		return vmRuntimeCatalogRef{}, "", fmt.Errorf("VM runtime catalog has no promoted %s runtime", channel)
	}
	if target == "" {
		return ref, vmRuntimeTargetSelectionChannel, nil
	}
	version, err := normalizeVMRuntimeUpgradeVersion(target)
	if err != nil {
		return vmRuntimeCatalogRef{}, "", err
	}
	if ref.UpstreamVersion != version {
		return vmRuntimeCatalogRef{}, "", fmt.Errorf("VM runtime upstream version %s is not promoted on the %s channel", version, channel)
	}
	return ref, vmRuntimeTargetSelectionUpstreamVersion, nil
}

func normalizeVMRuntimeUpgradeVersion(target string) (string, error) {
	version := target
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if !vmRuntimeVersionPattern.MatchString(version) {
		return "", fmt.Errorf("VM runtime target %q must be an exact upstream version, full official runtime ID, or local:<name>", target)
	}
	return version, nil
}

func validateResolvedOfficialVMRuntimeArtifact(artifact db.VMRuntimeArtifactConfig, ref vmRuntimeCatalogRef) error {
	if artifact.ID != ref.RuntimeID || artifact.ManifestSHA256 != ref.ManifestSHA || artifact.Source != "official" {
		return fmt.Errorf("cached VM runtime %s did not resolve to the exact catalog identity", ref.RuntimeID)
	}
	if err := validateVMRuntimeArtifact(artifact, "selected"); err != nil {
		return fmt.Errorf("cached VM runtime %s is invalid: %w", ref.RuntimeID, err)
	}
	return nil
}
