// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"strings"
	"testing"
)

func TestParseInitArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantPos     []string
		wantGithub  bool
		wantNightly bool
		wantErr     bool
	}{
		{
			name:    "strips command name",
			args:    []string{"init", "root@example.com"},
			wantPos: []string{"root@example.com"},
		},
		{
			name:        "parses flags",
			args:        []string{"--from-github", "--nightly", "root@example.com"},
			wantPos:     []string{"root@example.com"},
			wantGithub:  true,
			wantNightly: true,
		},
		{
			name:    "rejects too many args",
			args:    []string{"one", "two"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pos, opts, err := parseInitArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInitArgs failed: %v", err)
			}
			if strings.Join(pos, "\n") != strings.Join(tt.wantPos, "\n") {
				t.Fatalf("pos = %#v, want %#v", pos, tt.wantPos)
			}
			if opts.fromGithub != tt.wantGithub {
				t.Fatalf("fromGithub = %v, want %v", opts.fromGithub, tt.wantGithub)
			}
			if opts.nightly != tt.wantNightly {
				t.Fatalf("nightly = %v, want %v", opts.nightly, tt.wantNightly)
			}
		})
	}
}

func TestBuildCatchCmdDisablesCGOForCrossCompile(t *testing.T) {
	cmd := buildCatchCmd("amd64", "/tmp/repo")

	if got := cmd.Dir; got != "/tmp/repo" {
		t.Fatalf("Dir = %q, want /tmp/repo", got)
	}
	if got := strings.Join(cmd.Args, " "); got != "go build -o catch ./cmd/catch" {
		t.Fatalf("Args = %q", got)
	}

	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{
		"GOOS=linux",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("expected %q in env", want)
		}
	}
}

func TestNormalizeBuildTarget(t *testing.T) {
	goos, goarch, err := normalizeBuildTarget("Linux", "AMD64", "/tmp/repo")
	if err != nil {
		t.Fatalf("normalizeBuildTarget failed: %v", err)
	}
	if goos != "linux" || goarch != "amd64" {
		t.Fatalf("target = %s/%s, want linux/amd64", goos, goarch)
	}

	if _, _, err := normalizeBuildTarget("Darwin", "arm64", "/tmp/repo"); err == nil {
		t.Fatal("expected non-linux error")
	}
	if _, _, err := normalizeBuildTarget("Linux", "arm64", ""); err == nil {
		t.Fatal("expected missing git root error")
	}
}

func TestFormatBuildDetail(t *testing.T) {
	if got := formatBuildDetail("", "linux/amd64", 0); got != "linux/amd64" {
		t.Fatalf("detail = %q", got)
	}
	got := formatBuildDetail("go version go1.25.0 darwin/arm64", "linux/arm64", 2048)
	want := "go version go1.25.0 darwin/arm64 -> linux/arm64, 2.00 KB"
	if got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
}

func TestCatchInstallEnv(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "user and host", in: "root@example.com", want: []string{"CATCH_INSTALL_USER=root", "CATCH_INSTALL_HOST=example.com"}},
		{name: "host only", in: "example.com", want: []string{"CATCH_INSTALL_HOST=example.com"}},
		{name: "empty", in: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catchInstallEnv(tt.in)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("env = %#v, want %#v", got, tt.want)
			}
		})
	}
}
