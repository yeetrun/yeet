// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/svc"
)

func removalTestCompose(t *testing.T, server *Server, name string) *svc.DockerComposeService {
	t.Helper()
	root := filepath.Join(server.cfg.ServicesRoot, name)
	for _, dir := range []string{"data", "run"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	composePath := filepath.Join(root, "run", "compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedService(t, server, name, db.ServiceTypeDockerCompose, db.ArtifactStore{db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): composePath}}})
	// Prevent the status warning probe from accessing a real daemon.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose, err := server.dockerComposeService(name)
	if err != nil {
		t.Fatal(err)
	}
	return compose
}

func TestRemoveDockerFailureRetainsServiceAndRecoveryArtifacts(t *testing.T) {
	for _, route := range []string{"server", "tty"} {
		for _, clean := range []bool{false, true} {
			name := route + map[bool]string{true: " clean", false: " preserve"}[clean]
			t.Run(name, func(t *testing.T) {
				server := newTestServer(t)
				compose := removalTestCompose(t, server, "app")
				var calls [][]string
				compose.NewCmdContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
					calls = append(calls, slices.Clone(args))
					return exec.CommandContext(ctx, "sh", "-c", "exit 1")
				}
				old := dockerComposeServiceForRemoval
				dockerComposeServiceForRemoval = func(*Server, string) (*svc.DockerComposeService, error) { return compose, nil }
				t.Cleanup(func() { dockerComposeServiceForRemoval = old })
				var err error
				if route == "server" {
					_, err = server.RemoveServiceWithOptions("app", RemoveOptions{CleanData: clean})
				} else {
					runner := &recordingServiceRunner{}
					e := &ttyExecer{s: server, sn: "app", rw: &bytes.Buffer{}, serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil }}
					err = e.removeCmdFunc(cli.RemoveFlags{Yes: true, CleanData: clean})
					if len(runner.calls) != 0 {
						t.Fatal("TTY ran Docker removal outside authoritative server coordinator")
					}
				}
				if err == nil || !strings.Contains(err.Error(), "retained for retry") {
					t.Fatalf("removal error = %v", err)
				}
				if _, err := server.serviceView("app"); err != nil {
					t.Fatalf("service record lost: %v", err)
				}
				if _, err := os.Stat(filepath.Join(compose.RunDir, "compose.yml")); err != nil {
					t.Fatalf("recovery Compose file lost: %v", err)
				}
				expected := "down"
				if clean {
					expected = "config"
				}
				if len(calls) != 1 || !slices.Contains(calls[0], expected) {
					t.Fatalf("cleanup flag not propagated: %v", calls)
				}
			})
		}
	}
}

func TestRemoveRunnerFailureRetainsConfigAndData(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "app", db.ServiceType("unknown"), nil)
	runnerErr := errors.New("workload still running")
	runner := &recordingServiceRunner{errs: map[string]error{"remove": runnerErr}}
	e := &ttyExecer{s: server, sn: "app", rw: &bytes.Buffer{}, serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil }}
	if err := e.removeCmdFunc(cli.RemoveFlags{Yes: true, CleanData: true}); !errors.Is(err, runnerErr) {
		t.Fatalf("removal error = %v", err)
	}
	if _, err := server.serviceView("app"); err != nil {
		t.Fatalf("service record lost: %v", err)
	}
}

type isoDockerRemoveTestSteps struct {
	*isoRemoveRecorder
	concrete *isoConcreteRemoveSteps
}

func (r *isoDockerRemoveTestSteps) StopWorkload(ctx context.Context, name string) error {
	return r.concrete.StopWorkload(ctx, name)
}
func (r *isoDockerRemoveTestSteps) BeforeDelete(ctx context.Context, name string) error {
	return r.concrete.BeforeDelete(ctx, name)
}

func TestISODockerVolumeFailureRetainsTombstoneAndResumesCleanData(t *testing.T) {
	for _, verified := range []bool{false, true} {
		name := "initial cleanup"
		if verified {
			name = "late clean-data request"
		}
		t.Run(name, func(t *testing.T) { testISODockerVolumeRemovalRetry(t, verified) })
	}
}

