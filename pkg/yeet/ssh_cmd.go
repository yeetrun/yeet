// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type sshInvocation struct {
	Options []string
	Service string
	Command []string
}

func HandleSSH(ctx context.Context, args []string) error {
	sshArgs, err := sshCommandArgs(ctx, args)
	if err != nil {
		return err
	}
	return runSSHCommand(ctx, sshArgs, os.Stdin, os.Stdout, os.Stderr)
}

func sshCommandArgs(ctx context.Context, args []string) ([]string, error) {
	if err := ensureSSHCLI(); err != nil {
		return nil, err
	}
	inv, err := sshInvocationFromArgs(args)
	if err != nil {
		return nil, err
	}
	host, info, err := sshHostInfo(ctx, inv.Service)
	if err != nil {
		return nil, err
	}
	inv, err = withServiceShellCommand(ctx, host, info, inv)
	if err != nil {
		return nil, err
	}
	return buildSSHArgs(inv.Options, sshTarget(host, info), inv.Command), nil
}

func ensureSSHCLI() error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh CLI not found in PATH")
	}
	return nil
}

func sshInvocationFromArgs(args []string) (sshInvocation, error) {
	options, service, command, err := parseSSHArgs(trimSSHCommandName(args))
	if err != nil {
		return sshInvocation{}, err
	}
	return sshInvocation{
		Options: options,
		Service: sshServiceOrOverride(service),
		Command: command,
	}, nil
}

func sshServiceOrOverride(service string) string {
	if service == "" && serviceOverride != "" {
		return serviceOverride
	}
	return service
}

func sshHostInfo(ctx context.Context, service string) (string, serverInfo, error) {
	host, err := resolveSSHHost(service)
	if err != nil {
		return "", serverInfo{}, err
	}
	if strings.TrimSpace(host) == "" {
		return "", serverInfo{}, fmt.Errorf("no host configured")
	}
	info, err := fetchSSHServerInfo(ctx, host)
	if err != nil {
		return "", serverInfo{}, err
	}
	return host, info, nil
}

func fetchSSHServerInfo(ctx context.Context, host string) (serverInfo, error) {
	var info serverInfo
	err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info)
	return info, err
}

func withServiceShellCommand(ctx context.Context, host string, info serverInfo, inv sshInvocation) (sshInvocation, error) {
	if inv.Service == "" {
		return inv, nil
	}
	command, options, err := serviceShellCommand(ctx, host, inv.Service, info, inv.Command, inv.Options)
	if err != nil {
		return sshInvocation{}, err
	}
	inv.Command = command
	inv.Options = options
	return inv, nil
}

func runSSHCommand(ctx context.Context, sshArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
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
	selection := currentSSHHostSelection()
	svc, svcHost, _ := splitServiceHost(service)
	if svcHost != "" {
		return svcHost, nil
	}
	if service == "" || selection.overrideSet {
		return selection.host, nil
	}
	return resolveSSHHostFromProject(selection.host, svc)
}

type sshHostSelection struct {
	host        string
	overrideSet bool
}

func currentSSHHostSelection() sshHostSelection {
	host := Host()
	hostOverride, hostOverrideSet := HostOverride()
	if hostOverrideSet {
		host = hostOverride
	}
	return sshHostSelection{host: host, overrideSet: hostOverrideSet}
}

func resolveSSHHostFromProject(host, service string) (string, error) {
	cfgLoc, err := loadProjectConfigFromCwd()
	if err != nil {
		return "", err
	}
	if cfgLoc == nil {
		return host, nil
	}
	resolved, err := resolveServiceHost(cfgLoc.Config, service)
	if err != nil {
		return "", err
	}
	if resolved == "" {
		return host, nil
	}
	return resolved, nil
}

func serviceShellCommand(ctx context.Context, host, service string, info serverInfo, command []string, options []string) ([]string, []string, error) {
	service = baseSSHServiceName(service)
	resp, err := fetchSSHServiceInfo(ctx, host, service)
	if err != nil {
		return nil, nil, err
	}
	return serviceShellCommandFromResponse(service, info, resp, command, options)
}

func baseSSHServiceName(service string) string {
	svc, svcHost, _ := splitServiceHost(service)
	if svcHost != "" {
		return svc
	}
	return service
}

func fetchSSHServiceInfo(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
	return newRPCClient(host).ServiceInfo(ctx, service)
}

func serviceShellCommandFromResponse(service string, info serverInfo, resp catchrpc.ServiceInfoResponse, command []string, options []string) ([]string, []string, error) {
	service = baseSSHServiceName(service)
	if !resp.Found {
		return nil, nil, serviceNotFoundShellError(service, resp.Message)
	}
	serviceDir, err := serviceDataDir(service, info, resp)
	if err != nil {
		return nil, nil, err
	}
	command, options = buildServiceSSHCommand(serviceDir, command, options)
	return command, options, nil
}

func serviceNotFoundShellError(service, message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = fmt.Sprintf("service %q not found", service)
	}
	msg = msg + " (use `yeet ssh -- <cmd>` to run a remote command without a service)"
	return errors.New(msg)
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
