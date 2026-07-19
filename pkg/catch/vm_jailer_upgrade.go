// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

type vmJailerUpgradeVM struct {
	Service      string
	Payload      string
	ImageVersion string
	Architecture string
	ServiceRoot  string
	Disk         string
	Firecracker  string
	Jailer       string
	UnitPath     string
	UnitContent  []byte
	Readiness    vmJailerReadiness
	Running      bool
}

type VMJailerUpgradeSummary struct {
	Ready          []string
	PendingRestart []string
}

type vmJailerUpgradePlan struct {
	VMs     []vmJailerUpgradeVM
	Summary VMJailerUpgradeSummary
}

type vmJailerUnitReplacement struct {
	Service      string
	Path         string
	Staged       string
	Previous     []byte
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

type VMJailerUpgrade struct {
	units           []vmJailerUnitReplacement
	unitDirs        []*os.File
	summary         VMJailerUpgradeSummary
	deps            vmJailerUpgradeDeps
	closed          bool
	commitMu        sync.Mutex
	commitAttempted bool
	commitErr       error
}

type vmJailerCandidate struct {
	Path         string
	ArtifactName string
	SHA256       string
	Architecture string
}

type vmJailerUpgradeDeps struct {
	sibling               func(context.Context, vmJailerUpgradeVM) (string, bool, error)
	cached                func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error)
	localPayload          func(string) bool
	official              func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error)
	install               func(context.Context, vmJailerUpgradeVM, vmJailerCandidate) (string, error)
	readiness             func(string) (vmJailerReadiness, error)
	isRunning             func(*Server, string) (bool, error)
	renderUnit            func(vmSystemdConfig) (string, error)
	ensureRuntimeIdentity func() (vmRuntimeIdentity, error)
	renameAt              func(int, string, int, string) error
	exchangeAt            func(int, string, int, string) error
	renameNoReplaceAt     func(int, string, int, string) error
	restoreUnitAt         func(*os.File, string, vmJailerFileIdentity, []byte, os.FileMode, uint32, uint32, func(int, string, int, string) error, func(int, string, int) error) error
	unlinkAt              func(int, string, int) error
	systemctl             func(...string) error
	unitUID               uint32
	unitGID               uint32
}

var (
	errVMJailerUpgradeUnknownPayload             = errors.New("VM image payload is not in the trusted official catalog")
	errVMJailerUpgradeIncompatibleCacheCandidate = errors.New("cached VM jailer is incompatible with the target runtime")
)

func PrepareVMJailerUpgrade(ctx context.Context, cfg *Config) (*VMJailerUpgrade, error) {
	return prepareVMJailerUpgradeWithDeps(ctx, cfg, defaultVMJailerUpgradeDeps())
}

func prepareVMJailerUpgradeWithDeps(ctx context.Context, cfg *Config, deps vmJailerUpgradeDeps) (*VMJailerUpgrade, error) {
	deps = completeVMJailerUpgradeRuntimeDeps(completeVMJailerUpgradeTransactionDeps(deps))
	plan, err := planVMJailerUpgrade(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	if len(plan.VMs) > 0 {
		if _, err := deps.ensureRuntimeIdentity(); err != nil {
			return nil, fmt.Errorf("ensure VM runtime identity for jailer upgrade: %w", err)
		}
	}
	return prepareVMJailerUnitTransaction(ctx, plan.VMs, plan.Summary, deps)
}

func prepareVMJailerUnitTransaction(ctx context.Context, vms []vmJailerUpgradeVM, summary VMJailerUpgradeSummary, deps vmJailerUpgradeDeps) (*VMJailerUpgrade, error) {
	deps = completeVMJailerUpgradeTransactionDeps(deps)
	dirs, byPath, err := acquireVMJailerUnitDirs(ctx, vms, deps.unitUID)
	if err != nil {
		return nil, err
	}
	tx := &VMJailerUpgrade{summary: summary, deps: deps, unitDirs: dirs}
	for _, vm := range vms {
		replacement, err := stageVMJailerUnitAt(byPath[filepath.Dir(vm.UnitPath)], vm, deps)
		if err != nil {
			return nil, errors.Join(err, tx.Close())
		}
		tx.units = append(tx.units, replacement)
	}
	return tx, nil
}

func acquireVMJailerUnitDirs(ctx context.Context, vms []vmJailerUpgradeVM, trustedUID uint32) ([]*os.File, map[string]*os.File, error) {
	paths := make([]string, 0, len(vms))
	seen := make(map[string]struct{}, len(vms))
	for _, vm := range vms {
		path := filepath.Dir(vm.UnitPath)
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
			return nil, nil, errors.Join(err, closeVMJailerUnitDirs(dirs))
		}
		dir, err := openValidatedVMUnitDir(path, trustedUID)
		if err != nil {
			return nil, nil, errors.Join(err, closeVMJailerUnitDirs(dirs))
		}
		if err := acquireVMJailerUpgradeDirLock(ctx, dir); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("lock VM unit directory %s: %w", path, err), dir.Close(), closeVMJailerUnitDirs(dirs))
		}
		dirs = append(dirs, dir)
		byPath[path] = dir
	}
	return dirs, byPath, nil
}

func closeVMJailerUnitDirs(dirs []*os.File) error {
	var retErr error
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		retErr = errors.Join(retErr, releaseVMJailerUpgradeDirLock(dir), dir.Close())
	}
	return retErr
}

func stageVMJailerUnit(vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps) (replacement vmJailerUnitReplacement, retErr error) {
	deps = completeVMJailerUpgradeTransactionDeps(deps)
	replacement = vmJailerUnitReplacement{Service: vm.Service, Path: vm.UnitPath}
	dir, err := openValidatedVMUnitDir(filepath.Dir(vm.UnitPath), deps.unitUID)
	if err != nil {
		return replacement, err
	}
	defer func() {
		retErr = closeVMUnitStagingDir(dir, replacement.Staged, os.Remove, retErr)
	}()
	raw, existed, err := readVMUnitAt(dir, filepath.Base(vm.UnitPath), deps.unitUID)
	if err != nil {
		return replacement, err
	}
	if existed {
		replacement.Previous = raw
		replacement.Existed = true
	}
	staged, stagedID, stagedMode, err := prepareStagedVMUnitAt(dir, vm, deps)
	if err != nil {
		return replacement, err
	}
	replacement.Staged = staged
	replacement.stagedID = stagedID
	replacement.stagedMode = stagedMode
	return replacement, nil
}

