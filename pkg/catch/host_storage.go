// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/copyutil"
	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

const managedHostStorageDataDir = "/var/lib/yeet"

var (
	hostStorageMountPointsFn        = readHostStorageMountPoints
	hostStorageFreeBytesFn          = hostStorageFreeBytes
	hostStorageCleanupDeviceFn      = hostStorageCleanupDevice
	hostStorageRemoveSourceTreeFn   = removeHostStorageSourceTree
	hostStorageFinalizeOperationsFn = func(Config) hostStorageApplyOperations { return hostStorageApplyOperations{} }
)

type hostStoragePlanner struct {
	config Config
	store  *db.Store
	zfs    zfsCommandRunner
}

type resolvedHostStorageTarget struct {
	value              string
	zfs                bool
	dataset            string
	datasetsToCreate   []string
	mountpointWarnings []string
}

func loadTargetHostStorageTransaction(targetRoot, id string) (*hostStorageTransaction, error) {
	currentJournal, targetRoot, id, err := targetHostStorageTransactionJournal(targetRoot, id)
	if err != nil {
		return nil, err
	}
	current, err := loadHostStorageTransaction(currentJournal)
	if err != nil {
		return nil, err
	}
	if err := validateTargetHostStorageTransaction(current, currentJournal, targetRoot, id); err != nil {
		return nil, err
	}
	if current.TargetJournal == "" {
		return current, nil
	}
	authoritative, _, err := loadAuthoritativeHostStorageTransaction(currentJournal)
	if err != nil {
		return nil, err
	}
	if authoritative.ID != id {
		return nil, staleTargetHostStorageTransactionError(targetRoot, id)
	}
	return authoritative, nil
}

func targetHostStorageTransactionJournal(targetRoot, id string) (string, string, string, error) {
	targetRoot = cleanHostStoragePath(targetRoot)
	id = strings.TrimSpace(id)
	if targetRoot == "" || !filepath.IsAbs(targetRoot) || !validHostStorageTransactionID(id) {
		return "", targetRoot, id, staleTargetHostStorageTransactionError(targetRoot, id)
	}
	currentJournal := hostStorageTransactionPath(targetRoot, id)
	exists, err := hostStorageTransactionJournalExists(currentJournal)
	if err != nil {
		return "", targetRoot, id, fmt.Errorf("inspect target host storage transaction %q: %w", id, err)
	}
	if !exists {
		return "", targetRoot, id, staleTargetHostStorageTransactionError(targetRoot, id)
	}
	return currentJournal, targetRoot, id, nil
}

func validateTargetHostStorageTransaction(tx *hostStorageTransaction, journal, targetRoot, id string) error {
	if tx.ID != id || !hostStoragePathsEqual(tx.Plan.Desired.DataDir, targetRoot) {
		return staleTargetHostStorageTransactionError(targetRoot, id)
	}
	if tx.TargetJournal == "" && !hostStoragePathsEqual(tx.SourceJournal, journal) {
		return staleTargetHostStorageTransactionError(targetRoot, id)
	}
	if tx.TargetJournal != "" && !hostStoragePathsEqual(tx.TargetJournal, journal) {
		return staleTargetHostStorageTransactionError(targetRoot, id)
	}
	return nil
}

func staleTargetHostStorageTransactionError(targetRoot, id string) error {
	return fmt.Errorf("stale host storage transaction %q for target %q", id, targetRoot)
}

func completeHostStorageCleanupTransaction(tx *hostStorageTransaction) error {
	if tx == nil || strings.TrimSpace(tx.TargetJournal) == "" {
		return fmt.Errorf("host storage cleanup requires a target transaction journal")
	}
	previousPhase := tx.Phase
	previousAuthority := tx.CatchAuthority
	tx.Phase = hostStoragePhaseComplete
	tx.CatchAuthority = hostStorageCatchAuthorityTarget
	if err := writeHostStorageTransactionFile(tx.TargetJournal, tx); err != nil {
		tx.Phase = previousPhase
		tx.CatchAuthority = previousAuthority
		return fmt.Errorf("record completed host storage cleanup: %w", err)
	}
	return nil
}

func (s *Server) FinalizeHostStorage(ctx context.Context, req catchrpc.HostStorageFinalizeRequest) (catchrpc.HostStorageFinalizeResult, error) {
	s.hostStorageMutationMu.Lock()
	defer s.hostStorageMutationMu.Unlock()
	tx, err := loadTargetHostStorageTransaction(s.cfg.RootDir, req.TransactionID)
	if err != nil {
		return catchrpc.HostStorageFinalizeResult{}, err
	}
	return s.finalizeHostStorageTransaction(ctx, tx)
}

func (s *Server) finalizeHostStorageTransaction(ctx context.Context, tx *hostStorageTransaction) (catchrpc.HostStorageFinalizeResult, error) {
	result := catchrpc.HostStorageFinalizeResult{TransactionID: tx.ID}
	if tx.Phase == hostStoragePhaseComplete {
		s.clearFinalizedHostStorageRecovery(tx)
		return result, nil
	}
	if !finalizableHostStorageTransactionPhase(tx.Phase) {
		return result, fmt.Errorf("stale host storage transaction %q is in phase %q, want catch-switched or validated", tx.ID, tx.Phase)
	}
	validation, validateErr := s.validateFinalHostStorageTransaction(ctx, tx)
	result.Validation = catchrpc.HostStorageValidation{
		ActiveRefs:   validation.ActiveRefs,
		DatabaseRefs: validation.DBRefs,
		SystemdRefs:  validation.SystemdRefs,
	}
	if validateErr != nil {
		return result, s.finalHostStorageValidationError(ctx, tx, validateErr)
	}
	return s.advanceFinalizedHostStorageTransaction(tx, result)
}

func finalizableHostStorageTransactionPhase(phase hostStorageTransactionPhase) bool {
	return phase == hostStoragePhaseCatchSwitched || phase == hostStoragePhaseValidated || phase == hostStoragePhaseCleanupPending
}

func (s *Server) finalHostStorageValidationError(ctx context.Context, tx *hostStorageTransaction, validationErr error) error {
	if tx.Phase == hostStoragePhaseCatchSwitched {
		rollbackErr := s.rollbackFinalHostStorageTransaction(ctx, tx, validationErr)
		return fmt.Errorf("final host storage validation failed: %v; rollback result: %w", validationErr, rollbackErr)
	}
	return fmt.Errorf("final host storage validation failed after target validation: %w", validationErr)
}

func (s *Server) advanceFinalizedHostStorageTransaction(tx *hostStorageTransaction, result catchrpc.HostStorageFinalizeResult) (catchrpc.HostStorageFinalizeResult, error) {
	if tx.Phase == hostStoragePhaseValidated {
		s.clearFinalizedHostStorageRecovery(tx)
		return result, nil
	}
	if tx.Phase == hostStoragePhaseCleanupPending {
		result.CleanupPending = true
		s.clearFinalizedHostStorageRecovery(tx)
		return result, nil
	}
	next := finalizedHostStorageTransactionPhase(tx)
	if err := advanceHostStorageTransaction(tx, next); err != nil {
		return result, fmt.Errorf("record finalized host storage transaction: %w", err)
	}
	result.CleanupPending = next == hostStoragePhaseCleanupPending
	s.clearFinalizedHostStorageRecovery(tx)
	return result, nil
}

func (s *Server) clearFinalizedHostStorageRecovery(tx *hostStorageTransaction) {
	if s.hostStorageRecovery != nil && s.hostStorageRecovery.ID == tx.ID {
		s.hostStorageRecovery = nil
		s.hostStorageMutationBlock = nil
	}
}

func finalizedHostStorageTransactionPhase(tx *hostStorageTransaction) hostStorageTransactionPhase {
	if tx.TargetJournal == "" || hostStoragePathsEqual(tx.Plan.Current.DataDir, tx.Plan.Desired.DataDir) {
		return hostStoragePhaseComplete
	}
	if tx.Plan.Legacy.CleanupAllowed {
		return hostStoragePhaseCleanupPending
	}
	return hostStoragePhaseValidated
}

func (s *Server) validateFinalHostStorageTransaction(ctx context.Context, tx *hostStorageTransaction) (hostStorageValidationResult, error) {
	if err := validateFinalHostStorageCatchPaths(s.cfg, tx.Plan.Desired); err != nil {
		return hostStorageValidationResult{}, err
	}
	applier := &hostStorageApplier{
		config:                 s.cfg,
		store:                  s.cfg.DB,
		zfs:                    s.zfsRunner,
		ops:                    hostStorageFinalizeOperationsFn(s.cfg),
		runningCatchState:      hostStorageStateFromConfig(s.cfg),
		runningCatchStateKnown: true,
	}
	applier.ops = applier.completeOperations()
	validation, err := applier.validateHostStorageApply(ctx, tx.Plan)
	if err != nil {
		return validation, err
	}
	if err := validateFinalHostStorageServiceStates(ctx, applier.ops.isServiceRunning, tx); err != nil {
		return validation, err
	}
	return validation, nil
}

func validateFinalHostStorageCatchPaths(cfg Config, desired catchrpc.HostStorageState) error {
	info := GetInfoWithConfig(&cfg)
	if !hostStoragePathsEqual(info.RootDir, desired.DataDir) || !hostStoragePathsEqual(info.ServicesDir, desired.ServicesRoot) {
		return fmt.Errorf("catch reported data dir %q and services root %q, want %q and %q", info.RootDir, info.ServicesDir, desired.DataDir, desired.ServicesRoot)
	}
	return nil
}

func validateFinalHostStorageServiceStates(ctx context.Context, running func(context.Context, string) (bool, error), tx *hostStorageTransaction) error {
	wantRunning := make(map[string]bool, len(tx.PreviouslyRunning))
	for _, name := range tx.PreviouslyRunning {
		wantRunning[name] = true
	}
	names := append(slices.Clone(tx.StoppedServices), tx.RestartedServices...)
	names = append(names, tx.PreviouslyRunning...)
	for _, move := range tx.Plan.ServicesAction.AffectedServices {
		names = append(names, move.Name)
	}
	names = append(names, tx.Plan.RepairAction.RestartServices...)
	for _, name := range uniqueSortedStrings(names) {
		if hostStorageSelfManagedService(name) {
			continue
		}
		active, err := running(ctx, name)
		if err != nil {
			return fmt.Errorf("validate finalized service %q: %w", name, err)
		}
		if active != wantRunning[name] {
			return fmt.Errorf("validate finalized service %q: running=%t, want %t", name, active, wantRunning[name])
		}
	}
	return nil
}

func (s *Server) rollbackFinalHostStorageTransaction(ctx context.Context, tx *hostStorageTransaction, cause error) error {
	sourceConfig := s.cfg
	sourceConfig.RootDir = cleanHostStoragePath(tx.Plan.Current.DataDir)
	sourceConfig.ServicesRoot = cleanHostStoragePath(tx.Plan.Current.ServicesRoot)
	sourceConfig.MountsRoot = filepath.Join(sourceConfig.RootDir, "mounts")
	sourceConfig.RegistryRoot = filepath.Join(sourceConfig.RootDir, "registry")
	sourceConfig.DB = db.NewStore(filepath.Join(sourceConfig.RootDir, "db.json"), sourceConfig.ServicesRoot)
	applier := &hostStorageApplier{
		config:                 sourceConfig,
		store:                  sourceConfig.DB,
		zfs:                    s.zfsRunner,
		ops:                    hostStorageFinalizeOperationsFn(sourceConfig),
		runningCatchState:      hostStorageStateFromConfig(s.cfg),
		runningCatchStateKnown: true,
	}
	applier.ops = applier.completeOperations()
	if err := stopUnexpectedFinalHostStorageServices(ctx, applier.ops, tx); err != nil {
		cause = errors.Join(cause, fmt.Errorf("stop unexpected target services before rollback: %w", err))
	}
	return applier.rollbackHostStorageTransaction(ctx, tx, cause, nil)
}

func stopUnexpectedFinalHostStorageServices(ctx context.Context, ops hostStorageApplyOperations, tx *hostStorageTransaction) error {
	wantRunning := make(map[string]bool, len(tx.PreviouslyRunning))
	for _, name := range tx.PreviouslyRunning {
		wantRunning[name] = true
	}
	names := slices.Clone(tx.StoppedServices)
	for _, move := range tx.Plan.ServicesAction.AffectedServices {
		names = append(names, move.Name)
	}
	names = append(names, tx.Plan.RepairAction.RestartServices...)
	for _, name := range uniqueSortedStrings(names) {
		if hostStorageSelfManagedService(name) || wantRunning[name] {
			continue
		}
		running, err := ops.isServiceRunning(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect service %q: %w", name, err)
		}
		if !running {
			continue
		}
		runner, err := ops.runnerForService(ctx, name)
		if err != nil {
			return fmt.Errorf("runner for service %q: %w", name, err)
		}
		if err := runner.Stop(); err != nil {
			return fmt.Errorf("stop service %q: %w", name, err)
		}
	}
	return nil
}

func (s *Server) CleanupHostStorage(ctx context.Context, req catchrpc.HostStorageCleanupRequest) (catchrpc.HostStorageCleanupResult, error) {
	s.hostStorageMutationMu.Lock()
	defer s.hostStorageMutationMu.Unlock()
	source, err := validatedHostStorageCleanupSource(req)
	if err != nil {
		return catchrpc.HostStorageCleanupResult{}, err
	}
	tx, err := s.validatedHostStorageCleanupTransaction(source)
	if err != nil {
		return catchrpc.HostStorageCleanupResult{}, err
	}
	result := catchrpc.HostStorageCleanupResult{TransactionID: tx.ID, Removed: source}
	return s.cleanupHostStorageTransaction(ctx, tx, source, result)
}

func validatedHostStorageCleanupSource(req catchrpc.HostStorageCleanupRequest) (string, error) {
	if !req.Yes {
		return "", fmt.Errorf("host storage cleanup requires --yes confirmation")
	}
	source := cleanHostStoragePath(req.From)
	if source == "" || !filepath.IsAbs(source) {
		return "", fmt.Errorf("host storage cleanup --from must be an absolute path, got %q", req.From)
	}
	return source, nil
}

func (s *Server) cleanupHostStorageTransaction(ctx context.Context, tx *hostStorageTransaction, source string, result catchrpc.HostStorageCleanupResult) (catchrpc.HostStorageCleanupResult, error) {
	if tx.Phase == hostStoragePhaseComplete {
		return result, nil
	}
	if _, err := s.validateFinalHostStorageTransaction(ctx, tx); err != nil {
		return catchrpc.HostStorageCleanupResult{}, fmt.Errorf("revalidate host storage before cleanup: %w", err)
	}
	missing, err := hostStorageCleanupSourceMissing(source)
	if err != nil {
		return catchrpc.HostStorageCleanupResult{}, err
	}
	if missing {
		return completeMissingHostStorageCleanup(tx, source, result)
	}
	if tx.Phase == hostStoragePhaseValidated {
		if err := advanceHostStorageTransaction(tx, hostStoragePhaseCleanupPending); err != nil {
			return catchrpc.HostStorageCleanupResult{}, fmt.Errorf("record pending host storage cleanup: %w", err)
		}
	}
	if err := hostStorageRemoveSourceTreeFn(ctx, source); err != nil {
		return catchrpc.HostStorageCleanupResult{}, fmt.Errorf("remove inactive host storage source %q; cleanup remains pending: %w", source, err)
	}
	if err := completeHostStorageCleanupTransaction(tx); err != nil {
		return catchrpc.HostStorageCleanupResult{}, err
	}
	return result, nil
}

func hostStorageCleanupSourceMissing(source string) (bool, error) {
	_, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}

func completeMissingHostStorageCleanup(tx *hostStorageTransaction, source string, result catchrpc.HostStorageCleanupResult) (catchrpc.HostStorageCleanupResult, error) {
	if tx.Phase != hostStoragePhaseCleanupPending {
		return catchrpc.HostStorageCleanupResult{}, fmt.Errorf("validated host storage source %q is missing before cleanup", source)
	}
	if err := completeHostStorageCleanupTransaction(tx); err != nil {
		return catchrpc.HostStorageCleanupResult{}, err
	}
	return result, nil
}

func (s *Server) validatedHostStorageCleanupTransaction(source string) (*hostStorageTransaction, error) {
	paths, err := hostStorageTransactionJournalPaths(s.cfg.RootDir)
	if err != nil {
		return nil, err
	}
	active, completed, err := matchingHostStorageCleanupTransactions(paths, s.cfg.RootDir, source)
	if err != nil {
		return nil, err
	}
	return selectHostStorageCleanupTransaction(source, active, completed)
}

func matchingHostStorageCleanupTransactions(paths []string, targetRoot, source string) (active, completed []*hostStorageTransaction, err error) {
	for _, journal := range paths {
		tx, loadErr := loadHostStorageTransaction(journal)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		if !hostStorageCleanupTransactionMatches(tx, journal, targetRoot, source) {
			continue
		}
		switch tx.Phase {
		case hostStoragePhaseValidated, hostStoragePhaseCleanupPending:
			active = append(active, tx)
		case hostStoragePhaseComplete:
			completed = append(completed, tx)
		}
	}
	return active, completed, nil
}

func hostStorageCleanupTransactionMatches(tx *hostStorageTransaction, journal, targetRoot, source string) bool {
	return hostStoragePathsEqual(tx.TargetJournal, journal) &&
		hostStoragePathsEqual(tx.Plan.Desired.DataDir, targetRoot) &&
		hostStoragePathsEqual(tx.Plan.Current.DataDir, source)
}

func selectHostStorageCleanupTransaction(source string, active, completed []*hostStorageTransaction) (*hostStorageTransaction, error) {
	if len(active) == 1 {
		return active[0], nil
	}
	if len(active) == 0 && len(completed) == 1 {
		return completed[0], nil
	}
	return nil, fmt.Errorf("host storage cleanup source %q is not authorized by exactly one validated host storage transaction", source)
}

func (s *Server) PlanHostStorage(ctx context.Context, req catchrpc.HostStoragePlanRequest) (catchrpc.HostStoragePlan, error) {
	s.hostStorageMutationMu.Lock()
	if s.hostStorageMutationBlock != nil {
		if s.hostStorageRecovery == nil || !hostStorageRecoveryRequestMatches(req, s.hostStorageRecovery.Plan) {
			err := s.hostStorageMutationBlock
			s.hostStorageMutationMu.Unlock()
			return catchrpc.HostStoragePlan{}, err
		}
		plan := s.hostStorageRecovery.Plan
		s.hostStorageMutationMu.Unlock()
		return plan, nil
	}
	s.hostStorageMutationMu.Unlock()
	planner := &hostStoragePlanner{
		config: s.cfg,
		store:  s.cfg.DB,
		zfs:    s.zfsRunner,
	}
	return planner.Plan(ctx, req)
}

