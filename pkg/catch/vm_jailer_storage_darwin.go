// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package catch

import "golang.org/x/sys/unix"

func openVMJailStorageAt(parentFD int, name string) (int, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	return unix.Openat(parentFD, name, flags, 0)
}
