// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"path"
	"strings"
)

type runWebHostStorage struct {
	DataDir      string `json:"dataDir,omitempty"`
	ServicesRoot string `json:"servicesRoot,omitempty"`
}

type runWebHostStorageResponse struct {
	State    string            `json:"state"`
	Storage  runWebHostStorage `json:"storage,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

var fetchRunWebHostStorageInfoFn = fetchRunWebHostStorageInfo

func fetchRunWebHostStorageInfo(ctx context.Context, host string) (serverInfo, error) {
	var info serverInfo
	err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info)
	return info, err
}

func runWebHostStorageResponseForHost(ctx context.Context, host string) runWebHostStorageResponse {
	host = strings.TrimSpace(host)
	if host == "" {
		return runWebHostStorageResponse{
			State:    "error",
			Warnings: []string{"no host selected"},
		}
	}
	info, err := fetchRunWebHostStorageInfoFn(ctx, host)
	if err != nil {
		if isRunWebZFSHostUnreachable(err) {
			return runWebHostStorageResponse{State: "host-unreachable"}
		}
		return runWebHostStorageResponse{
			State:    "error",
			Warnings: []string{err.Error()},
		}
	}
	storage := runWebHostStorage{
		DataDir:      strings.TrimSpace(info.RootDir),
		ServicesRoot: strings.TrimSpace(info.ServicesDir),
	}
	if storage.ServicesRoot == "" && storage.DataDir != "" {
		storage.ServicesRoot = path.Join(storage.DataDir, "services")
	}
	if storage.DataDir == "" && storage.ServicesRoot == "" {
		return runWebHostStorageResponse{
			State:    "unknown",
			Warnings: []string{"catch did not report host storage paths"},
		}
	}
	return runWebHostStorageResponse{
		State:   "available",
		Storage: storage,
	}
}