func stageVMJailerUnitAt(dir *os.File, vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps) (vmJailerUnitReplacement, error) {
	if dir == nil {
		return vmJailerUnitReplacement{}, fmt.Errorf("bound VM unit directory is required for %s", vm.UnitPath)
	}
	replacement := vmJailerUnitReplacement{
		Service: vm.Service, Path: vm.UnitPath, dir: dir, liveName: filepath.Base(vm.UnitPath),
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
	staged, stagedID, stagedMode, err := prepareStagedVMUnitAt(dir, vm, deps)
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

func prepareStagedVMUnitAt(dir *os.File, vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps) (string, vmJailerFileIdentity, uint32, error) {
	name := filepath.Base(vm.UnitPath)
	temp, tempName, tempID, err := createStagedVMUnitAt(dir, name)
	if err != nil {
		return "", vmJailerFileIdentity{}, 0, fmt.Errorf("create staged VM unit %s: %w", vm.UnitPath, err)
	}
	stat, err := writeAndSecureStagedVMUnit(temp, vm, deps, tempID)
	if err != nil {
		return "", vmJailerFileIdentity{}, 0, cleanupFailedStagedVMUnit(dir, temp, tempName, tempID, err)
	}
	if err := temp.Close(); err != nil {
		cause := fmt.Errorf("close staged VM unit %s: %w", vm.UnitPath, err)
		cause = errors.Join(cause, unlinkVMJailerNameIfIdentity(dir, tempName, tempID))
		return "", vmJailerFileIdentity{}, 0, cause
	}
	return filepath.Join(filepath.Dir(vm.UnitPath), tempName), tempID, uint32(stat.Mode), nil
}

func writeAndSecureStagedVMUnit(temp *os.File, vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps, tempID vmJailerFileIdentity) (unix.Stat_t, error) {
	if _, err := temp.Write(vm.UnitContent); err != nil {
		return unix.Stat_t{}, fmt.Errorf("write staged VM unit %s: %w", vm.UnitPath, err)
	}
	if err := temp.Chown(int(deps.unitUID), int(deps.unitGID)); err != nil {
		return unix.Stat_t{}, fmt.Errorf("chown staged VM unit %s: %w", vm.UnitPath, err)
	}
	if err := temp.Chmod(0o644); err != nil {
		return unix.Stat_t{}, fmt.Errorf("chmod staged VM unit %s: %w", vm.UnitPath, err)
	}
	if err := temp.Sync(); err != nil {
		return unix.Stat_t{}, fmt.Errorf("sync staged VM unit %s: %w", vm.UnitPath, err)
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
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, nil
	}
	if err != nil {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("open VM unit without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("bind VM unit file descriptor")
	}
	id, stat, statErr := vmJailerFileIdentityForFile(file)
	if statErr != nil {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, closeVMJailerFileOnError(file, fmt.Errorf("inspect VM unit: %w", statErr))
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, closeVMJailerFileOnError(file, fmt.Errorf("VM unit is not a regular file"))
	}
	if stat.Uid != trustedUID {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, closeVMJailerFileOnError(file, fmt.Errorf("VM unit owner is %d, want %d", stat.Uid, trustedUID))
	}
	raw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, errors.Join(fmt.Errorf("read VM unit: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return nil, false, vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("close VM unit: %w", closeErr)
	}
	return raw, true, id, stat, nil
}

func createStagedVMUnitAt(dir *os.File, unitName string) (*os.File, string, vmJailerFileIdentity, error) {
	for range 128 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", vmJailerFileIdentity{}, fmt.Errorf("generate staged VM unit name: %w", err)
		}
		name := "." + unitName + ".jailer-" + hex.EncodeToString(random[:])
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

func restoreVMJailerUnitAt(
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
			retErr = errors.Join(retErr, cleanupVMJailerRestorationTemp(dir, temp, tempName, tempID, unlinkAt))
		}
	}()
	if err := writeVMJailerRestorationTemp(temp, contents, mode, uid, gid); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close restoration file: %w", err)
	}
	temp = nil
	if err := validateVMJailerRestorationTemp(dir, tempName, tempID, mode, uid, gid); err != nil {
		return err
	}
	cleanupTemp, retErr = finishVMJailerUnitRestoration(dir, tempName, name, installedID, exchangeAt, unlinkAt)
	return retErr
}

func finishVMJailerUnitRestoration(
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

func writeVMJailerRestorationTemp(temp *os.File, contents []byte, mode os.FileMode, uid, gid uint32) error {
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

func validateVMJailerRestorationTemp(dir *os.File, name string, want vmJailerFileIdentity, mode os.FileMode, uid, gid uint32) error {
	got, stat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("inspect restoration file: %w", err)
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Uid != uid || stat.Gid != gid || os.FileMode(stat.Mode)&os.ModePerm != mode.Perm() {
		return fmt.Errorf("restoration file changed before rename")
	}
	return nil
}

func cleanupVMJailerRestorationTemp(dir, temp *os.File, name string, id vmJailerFileIdentity, unlinkAt func(int, string, int) error) error {
	var closeErr error
	if temp != nil {
		closeErr = temp.Close()
	}
	return errors.Join(closeErr, unlinkVMJailerNameIfIdentityWith(dir, name, id, unlinkAt))
}

func (tx *VMJailerUpgrade) Commit() error {
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

func (tx *VMJailerUpgrade) commitOnce() error {
	if len(tx.units) == 0 {
		return nil
	}
	for i := range tx.units {
		unit := &tx.units[i]
		if err := validateVMJailerUnitReplacement(*unit, tx.deps.unitUID, tx.deps.unitGID); err != nil {
			return tx.rollbackUnits(i, err)
		}
		if err := tx.deps.renameAt(int(unit.dir.Fd()), unit.stagedName, int(unit.dir.Fd()), unit.liveName); err != nil {
			return tx.rollbackUnits(i, fmt.Errorf("replace VM unit %s: %w", unit.Path, err))
		}
		unit.installedID = unit.stagedID
		if err := validateInstalledVMJailerUnit(*unit, tx.deps.unitUID, tx.deps.unitGID); err != nil {
			return tx.rollbackUnits(i+1, err)
		}
	}
	if err := tx.deps.systemctl("daemon-reload"); err != nil {
		return tx.rollbackUnits(len(tx.units), fmt.Errorf("reload systemd after replacing VM units: %w", err))
	}
	return nil
}

func validateVMJailerUnitReplacement(unit vmJailerUnitReplacement, uid, gid uint32) (retErr error) {
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
	// The directory is root-owned and not writable by group or others, so a
	// non-root process cannot replace this name. Rechecking identity and mode
	// here also refuses accidental or concurrent root-level replacement.
	if got != unit.stagedID || uint32(stat.Mode) != unit.stagedMode || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("staged VM unit %s changed before commit", unit.Staged)
	}
	if stat.Uid != uid || stat.Gid != gid {
		return fmt.Errorf("staged VM unit %s owner is %d:%d, want %d:%d", unit.Staged, stat.Uid, stat.Gid, uid, gid)
	}
	return validateOriginalVMJailerUnit(unit)
}

func validateOriginalVMJailerUnit(unit vmJailerUnitReplacement) error {
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

func validateInstalledVMJailerUnit(unit vmJailerUnitReplacement, uid, gid uint32) error {
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

func (tx *VMJailerUpgrade) rollbackUnits(applied int, cause error) error {
	var rollbackErr error
	for i := applied - 1; i >= 0; i-- {
		unit := &tx.units[i]
		if unit.Existed {
			mode := os.FileMode(unit.previousMode & 0o7777)
			if err := tx.deps.restoreUnitAt(
				unit.dir, unit.liveName, unit.installedID, unit.Previous, mode,
				unit.previousUID, unit.previousGID, tx.deps.exchangeAt, tx.deps.unlinkAt,
			); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore VM unit %s: %w", unit.Path, err))
			}
		} else if err := rollbackNewVMJailerUnit(unit, tx.deps); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove new VM unit %s: %w", unit.Path, err))
		}
	}
	if err := tx.deps.systemctl("daemon-reload"); err != nil {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("reload systemd after restoring VM units: %w", err))
	}
	return errors.Join(cause, rollbackErr)
}

func rollbackNewVMJailerUnit(unit *vmJailerUnitReplacement, deps vmJailerUpgradeDeps) error {
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
		// The quarantine now contains an inode that does not belong to this
		// transaction. Keep it for operator recovery instead of letting Close
		// remove it as if it were the original staging file.
		unit.stagedName = ""
		return errors.Join(refusal, fmt.Errorf("restore quarantined concurrent VM unit: %w", err))
	}
	return refusal
}

func (tx *VMJailerUpgrade) Close() error {
	if tx == nil || tx.closed {
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
		if err := tx.deps.unlinkAt(int(unit.dir.Fd()), unit.stagedName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			retErr = errors.Join(retErr, fmt.Errorf("remove staged VM unit %s: %w", unit.Staged, err))
		}
	}
	retErr = errors.Join(retErr, closeVMJailerUnitDirs(tx.unitDirs))
	return retErr
}

func (tx *VMJailerUpgrade) Summary() VMJailerUpgradeSummary {
	if tx == nil {
		return VMJailerUpgradeSummary{}
	}
	return VMJailerUpgradeSummary{
		Ready:          append([]string(nil), tx.summary.Ready...),
		PendingRestart: append([]string(nil), tx.summary.PendingRestart...),
	}
}

func resolveVMUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps) (string, string, error) {
	if path, ok, err := deps.sibling(ctx, vm); err != nil {
		return "", "", err
	} else if ok {
		return path, "sibling", nil
	}
	if candidate, ok, err := deps.cached(ctx, vm); err != nil {
		return "", "", err
	} else if ok {
		path, err := deps.install(ctx, vm, candidate)
		return path, "cache", err
	}
	if !strings.HasPrefix(strings.TrimSpace(vm.Payload), vmImagePayloadPrefix) || deps.localPayload(vm.Payload) {
		return "", "", vmJailerUpgradeReimportError(vm)
	}
	candidate, err := deps.official(ctx, vm)
	if err != nil {
		if errors.Is(err, errVMJailerUpgradeUnknownPayload) {
			return "", "", vmJailerUpgradeReimportError(vm)
		}
		return "", "", fmt.Errorf("VM %q: refresh the official VM image cache: %w", vm.Service, err)
	}
	path, err := deps.install(ctx, vm, candidate)
	return path, "remote", err
}

