// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/ftdetect"
)

type runChangeSummary struct {
	payloadChanged bool
	envChanged     bool
	argsChanged    bool
	payloadLabel   string
}

type runChangeResult struct {
	sandbox      *clientSandboxPolicy
	catchChanged bool
}

func (s runChangeSummary) hasChanges() bool {
	return s.payloadChanged || s.envChanged || s.argsChanged
}

func (s runChangeSummary) requiresRun() bool {
	return s.payloadChanged || s.argsChanged
}

func extractEnvFileFlag(args []string) (string, []string, bool, error) {
	if len(args) == 0 {
		return "", args, false, nil
	}
	out := make([]string, 0, len(args))
	var envFile string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == "--env-file" {
			if i+1 >= len(args) {
				return "", nil, false, fmt.Errorf("--env-file requires a value")
			}
			envFile = args[i+1]
			found = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "--env-file=") {
			envFile = strings.TrimPrefix(arg, "--env-file=")
			found = true
			continue
		}
		out = append(out, arg)
	}
	return envFile, out, found, nil
}

type serviceRootOptions struct {
	Root string
	ZFS  bool
}

type serviceRootParseState struct {
	opts      serviceRootOptions
	foundRoot bool
	foundZFS  bool
}

func extractServiceRootOptions(args []string) (serviceRootOptions, []string, bool, error) {
	if len(args) == 0 {
		return serviceRootOptions{}, args, false, nil
	}
	out := make([]string, 0, len(args))
	state := serviceRootParseState{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		next, handled, err := parseServiceRootControlArg(args, i, &state)
		if err != nil {
			return serviceRootOptions{}, nil, false, err
		}
		if handled {
			i = next
			continue
		}
		out = append(out, arg)
	}
	if err := validateServiceRootOptions(state); err != nil {
		return serviceRootOptions{}, nil, false, err
	}
	return state.opts, out, state.foundRoot || state.foundZFS, nil
}

func parseServiceRootControlArg(args []string, i int, state *serviceRootParseState) (int, bool, error) {
	arg := args[i]
	switch {
	case arg == "--zfs":
		state.opts.ZFS = true
		state.foundZFS = true
		return i, true, nil
	case strings.HasPrefix(arg, "--zfs="):
		value := strings.TrimPrefix(arg, "--zfs=")
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return i, false, fmt.Errorf("invalid --zfs value %q", value)
		}
		state.opts.ZFS = parsed
		state.foundZFS = true
		return i, true, nil
	case arg == "--service-root":
		if i+1 >= len(args) {
			return i, false, fmt.Errorf("--service-root requires a value")
		}
		state.opts.Root = strings.TrimSpace(args[i+1])
		state.foundRoot = true
		return i + 1, true, nil
	case strings.HasPrefix(arg, "--service-root="):
		state.opts.Root = strings.TrimSpace(strings.TrimPrefix(arg, "--service-root="))
		state.foundRoot = true
		return i, true, nil
	default:
		return i, false, nil
	}
}

func validateServiceRootOptions(state serviceRootParseState) error {
	if state.foundRoot && strings.TrimSpace(state.opts.Root) == "" {
		return fmt.Errorf("--service-root requires a value")
	}
	if state.foundZFS && !state.foundRoot {
		return fmt.Errorf("--zfs requires --service-root")
	}
	if state.foundRoot && !state.opts.ZFS && !filepath.IsAbs(state.opts.Root) {
		return fmt.Errorf("--service-root must be absolute unless --zfs is set")
	}
	return nil
}

func runArgsWithServiceRootOptions(args []string, opts serviceRootOptions) []string {
	args = append([]string{}, args...)
	prefix := serviceRootOptionArgs(opts)
	if len(prefix) == 0 {
		return args
	}
	out := make([]string, 0, len(args)+len(prefix))
	out = append(out, prefix...)
	out = append(out, args...)
	return out
}

func runArgsWithSandboxOptions(args []string, entry ServiceEntry) []string {
	args = append([]string{}, args...)
	if runArgsHaveSandboxFlag(args) {
		return args
	}
	state := strings.ToLower(strings.TrimSpace(entry.Sandbox))
	if state != "on" && state != "off" {
		return args
	}
	prefix := []string{"--sandbox=" + state}
	for _, exposure := range canonicalSandboxConfigValues(entry.SandboxRO) {
		prefix = append(prefix, "--sandbox-ro="+exposure)
	}
	for _, exposure := range canonicalSandboxConfigValues(entry.SandboxRW) {
		prefix = append(prefix, "--sandbox-rw="+exposure)
	}
	return append(prefix, args...)
}

func runArgsHaveSandboxFlag(args []string) bool {
	return runArgsHaveFlag(args, "--sandbox") || runArgsHaveFlag(args, "--sandbox-ro") || runArgsHaveFlag(args, "--sandbox-rw")
}

func serviceRootOptionArgs(opts serviceRootOptions) []string {
	opts.Root = strings.TrimSpace(opts.Root)
	if opts.Root == "" {
		return nil
	}
	out := make([]string, 0, 2)
	out = append(out, "--service-root="+opts.Root)
	if opts.ZFS {
		out = append(out, "--zfs")
	}
	return out
}

type snapshotOptions struct {
	Snapshots       string
	KeepLast        int
	KeepLastInherit bool
	MaxAge          string
	MaxAgeInherit   bool
	Required        *bool
	RequiredInherit bool
	Events          []string
	EventsInherit   bool
}

