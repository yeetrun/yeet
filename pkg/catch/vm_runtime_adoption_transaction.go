// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

// VMRuntimeAdoptionSummary reports the deterministic fleet decision made at
// preparation time. Blocked VMs remain on their existing explicit runtime
// pair. Adopting contains only members of the atomic journal cohort.
type VMRuntimeAdoptionSummary struct {
	Ready                      []string
	PendingRestart             []string
	AlreadyAdopted             []string
	Adopting                   []string
	Blocked                    []string
	BlockedReasons             map[string]string
	HasChanges                 bool
	RequiresRollbackGeneration bool
}

type vmRuntimeAdoptionCoordinatorDeps struct {
	inventory       vmRuntimeAdoptionInventoryDeps
	journal         vmRuntimeJournalStoreDeps
	descriptor      vmRuntimeDescriptorFileDeps
	unit            vmRuntimeJournalUnitDeps
	provenance      vmLegacyCompositionStoreDeps
	random          io.Reader
	now             func() time.Time
	afterTransition func(string) error
}

type vmRuntimeAdoptionDescriptorCohort struct {
	transactions []vmRuntimeAdoptionDescriptorBinding
}

type vmRuntimeAdoptionDescriptorBinding struct {
	service     string
	transaction *vmRuntimeDescriptorRawTransaction
}

// VMRuntimeAdoption is a prepared, single-use fleet transaction. It holds the
// root runtime journal flock and all bound descriptor/unit directory locks
// until Close, so later runtime writers can serialize on the same root lock.
type VMRuntimeAdoption struct {
	mu                     sync.Mutex
	cfg                    *Config
	deps                   vmRuntimeAdoptionCoordinatorDeps
	journal                *vmRuntimeJournalStore
	records                []vmRuntimeJournalRecord
	preparations           []vmRuntimeAdoptionPreparation
	publishedUnitFragments [][]vmRuntimeAdoptionUnitFragment
	descriptors            *vmRuntimeAdoptionDescriptorCohort
	units                  *vmRuntimeJournalUnitReconciler
	summary                VMRuntimeAdoptionSummary
	transactionID          string
	commitDone             bool
	commitErr              error
	rollbackSafe           bool
	closed                 bool
	beforeDerived          func() error
	validateLatest         func(*db.Data) error
	revalidateEvidence     func(vmRuntimeAdoptionPreparation, map[string]struct{}) error
}

func defaultVMRuntimeAdoptionCoordinatorDeps() vmRuntimeAdoptionCoordinatorDeps {
	return vmRuntimeAdoptionCoordinatorDeps{
		inventory:  defaultVMRuntimeAdoptionInventoryDeps(),
		journal:    defaultVMRuntimeJournalStoreDeps(),
		descriptor: defaultVMRuntimeDescriptorFileDeps(),
		unit:       defaultVMRuntimeJournalUnitDeps(),
		provenance: defaultVMLegacyCompositionStoreDeps(),
		random:     rand.Reader,
		now:        time.Now,
	}
}

func completeVMRuntimeAdoptionCoordinatorDeps(deps vmRuntimeAdoptionCoordinatorDeps) vmRuntimeAdoptionCoordinatorDeps {
	defaults := defaultVMRuntimeAdoptionCoordinatorDeps()
	deps.inventory = completeVMRuntimeAdoptionInventoryDeps(deps.inventory)
	deps.journal = completeVMRuntimeJournalStoreDeps(deps.journal)
	deps.descriptor = completeVMRuntimeDescriptorFileDeps(deps.descriptor)
	deps.unit = completeVMRuntimeJournalUnitDeps(deps.unit)
	deps.provenance = completeVMLegacyCompositionStoreDeps(deps.provenance)
	if deps.random == nil {
		deps.random = defaults.random
	}
	if deps.now == nil {
		deps.now = defaults.now
	}
	return deps
}

// PrepareVMRuntimeAdoption inventories and durably prepares one sorted fleet
// cohort. It never starts, stops, or restarts a VM.
func PrepareVMRuntimeAdoption(ctx context.Context, cfg *Config) (*VMRuntimeAdoption, error) {
	return prepareVMRuntimeAdoptionWithDeps(ctx, cfg, defaultVMRuntimeAdoptionCoordinatorDeps())
}

// InspectVMRuntimeAdoption performs the install-time fleet preflight without
// publishing provenance, descriptors, units, database state, or a journal.
// Preparation repeats the inventory under the runtime transaction lock after
// the new Catch generation is installed.
func InspectVMRuntimeAdoption(ctx context.Context, cfg *Config) (VMRuntimeAdoptionSummary, error) {
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return VMRuntimeAdoptionSummary{}, err
	}
	deps := completeVMRuntimeAdoptionCoordinatorDeps(defaultVMRuntimeAdoptionCoordinatorDeps())
	inventory, err := inventoryVMRuntimeAdoptionFleet(ctx, effectiveCfg, deps.inventory)
	if err != nil {
		return VMRuntimeAdoptionSummary{}, err
	}
	return vmRuntimeAdoptionPublicSummary(inventory, deps.inventory.readiness)
}

func prepareVMRuntimeAdoptionWithDeps(ctx context.Context, cfg *Config, deps vmRuntimeAdoptionCoordinatorDeps) (_ *VMRuntimeAdoption, retErr error) {
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return nil, err
	}
	deps = completeVMRuntimeAdoptionCoordinatorDeps(deps)
	journal, err := openVMRuntimeJournalStore(ctx, effectiveCfg.RootDir, deps.journal)
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, journal.Close())
		}
	}()
	return prepareVMRuntimeAdoptionWithStore(ctx, effectiveCfg, journal, deps)
}

func prepareVMRuntimeAdoptionWithStore(ctx context.Context, cfg *Config, journal *vmRuntimeJournalStore, deps vmRuntimeAdoptionCoordinatorDeps) (*VMRuntimeAdoption, error) {
	if err := recoverVMRuntimeAdoptionsWithStore(ctx, cfg, journal, deps); err != nil {
		return nil, fmt.Errorf("recover VM runtime adoption before preparation: %w", err)
	}
	if err := journal.CleanupCommittedTombstones(); err != nil {
		return nil, fmt.Errorf("clean committed VM runtime journal tombstones: %w", err)
	}

	inventory, err := inventoryVMRuntimeAdoptionFleet(ctx, cfg, deps.inventory)
	if err != nil {
		return nil, err
	}
	summary, err := vmRuntimeAdoptionPublicSummary(inventory, deps.inventory.readiness)
	if err != nil {
		return nil, err
	}
	transaction := &VMRuntimeAdoption{
		cfg: cfg, deps: deps, journal: journal,
		summary: summary,
	}
	if len(inventory.Summary.Adoptable) == 0 {
		transaction.rollbackSafe = true
		return transaction, nil
	}
	return prepareVMRuntimeAdoptionCohort(ctx, transaction, inventory)
}

