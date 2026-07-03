// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type initStorageOptions struct {
	DataDir           string
	DataDirZFS        bool
	ServicesRoot      string
	ServicesRootZFS   bool
	remoteCatchBinary string
}

type initStorageProbe struct {
	Home               string
	ZFSAvailable       bool
	SuggestedZFSPrefix string
}

type initStorageWizardFunc func(io.Reader, io.Writer, initStorageProbe) (initStorageOptions, error)

var (
	prepareInitStorageOptionsFn                            = prepareInitStorageOptions
	runInitStorageWizardFn           initStorageWizardFunc = runInitStorageWizard
	remoteInitExistingCatchStorageFn                       = remoteInitExistingCatchStorage
	remoteInitStorageProbeFn                               = remoteInitStorageProbe
	remoteInitStorageCommandOKFn                           = remoteInitStorageCommandOK
	remoteInitStorageOutputFn                              = remoteInitStorageOutput
)

func initStorageOptionsFromFlags(flags initFlagsParsed) (initStorageOptions, error) {
	storage := initStorageOptions{
		DataDir:      strings.TrimSpace(flags.DataDir),
		ServicesRoot: strings.TrimSpace(flags.ServicesRoot),
	}
	if !flags.ZFS {
		return storage, nil
	}
	if storage.DataDir == "" && storage.ServicesRoot == "" {
		return initStorageOptions{}, fmt.Errorf("--zfs requires --data-dir or --services-root")
	}
	if storage.DataDir != "" {
		storage.DataDirZFS = true
	}
	if storage.ServicesRoot != "" {
		storage.ServicesRootZFS = true
	}
	return storage, nil
}

func (o initStorageOptions) explicit() bool {
	return strings.TrimSpace(o.DataDir) != "" ||
		strings.TrimSpace(o.ServicesRoot) != "" ||
		o.DataDirZFS ||
		o.ServicesRootZFS
}

func (o initStorageOptions) summary() string {
	if !o.explicit() {
		return "defaults"
	}
	parts := make([]string, 0, 2)
	if strings.TrimSpace(o.DataDir) != "" {
		label := "data dir " + o.DataDir
		if o.DataDirZFS {
			label = "data dataset " + o.DataDir
		}
		parts = append(parts, label)
	}
	if strings.TrimSpace(o.ServicesRoot) != "" {
		label := "services root " + o.ServicesRoot
		if o.ServicesRootZFS {
			label = "services dataset " + o.ServicesRoot
		}
		parts = append(parts, label)
	} else {
		parts = append(parts, "services under data dir")
	}
	return strings.Join(parts, "; ")
}

func withInitCatchRemoteBinary(storage initStorageOptions, useSudo bool) initStorageOptions {
	storage.remoteCatchBinary = initCatchRemoteBinaryPath(storage, useSudo)
	return storage
}

func (o initStorageOptions) catchRemoteBinary() string {
	if binary := strings.TrimSpace(o.remoteCatchBinary); binary != "" {
		return binary
	}
	return "./catch"
}

func initCatchRemoteBinaryPath(storage initStorageOptions, useSudo bool) string {
	if useSudo {
		return ""
	}
	servicesRoot := initCatchRemoteServicesRoot(storage)
	if servicesRoot == "" {
		return ""
	}
	return path.Join(servicesRoot, catchServiceName, "run", "catch.install")
}

func initCatchRemoteServicesRoot(storage initStorageOptions) string {
	if servicesRoot := strings.TrimSpace(storage.ServicesRoot); initRemoteAbsolutePath(servicesRoot) {
		return path.Clean(servicesRoot)
	}
	dataDir := strings.TrimSpace(storage.DataDir)
	if !initRemoteAbsolutePath(dataDir) {
		return ""
	}
	return path.Join(path.Clean(dataDir), "services")
}

func initRemoteAbsolutePath(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "/")
}

func prepareInitStorageOptions(ui *initUI, userAtRemote string, useSudo bool, opts initOptions) (initStorageOptions, error) {
	if opts.storage.explicit() {
		return opts.storage, nil
	}
	ui.StartStep("Plan storage")
	existing, installed, err := remoteInitExistingCatchStorageFn(userAtRemote)
	if err != nil {
		ui.Warn(fmt.Sprintf("Warning: could not check existing catch install: %v", err))
	} else if installed {
		ui.DoneStep("existing catch")
		return existing, nil
	}
	if !canPromptInitStorage() {
		ui.DoneStep("defaults")
		return initStorageOptions{}, nil
	}
	probe, err := remoteInitStorageProbeFn(userAtRemote, useSudo)
	if err != nil {
		ui.Warn(fmt.Sprintf("Warning: could not inspect remote storage: %v", err))
		probe = initStorageProbe{Home: defaultInitStorageHome(useSudo)}
	}
	ui.Suspend()
	storage, err := runInitStorageWizardFn(os.Stdin, os.Stdout, probe)
	ui.Resume()
	if err != nil {
		ui.FailStep(err.Error())
		return initStorageOptions{}, err
	}
	ui.DoneStep(storage.summary())
	return storage, nil
}

