// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
)

func splitQualifiedName(value string) (string, string, bool) {
	idx := strings.LastIndex(value, "@")
	if idx <= 0 || idx >= len(value)-1 {
		return value, "", false
	}
	return value[:idx], value[idx+1:], true
}

func splitCommandHost(args []string) (string, []string, bool) {
	if len(args) == 0 {
		return "", args, false
	}
	cmd, host, ok := splitQualifiedName(args[0])
	if !ok {
		return "", args, false
	}
	if _, ok := cli.RemoteCommandInfos()[cmd]; ok {
		args[0] = cmd
		return host, args, true
	}
	if _, ok := cli.RemoteGroupInfos()[cmd]; ok {
		args[0] = cmd
		return host, args, true
	}
	return "", args, false
}
