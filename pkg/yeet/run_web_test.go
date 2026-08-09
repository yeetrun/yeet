// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestExtractRunWebFlag(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string
		wantWeb bool
		wantErr string
	}{
		{name: "none", args: []string{"./compose.yml"}, want: []string{"./compose.yml"}},
		{name: "flag before payload", args: []string{"--web", "./compose.yml"}, want: []string{"./compose.yml"}, wantWeb: true},
		{name: "flag after payload", args: []string{"./compose.yml", "--web"}, want: []string{"./compose.yml"}, wantWeb: true},
		{name: "equals true", args: []string{"--web=true", "./compose.yml"}, want: []string{"./compose.yml"}, wantWeb: true},
		{name: "equals false", args: []string{"--web=false", "./compose.yml"}, want: []string{"./compose.yml"}},
		{name: "after terminator ignored", args: []string{"./compose.yml", "--", "--web"}, want: []string{"./compose.yml", "--", "--web"}},
		{name: "invalid bool", args: []string{"--web=wat"}, wantErr: "invalid --web value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, web, err := extractRunWebFlag(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractRunWebFlag: %v", err)
			}
			if web != tt.wantWeb {
				t.Fatalf("web = %v, want %v", web, tt.wantWeb)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("args = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRunWebTerminalProfileUsesTTYGeometryAndTerm(t *testing.T) {
	oldIsTerminal := isTerminalFn
	oldGetSize := termGetSizeFn
	t.Cleanup(func() {
		isTerminalFn = oldIsTerminal
		termGetSizeFn = oldGetSize
	})
	t.Setenv("TERM", "xterm-256color")
	isTerminalFn = func(fd int) bool {
		return fd == 42
	}
	termGetSizeFn = func(fd int) (int, int, error) {
		if fd != 42 {
			t.Fatalf("termGetSizeFn fd = %d, want 42", fd)
		}
		return 120, 40, nil
	}

	got := currentRunWebTerminalProfile(42)
	want := runWebTerminalProfile{
		TTY: true, Cols: 120, Rows: 40, Term: "xterm-256color", Scrollback: runWebTerminalScrollback,
	}
	if got != want {
		t.Fatalf("currentRunWebTerminalProfile = %#v, want %#v", got, want)
	}
}

func TestRunWebTerminalProfileUsesStableNonTTYGeometry(t *testing.T) {
	oldIsTerminal := isTerminalFn
	oldGetSize := termGetSizeFn
	t.Cleanup(func() {
		isTerminalFn = oldIsTerminal
		termGetSizeFn = oldGetSize
	})
	t.Setenv("TERM", "ignored")
	isTerminalFn = func(int) bool { return false }
	termGetSizeFn = func(int) (int, int, error) {
		t.Fatal("termGetSizeFn called for non-TTY")
		return 0, 0, nil
	}

	got := currentRunWebTerminalProfile(42)
	want := runWebTerminalProfile{Cols: 80, Rows: 24, Scrollback: runWebTerminalScrollback}
	if got != want {
		t.Fatalf("currentRunWebTerminalProfile = %#v, want %#v", got, want)
	}
}

func TestRunWebTerminalProfileFallsBackWhenSizeLookupFails(t *testing.T) {
	oldIsTerminal := isTerminalFn
	oldGetSize := termGetSizeFn
	t.Cleanup(func() {
		isTerminalFn = oldIsTerminal
		termGetSizeFn = oldGetSize
	})
	t.Setenv("TERM", "screen-256color")
	isTerminalFn = func(int) bool { return true }
	termGetSizeFn = func(int) (int, int, error) {
		return 0, 0, errors.New("no terminal size")
	}

	got := currentRunWebTerminalProfile(42)
	want := runWebTerminalProfile{
		TTY: true, Cols: 80, Rows: 24, Term: "screen-256color", Scrollback: runWebTerminalScrollback,
	}
	if got != want {
		t.Fatalf("currentRunWebTerminalProfile = %#v, want %#v", got, want)
	}
}

func TestRunWebBootstrapUsesProjectHostsEnvAndPrefs(t *testing.T) {
	oldPrefs := loadedPrefs
	oldService := serviceOverride
	defer func() { loadedPrefs = oldPrefs; serviceOverride = oldService }()
	loadedPrefs.DefaultHost = "prefs-host"
	serviceOverride = "global-svc"
	t.Setenv("CATCH_HOST", "env-host")
	cfg := &projectConfigLocation{
		Dir: t.TempDir(),
		Config: &ProjectConfig{
			Version:  projectConfigVersion,
			Hosts:    []string{"toml-host"},
			Services: []ServiceEntry{{Name: "svc-a", Host: "service-host"}},
		},
	}
	boot := newRunWebBootstrap(cfg, "override-host", "svc-a", []string{"svc-a", "./compose.yml"})
	wantHosts := []string{"env-host", "override-host", "prefs-host", "service-host", "toml-host"}
	if !reflect.DeepEqual(boot.Hosts, wantHosts) {
		t.Fatalf("hosts = %#v, want %#v", boot.Hosts, wantHosts)
	}
	if boot.SelectedHost != "override-host" {
		t.Fatalf("SelectedHost = %q, want override-host", boot.SelectedHost)
	}
	if boot.Prefill.Service != "svc-a" || boot.Prefill.Payload != "./compose.yml" {
		t.Fatalf("Prefill = %#v, want service/payload", boot.Prefill)
	}
}

func TestRunWebBootstrapSelectsProjectHostBeforeDefaultPrefs(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch"
	t.Setenv("CATCH_HOST", "")
	cfg := &projectConfigLocation{
		Dir: t.TempDir(),
		Config: &ProjectConfig{
			Version: projectConfigVersion,
			Hosts:   []string{"yeet-lab", "yeet-cloud"},
		},
	}

	boot := newRunWebBootstrap(cfg, "", "", nil)

	if boot.SelectedHost != "yeet-cloud" {
		t.Fatalf("SelectedHost = %q, want first project host yeet-cloud", boot.SelectedHost)
	}
}

func TestRunWebBootstrapNetworkModesMatchCatchModes(t *testing.T) {
	boot := newRunWebBootstrap(nil, "", "", nil)
	want := []string{"svc", "ts", "lan", "iso"}
	if !reflect.DeepEqual(boot.Options.NetworkModes, want) {
		t.Fatalf("network modes = %#v, want %#v", boot.Options.NetworkModes, want)
	}
}

func TestRunWebBootstrapISOCompatibilityEnums(t *testing.T) {
	boot := newRunWebBootstrap(nil, "", "", nil)
	wantISO := map[string]bool{
		"compose":      true,
		"vm":           true,
		"dockerfile":   true,
		"remote-image": true,
		"python":       true,
		"typescript":   true,
		"file":         true,
	}
	for _, workload := range boot.Options.Workloads {
		gotISO := false
		for _, mode := range workload.Networks {
			gotISO = gotISO || mode == "iso"
		}
		if gotISO != wantISO[workload.Kind] {
			t.Fatalf("workload %q networks = %#v, ISO = %v, want %v", workload.Kind, workload.Networks, gotISO, wantISO[workload.Kind])
		}
	}
}

func TestRunWebBootstrapExposesWorkloadsAndCatalogVMImages(t *testing.T) {
	boot := newRunWebBootstrap(nil, "", "", nil)

	wantKinds := []string{"compose", "vm", "dockerfile", "remote-image", "python", "typescript", "file"}
	if got := runWebWorkloadKinds(boot.Options.Workloads); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("workload kinds = %#v, want %#v", got, wantKinds)
	}
	fileHint := boot.Options.Workloads[len(boot.Options.Workloads)-1]
	if !reflect.DeepEqual(fileHint.Networks, []string{"host", "svc", "ts", "lan", "iso"}) {
		t.Fatalf("file networks = %#v, want host, svc, ts, lan, iso", fileHint.Networks)
	}
	if fileHint.Description != "Upload and run, or schedule, a native binary or script." {
		t.Fatalf("file description = %q", fileHint.Description)
	}
	if len(boot.Options.VMImages) != 2 {
		t.Fatalf("VMImages = %#v, want ubuntu and nixos catalog images", boot.Options.VMImages)
	}
	if boot.Options.VMImages[0].Payload != "vm://ubuntu/26.04" || boot.Options.VMImages[0].Label != "Ubuntu 26.04" {
		t.Fatalf("first VM image = %#v, want Ubuntu 26.04", boot.Options.VMImages[0])
	}
	if boot.Options.VMImages[1].Payload != "vm://nixos/26.05" || boot.Options.VMImages[1].Label != "NixOS 26.05" {
		t.Fatalf("second VM image = %#v, want NixOS 26.05", boot.Options.VMImages[1])
	}
}

