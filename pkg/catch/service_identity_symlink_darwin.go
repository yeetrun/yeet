// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package catch

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func mutateServiceIdentitySymlink(parentFD int, name string, expected serviceIdentityInodeRecord, uid, gid uint32) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_SYMLINK, 0)
	if err != nil {
		return fmt.Errorf("open symlink by stable handle: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect opened symlink: %w", err)
	}
	if err := validateServiceIdentityMutationState(stat, expected, true); err != nil {
		return err
	}
	xattrs, err := listServiceIdentityOpenFDXattrs(fd)
	if err != nil {
		return fmt.Errorf("inspect opened symlink xattrs: %w", err)
	}
	if blocked := blockedServiceIdentityXattr(xattrs); blocked != "" {
		return fmt.Errorf("extended attribute %s appeared after inventory seal", blocked)
	}
	if err := unix.Fchown(fd, int(uid), int(gid)); err != nil {
		return fmt.Errorf("change opened symlink owner: %w", err)
	}
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
