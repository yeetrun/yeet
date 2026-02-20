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

func HandleSvcCmd(args []string) error {
	cmd := "status"
	cmdArgs := []string{}
	if len(args) > 0 {
		cmd = args[0]
		cmdArgs = args[1:]
	}
	checkArgs := args
	if len(checkArgs) == 0 {
		checkArgs = []string{"status"}
	}

	cfgLoc, err := loadProjectConfigFromCwd()
	if err != nil {
		return err
	}

	if serviceOverride == "" {
		needsService, err := commandNeedsService(checkArgs)
		if err != nil {
			return err
		}
		if needsService {
			return missingServiceError(checkArgs)
		}
	}

	hostOverride, hostOverrideSet := HostOverride()
	if serviceOverride != "" && !hostOverrideSet && cfgLoc != nil {
		host, err := resolveServiceHost(cfgLoc.Config, serviceOverride)
		if err != nil {
			return err
		}
		if host != "" {
			SetHost(host)
		}
	}

	svc := getService()

	// Check for special commands
	switch cmd {
	case "env":
		if len(args) >= 2 && args[1] == "copy" {
			if len(args) != 3 {
				return fmt.Errorf("env copy requires a file")
			}
			if err := runEnvCopy(args[2]); err != nil {
				return err
			}
			return saveEnvFileConfig(cfgLoc, hostOverride, args[2])
		}
		if len(args) >= 2 && args[1] == "set" {
			if len(args) < 3 {
				return fmt.Errorf("env set requires at least one KEY=VALUE assignment")
			}
			assignments, err := parseEnvAssignments(args[2:])
			if err != nil {
				return err
			}
			svc := getService()
			setArgs := []string{"env", "set"}
			for _, assignment := range assignments {
				setArgs = append(setArgs, assignment.Key+"="+assignment.Value)
			}
			return execRemoteFn(context.Background(), svc, setArgs, nil, true)
		}
	// `run <svc> <file/docker-image> [args...]`
	case "run":
		if len(cmdArgs) == 0 {
			return runFromProjectConfig(cfgLoc, hostOverride)
		}
		forceFromConfig, err := shouldRunFromConfigWithForce(cmdArgs)
		if err != nil {
			return err
		}
		if forceFromConfig {
			return runFromProjectConfigWithForce(cfgLoc, hostOverride, true)
		}
		payload, runArgs, err := splitRunPayloadArgs(cmdArgs)
		if err != nil {
			return err
		}
		envFileArg, filteredArgs, envFileSet, err := extractEnvFileFlag(runArgs)
		if err != nil {
			return err
		}
		forceDeploy, filteredArgs, err := extractForceFlag(filteredArgs)
		if err != nil {
			return err
		}
		entry, hasEntry := serviceEntryForConfig(cfgLoc, hostOverride)
		if hasEntry {
			if err := ensureLockedRunFlags(entry, filteredArgs); err != nil {
				return err
			}
		}
		envFile := envFileArg
		if envFile == "" && hasEntry && entry.EnvFile != "" && cfgLoc != nil {
			envFile = resolveEnvFilePath(cfgLoc.Dir, entry.EnvFile)
		}
		if err := runWithChanges(payload, filteredArgs, envFile, entry, forceDeploy); err != nil {
			return err
		}
		normalizedArgs := normalizeRunArgs(filteredArgs)
		if err := saveRunConfig(cfgLoc, hostOverride, payload, normalizedArgs); err != nil {
			return err
		}
		if envFileSet {
			if err := saveEnvFileConfig(cfgLoc, hostOverride, envFileArg); err != nil {
				return err
			}
		}
		return nil
	case "remove":
		removeFlags, _, err := cli.ParseRemove(cmdArgs)
		if err != nil {
			return err
		}
		remoteArgs := filterRemoveArgs(cmdArgs)
		if err := execRemoteFn(context.Background(), svc, append([]string{"remove"}, remoteArgs...), nil, true); err != nil {
			return err
		}
		if removeFlags.CleanConfig {
			return removeServiceConfig(cfgLoc, hostOverride)
		}
		if removeFlags.Yes {
			return nil
		}
		if !hasServiceConfig(cfgLoc, hostOverride) {
			return nil
		}
		ok, err := cmdutil.Confirm(os.Stdin, os.Stdout, fmt.Sprintf("Remove %q from yeet.toml?", svc))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return removeServiceConfig(cfgLoc, hostOverride)
	// `copy [-avz] <src> <dst>`
	case "copy":
		var cfg *ProjectConfig
		if cfgLoc != nil {
			cfg = cfgLoc.Config
		}
		return runCopyCommand(cmdArgs, cfg)
	// `cron <svc> <file> <cronexpr>`
	case "cron":
		if len(cmdArgs) == 0 {
			return runCronFromProjectConfig(cfgLoc, hostOverride)
		}
		payload := cmdArgs[0]
		cronArgs := cmdArgs[1:]
		cronFields, binArgs, err := splitCronArgs(cronArgs)
		if err != nil {
			return err
		}
		if err := runCron(payload, cronFields, binArgs); err != nil {
			return err
		}
		return saveCronConfig(cfgLoc, hostOverride, payload, cronFields, binArgs)
	// `stage <svc> <file>`
	case "stage":
		if len(cmdArgs) == 1 {
			return runStageBinary(cmdArgs[0])
		}
	case cli.CommandEvents:
		flags, _, err := cli.ParseEvents(cmdArgs)
		if err != nil {
			return err
		}
		if serviceOverride == "" && !flags.All {
			return missingServiceError(args)
		}
		return handleEventsRPC(context.Background(), svc, flags)
	case "status":
		return handleStatusCommand(context.Background(), cmdArgs, cfgLoc, hostOverrideSet)
	case "info":
		return handleInfoCommand(context.Background(), cmdArgs, cfgLoc)
	}

	// Assume the first argument is a command
	return execRemoteFn(context.Background(), svc, args, nil, true)
}