func (s *Server) ApplyHostStoragePlan(ctx context.Context, plan catchrpc.HostStoragePlan, yes bool, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	s.hostStorageMutationMu.Lock()
	defer s.hostStorageMutationMu.Unlock()
	if s.hostStorageMutationBlock != nil {
		if !yes || s.hostStorageRecovery == nil || !reflect.DeepEqual(plan, s.hostStorageRecovery.Plan) {
			return catchrpc.HostStorageApplyResult{}, s.hostStorageMutationBlock
		}
		return s.applyHostStorageRecovery(ctx, yes, w)
	}
	applier := &hostStorageApplier{
		config:                 s.cfg,
		store:                  s.cfg.DB,
		zfs:                    s.zfsRunner,
		runningCatchState:      hostStorageStateFromConfig(s.cfg),
		runningCatchStateKnown: true,
	}
	return applier.Apply(ctx, plan, yes, w)
}

func hostStorageRecoveryRequestMatches(req catchrpc.HostStoragePlanRequest, plan catchrpc.HostStoragePlan) bool {
	set := req.Set
	if set.DataDir == nil && set.ServicesRoot == nil {
		return false
	}
	desired := plan.Current
	if set.DataDir != nil {
		desired.DataDir = set.DataDir.Value
		desired.DataDirZFS = set.DataDir.ZFS
	}
	if set.ServicesRoot != nil {
		if set.MigrateServices != catchrpc.HostStorageMigrateAll {
			return false
		}
		desired.ServicesRoot = set.ServicesRoot.Value
		desired.ServicesZFS = set.ServicesRoot.ZFS
	} else if set.MigrateServices != "" {
		return false
	}
	return hostStoragePathsEqual(desired.DataDir, plan.Desired.DataDir) && desired.DataDirZFS == plan.Desired.DataDirZFS &&
		hostStoragePathsEqual(desired.ServicesRoot, plan.Desired.ServicesRoot) && desired.ServicesZFS == plan.Desired.ServicesZFS
}

func (s *Server) applyHostStorageRecovery(ctx context.Context, yes bool, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	tx := s.hostStorageRecovery
	if tx == nil {
		return catchrpc.HostStorageApplyResult{}, s.hostStorageMutationBlock
	}
	sourceConfig := s.cfg
	sourceConfig.RootDir = cleanHostStoragePath(tx.Plan.Current.DataDir)
	sourceConfig.ServicesRoot = cleanHostStoragePath(tx.Plan.Current.ServicesRoot)
	sourceConfig.MountsRoot = filepath.Join(sourceConfig.RootDir, "mounts")
	sourceConfig.RegistryRoot = filepath.Join(sourceConfig.RootDir, "registry")
	sourceConfig.DB = db.NewStore(filepath.Join(sourceConfig.RootDir, "db.json"), sourceConfig.ServicesRoot)
	applier := &hostStorageApplier{
		config:                 sourceConfig,
		store:                  sourceConfig.DB,
		zfs:                    s.zfsRunner,
		runningCatchState:      hostStorageStateFromConfig(s.cfg),
		runningCatchStateKnown: true,
	}
	applier.ops = applier.completeOperations()
	rollbackErr := applier.rollbackHostStorageTransaction(ctx, tx, errors.New("recover unfinished host storage transaction"), w)
	if tx.Phase != hostStoragePhaseRolledBack || tx.CatchAuthority != hostStorageCatchAuthoritySource {
		return catchrpc.HostStorageApplyResult{}, rollbackErr
	}
	s.hostStorageMutationBlock = nil
	s.hostStorageRecovery = nil
	return catchrpc.HostStorageApplyResult{}, fmt.Errorf(
		"host storage recovery completed; rerun the requested migration with: %s: %w",
		hostStorageTransactionRecoveryCommand(tx.Plan),
		rollbackErr,
	)
}

func hostStorageStateFromConfig(config Config) catchrpc.HostStorageState {
	return catchrpc.HostStorageState{
		DataDir:      config.RootDir,
		ServicesRoot: config.ServicesRoot,
	}
}

func (p *hostStoragePlanner) Plan(ctx context.Context, req catchrpc.HostStoragePlanRequest) (catchrpc.HostStoragePlan, error) {
	current := p.currentState()
	desired, datasetsToCreate, warnings, servicesRootDataset, err := p.resolveDesiredState(ctx, current, req.Set)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	services, err := p.planServiceRootChange(ctx, current, desired, req.Set.MigrateServices, servicesRootDataset)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	catchAction, err := p.planCatchRootChange(ctx, desired, req.Set, servicesRootDataset)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	services, catchAction, legacyCandidate, legacyServices, err := p.prepareLegacyPlanActions(current, desired, services, catchAction)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	datasetsToCreate, err = p.appendServicesRootChildZFSDatasets(ctx, datasetsToCreate, services, catchAction)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	plan := catchrpc.HostStoragePlan{
		Current:             current,
		Desired:             desired,
		DataDirAction:       planHostStorageDataDirAction(current, desired),
		ServicesAction:      services,
		CatchAction:         catchAction,
		ZFSDatasetsToCreate: datasetsToCreate,
		Warnings:            warnings,
		RequiresRestart:     hostStorageStateRequiresRestart(current, desired) || catchAction.Move,
	}
	if err := p.populateHostStoragePreflight(ctx, &plan, legacyCandidate, legacyServices); err != nil {
		return plan, err
	}
	repair, err := p.planRepairAction(ctx, plan)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	plan.RepairAction = repair
	if repair.References > 0 {
		plan.RequiresRestart = true
	}
	return plan, nil
}

func (p *hostStoragePlanner) prepareLegacyPlanActions(current, desired catchrpc.HostStorageState, services catchrpc.HostStorageServicesAction, catchAction catchrpc.HostStorageCatchAction) (catchrpc.HostStorageServicesAction, catchrpc.HostStorageCatchAction, bool, []db.Service, error) {
	installHome, hasInstallHome := p.legacyInstallHome()
	legacyCandidate := hasInstallHome &&
		isExactLegacyDefault(current, installHome) &&
		hostStoragePathsEqual(desired.DataDir, managedHostStorageDataDir)
	if !legacyCandidate {
		return services, catchAction, false, nil, nil
	}
	legacyServices, err := p.hostStorageServices()
	if err != nil {
		return catchrpc.HostStorageServicesAction{}, catchrpc.HostStorageCatchAction{}, false, nil, err
	}
	services = preserveLegacyServiceMoves(p.config, legacyServices, services, current.DataDir)
	catchAction, err = p.preserveLegacyCatchAction(catchAction, current.DataDir)
	if err != nil {
		return catchrpc.HostStorageServicesAction{}, catchrpc.HostStorageCatchAction{}, false, nil, err
	}
	return services, catchAction, true, legacyServices, nil
}

func (p *hostStoragePlanner) legacyInstallHome() (string, bool) {
	recorded := strings.TrimSpace(p.config.InstallHome)
	if recorded != "" {
		cleaned := cleanHostStoragePath(recorded)
		return cleaned, filepath.IsAbs(cleaned)
	}
	home, ok := hostStorageLookupUserHomeFn(p.config.InstallUser)
	if !ok {
		return "", false
	}
	home = cleanHostStoragePath(home)
	return home, filepath.IsAbs(home)
}

func preserveLegacyServiceMoves(cfg Config, services []db.Service, action catchrpc.HostStorageServicesAction, legacyRoot string) catchrpc.HostStorageServicesAction {
	byName := make(map[string]db.Service, len(services))
	for _, service := range services {
		byName[service.Name] = service
	}
	legacyServicesRoot := filepath.Join(cleanHostStoragePath(legacyRoot), "services")
	preserved := make([]catchrpc.HostStorageServiceMove, 0, len(action.AffectedServices))
	for _, move := range action.AffectedServices {
		service, ok := byName[move.Name]
		if !ok {
			preserved = append(preserved, move)
			continue
		}
		currentRoot := cleanHostStoragePath(serviceRootFromConfig(cfg, service))
		if strings.TrimSpace(service.ServiceRootZFS) == "" && hostStorageRootContains(legacyServicesRoot, currentRoot) {
			preserved = append(preserved, move)
			continue
		}
	}
	action.AffectedServices = preserved
	return action
}

func (p *hostStoragePlanner) preserveLegacyCatchAction(action catchrpc.HostStorageCatchAction, legacyRoot string) (catchrpc.HostStorageCatchAction, error) {
	if !action.Move {
		return action, nil
	}
	service, ok, err := p.currentCatchServiceForHostStorage()
	if err != nil || !ok {
		return action, err
	}
	currentRoot := cleanHostStoragePath(serviceRootFromConfig(p.config, *service))
	legacyServicesRoot := filepath.Join(cleanHostStoragePath(legacyRoot), "services")
	if strings.TrimSpace(service.ServiceRootZFS) != "" || !hostStorageRootContains(legacyServicesRoot, currentRoot) {
		return catchrpc.HostStorageCatchAction{}, nil
	}
	return action, nil
}

func (p *hostStoragePlanner) populateHostStoragePreflight(ctx context.Context, plan *catchrpc.HostStoragePlan, legacyCandidate bool, services []db.Service) error {
	if plan == nil || !plan.DataDirAction.Move {
		return nil
	}
	mountPoints, err := hostStorageMountPointsFn()
	if err != nil {
		return err
	}
	if legacyCandidate {
		plan.Legacy = catchrpc.HostStorageLegacyPlan{
			Eligible:       true,
			SourceRoot:     cleanHostStoragePath(plan.Current.DataDir),
			TargetRoot:     cleanHostStoragePath(plan.Desired.DataDir),
			PreservedRoots: hostStoragePreservedServiceRoots(p.config, services, plan.Current.DataDir),
		}
		p.appendPreservedLegacyCatchRoot(&plan.Legacy)
		if err := p.preflightLegacyCleanupBoundaries(ctx, plan, mountPoints); err != nil {
			return err
		}
		plan.Legacy.CleanupAllowed = hostStorageLegacyCleanupScopeComplete(*plan) &&
			!hostStorageContainsPreservedRoot(plan.Legacy.SourceRoot, plan.Legacy.PreservedRoots)
	}
	bytesToCopy, err := hostStorageBytesToCopy(plan.Current.DataDir, mountPoints)
	if err != nil {
		return fmt.Errorf("estimate host storage copy: %w", err)
	}
	bytesFree, err := hostStorageFreeBytesFn(plan.Desired.DataDir)
	if err != nil {
		return fmt.Errorf("estimate host storage target space: %w", err)
	}
	plan.Estimate = catchrpc.HostStorageEstimate{BytesToCopy: bytesToCopy, BytesFree: bytesFree}
	if bytesToCopy > bytesFree {
		return fmt.Errorf("insufficient free space for host storage migration: need %d bytes at %q, only %d bytes free", bytesToCopy, plan.Desired.DataDir, bytesFree)
	}
	return nil
}

func (p *hostStoragePlanner) appendPreservedLegacyCatchRoot(legacy *catchrpc.HostStorageLegacyPlan) {
	if legacy == nil {
		return
	}
	service, ok, err := p.currentCatchServiceForHostStorage()
	if err != nil || !ok {
		return
	}
	root := cleanHostStoragePath(serviceRootFromConfig(p.config, *service))
	legacyServicesRoot := filepath.Join(legacy.SourceRoot, "services")
	if root != "" && (strings.TrimSpace(service.ServiceRootZFS) != "" || !hostStorageRootContains(legacyServicesRoot, root)) {
		legacy.PreservedRoots = uniqueSortedStrings(append(legacy.PreservedRoots, root))
	}
}

func hostStorageLegacyCleanupScopeComplete(plan catchrpc.HostStoragePlan) bool {
	return plan.DataDirAction.Move &&
		hostStoragePathsEqual(plan.Desired.DataDir, managedHostStorageDataDir) &&
		hostStoragePathsEqual(plan.Desired.ServicesRoot, filepath.Join(managedHostStorageDataDir, "services")) &&
		plan.ServicesAction.Mode == catchrpc.HostStorageMigrateAll
}

func hostStorageContainsPreservedRoot(sourceRoot string, preservedRoots []string) bool {
	for _, root := range preservedRoots {
		if hostStorageRootContains(sourceRoot, root) {
			return true
		}
	}
	return false
}

type hostStorageZFSBoundary struct {
	dataset    string
	mountPoint string
}

func (p *hostStoragePlanner) preflightLegacyCleanupBoundaries(ctx context.Context, plan *catchrpc.HostStoragePlan, mountPoints []string) error {
	sourceRoot := plan.Legacy.SourceRoot
	filesystemMounts := hostStoragePathsAtOrBelow(sourceRoot, mountPoints)
	blockingMounts := slices.Clone(filesystemMounts)
	boundaries, err := hostStorageZFSBoundaries(ctx, p.zfs, sourceRoot)
	if err != nil {
		return err
	}
	for _, boundary := range boundaries {
		blockingMounts = append(blockingMounts, boundary.mountPoint)
	}
	plan.Legacy.BlockingMounts = uniqueSortedStrings(blockingMounts)
	command := hostStorageExplicitMigrationCommand(*plan)
	if len(filesystemMounts) > 0 {
		return fmt.Errorf("mounted path %q blocks cleanup of exact legacy storage %q; migrate explicitly with: %s", filesystemMounts[0], sourceRoot, command)
	}
	if len(boundaries) > 0 {
		return fmt.Errorf("ZFS dataset %q mounted at %q blocks cleanup of exact legacy storage %q; migrate explicitly with: %s", boundaries[0].dataset, boundaries[0].mountPoint, sourceRoot, command)
	}
	return nil
}

func hostStorageExplicitMigrationCommand(plan catchrpc.HostStoragePlan) string {
	return fmt.Sprintf(
		"yeet host set --data-dir=%s --services-root=%s --migrate-services=all --yes",
		plan.Desired.DataDir,
		plan.Desired.ServicesRoot,
	)
}

func hostStoragePathsAtOrBelow(root string, candidates []string) []string {
	root = cleanHostStoragePath(root)
	var paths []string
	for _, candidate := range candidates {
		candidate = cleanHostStoragePath(candidate)
		if candidate != "" && (candidate == root || hostStorageRootContains(root, candidate)) {
			paths = append(paths, candidate)
		}
	}
	return uniqueSortedStrings(paths)
}

func hostStorageZFSBoundaries(ctx context.Context, runner zfsCommandRunner, root string) ([]hostStorageZFSBoundary, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	stdout, stderr, err := runner(ctx, "list", "-H", "-o", "name,mountpoint")
	if err != nil {
		if isZFSMissingCommand(stderr, err) || isZFSNoDatasetsAvailable(stderr, err) {
			return nil, nil
		}
		return nil, formatZFSCommandError("zfs list filesystems", stderr, err)
	}
	var boundaries []hostStorageZFSBoundary
	for _, line := range strings.Split(stdout, "\n") {
		dataset, mountPoint, ok := parseZFSMountpointRow(line)
		if !ok {
			continue
		}
		mountPoint = cleanHostStoragePath(mountPoint)
		if mountPoint == root || hostStorageRootContains(root, mountPoint) {
			boundaries = append(boundaries, hostStorageZFSBoundary{dataset: dataset, mountPoint: mountPoint})
		}
	}
	slices.SortFunc(boundaries, func(a, b hostStorageZFSBoundary) int {
		if cmp := strings.Compare(a.mountPoint, b.mountPoint); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.dataset, b.dataset)
	})
	return boundaries, nil
}

func readHostStorageMountPoints() ([]string, error) {
	mountInfo, err := os.Open("/proc/self/mountinfo")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect host storage mounts: %w", err)
	}
	defer func() { _ = mountInfo.Close() }()
	return hostStorageMountPointsFromReader(mountInfo)
}

func removeHostStorageSourceTree(ctx context.Context, source string) error {
	source, err := validatedHostStorageCleanupTreePath(source)
	if err != nil {
		return err
	}
	if err := rejectHostStorageCleanupMounts(source); err != nil {
		return err
	}
	info, device, root, err := openValidatedHostStorageCleanupTree(source)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return removeOpenHostStorageCleanupTree(ctx, root, source, info, device)
}

func validatedHostStorageCleanupTreePath(source string) (string, error) {
	source = cleanHostStoragePath(source)
	if source == "" || !filepath.IsAbs(source) || filepath.Dir(source) == source {
		return "", fmt.Errorf("host storage cleanup source must be a non-root absolute path, got %q", source)
	}
	return source, nil
}

func openValidatedHostStorageCleanupTree(source string) (os.FileInfo, uint64, *os.Root, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return nil, 0, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, nil, fmt.Errorf("host storage cleanup source %s is a symlink", source)
	}
	if !info.IsDir() {
		return nil, 0, nil, fmt.Errorf("host storage cleanup source %s is not a directory", source)
	}
	device, err := hostStorageCleanupDeviceFn(info)
	if err != nil {
		return nil, 0, nil, err
	}
	root, err := openHostStorageCleanupRoot(source, info)
	return info, device, root, err
}

func removeOpenHostStorageCleanupTree(ctx context.Context, root *os.Root, source string, info os.FileInfo, device uint64) error {
	if err := rejectHostStorageCleanupMounts(source); err != nil {
		return err
	}
	if err := removeHostStorageRootContents(ctx, root, source, device); err != nil {
		return err
	}
	if err := rejectHostStorageCleanupMounts(source); err != nil {
		return err
	}
	parent, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	name := filepath.Base(source)
	if err := verifyHostStorageCleanupSourceName(parent, name, source, info); err != nil {
		return err
	}
	if err := root.Close(); err != nil {
		return err
	}
	if err := verifyHostStorageCleanupSourceName(parent, name, source, info); err != nil {
		return err
	}
	return parent.Remove(name)
}

func rejectHostStorageCleanupMounts(source string) error {
	mounts, err := hostStorageMountPointsFn()
	if err != nil {
		return fmt.Errorf("inspect host storage cleanup mounts: %w", err)
	}
	if blocked := hostStoragePathsAtOrBelow(source, mounts); len(blocked) != 0 {
		return fmt.Errorf("mount boundary %q blocks host storage cleanup of %q", blocked[0], source)
	}
	return nil
}

func openHostStorageCleanupRoot(path string, expected os.FileInfo) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(expected, openedInfo) {
		_ = root.Close()
		return nil, fmt.Errorf("host storage cleanup path %s was replaced", path)
	}
	return root, nil
}

func removeHostStorageRootContents(ctx context.Context, root *os.Root, display string, device uint64) error {
	entries, err := readHostStorageCleanupDirectory(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeHostStorageRootEntry(ctx, root, display, entry.Name(), device); err != nil {
			return err
		}
	}
	return nil
}

