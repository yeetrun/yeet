// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
)

type failAfterWriter struct {
	writes int
	err    error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, w.err
	}
	return len(p), nil
}

func preserveSvcCommandGlobals(t *testing.T) {
	t.Helper()
	oldExec := execRemoteFn
	oldExecOutput := execRemoteOutputFn
	oldExecDirect := execRemoteDirectFn
	oldFetchStatus := fetchStatusForHostFn
	oldFetchHashes := fetchRemoteArtifactHashesFn
	oldArch := remoteCatchOSAndArchFn
	oldPushLocal := pushAllLocalImagesFn
	oldBuildDocker := buildDockerImageForRemoteFn
	oldTryDocker := tryRunDockerFn
	oldTryImage := tryRunRemoteImageFn
	oldImageExists := imageExistsFn
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		execRemoteFn = oldExec
		execRemoteOutputFn = oldExecOutput
		execRemoteDirectFn = oldExecDirect
		fetchStatusForHostFn = oldFetchStatus
		fetchRemoteArtifactHashesFn = oldFetchHashes
		remoteCatchOSAndArchFn = oldArch
		pushAllLocalImagesFn = oldPushLocal
		buildDockerImageForRemoteFn = oldBuildDocker
		tryRunDockerFn = oldTryDocker
		tryRunRemoteImageFn = oldTryImage
		imageExistsFn = oldImageExists
		serviceOverride = oldService
		loadedPrefs = oldPrefs
		isTerminalFn = oldIsTerminal
		resetHostOverride()
	})
}

func useTempSvcCwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})
	return tmp
}

func writeSvcBranchConfig(t *testing.T, dir string, entries ...ServiceEntry) *projectConfigLocation {
	t.Helper()
	cfg := &ProjectConfig{Version: projectConfigVersion}
	for _, entry := range entries {
		cfg.SetServiceEntry(entry)
	}
	loc := &projectConfigLocation{Path: filepath.Join(dir, projectConfigName), Dir: dir, Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}
	return loc
}

func captureSvcStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	os.Stdout = w
	out := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		_, err := io.Copy(&buf, r)
		out <- buf.String()
		readErr <- err
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout = oldStdout
	if err := <-readErr; err != nil {
		t.Fatalf("ReadAll stdout error: %v", err)
	}
	return <-out, runErr
}

func withSvcPromptInput(t *testing.T, input string) {
	t.Helper()
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe stdin error: %v", err)
	}
	if _, err := io.WriteString(w, input); err != nil {
		t.Fatalf("WriteString stdin error: %v", err)
	}
	_ = w.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile devnull error: %v", err)
	}
	os.Stdin = r
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		_ = r.Close()
		_ = devNull.Close()
	})
}

func TestSvcMissingServiceHelpersCoverGroupsAndEvents(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "empty", want: "missing service name"},
		{name: "plain command", args: []string{"logs"}, want: "logs requires a service name"},
		{name: "group command", args: []string{"docker", "update"}, want: "docker update requires a service name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := missingServiceError(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("missingServiceError(%v) = %v, want containing %q", tt.args, err, tt.want)
			}
		})
	}

	needs, err := commandNeedsService([]string{"events", "--all"})
	if err != nil {
		t.Fatalf("commandNeedsService events --all error: %v", err)
	}
	if needs {
		t.Fatalf("events --all needs service = true, want false")
	}
	if _, err := commandNeedsService([]string{"events", "--all=not-bool"}); err == nil {
		t.Fatalf("expected parse error for invalid events flag")
	}
	needs, err = commandNeedsService([]string{"mount"})
	if err != nil {
		t.Fatalf("commandNeedsService mount error: %v", err)
	}
	if needs {
		t.Fatalf("mount needs service = true, want false")
	}
}

func TestSvcCommandRequestErrorsAndHostResolution(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)

	serviceOverride = ""
	if _, err := newSvcCommandRequest([]string{"logs"}); err == nil || !strings.Contains(err.Error(), "logs requires a service name") {
		t.Fatalf("newSvcCommandRequest logs error = %v, want missing service", err)
	}

	serviceOverride = "svc-a"
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-b", Type: serviceTypeRun, Payload: "run.sh"})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
	if err := applySvcCommandHost(loc, false); err == nil || !strings.Contains(err.Error(), "multiple hosts") {
		t.Fatalf("applySvcCommandHost error = %v, want ambiguous host", err)
	}

	if err := ensureSvcCommandService([]string{"logs"}); err != nil {
		t.Fatalf("ensureSvcCommandService with service override error: %v", err)
	}
}

