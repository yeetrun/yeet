// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

type effectiveVMRuntimePolicy struct {
	Mode    string
	Channel string
}

type vmRuntimePolicyReconcileResult struct {
	Service   string
	State     string
	RuntimeID string
}

type VMRuntimePolicyReconcileSummary struct {
	Staged   []string
	Skipped  []string
	Warnings []string
}

type vmRuntimePolicyMutation struct {
	ExpectedPolicy  string
	ExpectedChannel string
	Policy          string
	Channel         string
}

type vmRuntimePolicyPlan struct {
	result vmRuntimePolicyReconcileResult
	target *vmRuntimeResolvedTarget
}

type vmRuntimePolicyFleetPrecondition struct {
	Service string
	Runtime db.VMRuntimeLifecycleConfig
}

type vmRuntimePolicyArtifactLoader struct {
	loaded     bool
	catalog    vmRuntimeCatalog
	catalogErr error
	artifacts  map[string]db.VMRuntimeArtifactConfig
}

var legacyVMRuntimePolicyIDPattern = regexp.MustCompile(`^legacy-firecracker-v?([0-9]+)-([0-9]+)-([0-9]+)-[a-f0-9]{64}-jailer-[a-f0-9]{64}$`)
var officialVMRuntimePolicyIDPattern = regexp.MustCompile(`^firecracker-(v[0-9]+\.[0-9]+\.[0-9]+)-yeet-v([1-9][0-9]*)$`)

func (s *Server) reconcileVMRuntimePolicy(ctx context.Context, serviceName string) (vmRuntimePolicyReconcileResult, error) {
	view, err := s.getDB()
	if err != nil {
		return vmRuntimePolicyReconcileResult{}, err
	}
	plan, err := s.planVMRuntimePolicy(ctx, view.AsStruct(), serviceName)
	if err != nil {
		return vmRuntimePolicyReconcileResult{}, err
	}
	if plan.target == nil {
		return plan.result, nil
	}
	transition, err := s.runtimeCommandDependencies().stageRuntime(ctx, &s.cfg, serviceName, *plan.target)
	if err != nil {
		return vmRuntimePolicyReconcileResult{}, fmt.Errorf("stage VM runtime policy for %s: %w", serviceName, err)
	}
	plan.result.State = "staged"
	if transition.Staged != nil {
		plan.result.RuntimeID = transition.Staged.ID
	}
	return plan.result, nil
}

func ReconcileVMRuntimePoliciesOnCatchUpgrade(ctx context.Context, cfg *Config) (VMRuntimePolicyReconcileSummary, error) {
	if cfg == nil || cfg.DB == nil {
		return VMRuntimePolicyReconcileSummary{}, fmt.Errorf("catch configuration with database is required for VM runtime policy reconciliation")
	}
	server := &Server{cfg: *cfg}
	return server.reconcileVMRuntimePoliciesForCatchUpgrade(ctx)
}

func (s *Server) reconcileVMRuntimePoliciesForCatchUpgrade(ctx context.Context) (VMRuntimePolicyReconcileSummary, error) {
	view, err := s.getDB()
	if err != nil {
		return VMRuntimePolicyReconcileSummary{}, err
	}
	summary := VMRuntimePolicyReconcileSummary{}
	loader := &vmRuntimePolicyArtifactLoader{}
	for _, service := range adoptedVMRuntimeServices(view.AsStruct()) {
		plan, err := s.planVMRuntimePolicyWithLoader(ctx, view.AsStruct(), service.Name, loader)
		if err != nil {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("%s: %v", service.Name, err))
			continue
		}
		if plan.target == nil {
			summary.Skipped = append(summary.Skipped, service.Name)
			continue
		}
		if _, err := s.runtimeCommandDependencies().stageRuntime(ctx, &s.cfg, service.Name, *plan.target); err != nil {
			return summary, fmt.Errorf("stage VM runtime policy for %s during Catch upgrade: %w", service.Name, err)
		}
		summary.Staged = append(summary.Staged, service.Name)
	}
	return summary, nil
}

