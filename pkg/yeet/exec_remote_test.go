// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestErrorPrefixForRemoteExitRawNewline(t *testing.T) {
	if got := errorPrefixForRemoteExit(true, '\n', true); got != "\r" {
		t.Fatalf("expected carriage return prefix, got %q", got)
	}
}

func TestErrorPrefixForRemoteExitVariants(t *testing.T) {
	tests := []struct {
		name      string
		rawMode   bool
		lastByte  byte
		sawOutput bool
		want      string
	}{
		{name: "not raw"},
		{name: "no output", rawMode: true},
		{name: "last carriage return", rawMode: true, sawOutput: true, lastByte: '\r', want: "\n"},
		{name: "partial line", rawMode: true, sawOutput: true, lastByte: 'x', want: "\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorPrefixForRemoteExit(tt.rawMode, tt.lastByte, tt.sawOutput)
			if got != tt.want {
				t.Fatalf("errorPrefixForRemoteExit = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrintCLIErrorIncludesPrefix(t *testing.T) {
	buf := new(bytes.Buffer)
	PrintCLIError(buf, remoteExitError{code: 1, prefix: "\r"})
	if got := buf.String(); got != "\rremote exit 1\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestPrintCLIErrorIncludesPrefixWhenWrapped(t *testing.T) {
	buf := new(bytes.Buffer)
	err := fmt.Errorf("failed: %w", remoteExitError{code: 2, prefix: "\r"})
	PrintCLIError(buf, err)
	if got := buf.String(); got != "\rfailed: remote exit 2\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestPrintCLIErrorReturnsWriteError(t *testing.T) {
	wantErr := errors.New("write failed")
	err := printCLIError(failingWriter{err: wantErr}, remoteExitError{code: 1, prefix: "\r"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("printCLIError error = %v, want %v", err, wantErr)
	}
}

type namedReader struct {
	io.Reader
	name string
}

func (r namedReader) Name() string {
	return r.name
}

func TestPayloadNameFromReader(t *testing.T) {
	tests := []struct {
		name string
		r    io.Reader
		want string
	}{
		{name: "nil reader"},
		{name: "unnamed reader", r: strings.NewReader("payload")},
		{name: "blank name", r: namedReader{Reader: strings.NewReader("payload"), name: "  "}},
		{name: "base name", r: namedReader{Reader: strings.NewReader("payload"), name: "/tmp/app.env"}, want: "app.env"},
		{name: "trimmed path", r: namedReader{Reader: strings.NewReader("payload"), name: " /tmp/run.yml "}, want: "run.yml"},
		{name: "current directory", r: namedReader{Reader: strings.NewReader("payload"), name: "."}},
		{name: "parent directory", r: namedReader{Reader: strings.NewReader("payload"), name: ".."}},
		{name: "root directory", r: namedReader{Reader: strings.NewReader("payload"), name: string(os.PathSeparator)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := payloadNameFromReader(tt.r); got != tt.want {
				t.Fatalf("payloadNameFromReader() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewRPCClientReturnsClient(t *testing.T) {
	if client := newRPCClient("catch"); client == nil {
		t.Fatal("newRPCClient returned nil")
	}
}

func TestWatchResizeCancellationUnblocksSaturatedProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	resizes := watchResizeSignals(
		ctx,
		42,
		signals,
		func() { close(stopped) },
		func(fd int) (int, int, error) {
			if fd != 42 {
				t.Fatalf("terminal fd = %d, want 42", fd)
			}
			return 120, 40, nil
		},
	)

	for range 6 {
		signals <- syscall.SIGWINCH
	}
	cancel()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("saturated resize producer did not stop after context cancellation")
	}
	for range resizes {
	}
}

func TestPayloadNameForStdinIgnoresNilAndProcessStdin(t *testing.T) {
	if got := payloadNameForStdin(nil); got != "" {
		t.Fatalf("payloadNameForStdin nil = %q, want empty", got)
	}
	if got := payloadNameForStdin(os.Stdin); got != "" {
		t.Fatalf("payloadNameForStdin os.Stdin = %q, want empty", got)
	}
	got := payloadNameForStdin(namedReader{Reader: strings.NewReader("payload"), name: "/tmp/input.txt"})
	if got != "input.txt" {
		t.Fatalf("payloadNameForStdin named reader = %q, want input.txt", got)
	}
}

func TestTrackingWriterRecordsLastByte(t *testing.T) {
	tw := &trackingWriter{w: io.Discard}
	if last, ok := tw.LastByte(); ok || last != 0 {
		t.Fatalf("LastByte before write = %q %v, want empty", last, ok)
	}
	n, err := tw.Write([]byte("abc\n"))
	if err != nil || n != 4 {
		t.Fatalf("Write = %d, %v, want 4 nil", n, err)
	}
	if last, ok := tw.LastByte(); !ok || last != '\n' {
		t.Fatalf("LastByte after write = %q %v, want newline true", last, ok)
	}
	if got, want := tw.OutputTail(), "abc\n"; got != want {
		t.Fatalf("OutputTail = %q, want %q", got, want)
	}
}

func TestTrackingWriterBoundsRemoteErrorOutputTail(t *testing.T) {
	raw := bytes.Repeat([]byte("x"), remoteErrorOutputLimit+17)
	raw[len(raw)-1] = 'z'
	tw := &trackingWriter{w: io.Discard}
	if _, err := tw.Write(raw); err != nil {
		t.Fatal(err)
	}
	got := tw.OutputTail()
	if len(got) != remoteErrorOutputLimit || got[len(got)-1] != 'z' {
		t.Fatalf("bounded output tail length/last = %d/%q, want %d/z", len(got), got[len(got)-1], remoteErrorOutputLimit)
	}
}

func TestRawTerminalOutputWriterConvertsBareNewlines(t *testing.T) {
	var buf bytes.Buffer
	w := &rawTerminalOutputWriter{w: &buf}
	if _, err := w.Write([]byte("one\ntwo\r\nthree\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if got, want := buf.String(), "one\r\ntwo\r\nthree\r\n"; got != want {
		t.Fatalf("raw terminal output = %q, want %q", got, want)
	}
}

func TestRawTerminalOutputWriterPreservesCRLFAcrossChunks(t *testing.T) {
	var buf bytes.Buffer
	w := &rawTerminalOutputWriter{w: &buf}
	if _, err := w.Write([]byte("one\r")); err != nil {
		t.Fatalf("first Write returned error: %v", err)
	}
	if _, err := w.Write([]byte("\ntwo\n")); err != nil {
		t.Fatalf("second Write returned error: %v", err)
	}
	if got, want := buf.String(), "one\r\ntwo\r\n"; got != want {
		t.Fatalf("raw terminal output = %q, want %q", got, want)
	}
}

func TestTerminalStdinReadErrorHelpers(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrClosedPipe, os.ErrClosed} {
		if !isTerminalStdinReadError(err) {
			t.Fatalf("isTerminalStdinReadError(%v) = false, want true", err)
		}
	}
	if isTerminalStdinReadError(errors.New("other")) {
		t.Fatal("isTerminalStdinReadError other = true, want false")
	}
	if !isRetryableStdinReadError(syscall.EAGAIN) {
		t.Fatal("isRetryableStdinReadError EAGAIN = false, want true")
	}
	if isRetryableStdinReadError(io.EOF) {
		t.Fatal("isRetryableStdinReadError EOF = true, want false")
	}
}

func TestSessionStdinProxyWaitForInputRetry(t *testing.T) {
	t.Run("stop closes writer", func(t *testing.T) {
		_, pw := io.Pipe()
		p := &sessionStdinProxy{stop: make(chan struct{})}
		close(p.stop)
		if p.waitForInputRetry(pw) {
			t.Fatal("waitForInputRetry = true after stop, want false")
		}
	})

	t.Run("timeout retries", func(t *testing.T) {
		_, pw := io.Pipe()
		defer pw.Close()
		p := &sessionStdinProxy{stop: make(chan struct{})}
		if !p.waitForInputRetry(pw) {
			t.Fatal("waitForInputRetry = false before stop, want true")
		}
	})
}

func TestSessionStdinProxyCloseUnblocksPendingRead(t *testing.T) {
	srcR, srcW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	defer srcR.Close()
	defer srcW.Close()

	proxy, err := newSessionStdinProxy(srcR)
	if err != nil {
		t.Fatalf("newSessionStdinProxy error: %v", err)
	}

	if _, err := srcW.Write([]byte("y")); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	buf := make([]byte, 1)
	if _, err := io.ReadFull(proxy, buf); err != nil {
		t.Fatalf("ReadFull error: %v", err)
	}
	if string(buf) != "y" {
		t.Fatalf("expected forwarded byte y, got %q", string(buf))
	}

	done := make(chan error, 1)
	go func() {
		next := make([]byte, 1)
		_, err := proxy.Read(next)
		done <- err
	}()

	closed := make(chan error, 1)
	go func() {
		closed <- proxy.Close()
	}()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Close to stop stdin proxy")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected blocked read to be interrupted")
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("unexpected read error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked read to stop")
	}
}

func TestSessionStdinProxyCloseRestoresTTYFlags(t *testing.T) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		t.Skipf("controlling tty unavailable: %v", err)
	}
	defer tty.Close()

	origFlags, err := unix.FcntlInt(tty.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatalf("FcntlInt original flags error: %v", err)
	}

	proxy, err := newSessionStdinProxy(tty)
	if err != nil {
		t.Fatalf("newSessionStdinProxy error: %v", err)
	}
	if err := proxy.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	gotFlags, err := unix.FcntlInt(tty.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatalf("FcntlInt restored flags error: %v", err)
	}
	if gotFlags != origFlags {
		t.Fatalf("expected tty flags %#x after close, got %#x", origFlags, gotFlags)
	}
}

type fakeExecClient struct {
	execFn   func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error)
	eventsFn func(ctx context.Context, req catchrpc.EventsRequest, onEvent func(catchrpc.Event)) error
}

func (f fakeExecClient) Exec(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
	if f.execFn == nil {
		return 0, nil
	}
	return f.execFn(ctx, req, stdin, stdout, resizeCh)
}

func (f fakeExecClient) Events(ctx context.Context, req catchrpc.EventsRequest, onEvent func(catchrpc.Event)) error {
	if f.eventsFn != nil {
		return f.eventsFn(ctx, req, onEvent)
	}
	return nil
}

func restoreExecRemoteGlobals(t *testing.T) {
	t.Helper()
	oldClientFactory := newRPCExecClientFn
	oldIsTerminal := isTerminalFn
	oldGetSize := termGetSizeFn
	oldMakeRaw := termMakeRawFn
	oldRestore := termRestoreFn
	oldPrefs := loadedPrefs
	oldUI := execUIOverrides
	t.Cleanup(func() {
		newRPCExecClientFn = oldClientFactory
		isTerminalFn = oldIsTerminal
		termGetSizeFn = oldGetSize
		termMakeRawFn = oldMakeRaw
		termRestoreFn = oldRestore
		loadedPrefs = oldPrefs
		execUIOverrides = oldUI
	})
}

func TestExecRemoteShellBuildsHostShellRequest(t *testing.T) {
	restoreExecRemoteGlobals(t)

	var gotReq catchrpc.ExecRequest
	newRPCExecClientFn = func(host string) rpcExecClient {
		if host != "yeet-lab" {
			t.Fatalf("client host = %q, want yeet-lab", host)
		}
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				gotReq = req
				return 0, nil
			},
		}
	}

	err := execRemoteShell(context.Background(), "yeet-lab", catchrpc.ExecTargetHostShell, "", []string{"whoami"}, nil, false, io.Discard)
	if err != nil {
		t.Fatalf("execRemoteShell: %v", err)
	}
	if gotReq.Target != catchrpc.ExecTargetHostShell || gotReq.Service != "" || !reflect.DeepEqual(gotReq.Args, []string{"whoami"}) {
		t.Fatalf("request = %#v, want host shell whoami", gotReq)
	}
}

func TestExecRemoteShellBuildsServiceShellRequest(t *testing.T) {
	restoreExecRemoteGlobals(t)

	var gotReq catchrpc.ExecRequest
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				gotReq = req
				return 0, nil
			},
		}
	}

	err := execRemoteShell(context.Background(), "yeet-lab", catchrpc.ExecTargetServiceShell, "api", []string{"pwd"}, nil, false, io.Discard)
	if err != nil {
		t.Fatalf("execRemoteShell: %v", err)
	}
	if gotReq.Target != catchrpc.ExecTargetServiceShell || gotReq.Service != "api" || !reflect.DeepEqual(gotReq.Args, []string{"pwd"}) {
		t.Fatalf("request = %#v, want service shell pwd", gotReq)
	}
}

