// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package catch

import (
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
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func renameVMCheckpointNoReplace(parentFD int, oldName, newName string) error {
	return unix.RenameatxNp(parentFD, oldName, parentFD, newName, unix.RENAME_EXCL)
}
