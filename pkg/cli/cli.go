// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cli

import (
	"fmt"
	"path/filepath"
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
	// FlagsSchema optionally defines command flags via `flag` tags.
	FlagsSchema any
}

type GroupInfo struct {
	Name        string
	Description string
	Commands    map[string]CommandInfo
	Hidden      bool
}

type RunFlags struct {
	CPUs             int
	Memory           string
	Disk             string
	Net              string
	TsVer            string
	TsExit           string
	TsTags           []string
	TsAuthKey        string
	MacvlanMac       string
	MacvlanVlan      int
	MacvlanParent    string
	Restart          bool
	Pull             bool
	Force            bool
	Web              bool
	Publish          []string
	PublishReset     bool
	EnvFile          string
	ServiceRoot      string
	ZFS              bool
	Snapshots        string
	SnapshotKeepLast string
	SnapshotMaxAge   string
	SnapshotRequired string
	SnapshotEvents   string
	SnapshotChange   bool
}

type ServiceSetFlags struct {
	ServiceRoot      string
	ZFS              bool
	Copy             bool
	Empty            bool
	Publish          []string
	PublishReset     bool
	Snapshots        string
	SnapshotKeepLast string
	SnapshotMaxAge   string
	SnapshotRequired string
	SnapshotEvents   string
	SnapshotChange   bool
}

