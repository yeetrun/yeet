// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package catch

import "golang.org/x/sys/unix"

// Catch's production VM runtime is Linux. These Darwin operations exist only
// so local development and unit tests exercise equivalent name semantics.
func exchangeVMJailerUnitNamesAt(oldDir int, oldName string, newDir int, newName string) error {
	return unix.RenameatxNp(oldDir, oldName, newDir, newName, unix.RENAME_SWAP)
}

func renameVMJailerUnitNameNoReplaceAt(oldDir int, oldName string, newDir int, newName string) error {
	return unix.RenameatxNp(oldDir, oldName, newDir, newName, unix.RENAME_EXCL)
}
