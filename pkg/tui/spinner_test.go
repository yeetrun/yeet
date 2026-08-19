// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSpinnerStartUpdateStopClear(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	var out bytes.Buffer
	styles := NewStyles(true)
	spinner := NewSpinner(
		&out,
		WithFrames([]string{"-"}),
		WithHideCursor(true),
		WithInterval(time.Hour),
		WithStyle(styles, RoleSuccess),
	)

	spinner.Start("starting")
	spinner.Update("running")
	spinner.Stop(true)

	got := out.String()
	wantPrefix := "\x1b[?25l\r\033[K" + styles.Render(RoleSuccess, "-")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("spinner output = %q, want prefix %q", got, wantPrefix)
	}
	for _, want := range []string{
		"\x1b[?25l",
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
	spinner.Stop(false)

	got := out.String()
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("spinner output = %q, want trailing newline", got)
	}
	if count := strings.Count(got, "\n"); count != 1 {
		t.Fatalf("spinner output = %q, want one newline after repeated stop", got)
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
	spinner.mu.Lock()
	message := spinner.msg
	spinner.mu.Unlock()
	spinner.Stop(true)

	if got := out.String(); !strings.Contains(got, "first") {
		t.Fatalf("spinner output = %q, want first render", got)
	}
	if message != "second" {
		t.Fatalf("spinner message = %q, want repeated start to update it", message)
	}
}

func TestSpinnerConcurrentUpdatesAreSerialized(t *testing.T) {
	var out bytes.Buffer
	spinner := NewSpinner(&out, WithFrames([]string{"-", "+"}), WithInterval(time.Millisecond))

	spinner.Start("start")
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				spinner.Update("worker")
			}
		}(worker)
	}
	wg.Wait()
	spinner.Stop(true)
}

func TestSpinnerStopWhileUpdatesAreRendering(t *testing.T) {
	var out bytes.Buffer
	spinner := NewSpinner(&out, WithFrames([]string{"-", "+"}), WithInterval(time.Millisecond))

	spinner.Start("start")
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				spinner.Update("worker")
			}
		}()
	}
	spinner.Stop(true)
	wg.Wait()
}