func runArgsWithSnapshotOptions(args []string, opts snapshotOptions) []string {
	out := append([]string{}, args...)
	if opts.Snapshots != "" {
		out = append([]string{"--snapshots=" + opts.Snapshots}, out...)
	}
	if opts.KeepLastInherit {
		out = append([]string{"--snapshot-keep-last=inherit"}, out...)
	} else if opts.KeepLast != 0 {
		out = append([]string{fmt.Sprintf("--snapshot-keep-last=%d", opts.KeepLast)}, out...)
	}
	if opts.MaxAgeInherit {
		out = append([]string{"--snapshot-max-age=inherit"}, out...)
	} else if opts.MaxAge != "" {
		out = append([]string{"--snapshot-max-age=" + opts.MaxAge}, out...)
	}
	if opts.RequiredInherit {
		out = append([]string{"--snapshot-required=inherit"}, out...)
	} else if opts.Required != nil {
		out = append([]string{fmt.Sprintf("--snapshot-required=%t", *opts.Required)}, out...)
	}
	if opts.EventsInherit {
		out = append([]string{"--snapshot-events=inherit"}, out...)
	} else if len(opts.Events) != 0 {
		out = append([]string{"--snapshot-events=" + strings.Join(opts.Events, ",")}, out...)
	}
	return out
}

type publishOptions struct {
	Ports   []string
	Reset   bool
	Changed bool
}

func runArgsWithPublishOptions(args []string, ports []string) []string {
	normalized := normalizePublishPorts(ports)
	if len(normalized) == 0 {
		return append([]string{}, args...)
	}
	out := make([]string, 0, len(args)+len(normalized)*2)
	for _, port := range normalized {
		out = append(out, "-p", port)
	}
	out = append(out, args...)
	return out
}

func extractPublishOptions(args []string) (publishOptions, []string, error) {
	if len(args) == 0 {
		return publishOptions{}, args, nil
	}
	out := make([]string, 0, len(args))
	opts := publishOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		handled, consumed, err := parsePublishOptionArg(args, i, &opts)
		if err != nil {
			return publishOptions{}, nil, err
		}
		if handled {
			i += consumed
			continue
		}
		out = append(out, arg)
	}
	opts.Ports = normalizePublishPorts(opts.Ports)
	return opts, out, nil
}

func parsePublishOptionArg(args []string, i int, opts *publishOptions) (bool, int, error) {
	arg := args[i]
	if arg == "-p" || arg == "--publish" {
		consumed, err := consumePublishValue(args, i, opts)
		return true, consumed, err
	}
	if value, ok := publishEqualsValue(arg); ok {
		addPublishPort(opts, value)
		return true, 0, nil
	}
	if arg == "--publish-reset" {
		opts.Reset = true
		opts.Changed = true
		return true, 0, nil
	}
	if strings.HasPrefix(arg, "--publish-reset=") {
		return parsePublishResetValue(arg, opts)
	}
	return false, 0, nil
}

func consumePublishValue(args []string, i int, opts *publishOptions) (int, error) {
	arg := args[i]
	if i+1 >= len(args) {
		return 0, fmt.Errorf("%s requires a value", arg)
	}
	addPublishPort(opts, args[i+1])
	return 1, nil
}

func publishEqualsValue(arg string) (string, bool) {
	switch {
	case strings.HasPrefix(arg, "-p="):
		return strings.TrimPrefix(arg, "-p="), true
	case strings.HasPrefix(arg, "--publish="):
		return strings.TrimPrefix(arg, "--publish="), true
	default:
		return "", false
	}
}

func addPublishPort(opts *publishOptions, value string) {
	opts.Ports = append(opts.Ports, value)
	opts.Changed = true
}

func parsePublishResetValue(arg string, opts *publishOptions) (bool, int, error) {
	value := strings.TrimPrefix(arg, "--publish-reset=")
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return true, 0, fmt.Errorf("invalid --publish-reset value %q", value)
	}
	opts.Reset = parsed
	opts.Changed = opts.Changed || parsed
	return true, 0, nil
}

func normalizePublishPorts(ports []string) []string {
	out := make([]string, 0, len(ports))
	for _, port := range ports {
		if trimmed := strings.TrimSpace(port); trimmed != "" {
			out = append(out, normalizePublishPort(trimmed))
		}
	}
	return out
}

func normalizePublishPort(port string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(port), "/tcp"):
		return port[:len(port)-len("/tcp")]
	case strings.HasSuffix(strings.ToLower(port), "/udp"):
		return port[:len(port)-len("/udp")] + "/udp"
	default:
		return port
	}
}

func extractForceFlag(args []string) (bool, []string, error) {
	if len(args) == 0 {
		return false, args, nil
	}
	out := make([]string, 0, len(args))
	force := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == "--force" {
			force = true
			continue
		}
		if strings.HasPrefix(arg, "--force=") {
			value := strings.TrimPrefix(arg, "--force=")
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return false, nil, fmt.Errorf("invalid --force value %q", value)
			}
			force = parsed
			continue
		}
		out = append(out, arg)
	}
	return force, out, nil
}

func filterRemoveArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--clean-config" {
			continue
		}
		if strings.HasPrefix(arg, "--clean-config=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func serviceEntryForConfig(cfgLoc *projectConfigLocation, hostOverride string) (ServiceEntry, bool) {
	if cfgLoc == nil || cfgLoc.Config == nil {
		return ServiceEntry{}, false
	}
	if serviceOverride == "" {
		return ServiceEntry{}, false
	}
	entry, ok := cfgLoc.Config.ServiceEntry(serviceOverride, serviceConfigHost(hostOverride))
	return entry, ok
}

func hasServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) bool {
	_, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	return ok
}

func removeServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) error {
	cfg, service, host, ok := removableServiceConfig(cfgLoc, hostOverride)
	if !ok {
		return nil
	}
	if !cfg.RemoveServiceEntry(service, host) {
		return nil
	}
	return saveProjectConfig(cfgLoc)
}

func removableServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) (*ProjectConfig, string, string, bool) {
	if cfgLoc == nil || cfgLoc.Config == nil || serviceOverride == "" {
		return nil, "", "", false
	}
	return cfgLoc.Config, serviceOverride, serviceConfigHost(hostOverride), true
}

func serviceConfigHost(hostOverride string) string {
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	return host
}

