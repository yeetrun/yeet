// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestTTYExecerTracefWritesElapsedLine(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{
		trace:      true,
		rw:         &out,
		traceStart: time.Now().Add(-1500 * time.Millisecond),
	}

	execer.tracef("hello %s\nworld", "trace")

	text := out.String()
	if !strings.Contains(text, "trace +") {
		t.Fatalf("trace output = %q, want elapsed prefix", text)
	}
	if !strings.Contains(text, "hello trace world") {
		t.Fatalf("trace output = %q, want sanitized message", text)
	}
}

func TestTTYExecerTracefSkipsWhenDisabled(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{rw: &out}

	execer.tracef("hidden")

	if out.Len() != 0 {
		t.Fatalf("trace output = %q, want empty", out.String())
	}
}
