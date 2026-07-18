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
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

const hostStorageTransactionVersion = 1

type hostStorageTransactionPhase string

const (
	hostStoragePhasePrepared             hostStorageTransactionPhase = "prepared"
	hostStoragePhaseServicesStopped      hostStorageTransactionPhase = "services-stopped"
	hostStoragePhaseTargetReady          hostStorageTransactionPhase = "target-ready"
	hostStoragePhaseCatchSwitching       hostStorageTransactionPhase = "catch-switching"
	hostStoragePhaseSourceRestartPending hostStorageTransactionPhase = "source-restart-pending"
	hostStoragePhaseCatchSwitched        hostStorageTransactionPhase = "catch-switched"
	hostStoragePhaseValidated            hostStorageTransactionPhase = "validated"
	hostStoragePhaseCleanupPending       hostStorageTransactionPhase = "cleanup-pending"
	hostStoragePhaseComplete             hostStorageTransactionPhase = "complete"
	hostStoragePhaseRolledBack           hostStorageTransactionPhase = "rolled-back"
)

const (
	hostStorageCatchAuthoritySource = "source"
	hostStorageCatchAuthorityTarget = "target"
)

type hostStorageTransaction struct {
	Version           int                         `json:"version"`
	ID                string                      `json:"id"`
	Phase             hostStorageTransactionPhase `json:"phase"`
	Plan              catchrpc.HostStoragePlan    `json:"plan"`
	SourceJournal     string                      `json:"sourceJournal"`
	TargetJournal     string                      `json:"targetJournal,omitempty"`
	DatabaseBackup    string                      `json:"databaseBackup"`
	UnitBackups       map[string]string           `json:"unitBackups"`
	PreviouslyRunning []string                    `json:"previouslyRunning"`
	StoppedServices   []string                    `json:"stoppedServices,omitempty"`
	RestartedServices []string                    `json:"restartedServices,omitempty"`
	CatchAuthority    string                      `json:"catchAuthority"`
	LastError         string                      `json:"lastError,omitempty"`
}

var (
	hostStorageFsyncParentFn    = fsyncParent
	hostStorageSyncRootFn       = syncHostStorageRoot
	hostStorageOpenBackupRootFn = os.OpenRoot
	hostStorageChownFn          = os.Chown
	hostStorageIDPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	hostStorageShellSafePattern = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
)

func hostStorageTransactionPath(dataDir, id string) string {
	return filepath.Join(dataDir, "migrations", "host-storage", id, "transaction.json")
}

