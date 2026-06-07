// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "github.com/yeetrun/yeet/pkg/buildinfo"

// Version returns the release version if set, otherwise falls back to the commit hash.
func Version() string {
	return buildinfo.Version()
}

// VersionCommit returns the commit hash of the current build.
func VersionCommit() string {
	return buildinfo.CommitVersion()
}
