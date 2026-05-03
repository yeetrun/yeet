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
	type namer interface {
		Name() string
	}
	n, ok := r.(namer)
	if !ok {
		return ""
	}
	return payloadNameFromPath(n.Name())
}

func payloadNameFromPath(name string) string {
	name = strings.TrimSpace(name)
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
	_ = printCLIError(w, err)
}

func printCLIError(w io.Writer, err error) error {
	if err == nil {
		return nil
	}
	var pref errorPrefixer
	if errors.As(err, &pref) {
		if prefix := pref.errorPrefix(); prefix != "" {
			if _, writeErr := io.WriteString(w, prefix); writeErr != nil {
				return writeErr
			}
		}
	}
	_, writeErr := fmt.Fprintln(w, err)
	return writeErr
}

type remoteExecCleanup struct {
	label string
	fn    func() error
}

type remoteExecSession struct {
	req      catchrpc.ExecRequest
	stdin    io.Reader
	resizeCh <-chan catchrpc.Resize
	rawMode  bool
	cleanups []remoteExecCleanup
}

func newRemoteExecRequest(host string, service string, args []string, stdin io.Reader, tty bool) catchrpc.ExecRequest {
	req := catchrpc.ExecRequest{
		Service:  service,
		Args:     args,
		Host:     host,
		TTY:      tty,
		Progress: execProgressMode(),
	}
	if payload := payloadNameForStdin(stdin); payload != "" {
		req.PayloadName = payload
	}
	return req
}

func payloadNameForStdin(stdin io.Reader) string {
	if stdin == nil {
		return ""
	}
	if stdinFile, ok := stdin.(*os.File); ok && stdinFile == os.Stdin {
		return ""
	}
	return payloadNameFromReader(stdin)
}

func prepareRemoteExecSession(ctx context.Context, host string, service string, args []string, stdin io.Reader, tty bool) (*remoteExecSession, error) {
	session := &remoteExecSession{
		req:   newRemoteExecRequest(host, service, args, stdin, applyTTYOverride(tty)),
		stdin: stdin,
	}
	session.configureTTY(ctx)
	if err := session.prepareTTYStdin(); err != nil {
		session.close(&err)
		return nil, err
	}
	return session, nil
}

func (s *remoteExecSession) configureTTY(ctx context.Context) {
	if !s.req.TTY {
		return
	}
	fd := int(os.Stdin.Fd())
	if !isTerminalFn(fd) {
		s.req.TTY = false
		return
	}
	s.applyTerminalSize(fd)
	s.req.Term = os.Getenv("TERM")
	if s.stdinNeedsRawMode() {
		state, err := termMakeRawFn(fd)
		if err != nil {
			s.req.TTY = false
			return
		}
		s.rawMode = true
		s.addCleanup("restore terminal", func() error {
			return termRestoreFn(fd, state)
		})
	}
	s.resizeCh = watchResize(ctx, fd)
}

func (s *remoteExecSession) applyTerminalSize(fd int) {
	cols, rows, err := termGetSizeFn(fd)
	if err != nil {
		return
	}
	s.req.Cols = cols
	s.req.Rows = rows
}

func (s *remoteExecSession) stdinNeedsRawMode() bool {
	stdinFile, ok := s.stdin.(*os.File)
	return s.stdin == nil || ok && stdinFile == os.Stdin
}

func (s *remoteExecSession) prepareTTYStdin() error {
	if !s.req.TTY {
		return nil
	}
	if s.stdin == nil {
		s.stdin = os.Stdin
	}
	stdinFile, ok := s.stdin.(*os.File)
	if !ok || stdinFile != os.Stdin {
		return nil
	}
	proxy, err := newSessionStdinProxy(stdinFile)
	if err != nil {
		return fmt.Errorf("failed to prepare interactive stdin: %w", err)
	}
	s.addCleanup("close interactive stdin", proxy.Close)
	s.stdin = proxy
	return nil
}

