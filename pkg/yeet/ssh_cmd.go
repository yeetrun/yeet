// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
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

type sshExecutionPlan struct {
	Args            []string
	KnownHostRepair *sshKnownHostRepair
}

type sshKnownHostRepair struct {
	Alias          string
	KnownHostsFile string
}

type sshCommandRunner func(context.Context, []string, io.Reader, io.Writer, io.Writer) error
type sshKnownHostRemover func(context.Context, string, string) error

var (
	runSSHCommandFunc       sshCommandRunner    = runSSHCommand
	removeSSHKnownHostFunc  sshKnownHostRemover = removeSSHKnownHost
	fetchSSHServerInfoFunc                      = fetchSSHServerInfo
	fetchSSHServiceInfoFunc                     = fetchSSHServiceInfo
)

func HandleSSH(ctx context.Context, args []string) error {
	plan, err := sshExecutionPlanForArgs(ctx, args)
	if err != nil {
		return err
	}
	return runSSHPlan(ctx, plan, os.Stdin, os.Stdout, os.Stderr)
}

func sshExecutionPlanForArgs(ctx context.Context, args []string) (sshExecutionPlan, error) {
	if err := ensureSSHCLI(); err != nil {
		return sshExecutionPlan{}, err
	}
	host, info, inv, repair, err := resolvedSSHInvocation(ctx, args)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	return sshExecutionPlan{
		Args:            sshArgsFromInvocation(host, info, inv),
		KnownHostRepair: repair,
	}, nil
}

