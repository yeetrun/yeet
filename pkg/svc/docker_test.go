// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
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

func TestDockerComposeUpUsesPullMode(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))

	if err := svc.Up(); err != nil {
		t.Fatalf("Up returned error: %v", err)
	}

	if !composeCallHasSubcmd(calls, "up") {
		t.Fatalf("expected compose up command, got %#v", calls)
	}
	for _, want := range []string{"--pull", "always", "-d"} {
		if !composeCallHasArg(calls, "up", want) {
			t.Fatalf("compose up missing %q: %#v", want, calls)
		}
	}
}

func TestDockerComposeCommandFallsBackToNewCmdWithoutContext(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.NewCmdContext = nil

	if _, err := svc.command("ps"); err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1: %#v", len(calls), calls)
	}
}

func TestDockerComposeInstallPrefetchesRunningExternalServiceThenInstalls(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")
	calls := []cmdCall{}
	sd := &fakeDockerSystemdService{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.sd = sd

	if err := svc.Install(); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	assertCallOrder(t, calls,
		callSpec{composeSubcmd: "ps"},
		callSpec{composeSubcmd: "pull"},
		callSpec{composeSubcmd: "down"},
	)
	if sd.installCalls != 1 {
		t.Fatalf("systemd Install called %d times, want 1", sd.installCalls)
	}
}

func TestDockerComposeInstallWithPullDisabledSkipsPrePull(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")
	calls := []cmdCall{}
	sd := &fakeDockerSystemdService{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.sd = sd

	if err := svc.InstallWithPull(false); err != nil {
		t.Fatalf("InstallWithPull(false) returned error: %v", err)
	}

	assertCallOrder(t, calls, callSpec{composeSubcmd: "ps"}, callSpec{composeSubcmd: "down"})
	if composeCallHasSubcmd(calls, "pull") {
		t.Fatalf("did not expect compose pull when pull is disabled, got %#v", calls)
	}
	if sd.installCalls != 1 {
		t.Fatalf("systemd Install called %d times, want 1", sd.installCalls)
	}
}

func TestDockerComposeInstallWrapsPrePullError(t *testing.T) {
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	delete(svc.cfg.Artifacts, db.ArtifactDockerComposeFile)

	err := svc.Install()
	if err == nil || !strings.Contains(err.Error(), "failed to pre-pull images") {
		t.Fatalf("Install error = %v, want pre-pull wrapper", err)
	}
}

func TestDockerComposeStartBranches(t *testing.T) {
	t.Run("compose start without systemd", func(t *testing.T) {
		calls := []cmdCall{}
		svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))

		if err := svc.Start(); err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
		if !composeCallHasSubcmd(calls, "start") {
			t.Fatalf("expected compose start command, got %#v", calls)
		}
	})

	t.Run("auxiliary start error", func(t *testing.T) {
		calls := []cmdCall{}
		startErr := errors.New("aux start failed")
		svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
		svc.sd = &fakeDockerSystemdService{startAuxErr: startErr}

		err := svc.Start()
		if !errors.Is(err, startErr) {
			t.Fatalf("Start error = %v, want auxiliary start error", err)
		}
		if composeCallHasSubcmd(calls, "start") {
			t.Fatalf("did not expect compose start after auxiliary error, got %#v", calls)
		}
	})
}

