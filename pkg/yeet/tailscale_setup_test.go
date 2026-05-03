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
