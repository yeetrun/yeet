// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

func TestRunWebAPIStaticAssetsRequireAuth(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/app.js?token=secret", nil)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, want javascript", ct)
	}

	req = httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.AddCookie(&http.Cookie{Name: runWebTokenCookieName, Value: "secret"})
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie auth status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIStaticIndexDoesNotSpreadTokenToAssets(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", CSRFToken: "csrf-value", Root: t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/?token=secret", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"/styles.css?token=", "/app.js?token=", "/yeet-mark.svg?token=", "href=\"/?token="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("index contains %q; body=%s", forbidden, body)
		}
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("index body leaked token: %s", body)
	}
	if !strings.Contains(body, "history.replaceState") {
		t.Fatalf("index body missing token removal script: %s", body)
	}
	if !strings.Contains(body, "window.__YEET_CSRF_TOKEN__") {
		t.Fatalf("index body missing csrf script: %s", body)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != runWebTokenCookieName || cookies[0].Value != "secret" || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %#v, want http-only token cookie", cookies)
	}

	req = httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie asset status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIUnsafeRequestsNeedTokenOrCSRFHeader(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", CSRFToken: "csrf-value", Root: t.TempDir()})

	req := httptest.NewRequest(http.MethodPost, "/api/validate", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: runWebTokenCookieName, Value: "secret"})
	req.Header.Set("Origin", "http://127.0.0.1:9999")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie-only unsafe status = %d, want 403 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/validate", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: runWebTokenCookieName, Value: "secret"})
	req.Header.Set("Origin", "http://127.0.0.1:9999")
	req.Header.Set("X-Yeet-Run-CSRF", "csrf-value")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("csrf unsafe status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/validate?token=secret", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query-token unsafe status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIStaticRejectsBadMethodsAndTraversal(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/app.js", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post static status = %d, want 405", rec.Code)
	}

	for _, target := range []string{"/%2e%2e/run_web_api.go", "/assets/%2e%2e/run_web_api.go"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("X-Yeet-Run-Token", "secret")
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 body=%s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestRunWebAPIRedactsTSAuthKeyInValidationResponses(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "run.sh",
		Network: RunDraftNetwork{
			TSAuthKey: "tskey-secret",
		},
	}
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/validate", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tskey-secret") {
		t.Fatalf("validate body leaked ts auth key: %s", rec.Body.String())
	}

	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true}, nil
	}
	rec = runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tskey-secret") {
		t.Fatalf("deploy body leaked ts auth key: %s", rec.Body.String())
	}
}

func TestRunWebAPIValidateAndDeploy(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var deployed RunDraft
	done := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		deployed = draft
		close(done)
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
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background deploy")
	}
	if deployed.Service != "svc-a" || deployed.Host != "host-a" || deployed.Payload != payload || !deployed.NewServiceOnly {
		t.Fatalf("deployed = %#v, want normalized new-service draft", deployed)
	}
	if deployed.EnvFile != envFile || !deployed.EnvFileSet || deployed.EnvFileArg != envFile {
		t.Fatalf("deployed env = file:%q set:%v arg:%q, want normalized explicit env", deployed.EnvFile, deployed.EnvFileSet, deployed.EnvFileArg)
	}
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
}

func TestRunWebAPIDeployStartsJobWithoutWaitingForCompletion(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		startedOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s", rec.Code, rec.Body.String())
	}
	startedResp := decodeRunWebDeployStarted(t, rec)
	if !startedResp.OK || startedResp.JobID == "" {
		t.Fatalf("deploy response = %#v, want ok with job ID", startedResp)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background deploy to start")
	}
	close(release)
	waitRunWebJobState(t, s, startedResp.JobID, runWebJobSucceeded)
}

func TestRunWebAPIDeployStreamReplaysOutputAndStatus(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		_, _ = io.WriteString(opts.Stdout, "deploying\n")
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var terminal bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: &terminal})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)

	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", stream.Code, stream.Body.String())
	}
	if ct := stream.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream Content-Type = %q, want text/event-stream", ct)
	}
	events := parseRunWebSSE(t, stream.Body.String())
	if len(events) != 2 {
		t.Fatalf("events = %#v, want output and status", events)
	}
	if events[0].Name != "output" || events[0].ID == "" {
		t.Fatalf("first event = %#v, want output with id", events[0])
	}
	var output struct {
		Encoding string `json:"encoding"`
		Chunk    string `json:"chunk"`
	}
	if err := json.Unmarshal([]byte(events[0].Data), &output); err != nil {
		t.Fatalf("decode output data: %v", err)
	}
	chunk, err := base64.StdEncoding.DecodeString(output.Chunk)
	if err != nil {
		t.Fatalf("decode output chunk: %v", err)
	}
	if output.Encoding != "base64" || string(chunk) != "deploying\n" {
		t.Fatalf("output event = %#v chunk=%q, want deploying", output, string(chunk))
	}
	if events[1].Name != "status" || !strings.Contains(events[1].Data, `"state":"succeeded"`) {
		t.Fatalf("second event = %#v, want succeeded status", events[1])
	}
	if terminal.String() != "deploying\n" {
		t.Fatalf("terminal output = %q, want deploying", terminal.String())
	}
}

