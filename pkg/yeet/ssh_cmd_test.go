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
	var removedAlias, removedKnownHosts string
	stubRemoveSSHKnownHost(t, func(ctx context.Context, alias, knownHosts string) error {
		removedAlias = alias
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
	if removedAlias != wantAlias || removedKnownHosts != wantKnownHosts {
		t.Fatalf("removed %q from %q, want %q from %q", removedAlias, removedKnownHosts, wantAlias, wantKnownHosts)
	}
	if strings.Contains(stderr.String(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("stderr = %q, want stale host-key warning suppressed", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Permanently added") {
		t.Fatalf("stderr = %q, want retry stderr forwarded", stderr.String())
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

func TestRunSSHPlanDoesNotRepairNonVMServiceCommand(t *testing.T) {
	_, plan := testSSHExecutionPlan(t, []string{"ssh", "api"}, catchrpc.ServiceInfoResponse{
		Found: true,
		Info:  catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{Root: "/srv/api"}},
	})
	assertNoSSHRepairOnChangedHostKey(t, plan)
}

func TestRunSSHPlanDoesNotRepairHostLevelSSH(t *testing.T) {
	_, plan := testSSHExecutionPlanWithServiceInfo(t, []string{"ssh", "--", "uptime"}, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		t.Fatalf("fetchSSHServiceInfoFunc called for host-level ssh with host=%q service=%q", host, service)
		return catchrpc.ServiceInfoResponse{}, nil
	})
	assertNoSSHRepairOnChangedHostKey(t, plan)
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
	loadedPrefs = prefs{DefaultHost: "yeet-pve1"}
	serviceOverride = ""
	resetHostOverride()
	t.Cleanup(func() {
		loadedPrefs = oldPrefs
		serviceOverride = oldServiceOverride
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
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