func prepareVMRuntimeAdoptionCohort(ctx context.Context, transaction *VMRuntimeAdoption, inventory vmRuntimeAdoptionInventory) (_ *VMRuntimeAdoption, retErr error) {
	dataView, err := transaction.cfg.DB.Get()
	if err != nil {
		return nil, fmt.Errorf("read VM runtime adoption services: %w", err)
	}
	data := dataView.AsStruct()
	preparations, err := persistVMRuntimeAdoptionProvenance(transaction.cfg, inventory, data, transaction.deps)
	if err != nil {
		return nil, err
	}
	transactionID, err := newVMRuntimeAdoptionTransactionID(transaction.deps.random)
	if err != nil {
		return nil, err
	}
	preparedAt := transaction.deps.now().UTC()
	records, err := buildVMRuntimeAdoptionJournalRecords(transaction.cfg, data, preparations, transactionID, preparedAt, transaction.deps)
	if err != nil {
		return nil, err
	}

	descriptors, err := prepareVMRuntimeAdoptionDescriptorCohort(ctx, records, transaction.deps.descriptor)
	if err != nil {
		return nil, err
	}
	transaction.descriptors = descriptors
	completed := false
	var units *vmRuntimeJournalUnitReconciler
	defer cleanupFailedVMRuntimeAdoptionCohort(&retErr, descriptors, &units, &completed)
	units, err = prepareVMRuntimeJournalUnitReconciler(ctx, records, transaction.deps.unit)
	if err != nil {
		return nil, err
	}
	transaction.units = units
	if err := validatePreparedVMRuntimeAdoption(ctx, transaction.cfg, preparations, records, descriptors, units, transaction.deps); err != nil {
		return nil, fmt.Errorf("revalidate VM runtime adoption before journal publication: %w", err)
	}
	transaction.records = cloneVMRuntimeJournalRecords(records)
	transaction.preparations = append([]vmRuntimeAdoptionPreparation(nil), preparations...)
	transaction.transactionID = transactionID
	if err := transaction.journal.Prepare(records); err != nil {
		return nil, transaction.handlePrepareFailure(err)
	}
	if err := runVMRuntimeAdoptionTransitionHook(transaction.deps, "prepared"); err != nil {
		return nil, transaction.finishPreDatabaseFailure(err)
	}

	completed = true
	return transaction, nil
}

func (tx *VMRuntimeAdoption) handlePrepareFailure(cause error) error {
	groups, err := tx.journal.LoadAll()
	if err != nil {
		return errors.Join(cause, fmt.Errorf("classify VM runtime journal after prepare failure: %w", err))
	}
	if _, found := findVMRuntimeJournalGroup(groups, tx.transactionID); !found {
		return cause
	}
	if err := tx.journal.Resume(tx.transactionID); err != nil {
		return errors.Join(cause, fmt.Errorf("resume VM runtime journal after prepare failure: %w", err))
	}
	return tx.finishPreDatabaseFailure(cause)
}

func cleanupFailedVMRuntimeAdoptionCohort(retErr *error, descriptors *vmRuntimeAdoptionDescriptorCohort, units **vmRuntimeJournalUnitReconciler, completed *bool) {
	if *completed {
		return
	}
	if *units != nil {
		*retErr = errors.Join(*retErr, (*units).Close())
	}
	*retErr = errors.Join(*retErr, descriptors.Close())
}

func vmRuntimeAdoptionPublicSummary(inventory vmRuntimeAdoptionInventory, readiness func(string) (vmJailerReadiness, error)) (VMRuntimeAdoptionSummary, error) {
	result := VMRuntimeAdoptionSummary{
		AlreadyAdopted: append([]string(nil), inventory.Summary.AlreadyAdopted...),
		Adopting:       append([]string(nil), inventory.Summary.Adoptable...),
		Blocked:        append([]string(nil), inventory.Summary.Blocked...),
	}
	for _, preparation := range inventory.VMs {
		if strings.TrimSpace(preparation.ServiceRoot) == "" {
			continue
		}
		state, err := readiness(preparation.ServiceRoot)
		if err != nil {
			return VMRuntimeAdoptionSummary{}, fmt.Errorf("read VM jailer readiness for %s: %w", preparation.Service, err)
		}
		switch state {
		case vmJailerReady:
			result.Ready = append(result.Ready, preparation.Service)
		case vmJailerPendingRestart:
			result.PendingRestart = append(result.PendingRestart, preparation.Service)
		default:
			return VMRuntimeAdoptionSummary{}, fmt.Errorf("unsupported VM jailer readiness %q for %s", state, preparation.Service)
		}
	}
	sort.Strings(result.Ready)
	sort.Strings(result.PendingRestart)
	if len(inventory.Summary.BlockedReasons) != 0 {
		result.BlockedReasons = make(map[string]string, len(inventory.Summary.BlockedReasons))
		for service, reason := range inventory.Summary.BlockedReasons {
			result.BlockedReasons[service] = reason
		}
	}
	result.HasChanges = len(result.Adopting) != 0
	result.RequiresRollbackGeneration = result.HasChanges
	return result, nil
}

func persistVMRuntimeAdoptionProvenance(cfg *Config, inventory vmRuntimeAdoptionInventory, data *db.Data, deps vmRuntimeAdoptionCoordinatorDeps) ([]vmRuntimeAdoptionPreparation, error) {
	preparations := make([]vmRuntimeAdoptionPreparation, 0, len(inventory.Summary.Adoptable))
	for _, preparation := range inventory.VMs {
		if !isAdoptableVMRuntimeClassification(preparation.Classification) {
			continue
		}
		preparation, err := bindVMRuntimeAdoptionProvenance(cfg, data, preparation, deps)
		if err != nil {
			return nil, err
		}
		preparations = append(preparations, preparation)
	}
	sort.Slice(preparations, func(i, j int) bool { return preparations[i].Service < preparations[j].Service })
	return preparations, nil
}

func isAdoptableVMRuntimeClassification(classification vmRuntimeAdoptionClassification) bool {
	switch classification {
	case vmRuntimeAdoptionOfficialLegacy, vmRuntimeAdoptionLocalLegacy, vmRuntimeAdoptionCustomLegacy:
		return true
	default:
		return false
	}
}

