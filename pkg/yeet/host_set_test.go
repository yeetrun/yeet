// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
)

func TestHandleHostSetCallsRunnerWithoutTargets(t *testing.T) {
	oldRunHostSetFn := runHostSetFn
	t.Cleanup(func() {
		runHostSetFn = oldRunHostSetFn
	})

	var got cli.HostSetFlags
	called := false
	runHostSetFn = func(ctx context.Context, flags cli.HostSetFlags) error {
		called = true
		got = flags
		return nil
	}

	if err := HandleHostSet(context.Background(), []string{"set"}); err != nil {
		t.Fatalf("HandleHostSet returned error: %v", err)
	}
	if !called {
		t.Fatal("runHostSetFn was not called")
	}
	if got != (cli.HostSetFlags{}) {
		t.Fatalf("flags = %#v, want zero-value flags", got)
	}
}

func TestHandleHostSetParsesFlagsAndCallsRunner(t *testing.T) {
	oldRunHostSetFn := runHostSetFn
	t.Cleanup(func() {
		runHostSetFn = oldRunHostSetFn
	})

	var got cli.HostSetFlags
	runHostSetFn = func(ctx context.Context, flags cli.HostSetFlags) error {
		got = flags
		return nil
	}

	err := HandleHostSet(context.Background(), []string{
		"set",
		"--data-dir", " /srv/yeet-data ",
		"--services-root=/srv/yeet-services ",
		"--zfs",
		"--migrate-services", " all ",
		"--config", " ./yeet.toml ",
		"--yes",
	})
	if err != nil {
		t.Fatalf("HandleHostSet returned error: %v", err)
	}
	want := cli.HostSetFlags{
		DataDir:         "/srv/yeet-data",
		ServicesRoot:    "/srv/yeet-services",
		ZFS:             true,
		MigrateServices: "all",
		Config:          "./yeet.toml",
		Yes:             true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flags = %#v, want %#v", got, want)
	}
}

func TestHandleHostSetRejectsUnexpectedArgsAfterParsing(t *testing.T) {
	oldRunHostSetFn := runHostSetFn
	t.Cleanup(func() {
		runHostSetFn = oldRunHostSetFn
	})

	runHostSetFn = func(ctx context.Context, flags cli.HostSetFlags) error {
		t.Fatal("runHostSetFn should not be called for leftover args")
		return nil
	}

	err := HandleHostSet(context.Background(), []string{"set", "--data-dir=/srv/yeet-data", "extra"})
	if err == nil || !strings.Contains(err.Error(), "unexpected host set args: extra") {
		t.Fatalf("HandleHostSet error = %v, want unexpected args error", err)
	}
}

func TestRunHostSetPlaceholder(t *testing.T) {
	err := runHostSet(context.Background(), cli.HostSetFlags{})
	if err == nil || !strings.Contains(err.Error(), "requires --data-dir or --services-root") {
		t.Fatalf("runHostSet error = %v, want missing target", err)
	}
}

