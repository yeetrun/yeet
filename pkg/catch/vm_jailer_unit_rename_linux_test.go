// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package catch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExchangeVMJailerUnitNamesAtSwapsBothInodes(t *testing.T) {
	dirPath := t.TempDir()
	dir, err := os.Open(dirPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer func() { _ = dir.Close() }()
	for name, content := range map[string]string{"left": "left-inode", "right": "right-inode"} {
		if err := os.WriteFile(filepath.Join(dirPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if err := exchangeVMJailerUnitNamesAt(int(dir.Fd()), "left", int(dir.Fd()), "right"); err != nil {
		t.Fatalf("exchange names: %v", err)
	}
	assertVMJailerUnitContent(t, filepath.Join(dirPath, "left"), "right-inode")
	assertVMJailerUnitContent(t, filepath.Join(dirPath, "right"), "left-inode")
}

func TestRenameVMJailerUnitNameNoReplaceAtPreservesExistingTarget(t *testing.T) {
	dirPath := t.TempDir()
	dir, err := os.Open(dirPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer func() { _ = dir.Close() }()
	for name, content := range map[string]string{"source": "source-inode", "target": "target-inode"} {
		if err := os.WriteFile(filepath.Join(dirPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	err = renameVMJailerUnitNameNoReplaceAt(int(dir.Fd()), "source", int(dir.Fd()), "target")
	if !errors.Is(err, unix.EEXIST) {
		t.Fatalf("no-replace rename error = %v, want EEXIST", err)
	}
	assertVMJailerUnitContent(t, filepath.Join(dirPath, "source"), "source-inode")
	assertVMJailerUnitContent(t, filepath.Join(dirPath, "target"), "target-inode")
}

func assertVMJailerUnitContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(raw) != want {
		t.Fatalf("%s content = %q, want %q", path, raw, want)
	}
}
