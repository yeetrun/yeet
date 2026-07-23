// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type vmUnitSpec struct {
	Service string
	Path    string
	Content []byte
}

type vmUnitReplacement struct {
	Service      string
	Path         string
	Staged       string
	Previous     []byte
	Installed    []byte
	Existed      bool
	dir          *os.File
	liveName     string
	stagedName   string
	previousID   vmJailerFileIdentity
	previousMode uint32
	previousUID  uint32
	previousGID  uint32
	stagedID     vmJailerFileIdentity
	stagedMode   uint32
	installedID  vmJailerFileIdentity
}

type vmUnitTransaction struct {
	units           []vmUnitReplacement
	unitDirs        []*os.File
	deps            vmUnitTransactionDeps
	closed          bool
	commitMu        sync.Mutex
	commitAttempted bool
	commitErr       error
}

type vmUnitTransactionDeps struct {
	renameAt          func(int, string, int, string) error
	exchangeAt        func(int, string, int, string) error
	renameNoReplaceAt func(int, string, int, string) error
	restoreUnitAt     func(*os.File, string, vmJailerFileIdentity, []byte, os.FileMode, uint32, uint32, func(int, string, int, string) error, func(int, string, int) error) error
	unlinkAt          func(int, string, int) error
	syncDir           func(*os.File) error
	systemctl         func(...string) error
	afterRead         func(*os.File, string)
	unitUID           uint32
	unitGID           uint32
}

// ErrVMUnitRestorationUncertain marks a VM unit transaction whose exact prior
// state could not be proven after compensating restoration.
var ErrVMUnitRestorationUncertain = errors.New("VM unit restoration is uncertain")

// vmUnitRestorationUncertainError means a compensating restoration could not
// prove both exact prior files and a successful daemon-reload. Callers must
// retain their recovery journal and must not roll Catch back.
type vmUnitRestorationUncertainError struct {
	cause error
	paths []string
}

func (err *vmUnitRestorationUncertainError) Error() string {
	return fmt.Sprintf("%v; exact prior VM unit restoration is uncertain for %s", err.cause, strings.Join(err.paths, ", "))
}

func (err *vmUnitRestorationUncertainError) Unwrap() error {
	return err.cause
}

func (err *vmUnitRestorationUncertainError) Is(target error) bool {
	return target == ErrVMUnitRestorationUncertain
}

func (err *vmUnitRestorationUncertainError) Paths() []string {
	return append([]string(nil), err.paths...)
}

func prepareVMUnitTransaction(ctx context.Context, specs []vmUnitSpec, deps vmUnitTransactionDeps) (*vmUnitTransaction, error) {
	deps = completeVMUnitTransactionDeps(deps)
	dirs, byPath, err := acquireVMUnitDirs(ctx, specs, deps.unitUID)
	if err != nil {
		return nil, err
	}
	tx := &vmUnitTransaction{deps: deps, unitDirs: dirs}
	for _, spec := range specs {
		replacement, err := stageVMUnitAt(byPath[filepath.Dir(spec.Path)], spec, deps)
		if err != nil {
			return nil, errors.Join(err, tx.Close())
		}
		tx.units = append(tx.units, replacement)
	}
	return tx, nil
}

func acquireVMUnitDirs(ctx context.Context, specs []vmUnitSpec, trustedUID uint32) ([]*os.File, map[string]*os.File, error) {
	paths := make([]string, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		path := filepath.Dir(spec.Path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	dirs := make([]*os.File, 0, len(paths))
	byPath := make(map[string]*os.File, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, errors.Join(err, closeVMUnitDirs(dirs))
		}
		dir, err := openValidatedVMUnitDir(path, trustedUID)
		if err != nil {
			return nil, nil, errors.Join(err, closeVMUnitDirs(dirs))
		}
		if err := acquireVMJailerUpgradeDirLock(ctx, dir); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("lock VM unit directory %s: %w", path, err), dir.Close(), closeVMUnitDirs(dirs))
		}
		dirs = append(dirs, dir)
		byPath[path] = dir
	}
	return dirs, byPath, nil
}

func closeVMUnitDirs(dirs []*os.File) error {
	var retErr error
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		retErr = errors.Join(retErr, releaseVMJailerUpgradeDirLock(dir), dir.Close())
	}
	return retErr
}