func vmJailerUpgradeReimportError(vm vmJailerUpgradeVM) error {
	return fmt.Errorf("VM %q has no trusted jailer for Firecracker %s; re-import the custom image with a matching jailer", vm.Service, vm.ImageVersion)
}

func planVMJailerUpgrade(ctx context.Context, cfg *Config, deps vmJailerUpgradeDeps) (vmJailerUpgradePlan, error) {
	if err := ctx.Err(); err != nil {
		return vmJailerUpgradePlan{}, err
	}
	effectiveCfg, err := prepareVMJailerUpgradeConfig(cfg)
	if err != nil {
		return vmJailerUpgradePlan{}, err
	}
	deps = completeVMJailerUpgradeDeps(&effectiveCfg, deps)
	if err := validateVMJailerUpgradeDeps(deps); err != nil {
		return vmJailerUpgradePlan{}, err
	}
	services, names, err := readVMJailerUpgradeServices(&effectiveCfg)
	if err != nil {
		return vmJailerUpgradePlan{}, err
	}
	server := &Server{cfg: effectiveCfg}
	plan := vmJailerUpgradePlan{VMs: make([]vmJailerUpgradeVM, 0, len(names))}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return vmJailerUpgradePlan{}, err
		}
		vm, err := planVMJailerUpgradeService(ctx, &effectiveCfg, server, *services[name], deps)
		if err != nil {
			return vmJailerUpgradePlan{}, err
		}
		plan.VMs = append(plan.VMs, vm)
		if err := addVMJailerUpgradeSummary(&plan.Summary, vm); err != nil {
			return vmJailerUpgradePlan{}, err
		}
	}
	sort.Slice(plan.VMs, func(i, j int) bool { return plan.VMs[i].Service < plan.VMs[j].Service })
	sort.Strings(plan.Summary.Ready)
	sort.Strings(plan.Summary.PendingRestart)
	return plan, nil
}

func prepareVMJailerUpgradeConfig(cfg *Config) (Config, error) {
	if cfg == nil || cfg.DB == nil {
		return Config{}, fmt.Errorf("catch configuration and database are required for VM jailer upgrade")
	}
	dataRoot := filepath.Clean(strings.TrimSpace(cfg.RootDir))
	if !filepath.IsAbs(dataRoot) {
		return Config{}, fmt.Errorf("configured VM data root must be absolute for jailer upgrade: %s", cfg.RootDir)
	}
	effectiveCfg := *cfg
	if strings.TrimSpace(effectiveCfg.ServicesRoot) == "" {
		effectiveCfg.ServicesRoot = filepath.Join(dataRoot, "services")
	}
	return effectiveCfg, nil
}

func readVMJailerUpgradeServices(cfg *Config) (map[string]*db.Service, []string, error) {
	dv, err := cfg.DB.Get()
	if err != nil {
		return nil, nil, fmt.Errorf("read VM upgrade inventory: %w", err)
	}
	if !dv.Valid() {
		return nil, nil, fmt.Errorf("read VM upgrade inventory: database is invalid")
	}
	services := dv.AsStruct().Services
	names := make([]string, 0, len(services))
	for name, service := range services {
		if isVMJailerUpgradeService(service) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return services, names, nil
}

func isVMJailerUpgradeService(service *db.Service) bool {
	return service != nil && service.ServiceType == db.ServiceTypeVM && service.VM != nil && strings.TrimSpace(service.Name) != ""
}

func planVMJailerUpgradeService(ctx context.Context, cfg *Config, server *Server, service db.Service, deps vmJailerUpgradeDeps) (vmJailerUpgradeVM, error) {
	vm, renderCfg, err := inventoryVMJailerUpgrade(ctx, cfg, server, service, deps)
	if err != nil {
		return vmJailerUpgradeVM{}, fmt.Errorf("plan VM jailer upgrade for %q: %w", service.Name, err)
	}
	unit, err := deps.renderUnit(renderCfg)
	if err != nil {
		return vmJailerUpgradeVM{}, fmt.Errorf("render VM jailer upgrade unit for %q: %w", service.Name, err)
	}
	vm.UnitContent = []byte(unit)
	return vm, nil
}

func addVMJailerUpgradeSummary(summary *VMJailerUpgradeSummary, vm vmJailerUpgradeVM) error {
	switch vm.Readiness {
	case vmJailerReady:
		summary.Ready = append(summary.Ready, vm.Service)
	case vmJailerPendingRestart:
		summary.PendingRestart = append(summary.PendingRestart, vm.Service)
	default:
		return fmt.Errorf("VM %q has unsupported jailer readiness %q", vm.Service, vm.Readiness)
	}
	return nil
}

type vmJailerUpgradeRuntime struct {
	disk        string
	firecracker string
	manifest    vmImageManifest
}

func inventoryVMJailerUpgrade(ctx context.Context, cfg *Config, server *Server, service db.Service, deps vmJailerUpgradeDeps) (vmJailerUpgradeVM, vmSystemdConfig, error) {
	root := filepath.Clean(strings.TrimSpace(serviceRootFromConfig(*cfg, service)))
	if !filepath.IsAbs(root) {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, fmt.Errorf("effective service root must be absolute: %s", root)
	}
	vmRuntime, err := inspectVMJailerUpgradeRuntime(service, runtime.GOARCH)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, err
	}
	readiness, err := deps.readiness(root)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, err
	}
	running, err := deps.isRunning(server, service.Name)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, fmt.Errorf("inspect VM running state: %w", err)
	}
	vm := vmJailerUpgradeVM{
		Service:      service.Name,
		Payload:      strings.TrimSpace(service.VM.Image.Payload),
		ImageVersion: vmRuntime.manifest.Version,
		Architecture: vmRuntime.manifest.Architecture,
		ServiceRoot:  root,
		Disk:         vmRuntime.disk,
		Firecracker:  vmRuntime.firecracker,
		Jailer:       filepath.Join(filepath.Dir(vmRuntime.firecracker), "jailer"),
		UnitPath:     filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service.Name)),
		Readiness:    readiness,
		Running:      running,
	}
	resolvedJailer, _, err := resolveVMUpgradeJailer(ctx, vm, deps)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, err
	}
	vm.Jailer = resolvedJailer
	renderCfg := vmJailerUpgradeSystemdConfig(cfg, server, service, vm)
	return vm, renderCfg, nil
}

