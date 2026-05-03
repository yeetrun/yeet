// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRegistryAPIVersionCatalogAndPathHelpers(t *testing.T) {
	storage := &fakeRegistryStorage{}

	tests := []struct {
		name   string
		method string
		path   string
		status int
		code   string
	}{
		{name: "api version", method: http.MethodGet, path: "/v2", status: http.StatusOK},
		{name: "api version slash", method: http.MethodGet, path: "/v2/", status: http.StatusOK},
		{name: "api version method", method: http.MethodPost, path: "/v2", status: http.StatusMethodNotAllowed, code: ErrCodeUnsupported},
		{name: "catalog not implemented", method: http.MethodGet, path: "/v2/_catalog", status: http.StatusNotImplemented, code: ErrCodeUnsupported},
		{name: "catalog method", method: http.MethodPost, path: "/v2/_catalog", status: http.StatusMethodNotAllowed, code: ErrCodeUnsupported},
		{name: "tags list not implemented", method: http.MethodGet, path: "/v2/ns/app/tags/list", status: http.StatusNotFound},
		{name: "bad path", method: http.MethodGet, path: "/bad", status: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := performRegistryRequest(t, storage, tt.method, tt.path, nil, nil)
			if resp.Code != tt.status {
				t.Fatalf("status=%d, want %d", resp.Code, tt.status)
			}
			if tt.path == "/v2" && tt.method == http.MethodGet {
				if got := resp.Header().Get("Docker-Distribution-API-Version"); got != "registry/2.0" {
					t.Fatalf("Docker-Distribution-API-Version=%q, want registry/2.0", got)
				}
			}
			if tt.code != "" {
				requireRegistryError(t, resp, tt.code)
			}
		})
	}

	if BasePath() != "/v2" {
		t.Fatalf("BasePath=%q, want /v2", BasePath())
	}
	if got := ManifestPath("ns/app", "latest"); got != "/v2/ns/app/manifests/latest" {
		t.Fatalf("ManifestPath=%q, want /v2/ns/app/manifests/latest", got)
	}
	if got := BlobPath("ns/app", "sha256:abc"); got != "/v2/ns/app/blobs/sha256:abc" {
		t.Fatalf("BlobPath=%q, want /v2/ns/app/blobs/sha256:abc", got)
	}
}

func TestRegistrySmallHelpers(t *testing.T) {
	if got := normalizedSHA256Digest("sha256:abc"); got != "sha256:abc" {
		t.Fatalf("normalizedSHA256Digest prefixed=%q, want sha256:abc", got)
	}
	if got := normalizedSHA256Digest("abc"); got != "sha256:abc" {
		t.Fatalf("normalizedSHA256Digest bare=%q, want sha256:abc", got)
	}
	if _, err := computeDigest(errReader{}); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("computeDigest err=%v, want read failed", err)
	}
	if err := ListenAndServe("127.0.0.1:bad-port", &fakeRegistryStorage{}); err == nil {
		t.Fatal("ListenAndServe invalid address returned nil error")
	}
}