func removeHostStorageRootEntry(ctx context.Context, root *os.Root, display, name string, device uint64) error {
	path := filepath.Join(display, name)
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("host storage cleanup path %s is a symlink", path)
	}
	entryDevice, err := hostStorageCleanupDeviceFn(info)
	if err != nil {
		return err
	}
	if entryDevice != device {
		return fmt.Errorf("device boundary at %s blocks host storage cleanup", path)
	}
	if info.IsDir() {
		return removeHostStorageCleanupDirectory(ctx, root, name, path, info, device)
	}
	if err := rejectHostStorageCleanupHardlink(path, info); err != nil {
		return err
	}
	return root.Remove(name)
}

func readHostStorageCleanupDirectory(root *os.Root) ([]os.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	return entries, errors.Join(readErr, closeErr)
}

func removeHostStorageCleanupDirectory(ctx context.Context, parent *os.Root, name, display string, expected os.FileInfo, device uint64) error {
	child, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() { _ = child.Close() }()
	opened, err := child.Open(".")
	if err != nil {
		return err
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	current, lstatErr := parent.Lstat(name)
	if err := errors.Join(statErr, closeErr, lstatErr); err != nil {
		return err
	}
	if !os.SameFile(expected, openedInfo) || !os.SameFile(openedInfo, current) {
		return fmt.Errorf("host storage cleanup directory %s was replaced", display)
	}
	if err := rejectHostStorageCleanupMounts(display); err != nil {
		return err
	}
	if err := removeHostStorageRootContents(ctx, child, display, device); err != nil {
		return err
	}
	current, err = parent.Lstat(name)
	if err != nil || !os.SameFile(openedInfo, current) {
		return fmt.Errorf("host storage cleanup directory %s was replaced", display)
	}
	return parent.Remove(name)
}

func verifyHostStorageCleanupSourceName(parent *os.Root, name, display string, expected os.FileInfo) error {
	current, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return fmt.Errorf("host storage cleanup source %s was replaced", display)
	}
	return nil
}

func rejectHostStorageCleanupHardlink(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && info.Mode().IsRegular() && stat.Nlink != 1 {
		return fmt.Errorf("host storage cleanup file %s is a hardlink with link count %d", path, stat.Nlink)
	}
	return nil
}

func hostStorageCleanupDevice(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("inspect host storage cleanup device for %s", info.Name())
	}
	return uint64(stat.Dev), nil
}

func hostStorageBytesToCopy(root string, mountPoints []string) (uint64, error) {
	root = cleanHostStoragePath(root)
	if root == "" {
		return 0, nil
	}
	blocked := hostStoragePathsAtOrBelow(root, mountPoints)
	var total uint64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		path = cleanHostStoragePath(path)
		if path != root && slices.Contains(blocked, path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		size := info.Size()
		if size <= 0 {
			return nil
		}
		if uint64(size) > ^uint64(0)-total {
			return fmt.Errorf("host storage byte estimate overflow at %q", path)
		}
		total += uint64(size)
		return nil
	})
	return total, err
}

func hostStorageFreeBytes(target string) (uint64, error) {
	parent, err := nearestExistingHostStoragePath(target)
	if err != nil {
		return 0, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(parent, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 || stat.Bavail <= 0 {
		return 0, nil
	}
	blockSize := uint64(stat.Bsize)
	available := uint64(stat.Bavail)
	if blockSize != 0 && available > ^uint64(0)/blockSize {
		return ^uint64(0), nil
	}
	return available * blockSize, nil
}

func nearestExistingHostStoragePath(target string) (string, error) {
	target = cleanHostStoragePath(target)
	if target == "" || !filepath.IsAbs(target) {
		return "", fmt.Errorf("host storage target must be an absolute path, got %q", target)
	}
	for candidate := target; ; candidate = filepath.Dir(candidate) {
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("no existing parent for host storage target %q", target)
		}
	}
}

type hostStorageApplier struct {
	config                 Config
	store                  *db.Store
	zfs                    zfsCommandRunner
	ops                    hostStorageApplyOperations
	runningCatchState      catchrpc.HostStorageState
	runningCatchStateKnown bool
}

type hostStorageApplyOperations struct {
	createTransaction                       func(context.Context, catchrpc.HostStoragePlan, string, []string, []string) (*hostStorageTransaction, error)
	preflightCatchRestart                   func(context.Context, catchrpc.HostStoragePlan) error
	isServiceRunning                        func(context.Context, string) (bool, error)
	runnerForService                        func(context.Context, string) (ServiceRunner, error)
	materializeServiceRootMigration         func(context.Context, serviceRootMigrationPlan, io.Writer) error
	applyServiceRootMigrationRuntimeChanges func(context.Context, Config, db.Service, db.Service, io.Writer) error
	copyDataDir                             func(context.Context, string, string, hostStorageDataDirCopyOptions) error
	applyManagedTargetLayout                func(string) error
	reinstallServiceUnits                   func(context.Context, Config, *db.Service) ([]string, error)
	regenerateVMUnit                        func(context.Context, Config, *db.Service, string) ([]string, error)
	reloadSystemd                           func(context.Context) error
	enableSystemdUnits                      func(context.Context, []string) error
	reinstallCatchUnit                      func(context.Context, hostStorageInstallRequest, io.Writer) error
	cancelCatchRestarts                     func(context.Context) error
	restartCatch                            func(context.Context, hostStorageInstallRequest, io.Writer) error
	verifyCatchInfo                         func(context.Context, catchrpc.HostStorageState, Config) (ServerInfo, error)
}

type hostStorageInstallRequest struct {
	DataDir             string
	DataDirZFS          bool
	ServicesRoot        string
	ServicesZFS         bool
	Config              Config
	CatchServiceRoot    string
	CatchServiceRootZFS string
	PinCatchServiceRoot bool
}

type hostStorageDataDirCopyOptions struct {
	AllowedExistingRoots []string
	ExcludeRoots         []string
}

type hostStorageServiceApplyMove struct {
	plan            serviceRootMigrationPlan
	move            catchrpc.HostStorageServiceMove
	old             db.Service
	pin             bool
	runningCaptured bool
}

var (
	hostStorageInstallCatchUnit    = installHostStorageCatchUnit
	hostStorageCancelCatchRestarts = cancelHostStorageCatchRestarts
	hostStorageRestartCatch        = restartHostStorageCatch
	hostStorageVerifyCatchInfo     = verifyHostStorageCatchInfo
	hostStorageRunCommand          = runHostStorageCommand
)

var (
	errHostStorageCatchRestartScheduled = errors.New("catch restart scheduled")
	errHostStorageSourceRestartHandoff  = errors.New("source catch restart handoff")
)

func (a *hostStorageApplier) Apply(ctx context.Context, plan catchrpc.HostStoragePlan, yes bool, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	_ = yes
	w = hostStorageApplyWriter(w)
	a.ops = a.completeOperations()
	plan, err := a.authoritativeHostStoragePreflight(ctx, plan)
	if err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	serviceMoves, err := a.prepareApply(ctx, plan)
	if err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if !hostStoragePlanNeedsTransaction(plan, serviceMoves) {
		return a.applyPreparedPlan(ctx, plan, serviceMoves, nil, w)
	}
	previouslyRunning, err := a.captureHostStorageRunningState(ctx, serviceMoves)
	if err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	unitPaths, err := a.hostStorageTransactionUnitPaths(plan)
	if err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	tx, err := a.ops.createTransaction(ctx, plan, filepath.Join(a.config.RootDir, "db.json"), unitPaths, previouslyRunning)
	if err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.prepareZFS(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, a.rollbackHostStorageTransaction(ctx, tx, err, w)
	}
	if err := persistHostStorageTransaction(tx); err != nil {
		return catchrpc.HostStorageApplyResult{}, a.rollbackHostStorageTransaction(ctx, tx, fmt.Errorf("mirror prepared host storage transaction: %w", err), w)
	}
	result, err := a.applyPreparedPlan(ctx, plan, serviceMoves, tx, w)
	if err != nil {
		return catchrpc.HostStorageApplyResult{}, a.rollbackHostStorageTransaction(ctx, tx, err, w)
	}
	result.TransactionID = tx.ID
	return result, nil
}

func hostStoragePlanNeedsTransaction(plan catchrpc.HostStoragePlan, moves []hostStorageServiceApplyMove) bool {
	return len(moves) > 0 || plan.DataDirAction.Move || plan.CatchAction.Move ||
		plan.RepairAction.References > 0 || hostStoragePlanNeedsCatchRestart(plan)
}

func (a *hostStorageApplier) authoritativeHostStoragePreflight(ctx context.Context, plan catchrpc.HostStoragePlan) (catchrpc.HostStoragePlan, error) {
	if err := ctx.Err(); err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	if err := a.validatePlanStillCurrent(plan); err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	planner := &hostStoragePlanner{config: a.config, store: a.store, zfs: a.zfs}
	authoritative := plan
	// Estimate and Legacy are server-derived advisory fields. Older clients may
	// omit them; non-empty legacy authority must still match current server state.
	authoritative.DataDirAction = planHostStorageDataDirAction(plan.Current, plan.Desired)
	authoritative.Estimate = catchrpc.HostStorageEstimate{}
	authoritative.Legacy = catchrpc.HostStorageLegacyPlan{}
	legacyCandidate, services, err := planner.authoritativeLegacyCandidate(plan.Current, plan.Desired)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	if err := planner.populateHostStoragePreflight(ctx, &authoritative, legacyCandidate, services); err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	if !hostStorageLegacyPlanOmitted(plan.Legacy) && !hostStorageLegacyPlansEqual(plan.Legacy, authoritative.Legacy) {
		return catchrpc.HostStoragePlan{}, fmt.Errorf("host storage plan legacy metadata does not match current server state; run yeet host set again")
	}
	return authoritative, nil
}

func (p *hostStoragePlanner) authoritativeLegacyCandidate(current, desired catchrpc.HostStorageState) (bool, []db.Service, error) {
	installHome, hasInstallHome := p.legacyInstallHome()
	candidate := hasInstallHome &&
		isExactLegacyDefault(current, installHome) &&
		hostStoragePathsEqual(desired.DataDir, managedHostStorageDataDir)
	if !candidate {
		return false, nil, nil
	}
	services, err := p.hostStorageServices()
	return true, services, err
}

func hostStorageLegacyPlanOmitted(plan catchrpc.HostStorageLegacyPlan) bool {
	return !plan.Eligible && !plan.CleanupAllowed &&
		strings.TrimSpace(plan.SourceRoot) == "" && strings.TrimSpace(plan.TargetRoot) == "" &&
		len(plan.PreservedRoots) == 0 && len(plan.BlockingMounts) == 0
}

func hostStorageLegacyPlansEqual(a, b catchrpc.HostStorageLegacyPlan) bool {
	return a.Eligible == b.Eligible && a.CleanupAllowed == b.CleanupAllowed &&
		hostStoragePathsEqual(a.SourceRoot, b.SourceRoot) && hostStoragePathsEqual(a.TargetRoot, b.TargetRoot) &&
		slices.Equal(uniqueSortedStrings(a.PreservedRoots), uniqueSortedStrings(b.PreservedRoots)) &&
		slices.Equal(uniqueSortedStrings(a.BlockingMounts), uniqueSortedStrings(b.BlockingMounts))
}

func hostStorageApplyWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func (a *hostStorageApplier) prepareApply(ctx context.Context, plan catchrpc.HostStoragePlan) ([]hostStorageServiceApplyMove, error) {
	steps := []func() error{
		func() error { return a.validatePlanStillCurrent(plan) },
		func() error { return a.validateCatchRootAction(plan) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	serviceMoves, err := a.buildServiceRootApplyMoves(ctx, plan)
	if err != nil {
		return nil, err
	}
	serviceMoves, err = a.appendRepairRestartMoves(ctx, serviceMoves, plan.RepairAction.RestartServices)
	if err != nil {
		return nil, err
	}
	serviceMoves, err = a.appendLegacyPreservedServicePins(ctx, plan, serviceMoves)
	if err != nil {
		return nil, err
	}
	if err := a.preflightCatchRestart(ctx, plan); err != nil {
		return nil, err
	}
	if err := a.preflightHostStorageEstimate(plan); err != nil {
		return nil, err
	}
	if err := a.preflightDataDirTarget(plan); err != nil {
		return nil, err
	}
	return serviceMoves, nil
}

func (a *hostStorageApplier) preflightHostStorageEstimate(plan catchrpc.HostStoragePlan) error {
	if !plan.DataDirAction.Move || plan.Estimate.BytesToCopy == 0 {
		return nil
	}
	bytesFree, err := hostStorageFreeBytesFn(plan.DataDirAction.To)
	if err != nil {
		return fmt.Errorf("preflight host storage target space: %w", err)
	}
	if plan.Estimate.BytesToCopy > bytesFree {
		return fmt.Errorf("insufficient free space for host storage migration: need %d bytes at %q, only %d bytes free", plan.Estimate.BytesToCopy, plan.DataDirAction.To, bytesFree)
	}
	return nil
}

func (a *hostStorageApplier) appendLegacyPreservedServicePins(ctx context.Context, plan catchrpc.HostStoragePlan, moves []hostStorageServiceApplyMove) ([]hostStorageServiceApplyMove, error) {
	if !plan.Legacy.Eligible {
		return moves, nil
	}
	dv, err := a.serviceRootApplyData()
	if err != nil {
		return nil, err
	}
	migrating := make(map[string]bool, len(moves))
	for _, move := range moves {
		if strings.TrimSpace(move.plan.OldRoot) != "" || strings.TrimSpace(move.plan.NewRoot) != "" {
			migrating[move.move.Name] = true
		}
	}
	for _, service := range hostStorageServicesFromDataView(dv) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root := cleanHostStoragePath(serviceRootFromConfig(a.config, service))
		if migrating[service.Name] || !slices.Contains(plan.Legacy.PreservedRoots, root) {
			continue
		}
		moves = append(moves, hostStorageServiceApplyMove{
			plan: serviceRootMigrationPlan{
				ServiceName: service.Name,
				OldRoot:     root,
				OldRootZFS:  strings.TrimSpace(service.ServiceRootZFS),
				NewRoot:     root,
				NewRootZFS:  strings.TrimSpace(service.ServiceRootZFS),
				Mode:        serviceRootMigrationCopy,
			},
			move: catchrpc.HostStorageServiceMove{Name: service.Name, From: root, To: root, ToZFS: strings.TrimSpace(service.ServiceRootZFS)},
			old:  *service.Clone(),
			pin:  true,
		})
	}
	return moves, nil
}

func (a *hostStorageApplier) applyPreparedPlan(ctx context.Context, plan catchrpc.HostStoragePlan, serviceMoves []hostStorageServiceApplyMove, tx *hostStorageTransaction, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	if err := a.stopAffectedServices(ctx, serviceMoves, tx); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if tx != nil {
		if err := advanceHostStorageTransaction(tx, hostStoragePhaseServicesStopped); err != nil {
			return catchrpc.HostStorageApplyResult{}, fmt.Errorf("record stopped host storage services: %w", err)
		}
	}
	rootMoves := hostStorageServiceRootApplyMoves(serviceMoves)
	result := catchrpc.HostStorageApplyResult{
		MigratedServices: hostStorageApplyResultMoves(rootMoves),
	}
	if err := a.mutateHostStorageState(ctx, plan, rootMoves, tx, w); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if tx != nil {
		if err := advanceHostStorageTransaction(tx, hostStoragePhaseTargetReady); err != nil {
			return catchrpc.HostStorageApplyResult{}, fmt.Errorf("record ready host storage target: %w", err)
		}
	}
	if err := a.finishHostStorageApply(ctx, plan, serviceMoves, tx, w, &result); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	return result, nil
}

func (a *hostStorageApplier) mutateHostStorageState(ctx context.Context, plan catchrpc.HostStoragePlan, rootMoves []hostStorageServiceApplyMove, tx *hostStorageTransaction, w io.Writer) error {
	if err := a.moveServiceRoots(ctx, rootMoves, w); err != nil {
		return err
	}
	if err := a.applyServiceDBUpdates(ctx, plan, rootMoves, w); err != nil {
		return err
	}
	if err := a.moveCatchRoot(ctx, plan, w); err != nil {
		return err
	}
	if err := a.moveDataDir(ctx, plan, tx, w); err != nil {
		return err
	}
	if err := a.rewriteTargetDataStore(ctx, plan, w); err != nil {
		if plan.DataDirAction.Move {
			err = hostStorageDataDirRecoveryError(plan, "rewrite target db", err)
		}
		return err
	}
	if err := a.repairGeneratedArtifactsAndUnits(ctx, plan, w); err != nil {
		return err
	}
	return nil
}

func (a *hostStorageApplier) finishHostStorageApply(ctx context.Context, plan catchrpc.HostStoragePlan, serviceMoves []hostStorageServiceApplyMove, tx *hostStorageTransaction, w io.Writer, result *catchrpc.HostStorageApplyResult) error {
	if err := a.restartPreviouslyRunningServices(ctx, serviceMoves, tx); err != nil {
		return err
	}
	if tx != nil && hostStoragePlanNeedsCatchRestart(plan) {
		if err := advanceHostStorageTransactionState(tx, hostStoragePhaseCatchSwitching, hostStorageCatchAuthorityTarget); err != nil {
			return fmt.Errorf("record host storage catch switch intent: %w", err)
		}
	}
	restarted, restartScheduled, err := a.reinstallRestartAndVerifyCatch(ctx, plan, w)
	if err != nil {
		return err
	}
	result.Restarted = restarted
	result.RestartScheduled = restartScheduled
	if restarted {
		a.runningCatchState = plan.Desired
		a.runningCatchStateKnown = true
	}
	if tx != nil {
		if err := advanceHostStorageTransaction(tx, hostStoragePhaseCatchSwitched); err != nil {
			return fmt.Errorf("record host storage catch switch: %w", err)
		}
	}
	validation, err := a.validateHostStorageApply(ctx, plan)
	if err != nil {
		return err
	}
	result.Validation = catchrpc.HostStorageValidation{
		ActiveRefs:   validation.ActiveRefs,
		DatabaseRefs: validation.DBRefs,
		SystemdRefs:  validation.SystemdRefs,
	}
	return nil
}

func (a *hostStorageApplier) completeOperations() hostStorageApplyOperations {
	ops := a.ops
	if ops.createTransaction == nil {
		ops.createTransaction = createHostStorageTransaction
	}
	a.completeCatchRestartOperations(&ops)
	a.completeServiceOperations(&ops)
	a.completeStorageOperations(&ops)
	return ops
}

func (a *hostStorageApplier) completeCatchRestartOperations(ops *hostStorageApplyOperations) {
	catchRestartSupported := ops.reinstallCatchUnit != nil && ops.restartCatch != nil && ops.verifyCatchInfo != nil
	if ops.preflightCatchRestart == nil {
		ops.preflightCatchRestart = a.preflightHostStorageCatchRestart
		if catchRestartSupported {
			ops.preflightCatchRestart = func(context.Context, catchrpc.HostStoragePlan) error {
				return nil
			}
		}
	}
	if ops.reinstallCatchUnit == nil {
		ops.reinstallCatchUnit = hostStorageInstallCatchUnit
	}
	if ops.cancelCatchRestarts == nil {
		ops.cancelCatchRestarts = hostStorageCancelCatchRestarts
	}
	if ops.restartCatch == nil {
		ops.restartCatch = hostStorageRestartCatch
	}
	if ops.verifyCatchInfo == nil {
		ops.verifyCatchInfo = hostStorageVerifyCatchInfo
	}
}

func (a *hostStorageApplier) runningCatchMatches(state catchrpc.HostStorageState) bool {
	running := a.runningCatchState
	if !a.runningCatchStateKnown {
		running = catchrpc.HostStorageState{
			DataDir:      a.config.RootDir,
			ServicesRoot: a.config.ServicesRoot,
		}
	}
	return hostStoragePathsEqual(running.DataDir, state.DataDir) &&
		hostStoragePathsEqual(running.ServicesRoot, state.ServicesRoot)
}

func (a *hostStorageApplier) completeServiceOperations(ops *hostStorageApplyOperations) {
	if ops.isServiceRunning == nil {
		ops.isServiceRunning = func(_ context.Context, name string) (bool, error) {
			return newHostStorageServer(a.config).IsServiceRunning(name)
		}
	}
	if ops.runnerForService == nil {
		ops.runnerForService = func(ctx context.Context, name string) (ServiceRunner, error) {
			execer := &ttyExecer{ctx: ctx, s: newHostStorageServer(a.config), sn: name}
			return execer.serviceRunner()
		}
	}
	if ops.materializeServiceRootMigration == nil {
		ops.materializeServiceRootMigration = a.materializeHostStorageServiceRootMigration
	}
	if ops.applyServiceRootMigrationRuntimeChanges == nil {
		ops.applyServiceRootMigrationRuntimeChanges = func(ctx context.Context, desired Config, before db.Service, after db.Service, w io.Writer) error {
			return applyServiceRootMigrationRuntimeChangesForConfigs(ctx, a.config, desired, before, after, w)
		}
	}
	if ops.reinstallServiceUnits == nil {
		ops.reinstallServiceUnits = reinstallHostStorageServiceUnits
	}
	if ops.regenerateVMUnit == nil {
		ops.regenerateVMUnit = regenerateHostStorageVMSystemdUnit
	}
	if ops.reloadSystemd == nil {
		ops.reloadSystemd = reloadHostStorageSystemd
	}
	if ops.enableSystemdUnits == nil {
		ops.enableSystemdUnits = enableHostStorageSystemdUnits
	}
}

func (a *hostStorageApplier) completeStorageOperations(ops *hostStorageApplyOperations) {
	if ops.copyDataDir == nil {
		ops.copyDataDir = func(ctx context.Context, from, to string, opts hostStorageDataDirCopyOptions) error {
			return copyHostStorageDataDir(ctx, from, to, opts)
		}
	}
	if ops.applyManagedTargetLayout == nil {
		ops.applyManagedTargetLayout = applyManagedHostStorageTargetLayout
	}
}

func newHostStorageServer(config Config) *Server {
	return NewUnstartedServer(&config)
}

func (a *hostStorageApplier) validatePlanStillCurrent(plan catchrpc.HostStoragePlan) error {
	current := catchrpc.HostStorageState{
		DataDir:      a.config.RootDir,
		ServicesRoot: a.config.ServicesRoot,
	}
	if !hostStoragePathsEqual(current.DataDir, plan.Current.DataDir) {
		return fmt.Errorf("host storage plan is stale: data dir is %q, plan expected %q", current.DataDir, plan.Current.DataDir)
	}
	if !hostStoragePathsEqual(current.ServicesRoot, plan.Current.ServicesRoot) {
		return fmt.Errorf("host storage plan is stale: services root is %q, plan expected %q", current.ServicesRoot, plan.Current.ServicesRoot)
	}
	return nil
}

func (a *hostStorageApplier) validateCatchRootAction(plan catchrpc.HostStoragePlan) error {
	if !plan.CatchAction.Move {
		return nil
	}
	catchService, err := a.currentCatchSystemdService()
	if err != nil {
		return err
	}
	currentRoot := cleanHostStoragePath(serviceRootFromConfig(a.config, *catchService))
	if !hostStoragePathsEqual(currentRoot, plan.CatchAction.From) {
		return fmt.Errorf("host storage plan is stale: catch service root is %q, plan expected %q", currentRoot, plan.CatchAction.From)
	}
	desiredRoot := filepath.Join(cleanHostStoragePath(plan.Desired.ServicesRoot), CatchService)
	if !hostStoragePathsEqual(plan.CatchAction.To, desiredRoot) {
		return fmt.Errorf("host storage plan is stale: catch service root target is %q, plan expected %q", plan.CatchAction.To, desiredRoot)
	}
	return nil
}

func (a *hostStorageApplier) preflightCatchRestart(ctx context.Context, plan catchrpc.HostStoragePlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !hostStoragePlanNeedsCatchRestart(plan) {
		return nil
	}
	if err := a.ops.preflightCatchRestart(ctx, plan); err != nil {
		return a.wrapCatchRestartError(plan, "preflight catch restart", err)
	}
	return nil
}

func (a *hostStorageApplier) preflightHostStorageCatchRestart(ctx context.Context, _ catchrpc.HostStoragePlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := a.currentCatchSystemdService()
	return err
}

func (a *hostStorageApplier) currentCatchSystemdService() (*db.Service, error) {
	store, err := a.dataStore()
	if err != nil {
		return nil, err
	}
	dv, err := store.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to get data: %v", err)
	}
	catchService, err := hostStorageCatchSystemdServiceFromData(dv)
	if err != nil {
		return nil, err
	}
	return catchService, nil
}

func hostStorageCatchSystemdServiceFromData(dv db.DataView) (*db.Service, error) {
	if !dv.Valid() {
		return nil, fmt.Errorf("db is invalid")
	}
	sv := dv.Services().Get(CatchService)
	if !sv.Valid() {
		return nil, fmt.Errorf("catch service is not configured")
	}
	service := sv.AsStruct()
	if service == nil {
		return nil, fmt.Errorf("catch service is not configured")
	}
	if service.ServiceType != db.ServiceTypeSystemd {
		return nil, fmt.Errorf("catch service must be systemd-backed, got %s", service.ServiceType)
	}
	return service, nil
}

func (a *hostStorageApplier) prepareZFS(ctx context.Context, plan catchrpc.HostStoragePlan) error {
	runner := a.zfs
	if runner == nil {
		runner = runZFSCommand
	}
	inlineDatasets := hostStorageInlineZFSDatasets(plan)
	for _, dataset := range plan.ZFSDatasetsToCreate {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := inlineDatasets[dataset]; ok {
			continue
		}
		if err := zfsCreateDataset(ctx, runner, dataset); err != nil {
			return err
		}
	}
	return nil
}

func hostStorageInlineZFSDatasets(plan catchrpc.HostStoragePlan) map[string]struct{} {
	out := map[string]struct{}{}
	for _, move := range plan.ServicesAction.AffectedServices {
		if hostStoragePathsEqual(move.From, move.To) && strings.TrimSpace(move.ToZFS) != "" {
			out[strings.TrimSpace(move.ToZFS)] = struct{}{}
		}
	}
	if plan.CatchAction.Move && hostStoragePathsEqual(plan.CatchAction.From, plan.CatchAction.To) && strings.TrimSpace(plan.CatchAction.ToZFS) != "" {
		out[strings.TrimSpace(plan.CatchAction.ToZFS)] = struct{}{}
	}
	return out
}

func (a *hostStorageApplier) buildServiceRootApplyMoves(ctx context.Context, plan catchrpc.HostStoragePlan) ([]hostStorageServiceApplyMove, error) {
	if len(plan.ServicesAction.AffectedServices) == 0 && !hostStoragePlanChangesServicesRoot(plan) {
		return nil, nil
	}
	if err := validateHostStorageServicesActionForApply(a.config, plan); err != nil {
		return nil, err
	}
	dv, err := a.serviceRootApplyData()
	if err != nil {
		return nil, err
	}

	moves := make([]hostStorageServiceApplyMove, 0, len(plan.ServicesAction.AffectedServices))
	for _, planned := range plan.ServicesAction.AffectedServices {
		if hostStorageSelfManagedService(planned.Name) {
			continue
		}
		move, err := a.buildServiceRootApplyMove(ctx, plan, dv, planned)
		if err != nil {
			return nil, err
		}
		moves = append(moves, move)
	}
	if err := a.validateCurrentServiceRootMoves(ctx, plan, dv); err != nil {
		return nil, err
	}
	return moves, nil
}

func validateHostStorageServicesActionForApply(config Config, plan catchrpc.HostStoragePlan) error {
	if err := validateHostStorageMigrateMode(plan.ServicesAction.Mode); err != nil {
		return err
	}
	if plan.ServicesAction.Mode != catchrpc.HostStorageMigrateAll {
		return nil
	}
	oldRoot := hostStorageBatchRoot(config.ServicesRoot, plan.ServicesAction.From)
	newRoot := hostStorageBatchRoot("", plan.ServicesAction.To)
	if rootsAreNested(oldRoot, newRoot) || rootsAreNested(newRoot, oldRoot) {
		return fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, newRoot)
	}
	return nil
}

