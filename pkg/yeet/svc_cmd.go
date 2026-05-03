// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/ftdetect"
)

var remoteRegistry = cli.RemoteCommandRegistry()

func stageFile(svc, bin string) error {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	payload, cleanup, _, err := openPayloadForUpload(bin, goos, goarch)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := execRemoteFn(context.Background(), svc, []string{"stage"}, payload, false); err != nil {
		return fmt.Errorf("failed to upload file %s to stage: %w", bin, err)
	}
	return nil
}

func missingServiceError(args []string) error {
	name := missingServiceCommandName(args)
	if name == "" {
		return fmt.Errorf("missing service name")
	}
	return fmt.Errorf("%s requires a service name\nRun 'yeet %s --help' for usage", name, name)
}

func missingServiceCommandName(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if len(args) > 1 {
		if _, ok := cli.RemoteGroupInfos()[args[0]]; ok {
			return args[0] + " " + args[1]
		}
	}
	return args[0]
}

func commandNeedsService(args []string) (bool, error) {
	res, ok, err := yargs.ResolveCommandWithRegistry(args, remoteRegistry)
	if err != nil || !ok {
		return false, err
	}
	if len(res.Path) > 0 && res.Path[0] == cli.CommandEvents {
		flags, _, err := cli.ParseEvents(args[1:])
		if err != nil {
			return false, err
		}
		if flags.All {
			return false, nil
		}
	}
	arg, ok := res.PArg(0)
	if !ok {
		return false, nil
	}
	if !cli.IsServiceArgSpec(arg) {
		return false, nil
	}
	return arg.Required, nil
}

type svcCommand struct {
	Name      string
	Args      []string
	CheckArgs []string
	RawArgs   []string
}

func svcCommandFromArgs(args []string) svcCommand {
	cmd := svcCommand{
		Name:      "status",
		CheckArgs: []string{"status"},
		RawArgs:   args,
	}
	if len(args) == 0 {
		return cmd
	}
	cmd.Name = args[0]
	cmd.Args = args[1:]
	cmd.CheckArgs = args
	return cmd
}

type svcCommandRequest struct {
	Command         svcCommand
	Config          *projectConfigLocation
	HostOverride    string
	HostOverrideSet bool
	Service         string
}

func HandleSvcCmd(args []string) error {
	req, err := newSvcCommandRequest(args)
	if err != nil {
		return err
	}
	return handleSvcCommand(context.Background(), req)
}

func newSvcCommandRequest(args []string) (svcCommandRequest, error) {
	command := svcCommandFromArgs(args)
	cfgLoc, err := loadProjectConfigFromCwd()
	if err != nil {
		return svcCommandRequest{}, err
	}

	if err := ensureSvcCommandService(command.CheckArgs); err != nil {
		return svcCommandRequest{}, err
	}

	hostOverride, hostOverrideSet := HostOverride()
	if err := applySvcCommandHost(cfgLoc, hostOverrideSet); err != nil {
		return svcCommandRequest{}, err
	}

	return svcCommandRequest{
		Command:         command,
		Config:          cfgLoc,
		HostOverride:    hostOverride,
		HostOverrideSet: hostOverrideSet,
		Service:         getService(),
	}, nil
}

func ensureSvcCommandService(checkArgs []string) error {
	if serviceOverride != "" {
		return nil
	}
	needsService, err := commandNeedsService(checkArgs)
	if err != nil {
		return err
	}
	if needsService {
		return missingServiceError(checkArgs)
	}
	return nil
}

func applySvcCommandHost(cfgLoc *projectConfigLocation, hostOverrideSet bool) error {
	if serviceOverride == "" || hostOverrideSet || cfgLoc == nil || cfgLoc.Config == nil {
		return nil
	}
	host, err := resolveServiceHost(cfgLoc.Config, serviceOverride)
	if err != nil {
		return err
	}
	if host != "" {
		SetHost(host)
	}
	return nil
}

func handleSvcCommand(ctx context.Context, req svcCommandRequest) error {
	switch req.Command.Name {
	case "env":
		return handleSvcEnv(ctx, req)
	// `run <svc> <file/docker-image> [args...]`
	case "run":
		return handleSvcRun(req)
	case "remove":
		return handleSvcRemove(ctx, req)
	// `copy [-avz] <src> <dst>`
	case "copy":
		return handleSvcCopy(req)
	// `cron <svc> <file> <cronexpr>`
	case "cron":
		return handleSvcCron(req)
	// `stage <svc> <file>`
	case "stage":
		return handleSvcStage(ctx, req)
	case cli.CommandEvents:
		return handleSvcEvents(ctx, req)
	case "status":
		return handleStatusCommand(ctx, req.Command.Args, req.Config, req.HostOverrideSet)
	case "info":
		return handleInfoCommand(ctx, req.Command.Args, req.Config)
	}

	return handleSvcRemote(ctx, req)
}

