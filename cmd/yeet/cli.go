// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/yeet"
)

type globalFlagsParsed struct {
	Host     string `flag:"host" help:"Override target host (CATCH_HOST)"`
	Service  string `flag:"service" help:"Force the service name for the command"`
	TTY      bool   `flag:"tty" help:"Force TTY for remote commands"`
	NoTTY    bool   `flag:"no-tty" help:"Disable TTY for remote commands"`
	Progress string `flag:"progress" help:"Progress output (auto|tty|plain|quiet)"`
}

func parseGlobalFlags(args []string) (globalFlagsParsed, []string, error) {
	result, err := yargs.ParseKnownFlags[globalFlagsParsed](args, yargs.KnownFlagsOptions{})
	if err != nil {
		return globalFlagsParsed{}, nil, err
	}
	return result.Flags, result.RemainingArgs, nil
}

func applyGlobalUIFlags(flags globalFlagsParsed) error {
	if flags.TTY && flags.NoTTY {
		return fmt.Errorf("cannot use --tty and --no-tty together")
	}
	cfg := yeet.UIConfig{Progress: catchrpc.ProgressAuto}
	if flags.TTY {
		cfg.TTYOverride = boolPtr(true)
	}
	if flags.NoTTY {
		cfg.TTYOverride = boolPtr(false)
	}
	if flags.Progress != "" {
		mode, err := yeet.ParseProgressMode(flags.Progress)
		if err != nil {
			return err
		}
		cfg.Progress = mode
	}
	yeet.SetUIConfig(cfg)
	return nil
}

func rewriteEnvSetArgs(args []string) []string {
	if len(args) < 3 {
		return args
	}
	if args[0] != "env" {
		return args
	}
	switch args[1] {
	case "show", "edit", "copy", "set":
		return args
	}
	if !strings.Contains(args[2], "=") {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, "env", "set", args[1])
	out = append(out, args[2:]...)
	return out
}

func buildGroupHandlers() map[string]yargs.Group {
	return map[string]yargs.Group{
		"docker": {
			Description: "Docker compose and registry management",
			Commands: map[string]yargs.SubcommandHandler{
				"pull":     handleDockerGroup,
				"update":   handleDockerGroup,
				"outdated": handleDockerGroup,
				"push":     yeet.HandlePush,
			},
		},
		"vm": {
			Description: "Manage VM-specific commands",
			Commands: map[string]yargs.SubcommandHandler{
				"console": handleVMGroup,
				"set":     handleVMGroup,
				"memory":  handleVMGroup,
				"images":  handleVMGroup,
				"kernel":  handleVMGroup,
			},
		},
		"env": {
			Description: "Manage service environment files",
			Commands: map[string]yargs.SubcommandHandler{
				"show": handleEnvGroup,
				"edit": handleEnvGroup,
				"copy": handleEnvGroup,
				"set":  handleEnvGroup,
			},
		},
		"host": {
			Description: "Manage catch host settings",
			Commands: map[string]yargs.SubcommandHandler{
				"set": handleHostSetFn,
			},
		},
		"service": {
			Description: "Manage service settings",
			Commands: map[string]yargs.SubcommandHandler{
				"set":         handleServiceGroup,
				"rollback":    handleServiceGroup,
				"generations": handleServiceGroup,
				"sync":        handleServiceGroup,
			},
		},
		"snapshots": {
			Description: "Manage service recovery points and snapshot defaults",
			Commands: map[string]yargs.SubcommandHandler{
				"list":      handleSnapshotsGroup,
				"inspect":   handleSnapshotsGroup,
				"create":    handleSnapshotsGroup,
				"clone":     handleSnapshotsGroup,
				"restore":   handleSnapshotsGroup,
				"rm":        handleSnapshotsGroup,
				"protect":   handleSnapshotsGroup,
				"unprotect": handleSnapshotsGroup,
				"defaults":  handleSnapshotsGroup,
			},
		},
	}
}

