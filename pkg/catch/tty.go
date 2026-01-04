// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/creack/pty"
	"github.com/shayne/yeet/pkg/catchrpc"
	"github.com/shayne/yeet/pkg/cli"
	"github.com/shayne/yeet/pkg/cmdutil"
	"github.com/shayne/yeet/pkg/cronutil"
	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/fileutil"
	"github.com/shayne/yeet/pkg/svc"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
	"tailscale.com/util/mak"
)

const (
	editUnitsSeparator = "=====================================|%s|====================================="
)

var (
	editUnitsSeparatorRe = regexp.MustCompile(`=====================================\|([^|]+)\|=====================================`)
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

// Human-readable format function
func humanReadableBytes(bts float64) string {
	const unit = 1024
	if bts <= unit {
		return fmt.Sprintf("%.2f B", bts)
	}
	const prefix = "KMGTPE"
	n := bts
	i := -1
	for n > unit {
		i++
		n = n / unit
	}

	return fmt.Sprintf("%.2f %cB", n, prefix[i])
}

// install installs a service by reading the binary from the `in` input stream.
// The service is configured via `cfg`, an InstallerCfg struct. Client output
// can be written to `out`. An error is returned if the installation fails.
func (e *ttyExecer) install(action string, in io.Reader, cfg FileInstallerCfg) (retErr error) {
	if runtime.GOOS == "darwin" {
		// Don't do anything on macOS yet.
		return nil
	}
	ui := e.newProgressUI(action)
	ui.Start()
	defer ui.Stop()

	cfg.Printer = ui.Printer
	cfg.UI = ui

	inst, err := NewFileInstaller(e.s, cfg)
	if err != nil {
		ui.FailStep("failed to create installer")
		return fmt.Errorf("failed to create installer: %w", err)
	}
	defer func() {
		if cerr := inst.Close(); cerr != nil && retErr == nil {
			ui.FailStep("install failed")
			retErr = cerr
		}
	}()

	ui.StartStep(runStepUpload)

	if !cfg.EnvFile {
		// Start a goroutine to close the session if no data is received after 1
		// second but only if it's not an env file which can be empty.
		started := make(chan struct{})
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-e.ctx.Done():
				return
			case <-started:
			case <-done:
				return
			case <-time.After(time.Second):
				ui.FailStep("timeout waiting for bytes")
				if e.rawCloser != nil {
					e.rawCloser.Close()
				}
				return
			}

			print := func() {
				detail := fmt.Sprintf("%s @ %s/s", humanReadableBytes(inst.Received()), humanReadableBytes(inst.Rate()))
				ui.UpdateDetail(detail)
			}

			for {
				select {
				case <-e.ctx.Done():
					return
				case <-done:
					print()
					return
				case <-time.After(100 * time.Millisecond):
					print()
				}
			}
		}()
		if _, err := io.CopyN(inst, in, 1); err != nil {
			inst.failed = true
			ui.FailStep("failed to read payload")
			return fmt.Errorf("failed to read binary: %w", err)
		}
		log.Print("Started receiving binary")
		close(started)
	}

	// Now copy the rest of the file
	if _, err := io.Copy(inst, in); err != nil {
		inst.failed = true
		ui.FailStep("failed to copy payload")
		return fmt.Errorf("failed to copy to installer: %w", err)
	}
	detail := fmt.Sprintf("%s @ %s/s", humanReadableBytes(inst.Received()), humanReadableBytes(inst.Rate()))
	ui.UpdateDetail(detail)
	ui.DoneStep(detail)
	if !cfg.NoBinary && !cfg.EnvFile {
		ui.StartStep(runStepDetect)
	}
	return nil
}

func (e *ttyExecer) printf(format string, a ...any) {
	fmt.Fprintf(e.rw, format, a...)
}

type netFlags struct {
	net           string
	tsVer         string
	tsExit        string
	tsTags        []string
	tsAuthKey     string
	macvlanMac    string
	macvlanVlan   int
	macvlanParent string
	publish       []string
}

func netFlagsFromRun(flags cli.RunFlags) netFlags {
	return netFlags{
		net:           flags.Net,
		tsVer:         flags.TsVer,
		tsExit:        flags.TsExit,
		tsTags:        flags.TsTags,
		tsAuthKey:     flags.TsAuthKey,
		macvlanMac:    flags.MacvlanMac,
		macvlanVlan:   flags.MacvlanVlan,
		macvlanParent: flags.MacvlanParent,
		publish:       flags.Publish,
	}
}

func netFlagsFromStage(flags cli.StageFlags) netFlags {
	return netFlags{
		net:           flags.Net,
		tsVer:         flags.TsVer,
		tsExit:        flags.TsExit,
		tsTags:        flags.TsTags,
		tsAuthKey:     flags.TsAuthKey,
		macvlanMac:    flags.MacvlanMac,
		macvlanVlan:   flags.MacvlanVlan,
		macvlanParent: flags.MacvlanParent,
		publish:       flags.Publish,
	}
}

func (e *ttyExecer) fileInstaller(flags netFlags, argsIn []string) FileInstallerCfg {
	var args []string
	if len(argsIn) > 0 {
		args = argsIn
	}
	ic := e.installerCfg()
	return FileInstallerCfg{
		InstallerCfg: ic,
		Network: NetworkOpts{
			Interfaces: flags.net,
			Tailscale: TailscaleOpts{
				Version:  flags.tsVer,
				Tags:     flags.tsTags,
				ExitNode: flags.tsExit,
				AuthKey:  flags.tsAuthKey,
			},
			Macvlan: MacvlanOpts{
				Parent: flags.macvlanParent,
				Mac:    flags.macvlanMac,
				VLAN:   flags.macvlanVlan,
			},
		},
		Args:        args,
		PayloadName: e.payloadName,
		NewCmd:      e.newCmd,
		Publish:     flags.publish,
	}
}