func TestRunHostSetPromptsAppliesAndUpdatesConfig(t *testing.T) {
	state := stubHostSetRuntime(t)
	state.host = "catch-a"
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, projectConfigName)
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "api", Host: "catch-a", Type: serviceTypeRun, Payload: "api.yml", ServiceRoot: "/old/services/api"})
	cfg.SetServiceEntry(ServiceEntry{Name: "other", Host: "catch-b", Type: serviceTypeRun, Payload: "other.yml", ServiceRoot: "/old/services/other"})
	if err := saveProjectConfig(&projectConfigLocation{Path: configPath, Dir: tmp, Config: cfg}); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	oldRoot := "/old/services"
	newRoot := "/new/services"
	state.client.plan = catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: "/old/data", ServicesRoot: oldRoot},
		Desired: catchrpc.HostStorageState{DataDir: "/old/data", ServicesRoot: newRoot},
		ServicesAction: catchrpc.HostStorageServicesAction{
			Mode: catchrpc.HostStorageMigrateAll,
			From: oldRoot,
			To:   newRoot,
			AffectedServices: []catchrpc.HostStorageServiceMove{{
				Name: "api",
				From: filepath.Join(oldRoot, "api"),
				To:   filepath.Join(newRoot, "api"),
			}},
		},
		RequiresRestart: true,
	}
	state.client.apply = catchrpc.HostStorageApplyResult{
		MigratedServices: []catchrpc.HostStorageServiceMove{{
			Name: "api",
			From: filepath.Join(oldRoot, "api"),
			To:   filepath.Join(newRoot, "api"),
		}},
		RestartScheduled: true,
	}

	err := runHostSet(context.Background(), cli.HostSetFlags{
		ServicesRoot: newRoot,
		Config:       configPath,
	})
	if err != nil {
		t.Fatalf("runHostSet: %v", err)
	}
	if len(state.prompts) != 2 {
		t.Fatalf("prompts = %#v, want migration and apply prompts", state.prompts)
	}
	if !strings.Contains(state.prompts[0], "Migrate services") || !strings.Contains(state.prompts[1], "Apply host storage changes") {
		t.Fatalf("prompts = %#v", state.prompts)
	}
	if len(state.client.planRequests) != 1 {
		t.Fatalf("planRequests = %#v, want one", state.client.planRequests)
	}
	if state.client.planRequests[0].Set.MigrateServices != catchrpc.HostStorageMigrateAll {
		t.Fatalf("MigrateServices = %q, want all", state.client.planRequests[0].Set.MigrateServices)
	}
	if len(state.client.applyRequests) != 1 || !state.client.applyRequests[0].Yes {
		t.Fatalf("applyRequests = %#v, want confirmed apply", state.client.applyRequests)
	}
	loaded, err := loadProjectConfigFromFile(configPath)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	api, ok := loaded.Config.ServiceEntry("api", "catch-a")
	if !ok || api.ServiceRoot != "" || api.ServiceRootZFS {
		t.Fatalf("api entry = %#v, want default root pins cleared", api)
	}
	other, ok := loaded.Config.ServiceEntry("other", "catch-b")
	if !ok || other.ServiceRoot != "/old/services/other" {
		t.Fatalf("other entry = %#v, want untouched other host", other)
	}
	out := state.stdout.String()
	for _, want := range []string{
		"Host storage plan for catch-a",
		"Services root: /old/services -> /new/services",
		"Migrate services: 1",
		"Scheduled catch restart",
		"Updated 1 service root in",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestRunHostSetYesRequiresExplicitMigrateServices(t *testing.T) {
	state := stubHostSetRuntime(t)
	err := runHostSet(context.Background(), cli.HostSetFlags{
		ServicesRoot: "/srv/yeet-services",
		Yes:          true,
	})
	if err == nil || !strings.Contains(err.Error(), "--migrate-services=all|none is required with --yes") {
		t.Fatalf("runHostSet error = %v, want explicit migrate-services requirement", err)
	}
	if len(state.client.planRequests) != 0 || len(state.client.applyRequests) != 0 {
		t.Fatalf("client calls = %#v %#v, want none", state.client.planRequests, state.client.applyRequests)
	}
}

func TestRunHostSetNoChangesReconcilesZFSConfig(t *testing.T) {
	state := stubHostSetRuntime(t)
	state.host = "catch-a"
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, projectConfigName)
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "newt", Host: "catch-a", Type: serviceTypeRun, Payload: "newt.yml", ServiceRoot: "/flash/yeet/services/newt"})
	cfg.SetServiceEntry(ServiceEntry{Name: "custom", Host: "catch-a", Type: serviceTypeRun, Payload: "custom.yml", ServiceRoot: "/flash/yeet/custom"})
	if err := saveProjectConfig(&projectConfigLocation{Path: configPath, Dir: tmp, Config: cfg}); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	state.client.plan = catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		Desired: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services", ServicesZFS: true},
	}

	err := runHostSet(context.Background(), cli.HostSetFlags{
		ServicesRoot:    "flash/yeet/services",
		ZFS:             true,
		MigrateServices: "all",
		Config:          configPath,
		Yes:             true,
	})
	if err != nil {
		t.Fatalf("runHostSet: %v", err)
	}
	if len(state.client.applyRequests) != 0 {
		t.Fatalf("applyRequests = %#v, want none for no host changes", state.client.applyRequests)
	}
	loaded, err := loadProjectConfigFromFile(configPath)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	newt, ok := loaded.Config.ServiceEntry("newt", "catch-a")
	if !ok || newt.ServiceRoot != "flash/yeet/services/newt" || !newt.ServiceRootZFS {
		t.Fatalf("newt entry = %#v, want zfs dataset root", newt)
	}
	custom, ok := loaded.Config.ServiceEntry("custom", "catch-a")
	if !ok || custom.ServiceRoot != "/flash/yeet/custom" || custom.ServiceRootZFS {
		t.Fatalf("custom entry = %#v, want unchanged custom root", custom)
	}
	out := state.stdout.String()
	for _, want := range []string{"No host storage changes to apply.", "Updated 1 service root in"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestRunHostSetRejectsMigrateServicesWithoutServicesRoot(t *testing.T) {
	stubHostSetRuntime(t)
	err := runHostSet(context.Background(), cli.HostSetFlags{
		DataDir:         "/srv/yeet-data",
		MigrateServices: "all",
	})
	if err == nil || !strings.Contains(err.Error(), "--migrate-services requires --services-root") {
		t.Fatalf("runHostSet error = %v, want migrate-services dependency", err)
	}
}

func TestRunHostSetNoopSkipsApply(t *testing.T) {
	state := stubHostSetRuntime(t)
	state.client.plan = catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: "/srv/yeet-data", ServicesRoot: "/srv/yeet-data/services"},
		Desired: catchrpc.HostStorageState{DataDir: "/srv/yeet-data", ServicesRoot: "/srv/yeet-data/services"},
	}
	err := runHostSet(context.Background(), cli.HostSetFlags{DataDir: "/srv/yeet-data"})
	if err != nil {
		t.Fatalf("runHostSet: %v", err)
	}
	if len(state.prompts) != 0 {
		t.Fatalf("prompts = %#v, want none for noop", state.prompts)
	}
	if len(state.client.applyRequests) != 0 {
		t.Fatalf("applyRequests = %#v, want none for noop", state.client.applyRequests)
	}
	if !strings.Contains(state.stdout.String(), "No host storage changes to apply") {
		t.Fatalf("stdout = %q, want noop message", state.stdout.String())
	}
}

