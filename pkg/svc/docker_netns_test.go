// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

type fakeNetNSInspector struct {
	namedID    string
	namedErr   error
	containers []composeContainer
	projectErr error
	namedCalls *int
}

func (f fakeNetNSInspector) NamedNetNSID(path string) (string, error) {
	if f.namedCalls != nil {
		*f.namedCalls++
	}
	return f.namedID, f.namedErr
}

func (f fakeNetNSInspector) ProjectContainers(project string) ([]composeContainer, error) {
	return f.containers, f.projectErr
}

func TestSelectNetNSContainers(t *testing.T) {
	project := "catch-demo"
	network := project + "_default"

	containers := []composeContainer{
		{ID: "app", PID: 101, Networks: []string{network}},
		{ID: "worker", PID: 202, Networks: []string{network, "extra"}},
		{ID: "sidecar", PID: 303, Networks: []string{"bridge"}},
	}

	got := selectNetNSContainers(containers, network)
	if diff := cmp.Diff([]composeContainer{containers[0], containers[1]}, got); diff != "" {
		t.Fatalf("selected containers mismatch (-want +got):\n%s", diff)
	}
}

func TestNeedsNetNSRestart(t *testing.T) {
	cases := []struct {
		name       string
		namedID    string
		containers []composeContainer
		want       bool
	}{
		{
			name:       "matching selected containers",
			namedID:    "net:[4026533001]",
			containers: []composeContainer{{ID: "app", NetNSID: "net:[4026533001]"}},
			want:       false,
		},
		{
			name:       "mismatch requires restart",
			namedID:    "net:[4026533009]",
			containers: []composeContainer{{ID: "app", NetNSID: "net:[4026533001]"}},
			want:       true,
		},
		{
			name:       "no selected containers",
			namedID:    "net:[4026533009]",
			containers: nil,
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsNetNSRestart(tc.namedID, tc.containers)
			if got != tc.want {
				t.Fatalf("needsNetNSRestart() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDockerComposeServiceReconcileNetNS(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.cfg.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
		Refs: map[db.ArtifactRef]string{
			db.Gen(svc.cfg.Generation): "/etc/systemd/system/yeet-svc-a-ns.service",
		},
	}
	svc.sd = &SystemdService{cfg: svc.cfg.View(), runDir: svc.DataDir}
	svc.netnsInspector = fakeNetNSInspector{
		namedID:    "net:[4026534010]",
		containers: []composeContainer{{ID: "app", PID: 1001, Networks: []string{"catch-svc-a_default"}, NetNSID: "net:[4026533010]"}},
	}

	restarted, err := svc.ReconcileNetNS()
	if err != nil {
		t.Fatalf("ReconcileNetNS returned error: %v", err)
	}
	if !restarted {
		t.Fatal("expected restart when container netns is stale")
	}
	if !composeCallHasSubcmd(calls, "restart") {
		t.Fatalf("expected compose restart command, got %#v", calls)
	}
}

func TestDockerComposeServiceReconcileNetNSNoSelectedContainersSkipsNamedLookup(t *testing.T) {
	namedCalls := 0
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	svc.cfg.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
		Refs: map[db.ArtifactRef]string{
			db.Gen(svc.cfg.Generation): "/etc/systemd/system/yeet-svc-a-ns.service",
		},
	}
	svc.sd = &SystemdService{cfg: svc.cfg.View(), runDir: svc.DataDir}
	svc.netnsInspector = fakeNetNSInspector{
		namedErr:   fmt.Errorf("named netns should not be read"),
		containers: []composeContainer{{ID: "app", PID: 1001, Networks: []string{"bridge"}, NetNSID: "net:[4026533010]"}},
		namedCalls: &namedCalls,
	}

	restarted, err := svc.ReconcileNetNS()
	if err != nil {
		t.Fatalf("ReconcileNetNS returned error: %v", err)
	}
	if restarted {
		t.Fatal("expected no restart when no containers are on the managed network")
	}
	if namedCalls != 0 {
		t.Fatalf("NamedNetNSID called %d times, want 0", namedCalls)
	}
}

func TestDockerComposeServiceRestartShortCircuitsAfterReconcileRestart(t *testing.T) {
	tmp := t.TempDir()
	systemctlPath := filepath.Join(tmp, "systemctl")
	if err := os.WriteFile(systemctlPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("failed to write fake systemctl: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")

	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.cfg.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
		Refs: map[db.ArtifactRef]string{
			db.Gen(svc.cfg.Generation): "/etc/systemd/system/yeet-svc-a-ns.service",
		},
	}
	svc.sd = &SystemdService{cfg: svc.cfg.View(), runDir: svc.DataDir}
	svc.netnsInspector = fakeNetNSInspector{
		namedID:    "net:[4026534010]",
		containers: []composeContainer{{ID: "app", PID: 1001, Networks: []string{"catch-svc-a_default"}, NetNSID: "net:[4026533010]"}},
	}

	if err := svc.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}

	restarts := 0
	for _, call := range calls {
		if len(call.args) > 0 && call.args[0] == "compose" && composeSubcommand(call.args) == "restart" {
			restarts++
		}
	}
	if restarts != 1 {
		t.Fatalf("compose restart called %d times, want 1; calls=%#v", restarts, calls)
	}
}

func TestLinuxNetNSInspectorNamedNetNSID(t *testing.T) {
	tmp := t.TempDir()
	handle := filepath.Join(tmp, "yeet-svc-a-ns")
	if err := os.WriteFile(handle, []byte("nsfs"), 0644); err != nil {
		t.Fatalf("failed to create fake netns handle: %v", err)
	}

	info, err := os.Stat(handle)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat payload type %T", info.Sys())
	}

	got, err := (linuxNetNSInspector{}).NamedNetNSID(handle)
	if err != nil {
		t.Fatalf("NamedNetNSID returned error: %v", err)
	}

	want := fmt.Sprintf("net:[%d]", stat.Ino)
	if got != want {
		t.Fatalf("NamedNetNSID() = %q, want %q", got, want)
	}
}

