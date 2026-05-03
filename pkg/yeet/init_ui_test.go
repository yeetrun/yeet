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

func TestInitUIPlainMessagesAndSteps(t *testing.T) {
	var buf bytes.Buffer
	ui := newInitUI(&buf, false, false, "catch", "root@example.com", catchServiceName)

	ui.Info(" installed ")
	ui.Warn(" docker missing ")
	ui.StartStep("Check local")
	ui.DoneStep("go version go1.25.0")
	ui.StartStep("Install catch")
	ui.FailStep("install failed")

	out := buf.String()
	for _, want := range []string{
		`status=info`,
		`detail=installed`,
		`status=warn`,
		`detail="docker missing"`,
		`status=running step="Check local"`,
		`status=ok step="Check local"`,
		`status=err step="Install catch"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestInitUISpinnerModeDirectStatusAndMessages(t *testing.T) {
	var buf bytes.Buffer
	ui := newInitUI(&buf, true, false, "catch", "root@example.com", catchServiceName)

	ui.Start()
	ui.Info(" connected ")
	ui.Warn(" warning ")
	ui.current = "Check local"
	ui.DoneStep("ok")
	ui.current = "Install catch"
	ui.FailStep("failed")
	ui.Suspend()
	if !ui.suspended {
		t.Fatal("Suspend did not mark UI suspended")
	}
	ui.Resume()
	if ui.suspended {
		t.Fatal("Resume left UI suspended")
	}

	out := buf.String()
	for _, want := range []string{
		"[+] yeet init root@example.com (host=catch)",
		"connected",
		"warning",
		"Check local",
		"ok",
		"Install catch",
		"failed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestInitUIQuietSkipsMessagesAndSteps(t *testing.T) {
	var buf bytes.Buffer
	ui := newInitUI(&buf, true, true, "catch", "root@example.com", catchServiceName)

	ui.Start()
	ui.Suspend()
	ui.Resume()
	ui.StartStep("Check local")
	ui.Info("info")
	ui.Warn("warn")
	ui.DoneStep("ok")
	ui.FailStep("err")
	ui.Stop()

	if buf.Len() != 0 {
		t.Fatalf("quiet output = %q, want empty", buf.String())
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