func bindVMRuntimeAdoptionProvenance(cfg *Config, data *db.Data, preparation vmRuntimeAdoptionPreparation, deps vmRuntimeAdoptionCoordinatorDeps) (vmRuntimeAdoptionPreparation, error) {
	publication, err := persistVMLegacyComposition(cfg.RootDir, preparation.Composition, deps.provenance)
	if err != nil {
		return preparation, fmt.Errorf("persist VM runtime adoption provenance for %s: %w", preparation.Service, err)
	}
	if publication.SHA256 != preparation.CompositionSHA256 {
		return preparation, fmt.Errorf("persisted VM runtime adoption provenance digest for %s changed", preparation.Service)
	}
	evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(publication.Path, true, deps.inventory.evidence)
	if err != nil {
		return preparation, fmt.Errorf("bind VM runtime adoption provenance for %s: %w", preparation.Service, err)
	}
	preparation.Evidence.Files = append(preparation.Evidence.Files, evidence)
	preparation.Evidence.Files, err = normalizeVMRuntimeAdoptionEvidence(preparation.Evidence.Files)
	if err != nil {
		return preparation, err
	}
	service := data.Services[preparation.Service]
	if service == nil || service.VM == nil {
		return preparation, fmt.Errorf("VM runtime adoption service %s disappeared", preparation.Service)
	}
	preparation.PreconditionSHA256, err = vmRuntimeAdoptionPreconditionDigest(*cfg, *service, preparation)
	return preparation, err
}

func newVMRuntimeAdoptionTransactionID(random io.Reader) (string, error) {
	var raw [32]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", fmt.Errorf("generate VM runtime adoption transaction ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func buildVMRuntimeAdoptionJournalRecords(cfg *Config, data *db.Data, preparations []vmRuntimeAdoptionPreparation, transactionID string, preparedAt time.Time, deps vmRuntimeAdoptionCoordinatorDeps) ([]vmRuntimeJournalRecord, error) {
	members := make([]string, len(preparations))
	for i := range preparations {
		members[i] = preparations[i].Service
	}
	records := make([]vmRuntimeJournalRecord, 0, len(preparations))
	for _, preparation := range preparations {
		service := data.Services[preparation.Service]
		if service == nil || service.VM == nil {
			return nil, fmt.Errorf("VM runtime adoption service %s is missing", preparation.Service)
		}
		descriptorPath := filepath.Join(serviceDataDirForRoot(preparation.ServiceRoot), vmRuntimeDescriptorFileName)
		oldDescriptor, err := readVMRuntimeAdoptionDescriptorJournalFile(descriptorPath, deps.descriptor)
		if err != nil {
			return nil, fmt.Errorf("capture old VM runtime descriptor for %s: %w", preparation.Service, err)
		}
		newDescriptor, err := renderVMRuntimeAdoptionDescriptorJournalFile(descriptorPath, preparation, deps.descriptor)
		if err != nil {
			return nil, err
		}
		unitPath := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(preparation.Service))
		oldUnit, err := readVMRuntimeAdoptionUnitJournalFile(unitPath, deps.unit)
		if err != nil {
			return nil, fmt.Errorf("capture old VM unit for %s: %w", preparation.Service, err)
		}
		newUnit, err := renderVMRuntimeAdoptionUnitJournalFile(cfg, *service, preparation, unitPath, deps.unit)
		if err != nil {
			return nil, err
		}
		records = append(records, vmRuntimeJournalRecord{
			Schema: vmRuntimeJournalSchema, SchemaVersion: vmRuntimeJournalSchemaVersion,
			TransactionID: transactionID, Members: append([]string(nil), members...),
			Service: preparation.Service, ServiceRoot: preparation.ServiceRoot,
			State: vmRuntimeJournalStatePrepared, PreparedAt: preparedAt, UpdatedAt: preparedAt,
			PreconditionSHA256: preparation.PreconditionSHA256,
			OldDB:              preparation.OldDB, NewDB: preparation.NewDB,
			OldDescriptor: oldDescriptor, NewDescriptor: newDescriptor,
			OldUnit: oldUnit, NewUnit: newUnit,
		})
	}
	return records, nil
}

func renderVMRuntimeAdoptionDescriptorJournalFile(path string, preparation vmRuntimeAdoptionPreparation, deps vmRuntimeDescriptorFileDeps) (vmRuntimeJournalFile, error) {
	descriptor := vmRuntimeDescriptor{
		SchemaVersion: vmRuntimeDescriptorSchemaVersion,
		Service:       preparation.Service,
		Configured:    preparation.EffectiveRuntime,
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return vmRuntimeJournalFile{}, fmt.Errorf("encode VM runtime descriptor for %s: %w", preparation.Service, err)
	}
	if _, err := decodeVMRuntimeDescriptor(raw, preparation.Service); err != nil {
		return vmRuntimeJournalFile{}, fmt.Errorf("validate VM runtime descriptor for %s: %w", preparation.Service, err)
	}
	raw = append(raw, '\n')
	return newVMRuntimeAdoptionJournalFile(path, raw, unix.S_IFREG|0o600, deps.uid, deps.gid), nil
}

func renderVMRuntimeAdoptionUnitJournalFile(cfg *Config, service db.Service, preparation vmRuntimeAdoptionPreparation, path string, deps vmRuntimeJournalUnitDeps) (vmRuntimeJournalFile, error) {
	flags, err := vmRuntimeAdoptionExecFlags(preparation.EffectiveUnit.ExecStart)
	if err != nil {
		return vmRuntimeJournalFile{}, err
	}
	runDir := serviceRunDirForRoot(preparation.ServiceRoot)
	descriptorPath := filepath.Join(serviceDataDirForRoot(preparation.ServiceRoot), vmRuntimeDescriptorFileName)
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service: preparation.Service, Runner: preparation.EffectiveUnit.Runner,
		DataDir: cfg.RootDir, ServicesRoot: cfg.ServicesRoot, ServiceRoot: preparation.ServiceRoot,
		DiskPath:             preparation.Evidence.ActiveDisk.Path,
		RuntimeDescriptor:    descriptorPath,
		RuntimeRunningMarker: filepath.Join(runDir, vmRuntimeRunningMarkerFileName),
		RuntimeTrialResult:   filepath.Join(runDir, vmRuntimeTrialResultFileName),
		JailerBase:           preparation.EffectiveUnit.JailerBase,
		ConfigPath:           flags["--config-file"], APISocket: flags["--api-sock"], ConsoleSocket: flags["--console-sock"],
		VsockSocket: effectiveVMRuntimeAdoptionVsock(service, runDir), WorkingDirectory: preparation.ServiceRoot,
	})
	if err != nil {
		return vmRuntimeJournalFile{}, fmt.Errorf("render descriptor-mode VM unit for %s: %w", preparation.Service, err)
	}
	return newVMRuntimeAdoptionJournalFile(path, []byte(unit), unix.S_IFREG|0o644, deps.uid, deps.gid), nil
}

