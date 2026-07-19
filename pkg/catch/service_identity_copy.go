// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/copyutil"
	"golang.org/x/sys/unix"
)

type serviceIdentityCopyGuard struct {
	ctx        context.Context
	root       string
	rootDev    uint64
	rootIno    uint64
	mounts     []string
	datasets   map[string]string
	runner     zfsCommandRunner
	beforeOpen func(string)
	afterCopy  func(string)
}

func newServiceIdentityCopyGuard(ctx context.Context, root, dataset string, runner zfsCommandRunner) (serviceIdentityCopyGuard, error) {
	root, err := validateServiceIdentityInspectionRoot(root)
	if err != nil {
		return serviceIdentityCopyGuard{}, err
	}
	mounts, err := serviceIdentityMountPointsFn()
	if err != nil {
		return serviceIdentityCopyGuard{}, fmt.Errorf("discover source mount boundaries: %w", err)
	}
	var boundaries []serviceIdentityDatasetBoundary
	if strings.TrimSpace(dataset) != "" {
		boundaries, err = serviceIdentityDatasetBoundariesFn(ctx, runner, root)
		if err != nil {
			return serviceIdentityCopyGuard{}, fmt.Errorf("discover source ZFS boundaries: %w", err)
		}
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return serviceIdentityCopyGuard{}, fmt.Errorf("open source root without following symlinks: %w", err)
	}
	defer func() { _ = unix.Close(rootFD) }()
	var stat unix.Stat_t
	if err := unix.Fstat(rootFD, &stat); err != nil {
		return serviceIdentityCopyGuard{}, err
	}
	guard := serviceIdentityCopyGuard{
		ctx: ctx, root: root, rootDev: uint64(stat.Dev), rootIno: uint64(stat.Ino), runner: runner,
		mounts: serviceIdentityNestedPaths(root, mounts), datasets: serviceIdentityDatasetMap(root, boundaries),
	}
	if len(guard.mounts) != 0 {
		return serviceIdentityCopyGuard{}, fmt.Errorf("mount boundary at %s blocks service identity source copy", guard.mounts[0])
	}
	if len(guard.datasets) != 0 {
		paths := make([]string, 0, len(guard.datasets))
		for path := range guard.datasets {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		return serviceIdentityCopyGuard{}, fmt.Errorf("nested ZFS dataset %q at %s blocks service identity source copy", guard.datasets[paths[0]], paths[0])
	}
	return guard, nil
}

func (g serviceIdentityCopyGuard) copyToStage(stage string) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := g.writeTar(pw)
		if err != nil {
			_ = pw.CloseWithError(err)
			errCh <- err
			return
		}
		errCh <- pw.Close()
	}()
	extractErr := copyutil.ExtractTarWithOptions(pr, stage, copyutil.ExtractOptions{})
	if extractErr != nil {
		_ = pr.CloseWithError(extractErr)
	}
	archiveErr := <-errCh
	if archiveErr != nil {
		return fmt.Errorf("archive guarded service root: %w", archiveErr)
	}
	if extractErr != nil {
		return fmt.Errorf("extract guarded service root: %w", extractErr)
	}
	return nil
}

func (g serviceIdentityCopyGuard) copyIntoMountedRoot(root string) error {
	retrySafeSkeleton, err := mountedRootIsEmptyOrRetrySafeSkeleton(root)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".yeet-service-root-")
	if err != nil {
		return fmt.Errorf("create migration stage: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := g.copyToStage(stage); err != nil {
		return err
	}
	if err := ensureDirsForRoot(stage, ""); err != nil {
		return err
	}
	if retrySafeSkeleton {
		if err := removeMountedRootServiceLayout(root); err != nil {
			return err
		}
	}
	if err := copyutil.MoveTree(stage, root); err != nil {
		return fmt.Errorf("move staged service root contents into place: %w", err)
	}
	removeStage = false
	return nil
}

func (g serviceIdentityCopyGuard) writeTar(w io.Writer) (err error) {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if err := validateServiceIdentityCurrentBoundaries(g.ctx, g.root, "source", g.runner); err != nil {
		return fmt.Errorf("source boundaries changed before guarded copy: %w", err)
	}
	rootFD, rootStat, err := g.openGuardedCopyRoot()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rootFD) }()
	tw := tar.NewWriter(w)
	defer func() { err = errors.Join(err, tw.Close()) }()
	if err := g.writeGuardedCopyRoot(tw, rootFD, rootStat); err != nil {
		return err
	}
	if err := validateServiceIdentityCurrentBoundaries(g.ctx, g.root, "source", g.runner); err != nil {
		return fmt.Errorf("source boundaries changed after guarded copy: %w", err)
	}
	return nil
}