func TestRegistryManifestResponses(t *testing.T) {
	manifestData := []byte(`{"schemaVersion":2}`)
	manifestDigest := "sha256:manifest"

	t.Run("get uses default media type when storage omits it", func(t *testing.T) {
		storage := &fakeRegistryStorage{
			getManifestMeta: &ManifestMetadata{
				Digest: manifestDigest,
				Size:   int64(len(manifestData)),
				Data:   io.NopCloser(bytes.NewReader(manifestData)),
			},
		}
		resp := performRegistryRequest(t, storage, http.MethodGet, "/v2/ns/app/manifests/latest", nil, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusOK)
		}
		if got := resp.Header().Get("Content-Type"); got != "application/vnd.oci.image.manifest.v1+json" {
			t.Fatalf("Content-Type=%q, want default OCI manifest media type", got)
		}
		if got := resp.Header().Get("Docker-Content-Digest"); got != manifestDigest {
			t.Fatalf("Docker-Content-Digest=%q, want %s", got, manifestDigest)
		}
	})

	t.Run("get supports gzip response compression", func(t *testing.T) {
		storage := &fakeRegistryStorage{
			getManifestMeta: &ManifestMetadata{
				MediaType: "application/test",
				Digest:    manifestDigest,
				Size:      int64(len(manifestData)),
				Data:      io.NopCloser(bytes.NewReader(manifestData)),
			},
		}
		resp := performRegistryRequest(t, storage, http.MethodGet, "/v2/ns/app/manifests/latest", nil, map[string]string{
			"Accept-Encoding": "gzip",
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusOK)
		}
		if got := resp.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding=%q, want gzip", got)
		}
	})

	t.Run("get not found", func(t *testing.T) {
		storage := &fakeRegistryStorage{getManifestErr: ErrManifestNotFound}
		resp := performRegistryRequest(t, storage, http.MethodGet, "/v2/ns/app/manifests/latest", nil, nil)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusNotFound)
		}
		requireRegistryError(t, resp, ErrCodeManifestUnknown)
	})

	t.Run("get storage error", func(t *testing.T) {
		storage := &fakeRegistryStorage{getManifestErr: errors.New("storage failed")}
		resp := performRegistryRequest(t, storage, http.MethodGet, "/v2/ns/app/manifests/latest", nil, nil)
		if resp.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusInternalServerError)
		}
		requireRegistryError(t, resp, ErrCodeManifestInvalid)
	})

	t.Run("head errors", func(t *testing.T) {
		tests := []struct {
			name   string
			err    error
			status int
		}{
			{name: "not found", err: ErrManifestNotFound, status: http.StatusNotFound},
			{name: "storage error", err: errors.New("storage failed"), status: http.StatusInternalServerError},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				storage := &fakeRegistryStorage{getManifestErr: tt.err}
				resp := performRegistryRequest(t, storage, http.MethodHead, "/v2/ns/app/manifests/latest", nil, nil)
				if resp.Code != tt.status {
					t.Fatalf("status=%d, want %d", resp.Code, tt.status)
				}
			})
		}
	})

	t.Run("put validation and storage errors", func(t *testing.T) {
		tests := []struct {
			name    string
			body    string
			headers map[string]string
			storage *fakeRegistryStorage
			status  int
			code    string
		}{
			{
				name:    "invalid gzip",
				body:    "not gzip",
				headers: map[string]string{"Content-Encoding": "gzip"},
				storage: &fakeRegistryStorage{},
				status:  http.StatusBadRequest,
				code:    ErrCodeManifestInvalid,
			},
			{
				name:    "invalid json",
				body:    "{",
				storage: &fakeRegistryStorage{},
				status:  http.StatusBadRequest,
				code:    ErrCodeManifestInvalid,
			},
			{
				name: "media type mismatch",
				body: `{"mediaType":"application/from-body"}`,
				headers: map[string]string{
					"Content-Type": "application/from-header",
				},
				storage: &fakeRegistryStorage{},
				status:  http.StatusBadRequest,
				code:    ErrCodeManifestInvalid,
			},
			{
				name:    "storage error",
				body:    `{"schemaVersion":2}`,
				storage: &fakeRegistryStorage{putManifestErr: errors.New("put failed")},
				status:  http.StatusInternalServerError,
				code:    ErrCodeManifestInvalid,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				resp := performRegistryRequest(t, tt.storage, http.MethodPut, "/v2/ns/app/manifests/latest", strings.NewReader(tt.body), tt.headers)
				if resp.Code != tt.status {
					t.Fatalf("status=%d, want %d", resp.Code, tt.status)
				}
				requireRegistryError(t, resp, tt.code)
			})
		}
	})

	t.Run("put defaults content type", func(t *testing.T) {
		storage := &fakeRegistryStorage{putManifestDigest: manifestDigest}
		resp := performRegistryRequest(t, storage, http.MethodPut, "/v2/ns/app/manifests/latest", strings.NewReader(`{"schemaVersion":2}`), nil)
		if resp.Code != http.StatusCreated {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusCreated)
		}
		if storage.putManifestMediaType != "application/vnd.oci.image.manifest.v1+json" {
			t.Fatalf("media type=%q, want default OCI manifest media type", storage.putManifestMediaType)
		}
		if got := resp.Header().Get("Location"); got != "/v2/ns/app/manifests/"+manifestDigest {
			t.Fatalf("Location=%q, want digest manifest location", got)
		}
	})

	t.Run("delete success and error", func(t *testing.T) {
		success := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodDelete, "/v2/ns/app/manifests/latest", nil, nil)
		if success.Code != http.StatusAccepted {
			t.Fatalf("delete status=%d, want %d", success.Code, http.StatusAccepted)
		}

		failure := performRegistryRequest(t, &fakeRegistryStorage{deleteManifestErr: errors.New("delete failed")}, http.MethodDelete, "/v2/ns/app/manifests/latest", nil, nil)
		if failure.Code != http.StatusInternalServerError {
			t.Fatalf("delete error status=%d, want %d", failure.Code, http.StatusInternalServerError)
		}
		requireRegistryError(t, failure, ErrCodeManifestInvalid)
	})

	t.Run("method not allowed", func(t *testing.T) {
		resp := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodPost, "/v2/ns/app/manifests/latest", nil, nil)
		if resp.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusMethodNotAllowed)
		}
		requireRegistryError(t, resp, ErrCodeUnsupported)
	})
}

