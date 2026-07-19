// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

const (
	serviceIdentityPhaseJournal               = "journal-captured"
	serviceIdentityPhaseStop                  = "service-stopped"
	serviceIdentityPhaseSourceSnapshot        = "source-snapshot-created"
	serviceIdentityPhaseMaterializeIntent     = "root-materialization-started"
	serviceIdentityPhaseMaterializeCreated    = "root-materialization-created"
	serviceIdentityPhaseMaterializePublish    = "root-materialization-publish-ready"
	serviceIdentityPhaseSnapshot              = "snapshot-created"
	serviceIdentityPhaseMaterialize           = "root-materialized"
	serviceIdentityPhaseGenerationBackup      = "generation-backup-started"
	serviceIdentityPhaseGenerationBackedUp    = "generation-backed-up"
	serviceIdentityPhaseGenerationStageIntent = "generation-staging-started"
	serviceIdentityPhaseGenerationStage       = "generation-staged"
	serviceIdentityPhaseMaterializeFinal      = "root-materialization-finalized"
	serviceIdentityPhaseRuntimePlan           = "legacy-runtime-planned"
	serviceIdentityPhaseInventorySeal         = "inventory-sealed"
	serviceIdentityPhaseOwnership             = "ownership-applied"
	serviceIdentityPhaseRuntimeBackup         = "legacy-runtime-backed-up"
	serviceIdentityPhaseRuntimeBackedUp       = "legacy-runtime-backup-complete"
	serviceIdentityPhaseUnitWriteIntent       = "unit-write-started"
	serviceIdentityPhaseUnitWrite             = "unit-written"
	serviceIdentityPhaseDaemonReload          = "daemon-reloaded"
	serviceIdentityPhaseGenerationActivate    = "generation-activation-started"
	serviceIdentityPhaseGeneration            = "generation-installed"
	serviceIdentityPhaseGenerationEnabled     = "generation-enabled"
	serviceIdentityPhaseStart                 = "service-started"
	serviceIdentityPhaseVerify                = "target-verified"
	serviceIdentityPhaseDBCommit              = "database-committed"
	serviceIdentityPhaseComplete              = "complete"
)

const serviceIdentityJournalDiagnosticLimit = 8 * 1024

var (
	serviceIdentityEnvironmentAssignment = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]{1,63})=("[^"]*"|'[^']*'|[^\s]+)`)
	serviceIdentityStartJournal          = readServiceIdentityStartJournal
)

type serviceIdentityMigrationRequest struct {
	Service           string
	Requested         string
	Target            resolvedServiceIdentity
	RootPlan          *serviceRootMigrationPlan
	ReplacementUnit   string
	TargetService     *db.Service
	StageGeneration   func(context.Context) error
	InstallGeneration func(context.Context) error
	GenerationPaths   []string
	GenerationIntents []serviceIdentityPathState
	GenerationUnits   []string
	StartNew          bool
	PredecessorAbsent bool
	ForceReconcile    bool

	ops *serviceIdentityMigrationOps
}

type serviceIdentityMigrationResult struct {
	Previous    resolvedServiceIdentity
	Current     resolvedServiceIdentity
	Root        string
	ZFSSnapshot string
	WasRunning  bool
	Restarted   bool
}

type serviceIdentityMigrationOps struct {
	phase                func(string) error
	unitPath             func(string) string
	isRunning            func(context.Context, string) (bool, error)
	isPreviousRunning    func(context.Context, string) (bool, error)
	isTargetRunning      func(context.Context, string) (bool, error)
	isReplacementRunning func(context.Context, string) (bool, error)
	captureRuntime       func(context.Context, string) ([]serviceIdentityRuntimeUnitState, error)
	restoreRuntime       func(context.Context, string, []serviceIdentityRuntimeUnitState) error
	stop                 func(context.Context, string) error
	start                func(context.Context, string) error
	stopReplacement      func(context.Context, string) error
	startPrevious        func(context.Context, string) error
	stopPrevious         func(context.Context, string) error
	snapshot             func(context.Context, *db.Service) (string, error)
	materialize          func(context.Context, serviceRootMigrationPlan, io.Writer) (bool, error)
	discardRoot          func(context.Context, serviceRootMigrationPlan, bool) error
	inspect              func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error)
	inspectSource        func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error)
	apply                func(serviceIdentityInspection, *serviceIdentityJournal) error
	restore              func(string) error
	reload               func(context.Context) error
	writeUnit            func(string, []byte, os.FileMode, serviceIdentityPathProof, uint32, uint32) error
	verify               func(context.Context, serviceIdentityMigrationVerification) error
	commit               func(*db.Service, *db.Service) error
	remove               func(string) error
	isEnabled            func(context.Context, string) (bool, error)
	enable               func(context.Context, string) error
	disable              func(context.Context, string) error
	newGenerationStager  func(*db.Service, string) (serviceIdentityGenerationStager, error)
}

type serviceIdentityGenerationStager interface {
	InstallTargetPaths() []string
	InstallUnits() []string
	InstallTargetStatesExcluding(...string) ([]svc.InstallTargetState, error)
	StageInstallForReload() ([]string, error)
	StageInstallForReloadExcluding(...string) ([]string, error)
}

func serviceIdentityInstallTargetStates(states []svc.InstallTargetState) []serviceIdentityPathState {
	out := make([]serviceIdentityPathState, len(states))
	for index, state := range states {
		out[index] = serviceIdentityPathState{
			Path: state.Path, Present: state.Present, Mode: state.Mode, UID: state.UID, GID: state.GID,
			Nlink: state.Nlink, Size: state.Size, SHA256: state.SHA256,
		}
	}
	return out
}

type serviceIdentityMigrationVerification struct {
	Service       string
	UnitPath      string
	Identity      db.ServiceIdentity
	Root          string
	WasRunning    bool
	ExpectProcess bool
}

type serviceIdentityMigration struct {
	server             *Server
	req                serviceIdentityMigrationRequest
	ops                serviceIdentityMigrationOps
	writer             io.Writer
	previous           *db.Service
	predecessor        *db.Service
	predecessorPresent bool
	target             *db.Service
	result             serviceIdentityMigrationResult

	journal                     *serviceIdentityJournal
	journalPath                 string
	migrationID                 string
	unitPath                    string
	previousUnit                []byte
	previousRuntime             []serviceIdentityRuntimeUnitState
	unitPresent                 bool
	previousUnitMode            os.FileMode
	previousUnitProof           serviceIdentityPathProof
	unitBeforeWrite             serviceIdentityPathProof
	writtenUnit                 serviceIdentityPathProof
	primaryUnitIntent           serviceIdentityPathState
	unitWriteStarted            bool
	stopped                     bool
	sealed                      bool
	ownership                   bool
	rootMaterialized            bool
	datasetCreated              bool
	dbCommitted                 bool
	completed                   bool
	replacementStarted          bool
	runtimeBackups              []serviceIdentityGenerationBackup
	runtimeBackupStarted        bool
	runtimeBackupCompleted      bool
	initialMaterialization      serviceIdentityPhaseRecord
	materializationCreation     serviceIdentityPhaseRecord
	materializationPublish      serviceIdentityPhaseRecord
	materialization             serviceIdentityPhaseRecord
	generationBackups           []serviceIdentityGenerationBackup
	generationPaths             []serviceIdentityPathProof
	generationIntents           []serviceIdentityPathState
	generationBackupStarted     bool
	generationBackupCompleted   bool
	generationStageStarted      bool
	generationStaged            bool
	generationActivationStarted bool
	generationUnits             []serviceIdentityUnitEnablement
	deferredReplacementUnit     bool
	prepareGeneration           func(string) error
	finalizeGeneration          func() error
}

func (s *Server) migrateServiceIdentity(ctx context.Context, req serviceIdentityMigrationRequest, w io.Writer) (serviceIdentityMigrationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if w == nil {
		w = io.Discard
	}
	if err := s.checkServiceIdentityMutationAllowed(req.Service); err != nil {
		return serviceIdentityMigrationResult{}, err
	}
	release := s.serviceOperationLocks.Lock(req.Service)
	defer release()
	return s.migrateServiceIdentityLocked(ctx, req, w)
}

// migrateServiceIdentityLocked runs an identity transaction while the caller
// owns the keyed service-operation lock. It exists for composed operations,
// such as FileInstaller.Close, which must keep one lock across staging and the
// identity commit.
func (s *Server) migrateServiceIdentityLocked(ctx context.Context, req serviceIdentityMigrationRequest, w io.Writer) (serviceIdentityMigrationResult, error) {
	if err := s.checkServiceIdentityMutationAllowed(req.Service); err != nil {
		return serviceIdentityMigrationResult{}, err
	}

	m := &serviceIdentityMigration{server: s, req: req, writer: w}
	m.ops = m.defaultOps()
	if req.ops != nil {
		m.ops.merge(*req.ops)
	}
	if err := m.prepare(ctx); err != nil {
		if req.PredecessorAbsent {
			err = errors.Join(err, m.removeProvisionalService())
		}
		return serviceIdentityMigrationResult{}, err
	}
	if m.isNoop() {
		return m.result, nil
	}
	if err := m.run(ctx); err != nil {
		if m.completed {
			committedErr := fmt.Errorf(
				"service identity migration for %q committed as %s; post-commit cleanup failed and will be retried from %s: %w",
				m.req.Service, formatServiceIdentity(m.req.Target.Persisted), m.journalPath, err,
			)
			s.setServiceIdentityMutationBlock(m.req.Service, committedErr)
			return m.result, committedErr
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rollbackErr := m.rollback(rollbackCtx)
		migrationErr := m.migrationError(err, rollbackErr)
		if rollbackErr != nil && m.journalPath != "" {
			s.setServiceIdentityMutationBlock(m.req.Service, migrationErr)
		}
		return serviceIdentityMigrationResult{}, migrationErr
	}
	return m.result, nil
}

func (m *serviceIdentityMigration) removeProvisionalService() error {
	if m.previous == nil {
		return nil
	}
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		current, ok := data.Services[m.req.Service]
		if !ok {
			return nil
		}
		if !reflect.DeepEqual(current, m.previous) {
			return fmt.Errorf("provisional service %q changed during failed migration preflight", m.req.Service)
		}
		delete(data.Services, m.req.Service)
		return nil
	})
	if err != nil {
		return fmt.Errorf("remove provisional service after failed identity preflight: %w", err)
	}
	return nil
}

func (m *serviceIdentityMigration) prepare(ctx context.Context) error {
	sv, err := m.prepareObservedService()
	if err != nil {
		return err
	}
	if err := m.prepareTargetService(ctx); err != nil {
		return err
	}
	if err := m.prepareServiceIdentityResult(sv); err != nil {
		return err
	}
	return m.capturePreviousServiceIdentityState(ctx)
}

func (m *serviceIdentityMigration) prepareObservedService() (db.ServiceView, error) {
	if strings.TrimSpace(m.req.Service) == "" {
		return db.ServiceView{}, fmt.Errorf("service identity migration requires a service")
	}
	sv, err := m.server.serviceView(m.req.Service)
	if err != nil {
		return db.ServiceView{}, err
	}
	m.previous = sv.AsStruct()
	m.predecessorPresent = !m.req.PredecessorAbsent
	if m.predecessorPresent {
		m.predecessor = m.previous.Clone()
	} else if !m.req.StartNew {
		return db.ServiceView{}, fmt.Errorf("absent predecessor semantics require a new native service install")
	}
	if m.previous.ServiceType != db.ServiceTypeSystemd {
		return db.ServiceView{}, serviceIdentityTypeError(m.req.Service, m.previous.ServiceType)
	}
	if m.req.Service == CatchService {
		return db.ServiceView{}, fmt.Errorf("service %q is privileged host infrastructure and cannot use --run-as", m.req.Service)
	}
	if m.previous.Identity != nil {
		if err := validateServiceIdentityDrift(*m.previous.Identity); err != nil {
			return db.ServiceView{}, fmt.Errorf("validate current persisted identity: %w", err)
		}
	}
	if err := validateServiceIdentityDrift(m.req.Target.Persisted); err != nil {
		return db.ServiceView{}, err
	}
	return sv, nil
}

func (m *serviceIdentityMigration) prepareTargetService(ctx context.Context) error {
	var err error
	m.target = m.previous.Clone()
	if m.req.TargetService != nil {
		m.target = m.req.TargetService.Clone()
	}
	if m.req.RootPlan != nil {
		if err := m.validateRootPlan(); err != nil {
			return err
		}
		if m.req.RootPlan.Mode == serviceRootMigrationCopy {
			if _, err := m.ops.inspectSource(ctx, serviceIdentityInspectionRequest{
				Root: m.req.RootPlan.OldRoot, Dataset: m.req.RootPlan.OldRootZFS,
				Target: m.req.Target.Persisted, ZFSRunner: m.server.zfsRunner,
			}); err != nil {
				return fmt.Errorf("inspect source root before copy: %w", err)
			}
		}
		m.target, err = plannedServiceForRootMigration(m.server.cfg, *m.req.RootPlan, m.target)
		if err != nil {
			return err
		}
	}
	targetIdentity := m.req.Target.Persisted
	m.target.Identity = &targetIdentity
	m.target.Name = m.req.Service
	m.target.ServiceType = db.ServiceTypeSystemd
	if err := validateNativeServicePrivilegedPorts(m.req.Service, m.target.Publish, m.req.Target.Persisted); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) prepareServiceIdentityResult(sv db.ServiceView) error {
	previousIdentity := effectiveServiceIdentity(sv)
	m.result = serviceIdentityMigrationResult{
		Previous: previousIdentity,
		Current:  m.req.Target,
		Root:     serviceRootFromConfig(m.server.cfg, *m.target),
	}
	if err := validateServiceIdentityRootTraversal(m.req.Service, m.result.Root, m.req.Target.Persisted); err != nil {
		return err
	}
	m.unitPath = m.ops.unitPath(m.req.Service)
	if err := m.configureCopiedRootGeneration(); err != nil {
		return err
	}
	if err := m.validateGenerationTargets(); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) capturePreviousServiceIdentityState(ctx context.Context) error {
	if err := m.capturePreviousServiceIdentityUnit(); err != nil {
		return err
	}
	if err := m.prepareReplacementServiceIdentityUnit(); err != nil {
		return err
	}
	return m.capturePreviousServiceIdentityRuntime(ctx)
}

func (m *serviceIdentityMigration) capturePreviousServiceIdentityUnit() error {
	previousUnit, unitPresent, unitMode, err := readOptionalServiceIdentityUnit(m.unitPath)
	if err != nil {
		return err
	}
	previousUnitProof, err := captureServiceIdentityPathProof(m.unitPath)
	if err != nil {
		return fmt.Errorf("capture previous primary unit provenance: %w", err)
	}
	if err := validateCapturedPreviousServiceIdentityUnit(m.unitPath, previousUnit, unitPresent, unitMode, previousUnitProof); err != nil {
		return err
	}
	m.previousUnit = previousUnit
	m.unitPresent = unitPresent
	m.previousUnitMode = unitMode
	m.previousUnitProof = previousUnitProof
	return nil
}

func validateCapturedPreviousServiceIdentityUnit(path string, raw []byte, present bool, mode os.FileMode, proof serviceIdentityPathProof) error {
	if proof.Present && proof.Nlink != 1 {
		return fmt.Errorf("primary unit %s has %d hard links; exact identity rollback requires one", path, proof.Nlink)
	}
	if err := validateServiceIdentityTransactionPath(proof); err != nil {
		return fmt.Errorf("primary unit cannot be restored exactly: %w", err)
	}
	digest := sha256.Sum256(raw)
	if proof.Present != present || present &&
		(proof.Mode.Perm() != mode.Perm() || proof.Size != int64(len(raw)) || proof.SHA256 != hex.EncodeToString(digest[:])) {
		return fmt.Errorf("primary unit changed while its previous state was captured")
	}
	return nil
}

func (m *serviceIdentityMigration) prepareReplacementServiceIdentityUnit() error {
	if m.req.ReplacementUnit == "" && !m.deferredReplacementUnit {
		replacementUnit, err := m.replacementUnit()
		if err != nil {
			return err
		}
		m.req.ReplacementUnit = replacementUnit
	}
	return nil
}

func (m *serviceIdentityMigration) capturePreviousServiceIdentityRuntime(ctx context.Context) error {
	previousRuntime, err := m.ops.captureRuntime(ctx, m.req.Service)
	if err != nil {
		return err
	}
	m.previousRuntime = previousRuntime
	m.result.WasRunning = serviceIdentityPrimaryRuntimeActive(m.previous, m.req.Service, m.previousRuntime)
	return nil
}

func (m *serviceIdentityMigration) validateRootPlan() error {
	plan := m.req.RootPlan
	if plan.ServiceName != m.req.Service {
		return fmt.Errorf("service identity root plan is for %q, want %q", plan.ServiceName, m.req.Service)
	}
	oldRoot := serviceRootFromConfig(m.server.cfg, *m.previous)
	if filepath.Clean(plan.OldRoot) != filepath.Clean(oldRoot) || plan.OldRootZFS != m.previous.ServiceRootZFS {
		return fmt.Errorf("service root for %q changed during migration planning", m.req.Service)
	}
	if plan.Mode != serviceRootMigrationCopy && plan.Mode != serviceRootMigrationEmpty {
		return fmt.Errorf("service root migration mode was not selected")
	}
	if plan.NewRootExisted {
		return fmt.Errorf("identity migration target root %s already exists; choose a new empty path or dataset so rollback cannot alter operator-owned content", plan.NewRoot)
	}
	return nil
}

func (m *serviceIdentityMigration) isNoop() bool {
	return !m.req.StartNew && !m.req.ForceReconcile && m.req.RootPlan == nil && m.req.StageGeneration == nil && m.req.InstallGeneration == nil && reflect.DeepEqual(m.previous.Identity, m.target.Identity)
}

