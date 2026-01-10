// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package copyutil

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestTarDirectoryPreservesMetadata(t *testing.T) {
	src := t.TempDir()
	subdir := filepath.Join(src, "dir")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	filePath := filepath.Join(subdir, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o640); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	modTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(filePath, modTime, modTime); err != nil {
		t.Fatalf("failed to set file times: %v", err)
	}
	if err := os.Chtimes(subdir, modTime, modTime); err != nil {
		t.Fatalf("failed to set dir times: %v", err)
	}
	if runtime.GOOS != "windows" {
		linkPath := filepath.Join(src, "link.txt")
		if err := os.Symlink("dir/file.txt", linkPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
	}

	var buf bytes.Buffer
	if err := TarDirectory(&buf, src, ""); err != nil {
		t.Fatalf("failed to tar directory: %v", err)
	}

	dest := t.TempDir()
	if err := ExtractTar(&buf, dest); err != nil {
		t.Fatalf("failed to extract tar: %v", err)
	}

	outFile := filepath.Join(dest, "dir", "file.txt")
	st, err := os.Stat(outFile)
	if err != nil {
		t.Fatalf("failed to stat extracted file: %v", err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("expected file mode 0640, got %o", st.Mode().Perm())
	}
	if st.ModTime().Unix() != modTime.Unix() {
		t.Fatalf("expected file modtime %v, got %v", modTime, st.ModTime())
	}
	outDir := filepath.Join(dest, "dir")
	dst, err := os.Stat(outDir)
	if err != nil {
		t.Fatalf("failed to stat extracted dir: %v", err)
	}
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("expected dir mode 0700, got %o", dst.Mode().Perm())
	}
	if dst.ModTime().Unix() != modTime.Unix() {
		t.Fatalf("expected dir modtime %v, got %v", modTime, dst.ModTime())
	}

	if runtime.GOOS != "windows" {
		linkOut := filepath.Join(dest, "link.txt")
		if linfo, err := os.Lstat(linkOut); err == nil && linfo.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(linkOut); err != nil || target != "dir/file.txt" {
				t.Fatalf("expected symlink target dir/file.txt, got %q (err=%v)", target, err)
			}
		} else if err != nil {
			t.Fatalf("failed to stat symlink: %v", err)
		}
	}
}

func TestMoveTreeMerges(t *testing.T) {
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("failed to write staged file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "nested"), 0o755); err != nil {
		t.Fatalf("failed to create staged nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "nested", "added.txt"), []byte("added"), 0o644); err != nil {
		t.Fatalf("failed to write staged nested file: %v", err)
	}

	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to write existing file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dest, "nested"), 0o755); err != nil {
		t.Fatalf("failed to create existing nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "nested", "existing.txt"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("failed to write existing nested file: %v", err)
	}

	if err := MoveTree(stage, dest); err != nil {
		t.Fatalf("failed to move tree: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "old.txt")); err != nil {
		t.Fatalf("expected old file to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "new.txt")); err != nil {
		t.Fatalf("expected new file to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "nested", "existing.txt")); err != nil {
		t.Fatalf("expected existing nested file to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "nested", "added.txt")); err != nil {
		t.Fatalf("expected added nested file to exist: %v", err)
	}
	if _, err := os.Stat(stage); err == nil {
		t.Fatalf("expected stage dir to be removed")
	}
}
