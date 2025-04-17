// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package registry implements an OCI Distribution Specification v1.1 compliant registry.
//
// The registry supports the full OCI Distribution Spec including:
//   - Pull workflow (required)
//   - Push workflow
//   - Content management
//   - Manifest and blob storage
//   - OCI-compliant error responses
//   - HTTP compression for data transfer (zstd, gzip, deflate)
//
// # Compression Support
//
// The registry supports bidirectional compression for both requests and responses.
//
// ## Response Compression (Downloads)
//
// The registry automatically compresses blob and manifest responses when clients
// include an Accept-Encoding header. Supported compression algorithms:
//
//   - zstd (Zstandard) - preferred for best compression ratio
//   - gzip - widely supported, good compression
//   - deflate - basic compression support
//
// The registry negotiates compression based on client preferences expressed via
// quality values in the Accept-Encoding header. When multiple encodings are
// acceptable, the registry prefers zstd > gzip > deflate.
//
// Examples:
//
//	Accept-Encoding: gzip
//	Accept-Encoding: zstd, gzip;q=0.9, deflate;q=0.8
//	Accept-Encoding: *
//
// Compression is applied transparently to GET requests for:
//   - Manifests (/v2/<repo>/manifests/<reference>)
//   - Blobs (/v2/<repo>/blobs/<digest>)
//
// When compression is used, the response includes:
//   - Content-Encoding header indicating the algorithm
//   - Vary: Accept-Encoding header for cache control
//   - Content-Length header is removed (chunked transfer encoding used)
//
// ## Request Decompression (Uploads)
//
// The registry automatically decompresses incoming request bodies when clients
// send a Content-Encoding header. This is useful for reducing upload bandwidth.
//
// Supported encodings for uploads:
//   - zstd, gzip, deflate
//
// Example:
//
//	Content-Encoding: gzip
//
// Request decompression is supported for:
//   - Manifest uploads (PUT /v2/<repo>/manifests/<reference>)
//   - Blob chunk uploads (PATCH /v2/<repo>/blobs/uploads/<uuid>)
//   - Blob upload completion (PUT /v2/<repo>/blobs/uploads/<uuid>)
//
// Spec: https://github.com/opencontainers/distribution-spec/blob/main/spec.md
package registry
