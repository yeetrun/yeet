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
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmRuntimeTrialConsumerDeps struct {
	control      vmRuntimeControlFileDeps
	coordinator  vmRuntimeAdoptionCoordinatorDeps
	unitState    func(context.Context, string) (vmRuntimeUnitState, error)
	processAlive func(int) bool
	jailerState  func(string, uint32, uint32) (vmJailerReadiness, error)
	interval     time.Duration
}

func (s *Server) runtimeTrialConsumerDependencies() vmRuntimeTrialConsumerDeps {
	deps := vmRuntimeTrialConsumerDeps{
		control:     defaultVMRuntimeControlFileDeps(),
		coordinator: defaultVMRuntimeAdoptionCoordinatorDeps(),
		unitState: func(ctx context.Context, service string) (vmRuntimeUnitState, error) {
			return readVMRuntimeUnitState(ctx, vmSystemdUnitName(service))
		},
		processAlive: vmRuntimeProcessAlive,
		jailerState:  vmJailerReadinessForRootWithOwner,
		interval:     2 * time.Second,
	}
	if s.vmRuntimeTrialDeps == nil {
		return deps
	}
	override := *s.vmRuntimeTrialDeps
	override.control = completeVMRuntimeControlFileDeps(override.control)
	override.coordinator = completeVMRuntimeAdoptionCoordinatorDeps(override.coordinator)
	deps.control = override.control
	deps.coordinator = override.coordinator
	if override.unitState != nil {
		deps.unitState = override.unitState
	}
	if override.processAlive != nil {
		deps.processAlive = override.processAlive
	}
	if override.jailerState != nil {
		deps.jailerState = override.jailerState
	}
	if override.interval > 0 {
		deps.interval = override.interval
	}
	return deps
}

func (s *Server) consumeVMRuntimeTrialResults(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	services := adoptedVMRuntimeServices(dv.AsStruct())
	deps := s.runtimeTrialConsumerDependencies()
	var result error
	for _, service := range services {
		if _, err := consumeVMRuntimeTrialResult(ctx, &s.cfg, service.Name, deps); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("consume VM runtime trial result for %s: %w", service.Name, err))
		}
		if trial := service.VM.Components.Runtime.Trial; trial != nil && trial.State == string(vmRuntimeTrialHealthy) && trial.RecoveryPoint != "" {
			if err := s.reconcileHealthyVMRuntimeRecoveryPoint(ctx, service.Name, s.runtimeRestartDependencies().unprotect); err != nil {
				result = errors.Join(result, fmt.Errorf("reconcile VM runtime recovery point for %s: %w", service.Name, err))
			}
		}
	}
	return result
}

func (s *Server) reconcileHealthyVMRuntimeRecoveryPoint(ctx context.Context, serviceName string, unprotect func(context.Context, string) error) error {
	if unprotect == nil {
		return fmt.Errorf("VM runtime recovery-point unprotect dependency is required")
	}
	return WithVMRuntimeTransactionLock(ctx, &s.cfg, func() error {
		return s.reconcileHealthyVMRuntimeRecoveryPointLocked(ctx, serviceName, unprotect)
	})
}

func (s *Server) reconcileHealthyVMRuntimeRecoveryPointLocked(ctx context.Context, serviceName string, unprotect func(context.Context, string) error) error {
	view, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	service := view.AsStruct().Services[serviceName]
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return nil
	}
	trial := service.VM.Components.Runtime.Trial
	if trial == nil || trial.State != string(vmRuntimeTrialHealthy) || trial.RecoveryPoint == "" {
		return nil
	}
	recoveryPoint := trial.RecoveryPoint
	if err := unprotect(ctx, recoveryPoint); err != nil {
		return err
	}
	_, _, err = s.cfg.DB.MutateService(serviceName, func(_ *db.Data, current *db.Service) error {
		return clearHealthyVMRuntimeRecoveryPoint(current, recoveryPoint)
	})
	return err
}

