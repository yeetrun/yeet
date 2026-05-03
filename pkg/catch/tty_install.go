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

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/copyutil"
	"github.com/yeetrun/yeet/pkg/cronutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
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
	return e.installLinux(action, in, cfg)
}

func (e *ttyExecer) runInstall(action string, in io.Reader, cfg FileInstallerCfg) error {
	if e.installFunc != nil {
		return e.installFunc(action, in, cfg)
	}
	return e.install(action, in, cfg)
}

func (e *ttyExecer) installLinux(action string, in io.Reader, cfg FileInstallerCfg) (retErr error) {
	ui := e.startInstallUI(action)
	defer ui.Stop()

	inst, err := e.newFileInstallerWithUI(&cfg, ui)
	if err != nil {
		return err
	}
	defer closeInstallerWithUI(inst, ui, &retErr)

	ui.StartStep(runStepUpload)
	if err := e.copyInstallPayload(in, cfg, ui, inst); err != nil {
		return err
	}
	finishInstallUploadStep(ui, inst, cfg)
	return nil
}

func (e *ttyExecer) startInstallUI(action string) *runUI {
	ui := e.newProgressUI(action)
	ui.Start()
	return ui
}

func (e *ttyExecer) newFileInstallerWithUI(cfg *FileInstallerCfg, ui *runUI) (*FileInstaller, error) {
	cfg.Printer = ui.Printer
	cfg.UI = ui
	inst, err := NewFileInstaller(e.s, *cfg)
	if err != nil {
		ui.FailStep("failed to create installer")
		return nil, fmt.Errorf("failed to create installer: %w", err)
	}
	return inst, nil
}

func closeInstallerWithUI(inst *FileInstaller, ui *runUI, retErr *error) {
	if cerr := inst.Close(); cerr != nil && *retErr == nil {
		ui.FailStep("install failed")
		*retErr = cerr
	}
}

func (e *ttyExecer) copyInstallPayload(in io.Reader, cfg FileInstallerCfg, ui *runUI, inst *FileInstaller) error {
	if cfg.EnvFile {
		return copyRemainingInstallPayload(inst, in, ui)
	}
	started, stopProgress := e.startInstallUploadProgress(ui, inst)
	defer stopProgress()
	if err := copyInitialInstallByte(inst, in, ui); err != nil {
		return err
	}
	close(started)
	return copyRemainingInstallPayload(inst, in, ui)
}

func (e *ttyExecer) startInstallUploadProgress(ui *runUI, inst *FileInstaller) (chan struct{}, func()) {
	started := make(chan struct{})
	done := make(chan struct{})
	go e.watchInstallUploadProgress(ui, inst, started, done)
	return started, func() { close(done) }
}

func (e *ttyExecer) watchInstallUploadProgress(ui *runUI, inst *FileInstaller, started, done <-chan struct{}) {
	if !e.waitForInstallUploadStart(ui, started, done) {
		return
	}
	e.updateInstallUploadProgressUntilDone(ui, inst, done)
}

func (e *ttyExecer) waitForInstallUploadStart(ui *runUI, started, done <-chan struct{}) bool {
	select {
	case <-e.ctx.Done():
		return false
	case <-started:
		return true
	case <-done:
		return false
	case <-time.After(time.Second):
		ui.FailStep("timeout waiting for bytes")
		closeBestEffort(e.rawCloser)
		return false
	}
}

func (e *ttyExecer) updateInstallUploadProgressUntilDone(ui *runUI, inst *FileInstaller, done <-chan struct{}) {
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-done:
			updateInstallUploadDetail(ui, inst)
			return
		case <-time.After(100 * time.Millisecond):
			updateInstallUploadDetail(ui, inst)
		}
	}
}

func copyInitialInstallByte(inst *FileInstaller, in io.Reader, ui *runUI) error {
	if _, err := io.CopyN(inst, in, 1); err != nil {
		inst.failed = true
		ui.FailStep("failed to read payload")
		return fmt.Errorf("failed to read binary: %w", err)
	}
	log.Print("Started receiving binary")
	return nil
}

func copyRemainingInstallPayload(inst *FileInstaller, in io.Reader, ui *runUI) error {
	if _, err := io.Copy(inst, in); err != nil {
		inst.failed = true
		ui.FailStep("failed to copy payload")
		return fmt.Errorf("failed to copy to installer: %w", err)
	}
	return nil
}

