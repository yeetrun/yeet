// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "testing"

func TestSplitCommandHost(t *testing.T) {
	args := []string{"events@host-a"}
	host, out, ok := splitCommandHost(args)
	if !ok {
		t.Fatalf("expected to split command host")
	}
	if host != "host-a" {
		t.Fatalf("expected host host-a, got %q", host)
	}
	if len(out) != 1 || out[0] != "events" {
		t.Fatalf("unexpected args: %v", out)
	}
}

func TestSplitCommandHostIgnoresUnknown(t *testing.T) {
	args := []string{"not-a-command@host-a"}
	if host, out, ok := splitCommandHost(args); ok {
		t.Fatalf("expected no split, got host=%q args=%v", host, out)
	}
}
