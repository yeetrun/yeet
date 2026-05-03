// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func TestShouldSuppressCmdOutputAllowsDockerComposeLogsInPlainMode(t *testing.T) {
	execer := &ttyExecer{
		progress: catchrpc.ProgressPlain,
		isPty:    false,
	}
	if execer.shouldSuppressCmdOutput("docker", []string{"compose", "--project-name", "catch-svc", "--file", "/tmp/compose.yml", "logs"}) {
		t.Fatalf("did not expect docker compose logs output to be suppressed in plain mode")
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

func TestPayloadReaderUsesRawInputWhenBypassingPty(t *testing.T) {
	raw := strings.NewReader("raw")
	ptyRW := strings.NewReader("pty")
	execer := &ttyExecer{
		rawRW:          readWriter{Reader: raw, Writer: io.Discard},
		rw:             readWriter{Reader: ptyRW, Writer: io.Discard},
		bypassPtyInput: true,
	}

	got, err := io.ReadAll(execer.payloadReader())
	if err != nil {
		t.Fatalf("ReadAll payloadReader: %v", err)
	}
	if string(got) != "raw" {
		t.Fatalf("payload = %q, want raw", got)
	}
}

func TestPayloadReaderUsesSessionInputByDefault(t *testing.T) {
	execer := &ttyExecer{
		rawRW: readWriter{Reader: strings.NewReader("raw"), Writer: io.Discard},
		rw:    readWriter{Reader: strings.NewReader("pty"), Writer: io.Discard},
	}

	got, err := io.ReadAll(execer.payloadReader())
	if err != nil {
		t.Fatalf("ReadAll payloadReader: %v", err)
	}
	if string(got) != "pty" {
		t.Fatalf("payload = %q, want pty", got)
	}
}

func TestProgressSettingsModes(t *testing.T) {
	tests := []struct {
		name        string
		mode        catchrpc.ProgressMode
		isPty       bool
		wantEnabled bool
		wantQuiet   bool
	}{
		{name: "tty", mode: catchrpc.ProgressTTY, wantEnabled: true},
		{name: "plain", mode: catchrpc.ProgressPlain},
		{name: "quiet", mode: catchrpc.ProgressQuiet, wantQuiet: true},
		{name: "auto pty", mode: catchrpc.ProgressAuto, isPty: true, wantEnabled: true},
		{name: "auto no pty", mode: catchrpc.ProgressAuto},
		{name: "invalid", mode: catchrpc.ProgressMode("bad")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotEnabled, gotQuiet := progressSettings(tc.mode, tc.isPty)
			if gotEnabled != tc.wantEnabled || gotQuiet != tc.wantQuiet {
				t.Fatalf("progressSettings = (%v, %v), want (%v, %v)", gotEnabled, gotQuiet, tc.wantEnabled, tc.wantQuiet)
			}
		})
	}
}

func TestShouldLogCopyErrIgnoresExpectedDisconnects(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "eof", err: io.EOF},
		{name: "closed pipe", err: io.ErrClosedPipe},
		{name: "os closed", err: os.ErrClosed},
		{name: "eio", err: syscall.EIO},
		{name: "epipe", err: syscall.EPIPE},
		{name: "conn reset", err: syscall.ECONNRESET},
		{name: "net closed", err: net.ErrClosed},
		{name: "wrapped epipe", err: &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}},
		{name: "wrapped closed", err: &net.OpError{Op: "write", Net: "tcp", Err: net.ErrClosed}},
		{name: "closed network string", err: errors.New("write tcp 127.0.0.1:1234->127.0.0.1:4321: use of closed network connection")},
		{name: "endpoint closed string", err: errors.New("write tcp 100.85.58.107:41548: endpoint is closed for send")},
		{name: "websocket close sent", err: errors.New("websocket: close sent")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if shouldLogCopyErr(tc.err) {
				t.Fatalf("expected disconnect error to be suppressed: %v", tc.err)
			}
		})
	}
}

