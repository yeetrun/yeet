// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestInspectISOProjectAcceptsExactAdmittedRuntime(t *testing.T) {
	opts, runner := newISOInspectTestOptions(t)
	inspection, err := InspectISOProject(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Verify(); err != nil {
		t.Fatalf("exact admitted runtime rejected: %v", err)
	}
	if got := inspection.Addresses["api"]; got != opts.Components["api"] {
		t.Fatalf("api address = %v, want %v", got, opts.Components["api"])
	}
	if !slices.Equal(runner.calls, []string{"compose-ps", "inspect"}) {
		t.Fatalf("inspection calls = %v, want compose ps then docker inspect", runner.calls)
	}
}

func TestInspectISOProjectAcceptsComposePSJSONLines(t *testing.T) {
	opts, runner := newISOInspectTestOptions(t)
	ps, err := json.Marshal(runner.ps[0])
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := json.Marshal(runner.inspect)
	if err != nil {
		t.Fatal(err)
	}
	opts.run = func(_ context.Context, operation string, _ ...string) ([]byte, error) {
		if operation == "compose-ps" {
			return append(ps, '\n'), nil
		}
		return inspect, nil
	}
	inspection, err := InspectISOProject(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectISOProjectRejectsContainerAndNetworkDrift(t *testing.T) {
	for _, tt := range []struct {
		name string
		want string
		edit func(ps []map[string]any, inspect []map[string]any)
	}{
		{name: "missing component", want: "api", edit: func(ps, _ []map[string]any) { clear(ps[0]) }},
		{name: "unexpected component", want: "worker", edit: func(ps, _ []map[string]any) { ps[0]["Service"] = "worker" }},
		{name: "stopped component", want: "running", edit: func(ps, _ []map[string]any) { ps[0]["State"] = "exited" }},
		{name: "wrong address", want: "172.30.128.2", edit: func(_, inspect []map[string]any) {
			networks := inspect[0]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
			networks["catch-app_default"].(map[string]any)["IPAddress"] = "172.30.128.9"
		}},
		{name: "extra generated network", want: "other", edit: func(_, inspect []map[string]any) {
			networks := inspect[0]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
			networks["other"] = map[string]any{"IPAddress": "10.0.0.2"}
		}},
		{name: "host network namespace", want: "network", edit: func(_, inspect []map[string]any) { inspect[0]["HostConfig"].(map[string]any)["NetworkMode"] = "host" }},
		{name: "container pid namespace", want: "pid", edit: func(_, inspect []map[string]any) {
			inspect[0]["HostConfig"].(map[string]any)["PidMode"] = "container:peer"
		}},
		{name: "host ipc namespace", want: "ipc", edit: func(_, inspect []map[string]any) { inspect[0]["HostConfig"].(map[string]any)["IpcMode"] = "host" }},
		{name: "host cgroup namespace", want: "cgroup", edit: func(_, inspect []map[string]any) { inspect[0]["HostConfig"].(map[string]any)["CgroupnsMode"] = "host" }},
		{name: "host user namespace", want: "user", edit: func(_, inspect []map[string]any) { inspect[0]["HostConfig"].(map[string]any)["UsernsMode"] = "host" }},
		{name: "privileged", want: "privileged", edit: func(_, inspect []map[string]any) { inspect[0]["HostConfig"].(map[string]any)["Privileged"] = true }},
		{name: "added capability", want: "cap", edit: func(_, inspect []map[string]any) {
			inspect[0]["HostConfig"].(map[string]any)["CapAdd"] = []any{"NET_ADMIN"}
		}},
		{name: "device", want: "device", edit: func(_, inspect []map[string]any) {
			inspect[0]["HostConfig"].(map[string]any)["Devices"] = []any{map[string]any{"PathOnHost": "/dev/kvm", "PathInContainer": "/dev/kvm"}}
		}},
		{name: "published port", want: "port", edit: func(_, inspect []map[string]any) {
			inspect[0]["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{"80/tcp": []any{map[string]any{"HostPort": "8080"}}}
		}},
		{name: "IPv6", want: "ipv6", edit: func(_, inspect []map[string]any) {
			networks := inspect[0]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
			networks["catch-app_default"].(map[string]any)["GlobalIPv6Address"] = "2001:db8::2"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, runner := newISOInspectTestOptions(t)
			tt.edit(runner.ps, runner.inspect)
			assertISOInspectionFailure(t, opts, tt.want)
		})
	}

	t.Run("duplicate component", func(t *testing.T) {
		opts, runner := newISOInspectTestOptions(t)
		duplicate := map[string]any{"ID": "cid-api-2", "Name": "catch-app-api-2", "Service": "api", "State": "running"}
		runner.ps = append(runner.ps, duplicate)
		assertISOInspectionFailure(t, opts, "exactly one")
	})
}

func TestInspectISOProjectRejectsMissingAndDuplicateContainerIDs(t *testing.T) {
	for _, tt := range []struct {
		name string
		want string
		edit func(ps *[]map[string]any, inspect *[]map[string]any)
	}{
		{
			name: "missing Compose ps ID",
			want: "container ID",
			edit: func(ps, _ *[]map[string]any) {
				(*ps)[0]["ID"] = ""
			},
		},
		{
			name: "duplicate Compose ps ID",
			want: "duplicate container ID",
			edit: func(ps, _ *[]map[string]any) {
				*ps = append(*ps, map[string]any{"ID": "cid-api", "Name": "catch-app-api-2", "Service": "api", "State": "running"})
			},
		},
		{
			name: "inspected container missing identity evidence",
			want: "containers[0].Id is missing",
			edit: func(_ *[]map[string]any, inspect *[]map[string]any) {
				clear((*inspect)[0])
			},
		},
		{
			name: "duplicate inspected container",
			want: "exactly one inspected container",
			edit: func(_ *[]map[string]any, inspect *[]map[string]any) {
				duplicate := make(map[string]any, len((*inspect)[0]))
				for key, value := range (*inspect)[0] {
					duplicate[key] = value
				}
				*inspect = append(*inspect, duplicate)
			},
		},
		{
			name: "unknown inspected container",
			want: "unexpected container",
			edit: func(_ *[]map[string]any, inspect *[]map[string]any) {
				(*inspect)[0]["Id"] = "cid-unknown"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, runner := newISOInspectTestOptions(t)
			tt.edit(&runner.ps, &runner.inspect)
			assertISOInspectionFailure(t, opts, tt.want)
		})
	}
}

func TestInspectISOProjectRejectsMalformedDockerJSON(t *testing.T) {
	for _, tt := range []struct {
		name, operation, want string
	}{
		{name: "Compose ps", operation: "compose-ps", want: "decode ISO Compose project containers"},
		{name: "Docker inspect", operation: "inspect", want: "decode ISO Docker containers"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, _ := newISOInspectTestOptions(t)
			opts.run = func(_ context.Context, operation string, _ ...string) ([]byte, error) {
				if operation == tt.operation {
					return []byte(`{"unterminated"`), nil
				}
				if operation == "compose-ps" {
					return []byte(`[{"ID":"cid-api","Service":"api","State":"running"}]`), nil
				}
				return nil, fmt.Errorf("unexpected operation %q", operation)
			}
			_, err := InspectISOProject(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("InspectISOProject error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestInspectISOProjectRejectsMissingDockerSecurityEvidence(t *testing.T) {
	tests := []struct {
		name string
		want string
		edit func(map[string]any)
	}{
		{name: "state", want: "State", edit: func(container map[string]any) { delete(container, "State") }},
		{name: "running state", want: "State.Running", edit: func(container map[string]any) { delete(container["State"].(map[string]any), "Running") }},
		{name: "config", want: "Config", edit: func(container map[string]any) { delete(container, "Config") }},
		{name: "compose labels", want: "Config.Labels", edit: func(container map[string]any) { delete(container["Config"].(map[string]any), "Labels") }},
		{name: "host config", want: "HostConfig", edit: func(container map[string]any) { delete(container, "HostConfig") }},
		{name: "network settings", want: "NetworkSettings", edit: func(container map[string]any) { delete(container, "NetworkSettings") }},
		{name: "mount list", want: "Mounts", edit: func(container map[string]any) { delete(container, "Mounts") }},
		{name: "null mount list", want: "Mounts", edit: func(container map[string]any) { container["Mounts"] = nil }},
	}
	for _, field := range []string{
		"NetworkMode", "PidMode", "IpcMode", "CgroupnsMode", "UTSMode", "UsernsMode", "Privileged", "CapAdd", "Devices", "DeviceCgroupRules", "DeviceRequests", "PortBindings", "SecurityOpt",
	} {
		field := field
		tests = append(tests, struct {
			name string
			want string
			edit func(map[string]any)
		}{
			name: "host config " + field,
			want: "HostConfig." + field,
			edit: func(container map[string]any) {
				delete(container["HostConfig"].(map[string]any), field)
			},
		})
	}
	for _, field := range []string{"Networks", "Ports"} {
		field := field
		tests = append(tests, struct {
			name string
			want string
			edit func(map[string]any)
		}{
			name: "network settings " + field,
			want: "NetworkSettings." + field,
			edit: func(container map[string]any) {
				delete(container["NetworkSettings"].(map[string]any), field)
			},
		})
	}
	for _, field := range []string{"IPAddress", "GlobalIPv6Address"} {
		field := field
		tests = append(tests, struct {
			name string
			want string
			edit func(map[string]any)
		}{
			name: "generated network " + field,
			want: "NetworkSettings.Networks.catch-app_default." + field,
			edit: func(container map[string]any) {
				networks := container["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
				delete(networks["catch-app_default"].(map[string]any), field)
			},
		})
	}
	for _, field := range []string{"Type", "Source", "Destination"} {
		field := field
		tests = append(tests, struct {
			name string
			want string
			edit func(map[string]any)
		}{
			name: "mount " + field,
			want: "Mounts[0]." + field,
			edit: func(container map[string]any) {
				container["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "named", "Destination": "/data"}}
				delete(container["Mounts"].([]any)[0].(map[string]any), field)
			},
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, runner := newISOInspectTestOptions(t)
			tt.edit(runner.inspect[0])
			_, err := InspectISOProject(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("InspectISOProject error = %v, want missing evidence path %q", err, tt.want)
			}
		})
	}
}

func TestInspectISOProjectRejectsAdditionalDockerSecurityDrift(t *testing.T) {
	for _, tt := range []struct {
		name  string
		field string
		value any
		want  string
	}{
		{name: "host UTS namespace", field: "UTSMode", value: "host", want: "uts"},
		{name: "device cgroup rule", field: "DeviceCgroupRules", value: []any{"c 1:3 rwm"}, want: "device cgroup"},
		{name: "device request", field: "DeviceRequests", value: []any{map[string]any{"Capabilities": []any{[]any{"gpu"}}}}, want: "device request"},
		{name: "unconfined security option", field: "SecurityOpt", value: []any{"seccomp=unconfined"}, want: "security"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, runner := newISOInspectTestOptions(t)
			runner.inspect[0]["HostConfig"].(map[string]any)[tt.field] = tt.value
			assertISOInspectionFailure(t, opts, tt.want)
		})
	}
}

func TestInspectISOProjectRejectsRuntimeBindChangedToSpecialResource(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "runtime-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	opts, runner := newISOInspectTestOptionsAtRoot(t, root)
	runner.inspect[0]["Mounts"] = []any{map[string]any{
		"Type": "bind", "Source": fifo, "Destination": "/data",
	}}
	assertISOInspectionFailure(t, opts, "FIFO")

	socketRoot, err := os.MkdirTemp("/tmp", "yiso-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socket := filepath.Join(socketRoot, "runtime.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	opts, runner = newISOInspectTestOptionsAtRoot(t, socketRoot)
	runner.inspect[0]["Mounts"] = []any{map[string]any{
		"Type": "bind", "Source": socket, "Destination": "/socket",
	}}
	assertISOInspectionFailure(t, opts, "socket")

	opts, runner = newISOInspectTestOptionsAtRoot(t, string(filepath.Separator))
	runner.inspect[0]["Mounts"] = []any{map[string]any{
		"Type": "bind", "Source": "/dev/null", "Destination": "/device",
	}}
	assertISOInspectionFailure(t, opts, "device")
}

func TestInspectISOProjectRejectsRuntimeBindChangedToNamespaceHandle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "namespace-handle")
	if err := os.WriteFile(source, []byte("handle"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	oldInspect := inspectISONamespaceHostSource
	inspectISONamespaceHostSource = func(path string) (bool, error) {
		return path == resolvedSource, nil
	}
	t.Cleanup(func() { inspectISONamespaceHostSource = oldInspect })
	opts, runner := newISOInspectTestOptionsAtRoot(t, root)
	runner.inspect[0]["Mounts"] = []any{map[string]any{
		"Type": "bind", "Source": source, "Destination": "/namespace",
	}}
	assertISOInspectionFailure(t, opts, "namespace")
}

func TestDefaultISOInspectRunnerBuildsExactDockerArguments(t *testing.T) {
	projectDir := t.TempDir()
	composeFiles := []string{filepath.Join(projectDir, "compose.yml"), filepath.Join(projectDir, "iso-network.yml")}
	calls := []cmdCall{}
	runner := defaultISOInspectRunner(ISOInspectOptions{
		ProjectName:  "catch-app",
		ProjectDir:   projectDir,
		ComposeFiles: composeFiles,
		NewCmd:       recordCmdContext(t, recordCmd(t, &calls)),
	})

	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "[]")
	if _, err := runner(context.Background(), "compose-ps"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner(context.Background(), "inspect", "cid-api", "cid-worker"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"compose", "--project-name", "catch-app", "--project-directory", projectDir, "--file", composeFiles[0], "--file", composeFiles[1], "ps", "--format", "json", "--no-trunc"},
		{"inspect", "cid-api", "cid-worker"},
	}
	if len(calls) != len(want) {
		t.Fatalf("Docker calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if !slices.Equal(calls[i].args, want[i]) {
			t.Fatalf("Docker call %d args = %#v, want %#v", i, calls[i].args, want[i])
		}
	}
}

func TestDefaultISOInspectRunnerWrapsCommandErrors(t *testing.T) {
	calls := []cmdCall{}
	runner := defaultISOInspectRunner(ISOInspectOptions{
		ProjectName: "catch-app",
		NewCmd:      recordCmdContext(t, recordCmd(t, &calls)),
	})
	t.Setenv("HELPER_DOCKER_FAIL_SUBCOMMAND", "ps")

	_, err := runner(context.Background(), "compose-ps")
	if err == nil || !strings.Contains(err.Error(), "docker compose-ps") || !strings.Contains(err.Error(), "exit status 12") {
		t.Fatalf("runner error = %v, want operation and wrapped command failure", err)
	}
}

func FuzzInspectISOProjectJSON(f *testing.F) {
	root := f.TempDir()
	f.Add(
		`[{"ID":"cid-api","Service":"api","State":"running"}]`,
		`[{"Id":"cid-api","State":{"Running":true}}]`,
	)
	f.Add(`{"not":"an array"}`, `null`)
	f.Add(`[{`, `[{`)
	f.Fuzz(func(t *testing.T, psJSON, inspectJSON string) {
		opts := ISOInspectOptions{
			ProjectName: "catch-app",
			NetworkName: "catch-app_default",
			ServiceRoot: root,
			Components:  map[string]netip.Addr{"api": netip.MustParseAddr("172.30.128.2")},
		}
		opts.run = func(_ context.Context, operation string, _ ...string) ([]byte, error) {
			if operation == "compose-ps" {
				return []byte(psJSON), nil
			}
			return []byte(inspectJSON), nil
		}
		inspection, err := InspectISOProject(context.Background(), opts)
		if err == nil {
			_ = inspection.Verify()
		}
	})
}

func FuzzValidateISOHostSource(f *testing.F) {
	root := f.TempDir()
	for _, dir := range []string{"run", "bin", "tailscale", "data"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			f.Fatal(err)
		}
	}
	for _, source := range []string{
		filepath.Join(root, "data"),
		filepath.Join(root, "run"),
		filepath.Dir(root),
		"relative/path",
		"",
	} {
		f.Add(source)
	}
	f.Fuzz(func(_ *testing.T, source string) {
		_, _ = ValidateISOHostSource(root, source)
	})
}

func TestInspectISOProjectRejectsRuntimeMountEscapes(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"run", "bin", "tailscale", "data"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "bin", "tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, source, want string
	}{
		{name: "service root parent", source: root, want: "host-managed"},
		{name: "direct run", source: filepath.Join(root, "run"), want: "host-managed"},
		{name: "inside bin", source: filepath.Join(root, "bin", "tool"), want: "host-managed"},
		{name: "direct tailscale", source: filepath.Join(root, "tailscale"), want: "host-managed"},
		{name: "outside root", source: filepath.Dir(root), want: "outside"},
		{name: "host control socket", source: "/var/run/docker.sock", want: "host-control"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, runner := newISOInspectTestOptionsAtRoot(t, root)
			runner.inspect[0]["Mounts"] = []any{map[string]any{"Type": "bind", "Source": tt.source, "Destination": "/host"}}
			assertISOInspectionFailure(t, opts, tt.want)
		})
	}
}

type isoInspectTestRunner struct {
	ps      []map[string]any
	inspect []map[string]any
	calls   []string
	err     map[string]error
}

func (r *isoInspectTestRunner) run(_ context.Context, operation string, _ ...string) ([]byte, error) {
	r.calls = append(r.calls, operation)
	if err := r.err[operation]; err != nil {
		return nil, err
	}
	switch operation {
	case "compose-ps":
		return json.Marshal(r.ps)
	case "inspect":
		return json.Marshal(r.inspect)
	default:
		return nil, fmt.Errorf("unexpected ISO inspection operation %q", operation)
	}
}

func newISOInspectTestOptions(t *testing.T) (ISOInspectOptions, *isoInspectTestRunner) {
	t.Helper()
	return newISOInspectTestOptionsAtRoot(t, t.TempDir())
}

func newISOInspectTestOptionsAtRoot(t *testing.T, root string) (ISOInspectOptions, *isoInspectTestRunner) {
	t.Helper()
	runner := &isoInspectTestRunner{
		ps: []map[string]any{{"ID": "cid-api", "Name": "catch-app-api-1", "Service": "api", "State": "running"}},
		inspect: []map[string]any{{
			"Id": "cid-api", "Name": "/catch-app-api-1",
			"State":  map[string]any{"Running": true},
			"Config": map[string]any{"Labels": map[string]any{"com.docker.compose.project": "catch-app", "com.docker.compose.service": "api"}},
			"HostConfig": map[string]any{
				"NetworkMode": "catch-app_default", "PidMode": "", "IpcMode": "", "CgroupnsMode": "private", "UTSMode": "", "UsernsMode": "",
				"Privileged": false, "CapAdd": nil, "Devices": nil, "DeviceCgroupRules": nil, "DeviceRequests": nil,
				"PortBindings": map[string]any{}, "SecurityOpt": nil,
			},
			"NetworkSettings": map[string]any{
				"Networks": map[string]any{"catch-app_default": map[string]any{"IPAddress": "172.30.128.2", "GlobalIPv6Address": ""}},
				"Ports":    map[string]any{},
			},
			"Mounts": []any{},
		}},
		err: map[string]error{},
	}
	return ISOInspectOptions{
		ProjectName: "catch-app",
		NetworkName: "catch-app_default",
		ServiceRoot: root,
		Components:  map[string]netip.Addr{"api": netip.MustParseAddr("172.30.128.2")},
		run:         runner.run,
	}, runner
}

func assertISOInspectionFailure(t *testing.T, opts ISOInspectOptions, want string) {
	t.Helper()
	inspection, inspectErr := InspectISOProject(context.Background(), opts)
	err := errors.Join(inspectErr, inspection.Verify())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("inspection error = %v, want finding containing %q", err, want)
	}
}
