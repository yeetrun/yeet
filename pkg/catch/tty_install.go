// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shayne/yeet/pkg/cli"
	"github.com/shayne/yeet/pkg/copyutil"
	"github.com/shayne/yeet/pkg/cronutil"
	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/svc"
)

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

func (e *ttyExecer) copyCmdFunc(args []string) error {
	parsed, err := parseCopyExecArgs(args)
	if err != nil {
		return err
	}
	if parsed.From != "" {
		return e.copyFromRemote(parsed)
	}
	return e.copyToRemote(parsed)
}

type copyExecArgs struct {
	From      string
	To        string
	Recursive bool
	Archive   bool
	Compress  bool
}

func parseCopyExecArgs(args []string) (copyExecArgs, error) {
	var out copyExecArgs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--from":
			if i+1 >= len(args) {
				return copyExecArgs{}, fmt.Errorf("copy --from requires a value")
			}
			out.From = args[i+1]
			i++
		case "--to":
			if i+1 >= len(args) {
				return copyExecArgs{}, fmt.Errorf("copy --to requires a value")
			}
			out.To = args[i+1]
			i++
		case "--recursive", "-r":
			out.Recursive = true
		case "--archive", "-a":
			out.Archive = true
			out.Recursive = true
		case "--compress", "-z":
			out.Compress = true
		default:
			return copyExecArgs{}, fmt.Errorf("invalid copy argument %q", args[i])
		}
	}
	if out.From == "" && out.To == "" {
		return copyExecArgs{}, fmt.Errorf("copy requires --from or --to")
	}
	if out.From != "" && out.To != "" {
		return copyExecArgs{}, fmt.Errorf("copy requires either --from or --to")
	}
	return out, nil
}

func (e *ttyExecer) copyToRemote(parsed copyExecArgs) error {
	dest, err := normalizeCopyRelPath(parsed.To, parsed.Archive)
	if err != nil {
		return err
	}
	if dest == "" && !parsed.Archive {
		return fmt.Errorf("copy destination must include a file name")
	}
	if err := e.s.ensureDirs(e.sn, e.user); err != nil {
		return fmt.Errorf("failed to ensure directories: %w", err)
	}
	dstRoot := filepath.Join(e.s.serviceDataDir(e.sn), dest)
	if dest == "" {
		dstRoot = e.s.serviceDataDir(e.sn)
	}
	if parsed.Archive {
		if err := os.MkdirAll(filepath.Dir(dstRoot), 0755); err != nil {
			return fmt.Errorf("failed to create destination directory: %w", err)
		}
		stageDir, err := os.MkdirTemp(filepath.Dir(dstRoot), "yeet-copy-*")
		if err != nil {
			return fmt.Errorf("failed to create temp dir: %w", err)
		}
		defer os.RemoveAll(stageDir)
		input := io.Reader(e.rw)
		var gz *gzip.Reader
		if parsed.Compress {
			gz, err = gzip.NewReader(e.rw)
			if err != nil {
				return fmt.Errorf("failed to read compressed payload: %w", err)
			}
			defer gz.Close()
			input = gz
		}
		if err := copyutil.ExtractTarWithOptions(input, stageDir, copyutil.ExtractOptions{}); err != nil {
			return fmt.Errorf("failed to extract archive: %w", err)
		}
		if err := copyutil.MoveTree(stageDir, dstRoot); err != nil {
			return fmt.Errorf("failed to move staged files: %w", err)
		}
		return nil
	}
	if strings.HasSuffix(parsed.To, "/") {
		return fmt.Errorf("copy destination must include a file name")
	}
	if err := os.MkdirAll(filepath.Dir(dstRoot), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	tmpf, err := os.CreateTemp(filepath.Dir(dstRoot), "yeet-copy-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	input := io.Reader(e.rw)
	var gz *gzip.Reader
	if parsed.Compress {
		gz, err = gzip.NewReader(e.rw)
		if err != nil {
			return fmt.Errorf("failed to read compressed payload: %w", err)
		}
		defer gz.Close()
		input = gz
	}
	if _, err := io.Copy(tmpf, input); err != nil {
		tmpf.Close()
		os.Remove(tmpf.Name())
		return fmt.Errorf("failed to copy file: %w", err)
	}
	if err := tmpf.Close(); err != nil {
		os.Remove(tmpf.Name())
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Rename(tmpf.Name(), dstRoot); err != nil {
		os.Remove(tmpf.Name())
		return fmt.Errorf("failed to move file in place: %w", err)
	}
	if err := os.Chmod(dstRoot, 0644); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}
	return nil
}

func (e *ttyExecer) copyFromRemote(parsed copyExecArgs) error {
	src, err := normalizeCopyRelPath(parsed.From, true)
	if err != nil {
		return err
	}
	srcPath := filepath.Join(e.s.serviceDataDir(e.sn), src)
	if src == "" {
		srcPath = e.s.serviceDataDir(e.sn)
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	base := ""
	if src != "" {
		base = filepath.Base(src)
	}
	if info.IsDir() {
		if !parsed.Recursive {
			return fmt.Errorf("copy requires recursive mode for directories")
		}
		if err := copyutil.WriteHeader(e.rw, "dir", base); err != nil {
			return err
		}
		payload := io.Writer(e.rw)
		var gz *gzip.Writer
		if parsed.Compress {
			gz = gzip.NewWriter(e.rw)
			payload = gz
		}
		if err := copyutil.TarDirectory(payload, srcPath, ""); err != nil {
			if gz != nil {
				gz.Close()
			}
			return err
		}
		if gz != nil {
			if err := gz.Close(); err != nil {
				return err
			}
		}
		return nil
	}
	if base == "" {
		return fmt.Errorf("copy requires a file path")
	}
	if err := copyutil.WriteHeader(e.rw, "file", base); err != nil {
		return err
	}
	payload := io.Writer(e.rw)
	var gz *gzip.Writer
	if parsed.Compress {
		gz = gzip.NewWriter(e.rw)
		payload = gz
	}
	if parsed.Archive {
		if err := copyutil.TarFile(payload, srcPath, base); err != nil {
			if gz != nil {
				gz.Close()
			}
			return err
		}
	} else {
		f, err := os.Open(srcPath)
		if err != nil {
			if gz != nil {
				gz.Close()
			}
			return err
		}
		if _, err := io.Copy(payload, f); err != nil {
			f.Close()
			if gz != nil {
				gz.Close()
			}
			return err
		}
		if err := f.Close(); err != nil {
			if gz != nil {
				gz.Close()
			}
			return err
		}
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			return err
		}
	}
	return nil
}

func normalizeCopyRelPath(raw string, allowEmpty bool) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "./")
	if raw == "." {
		raw = ""
	}
	if raw == "data" || strings.HasPrefix(raw, "data/") {
		raw = strings.TrimPrefix(raw, "data")
		raw = strings.TrimPrefix(raw, "/")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("copy path must be relative")
	}
	if raw == "" {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("copy path must not be empty")
	}
	clean := filepath.Clean(raw)
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("copy path must not be empty")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid copy path %q", raw)
	}
	return clean, nil
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