func (g serviceIdentityCopyGuard) openGuardedCopyRoot() (int, unix.Stat_t, error) {
	rootFD, err := unix.Open(g.root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, unix.Stat_t{}, fmt.Errorf("open source root without following symlinks: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(rootFD, &stat); err != nil {
		_ = unix.Close(rootFD)
		return -1, unix.Stat_t{}, err
	}
	if uint64(stat.Dev) != g.rootDev || uint64(stat.Ino) != g.rootIno {
		_ = unix.Close(rootFD)
		return -1, unix.Stat_t{}, fmt.Errorf("source root changed from its guarded inode")
	}
	if err := rejectServiceIdentityCopyFDXattrs(rootFD, g.root); err != nil {
		_ = unix.Close(rootFD)
		return -1, unix.Stat_t{}, err
	}
	return rootFD, stat, nil
}

func (g serviceIdentityCopyGuard) writeGuardedCopyRoot(tw *tar.Writer, rootFD int, rootStat unix.Stat_t) error {
	rootInfo, err := serviceIdentityCopyFDInfo(rootFD, filepath.Base(g.root))
	if err != nil {
		return err
	}
	rootHeader, err := tar.FileInfoHeader(rootInfo, "")
	if err != nil {
		return err
	}
	rootHeader.Name = "."
	if err := tw.WriteHeader(rootHeader); err != nil {
		return err
	}
	if err := g.writeDirectory(tw, rootFD, ""); err != nil {
		return err
	}
	var finalRoot unix.Stat_t
	if err := unix.Fstat(rootFD, &finalRoot); err != nil {
		return err
	}
	if !serviceIdentityCopyStatEqual(rootStat, finalRoot) {
		return fmt.Errorf("source root changed during guarded copy")
	}
	return nil
}

func (g serviceIdentityCopyGuard) writeDirectory(tw *tar.Writer, dirFD int, relDir string) error {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	entries, err := serviceIdentityCopyReadDir(dirFD, relDir)
	if err != nil {
		return err
	}
	initial := make(map[string]unix.Stat_t, len(entries))
	for _, entry := range entries {
		before, err := g.writeGuardedDirectoryChild(tw, dirFD, relDir, entry.Name())
		if err != nil {
			return err
		}
		initial[entry.Name()] = before
	}
	return g.validateGuardedDirectoryUnchanged(dirFD, relDir, entries, initial)
}

func (g serviceIdentityCopyGuard) writeGuardedDirectoryChild(tw *tar.Writer, dirFD int, relDir, name string) (unix.Stat_t, error) {
	if err := validateServiceIdentityCurrentBoundaries(g.ctx, g.root, "source", g.runner); err != nil {
		return unix.Stat_t{}, fmt.Errorf("source boundaries changed during guarded copy: %w", err)
	}
	if err := validateServiceIdentityCopyEntryName(name); err != nil {
		return unix.Stat_t{}, err
	}
	var before unix.Stat_t
	pathRel := filepath.Join(relDir, name)
	path := filepath.Join(g.root, pathRel)
	if err := unix.Fstatat(dirFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, fmt.Errorf("inspect guarded source entry %s: %w", path, err)
	}
	if err := g.validateGuardedSourceEntry(path, before); err != nil {
		return unix.Stat_t{}, err
	}
	if g.beforeOpen != nil {
		g.beforeOpen(path)
	}
	if err := g.writeGuardedSourceEntry(tw, dirFD, name, pathRel, path, before); err != nil {
		return unix.Stat_t{}, err
	}
	if g.afterCopy != nil {
		g.afterCopy(path)
	}
	return before, nil
}

func validateServiceIdentityCopyEntryName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
		return fmt.Errorf("invalid guarded source entry %q", name)
	}
	return nil
}

func (g serviceIdentityCopyGuard) validateGuardedSourceEntry(path string, before unix.Stat_t) error {
	if err := g.validateBoundary(path, before); err != nil {
		return err
	}
	if before.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return fmt.Errorf("special permission bits on %s block service identity source copy", path)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFDIR && before.Nlink != 1 {
		return fmt.Errorf("hard-linked source entry %s blocks service identity source copy", path)
	}
	return nil
}