func TestNewRemoteExecRequestAddsLocalSSHKeyForVMRun(t *testing.T) {
	tmp := t.TempDir()
	sshDir := filepath.Join(tmp, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	wantKey := "ssh-ed25519 AAAATEST local@example"
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte(wantKey+"\n"), 0o600); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("YEET_VM_SSH_KEY", "")

	for _, payload := range []string{"vm://ubuntu/26.04", "vm://foo/bar"} {
		t.Run(payload, func(t *testing.T) {
			req := newRemoteExecRequest("yeet-lab", "devbox", []string{"run", payload}, nil, false)
			if req.VMSSHKey != wantKey {
				t.Fatalf("VMSSHKey = %q, want local public key", req.VMSSHKey)
			}
		})
	}
}

func TestNewRemoteExecRequestSkipsLocalSSHKeyForNonVMRun(t *testing.T) {
	t.Setenv("YEET_VM_SSH_KEY", "ssh-ed25519 AAAATEST local@example")

	req := newRemoteExecRequest("yeet-lab", "api", []string{"run", "ghcr.io/example/api:latest"}, nil, false)
	if req.VMSSHKey != "" {
		t.Fatalf("VMSSHKey = %q, want empty for non-VM run", req.VMSSHKey)
	}
}

func TestNewRemoteExecRequestPropagatesTraceEnv(t *testing.T) {
	t.Setenv("YEET_TRACE", "1")

	req := newRemoteExecRequest("yeet-lab", "devbox", []string{"run", "vm://ubuntu/26.04"}, nil, false)
	if !req.Trace {
		t.Fatal("Trace = false, want true")
	}
}

