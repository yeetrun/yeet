// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"
)

func TestParseGlobalFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantVal string
		wantSvc string
		wantOut []string
	}{
		{
			name:    "consumes separate value",
			args:    []string{"--host", "catch", "status"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "consumes equals value",
			args:    []string{"status", "--host=catch"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "last value wins",
			args:    []string{"--host", "one", "--host", "two", "status"},
			wantVal: "two",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "stops at double dash",
			args:    []string{"--host", "catch", "--", "--host", "ignored"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"--", "--host", "ignored"},
		},
		{
			name:    "unknown flags are preserved",
			args:    []string{"--unknown", "x", "--host", "catch"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"--unknown", "x"},
		},
		{
			name:    "service flag parsed",
			args:    []string{"--service", "svc-a", "status"},
			wantVal: "",
			wantSvc: "svc-a",
			wantOut: []string{"status"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, out, err := parseGlobalFlags(tt.args)
			if err != nil {
				t.Fatalf("parseGlobalFlags error: %v", err)
			}
			if flags.Host != tt.wantVal {
				t.Fatalf("Host = %q, want %q", flags.Host, tt.wantVal)
			}
			if flags.Service != tt.wantSvc {
				t.Fatalf("Service = %q, want %q", flags.Service, tt.wantSvc)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("out = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}

func TestParseListHostsFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantTags []string
	}{
		{
			name:     "default tags",
			args:     []string{},
			wantTags: []string{"tag:catch"},
		},
		{
			name:     "comma-separated tags",
			args:     []string{"list-hosts", "--tags", "tag:a,tag:b"},
			wantTags: []string{"tag:a", "tag:b"},
		},
		{
			name:     "repeated tags",
			args:     []string{"list-hosts", "--tags", "tag:a", "--tags", "tag:b"},
			wantTags: []string{"tag:a", "tag:b"},
		},
		{
			name:     "ignores unknown flags",
			args:     []string{"list-hosts", "--tags", "tag:a", "--unknown", "x"},
			wantTags: []string{"tag:a"},
		},
		{
			name:     "stops at double dash",
			args:     []string{"list-hosts", "--tags", "tag:a", "--", "--tags", "tag:b"},
			wantTags: []string{"tag:a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := parseListHostsFlags(tt.args)
			if err != nil {
				t.Fatalf("parseListHostsFlags error: %v", err)
			}
			if !reflect.DeepEqual(flags.Tags, tt.wantTags) {
				t.Fatalf("Tags = %#v, want %#v", flags.Tags, tt.wantTags)
			}
		})
	}
}

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