func hostStoragePlanChangesServicesRoot(plan catchrpc.HostStoragePlan) bool {
	from, to := hostStorageServicesActionRoots(plan)
	return !hostStoragePathsEqual(from, to) ||
		!hostStoragePathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot)
}

func hostStorageServicesActionRoots(plan catchrpc.HostStoragePlan) (string, string) {
	from := strings.TrimSpace(plan.ServicesAction.From)
	if from == "" {
		from = plan.Current.ServicesRoot
	}
	to := strings.TrimSpace(plan.ServicesAction.To)
	if to == "" {
		to = plan.Desired.ServicesRoot
	}
	return from, to
}

func (a *hostStorageApplier) serviceRootApplyData() (db.DataView, error) {
	store, err := a.dataStore()
	if err != nil {
		return db.DataView{}, err
	}
	dv, err := store.Get()
	if err != nil {
		return db.DataView{}, fmt.Errorf("failed to get data: %v", err)
	}
	if !dv.Valid() {
		return db.DataView{}, fmt.Errorf("db is invalid")
	}
	return dv, nil
}

func (a *hostStorageApplier) validateCurrentServiceRootMoves(ctx context.Context, plan catchrpc.HostStoragePlan, dv db.DataView) error {
	return a.validateCurrentServiceRootMovesForServices(ctx, plan, hostStorageServicesFromDataView(dv))
}

func (a *hostStorageApplier) validateCurrentServiceRootMovesForServices(ctx context.Context, plan catchrpc.HostStoragePlan, services []db.Service) error {
	if len(plan.ServicesAction.AffectedServices) == 0 && !hostStoragePlanChangesServicesRoot(plan) {
		return nil
	}
	current, err := currentHostStorageServiceMovesForPlan(ctx, a.config, services, plan)
	if err != nil {
		return err
	}
	current = canonicalHostStorageServiceMoves(current)
	planned := canonicalHostStorageServiceMoves(plan.ServicesAction.AffectedServices)
	if !hostStorageServiceMovesEqual(current, planned) {
		return fmt.Errorf("host storage plan is stale: affected services changed during host storage planning; run yeet host set again")
	}
	return nil
}

func currentHostStorageServiceMovesForPlan(ctx context.Context, cfg Config, services []db.Service, plan catchrpc.HostStoragePlan) ([]catchrpc.HostStorageServiceMove, error) {
	from, to := hostStorageServicesActionRoots(plan)
	servicesRootDataset := hostStorageServicesRootDatasetFromMoves(plan.ServicesAction.AffectedServices)
	var moves []catchrpc.HostStorageServiceMove
	if hostStoragePathsEqual(from, to) && servicesRootDataset != "" && plan.ServicesAction.Mode == catchrpc.HostStorageMigrateAll {
		var err error
		moves, err = planServicesRootZFSChildDatasetMoves(ctx, cfg, services, to, servicesRootDataset)
		if err != nil {
			return nil, err
		}
	} else {
		batch, err := planServicesRootBatch(ctx, cfg, services, from, to, plan.ServicesAction.Mode)
		if err != nil {
			return nil, err
		}
		moves = hostStorageServiceMovesFromRootBatch(batch)
		applyServicesRootDatasetToMoves(moves, servicesRootDataset, plan.ServicesAction.Mode)
	}
	if plan.Legacy.Eligible {
		action := plan.ServicesAction
		action.AffectedServices = moves
		moves = preserveLegacyServiceMoves(cfg, services, action, plan.Legacy.SourceRoot).AffectedServices
	}
	return moves, nil
}

func hostStorageServicesRootDatasetFromMoves(moves []catchrpc.HostStorageServiceMove) string {
	for _, move := range moves {
		name := strings.TrimSpace(move.Name)
		dataset := strings.TrimSpace(move.ToZFS)
		if name == "" || dataset == "" || path.Base(dataset) != name {
			continue
		}
		return path.Dir(dataset)
	}
	return ""
}

func hostStorageServicesFromDataView(dv db.DataView) []db.Service {
	var services []db.Service
	for _, sv := range dv.Services().All() {
		service := sv.AsStruct()
		if service != nil && !hostStorageSelfManagedService(service.Name) {
			services = append(services, *service)
		}
	}
	return services
}

func hostStorageServicesFromMap(services map[string]*db.Service) []db.Service {
	out := make([]db.Service, 0, len(services))
	for _, service := range services {
		if service != nil && !hostStorageSelfManagedService(service.Name) {
			out = append(out, *service)
		}
	}
	return out
}

func canonicalHostStorageServiceMoves(moves []catchrpc.HostStorageServiceMove) []catchrpc.HostStorageServiceMove {
	out := make([]catchrpc.HostStorageServiceMove, 0, len(moves))
	for _, move := range moves {
		out = append(out, catchrpc.HostStorageServiceMove{
			Name:  strings.TrimSpace(move.Name),
			From:  cleanHostStoragePath(move.From),
			To:    cleanHostStoragePath(move.To),
			ToZFS: strings.TrimSpace(move.ToZFS),
		})
	}
	slices.SortFunc(out, func(a, b catchrpc.HostStorageServiceMove) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		if c := strings.Compare(a.To, b.To); c != 0 {
			return c
		}
		return strings.Compare(a.ToZFS, b.ToZFS)
	})
	return out
}

func hostStorageServiceMovesEqual(a, b []catchrpc.HostStorageServiceMove) bool {
	return slices.EqualFunc(a, b, func(a, b catchrpc.HostStorageServiceMove) bool {
		return a.Name == b.Name && a.From == b.From && a.To == b.To && a.ToZFS == b.ToZFS
	})
}

func (a *hostStorageApplier) buildServiceRootApplyMove(ctx context.Context, plan catchrpc.HostStoragePlan, dv db.DataView, planned catchrpc.HostStorageServiceMove) (hostStorageServiceApplyMove, error) {
	if err := ctx.Err(); err != nil {
		return hostStorageServiceApplyMove{}, err
	}
	name := strings.TrimSpace(planned.Name)
	if name == "" {
		return hostStorageServiceApplyMove{}, fmt.Errorf("host storage service move contains a service with no name")
	}
	oldService, err := serviceRootApplyOldService(dv, name)
	if err != nil {
		return hostStorageServiceApplyMove{}, err
	}
	oldRoot := filepath.Clean(serviceRootFromConfig(a.config, *oldService))
	plannedFrom := filepath.Clean(planned.From)
	plannedTo := filepath.Clean(planned.To)
	plannedToZFS := strings.TrimSpace(planned.ToZFS)
	if oldRoot != plannedFrom {
		return hostStorageServiceApplyMove{}, fmt.Errorf("service root for %q changed during host storage planning", name)
	}
	if plan.ServicesAction.Mode == catchrpc.HostStorageMigrateAll && plannedFrom == plannedTo && (plannedToZFS == "" || oldService.ServiceRootZFS == plannedToZFS) {
		return hostStorageServiceApplyMove{}, fmt.Errorf("service root for %q is already %s", name, planned.From)
	}
	return hostStorageServiceApplyMove{
		plan: serviceRootMigrationPlan{
			ServiceName: name,
			OldRoot:     plannedFrom,
			OldRootZFS:  oldService.ServiceRootZFS,
			NewRoot:     plannedTo,
			NewRootZFS:  plannedToZFS,
			Mode:        serviceRootApplyMigrationMode(plan.ServicesAction.Mode),
		},
		move: catchrpc.HostStorageServiceMove{
			Name:  name,
			From:  plannedFrom,
			To:    plannedTo,
			ToZFS: plannedToZFS,
		},
		old: *oldService.Clone(),
	}, nil
}

func (a *hostStorageApplier) appendRepairRestartMoves(ctx context.Context, moves []hostStorageServiceApplyMove, names []string) ([]hostStorageServiceApplyMove, error) {
	if len(names) == 0 {
		return moves, nil
	}
	seen := make(map[string]bool, len(moves))
	for _, move := range moves {
		seen[move.move.Name] = true
	}
	dv, err := a.serviceRootApplyData()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if name == "" || hostStorageSelfManagedService(name) || seen[name] {
			continue
		}
		service, err := serviceRootApplyOldService(dv, name)
		if err != nil {
			return nil, err
		}
		moves = append(moves, hostStorageServiceApplyMove{
			move: catchrpc.HostStorageServiceMove{Name: name},
			old:  *service.Clone(),
		})
		seen[name] = true
	}
	return moves, nil
}

func serviceRootApplyOldService(dv db.DataView, name string) (*db.Service, error) {
	sv := dv.Services().Get(name)
	if !sv.Valid() {
		return nil, fmt.Errorf("service %q not found", name)
	}
	oldService := sv.AsStruct()
	if oldService == nil {
		return nil, fmt.Errorf("service %q not found", name)
	}
	return oldService, nil
}

func serviceRootApplyMigrationMode(catchrpc.HostStorageMigrateServices) serviceRootMigrationMode {
	return serviceRootMigrationCopy
}

func (a *hostStorageApplier) captureHostStorageRunningState(ctx context.Context, moves []hostStorageServiceApplyMove) ([]string, error) {
	var previouslyRunning []string
	for i := range moves {
		if moves[i].pin {
			continue
		}
		running, err := a.ops.isServiceRunning(ctx, moves[i].move.Name)
		if err != nil {
			if hostStorageRunningCheckCanTreatAsStopped(moves[i].plan) {
				running = false
			} else {
				return nil, fmt.Errorf("check service %q running state: %w", moves[i].move.Name, err)
			}
		}
		moves[i].move.WasRunning = running
		moves[i].runningCaptured = true
		if running {
			previouslyRunning = append(previouslyRunning, moves[i].move.Name)
		}
	}
	return uniqueSortedStrings(previouslyRunning), nil
}

