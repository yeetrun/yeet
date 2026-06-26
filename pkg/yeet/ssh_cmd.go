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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type sshInvocation struct {
	Options    []string
	Service    string
	Command    []string
	ForceProxy bool
}

type rpcShellPlan struct {
	Host    string
	Target  catchrpc.ExecTarget
	Service string
	Command []string
}

type sshExecutionPlan struct {
	Args            []string
	KnownHostRepair *sshKnownHostRepair
	Notice          string
	RPCShell        *rpcShellPlan
}

type sshKnownHostRepair struct {
	Alias          string
	KnownHostsFile string
	ExtraAliases   []string
}

type sshCommandRunner func(context.Context, []string, io.Reader, io.Writer, io.Writer) error
type sshKnownHostRemover func(context.Context, string, string) error

var (
	runSSHCommandFunc       sshCommandRunner    = runSSHCommand
	removeSSHKnownHostFunc  sshKnownHostRemover = removeSSHKnownHost
	fetchSSHServerInfoFunc                      = fetchSSHServerInfo
	fetchSSHServiceInfoFunc                     = fetchSSHServiceInfo
	vmSSHLANReachableFunc                       = vmSSHLANReachable
	execRemoteShellFn                           = execRemoteShell
)

const vmSSHLANReachabilityTimeout = 300 * time.Millisecond

func HandleSSH(ctx context.Context, args []string) error {
	plan, err := sshExecutionPlanForArgs(ctx, args)
	if err != nil {
		return err
	}
	return runSSHPlan(ctx, plan, os.Stdin, os.Stdout, os.Stderr)
}

func sshExecutionPlanForArgs(ctx context.Context, args []string) (sshExecutionPlan, error) {
	inv, err := sshInvocationFromArgs(args)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	host, err := resolveSSHHost(inv.Service)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	if strings.TrimSpace(host) == "" {
		return sshExecutionPlan{}, fmt.Errorf("no host configured")
	}
	if inv.Service == "" {
		return rpcHostShellPlan(host, inv)
	}
	return serviceSSHExecutionPlan(ctx, host, inv)
}

func ensureSSHCLI() error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh CLI not found in PATH")
	}
	return nil
}

func sshInvocationFromArgs(args []string) (sshInvocation, error) {
	forceProxy, sshArgs, err := splitYeetSSHFlags(trimSSHCommandName(args))
	if err != nil {
		return sshInvocation{}, err
	}
	options, service, command, err := parseSSHArgs(sshArgs)
	if err != nil {
		return sshInvocation{}, err
	}
	return sshInvocation{
		Options:    options,
		Service:    sshServiceOrOverride(service),
		Command:    command,
		ForceProxy: forceProxy,
	}, nil
}

func sshServiceOrOverride(service string) string {
	if service == "" && serviceOverride != "" {
		return serviceOverride
	}
	return service
}

func fetchSSHServerInfo(ctx context.Context, host string) (serverInfo, error) {
	var info serverInfo
	err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info)
	return info, err
}

