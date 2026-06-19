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

func TestSearchRunWebFilesRanksRecursivePayloadMatches(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "apps", "searxng"),
		filepath.Join(root, "apps", "searxng-notes"),
		filepath.Join(root, "tools"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	files := map[string]os.FileMode{
		"apps/searxng/docker-compose.yml":      0o644,
		"apps/searxng-notes/compose-notes.txt": 0o644,
		"tools/searxng-compose-helper":         0o755,
		"compose.yml":                          0o644,
	}
	for name, mode := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("ok\n"), mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got, err := searchRunWebFiles(root, "sx compose", "payload")
	if err != nil {
		t.Fatalf("searchRunWebFiles: %v", err)
	}
	if len(got.Entries) < 2 {
		t.Fatalf("entries = %#v, want multiple ranked matches", got.Entries)
	}
	if got.Entries[0].Path != "apps/searxng/docker-compose.yml" {
		t.Fatalf("first path = %q, want nested compose payload; entries=%#v", got.Entries[0].Path, got.Entries)
	}
	if !got.Entries[0].LikelyPayload || got.Entries[0].Dir {
		t.Fatalf("first entry = %#v, want file marked as likely payload", got.Entries[0])
	}
	for _, entry := range got.Entries {
		if entry.Dir {
			t.Fatalf("search entry = %#v, want files only", entry)
		}
	}
}

func TestSearchRunWebFilesFindsEnvFilesWhenRequested(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "searxng"), 0o755); err != nil {
		t.Fatalf("mkdir app: %v", err)
	}
	for _, name := range []string{
		"apps/searxng/prod.env",
		"apps/searxng/docker-compose.yml",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("ok\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got, err := searchRunWebFiles(root, "searx env", "envFile")
	if err != nil {
		t.Fatalf("searchRunWebFiles: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "apps/searxng/prod.env" || !got.Entries[0].LikelyEnv {
		t.Fatalf("entries = %#v, want env file match only", got.Entries)
	}
}

func TestSearchRunWebFilesOmitsSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write outside compose: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	got, err := searchRunWebFiles(root, "secret compose", "payload")
	if err != nil {
		t.Fatalf("searchRunWebFiles: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("entries = %#v, want symlink escape omitted", got.Entries)
	}
}