func canPromptInitStorage() bool {
	return isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
}

func runInitStorageWizard(in io.Reader, out io.Writer, probe initStorageProbe) (initStorageOptions, error) {
	probe = normalizeInitStorageProbe(probe)
	reader := bufio.NewReader(in)
	if _, err := fmt.Fprintln(out, "Storage setup"); err != nil {
		return initStorageOptions{}, err
	}
	storage, err := promptInitDataStorage(reader, out, probe)
	if err != nil {
		return initStorageOptions{}, err
	}
	return promptInitServicesStorage(reader, out, storage, probe)
}

func promptInitDataStorage(reader *bufio.Reader, out io.Writer, probe initStorageProbe) (initStorageOptions, error) {
	storage := initStorageOptions{}
	defaultDataDir := filepath.Join(probe.Home, "yeet-data")
	useDefaultData, err := promptInitYesNo(reader, out, fmt.Sprintf("Use %s for catch data?", defaultDataDir), true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useDefaultData {
		storage.DataDir = defaultDataDir
		return storage, nil
	}
	if probe.ZFSAvailable {
		return promptInitCustomDataStorage(reader, out, storage, probe, defaultDataDir)
	}
	storage.DataDir, err = promptInitValue(reader, out, "Catch data directory", defaultDataDir)
	if err != nil {
		return initStorageOptions{}, err
	}
	return storage, nil
}

func promptInitCustomDataStorage(reader *bufio.Reader, out io.Writer, storage initStorageOptions, probe initStorageProbe, defaultDataDir string) (initStorageOptions, error) {
	useZFS, err := promptInitYesNo(reader, out, "Use a ZFS dataset for catch data?", true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useZFS {
		storage.DataDir, err = promptInitValue(reader, out, "Catch data dataset", suggestedInitDataDataset(probe))
		if err != nil {
			return initStorageOptions{}, err
		}
		storage.DataDirZFS = true
		return storage, nil
	}
	storage.DataDir, err = promptInitValue(reader, out, "Catch data directory", defaultDataDir)
	if err != nil {
		return initStorageOptions{}, err
	}
	return storage, nil
}

func promptInitServicesStorage(reader *bufio.Reader, out io.Writer, storage initStorageOptions, probe initStorageProbe) (initStorageOptions, error) {
	keepServicesUnderData, err := promptInitYesNo(reader, out, "Keep services under the catch data dir?", true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if keepServicesUnderData {
		return storage, nil
	}
	if probe.ZFSAvailable {
		return promptInitCustomServicesStorage(reader, out, storage, probe)
	}
	return promptInitFilesystemServicesRoot(reader, out, storage, probe)
}

func promptInitCustomServicesStorage(reader *bufio.Reader, out io.Writer, storage initStorageOptions, probe initStorageProbe) (initStorageOptions, error) {
	useZFS, err := promptInitYesNo(reader, out, "Use a ZFS dataset for services?", storage.DataDirZFS)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useZFS {
		storage.ServicesRoot, err = promptInitValue(reader, out, "Services dataset", suggestedInitServicesDataset(storage, probe))
		if err != nil {
			return initStorageOptions{}, err
		}
		storage.ServicesRootZFS = true
		return storage, nil
	}
	return promptInitFilesystemServicesRoot(reader, out, storage, probe)
}

func promptInitFilesystemServicesRoot(reader *bufio.Reader, out io.Writer, storage initStorageOptions, probe initStorageProbe) (initStorageOptions, error) {
	var err error
	storage.ServicesRoot, err = promptInitValue(reader, out, "Services root", suggestedInitServicesRootPath(storage, probe))
	if err != nil {
		return initStorageOptions{}, err
	}
	return storage, nil
}

func normalizeInitStorageProbe(probe initStorageProbe) initStorageProbe {
	probe.Home = strings.TrimSpace(probe.Home)
	if probe.Home == "" {
		probe.Home = "/root"
	}
	probe.SuggestedZFSPrefix = strings.Trim(strings.TrimSpace(probe.SuggestedZFSPrefix), "/")
	return probe
}

func promptInitYesNo(r *bufio.Reader, w io.Writer, msg string, def bool) (bool, error) {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	if _, err := fmt.Fprintf(w, "%s %s: ", msg, suffix); err != nil {
		return false, err
	}
	line, err := readInitPromptLine(r)
	if err != nil {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	switch line {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("expected yes or no, got %q", line)
	}
}

func promptInitValue(r *bufio.Reader, w io.Writer, msg string, placeholder string) (string, error) {
	if _, err := fmt.Fprintf(w, "%s [%s]: ", msg, placeholder); err != nil {
		return "", err
	}
	line, err := readInitPromptLine(r)
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = placeholder
	}
	if line == "" {
		return "", fmt.Errorf("%s is required", strings.ToLower(msg))
	}
	return line, nil
}

func readInitPromptLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func suggestedInitDataDataset(probe initStorageProbe) string {
	if probe.SuggestedZFSPrefix != "" {
		return path.Join(probe.SuggestedZFSPrefix, "data")
	}
	return "flash/yeet/data"
}

func suggestedInitServicesDataset(storage initStorageOptions, probe initStorageProbe) string {
	if storage.DataDirZFS {
		parent := path.Dir(strings.Trim(storage.DataDir, "/"))
		if parent != "." && parent != "/" {
			return path.Join(parent, "services")
		}
	}
	if probe.SuggestedZFSPrefix != "" {
		return path.Join(probe.SuggestedZFSPrefix, "services")
	}
	return "flash/yeet/services"
}

func suggestedInitServicesRootPath(storage initStorageOptions, probe initStorageProbe) string {
	home := probe.Home
	if home == "" {
		home = "/root"
	}
	if !storage.DataDirZFS && strings.TrimSpace(storage.DataDir) != "" {
		parent := filepath.Dir(storage.DataDir)
		if parent != "." && parent != string(filepath.Separator) {
			return filepath.Join(parent, "yeet-services")
		}
	}
	return filepath.Join(home, "yeet-services")
}

func defaultInitStorageHome(bool) string {
	return "/root"
}

func remoteInitExistingCatchStorage(userAtRemote string) (initStorageOptions, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, "systemctl cat catch.service 2>/dev/null")
	output, err := cmd.Output()
	if err == nil {
		return initStorageOptionsFromCatchUnit(string(output)), true, nil
	}
	if ctx.Err() != nil {
		return initStorageOptions{}, false, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return initStorageOptions{}, false, nil
	}
	return initStorageOptions{}, false, err
}

func initStorageOptionsFromCatchUnit(unit string) initStorageOptions {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecStart=") {
			return initStorageOptionsFromCatchExecStart(strings.Fields(strings.TrimPrefix(line, "ExecStart=")))
		}
	}
	return initStorageOptions{}
}