func inspectVMJailerUpgradeRuntime(service db.Service, hostArchitecture string) (vmJailerUpgradeRuntime, error) {
	rootFS := filepath.Clean(strings.TrimSpace(service.VM.Image.RootFS))
	if !filepath.IsAbs(rootFS) {
		return vmJailerUpgradeRuntime{}, fmt.Errorf("stored VM rootfs must be absolute: %s", rootFS)
	}
	imageDir := filepath.Dir(rootFS)
	firecracker := filepath.Join(imageDir, "firecracker")
	manifest, err := inspectVMJailerUpgradeManifest(service, imageDir, firecracker, hostArchitecture)
	if err != nil {
		return vmJailerUpgradeRuntime{}, err
	}
	if manifest.Version != strings.TrimSpace(service.VM.Image.Version) {
		return vmJailerUpgradeRuntime{}, fmt.Errorf("stored VM image version %q does not match runtime manifest version %q", service.VM.Image.Version, manifest.Version)
	}
	disk := filepath.Clean(strings.TrimSpace(service.VM.Disk.Path))
	if disk == "." || strings.TrimSpace(service.VM.Disk.Path) == "" {
		disk = rootFS
	}
	if !filepath.IsAbs(disk) {
		return vmJailerUpgradeRuntime{}, fmt.Errorf("stored VM disk must be absolute: %s", disk)
	}
	return vmJailerUpgradeRuntime{disk: disk, firecracker: firecracker, manifest: manifest}, nil
}

func inspectVMJailerUpgradeManifest(service db.Service, imageDir, firecracker, hostArchitecture string) (vmImageManifest, error) {
	manifestPath := filepath.Join(imageDir, "manifest.json")
	_, manifestErr := os.Lstat(manifestPath)
	if errors.Is(manifestErr, os.ErrNotExist) {
		if err := validateLegacyVMJailerUpgradeBundle(imageDir, firecracker); err != nil {
			return vmImageManifest{}, err
		}
		return legacyVMJailerUpgradeManifest(service, firecracker, hostArchitecture)
	}
	if manifestErr != nil {
		return vmImageManifest{}, fmt.Errorf("inspect VM image runtime manifest: %w", manifestErr)
	}
	return readValidatedVMImageRuntimeManifest(firecracker)
}

func validateLegacyVMJailerUpgradeBundle(imageDir, firecracker string) error {
	dirInfo, err := os.Lstat(imageDir)
	if err != nil {
		return fmt.Errorf("inspect legacy VM image bundle %s: %w", imageDir, err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("legacy VM image bundle %s must be a directory without symlinks", imageDir)
	}
	firecrackerInfo, err := os.Lstat(firecracker)
	if err != nil {
		return fmt.Errorf("inspect legacy VM image Firecracker %s: %w", firecracker, err)
	}
	if !firecrackerInfo.Mode().IsRegular() || firecrackerInfo.Mode()&os.ModeSymlink != 0 || firecrackerInfo.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("legacy VM image Firecracker %s must be an executable regular file without symlinks", firecracker)
	}
	return nil
}

func legacyVMJailerUpgradeManifest(service db.Service, firecracker, hostArchitecture string) (vmImageManifest, error) {
	architecture, err := normalizeVMImageArchitecture(hostArchitecture)
	if err != nil {
		return vmImageManifest{}, fmt.Errorf("identify legacy VM image runtime architecture: %w", err)
	}
	version := strings.TrimSpace(service.VM.Image.Version)
	if version == "" {
		return vmImageManifest{}, fmt.Errorf("stored VM image version is required for a legacy bundle without manifest.json")
	}
	return vmImageManifest{
		Version:      version,
		Architecture: architecture,
		Firecracker:  filepath.Base(firecracker),
	}, nil
}

func vmJailerUpgradeSystemdConfig(cfg *Config, server *Server, service db.Service, vm vmJailerUpgradeVM) vmSystemdConfig {
	runDir := serviceRunDirForRoot(vm.ServiceRoot)
	return vmSystemdConfig{
		Service:          vm.Service,
		Runner:           server.catchRunnerPath(),
		DataDir:          cfg.RootDir,
		ServicesRoot:     cfg.ServicesRoot,
		ServiceRoot:      vm.ServiceRoot,
		DiskPath:         vm.Disk,
		Firecracker:      vm.Firecracker,
		Jailer:           vm.Jailer,
		JailerBase:       vmJailerBaseForDataRoot(cfg.RootDir),
		ConfigPath:       filepath.Join(runDir, "firecracker.json"),
		APISocket:        service.VM.Sockets.APISocketPath,
		ConsoleSocket:    service.VM.Console.SocketPath,
		VsockSocket:      service.VM.Sockets.VsockSocketPath,
		WorkingDirectory: vm.ServiceRoot,
	}
}

func validateVMJailerUpgradeDeps(deps vmJailerUpgradeDeps) error {
	missing := ""
	for name, present := range map[string]bool{
		"sibling": deps.sibling != nil, "cached": deps.cached != nil,
		"local payload": deps.localPayload != nil, "official": deps.official != nil,
		"install": deps.install != nil, "readiness": deps.readiness != nil,
		"running state": deps.isRunning != nil, "unit renderer": deps.renderUnit != nil,
	} {
		if !present {
			missing = name
			break
		}
	}
	if missing != "" {
		return fmt.Errorf("VM jailer upgrade dependency %s is required", missing)
	}
	return nil
}

func defaultVMJailerUpgradeDeps() vmJailerUpgradeDeps {
	return vmJailerUpgradeDeps{
		sibling:   resolveSiblingVMUpgradeJailer,
		install:   installUpgradeJailer,
		readiness: vmJailerReadinessForRoot,
		isRunning: func(server *Server, service string) (bool, error) {
			return server.IsServiceRunning(service)
		},
		renderUnit:            renderVMSystemdUnit,
		ensureRuntimeIdentity: ensureVMRuntimeIdentity,
		renameAt:              unix.Renameat,
		exchangeAt:            exchangeVMJailerUnitNamesAt,
		renameNoReplaceAt:     renameVMJailerUnitNameNoReplaceAt,
		restoreUnitAt:         restoreVMJailerUnitAt,
		unlinkAt:              unix.Unlinkat,
		systemctl:             runVMSystemctl,
		unitUID:               0,
		unitGID:               0,
	}
}