func finishInstallUploadStep(ui *runUI, inst *FileInstaller, cfg FileInstallerCfg) {
	detail := fmt.Sprintf("%s @ %s/s", humanReadableBytes(inst.Received()), humanReadableBytes(inst.Rate()))
	ui.UpdateDetail(detail)
	ui.DoneStep(detail)
	if !cfg.NoBinary && !cfg.EnvFile {
		ui.StartStep(runStepDetect)
	}
}

func updateInstallUploadDetail(ui *runUI, inst *FileInstaller) {
	detail := fmt.Sprintf("%s @ %s/s", humanReadableBytes(inst.Received()), humanReadableBytes(inst.Rate()))
	ui.UpdateDetail(detail)
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
	return e.runInstall("run", e.payloadReader(), cfg)
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
	out, err := parseCopyExecFlags(args)
	if err != nil {
		return copyExecArgs{}, err
	}
	return out, out.validate()
}

func parseCopyExecFlags(args []string) (copyExecArgs, error) {
	var out copyExecArgs
	for i := 0; i < len(args); i++ {
		consumed, err := out.applyArg(args, i)
		if err != nil {
			return copyExecArgs{}, err
		}
		i += consumed
	}
	return out, nil
}

func (c *copyExecArgs) applyArg(args []string, i int) (int, error) {
	switch args[i] {
	case "--from":
		value, err := copyArgValue(args, i, "--from")
		if err != nil {
			return 0, err
		}
		c.From = value
		return 1, nil
	case "--to":
		value, err := copyArgValue(args, i, "--to")
		if err != nil {
			return 0, err
		}
		c.To = value
		return 1, nil
	case "--recursive", "-r":
		c.Recursive = true
	case "--archive", "-a":
		c.Archive = true
		c.Recursive = true
	case "--compress", "-z":
		c.Compress = true
	default:
		return 0, fmt.Errorf("invalid copy argument %q", args[i])
	}
	return 0, nil
}

func copyArgValue(args []string, i int, flag string) (string, error) {
	if i+1 >= len(args) {
		return "", fmt.Errorf("copy %s requires a value", flag)
	}
	return args[i+1], nil
}

func (c copyExecArgs) validate() error {
	if c.From == "" && c.To == "" {
		return fmt.Errorf("copy requires --from or --to")
	}
	if c.From != "" && c.To != "" {
		return fmt.Errorf("copy requires either --from or --to")
	}
	return nil
}

func (e *ttyExecer) copyToRemote(parsed copyExecArgs) error {
	dstRoot, err := e.prepareCopyDestination(parsed.To, parsed.Archive)
	if err != nil {
		return err
	}
	if parsed.Archive {
		return e.copyArchiveToRemote(dstRoot, parsed.Compress)
	}
	return e.copyFileToRemote(parsed.To, dstRoot, parsed.Compress)
}

func (e *ttyExecer) copyFromRemote(parsed copyExecArgs) error {
	srcPath, base, info, err := e.remoteCopySource(parsed.From)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return e.copyDirectoryFromRemote(srcPath, base, parsed)
	}
	if base == "" {
		return fmt.Errorf("copy requires a file path")
	}
	return e.copyFileFromRemote(srcPath, base, parsed)
}

func normalizeCopyRelPath(raw string, allowEmpty bool) (string, error) {
	raw = canonicalCopyRelPath(raw)
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("copy path must be relative")
	}
	if isEmptyCopyPath(raw) {
		return normalizeEmptyCopyPath(allowEmpty)
	}
	clean := filepath.Clean(raw)
	if isEmptyCopyPath(clean) {
		return normalizeEmptyCopyPath(allowEmpty)
	}
	if copyPathEscapesRoot(clean) {
		return "", fmt.Errorf("invalid copy path %q", raw)
	}
	return clean, nil
}

func (e *ttyExecer) prepareCopyDestination(raw string, allowEmpty bool) (string, error) {
	dest, err := normalizeCopyRelPath(raw, allowEmpty)
	if err != nil {
		return "", err
	}
	if dest == "" && !allowEmpty {
		return "", fmt.Errorf("copy destination must include a file name")
	}
	if err := e.s.ensureDirs(e.sn, e.user); err != nil {
		return "", fmt.Errorf("failed to ensure directories: %w", err)
	}
	return copyDestinationRoot(e.s.serviceDataDir(e.sn), dest), nil
}

