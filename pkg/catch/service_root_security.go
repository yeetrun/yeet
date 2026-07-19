// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// validateHostControlledServiceRootPath makes path-based root lifecycle
// operations safe from non-Catch users replacing an ancestor component. Catch
// owns the path name; the workload may own data below the root, but it must not
// be able to rename the root (or one of its ancestors) while Catch is mutating
// it as root.
func validateHostControlledServiceRootPath(root string) error {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return fmt.Errorf("service root %q must be an absolute non-root path", root)
	}
	parts := strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator))
	return validateHostControlledServiceRootComponents(root, parts)
}

func validateHostControlledServiceRootComponents(root string, parts []string) error {
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open filesystem root while validating service root %s: %w", root, err)
	}
	defer func() { _ = unix.Close(current) }()

	currentPath := string(filepath.Separator)
	for _, part := range parts {
		var parent unix.Stat_t
		if err := unix.Fstat(current, &parent); err != nil {
			return fmt.Errorf("inspect service root parent %s: %w", currentPath, err)
		}
		var child unix.Stat_t
		err := unix.Fstatat(current, part, &child, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			if serviceRootParentAllowsUntrustedRename(parent, nil) {
				return unsafeServiceRootParentError(root, currentPath)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect service root component %s: %w", filepath.Join(currentPath, part), err)
		}
		if child.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("service root component %s must be a non-symlink directory", filepath.Join(currentPath, part))
		}
		if serviceRootParentAllowsUntrustedRename(parent, &child) {
			return unsafeServiceRootParentError(root, currentPath)
		}
		next, err := unix.Openat(current, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("open service root component %s without following links: %w", filepath.Join(currentPath, part), err)
		}
		_ = unix.Close(current)
		current = next
		currentPath = filepath.Join(currentPath, part)
	}
	return nil
}

func serviceRootParentAllowsUntrustedRename(parent unix.Stat_t, child *unix.Stat_t) bool {
	mode := uint32(parent.Mode)
	trustedUID := uint32(os.Geteuid())
	ownerCanRename := serviceRootUntrustedOwnerCanRename(parent, trustedUID)
	groupCanRename := serviceRootPermissionAllowsRename(mode, unix.S_IWGRP, unix.S_IXGRP)
	otherCanRename := serviceRootPermissionAllowsRename(mode, unix.S_IWOTH, unix.S_IXOTH)
	if serviceRootStickyProtectsChild(parent, child, trustedUID) {
		groupCanRename = false
		otherCanRename = false
	}
	return ownerCanRename || groupCanRename || otherCanRename
}

func serviceRootUntrustedOwnerCanRename(parent unix.Stat_t, trustedUID uint32) bool {
	return parent.Uid != 0 && parent.Uid != trustedUID &&
		serviceRootPermissionAllowsRename(uint32(parent.Mode), unix.S_IWUSR, unix.S_IXUSR)
}

func serviceRootPermissionAllowsRename(mode, write, execute uint32) bool {
	return mode&write != 0 && mode&execute != 0
}

func serviceRootStickyProtectsChild(parent unix.Stat_t, child *unix.Stat_t, trustedUID uint32) bool {
	return parent.Mode&unix.S_ISVTX != 0 && child != nil && child.Uid == trustedUID
}

func unsafeServiceRootParentError(root, parent string) error {
	return fmt.Errorf(
		"service root %s is not host-controlled because parent %s can be modified by non-Catch users; use /var/lib/yeet/services/<svc> or make every parent Catch-owned and not group/world-writable",
		root, parent,
	)
}
