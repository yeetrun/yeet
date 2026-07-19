// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/serviceid"
	"golang.org/x/sys/unix"
)

const (
	vmRuntimeDescriptorSchemaVersion = 1
	vmRuntimeDescriptorFileName      = "vmm-runtime.json"
	vmRuntimeRunningMarkerFileName   = "vmm-runtime-running.json"
	vmRuntimeTrialResultFileName     = "vmm-runtime-trial-result.json"
	vmRuntimeDescriptorMaxSize       = 1 << 20
)

type vmRuntimeDescriptor struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Service       string                      `json:"service"`
	Configured    db.VMRuntimeArtifactConfig  `json:"configured"`
	Staged        *db.VMRuntimeArtifactConfig `json:"staged,omitempty"`
	Previous      *db.VMRuntimeArtifactConfig `json:"previous,omitempty"`
	Trial         bool                        `json:"trial"`
}

type vmRuntimeDescriptorSnapshot struct {
	Descriptor vmRuntimeDescriptor
	SHA256     string
}

type vmRuntimeDescriptorFileDeps struct {
	uid               uint32
	gid               uint32
	exchangeAt        func(int, string, int, string) error
	renameNoReplaceAt func(int, string, int, string) error
	unlinkAt          func(int, string, int) error
	syncFile          func(*os.File) error
	syncDir           func(*os.File) error
	beforePublish     func(*os.File, string) error
	afterPublish      func(*os.File, string)
}

type vmRuntimeDescriptorRawClassification string

const (
	vmRuntimeDescriptorRawOld     vmRuntimeDescriptorRawClassification = "old"
	vmRuntimeDescriptorRawNew     vmRuntimeDescriptorRawClassification = "new"
	vmRuntimeDescriptorRawNeither vmRuntimeDescriptorRawClassification = "neither"
)

type vmRuntimeDescriptorRawOutcome struct {
	Path           string
	Classification vmRuntimeDescriptorRawClassification
	RecoveryPaths  []string
}

type vmRuntimeDescriptorRawPostPublicationError struct {
	cause   error
	outcome vmRuntimeDescriptorRawOutcome
}

func (err *vmRuntimeDescriptorRawPostPublicationError) Error() string {
	return fmt.Sprintf("%v; exact VM runtime descriptor %s state is visible at %s", err.cause, err.outcome.Classification, err.outcome.Path)
}

func (err *vmRuntimeDescriptorRawPostPublicationError) Unwrap() error {
	return err.cause
}

func (err *vmRuntimeDescriptorRawPostPublicationError) Outcome() vmRuntimeDescriptorRawOutcome {
	return cloneVMRuntimeDescriptorRawOutcome(err.outcome)
}

type vmRuntimeDescriptorRawUncertainError struct {
	cause   error
	outcome vmRuntimeDescriptorRawOutcome
}

func (err *vmRuntimeDescriptorRawUncertainError) Error() string {
	return fmt.Sprintf("%v; exact VM runtime descriptor state is uncertain at %s", err.cause, err.outcome.Path)
}

func (err *vmRuntimeDescriptorRawUncertainError) Unwrap() error {
	return err.cause
}

func (err *vmRuntimeDescriptorRawUncertainError) Outcome() vmRuntimeDescriptorRawOutcome {
	return cloneVMRuntimeDescriptorRawOutcome(err.outcome)
}

func cloneVMRuntimeDescriptorRawOutcome(outcome vmRuntimeDescriptorRawOutcome) vmRuntimeDescriptorRawOutcome {
	outcome.RecoveryPaths = append([]string(nil), outcome.RecoveryPaths...)
	return outcome
}

type vmRuntimeDescriptorRawTransaction struct {
	mu               sync.Mutex
	dir              *os.File
	path             string
	name             string
	old              vmRuntimeJournalFile
	new              vmRuntimeJournalFile
	deps             vmRuntimeDescriptorFileDeps
	recoveryName     string
	recoveryIdentity vmJailerFileIdentity
	retainRecovery   bool
	closed           bool
}

type vmRuntimeDescriptorPublicationRetainedError struct {
	cause         error
	canonicalPath string
	recoveryPath  string
}

func (err *vmRuntimeDescriptorPublicationRetainedError) Error() string {
	state := fmt.Sprintf("post-publication canonical state retained at %s without rollback", err.canonicalPath)
	if err.recoveryPath != "" {
		state += fmt.Sprintf("; displaced descriptor retained for recovery at %s", err.recoveryPath)
	}
	return fmt.Sprintf("%v; %s", err.cause, state)
}

func (err *vmRuntimeDescriptorPublicationRetainedError) Unwrap() error {
	return err.cause
}

func (err *vmRuntimeDescriptorPublicationRetainedError) CanonicalPath() string {
	return err.canonicalPath
}

func (err *vmRuntimeDescriptorPublicationRetainedError) RecoveryPath() string {
	return err.recoveryPath
}

func defaultVMRuntimeDescriptorFileDeps() vmRuntimeDescriptorFileDeps {
	return vmRuntimeDescriptorFileDeps{
		uid:               0,
		gid:               0,
		exchangeAt:        exchangeVMJailerUnitNamesAt,
		renameNoReplaceAt: renameVMJailerUnitNameNoReplaceAt,
		unlinkAt:          unix.Unlinkat,
		syncFile:          func(file *os.File) error { return file.Sync() },
		syncDir:           func(dir *os.File) error { return dir.Sync() },
	}
}

func completeVMRuntimeDescriptorFileDeps(deps vmRuntimeDescriptorFileDeps) vmRuntimeDescriptorFileDeps {
	defaults := defaultVMRuntimeDescriptorFileDeps()
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
	return deps
}

func validateVMRuntimeDescriptorRawFile(file vmRuntimeJournalFile, deps vmRuntimeDescriptorFileDeps) error {
	if err := validateVMRuntimeDescriptorPath(file.Path); err != nil {
		return err
	}
	if !file.Exists {
		return validateAbsentVMRuntimeDescriptorRawFile(file)
	}
	return validatePresentVMRuntimeDescriptorRawFile(file, deps)
}

func validateAbsentVMRuntimeDescriptorRawFile(file vmRuntimeJournalFile) error {
	if len(file.Contents) != 0 || file.Mode != 0 || file.UID != 0 || file.GID != 0 || file.SHA256 != "" {
		return fmt.Errorf("absent VM runtime descriptor state contains file metadata")
	}
	return nil
}

func validatePresentVMRuntimeDescriptorRawFile(file vmRuntimeJournalFile, deps vmRuntimeDescriptorFileDeps) error {
	if len(file.Contents) > vmRuntimeDescriptorMaxSize {
		return fmt.Errorf("VM runtime descriptor raw state exceeds %d bytes", vmRuntimeDescriptorMaxSize)
	}
	if file.Mode&unix.S_IFMT != unix.S_IFREG || file.Mode&0o7777 != 0o600 {
		return fmt.Errorf("VM runtime descriptor raw state must be a regular file with permissions 0600")
	}
	if file.UID != deps.uid || file.GID != deps.gid {
		return fmt.Errorf("VM runtime descriptor raw state owner is %d:%d, want %d:%d", file.UID, file.GID, deps.uid, deps.gid)
	}
	digest := sha256.Sum256(file.Contents)
	if file.SHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("VM runtime descriptor raw state SHA-256 does not match its exact bytes")
	}
	return nil
}

