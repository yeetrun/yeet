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
	"time"

	"github.com/creack/pty"
	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
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
	"broken pipe",
	"use of closed network connection",
	"endpoint is closed for send",
	"websocket: close sent",
}

var (
	openPty          = pty.Open
	dupPtyFileForTTY = dupPtyFile
	setWinsizeForTTY = setWinsize
)

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
	ctx                context.Context
	args               []string
	s                  *Server
	target             catchrpc.ExecTarget
	sn                 string
	hostLabel          string
	user               string
	payloadName        string
	vmSSHAuthorizedKey string
	progress           catchrpc.ProgressMode
	trace              bool
	rawRW              io.ReadWriter
	rawCloser          io.Closer
	isPty              bool
	ptyReq             PtySpec

	// Assigned during run
	rw                       io.ReadWriter // May be a pty
	bypassPtyInput           bool
	traceStart               time.Time
	serviceOperationLockHeld bool

	// Optional override for tests.
	serviceRunnerFn            func() (ServiceRunner, error)
	migrateServiceIdentityFunc func(context.Context, serviceIdentityMigrationRequest, io.Writer) (serviceIdentityMigrationResult, error)
	installFunc                func(action string, in io.Reader, cfg FileInstallerCfg) error
	editFileFunc               func(path string) error
	systemdStatusFunc          func(string) (svc.Status, error)
	systemdStatusesFunc        func() (map[string]svc.Status, error)
	dockerComposeStatusFunc    func(string) (svc.DockerComposeStatus, error)
	dockerComposeStatusesFunc  func() (map[string]svc.DockerComposeStatus, error)
	dockerOutdatedFunc         func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error)
	dockerOutdatedAllFunc      func(context.Context) ([]svc.DockerOutdatedRow, error)
	serviceInstallFunc         func(InstallerCfg) error
	serviceInstallGenFunc      func(InstallerCfg, int) error
	closeNewStageInstallerFunc func(FileInstallerCfg) error
	removeServiceFunc          func(string, RemoveOptions) (*RemoveReport, error)
	nativeCopyHook             func(string)
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
	if e.trace {
		e.traceStart = time.Now()
		e.tracef("exec start service=%s args=%s pty=%v", e.sn, strings.Join(e.args, " "), e.isPty)
	}
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
	if !e.shouldStartPtySession() {
		return nil, nil
	}

	stdin, tty, err := openPty()
	if err != nil {
		writef(e.rw, "Error: %v\n", err)
		return nil, err
	}
	stdout, err := dupPtyFileForTTY(stdin)
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
	setWinsizeForTTY(tty, e.ptyReq.Window.Width, e.ptyReq.Window.Height)
	session.copyOutputToSession(e)
	if !e.bypassPtyInput {
		session.copyInputFromSession(e)
	}
	return session, nil
}

func (e *ttyExecer) shouldStartPtySession() bool {
	return e.isPty && !e.isTransparentTTYCommand()
}

func (e *ttyExecer) isTransparentTTYCommand() bool {
	return len(e.args) == 2 && e.args[0] == "vm" && e.args[1] == "console"
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
	case "vm":
		return vmCommandShouldBypassPtyInput(e.args[1:])
	default:
		return false
	}
}

func vmCommandShouldBypassPtyInput(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "images":
		flags, remaining, err := cli.ParseVMImages(args[1:])
		if err != nil || !flags.Stdin || len(remaining) == 0 {
			return false
		}
		return remaining[0] == "import"
	case "runtime":
		action, _, ok := cli.FindVMRuntimeAction(args[1:])
		return ok && action == cli.VMRuntimeActionImport
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
		setWinsizeForTTY(tty, cols, rows)
	}
}

func (e *ttyExecer) exec() error {
	if e.args == nil {
		e.args = []string{}
	}
	switch e.target {
	case catchrpc.ExecTargetHostShell:
		return e.hostShellCmdFunc(e.args)
	case catchrpc.ExecTargetServiceShell:
		return e.withLockedServiceMutation(func() error {
			return e.serviceShellCmdFunc(e.args)
		})
	case catchrpc.ExecTargetVMSSHProxy:
		return e.vmSSHProxyCmdFunc(e.args)
	}
	if len(e.args) == 0 {
		return nil
	}
	return e.dispatch(e.args)
}

type ttyCommandHandler func(*ttyExecer, []string) error