func (s *remoteExecSession) addCleanup(label string, fn func() error) {
	s.cleanups = append(s.cleanups, remoteExecCleanup{label: label, fn: fn})
}

func (s *remoteExecSession) close(errp *error) {
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		if err := s.cleanups[i].fn(); err != nil && *errp == nil {
			*errp = fmt.Errorf("%s: %w", s.cleanups[i].label, err)
		}
	}
}

func execRemote(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) (err error) {
	host := Host()
	client := newRPCExecClientFn(host)
	session, err := prepareRemoteExecSession(ctx, host, service, args, stdin, tty)
	if err != nil {
		return err
	}
	defer session.close(&err)

	out := &trackingWriter{w: os.Stdout}
	code, err := client.Exec(ctx, session.req, session.stdin, out, session.resizeCh)
	if err != nil {
		return err
	}
	return remoteExecExitError(code, session.rawMode, out)
}

func remoteExecExitError(code int, rawMode bool, out *trackingWriter) error {
	if code == 0 {
		return nil
	}
	last, saw := out.LastByte()
	prefix := errorPrefixForRemoteExit(rawMode && isTerminalFn(int(os.Stderr.Fd())), last, saw)
	return remoteExitError{code: code, prefix: prefix}
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
	req := newRemoteExecRequest(host, service, args, stdin, false)
	var buf bytes.Buffer
	code, err := client.Exec(ctx, req, stdin, &buf, nil)
	if err != nil {
		return nil, err
	}
	if err := remoteExitCodeError(code); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func execRemoteStream(ctx context.Context, service string, args []string, stdin io.Reader) (io.ReadCloser, <-chan error, error) {
	host := Host()
	client := newRPCExecClientFn(host)
	req := newRemoteExecRequest(host, service, args, stdin, false)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go execRemoteStreamToPipe(ctx, client, req, stdin, pw, done)
	return pr, done, nil
}

func execRemoteStreamToPipe(ctx context.Context, client rpcExecClient, req catchrpc.ExecRequest, stdin io.Reader, pw *io.PipeWriter, done chan<- error) {
	code, err := client.Exec(ctx, req, stdin, pw, nil)
	if err == nil {
		err = remoteExitCodeError(code)
	}
	done <- closeExecStreamPipe(pw, err)
}

func remoteExitCodeError(code int) error {
	if code == 0 {
		return nil
	}
	return remoteExitError{code: code}
}

func closeExecStreamPipe(pw *io.PipeWriter, err error) error {
	if err != nil {
		if closeErr := pw.CloseWithError(err); closeErr != nil {
			return errors.Join(err, fmt.Errorf("close stream: %w", closeErr))
		}
		return err
	}
	if closeErr := pw.Close(); closeErr != nil {
		return fmt.Errorf("close stream: %w", closeErr)
	}
	return nil
}

func handleEventsRPC(ctx context.Context, svc string, flags cli.EventsFlags) error {
	req := eventsRequest(svc, flags)
	var writeErr error
	err := newRPCExecClientFn(Host()).Events(ctx, req, func(ev catchrpc.Event) {
		if writeErr != nil {
			return
		}
		writeErr = writeEvent(os.Stdout, ev)
	})
	if err != nil {
		return err
	}
	if writeErr != nil {
		return fmt.Errorf("write event: %w", writeErr)
	}
	return nil
}

func eventsRequest(svc string, flags cli.EventsFlags) catchrpc.EventsRequest {
	req := catchrpc.EventsRequest{All: flags.All}
	if !flags.All {
		req.Service = svc
	}
	return req
}

func writeEvent(w io.Writer, ev catchrpc.Event) error {
	_, err := fmt.Fprintf(w, "Received event: %v\n", ev)
	return err
}

func HandleMountSys(ctx context.Context, rawArgs []string) error {
	return execRemote(ctx, systemServiceName, rawArgs, nil, true)
}