func TestExecRemoteToWritesClientOutputToProvidedWriter(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"

	newRPCExecClientFn = func(host string) rpcExecClient {
		if host != "host-a" {
			t.Fatalf("host = %q, want host-a", host)
		}
		return rpcExecClientFunc(func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
			if req.Service != "svc-a" {
				t.Fatalf("service = %q, want svc-a", req.Service)
			}
			if _, err := stdout.Write([]byte("remote output\n")); err != nil {
				t.Fatalf("write stdout: %v", err)
			}
			return 0, nil
		})
	}

	var out bytes.Buffer
	if err := execRemoteTo(context.Background(), "svc-a", []string{"status"}, nil, false, &out); err != nil {
		t.Fatalf("execRemoteTo: %v", err)
	}
	if got := out.String(); got != "remote output\n" {
		t.Fatalf("output = %q, want remote output", got)
	}
}

type rpcExecClientFunc func(context.Context, catchrpc.ExecRequest, io.Reader, io.Writer, <-chan catchrpc.Resize) (int, error)

func (f rpcExecClientFunc) Exec(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
	return f(ctx, req, stdin, stdout, resizeCh)
}

func (f rpcExecClientFunc) Events(context.Context, catchrpc.EventsRequest, func(catchrpc.Event)) error {
	return errors.New("events not implemented in test")
}

