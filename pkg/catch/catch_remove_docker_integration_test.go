//go:build integration

// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

// Run explicitly on a Linux Docker host. Missing prerequisites fail; the
// isolated server does not replace or start the host's installed catch daemon.
func TestRemoveRunningDockerServiceLive(t *testing.T) {
	for _, clean := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "clean"}[clean], func(t *testing.T) {
			testRemoveRunningDockerServiceLive(t, clean)
		})
	}
}

func testRemoveRunningDockerServiceLive(t *testing.T, clean bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return out
	}
	run("info")
	run("compose", "version")
	server := newTestServer(t)
	name := fmt.Sprintf("rm-live-%d", time.Now().UnixNano())
	project := "catch-" + name
	root := filepath.Join(server.cfg.ServicesRoot, name)
	for _, dir := range []string{"data", "run"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "run", "compose.yml")
	if err := os.WriteFile(path, []byte("services:\n  app:\n    image: alpine:3.22\n    command: [sleep, infinity]\n    stop_grace_period: 1s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedService(t, server, name, db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): path}},
	})
	t.Cleanup(func() {
		out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+project).Output()
		if err == nil && len(strings.Fields(string(out))) > 0 {
			exec.Command("docker", append([]string{"rm", "--force"}, strings.Fields(string(out))...)...).Run()
		}
		exec.Command("docker", "network", "rm", project+"_default").Run()
	})
	run("compose", "-p", project, "-f", path, "up", "-d")
	if out := run("ps", "-q", "--filter", "label=com.docker.compose.project="+project); len(bytes.TrimSpace(out)) == 0 {
		t.Fatal("fixture did not start")
	}
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()
	host, portString, err := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"remove", "--yes"}
	if clean {
		args = append(args, "--clean-data")
	}
	var output bytes.Buffer
	code, err := catchrpc.NewClient(host, port).Exec(ctx, catchrpc.ExecRequest{Service: name, Args: args}, nil, &output, nil)
	if err != nil || code != 0 || strings.Contains(output.String(), "warning:") {
		t.Fatalf("remove running service: code=%d err=%v output=%s", code, err, &output)
	}
	t.Logf("remove running service clean=%v: %s", clean, &output)
	if out := run("ps", "-aq", "--filter", "label=com.docker.compose.project="+project); len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("containers remain: %s", out)
	}
	if _, err := server.serviceView(name); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("service record remains: %v", err)
	}
	_, err = os.Stat(filepath.Join(root, "data"))
	if clean && !errors.Is(err, os.ErrNotExist) || !clean && err != nil {
		t.Fatalf("data cleanup clean=%v: %v", clean, err)
	}
}
