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
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"github.com/shayne/yeet/pkg/catchrpc"
	"github.com/shayne/yeet/pkg/cli"
	"golang.org/x/sys/unix"
)

type writeCloser interface {
	CloseWrite() error
}

func shouldLogCopyErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EIO) {
		return false
	}
	if ne, ok := err.(interface{ Unwrap() error }); ok {
		return shouldLogCopyErr(ne.Unwrap())
	}
	return true
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
	var doneWritingToSession chan struct{}
	e.rw = e.rawRW
	e.bypassPtyInput = e.shouldBypassPtyInput()
	var closer io.Closer
	if e.isPty {
		stdin, tty, err := pty.Open()
		if err != nil {
			fmt.Fprintf(e.rw, "Error: %v\n", err)
			return err
		}
		dup, err := syscall.Dup(int(stdin.Fd()))
		if err != nil {
			stdin.Close()
			tty.Close()
			log.Printf("Error duping pty: %v", err)
			return err
		}
		stdout := os.NewFile(uintptr(dup), stdin.Name())

		e.rw = tty
		closer = tty

		setWinsize(tty, e.ptyReq.Window.Width, e.ptyReq.Window.Height)

		doneWritingToSession = make(chan struct{})
		go func() {
			if c, ok := e.rawRW.(writeCloser); ok {
				defer c.CloseWrite()
			}
			defer stdout.Close()
			defer close(doneWritingToSession)
			if _, err := io.Copy(e.rawRW, stdout); shouldLogCopyErr(err) && e.ctx.Err() == nil {
				log.Printf("Error copying from stdout to session: %v", err)
			}
		}()
		if !e.bypassPtyInput {
			go func() {
				defer stdin.Close()
				if _, err := io.Copy(stdin, e.rawRW); shouldLogCopyErr(err) && e.ctx.Err() == nil {
					log.Printf("Error copying from session to stdin: %v", err)
				}
			}()
		}
	}

	err := e.exec()
	if err != nil {
		fmt.Fprintf(e.rawRW, "Error: %v\n", err)
	}
	if closer != nil {
		closer.Close()
	}
	if doneWritingToSession != nil {
		<-doneWritingToSession
	}
	return err
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

func (e *ttyExecer) dispatch(args []string) error {
	cmd := args[0]
	args = args[1:]
	switch cmd {
	case "cron":
		if err := cli.RequireArgsAtLeast("cron", args, 5); err != nil {
			return err
		}
		cronexpr := strings.Join(args[0:5], " ")
		return e.cronCmdFunc(cronexpr, args[5:])
	case "disable":
		return e.disableCmdFunc()
	case "edit":
		flags, _, err := cli.ParseEdit(args)
		if err != nil {
			return err
		}
		return e.editCmdFunc(flags)
	case "events":
		flags, _, err := cli.ParseEvents(args)
		if err != nil {
			return err
		}
		return e.eventsCmdFunc(flags)
	case "enable":
		return e.enableCmdFunc()
	case "mount":
		flags, mountArgs, err := cli.ParseMount(args)
		if err != nil {
			return err
		}
		return e.mountCmdFunc(flags, mountArgs)
	case "ip":
		return e.ipCmdFunc()
	case "tailscale", "ts":
		return e.tsCmdFunc(args)
	case "umount":
		return e.umountCmdFunc(args)
	case "env":
		return e.envCmdFunc(args)
	case "logs":
		flags, _, err := cli.ParseLogs(args)
		if err != nil {
			return err
		}
		return e.logsCmdFunc(flags)
	case "remove":
		return e.removeCmdFunc()
	case "restart":
		return e.restartCmdFunc()
	case "rollback":
		return e.rollbackCmdFunc()
	case "run":
		flags, runArgs, err := cli.ParseRun(args)
		if err != nil {
			return err
		}
		return e.runCmdFunc(flags, runArgs)
	case "copy":
		if err := cli.RequireArgsAtLeast("copy", args, 1); err != nil {
			return err
		}
		return e.copyCmdFunc(args[0])
	case "docker":
		return e.dockerCmdFunc(args)
	case "stage":
		flags, subcmd, stageArgs, err := cli.ParseStage(args)
		if err != nil {
			return err
		}
		return e.stageCmdFunc(subcmd, flags, stageArgs)
	case "start":
		return e.startCmdFunc()
	case "status":
		flags, _, err := cli.ParseStatus(args)
		if err != nil {
			return err
		}
		return e.statusCmdFunc(flags)
	case "stop":
		return e.stopCmdFunc()
	case "version":
		flags, _, err := cli.ParseVersion(args)
		if err != nil {
			return err
		}
		if flags.JSON {
			json.NewEncoder(e.rw).Encode(GetInfoWithInstallUser(e.s.cfg.InstallUser, e.s.cfg.InstallHost))
		} else {
			fmt.Fprintln(e.rw, VersionCommit())
		}
		return nil
	default:
		log.Printf("Unhandled command %q", cmd)
		return fmt.Errorf("unhandled command %q", cmd)
	}
}

// VersionCommit returns the commit hash of the current build.
func VersionCommit() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var dirty bool
	var commit string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if commit == "" {
		return "dev"
	}

	if len(commit) >= 9 {
		commit = commit[:9]
	}
	if dirty {
		commit += "+dirty"
	}
	return commit
}

func (e *ttyExecer) printf(format string, a ...any) {
	fmt.Fprintf(e.rw, format, a...)
}

func (e *ttyExecer) newCmd(name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(e.ctx, name, args...)
	rw := e.rw

	c.Stdin = rw
	if e.isPty && filepath.Base(name) == "docker" && len(args) > 0 && args[0] == "compose" {
		// Ensure compose starts on a clean line without wrapping stdout/stderr,
		// so it still detects a TTY and renders its own progress UI.
		fmt.Fprint(rw, "\r\033[K")
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
			return true
		}
	}
	return false
}

func setWinsize(f *os.File, w, h int) {
	unix.IoctlSetWinsize(int(f.Fd()), syscall.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(h),
		Col: uint16(w),
	})
}
