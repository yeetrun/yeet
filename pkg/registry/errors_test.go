// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorWritesOCIErrorResponse(t *testing.T) {
	w := httptest.NewRecorder()
	detail := map[string]string{"digest": "sha256:missing"}

	WriteError(w, http.StatusNotFound, ErrCodeBlobUnknown, "blob not found", detail)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q, want application/json", got)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("Decode error response: %v", err)
	}
	if len(body.Errors) != 1 {
		t.Fatalf("errors length=%d, want 1", len(body.Errors))
	}
	got := body.Errors[0]
	if got.Code != ErrCodeBlobUnknown || got.Message != "blob not found" {
		t.Fatalf("error descriptor=%+v, want code %s message %q", got, ErrCodeBlobUnknown, "blob not found")
	}
	gotDetail, ok := got.Detail.(map[string]any)
	if !ok {
		t.Fatalf("detail type=%T, want object", got.Detail)
	}
	if gotDetail["digest"] != "sha256:missing" {
		t.Fatalf("detail digest=%v, want sha256:missing", gotDetail["digest"])
	}
}

func TestErrorDescriptorErrorString(t *testing.T) {
	err := NewError(ErrCodeManifestInvalid, "invalid manifest")

	if err.Code != ErrCodeManifestInvalid || err.Message != "invalid manifest" {
		t.Fatalf("NewError=%+v, want code %s message %q", err, ErrCodeManifestInvalid, "invalid manifest")
	}
	if got := err.Error(); got != "MANIFEST_INVALID: invalid manifest" {
		t.Fatalf("Error()=%q, want %q", got, "MANIFEST_INVALID: invalid manifest")
	}
}
