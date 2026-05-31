// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package codecutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
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
	if !strings.Contains(err.Error(), "failed to open source file") {
		t.Fatalf("ZstdCompress error = %v, want source open context", err)
	}
}

func TestZstdCompressCreateDestinationFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	err := ZstdCompress(src, filepath.Join(dir, "missing", "out.zst"))
	if err == nil {
		t.Fatalf("expected destination create error")
	}
	if !strings.Contains(err.Error(), "failed to create destination file") {
		t.Fatalf("ZstdCompress error = %v, want destination create context", err)
	}
}

func TestZstdCompressDirectorySourceReportsCopyFailure(t *testing.T) {
	dir := t.TempDir()

	err := ZstdCompress(dir, filepath.Join(dir, "out.zst"))
	if err == nil {
		t.Fatalf("expected directory source copy error")
	}
	if !strings.Contains(err.Error(), "failed to compress file") {
		t.Fatalf("ZstdCompress error = %v, want copy context", err)
	}
}

func TestZstdCompressReportsEncoderCreateFailure(t *testing.T) {
	oldNewWriter := newZstdWriter
	defer func() { newZstdWriter = oldNewWriter }()
	wantErr := errors.New("encoder failed")
	newZstdWriter = func(io.Writer, ...zstd.EOption) (*zstd.Encoder, error) {
		return nil, wantErr
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	err := ZstdCompress(src, filepath.Join(dir, "out.zst"))
	if err == nil {
		t.Fatalf("expected encoder create error")
	}
	if !strings.Contains(err.Error(), "failed to create zstd encoder") || !errors.Is(err, wantErr) {
		t.Fatalf("ZstdCompress error = %v, want encoder context", err)
	}
}

func TestZstdDecompressMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := ZstdDecompress(filepath.Join(dir, "missing.zst"), filepath.Join(dir, "out.txt"))
	if err == nil {
		t.Fatalf("expected missing source error")
	}
	if !strings.Contains(err.Error(), "failed to open source file") {
		t.Fatalf("ZstdDecompress error = %v, want source open context", err)
	}
}

func TestZstdDecompressReportsDecoderCreateFailure(t *testing.T) {
	oldNewReader := newZstdReader
	defer func() { newZstdReader = oldNewReader }()
	wantErr := errors.New("decoder failed")
	newZstdReader = func(io.Reader, ...zstd.DOption) (*zstd.Decoder, error) {
		return nil, wantErr
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.zst")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	err := ZstdDecompress(src, filepath.Join(dir, "out.txt"))
	if err == nil {
		t.Fatalf("expected decoder create error")
	}
	if !strings.Contains(err.Error(), "failed to create zstd decoder") || !errors.Is(err, wantErr) {
		t.Fatalf("ZstdDecompress error = %v, want decoder context", err)
	}
}

func TestZstdDecompressReportsResetFailure(t *testing.T) {
	oldReset := resetZstdDecoder
	defer func() { resetZstdDecoder = oldReset }()
	wantErr := errors.New("reset failed")
	resetZstdDecoder = func(*zstd.Decoder, io.Reader) error {
		return wantErr
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.zst")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	err := ZstdDecompress(src, filepath.Join(dir, "out.txt"))
	if err == nil {
		t.Fatalf("expected reset error")
	}
	if !strings.Contains(err.Error(), "failed to reset decoder") || !errors.Is(err, wantErr) {
		t.Fatalf("ZstdDecompress error = %v, want reset context", err)
	}
}

func TestZstdDecompressCreateDestinationFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt")
	compressed := filepath.Join(dir, "payload.zst")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := ZstdCompress(src, compressed); err != nil {
		t.Fatalf("ZstdCompress: %v", err)
	}

	err := ZstdDecompress(compressed, filepath.Join(dir, "missing", "out.txt"))
	if err == nil {
		t.Fatalf("expected destination create error")
	}
	if !strings.Contains(err.Error(), "failed to create destination file") {
		t.Fatalf("ZstdDecompress error = %v, want destination create context", err)
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
	if !strings.Contains(err.Error(), "failed to decompress file") {
		t.Fatalf("ZstdDecompress error = %v, want decompress context", err)
	}
}

func TestCaptureCloseCapturesCloseError(t *testing.T) {
	wantErr := errors.New("close failed")
	var err error

	captureClose(errCloser{err: wantErr}, &err)

	if !errors.Is(err, wantErr) {
		t.Fatalf("captured error = %v, want %v", err, wantErr)
	}
}

func TestCaptureClosePreservesEarlierError(t *testing.T) {
	earlier := errors.New("earlier")
	closeErr := errors.New("close failed")
	err := earlier

	captureClose(errCloser{err: closeErr}, &err)

	if !errors.Is(err, earlier) {
		t.Fatalf("captured error = %v, want earlier error", err)
	}
}

type errCloser struct {
	err error
}

func (c errCloser) Close() error {
	return c.err
}
