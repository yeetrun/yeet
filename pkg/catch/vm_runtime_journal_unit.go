// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type vmRuntimeJournalUnitClassification string

const (
	vmRuntimeJournalUnitOld     vmRuntimeJournalUnitClassification = "old"
	vmRuntimeJournalUnitNew     vmRuntimeJournalUnitClassification = "new"
	vmRuntimeJournalUnitMixed   vmRuntimeJournalUnitClassification = "mixed"
	vmRuntimeJournalUnitNeither vmRuntimeJournalUnitClassification = "neither"
)

type vmRuntimeJournalUnitDeps struct {
	exchangeAt        func(int, string, int, string) error
	renameNoReplaceAt func(int, string, int, string) error
	unlinkAt          func(int, string, int) error
	syncFile          func(*os.File) error
	syncDir           func(*os.File) error
	systemctl         func(...string) error
	afterRead         func(*os.File, string)
	afterSourceCheck  func(*os.File, string)
	afterPublish      func(*os.File, string, string)
	uid               uint32
	gid               uint32
}

type vmRuntimeJournalUnitBinding struct {
	service string
	path    string
	name    string
	dir     *os.File
	old     vmRuntimeJournalFile
	new     vmRuntimeJournalFile
}

type vmRuntimeJournalUnitReconciler struct {
	mu     sync.Mutex
	units  []vmRuntimeJournalUnitBinding
	dirs   []*os.File
	deps   vmRuntimeJournalUnitDeps
	closed bool
}

type vmRuntimeJournalUnitCurrent struct {
	exists bool
	raw    []byte
	id     vmJailerFileIdentity
	stat   unix.Stat_t
}

type vmRuntimeJournalUnitUncertainError struct {
	cause error
	paths []string
}

func (err *vmRuntimeJournalUnitUncertainError) Error() string {
	return fmt.Sprintf("%v; exact VM unit state is uncertain for %s", err.cause, strings.Join(err.paths, ", "))
}

func (err *vmRuntimeJournalUnitUncertainError) Unwrap() error { return err.cause }

func (err *vmRuntimeJournalUnitUncertainError) Paths() []string {
	return append([]string(nil), err.paths...)
}

type vmRuntimeJournalUnitMemberState uint8

const (
	vmRuntimeJournalUnitMemberNeither vmRuntimeJournalUnitMemberState = iota
	vmRuntimeJournalUnitMemberOld
	vmRuntimeJournalUnitMemberNew
	vmRuntimeJournalUnitMemberBoth
)

func defaultVMRuntimeJournalUnitDeps() vmRuntimeJournalUnitDeps {
	return vmRuntimeJournalUnitDeps{
		exchangeAt:        exchangeVMJailerUnitNamesAt,
		renameNoReplaceAt: renameVMJailerUnitNameNoReplaceAt,
		unlinkAt:          unix.Unlinkat,
		syncFile:          func(file *os.File) error { return file.Sync() },
		syncDir:           func(dir *os.File) error { return dir.Sync() },
		systemctl:         runVMSystemctl,
		uid:               0,
		gid:               0,
	}
}

func completeVMRuntimeJournalUnitDeps(deps vmRuntimeJournalUnitDeps) vmRuntimeJournalUnitDeps {
	defaults := defaultVMRuntimeJournalUnitDeps()
	if deps.exchangeAt == nil {
		deps.exchangeAt = defaults.exchangeAt
	}
	if deps.renameNoReplaceAt == nil {
		deps.renameNoReplaceAt = defaults.renameNoReplaceAt
	}
	if deps.unlinkAt == nil {
		deps.unlinkAt = defaults.unlinkAt
	}
	if deps.syncFile == nil {
		deps.syncFile = defaults.syncFile
	}
	if deps.syncDir == nil {
		deps.syncDir = defaults.syncDir
	}
	if deps.systemctl == nil {
		deps.systemctl = defaults.systemctl
	}
	return deps
}

func prepareVMRuntimeJournalUnitReconciler(
	ctx context.Context,
	records []vmRuntimeJournalRecord,
	deps vmRuntimeJournalUnitDeps,
) (*vmRuntimeJournalUnitReconciler, error) {
	deps = completeVMRuntimeJournalUnitDeps(deps)
	if err := validateVMRuntimeJournalCohort(records); err != nil {
		return nil, fmt.Errorf("validate VM runtime journal unit cohort: %w", err)
	}
	if err := validateVMRuntimeJournalUnitStates(records, deps); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	specs := make([]vmUnitSpec, 0, len(records))
	for _, record := range records {
		specs = append(specs, vmUnitSpec{Service: record.Service, Path: record.NewUnit.Path})
	}
	dirs, byPath, err := acquireVMUnitDirs(ctx, specs, deps.uid)
	if err != nil {
		return nil, fmt.Errorf("bind VM runtime journal unit directories: %w", err)
	}

	reconciler := &vmRuntimeJournalUnitReconciler{dirs: dirs, deps: deps}
	for _, record := range records {
		path := record.NewUnit.Path
		reconciler.units = append(reconciler.units, vmRuntimeJournalUnitBinding{
			service: record.Service,
			path:    path,
			name:    filepath.Base(path),
			dir:     byPath[filepath.Dir(path)],
			old:     cloneVMRuntimeJournalFile(record.OldUnit),
			new:     cloneVMRuntimeJournalFile(record.NewUnit),
		})
	}
	return reconciler, nil
}