func TestLinuxNetNSInspectorProjectContainers(t *testing.T) {
	tmp := t.TempDir()
	procDir := filepath.Join(tmp, "proc")
	pidOne := 4242
	pidTwo := 5252
	netNSIDOne := "net:[4026533001]"
	netNSIDTwo := "net:[4026533002]"
	for pid, target := range map[int]string{
		pidOne: netNSIDOne,
		pidTwo: netNSIDTwo,
	} {
		nsDir := filepath.Join(procDir, strconv.Itoa(pid), "ns")
		if err := os.MkdirAll(nsDir, 0755); err != nil {
			t.Fatalf("failed to create fake proc dir: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(nsDir, "net")); err != nil {
			t.Fatalf("failed to create fake proc netns symlink: %v", err)
		}
	}

	t.Setenv("HELPER_DOCKER_PSQ_OUTPUT", "cid-app\ncid-worker\n")
	t.Setenv(
		"HELPER_DOCKER_INSPECT_OUTPUT",
		fmt.Sprintf(
			"cid-app\t%s\tcatch-svc-a_default extra\ncid-worker\t%s\tbridge\n",
			strconv.Itoa(pidOne),
			strconv.Itoa(pidTwo),
		),
	)

	_ = newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	oldProcNetNSPath := procNetNSPath
	procNetNSPath = func(pid int) string { return filepath.Join(procDir, strconv.Itoa(pid), "ns/net") }
	defer func() { procNetNSPath = oldProcNetNSPath }()

	got, err := (linuxNetNSInspector{}).ProjectContainers("catch-svc-a")
	if err != nil {
		t.Fatalf("ProjectContainers returned error: %v", err)
	}

	want := []composeContainer{
		{ID: "cid-app", PID: pidOne, Networks: []string{"catch-svc-a_default", "extra"}, NetNSID: netNSIDOne},
		{ID: "cid-worker", PID: pidTwo, Networks: []string{"bridge"}, NetNSID: netNSIDTwo},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("ProjectContainers mismatch (-want +got):\n%s", diff)
	}
}
