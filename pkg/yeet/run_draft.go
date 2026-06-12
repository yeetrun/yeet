// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
)

type RunDraft struct {
	Service        string            `json:"service"`
	Host           string            `json:"host"`
	Payload        string            `json:"payload"`
	PayloadKind    string            `json:"payloadKind,omitempty"`
	EnvFile        string            `json:"envFile,omitempty"`
	Pull           bool              `json:"pull,omitempty"`
	EnvFileArg     string            `json:"-"`
	EnvFileSet     bool              `json:"-"`
	PayloadArgs    []string          `json:"payloadArgs,omitempty"`
	VM             RunDraftVM        `json:"vm,omitempty"`
	Network        RunDraftNetwork   `json:"network"`
	Storage        RunDraftStorage   `json:"storage"`
	Snapshots      RunDraftSnapshots `json:"snapshots"`
	SnapshotChange bool              `json:"-"`
	NewServiceOnly bool              `json:"newServiceOnly,omitempty"`
	ForceDeploy    bool              `json:"forceDeploy,omitempty"`
	RunArgs        []string          `json:"-"`
	RunArgsSet     bool              `json:"-"`
	ExistingEntry  ServiceEntry      `json:"-"`
}

type RunDraftNetwork struct {
	Modes         []string `json:"modes,omitempty"`
	TSVersion     string   `json:"tsVersion,omitempty"`
	TSExitNode    string   `json:"tsExitNode,omitempty"`
	TSTags        []string `json:"tsTags,omitempty"`
	TSAuthKey     string   `json:"tsAuthKey,omitempty"`
	MacvlanMAC    string   `json:"macvlanMac,omitempty"`
	MacvlanVLAN   int      `json:"macvlanVlan,omitempty"`
	MacvlanParent string   `json:"macvlanParent,omitempty"`
	Restart       *bool    `json:"restart,omitempty"`
	Publish       []string `json:"publish,omitempty"`
}

type RunDraftVM struct {
	CPUs   int    `json:"cpus,omitempty"`
	Memory string `json:"memory,omitempty"`
	Disk   string `json:"disk,omitempty"`
}

type RunDraftStorage struct {
	ServiceRoot string `json:"serviceRoot,omitempty"`
	ZFS         bool   `json:"zfs,omitempty"`
}

type RunDraftSnapshots struct {
	Mode            string   `json:"mode,omitempty"`
	KeepLast        int      `json:"keepLast,omitempty"`
	KeepLastInherit bool     `json:"keepLastInherit,omitempty"`
	MaxAge          string   `json:"maxAge,omitempty"`
	MaxAgeInherit   bool     `json:"maxAgeInherit,omitempty"`
	Required        *bool    `json:"required,omitempty"`
	RequiredInherit bool     `json:"requiredInherit,omitempty"`
	Events          []string `json:"events,omitempty"`
	EventsInherit   bool     `json:"eventsInherit,omitempty"`
}

func runDraftFromCLI(cmdArgs []string, cfgLoc *projectConfigLocation, hostOverride string) (RunDraft, error) {
	payload, runArgs, err := splitRunPayloadArgs(cmdArgs)
	if err != nil {
		return RunDraft{}, err
	}
	flags, err := parseSvcRunControlFlags(runArgs)
	if err != nil {
		return RunDraft{}, err
	}
	parsedFlags, _, err := cli.ParseRun(flags.Args)
	if err != nil {
		return RunDraft{}, err
	}
	if parsedFlags.Web {
		return RunDraft{}, fmt.Errorf("--web starts the local web deploy UI and cannot be forwarded to catch")
	}
	entry, hasEntry := serviceEntryForConfig(cfgLoc, hostOverride)
	effectiveArgs, err := effectiveSvcRunArgs(entry, hasEntry, flags.Args)
	if err != nil {
		return RunDraft{}, err
	}
	if err := ensureSvcRunEntryFlags(entry, hasEntry, effectiveArgs); err != nil {
		return RunDraft{}, err
	}
	effectiveParsed, _, err := cli.ParseRun(effectiveArgs)
	if err != nil {
		return RunDraft{}, err
	}
	envFile := svcRunEnvFile(flags, entry, hasEntry, cfgLoc)
	serviceRoot, serviceRootZFS := svcRunServiceRoot(flags, entry, hasEntry)
	snapshots := snapshotOptionsForSvcRun(entry, flags)
	filteredArgs := runArgsWithServiceRootOptions(effectiveArgs, serviceRootOptions{Root: serviceRoot, ZFS: serviceRootZFS})
	filteredArgs = runArgsWithSnapshotOptions(filteredArgs, snapshots)
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}

	return RunDraft{
		Service:        getService(),
		Host:           host,
		Payload:        payload,
		EnvFile:        envFile,
		Pull:           effectiveParsed.Pull,
		EnvFileArg:     flags.EnvFileArg,
		EnvFileSet:     flags.EnvFileSet,
		PayloadArgs:    payloadArgsFromRunArgs(effectiveArgs),
		VM:             runDraftVMFromRunFlags(effectiveParsed),
		Network:        runDraftNetworkFromRunFlags(effectiveParsed),
		Storage:        RunDraftStorage{ServiceRoot: serviceRoot, ZFS: serviceRootZFS},
		Snapshots:      runDraftSnapshotsFromOptions(snapshots),
		SnapshotChange: flags.SnapshotChange,
		ForceDeploy:    flags.ForceDeploy,
		RunArgs:        append([]string{}, filteredArgs...),
		RunArgsSet:     true,
		ExistingEntry:  entry,
	}, nil
}

