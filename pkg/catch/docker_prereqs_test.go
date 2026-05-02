// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestDockerNetNSServiceUnitsSelectsCurrentDockerComposeNetNSUnits(t *testing.T) {
	s := newTestServer(t)
	addTestServices(t, s,
		db.Service{
			Name:        "zeta",
			ServiceType: db.ServiceTypeDockerCompose,
			Generation:  2,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(2): "/root/data/services/zeta/bin/yeet-zeta-ns-20250501010101.service"}},
			},
		},
		db.Service{
			Name:        "alpha",
			ServiceType: db.ServiceTypeDockerCompose,
			Generation:  1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/root/data/services/alpha/bin/yeet-alpha-ns-20250501010101.service"}},
			},
		},
		db.Service{
			Name:        "plain-compose",
			ServiceType: db.ServiceTypeDockerCompose,
			Generation:  1,
		},
		db.Service{
			Name:        "systemd-netns",
			ServiceType: db.ServiceTypeSystemd,
			Generation:  1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/etc/systemd/system/yeet-systemd-netns-ns.service"}},
			},
		},
	)

	got, err := s.dockerNetNSServiceUnits()
	if err != nil {
		t.Fatalf("dockerNetNSServiceUnits returned error: %v", err)
	}
	want := []string{"yeet-alpha-ns.service", "yeet-zeta-ns.service"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("unexpected units (-want +got):\n%s", diff)
	}
}

func TestDockerPrereqsTargetContentIncludesServiceNetNSUnits(t *testing.T) {
	got := dockerPrereqsTargetContent([]string{"yeet-zeta-ns.service", "yeet-alpha-ns.service"})
	for _, want := range []string{
		"Description=Yeet Docker network prerequisites\n",
		"Wants=catch.service yeet-ns.service yeet-alpha-ns.service yeet-zeta-ns.service\n",
		"After=catch.service yeet-ns.service yeet-alpha-ns.service yeet-zeta-ns.service\n",
		"Before=docker.service\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("target missing %q:\n%s", want, got)
		}
	}
}

func TestDockerPrereqsInstallerWritesTargetAndDockerDropIn(t *testing.T) {
	root := t.TempDir()
	var calls [][]string
	installer := dockerPrereqsInstaller{
		root: root,
		runSystemctl: func(args ...string) error {
			calls = append(calls, append([]string(nil), args...))
			return nil
		},
	}

	if err := installer.install([]string{"yeet-plex-ns.service"}); err != nil {
		t.Fatalf("install returned error: %v", err)
	}

	targetRaw, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/yeet-docker-prereqs.target"))
	if err != nil {
		t.Fatalf("read target returned error: %v", err)
	}
	if !strings.Contains(string(targetRaw), "Wants=catch.service yeet-ns.service yeet-plex-ns.service\n") {
		t.Fatalf("target missing service unit:\n%s", string(targetRaw))
	}
	dropInRaw, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/docker.service.d/yeet.conf"))
	if err != nil {
		t.Fatalf("read docker drop-in returned error: %v", err)
	}
	for _, want := range []string{
		"Wants=yeet-docker-prereqs.target\n",
		"After=yeet-docker-prereqs.target\n",
	} {
		if !strings.Contains(string(dropInRaw), want) {
			t.Fatalf("drop-in missing %q:\n%s", want, string(dropInRaw))
		}
	}
	if diff := cmp.Diff([][]string{{"daemon-reload"}}, calls); diff != "" {
		t.Fatalf("unexpected systemctl calls (-want +got):\n%s", diff)
	}
}