func TestSvcEnvErrorsAndRemoteFailure(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"

	req := svcCommandRequest{Service: "svc-a", Command: svcCommand{RawArgs: []string{"env", "copy"}}}
	if err := handleSvcEnv(context.Background(), req); err == nil || !strings.Contains(err.Error(), "env copy requires a file") {
		t.Fatalf("env copy missing file error = %v", err)
	}

	req.Command.RawArgs = []string{"env", "set"}
	if err := handleSvcEnv(context.Background(), req); err == nil || !strings.Contains(err.Error(), "requires at least one") {
		t.Fatalf("env set missing assignment error = %v", err)
	}

	req.Command.RawArgs = []string{"env", "set", " BAD=value"}
	if err := handleSvcEnv(context.Background(), req); err == nil || !strings.Contains(err.Error(), "contains whitespace") {
		t.Fatalf("env set invalid assignment error = %v", err)
	}

	remoteErr := errors.New("remote env failed")
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return remoteErr
	}
	req.Command.RawArgs = []string{"env", "set", "PORT1=8080"}
	if err := handleSvcEnv(context.Background(), req); !errors.Is(err, remoteErr) {
		t.Fatalf("env set remote error = %v, want %v", err, remoteErr)
	}
}

func TestSvcRunEnvCopyErrors(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"

	if err := runEnvCopy(""); err == nil || !strings.Contains(err.Error(), "env copy requires a file") {
		t.Fatalf("runEnvCopy empty error = %v", err)
	}
	if err := runEnvCopy(filepath.Join(t.TempDir(), "missing.env")); err == nil {
		t.Fatalf("expected missing file error")
	}
	dir := t.TempDir()
	if err := runEnvCopy(dir); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("runEnvCopy dir error = %v", err)
	}

	envFile := filepath.Join(t.TempDir(), "prod.env")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("WriteFile env error: %v", err)
	}
	remoteErr := errors.New("copy refused")
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return remoteErr
	}
	if err := runEnvCopy(envFile); !errors.Is(err, remoteErr) {
		t.Fatalf("runEnvCopy remote error = %v, want %v", err, remoteErr)
	}
}

func TestSvcEnvAssignmentValidation(t *testing.T) {
	assignments, err := parseEnvAssignments([]string{"FOO1=bar", "_KEY=value"})
	if err != nil {
		t.Fatalf("parseEnvAssignments valid error: %v", err)
	}
	if len(assignments) != 2 || assignments[0].Key != "FOO1" || assignments[1].Key != "_KEY" {
		t.Fatalf("assignments = %#v", assignments)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "empty", want: "requires at least one"},
		{name: "missing equals", args: []string{"FOO"}, want: "expected KEY=VALUE"},
		{name: "digit start", args: []string{"1FOO=bar"}, want: "invalid env key"},
		{name: "bad char", args: []string{"FOO-BAR=value"}, want: "invalid env key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseEnvAssignments(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseEnvAssignments error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSvcRunParsingErrorsAndStoredEnv(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := t.TempDir()
	serviceOverride = "svc-a"

	if _, err := parseSvcRun(nil, nil, ""); err == nil || !strings.Contains(err.Error(), "run requires a payload") {
		t.Fatalf("parseSvcRun empty error = %v", err)
	}
	if _, err := parseSvcRun([]string{"app", "--env-file"}, nil, ""); err == nil || !strings.Contains(err.Error(), "--env-file requires a value") {
		t.Fatalf("parseSvcRun env error = %v", err)
	}
	if _, err := parseSvcRun([]string{"app", "--force=maybe"}, nil, ""); err == nil || !strings.Contains(err.Error(), "invalid --force") {
		t.Fatalf("parseSvcRun force error = %v", err)
	}

	loc := &projectConfigLocation{Dir: tmp, Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    Host(),
		Type:    serviceTypeRun,
		Payload: "app",
		Args:    []string{"--net=ts", "--ts-tags=tag:a"},
		EnvFile: "stored.env",
	})

	if _, err := parseSvcRun([]string{"app", "--net=lan"}, loc, ""); err == nil || !strings.Contains(err.Error(), "cannot change --net") {
		t.Fatalf("parseSvcRun locked flags error = %v", err)
	}

	loc.Config.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    Host(),
		Type:    serviceTypeRun,
		Payload: "app",
		EnvFile: "stored.env",
	})
	run, err := parseSvcRun([]string{"app", "--force", "--", "--app-flag"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun stored env error: %v", err)
	}
	if !run.ForceDeploy {
		t.Fatalf("ForceDeploy = false, want true")
	}
	if run.EnvFile != filepath.Join(tmp, "stored.env") {
		t.Fatalf("EnvFile = %q, want stored path", run.EnvFile)
	}
	if !reflect.DeepEqual(run.Args, []string{"--", "--app-flag"}) {
		t.Fatalf("Args = %#v", run.Args)
	}
}