func (m *serviceIdentityMigration) run(ctx context.Context) error {
	if err := m.captureAndStop(ctx); err != nil {
		return err
	}
	snapshotted, err := m.materializeServiceIdentityRoot(ctx)
	if err != nil {
		return err
	}
	if err := m.backupServiceIdentityRuntime(); err != nil {
		return err
	}
	if err := m.stageServiceIdentityGeneration(ctx); err != nil {
		return err
	}
	if err := m.finalizeMaterializationAndSnapshot(ctx, snapshotted); err != nil {
		return err
	}
	if err := m.sealAndApplyServiceIdentity(ctx); err != nil {
		return err
	}
	if err := m.writeServiceIdentityUnit(ctx); err != nil {
		return err
	}
	if err := m.installServiceIdentityGeneration(ctx); err != nil {
		return err
	}
	if err := m.startVerifyAndCommitServiceIdentity(ctx); err != nil {
		return err
	}
	return m.completeServiceIdentityMigration(ctx)
}

func (m *serviceIdentityMigration) captureAndStop(ctx context.Context) error {
	if err := m.phase(serviceIdentityPhaseJournal); err != nil {
		return err
	}
	if err := m.captureJournal(ctx); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseJournal, err)
	}
	if serviceIdentityAnyRuntimeActive(m.previousRuntime) || m.req.ops == nil {
		if err := m.phase(serviceIdentityPhaseStop); err != nil {
			return err
		}
		if err := m.ops.stop(ctx, m.req.Service); err != nil {
			return fmt.Errorf("%s: %w", serviceIdentityPhaseStop, err)
		}
		m.stopped = true
		if err := m.appendPhase(serviceIdentityPhaseStop, serviceIdentityPhaseRecord{}); err != nil {
			return err
		}
	}
	return nil
}

func (m *serviceIdentityMigration) materializeServiceIdentityRoot(ctx context.Context) (bool, error) {
	snapshotted, err := m.snapshotServiceIdentitySourceRoot(ctx)
	if err != nil {
		return false, err
	}
	if m.req.RootPlan == nil {
		return snapshotted, nil
	}
	if err := m.materializePlannedServiceIdentityRoot(ctx); err != nil {
		return false, err
	}
	return snapshotted, nil
}

func (m *serviceIdentityMigration) snapshotServiceIdentitySourceRoot(ctx context.Context) (bool, error) {
	if m.req.RootPlan == nil || m.previous.ServiceRootZFS == "" {
		return false, nil
	}
	if err := m.takeSnapshot(ctx, serviceIdentityPhaseSourceSnapshot, m.previous); err != nil {
		return false, err
	}
	return true, nil
}

func (m *serviceIdentityMigration) materializePlannedServiceIdentityRoot(ctx context.Context) error {
	if err := m.phase(serviceIdentityPhaseMaterializeIntent); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseMaterializeIntent, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	m.rootMaterialized = true
	m.datasetCreated = m.req.RootPlan.CreateNewRootZFS
	created, err := m.ops.materialize(ctx, *m.req.RootPlan, m.writer)
	m.datasetCreated = created
	if err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseMaterialize, err)
	}
	if err := m.prepareMaterializedServiceIdentityRoot(); err != nil {
		return err
	}
	if err := syncServiceIdentityTree(m.req.RootPlan.NewRoot); err != nil {
		return fmt.Errorf("%s: make target tree durable: %w", serviceIdentityPhaseMaterialize, err)
	}
	m.materialization, err = m.captureMaterialization(ctx, created)
	if err != nil {
		return fmt.Errorf("%s: capture target provenance: %w", serviceIdentityPhaseMaterialize, err)
	}
	if err := m.phase(serviceIdentityPhaseMaterialize); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseMaterialize, m.materialization); err != nil {
		return err
	}
	m.initialMaterialization = m.materialization
	return nil
}

func (m *serviceIdentityMigration) prepareMaterializedServiceIdentityRoot() error {
	if m.req.ops != nil && m.req.RootPlan.Mode == serviceRootMigrationCopy {
		if err := rewriteCopiedServiceRootArtifacts(m.target.Artifacts, m.req.RootPlan.OldRoot, m.req.RootPlan.NewRoot); err != nil {
			return fmt.Errorf("%s: rewrite materialized artifacts: %w", serviceIdentityPhaseMaterialize, err)
		}
	}
	if m.req.ops != nil && m.prepareGeneration != nil {
		if err := m.prepareGeneration(m.req.RootPlan.NewRoot); err != nil {
			return fmt.Errorf("%s: prepare copied target generation: %w", serviceIdentityPhaseMaterialize, err)
		}
	}
	if m.finalizeGeneration != nil {
		if err := m.finalizeGeneration(); err != nil {
			return fmt.Errorf("%s: finalize copied target generation: %w", serviceIdentityPhaseMaterialize, err)
		}
	}
	return nil
}

func (m *serviceIdentityMigration) backupServiceIdentityRuntime() error {
	if err := m.planServiceIdentityRuntimeBackup(); err != nil {
		return err
	}
	if len(m.runtimeBackups) == 0 {
		return nil
	}
	return m.backupPlannedServiceIdentityRuntime()
}

func (m *serviceIdentityMigration) planServiceIdentityRuntimeBackup() error {
	if err := m.phase(serviceIdentityPhaseRuntimePlan); err != nil {
		return err
	}
	runtimeBackups, err := captureLegacyNativeRuntimeBackups(m.server.cfg.RootDir, m.result.Root, m.req.Service, m.migrationID)
	if err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseRuntimePlan, err)
	}
	m.runtimeBackups = runtimeBackups
	if err := m.appendPhase(serviceIdentityPhaseRuntimePlan, serviceIdentityPhaseRecord{
		RuntimeBackups: append([]serviceIdentityGenerationBackup(nil), runtimeBackups...),
	}); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) backupPlannedServiceIdentityRuntime() error {
	if err := m.phase(serviceIdentityPhaseRuntimeBackup); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseRuntimeBackup, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	m.runtimeBackupStarted = true
	backups, err := backupLegacyNativeRuntimeArtifacts(m.server.cfg.RootDir, m.result.Root, m.runtimeBackups)
	if err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseRuntimeBackup, err)
	}
	m.runtimeBackups = backups
	if err := m.phase(serviceIdentityPhaseRuntimeBackedUp); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseRuntimeBackedUp, serviceIdentityPhaseRecord{
		RuntimeBackups: append([]serviceIdentityGenerationBackup(nil), backups...),
	}); err != nil {
		return err
	}
	m.runtimeBackupCompleted = true
	if err := removeLegacyNativeRuntimeArtifacts(m.result.Root, m.runtimeBackups); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseRuntimeBackup, err)
	}
	return nil
}

func (m *serviceIdentityMigration) stageServiceIdentityGeneration(ctx context.Context) error {
	if m.req.StageGeneration == nil {
		return nil
	}
	if err := m.phase(serviceIdentityPhaseGenerationStage); err != nil {
		return err
	}
	if err := m.backupGenerationPaths(); err != nil {
		return fmt.Errorf("%s: backup installed generation paths: %w", serviceIdentityPhaseGenerationStage, err)
	}
	m.generationIntents = append([]serviceIdentityPathState(nil), m.req.GenerationIntents...)
	if err := m.validateServiceIdentityGenerationIntents(); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseGenerationStageIntent, serviceIdentityPhaseRecord{
		GenerationIntents: append([]serviceIdentityPathState(nil), m.generationIntents...),
	}); err != nil {
		return err
	}
	m.generationStageStarted = true
	if err := m.req.StageGeneration(ctx); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseGenerationStage, err)
	}
	if err := m.verifyDatabaseStillPrevious(); err != nil {
		return fmt.Errorf("%s: generation callback must be stage-only: %w", serviceIdentityPhaseGenerationStage, err)
	}
	if err := m.captureStagedServiceIdentityGeneration(); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseGenerationStage, serviceIdentityPhaseRecord{
		GenerationPaths: append([]serviceIdentityPathProof(nil), m.generationPaths...),
	}); err != nil {
		return err
	}
	m.generationStaged = true
	return nil
}

func (m *serviceIdentityMigration) validateServiceIdentityGenerationIntents() error {
	if m.req.ops != nil {
		return nil
	}
	expected := serviceIdentityGenerationIntentPaths(m.req.GenerationPaths, m.unitPath)
	actual, err := validateServiceIdentityGenerationIntentStates(m.generationIntents)
	if err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseGenerationStage, err)
	}
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("%s: generation intent paths do not match the install plan", serviceIdentityPhaseGenerationStage)
	}
	return nil
}

func serviceIdentityGenerationIntentPaths(paths []string, unitPath string) []string {
	expected := make([]string, 0, len(paths))
	for _, path := range paths {
		if filepath.Clean(path) != filepath.Clean(unitPath) {
			expected = append(expected, filepath.Clean(path))
		}
	}
	return expected
}

func validateServiceIdentityGenerationIntentStates(states []serviceIdentityPathState) ([]string, error) {
	actual := make([]string, len(states))
	for index, state := range states {
		if err := validateServiceIdentityPathState(state, state.Path); err != nil {
			return nil, err
		}
		actual[index] = filepath.Clean(state.Path)
	}
	return actual, nil
}

func (m *serviceIdentityMigration) captureStagedServiceIdentityGeneration() error {
	generationPaths, err := captureServiceIdentityGenerationPaths(m.req.GenerationPaths)
	if err != nil {
		return fmt.Errorf("%s: capture staged generation provenance: %w", serviceIdentityPhaseGenerationStage, err)
	}
	m.generationPaths = generationPaths
	if primary, ok := serviceIdentityProofForPath(m.generationPaths, m.unitPath); ok && !reflect.DeepEqual(primary, m.previousUnitProof) {
		return fmt.Errorf("%s: primary unit changed outside the dedicated unit-write phase", serviceIdentityPhaseGenerationStage)
	}
	for _, state := range m.generationIntents {
		proof, ok := serviceIdentityProofForPath(m.generationPaths, state.Path)
		if !ok || !serviceIdentityPathMatchesState(proof, state) {
			return fmt.Errorf("%s: staged path %s does not match its durable intent", serviceIdentityPhaseGenerationStage, state.Path)
		}
	}
	return nil
}

func (m *serviceIdentityMigration) finalizeMaterializationAndSnapshot(ctx context.Context, snapshotted bool) error {
	if m.req.RootPlan != nil {
		if err := m.finalizeServiceIdentityMaterialization(ctx); err != nil {
			return err
		}
	}
	if m.req.RootPlan != nil && m.target.ServiceRootZFS != "" && m.target.ServiceRootZFS != m.previous.ServiceRootZFS {
		if err := m.takeSnapshot(ctx, serviceIdentityPhaseSnapshot, m.target); err != nil {
			return err
		}
		snapshotted = true
	}
	if !snapshotted {
		return m.takeSnapshot(ctx, serviceIdentityPhaseSnapshot, m.target)
	}
	return nil
}

func (m *serviceIdentityMigration) finalizeServiceIdentityMaterialization(ctx context.Context) error {
	if err := syncServiceIdentityTree(m.req.RootPlan.NewRoot); err != nil {
		return fmt.Errorf("%s: make final target tree durable: %w", serviceIdentityPhaseMaterializeFinal, err)
	}
	finalMaterialization, err := m.captureMaterialization(ctx, m.datasetCreated)
	if err != nil {
		return fmt.Errorf("%s: capture final target provenance: %w", serviceIdentityPhaseMaterializeFinal, err)
	}
	if err := m.phase(serviceIdentityPhaseMaterializeFinal); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseMaterializeFinal, finalMaterialization); err != nil {
		return err
	}
	m.materialization = finalMaterialization
	return nil
}

func (m *serviceIdentityMigration) sealAndApplyServiceIdentity(ctx context.Context) error {
	inspection, err := m.sealServiceIdentityInventory(ctx)
	if err != nil {
		return err
	}
	return m.applyAndVerifyServiceIdentity(ctx, inspection)
}

func (m *serviceIdentityMigration) sealServiceIdentityInventory(ctx context.Context) (serviceIdentityInspection, error) {
	if err := m.phase(serviceIdentityPhaseInventorySeal); err != nil {
		return serviceIdentityInspection{}, err
	}
	inspection, err := m.ops.inspect(ctx, serviceIdentityInspectionRequest{
		Root: m.result.Root, Dataset: m.target.ServiceRootZFS, Target: m.req.Target.Persisted,
		ZFSRunner: m.server.zfsRunner,
	})
	if err != nil {
		return serviceIdentityInspection{}, fmt.Errorf("%s: %w", serviceIdentityPhaseInventorySeal, err)
	}
	for _, record := range inspection.Records {
		if err := m.journal.AppendInode(record); err != nil {
			return serviceIdentityInspection{}, fmt.Errorf("%s: %w", serviceIdentityPhaseInventorySeal, err)
		}
	}
	if err := m.journal.Seal(m.result.ZFSSnapshot); err != nil {
		return serviceIdentityInspection{}, fmt.Errorf("%s: %w", serviceIdentityPhaseInventorySeal, err)
	}
	m.sealed = true
	return inspection, nil
}

func (m *serviceIdentityMigration) applyAndVerifyServiceIdentity(ctx context.Context, inspection serviceIdentityInspection) error {
	if err := m.phase(serviceIdentityPhaseOwnership); err != nil {
		return err
	}
	m.ownership = true
	if err := m.ops.apply(inspection, m.journal); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseOwnership, err)
	}
	postApply, err := m.ops.inspect(ctx, serviceIdentityInspectionRequest{
		Root: m.result.Root, Dataset: m.target.ServiceRootZFS, Target: m.req.Target.Persisted,
		ZFSRunner: m.server.zfsRunner,
	})
	if err != nil {
		return fmt.Errorf("%s: re-inspect target after ownership mutation: %w", serviceIdentityPhaseOwnership, err)
	}
	if len(postApply.Mutations) != 0 {
		return fmt.Errorf("%s: %d unsealed ownership mutations appeared after inventory; service data changed during migration", serviceIdentityPhaseOwnership, len(postApply.Mutations))
	}
	if err := m.appendPhase(serviceIdentityPhaseOwnership, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) writeServiceIdentityUnit(ctx context.Context) error {
	if err := m.phase(serviceIdentityPhaseUnitWrite); err != nil {
		return err
	}
	if strings.TrimSpace(m.req.ReplacementUnit) == "" {
		return fmt.Errorf("%s: replacement unit was not produced by generation staging", serviceIdentityPhaseUnitWrite)
	}
	if err := m.captureAndWriteServiceIdentityUnit(); err != nil {
		return err
	}
	return m.reloadServiceIdentityUnits(ctx)
}

func (m *serviceIdentityMigration) captureAndWriteServiceIdentityUnit() error {
	unitBeforeWrite, err := captureServiceIdentityPathProof(m.unitPath)
	if err != nil {
		return fmt.Errorf("%s: capture pre-write unit provenance: %w", serviceIdentityPhaseUnitWrite, err)
	}
	m.unitBeforeWrite = unitBeforeWrite
	m.primaryUnitIntent = serviceIdentityDesiredFileState(
		m.unitPath, []byte(m.req.ReplacementUnit), 0o644, uint32(os.Geteuid()), uint32(os.Getegid()),
	)
	if err := m.appendPhase(serviceIdentityPhaseUnitWriteIntent, serviceIdentityPhaseRecord{
		PrimaryUnit: m.unitBeforeWrite, PrimaryUnitIntent: m.primaryUnitIntent,
	}); err != nil {
		return err
	}
	m.unitWriteStarted = true
	if err := m.ops.writeUnit(
		m.unitPath, []byte(m.req.ReplacementUnit), 0o644, m.unitBeforeWrite,
		uint32(os.Geteuid()), uint32(os.Getegid()),
	); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseUnitWrite, err)
	}
	writtenUnit, err := captureServiceIdentityPathProof(m.unitPath)
	if err != nil {
		return fmt.Errorf("%s: capture replacement unit provenance: %w", serviceIdentityPhaseUnitWrite, err)
	}
	m.writtenUnit = writtenUnit
	if !m.writtenUnit.Present {
		return fmt.Errorf("%s: replacement unit is absent after atomic write", serviceIdentityPhaseUnitWrite)
	}
	if !serviceIdentityPathMatchesState(m.writtenUnit, m.primaryUnitIntent) {
		return fmt.Errorf("%s: replacement unit does not match its durable intent", serviceIdentityPhaseUnitWrite)
	}
	if err := m.appendPhase(serviceIdentityPhaseUnitWrite, serviceIdentityPhaseRecord{PrimaryUnit: m.writtenUnit}); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) reloadServiceIdentityUnits(ctx context.Context) error {
	if err := m.phase(serviceIdentityPhaseDaemonReload); err != nil {
		return err
	}
	if err := m.ops.reload(ctx); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseDaemonReload, err)
	}
	if err := m.appendPhase(serviceIdentityPhaseDaemonReload, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) installServiceIdentityGeneration(ctx context.Context) error {
	if m.req.InstallGeneration != nil {
		if err := m.phase(serviceIdentityPhaseGeneration); err != nil {
			return err
		}
		if err := m.req.InstallGeneration(ctx); err != nil {
			return fmt.Errorf("%s: %w", serviceIdentityPhaseGeneration, err)
		}
		if err := m.verifyDatabaseStillPrevious(); err != nil {
			return fmt.Errorf("%s: generation callback must be stage-only: %w", serviceIdentityPhaseGeneration, err)
		}
		if err := m.appendPhase(serviceIdentityPhaseGeneration, serviceIdentityPhaseRecord{}); err != nil {
			return err
		}
	}
	if len(m.req.GenerationUnits) != 0 {
		return m.activateGenerationUnits(ctx)
	}
	return nil
}

func (m *serviceIdentityMigration) startVerifyAndCommitServiceIdentity(ctx context.Context) error {
	if err := m.startReplacementServiceIdentity(ctx); err != nil {
		return err
	}
	if err := m.verifyReplacementServiceIdentity(ctx); err != nil {
		return err
	}
	return m.commitServiceIdentityDatabase()
}