func TestRunWebAPIDeployStreamMirrorsStderr(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		if opts.Stderr == nil {
			return errors.New("stderr writer was nil")
		}
		_, _ = io.WriteString(opts.Stderr, "stderr writer line\n")
		return errors.New("deploy failed")
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var terminal bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: &terminal})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobFailed)

	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", stream.Code, stream.Body.String())
	}
	output := decodeRunWebOutputText(t, parseRunWebSSE(t, stream.Body.String()))
	for _, want := range []string{"stderr writer line\n", "Error: deploy failed\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stream output missing %q:\n%s", want, output)
		}
		if !strings.Contains(terminal.String(), want) {
			t.Fatalf("terminal output missing %q:\n%s", want, terminal.String())
		}
	}
}

func TestRunWebAPIDeployKeepsTerminalBackedOutputTTY(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	oldIsTerminal := isTerminalFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
		isTerminalFn = oldIsTerminal
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = stdoutR.Close() }()
	defer func() { _ = stdoutW.Close() }()
	isTerminalFn = func(fd int) bool {
		return fd == int(stdoutW.Fd())
	}
	gotTTY := make(chan bool, 1)
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		gotTTY <- isWriterTerminal(opts.Stdout)
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: stdoutW})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)

	select {
	case tty := <-gotTTY:
		if !tty {
			t.Fatal("web deploy stdout was not terminal-backed; catch would render plain progress instead of TTY output")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deploy execution")
	}
}

func TestRunWebAPISuccessfulJobCompletesAndRejectsFurtherDeploys(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return nil
	}
	completed := make(chan struct{})
	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, OnComplete: func() { close(completed) }})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OnComplete")
	}
	again := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if again.Code != http.StatusConflict {
		t.Fatalf("second deploy status = %d, want 409 body=%s", again.Code, again.Body.String())
	}
}

func TestRunWebAPISuccessIsNotPublishedBeforeServerIsComplete(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	release := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		<-release
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	handler := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	server := handler.(*runWebServer)
	rec := runWebAPIRequest(t, handler, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	job, ok := server.lookupJob(jobID)
	if !ok {
		t.Fatalf("job %q not found", jobID)
	}

	server.deployMu.Lock()
	close(release)
	select {
	case <-job.done:
		server.deployMu.Unlock()
		t.Fatal("job published terminal success before server completion could be marked")
	case <-time.After(25 * time.Millisecond):
	}
	if status := job.status(); status.State == runWebJobSucceeded {
		server.deployMu.Unlock()
		t.Fatalf("job status = %s while server completion lock is held, want not externally succeeded", status.State)
	}
	server.deployMu.Unlock()

	status := waitRunWebJobState(t, handler, jobID, runWebJobSucceeded)
	if status.State != runWebJobSucceeded {
		t.Fatalf("status = %#v, want succeeded", status)
	}
	server.deployMu.Lock()
	complete := server.complete
	server.deployMu.Unlock()
	if !complete {
		t.Fatal("server complete = false after job succeeded")
	}
}

func TestRunWebAPIFailedJobAllowsRetry(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var mu sync.Mutex
	calls := 0
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return errors.New("deploy failed")
		}
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	first := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	firstID := decodeRunWebDeployStarted(t, first).JobID
	waitRunWebJobState(t, s, firstID, runWebJobFailed)
	second := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	secondID := decodeRunWebDeployStarted(t, second).JobID
	if secondID == firstID {
		t.Fatalf("second job id = %q, want new job id", secondID)
	}
	waitRunWebJobState(t, s, secondID, runWebJobSucceeded)
}

func TestRunWebAPISessionClosedWritesNoticeForFailedIncompleteJob(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return errors.New("deploy failed")
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var notices bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Err: &notices})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobFailed)

	for i := 0; i < 2; i++ {
		closed := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", nil)
		if closed.Code != http.StatusNoContent {
			t.Fatalf("closed status = %d body=%s", closed.Code, closed.Body.String())
		}
	}
	if got := notices.String(); got != runWebBrowserClosedMessage {
		t.Fatalf("notice output = %q, want exactly one close notice after failed incomplete job", got)
	}
}