func runWebWorkloadKinds(workloads []runWebWorkloadHint) []string {
	out := make([]string, 0, len(workloads))
	for _, workload := range workloads {
		out = append(out, workload.Kind)
	}
	return out
}

func TestRunWebBootstrapPrefillUsesRequestServiceAndRunFlags(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()
	serviceOverride = "global-svc"

	boot := newRunWebBootstrap(nil, "", "svc-a", []string{"--net=svc", "./compose.yml"})
	if boot.Prefill.Service != "svc-a" {
		t.Fatalf("Prefill.Service = %q, want request service", boot.Prefill.Service)
	}
	if boot.Prefill.Payload != "./compose.yml" {
		t.Fatalf("Prefill.Payload = %q, want flag-aware payload", boot.Prefill.Payload)
	}
}

func TestRunWebBootstrapPrefillKeepsPayloadMatchingService(t *testing.T) {
	boot := newRunWebBootstrap(nil, "", "redis", []string{"redis"})
	if boot.Prefill.Service != "redis" {
		t.Fatalf("Prefill.Service = %q, want redis", boot.Prefill.Service)
	}
	if boot.Prefill.Payload != "redis" {
		t.Fatalf("Prefill.Payload = %q, want redis", boot.Prefill.Payload)
	}
}

func TestSvcRunWebRoutesToLocalWeb(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"
	cfg := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
	tryRunRemoteImageFn = func(ctx context.Context, image string, args []string) (bool, error) {
		t.Fatalf("tryRunRemoteImageFn called for web run image=%q args=%#v", image, args)
		return false, nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn called for web run service=%q args=%#v", service, args)
		return nil
	}

	var got runWebRequest
	var called bool
	runWebFn = func(ctx context.Context, req runWebRequest) error {
		called = true
		got = req
		return nil
	}

	req := svcCommandRequest{
		Config:       cfg,
		HostOverride: "override-host",
		Service:      "svc-a",
		Command: svcCommand{
			Name:    "run",
			Args:    []string{"--web", "./compose.yml"},
			RawArgs: []string{"run", "--web", "./compose.yml"},
		},
	}
	if err := handleSvcRun(req); err != nil {
		t.Fatalf("handleSvcRun web error = %v", err)
	}
	if !called {
		t.Fatal("runWebFn was not called")
	}
	if !reflect.DeepEqual(got.Args, []string{"./compose.yml"}) {
		t.Fatalf("runWeb args = %#v, want payload only", got.Args)
	}
	if got.Service != "svc-a" {
		t.Fatalf("runWeb service = %q, want svc-a", got.Service)
	}
	if got.Config != cfg {
		t.Fatalf("runWeb config = %#v, want original config", got.Config)
	}
	if got.HostOverride != "override-host" {
		t.Fatalf("runWeb host override = %q, want override-host", got.HostOverride)
	}
	if got.Out == nil || got.Err == nil {
		t.Fatalf("runWeb writers = out:%v err:%v, want non-nil", got.Out, got.Err)
	}
}