func (a *hostStorageApplier) hostStorageTransactionUnitPaths(plan catchrpc.HostStoragePlan) ([]string, error) {
	dv, err := a.serviceRootApplyData()
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{})
	addHostStorageServiceUnitPaths(paths, hostStorageServicesFromDataView(dv))
	addHostStorageRepairUnitPaths(paths, plan.RepairAction.RegenerateUnits)
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	slices.Sort(out)
	return out, nil
}

func addHostStorageServiceUnitPaths(paths map[string]struct{}, services []db.Service) {
	for _, service := range services {
		if strings.TrimSpace(service.Name) == "" {
			continue
		}
		name := service.Name
		for _, unit := range []string{name + ".service", name + ".timer", "yeet-" + name + "-ns.service", "yeet-" + name + "-ts.service"} {
			paths[filepath.Join(systemdSystemDir, unit)] = struct{}{}
		}
		if service.ServiceType == db.ServiceTypeVM {
			paths[filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(name))] = struct{}{}
		}
	}
}

func addHostStorageRepairUnitPaths(paths map[string]struct{}, units []string) {
	for _, unit := range units {
		unit = strings.TrimSpace(unit)
		if unit == "" {
			continue
		}
		if filepath.IsAbs(unit) {
			paths[filepath.Clean(unit)] = struct{}{}
			continue
		}
		dir := systemdSystemDir
		if strings.HasPrefix(unit, "yeet-vm-") {
			dir = vmSystemdSystemDir
		}
		paths[filepath.Join(dir, unit)] = struct{}{}
	}
}

func (a *hostStorageApplier) stopAffectedServices(ctx context.Context, moves []hostStorageServiceApplyMove, tx *hostStorageTransaction) error {
	for i := range moves {
		if moves[i].pin {
			continue
		}
		running, err := a.hostStorageMoveWasRunning(ctx, &moves[i])
		if err != nil {
			return err
		}
		if !running {
			continue
		}
		if err := a.stopHostStorageService(ctx, moves[i].move.Name, tx); err != nil {
			return err
		}
	}
	return nil
}

func (a *hostStorageApplier) hostStorageMoveWasRunning(ctx context.Context, move *hostStorageServiceApplyMove) (bool, error) {
	if move.runningCaptured {
		return move.move.WasRunning, nil
	}
	running, err := a.ops.isServiceRunning(ctx, move.move.Name)
	if err != nil && !hostStorageRunningCheckCanTreatAsStopped(move.plan) {
		return false, fmt.Errorf("check service %q running state: %w", move.move.Name, err)
	}
	if err != nil {
		running = false
	}
	move.move.WasRunning = running
	return running, nil
}

func (a *hostStorageApplier) stopHostStorageService(ctx context.Context, name string, tx *hostStorageTransaction) error {
	runner, err := a.ops.runnerForService(ctx, name)
	if err != nil {
		return fmt.Errorf("runner for service %q: %w", name, err)
	}
	if err := runner.Stop(); err != nil {
		return fmt.Errorf("stop service %q before host storage apply: %w", name, err)
	}
	if tx == nil {
		return nil
	}
	tx.StoppedServices = uniqueSortedStrings(append(tx.StoppedServices, name))
	if err := persistHostStorageTransaction(tx); err != nil {
		return fmt.Errorf("record stopped service %q: %w", name, err)
	}
	return nil
}

func hostStorageRunningCheckCanTreatAsStopped(plan serviceRootMigrationPlan) bool {
	if !hostStorageServiceRootNeedsInPlaceZFS(plan) {
		return false
	}
	_, ok, err := findInPlaceZFSServiceRootStage(plan.NewRoot)
	if err != nil || !ok {
		return false
	}
	emptyLayout, err := serviceRootHasOnlyEmptyServiceLayout(plan.NewRoot)
	return err == nil && emptyLayout
}

func (a *hostStorageApplier) moveServiceRoots(ctx context.Context, moves []hostStorageServiceApplyMove, w io.Writer) error {
	for _, move := range moves {
		if move.pin {
			continue
		}
		if strings.TrimSpace(move.plan.OldRoot) == "" && strings.TrimSpace(move.plan.NewRoot) == "" {
			continue
		}
		if move.plan.Mode != serviceRootMigrationCopy || hostStorageServiceRootMigrationIsNoop(move.plan) {
			continue
		}
		if err := prepareHostStorageServiceMoveTarget(move.plan); err != nil {
			return hostStorageServiceMoveRecoveryError(move.plan, err)
		}
		if err := a.ops.materializeServiceRootMigration(ctx, move.plan, w); err != nil {
			return hostStorageServiceMoveRecoveryError(move.plan, err)
		}
	}
	return nil
}

func hostStorageServiceRootMigrationIsNoop(plan serviceRootMigrationPlan) bool {
	return hostStoragePathsEqual(plan.OldRoot, plan.NewRoot) &&
		strings.TrimSpace(plan.OldRootZFS) == strings.TrimSpace(plan.NewRootZFS)
}

func (a *hostStorageApplier) materializeHostStorageServiceRootMigration(ctx context.Context, plan serviceRootMigrationPlan, w io.Writer) error {
	if hostStorageServiceRootNeedsInPlaceZFS(plan) {
		runner := a.zfs
		if runner == nil {
			runner = runZFSCommand
		}
		return materializeInPlaceZFSServiceRootMigration(ctx, runner, plan, w)
	}
	return materializeServiceRootMigration(ctx, plan, w)
}

func hostStorageServiceRootNeedsInPlaceZFS(plan serviceRootMigrationPlan) bool {
	return strings.TrimSpace(plan.NewRootZFS) != "" &&
		hostStoragePathsEqual(plan.OldRoot, plan.NewRoot) &&
		strings.TrimSpace(plan.OldRootZFS) != strings.TrimSpace(plan.NewRootZFS)
}

func materializeInPlaceZFSServiceRootMigration(ctx context.Context, runner zfsCommandRunner, plan serviceRootMigrationPlan, w io.Writer) error {
	_ = w
	root, dataset, err := validateInPlaceZFSServiceRootMigrationPlan(ctx, plan)
	if err != nil {
		return err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return err
	}
	if exists {
		return materializeExistingInPlaceZFSDataset(ctx, runner, dataset, root)
	}
	return materializeNewInPlaceZFSDataset(ctx, runner, dataset, root)
}

func validateInPlaceZFSServiceRootMigrationPlan(ctx context.Context, plan serviceRootMigrationPlan) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	root := cleanHostStoragePath(plan.NewRoot)
	dataset := strings.TrimSpace(plan.NewRootZFS)
	if root == "" || dataset == "" {
		return "", "", fmt.Errorf("in-place zfs service root migration requires a root and dataset")
	}
	if !hostStoragePathsEqual(plan.OldRoot, plan.NewRoot) {
		return "", "", fmt.Errorf("in-place zfs service root migration requires matching source and target roots")
	}
	return root, dataset, nil
}

func materializeExistingInPlaceZFSDataset(ctx context.Context, runner zfsCommandRunner, dataset, root string) error {
	if err := validateInPlaceZFSServiceRoot(ctx, runner, dataset, root); err != nil {
		return err
	}
	return restoreRetryStagedInPlaceZFSServiceRoot(root)
}

func materializeNewInPlaceZFSDataset(ctx context.Context, runner zfsCommandRunner, dataset, root string) error {
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return materializeMissingInPlaceZFSRoot(ctx, runner, dataset, root)
	}
	if err != nil {
		return fmt.Errorf("stat service root %q before zfs conversion: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("service root %q is not a directory", root)
	}
	return materializePopulatedInPlaceZFSRoot(ctx, runner, dataset, root, info.Mode().Perm())
}

func materializeMissingInPlaceZFSRoot(ctx context.Context, runner zfsCommandRunner, dataset, root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create empty service root for zfs conversion: %w", err)
	}
	if err := zfsCreateDataset(ctx, runner, dataset); err != nil {
		return err
	}
	return validateInPlaceZFSServiceRoot(ctx, runner, dataset, root)
}

func materializePopulatedInPlaceZFSRoot(ctx context.Context, runner zfsCommandRunner, dataset, root string, mode os.FileMode) error {
	stage, err := os.MkdirTemp(filepath.Dir(root), ".yeet-service-root-stage-")
	if err != nil {
		return fmt.Errorf("create in-place zfs migration stage: %w", err)
	}
	if err := os.Remove(stage); err != nil {
		return fmt.Errorf("prepare in-place zfs migration stage: %w", err)
	}
	staged := false
	rollback := func(cause error) error {
		if !staged {
			_ = os.RemoveAll(stage)
			return cause
		}
		_ = os.RemoveAll(root)
		if err := renameServiceRoot(stage, root); err != nil {
			return fmt.Errorf("%w; rollback service root from %q to %q failed: %v", cause, stage, root, err)
		}
		staged = false
		return cause
	}
	if err := renameServiceRoot(root, stage); err != nil {
		return fmt.Errorf("stage service root for zfs conversion: %w", err)
	}
	staged = true
	if err := os.MkdirAll(root, mode); err != nil {
		return rollback(fmt.Errorf("create empty zfs mountpoint for service root: %w", err))
	}
	if err := zfsCreateDataset(ctx, runner, dataset); err != nil {
		return rollback(err)
	}
	if err := validateInPlaceZFSServiceRoot(ctx, runner, dataset, root); err != nil {
		return err
	}
	if err := restoreStagedServiceRootIntoZFS(stage, root); err != nil {
		return err
	}
	staged = false
	return nil
}

func validateInPlaceZFSServiceRoot(ctx context.Context, runner zfsCommandRunner, dataset, expectedRoot string) error {
	mountpoint, err := zfsDatasetMountpoint(ctx, runner, dataset)
	if err != nil {
		return err
	}
	root, _, err := validateZFSMountpoint(mountpoint, zfsServiceRootExisting, true)
	if err != nil {
		return err
	}
	if !hostStoragePathsEqual(root, expectedRoot) {
		return fmt.Errorf("ZFS dataset %q is mounted at %q, expected %q", dataset, root, expectedRoot)
	}
	return nil
}

func restoreRetryStagedInPlaceZFSServiceRoot(root string) error {
	stage, ok, err := findInPlaceZFSServiceRootStage(root)
	if err != nil || !ok {
		return err
	}
	return restoreStagedServiceRootIntoZFS(stage, root)
}

func findInPlaceZFSServiceRootStage(root string) (string, bool, error) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(root), ".yeet-service-root-stage-*"))
	if err != nil {
		return "", false, err
	}
	stages := make([]string, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", false, fmt.Errorf("stat in-place zfs migration stage %q: %w", match, err)
		}
		if info.IsDir() {
			stages = append(stages, match)
		}
	}
	if len(stages) == 0 {
		return "", false, nil
	}
	if len(stages) > 1 {
		return "", false, fmt.Errorf("found multiple in-place zfs migration stages near %q: %s", root, strings.Join(stages, ", "))
	}
	return stages[0], true, nil
}

func restoreStagedServiceRootIntoZFS(stage, root string) error {
	emptyLayout, err := serviceRootHasOnlyEmptyServiceLayout(root)
	if err != nil {
		return err
	}
	if !emptyLayout {
		return fmt.Errorf("cannot restore staged service root into non-empty zfs root %q", root)
	}
	if err := removeMountedRootServiceLayout(root); err != nil {
		return err
	}
	if err := copyServiceRootToStage(stage, root); err != nil {
		return fmt.Errorf("copy staged service root into zfs dataset: %w", err)
	}
	if err := os.RemoveAll(stage); err != nil {
		return fmt.Errorf("remove staged service root %q: %w", stage, err)
	}
	return nil
}

func serviceRootHasOnlyEmptyServiceLayout(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, fmt.Errorf("read zfs service root %q: %w", root, err)
	}
	allowed := map[string]struct{}{
		"bin":  {},
		"data": {},
		"env":  {},
		"run":  {},
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || !entry.IsDir() {
			return false, nil
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return false, fmt.Errorf("read zfs service root child %q: %w", filepath.Join(root, entry.Name()), err)
		}
		if len(children) != 0 {
			return false, nil
		}
	}
	return true, nil
}

func prepareHostStorageServiceMoveTarget(plan serviceRootMigrationPlan) error {
	if strings.TrimSpace(plan.NewRootZFS) != "" {
		return nil
	}
	return os.MkdirAll(filepath.Dir(plan.NewRoot), 0o755)
}

func (a *hostStorageApplier) applyServiceDBUpdates(ctx context.Context, plan catchrpc.HostStoragePlan, moves []hostStorageServiceApplyMove, w io.Writer) error {
	if len(moves) == 0 {
		return a.validateServiceRootPlanStillMatchesStore(ctx, plan)
	}
	desiredConfig := a.desiredConfig(plan)
	updates, err := a.prepareServiceDBUpdates(ctx, desiredConfig, moves, w)
	if err != nil {
		return err
	}
	return a.commitServiceDBUpdates(ctx, plan, moves, updates)
}

func (a *hostStorageApplier) prepareServiceDBUpdates(ctx context.Context, desiredConfig Config, moves []hostStorageServiceApplyMove, w io.Writer) (map[string]*db.Service, error) {
	updates := make(map[string]*db.Service, len(moves))
	for _, move := range moves {
		updated, err := updatedServiceForHostStorageRootMigration(desiredConfig, move.plan, move.old.Clone())
		if err != nil {
			return nil, err
		}
		if !move.pin {
			if err := a.ops.applyServiceRootMigrationRuntimeChanges(ctx, desiredConfig, move.old, *updated, w); err != nil {
				return nil, err
			}
		}
		updates[move.plan.ServiceName] = updated
	}
	return updates, nil
}

func (a *hostStorageApplier) commitServiceDBUpdates(ctx context.Context, plan catchrpc.HostStoragePlan, moves []hostStorageServiceApplyMove, updates map[string]*db.Service) error {
	store, err := a.dataStore()
	if err != nil {
		return err
	}
	_, err = store.MutateData(func(d *db.Data) error {
		if err := a.validateCurrentServiceRootMovesForServices(ctx, plan, hostStorageServicesFromMap(d.Services)); err != nil {
			return err
		}
		for _, move := range moves {
			currentService, ok := d.Services[move.plan.ServiceName]
			if !ok {
				return fmt.Errorf("service %q not found", move.plan.ServiceName)
			}
			if filepath.Clean(serviceRootFromConfig(a.config, *currentService)) != move.plan.OldRoot || currentService.ServiceRootZFS != move.plan.OldRootZFS {
				return fmt.Errorf("service root for %q changed during host storage planning", move.plan.ServiceName)
			}
			d.Services[move.plan.ServiceName] = updates[move.plan.ServiceName].Clone()
		}
		return nil
	})
	return err
}

func (a *hostStorageApplier) validateServiceRootPlanStillMatchesStore(ctx context.Context, plan catchrpc.HostStoragePlan) error {
	if !hostStoragePlanChangesServicesRoot(plan) {
		return nil
	}
	dv, err := a.serviceRootApplyData()
	if err != nil {
		return err
	}
	return a.validateCurrentServiceRootMoves(ctx, plan, dv)
}

func updatedServiceForHostStorageRootMigration(cfg Config, plan serviceRootMigrationPlan, oldService *db.Service) (*db.Service, error) {
	updated, err := updatedServiceForRootMigration(cfg, plan, oldService)
	if err != nil {
		return nil, err
	}
	updated.ServiceRoot = plan.NewRoot
	updated.ServiceRootZFS = plan.NewRootZFS
	return updated, nil
}

func (a *hostStorageApplier) moveCatchRoot(ctx context.Context, plan catchrpc.HostStoragePlan, w io.Writer) error {
	if !plan.CatchAction.Move {
		return nil
	}
	catchService, err := a.currentCatchSystemdService()
	if err != nil {
		return err
	}
	move := serviceRootMigrationPlan{
		ServiceName: CatchService,
		OldRoot:     cleanHostStoragePath(plan.CatchAction.From),
		OldRootZFS:  strings.TrimSpace(catchService.ServiceRootZFS),
		NewRoot:     cleanHostStoragePath(plan.CatchAction.To),
		NewRootZFS:  strings.TrimSpace(plan.CatchAction.ToZFS),
		Mode:        serviceRootMigrationCopy,
	}
	if err := prepareHostStorageServiceMoveTarget(move); err != nil {
		return hostStorageCatchMoveRecoveryError(move, err)
	}
	if err := a.ops.materializeServiceRootMigration(ctx, move, w); err != nil {
		return hostStorageCatchMoveRecoveryError(move, err)
	}
	return a.updateCatchRootAfterMove(ctx, plan, move)
}

func (a *hostStorageApplier) updateCatchRootAfterMove(ctx context.Context, plan catchrpc.HostStoragePlan, move serviceRootMigrationPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := a.dataStore()
	if err != nil {
		return err
	}
	desiredConfig := a.desiredConfig(plan)
	_, err = store.MutateData(func(d *db.Data) error {
		service, ok := d.Services[CatchService]
		if !ok || service == nil {
			return fmt.Errorf("catch service is not configured")
		}
		currentRoot := cleanHostStoragePath(serviceRootFromConfig(a.config, *service))
		if !hostStoragePathsEqual(currentRoot, move.OldRoot) {
			return fmt.Errorf("catch service root changed during host storage planning")
		}
		if strings.TrimSpace(service.ServiceRootZFS) != strings.TrimSpace(move.OldRootZFS) {
			return fmt.Errorf("catch service root zfs changed during host storage planning")
		}
		updated := service.Clone()
		applyServiceRoot(desiredConfig, CatchService, updated, move.NewRoot, move.NewRootZFS)
		d.Services[CatchService] = updated
		return nil
	})
	return err
}

func (a *hostStorageApplier) moveDataDir(ctx context.Context, plan catchrpc.HostStoragePlan, tx *hostStorageTransaction, w io.Writer) error {
	if !plan.DataDirAction.Move {
		return nil
	}
	from := cleanHostStoragePath(plan.DataDirAction.From)
	to := cleanHostStoragePath(plan.DataDirAction.To)
	if from == "" {
		from = cleanHostStoragePath(plan.Current.DataDir)
	}
	if to == "" {
		to = cleanHostStoragePath(plan.Desired.DataDir)
	}
	opts := hostStorageDataDirCopyOptions{
		AllowedExistingRoots: hostStorageDataDirAllowedTargetRoots(plan),
		ExcludeRoots:         hostStorageDataDirExcludedSourceRoots(plan),
	}
	if tx != nil && tx.TargetJournal != "" {
		opts.AllowedExistingRoots = append(opts.AllowedExistingRoots, filepath.Dir(tx.TargetJournal))
	}
	if err := a.ops.copyDataDir(ctx, from, to, opts); err != nil {
		return hostStorageDataDirRecoveryError(plan, "copy data dir", err)
	}
	if plan.Legacy.Eligible && hostStoragePathsEqual(to, managedHostStorageDataDir) {
		if err := a.ops.applyManagedTargetLayout(to); err != nil {
			return hostStorageDataDirRecoveryError(plan, "apply managed target modes", err)
		}
	}
	return nil
}

