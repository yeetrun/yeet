// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

var errServiceIdentityRecoveryBlocked = errors.New("service identity recovery blocks mutations")

func (s *Server) recoverServiceIdentityMigrations(ctx context.Context) error {
	return s.recoverServiceIdentityMigrationsWithOps(ctx, nil)
}

func (s *Server) recoverServiceIdentityMigrationsWithOps(ctx context.Context, overrides *serviceIdentityMigrationOps) error {
	dir := filepath.Join(s.cfg.RootDir, "migrations", "service-identity")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan service identity recovery journals: %w", err)
	}
	pathsByService, loadErrors, scanErr := s.scanServiceIdentityRecoveryJournals(dir, entries)
	for _, service := range sortedServiceIdentityRecoveryServices(pathsByService) {
		recoverErr := s.recoverServiceIdentityJournalSet(ctx, service, pathsByService[service], loadErrors, overrides)
		scanErr = errors.Join(scanErr, recoverErr)
	}
	return scanErr
}

func (s *Server) scanServiceIdentityRecoveryJournals(dir string, entries []os.DirEntry) (map[string][]string, map[string]error, error) {
	pathsByService := make(map[string][]string)
	loadErrors := make(map[string]error)
	var scanErr error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		filenameService, filenameAttributed := s.serviceIdentityJournalFilenameService(path)
		if filenameAttributed {
			pathsByService[filenameService] = append(pathsByService[filenameService], path)
		}
		contents, loadErr := loadServiceIdentityJournalForRecovery(path)
		if loadErr != nil {
			block := fmt.Errorf("%w: load %s: %v", errServiceIdentityRecoveryBlocked, path, loadErr)
			if filenameAttributed {
				loadErrors[path] = block
			} else {
				s.setServiceIdentityGlobalMutationBlock(block)
				scanErr = errors.Join(scanErr, block)
			}
			continue
		}
		if !filenameAttributed {
			pathsByService[contents.Header.Service] = append(pathsByService[contents.Header.Service], path)
		}
	}
	return pathsByService, loadErrors, scanErr
}

func sortedServiceIdentityRecoveryServices(pathsByService map[string][]string) []string {
	services := make([]string, 0, len(pathsByService))
	for service := range pathsByService {
		services = append(services, service)
	}
	slices.Sort(services)
	return services
}

func (s *Server) recoverServiceIdentityJournalSet(ctx context.Context, service string, paths []string, loadErrors map[string]error, overrides *serviceIdentityMigrationOps) error {
	slices.Sort(paths)
	if len(paths) != 1 {
		block := fmt.Errorf("%w for %q: duplicate journals: %s", errServiceIdentityRecoveryBlocked, service, strings.Join(paths, ", "))
		s.setServiceIdentityMutationBlock(service, block)
		return block
	}
	if loadErr := loadErrors[paths[0]]; loadErr != nil {
		s.setServiceIdentityMutationBlock(service, loadErr)
		return loadErr
	}
	if recoverErr := s.recoverServiceIdentityMigration(ctx, paths[0], overrides); recoverErr != nil {
		s.setServiceIdentityMutationBlock(service, recoverErr)
		return recoverErr
	}
	s.clearServiceIdentityMutationBlock(service)
	return nil
}

func (s *Server) recoverServiceIdentityMigration(ctx context.Context, path string, overrides *serviceIdentityMigrationOps) error {
	contents, err := loadServiceIdentityJournalForRecovery(path)
	if err != nil {
		return fmt.Errorf("%w: load journal %s: %v", errServiceIdentityRecoveryBlocked, path, err)
	}
	if err := s.validateServiceIdentityRecoveryHeader(contents, overrides); err != nil {
		return fmt.Errorf("%w for %q: validate %s: %v", errServiceIdentityRecoveryBlocked, contents.Header.Service, path, err)
	}
	if err := s.validateServiceIdentityRecoveryContents(contents); err != nil {
		return fmt.Errorf("%w for %q: validate %s: %v", errServiceIdentityRecoveryBlocked, contents.Header.Service, path, err)
	}
	release := s.serviceOperationLocks.Lock(contents.Header.Service)
	defer release()
	recovery := newServiceIdentityRecoveryTransaction(s, path, contents, overrides)
	if serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseComplete) {
		return recovery.cleanupCommitted(ctx)
	}
	if err := recovery.validateRollback(); err != nil {
		return err
	}
	if err := recovery.restoreRollback(ctx); err != nil {
		return serviceIdentityRecoveryError(contents, path, "rollback", err)
	}
	return recovery.finishRollback(ctx)
}

type serviceIdentityRecoveryTransaction struct {
	server               *Server
	path                 string
	contents             serviceIdentityJournalContents
	migration            *serviceIdentityMigration
	ops                  serviceIdentityMigrationOps
	customRestore        bool
	materialization      serviceIdentityPhaseRecord
	materialized         bool
	generationStarted    bool
	generationComplete   bool
	runtimeStarted       bool
	runtimeComplete      bool
	unitMutation         bool
	discardedRoot        string
	rootAlreadyDiscarded bool
}

func newServiceIdentityRecoveryTransaction(s *Server, path string, contents serviceIdentityJournalContents, overrides *serviceIdentityMigrationOps) *serviceIdentityRecoveryTransaction {
	m := &serviceIdentityMigration{
		server:             s,
		req:                serviceIdentityMigrationRequest{Service: contents.Header.Service},
		migrationID:        contents.Header.ID,
		previous:           serviceIdentityJournalObservedService(contents.Header).Clone(),
		predecessor:        contents.Header.PreviousService.Clone(),
		predecessorPresent: serviceIdentityJournalPredecessorPresent(contents.Header),
		target:             contents.Header.TargetService.Clone(),
		previousRuntime:    serviceIdentityJournalRuntimeState(contents.Header),
		previousUnitProof:  contents.Header.PreviousUnitProof,
	}
	materialization, materialized := serviceIdentityJournalPhase(contents, serviceIdentityPhaseMaterialize)
	if materialized {
		m.materialization = materialization
		m.initialMaterialization = materialization
	}
	if creation, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseMaterializeCreated); ok {
		m.materializationCreation = creation
	}
	if publish, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseMaterializePublish); ok {
		m.materializationPublish = publish
	}
	ops := m.defaultOps()
	customRestore := false
	if overrides != nil {
		customRestore = overrides.restore != nil
		ops.merge(*overrides)
	}
	return &serviceIdentityRecoveryTransaction{
		server: s, path: path, contents: contents, migration: m, ops: ops, customRestore: customRestore,
		materialization: materialization, materialized: materialized,
		generationStarted:  serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationBackup),
		generationComplete: serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationBackedUp),
		runtimeStarted:     serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseRuntimeBackup),
		runtimeComplete:    serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseRuntimeBackedUp),
		unitMutation: serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationStageIntent) ||
			serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseUnitWriteIntent),
	}
}

