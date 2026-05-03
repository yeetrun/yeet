// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemStorageBlobLifecycleAndMissingBranches(t *testing.T) {
	storage := newTestFilesystemStorage(t)
	ctx := context.Background()

	if got := storage.blobPath("abc"); got != filepath.Join(storage.rootDir, "blobs", "abc") {
		t.Fatalf("short blob path=%q, want flat blob path", got)
	}
	longDigest := "sha256:abcdef"
	if got := storage.blobPath(longDigest); !strings.Contains(got, filepath.Join("blobs", "sha256", "ab", "cd", "abcdef")) {
		t.Fatalf("long blob path=%q, want two-level sha256 path", got)
	}

	data, dg := putTestBlob(t, storage, []byte("blob lifecycle"))
	if !storage.BlobExists(ctx, dg) {
		t.Fatalf("BlobExists(%q)=false, want true", dg)
	}
	size, err := storage.BlobSize(ctx, dg)
	if err != nil {
		t.Fatalf("BlobSize: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("BlobSize=%d, want %d", size, len(data))
	}
	rc, err := storage.GetBlob(ctx, dg)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll blob: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("blob=%q, want %q", got, data)
	}

	if err := storage.DeleteBlob(ctx, dg); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}
	if storage.BlobExists(ctx, dg) {
		t.Fatalf("BlobExists(%q)=true after delete", dg)
	}
	if err := storage.DeleteBlob(ctx, dg); err != nil {
		t.Fatalf("DeleteBlob missing blob returned error: %v", err)
	}
	if _, err := storage.GetBlob(ctx, dg); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("GetBlob missing err=%v, want %v", err, ErrBlobNotFound)
	}
	if _, err := storage.BlobSize(ctx, dg); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("BlobSize missing err=%v, want %v", err, ErrBlobNotFound)
	}
}

func TestNewFilesystemStorageDirectoryErrors(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(rootFile, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("WriteFile rootFile: %v", err)
	}
	if _, err := NewFilesystemStorage(rootFile); err == nil || !strings.Contains(err.Error(), "create root directory") {
		t.Fatalf("NewFilesystemStorage root file err=%v, want create root directory", err)
	}

	rootWithBlobFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootWithBlobFile, "blobs"), []byte("not a directory"), 0644); err != nil {
		t.Fatalf("WriteFile blobs: %v", err)
	}
	if _, err := NewFilesystemStorage(rootWithBlobFile); err == nil || !strings.Contains(err.Error(), "create blobs directory") {
		t.Fatalf("NewFilesystemStorage blobs file err=%v, want create blobs directory", err)
	}

	rootWithManifestFile := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootWithManifestFile, "blobs"), 0755); err != nil {
		t.Fatalf("Mkdir blobs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootWithManifestFile, "manifests"), []byte("not a directory"), 0644); err != nil {
		t.Fatalf("WriteFile manifests: %v", err)
	}
	if _, err := NewFilesystemStorage(rootWithManifestFile); err == nil || !strings.Contains(err.Error(), "create manifests directory") {
		t.Fatalf("NewFilesystemStorage manifests file err=%v, want create manifests directory", err)
	}
}

func TestFilesystemStorageManifestDeleteAndErrorBranches(t *testing.T) {
	storage := newTestFilesystemStorage(t)
	ctx := context.Background()

	if _, err := storage.GetManifest(ctx, "ns/app", "missing"); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("GetManifest missing err=%v, want %v", err, ErrManifestNotFound)
	}
	if _, err := storage.PutManifest(ctx, "ns/app", "latest", []byte("{}"), ""); err == nil || !strings.Contains(err.Error(), "media type is empty") {
		t.Fatalf("PutManifest empty media type err=%v, want media type error", err)
	}

	data := []byte(`{"schemaVersion":2}`)
	const mediaType = "application/vnd.oci.image.manifest.v1+json"
	dg, err := storage.PutManifest(ctx, "ns/app", "latest", data, mediaType)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if !storage.ManifestExists(ctx, "ns/app", "latest") {
		t.Fatal("ManifestExists latest=false, want true")
	}
	if !storage.ManifestExists(ctx, "ns/app", dg) {
		t.Fatal("ManifestExists digest=false, want true")
	}

	if err := storage.DeleteManifest(ctx, "ns/app", "latest"); err != nil {
		t.Fatalf("DeleteManifest latest: %v", err)
	}
	if storage.ManifestExists(ctx, "ns/app", "latest") {
		t.Fatal("ManifestExists latest=true after delete")
	}
	if _, err := os.Stat(storage.manifestPath("ns/app", "latest") + ".mediatype"); !os.IsNotExist(err) {
		t.Fatalf("media type companion stat err=%v, want not exist", err)
	}
	if err := storage.DeleteManifest(ctx, "ns/app", "latest"); err != nil {
		t.Fatalf("DeleteManifest missing latest: %v", err)
	}
}

func TestFilesystemStorageUploadSessionBranches(t *testing.T) {
	storage := newTestFilesystemStorage(t)
	ctx := context.Background()

	if _, err := storage.GetUpload(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "upload not found") {
		t.Fatalf("GetUpload missing err=%v, want upload not found", err)
	}
	if _, err := storage.CopyChunk(ctx, "missing", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "upload not found") {
		t.Fatalf("CopyChunk missing err=%v, want upload not found", err)
	}
	if _, err := storage.CompleteUpload(ctx, "missing", "sha256:missing"); err == nil || !strings.Contains(err.Error(), "upload not found") {
		t.Fatalf("CompleteUpload missing err=%v, want upload not found", err)
	}
	if err := storage.AbortUpload(ctx, "missing"); err != nil {
		t.Fatalf("AbortUpload missing returned error: %v", err)
	}

	upload, err := storage.NewUpload(ctx)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	if upload.Written != 0 {
		t.Fatalf("new upload Written=%d, want 0", upload.Written)
	}
	session, err := storage.CopyChunk(ctx, upload.UUID, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("CopyChunk first: %v", err)
	}
	if session.Written != 5 {
		t.Fatalf("Written after first chunk=%d, want 5", session.Written)
	}
	session, err = storage.CopyChunk(ctx, upload.UUID, strings.NewReader(" world"))
	if err != nil {
		t.Fatalf("CopyChunk second: %v", err)
	}
	if session.Written != 11 {
		t.Fatalf("Written after second chunk=%d, want 11", session.Written)
	}
	session, err = storage.GetUpload(ctx, upload.UUID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if session.Written != 11 {
		t.Fatalf("GetUpload Written=%d, want 11", session.Written)
	}
	if _, err := storage.CompleteUpload(ctx, upload.UUID, "sha256:wrong"); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("CompleteUpload mismatch err=%v, want digest mismatch", err)
	}

	upload, err = storage.NewUpload(ctx)
	if err != nil {
		t.Fatalf("NewUpload for abort: %v", err)
	}
	uploadPath := storage.uploadPath(upload.UUID)
	if _, err := storage.CopyChunk(ctx, upload.UUID, bytes.NewReader([]byte("abort me"))); err != nil {
		t.Fatalf("CopyChunk abort upload: %v", err)
	}
	if err := storage.AbortUpload(ctx, upload.UUID); err != nil {
		t.Fatalf("AbortUpload: %v", err)
	}
	if _, err := storage.GetUpload(ctx, upload.UUID); err == nil || !strings.Contains(err.Error(), "upload not found") {
		t.Fatalf("GetUpload aborted err=%v, want upload not found", err)
	}
	if _, err := os.Stat(uploadPath); !os.IsNotExist(err) {
		t.Fatalf("aborted upload file stat err=%v, want not exist", err)
	}
}