func TestSvcRunFromStoredConfigViaHandle(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	isTerminalFn = func(int) bool { return false }

	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile payload error: %v", err)
	}
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "run.sh",
		Args:    []string{"--pull"},
	})

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		if stdin == nil {
			t.Fatalf("expected stdin payload")
		}
		return nil
	}

	if err := HandleSvcCmd([]string{"run"}); err != nil {
		t.Fatalf("HandleSvcCmd run error: %v", err)
	}
	if !reflect.DeepEqual(gotArgs, []string{"run", "--pull"}) {
		t.Fatalf("remote args = %#v, want run --pull", gotArgs)
	}
}

func TestSvcRunFromStoredConfigErrors(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := t.TempDir()

	serviceOverride = ""
	if err := runFromProjectConfig(nil, ""); err == nil || !strings.Contains(err.Error(), "run requires a service name") {
		t.Fatalf("runFromProjectConfig no service error = %v", err)
	}

	serviceOverride = "svc-a"
	if err := runFromProjectConfig(nil, ""); err == nil || !strings.Contains(err.Error(), "no yeet.toml found") {
		t.Fatalf("runFromProjectConfig no config error = %v", err)
	}

	loc := &projectConfigLocation{Dir: tmp, Config: &ProjectConfig{Version: projectConfigVersion}}
	if err := runFromProjectConfig(loc, "host-a"); err == nil || !strings.Contains(err.Error(), "no stored run config") {
		t.Fatalf("runFromProjectConfig no entry error = %v", err)
	}

	loc.Config.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeCron, Payload: "job.sh"})
	if err := runFromProjectConfig(loc, "host-a"); err == nil || !strings.Contains(err.Error(), "configured as cron") {
		t.Fatalf("runFromProjectConfig type error = %v", err)
	}

	loc.Config.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: " "})
	if err := runFromProjectConfig(loc, "host-a"); err == nil || !strings.Contains(err.Error(), "no payload configured") {
		t.Fatalf("runFromProjectConfig payload error = %v", err)
	}
}

func TestSvcShouldRunFromConfigWithForce(t *testing.T) {
	fromConfig, err := shouldRunFromConfigWithForce([]string{"--force"})
	if err != nil {
		t.Fatalf("shouldRunFromConfigWithForce error: %v", err)
	}
	if !fromConfig {
		t.Fatalf("--force should use stored config")
	}

	fromConfig, err = shouldRunFromConfigWithForce([]string{"--force", "app"})
	if err != nil {
		t.Fatalf("shouldRunFromConfigWithForce payload error: %v", err)
	}
	if fromConfig {
		t.Fatalf("--force with payload should not use stored config")
	}

	if _, err := shouldRunFromConfigWithForce([]string{"--force=bogus"}); err == nil {
		t.Fatalf("expected invalid force error")
	}
}

func TestSvcCronFromStoredConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := t.TempDir()
	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	isTerminalFn = func(int) bool { return false }

	payload := filepath.Join(tmp, "job.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho cron\n"), 0o700); err != nil {
		t.Fatalf("WriteFile payload error: %v", err)
	}
	loc := &projectConfigLocation{Dir: tmp, Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{
		Name:     "svc-a",
		Host:     "host-a",
		Type:     serviceTypeCron,
		Payload:  "job.sh",
		Schedule: "5 4 * * *",
		Args:     []string{"--daily"},
	})

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		if stdin == nil {
			t.Fatalf("expected cron payload")
		}
		return nil
	}

	if err := runCronFromProjectConfig(loc, "host-a"); err != nil {
		t.Fatalf("runCronFromProjectConfig error: %v", err)
	}
	wantArgs := []string{"cron", "5", "4", "*", "*", "*", "--daily"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("remote args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestSvcCronFromStoredConfigErrors(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := t.TempDir()

	serviceOverride = ""
	if err := runCronFromProjectConfig(nil, ""); err == nil || !strings.Contains(err.Error(), "cron requires a service name") {
		t.Fatalf("runCronFromProjectConfig no service error = %v", err)
	}

	serviceOverride = "svc-a"
	loc := &projectConfigLocation{Dir: tmp, Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: "", Payload: "job.sh"})
	if err := runCronFromProjectConfig(loc, "host-a"); err == nil || !strings.Contains(err.Error(), "not configured for cron") {
		t.Fatalf("runCronFromProjectConfig blank type error = %v", err)
	}

	loc.Config.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeCron, Payload: " ", Schedule: "0 9 * * *"})
	if err := runCronFromProjectConfig(loc, "host-a"); err == nil || !strings.Contains(err.Error(), "no payload configured") {
		t.Fatalf("runCronFromProjectConfig payload error = %v", err)
	}

	loc.Config.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeCron, Payload: "job.sh", Schedule: "* * *"})
	if err := runCronFromProjectConfig(loc, "host-a"); err == nil || !strings.Contains(err.Error(), "invalid schedule") {
		t.Fatalf("runCronFromProjectConfig schedule error = %v", err)
	}
}

func TestSvcCronAndStageErrorBranches(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"

	if _, _, err := splitCronArgs(nil); err == nil || !strings.Contains(err.Error(), "cron requires") {
		t.Fatalf("splitCronArgs nil error = %v", err)
	}
	if _, err := parseCronSchedule("* * *"); err == nil || !strings.Contains(err.Error(), "5 fields") {
		t.Fatalf("parseCronSchedule error = %v", err)
	}
	if _, err := parseCronSchedule("0 9 * * *"); err != nil {
		t.Fatalf("parseCronSchedule valid error: %v", err)
	}

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "", "", errors.New("arch unavailable")
	}
	if err := runCron("missing.sh", []string{"0", "9", "*", "*", "*"}, nil); err == nil || !strings.Contains(err.Error(), "arch unavailable") {
		t.Fatalf("runCron arch error = %v", err)
	}

	dir := t.TempDir()
	oldStderr := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile devnull error: %v", err)
	}
	os.Stderr = devNull
	t.Cleanup(func() {
		os.Stderr = oldStderr
		_ = devNull.Close()
	})
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	if err := runStageBinary(dir); err == nil {
		t.Fatalf("expected directory stage error")
	}
}

func TestSvcStageFileErrorBranches(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "", "", errors.New("arch unavailable")
	}
	if err := stageFile("svc-a", "missing"); err == nil || !strings.Contains(err.Error(), "arch unavailable") {
		t.Fatalf("stageFile arch error = %v", err)
	}

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	if err := stageFile("svc-a", filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "failed to detect file type") {
		t.Fatalf("stageFile missing payload error = %v", err)
	}

	payload := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile payload error: %v", err)
	}
	remoteErr := errors.New("stage refused")
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return remoteErr
	}
	if err := stageFile("svc-a", payload); err == nil || !strings.Contains(err.Error(), "failed to upload file") {
		t.Fatalf("stageFile remote error = %v", err)
	}
}