func TestRunWebAPISessionClosedWritesNoticeOnceForIncompleteActiveJob(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		startedOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var notices bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Err: &notices})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deploy")
	}
	for i := 0; i < 2; i++ {
		closed := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", nil)
		if closed.Code != http.StatusNoContent {
			t.Fatalf("closed status = %d body=%s", closed.Code, closed.Body.String())
		}
	}
	if got := notices.String(); got != runWebBrowserClosedMessage {
		t.Fatalf("notice output = %q, want exactly one close notice", got)
	}
	close(release)
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
	closed := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", nil)
	if closed.Code != http.StatusNoContent {
		t.Fatalf("closed after finish status = %d body=%s", closed.Code, closed.Body.String())
	}
	if got := notices.String(); got != runWebBrowserClosedMessage {
		t.Fatalf("notice after finish = %q, want unchanged", got)
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
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var mu sync.Mutex
	execCount := 0
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
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
	firstID := decodeRunWebDeployStarted(t, first).JobID
	waitRunWebJobState(t, s, firstID, runWebJobSucceeded)

	third := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if third.Code != http.StatusConflict {
		t.Fatalf("third deploy status = %d, want 409 body=%s", third.Code, third.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if execCount != 1 {
		t.Fatalf("executeRunDraftWithOptionsFn calls = %d, want 1", execCount)
	}
}

func TestRunWebAPIDeployUsesConfiguredContext(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
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
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	status := waitRunWebJobState(t, s, jobID, runWebJobFailed)
	if !strings.Contains(status.Error, "context canceled") {
		t.Fatalf("status error = %q, want context canceled", status.Error)
	}
}

func TestRunWebAPIDeployIgnoresCanceledRequestContext(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	done := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		defer close(done)
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Context: context.Background()})
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}); err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", &buf).WithContext(requestCtx)
	req.Header.Set("X-Yeet-Run-Token", "secret")
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background deploy")
	}
}

func TestRunWebAPIDeployJobUnknownAndBadMethods(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/api/deploy/missing/status", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/api/deploy/missing/stream", want: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/deploy/missing/stream", want: http.StatusMethodNotAllowed},
		{method: http.MethodPost, path: "/api/deploy/missing/status", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/api/deploy", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/api/session/closed", want: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		rec := runWebAPIRequest(t, s, tt.method, tt.path, nil)
		if rec.Code != tt.want {
			t.Fatalf("%s %s status = %d, want %d body=%s", tt.method, tt.path, rec.Code, tt.want, rec.Body.String())
		}
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

type runWebDeployStartedResponse struct {
	OK    bool   `json:"ok"`
	JobID string `json:"jobId"`
}

func decodeRunWebDeployStarted(t *testing.T, rec *httptest.ResponseRecorder) runWebDeployStartedResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var response runWebDeployStartedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode deploy response %q: %v", rec.Body.String(), err)
	}
	if !response.OK || response.JobID == "" {
		t.Fatalf("deploy response = %#v, want ok job ID", response)
	}
	return response
}

func waitRunWebJobState(t *testing.T, handler http.Handler, jobID string, want runWebJobState) runWebJobStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var last runWebJobStatus
	for time.Now().Before(deadline) {
		rec := runWebAPIRequest(t, handler, http.MethodGet, "/api/deploy/"+jobID+"/status", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
			t.Fatalf("decode status %q: %v", rec.Body.String(), err)
		}
		if last.State == want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %s state %s, last=%#v", jobID, want, last)
	return runWebJobStatus{}
}

type runWebSSETestEvent struct {
	Name string
	ID   string
	Data string
}

func parseRunWebSSE(t *testing.T, body string) []runWebSSETestEvent {
	t.Helper()
	blocks := strings.Split(strings.TrimSpace(body), "\n\n")
	events := make([]runWebSSETestEvent, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var ev runWebSSETestEvent
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			switch key {
			case "event":
				ev.Name = value
			case "id":
				ev.ID = value
				if _, err := strconv.ParseInt(value, 10, 64); err != nil {
					t.Fatalf("event id %q is not int64: %v", value, err)
				}
			case "data":
				ev.Data = value
			}
		}
		events = append(events, ev)
	}
	return events
}

func decodeRunWebOutputText(t *testing.T, events []runWebSSETestEvent) string {
	t.Helper()
	var out strings.Builder
	for _, ev := range events {
		if ev.Name != string(runWebStreamOutput) {
			continue
		}
		var output struct {
			Encoding string `json:"encoding"`
			Chunk    string `json:"chunk"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &output); err != nil {
			t.Fatalf("decode output event %q: %v", ev.Data, err)
		}
		if output.Encoding != "base64" {
			t.Fatalf("output encoding = %q, want base64", output.Encoding)
		}
		chunk, err := base64.StdEncoding.DecodeString(output.Chunk)
		if err != nil {
			t.Fatalf("decode output chunk %q: %v", output.Chunk, err)
		}
		out.Write(chunk)
	}
	return out.String()
}

func writeRunWebTestPayload(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
}
