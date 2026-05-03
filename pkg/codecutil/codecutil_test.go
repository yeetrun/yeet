// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package codecutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZstdRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt")
	compressed := filepath.Join(dir, "payload.zst")
	decompressed := filepath.Join(dir, "payload.out")
	want := strings.Repeat("payload\n", 32)
	if err := os.WriteFile(src, []byte(want), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := ZstdCompress(src, compressed); err != nil {
		t.Fatalf("ZstdCompress: %v", err)
	}
	if err := ZstdDecompress(compressed, decompressed); err != nil {
		t.Fatalf("ZstdDecompress: %v", err)
	}

	got, err := os.ReadFile(decompressed)
	if err != nil {
		t.Fatalf("read decompressed: %v", err)
	}
	if string(got) != want {
		t.Fatalf("decompressed payload = %q, want %q", string(got), want)
	}
}

func TestZstdCompressMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := ZstdCompress(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "out.zst"))
	if err == nil {
		t.Fatalf("expected missing source error")
	}
}

func TestZstdDecompressRejectsInvalidInput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "invalid.zst")
	dst := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(src, []byte("not zstd"), 0o644); err != nil {
		t.Fatalf("write invalid source: %v", err)
	}

	err := ZstdDecompress(src, dst)
	if err == nil {
		t.Fatalf("expected invalid zstd error")
	}
}
