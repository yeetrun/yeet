// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func HandleSSH(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "ssh" {
		args = args[1:]
	}
	host := Host()
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("no host configured")
	}

	var info serverInfo
	if err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info); err != nil {
		return err
	}

	user := strings.TrimSpace(info.InstallUser)
	target := host
	if user != "" {
		target = fmt.Sprintf("%s@%s", user, host)
	}

	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh CLI not found in PATH")
	}

	options, commandTokens := splitSSHArgs(args)
	sshArgs := append(append([]string{}, options...), target)
	sshArgs = append(sshArgs, commandTokens...)
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func splitSSHArgs(args []string) (options []string, command []string) {
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			return options, args[i+1:]
		}
		if token == "-" || !strings.HasPrefix(token, "-") {
			return options, args[i:]
		}
		options = append(options, token)
		if sshOptionNeedsArg(token) && len(token) == 2 && i+1 < len(args) {
			options = append(options, args[i+1])
			i++
		}
	}
	return options, nil
}

func sshOptionNeedsArg(token string) bool {
	if len(token) < 2 || token[0] != '-' || token[1] == '-' {
		return false
	}
	switch token[1] {
	case 'B', 'b', 'c', 'D', 'E', 'F', 'I', 'i', 'J', 'L', 'l', 'm', 'O', 'o', 'p', 'Q', 'R', 'S', 'W', 'w':
		return true
	default:
		return false
	}
}