func validateVMRuntimeJournalUnitStates(records []vmRuntimeJournalRecord, deps vmRuntimeJournalUnitDeps) error {
	for _, record := range records {
		if record.OldUnit.Exists && record.OldUnit.UID != deps.uid {
			return fmt.Errorf("old VM unit %s owner UID is %d, want %d", record.OldUnit.Path, record.OldUnit.UID, deps.uid)
		}
		if !record.NewUnit.Exists || record.NewUnit.UID != deps.uid || record.NewUnit.GID != deps.gid || record.NewUnit.Mode&0o7777 != 0o644 {
			return fmt.Errorf("new VM unit %s must be a %d:%d regular file with permissions 0644", record.NewUnit.Path, deps.uid, deps.gid)
		}
	}
	return nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) Classify(ctx context.Context) (vmRuntimeJournalUnitClassification, error) {
	if reconciler == nil {
		return vmRuntimeJournalUnitNeither, fmt.Errorf("VM runtime journal unit reconciler is nil")
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if err := reconciler.requireOpenLocked(); err != nil {
		return vmRuntimeJournalUnitNeither, err
	}
	classification, _, err := reconciler.classifyLocked(ctx)
	return classification, err
}

func (reconciler *vmRuntimeJournalUnitReconciler) VerifyOld(ctx context.Context) error {
	return reconciler.verify(ctx, vmRuntimeJournalUnitOld)
}

func (reconciler *vmRuntimeJournalUnitReconciler) VerifyNew(ctx context.Context) error {
	return reconciler.verify(ctx, vmRuntimeJournalUnitNew)
}

func (reconciler *vmRuntimeJournalUnitReconciler) verify(ctx context.Context, want vmRuntimeJournalUnitClassification) error {
	if reconciler == nil {
		return fmt.Errorf("VM runtime journal unit reconciler is nil")
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if err := reconciler.requireOpenLocked(); err != nil {
		return err
	}
	return reconciler.verifyLocked(ctx, want)
}

func (reconciler *vmRuntimeJournalUnitReconciler) verifyLocked(ctx context.Context, want vmRuntimeJournalUnitClassification) error {
	classification, states, err := reconciler.classifyLocked(ctx)
	if err != nil {
		return err
	}
	for i, state := range states {
		if !vmRuntimeJournalUnitMemberMatches(state, want) {
			return fmt.Errorf("VM unit %s does not match exact %s journal state; cohort is %s", reconciler.units[i].path, want, classification)
		}
	}
	return nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) ReconcileOld(ctx context.Context) error {
	return reconciler.reconcile(ctx, vmRuntimeJournalUnitOld)
}

func (reconciler *vmRuntimeJournalUnitReconciler) ReconcileNew(ctx context.Context) error {
	return reconciler.reconcile(ctx, vmRuntimeJournalUnitNew)
}

func (reconciler *vmRuntimeJournalUnitReconciler) reconcile(ctx context.Context, target vmRuntimeJournalUnitClassification) error {
	if reconciler == nil {
		return fmt.Errorf("VM runtime journal unit reconciler is nil")
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if err := reconciler.requireOpenLocked(); err != nil {
		return err
	}
	return reconciler.reconcileLocked(ctx, target)
}

func (reconciler *vmRuntimeJournalUnitReconciler) reconcileLocked(ctx context.Context, target vmRuntimeJournalUnitClassification) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	classification, states, err := reconciler.classifyLocked(ctx)
	if err != nil {
		return err
	}
	if classification == vmRuntimeJournalUnitNeither {
		return fmt.Errorf("refuse to reconcile VM unit cohort containing state outside its exact journal generations")
	}

	mutated := false
	for i := range reconciler.units {
		if vmRuntimeJournalUnitMemberMatches(states[i], target) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return reconciler.finishInterruptedLocked(err, mutated)
		}
		changed, publishErr := reconciler.publishMemberLocked(ctx, i, target)
		mutated = mutated || changed
		if publishErr != nil {
			return reconciler.finishInterruptedLocked(publishErr, mutated)
		}
	}
	return reconciler.finishTargetLocked(ctx, target)
}

func (reconciler *vmRuntimeJournalUnitReconciler) finishTargetLocked(ctx context.Context, target vmRuntimeJournalUnitClassification) error {
	if err := reconciler.syncDirsLocked(); err != nil {
		return reconciler.finishInterruptedLocked(err, true)
	}
	reloadErr := reconciler.deps.systemctl("daemon-reload")
	verifyCtx := ctx
	if ctx.Err() != nil {
		verifyCtx = context.Background()
	}
	verifyErr := reconciler.verifyLocked(verifyCtx, target)
	return errors.Join(ctx.Err(), wrapVMRuntimeJournalUnitReloadError(reloadErr, target), verifyErr)
}

func (reconciler *vmRuntimeJournalUnitReconciler) finishInterruptedLocked(cause error, mutated bool) error {
	if !mutated {
		return cause
	}
	syncErr := reconciler.syncDirsLocked()
	reloadErr := reconciler.deps.systemctl("daemon-reload")
	classification, _, verifyErr := reconciler.classifyLocked(context.Background())
	if verifyErr == nil && classification == vmRuntimeJournalUnitNeither {
		verifyErr = fmt.Errorf("VM unit cohort contains state outside its exact journal generations after interrupted reconciliation")
	}
	return errors.Join(cause, syncErr, wrapVMRuntimeJournalUnitReloadError(reloadErr, classification), verifyErr)
}

func wrapVMRuntimeJournalUnitReloadError(err error, state vmRuntimeJournalUnitClassification) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("reload systemd after publishing exact %s VM unit generation: %w", state, err)
}