func stageVMUnit(spec vmUnitSpec, deps vmUnitTransactionDeps) (replacement vmUnitReplacement, retErr error) {
	deps = completeVMUnitTransactionDeps(deps)
	replacement = vmUnitReplacement{Service: spec.Service, Path: spec.Path, Installed: append([]byte(nil), spec.Content...)}
	dir, err := openValidatedVMUnitDir(filepath.Dir(spec.Path), deps.unitUID)
	if err != nil {
		return replacement, err
	}
	defer func() {
		retErr = closeVMUnitStagingDir(dir, replacement.Staged, os.Remove, retErr)
	}()
	raw, existed, err := readVMUnitAt(dir, filepath.Base(spec.Path), deps.unitUID)
	if err != nil {
		return replacement, err
	}
	if existed {
		replacement.Previous = raw
		replacement.Existed = true
	}
	staged, stagedID, stagedMode, err := prepareStagedVMUnitAt(dir, spec, deps)
	if err != nil {
		return replacement, err
	}
	replacement.Staged = staged
	replacement.stagedID = stagedID
	replacement.stagedMode = stagedMode
	return replacement, nil
}

func stageVMUnitAt(dir *os.File, spec vmUnitSpec, deps vmUnitTransactionDeps) (vmUnitReplacement, error) {
	if dir == nil {
		return vmUnitReplacement{}, fmt.Errorf("bound VM unit directory is required for %s", spec.Path)
	}
	replacement := vmUnitReplacement{
		Service: spec.Service, Path: spec.Path, Installed: append([]byte(nil), spec.Content...), dir: dir, liveName: filepath.Base(spec.Path),
	}
	raw, existed, id, stat, err := readVMUnitStateAt(dir, replacement.liveName, deps.unitUID)
	if err != nil {
		return replacement, err
	}
	if existed {
		replacement.Previous = raw
		replacement.Existed = true
		replacement.previousID = id
		replacement.previousMode = uint32(stat.Mode)
		replacement.previousUID = stat.Uid
		replacement.previousGID = stat.Gid
	}
	staged, stagedID, stagedMode, err := prepareStagedVMUnitAt(dir, spec, deps)
	if err != nil {
		return replacement, err
	}
	replacement.Staged = staged
	replacement.stagedName = filepath.Base(staged)
	replacement.stagedID = stagedID
	replacement.stagedMode = stagedMode
	return replacement, nil
}

func closeVMUnitStagingDir(dir *os.File, staged string, remove func(string) error, cause error) error {
	if closeErr := dir.Close(); closeErr != nil {
		cause = errors.Join(cause, fmt.Errorf("close VM unit directory: %w", closeErr))
		if staged != "" {
			cause = errors.Join(cause, remove(staged))
		}
	}
	return cause
}

func prepareStagedVMUnitAt(dir *os.File, spec vmUnitSpec, deps vmUnitTransactionDeps) (string, vmJailerFileIdentity, uint32, error) {
	name := filepath.Base(spec.Path)
	temp, tempName, tempID, err := createStagedVMUnitAt(dir, name)
	if err != nil {
		return "", vmJailerFileIdentity{}, 0, fmt.Errorf("create staged VM unit %s: %w", spec.Path, err)
	}
	stat, err := writeAndSecureStagedVMUnit(temp, spec, deps, tempID)
	if err != nil {
		return "", vmJailerFileIdentity{}, 0, cleanupFailedStagedVMUnit(dir, temp, tempName, tempID, err)
	}
	if err := temp.Close(); err != nil {
		cause := fmt.Errorf("close staged VM unit %s: %w", spec.Path, err)
		cause = errors.Join(cause, unlinkVMJailerNameIfIdentity(dir, tempName, tempID))
		return "", vmJailerFileIdentity{}, 0, cause
	}
	return filepath.Join(filepath.Dir(spec.Path), tempName), tempID, uint32(stat.Mode), nil
}

func writeAndSecureStagedVMUnit(temp *os.File, spec vmUnitSpec, deps vmUnitTransactionDeps, tempID vmJailerFileIdentity) (unix.Stat_t, error) {
	if _, err := temp.Write(spec.Content); err != nil {
		return unix.Stat_t{}, fmt.Errorf("write staged VM unit %s: %w", spec.Path, err)
	}
	if err := temp.Chown(int(deps.unitUID), int(deps.unitGID)); err != nil {
		return unix.Stat_t{}, fmt.Errorf("chown staged VM unit %s: %w", spec.Path, err)
	}
	if err := temp.Chmod(0o644); err != nil {
		return unix.Stat_t{}, fmt.Errorf("chmod staged VM unit %s: %w", spec.Path, err)
	}
	if err := temp.Sync(); err != nil {
		return unix.Stat_t{}, fmt.Errorf("sync staged VM unit %s: %w", spec.Path, err)
	}
	return validateStagedVMUnitFile(temp, tempID, deps.unitUID, deps.unitGID)
}

