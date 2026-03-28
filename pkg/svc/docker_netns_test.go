// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

type fakeNetNSInspector struct {
	linkNames  []string
	namedErr   error
	containers []composeContainer
	projectErr error
	namedCalls *int
}

func (f fakeNetNSInspector) NamedNetNSLinkNames(ctx context.Context, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.namedCalls != nil {
		*f.namedCalls++
	}
	return f.linkNames, f.namedErr
}

func (f fakeNetNSInspector) ProjectContainers(ctx context.Context, project string) ([]composeContainer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.containers, f.projectErr
}

func TestSelectNetNSContainers(t *testing.T) {
	project := "catch-demo"
	network := project + "_default"

	containers := []composeContainer{
		{ID: "app", NetworkEndpointIDs: map[string]string{network: "endpoint-app"}},
		{ID: "worker", NetworkEndpointIDs: map[string]string{network: "endpoint-worker", "extra": "endpoint-extra"}},
		{ID: "sidecar", NetworkEndpointIDs: map[string]string{"bridge": "endpoint-sidecar"}},
	}

	got := selectNetNSContainers(containers, network)
	if diff := cmp.Diff([]composeContainer{containers[0], containers[1]}, got); diff != "" {
		t.Fatalf("selected containers mismatch (-want +got):\n%s", diff)
	}
}

