// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildinfo

import (
	"runtime/debug"
	"strconv"
	"strings"
)

type Channel string

const (
	ChannelStable  Channel = "stable"
	ChannelNightly Channel = "nightly"
	ChannelDev     Channel = "dev"
	ChannelUnknown Channel = "unknown"
)

// BuildVersion is injected by release workflows with -ldflags.
var BuildVersion string

type Info struct {
	Version string  `json:"version"`
	Commit  string  `json:"commit,omitempty"`
	Dirty   bool    `json:"dirty,omitempty"`
	Channel Channel `json:"channel"`
}

type buildSetting struct {
	Key   string
	Value string
}

func Current() Info {
	commit, dirty, ok := readCommit()
	version := strings.TrimSpace(BuildVersion)
	if version == "" {
		version = commitVersion(commit, dirty, ok)
	}

	info := Info{
		Version: version,
		Commit:  commit,
		Dirty:   dirty,
	}
	info.Channel = info.ReleaseChannel()
	return info
}

func Version() string {
	return Current().Version
}

func CommitVersion() string {
	commit, dirty, ok := readCommit()
	return commitVersion(commit, dirty, ok)
}

func (i Info) ReleaseChannel() Channel {
	if i.Channel != "" {
		return i.Channel
	}

	version := strings.TrimSpace(i.Version)
	switch {
	case isStableVersion(version):
		return ChannelStable
	case strings.HasPrefix(version, "nightly-"):
		return ChannelNightly
	case version == "" || version == "unknown":
		return ChannelUnknown
	default:
		return ChannelDev
	}
}

func (i Info) IsRelease() bool {
	channel := i.ReleaseChannel()
	return channel == ChannelStable || channel == ChannelNightly
}

func CompareSemver(a, b string) int {
	av, aok := parseSemver(a)
	bv, bok := parseSemver(b)
	if !aok || !bok {
		return 0
	}

	for i := range av {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}

func readCommit() (string, bool, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, false
	}

	settings := make([]buildSetting, 0, len(info.Settings))
	for _, setting := range info.Settings {
		settings = append(settings, buildSetting{Key: setting.Key, Value: setting.Value})
	}
	return commitFromSettings(settings)
}

func commitVersionFromSettings(settings []buildSetting) string {
	commit, dirty, ok := commitFromSettings(settings)
	return commitVersion(commit, dirty, ok)
}

func commitFromSettings(settings []buildSetting) (string, bool, bool) {
	var commit string
	var dirty bool
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			commit = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	return commit, dirty, true
}

func commitVersion(commit string, dirty bool, ok bool) string {
	if !ok {
		return "unknown"
	}
	if commit == "" {
		return "dev"
	}
	if len(commit) > 9 {
		commit = commit[:9]
	}
	if dirty {
		commit += "+dirty"
	}
	return commit
}

func isStableVersion(version string) bool {
	_, ok := parseSemver(version)
	return ok
}

func parseSemver(version string) ([3]int, bool) {
	var out [3]int
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return out, false
	}

	for i, part := range parts {
		if part == "" {
			return out, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
