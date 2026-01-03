// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"strings"
	"testing"
)

func TestResolveServiceHostSingle(t *testing.T) {
	cfg := &ProjectConfig{Services: []ServiceEntry{
		{Name: "svc-a", Host: "host-a"},
	}}
	host, err := resolveServiceHost(cfg, "svc-a")
	if err != nil {
		t.Fatalf("resolveServiceHost error: %v", err)
	}
	if host != "host-a" {
		t.Fatalf("host = %q, want host-a", host)
	}
}

func TestResolveServiceHostAmbiguous(t *testing.T) {
	cfg := &ProjectConfig{Services: []ServiceEntry{
		{Name: "svc-a", Host: "host-a"},
		{Name: "svc-a", Host: "host-b"},
	}}
	_, err := resolveServiceHost(cfg, "svc-a")
	if err == nil {
		t.Fatalf("expected ambiguous error")
	}
	if got := err.Error(); got == "" || !containsAll(got, []string{"svc-a@host-a", "svc-a@host-b"}) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
