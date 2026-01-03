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
			return runEnvCopy(args[2])
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
		payload, runArgs, err := splitRunPayloadArgs(cmdArgs)
		if err != nil {
			return err
		}
		if err := runRun(payload, runArgs); err != nil {
			return err
		}
		runArgs = normalizeArgs(runArgs)
		return saveRunConfig(cfgLoc, hostOverride, payload, runArgs)
	// `copy <svc> <file> [dest]`
	case "copy":
		switch len(cmdArgs) {
		case 1:
			return runCopy(cmdArgs[0], "")
		case 2:
			return runCopy(cmdArgs[0], cmdArgs[1])
		}
		return fmt.Errorf("copy requires a source file and optional destination")
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
		return false, fmt.Errorf("Dockerfile payload does not exist: %s", path)
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
	svc := getService()
	if ft == ftdetect.DockerCompose && pushLocalImages {
		if err := pushAllLocalImagesFn(svc, goos, goarch); err != nil {
			return false, fmt.Errorf("failed to push all local images: %w", err)
		}
	}
	defer cleanup()
	runArgs := append([]string{"run"}, args...)
	tty := isTerminalFn(int(os.Stdout.Fd()))
	if err := execRemoteFn(context.Background(), svc, runArgs, payload, tty); err != nil {
		return false, fmt.Errorf("failed to run service: %w", err)
	}
	return true, nil
}

func runCopy(file, dest string) error {
	if file == "" {
		return fmt.Errorf("copy requires a source file")
	}
	if st, err := os.Stat(file); err != nil {
		return err
	} else if st.IsDir() {
		return fmt.Errorf("%q is a directory, expected a file", file)
	}
	normalized, err := normalizeCopyDest(file, dest)
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	svc := getService()
	args := []string{"copy", normalized}
	if err := execRemoteFn(context.Background(), svc, args, f, false); err != nil {
		return err
	}
	return nil
}

func normalizeCopyDest(src, dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	trimmed := strings.TrimPrefix(dest, "./")
	if trimmed == "" {
		trimmed = filepath.Base(src)
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("copy destination must be relative")
	}

	rel := trimmed
	if rel == "data" || strings.HasPrefix(rel, "data/") {
		rel = strings.TrimPrefix(rel, "data")
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "" || strings.HasSuffix(dest, "/") || strings.HasSuffix(rel, "/") {
		base := filepath.Base(src)
		if base == "." || base == string(os.PathSeparator) {
			return "", fmt.Errorf("invalid source file %q", src)
		}
		rel = filepath.Join(rel, base)
	}

	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid copy destination %q", dest)
	}
	return filepath.Join("data", rel), nil
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
	if serviceOverride == "" {
		format := strings.TrimSpace(flags.Format)
		if hostOverrideSet && (format == "" || format == "table") {
			return statusMultiHost(ctx, []string{Host()}, flags)
		}
		if !hostOverrideSet && cfgLoc != nil {
			hosts := cfgLoc.Config.AllHosts()
			if len(hosts) > 0 {
				return statusMultiHost(ctx, hosts, flags)
			}
		}
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
	return renderStatusTables(os.Stdout, results)
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

func renderStatusTables(w io.Writer, results []hostStatusData) error {
	type statusRow struct {
		Host      string
		Service   string
		Type      string
		Container string
		Status    string
	}

	rows := make([]statusRow, 0)
	for _, res := range results {
		for _, status := range res.Services {
			for _, component := range status.Components {
				container := "-"
				if status.ServiceType == dockerServiceType {
					container = component.Name
				}
				rows = append(rows, statusRow{
					Host:      res.Host,
					Service:   status.ServiceName,
					Type:      status.ServiceType,
					Container: container,
					Status:    component.Status,
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
		if rows[i].Container != rows[j].Container {
			return rows[i].Container < rows[j].Container
		}
		return rows[i].Status < rows[j].Status
	})

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tHOST\tTYPE\tCONTAINER\tSTATUS\t")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", row.Service, row.Host, row.Type, row.Container, row.Status)
	}
	return tw.Flush()
}

func runFromProjectConfig(cfgLoc *projectConfigLocation, hostOverride string) error {
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
	return runRun(payload, entry.Args)
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
		Type:    serviceTypeRun,
		Payload: payloadRel,
		Args:    runArgs,
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
