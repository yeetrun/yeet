// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"errors"
	"strings"
	"testing"
)

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
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