func runSSHCommand(ctx context.Context, sshArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func runSSHPlan(ctx context.Context, plan sshExecutionPlan, stdin io.Reader, stdout, stderr io.Writer) error {
	if plan.RPCShell != nil {
		rpc := plan.RPCShell
		return execRemoteShellFn(ctx, rpc.Host, rpc.Target, rpc.Service, rpc.Command, stdin, true, stdout)
	}
	if strings.TrimSpace(plan.Notice) != "" {
		if _, err := fmt.Fprintln(writerOrDiscard(stderr), plan.Notice); err != nil {
			return err
		}
	}
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
	for _, alias := range plan.KnownHostRepair.Aliases() {
		if err := removeSSHKnownHostFunc(ctx, alias, plan.KnownHostRepair.KnownHostsFile); err != nil {
			return err
		}
	}
	return runSSHCommandFunc(ctx, plan.Args, stdin, stdout, stderr)
}

func rpcHostShellPlan(host string, inv sshInvocation) (sshExecutionPlan, error) {
	if len(inv.Options) > 0 || inv.ForceProxy {
		return sshExecutionPlan{}, fmt.Errorf("SSH options only apply to VM targets")
	}
	return sshExecutionPlan{
		RPCShell: &rpcShellPlan{
			Host:    host,
			Target:  catchrpc.ExecTargetHostShell,
			Command: inv.Command,
		},
	}, nil
}

func serviceSSHExecutionPlan(ctx context.Context, host string, inv sshInvocation) (sshExecutionPlan, error) {
	service := baseSSHServiceName(inv.Service)
	resp, err := fetchSSHServiceInfoFunc(ctx, host, service)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	if !resp.Found {
		return sshExecutionPlan{}, serviceNotFoundShellError(service, resp.Message)
	}
	if resp.Info.ServiceType != serviceTypeVM {
		if len(inv.Options) > 0 || inv.ForceProxy {
			return sshExecutionPlan{}, fmt.Errorf("SSH options only apply to VM targets")
		}
		return sshExecutionPlan{
			RPCShell: &rpcShellPlan{
				Host:    host,
				Target:  catchrpc.ExecTargetServiceShell,
				Service: service,
				Command: inv.Command,
			},
		}, nil
	}
	if err := ensureSSHCLI(); err != nil {
		return sshExecutionPlan{}, err
	}
	info, err := fetchSSHServerInfoFunc(ctx, host)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	plan, err := vmSSHExecutionPlanForServiceInfo(host, info, service, resp, inv.Command, inv.Options, inv.ForceProxy)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	if err := ensureVMSSHKnownHostsDir(plan.Args); err != nil {
		return sshExecutionPlan{}, err
	}
	return plan, nil
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

func (r sshKnownHostRepair) Aliases() []string {
	aliases := []string{r.Alias}
	for _, alias := range r.ExtraAliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || slices.Contains(aliases, alias) {
			continue
		}
		aliases = append(aliases, alias)
	}
	return aliases
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func removeSSHKnownHost(ctx context.Context, alias, knownHosts string) error {
	backup := knownHosts + ".old"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale VM SSH host key backup %s: %w", backup, err)
	}
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

func splitYeetSSHFlags(args []string) (bool, []string, error) {
	out := make([]string, 0, len(args))
	forceProxy := false
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" || token == "-" || !strings.HasPrefix(token, "-") {
			out = append(out, args[i:]...)
			return forceProxy, out, nil
		}
		if token == "--force-proxy" {
			forceProxy = true
			continue
		}
		if strings.HasPrefix(token, "--force-proxy=") {
			return false, nil, fmt.Errorf("ssh --force-proxy does not take a value")
		}
		out = append(out, token)
		if sshOptionNeedsArg(token) && len(token) == 2 && i+1 < len(args) {
			out = append(out, args[i+1])
			i++
		}
	}
	return forceProxy, out, nil
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
	if len(command) > 0 {
		sshArgs = append(sshArgs, shellJoin(command))
	}
	return sshArgs
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
	command, options, repair, _, err := serviceShellCommandPlanFromResponseWithForce(host, service, info, resp, command, options, false)
	return command, options, repair, err
}

func serviceShellCommandPlanFromResponseWithForce(host, service string, info serverInfo, resp catchrpc.ServiceInfoResponse, command []string, options []string, forceProxy bool) ([]string, []string, *sshKnownHostRepair, string, error) {
	service = baseSSHServiceName(service)
	if resp.Info.ServiceType == serviceTypeVM {
		plan, err := vmSSHExecutionPlanForServiceInfo(host, info, service, resp, command, options, forceProxy)
		if err != nil {
			return nil, nil, nil, "", err
		}
		commandArgs := 0
		if len(command) > 0 {
			commandArgs = 1
		}
		optionsLen := len(plan.Args) - 1 - commandArgs
		if optionsLen < 0 {
			return nil, nil, nil, "", fmt.Errorf("invalid VM SSH plan for service %q", service)
		}
		return command, plan.Args[:optionsLen], plan.KnownHostRepair, plan.Notice, nil
	}
	if !resp.Found {
		return nil, nil, nil, "", serviceNotFoundShellError(service, resp.Message)
	}
	serviceDir, err := serviceDataDir(service, info, resp)
	if err != nil {
		return nil, nil, nil, "", err
	}
	command, options = buildServiceSSHCommand(serviceDir, command, options)
	return command, options, nil, "", nil
}

func vmSSHExecutionPlanForServiceInfo(host string, info serverInfo, service string, resp catchrpc.ServiceInfoResponse, command []string, options []string, forceProxy bool) (sshExecutionPlan, error) {
	if !resp.Found {
		return sshExecutionPlan{}, serviceNotFoundShellError(service, resp.Message)
	}
	if resp.Info.ServiceType != serviceTypeVM {
		return sshExecutionPlan{}, fmt.Errorf("service %q is not a VM service", service)
	}
	vmPlan, err := buildVMSSHOptionsPlan(host, info, service, resp, options, forceProxy)
	if err != nil {
		return sshExecutionPlan{}, err
	}
	inv := sshInvocation{
		Options:    vmPlan.Options,
		Service:    service,
		Command:    command,
		ForceProxy: forceProxy,
	}
	return sshExecutionPlan{
		Args:            sshArgsFromInvocation(host, info, inv),
		KnownHostRepair: vmPlan.KnownHostRepair,
		Notice:          vmPlan.Notice,
	}, nil
}