func clearHealthyVMRuntimeRecoveryPoint(service *db.Service, recoveryPoint string) error {
	trial := service.VM.Components.Runtime.Trial
	if trial == nil || trial.State != string(vmRuntimeTrialHealthy) || trial.RecoveryPoint != recoveryPoint {
		return fmt.Errorf("VM runtime recovery-point state changed during reconciliation")
	}
	trial.RecoveryPoint = ""
	return nil
}

func (s *Server) runVMRuntimeTrialWatcher(ctx context.Context) {
	deps := s.runtimeTrialConsumerDependencies()
	ticker := time.NewTicker(deps.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logRuntimeReconcileError("VM runtime trial-result reconciliation failed", s.consumeVMRuntimeTrialResults(ctx))
		}
	}
}

func consumeVMRuntimeTrialResult(
	ctx context.Context,
	cfg *Config,
	serviceName string,
	deps vmRuntimeTrialConsumerDeps,
) (_ vmRuntimeTrialOutcome, retErr error) {
	effectiveCfg, deps, resultPath, result, resultID, err := prepareVMRuntimeTrialConsumption(cfg, serviceName, deps)
	if err != nil {
		return "", err
	}

	journal, err := openVMRuntimeJournalStore(ctx, effectiveCfg.RootDir, deps.coordinator.journal)
	if err != nil {
		return "", err
	}
	defer func() { retErr = errors.Join(retErr, journal.Close()) }()
	if err := recoverVMRuntimeAdoptionsWithStore(ctx, effectiveCfg, journal, deps.coordinator); err != nil {
		return "", fmt.Errorf("recover VM runtime transactions before consuming trial: %w", err)
	}
	if err := journal.CleanupCommittedTombstones(); err != nil {
		return "", err
	}

	data, service, err := loadCurrentVMRuntimeTrialService(effectiveCfg, serviceName)
	if err != nil {
		return "", err
	}
	if result.Outcome == vmRuntimeTrialFailedNoFallback {
		return consumeTerminalVMRuntimeTrialResult(effectiveCfg, service, resultPath, resultID, result, deps.control)
	}
	if alreadyConsumedVMRuntimeTrial(service, result) {
		return result.Outcome, removeVMRuntimeTrialResult(resultPath, resultID, deps.control)
	}
	return commitVMRuntimeTrialResult(ctx, effectiveCfg, journal, data, service, resultPath, resultID, result, deps)
}

func prepareVMRuntimeTrialConsumption(cfg *Config, serviceName string, deps vmRuntimeTrialConsumerDeps) (*Config, vmRuntimeTrialConsumerDeps, string, vmRuntimeTrialResult, vmJailerFileIdentity, error) {
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return nil, deps, "", vmRuntimeTrialResult{}, vmJailerFileIdentity{}, err
	}
	deps.control = completeVMRuntimeControlFileDeps(deps.control)
	deps.coordinator = completeVMRuntimeAdoptionCoordinatorDeps(deps.coordinator)
	data, service, err := loadCurrentVMRuntimeTrialService(effectiveCfg, serviceName)
	if err != nil {
		return nil, deps, "", vmRuntimeTrialResult{}, vmJailerFileIdentity{}, err
	}
	_ = data
	root := serviceRootFromConfig(*effectiveCfg, *service)
	resultPath := filepath.Join(serviceRunDirForRoot(root), vmRuntimeTrialResultFileName)
	result, resultID, err := readTrustedVMRuntimeTrialResult(resultPath, serviceName, deps.control)
	return effectiveCfg, deps, resultPath, result, resultID, err
}

func loadCurrentVMRuntimeTrialService(cfg *Config, serviceName string) (*db.Data, *db.Service, error) {
	view, err := cfg.DB.Get()
	if err != nil {
		return nil, nil, err
	}
	data := view.AsStruct()
	service := data.Services[serviceName]
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return nil, nil, fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	return data, service, nil
}

