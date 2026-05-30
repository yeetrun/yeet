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
		"--snapshot-events=run",
		"--snapshot-required=false",
		"--snapshot-max-age=72h",
		"--snapshot-keep-last=3",
		"--snapshots=on",
		"--service-root=tank/apps/svc-a",
		"--zfs",
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

func TestRunDraftFromCLIMatchesParseSvcRunParity(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		entries   []ServiceEntry
		wantPull  bool
		wantForce bool
	}{
		{
			name:      "pull and force",
			args:      []string{"--net=svc", "--pull", "--force", "./compose.yml", "--", "--app-flag"},
			wantPull:  true,
			wantForce: true,
		},
		{
			name: "existing stored args",
			args: []string{"./compose.yml"},
			entries: []ServiceEntry{{
				Name:    "svc-a",
				Host:    "host-a",
				Type:    serviceTypeRun,
				Payload: "./compose.yml",
				Args:    []string{"--net=svc,ts", "--ts-tags=tag:app", "--pull", "--app-flag"},
			}},
			wantPull: true,
		},
		{
			name: "existing snapshot overrides",
			args: []string{"--net=svc", "./compose.yml"},
			entries: []ServiceEntry{{
				Name:             "svc-a",
				Host:             "host-a",
				Type:             serviceTypeRun,
				Payload:          "./compose.yml",
				ServiceRoot:      "tank/apps/svc-a",
				ServiceRootZFS:   true,
				Snapshots:        "on",
				SnapshotKeepLast: 3,
				SnapshotMaxAge:   "72h",
				SnapshotRequired: runDraftTestBool(false),
				SnapshotEvents:   []string{"run"},
				Args:             []string{"--net=svc"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveRunDraftGlobals(t)
			serviceOverride = "svc-a"
			loc := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
			for _, entry := range tt.entries {
				loc.Config.SetServiceEntry(entry)
			}

			parsed, err := parseSvcRun(tt.args, loc, "host-a")
			if err != nil {
				t.Fatalf("parseSvcRun error: %v", err)
			}
			draft, err := runDraftFromCLI(tt.args, loc, "host-a")
			if err != nil {
				t.Fatalf("runDraftFromCLI error: %v", err)
			}
			if got := draft.runArgs(); !reflect.DeepEqual(got, parsed.Args) {
				t.Fatalf("draft runArgs = %#v, want parseSvcRun args %#v", got, parsed.Args)
			}
			if draft.ForceDeploy != parsed.ForceDeploy || draft.ForceDeploy != tt.wantForce {
				t.Fatalf("ForceDeploy = %v, parseSvcRun = %v, want %v", draft.ForceDeploy, parsed.ForceDeploy, tt.wantForce)
			}
			if draft.Pull != tt.wantPull {
				t.Fatalf("Pull = %v, want %v", draft.Pull, tt.wantPull)
			}
		})
	}
}

func TestRunDraftRunArgsDefaultsRestartOnForWebDrafts(t *testing.T) {
	draft := RunDraft{Network: RunDraftNetwork{Modes: []string{"svc"}}}
	if got, want := draft.runArgs(), []string{"--net=svc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs() = %#v, want %#v", got, want)
	}

	draft.Network.Restart = runDraftTestBool(false)
	if got, want := draft.runArgs(), []string{"--net=svc", "--restart=false"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs() with explicit restart=false = %#v, want %#v", got, want)
	}
}

func TestRunDraftFromCLIRejectsWebForDraftExecution(t *testing.T) {
	preserveRunDraftGlobals(t)
	serviceOverride = "svc-a"

	if _, err := runDraftFromCLI([]string{"--web", "./compose.yml"}, nil, ""); err == nil {
		t.Fatal("runDraftFromCLI error = nil, want --web rejection")
	}
}

func runDraftTestBool(v bool) *bool {
	return &v
}
