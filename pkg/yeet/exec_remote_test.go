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

	if err := proxy.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
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
	if !errors.As(err, &exitErr) || exitErr.code != 5 || exitErr.prefix != "\r\n" {
		t.Fatalf("remoteExecExitError = %#v, want code 5 with raw prefix", err)
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