func prepareVMRuntimeDescriptorRawTransaction(
	ctx context.Context,
	oldFile, newFile vmRuntimeJournalFile,
	deps vmRuntimeDescriptorFileDeps,
) (*vmRuntimeDescriptorRawTransaction, error) {
	deps = completeVMRuntimeDescriptorFileDeps(deps)
	if err := validateVMRuntimeDescriptorRawFile(oldFile, deps); err != nil {
		return nil, fmt.Errorf("validate old raw VM runtime descriptor: %w", err)
	}
	if err := validateVMRuntimeDescriptorRawFile(newFile, deps); err != nil {
		return nil, fmt.Errorf("validate new raw VM runtime descriptor: %w", err)
	}
	if !newFile.Exists {
		return nil, fmt.Errorf("new raw VM runtime descriptor must exist")
	}
	if oldFile.Path != newFile.Path {
		return nil, fmt.Errorf("old and new VM runtime descriptor paths differ")
	}
	if equalVMRuntimeDescriptorRawFiles(oldFile, newFile) {
		return nil, fmt.Errorf("old and new raw VM runtime descriptor states are identical")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(newFile.Path), deps.uid)
	if err != nil {
		return nil, fmt.Errorf("open raw VM runtime descriptor parent: %w", err)
	}
	if err := acquireVMJailerUpgradeDirLock(ctx, dir); err != nil {
		return nil, errors.Join(fmt.Errorf("lock raw VM runtime descriptor parent: %w", err), dir.Close())
	}
	return &vmRuntimeDescriptorRawTransaction{
		dir:  dir,
		path: newFile.Path,
		name: filepath.Base(newFile.Path),
		old:  cloneVMRuntimeJournalFile(oldFile),
		new:  cloneVMRuntimeJournalFile(newFile),
		deps: deps,
	}, nil
}

func cloneVMRuntimeJournalFile(file vmRuntimeJournalFile) vmRuntimeJournalFile {
	file.Contents = append([]byte(nil), file.Contents...)
	return file
}

func equalVMRuntimeDescriptorRawFiles(left, right vmRuntimeJournalFile) bool {
	return left.Path == right.Path && left.Exists == right.Exists && left.Mode == right.Mode && left.UID == right.UID && left.GID == right.GID && left.SHA256 == right.SHA256 && bytes.Equal(left.Contents, right.Contents)
}

type vmRuntimeDescriptorRawCurrent struct {
	exists bool
	raw    []byte
	id     vmJailerFileIdentity
	stat   unix.Stat_t
}

func inspectVMRuntimeDescriptorRawAt(
	ctx context.Context,
	dir *os.File,
	name string,
	maxBytes int64,
	syncFile func(*os.File) error,
) (vmRuntimeDescriptorRawCurrent, error) {
	if err := ctx.Err(); err != nil {
		return vmRuntimeDescriptorRawCurrent{}, err
	}
	file, exists, err := openVMRuntimeDescriptorRawAt(dir, name)
	if err != nil || !exists {
		return vmRuntimeDescriptorRawCurrent{}, err
	}
	current, err := readOpenVMRuntimeDescriptorRaw(file, maxBytes, syncFile)
	if err != nil {
		return vmRuntimeDescriptorRawCurrent{}, err
	}
	if err := ctx.Err(); err != nil {
		return vmRuntimeDescriptorRawCurrent{}, err
	}
	if err := revalidateVMRuntimeDescriptorRaw(dir, name, current); err != nil {
		return vmRuntimeDescriptorRawCurrent{}, err
	}
	return current, nil
}

func openVMRuntimeDescriptorRawAt(dir *os.File, name string) (*os.File, bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open raw VM runtime descriptor without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, fmt.Errorf("bind raw VM runtime descriptor file descriptor")
	}
	return file, true, nil
}

func readOpenVMRuntimeDescriptorRaw(file *os.File, maxBytes int64, syncFile func(*os.File) error) (vmRuntimeDescriptorRawCurrent, error) {
	id, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return vmRuntimeDescriptorRawCurrent{}, closeVMJailerFileOnError(file, fmt.Errorf("inspect raw VM runtime descriptor: %w", err))
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return vmRuntimeDescriptorRawCurrent{}, closeVMJailerFileOnError(file, fmt.Errorf("raw VM runtime descriptor is not a regular file"))
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if readErr == nil && len(raw) > int(maxBytes) {
		readErr = fmt.Errorf("raw VM runtime descriptor exceeds %d bytes", maxBytes)
	}
	if readErr == nil && syncFile != nil {
		readErr = syncFile(file)
	}
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return vmRuntimeDescriptorRawCurrent{}, errors.Join(readErr, closeErr)
	}
	return vmRuntimeDescriptorRawCurrent{exists: true, raw: raw, id: id, stat: stat}, nil
}

func revalidateVMRuntimeDescriptorRaw(dir *os.File, name string, current vmRuntimeDescriptorRawCurrent) error {
	gotID, gotStat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("revalidate raw VM runtime descriptor: %w", err)
	}
	if gotID != current.id || uint32(gotStat.Mode) != uint32(current.stat.Mode) || gotStat.Uid != current.stat.Uid || gotStat.Gid != current.stat.Gid || gotStat.Size != current.stat.Size {
		return fmt.Errorf("raw VM runtime descriptor changed while it was inspected")
	}
	return nil
}

func matchesVMRuntimeDescriptorRawFile(current vmRuntimeDescriptorRawCurrent, expected vmRuntimeJournalFile) bool {
	if current.exists != expected.Exists {
		return false
	}
	if !expected.Exists {
		return true
	}
	return uint32(current.stat.Mode) == expected.Mode && current.stat.Uid == expected.UID && current.stat.Gid == expected.GID && bytes.Equal(current.raw, expected.Contents)
}

func (tx *vmRuntimeDescriptorRawTransaction) classifyLocked(ctx context.Context) (vmRuntimeDescriptorRawClassification, vmRuntimeDescriptorRawCurrent, error) {
	current, err := inspectVMRuntimeDescriptorRawAt(ctx, tx.dir, tx.name, vmRuntimeDescriptorMaxSize, nil)
	if err != nil {
		return vmRuntimeDescriptorRawNeither, current, err
	}
	if matchesVMRuntimeDescriptorRawFile(current, tx.old) {
		return vmRuntimeDescriptorRawOld, current, nil
	}
	if matchesVMRuntimeDescriptorRawFile(current, tx.new) {
		return vmRuntimeDescriptorRawNew, current, nil
	}
	return vmRuntimeDescriptorRawNeither, current, nil
}

func (tx *vmRuntimeDescriptorRawTransaction) Classify(ctx context.Context) (vmRuntimeDescriptorRawClassification, error) {
	if tx == nil {
		return vmRuntimeDescriptorRawNeither, fmt.Errorf("raw VM runtime descriptor transaction is nil")
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return vmRuntimeDescriptorRawNeither, fmt.Errorf("raw VM runtime descriptor transaction is closed")
	}
	classification, _, err := tx.classifyLocked(ctx)
	return classification, err
}