func TestExecRemoteBuildsNonTTYRequestWithPayloadName(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"

	var gotHost string
	var gotReq catchrpc.ExecRequest
	newRPCExecClientFn = func(host string) rpcExecClient {
		gotHost = host
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				gotReq = req
				if resizeCh != nil {
					t.Fatal("non-tty exec should not pass a resize channel")
				}
				return 0, nil
			},
		}
	}

	stdin := namedReader{Reader: strings.NewReader("payload"), name: "/tmp/app.env"}
	err := execRemote(context.Background(), "app", []string{"env", "set"}, stdin, false)
	if err != nil {
		t.Fatalf("execRemote returned error: %v", err)
	}

	if gotHost != "host-a" {
		t.Fatalf("client host = %q, want host-a", gotHost)
	}
	if gotReq.Host != "host-a" || gotReq.Service != "app" || gotReq.PayloadName != "app.env" {
		t.Fatalf("request = %+v, want host/service/payload set", gotReq)
	}
	if gotReq.TTY {
		t.Fatal("request TTY = true, want false")
	}
	if !reflect.DeepEqual(gotReq.Args, []string{"env", "set"}) {
		t.Fatalf("request args = %#v", gotReq.Args)
	}
}

func TestRemoteExecExitErrorUsesRawModePrefix(t *testing.T) {
	restoreExecRemoteGlobals(t)
	isTerminalFn = func(int) bool { return true }

	if err := remoteExecExitError(0, true, &trackingWriter{w: io.Discard}); err != nil {
		t.Fatalf("remoteExecExitError zero = %v, want nil", err)
	}

	out := &trackingWriter{w: io.Discard}
	if _, err := out.Write([]byte("partial")); err != nil {
		t.Fatalf("tracking write error: %v", err)
	}
	err := remoteExecExitError(5, true, out)
	var exitErr remoteExitError
	if !errors.As(err, &exitErr) || exitErr.code != 5 || exitErr.prefix != "\r\n" || exitErr.output != "partial" {
		t.Fatalf("remoteExecExitError = %#v, want code 5 with raw prefix and captured output", err)
	}
}