func TestRunHostSetAppliesRepairOnlyPlanWithoutRestartFlag(t *testing.T) {
	state := stubHostSetRuntime(t)
	state.client.plan = catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		Desired: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      2,
			DatabaseRefs:    1,
			SystemdRefs:     1,
			RegenerateUnits: []string{"api.service"},
			RestartServices: []string{"api"},
			ValidationRoots: []string{"/root/data"},
		},
	}
	err := runHostSet(context.Background(), cli.HostSetFlags{
		DataDir: "/flash/yeet/data",
		Yes:     true,
	})
	if err != nil {
		t.Fatalf("runHostSet: %v", err)
	}
	if len(state.client.applyRequests) != 1 {
		t.Fatalf("applyRequests = %#v, want repair-only apply", state.client.applyRequests)
	}
	if strings.Contains(state.stdout.String(), "No host storage changes to apply") {
		t.Fatalf("stdout = %q, want repair action treated as a change", state.stdout.String())
	}
}

func TestRunHostSetAppliesCatchRootOnlyPlan(t *testing.T) {
	state := stubHostSetRuntime(t)
	state.client.plan = catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		Desired: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		CatchAction: catchrpc.HostStorageCatchAction{
			Move: true,
			From: "/root/data/services/catch",
			To:   "/flash/yeet/services/catch",
		},
		RequiresRestart: true,
	}
	state.client.apply = catchrpc.HostStorageApplyResult{RestartScheduled: true}
	err := runHostSet(context.Background(), cli.HostSetFlags{
		ServicesRoot:    "flash/yeet/services",
		ZFS:             true,
		MigrateServices: "all",
		Yes:             true,
	})
	if err != nil {
		t.Fatalf("runHostSet: %v", err)
	}
	if len(state.client.applyRequests) != 1 {
		t.Fatalf("applyRequests = %#v, want one", state.client.applyRequests)
	}
	out := state.stdout.String()
	for _, want := range []string{
		"Catch service root: /root/data/services/catch -> /flash/yeet/services/catch",
		"Scheduled catch restart",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestRenderHostStoragePlanShowsRepairAction(t *testing.T) {
	var out strings.Builder
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		Desired: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      4,
			DatabaseRefs:    2,
			SystemdRefs:     1,
			ArtifactRefs:    1,
			RegenerateUnits: []string{"api.service"},
			RestartServices: []string{"api"},
			ValidationRoots: []string{"/root/data"},
		},
		RequiresRestart: true,
	}
	if err := renderHostStoragePlan(&out, "catch-a", plan); err != nil {
		t.Fatalf("renderHostStoragePlan: %v", err)
	}
	for _, want := range []string{
		"Repair host storage references: 4",
		"Database refs: 2",
		"Systemd refs: 1",
		"Generated artifact refs: 1",
		"Regenerate systemd units: 1 (api.service)",
		"Restart services: 1 (api)",
		"Validate old roots: /root/data",
		"Catch restart required.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
}