func (m *serviceIdentityMigration) startReplacementServiceIdentity(ctx context.Context) error {
	if m.result.WasRunning || m.req.StartNew {
		if err := m.phase(serviceIdentityPhaseStart); err != nil {
			return err
		}
		m.replacementStarted = true
		if err := m.ops.start(ctx, m.req.Service); err != nil {
			return fmt.Errorf("%s: %w", serviceIdentityPhaseStart, err)
		}
		m.result.Restarted = m.result.WasRunning
		if err := m.appendPhase(serviceIdentityPhaseStart, serviceIdentityPhaseRecord{}); err != nil {
			return err
		}
	}
	return nil
}

func (m *serviceIdentityMigration) verifyReplacementServiceIdentity(ctx context.Context) error {
	if err := m.phase(serviceIdentityPhaseVerify); err != nil {
		return err
	}
	if err := m.ops.verify(ctx, serviceIdentityMigrationVerification{
		Service: m.req.Service, UnitPath: m.unitPath, Identity: m.req.Target.Persisted,
		Root: m.result.Root, WasRunning: m.result.WasRunning || m.req.StartNew,
		ExpectProcess: (m.result.WasRunning || m.req.StartNew) && !serviceIdentityUsesTimer(m.target),
	}); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseVerify, err)
	}
	if err := m.appendPhase(serviceIdentityPhaseVerify, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) commitServiceIdentityDatabase() error {
	if err := m.phase(serviceIdentityPhaseDBCommit); err != nil {
		return err
	}
	if err := m.ops.commit(m.previous, m.target); err != nil {
		return fmt.Errorf("%s: %w", serviceIdentityPhaseDBCommit, err)
	}
	m.dbCommitted = true
	if err := m.appendPhase(serviceIdentityPhaseDBCommit, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) completeServiceIdentityMigration(ctx context.Context) error {
	if err := m.finalizeServiceIdentityJournal(); err != nil {
		return err
	}
	if err := m.clearServiceIdentityDatasetMarker(ctx); err != nil {
		return err
	}
	return m.cleanupServiceIdentityMigration()
}

func (m *serviceIdentityMigration) finalizeServiceIdentityJournal() error {
	if err := m.phase(serviceIdentityPhaseComplete); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseComplete, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	m.completed = true
	if err := m.journal.Close(); err != nil {
		return fmt.Errorf("%s: close journal: %w", serviceIdentityPhaseComplete, err)
	}
	return nil
}

func (m *serviceIdentityMigration) clearServiceIdentityDatasetMarker(ctx context.Context) error {
	if m.req.RootPlan != nil && m.req.RootPlan.CreateNewRootZFS {
		if err := clearServiceIdentityZFSDatasetMarker(
			ctx, m.server.zfsRunner, m.req.RootPlan.NewRootZFS,
			m.materialization.DatasetGUID, m.migrationID,
		); err != nil {
			return fmt.Errorf("%s: clear target dataset transaction marker: %w", serviceIdentityPhaseComplete, err)
		}
	}
	return nil
}

func (m *serviceIdentityMigration) cleanupServiceIdentityMigration() error {
	if err := cleanupServiceIdentityGenerationBackups(m.generationBackups); err != nil {
		return fmt.Errorf("%s: cleanup generation backups: %w", serviceIdentityPhaseComplete, err)
	}
	if err := cleanupLegacyNativeRuntimeBackup(m.runtimeBackups); err != nil {
		return fmt.Errorf("%s: cleanup stale runtime artifact backup: %w", serviceIdentityPhaseComplete, err)
	}
	if err := cleanupServiceIdentityTransactionBackupDir(m.server.cfg.RootDir, m.journal.header.ID); err != nil {
		return fmt.Errorf("%s: cleanup transaction backup directory: %w", serviceIdentityPhaseComplete, err)
	}
	if err := m.ops.remove(m.journalPath); err != nil {
		return fmt.Errorf("%s: remove journal: %w", serviceIdentityPhaseComplete, err)
	}
	m.journal = nil
	return nil
}

func (m *serviceIdentityMigration) takeSnapshot(ctx context.Context, phase string, service *db.Service) error {
	if err := m.phase(phase); err != nil {
		return err
	}
	snapshot, err := m.ops.snapshot(ctx, service)
	if err != nil {
		return fmt.Errorf("%s: %w", phase, err)
	}
	if snapshot != "" {
		if m.result.ZFSSnapshot == "" {
			m.result.ZFSSnapshot = snapshot
		} else {
			m.result.ZFSSnapshot += ", " + snapshot
		}
	}
	if err := m.appendPhase(phase, serviceIdentityPhaseRecord{ZFSSnapshot: snapshot}); err != nil {
		return err
	}
	return nil
}

func (m *serviceIdentityMigration) verifyDatabaseStillPrevious() error {
	sv, err := m.server.serviceView(m.req.Service)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(sv.AsStruct(), m.previous) {
		return fmt.Errorf("service database record changed before the migration CAS")
	}
	return nil
}

func captureServiceIdentityGenerationBackups(rootDir string, paths []string, primaryUnit, migrationID string) ([]serviceIdentityGenerationBackup, error) {
	seen := make(map[string]struct{}, len(paths))
	backups := make([]serviceIdentityGenerationBackup, 0, len(paths))
	backupDir := serviceIdentityMigrationBackupDir(rootDir, migrationID)
	if err := requireAbsentServiceIdentityGenerationBackupDir(backupDir); err != nil {
		return nil, err
	}
	for _, path := range paths {
		backup, include, err := planServiceIdentityGenerationBackup(path, primaryUnit, backupDir, len(backups), seen)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

func requireAbsentServiceIdentityGenerationBackupDir(backupDir string) error {
	_, err := os.Lstat(backupDir)
	if err == nil {
		return fmt.Errorf("generation backup directory already exists: %s", backupDir)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func planServiceIdentityGenerationBackup(rawPath, primaryUnit, backupDir string, index int, seen map[string]struct{}) (serviceIdentityGenerationBackup, bool, error) {
	path := filepath.Clean(strings.TrimSpace(rawPath))
	if path == "." || !filepath.IsAbs(path) {
		return serviceIdentityGenerationBackup{}, false, fmt.Errorf("invalid generation install path %q", path)
	}
	if path == filepath.Clean(primaryUnit) {
		return serviceIdentityGenerationBackup{}, false, nil
	}
	if _, ok := seen[path]; ok {
		return serviceIdentityGenerationBackup{}, false, fmt.Errorf("duplicate generation install path %q", path)
	}
	seen[path] = struct{}{}
	original, err := captureServiceIdentityPathProof(path)
	if err != nil {
		return serviceIdentityGenerationBackup{}, false, fmt.Errorf("inspect generation install path %s: %w", path, err)
	}
	if original.Present && original.Nlink != 1 {
		return serviceIdentityGenerationBackup{}, false, fmt.Errorf("generation install path %s has %d hard links; exact rollback requires one", path, original.Nlink)
	}
	if err := validateServiceIdentityTransactionPath(original); err != nil {
		return serviceIdentityGenerationBackup{}, false, fmt.Errorf("generation install path %s: %w", path, err)
	}
	return serviceIdentityGenerationBackup{
		Path: path, BackupPath: filepath.Join(backupDir, "generation", fmt.Sprintf("%03d", index)),
		Present: original.Present, Original: original,
	}, true, nil
}

func (m *serviceIdentityMigration) validateGenerationTargets() error {
	if m.req.StageGeneration == nil {
		if len(m.req.GenerationPaths) != 0 || len(m.req.GenerationUnits) != 0 {
			return fmt.Errorf("generation paths and units require a stage-only generation callback")
		}
		return nil
	}
	// Tests inject a synthetic installer and explicit temporary paths. Production
	// requests must exactly match the target service's install plan so a journal
	// can never authorize cleanup outside Yeet-owned destinations.
	if m.req.ops != nil {
		return nil
	}
	expectedPaths, expectedUnits, err := serviceIdentityExpectedGenerationTargets(m.target, m.result.Root, m.unitPath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(cleanServiceIdentityPaths(m.req.GenerationPaths), expectedPaths) {
		return fmt.Errorf("generation install paths do not match the target service install plan")
	}
	if !reflect.DeepEqual(m.req.GenerationUnits, expectedUnits) {
		return fmt.Errorf("generation units do not match the target service install plan")
	}
	return nil
}

func (m *serviceIdentityMigration) configureCopiedRootGeneration() error {
	if !m.needsCopiedRootGeneration() {
		return nil
	}
	stager, err := m.ops.newGenerationStager(m.target, m.result.Root)
	if err != nil {
		return fmt.Errorf("prepare copied target generation: %w", err)
	}
	unitArtifact, ok := m.target.Artifacts.Gen(db.ArtifactSystemdUnit, m.target.Generation)
	if !ok {
		return fmt.Errorf("copied native service %q has no generation %d systemd unit to rewrite", m.req.Service, m.target.Generation)
	}
	m.req.GenerationPaths = stager.InstallTargetPaths()
	m.req.GenerationUnits = stager.InstallUnits()
	m.deferredReplacementUnit = true
	m.prepareGeneration = func(actualRoot string) error {
		return m.prepareCopiedRootGeneration(unitArtifact, actualRoot)
	}
	m.finalizeGeneration = func() error {
		return m.finalizeCopiedRootGeneration(stager)
	}
	m.req.StageGeneration = func(context.Context) error {
		return m.stageCopiedRootGeneration(stager)
	}
	return nil
}

func (m *serviceIdentityMigration) needsCopiedRootGeneration() bool {
	return m.req.RootPlan != nil && m.req.RootPlan.Mode == serviceRootMigrationCopy &&
		m.req.ReplacementUnit == "" && m.req.StageGeneration == nil && m.req.InstallGeneration == nil
}

func (m *serviceIdentityMigration) prepareCopiedRootGeneration(unitArtifact, actualRoot string) error {
	actualUnit, ok, err := relocatePathUnderRoot(unitArtifact, m.result.Root, actualRoot)
	if err != nil || !ok {
		return errors.Join(fmt.Errorf("locate copied target systemd unit under %s", actualRoot), err)
	}
	raw, err := os.ReadFile(actualUnit)
	if err != nil {
		return fmt.Errorf("read copied target systemd unit: %w", err)
	}
	replacement, err := rewriteServiceIdentityUnit(string(raw), m.req.Target.Persisted, m.result.Root)
	if err != nil {
		return fmt.Errorf("rewrite copied target systemd unit: %w", err)
	}
	info, err := os.Stat(actualUnit)
	if err != nil {
		return fmt.Errorf("inspect copied target systemd unit: %w", err)
	}
	if err := writeServiceIdentityUnitAtomically(actualUnit, []byte(replacement), info.Mode().Perm()); err != nil {
		return err
	}
	m.req.ReplacementUnit = replacement
	return nil
}

func (m *serviceIdentityMigration) finalizeCopiedRootGeneration(stager serviceIdentityGenerationStager) error {
	states, err := stager.InstallTargetStatesExcluding(m.unitPath)
	if err != nil {
		return fmt.Errorf("capture copied generation install intent: %w", err)
	}
	m.req.GenerationIntents = serviceIdentityInstallTargetStates(states)
	return nil
}

func (m *serviceIdentityMigration) stageCopiedRootGeneration(stager serviceIdentityGenerationStager) error {
	units, err := stager.StageInstallForReloadExcluding(m.unitPath)
	if err != nil {
		return err
	}
	if !slices.Equal(units, m.req.GenerationUnits) {
		return fmt.Errorf("staged copied generation units changed: got %v, want %v", units, m.req.GenerationUnits)
	}
	return nil
}

func serviceIdentityExpectedGenerationTargets(service *db.Service, root, primaryUnit string) ([]string, []string, error) {
	if service == nil {
		return nil, nil, fmt.Errorf("target service is unavailable for generation validation")
	}
	systemdService, err := svc.NewSystemdService(nil, service.View(), serviceRunDirForRoot(root))
	if err != nil {
		return nil, nil, fmt.Errorf("load target service install plan: %w", err)
	}
	paths := systemdService.InstallTargetPaths()
	unitDir := filepath.Dir(primaryUnit)
	for index, path := range paths {
		path = filepath.Clean(path)
		if filepath.Dir(path) == filepath.Clean(systemdSystemDir) {
			path = filepath.Join(unitDir, filepath.Base(path))
		}
		paths[index] = path
	}
	return paths, systemdService.InstallUnits(), nil
}

func cleanServiceIdentityPaths(paths []string) []string {
	cleaned := make([]string, len(paths))
	for index, path := range paths {
		cleaned[index] = filepath.Clean(strings.TrimSpace(path))
	}
	return cleaned
}

func serviceIdentityExpectedBackupPaths(paths []string, primaryUnit string) []string {
	primaryUnit = filepath.Clean(primaryUnit)
	backups := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if path != primaryUnit {
			backups = append(backups, path)
		}
	}
	return backups
}

func (m *serviceIdentityMigration) captureGenerationUnitEnablement(ctx context.Context) ([]serviceIdentityUnitEnablement, error) {
	if len(m.req.GenerationUnits) == 0 {
		return nil, nil
	}
	target := make(map[string]struct{}, len(m.req.GenerationUnits))
	for _, unit := range m.req.GenerationUnits {
		unit = strings.TrimSpace(unit)
		if unit == "" || filepath.Base(unit) != unit || strings.ContainsAny(unit, "\x00\r\n\t ") {
			return nil, fmt.Errorf("invalid generation unit %q", unit)
		}
		if _, duplicate := target[unit]; duplicate {
			return nil, fmt.Errorf("duplicate generation unit %q", unit)
		}
		target[unit] = struct{}{}
	}
	plan := serviceIdentityGenerationUnitPlan(m.previous, m.req.Service, m.req.GenerationUnits)
	units := make([]serviceIdentityUnitEnablement, 0, len(plan))
	for _, unit := range plan {
		enabled, err := m.ops.isEnabled(ctx, unit)
		if err != nil {
			return nil, fmt.Errorf("inspect enablement for %s: %w", unit, err)
		}
		_, targetEnabled := target[unit]
		units = append(units, serviceIdentityUnitEnablement{Unit: unit, Enabled: enabled, TargetEnabled: targetEnabled})
	}
	return units, nil
}

func serviceIdentityGenerationUnitPlan(previous *db.Service, fallback string, targetUnits []string) []string {
	seen := make(map[string]struct{})
	units := make([]string, 0, len(targetUnits)+3)
	appendUnit := func(unit string) {
		if _, ok := seen[unit]; ok {
			return
		}
		seen[unit] = struct{}{}
		units = append(units, unit)
	}
	for _, unit := range serviceIdentityEnabledUnits(previous, fallback) {
		appendUnit(unit)
	}
	for _, unit := range targetUnits {
		appendUnit(strings.TrimSpace(unit))
	}
	return units
}

func serviceIdentityEnabledUnits(service *db.Service, fallback string) []string {
	name := fallback
	if service != nil && service.Name != "" {
		name = service.Name
	}
	primary := name + ".service"
	if service != nil {
		if _, ok := service.Artifacts.Gen(db.ArtifactSystemdTimerFile, service.Generation); ok {
			primary = name + ".timer"
		}
	}
	units := []string{primary}
	if service != nil {
		if _, ok := service.Artifacts.Gen(db.ArtifactNetNSService, service.Generation); ok {
			units = append(units, "yeet-"+name+"-ns.service")
		}
		if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); ok {
			units = append(units, "yeet-"+name+"-ts.service")
		}
	}
	return units
}

func (m *serviceIdentityMigration) activateGenerationUnits(ctx context.Context) error {
	if len(m.generationUnits) == 0 {
		return nil
	}
	if err := m.phase(serviceIdentityPhaseGenerationActivate); err != nil {
		return err
	}
	if err := m.appendPhase(serviceIdentityPhaseGenerationActivate, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	m.generationActivationStarted = true
	for _, state := range m.generationUnits {
		enabled, observeErr := m.ops.isEnabled(ctx, state.Unit)
		if observeErr != nil {
			return fmt.Errorf("%s: inspect %s: %w", serviceIdentityPhaseGenerationActivate, state.Unit, observeErr)
		}
		if enabled == state.TargetEnabled {
			continue
		}
		var actionErr error
		if state.TargetEnabled {
			actionErr = m.ops.enable(ctx, state.Unit)
		} else {
			actionErr = m.ops.disable(ctx, state.Unit)
		}
		enabled, observeErr = m.ops.isEnabled(ctx, state.Unit)
		if observeErr != nil {
			return errors.Join(actionErr, fmt.Errorf("%s: verify %s: %w", serviceIdentityPhaseGenerationActivate, state.Unit, observeErr))
		}
		if enabled != state.TargetEnabled {
			return errors.Join(actionErr, fmt.Errorf("%s: unit %s enabled=%t, want %t", serviceIdentityPhaseGenerationActivate, state.Unit, enabled, state.TargetEnabled))
		}
	}
	return m.appendPhase(serviceIdentityPhaseGenerationEnabled, serviceIdentityPhaseRecord{})
}

func restoreServiceIdentityUnitEnablement(ctx context.Context, ops serviceIdentityMigrationOps, units []serviceIdentityUnitEnablement) error {
	var restoreErr error
	for _, state := range units {
		enabled, observeErr := ops.isEnabled(ctx, state.Unit)
		if observeErr != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("inspect enablement for %s: %w", state.Unit, observeErr))
			continue
		}
		if enabled == state.Enabled {
			continue
		}
		var actionErr error
		if state.Enabled {
			actionErr = ops.enable(ctx, state.Unit)
		} else {
			actionErr = ops.disable(ctx, state.Unit)
		}
		enabled, observeErr = ops.isEnabled(ctx, state.Unit)
		if observeErr != nil {
			restoreErr = errors.Join(restoreErr, actionErr, fmt.Errorf("observe restored enablement for %s: %w", state.Unit, observeErr))
			continue
		}
		if enabled != state.Enabled {
			restoreErr = errors.Join(restoreErr, actionErr, fmt.Errorf("unit %s enabled=%t, want %t", state.Unit, enabled, state.Enabled))
		}
	}
	return restoreErr
}

func (m *serviceIdentityMigration) backupGenerationPaths() error {
	if err := m.appendPhase(serviceIdentityPhaseGenerationBackup, serviceIdentityPhaseRecord{}); err != nil {
		return err
	}
	m.generationBackupStarted = true
	if len(m.generationBackups) == 0 {
		return m.completeGenerationPathBackup()
	}
	if err := prepareServiceIdentityGenerationBackupDirectory(m.generationBackups[0].BackupPath); err != nil {
		return err
	}
	for index := range m.generationBackups {
		if err := backupServiceIdentityGenerationPath(&m.generationBackups[index]); err != nil {
			return err
		}
	}
	return m.completeGenerationPathBackup()
}

func prepareServiceIdentityGenerationBackupDirectory(firstBackupPath string) error {
	backupDir := filepath.Dir(firstBackupPath)
	transactionDir := filepath.Dir(backupDir)
	parent := filepath.Dir(transactionDir)
	if _, err := ensureRootOnlyDirectory(parent); err != nil {
		return err
	}
	if _, err := ensureRootOnlyDirectory(transactionDir); err != nil {
		return err
	}
	if created, err := ensureRootOnlyDirectory(backupDir); err != nil {
		return err
	} else if !created {
		return fmt.Errorf("generation backup directory already exists: %s", backupDir)
	}
	if err := syncServiceIdentityJournalDirectory(transactionDir); err != nil {
		return err
	}
	return nil
}

func backupServiceIdentityGenerationPath(backup *serviceIdentityGenerationBackup) error {
	if err := validateServiceIdentityPathProof(backup.Original); err != nil {
		return err
	}
	if !backup.Present {
		return nil
	}
	proof, err := copyServiceIdentityProof(backup.Original, backup.BackupPath)
	if err != nil {
		return fmt.Errorf("back up generation install path %s: %w", backup.Path, err)
	}
	backup.Backup = proof
	return nil
}

func (m *serviceIdentityMigration) completeGenerationPathBackup() error {
	if err := m.appendPhase(serviceIdentityPhaseGenerationBackedUp, serviceIdentityPhaseRecord{
		GenerationBackups: append([]serviceIdentityGenerationBackup(nil), m.generationBackups...),
	}); err != nil {
		return err
	}
	m.generationBackupCompleted = true
	return nil
}

func restoreServiceIdentityGenerationBackups(backups []serviceIdentityGenerationBackup, staged []serviceIdentityPathProof, intents []serviceIdentityPathState, discardedRoot string) error {
	if err := validateServiceIdentityGenerationRestoration(backups, staged, intents, discardedRoot); err != nil {
		return err
	}
	var restoreErr error
	for _, backup := range backups {
		restoreErr = errors.Join(restoreErr, restoreServiceIdentityGenerationBackup(backup, discardedRoot))
	}
	if restoreErr == nil {
		restoreErr = cleanupServiceIdentityGenerationBackups(backups)
	}
	return restoreErr
}

func restoreServiceIdentityGenerationBackup(backup serviceIdentityGenerationBackup, discardedRoot string) error {
	if discardedRoot != "" && pathWithinServiceIdentityRoot(filepath.Clean(discardedRoot), filepath.Clean(backup.Path)) {
		return nil
	}
	current, err := captureServiceIdentityPathProof(backup.Path)
	if err != nil || serviceIdentityPathStateEqual(current, backup.Original) {
		return err
	}
	if backup.Present {
		_, err = copyServiceIdentityProofAt(
			filepath.Dir(backup.BackupPath), filepath.Base(backup.BackupPath), backup.Backup,
			filepath.Dir(backup.Path), filepath.Base(backup.Path), current,
		)
		if err != nil {
			return fmt.Errorf("restore generation install path %s: %w", backup.Path, err)
		}
		return nil
	}
	if err := removeServiceIdentityProofAt(filepath.Dir(backup.Path), filepath.Base(backup.Path), current); err != nil {
		return fmt.Errorf("remove newly installed generation path %s: %w", backup.Path, err)
	}
	return nil
}

func validateServiceIdentityGenerationRestoration(backups []serviceIdentityGenerationBackup, staged []serviceIdentityPathProof, intents []serviceIdentityPathState, discardedRoot string) error {
	for _, backup := range backups {
		if err := validateServiceIdentityGenerationBackupRestoration(backup, staged, intents, discardedRoot); err != nil {
			return err
		}
	}
	return nil
}

func validateServiceIdentityGenerationBackupRestoration(backup serviceIdentityGenerationBackup, staged []serviceIdentityPathProof, intents []serviceIdentityPathState, discardedRoot string) error {
	if discardedRoot != "" && pathWithinServiceIdentityRoot(filepath.Clean(discardedRoot), filepath.Clean(backup.Path)) {
		return nil
	}
	current, err := captureServiceIdentityPathProof(backup.Path)
	if err != nil || serviceIdentityPathStateEqual(current, backup.Original) {
		return err
	}
	proof, exactStaged := serviceIdentityProofForPath(staged, backup.Path)
	intent, intended := serviceIdentityStateForPath(intents, backup.Path)
	if !serviceIdentityGenerationCurrentStateAuthorized(current, proof, exactStaged, intent, intended) {
		return fmt.Errorf("generation path %s matches neither staged nor restored provenance", backup.Path)
	}
	if !backup.Present {
		return nil
	}
	if !backup.Backup.Present {
		return fmt.Errorf("generation path %s requires a backup that has no durable provenance", backup.Path)
	}
	return validateServiceIdentityPathProof(backup.Backup)
}

func serviceIdentityGenerationCurrentStateAuthorized(current, staged serviceIdentityPathProof, exactStaged bool, intent serviceIdentityPathState, intended bool) bool {
	return exactStaged && reflect.DeepEqual(current, staged) || intended && serviceIdentityPathMatchesState(current, intent)
}

func validateIncompleteServiceIdentityGeneration(backups []serviceIdentityGenerationBackup) error {
	for _, backup := range backups {
		if err := validateServiceIdentityPathProof(backup.Original); err != nil {
			return fmt.Errorf("generation staging lacks durable completion proof and path %s changed; paths were left untouched: %w", backup.Path, err)
		}
	}
	return nil
}

func cleanupIncompleteServiceIdentityBackups(backups []serviceIdentityGenerationBackup) error {
	if err := validateIncompleteServiceIdentityGeneration(backups); err != nil {
		return err
	}
	if len(backups) == 0 {
		return nil
	}
	dir := filepath.Dir(backups[0].BackupPath)
	allowedNames, allowedPrefixes, err := incompleteServiceIdentityBackupNames(backups, dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeIncompleteServiceIdentityBackupEntry(dir, entry, allowedNames, allowedPrefixes); err != nil {
			return err
		}
	}
	return removeServiceIdentityBackupDirectory(dir)
}

func incompleteServiceIdentityBackupNames(backups []serviceIdentityGenerationBackup, dir string) (map[string]struct{}, []string, error) {
	allowedNames := make(map[string]struct{}, len(backups)*2)
	allowedPrefixes := make([]string, 0, len(backups))
	for _, backup := range backups {
		if filepath.Clean(filepath.Dir(backup.BackupPath)) != filepath.Clean(dir) {
			return nil, nil, fmt.Errorf("incomplete transaction backups span multiple directories")
		}
		base := filepath.Base(backup.BackupPath)
		allowedNames[base] = struct{}{}
		allowedNames[base+".tmp"] = struct{}{}
		allowedPrefixes = append(allowedPrefixes, "."+base+".yeet-identity-")
	}
	return allowedNames, allowedPrefixes, nil
}

func removeIncompleteServiceIdentityBackupEntry(dir string, entry os.DirEntry, allowedNames map[string]struct{}, allowedPrefixes []string) error {
	name := entry.Name()
	if !incompleteServiceIdentityBackupNameAllowed(name, allowedNames, allowedPrefixes) {
		return fmt.Errorf("unexpected incomplete transaction backup %s was left untouched", filepath.Join(dir, name))
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("incomplete transaction backup path %s is a directory", path)
	}
	return os.Remove(path)
}

func incompleteServiceIdentityBackupNameAllowed(name string, allowedNames map[string]struct{}, allowedPrefixes []string) bool {
	if _, allowed := allowedNames[name]; allowed {
		return true
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func cleanupServiceIdentityGenerationBackups(backups []serviceIdentityGenerationBackup) error {
	if len(backups) == 0 {
		return nil
	}
	var cleanupErr error
	for _, backup := range backups {
		cleanupErr = errors.Join(cleanupErr, cleanupServiceIdentityGenerationBackup(backup))
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return removeServiceIdentityBackupDirectory(filepath.Dir(backups[0].BackupPath))
}

func cleanupServiceIdentityGenerationBackup(backup serviceIdentityGenerationBackup) error {
	if !backup.Backup.Present {
		return nil
	}
	current, err := captureServiceIdentityPathProof(backup.BackupPath)
	if err != nil || !current.Present {
		return err
	}
	if !reflect.DeepEqual(current, backup.Backup) {
		return fmt.Errorf("transaction backup %s changed from its durable provenance", backup.BackupPath)
	}
	if err := os.Remove(backup.BackupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeServiceIdentityBackupDirectory(dir string) error {
	removed := false
	if err := os.Remove(dir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		removed = true
	}
	if removed {
		return syncServiceIdentityJournalDirectory(filepath.Dir(dir))
	}
	return nil
}

func cleanupServiceIdentityTransactionBackupDir(rootDir, migrationID string) error {
	dir := serviceIdentityMigrationBackupDir(rootDir, migrationID)
	if err := os.Remove(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncServiceIdentityJournalDirectory(filepath.Dir(dir))
}

func serviceIdentityProofForPath(proofs []serviceIdentityPathProof, path string) (serviceIdentityPathProof, bool) {
	path = filepath.Clean(path)
	for _, proof := range proofs {
		if filepath.Clean(proof.Path) == path {
			return proof, true
		}
	}
	return serviceIdentityPathProof{}, false
}

func captureServiceIdentityGenerationPaths(paths []string) ([]serviceIdentityPathProof, error) {
	proofs := make([]serviceIdentityPathProof, 0, len(paths))
	for _, path := range paths {
		proof, err := captureServiceIdentityPathProof(path)
		if err != nil {
			return nil, err
		}
		if proof.Present && proof.Nlink != 1 {
			return nil, fmt.Errorf("staged generation path %s has %d hard links; exact rollback requires one", path, proof.Nlink)
		}
		if err := validateServiceIdentityTransactionPath(proof); err != nil {
			return nil, fmt.Errorf("staged generation path %s: %w", path, err)
		}
		proofs = append(proofs, proof)
	}
	return proofs, nil
}

func validateServiceIdentityPrimaryUnitRollback(previous, current serviceIdentityPathProof, intent serviceIdentityPathState) (serviceIdentityPathProof, bool, error) {
	actual, err := captureServiceIdentityPathProof(previous.Path)
	if err != nil {
		return serviceIdentityPathProof{}, false, err
	}
	if serviceIdentityPathStateEqual(actual, previous) {
		return actual, false, nil
	}
	if current.Path != "" && reflect.DeepEqual(actual, current) {
		return actual, true, nil
	}
	if intent.Path == "" || !serviceIdentityPathMatchesState(actual, intent) {
		return actual, false, fmt.Errorf("primary unit %s matches neither replacement nor restored provenance", previous.Path)
	}
	return actual, true, nil
}

func restoreServiceIdentityPrimaryUnit(previous, current serviceIdentityPathProof, intent serviceIdentityPathState, previousBytes []byte) error {
	actual, restore, err := validateServiceIdentityPrimaryUnitRollback(previous, current, intent)
	if err != nil || !restore {
		return err
	}
	if previous.Present {
		return writeServiceIdentityUnitAtomicallyExpected(previous.Path, previousBytes, previous.Mode.Perm(), actual, previous.UID, previous.GID)
	}
	root, rel := filepath.Dir(previous.Path), filepath.Base(previous.Path)
	if err := removeServiceIdentityProofAt(root, rel, actual); err != nil {
		return fmt.Errorf("remove replacement unit %s: %w", previous.Path, err)
	}
	return nil
}

func serviceIdentityMigrationPrimaryProof(m *serviceIdentityMigration) serviceIdentityPathProof {
	if m.writtenUnit.Path != "" {
		return m.writtenUnit
	}
	if m.unitBeforeWrite.Path != "" {
		return m.unitBeforeWrite
	}
	return m.previousUnitProof
}

func (m *serviceIdentityMigration) captureJournal(ctx context.Context) error {
	runtimeState, err := m.ops.captureRuntime(ctx, m.req.Service)
	if err != nil {
		return fmt.Errorf("capture exact pre-migration runtime state: %w", err)
	}
	m.previousRuntime = runtimeState
	m.result.WasRunning = serviceIdentityPrimaryRuntimeActive(m.previous, m.req.Service, runtimeState)
	id, err := newServiceIdentityMigrationID()
	if err != nil {
		return err
	}
	m.migrationID = id
	header := serviceIdentityJournalHeader{
		ID: id, Service: m.req.Service, Root: m.result.Root,
		TargetIdentity: m.req.Target.Persisted, PreviousUnit: string(m.previousUnit),
		PreviousUnitPresent: m.unitPresent, WasRunning: m.result.WasRunning,
		PreviousUnitProof:      m.previousUnitProof,
		PreviousRuntimeUnits:   append([]serviceIdentityRuntimeUnitState(nil), m.previousRuntime...),
		PreviousServicePresent: m.predecessorPresent, PreviousService: m.predecessor.Clone(),
		ObservedService: m.previous.Clone(), TargetService: m.target.Clone(), RootPlan: cloneServiceRootMigrationPlan(m.req.RootPlan),
		PreviousUnitPath: m.unitPath, PreviousUnitMode: m.previousUnitMode,
		PreviousRoot: serviceRootFromConfig(m.server.cfg, *m.previous), PreviousDataset: m.previous.ServiceRootZFS,
		TargetRoot: m.result.Root, TargetDataset: m.target.ServiceRootZFS,
	}
	header.GenerationBackups, err = captureServiceIdentityGenerationBackups(m.server.cfg.RootDir, m.req.GenerationPaths, m.unitPath, id)
	if err != nil {
		return err
	}
	m.generationBackups = append([]serviceIdentityGenerationBackup(nil), header.GenerationBackups...)
	header.GenerationUnits, err = m.captureGenerationUnitEnablement(ctx)
	if err != nil {
		return err
	}
	m.generationUnits = append([]serviceIdentityUnitEnablement(nil), header.GenerationUnits...)
	if m.req.RootPlan != nil {
		header.TargetDatasetCreate = m.req.RootPlan.CreateNewRootZFS
	}
	if m.predecessor != nil && m.predecessor.Identity != nil {
		header.PreviousIdentity = m.predecessor.Identity.Clone()
	}
	m.journal, err = createServiceIdentityJournal(m.server.cfg.RootDir, header)
	if err != nil {
		return err
	}
	m.journalPath = m.journal.Path()
	return nil
}

func (m *serviceIdentityMigration) appendPhase(phase string, record serviceIdentityPhaseRecord) error {
	if m.journal == nil {
		return fmt.Errorf("%s: identity journal is unavailable", phase)
	}
	record.Phase = phase
	if err := m.journal.AppendPhase(record); err != nil {
		return fmt.Errorf("%s: %w", phase, err)
	}
	return nil
}

func (m *serviceIdentityMigration) phase(phase string) error {
	if err := m.ops.phase(phase); err != nil {
		return fmt.Errorf("%s: %w", phase, err)
	}
	return nil
}

func (m *serviceIdentityMigration) rollback(ctx context.Context) error {
	if err := m.validateRollbackPreconditions(); err != nil {
		return m.closeJournalAfterRollbackError(err)
	}
	criticalErr := m.restoreCriticalServiceIdentityState(ctx)
	rollbackErr := criticalErr
	if criticalErr == nil {
		rollbackErr = m.ops.restoreRuntime(ctx, m.req.Service, m.previousRuntime)
	}
	if rollbackErr == nil {
		rollbackErr = m.verifyRollback(ctx)
	}
	return m.finishServiceIdentityRollback(rollbackErr)
}

func (m *serviceIdentityMigration) validateRollbackPreconditions() error {
	if err := m.validateIncompleteRollbackBackups(); err != nil {
		return err
	}
	if err := m.validateRuntimeRollbackBackups(); err != nil {
		return err
	}
	return m.validatePrimaryUnitRollback()
}

func (m *serviceIdentityMigration) validateIncompleteRollbackBackups() error {
	if m.generationBackupStarted && !m.generationBackupCompleted {
		return validateIncompleteServiceIdentityGeneration(m.generationBackups)
	}
	if m.runtimeBackupStarted && !m.runtimeBackupCompleted {
		return validateIncompleteServiceIdentityGeneration(m.runtimeBackups)
	}
	return nil
}

func (m *serviceIdentityMigration) validateRuntimeRollbackBackups() error {
	if !m.runtimeBackupCompleted {
		return nil
	}
	return validateLegacyNativeRuntimeRestoration(m.server.cfg.RootDir, m.result.Root, m.runtimeBackups, "")
}

func (m *serviceIdentityMigration) validatePrimaryUnitRollback() error {
	if !m.unitWriteStarted && !m.generationStaged && !m.generationStageStarted {
		return nil
	}
	_, _, err := validateServiceIdentityPrimaryUnitRollback(
		m.previousUnitProof, serviceIdentityMigrationPrimaryProof(m), m.primaryUnitIntent,
	)
	return err
}

func (m *serviceIdentityMigration) closeJournalAfterRollbackError(cause error) error {
	if m.journal == nil {
		return cause
	}
	cause = errors.Join(cause, m.journal.Close())
	m.journal = nil
	return cause
}

func (m *serviceIdentityMigration) restoreCriticalServiceIdentityState(ctx context.Context) error {
	var criticalErr error
	if m.replacementStarted {
		criticalErr = errors.Join(criticalErr, m.ensureReplacementStopped(ctx))
	}
	criticalErr = errors.Join(criticalErr, m.restoreDatabase())
	criticalErr = errors.Join(criticalErr, m.restorePrimaryServiceIdentityUnit())
	criticalErr = errors.Join(criticalErr, m.restoreServiceIdentityRuntimeBackups())
	if m.ownership && m.journalPath != "" {
		criticalErr = errors.Join(criticalErr, m.ops.restore(m.journalPath))
	}
	criticalErr = errors.Join(criticalErr, m.restoreServiceIdentityGeneration())
	criticalErr = errors.Join(criticalErr, m.discardMaterializedServiceIdentityRoot(ctx))
	criticalErr = errors.Join(criticalErr, m.reloadRestoredServiceIdentityUnits(ctx))
	criticalErr = errors.Join(criticalErr, m.restoreServiceIdentityEnablement(ctx))
	return criticalErr
}

func (m *serviceIdentityMigration) restorePrimaryServiceIdentityUnit() error {
	if !m.unitWriteStarted && !m.generationStaged && !m.generationStageStarted {
		return nil
	}
	if err := restoreServiceIdentityPrimaryUnit(
		m.previousUnitProof, serviceIdentityMigrationPrimaryProof(m), m.primaryUnitIntent, m.previousUnit,
	); err != nil {
		return fmt.Errorf("restore old unit: %w", err)
	}
	return nil
}

func (m *serviceIdentityMigration) restoreServiceIdentityRuntimeBackups() error {
	if m.runtimeBackupCompleted {
		return restoreLegacyNativeRuntimeBackup(m.server.cfg.RootDir, m.result.Root, m.runtimeBackups, "")
	}
	if m.runtimeBackupStarted {
		return cleanupIncompleteServiceIdentityBackups(m.runtimeBackups)
	}
	return nil
}

func (m *serviceIdentityMigration) restoreServiceIdentityGeneration() error {
	if m.generationBackupCompleted {
		if err := validateServiceIdentityGenerationRestoration(m.generationBackups, m.generationPaths, m.generationIntents, ""); err != nil {
			return err
		}
		return restoreServiceIdentityGenerationBackups(m.generationBackups, m.generationPaths, m.generationIntents, "")
	}
	if m.generationBackupStarted {
		return cleanupIncompleteServiceIdentityBackups(m.generationBackups)
	}
	return nil
}

func (m *serviceIdentityMigration) discardMaterializedServiceIdentityRoot(ctx context.Context) error {
	if m.rootMaterialized && m.req.RootPlan != nil {
		if m.initialMaterialization.Phase != "" {
			m.materialization = m.initialMaterialization
		}
		return m.ops.discardRoot(ctx, *m.req.RootPlan, m.datasetCreated)
	}
	return nil
}

func (m *serviceIdentityMigration) reloadRestoredServiceIdentityUnits(ctx context.Context) error {
	if m.unitPath != "" {
		return m.ops.reload(ctx)
	}
	return nil
}

func (m *serviceIdentityMigration) restoreServiceIdentityEnablement(ctx context.Context) error {
	if m.generationActivationStarted {
		return restoreServiceIdentityUnitEnablement(ctx, m.ops, m.generationUnits)
	}
	return nil
}

func (m *serviceIdentityMigration) finishServiceIdentityRollback(rollbackErr error) error {
	if m.journal != nil {
		rollbackErr = errors.Join(rollbackErr, m.journal.Close())
		m.journal = nil
	}
	if rollbackErr == nil && m.journalPath != "" {
		rollbackErr = cleanupServiceIdentityTransactionBackupDir(m.server.cfg.RootDir, m.migrationID)
	}
	if rollbackErr == nil && m.journalPath != "" {
		rollbackErr = m.ops.remove(m.journalPath)
	}
	return rollbackErr
}

func (m *serviceIdentityMigration) ensureReplacementStopped(ctx context.Context) error {
	err := stopServiceIdentityObserved(ctx, m.ops.stopReplacement, m.ops.isReplacementRunning, m.req.Service)
	if err == nil {
		m.replacementStarted = false
	}
	return err
}

func stopServiceIdentityObserved(
	ctx context.Context,
	stop func(context.Context, string) error,
	isRunning func(context.Context, string) (bool, error),
	service string,
) error {
	firstErr := stop(ctx, service)
	running, observeErr := isRunning(ctx, service)
	if observeErr != nil {
		if firstErr != nil {
			firstErr = fmt.Errorf("stop service: %w", firstErr)
		}
		return errors.Join(firstErr, fmt.Errorf("observe service after stop: %w", observeErr))
	}
	if !running {
		return nil
	}
	secondErr := stop(ctx, service)
	running, observeErr = isRunning(ctx, service)
	if observeErr != nil {
		return errors.Join(firstErr, secondErr, fmt.Errorf("observe service after stop retry: %w", observeErr))
	}
	if running {
		return errors.Join(firstErr, secondErr, fmt.Errorf("service is still running after rollback stop"))
	}
	return nil
}

func reconcileServiceIdentityRunningState(ctx context.Context, ops serviceIdentityMigrationOps, service string, wantRunning bool) error {
	running, err := ops.isPreviousRunning(ctx, service)
	if err != nil {
		return fmt.Errorf("observe service running state: %w", err)
	}
	if running == wantRunning {
		return nil
	}
	var actionErr error
	if wantRunning {
		actionErr = ops.startPrevious(ctx, service)
	} else {
		actionErr = ops.stopPrevious(ctx, service)
	}
	running, observeErr := ops.isPreviousRunning(ctx, service)
	if observeErr != nil {
		return errors.Join(actionErr, fmt.Errorf("observe service running state after rollback action: %w", observeErr))
	}
	if running == wantRunning {
		return nil
	}
	return errors.Join(actionErr, fmt.Errorf("service running state is %t after rollback action, want %t", running, wantRunning))
}

func (m *serviceIdentityMigration) restoreDatabase() error {
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		current, ok := data.Services[m.req.Service]
		if !m.predecessorPresent {
			if !ok {
				return nil
			}
			if !reflect.DeepEqual(current, m.previous) && !reflect.DeepEqual(current, m.target) {
				return fmt.Errorf("service %q no longer matches the observed or provisional target database state", m.req.Service)
			}
			delete(data.Services, m.req.Service)
			return nil
		}
		if !ok {
			return fmt.Errorf("service %q disappeared during rollback", m.req.Service)
		}
		switch {
		case reflect.DeepEqual(current, m.previous):
			return nil
		case reflect.DeepEqual(current, m.target):
			data.Services[m.req.Service] = m.previous.Clone()
			return nil
		default:
			return fmt.Errorf("service %q no longer matches the old or provisional target database state", m.req.Service)
		}
	})
	return err
}

func (m *serviceIdentityMigration) verifyRollback(ctx context.Context) error {
	if err := m.verifyRolledBackServiceIdentityDatabase(); err != nil {
		return err
	}
	if err := m.verifyRolledBackServiceIdentityUnit(); err != nil {
		return err
	}
	return m.verifyRolledBackServiceIdentityRuntime(ctx)
}

func (m *serviceIdentityMigration) verifyRolledBackServiceIdentityDatabase() error {
	sv, err := m.server.serviceView(m.req.Service)
	if !m.predecessorPresent {
		if err == nil {
			return fmt.Errorf("rolled-back database service should be absent, found %#v", sv.AsStruct())
		}
		if !errors.Is(err, errServiceNotFound) {
			return fmt.Errorf("rolled-back database service should be absent: %w", err)
		}
	} else {
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(sv.AsStruct(), m.predecessor) {
			return fmt.Errorf("rolled-back database record does not match the captured predecessor record")
		}
	}
	return nil
}

func (m *serviceIdentityMigration) verifyRolledBackServiceIdentityUnit() error {
	raw, present, mode, err := readOptionalServiceIdentityUnit(m.unitPath)
	if err != nil {
		return err
	}
	if present != m.unitPresent || present && string(raw) != string(m.previousUnit) {
		return fmt.Errorf("rolled-back primary unit bytes/existence do not match the captured previous state")
	}
	if present && mode.Perm() != m.previousUnitMode.Perm() {
		return fmt.Errorf("rolled-back primary unit mode is %o, want %o", mode.Perm(), m.previousUnitMode.Perm())
	}
	return nil
}

func (m *serviceIdentityMigration) verifyRolledBackServiceIdentityRuntime(ctx context.Context) error {
	runtimeState, err := m.ops.captureRuntime(ctx, m.req.Service)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(runtimeState, m.previousRuntime) {
		return fmt.Errorf("rolled-back runtime state is %#v, want %#v", runtimeState, m.previousRuntime)
	}
	return nil
}

func (m *serviceIdentityMigration) migrationError(cause, rollbackErr error) error {
	root := serviceRootFromConfig(m.server.cfg, *m.previous)
	err := fmt.Errorf(
		"service identity migration for native systemd service %q failed in %v (%s -> %s, root %s, dataset %q); journal: %s; snapshot: %s; retry: %s: %w",
		m.req.Service, phaseFromMigrationError(cause), formatServiceIdentity(m.result.Previous.Persisted),
		formatServiceIdentity(m.req.Target.Persisted), root, m.previous.ServiceRootZFS,
		m.journalPath, m.result.ZFSSnapshot, m.retryCommand(), cause,
	)
	if phaseFromMigrationError(cause) == serviceIdentityPhaseStart && m.req.Target.Persisted.UID != 0 {
		if diagnostic := m.startFailureDiagnostic(); diagnostic != "" {
			err = fmt.Errorf("%w\n%s", err, diagnostic)
		}
	}
	if rollbackErr != nil {
		return errors.Join(err, fmt.Errorf("rollback incomplete; old unit/ownership/running state may require repair and the journal was retained: %w", rollbackErr))
	}
	return fmt.Errorf("%w; rollback restored the old unit, ownership, database identity, root, and running state", err)
}

func (m *serviceIdentityMigration) startFailureDiagnostic() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := serviceIdentityStartJournal(ctx, m.req.Service+".service")
	if err != nil || len(raw) == 0 {
		return ""
	}
	redacted := serviceIdentityEnvironmentAssignment.ReplaceAllString(string(raw), `$1=<redacted>`)
	if len(redacted) > serviceIdentityJournalDiagnosticLimit {
		redacted = redacted[len(redacted)-serviceIdentityJournalDiagnosticLimit:]
	}
	redacted = strings.TrimSpace(redacted)
	if redacted == "" {
		return ""
	}
	id := m.req.Target.Persisted
	return fmt.Sprintf(
		"service %s failed to start as %s:%s (%d:%d)\nsystemd:\n%s\ncheck service data permissions, privileged ports, devices, and absolute host paths",
		m.req.Service, id.RequestedUser, id.RequestedGroup, id.UID, id.GID, redacted,
	)
}

func readServiceIdentityStartJournal(ctx context.Context, unit string) ([]byte, error) {
	return exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", "80", "-o", "cat", "--no-pager").CombinedOutput()
}

func (m *serviceIdentityMigration) retryCommand() string {
	args := []string{"yeet", "service", "set", m.req.Service, "--run-as=" + m.req.Requested}
	if m.req.RootPlan != nil {
		args = append(args, "--service-root="+m.req.RootPlan.NewRoot)
		if m.req.RootPlan.NewRootZFS != "" {
			args = append(args, "--zfs")
		}
		switch m.req.RootPlan.Mode {
		case serviceRootMigrationCopy:
			args = append(args, "--copy")
		case serviceRootMigrationEmpty:
			args = append(args, "--empty")
		}
	}
	return shellJoinHostStorageRecoveryArgs(args)
}

func (m *serviceIdentityMigration) replacementUnit() (string, error) {
	if !m.unitPresent {
		return "", fmt.Errorf("installed primary unit %s does not exist", m.unitPath)
	}
	return rewriteServiceIdentityUnit(string(m.previousUnit), m.req.Target.Persisted, m.result.Root)
}

func (m *serviceIdentityMigration) defaultOps() serviceIdentityMigrationOps {
	previousStop := func(ctx context.Context, fallback string) error {
		stop, _ := serviceIdentityRuntimeActions(m.previous)
		return stop(ctx, fallback)
	}
	previousStart := func(ctx context.Context, fallback string) error {
		_, start := serviceIdentityRuntimeActions(m.previous)
		return start(ctx, fallback)
	}
	targetStart := func(ctx context.Context, fallback string) error {
		_, start := serviceIdentityRuntimeActions(m.target)
		return start(ctx, fallback)
	}
	replacementStop := func(ctx context.Context, fallback string) error {
		return serviceIdentityCombinedStopAction(m.target, m.previous)(ctx, fallback)
	}
	previousRunning := func(ctx context.Context, fallback string) (bool, error) {
		return serviceIdentityRuntimeRunningAction(m.previous)(ctx, fallback)
	}
	targetRunning := func(ctx context.Context, fallback string) (bool, error) {
		return serviceIdentityRuntimeRunningAction(m.target)(ctx, fallback)
	}
	replacementRunning := func(ctx context.Context, fallback string) (bool, error) {
		return serviceIdentityCombinedRunningAction(m.target, m.previous)(ctx, fallback)
	}
	return serviceIdentityMigrationOps{
		phase:                func(string) error { return nil },
		unitPath:             func(service string) string { return filepath.Join(systemdSystemDir, service+".service") },
		isPreviousRunning:    previousRunning,
		isTargetRunning:      targetRunning,
		isReplacementRunning: replacementRunning,
		captureRuntime: func(ctx context.Context, fallback string) ([]serviceIdentityRuntimeUnitState, error) {
			return captureServiceIdentityRuntimeState(ctx, m.previous, fallback)
		},
		restoreRuntime: func(ctx context.Context, fallback string, state []serviceIdentityRuntimeUnitState) error {
			return restoreServiceIdentityRuntimeState(ctx, m.previous, fallback, state)
		},
		isRunning: func(_ context.Context, service string) (bool, error) {
			return m.server.IsServiceRunning(service)
		},
		stop:            previousStop,
		start:           targetStart,
		stopReplacement: replacementStop,
		startPrevious:   previousStart,
		stopPrevious:    previousStop,
		snapshot: func(ctx context.Context, service *db.Service) (string, error) {
			if service.ServiceRootZFS == "" {
				return "", nil
			}
			return createServiceSnapshot(ctx, m.server.zfsRunner, snapshotCreateRequest{
				Service: service.Name, Dataset: service.ServiceRootZFS, Event: snapshotEventServiceIdentityMigration,
				Generation: intPointer(service.Generation), Now: time.Now(),
			})
		},
		materialize:   m.materializeRoot,
		discardRoot:   m.discardRoot,
		inspect:       inspectServiceIdentityChange,
		inspectSource: inspectServiceIdentityChange,
		apply:         applyServiceIdentityInspection,
		restore: func(path string) error {
			contents, err := loadServiceIdentityJournal(path)
			if err != nil {
				return err
			}
			return restoreServiceIdentityJournalContents(contents)
		},
		reload: func(context.Context) error {
			return catchSystemctl("daemon-reload")
		},
		writeUnit: writeServiceIdentityUnitAtomicallyExpected,
		verify:    m.verify,
		commit:    m.commit,
		remove: func(path string) error {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return syncServiceIdentityJournalDir(filepath.Dir(path))
		},
		isEnabled: func(ctx context.Context, unit string) (bool, error) {
			err := exec.CommandContext(ctx, "systemctl", "is-enabled", unit).Run()
			if err == nil {
				return true, nil
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return false, nil
			}
			return false, err
		},
		enable:  func(_ context.Context, unit string) error { return catchSystemctl("enable", unit) },
		disable: func(_ context.Context, unit string) error { return catchSystemctl("disable", unit) },
		newGenerationStager: func(service *db.Service, root string) (serviceIdentityGenerationStager, error) {
			stager, err := svc.NewSystemdService(m.server.cfg.DB, service.View(), serviceRunDirForRoot(root))
			if err != nil {
				return nil, err
			}
			return stager, nil
		},
	}
}

func serviceIdentityRuntimeActions(service *db.Service) (
	func(context.Context, string) error,
	func(context.Context, string) error,
) {
	return func(ctx context.Context, fallback string) error {
			stopUnits, _ := serviceIdentityRuntimeUnits(service, fallback)
			return stopServiceIdentityUnits(ctx, stopUnits)
		}, func(ctx context.Context, fallback string) error {
			_, startUnits := serviceIdentityRuntimeUnits(service, fallback)
			return startServiceIdentityUnits(ctx, startUnits)
		}
}

func serviceIdentityRuntimeRunningAction(service *db.Service) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, fallback string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		_, startUnits := serviceIdentityRuntimeUnits(service, fallback)
		return catchSystemdUnitActive(startUnits[len(startUnits)-1]), nil
	}
}

func serviceIdentityCombinedRunningAction(services ...*db.Service) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, fallback string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		for _, unit := range uniqueServiceIdentityStopUnits(services, fallback) {
			if catchSystemdUnitActive(unit) {
				return true, nil
			}
		}
		return false, nil
	}
}

func serviceIdentityCombinedStopAction(services ...*db.Service) func(context.Context, string) error {
	return func(ctx context.Context, fallback string) error {
		return stopServiceIdentityUnits(ctx, uniqueServiceIdentityStopUnits(services, fallback))
	}
}

func uniqueServiceIdentityStopUnits(services []*db.Service, fallback string) []string {
	seen := make(map[string]struct{})
	var units []string
	for _, service := range services {
		stopUnits, _ := serviceIdentityRuntimeUnits(service, fallback)
		for _, unit := range stopUnits {
			if _, ok := seen[unit]; ok {
				continue
			}
			seen[unit] = struct{}{}
			units = append(units, unit)
		}
	}
	return units
}

func serviceIdentityRuntimeUnits(service *db.Service, fallback string) (stop, start []string) {
	name := fallback
	if service != nil && service.Name != "" {
		name = service.Name
	}
	primary := name + ".service"
	hasArtifact := func(db.ArtifactName) bool { return false }
	if service != nil {
		hasArtifact = func(artifact db.ArtifactName) bool {
			_, ok := service.Artifacts.Gen(artifact, service.Generation)
			return ok
		}
	}
	if hasArtifact(db.ArtifactSystemdTimerFile) {
		primary = name + ".timer"
	}
	stop = append(stop, primary)
	if primary != name+".service" {
		stop = append(stop, name+".service")
	}
	if hasArtifact(db.ArtifactTSService) {
		stop = append(stop, "yeet-"+name+"-ts.service")
	}
	if hasArtifact(db.ArtifactNetNSService) {
		stop = append(stop, "yeet-"+name+"-ns.service")
		start = append(start, "yeet-"+name+"-ns.service")
	}
	if hasArtifact(db.ArtifactTSService) {
		start = append(start, "yeet-"+name+"-ts.service")
	}
	start = append(start, primary)
	return stop, start
}

func serviceIdentityPrimaryRuntimeUnit(service *db.Service, fallback string) string {
	_, start := serviceIdentityRuntimeUnits(service, fallback)
	return start[len(start)-1]
}

func captureServiceIdentityRuntimeState(ctx context.Context, service *db.Service, fallback string) ([]serviceIdentityRuntimeUnitState, error) {
	stop, _ := serviceIdentityRuntimeUnits(service, fallback)
	state := make([]serviceIdentityRuntimeUnitState, 0, len(stop))
	for _, unit := range stop {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state = append(state, serviceIdentityRuntimeUnitState{Unit: unit, Active: catchSystemdUnitActive(unit)})
	}
	return state, nil
}

func serviceIdentityAnyRuntimeActive(state []serviceIdentityRuntimeUnitState) bool {
	for _, unit := range state {
		if unit.Active {
			return true
		}
	}
	return false
}

func serviceIdentityPrimaryRuntimeActive(service *db.Service, fallback string, state []serviceIdentityRuntimeUnitState) bool {
	primary := serviceIdentityPrimaryRuntimeUnit(service, fallback)
	for _, unit := range state {
		if unit.Unit == primary {
			return unit.Active
		}
	}
	return false
}

func restoreServiceIdentityRuntimeState(ctx context.Context, service *db.Service, fallback string, desired []serviceIdentityRuntimeUnitState) error {
	want := make(map[string]bool, len(desired))
	for _, state := range desired {
		want[state.Unit] = state.Active
	}
	stop, start := serviceIdentityRuntimeUnits(service, fallback)
	if err := restoreInactiveServiceIdentityUnits(ctx, stop, want); err != nil {
		return err
	}
	started, err := restoreOrderedServiceIdentityUnits(ctx, start, want)
	if err != nil {
		return err
	}
	if err := restoreRemainingServiceIdentityUnits(ctx, desired, started); err != nil {
		return err
	}
	actual, err := captureServiceIdentityRuntimeState(ctx, service, fallback)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, desired) {
		return fmt.Errorf("restored runtime state is %#v, want %#v", actual, desired)
	}
	return nil
}

func restoreInactiveServiceIdentityUnits(ctx context.Context, units []string, want map[string]bool) error {
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return err
		}
		if catchSystemdUnitActive(unit) && !want[unit] {
			if err := catchSystemctl("stop", unit); err != nil {
				return fmt.Errorf("restore stopped state for %s: %w", unit, err)
			}
		}
	}
	return nil
}