func handleSvcEnv(ctx context.Context, req svcCommandRequest) error {
	args := req.Command.RawArgs
	if len(args) >= 2 && args[1] == "copy" {
		if len(args) != 3 {
			return fmt.Errorf("env copy requires a file")
		}
		if err := runEnvCopy(args[2]); err != nil {
			return err
		}
		return saveEnvFileConfig(req.Config, req.HostOverride, args[2])
	}
	if len(args) >= 2 && args[1] == "set" {
		if len(args) < 3 {
			return fmt.Errorf("env set requires at least one KEY=VALUE assignment")
		}
		assignments, err := parseEnvAssignments(args[2:])
		if err != nil {
			return err
		}
		setArgs := []string{"env", "set"}
		for _, assignment := range assignments {
			setArgs = append(setArgs, assignment.Key+"="+assignment.Value)
		}
		return execRemoteFn(ctx, req.Service, setArgs, nil, true)
	}
	return handleSvcRemote(ctx, req)
}

type parsedSvcRun struct {
	Payload     string
	Args        []string
	EnvFile     string
	EnvFileArg  string
	EnvFileSet  bool
	Entry       ServiceEntry
	ForceDeploy bool
}

func handleSvcRun(req svcCommandRequest) error {
	cmdArgs := req.Command.Args
	if len(cmdArgs) == 0 {
		return runFromProjectConfig(req.Config, req.HostOverride)
	}
	forceFromConfig, err := shouldRunFromConfigWithForce(cmdArgs)
	if err != nil {
		return err
	}
	if forceFromConfig {
		return runFromProjectConfigWithForce(req.Config, req.HostOverride, true)
	}
	run, err := parseSvcRun(cmdArgs, req.Config, req.HostOverride)
	if err != nil {
		return err
	}
	if err := runWithChanges(run.Payload, run.Args, run.EnvFile, run.Entry, run.ForceDeploy); err != nil {
		return err
	}
	if err := saveRunConfig(req.Config, req.HostOverride, run.Payload, normalizeRunArgs(run.Args)); err != nil {
		return err
	}
	if run.EnvFileSet {
		return saveEnvFileConfig(req.Config, req.HostOverride, run.EnvFileArg)
	}
	return nil
}

func parseSvcRun(cmdArgs []string, cfgLoc *projectConfigLocation, hostOverride string) (parsedSvcRun, error) {
	payload, runArgs, err := splitRunPayloadArgs(cmdArgs)
	if err != nil {
		return parsedSvcRun{}, err
	}
	envFileArg, filteredArgs, envFileSet, err := extractEnvFileFlag(runArgs)
	if err != nil {
		return parsedSvcRun{}, err
	}
	forceDeploy, filteredArgs, err := extractForceFlag(filteredArgs)
	if err != nil {
		return parsedSvcRun{}, err
	}
	entry, hasEntry := serviceEntryForConfig(cfgLoc, hostOverride)
	if hasEntry {
		if err := ensureLockedRunFlags(entry, filteredArgs); err != nil {
			return parsedSvcRun{}, err
		}
	}
	envFile := envFileArg
	if envFile == "" && hasEntry && entry.EnvFile != "" && cfgLoc != nil {
		envFile = resolveEnvFilePath(cfgLoc.Dir, entry.EnvFile)
	}
	return parsedSvcRun{
		Payload:     payload,
		Args:        filteredArgs,
		EnvFile:     envFile,
		EnvFileArg:  envFileArg,
		EnvFileSet:  envFileSet,
		Entry:       entry,
		ForceDeploy: forceDeploy,
	}, nil
}

func handleSvcRemove(ctx context.Context, req svcCommandRequest) error {
	removeFlags, _, err := cli.ParseRemove(req.Command.Args)
	if err != nil {
		return err
	}
	remoteArgs := filterRemoveArgs(req.Command.Args)
	if err := execRemoteFn(ctx, req.Service, append([]string{"remove"}, remoteArgs...), nil, true); err != nil {
		return err
	}
	if removeFlags.CleanConfig {
		return removeServiceConfig(req.Config, req.HostOverride)
	}
	if removeFlags.Yes {
		return nil
	}
	if !hasServiceConfig(req.Config, req.HostOverride) {
		return nil
	}
	ok, err := cmdutil.Confirm(os.Stdin, os.Stdout, fmt.Sprintf("Remove %q from yeet.toml?", req.Service))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return removeServiceConfig(req.Config, req.HostOverride)
}

