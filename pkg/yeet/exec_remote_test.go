// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"fmt"
	"testing"
)

func TestErrorPrefixForRemoteExitRawNewline(t *testing.T) {
	if got := errorPrefixForRemoteExit(true, '\n', true); got != "\r" {
		t.Fatalf("expected carriage return prefix, got %q", got)
	}
}

func TestPrintCLIErrorIncludesPrefix(t *testing.T) {
	buf := new(bytes.Buffer)
	PrintCLIError(buf, remoteExitError{code: 1, prefix: "\r"})
	if got := buf.String(); got != "\rremote exit 1\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestPrintCLIErrorIncludesPrefixWhenWrapped(t *testing.T) {
	buf := new(bytes.Buffer)
	err := fmt.Errorf("failed: %w", remoteExitError{code: 2, prefix: "\r"})
	PrintCLIError(buf, err)
	if got := buf.String(); got != "\rfailed: remote exit 2\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}
