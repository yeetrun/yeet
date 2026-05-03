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
		{name: "install user", host: "catch", info: serverInfo{InstallUser: "root"}, want: "root@catch"},
		{name: "trim user", host: "catch", info: serverInfo{InstallUser: " root "}, want: "root@catch"},
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
host = "hetz"
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
	if got != "hetz" {
		t.Fatalf("host = %q, want hetz", got)
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
		"api@hetz",
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

func TestServiceShellCommandFromResponseNotFoundIncludesHint(t *testing.T) {
	_, _, err := serviceShellCommandFromResponse(
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