func effectiveVMRuntimeAdoptionVsock(service db.Service, runDir string) string {
	if service.VM != nil && strings.TrimSpace(service.VM.Sockets.VsockSocketPath) != "" {
		return service.VM.Sockets.VsockSocketPath
	}
	return filepath.Join(runDir, "vsock.sock")
}

func newVMRuntimeAdoptionJournalFile(path string, raw []byte, mode, uid, gid uint32) vmRuntimeJournalFile {
	digest := sha256.Sum256(raw)
	return vmRuntimeJournalFile{
		Path: path, Exists: true, Contents: append([]byte(nil), raw...),
		Mode: mode, UID: uid, GID: gid, SHA256: hex.EncodeToString(digest[:]),
	}
}

func readVMRuntimeAdoptionDescriptorJournalFile(path string, deps vmRuntimeDescriptorFileDeps) (_ vmRuntimeJournalFile, retErr error) {
	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(path), deps.uid)
	if err != nil {
		return vmRuntimeJournalFile{}, err
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	current, err := inspectVMRuntimeDescriptorRawAt(context.Background(), dir, filepath.Base(path), vmRuntimeDescriptorMaxSize, nil)
	if err != nil {
		return vmRuntimeJournalFile{}, err
	}
	if !current.exists {
		return vmRuntimeJournalFile{Path: path}, nil
	}
	return newVMRuntimeAdoptionJournalFile(path, current.raw, uint32(current.stat.Mode), current.stat.Uid, current.stat.Gid), nil
}

func readVMRuntimeAdoptionUnitJournalFile(path string, deps vmRuntimeJournalUnitDeps) (_ vmRuntimeJournalFile, retErr error) {
	dir, err := openValidatedVMUnitDir(filepath.Dir(path), deps.uid)
	if err != nil {
		return vmRuntimeJournalFile{}, err
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	raw, exists, _, stat, err := readVMUnitStateAt(dir, filepath.Base(path), deps.uid)
	if err != nil {
		return vmRuntimeJournalFile{}, err
	}
	if !exists {
		return vmRuntimeJournalFile{Path: path}, nil
	}
	return newVMRuntimeAdoptionJournalFile(path, raw, uint32(stat.Mode), stat.Uid, stat.Gid), nil
}

func prepareVMRuntimeAdoptionDescriptorCohort(ctx context.Context, records []vmRuntimeJournalRecord, deps vmRuntimeDescriptorFileDeps) (*vmRuntimeAdoptionDescriptorCohort, error) {
	records = cloneVMRuntimeJournalRecords(records)
	sort.Slice(records, func(i, j int) bool { return records[i].NewDescriptor.Path < records[j].NewDescriptor.Path })
	cohort := &vmRuntimeAdoptionDescriptorCohort{}
	for _, record := range records {
		tx, err := prepareVMRuntimeDescriptorRawTransaction(ctx, record.OldDescriptor, record.NewDescriptor, deps)
		if err != nil {
			return nil, errors.Join(err, cohort.Close())
		}
		cohort.transactions = append(cohort.transactions, vmRuntimeAdoptionDescriptorBinding{service: record.Service, transaction: tx})
	}
	return cohort, nil
}

func (cohort *vmRuntimeAdoptionDescriptorCohort) publishNew(ctx context.Context, hook func(string) error) error {
	for _, binding := range cohort.transactions {
		tx := binding.transaction
		if err := tx.PublishAndVerify(ctx); err != nil {
			return err
		}
		if hook != nil {
			if err := hook("descriptor-published:" + binding.service); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cohort *vmRuntimeAdoptionDescriptorCohort) restoreOld(ctx context.Context) error {
	var retErr error
	for i := len(cohort.transactions) - 1; i >= 0; i-- {
		retErr = errors.Join(retErr, cohort.transactions[i].transaction.RestorePreviousAndVerify(ctx))
	}
	return retErr
}

func (cohort *vmRuntimeAdoptionDescriptorCohort) verify(ctx context.Context, want vmRuntimeDescriptorRawClassification) error {
	for _, binding := range cohort.transactions {
		tx := binding.transaction
		classification, err := tx.Classify(ctx)
		if err != nil {
			return err
		}
		if classification != want {
			return fmt.Errorf("VM runtime descriptor %s is %s, want %s", tx.path, classification, want)
		}
	}
	return nil
}

func (cohort *vmRuntimeAdoptionDescriptorCohort) Close() error {
	if cohort == nil {
		return nil
	}
	var retErr error
	for i := len(cohort.transactions) - 1; i >= 0; i-- {
		retErr = errors.Join(retErr, cohort.transactions[i].transaction.Close())
	}
	return retErr
}

func validatePreparedVMRuntimeAdoption(ctx context.Context, cfg *Config, preparations []vmRuntimeAdoptionPreparation, records []vmRuntimeJournalRecord, descriptors *vmRuntimeAdoptionDescriptorCohort, units *vmRuntimeJournalUnitReconciler, deps vmRuntimeAdoptionCoordinatorDeps) error {
	if err := descriptors.verify(ctx, vmRuntimeDescriptorRawOld); err != nil {
		return err
	}
	if err := units.VerifyOld(ctx); err != nil {
		return err
	}
	return cfg.DB.WithLatestDataLocked(func(view db.DataView) error {
		data := view.AsStruct()
		for i := range records {
			service, err := validateVMRuntimeAdoptionOldProjection(data, records[i])
			if err != nil {
				return err
			}
			digest, err := vmRuntimeAdoptionPreconditionDigest(*cfg, *service, preparations[i])
			if err != nil || digest != records[i].PreconditionSHA256 {
				return errors.Join(err, fmt.Errorf("VM runtime adoption precondition changed for %s", records[i].Service))
			}
			if err := revalidateVMRuntimeAdoptionEvidence(preparations[i], nil, deps.inventory.evidence); err != nil {
				return err
			}
			if err := revalidateVMRuntimeAdoptionLoadedState(ctx, preparations[i], false, deps.inventory); err != nil {
				return err
			}
		}
		return nil
	})
}

func (tx *VMRuntimeAdoption) Commit() error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.commitDone {
		return tx.commitErr
	}
	tx.commitDone = true
	tx.rollbackSafe = false
	tx.commitErr = tx.commitLocked()
	return tx.commitErr
}

func (tx *VMRuntimeAdoption) commitLocked() error {
	if tx.closed {
		return fmt.Errorf("VM runtime adoption transaction is closed")
	}
	if len(tx.records) == 0 {
		tx.rollbackSafe = true
		return nil
	}
	if err := tx.publishNewDerived(); err != nil {
		return tx.failBeforeDatabase(err)
	}
	if err := tx.publishDatabase(); err != nil {
		return err
	}
	return tx.finalizeCommittedAdoption()
}

func (tx *VMRuntimeAdoption) publishNewDerived() error {
	if tx.beforeDerived != nil {
		if err := tx.beforeDerived(); err != nil {
			return err
		}
	}
	if err := tx.descriptors.publishNew(context.Background(), tx.deps.afterTransition); err != nil {
		return err
	}
	if err := tx.units.ReconcileNew(context.Background()); err != nil {
		return err
	}
	if err := runVMRuntimeAdoptionTransitionHook(tx.deps, "unit-published"); err != nil {
		return err
	}
	if err := tx.transitionJournal(vmRuntimeJournalStateDerivedPublished); err != nil {
		return err
	}
	return runVMRuntimeAdoptionTransitionHook(tx.deps, "derived-published")
}

func (tx *VMRuntimeAdoption) publishDatabase() error {
	tx.publishedUnitFragments = make([][]vmRuntimeAdoptionUnitFragment, len(tx.records))
	_, err := tx.cfg.DB.MutateDataWithPrePublicationCompensation(func(data *db.Data) (func() error, error) {
		compensate := func() error { return tx.restoreOldDerived(context.Background()) }
		if err := tx.validateNewAgainstLatest(context.Background(), data); err != nil {
			return compensate, err
		}
		for _, record := range tx.records {
			service := data.Services[record.Service]
			service.VM.Components = record.NewDB.Components.Clone()
			service.VM.Image.Kernel = record.NewDB.ImageKernel
		}
		if tx.records[0].VMHostProjection {
			if data.VMHost == nil {
				data.VMHost = &db.VMHostConfig{}
			}
			data.VMHost.RuntimePolicy = tx.records[0].NewVMHost.RuntimePolicy
			data.VMHost.RuntimeChannel = tx.records[0].NewVMHost.RuntimeChannel
		}
		return compensate, nil
	})
	if err != nil {
		return tx.handleDatabaseMutationError(err)
	}
	if err := runVMRuntimeAdoptionTransitionHook(tx.deps, "database-published"); err != nil {
		return err
	}
	if err := tx.transitionJournal(vmRuntimeJournalStateDatabaseCommitted); err != nil {
		return err
	}
	if err := runVMRuntimeAdoptionTransitionHook(tx.deps, "database-committed"); err != nil {
		return err
	}
	return nil
}

func (tx *VMRuntimeAdoption) finalizeCommittedAdoption() error {
	if err := tx.journal.BeginRemoval(tx.transactionID); err != nil {
		var pending *vmRuntimeJournalRemovalPendingError
		if !errors.As(err, &pending) {
			return err
		}
	}
	if err := runVMRuntimeAdoptionTransitionHook(tx.deps, "removal-begun"); err != nil {
		return err
	}
	if err := tx.journal.FinalizeRemovalWithLatestDataLocked(
		tx.transactionID, tx.cfg.DB, vmRuntimeJournalDBNew,
		func([]vmRuntimeJournalRecord) error { return tx.verifyNewFinalization(context.Background()) },
	); err != nil {
		return err
	}
	return runVMRuntimeAdoptionTransitionHook(tx.deps, "removal-finalized")
}

func (tx *VMRuntimeAdoption) handleDatabaseMutationError(err error) error {
	mutationErr := fmt.Errorf("commit VM runtime adoption database: %w", err)
	if !vmRuntimeAdoptionMutationCommitted(err) {
		return tx.finishPreDatabaseFailure(mutationErr)
	}
	transitionErr := tx.transitionJournal(vmRuntimeJournalStateDatabaseCommitted)
	return errors.Join(mutationErr, wrapVMRuntimeAdoptionJournalTransitionError(transitionErr))
}

func vmRuntimeAdoptionMutationCommitted(err error) bool {
	var published *db.PostPublicationError
	return errors.As(err, &published) && published.MutationCommitted
}

func wrapVMRuntimeAdoptionJournalTransitionError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("record committed VM runtime adoption database in journal: %w", err)
}

func (tx *VMRuntimeAdoption) transitionJournal(state vmRuntimeJournalState) error {
	groups, err := tx.journal.LoadAll()
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group.TransactionID != tx.transactionID || len(group.Records) == 0 {
			continue
		}
		current := group.Records[0]
		if current.State == state {
			return nil
		}
		at := tx.deps.now().UTC()
		if !at.After(current.UpdatedAt) {
			at = current.UpdatedAt.Add(time.Nanosecond)
		}
		return tx.journal.Transition(tx.transactionID, state, at)
	}
	return fmt.Errorf("VM runtime adoption journal %s disappeared", tx.transactionID)
}