func handleSvcCopy(req svcCommandRequest) error {
	var cfg *ProjectConfig
	if req.Config != nil {
		cfg = req.Config.Config
	}
	return runCopyCommand(req.Command.Args, cfg)
}

func handleSvcCron(req svcCommandRequest) error {
	cmdArgs := req.Command.Args
	if len(cmdArgs) == 0 {
		return runCronFromProjectConfig(req.Config, req.HostOverride)
	}
	payload := cmdArgs[0]
	cronFields, binArgs, err := splitCronArgs(cmdArgs[1:])
	if err != nil {
		return err
	}
	if err := runCron(payload, cronFields, binArgs); err != nil {
		return err
	}
	return saveCronConfig(req.Config, req.HostOverride, payload, cronFields, binArgs)
}

func handleSvcStage(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) == 1 {
		return runStageBinary(req.Command.Args[0])
	}
	return handleSvcRemote(ctx, req)
}

func handleSvcEvents(ctx context.Context, req svcCommandRequest) error {
	flags, _, err := cli.ParseEvents(req.Command.Args)
	if err != nil {
		return err
	}
	if serviceOverride == "" && !flags.All {
		return missingServiceError(req.Command.RawArgs)
	}
	return handleEventsRPC(ctx, req.Service, flags)
}

func handleSvcRemote(ctx context.Context, req svcCommandRequest) error {
	return execRemoteFn(ctx, req.Service, req.Command.RawArgs, nil, true)
}

var tryRunDockerFn = tryRunDocker
var buildDockerImageForRemoteFn = buildDockerImageForRemote
var tryRunRemoteImageFn = tryRunRemoteImage
var imageExistsFn = imageExists
var pushImageFn = pushImage
var execRemoteDirectFn = execRemote

func splitRunPayloadArgs(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("run requires a payload")
	}
	payloadIdx := -1
	for i := 0; i < len(args); i++ {
		consumed, stop := scanRunFlag(args, &i, false)
		if stop {
			break
		}
		if consumed {
			continue
		}
		payloadIdx = i
		break
	}
	if payloadIdx == -1 {
		return "", nil, fmt.Errorf("run requires a payload")
	}
	payload := args[payloadIdx]
	out := make([]string, 0, len(args)-1)
	out = append(out, args[:payloadIdx]...)
	out = append(out, args[payloadIdx+1:]...)
	return payload, out, nil
}

func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func normalizeRunArgs(args []string) []string {
	args = normalizeArgs(args)
	for i, arg := range args {
		if arg == "--" {
			out := make([]string, 0, len(args)-1)
			out = append(out, args[:i]...)
			out = append(out, args[i+1:]...)
			return out
		}
	}
	return args
}

func splitRunArgsForParsing(args []string) ([]string, []string) {
	for i := 0; i < len(args); i++ {
		consumed, stop := scanRunFlag(args, &i, true)
		if stop {
			if i+1 < len(args) {
				return args[:i], args[i+1:]
			}
			return args[:i], nil
		}
		if consumed {
			continue
		}
		return args[:i], args[i:]
	}
	return args, nil
}

func scanRunFlag(args []string, idx *int, consumeBundledShort bool) (consumed bool, stop bool) {
	arg := args[*idx]
	if arg == "--" {
		return false, true
	}
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return false, false
	}
	specs := cli.RemoteFlagSpecs()["run"]
	if strings.HasPrefix(arg, "--") && len(arg) > 2 {
		return scanLongRunFlag(arg, idx, specs), false
	}
	if strings.Contains(arg, "=") {
		_, ok := specs[flagName(arg)]
		return ok, false
	}
	if len(arg) == 2 {
		return scanShortRunFlag(arg, idx, specs), false
	}
	spec, ok := specs["-"+string(arg[1])]
	if !ok {
		return false, false
	}
	return consumeBundledShort || spec.ConsumesValue, false
}

func scanLongRunFlag(arg string, idx *int, specs map[string]cli.FlagSpec) bool {
	name := flagName(arg)
	spec, ok := specs[name]
	if !ok {
		return false
	}
	if spec.ConsumesValue && !strings.Contains(arg, "=") {
		*idx = *idx + 1
	}
	return true
}

