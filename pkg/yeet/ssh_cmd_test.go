// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

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

func TestResolveSSHHostUsesExplicitAndOverrideHosts(t *testing.T) {
	oldPrefs := loadedPrefs
	oldOverride := hostOverride
	oldOverrideSet := hostOverrideSet
	defer func() {
		loadedPrefs = oldPrefs
		hostOverride = oldOverride
		hostOverrideSet = oldOverrideSet
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
	defer func() {
		loadedPrefs.DefaultHost = oldHost
		hostOverride = oldOverride
		hostOverrideSet = oldOverrideSet
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
	if want := []string{"sh", "-lc", `'cd /srv/yeet/services/api/data && exec ls -la'`}; !reflect.DeepEqual(gotCommand, want) {
		t.Fatalf("command = %#v, want %#v", gotCommand, want)
	}
	if want := []string{"-p", "2222"}; !reflect.DeepEqual(gotOptions, want) {
		t.Fatalf("options = %#v, want %#v", gotOptions, want)
	}
}

func TestServiceShellCommandForVMUsesGuestSSH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

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
		"-o", "ProxyCommand=ssh -W %h:%p root@yeet-pve1",
	}
	if !reflect.DeepEqual(gotOptions, wantOptions) {
		t.Fatalf("options = %#v, want %#v", gotOptions, wantOptions)
	}
}

func TestServiceShellCommandForVMLANNetworkDoesNotProxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	gotCommand, gotOptions, err := serviceShellCommandFromResponse(
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
	}
	if !reflect.DeepEqual(gotOptions, wantOptions) {
		t.Fatalf("options = %#v, want %#v", gotOptions, wantOptions)
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
	if strings.Count(strings.Join(gotOptions, "\n"), "StrictHostKeyChecking") != 1 {
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
	if strings.Count(strings.Join(gotOptions, "\n"), "UserKnownHostsFile") != 1 {
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
			wantCommand: []string{"sh", "-lc", `'cd /srv/svc/data && exec ${SHELL:-/bin/sh} -l'`},
			wantOptions: []string{"-t"},
		},
		{
			name:        "command preserves options",
			serviceDir:  "/srv/svc data",
			command:     []string{"echo", "hello world"},
			options:     []string{"-p", "2222"},
			wantCommand: []string{"sh", "-lc", `'cd '"'"'/srv/svc data'"'"' && exec echo '"'"'hello world'"'"''`},
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
	got := buildSSHArgs([]string{"-p", "2222"}, "admin@host", []string{"uptime"})
	want := []string{"-p", "2222", "admin@host", "uptime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSSHArgs = %#v, want %#v", got, want)
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
