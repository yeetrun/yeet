// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/yeet"
)

// serviceBridgeSkippedGroupCommands lists group commands whose positional args
// should not be treated as service names by service-arg bridging.
var serviceBridgeSkippedGroupCommands = map[string]map[string]struct{}{
	"docker": {
		"push": {},
	},
	"vm": {
		"images": {},
	},
	"service": {
		"sync": {},
	},
	"snapshots": {
		"list":      {},
		"inspect":   {},
		"create":    {},
		"clone":     {},
		"restore":   {},
		"rm":        {},
		"protect":   {},
		"unprotect": {},
		"defaults":  {},
	},
}

var serviceBridgeSkippedRemoteCommands = map[string]struct{}{
	"status": {},
}

var serviceBridgeHostLevelGroupCommands = map[string]map[string]struct{}{
	"host": {
		"set":     {},
		"cleanup": {},
	},
	"vm": {
		"memory": {},
	},
}

func findServiceIndex(args []string, start int, flags map[string]cli.FlagSpec) int {
	for i := start; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return serviceIndexAfterTerminator(args, i)
		}
		if skip, ok := flagTokenSkip(arg, flags); ok {
			i += skip
			continue
		}
		return i
	}
	return -1
}

func serviceIndexAfterTerminator(args []string, idx int) int {
	if idx+1 < len(args) {
		return idx + 1
	}
	return -1
}

func flagTokenSkip(arg string, flags map[string]cli.FlagSpec) (int, bool) {
	if strings.HasPrefix(arg, "--") && len(arg) > 2 {
		return longFlagSkip(arg, flags), true
	}
	if strings.HasPrefix(arg, "-") && arg != "-" {
		return shortFlagSkip(arg, flags), true
	}
	return 0, false
}

func longFlagSkip(arg string, flags map[string]cli.FlagSpec) int {
	if strings.Contains(arg, "=") {
		return 0
	}
	if spec, ok := flags[arg]; ok && spec.ConsumesValue {
		return 1
	}
	return 0
}

func shortFlagSkip(arg string, flags map[string]cli.FlagSpec) int {
	if strings.Contains(arg, "=") {
		return 0
	}
	if len(arg) == 2 {
		if spec, ok := flags[arg]; ok && spec.ConsumesValue {
			return 1
		}
		return 0
	}
	// Handle shorthand with attached value (e.g. -n5).
	if spec, ok := flags[arg[:2]]; ok && spec.ConsumesValue {
		return 0
	}
	return 0
}

func removeArgAt(args []string, idx int) []string {
	if idx < 0 || idx >= len(args) {
		return args
	}
	out := make([]string, 0, len(args)-1)
	out = append(out, args[:idx]...)
	out = append(out, args[idx+1:]...)
	return out
}

func bridgeServiceArgs(args []string, remoteSpecs map[string]map[string]cli.FlagSpec, groupSpecs map[string]map[string]map[string]cli.FlagSpec, override string) (service string, host string, bridged []string, ok bool) {
	if len(args) == 0 {
		return "", "", nil, false
	}
	if args[0] == "copy" {
		return "", "", nil, false
	}

	if override != "" {
		return bridgeWithOverride(args, remoteSpecs, groupSpecs, override)
	}

	if isServiceBridgeSkippedRemoteCommand(args[0]) {
		return "", "", nil, false
	}

	if flags, ok := remoteSpecs[args[0]]; ok {
		return bridgeCommandArgs(args, 1, flags)
	}

	if len(args) > 1 {
		return bridgeGroupArgs(args, groupSpecs)
	}
	return "", "", nil, false
}

func bridgeWithOverride(args []string, remoteSpecs map[string]map[string]cli.FlagSpec, groupSpecs map[string]map[string]map[string]cli.FlagSpec, override string) (service string, host string, bridged []string, ok bool) {
	if _, ok := remoteSpecs[args[0]]; ok {
		return override, "", args, true
	}
	if len(args) <= 1 {
		return "", "", nil, false
	}
	if group, ok := groupSpecs[args[0]]; ok {
		if flags, ok := group[args[1]]; ok {
			if args[0] == "vm" && args[1] == "runtime" {
				return bridgeVMRuntimeArgs(args, flags, override)
			}
			return override, "", args, true
		}
	}
	return "", "", nil, false
}

func bridgeCommandArgs(args []string, start int, flags map[string]cli.FlagSpec) (service string, host string, bridged []string, ok bool) {
	idx := findServiceIndex(args, start, flags)
	if idx == -1 {
		return "", "", nil, false
	}
	service, host, _ = splitQualifiedName(args[idx])
	return service, host, removeArgAt(args, idx), true
}

