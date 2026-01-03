// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/yeet"
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
		yeet.SetHost(globalFlags.Host)
	}
	if globalFlags.Service != "" {
		serviceOverride = globalFlags.Service
		yeet.SetServiceOverride(serviceOverride)
	}
	if globalFlags.RPCPort != 0 {
		yeet.SetRPCPort(globalFlags.RPCPort)
	}

	helpConfig := buildHelpConfig()
	args := yargs.ApplyAliases(remaining, helpConfig)
	args = rewriteEnvSetArgs(args)

	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	if len(args) > 1 {
		if svc, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, serviceOverride); ok {
			serviceOverride = svc
			yeet.SetServiceOverride(serviceOverride)
			bridgedArgs = bridged
			args = bridged
		}
	}

	handlers := make(map[string]yargs.SubcommandHandler)
	for _, name := range cli.RemoteCommandNames() {
		handlers[name] = handleRemote
	}
	handlers["mount"] = handleMountSys
	handlers["umount"] = handleMountSys
	handlers["init"] = yeet.HandleInit
	handlers["list-hosts"] = handleListHosts
	handlers["prefs"] = yeet.HandlePrefs
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

func handleDockerGroup(_ context.Context, args []string) error {
	full := append([]string{"docker"}, args...)
	return handleRemote(nil, full)
}

func handleEnvGroup(_ context.Context, args []string) error {
	full := append([]string{"env"}, args...)
	return handleRemote(nil, full)
}

func handleMountSys(ctx context.Context, _ []string) error {
	return yeet.HandleMountSys(ctx, rawArgs)
}
