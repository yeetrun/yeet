// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cli

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/shayne/yargs"
)

type FlagSpec struct {
	ConsumesValue bool
}

type CommandInfo struct {
	Name        string
	Description string
	Usage       string
	Examples    []string
	Hidden      bool
	Aliases     []string
	// ArgsSchema optionally defines positional args via `pos` tags.
	ArgsSchema any
}

type GroupInfo struct {
	Name        string
	Description string
	Commands    map[string]CommandInfo
	Hidden      bool
}

type RunFlags struct {
	Net           string
	TsVer         string
	TsExit        string
	TsTags        []string
	TsAuthKey     string
	MacvlanMac    string
	MacvlanVlan   int
	MacvlanParent string
	Restart       bool
	Pull          bool
	Publish       []string
}

type StageFlags struct {
	Net           string
	TsVer         string
	TsExit        string
	TsTags        []string
	TsAuthKey     string
	MacvlanMac    string
	MacvlanVlan   int
	MacvlanParent string
	Restart       bool
	Pull          bool
	Publish       []string
}

type EditFlags struct {
	Config  bool
	TS      bool
	Restart bool
}

type LogsFlags struct {
	Follow bool
	Lines  int
}

type StatusFlags struct {
	Format string
}

type InfoFlags struct {
	Format string
}

type EventsFlags struct {
	All bool
}

type MountFlags struct {
	Type string
	Opts string
	Deps []string
}

type VersionFlags struct {
	JSON bool
}

type EnvShowFlags struct {
	Staged bool
}

type dockerPushFlagsParsed struct {
	Run      bool `flag:"run"`
	AllLocal bool `flag:"all-local"`
}

type runFlagsParsed struct {
	Net           string   `flag:"net"`
	TsVer         string   `flag:"ts-ver"`
	TsExit        string   `flag:"ts-exit"`
	TsTags        []string `flag:"ts-tags"`
	TsAuthKey     string   `flag:"ts-auth-key"`
	MacvlanMac    string   `flag:"macvlan-mac"`
	MacvlanVlan   int      `flag:"macvlan-vlan"`
	MacvlanParent string   `flag:"macvlan-parent"`
	Restart       bool     `flag:"restart" default:"true"`
	Pull          bool     `flag:"pull"`
	Publish       []string `flag:"publish" short:"p"`
}

type stageFlagsParsed struct {
	Net           string   `flag:"net"`
	TsVer         string   `flag:"ts-ver"`
	TsExit        string   `flag:"ts-exit"`
	TsTags        []string `flag:"ts-tags"`
	TsAuthKey     string   `flag:"ts-auth-key"`
	MacvlanMac    string   `flag:"macvlan-mac"`
	MacvlanVlan   int      `flag:"macvlan-vlan"`
	MacvlanParent string   `flag:"macvlan-parent"`
	Restart       bool     `flag:"restart" default:"true"`
	Pull          bool     `flag:"pull"`
	Publish       []string `flag:"publish" short:"p"`
}

type editFlagsParsed struct {
	Config  bool `flag:"config"`
	TS      bool `flag:"ts"`
	Restart bool `flag:"restart" default:"true"`
}

type logsFlagsParsed struct {
	Follow bool `flag:"follow" short:"f"`
	Lines  int  `flag:"lines" short:"n" default:"-1"`
}

type statusFlagsParsed struct {
	Format string `flag:"format" default:"table"`
}

type infoFlagsParsed struct {
	Format string `flag:"format" default:"plain"`
}

type eventsFlagsParsed struct {
	All bool `flag:"all"`
}

type mountFlagsParsed struct {
	Type string   `flag:"type" short:"t" default:"nfs"`
	Opts string   `flag:"opts" short:"o" default:"defaults"`
	Deps []string `flag:"deps"`
}

type versionFlagsParsed struct {
	JSON bool `flag:"json"`
}

type envShowFlagsParsed struct {
	Staged bool `flag:"staged"`
}

const (
	CommandEvents = "events"
)