func createHostStorageTransaction(ctx context.Context, plan catchrpc.HostStoragePlan, databasePath string, unitPaths, previouslyRunning []string) (*hostStorageTransaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := newPreparedHostStorageTransaction(plan, previouslyRunning)
	if err != nil {
		return nil, err
	}
	if err := backupHostStorageTransactionState(tx, databasePath, unitPaths); err != nil {
		return nil, err
	}
	if err := writeHostStorageTransactionFile(tx.SourceJournal, tx); err != nil {
		return nil, fmt.Errorf("write prepared host storage transaction: %w", err)
	}
	if err := mirrorPreparedHostStorageTransaction(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

func newPreparedHostStorageTransaction(plan catchrpc.HostStoragePlan, previouslyRunning []string) (*hostStorageTransaction, error) {
	source := cleanHostStoragePath(plan.Current.DataDir)
	if source == "" || !filepath.IsAbs(source) {
		return nil, fmt.Errorf("host storage transaction source must be absolute, got %q", plan.Current.DataDir)
	}
	id, err := newHostStorageTransactionID()
	if err != nil {
		return nil, err
	}
	tx := &hostStorageTransaction{
		Version:           hostStorageTransactionVersion,
		ID:                id,
		Phase:             hostStoragePhasePrepared,
		Plan:              plan,
		SourceJournal:     hostStorageTransactionPath(source, id),
		UnitBackups:       make(map[string]string),
		PreviouslyRunning: uniqueSortedStrings(previouslyRunning),
		CatchAuthority:    hostStorageCatchAuthoritySource,
	}
	target := cleanHostStoragePath(plan.Desired.DataDir)
	if target != "" && !hostStoragePathsEqual(source, target) {
		if !filepath.IsAbs(target) {
			return nil, fmt.Errorf("host storage transaction target must be absolute, got %q", plan.Desired.DataDir)
		}
		tx.TargetJournal = hostStorageTransactionPath(target, id)
	}
	txDir := filepath.Dir(tx.SourceJournal)
	tx.DatabaseBackup = filepath.Join(txDir, "db.before.json")
	return tx, nil
}

func backupHostStorageTransactionState(tx *hostStorageTransaction, databasePath string, unitPaths []string) error {
	if err := copyHostStorageTransactionFile(databasePath, tx.DatabaseBackup, 0o600); err != nil {
		return fmt.Errorf("back up host storage database: %w", err)
	}
	if err := backupHostStorageTransactionUnits(tx, unitPaths); err != nil {
		return err
	}
	return nil
}

func mirrorPreparedHostStorageTransaction(tx *hostStorageTransaction) error {
	if tx.TargetJournal == "" || tx.Plan.Desired.DataDirZFS {
		return nil
	}
	if err := writeHostStorageTransactionFile(tx.TargetJournal, tx); err != nil {
		mirrorErr := fmt.Errorf("mirror prepared host storage transaction: %w", err)
		tx.Phase = hostStoragePhaseRolledBack
		tx.LastError = mirrorErr.Error()
		if terminalErr := writeHostStorageTransactionFile(tx.SourceJournal, tx); terminalErr != nil {
			return errors.Join(mirrorErr, fmt.Errorf("record failed host storage transaction: %w", terminalErr))
		}
		return mirrorErr
	}
	return nil
}

func newHostStorageTransactionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate host storage transaction id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func backupHostStorageTransactionUnits(tx *hostStorageTransaction, unitPaths []string) error {
	unitPaths = uniqueSortedStrings(unitPaths)
	unitsDir := filepath.Join(filepath.Dir(tx.SourceJournal), "units")
	for _, unitPath := range unitPaths {
		unitPath = filepath.Clean(strings.TrimSpace(unitPath))
		if unitPath == "." || !filepath.IsAbs(unitPath) {
			return fmt.Errorf("host storage unit path must be absolute, got %q", unitPath)
		}
		info, err := os.Lstat(unitPath)
		if errors.Is(err, os.ErrNotExist) {
			tx.UnitBackups[unitPath] = ""
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect host storage unit %s: %w", unitPath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("host storage unit %s is not a regular file", unitPath)
		}
		digest := sha256.Sum256([]byte(unitPath))
		backup := filepath.Join(unitsDir, hex.EncodeToString(digest[:])+".unit")
		if err := copyHostStorageTransactionFile(unitPath, backup, 0o600); err != nil {
			return fmt.Errorf("back up host storage unit %s: %w", unitPath, err)
		}
		tx.UnitBackups[unitPath] = backup
	}
	return nil
}

func advanceHostStorageTransaction(tx *hostStorageTransaction, phase hostStorageTransactionPhase) error {
	if tx == nil {
		return fmt.Errorf("host storage transaction is nil")
	}
	return advanceHostStorageTransactionState(tx, phase, tx.CatchAuthority)
}

func advanceHostStorageTransactionState(tx *hostStorageTransaction, phase hostStorageTransactionPhase, catchAuthority string) error {
	if tx == nil {
		return fmt.Errorf("host storage transaction is nil")
	}
	if !validHostStorageTransactionPhase(phase) {
		return fmt.Errorf("unknown host storage transaction phase %q", phase)
	}
	if catchAuthority != hostStorageCatchAuthoritySource && catchAuthority != hostStorageCatchAuthorityTarget {
		return fmt.Errorf("unknown host storage catch authority %q", catchAuthority)
	}
	previous := *tx
	tx.Phase = phase
	tx.CatchAuthority = catchAuthority
	if err := persistHostStorageTransaction(tx); err != nil {
		if durable, loadErr := loadHostStorageTransaction(tx.SourceJournal); loadErr == nil {
			*tx = *durable
		} else {
			*tx = previous
		}
		return err
	}
	return nil
}

func persistHostStorageTransaction(tx *hostStorageTransaction) error {
	if err := writeHostStorageTransactionFile(tx.SourceJournal, tx); err != nil {
		return err
	}
	if hostStorageTargetJournalReady(tx) {
		if err := writeHostStorageTransactionFile(tx.TargetJournal, tx); err != nil {
			return err
		}
	}
	return nil
}

func hostStorageTargetJournalReady(tx *hostStorageTransaction) bool {
	if tx == nil || tx.TargetJournal == "" {
		return false
	}
	if !tx.Plan.Desired.DataDirZFS {
		return true
	}
	_, err := os.Stat(cleanHostStoragePath(tx.Plan.Desired.DataDir))
	return err == nil
}

func writeHostStorageTransactionFile(path string, tx *hostStorageTransaction) error {
	raw, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeHostStorageTransactionBytes(path, raw, 0o600)
}

func writeHostStorageTransactionBytes(path string, raw []byte, mode os.FileMode) error {
	tree, relative, err := openHostStorageTransactionTree(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = tree.Close() }()
	parent, name, closeParent, err := tree.openFileParent(relative, true)
	if err != nil {
		return err
	}
	defer closeParent()
	if err := validateExistingHostStorageManagedFile(parent, name, path); err != nil {
		return err
	}
	if err := replaceHostStorageManagedFile(parent, name, path, raw, mode); err != nil {
		return err
	}
	return tree.verifyStillNamed(path)
}

func validateExistingHostStorageManagedFile(parent *os.Root, name, path string) error {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateHostStorageManagedFileInfo(path, info)
}

func replaceHostStorageManagedFile(parent *os.Root, name, path string, raw []byte, mode os.FileMode) error {
	suffix, err := newHostStorageTransactionID()
	if err != nil {
		return err
	}
	tmpName := ".transaction-" + strings.ReplaceAll(filepath.Base(path), ".", "-") + "-" + suffix
	defer func() { _ = parent.Remove(tmpName) }()
	if err := writeHostStorageManagedTempFile(parent, tmpName, raw, mode); err != nil {
		return err
	}
	if err := parent.Rename(tmpName, name); err != nil {
		return err
	}
	if err := hostStorageSyncRootFn(parent); err != nil {
		return err
	}
	return hostStorageFsyncParentFn(path)
}

func writeHostStorageManagedTempFile(parent *os.Root, name string, raw []byte, mode os.FileMode) error {
	tmp, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	return tmp.Close()
}

func syncHostStorageRoot(root *os.Root) error {
	parentFile, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := parentFile.Sync()
	closeErr := parentFile.Close()
	return errors.Join(syncErr, closeErr)
}

func copyHostStorageTransactionFile(source, target string, mode os.FileMode) error {
	raw, err := readHostStorageBackupInput(source)
	if err != nil {
		return err
	}
	return writeHostStorageTransactionBytes(target, raw, mode)
}

func writeHostStorageRestoredFile(path string, raw []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".host-storage-restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return hostStorageFsyncParentFn(path)
}

func fsyncParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

type hostStorageTransactionTree struct {
	dataRoot *os.Root
	txRoot   *os.Root
	txInfo   os.FileInfo
	roots    []*os.Root
}

type hostStorageTransactionContainer struct {
	root  *os.Root
	roots []*os.Root
}

func (c *hostStorageTransactionContainer) Close() error {
	var closeErr error
	for i := len(c.roots) - 1; i >= 0; i-- {
		closeErr = errors.Join(closeErr, c.roots[i].Close())
	}
	return closeErr
}

func (t *hostStorageTransactionTree) Close() error {
	var closeErr error
	for i := len(t.roots) - 1; i >= 0; i-- {
		closeErr = errors.Join(closeErr, t.roots[i].Close())
	}
	return closeErr
}

func (t *hostStorageTransactionTree) openFileParent(relative string, create bool) (*os.Root, string, func(), error) {
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, "", func() {}, fmt.Errorf("invalid host storage transaction path %q", relative)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	parent := t.txRoot
	var opened []*os.Root
	closeOpened := func() {
		for i := len(opened) - 1; i >= 0; i-- {
			_ = opened[i].Close()
		}
	}
	for _, part := range parts[:len(parts)-1] {
		child, _, err := openHostStorageManagedDirectory(parent, part, filepath.Join(t.dataRoot.Name(), "migrations", "host-storage", part), create)
		if err != nil {
			closeOpened()
			return nil, "", func() {}, err
		}
		opened = append(opened, child)
		parent = child
	}
	return parent, parts[len(parts)-1], closeOpened, nil
}

func (t *hostStorageTransactionTree) verifyStillNamed(path string) error {
	fresh, relative, err := openHostStorageTransactionTree(path, false)
	if err != nil {
		return fmt.Errorf("host storage transaction directory was replaced: %w", err)
	}
	defer func() { _ = fresh.Close() }()
	if !os.SameFile(t.txInfo, fresh.txInfo) {
		return fmt.Errorf("host storage transaction directory %s was replaced", filepath.Dir(path))
	}
	parent, name, closeParent, err := fresh.openFileParent(relative, false)
	if err != nil {
		return err
	}
	defer closeParent()
	file, err := openHostStorageManagedFile(parent, name, path)
	if err != nil {
		return err
	}
	return file.Close()
}

func openHostStorageTransactionTree(path string, create bool) (*hostStorageTransactionTree, string, error) {
	dataDir, id, relative, err := splitHostStorageTransactionPath(path)
	if err != nil {
		return nil, "", err
	}
	dataRoot, err := openHostStorageDataRoot(dataDir, create)
	if err != nil {
		return nil, "", err
	}
	tree := &hostStorageTransactionTree{dataRoot: dataRoot, roots: []*os.Root{dataRoot}}
	fail := func(err error) (*hostStorageTransactionTree, string, error) {
		_ = tree.Close()
		return nil, "", err
	}
	parent := dataRoot
	for _, name := range []string{"migrations", "host-storage", id} {
		var display string
		switch name {
		case "migrations":
			display = filepath.Join(dataDir, name)
		case "host-storage":
			display = filepath.Join(dataDir, "migrations", name)
		default:
			display = filepath.Join(dataDir, "migrations", "host-storage", id)
		}
		child, info, openErr := openHostStorageManagedDirectory(parent, name, display, create)
		if openErr != nil {
			return fail(openErr)
		}
		tree.roots = append(tree.roots, child)
		parent = child
		if name == id {
			tree.txRoot = child
			tree.txInfo = info
		}
	}
	return tree, relative, nil
}

func splitHostStorageTransactionPath(path string) (string, string, string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return "", "", "", fmt.Errorf("host storage transaction path must be absolute, got %q", path)
	}
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	parts := strings.Split(rest, string(filepath.Separator))
	for i := len(parts) - 4; i >= 0; i-- {
		if parts[i] != "migrations" || parts[i+1] != "host-storage" || !validHostStorageTransactionID(parts[i+2]) {
			continue
		}
		dataDir := volume + string(filepath.Separator) + filepath.Join(parts[:i]...)
		if i == 0 {
			dataDir = volume + string(filepath.Separator)
		}
		return filepath.Clean(dataDir), parts[i+2], filepath.Join(parts[i+3:]...), nil
	}
	return "", "", "", fmt.Errorf("path %s is not inside a host storage transaction tree", path)
}

func openHostStorageDataRoot(path string, create bool) (*os.Root, error) {
	info, err := inspectHostStorageDataRoot(path, create)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("host storage data root %s is a symlink", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("host storage data root %s is not a directory", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if err := verifyOpenedHostStorageDataRoot(root, path, info); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func inspectHostStorageDataRoot(path string, create bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if !errors.Is(err, os.ErrNotExist) || !create {
		return info, err
	}
	if err := os.MkdirAll(path, 0o711); err != nil {
		return nil, err
	}
	return os.Lstat(path)
}

func verifyOpenedHostStorageDataRoot(root *os.Root, path string, expected os.FileInfo) error {
	opened, err := root.Open(".")
	if err != nil {
		return err
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return err
	}
	if !os.SameFile(expected, openedInfo) {
		return fmt.Errorf("host storage data root %s was replaced", path)
	}
	return nil
}

func openHostStorageManagedDirectory(parent *os.Root, name, display string, create bool) (*os.Root, os.FileInfo, error) {
	info, err := inspectHostStorageManagedDirectory(parent, name, create)
	if err != nil {
		return nil, nil, err
	}
	if err := validateHostStorageManagedDirectoryInfo(display, info, create); err != nil {
		return nil, nil, err
	}
	if create {
		if err := hostStorageSyncRootFn(parent); err != nil {
			return nil, nil, fmt.Errorf("sync parent of managed host storage transaction directory %s: %w", display, err)
		}
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, err := verifyOpenedHostStorageManagedDirectory(child, parent, name, display, info, create)
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	return child, openedInfo, nil
}

func inspectHostStorageManagedDirectory(parent *os.Root, name string, create bool) (os.FileInfo, error) {
	info, err := parent.Lstat(name)
	if !errors.Is(err, os.ErrNotExist) || !create {
		return info, err
	}
	if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return parent.Lstat(name)
}

func validateHostStorageManagedDirectoryInfo(display string, info os.FileInfo, create bool) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed host storage transaction directory %s is a symlink", display)
	}
	if !info.IsDir() {
		return fmt.Errorf("managed host storage transaction path %s is not a directory", display)
	}
	if err := validateHostStorageManagedOwner(display, info); err != nil {
		return err
	}
	if !create && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("managed host storage transaction directory %s has insecure permissions %04o", display, info.Mode().Perm())
	}
	return nil
}

func verifyOpenedHostStorageManagedDirectory(child, parent *os.Root, name, display string, expected os.FileInfo, create bool) (os.FileInfo, error) {
	opened, err := child.Open(".")
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := opened.Stat()
	var chmodErr error
	if create {
		chmodErr = opened.Chmod(0o700)
	}
	closeErr := opened.Close()
	if err := errors.Join(statErr, chmodErr, closeErr); err != nil {
		return nil, err
	}
	current, lstatErr := parent.Lstat(name)
	if lstatErr != nil || !os.SameFile(expected, openedInfo) || !os.SameFile(openedInfo, current) {
		return nil, fmt.Errorf("managed host storage transaction directory %s was replaced", display)
	}
	return openedInfo, nil
}

func validateHostStorageManagedOwner(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect managed host storage ownership for %s", path)
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("managed host storage path %s is owned by uid %d, want %d", path, stat.Uid, os.Geteuid())
	}
	return nil
}

func validateHostStorageManagedFileInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed host storage file %s is a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("managed host storage path %s is not a regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect managed host storage metadata for %s", path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("managed host storage file %s is a hardlink with link count %d", path, stat.Nlink)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("managed host storage file %s has insecure permissions %04o", path, info.Mode().Perm())
	}
	return validateHostStorageManagedOwner(path, info)
}

