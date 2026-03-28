// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

type fakeNetNSInspector struct {
	namedID    string
	namedErr   error
	containers []composeContainer
	projectErr error
}

func (f fakeNetNSInspector) NamedNetNSID(path string) (string, error) {
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