func (reconciler *vmRuntimeJournalUnitReconciler) publishMemberLocked(
	ctx context.Context,
	index int,
	target vmRuntimeJournalUnitClassification,
) (bool, error) {
	unit := &reconciler.units[index]
	want, source := unit.new, unit.old
	if target == vmRuntimeJournalUnitOld {
		want, source = unit.old, unit.new
	}
	current, err := inspectVMRuntimeJournalUnitAt(ctx, unit.dir, unit.name, reconciler.deps)
	if err != nil {
		return false, fmt.Errorf("inspect VM unit %s before exact publication: %w", unit.path, err)
	}
	if matchesVMRuntimeJournalUnitFile(current, want) {
		return false, nil
	}
	if !matchesVMRuntimeJournalUnitFile(current, source) {
		return false, fmt.Errorf("refuse to publish VM unit %s: current state is outside its exact journal generations", unit.path)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if want.Exists {
		return reconciler.publishPresentLocked(ctx, unit, current, want)
	}
	return reconciler.publishAbsentLocked(ctx, unit, current, want)
}

func (reconciler *vmRuntimeJournalUnitReconciler) publishPresentLocked(
	ctx context.Context,
	unit *vmRuntimeJournalUnitBinding,
	current vmRuntimeJournalUnitCurrent,
	want vmRuntimeJournalFile,
) (bool, error) {
	temp, tempName, tempID, err := createStagedVMUnitAt(unit.dir, unit.name)
	if err != nil {
		return false, fmt.Errorf("create staged journal VM unit %s: %w", unit.path, err)
	}
	if err := writeVMRuntimeJournalUnitTemp(temp, want, reconciler.deps); err != nil {
		return false, errors.Join(
			fmt.Errorf("stage exact journal VM unit %s: %w", unit.path, err), temp.Close(),
			unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, tempID, reconciler.deps.unlinkAt),
		)
	}
	if err := temp.Close(); err != nil {
		return false, errors.Join(
			fmt.Errorf("close staged journal VM unit %s: %w", unit.path, err),
			unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, tempID, reconciler.deps.unlinkAt),
		)
	}
	if err := verifyVMRuntimeJournalUnitName(ctx, unit.dir, tempName, tempID, want, reconciler.deps); err != nil {
		return false, errors.Join(
			fmt.Errorf("verify staged journal VM unit %s: %w", unit.path, err),
			unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, tempID, reconciler.deps.unlinkAt),
		)
	}
	if err := revalidateExactVMRuntimeJournalUnitSource(ctx, unit.dir, unit.name, current, reconciler.deps); err != nil {
		return false, errors.Join(
			fmt.Errorf("VM unit %s changed before exact publication: %w", unit.path, err),
			unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, tempID, reconciler.deps.unlinkAt),
		)
	}
	if reconciler.deps.afterSourceCheck != nil {
		reconciler.deps.afterSourceCheck(unit.dir, unit.name)
	}
	if err := ctx.Err(); err != nil {
		return false, errors.Join(err, unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, tempID, reconciler.deps.unlinkAt))
	}

	dirFD := int(unit.dir.Fd())
	if !current.exists {
		renameErr := reconciler.deps.renameNoReplaceAt(dirFD, tempName, dirFD, unit.name)
		reconciler.runAfterVMRuntimeJournalUnitPublishHook(unit, tempName)
		return reconciler.classifyCreatedUnitLocked(unit, tempName, tempID, want, renameErr)
	}
	exchangeErr := reconciler.deps.exchangeAt(dirFD, tempName, dirFD, unit.name)
	return reconciler.classifyExchangedUnitLocked(unit, tempName, tempID, current, want, exchangeErr)
}