func TestRegistryBlobResponses(t *testing.T) {
	blobData := []byte("blob data")
	blobDigest := "sha256:blob"

	t.Run("get supports gzip response compression", func(t *testing.T) {
		storage := &fakeRegistryStorage{blobData: blobData, blobSize: int64(len(blobData))}
		resp := performRegistryRequest(t, storage, http.MethodGet, "/v2/ns/app/blobs/"+blobDigest, nil, map[string]string{
			"Accept-Encoding": "gzip",
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusOK)
		}
		if got := resp.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding=%q, want gzip", got)
		}
		if got := resp.Header().Get("Content-Length"); got != "" {
			t.Fatalf("Content-Length=%q, want empty for compressed response", got)
		}
	})

	t.Run("get not found and storage errors", func(t *testing.T) {
		tests := []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{name: "not found", err: ErrBlobNotFound, status: http.StatusNotFound, code: ErrCodeBlobUnknown},
			{name: "storage error", err: errors.New("read failed"), status: http.StatusInternalServerError, code: ErrCodeBlobUnknown},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				storage := &fakeRegistryStorage{getBlobErr: tt.err}
				resp := performRegistryRequest(t, storage, http.MethodGet, "/v2/ns/app/blobs/"+blobDigest, nil, nil)
				if resp.Code != tt.status {
					t.Fatalf("status=%d, want %d", resp.Code, tt.status)
				}
				requireRegistryError(t, resp, tt.code)
			})
		}
	})

	t.Run("head errors", func(t *testing.T) {
		tests := []struct {
			name   string
			err    error
			status int
		}{
			{name: "not found", err: ErrBlobNotFound, status: http.StatusNotFound},
			{name: "storage error", err: errors.New("size failed"), status: http.StatusInternalServerError},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				storage := &fakeRegistryStorage{blobSizeErr: tt.err}
				resp := performRegistryRequest(t, storage, http.MethodHead, "/v2/ns/app/blobs/"+blobDigest, nil, nil)
				if resp.Code != tt.status {
					t.Fatalf("status=%d, want %d", resp.Code, tt.status)
				}
			})
		}
	})

	t.Run("delete success and error", func(t *testing.T) {
		success := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodDelete, "/v2/ns/app/blobs/"+blobDigest, nil, nil)
		if success.Code != http.StatusAccepted {
			t.Fatalf("delete status=%d, want %d", success.Code, http.StatusAccepted)
		}

		failure := performRegistryRequest(t, &fakeRegistryStorage{deleteBlobErr: errors.New("delete failed")}, http.MethodDelete, "/v2/ns/app/blobs/"+blobDigest, nil, nil)
		if failure.Code != http.StatusInternalServerError {
			t.Fatalf("delete error status=%d, want %d", failure.Code, http.StatusInternalServerError)
		}
		requireRegistryError(t, failure, ErrCodeBlobUnknown)
	})

	t.Run("method not allowed", func(t *testing.T) {
		resp := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodPost, "/v2/ns/app/blobs/"+blobDigest, nil, nil)
		if resp.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusMethodNotAllowed)
		}
		requireRegistryError(t, resp, ErrCodeUnsupported)
	})
}

