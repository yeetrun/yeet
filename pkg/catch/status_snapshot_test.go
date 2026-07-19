// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestParseDockerComposeStatusSnapshotGroupsYeetProjects(t *testing.T) {
	raw := strings.Join([]string{
		`{"State":"running","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=api"}`,
		`{"State":"exited","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=worker"}`,
		`{"State":"running","Labels":"com.docker.compose.project=other,com.docker.compose.service=ignored"}`,
		`{"State":"mystery","Labels":"com.docker.compose.project=catch-db,com.docker.compose.service=postgres"}`,
		`bad-json`,
	}, "\n")

	got, err := parseDockerComposeStatusSnapshot([]byte(raw))
	if err != nil {
		t.Fatalf("parseDockerComposeStatusSnapshot returned error: %v", err)
	}
	want := map[string]svc.DockerComposeStatus{
		"web": {
			"api":    svc.StatusRunning,
			"worker": svc.StatusStopped,
		},
		"db": {
			"postgres": svc.StatusUnknown,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docker snapshot = %#v, want %#v", got, want)
	}
}

func TestParseDockerComposeStatusSnapshotRejectsOnlyMalformedOutput(t *testing.T) {
	_, err := parseDockerComposeStatusSnapshot([]byte("bad-json\nalso-bad\n"))
	if err == nil || !strings.Contains(err.Error(), "no valid docker status rows") {
		t.Fatalf("error = %v, want no valid docker status rows", err)
	}
}

func TestParseDockerComposeStatusSnapshotRejectsStructurallyMalformedRows(t *testing.T) {
	raw := strings.Join([]string{
		`{"State":"running"}`,
		`{"Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=api"}`,
		`{"State":"","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=api"}`,
		`{"State":"running","Labels":""}`,
	}, "\n")

	_, err := parseDockerComposeStatusSnapshot([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "no valid docker status rows") {
		t.Fatalf("error = %v, want no valid docker status rows", err)
	}
}

func TestParseDockerComposeStatusSnapshotAllowsOnlyNonYeetProjects(t *testing.T) {
	raw := []byte(`{"State":"running","Labels":"com.docker.compose.project=other,com.docker.compose.service=api"}`)
	got, err := parseDockerComposeStatusSnapshot(raw)
	if err != nil {
		t.Fatalf("parseDockerComposeStatusSnapshot returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("docker snapshot = %#v, want empty map", got)
	}
}

func TestParseSystemdShowStatusSnapshotMapsUnitStates(t *testing.T) {
	raw := strings.Join([]string{
		"Id=api.service",
		"LoadState=loaded",
		"ActiveState=active",
		"SubState=running",
		"",
		"Id=worker.service",
		"LoadState=loaded",
		"ActiveState=failed",
		"SubState=failed",
		"",
		"Id=missing.service",
		"LoadState=not-found",
		"ActiveState=inactive",
		"SubState=dead",
		"",
		"Id=odd.service",
		"LoadState=loaded",
		"ActiveState=",
		"SubState=",
	}, "\n")

	got := parseSystemdShowStatusSnapshot([]byte(raw))
	want := map[string]svc.Status{
		"api.service":     svc.StatusRunning,
		"worker.service":  svc.StatusStopped,
		"missing.service": svc.StatusUnknown,
		"odd.service":     svc.StatusUnknown,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemd snapshot = %#v, want %#v", got, want)
	}
}

func TestNewStatusSnapshotCommandDefaultExists(t *testing.T) {
	if newStatusSnapshotCommand == nil {
		t.Fatal("newStatusSnapshotCommand is nil")
	}
}

func TestCollectDockerComposeStatusSnapshotBuildsCommandAndParsesOutput(t *testing.T) {
	raw := `{"State":"running","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=api"}`
	var gotName string
	var gotArgs []string
	newCmd := statusSnapshotCommandContext(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return statusSnapshotFakeCommand(t, ctx, raw)
	})

	got, err := collectDockerComposeStatusSnapshot(context.Background(), newCmd)
	if err != nil {
		t.Fatalf("collectDockerComposeStatusSnapshot returned error: %v", err)
	}
	want := map[string]svc.DockerComposeStatus{
		"web": {"api": svc.StatusRunning},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docker snapshot = %#v, want %#v", got, want)
	}
	wantArgs := []string{"ps", "-a", "--filter", "label=com.docker.compose.project", "--format", "{{json .}}"}
	if gotName != "docker" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command = %s %#v, want docker %#v", gotName, gotArgs, wantArgs)
	}
}

