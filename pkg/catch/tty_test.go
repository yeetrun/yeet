// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
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

func TestProgressUIIncludesHostLabel(t *testing.T) {
	var buf bytes.Buffer
	execer := &ttyExecer{
		rw:        &buf,
		isPty:     true,
		progress:  catchrpc.ProgressAuto,
		sn:        "svc-a",
		hostLabel: "yeet-hetz",
	}

	ui := execer.newProgressUI("run")
	ui.Start()

	if got := buf.String(); !strings.Contains(got, "yeet run svc-a@yeet-hetz") {
		t.Fatalf("expected host label in header, got %q", got)
	}
}

func TestShouldBypassPtyInputForCron(t *testing.T) {
	execer := &ttyExecer{
		isPty: true,
		args:  []string{"cron"},
	}
	if !execer.shouldBypassPtyInput() {
		t.Fatalf("expected cron to bypass pty input")
	}
}

func TestDockerComposeServiceCmdOverridesContextFactoryForTTY(t *testing.T) {
	server := newTestServer(t)
	composePath := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if _, _, err := server.cfg.DB.MutateService("svc-a", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 1
		s.LatestGeneration = 1
		s.Artifacts = db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(1): composePath,
					"latest":  composePath,
					"staged":  composePath,
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	var rw bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "svc-a",
		rw:  &rw,
	}

	docker, err := execer.dockerComposeServiceCmd()
	if err != nil {
		t.Fatalf("dockerComposeServiceCmd: %v", err)
	}

	cmd := docker.NewCmdContext(context.Background(), "echo", "hi")
	if cmd.Stdout != &rw {
		t.Fatalf("expected tty command context stdout to use session writer, got %T", cmd.Stdout)
	}
	if cmd.Stderr != &rw {
		t.Fatalf("expected tty command context stderr to use session writer, got %T", cmd.Stderr)
	}
	if cmd.Stdin != &rw {
		t.Fatalf("expected tty command context stdin to use session reader, got %T", cmd.Stdin)
	}
}

func TestDockerComposeServiceRunnerSetNewCmdAlsoOverridesContextFactory(t *testing.T) {
	var rw bytes.Buffer
	runner := &dockerComposeServiceRunner{
		DockerComposeService: &svc.DockerComposeService{},
	}
	runner.SetNewCmd(func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Stdin = &rw
		cmd.Stdout = &rw
		cmd.Stderr = &rw
		return cmd
	})

	if runner.NewCmdContext == nil {
		t.Fatal("expected runner to override the context-aware command factory")
	}
	cmd := runner.NewCmdContext(context.Background(), "echo", "hi")
	if cmd.Stdout != &rw {
		t.Fatalf("expected runner command context stdout to use session writer, got %T", cmd.Stdout)
	}
	if cmd.Stderr != &rw {
		t.Fatalf("expected runner command context stderr to use session writer, got %T", cmd.Stderr)
	}
	if cmd.Stdin != &rw {
		t.Fatalf("expected runner command context stdin to use session reader, got %T", cmd.Stdin)
	}
}