func cleanupFailedStagedVMUnit(dir, temp *os.File, tempName string, tempID vmJailerFileIdentity, cause error) error {
	cleanupErr := unlinkVMJailerNameIfIdentity(dir, tempName, tempID)
	closeErr := temp.Close()
	return errors.Join(cause, cleanupErr, closeErr)
}

func openValidatedVMUnitDir(path string, trustedUID uint32) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open VM unit directory without following symlinks: %w", err)
	}
	dir := os.NewFile(uintptr(fd), path)
	if dir == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind VM unit directory file descriptor")
	}
	if err := validateOpenVMUnitDir(dir, trustedUID); err != nil {
		return nil, closeVMJailerFileOnError(dir, err)
	}
	return dir, nil
}

func validateOpenVMUnitDir(dir *os.File, trustedUID uint32) error {
	_, stat, err := vmJailerFileIdentityForFile(dir)
	if err != nil {
		return fmt.Errorf("inspect VM unit directory: %w", err)
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("VM unit directory is not a directory")
	}
	if stat.Uid != trustedUID {
		return fmt.Errorf("VM unit directory owner is %d, want %d", stat.Uid, trustedUID)
	}
	if mode&0o022 != 0 {
		return fmt.Errorf("VM unit directory is writable by group or others: mode %o", mode&0o7777)
	}
	return nil
}

func readVMUnitAt(dir *os.File, name string, trustedUID uint32) ([]byte, bool, error) {
	raw, existed, _, _, err := readVMUnitStateAt(dir, name, trustedUID)
	return raw, existed, err
}

func readVMUnitStateAt(dir *os.File, name string, trustedUID uint32) ([]byte, bool, vmJailerFileIdentity, unix.Stat_t, error) {
	return readVMUnitStateAtWithHook(dir, name, trustedUID, nil)
}

func readVMUnitStateAtWithHook(dir *os.File, name string, trustedUID uint32, afterRead func(*os.File, string)) ([]byte, bool, vmJailerFileIdentity, unix.Stat_t, error) {
	file, existed, err := openVMUnitForRead(dir, name)
	if err != nil || !existed {
		return nil, existed, vmJailerFileIdentity{}, unix.Stat_t{}, err
	}
	raw, id, stat, err := readOpenVMUnit(file, trustedUID, func() {
		if afterRead != nil {
			afterRead(dir, name)
		}
	})
	if err != nil {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, err
	}
	if err := revalidateReadVMUnit(dir, name, id, stat); err != nil {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, err
	}
	return raw, true, id, stat, nil
}

func openVMUnitForRead(dir *os.File, name string) (*os.File, bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open VM unit without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, fmt.Errorf("bind VM unit file descriptor")
	}
	return file, true, nil
}

func readOpenVMUnit(file *os.File, trustedUID uint32, afterRead func()) ([]byte, vmJailerFileIdentity, unix.Stat_t, error) {
	id, stat, statErr := vmJailerFileIdentityForFile(file)
	if statErr != nil {
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, closeVMJailerFileOnError(file, fmt.Errorf("inspect VM unit: %w", statErr))
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, closeVMJailerFileOnError(file, fmt.Errorf("VM unit is not a regular file"))
	}
	if stat.Uid != trustedUID {
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, closeVMJailerFileOnError(file, fmt.Errorf("VM unit owner is %d, want %d", stat.Uid, trustedUID))
	}
	raw, readErr := io.ReadAll(file)
	if readErr == nil && afterRead != nil {
		afterRead()
	}
	closeErr := file.Close()
	if readErr != nil {
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, errors.Join(fmt.Errorf("read VM unit: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("close VM unit: %w", closeErr)
	}
	return raw, id, stat, nil
}

func revalidateReadVMUnit(dir *os.File, name string, id vmJailerFileIdentity, stat unix.Stat_t) error {
	gotID, gotStat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("revalidate VM unit after read: %w", err)
	}
	if gotID != id || uint32(gotStat.Mode) != uint32(stat.Mode) || gotStat.Uid != stat.Uid || gotStat.Gid != stat.Gid || gotStat.Size != stat.Size {
		return fmt.Errorf("VM unit changed while it was read")
	}
	return nil
}

func createStagedVMUnitAt(dir *os.File, unitName string) (*os.File, string, vmJailerFileIdentity, error) {
	for range 128 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", vmJailerFileIdentity{}, fmt.Errorf("generate staged VM unit name: %w", err)
		}
		name := "." + unitName + ".unit-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", vmJailerFileIdentity{}, err
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(dir.Fd()), name, 0)
			return nil, "", vmJailerFileIdentity{}, fmt.Errorf("bind staged VM unit file descriptor")
		}
		id, _, err := vmJailerFileIdentityForFile(file)
		if err != nil {
			cause := closeVMJailerFileOnError(file, err)
			return nil, "", vmJailerFileIdentity{}, errors.Join(cause, unix.Unlinkat(int(dir.Fd()), name, 0))
		}
		return file, name, id, nil
	}
	return nil, "", vmJailerFileIdentity{}, fmt.Errorf("create staged VM unit: exhausted unique names")
}