func saveEnvFileConfig(cfgLoc *projectConfigLocation, hostOverride string, envFile string) error {
	if serviceOverride == "" {
		return nil
	}
	envFile = strings.TrimSpace(envFile)
	if envFile == "" {
		return nil
	}
	loc := cfgLoc
	if loc == nil {
		var err error
		loc, _, err = projectConfigForWrite("env")
		if err != nil {
			return err
		}
		if loc == nil {
			return nil
		}
	}
	entry := ServiceEntry{
		Name:    serviceOverride,
		Host:    serviceConfigHost(hostOverride),
		EnvFile: relativeEnvFilePath(loc.Dir, envFile),
	}
	if existing, ok := loc.Config.ServiceEntry(serviceOverride, entry.Host); ok {
		entry.Type = existing.Type
		entry.Payload = existing.Payload
		entry.PayloadKind = existing.PayloadKind
		entry.ServiceRoot = existing.ServiceRoot
		entry.ServiceRootZFS = existing.ServiceRootZFS
		entry.Snapshots = existing.Snapshots
		entry.SnapshotKeepLast = existing.SnapshotKeepLast
		entry.SnapshotMaxAge = existing.SnapshotMaxAge
		entry.SnapshotRequired = existing.SnapshotRequired
		entry.SnapshotEvents = append([]string{}, existing.SnapshotEvents...)
		entry.Ports = append([]string{}, existing.Ports...)
		entry.Schedule = existing.Schedule
		entry.Sandbox = existing.Sandbox
		entry.SandboxRO = cloneSandboxStringSlice(existing.SandboxRO)
		entry.SandboxRW = cloneSandboxStringSlice(existing.SandboxRW)
		entry.Args = existing.Args
	}
	loc.Config.SetServiceEntry(entry)
	return saveProjectConfig(loc)
}

func effectiveRunArgsForExistingEntry(entry ServiceEntry, runArgs []string) ([]string, error) {
	if len(normalizeRunArgs(runArgs)) == 0 {
		args := runArgsWithPublishOptions(rehydrateRunArgs(entry.Args), effectiveServiceEntryPorts(entry))
		return runArgsWithSandboxOptions(args, entry), nil
	}
	out, err := runArgsWithStoredLockedFlags(entry, runArgs)
	if err != nil {
		return nil, err
	}
	publish, _, err := extractPublishOptions(runArgs)
	if err != nil {
		return nil, err
	}
	if publish.Changed {
		return runArgsWithSandboxOptions(out, entry), nil
	}
	return runArgsWithSandboxOptions(runArgsWithPublishOptions(out, effectiveServiceEntryPorts(entry)), entry), nil
}

func effectiveServiceEntryPorts(entry ServiceEntry) []string {
	ports := normalizePublishPorts(entry.Ports)
	if len(ports) != 0 {
		return ports
	}
	legacy, _, err := extractPublishOptions(rehydrateRunArgs(entry.Args))
	if err != nil {
		return nil
	}
	return legacy.Ports
}

func runArgsWithStoredLockedFlags(entry ServiceEntry, runArgs []string) ([]string, error) {
	storedFlags, _, err := cli.ParseRun(rehydrateRunArgs(entry.Args))
	if err != nil {
		return nil, err
	}
	prefix := storedLockedRunFlagsPrefix(storedFlags, runArgs)
	if len(prefix) == 0 {
		return append([]string{}, runArgs...), nil
	}
	out := make([]string, 0, len(prefix)+len(runArgs))
	out = append(out, prefix...)
	out = append(out, runArgs...)
	return out, nil
}

func storedLockedRunFlagsPrefix(storedFlags cli.RunFlags, runArgs []string) []string {
	var prefix []string
	appendValue := func(name, value string) {
		if value = strings.TrimSpace(value); value != "" && !runArgsHaveFlag(runArgs, name) {
			prefix = append(prefix, name+"="+value)
		}
	}
	appendValue("--net", storedFlags.Net)
	appendValue("--ts-ver", storedFlags.TsVer)
	appendValue("--ts-exit", storedFlags.TsExit)
	if len(storedFlags.TsTags) != 0 && !runArgsHaveFlag(runArgs, "--ts-tags") {
		for _, tag := range storedFlags.TsTags {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				prefix = append(prefix, "--ts-tags="+tag)
			}
		}
	}
	appendValue("--macvlan-parent", storedFlags.MacvlanParent)
	if storedFlags.MacvlanVlan != 0 && !runArgsHaveFlag(runArgs, "--macvlan-vlan") {
		prefix = append(prefix, "--macvlan-vlan="+strconv.Itoa(storedFlags.MacvlanVlan))
	}
	appendValue("--macvlan-mac", storedFlags.MacvlanMac)
	return prefix
}

func runArgsHaveFlag(runArgs []string, name string) bool {
	flagArgs, _ := splitRunArgsForParsing(runArgs)
	for _, arg := range flagArgs {
		if flagName(arg) == name {
			return true
		}
	}
	return false
}

func ensureLockedRunFlags(entry ServiceEntry, runArgs []string) error {
	if entry.Name == "" || entry.Host == "" {
		return nil
	}
	_, _, err := cli.ParseRun(runArgs)
	return err
}

type runPayloadContextFunc func(context.Context, string, []string) error

func runWithChanges(payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool) error {
	return runWithChangesContext(context.Background(), payload, runArgs, envFile, entry, forceDeploy)
}

func runWithChangesTo(stdout io.Writer, payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool) error {
	return runWithChangesToContext(context.Background(), stdout, payload, runArgs, envFile, entry, forceDeploy)
}

func runWithChangesContext(ctx context.Context, payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool) error {
	return runWithChangesToContext(ctx, os.Stdout, payload, runArgs, envFile, entry, forceDeploy)
}

func runWithChangesToContext(ctx context.Context, stdout io.Writer, payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool) error {
	return runWithChangesToWithContextRunner(ctx, stdout, payload, runArgs, envFile, entry, forceDeploy, runRunContext, false)
}

func runWithChangesToWithContextRunner(ctx context.Context, stdout io.Writer, payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool, runner runPayloadContextFunc, alwaysDeployPayload bool) error {
	_, err := runWithChangesToWithContextRunnerResult(ctx, stdout, payload, runArgs, envFile, entry, forceDeploy, runner, alwaysDeployPayload)
	return err
}

