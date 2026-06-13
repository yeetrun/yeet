// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type runWebVMDefaultsResponse struct {
	State    string                      `json:"state"`
	Defaults catchrpc.VMDefaultsResponse `json:"defaults,omitempty"`
	Warnings []string                    `json:"warnings,omitempty"`
}

var fetchRunWebVMDefaultsFn = fetchRunWebVMDefaults

func fetchRunWebVMDefaults(ctx context.Context, host string, req catchrpc.VMDefaultsRequest) (catchrpc.VMDefaultsResponse, error) {
	return newRPCClient(host).VMDefaults(ctx, req)
}

func runWebVMDefaultsResponseForHost(ctx context.Context, host string, req catchrpc.VMDefaultsRequest) runWebVMDefaultsResponse {
	host = strings.TrimSpace(host)
	if host == "" {
		return runWebVMDefaultsResponse{
			State:    "error",
			Warnings: []string{"no host selected"},
		}
	}
	resp, err := fetchRunWebVMDefaultsFn(ctx, host, req)
	if err == nil {
		return runWebVMDefaultsResponse{
			State:    "available",
			Defaults: resp,
			Warnings: resp.Warnings,
		}
	}
	switch {
	case isRPCMethodNotFound(err):
		return runWebVMDefaultsResponse{State: "unsupported-rpc"}
	case isRunWebZFSHostUnreachable(err):
		return runWebVMDefaultsResponse{State: "host-unreachable"}
	default:
		return runWebVMDefaultsResponse{
			State:    "error",
			Warnings: []string{err.Error()},
		}
	}
}
