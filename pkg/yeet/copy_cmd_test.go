// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCopyArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    copyRequest
		wantErr string
	}{
		{
			name: "remote destination with bundled flags",
			args: []string{"-azv", "local.txt", "svc:data/logs/"},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Src:       copyEndpoint{Raw: "local.txt", Path: "local.txt"},
				Dst:       copyEndpoint{Raw: "svc:data/logs/", Path: "logs", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{
			name: "double dash keeps dash path operand",
			args: []string{"--", "-", "svc:."},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Src:       copyEndpoint{Raw: "-", Path: "-"},
				Dst:       copyEndpoint{Raw: "svc:.", Path: "", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{name: "unknown long flag", args: []string{"--bogus", "a", "svc:b"}, wantErr: "unknown flag"},
		{name: "unknown short flag", args: []string{"-x", "a", "svc:b"}, wantErr: "unknown flag"},
		{name: "wrong operand count", args: []string{"a"}, wantErr: "exactly two paths"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCopyArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCopyArgs: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseCopyArgs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNormalizeRemotePath(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantPath    string
		wantDirHint bool
		wantErr     string
	}{
		{name: "empty path targets data root", raw: "", wantDirHint: true},
		{name: "dot path targets data root", raw: ".", wantDirHint: true},
		{name: "slash suffix records directory hint", raw: "logs/", wantPath: "logs", wantDirHint: true},
		{name: "trims dot slash", raw: "./logs/app.txt", wantPath: "logs/app.txt"},
		{name: "strips data prefix", raw: "data/logs/app.txt", wantPath: "logs/app.txt"},
		{name: "cleans relative path", raw: "logs/../state/app.db", wantPath: "state/app.db"},
		{name: "rejects absolute path", raw: "/etc/passwd", wantErr: "remote path must be relative"},
		{name: "rejects parent escape", raw: "../secret", wantErr: "invalid remote path"},
		{name: "rejects parent escape under data prefix", raw: "data/../secret", wantErr: "invalid remote path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotDirHint, err := normalizeRemotePath(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRemotePath: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotDirHint != tt.wantDirHint {
				t.Fatalf("dirHint = %v, want %v", gotDirHint, tt.wantDirHint)
			}
		})
	}
}