func (e *ttyExecer) installerCfg() InstallerCfg {
	return InstallerCfg{
		ServiceName:  e.sn,
		User:         e.user,
		Printer:      e.printf,
		ClientOut:    e.rw,
		ClientCloser: sessionCloser{e.rawCloser},
	}
}

func (e *ttyExecer) runCmdFunc(flags cli.RunFlags, argsIn []string) error {
	if e.sn == SystemService {
		return fmt.Errorf("cannot run, reserved service name")
	}
	cfg := e.fileInstaller(netFlagsFromRun(flags), argsIn)
	cfg.Pull = flags.Pull
	return e.install("run", e.payloadReader(), cfg)
}

func (e *ttyExecer) copyCmdFunc(dest string) error {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return fmt.Errorf("copy requires a destination")
	}
	dest = strings.TrimPrefix(dest, "./")
	if strings.HasPrefix(dest, "/") {
		return fmt.Errorf("copy destination must be relative")
	}
	rel := dest
	if rel == "data" || strings.HasPrefix(rel, "data/") {
		rel = strings.TrimPrefix(rel, "data")
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "" {
		return fmt.Errorf("copy destination must include a file name")
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("invalid copy destination %q", dest)
	}
	if err := e.s.ensureDirs(e.sn, e.user); err != nil {
		return fmt.Errorf("failed to ensure directories: %w", err)
	}
	dstPath := filepath.Join(e.s.serviceDataDir(e.sn), rel)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	tmpf, err := os.CreateTemp(filepath.Dir(dstPath), "yeet-copy-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	if _, err := io.Copy(tmpf, e.rw); err != nil {
		tmpf.Close()
		os.Remove(tmpf.Name())
		return fmt.Errorf("failed to copy file: %w", err)
	}
	if err := tmpf.Close(); err != nil {
		os.Remove(tmpf.Name())
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Rename(tmpf.Name(), dstPath); err != nil {
		os.Remove(tmpf.Name())
		return fmt.Errorf("failed to move file in place: %w", err)
	}
	if err := os.Chmod(dstPath, 0644); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}
	return nil
}

type sessionCloser struct {
	io.Closer
}

func (s sessionCloser) Close() error {
	if s.Closer != nil {
		// If the closer supports Exit, call Exit(0).
		if closer, ok := s.Closer.(interface{ Exit(int) }); ok {
			closer.Exit(0)
		}
	}
	return nil
}

func (e *ttyExecer) dockerCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("docker requires a subcommand")
	}
	subcmd := args[0]
	args = args[1:]
	if len(args) > 0 {
		return fmt.Errorf("docker %s takes no arguments", subcmd)
	}
	switch subcmd {
	case "pull":
		return e.dockerPullCmdFunc()
	case "update":
		return e.dockerUpdateCmdFunc()
	default:
		return fmt.Errorf("unknown docker command %q", subcmd)
	}
}

func (e *ttyExecer) dockerComposeServiceCmd() (*svc.DockerComposeService, error) {
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service type: %w", err)
	}
	if st != db.ServiceTypeDockerCompose {
		return nil, fmt.Errorf("service %q is not a docker compose service", e.sn)
	}
	docker, err := e.s.dockerComposeService(e.sn)
	if err != nil {
		return nil, err
	}
	docker.NewCmd = e.newCmd
	return docker, nil
}

func (e *ttyExecer) dockerPullCmdFunc() error {
	docker, err := e.dockerComposeServiceCmd()
	if err != nil {
		return err
	}
	return docker.Pull()
}

func (e *ttyExecer) dockerUpdateCmdFunc() error {
	ui := e.newProgressUI("docker update")
	ui.Start()
	defer ui.Stop()
	ui.StartStep("Update service")
	// Stop the spinner so compose output has a clean line to write to.
	ui.Suspend()
	docker, err := e.dockerComposeServiceCmd()
	if err != nil {
		ui.FailStep(err.Error())
		return err
	}
	if err := docker.Update(); err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep("")
	return nil
}

func (e *ttyExecer) stageCmdFunc(subcmd string, flags cli.StageFlags, args []string) error {
	if e.sn == SystemService {
		return fmt.Errorf("cannot stage system service")
	}
	fi := e.fileInstaller(netFlagsFromStage(flags), args)
	fi.Pull = flags.Pull
	if err := e.s.ensureDirs(e.sn, e.user); err != nil {
		return fmt.Errorf("failed to ensure directories: %w", err)
	}
	fi.NoBinary = true
	switch subcmd {
	case "show":
		sv, err := e.s.serviceView(e.sn)
		if err != nil {
			log.Printf("%v", err)
		}
		fmt.Fprintf(e.rw, "%s\n", asJSON(sv))
	case "clear":
		return fmt.Errorf("not implemented")
	case "stage", "commit":
		fi.StageOnly = subcmd == "stage"
		var ui *runUI
		if !fi.StageOnly {
			ui = e.newProgressUI("stage")
			ui.Start()
			defer ui.Stop()
			fi.Printer = ui.Printer
			fi.UI = ui
		}
		inst, err := NewFileInstaller(e.s, fi)
		if err != nil {
			return fmt.Errorf("failed to create installer: %w", err)
		}
		if err := inst.Close(); err != nil {
			return fmt.Errorf("failed to close installer: %w", err)
		}
		if len(flags.Publish) > 0 {
			if err := e.applyPublishToCompose(flags.Publish); err != nil {
				return fmt.Errorf("failed to apply publish ports: %w", err)
			}
		}
		if fi.StageOnly {
			if ui == nil {
				fmt.Fprintf(e.rw, "Staged service %q\n", e.sn)
			}
		}
	default:
		return fmt.Errorf("invalid argument %q", subcmd)
	}
	return nil
}

