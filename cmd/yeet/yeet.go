// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/buildinfo"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/yeet"
	"golang.org/x/term"
)

var (
	bridgedArgs                   []string
	handleHostCleanupFn           = yeet.HandleHostCleanup
	handleHostSetFn               = yeet.HandleHostSet
	handleSvcCmdFn                = yeet.HandleSvcCmd
	handleUpgradeFn               = yeet.HandleUpgrade
	handleVMSSHProxyFn            = yeet.HandleVMSSHProxy
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
	remaining = preserveConfigHostFlag(remaining, globalFlags)

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
	handlers["config"] = yeet.HandleConfig
	handlers["ssh"] = yeet.HandleSSH
	handlers["_vm-ssh-proxy"] = handleVMSSHProxyFn
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

func preserveConfigHostFlag(args []string, flags globalFlagsParsed) []string {
	host := strings.TrimSpace(flags.Host)
	if host == "" || len(args) == 0 || args[0] != "config" {
		return args
	}
	out := append([]string{}, args...)
	out = append(out, "--host", host)
	return out
}

func handleSchemaBackedHelp(args []string, config yargs.HelpConfig) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "help" && len(args) > 1 {
		target := yargs.ApplyAliases(args[1:], config)
		return printSchemaBackedHelp(target, config, false)
	}
	if hasAgentHelpFlag(args) {
		return printSchemaBackedHelp(args, config, true)
	}
	if hasHumanHelpFlag(args) {
		return printSchemaBackedHelp(args, config, false)
	}
	return false
}

func printSchemaBackedHelp(args []string, config yargs.HelpConfig, agent bool) bool {
	if len(args) == 0 {
		return false
	}
	reg, path, ok := schemaBackedHelpTarget(args, config)
	if !ok {
		return false
	}
	if agent {
		fmt.Print(yargs.GenerateAgentHelpFromRegistry(reg, path, globalFlagsParsed{}))
		return true
	}
	info, ok := reg.CommandSpec(path)
	if !ok {
		return false
	}
	if len(path) == 2 {
		help := yargs.GenerateGroupCommandHelp(config, path[0], path[1], globalFlagsParsed{})
		fmt.Print(insertGroupCommandOptions(help, info.FlagsSchema))
		return true
	}
	if len(path) != 1 {
		return false
	}
	cmd := path[0]
	flagsSchema := schemaOrEmpty(info.FlagsSchema)
	argsSchema := schemaOrEmpty(info.ArgsSchema)
	fmt.Print(stripMarkdownAliasBlock(yargs.GenerateSubCommandHelp(config, cmd, globalFlagsParsed{}, flagsSchema, argsSchema)))
	return true
}

func insertGroupCommandOptions(help string, schema any) string {
	options := humanSchemaOptions(schema)
	if options == "" {
		return help
	}
	section := "OPTIONS:\n" + options +
		fmt.Sprintf("%-30s %s\n", "    -h, --help", "Show this help message") +
		fmt.Sprintf("%-30s %s\n\n", "        --help-agent", "Show agent-readable CLI context")
	for _, marker := range []string{"GLOBAL OPTIONS:\n", "EXAMPLES:\n"} {
		if idx := strings.Index(help, marker); idx >= 0 {
			return help[:idx] + section + help[idx:]
		}
	}
	return help + section
}

func humanSchemaOptions(schema any) string {
	if schema == nil {
		return ""
	}
	typ := reflect.TypeOf(schema)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return ""
	}

	var options strings.Builder
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Tag.Get("flag")
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		label := "--" + name + " " + field.Type.String()
		if short := field.Tag.Get("short"); short != "" {
			label = "-" + short + ", " + label
		}
		fmt.Fprintf(&options, "    %-25s %s\n", label, field.Tag.Get("help"))
	}
	return options.String()
}

func schemaBackedHelpTarget(args []string, config yargs.HelpConfig) (yargs.Registry, []string, bool) {
	reg := cli.RemoteCommandRegistry()
	reg.Command = config.Command
	reg = withAgentUsage(reg)
	resolved, ok, err := yargs.ResolveCommandWithRegistry(args, reg)
	if err != nil || !ok {
		return reg, nil, false
	}
	spec, ok := reg.CommandSpec(resolved.Path)
	if !ok || (spec.FlagsSchema == nil && spec.ArgsSchema == nil) {
		return reg, nil, false
	}
	return reg, resolved.Path, true
}

func withAgentUsage(reg yargs.Registry) yargs.Registry {
	for name, spec := range reg.SubCommands {
		spec.Info.Usage = schemaAwareAgentUsage([]string{name}, spec.Info.Usage, spec.ArgsSchema)
		reg.SubCommands[name] = spec
	}
	for groupName, group := range reg.Groups {
		for cmdName, spec := range group.Commands {
			spec.Info.Usage = schemaAwareAgentUsage([]string{groupName, cmdName}, spec.Info.Usage, spec.ArgsSchema)
			group.Commands[cmdName] = spec
		}
		reg.Groups[groupName] = group
	}
	return reg
}

func schemaAwareAgentUsage(path []string, usage string, argsSchema any) string {
	usage = strings.TrimSpace(usage)
	pathText := strings.Join(path, " ")
	if usage == "" {
		return strings.TrimSpace(pathText + " " + agentArgUsageSuffix(argsSchema))
	}
	if usageStartsWithCommandPath(usage, path) {
		return usage
	}
	if usageStartsWithSchemaArg(usage, argsSchema) {
		return pathText + " " + usage
	}
	return strings.TrimSpace(pathText + " " + agentArgUsageSuffix(argsSchema) + " " + usage)
}

func usageStartsWithCommandPath(usage string, path []string) bool {
	if len(path) == 0 {
		return false
	}
	pathText := strings.Join(path, " ")
	if usage == pathText || strings.HasPrefix(usage, pathText+" ") {
		return true
	}
	last := path[len(path)-1]
	return usage == last || strings.HasPrefix(usage, last+" ")
}

func usageStartsWithSchemaArg(usage string, argsSchema any) bool {
	specs := yargs.ExtractArgSpecs(argsSchema)
	if len(specs) == 0 {
		return false
	}
	token := usage
	if idx := strings.IndexAny(token, " \t"); idx >= 0 {
		token = token[:idx]
	}
	token = strings.Trim(token, "<>[].")
	first := strings.ToUpper(specs[0].Name)
	switch first {
	case "SERVICE":
		return strings.EqualFold(token, "service") || strings.EqualFold(token, "svc")
	default:
		return strings.EqualFold(token, first)
	}
}

func agentArgUsageSuffix(argsSchema any) string {
	specs := yargs.ExtractArgSpecs(argsSchema)
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		parts = append(parts, agentArgUsage(spec))
	}
	return strings.Join(parts, " ")
}

func agentArgUsage(spec yargs.ArgSpec) string {
	name := strings.ToUpper(spec.Name)
	if spec.Variadic {
		if spec.MinCount > 0 {
			return fmt.Sprintf("<%s...>", name)
		}
		return fmt.Sprintf("[%s...]", name)
	}
	if spec.Required {
		return fmt.Sprintf("<%s>", name)
	}
	return fmt.Sprintf("[%s]", name)
}

func schemaOrEmpty(schema any) any {
	if schema != nil {
		return schema
	}
	return struct{}{}
}

func hasHumanHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func hasAgentHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help-agent" {
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