func TestSvcRemovePromptAndErrorBranches(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		remoteErr   error
		wantErr     string
		wantRemoved bool
	}{
		{name: "prompt no keeps config", input: "n\n"},
		{name: "prompt yes removes config", input: "y\n", wantRemoved: true},
		{name: "remote error keeps config", remoteErr: errors.New("remote remove failed"), wantErr: "remote remove failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveSvcCommandGlobals(t)
			tmp := useTempSvcCwd(t)
			serviceOverride = "svc-a"
			loadedPrefs.DefaultHost = "host-a"
			loc := writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
			if tt.input != "" {
				withSvcPromptInput(t, tt.input)
			}
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				return tt.remoteErr
			}

			err := handleSvcRemove(context.Background(), svcCommandRequest{
				Command: svcCommand{Args: nil, RawArgs: []string{"remove"}},
				Config:  loc,
				Service: "svc-a",
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("handleSvcRemove error = %v, want containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("handleSvcRemove error: %v", err)
			}
			_, has := loc.Config.ServiceEntry("svc-a", "host-a")
			if has == tt.wantRemoved {
				t.Fatalf("config entry present = %v, want %v", has, !tt.wantRemoved)
			}
		})
	}

	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"
	if err := handleSvcRemove(context.Background(), svcCommandRequest{
		Command: svcCommand{Args: []string{"--clean-config=bogus"}, RawArgs: []string{"remove", "--clean-config=bogus"}},
		Service: "svc-a",
	}); err == nil {
		t.Fatalf("expected parse error for invalid clean-config flag")
	}
}

func TestSvcEventsErrors(t *testing.T) {
	preserveSvcCommandGlobals(t)

	req := svcCommandRequest{Command: svcCommand{Args: []string{"--all=bogus"}, RawArgs: []string{"events", "--all=bogus"}}}
	if err := handleSvcEvents(context.Background(), req); err == nil {
		t.Fatalf("expected invalid events flag error")
	}

	serviceOverride = ""
	req = svcCommandRequest{Command: svcCommand{Args: nil, RawArgs: []string{"events"}}}
	if err := handleSvcEvents(context.Background(), req); err == nil || !strings.Contains(err.Error(), "events requires a service name") {
		t.Fatalf("handleSvcEvents missing service error = %v", err)
	}
}

func TestSvcRunPayloadScanningAndFallbacks(t *testing.T) {
	if _, _, err := splitRunPayloadArgs([]string{"--pull"}); err == nil || !strings.Contains(err.Error(), "run requires a payload") {
		t.Fatalf("splitRunPayloadArgs flags-only error = %v", err)
	}

	flagArgs, payloadArgs := splitRunArgsForParsing([]string{"--net", "svc", "--", "--remote-flag"})
	if !reflect.DeepEqual(flagArgs, []string{"--net", "svc"}) || !reflect.DeepEqual(payloadArgs, []string{"--remote-flag"}) {
		t.Fatalf("splitRunArgsForParsing = %#v %#v", flagArgs, payloadArgs)
	}
	flagArgs, payloadArgs = splitRunArgsForParsing([]string{"--"})
	if len(flagArgs) != 0 || payloadArgs != nil {
		t.Fatalf("splitRunArgsForParsing delimiter only = %#v %#v", flagArgs, payloadArgs)
	}

	if got := normalizeArgs([]string{"", "  ", "arg"}); !reflect.DeepEqual(got, []string{"arg"}) {
		t.Fatalf("normalizeArgs = %#v", got)
	}

	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"
	imageExistsFn = func(string) bool { return false }
	if err := runRun("not-a-known-payload", nil); err == nil || !strings.Contains(err.Error(), "unknown payload") {
		t.Fatalf("runRun unknown error = %v", err)
	}
}

func TestSvcDockerfileAndRemoteImageErrorBranches(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"

	ok, err := tryRunDockerfile("compose.yml", nil)
	if err != nil || ok {
		t.Fatalf("tryRunDockerfile non-Dockerfile = %v %v, want false nil", ok, err)
	}
	if ok, err := tryRunDockerfile(filepath.Join(t.TempDir(), "Dockerfile"), nil); ok || err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("tryRunDockerfile missing = %v %v, want false with error", ok, err)
	}

	dockerDir := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.Mkdir(dockerDir, 0o755); err != nil {
		t.Fatalf("Mkdir Dockerfile dir error: %v", err)
	}
	if ok, err := tryRunDockerfile(dockerDir, nil); ok || err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("tryRunDockerfile dir = %v %v, want false with error", ok, err)
	}

	dockerfile := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatalf("WriteFile Dockerfile error: %v", err)
	}
	buildErr := errors.New("build failed")
	buildDockerImageForRemoteFn = func(ctx context.Context, dockerfilePath, imageName string) error {
		if dockerfilePath != dockerfile {
			t.Fatalf("dockerfile path = %q, want %q", dockerfilePath, dockerfile)
		}
		return buildErr
	}
	if ok, err := tryRunDockerfile(dockerfile, nil); !ok || !errors.Is(err, buildErr) {
		t.Fatalf("tryRunDockerfile build error = %v %v, want ok and %v", ok, err, buildErr)
	}

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "", "", errors.New("arch unavailable")
	}
	if ok, err := tryRunRemoteImage("nginx:latest", nil); ok || err == nil || !strings.Contains(err.Error(), "arch unavailable") {
		t.Fatalf("tryRunRemoteImage arch error = %v %v", ok, err)
	}
	if ok, err := tryRunRemoteImage("not-an-image", nil); ok || err != nil {
		t.Fatalf("tryRunRemoteImage non-image = %v %v, want false nil", ok, err)
	}
}