type ServiceArgs struct {
	Service ServiceName `pos:"0" help:"Service name"`
}

type DockerPushArgs struct {
	Service ServiceName `pos:"0" help:"Service name"`
	Image   string      `pos:"1" help:"Local image ref"`
}

type ServiceName string

func IsServiceArgSpec(spec yargs.ArgSpec) bool {
	return spec.GoType == reflect.TypeOf(ServiceName(""))
}

var remoteCommandInfos = map[string]CommandInfo{
	"cron": {Name: "cron", Description: "Install a cron job from a file and 5-field expression", Usage: `SVC FILE "<cron expr>" [-- <args...>]`, Examples: []string{`yeet cron <svc> ./job.sh "0 9 * * *" -- --job-arg foo`}, ArgsSchema: ServiceArgs{}},
	"copy": {Name: "copy", Description: "Copy files between local and service data", Usage: "[-avz] <src> <dst>", Examples: []string{
		"yeet copy ./config.yml svc:data/config.yml",
		"yeet copy ./configs/ svc:data/",
		"yeet copy svc:data/configs ./configs",
	}, Aliases: []string{"cp"}},
	"disable": {Name: "disable", Description: "Disable a service", ArgsSchema: ServiceArgs{}},
	"edit":    {Name: "edit", Description: "Edit a service", ArgsSchema: ServiceArgs{}},
	"enable":  {Name: "enable", Description: "Enable a service", ArgsSchema: ServiceArgs{}},
	"events":  {Name: "events", Description: "Show events for a service"},
	"info":    {Name: "info", Description: "Show detailed info about a service", Usage: "SVC [--format=plain|json|json-pretty]", ArgsSchema: ServiceArgs{}},
	"logs":    {Name: "logs", Description: "Show logs of a service", ArgsSchema: ServiceArgs{}},
	"mount": {Name: "mount", Description: "Mount a network filesystem on the host (global, not per-service)", Usage: "SOURCE [name] [--type=nfs] [--opts=defaults]", Examples: []string{
		"yeet mount host:/export data-share --type=nfs --opts=defaults",
		"yeet mount",
	}},
	"ip":       {Name: "ip", Description: "Show the IP addresses of a service"},
	"umount":   {Name: "umount", Description: "Unmount a host mount by name", Usage: "NAME", Examples: []string{"yeet umount data-share"}},
	"remove":   {Name: "remove", Description: "Remove a service", Aliases: []string{"rm"}, ArgsSchema: ServiceArgs{}},
	"restart":  {Name: "restart", Description: "Restart a service", ArgsSchema: ServiceArgs{}},
	"rollback": {Name: "rollback", Description: "Rollback a service", ArgsSchema: ServiceArgs{}},
	"run": {Name: "run", Description: "Install/update from a payload (binary, compose, image, Dockerfile)", Usage: "SVC PAYLOAD [-- <payload args>]", Examples: []string{
		"yeet run <svc> ./bin/<svc> -- --app-flag value",
		"yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app",
		"yeet run --pull <svc> ./compose.yml",
		"yeet run <svc> ghcr.io/org/app:latest",
		"yeet run <svc> ./Dockerfile",
	}, ArgsSchema: ServiceArgs{}},
	"start": {Name: "start", Description: "Start a service", ArgsSchema: ServiceArgs{}},
	"stage": {Name: "stage", Description: "Upload a payload without applying it (use stage show/commit/clear)", Usage: "SVC PAYLOAD|show|commit|clear [-- <payload args>]", Examples: []string{
		"yeet stage <svc> ./bin/<svc>",
		"yeet stage <svc> show",
		"yeet stage <svc> commit",
		"yeet stage <svc> clear",
	}, ArgsSchema: ServiceArgs{}},
	"status": {Name: "status", Description: "Show status of a service"},
	"tailscale": {Name: "tailscale", Description: "Configure tailscale OAuth or run tailscale commands in a service netns", Usage: "--setup [--client-secret=...] | <svc> -- <tailscale args...>", Examples: []string{
		"yeet tailscale --setup",
		"yeet tailscale --setup --client-secret=tskey-client-***",
		"yeet tailscale <svc> -- serve --bg 8080",
	}, Aliases: []string{"ts"}, ArgsSchema: ServiceArgs{}},
	"stop":    {Name: "stop", Description: "Stop a service", ArgsSchema: ServiceArgs{}},
	"version": {Name: "version", Description: "Show the version of the Catch server"},
}

