// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package copyutil

import (
	"archive/tar"
	"fmt"

	"golang.org/x/sys/unix"
)

func createSpecial(path string, hdr *tar.Header) error {
	mode := uint32(hdr.Mode)
	switch hdr.Typeflag {
	case tar.TypeChar:
		mode |= unix.S_IFCHR
	case tar.TypeBlock:
		mode |= unix.S_IFBLK
	case tar.TypeFifo:
		mode |= unix.S_IFIFO
	default:
		return fmt.Errorf("unsupported special type %q", hdr.Typeflag)
	}
	dev := unix.Mkdev(uint32(hdr.Devmajor), uint32(hdr.Devminor))
	return unix.Mknod(path, mode, int(dev))
}
