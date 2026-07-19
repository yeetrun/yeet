// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

type vmRuntimeTransitionResult struct {
	Service          string
	RunningUnchanged bool
	Configured       db.VMRuntimeArtifactConfig
	Staged           *db.VMRuntimeArtifactConfig
	Previous         *db.VMRuntimeArtifactConfig
}

type vmRuntimeNamedTarget struct {
	Service string
	Target  vmRuntimeResolvedTarget
}

// stageVMRuntime publishes an immutable candidate as Staged without changing
// the running process or the configured rollback chain. The existing runtime
// adoption journal owns the descriptor/unit/database commit and crash recovery.
func stageVMRuntime(ctx context.Context, cfg *Config, service string, target vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
	return stageVMRuntimeWithDeps(ctx, cfg, service, target, defaultVMRuntimeAdoptionCoordinatorDeps())
}

func stageVMRuntimeWithDeps(
	ctx context.Context,
	cfg *Config,
	service string,
	target vmRuntimeResolvedTarget,
	deps vmRuntimeAdoptionCoordinatorDeps,
) (_ vmRuntimeTransitionResult, retErr error) {
	effectiveCfg, service, deps, err := prepareVMRuntimeStageRequest(cfg, service, target, deps)
	if err != nil {
		return vmRuntimeTransitionResult{}, err
	}
	journal, err := openVMRuntimeJournalStore(ctx, effectiveCfg.RootDir, deps.journal)
	if err != nil {
		return vmRuntimeTransitionResult{}, err
	}
	defer func() { retErr = errors.Join(retErr, journal.Close()) }()
	if err := recoverVMRuntimeAdoptionsWithStore(ctx, effectiveCfg, journal, deps); err != nil {
		return vmRuntimeTransitionResult{}, fmt.Errorf("recover VM runtime transactions before staging: %w", err)
	}
	if err := journal.CleanupCommittedTombstones(); err != nil {
		return vmRuntimeTransitionResult{}, fmt.Errorf("clean committed VM runtime journal tombstones: %w", err)
	}
	return stageVMRuntimeWithJournal(ctx, effectiveCfg, service, target, deps, journal)
}

func prepareVMRuntimeStageRequest(cfg *Config, service string, target vmRuntimeResolvedTarget, deps vmRuntimeAdoptionCoordinatorDeps) (*Config, string, vmRuntimeAdoptionCoordinatorDeps, error) {
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return nil, "", deps, err
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, "", deps, fmt.Errorf("VM service is required")
	}
	if err := validateVMRuntimeArtifact(target.Artifact, "selected"); err != nil {
		return nil, "", deps, err
	}
	return effectiveCfg, service, completeVMRuntimeAdoptionCoordinatorDeps(deps), nil
}

func stageVMRuntimeWithJournal(ctx context.Context, cfg *Config, service string, target vmRuntimeResolvedTarget, deps vmRuntimeAdoptionCoordinatorDeps, journal *vmRuntimeJournalStore) (_ vmRuntimeTransitionResult, retErr error) {
	view, err := cfg.DB.Get()
	if err != nil {
		return vmRuntimeTransitionResult{}, fmt.Errorf("read VM runtime state: %w", err)
	}
	data := view.AsStruct()
	stored, err := validateVMRuntimeTransitionTarget(ctx, cfg, data, service, target)
	if err != nil {
		return vmRuntimeTransitionResult{}, err
	}
	runtimeState := stored.VM.Components.Runtime
	result := vmRuntimeTransitionResult{
		Service: service, Configured: runtimeState.Configured,
		Staged: cloneVMRuntimeArtifact(runtimeState.Staged), Previous: cloneVMRuntimeArtifact(runtimeState.Previous),
	}
	done, terminalReplacement, err := prepareVMRuntimeStageCandidate(ctx, cfg, stored, target, deps, &result)
	if err != nil || done {
		return result, err
	}

	tx, err := prepareVMRuntimeTransitionWithStore(ctx, cfg, journal, deps, data, stored, target)
	if err != nil {
		return vmRuntimeTransitionResult{}, err
	}
	// The transaction now owns the journal and derived locks, while this
	// function retains the root journal flock through journal.Close above.
	tx.journal = journal
	defer func() { retErr = errors.Join(retErr, tx.Close()) }()
	if err := tx.Commit(); err != nil {
		return vmRuntimeTransitionResult{}, err
	}

	result.Staged = cloneVMRuntimeArtifact(&target.Artifact)
	result.RunningUnchanged = tx.preparations[0].EffectiveUnit.ActiveState == "active"
	if terminalReplacement != nil {
		if err := removeVMRuntimeTrialResult(terminalReplacement.path, terminalReplacement.id, terminalReplacement.control); err != nil {
			return result, fmt.Errorf("runtime %s was staged, but the prior terminal trial result could not be removed: %w", target.Artifact.ID, err)
		}
	}
	return result, nil
}