func (a *hostStorageApplier) rewriteTargetDataStore(ctx context.Context, plan catchrpc.HostStoragePlan, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mappings, err := a.activeHostStorageDBRewriteMappings(ctx, plan)
	if err != nil {
		return err
	}
	if len(mappings) == 0 {
		return nil
	}
	store := a.finalConfig(plan).DB
	if store == nil {
		return fmt.Errorf("host storage rewrite requires target db store")
	}
	_, err = store.MutateData(func(d *db.Data) error {
		result, err := rewriteHostStorageDataPaths(d, mappings)
		if err != nil {
			return err
		}
		if result.Changed > 0 {
			writef(w, "Rewrote %d host storage database reference%s.\n", result.Changed, hostStoragePluralSuffix(result.Changed))
		}
		return nil
	})
	return err
}

func (a *hostStorageApplier) repairGeneratedArtifactsAndUnits(ctx context.Context, plan catchrpc.HostStoragePlan, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mappings, err := a.activeHostStorageDBRewriteMappings(ctx, plan)
	if err != nil {
		return err
	}
	if len(mappings) == 0 {
		return nil
	}
	artifactMappings := hostStorageGeneratedArtifactRewriteMappings(mappings)
	cfg := a.finalConfig(plan)
	if cfg.DB == nil {
		return fmt.Errorf("host storage artifact repair requires target db store")
	}
	dv, err := cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("load rewritten data for artifact repair: %w", err)
	}
	catchRunner := hostStorageCatchRunnerFromData(cfg, dv.AsStruct())
	var summary hostStorageUnitRepairSummary
	for _, sv := range dv.Services().All() {
		service := sv.AsStruct()
		serviceSummary, err := a.repairGeneratedArtifactsAndUnitsForService(ctx, cfg, service, artifactMappings, catchRunner)
		if err != nil {
			return err
		}
		summary.add(serviceSummary)
	}
	if err := a.reloadAndEnableHostStorageUnits(ctx, summary.enableUnits); err != nil {
		return err
	}
	summary.write(w)
	return nil
}

type hostStorageUnitRepairSummary struct {
	artifacts      int
	reinstalled    int
	regeneratedVMs int
	enableUnits    []string
}

func (s *hostStorageUnitRepairSummary) add(other hostStorageUnitRepairSummary) {
	s.artifacts += other.artifacts
	s.reinstalled += other.reinstalled
	s.regeneratedVMs += other.regeneratedVMs
	s.enableUnits = append(s.enableUnits, other.enableUnits...)
}

func (s hostStorageUnitRepairSummary) units() int {
	return s.reinstalled + s.regeneratedVMs
}

func (s hostStorageUnitRepairSummary) write(w io.Writer) {
	if s.artifacts > 0 {
		writef(w, "Rewrote %d generated host storage artifact%s.\n", s.artifacts, hostStoragePluralSuffix(s.artifacts))
	}
	if units := s.units(); units > 0 {
		writef(w, "Regenerated host storage units for %d service%s.\n", units, hostStoragePluralSuffix(units))
	}
}

func (a *hostStorageApplier) reloadAndEnableHostStorageUnits(ctx context.Context, units []string) error {
	units = uniqueSortedStrings(units)
	if len(units) == 0 {
		return nil
	}
	if err := a.ops.reloadSystemd(ctx); err != nil {
		return fmt.Errorf("reload systemd after host storage unit repair: %w", err)
	}
	if err := a.ops.enableSystemdUnits(ctx, units); err != nil {
		return err
	}
	return nil
}

func (a *hostStorageApplier) repairGeneratedArtifactsAndUnitsForService(ctx context.Context, cfg Config, service *db.Service, mappings hostStoragePathMappings, catchRunner string) (hostStorageUnitRepairSummary, error) {
	var summary hostStorageUnitRepairSummary
	if service == nil {
		return summary, nil
	}
	result, err := repairHostStorageGeneratedArtifacts(service, mappings)
	if err != nil {
		return summary, err
	}
	summary.artifacts = result.Rewritten
	if hostStorageSelfManagedService(service.Name) {
		return summary, nil
	}
	if service.ServiceType == db.ServiceTypeVM {
		return a.regenerateHostStorageVMUnit(ctx, cfg, service, catchRunner, summary)
	}
	if result.SystemdArtifactsRepaired || len(mappings) > 0 && serviceHasHostStorageSystemdArtifacts(service) {
		return a.reinstallHostStorageServiceUnits(ctx, cfg, service, summary)
	}
	return summary, nil
}

func (a *hostStorageApplier) regenerateHostStorageVMUnit(ctx context.Context, cfg Config, service *db.Service, catchRunner string, summary hostStorageUnitRepairSummary) (hostStorageUnitRepairSummary, error) {
	units, err := a.ops.regenerateVMUnit(ctx, cfg, service, catchRunner)
	if err != nil {
		return summary, fmt.Errorf("regenerate VM systemd unit %s: %w", service.Name, err)
	}
	summary.regeneratedVMs = 1
	summary.enableUnits = append(summary.enableUnits, units...)
	return summary, nil
}

func (a *hostStorageApplier) reinstallHostStorageServiceUnits(ctx context.Context, cfg Config, service *db.Service, summary hostStorageUnitRepairSummary) (hostStorageUnitRepairSummary, error) {
	units, err := a.ops.reinstallServiceUnits(ctx, cfg, service)
	if err != nil {
		return summary, err
	}
	summary.reinstalled = 1
	summary.enableUnits = append(summary.enableUnits, units...)
	return summary, nil
}

func (a *hostStorageApplier) activeHostStorageDBRewriteMappings(ctx context.Context, plan catchrpc.HostStoragePlan) (hostStoragePathMappings, error) {
	if len(hostStorageMappingsFromPlan(plan)) > 0 {
		return hostStorageDBRewriteMappingsFromPlan(plan), nil
	}
	mappings, err := a.activeHostStorageMappings(ctx, plan)
	if err != nil {
		return nil, err
	}
	return hostStorageGuardRepairOnlyUnchangedServicesRootMappings(plan, mappings), nil
}

func hostStorageGuardRepairOnlyUnchangedServicesRootMappings(plan catchrpc.HostStoragePlan, mappings hostStoragePathMappings) hostStoragePathMappings {
	if hostStorageMappingsContainReason(mappings, hostStoragePathReasonDataDir) &&
		strings.TrimSpace(plan.Current.ServicesRoot) != "" &&
		hostStoragePathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot) {
		mappings = append(mappings, hostStoragePathMapping{
			From:   plan.Current.ServicesRoot,
			To:     plan.Current.ServicesRoot,
			Reason: hostStoragePathReasonServicesDir,
		})
	}
	return mappings.Sorted()
}

func hostStorageMappingsContainReason(mappings hostStoragePathMappings, reason hostStoragePathReason) bool {
	for _, mapping := range mappings {
		if mapping.Reason == reason {
			return true
		}
	}
	return false
}

func (a *hostStorageApplier) activeHostStorageMappings(ctx context.Context, plan catchrpc.HostStoragePlan) (hostStoragePathMappings, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mappings := hostStorageMappingsFromPlan(plan)
	if len(mappings) > 0 {
		return mappings, nil
	}
	if plan.RepairAction.References == 0 {
		return nil, nil
	}
	cfg := a.finalConfig(plan)
	if cfg.DB == nil {
		return nil, fmt.Errorf("host storage repair requires target db store")
	}
	dv, err := cfg.DB.Get()
	if err != nil {
		return nil, fmt.Errorf("load data for host storage repair mapping: %w", err)
	}
	data := dv.AsStruct()
	catchRoot := hostStorageCatchRootFromData(cfg, data)
	legacyDataDirs := hostStorageLegacyRepairDataDirs(cfg)
	mappings = hostStorageKnownLegacyRepairMappings(plan.Desired, catchRoot, legacyDataDirs)
	if len(plan.RepairAction.ValidationRoots) > 0 {
		mappings = filterHostStorageMappingsBySources(mappings, plan.RepairAction.ValidationRoots)
	}
	if len(mappings) > 0 {
		return mappings, nil
	}
	probeMappings := hostStorageKnownLegacyRepairMappings(plan.Desired, catchRoot, legacyDataDirs)
	dbRefs := scanHostStorageDataRefs(data, probeMappings)
	systemdRefs, err := scanHostStorageSystemdRefs(systemdSystemDir, probeMappings)
	if err != nil {
		return nil, err
	}
	artifactRefs, err := scanHostStorageGeneratedArtifactRefs(data, probeMappings)
	if err != nil {
		return nil, err
	}
	refs := append(dbRefs, systemdRefs...)
	refs = append(refs, artifactRefs...)
	return inferredHostStorageRepairMappings(plan.Desired, catchRoot, refs, legacyDataDirs), nil
}

func hostStorageCatchRootFromData(cfg Config, data *db.Data) string {
	if data != nil {
		if service := data.Services[CatchService]; service != nil {
			return cleanHostStoragePath(serviceRootFromConfig(cfg, *service))
		}
	}
	return cleanHostStoragePath(filepath.Join(cfg.ServicesRoot, CatchService))
}

func hostStorageCatchRunnerFromData(cfg Config, data *db.Data) string {
	return filepath.Join(serviceRunDirForRoot(hostStorageCatchRootFromData(cfg, data)), "catch")
}

func filterHostStorageMappingsBySources(mappings hostStoragePathMappings, roots []string) hostStoragePathMappings {
	if len(roots) == 0 {
		return mappings.Sorted()
	}
	var out hostStoragePathMappings
	for _, mapping := range mappings {
		from := cleanHostStoragePath(mapping.From)
		for _, root := range roots {
			root = cleanHostStoragePath(root)
			if from == root || hostStorageRootContains(root, from) {
				out = append(out, mapping)
				break
			}
		}
	}
	return out.Sorted()
}

func (a *hostStorageApplier) preflightDataDirTarget(plan catchrpc.HostStoragePlan) error {
	if !plan.DataDirAction.Move {
		return nil
	}
	from := cleanHostStoragePath(plan.DataDirAction.From)
	to := cleanHostStoragePath(plan.DataDirAction.To)
	if from == "" {
		from = cleanHostStoragePath(plan.Current.DataDir)
	}
	if to == "" {
		to = cleanHostStoragePath(plan.Desired.DataDir)
	}
	_, to, err := hostStorageDataDirCopyPaths(from, to)
	if err != nil {
		return err
	}
	if to == "" {
		return nil
	}
	return ensureHostStorageDataDirTargetCompatible(to)
}

func (a *hostStorageApplier) reinstallRestartAndVerifyCatch(ctx context.Context, plan catchrpc.HostStoragePlan, w io.Writer) (bool, bool, error) {
	if !hostStoragePlanNeedsCatchRestart(plan) {
		return false, false, nil
	}
	finalConfig := a.finalConfig(plan)
	req, err := a.hostStorageInstallRequest(plan, finalConfig)
	if err != nil {
		return false, false, a.wrapCatchRestartError(plan, "prepare catch reinstall", err)
	}
	if err := prepareDesiredHostStorageCatchService(req); err != nil {
		return false, false, a.wrapCatchRestartError(plan, "prepare catch reinstall", err)
	}
	if err := a.ops.reinstallCatchUnit(ctx, req, w); err != nil {
		return false, false, a.wrapCatchRestartError(plan, "reinstall catch unit", err)
	}
	if err := a.ops.restartCatch(ctx, req, w); err != nil {
		if errors.Is(err, errHostStorageCatchRestartScheduled) {
			return false, true, nil
		}
		return false, false, a.wrapCatchRestartError(plan, "restart catch", err)
	}
	info, err := a.ops.verifyCatchInfo(ctx, plan.Desired, finalConfig)
	if err != nil {
		return false, false, a.wrapCatchRestartError(plan, "verify catch info", err)
	}
	if !hostStoragePathsEqual(info.RootDir, plan.Desired.DataDir) || !hostStoragePathsEqual(info.ServicesDir, plan.Desired.ServicesRoot) {
		err := fmt.Errorf("catch reported data dir %q and services root %q", info.RootDir, info.ServicesDir)
		return false, false, a.wrapCatchRestartError(plan, "verify catch info", err)
	}
	return true, false, nil
}

func (a *hostStorageApplier) hostStorageInstallRequest(plan catchrpc.HostStoragePlan, desiredConfig Config) (hostStorageInstallRequest, error) {
	catchService, err := a.currentCatchSystemdService()
	if err != nil {
		return hostStorageInstallRequest{}, err
	}
	catchRoot := cleanHostStoragePath(serviceRootFromConfig(a.config, *catchService))
	if plan.CatchAction.Move {
		catchRoot = cleanHostStoragePath(plan.CatchAction.To)
	}
	pinCatchRoot := strings.TrimSpace(catchService.ServiceRoot) != "" ||
		strings.TrimSpace(catchService.ServiceRootZFS) != "" ||
		!hostStoragePathsEqual(catchRoot, filepath.Join(desiredConfig.ServicesRoot, CatchService))
	if plan.CatchAction.Move {
		pinCatchRoot = false
	}
	return hostStorageInstallRequest{
		DataDir:             desiredConfig.RootDir,
		DataDirZFS:          plan.Desired.DataDirZFS,
		ServicesRoot:        desiredConfig.ServicesRoot,
		ServicesZFS:         plan.Desired.ServicesZFS,
		Config:              desiredConfig,
		CatchServiceRoot:    catchRoot,
		CatchServiceRootZFS: catchService.ServiceRootZFS,
		PinCatchServiceRoot: pinCatchRoot,
	}, nil
}

func (a *hostStorageApplier) wrapCatchRestartError(plan catchrpc.HostStoragePlan, step string, err error) error {
	if plan.DataDirAction.Move {
		return hostStorageDataDirRecoveryError(plan, step, err)
	}
	return fmt.Errorf("%s: %w", step, err)
}

func (a *hostStorageApplier) restartPreviouslyRunningServices(ctx context.Context, moves []hostStorageServiceApplyMove, tx *hostStorageTransaction) error {
	for _, move := range moves {
		if !move.move.WasRunning {
			continue
		}
		runner, err := a.ops.runnerForService(ctx, move.move.Name)
		if err != nil {
			return fmt.Errorf("runner for service %q: %w", move.move.Name, err)
		}
		if err := runner.Start(); err != nil {
			return fmt.Errorf("start service %q after host storage apply: %w", move.move.Name, err)
		}
		if tx != nil {
			tx.RestartedServices = uniqueSortedStrings(append(tx.RestartedServices, move.move.Name))
			if err := persistHostStorageTransaction(tx); err != nil {
				return fmt.Errorf("record restarted service %q: %w", move.move.Name, err)
			}
		}
	}
	return nil
}

func (a *hostStorageApplier) dataStore() (*db.Store, error) {
	store := a.store
	if store == nil {
		store = a.config.DB
	}
	if store == nil {
		return nil, fmt.Errorf("host storage applier requires a db store")
	}
	return store, nil
}

func (a *hostStorageApplier) desiredConfig(plan catchrpc.HostStoragePlan) Config {
	cfg := a.config
	cfg.RootDir = cleanHostStoragePath(plan.Desired.DataDir)
	cfg.ServicesRoot = cleanHostStoragePath(plan.Desired.ServicesRoot)
	if cfg.RootDir == "" {
		cfg.RootDir = a.config.RootDir
	}
	if cfg.ServicesRoot == "" {
		cfg.ServicesRoot = a.config.ServicesRoot
	}
	cfg.MountsRoot = filepath.Join(cfg.RootDir, "mounts")
	cfg.RegistryRoot = filepath.Join(cfg.RootDir, "registry")
	return cfg
}

func (a *hostStorageApplier) finalConfig(plan catchrpc.HostStoragePlan) Config {
	cfg := a.desiredConfig(plan)
	if hostStoragePathsEqual(cfg.RootDir, a.config.RootDir) && a.config.DB != nil {
		cfg.DB = a.config.DB
	} else {
		cfg.DB = db.NewStore(filepath.Join(cfg.RootDir, "db.json"), cfg.ServicesRoot)
	}
	return cfg
}

func hostStorageApplyResultMoves(moves []hostStorageServiceApplyMove) []catchrpc.HostStorageServiceMove {
	if len(moves) == 0 {
		return nil
	}
	var out []catchrpc.HostStorageServiceMove
	for _, move := range moves {
		if move.pin {
			continue
		}
		out = append(out, move.move)
	}
	return out
}

func hostStorageServiceRootApplyMoves(moves []hostStorageServiceApplyMove) []hostStorageServiceApplyMove {
	out := make([]hostStorageServiceApplyMove, 0, len(moves))
	for _, move := range moves {
		if strings.TrimSpace(move.plan.OldRoot) == "" && strings.TrimSpace(move.plan.NewRoot) == "" {
			continue
		}
		out = append(out, move)
	}
	return out
}

func hostStoragePlanNeedsCatchRestart(plan catchrpc.HostStoragePlan) bool {
	return plan.RequiresRestart ||
		plan.RepairAction.References > 0 ||
		plan.DataDirAction.Move ||
		!hostStoragePathsEqual(plan.Current.DataDir, plan.Desired.DataDir) ||
		!hostStoragePathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot)
}

type hostStorageValidationResult struct {
	ActiveRefs  int
	DBRefs      int
	SystemdRefs int
	Examples    []hostStorageReference
}

func (a *hostStorageApplier) validateHostStorageApply(ctx context.Context, plan catchrpc.HostStoragePlan) (hostStorageValidationResult, error) {
	if err := ctx.Err(); err != nil {
		return hostStorageValidationResult{}, err
	}
	mappings, err := a.activeHostStorageDBRewriteMappings(ctx, plan)
	if err != nil {
		return hostStorageValidationResult{}, err
	}
	if len(mappings) == 0 {
		return hostStorageValidationResult{}, nil
	}
	cfg := a.finalConfig(plan)
	if cfg.DB == nil {
		return hostStorageValidationResult{}, fmt.Errorf("host storage validation requires target db store")
	}
	dv, err := cfg.DB.Get()
	if err != nil {
		return hostStorageValidationResult{}, fmt.Errorf("load data for host storage validation: %w", err)
	}
	return validateHostStorageNoActiveRefs(dv.AsStruct(), systemdSystemDir, mappings)
}

func validateHostStorageNoActiveRefs(data *db.Data, systemdDir string, mappings hostStoragePathMappings) (hostStorageValidationResult, error) {
	var result hostStorageValidationResult
	dbRefs := scanHostStorageDataRefs(data, mappings)
	systemdRefs, err := scanHostStorageSystemdRefs(systemdDir, mappings)
	if err != nil {
		return result, err
	}
	result.DBRefs = len(dbRefs)
	result.SystemdRefs = len(systemdRefs)
	result.ActiveRefs = result.DBRefs + result.SystemdRefs
	result.Examples = append(result.Examples, dbRefs...)
	result.Examples = append(result.Examples, systemdRefs...)
	if len(result.Examples) > 5 {
		result.Examples = result.Examples[:5]
	}
	if result.ActiveRefs != 0 {
		return result, fmt.Errorf("host storage validation found %d active old-root reference%s: %s", result.ActiveRefs, hostStoragePluralSuffix(result.ActiveRefs), formatHostStorageValidationExamples(result.Examples))
	}
	return result, nil
}