var ttyCommandHandlers = map[string]ttyCommandHandler{
	"cron": func(e *ttyExecer, args []string) error {
		flags, payloadArgs, err := cli.ParseCron(args)
		if err != nil {
			return err
		}
		return e.cronCmdFuncFlags(flags, payloadArgs)
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
	"vm": func(e *ttyExecer, args []string) error {
		return e.vmCmdFunc(args)
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
	"run": func(e *ttyExecer, args []string) error {
		flags, runArgs, err := cli.ParseRun(args)
		if err != nil {
			return err
		}
		return e.runCmdFunc(flags, runArgs)
	},
	"service": func(e *ttyExecer, args []string) error {
		return e.serviceCmdFunc(args)
	},
	"copy": func(e *ttyExecer, args []string) error {
		return e.copyCmdFunc(args)
	},
	"docker": func(e *ttyExecer, args []string) error {
		return e.dockerCmdFunc(args)
	},
	"snapshots": func(e *ttyExecer, args []string) error {
		return e.snapshotsCmdFunc(args)
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
	handler, ok := ttyCommandHandlers[cmd]
	if !ok {
		log.Printf("Unhandled command %q", cmd)
		return fmt.Errorf("unhandled command %q", cmd)
	}
	permissions, err := ttyCommandPermissions(args)
	if err != nil {
		return err
	}
	args = args[1:]
	if permissions.has(permissionManage) {
		return e.withLockedServiceMutation(func() error {
			return handler(e, args)
		})
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

var passwdFilePath = "/etc/passwd"

func (e *ttyExecer) hostShellCmdFunc(args []string) error {
	return e.runShellCommand(args, e.defaultShellHomeDir())
}

func (e *ttyExecer) serviceShellCmdFunc(args []string) error {
	cmd, err := e.serviceShellCommand(args)
	if err != nil {
		return err
	}
	return cmd.Run()
}

func (e *ttyExecer) serviceShellCommand(args []string) (*exec.Cmd, error) {
	dir, err := e.serviceShellDir()
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	if len(args) == 0 {
		cmd = e.newCmd("/bin/sh")
	} else {
		cmd = e.newCmd(args[0], args[1:]...)
	}
	cmd.Dir = dir

	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return nil, err
	}
	if sv.ServiceType() != db.ServiceTypeSystemd {
		return cmd, nil
	}
	identity := effectiveServiceIdentity(sv).Persisted
	if err := validateServiceIdentityDrift(identity); err != nil {
		return nil, fmt.Errorf("service %q identity changed on this host: %w", e.sn, err)
	}
	cmd.Env = serviceShellEnvironment(cmd.Env, dir, identity.RequestedUser)
	if identity.UID != 0 || identity.GID != 0 {
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: identity.UID, Gid: identity.GID,
			// Keep NoSetGroups false so StartProcess invokes setgroups with
			// this empty list instead of inheriting Catch's root groups.
			Groups: []uint32{},
		}
	}
	return cmd, nil
}

func serviceShellEnvironment(base []string, home, userName string) []string {
	identityKeys := map[string]struct{}{
		"HOME": {}, "USER": {}, "LOGNAME": {}, "SHELL": {},
	}
	env := make([]string, 0, len(base)+4)
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, identity := identityKeys[key]; identity {
				continue
			}
		}
		env = append(env, entry)
	}
	return append(env,
		"HOME="+home,
		"USER="+userName,
		"LOGNAME="+userName,
		"SHELL=/bin/sh",
	)
}

func (e *ttyExecer) serviceShellDir() (string, error) {
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return "", fmt.Errorf("failed to get service type: %w", err)
	}
	if st == db.ServiceTypeVM {
		return "", fmt.Errorf("service %q is a VM service; VM targets use guest SSH", e.sn)
	}
	root, err := e.s.serviceRootDir(e.sn)
	if err != nil {
		return "", err
	}
	return serviceDataDirForRoot(root), nil
}

func (e *ttyExecer) runShellCommand(args []string, dir string) error {
	var cmd *exec.Cmd
	if len(args) == 0 {
		shell := e.defaultShellPath()
		cmd = e.newCmd(shell)
		cmd.Args[0] = "-" + filepath.Base(shell)
	} else {
		cmd = e.newCmd(args[0], args[1:]...)
	}
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Run()
}

func (e *ttyExecer) defaultShellPath() string {
	if e != nil && e.s != nil {
		if user := strings.TrimSpace(e.s.cfg.InstallUser); user != "" {
			if shell, ok := loginShellForUser(user); ok {
				return shell
			}
		}
	}
	if shell, ok := loginShellForUser("root"); ok {
		return shell
	}
	return "/bin/sh"
}

func (e *ttyExecer) defaultShellHomeDir() string {
	if e != nil && e.s != nil {
		if user := strings.TrimSpace(e.s.cfg.InstallUser); user != "" {
			if entry, ok := passwdEntryForUser(user); ok && entry.home != "" {
				return entry.home
			}
		}
	}
	if entry, ok := passwdEntryForUser("root"); ok && entry.home != "" {
		return entry.home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func loginShellForUser(username string) (string, bool) {
	entry, ok := passwdEntryForUser(username)
	if !ok || entry.shell == "" {
		return "", false
	}
	return entry.shell, true
}

type passwdEntry struct {
	home  string
	shell string
}

func passwdEntryForUser(username string) (passwdEntry, bool) {
	raw, err := os.ReadFile(passwdFilePath)
	if err != nil {
		return passwdEntry{}, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 7 || fields[0] != username {
			continue
		}
		return passwdEntry{
			home:  strings.TrimSpace(fields[5]),
			shell: strings.TrimSpace(fields[6]),
		}, true
	}
	return passwdEntry{}, false
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