func (g serviceIdentityCopyGuard) writeGuardedSourceEntry(tw *tar.Writer, dirFD int, name, rel, path string, before unix.Stat_t) error {
	switch before.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return g.writeDirectoryEntry(tw, dirFD, name, rel, before)
	case unix.S_IFREG:
		return g.writeRegularEntry(tw, dirFD, name, rel, before)
	case unix.S_IFLNK:
		return g.writeSymlinkEntry(tw, dirFD, name, rel, before)
	default:
		return fmt.Errorf("special filesystem object %s blocks service identity source copy", path)
	}
}

func (g serviceIdentityCopyGuard) validateGuardedDirectoryUnchanged(dirFD int, relDir string, entries []os.DirEntry, initial map[string]unix.Stat_t) error {
	finalEntries, err := serviceIdentityCopyReadDir(dirFD, relDir)
	if err != nil {
		return err
	}
	if len(finalEntries) != len(entries) {
		return fmt.Errorf("guarded source directory %s changed during copy", filepath.Join(g.root, relDir))
	}
	for _, entry := range finalEntries {
		before, ok := initial[entry.Name()]
		if !ok {
			return fmt.Errorf("guarded source directory %s gained entry %s during copy", filepath.Join(g.root, relDir), entry.Name())
		}
		var after unix.Stat_t
		if err := unix.Fstatat(dirFD, entry.Name(), &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if !serviceIdentityCopyStatEqual(before, after) {
			return fmt.Errorf("guarded source entry %s changed during copy", filepath.Join(g.root, relDir, entry.Name()))
		}
	}
	return nil
}

func (g serviceIdentityCopyGuard) writeDirectoryEntry(tw *tar.Writer, parentFD int, name, rel string, expected unix.Stat_t) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open guarded source directory %s: %w", filepath.Join(g.root, rel), err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := serviceIdentityCopyValidateFD(fd, expected); err != nil {
		return fmt.Errorf("validate guarded source directory %s: %w", filepath.Join(g.root, rel), err)
	}
	if err := rejectServiceIdentityCopyFDXattrs(fd, filepath.Join(g.root, rel)); err != nil {
		return err
	}
	info, err := serviceIdentityCopyFDInfo(fd, name)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(rel) + "/"
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if err := g.writeDirectory(tw, fd, rel); err != nil {
		return err
	}
	return serviceIdentityCopyValidateFD(fd, expected)
}

func (g serviceIdentityCopyGuard) writeRegularEntry(tw *tar.Writer, parentFD int, name, rel string, expected unix.Stat_t) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open guarded source file %s: %w", filepath.Join(g.root, rel), err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(g.root, rel))
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("wrap guarded source file %s", filepath.Join(g.root, rel))
	}
	defer func() { _ = file.Close() }()
	if err := serviceIdentityCopyValidateFD(fd, expected); err != nil {
		return fmt.Errorf("validate guarded source file %s: %w", filepath.Join(g.root, rel), err)
	}
	if err := rejectServiceIdentityCopyFDXattrs(fd, filepath.Join(g.root, rel)); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(rel)
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.Copy(tw, file); err != nil {
		return err
	}
	return serviceIdentityCopyValidateFD(fd, expected)
}

func (g serviceIdentityCopyGuard) writeSymlinkEntry(tw *tar.Writer, parentFD int, name, rel string, expected unix.Stat_t) error {
	path := filepath.Join(g.root, rel)
	if err := rejectServiceIdentityCopyPathXattrs(path); err != nil {
		return err
	}
	target, err := serviceIdentityCopyReadlink(parentFD, name)
	if err != nil {
		return fmt.Errorf("read guarded source symlink %s: %w", filepath.Join(g.root, rel), err)
	}
	var after unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !serviceIdentityCopyStatEqual(expected, after) {
		return fmt.Errorf("guarded source symlink %s changed before copy", filepath.Join(g.root, rel))
	}
	info := serviceIdentityCopyFileInfo{name: name, stat: expected}
	header, err := tar.FileInfoHeader(info, target)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(rel)
	header.Uid = int(expected.Uid)
	header.Gid = int(expected.Gid)
	return tw.WriteHeader(header)
}

func (g serviceIdentityCopyGuard) validateBoundary(path string, stat unix.Stat_t) error {
	if dataset, ok := g.datasets[path]; ok {
		return fmt.Errorf("nested ZFS dataset %q at %s blocks service identity source copy", dataset, path)
	}
	if slices.Contains(g.mounts, path) {
		return fmt.Errorf("mount boundary at %s blocks service identity source copy", path)
	}
	if uint64(stat.Dev) != g.rootDev {
		return fmt.Errorf("device boundary at %s blocks service identity source copy", path)
	}
	return nil
}

