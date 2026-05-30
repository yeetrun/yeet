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
	if err := os.WriteFile(filepath.Join(root, ".envrc"), []byte("export A=B\n"), 0o644); err != nil {
		t.Fatalf("write envrc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "prod.env.local"), []byte("A=B\n"), 0o644); err != nil {
		t.Fatalf("write env local: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got, err := listRunWebFiles(root, ".")
	if err != nil {
		t.Fatalf("listRunWebFiles: %v", err)
	}
	if len(got.Entries) != 5 {
		t.Fatalf("entries = %#v, want 5", got.Entries)
	}
	compose := got.entry("compose.yml")
	if compose == nil || !compose.LikelyPayload {
		t.Fatalf("compose entry = %#v, want likely payload", compose)
	}
	env := got.entry(".env")
	if env == nil || !env.LikelyEnv {
		t.Fatalf("env entry = %#v, want likely env", env)
	}
	envrc := got.entry(".envrc")
	if envrc == nil || !envrc.LikelyEnv {
		t.Fatalf("envrc entry = %#v, want likely env", envrc)
	}
	envLocal := got.entry("prod.env.local")
	if envLocal == nil || !envLocal.LikelyEnv {
		t.Fatalf("prod.env.local entry = %#v, want likely env", envLocal)
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

func TestListRunWebFilesRejectsListedSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	got, err := listRunWebFiles(root, ".")
	if err != nil {
		t.Fatalf("listRunWebFiles: %v", err)
	}
	if got.entry("outside") != nil {
		t.Fatalf("outside entry = %#v, want omitted", got.entry("outside"))
	}
	if got.entry("compose.yml") == nil {
		t.Fatalf("compose.yml entry missing from %#v", got.Entries)
	}
}

func TestListRunWebFilesOmitsBrokenSymlinkEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	got, err := listRunWebFiles(root, ".")
	if err != nil {
		t.Fatalf("listRunWebFiles: %v", err)
	}
	if got.entry("broken") != nil {
		t.Fatalf("broken entry = %#v, want omitted", got.entry("broken"))
	}
	if got.entry("compose.yml") == nil {
		t.Fatalf("compose.yml entry missing from %#v", got.Entries)
	}
}

func TestListRunWebFilesClassifiesSafeSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real-dir"), 0o755); err != nil {
		t.Fatalf("mkdir real-dir: %v", err)
	}
	script := filepath.Join(root, "real-script")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "real-dir"), filepath.Join(root, "dir-link")); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	if err := os.Symlink(script, filepath.Join(root, "script-link")); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	got, err := listRunWebFiles(root, ".")
	if err != nil {
		t.Fatalf("listRunWebFiles: %v", err)
	}
	dirLink := got.entry("dir-link")
	if dirLink == nil || !dirLink.Dir {
		t.Fatalf("dir-link entry = %#v, want directory", dirLink)
	}
	scriptLink := got.entry("script-link")
	if scriptLink == nil || !scriptLink.LikelyPayload {
		t.Fatalf("script-link entry = %#v, want likely payload", scriptLink)
	}
}

func TestRunWebPathInsideRootAllowsFilesystemRootChildren(t *testing.T) {
	if !runWebPathInsideRoot(string(filepath.Separator), filepath.Join(string(filepath.Separator), "tmp")) {
		t.Fatal("runWebPathInsideRoot rejected child of filesystem root")
	}
}
