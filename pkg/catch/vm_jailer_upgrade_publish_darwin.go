// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package catch

import (
	"os"

	"golang.org/x/sys/unix"
)

// Darwin has no linkat AT_EMPTY_PATH equivalent. This name-bound fallback is
// only for local macOS development and tests; Catch's production VM runtime is
// Linux and uses the open-fd implementation in vm_jailer_upgrade_publish_linux.go.
func publishOpenVMJailerNoReplace(dir, _ *os.File, tempName, targetName string) error {
	return unix.Linkat(int(dir.Fd()), tempName, int(dir.Fd()), targetName, 0)
}