func TestDockerComposeStatusReturnsNotImplemented(t *testing.T) {
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	status, err := svc.Status()
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("Status error = %v, want not implemented", err)
	}
	if status != StatusUnknown {
		t.Fatalf("Status = %v, want %v", status, StatusUnknown)
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

func TestParseDockerComposeStatusesSkipsMalformedLines(t *testing.T) {
	got, err := parseDockerComposeStatuses(strings.Join([]string{
		"app,running",
		"worker,restarting",
		"db,exited",
		"pending,created",
		"paused,paused",
		"dead,dead",
		"removing,removing",
		"mystery,weird",
		"malformed",
		"too,many,fields",
		"",
	}, "\n"))
	if err != nil {
		t.Fatalf("parseDockerComposeStatuses returned error: %v", err)
	}

	want := DockerComposeStatus{
		"app":      StatusRunning,
		"worker":   StatusRunning,
		"db":       StatusStopped,
		"pending":  StatusStopped,
		"paused":   StatusStopped,
		"dead":     StatusStopped,
		"removing": StatusStopped,
		"mystery":  StatusUnknown,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("statuses mismatch (-want +got):\n%s", diff)
	}
}

func TestParseDockerComposeStatusesEmptyOutputUnknown(t *testing.T) {
	_, err := parseDockerComposeStatuses(" \n\t\n")
	if !errors.Is(err, ErrDockerStatusUnknown) {
		t.Fatalf("parseDockerComposeStatuses error = %v, want ErrDockerStatusUnknown", err)
	}
}

func TestDockerComposeCommandCopiesEnvAndIncludesNetworkComposeFile(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	networkPath := filepath.Join(svc.DataDir, "network.yml")
	envPath := filepath.Join(svc.DataDir, "source.env")
	if err := os.WriteFile(networkPath, []byte("networks: {}\n"), 0644); err != nil {
		t.Fatalf("failed to write network compose file: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("A=1\n"), 0644); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	svc.cfg.Artifacts[db.ArtifactDockerComposeNetwork] = artifactAt(1, networkPath)
	svc.cfg.Artifacts[db.ArtifactEnvFile] = artifactAt(1, envPath)

	cmd, err := svc.command("ps")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if cmd.Dir != svc.DataDir {
		t.Fatalf("command dir = %q, want %q", cmd.Dir, svc.DataDir)
	}
	copiedEnv, err := os.ReadFile(filepath.Join(svc.DataDir, ".env"))
	if err != nil {
		t.Fatalf("failed to read copied env file: %v", err)
	}
	if string(copiedEnv) != "A=1\n" {
		t.Fatalf("copied env = %q, want A=1", copiedEnv)
	}
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1: %#v", len(calls), calls)
	}
	if countArg(calls[0].args, "--file") != 2 {
		t.Fatalf("command args should include two --file entries, got %#v", calls[0].args)
	}
	if !hasArg(calls[0].args, networkPath) {
		t.Fatalf("command args missing network compose file %s: %#v", networkPath, calls[0].args)
	}
}

func TestDockerComposeRunCommandContextReturnsCommandCreationError(t *testing.T) {
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	delete(svc.cfg.Artifacts, db.ArtifactDockerComposeFile)

	err := svc.runCommandContext(context.Background(), "ps")
	if err == nil || !strings.Contains(err.Error(), "failed to create docker-compose command") {
		t.Fatalf("runCommandContext error = %v, want command creation error", err)
	}
}

func TestDockerComposeStatusesReturnsDockerCommandError(t *testing.T) {
	t.Setenv("HELPER_DOCKER_FAIL_SUBCOMMAND", "ps")
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	_, err := svc.Statuses()
	if err == nil || !strings.Contains(err.Error(), "failed to run docker command") {
		t.Fatalf("Statuses error = %v, want docker command error", err)
	}
}

func TestDockerComposeAnyRunningAndExistsTreatUnknownStatusAsAbsent(t *testing.T) {
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))

	running, err := svc.AnyRunning()
	if err != nil {
		t.Fatalf("AnyRunning returned error: %v", err)
	}
	if running {
		t.Fatal("AnyRunning = true, want false for unknown status")
	}
	exists, err := svc.Exists()
	if err != nil {
		t.Fatalf("Exists returned error: %v", err)
	}
	if exists {
		t.Fatal("Exists = true, want false for unknown status")
	}
}

func TestDockerComposeCommandContextRemovesStaleEnvFileWithoutEnvArtifact(t *testing.T) {
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	envPath := filepath.Join(svc.DataDir, ".env")
	if err := os.WriteFile(envPath, []byte("STALE=1\n"), 0644); err != nil {
		t.Fatalf("failed to write stale env file: %v", err)
	}

	if _, err := svc.command("ps"); err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("stale env file stat error = %v, want not exist", err)
	}
}

func TestDockerComposeLogsBuildsOptions(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))

	if err := svc.Logs(&LogOptions{Follow: true, Lines: 42}); err != nil {
		t.Fatalf("Logs returned error: %v", err)
	}

	if !composeCallHasSubcmd(calls, "logs") {
		t.Fatalf("expected compose logs command, got %#v", calls)
	}
	for _, want := range []string{"--follow", "--tail", "42"} {
		if !composeCallHasArg(calls, "logs", want) {
			t.Fatalf("compose logs missing %q: %#v", want, calls)
		}
	}
}