func (e *ttyExecer) applyPublishToCompose(publish []string) error {
	if len(publish) == 0 {
		return nil
	}
	service, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	af := service.AsStruct().Artifacts
	if af == nil {
		return fmt.Errorf("compose file not found")
	}
	path, ok := af.Staged(db.ArtifactDockerComposeFile)
	if !ok {
		path, ok = af.Latest(db.ArtifactDockerComposeFile)
	}
	if !ok {
		return fmt.Errorf("compose file not found")
	}
	return updateComposePorts(path, e.sn, publish)
}

func updateComposePorts(path, serviceName string, publish []string) error {
	ports := make([]string, 0, len(publish))
	for _, entry := range publish {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			ports = append(ports, trimmed)
		}
	}
	if len(ports) == 0 {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return err
	}
	servicesRaw, ok := doc["services"]
	if !ok {
		return fmt.Errorf("compose file missing services")
	}
	services, ok := servicesRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("compose services are not a map")
	}
	serviceRaw, ok := services[serviceName]
	if !ok {
		return fmt.Errorf("compose service %q not found", serviceName)
	}
	serviceMap, ok := serviceRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("compose service %q is malformed", serviceName)
	}
	serviceMap["ports"] = ports
	services[serviceName] = serviceMap
	doc["services"] = services
	updated, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, updated, 0644)
}

func (e *ttyExecer) startCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot start system service")
	}
	return e.runAction("start", "Start service", func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Start(); err != nil {
			return fmt.Errorf("failed to start service: %w", err)
		}
		return nil
	})
}

func (e *ttyExecer) stopCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot stop system service")
	}
	return e.runAction("stop", "Stop service", func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Stop(); err != nil {
			return fmt.Errorf("failed to stop service: %w", err)
		}
		return nil
	})
}

func (e *ttyExecer) rollbackCmdFunc() error {
	ui := e.newProgressUI("rollback")
	ui.Start()
	defer ui.Stop()

	ui.StartStep("Select generation")
	_, s, err := e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
		if s.Generation == 0 {
			return fmt.Errorf("no generation to rollback")
		}
		minG := s.LatestGeneration - maxGenerations
		gen := s.Generation - 1
		if gen < minG {
			return fmt.Errorf("generation %d is too old, earliest rollback is %d", gen, minG)
		}
		if gen == 0 {
			return fmt.Errorf("generation %d is the oldest, cannot rollback", s.Generation)
		}
		s.Generation = gen
		return nil
	})
	if err != nil {
		ui.FailStep(err.Error())
		return fmt.Errorf("failed to rollback service: %w", err)
	}
	gen := s.Generation
	ui.DoneStep(fmt.Sprintf("generation=%d", gen))

	ui.StartStep("Install generation")
	cfg := e.installerCfg()
	i, err := e.s.NewInstaller(cfg)
	if err != nil {
		ui.FailStep(err.Error())
		return fmt.Errorf("failed to create installer: %w", err)
	}
	i.NewCmd = e.newCmd
	if err := i.InstallGen(gen); err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep(fmt.Sprintf("generation=%d", gen))
	return nil
}

func (e *ttyExecer) restartCmdFunc() error {
	return e.runAction("restart", "Restart service", func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Restart(); err != nil {
			return fmt.Errorf("failed to restart service: %w", err)
		}
		return nil
	})
}

