// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	vmJailStorageOpenAt = openVMJailStorageAt
	vmJailStorageChown  = func(file *os.File, uid, gid int) error {
		return file.Chown(uid, gid)
	}
	vmJailStorageChmod = func(file *os.File, mode os.FileMode) error {
		return file.Chmod(mode)
	}
)

func delegateVMJailStorageFile(path string, identity vmRuntimeIdentity) error {
	file, parent, name, err := openVMJailStoragePath(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if parent != nil {
		defer func() { _ = parent.Close() }()
	}

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect VM jail storage %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("VM jail storage %s must be a regular file", path)
	}
	if err := mutateVMJailStorageFile(file, path, info, identity); err != nil {
		return err
	}
	return verifyVMJailStorageEntryUnchanged(parent, name, file, path)
}

func delegateOpenedVMJailStorageTree(parent *os.File, name string, file *os.File, path string, info os.FileInfo, identity vmRuntimeIdentity) error {
	if err := mutateVMJailStorageFile(file, path, info, identity); err != nil {
		return err
	}
	if err := verifyVMJailStorageEntryUnchanged(parent, name, file, path); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}

	names, err := file.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("read VM jail storage directory %s: %w", path, err)
	}
	sort.Strings(names)
	for _, name := range names {
		childPath := filepath.Join(path, name)
		child, childInfo, err := openVerifiedVMJailStorageChild(file, name, childPath)
		if err != nil {
			return err
		}
		err = delegateOpenedVMJailStorageTree(file, name, child, childPath, childInfo, identity)
		closeErr := child.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return fmt.Errorf("close VM jail storage %s: %w", childPath, closeErr)
		}
	}
	return nil
}

func mutateVMJailStorageFile(file *os.File, path string, info os.FileInfo, identity vmRuntimeIdentity) error {
	requiredMode := os.FileMode(0o600)
	if info.IsDir() {
		requiredMode = 0o700
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("VM jail storage %s must be a regular file or directory", path)
	}
	if err := vmJailStorageChown(file, identity.UID, identity.GID); err != nil {
		return fmt.Errorf("delegate VM jail storage %s: %w", path, err)
	}
	if err := vmJailStorageChmod(file, info.Mode().Perm()|requiredMode); err != nil {
		return fmt.Errorf("set delegated VM jail storage permissions %s: %w", path, err)
	}
	return nil
}

func verifyVMJailStorageEntryUnchanged(parent *os.File, name string, file *os.File, path string) error {
	if parent == nil {
		return nil
	}
	var entry unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("VM jail storage %s changed during delegation: %w", path, err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return fmt.Errorf("inspect delegated VM jail storage %s: %w", path, err)
	}
	if entry.Dev != opened.Dev || entry.Ino != opened.Ino {
		return fmt.Errorf("VM jail storage %s changed during delegation", path)
	}
	return nil
}

func openVMJailStoragePath(path string) (*os.File, *os.File, string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return nil, nil, "", fmt.Errorf("VM jail storage path must be absolute: %s", path)
	}
	current, err := os.Open(string(filepath.Separator))
	if err != nil {
		return nil, nil, "", fmt.Errorf("open trusted VM jail storage root: %w", err)
	}
	if path == string(filepath.Separator) {
		return current, nil, "", nil
	}

	currentPath := string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		childPath := filepath.Join(currentPath, component)
		child, info, err := openVerifiedVMJailStorageChild(current, component, childPath)
		if err != nil {
			_ = current.Close()
			return nil, nil, "", err
		}
		if index < len(components)-1 && !info.IsDir() {
			_ = child.Close()
			_ = current.Close()
			return nil, nil, "", fmt.Errorf("VM jail storage ancestor %s must be a directory", childPath)
		}
		if index == len(components)-1 {
			return child, current, component, nil
		}
		if err := current.Close(); err != nil {
			_ = child.Close()
			return nil, nil, "", fmt.Errorf("close VM jail storage ancestor %s: %w", currentPath, err)
		}
		current = child
		currentPath = childPath
	}
	_ = current.Close()
	return nil, nil, "", fmt.Errorf("open VM jail storage %s", path)
}

func openVerifiedVMJailStorageChild(parent *os.File, name, path string) (*os.File, os.FileInfo, error) {
	if err := validateVMJailStorageChildName(name); err != nil {
		return nil, nil, err
	}
	before, beforeType, err := lstatVMJailStorageChild(parent, name, path)
	if err != nil {
		return nil, nil, err
	}
	file, err := openVMJailStorageChildNoFollow(parent, name, path)
	if err != nil {
		return nil, nil, err
	}
	info, err := verifyOpenedVMJailStorageChild(file, path, before, beforeType)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func validateVMJailStorageChildName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
		return fmt.Errorf("invalid VM jail storage path component %q", name)
	}
	return nil
}

func lstatVMJailStorageChild(parent *os.File, name, path string) (unix.Stat_t, uint32, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, 0, fmt.Errorf("inspect VM jail storage %s: %w", path, err)
	}
	beforeType := uint32(before.Mode) & unix.S_IFMT
	if beforeType == unix.S_IFLNK {
		return unix.Stat_t{}, 0, fmt.Errorf("refusing to delegate symbolic link in VM jail storage: %s", path)
	}
	if beforeType != unix.S_IFREG && beforeType != unix.S_IFDIR {
		return unix.Stat_t{}, 0, fmt.Errorf("VM jail storage %s must be a regular file or directory", path)
	}
	return before, beforeType, nil
}

func openVMJailStorageChildNoFollow(parent *os.File, name, path string) (*os.File, error) {
	fd, err := vmJailStorageOpenAt(int(parent.Fd()), name)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("refusing to delegate symbolic link in VM jail storage: %s", path)
		}
		return nil, fmt.Errorf("open VM jail storage %s without following links: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open VM jail storage %s", path)
	}
	return file, nil
}

func verifyOpenedVMJailStorageChild(file *os.File, path string, before unix.Stat_t, beforeType uint32) (os.FileInfo, error) {
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return nil, fmt.Errorf("inspect opened VM jail storage %s: %w", path, err)
	}
	openedType := uint32(opened.Mode) & unix.S_IFMT
	if before.Dev != opened.Dev || before.Ino != opened.Ino || beforeType != openedType {
		return nil, fmt.Errorf("VM jail storage %s changed before delegation", path)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened VM jail storage %s: %w", path, err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("VM jail storage %s must be a regular file or directory", path)
	}
	return info, nil
}
