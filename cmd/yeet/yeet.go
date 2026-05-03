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
	bridgedArgs    []string
	handleSvcCmdFn = yeet.HandleSvcCmd
	rawArgs        []string
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

	overrides := resolveGlobalOverrides(globalFlags)
	applyRuntimeOverrides(overrides)
	route := prepareCommandRoute(remaining, overrides.service)
	applyRuntimeOverrides(route.runtimeOverrides)
	bridgedArgs = route.bridgedArgs
	args := route.args

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

	helpConfig := buildHelpConfig()
	groups := buildGroupHandlers()
	if err := yargs.RunSubcommandsWithGroups(context.Background(), args, helpConfig, globalFlagsParsed{}, handlers, groups); err != nil {
		yeet.PrintCLIError(os.Stderr, err)
	}
}

type runtimeOverrides struct {
	host    string
	service string
}

type commandRoute struct {
	runtimeOverrides
	args        []string
	bridgedArgs []string
}

func resolveGlobalOverrides(flags globalFlagsParsed) runtimeOverrides {
	overrides := runtimeOverrides{host: flags.Host}
	if flags.Service == "" {
		return overrides
	}
	service, host, ok := splitQualifiedName(flags.Service)
	if ok && host != "" {
		overrides.host = host
	}
	overrides.service = service
	return overrides
}

func prepareCommandRoute(remaining []string, service string) commandRoute {
	args := yargs.ApplyAliases(remaining, buildHelpConfig())
	args = rewriteEnvSetArgs(args)

	route := commandRoute{
		runtimeOverrides: runtimeOverrides{service: service},
		args:             args,
	}
	if host, updated, ok := splitCommandHost(args); ok {
		route.host = host
		route.args = updated
		if len(route.args) == 1 && route.args[0] == cli.CommandEvents {
			route.args = append(route.args, "--all")
		}
	}
	if len(route.args) <= 1 {
		return route
	}

	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	svc, host, bridged, ok := bridgeServiceArgs(route.args, remoteSpecs, groupSpecs, route.service)
	if !ok {
		return route
	}
	route.service = svc
	if host != "" {
		route.host = host
	}
	route.bridgedArgs = bridged
	route.args = bridged
	return route
}

func applyRuntimeOverrides(overrides runtimeOverrides) {
	if overrides.host != "" {
		yeet.SetHostOverride(overrides.host)
	}
	if overrides.service != "" {
		yeet.SetServiceOverride(overrides.service)
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