func (r *serviceIdentityRecoveryTransaction) cleanupCommitted(ctx context.Context) error {
	header := r.contents.Header
	if header.RootPlan != nil && header.RootPlan.CreateNewRootZFS {
		if err := clearServiceIdentityZFSDatasetMarker(ctx, r.server.zfsRunner, header.RootPlan.NewRootZFS, r.materialization.DatasetGUID, header.ID); err != nil {
			return serviceIdentityRecoveryError(r.contents, r.path, "clear committed target dataset transaction marker", err)
		}
	}
	if serviceIdentityJournalHasPhase(r.contents, serviceIdentityPhaseGenerationBackup) {
		if err := cleanupServiceIdentityGenerationBackups(serviceIdentityJournalGenerationBackups(r.contents)); err != nil {
			return serviceIdentityRecoveryError(r.contents, r.path, "cleanup committed generation backups", err)
		}
	}
	if err := cleanupLegacyNativeRuntimeBackup(serviceIdentityJournalRuntimeBackups(r.contents)); err != nil {
		return serviceIdentityRecoveryError(r.contents, r.path, "cleanup committed runtime artifact backup", err)
	}
	if err := cleanupServiceIdentityTransactionBackupDir(r.server.cfg.RootDir, header.ID); err != nil {
		return serviceIdentityRecoveryError(r.contents, r.path, "cleanup committed transaction backup directory", err)
	}
	if err := r.ops.remove(r.path); err != nil {
		return serviceIdentityRecoveryError(r.contents, r.path, "remove committed journal", err)
	}
	return nil
}

func (r *serviceIdentityRecoveryTransaction) validateRollback() error {
	contents := r.contents
	if r.generationStarted && !r.generationComplete {
		if err := validateIncompleteServiceIdentityGeneration(contents.Header.GenerationBackups); err != nil {
			return serviceIdentityRecoveryError(contents, r.path, "validate incomplete generation backup before rollback", err)
		}
	}
	if r.runtimeStarted && !r.runtimeComplete {
		planned, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseRuntimePlan)
		if err := validateIncompleteServiceIdentityGeneration(planned.RuntimeBackups); err != nil {
			return serviceIdentityRecoveryError(contents, r.path, "validate incomplete runtime backup before rollback", err)
		}
	}
	if r.unitMutation {
		if _, _, err := validateServiceIdentityPrimaryUnitRollback(
			contents.Header.PreviousUnitProof, serviceIdentityJournalPrimaryProof(contents), serviceIdentityJournalPrimaryIntent(contents),
		); err != nil {
			return serviceIdentityRecoveryError(contents, r.path, "validate primary unit before rollback", err)
		}
	}
	return nil
}

func (r *serviceIdentityRecoveryTransaction) restoreRollback(ctx context.Context) error {
	contents := r.contents
	var recoveryErr error
	running, runningErr := r.ops.isReplacementRunning(ctx, contents.Header.Service)
	recoveryErr = errors.Join(recoveryErr, runningErr)
	if runningErr == nil && running {
		recoveryErr = errors.Join(recoveryErr, stopServiceIdentityObserved(ctx, r.ops.stopReplacement, r.ops.isReplacementRunning, contents.Header.Service))
	}
	if r.unitMutation {
		recoveryErr = errors.Join(recoveryErr, restoreServiceIdentityRecoveryUnit(contents))
	}
	r.detectDiscardedRoot()
	recoveryErr = errors.Join(recoveryErr, r.restoreOwnership())
	generationErr, fatal := r.restoreGeneration()
	if fatal != nil {
		return fatal
	}
	recoveryErr = errors.Join(recoveryErr, generationErr)
	runtimeErr, fatal := r.restoreRuntimeBackups()
	if fatal != nil {
		return fatal
	}
	recoveryErr = errors.Join(recoveryErr, runtimeErr)
	recoveryErr = errors.Join(recoveryErr, r.ops.reload(ctx))
	if serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationActivate) {
		recoveryErr = errors.Join(recoveryErr, restoreServiceIdentityUnitEnablement(ctx, r.ops, contents.Header.GenerationUnits))
	}
	recoveryErr = errors.Join(recoveryErr, restoreServiceIdentityRecoveryDatabase(r.server, contents.Header))
	recoveryErr = errors.Join(recoveryErr, r.discardMaterializedRoot(ctx))
	if recoveryErr == nil {
		recoveryErr = restoreServiceIdentityRecoveryRunningState(ctx, r.ops, contents.Header)
	}
	return recoveryErr
}

func (r *serviceIdentityRecoveryTransaction) detectDiscardedRoot() {
	contents := r.contents
	if contents.Header.RootPlan != nil && !contents.Header.RootPlan.NewRootExisted {
		_, statErr := os.Lstat(contents.Header.RootPlan.NewRoot)
		r.rootAlreadyDiscarded = errors.Is(statErr, os.ErrNotExist)
		if r.rootAlreadyDiscarded {
			r.discardedRoot = contents.Header.RootPlan.NewRoot
		}
	}
}

func (r *serviceIdentityRecoveryTransaction) restoreOwnership() error {
	if !r.contents.Sealed || r.rootAlreadyDiscarded {
		return nil
	}
	if r.customRestore {
		return r.ops.restore(r.path)
	}
	return restoreServiceIdentityJournalContents(r.contents)
}

func (r *serviceIdentityRecoveryTransaction) restoreGeneration() (error, error) {
	contents := r.contents
	staged, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationStage)
	stageIntent, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationStageIntent)
	if r.generationComplete {
		if err := validateServiceIdentityGenerationRestoration(
			serviceIdentityJournalGenerationBackups(contents), staged.GenerationPaths, stageIntent.GenerationIntents, r.discardedRoot,
		); err != nil {
			return nil, serviceIdentityRecoveryError(contents, r.path, "validate staged generation before rollback", err)
		}
		return restoreServiceIdentityGenerationBackups(serviceIdentityJournalGenerationBackups(contents), staged.GenerationPaths, stageIntent.GenerationIntents, r.discardedRoot), nil
	}
	if r.generationStarted {
		return cleanupIncompleteServiceIdentityBackups(contents.Header.GenerationBackups), nil
	}
	return nil, nil
}

func (r *serviceIdentityRecoveryTransaction) restoreRuntimeBackups() (error, error) {
	contents := r.contents
	if r.runtimeComplete {
		if err := validateLegacyNativeRuntimeRestoration(r.server.cfg.RootDir, serviceIdentityJournalTargetRoot(contents.Header), serviceIdentityJournalRuntimeBackups(contents), r.discardedRoot); err != nil {
			return nil, serviceIdentityRecoveryError(contents, r.path, "validate runtime backup before rollback", err)
		}
		return restoreLegacyNativeRuntimeBackup(r.server.cfg.RootDir, serviceIdentityJournalTargetRoot(contents.Header), serviceIdentityJournalRuntimeBackups(contents), r.discardedRoot), nil
	}
	if r.runtimeStarted {
		planned, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseRuntimePlan)
		return cleanupIncompleteServiceIdentityBackups(planned.RuntimeBackups), nil
	}
	return nil, nil
}

func (r *serviceIdentityRecoveryTransaction) discardMaterializedRoot(ctx context.Context) error {
	contents := r.contents
	if contents.Header.RootPlan != nil {
		switch {
		case r.materialized:
			return r.ops.discardRoot(ctx, *contents.Header.RootPlan, r.materialization.DatasetCreated)
		case r.migration.materializationCreation.RootDev != 0:
			return r.ops.discardRoot(ctx, *contents.Header.RootPlan, r.migration.materializationCreation.DatasetCreated)
		case serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseMaterializeIntent):
			return r.ops.discardRoot(ctx, *contents.Header.RootPlan, contents.Header.RootPlan.CreateNewRootZFS)
		}
	}
	return nil
}

func (r *serviceIdentityRecoveryTransaction) finishRollback(ctx context.Context) error {
	if err := verifyServiceIdentityRecoveryState(ctx, r.server, r.ops, r.contents.Header); err != nil {
		return serviceIdentityRecoveryError(r.contents, r.path, "verify rollback", err)
	}
	if err := cleanupServiceIdentityTransactionBackupDir(r.server.cfg.RootDir, r.contents.Header.ID); err != nil {
		return serviceIdentityRecoveryError(r.contents, r.path, "remove recovered transaction backup directory", err)
	}
	if err := r.ops.remove(r.path); err != nil {
		return serviceIdentityRecoveryError(r.contents, r.path, "remove recovered journal", err)
	}
	return nil
}

