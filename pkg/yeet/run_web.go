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
	NetworkModes  []string             `json:"networkModes"`
	SnapshotModes []string             `json:"snapshotModes"`
	Workloads     []runWebWorkloadHint `json:"workloads"`
	VMImages      []runWebVMImageHint  `json:"vmImages"`
}

type runWebWorkloadHint struct {
	Kind        string   `json:"kind"`
	Label       string   `json:"label"`
	PayloadKind string   `json:"payloadKind,omitempty"`
	Networks    []string `json:"networks,omitempty"`
	Description string   `json:"description,omitempty"`
}

type runWebVMImageHint struct {
	Payload string `json:"payload"`
	Label   string `json:"label"`
}

var openBrowserFn = openBrowser
var runWebFn = runWeb

const (
	runWebCompletionGracePeriod   = 500 * time.Millisecond
	runWebCompletionShutdownLimit = 250 * time.Millisecond
)

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
	boot := runWebBootstrap{
		CWD:          cwd,
		Hosts:        runWebHostCandidates(cfg, hostOverride),
		SelectedHost: runWebSelectedHost(cfg, hostOverride),
		Prefill:      runWebPrefillFromArgs(service, args),
		Options:      defaultRunWebOptionHints(),
	}
	if cfg != nil {
		boot.ConfigPath = cfg.Path
	}
	return boot
}

func defaultRunWebOptionHints() runWebOptionHints {
	return runWebOptionHints{
		NetworkModes:  []string{"svc", "ts", "lan"},
		SnapshotModes: []string{"inherit", "on", "off"},
		Workloads: []runWebWorkloadHint{
			{Kind: "compose", Label: "Compose app", PayloadKind: "compose", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Deploy a Docker Compose file."},
			{Kind: "vm", Label: "Virtual machine", PayloadKind: serviceTypeVM, Networks: []string{"svc", "lan"}, Description: "Create a VM from a managed catalog image."},
			{Kind: "dockerfile", Label: "Dockerfile", PayloadKind: "dockerfile", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Build and deploy a Dockerfile."},
			{Kind: "remote-image", Label: "Container image", PayloadKind: "remote-image", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Deploy an image reference."},
			{Kind: "file", Label: "Binary/script", PayloadKind: "file", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Upload and run a binary or script."},
			{Kind: serviceTypeCron, Label: "Scheduled job", PayloadKind: serviceTypeCron, Networks: []string{"host"}, Description: "Install a cron-style systemd timer."},
		},
		VMImages: []runWebVMImageHint{
			{Payload: "vm://ubuntu/26.04", Label: "Ubuntu 26.04"},
			{Payload: "vm://nixos/26.05", Label: "NixOS 26.05"},
		},
	}
}

func runWebSelectedHost(cfg *projectConfigLocation, hostOverride string) string {
	if host := strings.TrimSpace(hostOverride); host != "" {
		return host
	}
	if host := strings.TrimSpace(os.Getenv("CATCH_HOST")); host != "" {
		return host
	}
	if cfg != nil && cfg.Config != nil {
		hosts := cfg.Config.AllHosts()
		if len(hosts) != 0 {
			return hosts[0]
		}
	}
	return Host()
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
	if req.Out == nil {
		req.Out = os.Stdout
	}
	if req.Err == nil {
		req.Err = os.Stderr
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
		Out:       req.Out,
		Err:       req.Err,
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
		if err := waitRunWebCompletionGrace(ctx); err != nil {
			cancelServer()
			_ = server.Close()
			return err
		}
		shutdownRunWebServerAfterCompletion(cancelServer, server)
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

func waitRunWebCompletionGrace(ctx context.Context) error {
	timer := time.NewTimer(runWebCompletionGracePeriod)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func shutdownRunWebServerAfterCompletion(cancelServer context.CancelFunc, server *http.Server) {
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), runWebCompletionShutdownLimit)
	err := server.Shutdown(shutdownCtx)
	cancelShutdown()
	cancelServer()
	if err != nil {
		_ = server.Close()
	}
}