func writeVMRuntimeJournalUnitTemp(temp *os.File, state vmRuntimeJournalFile, deps vmRuntimeJournalUnitDeps) error {
	if _, err := temp.Write(state.Contents); err != nil {
		return fmt.Errorf("write staged VM unit: %w", err)
	}
	if err := temp.Chown(int(state.UID), int(state.GID)); err != nil {
		return fmt.Errorf("chown staged VM unit: %w", err)
	}
	if err := temp.Chmod(vmUnitFileMode(state.Mode)); err != nil {
		return fmt.Errorf("chmod staged VM unit: %w", err)
	}
	if err := deps.syncFile(temp); err != nil {
		return fmt.Errorf("sync staged VM unit: %w", err)
	}
	return nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) classifyCreatedUnitLocked(
	unit *vmRuntimeJournalUnitBinding,
	tempName string,
	stagedID vmJailerFileIdentity,
	want vmRuntimeJournalFile,
	publishErr error,
) (bool, error) {
	current, inspectErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, unit.name, reconciler.deps)
	if inspectErr == nil && matchesVMRuntimeJournalUnitFile(current, want) && current.id == stagedID {
		if publishErr != nil {
			return true, fmt.Errorf("publish exact journal VM unit %s reported an error after the target became visible: %w", unit.path, publishErr)
		}
		return true, nil
	}
	staged, stagedErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, tempName, reconciler.deps)
	stagedIntact := vmRuntimeJournalUnitTargetVisible(staged, stagedErr, stagedID, want)
	if publishErr == nil || !stagedIntact {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("published VM unit %s is no longer visible at its exact canonical or staged generation", unit.path), inspectErr, stagedErr, publishErr),
			unit.path, filepath.Join(filepath.Dir(unit.path), tempName),
		)
	}
	cleanupErr := unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, stagedID, reconciler.deps.unlinkAt)
	if cleanupErr != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("publish exact journal VM unit %s: %w", unit.path, publishErr), inspectErr, cleanupErr),
			unit.path, filepath.Join(filepath.Dir(unit.path), tempName),
		)
	}
	return false, errors.Join(fmt.Errorf("publish exact journal VM unit %s: %w", unit.path, publishErr), inspectErr, cleanupErr)
}

func (reconciler *vmRuntimeJournalUnitReconciler) classifyExchangedUnitLocked(
	unit *vmRuntimeJournalUnitBinding,
	tempName string,
	stagedID vmJailerFileIdentity,
	source vmRuntimeJournalUnitCurrent,
	want vmRuntimeJournalFile,
	exchangeErr error,
) (bool, error) {
	live, liveErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, unit.name, reconciler.deps)
	displaced, displacedErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, tempName, reconciler.deps)
	targetVisible := vmRuntimeJournalUnitTargetVisible(live, liveErr, stagedID, want)
	sourceDisplaced := vmRuntimeJournalUnitCurrentMatches(displaced, displacedErr, source)
	if targetVisible && sourceDisplaced {
		return reconciler.finishExpectedExchangeLocked(unit, tempName, source.id, exchangeErr)
	}
	stagedUnmoved := vmRuntimeJournalUnitTargetVisible(displaced, displacedErr, stagedID, want)
	if stagedUnmoved {
		return reconciler.finishUnchangedExchangeLocked(unit, tempName, stagedID, exchangeErr, liveErr)
	}
	if !targetVisible && vmRuntimeJournalUnitCurrentMatches(live, liveErr, source) {
		return reconciler.finishUnchangedExchangeLocked(unit, tempName, stagedID, exchangeErr, displacedErr)
	}
	return reconciler.restoreUnexpectedExchangeLocked(unit, tempName, stagedID, displaced, displacedErr, exchangeErr, liveErr)
}

func vmRuntimeJournalUnitTargetVisible(
	current vmRuntimeJournalUnitCurrent,
	err error,
	wantID vmJailerFileIdentity,
	want vmRuntimeJournalFile,
) bool {
	return err == nil && current.id == wantID && matchesVMRuntimeJournalUnitFile(current, want)
}

func vmRuntimeJournalUnitCurrentMatches(current vmRuntimeJournalUnitCurrent, err error, want vmRuntimeJournalUnitCurrent) bool {
	return err == nil && equalVMRuntimeJournalUnitCurrent(current, want)
}

