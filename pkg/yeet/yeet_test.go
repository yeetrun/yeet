// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import "testing"

func TestLooksLikeImageRef(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "dockerhub tag", payload: "nginx:latest", want: true},
		{name: "ghcr tag", payload: "ghcr.io/org/app:1.2.3", want: true},
		{name: "registry with port", payload: "registry.example.com:5000/org/app:tag", want: true},
		{name: "digest ref", payload: "ghcr.io/org/app@sha256:deadbeef", want: true},
		{name: "no tag", payload: "nginx", want: false},
		{name: "no tag with slash", payload: "ghcr.io/org/app", want: false},
		{name: "file path", payload: "./compose.yml", want: false},
		{name: "url", payload: "https://example.com/app:latest", want: false},
		{name: "whitespace", payload: "ghcr.io/org/app:latest --flag", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeImageRef(tt.payload); got != tt.want {
				t.Fatalf("looksLikeImageRef(%q) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}
