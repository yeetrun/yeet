// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestSystemdUnitRendersExplicitDependencies(t *testing.T) {
	unit := SystemdUnit{
		Name:       "catch",
		Executable: "/usr/local/bin/catch",
		Wants:      "containerd.service",
		Requires:   "local-fs.target",
		After:      "containerd.service local-fs.target",
		Before:     "yeet-docker-prereqs.target docker.service",
		ExecStartPre: []string{
			"/bin/systemctl is-active --quiet yeet-demo-ns.service",
		},
		ExecStartPost: []string{
			"/bin/sh -c 'i=0; while [ \"$i\" -lt 600 ]; do [ -S /run/docker/plugins/yeet.sock ] && exit 0; i=$((i+1)); sleep 0.1; done; exit 1'",
		},
		WantedBy: "multi-user.target yeet-docker-prereqs.target",
	}

	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Wants=containerd.service\n",
		"Requires=local-fs.target\n",
		"After=containerd.service local-fs.target\n",
		"Before=yeet-docker-prereqs.target docker.service\n",
		"ExecStartPre=/bin/systemctl is-active --quiet yeet-demo-ns.service\n",
		"ExecStartPost=/bin/sh -c 'i=0; while [ \"$i\" -lt 600 ]; do [ -S /run/docker/plugins/yeet.sock ] && exit 0; i=$((i+1)); sleep 0.1; done; exit 1'\n",
		"WantedBy=multi-user.target yeet-docker-prereqs.target\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}

func TestSystemdUnitDefaultsAfterToRequires(t *testing.T) {
	unit := SystemdUnit{
		Name:       "demo",
		Executable: "/usr/local/bin/demo",
		Requires:   "network-online.target",
	}

	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "After=network-online.target\n") {
		t.Fatalf("unit did not preserve After=Requires default:\n%s", got)
	}
}

func TestSystemdServiceInstallPlanOrdersArtifactsAndPrimaryTimer(t *testing.T) {
	tmp := t.TempDir()
	cfg := db.Service{
		Name:       "demo",
		Generation: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:      testArtifact("svc"),
			db.ArtifactSystemdTimerFile: testArtifact("timer"),
			db.ArtifactNetNSService:     testArtifact("netns"),
			db.ArtifactNetNSEnv:         testArtifact("netns-env"),
			db.ArtifactTSService:        testArtifact("ts"),
			db.ArtifactTSEnv:            testArtifact("ts-env"),
			db.ArtifactTSBinary:         testArtifact("ts-bin"),
			db.ArtifactTSConfig:         testArtifact("ts-config"),
			db.ArtifactBinary:           testArtifact("bin"),
			db.ArtifactEnvFile:          testArtifact("env"),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: tmp}

	plan := svc.installPlan()

	gotArtifacts := make([]db.ArtifactName, 0, len(plan))
	for _, step := range plan {
		gotArtifacts = append(gotArtifacts, step.artifact)
	}
	wantArtifacts := []db.ArtifactName{
		db.ArtifactSystemdUnit,
		db.ArtifactSystemdTimerFile,
		db.ArtifactNetNSService,
		db.ArtifactNetNSEnv,
		db.ArtifactBinary,
		db.ArtifactTypeScriptFile,
		db.ArtifactPythonFile,
		db.ArtifactEnvFile,
		db.ArtifactTSService,
		db.ArtifactTSEnv,
		db.ArtifactTSBinary,
		db.ArtifactTSConfig,
	}
	if diff := cmp.Diff(wantArtifacts, gotArtifacts); diff != "" {
		t.Fatalf("install artifact order mismatch (-want +got):\n%s", diff)
	}

	gotUnits := enabledUnitsForInstallPlan(plan, cfg.Artifacts, cfg.Generation)
	wantUnits := []string{"demo.timer", "yeet-demo-ns.service", "yeet-demo-ts.service"}
	if diff := cmp.Diff(wantUnits, gotUnits); diff != "" {
		t.Fatalf("enabled units mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdServiceAuxiliaryCleanupPlansAreOptionalAndOrdered(t *testing.T) {
	cfg := db.Service{
		Name:       "demo",
		Generation: 7,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:  testArtifact("svc"),
			db.ArtifactNetNSService: testArtifact("netns"),
			db.ArtifactTSService:    testArtifact("ts"),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: t.TempDir()}

	wantStop := []string{"demo.service", "yeet-demo-ts.service", "yeet-demo-ns.service"}
	if diff := cmp.Diff(wantStop, svc.stopUnits()); diff != "" {
		t.Fatalf("stop units mismatch (-want +got):\n%s", diff)
	}

	wantUninstall := []string{"demo.service", "yeet-demo-ns.service", "yeet-demo-ts.service"}
	if diff := cmp.Diff(wantUninstall, svc.uninstallDisableUnits()); diff != "" {
		t.Fatalf("uninstall disable units mismatch (-want +got):\n%s", diff)
	}
}

func testArtifact(name string) *db.Artifact {
	return &db.Artifact{
		Refs: map[db.ArtifactRef]string{
			db.Gen(3): "/tmp/" + name + "-3",
			db.Gen(7): "/tmp/" + name + "-7",
		},
	}
}