func TestDockerComposeStopPropagatesSystemdStopError(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")
	calls := []cmdCall{}
	stopErr := errors.New("systemd stop failed")
	sd := &fakeDockerSystemdService{stopErr: stopErr}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.sd = sd

	err := svc.Stop()
	if !errors.Is(err, stopErr) {
		t.Fatalf("Stop error = %v, want systemd stop error", err)
	}
	if sd.stopCalls != 1 {
		t.Fatalf("systemd Stop called %d times, want 1", sd.stopCalls)
	}
	if !composeCallHasSubcmd(calls, "stop") {
		t.Fatalf("expected compose stop command, got %#v", calls)
	}
}

func TestDockerComposeRemovePropagatesSystemdStopErrorAfterCleanup(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")
	calls := []cmdCall{}
	stopErr := errors.New("systemd stop failed")
	sd := &fakeDockerSystemdService{stopErr: stopErr}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &calls))
	svc.sd = sd

	err := svc.Remove()
	if !errors.Is(err, stopErr) {
		t.Fatalf("Remove error = %v, want systemd stop error", err)
	}
	if sd.stopCalls != 1 {
		t.Fatalf("systemd Stop called %d times, want 1", sd.stopCalls)
	}
	if sd.uninstallCalls != 1 {
		t.Fatalf("systemd Uninstall called %d times, want 1", sd.uninstallCalls)
	}
	assertCallOrder(t, calls,
		callSpec{composeSubcmd: "ps"},
		callSpec{composeSubcmd: "down"},
	)
}

func TestDockerComposeRemoveJoinsStopAndUninstallErrors(t *testing.T) {
	t.Setenv("HELPER_DOCKER_PS_OUTPUT", "app,running\n")
	stopErr := errors.New("systemd stop failed")
	uninstallErr := errors.New("systemd uninstall failed")
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	svc.sd = &fakeDockerSystemdService{stopErr: stopErr, uninstallErr: uninstallErr}

	err := svc.Remove()
	if !errors.Is(err, stopErr) || !errors.Is(err, uninstallErr) {
		t.Fatalf("Remove error = %v, want joined stop and uninstall errors", err)
	}
}

func TestNewDockerComposeServiceSetsContextAwareCommandFactory(t *testing.T) {
	tmp := t.TempDir()
	composePath := filepath.Join(tmp, "compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0644); err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}
	cfg := (&db.Service{
		Name:       "svc-a",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(1): composePath,
				},
			},
		},
	}).View()

	svc, err := NewDockerComposeService(db.NewStore(filepath.Join(tmp, "db.json"), tmp), cfg, tmp, filepath.Join(tmp, "run"))
	if err != nil {
		t.Fatalf("NewDockerComposeService returned error: %v", err)
	}
	if svc.NewCmdContext == nil {
		t.Fatal("expected NewDockerComposeService to install a context-aware command factory")
	}
}

func TestDockerComposeServiceRunCommandContextReturnsContextErrorAfterStart(t *testing.T) {
	t.Setenv("HELPER_SLEEP_MS", "500")

	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	svc.NewCmd = nil
	svc.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, name, args...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := svc.runCommandContext(ctx, "ps", "-a")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runCommandContext error = %v, want context deadline exceeded", err)
	}
}