func buildHelpConfig() yargs.HelpConfig {
	subcommands := make(map[string]yargs.SubCommandInfo)
	for name, info := range cli.RemoteCommandInfos() {
		subcommands[name] = yargs.SubCommandInfo{
			Name:        name,
			Description: info.Description,
			Usage:       info.Usage,
			Examples:    info.Examples,
			Hidden:      info.Hidden,
			Aliases:     info.Aliases,
		}
	}
	subcommands["init"] = yargs.SubCommandInfo{
		Name:        "init",
		Description: "Install catch on a remote host (prompts for Tailscale OAuth setup when needed)",
		Usage:       "[--from-github] [--nightly] [--install-docker] [--install-vm-tools] [--data-dir=PATH_OR_DATASET] [--services-root=PATH_OR_DATASET] [--zfs] [--ts-client-secret=<secret>] [--ts-auth-key=<key>] [ROOT@MACHINE-HOST]",
		Examples: []string{
			"yeet init root@<machine-host>",
			"yeet init --data-dir=/srv/yeet-data root@<machine-host>",
			"yeet init --zfs --data-dir=flash/yeet/data --services-root=flash/yeet/services root@<machine-host>",
			"yeet init --ts-client-secret=<secret> root@<machine-host>",
			"yeet init --install-docker root@<machine-host>",
			"yeet init --install-docker --install-vm-tools root@<machine-host>",
			"yeet init --ts-auth-key=<key> root@<machine-host>",
			"yeet init",
		},
	}
	subcommands["list-hosts"] = yargs.SubCommandInfo{
		Name:        "list-hosts",
		Description: "List tailnet hosts with the given tags; requires a local Tailscale client",
		Usage:       "[--tags=tag:catch]",
	}
	subcommands["prefs"] = yargs.SubCommandInfo{
		Name:        "prefs",
		Description: "Manage the current preferences",
	}
	subcommands["ssh"] = yargs.SubCommandInfo{
		Name:        "ssh",
		Description: "Open a catch host shell, a service shell, or a VM guest shell",
		Usage:       "[--force-proxy] [<svc>] [-- <remote-cmd...>]",
		Examples: []string{
			"yeet ssh",
			"yeet --host=<host> ssh",
			"yeet ssh <svc>",
			"yeet ssh --force-proxy <vm>",
			"yeet ssh -- uname -a",
			"yeet ssh <svc> -- ls -la",
		},
	}
	subcommands["upgrade"] = yargs.SubCommandInfo{
		Name:        "upgrade",
		Description: "Check for and install yeet/catch updates",
		Usage:       "[check] [--host=catch-a] [--json] [--yes] [--force] [--version=vX.Y.Z]",
		Examples: []string{
			"yeet upgrade check",
			"yeet upgrade",
			"yeet upgrade --host=catch-a",
			"yeet upgrade --force",
			"yeet upgrade --version v0.6.1 --force",
		},
	}
	subcommands["skirt"] = yargs.SubCommandInfo{
		Name:   "skirt",
		Hidden: true,
	}
	subcommands["_vm-ssh-proxy"] = yargs.SubCommandInfo{
		Name:   "_vm-ssh-proxy",
		Hidden: true,
	}
	groups := make(map[string]yargs.GroupInfo)
	for name, info := range cli.RemoteGroupInfos() {
		commands := make(map[string]yargs.SubCommandInfo)
		for sub, cmd := range info.Commands {
			commands[sub] = yargs.SubCommandInfo{
				Name:        cmd.Name,
				Description: cmd.Description,
				Usage:       cmd.Usage,
				Examples:    cmd.Examples,
				Hidden:      cmd.Hidden,
				Aliases:     cmd.Aliases,
			}
		}
		groups[name] = yargs.GroupInfo{
			Name:        info.Name,
			Description: info.Description,
			Commands:    commands,
			Hidden:      info.Hidden,
		}
	}
	if docker, ok := groups["docker"]; ok {
		docker.Commands["push"] = yargs.SubCommandInfo{
			Name:        "push",
			Description: "Push a container image to the remote host (optionally run it)",
			Usage:       "docker push SVC IMAGE [--run] [--all-local]",
			Examples:    []string{"yeet docker push <svc> <local-image>:<tag> --run"},
		}
		groups["docker"] = docker
	}
	if service, ok := groups["service"]; ok {
		for _, name := range []string{"rollback", "generations"} {
			cmd := service.Commands[name]
			cmd.Description = fmt.Sprintf("%s - %s", cmd.Usage, cmd.Description)
			service.Commands[name] = cmd
		}
		groups["service"] = service
	}
	return yargs.HelpConfig{
		Command: yargs.CommandInfo{
			Name:        "yeet",
			Description: "Deploy/manage services on a remote catch host; commands go over RPC.",
			Examples: []string{
				"yeet status",
				"yeet status <svc>",
				"yeet status <svc-a> <svc-b>",
				"yeet run <svc> ./bin/<svc> -- --app-flag value",
				"yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app",
			},
		},
		SubCommands: subcommands,
		Groups:      groups,
	}
}

func boolPtr(v bool) *bool {
	return &v
}

type listHostsFlags struct {
	Tags []string
}

type listHostsFlagsParsed struct {
	Tags []string `flag:"tags"`
}

func parseListHostsFlags(args []string) (listHostsFlags, error) {
	flags := listHostsFlags{Tags: []string{"tag:catch"}}
	if len(args) == 0 {
		return flags, nil
	}
	if args[0] == "list-hosts" {
		args = args[1:]
	}
	result, err := yargs.ParseKnownFlags[listHostsFlagsParsed](args, yargs.KnownFlagsOptions{SplitCommaSlices: true})
	if err != nil {
		return flags, err
	}
	if len(result.Flags.Tags) > 0 {
		flags.Tags = result.Flags.Tags
	}
	return flags, nil
}

func handleListHosts(ctx context.Context, args []string) error {
	flags, err := parseListHostsFlags(args)
	if err != nil {
		return err
	}
	return yeet.HandleListHosts(ctx, flags.Tags)
}
