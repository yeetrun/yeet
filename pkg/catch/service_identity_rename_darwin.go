// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package catch

import "golang.org/x/sys/unix"

func renameServiceIdentityRootNoReplace(oldPath, newPath string) error {
	return unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
}
