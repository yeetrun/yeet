// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
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
	OnComplete func()
}

type runWebServer struct {
	cfg         runWebServerConfig
	mux         *http.ServeMux
	deployMu    sync.Mutex
	deployState runWebDeployState
}

type runWebDeployState int

const (
	runWebDeployReady runWebDeployState = iota
	runWebDeployRunning
	runWebDeployComplete
)

func newRunWebServer(cfg runWebServerConfig) http.Handler {
	s := &runWebServer{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("/api/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("/api/files", s.handleFiles)
	s.mux.HandleFunc("/api/validate", s.handleValidate)
	s.mux.HandleFunc("/api/deploy", s.handleDeploy)
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
	if ok, status, message := s.beginDeploy(); !ok {
		http.Error(w, message, status)
		return
	}
	deployed := false
	defer func() { s.finishDeploy(deployed) }()

	draft.NewServiceOnly = true
	ctx, cancel := runWebDeployContext(s.cfg.Context)
	defer cancel()
	normalized, result := validateRunDraft(ctx, draft, s.cfg.Root)
	if !result.OK {
		writeRunWebJSON(w, http.StatusBadRequest, map[string]any{"draft": redactRunWebDraftSecrets(normalized), "validation": result})
		return
	}
	if normalized.EnvFile != "" {
		normalized.EnvFileSet = true
		normalized.EnvFileArg = normalized.EnvFile
	}
	if err := executeRunDraftFn(ctx, normalized, s.cfg.Config, false); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deployed = true
	writeRunWebJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Service deployed. Close this tab and return to the terminal."})
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

func (s *runWebServer) beginDeploy() (bool, int, string) {
	s.deployMu.Lock()
	defer s.deployMu.Unlock()
	switch s.deployState {
	case runWebDeployRunning:
		return false, http.StatusConflict, "deployment already in progress"
	case runWebDeployComplete:
		return false, http.StatusConflict, "deployment already completed"
	default:
		s.deployState = runWebDeployRunning
		return true, http.StatusOK, ""
	}
}

func (s *runWebServer) finishDeploy(deployed bool) {
	s.deployMu.Lock()
	if deployed {
		s.deployState = runWebDeployComplete
	} else {
		s.deployState = runWebDeployReady
	}
	s.deployMu.Unlock()

	if deployed && s.cfg.OnComplete != nil {
		go s.cfg.OnComplete()
	}
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
