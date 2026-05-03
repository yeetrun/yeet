// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"golang.org/x/sys/unix"
)

type writeCloser interface {
	CloseWrite() error
}

var expectedCopyErrs = []error{
	io.EOF,
	io.ErrClosedPipe,
	os.ErrClosed,
	syscall.EIO,
	syscall.EPIPE,
	syscall.ECONNRESET,
	net.ErrClosed,
}

var expectedCopyErrMessages = []string{
	"use of closed network connection",
	"endpoint is closed for send",
	"websocket: close sent",
}

func shouldLogCopyErr(err error) bool {
	return err != nil && !isExpectedCopyErr(err)
}

func isExpectedCopyErr(err error) bool {
	if err == nil {
		return false
	}
	return isExpectedCopyErrValue(err) ||
		isExpectedCopyErrMessage(err) ||
		isExpectedCopyErrUnwrapped(err)
}

func isExpectedCopyErrValue(err error) bool {
	for _, expected := range expectedCopyErrs {
		if errors.Is(err, expected) {
			return true
		}
	}
	return false
}

func isExpectedCopyErrMessage(err error) bool {
	msg := err.Error()
	for _, expected := range expectedCopyErrMessages {
		if strings.Contains(msg, expected) {
			return true
		}
	}
	return false
}

func isExpectedCopyErrUnwrapped(err error) bool {
	if ne, ok := err.(interface{ Unwrap() error }); ok {
		return isExpectedCopyErr(ne.Unwrap())
	}
	return false
}

type PtyWindow struct {
	Width  int
	Height int
}

type PtySpec struct {
	Term   string
	Window PtyWindow
}

type ttyExecer struct {
	// Inputs
	ctx         context.Context
	args        []string
	s           *Server
	sn          string
	hostLabel   string
	user        string
	payloadName string
	progress    catchrpc.ProgressMode
	rawRW       io.ReadWriter
	rawCloser   io.Closer
	isPty       bool
	ptyReq      PtySpec

	// Assigned during run
	rw             io.ReadWriter // May be a pty
	bypassPtyInput bool

	// Optional override for tests.
	serviceRunnerFn func() (ServiceRunner, error)
}

type ttyPtySession struct {
	stdin                *os.File
	tty                  *os.File
	stdout               *os.File
	doneWritingToSession chan struct{}
}

func normalizeProgressMode(mode catchrpc.ProgressMode) catchrpc.ProgressMode {
	switch mode {
	case catchrpc.ProgressAuto, catchrpc.ProgressTTY, catchrpc.ProgressPlain, catchrpc.ProgressQuiet:
		return mode
	default:
		return catchrpc.ProgressAuto
	}
}

func progressSettings(mode catchrpc.ProgressMode, isPty bool) (enabled bool, quiet bool) {
	mode = normalizeProgressMode(mode)
	switch mode {
	case catchrpc.ProgressTTY:
		return true, false
	case catchrpc.ProgressPlain:
		return false, false
	case catchrpc.ProgressQuiet:
		return false, true
	default:
		return isPty, false
	}
}

func (e *ttyExecer) newProgressUI(action string) *runUI {
	enabled, quiet := progressSettings(e.progress, e.isPty)
	serviceLabel := e.sn
	if e.hostLabel != "" && serviceLabel != "" {
		serviceLabel = fmt.Sprintf("%s@%s", serviceLabel, e.hostLabel)
	}
	return newRunUI(e.rw, enabled, quiet, action, serviceLabel)
}

func (e *ttyExecer) runAction(action, step string, fn func() error) error {
	ui := e.newProgressUI(action)
	ui.Start()
	defer ui.Stop()
	ui.StartStep(step)
	if err := fn(); err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep("")
	return nil
}

func (e *ttyExecer) run() error {
	e.rw = e.rawRW
	e.bypassPtyInput = e.shouldBypassPtyInput()
	ptySession, err := e.startPtySession()
	if err != nil {
		return err
	}

	err = e.exec()
	if err != nil {
		writef(e.rawRW, "Error: %v\n", err)
	}
	if ptySession != nil {
		ptySession.close()
		ptySession.wait()
	}
	return err
}

