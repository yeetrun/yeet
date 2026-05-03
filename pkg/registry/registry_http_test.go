// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestBlobHeadIncludesContentLength(t *testing.T) {
	storage := newTestFilesystemStorage(t)
	data, digest := putTestBlob(t, storage, []byte("hello registry blob"))

	server := httptest.NewServer(NewHandler(storage))
	defer server.Close()

	req, err := http.NewRequest(http.MethodHead, server.URL+"/v2/test/blobs/"+digest, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	wantLen := strconv.Itoa(len(data))
	if got := resp.Header.Get("Content-Length"); got != wantLen {
		t.Fatalf("Content-Length = %q, want %q", got, wantLen)
	}
}

func TestManifestPutGetAndHead(t *testing.T) {
	storage := newTestFilesystemStorage(t)
	server := httptest.NewServer(NewHandler(storage))
	defer server.Close()

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","subject":{"digest":"sha256:abc"}}`
	putReq, err := http.NewRequest(http.MethodPut, server.URL+"/v2/ns/app/manifests/latest", strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("Do PUT: %v", err)
	}
	defer putResp.Body.Close()
	_, _ = io.Copy(io.Discard, putResp.Body)

	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want %d", putResp.StatusCode, http.StatusCreated)
	}
	digest := putResp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		t.Fatal("missing manifest digest")
	}
	if got := putResp.Header.Get("OCI-Subject"); got != "sha256:abc" {
		t.Fatalf("OCI-Subject = %q, want sha256:abc", got)
	}

	getResp, err := http.Get(server.URL + "/v2/ns/app/manifests/latest")
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	defer getResp.Body.Close()
	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getResp.StatusCode, http.StatusOK)
	}
	if string(body) != manifest {
		t.Fatalf("GET body = %q, want %q", body, manifest)
	}
	if got := getResp.Header.Get("Docker-Content-Digest"); got != digest {
		t.Fatalf("GET digest = %q, want %q", got, digest)
	}

	headReq, err := http.NewRequest(http.MethodHead, server.URL+"/v2/ns/app/manifests/"+digest, nil)
	if err != nil {
		t.Fatalf("NewRequest HEAD: %v", err)
	}
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("Do HEAD: %v", err)
	}
	defer headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", headResp.StatusCode, http.StatusOK)
	}
	if got := headResp.Header.Get("Content-Length"); got != strconv.Itoa(len(manifest)) {
		t.Fatalf("HEAD Content-Length = %q, want %d", got, len(manifest))
	}
}

func TestBlobGetReturnsContentAndLength(t *testing.T) {
	storage := newTestFilesystemStorage(t)
	data, digest := putTestBlob(t, storage, []byte("hello blob get"))

	server := httptest.NewServer(NewHandler(storage))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v2/test/blobs/"+digest, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if string(body) != string(data) {
		t.Fatalf("body = %q, want %q", body, data)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(data)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(data))
	}
}

func newTestFilesystemStorage(t *testing.T) *FilesystemStorage {
	t.Helper()
	storage, err := NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	return storage
}

func putTestBlob(t *testing.T, storage *FilesystemStorage, data []byte) ([]byte, string) {
	t.Helper()
	upload, err := storage.NewUpload(context.Background())
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	if _, err := storage.CopyChunk(context.Background(), upload.UUID, bytes.NewReader(data)); err != nil {
		t.Fatalf("CopyChunk: %v", err)
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if _, err := storage.CompleteUpload(context.Background(), upload.UUID, digest); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	return data, digest
}
