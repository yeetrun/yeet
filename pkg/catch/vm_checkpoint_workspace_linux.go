// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package catch

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openVMCheckpointDirectoryPath(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openVMCheckpointDirectoryAt(parentFD int, name string) (int, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	how := &unix.OpenHow{
		Flags:   uint64(flags),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	}
	fd, err := unix.Openat2(parentFD, name, how)
	if err == nil || !errors.Is(err, unix.ENOSYS) {
		return fd, err
	}
	return unix.Openat(parentFD, name, flags, 0)
}

func renameVMCheckpointNoReplace(parentFD int, oldName, newName string) error {
	return unix.Renameat2(parentFD, oldName, parentFD, newName, unix.RENAME_NOREPLACE)
}