func stageVMRuntimeDescriptorRawAt(
	ctx context.Context,
	dir *os.File,
	name string,
	state vmRuntimeJournalFile,
	deps vmRuntimeDescriptorFileDeps,
) (stagedName string, stagedID vmJailerFileIdentity, retErr error) {
	if err := ctx.Err(); err != nil {
		return "", vmJailerFileIdentity{}, err
	}
	temp, tempName, tempID, err := createStagedVMUnitAt(dir, name)
	if err != nil {
		return "", vmJailerFileIdentity{}, fmt.Errorf("create staged raw VM runtime descriptor: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			retErr = errors.Join(retErr, cleanupVMUnitRestorationTemp(dir, temp, tempName, tempID, deps.unlinkAt))
		}
	}()
	if err := writeStagedVMRuntimeDescriptorRaw(temp, state, deps); err != nil {
		return "", vmJailerFileIdentity{}, err
	}
	if err := temp.Close(); err != nil {
		temp = nil
		return "", vmJailerFileIdentity{}, fmt.Errorf("close staged raw VM runtime descriptor: %w", err)
	}
	temp = nil
	current, err := inspectVMRuntimeDescriptorRawAt(ctx, dir, tempName, vmRuntimeDescriptorMaxSize, nil)
	if err != nil {
		return "", vmJailerFileIdentity{}, fmt.Errorf("inspect staged raw VM runtime descriptor: %w", err)
	}
	if current.id != tempID || !matchesVMRuntimeDescriptorRawFile(current, state) {
		return "", vmJailerFileIdentity{}, fmt.Errorf("staged raw VM runtime descriptor changed before publication")
	}
	cleanup = false
	return tempName, tempID, nil
}

func writeStagedVMRuntimeDescriptorRaw(temp *os.File, state vmRuntimeJournalFile, deps vmRuntimeDescriptorFileDeps) error {
	if _, err := temp.Write(state.Contents); err != nil {
		return fmt.Errorf("write staged raw VM runtime descriptor: %w", err)
	}
	if err := temp.Chown(int(state.UID), int(state.GID)); err != nil {
		return fmt.Errorf("chown staged raw VM runtime descriptor: %w", err)
	}
	if err := temp.Chmod(os.FileMode(state.Mode & 0o7777)); err != nil {
		return fmt.Errorf("chmod staged raw VM runtime descriptor: %w", err)
	}
	if err := deps.syncFile(temp); err != nil {
		return fmt.Errorf("sync staged raw VM runtime descriptor: %w", err)
	}
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) syncVisibleLocked(ctx context.Context, classification vmRuntimeDescriptorRawClassification) error {
	expected := tx.old
	if classification == vmRuntimeDescriptorRawNew {
		expected = tx.new
	}
	current, err := inspectVMRuntimeDescriptorRawAt(ctx, tx.dir, tx.name, vmRuntimeDescriptorMaxSize, tx.deps.syncFile)
	if err != nil {
		return err
	}
	if !matchesVMRuntimeDescriptorRawFile(current, expected) {
		return fmt.Errorf("visible raw VM runtime descriptor no longer matches %s state", classification)
	}
	if err := tx.deps.syncDir(tx.dir); err != nil {
		return fmt.Errorf("sync raw VM runtime descriptor parent: %w", err)
	}
	return ctx.Err()
}

func (tx *vmRuntimeDescriptorRawTransaction) syncVisibleAfterMutationLocked(ctx context.Context, classification vmRuntimeDescriptorRawClassification) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.syncVisibleLocked(context.Background(), classification))
	}
	return tx.syncVisibleLocked(ctx, classification)
}

func (tx *vmRuntimeDescriptorRawTransaction) recoveryPathsLocked() []string {
	if tx.recoveryName == "" {
		return nil
	}
	return []string{filepath.Join(filepath.Dir(tx.path), tx.recoveryName)}
}

func (tx *vmRuntimeDescriptorRawTransaction) postPublicationLocked(cause error, classification vmRuntimeDescriptorRawClassification) error {
	tx.retainRecovery = true
	return &vmRuntimeDescriptorRawPostPublicationError{
		cause: cause,
		outcome: vmRuntimeDescriptorRawOutcome{
			Path: tx.path, Classification: classification, RecoveryPaths: tx.recoveryPathsLocked(),
		},
	}
}

func (tx *vmRuntimeDescriptorRawTransaction) classifyVisibleFailureLocked(cause error, expected vmRuntimeDescriptorRawClassification) error {
	classification, _, err := tx.classifyLocked(context.Background())
	if err == nil && classification == expected {
		return tx.postPublicationLocked(cause, expected)
	}
	return tx.uncertainLocked(errors.Join(cause, err, fmt.Errorf("visible raw VM runtime descriptor no longer verifies as exact %s state", expected)))
}

func (tx *vmRuntimeDescriptorRawTransaction) uncertainLocked(cause error) error {
	tx.retainRecovery = true
	return &vmRuntimeDescriptorRawUncertainError{
		cause: cause,
		outcome: vmRuntimeDescriptorRawOutcome{
			Path: tx.path, Classification: vmRuntimeDescriptorRawNeither, RecoveryPaths: tx.recoveryPathsLocked(),
		},
	}
}

func (tx *vmRuntimeDescriptorRawTransaction) bindRecoveryLocked(name string, id vmJailerFileIdentity) {
	tx.recoveryName = name
	tx.recoveryIdentity = id
}

func (tx *vmRuntimeDescriptorRawTransaction) cleanupRecoveryLocked() error {
	if tx.recoveryName == "" {
		return nil
	}
	if err := unlinkVMJailerNameIfIdentityWith(tx.dir, tx.recoveryName, tx.recoveryIdentity, tx.deps.unlinkAt); err != nil {
		return fmt.Errorf("remove retained raw VM runtime descriptor %s: %w", tx.recoveryName, err)
	}
	tx.recoveryName = ""
	tx.recoveryIdentity = vmJailerFileIdentity{}
	if err := tx.deps.syncDir(tx.dir); err != nil {
		return fmt.Errorf("sync retained raw VM runtime descriptor removal: %w", err)
	}
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) PublishAndVerify(ctx context.Context) error {
	if tx == nil {
		return fmt.Errorf("raw VM runtime descriptor transaction is nil")
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return fmt.Errorf("raw VM runtime descriptor transaction is closed")
	}
	classification, current, err := tx.classifyLocked(ctx)
	if err != nil {
		return tx.uncertainLocked(fmt.Errorf("classify raw VM runtime descriptor before publication: %w", err))
	}
	return tx.publishClassifiedLocked(ctx, classification, current)
}

func (tx *vmRuntimeDescriptorRawTransaction) publishClassifiedLocked(ctx context.Context, classification vmRuntimeDescriptorRawClassification, current vmRuntimeDescriptorRawCurrent) error {
	switch classification {
	case vmRuntimeDescriptorRawNew:
		return tx.resyncNewRawLocked(ctx)
	case vmRuntimeDescriptorRawNeither:
		return tx.uncertainLocked(fmt.Errorf("raw VM runtime descriptor matches neither exact old nor exact new state"))
	default:
		return tx.publishOldRawLocked(ctx, current)
	}
}