func scanShortRunFlag(arg string, idx *int, specs map[string]cli.FlagSpec) bool {
	spec, ok := specs[arg]
	if !ok {
		return false
	}
	if spec.ConsumesValue {
		*idx = *idx + 1
	}
	return true
}

func flagName(arg string) string {
	if idx := strings.Index(arg, "="); idx != -1 {
		return arg[:idx]
	}
	return arg
}

func rehydrateRunArgs(args []string) []string {
	args = normalizeArgs(args)
	if len(args) == 0 {
		return nil
	}
	flagArgs, payloadArgs := splitRunArgsForParsing(args)
	if len(payloadArgs) == 0 {
		return flagArgs
	}
	out := make([]string, 0, len(flagArgs)+1+len(payloadArgs))
	out = append(out, flagArgs...)
	out = append(out, "--")
	out = append(out, payloadArgs...)
	return out
}

func runRun(payload string, args []string) error {
	if ok, err := tryRunDockerfile(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunFile(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunRemoteImageFn(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunDockerFn(payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("unknown payload: %s", payload)
}

func tryRunDockerfile(path string, args []string) (ok bool, _ error) {
	if filepath.Base(path) != "Dockerfile" {
		return false, nil
	}
	if st, err := os.Stat(path); os.IsNotExist(err) || st != nil && st.IsDir() {
		return false, fmt.Errorf("dockerfile payload does not exist: %s", path)
	} else if err != nil {
		return false, err
	}
	svc := getService()
	tag := fmt.Sprintf("yeet-build-%d", time.Now().UnixNano())
	imageName := fmt.Sprintf("%s:%s", svc, tag)
	if err := buildDockerImageForRemoteFn(context.Background(), path, imageName); err != nil {
		return true, err
	}
	ok, err := tryRunDockerFn(imageName, args)
	_ = exec.Command("docker", "rmi", imageName).Run()
	return ok, err
}

const imageComposeTemplate = `services:
  %s:
    image: %s
    restart: unless-stopped
    volumes:
      - "./:/data"
`

func tryRunRemoteImage(image string, args []string) (ok bool, _ error) {
	if !looksLikeImageRef(image) {
		return false, nil
	}
	svc := getService()
	tmpDir, err := os.MkdirTemp("", "yeet-image-")
	if err != nil {
		return true, err
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to remove temporary directory %s: %v\n", tmpDir, err)
		}
	}()
	composePath := filepath.Join(tmpDir, "compose.yml")
	content := fmt.Sprintf(imageComposeTemplate, svc, image)
	if err := os.WriteFile(composePath, []byte(content), 0o644); err != nil {
		return true, err
	}
	return runFilePayload(composePath, args, false)
}

func looksLikeImageRef(payload string) bool {
	if payload == "" {
		return false
	}
	if strings.ContainsAny(payload, " \t\n\r") {
		return false
	}
	if strings.HasPrefix(payload, "http://") || strings.HasPrefix(payload, "https://") {
		return false
	}
	if strings.Contains(payload, "@") {
		parts := strings.SplitN(payload, "@", 2)
		return parts[0] != "" && parts[1] != ""
	}
	lastSlash := strings.LastIndex(payload, "/")
	lastColon := strings.LastIndex(payload, ":")
	if lastColon == -1 || lastColon < lastSlash {
		return false
	}
	tag := payload[lastColon+1:]
	return tag != "" && !strings.Contains(tag, "/")
}

func tryRunFile(file string, args []string) (ok bool, _ error) {
	if st, err := os.Stat(file); os.IsNotExist(err) || st != nil && st.IsDir() {
		// If the file does not exist or is a directory, it's not an error
		// (yet), it could be another deployment method (i.e. docker)
		if st != nil && st.IsDir() {
			fmt.Fprintf(os.Stderr, "%q is a directory, ignoring\n", file)
		}
		return false, nil
	} else if err != nil {
		// If it's a different error, return it
		return false, err
	}
	return runFilePayload(file, args, true)
}

type runFileUpload struct {
	payload io.ReadCloser
	cleanup func()
	ft      ftdetect.FileType
	goos    string
	goarch  string
}

func runFilePayload(file string, args []string, pushLocalImages bool) (ok bool, _ error) {
	upload, err := prepareRunFileUpload(file, args, pushLocalImages)
	if err != nil {
		return false, err
	}
	defer upload.cleanup()

	svc := getService()
	if err := pushRunFileLocalImages(svc, upload, pushLocalImages); err != nil {
		return false, err
	}
	if err := execRunFilePayload(context.Background(), svc, upload.payload, args); err != nil {
		return false, err
	}
	return true, nil
}

func prepareRunFileUpload(file string, args []string, pushLocalImages bool) (runFileUpload, error) {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return runFileUpload{}, err
	}
	payload, cleanup, ft, err := openPayloadForUpload(file, goos, goarch)
	if err != nil {
		return runFileUpload{}, err
	}
	if err := validateRunFileArgs(ft, args, pushLocalImages); err != nil {
		cleanup()
		return runFileUpload{}, err
	}
	return runFileUpload{
		payload: payload,
		cleanup: cleanup,
		ft:      ft,
		goos:    goos,
		goarch:  goarch,
	}, nil
}

func validateRunFileArgs(ft ftdetect.FileType, args []string, pushLocalImages bool) error {
	if ft != ftdetect.DockerCompose {
		return nil
	}
	flags, _, err := cli.ParseRun(args)
	if err != nil {
		return err
	}
	if len(flags.Publish) > 0 && pushLocalImages {
		return fmt.Errorf("-p/--publish is not supported for docker compose payloads")
	}
	return nil
}

func pushRunFileLocalImages(svc string, upload runFileUpload, pushLocalImages bool) error {
	if upload.ft != ftdetect.DockerCompose || !pushLocalImages {
		return nil
	}
	if err := pushAllLocalImagesFn(svc, upload.goos, upload.goarch); err != nil {
		return fmt.Errorf("failed to push all local images: %w", err)
	}
	return nil
}

func execRunFilePayload(ctx context.Context, svc string, payload io.Reader, args []string) error {
	runArgs := append([]string{"run"}, args...)
	tty := isTerminalFn(int(os.Stdout.Fd()))
	if err := execRemoteFn(ctx, svc, runArgs, payload, tty); err != nil {
		return fmt.Errorf("failed to run service: %w", err)
	}
	return nil
}

func tryRunDocker(image string, args []string) (ok bool, _ error) {
	if !imageExistsFn(image) {
		// If the image does not exist, it's not an error
		return false, nil
	}
	svc := getService()
	if err := pushImageFn(context.Background(), svc, image, "latest"); err != nil {
		return false, fmt.Errorf("failed to push image: %w", err)
	}
	if err := stageDockerArgs(context.Background(), svc, args); err != nil {
		return false, err
	}
	if err := commitDockerStage(context.Background(), svc); err != nil {
		return false, err
	}
	return true, nil
}

func stageDockerArgs(ctx context.Context, svc string, args []string) error {
	if len(args) > 0 {
		stageArgs := append([]string{"stage"}, args...)
		if err := execRemoteDirectFn(ctx, svc, stageArgs, nil, true); err != nil {
			fmt.Println("failed to stage args:", err)
			return fmt.Errorf("failed to stage args: %w", err)
		}
	}
	return nil
}

func commitDockerStage(ctx context.Context, svc string) error {
	if err := execRemoteDirectFn(ctx, svc, []string{"stage", "commit"}, nil, true); err != nil {
		return errors.New("failed to run service")
	}
	return nil
}

func runEnvCopy(file string) (err error) {
	if file == "" {
		return fmt.Errorf("env copy requires a file")
	}
	if st, err := os.Stat(file); err != nil {
		return err
	} else if st.IsDir() {
		return fmt.Errorf("%q is a directory, expected a file", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	svc := getService()
	args := []string{"env", "copy"}
	if err := execRemoteFn(context.Background(), svc, args, f, false); err != nil {
		return err
	}
	return nil
}

type envAssignment struct {
	Key   string
	Value string
}

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
			if !isEnvKeyStart(r) {
				return false
			}
			continue
		}
		if !isEnvKeyChar(r) {
			return false
		}
	}
	return true
}

func isEnvKeyStart(r rune) bool {
	return r == '_' || isASCIILetter(r)
}

func isEnvKeyChar(r rune) bool {
	return isEnvKeyStart(r) || isASCIIDigit(r)
}

func isASCIILetter(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func runCron(file string, cronFields []string, binArgs []string) error {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	payload, cleanup, _, err := openPayloadForUpload(file, goos, goarch)
	if err != nil {
		return err
	}
	defer cleanup()
	if len(cronFields) != 5 {
		return fmt.Errorf("cron expression must have 5 fields, got %d", len(cronFields))
	}
	svc := getService()
	nargs := append([]string{"cron"}, cronFields...)
	if len(binArgs) > 0 {
		nargs = append(nargs, binArgs...)
	}
	tty := isTerminalFn(int(os.Stdout.Fd()))
	return execRemoteFn(context.Background(), svc, nargs, payload, tty)
}

func splitCronArgs(args []string) ([]string, []string, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("cron requires a cron expression")
	}
	cronArgs := args
	var binArgs []string
	for i, arg := range args {
		if arg == "--" {
			cronArgs = args[:i]
			if i+1 < len(args) {
				binArgs = args[i+1:]
			}
			break
		}
	}
	if len(cronArgs) == 1 {
		cronArgs = strings.Fields(cronArgs[0])
	}
	if len(cronArgs) != 5 {
		return nil, nil, fmt.Errorf("cron expression must have 5 fields, got %d", len(cronArgs))
	}
	return cronArgs, binArgs, nil
}

func parseCronSchedule(schedule string) ([]string, error) {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}
	return fields, nil
}

