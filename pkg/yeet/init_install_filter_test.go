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
