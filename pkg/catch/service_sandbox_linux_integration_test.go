// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServiceSandboxLinuxIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("service sandbox integration requires Linux")
	}
	deps := defaultBubblewrapDependencyDeps()
	trusted, err := inspectTrustedBubblewrap(deps)
	if err != nil {
		t.Skipf("service sandbox integration requires trusted /usr/bin/bwrap: %v", err)
	}
	if !trusted {
		t.Skip("service sandbox integration requires trusted /usr/bin/bwrap: binary is missing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if diagnostic, err := deps.run(ctx, bubblewrapPath, bubblewrapProbeArgs(os.Geteuid(), os.Getegid(), deps.pathPresent), nil); err != nil {
		t.Skipf("service sandbox integration requires usable user namespaces: %v; stderr: %s", err, string(diagnostic))
	}
	hostNetworkNamespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Skipf("service sandbox integration requires readable network namespace identity: %v", err)
	}

	root, err := os.MkdirTemp("/var/tmp", "yeet-sandbox-integration-")
	if err != nil {
		t.Skipf("service sandbox integration requires writable /var/tmp fixtures: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dataDir := filepath.Join(root, "data")
	writableSource := filepath.Join(root, "writable")
	for _, path := range []string{dataDir, writableSource} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	readOnlySource := filepath.Join(root, "read-only.txt")
	if err := os.WriteFile(readOnlySource, []byte("read-only fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "unmounted-sentinel")
	if err := os.WriteFile(sentinel, []byte("must remain hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const readOnlyDestination = "/sandbox/input/config"
	const writableDestination = "/sandbox/state"
	request := serviceSandboxPlanRequest{
		Service: "sandbox-integration", Hostname: "sandbox-integration",
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
		Payload: "/usr/bin/true", DataDir: dataDir, ResolverSource: "/etc/resolv.conf",
		Policy: serviceSandboxPolicy{State: "on",
			ReadOnly: []serviceSandboxExposure{{Source: readOnlySource, Destination: readOnlyDestination}},
			Writable: []serviceSandboxExposure{{Source: writableSource, Destination: writableDestination}},
		},
	}
	plan, err := buildServiceSandboxPlan(request)
	if err != nil {
		t.Fatalf("build integration plan: %v", err)
	}

	tmpName := "yeet-private-" + filepath.Base(root)
	script := strings.Join([]string{
		"set -eu",
		`test "$(pwd)" = "$1"`,
		`test "$HOME" = "$1"`,
		`printf data > "$1/from-sandbox"`,
		`printf private > "/tmp/$7"`,
		`test -r "$2"`,
		`if printf denied > "$2" 2>/dev/null; then exit 41; fi`,
		`printf persisted > "$3/persisted"`,
		`test ! -e "$4"`,
		`test ! -e "/proc/$5"`,
		`grep -q 'lo:' /proc/net/dev`,
		`printf '%s\n%s\n%s\n' "$(pwd)" "$HOME" "$(readlink /proc/self/ns/net)"`,
	}, "\n")
	args := append(append([]string(nil), plan.Arguments...),
		"/bin/sh", "-c", script, "sandbox-check",
		dataDir, readOnlyDestination, writableDestination, sentinel,
		strconv.Itoa(os.Getpid()), hostNetworkNamespace, tmpName,
	)
	command := exec.CommandContext(ctx, bubblewrapPath, args...)
	command.Env = append(os.Environ(), "HOME="+dataDir)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: request.UID, Gid: request.GID}}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("run integration policy: %v; stderr: %s", err, stderr.String())
	}

	wantOutput := fmt.Sprintf("%s\n%s\n%s\n", dataDir, dataDir, hostNetworkNamespace)
	if stdout.String() != wantOutput {
		t.Fatalf("sandbox identity output = %q, want %q", stdout.String(), wantOutput)
	}
	for path, want := range map[string]string{
		filepath.Join(dataDir, "from-sandbox"):     "data",
		filepath.Join(writableSource, "persisted"): "persisted",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("host result %s = %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join("/tmp", tmpName)); !os.IsNotExist(err) {
		t.Errorf("private sandbox /tmp leaked to host: %v", err)
	}
	if got, err := os.ReadFile(readOnlySource); err != nil || string(got) != "read-only fixture\n" {
		t.Errorf("read-only source changed: %q, %v", got, err)
	}
}