func runWithChangesToWithContextRunnerResult(ctx context.Context, stdout io.Writer, payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool, runner runPayloadContextFunc, alwaysDeployPayload bool) (runChangeResult, error) {
	response, err := inspectExistingRunProtectedChanges(ctx, entry, runArgs)
	if err != nil {
		return runChangeResult{}, err
	}
	storedArgs := runArgsWithServiceRootOptions(entry.Args, serviceRootOptions{Root: entry.ServiceRoot, ZFS: entry.ServiceRootZFS})
	storedArgs = runArgsWithSnapshotOptions(storedArgs, snapshotOptions{
		Snapshots: entry.Snapshots,
		KeepLast:  entry.SnapshotKeepLast,
		MaxAge:    entry.SnapshotMaxAge,
		Required:  entry.SnapshotRequired,
		Events:    entry.SnapshotEvents,
	})
	comparisonArgs := removeRunSandboxControlFlags(runArgs)
	storedComparisonArgs := normalizeRunArgs(storedArgs)
	summary, err := detectRunChangesWithOptions(ctx, payload, comparisonArgs, envFile, storedComparisonArgs, alwaysDeployPayload)
	if err != nil {
		return runChangeResult{}, err
	}
	if err := applyRunChangeSummary(ctx, stdout, payload, runArgs, envFile, summary, forceDeploy, runner); err != nil {
		return runChangeResult{}, err
	}
	runApplied := summary.requiresRun() || (!summary.hasChanges() && forceDeploy)
	return captureRunSandboxResult(ctx, payload, entry, response, runApplied, runApplied || summary.envChanged)
}

func captureRunSandboxResult(ctx context.Context, payload string, entry ServiceEntry, response catchrpc.ServiceInfoResponse, runApplied, catchChanged bool) (runChangeResult, error) {
	result := runChangeResult{catchChanged: catchChanged}
	var err error
	if runSandboxPostSuccessRefreshRequired(payload, entry, response, runApplied) {
		response, err = fetchRunChangeServiceInfoFn(ctx, entry.Host, entry.Name)
		if err != nil {
			return result, runSandboxCaptureError(entry, err)
		}
		if !response.Found {
			return result, runSandboxCaptureError(entry, fmt.Errorf("service info reports service not found"))
		}
	}
	sandbox, captured, err := runSandboxCaptureFromResponse(entry, response)
	if err != nil {
		return result, runSandboxCaptureError(entry, err)
	}
	if !captured && runApplied && runSandboxRequestedBackendExcludesSandbox(payload, entry) {
		sandbox = clientSandboxPolicy{}
		captured = true
	}
	if captured {
		result.sandbox = cloneClientSandboxPolicy(&sandbox)
	}
	return result, nil
}

func applyRunChangeSummary(ctx context.Context, stdout io.Writer, payload string, runArgs []string, envFile string, summary runChangeSummary, forceDeploy bool, runner runPayloadContextFunc) error {
	if !summary.hasChanges() {
		return applyUnchangedRun(ctx, stdout, payload, runArgs, forceDeploy, runner)
	}
	if summary.envChanged {
		if err := runEnvCopyContextWithOutputArgs(ctx, stdout, envFile, runArgs); err != nil {
			return err
		}
		if err := writeRunChangeLine(stdout, "Updated env file"); err != nil {
			return err
		}
	}
	if summary.requiresRun() {
		if err := runner(ctx, payload, runArgs); err != nil {
			return err
		}
		return writeRunDeployStatus(stdout, summary)
	}
	return nil
}

func applyUnchangedRun(ctx context.Context, stdout io.Writer, payload string, runArgs []string, forceDeploy bool, runner runPayloadContextFunc) error {
	if !forceDeploy {
		return writeRunChangeLine(stdout, "No changes detected")
	}
	if err := writeRunChangeLine(stdout, "No changes detected, forcing deploy"); err != nil {
		return err
	}
	return runner(ctx, payload, runArgs)
}

func writeRunDeployStatus(stdout io.Writer, summary runChangeSummary) error {
	if summary.payloadChanged && summary.payloadLabel != "" {
		return writeRunChangeLine(stdout, "Updated %s", summary.payloadLabel)
	}
	if summary.argsChanged && !summary.payloadChanged {
		return writeRunChangeLine(stdout, "Updated run config")
	}
	return nil
}

var fetchRunChangeServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
	return newRPCClient(host).ServiceInfo(ctx, service)
}

func rejectExistingRunProtectedChanges(ctx context.Context, entry ServiceEntry, runArgs []string) error {
	_, err := inspectExistingRunProtectedChanges(ctx, entry, runArgs)
	return err
}

func inspectExistingRunProtectedChanges(ctx context.Context, entry ServiceEntry, runArgs []string) (catchrpc.ServiceInfoResponse, error) {
	if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Host) == "" || serviceEntryIsVM(entry) {
		return catchrpc.ServiceInfoResponse{}, nil
	}
	flags, requested, authKeySet, err := parseProtectedRunSettings(runArgs)
	if err != nil {
		return catchrpc.ServiceInfoResponse{}, err
	}
	response, err := fetchRunChangeServiceInfoFn(ctx, entry.Host, entry.Name)
	if err != nil {
		return catchrpc.ServiceInfoResponse{}, err
	}
	if !response.Found {
		return response, nil
	}
	if err := rejectExistingRunNetworkSettings(requested, authKeySet, response.Info.Network); err != nil {
		return catchrpc.ServiceInfoResponse{}, err
	}
	if err := rejectExistingRunSandboxChange(entry.Name, flags.Sandbox, response.Info.Sandbox); err != nil {
		return catchrpc.ServiceInfoResponse{}, err
	}
	return response, nil
}

func parseProtectedRunSettings(runArgs []string) (cli.RunFlags, catchrpc.ServiceNetworkSettings, bool, error) {
	flags, _, err := cli.ParseRun(runArgs)
	if err != nil {
		return cli.RunFlags{}, catchrpc.ServiceNetworkSettings{}, false, err
	}
	requested, authKeySet, err := requestedRunNetworkSettingsFromFlags(flags, runArgs)
	return flags, requested, authKeySet, err
}

