// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type initInstallErrWriter struct {
	err error
}

func (w initInstallErrWriter) Write([]byte) (int, error) {
	return 0, w.err
}

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
	if warning := filter.WarningSummary(); warning != "Warning: docker is recommended but not installed" {
		t.Fatalf("unexpected warning summary: %q", warning)
	}
	if info := filter.InfoSummary(); info != "" {
		t.Fatalf("unexpected info summary: %q", info)
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

func TestInitInstallFilterReportsOutputErrors(t *testing.T) {
	want := errors.New("write failed")
	filter := newInitInstallFilter(initInstallErrWriter{err: want})

	_, err := filter.Write([]byte("Error: install failed\n"))
	if !errors.Is(err, want) {
		t.Fatalf("Write error = %v, want %v", err, want)
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

func TestInitInstallSummaryInfoSummaryDedupes(t *testing.T) {
	summary := initInstallSummary{
		infos: []string{"next step", "next step", " "},
	}
	if got := summary.InfoSummary(); got != "next step" {
		t.Fatalf("InfoSummary = %q, want next step", got)
	}
}

func TestInitInstallLineClassifiers(t *testing.T) {
	ignored := []string{
		"NetNS: created",
		"Requires: docker",
		"Detected binary file",
		"File moved to /tmp/catch",
		"Removed old file: /tmp/catch",
		"copying catch",
		"adding unit catch.service",
		"Installing service catch",
		"File received",
		`Service "catch" installed`,
		"no env artifact found",
	}
	for _, msg := range ignored {
		if !isIgnoredInstallProgressLine(msg) {
			t.Fatalf("isIgnoredInstallProgressLine(%q) = false, want true", msg)
		}
	}

	important := []string{"Warning: docker missing", "Error: install failed", "operation failed", "runtime error"}
	for _, msg := range important {
		if !isImportantInitLine(msg) {
			t.Fatalf("isImportantInitLine(%q) = false, want true", msg)
		}
	}
	if isImportantInitLine("all good") {
		t.Fatal("isImportantInitLine all good = true, want false")
	}
}

func TestShouldFlushPartial(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "   "},
		{input: "Password:", want: true},
		{input: "Continue [y/n]", want: true},
		{input: "Name:", want: true},
		{input: "ordinary output"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := shouldFlushPartial(tt.input); got != tt.want {
				t.Fatalf("shouldFlushPartial(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
