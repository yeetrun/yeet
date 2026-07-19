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
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/copyutil"
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
	skipService, err := commandAllowsMissingService(args)
	if err != nil || skipService {
		return false, err
	}
	res, ok, err := yargs.ResolveCommandWithRegistry(args, remoteRegistry)
	if err != nil || !ok {
		return false, err
	}
	skipService, err = resolvedCommandAllowsMissingService(res, args)
	if err != nil || skipService {
		return false, err
	}
	return resolvedCommandNeedsService(res), nil
}

func commandAllowsMissingService(args []string) (bool, error) {
	if len(args) >= 1 && args[0] == "run" {
		_, web, err := extractRunWebFlag(args[1:])
		if err != nil || web {
			return web, err
		}
	}
	if len(args) < 2 || args[0] != "docker" || args[1] != "update" {
		return false, nil
	}
	flags, remaining, err := cli.ParseDockerUpdate(args[2:])
	if err != nil {
		return false, err
	}
	return flags.Outdated || len(remaining) > 0, nil
}

func resolvedCommandAllowsMissingService(res yargs.ResolvedCommand, args []string) (bool, error) {
	if len(res.Path) > 0 && res.Path[0] == cli.CommandEvents {
		flags, _, err := cli.ParseEvents(args[1:])
		if err != nil {
			return false, err
		}
		if flags.All {
			return true, nil
		}
	}
	return false, nil
}

func resolvedCommandNeedsService(res yargs.ResolvedCommand) bool {
	arg, ok := res.PArg(0)
	if !ok {
		return false
	}
	if !cli.IsServiceArgSpec(arg) {
		return false
	}
	return arg.Required
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

type svcCommandHandler func(context.Context, svcCommandRequest) error

var svcCommandHandlers = map[string]svcCommandHandler{
	"env": func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcEnv(ctx, req)
	},
	"run": func(_ context.Context, req svcCommandRequest) error {
		return handleSvcRun(req)
	},
	"service": func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcService(ctx, req)
	},
	"remove": func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcRemove(ctx, req)
	},
	"copy": func(_ context.Context, req svcCommandRequest) error {
		return handleSvcCopy(req)
	},
	"cron": func(_ context.Context, req svcCommandRequest) error {
		return handleSvcCron(req)
	},
	"stage": func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcStage(ctx, req)
	},
	"snapshots": func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcSnapshots(ctx, req)
	},
	cli.CommandEvents: func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcEvents(ctx, req)
	},
	"status": func(ctx context.Context, req svcCommandRequest) error {
		return handleStatusCommand(ctx, req.Command.Args, req.Config, req.HostOverrideSet)
	},
	"info": func(ctx context.Context, req svcCommandRequest) error {
		return handleInfoCommand(ctx, req.Command.Args, req.Config)
	},
	"docker": func(ctx context.Context, req svcCommandRequest) error {
		if len(req.Command.Args) > 0 && req.Command.Args[0] == "outdated" {
			return handleDockerOutdatedCommand(ctx, req.Command.Args, req.Config, req.HostOverrideSet)
		}
		if len(req.Command.Args) > 0 && req.Command.Args[0] == "update" {
			return handleDockerUpdateCommand(ctx, req)
		}
		return handleSvcRemote(ctx, req)
	},
	"vm": func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcVM(ctx, req)
	},
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
	cfgLoc, err := loadSvcCommandConfig(command)
	if err != nil {
		return svcCommandRequest{}, err
	}

	if err := ensureSvcCommandService(command.CheckArgs); err != nil {
		return svcCommandRequest{}, err
	}

	hostOverride, hardHostOverrideSet := HardHostOverride()
	if err := applySvcCommandHost(cfgLoc, hardHostOverrideSet); err != nil {
		return svcCommandRequest{}, err
	}

	return svcCommandRequest{
		Command:         command,
		Config:          cfgLoc,
		HostOverride:    hostOverride,
		HostOverrideSet: hardHostOverrideSet,
		Service:         getService(),
	}, nil
}

func loadSvcCommandConfig(command svcCommand) (*projectConfigLocation, error) {
	if skip, err := serviceSyncUsesExplicitConfig(command); skip || err != nil {
		return nil, err
	}
	return loadProjectConfigForCommandFromCwd()
}

func serviceSyncUsesExplicitConfig(command svcCommand) (bool, error) {
	if command.Name != "service" || len(command.Args) == 0 || command.Args[0] != "sync" {
		return false, nil
	}
	return serviceSyncHasConfigArg(command.Args[1:])
}

