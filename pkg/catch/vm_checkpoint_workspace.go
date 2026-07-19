// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	vmCheckpointRootMode = 0o755
	vmCheckpointTempMode = 0o700
)

var (
	vmSnapshotFileChown = func(file *os.File, uid, gid int) error {
		return file.Chown(uid, gid)
	}
	vmSnapshotFileChmod = func(file *os.File, mode os.FileMode) error {
		return file.Chmod(mode)
	}
)

type vmCheckpointEntryIdentity struct {
	dev uint64
	ino uint64
}

type vmCheckpointWorkspace struct {
	dataParent    *os.File
	parent        *os.File
	dir           *os.File
	baseDir       string
	rootIdentity  vmCheckpointEntryIdentity
	dirIdentity   vmCheckpointEntryIdentity
	metadataID    vmCheckpointEntryIdentity
	entryName     string
	metadataReady bool
	sealed        bool
	removed       bool
	closed        bool
}

func newVMCheckpointWorkspace(baseDir string, identity vmRuntimeIdentity) (*vmCheckpointWorkspace, error) {
	baseDir = filepath.Clean(strings.TrimSpace(baseDir))
	if !filepath.IsAbs(baseDir) {
		return nil, fmt.Errorf("VM checkpoint directory must be absolute: %s", baseDir)
	}
	if identity.UID <= 0 || identity.GID <= 0 {
		return nil, fmt.Errorf("VM checkpoint runtime identity must be non-root")
	}
	dataDir := filepath.Dir(baseDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create VM data directory %s: %w", dataDir, err)
	}
	dataParent, err := openVMCheckpointDirectoryPath(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open trusted VM data directory %s: %w", dataDir, err)
	}
	workspace := &vmCheckpointWorkspace{dataParent: dataParent, baseDir: baseDir}
	if err := workspace.openRoot(); err != nil {
		_ = workspace.close()
		return nil, err
	}
	if err := workspace.createTemporaryDirectory(identity); err != nil {
		cleanupErr := errors.Join(workspace.remove(), workspace.close())
		return nil, errors.Join(err, checkpointWorkspaceCleanupError(workspace.path(), cleanupErr))
	}
	return workspace, nil
}

func (w *vmCheckpointWorkspace) openRoot() error {
	name := filepath.Base(w.baseDir)
	before, err := ensureVMCheckpointRootEntry(w.dataParent, name, w.baseDir)
	if err != nil {
		return err
	}
	if err := w.openRootEntry(name, before); err != nil {
		return err
	}
	if err := normalizeVMCheckpointRoot(w.parent, w.baseDir); err != nil {
		return err
	}
	return w.verifyRoot()
}

func ensureVMCheckpointRootEntry(dataParent *os.File, name, path string) (unix.Stat_t, error) {
	if err := validateVMCheckpointEntryName(name); err != nil {
		return unix.Stat_t{}, err
	}
	if err := unix.Mkdirat(int(dataParent.Fd()), name, vmCheckpointRootMode); err != nil && !errors.Is(err, unix.EEXIST) {
		return unix.Stat_t{}, fmt.Errorf("create VM checkpoint root %s: %w", path, err)
	}
	before, err := statVMCheckpointEntry(dataParent, name, path)
	if err != nil {
		return unix.Stat_t{}, err
	}
	if uint32(before.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return unix.Stat_t{}, fmt.Errorf("VM checkpoint root %s must be a directory", path)
	}
	return before, nil
}

func (w *vmCheckpointWorkspace) openRootEntry(name string, before unix.Stat_t) error {
	root, err := openVMCheckpointDirectoryAt(int(w.dataParent.Fd()), name)
	if err != nil {
		return fmt.Errorf("open VM checkpoint root %s without following links: %w", w.baseDir, err)
	}
	w.parent = os.NewFile(uintptr(root), w.baseDir)
	if w.parent == nil {
		_ = unix.Close(root)
		return fmt.Errorf("open VM checkpoint root %s", w.baseDir)
	}
	opened, err := statVMCheckpointFile(w.parent, w.baseDir)
	if err != nil {
		return err
	}
	if !sameVMCheckpointEntry(before, opened) {
		return fmt.Errorf("VM checkpoint root %s changed while opening", w.baseDir)
	}
	w.rootIdentity = identityForVMCheckpointEntry(opened)
	return nil
}

func normalizeVMCheckpointRoot(root *os.File, path string) error {
	if err := vmSnapshotFileChown(root, 0, 0); err != nil {
		return fmt.Errorf("set VM checkpoint root owner %s: %w", path, err)
	}
	if err := vmSnapshotFileChmod(root, vmCheckpointRootMode); err != nil {
		return fmt.Errorf("set VM checkpoint root mode %s: %w", path, err)
	}
	return nil
}

