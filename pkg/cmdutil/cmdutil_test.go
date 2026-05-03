// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmdutil

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestConfirmAcceptsY(t *testing.T) {
	var out bytes.Buffer
	ok, err := Confirm(strings.NewReader("y\n"), &out, "Continue?")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !ok {
		t.Fatalf("expected confirmation to be accepted")
	}
	if out.String() != "Continue? [y/N]: " {
		t.Fatalf("prompt = %q", out.String())
	}
}

func TestConfirmRejectsDefaultAndOtherAnswers(t *testing.T) {
	for _, input := range []string{"\n", "n\n", "yes\n"} {
		t.Run(input, func(t *testing.T) {
			ok, err := Confirm(strings.NewReader(input), io.Discard, "Continue?")
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if ok {
				t.Fatalf("expected %q to be rejected", input)
			}
		})
	}
}

func TestConfirmPropagatesPromptWriteError(t *testing.T) {
	wantErr := errors.New("write failed")
	_, err := Confirm(strings.NewReader("y\n"), failingWriter{err: wantErr}, "Continue?")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Confirm error = %v, want %v", err, wantErr)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