func completeVMJailerUpgradeTransactionDeps(deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	defaults := defaultVMJailerUpgradeDeps()
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
	if deps.systemctl == nil {
		deps.systemctl = defaults.systemctl
	}
	return deps
}

func completeVMJailerUpgradeDeps(cfg *Config, deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	deps = completeVMJailerUpgradeRuntimeDeps(deps)
	return completeVMJailerUpgradeSourceDeps(cfg, deps)
}

func completeVMJailerUpgradeRuntimeDeps(deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	defaults := defaultVMJailerUpgradeDeps()
	if deps.sibling == nil {
		deps.sibling = defaults.sibling
	}
	if deps.install == nil {
		deps.install = defaults.install
	}
	if deps.readiness == nil {
		deps.readiness = defaults.readiness
	}
	if deps.isRunning == nil {
		deps.isRunning = defaults.isRunning
	}
	if deps.renderUnit == nil {
		deps.renderUnit = defaults.renderUnit
	}
	if deps.ensureRuntimeIdentity == nil {
		deps.ensureRuntimeIdentity = defaults.ensureRuntimeIdentity
	}
	return deps
}

func completeVMJailerUpgradeSourceDeps(cfg *Config, deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	cacheRoot := filepath.Join(filepath.Clean(strings.TrimSpace(cfg.RootDir)), "vm-images")
	cache := vmImageCache{Root: cacheRoot}
	if deps.cached == nil {
		deps.cached = func(ctx context.Context, vm vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
			return cachedVMUpgradeJailerCandidate(ctx, vm, cacheRoot, validateVMJailerRuntimePair)
		}
	}
	if deps.localPayload == nil {
		deps.localPayload = func(payload string) bool {
			payload = strings.TrimSpace(payload)
			if !strings.HasPrefix(payload, vmImagePayloadPrefix) {
				return true
			}
			name := strings.TrimPrefix(payload, vmImagePayloadPrefix)
			exists, err := localVMImageRefExists(cacheRoot, name)
			return err != nil || exists
		}
	}
	if deps.official == nil {
		deps.official = func(ctx context.Context, vm vmJailerUpgradeVM) (vmJailerCandidate, error) {
			return fetchOfficialVMUpgradeJailer(ctx, vm, cache, validateVMJailerRuntimePair)
		}
	}
	return deps
}

func resolveSiblingVMUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM) (string, bool, error) {
	path := filepath.Join(filepath.Dir(filepath.Clean(vm.Firecracker)), "jailer")
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect sibling VM jailer for %q: %w", vm.Service, err)
	}
	if err := validateVMJailerRuntimePair(ctx, vm.Firecracker, path); err != nil {
		return "", true, fmt.Errorf("validate sibling VM jailer for %q: %w", vm.Service, err)
	}
	return path, true, nil
}

func cachedVMUpgradeJailerCandidate(ctx context.Context, vm vmJailerUpgradeVM, cacheRoot string, validatePair func(context.Context, string, string) error) (vmJailerCandidate, bool, error) {
	targetArchitecture, entries, err := cachedVMUpgradeJailerInputs(vm, cacheRoot, validatePair)
	if err != nil {
		return vmJailerCandidate{}, false, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return vmJailerCandidate{}, false, err
		}
		manifest, dir, ok, err := cachedVMImageManifestFromEntry(cacheRoot, entry)
		if err != nil {
			return vmJailerCandidate{}, false, err
		}
		if !ok || strings.TrimSpace(manifest.Jailer) == "" {
			continue
		}
		candidate, err := validateCachedVMUpgradeJailerCandidate(ctx, vm, dir, manifest, targetArchitecture, validatePair)
		if err != nil {
			if errors.Is(err, errVMJailerUpgradeIncompatibleCacheCandidate) {
				continue
			}
			return vmJailerCandidate{}, false, err
		}
		return candidate, true, nil
	}
	return vmJailerCandidate{}, false, nil
}

func cachedVMUpgradeJailerInputs(vm vmJailerUpgradeVM, cacheRoot string, validatePair func(context.Context, string, string) error) (string, []os.DirEntry, error) {
	if validatePair == nil {
		return "", nil, fmt.Errorf("VM jailer runtime-pair validator is required")
	}
	targetArchitecture, err := normalizeVMImageArchitecture(vm.Architecture)
	if err != nil {
		return "", nil, err
	}
	entries, err := readVMImageCacheEntries(cacheRoot)
	if err != nil {
		return "", nil, err
	}
	return targetArchitecture, entries, nil
}

func validateCachedVMUpgradeJailerCandidate(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	dir string,
	manifest vmImageManifest,
	targetArchitecture string,
	validatePair func(context.Context, string, string) error,
) (vmJailerCandidate, error) {
	architecture, err := normalizeVMImageArchitecture(manifest.Architecture)
	if err != nil {
		return vmJailerCandidate{}, fmt.Errorf("cached VM jailer %s architecture: %w", dir, err)
	}
	if err := classifyCachedVMUpgradeJailerArchitecture(architecture, targetArchitecture); err != nil {
		return vmJailerCandidate{}, err
	}
	firecracker := filepath.Join(dir, manifest.Firecracker)
	jailer := filepath.Join(dir, manifest.Jailer)
	if err := verifyVMImageArtifactChecksum(firecracker, manifest.Firecracker, manifest.Checksums[manifest.Firecracker]); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("verify cached VM Firecracker: %w", err)
	}
	if err := verifyVMImageArtifactChecksum(jailer, manifest.Jailer, manifest.Checksums[manifest.Jailer]); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("verify cached VM jailer: %w", err)
	}
	if err := validatePair(ctx, firecracker, jailer); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("validate cached Firecracker/jailer pair: %w", err)
	}
	if err := validatePair(ctx, vm.Firecracker, jailer); err != nil {
		if errors.Is(err, errVMJailerRuntimeVersionMismatch) && strings.TrimSpace(manifest.Version) != strings.TrimSpace(vm.ImageVersion) {
			return vmJailerCandidate{}, fmt.Errorf("%w: validate cached jailer against target Firecracker: %w", errVMJailerUpgradeIncompatibleCacheCandidate, err)
		}
		return vmJailerCandidate{}, fmt.Errorf("validate cached jailer against target Firecracker: %w", err)
	}
	return vmJailerCandidate{
		Path:         jailer,
		ArtifactName: manifest.Jailer,
		SHA256:       manifest.Checksums[manifest.Jailer],
		Architecture: architecture,
	}, nil
}

func classifyCachedVMUpgradeJailerArchitecture(candidateArchitecture, targetArchitecture string) error {
	if candidateArchitecture == targetArchitecture {
		return nil
	}
	return fmt.Errorf("%w: cached VM jailer architecture %q does not match target architecture %q", errVMJailerUpgradeIncompatibleCacheCandidate, candidateArchitecture, targetArchitecture)
}

func fetchOfficialVMUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM, cache vmImageCache, validatePair func(context.Context, string, string) error) (vmJailerCandidate, error) {
	if validatePair == nil {
		return vmJailerCandidate{}, fmt.Errorf("VM jailer runtime-pair validator is required")
	}
	family, targetArchitecture, err := officialVMUpgradeJailerFamily(ctx, vm, cache)
	if err != nil {
		return vmJailerCandidate{}, err
	}
	manifestCache := cache
	manifestCache.ManifestURL = family.ManifestURL
	manifest, artifactName, architecture, err := officialVMUpgradeJailerManifest(ctx, vm, manifestCache, family, targetArchitecture)
	if err != nil {
		return vmJailerCandidate{}, err
	}
	return stageOfficialVMUpgradeJailer(ctx, vm, manifestCache, manifest, artifactName, architecture, validatePair)
}