func (tx *VMRuntimeAdoption) failBeforeDatabase(cause error) error {
	return tx.finishPreDatabaseFailure(errors.Join(cause, tx.restoreOldDerived(context.Background())))
}

func (tx *VMRuntimeAdoption) finishPreDatabaseFailure(cause error) error {
	rollbackSafe, safeErr := tx.finalizeRollbackSafe()
	tx.rollbackSafe = rollbackSafe
	return errors.Join(cause, safeErr)
}

func (tx *VMRuntimeAdoption) finalizeRollbackSafe() (bool, error) {
	if err := tx.verifyRollbackSafe(); err != nil {
		return false, err
	}
	if err := tx.journal.BeginRemoval(tx.transactionID); err != nil {
		var pending *vmRuntimeJournalRemovalPendingError
		if !errors.As(err, &pending) {
			return false, err
		}
	}
	finalizeErr := tx.journal.FinalizeRemovalWithLatestDataLocked(
		tx.transactionID, tx.cfg.DB, vmRuntimeJournalDBOld,
		func([]vmRuntimeJournalRecord) error { return tx.verifyOldDerived(context.Background()) },
	)
	if finalizeErr == nil {
		return true, nil
	}
	groups, loadErr := tx.journal.LoadAll()
	if loadErr != nil {
		return false, errors.Join(finalizeErr, loadErr)
	}
	if _, found := findVMRuntimeJournalGroup(groups, tx.transactionID); found {
		return false, finalizeErr
	}
	return true, finalizeErr
}

func (tx *VMRuntimeAdoption) restoreOldDerived(ctx context.Context) error {
	// The old explicit unit is restored and reloaded before an old descriptor is
	// restored or removed, so no loaded unit can depend on a disappearing file.
	if err := tx.units.ReconcileOld(ctx); err != nil {
		return err
	}
	return tx.descriptors.restoreOld(ctx)
}