func copyDestinationRoot(dataDir, dest string) string {
	if dest == "" {
		return dataDir
	}
	return filepath.Join(dataDir, dest)
}

func (e *ttyExecer) copyArchiveToRemote(dstRoot string, compressed bool) (err error) {
	if err := os.MkdirAll(filepath.Dir(dstRoot), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(dstRoot), "yeet-copy-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer removeAllBestEffort(stageDir)

	input, closer, err := copyPayloadReader(e.rw, compressed)
	if err != nil {
		return fmt.Errorf("failed to read compressed payload: %w", err)
	}
	defer closeWithError(closer, &err, "failed to close compressed payload")

	if err = copyutil.ExtractTarWithOptions(input, stageDir, copyutil.ExtractOptions{}); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}
	if err = copyutil.MoveTree(stageDir, dstRoot); err != nil {
		return fmt.Errorf("failed to move staged files: %w", err)
	}
	return nil
}

func (e *ttyExecer) copyFileToRemote(rawTo, dstRoot string, compressed bool) (err error) {
	if strings.HasSuffix(rawTo, "/") {
		return fmt.Errorf("copy destination must include a file name")
	}
	if err := os.MkdirAll(filepath.Dir(dstRoot), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	tmpf, err := os.CreateTemp(filepath.Dir(dstRoot), "yeet-copy-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpf.Name()
	cleanup := true
	defer func() {
		if cleanup {
			removeBestEffort(tmpPath)
		}
	}()

	input, closer, err := copyPayloadReader(e.rw, compressed)
	if err != nil {
		closeBestEffort(tmpf)
		return fmt.Errorf("failed to read compressed payload: %w", err)
	}
	defer closeWithError(closer, &err, "failed to close compressed payload")

	if _, err = io.Copy(tmpf, input); err != nil {
		closeBestEffort(tmpf)
		return fmt.Errorf("failed to copy file: %w", err)
	}
	if err = tmpf.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err = os.Rename(tmpPath, dstRoot); err != nil {
		return fmt.Errorf("failed to move file in place: %w", err)
	}
	cleanup = false
	if err = os.Chmod(dstRoot, 0644); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}
	return nil
}

func (e *ttyExecer) remoteCopySource(raw string) (string, string, os.FileInfo, error) {
	src, err := normalizeCopyRelPath(raw, true)
	if err != nil {
		return "", "", nil, err
	}
	srcPath := copyDestinationRoot(e.s.serviceDataDir(e.sn), src)
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", "", nil, err
	}
	return srcPath, copySourceBase(src), info, nil
}

func copySourceBase(src string) string {
	if src == "" {
		return ""
	}
	return filepath.Base(src)
}

func (e *ttyExecer) copyDirectoryFromRemote(srcPath, base string, parsed copyExecArgs) error {
	if !parsed.Recursive {
		return fmt.Errorf("copy requires recursive mode for directories")
	}
	if err := copyutil.WriteHeader(e.rw, "dir", base); err != nil {
		return err
	}
	payload, closer := copyPayloadWriter(e.rw, parsed.Compress)
	if err := copyutil.TarDirectory(payload, srcPath, ""); err != nil {
		closeBestEffort(closer)
		return err
	}
	return closePayload(closer)
}

func (e *ttyExecer) copyFileFromRemote(srcPath, base string, parsed copyExecArgs) error {
	if err := copyutil.WriteHeader(e.rw, "file", base); err != nil {
		return err
	}
	payload, closer := copyPayloadWriter(e.rw, parsed.Compress)
	if err := copyRemoteFilePayload(payload, srcPath, base, parsed.Archive); err != nil {
		closeBestEffort(closer)
		return err
	}
	return closePayload(closer)
}

func copyRemoteFilePayload(w io.Writer, srcPath, base string, archive bool) error {
	if archive {
		return copyutil.TarFile(w, srcPath, base)
	}
	return copyRegularFile(w, srcPath)
}

func copyRegularFile(w io.Writer, srcPath string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		closeBestEffort(f)
		return err
	}
	return f.Close()
}

