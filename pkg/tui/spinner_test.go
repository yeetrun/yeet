// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSpinnerStartUpdateStopClear(t *testing.T) {
	var out bytes.Buffer
	spinner := NewSpinner(
		&out,
		WithFrames([]string{"-"}),
		WithHideCursor(true),
		WithInterval(time.Hour),
		WithColor(Colorizer{Enabled: true}, ColorGreen),
	)

	spinner.Start("starting")
	spinner.Update("running")
	spinner.Stop(true)

	got := out.String()
	for _, want := range []string{
		"\x1b[?25l",
		"\r\033[K" + ColorGreen + "-",
		"starting",
		"running",
		"\r\033[K",
		"\x1b[?25h",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("spinner output missing %q in %q", want, got)
		}
	}
}

func TestSpinnerStopWithoutClearPrintsNewline(t *testing.T) {
	var out bytes.Buffer
	spinner := NewSpinner(&out, WithFrames([]string{"-"}), WithInterval(time.Hour))

	spinner.Start("running")
	spinner.Stop(false)

	if got := out.String(); !strings.HasSuffix(got, "\n") {
		t.Fatalf("spinner output = %q, want trailing newline", got)
	}
}

func TestSpinnerUpdateBeforeStartIsNoop(t *testing.T) {
	var out bytes.Buffer
	spinner := NewSpinner(&out)

	spinner.Update("not running")
	spinner.Stop(true)

	if out.Len() != 0 {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestSpinnerStartWhileRunningUpdatesMessage(t *testing.T) {
	var out bytes.Buffer
	spinner := NewSpinner(&out, WithFrames([]string{"-"}), WithInterval(time.Hour))

	spinner.Start("first")
	spinner.Start("second")
	spinner.Stop(true)

	if got := out.String(); !strings.Contains(got, "first") {
		t.Fatalf("spinner output = %q, want first render", got)
	}
}
