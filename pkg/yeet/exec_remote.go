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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type rpcExecClient interface {
	Exec(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error)
	Events(ctx context.Context, req catchrpc.EventsRequest, onEvent func(catchrpc.Event)) error
}

func newRPCClient(host string) *catchrpc.Client {
	return catchrpc.NewClient(host, defaultRPCPort)
}

func watchResize(ctx context.Context, fd int) <-chan catchrpc.Resize {
	ch := make(chan catchrpc.Resize, 4)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		defer close(ch)
		defer signal.Stop(sigCh)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				cols, rows, err := term.GetSize(fd)
				if err != nil {
					continue
				}
				ch <- catchrpc.Resize{Rows: rows, Cols: cols}
			}
		}
	}()
	return ch
}

func payloadNameFromReader(r io.Reader) string {
	if r == nil {
		return ""
	}
	type namer interface {
		Name() string
	}
	n, ok := r.(namer)
	if !ok {
		return ""
	}
	name := strings.TrimSpace(n.Name())
	if name == "" {
		return ""
	}
	base := filepath.Base(name)
	if base == "." || base == string(os.PathSeparator) || base == ".." {
		return ""
	}
	return base
}

type errorPrefixer interface {
	errorPrefix() string
}

type remoteExitError struct {
	code   int
	prefix string
}

func (e remoteExitError) Error() string {
	return fmt.Sprintf("remote exit %d", e.code)
}

func (e remoteExitError) errorPrefix() string {
	return e.prefix
}

type trackingWriter struct {
	w    io.Writer
	last byte
	saw  bool
}

type sessionStdinProxy struct {
	r         *io.PipeReader
	dup       *os.File
	done      chan struct{}
	stop      chan struct{}
	origFlags int
}

func newSessionStdinProxy(stdin *os.File) (*sessionStdinProxy, error) {
	origFlags, err := unix.FcntlInt(stdin.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return nil, fmt.Errorf("get stdin flags: %w", err)
	}
	dup, err := syscall.Dup(int(stdin.Fd()))
	if err != nil {
		return nil, fmt.Errorf("dup stdin: %w", err)
	}
	if err := syscall.SetNonblock(dup, true); err != nil {
		_ = syscall.Close(dup)
		return nil, fmt.Errorf("set stdin nonblocking: %w", err)
	}
	pr, pw := io.Pipe()
	proxy := &sessionStdinProxy{
		r:         pr,
		dup:       os.NewFile(uintptr(dup), stdin.Name()),
		done:      make(chan struct{}),
		stop:      make(chan struct{}),
		origFlags: origFlags,
	}
	go proxy.forwardInput(pw)
	return proxy, nil
}

func (p *sessionStdinProxy) forwardInput(pw *io.PipeWriter) {
	defer close(p.done)
	defer func() {
		_ = p.dup.Close()
	}()
	buf := make([]byte, 4096)
	for {
		if p.stopRequested() {
			_ = pw.Close()
			return
		}
		if !p.copyInputChunk(pw, buf) {
			return
		}
	}
}

func (p *sessionStdinProxy) stopRequested() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

func (p *sessionStdinProxy) copyInputChunk(pw *io.PipeWriter, buf []byte) bool {
	n, err := p.dup.Read(buf)
	if n > 0 {
		if _, werr := pw.Write(buf[:n]); werr != nil {
			_ = pw.CloseWithError(werr)
			return false
		}
	}
	if err == nil {
		return true
	}
	if isTerminalStdinReadError(err) {
		_ = pw.Close()
		return false
	}
	if isRetryableStdinReadError(err) {
		return p.waitForInputRetry(pw)
	}
	_ = pw.CloseWithError(err)
	return false
}

func isTerminalStdinReadError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrClosed)
}

func isRetryableStdinReadError(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK)
}

func (p *sessionStdinProxy) waitForInputRetry(pw *io.PipeWriter) bool {
	select {
	case <-p.stop:
		_ = pw.Close()
		return false
	case <-time.After(10 * time.Millisecond):
		return true
	}
}

func (p *sessionStdinProxy) Read(b []byte) (int, error) {
	return p.r.Read(b)
}

func (p *sessionStdinProxy) Close() error {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	_, errFlags := unix.FcntlInt(p.dup.Fd(), unix.F_SETFL, p.origFlags)
	errRead := p.r.Close()
	errDup := p.dup.Close()
	<-p.done
	if errFlags != nil {
		return errFlags
	}
	if errDup != nil && !errors.Is(errDup, os.ErrClosed) {
		return errDup
	}
	if errRead != nil && !errors.Is(errRead, io.ErrClosedPipe) {
		return errRead
	}
	return nil
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		t.last = p[len(p)-1]
		t.saw = true
	}
	return t.w.Write(p)
}

func (t *trackingWriter) LastByte() (byte, bool) {
	return t.last, t.saw
}

func errorPrefixForRemoteExit(rawMode bool, lastByte byte, sawOutput bool) string {
	if !rawMode || !sawOutput {
		return ""
	}
	switch lastByte {
	case '\n':
		return "\r"
	case '\r':
		return "\n"
	default:
		return "\r\n"
	}
}