func testISODockerVolumeRemovalRetry(t *testing.T, previouslyVerified bool) {
	t.Helper()
	server := newTestServer(t)
	compose := removalTestCompose(t, server, "app")
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	if previouslyVerified {
		allocation.State = string(iso.StateTombstoned)
		allocation.RemoveRequested = true
		allocation.CleanupVerified = true
	}
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, s *db.Service) error { s.ISO = allocation; return nil }); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(compose.DataDir, "keep-until-cleanup")
	if err := os.WriteFile(sentinel, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	active, volumePresent, failRemove := !previouslyVerified, true, true
	var calls [][]string
	compose.NewCmdContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, slices.Clone(args))
		output, status := "", "0"
		switch {
		case args[0] == "compose":
			if slices.Contains(args, "config") {
				output = `{"volumes":{"data":{"name":"catch-app_data"}}}`
			}
		case args[0] == "ps":
			if active {
				output = strings.Repeat("a", 64)
			}
		case args[0] == "container":
			output = `[{"Config":{"Labels":{"com.docker.compose.project":"catch-app"}}}]`
		case args[0] == "rm":
			active = false
		case args[0] == "volume":
			switch args[1] {
			case "ls":
				if volumePresent {
					output = "catch-app_data"
				}
			case "inspect":
				output = `[{"Name":"catch-app_data","CreatedAt":"now","Driver":"local","Scope":"local","Labels":{"com.docker.compose.project":"catch-app","com.docker.compose.volume":"data"}}]`
			case "rm":
				if failRemove {
					status = "1"
				} else {
					volumePresent = false
				}
			}
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$REMOVE_TEST_OUTPUT"; exit "$REMOVE_TEST_STATUS"`)
		cmd.Env = append(os.Environ(), "REMOVE_TEST_OUTPUT="+output, "REMOVE_TEST_STATUS="+status)
		return cmd
	}
	server.newISORemoveSteps = func(_ string, opts RemoveOptions, report *RemoveReport, dataset string) (isoRemoveSteps, error) {
		return &isoDockerRemoveTestSteps{isoRemoveRecorder: &isoRemoveRecorder{server: server}, concrete: &isoConcreteRemoveSteps{server: server, compose: compose, service: &db.Service{Name: "app"}, options: opts, report: report, zfsDataset: dataset}}, nil
	}
	events := make(chan Event, 1)
	handle := server.AddEventListener(events, nil)
	defer server.RemoveEventListener(handle)
	if _, err := server.RemoveServiceWithOptions("app", RemoveOptions{CleanData: true}); err == nil || !strings.Contains(err.Error(), "remove volume") {
		t.Fatalf("cleanup error=%v", err)
	}
	view, err := server.serviceView("app")
	if err != nil {
		t.Fatal(err)
	}
	if !view.ISO().CleanupVerified() || !view.ISO().RemoveCleanData() || view.ISO().State() != string(iso.StateTombstoned) {
		t.Fatalf("cleanup lost durable removal state: %v", view.ISO().AsStruct())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("data lost before requested cleanup succeeded: %v", err)
	}
	select {
	case event := <-events:
		t.Fatalf("failed removal emitted deletion: %v", event)
	default:
	}
	// Ownership evidence must be captured before label-based container removal.
	configAt, removeAt := -1, -1
	for i, args := range calls {
		if slices.Contains(args, "config") {
			configAt = i
		}
		if args[0] == "rm" {
			removeAt = i
		}
	}
	if !previouslyVerified && (configAt < 0 || removeAt <= configAt) {
		t.Fatalf("unsafe iso removal order: %v", calls)
	}
	failRemove = false
	if _, err := server.RemoveServiceWithOptions("app", RemoveOptions{}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if volumePresent {
		t.Fatal("retry lost requested volume cleanup")
	}
	if _, err := server.serviceView("app"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("service survived successful retry: %v", err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry lost clean-data intent: %v", err)
	}
}

func TestRemoveTTYKeepsPreparedDockerRunnerOutput(t *testing.T) {
	server := newTestServer(t)
	compose := removalTestCompose(t, server, "app")
	var output bytes.Buffer
	compose.NewCmdContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", "printf 'fixture removal progress'; exit 1")
		cmd.Stdout = &output
		cmd.Stderr = &output
		return cmd
	}
	old := dockerComposeServiceForRemoval
	dockerComposeServiceForRemoval = func(*Server, string) (*svc.DockerComposeService, error) {
		t.Fatal("prepared TTY runner replaced")
		return nil, nil
	}
	t.Cleanup(func() { dockerComposeServiceForRemoval = old })
	e := &ttyExecer{s: server, sn: "app", rw: &output, serviceRunnerFn: func() (ServiceRunner, error) { return &dockerComposeServiceRunner{DockerComposeService: compose}, nil }}
	if err := e.removeCmdFunc(cli.RemoveFlags{Yes: true}); err == nil {
		t.Fatal("expected fixture failure")
	}
	if !strings.Contains(output.String(), "fixture removal progress") {
		t.Fatalf("Docker progress lost: %q", output.String())
	}
}
