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
	"slices"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/copyutil"
	"github.com/yeetrun/yeet/pkg/db"
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

func (s *Server) PlanHostStorage(ctx context.Context, req catchrpc.HostStoragePlanRequest) (catchrpc.HostStoragePlan, error) {
	planner := &hostStoragePlanner{
		config: s.cfg,
		store:  s.cfg.DB,
		zfs:    s.zfsRunner,
	}
	return planner.Plan(ctx, req)
}

func (s *Server) ApplyHostStoragePlan(ctx context.Context, plan catchrpc.HostStoragePlan, yes bool, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	applier := &hostStorageApplier{
		config: s.cfg,
		store:  s.cfg.DB,
		zfs:    s.zfsRunner,
	}
	return applier.Apply(ctx, plan, yes, w)
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

type hostStorageApplier struct {
	config Config
	store  *db.Store
	zfs    zfsCommandRunner
	ops    hostStorageApplyOperations
}

type hostStorageApplyOperations struct {
	preflightCatchRestart                   func(context.Context, catchrpc.HostStoragePlan) error
	isServiceRunning                        func(context.Context, string) (bool, error)
	runnerForService                        func(context.Context, string) (ServiceRunner, error)
	materializeServiceRootMigration         func(context.Context, serviceRootMigrationPlan, io.Writer) error
	applyServiceRootMigrationRuntimeChanges func(context.Context, Config, db.Service, db.Service, io.Writer) error
	copyDataDir                             func(context.Context, string, string, hostStorageDataDirCopyOptions) error
	reinstallServiceUnits                   func(context.Context, Config, *db.Service) ([]string, error)
	regenerateVMUnit                        func(context.Context, Config, *db.Service, string) ([]string, error)
	reloadSystemd                           func(context.Context) error
	enableSystemdUnits                      func(context.Context, []string) error
	reinstallCatchUnit                      func(context.Context, hostStorageInstallRequest, io.Writer) error
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
	plan serviceRootMigrationPlan
	move catchrpc.HostStorageServiceMove
	old  db.Service
}

var (
	hostStorageInstallCatchUnit = installHostStorageCatchUnit
	hostStorageRestartCatch     = restartHostStorageCatch
	hostStorageVerifyCatchInfo  = verifyHostStorageCatchInfo
	hostStorageRunCommand       = runHostStorageCommand
)

var errHostStorageCatchRestartScheduled = errors.New("catch restart scheduled")

func (a *hostStorageApplier) Apply(ctx context.Context, plan catchrpc.HostStoragePlan, yes bool, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	_ = yes
	w = hostStorageApplyWriter(w)
	a.ops = a.completeOperations()
	serviceMoves, err := a.prepareApply(ctx, plan)
	if err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	return a.applyPreparedPlan(ctx, plan, serviceMoves, w)
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
	if err := a.preflightCatchRestart(ctx, plan); err != nil {
		return nil, err
	}
	if err := a.prepareZFS(ctx, plan); err != nil {
		return nil, err
	}
	if err := a.preflightDataDirTarget(plan); err != nil {
		return nil, err
	}
	return serviceMoves, nil
}

func (a *hostStorageApplier) applyPreparedPlan(ctx context.Context, plan catchrpc.HostStoragePlan, serviceMoves []hostStorageServiceApplyMove, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	if err := a.stopAffectedServices(ctx, serviceMoves); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	rootMoves := hostStorageServiceRootApplyMoves(serviceMoves)
	result := catchrpc.HostStorageApplyResult{
		MigratedServices: hostStorageApplyResultMoves(rootMoves),
	}
	if err := a.mutateHostStorageState(ctx, plan, serviceMoves, rootMoves, w); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.finishHostStorageApply(ctx, plan, serviceMoves, w, &result); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	return result, nil
}

func (a *hostStorageApplier) mutateHostStorageState(ctx context.Context, plan catchrpc.HostStoragePlan, serviceMoves []hostStorageServiceApplyMove, rootMoves []hostStorageServiceApplyMove, w io.Writer) error {
	if err := a.moveServiceRoots(ctx, rootMoves, w); err != nil {
		return err
	}
	if err := a.applyServiceDBUpdates(ctx, plan, rootMoves, w); err != nil {
		return err
	}
	if err := a.moveCatchRoot(ctx, plan, w); err != nil {
		return err
	}
	if err := a.moveDataDir(ctx, plan, w); err != nil {
		return hostStorageStoppedServicesRecoveryError(err, serviceMoves)
	}
	if err := a.rewriteTargetDataStore(ctx, plan, w); err != nil {
		if plan.DataDirAction.Move {
			err = hostStorageDataDirRecoveryError(plan, "rewrite target db", err)
		}
		return hostStorageStoppedServicesRecoveryError(err, serviceMoves)
	}
	if err := a.repairGeneratedArtifactsAndUnits(ctx, plan, w); err != nil {
		return hostStorageStoppedServicesRecoveryError(err, serviceMoves)
	}
	return nil
}

func (a *hostStorageApplier) finishHostStorageApply(ctx context.Context, plan catchrpc.HostStoragePlan, serviceMoves []hostStorageServiceApplyMove, w io.Writer, result *catchrpc.HostStorageApplyResult) error {
	if err := a.restartPreviouslyRunningServices(ctx, serviceMoves); err != nil {
		return err
	}
	restarted, restartScheduled, err := a.reinstallRestartAndVerifyCatch(ctx, plan, w)
	if err != nil {
		return err
	}
	result.Restarted = restarted
	result.RestartScheduled = restartScheduled
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
	if ops.restartCatch == nil {
		ops.restartCatch = hostStorageRestartCatch
	}
	if ops.verifyCatchInfo == nil {
		ops.verifyCatchInfo = hostStorageVerifyCatchInfo
	}
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
	if hostStoragePathsEqual(from, to) && servicesRootDataset != "" && plan.ServicesAction.Mode == catchrpc.HostStorageMigrateAll {
		return planServicesRootZFSChildDatasetMoves(ctx, cfg, services, to, servicesRootDataset)
	}
	batch, err := planServicesRootBatch(ctx, cfg, services, from, to, plan.ServicesAction.Mode)
	if err != nil {
		return nil, err
	}
	moves := hostStorageServiceMovesFromRootBatch(batch)
	applyServicesRootDatasetToMoves(moves, servicesRootDataset, plan.ServicesAction.Mode)
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

func (a *hostStorageApplier) stopAffectedServices(ctx context.Context, moves []hostStorageServiceApplyMove) error {
	for i := range moves {
		running, err := a.ops.isServiceRunning(ctx, moves[i].move.Name)
		if err != nil {
			if hostStorageRunningCheckCanTreatAsStopped(moves[i].plan) {
				running = false
			} else {
				return fmt.Errorf("check service %q running state: %w", moves[i].move.Name, err)
			}
		}
		moves[i].move.WasRunning = running
		if !running {
			continue
		}
		runner, err := a.ops.runnerForService(ctx, moves[i].move.Name)
		if err != nil {
			return fmt.Errorf("runner for service %q: %w", moves[i].move.Name, err)
		}
		if err := runner.Stop(); err != nil {
			return fmt.Errorf("stop service %q before host storage apply: %w", moves[i].move.Name, err)
		}
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
		if err := a.ops.applyServiceRootMigrationRuntimeChanges(ctx, desiredConfig, move.old, *updated, w); err != nil {
			return nil, err
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

func (a *hostStorageApplier) moveDataDir(ctx context.Context, plan catchrpc.HostStoragePlan, w io.Writer) error {
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
	if err := a.ops.copyDataDir(ctx, from, to, opts); err != nil {
		return hostStorageDataDirRecoveryError(plan, "copy data dir", err)
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
	legacyDataDirs := hostStorageLegacyDefaultDataDirs(cfg)
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

func (a *hostStorageApplier) restartPreviouslyRunningServices(ctx context.Context, moves []hostStorageServiceApplyMove) error {
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
	out := make([]catchrpc.HostStorageServiceMove, 0, len(moves))
	for _, move := range moves {
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
	return fmt.Errorf("move service root for %q from %q to %q failed: %v. Services were left stopped; repair the failed root and retry host storage apply or start services manually", plan.ServiceName, plan.OldRoot, plan.NewRoot, err)
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

func hostStorageStoppedServicesRecoveryError(err error, moves []hostStorageServiceApplyMove) error {
	stopped := make([]string, 0, len(moves))
	for _, move := range moves {
		if move.move.WasRunning {
			stopped = append(stopped, move.move.Name)
		}
	}
	if len(stopped) == 0 {
		return err
	}
	return fmt.Errorf("%w. Services were left stopped: %s", err, strings.Join(stopped, ", "))
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
	case "backups", "catch.lock", "db.json", "id_ed25519", "install.json", "mounts", "registry", "services", "tsd", "tsnet", "vm-images":
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
		legacyDataDirs := hostStorageLegacyDefaultDataDirs(p.config)
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
