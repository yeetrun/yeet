// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shayne/yeet/pkg/db"
)

type cmdCall struct {
	name string
	args []string
}

func TestDockerComposePullInternal(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: catchit.dev/svc-a/app\n", recordCmd(t, &calls))

	if err := svc.Pull(); err != nil {
		t.Fatalf("Pull returned error: %v", err)
	}
	if composeCallHasSubcmd(calls, "pull") {
		t.Fatalf("did not expect compose pull for internal images")
	}
}

func TestDockerComposePullExternalOnly(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))

	if err := svc.Pull(); err != nil {
		t.Fatalf("Pull returned error: %v", err)
	}

	if !composeCallHasSubcmd(calls, "pull") {
		t.Fatalf("expected compose pull command")
	}
}

func TestDockerComposeUpdateRunningInternal(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: catchit.dev/svc-a/app\n", recordCmd(t, &calls))

	if err := svc.Update(); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	assertCallOrder(t, calls,
		callSpec{composeSubcmd: "ps"},
		callSpec{composeSubcmd: "up"},
	)
	if composeCallHasSubcmd(calls, "pull") {
		t.Fatalf("did not expect compose pull for internal images")
	}
	if !composeCallHasArg(calls, "up", "--pull") || !composeCallHasArg(calls, "up", "never") {
		t.Fatalf("expected compose up to use --pull never")
	}
}

func TestDockerComposeUpdateStoppedExternal(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,exited\n")
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))

	if err := svc.Update(); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	assertCallOrder(t, calls,
		callSpec{composeSubcmd: "ps"},
		callSpec{composeSubcmd: "up"},
	)
	if composeCallHasSubcmd(calls, "pull") {
		t.Fatalf("did not expect compose pull when service is stopped")
	}
	if !composeCallHasArg(calls, "up", "--pull") || !composeCallHasArg(calls, "up", "always") {
		t.Fatalf("expected compose up to use --pull always")
	}
}

func TestDockerComposePrePullIfRunning(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: catchit.dev/svc-a/app\n", recordCmd(t, &calls))

	if err := svc.PrePullIfRunning(); err != nil {
		t.Fatalf("PrePullIfRunning returned error: %v", err)
	}

	assertCallOrder(t, calls, callSpec{composeSubcmd: "ps"})
	if composeCallHasSubcmd(calls, "pull") {
		t.Fatalf("did not expect compose pull for internal images")
	}
}

func TestDockerComposePrePullIfStopped(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,exited\n")
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))

	if err := svc.PrePullIfRunning(); err != nil {
		t.Fatalf("PrePullIfRunning returned error: %v", err)
	}

	assertCallOrder(t, calls, callSpec{composeSubcmd: "ps"})
	if composeCallHasSubcmd(calls, "pull") {
		t.Fatalf("did not expect compose pull when service is stopped")
	}
}

func TestDockerComposeUpWithoutPull(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))

	if err := svc.UpWithPull(false); err != nil {
		t.Fatalf("UpWithPull(false) returned error: %v", err)
	}

	if !composeCallHasSubcmd(calls, "up") {
		t.Fatalf("expected compose up command")
	}
	if composeCallHasArg(calls, "up", "--pull") {
		t.Fatalf("did not expect compose up to include --pull")
	}
}

func TestDockerComposeStatusesStateMapping(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", strings.Join([]string{
		"app,created",
		"worker,restarting",
		"db,paused",
		"",
	}, "\n"))
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	statuses, err := svc.Statuses()
	if err != nil {
		t.Fatalf("Statuses returned error: %v", err)
	}
	if statuses["app"] != StatusStopped {
		t.Fatalf("app status = %v, want %v", statuses["app"], StatusStopped)
	}
	if statuses["worker"] != StatusRunning {
		t.Fatalf("worker status = %v, want %v", statuses["worker"], StatusRunning)
	}
	if statuses["db"] != StatusStopped {
		t.Fatalf("db status = %v, want %v", statuses["db"], StatusStopped)
	}
}

func newTestDockerComposeService(t *testing.T, composeContent string, newCmd func(string, ...string) *exec.Cmd) *DockerComposeService {
	t.Helper()
	tmp := t.TempDir()
	dockerPath := filepath.Join(tmp, "docker")
	if err := os.WriteFile(dockerPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("failed to write fake docker: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	composePath := filepath.Join(tmp, "compose.yml")
	if composeContent == "" {
		composeContent = "services: {}\n"
	}
	if err := os.WriteFile(composePath, []byte(composeContent), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	cfg := &db.Service{
		Name:       "svc-a",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(1): composePath,
				},
			},
		},
	}

	return &DockerComposeService{
		Name:    "svc-a",
		cfg:     cfg,
		DataDir: tmp,
		NewCmd:  newCmd,
	}
}

func recordCmd(t *testing.T, calls *[]cmdCall) func(string, ...string) *exec.Cmd {
	t.Helper()
	return func(name string, args ...string) *exec.Cmd {
		*calls = append(*calls, cmdCall{name: name, args: append([]string{}, args...)})
		cs := []string{"-test.run=TestHelperProcess", "--", name}
		cs = append(cs, args...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	idx := -1
	for i, arg := range args {
		if arg == "--" {
			idx = i
			break
		}
	}
	if idx == -1 || idx+1 >= len(args) {
		os.Exit(0)
	}
	cmdArgs := args[idx+1:]
	if len(cmdArgs) < 2 {
		os.Exit(0)
	}
	actualArgs := cmdArgs[1:]
	if len(actualArgs) > 0 && actualArgs[0] == "compose" {
		if composeSubcommand(actualArgs) == "ps" {
			output := os.Getenv("HELPER_DOCKER_PS_OUTPUT")
			if output != "" {
				os.Stdout.WriteString(output)
			}
		}
	}
	os.Exit(0)
}

type callSpec struct {
	argsPrefix    []string
	composeSubcmd string
}

func assertCallOrder(t *testing.T, calls []cmdCall, specs ...callSpec) {
	t.Helper()
	lastIndex := -1
	for _, spec := range specs {
		index := findCallIndex(calls, spec)
		if index == -1 {
			t.Fatalf("missing call for spec %+v in %#v", spec, calls)
		}
		if index <= lastIndex {
			t.Fatalf("call order incorrect for spec %+v", spec)
		}
		lastIndex = index
	}
}

func findCallIndex(calls []cmdCall, spec callSpec) int {
	for i, call := range calls {
		if spec.composeSubcmd != "" {
			if len(call.args) > 0 && call.args[0] == "compose" && composeSubcommand(call.args) == spec.composeSubcmd {
				return i
			}
			continue
		}
		if len(spec.argsPrefix) > 0 && hasPrefix(call.args, spec.argsPrefix) {
			return i
		}
	}
	return -1
}

func hasPrefix(args, prefix []string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

func composeCallHasSubcmd(calls []cmdCall, subcmd string) bool {
	for _, call := range calls {
		if len(call.args) > 0 && call.args[0] == "compose" && composeSubcommand(call.args) == subcmd {
			return true
		}
	}
	return false
}

func composeCallHasArg(calls []cmdCall, subcmd, arg string) bool {
	for _, call := range calls {
		if len(call.args) > 0 && call.args[0] == "compose" && composeSubcommand(call.args) == subcmd {
			for _, a := range call.args {
				if a == arg {
					return true
				}
			}
		}
	}
	return false
}

func composeSubcommand(args []string) string {
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--project-name", "--project-directory", "--file":
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		return args[i]
	}
	return ""
}
