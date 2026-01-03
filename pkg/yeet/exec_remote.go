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

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"golang.org/x/term"
)

func newRPCClient(host string) *catchrpc.Client {
	return catchrpc.NewClient(host, loadedPrefs.RPCPort)
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
	client := newRPCClient(loadedPrefs.Host)
	tty = applyTTYOverride(tty)
	req := catchrpc.ExecRequest{
		Service: service,
		Args:    args,
		Host:    loadedPrefs.Host,
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
		cols, rows, err := term.GetSize(fd)
		if err == nil {
			req.Cols = cols
			req.Rows = rows
		}
		req.Term = os.Getenv("TERM")
		if stdin == nil || stdin == os.Stdin {
			state, err := term.MakeRaw(fd)
			if err == nil {
				rawMode = true
				defer term.Restore(fd, state)
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
var remoteCatchOSAndArchFn = remoteCatchOSAndArch
var pushAllLocalImagesFn = pushAllLocalImages
var isTerminalFn = term.IsTerminal

func execRemoteOutput(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
	client := newRPCClient(host)
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

func handleEventsRPC(ctx context.Context, svc string, flags cli.EventsFlags) error {
	sub := catchrpc.EventsRequest{All: flags.All}
	if !flags.All {
		sub.Service = svc
	}
	return newRPCClient(loadedPrefs.Host).Events(ctx, sub, func(ev catchrpc.Event) {
		fmt.Fprintf(os.Stdout, "Received event: %v\n", ev)
	})
}

func HandleMountSys(ctx context.Context, rawArgs []string) error {
	return execRemote(ctx, systemServiceName, rawArgs, nil, true)
}