func TestRunWebStartsLocalhostServerAndOpensBrowser(t *testing.T) {
	oldOpenBrowser := openBrowserFn
	defer func() { openBrowserFn = oldOpenBrowser }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var opened string
	var openErr error
	openBrowserFn = func(rawURL string) error {
		opened = rawURL
		resp, err := http.Get(rawURL)
		if err != nil {
			openErr = err
			cancel()
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			openErr = errors.New(resp.Status)
		}
		cancel()
		return nil
	}

	var out bytes.Buffer
	err := runWeb(ctx, runWebRequest{
		Args: []string{"./compose.yml"},
		Out:  &out,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runWeb error = %v, want context canceled", err)
	}
	if opened != "" {
		if openErr != nil {
			t.Fatalf("open browser probe: %v", openErr)
		}
	} else {
		t.Fatal("openBrowserFn was not called")
	}
	rawURL := runWebOpeningURL(t, out.String())
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, parseErr)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		t.Fatalf("placeholder URL = %q, want localhost http URL", rawURL)
	}
	if parsed.Port() == "" {
		t.Fatalf("placeholder URL = %q, want allocated port", rawURL)
	}
	if token := parsed.Query().Get("token"); len(token) != 48 {
		t.Fatalf("token length = %d, want 48", len(token))
	}
}

