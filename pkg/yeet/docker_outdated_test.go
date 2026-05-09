// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
)

func preserveDockerOutdatedGlobals(t *testing.T) {
	t.Helper()
	preserveSvcCommandGlobals(t)
	oldFetchDockerOutdated := fetchDockerOutdatedForHostFn
	t.Cleanup(func() {
		fetchDockerOutdatedForHostFn = oldFetchDockerOutdated
	})
}

func TestFetchDockerOutdatedForHost(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		if host != "host-a" || service != systemServiceName || !reflect.DeepEqual(args, []string{"docker", "outdated", "--format=json"}) {
			t.Fatalf("execRemoteOutputFn = (%q, %q, %#v)", host, service, args)
		}
		return []byte(`[{"serviceName":"web","containerName":"app","image":"ghcr.io/acme/app:latest","runningDigest":"sha256:old","latestDigest":"sha256:new","status":"update available"}]`), nil
	}
	rows, err := fetchDockerOutdatedForHost(context.Background(), "host-a", "", cli.DockerOutdatedFlags{})
	if err != nil {
		t.Fatalf("fetchDockerOutdatedForHost: %v", err)
	}
	if len(rows) != 1 || rows[0].ServiceName != "web" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestDockerOutdatedMultiHostJSON(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		return []dockerOutdatedRow{{ServiceName: "svc-" + host, ContainerName: "app", Status: "update available"}}, nil
	}
	out, err := captureSvcStdout(t, func() error {
		return dockerOutdatedMultiHost(context.Background(), []string{"host-b", "host-a"}, "", cli.DockerOutdatedFlags{Format: "json-pretty"})
	})
	if err != nil {
		t.Fatalf("dockerOutdatedMultiHost: %v", err)
	}
	var decoded []dockerOutdatedHostData
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(decoded) != 2 || decoded[0].Host != "host-a" || decoded[1].Host != "host-b" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestRenderDockerOutdatedTables(t *testing.T) {
	results := []dockerOutdatedHostData{{
		Host: "host-a",
		Rows: []dockerOutdatedRow{{
			ServiceName:   "web",
			ContainerName: "app",
			Image:         "ghcr.io/acme/app:latest",
			RunningDigest: "sha256:old",
			LatestDigest:  "sha256:new",
			Status:        "update available",
		}, {
			ServiceName: "api",
			Status:      "error",
			Reason:      "scan failed",
		}},
	}}
	var out bytes.Buffer
	if err := renderDockerOutdatedTables(&out, results); err != nil {
		t.Fatalf("render: %v", err)
	}
	got := out.String()
	for _, want := range []string{"SERVICE", "HOST", "web", "host-a", "update available"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	foundErrorRow := false
	for _, line := range strings.Split(got, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || fields[0] != "api" {
			continue
		}
		foundErrorRow = true
		if fields[3] != "-" {
			t.Fatalf("error row image = %q, want -\n%s", fields[3], got)
		}
		if strings.Join(fields[6:], " ") != "error: scan failed" {
			t.Fatalf("error row status = %q, want error: scan failed\n%s", strings.Join(fields[6:], " "), got)
		}
	}
	if !foundErrorRow {
		t.Fatalf("error row missing:\n%s", got)
	}
}

func TestDockerOutdatedMultiHostReturnsFetchError(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		return nil, errors.New("host failed")
	}
	if err := dockerOutdatedMultiHost(context.Background(), []string{"host-a"}, "", cli.DockerOutdatedFlags{}); err == nil || !strings.Contains(err.Error(), "host failed") {
		t.Fatalf("error = %v, want host failed", err)
	}
}

func TestDockerOutdatedMultiHostWaitsForCancelOnFetchError(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	var canceled int32
	started := make(chan string, 3)
	finished := make(chan string, 3)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		started <- host
		if host == "host-a" {
			return nil, errors.New("host failed")
		}
		<-ctx.Done()
		atomic.AddInt32(&canceled, 1)
		finished <- host
		return nil, ctx.Err()
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- dockerOutdatedMultiHost(context.Background(), []string{"host-a", "host-b", "host-c"}, "", cli.DockerOutdatedFlags{})
	}()

	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for all fetches to start")
		}
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("dockerOutdatedMultiHost error = nil, want fetch error")
		}
	case <-time.After(time.Second):
		t.Fatal("dockerOutdatedMultiHost did not return after canceling in-flight fetches")
	}
	for range 2 {
		select {
		case <-finished:
		default:
			t.Fatalf("dockerOutdatedMultiHost returned before canceled fetches finished; canceled=%d", atomic.LoadInt32(&canceled))
		}
	}
}