func TestExecRemoteReturnsTerminalRestoreError(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"

	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin Pipe error: %v", err)
	}
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = stdinR.Close()
		_ = stdinW.Close()
	})
	os.Stdin = stdinR

	isTerminalFn = func(int) bool { return true }
	termGetSizeFn = func(int) (int, int, error) { return 80, 24, nil }
	termMakeRawFn = func(int) (*term.State, error) { return &term.State{}, nil }
	restoreErr := errors.New("restore failed")
	termRestoreFn = func(int, *term.State) error { return restoreErr }
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{}
	}

	err = execRemote(context.Background(), "app", []string{"shell"}, nil, true)
	if !errors.Is(err, restoreErr) {
		t.Fatalf("execRemote error = %v, want restore error", err)
	}
}

func TestPrepareRemoteExecSessionUsesSharedTerminalState(t *testing.T) {
	restoreExecRemoteGlobals(t)
	isTerminalFn = func(int) bool { return true }
	sizeCalls := 0
	termGetSizeFn = func(int) (int, int, error) {
		sizeCalls++
		return 80, 24, nil
	}
	resizes := make(chan catchrpc.Resize, 8)
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := withRemoteExecTerminalState(baseCtx, remoteExecTerminalState{
		TTY:    true,
		Cols:   132,
		Rows:   44,
		Term:   "xterm-shared",
		Resize: resizes,
	})
	shared, ok := remoteExecTerminalStateFromContext(ctx)
	if !ok {
		t.Fatal("shared terminal state missing from context")
	}
	waitForSize := func(cols, rows int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			gotCols, gotRows := shared.size()
			if gotCols == cols && gotRows == rows {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("shared terminal size = %dx%d, want latest %dx%d", gotCols, gotRows, cols, rows)
			}
			time.Sleep(time.Millisecond)
		}
	}

	resizes <- catchrpc.Resize{Cols: 150, Rows: 50}
	resizes <- catchrpc.Resize{Cols: 180, Rows: 55}
	waitForSize(180, 55)

	session, err := prepareRemoteExecSession(ctx, "host-a", "svc-a", []string{"run"}, strings.NewReader(""), true)
	if err != nil {
		t.Fatalf("prepareRemoteExecSession: %v", err)
	}
	if !session.req.TTY || session.req.Cols != 180 || session.req.Rows != 55 || session.req.Term != "xterm-shared" {
		t.Fatalf("request terminal state = %#v, want latest shared 180x55 xterm-shared", session.req)
	}
	select {
	case stale := <-session.resizeCh:
		t.Fatalf("first session replayed stale resize %#v", stale)
	default:
	}
	resizes <- catchrpc.Resize{Cols: 200, Rows: 60}
	select {
	case resize := <-session.resizeCh:
		if resize != (catchrpc.Resize{Cols: 200, Rows: 60}) {
			t.Fatalf("first session resize = %#v, want 200x60", resize)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first session resize")
	}
	var closeErr error
	session.close(&closeErr)
	if closeErr != nil {
		t.Fatalf("close first remote exec session: %v", closeErr)
	}

	resizes <- catchrpc.Resize{Cols: 220, Rows: 70}
	resizes <- catchrpc.Resize{Cols: 240, Rows: 80}
	waitForSize(240, 80)

	next, err := prepareRemoteExecSession(ctx, "host-a", "svc-a", []string{"commit"}, strings.NewReader(""), true)
	if err != nil {
		t.Fatalf("prepare second remote exec session: %v", err)
	}
	if next.req.Cols != 240 || next.req.Rows != 80 {
		t.Fatalf("second request terminal size = %dx%d, want latest 240x80", next.req.Cols, next.req.Rows)
	}
	select {
	case stale := <-next.resizeCh:
		t.Fatalf("second session replayed stale resize %#v", stale)
	default:
	}
	if sizeCalls != 0 {
		t.Fatalf("terminal size reads = %d, want none after the shared profile snapshot", sizeCalls)
	}
}

