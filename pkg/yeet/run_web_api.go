// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

//go:embed web_run_assets/*
var webRunAssets embed.FS

const runWebTokenCookieName = "yeet_run_token"

const runWebDefaultCompletionAckTimeout = 10 * time.Second

type runWebServerConfig struct {
	Token                string
	CSRFToken            string
	Root                 string
	Bootstrap            runWebBootstrap
	Config               *projectConfigLocation
	Context              context.Context
	Out                  io.Writer
	Err                  io.Writer
	OnComplete           func()
	CompletionAckTimeout time.Duration
	TerminalProfile      func() runWebTerminalProfile
	TerminalResize       func(context.Context) <-chan catchrpc.Resize
	JournalDir           string
	JournalLimit         int64
	NewJournal           func(string, int64) (runWebEventJournal, error)
}

type runWebServer struct {
	cfg           runWebServerConfig
	ctx           context.Context
	cancel        context.CancelFunc
	mux           *http.ServeMux
	deployMu      sync.Mutex
	active        *runWebJob
	complete      bool
	closed        bool
	nextJob       int64
	completeOnce  sync.Once
	closeOnce     sync.Once
	closeErr      error
	lifecycleDone chan struct{}
}

var executeRunDraftWithOptionsFn = executeRunDraftWithOptions

type runWebValidateResponse struct {
	Draft      RunDraft                 `json:"draft"`
	Validation RunDraftValidationResult `json:"validation"`
	Command    string                   `json:"command,omitempty"`
}

func newRunWebServer(cfg runWebServerConfig) http.Handler {
	parent := cfg.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	cfg.Context = ctx
	s := &runWebServer{
		cfg:           cfg,
		ctx:           ctx,
		cancel:        cancel,
		mux:           http.NewServeMux(),
		lifecycleDone: make(chan struct{}),
	}
	s.mux.HandleFunc("/api/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("/api/files", s.handleFiles)
	s.mux.HandleFunc("/api/host-storage", s.handleHostStorage)
	s.mux.HandleFunc("/api/zfs-roots", s.handleZFSRoots)
	s.mux.HandleFunc("/api/vm-defaults", s.handleVMDefaults)
	s.mux.HandleFunc("/api/validate", s.handleValidate)
	s.mux.HandleFunc("/api/deploy", s.handleDeploy)
	s.mux.HandleFunc("/api/deploy/", s.handleDeployJob)
	s.mux.HandleFunc("/api/session/closed", s.handleSessionClosed)
	s.mux.HandleFunc("/", s.handleStatic)
	go func() {
		defer close(s.lifecycleDone)
		<-s.ctx.Done()
		_ = s.cleanupActive()
	}()
	return s
}

func (s *runWebServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.unsafeAuthorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.setAuthCookie(w, r)
	s.mux.ServeHTTP(w, r)
}

func (s *runWebServer) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		return false
	}
	if r.Header.Get("X-Yeet-Run-Token") == s.cfg.Token || r.URL.Query().Get("token") == s.cfg.Token {
		return true
	}
	cookie, err := r.Cookie(runWebTokenCookieName)
	return err == nil && cookie.Value == s.cfg.Token
}

func (s *runWebServer) unsafeAuthorized(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if r.Header.Get("X-Yeet-Run-Token") == s.cfg.Token || r.URL.Query().Get("token") == s.cfg.Token {
		return true
	}
	return s.cfg.CSRFToken != "" && r.Header.Get("X-Yeet-Run-CSRF") == s.cfg.CSRFToken
}

func (s *runWebServer) setAuthCookie(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Yeet-Run-Token") != s.cfg.Token && r.URL.Query().Get("token") != s.cfg.Token {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     runWebTokenCookieName,
		Value:    s.cfg.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *runWebServer) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeRunWebJSON(w, http.StatusOK, s.cfg.Bootstrap)
}