func (s *Server) validateServiceIdentityRecoveryHeader(contents serviceIdentityJournalContents, overrides *serviceIdentityMigrationOps) error {
	header := contents.Header
	observed := serviceIdentityJournalObservedService(header)
	predecessorPresent := serviceIdentityJournalPredecessorPresent(header)
	if err := validateServiceIdentityRecoveryDatabaseRecords(header, observed, predecessorPresent); err != nil {
		return err
	}
	if err := s.validateServiceIdentityRecoveryGenerationBackups(header); err != nil {
		return err
	}
	if err := s.validateServiceIdentityRecoveryRuntimeBackups(contents); err != nil {
		return err
	}
	if err := validateServiceIdentityRecoveryRuntimeUnits(header, observed); err != nil {
		return err
	}
	unitPath, err := validateServiceIdentityRecoveryUnit(contents, overrides)
	if err != nil {
		return err
	}
	if err := s.validateServiceIdentityRecoveryStorage(contents, observed, unitPath); err != nil {
		return err
	}
	return s.validateServiceIdentityRecoveryCurrentDatabase(header, observed, predecessorPresent)
}

func validateServiceIdentityRecoveryDatabaseRecords(header serviceIdentityJournalHeader, observed *db.Service, predecessorPresent bool) error {
	if observed == nil || header.TargetService == nil {
		return fmt.Errorf("journal does not contain exact observed and target database records")
	}
	if header.PreviousServicePresent && header.PreviousService == nil {
		return fmt.Errorf("journal marks the predecessor present without its exact database record")
	}
	if !serviceIdentityRecoveryRecordNamesMatch(header, observed, predecessorPresent) {
		return fmt.Errorf("database record names do not match service %q", header.Service)
	}
	if !serviceIdentityRecoveryRecordTypesMatch(header, observed, predecessorPresent) {
		return fmt.Errorf("database records for %q are not native systemd services", header.Service)
	}
	return validateServiceIdentityRecoveryRecordIdentities(header, predecessorPresent)
}

func serviceIdentityRecoveryRecordNamesMatch(header serviceIdentityJournalHeader, observed *db.Service, predecessorPresent bool) bool {
	return observed.Name == header.Service && header.TargetService.Name == header.Service &&
		(!predecessorPresent || header.PreviousService.Name == header.Service)
}

func serviceIdentityRecoveryRecordTypesMatch(header serviceIdentityJournalHeader, observed *db.Service, predecessorPresent bool) bool {
	return observed.ServiceType == db.ServiceTypeSystemd && header.TargetService.ServiceType == db.ServiceTypeSystemd &&
		(!predecessorPresent || header.PreviousService.ServiceType == db.ServiceTypeSystemd)
}

func validateServiceIdentityRecoveryRecordIdentities(header serviceIdentityJournalHeader, predecessorPresent bool) error {
	if header.TargetService.Identity == nil || *header.TargetService.Identity != header.TargetIdentity {
		return fmt.Errorf("target database identity does not match journal identity")
	}
	var predecessorIdentity *db.ServiceIdentity
	if predecessorPresent {
		predecessorIdentity = header.PreviousService.Identity
	}
	if !reflect.DeepEqual(predecessorIdentity, header.PreviousIdentity) {
		return fmt.Errorf("previous database identity does not match journal identity")
	}
	return nil
}

func (s *Server) validateServiceIdentityRecoveryGenerationBackups(header serviceIdentityJournalHeader) error {
	seenGenerationPaths := make(map[string]struct{}, len(header.GenerationBackups))
	for index, backup := range header.GenerationBackups {
		path := filepath.Clean(backup.Path)
		if !filepath.IsAbs(path) || path == filepath.Clean(header.PreviousUnitPath) {
			return fmt.Errorf("invalid generation backup path %q", backup.Path)
		}
		if _, duplicate := seenGenerationPaths[path]; duplicate {
			return fmt.Errorf("duplicate generation backup path %q", path)
		}
		seenGenerationPaths[path] = struct{}{}
		wantBackup := filepath.Join(serviceIdentityMigrationBackupDir(s.cfg.RootDir, header.ID), "generation", fmt.Sprintf("%03d", index))
		if filepath.Clean(backup.BackupPath) != filepath.Clean(wantBackup) {
			return fmt.Errorf("generation backup for %s is not transaction-owned", path)
		}
		if filepath.Clean(backup.Original.Path) != path || backup.Original.Present != backup.Present {
			return fmt.Errorf("generation backup for %s has incoherent original provenance", path)
		}
		if err := validateServiceIdentityPathProofRecord(backup.Original, path); err != nil {
			return fmt.Errorf("generation backup for %s: %w", path, err)
		}
	}
	return nil
}

func (s *Server) validateServiceIdentityRecoveryRuntimeBackups(contents serviceIdentityJournalContents) error {
	header := contents.Header
	runtimePlan, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseRuntimePlan)
	runtimeBackups := runtimePlan.RuntimeBackups
	runtimeDir := filepath.Clean(serviceRunDirForRoot(serviceIdentityJournalTargetRoot(header)))
	seenRuntimePaths := make(map[string]struct{}, len(runtimeBackups))
	for index, backup := range runtimeBackups {
		if err := s.validateServiceIdentityRecoveryRuntimeBackup(header, runtimeDir, backup, index, seenRuntimePaths); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) validateServiceIdentityRecoveryRuntimeBackup(header serviceIdentityJournalHeader, runtimeDir string, backup serviceIdentityGenerationBackup, index int, seen map[string]struct{}) error {
	path := filepath.Clean(backup.Path)
	if !filepath.IsAbs(path) || filepath.Dir(path) != runtimeDir {
		return fmt.Errorf("invalid legacy runtime backup path %q", backup.Path)
	}
	if !legacyNativeRuntimeArtifactName(filepath.Base(path), header.Service) {
		return fmt.Errorf("legacy runtime backup path %q is not a managed compatibility artifact", backup.Path)
	}
	if _, duplicate := seen[path]; duplicate {
		return fmt.Errorf("duplicate legacy runtime backup path %q", path)
	}
	seen[path] = struct{}{}
	wantBackup := filepath.Join(serviceIdentityMigrationBackupDir(s.cfg.RootDir, header.ID), "runtime", fmt.Sprintf("%03d", index))
	if filepath.Clean(backup.BackupPath) != filepath.Clean(wantBackup) {
		return fmt.Errorf("legacy runtime backup for %s is not transaction-owned", path)
	}
	if !backup.Present || filepath.Clean(backup.Original.Path) != path || !backup.Original.Present {
		return fmt.Errorf("legacy runtime backup for %s has incoherent original provenance", path)
	}
	if err := validateServiceIdentityPathProofRecord(backup.Original, path); err != nil {
		return fmt.Errorf("legacy runtime backup for %s: %w", path, err)
	}
	return nil
}

func validateServiceIdentityRecoveryRuntimeUnits(header serviceIdentityJournalHeader, observed *db.Service) error {
	if err := validateServiceIdentityRecoveryGenerationUnitNames(header.GenerationUnits); err != nil {
		return err
	}
	return validateServiceIdentityRecoveryPreviousRuntimeUnits(header, observed)
}