func (tx *vmRuntimeDescriptorRawTransaction) resyncNewRawLocked(ctx context.Context) error {
	if err := tx.syncVisibleLocked(ctx, vmRuntimeDescriptorRawNew); err != nil {
		return tx.classifyVisibleFailureLocked(fmt.Errorf("resync exact new raw VM runtime descriptor: %w", err), vmRuntimeDescriptorRawNew)
	}
	tx.retainRecovery = false
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) publishOldRawLocked(ctx context.Context, current vmRuntimeDescriptorRawCurrent) (retErr error) {
	stagedName, stagedID, err := stageVMRuntimeDescriptorRawAt(ctx, tx.dir, tx.name, tx.new, tx.deps)
	if err != nil {
		return err
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			retErr = errors.Join(retErr, cleanupVMRuntimeDescriptorRawStage(tx.dir, stagedName, stagedID, tx.deps))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	err = tx.publishRawStageLocked(stagedName)
	if err != nil {
		return tx.resolveRawPublishError(err, current, stagedName, stagedID, &cleanupStage)
	}
	cleanupStage = false
	if err := tx.bindPublishedOldLocked(stagedName, current); err != nil {
		return err
	}
	return tx.verifyPublishedNewLocked(ctx, stagedID)
}

func (tx *vmRuntimeDescriptorRawTransaction) publishRawStageLocked(stagedName string) error {
	if tx.old.Exists {
		return tx.deps.exchangeAt(int(tx.dir.Fd()), stagedName, int(tx.dir.Fd()), tx.name)
	}
	return tx.deps.renameNoReplaceAt(int(tx.dir.Fd()), stagedName, int(tx.dir.Fd()), tx.name)
}

func (tx *vmRuntimeDescriptorRawTransaction) resolveRawPublishError(publishErr error, current vmRuntimeDescriptorRawCurrent, stagedName string, stagedID vmJailerFileIdentity, cleanupStage *bool) error {
	cause := fmt.Errorf("publish exact raw VM runtime descriptor: %w", publishErr)
	resolved, _, inspectErr := tx.classifyLocked(context.Background())
	if resolved == vmRuntimeDescriptorRawOld {
		return cause
	}
	*cleanupStage = false
	if resolved == vmRuntimeDescriptorRawNew {
		return tx.resolveVisibleRawPublishError(cause, inspectErr, current, stagedName)
	}
	tx.bindRecoveryLocked(stagedName, stagedID)
	return tx.uncertainLocked(errors.Join(cause, inspectErr))
}

func (tx *vmRuntimeDescriptorRawTransaction) resolveVisibleRawPublishError(cause, inspectErr error, current vmRuntimeDescriptorRawCurrent, stagedName string) error {
	if err := tx.bindPublishedOldLocked(stagedName, current); err != nil {
		return tx.uncertainLocked(errors.Join(cause, inspectErr, err))
	}
	syncErr := tx.syncVisibleLocked(context.Background(), vmRuntimeDescriptorRawNew)
	return tx.classifyVisibleFailureLocked(errors.Join(cause, inspectErr, syncErr), vmRuntimeDescriptorRawNew)
}

func (tx *vmRuntimeDescriptorRawTransaction) bindPublishedOldLocked(stagedName string, current vmRuntimeDescriptorRawCurrent) error {
	if !tx.old.Exists {
		return nil
	}
	recovery, err := inspectVMRuntimeDescriptorRawAt(context.Background(), tx.dir, stagedName, vmRuntimeDescriptorMaxSize, nil)
	if err != nil || recovery.id != current.id || !matchesVMRuntimeDescriptorRawFile(recovery, tx.old) {
		tx.bindRecoveryLocked(stagedName, current.id)
		return errors.Join(err, fmt.Errorf("displaced old raw VM runtime descriptor is not exact after publication"))
	}
	tx.bindRecoveryLocked(stagedName, recovery.id)
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) verifyPublishedNewLocked(ctx context.Context, stagedID vmJailerFileIdentity) error {
	published, inspectErr := inspectVMRuntimeDescriptorRawAt(context.Background(), tx.dir, tx.name, vmRuntimeDescriptorMaxSize, nil)
	if inspectErr != nil || published.id != stagedID || !matchesVMRuntimeDescriptorRawFile(published, tx.new) {
		return tx.uncertainLocked(errors.Join(inspectErr, fmt.Errorf("published raw VM runtime descriptor is not the exact staged file")))
	}
	if err := tx.syncVisibleAfterMutationLocked(ctx, vmRuntimeDescriptorRawNew); err != nil {
		return tx.classifyVisibleFailureLocked(fmt.Errorf("sync published raw VM runtime descriptor: %w", err), vmRuntimeDescriptorRawNew)
	}
	tx.retainRecovery = false
	return nil
}

func cleanupVMRuntimeDescriptorRawStage(dir *os.File, name string, id vmJailerFileIdentity, deps vmRuntimeDescriptorFileDeps) error {
	if err := unlinkVMJailerNameIfIdentityWith(dir, name, id, deps.unlinkAt); err != nil {
		return fmt.Errorf("remove staged raw VM runtime descriptor: %w", err)
	}
	if err := deps.syncDir(dir); err != nil {
		return fmt.Errorf("sync staged raw VM runtime descriptor removal: %w", err)
	}
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) RestorePreviousAndVerify(ctx context.Context) error {
	if tx == nil {
		return fmt.Errorf("raw VM runtime descriptor transaction is nil")
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return fmt.Errorf("raw VM runtime descriptor transaction is closed")
	}
	classification, current, err := tx.classifyLocked(ctx)
	if err != nil {
		return tx.uncertainLocked(fmt.Errorf("classify raw VM runtime descriptor before restoration: %w", err))
	}
	return tx.restoreClassifiedRawLocked(ctx, classification, current)
}

func (tx *vmRuntimeDescriptorRawTransaction) restoreClassifiedRawLocked(ctx context.Context, classification vmRuntimeDescriptorRawClassification, current vmRuntimeDescriptorRawCurrent) error {
	switch classification {
	case vmRuntimeDescriptorRawNeither:
		return tx.uncertainLocked(fmt.Errorf("raw VM runtime descriptor matches neither exact old nor exact new state"))
	case vmRuntimeDescriptorRawNew:
		if err := tx.restoreNewRawLocked(ctx, current); err != nil {
			return err
		}
		return tx.finishRawRestorationLocked(ctx, true)
	default:
		return tx.finishRawRestorationLocked(ctx, false)
	}
}

func (tx *vmRuntimeDescriptorRawTransaction) restoreNewRawLocked(ctx context.Context, current vmRuntimeDescriptorRawCurrent) error {
	if tx.old.Exists {
		return tx.restoreExistingRawLocked(ctx, current)
	}
	return tx.restoreAbsentRawLocked(ctx, current)
}

func (tx *vmRuntimeDescriptorRawTransaction) finishRawRestorationLocked(ctx context.Context, mutated bool) error {
	var syncErr error
	if mutated {
		syncErr = tx.syncVisibleAfterMutationLocked(ctx, vmRuntimeDescriptorRawOld)
	} else {
		syncErr = tx.syncVisibleLocked(ctx, vmRuntimeDescriptorRawOld)
	}
	if syncErr != nil {
		return tx.classifyVisibleFailureLocked(fmt.Errorf("sync exact old raw VM runtime descriptor: %w", syncErr), vmRuntimeDescriptorRawOld)
	}
	if err := tx.cleanupRecoveryLocked(); err != nil {
		return tx.classifyVisibleFailureLocked(err, vmRuntimeDescriptorRawOld)
	}
	verified, _, err := tx.classifyLocked(ctx)
	if err != nil || verified != vmRuntimeDescriptorRawOld {
		return tx.uncertainLocked(errors.Join(err, fmt.Errorf("restored raw VM runtime descriptor did not verify as exact old state")))
	}
	tx.retainRecovery = false
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) restoreExistingRawLocked(ctx context.Context, current vmRuntimeDescriptorRawCurrent) error {
	stagedName, stagedID, err := tx.prepareOldRawStageLocked(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err = tx.deps.exchangeAt(int(tx.dir.Fd()), stagedName, int(tx.dir.Fd()), tx.name)
	if err != nil {
		return tx.resolveRawRestoreExchangeError(err, current, stagedName)
	}
	if err := tx.bindDisplacedNewRawLocked(stagedName, current, " after restoration"); err != nil {
		return tx.uncertainLocked(err)
	}
	return tx.verifyRestoredOldRawLocked(stagedID)
}

func (tx *vmRuntimeDescriptorRawTransaction) prepareOldRawStageLocked(ctx context.Context) (string, vmJailerFileIdentity, error) {
	if tx.recoveryName == "" {
		stagedName, stagedID, err := stageVMRuntimeDescriptorRawAt(ctx, tx.dir, tx.name, tx.old, tx.deps)
		if err == nil {
			tx.bindRecoveryLocked(stagedName, stagedID)
		}
		return stagedName, stagedID, err
	}
	staged, err := inspectVMRuntimeDescriptorRawAt(ctx, tx.dir, tx.recoveryName, vmRuntimeDescriptorMaxSize, nil)
	if err != nil || staged.id != tx.recoveryIdentity || !matchesVMRuntimeDescriptorRawFile(staged, tx.old) {
		return "", vmJailerFileIdentity{}, tx.uncertainLocked(errors.Join(err, fmt.Errorf("retained old raw VM runtime descriptor is not exact")))
	}
	return tx.recoveryName, tx.recoveryIdentity, nil
}

func (tx *vmRuntimeDescriptorRawTransaction) resolveRawRestoreExchangeError(exchangeErr error, current vmRuntimeDescriptorRawCurrent, stagedName string) error {
	cause := fmt.Errorf("restore exact old raw VM runtime descriptor: %w", exchangeErr)
	resolved, _, inspectErr := tx.classifyLocked(context.Background())
	switch resolved {
	case vmRuntimeDescriptorRawOld:
		return tx.resolveVisibleRawRestoreError(cause, inspectErr, current, stagedName)
	case vmRuntimeDescriptorRawNew:
		return cause
	default:
		return tx.uncertainLocked(errors.Join(cause, inspectErr))
	}
}

func (tx *vmRuntimeDescriptorRawTransaction) resolveVisibleRawRestoreError(cause, inspectErr error, current vmRuntimeDescriptorRawCurrent, stagedName string) error {
	if err := tx.bindDisplacedNewRawLocked(stagedName, current, ""); err != nil {
		return tx.uncertainLocked(errors.Join(cause, inspectErr, err))
	}
	syncErr := tx.syncVisibleLocked(context.Background(), vmRuntimeDescriptorRawOld)
	return tx.classifyVisibleFailureLocked(errors.Join(cause, inspectErr, syncErr), vmRuntimeDescriptorRawOld)
}

func (tx *vmRuntimeDescriptorRawTransaction) bindDisplacedNewRawLocked(stagedName string, current vmRuntimeDescriptorRawCurrent, suffix string) error {
	displaced, err := inspectVMRuntimeDescriptorRawAt(context.Background(), tx.dir, stagedName, vmRuntimeDescriptorMaxSize, nil)
	if err != nil || displaced.id != current.id || !matchesVMRuntimeDescriptorRawFile(displaced, tx.new) {
		return errors.Join(err, fmt.Errorf("displaced new raw VM runtime descriptor is not exact%s", suffix))
	}
	tx.bindRecoveryLocked(stagedName, displaced.id)
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) verifyRestoredOldRawLocked(stagedID vmJailerFileIdentity) error {
	restored, restoredErr := inspectVMRuntimeDescriptorRawAt(context.Background(), tx.dir, tx.name, vmRuntimeDescriptorMaxSize, nil)
	if restoredErr != nil || restored.id != stagedID || !matchesVMRuntimeDescriptorRawFile(restored, tx.old) {
		return tx.uncertainLocked(errors.Join(restoredErr, fmt.Errorf("restored old raw VM runtime descriptor is not the exact staged file")))
	}
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) restoreAbsentRawLocked(ctx context.Context, current vmRuntimeDescriptorRawCurrent) error {
	for range 128 {
		if err := ctx.Err(); err != nil {
			return err
		}
		name, err := newVMRuntimeDescriptorRawQuarantineName(tx.name)
		if err != nil {
			return err
		}
		err = tx.deps.renameNoReplaceAt(int(tx.dir.Fd()), tx.name, int(tx.dir.Fd()), name)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return tx.resolveAbsentRawRestoreError(err, current, name)
		}
		if err := tx.bindQuarantinedNewRawLocked(name, current, true); err != nil {
			return tx.uncertainLocked(err)
		}
		return nil
	}
	return fmt.Errorf("exhausted raw VM runtime descriptor quarantine names")
}

func newVMRuntimeDescriptorRawQuarantineName(name string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate raw VM runtime descriptor quarantine name: %w", err)
	}
	return "." + name + ".restore-" + hex.EncodeToString(random[:]), nil
}