func (tx *VMRuntimeAdoption) verifyNewDerived(ctx context.Context) error {
	return errors.Join(tx.descriptors.verify(ctx, vmRuntimeDescriptorRawNew), tx.units.VerifyNew(ctx))
}

func (tx *VMRuntimeAdoption) verifyNewFinalization(ctx context.Context) error {
	if err := tx.verifyNewDerived(ctx); err != nil {
		return err
	}
	for i, preparation := range tx.preparations {
		loaded, err := loadAndRevalidateVMRuntimeAdoptionUnit(ctx, preparation, true, tx.deps.inventory)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(loaded.Fragments, tx.publishedUnitFragments[i]) {
			return fmt.Errorf("VM %s effective unit fragment evidence changed after database publication", preparation.Service)
		}
	}
	return nil
}

func (tx *VMRuntimeAdoption) verifyOldDerived(ctx context.Context) error {
	return errors.Join(tx.units.VerifyOld(ctx), tx.descriptors.verify(ctx, vmRuntimeDescriptorRawOld))
}

func (tx *VMRuntimeAdoption) verifyRollbackSafe() error {
	return tx.cfg.DB.WithLatestDataLocked(func(view db.DataView) error {
		group := vmRuntimeJournalGroup{Records: tx.records}
		if classification := group.ClassifyDB(view.AsStruct()); classification != vmRuntimeJournalDBOld {
			return fmt.Errorf("VM runtime adoption database is %s, want old", classification)
		}
		return tx.verifyOldDerived(context.Background())
	})
}

func (tx *VMRuntimeAdoption) validateNewAgainstLatest(ctx context.Context, data *db.Data) error {
	if err := tx.verifyNewDerived(ctx); err != nil {
		return err
	}
	if err := tx.validateVMHostAgainstLatest(data); err != nil {
		return err
	}
	if tx.validateLatest != nil {
		if err := tx.validateLatest(data); err != nil {
			return err
		}
	}
	for i := range tx.records {
		if err := tx.validateVMRuntimeAdoptionRecordLatest(ctx, data, i); err != nil {
			return err
		}
	}
	return nil
}

func (tx *VMRuntimeAdoption) validateVMHostAgainstLatest(data *db.Data) error {
	if len(tx.records) == 0 || !tx.records[0].VMHostProjection {
		return nil
	}
	if tx.records[0].OldVMHost == nil || vmRuntimeJournalVMHostProjectionFromConfig(data.VMHost) != *tx.records[0].OldVMHost {
		return fmt.Errorf("VM host runtime policy changed during runtime transaction")
	}
	return nil
}

func (tx *VMRuntimeAdoption) validateVMRuntimeAdoptionRecordLatest(ctx context.Context, data *db.Data, index int) error {
	record := tx.records[index]
	preparation := tx.preparations[index]
	service, err := validateVMRuntimeAdoptionOldProjection(data, record)
	if err != nil {
		return err
	}
	digest, err := vmRuntimeAdoptionPreconditionDigest(*tx.cfg, *service, preparation)
	if err != nil || digest != record.PreconditionSHA256 {
		return errors.Join(err, fmt.Errorf("VM runtime adoption database precondition changed for %s", record.Service))
	}
	skipped := map[string]struct{}{record.OldDescriptor.Path: {}, record.OldUnit.Path: {}}
	if err := tx.revalidatePreparedEvidence(preparation, skipped); err != nil {
		return err
	}
	loaded, err := loadAndRevalidateVMRuntimeAdoptionUnit(ctx, preparation, true, tx.deps.inventory)
	if err != nil {
		return err
	}
	tx.publishedUnitFragments[index] = append([]vmRuntimeAdoptionUnitFragment(nil), loaded.Fragments...)
	return nil
}

func (tx *VMRuntimeAdoption) revalidatePreparedEvidence(preparation vmRuntimeAdoptionPreparation, skipped map[string]struct{}) error {
	if tx.revalidateEvidence != nil {
		return tx.revalidateEvidence(preparation, skipped)
	}
	return revalidateVMRuntimeAdoptionEvidence(preparation, skipped, tx.deps.inventory.evidence)
}

func validateVMRuntimeAdoptionOldProjection(data *db.Data, record vmRuntimeJournalRecord) (*db.Service, error) {
	projection, ok := vmRuntimeJournalProjectionFromData(data, record.Service)
	if !ok {
		return nil, fmt.Errorf("VM runtime adoption service %s disappeared", record.Service)
	}
	if !equalVMRuntimeJournalProjection(projection, record.OldDB) {
		return nil, fmt.Errorf("VM runtime adoption database projection changed for %s", record.Service)
	}
	return data.Services[record.Service], nil
}

func revalidateVMRuntimeAdoptionEvidence(preparation vmRuntimeAdoptionPreparation, skipped map[string]struct{}, deps vmRuntimeAdoptionEvidenceDeps) error {
	for _, expected := range preparation.Evidence.Files {
		if _, ok := skipped[expected.Path]; ok {
			continue
		}
		actual, err := collectTrustedVMRuntimeAdoptionFileEvidence(expected.Path, expected.Exists, deps)
		if err != nil {
			return fmt.Errorf("revalidate VM runtime adoption evidence for %s: %w", preparation.Service, err)
		}
		if actual != expected {
			return fmt.Errorf("VM runtime adoption evidence changed for %s at %s", preparation.Service, expected.Path)
		}
	}
	disk := preparation.Evidence.ActiveDisk
	actualDisk, err := collectVMRuntimeActiveDiskEvidence(disk.Path, disk.Backend, disk.Bytes, deps)
	if err != nil {
		return fmt.Errorf("revalidate VM runtime adoption disk for %s: %w", preparation.Service, err)
	}
	if actualDisk != disk {
		return fmt.Errorf("VM runtime adoption disk evidence changed for %s", preparation.Service)
	}
	return nil
}

func revalidateVMRuntimeAdoptionLoadedState(ctx context.Context, preparation vmRuntimeAdoptionPreparation, descriptorMode bool, deps vmRuntimeAdoptionInventoryDeps) error {
	_, err := loadAndRevalidateVMRuntimeAdoptionUnit(ctx, preparation, descriptorMode, deps)
	return err
}