func (reconciler *vmRuntimeJournalUnitReconciler) finishExpectedExchangeLocked(
	unit *vmRuntimeJournalUnitBinding,
	tempName string,
	sourceID vmJailerFileIdentity,
	exchangeErr error,
) (bool, error) {
	cleanupErr := unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, sourceID, reconciler.deps.unlinkAt)
	if cleanupErr != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(exchangeErr, fmt.Errorf("remove displaced exact source for %s: %w", unit.path, cleanupErr)),
			unit.path, filepath.Join(filepath.Dir(unit.path), tempName),
		)
	}
	if exchangeErr != nil {
		return true, fmt.Errorf("exchange exact journal VM unit %s reported an error after both generations were verified: %w", unit.path, exchangeErr)
	}
	return true, nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) finishUnchangedExchangeLocked(
	unit *vmRuntimeJournalUnitBinding,
	tempName string,
	stagedID vmJailerFileIdentity,
	exchangeErr, displacedErr error,
) (bool, error) {
	if exchangeErr == nil {
		exchangeErr = fmt.Errorf("atomic exchange did not make the staged generation visible")
	}
	cleanupErr := unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, stagedID, reconciler.deps.unlinkAt)
	if cleanupErr != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("exchange exact journal VM unit %s: %w", unit.path, exchangeErr), displacedErr, cleanupErr),
			unit.path, filepath.Join(filepath.Dir(unit.path), tempName),
		)
	}
	return false, errors.Join(fmt.Errorf("exchange exact journal VM unit %s: %w", unit.path, exchangeErr), displacedErr, cleanupErr)
}

func (reconciler *vmRuntimeJournalUnitReconciler) restoreUnexpectedExchangeLocked(
	unit *vmRuntimeJournalUnitBinding,
	tempName string,
	stagedID vmJailerFileIdentity,
	displaced vmRuntimeJournalUnitCurrent,
	displacedErr, exchangeErr, liveErr error,
) (bool, error) {
	dirFD := int(unit.dir.Fd())
	restoreErr := reconciler.deps.exchangeAt(dirFD, tempName, dirFD, unit.name)
	if restoreErr != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("restore VM unit after unexpected atomic exchange: %w", restoreErr), exchangeErr, liveErr, displacedErr),
			unit.path, filepath.Join(filepath.Dir(unit.path), tempName),
		)
	}
	live, restoredErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, unit.name, reconciler.deps)
	staged, stagedErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, tempName, reconciler.deps)
	if displacedErr != nil || restoredErr != nil || stagedErr != nil || !equalVMRuntimeJournalUnitCurrent(live, displaced) || staged.id != stagedID {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("could not prove restoration after unexpected atomic exchange"), exchangeErr, liveErr, displacedErr, restoredErr, stagedErr),
			unit.path, filepath.Join(filepath.Dir(unit.path), tempName),
		)
	}
	cleanupErr := unlinkVMJailerNameIfIdentityWith(unit.dir, tempName, stagedID, reconciler.deps.unlinkAt)
	if cleanupErr != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("VM unit %s was restored but staged cleanup failed", unit.path), exchangeErr, liveErr, cleanupErr),
			unit.path, filepath.Join(filepath.Dir(unit.path), tempName),
		)
	}
	return true, errors.Join(fmt.Errorf("VM unit %s changed after exact source validation; atomic exchange was restored", unit.path), exchangeErr, liveErr, cleanupErr)
}

func (reconciler *vmRuntimeJournalUnitReconciler) publishAbsentLocked(
	ctx context.Context,
	unit *vmRuntimeJournalUnitBinding,
	current vmRuntimeJournalUnitCurrent,
	want vmRuntimeJournalFile,
) (bool, error) {
	if !current.exists {
		return false, nil
	}
	if err := revalidateExactVMRuntimeJournalUnitSource(ctx, unit.dir, unit.name, current, reconciler.deps); err != nil {
		return false, fmt.Errorf("VM unit %s changed before exact removal: %w", unit.path, err)
	}
	quarantineName, err := reconciler.reserveVMRuntimeJournalUnitQuarantine(unit)
	if err != nil {
		return false, err
	}
	if reconciler.deps.afterSourceCheck != nil {
		reconciler.deps.afterSourceCheck(unit.dir, unit.name)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dirFD := int(unit.dir.Fd())
	renameErr := reconciler.deps.renameNoReplaceAt(dirFD, unit.name, dirFD, quarantineName)
	reconciler.runAfterVMRuntimeJournalUnitPublishHook(unit, quarantineName)
	return reconciler.classifyQuarantinedUnitLocked(unit, quarantineName, current, want, renameErr)
}

func (reconciler *vmRuntimeJournalUnitReconciler) runAfterVMRuntimeJournalUnitPublishHook(
	unit *vmRuntimeJournalUnitBinding,
	recoveryName string,
) {
	if reconciler.deps.afterPublish != nil {
		reconciler.deps.afterPublish(unit.dir, unit.name, recoveryName)
	}
}

func (reconciler *vmRuntimeJournalUnitReconciler) reserveVMRuntimeJournalUnitQuarantine(
	unit *vmRuntimeJournalUnitBinding,
) (string, error) {
	temp, name, id, err := createStagedVMUnitAt(unit.dir, unit.name)
	if err != nil {
		return "", fmt.Errorf("reserve VM unit quarantine for %s: %w", unit.path, err)
	}
	closeErr := temp.Close()
	unlinkErr := unlinkVMJailerNameIfIdentityWith(unit.dir, name, id, reconciler.deps.unlinkAt)
	if closeErr != nil || unlinkErr != nil {
		return "", errors.Join(fmt.Errorf("reserve VM unit quarantine for %s", unit.path), closeErr, unlinkErr)
	}
	return name, nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) classifyQuarantinedUnitLocked(
	unit *vmRuntimeJournalUnitBinding,
	quarantineName string,
	source vmRuntimeJournalUnitCurrent,
	want vmRuntimeJournalFile,
	renameErr error,
) (bool, error) {
	live, liveErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, unit.name, reconciler.deps)
	quarantined, quarantineErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, quarantineName, reconciler.deps)
	targetVisible := liveErr == nil && matchesVMRuntimeJournalUnitFile(live, want)
	sourceQuarantined := quarantineErr == nil && equalVMRuntimeJournalUnitCurrent(quarantined, source)
	if targetVisible && sourceQuarantined {
		return reconciler.finishQuarantinedSourceLocked(unit, quarantineName, source, renameErr)
	}
	if quarantineErr == nil && quarantined.exists {
		return reconciler.restoreUnexpectedQuarantineLocked(unit, quarantineName, quarantined, renameErr, liveErr)
	}
	if liveErr == nil && equalVMRuntimeJournalUnitCurrent(live, source) {
		if renameErr == nil {
			return true, newVMRuntimeJournalUnitUncertainError(
				fmt.Errorf("quarantined VM unit %s was restored by late activity after successful publication", unit.path),
				unit.path, filepath.Join(filepath.Dir(unit.path), quarantineName),
			)
		}
		return false, errors.Join(fmt.Errorf("quarantine exact VM unit %s: %w", unit.path, renameErr), quarantineErr)
	}
	return renameErr == nil, newVMRuntimeJournalUnitUncertainError(
		errors.Join(fmt.Errorf("quarantine exact VM unit %s left an unclassified state", unit.path), renameErr, liveErr, quarantineErr),
		unit.path, filepath.Join(filepath.Dir(unit.path), quarantineName),
	)
}