func restoreOrderedServiceIdentityUnits(ctx context.Context, units []string, want map[string]bool) (map[string]struct{}, error) {
	started := make(map[string]struct{}, len(units))
	for _, unit := range units {
		started[unit] = struct{}{}
		if want[unit] && !catchSystemdUnitActive(unit) {
			if err := catchSystemctl("start", unit); err != nil {
				return nil, fmt.Errorf("restore active state for %s: %w", unit, err)
			}
		}
	}
	return started, nil
}

func restoreRemainingServiceIdentityUnits(ctx context.Context, desired []serviceIdentityRuntimeUnitState, started map[string]struct{}) error {
	for _, state := range desired {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ordered := started[state.Unit]; ordered || !state.Active || catchSystemdUnitActive(state.Unit) {
			continue
		}
		if err := catchSystemctl("start", state.Unit); err != nil {
			return fmt.Errorf("restore active state for %s: %w", state.Unit, err)
		}
	}
	return nil
}

func stopServiceIdentityUnits(ctx context.Context, units []string) error {
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !catchSystemdUnitActive(unit) {
			continue
		}
		if err := catchSystemctl("stop", unit); err != nil {
			return fmt.Errorf("stop %s: %w", unit, err)
		}
		if catchSystemdUnitActive(unit) {
			return fmt.Errorf("unit %s is still active after stop", unit)
		}
	}
	return nil
}