var tryRunDockerFn = tryRunDocker
var buildDockerImageForRemoteFn = buildDockerImageForRemote
var tryRunRemoteImageFn = tryRunRemoteImage

func splitRunPayloadArgs(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("run requires a payload")
	}
	specs := cli.RemoteFlagSpecs()["run"]
	payloadIdx := -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") && len(arg) > 2 {
			name := arg
			if idx := strings.Index(name, "="); idx != -1 {
				name = name[:idx]
			}
			if spec, ok := specs[name]; ok {
				if spec.ConsumesValue && !strings.Contains(arg, "=") {
					i++
				}
				continue
			}
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			if strings.Contains(arg, "=") {
				name := arg[:strings.Index(arg, "=")]
				if _, ok := specs[name]; ok {
					continue
				}
			} else if len(arg) == 2 {
				if spec, ok := specs[arg]; ok {
					if spec.ConsumesValue {
						i++
					}
					continue
				}
			} else if short := "-" + string(arg[1]); short != "-" {
				if spec, ok := specs[short]; ok && spec.ConsumesValue {
					continue
				}
			}
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
	specs := cli.RemoteFlagSpecs()["run"]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return args[:i], args[i+1:]
			}
			return args[:i], nil
		}
		if strings.HasPrefix(arg, "--") && len(arg) > 2 {
			name := arg
			if idx := strings.Index(name, "="); idx != -1 {
				name = name[:idx]
			}
			spec, ok := specs[name]
			if !ok {
				return args[:i], args[i:]
			}
			if spec.ConsumesValue && !strings.Contains(arg, "=") {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			if strings.Contains(arg, "=") {
				name := arg[:strings.Index(arg, "=")]
				if _, ok := specs[name]; ok {
					continue
				}
				return args[:i], args[i:]
			}
			if len(arg) == 2 {
				spec, ok := specs[arg]
				if !ok {
					return args[:i], args[i:]
				}
				if spec.ConsumesValue {
					i++
				}
				continue
			}
			short := "-" + string(arg[1])
			spec, ok := specs[short]
			if !ok {
				return args[:i], args[i:]
			}
			if spec.ConsumesValue {
				continue
			}
			continue
		}
	}
	return args, nil
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
	defer os.RemoveAll(tmpDir)
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

func runFilePayload(file string, args []string, pushLocalImages bool) (ok bool, _ error) {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return false, err
	}
	payload, cleanup, ft, err := openPayloadForUpload(file, goos, goarch)
	if err != nil {
		return false, err
	}
	defer cleanup()
	if ft == ftdetect.DockerCompose {
		flags, _, err := cli.ParseRun(args)
		if err != nil {
			return false, err
		}
		if len(flags.Publish) > 0 && pushLocalImages {
			return false, fmt.Errorf("-p/--publish is not supported for docker compose payloads")
		}
	}
	svc := getService()
	if ft == ftdetect.DockerCompose && pushLocalImages {
		if err := pushAllLocalImagesFn(svc, goos, goarch); err != nil {
			return false, fmt.Errorf("failed to push all local images: %w", err)
		}
	}
	runArgs := append([]string{"run"}, args...)
	tty := isTerminalFn(int(os.Stdout.Fd()))
	if err := execRemoteFn(context.Background(), svc, runArgs, payload, tty); err != nil {
		return false, fmt.Errorf("failed to run service: %w", err)
	}
	return true, nil
}

func tryRunDocker(image string, args []string) (ok bool, _ error) {
	if !imageExists(image) {
		// If the image does not exist, it's not an error
		return false, nil
	}
	svc := getService()
	if err := pushImage(context.Background(), svc, image, "latest"); err != nil {
		return false, fmt.Errorf("failed to push image: %w", err)
	}
	// If there are more arguments, run `stage <svc> <args...>`
	if len(args) > 0 {
		stageArgs := append([]string{"stage"}, args...)
		if err := execRemote(context.Background(), svc, stageArgs, nil, true); err != nil {
			fmt.Println("failed to stage args:", err)
			return false, fmt.Errorf("failed to stage args: %w", err)
		}
	}
	// Run stage commit (don't inherit os.Args)
	if err := execRemote(context.Background(), svc, []string{"stage", "commit"}, nil, true); err != nil {
		return false, errors.New("failed to run service")
	}
	return true, nil
}

