// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/copyutil"
	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

type nativeCopyDescriptors struct {
	root     int
	data     int
	rootPath string
	identity db.ServiceIdentity
}

func openNativeCopyDescriptors(root string, identity db.ServiceIdentity) (nativeCopyDescriptors, error) {
	if err := validateHostControlledServiceRootPath(root); err != nil {
		return nativeCopyDescriptors{}, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nativeCopyDescriptors{}, fmt.Errorf("open native copy service root: %w", err)
	}
	dataFD, err := unix.Openat(rootFD, "data", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Close(rootFD)
		return nativeCopyDescriptors{}, fmt.Errorf("open native copy data directory: %w", err)
	}
	var dataStat unix.Stat_t
	if err := unix.Fstat(dataFD, &dataStat); err != nil {
		_ = unix.Close(dataFD)
		_ = unix.Close(rootFD)
		return nativeCopyDescriptors{}, err
	}
	if dataStat.Uid != identity.UID || dataStat.Gid != identity.GID {
		_ = unix.Close(dataFD)
		_ = unix.Close(rootFD)
		return nativeCopyDescriptors{}, fmt.Errorf("native copy data owner is %d:%d, want %d:%d", dataStat.Uid, dataStat.Gid, identity.UID, identity.GID)
	}
	return nativeCopyDescriptors{root: rootFD, data: dataFD, rootPath: filepath.Clean(root), identity: identity}, nil
}

func (d nativeCopyDescriptors) close() {
	_ = unix.Close(d.data)
	_ = unix.Close(d.root)
}

func (e *ttyExecer) copyToNativeRemote(parsed copyExecArgs, serviceRoot, destination string, identity db.ServiceIdentity) error {
	descriptors, err := openNativeCopyDescriptors(serviceRoot, identity)
	if err != nil {
		return err
	}
	defer descriptors.close()
	if parsed.Archive {
		return e.copyArchiveToNativeRemote(parsed.Compress, destination, descriptors)
	}
	if strings.HasSuffix(parsed.To, "/") {
		return fmt.Errorf("copy destination must include a file name")
	}
	return e.copyFileToNativeRemote(parsed.Compress, destination, descriptors)
}

func (e *ttyExecer) copyFileToNativeRemote(compressed bool, destination string, descriptors nativeCopyDescriptors) (err error) {
	parentRel, name := filepath.Split(destination)
	parentFD, closeParent, err := openNativeCopyDirectory(descriptors.data, strings.TrimSuffix(parentRel, string(filepath.Separator)), descriptors.identity)
	if err != nil {
		return err
	}
	defer closeParent()
	id, err := newServiceIdentityMigrationID()
	if err != nil {
		return err
	}
	tmp, tmpName, err := createNativeCopyTemporaryFile(parentFD, name, id)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		_ = tmp.Close()
		if !published {
			_ = unix.Unlinkat(parentFD, tmpName, 0)
		}
	}()

	if err := e.writeNativeCopyTemporaryFile(tmp, compressed, descriptors.identity); err != nil {
		return err
	}
	if err := e.publishNativeCopyTemporaryFile(parentFD, tmp, tmpName, name); err != nil {
		return err
	}
	published = true
	return nil
}

func createNativeCopyTemporaryFile(parentFD int, name, id string) (*os.File, string, error) {
	tmpName := "." + name + ".yeet-copy-" + id
	fd, err := unix.Openat(parentFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create native copy temporary file: %w", err)
	}
	tmp := os.NewFile(uintptr(fd), tmpName)
	if tmp == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(parentFD, tmpName, 0)
		return nil, "", fmt.Errorf("wrap native copy temporary file")
	}
	return tmp, tmpName, nil
}

func (e *ttyExecer) writeNativeCopyTemporaryFile(tmp *os.File, compressed bool, identity db.ServiceIdentity) (err error) {
	input, closer, err := copyPayloadReader(e.rw, compressed)
	if err != nil {
		return fmt.Errorf("failed to read compressed payload: %w", err)
	}
	defer closeWithError(closer, &err, "failed to close compressed payload")
	if _, err = io.Copy(tmp, input); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}
	if err = tmp.Chown(int(identity.UID), int(identity.GID)); err != nil {
		return fmt.Errorf("set native copied file owner: %w", err)
	}
	if err = tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("set native copied file mode: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync native copied file: %w", err)
	}
	return nil
}

func (e *ttyExecer) publishNativeCopyTemporaryFile(parentFD int, tmp *os.File, tmpName, name string) error {
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close native copied file: %w", err)
	}
	if e.nativeCopyHook != nil {
		e.nativeCopyHook("before-file-publish")
	}
	if err := unix.Renameat(parentFD, tmpName, parentFD, name); err != nil {
		return fmt.Errorf("publish native copied file: %w", err)
	}
	if err := unix.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync native copy destination: %w", err)
	}
	return nil
}

