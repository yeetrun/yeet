// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
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
	tryRunRemoteImageFn = func(image string, args []string) (bool, error) {
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

func TestRunWebWritesLocalhostPlaceholderWithoutOpeningBrowser(t *testing.T) {
	oldOpenBrowser := openBrowserFn
	defer func() { openBrowserFn = oldOpenBrowser }()

	var opened string
	openBrowserFn = func(rawURL string) error {
		opened = rawURL
		return nil
	}

	var out bytes.Buffer
	err := runWeb(context.Background(), runWebRequest{
		Args: []string{"./compose.yml"},
		Out:  &out,
	})
	if err == nil || !strings.Contains(err.Error(), "web run server is not implemented yet") {
		t.Fatalf("runWeb error = %v, want placeholder", err)
	}
	if opened != "" {
		t.Fatalf("openBrowserFn called with %q, want no browser side effect", opened)
	}
	rawURL := runWebPlaceholderURL(t, out.String())
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

func runWebPlaceholderURL(t *testing.T, output string) string {
	t.Helper()
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, "http://127.0.0.1:") {
			return strings.TrimSpace(field)
		}
	}
	t.Fatalf("output = %q, want localhost URL", output)
	return ""
}
