// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
)

// localGroupCommands lists group commands handled locally (not bridged to catch).
// Keep this in sync with cmd/yeet/cli.go group handlers and
// pkg/cli/cli.go remoteGroupInfos/remoteGroupFlagSpecs.
var localGroupCommands = map[string]map[string]struct{}{
	"docker": {
		"push": {},
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
		if _, ok := group[args[1]]; ok {
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
	if !ok || isLocalGroupCommand(args[0], args[1]) {
		return "", "", nil, false
	}
	flags, ok := group[args[1]]
	if !ok {
		return "", "", nil, false
	}
	return bridgeCommandArgs(args, 2, flags)
}

func isLocalGroupCommand(group string, command string) bool {
	locals, ok := localGroupCommands[group]
	if !ok {
		return false
	}
	_, ok = locals[command]
	return ok
}