func (e *ttyExecer) editCmdFunc(flags cli.EditFlags) error {
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return err
	}

	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	editConfig := flags.Config

	var srcPath string

	editConfigFn := func(cfg any) error {
		bs, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal systemd config: %w", err)
		}
		srcf, err := createTmpFile()
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}
		defer srcf.Close()
		srcPath = srcf.Name()
		if _, err := io.Copy(srcf, bytes.NewReader(bs)); err != nil {
			return fmt.Errorf("failed to write to temp file: %w", err)
		}
		return nil
	}

	var systemdUnitsBeingEdited []string
	af := sv.AsStruct().Artifacts
	if editConfig {
		if err := editConfigFn(sv); err != nil {
			return fmt.Errorf("failed to edit config: %w", err)
		}
	} else {
		switch st {
		case db.ServiceTypeDockerCompose:
			srcPath, _ = af.Latest(db.ArtifactDockerComposeFile)
		case db.ServiceTypeSystemd:
			if len(af) == 0 {
				return fmt.Errorf("no unit files found")
			}
			srcf, err := createTmpFile()
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer srcf.Close()

			count := 0
			for _, name := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactSystemdTimerFile} {
				path, ok := af.Latest(name)
				if !ok {
					continue
				}
				if count > 0 {
					fmt.Fprintf(srcf, "\n\n")
				}
				fmt.Fprintf(srcf, editUnitsSeparator, name)
				fmt.Fprintf(srcf, "\n\n")
				systemdUnitsBeingEdited = append(systemdUnitsBeingEdited, path)
				f, err := os.Open(path)
				if err != nil {
					return fmt.Errorf("failed to open unit file: %w", err)
				}
				if _, err := io.Copy(srcf, f); err != nil {
					return fmt.Errorf("failed to write to temp file: %w", err)
				}
				count++
			}
			if err := srcf.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}
			srcPath = srcf.Name()
		}
	}

	tmpPath, err := copyToTmpFile(srcPath)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	if err := e.editFile(tmpPath); err != nil {
		return fmt.Errorf("failed to edit file: %w", err)
	}

	if same, err := fileutil.Identical(srcPath, tmpPath); err != nil {
		return err
	} else if same {
		e.printf("No changes detected\n")
		return nil
	}

	if editConfig {
		bs, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to read temp file: %w", err)
		}
		var s2 db.Service
		if err := json.Unmarshal(bs, &s2); err != nil {
			return fmt.Errorf("failed to unmarshal temp file: %w", err)
		}
		_, _, err = e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
			*s = s2
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to update service: %w", err)
		}
		i, err := e.s.NewInstaller(e.installerCfg())
		if err != nil {
			return fmt.Errorf("failed to create installer: %w", err)
		}
		i.NewCmd = e.newCmd
		return i.InstallGen(s2.Generation)
	}

	installFile := func() error {
		f, err := os.Open(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to open temp file: %w", err)
		}
		defer f.Close()
		icfg := e.fileInstaller(netFlags{}, nil)
		fi, err := NewFileInstaller(e.s, icfg)
		if err != nil {
			return fmt.Errorf("failed to create installer: %w", err)
		}
		defer fi.Close()
		if _, err := io.Copy(fi, f); err != nil {
			fi.Fail()
			return fmt.Errorf("failed to copy temp file to installer: %w", err)
		}
		return fi.Close()
	}

	switch st {
	case db.ServiceTypeDockerCompose:
		if editConfig {
			return fmt.Errorf("not implemented")
		}
		return installFile()
	case db.ServiceTypeSystemd:
		if editConfig {
			return fmt.Errorf("not implemented")
		}
		bs, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to read temp file: %w", err)
		}
		submatches := editUnitsSeparatorRe.FindAllSubmatch(bs, -1)
		separateContents := editUnitsSeparatorRe.Split(string(bs), -1)
		if len(separateContents) < 1 {
			return fmt.Errorf("no unit files found")
		}
		separateContents = separateContents[1:] // Skip the first split which is empty
		if len(separateContents) != len(systemdUnitsBeingEdited) || len(submatches) != len(systemdUnitsBeingEdited) {
			return fmt.Errorf("mismatched number of unit files and contents")
		}
		newArtifacts := make(map[db.ArtifactName]string)
		for i, content := range separateContents {
			name := string(submatches[i][1])
			content = strings.TrimSpace(content)
			tmpf, err := createTmpFile()
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer os.Remove(tmpf.Name())
			defer tmpf.Close()
			if _, err := tmpf.WriteString(content); err != nil {
				return fmt.Errorf("failed to write to temp file: %w", err)
			}
			if err := tmpf.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}
			p, ok := af.Latest(db.ArtifactName(name))
			if !ok {
				return fmt.Errorf("no unit file found for %q", name)
			}
			binPath := fileutil.UpdateVersion(p)
			if err := fileutil.CopyFile(tmpf.Name(), binPath); err != nil {
				return fmt.Errorf("failed to copy temp file to binary path: %w", err)
			}
			newArtifacts[db.ArtifactName(name)] = binPath
		}
		_, _, err = e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
			for name, path := range newArtifacts {
				s.Artifacts[name].Refs["staged"] = path
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to update artifacts: %w", err)
		}
		i, err := e.s.NewInstaller(e.installerCfg())
		if err != nil {
			return fmt.Errorf("failed to create installer: %w", err)
		}
		i.NewCmd = e.newCmd
		return i.Install()
	default:
		return fmt.Errorf("unsupported service type: %v", st)
	}
}

func createTmpFile() (*os.File, error) {
	return os.CreateTemp("", "catch-tmp-*")
}

func copyToTmpFile(src string) (string, error) {
	tmpf, err := createTmpFile()
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	if src != "" {
		if err := fileutil.CopyFile(src, tmpf.Name()); err != nil {
			return "", fmt.Errorf("failed to copy file: %w", err)
		}
	}
	tmpf.Close()
	return tmpf.Name(), nil
}

func setWinsize(f *os.File, w, h int) {
	unix.IoctlSetWinsize(int(f.Fd()), syscall.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(h),
		Col: uint16(w),
	})
}

func (e *ttyExecer) editFile(path string) error {
	if !e.isPty {
		return fmt.Errorf("edit requires a pty, please run with a TTY")
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	cmd := e.newCmd(editor, path)
	term := e.ptyReq.Term
	if term == "" {
		term = "xterm"
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", term))
	return cmd.Run()
}

func (s *Server) envFile(sv db.ServiceView, staged bool) (string, error) {
	af := sv.AsStruct().Artifacts
	if staged {
		ef, _ := af.Staged(db.ArtifactEnvFile)
		return ef, nil
	}
	ef, _ := af.Latest(db.ArtifactEnvFile)
	return ef, nil
}

func (s *Server) printEnv(w io.Writer, sv db.ServiceView, staged bool) error {
	ef, err := s.envFile(sv, staged)
	if err != nil {
		return err
	}
	if ef == "" {
		if staged {
			return fmt.Errorf("no staged env file found")
		}
		return fmt.Errorf("no env file found")
	}
	b, err := os.ReadFile(ef)
	if err != nil {
		return fmt.Errorf("failed to read env file: %w", err)
	}
	fmt.Fprintf(w, "%s\n", b)
	return nil
}

func (e *ttyExecer) envCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("env requires a subcommand")
	}
	subcmd := args[0]
	args = args[1:]
	switch subcmd {
	case "show":
		flags, rest, err := cli.ParseEnvShow(args)
		if err != nil {
			return err
		}
		if len(rest) > 0 {
			return fmt.Errorf("env show takes no arguments")
		}
		sv, err := e.s.serviceView(e.sn)
		if err != nil {
			return err
		}
		return e.s.printEnv(e.rw, sv, flags.Staged)
	case "edit":
		if len(args) > 0 {
			return fmt.Errorf("env edit takes no arguments")
		}
		return e.editEnvCmdFunc()
	case "copy":
		if len(args) > 0 {
			return fmt.Errorf("env copy takes no arguments")
		}
		return e.envCopyCmdFunc()
	case "set":
		assignments, err := parseEnvAssignments(args)
		if err != nil {
			return err
		}
		return e.envSetCmdFunc(assignments)
	default:
		return fmt.Errorf("unknown env command %q", subcmd)
	}
}