func (s *runWebServer) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query != "" {
		files, err := searchRunWebFiles(s.cfg.Root, query, r.URL.Query().Get("field"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeRunWebJSON(w, http.StatusOK, files)
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = "."
	}
	files, err := listRunWebFiles(s.cfg.Root, dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeRunWebJSON(w, http.StatusOK, files)
}

func (s *runWebServer) handleHostStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		host = s.cfg.Bootstrap.SelectedHost
	}
	ctx, cancel := runWebHandlerContext(s.cfg.Context, r.Context())
	defer cancel()
	writeRunWebJSON(w, http.StatusOK, runWebHostStorageResponseForHost(ctx, host, catchrpc.ServiceRootDefaultsRequest{
		Service: r.URL.Query().Get("service"),
	}))
}

func (s *runWebServer) handleZFSRoots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	host := strings.TrimSpace(query.Get("host"))
	if host == "" {
		host = s.cfg.Bootstrap.SelectedHost
	}
	ctx, cancel := runWebHandlerContext(s.cfg.Context, r.Context())
	defer cancel()
	writeRunWebJSON(w, http.StatusOK, runWebZFSRootsResponse(ctx, host, catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: query.Get("workload"),
		Service:  query.Get("service"),
	}))
}

func (s *runWebServer) handleVMDefaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	host := strings.TrimSpace(query.Get("host"))
	if host == "" {
		host = s.cfg.Bootstrap.SelectedHost
	}
	zfs, _ := strconv.ParseBool(query.Get("zfs"))
	ctx, cancel := runWebHandlerContext(s.cfg.Context, r.Context())
	defer cancel()
	writeRunWebJSON(w, http.StatusOK, runWebVMDefaultsResponseForHost(ctx, host, catchrpc.VMDefaultsRequest{
		Service:     query.Get("service"),
		ServiceRoot: query.Get("serviceRoot"),
		ZFS:         zfs,
	}))
}

func (s *runWebServer) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	draft, ok := decodeRunWebDraft(w, r)
	if !ok {
		return
	}
	draft.NewServiceOnly = true
	ctx, cancel := runWebHandlerContext(s.cfg.Context, r.Context())
	defer cancel()
	normalized, result := validateRunDraft(ctx, draft, s.cfg.Root)
	writeRunWebJSON(w, http.StatusOK, runWebValidationResponse(normalized, result))
}

func (s *runWebServer) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	draft, ok := decodeRunWebDraft(w, r)
	if !ok {
		return
	}
	draft.NewServiceOnly = true
	validateCtx, cancel := runWebDeployContext(s.cfg.Context)
	defer cancel()
	normalized, result := validateRunDraft(validateCtx, draft, s.cfg.Root)
	if !result.OK {
		writeRunWebJSON(w, http.StatusBadRequest, runWebValidationResponse(normalized, result))
		return
	}
	job, status, message := s.startDeployJob(normalized)
	if job == nil {
		http.Error(w, message, status)
		return
	}
	writeRunWebJSON(w, http.StatusOK, map[string]any{"ok": true, "jobId": job.id})
}

func runWebValidationResponse(draft RunDraft, result RunDraftValidationResult) runWebValidateResponse {
	return runWebValidateResponse{
		Draft:      redactRunWebDraftSecrets(draft),
		Validation: result,
		Command:    runDraftCommandPreview(draft),
	}
}

func (s *runWebServer) startDeployJob(draft RunDraft) (*runWebJob, int, string) {
	s.deployMu.Lock()
	defer s.deployMu.Unlock()
	if s.closed {
		return nil, http.StatusConflict, "deployment server is closed"
	}
	if s.complete {
		return nil, http.StatusConflict, "deployment already completed"
	}
	if s.active != nil && s.active.status().State == runWebJobRunning {
		return nil, http.StatusConflict, "deployment already in progress"
	}
	previous := s.active
	s.nextJob++
	ctx, cancel := runWebDeployContext(s.ctx)
	job, execCtx, err := s.newDeployJob(ctx, strconv.FormatInt(s.nextJob, 10))
	if err != nil {
		cancel()
		return nil, http.StatusInternalServerError, fmt.Sprintf("create deployment journal: %v", err)
	}
	if previous != nil {
		_ = previous.close()
	}
	s.active = job
	if s.ctx.Err() != nil {
		_ = job.close()
	}
	go s.runDeployJob(execCtx, draft, job)
	go func() {
		<-job.done
		cancel()
		if s.ctx.Err() != nil {
			_ = job.close()
		}
	}()
	return job, http.StatusOK, ""
}

