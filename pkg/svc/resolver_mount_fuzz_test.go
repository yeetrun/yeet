// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"strings"
	"testing"
)

func FuzzVisibleResolverMount(f *testing.F) {
	for _, seed := range []string{
		"37 26 0:31 / /etc/resolv.conf ro,relatime - ext4 /dev/root rw",
		"38 26 0:31 / /etc/resolv.conf rw,relatime - ext4 /dev/root rw",
		"39 26 0:31 / /etc/resolv\\040conf ro - ext4 /dev/root rw",
		"malformed",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, mountInfo string) {
		const mountPoint = "/etc/resolv.conf"
		entry, err := visibleResolverMount(strings.NewReader(mountInfo), mountPoint)
		if err == nil && (entry.ID <= 0 || entry.MountPoint != mountPoint) {
			t.Fatalf("visibleResolverMount() = %#v, nil", entry)
		}
	})
}