func (tx *vmRuntimeDescriptorRawTransaction) resolveAbsentRawRestoreError(renameErr error, current vmRuntimeDescriptorRawCurrent, name string) error {
	cause := fmt.Errorf("quarantine new raw VM runtime descriptor: %w", renameErr)
	resolved, _, inspectErr := tx.classifyLocked(context.Background())
	switch resolved {
	case vmRuntimeDescriptorRawOld:
		return tx.resolveVisibleAbsentRawRestoreError(cause, inspectErr, current, name)
	case vmRuntimeDescriptorRawNew:
		return cause
	default:
		return tx.uncertainLocked(errors.Join(cause, inspectErr))
	}
}

func (tx *vmRuntimeDescriptorRawTransaction) resolveVisibleAbsentRawRestoreError(cause, inspectErr error, current vmRuntimeDescriptorRawCurrent, name string) error {
	if err := tx.bindQuarantinedNewRawLocked(name, current, false); err != nil {
		return tx.uncertainLocked(errors.Join(cause, inspectErr, err))
	}
	syncErr := tx.syncVisibleLocked(context.Background(), vmRuntimeDescriptorRawOld)
	return tx.classifyVisibleFailureLocked(errors.Join(cause, inspectErr, syncErr), vmRuntimeDescriptorRawOld)
}

func (tx *vmRuntimeDescriptorRawTransaction) bindQuarantinedNewRawLocked(name string, current vmRuntimeDescriptorRawCurrent, bindOnFailure bool) error {
	quarantined, err := inspectVMRuntimeDescriptorRawAt(context.Background(), tx.dir, name, vmRuntimeDescriptorMaxSize, nil)
	if err != nil || quarantined.id != current.id || !matchesVMRuntimeDescriptorRawFile(quarantined, tx.new) {
		if bindOnFailure {
			tx.bindRecoveryLocked(name, current.id)
		}
		return errors.Join(err, fmt.Errorf("quarantined new raw VM runtime descriptor is not exact"))
	}
	tx.bindRecoveryLocked(name, quarantined.id)
	return nil
}

func (tx *vmRuntimeDescriptorRawTransaction) Close() error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return nil
	}
	tx.closed = true
	var retErr error
	if !tx.retainRecovery {
		retErr = errors.Join(retErr, tx.cleanupRecoveryLocked())
	}
	retErr = errors.Join(retErr, releaseVMJailerUpgradeDirLock(tx.dir), tx.dir.Close())
	return retErr
}

// ReadVMRuntimeDescriptor reads a root-owned runtime descriptor and returns the
// configured Firecracker and jailer artifact selected for a normal launch.
func ReadVMRuntimeDescriptor(path, service string) (db.VMRuntimeArtifactConfig, error) {
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(path, service, 0, 0)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	return snapshot.Descriptor.Configured, nil
}

func readVMRuntimeDescriptorWithOwner(path, service string, uid, gid uint32) (vmRuntimeDescriptor, error) {
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(path, service, uid, gid)
	return snapshot.Descriptor, err
}