func serviceSyncHasConfigArg(args []string) (bool, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false, nil
		}
		if arg == "--config" {
			if i+1 >= len(args) {
				return false, fmt.Errorf("--config requires a value")
			}
			return strings.TrimSpace(args[i+1]) != "", nil
		}
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimSpace(strings.TrimPrefix(arg, "--config=")) != "", nil
		}
	}
	return false, nil
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
	if handler, ok := svcCommandHandlers[req.Command.Name]; ok {
		return handler(ctx, req)
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

func handleSvcVM(ctx context.Context, req svcCommandRequest) error {
	args := req.Command.RawArgs
	if len(args) >= 2 && args[0] == "vm" && args[1] == "images" {
		flags, remaining, err := cli.ParseVMImages(args[2:])
		if err != nil {
			return err
		}
		if len(remaining) > 0 && remaining[0] == "import" {
			return handleVMImagesImportParsed(ctx, flags, remaining)
		}
	}
	if len(args) >= 2 && args[0] == "vm" && args[1] == "set" {
		return handleVMSet(ctx, req)
	}
	return handleSvcRemote(ctx, req)
}

func handleVMSet(ctx context.Context, req svcCommandRequest) error {
	flags, _, err := cli.ParseVMSet(req.Command.Args[1:])
	if err != nil {
		return err
	}
	if err := execRemoteFn(ctx, req.Service, req.Command.RawArgs, nil, false); err != nil {
		return err
	}
	updated, err := saveVMSetConfig(req.Config, req.HostOverride, flags)
	if err != nil {
		return fmt.Errorf("updated catch VM settings, but failed to update %s: %w", projectConfigName, err)
	}
	if !updated {
		return printServiceSetSyncHint(os.Stdout, req.Service, serviceSetSyncHintHost(req))
	}
	return nil
}

func handleVMImagesImportParsed(ctx context.Context, flags cli.VMImagesFlags, remaining []string) error {
	if len(remaining) != 3 || remaining[0] != "import" {
		return fmt.Errorf("vm images import requires a name and bundle directory")
	}
	name := remaining[1]
	dir := remaining[2]
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("VM image bundle directory does not exist: %s", dir)
	}
	if err != nil {
		return fmt.Errorf("inspect VM image bundle directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("VM image bundle path must be a directory: %s", dir)
	}

	pr, pw := io.Pipe()
	go func() {
		err := copyutil.TarDirectory(pw, dir, "")
		_ = pw.CloseWithError(err)
	}()

	defer func() { _ = pr.Close() }()

	remoteArgs := []string{"vm", "images", "import", name, "--stdin"}
	if flags.AllowLocalKernel {
		remoteArgs = append(remoteArgs, "--allow-local-kernel")
	}
	if flags.Format != "" && flags.Format != "table" {
		remoteArgs = append(remoteArgs, "--format="+flags.Format)
	}
	return withRemoteExecTTYDisabled(func() error {
		return execRemoteFn(ctx, systemServiceName, remoteArgs, pr, false)
	})
}

func withRemoteExecTTYDisabled(fn func() error) error {
	old := execUIOverrides
	tty := false
	execUIOverrides.TTYOverride = &tty
	defer func() {
		execUIOverrides = old
	}()
	return fn()
}

type parsedSvcRun struct {
	Payload                 string
	Args                    []string
	EnvFile                 string
	EnvFileArg              string
	EnvFileSet              bool
	ServiceRoot             string
	ServiceRootZFS          bool
	ServiceRootArg          string
	ServiceRootZFSArg       bool
	ServiceRootSet          bool
	Snapshots               string
	SnapshotKeepLast        int
	SnapshotKeepLastInherit bool
	SnapshotMaxAge          string
	SnapshotMaxAgeInherit   bool
	SnapshotRequired        *bool
	SnapshotRequiredInherit bool
	SnapshotEvents          []string
	SnapshotEventsInherit   bool
	SnapshotChange          bool
	Entry                   ServiceEntry
	ForceDeploy             bool
}

func handleSvcRun(req svcCommandRequest) error {
	cmdArgs := req.Command.Args
	webArgs, web, err := extractRunWebFlag(cmdArgs)
	if err != nil {
		return err
	}
	if web {
		cfgLoc := req.Config
		if cfgLoc == nil {
			cfgLoc, _, err = projectConfigForWrite("service")
			if err != nil {
				return err
			}
		}
		return runWebFn(context.Background(), runWebRequest{
			Args:         webArgs,
			Config:       cfgLoc,
			HostOverride: req.HostOverride,
			Service:      runWebRequestService(req.Service),
			Out:          os.Stdout,
			Err:          os.Stderr,
		})
	}
	cmdArgs = webArgs
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
	draft, err := runDraftFromCLI(cmdArgs, req.Config, req.HostOverride)
	if err != nil {
		return err
	}
	return executeRunDraft(context.Background(), draft, req.Config, false)
}

func runWebRequestService(service string) string {
	if serviceOverride == "" && service == systemServiceName {
		return ""
	}
	return service
}

func parseSvcRun(cmdArgs []string, cfgLoc *projectConfigLocation, hostOverride string) (parsedSvcRun, error) {
	payload, runArgs, err := splitRunPayloadArgs(cmdArgs)
	if err != nil {
		return parsedSvcRun{}, err
	}
	flags, err := parseSvcRunControlFlags(runArgs)
	if err != nil {
		return parsedSvcRun{}, err
	}
	entry, hasEntry := serviceEntryForConfig(cfgLoc, hostOverride)
	if hasEntry {
		if err := ensureRunPublishPortsRetained(entry, flags.Args, payload); err != nil {
			return parsedSvcRun{}, err
		}
	}
	effectiveArgs, err := effectiveSvcRunArgs(entry, hasEntry, flags.Args)
	if err != nil {
		return parsedSvcRun{}, err
	}
	if err := ensureSvcRunEntryFlags(entry, hasEntry, effectiveArgs); err != nil {
		return parsedSvcRun{}, err
	}
	envFile := svcRunEnvFile(flags, entry, hasEntry, cfgLoc)
	serviceRoot, serviceRootZFS := svcRunServiceRoot(flags, entry, hasEntry)
	filteredArgs := runArgsWithServiceRootOptions(effectiveArgs, serviceRootOptions{Root: serviceRoot, ZFS: serviceRootZFS})
	filteredArgs = runArgsWithSnapshotOptions(filteredArgs, snapshotOptionsForSvcRun(entry, flags))
	return parsedSvcRun{
		Payload:                 payload,
		Args:                    filteredArgs,
		EnvFile:                 envFile,
		EnvFileArg:              flags.EnvFileArg,
		EnvFileSet:              flags.EnvFileSet,
		ServiceRoot:             serviceRoot,
		ServiceRootZFS:          serviceRootZFS,
		ServiceRootArg:          flags.ServiceRootArg,
		ServiceRootZFSArg:       flags.ServiceRootZFSArg,
		ServiceRootSet:          flags.ServiceRootSet,
		Snapshots:               flags.Snapshots,
		SnapshotKeepLast:        flags.SnapshotKeepLast,
		SnapshotKeepLastInherit: flags.SnapshotKeepLastInherit,
		SnapshotMaxAge:          flags.SnapshotMaxAge,
		SnapshotMaxAgeInherit:   flags.SnapshotMaxAgeInherit,
		SnapshotRequired:        flags.SnapshotRequired,
		SnapshotRequiredInherit: flags.SnapshotRequiredInherit,
		SnapshotEvents:          flags.SnapshotEvents,
		SnapshotEventsInherit:   flags.SnapshotEventsInherit,
		SnapshotChange:          flags.SnapshotChange,
		Entry:                   entry,
		ForceDeploy:             flags.ForceDeploy,
	}, nil
}

func effectiveSvcRunArgs(entry ServiceEntry, hasEntry bool, runArgs []string) ([]string, error) {
	if !hasEntry {
		return runArgs, nil
	}
	effective, err := effectiveRunArgsForExistingEntry(entry, runArgs)
	if err != nil {
		return nil, err
	}
	return runArgsWithConfiguredIdentity(effective, entry.RunAs), nil
}

func svcRunEnvFile(flags svcRunControlFlags, entry ServiceEntry, hasEntry bool, cfgLoc *projectConfigLocation) string {
	if flags.EnvFileArg != "" || !hasEntry || entry.EnvFile == "" || cfgLoc == nil {
		return flags.EnvFileArg
	}
	return resolveEnvFilePath(cfgLoc.Dir, entry.EnvFile)
}

func svcRunServiceRoot(flags svcRunControlFlags, entry ServiceEntry, hasEntry bool) (string, bool) {
	if flags.ServiceRootArg != "" || !hasEntry {
		return flags.ServiceRootArg, flags.ServiceRootZFSArg
	}
	return entry.ServiceRoot, entry.ServiceRootZFS
}

func snapshotOptionsForSvcRun(entry ServiceEntry, flags svcRunControlFlags) snapshotOptions {
	if flags.SnapshotChange {
		return snapshotOptions{
			Snapshots:       flags.Snapshots,
			KeepLast:        flags.SnapshotKeepLast,
			KeepLastInherit: flags.SnapshotKeepLastInherit,
			MaxAge:          flags.SnapshotMaxAge,
			MaxAgeInherit:   flags.SnapshotMaxAgeInherit,
			Required:        flags.SnapshotRequired,
			RequiredInherit: flags.SnapshotRequiredInherit,
			Events:          flags.SnapshotEvents,
			EventsInherit:   flags.SnapshotEventsInherit,
		}
	}
	return snapshotOptions{
		Snapshots: entry.Snapshots,
		KeepLast:  entry.SnapshotKeepLast,
		MaxAge:    entry.SnapshotMaxAge,
		Required:  entry.SnapshotRequired,
		Events:    entry.SnapshotEvents,
	}
}

func ensureSvcRunEntryFlags(entry ServiceEntry, hasEntry bool, args []string) error {
	if !hasEntry {
		return nil
	}
	return ensureLockedRunFlags(entry, args)
}

type svcRunControlFlags struct {
	Args                    []string
	EnvFileArg              string
	EnvFileSet              bool
	ServiceRootArg          string
	ServiceRootZFSArg       bool
	ServiceRootSet          bool
	Snapshots               string
	SnapshotKeepLast        int
	SnapshotKeepLastInherit bool
	SnapshotMaxAge          string
	SnapshotMaxAgeInherit   bool
	SnapshotRequired        *bool
	SnapshotRequiredInherit bool
	SnapshotEvents          []string
	SnapshotEventsInherit   bool
	SnapshotChange          bool
	ForceDeploy             bool
}

func parseSvcRunControlFlags(runArgs []string) (svcRunControlFlags, error) {
	rootOpts, filteredArgs, serviceRootSet, err := extractServiceRootOptions(runArgs)
	if err != nil {
		return svcRunControlFlags{}, err
	}
	envFileArg, filteredArgs, envFileSet, err := extractEnvFileFlag(filteredArgs)
	if err != nil {
		return svcRunControlFlags{}, err
	}
	forceDeploy, filteredArgs, err := extractForceFlag(filteredArgs)
	if err != nil {
		return svcRunControlFlags{}, err
	}
	snapOpts, filteredArgs, snapshotChange, err := extractSnapshotOptions(filteredArgs)
	if err != nil {
		return svcRunControlFlags{}, err
	}
	return svcRunControlFlags{
		Args:                    filteredArgs,
		EnvFileArg:              envFileArg,
		EnvFileSet:              envFileSet,
		ServiceRootArg:          rootOpts.Root,
		ServiceRootZFSArg:       rootOpts.ZFS,
		ServiceRootSet:          serviceRootSet,
		Snapshots:               snapOpts.Snapshots,
		SnapshotKeepLast:        snapOpts.KeepLast,
		SnapshotKeepLastInherit: snapOpts.KeepLastInherit,
		SnapshotMaxAge:          snapOpts.MaxAge,
		SnapshotMaxAgeInherit:   snapOpts.MaxAgeInherit,
		SnapshotRequired:        snapOpts.Required,
		SnapshotRequiredInherit: snapOpts.RequiredInherit,
		SnapshotEvents:          snapOpts.Events,
		SnapshotEventsInherit:   snapOpts.EventsInherit,
		SnapshotChange:          snapshotChange,
		ForceDeploy:             forceDeploy,
	}, nil
}

func extractSnapshotOptions(args []string) (snapshotOptions, []string, bool, error) {
	if len(args) == 0 {
		return snapshotOptions{}, args, false, nil
	}
	if err := validateSnapshotControlInheritExclusive(args); err != nil {
		return snapshotOptions{}, nil, false, err
	}
	out := make([]string, 0, len(args))
	opts := snapshotOptions{}
	changed := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		next, handled, err := parseSnapshotControlArg(args, i, &opts)
		if err != nil {
			return snapshotOptions{}, nil, false, err
		}
		if handled {
			changed = true
			i = next
			continue
		}
		out = append(out, arg)
	}
	return opts, out, changed, nil
}