func bridgeGroupArgs(args []string, groupSpecs map[string]map[string]map[string]cli.FlagSpec) (service string, host string, bridged []string, ok bool) {
	group, ok := groupSpecs[args[0]]
	if !ok || isServiceBridgeSkippedGroupCommand(args[0], args[1]) {
		return "", "", nil, false
	}
	flags, ok := group[args[1]]
	if !ok {
		return "", "", nil, false
	}
	if isServiceBridgeHostLevelGroupCommand(args[0], args[1]) {
		return "", "", append([]string{}, args...), true
	}
	if args[0] == "vm" && args[1] == "kernel" {
		return bridgeVMKernelArgs(args, flags)
	}
	if args[0] == "vm" && args[1] == "runtime" {
		return bridgeVMRuntimeArgs(args, flags, "")
	}
	if isVariadicServiceGroupCommand(args[0], args[1]) {
		return "", "", nil, false
	}
	return bridgeCommandArgs(args, 2, flags)
}

func bridgeVMRuntimeArgs(args []string, flags map[string]cli.FlagSpec, override string) (service string, host string, bridged []string, ok bool) {
	positionals := positionalArgIndices(args, 2, flags)
	if len(positionals) == 0 {
		return "", "", nil, false
	}
	direct, serviceIndex, valid := vmRuntimeBridgeTarget(args, positionals, override)
	if !valid {
		return "", "", nil, false
	}
	if direct != "" {
		return direct, "", append([]string(nil), args...), true
	}
	if serviceIndex < 0 {
		return yeet.SystemServiceName(), "", append([]string(nil), args...), true
	}
	service, host, _ = splitQualifiedName(args[serviceIndex])
	if override != "" {
		service = override
		host = ""
	}
	return service, host, removeArgAt(args, serviceIndex), true
}

func vmRuntimeBridgeTarget(args []string, positionals []int, override string) (direct string, serviceIndex int, ok bool) {
	action := args[positionals[0]]
	switch action {
	case cli.VMRuntimeActionStatus, cli.VMRuntimeActionUpgrade, cli.VMRuntimeActionRollback:
		return vmRuntimeBridgeServiceTarget(positionals, override)
	case cli.VMRuntimeActionPolicy:
		return vmRuntimePolicyBridgeTarget(args, positionals, override)
	case cli.VMRuntimeActionUpdate, cli.VMRuntimeActionImport, cli.VMRuntimeActionProtect,
		cli.VMRuntimeActionUnprotect, cli.VMRuntimeActionPrune:
		return yeet.SystemServiceName(), -1, true
	default:
		return "", -1, false
	}
}

func vmRuntimeBridgeServiceTarget(positionals []int, override string) (direct string, serviceIndex int, ok bool) {
	if len(positionals) > 1 {
		return "", positionals[1], true
	}
	if override != "" {
		return override, -1, true
	}
	return "", -1, true
}

func vmRuntimePolicyBridgeTarget(args []string, positionals []int, override string) (direct string, serviceIndex int, ok bool) {
	if len(positionals) <= 1 {
		return vmRuntimeBridgeServiceTarget(positionals, override)
	}
	value := args[positionals[1]]
	if value == "defaults" {
		return yeet.SystemServiceName(), -1, true
	}
	if override != "" && len(positionals) == 2 && isVMRuntimePolicy(value) {
		return override, -1, true
	}
	return "", positionals[1], true
}

func isVMRuntimePolicy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case cli.VMRuntimePolicyInherit, cli.VMRuntimePolicyManual, cli.VMRuntimePolicyStageOnRestart:
		return true
	default:
		return false
	}
}

func positionalArgIndices(args []string, start int, flags map[string]cli.FlagSpec) []int {
	var result []int
	for i := start; i < len(args); i++ {
		if args[i] == "--" {
			for i++; i < len(args); i++ {
				result = append(result, i)
			}
			break
		}
		if skip, ok := flagTokenSkip(args[i], flags); ok {
			i += skip
			continue
		}
		result = append(result, i)
	}
	return result
}

func bridgeVMKernelArgs(args []string, flags map[string]cli.FlagSpec) (service string, host string, bridged []string, ok bool) {
	if len(args) < 3 || args[2] != "sync" {
		return "", "", nil, false
	}
	return bridgeCommandArgs(args, 3, flags)
}

func isVariadicServiceGroupCommand(group string, command string) bool {
	reg := cli.RemoteCommandRegistry()
	groupSpec, ok := reg.Groups[group]
	if !ok {
		return false
	}
	cmdSpec, ok := groupSpec.Commands[command]
	if !ok {
		return false
	}
	arg, ok := yargs.ArgSpecAt(cmdSpec.ArgsSchema, 0)
	return ok && cli.IsServiceArgSpec(arg) && arg.Variadic
}

func isServiceBridgeSkippedGroupCommand(group string, command string) bool {
	locals, ok := serviceBridgeSkippedGroupCommands[group]
	if !ok {
		return false
	}
	_, ok = locals[command]
	return ok
}

func isServiceBridgeSkippedRemoteCommand(command string) bool {
	_, ok := serviceBridgeSkippedRemoteCommands[command]
	return ok
}

func isServiceBridgeHostLevelGroupCommand(group string, command string) bool {
	commands, ok := serviceBridgeHostLevelGroupCommands[group]
	if !ok {
		return false
	}
	_, ok = commands[command]
	return ok
}