func loadAndRevalidateVMRuntimeAdoptionUnit(ctx context.Context, preparation vmRuntimeAdoptionPreparation, descriptorMode bool, deps vmRuntimeAdoptionInventoryDeps) (vmRuntimeAdoptionLoadedUnit, error) {
	loaded, err := deps.loadUnit(ctx, vmSystemdUnitName(preparation.Service))
	if err != nil {
		return vmRuntimeAdoptionLoadedUnit{}, fmt.Errorf("reload effective VM unit for %s: %w", preparation.Service, err)
	}
	if loaded.ActiveState != preparation.EffectiveUnit.ActiveState || loaded.MainPID != preparation.EffectiveUnit.MainPID {
		return vmRuntimeAdoptionLoadedUnit{}, fmt.Errorf("VM %s active state or PID changed during runtime adoption", preparation.Service)
	}
	if loaded.NeedDaemonReload != "no" {
		return vmRuntimeAdoptionLoadedUnit{}, fmt.Errorf("VM %s unit still requires daemon-reload", preparation.Service)
	}
	if !descriptorMode {
		return loaded, validateExplicitVMRuntimeAdoptionLoadedState(preparation, loaded)
	}
	return loaded, validateDescriptorVMRuntimeAdoptionLoadedState(preparation, loaded)
}

func validateExplicitVMRuntimeAdoptionLoadedState(preparation vmRuntimeAdoptionPreparation, loaded vmRuntimeAdoptionLoadedUnit) error {
	if !reflect.DeepEqual(loaded.ExecStart, preparation.EffectiveUnit.ExecStart) {
		return fmt.Errorf("VM %s effective explicit launch command changed", preparation.Service)
	}
	return nil
}

func validateDescriptorVMRuntimeAdoptionLoadedState(preparation vmRuntimeAdoptionPreparation, loaded vmRuntimeAdoptionLoadedUnit) error {
	runner, flags, err := validateVMRuntimeAdoptionLoadedCommand(loaded.ExecStart)
	if err != nil {
		return err
	}
	runDir := serviceRunDirForRoot(preparation.ServiceRoot)
	wants := []struct {
		name string
		want string
	}{
		{name: "--service", want: preparation.Service},
		{name: "--service-root", want: preparation.ServiceRoot},
		{name: "--disk-path", want: preparation.Evidence.ActiveDisk.Path},
		{name: "--runtime-descriptor", want: filepath.Join(serviceDataDirForRoot(preparation.ServiceRoot), vmRuntimeDescriptorFileName)},
		{name: "--runtime-running-marker", want: filepath.Join(runDir, vmRuntimeRunningMarkerFileName)},
		{name: "--runtime-trial-result", want: filepath.Join(runDir, vmRuntimeTrialResultFileName)},
		{name: "--jailer-base", want: preparation.EffectiveUnit.JailerBase},
	}
	if runner != preparation.EffectiveUnit.Runner {
		return fmt.Errorf("VM %s runner changed during runtime adoption", preparation.Service)
	}
	for _, expected := range wants {
		if flags[expected.name] != expected.want {
			return fmt.Errorf("VM %s effective descriptor unit %s is %q, want %q", preparation.Service, expected.name, flags[expected.name], expected.want)
		}
	}
	if _, ok := flags["--firecracker"]; ok {
		return fmt.Errorf("VM %s effective descriptor unit retained explicit Firecracker", preparation.Service)
	}
	if _, ok := flags["--jailer"]; ok {
		return fmt.Errorf("VM %s effective descriptor unit retained explicit jailer", preparation.Service)
	}
	return validateVMRuntimeAdoptionLoadedFragments(preparation, loaded)
}

func validateVMRuntimeAdoptionLoadedFragments(preparation vmRuntimeAdoptionPreparation, loaded vmRuntimeAdoptionLoadedUnit) error {
	wantFragments := make([]string, len(preparation.EffectiveUnit.Fragments))
	gotFragments := make([]string, len(loaded.Fragments))
	for i := range preparation.EffectiveUnit.Fragments {
		wantFragments[i] = preparation.EffectiveUnit.Fragments[i].Path
	}
	for i := range loaded.Fragments {
		gotFragments[i] = loaded.Fragments[i].Path
	}
	if !slicesEqualVMRuntimeAdoptionStrings(wantFragments, gotFragments) {
		return fmt.Errorf("VM %s effective unit fragment set changed during runtime adoption", preparation.Service)
	}
	return nil
}

func (tx *VMRuntimeAdoption) Summary() VMRuntimeAdoptionSummary {
	if tx == nil {
		return VMRuntimeAdoptionSummary{}
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	result := tx.summary
	result.Ready = append([]string(nil), result.Ready...)
	result.PendingRestart = append([]string(nil), result.PendingRestart...)
	result.AlreadyAdopted = append([]string(nil), result.AlreadyAdopted...)
	result.Adopting = append([]string(nil), result.Adopting...)
	result.Blocked = append([]string(nil), result.Blocked...)
	if result.BlockedReasons != nil {
		clone := make(map[string]string, len(result.BlockedReasons))
		for service, reason := range result.BlockedReasons {
			clone[service] = reason
		}
		result.BlockedReasons = clone
	}
	return result
}

func (tx *VMRuntimeAdoption) CatchRollbackSafe() bool {
	if tx == nil {
		return true
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.rollbackSafe
}

func (tx *VMRuntimeAdoption) Close() error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return nil
	}
	var abortErr error
	if !tx.commitDone && len(tx.records) != 0 {
		rollbackSafe, err := tx.finalizeRollbackSafe()
		tx.rollbackSafe = rollbackSafe
		abortErr = err
	}
	tx.closed = true
	return errors.Join(abortErr, tx.units.Close(), tx.descriptors.Close(), tx.journal.Close())
}

// RecoverVMRuntimeAdoptions waits only on the root runtime journal flock. It is
// suitable for asynchronous startup recovery and deliberately does not acquire
// the Catch installer lock.
func RecoverVMRuntimeAdoptions(ctx context.Context, cfg *Config) (retErr error) {
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return err
	}
	deps := completeVMRuntimeAdoptionCoordinatorDeps(defaultVMRuntimeAdoptionCoordinatorDeps())
	store, err := openVMRuntimeJournalStore(ctx, effectiveCfg.RootDir, deps.journal)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()
	if err := recoverVMRuntimeAdoptionsWithStore(ctx, effectiveCfg, store, deps); err != nil {
		return err
	}
	return store.CleanupCommittedTombstones()
}

func recoverVMRuntimeAdoptionsWithStore(ctx context.Context, cfg *Config, store *vmRuntimeJournalStore, deps vmRuntimeAdoptionCoordinatorDeps) error {
	groups, err := store.LoadAll()
	if err != nil {
		return err
	}
	for _, initial := range groups {
		if err := store.Resume(initial.TransactionID); err != nil {
			var pending *vmRuntimeJournalRemovalPendingError
			if !errors.As(err, &pending) {
				return err
			}
		}
		currentGroups, err := store.LoadAll()
		if err != nil {
			return err
		}
		group, ok := findVMRuntimeJournalGroup(currentGroups, initial.TransactionID)
		if !ok {
			continue
		}
		if err := recoverVMRuntimeAdoptionGroup(ctx, cfg, store, group, deps); err != nil {
			return fmt.Errorf("recover VM runtime adoption %s: %w", group.TransactionID, err)
		}
	}
	return nil
}

