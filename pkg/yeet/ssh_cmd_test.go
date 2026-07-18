// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

const testVMSSHProxyExecutable = "/tmp/yeet-current"

func stubCurrentExecutable(t *testing.T, path string) {
	t.Helper()
	old := currentExecutableFunc
	currentExecutableFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { currentExecutableFunc = old })
}

func TestSSHTarget(t *testing.T) {
	tests := []struct {
		name string
		host string
		info serverInfo
		want string
	}{
		{name: "host only", host: "catch", want: "catch"},
		{name: "install user", host: "catch", info: serverInfo{InstallUser: "admin"}, want: "admin@catch"},
		{name: "trim user", host: "catch", info: serverInfo{InstallUser: " admin "}, want: "admin@catch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sshTarget(tt.host, tt.info); got != tt.want {
				t.Fatalf("sshTarget = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSSHArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantOptions []string
		wantService string
		wantCommand []string
		wantErr     bool
	}{
		{
			name:        "options only",
			args:        []string{"-p", "2222", "-o", "StrictHostKeyChecking=no"},
			wantOptions: []string{"-p", "2222", "-o", "StrictHostKeyChecking=no"},
		},
		{
			name:        "service only",
			args:        []string{"api"},
			wantService: "api",
		},
		{
			name:        "service command after delimiter",
			args:        []string{"api", "--", "systemctl", "status", "api"},
			wantService: "api",
			wantCommand: []string{"systemctl", "status", "api"},
		},
		{
			name:        "remote command without service",
			args:        []string{"--", "uptime"},
			wantCommand: []string{"uptime"},
		},
		{
			name:        "literal dash can be service",
			args:        []string{"-"},
			wantService: "-",
		},
		{
			name:        "short flag without argument stays alone when missing value",
			args:        []string{"-p"},
			wantOptions: []string{"-p"},
		},
		{
			name:        "short flag with attached value is not split",
			args:        []string{"-p2222", "api"},
			wantOptions: []string{"-p2222"},
			wantService: "api",
		},
		{
			name:    "implicit command requires delimiter",
			args:    []string{"api", "uptime"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOptions, gotService, gotCommand, err := parseSSHArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSSHArgs: %v", err)
			}
			if !reflect.DeepEqual(gotOptions, tt.wantOptions) {
				t.Fatalf("options = %#v, want %#v", gotOptions, tt.wantOptions)
			}
			if gotService != tt.wantService {
				t.Fatalf("service = %q, want %q", gotService, tt.wantService)
			}
			if !reflect.DeepEqual(gotCommand, tt.wantCommand) {
				t.Fatalf("command = %#v, want %#v", gotCommand, tt.wantCommand)
			}
		})
	}
}

func TestEnsureSSHCLIReturnsErrorWhenMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := ensureSSHCLI()
	if err == nil || !strings.Contains(err.Error(), "ssh CLI not found") {
		t.Fatalf("ensureSSHCLI error = %v, want missing ssh error", err)
	}
}

func TestSSHHostShellUsesRPCWithoutSSHCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	oldPrefs := loadedPrefs
	oldFetchInfo := fetchSSHServerInfoFunc
	oldExec := execRemoteShellFn
	t.Cleanup(func() {
		loadedPrefs = oldPrefs
		fetchSSHServerInfoFunc = oldFetchInfo
		execRemoteShellFn = oldExec
	})
	loadedPrefs.DefaultHost = "yeet-pve1"
	fetchSSHServerInfoFunc = func(ctx context.Context, host string) (serverInfo, error) {
		if host != "yeet-pve1" {
			t.Fatalf("server info host = %q, want yeet-pve1", host)
		}
		return serverInfo{RootDir: "/srv/yeet"}, nil
	}

	var gotHost, gotService string
	var gotTarget catchrpc.ExecTarget
	var gotArgs []string
	execRemoteShellFn = func(ctx context.Context, host string, target catchrpc.ExecTarget, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		gotHost = host
		gotTarget = target
		gotService = service
		gotArgs = append([]string{}, args...)
		return nil
	}

	if err := HandleSSH(context.Background(), []string{"ssh", "--", "whoami"}); err != nil {
		t.Fatalf("HandleSSH: %v", err)
	}
	if gotHost != "yeet-pve1" || gotTarget != catchrpc.ExecTargetHostShell || gotService != "" || !reflect.DeepEqual(gotArgs, []string{"whoami"}) {
		t.Fatalf("rpc shell = host %q target %q service %q args %#v", gotHost, gotTarget, gotService, gotArgs)
	}
}

func TestSSHNonVMServiceUsesRPCShell(t *testing.T) {
	oldFetchInfo := fetchSSHServerInfoFunc
	oldFetchSvc := fetchSSHServiceInfoFunc
	oldExec := execRemoteShellFn
	t.Cleanup(func() {
		fetchSSHServerInfoFunc = oldFetchInfo
		fetchSSHServiceInfoFunc = oldFetchSvc
		execRemoteShellFn = oldExec
	})
	fetchSSHServerInfoFunc = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{RootDir: "/srv/yeet"}, nil
	}
	fetchSSHServiceInfoFunc = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if service != "api" {
			t.Fatalf("service info service = %q, want api", service)
		}
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{ServiceType: "docker-compose"}}, nil
	}

	var gotTarget catchrpc.ExecTarget
	var gotService string
	var gotArgs []string
	execRemoteShellFn = func(ctx context.Context, host string, target catchrpc.ExecTarget, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		gotTarget = target
		gotService = service
		gotArgs = append([]string{}, args...)
		return nil
	}

	if err := HandleSSH(context.Background(), []string{"ssh", "api", "--", "pwd"}); err != nil {
		t.Fatalf("HandleSSH: %v", err)
	}
	if gotTarget != catchrpc.ExecTargetServiceShell || gotService != "api" || !reflect.DeepEqual(gotArgs, []string{"pwd"}) {
		t.Fatalf("rpc shell = target %q service %q args %#v", gotTarget, gotService, gotArgs)
	}
}

func TestSSHOptionsRejectedForRPCShell(t *testing.T) {
	oldPrefs := loadedPrefs
	oldFetchInfo := fetchSSHServerInfoFunc
	t.Cleanup(func() {
		loadedPrefs = oldPrefs
		fetchSSHServerInfoFunc = oldFetchInfo
	})
	loadedPrefs.DefaultHost = "yeet-pve1"
	fetchSSHServerInfoFunc = func(context.Context, string) (serverInfo, error) {
		return serverInfo{}, nil
	}

	_, err := sshExecutionPlanForArgs(context.Background(), []string{"ssh", "-p", "2222", "--", "whoami"})
	if err == nil || !strings.Contains(err.Error(), "SSH options only apply to VM targets") {
		t.Fatalf("error = %v, want SSH options rejected for RPC shell", err)
	}
}