func TestExecRemoteOutputBuildsRequestAndReturnsOutput(t *testing.T) {
	restoreExecRemoteGlobals(t)

	var gotHost string
	var gotReq catchrpc.ExecRequest
	newRPCExecClientFn = func(host string) rpcExecClient {
		gotHost = host
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				gotReq = req
				if _, err := stdout.Write([]byte("ok")); err != nil {
					return 0, err
				}
				return 0, nil
			},
		}
	}

	stdin := namedReader{Reader: strings.NewReader("payload"), name: "/tmp/run.yml"}
	got, err := execRemoteOutput(context.Background(), "host-b", "app", []string{"status"}, stdin)
	if err != nil {
		t.Fatalf("execRemoteOutput returned error: %v", err)
	}

	if string(got) != "ok" {
		t.Fatalf("output = %q, want ok", string(got))
	}
	if gotHost != "host-b" {
		t.Fatalf("client host = %q, want host-b", gotHost)
	}
	if gotReq.Host != "host-b" || gotReq.Service != "app" || gotReq.PayloadName != "run.yml" {
		t.Fatalf("request = %+v, want host/service/payload set", gotReq)
	}
	if gotReq.TTY {
		t.Fatal("request TTY = true, want false")
	}
}

func TestExecRemoteOutputIncludesRemoteOutputOnExitError(t *testing.T) {
	restoreExecRemoteGlobals(t)

	denial := "Error: missing yeet permission \"read\"; update your Tailscale grant for yeetrun.com/app/yeet:\nhttps://yeetrun.com/docs/security/tailscale-access-grants\n"
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				if _, err := io.WriteString(stdout, denial); err != nil {
					return 0, err
				}
				return 1, nil
			},
		}
	}

	_, err := execRemoteOutput(context.Background(), "host-b", systemServiceName, []string{"status"}, nil)
	if err == nil {
		t.Fatal("execRemoteOutput error = nil, want remote exit with denial text")
	}
	for _, want := range []string{"remote exit 1", `missing yeet permission "read"`, "https://yeetrun.com/docs/security/tailscale-access-grants"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("execRemoteOutput error = %q, want %q", err, want)
		}
	}
}