func (e *ttyExecer) editEnvCmdFunc() error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	af := sv.AsStruct().Artifacts
	srcPath, _ := af.Latest(db.ArtifactEnvFile)
	tmpPath, err := copyToTmpFile(srcPath)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	if err := e.editFile(tmpPath); err != nil {
		return fmt.Errorf("failed to edit env file: %w", err)
	}

	if srcPath != "" {
		if same, err := fileutil.Identical(srcPath, tmpPath); err != nil {
			return err
		} else if same {
			e.printf("No changes detected\n")
			return nil
		}
	} else {
		if st, err := os.Stat(tmpPath); err == nil && st.Size() == 0 {
			e.printf("No changes detected\n")
			return nil
		}
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	defer f.Close()
	icfg := e.fileInstaller(netFlags{}, nil)
	icfg.EnvFile = true
	fi, err := NewFileInstaller(e.s, icfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	defer fi.Close()
	if _, err := io.Copy(fi, f); err != nil {
		fi.Fail()
		return fmt.Errorf("failed to copy temp file to installer: %w", err)
	}
	return fi.Close()
}

func (e *ttyExecer) envCopyCmdFunc() error {
	cfg := e.fileInstaller(netFlags{}, nil)
	cfg.EnvFile = true
	return e.install("env", e.payloadReader(), cfg)
}

type envAssignment struct {
	Key   string
	Value string
}

var envLineRe = regexp.MustCompile(`^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)\s*=`)

func parseEnvAssignments(args []string) ([]envAssignment, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("env set requires at least one KEY=VALUE assignment")
	}
	seen := make(map[string]int, len(args))
	assignments := make([]envAssignment, 0, len(args))
	for _, arg := range args {
		key, value, err := splitEnvAssignment(arg)
		if err != nil {
			return nil, err
		}
		if idx, ok := seen[key]; ok {
			assignments[idx].Value = value
			continue
		}
		seen[key] = len(assignments)
		assignments = append(assignments, envAssignment{Key: key, Value: value})
	}
	return assignments, nil
}

func splitEnvAssignment(arg string) (string, string, error) {
	i := strings.Index(arg, "=")
	if i <= 0 {
		return "", "", fmt.Errorf("invalid env assignment %q (expected KEY=VALUE)", arg)
	}
	key := arg[:i]
	value := arg[i+1:]
	if strings.TrimSpace(key) != key {
		return "", "", fmt.Errorf("invalid env key %q (contains whitespace)", key)
	}
	if !isValidEnvKey(key) {
		return "", "", fmt.Errorf("invalid env key %q", key)
	}
	return key, value, nil
}

func isValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				return false
			}
			continue
		}
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func applyEnvAssignments(contents []byte, assignments []envAssignment) ([]byte, bool, error) {
	if len(assignments) == 0 {
		return contents, false, fmt.Errorf("no env assignments provided")
	}
	raw := string(contents)
	hadTrailingNewline := strings.HasSuffix(raw, "\n")
	raw = strings.TrimSuffix(raw, "\n")

	var lines []string
	if raw != "" {
		lines = strings.Split(raw, "\n")
	}

	updates := make(map[string]string, len(assignments))
	order := make([]string, 0, len(assignments))
	for _, a := range assignments {
		if _, ok := updates[a.Key]; !ok {
			order = append(order, a.Key)
		}
		updates[a.Key] = a.Value
	}

	updated := make(map[string]bool, len(assignments))
	changed := false
	for i, line := range lines {
		matches := envLineRe.FindStringSubmatch(line)
		if len(matches) == 0 {
			continue
		}
		key := matches[2]
		val, ok := updates[key]
		if !ok {
			continue
		}
		if val == "" {
			lines = append(lines[:i], lines[i+1:]...)
			i--
			changed = true
			updated[key] = true
			continue
		}
		newLine := matches[1] + key + "=" + val
		if newLine != line {
			lines[i] = newLine
			changed = true
		}
		updated[key] = true
	}

	for _, key := range order {
		if updated[key] {
			continue
		}
		val := updates[key]
		if val == "" {
			continue
		}
		lines = append(lines, key+"="+val)
		changed = true
	}

	if !changed {
		return contents, false, nil
	}

	out := strings.Join(lines, "\n")
	if out != "" || hadTrailingNewline || len(lines) > 0 {
		out += "\n"
	}
	return []byte(out), true, nil
}

func (e *ttyExecer) envSetCmdFunc(assignments []envAssignment) error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	ef, err := e.s.envFile(sv, false)
	if err != nil {
		return err
	}
	var contents []byte
	if ef != "" {
		contents, err = os.ReadFile(ef)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to read env file: %w", err)
		}
	}
	updated, changed, err := applyEnvAssignments(contents, assignments)
	if err != nil {
		return err
	}
	if !changed {
		e.printf("No changes detected\n")
		return nil
	}
	cfg := e.fileInstaller(netFlags{}, nil)
	cfg.EnvFile = true
	return e.install("env", bytes.NewReader(updated), cfg)
}