func startServiceIdentityUnits(ctx context.Context, units []string) error {
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := catchSystemctl("start", unit); err != nil {
			return fmt.Errorf("start %s: %w", unit, err)
		}
		if !catchSystemdUnitActive(unit) {
			return fmt.Errorf("unit %s is not active after start", unit)
		}
	}
	return nil
}

func (ops *serviceIdentityMigrationOps) merge(overrides serviceIdentityMigrationOps) {
	ops.mergeRunningOverrides(overrides)
	ops.mergeLifecycleOverrides(overrides)
	ops.mergeRootOverrides(overrides)
	ops.mergeInspectionOverrides(overrides)
	ops.mergeCommitOverrides(overrides)
	ops.mergeEnablementOverrides(overrides)
}

func (ops *serviceIdentityMigrationOps) mergeRunningOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.phase != nil {
		ops.phase = overrides.phase
	}
	ops.mergeCombinedRunningOverride(overrides)
	ops.mergeExplicitRunningOverrides(overrides)
}

func (ops *serviceIdentityMigrationOps) mergeCombinedRunningOverride(overrides serviceIdentityMigrationOps) {
	if overrides.isRunning != nil {
		ops.isRunning = overrides.isRunning
		if overrides.isPreviousRunning == nil {
			ops.isPreviousRunning = overrides.isRunning
		}
		if overrides.isTargetRunning == nil {
			ops.isTargetRunning = overrides.isRunning
		}
		if overrides.isReplacementRunning == nil {
			ops.isReplacementRunning = overrides.isRunning
		}
		if overrides.captureRuntime == nil {
			ops.captureRuntime = func(ctx context.Context, service string) ([]serviceIdentityRuntimeUnitState, error) {
				running, err := ops.isPreviousRunning(ctx, service)
				return []serviceIdentityRuntimeUnitState{{Unit: serviceIdentityPrimaryRuntimeUnit(nil, service), Active: running}}, err
			}
		}
		if overrides.restoreRuntime == nil {
			ops.restoreRuntime = func(ctx context.Context, service string, state []serviceIdentityRuntimeUnitState) error {
				want := len(state) != 0 && state[0].Active
				return reconcileServiceIdentityRunningState(ctx, *ops, service, want)
			}
		}
	}
}