func validateStagedVMUnitFile(file *os.File, want vmJailerFileIdentity, uid, gid uint32) (unix.Stat_t, error) {
	got, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return unix.Stat_t{}, fmt.Errorf("inspect staged VM unit: %w", err)
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return unix.Stat_t{}, fmt.Errorf("staged VM unit inode changed or is not a regular file")
	}
	if stat.Uid != uid || stat.Gid != gid {
		return unix.Stat_t{}, fmt.Errorf("staged VM unit owner is %d:%d, want %d:%d", stat.Uid, stat.Gid, uid, gid)
	}
	if uint32(stat.Mode)&0o777 != 0o644 {
		return unix.Stat_t{}, fmt.Errorf("staged VM unit permissions are %o, want 0644", uint32(stat.Mode)&0o777)
	}
	return stat, nil
}

func restoreVMUnitAt(
	dir *os.File,
	name string,
	installedID vmJailerFileIdentity,
	contents []byte,
	mode os.FileMode,
	uid, gid uint32,
	exchangeAt func(int, string, int, string) error,
	unlinkAt func(int, string, int) error,
) (retErr error) {
	temp, tempName, tempID, err := createStagedVMUnitAt(dir, name)
	if err != nil {
		return fmt.Errorf("create restoration file: %w", err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			retErr = errors.Join(retErr, cleanupVMUnitRestorationTemp(dir, temp, tempName, tempID, unlinkAt))
		}
	}()
	if err := writeVMUnitRestorationTemp(temp, contents, mode, uid, gid); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close restoration file: %w", err)
	}
	temp = nil
	if err := validateVMUnitRestorationTemp(dir, tempName, tempID, mode, uid, gid); err != nil {
		return err
	}
	cleanupTemp, retErr = finishVMUnitRestoration(dir, tempName, name, installedID, exchangeAt, unlinkAt)
	return retErr
}

func finishVMUnitRestoration(
	dir *os.File,
	tempName, liveName string,
	installedID vmJailerFileIdentity,
	exchangeAt func(int, string, int, string) error,
	unlinkAt func(int, string, int) error,
) (bool, error) {
	if err := exchangeAt(int(dir.Fd()), tempName, int(dir.Fd()), liveName); err != nil {
		return true, fmt.Errorf("exchange restoration file with live VM unit: %w", err)
	}
	displacedID, _, displacedErr := vmJailerNameIdentityAt(dir, tempName)
	if displacedErr == nil && displacedID == installedID {
		return false, unlinkVMJailerNameIfIdentityWith(dir, tempName, installedID, unlinkAt)
	}
	if displacedErr == nil {
		displacedErr = fmt.Errorf("inode changed")
	}
	refusal := fmt.Errorf("live VM unit changed after install; refusing rollback: %w", displacedErr)
	if err := exchangeAt(int(dir.Fd()), tempName, int(dir.Fd()), liveName); err != nil {
		return false, errors.Join(refusal, fmt.Errorf("exchange concurrent VM unit back into place: %w", err))
	}
	return true, refusal
}

func writeVMUnitRestorationTemp(temp *os.File, contents []byte, mode os.FileMode, uid, gid uint32) error {
	if _, err := temp.Write(contents); err != nil {
		return fmt.Errorf("write restoration file: %w", err)
	}
	if err := temp.Chown(int(uid), int(gid)); err != nil {
		return fmt.Errorf("chown restoration file: %w", err)
	}
	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod restoration file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync restoration file: %w", err)
	}
	return nil
}

func validateVMUnitRestorationTemp(dir *os.File, name string, want vmJailerFileIdentity, mode os.FileMode, uid, gid uint32) error {
	got, stat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("inspect restoration file: %w", err)
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Uid != uid || stat.Gid != gid || uint32(stat.Mode)&0o7777 != vmUnitPOSIXPermissions(mode) {
		return fmt.Errorf("restoration file changed before rename")
	}
	return nil
}

