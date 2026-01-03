// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"

	"github.com/shayne/yeet/pkg/cli"
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
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}
		if strings.HasPrefix(arg, "--") && len(arg) > 2 {
			if strings.Contains(arg, "=") {
				continue
			}
			if spec, ok := flags[arg]; ok {
				if spec.ConsumesValue {
					i++
				}
				continue
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			if strings.Contains(arg, "=") {
				continue
			}
			if len(arg) == 2 {
				if spec, ok := flags[arg]; ok {
					if spec.ConsumesValue {
						i++
					}
				}
				continue
			}
			// Handle shorthand with attached value (e.g. -n5).
			if spec, ok := flags[arg[:2]]; ok && spec.ConsumesValue {
				continue
			}
			continue
		}
		return i
	}
	return -1
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
	bridged = args

	if override != "" {
		if _, ok := remoteSpecs[args[0]]; ok {
			return override, "", bridged, true
		}
		if len(args) > 1 {
			if group, ok := groupSpecs[args[0]]; ok {
				if _, ok := group[args[1]]; ok {
					return override, "", bridged, true
				}
			}
		}
		return "", "", nil, false
	}

	if flags, ok := remoteSpecs[args[0]]; ok {
		if idx := findServiceIndex(args, 1, flags); idx != -1 {
			service, host, _ = splitQualifiedName(args[idx])
			bridged = removeArgAt(args, idx)
			return service, host, bridged, true
		}
		return "", "", nil, false
	}

	if len(args) > 1 {
		if group, ok := groupSpecs[args[0]]; ok {
			if locals, ok := localGroupCommands[args[0]]; ok {
				if _, ok := locals[args[1]]; ok {
					return "", "", nil, false
				}
			}
			if flags, ok := group[args[1]]; ok {
				if idx := findServiceIndex(args, 2, flags); idx != -1 {
					service, host, _ = splitQualifiedName(args[idx])
					bridged = removeArgAt(args, idx)
					return service, host, bridged, true
				}
				return "", "", nil, false
			}
		}
	}
	return "", "", nil, false
}