func openHostStorageManagedFile(parent *os.Root, name, display string) (*os.File, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := validateHostStorageManagedFileInfo(display, info); err != nil {
		return nil, err
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := file.Stat()
	current, lstatErr := parent.Lstat(name)
	if statErr != nil || lstatErr != nil || !os.SameFile(info, openedInfo) || !os.SameFile(openedInfo, current) {
		_ = file.Close()
		return nil, fmt.Errorf("managed host storage file %s was replaced", display)
	}
	return file, nil
}

func readHostStorageTransactionFile(path string) ([]byte, error) {
	tree, relative, err := openHostStorageTransactionTree(path, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tree.Close() }()
	parent, name, closeParent, err := tree.openFileParent(relative, false)
	if err != nil {
		return nil, err
	}
	defer closeParent()
	file, err := openHostStorageManagedFile(parent, name, path)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if err := tree.verifyStillNamed(path); err != nil {
		return nil, err
	}
	return raw, nil
}

func readHostStorageBackupInput(path string) ([]byte, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("host storage backup input must be absolute, got %q", path)
	}
	root, err := openHostStorageBackupInputParent(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := openHostStorageBackupInputFile(root, path)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	return raw, nil
}

func openHostStorageBackupInputParent(parentPath string) (*os.Root, error) {
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, err
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("host storage backup input parent %s is a symlink", parentPath)
	}
	if !parentInfo.IsDir() {
		return nil, fmt.Errorf("host storage backup input parent %s is not a directory", parentPath)
	}
	root, err := hostStorageOpenBackupRootFn(parentPath)
	if err != nil {
		return nil, err
	}
	if err := verifyOpenedHostStorageBackupInputParent(root, parentPath, parentInfo); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func verifyOpenedHostStorageBackupInputParent(root *os.Root, path string, expected os.FileInfo) error {
	opened, err := root.Open(".")
	if err != nil {
		return err
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	current, lstatErr := os.Lstat(path)
	if err := errors.Join(statErr, closeErr, lstatErr); err != nil {
		return err
	}
	if !os.SameFile(expected, openedInfo) || !os.SameFile(openedInfo, current) {
		return fmt.Errorf("host storage backup input parent %s was replaced", path)
	}
	return nil
}

func openHostStorageBackupInputFile(root *os.Root, path string) (*os.File, error) {
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := validateHostStorageBackupInputInfo(path, info); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := file.Stat()
	current, lstatErr := root.Lstat(name)
	if statErr != nil || lstatErr != nil || !os.SameFile(info, openedInfo) || !os.SameFile(openedInfo, current) {
		_ = file.Close()
		return nil, fmt.Errorf("host storage backup input %s was replaced", path)
	}
	return file, nil
}

func validateHostStorageBackupInputInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("host storage backup input %s is a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("host storage backup input %s is not a regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect host storage backup input %s", path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("host storage backup input %s is a hardlink with link count %d", path, stat.Nlink)
	}
	return nil
}

func loadHostStorageTransaction(path string) (*hostStorageTransaction, error) {
	raw, err := readHostStorageTransactionFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var tx hostStorageTransaction
	if err := decoder.Decode(&tx); err != nil {
		return nil, fmt.Errorf("decode host storage transaction %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("additional JSON value")
		}
		return nil, fmt.Errorf("decode host storage transaction %s: trailing data: %w", path, err)
	}
	if err := validateLoadedHostStorageTransaction(path, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

func validateLoadedHostStorageTransaction(path string, tx *hostStorageTransaction) error {
	if tx.Version != hostStorageTransactionVersion {
		return fmt.Errorf("unsupported host storage transaction version %d in %s", tx.Version, path)
	}
	if !validHostStorageTransactionID(tx.ID) {
		return fmt.Errorf("invalid host storage transaction id %q in %s", tx.ID, path)
	}
	if !validHostStorageTransactionPhase(tx.Phase) {
		return fmt.Errorf("unknown host storage transaction phase %q in %s", tx.Phase, path)
	}
	expectedSource, expectedTarget, err := expectedHostStorageTransactionJournals(path, tx)
	if err != nil {
		return err
	}
	if err := validateHostStorageTransactionFileLocations(path, tx, expectedSource, expectedTarget); err != nil {
		return err
	}
	return validateHostStorageTransactionUnitBackups(path, tx)
}

func expectedHostStorageTransactionJournals(path string, tx *hostStorageTransaction) (string, string, error) {
	sourceRoot := cleanHostStoragePath(tx.Plan.Current.DataDir)
	if sourceRoot == "" || !filepath.IsAbs(sourceRoot) {
		return "", "", fmt.Errorf("host storage transaction %s has invalid source data dir %q", path, tx.Plan.Current.DataDir)
	}
	expectedSource := hostStorageTransactionPath(sourceRoot, tx.ID)
	if !hostStoragePathsEqual(tx.SourceJournal, expectedSource) {
		return "", "", fmt.Errorf("host storage transaction %s has invalid source journal location %q", path, tx.SourceJournal)
	}
	targetRoot := cleanHostStoragePath(tx.Plan.Desired.DataDir)
	expectedTarget := ""
	if targetRoot != "" && !hostStoragePathsEqual(sourceRoot, targetRoot) {
		if !filepath.IsAbs(targetRoot) {
			return "", "", fmt.Errorf("host storage transaction %s has invalid target data dir %q", path, tx.Plan.Desired.DataDir)
		}
		expectedTarget = hostStorageTransactionPath(targetRoot, tx.ID)
	}
	if !hostStoragePathsEqual(tx.TargetJournal, expectedTarget) {
		return "", "", fmt.Errorf("host storage transaction %s has invalid target journal location %q", path, tx.TargetJournal)
	}
	return expectedSource, expectedTarget, nil
}

func validateHostStorageTransactionFileLocations(path string, tx *hostStorageTransaction, expectedSource, expectedTarget string) error {
	path = filepath.Clean(path)
	if path != filepath.Clean(expectedSource) && (expectedTarget == "" || path != filepath.Clean(expectedTarget)) {
		return fmt.Errorf("host storage transaction %s does not name the loaded journal", path)
	}
	expectedDatabaseBackup := filepath.Join(filepath.Dir(expectedSource), "db.before.json")
	if !hostStoragePathsEqual(tx.DatabaseBackup, expectedDatabaseBackup) {
		return fmt.Errorf("host storage transaction %s has invalid database backup %q", path, tx.DatabaseBackup)
	}
	if tx.CatchAuthority != hostStorageCatchAuthoritySource && tx.CatchAuthority != hostStorageCatchAuthorityTarget {
		return fmt.Errorf("host storage transaction %s has invalid catch authority %q", path, tx.CatchAuthority)
	}
	return nil
}

func validateHostStorageTransactionUnitBackups(path string, tx *hostStorageTransaction) error {
	for unitPath, backup := range tx.UnitBackups {
		if !filepath.IsAbs(unitPath) {
			return fmt.Errorf("host storage transaction %s has non-absolute unit path %q", path, unitPath)
		}
		if backup != "" && (!filepath.IsAbs(backup) || !hostStorageRootContains(filepath.Join(filepath.Dir(tx.SourceJournal), "units"), backup)) {
			return fmt.Errorf("host storage transaction %s has invalid unit backup %q", path, backup)
		}
	}
	return nil
}

func validHostStorageTransactionID(id string) bool {
	return hostStorageIDPattern.MatchString(id)
}

func validHostStorageTransactionPhase(phase hostStorageTransactionPhase) bool {
	switch phase {
	case hostStoragePhasePrepared,
		hostStoragePhaseServicesStopped,
		hostStoragePhaseTargetReady,
		hostStoragePhaseCatchSwitching,
		hostStoragePhaseSourceRestartPending,
		hostStoragePhaseCatchSwitched,
		hostStoragePhaseValidated,
		hostStoragePhaseCleanupPending,
		hostStoragePhaseComplete,
		hostStoragePhaseRolledBack:
		return true
	default:
		return false
	}
}

func resumeHostStorageTransaction(ctx context.Context, tx *hostStorageTransaction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx == nil {
		return fmt.Errorf("host storage transaction is nil")
	}
	switch tx.Phase {
	case hostStoragePhaseCleanupPending, hostStoragePhaseComplete, hostStoragePhaseRolledBack:
		return nil
	default:
		return fmt.Errorf("host storage transaction %s is unfinished in phase %q; recover it explicitly with: %s", tx.SourceJournal, tx.Phase, hostStorageTransactionRecoveryCommand(tx.Plan))
	}
}

func hostStorageTransactionRecoveryCommand(plan catchrpc.HostStoragePlan) string {
	dataChanged, servicesChanged := hostStorageRecoveryChangedTargets(plan)
	args := []string{"yeet", "host", "set"}
	args = appendHostStorageRecoveryTargetArgs(args, plan, dataChanged, servicesChanged)
	if hostStorageRecoveryTargetsUseZFS(plan, dataChanged, servicesChanged) {
		args = append(args, "--zfs")
	}
	if servicesChanged {
		args = append(args, "--migrate-services=all")
	}
	args = append(args, "--yes")
	return shellJoinHostStorageRecoveryArgs(args)
}

func hostStorageRecoveryChangedTargets(plan catchrpc.HostStoragePlan) (bool, bool) {
	dataChanged := !hostStoragePathsEqual(plan.Current.DataDir, plan.Desired.DataDir) || plan.Current.DataDirZFS != plan.Desired.DataDirZFS
	servicesChanged := !hostStoragePathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot) || plan.Current.ServicesZFS != plan.Desired.ServicesZFS
	if !dataChanged && !servicesChanged {
		dataChanged = cleanHostStoragePath(plan.Desired.DataDir) != ""
		servicesChanged = !dataChanged && cleanHostStoragePath(plan.Desired.ServicesRoot) != ""
	}
	return dataChanged, servicesChanged
}

func appendHostStorageRecoveryTargetArgs(args []string, plan catchrpc.HostStoragePlan, dataChanged, servicesChanged bool) []string {
	if dataDir := cleanHostStoragePath(plan.Desired.DataDir); dataChanged && dataDir != "" {
		args = append(args, "--data-dir="+dataDir)
	}
	if servicesRoot := cleanHostStoragePath(plan.Desired.ServicesRoot); servicesChanged && servicesRoot != "" {
		args = append(args, "--services-root="+servicesRoot)
	}
	return args
}

func hostStorageRecoveryTargetsUseZFS(plan catchrpc.HostStoragePlan, dataChanged, servicesChanged bool) bool {
	return dataChanged && plan.Desired.DataDirZFS || servicesChanged && plan.Desired.ServicesZFS
}

func shellJoinHostStorageRecoveryArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "" && hostStorageShellSafePattern.MatchString(arg) {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'"'"'`)+"'")
	}
	return strings.Join(quoted, " ")
}

func findHostStorageStartupRecovery(ctx context.Context, root string) (*hostStorageTransaction, error) {
	paths, err := hostStorageTransactionJournalPaths(root)
	if err != nil {
		return nil, err
	}
	seenPaths := make(map[string]bool)
	for len(paths) > 0 {
		path := filepath.Clean(paths[0])
		paths = paths[1:]
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		tx, authoritativePath, loadErr := loadAuthoritativeHostStorageTransaction(path)
		if loadErr != nil {
			return nil, loadErr
		}
		if authoritativePath != "" {
			seenPaths[authoritativePath] = true
		}
		counterparts, counterpartErr := pendingHostStorageCounterpartJournals(tx, seenPaths)
		if counterpartErr != nil {
			return nil, counterpartErr
		}
		paths = append(paths, counterparts...)
		if resumeErr := resumeHostStorageTransaction(ctx, tx); resumeErr != nil {
			return tx, resumeErr
		}
	}
	return nil, nil
}

func loadAuthoritativeHostStorageTransaction(path string) (*hostStorageTransaction, string, error) {
	tx, err := loadHostStorageTransaction(path)
	if err != nil {
		return nil, "", fmt.Errorf("load host storage recovery journal %s: %w", path, err)
	}
	source := filepath.Clean(tx.SourceJournal)
	if path == source {
		return tx, "", nil
	}
	sourceTx, err := loadHostStorageTransaction(source)
	if err != nil {
		return nil, "", fmt.Errorf("load authoritative host storage recovery journal %s: %w", source, err)
	}
	if sourceTx.ID != tx.ID {
		return nil, "", fmt.Errorf("host storage recovery journal %s does not match authoritative transaction %s", path, source)
	}
	return sourceTx, source, nil
}

func pendingHostStorageCounterpartJournals(tx *hostStorageTransaction, seenPaths map[string]bool) ([]string, error) {
	var paths []string
	for _, counterpart := range []string{tx.SourceJournal, tx.TargetJournal} {
		counterpart = filepath.Clean(strings.TrimSpace(counterpart))
		if counterpart == "." || seenPaths[counterpart] {
			continue
		}
		exists, err := hostStorageTransactionJournalExists(counterpart)
		if err != nil {
			return nil, fmt.Errorf("inspect host storage counterpart journal %s: %w", counterpart, err)
		}
		if exists {
			paths = append(paths, counterpart)
		}
	}
	return paths, nil
}

func hostStorageTransactionJournalExists(path string) (bool, error) {
	tree, relative, err := openHostStorageTransactionTree(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = tree.Close() }()
	parent, name, closeParent, err := tree.openFileParent(relative, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closeParent()
	file, err := openHostStorageManagedFile(parent, name, path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, file.Close()
}

func hostStorageTransactionJournalPaths(root string) ([]string, error) {
	root = cleanHostStoragePath(root)
	if root == "" {
		return nil, nil
	}
	container, err := openHostStorageTransactionContainer(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = container.Close() }()
	return listHostStorageTransactionJournalPaths(container.root, root)
}

func openHostStorageTransactionContainer(root string) (*hostStorageTransactionContainer, error) {
	dataRoot, err := openHostStorageDataRoot(root, false)
	if err != nil {
		return nil, err
	}
	result := &hostStorageTransactionContainer{roots: []*os.Root{dataRoot}}
	fail := func(err error) (*hostStorageTransactionContainer, error) {
		_ = result.Close()
		return nil, err
	}
	migrations, _, err := openHostStorageManagedDirectory(dataRoot, "migrations", filepath.Join(root, "migrations"), false)
	if err != nil {
		return fail(err)
	}
	result.roots = append(result.roots, migrations)
	containerPath := filepath.Join(root, "migrations", "host-storage")
	containerRoot, _, err := openHostStorageManagedDirectory(migrations, "host-storage", containerPath, false)
	if err != nil {
		return fail(err)
	}
	result.roots = append(result.roots, containerRoot)
	result.root = containerRoot
	return result, nil
}

func listHostStorageTransactionJournalPaths(container *os.Root, dataRoot string) ([]string, error) {
	dir, err := container.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		path, err := inspectHostStorageTransactionJournalEntry(container, dataRoot, entry.Name())
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

func inspectHostStorageTransactionJournalEntry(container *os.Root, dataRoot, id string) (string, error) {
	containerPath := filepath.Join(dataRoot, "migrations", "host-storage")
	if !validHostStorageTransactionID(id) {
		return "", fmt.Errorf("unexpected host storage transaction path %s", filepath.Join(containerPath, id))
	}
	txRoot, _, err := openHostStorageManagedDirectory(container, id, filepath.Join(containerPath, id), false)
	if err != nil {
		return "", err
	}
	journal := hostStorageTransactionPath(dataRoot, id)
	file, err := openHostStorageManagedFile(txRoot, "transaction.json", journal)
	closeRootErr := txRoot.Close()
	if err != nil {
		return "", errors.Join(err, closeRootErr)
	}
	return journal, errors.Join(file.Close(), closeRootErr)
}

func applyManagedHostStorageTargetLayout(target string) error {
	target = cleanHostStoragePath(target)
	if target == "" || !filepath.IsAbs(target) {
		return fmt.Errorf("managed host storage target must be absolute, got %q", target)
	}
	if err := applyManagedHostStorageDirectoryLayout(target); err != nil {
		return err
	}
	if err := tightenManagedHostStorageFiles(target); err != nil {
		return err
	}
	return filepath.WalkDir(filepath.Join(target, "migrations"), tightenManagedHostStorageMigrationPath)
}

func applyManagedHostStorageDirectoryLayout(target string) error {
	dirs := map[string]os.FileMode{
		target:                            0o711,
		filepath.Join(target, "services"): 0o711,
	}
	for _, name := range []string{"backups", "checkpoints", "migrations", "mounts", "registry", "tsd", "tsnet", "vm-images"} {
		dirs[filepath.Join(target, name)] = 0o700
	}
	for dir, mode := range dirs {
		if err := os.MkdirAll(dir, mode); err != nil {
			return err
		}
		if err := os.Chmod(dir, mode); err != nil {
			return err
		}
		if err := hostStorageChownFn(dir, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

func tightenManagedHostStorageFiles(target string) error {
	for _, name := range []string{"catch.lock", "db.json", "id_ed25519", "install.json"} {
		path := filepath.Join(target, name)
		if err := tightenHostStorageManagedFile(path); err != nil {
			return err
		}
	}
	return nil
}

func tightenManagedHostStorageMigrationPath(path string, entry os.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed host storage migration path %s is a symlink", path)
	}
	mode := os.FileMode(0o600)
	if entry.IsDir() {
		mode = 0o700
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return hostStorageChownFn(path, 0, 0)
}

func tightenHostStorageManagedFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("managed host storage state path %s is not a regular file", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return hostStorageChownFn(path, 0, 0)
}

func (a *hostStorageApplier) rollbackHostStorageTransaction(ctx context.Context, tx *hostStorageTransaction, cause error, w io.Writer) error {
	if cause == nil {
		cause = errors.New("host storage apply failed")
	}
	if tx == nil {
		return errors.Join(cause, errors.New("host storage rollback requires a transaction journal"))
	}
	tx.LastError = cause.Error()
	rollbackErr := errors.Join(
		recordHostStorageRollbackCause(tx),
		a.cancelHostStorageTransactionCatchRestarts(ctx, tx),
		a.stopHostStorageReplacementServices(ctx, tx),
		a.restoreHostStorageTransactionState(ctx, tx),
		a.restartHostStoragePreviousServices(ctx, tx),
		a.validateHostStorageRollbackRunningSet(ctx, tx),
	)
	if rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf("host storage rollback from journal %s failed: %w", tx.SourceJournal, rollbackErr))
	}
	if err := a.restoreHostStorageTransactionAuthority(ctx, tx, w); err != nil {
		return errors.Join(cause, fmt.Errorf("host storage rollback from journal %s failed: %w", tx.SourceJournal, err))
	}
	if err := advanceHostStorageTransaction(tx, hostStoragePhaseRolledBack); err != nil {
		return errors.Join(cause, fmt.Errorf("mark host storage transaction rolled back: %w", err))
	}
	return fmt.Errorf("%w; rollback completed from journal %q", cause, tx.SourceJournal)
}

func (a *hostStorageApplier) cancelHostStorageTransactionCatchRestarts(ctx context.Context, tx *hostStorageTransaction) error {
	if tx.CatchAuthority != hostStorageCatchAuthorityTarget {
		return nil
	}
	if err := a.ops.cancelCatchRestarts(ctx); err != nil {
		return fmt.Errorf("cancel pending catch restarts: %w", err)
	}
	return nil
}

func recordHostStorageRollbackCause(tx *hostStorageTransaction) error {
	if err := persistHostStorageTransaction(tx); err != nil {
		return fmt.Errorf("record rollback cause: %w", err)
	}
	return nil
}

func (a *hostStorageApplier) stopHostStorageReplacementServices(ctx context.Context, tx *hostStorageTransaction) error {
	var stopErr error
	for _, name := range uniqueSortedStrings(append(slices.Clone(tx.RestartedServices), tx.PreviouslyRunning...)) {
		runner, err := a.ops.runnerForService(ctx, name)
		if err != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("runner for replacement service %q: %w", name, err))
			continue
		}
		if err := runner.Stop(); err != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("stop replacement service %q: %w", name, err))
		}
	}
	return stopErr
}

func (a *hostStorageApplier) restoreHostStorageTransactionState(ctx context.Context, tx *hostStorageTransaction) error {
	stateErr := errors.Join(restoreHostStorageTransactionUnits(tx), a.restoreHostStorageTransactionDatabase(tx))
	if len(tx.UnitBackups) > 0 {
		if err := a.ops.reloadSystemd(ctx); err != nil {
			stateErr = errors.Join(stateErr, fmt.Errorf("reload restored systemd units: %w", err))
		}
	}
	return stateErr
}

func (a *hostStorageApplier) restoreHostStorageTransactionAuthority(ctx context.Context, tx *hostStorageTransaction, w io.Writer) error {
	if tx.CatchAuthority == hostStorageCatchAuthorityTarget {
		if err := a.restoreHostStorageSourceCatch(ctx, tx, w); err != nil {
			return err
		}
		if err := advanceHostStorageTransactionState(tx, tx.Phase, hostStorageCatchAuthoritySource); err != nil {
			return fmt.Errorf("record restored source catch authority: %w", err)
		}
	}
	return nil
}

func (a *hostStorageApplier) restartHostStoragePreviousServices(ctx context.Context, tx *hostStorageTransaction) error {
	var restartErr error
	for _, name := range uniqueSortedStrings(tx.PreviouslyRunning) {
		runner, err := a.ops.runnerForService(ctx, name)
		if err != nil {
			restartErr = errors.Join(restartErr, fmt.Errorf("runner for old service %q: %w", name, err))
			continue
		}
		if err := runner.Start(); err != nil {
			restartErr = errors.Join(restartErr, fmt.Errorf("start old service %q: %w", name, err))
		}
	}
	return restartErr
}

func restoreHostStorageTransactionUnits(tx *hostStorageTransaction) error {
	var restoreErr error
	paths := make([]string, 0, len(tx.UnitBackups))
	for path := range tx.UnitBackups {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		backup := tx.UnitBackups[path]
		if backup == "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("remove replacement unit %s: %w", path, err))
			}
			continue
		}
		raw, err := readHostStorageTransactionFile(backup)
		if err == nil {
			err = writeHostStorageRestoredFile(path, raw, 0o644)
		}
		if err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore systemd unit %s: %w", path, err))
		}
	}
	return restoreErr
}