func TestSvcStatusFetchMultiHostAndRemoteFormats(t *testing.T) {
	preserveSvcCommandGlobals(t)

	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		if host != "host-a" || service != systemServiceName || !reflect.DeepEqual(args, []string{"status", "--format=json"}) {
			t.Fatalf("execRemoteOutputFn = (%q, %q, %#v)", host, service, args)
		}
		return []byte(`[{"serviceName":"svc-a","serviceType":"service","components":[{"name":"svc-a","status":"running"}]}]`), nil
	}
	statuses, err := fetchStatusForHost(context.Background(), "host-a", cli.StatusFlags{})
	if err != nil {
		t.Fatalf("fetchStatusForHost error: %v", err)
	}
	if len(statuses) != 1 || statuses[0].ServiceName != "svc-a" {
		t.Fatalf("statuses = %#v", statuses)
	}

	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		return nil, errors.New("dial failed")
	}
	if _, err := fetchStatusForHost(context.Background(), "host-a", cli.StatusFlags{}); err == nil || !strings.Contains(err.Error(), "status on host-a") {
		t.Fatalf("fetchStatusForHost remote error = %v", err)
	}

	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		return []byte(`not-json`), nil
	}
	if _, err := fetchStatusForHost(context.Background(), "host-a", cli.StatusFlags{}); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("fetchStatusForHost JSON error = %v", err)
	}

	fetchStatusForHostFn = func(ctx context.Context, host string, flags cli.StatusFlags) ([]statusService, error) {
		return []statusService{{ServiceName: "svc-" + host, ServiceType: "service"}}, nil
	}
	out, err := captureSvcStdout(t, func() error {
		return statusMultiHost(context.Background(), []string{"host-b", "host-a"}, cli.StatusFlags{Format: "json-pretty"})
	})
	if err != nil {
		t.Fatalf("statusMultiHost json error: %v", err)
	}
	var decoded []hostStatusData
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("statusMultiHost output invalid JSON: %v\n%s", err, out)
	}
	if len(decoded) != 2 || decoded[0].Host != "host-a" || decoded[1].Host != "host-b" {
		t.Fatalf("decoded hosts = %#v", decoded)
	}

	fetchStatusForHostFn = func(ctx context.Context, host string, flags cli.StatusFlags) ([]statusService, error) {
		return nil, errors.New("host failed")
	}
	if err := statusMultiHost(context.Background(), []string{"host-a"}, cli.StatusFlags{}); err == nil || !strings.Contains(err.Error(), "host failed") {
		t.Fatalf("statusMultiHost error = %v", err)
	}
}