func runStageBinary(file string) error {
	svc := getService()
	if st, err := os.Stat(file); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return execRemote(context.Background(), svc, []string{"stage", file}, nil, true)
	} else if st != nil && st.IsDir() {
		if st.IsDir() {
			fmt.Fprintf(os.Stderr, "%q is a directory, ignoring\n", file)
		}
	}
	if err := stageFile(svc, file); err != nil {
		return err
	}
	return nil
}

type hostStatusData struct {
	Host     string          `json:"host"`
	Services []statusService `json:"services"`
}

type statusService struct {
	ServiceName string            `json:"serviceName"`
	ServiceType string            `json:"serviceType"`
	Components  []statusComponent `json:"components"`
}

type statusComponent struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func handleStatusCommand(ctx context.Context, args []string, cfgLoc *projectConfigLocation, hostOverrideSet bool) error {
	flags, _, err := cli.ParseStatus(args)
	if err != nil {
		return err
	}
	if shouldRenderStatusTable(flags.Format) && serviceOverride != "" {
		return renderStatusTableForService(ctx, Host(), serviceOverride)
	}
	if serviceOverride == "" && shouldRenderStatusTable(flags.Format) {
		return statusMultiHost(ctx, statusHosts(cfgLoc, hostOverrideSet), flags)
	}
	svc := getService()
	statusArgs := append([]string{"status"}, args...)
	return execRemoteFn(ctx, svc, statusArgs, nil, true)
}