func serviceIdentityCopyReadDir(fd int, rel string) ([]os.DirEntry, error) {
	if _, err := unix.Seek(fd, 0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind guarded source directory %s: %w", rel, err)
	}
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(dup), rel)
	if file == nil {
		_ = unix.Close(dup)
		return nil, fmt.Errorf("wrap guarded source directory %s", rel)
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func serviceIdentityCopyFDInfo(fd int, name string) (os.FileInfo, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(dup), name)
	if file == nil {
		_ = unix.Close(dup)
		return nil, fmt.Errorf("wrap guarded source descriptor %s", name)
	}
	defer func() { _ = file.Close() }()
	return file.Stat()
}

func serviceIdentityCopyValidateFD(fd int, expected unix.Stat_t) error {
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil {
		return err
	}
	if !serviceIdentityCopyStatEqual(expected, actual) {
		return fmt.Errorf("inode changed from guarded provenance")
	}
	return nil
}

func serviceIdentityCopyStatEqual(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Nlink == b.Nlink && a.Size == b.Size &&
		a.Uid == b.Uid && a.Gid == b.Gid &&
		serviceIdentityCopyStatTime(a, "Mtim", "Mtimespec") == serviceIdentityCopyStatTime(b, "Mtim", "Mtimespec") &&
		serviceIdentityCopyStatTime(a, "Ctim", "Ctimespec") == serviceIdentityCopyStatTime(b, "Ctim", "Ctimespec")
}

func serviceIdentityCopyStatTime(stat unix.Stat_t, names ...string) [2]int64 {
	value := reflect.ValueOf(stat)
	for _, name := range names {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		sec, nsec := field.FieldByName("Sec"), field.FieldByName("Nsec")
		if sec.IsValid() && nsec.IsValid() {
			return [2]int64{sec.Int(), nsec.Int()}
		}
	}
	return [2]int64{}
}

func rejectServiceIdentityCopyFDXattrs(fd int, path string) error {
	dup, err := unix.Dup(fd)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(dup), path)
	if file == nil {
		_ = unix.Close(dup)
		return fmt.Errorf("wrap guarded source descriptor %s", path)
	}
	defer func() { _ = file.Close() }()
	xattrs, err := listServiceIdentityOpenFileXattrs(file)
	if err != nil {
		return fmt.Errorf("inspect guarded source xattrs %s: %w", path, err)
	}
	if xattrs = unsupportedServiceIdentityTransactionXattrs(xattrs); len(xattrs) != 0 {
		return fmt.Errorf("extended attributes on %s block exact service identity source copy: %s", path, strings.Join(xattrs, ", "))
	}
	return nil
}

func rejectServiceIdentityCopyPathXattrs(path string) error {
	xattrs, err := listServiceIdentityXattrs(path)
	if err != nil {
		return fmt.Errorf("inspect guarded source xattrs %s: %w", path, err)
	}
	if xattrs = unsupportedServiceIdentityTransactionXattrs(xattrs); len(xattrs) != 0 {
		return fmt.Errorf("extended attributes on %s block exact service identity source copy: %s", path, strings.Join(xattrs, ", "))
	}
	return nil
}

func serviceIdentityCopyReadlink(parentFD int, name string) (string, error) {
	for size := 256; size <= 1<<20; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(parentFD, name, buf)
		if err != nil {
			return "", err
		}
		if n < len(buf) {
			return string(buf[:n]), nil
		}
	}
	return "", fmt.Errorf("symlink target is too long")
}

type serviceIdentityCopyFileInfo struct {
	name string
	stat unix.Stat_t
}

func (i serviceIdentityCopyFileInfo) Name() string { return i.name }
func (i serviceIdentityCopyFileInfo) Size() int64  { return i.stat.Size }
func (i serviceIdentityCopyFileInfo) Mode() os.FileMode {
	return serviceIdentityCopyFileMode(uint32(i.stat.Mode))
}
func (i serviceIdentityCopyFileInfo) ModTime() time.Time {
	stamp := serviceIdentityCopyStatTime(i.stat, "Mtim", "Mtimespec")
	return time.Unix(stamp[0], stamp[1])
}
func (i serviceIdentityCopyFileInfo) IsDir() bool { return i.stat.Mode&unix.S_IFMT == unix.S_IFDIR }
func (i serviceIdentityCopyFileInfo) Sys() any    { return &i.stat }

func serviceIdentityCopyFileMode(mode uint32) os.FileMode {
	result := os.FileMode(mode & 0o777)
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		result |= os.ModeDir
	case unix.S_IFLNK:
		result |= os.ModeSymlink
	}
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