func PrintCLIError(w io.Writer, err error) {
	if err == nil {
		return
	}
	var pref errorPrefixer
	if errors.As(err, &pref) {
		if prefix := pref.errorPrefix(); prefix != "" {
			fmt.Fprint(w, prefix)
		}
	}
	fmt.Fprintln(w, err)
}

func execRemote(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
	host := Host()
	client := newRPCExecClientFn(host)
	tty = applyTTYOverride(tty)
	req := catchrpc.ExecRequest{
		Service: service,
		Args:    args,
		Host:    host,
		TTY:     tty,
	}
	req.Progress = execProgressMode()
	if stdin != nil && stdin != os.Stdin {
		if payload := payloadNameFromReader(stdin); payload != "" {
			req.PayloadName = payload
		}
	}
	var resizeCh <-chan catchrpc.Resize
	fd := int(os.Stdin.Fd())
	rawMode := false
	if tty && isTerminalFn(fd) {
		cols, rows, err := termGetSizeFn(fd)
		if err == nil {
			req.Cols = cols
			req.Rows = rows
		}
		req.Term = os.Getenv("TERM")
		if stdin == nil || stdin == os.Stdin {
			state, err := termMakeRawFn(fd)
			if err == nil {
				rawMode = true
				defer termRestoreFn(fd, state)
				resizeCh = watchResize(ctx, fd)
			} else {
				req.TTY = false
			}
		} else {
			resizeCh = watchResize(ctx, fd)
		}
	} else {
		req.TTY = false
	}
	if stdin == nil && req.TTY {
		stdin = os.Stdin
	}
	if req.TTY {
		if stdinFile, ok := stdin.(*os.File); ok && stdinFile == os.Stdin {
			proxy, err := newSessionStdinProxy(stdinFile)
			if err != nil {
				return fmt.Errorf("failed to prepare interactive stdin: %w", err)
			}
			defer proxy.Close()
			stdin = proxy
		}
	}
	out := &trackingWriter{w: os.Stdout}
	code, err := client.Exec(ctx, req, stdin, out, resizeCh)
	if err != nil {
		return err
	}
	if code != 0 {
		last, saw := out.LastByte()
		prefix := errorPrefixForRemoteExit(rawMode && isTerminalFn(int(os.Stderr.Fd())), last, saw)
		return remoteExitError{code: code, prefix: prefix}
	}
	return nil
}

var execRemoteFn = execRemote
var execRemoteOutputFn = execRemoteOutput
var execRemoteStreamFn = execRemoteStream
var remoteCatchOSAndArchFn = remoteCatchOSAndArch
var pushAllLocalImagesFn = pushAllLocalImages
var isTerminalFn = term.IsTerminal
var termGetSizeFn = term.GetSize
var termMakeRawFn = term.MakeRaw
var termRestoreFn = term.Restore
var newRPCExecClientFn = func(host string) rpcExecClient {
	return newRPCClient(host)
}

func execRemoteOutput(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
	client := newRPCExecClientFn(host)
	req := catchrpc.ExecRequest{
		Service:  service,
		Args:     args,
		Host:     host,
		TTY:      false,
		Progress: execProgressMode(),
	}
	if stdin != nil && stdin != os.Stdin {
		if payload := payloadNameFromReader(stdin); payload != "" {
			req.PayloadName = payload
		}
	}
	var buf bytes.Buffer
	code, err := client.Exec(ctx, req, stdin, &buf, nil)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, remoteExitError{code: code}
	}
	return buf.Bytes(), nil
}

func execRemoteStream(ctx context.Context, service string, args []string, stdin io.Reader) (io.ReadCloser, <-chan error, error) {
	host := Host()
	client := newRPCExecClientFn(host)
	req := catchrpc.ExecRequest{
		Service:  service,
		Args:     args,
		Host:     host,
		TTY:      false,
		Progress: execProgressMode(),
	}
	if stdin != nil && stdin != os.Stdin {
		if payload := payloadNameFromReader(stdin); payload != "" {
			req.PayloadName = payload
		}
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		code, err := client.Exec(ctx, req, stdin, pw, nil)
		if err != nil {
			_ = pw.CloseWithError(err)
			done <- err
			return
		}
		if code != 0 {
			err := remoteExitError{code: code}
			_ = pw.CloseWithError(err)
			done <- err
			return
		}
		_ = pw.Close()
		done <- nil
	}()
	return pr, done, nil
}

func handleEventsRPC(ctx context.Context, svc string, flags cli.EventsFlags) error {
	sub := catchrpc.EventsRequest{All: flags.All}
	if !flags.All {
		sub.Service = svc
	}
	return newRPCExecClientFn(Host()).Events(ctx, sub, func(ev catchrpc.Event) {
		fmt.Fprintf(os.Stdout, "Received event: %v\n", ev)
	})
}

func HandleMountSys(ctx context.Context, rawArgs []string) error {
	return execRemote(ctx, systemServiceName, rawArgs, nil, true)
}