func consumeTerminalVMRuntimeTrialResult(cfg *Config, service *db.Service, resultPath string, resultID vmJailerFileIdentity, result vmRuntimeTrialResult, control vmRuntimeControlFileDeps) (vmRuntimeTrialOutcome, error) {
	currentResult, currentID, err := readTrustedVMRuntimeTrialResult(resultPath, service.Name, control)
	if err != nil || currentID != resultID || !equalVMRuntimeTrialResult(currentResult, result) {
		return "", errors.Join(err, fmt.Errorf("VM runtime terminal trial result changed during reconciliation"))
	}
	current, err := terminalVMRuntimeTrialMatchesCurrentGeneration(cfg, service, result, control)
	if err != nil {
		return "", err
	}
	if !current {
		if err := removeVMRuntimeTrialResult(resultPath, resultID, control); err != nil {
			return "", fmt.Errorf("remove stale VM runtime terminal trial result: %w", err)
		}
		return "", os.ErrNotExist
	}
	// There is intentionally no live marker to authorize a selection change.
	// Keep the current terminal result for status and operator diagnosis.
	return result.Outcome, nil
}

func commitVMRuntimeTrialResult(ctx context.Context, cfg *Config, journal *vmRuntimeJournalStore, data *db.Data, service *db.Service, resultPath string, resultID vmJailerFileIdentity, result vmRuntimeTrialResult, deps vmRuntimeTrialConsumerDeps) (_ vmRuntimeTrialOutcome, retErr error) {
	marker, err := validateVMRuntimeTrialForCommit(ctx, cfg, service, result, deps)
	if err != nil {
		return "", err
	}
	desired := desiredVMRuntimeServiceAfterTrial(service, result)
	tx, err := prepareVMRuntimeTrialCommitWithStore(ctx, cfg, journal, deps.coordinator, data, service, desired, func() error {
		currentResult, currentID, err := readTrustedVMRuntimeTrialResult(resultPath, service.Name, deps.control)
		if err != nil || currentID != resultID || !equalVMRuntimeTrialResult(currentResult, result) {
			return errors.Join(err, fmt.Errorf("VM runtime trial result changed before commit"))
		}
		_, err = validateVMRuntimeTrialForCommit(ctx, cfg, service, result, deps)
		return err
	})
	if err != nil {
		return "", err
	}
	tx.journal = journal
	defer func() { retErr = errors.Join(retErr, tx.Close()) }()
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if !vmRuntimeMarkerMatchesArtifact(marker, desired.VM.Components.Runtime.Configured) {
		return "", fmt.Errorf("committed VM runtime does not match the live marker")
	}
	if err := removeVMRuntimeTrialResult(resultPath, resultID, deps.control); err != nil {
		return result.Outcome, fmt.Errorf("remove consumed VM runtime trial result: %w", err)
	}
	return result.Outcome, nil
}

func terminalVMRuntimeTrialMatchesCurrentGeneration(
	cfg *Config,
	service *db.Service,
	result vmRuntimeTrialResult,
	control vmRuntimeControlFileDeps,
) (bool, error) {
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return false, fmt.Errorf("adopted VM runtime state is required")
	}
	runtimeState := service.VM.Components.Runtime
	if !vmRuntimeTrialResultMatchesSelections(result, runtimeState) {
		return false, nil
	}
	root := serviceRootFromConfig(*cfg, *service)
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(
		filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName), service.Name, control.uid, control.gid,
	)
	if err != nil {
		return false, err
	}
	wantDescriptor, err := vmRuntimeDescriptorFromService(service)
	if err != nil {
		return false, err
	}
	if snapshot.SHA256 != result.DescriptorSHA256 || !equalVMRuntimeDescriptors(snapshot.Descriptor, wantDescriptor) {
		return false, fmt.Errorf("terminal VM runtime trial descriptor generation does not match current database state")
	}
	return true, nil
}

func alreadyConsumedVMRuntimeTrial(service *db.Service, result vmRuntimeTrialResult) bool {
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return false
	}
	runtimeState := service.VM.Components.Runtime
	if !consumedVMRuntimeTrialMetadataMatches(runtimeState, result) {
		return false
	}
	switch result.Outcome {
	case vmRuntimeTrialHealthy:
		return result.Candidate.matches(runtimeState.Configured) && runtimeState.Previous != nil && result.Configured.matches(*runtimeState.Previous)
	case vmRuntimeTrialFailedRolledBack:
		return result.Configured.matches(runtimeState.Configured)
	default:
		return false
	}
}