type vmSSHOptionsPlan struct {
	Options         []string
	KnownHostRepair *sshKnownHostRepair
	Notice          string
	GeneratedProxy  bool
}

func buildVMSSHOptionsPlan(proxyHost string, info serverInfo, service string, resp catchrpc.ServiceInfoResponse, options []string, forceProxy bool) (vmSSHOptionsPlan, error) {
	out := append([]string{}, options...)
	target := vmSSHTargetWithOptions(resp, out, forceProxy)
	if target.Host == "" && !hasSSHHostNameOption(out) {
		return vmSSHOptionsPlan{}, fmt.Errorf("VM %q has no SSH address yet; use `yeet vm console %s`", service, service)
	}
	knownHosts := vmSSHKnownHostsFile()
	addYeetKnownHosts := knownHosts != "" && !hasSSHUserKnownHostsFileOption(out)
	alias := vmSSHHostKeyAlias(service, proxyHost)
	addGeneratedAlias := !hasSSHHostKeyAliasOption(out)
	addGeneratedCheckHostIP := addGeneratedAlias && !hasSSHCheckHostIPOption(out)

	out = appendVMSSHBaseOptions(out, target)
	out = appendVMSSHIdentityOptions(out, service, proxyHost, addGeneratedAlias, addGeneratedCheckHostIP)
	userProxy := hasSSHProxyOption(out)
	out, generatedProxy := appendVMSSHProxyOptions(out, target, proxyHost, info)
	plan := vmSSHOptionsPlan{
		Options:        out,
		Notice:         vmSSHTransportNotice(proxyHost, target, generatedProxy, userProxy),
		GeneratedProxy: generatedProxy,
	}
	plan.KnownHostRepair = vmSSHKnownHostRepair(alias, knownHosts, addYeetKnownHosts, addGeneratedAlias, generatedProxy, proxyHost)
	return plan, nil
}

func vmSSHTargetWithOptions(resp catchrpc.ServiceInfoResponse, options []string, forceProxy bool) vmSSHTargetInfo {
	if hasSSHHostNameOption(options) && !forceProxy {
		return vmSSHTargetInfo{User: vmSSHUser(resp)}
	}
	target := vmSSHTarget(resp, forceProxy)
	return target
}

