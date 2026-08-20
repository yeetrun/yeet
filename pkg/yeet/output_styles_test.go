// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"strings"
	"testing"
)

type fdBuffer struct {
	bytes.Buffer
	fd uintptr
}

func (w *fdBuffer) Fd() uintptr { return w.fd }

func stripHeadingANSI(text string) string {
	return strings.NewReplacer("\x1b[1;36m", "", "\x1b[m", "").Replace(text)
}

func TestOutputStylesWriterPolicy(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	oldIsTerminal := isTerminalFn
	t.Cleanup(func() { isTerminalFn = oldIsTerminal })

	for _, tc := range []struct {
		name       string
		writer     interface{ Write([]byte) (int, error) }
		terminal   bool
		noColor    string
		wantEnable bool
	}{
		{name: "ordinary buffer", writer: &bytes.Buffer{}, wantEnable: false},
		{name: "terminal descriptor", writer: &fdBuffer{fd: 42}, terminal: true, wantEnable: true},
		{name: "non-terminal descriptor", writer: &fdBuffer{fd: 42}, wantEnable: false},
		{name: "no color", writer: &fdBuffer{fd: 42}, terminal: true, noColor: "1", wantEnable: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			isTerminalFn = func(fd int) bool { return tc.terminal && fd == 42 }

			if got := outputStyles(tc.writer).Enabled(); got != tc.wantEnable {
				t.Fatalf("outputStyles().Enabled() = %v, want %v", got, tc.wantEnable)
			}
		})
	}
}
