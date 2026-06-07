// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/buildinfo"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/yeet"
	"golang.org/x/term"
)

var (
	bridgedArgs                   []string
	handleSvcCmdFn                = yeet.HandleSvcCmd
	handleUpgradeFn               = yeet.HandleUpgrade
	isTerminalFn                  = term.IsTerminal
	maybePrintUpdateAdvisoryFn    = yeet.MaybePrintUpdateAdvisory
	projectHostCountForAdvisoryFn = yeet.ProjectHostCountForAdvisory
	rawArgs                       []string
)

func main() {
	os.Exit(run())
}

func run() int {
	// Keep buildinfo linked into yeet so release ldflags can stamp the binary.
	_ = buildinfo.Version()
	rawArgs = os.Args[1:]
	globalFlags, remaining, err := parseGlobalFlags(rawArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := applyGlobalUIFlags(globalFlags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	overrides := resolveGlobalOverrides(globalFlags)
	applyRuntimeOverrides(overrides)
	route := prepareCommandRoute(remaining, overrides.service)
	applyRuntimeOverrides(route.runtimeOverrides)
	bridgedArgs = route.bridgedArgs
	args := route.args

	helpConfig := buildHelpConfig()
	if handleSchemaBackedHelp(args, helpConfig) {
		return 0
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
	handlers["upgrade"] = handleUpgradeFn

	groups := buildGroupHandlers()
	exitCode := 0
	if err := yargs.RunSubcommandsWithGroups(context.Background(), args, helpConfig, globalFlagsParsed{}, handlers, groups); err != nil {
		yeet.PrintCLIError(os.Stderr, err)
		exitCode = 1
	}
	maybePrintUpdateAdvisoryFn(
		os.Stderr,
		args,
		exitCode,
		isTerminalFn(int(os.Stdout.Fd())),
		isTerminalFn(int(os.Stderr.Fd())),
		projectHostCountForAdvisoryFn(),
	)
	return exitCode
}

func handleSchemaBackedHelp(args []string, config yargs.HelpConfig) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "help" && len(args) > 1 {
		target := yargs.ApplyAliases(args[1:], config)
		return printSchemaBackedHelp(target, config, false)
	}
	if hasLLMHelpFlag(args) {
		return printSchemaBackedHelp(args, config, true)
	}
	if hasHumanHelpFlag(args) {
		return printSchemaBackedHelp(args, config, false)
	}
	return false
}

func printSchemaBackedHelp(args []string, config yargs.HelpConfig, llm bool) bool {
	if len(args) == 0 {
		return false
	}
	cmd := args[0]
	info, ok := cli.RemoteCommandInfo(cmd)
	if !ok || (info.FlagsSchema == nil && info.ArgsSchema == nil) {
		return false
	}
	flagsSchema := info.FlagsSchema
	if flagsSchema == nil {
		flagsSchema = struct{}{}
	}
	argsSchema := info.ArgsSchema
	if argsSchema == nil {
		argsSchema = struct{}{}
	}
	if llm {
		fmt.Print(yargs.GenerateSubCommandHelpLLM(config, cmd, globalFlagsParsed{}, flagsSchema, argsSchema))
		return true
	}
	fmt.Print(stripMarkdownAliasBlock(yargs.GenerateSubCommandHelp(config, cmd, globalFlagsParsed{}, flagsSchema, argsSchema)))
	return true
}

func hasHumanHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func hasLLMHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help-llm" {
			return true
		}
	}
	return false
}

func stripMarkdownAliasBlock(text string) string {
	lines := strings.SplitAfter(text, "\n")
	out := make([]string, 0, len(lines))
	skipBlank := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "**Aliases**:") {
			skipBlank = true
			continue
		}
		if skipBlank && strings.TrimSpace(line) == "" {
			skipBlank = false
			continue
		}
		skipBlank = false
		out = append(out, line)
	}
	return strings.Join(out, "")
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

func handleVMGroup(ctx context.Context, args []string) error {
	full := append([]string{"vm"}, args...)
	return handleRemote(ctx, full)
}

func handleEnvGroup(ctx context.Context, args []string) error {
	full := append([]string{"env"}, args...)
	return handleRemote(ctx, full)
}

func handleServiceGroup(ctx context.Context, args []string) error {
	full := append([]string{"service"}, args...)
	return handleRemote(ctx, full)
}

func handleSnapshotsGroup(ctx context.Context, args []string) error {
	full := append([]string{"snapshots"}, args...)
	yeet.SetServiceOverride(yeet.SystemServiceName())
	return handleRemote(ctx, full)
}

func handleMountSys(ctx context.Context, _ []string) error {
	return yeet.HandleMountSys(ctx, rawArgs)
}
