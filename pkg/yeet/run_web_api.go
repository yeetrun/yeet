// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
)

//go:embed web_run_assets/*
var webRunAssets embed.FS

const runWebTokenCookieName = "yeet_run_token"

type runWebServerConfig struct {
	Token      string
	CSRFToken  string
	Root       string
	Bootstrap  runWebBootstrap
	Config     *projectConfigLocation
	Context    context.Context
	Out        io.Writer
	Err        io.Writer
	OnComplete func()
}

type runWebServer struct {
	cfg      runWebServerConfig
	mux      *http.ServeMux
	deployMu sync.Mutex
	active   *runWebJob
	complete bool
	nextJob  int64
}

var executeRunDraftWithOptionsFn = executeRunDraftWithOptions

func newRunWebServer(cfg runWebServerConfig) http.Handler {
	s := &runWebServer{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("/api/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("/api/files", s.handleFiles)
	s.mux.HandleFunc("/api/validate", s.handleValidate)
	s.mux.HandleFunc("/api/deploy", s.handleDeploy)
	s.mux.HandleFunc("/api/deploy/", s.handleDeployJob)
	s.mux.HandleFunc("/api/session/closed", s.handleSessionClosed)
	s.mux.HandleFunc("/", s.handleStatic)
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
	writeRunWebJSON(w, http.StatusOK, map[string]any{"draft": redactRunWebDraftSecrets(normalized), "validation": result})
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
		writeRunWebJSON(w, http.StatusBadRequest, map[string]any{"draft": redactRunWebDraftSecrets(normalized), "validation": result})
		return
	}
	job, status, message := s.startDeployJob(normalized)
	if job == nil {
		http.Error(w, message, status)
		return
	}
	writeRunWebJSON(w, http.StatusOK, map[string]any{"ok": true, "jobId": job.id})
}

func (s *runWebServer) startDeployJob(draft RunDraft) (*runWebJob, int, string) {
	s.deployMu.Lock()
	defer s.deployMu.Unlock()
	if s.complete {
		return nil, http.StatusConflict, "deployment already completed"
	}
	if s.active != nil && s.active.status().State == runWebJobRunning {
		return nil, http.StatusConflict, "deployment already in progress"
	}
	s.nextJob++
	out := s.cfg.Out
	if out == nil {
		out = os.Stdout
	}
	errOut := s.cfg.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	job := newRunWebJob(strconv.FormatInt(s.nextJob, 10), runWebJobConfig{
		Stdout: out,
		Notice: errOut,
	})
	s.active = job
	ctx, cancel := runWebDeployContext(s.cfg.Context)
	go s.runDeployJob(ctx, draft, job)
	go func() {
		<-job.done
		cancel()
	}()
	return job, http.StatusOK, ""
}

func (s *runWebServer) runDeployJob(ctx context.Context, draft RunDraft, job *runWebJob) {
	if draft.EnvFile != "" {
		draft.EnvFileSet = true
		draft.EnvFileArg = draft.EnvFile
	}
	err := executeRunDraftWithOptionsFn(ctx, draft, s.cfg.Config, runDraftExecuteOptions{Stdout: job})
	if err != nil {
		job.finish(err)
		return
	}
	s.deployMu.Lock()
	s.complete = true
	s.deployMu.Unlock()
	job.finish(nil)
	if s.cfg.OnComplete != nil {
		go s.cfg.OnComplete()
	}
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
	case "stream":
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
	default:
		http.NotFound(w, r)
	}
}

func (s *runWebServer) handleDeployStream(w http.ResponseWriter, r *http.Request, job *runWebJob) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	lastID, _ := strconv.ParseInt(strings.TrimSpace(r.Header.Get("Last-Event-ID")), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	events, _ := job.subscribe(r.Context(), lastID)
	for ev := range events {
		if err := writeRunWebSSE(w, ev); err != nil {
			return
		}
		flusher.Flush()
	}
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
	complete := s.complete
	s.deployMu.Unlock()
	if job != nil && !complete {
		job.browserClosed()
	}
	writeRunWebJSON(w, http.StatusOK, map[string]any{"ok": true})
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