var remoteFlagSpecs = map[string]map[string]FlagSpec{
	"run":       flagSpecsFromStruct(runFlagsParsed{}),
	"stage":     flagSpecsFromStruct(stageFlagsParsed{}),
	"edit":      flagSpecsFromStruct(editFlagsParsed{}),
	"logs":      flagSpecsFromStruct(logsFlagsParsed{}),
	"status":    flagSpecsFromStruct(statusFlagsParsed{}),
	"info":      flagSpecsFromStruct(infoFlagsParsed{}),
	"events":    flagSpecsFromStruct(eventsFlagsParsed{}),
	"mount":     flagSpecsFromStruct(mountFlagsParsed{}),
	"version":   flagSpecsFromStruct(versionFlagsParsed{}),
	"copy":      {},
	"cron":      {},
	"disable":   {},
	"enable":    {},
	"ip":        {},
	"remove":    {},
	"restart":   {},
	"rollback":  {},
	"start":     {},
	"stop":      {},
	"tailscale": {},
	"umount":    {},
}

// Keep this in sync with cmd/yeet/yeet.go group handlers and
// cmd/yeet/cli_bridge.go localGroupCommands (local-only group commands still
// belong here for registry metadata, but must be skipped during bridging).
var remoteGroupInfos = map[string]GroupInfo{
	"docker": {
		Name:        "docker",
		Description: "Docker compose and registry management",
		Commands: map[string]CommandInfo{
			"update": {Name: "update", Description: "Pull images and recreate containers for a compose service", Usage: "docker update <svc>", ArgsSchema: ServiceArgs{}},
			"pull":   {Name: "pull", Description: "Pull images for a compose service without restarting", Usage: "docker pull <svc>", ArgsSchema: ServiceArgs{}},
			"push":   {Name: "push", Description: "Push a local image into the internal registry", Usage: "docker push <svc> <image> [--run] [--all-local]", ArgsSchema: DockerPushArgs{}},
		},
	},
	"env": {
		Name:        "env",
		Description: "Manage service environment files",
		Commands: map[string]CommandInfo{
			"show": {Name: "show", Description: "Print the current env file", Usage: "env show <svc> [--staged]", ArgsSchema: ServiceArgs{}},
			"edit": {Name: "edit", Description: "Edit the env file", Usage: "env edit <svc>", ArgsSchema: ServiceArgs{}},
			"copy": {Name: "copy", Description: "Upload an env file", Usage: "env copy <svc> <file>", Aliases: []string{"cp"}, ArgsSchema: ServiceArgs{}},
			"set":  {Name: "set", Description: "Set env keys", Usage: "env set <svc> KEY=VALUE [KEY=VALUE...]", ArgsSchema: ServiceArgs{}},
		},
	},
}

// Keep this aligned with remoteGroupInfos and cmd/yeet/cli_bridge.go to avoid
// accidentally bridging local-only group commands like docker push.
var remoteGroupFlagSpecs = map[string]map[string]map[string]FlagSpec{
	"docker": {
		"update": {},
		"pull":   {},
		"push":   flagSpecsFromStruct(dockerPushFlagsParsed{}),
	},
	"env": {
		"show": flagSpecsFromStruct(envShowFlagsParsed{}),
		"edit": {},
		"copy": {},
		"set":  {},
	},
}

func RemoteCommandNames() []string {
	names := make([]string, 0, len(remoteCommandInfos))
	for name := range remoteCommandInfos {
		names = append(names, name)
	}
	return names
}

