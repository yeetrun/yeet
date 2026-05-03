// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type tailscaleReadErrReader struct {
	err error
}

func (r tailscaleReadErrReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestPromptTailscaleClientSecret(t *testing.T) {
	var out strings.Builder

	secret, err := promptTailscaleClientSecret(&out, strings.NewReader("tskey-client-secret\n"))
	if err != nil {
		t.Fatalf("promptTailscaleClientSecret error: %v", err)
	}
	if secret != "tskey-client-secret" {
		t.Fatalf("secret = %q, want trimmed input", secret)
	}
	if !strings.Contains(out.String(), "Tailscale OAuth setup") {
		t.Fatalf("prompt output = %q, want setup instructions", out.String())
	}
}

func TestPromptTailscaleClientSecretReportsWriteError(t *testing.T) {
	want := errors.New("write failed")

	_, err := promptTailscaleClientSecret(errWriter{err: want}, strings.NewReader("tskey-client-secret\n"))
	if !errors.Is(err, want) {
		t.Fatalf("promptTailscaleClientSecret error = %v, want %v", err, want)
	}
}

func TestPromptTailscaleClientSecretReportsReadError(t *testing.T) {
	want := errors.New("read failed")

	_, err := promptTailscaleClientSecret(io.Discard, tailscaleReadErrReader{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("promptTailscaleClientSecret error = %v, want %v", err, want)
	}
}

func TestResolveTailscaleClientSecret(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		interactive bool
		input       string
		want        string
		wantErr     string
	}{
		{name: "provided secret", secret: "  tskey-client-provided  ", want: "tskey-client-provided"},
		{name: "non tty requires secret", wantErr: "client secret is required"},
		{name: "prompts interactively", interactive: true, input: "tskey-client-prompted\n", want: "tskey-client-prompted"},
		{name: "empty prompt rejected", interactive: true, input: "\n", wantErr: "client secret is required"},
		{name: "rejects invalid prefix", secret: "not-a-client-secret", wantErr: "invalid client secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := resolveTailscaleClientSecret(tt.secret, tt.interactive, &out, strings.NewReader(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTailscaleClientSecret: %v", err)
			}
			if got != tt.want {
				t.Fatalf("secret = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTailscaleSetupFlags(t *testing.T) {
	flags, remaining, err := parseTailscaleSetupFlags([]string{"tailscale", "--setup", "--client-secret", "tskey-client-secret", "status"})
	if err != nil {
		t.Fatalf("parseTailscaleSetupFlags error: %v", err)
	}
	if !flags.Setup {
		t.Fatal("Setup = false, want true")
	}
	if flags.ClientSecret != "tskey-client-secret" {
		t.Fatalf("ClientSecret = %q", flags.ClientSecret)
	}
	if strings.Join(remaining, ",") != "status" {
		t.Fatalf("remaining = %#v", remaining)
	}
}

func TestHandleTailscaleDelegatesWithoutSetup(t *testing.T) {
	oldHandle := handleSvcCmdFn
	defer func() { handleSvcCmdFn = oldHandle }()

	wantErr := errors.New("svc failed")
	var gotArgs []string
	handleSvcCmdFn = func(args []string) error {
		gotArgs = append([]string{}, args...)
		return wantErr
	}

	err := HandleTailscale(context.Background(), []string{"status", "svc-a"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("HandleTailscale error = %v, want %v", err, wantErr)
	}
	if strings.Join(gotArgs, ",") != "status,svc-a" {
		t.Fatalf("delegated args = %#v", gotArgs)
	}
}

func TestHandleTailscaleRejectsClientSecretWithoutSetup(t *testing.T) {
	oldHandle := handleSvcCmdFn
	defer func() { handleSvcCmdFn = oldHandle }()
	handleSvcCmdFn = func([]string) error {
		t.Fatal("handleSvcCmdFn should not be called")
		return nil
	}

	err := HandleTailscale(context.Background(), []string{"--client-secret", "tskey-client-secret"})
	if err == nil || !strings.Contains(err.Error(), "--client-secret requires --setup") {
		t.Fatalf("HandleTailscale error = %v, want client-secret setup error", err)
	}
}

func TestHandleTailscaleSetupStoresSecret(t *testing.T) {
	oldCall := tailscaleSetupCallFn
	oldPrefs := loadedPrefs
	defer func() {
		tailscaleSetupCallFn = oldCall
		loadedPrefs = oldPrefs
	}()
	loadedPrefs.DefaultHost = "host-a"

	tailscaleSetupCallFn = func(_ context.Context, host string, req catchrpc.TailscaleSetupRequest) (catchrpc.TailscaleSetupResponse, error) {
		if host != "host-a" {
			t.Fatalf("host = %q, want host-a", host)
		}
		if req.ClientSecret != "tskey-client-secret" {
			t.Fatalf("ClientSecret = %q", req.ClientSecret)
		}
		return catchrpc.TailscaleSetupResponse{Verified: true, Path: "/root/.config/yeet/tailscale-client-secret"}, nil
	}

	stdout, err := captureTailscaleStdout(t, func() error {
		return HandleTailscale(context.Background(), []string{"tailscale", "--setup", "--client-secret", "tskey-client-secret"})
	})
	if err != nil {
		t.Fatalf("HandleTailscale error: %v", err)
	}
	for _, want := range []string{"host-a", "/root/.config/yeet/tailscale-client-secret"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
	}
}

func TestHandleTailscaleSetupReportsVerificationFailure(t *testing.T) {
	oldCall := tailscaleSetupCallFn
	defer func() { tailscaleSetupCallFn = oldCall }()
	tailscaleSetupCallFn = func(context.Context, string, catchrpc.TailscaleSetupRequest) (catchrpc.TailscaleSetupResponse, error) {
		return catchrpc.TailscaleSetupResponse{Verified: false}, nil
	}

	err := HandleTailscale(context.Background(), []string{"--setup", "--client-secret", "tskey-client-secret"})
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("HandleTailscale error = %v, want verification failure", err)
	}
}

func TestHandleTailscaleSetupReportsRPCError(t *testing.T) {
	oldCall := tailscaleSetupCallFn
	defer func() { tailscaleSetupCallFn = oldCall }()
	want := errors.New("rpc failed")
	tailscaleSetupCallFn = func(context.Context, string, catchrpc.TailscaleSetupRequest) (catchrpc.TailscaleSetupResponse, error) {
		return catchrpc.TailscaleSetupResponse{}, want
	}

	err := HandleTailscale(context.Background(), []string{"--setup", "--client-secret", "tskey-client-secret"})
	if !errors.Is(err, want) {
		t.Fatalf("HandleTailscale error = %v, want %v", err, want)
	}
}

func TestHandleTailscaleSetupRejectsExtraArgs(t *testing.T) {
	oldCall := tailscaleSetupCallFn
	defer func() { tailscaleSetupCallFn = oldCall }()
	tailscaleSetupCallFn = func(context.Context, string, catchrpc.TailscaleSetupRequest) (catchrpc.TailscaleSetupResponse, error) {
		t.Fatal("tailscaleSetupCallFn should not be called")
		return catchrpc.TailscaleSetupResponse{}, nil
	}

	err := HandleTailscale(context.Background(), []string{"tailscale", "--setup", "status"})
	if err == nil || !strings.Contains(err.Error(), "does not accept additional arguments") {
		t.Fatalf("HandleTailscale error = %v, want extra args error", err)
	}
}

func captureTailscaleStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout Pipe error: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
		_ = r.Close()
	}()

	runErr := fn()
	if err := w.Close(); err != nil {
		t.Fatalf("stdout close error: %v", err)
	}
	b, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("stdout ReadAll error: %v", readErr)
	}
	return string(b), runErr
}
