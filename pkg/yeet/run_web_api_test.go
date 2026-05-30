// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestRunWebAPITokenRequired(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRunWebAPITokenQueryAllowedButEmptyConfiguredTokenRejected(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap?token=secret", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query token status = %d, want 200", rec.Code)
	}

	s = newRunWebServer(runWebServerConfig{Root: t.TempDir()})
	req = httptest.NewRequest(http.MethodGet, "/api/bootstrap?token=", nil)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty configured token status = %d, want 401", rec.Code)
	}
}

func TestRunWebAPIBootstrapAndFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      root,
		Bootstrap: runWebBootstrap{SelectedHost: "host-a", Hosts: []string{"host-a"}},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/bootstrap", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d", rec.Code)
	}
	rec = runWebAPIRequest(t, s, http.MethodGet, "/api/files?dir=.", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("files status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "compose.yml") {
		t.Fatalf("files body = %s, want compose.yml", rec.Body.String())
	}
}

func TestRunWebAPIValidateAndDeploy(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var deployed RunDraft
	executeRunDraftFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, force bool) error {
		deployed = draft
		return nil
	}
	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	envFile := filepath.Join(root, ".env")
	if err := os.WriteFile(envFile, []byte("A=B\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
		Config: &projectConfigLocation{
			Dir:    root,
			Config: &ProjectConfig{Version: projectConfigVersion},
		},
	})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh", EnvFile: ".env"}
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/validate", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s", rec.Code, rec.Body.String())
	}
	if deployed.Service != "svc-a" || deployed.Host != "host-a" || deployed.Payload != payload || !deployed.NewServiceOnly {
		t.Fatalf("deployed = %#v, want normalized new-service draft", deployed)
	}
	if deployed.EnvFile != envFile || !deployed.EnvFileSet || deployed.EnvFileArg != envFile {
		t.Fatalf("deployed env = file:%q set:%v arg:%q, want normalized explicit env", deployed.EnvFile, deployed.EnvFileSet, deployed.EnvFileArg)
	}
	if !strings.Contains(rec.Body.String(), "Service deployed. Close this tab and return to the terminal.") {
		t.Fatalf("deploy body = %s, want success message", rec.Body.String())
	}
}

func TestRunWebAPIDeployRepeatsValidation(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true}, nil
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "ghcr.io/example/app:latest"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("deploy body = %s, want already exists", rec.Body.String())
	}
}

func TestRunWebAPIDeployIsSingleUse(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var mu sync.Mutex
	execCount := 0
	executeRunDraftFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, force bool) error {
		mu.Lock()
		execCount++
		mu.Unlock()
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first deploy to start")
	}

	second := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if second.Code != http.StatusConflict {
		t.Fatalf("second deploy status = %d, want 409 body=%s", second.Code, second.Body.String())
	}
	close(release)
	first := <-firstDone
	if first.Code != http.StatusOK {
		t.Fatalf("first deploy status = %d, want 200 body=%s", first.Code, first.Body.String())
	}

	third := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if third.Code != http.StatusConflict {
		t.Fatalf("third deploy status = %d, want 409 body=%s", third.Code, third.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if execCount != 1 {
		t.Fatalf("executeRunDraftFn calls = %d, want 1", execCount)
	}
}

func TestRunWebAPIDeployUsesConfiguredContext(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, force bool) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
			return context.DeadlineExceeded
		}
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Context: ctx})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy status = %d, want 500 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "context canceled") {
		t.Fatalf("deploy body = %s, want context canceled", rec.Body.String())
	}
}

func runWebAPIRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
		r = &buf
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("X-Yeet-Run-Token", "secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
