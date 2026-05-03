// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"errors"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type failingInfoWriter struct {
	err error
}

func (w failingInfoWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type failAfterInfoWriter struct {
	writes int
	err    error
}

func (w *failAfterInfoWriter) Write(p []byte) (int, error) {
	if w.writes == 0 {
		w.writes++
		return len(p), nil
	}
	return 0, w.err
}

func TestRenderInfoPlainReportsWriteError(t *testing.T) {
	want := errors.New("write failed")

	err := renderInfoPlain(failingInfoWriter{err: want}, "svc", "host", nil, serverInfo{}, clientInfo{}, catchrpc.ServiceInfoResponse{})
	if !errors.Is(err, want) {
		t.Fatalf("renderInfoPlain error = %v, want %v", err, want)
	}
}

func TestRenderInfoPlainReportsTabwriterFlushError(t *testing.T) {
	want := errors.New("flush failed")
	w := &failAfterInfoWriter{err: want}

	err := renderInfoPlain(w, "svc", "host", nil, serverInfo{}, clientInfo{}, catchrpc.ServiceInfoResponse{})
	if !errors.Is(err, want) {
		t.Fatalf("renderInfoPlain error = %v, want %v", err, want)
	}
}