func validateServiceIdentityRecoveryGenerationUnitNames(states []serviceIdentityUnitEnablement) error {
	seenGenerationUnits := make(map[string]struct{}, len(states))
	for _, state := range states {
		unit := strings.TrimSpace(state.Unit)
		if unit == "" || filepath.Base(unit) != unit || strings.ContainsAny(unit, "\x00\r\n\t ") {
			return fmt.Errorf("invalid generation unit %q", state.Unit)
		}
		if _, duplicate := seenGenerationUnits[unit]; duplicate {
			return fmt.Errorf("duplicate generation unit %q", unit)
		}
		seenGenerationUnits[unit] = struct{}{}
	}
	return nil
}

func validateServiceIdentityRecoveryPreviousRuntimeUnits(header serviceIdentityJournalHeader, observed *db.Service) error {
	stopUnits, _ := serviceIdentityRuntimeUnits(observed, header.Service)
	actualUnits := make([]string, len(header.PreviousRuntimeUnits))
	for index, state := range header.PreviousRuntimeUnits {
		unit := strings.TrimSpace(state.Unit)
		if unit == "" || filepath.Base(unit) != unit || strings.ContainsAny(unit, "\x00\r\n\t ") {
			return fmt.Errorf("invalid previous runtime unit %q", state.Unit)
		}
		actualUnits[index] = unit
	}
	if !slices.Equal(actualUnits, stopUnits) {
		return fmt.Errorf("previous runtime units do not match the observed service generation")
	}
	if serviceIdentityPrimaryRuntimeActive(observed, header.Service, header.PreviousRuntimeUnits) != header.WasRunning {
		return fmt.Errorf("previous primary runtime state does not match wasRunning")
	}
	return nil
}

func validateServiceIdentityRecoveryUnit(contents serviceIdentityJournalContents, overrides *serviceIdentityMigrationOps) (string, error) {
	header := contents.Header
	unitPath, err := serviceIdentityRecoveryUnitPath(header, overrides)
	if err != nil {
		return "", err
	}
	unitMutationRecorded := serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseUnitWriteIntent) ||
		serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseUnitWrite) ||
		serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationStageIntent)
	if err := validateServiceIdentityRecoveryPreviousUnit(header, unitPath, unitMutationRecorded); err != nil {
		return "", err
	}
	if err := validateServiceIdentityRecoveryUnitIntent(contents, unitPath); err != nil {
		return "", err
	}
	if err := validateServiceIdentityRecoveryWrittenUnit(contents, unitPath); err != nil {
		return "", err
	}
	return unitPath, nil
}

func serviceIdentityRecoveryUnitPath(header serviceIdentityJournalHeader, overrides *serviceIdentityMigrationOps) (string, error) {
	expected := filepath.Join(systemdSystemDir, header.Service+".service")
	if overrides != nil && overrides.unitPath != nil {
		expected = overrides.unitPath(header.Service)
	}
	unitPath := header.PreviousUnitPath
	if unitPath == "" {
		unitPath = expected
	}
	if !filepath.IsAbs(unitPath) || filepath.Clean(unitPath) != filepath.Clean(expected) {
		return "", fmt.Errorf("unit path %q does not match %q", unitPath, expected)
	}
	return unitPath, nil
}

func validateServiceIdentityRecoveryPreviousUnit(header serviceIdentityJournalHeader, unitPath string, mutationRecorded bool) error {
	if header.PreviousUnitProof.Path != "" {
		if err := validateServiceIdentityPathProofRecord(header.PreviousUnitProof, unitPath); err != nil {
			return fmt.Errorf("previous primary unit: %w", err)
		}
		previousDigest := sha256.Sum256([]byte(header.PreviousUnit))
		if header.PreviousUnitProof.Present != header.PreviousUnitPresent || header.PreviousUnitPresent &&
			(header.PreviousUnitProof.Mode.Perm() != header.PreviousUnitMode.Perm() ||
				header.PreviousUnitProof.Size != int64(len(header.PreviousUnit)) ||
				header.PreviousUnitProof.SHA256 != hex.EncodeToString(previousDigest[:])) {
			return fmt.Errorf("previous primary unit proof does not match captured bytes and mode")
		}
		return nil
	}
	if mutationRecorded {
		return fmt.Errorf("unit mutation lacks previous primary unit provenance")
	}
	return nil
}

func validateServiceIdentityRecoveryUnitIntent(contents serviceIdentityJournalContents, unitPath string) error {
	if intent, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseUnitWriteIntent); ok {
		if err := validateServiceIdentityPathProofRecord(intent.PrimaryUnit, unitPath); err != nil {
			return fmt.Errorf("unit write intent: %w", err)
		}
		if err := validateServiceIdentityPathState(intent.PrimaryUnitIntent, unitPath); err != nil {
			return fmt.Errorf("unit write intent: %w", err)
		}
	}
	return nil
}

func validateServiceIdentityRecoveryWrittenUnit(contents serviceIdentityJournalContents, unitPath string) error {
	if written, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseUnitWrite); ok {
		if !written.PrimaryUnit.Present {
			return fmt.Errorf("completed unit write lacks replacement provenance")
		}
		if err := validateServiceIdentityPathProofRecord(written.PrimaryUnit, unitPath); err != nil {
			return fmt.Errorf("completed unit write: %w", err)
		}
		intent, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseUnitWriteIntent)
		if !serviceIdentityPathMatchesState(written.PrimaryUnit, intent.PrimaryUnitIntent) {
			return fmt.Errorf("completed unit write does not match its durable intent")
		}
	}
	return nil
}

func (s *Server) validateServiceIdentityRecoveryStorage(contents serviceIdentityJournalContents, observed *db.Service, unitPath string) error {
	header := contents.Header
	previousRoot := serviceRootFromConfig(s.cfg, *observed)
	targetRoot := serviceRootFromConfig(s.cfg, *header.TargetService)
	if err := validateServiceIdentityRecoveryRootPaths(header, previousRoot, targetRoot); err != nil {
		return err
	}
	if err := validateServiceIdentityRecoveryGenerationPlan(contents, targetRoot, unitPath); err != nil {
		return err
	}
	if header.PreviousDataset != observed.ServiceRootZFS || header.TargetDataset != header.TargetService.ServiceRootZFS {
		return fmt.Errorf("dataset paths do not match database records")
	}
	return validateServiceIdentityRecoveryRootPlan(header, previousRoot, targetRoot)
}

func validateServiceIdentityRecoveryRootPaths(header serviceIdentityJournalHeader, previousRoot, targetRoot string) error {
	if filepath.Clean(header.PreviousRoot) != filepath.Clean(previousRoot) {
		return fmt.Errorf("previous root %q does not match database root %q", header.PreviousRoot, previousRoot)
	}
	if filepath.Clean(header.TargetRoot) != filepath.Clean(targetRoot) || filepath.Clean(header.Root) != filepath.Clean(targetRoot) {
		return fmt.Errorf("target root paths do not match database root %q", targetRoot)
	}
	return nil
}

func validateServiceIdentityRecoveryRootPlan(header serviceIdentityJournalHeader, previousRoot, targetRoot string) error {
	if header.RootPlan == nil {
		if filepath.Clean(previousRoot) != filepath.Clean(targetRoot) || header.PreviousDataset != header.TargetDataset || header.TargetDatasetCreate {
			return fmt.Errorf("journal changes service storage without a root plan")
		}
		return nil
	}
	plan := header.RootPlan
	if !serviceIdentityRecoveryRootPlanMatches(header, previousRoot, targetRoot) {
		return fmt.Errorf("root plan does not match journal storage paths")
	}
	if plan.NewRootExisted != (len(plan.NewRootState) != 0) {
		return fmt.Errorf("root plan target provenance is incomplete")
	}
	for _, state := range plan.NewRootState {
		if err := validateServiceIdentityJournalRecordPath(state.Path); err != nil {
			return fmt.Errorf("root plan target state: %w", err)
		}
	}
	return nil
}