func TestDockerOutdatedMultiHostRejectsInvalidFormat(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		t.Fatalf("invalid format should fail before fetching host %q", host)
		return nil, nil
	}
	err := dockerOutdatedMultiHost(context.Background(), []string{"host-a"}, "", cli.DockerOutdatedFlags{Format: "xml"})
	if err == nil || !strings.Contains(err.Error(), `unsupported docker outdated format "xml"`) {
		t.Fatalf("invalid format error = %v", err)
	}
}

func TestHandleSvcCommandDockerOutdatedRejectsConflictingServiceArgs(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	serviceOverride = "svc-a"
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		t.Fatalf("conflicting service args should fail before fetch")
		return nil, nil
	}
	err := handleSvcCommand(context.Background(), svcCommandRequest{
		Command: svcCommand{Name: "docker", Args: []string{"outdated", "svc-b"}, RawArgs: []string{"docker", "outdated", "svc-b"}},
		Config:  nil,
		Service: "svc-a",
	})
	if err == nil || !strings.Contains(err.Error(), "at most one service argument") {
		t.Fatalf("conflicting service error = %v", err)
	}
}

func TestHandleSvcCmdDockerOutdatedRejectsUnbridgedPositionalService(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	useTempSvcCwd(t)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		t.Fatalf("unbridged positional service should fail before fetch")
		return nil, nil
	}
	err := HandleSvcCmd([]string{"docker", "outdated", "svc-b"})
	if err == nil || !strings.Contains(err.Error(), "positional service arguments") {
		t.Fatalf("unbridged positional service error = %v", err)
	}
}

func TestHandleSvcCommandDockerOutdatedScopedJSONUsesRemoteExec(t *testing.T) {
	for _, format := range []string{"json", "json-pretty"} {
		t.Run(format, func(t *testing.T) {
			preserveDockerOutdatedGlobals(t)
			serviceOverride = "svc-a"
			fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
				t.Fatalf("scoped %s output should use remote exec, not local fetch", format)
				return nil, nil
			}
			called := false
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				called = true
				if service != "svc-a" {
					t.Fatalf("execRemoteFn service = %q, want svc-a", service)
				}
				wantArgs := []string{"docker", "outdated", "--format=" + format}
				if !reflect.DeepEqual(args, wantArgs) {
					t.Fatalf("execRemoteFn args = %#v, want %#v", args, wantArgs)
				}
				return nil
			}
			err := handleSvcCommand(context.Background(), svcCommandRequest{
				Command: svcCommand{Name: "docker", Args: []string{"outdated", "--format=" + format}, RawArgs: []string{"docker", "outdated", "--format=" + format}},
				Config:  nil,
				Service: "svc-a",
			})
			if err != nil {
				t.Fatalf("handleSvcCommand scoped %s: %v", format, err)
			}
			if !called {
				t.Fatal("execRemoteFn was not called")
			}
		})
	}
}

func TestHandleSvcCommandDockerOutdatedInterceptsLocalTable(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("docker outdated table should be handled locally, got remote exec service=%q args=%v", service, args)
		return nil
	}
	called := false
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		called = true
		if service != "" {
			t.Fatalf("unscoped docker outdated service = %q, want empty", service)
		}
		return []dockerOutdatedRow{{ServiceName: "web", ContainerName: "app", Status: "update available"}}, nil
	}
	_, err := captureSvcStdout(t, func() error {
		return handleSvcCommand(context.Background(), svcCommandRequest{
			Command: svcCommand{Name: "docker", Args: []string{"outdated"}, RawArgs: []string{"docker", "outdated"}},
			Config:  nil,
		})
	})
	if err != nil {
		t.Fatalf("handleSvcCommand docker outdated: %v", err)
	}
	if !called {
		t.Fatal("fetchDockerOutdatedForHostFn was not called")
	}
}