func (s *runWebServer) newDeployJob(ctx context.Context, id string) (*runWebJob, context.Context, error) {
	out := s.cfg.Out
	if out == nil {
		out = os.Stdout
	}
	errOut := s.cfg.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	profile := runWebTerminalProfile{}
	if s.cfg.TerminalProfile != nil {
		profile = s.cfg.TerminalProfile()
	}
	profile = normalizeRunWebTerminalProfile(profile)
	var resizeSource <-chan catchrpc.Resize
	if s.cfg.TerminalResize != nil {
		resizeSource = s.cfg.TerminalResize(ctx)
	}
	job, err := newRunWebJob(id, runWebJobConfig{
		Stdout:       out,
		Notice:       errOut,
		JournalDir:   s.cfg.JournalDir,
		JournalLimit: s.cfg.JournalLimit,
		Profile:      profile,
		NewJournal:   s.cfg.NewJournal,
	})
	if err != nil {
		return nil, ctx, err
	}
	remoteResize := bridgeRunWebTerminalResizes(ctx, job, resizeSource)
	execCtx := withRemoteExecTerminalState(ctx, remoteExecTerminalState{
		TTY:    profile.TTY,
		Cols:   profile.Cols,
		Rows:   profile.Rows,
		Term:   profile.Term,
		Resize: remoteResize,
	})
	return job, execCtx, nil
}

func bridgeRunWebTerminalResizes(ctx context.Context, job *runWebJob, source <-chan catchrpc.Resize) <-chan catchrpc.Resize {
	if source == nil {
		return nil
	}
	remote := make(chan catchrpc.Resize)
	go func() {
		defer close(remote)
		for {
			select {
			case <-ctx.Done():
				return
			case size, ok := <-source:
				if !ok {
					return
				}
				job.recordResize(size)
				select {
				case remote <- size:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return remote
}

func (s *runWebServer) runDeployJob(ctx context.Context, draft RunDraft, job *runWebJob) {
	if draft.EnvFile != "" {
		draft.EnvFileSet = true
		draft.EnvFileArg = draft.EnvFile
	}
	err := executeRunDraftWithOptionsFn(ctx, draft, s.cfg.Config, runDraftExecuteOptions{Stdout: job, Stderr: job})
	if err != nil {
		job.finish(err)
		return
	}
	s.deployMu.Lock()
	s.complete = true
	s.deployMu.Unlock()
	job.finish(nil)
	status := job.status()
	if s.cfg.OnComplete != nil && status.State == runWebJobSucceeded {
		go s.awaitSuccessfulRender(job)
	}
}

func (s *runWebServer) completionAckTimeout() time.Duration {
	if s.cfg.CompletionAckTimeout > 0 {
		return s.cfg.CompletionAckTimeout
	}
	return runWebDefaultCompletionAckTimeout
}

func (s *runWebServer) awaitSuccessfulRender(job *runWebJob) {
	timer := time.NewTimer(s.completionAckTimeout())
	defer timer.Stop()
	select {
	case <-job.acknowledged():
	case <-timer.C:
	case <-s.ctx.Done():
		return
	}
	s.completeOnce.Do(func() {
		s.cfg.OnComplete()
	})
}

func (s *runWebServer) lookupJob(id string) (*runWebJob, bool) {
	s.deployMu.Lock()
	defer s.deployMu.Unlock()
	if s.active == nil || s.active.id != id {
		return nil, false
	}
	return s.active, true
}

func (s *runWebServer) handleDeployJob(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/api/deploy/")
	jobID, action, ok := strings.Cut(rel, "/")
	if !ok || jobID == "" {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "status":
		s.handleDeployStatus(w, r, jobID)
	case "stream":
		s.handleDeployStreamAction(w, r, jobID)
	case "ack":
		s.handleDeployAck(w, r, jobID)
	default:
		http.NotFound(w, r)
	}
}

func (s *runWebServer) handleDeployStatus(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	job, ok := s.lookupJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeRunWebJSON(w, http.StatusOK, job.status())
}

func (s *runWebServer) handleDeployStreamAction(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	job, ok := s.lookupJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.handleDeployStream(w, r, job)
}

func (s *runWebServer) handleDeployAck(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	job, ok := s.lookupJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !job.prepareAcknowledgement() {
		http.Error(w, "deployment is not eligible for acknowledgement", http.StatusConflict)
		return
	}
	writeRunWebNoContent(w)
	job.releaseAcknowledgement()
}

func (s *runWebServer) handleDeployStream(w http.ResponseWriter, r *http.Request, job *runWebJob) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	lastID, err := parseRunWebLastEventID(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := job.validateCursor(lastID); err != nil {
		if errors.Is(err, errRunWebJournalCursor) {
			http.Error(w, "Last-Event-ID is not a journal boundary", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, err := io.WriteString(w, "retry: 250\n\n"); err != nil {
		return
	}
	flusher.Flush()
	events, _ := job.subscribe(r.Context(), lastID)
	for ev := range events {
		if err := writeRunWebSSE(w, ev); err != nil {
			return
		}
		flusher.Flush()
	}
}

func parseRunWebLastEventID(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, fmt.Errorf("invalid Last-Event-ID %q", value)
	}
	return cursor, nil
}

func writeRunWebSSE(w io.Writer, ev runWebStreamEvent) error {
	eventName, data, err := ev.ssePayload()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", eventName, ev.ID, data)
	return err
}

func (s *runWebServer) handleSessionClosed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.deployMu.Lock()
	job := s.active
	s.deployMu.Unlock()
	eligible := job != nil && job.prepareAcknowledgement()
	writeRunWebNoContent(w)
	if eligible {
		job.releaseAcknowledgement()
	} else if job != nil {
		job.browserClosed()
	}
}

func writeRunWebNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *runWebServer) close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.deployMu.Lock()
		s.closed = true
		job := s.active
		s.deployMu.Unlock()
		if job != nil {
			s.closeErr = job.close()
		}
	})
	return s.closeErr
}

func (s *runWebServer) cleanupActive() error {
	s.deployMu.Lock()
	job := s.active
	s.deployMu.Unlock()
	if job == nil {
		return nil
	}
	return job.close()
}

func (s *runWebServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	assetPath := path.Clean("/" + r.URL.Path)
	assetPath = path.Clean(assetPath[1:])
	if assetPath == "." {
		assetPath = "index.html"
	}
	if assetPath == "index.html" {
		s.serveIndex(w)
		return
	}
	sub, err := fs.Sub(webRunAssets, "web_run_assets")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := fs.Stat(sub, assetPath); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, sub, assetPath)
}