func serviceIdentityRecoveryRootPlanMatches(header serviceIdentityJournalHeader, previousRoot, targetRoot string) bool {
	plan := header.RootPlan
	return plan.ServiceName == header.Service && filepath.Clean(plan.OldRoot) == filepath.Clean(previousRoot) &&
		filepath.Clean(plan.NewRoot) == filepath.Clean(targetRoot) && plan.OldRootZFS == header.PreviousDataset &&
		plan.NewRootZFS == header.TargetDataset && plan.CreateNewRootZFS == header.TargetDatasetCreate
}

func validateServiceIdentityRecoveryGenerationPlan(contents serviceIdentityJournalContents, targetRoot, unitPath string) error {
	header := contents.Header
	if !serviceIdentityRecoveryGenerationPlanRequired(contents) {
		return nil
	}
	expectedPaths, targetUnits, err := serviceIdentityExpectedGenerationTargets(header.TargetService, targetRoot, unitPath)
	if err != nil {
		return err
	}
	actualBackupPaths := serviceIdentityRecoveryBackupPaths(header.GenerationBackups)
	if !slices.Equal(actualBackupPaths, serviceIdentityExpectedBackupPaths(expectedPaths, unitPath)) {
		return fmt.Errorf("generation backup paths do not match the target service install plan")
	}
	if err := validateServiceIdentityRecoveryStagedPaths(contents, expectedPaths); err != nil {
		return err
	}
	return validateServiceIdentityRecoveryGenerationUnits(header, targetUnits)
}

func serviceIdentityRecoveryGenerationPlanRequired(contents serviceIdentityJournalContents) bool {
	header := contents.Header
	return serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationBackup) ||
		serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationStage) ||
		serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationActivate) ||
		serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationEnabled) ||
		len(header.GenerationBackups) != 0 || len(header.GenerationUnits) != 0
}

func serviceIdentityRecoveryBackupPaths(backups []serviceIdentityGenerationBackup) []string {
	paths := make([]string, len(backups))
	for index, backup := range backups {
		paths[index] = filepath.Clean(backup.Path)
	}
	return paths
}

func validateServiceIdentityRecoveryStagedPaths(contents serviceIdentityJournalContents, expected []string) error {
	staged, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationStage)
	if !ok {
		return nil
	}
	actual := make([]string, len(staged.GenerationPaths))
	for index, proof := range staged.GenerationPaths {
		actual[index] = filepath.Clean(proof.Path)
	}
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("staged generation paths do not match the target service install plan")
	}
	return nil
}

func validateServiceIdentityRecoveryGenerationUnits(header serviceIdentityJournalHeader, targetUnits []string) error {
	expected := serviceIdentityGenerationUnitPlan(header.PreviousService, header.Service, targetUnits)
	actual := make([]string, len(header.GenerationUnits))
	targetSet := make(map[string]struct{}, len(targetUnits))
	for _, unit := range targetUnits {
		targetSet[unit] = struct{}{}
	}
	for index, state := range header.GenerationUnits {
		actual[index] = state.Unit
		_, wantTargetEnabled := targetSet[state.Unit]
		if state.TargetEnabled != wantTargetEnabled {
			return fmt.Errorf("generation unit %s target enablement does not match the target service install plan", state.Unit)
		}
	}
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("generation units do not match the target service install plan")
	}
	return nil
}

func (s *Server) validateServiceIdentityRecoveryCurrentDatabase(header serviceIdentityJournalHeader, observed *db.Service, predecessorPresent bool) error {
	current, err := s.serviceView(header.Service)
	if errors.Is(err, errServiceNotFound) && !predecessorPresent {
		return nil
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.AsStruct(), observed) && !reflect.DeepEqual(current.AsStruct(), header.TargetService) {
		return fmt.Errorf("current database record matches neither captured old nor target state")
	}
	return nil
}

func (s *Server) validateServiceIdentityRecoveryContents(contents serviceIdentityJournalContents) error {
	if err := validateServiceIdentityRecoveryGenerationContents(contents); err != nil {
		return err
	}
	if err := validateServiceIdentityRecoveryMaterialization(contents); err != nil {
		return err
	}
	if err := validateServiceIdentityRecoveryRuntimeContents(contents); err != nil {
		return err
	}
	return s.validateCompletedServiceIdentityRecovery(contents)
}

func validateServiceIdentityRecoveryGenerationContents(contents serviceIdentityJournalContents) error {
	phases := serviceIdentityRecoveryGenerationPhases{
		backupStarted:  serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationBackup),
		backupComplete: serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationBackedUp),
		stageStarted:   serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationStageIntent),
		staged:         serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationStage),
		activated:      serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationActivate),
		enabled:        serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseGenerationEnabled),
	}
	if err := validateServiceIdentityRecoveryGenerationOrder(phases); err != nil {
		return err
	}
	if err := validateServiceIdentityRecoveryGenerationBackup(contents, phases); err != nil {
		return err
	}
	return validateServiceIdentityRecoveryGenerationStage(contents, phases)
}

type serviceIdentityRecoveryGenerationPhases struct {
	backupStarted  bool
	backupComplete bool
	stageStarted   bool
	staged         bool
	activated      bool
	enabled        bool
}

func validateServiceIdentityRecoveryGenerationOrder(phases serviceIdentityRecoveryGenerationPhases) error {
	violations := []struct {
		invalid bool
		message string
	}{
		{phases.backupComplete && !phases.backupStarted, "completed generation backup has no durable start phase"},
		{phases.staged && !phases.backupComplete, "staged generation has no durable completed backup phase"},
		{phases.stageStarted && !phases.backupComplete, "generation staging started without a durable completed backup phase"},
		{phases.staged && !phases.stageStarted, "staged generation has no durable start phase"},
		{phases.activated && !phases.staged, "generation activation has no durable staged phase"},
		{phases.enabled && !phases.activated, "enabled generation has no durable activation phase"},
	}
	for _, violation := range violations {
		if violation.invalid {
			return fmt.Errorf("%s", violation.message)
		}
	}
	return nil
}

func validateServiceIdentityRecoveryGenerationBackup(contents serviceIdentityJournalContents, phases serviceIdentityRecoveryGenerationPhases) error {
	header := contents.Header
	if phases.backupStarted && !phases.backupComplete {
		if err := validateIncompleteServiceIdentityGeneration(header.GenerationBackups); err != nil {
			return err
		}
	}
	if phases.backupComplete {
		phase, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationBackedUp)
		if err := validateServiceIdentityGenerationBackupPhase(header.GenerationBackups, phase.GenerationBackups); err != nil {
			return err
		}
	}
	return nil
}

func validateServiceIdentityRecoveryGenerationStage(contents serviceIdentityJournalContents, phases serviceIdentityRecoveryGenerationPhases) error {
	header := contents.Header
	if phases.stageStarted {
		phase, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationStageIntent)
		if err := validateServiceIdentityGenerationIntentPhase(header, phase.GenerationIntents); err != nil {
			return err
		}
	}
	if phases.staged {
		phase, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationStage)
		if err := validateServiceIdentityGenerationStagePhase(header, phase.GenerationPaths); err != nil {
			return err
		}
		return validateServiceIdentityRecoveryStagedIntents(contents, phase.GenerationPaths)
	}
	return nil
}

func validateServiceIdentityRecoveryStagedIntents(contents serviceIdentityJournalContents, proofs []serviceIdentityPathProof) error {
	intent, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationStageIntent)
	for _, state := range intent.GenerationIntents {
		proof, ok := serviceIdentityProofForPath(proofs, state.Path)
		if !ok || !serviceIdentityPathMatchesState(proof, state) {
			return fmt.Errorf("staged generation path %s does not match its durable intent", state.Path)
		}
	}
	return nil
}