func (a *hostStorageApplier) restoreHostStorageTransactionDatabase(tx *hostStorageTransaction) error {
	raw, err := readHostStorageTransactionFile(tx.DatabaseBackup)
	if err != nil {
		return fmt.Errorf("read host storage database backup: %w", err)
	}
	var data db.Data
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("decode host storage database backup: %w", err)
	}
	databasePath := filepath.Join(cleanHostStoragePath(tx.Plan.Current.DataDir), "db.json")
	store := a.store
	if store == nil || !hostStoragePathsEqual(a.config.RootDir, tx.Plan.Current.DataDir) {
		store = db.NewStore(databasePath, tx.Plan.Current.ServicesRoot)
	}
	if err := store.Set(&data); err != nil {
		return fmt.Errorf("restore host storage database state: %w", err)
	}
	if err := writeHostStorageRestoredFile(databasePath, raw, 0o600); err != nil {
		return fmt.Errorf("restore exact host storage database bytes: %w", err)
	}
	a.store = store
	return nil
}

func (a *hostStorageApplier) restoreHostStorageSourceCatch(ctx context.Context, tx *hostStorageTransaction, w io.Writer) error {
	req, sourceConfig, err := a.hostStorageSourceCatchInstallRequest(tx)
	if err != nil {
		return err
	}
	if err := a.ops.reinstallCatchUnit(ctx, req, w); err != nil {
		return fmt.Errorf("reinstall source catch unit: %w", err)
	}
	if a.runningCatchMatches(tx.Plan.Current) {
		return nil
	}
	return a.restartAndVerifyHostStorageSourceCatch(ctx, tx, req, sourceConfig, w)
}