func readVMRuntimeDescriptorSnapshotWithOwner(path, service string, uid, gid uint32) (vmRuntimeDescriptorSnapshot, error) {
	if err := validateVMRuntimeDescriptorPath(path); err != nil {
		return vmRuntimeDescriptorSnapshot{}, err
	}
	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(path), uid)
	if err != nil {
		return vmRuntimeDescriptorSnapshot{}, fmt.Errorf("open VM runtime descriptor parent: %w", err)
	}
	defer func() { _ = dir.Close() }()

	name := filepath.Base(path)
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return vmRuntimeDescriptorSnapshot{}, fmt.Errorf("open VM runtime descriptor without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return vmRuntimeDescriptorSnapshot{}, fmt.Errorf("bind VM runtime descriptor file descriptor")
	}
	return readOpenVMRuntimeDescriptor(dir, file, name, service, uid, gid)
}

func readOpenVMRuntimeDescriptor(dir, file *os.File, name, service string, uid, gid uint32) (vmRuntimeDescriptorSnapshot, error) {
	id, stat, err := validateOpenVMRuntimeDescriptor(file, uid, gid)
	if err != nil {
		return vmRuntimeDescriptorSnapshot{}, closeVMJailerFileOnError(file, err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, vmRuntimeDescriptorMaxSize+1))
	closeErr := file.Close()
	if err != nil {
		return vmRuntimeDescriptorSnapshot{}, errors.Join(fmt.Errorf("read VM runtime descriptor: %w", err), closeErr)
	}
	if closeErr != nil {
		return vmRuntimeDescriptorSnapshot{}, fmt.Errorf("close VM runtime descriptor: %w", closeErr)
	}
	if len(raw) > vmRuntimeDescriptorMaxSize {
		return vmRuntimeDescriptorSnapshot{}, fmt.Errorf("VM runtime descriptor exceeds %d bytes", vmRuntimeDescriptorMaxSize)
	}
	if err := validateVMRuntimeDescriptorName(dir, name, id, stat, uid, gid); err != nil {
		return vmRuntimeDescriptorSnapshot{}, err
	}
	descriptor, err := decodeVMRuntimeDescriptor(raw, service)
	if err != nil {
		return vmRuntimeDescriptorSnapshot{}, err
	}
	return vmRuntimeDescriptorSnapshot{Descriptor: descriptor, SHA256: vmRuntimeSHA256Bytes(raw)}, nil
}

func openValidatedVMRuntimeDescriptorDir(path string, uid uint32) (*os.File, error) {
	dir, parent, _, err := openVMJailStoragePath(path)
	if err != nil {
		return nil, fmt.Errorf("open VM runtime descriptor parent without following ancestor symlinks: %w", err)
	}
	if parent != nil {
		if err := parent.Close(); err != nil {
			_ = dir.Close()
			return nil, fmt.Errorf("close VM runtime descriptor parent ancestor: %w", err)
		}
	}
	if err := validateOpenVMUnitDir(dir, uid); err != nil {
		return nil, closeVMJailerFileOnError(dir, err)
	}
	return dir, nil
}

func validateOpenVMRuntimeDescriptor(file *os.File, uid, gid uint32) (vmJailerFileIdentity, unix.Stat_t, error) {
	id, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("inspect VM runtime descriptor: %w", err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("VM runtime descriptor is not a regular file")
	}
	if stat.Uid != uid || stat.Gid != gid {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("VM runtime descriptor owner is %d:%d, want %d:%d", stat.Uid, stat.Gid, uid, gid)
	}
	if uint32(stat.Mode)&0o777 != 0o600 {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("VM runtime descriptor permissions are %o, want 0600", uint32(stat.Mode)&0o777)
	}
	return id, stat, nil
}

func validateVMRuntimeDescriptorName(dir *os.File, name string, wantID vmJailerFileIdentity, wantStat unix.Stat_t, uid, gid uint32) error {
	gotID, gotStat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("revalidate VM runtime descriptor: %w", err)
	}
	if gotID != wantID || uint32(gotStat.Mode) != uint32(wantStat.Mode) || gotStat.Uid != uid || gotStat.Gid != gid {
		return fmt.Errorf("VM runtime descriptor changed while it was read")
	}
	return nil
}

func writeVMRuntimeDescriptorWithDeps(path string, descriptor vmRuntimeDescriptor, deps vmRuntimeDescriptorFileDeps) (retErr error) {
	deps = completeVMRuntimeDescriptorFileDeps(deps)
	raw, err := encodeVMRuntimeDescriptorForWrite(path, descriptor)
	if err != nil {
		return err
	}

	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(path), deps.uid)
	if err != nil {
		return fmt.Errorf("open VM runtime descriptor parent: %w", err)
	}
	published := false
	recoveryPath := ""
	defer func() {
		retErr = cleanupVMRuntimeDescriptorDir(dir, path, recoveryPath, published, retErr)
	}()
	if err := acquireVMJailerUpgradeDirLock(context.Background(), dir); err != nil {
		return fmt.Errorf("lock VM runtime descriptor parent: %w", err)
	}

	name := filepath.Base(path)
	temp, tempName, tempID, err := createStagedVMUnitAt(dir, name)
	if err != nil {
		return fmt.Errorf("create staged VM runtime descriptor: %w", err)
	}
	tempOpen := true
	cleanupTemp := true
	defer func() {
		retErr = cleanupStagedVMRuntimeDescriptor(dir, temp, tempName, tempID, tempOpen, cleanupTemp, deps, retErr)
	}()
	stagedStat, closed, err := writeStagedVMRuntimeDescriptor(temp, raw, deps.uid, deps.gid)
	tempOpen = !closed
	if err != nil {
		return err
	}
	if err := validateVMRuntimeDescriptorName(dir, tempName, tempID, stagedStat, deps.uid, deps.gid); err != nil {
		return fmt.Errorf("validate staged VM runtime descriptor: %w", err)
	}
	existing, didPublish, shouldCleanup, recovery, err := publishStagedVMRuntimeDescriptor(dir, path, name, tempName, deps)
	published, cleanupTemp, recoveryPath = didPublish, shouldCleanup, recovery
	if err != nil {
		return err
	}
	return finalizePublishedVMRuntimeDescriptor(dir, path, name, tempName, tempID, stagedStat, recoveryPath, existing, deps)
}

func encodeVMRuntimeDescriptorForWrite(path string, descriptor vmRuntimeDescriptor) ([]byte, error) {
	if err := validateVMRuntimeDescriptorPath(path); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return nil, fmt.Errorf("encode VM runtime descriptor: %w", err)
	}
	if _, err := decodeVMRuntimeDescriptor(raw, descriptor.Service); err != nil {
		return nil, fmt.Errorf("validate VM runtime descriptor before write: %w", err)
	}
	return append(raw, '\n'), nil
}

func cleanupVMRuntimeDescriptorDir(dir *os.File, path, recoveryPath string, published bool, retErr error) error {
	cleanupErr := errors.Join(releaseVMJailerUpgradeDirLock(dir), dir.Close())
	if cleanupErr != nil && published {
		cleanupErr = retainedVMRuntimeDescriptorPublicationError(cleanupErr, path, recoveryPath)
	}
	return errors.Join(retErr, cleanupErr)
}

func cleanupStagedVMRuntimeDescriptor(dir, temp *os.File, tempName string, tempID vmJailerFileIdentity, tempOpen, cleanupTemp bool, deps vmRuntimeDescriptorFileDeps, retErr error) error {
	if tempOpen {
		retErr = errors.Join(retErr, temp.Close())
	}
	if cleanupTemp {
		retErr = errors.Join(retErr, unlinkVMJailerNameIfIdentityWith(dir, tempName, tempID, deps.unlinkAt))
	}
	return retErr
}