func validateSnapshotControlInheritExclusive(args []string) error {
	snapshotsInherit := false
	fieldFlag := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, value, separate := splitSnapshotControlArg(args, i)
		if name == "" {
			continue
		}
		if name == "--snapshots" && strings.EqualFold(strings.TrimSpace(value), "inherit") {
			snapshotsInherit = true
		}
		if name != "--snapshots" {
			fieldFlag = true
		}
		if separate {
			i++
		}
	}
	if snapshotsInherit && fieldFlag {
		return fmt.Errorf("--snapshots=inherit cannot be combined with field-level snapshot flags")
	}
	return nil
}

func splitSnapshotControlArg(args []string, i int) (name string, value string, separate bool) {
	arg := args[i]
	for _, name := range snapshotControlFlagNames {
		if strings.HasPrefix(arg, name+"=") {
			return name, strings.TrimPrefix(arg, name+"="), false
		}
		if arg == name {
			if i+1 >= len(args) {
				return name, "", false
			}
			return name, args[i+1], true
		}
	}
	return "", "", false
}

func parseSnapshotControlArg(args []string, i int, opts *snapshotOptions) (int, bool, error) {
	for _, name := range snapshotControlFlagNames {
		value, next, ok, err := snapshotControlFlagValue(args, i, name)
		if err != nil {
			return next, false, err
		}
		if !ok {
			continue
		}
		if err := applySnapshotControlValue(opts, name, value); err != nil {
			return i, false, err
		}
		return next, true, nil
	}
	return i, false, nil
}

var snapshotControlFlagNames = []string{
	"--snapshots",
	"--snapshot-keep-last",
	"--snapshot-max-age",
	"--snapshot-required",
	"--snapshot-events",
}

func snapshotControlFlagValue(args []string, i int, name string) (string, int, bool, error) {
	arg := args[i]
	if strings.HasPrefix(arg, name+"=") {
		return strings.TrimPrefix(arg, name+"="), i, true, nil
	}
	if arg != name {
		return "", i, false, nil
	}
	if i+1 >= len(args) {
		return "", i, false, snapshotControlMissingValueError(name)
	}
	if snapshotSeparateValueMissing(args[i+1]) {
		return "", i, false, snapshotControlMissingValueError(name)
	}
	return args[i+1], i + 1, true, nil
}

func snapshotSeparateValueMissing(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "--" || strings.HasPrefix(value, "-")
}

func snapshotControlMissingValueError(name string) error {
	if name == "--snapshots" {
		return fmt.Errorf("--snapshots must be on, off, or inherit")
	}
	return fmt.Errorf("%s requires a value", name)
}

func applySnapshotControlValue(opts *snapshotOptions, name, value string) error {
	switch name {
	case "--snapshots":
		mode, err := parseSnapshotModeValue(value)
		if err != nil {
			return err
		}
		opts.Snapshots = mode
	case "--snapshot-keep-last":
		return applySnapshotKeepLastControlValue(opts, value)
	case "--snapshot-max-age":
		applySnapshotMaxAgeControlValue(opts, value)
	case "--snapshot-required":
		return applySnapshotRequiredControlValue(opts, value)
	case "--snapshot-events":
		return applySnapshotEventsControlValue(opts, value)
	}
	return nil
}

func applySnapshotKeepLastControlValue(opts *snapshotOptions, value string) error {
	n, inherit, err := parseOptionalPositiveIntOrInheritFlag(value, "--snapshot-keep-last")
	if err != nil {
		return err
	}
	opts.KeepLast = n
	opts.KeepLastInherit = inherit
	return nil
}

func applySnapshotMaxAgeControlValue(opts *snapshotOptions, value string) {
	if strings.TrimSpace(value) == "inherit" {
		opts.MaxAgeInherit = true
		opts.MaxAge = ""
		return
	}
	opts.MaxAge = strings.TrimSpace(value)
	opts.MaxAgeInherit = false
}

func applySnapshotRequiredControlValue(opts *snapshotOptions, value string) error {
	v, inherit, err := parseOptionalBoolOrInheritFlag(value, "--snapshot-required")
	if err != nil {
		return err
	}
	opts.Required = v
	opts.RequiredInherit = inherit
	return nil
}

func applySnapshotEventsControlValue(opts *snapshotOptions, value string) error {
	if strings.TrimSpace(value) == "inherit" {
		opts.EventsInherit = true
		opts.Events = nil
		return nil
	}
	events, err := splitSnapshotEventList(value)
	if err != nil {
		return err
	}
	opts.Events = events
	opts.EventsInherit = false
	return nil
}

func parseSnapshotModeValue(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "on", "off", "inherit":
		return raw, nil
	default:
		return "", fmt.Errorf("--snapshots must be on, off, or inherit")
	}
}

func parseOptionalBoolOrInheritFlag(raw, name string) (*bool, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}
	if raw == "inherit" {
		return nil, true, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, false, fmt.Errorf("invalid %s value %q", name, raw)
	}
	return &v, false, nil
}

func parseOptionalPositiveIntOrInheritFlag(raw, name string) (int, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	if raw == "inherit" {
		return 0, true, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, false, fmt.Errorf("%s must be a positive integer or inherit", name)
	}
	return n, false, nil
}

func splitSnapshotEventList(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	events := make([]string, 0, len(parts))
	for _, part := range parts {
		event := strings.TrimSpace(part)
		if event == "" {
			return nil, fmt.Errorf("snapshot events must not contain empty values")
		}
		events = append(events, event)
	}
	return events, nil
}

func handleSvcService(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) == 0 {
		return handleSvcRemote(ctx, req)
	}
	switch req.Command.Args[0] {
	case "sync":
		return handleServiceSync(ctx, req)
	case "set":
		return handleServiceSet(ctx, req)
	default:
		return handleSvcRemote(ctx, req)
	}
}

func handleServiceSet(ctx context.Context, req svcCommandRequest) error {
	flags, _, err := cli.ParseServiceSet(req.Command.Args[1:])
	if err != nil {
		return err
	}
	if err := validateServiceSetConfigFlags(flags); err != nil {
		return err
	}
	if err := ensureServiceSetPublishPortsRetained(req.Config, req.HostOverride, req.Service, flags); err != nil {
		return err
	}
	tty := !flags.Copy && !flags.Empty && isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
	if err := execRemoteFn(ctx, req.Service, req.Command.RawArgs, nil, tty); err != nil {
		return wrapServiceSetRemoteError(err, flags)
	}
	return saveServiceSetResult(req, flags)
}

func saveServiceSetResult(req svcCommandRequest, flags cli.ServiceSetFlags) error {
	updated, err := saveServiceSetConfig(req.Config, req.HostOverride, flags)
	if err != nil {
		if flags.RunAsSet {
			return serviceIdentityConfigWriteError(serviceSetConfigHost(req), req.Service, flags.RunAs, err)
		}
		return fmt.Errorf("updated catch service settings, but failed to update %s: %w", projectConfigName, err)
	}
	if !updated {
		return printServiceSetSyncHint(os.Stdout, req.Service, serviceSetSyncHintHost(req))
	}
	return nil
}

func serviceSetConfigHost(req svcCommandRequest) string {
	if host := strings.TrimSpace(req.HostOverride); host != "" {
		return host
	}
	return Host()
}

func serviceIdentityConfigWriteError(host, service, runAs string, _ error) error {
	return fmt.Errorf("service identity changed on %s, but %s was not updated; set run_as = %q for service %q and retry sync", strings.TrimSpace(host), projectConfigName, strings.TrimSpace(runAs), strings.TrimSpace(service))
}

func wrapServiceSetRemoteError(err error, flags cli.ServiceSetFlags) error {
	if err == nil || !serviceSetPublishChanged(flags) {
		return err
	}
	var exitErr remoteExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	return fmt.Errorf("%w\npublished-port changes require catch v0.4.3 or newer; if the remote output says service set requires --service-root or snapshot settings, run `yeet init` for this host and retry", err)
}

func serviceSetPublishChanged(flags cli.ServiceSetFlags) bool {
	return len(flags.Publish) != 0 || flags.PublishReset
}

func serviceSetSyncHintHost(req svcCommandRequest) string {
	if !req.HostOverrideSet {
		return ""
	}
	return strings.TrimSpace(req.HostOverride)
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
	payload, cronArgs, err := splitRunPayloadArgs(cmdArgs)
	if err != nil {
		return err
	}
	flags, binArgs, err := cli.ParseCron(cronArgs)
	if err != nil {
		return err
	}
	explicitRunAs := flags.RunAsSet
	flags = cronFlagsWithConfiguredIdentity(req.Config, req.HostOverride, flags)
	cronFields := strings.Fields(flags.Schedule)
	if err := runCronIdentity(payload, flags, binArgs); err != nil {
		return err
	}
	if err := saveCronConfigWithRunAs(req.Config, req.HostOverride, payload, cronFields, binArgs, flags.RunAs, explicitRunAs); err != nil {
		if explicitRunAs {
			return serviceIdentityConfigWriteError(serviceSetConfigHost(req), req.Service, flags.RunAs, err)
		}
		return err
	}
	return nil
}