func (a *hostStorageApplier) hostStorageSourceCatchInstallRequest(tx *hostStorageTransaction) (hostStorageInstallRequest, Config, error) {
	sourceConfig := a.config
	sourceConfig.RootDir = cleanHostStoragePath(tx.Plan.Current.DataDir)
	sourceConfig.ServicesRoot = cleanHostStoragePath(tx.Plan.Current.ServicesRoot)
	sourceConfig.MountsRoot = filepath.Join(sourceConfig.RootDir, "mounts")
	sourceConfig.RegistryRoot = filepath.Join(sourceConfig.RootDir, "registry")
	sourceConfig.DB = db.NewStore(filepath.Join(sourceConfig.RootDir, "db.json"), sourceConfig.ServicesRoot)
	dv, err := sourceConfig.DB.Get()
	if err != nil {
		return hostStorageInstallRequest{}, Config{}, fmt.Errorf("load restored source database: %w", err)
	}
	catchService, err := hostStorageCatchSystemdServiceFromData(dv)
	if err != nil {
		return hostStorageInstallRequest{}, Config{}, err
	}
	catchRoot := cleanHostStoragePath(serviceRootFromConfig(sourceConfig, *catchService))
	req := hostStorageInstallRequest{
		DataDir:             sourceConfig.RootDir,
		DataDirZFS:          tx.Plan.Current.DataDirZFS,
		ServicesRoot:        sourceConfig.ServicesRoot,
		ServicesZFS:         tx.Plan.Current.ServicesZFS,
		Config:              sourceConfig,
		CatchServiceRoot:    catchRoot,
		CatchServiceRootZFS: catchService.ServiceRootZFS,
		PinCatchServiceRoot: !hostStoragePathsEqual(catchRoot, filepath.Join(sourceConfig.ServicesRoot, CatchService)),
	}
	return req, sourceConfig, nil
}