func findVMRuntimeJournalGroup(groups []vmRuntimeJournalGroup, id string) (vmRuntimeJournalGroup, bool) {
	for _, group := range groups {
		if group.TransactionID == id {
			return group, true
		}
	}
	return vmRuntimeJournalGroup{}, false
}

func recoverVMRuntimeAdoptionGroup(ctx context.Context, cfg *Config, store *vmRuntimeJournalStore, group vmRuntimeJournalGroup, deps vmRuntimeAdoptionCoordinatorDeps) (retErr error) {
	descriptors, err := prepareVMRuntimeAdoptionDescriptorCohort(ctx, group.Records, deps.descriptor)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, descriptors.Close()) }()
	units, err := prepareVMRuntimeJournalUnitReconciler(ctx, group.Records, deps.unit)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, units.Close()) }()

	classification, err := reconcileVMRuntimeAdoptionFromDatabase(ctx, cfg.DB, group, descriptors, units)
	if err != nil {
		return err
	}
	if err := advanceRecoveredVMRuntimeAdoptionJournal(store, group, classification, deps.now); err != nil {
		return err
	}
	if err := store.BeginRemoval(group.TransactionID); err != nil {
		var pending *vmRuntimeJournalRemovalPendingError
		if !errors.As(err, &pending) {
			return err
		}
	}
	return store.FinalizeRemovalWithLatestDataLocked(group.TransactionID, cfg.DB, classification, func([]vmRuntimeJournalRecord) error {
		return verifyRecoveredVMRuntimeAdoption(ctx, classification, descriptors, units)
	})
}

func reconcileVMRuntimeAdoptionFromDatabase(
	ctx context.Context,
	database *db.Store,
	group vmRuntimeJournalGroup,
	descriptors *vmRuntimeAdoptionDescriptorCohort,
	units *vmRuntimeJournalUnitReconciler,
) (vmRuntimeJournalDBClassification, error) {
	classification := vmRuntimeJournalDBNeither
	err := database.WithLatestDataLocked(func(view db.DataView) error {
		classification = group.ClassifyDB(view.AsStruct())
		switch classification {
		case vmRuntimeJournalDBOld:
			if err := units.ReconcileOld(ctx); err != nil {
				return err
			}
			return descriptors.restoreOld(ctx)
		case vmRuntimeJournalDBNew:
			if err := descriptors.publishNew(ctx, nil); err != nil {
				return err
			}
			return units.ReconcileNew(ctx)
		default:
			return fmt.Errorf("database is %s; refusing derived-state recovery", classification)
		}
	})
	return classification, err
}

func advanceRecoveredVMRuntimeAdoptionJournal(store *vmRuntimeJournalStore, group vmRuntimeJournalGroup, classification vmRuntimeJournalDBClassification, now func() time.Time) error {
	if classification != vmRuntimeJournalDBNew {
		return nil
	}
	if group.Records[0].State == vmRuntimeJournalStatePrepared {
		if err := transitionRecoveredVMRuntimeJournal(store, group.TransactionID, vmRuntimeJournalStateDerivedPublished, now); err != nil {
			return err
		}
	}
	return transitionRecoveredVMRuntimeJournal(store, group.TransactionID, vmRuntimeJournalStateDatabaseCommitted, now)
}

func verifyRecoveredVMRuntimeAdoption(
	ctx context.Context,
	classification vmRuntimeJournalDBClassification,
	descriptors *vmRuntimeAdoptionDescriptorCohort,
	units *vmRuntimeJournalUnitReconciler,
) error {
	if classification == vmRuntimeJournalDBOld {
		return errors.Join(units.VerifyOld(ctx), descriptors.verify(ctx, vmRuntimeDescriptorRawOld))
	}
	return errors.Join(descriptors.verify(ctx, vmRuntimeDescriptorRawNew), units.VerifyNew(ctx))
}

func transitionRecoveredVMRuntimeJournal(store *vmRuntimeJournalStore, transactionID string, target vmRuntimeJournalState, now func() time.Time) error {
	groups, err := store.LoadAll()
	if err != nil {
		return err
	}
	group, ok := findVMRuntimeJournalGroup(groups, transactionID)
	if !ok || len(group.Records) == 0 {
		return fmt.Errorf("VM runtime journal %s disappeared during recovery", transactionID)
	}
	if group.Records[0].State == target {
		return nil
	}
	at := now().UTC()
	if !at.After(group.Records[0].UpdatedAt) {
		at = group.Records[0].UpdatedAt.Add(time.Nanosecond)
	}
	return store.Transition(transactionID, target, at)
}

func runVMRuntimeAdoptionTransitionHook(deps vmRuntimeAdoptionCoordinatorDeps, state string) error {
	if deps.afterTransition == nil {
		return nil
	}
	if err := deps.afterTransition(state); err != nil {
		return fmt.Errorf("VM runtime adoption after %s: %w", state, err)
	}
	return nil
}

// WithVMRuntimeTransactionLock is the shared serialization seam for later
// writers of VM.Components, VM.Image.Kernel, runtime descriptors, or VM units.
// The callback must not call adoption/recovery recursively for the same root.
func WithVMRuntimeTransactionLock(ctx context.Context, cfg *Config, fn func() error) (retErr error) {
	if fn == nil {
		return fmt.Errorf("VM runtime transaction callback is required")
	}
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return err
	}
	return WithVMRuntimeRootLock(ctx, effectiveCfg.RootDir, fn)
}

// WithVMRuntimeRootLock serializes a root-scoped runtime writer that does not
// need database access. It still refuses to cross a retained adoption journal.
func WithVMRuntimeRootLock(ctx context.Context, dataRoot string, fn func() error) (retErr error) {
	if fn == nil {
		return fmt.Errorf("VM runtime transaction callback is required")
	}
	root, err := cleanRequiredVMRuntimeAdoptionPath("configured VM data root", dataRoot)
	if err != nil {
		return err
	}
	store, err := openVMRuntimeJournalStore(ctx, root, defaultVMRuntimeJournalStoreDeps())
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	groups, err := store.LoadAll()
	if err != nil {
		return fmt.Errorf("inspect pending VM runtime transactions: %w", err)
	}
	if len(groups) != 0 {
		return fmt.Errorf("VM runtime recovery is pending for %d transaction(s)", len(groups))
	}
	return fn()
}
