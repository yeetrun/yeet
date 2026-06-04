// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"strings"
	"time"
)

func (e *ttyExecer) tracef(format string, args ...any) {
	if e == nil || !e.trace || e.rw == nil {
		return
	}
	if e.traceStart.IsZero() {
		e.traceStart = time.Now()
	}
	msg := fmt.Sprintf(format, args...)
	msg = strings.ReplaceAll(msg, "\n", " ")
	e.printf("trace +%s %s\n", time.Since(e.traceStart).Round(time.Millisecond), msg)
}

func (e *ttyExecer) traceBlock(label string) func() {
	if e == nil || !e.trace {
		return func() {}
	}
	start := time.Now()
	e.tracef("%s start", label)
	return func() {
		e.tracef("%s done duration=%s", label, time.Since(start).Round(time.Millisecond))
	}
}