func (ops *serviceIdentityMigrationOps) mergeExplicitRunningOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.isPreviousRunning != nil {
		ops.isPreviousRunning = overrides.isPreviousRunning
	}
	if overrides.isTargetRunning != nil {
		ops.isTargetRunning = overrides.isTargetRunning
	}
	if overrides.isReplacementRunning != nil {
		ops.isReplacementRunning = overrides.isReplacementRunning
	}
	if overrides.captureRuntime != nil {
		ops.captureRuntime = overrides.captureRuntime
	}
	if overrides.restoreRuntime != nil {
		ops.restoreRuntime = overrides.restoreRuntime
	}
}

func (ops *serviceIdentityMigrationOps) mergeLifecycleOverrides(overrides serviceIdentityMigrationOps) {
	ops.mergeCombinedLifecycleOverrides(overrides)
	ops.mergeExplicitLifecycleOverrides(overrides)
}

func (ops *serviceIdentityMigrationOps) mergeCombinedLifecycleOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.stop != nil {
		ops.stop = overrides.stop
		if overrides.stopReplacement == nil {
			ops.stopReplacement = overrides.stop
		}
		if overrides.stopPrevious == nil {
			ops.stopPrevious = overrides.stop
		}
	}
	if overrides.start != nil {
		ops.start = overrides.start
		if overrides.startPrevious == nil {
			ops.startPrevious = overrides.start
		}
	}
	if overrides.inspect != nil {
		if overrides.inspectSource == nil {
			ops.inspectSource = overrides.inspect
		}
	}
}

func (ops *serviceIdentityMigrationOps) mergeExplicitLifecycleOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.stopReplacement != nil {
		ops.stopReplacement = overrides.stopReplacement
	}
	if overrides.startPrevious != nil {
		ops.startPrevious = overrides.startPrevious
	}
	if overrides.stopPrevious != nil {
		ops.stopPrevious = overrides.stopPrevious
	}
}

func (ops *serviceIdentityMigrationOps) mergeRootOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.unitPath != nil {
		ops.unitPath = overrides.unitPath
	}
	if overrides.snapshot != nil {
		ops.snapshot = overrides.snapshot
	}
	if overrides.materialize != nil {
		ops.materialize = overrides.materialize
	}
	if overrides.discardRoot != nil {
		ops.discardRoot = overrides.discardRoot
	}
}

func (ops *serviceIdentityMigrationOps) mergeInspectionOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.inspect != nil {
		ops.inspect = overrides.inspect
	}
	if overrides.inspectSource != nil {
		ops.inspectSource = overrides.inspectSource
	}
	if overrides.apply != nil {
		ops.apply = overrides.apply
	}
	if overrides.restore != nil {
		ops.restore = overrides.restore
	}
	if overrides.reload != nil {
		ops.reload = overrides.reload
	}
}

func (ops *serviceIdentityMigrationOps) mergeCommitOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.writeUnit != nil {
		ops.writeUnit = overrides.writeUnit
	}
	if overrides.verify != nil {
		ops.verify = overrides.verify
	}
	if overrides.commit != nil {
		ops.commit = overrides.commit
	}
	if overrides.remove != nil {
		ops.remove = overrides.remove
	}
}

func (ops *serviceIdentityMigrationOps) mergeEnablementOverrides(overrides serviceIdentityMigrationOps) {
	if overrides.isEnabled != nil {
		ops.isEnabled = overrides.isEnabled
	}
	if overrides.enable != nil {
		ops.enable = overrides.enable
	}
	if overrides.disable != nil {
		ops.disable = overrides.disable
	}
	if overrides.newGenerationStager != nil {
		ops.newGenerationStager = overrides.newGenerationStager
	}
}

func (m *serviceIdentityMigration) materializeRoot(ctx context.Context, plan serviceRootMigrationPlan, w io.Writer) (bool, error) {
	_ = w
	copyGuard, err := m.serviceIdentityRootCopyGuard(ctx, plan)
	if err != nil {
		return false, err
	}
	if err := validateServiceIdentityTargetRootAbsent(plan); err != nil {
		return false, err
	}
	if plan.CreateNewRootZFS {
		return m.materializeServiceIdentityZFSTarget(ctx, plan, copyGuard)
	}
	return false, m.materializeServiceIdentityFilesystemTarget(ctx, plan, copyGuard)
}

func (m *serviceIdentityMigration) serviceIdentityRootCopyGuard(ctx context.Context, plan serviceRootMigrationPlan) (serviceIdentityCopyGuard, error) {
	if plan.Mode != serviceRootMigrationCopy {
		return serviceIdentityCopyGuard{}, nil
	}
	return newServiceIdentityCopyGuard(ctx, plan.OldRoot, plan.OldRootZFS, m.server.zfsRunner)
}

func validateServiceIdentityTargetRootAbsent(plan serviceRootMigrationPlan) error {
	if plan.NewRootExisted {
		return fmt.Errorf("identity migration requires a newly-created target root")
	}
	if _, err := os.Lstat(plan.NewRoot); err == nil {
		return fmt.Errorf("target root existence changed after planning (was false, now true)")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect target root before materialization: %w", err)
	}
	return nil
}

func (m *serviceIdentityMigration) materializeServiceIdentityZFSTarget(ctx context.Context, plan serviceRootMigrationPlan, guard serviceIdentityCopyGuard) (bool, error) {
	runner := serviceIdentityZFSRunner(m.server.zfsRunner)
	created, err := m.createServiceIdentityZFSTarget(ctx, runner, plan)
	if err != nil {
		return created, err
	}
	if err := validateServiceIdentityZFSMountpoint(ctx, runner, plan); err != nil {
		return true, err
	}
	if err := m.recordMaterializationCreation(ctx, plan.NewRoot, "", true); err != nil {
		return true, err
	}
	if err := m.populateMountedServiceIdentityRoot(plan, guard); err != nil {
		return true, err
	}
	return true, syncServiceIdentityTree(plan.NewRoot)
}

func serviceIdentityZFSRunner(runner zfsCommandRunner) zfsCommandRunner {
	if runner == nil {
		return runZFSCommand
	}
	return runner
}

func validateServiceIdentityZFSMountpoint(ctx context.Context, runner zfsCommandRunner, plan serviceRootMigrationPlan) error {
	mountpoint, err := zfsDatasetMountpoint(ctx, runner, plan.NewRootZFS)
	if err != nil {
		return err
	}
	if filepath.Clean(mountpoint) != filepath.Clean(plan.NewRoot) {
		return fmt.Errorf("new ZFS dataset %q mounted at %s, planned %s", plan.NewRootZFS, mountpoint, plan.NewRoot)
	}
	return nil
}

func (m *serviceIdentityMigration) createServiceIdentityZFSTarget(ctx context.Context, runner zfsCommandRunner, plan serviceRootMigrationPlan) (bool, error) {
	createErr := createServiceIdentityZFSDataset(ctx, runner, plan.NewRootZFS, m.migrationID)
	if createErr == nil {
		return true, nil
	}
	return m.resolveAmbiguousServiceIdentityZFSCreate(ctx, runner, plan, createErr)
}

func (m *serviceIdentityMigration) resolveAmbiguousServiceIdentityZFSCreate(ctx context.Context, runner zfsCommandRunner, plan serviceRootMigrationPlan, createErr error) (bool, error) {
	exists, probeErr := zfsDatasetExists(ctx, runner, plan.NewRootZFS)
	if probeErr != nil {
		return true, errors.Join(createErr, fmt.Errorf("probe ambiguous ZFS create outcome: %w", probeErr))
	}
	if !exists {
		return false, createErr
	}
	marker, markerErr := serviceIdentityZFSDatasetMarker(ctx, runner, plan.NewRootZFS)
	if markerErr != nil {
		return true, errors.Join(createErr, fmt.Errorf("verify ambiguous ZFS create marker: %w", markerErr))
	}
	if marker != m.migrationID {
		return true, errors.Join(createErr, fmt.Errorf("ambiguous ZFS create produced marker %q, want %q; dataset was left for recovery", marker, m.migrationID))
	}
	return true, nil
}

func (m *serviceIdentityMigration) populateMountedServiceIdentityRoot(plan serviceRootMigrationPlan, guard serviceIdentityCopyGuard) error {
	switch plan.Mode {
	case serviceRootMigrationCopy:
		return m.populateCopiedMountedServiceIdentityRoot(plan, guard)
	case serviceRootMigrationEmpty:
		return createEmptyMountedServiceRoot(plan.NewRoot)
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
}

func (m *serviceIdentityMigration) populateCopiedMountedServiceIdentityRoot(plan serviceRootMigrationPlan, guard serviceIdentityCopyGuard) error {
	if err := guard.copyIntoMountedRoot(plan.NewRoot); err != nil {
		return err
	}
	if err := rewriteCopiedServiceRootArtifacts(m.target.Artifacts, plan.OldRoot, plan.NewRoot); err != nil {
		return err
	}
	if m.prepareGeneration != nil {
		return m.prepareGeneration(plan.NewRoot)
	}
	return nil
}

func (m *serviceIdentityMigration) materializeServiceIdentityFilesystemTarget(ctx context.Context, plan serviceRootMigrationPlan, guard serviceIdentityCopyGuard) error {
	stage := filepath.Join(filepath.Dir(plan.NewRoot), ".yeet-service-root-"+m.migrationID)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return fmt.Errorf("create identity migration root stage %s: %w", stage, err)
	}
	if err := syncServiceIdentityJournalDirectory(filepath.Dir(stage)); err != nil {
		return err
	}
	if err := m.recordMaterializationCreation(ctx, stage, stage, false); err != nil {
		return err
	}
	if err := m.populateStagedServiceIdentityRoot(plan, guard, stage); err != nil {
		return err
	}
	if err := syncServiceIdentityTree(stage); err != nil {
		return err
	}
	if err := m.recordMaterializationPublish(stage); err != nil {
		return err
	}
	if err := renameServiceIdentityRootNoReplace(stage, plan.NewRoot); err != nil {
		return fmt.Errorf("publish identity migration root stage: %w", err)
	}
	return syncServiceIdentityJournalDirectory(filepath.Dir(plan.NewRoot))
}

func (m *serviceIdentityMigration) populateStagedServiceIdentityRoot(plan serviceRootMigrationPlan, guard serviceIdentityCopyGuard, stage string) error {
	switch plan.Mode {
	case serviceRootMigrationCopy:
		if err := guard.copyToStage(stage); err != nil {
			return err
		}
		if err := ensureDirsForRoot(stage, ""); err != nil {
			return err
		}
		stagedArtifacts, err := serviceIdentityArtifactsAtRoot(m.target.Artifacts, plan.NewRoot, stage)
		if err != nil {
			return err
		}
		if err := rewriteCopiedServiceRootArtifacts(stagedArtifacts, plan.OldRoot, plan.NewRoot); err != nil {
			return err
		}
		if m.prepareGeneration != nil {
			return m.prepareGeneration(stage)
		}
		return nil
	case serviceRootMigrationEmpty:
		return ensureDirsForRoot(stage, "")
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
}

func serviceIdentityArtifactsAtRoot(artifacts db.ArtifactStore, fromRoot, toRoot string) (db.ArtifactStore, error) {
	service := (&db.Service{Artifacts: artifacts}).Clone()
	for name, artifact := range service.Artifacts {
		if artifact == nil {
			continue
		}
		for ref, path := range artifact.Refs {
			relocated, ok, err := relocatePathUnderRoot(path, fromRoot, toRoot)
			if err != nil {
				return nil, fmt.Errorf("relocate %s artifact %s for unpublished stage: %w", name, path, err)
			}
			if ok {
				artifact.Refs[ref] = relocated
			}
		}
	}
	return service.Artifacts, nil
}

func (m *serviceIdentityMigration) recordMaterializationPublish(stage string) error {
	digest, count, meta, err := serviceIdentityTargetInventory(stage)
	if err != nil {
		return err
	}
	record := serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseMaterializePublish, RootCreated: true,
		RootDev: meta.Dev, RootIno: meta.Ino, InventoryDigest: digest, InventoryCount: count,
		StagePath: stage,
	}
	if err := m.appendPhase(serviceIdentityPhaseMaterializePublish, record); err != nil {
		return err
	}
	m.materializationPublish = record
	return nil
}

