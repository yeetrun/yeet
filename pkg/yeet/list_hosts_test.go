// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type failingListHostsWriter struct {
	err error
}

func (w failingListHostsWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRenderListHosts(t *testing.T) {
	var buf bytes.Buffer
	rows := []listHostRow{
		{Host: "host-a", Version: "v0.1.0", Tags: []string{"tag:catch", "tag:app"}},
		{Host: "host-b", Version: "unknown", Tags: []string{"tag:catch"}},
	}

	if err := renderListHosts(&buf, rows); err != nil {
		t.Fatalf("renderListHosts error: %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"HOST",
		"VERSION",
		"TAGS",
		"host-a",
		"v0.1.0",
		"tag:catch,tag:app",
		"host-b",
		"unknown",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderListHosts output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderListHostsReportsFlushError(t *testing.T) {
	want := errors.New("flush failed")

	err := renderListHosts(failingListHostsWriter{err: want}, []listHostRow{{Host: "host", Version: "v0.1.0", Tags: []string{"tag:catch"}}})
	if !errors.Is(err, want) {
		t.Fatalf("renderListHosts error = %v, want %v", err, want)
	}
}
