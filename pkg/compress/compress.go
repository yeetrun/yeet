// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package compress provides HTTP request and response compression/decompression
// for various encoding formats including zstd, gzip, and deflate.
package compress

import (
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ResponseWriter wraps an http.ResponseWriter to provide transparent compression.
// It handles setting appropriate headers (Content-Encoding, Vary) and removes
// Content-Length since the compressed size differs from the original.
type ResponseWriter struct {
	http.ResponseWriter
	writer        io.Writer
	encoding      string
	headerWritten bool
	wroteHeader   bool
}

// Write compresses data and writes it to the underlying response writer.
func (cw *ResponseWriter) Write(data []byte) (int, error) {
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	if !cw.headerWritten {
		cw.writeCompressionHeaders()
		cw.headerWritten = true
	}
	return cw.writer.Write(data)
}

// WriteHeader writes the status code and compression headers if needed.
func (cw *ResponseWriter) WriteHeader(code int) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	if cw.encoding != "" {
		cw.writeCompressionHeaders()
		cw.headerWritten = true
	}
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *ResponseWriter) writeCompressionHeaders() {
	if cw.encoding == "" {
		return
	}

	// Set Content-Encoding header
	cw.ResponseWriter.Header().Set("Content-Encoding", cw.encoding)

	// Remove Content-Length since compressed size will differ
	// The compressed content will be sent with chunked transfer encoding
	cw.ResponseWriter.Header().Del("Content-Length")

	// Add Vary header to indicate response varies based on Accept-Encoding
	cw.ResponseWriter.Header().Set("Vary", "Accept-Encoding")
}

// Close flushes and closes the compression writer.
func (cw *ResponseWriter) Close() error {
	if closer, ok := cw.writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// SelectEncoding chooses the best compression encoding based on client preferences.
// It parses the Accept-Encoding header and selects the most appropriate algorithm
// based on quality values and the preference order (zstd > gzip > deflate).
// Returns the encoding name or empty string if no compression should be used.
func SelectEncoding(acceptEncoding string) string {
	if acceptEncoding == "" {
		return ""
	}

	// Parse Accept-Encoding header
	// Format: "gzip, deflate, br;q=0.9, *;q=0.8"
	encodings := strings.Split(acceptEncoding, ",")

	// Track supported encodings with their quality values
	supported := make(map[string]float32)

	for _, enc := range encodings {
		enc = strings.TrimSpace(enc)

		// Split on ';' to separate encoding from quality
		parts := strings.Split(enc, ";")
		name := strings.TrimSpace(parts[0])
		quality := float32(1.0)

		// Parse quality value if present
		if len(parts) > 1 {
			qPart := strings.TrimSpace(parts[1])
			if strings.HasPrefix(qPart, "q=") {
				var q float32
				if _, err := fmt.Sscanf(qPart, "q=%f", &q); err == nil {
					quality = q
				}
			}
		}

		// Store supported encodings
		switch name {
		case "zstd":
			supported["zstd"] = quality
		case "gzip":
			supported["gzip"] = quality
		case "deflate":
			supported["deflate"] = quality
		case "*":
			// Wildcard - support all if not explicitly listed
			if _, ok := supported["zstd"]; !ok {
				supported["zstd"] = quality
			}
			if _, ok := supported["gzip"]; !ok {
				supported["gzip"] = quality
			}
			if _, ok := supported["deflate"]; !ok {
				supported["deflate"] = quality
			}
		}
	}

	// Select best encoding (prefer zstd > gzip > deflate for better compression)
	bestQuality := float32(0)
	bestEncoding := ""

	// Check in order of preference
	if q, ok := supported["zstd"]; ok && q > bestQuality {
		bestQuality = q
		bestEncoding = "zstd"
	}
	if q, ok := supported["gzip"]; ok && q > bestQuality {
		bestQuality = q
		bestEncoding = "gzip"
	}
	if q, ok := supported["deflate"]; ok && q > bestQuality {
		bestQuality = q
		bestEncoding = "deflate"
	}

	// Only use compression if quality > 0
	if bestQuality > 0 {
		return bestEncoding
	}

	return ""
}

// NewResponseWriter creates a compression writer based on the selected encoding.
// Supported encodings: zstd, gzip, deflate.
// Returns an error if the compression writer cannot be created.
func NewResponseWriter(w http.ResponseWriter, encoding string) (*ResponseWriter, error) {
	cw := &ResponseWriter{
		ResponseWriter: w,
		encoding:       encoding,
	}

	var err error
	switch encoding {
	case "zstd":
		cw.writer, err = zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest))
	case "gzip":
		cw.writer = gzip.NewWriter(w)
	case "deflate":
		cw.writer, err = flate.NewWriter(w, flate.DefaultCompression)
	default:
		cw.writer = w
		cw.encoding = ""
	}

	if err != nil {
		return nil, err
	}

	return cw, nil
}

// DecompressRequest wraps the request body with a decompressing reader
// if the Content-Encoding header is set.
func DecompressRequest(r *http.Request) error {
	contentEncoding := r.Header.Get("Content-Encoding")
	if contentEncoding == "" {
		return nil
	}

	var reader io.ReadCloser
	var err error

	switch contentEncoding {
	case "gzip":
		reader, err = gzip.NewReader(r.Body)
	case "deflate":
		reader = flate.NewReader(r.Body)
	case "zstd":
		var zr *zstd.Decoder
		zr, err = zstd.NewReader(r.Body)
		if err == nil {
			reader = io.NopCloser(zr.IOReadCloser())
		}
	case "identity":
		// No decompression needed
		return nil
	default:
		// Unsupported encoding - leave body as is
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to create decompressor for %s: %w", contentEncoding, err)
	}

	// Wrap in a ReadCloser that closes both the decompressor and original body
	oldBody := r.Body
	r.Body = &closeWrapper{
		ReadCloser: reader,
		onClose:    oldBody.Close,
	}

	// Remove Content-Encoding header since we've decompressed
	r.Header.Del("Content-Encoding")
	// Remove Content-Length since decompressed size differs
	r.Header.Del("Content-Length")

	return nil
}

// closeWrapper wraps an io.ReadCloser and calls an additional function on Close.
type closeWrapper struct {
	io.ReadCloser
	onClose func() error
}

func (cw *closeWrapper) Close() error {
	err1 := cw.ReadCloser.Close()
	err2 := cw.onClose()
	return errors.Join(err1, err2)
}
