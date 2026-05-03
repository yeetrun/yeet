// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestPayloadNamedReadCloserName(t *testing.T) {
	payload := &namedReadCloser{ReadCloser: io.NopCloser(strings.NewReader("payload")), name: "app"}
	if got := payload.Name(); got != "app" {
		t.Fatalf("Name() = %q, want app", got)
	}
	if err := payload.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

func TestPayloadOpenMissingFileWrapsDetectionError(t *testing.T) {
	_, cleanup, _, err := openPayloadForUpload(filepath.Join(t.TempDir(), "missing"), "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "failed to detect file type") {
		t.Fatalf("openPayloadForUpload error = %v, want detection error", err)
	}
	if cleanup != nil {
		t.Fatalf("cleanup = non-nil, want nil on detect error")
	}
}