func officialVMUpgradeJailerFamily(ctx context.Context, vm vmJailerUpgradeVM, cache vmImageCache) (vmImageCatalogImage, string, error) {
	catalog, err := cache.FetchCatalog(ctx)
	if err != nil {
		return vmImageCatalogImage{}, "", err
	}
	family, ok := catalog.ImageByPayload(vm.Payload)
	if !ok {
		return vmImageCatalogImage{}, "", fmt.Errorf("%w: %s", errVMJailerUpgradeUnknownPayload, vm.Payload)
	}
	if err := validateTrustedVMImageRepoURL(family.ManifestURL, "manifest"); err != nil {
		return vmImageCatalogImage{}, "", err
	}
	targetArchitecture, err := normalizeVMImageArchitecture(vm.Architecture)
	if err != nil {
		return vmImageCatalogImage{}, "", err
	}
	familyArchitecture, err := normalizeVMImageArchitecture(family.Architecture)
	if err != nil {
		return vmImageCatalogImage{}, "", err
	}
	if familyArchitecture != targetArchitecture {
		return vmImageCatalogImage{}, "", fmt.Errorf("official VM image architecture %q does not match target architecture %q", familyArchitecture, targetArchitecture)
	}
	return family, targetArchitecture, nil
}

func officialVMUpgradeJailerManifest(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	cache vmImageCache,
	family vmImageCatalogImage,
	targetArchitecture string,
) (vmImageManifest, string, string, error) {
	manifest, err := cache.fetchValidatedManifest(ctx)
	if err != nil {
		return vmImageManifest{}, "", "", err
	}
	if err := validateVMImageManifestCatalogFamily(manifest, family, vm.Payload); err != nil {
		return vmImageManifest{}, "", "", err
	}
	manifestArchitecture, err := normalizeVMImageArchitecture(manifest.Architecture)
	if err != nil {
		return vmImageManifest{}, "", "", err
	}
	if manifestArchitecture != targetArchitecture {
		return vmImageManifest{}, "", "", fmt.Errorf("official VM jailer architecture %q does not match target architecture %q", manifestArchitecture, targetArchitecture)
	}
	artifactName := strings.TrimSpace(manifest.Jailer)
	if artifactName == "" {
		return vmImageManifest{}, "", "", fmt.Errorf("official VM image manifest for %s does not declare a jailer", vm.Payload)
	}
	return manifest, artifactName, manifestArchitecture, nil
}

func stageOfficialVMUpgradeJailer(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	cache vmImageCache,
	manifest vmImageManifest,
	artifactName string,
	architecture string,
	validatePair func(context.Context, string, string) error,
) (vmJailerCandidate, error) {
	stagingRoot := filepath.Join(filepath.Clean(cache.Root), "upgrade-jailers")
	stagingDir := filepath.Join(stagingRoot, manifest.Version)
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stagingDir)
			_ = os.Remove(stagingRoot)
		}
	}()
	path, err := cache.ensureArtifact(ctx, stagingDir, manifest, artifactName, nil, nil)
	if err != nil {
		return vmJailerCandidate{}, err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("chmod staged VM jailer: %w", err)
	}
	if err := verifyVMImageArtifactChecksum(path, artifactName, manifest.Checksums[artifactName]); err != nil {
		return vmJailerCandidate{}, err
	}
	if err := validatePair(ctx, vm.Firecracker, path); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("validate official jailer against target Firecracker: %w", err)
	}
	cleanup = false
	return vmJailerCandidate{
		Path:         path,
		ArtifactName: artifactName,
		SHA256:       manifest.Checksums[artifactName],
		Architecture: architecture,
	}, nil
}

type vmJailerUpgradeInstallOps struct {
	copy               func(io.Writer, io.Reader) (int64, error)
	fchown             func(*os.File, int, int) error
	fchmod             func(*os.File, os.FileMode) error
	publishNoReplace   func(*os.File, *os.File, string, string) error
	verifyFileChecksum func(*os.File, string, string) error
	validatePair       func(context.Context, string, string) error
	beforePublish      func(*os.File, string) error
	afterPublish       func(*os.File) error
	trustedDirUID      uint32
}

func defaultVMJailerUpgradeInstallOps() vmJailerUpgradeInstallOps {
	return vmJailerUpgradeInstallOps{
		copy: io.Copy,
		fchown: func(file *os.File, uid, gid int) error {
			return file.Chown(uid, gid)
		},
		fchmod: func(file *os.File, mode os.FileMode) error {
			return file.Chmod(mode)
		},
		publishNoReplace:   publishOpenVMJailerNoReplace,
		verifyFileChecksum: verifyOpenVMImageArtifactChecksum,
		validatePair:       validateVMJailerRuntimePair,
		trustedDirUID:      0,
	}
}

func installUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM, candidate vmJailerCandidate) (string, error) {
	return installUpgradeJailerWithOps(ctx, vm, candidate, defaultVMJailerUpgradeInstallOps())
}

func installUpgradeJailerWithOps(ctx context.Context, vm vmJailerUpgradeVM, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) (_ string, retErr error) {
	target, err := validateUpgradeJailerInstallInput(ctx, vm, candidate, ops)
	if err != nil {
		return "", err
	}
	targetDir := filepath.Dir(target)
	return withLockedVMJailerUpgradeDir(ctx, targetDir, ops.trustedDirUID, func(dir *os.File, dirID vmJailerFileIdentity) (string, error) {
		return installUpgradeJailerInLockedDir(ctx, vm, candidate, target, targetDir, dir, dirID, ops)
	})
}

func withLockedVMJailerUpgradeDir(
	ctx context.Context,
	targetDir string,
	trustedUID uint32,
	fn func(*os.File, vmJailerFileIdentity) (string, error),
) (_ string, retErr error) {
	dir, dirID, err := openValidatedVMJailerUpgradeDir(targetDir, trustedUID)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close VM jailer target directory: %w", closeErr))
		}
	}()
	if err := acquireVMJailerUpgradeDirLock(ctx, dir); err != nil {
		return "", err
	}
	defer func() {
		if unlockErr := releaseVMJailerUpgradeDirLock(dir); unlockErr != nil {
			retErr = errors.Join(retErr, unlockErr)
		}
	}()
	return fn(dir, dirID)
}

func installUpgradeJailerInLockedDir(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	candidate vmJailerCandidate,
	target string,
	targetDir string,
	dir *os.File,
	dirID vmJailerFileIdentity,
	ops vmJailerUpgradeInstallOps,
) (_ string, retErr error) {
	temp, tempName, tempID, err := prepareUpgradeJailerTempAt(dir, candidate, ops)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := temp.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close staged VM jailer: %w", closeErr))
		}
	}()
	tempPublished := false
	defer func() {
		if tempPublished {
			return
		}
		if cleanupErr := unlinkVMJailerNameIfIdentity(dir, tempName, tempID); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()
	installedID, err := publishUpgradeJailerNoReplace(dir, temp, tempName, filepath.Base(target), ops)
	if err != nil {
		return "", err
	}
	tempPublished = true
	if _, err := validatePublishedUpgradeJailer(ctx, vm, candidate, target, targetDir, dir, dirID, installedID, ops); err != nil {
		return "", err
	}
	return target, nil
}

