// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package compress

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestSelectEncoding(t *testing.T) {
	tests := []struct {
		name           string
		acceptEncoding string
		want           string
	}{
		{
			name:           "empty",
			acceptEncoding: "",
			want:           "",
		},
		{
			name:           "gzip only",
			acceptEncoding: "gzip",
			want:           "gzip",
		},
		{
			name:           "deflate only",
			acceptEncoding: "deflate",
			want:           "deflate",
		},
		{
			name:           "zstd only",
			acceptEncoding: "zstd",
			want:           "zstd",
		},
		{
			name:           "prefer zstd over gzip",
			acceptEncoding: "gzip, zstd",
			want:           "zstd",
		},
		{
			name:           "prefer zstd over deflate",
			acceptEncoding: "deflate, zstd",
			want:           "zstd",
		},
		{
			name:           "prefer gzip over deflate",
			acceptEncoding: "deflate, gzip",
			want:           "gzip",
		},
		{
			name:           "all three - prefer zstd",
			acceptEncoding: "gzip, deflate, zstd",
			want:           "zstd",
		},
		{
			name:           "with quality values - prefer higher quality",
			acceptEncoding: "gzip;q=0.5, zstd;q=0.9",
			want:           "zstd",
		},
		{
			name:           "with quality values - gzip higher",
			acceptEncoding: "gzip;q=0.9, zstd;q=0.5",
			want:           "gzip",
		},
		{
			name:           "unsupported encoding",
			acceptEncoding: "br",
			want:           "",
		},
		{
			name:           "wildcard includes all",
			acceptEncoding: "*",
			want:           "zstd", // Prefer zstd when wildcard is used
		},
		{
			name:           "zero quality should not be selected",
			acceptEncoding: "gzip;q=0",
			want:           "",
		},
		{
			name:           "identity is not supported",
			acceptEncoding: "identity",
			want:           "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectEncoding(tt.acceptEncoding)
			if got != tt.want {
				t.Errorf("SelectEncoding(%q) = %q, want %q", tt.acceptEncoding, got, tt.want)
			}
		})
	}
}

func TestResponseWriter_Gzip(t *testing.T) {
	testData := []byte(strings.Repeat("test data for compression ", 100))

	// Create a mock response writer
	w := httptest.NewRecorder()

	// Create compressed writer
	cw, err := NewResponseWriter(w, "gzip")
	if err != nil {
		t.Fatalf("NewResponseWriter failed: %v", err)
	}

	// Write data
	n, err := cw.Write(testData)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Write returned %d, want %d", n, len(testData))
	}

	// Close to flush compression
	if err := cw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Check headers
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want %q", got, "gzip")
	}
	if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want %q", got, "Accept-Encoding")
	}
	if got := w.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length should be removed, got %q", got)
	}

	// Decompress and verify
	gr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	defer gr.Close()

	decompressed, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(decompressed, testData) {
		t.Errorf("decompressed data doesn't match original")
	}
}

func TestResponseWriter_Deflate(t *testing.T) {
	testData := []byte(strings.Repeat("test data for deflate compression ", 100))

	w := httptest.NewRecorder()

	cw, err := NewResponseWriter(w, "deflate")
	if err != nil {
		t.Fatalf("NewResponseWriter failed: %v", err)
	}

	if _, err := cw.Write(testData); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := cw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Check headers
	if got := w.Header().Get("Content-Encoding"); got != "deflate" {
		t.Errorf("Content-Encoding = %q, want %q", got, "deflate")
	}

	// Decompress and verify
	dr := flate.NewReader(w.Body)
	defer dr.Close()

	decompressed, err := io.ReadAll(dr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(decompressed, testData) {
		t.Errorf("decompressed data doesn't match original")
	}
}

func TestResponseWriter_Zstd(t *testing.T) {
	testData := []byte(strings.Repeat("test data for zstd compression ", 100))

	w := httptest.NewRecorder()

	cw, err := NewResponseWriter(w, "zstd")
	if err != nil {
		t.Fatalf("NewResponseWriter failed: %v", err)
	}

	if _, err := cw.Write(testData); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := cw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Check headers
	if got := w.Header().Get("Content-Encoding"); got != "zstd" {
		t.Errorf("Content-Encoding = %q, want %q", got, "zstd")
	}

	// Decompress and verify
	zr, err := zstd.NewReader(w.Body)
	if err != nil {
		t.Fatalf("zstd.NewReader failed: %v", err)
	}
	defer zr.Close()

	decompressed, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(decompressed, testData) {
		t.Errorf("decompressed data doesn't match original")
	}
}

func TestResponseWriter_NoCompression(t *testing.T) {
	testData := []byte("test data without compression")

	w := httptest.NewRecorder()

	// Create writer with empty encoding (no compression)
	cw, err := NewResponseWriter(w, "")
	if err != nil {
		t.Fatalf("NewResponseWriter failed: %v", err)
	}

	if _, err := cw.Write(testData); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := cw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Should have no compression headers
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding should be empty, got %q", got)
	}

	// Data should be uncompressed
	if !bytes.Equal(w.Body.Bytes(), testData) {
		t.Errorf("data doesn't match original")
	}
}

func TestDecompressRequest(t *testing.T) {
	testData := []byte("test data for request decompression")

	tests := []struct {
		name     string
		encoding string
		compress func([]byte) ([]byte, error)
	}{
		{
			name:     "gzip",
			encoding: "gzip",
			compress: func(data []byte) ([]byte, error) {
				var buf bytes.Buffer
				w := gzip.NewWriter(&buf)
				if _, err := w.Write(data); err != nil {
					return nil, err
				}
				if err := w.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
		},
		{
			name:     "deflate",
			encoding: "deflate",
			compress: func(data []byte) ([]byte, error) {
				var buf bytes.Buffer
				w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
				if _, err := w.Write(data); err != nil {
					return nil, err
				}
				if err := w.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
		},
		{
			name:     "zstd",
			encoding: "zstd",
			compress: func(data []byte) ([]byte, error) {
				var buf bytes.Buffer
				w, err := zstd.NewWriter(&buf)
				if err != nil {
					return nil, err
				}
				if _, err := w.Write(data); err != nil {
					return nil, err
				}
				if err := w.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Compress the test data
			compressed, err := tt.compress(testData)
			if err != nil {
				t.Fatalf("failed to compress: %v", err)
			}

			// Create request with compressed body
			req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(compressed))
			req.Header.Set("Content-Encoding", tt.encoding)

			// Decompress the request
			if err := DecompressRequest(req); err != nil {
				t.Fatalf("DecompressRequest failed: %v", err)
			}

			// Verify the decompressed data
			decompressed, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("failed to read decompressed body: %v", err)
			}

			if !bytes.Equal(decompressed, testData) {
				t.Errorf("decompressed data = %q, want %q", string(decompressed), string(testData))
			}

			// Verify Content-Encoding header was removed
			if got := req.Header.Get("Content-Encoding"); got != "" {
				t.Errorf("Content-Encoding should be removed, got %q", got)
			}
		})
	}
}

func TestDecompressRequest_NoEncoding(t *testing.T) {
	testData := []byte("uncompressed data")
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(testData))

	if err := DecompressRequest(req); err != nil {
		t.Fatalf("DecompressRequest failed: %v", err)
	}

	// Data should remain unchanged
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if !bytes.Equal(body, testData) {
		t.Errorf("body = %q, want %q", string(body), string(testData))
	}
}