func validateServiceIdentityRecoveryMaterialization(contents serviceIdentityJournalContents) error {
	header := contents.Header
	intent := serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseMaterializeIntent)
	creation, created := serviceIdentityJournalPhase(contents, serviceIdentityPhaseMaterializeCreated)
	publish, published := serviceIdentityJournalPhase(contents, serviceIdentityPhaseMaterializePublish)
	materialization, materialized := serviceIdentityJournalPhase(contents, serviceIdentityPhaseMaterialize)
	finalMaterialization, finalized := serviceIdentityJournalPhase(contents, serviceIdentityPhaseMaterializeFinal)
	if materialized && (!intent || header.RootPlan == nil) {
		return fmt.Errorf("materialized target has no matching durable root-plan intent")
	}
	if intent && header.RootPlan == nil {
		return fmt.Errorf("materialization intent has no root plan")
	}
	if err := validateServiceIdentityRecoveryCreation(header, intent, creation, created); err != nil {
		return err
	}
	if err := validateServiceIdentityRecoveryPublish(header, creation, created, publish, published); err != nil {
		return err
	}
	return validateServiceIdentityRecoveryMaterializedRoot(header, materialization, materialized, published, finalMaterialization, finalized)
}

func validateServiceIdentityRecoveryCreation(header serviceIdentityJournalHeader, intent bool, creation serviceIdentityPhaseRecord, created bool) error {
	if !created {
		return nil
	}
	if !serviceIdentityCreationMatchesIntent(header, intent, creation) {
		return fmt.Errorf("created target root has no matching durable creation intent")
	}
	if !serviceIdentityCreationDatasetProofMatches(header, creation) {
		return fmt.Errorf("created target root dataset provenance is incomplete")
	}
	if !serviceIdentityCreationStageMatches(header, creation) {
		return fmt.Errorf("created target root stage path does not match the transaction")
	}
	return nil
}

func serviceIdentityCreationMatchesIntent(header serviceIdentityJournalHeader, intent bool, creation serviceIdentityPhaseRecord) bool {
	return intent && header.RootPlan != nil && serviceIdentityCreationHasRootProof(creation)
}

func serviceIdentityCreationDatasetProofMatches(header serviceIdentityJournalHeader, creation serviceIdentityPhaseRecord) bool {
	return creation.DatasetCreated == header.RootPlan.CreateNewRootZFS && creation.DatasetCreated == (creation.DatasetGUID != "")
}

func serviceIdentityCreationStageMatches(header serviceIdentityJournalHeader, creation serviceIdentityPhaseRecord) bool {
	want := ""
	if !creation.DatasetCreated {
		want = filepath.Join(filepath.Dir(header.RootPlan.NewRoot), ".yeet-service-root-"+header.ID)
	}
	return filepath.Clean(creation.StagePath) == filepath.Clean(want)
}

func serviceIdentityCreationHasRootProof(creation serviceIdentityPhaseRecord) bool {
	return creation.RootDev != 0 && creation.RootIno != 0 && creation.RootCreated
}

func validateServiceIdentityRecoveryPublish(header serviceIdentityJournalHeader, creation serviceIdentityPhaseRecord, created bool, publish serviceIdentityPhaseRecord, published bool) error {
	if !published {
		return nil
	}
	if !serviceIdentityPublishHasFilesystemCreation(header, creation, created, publish) {
		return fmt.Errorf("root publish proof does not match its durable creation record")
	}
	if !serviceIdentityPhaseHasInventoryProof(publish) || !serviceIdentityPublishMatchesCreation(publish, creation) {
		return fmt.Errorf("root publish proof does not match its durable creation record")
	}
	return nil
}

func serviceIdentityPublishHasFilesystemCreation(header serviceIdentityJournalHeader, creation serviceIdentityPhaseRecord, created bool, publish serviceIdentityPhaseRecord) bool {
	return created && !creation.DatasetCreated && header.RootPlan != nil && !publish.DatasetCreated
}

func serviceIdentityPublishMatchesCreation(publish, creation serviceIdentityPhaseRecord) bool {
	return publish.RootCreated && publish.RootDev == creation.RootDev && publish.RootIno == creation.RootIno &&
		filepath.Clean(publish.StagePath) == filepath.Clean(creation.StagePath)
}

func validateServiceIdentityRecoveryMaterializedRoot(
	header serviceIdentityJournalHeader,
	materialization serviceIdentityPhaseRecord,
	materialized bool,
	published bool,
	final serviceIdentityPhaseRecord,
	finalized bool,
) error {
	if !materialized {
		return validateServiceIdentityAbsentMaterialization(finalized)
	}
	if err := validateServiceIdentityMaterializedRootProof(header, materialization, published); err != nil {
		return err
	}
	return validateServiceIdentityFinalMaterialization(materialization, final, finalized)
}

func validateServiceIdentityAbsentMaterialization(finalized bool) error {
	if finalized {
		return fmt.Errorf("final target provenance has no materialized root")
	}
	return nil
}

func validateServiceIdentityMaterializedRootProof(header serviceIdentityJournalHeader, materialization serviceIdentityPhaseRecord, published bool) error {
	if !serviceIdentityPhaseHasInventoryProof(materialization) {
		return fmt.Errorf("materialized target lacks inode and inventory proof")
	}
	if !serviceIdentityMaterializationPlanMatches(header, materialization) {
		return fmt.Errorf("materialized target proof does not match root-plan provenance")
	}
	if !serviceIdentityMaterializationDatasetProofMatches(materialization) {
		return fmt.Errorf("materialized target dataset proof is incomplete")
	}
	if serviceIdentityMaterializationNeedsPublish(materialization, published) {
		return fmt.Errorf("materialized filesystem target has no durable publish proof")
	}
	return nil
}

func serviceIdentityMaterializationPlanMatches(header serviceIdentityJournalHeader, materialization serviceIdentityPhaseRecord) bool {
	return materialization.DatasetCreated == header.RootPlan.CreateNewRootZFS && materialization.RootCreated == !header.RootPlan.NewRootExisted
}

func serviceIdentityMaterializationDatasetProofMatches(materialization serviceIdentityPhaseRecord) bool {
	return materialization.DatasetCreated == (materialization.DatasetGUID != "")
}

func serviceIdentityMaterializationNeedsPublish(materialization serviceIdentityPhaseRecord, published bool) bool {
	return !materialization.DatasetCreated && !published
}

func validateServiceIdentityFinalMaterialization(materialization, final serviceIdentityPhaseRecord, finalized bool) error {
	if finalized && (!serviceIdentityPhaseHasInventoryProof(final) || serviceIdentityMaterializationIdentity(materialization) != serviceIdentityMaterializationIdentity(final)) {
		return fmt.Errorf("final target provenance does not match the materialized root identity")
	}
	return nil
}

func serviceIdentityPhaseHasInventoryProof(phase serviceIdentityPhaseRecord) bool {
	return phase.RootDev != 0 && phase.RootIno != 0 && phase.InventoryDigest != "" && phase.InventoryCount > 0
}

type serviceIdentityMaterializationIdentityProof struct {
	rootDev        uint64
	rootIno        uint64
	datasetCreated bool
	datasetGUID    string
	rootCreated    bool
}

func serviceIdentityMaterializationIdentity(phase serviceIdentityPhaseRecord) serviceIdentityMaterializationIdentityProof {
	return serviceIdentityMaterializationIdentityProof{
		rootDev: phase.RootDev, rootIno: phase.RootIno, datasetCreated: phase.DatasetCreated,
		datasetGUID: phase.DatasetGUID, rootCreated: phase.RootCreated,
	}
}