func TestRunWebOpensBrowserForAlreadyCanceledContext(t *testing.T) {
	oldOpenBrowser := openBrowserFn
	defer func() { openBrowserFn = oldOpenBrowser }()

	var opened string
	openBrowserFn = func(rawURL string) error {
		opened = rawURL
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := runWeb(ctx, runWebRequest{Out: &out, Err: io.Discard})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runWeb error = %v, want context canceled", err)
	}
	if !strings.HasPrefix(opened, "http://127.0.0.1:") {
		t.Fatalf("opened = %q, want localhost URL", opened)
	}
	if !strings.Contains(out.String(), "Opening http://127.0.0.1:") {
		t.Fatalf("out = %q, want opening line", out.String())
	}
}

func TestRunWebReturnsAfterSuccessfulDeploy(t *testing.T) {
	oldOpenBrowser := openBrowserFn
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		openBrowserFn = oldOpenBrowser
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return nil
	}

	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	postResult := make(chan runWebHTTPResult, 1)
	openBrowserFn = func(rawURL string) error {
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				postResult <- runWebHTTPResult{err: err}
				return
			}
			parsed.Path = "/api/deploy"
			resp, err := http.Post(parsed.String(), "application/json", strings.NewReader(`{"service":"svc-a","host":"host-a","payload":"run.sh"}`))
			if err != nil {
				postResult <- runWebHTTPResult{err: err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				var started runWebDeployStartedResponse
				if decodeErr := json.Unmarshal(body, &started); decodeErr != nil {
					err = decodeErr
				} else {
					err = acknowledgeRunWebJobAfterSuccess(parsed, started.JobID)
				}
			}
			postResult <- runWebHTTPResult{status: resp.StatusCode, body: string(body), err: err}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out bytes.Buffer
	err := runWeb(ctx, runWebRequest{Out: &out, Err: io.Discard})
	if err != nil {
		t.Fatalf("runWeb error = %v, want nil", err)
	}
	result := readRunWebHTTPResult(t, postResult)
	if result.err != nil {
		t.Fatalf("deploy post error = %v", result.err)
	}
	if result.status != http.StatusOK {
		t.Fatalf("deploy post status = %d body=%s, want 200", result.status, result.body)
	}
	if !strings.Contains(result.body, `"ok":true`) || !strings.Contains(result.body, `"jobId":"`) {
		t.Fatalf("deploy post body = %s, want job-start response", result.body)
	}
	if !strings.Contains(out.String(), "Deployment finished") {
		t.Fatalf("output = %q, want deployment finished message", out.String())
	}
}

func TestRunWebKeepsServerAliveForTerminalStatusAfterFastDeploy(t *testing.T) {
	oldOpenBrowser := openBrowserFn
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		openBrowserFn = oldOpenBrowser
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
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	streamResult := make(chan runWebHTTPResult, 1)
	openBrowserFn = func(rawURL string) error {
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				streamResult <- runWebHTTPResult{err: err}
				return
			}
			deployURL := *parsed
			deployURL.Path = "/api/deploy"
			resp, err := http.Post(deployURL.String(), "application/json", strings.NewReader(`{"service":"svc-a","host":"host-a","payload":"run.sh"}`))
			if err != nil {
				streamResult <- runWebHTTPResult{err: err}
				return
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				streamResult <- runWebHTTPResult{err: readErr}
				return
			}
			if resp.StatusCode != http.StatusOK {
				streamResult <- runWebHTTPResult{status: resp.StatusCode, body: string(body)}
				return
			}
			var started runWebDeployStartedResponse
			if err := json.Unmarshal(body, &started); err != nil {
				streamResult <- runWebHTTPResult{err: err, body: string(body)}
				return
			}
			time.Sleep(100 * time.Millisecond)
			streamURL := *parsed
			streamURL.Path = "/api/deploy/" + url.PathEscape(started.JobID) + "/stream"
			resp, err = http.Get(streamURL.String())
			if err != nil {
				streamResult <- runWebHTTPResult{err: err}
				return
			}
			body, readErr = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil {
				readErr = acknowledgeRunWebJobAfterSuccess(parsed, started.JobID)
			}
			streamResult <- runWebHTTPResult{status: resp.StatusCode, body: string(body), err: readErr}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out bytes.Buffer
	err := runWeb(ctx, runWebRequest{Out: &out, Err: io.Discard})
	if err != nil {
		t.Fatalf("runWeb error = %v, want nil", err)
	}
	result := readRunWebHTTPResult(t, streamResult)
	if result.err != nil {
		t.Fatalf("stream error = %v", result.err)
	}
	if result.status != http.StatusOK {
		t.Fatalf("stream status = %d body=%s, want 200", result.status, result.body)
	}
	events := parseRunWebSSE(t, result.body)
	output := decodeRunWebOutputText(t, events)
	if !strings.Contains(output, "deploying\n") {
		t.Fatalf("stream output = %q, want deploying line", output)
	}
	last := events[len(events)-1]
	if last.Name != "status" || !strings.Contains(last.Data, `"state":"succeeded"`) {
		t.Fatalf("last event = %#v, want succeeded status", last)
	}
}