func formatHostStorageValidationExamples(refs []hostStorageReference) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		switch ref.Kind {
		case hostStorageReferenceSystemd:
			parts = append(parts, fmt.Sprintf("%s:%d %s", ref.Unit, ref.Line, ref.Path))
		case hostStorageReferenceDB:
			parts = append(parts, fmt.Sprintf("db %s %s %s", ref.Service, ref.Field, ref.Path))
		default:
			parts = append(parts, ref.Path)
		}
	}
	return strings.Join(parts, "; ")
}

func hostStorageServiceMoveRecoveryError(plan serviceRootMigrationPlan, err error) error {
	return fmt.Errorf("move service root for %q from %q to %q failed: %w", plan.ServiceName, plan.OldRoot, plan.NewRoot, err)
}

func hostStorageCatchMoveRecoveryError(plan serviceRootMigrationPlan, err error) error {
	return fmt.Errorf("move catch service root from %q to %q failed: %v. Old catch service root remains at %q; repair the target and retry host storage apply", plan.OldRoot, plan.NewRoot, err, plan.OldRoot)
}

func hostStorageDataDirRecoveryError(plan catchrpc.HostStoragePlan, step string, err error) error {
	from := cleanHostStoragePath(plan.DataDirAction.From)
	to := cleanHostStoragePath(plan.DataDirAction.To)
	if from == "" {
		from = cleanHostStoragePath(plan.Current.DataDir)
	}
	if to == "" {
		to = cleanHostStoragePath(plan.Desired.DataDir)
	}
	return fmt.Errorf("host data dir change from %q to %q failed during %s: %v. Old data dir remains at %q; restore the catch unit to --data-dir=%q or retry after fixing %q", from, to, step, err, from, from, to)
}

func hostStoragePluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func installHostStorageCatchUnit(ctx context.Context, req hostStorageInstallRequest, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := req.Config
	server := NewUnstartedServer(&cfg)
	fileInstaller, err := newHostStorageCatchUnitFileInstaller(server, req, w)
	if err != nil {
		return err
	}
	defer cleanupHostStorageFileInstaller(fileInstaller)

	if err := stageHostStorageCatchUnitDefinition(fileInstaller, req); err != nil {
		return err
	}
	return installHostStorageCatchDefinition(server, w)
}

func newHostStorageCatchUnitFileInstaller(server *Server, req hostStorageInstallRequest, w io.Writer) (*FileInstaller, error) {
	serviceRoot, serviceRootZFS := hostStorageCatchInstallRoot(req)
	return NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName:    CatchService,
			ServiceRoot:    serviceRoot,
			ServiceRootZFS: serviceRootZFS,
			ClientOut:      w,
		},
		NoBinary: true,
		Args:     hostStorageCatchUnitArgs(req),
	})
}

func stageHostStorageCatchUnitDefinition(fileInstaller *FileInstaller, req hostStorageInstallRequest) error {
	if err := prepareDesiredHostStorageCatchService(req); err != nil {
		return err
	}
	installPlan, err := prepareHostStorageCatchUnitInstallPlan(fileInstaller)
	if err != nil {
		return err
	}
	if err := fileInstaller.stageInstallPlan(installPlan); err != nil {
		return err
	}
	return nil
}

func prepareHostStorageCatchUnitInstallPlan(fileInstaller *FileInstaller) (fileInstallPlan, error) {
	installPlan, err := fileInstaller.prepareNoBinaryInstall()
	if err != nil {
		return fileInstallPlan{}, err
	}
	if installPlan.detectedServiceType != db.ServiceTypeSystemd {
		return fileInstallPlan{}, fmt.Errorf("catch service must be systemd-backed, got %s", installPlan.detectedServiceType)
	}
	return installPlan, nil
}

func installHostStorageCatchDefinition(server *Server, w io.Writer) error {
	installer, err := server.NewInstaller(InstallerCfg{
		ServiceName: CatchService,
		ClientOut:   w,
	})
	if err != nil {
		return err
	}
	_, catchService, err := installer.commitGen(0)
	if err != nil {
		return err
	}
	installer.prune()
	return installer.installDefinitionOnly(catchService)
}

func restartHostStorageCatch(ctx context.Context, _ hostStorageInstallRequest, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unit := fmt.Sprintf("yeet-catch-restart-%d", time.Now().UnixNano())
	args := []string{
		"--unit=" + unit,
		"--collect",
		"--on-active=1s",
		"systemctl",
		"restart",
		CatchService + ".service",
	}
	if err := hostStorageRunCommand(ctx, "systemd-run", args...); err != nil {
		return fmt.Errorf("schedule catch restart: %w", err)
	}
	writef(w, "Scheduled catch restart.\n")
	return errHostStorageCatchRestartScheduled
}

func cancelHostStorageCatchRestarts(ctx context.Context) error {
	if err := hostStorageRunCommand(ctx, "systemctl", "stop", "yeet-catch-restart-*.timer", "yeet-catch-restart-*.service"); err != nil {
		return err
	}
	return nil
}

func runHostStorageCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to run %s %s: %w\n%s", name, strings.Join(args, " "), err, string(out))
	}
	return nil
}

func reloadHostStorageSystemd(ctx context.Context) error {
	if err := hostStorageRunCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return nil
}

func enableHostStorageSystemdUnits(ctx context.Context, units []string) error {
	for _, unit := range units {
		if err := hostStorageRunCommand(ctx, "systemctl", "enable", unit); err != nil {
			return fmt.Errorf("enable repaired unit %s: %w", unit, err)
		}
	}
	return nil
}

func verifyHostStorageCatchInfo(ctx context.Context, _ catchrpc.HostStorageState, cfg Config) (ServerInfo, error) {
	if err := ctx.Err(); err != nil {
		return ServerInfo{}, err
	}
	return GetInfoWithConfig(&cfg), nil
}

func prepareDesiredHostStorageCatchService(req hostStorageInstallRequest) error {
	if req.Config.DB == nil {
		return fmt.Errorf("host storage catch reinstall requires a db store")
	}
	dv, err := req.Config.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get data: %v", err)
	}
	if _, err := hostStorageCatchSystemdServiceFromData(dv); err != nil {
		return err
	}
	if !req.PinCatchServiceRoot {
		return nil
	}
	_, err = req.Config.DB.MutateData(func(d *db.Data) error {
		service, ok := d.Services[CatchService]
		if !ok || service == nil {
			return fmt.Errorf("catch service is not configured")
		}
		if service.ServiceType != db.ServiceTypeSystemd {
			return fmt.Errorf("catch service must be systemd-backed, got %s", service.ServiceType)
		}
		applyServiceRoot(req.Config, CatchService, service, req.CatchServiceRoot, req.CatchServiceRootZFS)
		return nil
	})
	return err
}

func hostStorageCatchInstallRoot(req hostStorageInstallRequest) (string, bool) {
	if !req.PinCatchServiceRoot {
		return "", false
	}
	if strings.TrimSpace(req.CatchServiceRootZFS) != "" {
		return req.CatchServiceRootZFS, true
	}
	return req.CatchServiceRoot, false
}

func hostStorageCatchUnitArgs(req hostStorageInstallRequest) []string {
	return []string{
		"--data-dir=" + req.DataDir,
		"--services-root=" + req.ServicesRoot,
		"--tsnet-host=" + req.Config.TSNetHost,
	}
}

func cleanupHostStorageFileInstaller(installer *FileInstaller) {
	if installer == nil {
		return
	}
	if installer.File != nil {
		_ = installer.File.Close()
	}
	installer.cleanupTemp()
}

func copyHostStorageDataDir(ctx context.Context, from string, to string, options ...hostStorageDataDirCopyOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var opts hostStorageDataDirCopyOptions
	if len(options) > 0 {
		opts = options[0]
	}
	from, to, err := hostStorageDataDirCopyPaths(from, to)
	if err != nil {
		return err
	}
	if from == "" || to == "" {
		return nil
	}
	if err := ensureHostStorageDataDirTargetCompatible(to, opts.AllowedExistingRoots...); err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(to, 0o700); err != nil {
		return err
	}
	return copyHostStorageDataDirContents(from, to, opts.ExcludeRoots)
}

func hostStorageDataDirCopyPaths(from string, to string) (string, string, error) {
	from = cleanHostStoragePath(from)
	to = cleanHostStoragePath(to)
	if from == "" || to == "" || from == to {
		return "", "", nil
	}
	if rootsAreNested(from, to) || rootsAreNested(to, from) {
		return "", "", fmt.Errorf("cannot copy host data dir between nested paths: %s and %s", from, to)
	}
	return from, to, nil
}

func copyHostStorageDataDirContents(from string, to string, excludeRoots []string) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := copyutil.TarDirectoryWithOptions(pw, from, "", copyutil.TarOptions{
			Filter: hostStorageDataDirTarFilter(from, excludeRoots),
		})
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		errCh <- err
	}()

	extractErr := copyutil.ExtractTar(pr, to)
	if extractErr != nil {
		_ = pr.CloseWithError(extractErr)
	}
	tarErr := <-errCh
	if extractErr != nil {
		return extractErr
	}
	return tarErr
}

func hostStorageDataDirTarFilter(root string, excludeRoots []string) copyutil.TarFilter {
	excludes := cleanHostStorageExcludeRoots(root, excludeRoots)
	if len(excludes) == 0 {
		return nil
	}
	return func(path string, _ os.DirEntry) (bool, error) {
		path = cleanHostStoragePath(path)
		for _, exclude := range excludes {
			if hostStorageRootContains(exclude, path) {
				return false, nil
			}
		}
		return true, nil
	}
}

func cleanHostStorageExcludeRoots(root string, excludeRoots []string) []string {
	root = cleanHostStoragePath(root)
	if root == "" || len(excludeRoots) == 0 {
		return nil
	}
	cleaned := make([]string, 0, len(excludeRoots))
	for _, exclude := range excludeRoots {
		exclude = cleanHostStoragePath(exclude)
		if exclude == "" || !hostStorageRootContains(root, exclude) {
			continue
		}
		cleaned = append(cleaned, exclude)
	}
	return uniqueSortedStrings(cleaned)
}

func ensureHostStorageDataDirTargetCompatible(target string, allowedExistingRoots ...string) error {
	entries, err := hostStorageDataDirTargetEntries(target)
	if err != nil {
		return err
	}
	if len(entries) > 0 && !hostStorageDataDirEntriesCompatible(entries) {
		if !hostStorageDataDirOnlyContainsAllowedRoots(target, allowedExistingRoots) {
			return fmt.Errorf("target data dir %q is not empty and does not look like a compatible catch data directory", target)
		}
	}
	return nil
}

func hostStorageDataDirAllowedTargetRoots(plan catchrpc.HostStoragePlan) []string {
	if !plan.DataDirAction.Move || plan.ServicesAction.Mode != catchrpc.HostStorageMigrateAll {
		return nil
	}
	dataDir := cleanHostStoragePath(plan.Desired.DataDir)
	servicesRoot := cleanHostStoragePath(plan.Desired.ServicesRoot)
	if dataDir == "" || servicesRoot == "" || !hostStorageRootContains(dataDir, servicesRoot) {
		return nil
	}
	return []string{servicesRoot}
}

func hostStorageDataDirExcludedSourceRoots(plan catchrpc.HostStoragePlan) []string {
	fromDataDir, toDataDir, fromServicesRoot, toServicesRoot, ok := hostStorageDataDirExclusionRoots(plan)
	if !ok {
		return nil
	}
	if !hostStorageRootContains(fromDataDir, fromServicesRoot) {
		return nil
	}
	if hostStorageRootContains(toDataDir, toServicesRoot) {
		return nil
	}
	return []string{fromServicesRoot}
}

func hostStorageDataDirExclusionRoots(plan catchrpc.HostStoragePlan) (string, string, string, string, bool) {
	if !plan.DataDirAction.Move || plan.ServicesAction.Mode != catchrpc.HostStorageMigrateAll {
		return "", "", "", "", false
	}
	fromDataDir := cleanHostStoragePath(plan.DataDirAction.From)
	if fromDataDir == "" {
		fromDataDir = cleanHostStoragePath(plan.Current.DataDir)
	}
	toDataDir := cleanHostStoragePath(plan.DataDirAction.To)
	if toDataDir == "" {
		toDataDir = cleanHostStoragePath(plan.Desired.DataDir)
	}
	fromServicesRoot := cleanHostStoragePath(plan.Current.ServicesRoot)
	toServicesRoot := cleanHostStoragePath(plan.Desired.ServicesRoot)
	if fromDataDir == "" || toDataDir == "" || fromServicesRoot == "" || toServicesRoot == "" {
		return "", "", "", "", false
	}
	return fromDataDir, toDataDir, fromServicesRoot, toServicesRoot, true
}

func hostStorageDataDirOnlyContainsAllowedRoots(target string, allowedRoots []string) bool {
	target = cleanHostStoragePath(target)
	allowedRoots = cleanHostStorageAllowedRootsUnderTarget(target, allowedRoots)
	if target == "" || len(allowedRoots) == 0 {
		return false
	}
	ok := true
	err := filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
		entryOK, walkErr := hostStorageDataDirAllowedRootWalk(target, allowedRoots, path, entry, err)
		ok = ok && entryOK
		return walkErr
	})
	return err == nil && ok
}

func hostStorageDataDirAllowedRootWalk(target string, allowedRoots []string, entryPath string, entry os.DirEntry, entryErr error) (bool, error) {
	if entryErr != nil {
		return false, entryErr
	}
	entryPath = cleanHostStoragePath(entryPath)
	switch {
	case entryPath == target:
		return true, nil
	case hostStoragePathWithinAnyRoot(entryPath, allowedRoots):
		return true, hostStorageSkipDir(entry)
	case entry.IsDir() && hostStoragePathParentOfAnyRoot(entryPath, allowedRoots):
		return true, nil
	case entry.IsDir():
		return false, filepath.SkipDir
	default:
		return false, nil
	}
}

func hostStorageSkipDir(entry os.DirEntry) error {
	if entry.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func cleanHostStorageAllowedRootsUnderTarget(target string, roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = cleanHostStoragePath(root)
		if root != "" && root != target && hostStorageRootContains(target, root) {
			out = append(out, root)
		}
	}
	return uniqueSortedStrings(out)
}

func hostStoragePathWithinAnyRoot(candidate string, roots []string) bool {
	for _, root := range roots {
		if root == candidate || hostStorageRootContains(root, candidate) {
			return true
		}
	}
	return false
}

func hostStoragePathParentOfAnyRoot(candidate string, roots []string) bool {
	for _, root := range roots {
		if hostStorageRootContains(candidate, root) {
			return true
		}
	}
	return false
}

func hostStorageDataDirTargetEntries(target string) ([]os.DirEntry, error) {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("target data dir %q is not a directory", target)
	}
	return os.ReadDir(target)
}

func hostStorageDataDirEntriesCompatible(entries []os.DirEntry) bool {
	hasDB := false
	hasRegistry := false
	for _, entry := range entries {
		if !hostStorageDataDirEntryNameCompatible(entry.Name()) {
			return false
		}
		switch entry.Name() {
		case "db.json":
			if entry.IsDir() {
				return false
			}
			hasDB = true
		case "registry":
			if !entry.IsDir() {
				return false
			}
			hasRegistry = true
		}
	}
	return hasDB && hasRegistry
}

func hostStorageDataDirEntryNameCompatible(name string) bool {
	switch name {
	case "backups", "catch.lock", "db.json", "id_ed25519", "install.json", isoOperationLockFileName, "mounts", "registry", "services", "tsd", "tsnet", "vm-images":
		return true
	default:
		return strings.HasPrefix(name, "db.json.")
	}
}

func (p *hostStoragePlanner) currentState() catchrpc.HostStorageState {
	return catchrpc.HostStorageState{
		DataDir:      p.config.RootDir,
		ServicesRoot: p.config.ServicesRoot,
	}
}

func (p *hostStoragePlanner) resolveDesiredState(ctx context.Context, current catchrpc.HostStorageState, set catchrpc.HostStorageSetRequest) (catchrpc.HostStorageState, []string, []string, string, error) {
	if set.DataDir != nil && set.ServicesRoot != nil && set.DataDir.ZFS != set.ServicesRoot.ZFS {
		return catchrpc.HostStorageState{}, nil, nil, "", fmt.Errorf("mixed filesystem and ZFS storage changes must be run separately")
	}

	desired := current
	var datasetsToCreate []string
	var warnings []string
	var servicesRootDataset string
	if set.DataDir != nil {
		resolved, err := p.resolveTarget(ctx, *set.DataDir)
		if err != nil {
			return catchrpc.HostStorageState{}, nil, nil, "", err
		}
		desired.DataDir = resolved.value
		desired.DataDirZFS = resolved.zfs
		datasetsToCreate = append(datasetsToCreate, resolved.datasetsToCreate...)
		warnings = append(warnings, resolved.mountpointWarnings...)
	}
	if set.ServicesRoot != nil {
		resolved, err := p.resolveTarget(ctx, *set.ServicesRoot)
		if err != nil {
			return catchrpc.HostStorageState{}, nil, nil, "", err
		}
		desired.ServicesRoot = resolved.value
		desired.ServicesZFS = resolved.zfs
		servicesRootDataset = resolved.dataset
		datasetsToCreate = append(datasetsToCreate, resolved.datasetsToCreate...)
		warnings = append(warnings, resolved.mountpointWarnings...)
	}
	return desired, uniqueSortedStrings(datasetsToCreate), warnings, servicesRootDataset, nil
}

func (p *hostStoragePlanner) resolveTarget(ctx context.Context, target catchrpc.HostStorageTarget) (resolvedHostStorageTarget, error) {
	value := strings.TrimSpace(target.Value)
	if target.ZFS {
		return p.resolveZFSTarget(ctx, value)
	}
	if value == "" {
		return resolvedHostStorageTarget{}, fmt.Errorf("storage target must not be empty")
	}
	return resolvedHostStorageTarget{value: filepath.Clean(value)}, nil
}

