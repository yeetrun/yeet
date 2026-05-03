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

func TestWriteHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHeader(&buf, "file", "dir/file.txt"); err != nil {
		t.Fatalf("WriteHeader returned error: %v", err)
	}
	kind, base, err := ReadHeader(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("ReadHeader returned error: %v", err)
	}
	if kind != "file" || base != "dir/file.txt" {
		t.Fatalf("header = (%q, %q), want file dir/file.txt", kind, base)
	}

	buf.Reset()
	if err := WriteHeader(&buf, "dir", ""); err != nil {
		t.Fatalf("WriteHeader empty base returned error: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, " -\n") {
		t.Fatalf("empty base header = %q, want dash marker", got)
	}
	if err := WriteHeader(io.Discard, "", "base"); err == nil {
		t.Fatalf("WriteHeader succeeded with empty kind")
	}
}

func TestTarDirectoryRejectsNonDirectory(t *testing.T) {
	src := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := TarDirectory(io.Discard, src, ""); err == nil || !strings.Contains(err.Error(), "expected directory") {
		t.Fatalf("TarDirectory file error = %v, want expected directory", err)
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

func TestTarFileDefaultsNameAndNotifiesObserver(t *testing.T) {
	src := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o640); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	var entries []TarEntry
	var buf bytes.Buffer
	if err := TarFileWithObserver(&buf, src, "", func(entry TarEntry) {
		entries = append(entries, entry)
	}); err != nil {
		t.Fatalf("TarFileWithObserver returned error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "file.txt" || entries[0].Size != int64(len("hello")) {
		t.Fatalf("observer entries = %#v, want file.txt metadata", entries)
	}

	dest := t.TempDir()
	if err := ExtractTar(&buf, dest); err != nil {
		t.Fatalf("ExtractTar returned error: %v", err)
	}
	assertFileContents(t, filepath.Join(dest, "file.txt"), "hello")
}

func TestTarFileRejectsDirectoriesAndMissingFiles(t *testing.T) {
	if err := TarFile(io.Discard, t.TempDir(), "dir"); err == nil || !strings.Contains(err.Error(), "expected file") {
		t.Fatalf("TarFile directory error = %v, want expected file", err)
	}
	if err := TarFile(io.Discard, filepath.Join(t.TempDir(), "missing"), "missing"); err == nil {
		t.Fatalf("TarFile succeeded for missing source")
	}
}

func TestExtractTarSkipsEmptyEntriesAndRejectsUnsupportedTypes(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarEntry(t, tw, &tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0o755}, nil)
	writeTarEntry(t, tw, &tar.Header{Name: "unsupported", Typeflag: tar.TypeXGlobalHeader}, nil)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	err := ExtractTar(&buf, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsupported tar entry") {
		t.Fatalf("ExtractTar unsupported error = %v, want unsupported tar entry", err)
	}
}

func TestExtractTarExtractsHardlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hardlink metadata varies on windows")
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarEntry(t, tw, &tar.Header{Name: "file.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len("hello"))}, []byte("hello"))
	writeTarEntry(t, tw, &tar.Header{Name: "hardlink.txt", Typeflag: tar.TypeLink, Linkname: "file.txt", Mode: 0o644}, nil)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	dest := t.TempDir()
	if err := ExtractTar(&buf, dest); err != nil {
		t.Fatalf("ExtractTar returned error: %v", err)
	}
	assertFileContents(t, filepath.Join(dest, "hardlink.txt"), "hello")
	fileInfo, err := os.Stat(filepath.Join(dest, "file.txt"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	linkInfo, err := os.Stat(filepath.Join(dest, "hardlink.txt"))
	if err != nil {
		t.Fatalf("stat hardlink: %v", err)
	}
	if !os.SameFile(fileInfo, linkInfo) {
		t.Fatalf("hardlink.txt is not the same file as file.txt")
	}
}

func TestExtractTarExtractsFIFO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("special files are unsupported on windows")
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarEntry(t, tw, &tar.Header{Name: "fifo", Typeflag: tar.TypeFifo, Mode: 0o600}, nil)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	dest := t.TempDir()
	if err := ExtractTar(&buf, dest); err != nil {
		t.Fatalf("ExtractTar FIFO returned error: %v", err)
	}
	info, err := os.Lstat(filepath.Join(dest, "fifo"))
	if err != nil {
		t.Fatalf("lstat fifo: %v", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("fifo mode = %v, want named pipe", info.Mode())
	}
}

func TestCreateSpecialRejectsUnsupportedType(t *testing.T) {
	err := createSpecial(filepath.Join(t.TempDir(), "regular"), &tar.Header{Typeflag: tar.TypeReg})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("createSpecial unsupported error = %v, want unsupported", err)
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

func TestMoveTreeNoopsAndRenamesWhenDestinationMissing(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := MoveTree("", filepath.Join(root, "ignored")); err != nil {
		t.Fatalf("MoveTree empty src: %v", err)
	}
	if err := MoveTree(src, src); err != nil {
		t.Fatalf("MoveTree same path: %v", err)
	}

	dst := filepath.Join(root, "dst")
	if err := MoveTree(src, dst); err != nil {
		t.Fatalf("MoveTree rename path: %v", err)
	}
	assertFileContents(t, filepath.Join(dst, "file.txt"), "hello")
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source after rename stat error = %v, want missing", err)
	}
}

func TestMoveTreeReplacesNestedFileDestinationWithDirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	nestedSrc := filepath.Join(src, "nested")
	if err := os.MkdirAll(nestedSrc, 0o755); err != nil {
		t.Fatalf("mkdir nested src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedSrc, "file.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	dst := filepath.Join(root, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "nested"), []byte("old file"), 0o644); err != nil {
		t.Fatalf("write nested destination file: %v", err)
	}

	if err := MoveTree(src, dst); err != nil {
		t.Fatalf("MoveTree returned error: %v", err)
	}
	assertFileContents(t, filepath.Join(dst, "nested", "file.txt"), "new")
}

func TestMoveTreeReplacesConflictingFile(t *testing.T) {
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "same.txt"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "same.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	if err := MoveTree(stage, dest); err != nil {
		t.Fatalf("MoveTree returned error: %v", err)
	}
	assertFileContents(t, filepath.Join(dest, "same.txt"), "new")
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
