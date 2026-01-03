// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/yeet"
)

func TestApplyGlobalUIFlagsRejectsConflicts(t *testing.T) {
	yeet.ResetUIConfig()
	if err := applyGlobalUIFlags(globalFlagsParsed{TTY: true, NoTTY: true}); err == nil {
		t.Fatalf("expected error when both --tty and --no-tty are set")
	}
}

func TestApplyGlobalUIFlagsProgressMode(t *testing.T) {
	yeet.ResetUIConfig()
	if err := applyGlobalUIFlags(globalFlagsParsed{Progress: "plain"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := yeet.CurrentUIConfig()
	if cfg.Progress != catchrpc.ProgressPlain {
		t.Fatalf("expected progress mode %q, got %q", catchrpc.ProgressPlain, cfg.Progress)
	}
}

func TestApplyGlobalUIFlagsTTYOverride(t *testing.T) {
	yeet.ResetUIConfig()
	if err := applyGlobalUIFlags(globalFlagsParsed{NoTTY: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := yeet.CurrentUIConfig()
	if cfg.TTYOverride == nil || *cfg.TTYOverride {
		t.Fatalf("expected tty override to disable TTY")
	}
}
