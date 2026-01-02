// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"strings"
	"testing"
)

func TestPlainRunUIOutputsKeyValueLines(t *testing.T) {
	var buf bytes.Buffer
	ui := newRunUI(&buf, false, false, "run", "svc-a")
	ui.Start()
	ui.StartStep(runStepUpload)
	ui.DoneStep("16.61 MB @ 24.70 MB/s")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d (%q)", len(lines), buf.String())
	}
	if want := `action=run service=svc-a status=running step="Upload payload"`; lines[0] != want {
		t.Fatalf("unexpected first line: %q", lines[0])
	}
	if want := `action=run service=svc-a status=ok step="Upload payload" detail="16.61 MB @ 24.70 MB/s"`; lines[1] != want {
		t.Fatalf("unexpected second line: %q", lines[1])
	}
}