func shouldRenderStatusTable(format string) bool {
	format = strings.TrimSpace(format)
	return format == "" || format == "table"
}

func statusHosts(cfgLoc *projectConfigLocation, hostOverrideSet bool) []string {
	if hostOverrideSet || cfgLoc == nil {
		return []string{Host()}
	}
	hosts := cfgLoc.Config.AllHosts()
	if len(hosts) == 0 {
		return []string{Host()}
	}
	return hosts
}

var fetchStatusForHostFn = fetchStatusForHost

func statusMultiHost(ctx context.Context, hosts []string, flags cli.StatusFlags) error {
	type hostResult struct {
		host     string
		services []statusService
		err      error
	}

	results := make([]hostStatusData, 0, len(hosts))
	ch := make(chan hostResult, len(hosts))
	for _, host := range hosts {
		host := host
		go func() {
			statuses, err := fetchStatusForHostFn(ctx, host, flags)
			ch <- hostResult{host: host, services: statuses, err: err}
		}()
	}
	for range hosts {
		res := <-ch
		if res.err != nil {
			return res.err
		}
		results = append(results, hostStatusData{Host: res.host, Services: res.services})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Host < results[j].Host
	})
	format := strings.TrimSpace(flags.Format)
	if format == "json" || format == "json-pretty" {
		enc := json.NewEncoder(os.Stdout)
		if format == "json-pretty" {
			enc.SetIndent("", "  ")
		}
		return enc.Encode(results)
	}
	return renderStatusTables(os.Stdout, results, true)
}

func fetchStatusForHost(ctx context.Context, host string, _ cli.StatusFlags) ([]statusService, error) {
	args := []string{"status", "--format=json"}
	payload, err := execRemoteOutputFn(ctx, host, systemServiceName, args, nil)
	if err != nil {
		return nil, fmt.Errorf("status on %s: %w", host, err)
	}
	var statuses []statusService
	if err := json.Unmarshal(payload, &statuses); err != nil {
		return nil, fmt.Errorf("status on %s returned invalid JSON: %w", host, err)
	}
	return statuses, nil
}

