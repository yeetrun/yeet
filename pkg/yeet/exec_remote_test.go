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
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestErrorPrefixForRemoteExitRawNewline(t *testing.T) {
	if got := errorPrefixForRemoteExit(true, '\n', true); got != "\r" {
		t.Fatalf("expected carriage return prefix, got %q", got)
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
	execFn func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error)
}

func (f fakeExecClient) Exec(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
	return f.execFn(ctx, req, stdin, stdout, resizeCh)
}

func (f fakeExecClient) Events(ctx context.Context, req catchrpc.EventsRequest, onEvent func(catchrpc.Event)) error {
	return nil
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