func TestSvcStatusHostsAndRenderErrors(t *testing.T) {
	preserveSvcCommandGlobals(t)
	loadedPrefs.DefaultHost = "default-host"
	SetHostOverride("override-host")
	if got := statusHosts(nil, true); !reflect.DeepEqual(got, []string{"override-host"}) {
		t.Fatalf("statusHosts override = %#v", got)
	}
	resetHostOverride()
	loadedPrefs.DefaultHost = "default-host"
	if got := statusHosts(nil, false); !reflect.DeepEqual(got, []string{"default-host"}) {
		t.Fatalf("statusHosts nil = %#v", got)
	}
	if got := statusHosts(&projectConfigLocation{Config: &ProjectConfig{Version: projectConfigVersion}}, false); !reflect.DeepEqual(got, []string{"default-host"}) {
		t.Fatalf("statusHosts empty config = %#v", got)
	}
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-b"})
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-b", Host: "host-a"})
	if got := statusHosts(&projectConfigLocation{Config: cfg}, false); !reflect.DeepEqual(got, []string{"host-a", "host-b"}) {
		t.Fatalf("statusHosts config = %#v", got)
	}

	writeErr := errors.New("write failed")
	if err := renderStatusTables(errorWriter{err: writeErr}, []hostStatusData{}, false); !errors.Is(err, writeErr) {
		t.Fatalf("renderStatusTables header error = %v, want %v", err, writeErr)
	}
	rowWriter := &failAfterWriter{err: writeErr}
	if err := renderStatusTables(rowWriter, []hostStatusData{{Host: "host-a", Services: []statusService{{ServiceName: "svc-a", ServiceType: "service", Components: []statusComponent{{Status: "running"}}}}}}, false); !errors.Is(err, writeErr) {
		t.Fatalf("renderStatusTables row error = %v, want %v", err, writeErr)
	}

	if got := dockerAggregateStatus(nil); got != "(0) stopped" {
		t.Fatalf("dockerAggregateStatus nil = %q", got)
	}
	if got := formatStatusContainers([]statusComponent{{}, {Name: "web"}}); got != "web" {
		t.Fatalf("formatStatusContainers = %q, want web", got)
	}
	if got := formatStatusContainers([]statusComponent{{}}); got != "-" {
		t.Fatalf("formatStatusContainers empty names = %q, want -", got)
	}
	if got := truncateStatusContainers(strings.Repeat("a", statusContainersMaxWidth+1)); !strings.HasSuffix(got, "...") {
		t.Fatalf("truncateStatusContainers = %q, want ellipsis", got)
	}
}

func TestSvcHandleStatusRemoteAndParseErrors(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "svc-a"

	if err := handleStatusCommand(context.Background(), []string{"--unknown"}, nil, false); err == nil {
		t.Fatalf("expected status parse error")
	}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		return nil
	}
	if err := handleStatusCommand(context.Background(), []string{"--format=json"}, nil, false); err != nil {
		t.Fatalf("handleStatusCommand json error: %v", err)
	}
	if !reflect.DeepEqual(gotArgs, []string{"status", "--format=json"}) {
		t.Fatalf("status remote args = %#v", gotArgs)
	}

	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		return nil, errors.New("status failed")
	}
	if err := renderStatusTableForService(context.Background(), "host-a", "svc-a"); err == nil || !strings.Contains(err.Error(), "status failed") {
		t.Fatalf("renderStatusTableForService remote error = %v", err)
	}
	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		return []byte(`bad-json`), nil
	}
	if err := renderStatusTableForService(context.Background(), "host-a", "svc-a"); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("renderStatusTableForService JSON error = %v", err)
	}
}

func TestSvcSaveConfigEarlyReturnsAndCreation(t *testing.T) {
	preserveSvcCommandGlobals(t)
	useTempSvcCwd(t)

	serviceOverride = ""
	if err := saveRunConfig(nil, "", "payload", []string{"--pull"}); err != nil {
		t.Fatalf("saveRunConfig no service error: %v", err)
	}
	if err := saveCronConfig(nil, "", "payload", []string{"0", "9", "*", "*", "*"}, nil); err != nil {
		t.Fatalf("saveCronConfig no service error: %v", err)
	}

	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	payload := "run.sh"
	if err := saveRunConfig(nil, "", payload, []string{"--", "--app-flag"}); err != nil {
		t.Fatalf("saveRunConfig create error: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok || entry.Payload != "run.sh" || !reflect.DeepEqual(entry.Args, []string{"--app-flag"}) {
		t.Fatalf("run entry = %#v, ok=%v", entry, ok)
	}

	if err := saveCronConfig(loaded, "host-b", payload, []string{"0", "9", "*", "*", "*"}, []string{" "}); err != nil {
		t.Fatalf("saveCronConfig error: %v", err)
	}
	entry, ok = loaded.Config.ServiceEntry("svc-a", "host-b")
	if !ok || entry.Type != serviceTypeCron || entry.Schedule != "0 9 * * *" || len(entry.Args) != 0 {
		t.Fatalf("cron entry = %#v, ok=%v", entry, ok)
	}
}