func RemoteCommandInfos() map[string]CommandInfo {
	return remoteCommandInfos
}

func RemoteCommandRegistry() yargs.Registry {
	subcommands := make(map[string]yargs.CommandSpec, len(remoteCommandInfos))
	for name, info := range remoteCommandInfos {
		subcommands[name] = yargs.CommandSpec{
			Info:       toSubCommandInfo(name, info),
			ArgsSchema: info.ArgsSchema,
		}
	}
	groups := make(map[string]yargs.GroupSpec, len(remoteGroupInfos))
	for name, info := range remoteGroupInfos {
		cmds := make(map[string]yargs.CommandSpec, len(info.Commands))
		for cmdName, cmd := range info.Commands {
			cmds[cmdName] = yargs.CommandSpec{
				Info:       toSubCommandInfo(cmdName, cmd),
				ArgsSchema: cmd.ArgsSchema,
			}
		}
		groupInfo := yargs.GroupInfo{
			Name:        info.Name,
			Description: info.Description,
			Hidden:      info.Hidden,
		}
		groups[name] = yargs.GroupSpec{
			Info:     groupInfo,
			Commands: cmds,
		}
	}
	return yargs.Registry{
		Command:     yargs.CommandInfo{Name: "yeet"},
		SubCommands: subcommands,
		Groups:      groups,
	}
}

func RemoteFlagSpecs() map[string]map[string]FlagSpec {
	return remoteFlagSpecs
}

func RemoteGroupInfos() map[string]GroupInfo {
	return remoteGroupInfos
}

func RemoteGroupFlagSpecs() map[string]map[string]map[string]FlagSpec {
	return remoteGroupFlagSpecs
}

func toSubCommandInfo(name string, info CommandInfo) yargs.SubCommandInfo {
	return yargs.SubCommandInfo{
		Name:            name,
		Description:     info.Description,
		Usage:           info.Usage,
		Examples:        info.Examples,
		Hidden:          info.Hidden,
		Aliases:         info.Aliases,
		LLMInstructions: "",
	}
}