func cronFlagsWithConfiguredIdentity(cfgLoc *projectConfigLocation, hostOverride string, flags cli.CronFlags) cli.CronFlags {
	if flags.RunAsSet {
		return flags
	}
	entry, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	if !ok {
		return flags
	}
	runAs := strings.TrimSpace(entry.RunAs)
	if runAs == "" {
		return flags
	}
	flags.RunAs = runAs
	flags.RunAsSet = true
	return flags
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
	return execRemoteFn(ctx, req.Service, req.Command.RawArgs, nil, svcRemoteUsesTTY(req.Command.RawArgs))
}

func svcRemoteUsesTTY(args []string) bool {
	if len(args) > 0 && args[0] == "logs" {
		return false
	}
	return true
}

func ensureRunPublishPortsRetained(entry ServiceEntry, args []string, payload string) error {
	publish, _, err := extractPublishOptions(args)
	if err != nil {
		return err
	}
	if !publish.Changed || publish.Reset {
		return nil
	}
	return ensurePublishPortsRetained(effectiveServiceEntryPorts(entry), publish.Ports, publishGuardCommand{
		Base:    []string{"yeet", "run", entry.Name},
		Current: publish.Ports,
		Suffix:  []string{payload},
	})
}

func ensureServiceSetPublishPortsRetained(cfgLoc *projectConfigLocation, hostOverride string, service string, flags cli.ServiceSetFlags) error {
	if len(flags.Publish) == 0 && !flags.PublishReset {
		return nil
	}
	if flags.PublishReset {
		return nil
	}
	entry, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	if !ok {
		return nil
	}
	return ensurePublishPortsRetained(effectiveServiceEntryPorts(entry), flags.Publish, publishGuardCommand{
		Base:    []string{"yeet", "service", "set", service},
		Current: flags.Publish,
	})
}

type publishGuardCommand struct {
	Base    []string
	Current []string
	Suffix  []string
}

func ensurePublishPortsRetained(existingPorts, desiredPorts []string, command publishGuardCommand) error {
	existingPorts = normalizePublishPorts(existingPorts)
	if len(existingPorts) == 0 {
		return nil
	}
	desiredPorts = normalizePublishPorts(desiredPorts)
	missing := missingPublishPorts(existingPorts, desiredPorts)
	if len(missing) == 0 {
		return nil
	}
	keepPorts := append(append([]string{}, existingPorts...), desiredPorts...)
	return fmt.Errorf("changing published ports would remove existing mappings:\n  %s\n\nTo keep them, include them explicitly:\n  %s\n\nTo replace the published port list, re-run with --publish-reset:\n  %s",
		strings.Join(missing, "\n  "),
		formatPublishGuardCommand(command.Base, keepPorts, nil, command.Suffix),
		formatPublishGuardCommand(command.Base, desiredPorts, []string{"--publish-reset"}, command.Suffix),
	)
}

func missingPublishPorts(existingPorts, desiredPorts []string) []string {
	desired := make(map[string]struct{}, len(desiredPorts))
	for _, port := range desiredPorts {
		desired[port] = struct{}{}
	}
	missing := make([]string, 0)
	for _, port := range existingPorts {
		if _, ok := desired[port]; !ok {
			missing = append(missing, port)
		}
	}
	return missing
}

func formatPublishGuardCommand(base []string, ports []string, extra []string, suffix []string) string {
	parts := append([]string{}, base...)
	parts = append(parts, extra...)
	for _, port := range normalizePublishPorts(ports) {
		parts = append(parts, "-p", port)
	}
	parts = append(parts, suffix...)
	return strings.Join(parts, " ")
}

var tryRunDockerFn = tryRunDockerContext
var buildDockerImageForRemoteFn = buildDockerImageForRemote
var buildDockerImageForRemoteWithOutputFn = buildDockerImageForRemoteWithOutput
var tryRunRemoteImageFn = tryRunRemoteImageContext
var tryRunVMPayloadWithOutputFn = tryRunVMPayloadContextWithOutput
var tryRunDockerfileWithOutputFn = tryRunDockerfileContextWithOutput
var tryRunFileWithOutputFn = tryRunFileContextWithOutput
var tryRunRemoteImageWithOutputFn = tryRunRemoteImageContextWithOutput
var tryRunDockerWithOutputFn = tryRunDockerContextWithOutput
var runFilePayloadWithOutputFn = runFilePayloadContextWithOutput
var execRunFilePayloadWithOutputFn = execRunFilePayloadWithOutput
var stageDockerArgsWithOutputFn = stageDockerArgsWithOutput
var commitDockerStageWithOutputFn = commitDockerStageWithOutput
var imageExistsFn = imageExists
var pushImageFn = pushImage
var execRemoteDirectFn = execRemote
var removeDockerImageFn = removeDockerImage

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
	return runRunContext(context.Background(), payload, args)
}