func TestNeedsNetNSRecreate(t *testing.T) {
	cases := []struct {
		name       string
		linkNames  []string
		containers []composeContainer
		want       bool
	}{
		{
			name:      "expected endpoint link present",
			linkNames: []string{"lo", "yv-abcd", "br0"},
			containers: []composeContainer{{
				ID:                 "app",
				NetworkEndpointIDs: map[string]string{"catch-svc-a_default": "abcd1234"},
			}},
			want: false,
		},
		{
			name:      "missing endpoint link requires recreate",
			linkNames: []string{"lo", "br0"},
			containers: []composeContainer{{
				ID:                 "app",
				NetworkEndpointIDs: map[string]string{"catch-svc-a_default": "abcd1234"},
			}},
			want: true,
		},
		{
			name:       "no selected containers",
			linkNames:  []string{"lo", "br0"},
			containers: nil,
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsNetNSRecreate(tc.linkNames, tc.containers, "catch-svc-a_default")
			if got != tc.want {
				t.Fatalf("needsNetNSRecreate() = %v, want %v", got, tc.want)
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
		linkNames: []string{"lo", "br0"},
		containers: []composeContainer{{
			ID:                 "app",
			NetworkEndpointIDs: map[string]string{"catch-svc-a_default": "abcd1234"},
		}},
	}

	restarted, err := svc.ReconcileNetNS(context.Background())
	if err != nil {
		t.Fatalf("ReconcileNetNS returned error: %v", err)
	}
	if !restarted {
		t.Fatal("expected recreate when endpoint link is stale")
	}
	if !composeCallHasSubcmd(calls, "up") {
		t.Fatalf("expected compose up command, got %#v", calls)
	}
	if !composeCallHasArg(calls, "up", "--force-recreate") {
		t.Fatalf("expected compose recreate flag, got %#v", calls)
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
		containers: []composeContainer{{ID: "app", NetworkEndpointIDs: map[string]string{"bridge": "bridge1234"}}},
		namedCalls: &namedCalls,
	}

	restarted, err := svc.ReconcileNetNS(context.Background())
	if err != nil {
		t.Fatalf("ReconcileNetNS returned error: %v", err)
	}
	if restarted {
		t.Fatal("expected no restart when no containers are on the managed network")
	}
	if namedCalls != 0 {
		t.Fatalf("NamedNetNSLinkNames called %d times, want 0", namedCalls)
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
		linkNames: []string{"lo", "br0"},
		containers: []composeContainer{{
			ID:                 "app",
			NetworkEndpointIDs: map[string]string{"catch-svc-a_default": "abcd1234"},
		}},
	}

	if err := svc.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}

	recreates := 0
	for _, call := range calls {
		if composeCallHasArg([]cmdCall{call}, "up", "--force-recreate") {
			recreates++
		}
	}
	if recreates != 1 {
		t.Fatalf("compose recreate called %d times, want 1; calls=%#v", recreates, calls)
	}
}

func TestDockerComposeServiceReconcileNetNSHonorsCanceledContext(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.cfg.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
		Refs: map[db.ArtifactRef]string{
			db.Gen(svc.cfg.Generation): "/etc/systemd/system/yeet-svc-a-ns.service",
		},
	}
	svc.sd = &SystemdService{cfg: svc.cfg.View(), runDir: svc.DataDir}
	svc.netnsInspector = fakeNetNSInspector{
		linkNames: []string{"lo", "br0"},
		containers: []composeContainer{{
			ID:                 "app",
			NetworkEndpointIDs: map[string]string{"catch-svc-a_default": "abcd1234"},
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	restarted, err := svc.ReconcileNetNS(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileNetNS error = %v, want context.Canceled", err)
	}
	if restarted {
		t.Fatal("expected no restart when context is canceled")
	}
	if composeCallHasSubcmd(calls, "up") {
		t.Fatalf("did not expect compose recreate command after cancellation, got %#v", calls)
	}
}

func TestDockerComposeServiceRestartStartsOnlyAuxiliaryUnits(t *testing.T) {
	tmp := t.TempDir()
	systemctlLog := filepath.Join(tmp, "systemctl.log")
	systemctlPath := filepath.Join(tmp, "systemctl")
	systemctlScript := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(systemctlLog) + "\nexit 0\n"
	if err := os.WriteFile(systemctlPath, []byte(systemctlScript), 0755); err != nil {
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
		linkNames: []string{"lo", "yv-abcd", "br0"},
		containers: []composeContainer{{
			ID:                 "app",
			NetworkEndpointIDs: map[string]string{"catch-svc-a_default": "abcd1234"},
		}},
	}

	if err := svc.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}

	logBytes, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatalf("failed to read systemctl log: %v", err)
	}
	logOutput := string(logBytes)
	if !strings.Contains(logOutput, "start yeet-svc-a-ns.service") {
		t.Fatalf("expected netns unit start, got:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "start svc-a.service") {
		t.Fatalf("unexpected primary unit start for docker-compose service:\n%s", logOutput)
	}
	if !composeCallHasSubcmd(calls, "restart") {
		t.Fatalf("expected compose restart command, got %#v", calls)
	}
}

func TestLinuxNetNSInspectorNamedNetNSLinkNames(t *testing.T) {
	t.Setenv("HELPER_NSENTER_IP_LINK_OUTPUT", strings.Join([]string{
		"1: lo: <LOOPBACK,UP> mtu 65536 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000",
		"3: br0: <BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000",
		"284: yv-e091@if283: <BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue master br0 state UP mode DEFAULT group default qlen 1000",
	}, "\n")+"\n")
	_ = newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	got, err := (linuxNetNSInspector{}).NamedNetNSLinkNames(context.Background(), filepath.Join("/var/run/netns", "yeet-svc-a-ns"))
	if err != nil {
		t.Fatalf("NamedNetNSLinkNames returned error: %v", err)
	}
	want := []string{"lo", "br0", "yv-e091"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("NamedNetNSLinkNames mismatch (-want +got):\n%s", diff)
	}
}

func TestLinuxNetNSInspectorProjectContainers(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PSQ_OUTPUT", "cid-app\ncid-worker\n")
	t.Setenv(
		"HELPER_DOCKER_INSPECT_OUTPUT",
		strings.Join([]string{
			"cid-app\tcatch-svc-a_default=endpoint-app extra=endpoint-extra",
			"cid-worker\tbridge=endpoint-worker",
		}, "\n")+"\n",
	)

	_ = newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	got, err := (linuxNetNSInspector{}).ProjectContainers(context.Background(), "catch-svc-a")
	if err != nil {
		t.Fatalf("ProjectContainers returned error: %v", err)
	}

	want := []composeContainer{
		{ID: "cid-app", NetworkEndpointIDs: map[string]string{"catch-svc-a_default": "endpoint-app", "extra": "endpoint-extra"}},
		{ID: "cid-worker", NetworkEndpointIDs: map[string]string{"bridge": "endpoint-worker"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("ProjectContainers mismatch (-want +got):\n%s", diff)
	}
}

func TestLinuxNetNSInspectorProjectContainersHandlesContainerWithNoNetworks(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PSQ_OUTPUT", "cid-empty\n")
	t.Setenv("HELPER_DOCKER_INSPECT_OUTPUT", "cid-empty\t\n")

	_ = newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	got, err := (linuxNetNSInspector{}).ProjectContainers(context.Background(), "catch-svc-a")
	if err != nil {
		t.Fatalf("ProjectContainers returned error: %v", err)
	}

	want := []composeContainer{
		{ID: "cid-empty", NetworkEndpointIDs: map[string]string{}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("ProjectContainers mismatch (-want +got):\n%s", diff)
	}
}

func TestLinuxNetNSInspectorNamedNetNSLinkNamesHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (linuxNetNSInspector{}).NamedNetNSLinkNames(ctx, filepath.Join("/var/run/netns", "yeet-svc-a-ns"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("NamedNetNSLinkNames error = %v, want context.Canceled", err)
	}
}

func TestLinuxNetNSInspectorProjectContainersHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (linuxNetNSInspector{}).ProjectContainers(ctx, "catch-svc-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProjectContainers error = %v, want context.Canceled", err)
	}
}

func TestLinuxNetNSInspectorNamedNetNSLinkNamesReturnsContextErrorAfterStart(t *testing.T) {
	t.Setenv("HELPER_SLEEP_MS", "500")
	_ = newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := (linuxNetNSInspector{}).NamedNetNSLinkNames(ctx, filepath.Join("/var/run/netns", "yeet-svc-a-ns"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NamedNetNSLinkNames error = %v, want context deadline exceeded", err)
	}
}

func TestLinuxNetNSInspectorProjectContainersReturnsContextErrorAfterStart(t *testing.T) {
	t.Setenv("HELPER_SLEEP_MS", "500")
	_ = newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := (linuxNetNSInspector{}).ProjectContainers(ctx, "catch-svc-a")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ProjectContainers error = %v, want context deadline exceeded", err)
	}
}