func initStorageOptionsFromCatchExecStart(args []string) initStorageOptions {
	storage := initStorageOptions{}
	for i := 1; i < len(args); i++ {
		flag, value, next := initStorageCatchExecStartStorageFlag(args, i)
		i = next
		switch flag {
		case "data-dir":
			storage.DataDir = value
		case "services-root":
			storage.ServicesRoot = value
		}
	}
	return storage
}

func initStorageCatchExecStartStorageFlag(args []string, i int) (string, string, int) {
	arg := strings.TrimSpace(args[i])
	name, value, ok := strings.Cut(arg, "=")
	flag := initStorageCatchStorageFlagName(name)
	if ok {
		return flag, value, i
	}
	if flag == "" || i+1 >= len(args) {
		return "", "", i
	}
	return flag, args[i+1], i + 1
}

func initStorageCatchStorageFlagName(name string) string {
	switch strings.TrimSpace(name) {
	case "--data-dir", "-data-dir":
		return "data-dir"
	case "--services-root", "-services-root":
		return "services-root"
	default:
		return ""
	}
}

func remoteInitStorageProbe(userAtRemote string, useSudo bool) (initStorageProbe, error) {
	home := defaultInitStorageHome(useSudo)
	if !useSudo {
		out, err := remoteInitStorageOutputFn(userAtRemote, "printf '%s\\n' \"$HOME\"")
		if err != nil {
			return initStorageProbe{}, err
		}
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			home = trimmed
		}
	}
	probe := initStorageProbe{Home: home}
	if ok, _ := remoteInitStorageCommandOKFn(userAtRemote, "command -v zfs >/dev/null 2>&1"); !ok {
		return probe, nil
	}
	probe.ZFSAvailable = true
	if pool, err := remoteInitStorageOutputFn(userAtRemote, "zfs list -H -d 0 -o name -t filesystem 2>/dev/null | head -n 1"); err == nil {
		if pool = strings.TrimSpace(pool); pool != "" {
			probe.SuggestedZFSPrefix = path.Join(pool, "yeet")
		}
	}
	return probe, nil
}

func remoteInitStorageCommandOK(userAtRemote, script string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, script)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func remoteInitStorageOutput(userAtRemote, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", userAtRemote, script)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}