func (reconciler *vmRuntimeJournalUnitReconciler) finishQuarantinedSourceLocked(
	unit *vmRuntimeJournalUnitBinding,
	quarantineName string,
	source vmRuntimeJournalUnitCurrent,
	renameErr error,
) (bool, error) {
	quarantinePath := filepath.Join(filepath.Dir(unit.path), quarantineName)
	if err := reconciler.deps.syncDir(unit.dir); err != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(renameErr, fmt.Errorf("sync canonical absence for %s: %w", unit.path, err)), unit.path, quarantinePath,
		)
	}
	live, inspectErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, unit.name, reconciler.deps)
	if inspectErr != nil || live.exists {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(renameErr, fmt.Errorf("canonical VM unit %s reappeared before quarantine cleanup", unit.path), inspectErr), unit.path, quarantinePath,
		)
	}
	if err := unlinkVMJailerNameIfIdentityWith(unit.dir, quarantineName, source.id, reconciler.deps.unlinkAt); err != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(renameErr, fmt.Errorf("remove verified VM unit quarantine %s: %w", quarantinePath, err)), unit.path, quarantinePath,
		)
	}
	if renameErr != nil {
		return true, fmt.Errorf("quarantine exact VM unit %s reported an error after exact absence was made durable: %w", unit.path, renameErr)
	}
	return true, nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) restoreUnexpectedQuarantineLocked(
	unit *vmRuntimeJournalUnitBinding,
	quarantineName string,
	quarantined vmRuntimeJournalUnitCurrent,
	renameErr, liveErr error,
) (bool, error) {
	dirFD := int(unit.dir.Fd())
	restoreErr := reconciler.deps.renameNoReplaceAt(dirFD, quarantineName, dirFD, unit.name)
	if restoreErr != nil {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("restore unexpected quarantined VM unit for %s: %w", unit.path, restoreErr), renameErr, liveErr),
			unit.path, filepath.Join(filepath.Dir(unit.path), quarantineName),
		)
	}
	live, inspectErr := inspectVMRuntimeJournalUnitAt(context.Background(), unit.dir, unit.name, reconciler.deps)
	if inspectErr != nil || !equalVMRuntimeJournalUnitCurrent(live, quarantined) {
		return true, newVMRuntimeJournalUnitUncertainError(
			errors.Join(fmt.Errorf("could not prove restoration of unexpected quarantined VM unit for %s", unit.path), renameErr, liveErr, inspectErr), unit.path,
		)
	}
	return true, errors.Join(fmt.Errorf("VM unit %s changed after exact source validation; quarantined replacement was restored", unit.path), renameErr, liveErr)
}

func newVMRuntimeJournalUnitUncertainError(cause error, paths ...string) error {
	return &vmRuntimeJournalUnitUncertainError{cause: cause, paths: append([]string(nil), paths...)}
}

func equalVMRuntimeJournalUnitCurrent(left, right vmRuntimeJournalUnitCurrent) bool {
	if left.exists != right.exists {
		return false
	}
	if !left.exists {
		return true
	}
	return left.id == right.id && uint32(left.stat.Mode) == uint32(right.stat.Mode) && left.stat.Uid == right.stat.Uid && left.stat.Gid == right.stat.Gid && left.stat.Size == right.stat.Size && bytes.Equal(left.raw, right.raw)
}

