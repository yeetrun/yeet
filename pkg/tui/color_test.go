// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tui

import "testing"

func TestNewColorizer(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if c := NewColorizer(true); !c.Enabled {
		t.Fatalf("expected colorizer to be enabled")
	}

	if c := NewColorizer(false); c.Enabled {
		t.Fatalf("expected disabled colorizer")
	}

	t.Setenv("NO_COLOR", "1")
	if c := NewColorizer(true); c.Enabled {
		t.Fatalf("expected NO_COLOR to disable colorizer")
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if c := NewColorizer(true); c.Enabled {
		t.Fatalf("expected dumb terminal to disable colorizer")
	}
}

func TestColorizerWrap(t *testing.T) {
	if got := (Colorizer{}).Wrap(ColorRed, "text"); got != "text" {
		t.Fatalf("disabled wrap = %q", got)
	}
	if got := (Colorizer{Enabled: true}).Wrap("", "text"); got != "text" {
		t.Fatalf("empty code wrap = %q", got)
	}

	got := (Colorizer{Enabled: true}).Wrap(ColorRed, "text")
	want := ColorRed + "text" + ColorReset
	if got != want {
		t.Fatalf("enabled wrap = %q, want %q", got, want)
	}
}
