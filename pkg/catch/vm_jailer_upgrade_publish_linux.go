// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package catch

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishOpenVMJailerNoReplace(dir, temp *os.File, _, targetName string) error {
	return unix.Linkat(int(temp.Fd()), "", int(dir.Fd()), targetName, unix.AT_EMPTY_PATH)
}
