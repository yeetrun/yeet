// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !darwin && !linux

package catch

import "fmt"

func mutateServiceIdentitySymlink(_ int, _ string, expected serviceIdentityInodeRecord, uid, gid uint32) error {
	return fmt.Errorf("service identity migration cannot safely change symlink ownership on this platform; replace %s with a non-symlink or pre-own it as %d:%d", expected.Path, uid, gid)
}