func (w *vmCheckpointWorkspace) createTemporaryDirectory(identity vmRuntimeIdentity) error {
	for attempt := 0; attempt < 100; attempt++ {
		collision, err := w.tryCreateTemporaryDirectory(identity)
		if collision {
			continue
		}
		return err
	}
	return fmt.Errorf("create unique temporary VM checkpoint directory in %s", w.baseDir)
}

func (w *vmCheckpointWorkspace) tryCreateTemporaryDirectory(identity vmRuntimeIdentity) (bool, error) {
	name, err := randomVMCheckpointTempName()
	if err != nil {
		return false, err
	}
	if err := unix.Mkdirat(int(w.parent.Fd()), name, vmCheckpointTempMode); errors.Is(err, unix.EEXIST) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("create temporary VM checkpoint directory in %s: %w", w.baseDir, err)
	}
	w.entryName = name
	if err := w.openTemporaryDirectory(); err != nil {
		return false, err
	}
	if err := delegateVMCheckpointDirectory(w.dir, w.path(), identity); err != nil {
		return false, err
	}
	return false, w.verifyTemporaryEntry()
}

func (w *vmCheckpointWorkspace) openTemporaryDirectory() error {
	path := w.path()
	before, err := statVMCheckpointEntry(w.parent, w.entryName, path)
	if err != nil {
		return err
	}
	fd, err := openVMCheckpointDirectoryAt(int(w.parent.Fd()), w.entryName)
	if err != nil {
		return fmt.Errorf("open temporary VM checkpoint directory %s without following links: %w", path, err)
	}
	w.dir = os.NewFile(uintptr(fd), path)
	if w.dir == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open temporary VM checkpoint directory %s", path)
	}
	opened, err := statVMCheckpointFile(w.dir, path)
	if err != nil {
		return err
	}
	if !sameVMCheckpointEntry(before, opened) {
		return fmt.Errorf("temporary VM checkpoint directory %s changed while opening", path)
	}
	w.dirIdentity = identityForVMCheckpointEntry(opened)
	return nil
}

func delegateVMCheckpointDirectory(dir *os.File, path string, identity vmRuntimeIdentity) error {
	if err := vmSnapshotFileChown(dir, identity.UID, identity.GID); err != nil {
		return fmt.Errorf("delegate VM checkpoint directory %s: %w", path, err)
	}
	if err := vmSnapshotFileChmod(dir, vmCheckpointTempMode); err != nil {
		return fmt.Errorf("set delegated VM checkpoint directory mode %s: %w", path, err)
	}
	return nil
}

func randomVMCheckpointTempName() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate temporary VM checkpoint name: %w", err)
	}
	return ".firecracker-checkpoint-" + hex.EncodeToString(raw[:]), nil
}

func (w *vmCheckpointWorkspace) path() string {
	if w == nil || w.entryName == "" {
		return ""
	}
	return filepath.Join(w.baseDir, w.entryName)
}

func (w *vmCheckpointWorkspace) verifyRoot() error {
	if w == nil || w.dataParent == nil || w.parent == nil {
		return fmt.Errorf("VM checkpoint workspace root is not open")
	}
	entry, err := statVMCheckpointEntry(w.dataParent, filepath.Base(w.baseDir), w.baseDir)
	if err != nil {
		return err
	}
	opened, err := statVMCheckpointFile(w.parent, w.baseDir)
	if err != nil {
		return err
	}
	if identityForVMCheckpointEntry(entry) != w.rootIdentity || identityForVMCheckpointEntry(opened) != w.rootIdentity {
		return fmt.Errorf("VM checkpoint root %s changed during snapshot", w.baseDir)
	}
	return nil
}

func (w *vmCheckpointWorkspace) verifyTemporaryEntry() error {
	if w == nil || w.parent == nil || w.dir == nil || w.entryName == "" {
		return fmt.Errorf("temporary VM checkpoint workspace is not open")
	}
	path := w.path()
	entry, err := statVMCheckpointEntry(w.parent, w.entryName, path)
	if err != nil {
		return fmt.Errorf("temporary VM checkpoint directory %s changed during snapshot: %w", path, err)
	}
	opened, err := statVMCheckpointFile(w.dir, path)
	if err != nil {
		return err
	}
	if identityForVMCheckpointEntry(entry) != w.dirIdentity || identityForVMCheckpointEntry(opened) != w.dirIdentity {
		return fmt.Errorf("temporary VM checkpoint directory %s changed during snapshot", path)
	}
	return nil
}

