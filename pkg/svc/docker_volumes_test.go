// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type volumeTestDocker struct {
	service    *DockerComposeService
	volumes    map[string]dockerVolumeInfo
	containers bool
	shared     map[string]bool
	removed    []string
	commands   [][]string
	fail       string
}

func newVolumeTestDocker(t *testing.T) *volumeTestDocker {
	t.Helper()
	state := &volumeTestDocker{volumes: make(map[string]dockerVolumeInfo), shared: make(map[string]bool), containers: true}
	service := newTestDockerComposeService(t, "services: {}\n", nil)
	service.RunDir = filepath.Join(t.TempDir(), "run")
	service.NewCmdContext = state.command(t)
	state.service = service
	for _, name := range []string{"catch-svc-a_data", "catch-svc-a_shared", "catch-svc-a_external", "catch-svc-a_driver"} {
		state.volumes[name] = dockerVolumeInfo{Name: name, CreatedAt: "2026-01-01T00:00:00Z", Driver: "local", Scope: "local", Labels: map[string]string{composeProjectLabel: "catch-svc-a", "com.docker.compose.volume": "data"}}
	}
	for _, name := range []string{strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64), "foreign"} {
		state.volumes[name] = dockerVolumeInfo{Name: name, CreatedAt: "2026-01-01T00:00:00Z", Driver: "local", Scope: "local"}
	}
	volume := state.volumes["catch-svc-a_driver"]
	volume.Options = map[string]string{"device": "shared"}
	state.volumes[volume.Name] = volume
	state.shared["catch-svc-a_shared"] = true
	return state
}

func volumeTestJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

func (d *volumeTestDocker) command(t *testing.T) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		t.Helper()
		d.commands = append(d.commands, slices.Clone(args))
		output := ""
		key := ""
		switch {
		case args[0] == "compose":
			switch {
			case slices.Contains(args, "config"):
				key = "config"
				output = `{"volumes":{"external":{"name":"catch-svc-a_external","external":true}}}`
			case slices.Contains(args, "down"):
				key = "down"
				if d.fail != key {
					d.containers = false
				}
			case slices.Contains(args, "ps"):
				if d.containers {
					output = "app,running\n"
				}
			default:
				t.Fatalf("unexpected Compose command: %v", args)
			}
		case args[0] == "ps":
			key = "ps"
			filter := args[len(args)-1]
			if strings.HasPrefix(filter, "label=") {
				if d.containers {
					output = strings.Repeat("a", 64) + "\n"
				}
			} else {
				name := strings.TrimPrefix(filter, "volume=")
				if d.containers {
					output = strings.Repeat("a", 64) + "\n"
				}
				if d.shared[name] {
					output += strings.Repeat("b", 64) + "\n"
				}
			}
		case args[0] == "container" && args[1] == "inspect":
			key = "inspect-container"
			output = `[{"Config":{"Labels":{"com.docker.compose.project":"catch-svc-a"}},"Mounts":[{"Type":"volume","Name":"` + strings.Repeat("c", 64) + `","Destination":"/anon"},{"Type":"volume","Name":"` + strings.Repeat("d", 64) + `","Destination":"/image"},{"Type":"volume","Name":"` + strings.Repeat("e", 64) + `","Destination":"/explicit"},{"Type":"bind","Name":"foreign","Destination":"/bind"}],"HostConfig":{"Mounts":[{"Type":"volume","Target":"/anon"},{"Type":"volume","Source":"` + strings.Repeat("e", 64) + `","Target":"/explicit"}]}}]`
		case args[0] == "stop":
			key = "stop"
		case args[0] == "volume":
			switch args[1] {
			case "ls":
				key = "ls"
				var names []string
				for name, volume := range d.volumes {
					if len(args) == 3 || volume.Labels[composeProjectLabel] == "catch-svc-a" {
						names = append(names, name)
					}
				}
				slices.Sort(names)
				output = strings.Join(names, "\n")
			case "inspect":
				key = "inspect-volume"
				output = volumeTestJSON([]dockerVolumeInfo{d.volumes[args[2]]})
			case "rm":
				key = "rm"
				if len(args) != 3 {
					t.Fatalf("unbounded volume deletion: %v", args)
				}
				if d.fail != key {
					d.removed = append(d.removed, args[2])
					delete(d.volumes, args[2])
				}
			default:
				t.Fatalf("unexpected volume command: %v", args)
			}
		default:
			t.Fatalf("unexpected Docker command: %v", args)
		}
		status := "0"
		if d.fail != "" && d.fail == key {
			status = "1"
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$VOLUME_TEST_OUTPUT"; exit "$VOLUME_TEST_STATUS"`)
		cmd.Env = append(os.Environ(), "VOLUME_TEST_OUTPUT="+output, "VOLUME_TEST_STATUS="+status)
		return cmd
	}
}