func (d RunDraft) runArgs() []string {
	if d.RunArgsSet {
		return append([]string{}, d.RunArgs...)
	}
	args := runArgsFromDraftNetwork(d.Network)
	if d.Pull {
		args = append(args, "--pull")
	}
	args = runArgsWithServiceRootOptions(args, serviceRootOptions{
		Root: d.Storage.ServiceRoot,
		ZFS:  d.Storage.ZFS,
	})
	args = runArgsWithSnapshotOptions(args, runDraftSnapshotOptions(d.Snapshots))
	args = appendRunDraftVMArgs(args, d.VM)
	if len(d.PayloadArgs) != 0 {
		args = append(args, "--")
		args = append(args, d.PayloadArgs...)
	}
	return args
}

func runArgsFromDraftNetwork(n RunDraftNetwork) []string {
	var args []string
	args = appendRunDraftStringFlag(args, "--net", joinRunDraftModes(n.Modes))
	args = appendRunDraftStringFlag(args, "--ts-ver", n.TSVersion)
	args = appendRunDraftStringFlag(args, "--ts-exit", n.TSExitNode)
	args = appendRunDraftRepeatedFlag(args, "--ts-tags", n.TSTags)
	args = appendRunDraftStringFlag(args, "--ts-auth-key", n.TSAuthKey)
	args = appendRunDraftStringFlag(args, "--macvlan-mac", n.MacvlanMAC)
	args = appendRunDraftIntFlag(args, "--macvlan-vlan", n.MacvlanVLAN)
	args = appendRunDraftStringFlag(args, "--macvlan-parent", n.MacvlanParent)
	args = appendRunDraftRestartFlag(args, n.Restart)
	return appendRunDraftRepeatedFlag(args, "--publish", n.Publish)
}

func payloadArgsFromRunArgs(args []string) []string {
	_, payloadArgs := splitRunArgsForParsing(args)
	return append([]string{}, payloadArgs...)
}

func splitRunModes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		mode := strings.TrimSpace(part)
		if mode != "" {
			out = append(out, mode)
		}
	}
	return out
}

func joinRunDraftModes(modes []string) string {
	return strings.Join(splitRunModes(strings.Join(modes, ",")), ",")
}

func appendRunDraftStringFlag(args []string, name, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return args
	}
	return append(args, name+"="+value)
}

func appendRunDraftIntFlag(args []string, name string, value int) []string {
	if value == 0 {
		return args
	}
	return append(args, fmt.Sprintf("%s=%d", name, value))
}

func appendRunDraftVMArgs(args []string, vm RunDraftVM) []string {
	if vm.CPUs != 0 {
		args = append(args, fmt.Sprintf("--cpus=%d", vm.CPUs))
	}
	args = appendRunDraftStringFlag(args, "--memory", vm.Memory)
	return appendRunDraftStringFlag(args, "--disk", vm.Disk)
}

func appendRunDraftRepeatedFlag(args []string, name string, values []string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			args = append(args, name+"="+value)
		}
	}
	return args
}

func appendRunDraftRestartFlag(args []string, restart *bool) []string {
	if restart == nil || *restart {
		return args
	}
	return append(args, "--restart=false")
}