func (w *vmCheckpointWorkspace) writeMetadata(raw []byte) error {
	if err := w.seal(); err != nil {
		return err
	}
	if err := w.verifyRoot(); err != nil {
		return err
	}
	if err := w.verifyTemporaryEntry(); err != nil {
		return err
	}
	const name = "metadata.json"
	flags := unix.O_WRONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_CREAT | unix.O_EXCL
	fd, err := unix.Openat(int(w.dir.Fd()), name, flags, 0o644)
	if err != nil {
		return fmt.Errorf("create VM checkpoint metadata %s without following links: %w", filepath.Join(w.path(), name), err)
	}
	metadata := os.NewFile(uintptr(fd), filepath.Join(w.path(), name))
	if metadata == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open VM checkpoint metadata %s", filepath.Join(w.path(), name))
	}
	metadataID, writeErr := writeAndSyncVMCheckpointMetadata(metadata, raw)
	closeErr := metadata.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	w.metadataID = metadataID
	w.metadataReady = true
	if err := w.dir.Sync(); err != nil {
		return fmt.Errorf("sync temporary VM checkpoint directory %s: %w", w.path(), err)
	}
	if err := w.verifyTemporaryEntry(); err != nil {
		return err
	}
	return w.verifyMetadata()
}

func writeAndSyncVMCheckpointMetadata(file *os.File, raw []byte) (vmCheckpointEntryIdentity, error) {
	if err := file.Chmod(0o644); err != nil {
		return vmCheckpointEntryIdentity{}, fmt.Errorf("set VM checkpoint metadata mode: %w", err)
	}
	if _, err := file.Write(raw); err != nil {
		return vmCheckpointEntryIdentity{}, fmt.Errorf("write VM checkpoint metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		return vmCheckpointEntryIdentity{}, fmt.Errorf("sync VM checkpoint metadata: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return vmCheckpointEntryIdentity{}, fmt.Errorf("inspect VM checkpoint metadata: %w", err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return vmCheckpointEntryIdentity{}, fmt.Errorf("VM checkpoint metadata must be a regular file")
	}
	return identityForVMCheckpointEntry(stat), nil
}

func (w *vmCheckpointWorkspace) publish(finalName string) error {
	if err := validateVMCheckpointEntryName(finalName); err != nil {
		return fmt.Errorf("publish VM checkpoint: %w", err)
	}
	if err := w.verifyRoot(); err != nil {
		return err
	}
	if err := w.verifyTemporaryEntry(); err != nil {
		return err
	}
	if err := w.verifyMetadata(); err != nil {
		return err
	}
	tempName := w.entryName
	if err := renameVMCheckpointNoReplace(int(w.parent.Fd()), tempName, finalName); err != nil {
		return fmt.Errorf("publish VM checkpoint directory %s as %s: %w", w.path(), filepath.Join(w.baseDir, finalName), err)
	}
	w.entryName = finalName
	if err := w.verifyTemporaryEntry(); err != nil {
		return fmt.Errorf("verify published VM checkpoint directory: %w", err)
	}
	if err := w.parent.Sync(); err != nil {
		return fmt.Errorf("sync published VM checkpoint root %s: %w", w.baseDir, err)
	}
	return nil
}

func (w *vmCheckpointWorkspace) seal() error {
	if w == nil || w.dir == nil {
		return fmt.Errorf("temporary VM checkpoint workspace is not open")
	}
	if w.sealed {
		return w.verifyTemporaryEntry()
	}
	if err := vmSnapshotFileChown(w.dir, 0, 0); err != nil {
		return fmt.Errorf("seal VM checkpoint directory owner %s: %w", w.path(), err)
	}
	if err := vmSnapshotFileChmod(w.dir, vmCheckpointRootMode); err != nil {
		return fmt.Errorf("seal VM checkpoint directory mode %s: %w", w.path(), err)
	}
	w.sealed = true
	return w.verifyTemporaryEntry()
}

func (w *vmCheckpointWorkspace) verifyMetadata() error {
	if !w.metadataReady {
		return fmt.Errorf("VM checkpoint metadata is not ready for publication")
	}
	path := filepath.Join(w.path(), "metadata.json")
	stat, err := statVMCheckpointEntry(w.dir, "metadata.json", path)
	if err != nil {
		return fmt.Errorf("VM checkpoint metadata changed before publication: %w", err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || identityForVMCheckpointEntry(stat) != w.metadataID {
		return fmt.Errorf("VM checkpoint metadata %s changed before publication", path)
	}
	return nil
}

func (w *vmCheckpointWorkspace) remove() error {
	if w == nil || w.removed {
		return nil
	}
	if w.dir == nil {
		return w.removeUnopened()
	}
	contentsErr := removeVMCheckpointDirectoryContents(w.dir, w.path())
	entryErr := w.removeOpenedEntry()
	return errors.Join(contentsErr, entryErr)
}

func (w *vmCheckpointWorkspace) removeUnopened() error {
	if w.parent == nil || w.entryName == "" {
		return nil
	}
	return w.unlinkCurrentEntry("unopened ")
}

func (w *vmCheckpointWorkspace) removeOpenedEntry() error {
	if err := w.verifyTemporaryEntry(); err != nil {
		return err
	}
	return w.unlinkCurrentEntry("")
}

func (w *vmCheckpointWorkspace) unlinkCurrentEntry(description string) error {
	if err := unix.Unlinkat(int(w.parent.Fd()), w.entryName, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove %sVM checkpoint directory %s: %w", description, w.path(), err)
	}
	w.removed = true
	if err := w.parent.Sync(); err != nil {
		return fmt.Errorf("sync VM checkpoint root %s after cleanup: %w", w.baseDir, err)
	}
	return nil
}

func removeVMCheckpointDirectoryContents(dir *os.File, path string) error {
	names, err := readVMCheckpointDirectoryNames(dir, path)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		errs = append(errs, removeVMCheckpointDirectoryEntry(dir, path, name))
	}
	return errors.Join(errs...)
}

func readVMCheckpointDirectoryNames(dir *os.File, path string) ([]string, error) {
	readerFD, err := openVMCheckpointDirectoryAt(int(dir.Fd()), ".")
	if err != nil {
		return nil, fmt.Errorf("open VM checkpoint directory %s for cleanup: %w", path, err)
	}
	reader := os.NewFile(uintptr(readerFD), path)
	if reader == nil {
		_ = unix.Close(readerFD)
		return nil, fmt.Errorf("open VM checkpoint directory %s for cleanup", path)
	}
	names, readErr := reader.Readdirnames(-1)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	sort.Strings(names)
	return names, nil
}

func removeVMCheckpointDirectoryEntry(dir *os.File, path, name string) error {
	if err := validateVMCheckpointEntryName(name); err != nil {
		return err
	}
	err := unix.Unlinkat(int(dir.Fd()), name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if !errors.Is(err, unix.EISDIR) && !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("remove VM checkpoint entry %s: %w", filepath.Join(path, name), err)
	}
	return removeVMCheckpointChildDirectory(dir, path, name)
}

func removeVMCheckpointChildDirectory(dir *os.File, path, name string) error {
	childPath := filepath.Join(path, name)
	child, err := openVMCheckpointChildDirectory(dir, childPath, name)
	if err != nil {
		return err
	}
	contentsErr := removeVMCheckpointDirectoryContents(child, childPath)
	closeErr := child.Close()
	if err := errors.Join(contentsErr, closeErr); err != nil {
		return err
	}
	return unlinkVMCheckpointChildDirectory(dir, childPath, name)
}

func openVMCheckpointChildDirectory(dir *os.File, childPath, name string) (*os.File, error) {
	childFD, err := openVMCheckpointDirectoryAt(int(dir.Fd()), name)
	if err != nil {
		return nil, fmt.Errorf("open VM checkpoint directory %s for cleanup without following links: %w", childPath, err)
	}
	child := os.NewFile(uintptr(childFD), childPath)
	if child == nil {
		_ = unix.Close(childFD)
		return nil, fmt.Errorf("open VM checkpoint directory %s for cleanup", childPath)
	}
	return child, nil
}

func unlinkVMCheckpointChildDirectory(dir *os.File, childPath, name string) error {
	if err := unix.Unlinkat(int(dir.Fd()), name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove VM checkpoint directory %s: %w", childPath, err)
	}
	return nil
}

func (w *vmCheckpointWorkspace) close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	var errs []error
	for _, file := range []*os.File{w.dir, w.parent, w.dataParent} {
		if file != nil {
			errs = append(errs, file.Close())
		}
	}
	return errors.Join(errs...)
}

func validateVMCheckpointEntryName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, filepath.Separator) {
		return fmt.Errorf("invalid VM checkpoint entry name %q", name)
	}
	return nil
}

func statVMCheckpointEntry(parent *os.File, name, path string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, fmt.Errorf("inspect VM checkpoint entry %s without following links: %w", path, err)
	}
	if uint32(stat.Mode)&unix.S_IFMT == unix.S_IFLNK {
		return unix.Stat_t{}, fmt.Errorf("refusing symbolic link VM checkpoint entry: %s", path)
	}
	return stat, nil
}

func statVMCheckpointFile(file *os.File, path string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return unix.Stat_t{}, fmt.Errorf("inspect opened VM checkpoint entry %s: %w", path, err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return unix.Stat_t{}, fmt.Errorf("VM checkpoint entry %s must be a directory", path)
	}
	return stat, nil
}

func identityForVMCheckpointEntry(stat unix.Stat_t) vmCheckpointEntryIdentity {
	return vmCheckpointEntryIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}
}

func sameVMCheckpointEntry(left, right unix.Stat_t) bool {
	return identityForVMCheckpointEntry(left) == identityForVMCheckpointEntry(right) &&
		uint32(left.Mode)&unix.S_IFMT == uint32(right.Mode)&unix.S_IFMT
}