func writeStagedVMRuntimeDescriptor(temp *os.File, raw []byte, uid, gid uint32) (unix.Stat_t, bool, error) {
	if _, err := temp.Write(raw); err != nil {
		return unix.Stat_t{}, false, fmt.Errorf("write staged VM runtime descriptor: %w", err)
	}
	if err := temp.Chown(int(uid), int(gid)); err != nil {
		return unix.Stat_t{}, false, fmt.Errorf("chown staged VM runtime descriptor: %w", err)
	}
	if err := temp.Chmod(0o600); err != nil {
		return unix.Stat_t{}, false, fmt.Errorf("chmod staged VM runtime descriptor: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return unix.Stat_t{}, false, fmt.Errorf("sync staged VM runtime descriptor: %w", err)
	}
	_, stat, err := validateOpenVMRuntimeDescriptor(temp, uid, gid)
	if err != nil {
		return unix.Stat_t{}, false, err
	}
	if err := temp.Close(); err != nil {
		return unix.Stat_t{}, false, fmt.Errorf("close staged VM runtime descriptor: %w", err)
	}
	return stat, true, nil
}

func publishStagedVMRuntimeDescriptor(dir *os.File, path, name, tempName string, deps vmRuntimeDescriptorFileDeps) (existing vmRuntimeDescriptorExistingState, published, cleanupTemp bool, recoveryPath string, retErr error) {
	existing, err := inspectExistingVMRuntimeDescriptor(dir, name, deps.uid, deps.gid)
	if err != nil {
		return existing, false, true, "", err
	}
	if deps.beforePublish != nil {
		if err := deps.beforePublish(dir, name); err != nil {
			return existing, false, true, "", fmt.Errorf("before VM runtime descriptor publish: %w", err)
		}
	}
	if existing.existed {
		published, cleanupTemp, recoveryPath, retErr = exchangeVMRuntimeDescriptor(dir, path, name, tempName, existing, deps)
	} else {
		published, cleanupTemp, retErr = publishNewVMRuntimeDescriptor(dir, name, tempName, deps)
	}
	if published && deps.afterPublish != nil {
		deps.afterPublish(dir, name)
	}
	return existing, published, cleanupTemp, recoveryPath, retErr
}

func exchangeVMRuntimeDescriptor(dir *os.File, path, name, tempName string, existing vmRuntimeDescriptorExistingState, deps vmRuntimeDescriptorFileDeps) (bool, bool, string, error) {
	if err := deps.exchangeAt(int(dir.Fd()), tempName, int(dir.Fd()), name); err != nil {
		return false, true, "", fmt.Errorf("exchange VM runtime descriptor with current descriptor: %w", err)
	}
	recoveryPath := filepath.Join(filepath.Dir(path), tempName)
	displacedID, displacedStat, err := vmJailerNameIdentityAt(dir, tempName)
	if err != nil {
		return true, false, recoveryPath, retainedVMRuntimeDescriptorPublicationError(fmt.Errorf("inspect displaced VM runtime descriptor after exchange: %w", err), path, recoveryPath)
	}
	if displacedID != existing.id || uint32(displacedStat.Mode) != uint32(existing.stat.Mode) || displacedStat.Uid != existing.stat.Uid || displacedStat.Gid != existing.stat.Gid {
		return true, false, recoveryPath, retainedVMRuntimeDescriptorPublicationError(fmt.Errorf("concurrent VM runtime descriptor replacement detected"), path, recoveryPath)
	}
	return true, false, recoveryPath, nil
}

func publishNewVMRuntimeDescriptor(dir *os.File, name, tempName string, deps vmRuntimeDescriptorFileDeps) (bool, bool, error) {
	if err := deps.renameNoReplaceAt(int(dir.Fd()), tempName, int(dir.Fd()), name); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return false, true, fmt.Errorf("concurrent VM runtime descriptor creation detected: %w", err)
		}
		return false, true, fmt.Errorf("publish new VM runtime descriptor without replacing another writer: %w", err)
	}
	return true, false, nil
}

func finalizePublishedVMRuntimeDescriptor(dir *os.File, path, name, tempName string, tempID vmJailerFileIdentity, stagedStat unix.Stat_t, recoveryPath string, existing vmRuntimeDescriptorExistingState, deps vmRuntimeDescriptorFileDeps) error {
	if err := validateVMRuntimeDescriptorName(dir, name, tempID, stagedStat, deps.uid, deps.gid); err != nil {
		cause := fmt.Errorf("validate published VM runtime descriptor: %w", err)
		return retainedVMRuntimeDescriptorPublicationError(cause, path, recoveryPath)
	}
	if err := deps.syncDir(dir); err != nil {
		cause := fmt.Errorf("sync VM runtime descriptor parent: %w", err)
		return retainedVMRuntimeDescriptorPublicationError(cause, path, recoveryPath)
	}
	if existing.existed {
		if err := unlinkVMJailerNameIfIdentityWith(dir, tempName, existing.id, deps.unlinkAt); err != nil {
			return retainedVMRuntimeDescriptorPublicationError(fmt.Errorf("remove displaced VM runtime descriptor: %w", err), path, recoveryPath)
		}
		recoveryPath = ""
		if err := deps.syncDir(dir); err != nil {
			return retainedVMRuntimeDescriptorPublicationError(fmt.Errorf("sync removal of displaced VM runtime descriptor: %w", err), path, recoveryPath)
		}
	}
	return nil
}

type vmRuntimeDescriptorExistingState struct {
	existed bool
	id      vmJailerFileIdentity
	stat    unix.Stat_t
}

func inspectExistingVMRuntimeDescriptor(dir *os.File, name string, uid, gid uint32) (vmRuntimeDescriptorExistingState, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return vmRuntimeDescriptorExistingState{}, nil
	}
	if err != nil {
		return vmRuntimeDescriptorExistingState{}, fmt.Errorf("open existing VM runtime descriptor without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return vmRuntimeDescriptorExistingState{}, fmt.Errorf("bind existing VM runtime descriptor file descriptor")
	}
	id, stat, validateErr := validateOpenVMRuntimeDescriptor(file, uid, gid)
	if validateErr == nil {
		validateErr = validateVMRuntimeDescriptorName(dir, name, id, stat, uid, gid)
	}
	if closeErr := file.Close(); closeErr != nil {
		validateErr = errors.Join(validateErr, fmt.Errorf("close existing VM runtime descriptor: %w", closeErr))
	}
	if validateErr != nil {
		return vmRuntimeDescriptorExistingState{}, validateErr
	}
	return vmRuntimeDescriptorExistingState{existed: true, id: id, stat: stat}, nil
}

func retainedVMRuntimeDescriptorPublicationError(cause error, canonicalPath, recoveryPath string) error {
	return &vmRuntimeDescriptorPublicationRetainedError{
		cause:         cause,
		canonicalPath: canonicalPath,
		recoveryPath:  recoveryPath,
	}
}

func validateVMRuntimeDescriptorPath(path string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("VM runtime descriptor path must be absolute")
	}
	if filepath.Clean(path) != path || filepath.Base(path) != vmRuntimeDescriptorFileName {
		return fmt.Errorf("VM runtime descriptor path must be clean and end in %s", vmRuntimeDescriptorFileName)
	}
	return nil
}