func renderStatusTableForService(ctx context.Context, host, service string) error {
	args := []string{"status", "--format=json"}
	payload, err := execRemoteOutputFn(ctx, host, service, args, nil)
	if err != nil {
		return err
	}
	var statuses []statusService
	if err := json.Unmarshal(payload, &statuses); err != nil {
		return fmt.Errorf("status on %s returned invalid JSON: %w", host, err)
	}
	return renderStatusTables(os.Stdout, []hostStatusData{{Host: host, Services: statuses}}, false)
}

const statusContainersMaxWidth = 32

type statusRow struct {
	Host       string
	Service    string
	Type       string
	Containers string
	Status     string
}

func buildStatusRows(results []hostStatusData, aggregateContainers bool) []statusRow {
	rows := make([]statusRow, 0)
	for _, res := range results {
		for _, status := range res.Services {
			rows = append(rows, buildStatusRowsForService(res.Host, status, aggregateContainers)...)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Service != rows[j].Service {
			return rows[i].Service < rows[j].Service
		}
		if rows[i].Host != rows[j].Host {
			return rows[i].Host < rows[j].Host
		}
		if rows[i].Containers != rows[j].Containers {
			return rows[i].Containers < rows[j].Containers
		}
		return rows[i].Status < rows[j].Status
	})
	return rows
}

func buildStatusRowsForService(host string, status statusService, aggregateContainers bool) []statusRow {
	if aggregateContainers && status.ServiceType == dockerServiceType {
		return []statusRow{{
			Host:       host,
			Service:    status.ServiceName,
			Type:       status.ServiceType,
			Containers: truncateStatusContainers(formatStatusContainers(status.Components)),
			Status:     dockerAggregateStatus(status.Components),
		}}
	}
	if len(status.Components) == 0 {
		return []statusRow{{
			Host:       host,
			Service:    status.ServiceName,
			Type:       status.ServiceType,
			Containers: "-",
			Status:     "unknown",
		}}
	}
	rows := make([]statusRow, 0, len(status.Components))
	for _, component := range status.Components {
		container := "-"
		if status.ServiceType == dockerServiceType {
			container = component.Name
		}
		rows = append(rows, statusRow{
			Host:       host,
			Service:    status.ServiceName,
			Type:       status.ServiceType,
			Containers: container,
			Status:     component.Status,
		})
	}
	return rows
}

func renderStatusTables(w io.Writer, results []hostStatusData, aggregateContainers bool) error {
	rows := buildStatusRows(results, aggregateContainers)
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	header := "CONTAINER"
	if aggregateContainers {
		header = "CONTAINERS"
	}
	if _, err := fmt.Fprintf(tw, "SERVICE\tHOST\tTYPE\t%s\tSTATUS\t\n", header); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", row.Service, row.Host, row.Type, row.Containers, row.Status); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func dockerAggregateStatus(components []statusComponent) string {
	total := len(components)
	if total == 0 {
		return "(0) stopped"
	}
	running := 0
	stopped := 0
	for _, component := range components {
		switch component.Status {
		case "running":
			running++
		case "stopped":
			stopped++
		}
	}
	if running == total {
		return fmt.Sprintf("running (%d)", total)
	}
	if stopped == total {
		return fmt.Sprintf("stopped (%d)", total)
	}
	return fmt.Sprintf("partial (%d/%d)", running, total)
}

func formatStatusContainers(components []statusComponent) string {
	if len(components) == 0 {
		return "-"
	}
	names := make([]string, 0, len(components))
	for _, component := range components {
		if component.Name == "" {
			continue
		}
		names = append(names, component.Name)
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

func truncateStatusContainers(value string) string {
	if value == "-" || statusContainersMaxWidth <= 0 {
		return value
	}
	if len(value) <= statusContainersMaxWidth {
		return value
	}
	if statusContainersMaxWidth <= 3 {
		return value[:statusContainersMaxWidth]
	}
	return value[:statusContainersMaxWidth-3] + "..."
}

func runFromProjectConfig(cfgLoc *projectConfigLocation, hostOverride string) error {
	return runFromProjectConfigWithForce(cfgLoc, hostOverride, false)
}

func runFromProjectConfigWithForce(cfgLoc *projectConfigLocation, hostOverride string, forceDeploy bool) error {
	stored, err := storedServiceConfig(cfgLoc, hostOverride, "run", serviceTypeRun)
	if err != nil {
		return err
	}
	payload := resolvePayloadPath(cfgLoc.Dir, stored.Entry.Payload)
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("no payload configured for %s@%s", stored.Service, stored.Host)
	}
	envFile := resolveEnvFilePath(cfgLoc.Dir, stored.Entry.EnvFile)
	return runWithChanges(payload, rehydrateRunArgs(stored.Entry.Args), envFile, stored.Entry, forceDeploy)
}

func shouldRunFromConfigWithForce(args []string) (bool, error) {
	forceDeploy, filtered, err := extractForceFlag(args)
	if err != nil {
		return false, err
	}
	if !forceDeploy {
		return false, nil
	}
	return len(normalizeRunArgs(filtered)) == 0, nil
}

func runCronFromProjectConfig(cfgLoc *projectConfigLocation, hostOverride string) error {
	stored, err := storedServiceConfig(cfgLoc, hostOverride, "cron", serviceTypeCron)
	if err != nil {
		return err
	}
	payload := resolvePayloadPath(cfgLoc.Dir, stored.Entry.Payload)
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("no payload configured for %s@%s", stored.Service, stored.Host)
	}
	cronFields, err := parseCronSchedule(stored.Entry.Schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule for %s@%s: %w", stored.Service, stored.Host, err)
	}
	return runCron(payload, cronFields, stored.Entry.Args)
}