func runDraftVMFromRunFlags(flags cli.RunFlags) RunDraftVM {
	return RunDraftVM{
		CPUs:   flags.CPUs,
		Memory: strings.TrimSpace(flags.Memory),
		Disk:   strings.TrimSpace(flags.Disk),
	}
}

func runDraftNetworkFromRunFlags(flags cli.RunFlags) RunDraftNetwork {
	return RunDraftNetwork{
		Modes:         splitRunModes(flags.Net),
		TSVersion:     flags.TsVer,
		TSExitNode:    flags.TsExit,
		TSTags:        append([]string{}, flags.TsTags...),
		TSAuthKey:     flags.TsAuthKey,
		MacvlanMAC:    flags.MacvlanMac,
		MacvlanVLAN:   flags.MacvlanVlan,
		MacvlanParent: flags.MacvlanParent,
		Restart:       runDraftBool(flags.Restart),
		Publish:       append([]string{}, flags.Publish...),
	}
}

func runDraftSnapshotsFromOptions(opts snapshotOptions) RunDraftSnapshots {
	return RunDraftSnapshots{
		Mode:            opts.Snapshots,
		KeepLast:        opts.KeepLast,
		KeepLastInherit: opts.KeepLastInherit,
		MaxAge:          opts.MaxAge,
		MaxAgeInherit:   opts.MaxAgeInherit,
		Required:        cloneBoolPtr(opts.Required),
		RequiredInherit: opts.RequiredInherit,
		Events:          append([]string{}, opts.Events...),
		EventsInherit:   opts.EventsInherit,
	}
}

func runDraftSnapshotOptions(snapshots RunDraftSnapshots) snapshotOptions {
	return snapshotOptions{
		Snapshots:       snapshots.Mode,
		KeepLast:        snapshots.KeepLast,
		KeepLastInherit: snapshots.KeepLastInherit,
		MaxAge:          snapshots.MaxAge,
		MaxAgeInherit:   snapshots.MaxAgeInherit,
		Required:        snapshots.Required,
		RequiredInherit: snapshots.RequiredInherit,
		Events:          snapshots.Events,
		EventsInherit:   snapshots.EventsInherit,
	}
}

func runDraftBool(v bool) *bool {
	return &v
}

type runDraftExecuteOptions struct {
	Stdout      io.Writer
	Stderr      io.Writer
	ForceDeploy bool
}

func executeRunDraft(ctx context.Context, draft RunDraft, cfgLoc *projectConfigLocation, forceDeploy bool) error {
	return executeRunDraftWithOptions(ctx, draft, cfgLoc, runDraftExecuteOptions{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		ForceDeploy: forceDeploy,
	})
}

func executeRunDraftWithOptions(ctx context.Context, draft RunDraft, cfgLoc *projectConfigLocation, opts runDraftExecuteOptions) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	normalized, validation := validateRunDraft(ctx, draft, cwd)
	if !validation.OK {
		return fmt.Errorf("invalid run draft: %s", validation.Errors[0].Message)
	}
	draft = normalized

	service := strings.TrimSpace(draft.Service)
	if service == "" {
		return fmt.Errorf("service name is required")
	}

	prevService := serviceOverride
	prevHost := hostOverride
	prevHostSet := hostOverrideSet
	prevPrefs := loadedPrefs
	serviceOverride = service
	host := strings.TrimSpace(draft.Host)
	if host != "" {
		hostOverride = host
		hostOverrideSet = true
		loadedPrefs.DefaultHost = host
	}
	defer func() {
		serviceOverride = prevService
		hostOverride = prevHost
		hostOverrideSet = prevHostSet
		loadedPrefs = prevPrefs
	}()

	runArgs := draft.runArgs()
	if err := executeRunDraftOutput(ctx, opts.Stdout, draft, runArgs, opts.ForceDeploy || draft.ForceDeploy || draft.SnapshotChange); err != nil {
		return err
	}
	return saveRunDraftExecutionConfig(cfgLoc, host, draft, runArgs)
}

func executeRunDraftOutput(ctx context.Context, stdout io.Writer, draft RunDraft, runArgs []string, forceDeploy bool) error {
	if isStdoutWriter(stdout) {
		return runDraftWithChanges(ctx, draft, runArgs, forceDeploy)
	}
	return runDraftWithChangesTo(ctx, stdout, draft, runArgs, forceDeploy)
}