func TestCollectSystemdStatusSnapshotSortsUnitsAndParsesOutput(t *testing.T) {
	raw := strings.Join([]string{
		"Id=api.service",
		"LoadState=loaded",
		"ActiveState=active",
		"SubState=running",
		"",
		"Id=worker.service",
		"LoadState=loaded",
		"ActiveState=inactive",
		"SubState=dead",
	}, "\n")
	var gotName string
	var gotArgs []string
	newCmd := statusSnapshotCommandContext(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return statusSnapshotFakeCommand(t, ctx, raw)
	})

	got, err := collectSystemdStatusSnapshot(context.Background(), newCmd, []string{"worker.service", "", "api.service", "worker.service"})
	if err != nil {
		t.Fatalf("collectSystemdStatusSnapshot returned error: %v", err)
	}
	want := map[string]svc.Status{
		"api.service":    svc.StatusRunning,
		"worker.service": svc.StatusStopped,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemd snapshot = %#v, want %#v", got, want)
	}
	wantArgs := []string{"show", "--property=Id,LoadState,ActiveState,SubState", "api.service", "worker.service"}
	if gotName != "systemctl" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command = %s %#v, want systemctl %#v", gotName, gotArgs, wantArgs)
	}
}

func TestCollectSystemdStatusSnapshotSkipsEmptyUnits(t *testing.T) {
	called := false
	newCmd := statusSnapshotCommandContext(func(context.Context, string, ...string) *exec.Cmd {
		called = true
		return nil
	})

	got, err := collectSystemdStatusSnapshot(context.Background(), newCmd, []string{" ", ""})
	if err != nil {
		t.Fatalf("collectSystemdStatusSnapshot returned error: %v", err)
	}
	if called {
		t.Fatal("collectSystemdStatusSnapshot called command for empty units")
	}
	if len(got) != 0 {
		t.Fatalf("systemd snapshot = %#v, want empty map", got)
	}
}

func TestBuildStatusDataFromSnapshotsMapsConfiguredServices(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/web.yml"}},
	})
	seedService(t, server, "missing-web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/missing.yml"}},
	})
	seedService(t, server, "api", db.ServiceTypeSystemd, nil)
	seedService(t, server, "timer", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/timer.timer"}},
	})
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB returned error: %v", err)
	}
	statuses, err := server.buildStatusDataFromSnapshots(dv, map[string]svc.DockerComposeStatus{
		"web": {"api": svc.StatusRunning},
	}, map[string]svc.Status{
		"api.service":            svc.StatusRunning,
		"timer.timer":            svc.StatusStopped,
		"yeet-vm-devbox.service": svc.StatusRunning,
	})
	if err != nil {
		t.Fatalf("buildStatusDataFromSnapshots returned error: %v", err)
	}

	got := statusByName(statuses)
	assertComponents(t, got["web"], []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}})
	assertComponents(t, got["missing-web"], []ComponentStatusData{{Name: "missing-web", Status: ComponentStatusUnknown}})
	assertComponents(t, got["api"], []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}})
	assertComponents(t, got["timer"], []ComponentStatusData{{Name: "timer", Status: ComponentStatusStopped}})
	assertComponents(t, got["devbox"], []ComponentStatusData{{Name: "devbox", Status: ComponentStatusRunning}})
}

func TestStatusSnapshotUnitNamesIncludesSystemdAndVMUnits(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, nil)
	seedService(t, server, "api", db.ServiceTypeSystemd, nil)
	seedService(t, server, "timer", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/timer.timer"}},
	})
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB returned error: %v", err)
	}
	got, err := server.statusSnapshotUnitNames(dv)
	if err != nil {
		t.Fatalf("statusSnapshotUnitNames returned error: %v", err)
	}
	want := []string{"api.service", "timer.timer", "yeet-vm-devbox.service"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statusSnapshotUnitNames = %#v, want %#v", got, want)
	}
}

func statusByName(statuses []ServiceStatusData) map[string]ServiceStatusData {
	out := make(map[string]ServiceStatusData, len(statuses))
	for _, status := range statuses {
		out[status.ServiceName] = status
	}
	return out
}

func assertComponents(t *testing.T, status ServiceStatusData, want []ComponentStatusData) {
	t.Helper()
	if !reflect.DeepEqual(status.ComponentStatus, want) {
		t.Fatalf("%s components = %#v, want %#v", status.ServiceName, status.ComponentStatus, want)
	}
}

func statusSnapshotFakeCommand(t *testing.T, ctx context.Context, output string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestStatusSnapshotFakeCommand", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_STATUS_SNAPSHOT_HELPER=1",
		"STATUS_SNAPSHOT_HELPER_OUTPUT="+output,
	)
	return cmd
}

func TestStatusSnapshotFakeCommand(t *testing.T) {
	if os.Getenv("GO_WANT_STATUS_SNAPSHOT_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(os.Getenv("STATUS_SNAPSHOT_HELPER_OUTPUT"))
	os.Exit(0)
}