func equalVMRuntimeJournalUnitObservedCurrent(left, right vmRuntimeJournalUnitCurrent) bool {
	return equalVMRuntimeJournalUnitCurrent(left, right) && equalVMRuntimeJournalUnitStableStat(left.stat, right.stat)
}

func verifyVMRuntimeJournalUnitName(
	ctx context.Context,
	dir *os.File,
	name string,
	wantID vmJailerFileIdentity,
	want vmRuntimeJournalFile,
	deps vmRuntimeJournalUnitDeps,
) error {
	current, err := inspectVMRuntimeJournalUnitAt(ctx, dir, name, deps)
	if err != nil {
		return err
	}
	if current.id != wantID || !matchesVMRuntimeJournalUnitFile(current, want) {
		return fmt.Errorf("staged VM unit inode or exact state changed")
	}
	return nil
}

func revalidateVMRuntimeJournalUnitSource(dir *os.File, name string, current vmRuntimeJournalUnitCurrent) error {
	if !current.exists {
		_, _, err := vmJailerNameIdentityAt(dir, name)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("previously absent VM unit appeared")
	}
	gotID, gotStat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return err
	}
	if gotID != current.id || !equalVMRuntimeJournalUnitStableStat(gotStat, current.stat) {
		return fmt.Errorf("VM unit inode or metadata changed")
	}
	return nil
}

func revalidateExactVMRuntimeJournalUnitSource(
	ctx context.Context,
	dir *os.File,
	name string,
	want vmRuntimeJournalUnitCurrent,
	deps vmRuntimeJournalUnitDeps,
) error {
	got, err := inspectVMRuntimeJournalUnitAt(ctx, dir, name, deps)
	if err != nil {
		return err
	}
	if got.exists != want.exists {
		return fmt.Errorf("VM unit existence changed")
	}
	if !want.exists {
		return nil
	}
	if !equalVMRuntimeJournalUnitObservedCurrent(got, want) {
		return fmt.Errorf("VM unit inode, metadata, or exact bytes changed")
	}
	return nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) classifyLocked(
	ctx context.Context,
) (vmRuntimeJournalUnitClassification, []vmRuntimeJournalUnitMemberState, error) {
	states := make([]vmRuntimeJournalUnitMemberState, 0, len(reconciler.units))
	allOld, allNew := true, true
	for i := range reconciler.units {
		unit := &reconciler.units[i]
		current, err := inspectVMRuntimeJournalUnitAt(ctx, unit.dir, unit.name, reconciler.deps)
		if err != nil {
			return vmRuntimeJournalUnitNeither, nil, fmt.Errorf("inspect VM unit %s: %w", unit.path, err)
		}
		oldMatch := matchesVMRuntimeJournalUnitFile(current, unit.old)
		newMatch := matchesVMRuntimeJournalUnitFile(current, unit.new)
		state := classifyVMRuntimeJournalUnitMember(oldMatch, newMatch)
		states = append(states, state)
		allOld = allOld && oldMatch
		allNew = allNew && newMatch
		if state == vmRuntimeJournalUnitMemberNeither {
			return vmRuntimeJournalUnitNeither, states, nil
		}
	}
	switch {
	case allOld:
		return vmRuntimeJournalUnitOld, states, nil
	case allNew:
		return vmRuntimeJournalUnitNew, states, nil
	default:
		return vmRuntimeJournalUnitMixed, states, nil
	}
}

func classifyVMRuntimeJournalUnitMember(oldMatch, newMatch bool) vmRuntimeJournalUnitMemberState {
	switch {
	case oldMatch && newMatch:
		return vmRuntimeJournalUnitMemberBoth
	case oldMatch:
		return vmRuntimeJournalUnitMemberOld
	case newMatch:
		return vmRuntimeJournalUnitMemberNew
	default:
		return vmRuntimeJournalUnitMemberNeither
	}
}

func vmRuntimeJournalUnitMemberMatches(state vmRuntimeJournalUnitMemberState, target vmRuntimeJournalUnitClassification) bool {
	if state == vmRuntimeJournalUnitMemberBoth {
		return true
	}
	if target == vmRuntimeJournalUnitOld {
		return state == vmRuntimeJournalUnitMemberOld
	}
	return state == vmRuntimeJournalUnitMemberNew
}

func inspectVMRuntimeJournalUnitAt(
	ctx context.Context,
	dir *os.File,
	name string,
	deps vmRuntimeJournalUnitDeps,
) (vmRuntimeJournalUnitCurrent, error) {
	if err := ctx.Err(); err != nil {
		return vmRuntimeJournalUnitCurrent{}, err
	}
	file, exists, err := openVMUnitForRead(dir, name)
	if err != nil {
		return vmRuntimeJournalUnitCurrent{}, err
	}
	if !exists {
		return vmRuntimeJournalUnitCurrent{}, nil
	}
	return readOpenVMRuntimeJournalUnit(ctx, dir, name, file, deps)
}