func validateServiceIdentityRecoveryRuntimeContents(contents serviceIdentityJournalContents) error {
	runtimeStarted := serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseRuntimeBackup)
	runtimeComplete := serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseRuntimeBackedUp)
	runtimePlanned := serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseRuntimePlan)
	if runtimeStarted && !runtimePlanned {
		return fmt.Errorf("legacy runtime backup started without a durable plan")
	}
	if runtimeComplete && !runtimeStarted {
		return fmt.Errorf("completed runtime backup has no durable start phase")
	}
	if runtimeComplete {
		plan, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseRuntimePlan)
		phase, _ := serviceIdentityJournalPhase(contents, serviceIdentityPhaseRuntimeBackedUp)
		if err := validateServiceIdentityGenerationBackupPhase(plan.RuntimeBackups, phase.RuntimeBackups); err != nil {
			return fmt.Errorf("legacy runtime backup: %w", err)
		}
	}
	unitWriteStarted := serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseUnitWriteIntent)
	unitWritten := serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseUnitWrite)
	if unitWritten && !unitWriteStarted {
		return fmt.Errorf("completed unit write has no durable start phase")
	}
	return nil
}

func (s *Server) validateCompletedServiceIdentityRecovery(contents serviceIdentityJournalContents) error {
	header := contents.Header
	if serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseComplete) {
		if !serviceIdentityJournalHasPhase(contents, serviceIdentityPhaseDBCommit) {
			return fmt.Errorf("complete migration has no durable database commit")
		}
		current, err := s.serviceView(header.Service)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current.AsStruct(), header.TargetService) {
			return fmt.Errorf("complete migration database does not match the target record")
		}
	}
	return nil
}

func validateServiceIdentityGenerationIntentPhase(header serviceIdentityJournalHeader, states []serviceIdentityPathState) error {
	seen := make(map[string]struct{}, len(states))
	for _, state := range states {
		path := filepath.Clean(state.Path)
		if path == filepath.Clean(header.PreviousUnitPath) {
			return fmt.Errorf("generation intent must not include the separately journaled primary unit")
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("duplicate generation intent path %q", path)
		}
		seen[path] = struct{}{}
		if err := validateServiceIdentityPathState(state, path); err != nil {
			return err
		}
	}
	if len(header.GenerationBackups) != len(states) {
		return fmt.Errorf("generation intent count does not match the auxiliary install plan")
	}
	for index, backup := range header.GenerationBackups {
		if filepath.Clean(states[index].Path) != filepath.Clean(backup.Path) {
			return fmt.Errorf("generation intent paths do not match the auxiliary install plan")
		}
	}
	return nil
}

func serviceIdentityJournalPrimaryProof(contents serviceIdentityJournalContents) serviceIdentityPathProof {
	if phase, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseUnitWrite); ok {
		return phase.PrimaryUnit
	}
	if phase, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseUnitWriteIntent); ok {
		return phase.PrimaryUnit
	}
	return contents.Header.PreviousUnitProof
}

func serviceIdentityJournalPrimaryIntent(contents serviceIdentityJournalContents) serviceIdentityPathState {
	if phase, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseUnitWriteIntent); ok {
		return phase.PrimaryUnitIntent
	}
	return serviceIdentityPathState{}
}

func restoreServiceIdentityRecoveryUnit(contents serviceIdentityJournalContents) error {
	header := contents.Header
	if header.PreviousUnitProof.Path != "" {
		return restoreServiceIdentityPrimaryUnit(
			header.PreviousUnitProof, serviceIdentityJournalPrimaryProof(contents),
			serviceIdentityJournalPrimaryIntent(contents), []byte(header.PreviousUnit),
		)
	}
	return fmt.Errorf("identity migration journal has no exact primary unit provenance")
}

func restoreServiceIdentityRecoveryDatabase(s *Server, header serviceIdentityJournalHeader) error {
	observed := serviceIdentityJournalObservedService(header)
	predecessorPresent := serviceIdentityJournalPredecessorPresent(header)
	if observed == nil || header.TargetService == nil {
		return fmt.Errorf("journal does not contain exact observed and target database records")
	}
	_, err := s.cfg.DB.MutateData(func(data *db.Data) error {
		return restoreServiceIdentityRecoveryDatabaseRecord(data, header, observed, predecessorPresent)
	})
	return err
}

func restoreServiceIdentityRecoveryDatabaseRecord(data *db.Data, header serviceIdentityJournalHeader, observed *db.Service, predecessorPresent bool) error {
	current, ok := data.Services[header.Service]
	if !predecessorPresent {
		if !ok {
			return nil
		}
		if !reflect.DeepEqual(current, observed) && !reflect.DeepEqual(current, header.TargetService) {
			return fmt.Errorf("service %q does not match the exact observed or provisional target database record", header.Service)
		}
		delete(data.Services, header.Service)
		return nil
	}
	if !ok {
		return fmt.Errorf("service %q disappeared during recovery", header.Service)
	}
	switch {
	case reflect.DeepEqual(current, header.PreviousService):
		return nil
	case reflect.DeepEqual(current, header.TargetService):
		data.Services[header.Service] = header.PreviousService.Clone()
		return nil
	default:
		return fmt.Errorf("service %q does not match the exact old or provisional target database record", header.Service)
	}
}

func restoreServiceIdentityRecoveryRunningState(ctx context.Context, ops serviceIdentityMigrationOps, header serviceIdentityJournalHeader) error {
	return ops.restoreRuntime(ctx, header.Service, serviceIdentityJournalRuntimeState(header))
}

func verifyServiceIdentityRecoveryState(ctx context.Context, s *Server, ops serviceIdentityMigrationOps, header serviceIdentityJournalHeader) error {
	if err := verifyServiceIdentityRecoveryDatabase(s, header); err != nil {
		return err
	}
	if err := verifyServiceIdentityRecoveryUnit(header); err != nil {
		return err
	}
	return verifyServiceIdentityRecoveryRuntime(ctx, ops, header)
}

func verifyServiceIdentityRecoveryDatabase(s *Server, header serviceIdentityJournalHeader) error {
	sv, err := s.serviceView(header.Service)
	if !serviceIdentityJournalPredecessorPresent(header) {
		if err == nil {
			return fmt.Errorf("database service should be absent after recovery, found %#v", sv.AsStruct())
		}
		if !errors.Is(err, errServiceNotFound) {
			return fmt.Errorf("database service should be absent after recovery: %w", err)
		}
	} else {
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(sv.AsStruct(), header.PreviousService) {
			return fmt.Errorf("database service does not match the previous record")
		}
	}
	return nil
}

func verifyServiceIdentityRecoveryUnit(header serviceIdentityJournalHeader) error {
	path := header.PreviousUnitPath
	if path == "" {
		path = filepath.Join(systemdSystemDir, header.Service+".service")
	}
	raw, present, mode, err := readOptionalServiceIdentityUnit(path)
	if err != nil {
		return err
	}
	if present != header.PreviousUnitPresent || present && string(raw) != header.PreviousUnit {
		return fmt.Errorf("primary unit bytes/existence do not match the previous state")
	}
	if present && header.PreviousUnitMode.Perm() != 0 && mode.Perm() != header.PreviousUnitMode.Perm() {
		return fmt.Errorf("primary unit mode is %o, want %o", mode.Perm(), header.PreviousUnitMode.Perm())
	}
	return nil
}