func TestDockerVolumeRemovalProtectsSharedExternalAndExplicitVolumes(t *testing.T) {
	d := newVolumeTestDocker(t)
	if err := d.service.RemoveWithData(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want := []string{"catch-svc-a_data", strings.Repeat("c", 64), strings.Repeat("d", 64)}
	slices.Sort(want)
	slices.Sort(d.removed)
	if !reflect.DeepEqual(d.removed, want) {
		t.Fatalf("removed %v, want %v", d.removed, want)
	}
	for _, name := range []string{"catch-svc-a_shared", "catch-svc-a_external", "catch-svc-a_driver", strings.Repeat("e", 64), "foreign"} {
		if _, ok := d.volumes[name]; !ok {
			t.Fatalf("protected volume %q deleted", name)
		}
	}
}

func TestDockerVolumeRemovalPreservesDataByDefault(t *testing.T) {
	d := newVolumeTestDocker(t)
	if err := d.service.Remove(); err != nil {
		t.Fatal(err)
	}
	if len(d.removed) != 0 || d.containers {
		t.Fatalf("default removal: removed=%v containers=%v", d.removed, d.containers)
	}
	for _, args := range d.commands {
		if args[0] == "volume" || slices.Contains(args, "config") {
			t.Fatalf("default removal inspected/deleted volumes: %v", args)
		}
	}
}

func TestDockerVolumeRemovalFailureRetainsPlanAndRetriesWithoutContainers(t *testing.T) {
	for _, stage := range []string{"down", "rm"} {
		t.Run(stage, func(t *testing.T) {
			d := newVolumeTestDocker(t)
			d.fail = stage
			if err := d.service.RemoveWithData(context.Background(), true); err == nil {
				t.Fatal("expected cleanup failure")
			}
			if _, err := os.Stat(filepath.Join(d.service.RunDir, dockerVolumeRemovalFile)); err != nil {
				t.Fatalf("recovery plan missing: %v", err)
			}
			if err := d.service.Remove(); err == nil {
				t.Fatal("default removal discarded pending cleanup")
			}
			d.fail = ""
			// Rebuild the service to prove the evidence survives a catch restart.
			retry := &DockerComposeService{
				Name:          d.service.Name,
				cfg:           d.service.cfg,
				DataDir:       d.service.DataDir,
				RunDir:        d.service.RunDir,
				NewCmdContext: d.service.NewCmdContext,
			}
			if err := retry.RemoveWithData(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if len(d.removed) != 3 {
				t.Fatalf("retry removed %v, want all three owned volumes", d.removed)
			}
			if err := retry.RemoveWithData(context.Background(), true); err != nil {
				t.Fatalf("idempotent retry: %v", err)
			}
		})
	}
}

func TestDockerVolumeRemovalFailsBeforeTeardownWhenEvidenceUnavailable(t *testing.T) {
	for _, stage := range []string{"config", "ps", "ls", "inspect-container", "inspect-volume", "plan-write"} {
		t.Run(stage, func(t *testing.T) {
			d := newVolumeTestDocker(t)
			if stage == "plan-write" {
				if err := os.WriteFile(d.service.RunDir, []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				d.fail = stage
			}
			if err := d.service.RemoveWithData(context.Background(), true); err == nil {
				t.Fatal("expected evidence failure")
			}
			if !d.containers || len(d.removed) != 0 {
				t.Fatal("cleanup began before ownership evidence was persisted")
			}
		})
	}
}

func TestDockerVolumeRemovalRechecksSharingAndIdentityAfterTeardown(t *testing.T) {
	for _, change := range []string{"new user", "replacement", "changed owner", "invalid plan"} {
		t.Run(change, func(t *testing.T) {
			d := newVolumeTestDocker(t)
			if err := d.service.PrepareVolumeRemoval(context.Background()); err != nil {
				t.Fatal(err)
			}
			d.containers = false
			name := "catch-svc-a_data"
			switch change {
			case "new user":
				d.shared[name] = true
			case "replacement":
				volume := d.volumes[name]
				volume.CreatedAt = "2026-02-01T00:00:00Z"
				d.volumes[name] = volume
			case "changed owner":
				volume := d.volumes[name]
				volume.Labels[composeProjectLabel] = "other"
				d.volumes[name] = volume
			case "invalid plan":
				if err := os.WriteFile(filepath.Join(d.service.RunDir, dockerVolumeRemovalFile), []byte(`{"Project":"other"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.service.RemoveVolumes(context.Background()); err == nil {
				t.Fatal("expected safe cleanup failure")
			}
			if _, ok := d.volumes[name]; !ok {
				t.Fatal("changed/shared volume removed")
			}
		})
	}
}

func FuzzDockerVolumeRemovalPlan(f *testing.F) {
	f.Add([]byte(`{"Project":"catch-app","Volumes":[{"Name":"data","CreatedAt":"now"}]}`))
	f.Add([]byte(`{"Project":"catch-app","Volumes":[{"Name":"--force","CreatedAt":"now"}]}`))
	f.Add([]byte(`{"Project":"other"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		plan, err := parseVolumeRemovalPlan(data, "catch-app")
		if err != nil {
			return
		}
		if plan.Project != "catch-app" {
			t.Fatal("accepted foreign project")
		}
		for _, volume := range plan.Volumes {
			if !validRemovalVolumeName(volume.Name) || volume.CreatedAt == "" {
				t.Fatal("accepted invalid volume")
			}
		}
	})
}

func TestAnonymousVolumeMountRequiresNoExplicitSource(t *testing.T) {
	for _, test := range []struct {
		name, config string
		want         bool
	}{
		{name: "image volume", config: `{}`, want: true},
		{name: "anonymous mount", config: `{"HostConfig":{"Mounts":[{"Type":"volume","Target":"/data"}]}}`, want: true},
		{name: "explicit mount", config: `{"HostConfig":{"Mounts":[{"Type":"volume","Target":"/data","Source":"shared"}]}}`},
		{name: "named bind", config: `{"HostConfig":{"Binds":["shared:/data:rw"]}}`},
		{name: "inherited volume", config: `{"HostConfig":{"VolumesFrom":["other"]}}`},
		{name: "bind mount", config: `{"HostConfig":{"Mounts":[{"Type":"bind","Target":"/data"}]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var container dockerVolumeContainer
			if err := json.Unmarshal([]byte(test.config), &container); err != nil {
				t.Fatal(err)
			}
			if got := anonymousVolumeMount(container, strings.Repeat("a", 64), "/data"); got != test.want {
				t.Fatalf("anonymous mount=%v, want %v", got, test.want)
			}
		})
	}
}

func TestVolumeRemovalPlanValidation(t *testing.T) {
	for _, test := range []struct{ name, data string }{
		{"malformed", "{"},
		{"foreign", `{"Project":"other"}`},
		{"option injection", `{"Project":"catch-app","Volumes":[{"Name":"--force","CreatedAt":"now"}]}`},
		{"path", `{"Project":"catch-app","Volumes":[{"Name":"../other","CreatedAt":"now"}]}`},
		{"missing identity", `{"Project":"catch-app","Volumes":[{"Name":"data"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseVolumeRemovalPlan([]byte(test.data), "catch-app"); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}

func TestDockerStopProjectWorkloadKeepsAnonymousMountEvidence(t *testing.T) {
	d := newVolumeTestDocker(t)
	if err := d.service.StopProjectWorkload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !d.containers {
		t.Fatal("workload stop deleted containers before mount evidence was captured")
	}
	if err := d.service.PrepareVolumeRemoval(context.Background()); err != nil {
		t.Fatal(err)
	}
	plan, err := d.service.loadVolumeRemovalPlan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Volumes) != 3 {
		t.Fatalf("stopping workload lost volume evidence: %v", plan.Volumes)
	}
}

func TestDockerVolumeRemovalRetainsPlanAfterSystemdFailure(t *testing.T) {
	d := newVolumeTestDocker(t)
	d.service.sd = &fakeDockerSystemdService{uninstallErr: errors.New("uninstall failed")}
	if err := d.service.RemoveWithData(context.Background(), true); err == nil {
		t.Fatal("expected systemd failure")
	}
	if err := d.service.Remove(); err == nil {
		t.Fatal("failed removal recovery record was discarded")
	}
	d.service.sd = &fakeDockerSystemdService{}
	if err := d.service.RemoveWithData(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(d.removed) != 3 {
		t.Fatalf("retry deleted additional volumes: %v", d.removed)
	}
}

func TestDockerVolumeRemovalPreservesSharedAnonymousVolume(t *testing.T) {
	d := newVolumeTestDocker(t)
	anonymous := strings.Repeat("c", 64)
	d.shared[anonymous] = true
	if err := d.service.RemoveWithData(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.volumes[anonymous]; !ok {
		t.Fatal("anonymous volume referenced by another container was deleted")
	}
	if len(d.removed) != 2 {
		t.Fatalf("owned volumes were not cleaned: %v", d.removed)
	}
}