func TestShouldLogCopyErrReturnsTrueForUnexpectedErrors(t *testing.T) {
	if !shouldLogCopyErr(errors.New("boom")) {
		t.Fatal("expected unexpected error to be logged")
	}
}

type opaqueCopyErr struct {
	err error
}

func (e opaqueCopyErr) Error() string {
	return "copy failed"
}

func (e opaqueCopyErr) Unwrap() error {
	return e.err
}

func TestIsExpectedCopyErrUnwrapsOpaqueErrors(t *testing.T) {
	err := opaqueCopyErr{err: errors.New("websocket: close sent")}
	if !isExpectedCopyErr(err) {
		t.Fatalf("expected wrapped websocket close to be recognized")
	}
}

func TestTTYCommandHandlersCoverDispatchCommands(t *testing.T) {
	expected := []string{
		"copy",
		"cron",
		"disable",
		"docker",
		"edit",
		"enable",
		"env",
		"events",
		"ip",
		"logs",
		"mount",
		"remove",
		"restart",
		"rollback",
		"run",
		"stage",
		"start",
		"status",
		"stop",
		"tailscale",
		"ts",
		"umount",
		"version",
	}

	for _, name := range expected {
		if ttyCommandHandlers[name] == nil {
			t.Fatalf("expected command handler for %q", name)
		}
	}
}

func TestExecWithNoArgsReturnsNil(t *testing.T) {
	for _, args := range [][]string{nil, []string{}} {
		execer := &ttyExecer{args: args}
		if err := execer.exec(); err != nil {
			t.Fatalf("exec(%#v) returned error: %v", args, err)
		}
	}
}

func TestDispatchUnknownCommandReturnsError(t *testing.T) {
	execer := &ttyExecer{}
	err := execer.dispatch([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), `unhandled command "bogus"`) {
		t.Fatalf("dispatch error = %v, want unhandled command", err)
	}
}

func TestRunWritesCommandError(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{
		ctx:   context.Background(),
		args:  []string{"bogus"},
		rawRW: &out,
	}

	err := execer.run()
	if err == nil || !strings.Contains(err.Error(), `unhandled command "bogus"`) {
		t.Fatalf("run error = %v, want unhandled command", err)
	}
	if got := out.String(); !strings.Contains(got, `Error: unhandled command "bogus"`) {
		t.Fatalf("run output = %q, want error line", got)
	}
}

func TestVersionCommandWritesPlainAndJSON(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		var out bytes.Buffer
		execer := &ttyExecer{
			s:  newTestServer(t),
			rw: &out,
		}
		if err := execer.dispatch([]string{"version"}); err != nil {
			t.Fatalf("version dispatch returned error: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got == "" {
			t.Fatal("expected plain version output")
		}
	})

	t.Run("json", func(t *testing.T) {
		var out bytes.Buffer
		execer := &ttyExecer{
			s:  newTestServer(t),
			rw: &out,
		}
		if err := execer.dispatch([]string{"version", "--json"}); err != nil {
			t.Fatalf("version --json dispatch returned error: %v", err)
		}
		if got := out.String(); !strings.Contains(got, `"goos"`) {
			t.Fatalf("json version output = %q, want goos", got)
		}
	})
}

func TestDockerComposeSubcommandParsesFlagsAndSentinel(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "flag with value", args: []string{"--project-name", "catch-svc", "--file", "compose.yml", "up"}, want: "up"},
		{name: "flag equals", args: []string{"--project-name=catch-svc", "logs"}, want: "logs"},
		{name: "sentinel", args: []string{"--", "ps"}, want: "ps"},
		{name: "dash command", args: []string{"-"}, want: "-"},
		{name: "missing", args: []string{"--project-name"}, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dockerComposeSubcommand(tc.args); got != tc.want {
				t.Fatalf("dockerComposeSubcommand = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWriteLnWritesLine(t *testing.T) {
	var out bytes.Buffer
	writeln(&out, "hello")
	if got := out.String(); got != "hello\n" {
		t.Fatalf("writeln output = %q, want hello newline", got)
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