type ServiceSyncFlags struct {
	All    bool
	Config string
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

type RemoveFlags struct {
	Yes         bool
	CleanConfig bool
	CleanData   bool
}

type StatusFlags struct {
	Format string
}

type DockerOutdatedFlags struct {
	Format string
}

type DockerUpdateFlags struct {
	Outdated bool
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

type SnapshotDefaultsSetFlags struct {
	Enabled  string
	KeepLast string
	MaxAge   string
	Events   string
	Required string
}

type snapshotDefaultsSetFlagsParsed struct {
	Enabled  string `flag:"enabled"`
	KeepLast string `flag:"keep-last"`
	MaxAge   string `flag:"max-age"`
	Events   string `flag:"events"`
	Required string `flag:"required"`
}

type dockerPushFlagsParsed struct {
	Run      bool `flag:"run"`
	AllLocal bool `flag:"all-local"`
}

type runFlagsParsed struct {
	CPUs             int      `flag:"cpus"`
	Memory           string   `flag:"memory"`
	Disk             string   `flag:"disk"`
	Net              string   `flag:"net"`
	TsVer            string   `flag:"ts-ver"`
	TsExit           string   `flag:"ts-exit"`
	TsTags           []string `flag:"ts-tags"`
	TsAuthKey        string   `flag:"ts-auth-key"`
	MacvlanMac       string   `flag:"macvlan-mac"`
	MacvlanVlan      int      `flag:"macvlan-vlan"`
	MacvlanParent    string   `flag:"macvlan-parent"`
	Restart          bool     `flag:"restart" default:"true"`
	Pull             bool     `flag:"pull"`
	Force            bool     `flag:"force"`
	Web              bool     `flag:"web"`
	Publish          []string `flag:"publish" short:"p"`
	PublishReset     bool     `flag:"publish-reset"`
	EnvFile          string   `flag:"env-file"`
	ServiceRoot      string   `flag:"service-root"`
	ZFS              bool     `flag:"zfs"`
	Snapshots        string   `flag:"snapshots"`
	SnapshotKeepLast string   `flag:"snapshot-keep-last"`
	SnapshotMaxAge   string   `flag:"snapshot-max-age"`
	SnapshotRequired string   `flag:"snapshot-required"`
	SnapshotEvents   string   `flag:"snapshot-events"`
}

type serviceSetFlagsParsed struct {
	ServiceRoot      string   `flag:"service-root"`
	ZFS              bool     `flag:"zfs"`
	Copy             bool     `flag:"copy"`
	Empty            bool     `flag:"empty"`
	Publish          []string `flag:"publish" short:"p"`
	PublishReset     bool     `flag:"publish-reset"`
	Snapshots        string   `flag:"snapshots"`
	SnapshotKeepLast string   `flag:"snapshot-keep-last"`
	SnapshotMaxAge   string   `flag:"snapshot-max-age"`
	SnapshotRequired string   `flag:"snapshot-required"`
	SnapshotEvents   string   `flag:"snapshot-events"`
}

type serviceSyncFlagsParsed struct {
	All    bool   `flag:"all"`
	Config string `flag:"config"`
}

type removeFlagsParsed struct {
	Yes         bool `flag:"yes" short:"y" help:"Skip the removal prompt"`
	CleanConfig bool `flag:"clean-config" help:"Delete the matching yeet.toml entry without prompting"`
	CleanData   bool `flag:"clean-data" help:"Delete service data instead of preserving data/"`
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

type dockerOutdatedFlagsParsed struct {
	Format string `flag:"format" default:"table"`
}

type dockerUpdateFlagsParsed struct {
	Outdated bool `flag:"outdated"`
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

type ServiceSyncArgs struct {
	Service ServiceName `pos:"0?" help:"Service name"`
}

type DockerPushArgs struct {
	Service ServiceName `pos:"0" help:"Service name"`
	Image   string      `pos:"1" help:"Local image ref"`
}

type DockerOutdatedArgs struct {
	Service ServiceName `pos:"0?" help:"Service name"`
}

type DockerUpdateArgs struct {
	Services []ServiceName `pos:"0+" help:"Service names"`
}

type ServiceName string

func IsServiceArgSpec(spec yargs.ArgSpec) bool {
	serviceType := reflect.TypeOf(ServiceName(""))
	if spec.GoType == serviceType {
		return true
	}
	return spec.GoType == reflect.SliceOf(serviceType)
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
	"info":    {Name: "info", Description: "Show detailed info about a service, including published ports", Usage: "SVC [--format=plain|json|json-pretty]", ArgsSchema: ServiceArgs{}},
	"logs":    {Name: "logs", Description: "Show logs of a service", ArgsSchema: ServiceArgs{}},
	"mount": {Name: "mount", Description: "Mount a network filesystem on the host (global, not per-service)", Usage: "SOURCE [name] [--type=nfs] [--opts=defaults]", Examples: []string{
		"yeet mount host:/export data-share --type=nfs --opts=defaults",
		"yeet mount",
	}},
	"ip":       {Name: "ip", Description: "Show the IP addresses of a service"},
	"umount":   {Name: "umount", Description: "Unmount a host mount by name", Usage: "NAME", Examples: []string{"yeet umount data-share"}},
	"remove":   {Name: "remove", Description: "Remove a service", Aliases: []string{"rm"}, ArgsSchema: ServiceArgs{}, FlagsSchema: removeFlagsParsed{}},
	"restart":  {Name: "restart", Description: "Restart a service", ArgsSchema: ServiceArgs{}},
	"rollback": {Name: "rollback", Description: "Rollback a service", ArgsSchema: ServiceArgs{}},
	"run": {Name: "run", Description: "Install/update from a payload (binary, compose, image, Dockerfile, VM)", Usage: "SVC [PAYLOAD] [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--snapshots=on|off|inherit] [-- <payload args>] | --web [SVC] [PAYLOAD]", Examples: []string{
		"yeet run --web",
		"yeet run --web <svc>",
		"yeet run --web <svc> ./compose.yml",
		"yeet run <svc> ./bin/<svc> -- --app-flag value",
		"yeet run -p 80:80 <svc> nginx:latest",
		"yeet run --publish-reset -p 443:443 <svc> nginx:latest",
		"yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app",
		"yeet run <svc> vm://ubuntu/26.04 --net=svc",
		"yeet run <svc> ./compose.yml --service-root=tank/apps/<svc> --zfs",
		"yeet run <svc> ./compose.yml --snapshots=off",
		"yeet run --pull <svc> ./compose.yml",
		"yeet run --force <svc> ./compose.yml",
		"yeet run --env-file=prod.env <svc> ./compose.yml",
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
	"remove":    flagSpecsFromStruct(removeFlagsParsed{}),
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
			"update": {Name: "update", Description: "Pull images and recreate containers for compose services", Usage: "docker update <svc...> | docker update --outdated", ArgsSchema: DockerUpdateArgs{}, Examples: []string{
				"yeet docker update <svc>",
				"yeet docker update <svc-a> <svc-b>",
				"yeet docker update <svc-a> <svc-b>@<host>",
				"yeet docker update --outdated",
			}},
			"pull": {Name: "pull", Description: "Pull images for a compose service without restarting", Usage: "docker pull <svc>", ArgsSchema: ServiceArgs{}},
			"push": {Name: "push", Description: "Push a local image into the internal registry", Usage: "docker push <svc> <image> [--run] [--all-local]", ArgsSchema: DockerPushArgs{}},
			"outdated": {
				Name:        "outdated",
				Description: "Show Docker compose containers with upstream image updates",
				Usage:       "docker outdated [SVC] [--format=table|json|json-pretty]",
				Examples: []string{
					"yeet docker outdated",
					"yeet docker outdated <svc>",
					"yeet docker outdated --format=json",
				},
				ArgsSchema: DockerOutdatedArgs{},
			},
		},
	},
	"vm": {
		Name:        "vm",
		Description: "Manage VM-specific commands",
		Commands: map[string]CommandInfo{
			"console": {Name: "console", Description: "Stream VM serial console output", Usage: "vm console <svc>", ArgsSchema: ServiceArgs{}},
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
	"service": {
		Name:        "service",
		Description: "Manage service settings",
		Commands: map[string]CommandInfo{
			"set": {
				Name:        "set",
				Description: "Set service settings",
				Usage:       "service set <svc> [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--copy|--empty] [--snapshots=on|off|inherit] [--snapshot-keep-last=N] [--snapshot-max-age=7d] [--snapshot-events=run,docker-update] [--snapshot-required=true|false]",
				Examples: []string{
					"yeet service set <svc> -p 80:80 -p 443:443",
					"yeet service set <svc> --publish-reset -p 443:443",
					"yeet service set <svc> --publish-reset",
					"yeet service set <svc> --service-root=/srv/apps/<svc>",
					"yeet service set <svc> --service-root=tank/apps/<svc> --zfs --copy",
					"yeet service set <svc> --service-root=/srv/apps/<svc> --empty",
					"yeet service set <svc> --snapshots=off",
					"yeet service set <svc> --snapshots=on --snapshot-keep-last=5 --snapshot-max-age=7d",
				},
				ArgsSchema: ServiceArgs{},
			},
			"sync": {
				Name:        "sync",
				Description: "Sync local yeet.toml service settings from catch",
				Usage:       "service sync <svc> [--config=PATH] | service sync --all [--config=PATH]",
				Examples: []string{
					"yeet service sync <svc>",
					"yeet service sync --all",
					"yeet service sync <svc> --config ~/yeet-services/yeet.toml",
				},
				ArgsSchema: ServiceSyncArgs{},
			},
		},
	},
	"snapshots": {
		Name:        "snapshots",
		Description: "Manage catch ZFS snapshot defaults",
		Commands: map[string]CommandInfo{
			"defaults": {
				Name:        "defaults",
				Description: "Show or set catch snapshot defaults",
				Usage:       "snapshots defaults show | snapshots defaults set [--enabled=true|false] [--keep-last=N] [--max-age=7d] [--events=run,docker-update] [--required=true|false]",
				Examples: []string{
					"yeet snapshots defaults show",
					"yeet snapshots defaults set --enabled=false",
					"yeet snapshots defaults set --enabled=true --keep-last=5 --max-age=7d",
				},
			},
		},
	},
}

// Keep this aligned with remoteGroupInfos and cmd/yeet/cli_bridge.go to avoid
// accidentally bridging local-only group commands like docker push.
var remoteGroupFlagSpecs = map[string]map[string]map[string]FlagSpec{
	"docker": {
		"update":   flagSpecsFromStruct(dockerUpdateFlagsParsed{}),
		"pull":     {},
		"push":     flagSpecsFromStruct(dockerPushFlagsParsed{}),
		"outdated": flagSpecsFromStruct(dockerOutdatedFlagsParsed{}),
	},
	"vm": {
		"console": {},
	},
	"env": {
		"show": flagSpecsFromStruct(envShowFlagsParsed{}),
		"edit": {},
		"copy": {},
		"set":  {},
	},
	"service": {
		"set":  flagSpecsFromStruct(serviceSetFlagsParsed{}),
		"sync": flagSpecsFromStruct(serviceSyncFlagsParsed{}),
	},
	"snapshots": {
		"defaults": flagSpecsFromStruct(snapshotDefaultsSetFlagsParsed{}),
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

func RemoteCommandInfo(name string) (CommandInfo, bool) {
	info, ok := remoteCommandInfos[name]
	return info, ok
}

func RemoteGroupCommandInfo(groupName, commandName string) (CommandInfo, bool) {
	group, ok := remoteGroupInfos[groupName]
	if !ok {
		return CommandInfo{}, false
	}
	info, ok := group.Commands[commandName]
	return info, ok
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
	if hasMissingSnapshotMode(parseArgs) {
		return RunFlags{}, nil, fmt.Errorf("--snapshots must be on, off, or inherit")
	}
	snapshotMode, err := normalizeSnapshotMode(parsed.Flags.Snapshots)
	if err != nil {
		return RunFlags{}, nil, err
	}
	flags := RunFlags{
		CPUs:             parsed.Flags.CPUs,
		Memory:           strings.TrimSpace(parsed.Flags.Memory),
		Disk:             strings.TrimSpace(parsed.Flags.Disk),
		Net:              parsed.Flags.Net,
		TsVer:            parsed.Flags.TsVer,
		TsExit:           parsed.Flags.TsExit,
		TsTags:           parsed.Flags.TsTags,
		TsAuthKey:        parsed.Flags.TsAuthKey,
		MacvlanMac:       parsed.Flags.MacvlanMac,
		MacvlanVlan:      parsed.Flags.MacvlanVlan,
		MacvlanParent:    parsed.Flags.MacvlanParent,
		Restart:          parsed.Flags.Restart,
		Pull:             parsed.Flags.Pull,
		Force:            parsed.Flags.Force,
		Web:              parsed.Flags.Web,
		Publish:          orderedFlagValues(parseArgs, "--publish", "-p"),
		PublishReset:     parsed.Flags.PublishReset,
		EnvFile:          parsed.Flags.EnvFile,
		ServiceRoot:      parsed.Flags.ServiceRoot,
		ZFS:              parsed.Flags.ZFS,
		Snapshots:        snapshotMode,
		SnapshotKeepLast: strings.TrimSpace(parsed.Flags.SnapshotKeepLast),
		SnapshotMaxAge:   strings.TrimSpace(parsed.Flags.SnapshotMaxAge),
		SnapshotRequired: strings.TrimSpace(parsed.Flags.SnapshotRequired),
		SnapshotEvents:   strings.TrimSpace(parsed.Flags.SnapshotEvents),
		SnapshotChange:   hasAnySnapshotRunFlag(parsed.Flags),
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseServiceSet(args []string) (ServiceSetFlags, []string, error) {
	specs := remoteGroupFlagSpecs["service"]["set"]
	parseArgs, extraArgs := splitArgsForParsing(args, specs)
	parsed, err := parseFlags[serviceSetFlagsParsed](parseArgs)
	if err != nil {
		return ServiceSetFlags{}, nil, err
	}
	flags, err := serviceSetFlagsFromParsed(parsed.Flags, parseArgs)
	if err != nil {
		return ServiceSetFlags{}, nil, err
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func serviceSetFlagsFromParsed(parsed serviceSetFlagsParsed, parseArgs []string) (ServiceSetFlags, error) {
	if hasMissingSnapshotMode(parseArgs) {
		return ServiceSetFlags{}, fmt.Errorf("--snapshots must be on, off, or inherit")
	}
	snapshotMode, err := normalizeSnapshotMode(parsed.Snapshots)
	if err != nil {
		return ServiceSetFlags{}, err
	}
	flags := ServiceSetFlags{
		ServiceRoot:      strings.TrimSpace(parsed.ServiceRoot),
		ZFS:              parsed.ZFS,
		Copy:             parsed.Copy,
		Empty:            parsed.Empty,
		Publish:          orderedFlagValues(parseArgs, "--publish", "-p"),
		PublishReset:     parsed.PublishReset,
		Snapshots:        snapshotMode,
		SnapshotKeepLast: strings.TrimSpace(parsed.SnapshotKeepLast),
		SnapshotMaxAge:   strings.TrimSpace(parsed.SnapshotMaxAge),
		SnapshotRequired: strings.TrimSpace(parsed.SnapshotRequired),
		SnapshotEvents:   strings.TrimSpace(parsed.SnapshotEvents),
		SnapshotChange:   hasAnySnapshotServiceSetFlag(parsed),
	}
	if err := validateServiceSetFlags(flags); err != nil {
		return ServiceSetFlags{}, err
	}
	return flags, nil
}

func validateServiceSetFlags(flags ServiceSetFlags) error {
	if err := validateServiceSetRootFlags(flags); err != nil {
		return err
	}
	if err := validateServiceSetMigrationFlags(flags); err != nil {
		return err
	}
	return nil
}

func validateServiceSetMigrationFlags(flags ServiceSetFlags) error {
	rootChange := hasServiceSetRootChange(flags)
	if flags.Copy && flags.Empty {
		return fmt.Errorf("cannot use --copy and --empty together")
	}
	if rootChange {
		return nil
	}
	if flags.Copy {
		return fmt.Errorf("--copy requires --service-root")
	}
	if flags.Empty {
		return fmt.Errorf("--empty requires --service-root")
	}
	return nil
}

func validateServiceSetRootFlags(flags ServiceSetFlags) error {
	rootChange := hasServiceSetRootChange(flags)
	if err := validateServiceSetRootValue(flags, rootChange); err != nil {
		return err
	}
	if !serviceSetHasChange(flags, rootChange) {
		return fmt.Errorf("service set requires --service-root, snapshot settings, or published ports")
	}
	return nil
}

func hasServiceSetRootChange(flags ServiceSetFlags) bool {
	return flags.ServiceRoot != "" || flags.ZFS
}

func hasServiceSetPublishChange(flags ServiceSetFlags) bool {
	return len(flags.Publish) != 0 || flags.PublishReset
}

func validateServiceSetRootValue(flags ServiceSetFlags, rootChange bool) error {
	if !rootChange {
		return nil
	}
	if flags.ServiceRoot == "" {
		return fmt.Errorf("--service-root is required when --zfs is set")
	}
	if !flags.ZFS && !filepath.IsAbs(flags.ServiceRoot) {
		return fmt.Errorf("--service-root must be absolute unless --zfs is set")
	}
	return nil
}

func serviceSetHasChange(flags ServiceSetFlags, rootChange bool) bool {
	return rootChange || flags.SnapshotChange || hasServiceSetPublishChange(flags)
}

func normalizeSnapshotMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "on", "off", "inherit":
		return value, nil
	default:
		return "", fmt.Errorf("--snapshots must be on, off, or inherit")
	}
}

func hasAnySnapshotServiceSetFlag(f serviceSetFlagsParsed) bool {
	return strings.TrimSpace(f.Snapshots) != "" ||
		strings.TrimSpace(f.SnapshotKeepLast) != "" ||
		strings.TrimSpace(f.SnapshotMaxAge) != "" ||
		strings.TrimSpace(f.SnapshotRequired) != "" ||
		strings.TrimSpace(f.SnapshotEvents) != ""
}

func hasAnySnapshotRunFlag(f runFlagsParsed) bool {
	return strings.TrimSpace(f.Snapshots) != "" ||
		strings.TrimSpace(f.SnapshotKeepLast) != "" ||
		strings.TrimSpace(f.SnapshotMaxAge) != "" ||
		strings.TrimSpace(f.SnapshotRequired) != "" ||
		strings.TrimSpace(f.SnapshotEvents) != ""
}

func hasMissingSnapshotMode(args []string) bool {
	return hasFlagWithoutValue(args, "--snapshots")
}

func hasFlagWithoutValue(args []string, name string) bool {
	for i, arg := range args {
		flagName, hasInlineValue := splitInlineFlagValue(arg)
		if flagName != name {
			continue
		}
		if hasInlineValue {
			if strings.TrimSpace(strings.TrimPrefix(arg, name+"=")) == "" {
				return true
			}
			continue
		}
		if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || isFlagToken(args[i+1]) {
			return true
		}
	}
	return false
}

func orderedFlagValues(args []string, longName, shortName string) []string {
	var values []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == longName || arg == shortName:
			if i+1 < len(args) {
				values = append(values, args[i+1])
				i++
			}
		case strings.HasPrefix(arg, longName+"="):
			values = append(values, strings.TrimPrefix(arg, longName+"="))
		case shortName != "" && strings.HasPrefix(arg, shortName+"="):
			values = append(values, strings.TrimPrefix(arg, shortName+"="))
		}
	}
	return values
}

func isFlagToken(arg string) bool {
	return isLongFlag(arg) || isShortFlag(arg)
}

func ParseSnapshotDefaultsShow(args []string) ([]string, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("snapshots defaults show takes no arguments")
	}
	return nil, nil
}

func ParseSnapshotDefaultsSet(args []string) (SnapshotDefaultsSetFlags, []string, error) {
	parsed, err := parseFlags[snapshotDefaultsSetFlagsParsed](args)
	if err != nil {
		return SnapshotDefaultsSetFlags{}, nil, err
	}
	flags := SnapshotDefaultsSetFlags{
		Enabled:  strings.TrimSpace(parsed.Flags.Enabled),
		KeepLast: strings.TrimSpace(parsed.Flags.KeepLast),
		MaxAge:   strings.TrimSpace(parsed.Flags.MaxAge),
		Events:   strings.TrimSpace(parsed.Flags.Events),
		Required: strings.TrimSpace(parsed.Flags.Required),
	}
	if flags == (SnapshotDefaultsSetFlags{}) {
		return SnapshotDefaultsSetFlags{}, nil, fmt.Errorf("snapshots defaults set requires at least one setting")
	}
	return flags, parsed.Args, nil
}

func ParseServiceSync(args []string) (ServiceSyncFlags, []string, error) {
	specs := remoteGroupFlagSpecs["service"]["sync"]
	parseArgs, extraArgs := splitArgsForParsing(args, specs)
	parsed, err := parseFlags[serviceSyncFlagsParsed](parseArgs)
	if err != nil {
		return ServiceSyncFlags{}, nil, err
	}
	flags := ServiceSyncFlags{
		All:    parsed.Flags.All,
		Config: strings.TrimSpace(parsed.Flags.Config),
	}
	argsOut := append(parsed.Args, extraArgs...)
	if len(argsOut) == 0 {
		argsOut = nil
	}
	if flags.All && len(argsOut) > 0 {
		return ServiceSyncFlags{}, nil, fmt.Errorf("--all cannot be combined with a service name")
	}
	if !flags.All && len(argsOut) == 0 {
		return ServiceSyncFlags{}, nil, fmt.Errorf("service sync requires a service name or --all")
	}
	if len(argsOut) > 1 {
		return ServiceSyncFlags{}, nil, fmt.Errorf("service sync accepts one service name")
	}
	return flags, argsOut, nil
}

func ParseRemove(args []string) (RemoveFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[removeFlagsParsed](parseArgs)
	if err != nil {
		return RemoveFlags{}, nil, err
	}
	flags := RemoveFlags{
		Yes:         parsed.Flags.Yes,
		CleanConfig: parsed.Flags.CleanConfig,
		CleanData:   parsed.Flags.CleanData,
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

func ParseDockerOutdated(args []string) (DockerOutdatedFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[dockerOutdatedFlagsParsed](parseArgs)
	if err != nil {
		return DockerOutdatedFlags{}, nil, err
	}
	flags := DockerOutdatedFlags{Format: parsed.Flags.Format}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}

func ParseDockerUpdate(args []string) (DockerUpdateFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[dockerUpdateFlagsParsed](parseArgs)
	if err != nil {
		return DockerUpdateFlags{}, nil, err
	}
	flags := DockerUpdateFlags{Outdated: parsed.Flags.Outdated}
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
			return splitAtArgDelimiter(args, i)
		}
		next, ok := nextParseArgIndex(args, i, specs)
		if !ok {
			return args[:i], args[i:]
		}
		i = next
	}
	return args, nil
}

func splitAtArgDelimiter(args []string, i int) ([]string, []string) {
	if i+1 < len(args) {
		return args[:i], args[i+1:]
	}
	return args[:i], nil
}

func nextParseArgIndex(args []string, i int, specs map[string]FlagSpec) (int, bool) {
	arg := args[i]
	switch {
	case isLongFlag(arg):
		return nextLongFlagIndex(arg, i, specs)
	case isShortFlag(arg):
		return nextShortFlagIndex(arg, i, specs)
	default:
		return i, true
	}
}

func isLongFlag(arg string) bool {
	return strings.HasPrefix(arg, "--") && len(arg) > 2
}

func isShortFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") && arg != "-" && !isLongFlag(arg)
}

func nextLongFlagIndex(arg string, i int, specs map[string]FlagSpec) (int, bool) {
	name, hasInlineValue := splitInlineFlagValue(arg)
	spec, ok := specs[name]
	if !ok {
		return i, false
	}
	if spec.ConsumesValue && !hasInlineValue {
		return i + 1, true
	}
	return i, true
}

func nextShortFlagIndex(arg string, i int, specs map[string]FlagSpec) (int, bool) {
	if name, hasInlineValue := splitInlineFlagValue(arg); hasInlineValue {
		_, ok := specs[name]
		return i, ok
	}
	if len(arg) == 2 {
		spec, ok := specs[arg]
		if !ok {
			return i, false
		}
		if spec.ConsumesValue {
			return i + 1, true
		}
		return i, true
	}
	_, ok := specs["-"+string(arg[1])]
	return i, ok
}

func splitInlineFlagValue(arg string) (name string, hasInlineValue bool) {
	idx := strings.Index(arg, "=")
	if idx == -1 {
		return arg, false
	}
	return arg[:idx], true
}

func flagSpecsFromStruct(v any) map[string]FlagSpec {
	specs := make(map[string]FlagSpec)
	t := indirectType(reflect.TypeOf(v))
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
	t = indirectType(t)
	switch t.Kind() {
	case reflect.Bool:
		return false
	default:
		return true
	}
}

func indirectType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func RequireArgsAtLeast(subcmd string, args []string, count int) error {
	if len(args) < count {
		return fmt.Errorf("'%s' requires at least %d argument(s), got %d", subcmd, count, len(args))
	}
	return nil
}
