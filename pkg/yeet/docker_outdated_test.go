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
	"testing"

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