func vmUnitFileMode(mode uint32) os.FileMode {
	result := os.FileMode(mode & 0o777)
	if mode&unix.S_ISUID != 0 {
		result |= os.ModeSetuid
	}
	if mode&unix.S_ISGID != 0 {
		result |= os.ModeSetgid
	}
	if mode&unix.S_ISVTX != 0 {
		result |= os.ModeSticky
	}
	return result
}

func vmUnitPOSIXPermissions(mode os.FileMode) uint32 {
	result := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		result |= unix.S_ISUID
	}
	if mode&os.ModeSetgid != 0 {
		result |= unix.S_ISGID
	}
	if mode&os.ModeSticky != 0 {
		result |= unix.S_ISVTX
	}
	return result
}

func cleanupVMUnitRestorationTemp(dir, temp *os.File, name string, id vmJailerFileIdentity, unlinkAt func(int, string, int) error) error {
	var closeErr error
	if temp != nil {
		closeErr = temp.Close()
	}
	return errors.Join(closeErr, unlinkVMJailerNameIfIdentityWith(dir, name, id, unlinkAt))
}

func (tx *vmUnitTransaction) Commit() error {
	if tx == nil {
		return nil
	}
	tx.commitMu.Lock()
	defer tx.commitMu.Unlock()
	if tx.commitAttempted {
		return tx.commitErr
	}
	tx.commitAttempted = true
	tx.commitErr = tx.commitOnce()
	return tx.commitErr
}

func (tx *vmUnitTransaction) commitOnce() error {
	if len(tx.units) == 0 {
		return nil
	}
	for i := range tx.units {
		unit := &tx.units[i]
		applied, uncertain, err := tx.installUnit(unit)
		if err != nil {
			result := tx.rollbackUnits(i+applied, err)
			if uncertain {
				return &vmUnitRestorationUncertainError{cause: result, paths: []string{unit.Path}}
			}
			return result
		}
	}
	if err := tx.syncUnitDirs(); err != nil {
		return tx.rollbackUnits(len(tx.units), fmt.Errorf("sync published VM unit directories: %w", err))
	}
	if err := tx.deps.systemctl("daemon-reload"); err != nil {
		return tx.rollbackUnits(len(tx.units), fmt.Errorf("reload systemd after replacing VM units: %w", err))
	}
	return nil
}

func (tx *vmUnitTransaction) installUnit(unit *vmUnitReplacement) (applied int, uncertain bool, retErr error) {
	if err := validateVMUnitReplacement(*unit, tx.deps.unitUID, tx.deps.unitGID); err != nil {
		return 0, false, err
	}
	err := tx.deps.renameAt(int(unit.dir.Fd()), unit.stagedName, int(unit.dir.Fd()), unit.liveName)
	if err != nil {
		return tx.classifyUnitRenameError(unit, err)
	}
	unit.installedID = unit.stagedID
	return 1, false, validateInstalledVMUnit(*unit, tx.deps.unitUID, tx.deps.unitGID)
}

func (tx *vmUnitTransaction) classifyUnitRenameError(unit *vmUnitReplacement, renameErr error) (int, bool, error) {
	cause := fmt.Errorf("replace VM unit %s: %w", unit.Path, renameErr)
	state, installedID, inspectErr := tx.classifyUnit(unit)
	cause = errors.Join(cause, inspectErr)
	switch state {
	case vmUnitExactInstalled:
		unit.installedID = installedID
		return 1, false, cause
	case vmUnitExactPrevious:
		return 0, false, cause
	default:
		return 0, true, cause
	}
}

type vmUnitExactState uint8

const (
	vmUnitExactNeither vmUnitExactState = iota
	vmUnitExactPrevious
	vmUnitExactInstalled
)

func (tx *vmUnitTransaction) classifyUnit(unit *vmUnitReplacement) (vmUnitExactState, vmJailerFileIdentity, error) {
	raw, existed, id, stat, err := readVMUnitStateAtWithHook(unit.dir, unit.liveName, tx.deps.unitUID, tx.deps.afterRead)
	if err != nil {
		return vmUnitExactNeither, vmJailerFileIdentity{}, err
	}
	if !existed {
		return classifyAbsentVMUnit(unit), vmJailerFileIdentity{}, nil
	}
	if matchesPreviousVMUnit(unit, raw, stat) {
		return vmUnitExactPrevious, id, nil
	}
	if matchesInstalledVMUnit(unit, raw, stat, tx.deps) {
		return vmUnitExactInstalled, id, nil
	}
	return vmUnitExactNeither, id, nil
}

