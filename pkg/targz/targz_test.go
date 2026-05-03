// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package targz

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"sort"
	"testing"
)

func TestReadFileVisitsEntries(t *testing.T) {
	var seen []string
	err := ReadFile(tarGzipArchive(t, map[string]string{
		"one.txt": "one",
		"two.txt": "two",
	}), func(header *tar.Header, r io.Reader) error {
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read body for %s: %v", header.Name, err)
		}
		seen = append(seen, header.Name+"="+string(body))
		return nil
	})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := []string{"one.txt=one", "two.txt=two"}
	if len(seen) != len(want) {
		t.Fatalf("seen count = %d, want %d: %#v", len(seen), len(want), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestReadFilePropagatesCallbackError(t *testing.T) {
	wantErr := errors.New("stop")
	err := ReadFile(tarGzipArchive(t, map[string]string{"one.txt": "one"}), func(*tar.Header, io.Reader) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReadFile error = %v, want %v", err, wantErr)
	}
}

func TestReadFileRejectsInvalidGzip(t *testing.T) {
	err := ReadFile(bytes.NewReader([]byte("not gzip")), func(*tar.Header, io.Reader) error {
		t.Fatalf("callback should not be called for invalid gzip")
		return nil
	})
	if err == nil {
		t.Fatalf("expected invalid gzip error")
	}
}

func tarGzipArchive(t *testing.T, files map[string]string) io.Reader {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		header := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}
