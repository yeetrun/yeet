// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"os"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/yeetrun/yeet/pkg/tui"
)

func TestRenderSkirtFrameDisabledIsExact(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	const frame = "parrot"
	style := lipgloss.NewStyle().Foreground(lipgloss.Red)
	if got := renderSkirtFrame(tui.NewStyles(false), style, frame); got != frame {
		t.Fatalf("renderSkirtFrame() = %q, want exact frame %q", got, frame)
	}
}

func TestSkirtStopsWhenContextCancelled(t *testing.T) {
	oldStdout := os.Stdout
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile devnull error: %v", err)
	}
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = devNull.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := HandleSkirt(ctx, nil); err != nil {
		t.Fatalf("HandleSkirt error: %v", err)
	}
}