func TestExecRemoteRawModeNormalizesPermissionErrorNewlines(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"

	oldStdin := os.Stdin
	oldStdout := os.Stdout
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin Pipe error: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout Pipe error: %v", err)
	}
	t.Cleanup(func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	})
	os.Stdin = stdinR
	os.Stdout = stdoutW

	isTerminalFn = func(int) bool { return true }
	termGetSizeFn = func(int) (int, int, error) { return 100, 24, nil }
	termMakeRawFn = func(int) (*term.State, error) { return &term.State{}, nil }
	termRestoreFn = func(int, *term.State) error { return nil }
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				_, err := io.WriteString(stdout, "Error: missing yeet permission \"ssh\"; update your Tailscale grant for yeetrun.com/app/yeet:\nhttps://yeetrun.com/docs/security/tailscale-access-grants\n")
				if err != nil {
					return 0, err
				}
				return 1, nil
			},
		}
	}

	err = execRemote(context.Background(), systemServiceName, []string{"ssh"}, nil, true)
	var exitErr remoteExitError
	if !errors.As(err, &exitErr) || exitErr.code != 1 {
		t.Fatalf("execRemote error = %v, want remote exit 1", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("stdout close error: %v", err)
	}
	got, err := io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("ReadAll stdout error: %v", err)
	}
	want := "Error: missing yeet permission \"ssh\"; update your Tailscale grant for yeetrun.com/app/yeet:\r\nhttps://yeetrun.com/docs/security/tailscale-access-grants\r\n"
	if string(got) != want {
		t.Fatalf("stdout = %q, want %q", string(got), want)
	}
}

func TestExecRemoteStreamReturnsStreamAndDone(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"

	var gotReq catchrpc.ExecRequest
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				gotReq = req
				if _, err := stdout.Write([]byte("streamed")); err != nil {
					return 0, err
				}
				return 0, nil
			},
		}
	}

	stdin := namedReader{Reader: strings.NewReader("payload"), name: "/tmp/input.txt"}
	rc, done, err := execRemoteStream(context.Background(), "app", []string{"logs"}, stdin)
	if err != nil {
		t.Fatalf("execRemoteStream returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = rc.Close()
	})

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(got) != "streamed" {
		t.Fatalf("stream output = %q, want streamed", string(got))
	}
	if err := <-done; err != nil {
		t.Fatalf("done error = %v", err)
	}
	if gotReq.Host != "host-a" || gotReq.Service != "app" || gotReq.PayloadName != "input.txt" {
		t.Fatalf("request = %+v, want host/service/payload set", gotReq)
	}
}

func TestExecRemoteStreamReportsRemoteExit(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				return 7, nil
			},
		}
	}

	rc, done, err := execRemoteStream(context.Background(), "app", []string{"logs"}, nil)
	if err != nil {
		t.Fatalf("execRemoteStream returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = rc.Close()
	})

	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("ReadAll error = nil, want remote exit error")
	}
	err = <-done
	var exitErr remoteExitError
	if !errors.As(err, &exitErr) || exitErr.code != 7 {
		t.Fatalf("done error = %v, want remote exit 7", err)
	}
}

func TestHandleMountSysExecutesSystemService(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"
	isTerminalFn = func(int) bool { return false }

	var gotReq catchrpc.ExecRequest
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				gotReq = req
				return 0, nil
			},
		}
	}

	if err := HandleMountSys(context.Background(), []string{"mount", "tmpfs"}); err != nil {
		t.Fatalf("HandleMountSys error: %v", err)
	}
	if gotReq.Host != "host-a" || gotReq.Service != systemServiceName {
		t.Fatalf("request host/service = %q/%q, want host-a/%s", gotReq.Host, gotReq.Service, systemServiceName)
	}
	if !reflect.DeepEqual(gotReq.Args, []string{"mount", "tmpfs"}) {
		t.Fatalf("request args = %#v, want mount tmpfs", gotReq.Args)
	}
	if gotReq.TTY {
		t.Fatal("request TTY = true, want false when stdin is not terminal")
	}
}

func TestHandleEventsRPCWritesServiceEvents(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"

	oldStdout := os.Stdout
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout Pipe error: %v", err)
	}
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	})
	os.Stdout = stdoutW

	var gotReq catchrpc.EventsRequest
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			eventsFn: func(ctx context.Context, req catchrpc.EventsRequest, onEvent func(catchrpc.Event)) error {
				gotReq = req
				onEvent(catchrpc.Event{ServiceName: "app", Type: "started"})
				return nil
			},
		}
	}

	if err := handleEventsRPC(context.Background(), "app", cli.EventsFlags{}); err != nil {
		t.Fatalf("handleEventsRPC returned error: %v", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("stdout close error: %v", err)
	}
	out, err := io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("ReadAll stdout error: %v", err)
	}

	if gotReq.Service != "app" || gotReq.All {
		t.Fatalf("events request = %+v, want service app only", gotReq)
	}
	if !strings.Contains(string(out), "Received event:") || !strings.Contains(string(out), "started") {
		t.Fatalf("stdout = %q, want event output", string(out))
	}
}