func (m *serviceIdentityMigration) recordMaterializationCreation(ctx context.Context, root, stage string, datasetCreated bool) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("created identity migration root %s is not a real directory", root)
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		return err
	}
	record := serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseMaterializeCreated, DatasetCreated: datasetCreated,
		RootCreated: true, RootDev: meta.Dev, RootIno: meta.Ino, StagePath: stage,
	}
	if datasetCreated {
		runner := m.server.zfsRunner
		if runner == nil {
			runner = runZFSCommand
		}
		record.DatasetGUID, err = zfsDatasetGUID(ctx, runner, m.req.RootPlan.NewRootZFS)
		if err != nil {
			return err
		}
	}
	if err := m.appendPhase(serviceIdentityPhaseMaterializeCreated, record); err != nil {
		return err
	}
	m.materializationCreation = record
	return nil
}

func syncServiceIdentityTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		return syncServiceIdentityTreePath(path, walkErr, &directories)
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncServiceIdentityJournalDirectory(directories[index]); err != nil {
			return err
		}
	}
	return syncServiceIdentityJournalDirectory(filepath.Dir(root))
}

func syncServiceIdentityTreePath(path string, walkErr error, directories *[]string) error {
	if walkErr != nil {
		return walkErr
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		*directories = append(*directories, path)
		return nil
	}
	if info.Mode().IsRegular() {
		return syncServiceIdentityRegularFile(path)
	}
	return nil
}

func syncServiceIdentityRegularFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func (m *serviceIdentityMigration) captureMaterialization(ctx context.Context, datasetCreated bool) (serviceIdentityPhaseRecord, error) {
	if m.req.RootPlan == nil {
		return serviceIdentityPhaseRecord{}, fmt.Errorf("root plan is unavailable")
	}
	digest, count, meta, err := serviceIdentityTargetInventory(m.req.RootPlan.NewRoot)
	if err != nil {
		return serviceIdentityPhaseRecord{}, err
	}
	record := serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseMaterialize, DatasetCreated: datasetCreated,
		RootCreated: !m.req.RootPlan.NewRootExisted, RootDev: meta.Dev, RootIno: meta.Ino,
		InventoryDigest: digest, InventoryCount: count,
	}
	if datasetCreated {
		runner := m.server.zfsRunner
		if runner == nil {
			runner = runZFSCommand
		}
		record.DatasetGUID, err = zfsDatasetGUID(ctx, runner, m.req.RootPlan.NewRootZFS)
		if err != nil {
			return serviceIdentityPhaseRecord{}, err
		}
	}
	return record, nil
}

func serviceIdentityTargetInventory(root string) (string, int, serviceIdentityInodeMetadata, error) {
	builder := serviceIdentityInventoryBuilder{root: root, hash: sha256.New()}
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		return builder.add(path, walkErr)
	})
	if err != nil {
		return "", 0, serviceIdentityInodeMetadata{}, fmt.Errorf("inventory target service root %s: %w", root, err)
	}
	return hex.EncodeToString(builder.hash.Sum(nil)), builder.count, builder.rootMeta, nil
}

type serviceIdentityInventoryBuilder struct {
	root     string
	hash     hash.Hash
	count    int
	rootMeta serviceIdentityInodeMetadata
}

func (b *serviceIdentityInventoryBuilder) add(path string, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(b.root, path)
	if err != nil {
		return err
	}
	if rel == "." {
		b.rootMeta = meta
	}
	b.writeMetadata(rel, info, meta)
	if err := b.writeContent(path, info); err != nil {
		return err
	}
	_, _ = b.hash.Write([]byte{0})
	b.count++
	return nil
}

func (b *serviceIdentityInventoryBuilder) writeMetadata(rel string, info os.FileInfo, meta serviceIdentityInodeMetadata) {
	_, _ = b.hash.Write([]byte(rel))
	_, _ = b.hash.Write([]byte{0})
	var raw [40]byte
	binary.BigEndian.PutUint64(raw[0:8], meta.Dev)
	binary.BigEndian.PutUint64(raw[8:16], meta.Nlink)
	binary.BigEndian.PutUint32(raw[16:20], uint32(info.Mode()))
	binary.BigEndian.PutUint32(raw[20:24], meta.UID)
	binary.BigEndian.PutUint32(raw[24:28], meta.GID)
	if info.Mode().IsRegular() {
		binary.BigEndian.PutUint64(raw[28:36], uint64(info.Size()))
	}
	_, _ = b.hash.Write(raw[:])
}

func (b *serviceIdentityInventoryBuilder) writeContent(path string, info os.FileInfo) error {
	if info.Mode().IsRegular() {
		proof, err := captureServiceIdentityPathProof(path)
		if err != nil {
			return err
		}
		_, _ = b.hash.Write([]byte(proof.SHA256))
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		_, _ = b.hash.Write([]byte(target))
	}
	return nil
}

func zfsDatasetGUID(ctx context.Context, runner zfsCommandRunner, dataset string) (string, error) {
	stdout, stderr, err := runner(ctx, "get", "-H", "-o", "value", "guid", dataset)
	if err != nil {
		return "", formatZFSCommandError("zfs get guid "+dataset, stderr, err)
	}
	guid := strings.TrimSpace(stdout)
	if guid == "" || guid == "-" {
		return "", fmt.Errorf("zfs get guid %s returned no GUID", dataset)
	}
	return guid, nil
}

const serviceIdentityZFSMarkerProperty = "org.yeetrun:identity-migration"

func createServiceIdentityZFSDataset(ctx context.Context, runner zfsCommandRunner, dataset, migrationID string) error {
	if runner == nil {
		runner = runZFSCommand
	}
	property := serviceIdentityZFSMarkerProperty + "=" + migrationID
	_, stderr, err := runner(ctx, "create", "-o", property, dataset)
	if err != nil {
		return formatZFSCommandError("zfs create -o "+property+" "+dataset, stderr, err)
	}
	return nil
}

func serviceIdentityZFSDatasetMarker(ctx context.Context, runner zfsCommandRunner, dataset string) (string, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	stdout, stderr, err := runner(ctx, "get", "-H", "-o", "value", serviceIdentityZFSMarkerProperty, dataset)
	if err != nil {
		return "", formatZFSCommandError("zfs get "+serviceIdentityZFSMarkerProperty+" "+dataset, stderr, err)
	}
	return strings.TrimSpace(stdout), nil
}

func clearServiceIdentityZFSDatasetMarker(ctx context.Context, runner zfsCommandRunner, dataset, expectedGUID, migrationID string) error {
	if runner == nil {
		runner = runZFSCommand
	}
	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := validateServiceIdentityZFSDatasetGUID(ctx, runner, dataset, expectedGUID); err != nil {
		return err
	}
	marker, err := serviceIdentityZFSDatasetMarker(ctx, runner, dataset)
	if err != nil {
		return err
	}
	if err := validateServiceIdentityZFSDatasetMarker(marker, migrationID); err != nil || marker == "" || marker == "-" {
		return err
	}
	_, stderr, err := runner(ctx, "inherit", serviceIdentityZFSMarkerProperty, dataset)
	if err != nil {
		return formatZFSCommandError("zfs inherit "+serviceIdentityZFSMarkerProperty+" "+dataset, stderr, err)
	}
	return nil
}

func validateServiceIdentityZFSDatasetGUID(ctx context.Context, runner zfsCommandRunner, dataset, expectedGUID string) error {
	guid, err := zfsDatasetGUID(ctx, runner, dataset)
	if err != nil {
		return err
	}
	if expectedGUID == "" || guid != expectedGUID {
		return fmt.Errorf("target ZFS dataset GUID is %q, want %q; transaction marker was left untouched", guid, expectedGUID)
	}
	return nil
}

func validateServiceIdentityZFSDatasetMarker(marker, migrationID string) error {
	if marker == "" || marker == "-" {
		return nil
	}
	if marker != migrationID {
		return fmt.Errorf("target ZFS dataset transaction marker is %q, want %q; marker was left untouched", marker, migrationID)
	}
	return nil
}

func (m *serviceIdentityMigration) discardRoot(ctx context.Context, plan serviceRootMigrationPlan, datasetCreated bool) error {
	if plan.NewRootExisted {
		return fmt.Errorf("identity migration rollback refuses to alter a pre-existing target root")
	}
	if datasetCreated {
		exists, err := zfsDatasetExists(ctx, m.server.zfsRunner, plan.NewRootZFS)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
	}
	proof, creation := m.serviceIdentityDiscardProofs()
	if datasetCreated {
		return m.discardServiceIdentityZFSTarget(ctx, plan, creation)
	}
	if creation.RootDev == 0 || creation.RootIno == 0 {
		if proof.RootDev == 0 || proof.RootIno == 0 {
			return discardUnrecordedServiceIdentityStage(plan, m.migrationID)
		}
		creation = proof
	}
	return m.discardServiceIdentityFilesystemTarget(plan, proof, creation)
}

func (m *serviceIdentityMigration) serviceIdentityDiscardProofs() (serviceIdentityPhaseRecord, serviceIdentityPhaseRecord) {
	proof := m.materialization
	if proof.InventoryDigest == "" && m.materializationPublish.InventoryDigest != "" {
		proof = m.materializationPublish
	}
	creation := m.materializationCreation
	if (creation.DatasetGUID == "" || creation.RootDev == 0 || creation.RootIno == 0) &&
		proof.DatasetGUID != "" && proof.RootDev != 0 && proof.RootIno != 0 {
		creation = proof
	}
	return proof, creation
}

func (m *serviceIdentityMigration) discardServiceIdentityZFSTarget(ctx context.Context, plan serviceRootMigrationPlan, creation serviceIdentityPhaseRecord) error {
	if creation.DatasetGUID == "" || creation.RootDev == 0 || creation.RootIno == 0 {
		marker, err := serviceIdentityZFSDatasetMarker(ctx, m.server.zfsRunner, plan.NewRootZFS)
		if err != nil {
			return err
		}
		if marker != m.migrationID {
			return fmt.Errorf("target ZFS dataset transaction marker is %q, want %q; dataset was left untouched", marker, m.migrationID)
		}
		runner := m.server.zfsRunner
		if runner == nil {
			runner = runZFSCommand
		}
		return zfsDestroyDataset(ctx, runner, plan.NewRootZFS)
	}
	runner := m.server.zfsRunner
	if runner == nil {
		runner = runZFSCommand
	}
	guid, err := zfsDatasetGUID(ctx, runner, plan.NewRootZFS)
	if err != nil {
		return err
	}
	if guid != creation.DatasetGUID {
		return fmt.Errorf("target ZFS dataset GUID changed after materialization; dataset was left untouched")
	}
	return zfsDestroyDataset(ctx, runner, plan.NewRootZFS)
}

type serviceIdentityDiscardTarget struct {
	path          string
	stage         string
	targetPresent bool
}

func (m *serviceIdentityMigration) discardServiceIdentityFilesystemTarget(plan serviceRootMigrationPlan, proof, creation serviceIdentityPhaseRecord) error {
	target, err := m.locateServiceIdentityDiscardTarget(plan, creation)
	if err != nil || target.path == "" {
		return err
	}
	if err := validateServiceIdentityDiscardInode(target.path, creation); err != nil {
		return err
	}
	if target.targetPresent {
		if err := validateServiceIdentityDiscardInventory(target.path, proof); err != nil {
			return err
		}
		if err := quarantineServiceIdentityDiscardTarget(plan.NewRoot, target.stage); err != nil {
			return err
		}
		target.path = target.stage
	}
	return removeServiceIdentityDiscardTarget(target.path)
}

func (m *serviceIdentityMigration) locateServiceIdentityDiscardTarget(plan serviceRootMigrationPlan, creation serviceIdentityPhaseRecord) (serviceIdentityDiscardTarget, error) {
	stage := creation.StagePath
	if stage == "" && m.migrationID != "" {
		stage = filepath.Join(filepath.Dir(plan.NewRoot), ".yeet-service-root-"+m.migrationID)
	}
	targetPresent, stagePresent, err := serviceIdentityDiscardPathPresence(plan.NewRoot, stage)
	if err != nil {
		return serviceIdentityDiscardTarget{}, err
	}
	if !targetPresent && !stagePresent {
		return serviceIdentityDiscardTarget{}, nil
	}
	if targetPresent && stagePresent {
		return serviceIdentityDiscardTarget{}, fmt.Errorf("both target root and transaction stage exist; neither was removed")
	}
	path := plan.NewRoot
	if stagePresent {
		path = stage
	}
	return serviceIdentityDiscardTarget{path: path, stage: stage, targetPresent: targetPresent}, nil
}

func serviceIdentityDiscardPathPresence(target, stage string) (bool, bool, error) {
	targetPresent, err := serviceIdentityPathPresent(target)
	if err != nil {
		return false, false, err
	}
	if stage == "" {
		return targetPresent, false, nil
	}
	stagePresent, err := serviceIdentityPathPresent(stage)
	return targetPresent, stagePresent, err
}

func serviceIdentityPathPresent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func validateServiceIdentityDiscardInode(path string, creation serviceIdentityPhaseRecord) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || meta.Dev != creation.RootDev || meta.Ino != creation.RootIno {
		return fmt.Errorf("target root inode changed after transaction creation; target was left untouched")
	}
	return nil
}

func validateServiceIdentityDiscardInventory(path string, proof serviceIdentityPhaseRecord) error {
	if proof.InventoryDigest == "" {
		return fmt.Errorf("target root cleanup lacks exact inventory provenance; target was left untouched")
	}
	digest, count, currentMeta, err := serviceIdentityTargetInventory(path)
	if err != nil || currentMeta.Dev != proof.RootDev || currentMeta.Ino != proof.RootIno || digest != proof.InventoryDigest || count != proof.InventoryCount {
		return fmt.Errorf("target root changed after materialization; target was left untouched")
	}
	return nil
}

func quarantineServiceIdentityDiscardTarget(target, stage string) error {
	if stage == "" {
		return fmt.Errorf("target root cleanup lacks a transaction quarantine path; target was left untouched")
	}
	if err := renameServiceIdentityRootNoReplace(target, stage); err != nil {
		return fmt.Errorf("quarantine target root before cleanup: %w", err)
	}
	return syncServiceIdentityJournalDirectory(filepath.Dir(target))
}

func removeServiceIdentityDiscardTarget(path string) error {
	if err := os.Chown(path, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	if err := syncServiceIdentityJournalDirectory(path); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return syncServiceIdentityJournalDirectory(filepath.Dir(path))
}

func discardUnrecordedServiceIdentityStage(plan serviceRootMigrationPlan, migrationID string) error {
	if plan.CreateNewRootZFS {
		return fmt.Errorf("target ZFS cleanup lacks durable dataset identity; target was left untouched")
	}
	if err := validateUnrecordedServiceIdentityTargetAbsent(plan.NewRoot); err != nil {
		return err
	}
	stage := filepath.Join(filepath.Dir(plan.NewRoot), ".yeet-service-root-"+migrationID)
	present, err := validateUnrecordedServiceIdentityStage(stage)
	if err != nil || !present {
		return err
	}
	if err := os.Remove(stage); err != nil {
		return err
	}
	return syncServiceIdentityJournalDirectory(filepath.Dir(stage))
}

func validateUnrecordedServiceIdentityTargetAbsent(target string) error {
	_, err := os.Lstat(target)
	if err == nil {
		return fmt.Errorf("target root exists without durable creation proof; target was left untouched")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validateUnrecordedServiceIdentityStage(stage string) (bool, error) {
	info, err := os.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		return false, err
	}
	entries, readErr := os.ReadDir(stage)
	if readErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 || meta.UID != uint32(os.Geteuid()) || len(entries) != 0 {
		return false, errors.Join(fmt.Errorf("unproven identity migration stage %s was left untouched", stage), readErr)
	}
	return true, nil
}

func (m *serviceIdentityMigration) verify(ctx context.Context, check serviceIdentityMigrationVerification) error {
	if err := verifyServiceIdentityUnitMetadata(check.UnitPath); err != nil {
		return err
	}
	if err := verifyServiceIdentityUnitDirectives(check); err != nil {
		return err
	}
	if err := verifyEffectiveServiceIdentity(ctx, check.Service, check.Identity, check.Root, check.ExpectProcess); err != nil {
		return err
	}
	running, err := m.ops.isTargetRunning(ctx, check.Service)
	if err != nil {
		return err
	}
	if running != check.WasRunning {
		return fmt.Errorf("service running state is %t, want %t", running, check.WasRunning)
	}
	return validateNativeServiceLayout(check.Root, check.Identity)
}

func verifyServiceIdentityUnitMetadata(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o644 ||
		meta.UID != uint32(os.Geteuid()) || meta.GID != uint32(os.Getegid()) || meta.Nlink != 1 {
		return fmt.Errorf("installed unit metadata is mode %s owner %d:%d links %d, want regular 0644 owner %d:%d links 1",
			info.Mode(), meta.UID, meta.GID, meta.Nlink, os.Geteuid(), os.Getegid())
	}
	return nil
}

func verifyServiceIdentityUnitDirectives(check serviceIdentityMigrationVerification) error {
	raw, err := os.ReadFile(check.UnitPath)
	if err != nil {
		return err
	}
	directives := serviceIdentityUnitDirectives(string(raw))
	if directives["User"] != check.Identity.RequestedUser || directives["Group"] != check.Identity.RequestedGroup {
		return fmt.Errorf("installed unit identity is %s:%s, want %s:%s", directives["User"], directives["Group"], check.Identity.RequestedUser, check.Identity.RequestedGroup)
	}
	if filepath.Clean(directives["WorkingDirectory"]) != filepath.Clean(serviceDataDirForRoot(check.Root)) {
		return fmt.Errorf("installed unit working directory is %q, want %q", directives["WorkingDirectory"], serviceDataDirForRoot(check.Root))
	}
	return nil
}

func serviceIdentityUsesTimer(service *db.Service) bool {
	if service == nil {
		return false
	}
	_, ok := service.Artifacts.Gen(db.ArtifactSystemdTimerFile, service.Generation)
	return ok
}

func (m *serviceIdentityMigration) commit(previous, target *db.Service) error {
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		current, ok := data.Services[m.req.Service]
		if !ok {
			return fmt.Errorf("service %q disappeared during migration", m.req.Service)
		}
		if !reflect.DeepEqual(current, previous) {
			return fmt.Errorf("service %q changed during migration", m.req.Service)
		}
		data.Services[m.req.Service] = target.Clone()
		return nil
	})
	return err
}