func consumedVMRuntimeTrialMetadataMatches(runtimeState db.VMRuntimeLifecycleConfig, result vmRuntimeTrialResult) bool {
	return runtimeState.Staged == nil && runtimeState.Trial != nil && runtimeState.Trial.State == string(result.Outcome) &&
		runtimeState.Trial.CandidateID == result.Candidate.ID && runtimeState.Trial.PreviousID == result.Configured.ID
}

func validateVMRuntimeTrialForCommit(
	ctx context.Context,
	cfg *Config,
	service *db.Service,
	result vmRuntimeTrialResult,
	deps vmRuntimeTrialConsumerDeps,
) (vmRuntimeRunningMarker, error) {
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return vmRuntimeRunningMarker{}, fmt.Errorf("adopted VM runtime state is required")
	}
	runtimeState := service.VM.Components.Runtime
	if !vmRuntimeTrialResultMatchesSelections(result, runtimeState) {
		return vmRuntimeRunningMarker{}, fmt.Errorf("VM runtime trial identities do not match current staged and configured state")
	}
	root := serviceRootFromConfig(*cfg, *service)
	if err := validateVMRuntimeTrialDescriptorGeneration(root, service, result, deps.control); err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	markerPath := filepath.Join(serviceRunDirForRoot(root), vmRuntimeRunningMarkerFileName)
	marker, err := readTrustedVMRuntimeRunningMarker(markerPath, service.Name, deps.control.uid, deps.control.gid)
	if err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	if !vmRuntimeTrialMarkerMatchesLease(marker, result) {
		return vmRuntimeRunningMarker{}, fmt.Errorf("VM runtime trial marker does not match the result lease")
	}
	if err := validateVMRuntimeTrialRunningIdentity(runtimeState, result, marker); err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	if err := validateActiveVMRuntimeTrialProcesses(ctx, service.Name, root, result, deps); err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	return marker, nil
}

func validateVMRuntimeTrialRunningIdentity(runtimeState db.VMRuntimeLifecycleConfig, result vmRuntimeTrialResult, marker vmRuntimeRunningMarker) error {
	wantRunning := expectedVMRuntimeTrialRunningArtifact(runtimeState, result.Outcome)
	if !result.Running.matches(wantRunning) || !vmRuntimeMarkerMatchesArtifact(marker, wantRunning) {
		return fmt.Errorf("VM runtime trial running identity does not match its outcome")
	}
	return nil
}

func validateVMRuntimeTrialDescriptorGeneration(root string, service *db.Service, result vmRuntimeTrialResult, control vmRuntimeControlFileDeps) error {
	descriptorPath := filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName)
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(descriptorPath, service.Name, control.uid, control.gid)
	if err != nil {
		return err
	}
	wantDescriptor, err := vmRuntimeDescriptorFromService(service)
	if err != nil || snapshot.SHA256 != result.DescriptorSHA256 || !equalVMRuntimeDescriptors(snapshot.Descriptor, wantDescriptor) {
		return errors.Join(err, fmt.Errorf("VM runtime trial descriptor generation does not match current database state"))
	}
	return nil
}

func vmRuntimeTrialMarkerMatchesLease(marker vmRuntimeRunningMarker, result vmRuntimeTrialResult) bool {
	return marker.DescriptorSHA256 == result.DescriptorSHA256 && marker.LaunchID == result.LaunchID &&
		marker.RunnerPID == result.RunnerPID && marker.ChildPID == result.ChildPID && result.Running != nil
}

func expectedVMRuntimeTrialRunningArtifact(runtimeState db.VMRuntimeLifecycleConfig, outcome vmRuntimeTrialOutcome) db.VMRuntimeArtifactConfig {
	if outcome == vmRuntimeTrialFailedRolledBack {
		return runtimeState.Configured
	}
	return *runtimeState.Staged
}