func (a *hostStorageApplier) restartAndVerifyHostStorageSourceCatch(
	ctx context.Context,
	tx *hostStorageTransaction,
	req hostStorageInstallRequest,
	sourceConfig Config,
	w io.Writer,
) error {
	if err := advanceHostStorageTransactionState(tx, hostStoragePhaseSourceRestartPending, hostStorageCatchAuthorityTarget); err != nil {
		return fmt.Errorf("record source catch restart handoff: %w", err)
	}
	if err := a.ops.restartCatch(ctx, req, w); err != nil {
		if errors.Is(err, errHostStorageCatchRestartScheduled) {
			return fmt.Errorf("source catch restart handoff scheduled: %w", errHostStorageSourceRestartHandoff)
		}
		return fmt.Errorf("restart source catch: %w", err)
	}
	info, err := a.ops.verifyCatchInfo(ctx, tx.Plan.Current, sourceConfig)
	if err != nil {
		return fmt.Errorf("validate source catch: %w", err)
	}
	if !hostStoragePathsEqual(info.RootDir, sourceConfig.RootDir) || !hostStoragePathsEqual(info.ServicesDir, sourceConfig.ServicesRoot) {
		return fmt.Errorf("validate source catch: reported data dir %q and services root %q", info.RootDir, info.ServicesDir)
	}
	a.runningCatchState = tx.Plan.Current
	a.runningCatchStateKnown = true
	return nil
}

func (a *hostStorageApplier) validateHostStorageRollbackRunningSet(ctx context.Context, tx *hostStorageTransaction) error {
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
		running, err := a.ops.isServiceRunning(ctx, name)
		if err != nil {
			return fmt.Errorf("validate restored service %q: %w", name, err)
		}
		if running != wantRunning[name] {
			return fmt.Errorf("validate restored service %q: running=%t, want %t", name, running, wantRunning[name])
		}
	}
	return nil
}
