// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestShouldSuppressCmdOutputForDockerComposePlain(t *testing.T) {
	execer := &ttyExecer{
		progress: catchrpc.ProgressPlain,
		isPty:    false,
	}
	if !execer.shouldSuppressCmdOutput("docker", []string{"compose", "up"}) {
		t.Fatalf("expected docker compose output to be suppressed in plain mode")
	}
}

func TestShouldSuppressCmdOutputAllowsTTY(t *testing.T) {
	execer := &ttyExecer{
		progress: catchrpc.ProgressAuto,
		isPty:    true,
	}
	if execer.shouldSuppressCmdOutput("docker", []string{"compose", "up"}) {
		t.Fatalf("did not expect suppression in TTY mode")
	}
}

func TestNewCmdClearsLineForDockerComposeTTY(t *testing.T) {
	var buf bytes.Buffer
	execer := &ttyExecer{
		ctx:   context.Background(),
		rw:    &buf,
		isPty: true,
	}

	execer.newCmd("docker", "compose", "up")
	if got := buf.String(); got != "\r\033[K" {
		t.Fatalf("expected line clear prefix, got %q", got)
	}
}

func TestNewCmdDoesNotClearLineWhenNotComposeTTY(t *testing.T) {
	cases := []struct {
		name  string
		isPty bool
		cmd   string
		args  []string
	}{
		{name: "non-pty", isPty: false, cmd: "docker", args: []string{"compose", "up"}},
		{name: "non-compose", isPty: true, cmd: "docker", args: []string{"run", "alpine"}},
		{name: "non-docker", isPty: true, cmd: "echo", args: []string{"hi"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			execer := &ttyExecer{
				ctx:   context.Background(),
				rw:    &buf,
				isPty: tc.isPty,
			}

			execer.newCmd(tc.cmd, tc.args...)
			if got := buf.String(); got != "" {
				t.Fatalf("expected no line clear prefix, got %q", got)
			}
		})
	}
}