func prepareVMRuntimeStageCandidate(ctx context.Context, cfg *Config, stored *db.Service, target vmRuntimeResolvedTarget, deps vmRuntimeAdoptionCoordinatorDeps, result *vmRuntimeTransitionResult) (bool, *vmRuntimeTerminalReplacement, error) {
	runtimeState := stored.VM.Components.Runtime
	if _, err := collectVMRuntimeTransitionArtifactEvidence(target.Artifact, deps.inventory.evidence); err != nil {
		return false, nil, err
	}
	if runtimeState.Staged != nil && *runtimeState.Staged == target.Artifact && vmRuntimePolicyMutationMatches(runtimeState, target.PolicyMutation) {
		var err error
		result.RunningUnchanged, err = validateExistingVMRuntimeStage(ctx, cfg, stored, deps)
		return true, nil, err
	}
	var terminalReplacement *vmRuntimeTerminalReplacement
	if pendingVMRuntimeTrial(runtimeState) {
		var err error
		terminalReplacement, err = validateTerminalVMRuntimeTrialReplacement(cfg, stored, deps)
		if err != nil {
			return false, nil, errors.Join(fmt.Errorf("VM %s already has a pending runtime trial", stored.Name), err)
		}
	} else if runtimeState.Staged != nil {
		if err := refuseActiveVMRuntimeTrialReplacement(cfg, stored, deps); err != nil {
			return false, nil, err
		}
	}
	if runtimeState.Configured == target.Artifact {
		return false, nil, fmt.Errorf("VM %s already has runtime %s configured", stored.Name, target.Artifact.ID)
	}
	return false, terminalReplacement, nil
}

// stageVMRuntimePolicyCohort commits one host-default policy change and every
// VM selection implied by it as one durable journal cohort. It never changes a
// running process.
func stageVMRuntimePolicyCohort(
	ctx context.Context,
	cfg *Config,
	oldHost, newHost *db.VMHostConfig,
	fleet []vmRuntimePolicyFleetPrecondition,
	targets []vmRuntimeNamedTarget,
	deps vmRuntimeAdoptionCoordinatorDeps,
) (retErr error) {
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return err
	}
	deps = completeVMRuntimeAdoptionCoordinatorDeps(deps)
	targets, err = normalizeVMRuntimePolicyTargets(targets)
	if err != nil {
		return err
	}

	journal, err := openVMRuntimePolicyCohortJournal(ctx, effectiveCfg, deps)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, journal.Close()) }()
	data, proposed, oldHostProjection, err := loadVMRuntimePolicyCohortData(effectiveCfg, oldHost, newHost)
	if err != nil {
		return err
	}

	transactionID, preparations, records, err := prepareVMRuntimePolicyCohortRecords(ctx, effectiveCfg, data, proposed, targets, oldHostProjection, newHost, deps)
	if err != nil {
		return err
	}

	descriptors, err := prepareVMRuntimeAdoptionDescriptorCohort(ctx, records, deps.descriptor)
	if err != nil {
		return err
	}
	completed := false
	var units *vmRuntimeJournalUnitReconciler
	defer cleanupFailedVMRuntimeAdoptionCohort(&retErr, descriptors, &units, &completed)
	units, err = prepareVMRuntimeJournalUnitReconciler(ctx, records, deps.unit)
	if err != nil {
		return err
	}
	tx := &VMRuntimeAdoption{
		cfg: effectiveCfg, deps: deps, journal: journal, records: records, preparations: preparations,
		descriptors: descriptors, units: units, transactionID: transactionID,
	}
	tx.revalidateEvidence = func(preparation vmRuntimeAdoptionPreparation, skipped map[string]struct{}) error {
		return revalidateVMRuntimeTransitionEvidence(preparation, skipped, deps.inventory.evidence)
	}
	tx.beforeDerived = func() error {
		return effectiveCfg.DB.WithLatestDataLocked(func(view db.DataView) error {
			return validateVMRuntimePolicyCohortAgainstData(ctx, tx, view.AsStruct(), newHost, fleet, targets)
		})
	}
	tx.validateLatest = func(data *db.Data) error {
		return validateVMRuntimePolicyFleetPrecondition(data, fleet)
	}
	if err := prepareVMRuntimePolicyCohortForCommit(ctx, tx, data, newHost, fleet, targets); err != nil {
		return err
	}
	completed = true
	tx.journal = journal
	defer func() { retErr = errors.Join(retErr, tx.Close()) }()
	return tx.Commit()
}

func openVMRuntimePolicyCohortJournal(ctx context.Context, cfg *Config, deps vmRuntimeAdoptionCoordinatorDeps) (*vmRuntimeJournalStore, error) {
	journal, err := openVMRuntimeJournalStore(ctx, cfg.RootDir, deps.journal)
	if err != nil {
		return nil, err
	}
	if err := recoverVMRuntimeAdoptionsWithStore(ctx, cfg, journal, deps); err != nil {
		return nil, errors.Join(fmt.Errorf("recover VM runtime transactions before policy staging: %w", err), journal.Close())
	}
	if err := journal.CleanupCommittedTombstones(); err != nil {
		return nil, errors.Join(fmt.Errorf("clean committed VM runtime journal tombstones: %w", err), journal.Close())
	}
	return journal, nil
}

func loadVMRuntimePolicyCohortData(cfg *Config, oldHost, newHost *db.VMHostConfig) (*db.Data, *db.Data, vmRuntimeJournalVMHostProjection, error) {
	view, err := cfg.DB.Get()
	if err != nil {
		return nil, nil, vmRuntimeJournalVMHostProjection{}, fmt.Errorf("read VM runtime policy cohort state: %w", err)
	}
	data := view.AsStruct()
	oldProjection := vmRuntimeJournalVMHostProjectionFromConfig(oldHost)
	if vmRuntimeJournalVMHostProjectionFromConfig(data.VMHost) != oldProjection {
		return nil, nil, vmRuntimeJournalVMHostProjection{}, fmt.Errorf("host VM runtime policy changed while applying operator selection")
	}
	proposed := data.Clone()
	proposed.VMHost = newHost.Clone()
	return data, proposed, oldProjection, nil
}

