// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"strings"
	"testing"
)

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
