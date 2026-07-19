// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package catch

import "golang.org/x/sys/unix"

func exchangeVMJailerUnitNamesAt(oldDir int, oldName string, newDir int, newName string) error {
	return unix.Renameat2(oldDir, oldName, newDir, newName, unix.RENAME_EXCHANGE)
}

func renameVMJailerUnitNameNoReplaceAt(oldDir int, oldName string, newDir int, newName string) error {
	return unix.Renameat2(oldDir, oldName, newDir, newName, unix.RENAME_NOREPLACE)
}