func TestSSHServiceOrOverride(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()
	serviceOverride = "override-svc"

	if got := sshServiceOrOverride(""); got != "override-svc" {
		t.Fatalf("sshServiceOrOverride empty = %q, want override-svc", got)
	}
	if got := sshServiceOrOverride("explicit-svc"); got != "explicit-svc" {
		t.Fatalf("sshServiceOrOverride explicit = %q, want explicit-svc", got)
	}
}

func TestSSHInvocationFromArgsAppliesServiceOverride(t *testing.T) {
	oldService := serviceOverride
	defer func() {
		serviceOverride = oldService
	}()
	serviceOverride = "api"

	got, err := sshInvocationFromArgs([]string{"ssh", "--", "uptime"})
	if err != nil {
		t.Fatalf("sshInvocationFromArgs: %v", err)
	}
	if got.Service != "api" {
		t.Fatalf("service = %q, want api", got.Service)
	}
	if want := []string{"uptime"}; !reflect.DeepEqual(got.Command, want) {
		t.Fatalf("command = %#v, want %#v", got.Command, want)
	}
}

func TestSSHInvocationFromArgsConsumesForceProxy(t *testing.T) {
	inv, err := sshInvocationFromArgs([]string{"ssh", "--force-proxy", "-i", "key.pem", "devbox", "--", "hostname"})
	if err != nil {
		t.Fatalf("sshInvocationFromArgs: %v", err)
	}
	if !inv.ForceProxy {
		t.Fatal("ForceProxy = false, want true")
	}
	if !reflect.DeepEqual(inv.Options, []string{"-i", "key.pem"}) {
		t.Fatalf("Options = %#v", inv.Options)
	}
	if inv.Service != "devbox" {
		t.Fatalf("Service = %q", inv.Service)
	}
	if !reflect.DeepEqual(inv.Command, []string{"hostname"}) {
		t.Fatalf("Command = %#v", inv.Command)
	}
}

func TestSSHInvocationFromArgsRejectsForceProxyValue(t *testing.T) {
	_, err := sshInvocationFromArgs([]string{"ssh", "--force-proxy=true", "devbox"})
	if err == nil || !strings.Contains(err.Error(), "does not take a value") {
		t.Fatalf("error = %v, want --force-proxy value error", err)
	}
}

func TestResolveSSHHostUsesExplicitAndOverrideHosts(t *testing.T) {
	oldPrefs := loadedPrefs
	oldOverride := hostOverride
	oldOverrideSet := hostOverrideSet
	oldOverrideHard := hostOverrideHard
	defer func() {
		loadedPrefs = oldPrefs
		hostOverride = oldOverride
		hostOverrideSet = oldOverrideSet
		hostOverrideHard = oldOverrideHard
	}()
	loadedPrefs.DefaultHost = "default-host"
	resetHostOverride()

	got, err := resolveSSHHost("api@explicit-host")
	if err != nil {
		t.Fatalf("resolveSSHHost explicit error: %v", err)
	}
	if got != "explicit-host" {
		t.Fatalf("explicit host = %q, want explicit-host", got)
	}

	SetHostOverride("override-host")
	got, err = resolveSSHHost("api")
	if err != nil {
		t.Fatalf("resolveSSHHost override error: %v", err)
	}
	if got != "override-host" {
		t.Fatalf("override host = %q, want override-host", got)
	}
}

