// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"reflect"
	"testing"
)

func preserveRunDraftGlobals(t *testing.T) {
	t.Helper()
	oldService := serviceOverride
	oldHost := hostOverride
	oldHostSet := hostOverrideSet
	t.Setenv("CATCH_HOST", "")
	t.Cleanup(func() {
		serviceOverride = oldService
		hostOverride = oldHost
		hostOverrideSet = oldHostSet
	})
}

func TestRunDraftFromCLIParsesFirstDeployOptions(t *testing.T) {
	preserveRunDraftGlobals(t)
	serviceOverride = "svc-a"
	hostOverride = "host-a"
	hostOverrideSet = true

	loc := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
	draft, err := runDraftFromCLI([]string{
		"--net=svc,ts",
		"--ts-tags", "tag:app",
		"--env-file=.env",
		"--service-root=tank/apps/svc-a",
		"--zfs",
		"--snapshots=on",
		"--snapshot-keep-last=3",
		"./compose.yml",
		"--",
		"--app-flag",
	}, loc, "host-a")
	if err != nil {
		t.Fatalf("runDraftFromCLI error: %v", err)
	}

	if draft.Service != "svc-a" || draft.Host != "host-a" || draft.Payload != "./compose.yml" {
		t.Fatalf("draft identity = service %q host %q payload %q", draft.Service, draft.Host, draft.Payload)
	}
	if draft.EnvFile != ".env" {
		t.Fatalf("EnvFile = %q, want .env", draft.EnvFile)
	}
	if draft.Storage.ServiceRoot != "tank/apps/svc-a" || !draft.Storage.ZFS {
		t.Fatalf("Storage = %#v, want service root tank/apps/svc-a with zfs", draft.Storage)
	}
	if draft.Snapshots.Mode != "on" || draft.Snapshots.KeepLast != 3 {
		t.Fatalf("Snapshots = %#v, want mode on keep-last 3", draft.Snapshots)
	}
	if !reflect.DeepEqual(draft.Network.Modes, []string{"svc", "ts"}) {
		t.Fatalf("Network.Modes = %#v", draft.Network.Modes)
	}
	if !reflect.DeepEqual(draft.Network.TSTags, []string{"tag:app"}) {
		t.Fatalf("Network.TSTags = %#v", draft.Network.TSTags)
	}
	if !reflect.DeepEqual(draft.PayloadArgs, []string{"--app-flag"}) {
		t.Fatalf("PayloadArgs = %#v", draft.PayloadArgs)
	}
}

func TestRunDraftBuildsExistingRunArgs(t *testing.T) {
	required := false
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "./compose.yml",
		EnvFile: ".env",
		Network: RunDraftNetwork{
			Modes:   []string{"svc", "ts"},
			TSTags:  []string{"tag:app"},
			Publish: []string{"8080:80"},
			Restart: true,
		},
		Storage: RunDraftStorage{ServiceRoot: "tank/apps/svc-a", ZFS: true},
		Snapshots: RunDraftSnapshots{
			Mode:     "on",
			KeepLast: 3,
			MaxAge:   "72h",
			Required: &required,
			Events:   []string{"run"},
		},
		PayloadArgs: []string{"--app-flag"},
	}

	want := []string{
		"--service-root=tank/apps/svc-a",
		"--zfs",
		"--snapshots=on",
		"--snapshot-keep-last=3",
		"--snapshot-max-age=72h",
		"--snapshot-required=false",
		"--snapshot-events=run",
		"--net=svc,ts",
		"--ts-tags=tag:app",
		"--publish=8080:80",
		"--",
		"--app-flag",
	}
	if got := draft.runArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs() = %#v, want %#v", got, want)
	}
}

func TestRunDraftFromCLIRejectsWebForDraftExecution(t *testing.T) {
	preserveRunDraftGlobals(t)
	serviceOverride = "svc-a"

	if _, err := runDraftFromCLI([]string{"--web", "./compose.yml"}, nil, ""); err == nil {
		t.Fatal("runDraftFromCLI error = nil, want --web rejection")
	}
}