func validateActiveVMRuntimeTrialProcesses(ctx context.Context, service, root string, result vmRuntimeTrialResult, deps vmRuntimeTrialConsumerDeps) error {
	unit, err := deps.unitState(ctx, service)
	if err != nil {
		return err
	}
	if unit.ActiveState != "active" || unit.MainPID != result.RunnerPID || !deps.processAlive(result.RunnerPID) || !deps.processAlive(result.ChildPID) {
		return fmt.Errorf("VM runtime trial processes do not match the active unit")
	}
	readiness, err := deps.jailerState(root, deps.control.uid, deps.control.gid)
	if err != nil || readiness != vmJailerReady {
		return errors.Join(err, fmt.Errorf("VM runtime trial does not have active jailer isolation"))
	}
	return nil
}

func desiredVMRuntimeServiceAfterTrial(service *db.Service, result vmRuntimeTrialResult) *db.Service {
	desired := service.Clone()
	runtimeState := &desired.VM.Components.Runtime
	configuredBefore := runtimeState.Configured
	if result.Outcome == vmRuntimeTrialHealthy {
		runtimeState.Previous = cloneVMRuntimeArtifact(&configuredBefore)
		runtimeState.Configured = *runtimeState.Staged
	}
	runtimeState.Staged = nil
	recoveryPoint := ""
	if runtimeState.Trial != nil {
		recoveryPoint = runtimeState.Trial.RecoveryPoint
	}
	runtimeState.Trial = &db.VMRuntimeTrialConfig{
		State: string(result.Outcome), CandidateID: result.Candidate.ID,
		PreviousID: configuredBefore.ID, RecoveryPoint: recoveryPoint,
		StartedAt: result.StartedAt, LastError: result.Error,
	}
	return desired
}

func equalVMRuntimeTrialResult(left, right vmRuntimeTrialResult) bool {
	return equalVMRuntimeTrialResultIdentity(left, right) && equalVMRuntimeTrialResultOutcome(left, right) && equalVMRuntimeTrialResultTiming(left, right)
}

func equalVMRuntimeTrialResultIdentity(left, right vmRuntimeTrialResult) bool {
	return left.SchemaVersion == right.SchemaVersion && left.Service == right.Service &&
		left.DescriptorSHA256 == right.DescriptorSHA256 && left.LaunchID == right.LaunchID &&
		left.Candidate == right.Candidate && left.Configured == right.Configured &&
		equalVMRuntimeTrialIdentityPointers(left.Running, right.Running)
}

func equalVMRuntimeTrialResultOutcome(left, right vmRuntimeTrialResult) bool {
	return left.Outcome == right.Outcome && left.RunnerPID == right.RunnerPID && left.ChildPID == right.ChildPID && left.Error == right.Error
}

func equalVMRuntimeTrialResultTiming(left, right vmRuntimeTrialResult) bool {
	return left.StartedAt == right.StartedAt && left.CompletedAt == right.CompletedAt
}