func TestWaitRunWebServerDrainsDoneResponseBeforeShutdown(t *testing.T) {
	done := make(chan struct{})
	var doneOnce sync.Once
	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			doneOnce.Do(func() { close(done) })
			close(handlerStarted)
			<-handlerRelease
			_, _ = io.WriteString(w, "ok")
		}),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	getResult := make(chan runWebHTTPResult, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			getResult <- runWebHTTPResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		getResult <- runWebHTTPResult{status: resp.StatusCode, body: string(body), err: err}
	}()

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler to start")
	}
	go func() {
		time.Sleep(25 * time.Millisecond)
		close(handlerRelease)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	err = waitRunWebServer(ctx, cancel, server, errCh, done, io.Discard)
	if err != nil {
		t.Fatalf("waitRunWebServer error = %v, want nil", err)
	}
	result := readRunWebHTTPResult(t, getResult)
	if result.err != nil {
		t.Fatalf("get error = %v", result.err)
	}
	if result.status != http.StatusOK || result.body != "ok" {
		t.Fatalf("get result = status %d body %q, want 200 ok", result.status, result.body)
	}
}

func TestWaitRunWebServerCommitsFallbackAckBeforeClosingListener(t *testing.T) {
	const completionFallback = 25 * time.Millisecond

	testCtx, cancelTest := context.WithCancel(context.Background())
	done := make(chan struct{})
	handler := newRunWebServer(runWebServerConfig{
		Token:                "secret",
		Root:                 t.TempDir(),
		CompletionAckTimeout: completionFallback,
		OnComplete:           func() { close(done) },
	}).(*runWebServer)
	job := mustNewRunWebJob(t, runWebJobConfig{})
	job.finish(nil)
	handler.active = job

	writeHeaderReached := make(chan struct{})
	releaseWriteHeader := make(chan struct{})
	var writeHeaderReachedOnce sync.Once
	var releaseWriteHeaderOnce sync.Once
	releaseCommitGate := func() {
		releaseWriteHeaderOnce.Do(func() { close(releaseWriteHeader) })
	}
	order := newRunWebShutdownOrder()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writer := &runWebFlushBarrierWriter{
				ResponseWriter: w,
				ctx:            r.Context(),
				cleanup:        testCtx.Done(),
				release:        releaseWriteHeader,
				writeHeaderReached: func() {
					writeHeaderReachedOnce.Do(func() { close(writeHeaderReached) })
				},
				order: order,
			}
			handler.ServeHTTP(writer, r)
		}),
	}
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listener := &runWebOrderedListener{Listener: rawListener, order: order}
	t.Cleanup(func() {
		releaseCommitGate()
		cancelTest()
		_ = server.Close()
		_ = listener.Close()
		_ = handler.close()
	})
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	ackResult := make(chan runWebHTTPResult, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/api/deploy/job-a/ack", nil)
		if err != nil {
			ackResult <- runWebHTTPResult{err: err}
			return
		}
		req.Header.Set("X-Yeet-Run-Token", "secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			ackResult <- runWebHTTPResult{err: err}
			return
		}
		defer resp.Body.Close()
		_, readErr := io.ReadAll(resp.Body)
		if readErr == nil && resp.StatusCode == http.StatusNoContent {
			order.markClientNoContent()
		}
		ackResult <- runWebHTTPResult{status: resp.StatusCode, err: readErr}
	}()

	select {
	case <-writeHeaderReached:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for acknowledgement handler to reach the 204 response")
	}

	go handler.awaitSuccessfulRender(job)
	ctx, cancel := context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() {
		err := waitRunWebServer(ctx, cancel, server, errCh, done, io.Discard)
		order.markWaitReturned()
		waitResult <- err
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for injected 25ms completion fallback")
	}
	select {
	case <-job.acknowledged():
		t.Fatal("completion fallback was not the source of done; acknowledgement was released while WriteHeader was gated")
	default:
	}

	releaseCommitGate()
	select {
	case err := <-waitResult:
		t.Fatalf("waitRunWebServer returned before delegated acknowledgement flush: %v", err)
	case <-order.flushed:
	}
	select {
	case err := <-waitResult:
		t.Fatalf("waitRunWebServer returned before the real HTTP client received 204: %v", err)
	case ack := <-ackResult:
		if ack.err != nil {
			t.Fatalf("acknowledgement request: %v", ack.err)
		}
		if ack.status != http.StatusNoContent {
			t.Fatalf("acknowledgement status = %d, want 204", ack.status)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the real HTTP client to receive 204")
	}

	select {
	case err := <-waitResult:
		if err != nil {
			t.Fatalf("waitRunWebServer error = %v, want nil", err)
		}
	case <-time.After(runWebCompletionGracePeriod + 250*time.Millisecond):
		t.Fatal("waitRunWebServer did not finish within the normal post-flush completion grace period")
	}
	select {
	case <-order.listenerClosed:
	default:
		t.Fatal("waitRunWebServer returned before closing the real listener")
	}
	gotOrder := order.snapshot()
	if gotOrder.listenerClosedBeforeFlush {
		t.Fatal("real listener Close ran before delegated acknowledgement Flush returned")
	}
	if gotOrder.waitReturnedBeforeFlush {
		t.Fatal("waitRunWebServer returned before delegated acknowledgement Flush returned")
	}
	if gotOrder.waitReturnedBeforeClientNoContent {
		t.Fatal("waitRunWebServer returned before the real HTTP client received 204")
	}
}