func runEnvCopy(file string) error {
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
	defer f.Close()
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
	format := strings.TrimSpace(flags.Format)
	if (format == "" || format == "table") && serviceOverride != "" {
		return renderStatusTableForService(ctx, Host(), serviceOverride)
	}
	if serviceOverride == "" && (format == "" || format == "table") {
		if hostOverrideSet {
			return statusMultiHost(ctx, []string{Host()}, flags)
		}
		if cfgLoc != nil {
			hosts := cfgLoc.Config.AllHosts()
			if len(hosts) > 0 {
				return statusMultiHost(ctx, hosts, flags)
			}
		}
		return statusMultiHost(ctx, []string{Host()}, flags)
	}
	svc := getService()
	statusArgs := append([]string{"status"}, args...)
	return execRemoteFn(ctx, svc, statusArgs, nil, true)
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

func renderStatusTables(w io.Writer, results []hostStatusData, aggregateContainers bool) error {
	type statusRow struct {
		Host       string
		Service    string
		Type       string
		Containers string
		Status     string
	}

	rows := make([]statusRow, 0)
	for _, res := range results {
		for _, status := range res.Services {
			if aggregateContainers && status.ServiceType == dockerServiceType {
				rows = append(rows, statusRow{
					Host:       res.Host,
					Service:    status.ServiceName,
					Type:       status.ServiceType,
					Containers: truncateStatusContainers(formatStatusContainers(status.Components)),
					Status:     dockerAggregateStatus(status.Components),
				})
				continue
			}
			if len(status.Components) == 0 {
				rows = append(rows, statusRow{
					Host:       res.Host,
					Service:    status.ServiceName,
					Type:       status.ServiceType,
					Containers: "-",
					Status:     "unknown",
				})
				continue
			}
			for _, component := range status.Components {
				container := "-"
				if status.ServiceType == dockerServiceType {
					container = component.Name
				}
				rows = append(rows, statusRow{
					Host:       res.Host,
					Service:    status.ServiceName,
					Type:       status.ServiceType,
					Containers: container,
					Status:     component.Status,
				})
			}
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

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	header := "CONTAINER"
	if aggregateContainers {
		header = "CONTAINERS"
	}
	fmt.Fprintf(tw, "SERVICE\tHOST\tTYPE\t%s\tSTATUS\t\n", header)
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", row.Service, row.Host, row.Type, row.Containers, row.Status)
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
	if serviceOverride == "" {
		return fmt.Errorf("run requires a service name")
	}
	if cfgLoc == nil || cfgLoc.Config == nil {
		return fmt.Errorf("run requires a payload (no %s found)", projectConfigName)
	}
	service := serviceOverride
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		hosts := cfgLoc.Config.ServiceHosts(service)
		if len(hosts) == 0 {
			return fmt.Errorf("no stored run config for %s", service)
		}
		if len(hosts) > 1 {
			return ambiguousServiceError(service, hosts)
		}
		host = hosts[0]
		SetHost(host)
	}
	entry, ok := cfgLoc.Config.ServiceEntry(service, host)
	if !ok {
		return fmt.Errorf("no stored run config for %s@%s", service, host)
	}
	if entry.Type != "" && entry.Type != serviceTypeRun {
		return fmt.Errorf("service %s@%s is configured as %s", service, host, entry.Type)
	}
	payload := resolvePayloadPath(cfgLoc.Dir, entry.Payload)
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("no payload configured for %s@%s", service, host)
	}
	envFile := resolveEnvFilePath(cfgLoc.Dir, entry.EnvFile)
	return runWithChanges(payload, rehydrateRunArgs(entry.Args), envFile, entry, forceDeploy)
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
	if serviceOverride == "" {
		return fmt.Errorf("cron requires a service name")
	}
	if cfgLoc == nil || cfgLoc.Config == nil {
		return fmt.Errorf("cron requires a payload (no %s found)", projectConfigName)
	}
	service := serviceOverride
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		hosts := cfgLoc.Config.ServiceHosts(service)
		if len(hosts) == 0 {
			return fmt.Errorf("no stored cron config for %s", service)
		}
		if len(hosts) > 1 {
			return ambiguousServiceError(service, hosts)
		}
		host = hosts[0]
		SetHost(host)
	}
	entry, ok := cfgLoc.Config.ServiceEntry(service, host)
	if !ok {
		return fmt.Errorf("no stored cron config for %s@%s", service, host)
	}
	if entry.Type != serviceTypeCron {
		if entry.Type == "" {
			return fmt.Errorf("service %s@%s is not configured for cron", service, host)
		}
		return fmt.Errorf("service %s@%s is configured as %s", service, host, entry.Type)
	}
	payload := resolvePayloadPath(cfgLoc.Dir, entry.Payload)
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("no payload configured for %s@%s", service, host)
	}
	cronFields, err := parseCronSchedule(entry.Schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule for %s@%s: %w", service, host, err)
	}
	return runCron(payload, cronFields, entry.Args)
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