func runRunContext(ctx context.Context, payload string, args []string) error {
	for _, attempt := range runContextAttempts() {
		ok, err := attempt(ctx, payload, args)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("unknown payload: %s", payload)
}

type runContextAttempt func(context.Context, string, []string) (bool, error)

func runContextAttempts() []runContextAttempt {
	return []runContextAttempt{
		tryRunVMPayloadContext,
		tryRunDockerfileContext,
		tryRunFileContext,
		tryRunRemoteImageFn,
		tryRunDockerFn,
	}
}

func runRunContextWithOutput(ctx context.Context, stdout io.Writer, payload string, args []string) error {
	if isStdoutWriter(stdout) {
		return runRunContext(ctx, payload, args)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	attempts := []runContextWithOutputAttempt{
		tryRunVMPayloadWithOutputFn,
		tryRunDockerfileWithOutputFn,
		tryRunFileWithOutputFn,
		tryRunRemoteImageWithOutputFn,
		tryRunDockerWithOutputFn,
	}
	for _, try := range attempts {
		ok, err := try(ctx, stdout, payload, args)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("unknown payload: %s", payload)
}

type runContextWithOutputAttempt func(context.Context, io.Writer, string, []string) (bool, error)

func tryRunVMPayloadContext(ctx context.Context, payload string, args []string) (bool, error) {
	return tryRunVMPayloadContextWithOutput(ctx, os.Stdout, payload, args)
}

func tryRunVMPayloadContextWithOutput(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
	if !isVMPayload(payload) {
		return false, nil
	}
	flagArgs, payloadArgs := splitRunArgsForParsing(args)
	if len(payloadArgs) != 0 {
		return true, fmt.Errorf("VM payloads do not accept payload args")
	}
	remoteArgs := append([]string{"run"}, flagArgs...)
	remoteArgs = append(remoteArgs, payload)
	if isStdoutWriter(stdout) {
		return true, execRemoteDirectFn(ctx, getService(), remoteArgs, nil, true)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	return true, execRemoteToFn(ctx, getService(), remoteArgs, nil, true, stdout)
}

func tryRunDockerfile(path string, args []string) (ok bool, _ error) {
	return tryRunDockerfileContext(context.Background(), path, args)
}

func tryRunDockerfileContext(ctx context.Context, path string, args []string) (ok bool, _ error) {
	return tryRunDockerfileContextWithOutput(ctx, os.Stdout, path, args)
}

func tryRunDockerfileContextWithOutput(ctx context.Context, stdout io.Writer, path string, args []string) (ok bool, _ error) {
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
	if isStdoutWriter(stdout) {
		if err := buildDockerImageForRemoteFn(ctx, path, imageName); err != nil {
			return true, err
		}
	} else if err := buildDockerImageForRemoteWithOutputFn(ctx, path, imageName, stdout); err != nil {
		return true, err
	}
	var runOK bool
	var err error
	if isStdoutWriter(stdout) {
		runOK, err = tryRunDockerFn(ctx, imageName, args)
	} else {
		runOK, err = tryRunDockerWithOutputFn(ctx, stdout, imageName, args)
	}
	_ = removeDockerImageFn(ctx, imageName)
	return runOK, err
}

const imageComposeTemplate = `services:
  %s:
    image: %s
    restart: unless-stopped
    volumes:
      - "./:/data"
`

func tryRunRemoteImage(image string, args []string) (ok bool, _ error) {
	return tryRunRemoteImageContext(context.Background(), image, args)
}

func tryRunRemoteImageContext(ctx context.Context, image string, args []string) (ok bool, _ error) {
	return tryRunRemoteImageContextWithOutput(ctx, os.Stdout, image, args)
}

func tryRunRemoteImageContextWithOutput(ctx context.Context, stdout io.Writer, image string, args []string) (ok bool, _ error) {
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
	return runFilePayloadWithOutputFn(ctx, stdout, composePath, args, false)
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

func tryRunFileContext(ctx context.Context, file string, args []string) (ok bool, _ error) {
	return tryRunFileContextWithOutput(ctx, os.Stdout, file, args)
}

func tryRunFileContextWithOutput(ctx context.Context, stdout io.Writer, file string, args []string) (ok bool, _ error) {
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
	return runFilePayloadWithOutputFn(ctx, stdout, file, args, true)
}

type runFileUpload struct {
	payload io.ReadCloser
	cleanup func()
	ft      ftdetect.FileType
	goos    string
	goarch  string
}

func runFilePayload(file string, args []string, pushLocalImages bool) (ok bool, _ error) {
	return runFilePayloadContext(context.Background(), file, args, pushLocalImages)
}

func runFilePayloadContext(ctx context.Context, file string, args []string, pushLocalImages bool) (ok bool, _ error) {
	return runFilePayloadContextWithOutput(ctx, os.Stdout, file, args, pushLocalImages)
}

func runFilePayloadContextWithOutput(ctx context.Context, stdout io.Writer, file string, args []string, pushLocalImages bool) (ok bool, _ error) {
	upload, err := prepareRunFileUpload(file, args, pushLocalImages)
	if err != nil {
		return false, err
	}
	defer upload.cleanup()

	svc := getService()
	if err := pushRunFileLocalImages(ctx, svc, upload, pushLocalImages); err != nil {
		return false, err
	}
	var runErr error
	if isStdoutWriter(stdout) {
		runErr = execRunFilePayload(ctx, svc, upload.payload, args)
	} else {
		runErr = execRunFilePayloadWithOutputFn(ctx, stdout, svc, upload.payload, args)
	}
	if runErr != nil {
		return false, runErr
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

func pushRunFileLocalImages(ctx context.Context, svc string, upload runFileUpload, pushLocalImages bool) error {
	if upload.ft != ftdetect.DockerCompose || !pushLocalImages {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := pushAllLocalImagesFn(ctx, svc, upload.goos, upload.goarch); err != nil {
		return fmt.Errorf("failed to push all local images: %w", err)
	}
	return ctx.Err()
}

func execRunFilePayload(ctx context.Context, svc string, payload io.Reader, args []string) error {
	return execRunFilePayloadWithOutput(ctx, os.Stdout, svc, payload, args)
}

func execRunFilePayloadWithOutput(ctx context.Context, stdout io.Writer, svc string, payload io.Reader, args []string) error {
	if stdout == nil {
		stdout = io.Discard
	}
	runArgs := append([]string{"run"}, args...)
	tty := isWriterTerminal(stdout)
	if isStdoutWriter(stdout) {
		if err := execRemoteFn(ctx, svc, runArgs, payload, tty); err != nil {
			return fmt.Errorf("failed to run service: %w", err)
		}
		return nil
	}
	if err := execRemoteToFn(ctx, svc, runArgs, payload, tty, stdout); err != nil {
		return fmt.Errorf("failed to run service: %w", err)
	}
	return nil
}

func tryRunDocker(image string, args []string) (ok bool, _ error) {
	return tryRunDockerContext(context.Background(), image, args)
}

func tryRunDockerContext(ctx context.Context, image string, args []string) (ok bool, _ error) {
	return tryRunDockerContextWithOutput(ctx, os.Stdout, image, args)
}

func tryRunDockerContextWithOutput(ctx context.Context, stdout io.Writer, image string, args []string) (ok bool, _ error) {
	if !imageExistsFn(ctx, image) {
		// If the image does not exist, it's not an error
		return false, nil
	}
	svc := getService()
	if err := pushImageFn(ctx, svc, image, "latest"); err != nil {
		return false, fmt.Errorf("failed to push image: %w", err)
	}
	var err error
	if isStdoutWriter(stdout) {
		err = stageDockerArgs(ctx, svc, args)
	} else {
		err = stageDockerArgsWithOutputFn(ctx, stdout, svc, args)
	}
	if err != nil {
		return false, err
	}
	if isStdoutWriter(stdout) {
		err = commitDockerStage(ctx, svc)
	} else {
		err = commitDockerStageWithOutputFn(ctx, stdout, svc)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func stageDockerArgs(ctx context.Context, svc string, args []string) error {
	return stageDockerArgsWithOutput(ctx, os.Stdout, svc, args)
}

func stageDockerArgsWithOutput(ctx context.Context, stdout io.Writer, svc string, args []string) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if len(args) > 0 {
		stageArgs := append([]string{"stage"}, args...)
		var err error
		if isStdoutWriter(stdout) {
			err = execRemoteDirectFn(ctx, svc, stageArgs, nil, true)
		} else {
			err = execRemoteToFn(ctx, svc, stageArgs, nil, true, stdout)
		}
		if err != nil {
			if _, writeErr := fmt.Fprintln(stdout, "failed to stage args:", err); writeErr != nil {
				return errors.Join(fmt.Errorf("failed to stage args: %w", err), fmt.Errorf("write stage failure: %w", writeErr))
			}
			return fmt.Errorf("failed to stage args: %w", err)
		}
	}
	return nil
}

func commitDockerStage(ctx context.Context, svc string) error {
	return commitDockerStageWithOutput(ctx, os.Stdout, svc)
}

func commitDockerStageWithOutput(ctx context.Context, stdout io.Writer, svc string) error {
	if stdout == nil {
		stdout = io.Discard
	}
	var err error
	if isStdoutWriter(stdout) {
		err = execRemoteDirectFn(ctx, svc, []string{"stage", "commit"}, nil, true)
	} else {
		err = execRemoteToFn(ctx, svc, []string{"stage", "commit"}, nil, true, stdout)
	}
	if err != nil {
		return errors.New("failed to run service")
	}
	return nil
}

func isStdoutWriter(stdout io.Writer) bool {
	f, ok := stdout.(*os.File)
	return ok && f == os.Stdout
}

type terminalFileWriter interface {
	terminalFile() *os.File
}

func isWriterTerminal(stdout io.Writer) bool {
	if w, ok := stdout.(terminalFileWriter); ok {
		f := w.terminalFile()
		return f != nil && isTerminalFn(int(f.Fd()))
	}
	f, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	return isTerminalFn(int(f.Fd()))
}

func runEnvCopy(file string) error {
	return runEnvCopyContext(context.Background(), file)
}

func runEnvCopyContext(ctx context.Context, file string) (err error) {
	return runEnvCopyContextWithExec(ctx, file, nil, func(ctx context.Context, svc string, args []string, stdin io.Reader, tty bool) error {
		return execRemoteFn(ctx, svc, args, stdin, tty)
	})
}

func runEnvCopyContextWithOutputArgs(ctx context.Context, stdout io.Writer, file string, runArgs []string) error {
	if isStdoutWriter(stdout) {
		return runEnvCopyContextWithExec(ctx, file, runArgs, func(ctx context.Context, svc string, args []string, stdin io.Reader, tty bool) error {
			return execRemoteFn(ctx, svc, args, stdin, tty)
		})
	}
	if stdout == nil {
		stdout = io.Discard
	}
	return runEnvCopyContextWithExec(ctx, file, runArgs, func(ctx context.Context, svc string, args []string, stdin io.Reader, tty bool) error {
		return execRemoteToFn(ctx, svc, args, stdin, tty, stdout)
	})
}

func runEnvCopyContextWithExec(ctx context.Context, file string, runArgs []string, execFn func(context.Context, string, []string, io.Reader, bool) error) (err error) {
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
	args, err := envCopyRemoteArgsFromRunArgs(runArgs)
	if err != nil {
		return err
	}
	if err := execFn(ctx, svc, args, f, false); err != nil {
		return err
	}
	return nil
}

func envCopyRemoteArgsFromRunArgs(runArgs []string) ([]string, error) {
	args := []string{"env", "copy"}
	opts, _, found, err := extractServiceRootOptions(runArgs)
	if err != nil {
		return nil, err
	}
	if found {
		args = append(args, serviceRootOptionArgs(opts)...)
	}
	return args, nil
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
	return runCronIdentity(file, cli.CronFlags{Schedule: strings.Join(cronFields, " ")}, binArgs)
}

func runCronIdentity(file string, flags cli.CronFlags, binArgs []string) error {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	payload, cleanup, _, err := openPayloadForUpload(file, goos, goarch)
	if err != nil {
		return err
	}
	defer cleanup()
	cronFields := strings.Fields(flags.Schedule)
	if len(cronFields) != 5 {
		return fmt.Errorf("cron expression must have 5 fields, got %d", len(cronFields))
	}
	svc := getService()
	nargs := []string{"cron"}
	if flags.RunAsSet {
		nargs = append(nargs, "--run-as="+flags.RunAs)
		nargs = append(nargs, "--schedule="+strings.Join(cronFields, " "))
		if len(binArgs) > 0 {
			nargs = append(nargs, "--")
		}
	} else {
		nargs = append(nargs, cronFields...)
	}
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
	if delimiter := slices.Index(args, "--"); delimiter >= 0 {
		cronArgs = args[:delimiter]
		binArgs = append(binArgs, args[delimiter+1:]...)
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
	flags, targets, err := cli.ParseStatus(args)
	if err != nil {
		return err
	}
	if serviceOverride == "" && len(targets) > 0 && shouldAggregateStatusFormat(flags.Format) {
		return statusSelectedServices(ctx, cfgLoc, hostOverrideSet, targets, flags)
	}
	if serviceOverride == "" && shouldAggregateStatusFormat(flags.Format) {
		return statusMultiHost(ctx, statusHosts(cfgLoc, hostOverrideSet), flags)
	}
	if shouldRenderStatusTable(flags.Format) && serviceOverride != "" {
		return renderStatusTableForService(ctx, Host(), serviceOverride)
	}
	svc := getService()
	statusArgs := append([]string{"status"}, args...)
	return execRemoteFn(ctx, svc, statusArgs, nil, true)
}

func shouldAggregateStatusFormat(format string) bool {
	switch strings.TrimSpace(format) {
	case "", "table", "json", "json-pretty":
		return true
	default:
		return false
	}
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
	results, err := collectStatusForHosts(ctx, hosts, flags)
	if err != nil {
		return err
	}
	return renderStatusResults(os.Stdout, results, flags, true)
}

type statusTarget struct {
	host    string
	service string
}

func statusSelectedServices(ctx context.Context, cfgLoc *projectConfigLocation, hostOverrideSet bool, targets []string, flags cli.StatusFlags) error {
	targetsByHost, err := resolveStatusTargets(cfgLoc, hostOverrideSet, targets)
	if err != nil {
		return err
	}
	hosts := make([]string, 0, len(targetsByHost))
	for host := range targetsByHost {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	results, err := collectStatusForHosts(ctx, hosts, flags)
	if err != nil {
		return err
	}
	for i := range results {
		services, err := filterStatusServicesForHost(results[i].Host, results[i].Services, targetsByHost[results[i].Host])
		if err != nil {
			return err
		}
		results[i].Services = services
	}
	return renderStatusResults(os.Stdout, results, flags, false)
}

func resolveStatusTargets(cfgLoc *projectConfigLocation, hostOverrideSet bool, rawTargets []string) (map[string][]string, error) {
	targetsByHost := make(map[string][]string)
	for _, raw := range rawTargets {
		target, err := resolveStatusTarget(cfgLoc, hostOverrideSet, raw)
		if err != nil {
			return nil, err
		}
		if target.service == "" {
			continue
		}
		addStatusTarget(targetsByHost, target)
	}
	if len(targetsByHost) == 0 {
		return nil, fmt.Errorf("status requires at least one service name")
	}
	return targetsByHost, nil
}

func resolveStatusTarget(cfgLoc *projectConfigLocation, hostOverrideSet bool, raw string) (statusTarget, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return statusTarget{}, nil
	}
	service, host, qualified := splitServiceHost(value)
	service = strings.TrimSpace(service)
	host = strings.TrimSpace(host)
	if service == "" {
		return statusTarget{}, fmt.Errorf("status target %q is missing a service name", raw)
	}
	if qualified {
		return statusTarget{host: host, service: service}, nil
	}
	if hostOverrideSet {
		return statusTarget{host: Host(), service: service}, nil
	}
	if cfgLoc != nil && cfgLoc.Config != nil {
		resolved, err := resolveServiceHost(cfgLoc.Config, service)
		if err != nil {
			return statusTarget{}, err
		}
		if resolved != "" {
			return statusTarget{host: resolved, service: service}, nil
		}
	}
	return statusTarget{host: Host(), service: service}, nil
}

func addStatusTarget(targetsByHost map[string][]string, target statusTarget) {
	for _, existing := range targetsByHost[target.host] {
		if existing == target.service {
			return
		}
	}
	targetsByHost[target.host] = append(targetsByHost[target.host], target.service)
}

func filterStatusServicesForHost(host string, statuses []statusService, serviceNames []string) ([]statusService, error) {
	filtered := make([]statusService, 0, len(serviceNames))
	for _, name := range serviceNames {
		status, ok := findStatusService(statuses, name)
		if !ok {
			return nil, fmt.Errorf("status on %s did not include service %q", host, name)
		}
		filtered = append(filtered, status)
	}
	return filtered, nil
}

func findStatusService(statuses []statusService, serviceName string) (statusService, bool) {
	for _, status := range statuses {
		if status.ServiceName == serviceName {
			return status, true
		}
	}
	return statusService{}, false
}

func collectStatusForHosts(ctx context.Context, hosts []string, flags cli.StatusFlags) ([]hostStatusData, error) {
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
			return nil, res.err
		}
		results = append(results, hostStatusData{Host: res.host, Services: res.services})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Host < results[j].Host
	})
	return results, nil
}

func renderStatusResults(w io.Writer, results []hostStatusData, flags cli.StatusFlags, aggregateContainers bool) error {
	format := strings.TrimSpace(flags.Format)
	if format == "json" || format == "json-pretty" {
		enc := json.NewEncoder(w)
		if format == "json-pretty" {
			enc.SetIndent("", "  ")
		}
		return enc.Encode(results)
	}
	return renderStatusTables(w, results, aggregateContainers)
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
	stored, err := storedRunServiceConfig(cfgLoc, hostOverride)
	if err != nil {
		return err
	}
	payload := resolvePayloadPathForEntry(cfgLoc.Dir, stored.Entry)
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("no payload configured for %s@%s", stored.Service, stored.Host)
	}
	envFile := resolveEnvFilePath(cfgLoc.Dir, stored.Entry.EnvFile)
	runArgs := runArgsWithPublishOptions(rehydrateRunArgs(stored.Entry.Args), stored.Entry.Ports)
	runArgs = runArgsWithConfiguredIdentity(runArgs, stored.Entry.RunAs)
	runArgs = runArgsWithServiceRootOptions(runArgs, serviceRootOptions{Root: stored.Entry.ServiceRoot, ZFS: stored.Entry.ServiceRootZFS})
	runArgs = runArgsWithSnapshotOptions(runArgs, snapshotOptions{
		Snapshots: stored.Entry.Snapshots,
		KeepLast:  stored.Entry.SnapshotKeepLast,
		MaxAge:    stored.Entry.SnapshotMaxAge,
		Required:  stored.Entry.SnapshotRequired,
		Events:    stored.Entry.SnapshotEvents,
	})
	if strings.TrimSpace(stored.Entry.PayloadKind) == "local-image" {
		return runWithChangesToWithRunner(os.Stdout, payload, runArgs, envFile, stored.Entry, forceDeploy, runLocalImagePayload, true)
	}
	if strings.TrimSpace(stored.Entry.Type) == serviceTypeVM {
		runner := func(ctx context.Context, payload string, args []string) error {
			return runVMPayloadContextWithOutput(ctx, os.Stdout, payload, args)
		}
		return runWithChangesToWithContextRunner(context.Background(), os.Stdout, payload, runArgs, envFile, stored.Entry, forceDeploy, runner, true)
	}
	return runWithChanges(payload, runArgs, envFile, stored.Entry, forceDeploy)
}

func storedRunServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) (storedService, error) {
	stored, err := storedServiceConfigWithoutTypeCheck(cfgLoc, hostOverride, "run")
	if err != nil {
		return storedService{}, err
	}
	gotType := strings.TrimSpace(stored.Entry.Type)
	if gotType == "" || gotType == serviceTypeRun || gotType == serviceTypeVM {
		return stored, nil
	}
	return storedService{}, validateStoredServiceType(stored.Service, stored.Host, gotType, "run", serviceTypeRun)
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
	runAs := strings.TrimSpace(stored.Entry.RunAs)
	return runCronIdentity(payload, cli.CronFlags{
		RunAs:    runAs,
		RunAsSet: runAs != "",
		Schedule: strings.Join(cronFields, " "),
	}, stored.Entry.Args)
}

type storedService struct {
	Service string
	Host    string
	Entry   ServiceEntry
}

func storedServiceConfig(cfgLoc *projectConfigLocation, hostOverride, commandName, wantType string) (storedService, error) {
	stored, err := storedServiceConfigWithoutTypeCheck(cfgLoc, hostOverride, commandName)
	if err != nil {
		return storedService{}, err
	}
	if err := validateStoredServiceType(stored.Service, stored.Host, stored.Entry.Type, commandName, wantType); err != nil {
		return storedService{}, err
	}
	return stored, nil
}

func storedServiceConfigWithoutTypeCheck(cfgLoc *projectConfigLocation, hostOverride, commandName string) (storedService, error) {
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

func saveRunConfig(cfgLoc *projectConfigLocation, hostOverride string, payload string, runArgs []string, serviceRoot string, serviceRootZFS bool) error {
	return saveRunConfigWithPayloadKind(cfgLoc, hostOverride, payload, "", runArgs, serviceRoot, serviceRootZFS)
}

func saveRunConfigWithPayloadKind(cfgLoc *projectConfigLocation, hostOverride string, payload string, payloadKind string, runArgs []string, serviceRoot string, serviceRootZFS bool) error {
	if serviceOverride == "" {
		return nil
	}
	loc, err := runConfigLocation(cfgLoc)
	if err != nil {
		return err
	}
	if loc == nil {
		return nil
	}
	host := runConfigHost(hostOverride)
	serviceRoot, serviceRootZFS, filteredArgs, err := runConfigServiceRoot(runArgs, serviceRoot, serviceRootZFS)
	if err != nil {
		return err
	}
	publish, filteredArgs, err := extractPublishOptions(filteredArgs)
	if err != nil {
		return err
	}
	existing, hasExisting := runConfigExistingEntry(loc, host)
	ports := publish.Ports
	if !publish.Changed && hasExisting {
		ports = effectiveServiceEntryPorts(existing)
	}
	snapOpts, filteredArgs, snapshotChange, err := extractSnapshotOptions(filteredArgs)
	if err != nil {
		return err
	}
	runFlags, _, err := cli.ParseRun(filteredArgs)
	if err != nil {
		return err
	}
	filteredArgs = removeRunAsControlFlag(filteredArgs)
	entryType, payloadKind := runConfigEntryType(payload, payloadKind)
	payloadRel := relativePayloadPathForKind(loc.Dir, payload, payloadKind)
	entry := ServiceEntry{
		Name:           serviceOverride,
		Host:           host,
		Type:           entryType,
		Payload:        payloadRel,
		PayloadKind:    payloadKind,
		RunAs:          runFlags.RunAs,
		ServiceRoot:    strings.TrimSpace(serviceRoot),
		ServiceRootZFS: serviceRootZFS,
		Ports:          normalizePublishPorts(ports),
		Args:           normalizeRunArgs(filteredArgs),
	}
	applyRunConfigSnapshotFields(&entry, existing, hasExisting, snapOpts, snapshotChange)
	loc.Config.SetServiceEntry(entry)
	return saveProjectConfig(loc)
}

func runConfigEntryType(payload string, payloadKind string) (string, string) {
	payloadKind = strings.TrimSpace(payloadKind)
	if payloadKind == serviceTypeVM || isVMPayload(payload) {
		return serviceTypeVM, serviceTypeVM
	}
	return "", payloadKind
}

func runConfigLocation(cfgLoc *projectConfigLocation) (*projectConfigLocation, error) {
	if cfgLoc != nil {
		return cfgLoc, nil
	}
	loc, _, err := projectConfigForWrite("service")
	return loc, err
}

func runConfigHost(hostOverride string) string {
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		return Host()
	}
	return host
}

func runConfigServiceRoot(runArgs []string, serviceRoot string, serviceRootZFS bool) (string, bool, []string, error) {
	rootOpts, filteredArgs, foundServiceRoot, err := extractServiceRootOptions(runArgs)
	if err != nil {
		return "", false, nil, err
	}
	if foundServiceRoot && strings.TrimSpace(serviceRoot) == "" {
		return rootOpts.Root, rootOpts.ZFS, filteredArgs, nil
	}
	return serviceRoot, serviceRootZFS, filteredArgs, nil
}

func runConfigExistingEntry(loc *projectConfigLocation, host string) (ServiceEntry, bool) {
	if loc == nil || loc.Config == nil {
		return ServiceEntry{}, false
	}
	return loc.Config.ServiceEntry(serviceOverride, strings.TrimSpace(host))
}

func applyRunConfigSnapshotFields(entry *ServiceEntry, existing ServiceEntry, hasExisting bool, opts snapshotOptions, changed bool) {
	if hasExisting && serviceEntryHasSnapshotOverride(existing) {
		copySnapshotFieldsFromEntry(entry, existing)
	}
	if !changed {
		return
	}
	if opts.Snapshots == "inherit" {
		entry.ClearSnapshotOverride()
		return
	}
	applySnapshotOptionsToEntry(entry, opts)
}

func copySnapshotFieldsFromEntry(dst *ServiceEntry, src ServiceEntry) {
	dst.Snapshots = src.Snapshots
	dst.SnapshotKeepLast = src.SnapshotKeepLast
	dst.SnapshotMaxAge = src.SnapshotMaxAge
	dst.SnapshotRequired = cloneBoolPtr(src.SnapshotRequired)
	dst.SnapshotEvents = cloneStringSlice(src.SnapshotEvents)
}

func applySnapshotOptionsToEntry(entry *ServiceEntry, opts snapshotOptions) {
	if opts.Snapshots != "" {
		entry.Snapshots = opts.Snapshots
	}
	if opts.KeepLastInherit {
		entry.SnapshotKeepLast = 0
	} else if opts.KeepLast != 0 {
		entry.SnapshotKeepLast = opts.KeepLast
	}
	if opts.MaxAgeInherit {
		entry.SnapshotMaxAge = ""
	} else if opts.MaxAge != "" {
		entry.SnapshotMaxAge = opts.MaxAge
	}
	if opts.RequiredInherit {
		entry.SnapshotRequired = nil
	} else if opts.Required != nil {
		entry.SnapshotRequired = cloneBoolPtr(opts.Required)
	}
	if opts.EventsInherit {
		entry.SnapshotEvents = nil
	} else if len(opts.Events) != 0 {
		entry.SnapshotEvents = cloneStringSlice(opts.Events)
	}
}

func saveServiceSetConfig(cfgLoc *projectConfigLocation, hostOverride string, flags cli.ServiceSetFlags) (bool, error) {
	if serviceOverride == "" {
		return false, nil
	}
	entry, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	if !ok {
		return false, nil
	}
	if err := applyServiceSetConfigFlags(&entry, flags); err != nil {
		return false, err
	}
	cfgLoc.Config.SetServiceEntry(entry)
	return true, saveProjectConfig(cfgLoc)
}

func validateServiceSetConfigFlags(flags cli.ServiceSetFlags) error {
	entry := ServiceEntry{}
	return applyServiceSetConfigFlags(&entry, flags)
}

func applyServiceSetConfigFlags(entry *ServiceEntry, flags cli.ServiceSetFlags) error {
	if flags.RunAsSet {
		entry.RunAs = strings.TrimSpace(flags.RunAs)
	}
	if strings.TrimSpace(flags.ServiceRoot) != "" {
		entry.ServiceRoot = strings.TrimSpace(flags.ServiceRoot)
		entry.ServiceRootZFS = flags.ZFS
	}
	if len(flags.Publish) != 0 || flags.PublishReset {
		entry.Ports = normalizePublishPorts(flags.Publish)
	}
	return applyServiceSetSnapshotFlags(entry, flags)
}

type runFlagUpdate struct {
	Name  string
	Value string
}

func saveVMSetConfig(cfgLoc *projectConfigLocation, hostOverride string, flags cli.VMSetFlags) (bool, error) {
	if serviceOverride == "" {
		return false, nil
	}
	entry, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	if !ok {
		return false, nil
	}
	if !applyVMSetConfigFlags(&entry, flags) {
		return false, nil
	}
	cfgLoc.Config.SetServiceEntry(entry)
	return true, saveProjectConfig(cfgLoc)
}

func applyVMSetConfigFlags(entry *ServiceEntry, flags cli.VMSetFlags) bool {
	removals, updates := vmSetRunFlagChanges(flags)
	if len(removals) == 0 || !serviceEntryIsVM(*entry) {
		return false
	}
	entry.Args = canonicalizeStoredVMRunArgs(entry.Args)
	entry.Args = rewriteStoredRunArgs(entry.Args, removals, updates)
	return true
}

func canonicalizeStoredVMRunArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i, arg := range out {
		switch {
		case arg == "--cpus":
			out[i] = "--vcpus"
		case strings.HasPrefix(arg, "--cpus="):
			out[i] = "--vcpus=" + strings.TrimPrefix(arg, "--cpus=")
		}
	}
	return out
}

func serviceEntryIsVM(entry ServiceEntry) bool {
	return strings.TrimSpace(entry.Type) == serviceTypeVM ||
		strings.TrimSpace(entry.PayloadKind) == serviceTypeVM ||
		isVMPayload(entry.Payload)
}

func vmSetRunFlagChanges(flags cli.VMSetFlags) (map[string]bool, []runFlagUpdate) {
	removals := map[string]bool{}
	var updates []runFlagUpdate
	add := func(name, value string) {
		removals[name] = true
		updates = append(updates, runFlagUpdate{Name: name, Value: value})
	}
	addVMSetShapeRunFlagChanges(flags, add)
	addVMSetNetworkRunFlagChanges(flags, removals, &updates, add)
	return removals, updates
}

func addVMSetShapeRunFlagChanges(flags cli.VMSetFlags, add func(string, string)) {
	if flags.CPUs > 0 {
		add("--vcpus", strconv.Itoa(flags.CPUs))
	}
	if value := strings.TrimSpace(flags.Memory); value != "" {
		add("--memory", value)
	}
	if value := strings.TrimSpace(flags.MemoryMin); value != "" {
		add("--memory-min", value)
	}
	if value := strings.TrimSpace(flags.Balloon); value != "" {
		add("--balloon", value)
	}
	if value := strings.TrimSpace(flags.Disk); value != "" {
		add("--disk", value)
	}
}

func addVMSetNetworkRunFlagChanges(flags cli.VMSetFlags, removals map[string]bool, updates *[]runFlagUpdate, add func(string, string)) {
	if flags.NetworkChange {
		removals["--net"] = true
		removals["--macvlan-parent"] = true
		removals["--macvlan-vlan"] = true
		removals["--macvlan-mac"] = true
		if value := strings.TrimSpace(flags.Net); value != "" {
			*updates = append(*updates, runFlagUpdate{Name: "--net", Value: value})
		}
	}
	if value := strings.TrimSpace(flags.MacvlanParent); value != "" {
		add("--macvlan-parent", value)
	}
	if flags.MacvlanVlan != 0 {
		add("--macvlan-vlan", strconv.Itoa(flags.MacvlanVlan))
	}
	if value := strings.TrimSpace(flags.MacvlanMac); value != "" {
		add("--macvlan-mac", value)
	}
}

func rewriteStoredRunArgs(args []string, removals map[string]bool, updates []runFlagUpdate) []string {
	flagArgs, payloadArgs := splitRunArgsForParsing(rehydrateRunArgs(args))
	out := removeRunFlags(flagArgs, removals)
	for _, update := range updates {
		out = append(out, update.Name+"="+update.Value)
	}
	if len(payloadArgs) != 0 {
		out = append(out, "--")
		out = append(out, payloadArgs...)
	}
	return normalizeRunArgs(out)
}

func removeRunFlags(args []string, removals map[string]bool) []string {
	out := make([]string, 0, len(args))
	specs := cli.RemoteFlagSpecs()["run"]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := flagName(arg)
		if !removals[name] {
			out = append(out, arg)
			continue
		}
		if spec, ok := specs[name]; ok && spec.ConsumesValue && !strings.Contains(arg, "=") {
			i++
		}
	}
	return out
}

func applyServiceSetSnapshotFlags(entry *ServiceEntry, flags cli.ServiceSetFlags) error {
	if !flags.SnapshotChange {
		return nil
	}
	if err := validateServiceSetSnapshotInheritExclusive(flags); err != nil {
		return err
	}
	if flags.Snapshots == "inherit" {
		entry.ClearSnapshotOverride()
		return nil
	}
	return applyServiceSetSnapshotOverride(entry, flags)
}

func validateServiceSetSnapshotInheritExclusive(flags cli.ServiceSetFlags) error {
	if flags.Snapshots != "inherit" {
		return nil
	}
	if flags.SnapshotKeepLast == "" && flags.SnapshotMaxAge == "" && flags.SnapshotRequired == "" && flags.SnapshotEvents == "" {
		return nil
	}
	return fmt.Errorf("--snapshots=inherit cannot be combined with field-level snapshot flags")
}

func applyServiceSetSnapshotOverride(entry *ServiceEntry, flags cli.ServiceSetFlags) error {
	if flags.Snapshots != "" {
		entry.Snapshots = flags.Snapshots
	}
	if flags.SnapshotKeepLast != "" {
		if flags.SnapshotKeepLast == "inherit" {
			entry.SnapshotKeepLast = 0
			return applyServiceSetSnapshotTextFields(entry, flags)
		}
		n, err := strconv.Atoi(flags.SnapshotKeepLast)
		if err != nil || n < 1 {
			return fmt.Errorf("--snapshot-keep-last must be a positive integer or inherit")
		}
		entry.SnapshotKeepLast = n
	}
	return applyServiceSetSnapshotTextFields(entry, flags)
}

func applyServiceSetSnapshotTextFields(entry *ServiceEntry, flags cli.ServiceSetFlags) error {
	if flags.SnapshotMaxAge != "" {
		if flags.SnapshotMaxAge == "inherit" {
			entry.SnapshotMaxAge = ""
		} else {
			entry.SnapshotMaxAge = flags.SnapshotMaxAge
		}
	}
	if flags.SnapshotRequired != "" {
		if flags.SnapshotRequired == "inherit" {
			entry.SnapshotRequired = nil
			return applyServiceSetSnapshotEvents(entry, flags.SnapshotEvents)
		}
		v, err := strconv.ParseBool(flags.SnapshotRequired)
		if err != nil {
			return fmt.Errorf("invalid --snapshot-required value %q", flags.SnapshotRequired)
		}
		entry.SnapshotRequired = &v
	}
	return applyServiceSetSnapshotEvents(entry, flags.SnapshotEvents)
}

func applyServiceSetSnapshotEvents(entry *ServiceEntry, raw string) error {
	if raw == "" {
		return nil
	}
	if raw == "inherit" {
		entry.SnapshotEvents = nil
		return nil
	}
	events, err := splitSnapshotEventList(raw)
	if err != nil {
		return err
	}
	entry.SnapshotEvents = events
	return nil
}

func printServiceSetSyncHint(w io.Writer, service string, host string) error {
	if _, err := fmt.Fprintln(w, "Updated catch service settings. No matching yeet.toml entry was updated."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "Run from the project directory, or run:"); err != nil {
		return err
	}
	cmd := "yeet"
	if host = strings.TrimSpace(host); host != "" {
		cmd += " --host " + host
	}
	if strings.TrimSpace(service) == "" {
		_, err := fmt.Fprintf(w, "  %s service sync <svc> --config ~/yeet-services/yeet.toml\n", cmd)
		return err
	}
	_, err := fmt.Fprintf(w, "  %s service sync %s --config ~/yeet-services/yeet.toml\n", cmd, service)
	return err
}

func saveCronConfig(cfgLoc *projectConfigLocation, hostOverride string, payload string, cronFields []string, binArgs []string) error {
	return saveCronConfigWithRunAs(cfgLoc, hostOverride, payload, cronFields, binArgs, "", false)
}

func saveCronConfigWithRunAs(cfgLoc *projectConfigLocation, hostOverride string, payload string, cronFields []string, binArgs []string, runAs string, runAsSet bool) error {
	if serviceOverride == "" {
		return nil
	}
	loc := cfgLoc
	if loc == nil {
		var err error
		loc, _, err = projectConfigForWrite("cron")
		if err != nil {
			return err
		}
		if loc == nil {
			return nil
		}
	}
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	if !runAsSet {
		if existing, ok := loc.Config.ServiceEntry(serviceOverride, host); ok {
			runAs = existing.RunAs
		}
	}
	payloadRel := relativePayloadPath(loc.Dir, payload)
	entry := ServiceEntry{
		Name:     serviceOverride,
		Host:     host,
		Type:     serviceTypeCron,
		Payload:  payloadRel,
		RunAs:    strings.TrimSpace(runAs),
		Schedule: strings.Join(cronFields, " "),
		Args:     normalizeArgs(binArgs),
	}
	loc.Config.ReplaceServiceEntry(entry)
	return saveProjectConfig(loc)
}

func runArgsWithConfiguredIdentity(args []string, runAs string) []string {
	runAs = strings.TrimSpace(runAs)
	if runAs == "" || runArgsHaveFlag(args, "--run-as") {
		return args
	}
	flagArgs, payloadArgs := splitRunArgsForParsing(args)
	flagArgs = append(flagArgs, "--run-as="+runAs)
	if len(payloadArgs) == 0 {
		return flagArgs
	}
	return append(append(flagArgs, "--"), payloadArgs...)
}

func removeRunAsControlFlag(args []string) []string {
	flagArgs, payloadArgs := splitRunArgsForParsing(args)
	flagArgs = removeRunFlags(flagArgs, map[string]bool{"--run-as": true})
	if len(payloadArgs) == 0 {
		return flagArgs
	}
	return append(append(flagArgs, "--"), payloadArgs...)
}