func validateUpgradeJailerInstallInput(ctx context.Context, vm vmJailerUpgradeVM, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	targetArchitecture, err := normalizeVMImageArchitecture(vm.Architecture)
	if err != nil {
		return "", err
	}
	candidateArchitecture, err := normalizeVMImageArchitecture(candidate.Architecture)
	if err != nil {
		return "", err
	}
	if candidateArchitecture != targetArchitecture {
		return "", fmt.Errorf("VM jailer candidate architecture %q does not match target architecture %q", candidateArchitecture, targetArchitecture)
	}
	if err := validateVMImageArtifactName(candidate.ArtifactName); err != nil {
		return "", err
	}
	if err := verifyVMImageArtifactChecksum(candidate.Path, candidate.ArtifactName, candidate.SHA256); err != nil {
		return "", err
	}
	if ops.validatePair == nil {
		return "", fmt.Errorf("VM jailer runtime-pair validator is required")
	}
	if err := ops.validatePair(ctx, vm.Firecracker, candidate.Path); err != nil {
		return "", fmt.Errorf("validate VM jailer candidate against target Firecracker: %w", err)
	}
	return vmUpgradeJailerTarget(vm)
}

func vmUpgradeJailerTarget(vm vmJailerUpgradeVM) (string, error) {
	target := filepath.Join(filepath.Dir(filepath.Clean(vm.Firecracker)), "jailer")
	if strings.TrimSpace(vm.Jailer) != "" && filepath.Clean(vm.Jailer) != target {
		return "", fmt.Errorf("VM jailer target %s is not beside Firecracker %s", vm.Jailer, vm.Firecracker)
	}
	return target, nil
}

type vmJailerFileIdentity struct {
	dev uint64
	ino uint64
}

func openValidatedVMJailerUpgradeDir(path string, trustedUID uint32) (*os.File, vmJailerFileIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, vmJailerFileIdentity{}, fmt.Errorf("open VM jailer target directory without following symlinks: %w", err)
	}
	dir := os.NewFile(uintptr(fd), path)
	if dir == nil {
		_ = unix.Close(fd)
		return nil, vmJailerFileIdentity{}, fmt.Errorf("bind VM jailer target directory file descriptor")
	}
	id, stat, err := vmJailerFileIdentityForFile(dir)
	if err != nil {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, err)
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, fmt.Errorf("VM jailer target is not a directory"))
	}
	if stat.Uid != trustedUID {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, fmt.Errorf("VM jailer target directory owner is %d, want %d", stat.Uid, trustedUID))
	}
	if mode&0o022 != 0 {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, fmt.Errorf("VM jailer target directory is writable by group or others: mode %o", mode&0o7777))
	}
	return dir, id, nil
}

func acquireVMJailerUpgradeDirLock(ctx context.Context, dir *os.File) error {
	for {
		err := unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock VM jailer target directory: %w", err)
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
		return fmt.Errorf("unlock VM jailer target directory: %w", err)
	}
	return nil
}

func prepareUpgradeJailerTempAt(dir *os.File, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) (*os.File, string, vmJailerFileIdentity, error) {
	source, err := os.Open(candidate.Path)
	if err != nil {
		return nil, "", vmJailerFileIdentity{}, fmt.Errorf("open VM jailer candidate: %w", err)
	}
	defer func() { _ = source.Close() }()
	temp, tempName, err := createVMJailerTempAt(dir)
	if err != nil {
		return nil, "", vmJailerFileIdentity{}, err
	}
	tempID, _, err := vmJailerFileIdentityForFile(temp)
	if err != nil {
		cause := errors.Join(err, fmt.Errorf("leave safely named temporary VM jailer %q because its inode identity is unavailable", tempName))
		if closeErr := temp.Close(); closeErr != nil {
			cause = errors.Join(cause, fmt.Errorf("close unidentified temporary VM jailer: %w", closeErr))
		}
		return nil, "", vmJailerFileIdentity{}, cause
	}
	if err := writeAndValidateUpgradeJailerTemp(source, temp, tempID, candidate, ops); err != nil {
		return cleanupFailedUpgradeJailerTemp(dir, temp, tempName, tempID, err)
	}
	return temp, tempName, tempID, nil
}

func writeAndValidateUpgradeJailerTemp(source, temp *os.File, tempID vmJailerFileIdentity, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) error {
	if _, err := ops.copy(temp, source); err != nil {
		return fmt.Errorf("copy VM jailer candidate: %w", err)
	}
	if err := secureUpgradeJailerTemp(temp, ops); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary VM jailer: %w", err)
	}
	if err := ops.verifyFileChecksum(temp, candidate.ArtifactName, candidate.SHA256); err != nil {
		return err
	}
	id, stat, err := vmJailerFileIdentityForFile(temp)
	if err != nil {
		return err
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("staged VM jailer is not a regular file")
	}
	if id != tempID {
		return fmt.Errorf("staged VM jailer file descriptor inode changed")
	}
	return nil
}

func cleanupFailedUpgradeJailerTemp(dir, temp *os.File, tempName string, tempID vmJailerFileIdentity, cause error) (*os.File, string, vmJailerFileIdentity, error) {
	if cleanupErr := unlinkVMJailerNameIfIdentity(dir, tempName, tempID); cleanupErr != nil {
		cause = errors.Join(cause, cleanupErr)
	}
	if closeErr := temp.Close(); closeErr != nil {
		cause = errors.Join(cause, fmt.Errorf("close failed staged VM jailer: %w", closeErr))
	}
	return nil, "", vmJailerFileIdentity{}, cause
}

func secureUpgradeJailerTemp(temp *os.File, ops vmJailerUpgradeInstallOps) error {
	if err := ops.fchown(temp, 0, 0); err != nil {
		return fmt.Errorf("chown temporary VM jailer: %w", err)
	}
	if err := ops.fchmod(temp, 0o755); err != nil {
		return fmt.Errorf("chmod temporary VM jailer: %w", err)
	}
	return nil
}

func createVMJailerTempAt(dir *os.File) (*os.File, string, error) {
	for range 128 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate temporary VM jailer name: %w", err)
		}
		name := ".jailer.tmp-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create temporary VM jailer relative to target directory: %w", err)
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			return nil, "", cleanupUnboundVMJailerTemp(dir, fd, name, fmt.Errorf("bind temporary VM jailer file descriptor"))
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("create temporary VM jailer: exhausted unique names")
}

func cleanupUnboundVMJailerTemp(dir *os.File, fd int, name string, cause error) error {
	var stat unix.Stat_t
	statErr := unix.Fstat(fd, &stat)
	if closeErr := unix.Close(fd); closeErr != nil {
		cause = errors.Join(cause, fmt.Errorf("close unbound temporary VM jailer: %w", closeErr))
	}
	if statErr != nil {
		return errors.Join(cause, fmt.Errorf("leave safely named temporary VM jailer %q because its inode identity is unavailable: %w", name, statErr))
	}
	id := vmJailerFileIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}
	if cleanupErr := unlinkVMJailerNameIfIdentity(dir, name, id); cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	return cause
}