func readOpenVMRuntimeJournalUnit(
	ctx context.Context,
	dir *os.File,
	name string,
	file *os.File,
	deps vmRuntimeJournalUnitDeps,
) (vmRuntimeJournalUnitCurrent, error) {
	id, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return vmRuntimeJournalUnitCurrent{}, closeVMJailerFileOnError(file, err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return vmRuntimeJournalUnitCurrent{}, closeVMJailerFileOnError(file, fmt.Errorf("VM unit is not a regular file"))
	}
	raw, postReadStat, err := readStableVMRuntimeJournalUnitContents(file, dir, name, deps)
	if err != nil {
		return vmRuntimeJournalUnitCurrent{}, err
	}
	if err := ctx.Err(); err != nil {
		return vmRuntimeJournalUnitCurrent{}, err
	}
	current := vmRuntimeJournalUnitCurrent{exists: true, raw: raw, id: id, stat: postReadStat}
	if err := revalidateVMRuntimeJournalUnitSource(dir, name, current); err != nil {
		return vmRuntimeJournalUnitCurrent{}, fmt.Errorf("VM unit changed while it was inspected: %w", err)
	}
	return current, nil
}

func readStableVMRuntimeJournalUnitContents(
	file *os.File,
	dir *os.File,
	name string,
	deps vmRuntimeJournalUnitDeps,
) ([]byte, unix.Stat_t, error) {
	first, err := readVMRuntimeJournalUnitPass(file)
	if err != nil {
		return nil, unix.Stat_t{}, errors.Join(err, file.Close())
	}
	if deps.afterRead != nil {
		deps.afterRead(dir, name)
	}
	_, middle, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return nil, unix.Stat_t{}, closeVMJailerFileOnError(file, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, unix.Stat_t{}, closeVMJailerFileOnError(file, err)
	}
	second, err := readVMRuntimeJournalUnitPass(file)
	if err != nil {
		return nil, unix.Stat_t{}, errors.Join(err, file.Close())
	}
	_, after, statErr := vmJailerFileIdentityForFile(file)
	closeErr := file.Close()
	if statErr != nil || closeErr != nil {
		return nil, unix.Stat_t{}, errors.Join(statErr, closeErr)
	}
	if !bytes.Equal(first, second) || !equalVMRuntimeJournalUnitStableStat(middle, after) {
		return nil, unix.Stat_t{}, fmt.Errorf("VM unit changed between stable reads")
	}
	return second, after, nil
}

func readVMRuntimeJournalUnitPass(file *os.File) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(file, vmRuntimeJournalMaxPayload+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > vmRuntimeJournalMaxPayload {
		return nil, fmt.Errorf("VM unit exceeds %d bytes", vmRuntimeJournalMaxPayload)
	}
	return raw, nil
}

func equalVMRuntimeJournalUnitStableStat(left, right unix.Stat_t) bool {
	return uint32(left.Mode) == uint32(right.Mode) && left.Uid == right.Uid && left.Gid == right.Gid && left.Size == right.Size && left.Nlink == right.Nlink &&
		equalVMRuntimeJournalUnitStatFields(left, right, "Mtim", "Mtimespec", "Ctim", "Ctimespec", "Gen", "Flags")
}

func equalVMRuntimeJournalUnitStatFields(left, right unix.Stat_t, names ...string) bool {
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	for _, name := range names {
		leftField := leftValue.FieldByName(name)
		rightField := rightValue.FieldByName(name)
		if leftField.IsValid() && rightField.IsValid() && !reflect.DeepEqual(leftField.Interface(), rightField.Interface()) {
			return false
		}
	}
	return true
}

func matchesVMRuntimeJournalUnitFile(current vmRuntimeJournalUnitCurrent, expected vmRuntimeJournalFile) bool {
	if current.exists != expected.Exists {
		return false
	}
	if !expected.Exists {
		return true
	}
	return uint32(current.stat.Mode) == expected.Mode && current.stat.Uid == expected.UID && current.stat.Gid == expected.GID && bytes.Equal(current.raw, expected.Contents)
}

func (reconciler *vmRuntimeJournalUnitReconciler) syncDirsLocked() error {
	var retErr error
	for _, dir := range reconciler.dirs {
		if err := reconciler.deps.syncDir(dir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("sync VM runtime journal unit directory %s: %w", dir.Name(), err))
		}
	}
	return retErr
}

func (reconciler *vmRuntimeJournalUnitReconciler) requireOpenLocked() error {
	if reconciler.closed {
		return fmt.Errorf("VM runtime journal unit reconciler is closed")
	}
	return nil
}

func (reconciler *vmRuntimeJournalUnitReconciler) Close() error {
	if reconciler == nil {
		return nil
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if reconciler.closed {
		return nil
	}
	reconciler.closed = true
	return closeVMUnitDirs(reconciler.dirs)
}
