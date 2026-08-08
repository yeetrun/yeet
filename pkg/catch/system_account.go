// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

const (
	staticSystemAccountHome  = "/nonexistent"
	staticSystemAccountShell = "/usr/sbin/nologin"
)

func staticSystemUserAddArgs(name string, groupArgs ...string) []string {
	args := []string{"--system"}
	args = append(args, groupArgs...)
	return append(args,
		"--home-dir", staticSystemAccountHome,
		"--no-create-home",
		"--shell", staticSystemAccountShell,
		name,
	)
}