type runWebShutdownOrderSnapshot struct {
	listenerClosedBeforeFlush         bool
	waitReturnedBeforeFlush           bool
	waitReturnedBeforeClientNoContent bool
}

type runWebShutdownOrder struct {
	mu sync.Mutex

	flushComplete   bool
	clientNoContent bool

	listenerClosedBeforeFlush         bool
	waitReturnedBeforeFlush           bool
	waitReturnedBeforeClientNoContent bool

	flushed        chan struct{}
	listenerClosed chan struct{}
	flushOnce      sync.Once
	clientOnce     sync.Once
	listenerOnce   sync.Once
	waitOnce       sync.Once
}

func newRunWebShutdownOrder() *runWebShutdownOrder {
	return &runWebShutdownOrder{
		flushed:        make(chan struct{}),
		listenerClosed: make(chan struct{}),
	}
}

func (o *runWebShutdownOrder) markFlushed() {
	o.flushOnce.Do(func() {
		o.mu.Lock()
		o.flushComplete = true
		o.mu.Unlock()
		close(o.flushed)
	})
}

func (o *runWebShutdownOrder) markClientNoContent() {
	o.clientOnce.Do(func() {
		o.mu.Lock()
		o.clientNoContent = true
		o.mu.Unlock()
	})
}

func (o *runWebShutdownOrder) markListenerClosed() {
	o.listenerOnce.Do(func() {
		o.mu.Lock()
		o.listenerClosedBeforeFlush = !o.flushComplete
		o.mu.Unlock()
		close(o.listenerClosed)
	})
}

func (o *runWebShutdownOrder) markWaitReturned() {
	o.waitOnce.Do(func() {
		o.mu.Lock()
		o.waitReturnedBeforeFlush = !o.flushComplete
		o.waitReturnedBeforeClientNoContent = !o.clientNoContent
		o.mu.Unlock()
	})
}