func (p *hostStoragePlanner) resolveZFSTarget(ctx context.Context, dataset string) (resolvedHostStorageTarget, error) {
	dataset = strings.TrimSpace(dataset)
	if err := validateHostStorageZFSTarget(dataset); err != nil {
		return resolvedHostStorageTarget{}, err
	}
	runner := p.zfs
	if runner == nil {
		runner = runZFSCommand
	}

	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return resolvedHostStorageTarget{}, err
	}
	if !exists {
		if inferred, ok, err := inferMissingZFSHostStorageMountpoint(ctx, runner, dataset); err != nil {
			return resolvedHostStorageTarget{}, err
		} else if ok {
			return resolvedHostStorageTarget{
				value:            inferred,
				zfs:              true,
				dataset:          dataset,
				datasetsToCreate: []string{dataset},
			}, nil
		}
		return resolvedHostStorageTarget{}, fmt.Errorf("cannot resolve mountpoint for missing ZFS dataset %q", dataset)
	}

	mountpoint, err := zfsDatasetMountpoint(ctx, runner, dataset)
	if err != nil {
		return resolvedHostStorageTarget{}, err
	}
	root, warnings, err := validateZFSMountpoint(mountpoint, zfsServiceRootExisting, true)
	if err != nil {
		return resolvedHostStorageTarget{}, err
	}
	return resolvedHostStorageTarget{value: root, zfs: true, dataset: dataset, mountpointWarnings: warnings}, nil
}

func validateHostStorageZFSTarget(dataset string) error {
	if dataset == "" ||
		filepath.IsAbs(dataset) ||
		strings.HasPrefix(dataset, "./") ||
		strings.HasPrefix(dataset, "../") ||
		dataset == "." ||
		dataset == ".." ||
		path.Clean(dataset) != dataset {
		return fmt.Errorf("ZFS storage target must be a dataset name, got %q", dataset)
	}
	if err := requireZFSDatasetName(dataset); err != nil {
		return fmt.Errorf("ZFS storage target must be a dataset name, got %q: %w", dataset, err)
	}
	return nil
}

func inferMissingZFSHostStorageMountpoint(ctx context.Context, runner zfsCommandRunner, dataset string) (string, bool, error) {
	slash := strings.LastIndex(dataset, "/")
	if slash <= 0 || slash == len(dataset)-1 {
		return "", false, nil
	}
	parentDataset := dataset[:slash]
	childName := dataset[slash+1:]
	parentExists, err := zfsDatasetExists(ctx, runner, parentDataset)
	if err != nil {
		return "", false, err
	}
	if !parentExists {
		return "", false, nil
	}
	parentMountpoint, err := zfsDatasetMountpoint(ctx, runner, parentDataset)
	if err != nil {
		return "", false, err
	}
	parentRoot, _, err := validateZFSMountpoint(parentMountpoint, zfsServiceRootExisting, true)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(parentRoot, childName), true, nil
}

func planHostStorageDataDirAction(current, desired catchrpc.HostStorageState) catchrpc.HostStorageDataDirAction {
	if hostStoragePathsEqual(current.DataDir, desired.DataDir) {
		return catchrpc.HostStorageDataDirAction{}
	}
	return catchrpc.HostStorageDataDirAction{
		Move: true,
		From: current.DataDir,
		To:   desired.DataDir,
	}
}

func hostStorageStateRequiresRestart(current, desired catchrpc.HostStorageState) bool {
	return !hostStoragePathsEqual(current.DataDir, desired.DataDir) ||
		!hostStoragePathsEqual(current.ServicesRoot, desired.ServicesRoot)
}

func hostStoragePathsEqual(a, b string) bool {
	return cleanHostStoragePath(a) == cleanHostStoragePath(b)
}

func cleanHostStoragePath(value string) string {
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func (p *hostStoragePlanner) planServiceRootChange(ctx context.Context, current, desired catchrpc.HostStorageState, mode catchrpc.HostStorageMigrateServices, servicesRootDataset string) (catchrpc.HostStorageServicesAction, error) {
	if hostStoragePathsEqual(current.ServicesRoot, desired.ServicesRoot) {
		if servicesRootDataset == "" || mode != catchrpc.HostStorageMigrateAll {
			return catchrpc.HostStorageServicesAction{}, nil
		}
		services, err := p.hostStorageServices()
		if err != nil {
			return catchrpc.HostStorageServicesAction{}, err
		}
		moves, err := planServicesRootZFSChildDatasetMoves(ctx, p.config, services, desired.ServicesRoot, servicesRootDataset)
		if err != nil {
			return catchrpc.HostStorageServicesAction{}, err
		}
		if len(moves) == 0 {
			return catchrpc.HostStorageServicesAction{}, nil
		}
		return catchrpc.HostStorageServicesAction{
			Mode:             mode,
			From:             current.ServicesRoot,
			To:               desired.ServicesRoot,
			AffectedServices: moves,
		}, nil
	}
	if err := validateHostStorageMigrateMode(mode); err != nil {
		return catchrpc.HostStorageServicesAction{}, err
	}
	services, err := p.hostStorageServices()
	if err != nil {
		return catchrpc.HostStorageServicesAction{}, err
	}
	batch, err := planServicesRootBatch(ctx, p.config, services, current.ServicesRoot, desired.ServicesRoot, mode)
	if err != nil {
		return catchrpc.HostStorageServicesAction{}, err
	}
	moves := hostStorageServiceMovesFromRootBatch(batch)
	applyServicesRootDatasetToMoves(moves, servicesRootDataset, mode)
	return catchrpc.HostStorageServicesAction{
		Mode:             mode,
		From:             current.ServicesRoot,
		To:               desired.ServicesRoot,
		AffectedServices: moves,
	}, nil
}

func planServicesRootZFSChildDatasetMoves(ctx context.Context, cfg Config, services []db.Service, servicesRoot string, servicesRootDataset string) ([]catchrpc.HostStorageServiceMove, error) {
	servicesRoot = cleanHostStoragePath(servicesRoot)
	servicesRootDataset = strings.TrimSpace(servicesRootDataset)
	if servicesRoot == "" || servicesRootDataset == "" {
		return nil, nil
	}
	var moves []catchrpc.HostStorageServiceMove
	for _, service := range services {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		move, ok, err := planServicesRootZFSChildDatasetMove(cfg, service, servicesRoot, servicesRootDataset)
		if err != nil {
			return nil, err
		}
		if ok {
			moves = append(moves, move)
		}
	}
	slices.SortFunc(moves, func(a, b catchrpc.HostStorageServiceMove) int {
		return strings.Compare(a.Name, b.Name)
	})
	return moves, nil
}

func planServicesRootZFSChildDatasetMove(cfg Config, service db.Service, servicesRoot string, servicesRootDataset string) (catchrpc.HostStorageServiceMove, bool, error) {
	service.Name = strings.TrimSpace(service.Name)
	if service.Name == "" {
		return catchrpc.HostStorageServiceMove{}, false, fmt.Errorf("service root batch contains a service with no name")
	}
	currentRoot := cleanHostStoragePath(serviceRootFromConfigWithDefault(cfg, service, servicesRoot))
	targetRoot := filepath.Join(servicesRoot, service.Name)
	targetDataset := path.Join(servicesRootDataset, service.Name)
	if hostStorageServiceRootAlreadyUsesZFSChild(service, currentRoot, targetRoot, targetDataset) {
		return catchrpc.HostStorageServiceMove{}, false, nil
	}
	if !hostStorageRootContains(servicesRoot, currentRoot) &&
		!hostStorageFlatServiceRootSibling(currentRoot, service.ServiceRootZFS, servicesRoot, servicesRootDataset, service.Name) {
		return catchrpc.HostStorageServiceMove{}, false, nil
	}
	return catchrpc.HostStorageServiceMove{
		Name:  service.Name,
		From:  currentRoot,
		To:    targetRoot,
		ToZFS: targetDataset,
	}, true, nil
}

func hostStorageServiceRootAlreadyUsesZFSChild(service db.Service, currentRoot, targetRoot, targetDataset string) bool {
	currentDataset := strings.TrimSpace(service.ServiceRootZFS)
	if currentDataset == targetDataset {
		return true
	}
	return currentDataset != "" && hostStoragePathsEqual(currentRoot, targetRoot)
}

func hostStorageFlatServiceRootSibling(currentRoot, currentDataset, servicesRoot, servicesRootDataset, serviceName string) bool {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return false
	}
	if hostStoragePathIsFlatServiceSibling(currentRoot, servicesRoot, serviceName) {
		return true
	}
	return hostStorageDatasetIsFlatServiceSibling(currentDataset, servicesRootDataset, serviceName)
}

func hostStoragePathIsFlatServiceSibling(currentRoot, servicesRoot, serviceName string) bool {
	currentRoot = cleanHostStoragePath(currentRoot)
	servicesRoot = cleanHostStoragePath(servicesRoot)
	if currentRoot == "" || servicesRoot == "" || filepath.Base(currentRoot) != serviceName {
		return false
	}
	return hostStoragePathsEqual(filepath.Dir(currentRoot), filepath.Dir(servicesRoot))
}

func hostStorageDatasetIsFlatServiceSibling(currentDataset, servicesRootDataset, serviceName string) bool {
	currentDataset = strings.TrimSpace(currentDataset)
	servicesRootDataset = strings.TrimSpace(servicesRootDataset)
	if currentDataset == "" || servicesRootDataset == "" || path.Base(currentDataset) != serviceName {
		return false
	}
	return path.Dir(currentDataset) == path.Dir(servicesRootDataset)
}

func applyServicesRootDatasetToMoves(moves []catchrpc.HostStorageServiceMove, servicesRootDataset string, mode catchrpc.HostStorageMigrateServices) {
	servicesRootDataset = strings.TrimSpace(servicesRootDataset)
	if servicesRootDataset == "" || mode != catchrpc.HostStorageMigrateAll {
		return
	}
	for i := range moves {
		moves[i].ToZFS = path.Join(servicesRootDataset, moves[i].Name)
	}
}

func (p *hostStoragePlanner) planCatchRootChange(ctx context.Context, desired catchrpc.HostStorageState, set catchrpc.HostStorageSetRequest, servicesRootDataset string) (catchrpc.HostStorageCatchAction, error) {
	if err := ctx.Err(); err != nil {
		return catchrpc.HostStorageCatchAction{}, err
	}
	if set.ServicesRoot == nil || set.MigrateServices != catchrpc.HostStorageMigrateAll {
		return catchrpc.HostStorageCatchAction{}, nil
	}
	catchService, ok, err := p.currentCatchServiceForHostStorage()
	if err != nil || !ok {
		return catchrpc.HostStorageCatchAction{}, err
	}
	currentRoot := cleanHostStoragePath(serviceRootFromConfig(p.config, *catchService))
	desiredRoot, desiredZFS := hostStorageCatchDesiredRoot(desired, servicesRootDataset)
	if hostStoragePathsEqual(currentRoot, desiredRoot) {
		return hostStorageSamePathCatchRootAction(currentRoot, desiredRoot, strings.TrimSpace(catchService.ServiceRootZFS), desiredZFS), nil
	}
	action, err := hostStorageCatchRootAction(currentRoot, desiredRoot)
	if err != nil {
		return catchrpc.HostStorageCatchAction{}, err
	}
	if action.Move && desiredZFS != "" {
		action.ToZFS = desiredZFS
	}
	return action, nil
}

func hostStorageCatchDesiredRoot(desired catchrpc.HostStorageState, servicesRootDataset string) (string, string) {
	desiredRoot := filepath.Join(cleanHostStoragePath(desired.ServicesRoot), CatchService)
	desiredZFS := ""
	if strings.TrimSpace(servicesRootDataset) != "" {
		desiredZFS = path.Join(strings.TrimSpace(servicesRootDataset), CatchService)
	}
	return desiredRoot, desiredZFS
}

func hostStorageSamePathCatchRootAction(currentRoot, desiredRoot, currentZFS, desiredZFS string) catchrpc.HostStorageCatchAction {
	if desiredZFS == "" || currentZFS == desiredZFS || currentZFS != "" {
		return catchrpc.HostStorageCatchAction{}
	}
	return catchrpc.HostStorageCatchAction{
		Move:  true,
		From:  currentRoot,
		To:    desiredRoot,
		ToZFS: desiredZFS,
	}
}

func (p *hostStoragePlanner) appendServicesRootChildZFSDatasets(ctx context.Context, datasets []string, services catchrpc.HostStorageServicesAction, catchAction catchrpc.HostStorageCatchAction) ([]string, error) {
	if len(services.AffectedServices) == 0 && strings.TrimSpace(catchAction.ToZFS) == "" {
		return uniqueSortedStrings(datasets), nil
	}
	runner := p.zfs
	if runner == nil {
		runner = runZFSCommand
	}
	for _, move := range services.AffectedServices {
		var err error
		datasets, err = p.appendMissingHostStorageDataset(ctx, runner, datasets, move.ToZFS)
		if err != nil {
			return nil, err
		}
	}
	var err error
	datasets, err = p.appendMissingHostStorageDataset(ctx, runner, datasets, catchAction.ToZFS)
	if err != nil {
		return nil, err
	}
	return uniqueSortedStrings(datasets), nil
}

func (p *hostStoragePlanner) appendMissingHostStorageDataset(ctx context.Context, runner zfsCommandRunner, datasets []string, dataset string) ([]string, error) {
	dataset = strings.TrimSpace(dataset)
	if dataset == "" || slices.Contains(datasets, dataset) {
		return datasets, nil
	}
	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return nil, err
	}
	if exists {
		return datasets, nil
	}
	return append(datasets, dataset), nil
}

func (p *hostStoragePlanner) planRepairAction(ctx context.Context, plan catchrpc.HostStoragePlan) (catchrpc.HostStorageRepairAction, error) {
	if err := ctx.Err(); err != nil {
		return catchrpc.HostStorageRepairAction{}, err
	}
	dv, err := p.hostStorageDataView()
	if err != nil {
		return catchrpc.HostStorageRepairAction{}, err
	}
	data := dv.AsStruct()
	mappings := hostStorageMappingsFromPlan(plan)
	catchRoot, _, err := p.currentCatchRootForHostStorage()
	if err != nil {
		return catchrpc.HostStorageRepairAction{}, err
	}
	if len(mappings) == 0 {
		legacyDataDirs := hostStorageLegacyRepairDataDirs(p.config)
		probeMappings := hostStorageKnownLegacyRepairMappings(plan.Desired, catchRoot, legacyDataDirs)
		probeDBRefs := scanHostStorageDataRefs(data, probeMappings)
		probeSystemdRefs, err := scanHostStorageSystemdRefs(systemdSystemDir, probeMappings)
		if err != nil {
			return catchrpc.HostStorageRepairAction{}, err
		}
		probeArtifactRefs, err := scanHostStorageGeneratedArtifactRefs(data, probeMappings)
		if err != nil {
			return catchrpc.HostStorageRepairAction{}, err
		}
		probeRefs := append(probeDBRefs, probeSystemdRefs...)
		probeRefs = append(probeRefs, probeArtifactRefs...)
		mappings = inferredHostStorageRepairMappings(plan.Desired, catchRoot, probeRefs, legacyDataDirs)
	}
	if len(mappings) == 0 {
		return catchrpc.HostStorageRepairAction{}, nil
	}
	dbRefs := scanHostStorageDataRefs(data, mappings)
	systemdRefs, err := scanHostStorageSystemdRefs(systemdSystemDir, mappings)
	if err != nil {
		return catchrpc.HostStorageRepairAction{}, err
	}
	artifactRefs, err := scanHostStorageGeneratedArtifactRefs(data, mappings)
	if err != nil {
		return catchrpc.HostStorageRepairAction{}, err
	}
	return hostStorageRepairActionFromRefs(dbRefs, systemdRefs, artifactRefs, mappings), nil
}

func (p *hostStoragePlanner) currentCatchRootForHostStorage() (string, bool, error) {
	catchService, ok, err := p.currentCatchServiceForHostStorage()
	if err != nil || !ok {
		return "", ok, err
	}
	return cleanHostStoragePath(serviceRootFromConfig(p.config, *catchService)), true, nil
}

func (p *hostStoragePlanner) currentCatchServiceForHostStorage() (*db.Service, bool, error) {
	dv, err := p.hostStorageDataView()
	if err != nil {
		return nil, false, err
	}
	if !dv.Services().Get(CatchService).Valid() {
		return nil, false, nil
	}
	catchService, err := hostStorageCatchSystemdServiceFromData(dv)
	if err != nil {
		return nil, false, err
	}
	return catchService, true, nil
}

func hostStorageCatchRootAction(currentRoot string, desiredRoot string) (catchrpc.HostStorageCatchAction, error) {
	if currentRoot == "" || desiredRoot == "" || hostStoragePathsEqual(currentRoot, desiredRoot) {
		return catchrpc.HostStorageCatchAction{}, nil
	}
	if rootsAreNested(currentRoot, desiredRoot) || rootsAreNested(desiredRoot, currentRoot) {
		return catchrpc.HostStorageCatchAction{}, fmt.Errorf("cannot migrate catch service root between nested paths: %s and %s", currentRoot, desiredRoot)
	}
	return catchrpc.HostStorageCatchAction{
		Move: true,
		From: currentRoot,
		To:   desiredRoot,
	}, nil
}

func validateHostStorageMigrateMode(mode catchrpc.HostStorageMigrateServices) error {
	switch mode {
	case catchrpc.HostStorageMigrateAll, catchrpc.HostStorageMigrateNone:
		return nil
	case catchrpc.HostStorageMigratePrompt:
		return fmt.Errorf("migrate services must be all or none")
	default:
		return fmt.Errorf("unknown migrate services mode %q", mode)
	}
}

func (p *hostStoragePlanner) hostStorageServices() ([]db.Service, error) {
	dv, err := p.hostStorageDataView()
	if err != nil {
		return nil, err
	}
	return hostStorageServicesFromDataView(dv), nil
}

func hostStorageSelfManagedService(name string) bool {
	switch strings.TrimSpace(name) {
	case CatchService, SystemService:
		return true
	default:
		return false
	}
}

func (p *hostStoragePlanner) hostStorageDataView() (db.DataView, error) {
	store := p.store
	if store == nil {
		store = p.config.DB
	}
	if store == nil {
		return db.DataView{}, fmt.Errorf("host storage planner requires a db store")
	}
	dv, err := store.Get()
	if err != nil {
		return db.DataView{}, fmt.Errorf("failed to get data: %v", err)
	}
	if !dv.Valid() {
		return db.DataView{}, fmt.Errorf("db is invalid")
	}
	return dv, nil
}

func hostStorageServiceMovesFromRootBatch(batch serviceRootBatchPlan) []catchrpc.HostStorageServiceMove {
	moves := make([]catchrpc.HostStorageServiceMove, 0, len(batch.Moves))
	for _, move := range batch.Moves {
		moves = append(moves, catchrpc.HostStorageServiceMove{
			Name: move.ServiceName,
			From: move.OldRoot,
			To:   move.NewRoot,
			// Runtime state is intentionally not consulted during planning.
			// Task 7 fills WasRunning when apply has service manager context.
			WasRunning: false,
		})
	}
	return moves
}

func hostStorageRootContains(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	values = slices.Clone(values)
	slices.Sort(values)
	return slices.Compact(values)
}
