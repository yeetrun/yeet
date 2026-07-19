// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !darwin && !linux

package catch

import "fmt"

func renameServiceIdentityRootNoReplace(_, _ string) error {
	return fmt.Errorf("atomic no-replace service-root publication is unsupported on this operating system")
}
