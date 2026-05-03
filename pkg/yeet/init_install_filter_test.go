// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"strings"
	"testing"
)

func TestInitInstallFilterSummaryAndOutput(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	input := strings.Join([]string{
		`2026/01/02 18:15:34 data dir: /root/data`,
		`2026/01/02 18:15:34 tsnet running state path /root/data/tsnet/tailscaled.state`,
		`2026/01/02 18:15:35 skipping image "plain-df"`,
		`2026/01/02 18:15:35 skipping image "nginx"`,
		`2026/01/02 18:15:35 copying /root/data/services/catch/bin/catch-123 to /root/data/services/catch/run/catch`,
		`Warning: docker is recommended but not installed`,
		`2026/01/02 18:15:36 Service "catch" installed`,
	}, "\n") + "\n"

	if _, err := filter.Write([]byte(input)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	gotOutput := strings.TrimSpace(buf.String())
	if gotOutput != "Warning: docker is recommended but not installed" {
		t.Fatalf("unexpected output: %q", gotOutput)
	}

	wantDetail := "systemd data=/root/data tsnet=/root/data/tsnet skipped-images=2"
	if detail := filter.SummaryDetail(); detail != wantDetail {
		t.Fatalf("unexpected detail: %q", detail)
	}
}

func TestInitInstallFilterFlushesPrompt(t *testing.T) {
	var buf bytes.Buffer
	filter := newInitInstallFilter(&buf)

	prompt := "Would you like to install docker? [y/N]: "
	if _, err := filter.Write([]byte(prompt)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	if got := buf.String(); got != prompt {
		t.Fatalf("expected prompt to pass through, got %q", got)
	}
}

func TestInitInstallSummaryAbsorbPathsImagesAndWarnings(t *testing.T) {
	var summary initInstallSummary

	absorbed := []string{
		"data dir: /srv/yeet",
		`tsnet starting with hostname "catch" varRoot "/srv/yeet/tsnet"`,
		`skipping image "nginx"`,
		`Installation of docker skipped`,
	}
	for _, msg := range absorbed {
		if !summary.Absorb(msg) {
			t.Fatalf("expected %q to be absorbed", msg)
		}
	}

	if summary.Detail() != "systemd data=/srv/yeet tsnet=/srv/yeet/tsnet skipped-images=1" {
		t.Fatalf("detail = %q", summary.Detail())
	}
	if summary.WarningSummary() != "Installation of docker skipped" {
		t.Fatalf("warning = %q", summary.WarningSummary())
	}
}

func TestInitInstallSummaryVisibleWarnings(t *testing.T) {
	var summary initInstallSummary

	for _, msg := range []string{
		"Warning: docker is recommended but not installed",
		"Failed to install service: exit status 1",
	} {
		if summary.Absorb(msg) {
			t.Fatalf("expected %q to remain visible", msg)
		}
	}

	want := "Warning: docker is recommended but not installed; Failed to install service: exit status 1"
	if got := summary.WarningSummary(); got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}
