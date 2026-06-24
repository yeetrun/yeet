// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirecrackerBalloonPatchUsesUnixHTTP(t *testing.T) {
	socket, requests := newFirecrackerBalloonUnixHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	err := firecrackerBalloonAPI{}.SetTarget(context.Background(), socket, 768<<20)
	if err != nil {
		t.Fatalf("SetTarget: %v", err)
	}

	got := <-requests
	if got.Method != http.MethodPatch || got.Path != "/balloon" {
		t.Fatalf("request = %s %s, want PATCH /balloon", got.Method, got.Path)
	}
	if got.ContentType != "application/json" || got.Accept != "application/json" {
		t.Fatalf("headers content-type=%q accept=%q, want json", got.ContentType, got.Accept)
	}
	assertFirecrackerBalloonRequestJSON(t, got.Body, map[string]any{"amount_mib": float64(768)})
}

func TestFirecrackerBalloonStatsUsesUnixHTTP(t *testing.T) {
	socket, requests := newFirecrackerBalloonUnixHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"target_pages": 512,
			"actual_pages": 384,
			"free_memory": 1048576,
			"available_memory": 2097152
		}`)
	}))

	got, err := firecrackerBalloonAPI{}.Stats(context.Background(), socket)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	req := <-requests
	if req.Method != http.MethodGet || req.Path != "/balloon/statistics" {
		t.Fatalf("request = %s %s, want GET /balloon/statistics", req.Method, req.Path)
	}
	if req.Accept != "application/json" {
		t.Fatalf("accept = %q, want application/json", req.Accept)
	}
	want := vmBalloonStats{
		TargetBytes:          512 * 4096,
		ActualBytes:          384 * 4096,
		FreeMemoryBytes:      1048576,
		AvailableMemoryBytes: 2097152,
	}
	if got != want {
		t.Fatalf("stats = %#v, want %#v", got, want)
	}
}

func TestFirecrackerBalloonStatsClosesUnixConnection(t *testing.T) {
	socketPath := filepath.Join(shortUnixSocketDirForTest(t), "firecracker-balloon-close.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	closed := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"target_pages": 0,
				"actual_pages": 0,
				"free_memory": 1048576,
				"available_memory": 2097152
			}`)
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				select {
				case <-closed:
				default:
					close(closed)
				}
			}
		},
	}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	if _, err := (firecrackerBalloonAPI{}).Stats(context.Background(), socketPath); err != nil {
		t.Fatalf("Stats: %v", err)
	}

	select {
	case <-closed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("firecracker balloon API connection stayed open after response")
	}
}

func TestFirecrackerBalloonRejectsNegativeTarget(t *testing.T) {
	err := firecrackerBalloonAPI{}.SetTarget(context.Background(), "/tmp/firecracker.sock", -1)
	if err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("SetTarget error = %v, want negative target error", err)
	}
}

func TestFirecrackerBalloonRejectsEmptySocket(t *testing.T) {
	err := firecrackerBalloonAPI{}.SetTarget(context.Background(), "", 0)
	if err == nil || !strings.Contains(err.Error(), "socket") {
		t.Fatalf("SetTarget error = %v, want empty socket error", err)
	}
	_, err = firecrackerBalloonAPI{}.Stats(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "socket") {
		t.Fatalf("Stats error = %v, want empty socket error", err)
	}
}

func TestFirecrackerBalloonReportsNonSuccessStatus(t *testing.T) {
	socket, _ := newFirecrackerBalloonUnixHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no balloon", http.StatusBadRequest)
	}))

	err := firecrackerBalloonAPI{}.SetTarget(context.Background(), socket, 0)
	if err == nil || !strings.Contains(err.Error(), "400 Bad Request") {
		t.Fatalf("SetTarget error = %v, want 400 status", err)
	}
}

type firecrackerBalloonUnixHTTPRequest struct {
	Method      string
	Path        string
	ContentType string
	Accept      string
	Body        string
}

func newFirecrackerBalloonUnixHTTPTestServer(t *testing.T, handler http.Handler) (string, <-chan firecrackerBalloonUnixHTTPRequest) {
	t.Helper()
	socketPath := filepath.Join(shortUnixSocketDirForTest(t), "firecracker-balloon.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	requests := make(chan firecrackerBalloonUnixHTTPRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		requests <- firecrackerBalloonUnixHTTPRequest{
			Method:      r.Method,
			Path:        r.URL.Path,
			ContentType: r.Header.Get("Content-Type"),
			Accept:      r.Header.Get("Accept"),
			Body:        string(raw),
		}
		handler.ServeHTTP(w, r)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return socketPath, requests
}

func assertFirecrackerBalloonRequestJSON(t *testing.T, body string, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode request body %q: %v", body, err)
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Fatalf("body %q key %q = %#v, want %#v", body, key, got[key], wantValue)
		}
	}
}