func (e *ttyExecer) enableCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot install, reserved service name")
	}
	runner, err := e.serviceRunner()
	if err != nil {
		return err
	}
	enabler, ok := runner.(ServiceEnabler)
	if !ok {
		return fmt.Errorf("service does not support enable")
	}
	return enabler.Enable()
}

func (e *ttyExecer) disableCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot disable system service")
	}

	runner, err := e.serviceRunner()
	if err != nil {
		return err
	}
	enabler, ok := runner.(ServiceEnabler)
	if !ok {
		return fmt.Errorf("service does not support disable")
	}
	return enabler.Disable()
}

func (e *ttyExecer) logsCmdFunc(flags cli.LogsFlags) error {
	// We don't support logs on the system service.
	if e.sn == SystemService {
		return fmt.Errorf("cannot show logs for system service")
	}
	// TODO(shayne): Make tailing optional
	runner, err := e.serviceRunner()
	if err != nil {
		return fmt.Errorf("failed to get service runner: %w", err)
	}
	return runner.Logs(&svc.LogOptions{Follow: flags.Follow, Lines: flags.Lines})
}

func (e *ttyExecer) statusCmdFunc(flags cli.StatusFlags) error {
	formatOut := flags.Format

	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	if !dv.Valid() {
		return fmt.Errorf("no services found")
	}

	var statuses []ServiceStatusData

	if e.sn == SystemService {
		systemdStatuses, err := e.s.SystemdStatuses()
		if err != nil {
			return fmt.Errorf("failed to get systemd statuses: %w", err)
		}
		for sn, status := range systemdStatuses {
			service, err := e.s.serviceView(sn)
			if err != nil {
				return err
			}
			statuses = append(statuses, ServiceStatusData{
				ServiceName: sn,
				ServiceType: ServiceDataTypeForService(service),
				ComponentStatus: []ComponentStatusData{
					{
						Name:   sn,
						Status: ComponentStatusFromServiceStatus(status),
					},
				},
			})
		}
		composeStatuses, err := e.s.DockerComposeStatuses()
		if err != nil {
			return fmt.Errorf("failed to get all docker compose statuses: %w", err)
		}
		for sn, cs := range composeStatuses {
			serviceType := ServiceDataTypeDocker
			if service, err := e.s.serviceView(sn); err == nil {
				serviceType = ServiceDataTypeForService(service)
			}
			if len(cs) == 0 {
				statuses = append(statuses, ServiceStatusData{
					ServiceName: sn,
					ServiceType: serviceType,
					ComponentStatus: []ComponentStatusData{
						{
							Name:   sn,
							Status: ComponentStatusUnknown,
						},
					},
				})
				continue
			}
			data := ServiceStatusData{
				ServiceName:     sn,
				ServiceType:     serviceType,
				ComponentStatus: []ComponentStatusData{},
			}
			for cn, status := range cs {
				data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
					Name:   cn,
					Status: ComponentStatusFromServiceStatus(status),
				})
			}
			statuses = append(statuses, data)
		}
	} else {
		service, err := e.s.serviceView(e.sn)
		if err != nil {
			return fmt.Errorf("failed to get service type: %w", err)
		}
		st := service.ServiceType()
		data := ServiceStatusData{
			ServiceName:     e.sn,
			ServiceType:     ServiceDataTypeForService(service),
			ComponentStatus: []ComponentStatusData{},
		}
		switch st {
		case db.ServiceTypeSystemd:
			status, err := e.s.SystemdStatus(e.sn)
			if err != nil {
				return fmt.Errorf("failed to get systemd status: %w", err)
			}
			data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
				Name:   e.sn,
				Status: ComponentStatusFromServiceStatus(status),
			})
		case db.ServiceTypeDockerCompose:
			cs, err := e.s.DockerComposeStatus(e.sn)
			if err != nil {
				if err == svc.ErrDockerStatusUnknown {
					data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
						Name:   e.sn,
						Status: ComponentStatusUnknown,
					})
					break
				}
				return fmt.Errorf("failed to get docker compose statuses: %w", err)
			}
			if len(cs) == 0 {
				data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
					Name:   e.sn,
					Status: ComponentStatusUnknown,
				})
				return nil
			}
			for cn, status := range cs {
				data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
					Name:   cn,
					Status: ComponentStatusFromServiceStatus(status),
				})
			}
		}
		statuses = append(statuses, data)
	}
	slices.SortFunc(statuses, func(a, b ServiceStatusData) int {
		return strings.Compare(a.ServiceName, b.ServiceName)
	})
	for _, status := range statuses {
		slices.SortFunc(status.ComponentStatus, func(a, b ComponentStatusData) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	if formatOut == "json" {
		return json.NewEncoder(e.rw).Encode(statuses)
	}
	if formatOut == "json-pretty" {
		encoder := json.NewEncoder(e.rw)
		encoder.SetIndent("", "  ")
		return encoder.Encode(statuses)
	}

	w := tabwriter.NewWriter(e.rw, 0, 0, 3, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "SERVICE\tTYPE\tCONTAINER\tSTATUS\t")

	for _, status := range statuses {
		for _, component := range status.ComponentStatus {
			if status.ServiceType == ServiceDataTypeDocker {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", status.ServiceName, status.ServiceType, component.Name, component.Status)
			} else {
				fmt.Fprintf(w, "%s\t%s\t-\t%s\t\n", status.ServiceName, status.ServiceType, component.Status)
			}
		}
	}
	return nil
}

func (e *ttyExecer) cronCmdFunc(cronexpr string, args []string) error {
	oncal, err := cronutil.CronToCalender(cronexpr)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	cfg := e.fileInstaller(netFlags{}, args)
	cfg.Timer = &svc.TimerConfig{
		OnCalendar: oncal,
		Persistent: true, // This should be an option keyvalue in the future
	}
	return e.install("cron", e.payloadReader(), cfg)
}

func (e *ttyExecer) removeCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot remove system service")
	}
	runner, err := e.serviceRunner()
	if err != nil {
		if errors.Is(err, errNoServiceConfigured) {
			report, err := e.s.RemoveService(e.sn)
			if err != nil {
				return fmt.Errorf("failed to cleanup service %q: %w", e.sn, err)
			}
			e.printRemoveWarnings(report)
			e.printf("service %q not found\n", e.sn)
			return nil
		}
		return fmt.Errorf("failed to get service runner: %w", err)
	}
	// Confirm the removal of the service.
	if ok, err := cmdutil.Confirm(e.rw, e.rw, fmt.Sprintf("Are you sure you want to remove service %q?", e.sn)); err != nil {
		return fmt.Errorf("failed to confirm removal: %w", err)
	} else if !ok {
		return nil
	}

	if err := runner.Remove(); err != nil {
		if errors.Is(err, svc.ErrNotInstalled) {
			// Systemd service is not installed
			e.printf("warning: systemd service %q was not installed\n", e.sn)
		} else {
			e.printf("warning: failed to stop/remove service %q: %v\n", e.sn, err)
		}
	}
	report, err := e.s.RemoveService(e.sn)
	if err != nil {
		return fmt.Errorf("failed to cleanup service %q: %w", e.sn, err)
	}
	e.printRemoveWarnings(report)
	return nil
}

