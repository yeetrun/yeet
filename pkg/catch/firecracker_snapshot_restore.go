// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"net/http"
)

type firecrackerLoadSnapshotRequest struct {
	SnapshotPath string                            `json:"snapshot_path"`
	MemBackend   firecrackerLoadSnapshotMemBackend `json:"mem_backend"`
	ResumeVM     bool                              `json:"resume_vm"`
}

type firecrackerLoadSnapshotMemBackend struct {
	BackendPath string `json:"backend_path"`
	BackendType string `json:"backend_type"`
}

func (firecrackerSnapshotAPI) LoadSnapshot(ctx context.Context, socket, statePath, memoryPath string, resume bool) error {
	body := firecrackerLoadSnapshotRequest{
		SnapshotPath: statePath,
		MemBackend: firecrackerLoadSnapshotMemBackend{
			BackendPath: memoryPath,
			BackendType: "File",
		},
		ResumeVM: resume,
	}
	return firecrackerJSON(ctx, socket, http.MethodPut, "http://unix/snapshot/load", body)
}
