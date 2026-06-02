// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFormatShortDuration(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "zero", in: 0, want: "0s"},
		{name: "seconds", in: 59 * time.Second, want: "59s"},
		{name: "minutes", in: 61 * time.Second, want: "1m01s"},
		{name: "hours", in: 62 * time.Minute, want: "1h02m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatShortDuration(tt.in); got != tt.want {
				t.Fatalf("formatShortDuration(%s) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestByteProgressDetailFormatting(t *testing.T) {
	got := formatByteProgressDetail(512, 1024, 256)
	want := "50% 512.00 B/1024.00 B @ 256.00 B/s ETA 2s"
	if got != want {
		t.Fatalf("formatByteProgressDetail with total = %q, want %q", got, want)
	}

	got = formatByteProgressDetail(2048, 0, 1024)
	want = "2.00 KB @ 1024.00 B/s"
	if got != want {
		t.Fatalf("formatByteProgressDetail without total = %q, want %q", got, want)
	}
}

func TestByteProgressReaderCountsBytes(t *testing.T) {
	progress := newByteProgress(5)
	n, err := io.Copy(io.Discard, progress.reader(bytes.NewBufferString("hello")))
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if n != 5 {
		t.Fatalf("copied bytes = %d, want 5", n)
	}
	if got := progress.seen.Load(); got != 5 {
		t.Fatalf("seen bytes = %d, want 5", got)
	}

	time.Sleep(time.Millisecond)
	detail := progress.finalDetail()
	if !strings.HasPrefix(detail, "5.00 B @ ") || !strings.HasSuffix(detail, "/s") {
		t.Fatalf("final detail = %q, want bytes and rate", detail)
	}
}