func classifyAbsentVMUnit(unit *vmUnitReplacement) vmUnitExactState {
	if unit.Existed {
		return vmUnitExactNeither
	}
	return vmUnitExactPrevious
}

func matchesPreviousVMUnit(unit *vmUnitReplacement, raw []byte, stat unix.Stat_t) bool {
	return unit.Existed && bytes.Equal(raw, unit.Previous) && uint32(stat.Mode) == unit.previousMode && stat.Uid == unit.previousUID && stat.Gid == unit.previousGID
}

func matchesInstalledVMUnit(unit *vmUnitReplacement, raw []byte, stat unix.Stat_t, deps vmUnitTransactionDeps) bool {
	return bytes.Equal(raw, unit.Installed) && uint32(stat.Mode) == unit.stagedMode && stat.Uid == deps.unitUID && stat.Gid == deps.unitGID
}

func (tx *vmUnitTransaction) syncUnitDirs() error {
	var retErr error
	for _, dir := range tx.unitDirs {
		if err := tx.deps.syncDir(dir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("sync VM unit directory %s: %w", dir.Name(), err))
		}
	}
	return retErr
}

// RestorePreviousAndVerify is an idempotent compensating operation for a
// transaction whose Commit succeeded. It keeps the transaction's bound
// directory locks, restores exact captured bytes and metadata (or exact
// absence), syncs every affected directory, performs only daemon-reload, and
// verifies the complete prior generation before returning success.
func (tx *vmUnitTransaction) RestorePreviousAndVerify() error {
	if tx == nil {
		return nil
	}
	tx.commitMu.Lock()
	defer tx.commitMu.Unlock()
	if !tx.commitAttempted || tx.commitErr != nil {
		return fmt.Errorf("restore previous VM units requires a successful commit")
	}
	if len(tx.units) == 0 {
		return nil
	}

	result := newVMUnitRestorationResult()
	for i := len(tx.units) - 1; i >= 0; i-- {
		unit := &tx.units[i]
		result.record(unit.Path, tx.restorePreviousUnit(unit))
	}
	result.recordAll(tx.units, wrapVMUnitReloadError(tx.deps.systemctl("daemon-reload")))
	for i := range tx.units {
		unit := &tx.units[i]
		result.record(unit.Path, tx.verifyPreviousUnit(unit))
	}
	return result.err()
}

type vmUnitRestorationResult struct {
	cause     error
	uncertain map[string]struct{}
}

func newVMUnitRestorationResult() *vmUnitRestorationResult {
	return &vmUnitRestorationResult{uncertain: make(map[string]struct{})}
}

func (result *vmUnitRestorationResult) record(path string, err error) {
	if err == nil {
		return
	}
	result.cause = errors.Join(result.cause, err)
	result.uncertain[path] = struct{}{}
}

func (result *vmUnitRestorationResult) recordAll(units []vmUnitReplacement, err error) {
	if err == nil {
		return
	}
	result.cause = errors.Join(result.cause, err)
	for i := range units {
		result.uncertain[units[i].Path] = struct{}{}
	}
}

func (result *vmUnitRestorationResult) err() error {
	if result.cause == nil {
		return nil
	}
	paths := make([]string, 0, len(result.uncertain))
	for path := range result.uncertain {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return &vmUnitRestorationUncertainError{cause: result.cause, paths: paths}
}

func (tx *vmUnitTransaction) restorePreviousUnit(unit *vmUnitReplacement) error {
	state, installedID, err := tx.classifyUnit(unit)
	if err != nil {
		return fmt.Errorf("classify VM unit %s before restoration: %w", unit.Path, err)
	}
	if state == vmUnitExactInstalled {
		err = tx.restoreInstalledUnit(unit, installedID)
	}
	if state == vmUnitExactNeither {
		err = fmt.Errorf("VM unit %s matches neither the exact prior nor installed generation", unit.Path)
	}
	if err != nil {
		return fmt.Errorf("restore VM unit %s: %w", unit.Path, err)
	}
	if err := tx.deps.syncDir(unit.dir); err != nil {
		return fmt.Errorf("sync restored VM unit directory for %s: %w", unit.Path, err)
	}
	return nil
}

func (tx *vmUnitTransaction) restoreInstalledUnit(unit *vmUnitReplacement, installedID vmJailerFileIdentity) error {
	if unit.Existed {
		return tx.deps.restoreUnitAt(
			unit.dir, unit.liveName, installedID, unit.Previous, vmUnitFileMode(unit.previousMode),
			unit.previousUID, unit.previousGID, tx.deps.exchangeAt, tx.deps.unlinkAt,
		)
	}
	candidate := *unit
	candidate.installedID = installedID
	err := rollbackNewVMUnit(&candidate, tx.deps)
	if candidate.stagedName == "" {
		unit.stagedName = ""
	}
	return err
}

func (tx *vmUnitTransaction) verifyPreviousUnit(unit *vmUnitReplacement) error {
	state, _, err := tx.classifyUnit(unit)
	if err != nil {
		return fmt.Errorf("verify restored VM unit %s: %w", unit.Path, err)
	}
	if state != vmUnitExactPrevious {
		return fmt.Errorf("verify restored VM unit %s: does not match exact prior state", unit.Path)
	}
	return nil
}

func wrapVMUnitReloadError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("reload systemd after restoring VM units: %w", err)
}