func (e *ttyExecer) startPtySession() (*ttyPtySession, error) {
	if !e.isPty {
		return nil, nil
	}

	stdin, tty, err := pty.Open()
	if err != nil {
		writef(e.rw, "Error: %v\n", err)
		return nil, err
	}
	stdout, err := dupPtyFile(stdin)
	if err != nil {
		_ = stdin.Close()
		_ = tty.Close()
		log.Printf("Error duping pty: %v", err)
		return nil, err
	}

	session := &ttyPtySession{
		stdin:                stdin,
		tty:                  tty,
		stdout:               stdout,
		doneWritingToSession: make(chan struct{}),
	}
	e.rw = tty
	setWinsize(tty, e.ptyReq.Window.Width, e.ptyReq.Window.Height)
	session.copyOutputToSession(e)
	if !e.bypassPtyInput {
		session.copyInputFromSession(e)
	}
	return session, nil
}

func dupPtyFile(stdin *os.File) (*os.File, error) {
	dup, err := syscall.Dup(int(stdin.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(dup), stdin.Name()), nil
}

func (s *ttyPtySession) copyOutputToSession(e *ttyExecer) {
	go func() {
		if c, ok := e.rawRW.(writeCloser); ok {
			defer func() { _ = c.CloseWrite() }()
		}
		defer func() { _ = s.stdout.Close() }()
		defer close(s.doneWritingToSession)
		if _, err := io.Copy(e.rawRW, s.stdout); shouldLogCopyErr(err) && e.ctx.Err() == nil {
			log.Printf("Error copying from stdout to session: %v", err)
		}
	}()
}

func (s *ttyPtySession) copyInputFromSession(e *ttyExecer) {
	go func() {
		defer func() { _ = s.stdin.Close() }()
		if _, err := io.Copy(s.stdin, e.rawRW); shouldLogCopyErr(err) && e.ctx.Err() == nil {
			log.Printf("Error copying from session to stdin: %v", err)
		}
	}()
}

func (s *ttyPtySession) close() {
	_ = s.tty.Close()
}

func (s *ttyPtySession) wait() {
	<-s.doneWritingToSession
}

func (e *ttyExecer) shouldBypassPtyInput() bool {
	if !e.isPty || len(e.args) == 0 {
		return false
	}
	switch e.args[0] {
	case "run", "copy", "stage", "cron":
		return true
	default:
		return false
	}
}

func (e *ttyExecer) payloadReader() io.Reader {
	if e.bypassPtyInput {
		return e.rawRW
	}
	return e.rw
}

func (e *ttyExecer) ResizeTTY(cols, rows int) {
	if !e.isPty {
		return
	}
	if tty, ok := e.rw.(*os.File); ok {
		setWinsize(tty, cols, rows)
	}
}

func (e *ttyExecer) exec() error {
	if e.args == nil {
		e.args = []string{}
	}
	if len(e.args) == 0 {
		return nil
	}
	return e.dispatch(e.args)
}

type ttyCommandHandler func(*ttyExecer, []string) error

var ttyCommandHandlers = map[string]ttyCommandHandler{
	"cron": func(e *ttyExecer, args []string) error {
		if err := cli.RequireArgsAtLeast("cron", args, 5); err != nil {
			return err
		}
		cronexpr := strings.Join(args[0:5], " ")
		return e.cronCmdFunc(cronexpr, args[5:])
	},
	"disable": func(e *ttyExecer, _ []string) error {
		return e.disableCmdFunc()
	},
	"edit": func(e *ttyExecer, args []string) error {
		flags, _, err := cli.ParseEdit(args)
		if err != nil {
			return err
		}
		return e.editCmdFunc(flags)
	},
	"events": func(e *ttyExecer, args []string) error {
		flags, _, err := cli.ParseEvents(args)
		if err != nil {
			return err
		}
		return e.eventsCmdFunc(flags)
	},
	"enable": func(e *ttyExecer, _ []string) error {
		return e.enableCmdFunc()
	},
	"mount": func(e *ttyExecer, args []string) error {
		flags, mountArgs, err := cli.ParseMount(args)
		if err != nil {
			return err
		}
		return e.mountCmdFunc(flags, mountArgs)
	},
	"ip": func(e *ttyExecer, _ []string) error {
		return e.ipCmdFunc()
	},
	"tailscale": func(e *ttyExecer, args []string) error {
		return e.tsCmdFunc(args)
	},
	"ts": func(e *ttyExecer, args []string) error {
		return e.tsCmdFunc(args)
	},
	"umount": func(e *ttyExecer, args []string) error {
		return e.umountCmdFunc(args)
	},
	"env": func(e *ttyExecer, args []string) error {
		return e.envCmdFunc(args)
	},
	"logs": func(e *ttyExecer, args []string) error {
		flags, _, err := cli.ParseLogs(args)
		if err != nil {
			return err
		}
		return e.logsCmdFunc(flags)
	},
	"remove": func(e *ttyExecer, args []string) error {
		flags, _, err := cli.ParseRemove(args)
		if err != nil {
			return err
		}
		return e.removeCmdFunc(flags)
	},
	"restart": func(e *ttyExecer, _ []string) error {
		return e.restartCmdFunc()
	},
	"rollback": func(e *ttyExecer, _ []string) error {
		return e.rollbackCmdFunc()
	},
	"run": func(e *ttyExecer, args []string) error {
		flags, runArgs, err := cli.ParseRun(args)
		if err != nil {
			return err
		}
		return e.runCmdFunc(flags, runArgs)
	},
	"copy": func(e *ttyExecer, args []string) error {
		return e.copyCmdFunc(args)
	},
	"docker": func(e *ttyExecer, args []string) error {
		return e.dockerCmdFunc(args)
	},
	"stage": func(e *ttyExecer, args []string) error {
		flags, subcmd, stageArgs, err := cli.ParseStage(args)
		if err != nil {
			return err
		}
		return e.stageCmdFunc(subcmd, flags, stageArgs)
	},
	"start": func(e *ttyExecer, _ []string) error {
		return e.startCmdFunc()
	},
	"status": func(e *ttyExecer, args []string) error {
		flags, _, err := cli.ParseStatus(args)
		if err != nil {
			return err
		}
		return e.statusCmdFunc(flags)
	},
	"stop": func(e *ttyExecer, _ []string) error {
		return e.stopCmdFunc()
	},
	"version": func(e *ttyExecer, args []string) error {
		flags, _, err := cli.ParseVersion(args)
		if err != nil {
			return err
		}
		if flags.JSON {
			_ = json.NewEncoder(e.rw).Encode(GetInfoWithInstallUser(e.s.cfg.InstallUser, e.s.cfg.InstallHost))
		} else {
			writeln(e.rw, Version())
		}
		return nil
	},
}

func (e *ttyExecer) dispatch(args []string) error {
	cmd := args[0]
	args = args[1:]
	handler, ok := ttyCommandHandlers[cmd]
	if !ok {
		log.Printf("Unhandled command %q", cmd)
		return fmt.Errorf("unhandled command %q", cmd)
	}
	return handler(e, args)
}

func (e *ttyExecer) printf(format string, a ...any) {
	writef(e.rw, format, a...)
}

func writef(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

func writeln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

func (e *ttyExecer) newCmd(name string, args ...string) *exec.Cmd {
	return e.newCmdContext(e.ctx, name, args...)
}

func (e *ttyExecer) newCmdContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = e.ctx
	}
	c := exec.CommandContext(ctx, name, args...)
	rw := e.rw

	c.Stdin = rw
	if e.isPty && filepath.Base(name) == "docker" && len(args) > 0 && args[0] == "compose" {
		// Ensure compose starts on a clean line without wrapping stdout/stderr,
		// so it still detects a TTY and renders its own progress UI.
		_, _ = fmt.Fprint(rw, "\r\033[K")
	}
	c.Stdout = rw
	c.Stderr = rw
	if e.shouldSuppressCmdOutput(name, args) {
		c.Stdout = io.Discard
		c.Stderr = io.Discard
	}

	env := os.Environ()
	if e.isPty {
		term := e.ptyReq.Term
		if term == "" {
			term = "xterm"
		}
		env = append(env, fmt.Sprintf("TERM=%s", term))
		c.SysProcAttr = &syscall.SysProcAttr{
			Setctty: true,
			Setsid:  true,
		}
	}
	if env != nil {
		c.Env = env
	}
	return c
}

func (e *ttyExecer) shouldSuppressCmdOutput(name string, args []string) bool {
	mode := normalizeProgressMode(e.progress)
	if mode == catchrpc.ProgressAuto && e.isPty {
		return false
	}
	if mode == catchrpc.ProgressAuto {
		mode = catchrpc.ProgressPlain
	}
	if mode == catchrpc.ProgressPlain || mode == catchrpc.ProgressQuiet {
		if filepath.Base(name) == "docker" && len(args) > 0 && args[0] == "compose" {
			return dockerComposeSubcommand(args[1:]) != "logs"
		}
	}
	return false
}

func dockerComposeSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return arg
		}
		if strings.Contains(arg, "=") {
			continue
		}
		switch arg {
		case "--project-name", "--project-directory", "--file", "--env-file", "--profile":
			i++
		}
	}
	return ""
}

func setWinsize(f *os.File, w, h int) {
	_ = unix.IoctlSetWinsize(int(f.Fd()), syscall.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(h),
		Col: uint16(w),
	})
}
