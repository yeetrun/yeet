// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

type updateAdvisoryRequest struct {
	Args             []string
	ExitCode         int
	StdoutTTY        bool
	StderrTTY        bool
	Local            buildinfo.Info
	Cache            updateCheckCache
	Now              time.Time
	ProjectHostCount int
}

func MaybePrintUpdateAdvisory(w io.Writer, args []string, exitCode int, stdoutTTY bool, stderrTTY bool, projectHostCount int) {
	cache := readUpdateCheckCache(updateCheckCacheFile)
	req := updateAdvisoryRequest{
		Args:             args,
		ExitCode:         exitCode,
		StdoutTTY:        stdoutTTY,
		StderrTTY:        stderrTTY,
		Local:            buildinfo.Current(),
		Cache:            cache,
		Now:              time.Now(),
		ProjectHostCount: projectHostCount,
	}
	if maybePrintUpdateAdvisory(w, req) {
		latest := latestTagForAdvisory(req.Local, cache)
		if latest != "" {
			cache.LastAdvisory[latest] = req.Now
			_ = writeUpdateCheckCache(updateCheckCacheFile, cache)
		}
	}
}

func maybePrintUpdateAdvisory(w io.Writer, req updateAdvisoryRequest) bool {
	if suppressUpdateAdvisory(req) {
		return false
	}
	latest := latestTagForAdvisory(req.Local, req.Cache)
	if latest == "" || !req.Local.IsRelease() || buildinfo.CompareSemver(req.Local.Version, latest) >= 0 {
		return false
	}
	if last := req.Cache.LastAdvisory[latest]; !last.IsZero() && req.Now.Sub(last) < updateCheckCacheTTL {
		return false
	}
	_, _ = fmt.Fprintf(w, "Update available: yeet %s -> %s.\n", req.Local.Version, latest)
	if req.ProjectHostCount > 1 {
		_, _ = fmt.Fprintf(w, "Run: yeet upgrade check to scan %d project catch hosts.\n", req.ProjectHostCount)
	} else {
		_, _ = fmt.Fprintln(w, "Run: yeet upgrade check")
	}
	return true
}

func suppressUpdateAdvisory(req updateAdvisoryRequest) bool {
	if req.ExitCode != 0 || !req.StdoutTTY || !req.StderrTTY || os.Getenv("YEET_NO_UPDATE_CHECK") != "" {
		return true
	}
	if len(req.Args) == 0 {
		return true
	}
	switch req.Args[0] {
	case "upgrade", "version", "help", "init", "skirt":
		return true
	}
	for _, arg := range req.Args {
		if suppressUpdateAdvisoryArg(arg) {
			return true
		}
	}
	return false
}

func suppressUpdateAdvisoryArg(arg string) bool {
	return arg == "--help" ||
		arg == "-h" ||
		arg == "--help-agent" ||
		arg == "--json" ||
		strings.Contains(arg, "json")
}

func latestTagForAdvisory(local buildinfo.Info, cache updateCheckCache) string {
	if local.ReleaseChannel() == buildinfo.ChannelNightly {
		return cache.LatestNightly.Tag
	}
	return cache.LatestStable.Tag
}

func ProjectHostCountForAdvisory() int {
	cfgLoc, err := loadProjectConfigFromCwd()
	if err != nil || cfgLoc == nil || cfgLoc.Config == nil {
		return 1
	}
	if n := len(cfgLoc.Config.AllHosts()); n > 0 {
		return n
	}
	return 1
}