func (s *runWebServer) serveIndex(w http.ResponseWriter) {
	b, err := fs.ReadFile(webRunAssets, "web_run_assets/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(string(b), "__YEET_SESSION_SCRIPT__", runWebIndexSessionScript(s.cfg.CSRFToken))))
}

func runWebIndexSessionScript(csrfToken string) string {
	encoded, _ := json.Marshal(csrfToken)
	return strings.ReplaceAll(runWebIndexSessionScriptTemplate, "__YEET_CSRF_VALUE__", string(encoded))
}

const runWebIndexSessionScriptTemplate = `<script>
window.__YEET_CSRF_TOKEN__ = __YEET_CSRF_VALUE__;
if (new URLSearchParams(window.location.search).has("token")) {
  window.history.replaceState(null, "", window.location.pathname + window.location.hash);
}
</script>`

func decodeRunWebDraft(w http.ResponseWriter, r *http.Request) (RunDraft, bool) {
	var draft RunDraft
	if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return RunDraft{}, false
	}
	return draft, true
}

func redactRunWebDraftSecrets(draft RunDraft) RunDraft {
	draft.Network.TSAuthKey = ""
	return draft
}

func writeRunWebJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func runWebHandlerContext(parent context.Context, request context.Context) (context.Context, context.CancelFunc) {
	if request == nil {
		request = context.Background()
	}
	if parent == nil {
		return request, func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-request.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func runWebDeployContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		return context.WithCancel(context.Background())
	}
	return context.WithCancel(parent)
}