func publishUpgradeJailerNoReplace(dir, temp *os.File, tempName, targetName string, ops vmJailerUpgradeInstallOps) (vmJailerFileIdentity, error) {
	tempID, _, err := vmJailerFileIdentityForFile(temp)
	if err != nil {
		return vmJailerFileIdentity{}, err
	}
	if ops.beforePublish != nil {
		if err := ops.beforePublish(dir, tempName); err != nil {
			return vmJailerFileIdentity{}, fmt.Errorf("before VM jailer publish: %w", err)
		}
	}
	if err := validateUpgradeJailerTempName(dir, tempName, tempID); err != nil {
		return vmJailerFileIdentity{}, err
	}
	if err := ops.publishNoReplace(dir, temp, tempName, targetName); err != nil {
		return vmJailerFileIdentity{}, formatVMJailerPublishError(err)
	}
	installedID, err := finishUpgradeJailerPublish(dir, tempName, targetName, tempID)
	if err != nil {
		return vmJailerFileIdentity{}, cleanupFailedUpgradeJailerPublish(dir, targetName, tempID, err)
	}
	return installedID, nil
}

func validateUpgradeJailerTempName(dir *os.File, tempName string, tempID vmJailerFileIdentity) error {
	boundNameID, _, err := vmJailerNameIdentityAt(dir, tempName)
	if err != nil {
		return fmt.Errorf("inspect temporary VM jailer before publish: %w", err)
	}
	if boundNameID != tempID {
		return fmt.Errorf("temporary VM jailer inode changed before publish")
	}
	return nil
}

func formatVMJailerPublishError(err error) error {
	if errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("publish VM jailer without replacing target: target already exists: %w", err)
	}
	return fmt.Errorf("publish VM jailer without replacing target: %w", err)
}

func finishUpgradeJailerPublish(dir *os.File, tempName, targetName string, tempID vmJailerFileIdentity) (vmJailerFileIdentity, error) {
	installedID, _, err := vmJailerNameIdentityAt(dir, targetName)
	if err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("inspect published VM jailer: %w", err)
	}
	if installedID != tempID {
		return vmJailerFileIdentity{}, fmt.Errorf("published VM jailer inode does not match staged inode")
	}
	if err := unlinkVMJailerNameIfIdentity(dir, tempName, tempID); err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("remove temporary VM jailer name after publish: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("sync VM jailer target directory: %w", err)
	}
	return installedID, nil
}

func cleanupFailedUpgradeJailerPublish(dir *os.File, targetName string, installedID vmJailerFileIdentity, cause error) error {
	if cleanupErr := unlinkVMJailerNameIfIdentity(dir, targetName, installedID); cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	return cause
}

func validatePublishedUpgradeJailer(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	candidate vmJailerCandidate,
	target string,
	targetDir string,
	dir *os.File,
	dirID vmJailerFileIdentity,
	installedID vmJailerFileIdentity,
	ops vmJailerUpgradeInstallOps,
) (string, error) {
	if err := inspectPublishedUpgradeJailer(ctx, vm, candidate, target, targetDir, dir, dirID, installedID, ops); err != nil {
		return "", cleanupFailedUpgradeJailerPublish(dir, filepath.Base(target), installedID, err)
	}
	return target, nil
}

func inspectPublishedUpgradeJailer(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	candidate vmJailerCandidate,
	target string,
	targetDir string,
	dir *os.File,
	dirID vmJailerFileIdentity,
	installedID vmJailerFileIdentity,
	ops vmJailerUpgradeInstallOps,
) error {
	if err := runVMJailerAfterPublishHook(dir, ops.afterPublish); err != nil {
		return err
	}
	if err := validateVMJailerUpgradeDirPathIdentity(targetDir, dirID, ops.trustedDirUID); err != nil {
		return err
	}
	installed, err := openVMJailerNameAt(dir, filepath.Base(target), installedID)
	if err != nil {
		return err
	}
	if err := ops.verifyFileChecksum(installed, candidate.ArtifactName, candidate.SHA256); err != nil {
		return closeVMJailerFileOnError(installed, err)
	}
	if err := installed.Close(); err != nil {
		return fmt.Errorf("close published VM jailer: %w", err)
	}
	if err := ops.validatePair(ctx, vm.Firecracker, target); err != nil {
		return fmt.Errorf("validate installed VM jailer: %w", err)
	}
	if err := validateVMJailerUpgradeDirPathIdentity(targetDir, dirID, ops.trustedDirUID); err != nil {
		return err
	}
	currentID, _, err := vmJailerNameIdentityAt(dir, filepath.Base(target))
	if err != nil {
		return fmt.Errorf("inspect installed VM jailer after validation: %w", err)
	}
	if currentID != installedID {
		return fmt.Errorf("installed VM jailer inode changed during validation")
	}
	return nil
}

func runVMJailerAfterPublishHook(dir *os.File, hook func(*os.File) error) error {
	if hook == nil {
		return nil
	}
	if err := hook(dir); err != nil {
		return fmt.Errorf("after VM jailer publish: %w", err)
	}
	return nil
}

func validateVMJailerUpgradeDirPathIdentity(path string, want vmJailerFileIdentity, trustedUID uint32) error {
	dir, got, err := openValidatedVMJailerUpgradeDir(path, trustedUID)
	if err != nil {
		return fmt.Errorf("target directory changed after VM jailer publish: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close revalidated VM jailer target directory: %w", err)
	}
	if got != want {
		return fmt.Errorf("target directory changed after VM jailer publish")
	}
	return nil
}

func openVMJailerNameAt(dir *os.File, name string, want vmJailerFileIdentity) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open published VM jailer relative to target directory: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind published VM jailer file descriptor")
	}
	got, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return nil, closeVMJailerFileOnError(file, err)
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return nil, closeVMJailerFileOnError(file, fmt.Errorf("published VM jailer inode changed before validation"))
	}
	return file, nil
}

func vmJailerFileIdentityForFile(file *os.File) (vmJailerFileIdentity, unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("inspect VM jailer file descriptor: %w", err)
	}
	return vmJailerFileIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, stat, nil
}

func vmJailerNameIdentityAt(dir *os.File, name string) (vmJailerFileIdentity, unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, err
	}
	return vmJailerFileIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, stat, nil
}

func unlinkVMJailerNameIfIdentity(dir *os.File, name string, want vmJailerFileIdentity) error {
	return unlinkVMJailerNameIfIdentityWith(dir, name, want, unix.Unlinkat)
}

func unlinkVMJailerNameIfIdentityWith(dir *os.File, name string, want vmJailerFileIdentity, unlinkAt func(int, string, int) error) error {
	got, _, err := vmJailerNameIdentityAt(dir, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect VM jailer cleanup target %q: %w", name, err)
	}
	if got != want {
		return fmt.Errorf("refuse to remove VM jailer cleanup target %q: inode changed", name)
	}
	if err := unlinkAt(int(dir.Fd()), name, 0); err != nil {
		return fmt.Errorf("remove VM jailer cleanup target %q: %w", name, err)
	}
	return nil
}

func verifyOpenVMImageArtifactChecksum(file *os.File, artifactName, want string) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek VM image artifact %q for checksum: %w", artifactName, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("verify downloaded VM image artifact %q: %w", artifactName, err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("VM image artifact %q checksum mismatch: got %s, want %s", artifactName, got, want)
	}
	return nil
}

func closeVMJailerFileOnError(file *os.File, cause error) error {
	if err := file.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close VM jailer file descriptor: %w", err))
	}
	return cause
}
