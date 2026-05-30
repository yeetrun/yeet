// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListRunWebFilesListsCwdEntriesAndMarksLikelyPayloads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("A=B\n"), 0o644); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got, err := listRunWebFiles(root, ".")
	if err != nil {
		t.Fatalf("listRunWebFiles: %v", err)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("entries = %#v, want 3", got.Entries)
	}
	compose := got.entry("compose.yml")
	if compose == nil || !compose.LikelyPayload {
		t.Fatalf("compose entry = %#v, want likely payload", compose)
	}
	env := got.entry(".env")
	if env == nil || !env.LikelyEnv {
		t.Fatalf("env entry = %#v, want likely env", env)
	}
}

func TestListRunWebFilesRejectsTraversal(t *testing.T) {
	_, err := listRunWebFiles(t.TempDir(), "..")
	if err == nil {
		t.Fatal("listRunWebFiles traversal succeeded, want error")
	}
}

func TestListRunWebFilesRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	_, err := listRunWebFiles(root, "outside")
	if err == nil {
		t.Fatal("listRunWebFiles symlink escape succeeded, want error")
	}
}