func (o *runWebShutdownOrder) snapshot() runWebShutdownOrderSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return runWebShutdownOrderSnapshot{
		listenerClosedBeforeFlush:         o.listenerClosedBeforeFlush,
		waitReturnedBeforeFlush:           o.waitReturnedBeforeFlush,
		waitReturnedBeforeClientNoContent: o.waitReturnedBeforeClientNoContent,
	}
}

type runWebOrderedListener struct {
	net.Listener
	order *runWebShutdownOrder
}

func (l *runWebOrderedListener) Close() error {
	l.order.markListenerClosed()
	return l.Listener.Close()
}

type runWebFlushBarrierWriter struct {
	http.ResponseWriter
	ctx                context.Context
	cleanup            <-chan struct{}
	release            <-chan struct{}
	writeHeaderReached func()
	order              *runWebShutdownOrder
}

func (w *runWebFlushBarrierWriter) WriteHeader(status int) {
	if status == http.StatusNoContent {
		if w.writeHeaderReached != nil {
			w.writeHeaderReached()
		}
		select {
		case <-w.release:
		case <-w.ctx.Done():
			return
		case <-w.cleanup:
			return
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *runWebFlushBarrierWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	w.order.markFlushed()
}

func TestRunWebReturnsAfterDeployWithActiveValidate(t *testing.T) {
	oldOpenBrowser := openBrowserFn
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		openBrowserFn = oldOpenBrowser
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()

	validateStarted := make(chan struct{})
	var validateOnce sync.Once
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if service == "stuck-validate" {
			validateOnce.Do(func() { close(validateStarted) })
			<-ctx.Done()
			return catchrpc.ServiceInfoResponse{}, ctx.Err()
		}
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return nil
	}

	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	openBrowserFn = func(rawURL string) error {
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				return
			}
			validateURL := *parsed
			validateURL.Path = "/api/validate"
			go func() {
				_, _ = http.Post(validateURL.String(), "application/json", strings.NewReader(`{"service":"stuck-validate","host":"host-a","payload":"run.sh"}`))
			}()
			select {
			case <-validateStarted:
			case <-time.After(time.Second):
				return
			}
			deployURL := *parsed
			deployURL.Path = "/api/deploy"
			resp, err := http.Post(deployURL.String(), "application/json", strings.NewReader(`{"service":"svc-a","host":"host-a","payload":"run.sh"}`))
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var started runWebDeployStartedResponse
			if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
				return
			}
			_ = acknowledgeRunWebJobAfterSuccess(parsed, started.JobID)
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out bytes.Buffer
	err := runWeb(ctx, runWebRequest{Out: &out, Err: io.Discard})
	if err != nil {
		t.Fatalf("runWeb error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "Deployment finished") {
		t.Fatalf("output = %q, want deployment finished message", out.String())
	}
}

func TestStartRunWebServerWiresCurrentStdinProfileAndSeparateResizeWatcher(t *testing.T) {
	oldProfile := currentRunWebTerminalProfileFn
	oldResize := watchRunWebResizeFn
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		currentRunWebTerminalProfileFn = oldProfile
		watchRunWebResizeFn = oldResize
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(context.Context, RunDraft, *projectConfigLocation, runDraftExecuteOptions) error {
		return nil
	}
	profile := runWebTerminalProfile{
		TTY: true, Cols: 101, Rows: 37, Term: "xterm-test", Scrollback: 1000,
	}
	var gotProfileFD, gotResizeFD int
	currentRunWebTerminalProfileFn = func(fd int) runWebTerminalProfile {
		gotProfileFD = fd
		return profile
	}
	watchRunWebResizeFn = func(ctx context.Context, fd int) <-chan catchrpc.Resize {
		if ctx == nil {
			t.Fatal("resize watcher received nil context")
		}
		gotResizeFD = fd
		ch := make(chan catchrpc.Resize)
		close(ch)
		return ch
	}

	root := t.TempDir()
	t.Chdir(root)
	writeRunWebTestPayload(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, listener, errCh, _, _, err := startRunWebServer(ctx, runWebRequest{Out: io.Discard, Err: io.Discard}, "secret", "csrf")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer server.Close()

	rec := runWebAPIRequest(t, server.Handler, http.MethodPost, "/api/deploy", RunDraft{
		Service: "svc-a", Host: "host-a", Payload: "run.sh",
	})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, server.Handler, jobID, runWebJobSucceeded)
	stream := runWebAPIRequest(t, server.Handler, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	events := parseRunWebSSE(t, stream.Body.String())
	if len(events) == 0 || events[0].Name != "terminal" ||
		events[0].Data != `{"tty":true,"cols":101,"rows":37,"term":"xterm-test","scrollback":1000}` {
		t.Fatalf("first event = %#v, want current stdin profile", events)
	}
	wantFD := int(os.Stdin.Fd())
	if gotProfileFD != wantFD || gotResizeFD != wantFD {
		t.Fatalf("profile fd = %d resize fd = %d, want stdin fd %d", gotProfileFD, gotResizeFD, wantFD)
	}

	cancel()
	_ = server.Close()
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server close")
	}
}

func TestStartRunWebServerGracefulAndHardCloseRemoveActiveJournal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(*http.Server) error
	}{
		{
			name: "graceful shutdown",
			close: func(server *http.Server) error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				return server.Shutdown(ctx)
			},
		},
		{name: "hard close", close: func(server *http.Server) error { return server.Close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldInfo := fetchRunDraftServiceInfoFn
			oldExecDraft := executeRunDraftWithOptionsFn
			oldResize := watchRunWebResizeFn
			defer func() {
				fetchRunDraftServiceInfoFn = oldInfo
				executeRunDraftWithOptionsFn = oldExecDraft
				watchRunWebResizeFn = oldResize
			}()
			fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
				return catchrpc.ServiceInfoResponse{Found: false}, nil
			}
			executeRunDraftWithOptionsFn = func(context.Context, RunDraft, *projectConfigLocation, runDraftExecuteOptions) error {
				return nil
			}
			watchRunWebResizeFn = func(context.Context, int) <-chan catchrpc.Resize {
				ch := make(chan catchrpc.Resize)
				close(ch)
				return ch
			}

			root := t.TempDir()
			journalDir := t.TempDir()
			t.Setenv("TMPDIR", journalDir)
			t.Chdir(root)
			writeRunWebTestPayload(t, root)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server, listener, errCh, _, _, err := startRunWebServer(ctx, runWebRequest{Out: io.Discard, Err: io.Discard}, "secret", "csrf")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

			rec := runWebAPIRequest(t, server.Handler, http.MethodPost, "/api/deploy", RunDraft{
				Service: "svc-a", Host: "host-a", Payload: "run.sh",
			})
			jobID := decodeRunWebDeployStarted(t, rec).JobID
			waitRunWebJobState(t, server.Handler, jobID, runWebJobSucceeded)
			paths, err := filepath.Glob(filepath.Join(journalDir, "yeet-web-run-*.journal"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("journal paths = %#v, %v; want one active journal", paths, err)
			}

			if err := tc.close(server); err != nil {
				t.Fatalf("close server: %v", err)
			}
			select {
			case <-errCh:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for server close")
			}
			if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal after server close = %v, want removed", err)
			}
		})
	}
}

type runWebHTTPResult struct {
	status int
	body   string
	err    error
}

func acknowledgeRunWebJobAfterSuccess(base *url.URL, jobID string) error {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		statusURL := *base
		statusURL.Path = "/api/deploy/" + url.PathEscape(jobID) + "/status"
		resp, err := http.Get(statusURL.String())
		if err != nil {
			return err
		}
		var status runWebJobStatus
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return decodeErr
		}
		if resp.StatusCode == http.StatusOK && status.State == runWebJobSucceeded {
			ackURL := *base
			ackURL.Path = "/api/deploy/" + url.PathEscape(jobID) + "/ack"
			req, err := http.NewRequest(http.MethodPost, ackURL.String(), nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				return fmt.Errorf("ack status = %d, want 204", resp.StatusCode)
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("timed out waiting to acknowledge successful job")
}

func readRunWebHTTPResult(t *testing.T, ch <-chan runWebHTTPResult) runWebHTTPResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for HTTP result")
		return runWebHTTPResult{}
	}
}

func runWebOpeningURL(t *testing.T, output string) string {
	t.Helper()
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, "http://127.0.0.1:") {
			return strings.TrimSpace(field)
		}
	}
	t.Fatalf("output = %q, want localhost URL", output)
	return ""
}
