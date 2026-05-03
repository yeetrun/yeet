// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func HandleSSH(ctx context.Context, args []string) error {
	args = trimSSHCommandName(args)
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh CLI not found in PATH")
	}

	options, service, commandTokens, err := parseSSHArgs(args)
	if err != nil {
		return err
	}
	if service == "" && serviceOverride != "" {
		service = serviceOverride
	}

	host, err := resolveSSHHost(service)
	if err != nil {
		return err
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("no host configured")
	}

	var info serverInfo
	if err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info); err != nil {
		return err
	}

	if service != "" {
		commandTokens, options, err = serviceShellCommand(ctx, host, service, info, commandTokens, options)
		if err != nil {
			return err
		}
	}

	sshArgs := buildSSHArgs(options, sshTarget(host, info), commandTokens)
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func trimSSHCommandName(args []string) []string {
	if len(args) > 0 && args[0] == "ssh" {
		return args[1:]
	}
	return args
}

func sshTarget(host string, info serverInfo) string {
	user := strings.TrimSpace(info.InstallUser)
	if user == "" {
		return host
	}
	return fmt.Sprintf("%s@%s", user, host)
}

func buildSSHArgs(options []string, target string, command []string) []string {
	sshArgs := append([]string{}, options...)
	sshArgs = append(sshArgs, target)
	return append(sshArgs, command...)
}

func parseSSHArgs(args []string) (options []string, service string, command []string, err error) {
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			return options, "", args[i+1:], nil
		}
		if token == "-" || !strings.HasPrefix(token, "-") {
			service = token
			if i+1 < len(args) {
				if args[i+1] == "--" {
					command = args[i+2:]
					return options, service, command, nil
				}
				return nil, "", nil, fmt.Errorf("ssh expects a single service name; use -- to pass a remote command")
			}
			return options, service, nil, nil
		}
		options = append(options, token)
		if sshOptionNeedsArg(token) && len(token) == 2 && i+1 < len(args) {
			options = append(options, args[i+1])
			i++
		}
	}
	return options, "", nil, nil
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

func resolveSSHHost(service string) (string, error) {
	host := Host()
	hostOverride, hostOverrideSet := HostOverride()
	if hostOverrideSet {
		host = hostOverride
	}

	if service == "" {
		return host, nil
	}

	svc, svcHost, _ := splitServiceHost(service)
	if svcHost != "" {
		return svcHost, nil
	}

	cfgLoc, err := loadProjectConfigFromCwd()
	if err != nil {
		return "", err
	}
	if !hostOverrideSet && cfgLoc != nil {
		resolved, err := resolveServiceHost(cfgLoc.Config, svc)
		if err != nil {
			return "", err
		}
		if resolved != "" {
			host = resolved
		}
	}
	return host, nil
}

func serviceShellCommand(ctx context.Context, host, service string, info serverInfo, command []string, options []string) ([]string, []string, error) {
	svc, svcHost, _ := splitServiceHost(service)
	if svcHost != "" {
		service = svc
	}
	resp, err := newRPCClient(host).ServiceInfo(ctx, service)
	if err != nil {
		return nil, nil, err
	}
	if !resp.Found {
		msg := strings.TrimSpace(resp.Message)
		if msg == "" {
			msg = fmt.Sprintf("service %q not found", service)
		}
		msg = msg + " (use `yeet ssh -- <cmd>` to run a remote command without a service)"
		return nil, nil, errors.New(msg)
	}
	serviceDir, err := serviceDataDir(service, info, resp)
	if err != nil {
		return nil, nil, err
	}
	command, options = buildServiceSSHCommand(serviceDir, command, options)
	return command, options, nil
}

func serviceDataDir(service string, info serverInfo, resp catchrpc.ServiceInfoResponse) (string, error) {
	serviceDir := strings.TrimSpace(resp.Info.Paths.Root)
	if serviceDir == "" {
		serviceDir = fallbackServiceRoot(service, info)
	}
	if serviceDir == "" {
		return "", fmt.Errorf("service %q has no remote path info", service)
	}
	return filepath.Join(serviceDir, "data"), nil
}

func fallbackServiceRoot(service string, info serverInfo) string {
	if info.ServicesDir != "" {
		return filepath.Join(info.ServicesDir, service)
	}
	if info.RootDir != "" {
		return filepath.Join(info.RootDir, "services", service)
	}
	return ""
}

func buildServiceSSHCommand(serviceDir string, command []string, options []string) ([]string, []string) {
	if len(command) == 0 {
		options = ensureTTYOption(options)
		cmd := fmt.Sprintf("cd %s && exec ${SHELL:-/bin/sh} -l", shellQuote(serviceDir))
		return []string{"sh", "-lc", shellQuote(cmd)}, options
	}
	cmd := fmt.Sprintf("cd %s && exec %s", shellQuote(serviceDir), shellJoin(command))
	return []string{"sh", "-lc", shellQuote(cmd)}, options
}

func ensureTTYOption(options []string) []string {
	for i, opt := range options {
		switch opt {
		case "-t", "-tt", "-T":
			return options
		case "-o":
			if i+1 < len(options) {
				val := strings.ToLower(strings.TrimSpace(options[i+1]))
				if strings.HasPrefix(val, "requesttty=") {
					return options
				}
			}
		default:
			lower := strings.ToLower(opt)
			if strings.HasPrefix(lower, "-orequesttty=") {
				return options
			}
		}
	}
	out := append([]string{}, options...)
	out = append(out, "-t")
	return out
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$&;|<>*?()[]{}") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func shellJoin(args []string) string {
	if len(args) == 0 {
		return ""
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, shellQuote(arg))
	}
	return strings.Join(out, " ")
}
