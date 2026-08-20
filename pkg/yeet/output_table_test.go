// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type shortOutputTableWriter struct{}

func (shortOutputTableWriter) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

func TestRenderOutputTablePlain(t *testing.T) {
	var out bytes.Buffer
	if err := renderOutputTable(&out, []string{"SERVICE", "HOST"}, [][]string{{"web", "host-a"}}); err != nil {
		t.Fatalf("renderOutputTable error: %v", err)
	}

	const want = "SERVICE   HOST\nweb       host-a\n"
	if got := out.String(); got != want {
		t.Fatalf("renderOutputTable output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderOutputTableRejectsMismatchedRows(t *testing.T) {
	var out bytes.Buffer
	err := renderOutputTable(&out, []string{"SERVICE", "HOST"}, [][]string{{"web"}})
	if err == nil || !strings.Contains(err.Error(), "row 0 has 1 columns, want 2") {
		t.Fatalf("renderOutputTable error = %v, want column mismatch", err)
	}
	if out.Len() != 0 {
		t.Fatalf("renderOutputTable wrote partial output: %q", out.String())
	}
}

func TestRenderOutputTableReportsShortWrite(t *testing.T) {
	if err := renderOutputTable(shortOutputTableWriter{}, []string{"SERVICE"}, nil); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("renderOutputTable error = %v, want %v", err, io.ErrShortWrite)
	}
}