func validateVMUnitReplacement(unit vmUnitReplacement, uid, gid uint32) error {
	if unit.dir == nil || unit.stagedID == (vmJailerFileIdentity{}) {
		return fmt.Errorf("VM unit replacement %s is not bound to a staged inode", unit.Path)
	}
	if err := validateOpenVMUnitDir(unit.dir, uid); err != nil {
		return fmt.Errorf("validate bound VM unit directory for %s: %w", unit.Path, err)
	}
	got, stat, err := vmJailerNameIdentityAt(unit.dir, unit.stagedName)
	if err != nil {
		return fmt.Errorf("inspect staged VM unit %s before commit: %w", unit.Staged, err)
	}
	if got != unit.stagedID || uint32(stat.Mode) != unit.stagedMode || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("staged VM unit %s changed before commit", unit.Staged)
	}
	if stat.Uid != uid || stat.Gid != gid {
		return fmt.Errorf("staged VM unit %s owner is %d:%d, want %d:%d", unit.Staged, stat.Uid, stat.Gid, uid, gid)
	}
	return validateOriginalVMUnit(unit)
}

func validateOriginalVMUnit(unit vmUnitReplacement) error {
	got, stat, err := vmJailerNameIdentityAt(unit.dir, unit.liveName)
	if !unit.Existed {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect live VM unit %s before commit: %w", unit.Path, err)
		}
		return fmt.Errorf("live VM unit %s appeared before commit with inode %d:%d", unit.Path, got.dev, got.ino)
	}
	if err != nil {
		return fmt.Errorf("live VM unit %s changed before commit: %w", unit.Path, err)
	}
	if got != unit.previousID || uint32(stat.Mode) != unit.previousMode || stat.Uid != unit.previousUID || stat.Gid != unit.previousGID {
		return fmt.Errorf("live VM unit %s changed before commit", unit.Path)
	}
	return nil
}

func validateInstalledVMUnit(unit vmUnitReplacement, uid, gid uint32) error {
	got, stat, err := vmJailerNameIdentityAt(unit.dir, unit.liveName)
	if err != nil {
		return fmt.Errorf("inspect installed VM unit %s: %w", unit.Path, err)
	}
	if got != unit.installedID || uint32(stat.Mode) != unit.stagedMode || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("installed VM unit %s changed immediately after rename", unit.Path)
	}
	if stat.Uid != uid || stat.Gid != gid {
		return fmt.Errorf("installed VM unit %s owner is %d:%d, want %d:%d", unit.Path, stat.Uid, stat.Gid, uid, gid)
	}
	return nil
}

func (tx *vmUnitTransaction) rollbackUnits(applied int, cause error) error {
	result := newVMUnitRestorationResult()
	for i := applied - 1; i >= 0; i-- {
		unit := &tx.units[i]
		result.record(unit.Path, tx.rollbackAppliedUnit(unit))
	}
	result.recordAll(tx.units[:applied], wrapVMUnitReloadError(tx.deps.systemctl("daemon-reload")))
	for i := 0; i < applied; i++ {
		unit := &tx.units[i]
		result.record(unit.Path, tx.verifyPreviousUnit(unit))
	}
	return errors.Join(cause, result.err())
}