func TestRunHostSetCancelSkipsApply(t *testing.T) {
	state := stubHostSetRuntime(t)
	state.confirm = false
	state.client.plan = catchrpc.HostStoragePlan{
		Current:         catchrpc.HostStorageState{DataDir: "/old/data", ServicesRoot: "/old/data/services"},
		Desired:         catchrpc.HostStorageState{DataDir: "/new/data", ServicesRoot: "/old/data/services"},
		DataDirAction:   catchrpc.HostStorageDataDirAction{Move: true, From: "/old/data", To: "/new/data"},
		RequiresRestart: true,
	}
	err := runHostSet(context.Background(), cli.HostSetFlags{DataDir: "/new/data"})
	if err != nil {
		t.Fatalf("runHostSet: %v", err)
	}
	if len(state.client.applyRequests) != 0 {
		t.Fatalf("applyRequests = %#v, want none after cancel", state.client.applyRequests)
	}
	if !strings.Contains(state.stdout.String(), "Cancelled") {
		t.Fatalf("stdout = %q, want cancel message", state.stdout.String())
	}
}

func TestRunHostSetBuildsZFSRequest(t *testing.T) {
	state := stubHostSetRuntime(t)
	state.client.plan = catchrpc.HostStoragePlan{
		Current:         catchrpc.HostStorageState{DataDir: "/flash/old-data", ServicesRoot: "/flash/old-services"},
		Desired:         catchrpc.HostStorageState{DataDir: "/flash/yeet/data", DataDirZFS: true, ServicesRoot: "/flash/yeet/services", ServicesZFS: true},
		DataDirAction:   catchrpc.HostStorageDataDirAction{Move: true, From: "/flash/old-data", To: "/flash/yeet/data"},
		RequiresRestart: true,
	}
	err := runHostSet(context.Background(), cli.HostSetFlags{
		DataDir:         "flash/yeet/data",
		ServicesRoot:    "flash/yeet/services",
		ZFS:             true,
		MigrateServices: "none",
		Yes:             true,
	})
	if err != nil {
		t.Fatalf("runHostSet: %v", err)
	}
	if len(state.client.planRequests) != 1 {
		t.Fatalf("planRequests = %#v, want one", state.client.planRequests)
	}
	set := state.client.planRequests[0].Set
	if set.DataDir == nil || set.DataDir.Value != "flash/yeet/data" || !set.DataDir.ZFS {
		t.Fatalf("DataDir target = %#v, want zfs data dataset", set.DataDir)
	}
	if set.ServicesRoot == nil || set.ServicesRoot.Value != "flash/yeet/services" || !set.ServicesRoot.ZFS {
		t.Fatalf("ServicesRoot target = %#v, want zfs services dataset", set.ServicesRoot)
	}
	if set.MigrateServices != catchrpc.HostStorageMigrateNone {
		t.Fatalf("MigrateServices = %q, want none", set.MigrateServices)
	}
	if len(state.prompts) != 0 {
		t.Fatalf("prompts = %#v, want none with --yes and explicit migration mode", state.prompts)
	}
}