func resolvedSSHInvocation(ctx context.Context, args []string) (string, serverInfo, sshInvocation, *sshKnownHostRepair, error) {
	inv, err := sshInvocationFromArgs(args)
	if err != nil {
		return "", serverInfo{}, sshInvocation{}, nil, err
	}
	host, info, err := sshHostInfo(ctx, inv.Service)
	if err != nil {
		return "", serverInfo{}, sshInvocation{}, nil, err
	}
	inv, repair, err := withServiceShellCommand(ctx, host, info, inv)
	if err != nil {
		return "", serverInfo{}, sshInvocation{}, nil, err
	}
	if err := ensureVMSSHKnownHostsDir(inv.Options); err != nil {
		return "", serverInfo{}, sshInvocation{}, nil, err
	}
	return host, info, inv, repair, nil
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
	info, err := fetchSSHServerInfoFunc(ctx, host)
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

func withServiceShellCommand(ctx context.Context, host string, info serverInfo, inv sshInvocation) (sshInvocation, *sshKnownHostRepair, error) {
	if inv.Service == "" {
		return inv, nil, nil
	}
	command, options, repair, err := serviceShellCommand(ctx, host, inv.Service, info, inv.Command, inv.Options)
	if err != nil {
		return sshInvocation{}, nil, err
	}
	inv.Command = command
	inv.Options = options
	return inv, repair, nil
}

func runSSHCommand(ctx context.Context, sshArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func runSSHPlan(ctx context.Context, plan sshExecutionPlan, stdin io.Reader, stdout, stderr io.Writer) error {
	if !plan.canRepairKnownHost() {
		return runSSHCommandFunc(ctx, plan.Args, stdin, stdout, stderr)
	}

	var stderrBuf bytes.Buffer
	firstErr := runSSHCommandFunc(ctx, plan.Args, stdin, stdout, &stderrBuf)
	if firstErr == nil {
		replaySSHStderr(&stderrBuf, stderr)
		return nil
	}
	if !shouldRepairSSHKnownHostError(stderrBuf.String(), *plan.KnownHostRepair) {
		replaySSHStderr(&stderrBuf, stderr)
		return firstErr
	}
	if err := removeSSHKnownHostFunc(ctx, plan.KnownHostRepair.Alias, plan.KnownHostRepair.KnownHostsFile); err != nil {
		return err
	}
	return runSSHCommandFunc(ctx, plan.Args, stdin, stdout, stderr)
}

func replaySSHStderr(buf *bytes.Buffer, stderr io.Writer) {
	if buf == nil || buf.Len() == 0 {
		return
	}
	_, _ = buf.WriteTo(writerOrDiscard(stderr))
}

func (p sshExecutionPlan) canRepairKnownHost() bool {
	return p.KnownHostRepair != nil &&
		strings.TrimSpace(p.KnownHostRepair.Alias) != "" &&
		strings.TrimSpace(p.KnownHostRepair.KnownHostsFile) != ""
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func removeSSHKnownHost(ctx context.Context, alias, knownHosts string) error {
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-R", alias, "-f", knownHosts)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(output)); msg != "" {
		return fmt.Errorf("remove stale VM SSH host key %q from %s: %w: %s", alias, knownHosts, err, msg)
	}
	return fmt.Errorf("remove stale VM SSH host key %q from %s: %w", alias, knownHosts, err)
}

func shouldRepairSSHKnownHostError(output string, repair sshKnownHostRepair) bool {
	if !strings.Contains(strings.ToLower(output), "remote host identification has changed") {
		return false
	}
	return sshChangedHostKeyOutputReferencesKnownHosts(output, repair.KnownHostsFile)
}

func sshChangedHostKeyOutputReferencesKnownHosts(output, knownHosts string) bool {
	knownHosts = filepath.Clean(strings.TrimSpace(knownHosts))
	if knownHosts == "." {
		return false
	}
	for _, line := range strings.Split(output, "\n") {
		offendingPath, ok := sshOffendingKeyPath(line)
		if !ok {
			continue
		}
		if filepath.Clean(offendingPath) == knownHosts {
			return true
		}
	}
	return false
}

func sshOffendingKeyPath(line string) (string, bool) {
	lower := strings.ToLower(line)
	const marker = " key in "
	idx := strings.Index(lower, marker)
	if idx < 0 || !strings.Contains(lower[:idx], "offending") {
		return "", false
	}
	path := strings.TrimSpace(line[idx+len(marker):])
	if colon := strings.LastIndex(path, ":"); colon >= 0 && asciiDigitsOnly(path[colon+1:]) {
		path = path[:colon]
	}
	path = strings.TrimSpace(path)
	return path, path != ""
}

func asciiDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
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

func sshArgsFromInvocation(host string, info serverInfo, inv sshInvocation) []string {
	target := sshInvocationTarget(host, info, inv)
	return buildSSHArgs(inv.Options, target, inv.Command)
}

func sshInvocationTarget(host string, info serverInfo, inv sshInvocation) string {
	if inv.Service != "" && hasSSHHostNameOption(inv.Options) {
		return host
	}
	return sshTarget(host, info)
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

func serviceShellCommand(ctx context.Context, host, service string, info serverInfo, command []string, options []string) ([]string, []string, *sshKnownHostRepair, error) {
	service = baseSSHServiceName(service)
	resp, err := fetchSSHServiceInfoFunc(ctx, host, service)
	if err != nil {
		return nil, nil, nil, err
	}
	return serviceShellCommandPlanFromResponse(host, service, info, resp, command, options)
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

func serviceShellCommandFromResponse(host, service string, info serverInfo, resp catchrpc.ServiceInfoResponse, command []string, options []string) ([]string, []string, error) {
	command, options, _, err := serviceShellCommandPlanFromResponse(host, service, info, resp, command, options)
	return command, options, err
}

func serviceShellCommandPlanFromResponse(host, service string, info serverInfo, resp catchrpc.ServiceInfoResponse, command []string, options []string) ([]string, []string, *sshKnownHostRepair, error) {
	service = baseSSHServiceName(service)
	if !resp.Found {
		return nil, nil, nil, serviceNotFoundShellError(service, resp.Message)
	}
	if resp.Info.ServiceType == serviceTypeVM {
		vmPlan, err := buildVMSSHOptionsPlan(host, info, service, resp, options)
		if err != nil {
			return nil, nil, nil, err
		}
		return command, vmPlan.Options, vmPlan.KnownHostRepair, nil
	}
	serviceDir, err := serviceDataDir(service, info, resp)
	if err != nil {
		return nil, nil, nil, err
	}
	command, options = buildServiceSSHCommand(serviceDir, command, options)
	return command, options, nil, nil
}

type vmSSHOptionsPlan struct {
	Options         []string
	KnownHostRepair *sshKnownHostRepair
}

func buildVMSSHOptionsPlan(proxyHost string, info serverInfo, service string, resp catchrpc.ServiceInfoResponse, options []string) (vmSSHOptionsPlan, error) {
	out := append([]string{}, options...)
	target := vmSSHTarget(resp)
	if target.Host == "" && !hasSSHHostNameOption(out) {
		return vmSSHOptionsPlan{}, fmt.Errorf("VM %q has no SSH address yet; use `yeet vm console %s`", service, service)
	}
	knownHosts := vmSSHKnownHostsFile()
	addYeetKnownHosts := knownHosts != "" && !hasSSHUserKnownHostsFileOption(out)
	alias := vmSSHHostKeyAlias(service, proxyHost)
	addGeneratedAlias := target.Proxy && !hasSSHHostKeyAliasOption(out)

	out = appendVMSSHBaseOptions(out, target)
	out = appendVMSSHProxyOptions(out, target, service, proxyHost, info)
	plan := vmSSHOptionsPlan{Options: out}
	if addYeetKnownHosts && addGeneratedAlias && knownHosts == vmSSHKnownHostsFile() && alias == vmSSHHostKeyAlias(service, proxyHost) {
		plan.KnownHostRepair = &sshKnownHostRepair{
			Alias:          alias,
			KnownHostsFile: knownHosts,
		}
	}
	return plan, nil
}

func appendVMSSHBaseOptions(options []string, target vmSSHTargetInfo) []string {
	out := options
	if !hasSSHUserOption(out) {
		out = append(out, "-l", target.User)
	}
	if target.Host != "" && !hasSSHHostNameOption(out) {
		out = append(out, "-o", "HostName="+target.Host)
	}
	if !hasSSHStrictHostKeyCheckingOption(out) {
		out = append(out, "-o", "StrictHostKeyChecking=accept-new")
	}
	if knownHosts := vmSSHKnownHostsFile(); knownHosts != "" && !hasSSHUserKnownHostsFileOption(out) {
		out = append(out, "-o", "UserKnownHostsFile="+knownHosts)
	}
	return out
}

func appendVMSSHProxyOptions(options []string, target vmSSHTargetInfo, service, proxyHost string, info serverInfo) []string {
	out := options
	if target.Proxy && !hasSSHHostKeyAliasOption(out) {
		out = append(out, "-o", "HostKeyAlias="+vmSSHHostKeyAlias(service, proxyHost))
	}
	if target.Proxy && !hasSSHProxyOption(out) {
		out = append(out, "-o", "ProxyCommand=ssh -W %h:%p "+shellQuote(sshTarget(proxyHost, info)))
	}
	return out
}

type vmSSHTargetInfo struct {
	User  string
	Host  string
	Proxy bool
}

func vmSSHTarget(resp catchrpc.ServiceInfoResponse) vmSSHTargetInfo {
	user := "ubuntu"
	host := strings.TrimSpace(resp.Info.Network.SvcIP)
	if resp.Info.VM != nil && resp.Info.VM.SSH != nil {
		user = firstNonEmpty(strings.TrimSpace(resp.Info.VM.SSH.User), user)
		host = firstNonEmpty(strings.TrimSpace(resp.Info.VM.SSH.Host), host)
	}
	return vmSSHTargetInfo{User: user, Host: host, Proxy: shouldProxyVMSSH(resp, host)}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func shouldProxyVMSSH(resp catchrpc.ServiceInfoResponse, guestHost string) bool {
	svcIP := strings.TrimSpace(resp.Info.Network.SvcIP)
	if svcIP != "" && guestHost == svcIP {
		return true
	}
	if resp.Info.VM == nil {
		return false
	}
	for _, network := range resp.Info.VM.Networks {
		if strings.TrimSpace(network.Mode) == "svc" && strings.TrimSpace(network.IP) == guestHost {
			return true
		}
	}
	return false
}

func vmSSHHostKeyAlias(service, host string) string {
	service = strings.TrimSpace(baseSSHServiceName(service))
	host = strings.TrimSpace(host)
	if host == "" {
		return "yeet-vm-" + service
	}
	return "yeet-vm-" + service + "@" + host
}

func vmSSHKnownHostsFile() string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".yeet", "known_hosts")
}

func ensureVMSSHKnownHostsDir(options []string) error {
	knownHosts := vmSSHKnownHostsFile()
	if knownHosts == "" || !usesVMSSHKnownHostsFile(options, knownHosts) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		return fmt.Errorf("prepare VM SSH known_hosts file: %w", err)
	}
	return nil
}

func usesVMSSHKnownHostsFile(options []string, knownHosts string) bool {
	want := "userknownhostsfile=" + strings.ToLower(knownHosts)
	for i, opt := range options {
		switch {
		case opt == "-o" && i+1 < len(options):
			if strings.ToLower(strings.TrimSpace(options[i+1])) == want {
				return true
			}
		case strings.HasPrefix(opt, "-o"):
			if strings.ToLower(strings.TrimSpace(opt[2:])) == want {
				return true
			}
		}
	}
	return false
}

func hasSSHUserOption(options []string) bool {
	for i, opt := range options {
		if isSSHUserOption(options, i, opt) {
			return true
		}
	}
	return false
}

func isSSHUserOption(options []string, i int, opt string) bool {
	switch {
	case opt == "-l" && i+1 < len(options):
		return true
	case strings.HasPrefix(opt, "-l") && len(opt) > 2:
		return true
	case opt == "-o" && i+1 < len(options):
		return isSSHUserOptionValue(options[i+1])
	case strings.HasPrefix(opt, "-o"):
		return isSSHUserOptionValue(opt[2:])
	default:
		return false
	}
}

func isSSHUserOptionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "user=")
}