func TestHandleEventsRPCReturnsWriteError(t *testing.T) {
	restoreExecRemoteGlobals(t)
	loadedPrefs.DefaultHost = "host-a"

	oldStdout := os.Stdout
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout Pipe error: %v", err)
	}
	if err := stdoutR.Close(); err != nil {
		t.Fatalf("stdout reader close error: %v", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("stdout writer close error: %v", err)
	}
	t.Cleanup(func() {
		os.Stdout = oldStdout
	})
	os.Stdout = stdoutW

	writeCalled := false
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			eventsFn: func(ctx context.Context, req catchrpc.EventsRequest, onEvent func(catchrpc.Event)) error {
				onEvent(catchrpc.Event{ServiceName: "app", Type: "started"})
				writeCalled = true
				return nil
			},
		}
	}

	err = handleEventsRPC(context.Background(), "app", cli.EventsFlags{})
	if err == nil {
		t.Fatal("handleEventsRPC error = nil, want write error")
	}
	if !writeCalled {
		t.Fatal("event callback was not called")
	}
}

func TestExecRemoteClosesInteractiveStdinBeforeNextLocalPrompt(t *testing.T) {
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	oldClientFactory := newRPCExecClientFn
	oldIsTerminal := isTerminalFn
	oldGetSize := termGetSizeFn
	oldMakeRaw := termMakeRawFn
	oldRestore := termRestoreFn
	oldPrefs := loadedPrefs
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		newRPCExecClientFn = oldClientFactory
		isTerminalFn = oldIsTerminal
		termGetSizeFn = oldGetSize
		termMakeRawFn = oldMakeRaw
		termRestoreFn = oldRestore
		loadedPrefs = oldPrefs
	}()

	loadedPrefs.DefaultHost = "host-a"

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin Pipe error: %v", err)
	}
	defer stdinR.Close()
	defer stdinW.Close()
	os.Stdin = stdinR

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout Pipe error: %v", err)
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	os.Stdout = stdoutW
	defer stdoutW.Close()
	go func() {
		_, _ = io.Copy(io.Discard, stdoutR)
	}()

	isTerminalFn = func(int) bool { return true }
	termGetSizeFn = func(int) (int, int, error) { return 80, 24, nil }
	termMakeRawFn = func(int) (*term.State, error) { return &term.State{}, nil }
	termRestoreFn = func(int, *term.State) error { return nil }

	remoteReaderDone := make(chan error, 1)
	newRPCExecClientFn = func(string) rpcExecClient {
		return fakeExecClient{
			execFn: func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
				go func() {
					_, err := io.Copy(io.Discard, stdin)
					remoteReaderDone <- err
				}()
				return 0, nil
			},
		}
	}

	if err := execRemote(context.Background(), "openspeedtest", []string{"remove"}, nil, true); err != nil {
		t.Fatalf("execRemote returned error: %v", err)
	}

	select {
	case err := <-remoteReaderDone:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("unexpected remote reader error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for remote stdin reader to stop")
	}

	confirmDone := make(chan struct {
		ok  bool
		err error
	}, 1)
	go func() {
		ok, err := cmdutil.Confirm(os.Stdin, io.Discard, `Remove "openspeedtest" from yeet.toml?`)
		confirmDone <- struct {
			ok  bool
			err error
		}{ok: ok, err: err}
	}()

	if _, err := stdinW.Write([]byte("y\n")); err != nil {
		t.Fatalf("stdin write error: %v", err)
	}

	select {
	case res := <-confirmDone:
		if res.err != nil {
			t.Fatalf("Confirm returned error: %v", res.err)
		}
		if !res.ok {
			t.Fatal("expected confirmation to succeed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for local confirmation to read input")
	}
}