func TestRegistryBlobUploadResponses(t *testing.T) {
	t.Run("initiate method and storage errors", func(t *testing.T) {
		methodFailure := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodGet, "/v2/ns/app/blobs/uploads", nil, nil)
		if methodFailure.Code != http.StatusMethodNotAllowed {
			t.Fatalf("method status=%d, want %d", methodFailure.Code, http.StatusMethodNotAllowed)
		}
		requireRegistryError(t, methodFailure, ErrCodeUnsupported)

		storageFailure := performRegistryRequest(t, &fakeRegistryStorage{newUploadErr: errors.New("new upload failed")}, http.MethodPost, "/v2/ns/app/blobs/uploads", nil, nil)
		if storageFailure.Code != http.StatusInternalServerError {
			t.Fatalf("storage status=%d, want %d", storageFailure.Code, http.StatusInternalServerError)
		}
		requireRegistryError(t, storageFailure, ErrCodeBlobUploadInvalid)
	})

	t.Run("chunk success and errors", func(t *testing.T) {
		storage := &fakeRegistryStorage{copyChunkSession: &UploadSession{UUID: "upload-id", Written: 4}}
		success := performRegistryRequest(t, storage, http.MethodPatch, "/v2/ns/app/blobs/uploads/upload-id", strings.NewReader("data"), nil)
		if success.Code != http.StatusAccepted {
			t.Fatalf("patch status=%d, want %d", success.Code, http.StatusAccepted)
		}
		if got := success.Header().Get("Range"); got != "0-3" {
			t.Fatalf("Range=%q, want 0-3", got)
		}
		if got := storage.copyChunkBody; got != "data" {
			t.Fatalf("copied body=%q, want data", got)
		}

		badEncoding := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodPatch, "/v2/ns/app/blobs/uploads/upload-id", strings.NewReader("not gzip"), map[string]string{
			"Content-Encoding": "gzip",
		})
		if badEncoding.Code != http.StatusBadRequest {
			t.Fatalf("bad encoding status=%d, want %d", badEncoding.Code, http.StatusBadRequest)
		}
		requireRegistryError(t, badEncoding, ErrCodeBlobUploadInvalid)

		copyFailure := performRegistryRequest(t, &fakeRegistryStorage{copyChunkErr: errors.New("copy failed")}, http.MethodPatch, "/v2/ns/app/blobs/uploads/upload-id", strings.NewReader("data"), nil)
		if copyFailure.Code != http.StatusInternalServerError {
			t.Fatalf("copy failure status=%d, want %d", copyFailure.Code, http.StatusInternalServerError)
		}
		requireRegistryError(t, copyFailure, ErrCodeBlobUploadInvalid)
	})

	t.Run("complete success and errors", func(t *testing.T) {
		digest := "sha256:complete"
		storage := &fakeRegistryStorage{
			copyChunkSession: &UploadSession{UUID: "upload-id", Written: 4},
			completeDigest:   digest,
		}
		success := performRegistryRequest(t, storage, http.MethodPut, "/v2/ns/app/blobs/uploads/upload-id?digest="+digest, strings.NewReader("tail"), nil)
		if success.Code != http.StatusCreated {
			t.Fatalf("put status=%d, want %d", success.Code, http.StatusCreated)
		}
		if got := success.Header().Get("Location"); got != "/v2/ns/app/blobs/"+digest {
			t.Fatalf("Location=%q, want blob location", got)
		}
		if storage.completeExpectedDigest != digest {
			t.Fatalf("expected digest=%q, want %s", storage.completeExpectedDigest, digest)
		}

		missingDigest := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodPut, "/v2/ns/app/blobs/uploads/upload-id", nil, nil)
		if missingDigest.Code != http.StatusBadRequest {
			t.Fatalf("missing digest status=%d, want %d", missingDigest.Code, http.StatusBadRequest)
		}
		requireRegistryError(t, missingDigest, ErrCodeDigestInvalid)

		copyFailure := performRegistryRequest(t, &fakeRegistryStorage{copyChunkErr: errors.New("copy failed")}, http.MethodPut, "/v2/ns/app/blobs/uploads/upload-id?digest="+digest, strings.NewReader("tail"), nil)
		if copyFailure.Code != http.StatusInternalServerError {
			t.Fatalf("copy failure status=%d, want %d", copyFailure.Code, http.StatusInternalServerError)
		}
		requireRegistryError(t, copyFailure, ErrCodeBlobUploadInvalid)

		completeFailure := performRegistryRequest(t, &fakeRegistryStorage{
			copyChunkSession: &UploadSession{UUID: "upload-id", Written: 4},
			completeErr:      errors.New("complete failed"),
		}, http.MethodPut, "/v2/ns/app/blobs/uploads/upload-id?digest="+digest, strings.NewReader("tail"), nil)
		if completeFailure.Code != http.StatusInternalServerError {
			t.Fatalf("complete failure status=%d, want %d", completeFailure.Code, http.StatusInternalServerError)
		}
		requireRegistryError(t, completeFailure, ErrCodeBlobUploadInvalid)
	})

	t.Run("status success and error", func(t *testing.T) {
		for _, written := range []int64{0, 5} {
			t.Run(strconv.FormatInt(written, 10), func(t *testing.T) {
				storage := &fakeRegistryStorage{getUploadSession: &UploadSession{UUID: "upload-id", Written: written}}
				resp := performRegistryRequest(t, storage, http.MethodGet, "/v2/ns/app/blobs/uploads/upload-id", nil, nil)
				if resp.Code != http.StatusNoContent {
					t.Fatalf("status=%d, want %d", resp.Code, http.StatusNoContent)
				}
				wantRange := "0-0"
				if written > 0 {
					wantRange = "0-4"
				}
				if got := resp.Header().Get("Range"); got != wantRange {
					t.Fatalf("Range=%q, want %s", got, wantRange)
				}
			})
		}

		failure := performRegistryRequest(t, &fakeRegistryStorage{getUploadErr: errors.New("missing")}, http.MethodGet, "/v2/ns/app/blobs/uploads/upload-id", nil, nil)
		if failure.Code != http.StatusNotFound {
			t.Fatalf("failure status=%d, want %d", failure.Code, http.StatusNotFound)
		}
		requireRegistryError(t, failure, ErrCodeBlobUploadUnknown)
	})

	t.Run("cancel success and error", func(t *testing.T) {
		success := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodDelete, "/v2/ns/app/blobs/uploads/upload-id", nil, nil)
		if success.Code != http.StatusNoContent {
			t.Fatalf("delete status=%d, want %d", success.Code, http.StatusNoContent)
		}

		failure := performRegistryRequest(t, &fakeRegistryStorage{abortUploadErr: errors.New("missing")}, http.MethodDelete, "/v2/ns/app/blobs/uploads/upload-id", nil, nil)
		if failure.Code != http.StatusNotFound {
			t.Fatalf("delete failure status=%d, want %d", failure.Code, http.StatusNotFound)
		}
		requireRegistryError(t, failure, ErrCodeBlobUploadUnknown)
	})

	t.Run("method not allowed", func(t *testing.T) {
		resp := performRegistryRequest(t, &fakeRegistryStorage{}, http.MethodPost, "/v2/ns/app/blobs/uploads/upload-id", nil, nil)
		if resp.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d, want %d", resp.Code, http.StatusMethodNotAllowed)
		}
		requireRegistryError(t, resp, ErrCodeUnsupported)
	})
}