func (s *Server) setVMRuntimePolicyAndReconcile(ctx context.Context, serviceName, policyName, channel string) (*db.Data, error) {
	view, err := s.getDB()
	if err != nil {
		return nil, err
	}
	current := view.AsStruct()
	service := current.Services[serviceName]
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil || service.VM.Components == nil {
		return nil, fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	oldRuntime := service.VM.Components.Runtime
	policy, policyChannel := desiredStoredVMRuntimePolicy(oldRuntime, policyName, channel)
	hypothetical := current.Clone()
	hypotheticalRuntime := &hypothetical.Services[serviceName].VM.Components.Runtime
	hypotheticalRuntime.Policy = policy
	hypotheticalRuntime.Channel = policyChannel
	plan, err := s.planVMRuntimePolicy(ctx, hypothetical, serviceName)
	if err != nil {
		return nil, err
	}
	if plan.target != nil {
		mutation := vmRuntimePolicyMutation{
			ExpectedPolicy: oldRuntime.Policy, ExpectedChannel: oldRuntime.Channel,
			Policy: policy, Channel: policyChannel,
		}
		plan.target.PolicyMutation = &mutation
		if _, err := s.runtimeCommandDependencies().stageRuntime(ctx, &s.cfg, serviceName, *plan.target); err != nil {
			return nil, fmt.Errorf("stage VM runtime while setting policy for %s: %w", serviceName, err)
		}
		updated, err := s.cfg.DB.Get()
		if err != nil {
			return nil, err
		}
		return updated.AsStruct(), nil
	}
	return s.mutateStoredVMRuntimePolicy(ctx, serviceName, oldRuntime, policy, policyChannel)
}

func desiredStoredVMRuntimePolicy(runtime db.VMRuntimeLifecycleConfig, policyName, channel string) (string, string) {
	if policyName == cli.VMRuntimePolicyInherit {
		return "", ""
	}
	if channel == "" {
		channel = runtime.Channel
	}
	return policyName, channel
}

func (s *Server) mutateStoredVMRuntimePolicy(ctx context.Context, serviceName string, oldRuntime db.VMRuntimeLifecycleConfig, policy, channel string) (*db.Data, error) {
	var updated *db.Data
	err := WithVMRuntimeTransactionLock(ctx, &s.cfg, func() error {
		var err error
		updated, err = s.cfg.DB.MutateData(func(data *db.Data) error {
			service := data.Services[serviceName]
			if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil || service.VM.Components == nil {
				return fmt.Errorf("service %q is not an adopted VM", serviceName)
			}
			runtimeState := &service.VM.Components.Runtime
			if !reflect.DeepEqual(*runtimeState, oldRuntime) {
				return fmt.Errorf("VM runtime lifecycle changed while applying operator selection")
			}
			runtimeState.Policy = policy
			runtimeState.Channel = channel
			_, err := effectiveVMRuntimePolicyFor(data.VMHost, runtimeState)
			return err
		})
		return err
	})
	return updated, err
}

func (s *Server) setVMRuntimePolicyDefaultsAndReconcile(ctx context.Context, policyName, channel string) (*db.Data, error) {
	view, err := s.getDB()
	if err != nil {
		return nil, err
	}
	current := view.AsStruct()
	fleet := snapshotVMRuntimePolicyFleet(current)
	oldHost := current.VMHost.Clone()
	oldPolicy, oldChannel := "", ""
	if oldHost != nil {
		oldPolicy, oldChannel = oldHost.RuntimePolicy, oldHost.RuntimeChannel
	}
	if channel == "" {
		channel = oldChannel
	}
	hypothetical, err := dataWithVMRuntimeHostPolicy(current, policyName, channel)
	if err != nil {
		return nil, err
	}
	plans, err := s.planVMRuntimeHostPolicy(ctx, hypothetical, policyName)
	if err != nil {
		return nil, err
	}
	if len(plans) != 0 {
		return s.stageVMRuntimeHostPolicy(ctx, oldHost, hypothetical.VMHost, fleet, plans)
	}
	return s.persistVMRuntimeHostPolicy(ctx, fleet, oldPolicy, oldChannel, policyName, channel)
}

func dataWithVMRuntimeHostPolicy(current *db.Data, policyName, channel string) (*db.Data, error) {
	hypothetical := current.Clone()
	if hypothetical.VMHost == nil {
		hypothetical.VMHost = &db.VMHostConfig{}
	}
	hypothetical.VMHost.RuntimePolicy = policyName
	hypothetical.VMHost.RuntimeChannel = channel
	if _, err := effectiveVMRuntimePolicyFor(hypothetical.VMHost, nil); err != nil {
		return nil, err
	}
	return hypothetical, nil
}

func (s *Server) planVMRuntimeHostPolicy(ctx context.Context, data *db.Data, policyName string) ([]vmRuntimePolicyPlan, error) {
	if policyName != cli.VMRuntimePolicyStageOnRestart {
		return nil, nil
	}
	loader := &vmRuntimePolicyArtifactLoader{}
	plans := make([]vmRuntimePolicyPlan, 0)
	for _, service := range adoptedVMRuntimeServices(data) {
		plan, err := s.planVMRuntimePolicyWithLoader(ctx, data, service.Name, loader)
		if err != nil {
			return nil, err
		}
		if plan.target != nil {
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

func (s *Server) stageVMRuntimeHostPolicy(ctx context.Context, oldHost, newHost *db.VMHostConfig, fleet []vmRuntimePolicyFleetPrecondition, plans []vmRuntimePolicyPlan) (*db.Data, error) {
	targets := make([]vmRuntimeNamedTarget, len(plans))
	for i := range plans {
		targets[i] = vmRuntimeNamedTarget{Service: plans[i].result.Service, Target: *plans[i].target}
	}
	if err := s.runtimeCommandDependencies().stagePolicyCohort(ctx, &s.cfg, oldHost, newHost, fleet, targets); err != nil {
		return nil, fmt.Errorf("stage VM runtime host policy cohort: %w", err)
	}
	latest, err := s.cfg.DB.Get()
	if err != nil {
		return nil, err
	}
	return latest.AsStruct(), nil
}

func (s *Server) persistVMRuntimeHostPolicy(ctx context.Context, fleet []vmRuntimePolicyFleetPrecondition, oldPolicy, oldChannel, policyName, channel string) (*db.Data, error) {
	var updated *db.Data
	err := WithVMRuntimeRootLock(ctx, s.cfg.RootDir, func() error {
		var err error
		updated, err = s.cfg.DB.MutateData(func(data *db.Data) error {
			return applyVMRuntimeHostPolicy(data, fleet, oldPolicy, oldChannel, policyName, channel)
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func applyVMRuntimeHostPolicy(data *db.Data, fleet []vmRuntimePolicyFleetPrecondition, oldPolicy, oldChannel, policyName, channel string) error {
	if err := validateVMRuntimePolicyFleetPrecondition(data, fleet); err != nil {
		return err
	}
	currentPolicy, currentChannel := "", ""
	if data.VMHost != nil {
		currentPolicy, currentChannel = data.VMHost.RuntimePolicy, data.VMHost.RuntimeChannel
	}
	if currentPolicy != oldPolicy || currentChannel != oldChannel {
		return fmt.Errorf("host VM runtime policy changed while applying operator selection")
	}
	if data.VMHost == nil {
		data.VMHost = &db.VMHostConfig{}
	}
	data.VMHost.RuntimePolicy = policyName
	data.VMHost.RuntimeChannel = channel
	_, err := effectiveVMRuntimePolicyFor(data.VMHost, nil)
	return err
}

func snapshotVMRuntimePolicyFleet(data *db.Data) []vmRuntimePolicyFleetPrecondition {
	services := adoptedVMRuntimeServices(data)
	result := make([]vmRuntimePolicyFleetPrecondition, len(services))
	for i, service := range services {
		result[i] = vmRuntimePolicyFleetPrecondition{
			Service: service.Name,
			Runtime: service.VM.Components.Clone().Runtime,
		}
	}
	return result
}

func validateVMRuntimePolicyFleetPrecondition(data *db.Data, expected []vmRuntimePolicyFleetPrecondition) error {
	services := adoptedVMRuntimeServices(data)
	if len(services) != len(expected) {
		return fmt.Errorf("adopted VM fleet changed while applying host runtime policy")
	}
	for i, service := range services {
		if service.Name != expected[i].Service || !reflect.DeepEqual(service.VM.Components.Runtime, expected[i].Runtime) {
			return fmt.Errorf("adopted VM fleet changed while applying host runtime policy")
		}
	}
	return nil
}

func (s *Server) planVMRuntimePolicy(ctx context.Context, data *db.Data, serviceName string) (vmRuntimePolicyPlan, error) {
	return s.planVMRuntimePolicyWithLoader(ctx, data, serviceName, &vmRuntimePolicyArtifactLoader{})
}

func (s *Server) planVMRuntimePolicyWithLoader(
	ctx context.Context,
	data *db.Data,
	serviceName string,
	loader *vmRuntimePolicyArtifactLoader,
) (vmRuntimePolicyPlan, error) {
	service, err := adoptedVMRuntimePolicyService(data, serviceName)
	if err != nil {
		return vmRuntimePolicyPlan{}, err
	}
	runtimeState := &service.VM.Components.Runtime
	policy, err := effectiveVMRuntimePolicyFor(data.VMHost, runtimeState)
	if err != nil {
		return vmRuntimePolicyPlan{}, fmt.Errorf("VM runtime policy for %s: %w", serviceName, err)
	}
	plan := vmRuntimePolicyPlan{result: vmRuntimePolicyReconcileResult{Service: serviceName, State: "unchanged"}}
	if policy.Mode != cli.VMRuntimePolicyStageOnRestart {
		plan.result.State = "manual"
		return plan, nil
	}
	if runtimeState.Staged != nil {
		plan.result.State = "already-staged"
		plan.result.RuntimeID = runtimeState.Staged.ID
		return plan, nil
	}
	if !vmRuntimePolicyAllowsOfficialUpgrade(runtimeState.Configured.Source) {
		plan.result.State = "source-ineligible"
		return plan, nil
	}

	return s.planPromotedVMRuntimePolicy(ctx, serviceName, runtimeState, policy, loader, plan)
}

func adoptedVMRuntimePolicyService(data *db.Data, serviceName string) (*db.Service, error) {
	service := data.Services[serviceName]
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil || service.VM.Components == nil {
		return nil, fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	return service, nil
}

func (s *Server) planPromotedVMRuntimePolicy(ctx context.Context, serviceName string, runtimeState *db.VMRuntimeLifecycleConfig, policy effectiveVMRuntimePolicy, loader *vmRuntimePolicyArtifactLoader, plan vmRuntimePolicyPlan) (vmRuntimePolicyPlan, error) {
	catalog, err := loader.loadCatalog(ctx, s)
	if err != nil {
		return vmRuntimePolicyPlan{}, fmt.Errorf("fetch VM runtime catalog for %s: %w", serviceName, err)
	}
	ref, ok := catalog.RuntimeForChannel("amd64", policy.Channel)
	if !ok {
		return vmRuntimePolicyPlan{}, fmt.Errorf("VM runtime catalog has no promoted %s runtime", policy.Channel)
	}
	if ref.Support == "revoked" {
		return vmRuntimePolicyPlan{}, fmt.Errorf("promoted VM runtime %s is revoked", ref.RuntimeID)
	}
	newer, err := promotedVMRuntimeIsNewer(ref, runtimeState.Configured)
	if err != nil {
		return vmRuntimePolicyPlan{}, fmt.Errorf("compare promoted VM runtime for %s: %w", serviceName, err)
	}
	if !newer {
		plan.result.State = "current"
		plan.result.RuntimeID = runtimeState.Configured.ID
		return plan, nil
	}
	artifact, err := loader.ensureRuntime(ctx, s, ref)
	if err != nil {
		return vmRuntimePolicyPlan{}, fmt.Errorf("cache VM runtime %s for %s: %w", ref.RuntimeID, serviceName, err)
	}
	if err := validateResolvedOfficialVMRuntimeArtifact(artifact, ref); err != nil {
		return vmRuntimePolicyPlan{}, err
	}
	refCopy := ref
	plan.target = &vmRuntimeResolvedTarget{
		Artifact: artifact, CatalogRef: &refCopy,
		Selection: vmRuntimeTargetSelectionChannel, Channel: policy.Channel, ChannelFromPolicy: true,
		ExpectedRuntime: runtimeState.Clone(),
	}
	plan.result.State = "ready-to-stage"
	plan.result.RuntimeID = artifact.ID
	return plan, nil
}

func (loader *vmRuntimePolicyArtifactLoader) loadCatalog(ctx context.Context, s *Server) (vmRuntimeCatalog, error) {
	if !loader.loaded {
		loader.loaded = true
		loader.catalog, loader.catalogErr = s.runtimeCommandDependencies().fetchCatalog(ctx)
	}
	return loader.catalog, loader.catalogErr
}

func (loader *vmRuntimePolicyArtifactLoader) ensureRuntime(ctx context.Context, s *Server, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
	if loader.artifacts == nil {
		loader.artifacts = make(map[string]db.VMRuntimeArtifactConfig)
	}
	key := vmRuntimeCatalogKey(ref.RuntimeID, ref.ManifestSHA)
	if artifact, ok := loader.artifacts[key]; ok {
		return artifact, nil
	}
	artifact, err := s.runtimeCommandDependencies().ensureRuntime(ctx, ref)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	loader.artifacts[key] = artifact
	return artifact, nil
}

func vmRuntimePolicyAllowsOfficialUpgrade(source string) bool {
	switch strings.TrimSpace(source) {
	case "official", string(vmRuntimeAdoptionOfficialLegacy):
		return true
	default:
		return false
	}
}

func promotedVMRuntimeIsNewer(promoted vmRuntimeCatalogRef, configured db.VMRuntimeArtifactConfig) (bool, error) {
	if configured.Source == "official" && configured.ID == promoted.RuntimeID {
		if configured.ManifestSHA256 != promoted.ManifestSHA {
			return false, fmt.Errorf("official runtime ID %s changed immutable manifest identity", configured.ID)
		}
		return false, nil
	}
	current, next, err := comparableVMRuntimePolicyVersions(promoted, configured)
	if err != nil {
		return false, err
	}
	if next.GreaterThan(current) {
		return true, nil
	}
	if next.LessThan(current) {
		return false, nil
	}
	if configured.Source == string(vmRuntimeAdoptionOfficialLegacy) {
		return true, nil
	}
	return newerVMRuntimePolicyPackagingRevision(promoted.RuntimeID, configured.ID)
}

func comparableVMRuntimePolicyVersions(promoted vmRuntimeCatalogRef, configured db.VMRuntimeArtifactConfig) (*semver.Version, *semver.Version, error) {
	configuredVersion, err := vmRuntimePolicyArtifactVersion(configured)
	if err != nil {
		return nil, nil, err
	}
	current, err := semver.NewVersion(strings.TrimPrefix(configuredVersion, "v"))
	if err != nil {
		return nil, nil, fmt.Errorf("configured runtime version %q is invalid: %w", configuredVersion, err)
	}
	next, err := semver.NewVersion(strings.TrimPrefix(promoted.UpstreamVersion, "v"))
	if err != nil {
		return nil, nil, fmt.Errorf("promoted runtime version %q is invalid: %w", promoted.UpstreamVersion, err)
	}
	return current, next, nil
}

func newerVMRuntimePolicyPackagingRevision(promotedID, configuredID string) (bool, error) {
	configuredRevision, err := vmRuntimePolicyPackagingRevision(configuredID)
	if err != nil {
		return false, err
	}
	promotedRevision, err := vmRuntimePolicyPackagingRevision(promotedID)
	if err != nil {
		return false, err
	}
	return promotedRevision > configuredRevision, nil
}

func vmRuntimePolicyPackagingRevision(runtimeID string) (int, error) {
	match := officialVMRuntimePolicyIDPattern.FindStringSubmatch(runtimeID)
	if len(match) != 3 {
		return 0, fmt.Errorf("invalid official runtime ID %q", runtimeID)
	}
	revision, err := strconv.Atoi(match[2])
	if err != nil {
		return 0, fmt.Errorf("invalid official runtime packaging revision in %q: %w", runtimeID, err)
	}
	return revision, nil
}

func vmRuntimePolicyArtifactVersion(artifact db.VMRuntimeArtifactConfig) (string, error) {
	if artifact.Source == "official" {
		return vmRuntimeVersionFromID(artifact.ID)
	}
	if artifact.Source == string(vmRuntimeAdoptionOfficialLegacy) {
		match := legacyVMRuntimePolicyIDPattern.FindStringSubmatch(artifact.ID)
		if len(match) == 4 {
			return fmt.Sprintf("v%s.%s.%s", match[1], match[2], match[3]), nil
		}
	}
	return "", fmt.Errorf("runtime %s does not expose a comparable official version", artifact.ID)
}

func effectiveVMRuntimePolicyFor(host *db.VMHostConfig, runtime *db.VMRuntimeLifecycleConfig) (effectiveVMRuntimePolicy, error) {
	policy := effectiveVMRuntimePolicy{Mode: cli.VMRuntimePolicyManual, Channel: cli.VMRuntimeChannelStable}
	if host != nil {
		if err := applyEffectiveVMRuntimePolicy(&policy, host.RuntimePolicy, host.RuntimeChannel, "host "); err != nil {
			return effectiveVMRuntimePolicy{}, err
		}
	}
	if runtime != nil {
		if err := applyEffectiveVMRuntimePolicy(&policy, runtime.Policy, runtime.Channel, ""); err != nil {
			return effectiveVMRuntimePolicy{}, err
		}
	}
	return policy, nil
}

func applyEffectiveVMRuntimePolicy(policy *effectiveVMRuntimePolicy, rawMode, rawChannel, label string) error {
	var err error
	if strings.TrimSpace(rawMode) != "" {
		policy.Mode, err = normalizeVMRuntimePolicyMode(rawMode, false)
		if err != nil {
			return fmt.Errorf("invalid %sVM runtime policy: %w", label, err)
		}
	}
	if strings.TrimSpace(rawChannel) != "" {
		policy.Channel, err = normalizeStoredVMRuntimeChannel(rawChannel, false)
		if err != nil {
			return fmt.Errorf("invalid %sVM runtime channel: %w", label, err)
		}
	}
	return nil
}

func normalizeVMRuntimePolicyMode(raw string, allowInherit bool) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case cli.VMRuntimePolicyManual, cli.VMRuntimePolicyStageOnRestart:
		return value, nil
	case cli.VMRuntimePolicyInherit:
		if allowInherit {
			return value, nil
		}
	}
	if allowInherit {
		return "", fmt.Errorf("policy must be inherit, manual, or stage-on-restart")
	}
	return "", fmt.Errorf("policy must be manual or stage-on-restart")
}

func normalizeStoredVMRuntimeChannel(raw string, allowEmpty bool) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" && allowEmpty {
		return "", nil
	}
	if value == cli.VMRuntimeChannelStable || value == cli.VMRuntimeChannelCandidate {
		return value, nil
	}
	return "", fmt.Errorf("channel must be stable or candidate")
}

func applyVMRuntimePolicyMutation(runtime *db.VMRuntimeLifecycleConfig, mutation vmRuntimePolicyMutation) error {
	if runtime.Policy != mutation.ExpectedPolicy || runtime.Channel != mutation.ExpectedChannel {
		return fmt.Errorf("VM runtime policy changed during staged policy selection")
	}
	runtime.Policy = mutation.Policy
	runtime.Channel = mutation.Channel
	return nil
}

func vmRuntimePolicyMutationMatches(runtime db.VMRuntimeLifecycleConfig, mutation *vmRuntimePolicyMutation) bool {
	return mutation == nil || runtime.Policy == mutation.Policy && runtime.Channel == mutation.Channel
}