func (e *ttyExecer) printRemoveWarnings(report *RemoveReport) {
	if report == nil {
		return
	}
	for _, warn := range report.Warnings {
		e.printf("warning: %v\n", warn)
	}
}

// ServiceRunner is an interface for the minimal set of methods required to
// manage a service.
type ServiceRunner interface {
	SetNewCmd(func(string, ...string) *exec.Cmd)

	Start() error
	Stop() error
	Restart() error

	Logs(opts *svc.LogOptions) error

	Remove() error
}

// ServiceEnabler is an interface extension for services that can be enabled and
// disabled.
type ServiceEnabler interface {
	Enable() error
	Disable() error
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

func (e *ttyExecer) serviceRunner() (ServiceRunner, error) {
	if e.serviceRunnerFn != nil {
		return e.serviceRunnerFn()
	}
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service type: %w", err)
	}
	var service ServiceRunner
	switch st {
	case db.ServiceTypeSystemd:
		systemd, err := e.s.systemdService(e.sn)
		if err != nil {
			return nil, err
		}
		service = &systemdServiceRunner{SystemdService: systemd}
	case db.ServiceTypeDockerCompose:
		docker, err := e.s.dockerComposeService(e.sn)
		if err != nil {
			return nil, err
		}
		service = &dockerComposeServiceRunner{DockerComposeService: docker}
	default:
		return nil, fmt.Errorf("unhandled service type %q", st)
	}
	if service != nil {
		service.SetNewCmd(e.newCmd)
	}
	return service, nil
}

type systemdServiceRunner struct {
	*svc.SystemdService
	newCmd func(string, ...string) *exec.Cmd
}

func (s *systemdServiceRunner) SetNewCmd(f func(string, ...string) *exec.Cmd) {
	s.newCmd = f
}

func (s *systemdServiceRunner) Start() error {
	return s.SystemdService.Start()
}

func (s *systemdServiceRunner) Stop() error {
	return s.SystemdService.Stop()
}

func (s *systemdServiceRunner) Restart() error {
	return s.SystemdService.Restart()
}

// Enable enables the service and starts it.
func (s *systemdServiceRunner) Enable() error {
	if err := s.SystemdService.Enable(); err != nil {
		return err
	}
	return s.SystemdService.Start()
}

// Disable stops and disables the service.
func (s *systemdServiceRunner) Disable() error {
	if err := s.SystemdService.Stop(); err != nil {
		return err
	}
	return s.SystemdService.Disable()
}

func (s *systemdServiceRunner) Logs(opts *svc.LogOptions) error {
	if opts == nil {
		opts = &svc.LogOptions{}
	}
	args := []string{"--no-pager", "--output=cat"}
	if opts.Follow {
		args = append(args, "--follow")
	}
	if opts.Lines > 0 {
		args = append(args, "--lines="+strconv.Itoa(opts.Lines))
	}
	args = append(args, "--unit="+s.SystemdService.Name())
	c := s.newCmd("journalctl", args...)
	if err := c.Start(); err != nil {
		return fmt.Errorf("failed to start journalctl: %w", err)
	}
	if err := c.Wait(); err != nil {
		return fmt.Errorf("failed to wait for journalctl: %w", err)
	}
	return nil
}

func (s *systemdServiceRunner) Remove() error {
	if err := s.SystemdService.Stop(); err != nil {
		return err
	}
	return s.SystemdService.Uninstall()
}

type dockerComposeServiceRunner struct {
	*svc.DockerComposeService
}

func (s *dockerComposeServiceRunner) SetNewCmd(f func(string, ...string) *exec.Cmd) {
	s.NewCmd = f
}

func (s *dockerComposeServiceRunner) Start() error {
	return s.DockerComposeService.Start()
}

func (s *dockerComposeServiceRunner) Stop() error {
	return s.DockerComposeService.Stop()
}

func (s *dockerComposeServiceRunner) Restart() error {
	return s.DockerComposeService.Restart()
}

func (s *dockerComposeServiceRunner) Logs(opts *svc.LogOptions) error {
	return s.DockerComposeService.Logs(opts)
}

func (s *dockerComposeServiceRunner) Remove() error {
	return s.DockerComposeService.Remove()
}

