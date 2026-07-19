// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package catch

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func mutateServiceIdentitySymlink(parentFD int, name string, expected serviceIdentityInodeRecord, uid, gid uint32) error {
	fd, err := openServiceIdentitySymlink(parentFD, name, expected)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Fchownat(fd, "", int(uid), int(gid), unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("change opened symlink owner: %w", err)
	}
	return verifyServiceIdentitySymlinkMutation(fd, expected, uid, gid)
}

func openServiceIdentitySymlink(parentFD int, name string, expected serviceIdentityInodeRecord) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open symlink by stable handle: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("inspect opened symlink: %w", err)
	}
	if err := validateServiceIdentityMutationState(stat, expected, true); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func verifyServiceIdentitySymlinkMutation(fd int, expected serviceIdentityInodeRecord, uid, gid uint32) error {
	var changed unix.Stat_t
	if err := unix.Fstat(fd, &changed); err != nil {
		return fmt.Errorf("verify opened symlink after ownership change: %w", err)
	}
	target := expected
	target.UID, target.GID = uid, gid
	if err := validateServiceIdentityMutationState(changed, target, true); err != nil {
		return fmt.Errorf("verify opened symlink after ownership change: %w", err)
	}
	return nil
}
