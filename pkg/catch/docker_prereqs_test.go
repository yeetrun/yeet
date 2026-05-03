// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
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

	if err := installer.install([]string{"yeet-media-ns.service"}); err != nil {
		t.Fatalf("install returned error: %v", err)
	}

	targetRaw, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/yeet-docker-prereqs.target"))
	if err != nil {
		t.Fatalf("read target returned error: %v", err)
	}
	if !strings.Contains(string(targetRaw), "Wants=catch.service yeet-ns.service yeet-media-ns.service\n") {
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

func TestWriteTextFileAtomicallyReplacesContentAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unit.conf")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := writeTextFileAtomically(path, []byte("new"), 0644); err != nil {
		t.Fatalf("writeTextFileAtomically returned error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(raw) != "new" {
		t.Fatalf("file content = %q, want new", string(raw))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("file mode = %v, want 0644", got)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "unit.conf.tmp.*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files were not cleaned up: %v", matches)
	}
}

func TestDockerPrereqsInstallerSkipsReloadWhenFilesUnchanged(t *testing.T) {
	root := t.TempDir()
	var calls [][]string
	installer := dockerPrereqsInstaller{
		root: root,
		runSystemctl: func(args ...string) error {
			calls = append(calls, append([]string(nil), args...))
			return nil
		},
	}

	if err := installer.install([]string{"yeet-api-ns.service"}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	calls = nil
	if err := installer.install([]string{"yeet-api-ns.service"}); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("systemctl calls on unchanged install = %#v, want none", calls)
	}
}

func TestDockerPrereqsInstallerReturnsReloadError(t *testing.T) {
	reloadErr := errors.New("reload failed")
	installer := dockerPrereqsInstaller{
		root: t.TempDir(),
		runSystemctl: func(args ...string) error {
			return reloadErr
		},
	}

	err := installer.install([]string{"yeet-api-ns.service"})
	if !errors.Is(err, reloadErr) {
		t.Fatalf("install error = %v, want reload error", err)
	}
}

func TestDockerPrereqsInstallerPathDefaultsToRoot(t *testing.T) {
	got := (dockerPrereqsInstaller{}).path("etc", "systemd")
	if got != "/etc/systemd" {
		t.Fatalf("path = %q, want /etc/systemd", got)
	}
}

func TestWriteTextFileIfChangedDetectsNoChangeAndReadErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unit.conf")
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	changed, err := writeTextFileIfChanged(path, "same", 0o644)
	if err != nil {
		t.Fatalf("writeTextFileIfChanged: %v", err)
	}
	if changed {
		t.Fatal("unchanged file reported changed")
	}

	if _, err := textFileContentMatches(dir, []byte("x")); err == nil {
		t.Fatal("expected read error for directory path")
	}
}

func TestAtomicTextFileCleanupClosesAndRemovesTempOnError(t *testing.T) {
	dir := t.TempDir()
	tmp, err := os.CreateTemp(dir, "unit.conf.tmp.")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	tmpName := tmp.Name()
	writeErr := errors.New("write failed")
	atomicFile := &atomicTextFile{path: filepath.Join(dir, "unit.conf"), tmpName: tmpName, file: tmp}

	atomicFile.cleanup(&writeErr)

	if !atomicFile.closed {
		t.Fatal("cleanup did not close temp file")
	}
	if _, err := os.Stat(tmpName); !os.IsNotExist(err) {
		t.Fatalf("temp stat err = %v, want not exist", err)
	}
}

func TestSortedUniqueUnitsDropsEmptyAndDeduplicates(t *testing.T) {
	got := sortedUniqueUnits([]string{"b.service", "", "a.service", "b.service"})
	want := []string{"a.service", "b.service"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("sortedUniqueUnits mismatch (-want +got):\n%s", diff)
	}
	if got := sortedUniqueUnits(nil); got != nil {
		t.Fatalf("empty units = %#v, want nil", got)
	}
}