func prepareVMRuntimePolicyCohortForCommit(ctx context.Context, tx *VMRuntimeAdoption, data *db.Data, newHost *db.VMHostConfig, fleet []vmRuntimePolicyFleetPrecondition, targets []vmRuntimeNamedTarget) error {
	if err := tx.descriptors.verify(ctx, vmRuntimeDescriptorRawOld); err != nil {
		return err
	}
	if err := tx.units.VerifyOld(ctx); err != nil {
		return err
	}
	if err := validateVMRuntimePolicyCohortAgainstData(ctx, tx, data, newHost, fleet, targets); err != nil {
		return fmt.Errorf("revalidate VM runtime policy cohort before journal publication: %w", err)
	}
	if err := tx.journal.Prepare(tx.records); err != nil {
		return tx.handlePrepareFailure(err)
	}
	if err := runVMRuntimeAdoptionTransitionHook(tx.deps, "prepared"); err != nil {
		return tx.finishPreDatabaseFailure(err)
	}
	return nil
}

func normalizeVMRuntimePolicyTargets(targets []vmRuntimeNamedTarget) ([]vmRuntimeNamedTarget, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("VM runtime policy cohort requires at least one target")
	}
	targets = append([]vmRuntimeNamedTarget(nil), targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].Service < targets[j].Service })
	for i := range targets {
		targets[i].Service = strings.TrimSpace(targets[i].Service)
		if targets[i].Service == "" || i > 0 && targets[i-1].Service == targets[i].Service {
			return nil, fmt.Errorf("VM runtime policy cohort has an empty or duplicate service")
		}
		if err := validateVMRuntimeArtifact(targets[i].Target.Artifact, "selected"); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func prepareVMRuntimePolicyCohortRecords(ctx context.Context, cfg *Config, data, proposed *db.Data, targets []vmRuntimeNamedTarget, oldHostProjection vmRuntimeJournalVMHostProjection, newHost *db.VMHostConfig, deps vmRuntimeAdoptionCoordinatorDeps) (string, []vmRuntimeAdoptionPreparation, []vmRuntimeJournalRecord, error) {
	transactionID, err := newVMRuntimeAdoptionTransactionID(deps.random)
	if err != nil {
		return "", nil, nil, err
	}
	preparedAt := deps.now().UTC()
	preparations := make([]vmRuntimeAdoptionPreparation, 0, len(targets))
	records := make([]vmRuntimeJournalRecord, 0, len(targets))
	for _, named := range targets {
		preparation, record, err := prepareVMRuntimePolicyCohortRecord(ctx, cfg, data, proposed, named, transactionID, preparedAt, deps)
		if err != nil {
			return "", nil, nil, err
		}
		preparations = append(preparations, preparation)
		records = append(records, record)
	}
	decorateVMRuntimePolicyCohortRecords(records, targets, oldHostProjection, vmRuntimeJournalVMHostProjectionFromConfig(newHost))
	return transactionID, preparations, records, nil
}

func prepareVMRuntimePolicyCohortRecord(ctx context.Context, cfg *Config, data, proposed *db.Data, named vmRuntimeNamedTarget, transactionID string, preparedAt time.Time, deps vmRuntimeAdoptionCoordinatorDeps) (vmRuntimeAdoptionPreparation, vmRuntimeJournalRecord, error) {
	if _, err := validateVMRuntimeTransitionTarget(ctx, cfg, proposed, named.Service, named.Target); err != nil {
		return vmRuntimeAdoptionPreparation{}, vmRuntimeJournalRecord{}, err
	}
	service := data.Services[named.Service]
	if service.VM.Components.Runtime.Staged != nil {
		return vmRuntimeAdoptionPreparation{}, vmRuntimeJournalRecord{}, fmt.Errorf("VM %s runtime selection changed while applying host policy", named.Service)
	}
	if service.VM.Components.Runtime.Configured == named.Target.Artifact {
		return vmRuntimeAdoptionPreparation{}, vmRuntimeJournalRecord{}, fmt.Errorf("VM %s already has runtime %s configured", named.Service, named.Target.Artifact.ID)
	}
	preparation, newService, err := prepareVMRuntimeTransitionState(ctx, cfg, service, named.Target.Artifact, nil, deps.inventory)
	if err != nil {
		return vmRuntimeAdoptionPreparation{}, vmRuntimeJournalRecord{}, err
	}
	record, err := buildVMRuntimeTransitionJournalRecord(cfg, preparation, newService, transactionID, preparedAt, deps)
	return preparation, record, err
}

func decorateVMRuntimePolicyCohortRecords(records []vmRuntimeJournalRecord, targets []vmRuntimeNamedTarget, oldHost, newHost vmRuntimeJournalVMHostProjection) {
	members := make([]string, len(targets))
	for i := range targets {
		members[i] = targets[i].Service
	}
	for i := range records {
		records[i].Members = append([]string(nil), members...)
		records[i].VMHostProjection = true
		records[i].OldVMHost = cloneVMRuntimeJournalVMHostProjection(&oldHost)
		records[i].NewVMHost = cloneVMRuntimeJournalVMHostProjection(&newHost)
	}
}

func validateVMRuntimePolicyCohortAgainstData(
	ctx context.Context,
	tx *VMRuntimeAdoption,
	data *db.Data,
	newHost *db.VMHostConfig,
	fleet []vmRuntimePolicyFleetPrecondition,
	targets []vmRuntimeNamedTarget,
) error {
	if len(tx.records) == 0 || !tx.records[0].VMHostProjection || tx.records[0].OldVMHost == nil || vmRuntimeJournalVMHostProjectionFromConfig(data.VMHost) != *tx.records[0].OldVMHost {
		return fmt.Errorf("host VM runtime policy changed during policy staging")
	}
	if err := validateVMRuntimePolicyFleetPrecondition(data, fleet); err != nil {
		return err
	}
	proposed := data.Clone()
	proposed.VMHost = newHost.Clone()
	for i := range tx.records {
		if err := validateVMRuntimePolicyCohortMember(ctx, tx, data, proposed, i, targets[i].Target); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimePolicyCohortMember(ctx context.Context, tx *VMRuntimeAdoption, data, proposed *db.Data, index int, target vmRuntimeResolvedTarget) error {
	record := tx.records[index]
	preparation := tx.preparations[index]
	service, err := validateVMRuntimeAdoptionOldProjection(data, record)
	if err != nil {
		return err
	}
	if _, err := validateVMRuntimeTransitionTarget(ctx, tx.cfg, proposed, record.Service, target); err != nil {
		return err
	}
	digest, err := vmRuntimeAdoptionPreconditionDigest(*tx.cfg, *service, preparation)
	if err != nil || digest != record.PreconditionSHA256 {
		return errors.Join(err, fmt.Errorf("VM runtime transition precondition changed for %s", record.Service))
	}
	if err := tx.revalidatePreparedEvidence(preparation, nil); err != nil {
		return err
	}
	_, err = loadAndRevalidateVMRuntimeAdoptionUnit(ctx, preparation, true, tx.deps.inventory)
	return err
}

func prepareVMRuntimeTransitionWithStore(
	ctx context.Context,
	cfg *Config,
	journal *vmRuntimeJournalStore,
	deps vmRuntimeAdoptionCoordinatorDeps,
	data *db.Data,
	service *db.Service,
	target vmRuntimeResolvedTarget,
) (_ *VMRuntimeAdoption, retErr error) {
	preparation, newService, err := prepareVMRuntimeTransitionState(ctx, cfg, service, target.Artifact, target.PolicyMutation, deps.inventory)
	if err != nil {
		return nil, err
	}
	transactionID, err := newVMRuntimeAdoptionTransactionID(deps.random)
	if err != nil {
		return nil, err
	}
	preparedAt := deps.now().UTC()
	record, err := buildVMRuntimeTransitionJournalRecord(cfg, preparation, newService, transactionID, preparedAt, deps)
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
	tx.beforeDerived = func() error {
		return validateVMRuntimeTransitionPrePublication(ctx, tx, target)
	}
	tx.revalidateEvidence = func(preparation vmRuntimeAdoptionPreparation, skipped map[string]struct{}) error {
		return revalidateVMRuntimeTransitionEvidence(preparation, skipped, deps.inventory.evidence)
	}
	if err := validatePreparedVMRuntimeTransition(ctx, tx, data, target); err != nil {
		return nil, fmt.Errorf("revalidate VM runtime transition before journal publication: %w", err)
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

func prepareVMRuntimeTransitionState(
	ctx context.Context,
	cfg *Config,
	service *db.Service,
	target db.VMRuntimeArtifactConfig,
	policyMutation *vmRuntimePolicyMutation,
	deps vmRuntimeAdoptionInventoryDeps,
) (vmRuntimeAdoptionPreparation, *db.Service, error) {
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return vmRuntimeAdoptionPreparation{}, nil, fmt.Errorf("adopted VM runtime state is required")
	}
	root, loaded, flags, err := loadVMRuntimeTransitionState(ctx, cfg, service, deps)
	if err != nil {
		return vmRuntimeAdoptionPreparation{}, nil, err
	}
	evidence, err := collectVMRuntimeTransitionEvidence(service, target, root, loaded, flags, deps.evidence)
	if err != nil {
		return vmRuntimeAdoptionPreparation{}, nil, err
	}
	newService, err := serviceWithStagedVMRuntime(service, target, policyMutation)
	if err != nil {
		return vmRuntimeAdoptionPreparation{}, nil, err
	}
	oldDB := vmRuntimeJournalDBProjection{Components: service.VM.Components.Clone(), ImageKernel: service.VM.Image.Kernel}
	newDB := vmRuntimeJournalDBProjection{Components: newService.VM.Components.Clone(), ImageKernel: newService.VM.Image.Kernel}
	preparation := vmRuntimeAdoptionPreparation{
		Service: service.Name, ServiceRoot: root, Classification: vmRuntimeAdoptionAlreadyAdopted,
		OldDB: oldDB, NewDB: newDB, Components: newService.VM.Components.Clone(),
		EffectiveUnit: cloneVMRuntimeAdoptionLoadedUnit(loaded), EffectiveRuntime: service.VM.Components.Runtime.Configured,
		EffectiveKernel: service.VM.Image.Kernel,
		Evidence:        evidence,
	}
	if err := validateDescriptorVMRuntimeAdoptionLoadedState(preparation, loaded); err != nil {
		return vmRuntimeAdoptionPreparation{}, nil, err
	}
	preparation.PreconditionSHA256, err = vmRuntimeAdoptionPreconditionDigest(*cfg, *service, preparation)
	if err != nil {
		return vmRuntimeAdoptionPreparation{}, nil, err
	}
	return preparation, newService, nil
}

func loadVMRuntimeTransitionState(ctx context.Context, cfg *Config, service *db.Service, deps vmRuntimeAdoptionInventoryDeps) (string, vmRuntimeAdoptionLoadedUnit, map[string]string, error) {
	root, err := effectiveVMRuntimeAdoptionServiceRoot(*cfg, *service)
	if err != nil {
		return "", vmRuntimeAdoptionLoadedUnit{}, nil, err
	}
	loaded, err := deps.loadUnit(ctx, vmSystemdUnitName(service.Name))
	if err != nil {
		return "", vmRuntimeAdoptionLoadedUnit{}, nil, fmt.Errorf("load effective VM unit for %s: %w", service.Name, err)
	}
	runner, flags, err := validateVMRuntimeAdoptionLoadedCommand(loaded.ExecStart)
	if err != nil {
		return "", vmRuntimeAdoptionLoadedUnit{}, nil, err
	}
	loaded.Runner = runner
	loaded.JailerBase = flags["--jailer-base"]
	if err := validateVMRuntimeTransitionLoadedPaths(*cfg, *service, root, flags); err != nil {
		return "", vmRuntimeAdoptionLoadedUnit{}, nil, err
	}
	return root, loaded, flags, nil
}

func collectVMRuntimeTransitionEvidence(service *db.Service, target db.VMRuntimeArtifactConfig, root string, loaded vmRuntimeAdoptionLoadedUnit, flags map[string]string, deps vmRuntimeAdoptionEvidenceDeps) (vmRuntimeAdoptionPreconditionEvidence, error) {
	disk, err := collectVMRuntimeActiveDiskEvidence(flags["--disk-path"], service.VM.Disk.Backend, service.VM.Disk.Bytes, deps)
	if err != nil {
		return vmRuntimeAdoptionPreconditionEvidence{}, fmt.Errorf("inventory active VM disk without reading its contents: %w", err)
	}
	files := make([]vmRuntimeAdoptionFileEvidence, 0, 10)
	for _, fragment := range loaded.Fragments {
		files = append(files, fragment.Evidence)
	}
	for _, artifact := range append(vmRuntimeLifecycleArtifacts(service.VM.Components.Runtime), target) {
		artifactFiles, err := collectVMRuntimeTransitionArtifactEvidence(artifact, deps)
		if err != nil {
			return vmRuntimeAdoptionPreconditionEvidence{}, err
		}
		files = append(files, artifactFiles...)
	}
	files, err = normalizeVMRuntimeAdoptionEvidence(files)
	if err != nil {
		return vmRuntimeAdoptionPreconditionEvidence{}, err
	}
	return vmRuntimeAdoptionPreconditionEvidence{Service: service.Name, ServiceRoot: root, Files: files, ActiveDisk: disk}, nil
}

func serviceWithStagedVMRuntime(service *db.Service, target db.VMRuntimeArtifactConfig, policyMutation *vmRuntimePolicyMutation) (*db.Service, error) {
	newService := service.Clone()
	newService.VM.Components.Runtime.Staged = cloneVMRuntimeArtifact(&target)
	if policyMutation != nil {
		if err := applyVMRuntimePolicyMutation(&newService.VM.Components.Runtime, *policyMutation); err != nil {
			return nil, err
		}
	}
	if pendingVMRuntimeTrial(newService.VM.Components.Runtime) {
		newService.VM.Components.Runtime.Trial = nil
	}
	return newService, nil
}

type vmRuntimeTerminalReplacement struct {
	path    string
	id      vmJailerFileIdentity
	control vmRuntimeControlFileDeps
}

func validateTerminalVMRuntimeTrialReplacement(cfg *Config, service *db.Service, deps vmRuntimeAdoptionCoordinatorDeps) (*vmRuntimeTerminalReplacement, error) {
	if service == nil || service.VM == nil || service.VM.Components == nil || service.VM.Components.Runtime.Staged == nil {
		return nil, fmt.Errorf("pending VM runtime trial state is incomplete")
	}
	root := serviceRootFromConfig(*cfg, *service)
	control := defaultVMRuntimeControlFileDeps()
	control.uid = deps.descriptor.uid
	control.gid = deps.descriptor.gid
	resultPath := filepath.Join(serviceRunDirForRoot(root), vmRuntimeTrialResultFileName)
	result, resultID, err := readTrustedVMRuntimeTrialResult(resultPath, service.Name, control)
	if err != nil {
		return nil, fmt.Errorf("pending VM runtime trial has no trusted terminal result: %w", err)
	}
	if result.Outcome != vmRuntimeTrialFailedNoFallback {
		return nil, fmt.Errorf("pending VM runtime trial is not a replaceable terminal failure")
	}
	current, err := terminalVMRuntimeTrialMatchesCurrentGeneration(cfg, service, result, control)
	if err != nil {
		return nil, fmt.Errorf("terminal VM runtime trial generation is stale: %w", err)
	}
	if !current {
		return nil, fmt.Errorf("pending VM runtime trial is not a replaceable terminal failure")
	}
	return &vmRuntimeTerminalReplacement{path: resultPath, id: resultID, control: control}, nil
}

func refuseActiveVMRuntimeTrialReplacement(cfg *Config, service *db.Service, deps vmRuntimeAdoptionCoordinatorDeps) error {
	runtimeState := service.VM.Components.Runtime
	root := serviceRootFromConfig(*cfg, *service)
	descriptor, err := readVMRuntimeDescriptorSnapshotWithOwner(
		filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName), service.Name, deps.descriptor.uid, deps.descriptor.gid,
	)
	if err != nil {
		return err
	}
	marker, err := readTrustedVMRuntimeRunningMarker(
		filepath.Join(serviceRunDirForRoot(root), vmRuntimeRunningMarkerFileName), service.Name, deps.descriptor.uid, deps.descriptor.gid,
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect live VM runtime trial marker before replacing staged runtime: %w", err)
	}
	if marker.DescriptorSHA256 != descriptor.SHA256 ||
		(!vmRuntimeMarkerMatchesArtifact(marker, *runtimeState.Staged) && !vmRuntimeMarkerMatchesArtifact(marker, runtimeState.Configured)) {
		return nil
	}
	if vmRuntimeProcessAlive(marker.RunnerPID) && vmRuntimeProcessAlive(marker.ChildPID) {
		return fmt.Errorf("VM %s has an active runtime trial; wait for its host result before replacing the staged runtime", service.Name)
	}
	return nil
}

func validateVMRuntimeTransitionLoadedPaths(cfg Config, service db.Service, root string, flags map[string]string) error {
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
	wants := map[string]string{
		"-data-dir":                cfg.RootDir,
		"-services-root":           cfg.ServicesRoot,
		"--service":                service.Name,
		"--service-root":           root,
		"--disk-path":              diskPath,
		"--runtime-descriptor":     filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName),
		"--runtime-running-marker": filepath.Join(runDir, vmRuntimeRunningMarkerFileName),
		"--runtime-trial-result":   filepath.Join(runDir, vmRuntimeTrialResultFileName),
		"--jailer-base":            vmJailerBaseForDataRoot(cfg.RootDir),
		"--api-sock":               apiSocket,
		"--config-file":            filepath.Join(runDir, "firecracker.json"),
		"--console-sock":           consoleSocket,
	}
	for name, want := range wants {
		if flags[name] != want {
			return fmt.Errorf("loaded VM unit %s is %q, want %q", name, flags[name], want)
		}
	}
	if _, ok := flags["--firecracker"]; ok {
		return fmt.Errorf("adopted VM unit retained explicit Firecracker path")
	}
	if _, ok := flags["--jailer"]; ok {
		return fmt.Errorf("adopted VM unit retained explicit jailer path")
	}
	return nil
}

func collectVMRuntimeTransitionArtifactEvidence(artifact db.VMRuntimeArtifactConfig, deps vmRuntimeAdoptionEvidenceDeps) ([]vmRuntimeAdoptionFileEvidence, error) {
	if err := validateVMRuntimeArtifact(artifact, "transition"); err != nil {
		return nil, err
	}
	result := make([]vmRuntimeAdoptionFileEvidence, 0, 3)
	for _, expected := range []struct {
		label, path, digest string
	}{
		{label: "Firecracker", path: artifact.Firecracker, digest: artifact.FirecrackerSHA256},
		{label: "jailer", path: artifact.Jailer, digest: artifact.JailerSHA256},
	} {
		evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(expected.path, true, deps)
		if err != nil {
			return nil, fmt.Errorf("inventory %s for runtime %s: %w", expected.label, artifact.ID, err)
		}
		if evidence.SHA256 != expected.digest {
			return nil, fmt.Errorf("runtime %s %s digest changed", artifact.ID, expected.label)
		}
		result = append(result, evidence)
	}
	if artifact.ManifestSHA256 != "" && (artifact.Source == "official" || strings.HasPrefix(artifact.Source, "local:")) {
		manifestPath := filepath.Join(filepath.Dir(artifact.Firecracker), vmRuntimeManifestFilename)
		evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(manifestPath, true, deps)
		if err != nil {
			return nil, fmt.Errorf("inventory manifest for runtime %s: %w", artifact.ID, err)
		}
		if evidence.SHA256 != artifact.ManifestSHA256 {
			return nil, fmt.Errorf("runtime %s manifest digest changed", artifact.ID)
		}
		result = append(result, evidence)
	}
	return result, nil
}

func buildVMRuntimeTransitionJournalRecord(
	cfg *Config,
	preparation vmRuntimeAdoptionPreparation,
	newService *db.Service,
	transactionID string,
	preparedAt time.Time,
	deps vmRuntimeAdoptionCoordinatorDeps,
) (vmRuntimeJournalRecord, error) {
	descriptorPath := filepath.Join(serviceDataDirForRoot(preparation.ServiceRoot), vmRuntimeDescriptorFileName)
	oldDescriptor, err := readVMRuntimeAdoptionDescriptorJournalFile(descriptorPath, deps.descriptor)
	if err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	wantOld, err := vmRuntimeDescriptorFromService(&db.Service{
		Name: preparation.Service,
		VM:   &db.VMConfig{Components: preparation.OldDB.Components.Clone()},
	})
	if err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	if !oldDescriptor.Exists {
		return vmRuntimeJournalRecord{}, fmt.Errorf("VM runtime descriptor for %s is missing", preparation.Service)
	}
	decodedOld, err := decodeVMRuntimeDescriptor(oldDescriptor.Contents, preparation.Service)
	if err != nil || !equalVMRuntimeDescriptors(decodedOld, wantOld) {
		return vmRuntimeJournalRecord{}, errors.Join(err, fmt.Errorf("VM runtime descriptor for %s does not match database state", preparation.Service))
	}
	newDescriptor, err := renderVMRuntimeTransitionDescriptorJournalFile(descriptorPath, newService, deps.descriptor)
	if err != nil {
		return vmRuntimeJournalRecord{}, err
	}

	unitPath := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(preparation.Service))
	oldUnit, err := readVMRuntimeAdoptionUnitJournalFile(unitPath, deps.unit)
	if err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	unitSpec, err := renderVMRuntimeUnitSpec(*cfg, newService, preparation.EffectiveUnit.Runner)
	if err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	if unitSpec.Path != unitPath {
		return vmRuntimeJournalRecord{}, fmt.Errorf("VM runtime unit path changed for %s", preparation.Service)
	}
	newUnit := newVMRuntimeAdoptionJournalFile(unitPath, unitSpec.Content, unix.S_IFREG|0o644, deps.unit.uid, deps.unit.gid)
	return vmRuntimeJournalRecord{
		Schema: vmRuntimeJournalSchema, SchemaVersion: vmRuntimeJournalSchemaVersion,
		TransactionID: transactionID, Members: []string{preparation.Service}, Service: preparation.Service,
		ServiceRoot: preparation.ServiceRoot, State: vmRuntimeJournalStatePrepared,
		PreparedAt: preparedAt, UpdatedAt: preparedAt, PreconditionSHA256: preparation.PreconditionSHA256,
		OldDB: preparation.OldDB, NewDB: preparation.NewDB,
		OldDescriptor: oldDescriptor, NewDescriptor: newDescriptor, OldUnit: oldUnit, NewUnit: newUnit,
	}, nil
}

func renderVMRuntimeTransitionDescriptorJournalFile(path string, service *db.Service, deps vmRuntimeDescriptorFileDeps) (vmRuntimeJournalFile, error) {
	descriptor, err := vmRuntimeDescriptorFromService(service)
	if err != nil {
		return vmRuntimeJournalFile{}, err
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return vmRuntimeJournalFile{}, err
	}
	if _, err := decodeVMRuntimeDescriptor(raw, service.Name); err != nil {
		return vmRuntimeJournalFile{}, err
	}
	raw = append(raw, '\n')
	return newVMRuntimeAdoptionJournalFile(path, raw, unix.S_IFREG|0o600, deps.uid, deps.gid), nil
}

func validatePreparedVMRuntimeTransition(ctx context.Context, tx *VMRuntimeAdoption, data *db.Data, target vmRuntimeResolvedTarget) error {
	if err := tx.descriptors.verify(ctx, vmRuntimeDescriptorRawOld); err != nil {
		return err
	}
	if err := tx.units.VerifyOld(ctx); err != nil {
		return err
	}
	return validateVMRuntimeTransitionAgainstData(ctx, tx, data, target)
}

func validateVMRuntimeTransitionPrePublication(ctx context.Context, tx *VMRuntimeAdoption, target vmRuntimeResolvedTarget) error {
	return tx.cfg.DB.WithLatestDataLocked(func(view db.DataView) error {
		return validateVMRuntimeTransitionAgainstData(ctx, tx, view.AsStruct(), target)
	})
}

func validateVMRuntimeTransitionAgainstData(ctx context.Context, tx *VMRuntimeAdoption, data *db.Data, target vmRuntimeResolvedTarget) error {
	record := tx.records[0]
	preparation := tx.preparations[0]
	service, err := validateVMRuntimeAdoptionOldProjection(data, record)
	if err != nil {
		return err
	}
	if _, err := validateVMRuntimeTransitionTarget(ctx, tx.cfg, data, record.Service, target); err != nil {
		return err
	}
	digest, err := vmRuntimeAdoptionPreconditionDigest(*tx.cfg, *service, preparation)
	if err != nil || digest != record.PreconditionSHA256 {
		return errors.Join(err, fmt.Errorf("VM runtime transition precondition changed for %s", record.Service))
	}
	if err := tx.revalidatePreparedEvidence(preparation, nil); err != nil {
		return err
	}
	_, err = loadAndRevalidateVMRuntimeAdoptionUnit(ctx, preparation, true, tx.deps.inventory)
	return err
}

func revalidateVMRuntimeTransitionEvidence(preparation vmRuntimeAdoptionPreparation, skipped map[string]struct{}, deps vmRuntimeAdoptionEvidenceDeps) error {
	for _, expected := range preparation.Evidence.Files {
		if _, ok := skipped[expected.Path]; ok {
			continue
		}
		actual, err := collectTrustedVMRuntimeAdoptionFileEvidence(expected.Path, expected.Exists, deps)
		if err != nil {
			return fmt.Errorf("revalidate VM runtime transition evidence for %s: %w", preparation.Service, err)
		}
		if actual != expected {
			return fmt.Errorf("VM runtime transition evidence changed for %s at %s", preparation.Service, expected.Path)
		}
	}
	expectedDisk := preparation.Evidence.ActiveDisk
	actualDisk, err := collectVMRuntimeActiveDiskEvidence(expectedDisk.Path, expectedDisk.Backend, expectedDisk.Bytes, deps)
	if err != nil {
		return fmt.Errorf("revalidate VM runtime transition disk for %s: %w", preparation.Service, err)
	}
	// Active guest writes legitimately change timestamps. Runtime staging binds
	// the disk path, backend, declared size, object identity, ownership, and
	// type, but never mutable timestamps or contents.
	expectedDisk.MTimeNS = 0
	actualDisk.MTimeNS = 0
	if actualDisk != expectedDisk {
		return fmt.Errorf("VM runtime transition disk identity changed for %s", preparation.Service)
	}
	return nil
}

func validateVMRuntimeTransitionTarget(ctx context.Context, cfg *Config, data *db.Data, serviceName string, target vmRuntimeResolvedTarget) (*db.Service, error) {
	service := data.Services[serviceName]
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil || service.VM.Components == nil {
		return nil, fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	if target.Selection == vmRuntimeTargetSelectionPrevious {
		return validatePreviousVMRuntimeTransitionTarget(service, target)
	}
	if target.Selection == vmRuntimeTargetSelectionLocalAlias {
		return validateLocalVMRuntimeTransitionTarget(ctx, cfg, service, target)
	}
	return validateOfficialVMRuntimeTransitionTarget(data, service, target)
}

func validatePreviousVMRuntimeTransitionTarget(service *db.Service, target vmRuntimeResolvedTarget) (*db.Service, error) {
	previous := service.VM.Components.Runtime.Previous
	if previous == nil || *previous != target.Artifact {
		return nil, fmt.Errorf("persisted previous VM runtime changed during rollback selection")
	}
	return service, nil
}

func validateLocalVMRuntimeTransitionTarget(ctx context.Context, cfg *Config, service *db.Service, target vmRuntimeResolvedTarget) (*db.Service, error) {
	if target.LocalAlias == "" || target.Artifact.Source != "local:"+target.LocalAlias {
		return nil, fmt.Errorf("local VM runtime target no longer matches its durable alias")
	}
	rebound, err := resolveLocalVMRuntime(ctx, filepath.Join(cfg.RootDir, "vm-runtimes"), target.LocalAlias)
	if err != nil || rebound != target.Artifact {
		return nil, errors.Join(err, fmt.Errorf("local VM runtime target no longer matches its durable alias"))
	}
	return service, nil
}

func validateOfficialVMRuntimeTransitionTarget(data *db.Data, service *db.Service, target vmRuntimeResolvedTarget) (*db.Service, error) {
	if err := validateOfficialVMRuntimeTransitionCatalogTarget(target); err != nil {
		return nil, err
	}
	runtimeState := &service.VM.Components.Runtime
	policyRuntime, err := effectiveVMRuntimeTransitionPolicyState(service.Name, runtimeState, target)
	if err != nil {
		return nil, err
	}
	switch target.Selection {
	case vmRuntimeTargetSelectionOfficialID:
		return service, nil
	case vmRuntimeTargetSelectionChannel, vmRuntimeTargetSelectionUpstreamVersion:
		return validateChannelVMRuntimeTransitionTarget(data, service, runtimeState, policyRuntime, target)
	default:
		return nil, fmt.Errorf("unsupported VM runtime target selection %q", target.Selection)
	}
}

func validateOfficialVMRuntimeTransitionCatalogTarget(target vmRuntimeResolvedTarget) error {
	if target.CatalogRef == nil {
		return fmt.Errorf("official VM runtime target is missing its catalog identity")
	}
	if err := validateResolvedOfficialVMRuntimeArtifact(target.Artifact, *target.CatalogRef); err != nil {
		return err
	}
	if target.CatalogRef.Support == "revoked" {
		return fmt.Errorf("VM runtime %s is revoked", target.CatalogRef.RuntimeID)
	}
	return nil
}

func effectiveVMRuntimeTransitionPolicyState(serviceName string, runtimeState *db.VMRuntimeLifecycleConfig, target vmRuntimeResolvedTarget) (*db.VMRuntimeLifecycleConfig, error) {
	policyRuntime := runtimeState
	if target.PolicyMutation != nil {
		clone := *runtimeState
		if err := applyVMRuntimePolicyMutation(&clone, *target.PolicyMutation); err != nil {
			return nil, err
		}
		policyRuntime = &clone
	}
	if target.ExpectedRuntime != nil && !reflect.DeepEqual(*policyRuntime, *target.ExpectedRuntime) {
		return nil, fmt.Errorf("VM %s runtime lifecycle changed during policy target selection", serviceName)
	}
	return policyRuntime, nil
}

func validateChannelVMRuntimeTransitionTarget(data *db.Data, service *db.Service, runtimeState, policyRuntime *db.VMRuntimeLifecycleConfig, target vmRuntimeResolvedTarget) (*db.Service, error) {
	if !vmRuntimePolicyAllowsOfficialUpgrade(runtimeState.Configured.Source) {
		return nil, fmt.Errorf("VM %s changed to non-official runtime source during target selection", service.Name)
	}
	policy, err := effectiveVMRuntimePolicyFor(data.VMHost, policyRuntime)
	if err != nil {
		return nil, err
	}
	if target.ChannelFromPolicy && (policy.Mode != cli.VMRuntimePolicyStageOnRestart || policy.Channel != target.Channel) {
		return nil, fmt.Errorf("VM %s effective runtime policy changed during target selection", service.Name)
	}
	return service, nil
}

func validateExistingVMRuntimeStage(ctx context.Context, cfg *Config, service *db.Service, deps vmRuntimeAdoptionCoordinatorDeps) (bool, error) {
	preparation, _, err := prepareVMRuntimeTransitionState(ctx, cfg, service, *service.VM.Components.Runtime.Staged, nil, deps.inventory)
	if err != nil {
		return false, err
	}
	wantDescriptor, err := vmRuntimeDescriptorFromService(service)
	if err != nil {
		return false, err
	}
	descriptorPath := filepath.Join(serviceDataDirForRoot(preparation.ServiceRoot), vmRuntimeDescriptorFileName)
	gotDescriptor, err := readVMRuntimeDescriptorWithOwner(descriptorPath, service.Name, deps.descriptor.uid, deps.descriptor.gid)
	if err != nil || !equalVMRuntimeDescriptors(gotDescriptor, wantDescriptor) {
		return false, errors.Join(err, fmt.Errorf("VM runtime descriptor for %s does not match staged database state", service.Name))
	}
	unitSpec, err := renderVMRuntimeUnitSpec(*cfg, service, preparation.EffectiveUnit.Runner)
	if err != nil {
		return false, err
	}
	gotUnit, err := readVMRuntimeAdoptionUnitJournalFile(unitSpec.Path, deps.unit)
	if err != nil {
		return false, err
	}
	wantUnit := newVMRuntimeAdoptionJournalFile(unitSpec.Path, unitSpec.Content, unix.S_IFREG|0o644, deps.unit.uid, deps.unit.gid)
	if !equalVMRuntimeDescriptorRawFiles(gotUnit, wantUnit) {
		return false, fmt.Errorf("VM runtime unit for %s does not match staged database state", service.Name)
	}
	return preparation.EffectiveUnit.ActiveState == "active", nil
}

func cloneVMRuntimeArtifact(artifact *db.VMRuntimeArtifactConfig) *db.VMRuntimeArtifactConfig {
	if artifact == nil {
		return nil
	}
	clone := *artifact
	return &clone
}
