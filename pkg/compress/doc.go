// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package compress provides HTTP request and response compression/decompression
// middleware and utilities for various encoding formats.
//
// # Supported Encodings
//
// The package supports three compression algorithms with automatic content
// negotiation:
//
//   - zstd (Zstandard) - Modern compression with excellent ratio and speed
//   - gzip - Widely supported, good general-purpose compression
//   - deflate - Basic compression with broad compatibility
//
// # Response Compression
//
// For compressing HTTP responses (downloads), use ResponseWriter:
//
//	acceptEncoding := r.Header.Get("Accept-Encoding")
//	encoding := compress.SelectEncoding(acceptEncoding)
//
//	if encoding != "" {
//	    w, err := compress.NewResponseWriter(responseWriter, encoding)
//	    if err != nil {
//	        // handle error
//	    }
//	    defer w.Close()
//
//	    // Write compressed data
//	    w.Write(data)
//	}
//
// The ResponseWriter automatically:
//   - Sets Content-Encoding header
//   - Sets Vary: Accept-Encoding header
//   - Removes Content-Length header (uses chunked transfer encoding)
//   - Handles compression writer lifecycle
//
// # Request Decompression
//
// For decompressing incoming request bodies (uploads), use DecompressRequest:
//
//	if err := compress.DecompressRequest(r); err != nil {
//	    // handle error
//	}
//	// r.Body is now decompressed and ready to read
//	data, _ := io.ReadAll(r.Body)
//
// The DecompressRequest function:
//   - Checks Content-Encoding header
//   - Wraps request body with appropriate decompressor
//   - Removes Content-Encoding header after decompression
//   - Handles cleanup of both decompressor and original body
//
// # Content Negotiation
//
// The SelectEncoding function parses Accept-Encoding headers and selects
// the best encoding based on client preferences and quality values:
//
//	// Client preference: zstd with quality 0.9, gzip with quality 0.8
//	encoding := compress.SelectEncoding("zstd;q=0.9, gzip;q=0.8")
//	// Returns: "zstd"
//
// Preference order when quality values are equal: zstd > gzip > deflate
package compress