func equalVMRuntimeTrialIdentityPointers(left, right *vmRuntimeTrialArtifactIdentity) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func prepareVMRuntimeTrialCommitWithStore(
	ctx context.Context,
	cfg *Config,
	journal *vmRuntimeJournalStore,
	deps vmRuntimeAdoptionCoordinatorDeps,
	data *db.Data,
	service, desired *db.Service,
	verifyLive func() error,
) (_ *VMRuntimeAdoption, retErr error) {
	transactionID, preparation, record, err := prepareVMRuntimeTrialJournalRecord(ctx, cfg, service, desired, deps)
	if err != nil {
		return nil, err
	}
	descriptors, err := prepareVMRuntimeAdoptionDescriptorCohort(ctx, []vmRuntimeJournalRecord{record}, deps.descriptor)
	if err != nil {
		return nil, err
	}
	completed := false
	var units *vmRuntimeJournalUnitReconciler
	defer cleanupFailedVMRuntimeAdoptionCohort(&retErr, descriptors, &units, &completed)
	units, err = prepareVMRuntimeJournalUnitReconciler(ctx, []vmRuntimeJournalRecord{record}, deps.unit)
	if err != nil {
		return nil, err
	}
	tx := &VMRuntimeAdoption{
		cfg: cfg, deps: deps, journal: journal,
		records: []vmRuntimeJournalRecord{record}, preparations: []vmRuntimeAdoptionPreparation{preparation},
		descriptors: descriptors, units: units, transactionID: transactionID,
	}
	validate := func(current *db.Data) error {
		return validateVMRuntimeTrialCommitPrecondition(ctx, tx, current, record, preparation, verifyLive)
	}
	tx.beforeDerived = func() error {
		return cfg.DB.WithLatestDataLocked(func(view db.DataView) error { return validate(view.AsStruct()) })
	}
	tx.revalidateEvidence = func(preparation vmRuntimeAdoptionPreparation, skipped map[string]struct{}) error {
		return revalidateVMRuntimeTransitionEvidence(preparation, skipped, deps.inventory.evidence)
	}
	if err := tx.descriptors.verify(ctx, vmRuntimeDescriptorRawOld); err != nil {
		return nil, err
	}
	if err := tx.units.VerifyOld(ctx); err != nil {
		return nil, err
	}
	if err := validate(data); err != nil {
		return nil, err
	}
	if err := journal.Prepare(tx.records); err != nil {
		return nil, tx.handlePrepareFailure(err)
	}
	if err := runVMRuntimeAdoptionTransitionHook(deps, "prepared"); err != nil {
		return nil, tx.finishPreDatabaseFailure(err)
	}
	completed = true
	return tx, nil
}

func prepareVMRuntimeTrialJournalRecord(ctx context.Context, cfg *Config, service, desired *db.Service, deps vmRuntimeAdoptionCoordinatorDeps) (string, vmRuntimeAdoptionPreparation, vmRuntimeJournalRecord, error) {
	if service.VM.Components.Runtime.Staged == nil {
		return "", vmRuntimeAdoptionPreparation{}, vmRuntimeJournalRecord{}, fmt.Errorf("VM runtime trial commit requires a staged artifact")
	}
	preparation, _, err := prepareVMRuntimeTransitionState(ctx, cfg, service, *service.VM.Components.Runtime.Staged, nil, deps.inventory)
	if err != nil {
		return "", vmRuntimeAdoptionPreparation{}, vmRuntimeJournalRecord{}, err
	}
	preparation.NewDB = vmRuntimeJournalDBProjection{Components: desired.VM.Components.Clone(), ImageKernel: desired.VM.Image.Kernel}
	preparation.Components = desired.VM.Components.Clone()
	transactionID, err := newVMRuntimeAdoptionTransactionID(deps.random)
	if err != nil {
		return "", vmRuntimeAdoptionPreparation{}, vmRuntimeJournalRecord{}, err
	}
	record, err := buildVMRuntimeTransitionJournalRecord(cfg, preparation, desired, transactionID, deps.now().UTC(), deps)
	return transactionID, preparation, record, err
}

func validateVMRuntimeTrialCommitPrecondition(ctx context.Context, tx *VMRuntimeAdoption, current *db.Data, record vmRuntimeJournalRecord, preparation vmRuntimeAdoptionPreparation, verifyLive func() error) error {
	stored, err := validateVMRuntimeAdoptionOldProjection(current, record)
	if err != nil {
		return err
	}
	digest, err := vmRuntimeAdoptionPreconditionDigest(*tx.cfg, *stored, preparation)
	if err != nil || digest != record.PreconditionSHA256 {
		return errors.Join(err, fmt.Errorf("VM runtime trial precondition changed for %s", record.Service))
	}
	if err := tx.revalidatePreparedEvidence(preparation, nil); err != nil {
		return err
	}
	if _, err := loadAndRevalidateVMRuntimeAdoptionUnit(ctx, preparation, true, tx.deps.inventory); err != nil {
		return err
	}
	return verifyLive()
}