func newTestDockerComposeService(t *testing.T, composeContent string, newCmd func(string, ...string) *exec.Cmd) *DockerComposeService {
	t.Helper()
	tmp := t.TempDir()
	dockerPath := filepath.Join(tmp, "docker")
	dockerScript := "#!/bin/sh\nGO_WANT_HELPER_PROCESS=1 exec " + strconv.Quote(os.Args[0]) + " -test.run=TestHelperProcess -- docker \"$@\"\n"
	if err := os.WriteFile(dockerPath, []byte(dockerScript), 0755); err != nil {
		t.Fatalf("failed to write fake docker: %v", err)
	}
	nsenterPath := filepath.Join(tmp, "nsenter")
	nsenterScript := "#!/bin/sh\nGO_WANT_HELPER_PROCESS=1 exec " + strconv.Quote(os.Args[0]) + " -test.run=TestHelperProcess -- nsenter \"$@\"\n"
	if err := os.WriteFile(nsenterPath, []byte(nsenterScript), 0755); err != nil {
		t.Fatalf("failed to write fake nsenter: %v", err)
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
		Name:          "svc-a",
		cfg:           cfg,
		DataDir:       tmp,
		NewCmd:        newCmd,
		NewCmdContext: recordCmdContext(t, newCmd),
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

func recordCmdContext(t *testing.T, base func(string, ...string) *exec.Cmd) func(context.Context, string, ...string) *exec.Cmd {
	t.Helper()
	return func(_ context.Context, name string, args ...string) *exec.Cmd {
		return base(name, args...)
	}
}

type fakeDockerSystemdService struct {
	stopErr      error
	uninstallErr error
	installErr   error
	startAuxErr  error
	artifacts    map[db.ArtifactName]bool

	installCalls   int
	stopCalls      int
	uninstallCalls int
	startAuxCalls  int
}

func (f *fakeDockerSystemdService) Install() error {
	f.installCalls++
	return f.installErr
}

func (f *fakeDockerSystemdService) Stop() error {
	f.stopCalls++
	return f.stopErr
}

func (f *fakeDockerSystemdService) Uninstall() error {
	f.uninstallCalls++
	return f.uninstallErr
}

func (f *fakeDockerSystemdService) StartAuxiliaryUnits() error {
	f.startAuxCalls++
	return f.startAuxErr
}

func (f *fakeDockerSystemdService) hasArtifact(a db.ArtifactName) bool {
	return f.artifacts[a]
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
	if delay := os.Getenv("HELPER_SLEEP_MS"); delay != "" {
		ms, err := strconv.Atoi(delay)
		if err == nil {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	actualArgs := cmdArgs[1:]
	if logPath := os.Getenv("HELPER_COMMAND_LOG"); logPath != "" {
		appendHelperCommandLog(logPath, cmdArgs[0], actualArgs)
	}
	if fail := os.Getenv("HELPER_DOCKER_FAIL_SUBCOMMAND"); fail != "" {
		subcmd := ""
		if len(actualArgs) > 0 && actualArgs[0] == "compose" {
			subcmd = composeSubcommand(actualArgs)
		} else if len(actualArgs) > 0 {
			subcmd = actualArgs[0]
		}
		if subcmd == fail {
			os.Stdout.WriteString("docker command failed\n")
			os.Exit(12)
		}
	}
	if len(actualArgs) > 0 && actualArgs[0] == "compose" {
		if composeSubcommand(actualArgs) == "ps" {
			output := os.Getenv("HELPER_DOCKER_PS_OUTPUT")
			for _, arg := range actualArgs {
				if arg == "-q" {
					output = os.Getenv("HELPER_DOCKER_PSQ_OUTPUT")
					break
				}
			}
			if output != "" {
				os.Stdout.WriteString(output)
			}
		}
	}
	if len(actualArgs) > 0 && actualArgs[0] == "inspect" {
		if output := os.Getenv("HELPER_DOCKER_INSPECT_OUTPUT"); output != "" {
			os.Stdout.WriteString(output)
		}
	}
	if cmdArgs[0] == "nsenter" {
		if output := os.Getenv("HELPER_NSENTER_IP_LINK_OUTPUT"); output != "" {
			os.Stdout.WriteString(output)
		}
	}
	os.Exit(0)
}

func appendHelperCommandLog(path, name string, args []string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		os.Exit(2)
	}
	if _, err := f.WriteString(name + "\t" + strings.Join(args, " ") + "\n"); err != nil {
		_ = f.Close()
		os.Exit(2)
	}
	if err := f.Close(); err != nil {
		os.Exit(2)
	}
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
			if hasArg(call.args, arg) {
				return true
			}
		}
	}
	return false
}

func hasArg(args []string, arg string) bool {
	for _, a := range args {
		if a == arg {
			return true
		}
	}
	return false
}

func countArg(args []string, arg string) int {
	count := 0
	for _, a := range args {
		if a == arg {
			count++
		}
	}
	return count
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
