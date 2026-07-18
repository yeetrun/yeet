//go:build linux

// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "syscall"

const isoNSFSMagic = 0x6e736673

func isISONamespaceHandle(path string) (bool, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, err
	}
	return stat.Type == isoNSFSMagic, nil
}