func hasSSHHostNameOption(options []string) bool {
	for i, opt := range options {
		switch {
		case opt == "-o" && i+1 < len(options):
			if isSSHHostNameOptionValue(options[i+1]) {
				return true
			}
		case strings.HasPrefix(opt, "-o") && isSSHHostNameOptionValue(opt[2:]):
			return true
		}
	}
	return false
}

func isSSHHostNameOptionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "hostname=")
}

func hasSSHHostKeyAliasOption(options []string) bool {
	for i, opt := range options {
		switch {
		case opt == "-o" && i+1 < len(options):
			if isSSHHostKeyAliasOptionValue(options[i+1]) {
				return true
			}
		case strings.HasPrefix(opt, "-o") && isSSHHostKeyAliasOptionValue(opt[2:]):
			return true
		}
	}
	return false
}

func isSSHHostKeyAliasOptionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "hostkeyalias=")
}

func hasSSHStrictHostKeyCheckingOption(options []string) bool {
	for i, opt := range options {
		switch {
		case opt == "-o" && i+1 < len(options):
			if isSSHStrictHostKeyCheckingOptionValue(options[i+1]) {
				return true
			}
		case strings.HasPrefix(opt, "-o") && isSSHStrictHostKeyCheckingOptionValue(opt[2:]):
			return true
		}
	}
	return false
}

func isSSHStrictHostKeyCheckingOptionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "stricthostkeychecking=")
}

func hasSSHUserKnownHostsFileOption(options []string) bool {
	for i, opt := range options {
		switch {
		case opt == "-o" && i+1 < len(options):
			if isSSHUserKnownHostsFileOptionValue(options[i+1]) {
				return true
			}
		case strings.HasPrefix(opt, "-o") && isSSHUserKnownHostsFileOptionValue(opt[2:]):
			return true
		}
	}
	return false
}

func isSSHUserKnownHostsFileOptionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "userknownhostsfile=")
}

func hasSSHProxyOption(options []string) bool {
	for i, opt := range options {
		switch {
		case opt == "-J":
			return true
		case strings.HasPrefix(opt, "-J") && len(opt) > 2:
			return true
		case opt == "-o" && i+1 < len(options):
			if isSSHProxyOptionValue(options[i+1]) {
				return true
			}
		case strings.HasPrefix(opt, "-o") && isSSHProxyOptionValue(opt[2:]):
			return true
		}
	}
	return false
}

func isSSHProxyOptionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "proxycommand=") ||
		strings.HasPrefix(value, "proxyjump=")
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