func copyPayloadReader(r io.Reader, compressed bool) (io.Reader, io.Closer, error) {
	if !compressed {
		return r, nil, nil
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return gz, gz, nil
}

func copyPayloadWriter(w io.Writer, compressed bool) (io.Writer, io.Closer) {
	if !compressed {
		return w, nil
	}
	gz := gzip.NewWriter(w)
	return gz, gz
}

func closePayload(closer io.Closer) error {
	if closer == nil {
		return nil
	}
	return closer.Close()
}

func canonicalCopyRelPath(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "./")
	if raw == "." {
		return ""
	}
	if raw == "data" || strings.HasPrefix(raw, "data/") {
		raw = strings.TrimPrefix(raw, "data")
		raw = strings.TrimPrefix(raw, "/")
	}
	return raw
}

func isEmptyCopyPath(path string) bool {
	return path == "" || path == "."
}

func normalizeEmptyCopyPath(allowEmpty bool) (string, error) {
	if allowEmpty {
		return "", nil
	}
	return "", fmt.Errorf("copy path must not be empty")
}

func copyPathEscapesRoot(path string) bool {
	return path == ".." || strings.HasPrefix(path, ".."+string(os.PathSeparator)) || filepath.IsAbs(path)
}

func writefChecked(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func closeWithError(closer io.Closer, retErr *error, msg string) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil && *retErr == nil {
		*retErr = fmt.Errorf("%s: %w", msg, err)
	}
}

func closeBestEffort(closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		log.Printf("failed to close resource: %v", err)
	}
}

func removeBestEffort(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to remove %q: %v", path, err)
	}
}

func removeAllBestEffort(path string) {
	if err := os.RemoveAll(path); err != nil {
		log.Printf("failed to remove %q: %v", path, err)
	}
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
	fi, err := e.stageInstallerCfg(flags, args)
	if err != nil {
		return err
	}
	switch subcmd {
	case "show":
		return e.showStage()
	case "clear":
		if len(args) > 0 {
			return fmt.Errorf("stage clear takes no arguments")
		}
		return e.clearStage()
	case "stage", "commit":
		return e.stageOrCommit(subcmd, flags, fi)
	default:
		return fmt.Errorf("invalid argument %q", subcmd)
	}
}

func (e *ttyExecer) stageInstallerCfg(flags cli.StageFlags, args []string) (FileInstallerCfg, error) {
	fi := e.fileInstaller(netFlagsFromStage(flags), args)
	fi.Pull = flags.Pull
	if err := e.s.ensureDirs(e.sn, e.user); err != nil {
		return FileInstallerCfg{}, fmt.Errorf("failed to ensure directories: %w", err)
	}
	fi.NoBinary = true
	return fi, nil
}

func (e *ttyExecer) showStage() error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		log.Printf("%v", err)
	}
	return writefChecked(e.rw, "%s\n", asJSON(sv))
}

func (e *ttyExecer) stageOrCommit(subcmd string, flags cli.StageFlags, fi FileInstallerCfg) error {
	if subcmd == "stage" {
		return e.stageOnly(fi)
	}
	return e.commitStage(flags, fi)
}

func (e *ttyExecer) stageOnly(fi FileInstallerCfg) error {
	fi.StageOnly = true
	if err := e.closeNewStageInstaller(fi); err != nil {
		return err
	}
	return writefChecked(e.rw, "Staged service %q\n", e.sn)
}

func (e *ttyExecer) commitStage(flags cli.StageFlags, fi FileInstallerCfg) error {
	ui := e.stageCommitUI(&fi)
	defer ui.Stop()
	if err := e.closeNewStageInstaller(fi); err != nil {
		return err
	}
	return e.applyStagePublish(flags)
}

func (e *ttyExecer) stageCommitUI(fi *FileInstallerCfg) *runUI {
	ui := e.newProgressUI("stage")
	ui.Start()
	fi.Printer = ui.Printer
	fi.UI = ui
	return ui
}

func (e *ttyExecer) closeNewStageInstaller(fi FileInstallerCfg) error {
	if e.closeNewStageInstallerFunc != nil {
		return e.closeNewStageInstallerFunc(fi)
	}
	inst, err := NewFileInstaller(e.s, fi)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	if err := inst.Close(); err != nil {
		return fmt.Errorf("failed to close installer: %w", err)
	}
	return nil
}

