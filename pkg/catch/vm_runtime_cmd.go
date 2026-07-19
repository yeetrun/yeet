// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type vmRuntimeCommandDeps struct {
	fetchCatalog      func(context.Context) (vmRuntimeCatalog, error)
	ensureRuntime     func(context.Context, vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error)
	importRuntime     func(context.Context, string, string, io.Reader) (db.VMRuntimeArtifactConfig, error)
	stageRuntime      func(context.Context, *Config, string, vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error)
	stagePolicyCohort func(context.Context, *Config, *db.VMHostConfig, *db.VMHostConfig, []vmRuntimePolicyFleetPrecondition, []vmRuntimeNamedTarget) error
	serviceActive     func(string) (bool, error)
	unitState         func(context.Context, string) (vmRuntimeUnitState, error)
	processAlive      func(int) bool
	jailerState       func(string, uint32, uint32) (vmJailerReadiness, error)
	expectedUID       uint32
	expectedGID       uint32
}

func (s *Server) runtimeCommandDependencies() vmRuntimeCommandDeps {
	cache := vmRuntimeCache{Root: filepath.Join(s.cfg.RootDir, "vm-runtimes")}
	deps := vmRuntimeCommandDeps{
		fetchCatalog:  cache.FetchCatalog,
		ensureRuntime: cache.Ensure,
		importRuntime: importVMRuntime,
		stageRuntime:  stageVMRuntime,
		stagePolicyCohort: func(ctx context.Context, cfg *Config, oldHost, newHost *db.VMHostConfig, fleet []vmRuntimePolicyFleetPrecondition, targets []vmRuntimeNamedTarget) error {
			return stageVMRuntimePolicyCohort(ctx, cfg, oldHost, newHost, fleet, targets, defaultVMRuntimeAdoptionCoordinatorDeps())
		},
		serviceActive: func(service string) (bool, error) {
			status, err := serverVMStatusFunc(service)
			return status == svc.StatusRunning, err
		},
		unitState: func(ctx context.Context, service string) (vmRuntimeUnitState, error) {
			return readVMRuntimeUnitState(ctx, vmSystemdUnitName(service))
		},
		processAlive: vmRuntimeProcessAlive,
		jailerState:  vmJailerReadinessForRootWithOwner,
		expectedUID:  0,
		expectedGID:  0,
	}
	if s.vmRuntimeCommandDeps == nil {
		return deps
	}
	override := *s.vmRuntimeCommandDeps
	deps = overrideVMRuntimeCatalogDeps(deps, override)
	deps = overrideVMRuntimeHostDeps(deps, override)
	deps.expectedUID = override.expectedUID
	deps.expectedGID = override.expectedGID
	return deps
}

func overrideVMRuntimeCatalogDeps(deps, override vmRuntimeCommandDeps) vmRuntimeCommandDeps {
	if override.fetchCatalog != nil {
		deps.fetchCatalog = override.fetchCatalog
	}
	if override.ensureRuntime != nil {
		deps.ensureRuntime = override.ensureRuntime
	}
	if override.importRuntime != nil {
		deps.importRuntime = override.importRuntime
	}
	if override.stageRuntime != nil {
		deps.stageRuntime = override.stageRuntime
	}
	if override.stagePolicyCohort != nil {
		deps.stagePolicyCohort = override.stagePolicyCohort
	}
	return deps
}

func overrideVMRuntimeHostDeps(deps, override vmRuntimeCommandDeps) vmRuntimeCommandDeps {
	if override.serviceActive != nil {
		deps.serviceActive = override.serviceActive
	}
	if override.unitState != nil {
		deps.unitState = override.unitState
	}
	if override.processAlive != nil {
		deps.processAlive = override.processAlive
	}
	if override.jailerState != nil {
		deps.jailerState = override.jailerState
	}
	return deps
}

func vmRuntimeProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func (s *Server) updateVMRuntimes(ctx context.Context, w io.Writer) error {
	deps := s.runtimeCommandDependencies()
	catalog, err := deps.fetchCatalog(ctx)
	if err != nil {
		return err
	}
	var artifacts []db.VMRuntimeArtifactConfig
	if err := WithVMRuntimeRootLock(ctx, s.cfg.RootDir, func() error {
		refs, err := s.vmRuntimeUpdateRefs(catalog)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			artifact, err := deps.ensureRuntime(ctx, ref)
			if err != nil {
				return fmt.Errorf("cache VM runtime %s: %w", ref.RuntimeID, err)
			}
			artifacts = append(artifacts, artifact)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", artifact.ID, artifact.ManifestSHA256); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) vmRuntimeUpdateRefs(catalog vmRuntimeCatalog) ([]vmRuntimeCatalogRef, error) {
	stable, ok := catalog.RuntimeForChannel("amd64", cli.VMRuntimeChannelStable)
	if !ok {
		return nil, fmt.Errorf("VM runtime catalog has no promoted stable runtime")
	}
	byKey := map[string]vmRuntimeCatalogRef{vmRuntimeCatalogKey(stable.RuntimeID, stable.ManifestSHA): stable}
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	for serviceName, service := range dv.AsStruct().Services {
		if err := addVMRuntimeUpdateRefs(byKey, catalog, serviceName, service); err != nil {
			return nil, err
		}
	}
	refs := make([]vmRuntimeCatalogRef, 0, len(byKey))
	for _, ref := range byKey {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(left, right vmRuntimeCatalogRef) int {
		if byID := strings.Compare(left.RuntimeID, right.RuntimeID); byID != 0 {
			return byID
		}
		return strings.Compare(left.ManifestSHA, right.ManifestSHA)
	})
	return refs, nil
}

func addVMRuntimeUpdateRefs(byKey map[string]vmRuntimeCatalogRef, catalog vmRuntimeCatalog, serviceName string, service *db.Service) error {
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return nil
	}
	for _, artifact := range vmRuntimeLifecycleArtifacts(service.VM.Components.Runtime) {
		// Only exact catalog-owned artifacts can be refreshed from the catalog.
		// Adopted legacy and locally imported identities stay untouched.
		if strings.TrimSpace(artifact.Source) != "official" {
			continue
		}
		ref, ok := catalog.RuntimeByID("amd64", artifact.ID)
		if !ok || ref.ManifestSHA != artifact.ManifestSHA256 {
			return fmt.Errorf("configured VM runtime %s for %s is absent from the trusted catalog", artifact.ID, serviceName)
		}
		byKey[vmRuntimeCatalogKey(ref.RuntimeID, ref.ManifestSHA)] = ref
	}
	return nil
}

func vmRuntimeLifecycleArtifacts(runtime db.VMRuntimeLifecycleConfig) []db.VMRuntimeArtifactConfig {
	artifacts := make([]db.VMRuntimeArtifactConfig, 0, 3)
	if strings.TrimSpace(runtime.Configured.ID) != "" {
		artifacts = append(artifacts, runtime.Configured)
	}
	if runtime.Staged != nil && strings.TrimSpace(runtime.Staged.ID) != "" {
		artifacts = append(artifacts, *runtime.Staged)
	}
	if runtime.Previous != nil && strings.TrimSpace(runtime.Previous.ID) != "" {
		artifacts = append(artifacts, *runtime.Previous)
	}
	return artifacts
}

func (s *Server) importVMRuntime(ctx context.Context, w io.Writer, name string, payload io.Reader) error {
	deps := s.runtimeCommandDependencies()
	var artifact db.VMRuntimeArtifactConfig
	if err := WithVMRuntimeRootLock(ctx, s.cfg.RootDir, func() error {
		var err error
		artifact, err = deps.importRuntime(ctx, filepath.Join(s.cfg.RootDir, "vm-runtimes"), name, payload)
		return err
	}); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%s\t%s\t%s\n", artifact.ID, artifact.ManifestSHA256, artifact.Source)
	return err
}

func (s *Server) upgradeVMRuntime(ctx context.Context, w io.Writer, serviceName, target, channel string) error {
	resolved, err := s.resolveVMRuntimeUpgradeTarget(ctx, serviceName, target, channel)
	if err != nil {
		return err
	}
	result, err := s.runtimeCommandDependencies().stageRuntime(ctx, &s.cfg, serviceName, resolved)
	if err != nil {
		return err
	}
	state := "staged-for-next-start"
	if result.RunningUnchanged {
		state = "staged-running-unchanged"
	}
	_, err = fmt.Fprintf(w, "%s\t%s\t%s\n", result.Service, result.Staged.ID, state)
	return err
}

func (s *Server) printVMRuntimePolicyDefaults(w io.Writer) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	var host *db.VMHostConfig
	if dv.VMHost().Valid() {
		host = dv.VMHost().AsStruct()
	}
	policy, err := effectiveVMRuntimePolicyFor(host, nil)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "POLICY\tCHANNEL\n%s\t%s\n", policy.Mode, policy.Channel)
	return err
}

func (s *Server) setVMRuntimePolicyDefaults(ctx context.Context, w io.Writer, policyName, channel string) error {
	policyName, err := normalizeVMRuntimePolicyMode(policyName, false)
	if err != nil {
		return err
	}
	channel, err = normalizeStoredVMRuntimeChannel(channel, true)
	if err != nil {
		return err
	}
	updated, err := s.setVMRuntimePolicyDefaultsAndReconcile(ctx, policyName, channel)
	if err != nil {
		return err
	}
	effective, err := effectiveVMRuntimePolicyFor(updated.VMHost, nil)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\t%s\n", effective.Mode, effective.Channel)
	return err
}

func (s *Server) setVMRuntimePolicy(ctx context.Context, w io.Writer, serviceName, policyName, channel string) error {
	policyName, err := normalizeVMRuntimePolicyMode(policyName, true)
	if err != nil {
		return err
	}
	channel, err = normalizeStoredVMRuntimeChannel(channel, true)
	if err != nil {
		return err
	}
	updated, err := s.setVMRuntimePolicyAndReconcile(ctx, serviceName, policyName, channel)
	if err != nil {
		return err
	}
	service := updated.Services[serviceName]
	effective, err := effectiveVMRuntimePolicyFor(updated.VMHost, &service.VM.Components.Runtime)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\t%s\t%s\n", serviceName, effective.Mode, effective.Channel)
	return err
}

func (s *Server) setVMRuntimeProtection(ctx context.Context, w io.Writer, runtimeID string, protect bool) error {
	runtimeID = strings.TrimSpace(runtimeID)
	if _, err := vmRuntimeVersionFromID(runtimeID); err != nil {
		return err
	}
	if err := WithVMRuntimeRootLock(ctx, s.cfg.RootDir, func() error {
		_, err := s.cfg.DB.MutateData(func(data *db.Data) error {
			if data.VMHost == nil {
				data.VMHost = &db.VMHostConfig{}
			}
			protected := append([]string(nil), data.VMHost.ProtectedRuntimeIDs...)
			if protect {
				protected = append(protected, runtimeID)
			} else {
				protected = slices.DeleteFunc(protected, func(id string) bool { return id == runtimeID })
			}
			slices.Sort(protected)
			data.VMHost.ProtectedRuntimeIDs = slices.Compact(protected)
			return nil
		})
		return err
	}); err != nil {
		return err
	}
	action := "unprotected"
	if protect {
		action = "protected"
	}
	_, err := fmt.Fprintf(w, "%s\t%s\n", runtimeID, action)
	return err
}
