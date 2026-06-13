// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

var fetchRunWebZFSRootCandidatesFn = fetchRunWebZFSRootCandidates

func fetchRunWebZFSRootCandidates(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
	return newRPCClient(host).ZFSServiceRootCandidates(ctx, req)
}

func runWebZFSRootsResponse(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) catchrpc.ZFSServiceRootCandidatesResponse {
	host = strings.TrimSpace(host)
	if host == "" {
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State:    catchrpc.ZFSRootDiscoveryError,
			Warnings: []string{"no host selected"},
		}
	}
	resp, err := fetchRunWebZFSRootCandidatesFn(ctx, host, req)
	if err == nil {
		return resp
	}
	switch {
	case isRPCMethodNotFound(err):
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryUnsupportedRPC}
	case isRunWebZFSHostUnreachable(err):
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryHostUnreachable}
	default:
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State:    catchrpc.ZFSRootDiscoveryError,
			Warnings: []string{err.Error()},
		}
	}
}

func isRunWebZFSHostUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	for _, target := range []error{
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.ETIMEDOUT,
		syscall.EHOSTUNREACH,
		syscall.ENETUNREACH,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