func ParseRun(args []string) (RunFlags, []string, error) {
	specs := remoteFlagSpecs["run"]
	parseArgs, extraArgs := splitArgsForParsing(args, specs)
	parsed, err := parseFlags[runFlagsParsed](parseArgs)
	if err != nil {
		return RunFlags{}, nil, err
	}
	flags := RunFlags{
		Net:           parsed.Flags.Net,
		TsVer:         parsed.Flags.TsVer,
		TsExit:        parsed.Flags.TsExit,
		TsTags:        parsed.Flags.TsTags,
		TsAuthKey:     parsed.Flags.TsAuthKey,
		MacvlanMac:    parsed.Flags.MacvlanMac,
		MacvlanVlan:   parsed.Flags.MacvlanVlan,
		MacvlanParent: parsed.Flags.MacvlanParent,
		Restart:       parsed.Flags.Restart,
		Pull:          parsed.Flags.Pull,
		Publish:       parsed.Flags.Publish,
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseStage(args []string) (StageFlags, string, []string, error) {
	specs := remoteFlagSpecs["stage"]
	parseArgs, extraArgs := splitArgsForParsing(args, specs)
	parsed, err := parseFlags[stageFlagsParsed](parseArgs)
	if err != nil {
		return StageFlags{}, "", nil, err
	}

	flags := StageFlags{
		Net:           parsed.Flags.Net,
		TsVer:         parsed.Flags.TsVer,
		TsExit:        parsed.Flags.TsExit,
		TsTags:        parsed.Flags.TsTags,
		TsAuthKey:     parsed.Flags.TsAuthKey,
		MacvlanMac:    parsed.Flags.MacvlanMac,
		MacvlanVlan:   parsed.Flags.MacvlanVlan,
		MacvlanParent: parsed.Flags.MacvlanParent,
		Restart:       parsed.Flags.Restart,
		Pull:          parsed.Flags.Pull,
		Publish:       parsed.Flags.Publish,
	}

	argsOut := append(parsed.Args, extraArgs...)
	subcmd := "stage"
	if len(argsOut) > 0 {
		switch argsOut[0] {
		case "show", "clear", "commit":
			subcmd = argsOut[0]
			argsOut = argsOut[1:]
		}
	}
	return flags, subcmd, argsOut, nil
}

func ParseEdit(args []string) (EditFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[editFlagsParsed](parseArgs)
	if err != nil {
		return EditFlags{}, nil, err
	}
	flags := EditFlags{
		Config:  parsed.Flags.Config,
		TS:      parsed.Flags.TS,
		Restart: parsed.Flags.Restart,
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseLogs(args []string) (LogsFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[logsFlagsParsed](parseArgs)
	if err != nil {
		return LogsFlags{}, nil, err
	}
	flags := LogsFlags{
		Follow: parsed.Flags.Follow,
		Lines:  parsed.Flags.Lines,
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseEnvShow(args []string) (EnvShowFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[envShowFlagsParsed](parseArgs)
	if err != nil {
		return EnvShowFlags{}, nil, err
	}
	flags := EnvShowFlags{
		Staged: parsed.Flags.Staged,
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseStatus(args []string) (StatusFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[statusFlagsParsed](parseArgs)
	if err != nil {
		return StatusFlags{}, nil, err
	}
	flags := StatusFlags{Format: parsed.Flags.Format}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseInfo(args []string) (InfoFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[infoFlagsParsed](parseArgs)
	if err != nil {
		return InfoFlags{}, nil, err
	}
	flags := InfoFlags{Format: parsed.Flags.Format}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseEvents(args []string) (EventsFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[eventsFlagsParsed](parseArgs)
	if err != nil {
		return EventsFlags{}, nil, err
	}
	flags := EventsFlags{All: parsed.Flags.All}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseMount(args []string) (MountFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[mountFlagsParsed](parseArgs)
	if err != nil {
		return MountFlags{}, nil, err
	}
	flags := MountFlags{
		Type: parsed.Flags.Type,
		Opts: parsed.Flags.Opts,
		Deps: parsed.Flags.Deps,
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseVersion(args []string) (VersionFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[versionFlagsParsed](parseArgs)
	if err != nil {
		return VersionFlags{}, nil, err
	}
	flags := VersionFlags{JSON: parsed.Flags.JSON}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

type parsedFlags[T any] struct {
	Flags  T
	Args   []string
	Parser *yargs.Parser
}

func parseFlags[T any](args []string) (parsedFlags[T], error) {
	result, err := yargs.ParseFlags[T](args)
	if err != nil {
		return parsedFlags[T]{}, err
	}
	argsOut := append([]string{}, result.Args...)
	if len(result.RemainingArgs) > 0 {
		argsOut = append(argsOut, result.RemainingArgs...)
	}
	return parsedFlags[T]{Flags: result.Flags, Args: argsOut, Parser: result.Parser}, nil
}

func splitArgsAtDoubleDash(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--" {
			if i+1 < len(args) {
				return args[:i], args[i+1:]
			}
			return args[:i], nil
		}
	}
	return args, nil
}

func splitArgsForParsing(args []string, specs map[string]FlagSpec) ([]string, []string) {
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

func flagSpecsFromStruct(v any) map[string]FlagSpec {
	specs := make(map[string]FlagSpec)
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return specs
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Tag.Get("flag")
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		spec := FlagSpec{ConsumesValue: consumesValue(field.Type)}
		specs["--"+name] = spec
		if short := field.Tag.Get("short"); short != "" {
			specs["-"+short] = spec
		}
	}
	return specs
}

func consumesValue(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return false
	default:
		return true
	}
}

func RequireArgsAtLeast(subcmd string, args []string, count int) error {
	if len(args) < count {
		return fmt.Errorf("'%s' requires at least %d argument(s), got %d", subcmd, count, len(args))
	}
	return nil
}
