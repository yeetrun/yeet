// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type runWebRequest struct {
	Args         []string
	Config       *projectConfigLocation
	HostOverride string
	Service      string
	Out          io.Writer
	Err          io.Writer
}

type runWebBootstrap struct {
	CWD          string            `json:"cwd"`
	ConfigPath   string            `json:"configPath,omitempty"`
	Hosts        []string          `json:"hosts"`
	SelectedHost string            `json:"selectedHost"`
	Prefill      runWebPrefill     `json:"prefill"`
	Options      runWebOptionHints `json:"options"`
}

type runWebPrefill struct {
	Service string `json:"service,omitempty"`
	Payload string `json:"payload,omitempty"`
}

type runWebOptionHints struct {
	NetworkModes  []string `json:"networkModes"`
	SnapshotModes []string `json:"snapshotModes"`
}

var openBrowserFn = openBrowser
var runWebFn = runWeb

const runWebShutdownTimeout = 2 * time.Second

func extractRunWebFlag(args []string) ([]string, bool, error) {
	out := make([]string, 0, len(args))
	web := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == "--web" {
			web = true
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--web="); ok {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, false, fmt.Errorf("invalid --web value %q", value)
			}
			web = parsed
			continue
		}
		out = append(out, arg)
	}
	return out, web, nil
}

func newRunWebBootstrap(cfg *projectConfigLocation, hostOverride string, service string, args []string) runWebBootstrap {
	cwd, _ := os.Getwd()
	selected := strings.TrimSpace(hostOverride)
	if selected == "" {
		selected = Host()
	}
	boot := runWebBootstrap{
		CWD:          cwd,
		Hosts:        runWebHostCandidates(cfg, hostOverride),
		SelectedHost: selected,
		Prefill:      runWebPrefillFromArgs(service, args),
		Options: runWebOptionHints{
			NetworkModes:  []string{"svc", "ts", "lan"},
			SnapshotModes: []string{"inherit", "on", "off"},
		},
	}
	if cfg != nil {
		boot.ConfigPath = cfg.Path
	}
	return boot
}

func runWebPrefillFromArgs(service string, args []string) runWebPrefill {
	prefill := runWebPrefill{Service: strings.TrimSpace(service)}
	if len(args) == 0 {
		return prefill
	}
	if prefill.Service != "" {
		payload, runArgs, err := splitRunPayloadArgs(args)
		if err != nil {
			return prefill
		}
		if payload == prefill.Service {
			nextPayload, _, err := splitRunPayloadArgs(runArgs)
			if err == nil {
				payload = nextPayload
			}
		}
		prefill.Payload = payload
		return prefill
	}

	first, runArgs, err := splitRunPayloadArgs(args)
	if err != nil {
		return prefill
	}
	second, _, err := splitRunPayloadArgs(runArgs)
	if err != nil {
		prefill.Payload = first
		return prefill
	}
	prefill.Service = first
	prefill.Payload = second
	return prefill
}

func runWebHostCandidates(cfg *projectConfigLocation, hostOverride string) []string {
	seen := make(map[string]struct{})
	add := func(host string) {
		host = strings.TrimSpace(host)
		if host != "" {
			seen[host] = struct{}{}
		}
	}
	add(os.Getenv("CATCH_HOST"))
	add(hostOverride)
	add(Host())
	if cfg != nil && cfg.Config != nil {
		for _, host := range cfg.Config.AllHosts() {
			add(host)
		}
	}
	hosts := make([]string, 0, len(seen))
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func newRunWebToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("opening a browser is unsupported on %s", runtime.GOOS)
	}
	return cmd.Start()
}

func runWeb(ctx context.Context, req runWebRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	token, err := newRunWebToken()
	if err != nil {
		return err
	}
	csrfToken, err := newRunWebToken()
	if err != nil {
		return err
	}
	serverCtx, cancelServer := context.WithCancel(ctx)
	defer cancelServer()
	server, listener, errCh, done, url, err := startRunWebServer(serverCtx, req, token, csrfToken)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	out := req.Out
	if out == nil {
		out = os.Stdout
	}
	if _, err := fmt.Fprintf(out, "Opening %s\n", url); err != nil {
		return err
	}
	openRunWebBrowser(url, req.Err)

	return waitRunWebServer(ctx, cancelServer, server, errCh, done, out)
}

func startRunWebServer(ctx context.Context, req runWebRequest, token string, csrfToken string) (*http.Server, net.Listener, <-chan error, <-chan struct{}, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	bootstrap := newRunWebBootstrap(req.Config, req.HostOverride, req.Service, req.Args)
	cwd, err := os.Getwd()
	if err != nil {
		_ = listener.Close()
		return nil, nil, nil, nil, "", err
	}
	done := make(chan struct{})
	var doneOnce sync.Once
	handler := newRunWebServer(runWebServerConfig{
		Token:     token,
		CSRFToken: csrfToken,
		Root:      cwd,
		Bootstrap: bootstrap,
		Config:    req.Config,
		Context:   ctx,
		OnComplete: func() {
			doneOnce.Do(func() { close(done) })
		},
	})
	server := &http.Server{
		Handler: handler,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	url := fmt.Sprintf("http://%s/?token=%s", listener.Addr().String(), token)
	return server, listener, errCh, done, url, nil
}

func openRunWebBrowser(url string, errOut io.Writer) {
	if err := openBrowserFn(url); err != nil {
		if errOut == nil {
			errOut = os.Stderr
		}
		_, _ = fmt.Fprintf(errOut, "failed to open browser: %v\n", err)
	}
}

func waitRunWebServer(ctx context.Context, cancelServer context.CancelFunc, server *http.Server, errCh <-chan error, done <-chan struct{}, out io.Writer) error {
	select {
	case <-done:
		cancelServer()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), runWebShutdownTimeout)
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		cancelShutdown()
		_, _ = fmt.Fprintln(out, "Deployment finished. Close the browser tab and return here.")
		return nil
	case <-ctx.Done():
		cancelServer()
		_ = server.Close()
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
