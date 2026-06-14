// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFirecrackerLoadSnapshotRequest(t *testing.T) {
	socket, requests := newFirecrackerUnixHTTPTestServer(t, http.StatusNoContent)

	err := firecrackerSnapshotAPI{}.LoadSnapshot(context.Background(), socket, "/tmp/state.bin", "/tmp/memory.bin", true)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}

	got := <-requests
	if got.Method != http.MethodPut || got.Path != "/snapshot/load" {
		t.Fatalf("request = %s %s, want PUT /snapshot/load", got.Method, got.Path)
	}
	var body struct {
		SnapshotPath string `json:"snapshot_path"`
		MemBackend   struct {
			BackendPath string `json:"backend_path"`
			BackendType string `json:"backend_type"`
		} `json:"mem_backend"`
		ResumeVM bool `json:"resume_vm"`
	}
	if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
		t.Fatalf("decode load snapshot body %q: %v", got.Body, err)
	}
	if body.SnapshotPath != "/tmp/state.bin" ||
		body.MemBackend.BackendPath != "/tmp/memory.bin" ||
		body.MemBackend.BackendType != "File" ||
		!body.ResumeVM {
		t.Fatalf("body = %#v, want Firecracker load snapshot request", body)
	}
}