func rejectExistingRunNetworkSettings(requested catchrpc.ServiceNetworkSettings, authKeySet bool, current catchrpc.ServiceNetwork) error {
	if !authKeySet && reflect.DeepEqual(requested, authoritativeRunNetworkSettings(current)) {
		return nil
	}
	detail := ""
	if authKeySet {
		detail = "; move the explicit --ts-auth-key to service set"
	}
	return fmt.Errorf("network changes for existing services require `yeet service set <service> ...`%s", detail)
}

func rejectExistingRunNetworkChange(ctx context.Context, entry ServiceEntry, runArgs []string) error {
	return rejectExistingRunProtectedChanges(ctx, entry, runArgs)
}

func rejectExistingRunSandboxChange(service string, requested cli.SandboxOptions, currentInfo *catchrpc.ServiceSandbox) error {
	if !requested.HasChange() {
		return nil
	}
	current, err := sandboxPolicyFromServiceInfo(currentInfo)
	if err != nil {
		return err
	}
	target, err := applyRunSandboxOptions(current, requested)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(current, target) {
		return nil
	}
	return fmt.Errorf("sandbox changes for existing services require `%s`", serviceSetCommandForSandboxPolicy(service, current, target))
}

func requestedRunNetworkSettings(runArgs []string) (catchrpc.ServiceNetworkSettings, bool, error) {
	flags, _, err := cli.ParseRun(runArgs)
	if err != nil {
		return catchrpc.ServiceNetworkSettings{}, false, err
	}
	return requestedRunNetworkSettingsFromFlags(flags, runArgs)
}

func requestedRunNetworkSettingsFromFlags(flags cli.RunFlags, runArgs []string) (catchrpc.ServiceNetworkSettings, bool, error) {
	modes, err := normalizeRunNetworkModes(flags.Net)
	if err != nil {
		return catchrpc.ServiceNetworkSettings{}, false, err
	}
	return normalizeRunNetworkSettings(catchrpc.ServiceNetworkSettings{
		Modes: modes, TSVersion: flags.TsVer, TSExitNode: flags.TsExit, TSTags: flags.TsTags,
		MacvlanParent: flags.MacvlanParent, MacvlanVLAN: flags.MacvlanVlan, MacvlanMAC: flags.MacvlanMac,
	}), runArgsHaveFlag(runArgs, "--ts-auth-key"), nil
}

type clientSandboxPolicy struct {
	State    string
	ReadOnly []string
	Writable []string
}

func sandboxEntryFromServiceInfo(info *catchrpc.ServiceSandbox) (state string, ro, rw []string, err error) {
	policy, err := sandboxPolicyFromServiceInfo(info)
	if err != nil {
		return "", nil, nil, err
	}
	return policy.State, cloneSandboxStringSlice(policy.ReadOnly), cloneSandboxStringSlice(policy.Writable), nil
}

func cloneSandboxStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func sandboxPolicyFromServiceInfo(info *catchrpc.ServiceSandbox) (clientSandboxPolicy, error) {
	if info == nil {
		return clientSandboxPolicy{}, fmt.Errorf("catch did not return sandbox state for native service")
	}
	state := strings.ToLower(strings.TrimSpace(info.State))
	if state != "on" && state != "off" && state != "legacy" {
		return clientSandboxPolicy{}, fmt.Errorf("catch returned invalid sandbox state %q", info.State)
	}
	destinations := map[string]string{}
	ro, err := canonicalRPCSandboxExposures("read-only", info.ReadOnly, destinations)
	if err != nil {
		return clientSandboxPolicy{}, err
	}
	rw, err := canonicalRPCSandboxExposures("writable", info.Writable, destinations)
	if err != nil {
		return clientSandboxPolicy{}, err
	}
	return clientSandboxPolicy{State: state, ReadOnly: ro, Writable: rw}, nil
}

func canonicalRPCSandboxExposures(class string, exposures []catchrpc.ServiceSandboxExposure, destinations map[string]string) ([]string, error) {
	if exposures == nil {
		return nil, nil
	}
	out := make([]string, 0, len(exposures))
	for _, exposure := range exposures {
		parsed, err := canonicalRPCSandboxExposure(class, exposure)
		if err != nil {
			return nil, err
		}
		if err := claimRPCSandboxDestination(parsed.Destination, class, destinations); err != nil {
			return nil, err
		}
		out = append(out, cli.FormatSandboxExposure(parsed))
	}
	return canonicalSandboxConfigValues(out), nil
}

func canonicalRPCSandboxExposure(class string, exposure catchrpc.ServiceSandboxExposure) (cli.SandboxExposure, error) {
	source := exposure.Source
	destination := exposure.Destination
	if err := validateRPCSandboxPath(source, "source", false); err != nil {
		return cli.SandboxExposure{}, fmt.Errorf("catch returned invalid %s sandbox exposure %q:%q: %w", class, exposure.Source, exposure.Destination, err)
	}
	if err := validateRPCSandboxPath(destination, "destination", true); err != nil {
		return cli.SandboxExposure{}, fmt.Errorf("catch returned invalid %s sandbox exposure %q:%q: %w", class, exposure.Source, exposure.Destination, err)
	}
	parsed, reset, err := cli.ParseSandboxExposure(cli.FormatSandboxExposure(cli.SandboxExposure{Source: source, Destination: destination}), false)
	if err == nil && (reset || parsed.Source != source || parsed.Destination != destination) {
		err = fmt.Errorf("exposure is not canonical")
	}
	if err != nil {
		return cli.SandboxExposure{}, fmt.Errorf("catch returned invalid %s sandbox exposure %q:%q: %w", class, exposure.Source, exposure.Destination, err)
	}
	return parsed, nil
}

func validateRPCSandboxPath(path, field string, rejectRoot bool) error {
	if strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("%s contains NUL", field)
	}
	if strings.Contains(path, ":") {
		return fmt.Errorf("%s %q contains an unsupported colon", field, path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s %q must be absolute", field, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%s %q must be a clean absolute path", field, path)
	}
	if rejectRoot && path == "/" {
		return fmt.Errorf("%s must not be root", field)
	}
	return nil
}

