// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shayne/yargs"
	"github.com/shayne/yeet/pkg/cli"
	"github.com/shayne/yeet/pkg/yeet"
)

var (
	bridgedArgs     []string
	handleSvcCmdFn  = yeet.HandleSvcCmd
	rawArgs         []string
	serviceOverride string
)

func main() {
	rawArgs = os.Args[1:]
	globalFlags, remaining, err := parseGlobalFlags(rawArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := applyGlobalUIFlags(globalFlags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if globalFlags.Host != "" {
		yeet.SetHostOverride(globalFlags.Host)
	}
	if globalFlags.Service != "" {
		svc, host, ok := splitQualifiedName(globalFlags.Service)
		if ok && host != "" {
			yeet.SetHostOverride(host)
		}
		serviceOverride = svc
		yeet.SetServiceOverride(serviceOverride)
	}
	helpConfig := buildHelpConfig()
	args := yargs.ApplyAliases(remaining, helpConfig)
	args = rewriteEnvSetArgs(args)
	if host, updated, ok := splitCommandHost(args); ok {
		if host != "" {
			yeet.SetHostOverride(host)
		}
		args = updated
		if len(args) == 1 && args[0] == cli.CommandEvents {
			args = append(args, "--all")
		}
	}

	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	if len(args) > 1 {
		if svc, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, serviceOverride); ok {
			serviceOverride = svc
			yeet.SetServiceOverride(serviceOverride)
			if host != "" {
				yeet.SetHostOverride(host)
			}
			bridgedArgs = bridged
			args = bridged
		}
	}

	handlers := make(map[string]yargs.SubcommandHandler)
	for _, name := range cli.RemoteCommandNames() {
		handlers[name] = handleRemote
	}
	handlers["tailscale"] = yeet.HandleTailscale
	handlers["mount"] = handleMountSys
	handlers["umount"] = handleMountSys
	handlers["init"] = yeet.HandleInit
	handlers["list-hosts"] = handleListHosts
	handlers["prefs"] = yeet.HandlePrefs
	handlers["ssh"] = yeet.HandleSSH
	handlers["skirt"] = yeet.HandleSkirt

	groups := buildGroupHandlers()
	if err := yargs.RunSubcommandsWithGroups(context.Background(), args, helpConfig, globalFlagsParsed{}, handlers, groups); err != nil {
		yeet.PrintCLIError(os.Stderr, err)
	}
}

func handleRemote(_ context.Context, args []string) error {
	if len(bridgedArgs) > 0 {
		return handleSvcCmdFn(bridgedArgs)
	}
	return handleSvcCmdFn(args)
}

func handleDockerGroup(ctx context.Context, args []string) error {
	full := append([]string{"docker"}, args...)
	return handleRemote(ctx, full)
}

func handleEnvGroup(ctx context.Context, args []string) error {
	full := append([]string{"env"}, args...)
	return handleRemote(ctx, full)
}

func handleMountSys(ctx context.Context, _ []string) error {
	return yeet.HandleMountSys(ctx, rawArgs)
}
