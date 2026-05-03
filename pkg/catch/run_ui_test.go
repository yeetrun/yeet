// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"strings"
	"testing"
)

func TestPlainRunUIOutputsKeyValueLines(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")
	ui.Start()
	ui.StartStep(runStepUpload)
	ui.DoneStep("16.61 MB @ 24.70 MB/s")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d (%q)", len(lines), buf.String())
	}
	if want := `action=run service=svc-a status=running step="Upload payload"`; lines[0] != want {
		t.Fatalf("unexpected first line: %q", lines[0])
	}
	if want := `action=run service=svc-a status=ok step="Upload payload" detail="16.61 MB @ 24.70 MB/s"`; lines[1] != want {
		t.Fatalf("unexpected second line: %q", lines[1])
	}
}

func TestRunUIHandlesKnownProgressMessages(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")

	ui.Printer("Detected binary file")
	ui.Printer("File received")
	ui.Printer("Installing service")
	ui.Printer(`Service "svc-a" installed`)
	ui.Printer("Service installed: svc-a")
	ui.Printer("Service restarted: svc-a")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		`action=run service=svc-a status=running step="Detect payload"`,
		`action=run service=svc-a status=ok step="Detect payload" detail=binary`,
		`action=run service=svc-a status=running step="Install service"`,
		`action=run service=svc-a status=ok step="Install service"`,
	}
	if len(lines) != len(want) {
		t.Fatalf("expected %d lines, got %d (%q)", len(want), len(lines), buf.String())
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestRunUIUpdateDetailPlainModeIsSilent(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")

	ui.StartStep(runStepUpload)
	ui.UpdateDetail("16.61 MB")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected only the start-step line, got %d (%q)", len(lines), buf.String())
	}
	if want := `action=run service=svc-a status=running step="Upload payload"`; lines[0] != want {
		t.Fatalf("line = %q, want %q", lines[0], want)
	}
}

func TestDetectedFileDetail(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
		ok   bool
	}{
		{name: "detected file", msg: "Detected binary file", want: "binary", ok: true},
		{name: "wrong prefix", msg: "Found binary file"},
		{name: "wrong suffix", msg: "Detected binary payload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := detectedFileDetail(tt.msg)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("detail = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunUIDetailText(t *testing.T) {
	tests := []struct {
		name   string
		step   string
		detail string
		want   string
	}{
		{name: "without detail", step: runStepUpload, want: runStepUpload},
		{name: "with detail", step: runStepUpload, detail: "16.61 MB", want: "Upload payload 16.61 MB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runUIDetailText(tt.step, tt.detail); got != tt.want {
				t.Fatalf("runUIDetailText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShouldUpdateRunUISpinner(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		suspended  bool
		hasSpinner bool
		want       bool
	}{
		{name: "enabled active spinner", enabled: true, hasSpinner: true, want: true},
		{name: "plain mode", hasSpinner: true},
		{name: "suspended", enabled: true, suspended: true, hasSpinner: true},
		{name: "no spinner", enabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUpdateRunUISpinner(tt.enabled, tt.suspended, tt.hasSpinner)
			if got != tt.want {
				t.Fatalf("shouldUpdateRunUISpinner = %v, want %v", got, tt.want)
			}
		})
	}
}