type storedService struct {
	Service string
	Host    string
	Entry   ServiceEntry
}

func storedServiceConfig(cfgLoc *projectConfigLocation, hostOverride, commandName, wantType string) (storedService, error) {
	if serviceOverride == "" {
		return storedService{}, fmt.Errorf("%s requires a service name", commandName)
	}
	if cfgLoc == nil || cfgLoc.Config == nil {
		return storedService{}, fmt.Errorf("%s requires a payload (no %s found)", commandName, projectConfigName)
	}
	service := serviceOverride
	host, err := storedServiceHost(cfgLoc.Config, service, hostOverride, commandName)
	if err != nil {
		return storedService{}, err
	}
	entry, ok := cfgLoc.Config.ServiceEntry(service, host)
	if !ok {
		return storedService{}, fmt.Errorf("no stored %s config for %s@%s", commandName, service, host)
	}
	if err := validateStoredServiceType(service, host, entry.Type, commandName, wantType); err != nil {
		return storedService{}, err
	}
	return storedService{Service: service, Host: host, Entry: entry}, nil
}

func storedServiceHost(cfg *ProjectConfig, service, hostOverride, commandName string) (string, error) {
	host := strings.TrimSpace(hostOverride)
	if host != "" {
		return host, nil
	}
	hosts := cfg.ServiceHosts(service)
	if len(hosts) == 0 {
		return "", fmt.Errorf("no stored %s config for %s", commandName, service)
	}
	if len(hosts) > 1 {
		return "", ambiguousServiceError(service, hosts)
	}
	SetHost(hosts[0])
	return hosts[0], nil
}

func validateStoredServiceType(service, host, gotType, commandName, wantType string) error {
	if commandName == "run" && gotType == "" {
		return nil
	}
	if gotType == wantType {
		return nil
	}
	if commandName == "cron" && gotType == "" {
		return fmt.Errorf("service %s@%s is not configured for cron", service, host)
	}
	return fmt.Errorf("service %s@%s is configured as %s", service, host, gotType)
}

func saveRunConfig(cfgLoc *projectConfigLocation, hostOverride string, payload string, runArgs []string) error {
	if serviceOverride == "" {
		return nil
	}
	loc := cfgLoc
	if loc == nil {
		var err error
		loc, err = loadOrCreateProjectConfigFromCwd()
		if err != nil {
			return err
		}
	}
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	payloadRel := relativePayloadPath(loc.Dir, payload)
	entry := ServiceEntry{
		Name:    serviceOverride,
		Host:    host,
		Type:    "",
		Payload: payloadRel,
		Args:    normalizeRunArgs(runArgs),
	}
	loc.Config.SetServiceEntry(entry)
	return saveProjectConfig(loc)
}

func saveCronConfig(cfgLoc *projectConfigLocation, hostOverride string, payload string, cronFields []string, binArgs []string) error {
	if serviceOverride == "" {
		return nil
	}
	loc := cfgLoc
	if loc == nil {
		var err error
		loc, err = loadOrCreateProjectConfigFromCwd()
		if err != nil {
			return err
		}
	}
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	payloadRel := relativePayloadPath(loc.Dir, payload)
	entry := ServiceEntry{
		Name:     serviceOverride,
		Host:     host,
		Type:     serviceTypeCron,
		Payload:  payloadRel,
		Schedule: strings.Join(cronFields, " "),
		Args:     normalizeArgs(binArgs),
	}
	loc.Config.SetServiceEntry(entry)
	return saveProjectConfig(loc)
}
