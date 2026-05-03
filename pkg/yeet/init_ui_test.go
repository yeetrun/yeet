// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestInitUIUpdateDetailWritesSpinnerMessage(t *testing.T) {
	var buf bytes.Buffer
	ui := newInitUI(&buf, true, false, "catch", "root@example.com", catchServiceName)

	ui.StartStep("Upload catch")
	ui.UpdateDetail("")
	ui.UpdateDetail("50% 1.00 KB/2.00 KB")
	ui.Stop()

	out := buf.String()
	if !strings.Contains(out, "Upload catch") {
		t.Fatalf("output = %q, want step name", out)
	}
	if !strings.Contains(out, "50% 1.00 KB/2.00 KB") {
		t.Fatalf("output = %q, want detail update", out)
	}
}

func TestInitUIUpdateDetailSkipsPlainAndQuietModes(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		quiet   bool
	}{
		{name: "plain", enabled: false},
		{name: "quiet", enabled: true, quiet: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			ui := newInitUI(&buf, tt.enabled, tt.quiet, "catch", "root@example.com", catchServiceName)
			ui.StartStep("Upload catch")
			before := buf.String()
			ui.UpdateDetail("50%")
			ui.Stop()
			if got := buf.String(); got != before {
				t.Fatalf("output changed from %q to %q", before, got)
			}
		})
	}
}

func TestInitUIProgressSettings(t *testing.T) {
	tests := []struct {
		name        string
		mode        catchrpc.ProgressMode
		isTTY       bool
		wantEnabled bool
		wantQuiet   bool
	}{
		{name: "tty mode forces spinner", mode: catchrpc.ProgressTTY, wantEnabled: true},
		{name: "plain mode disables spinner", mode: catchrpc.ProgressPlain},
		{name: "quiet mode disables output", mode: catchrpc.ProgressQuiet, wantQuiet: true},
		{name: "auto follows tty", mode: catchrpc.ProgressAuto, isTTY: true, wantEnabled: true},
		{name: "empty follows tty", isTTY: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled, quiet := initProgressSettings(tt.mode, tt.isTTY)
			if enabled != tt.wantEnabled || quiet != tt.wantQuiet {
				t.Fatalf("settings = enabled:%v quiet:%v, want enabled:%v quiet:%v", enabled, quiet, tt.wantEnabled, tt.wantQuiet)
			}
		})
	}
}