func vmSSHKnownHostRepair(alias, knownHosts string, addYeetKnownHosts, addGeneratedAlias, generatedProxy bool, proxyHost string) *sshKnownHostRepair {
	if !addYeetKnownHosts || !addGeneratedAlias {
		return nil
	}
	extraAliases := []string(nil)
	if generatedProxy {
		extraAliases = append(extraAliases, vmSSHProxyHostKeyAlias(proxyHost))
	}
	return &sshKnownHostRepair{
		Alias:          alias,
		KnownHostsFile: knownHosts,
		ExtraAliases:   extraAliases,
	}
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

func appendVMSSHIdentityOptions(options []string, service, proxyHost string, addAlias, addCheckHostIP bool) []string {
	out := options
	if addAlias {
		out = append(out, "-o", "HostKeyAlias="+vmSSHHostKeyAlias(service, proxyHost))
	}
	if addCheckHostIP {
		out = append(out, "-o", "CheckHostIP=no")
	}
	return out
}

func appendVMSSHProxyOptions(options []string, target vmSSHTargetInfo, proxyHost string, info serverInfo) ([]string, bool) {
	out := options
	if target.Proxy && !hasSSHProxyOption(out) {
		out = append(out, "-o", "ProxyCommand="+vmSSHProxyCommand(proxyHost, info))
		return out, true
	}
	return out, false
}

func vmSSHProxyCommand(proxyHost string, info serverInfo) string {
	args := []string{"ssh"}
	if knownHosts := vmSSHKnownHostsFile(); knownHosts != "" {
		args = append(args,
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "UserKnownHostsFile="+knownHosts,
			"-o", "HostKeyAlias="+vmSSHProxyHostKeyAlias(proxyHost),
			"-o", "CheckHostIP=no",
		)
	}
	args = append(args, "-W", "%h:%p", sshTarget(proxyHost, info))
	return shellJoin(args)
}

type vmSSHTargetInfo struct {
	User       string
	Host       string
	Mode       string
	Proxy      bool
	ForceProxy bool
}

func vmSSHTarget(resp catchrpc.ServiceInfoResponse, forceProxy bool) vmSSHTargetInfo {
	user := vmSSHUser(resp)
	host := vmSSHTargetHost(resp, forceProxy)
	mode := vmSSHNetworkMode(resp, host)
	return vmSSHTargetInfo{
		User:       user,
		Host:       host,
		Mode:       mode,
		Proxy:      shouldProxyVMSSH(resp, host, mode, forceProxy),
		ForceProxy: forceProxy,
	}
}

func vmSSHUser(resp catchrpc.ServiceInfoResponse) string {
	user := "ubuntu"
	if resp.Info.VM != nil && resp.Info.VM.SSH != nil {
		user = firstNonEmpty(strings.TrimSpace(resp.Info.VM.SSH.User), user)
	}
	return user
}

func vmSSHTargetHost(resp catchrpc.ServiceInfoResponse, forceProxy bool) string {
	lanHost := vmSSHLANHost(resp)
	svcHost := vmSSHSvcHost(resp)
	if forceProxy {
		return firstNonEmpty(svcHost, vmSSHReportedHost(resp), lanHost)
	}
	if lanHost != "" && svcHost != "" {
		if vmSSHLANReachableFunc(lanHost) {
			return lanHost
		}
		return svcHost
	}
	return vmSSHPreferredHost(resp)
}

func vmSSHPreferredHost(resp catchrpc.ServiceInfoResponse) string {
	if host := vmSSHLANHost(resp); host != "" {
		return host
	}
	if host := vmSSHSvcHost(resp); host != "" {
		return host
	}
	return vmSSHReportedHost(resp)
}

func vmSSHLANHost(resp catchrpc.ServiceInfoResponse) string {
	if resp.Info.VM != nil {
		for _, network := range resp.Info.VM.Networks {
			if strings.TrimSpace(network.Mode) == "lan" {
				if host := strings.TrimSpace(network.IP); host != "" {
					return host
				}
			}
		}
	}
	return ""
}

func vmSSHSvcHost(resp catchrpc.ServiceInfoResponse) string {
	return strings.TrimSpace(resp.Info.Network.SvcIP)
}

func vmSSHReportedHost(resp catchrpc.ServiceInfoResponse) string {
	if resp.Info.VM != nil && resp.Info.VM.SSH != nil {
		return strings.TrimSpace(resp.Info.VM.SSH.Host)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func vmSSHNetworkMode(resp catchrpc.ServiceInfoResponse, host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.TrimSpace(resp.Info.Network.SvcIP) == host {
		return "svc"
	}
	if resp.Info.VM == nil {
		return ""
	}
	for _, network := range resp.Info.VM.Networks {
		if strings.TrimSpace(network.IP) == host {
			return strings.TrimSpace(network.Mode)
		}
	}
	return ""
}

func shouldProxyVMSSH(resp catchrpc.ServiceInfoResponse, guestHost, mode string, forceProxy bool) bool {
	if resp.Info.VM == nil || strings.TrimSpace(guestHost) == "" {
		return false
	}
	if forceProxy || mode == "svc" {
		return true
	}
	return false
}

func vmSSHLANReachable(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "22"), vmSSHLANReachabilityTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func vmSSHTransportNotice(proxyHost string, target vmSSHTargetInfo, generatedProxy, userProxy bool) string {
	if strings.TrimSpace(target.Host) == "" {
		return ""
	}
	if generatedProxy {
		return fmt.Sprintf("Proxying VM SSH through %s to %s", proxyHost, target.Host)
	}
	if userProxy {
		return ""
	}
	if target.Mode == "lan" {
		return fmt.Sprintf("Connecting directly to VM LAN IP %s", target.Host)
	}
	return ""
}

func vmSSHHostKeyAlias(service, host string) string {
	service = strings.TrimSpace(baseSSHServiceName(service))
	host = strings.TrimSpace(host)
	if host == "" {
		return "yeet-vm-" + service
	}
	return "yeet-vm-" + service + "@" + host
}

func vmSSHProxyHostKeyAlias(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "yeet-proxy"
	}
	return "yeet-proxy@" + host
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

func hasSSHCheckHostIPOption(options []string) bool {
	for i, opt := range options {
		switch {
		case opt == "-o" && i+1 < len(options):
			if isSSHCheckHostIPOptionValue(options[i+1]) {
				return true
			}
		case strings.HasPrefix(opt, "-o") && isSSHCheckHostIPOptionValue(opt[2:]):
			return true
		}
	}
	return false
}

func isSSHCheckHostIPOptionValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "checkhostip=")
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
		return []string{"sh", "-lc", cmd}, options
	}
	cmd := fmt.Sprintf("cd %s && exec %s", shellQuote(serviceDir), shellJoin(command))
	return []string{"sh", "-lc", cmd}, options
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
