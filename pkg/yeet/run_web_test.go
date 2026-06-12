// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	want := []string{"svc", "ts", "lan"}
	if !reflect.DeepEqual(boot.Options.NetworkModes, want) {
		t.Fatalf("network modes = %#v, want %#v", boot.Options.NetworkModes, want)
	}
}

func TestRunWebBootstrapExposesWorkloadsAndCatalogVMImages(t *testing.T) {
	boot := newRunWebBootstrap(nil, "", "", nil)

	wantKinds := []string{"compose", "vm", "dockerfile", "remote-image", "file", "cron"}
	if got := runWebWorkloadKinds(boot.Options.Workloads); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("workload kinds = %#v, want %#v", got, wantKinds)
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
			_, _ = http.Post(deployURL.String(), "application/json", strings.NewReader(`{"service":"svc-a","host":"host-a","payload":"run.sh"}`))
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

type runWebHTTPResult struct {
	status int
	body   string
	err    error
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