func claimRPCSandboxDestination(destination, class string, destinations map[string]string) error {
	for previousDestination, previousClass := range destinations {
		if rpcSandboxDestinationsOverlap(destination, previousDestination) {
			return fmt.Errorf(
				"catch returned conflicting sandbox destination %q in %s with %q in %s",
				destination,
				class,
				previousDestination,
				previousClass,
			)
		}
	}
	destinations[destination] = class
	return nil
}

func rpcSandboxDestinationsOverlap(left, right string) bool {
	if left == right || left == "/" || right == "/" {
		return true
	}
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func applyRunSandboxOptions(current clientSandboxPolicy, requested cli.SandboxOptions) (clientSandboxPolicy, error) {
	target := clientSandboxPolicy{
		State: current.State, ReadOnly: cloneSandboxStringSlice(current.ReadOnly), Writable: cloneSandboxStringSlice(current.Writable),
	}
	if requested.StateSet {
		target.State = requested.State
	}
	if (requested.ReadOnlySet || requested.WritableSet) && !requested.StateSet {
		switch current.State {
		case "legacy":
			return clientSandboxPolicy{}, fmt.Errorf("legacy sandbox exposure changes require an explicit --sandbox=on or --sandbox=off with `yeet service set`")
		case "off":
			target.State = "on"
		}
	}
	if requested.ReadOnlySet {
		target.ReadOnly = canonicalCLISandboxExposures(requested.ReadOnly)
	}
	if requested.WritableSet {
		target.Writable = canonicalCLISandboxExposures(requested.Writable)
	}
	return target, nil
}

func canonicalCLISandboxExposures(exposures []cli.SandboxExposure) []string {
	out := make([]string, 0, len(exposures))
	for _, exposure := range exposures {
		out = append(out, cli.FormatSandboxExposure(exposure))
	}
	return canonicalSandboxConfigValues(out)
}

func serviceSetCommandForSandboxPolicy(service string, current, target clientSandboxPolicy) string {
	parts := []string{"yeet", "service", "set", service, "--sandbox=" + target.State}
	parts = appendSandboxServiceSetList(parts, "--sandbox-ro", current.ReadOnly, target.ReadOnly)
	parts = appendSandboxServiceSetList(parts, "--sandbox-rw", current.Writable, target.Writable)
	return shellJoin(parts)
}

func appendSandboxServiceSetList(parts []string, name string, current, target []string) []string {
	if reflect.DeepEqual(current, target) {
		return parts
	}
	targetSet := make(map[string]struct{}, len(target))
	for _, exposure := range target {
		targetSet[exposure] = struct{}{}
	}
	for _, exposure := range current {
		if _, retained := targetSet[exposure]; !retained {
			parts = append(parts, name+"=reset")
			for _, targetExposure := range target {
				parts = append(parts, name+"="+targetExposure)
			}
			return parts
		}
	}
	currentSet := make(map[string]struct{}, len(current))
	for _, exposure := range current {
		currentSet[exposure] = struct{}{}
	}
	for _, exposure := range target {
		if _, alreadyPresent := currentSet[exposure]; !alreadyPresent {
			parts = append(parts, name+"="+exposure)
		}
	}
	return parts
}

func runSandboxCaptureEligible(entry ServiceEntry) bool {
	return strings.TrimSpace(entry.Name) != "" && strings.TrimSpace(entry.Host) != "" && !serviceEntryIsVM(entry)
}

func runSandboxPostSuccessCaptureEligible(payload string, entry ServiceEntry) bool {
	if !runSandboxCaptureEligible(entry) || entry.Name == catchServiceName || entry.Name == systemServiceName {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(entry.Type)) {
	case "docker", "docker-compose", serviceTypeVM:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(entry.PayloadKind)) {
	case "binary", "script":
		return true
	case "file", "":
		payloadType, err := detectPayloadFileType(payload)
		return err == nil && (payloadType == ftdetect.Binary || payloadType == ftdetect.Script)
	default:
		return false
	}
}

func runSandboxPostSuccessRefreshRequired(payload string, entry ServiceEntry, response catchrpc.ServiceInfoResponse, runApplied bool) bool {
	if !runApplied {
		return false
	}
	if !response.Found {
		return runSandboxPostSuccessCaptureEligible(payload, entry)
	}
	return strings.TrimSpace(response.Info.ServiceType) == "systemd" && runSandboxRequestedBackendIsNonNative(payload, entry)
}

func runSandboxRequestedBackendIsNonNative(payload string, entry ServiceEntry) bool {
	if isVMPayload(payload) {
		return false
	}
	if (looksLikeImageRef(payload) || looksLikeRunDraftLocalImageName(payload)) && !payloadNamesExistingFile(payload) {
		return true
	}
	if filepath.Base(payload) == "Dockerfile" {
		return true
	}
	payloadType, err := detectPayloadFileType(payload)
	if err == nil {
		switch payloadType {
		case ftdetect.DockerCompose, ftdetect.TypeScript, ftdetect.Python:
			return true
		case ftdetect.Binary, ftdetect.Script:
			return false
		}
	}
	return runSandboxPayloadKindIsNonNative(entry.PayloadKind)
}

func runSandboxPayloadKindIsNonNative(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "compose", "docker", "docker-compose", "dockerfile", "local-image", "remote-image", "python", "typescript", "ts", "py":
		return true
	default:
		return false
	}
}

func runSandboxRequestedBackendExcludesSandbox(payload string, entry ServiceEntry) bool {
	if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Host) == "" || entry.Name == catchServiceName || entry.Name == systemServiceName {
		return false
	}
	if serviceEntryIsVM(entry) || isVMPayload(payload) {
		return true
	}
	return runSandboxRequestedBackendIsNonNative(payload, entry)
}

func payloadNamesExistingFile(payload string) bool {
	info, err := os.Stat(payload)
	return err == nil && !info.IsDir()
}

