// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"runtime/debug"
	"strings"
)

// buildVersion is injected at build time via -ldflags.
var buildVersion string

// Version returns the release version if set, otherwise falls back to the commit hash.
func Version() string {
	if v := strings.TrimSpace(buildVersion); v != "" {
		return v
	}
	return VersionCommit()
}

// VersionCommit returns the commit hash of the current build.
func VersionCommit() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var dirty bool
	var commit string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if commit == "" {
		return "dev"
	}

	if len(commit) >= 9 {
		commit = commit[:9]
	}
	if dirty {
		commit += "+dirty"
	}
	return commit
}
