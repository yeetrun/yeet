// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package copyutil

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReadHeader(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKind  string
		wantBase  string
		wantError bool
	}{
		{name: "empty base", input: "YEETCOPY1 file -\n", wantKind: "file"},
		{name: "encoded base", input: "YEETCOPY1 dir ZGlyL25hbWU=\n", wantKind: "dir", wantBase: "dir/name"},
		{name: "surrounding whitespace", input: "  YEETCOPY1 file ZmlsZS50eHQ=  \n", wantKind: "file", wantBase: "file.txt"},
		{name: "bad prefix", input: "BAD file -\n", wantError: true},
		{name: "missing field", input: "YEETCOPY1 file\n", wantError: true},
		{name: "bad base64", input: "YEETCOPY1 file ???\n", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKind, gotBase, err := ReadHeader(bufio.NewReader(strings.NewReader(tt.input)))
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadHeader: %v", err)
			}
			if gotKind != tt.wantKind || gotBase != tt.wantBase {
				t.Fatalf("ReadHeader = (%q, %q), want (%q, %q)", gotKind, gotBase, tt.wantKind, tt.wantBase)
			}
		})
	}
}

func TestExtractTarRejectsDangerousPaths(t *testing.T) {
	tests := []struct {
		name      string
		entryName string
		linkName  string
		typeflag  byte
	}{
		{name: "parent traversal", entryName: "../escape.txt", typeflag: tar.TypeReg},
		{name: "absolute path", entryName: "/escape.txt", typeflag: tar.TypeReg},
		{name: "hardlink traversal", entryName: "safe-link", linkName: "../escape.txt", typeflag: tar.TypeLink},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{
				Name:     tt.entryName,
				Linkname: tt.linkName,
				Typeflag: tt.typeflag,
				Mode:     0o644,
			}); err != nil {
				t.Fatalf("failed to write tar header: %v", err)
			}
			if err := tw.Close(); err != nil {
				t.Fatalf("failed to close tar: %v", err)
			}

			if err := ExtractTar(&buf, t.TempDir()); err == nil {
				t.Fatalf("expected dangerous tar path to fail")
			}
		})
	}
}

func TestExtractTarExtractsEntriesAndNotifiesObserver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink extraction is platform dependent on windows")
	}

	modTime := time.Unix(1700000300, 0)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarEntry(t, tw, &tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o750, ModTime: modTime}, nil)
	writeTarEntry(t, tw, &tar.Header{Name: "dir/file.txt", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len("hello")), ModTime: modTime}, []byte("hello"))
	writeTarEntry(t, tw, &tar.Header{Name: "link.txt", Typeflag: tar.TypeSymlink, Linkname: "dir/file.txt", Mode: 0o777, ModTime: modTime}, nil)
	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar: %v", err)
	}

	var entries []TarEntry
	dest := t.TempDir()
	err := ExtractTarWithOptions(&buf, dest, ExtractOptions{
		OnEntry: func(entry TarEntry) {
			entries = append(entries, entry)
		},
	})
	if err != nil {
		t.Fatalf("failed to extract tar: %v", err)
	}

	assertFileContents(t, filepath.Join(dest, "dir", "file.txt"), "hello")
	if st, err := os.Stat(filepath.Join(dest, "dir")); err != nil {
		t.Fatalf("failed to stat extracted dir: %v", err)
	} else if st.Mode().Perm() != 0o750 {
		t.Fatalf("expected dir mode 0750, got %o", st.Mode().Perm())
	}
	target, err := os.Readlink(filepath.Join(dest, "link.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted symlink: %v", err)
	}
	if target != "dir/file.txt" {
		t.Fatalf("expected symlink target dir/file.txt, got %q", target)
	}

	wantNames := []string{"dir", "dir/file.txt", "link.txt"}
	if len(entries) != len(wantNames) {
		t.Fatalf("expected %d observer entries, got %d: %#v", len(wantNames), len(entries), entries)
	}
	for i, want := range wantNames {
		if entries[i].Name != want {
			t.Fatalf("observer entry %d: expected %q, got %q", i, want, entries[i].Name)
		}
	}
	if entries[1].Size != int64(len("hello")) || entries[2].Linkname != "dir/file.txt" {
		t.Fatalf("observer entries did not include expected metadata: %#v", entries)
	}
}

func TestTarFileWithObserverReturnsCloseError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	wantErr := errors.New("close failed")
	err := TarFileWithObserver(&failAfterWriter{failAfter: 1024, err: wantErr}, src, "file.txt", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected close error %v, got %v", wantErr, err)
	}
}

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

func writeTarEntry(t *testing.T, tw *tar.Writer, hdr *tar.Header, body []byte) {
	t.Helper()
	if hdr.Typeflag == tar.TypeReg && hdr.Size == 0 {
		hdr.Size = int64(len(body))
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write tar header %q: %v", hdr.Name, err)
	}
	if len(body) == 0 {
		return
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("failed to write tar body %q: %v", hdr.Name, err)
	}
}

func assertFileContents(t *testing.T, filePath, want string) {
	t.Helper()
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", filePath, err)
	}
	if string(got) != want {
		t.Fatalf("expected %s contents %q, got %q", filePath, want, string(got))
	}
}

type failAfterWriter struct {
	written   int
	failAfter int
	err       error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.written >= w.failAfter {
		return 0, w.err
	}
	remaining := w.failAfter - w.written
	if len(p) > remaining {
		w.written += remaining
		return remaining, io.ErrShortWrite
	}
	w.written += len(p)
	return len(p), nil
}
