// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

func TestMaybePrintUpdateAdvisorySuppressesJSONAndUpgrade(t *testing.T) {
	cache := newUpdateCheckCache()
	cache.LatestStable = releaseCacheEntry{Tag: "v0.5.13", CheckedAt: time.Unix(100, 0)}
	for _, args := range [][]string{{"upgrade", "check"}, {"status", "--format=json"}, {"status", "--help-agent"}, {"version"}} {
		var out bytes.Buffer
		printed := maybePrintUpdateAdvisory(&out, updateAdvisoryRequest{
			Args:      args,
			ExitCode:  0,
			StdoutTTY: true,
			StderrTTY: true,
			Local:     buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable},
			Cache:     cache,
			Now:       time.Unix(100, 0),
		})
		if printed || out.Len() != 0 {
			t.Fatalf("args %#v printed advisory %q", args, out.String())
		}
	}
}

func TestMaybePrintUpdateAdvisoryPrintsLocalUpdate(t *testing.T) {
	cache := newUpdateCheckCache()
	cache.LatestStable = releaseCacheEntry{Tag: "v0.5.13", CheckedAt: time.Unix(100, 0)}
	var out bytes.Buffer
	printed := maybePrintUpdateAdvisory(&out, updateAdvisoryRequest{
		Args:             []string{"status"},
		ExitCode:         0,
		StdoutTTY:        true,
		StderrTTY:        true,
		Local:            buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable},
		Cache:            cache,
		Now:              time.Unix(100, 0),
		ProjectHostCount: 3,
	})
	if !printed || !strings.Contains(out.String(), "Update available") || !strings.Contains(out.String(), "yeet upgrade check --all") {
		t.Fatalf("advisory = %q", out.String())
	}
}