func runSandboxCaptureFromResponse(entry ServiceEntry, response catchrpc.ServiceInfoResponse) (clientSandboxPolicy, bool, error) {
	if !runSandboxCaptureEligible(entry) || !response.Found {
		return clientSandboxPolicy{}, false, nil
	}
	if strings.TrimSpace(response.Info.ServiceType) != "systemd" {
		if response.Info.Sandbox != nil {
			return clientSandboxPolicy{}, false, fmt.Errorf("catch returned sandbox state for non-native service type %q", response.Info.ServiceType)
		}
		return clientSandboxPolicy{}, true, nil
	}
	state, ro, rw, err := sandboxEntryFromServiceInfo(response.Info.Sandbox)
	if err != nil {
		return clientSandboxPolicy{}, false, err
	}
	return clientSandboxPolicy{State: state, ReadOnly: ro, Writable: rw}, true, nil
}

func cloneClientSandboxPolicy(policy *clientSandboxPolicy) *clientSandboxPolicy {
	if policy == nil {
		return nil
	}
	return &clientSandboxPolicy{
		State: policy.State, ReadOnly: cloneSandboxStringSlice(policy.ReadOnly), Writable: cloneSandboxStringSlice(policy.Writable),
	}
}

type runSandboxConfigSyncError struct {
	cause      error
	host       string
	service    string
	configPath string
}

func (e *runSandboxConfigSyncError) Error() string {
	command := serviceSetSyncCommand(e.service, e.host, e.configPath)
	return fmt.Sprintf("catch service changed, but its sandbox state could not be saved: %v; recover with `%s`", e.cause, command)
}

func (e *runSandboxConfigSyncError) Unwrap() error {
	return e.cause
}

func newRunSandboxConfigSyncError(host, service, configPath string, err error) error {
	return &runSandboxConfigSyncError{
		cause: err, host: strings.TrimSpace(host), service: strings.TrimSpace(service), configPath: strings.TrimSpace(configPath),
	}
}

func runSandboxCaptureError(entry ServiceEntry, err error) error {
	return newRunSandboxConfigSyncError(entry.Host, entry.Name, "", err)
}

func authoritativeRunNetworkSettings(network catchrpc.ServiceNetwork) catchrpc.ServiceNetworkSettings {
	if network.Desired != nil {
		return normalizeRunNetworkSettings(*network.Desired)
	}
	modes := normalizeRunNetworkModeValues(network.Modes)
	if len(modes) == 0 {
		if network.ISO != nil {
			modes = normalizeRunNetworkModeValues(network.ISO.Modes)
		} else {
			if strings.TrimSpace(network.SvcIP) != "" {
				modes = append(modes, "svc")
			}
			if network.Macvlan != nil {
				modes = append(modes, "lan")
			}
			if network.Tailscale != nil {
				modes = append(modes, "ts")
			}
			modes = normalizeRunNetworkModeValues(modes)
		}
	}
	if len(modes) == 0 {
		modes = []string{"host"}
	}
	settings := catchrpc.ServiceNetworkSettings{Modes: modes}
	if network.Tailscale != nil {
		settings.TSVersion = network.Tailscale.Version
		settings.TSExitNode = network.Tailscale.ExitNode
		settings.TSTags = network.Tailscale.Tags
	}
	if network.Macvlan != nil {
		settings.MacvlanParent = network.Macvlan.Parent
		settings.MacvlanVLAN = network.Macvlan.VLAN
		settings.MacvlanMAC = network.Macvlan.Mac
	}
	return normalizeRunNetworkSettings(settings)
}

func normalizeRunNetworkSettings(settings catchrpc.ServiceNetworkSettings) catchrpc.ServiceNetworkSettings {
	settings.Modes = normalizeRunNetworkModeValues(settings.Modes)
	if len(settings.Modes) == 0 {
		settings.Modes = []string{"host"}
	}
	settings.TSVersion = strings.TrimSpace(settings.TSVersion)
	settings.TSExitNode = strings.TrimSpace(settings.TSExitNode)
	settings.TSTags = normalizeRunNetworkTags(settings.TSTags)
	settings.MacvlanParent = strings.TrimSpace(settings.MacvlanParent)
	settings.MacvlanMAC = strings.TrimSpace(settings.MacvlanMAC)
	return settings
}

func normalizeRunNetworkModes(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "host") {
		return []string{"host"}, nil
	}
	parts := strings.Split(raw, ",")
	modes := normalizeRunNetworkModeValues(parts)
	for _, mode := range modes {
		switch mode {
		case "svc", "lan", "ts", "iso":
		default:
			return nil, fmt.Errorf("unsupported network mode %q", mode)
		}
	}
	return modes, nil
}

func normalizeRunNetworkModeValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeRunNetworkTags(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func writeRunChangeLine(stdout io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(stdout, format+"\n", args...)
	return err
}

func detectRunChanges(payload string, runArgs []string, envFile string, storedArgs []string) (runChangeSummary, error) {
	return detectRunChangesWithOptions(context.Background(), payload, runArgs, envFile, storedArgs, false)
}

func detectRunChangesWithOptions(ctx context.Context, payload string, runArgs []string, envFile string, storedArgs []string, alwaysDeployPayload bool) (runChangeSummary, error) {
	summary := runChangeSummary{
		argsChanged: runArgsChanged(normalizeRunArgs(runArgs), storedArgs),
	}
	needs := classifyRunChangeNeeds(payload, envFile)
	if alwaysDeployPayload {
		needs.payloadHash = false
		needs.alwaysDeployPayload = true
	}
	remoteHashes, supported, err := fetchHashesForRunChanges(ctx, needs)
	if err != nil {
		return summary, err
	}
	if !supported {
		return summaryForUnsupportedHashes(summary, payload, needs), nil
	}
	return detectHashBackedRunChanges(summary, payload, envFile, remoteHashes, needs)
}

type runChangeNeeds struct {
	payloadHash         bool
	envHash             bool
	alwaysDeployPayload bool
}

func classifyRunChangeNeeds(payload string, envFile string) runChangeNeeds {
	alwaysDeploy := shouldAlwaysDeployPayload(payload)
	return runChangeNeeds{
		payloadHash:         !alwaysDeploy,
		envHash:             strings.TrimSpace(envFile) != "",
		alwaysDeployPayload: alwaysDeploy,
	}
}

func (n runChangeNeeds) remoteHashes() bool {
	return n.payloadHash || n.envHash
}

func runArgsChanged(currentArgs []string, storedArgs []string) bool {
	if storedArgs == nil {
		return len(currentArgs) > 0
	}
	if len(currentArgs) == 0 && len(storedArgs) == 0 {
		return false
	}
	return !reflect.DeepEqual(currentArgs, storedArgs)
}

func fetchHashesForRunChanges(ctx context.Context, needs runChangeNeeds) (catchrpc.ArtifactHashesResponse, bool, error) {
	if !needs.remoteHashes() {
		return catchrpc.ArtifactHashesResponse{}, true, nil
	}
	return fetchRemoteArtifactHashesFn(ctx, getService())
}

func summaryForUnsupportedHashes(summary runChangeSummary, payload string, needs runChangeNeeds) runChangeSummary {
	summary.payloadChanged = needs.payloadHash || needs.alwaysDeployPayload
	summary.envChanged = needs.envHash
	if needs.payloadHash {
		summary.payloadLabel = payloadLabelFromLocal(payload, "")
	}
	return summary
}

func detectHashBackedRunChanges(summary runChangeSummary, payload string, envFile string, remoteHashes catchrpc.ArtifactHashesResponse, needs runChangeNeeds) (runChangeSummary, error) {
	if needs.alwaysDeployPayload {
		summary.payloadChanged = true
	} else if needs.payloadHash {
		changed, label, err := detectPayloadHashChange(payload, remoteHashes)
		if err != nil {
			return summary, err
		}
		summary.payloadChanged = changed
		summary.payloadLabel = label
	}
	if needs.envHash {
		changed, err := detectEnvHashChange(envFile, remoteHashes)
		if err != nil {
			return summary, err
		}
		summary.envChanged = changed
	}
	return summary, nil
}

func detectPayloadHashChange(payload string, remoteHashes catchrpc.ArtifactHashesResponse) (bool, string, error) {
	localHash, err := hashFileSHA256(payload)
	if err != nil {
		return false, "", err
	}
	remoteHash, remoteKind := remotePayloadHash(remoteHashes)
	return hashChanged(localHash, remoteHash), payloadLabelFromLocal(payload, remoteKind), nil
}

func detectEnvHashChange(envFile string, remoteHashes catchrpc.ArtifactHashesResponse) (bool, error) {
	localHash, err := hashFileSHA256(envFile)
	if err != nil {
		return false, err
	}
	return hashChanged(localHash, remoteEnvHash(remoteHashes)), nil
}

func hashChanged(localHash, remoteHash string) bool {
	return remoteHash == "" || localHash != remoteHash
}

func remotePayloadHash(resp catchrpc.ArtifactHashesResponse) (string, string) {
	if !resp.Found || resp.Payload == nil {
		return "", ""
	}
	return resp.Payload.SHA256, resp.Payload.Kind
}

func remoteEnvHash(resp catchrpc.ArtifactHashesResponse) string {
	if !resp.Found || resp.Env == nil {
		return ""
	}
	return resp.Env.SHA256
}

func shouldAlwaysDeployPayload(payload string) bool {
	if isVMPayload(payload) {
		return true
	}
	if looksLikeImageRef(payload) || looksLikeRunDraftLocalImageName(payload) {
		if st, err := os.Stat(payload); err == nil && !st.IsDir() {
			return false
		}
		// TODO: add change detection for image refs.
		return true
	}
	if filepath.Base(payload) == "Dockerfile" {
		// TODO: decide how to hash Dockerfile builds for change detection.
		return true
	}
	return false
}

var payloadLabelsByFileType = map[ftdetect.FileType]string{
	ftdetect.Binary:        "binary",
	ftdetect.Script:        "script",
	ftdetect.DockerCompose: "docker compose file",
	ftdetect.TypeScript:    "typescript file",
	ftdetect.Python:        "python file",
}

var payloadLabelsByKind = map[string]string{
	"binary":         "binary",
	"script":         "script",
	"docker compose": "docker compose file",
	"compose":        "docker compose file",
	"docker-compose": "docker compose file",
	"typescript":     "typescript file",
	"ts":             "typescript file",
	"python":         "python file",
	"py":             "python file",
}

func payloadLabelFromLocal(payloadPath, remoteKind string) string {
	if remoteKind != "" {
		return payloadLabelFromKind(remoteKind)
	}
	ft, err := detectPayloadFileType(payloadPath)
	if err != nil {
		return "payload"
	}
	return payloadLabelFromFileType(ft)
}

func detectPayloadFileType(payloadPath string) (ftdetect.FileType, error) {
	goos, goarch := payloadDetectionTarget()
	return ftdetect.DetectFile(payloadPath, goos, goarch)
}

func payloadDetectionTarget() (string, string) {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil || goos == "" || goarch == "" {
		return runtime.GOOS, runtime.GOARCH
	}
	return goos, goarch
}

func payloadLabelFromFileType(ft ftdetect.FileType) string {
	if label, ok := payloadLabelsByFileType[ft]; ok {
		return label
	}
	return "payload"
}

func payloadLabelFromKind(kind string) string {
	if label, ok := payloadLabelsByKind[strings.ToLower(strings.TrimSpace(kind))]; ok {
		return label
	}
	return "payload"
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	return hashReadCloserSHA256(f)
}

func hashReadCloserSHA256(r io.ReadCloser) (sum string, err error) {
	defer func() {
		if closeErr := r.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fetchRemoteArtifactHashes(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
	var resp catchrpc.ArtifactHashesResponse
	if err := newRPCClient(Host()).Call(ctx, "catch.ArtifactHashes", catchrpc.ArtifactHashesRequest{Service: service}, &resp); err != nil {
		if isRPCMethodNotFound(err) {
			return resp, false, nil
		}
		return resp, true, err
	}
	return resp, true, nil
}

func isRPCMethodNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "method not found")
}

var fetchRemoteArtifactHashesFn = fetchRemoteArtifactHashes