func readOptionalServiceIdentityUnit(path string) ([]byte, bool, os.FileMode, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("read installed primary unit %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, 0, fmt.Errorf("inspect installed primary unit %s: %w", path, err)
	}
	return raw, true, info.Mode(), nil
}

func writeServiceIdentityUnitAtomically(path string, raw []byte, mode os.FileMode) error {
	expected, err := captureServiceIdentityPathProof(path)
	if err != nil {
		return err
	}
	return writeServiceIdentityUnitAtomicallyExpected(
		path, raw, mode, expected, uint32(os.Geteuid()), uint32(os.Getegid()),
	)
}

func writeServiceIdentityUnitAtomicallyExpected(path string, raw []byte, mode os.FileMode, expected serviceIdentityPathProof, uid, gid uint32) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create unit directory %s: %w", dir, err)
	}
	tmpPath, err := writeTemporaryServiceIdentityUnit(dir, filepath.Base(path), raw, mode, uid, gid)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpPath) }()
	return publishServiceIdentityUnitExpected(path, tmpPath, expected)
}

func writeTemporaryServiceIdentityUnit(dir, name string, raw []byte, mode os.FileMode, uid, gid uint32) (string, error) {
	tmp, err := os.CreateTemp(dir, name+".identity.")
	if err != nil {
		return "", fmt.Errorf("create temporary unit for %s: %w", filepath.Join(dir, name), err)
	}
	tmpPath := tmp.Name()
	if _, err = tmp.Write(raw); err != nil {
		return cleanupTemporaryServiceIdentityUnit(tmp, tmpPath, fmt.Errorf("write temporary unit for %s: %w", filepath.Join(dir, name), err))
	}
	if err = tmp.Chown(int(uid), int(gid)); err != nil {
		return cleanupTemporaryServiceIdentityUnit(tmp, tmpPath, fmt.Errorf("chown temporary unit for %s: %w", filepath.Join(dir, name), err))
	}
	if err = tmp.Chmod(mode); err != nil {
		return cleanupTemporaryServiceIdentityUnit(tmp, tmpPath, fmt.Errorf("chmod temporary unit for %s: %w", filepath.Join(dir, name), err))
	}
	if err = tmp.Sync(); err != nil {
		return cleanupTemporaryServiceIdentityUnit(tmp, tmpPath, fmt.Errorf("sync temporary unit for %s: %w", filepath.Join(dir, name), err))
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close temporary unit for %s: %w", filepath.Join(dir, name), err)
	}
	return tmpPath, nil
}

func cleanupTemporaryServiceIdentityUnit(tmp *os.File, path string, cause error) (string, error) {
	_ = tmp.Close()
	_ = os.Remove(path)
	return "", cause
}

func publishServiceIdentityUnitExpected(path, tmpPath string, expected serviceIdentityPathProof) error {
	dir := filepath.Dir(path)
	parentFD, name, closeParent, openErr := openServiceIdentityMutationParent(dir, filepath.Base(path))
	if openErr != nil {
		return openErr
	}
	defer closeParent()
	actual, captureErr := captureServiceIdentityPathProofFromParent(parentFD, name, path)
	if captureErr != nil {
		return captureErr
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("unit %s changed from its durable provenance", path)
	}
	if err := unix.Renameat(unix.AT_FDCWD, tmpPath, parentFD, name); err != nil {
		return fmt.Errorf("replace unit %s: %w", path, err)
	}
	if err := unix.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync unit directory %s: %w", dir, err)
	}
	return nil
}

func rewriteServiceIdentityUnit(raw string, identity db.ServiceIdentity, root string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("installed primary unit is empty")
	}
	rewriter := serviceIdentityUnitRewriter{
		updates: map[string]string{
			"User": identity.RequestedUser, "Group": identity.RequestedGroup,
			"WorkingDirectory": serviceDataDirForRoot(root),
		},
		seen:        map[string]bool{},
		environment: nativeSystemdIdentityEnvironment(identity.RequestedUser, serviceDataDirForRoot(root)),
	}
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	rewriter.out = make([]string, 0, len(lines)+4)
	for _, line := range lines {
		rewriter.appendLine(line)
	}
	if !rewriter.seenService {
		return "", fmt.Errorf("installed primary unit has no [Service] section")
	}
	if rewriter.inService {
		rewriter.flush()
	}
	return strings.Join(rewriter.out, "\n") + "\n", nil
}

type serviceIdentityUnitRewriter struct {
	updates     map[string]string
	seen        map[string]bool
	out         []string
	environment string
	inService   bool
	seenService bool
}

func (r *serviceIdentityUnitRewriter) appendLine(line string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		r.appendSection(line, trimmed)
		return
	}
	if r.inService && (r.appendUpdatedDirective(trimmed) || isNativeSystemdIdentityEnvironment(trimmed)) {
		return
	}
	r.out = append(r.out, line)
}

func (r *serviceIdentityUnitRewriter) appendSection(line, section string) {
	if r.inService {
		r.flush()
	}
	r.inService = section == "[Service]"
	r.seenService = r.seenService || r.inService
	r.out = append(r.out, line)
}

func (r *serviceIdentityUnitRewriter) appendUpdatedDirective(line string) bool {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return false
	}
	value, replace := r.updates[key]
	if !replace {
		return false
	}
	if !r.seen[key] {
		r.out = append(r.out, key+"="+value)
		r.seen[key] = true
	}
	return true
}

func (r *serviceIdentityUnitRewriter) flush() {
	for _, key := range []string{"User", "Group", "WorkingDirectory"} {
		if !r.seen[key] {
			r.out = append(r.out, key+"="+r.updates[key])
			r.seen[key] = true
		}
	}
	r.out = append(r.out, r.environment)
}

func nativeSystemdIdentityEnvironment(user, workingDirectory string) string {
	return "Environment=HOME=" + workingDirectory + " USER=" + user + " LOGNAME=" + user + " SHELL=/bin/sh"
}

func isNativeSystemdIdentityEnvironment(line string) bool {
	return strings.HasPrefix(line, "Environment=HOME=") && strings.Contains(line, " USER=") &&
		strings.Contains(line, " LOGNAME=") && strings.HasSuffix(line, " SHELL=/bin/sh")
}

func serviceIdentityUnitDirectives(raw string) map[string]string {
	out := map[string]string{}
	inService := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inService = line == "[Service]"
			continue
		}
		if !inService || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			out[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return out
}

func newServiceIdentityMigrationID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create service identity migration id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func cloneServiceRootMigrationPlan(plan *serviceRootMigrationPlan) *serviceRootMigrationPlan {
	if plan == nil {
		return nil
	}
	clone := *plan
	clone.NewRootState = append([]serviceRootTargetPathState(nil), plan.NewRootState...)
	return &clone
}

func serviceIdentityTypeError(service string, serviceType db.ServiceType) error {
	switch serviceType {
	case db.ServiceTypeVM:
		return fmt.Errorf("--run-as does not control VM guest or Firecracker jailer identities; use VM guest settings because Firecracker host execution is managed separately")
	case db.ServiceTypeDockerCompose:
		return fmt.Errorf("--run-as applies only to native systemd workloads; configure the container image or Compose service \"user:\" field instead")
	default:
		return fmt.Errorf("service %q has unsupported type %q for --run-as", service, serviceType)
	}
}

func formatServiceIdentity(identity db.ServiceIdentity) string {
	return fmt.Sprintf("%s:%s (%d:%d)", identity.RequestedUser, identity.RequestedGroup, identity.UID, identity.GID)
}

func phaseFromMigrationError(err error) string {
	if err == nil {
		return "unknown"
	}
	text := err.Error()
	if phase, _, ok := strings.Cut(text, ":"); ok {
		return phase
	}
	return "unknown"
}

func captureLegacyNativeRuntimeBackups(rootDir, root, service, migrationID string) ([]serviceIdentityGenerationBackup, error) {
	runDir := serviceRunDirForRoot(root)
	runFD, _, closeRun, err := openServiceIdentityMutationParent(root, filepath.Join("run", ".entry"))
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closeRun()
	entries, err := readStableServiceIdentityRuntimeDirectory(runFD, runDir)
	if err != nil {
		return nil, err
	}
	var backups []serviceIdentityGenerationBackup
	backupDir := filepath.Join(serviceIdentityMigrationBackupDir(rootDir, migrationID), "runtime")
	for _, entry := range entries {
		backup, include, err := captureLegacyNativeRuntimeBackup(runFD, runDir, backupDir, service, entry.Name(), len(backups))
		if err != nil {
			return nil, err
		}
		if include {
			backups = append(backups, backup)
		}
	}
	return backups, nil
}

func readStableServiceIdentityRuntimeDirectory(runFD int, runDir string) ([]os.DirEntry, error) {
	readFD, err := unix.Dup(runFD)
	if err != nil {
		return nil, fmt.Errorf("duplicate stable runtime directory descriptor: %w", err)
	}
	runFile := os.NewFile(uintptr(readFD), runDir)
	if runFile == nil {
		_ = unix.Close(readFD)
		return nil, fmt.Errorf("wrap stable runtime directory descriptor")
	}
	entries, err := runFile.ReadDir(-1)
	closeErr := runFile.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return entries, nil
}

func captureLegacyNativeRuntimeBackup(runFD int, runDir, backupDir, service, name string, index int) (serviceIdentityGenerationBackup, bool, error) {
	if name == CatchService || !legacyNativeRuntimeArtifactName(name, service) {
		return serviceIdentityGenerationBackup{}, false, nil
	}
	path := filepath.Join(runDir, name)
	proof, err := captureServiceIdentityTransactionProofFromParent(runFD, name, path)
	if err != nil {
		return serviceIdentityGenerationBackup{}, false, fmt.Errorf("inspect legacy runtime artifact %s: %w", path, err)
	}
	if proof.Nlink != 1 {
		return serviceIdentityGenerationBackup{}, false, fmt.Errorf("legacy runtime artifact %s has %d hard links; exact rollback requires one", path, proof.Nlink)
	}
	return serviceIdentityGenerationBackup{
		Path: path, BackupPath: filepath.Join(backupDir, fmt.Sprintf("%03d", index)),
		Present: true, Original: proof,
	}, true, nil
}

func legacyNativeRuntimeArtifactName(name, service string) bool {
	return name == service || name == "env" || name == "netns.env" || name == "tailscaled" ||
		name == "tailscaled.env" || name == "tailscaled.json"
}

func backupLegacyNativeRuntimeArtifacts(rootDir, root string, backups []serviceIdentityGenerationBackup) ([]serviceIdentityGenerationBackup, error) {
	if len(backups) == 0 {
		return nil, nil
	}
	if err := prepareLegacyNativeRuntimeBackupDirectory(filepath.Dir(backups[0].BackupPath)); err != nil {
		return nil, err
	}
	out := append([]serviceIdentityGenerationBackup(nil), backups...)
	for index := range out {
		if err := backupLegacyNativeRuntimeArtifact(rootDir, root, &out[index]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func prepareLegacyNativeRuntimeBackupDirectory(backupDir string) error {
	transactionDir := filepath.Dir(backupDir)
	if _, err := ensureRootOnlyDirectory(filepath.Dir(transactionDir)); err != nil {
		return err
	}
	if _, err := ensureRootOnlyDirectory(transactionDir); err != nil {
		return err
	}
	created, err := ensureRootOnlyDirectory(backupDir)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("legacy runtime backup directory already exists: %s", backupDir)
	}
	return syncServiceIdentityJournalDirectory(transactionDir)
}

func backupLegacyNativeRuntimeArtifact(rootDir, root string, backup *serviceIdentityGenerationBackup) error {
	sourceRel, err := serviceIdentityTransactionRelativePath(root, backup.Path)
	if err != nil {
		return err
	}
	backupRel, err := serviceIdentityTransactionRelativePath(rootDir, backup.BackupPath)
	if err != nil {
		return err
	}
	proof, err := copyServiceIdentityProofAt(
		root, sourceRel, backup.Original,
		rootDir, backupRel, serviceIdentityPathProof{Path: backup.BackupPath},
	)
	if err != nil {
		return fmt.Errorf("back up legacy runtime artifact %s: %w", backup.Path, err)
	}
	backup.Backup = proof
	return nil
}

func removeLegacyNativeRuntimeArtifacts(root string, backups []serviceIdentityGenerationBackup) error {
	for _, backup := range backups {
		rel, err := serviceIdentityTransactionRelativePath(root, backup.Path)
		if err != nil {
			return err
		}
		if err := validateServiceIdentityPathProofAt(root, rel, backup.Original); err != nil {
			return err
		}
	}
	for _, backup := range backups {
		rel, err := serviceIdentityTransactionRelativePath(root, backup.Path)
		if err != nil {
			return err
		}
		if err := removeServiceIdentityProofAt(root, rel, backup.Original); err != nil {
			return fmt.Errorf("remove legacy runtime artifact %s: %w", backup.Path, err)
		}
	}
	return nil
}

func restoreLegacyNativeRuntimeBackup(rootDir, root string, backups []serviceIdentityGenerationBackup, discardedRoot string) error {
	if len(backups) == 0 {
		return nil
	}
	if err := validateLegacyNativeRuntimeRestoration(rootDir, root, backups, discardedRoot); err != nil {
		return err
	}
	for _, backup := range backups {
		if err := restoreLegacyNativeRuntimeArtifact(rootDir, root, backup, discardedRoot); err != nil {
			return err
		}
	}
	return cleanupLegacyNativeRuntimeBackup(backups)
}

func restoreLegacyNativeRuntimeArtifact(rootDir, root string, backup serviceIdentityGenerationBackup, discardedRoot string) error {
	if discardedRoot != "" && pathWithinServiceIdentityRoot(filepath.Clean(discardedRoot), filepath.Clean(backup.Path)) {
		return nil
	}
	originalRel, err := serviceIdentityTransactionRelativePath(root, backup.Path)
	if err != nil {
		return err
	}
	current, err := captureServiceIdentityPathProofAt(root, originalRel, backup.Path)
	if err != nil || serviceIdentityPathStateEqual(current, backup.Original) {
		return err
	}
	backupRel, err := serviceIdentityTransactionRelativePath(rootDir, backup.BackupPath)
	if err != nil {
		return err
	}
	if _, err := copyServiceIdentityProofAt(rootDir, backupRel, backup.Backup, root, originalRel, current); err != nil {
		return fmt.Errorf("restore legacy runtime artifact %s: %w", backup.Path, err)
	}
	return nil
}

func validateLegacyNativeRuntimeRestoration(rootDir, root string, backups []serviceIdentityGenerationBackup, discardedRoot string) error {
	for _, backup := range backups {
		if discardedRoot != "" && pathWithinServiceIdentityRoot(filepath.Clean(discardedRoot), filepath.Clean(backup.Path)) {
			continue
		}
		originalRel, err := serviceIdentityTransactionRelativePath(root, backup.Path)
		if err != nil {
			return err
		}
		current, err := captureServiceIdentityPathProofAt(root, originalRel, backup.Path)
		if err != nil {
			return err
		}
		if serviceIdentityPathStateEqual(current, backup.Original) {
			continue
		}
		if current.Present {
			return fmt.Errorf("legacy runtime artifact %s changed after backup; path was left untouched", backup.Path)
		}
		backupRel, err := serviceIdentityTransactionRelativePath(rootDir, backup.BackupPath)
		if err != nil {
			return err
		}
		if err := validateServiceIdentityPathProofAt(rootDir, backupRel, backup.Backup); err != nil {
			return err
		}
	}
	return nil
}

func serviceIdentityTransactionRelativePath(root, path string) (string, error) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("transaction path %s is outside stable root %s", path, root)
	}
	if err := validateServiceIdentityJournalRecordPath(rel); err != nil {
		return "", err
	}
	return rel, nil
}

func cleanupLegacyNativeRuntimeBackup(backups []serviceIdentityGenerationBackup) error {
	return cleanupServiceIdentityGenerationBackups(backups)
}

func validateServiceIdentityRootTraversal(service, root string, identity db.ServiceIdentity) error {
	if err := validateHostControlledServiceRootPath(root); err != nil {
		return err
	}
	if identity.UID == 0 {
		return nil
	}
	root = filepath.Clean(root)
	parent := filepath.Dir(root)
	for current := parent; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil {
			return fmt.Errorf("inspect service root parent %s: %w", current, err)
		}
		uid, gid, err := nativeServiceFileOwner(info)
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		traversable := mode&0o001 != 0
		if uid == identity.UID {
			traversable = mode&0o100 != 0
		} else if gid == identity.GID {
			traversable = mode&0o010 != 0
		}
		if !traversable {
			return fmt.Errorf(
				"service root %s is not traversable by %s\n"+
					"migrate the exact legacy host layout:\n"+
					"  yeet host set --data-dir=/var/lib/yeet --services-root=/var/lib/yeet/services --migrate-services=all\n"+
					"or move only this service in the same transaction:\n"+
					"  yeet service set %s --service-root=/var/lib/yeet/services/%s --copy --run-as=%s",
				root, identity.RequestedUser, service, service, identity.RequestedUser,
			)
		}
		if current == string(filepath.Separator) {
			break
		}
	}
	return nil
}