func TestResolveSSHHostFromProjectConfig(t *testing.T) {
	oldHost := loadedPrefs.DefaultHost
	oldOverride := hostOverride
	oldOverrideSet := hostOverrideSet
	oldOverrideHard := hostOverrideHard
	defer func() {
		loadedPrefs.DefaultHost = oldHost
		hostOverride = oldOverride
		hostOverrideSet = oldOverrideSet
		hostOverrideHard = oldOverrideHard
	}()
	loadedPrefs.DefaultHost = "catch"
	resetHostOverride()

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, projectConfigName), []byte(`
version = 1

[[services]]
name = "api"
host = "edge-b"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldCwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	got, err := resolveSSHHost("api")
	if err != nil {
		t.Fatalf("resolveSSHHost: %v", err)
	}
	if got != "edge-b" {
		t.Fatalf("host = %q, want edge-b", got)
	}
}

func TestServiceDataDir(t *testing.T) {
	tests := []struct {
		name    string
		service string
		info    serverInfo
		resp    catchrpc.ServiceInfoResponse
		want    string
	}{
		{name: "server path wins", service: "svc", resp: catchrpc.ServiceInfoResponse{Info: catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{Root: "/srv/svc"}}}, want: "/srv/svc/data"},
		{name: "services dir fallback", service: "svc", info: serverInfo{ServicesDir: "/srv/services"}, want: "/srv/services/svc/data"},
		{name: "root dir fallback", service: "svc", info: serverInfo{RootDir: "/srv/yeet"}, want: "/srv/yeet/services/svc/data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := serviceDataDir(tt.service, tt.info, tt.resp)
			if err != nil {
				t.Fatalf("serviceDataDir: %v", err)
			}
			if got != tt.want {
				t.Fatalf("serviceDataDir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceDataDirRequiresPathInfo(t *testing.T) {
	_, err := serviceDataDir("svc", serverInfo{}, catchrpc.ServiceInfoResponse{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServiceShellCommandFromResponseNormalizesQualifiedService(t *testing.T) {
	gotCommand, gotOptions, err := serviceShellCommandFromResponse(
		"edge-b",
		"api@edge-b",
		serverInfo{RootDir: "/srv/yeet"},
		catchrpc.ServiceInfoResponse{Found: true},
		[]string{"ls", "-la"},
		[]string{"-p", "2222"},
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if want := []string{"sh", "-lc", "cd /srv/yeet/services/api/data && exec ls -la"}; !reflect.DeepEqual(gotCommand, want) {
		t.Fatalf("command = %#v, want %#v", gotCommand, want)
	}
	if want := []string{"-p", "2222"}; !reflect.DeepEqual(gotOptions, want) {
		t.Fatalf("options = %#v, want %#v", gotOptions, want)
	}
}

func TestServiceShellCommandForVMUsesGuestSSH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	gotCommand, gotOptions, err := serviceShellCommandFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if len(gotCommand) != 0 {
		t.Fatalf("command = %#v, want empty", gotCommand)
	}
	wantOptions := []string{
		"-l", "ubuntu",
		"-o", "HostName=192.168.100.12",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".yeet", "known_hosts"),
		"-o", "HostKeyAlias=yeet-vm-devbox@yeet-pve1",
		"-o", "CheckHostIP=no",
		"-o", "ProxyCommand=/tmp/yeet-current --host=yeet-pve1 _vm-ssh-proxy devbox %h %p",
	}
	if !reflect.DeepEqual(gotOptions, wantOptions) {
		t.Fatalf("options = %#v, want %#v", gotOptions, wantOptions)
	}
}

func TestVMSSHExecutionPlanForServiceBuildsProxyPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	plan, err := vmSSHExecutionPlanForServiceInfo(
		"yeet-pve1",
		serverInfo{InstallUser: "root"},
		"devbox",
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
		nil,
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("vmSSHExecutionPlanForServiceInfo: %v", err)
	}
	for _, want := range []string{
		"-l", "ubuntu",
		"HostName=192.168.100.12",
		"HostKeyAlias=yeet-vm-devbox@yeet-pve1",
		"ProxyCommand=/tmp/yeet-current --host=yeet-pve1 _vm-ssh-proxy devbox %h %p",
		"yeet-pve1",
	} {
		if !sshOptionsContainValue(plan.Args, want) && !slices.Contains(plan.Args, want) {
			t.Fatalf("plan args = %#v, want %q", plan.Args, want)
		}
	}
	if plan.Notice != "Proxying VM SSH through yeet-pve1 to 192.168.100.12" {
		t.Fatalf("notice = %q", plan.Notice)
	}
	if plan.KnownHostRepair == nil || len(plan.KnownHostRepair.ExtraAliases) != 0 {
		t.Fatalf("repair = %#v, want only VM alias", plan.KnownHostRepair)
	}
}

func TestVMISOSSHAlwaysUsesCatchProxy(t *testing.T) {
	oldReachable := vmSSHLANReachableFunc
	vmSSHLANReachableFunc = func(string) bool {
		t.Fatal("ISO VM SSH must not probe workstation reachability")
		return false
	}
	t.Cleanup(func() { vmSSHLANReachableFunc = oldReachable })
	resp := catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
		ServiceType: serviceTypeVM,
		VM: &catchrpc.ServiceVM{
			SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "172.30.0.2"},
			Networks: []catchrpc.ServiceVMNetwork{{Mode: "iso", IP: "172.30.0.2"}},
		},
	}}
	target := vmSSHTarget(resp, false)
	if !target.Proxy || target.Host != "172.30.0.2" || target.Mode != "iso" {
		t.Fatalf("target = %#v", target)
	}
}

func TestVMISOSSHKeepsUserProxyJumpAuthoritative(t *testing.T) {
	_, plan := testSSHExecutionPlan(t,
		[]string{"ssh", "-J", "bastion", "devbox"},
		catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: serviceTypeVM,
			VM: &catchrpc.ServiceVM{
				SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "172.30.0.2"},
				Networks: []catchrpc.ServiceVMNetwork{{Mode: "iso", IP: "172.30.0.2"}},
			},
		}},
	)
	if !slices.Contains(plan.Args, "-J") || !slices.Contains(plan.Args, "bastion") {
		t.Fatalf("args = %#v, want custom proxy jump preserved", plan.Args)
	}
	if sshOptionsCountValuePrefix(plan.Args, "ProxyCommand=") != 0 {
		t.Fatalf("args = %#v, want no generated ProxyCommand", plan.Args)
	}
}

func TestVMSSHProxyCommandUsesYeetRPCInsteadOfRootSSH(t *testing.T) {
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	got := vmSSHProxyCommand("yeet-pve1", serverInfo{InstallUser: "root"}, "devbox")

	if strings.Contains(got, "ssh -W") || strings.Contains(got, "root@yeet-pve1") {
		t.Fatalf("proxy command = %q, want yeet RPC proxy without root SSH", got)
	}
	for _, want := range []string{testVMSSHProxyExecutable, "--host=yeet-pve1", "_vm-ssh-proxy", "devbox", "%h", "%p"} {
		if !strings.Contains(got, want) {
			t.Fatalf("proxy command = %q, want %q", got, want)
		}
	}
}

func TestVMSSHProxyCommandUsesCurrentExecutable(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "yeet current")
	stubCurrentExecutable(t, executable)

	got := vmSSHProxyCommand("yeet-pve1", serverInfo{}, "devbox")

	if strings.HasPrefix(got, "yeet ") {
		t.Fatalf("proxy command = %q, want current executable path instead of PATH lookup", got)
	}
	if want := shellQuote(executable) + " "; !strings.HasPrefix(got, want) {
		t.Fatalf("proxy command = %q, want prefix %q", got, want)
	}
	for _, want := range []string{"--host=yeet-pve1", "_vm-ssh-proxy", "devbox", "%h", "%p"} {
		if !strings.Contains(got, want) {
			t.Fatalf("proxy command = %q, want %q", got, want)
		}
	}
}

func TestHandleVMSSHProxyUsesRPCStream(t *testing.T) {
	oldPrefs := loadedPrefs
	oldExec := execRemoteShellFn
	t.Cleanup(func() {
		loadedPrefs = oldPrefs
		execRemoteShellFn = oldExec
	})
	loadedPrefs.DefaultHost = "yeet-pve1"

	var gotHost, gotService string
	var gotTarget catchrpc.ExecTarget
	var gotArgs []string
	execRemoteShellFn = func(ctx context.Context, host string, target catchrpc.ExecTarget, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		gotHost = host
		gotTarget = target
		gotService = service
		gotArgs = append([]string{}, args...)
		if tty {
			t.Fatal("VM SSH proxy RPC must not request a TTY")
		}
		return nil
	}

	if err := HandleVMSSHProxy(context.Background(), []string{"_vm-ssh-proxy", "devbox", "192.168.100.12", "22"}); err != nil {
		t.Fatalf("HandleVMSSHProxy: %v", err)
	}
	if gotHost != "yeet-pve1" || gotTarget != catchrpc.ExecTargetVMSSHProxy || gotService != "devbox" || !reflect.DeepEqual(gotArgs, []string{"192.168.100.12", "22"}) {
		t.Fatalf("rpc proxy = host %q target %q service %q args %#v", gotHost, gotTarget, gotService, gotArgs)
	}
}

func TestServiceShellCommandForVMSvcLANUsesReachableLANDirectly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubVMSSHLANReachable(t, func(host string) bool {
		return host == "10.0.4.80"
	})

	gotCommand, gotOptions, repair, err := serviceShellCommandPlanFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{
						{Mode: "svc", IP: "192.168.100.12"},
						{Mode: "lan", IP: "10.0.4.80"},
					},
				},
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if len(gotCommand) != 0 {
		t.Fatalf("command = %#v, want empty", gotCommand)
	}
	if !sshOptionsContainValue(gotOptions, "HostName=10.0.4.80") {
		t.Fatalf("options = %#v, want LAN hostname", gotOptions)
	}
	if sshOptionsCountValuePrefix(gotOptions, "ProxyCommand=") != 0 {
		t.Fatalf("options = %#v, want direct LAN without generated proxy", gotOptions)
	}
	if repair == nil {
		t.Fatal("KnownHostRepair = nil, want VM repair metadata")
	}
	if len(repair.ExtraAliases) != 0 {
		t.Fatalf("repair extra aliases = %#v, want none for direct LAN SSH", repair.ExtraAliases)
	}
}

func TestServiceShellCommandForVMSvcLANFallsBackToSvcProxyWhenLANUnreachable(t *testing.T) {
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	home := t.TempDir()
	t.Setenv("HOME", home)
	stubVMSSHLANReachable(t, func(host string) bool {
		if host != "10.0.4.80" {
			t.Fatalf("LAN reachability checked host %q, want 10.0.4.80", host)
		}
		return false
	})

	gotCommand, gotOptions, repair, err := serviceShellCommandPlanFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{
						{Mode: "svc", IP: "192.168.100.12"},
						{Mode: "lan", IP: "10.0.4.80"},
					},
				},
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if len(gotCommand) != 0 {
		t.Fatalf("command = %#v, want empty", gotCommand)
	}
	wantProxy := "ProxyCommand=/tmp/yeet-current --host=yeet-pve1 _vm-ssh-proxy devbox %h %p"
	for _, want := range []string{"HostName=192.168.100.12", wantProxy} {
		if !sshOptionsContainValue(gotOptions, want) {
			t.Fatalf("options = %#v, want %q", gotOptions, want)
		}
	}
	if repair == nil || len(repair.ExtraAliases) != 0 {
		t.Fatalf("repair = %#v, want only VM alias", repair)
	}
}

func TestServiceShellCommandForVMLANNetworkConnectsDirectByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	gotCommand, gotOptions, repair, err := serviceShellCommandPlanFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if len(gotCommand) != 0 {
		t.Fatalf("command = %#v, want empty", gotCommand)
	}
	wantOptions := []string{
		"-l", "ubuntu",
		"-o", "HostName=10.0.4.80",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".yeet", "known_hosts"),
		"-o", "HostKeyAlias=yeet-vm-devbox@yeet-pve1",
		"-o", "CheckHostIP=no",
	}
	if !reflect.DeepEqual(gotOptions, wantOptions) {
		t.Fatalf("options = %#v, want %#v", gotOptions, wantOptions)
	}
	if repair == nil {
		t.Fatal("KnownHostRepair = nil, want VM repair metadata")
	}
	if repair.Alias != "yeet-vm-devbox@yeet-pve1" {
		t.Fatalf("repair alias = %q", repair.Alias)
	}
	if len(repair.ExtraAliases) != 0 {
		t.Fatalf("repair extra aliases = %#v, want none for direct LAN SSH", repair.ExtraAliases)
	}
}

func TestServiceShellCommandForVMLANNetworkUsesNetworkIPWithoutSSHReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	gotCommand, gotOptions, repair, err := serviceShellCommandPlanFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				VM: &catchrpc.ServiceVM{
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if len(gotCommand) != 0 {
		t.Fatalf("command = %#v, want empty", gotCommand)
	}
	for _, want := range []string{"-l", "ubuntu", "HostName=10.0.4.80"} {
		if !sshOptionsContainValue(gotOptions, want) && !slices.Contains(gotOptions, want) {
			t.Fatalf("options = %#v, want %q", gotOptions, want)
		}
	}
	if repair == nil {
		t.Fatal("KnownHostRepair = nil, want VM repair metadata")
	}
}

func TestServiceShellCommandForVMLANNetworkCustomHostNameSkipsGeneratedProxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, gotOptions, _, notice, err := serviceShellCommandPlanFromResponseWithForce(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
		nil,
		[]string{"-o", "HostName=192.0.2.44"},
		false,
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if sshOptionsCountValuePrefix(gotOptions, "ProxyCommand=") != 0 {
		t.Fatalf("options = %#v, want no generated proxy for custom HostName", gotOptions)
	}
	if notice != "" {
		t.Fatalf("notice = %q, want none for custom HostName", notice)
	}
}

func TestServiceShellCommandForVMLANNetworkForceProxy(t *testing.T) {
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	_, plan := testSSHExecutionPlan(t,
		[]string{"ssh", "--force-proxy", "devbox"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
	)
	wantProxy := "ProxyCommand=/tmp/yeet-current --host=yeet-pve1 _vm-ssh-proxy devbox %h %p"
	if !sshOptionsContainValue(plan.Args, wantProxy) {
		t.Fatalf("args = %#v, want force proxy %q", plan.Args, wantProxy)
	}
	if slices.Contains(plan.Args, "--force-proxy") {
		t.Fatalf("args = %#v, --force-proxy should be consumed by yeet", plan.Args)
	}
	if plan.KnownHostRepair == nil || len(plan.KnownHostRepair.ExtraAliases) != 0 {
		t.Fatalf("repair = %#v, want only VM alias", plan.KnownHostRepair)
	}
}

func TestServiceShellCommandForVMSvcLANForceProxyUsesSvcIP(t *testing.T) {
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	stubVMSSHLANReachable(t, func(host string) bool {
		t.Fatalf("LAN reachability should not be checked when force proxy is set")
		return false
	})
	_, plan := testSSHExecutionPlan(t,
		[]string{"ssh", "--force-proxy", "devbox"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{
						{Mode: "svc", IP: "192.168.100.12"},
						{Mode: "lan", IP: "10.0.4.80"},
					},
				},
			},
		},
	)
	wantProxy := "ProxyCommand=/tmp/yeet-current --host=yeet-pve1 _vm-ssh-proxy devbox %h %p"
	for _, want := range []string{"HostName=192.168.100.12", wantProxy} {
		if !sshOptionsContainValue(plan.Args, want) {
			t.Fatalf("args = %#v, want %q", plan.Args, want)
		}
	}
	if sshOptionsContainValue(plan.Args, "HostName=10.0.4.80") {
		t.Fatalf("args = %#v, force proxy should not target LAN IP", plan.Args)
	}
}

func TestServiceShellCommandForVMLANNetworkCustomProxySuppressesDirectNotice(t *testing.T) {
	_, plan := testSSHExecutionPlan(t,
		[]string{"ssh", "-J", "bastion", "devbox"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
	)
	if plan.Notice != "" {
		t.Fatalf("notice = %q, want empty for custom proxy", plan.Notice)
	}
	if !slices.Contains(plan.Args, "-J") || !slices.Contains(plan.Args, "bastion") {
		t.Fatalf("args = %#v, want custom proxy jump preserved", plan.Args)
	}
	if plan.KnownHostRepair == nil {
		t.Fatal("KnownHostRepair = nil, want VM repair metadata")
	}
	if len(plan.KnownHostRepair.ExtraAliases) != 0 {
		t.Fatalf("repair extra aliases = %#v, want none for custom proxy", plan.KnownHostRepair.ExtraAliases)
	}
}

func TestServiceShellCommandForVMKeepsUserStrictHostKeyChecking(t *testing.T) {
	_, gotOptions, err := serviceShellCommandFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
		nil,
		[]string{"-o", "StrictHostKeyChecking=no"},
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if sshOptionsCountValuePrefix(gotOptions, "StrictHostKeyChecking=") != 1 {
		t.Fatalf("options = %#v, want one StrictHostKeyChecking option", gotOptions)
	}
}

func TestServiceShellCommandForVMUsesYeetKnownHostsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, gotOptions, err := serviceShellCommandFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	want := "UserKnownHostsFile=" + filepath.Join(home, ".yeet", "known_hosts")
	if !sshOptionsContainValue(gotOptions, want) {
		t.Fatalf("options = %#v, want %q", gotOptions, want)
	}
}

func TestServiceShellCommandForVMKeepsUserKnownHostsFile(t *testing.T) {
	_, gotOptions, err := serviceShellCommandFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{InstallUser: "root"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
		nil,
		[]string{"-o", "UserKnownHostsFile=/tmp/custom-known-hosts"},
	)
	if err != nil {
		t.Fatalf("serviceShellCommandFromResponse: %v", err)
	}
	if sshOptionsCountValuePrefix(gotOptions, "UserKnownHostsFile=") != 1 {
		t.Fatalf("options = %#v, want one UserKnownHostsFile option", gotOptions)
	}
	if !sshOptionsContainValue(gotOptions, "UserKnownHostsFile=/tmp/custom-known-hosts") {
		t.Fatalf("options = %#v, want custom known_hosts file", gotOptions)
	}
}

func TestEnsureVMSSHKnownHostsDirCreatesYeetDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	knownHosts := filepath.Join(home, ".yeet", "known_hosts")

	if err := ensureVMSSHKnownHostsDir([]string{"-o", "UserKnownHostsFile=" + knownHosts}); err != nil {
		t.Fatalf("ensureVMSSHKnownHostsDir: %v", err)
	}
	if st, err := os.Stat(filepath.Dir(knownHosts)); err != nil || !st.IsDir() {
		t.Fatalf("known_hosts dir stat = %v, %v; want dir", st, err)
	}
}

func TestServiceShellCommandForVMRequiresGuestHost(t *testing.T) {
	_, _, err := serviceShellCommandFromResponse(
		"yeet-pve1",
		"devbox",
		serverInfo{},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: "vm",
				VM:          &catchrpc.ServiceVM{SSH: &catchrpc.ServiceVMSSH{User: "ubuntu"}},
			},
		},
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected missing VM SSH host error")
	}
	if got := err.Error(); !strings.Contains(got, "no SSH address") || !strings.Contains(got, "yeet vm console devbox") {
		t.Fatalf("error = %q, want missing SSH address with console hint", got)
	}
}

func TestRunSSHPlanRepairsVMKnownHostAliasAndRetries(t *testing.T) {
	home, plan := testSSHExecutionPlan(t, []string{"ssh", "devbox"}, vmSSHRepairServiceInfo())
	wantAlias := "yeet-vm-devbox@yeet-pve1"
	wantKnownHosts := filepath.Join(home, ".yeet", "known_hosts")
	if plan.KnownHostRepair == nil {
		t.Fatal("KnownHostRepair = nil, want VM repair metadata")
	}
	if plan.KnownHostRepair.Alias != wantAlias {
		t.Fatalf("repair alias = %q, want %q", plan.KnownHostRepair.Alias, wantAlias)
	}
	if plan.KnownHostRepair.KnownHostsFile != wantKnownHosts {
		t.Fatalf("repair known_hosts = %q, want %q", plan.KnownHostRepair.KnownHostsFile, wantKnownHosts)
	}
	if len(plan.KnownHostRepair.ExtraAliases) != 0 {
		t.Fatalf("repair extra aliases = %#v, want none", plan.KnownHostRepair.ExtraAliases)
	}
	if !sshOptionsContainValue(plan.Args, "HostKeyAlias="+wantAlias) {
		t.Fatalf("args = %#v, want generated HostKeyAlias", plan.Args)
	}
	if !sshOptionsContainValue(plan.Args, "UserKnownHostsFile="+wantKnownHosts) {
		t.Fatalf("args = %#v, want yeet known_hosts file", plan.Args)
	}

	var runs [][]string
	firstErr := errors.New("first ssh failed")
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		runs = append(runs, append([]string{}, args...))
		if len(runs) == 1 {
			_, _ = io.WriteString(stderr, changedHostKeySSHOutput(wantKnownHosts))
			return firstErr
		}
		_, _ = io.WriteString(stderr, "Warning: Permanently added 'yeet-vm-devbox@yeet-pve1' (ED25519) to the list of known hosts.\n")
		return nil
	})
	var removedKnownHosts string
	var removedAliases []string
	stubRemoveSSHKnownHost(t, func(ctx context.Context, alias, knownHosts string) error {
		removedAliases = append(removedAliases, alias)
		removedKnownHosts = knownHosts
		return nil
	})

	var stderr bytes.Buffer
	if err := runSSHPlan(context.Background(), plan, nil, io.Discard, &stderr); err != nil {
		t.Fatalf("runSSHPlan: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("ssh runs = %d, want 2", len(runs))
	}
	if !reflect.DeepEqual(runs[0], plan.Args) || !reflect.DeepEqual(runs[1], plan.Args) {
		t.Fatalf("runs = %#v, want both with plan args %#v", runs, plan.Args)
	}
	if !reflect.DeepEqual(removedAliases, []string{wantAlias}) || removedKnownHosts != wantKnownHosts {
		t.Fatalf("removed aliases %#v lastKnownHosts=%q, want VM alias from %q", removedAliases, removedKnownHosts, wantKnownHosts)
	}
	if strings.Contains(stderr.String(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("stderr = %q, want stale host-key warning suppressed", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Permanently added") {
		t.Fatalf("stderr = %q, want retry stderr forwarded", stderr.String())
	}
}

func TestRunSSHPlanPrintsVMTransportNoticeToStderr(t *testing.T) {
	_, plan := testSSHExecutionPlan(t,
		[]string{"ssh", "devbox", "--", "hostname"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "svc", IP: "192.168.100.12"}},
				},
			},
		},
	)
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, "devbox\n")
		return nil
	})

	var stdout, stderr bytes.Buffer
	if err := runSSHPlan(context.Background(), plan, nil, &stdout, &stderr); err != nil {
		t.Fatalf("runSSHPlan: %v", err)
	}
	if stdout.String() != "devbox\n" {
		t.Fatalf("stdout = %q, want devbox newline", stdout.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "Proxying VM SSH through yeet-pve1 to 192.168.100.12" {
		t.Fatalf("stderr = %q, want VM transport notice", got)
	}
}

func TestRunSSHPlanPrintsDirectLANNoticeToStderr(t *testing.T) {
	_, plan := testSSHExecutionPlan(t,
		[]string{"ssh", "devbox"},
		catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
	)
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, "devbox\n")
		return nil
	})

	var stdout, stderr bytes.Buffer
	if err := runSSHPlan(context.Background(), plan, nil, &stdout, &stderr); err != nil {
		t.Fatalf("runSSHPlan: %v", err)
	}
	if stdout.String() != "devbox\n" {
		t.Fatalf("stdout = %q, want devbox newline", stdout.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "Connecting directly to VM LAN IP 10.0.4.80" {
		t.Fatalf("stderr = %q, want direct LAN notice", got)
	}
}

func TestRunSSHPlanDoesNotPrintNoticeForRegularService(t *testing.T) {
	plan := sshExecutionPlan{Args: []string{"root@yeet-pve1", "true"}}
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, "ok\n")
		return nil
	})

	var stdout, stderr bytes.Buffer
	if err := runSSHPlan(context.Background(), plan, nil, &stdout, &stderr); err != nil {
		t.Fatalf("runSSHPlan: %v", err)
	}
	if stdout.String() != "ok\n" {
		t.Fatalf("stdout = %q, want ok newline", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunSSHPlanDoesNotRepairCustomUserKnownHostsFile(t *testing.T) {
	_, plan := testSSHExecutionPlan(t, []string{"ssh", "-o", "UserKnownHostsFile=/tmp/custom-known-hosts", "devbox"}, vmSSHRepairServiceInfo())
	assertNoSSHRepairOnChangedHostKey(t, plan)
}

func TestRunSSHPlanDoesNotRepairCustomHostKeyAlias(t *testing.T) {
	_, plan := testSSHExecutionPlan(t, []string{"ssh", "-o", "HostKeyAlias=custom-devbox", "devbox"}, vmSSHRepairServiceInfo())
	assertNoSSHRepairOnChangedHostKey(t, plan)
}

func TestRunSSHPlanUsesRPCForNonVMServiceWithoutKnownHostRepair(t *testing.T) {
	_, plan := testSSHExecutionPlan(t, []string{"ssh", "api"}, catchrpc.ServiceInfoResponse{
		Found: true,
		Info:  catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{Root: "/srv/api"}},
	})
	assertRPCShellPlanDoesNotRepair(t, plan, catchrpc.ExecTargetServiceShell, "api")
}

func TestRunSSHPlanUsesRPCForHostShellWithoutKnownHostRepair(t *testing.T) {
	_, plan := testSSHExecutionPlanWithServiceInfo(t, []string{"ssh", "--", "uptime"}, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		t.Fatalf("fetchSSHServiceInfoFunc called for host-level ssh with host=%q service=%q", host, service)
		return catchrpc.ServiceInfoResponse{}, nil
	})
	assertRPCShellPlanDoesNotRepair(t, plan, catchrpc.ExecTargetHostShell, "")
}

func TestRunSSHPlanDoesNotRetryUnrelatedSSHError(t *testing.T) {
	_, plan := testSSHExecutionPlan(t, []string{"ssh", "devbox"}, vmSSHRepairServiceInfo())
	assertSSHPlanDoesNotRepairOnStderr(t, plan, "ssh: connect to host 192.168.100.12 port 22: Connection refused\n")
}

func TestRunSSHPlanDoesNotRepairBareHostKeyVerificationFailure(t *testing.T) {
	_, plan := testSSHExecutionPlan(t, []string{"ssh", "devbox"}, vmSSHRepairServiceInfo())
	assertSSHPlanDoesNotRepairOnStderr(t, plan, "Host key verification failed.\n")
}

func TestRunSSHPlanDoesNotRepairChangedHostKeyFromOtherKnownHostsFile(t *testing.T) {
	home, plan := testSSHExecutionPlan(t, []string{"ssh", "devbox"}, vmSSHRepairServiceInfo())
	assertSSHPlanDoesNotRepairOnStderr(t, plan, changedHostKeySSHOutput(filepath.Join(home, ".ssh", "known_hosts")))
}

func assertSSHPlanDoesNotRepairOnStderr(t *testing.T, plan sshExecutionPlan, stderrText string) {
	t.Helper()
	if plan.KnownHostRepair == nil {
		t.Fatal("KnownHostRepair = nil, want VM repair metadata")
	}

	runErr := errors.New("ssh failed")
	runs := 0
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		runs++
		_, _ = io.WriteString(stderr, stderrText)
		return runErr
	})
	removeCalled := false
	stubRemoveSSHKnownHost(t, func(ctx context.Context, alias, knownHosts string) error {
		removeCalled = true
		return nil
	})

	var stderr bytes.Buffer
	err := runSSHPlan(context.Background(), plan, nil, io.Discard, &stderr)
	if !errors.Is(err, runErr) {
		t.Fatalf("runSSHPlan error = %v, want %v", err, runErr)
	}
	if runs != 1 {
		t.Fatalf("ssh runs = %d, want 1", runs)
	}
	if removeCalled {
		t.Fatal("remove called for SSH error outside yeet's generated known_hosts entry")
	}
	if !strings.Contains(stderr.String(), stderrText) {
		t.Fatalf("stderr = %q, want replayed SSH stderr %q", stderr.String(), stderrText)
	}
}

func TestRunSSHPlanReturnsRemovalErrorWithoutRetry(t *testing.T) {
	home, plan := testSSHExecutionPlan(t, []string{"ssh", "devbox"}, vmSSHRepairServiceInfo())
	removeErr := errors.New("remove failed")
	runs := 0
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		runs++
		_, _ = io.WriteString(stderr, changedHostKeySSHOutput(filepath.Join(home, ".yeet", "known_hosts")))
		return errors.New("first ssh failed")
	})
	stubRemoveSSHKnownHost(t, func(ctx context.Context, alias, knownHosts string) error {
		return removeErr
	})

	err := runSSHPlan(context.Background(), plan, nil, io.Discard, io.Discard)
	if !errors.Is(err, removeErr) {
		t.Fatalf("runSSHPlan error = %v, want %v", err, removeErr)
	}
	if runs != 1 {
		t.Fatalf("ssh runs = %d, want 1", runs)
	}
}

func TestRemoveSSHKnownHostRemovesStaleBackupBeforeSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "host_key")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate test ssh key: %v: %s", err, strings.TrimSpace(string(output)))
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("read generated public key: %v", err)
	}
	knownHosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("yeet-vm-test@catch "+string(pub)), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	if err := os.WriteFile(knownHosts+".old", []byte("stale backup"), 0o600); err != nil {
		t.Fatalf("write stale known_hosts backup: %v", err)
	}

	if err := removeSSHKnownHost(context.Background(), "yeet-vm-test@catch", knownHosts); err != nil {
		t.Fatalf("removeSSHKnownHost error = %v", err)
	}
	got, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatalf("read known_hosts after removal: %v", err)
	}
	if strings.Contains(string(got), "yeet-vm-test@catch") {
		t.Fatalf("known_hosts still contains alias after removal: %q", string(got))
	}
	if _, err := os.Stat(knownHosts + ".old"); err != nil {
		t.Fatalf("known_hosts backup was not recreated by ssh-keygen: %v", err)
	}
}

func TestRunSSHPlanReturnsRetryError(t *testing.T) {
	home, plan := testSSHExecutionPlan(t, []string{"ssh", "devbox"}, vmSSHRepairServiceInfo())
	retryErr := errors.New("retry failed")
	runs := 0
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		runs++
		if runs == 1 {
			_, _ = io.WriteString(stderr, changedHostKeySSHOutput(filepath.Join(home, ".yeet", "known_hosts")))
			return errors.New("first ssh failed")
		}
		return retryErr
	})
	removals := 0
	stubRemoveSSHKnownHost(t, func(ctx context.Context, alias, knownHosts string) error {
		removals++
		return nil
	})

	err := runSSHPlan(context.Background(), plan, nil, io.Discard, io.Discard)
	if !errors.Is(err, retryErr) {
		t.Fatalf("runSSHPlan error = %v, want %v", err, retryErr)
	}
	if runs != 2 {
		t.Fatalf("ssh runs = %d, want 2", runs)
	}
	if removals != 1 {
		t.Fatalf("removals = %d, want 1", removals)
	}
}

func sshOptionsCountValuePrefix(options []string, prefix string) int {
	count := 0
	for i, opt := range options {
		switch {
		case opt == "-o" && i+1 < len(options) && strings.HasPrefix(options[i+1], prefix):
			count++
		case strings.HasPrefix(opt, "-o") && strings.HasPrefix(opt[2:], prefix):
			count++
		}
	}
	return count
}

func sshOptionsContainValue(options []string, value string) bool {
	for i, opt := range options {
		if opt == "-o" && i+1 < len(options) && options[i+1] == value {
			return true
		}
		if strings.HasPrefix(opt, "-o") && opt[2:] == value {
			return true
		}
	}
	return false
}

func TestServiceShellCommandFromResponseNotFoundIncludesHint(t *testing.T) {
	_, _, err := serviceShellCommandFromResponse(
		"catch",
		"api",
		serverInfo{},
		catchrpc.ServiceInfoResponse{Found: false, Message: "missing"},
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "missing") || !strings.Contains(got, "yeet ssh -- <cmd>") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServiceNotFoundShellErrorUsesDefaultMessage(t *testing.T) {
	err := serviceNotFoundShellError("api", " ")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, `service "api" not found`) || !strings.Contains(got, "yeet ssh -- <cmd>") {
		t.Fatalf("error = %q, want default not-found message with hint", got)
	}
}

func TestBuildServiceSSHCommand(t *testing.T) {
	tests := []struct {
		name        string
		serviceDir  string
		command     []string
		options     []string
		wantCommand []string
		wantOptions []string
	}{
		{
			name:        "interactive shell forces tty",
			serviceDir:  "/srv/svc/data",
			wantCommand: []string{"sh", "-lc", "cd /srv/svc/data && exec ${SHELL:-/bin/sh} -l"},
			wantOptions: []string{"-t"},
		},
		{
			name:        "command preserves options",
			serviceDir:  "/srv/svc data",
			command:     []string{"echo", "hello world"},
			options:     []string{"-p", "2222"},
			wantCommand: []string{"sh", "-lc", "cd '/srv/svc data' && exec echo 'hello world'"},
			wantOptions: []string{"-p", "2222"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCommand, gotOptions := buildServiceSSHCommand(tt.serviceDir, tt.command, tt.options)
			if !reflect.DeepEqual(gotCommand, tt.wantCommand) {
				t.Fatalf("command = %#v, want %#v", gotCommand, tt.wantCommand)
			}
			if !reflect.DeepEqual(gotOptions, tt.wantOptions) {
				t.Fatalf("options = %#v, want %#v", gotOptions, tt.wantOptions)
			}
		})
	}
}

func TestBuildSSHArgs(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		target  string
		command []string
		want    []string
	}{
		{
			name:    "simple command",
			options: []string{"-p", "2222"},
			target:  "admin@host",
			command: []string{"uptime"},
			want:    []string{"-p", "2222", "admin@host", "uptime"},
		},
		{
			name:    "shell command preserves argv boundaries",
			options: []string{"-p", "2222"},
			target:  "admin@host",
			command: []string{"bash", "-lc", "echo one; echo two"},
			want:    []string{"-p", "2222", "admin@host", "bash -lc 'echo one; echo two'"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSSHArgs(tt.options, tt.target, tt.command)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildSSHArgs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSSHArgsFromInvocationUsesHostNameOverrideTarget(t *testing.T) {
	got := sshArgsFromInvocation("yeet-pve1", serverInfo{InstallUser: "root"}, sshInvocation{
		Service: "devbox",
		Options: []string{
			"-l", "ubuntu",
			"-o", "HostName=192.168.100.12",
		},
		Command: []string{"hostname"},
	})
	want := []string{"-l", "ubuntu", "-o", "HostName=192.168.100.12", "yeet-pve1", "hostname"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshArgsFromInvocation = %#v, want %#v", got, want)
	}
}

func TestTrimSSHCommandName(t *testing.T) {
	if got := trimSSHCommandName([]string{"ssh", "api"}); !reflect.DeepEqual(got, []string{"api"}) {
		t.Fatalf("trimSSHCommandName command = %#v, want api", got)
	}
	if got := trimSSHCommandName([]string{"api"}); !reflect.DeepEqual(got, []string{"api"}) {
		t.Fatalf("trimSSHCommandName no command = %#v, want unchanged", got)
	}
}

func TestSSHOptionNeedsArg(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{token: "-p", want: true},
		{token: "-o", want: true},
		{token: "-v"},
		{token: "--"},
		{token: "-"},
		{token: ""},
	}

	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			if got := sshOptionNeedsArg(tt.token); got != tt.want {
				t.Fatalf("sshOptionNeedsArg(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestEnsureTTYOption(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		want    []string
	}{
		{name: "adds tty", want: []string{"-t"}},
		{name: "keeps single tty", options: []string{"-t"}, want: []string{"-t"}},
		{name: "keeps double tty", options: []string{"-tt"}, want: []string{"-tt"}},
		{name: "keeps disabled tty", options: []string{"-T"}, want: []string{"-T"}},
		{name: "keeps request tty option pair", options: []string{"-o", "RequestTTY=no"}, want: []string{"-o", "RequestTTY=no"}},
		{name: "keeps compact request tty option", options: []string{"-oRequestTTY=force"}, want: []string{"-oRequestTTY=force"}},
		{name: "adds after unrelated option pair", options: []string{"-o", "StrictHostKeyChecking=no"}, want: []string{"-o", "StrictHostKeyChecking=no", "-t"}},
		{name: "adds after dangling option", options: []string{"-o"}, want: []string{"-o", "-t"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureTTYOption(tt.options)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ensureTTYOption = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestHasSSHUserOption(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		want    bool
	}{
		{name: "none", options: []string{"-p", "2222"}},
		{name: "short pair", options: []string{"-l", "ubuntu"}, want: true},
		{name: "short compact", options: []string{"-lubuntu"}, want: true},
		{name: "option pair", options: []string{"-o", "User=ubuntu"}, want: true},
		{name: "option compact", options: []string{"-oUser=ubuntu"}, want: true},
		{name: "dangling option", options: []string{"-o"}},
		{name: "unrelated option", options: []string{"-o", "HostName=192.0.2.10"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasSSHUserOption(tt.options); got != tt.want {
				t.Fatalf("hasSSHUserOption(%#v) = %v, want %v", tt.options, got, tt.want)
			}
		})
	}
}

func testSSHExecutionPlan(t *testing.T, args []string, serviceInfo catchrpc.ServiceInfoResponse) (string, sshExecutionPlan) {
	t.Helper()
	return testSSHExecutionPlanWithServiceInfo(t, args, func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return serviceInfo, nil
	})
}

func testSSHExecutionPlanWithServiceInfo(
	t *testing.T,
	args []string,
	serviceInfoFn func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error),
) (string, sshExecutionPlan) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	sshDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sshDir, "ssh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", sshDir)

	oldPrefs := loadedPrefs
	oldServiceOverride := serviceOverride
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	oldHostOverrideHard := hostOverrideHard
	loadedPrefs = prefs{DefaultHost: "yeet-pve1"}
	serviceOverride = ""
	resetHostOverride()
	t.Cleanup(func() {
		loadedPrefs = oldPrefs
		serviceOverride = oldServiceOverride
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
		hostOverrideHard = oldHostOverrideHard
	})

	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldCwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	oldFetchServerInfo := fetchSSHServerInfoFunc
	oldFetchServiceInfo := fetchSSHServiceInfoFunc
	fetchSSHServerInfoFunc = func(ctx context.Context, host string) (serverInfo, error) {
		if host != "yeet-pve1" {
			t.Fatalf("fetchSSHServerInfoFunc host = %q, want yeet-pve1", host)
		}
		return serverInfo{InstallUser: "root"}, nil
	}
	fetchSSHServiceInfoFunc = serviceInfoFn
	t.Cleanup(func() {
		fetchSSHServerInfoFunc = oldFetchServerInfo
		fetchSSHServiceInfoFunc = oldFetchServiceInfo
	})

	plan, err := sshExecutionPlanForArgs(context.Background(), args)
	if err != nil {
		t.Fatalf("sshExecutionPlanForArgs: %v", err)
	}
	return home, plan
}

func stubRunSSHCommand(t *testing.T, fn sshCommandRunner) {
	t.Helper()
	old := runSSHCommandFunc
	runSSHCommandFunc = fn
	t.Cleanup(func() {
		runSSHCommandFunc = old
	})
}

func stubRemoveSSHKnownHost(t *testing.T, fn sshKnownHostRemover) {
	t.Helper()
	old := removeSSHKnownHostFunc
	removeSSHKnownHostFunc = fn
	t.Cleanup(func() {
		removeSSHKnownHostFunc = old
	})
}

func stubExecRemoteShell(t *testing.T, fn func(context.Context, string, catchrpc.ExecTarget, string, []string, io.Reader, bool, io.Writer) error) {
	t.Helper()
	old := execRemoteShellFn
	execRemoteShellFn = fn
	t.Cleanup(func() {
		execRemoteShellFn = old
	})
}

func stubVMSSHLANReachable(t *testing.T, fn func(string) bool) {
	t.Helper()
	old := vmSSHLANReachableFunc
	vmSSHLANReachableFunc = fn
	t.Cleanup(func() {
		vmSSHLANReachableFunc = old
	})
}

func vmSSHRepairServiceInfo() catchrpc.ServiceInfoResponse {
	return catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			ServiceType: serviceTypeVM,
			Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
			VM: &catchrpc.ServiceVM{
				SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
			},
		},
	}
}

func assertNoSSHRepairOnChangedHostKey(t *testing.T, plan sshExecutionPlan) {
	t.Helper()
	if plan.KnownHostRepair != nil {
		t.Fatalf("KnownHostRepair = %#v, want nil", plan.KnownHostRepair)
	}
	runErr := errors.New("ssh failed")
	runs := 0
	stubRunSSHCommand(t, func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		runs++
		_, _ = io.WriteString(stderr, changedHostKeySSHOutput("/tmp/known_hosts"))
		return runErr
	})
	stubRemoveSSHKnownHost(t, func(ctx context.Context, alias, knownHosts string) error {
		t.Fatalf("removeSSHKnownHostFunc called with alias=%q knownHosts=%q", alias, knownHosts)
		return nil
	})

	err := runSSHPlan(context.Background(), plan, nil, io.Discard, io.Discard)
	if !errors.Is(err, runErr) {
		t.Fatalf("runSSHPlan error = %v, want %v", err, runErr)
	}
	if runs != 1 {
		t.Fatalf("ssh runs = %d, want 1", runs)
	}
}

func changedHostKeySSHOutput(knownHosts string) string {
	return "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n" +
		"WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\n" +
		"Offending ED25519 key in " + knownHosts + ":1\n" +
		"Host key verification failed.\n"
}

func assertRPCShellPlanDoesNotRepair(t *testing.T, plan sshExecutionPlan, target catchrpc.ExecTarget, service string) {
	t.Helper()
	if plan.KnownHostRepair != nil {
		t.Fatalf("KnownHostRepair = %#v, want nil", plan.KnownHostRepair)
	}
	if plan.RPCShell == nil {
		t.Fatal("RPCShell = nil, want RPC shell plan")
	}
	if plan.RPCShell.Target != target || plan.RPCShell.Service != service {
		t.Fatalf("RPCShell = %#v, want target %q service %q", plan.RPCShell, target, service)
	}
	runErr := errors.New("rpc failed")
	stubExecRemoteShell(t, func(ctx context.Context, host string, gotTarget catchrpc.ExecTarget, gotService string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		if gotTarget != target || gotService != service {
			t.Fatalf("execRemoteShell target/service = %q/%q, want %q/%q", gotTarget, gotService, target, service)
		}
		return runErr
	})
	stubRemoveSSHKnownHost(t, func(ctx context.Context, alias, knownHosts string) error {
		t.Fatalf("removeSSHKnownHostFunc called with alias=%q knownHosts=%q", alias, knownHosts)
		return nil
	})

	err := runSSHPlan(context.Background(), plan, nil, io.Discard, io.Discard)
	if !errors.Is(err, runErr) {
		t.Fatalf("runSSHPlan error = %v, want %v", err, runErr)
	}
}