func verifyServiceIdentityRecoveryRuntime(ctx context.Context, ops serviceIdentityMigrationOps, header serviceIdentityJournalHeader) error {
	runtimeState, err := ops.captureRuntime(ctx, header.Service)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(runtimeState, serviceIdentityJournalRuntimeState(header)) {
		return fmt.Errorf("runtime state is %#v, want %#v", runtimeState, serviceIdentityJournalRuntimeState(header))
	}
	return nil
}

func serviceIdentityJournalRuntimeState(header serviceIdentityJournalHeader) []serviceIdentityRuntimeUnitState {
	return append([]serviceIdentityRuntimeUnitState(nil), header.PreviousRuntimeUnits...)
}

func serviceIdentityJournalObservedService(header serviceIdentityJournalHeader) *db.Service {
	if header.ObservedService != nil {
		return header.ObservedService
	}
	return header.PreviousService
}

func serviceIdentityJournalPredecessorPresent(header serviceIdentityJournalHeader) bool {
	return header.PreviousServicePresent || header.PreviousService != nil
}

func serviceIdentityJournalHasPhase(contents serviceIdentityJournalContents, phase string) bool {
	_, ok := serviceIdentityJournalPhase(contents, phase)
	return ok
}

func serviceIdentityJournalGenerationBackups(contents serviceIdentityJournalContents) []serviceIdentityGenerationBackup {
	if phase, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseGenerationBackedUp); ok {
		return phase.GenerationBackups
	}
	return contents.Header.GenerationBackups
}

func serviceIdentityJournalRuntimeBackups(contents serviceIdentityJournalContents) []serviceIdentityGenerationBackup {
	if phase, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseRuntimeBackedUp); ok {
		return phase.RuntimeBackups
	}
	if phase, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseRuntimePlan); ok {
		return phase.RuntimeBackups
	}
	return nil
}

func validateServiceIdentityGenerationBackupPhase(planned, actual []serviceIdentityGenerationBackup) error {
	if len(planned) != len(actual) {
		return fmt.Errorf("completed generation backup count does not match the journal header")
	}
	for index := range planned {
		if err := validateServiceIdentityGenerationBackupRecord(index, planned[index], actual[index]); err != nil {
			return err
		}
	}
	return nil
}

func validateServiceIdentityGenerationBackupRecord(index int, want, got serviceIdentityGenerationBackup) error {
	if want.Path != got.Path || want.BackupPath != got.BackupPath || want.Present != got.Present ||
		!reflect.DeepEqual(want.Original, got.Original) {
		return fmt.Errorf("completed generation backup %d does not match the journal header", index)
	}
	if !got.Present {
		if got.Backup.Present {
			return fmt.Errorf("completed generation backup %s exists for an originally absent path", got.Path)
		}
		return nil
	}
	if !got.Backup.Present {
		return fmt.Errorf("completed generation backup %s lacks exact backup provenance", got.Path)
	}
	if err := validateServiceIdentityPathProofRecord(got.Backup, got.BackupPath); err != nil {
		return fmt.Errorf("completed generation backup %s: %w", got.Path, err)
	}
	if !serviceIdentityPathPayloadEqual(got.Backup, got.Original) {
		return fmt.Errorf("completed generation backup %s does not preserve original content and metadata", got.Path)
	}
	return nil
}

func validateServiceIdentityGenerationStagePhase(header serviceIdentityJournalHeader, proofs []serviceIdentityPathProof) error {
	seen := make(map[string]struct{}, len(proofs))
	for _, proof := range proofs {
		path := filepath.Clean(proof.Path)
		if !filepath.IsAbs(path) {
			return fmt.Errorf("invalid staged generation path %q", proof.Path)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("duplicate staged generation path %q", path)
		}
		seen[path] = struct{}{}
		if err := validateServiceIdentityPathProofRecord(proof, path); err != nil {
			return fmt.Errorf("staged generation path %s: %w", path, err)
		}
	}
	if len(header.GenerationBackups) != 0 && len(proofs) == 0 {
		return fmt.Errorf("staged generation has no path provenance")
	}
	return nil
}

func serviceIdentityJournalPhase(contents serviceIdentityJournalContents, phase string) (serviceIdentityPhaseRecord, bool) {
	for i := len(contents.Phases) - 1; i >= 0; i-- {
		if contents.Phases[i].Phase == phase {
			return contents.Phases[i], true
		}
	}
	return serviceIdentityPhaseRecord{}, false
}

func serviceIdentityJournalTargetRoot(header serviceIdentityJournalHeader) string {
	if header.TargetRoot != "" {
		return header.TargetRoot
	}
	return header.Root
}

func serviceIdentityRecoveryError(contents serviceIdentityJournalContents, path, action string, cause error) error {
	snapshot := ""
	if phase, ok := serviceIdentityJournalPhase(contents, serviceIdentityPhaseSnapshot); ok {
		snapshot = phase.ZFSSnapshot
	}
	if snapshot == "" {
		for _, phase := range contents.Phases {
			if phase.ZFSSnapshot != "" {
				snapshot = phase.ZFSSnapshot
			}
		}
	}
	return fmt.Errorf("%w for %q: %s failed: %v; journal: %s; snapshot: %s", errServiceIdentityRecoveryBlocked, contents.Header.Service, action, cause, path, snapshot)
}

func (s *Server) setServiceIdentityMutationBlock(service string, err error) {
	s.serviceIdentityRecoveryMu.Lock()
	defer s.serviceIdentityRecoveryMu.Unlock()
	if s.serviceIdentityMutationBlocks == nil {
		s.serviceIdentityMutationBlocks = make(map[string]error)
	}
	if !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		err = fmt.Errorf("%w for %q: %v", errServiceIdentityRecoveryBlocked, service, err)
	}
	s.serviceIdentityMutationBlocks[service] = err
}

func (s *Server) clearServiceIdentityMutationBlock(service string) {
	s.serviceIdentityRecoveryMu.Lock()
	defer s.serviceIdentityRecoveryMu.Unlock()
	delete(s.serviceIdentityMutationBlocks, service)
}

func (s *Server) checkServiceIdentityMutationAllowed(service string) error {
	s.serviceIdentityRecoveryMu.RLock()
	defer s.serviceIdentityRecoveryMu.RUnlock()
	if s.serviceIdentityGlobalMutationBlock != nil {
		return s.serviceIdentityGlobalMutationBlock
	}
	if err := s.serviceIdentityMutationBlocks[service]; err != nil {
		return err
	}
	return nil
}

func (s *Server) setServiceIdentityGlobalMutationBlock(err error) {
	s.serviceIdentityRecoveryMu.Lock()
	defer s.serviceIdentityRecoveryMu.Unlock()
	s.serviceIdentityGlobalMutationBlock = err
}

func (s *Server) checkServiceIdentityMutationsAllowed(services []string) error {
	s.serviceIdentityRecoveryMu.RLock()
	defer s.serviceIdentityRecoveryMu.RUnlock()
	if s.serviceIdentityGlobalMutationBlock != nil {
		return s.serviceIdentityGlobalMutationBlock
	}
	for _, service := range uniqueSortedServiceOperationNames(services) {
		if err := s.serviceIdentityMutationBlocks[service]; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) serviceIdentityJournalFilenameService(path string) (string, bool) {
	dv, err := s.cfg.DB.Get()
	if err != nil || !dv.Valid() {
		return "", false
	}
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	services := make([]string, 0)
	for service := range dv.Services().All() {
		services = append(services, service)
	}
	slices.SortFunc(services, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	for _, service := range services {
		id, ok := strings.CutPrefix(base, service+"-")
		if ok && validateServiceIdentityJournalID(id) == nil {
			return service, true
		}
	}
	return "", false
}
