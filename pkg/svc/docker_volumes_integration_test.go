//go:build integration

// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

// Run explicitly with a local Docker daemon. Missing prerequisites fail rather
// than skipping the lifecycle. Every cleanup names only this fixture's resources.
func TestDockerVolumeRemovalLive(t *testing.T) {
	for _, mode := range []string{"preserve", "clean", "retry", "iso"} {
		t.Run(mode, func(t *testing.T) { testDockerVolumeRemovalLive(t, mode) })
	}
}

func testDockerVolumeRemovalLive(t *testing.T, mode string) {
	t.Helper()
	docker, err := DockerCmd()
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return out
	}
	run("info")
	run("compose", "version")
	name := fmt.Sprintf("volume-test-%s-%d", mode, time.Now().UnixNano())
	project := "catch-" + name
	external, outsider := project+"-external", project+"-outsider"
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bindDir := t.TempDir()
	sentinel := filepath.Join(bindDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(root, "compose.yml")
	compose := fmt.Sprintf(`services:
  app:
    image: alpine:3.22
    command: [sleep, infinity]
    stop_grace_period: 1s
    volumes:
      - data:/data
      - shared:/shared
      - external:/external
      - /anonymous
      - %s:/bind
volumes:
  data: {}
  shared: {}
  external:
    external: true
    name: %s
`, bindDir, external)
	if err := os.WriteFile(composePath, []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &DockerComposeService{Name: name, DataDir: dataDir, RunDir: runDir, cfg: &db.Service{Name: name, Generation: 1, Artifacts: db.ArtifactStore{db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): composePath}}}}}
	composeArgs := []string{"compose", "-p", project, "-f", composePath}
	var volumes []string
	t.Cleanup(func() {
		// Remove the stopped fixture container first so its shared volume is released.
		exec.Command(docker, "rm", "--force", outsider).Run()
		exec.Command(docker, append(slices.Clone(composeArgs), "down", "--remove-orphans")...).Run()
		for _, volume := range volumes {
			exec.Command(docker, "volume", "rm", volume).Run()
		}
		remaining, err := exec.Command(docker, "volume", "ls", "-q").Output()
		if err != nil {
			t.Errorf("verify fixture volume cleanup: %v", err)
			return
		}
		for _, volume := range volumes {
			if slices.Contains(strings.Fields(string(remaining)), volume) {
				t.Errorf("fixture left volume %q", volume)
			}
		}
		containers, err := exec.Command(docker, "ps", "-aq", "--filter", "label="+composeProjectLabel+"="+project).Output()
		if err != nil || len(strings.TrimSpace(string(containers))) != 0 {
			t.Errorf("fixture left project containers: %s %v", containers, err)
		}
	})
	volumes = append(volumes, external, project+"_data", project+"_shared")
	run("volume", "create", "--label", composeProjectLabel+"="+project, "--label", "com.docker.compose.volume=external", external)
	run(append(slices.Clone(composeArgs), "up", "-d")...)
	ids, err := service.projectContainerIDs(context.Background())
	if err != nil || len(ids) != 1 {
		t.Fatalf("project containers: %v, %v", ids, err)
	}
	var containers []dockerVolumeContainer
	if err := json.Unmarshal(run("container", "inspect", ids[0]), &containers); err != nil {
		t.Fatal(err)
	}
	anonymous := ""
	for _, mount := range containers[0].Mounts {
		if mount.Destination == "/anonymous" {
			anonymous = mount.Name
			volumes = append(volumes, anonymous)
		}
	}
	if anonymous == "" {
		t.Fatal("fixture anonymous volume missing")
	}
	run("create", "--name", outsider, "-v", project+"_shared:/shared", "alpine:3.22", "sleep", "infinity")
	var outsiderBefore []dockerVolumeContainer
	if err := json.Unmarshal(run("container", "inspect", outsider), &outsiderBefore); err != nil {
		t.Fatal(err)
	}
	if mode == "retry" {
		service.NewCmdContext = func(ctx context.Context, path string, args ...string) *exec.Cmd {
			if len(args) > 1 && args[0] == "volume" && args[1] == "rm" {
				return exec.CommandContext(ctx, "sh", "-c", "exit 1")
			}
			return exec.CommandContext(ctx, path, args...)
		}
		if err := service.RemoveWithData(context.Background(), true); err == nil {
			t.Fatal("expected injected volume cleanup failure")
		}
		if _, err := os.Stat(filepath.Join(runDir, dockerVolumeRemovalFile)); err != nil {
			t.Fatalf("missing recovery plan: %v", err)
		}
		ids, err = service.projectContainerIDs(context.Background())
		if err != nil || len(ids) != 0 {
			t.Fatalf("expected containers gone before retry: %v %v", ids, err)
		}
		service.NewCmdContext = nil
	}
	if mode == "iso" {
		for _, step := range []func(context.Context) error{service.StopProjectWorkload, service.PrepareVolumeRemoval, service.StopProjectContainers, service.RemoveVolumes} {
			if err := step(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		if err := service.RemoveWithData(context.Background(), mode != "preserve"); err != nil {
			t.Fatal(err)
		}
	}
	remaining := strings.Fields(string(run("volume", "ls", "-q")))
	for _, volume := range []string{project + "_data", anonymous} {
		if exists := slices.Contains(remaining, volume); exists != (mode == "preserve") {
			t.Fatalf("volume %s exists=%v in %s mode", volume, exists, mode)
		}
	}
	for _, volume := range []string{project + "_shared", external} {
		if !slices.Contains(remaining, volume) {
			t.Fatalf("protected volume %s deleted", volume)
		}
	}
	var outsiderAfter []dockerVolumeContainer
	if err := json.Unmarshal(run("container", "inspect", outsider), &outsiderAfter); err != nil {
		t.Fatal(err)
	}
	if volumeTestJSON(outsiderBefore) != volumeTestJSON(outsiderAfter) {
		t.Fatal("other container's mounts changed")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("bind data changed: %s %v", data, err)
	}
	ids, err = service.projectContainerIDs(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("remaining project containers: %v %v", ids, err)
	}
	if mode != "preserve" {
		if err := service.RemoveWithData(context.Background(), true); err != nil {
			t.Fatalf("idempotent cleanup: %v", err)
		}
	}
	run("image", "inspect", "alpine:3.22")
}