// ValidateVMRuntimeLaunchPaths binds descriptor-mode launch state to the
// selected VM's data and run directories instead of accepting arbitrary host
// paths from a systemd unit.
func ValidateVMRuntimeLaunchPaths(serviceRoot, descriptorPath, runningMarkerPath, trialResultPath string) error {
	if strings.TrimSpace(serviceRoot) == "" || !filepath.IsAbs(serviceRoot) || filepath.Clean(serviceRoot) != serviceRoot {
		return fmt.Errorf("VM runtime service root must be clean and absolute: %s", serviceRoot)
	}
	expected := []struct {
		label string
		got   string
		want  string
	}{
		{label: "descriptor", got: descriptorPath, want: filepath.Join(serviceDataDirForRoot(serviceRoot), vmRuntimeDescriptorFileName)},
		{label: "running marker", got: runningMarkerPath, want: filepath.Join(serviceRunDirForRoot(serviceRoot), vmRuntimeRunningMarkerFileName)},
		{label: "trial result", got: trialResultPath, want: filepath.Join(serviceRunDirForRoot(serviceRoot), vmRuntimeTrialResultFileName)},
	}
	for _, path := range expected {
		if strings.TrimSpace(path.got) == "" || !filepath.IsAbs(path.got) || filepath.Clean(path.got) != path.got || path.got != path.want {
			return fmt.Errorf("VM runtime %s path must be exactly %s", path.label, path.want)
		}
	}
	return nil
}

func decodeVMRuntimeDescriptor(raw []byte, expectedService string) (vmRuntimeDescriptor, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return vmRuntimeDescriptor{}, fmt.Errorf("decode VM runtime descriptor: %w", err)
	}
	if err := validateVMRuntimeDescriptorJSONFields(fields); err != nil {
		return vmRuntimeDescriptor{}, err
	}
	descriptor, err := decodeVMRuntimeDescriptorStrict(raw)
	if err != nil {
		return vmRuntimeDescriptor{}, err
	}
	if err := validateDecodedVMRuntimeDescriptor(descriptor, expectedService); err != nil {
		return vmRuntimeDescriptor{}, err
	}
	return descriptor, nil
}

func validateVMRuntimeDescriptorJSONFields(fields map[string]json.RawMessage) error {
	for _, required := range []string{"schemaVersion", "service", "configured", "trial"} {
		value, ok := fields[required]
		if !ok {
			return fmt.Errorf("VM runtime descriptor missing required field %q", required)
		}
		if required == "configured" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("VM runtime descriptor configured artifact must not be null")
		}
	}
	var trial bool
	if bytes.Equal(bytes.TrimSpace(fields["trial"]), []byte("null")) || json.Unmarshal(fields["trial"], &trial) != nil {
		return fmt.Errorf("VM runtime descriptor trial must be a boolean")
	}
	for name := range fields {
		switch name {
		case "schemaVersion", "service", "configured", "staged", "previous", "trial":
		default:
			return fmt.Errorf("VM runtime descriptor contains unknown field %q", name)
		}
	}
	return validateVMRuntimeDescriptorArtifactJSONFields(fields)
}

func validateVMRuntimeDescriptorArtifactJSONFields(fields map[string]json.RawMessage) error {
	for _, name := range []string{"configured", "staged", "previous"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		if err := validateVMRuntimeArtifactFields(value, name); err != nil {
			return err
		}
	}
	return nil
}

func decodeVMRuntimeDescriptorStrict(raw []byte) (vmRuntimeDescriptor, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var descriptor vmRuntimeDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return vmRuntimeDescriptor{}, fmt.Errorf("decode VM runtime descriptor: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return vmRuntimeDescriptor{}, err
	}
	return descriptor, nil
}

func validateDecodedVMRuntimeDescriptor(descriptor vmRuntimeDescriptor, expectedService string) error {
	if descriptor.SchemaVersion != vmRuntimeDescriptorSchemaVersion {
		return fmt.Errorf("unsupported VM runtime descriptor schemaVersion %d", descriptor.SchemaVersion)
	}
	if err := serviceid.Validate(descriptor.Service); err != nil {
		return fmt.Errorf("VM runtime descriptor service: %w", err)
	}
	if descriptor.Service != expectedService {
		return fmt.Errorf("VM runtime descriptor service %q does not match %q", descriptor.Service, expectedService)
	}
	if err := validateVMRuntimeArtifact(descriptor.Configured, "configured"); err != nil {
		return err
	}
	if descriptor.Staged != nil {
		if err := validateVMRuntimeArtifact(*descriptor.Staged, "staged"); err != nil {
			return err
		}
	}
	if descriptor.Previous != nil {
		if err := validateVMRuntimeArtifact(*descriptor.Previous, "previous"); err != nil {
			return err
		}
	}
	if descriptor.Trial != (descriptor.Staged != nil) {
		return fmt.Errorf("VM runtime descriptor trial must match staged artifact presence")
	}
	return nil
}

func validateVMRuntimeArtifactFields(raw json.RawMessage, label string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("decode VM runtime descriptor %s artifact: %w", label, err)
	}
	required := []string{"ID", "ManifestSHA256", "FirecrackerSHA256", "JailerSHA256", "Firecracker", "Jailer", "Source"}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("VM runtime descriptor %s artifact missing required field %q", label, name)
		}
	}
	var manifestSHA256 string
	manifestRaw := fields["ManifestSHA256"]
	if bytes.Equal(bytes.TrimSpace(manifestRaw), []byte("null")) || json.Unmarshal(manifestRaw, &manifestSHA256) != nil {
		return fmt.Errorf("VM runtime descriptor %s artifact ManifestSHA256 must be a string", label)
	}
	for name := range fields {
		found := false
		for _, allowed := range required {
			if name == allowed {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("VM runtime descriptor %s artifact contains unknown field %q", label, name)
		}
	}
	return nil
}

func validateVMRuntimeArtifact(artifact db.VMRuntimeArtifactConfig, label string) error {
	if strings.TrimSpace(artifact.ID) == "" {
		return fmt.Errorf("VM runtime descriptor %s ID is required", label)
	}
	if err := validateVMRuntimeArtifactDigests(artifact, label); err != nil {
		return err
	}
	if err := validateVMRuntimeArtifactPaths(artifact, label); err != nil {
		return err
	}
	if strings.TrimSpace(artifact.Source) == "" {
		return fmt.Errorf("VM runtime descriptor %s Source is required", label)
	}
	return nil
}

func validateVMRuntimeArtifactDigests(artifact db.VMRuntimeArtifactConfig, label string) error {
	if artifact.ManifestSHA256 == "" {
		switch artifact.Source {
		case "official-legacy", "custom-legacy", "local-legacy":
		default:
			return fmt.Errorf("VM runtime descriptor %s ManifestSHA256 is required for source %q", label, artifact.Source)
		}
	} else if !isLowerSHA256(artifact.ManifestSHA256) {
		return fmt.Errorf("VM runtime descriptor %s ManifestSHA256 must be a lowercase SHA-256", label)
	}
	if !isLowerSHA256(artifact.FirecrackerSHA256) {
		return fmt.Errorf("VM runtime descriptor %s FirecrackerSHA256 must be a lowercase SHA-256", label)
	}
	if !isLowerSHA256(artifact.JailerSHA256) {
		return fmt.Errorf("VM runtime descriptor %s JailerSHA256 must be a lowercase SHA-256", label)
	}
	return nil
}

func validateVMRuntimeArtifactPaths(artifact db.VMRuntimeArtifactConfig, label string) error {
	for name, path := range map[string]string{"Firecracker": artifact.Firecracker, "Jailer": artifact.Jailer} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("VM runtime descriptor %s %s path must be absolute", label, name)
		}
		if filepath.Clean(path) != path || filepath.Base(path) != strings.ToLower(name) {
			return fmt.Errorf("VM runtime descriptor %s %s path must be clean and end in %s", label, name, strings.ToLower(name))
		}
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("VM runtime descriptor contains trailing JSON values")
		}
		return fmt.Errorf("decode trailing VM runtime descriptor data: %w", err)
	}
	return nil
}
