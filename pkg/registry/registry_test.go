// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestParseRegistryPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantType  PathType
		wantRepo  string
		wantRef   string
		wantError string
	}{
		{name: "manifest", path: "/v2/ns/app/manifests/latest", wantType: PathTypeManifest, wantRepo: "ns/app", wantRef: "latest"},
		{name: "manifest digest", path: "/v2/app/manifests/sha256:abc", wantType: PathTypeManifest, wantRepo: "app", wantRef: "sha256:abc"},
		{name: "blob", path: "/v2/ns/app/blobs/sha256:abc", wantType: PathTypeBlob, wantRepo: "ns/app", wantRef: "sha256:abc"},
		{name: "blob upload init", path: "/v2/ns/app/blobs/uploads", wantType: PathTypeBlobUploadInit, wantRepo: "ns/app"},
		{name: "blob upload", path: "/v2/ns/app/blobs/uploads/upload-id", wantType: PathTypeBlobUpload, wantRepo: "ns/app", wantRef: "upload-id"},
		{name: "tags list", path: "/v2/ns/app/tags/list", wantType: PathTypeTagsList, wantRepo: "ns/app"},
		{name: "wrong prefix", path: "/v1/app/manifests/latest", wantError: "path must start"},
		{name: "too short", path: "/v2/app", wantError: "path too short"},
		{name: "missing operation", path: "/v2/app/latest", wantError: "no valid operation"},
		{name: "missing manifest reference", path: "/v2/app/manifests", wantError: "missing reference"},
		{name: "missing blob subpath", path: "/v2/app/blobs", wantError: "missing subpath"},
		{name: "bad tags path", path: "/v2/app/tags/bad", wantError: "tags/list"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRegistryPath(tt.path)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRegistryPath: %v", err)
			}
			if got.Type != tt.wantType || got.Repo != tt.wantRepo || got.Reference != tt.wantRef {
				t.Fatalf("path = %+v, want type=%s repo=%q ref=%q", got, tt.wantType, tt.wantRepo, tt.wantRef)
			}
		})
	}
}

func TestPathTypeString(t *testing.T) {
	tests := map[PathType]string{
		PathTypeManifest:       "manifest",
		PathTypeBlob:           "blob",
		PathTypeBlobUploadInit: "blob_upload_init",
		PathTypeBlobUpload:     "blob_upload",
		PathTypeTagsList:       "tags_list",
		PathTypeUnknown:        "unknown",
		PathType(99):           "unknown",
	}
	for typ, want := range tests {
		if got := typ.String(); got != want {
			t.Fatalf("PathType(%d).String() = %q, want %q", typ, got, want)
		}
	}
}

func TestFilesystemStoragePutAndGetManifest(t *testing.T) {
	storage, err := NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	ctx := context.Background()
	data := []byte(`{"schemaVersion":2}`)
	const mediaType = "application/vnd.oci.image.manifest.v1+json"

	digest, err := storage.PutManifest(ctx, "ns/app", "latest", data, mediaType)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	for _, ref := range []string{"latest", digest} {
		t.Run(ref, func(t *testing.T) {
			manifest, err := storage.GetManifest(ctx, "ns/app", ref)
			if err != nil {
				t.Fatalf("GetManifest(%q): %v", ref, err)
			}
			defer func() { _ = manifest.Data.Close() }()
			got, err := io.ReadAll(manifest.Data)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != string(data) {
				t.Fatalf("manifest data = %q, want %q", got, data)
			}
			if manifest.MediaType != mediaType {
				t.Fatalf("media type = %q, want %q", manifest.MediaType, mediaType)
			}
			if manifest.Digest != digest {
				t.Fatalf("digest = %q, want %q", manifest.Digest, digest)
			}
		})
	}
}