func saveRunDraftExecutionConfig(cfgLoc *projectConfigLocation, host string, draft RunDraft, runArgs []string) error {
	configRunArgs := runArgsWithoutSensitiveRunOptions(runArgs)
	if err := saveRunConfigWithPayloadKind(cfgLoc, host, draft.Payload, draft.PayloadKind, configRunArgs, draft.Storage.ServiceRoot, draft.Storage.ZFS); err != nil {
		return err
	}
	if draft.EnvFileSet {
		return saveEnvFileConfig(cfgLoc, host, draft.EnvFileArg)
	}
	return nil
}

func runDraftCommandPreview(draft RunDraft) string {
	parts := []string{"yeet", "run"}
	if target := runDraftCommandTarget(draft); target != "" {
		parts = append(parts, target)
	}
	if strings.TrimSpace(draft.Payload) != "" {
		parts = append(parts, draft.Payload)
	}
	parts = appendRunDraftCommandPreviewControlArgs(parts, draft)
	parts = append(parts, runArgsWithSensitiveRunOptionsHidden(draft.runArgs())...)
	return shellJoin(parts)
}

func appendRunDraftCommandPreviewControlArgs(parts []string, draft RunDraft) []string {
	parts = appendRunDraftStringFlag(parts, "--env-file", draft.EnvFile)
	if draft.ForceDeploy {
		parts = append(parts, "--force")
	}
	return parts
}

func runDraftCommandTarget(draft RunDraft) string {
	service := strings.TrimSpace(draft.Service)
	host := strings.TrimSpace(draft.Host)
	if service == "" {
		return ""
	}
	if host == "" || strings.Contains(service, "@") {
		return service
	}
	return service + "@" + host
}

func runArgsWithoutSensitiveRunOptions(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			return out
		}
		if arg == "--ts-auth-key" {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--ts-auth-key=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func runArgsWithSensitiveRunOptionsHidden(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			return out
		}
		if arg == "--ts-auth-key" {
			out = append(out, "--ts-auth-key=<hidden>")
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--ts-auth-key=") {
			out = append(out, "--ts-auth-key=<hidden>")
			continue
		}
		out = append(out, arg)
	}
	return out
}

func runDraftWithChanges(ctx context.Context, draft RunDraft, runArgs []string, forceDeploy bool) error {
	return runDraftWithChangesTo(ctx, os.Stdout, draft, runArgs, forceDeploy)
}

func runDraftWithChangesTo(ctx context.Context, stdout io.Writer, draft RunDraft, runArgs []string, forceDeploy bool) error {
	if stdout == nil {
		stdout = io.Discard
	}
	runner := func(ctx context.Context, payload string, args []string) error {
		return runRunContextWithOutput(ctx, stdout, payload, args)
	}
	if draft.PayloadKind == "local-image" {
		runner = func(ctx context.Context, payload string, args []string) error {
			return runLocalImagePayloadContextWithOutput(ctx, stdout, payload, args)
		}
	}
	if draft.PayloadKind == serviceTypeVM {
		runner = func(ctx context.Context, payload string, args []string) error {
			return runVMPayloadContextWithOutput(ctx, stdout, payload, args)
		}
	}
	alwaysDeploy := draft.PayloadKind == "local-image" || draft.PayloadKind == serviceTypeVM
	return runWithChangesToWithContextRunner(ctx, stdout, draft.Payload, runArgs, draft.EnvFile, draft.ExistingEntry, forceDeploy, runner, alwaysDeploy)
}

func runLocalImagePayload(payload string, args []string) error {
	return runLocalImagePayloadContext(context.Background(), payload, args)
}

func runLocalImagePayloadContext(ctx context.Context, payload string, args []string) error {
	if ok, err := tryRunDockerFn(ctx, payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("unknown local Docker image: %s", payload)
}

func runLocalImagePayloadContextWithOutput(ctx context.Context, stdout io.Writer, payload string, args []string) error {
	if isStdoutWriter(stdout) {
		return runLocalImagePayloadContext(ctx, payload, args)
	}
	if ok, err := tryRunDockerWithOutputFn(ctx, stdout, payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("unknown local Docker image: %s", payload)
}

func runVMPayloadContextWithOutput(ctx context.Context, stdout io.Writer, payload string, args []string) error {
	ok, err := tryRunVMPayloadWithOutputFn(ctx, stdout, payload, args)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown VM payload: %s", payload)
	}
	return nil
}