// Add this method to the ttyExecer struct
func (e *ttyExecer) eventsCmdFunc(flags cli.EventsFlags) error {
	ch := make(chan Event)
	all := flags.All
	defer e.s.RemoveEventListener(e.s.AddEventListener(ch, func(et Event) bool {
		if all {
			return true
		}
		return et.ServiceName == e.sn
	}))

	for {
		select {
		case event := <-ch:
			e.printf("Received event: %v\n", event)
		case <-e.ctx.Done():
			return nil
		}
	}
}

func (e *ttyExecer) umountCmdFunc(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("invalid number of arguments")
	}
	mountName := args[0]
	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	vol, ok := dv.Volumes().GetOk(mountName)
	if !ok {
		return fmt.Errorf("volume %q not found", mountName)
	}
	m := &systemdMounter{e: e, v: *vol.AsStruct()}
	if err := m.umount(); err != nil {
		return fmt.Errorf("failed to umount %s: %w", vol.Path(), err)
	}

	d := dv.AsStruct()
	delete(d.Volumes, mountName)
	if err := e.s.cfg.DB.Set(d); err != nil {
		return fmt.Errorf("failed to save data: %w", err)
	}

	return nil
}

func (e *ttyExecer) mountCmdFunc(flags cli.MountFlags, args []string) error {
	if len(args) == 0 {
		dv, err := e.s.cfg.DB.Get()
		if err != nil {
			return fmt.Errorf("failed to get services: %w", err)
		}
		tw := tabwriter.NewWriter(e.rw, 0, 0, 3, ' ', 0)
		defer tw.Flush()
		fmt.Fprintln(tw, "NAME\tSRC\tPATH\tTYPE\tOPTS")
		for _, v := range dv.AsStruct().Volumes {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Src, v.Path, v.Type, v.Opts)
		}
		return nil
	}
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("invalid number of arguments")
	}
	source := args[0]
	_, srcPath, ok := strings.Cut(source, ":")
	if !ok {
		return fmt.Errorf("source %q must be in the format host:path", source)
	}
	var mountName string
	if len(args) == 1 {
		mountName = filepath.Base(srcPath)
	} else {
		mountName = args[1]
	}

	if strings.Contains(mountName, "/") {
		return fmt.Errorf("target cannot contain a /")
	}

	mountType := flags.Type
	// Check the appropriate mounter is installed by stating /sbin/mount.<type>.
	mountCmd := fmt.Sprintf("/sbin/mount.%s", mountType)
	if _, err := os.Stat(mountCmd); err != nil {
		return fmt.Errorf("mount command %q not found", mountCmd)
	}

	opts := flags.Opts
	target := filepath.Join(e.s.cfg.MountsRoot, mountName)
	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	if dv.Volumes().Contains(mountName) {
		return fmt.Errorf("volume %q already exists; please remove it first", mountName)
	}
	deps := flags.Deps
	d := dv.AsStruct()
	vol := db.Volume{
		Name: mountName,
		Src:  source,
		Path: target,
		Type: mountType,
		Opts: opts,
		Deps: strings.Join(deps, " "),
	}
	mak.Set(&d.Volumes, mountName, &vol)
	if err := e.s.cfg.DB.Set(d); err != nil {
		return fmt.Errorf("failed to save data: %w", err)
	}
	m := &systemdMounter{v: vol}

	if err := m.mount(); err != nil {
		return fmt.Errorf("failed to mount %s at %s: %w", source, target, err)
	}

	fmt.Fprintf(e.rw, "Mounted %s at %s\n", source, target)
	return nil
}

func (e *ttyExecer) tsCmdFunc(args []string) error {
	if e.sn == SystemService || e.sn == CatchService {
		return errors.New("tailscale command not supported for sys or catch service")
	}
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return fmt.Errorf("failed to get service view: %w", err)
	}
	if !sv.TSNet().Valid() {
		return errors.New("service is not connected to tailscale")
	}
	sock := filepath.Join(e.s.serviceRunDir(e.sn), "tailscaled.sock")
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("tailscaled socket not found: %w", err)
	}
	ts, err := e.s.getTailscaleBinary(sv.TSNet().Version())
	if err != nil {
		return fmt.Errorf("failed to get tailscale binary: %w", err)
	}
	args = append([]string{
		"--socket=" + sock,
	}, args...)
	c := e.newCmd(ts, args...)
	if err := c.Run(); err != nil {
		return fmt.Errorf("failed to run tailscale command: %w", err)
	}
	return nil
}

func (e *ttyExecer) ipCmdFunc() error {
	if e.sn == CatchService {
		st, err := e.s.cfg.LocalClient.StatusWithoutPeers(e.ctx)
		if err != nil {
			return fmt.Errorf("failed to get IP address: %w", err)
		}
		for _, ip := range st.TailscaleIPs {
			fmt.Fprintln(e.rw, ip)
		}
		return nil
	}

	args := []string{"-o", "-4", "addr", "list"}
	if e.sn != SystemService {
		sv, err := e.s.serviceView(e.sn)
		if err != nil {
			return fmt.Errorf("failed to get service view: %w", err)
		}
		if _, ok := sv.AsStruct().Artifacts.Gen(db.ArtifactNetNSService, sv.Generation()); ok {
			netns := fmt.Sprintf("yeet-%s-ns", e.sn)
			args = append([]string{"netns", "exec", netns, "ip"}, args...)
		}
	}
	ips, err := listIPv4Addrs(args)
	if err != nil {
		return fmt.Errorf("failed to get IP addresses: %w", err)
	}
	for _, ip := range ips {
		// Skip 127.0.0.1
		fmt.Fprintln(e.rw, ip.IP)
	}
	return nil
}
