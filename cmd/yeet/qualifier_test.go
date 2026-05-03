// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "testing"

func TestSplitQualifiedNameEdges(t *testing.T) {
	tests := []struct {
		value    string
		wantSvc  string
		wantHost string
		wantOK   bool
	}{
		{value: "svc@host", wantSvc: "svc", wantHost: "host", wantOK: true},
		{value: "svc", wantSvc: "svc"},
		{value: "@host", wantSvc: "@host"},
		{value: "svc@", wantSvc: "svc@"},
	}
	for _, tt := range tests {
		svc, host, ok := splitQualifiedName(tt.value)
		if svc != tt.wantSvc || host != tt.wantHost || ok != tt.wantOK {
			t.Fatalf("splitQualifiedName(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.value, svc, host, ok, tt.wantSvc, tt.wantHost, tt.wantOK)
		}
	}
}

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

func TestSplitCommandHostHandlesEmptyAndGroups(t *testing.T) {
	if host, out, ok := splitCommandHost(nil); ok || host != "" || out != nil {
		t.Fatalf("splitCommandHost(nil) = host=%q out=%v ok=%v, want zero", host, out, ok)
	}

	args := []string{"docker@host-a", "update", "svc-a"}
	host, out, ok := splitCommandHost(args)
	if !ok || host != "host-a" {
		t.Fatalf("splitCommandHost group = host=%q ok=%v, want host-a true", host, ok)
	}
	if len(out) != 3 || out[0] != "docker" {
		t.Fatalf("splitCommandHost group args = %v, want docker command rewritten", out)
	}
}

func TestSplitCommandHostIgnoresUnknown(t *testing.T) {
	args := []string{"not-a-command@host-a"}
	if host, out, ok := splitCommandHost(args); ok {
		t.Fatalf("expected no split, got host=%q args=%v", host, out)
	}
}