func (e *ttyExecer) applyStagePublish(flags cli.StageFlags) error {
	if len(flags.Publish) == 0 {
		return nil
	}
	if err := e.applyPublishToCompose(flags.Publish); err != nil {
		return fmt.Errorf("failed to apply publish ports: %w", err)
	}
	return nil
}

type clearStageResult struct {
	stagedPaths      []string
	removedArtifacts int
	removedImages    int
}

func (r clearStageResult) hasChanges() bool {
	return r.removedArtifacts != 0 || r.removedImages != 0
}

func (e *ttyExecer) clearStage() error {
	result, err := e.clearStageRefs()
	if err != nil {
		return err
	}

	if !result.hasChanges() {
		return writefChecked(e.rw, "No staged changes for %q\n", e.sn)
	}

	referenced, err := e.referencedArtifactPaths()
	if err != nil {
		return err
	}
	removedFiles := removeUnreferencedStageFiles(result.stagedPaths, referenced)

	return writefChecked(e.rw, "Cleared staged refs for %q (artifacts: %d, images: %d). Removed %d files.\n", e.sn, result.removedArtifacts, result.removedImages, removedFiles)
}

func (e *ttyExecer) clearStageRefs() (clearStageResult, error) {
	var result clearStageResult
	_, err := e.s.cfg.DB.MutateData(func(d *db.Data) error {
		return e.clearStageRefsInData(d, &result)
	})
	return result, err
}

func (e *ttyExecer) clearStageRefsInData(d *db.Data, result *clearStageResult) error {
	svc, ok := d.Services[e.sn]
	if !ok {
		return errServiceNotFound
	}
	result.removeArtifactRefs(svc)
	result.removeImageRefs(d.Images, e.sn)
	return nil
}

func (r *clearStageResult) removeArtifactRefs(svc *db.Service) {
	for _, art := range svc.Artifacts {
		if art == nil || art.Refs == nil {
			continue
		}
		if p, ok := art.Refs[db.ArtifactRef("staged")]; ok {
			delete(art.Refs, db.ArtifactRef("staged"))
			r.stagedPaths = append(r.stagedPaths, p)
			r.removedArtifacts++
		}
	}
}

func (r *clearStageResult) removeImageRefs(images map[db.ImageRepoName]*db.ImageRepo, serviceName string) {
	for repoName, repo := range images {
		svcName, err := parseRepo(string(repoName))
		if err != nil || svcName != serviceName {
			continue
		}
		if repo == nil || repo.Refs == nil {
			continue
		}
		if _, ok := repo.Refs[db.ImageRef("staged")]; ok {
			delete(repo.Refs, db.ImageRef("staged"))
			r.removedImages++
		}
	}
}

func (e *ttyExecer) referencedArtifactPaths() (map[string]struct{}, error) {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]struct{})
	if sv.Valid() {
		for _, art := range sv.AsStruct().Artifacts {
			if art == nil {
				continue
			}
			for _, p := range art.Refs {
				referenced[p] = struct{}{}
			}
		}
	}
	return referenced, nil
}

func removeUnreferencedStageFiles(stagedPaths []string, referenced map[string]struct{}) int {
	removedFiles := 0
	for _, p := range stagedPaths {
		if p == "" {
			continue
		}
		if _, ok := referenced[p]; ok {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Printf("failed to remove staged file %q: %v", p, err)
			continue
		}
		removedFiles++
	}
	return removedFiles
}

func (e *ttyExecer) applyPublishToCompose(publish []string) error {
	if len(publish) == 0 {
		return nil
	}
	path, err := e.composePathForPublish()
	if err != nil {
		return err
	}
	return updateComposePorts(path, e.sn, publish)
}

func (e *ttyExecer) composePathForPublish() (string, error) {
	service, err := e.s.serviceView(e.sn)
	if err != nil {
		return "", err
	}
	return composePathFromArtifacts(service.AsStruct().Artifacts)
}

func composePathFromArtifacts(af db.ArtifactStore) (string, error) {
	if af == nil {
		return "", fmt.Errorf("compose file not found")
	}
	path, ok := af.Staged(db.ArtifactDockerComposeFile)
	if !ok {
		path, ok = af.Latest(db.ArtifactDockerComposeFile)
	}
	if !ok {
		return "", fmt.Errorf("compose file not found")
	}
	return path, nil
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
	return e.runInstall("cron", e.payloadReader(), cfg)
}