func (e *ttyExecer) copyArchiveToNativeRemote(compressed bool, destination string, descriptors nativeCopyDescriptors) (err error) {
	id, err := newServiceIdentityMigrationID()
	if err != nil {
		return err
	}
	stageName := ".yeet-copy-" + id
	if err := unix.Mkdirat(descriptors.root, stageName, 0o700); err != nil {
		return fmt.Errorf("create native archive stage: %w", err)
	}
	stageFD, err := unix.Openat(descriptors.root, stageName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Unlinkat(descriptors.root, stageName, unix.AT_REMOVEDIR)
		return err
	}
	defer func() {
		_ = unix.Close(stageFD)
		_ = removeNativeCopyTreeAt(descriptors.root, stageName)
	}()
	stagePath := filepath.Join(descriptors.rootPath, stageName)
	input, closer, err := copyPayloadReader(e.rw, compressed)
	if err != nil {
		return fmt.Errorf("failed to read compressed payload: %w", err)
	}
	defer closeWithError(closer, &err, "failed to close compressed payload")
	if err = copyutil.ExtractTarWithOptions(input, stagePath, copyutil.ExtractOptions{
		ValidateEntry: validateServiceCopyArchiveEntry,
	}); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}
	if err = applyCopyIdentityContents(stagePath, descriptors.identity); err != nil {
		return fmt.Errorf("failed to set copied archive ownership: %w", err)
	}
	destinationFD, closeDestination, err := openNativeCopyDirectory(descriptors.data, destination, descriptors.identity)
	if err != nil {
		return err
	}
	defer closeDestination()
	if e.nativeCopyHook != nil {
		e.nativeCopyHook("before-archive-publish")
	}
	if err := mergeNativeCopyTree(stageFD, destinationFD); err != nil {
		return fmt.Errorf("publish native copied archive: %w", err)
	}
	return unix.Fsync(destinationFD)
}

func openNativeCopyDirectory(dataFD int, rel string, identity db.ServiceIdentity) (int, func(), error) {
	current, err := unix.Dup(dataFD)
	if err != nil {
		return -1, func() {}, err
	}
	closeCurrent := func() { _ = unix.Close(current) }
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return current, closeCurrent, nil
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				closeCurrent()
				return -1, func() {}, mkdirErr
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			closeCurrent()
			return -1, func() {}, fmt.Errorf("open native copy directory %q without following links: %w", component, openErr)
		}
		if err := unix.Fchown(next, int(identity.UID), int(identity.GID)); err != nil {
			_ = unix.Close(next)
			closeCurrent()
			return -1, func() {}, err
		}
		_ = unix.Close(current)
		current = next
	}
	return current, closeCurrent, nil
}

func mergeNativeCopyTree(sourceFD, destinationFD int) error {
	entries, err := readNativeCopyDir(sourceFD)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		var source unix.Stat_t
		if err := unix.Fstatat(sourceFD, name, &source, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if source.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := mergeNativeCopyDirectoryEntry(sourceFD, destinationFD, name, source); err != nil {
				return err
			}
			continue
		}
		if err := removeNativeCopyDestination(destinationFD, name); err != nil {
			return err
		}
		if err := unix.Renameat(sourceFD, name, destinationFD, name); err != nil {
			return err
		}
	}
	return nil
}

func mergeNativeCopyDirectoryEntry(sourceParent, destinationParent int, name string, source unix.Stat_t) error {
	destination, err := unix.Openat(destinationParent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return unix.Renameat(sourceParent, name, destinationParent, name)
	}
	if err != nil {
		if removeErr := removeNativeCopyDestination(destinationParent, name); removeErr != nil {
			return removeErr
		}
		return unix.Renameat(sourceParent, name, destinationParent, name)
	}
	defer func() { _ = unix.Close(destination) }()
	sourceDirectory, err := unix.Openat(sourceParent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(sourceDirectory) }()
	if err := mergeNativeCopyTree(sourceDirectory, destination); err != nil {
		return err
	}
	if err := unix.Fchmod(destination, uint32(source.Mode)&0o777); err != nil {
		return err
	}
	if err := unix.Fchown(destination, int(source.Uid), int(source.Gid)); err != nil {
		return err
	}
	return unix.Unlinkat(sourceParent, name, unix.AT_REMOVEDIR)
}

func removeNativeCopyDestination(parentFD int, name string) error {
	var destination unix.Stat_t
	err := unix.Fstatat(parentFD, name, &destination, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if destination.Mode&unix.S_IFMT == unix.S_IFDIR {
		return removeNativeCopyTreeAt(parentFD, name)
	}
	return unix.Unlinkat(parentFD, name, 0)
}

func removeNativeCopyTreeAt(parentFD int, name string) error {
	directory, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return unix.Unlinkat(parentFD, name, 0)
	}
	defer func() { _ = unix.Close(directory) }()
	if err := unix.Fchown(directory, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	if err := unix.Fchmod(directory, 0o700); err != nil {
		return err
	}
	entries, err := readNativeCopyDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeNativeCopyDestination(directory, entry.Name()); err != nil {
			return err
		}
	}
	return unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
}

func readNativeCopyDir(fd int) ([]os.DirEntry, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicate), "native-copy-directory")
	if directory == nil {
		_ = unix.Close(duplicate)
		return nil, fmt.Errorf("wrap native copy directory descriptor")
	}
	defer func() { _ = directory.Close() }()
	return directory.ReadDir(-1)
}

func applyCopyIdentityContents(root string, identity db.ServiceIdentity) error {
	return filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filepath.Clean(path) == filepath.Clean(root) {
			return nil
		}
		if err := nativeServiceLchown(path, int(identity.UID), int(identity.GID)); err != nil {
			return fmt.Errorf("set copied path owner %s: %w", path, err)
		}
		return nil
	})
}