func (tx *vmUnitTransaction) rollbackAppliedUnit(unit *vmUnitReplacement) error {
	var err error
	if unit.Existed {
		err = tx.deps.restoreUnitAt(
			unit.dir, unit.liveName, unit.installedID, unit.Previous, vmUnitFileMode(unit.previousMode),
			unit.previousUID, unit.previousGID, tx.deps.exchangeAt, tx.deps.unlinkAt,
		)
	} else {
		err = rollbackNewVMUnit(unit, tx.deps)
	}
	if err != nil {
		return fmt.Errorf("restore VM unit %s: %w", unit.Path, err)
	}
	if err := tx.deps.syncDir(unit.dir); err != nil {
		return fmt.Errorf("sync restored VM unit directory for %s: %w", unit.Path, err)
	}
	return nil
}

func rollbackNewVMUnit(unit *vmUnitReplacement, deps vmUnitTransactionDeps) error {
	dirFD := int(unit.dir.Fd())
	if err := deps.renameNoReplaceAt(dirFD, unit.liveName, dirFD, unit.stagedName); err != nil {
		return fmt.Errorf("quarantine live VM unit: %w", err)
	}
	displacedID, _, displacedErr := vmJailerNameIdentityAt(unit.dir, unit.stagedName)
	if displacedErr == nil && displacedID == unit.installedID {
		return unlinkVMJailerNameIfIdentityWith(unit.dir, unit.stagedName, unit.installedID, deps.unlinkAt)
	}
	if displacedErr == nil {
		displacedErr = fmt.Errorf("inode changed")
	}
	refusal := fmt.Errorf("live VM unit changed after install; refusing rollback: %w", displacedErr)
	if err := deps.renameNoReplaceAt(dirFD, unit.stagedName, dirFD, unit.liveName); err != nil {
		unit.stagedName = ""
		return errors.Join(refusal, fmt.Errorf("restore quarantined concurrent VM unit: %w", err))
	}
	return refusal
}

func (tx *vmUnitTransaction) Close() error {
	if tx == nil {
		return nil
	}
	tx.commitMu.Lock()
	defer tx.commitMu.Unlock()
	if tx.closed {
		return nil
	}
	tx.closed = true
	var retErr error
	for _, unit := range tx.units {
		if unit.dir == nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove staged VM unit %s: replacement is not bound to a directory", unit.Staged))
			continue
		}
		if unit.stagedName == "" {
			continue
		}
		// The staging name is transaction-private. Remove whatever directory
		// entry is there without following it so cleanup also eliminates a
		// substituted symlink while leaving its target untouched.
		if err := tx.deps.unlinkAt(int(unit.dir.Fd()), unit.stagedName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			retErr = errors.Join(retErr, fmt.Errorf("remove staged VM unit %s: %w", unit.Staged, err))
		}
	}
	retErr = errors.Join(retErr, closeVMUnitDirs(tx.unitDirs))
	return retErr
}

func defaultVMUnitTransactionDeps() vmUnitTransactionDeps {
	return vmUnitTransactionDeps{
		renameAt:          unix.Renameat,
		exchangeAt:        exchangeVMJailerUnitNamesAt,
		renameNoReplaceAt: renameVMJailerUnitNameNoReplaceAt,
		restoreUnitAt:     restoreVMUnitAt,
		unlinkAt:          unix.Unlinkat,
		syncDir:           func(dir *os.File) error { return dir.Sync() },
		systemctl:         runVMSystemctl,
		unitUID:           0,
		unitGID:           0,
	}
}

func completeVMUnitTransactionDeps(deps vmUnitTransactionDeps) vmUnitTransactionDeps {
	defaults := defaultVMUnitTransactionDeps()
	if deps.renameAt == nil {
		deps.renameAt = defaults.renameAt
	}
	if deps.exchangeAt == nil {
		deps.exchangeAt = defaults.exchangeAt
	}
	if deps.renameNoReplaceAt == nil {
		deps.renameNoReplaceAt = defaults.renameNoReplaceAt
	}
	if deps.restoreUnitAt == nil {
		deps.restoreUnitAt = defaults.restoreUnitAt
	}
	if deps.unlinkAt == nil {
		deps.unlinkAt = defaults.unlinkAt
	}
	if deps.syncDir == nil {
		deps.syncDir = defaults.syncDir
	}
	if deps.systemctl == nil {
		deps.systemctl = defaults.systemctl
	}
	return deps
}

func acquireVMJailerUpgradeDirLock(ctx context.Context, dir *os.File) error {
	for {
		err := unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock trusted directory: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func releaseVMJailerUpgradeDirLock(dir *os.File) error {
	if err := unix.Flock(int(dir.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("unlock trusted directory: %w", err)
	}
	return nil
}