func TestApplyHostStorageConfigMovesKeepsCustomRoot(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "api", Host: "catch-a", Type: serviceTypeRun, Payload: "api.yml"})

	updated, skipped := applyHostStorageConfigMoves(cfg, "catch-a", "/new/services", []catchrpc.HostStorageServiceMove{{
		Name: "api",
		From: "/old/services/api",
		To:   "/custom/services/api",
	}})
	if updated != 1 || skipped != 0 {
		t.Fatalf("updated/skipped = %d/%d, want 1/0", updated, skipped)
	}
	api, ok := cfg.ServiceEntry("api", "catch-a")
	if !ok || api.ServiceRoot != "/custom/services/api" || api.ServiceRootZFS {
		t.Fatalf("api entry = %#v, want custom filesystem root", api)
	}
}

func TestApplyHostStorageConfigMovesUsesZFSChildDataset(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "api", Host: "catch-a", Type: serviceTypeRun, Payload: "api.yml", ServiceRoot: "/old/services/api"})

	updated, skipped := applyHostStorageConfigMoves(cfg, "catch-a", "/flash/yeet/services", []catchrpc.HostStorageServiceMove{{
		Name:  "api",
		From:  "/old/services/api",
		To:    "/flash/yeet/services/api",
		ToZFS: "flash/yeet/services/api",
	}})
	if updated != 1 || skipped != 0 {
		t.Fatalf("updated/skipped = %d/%d, want 1/0", updated, skipped)
	}
	api, ok := cfg.ServiceEntry("api", "catch-a")
	if !ok || api.ServiceRoot != "flash/yeet/services/api" || !api.ServiceRootZFS {
		t.Fatalf("api entry = %#v, want zfs child dataset", api)
	}
}

type hostSetTestState struct {
	client  *fakeHostStorageClient
	stdout  strings.Builder
	prompts []string
	confirm bool
	host    string
}

func stubHostSetRuntime(t *testing.T) *hostSetTestState {
	t.Helper()
	state := &hostSetTestState{
		client:  &fakeHostStorageClient{},
		confirm: true,
		host:    "catch-a",
	}
	oldClient := newHostStorageClientFn
	oldConfirm := confirmHostSetFn
	oldStdin := hostSetStdin
	oldStdout := hostSetStdout
	oldPrefs := loadedPrefs
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	t.Cleanup(func() {
		newHostStorageClientFn = oldClient
		confirmHostSetFn = oldConfirm
		hostSetStdin = oldStdin
		hostSetStdout = oldStdout
		loadedPrefs = oldPrefs
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
	})
	loadedPrefs = prefs{DefaultHost: state.host}
	resetHostOverride()
	newHostStorageClientFn = func(host string) hostStorageClient {
		if host != state.host {
			t.Fatalf("host = %q, want %q", host, state.host)
		}
		return state.client
	}
	confirmHostSetFn = func(_ io.Reader, _ io.Writer, msg string) (bool, error) {
		state.prompts = append(state.prompts, msg)
		return state.confirm, nil
	}
	hostSetStdin = strings.NewReader("")
	hostSetStdout = &state.stdout
	return state
}

type fakeHostStorageClient struct {
	planRequests  []catchrpc.HostStoragePlanRequest
	applyRequests []catchrpc.HostStorageApplyRequest
	plan          catchrpc.HostStoragePlan
	apply         catchrpc.HostStorageApplyResult
	planErr       error
	applyErr      error
}

func (c *fakeHostStorageClient) HostStoragePlan(_ context.Context, req catchrpc.HostStoragePlanRequest) (catchrpc.HostStoragePlan, error) {
	c.planRequests = append(c.planRequests, req)
	return c.plan, c.planErr
}

func (c *fakeHostStorageClient) HostStorageApply(_ context.Context, req catchrpc.HostStorageApplyRequest) (catchrpc.HostStorageApplyResult, error) {
	c.applyRequests = append(c.applyRequests, req)
	return c.apply, c.applyErr
}
