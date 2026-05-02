// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"os"
	"strings"
	"testing"

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
