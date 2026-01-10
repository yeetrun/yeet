// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package copyutil

import (
	"archive/tar"
	"fmt"
)

func createSpecial(path string, hdr *tar.Header) error {
	return fmt.Errorf("special files not supported on Windows")
}