func performRegistryRequest(t *testing.T, storage Storage, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	NewHandler(storage).ServeHTTP(rr, req)
	return rr
}

func requireRegistryError(t *testing.T, resp *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	var body ErrorResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("Decode error response: %v; body=%q", err, resp.Body.String())
	}
	if len(body.Errors) != 1 {
		t.Fatalf("errors length=%d, want 1", len(body.Errors))
	}
	if got := body.Errors[0].Code; got != wantCode {
		t.Fatalf("error code=%q, want %q; body=%q", got, wantCode, resp.Body.String())
	}
}

type fakeRegistryStorage struct {
	blobData    []byte
	getBlobErr  error
	blobSize    int64
	blobSizeErr error
	blobExists  bool

	getManifestMeta *ManifestMetadata
	getManifestErr  error

	putManifestDigest    string
	putManifestErr       error
	putManifestMediaType string

	deleteBlobErr     error
	deleteManifestErr error

	newUploadSession *UploadSession
	newUploadErr     error

	getUploadSession *UploadSession
	getUploadErr     error

	copyChunkSession *UploadSession
	copyChunkErr     error
	copyChunkBody    string

	completeDigest         string
	completeErr            error
	completeExpectedDigest string

	abortUploadErr error
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (f *fakeRegistryStorage) GetBlob(context.Context, string) (io.ReadCloser, error) {
	if f.getBlobErr != nil {
		return nil, f.getBlobErr
	}
	return io.NopCloser(bytes.NewReader(f.blobData)), nil
}

func (f *fakeRegistryStorage) BlobSize(context.Context, string) (int64, error) {
	if f.blobSizeErr != nil {
		return 0, f.blobSizeErr
	}
	return f.blobSize, nil
}

func (f *fakeRegistryStorage) BlobExists(context.Context, string) bool {
	return f.blobExists
}

func (f *fakeRegistryStorage) DeleteBlob(context.Context, string) error {
	return f.deleteBlobErr
}

func (f *fakeRegistryStorage) GetManifest(context.Context, string, string) (*ManifestMetadata, error) {
	if f.getManifestErr != nil {
		return nil, f.getManifestErr
	}
	if f.getManifestMeta == nil {
		return nil, ErrManifestNotFound
	}
	return f.getManifestMeta, nil
}

func (f *fakeRegistryStorage) PutManifest(_ context.Context, _, _ string, _ []byte, mediaType string) (string, error) {
	f.putManifestMediaType = mediaType
	if f.putManifestErr != nil {
		return "", f.putManifestErr
	}
	if f.putManifestDigest != "" {
		return f.putManifestDigest, nil
	}
	return "sha256:put", nil
}

func (f *fakeRegistryStorage) ManifestExists(context.Context, string, string) bool {
	return false
}

func (f *fakeRegistryStorage) DeleteManifest(context.Context, string, string) error {
	return f.deleteManifestErr
}

func (f *fakeRegistryStorage) NewUpload(context.Context) (*UploadSession, error) {
	if f.newUploadErr != nil {
		return nil, f.newUploadErr
	}
	if f.newUploadSession != nil {
		return f.newUploadSession, nil
	}
	return &UploadSession{UUID: "upload-id"}, nil
}

func (f *fakeRegistryStorage) GetUpload(context.Context, string) (*UploadSession, error) {
	if f.getUploadErr != nil {
		return nil, f.getUploadErr
	}
	if f.getUploadSession != nil {
		return f.getUploadSession, nil
	}
	return &UploadSession{UUID: "upload-id"}, nil
}

func (f *fakeRegistryStorage) CopyChunk(_ context.Context, uuid string, r io.Reader) (*UploadSession, error) {
	if f.copyChunkErr != nil {
		return nil, f.copyChunkErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	f.copyChunkBody += string(data)
	if f.copyChunkSession != nil {
		return f.copyChunkSession, nil
	}
	return &UploadSession{UUID: uuid, Written: int64(len(data))}, nil
}

func (f *fakeRegistryStorage) CompleteUpload(_ context.Context, _, expectedDigest string) (string, error) {
	f.completeExpectedDigest = expectedDigest
	if f.completeErr != nil {
		return "", f.completeErr
	}
	if f.completeDigest != "" {
		return f.completeDigest, nil
	}
	return expectedDigest, nil
}

func (f *fakeRegistryStorage) AbortUpload(context.Context, string) error {
	return f.abortUploadErr
}
