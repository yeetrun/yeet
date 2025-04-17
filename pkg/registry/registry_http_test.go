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
	"testing"
)

func TestBlobHeadIncludesContentLength(t *testing.T) {
	storage, err := NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	data := []byte("hello registry blob")
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
