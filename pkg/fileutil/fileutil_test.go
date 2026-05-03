// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fileutil

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestCopyFileCopiesContentsAndMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("dst contents = %q, want %q", string(got), "payload")
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("dst mode = %v, want %v", st.Mode().Perm(), os.FileMode(0o640))
	}
}

func TestCopyFileMissingSourceIsNoop(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.txt")

	if err := CopyFile(filepath.Join(dir, "missing.txt"), dst); err != nil {
		t.Fatalf("CopyFile missing source: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("expected no destination file, stat error: %v", err)
	}
}

func TestCopyFileReportsRenameFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	dst := filepath.Join(dir, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}

	err := CopyFile(src, dst)
	if err == nil {
		t.Fatalf("expected CopyFile to report rename failure")
	}
	if _, statErr := os.Stat(dst + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("expected temp file cleanup after failure, stat error: %v", statErr)
	}
}

func TestCopyFileReportsTempCreateFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	dst := filepath.Join(dir, "missing-parent", "dst.txt")
	if err := CopyFile(src, dst); err == nil {
		t.Fatal("CopyFile succeeded with missing destination parent")
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected no temp file after create failure, stat error: %v", err)
	}
}

func TestIdentical(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "left.txt")
	right := filepath.Join(dir, "right.txt")
	if err := os.WriteFile(left, []byte("same"), 0o644); err != nil {
		t.Fatalf("write left: %v", err)
	}
	if err := os.WriteFile(right, []byte("same"), 0o644); err != nil {
		t.Fatalf("write right: %v", err)
	}

	same, err := Identical(left, right)
	if err != nil {
		t.Fatalf("Identical same files: %v", err)
	}
	if !same {
		t.Fatalf("expected files to be identical")
	}

	if err := os.WriteFile(right, []byte("different"), 0o644); err != nil {
		t.Fatalf("rewrite right: %v", err)
	}
	same, err = Identical(left, right)
	if err != nil {
		t.Fatalf("Identical different files: %v", err)
	}
	if same {
		t.Fatalf("expected files to differ")
	}
}

func TestIdenticalMissingFileIsFalse(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "left.txt")
	if err := os.WriteFile(left, []byte("same"), 0o644); err != nil {
		t.Fatalf("write left: %v", err)
	}

	same, err := Identical(left, filepath.Join(dir, "missing.txt"))
	if err != nil {
		t.Fatalf("Identical missing file: %v", err)
	}
	if same {
		t.Fatalf("expected missing file to be non-identical")
	}
}

func TestIdenticalReportsReadErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("same"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	if _, err := Identical(subdir, file); err == nil {
		t.Fatal("Identical succeeded when file1 was a directory")
	}
	if _, err := Identical(file, subdir); err == nil {
		t.Fatal("Identical succeeded when file2 was a directory")
	}
}

func TestVersionHelpers(t *testing.T) {
	versionRe := regexp.MustCompile(`^\d{14}$`)
	if got := Version(); !versionRe.MatchString(got) {
		t.Fatalf("Version() = %q, want 14 digits", got)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "hyphen date", in: "app-20230405", want: "app"},
		{name: "dot date with extension", in: "app.20230405.service", want: "app.service"},
		{name: "semantic-ish suffix", in: "app-1.2", want: "app"},
		{name: "no version", in: "app", want: "app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemoveVersion(tt.in); got != tt.want {
				t.Fatalf("RemoveVersion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	updated := UpdateVersion(filepath.Join("dir", "app-20230405.service"))
	updateRe := regexp.MustCompile(`^dir/app-\d{14}\.service$`)
	if !updateRe.MatchString(filepath.ToSlash(updated)) {
		t.Fatalf("UpdateVersion returned %q, want dir/app-<version>.service", updated)
	}

	applied := ApplyVersion(filepath.Join("dir", "app.service"))
	applyRe := regexp.MustCompile(`^dir/app-\d{14}\.service$`)
	if !applyRe.MatchString(filepath.ToSlash(applied)) {
		t.Fatalf("ApplyVersion returned %q, want dir/app-<version>.service", applied)
	}
}
