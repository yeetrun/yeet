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
	"slices"
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
	oldFetchServiceInfoForSync := fetchServiceInfoForSyncFn
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
		fetchServiceInfoForSyncFn = oldFetchServiceInfoForSync
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
	needs, err = commandNeedsService([]string{"docker", "update", "svc-a", "svc-b"})
	if err != nil {
		t.Fatalf("commandNeedsService docker update services error: %v", err)
	}
	if needs {
		t.Fatalf("docker update with inline services needs service = true, want false")
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
	if _, err := parseSvcRun([]string{"app", "--service-root"}, nil, ""); err == nil || !strings.Contains(err.Error(), "--service-root requires a value") {
		t.Fatalf("parseSvcRun service-root error = %v", err)
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

func TestSvcRunPayloadOnlyReusesStoredRunArgs(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "jellyfin"
	loc := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{
		Name:    "jellyfin",
		Host:    Host(),
		Type:    serviceTypeRun,
		Payload: "jellyfin/compose.yml",
		Args:    []string{"--net=svc,ts", "--ts-tags=tag:app", "--pull", "--app-flag"},
	})

	run, err := parseSvcRun([]string{"jellyfin/compose.yml"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun payload-only redeploy error: %v", err)
	}
	wantArgs := []string{"--net=svc,ts", "--ts-tags=tag:app", "--pull", "--", "--app-flag"}
	if !reflect.DeepEqual(run.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", run.Args, wantArgs)
	}
}

func TestSvcRunExplicitArgsInheritStoredLockedRunFlags(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "jellyfin"
	loc := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{
		Name:    "jellyfin",
		Host:    Host(),
		Type:    serviceTypeRun,
		Payload: "jellyfin/compose.yml",
		Args:    []string{"--net=svc,ts", "--ts-tags=tag:app"},
	})

	run, err := parseSvcRun([]string{"jellyfin/compose.yml", "--pull"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun explicit arg redeploy error: %v", err)
	}
	wantArgs := []string{"--net=svc,ts", "--ts-tags=tag:app", "--pull"}
	if !reflect.DeepEqual(run.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", run.Args, wantArgs)
	}
}

func TestSvcRunServiceRoot(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := t.TempDir()
	serviceOverride = "svc-a"
	loc := &projectConfigLocation{Dir: tmp, Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{
		Name:        "svc-a",
		Host:        Host(),
		Type:        serviceTypeRun,
		Payload:     "app",
		ServiceRoot: "/srv/apps/stored",
		Args:        []string{"--pull"},
	})

	run, err := parseSvcRun([]string{"app", "--env-file", "prod.env", "--force", "--", "--app-flag"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun stored service-root error: %v", err)
	}
	if run.ServiceRoot != "/srv/apps/stored" || run.ServiceRootArg != "" || run.ServiceRootSet {
		t.Fatalf("stored service root fields = %#v", run)
	}
	if !reflect.DeepEqual(run.Args, []string{"--service-root=/srv/apps/stored", "--", "--app-flag"}) {
		t.Fatalf("Args = %#v", run.Args)
	}

	run, err = parseSvcRun([]string{"app", "--service-root", "/srv/apps/explicit", "--pull"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun explicit service-root error: %v", err)
	}
	if run.ServiceRoot != "/srv/apps/explicit" || run.ServiceRootArg != "/srv/apps/explicit" || !run.ServiceRootSet {
		t.Fatalf("explicit service root fields = %#v", run)
	}
	if !reflect.DeepEqual(run.Args, []string{"--service-root=/srv/apps/explicit", "--pull"}) {
		t.Fatalf("Args = %#v", run.Args)
	}
}

func TestSvcRunZFSServiceRoot(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = "app"
	loc := &projectConfigLocation{
		Dir: t.TempDir(),
		Config: &ProjectConfig{
			Services: []ServiceEntry{{
				Name:           "app",
				Host:           Host(),
				ServiceRoot:    "tank/apps/stored",
				ServiceRootZFS: true,
			}},
		},
	}
	run, err := parseSvcRun([]string{"app", "--pull"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun stored zfs service-root: %v", err)
	}
	if run.ServiceRoot != "tank/apps/stored" || !run.ServiceRootZFS {
		t.Fatalf("run root = %q zfs=%v, want tank/apps/stored true", run.ServiceRoot, run.ServiceRootZFS)
	}
	if !reflect.DeepEqual(run.Args, []string{"--service-root=tank/apps/stored", "--zfs", "--pull"}) {
		t.Fatalf("run args = %#v", run.Args)
	}
	run, err = parseSvcRun([]string{"app", "--service-root", "tank/apps/explicit", "--zfs"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun explicit zfs service-root: %v", err)
	}
	if run.ServiceRootArg != "tank/apps/explicit" || !run.ServiceRootZFSArg || !run.ServiceRootSet {
		t.Fatalf("explicit root = %q zfsArg=%v set=%v", run.ServiceRootArg, run.ServiceRootZFSArg, run.ServiceRootSet)
	}
}

func TestSvcRunPreservesServiceRootPayloadArgsInSavedConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	tryRunRemoteImageFn = func(image string, args []string) (bool, error) {
		if image != "ghcr.io/example/app:latest" {
			t.Fatalf("image = %q, want ghcr.io/example/app:latest", image)
		}
		if !reflect.DeepEqual(args, []string{"--", "--service-root", "/tmp/app"}) {
			t.Fatalf("run args = %#v, want payload service-root args after delimiter", args)
		}
		return true, nil
	}

	if err := HandleSvcCmd([]string{"run", "ghcr.io/example/app:latest", "--", "--service-root", "/tmp/app"}); err != nil {
		t.Fatalf("HandleSvcCmd run error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected saved service entry")
	}
	if entry.ServiceRoot != "" {
		t.Fatalf("ServiceRoot = %q, want empty", entry.ServiceRoot)
	}
	wantArgs := []string{"--service-root", "/tmp/app"}
	if !reflect.DeepEqual(entry.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", entry.Args, wantArgs)
	}
}

func TestServiceSetUpdatesExistingConfigOnly(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"

	var calls []struct {
		args []string
		tty  bool
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls = append(calls, struct {
			args []string
			tty  bool
		}{append([]string{}, args...), tty})
		return nil
	}
	isTerminalFn = func(int) bool { return false }

	updated, err := saveServiceSetConfig(nil, "host-a", cli.ServiceSetFlags{ServiceRoot: "/srv/apps/missing"})
	if err != nil {
		t.Fatalf("saveServiceSetConfig nil config error: %v", err)
	}
	if updated {
		t.Fatalf("saveServiceSetConfig nil config updated = true, want false")
	}

	SetHostOverride("host-b")
	out, err := captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "set", "--service-root=/srv/apps/missing"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd missing config error: %v", err)
	}
	if !strings.Contains(out, "No matching yeet.toml entry was updated") ||
		!strings.Contains(out, "yeet --host host-b service sync svc-a --config ~/yeet-services/yeet.toml") {
		t.Fatalf("HandleSvcCmd missing config output = %q, want host-qualified sync hint", out)
	}
	resetHostOverride()
	loadedPrefs.DefaultHost = "host-a"
	if _, err := os.Stat(filepath.Join(tmp, projectConfigName)); !os.IsNotExist(err) {
		t.Fatalf("service set without existing entry should not create yeet.toml, stat err=%v", err)
	}

	missingLoc := &projectConfigLocation{
		Path:   filepath.Join(tmp, "missing-entry.toml"),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	updated, err = saveServiceSetConfig(missingLoc, "host-a", cli.ServiceSetFlags{ServiceRoot: "/srv/apps/missing"})
	if err != nil {
		t.Fatalf("saveServiceSetConfig missing entry error: %v", err)
	}
	if updated {
		t.Fatalf("saveServiceSetConfig missing entry updated = true, want false")
	}

	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:    "svc-other",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "other.sh",
	})
	out, err = captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "set", "--service-root=/srv/apps/missing", "--copy"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd missing entry config error: %v", err)
	}
	if !strings.Contains(out, "No matching yeet.toml entry was updated") ||
		!strings.Contains(out, "yeet service sync svc-a --config ~/yeet-services/yeet.toml") {
		t.Fatalf("HandleSvcCmd missing entry config output = %q, want non-host sync hint", out)
	}

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}
	updated, err = saveServiceSetConfig(loc, "host-a", cli.ServiceSetFlags{ServiceRoot: "tank/apps/direct", ZFS: true})
	if err != nil {
		t.Fatalf("saveServiceSetConfig matching entry error: %v", err)
	}
	if !updated {
		t.Fatalf("saveServiceSetConfig matching entry updated = false, want true")
	}

	out, err = captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "set", "--service-root=tank/apps/svc-a", "--zfs", "--copy"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd existing config error: %v", err)
	}
	if strings.Contains(out, "No matching yeet.toml entry was updated") {
		t.Fatalf("HandleSvcCmd existing config output = %q, want no sync hint for matching config", out)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok || entry.ServiceRoot != "tank/apps/svc-a" || !entry.ServiceRootZFS {
		t.Fatalf("entry = %#v, want zfs service root", entry)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if !strings.Contains(string(raw), `service_root_zfs = true`) {
		t.Fatalf("saved config = %q, want service_root_zfs", string(raw))
	}

	out, err = captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "set", "--service-root=/srv/apps/svc-a", "--copy"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd existing config non-zfs error: %v", err)
	}
	if strings.Contains(out, "No matching yeet.toml entry was updated") {
		t.Fatalf("HandleSvcCmd existing config non-zfs output = %q, want no sync hint for matching config", out)
	}
	loaded, err = loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd non-zfs error: %v", err)
	}
	entry, ok = loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok || entry.ServiceRoot != "/srv/apps/svc-a" || entry.ServiceRootZFS {
		t.Fatalf("entry = %#v, want non-zfs service root", entry)
	}
	raw, err = os.ReadFile(filepath.Join(tmp, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile non-zfs config: %v", err)
	}
	if strings.Contains(string(raw), `service_root_zfs`) {
		t.Fatalf("saved config = %q, want service_root_zfs omitted", string(raw))
	}
	if len(calls) != 4 || calls[0].tty || calls[1].tty || calls[2].tty || calls[3].tty {
		t.Fatalf("remote calls = %#v, want four non-tty calls", calls)
	}
}

func TestServiceSetUpdatesSnapshotConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return nil
	}
	isTerminalFn = func(int) bool { return false }
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
	if err := HandleSvcCmd([]string{"service", "set", "--snapshots=off", "--snapshot-keep-last=3", "--snapshot-max-age=72h", "--snapshot-required=false"}); err != nil {
		t.Fatalf("HandleSvcCmd: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || *entry.SnapshotRequired {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestServiceSetRemoteFailureDoesNotUpdateSnapshotConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	remoteErr := errors.New("remote failed")
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return remoteErr
	}
	isTerminalFn = func(int) bool { return false }
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
	if err := HandleSvcCmd([]string{"service", "set", "--snapshots=off"}); !errors.Is(err, remoteErr) {
		t.Fatalf("HandleSvcCmd error = %v, want %v", err, remoteErr)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if serviceEntryHasSnapshotOverride(entry) {
		t.Fatalf("entry = %#v, want no local snapshot update after remote failure", entry)
	}
}

func TestServiceSetInvalidSnapshotConfigDoesNotRunRemote(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("unexpected remote exec: service=%q args=%v", service, args)
		return nil
	}
	isTerminalFn = func(int) bool { return false }
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
	err := HandleSvcCmd([]string{"service", "set", "--service-root=/srv/app", "--copy", "--snapshot-keep-last=bad"})
	if err == nil || !strings.Contains(err.Error(), "--snapshot-keep-last must be a positive integer") {
		t.Fatalf("HandleSvcCmd error = %v, want snapshot keep-last validation", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.ServiceRoot != "" || serviceEntryHasSnapshotOverride(entry) {
		t.Fatalf("entry = %#v, want no local update", entry)
	}
}

func TestServiceSetSnapshotInheritWithFieldFlagDoesNotRunRemoteOrSaveConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("unexpected remote exec: service=%q args=%v", service, args)
		return nil
	}
	isTerminalFn = func(int) bool { return false }
	required := true
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "run.sh",
		Snapshots:        "off",
		SnapshotKeepLast: 3,
		SnapshotMaxAge:   "72h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run"},
	})
	err := HandleSvcCmd([]string{"service", "set", "--snapshots=inherit", "--snapshot-keep-last=bad"})
	if err == nil || !strings.Contains(err.Error(), "--snapshots=inherit cannot be combined with field-level snapshot flags") {
		t.Fatalf("HandleSvcCmd error = %v, want mutually exclusive snapshot flags", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || !*entry.SnapshotRequired || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) {
		t.Fatalf("entry = %#v, want unchanged snapshot config", entry)
	}
}

func TestParseSvcRunControlFlagsExtractsSnapshotOptions(t *testing.T) {
	flags, err := parseSvcRunControlFlags([]string{
		"--pull",
		"--snapshots=off",
		"--snapshot-keep-last", "3",
		"--snapshot-max-age=72h",
		"--snapshot-required=false",
		"--snapshot-events=run,docker-update",
		"--", "--snapshot-events=payload",
	})
	if err != nil {
		t.Fatalf("parseSvcRunControlFlags: %v", err)
	}
	if !flags.SnapshotChange {
		t.Fatal("SnapshotChange = false, want true")
	}
	if flags.Snapshots != "off" || flags.SnapshotKeepLast != 3 || flags.SnapshotMaxAge != "72h" || flags.SnapshotRequired == nil || *flags.SnapshotRequired {
		t.Fatalf("snapshot flags = %#v", flags)
	}
	if !reflect.DeepEqual(flags.SnapshotEvents, []string{"run", "docker-update"}) {
		t.Fatalf("SnapshotEvents = %#v", flags.SnapshotEvents)
	}
	wantArgs := []string{"--pull", "--", "--snapshot-events=payload"}
	if !reflect.DeepEqual(flags.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", flags.Args, wantArgs)
	}
}

func TestParseSvcRunControlFlagsExtractsSnapshotFieldInherit(t *testing.T) {
	flags, err := parseSvcRunControlFlags([]string{
		"--snapshots=off",
		"--snapshot-keep-last=inherit",
		"--snapshot-max-age=inherit",
		"--snapshot-required=inherit",
		"--snapshot-events=inherit",
		"--", "--snapshot-events=payload",
	})
	if err != nil {
		t.Fatalf("parseSvcRunControlFlags: %v", err)
	}
	if !flags.SnapshotChange {
		t.Fatal("SnapshotChange = false, want true")
	}
	if flags.Snapshots != "off" || !flags.SnapshotKeepLastInherit || !flags.SnapshotMaxAgeInherit || !flags.SnapshotRequiredInherit || !flags.SnapshotEventsInherit {
		t.Fatalf("snapshot inherit flags = %#v", flags)
	}
	if flags.SnapshotKeepLast != 0 || flags.SnapshotMaxAge != "" || flags.SnapshotRequired != nil || len(flags.SnapshotEvents) != 0 {
		t.Fatalf("snapshot values = %#v, want cleared values", flags)
	}
	wantArgs := []string{"--", "--snapshot-events=payload"}
	if !reflect.DeepEqual(flags.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", flags.Args, wantArgs)
	}
}

func TestParseSvcRunControlFlagsExplicitSnapshotFieldOverridesInherit(t *testing.T) {
	flags, err := parseSvcRunControlFlags([]string{
		"--snapshot-keep-last=inherit",
		"--snapshot-keep-last=4",
		"--snapshot-max-age=inherit",
		"--snapshot-max-age=48h",
		"--snapshot-required=inherit",
		"--snapshot-required=false",
		"--snapshot-events=inherit",
		"--snapshot-events=run,docker-update",
	})
	if err != nil {
		t.Fatalf("parseSvcRunControlFlags: %v", err)
	}
	if flags.SnapshotKeepLastInherit || flags.SnapshotMaxAgeInherit || flags.SnapshotRequiredInherit || flags.SnapshotEventsInherit {
		t.Fatalf("snapshot inherit flags = %#v, want explicit values", flags)
	}
	if flags.SnapshotKeepLast != 4 || flags.SnapshotMaxAge != "48h" || flags.SnapshotRequired == nil || *flags.SnapshotRequired {
		t.Fatalf("snapshot values = %#v, want explicit values", flags)
	}
	if !reflect.DeepEqual(flags.SnapshotEvents, []string{"run", "docker-update"}) {
		t.Fatalf("SnapshotEvents = %#v", flags.SnapshotEvents)
	}
}

func TestSvcRunSnapshotInheritWithFieldFlagDoesNotRunRemoteOrSaveConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	tryRunRemoteImageFn = func(image string, args []string) (bool, error) {
		t.Fatalf("unexpected remote run: image=%q args=%v", image, args)
		return false, nil
	}
	required := true
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "old.sh",
		Snapshots:        "off",
		SnapshotKeepLast: 3,
		SnapshotMaxAge:   "72h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run"},
	})

	err := HandleSvcCmd([]string{"run", "ghcr.io/example/app:latest", "--snapshots=inherit", "--snapshot-keep-last=3"})
	if err == nil || !strings.Contains(err.Error(), "--snapshots=inherit cannot be combined with field-level snapshot flags") {
		t.Fatalf("HandleSvcCmd error = %v, want mutually exclusive snapshot flags", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || !*entry.SnapshotRequired || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) {
		t.Fatalf("entry = %#v, want unchanged snapshot config", entry)
	}
}

func TestSvcRunSnapshotFieldInheritRunsRemoteAndSavesConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	required := true
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "old.sh",
		Snapshots:        "on",
		SnapshotKeepLast: 3,
		SnapshotMaxAge:   "72h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run"},
	})
	var gotArgs []string
	tryRunRemoteImageFn = func(image string, args []string) (bool, error) {
		if image != "ghcr.io/example/app:latest" {
			t.Fatalf("image = %q, want ghcr.io/example/app:latest", image)
		}
		gotArgs = append([]string{}, args...)
		return true, nil
	}

	err := HandleSvcCmd([]string{
		"run",
		"ghcr.io/example/app:latest",
		"--snapshots=off",
		"--snapshot-keep-last=inherit",
		"--snapshot-max-age=inherit",
		"--snapshot-required=inherit",
		"--snapshot-events=inherit",
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd run error: %v", err)
	}
	for _, want := range []string{
		"--snapshots=off",
		"--snapshot-keep-last=inherit",
		"--snapshot-max-age=inherit",
		"--snapshot-required=inherit",
		"--snapshot-events=inherit",
	} {
		if !slices.Contains(gotArgs, want) {
			t.Fatalf("run args = %#v, missing %q", gotArgs, want)
		}
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 0 || entry.SnapshotMaxAge != "" || entry.SnapshotRequired != nil || len(entry.SnapshotEvents) != 0 {
		t.Fatalf("entry = %#v, want only snapshots=off", entry)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if strings.Contains(string(raw), "inherit") {
		t.Fatalf("saved config = %q, want no literal inherit", string(raw))
	}
}

func TestSvcRunExplicitSnapshotFlagsDeployWhenConfigAlreadyMatches(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found:   true,
			Payload: &catchrpc.ArtifactHash{Kind: "script", SHA256: payloadHash},
		}, true, nil
	}
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:      "svc-a",
		Host:      "host-a",
		Type:      serviceTypeRun,
		Payload:   "run.sh",
		Snapshots: "off",
	})
	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		gotArgs = append([]string{}, args...)
		if stdin == nil {
			t.Fatal("stdin = nil, want payload")
		}
		return nil
	}

	if err := HandleSvcCmd([]string{"run", payload, "--snapshots=off"}); err != nil {
		t.Fatalf("HandleSvcCmd run error: %v", err)
	}
	if !reflect.DeepEqual(gotArgs, []string{"run", "--snapshots=off"}) {
		t.Fatalf("remote args = %#v, want run --snapshots=off", gotArgs)
	}
}

func TestParseSvcRunControlFlagsRejectsMissingSnapshotValues(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "max age followed by long flag",
			args:    []string{"--snapshot-max-age", "--pull"},
			wantErr: "--snapshot-max-age requires a value",
		},
		{
			name:    "events followed by terminator",
			args:    []string{"--snapshot-events", "--", "--app-flag"},
			wantErr: "--snapshot-events requires a value",
		},
		{
			name:    "required followed by short flag",
			args:    []string{"--snapshot-required", "-x"},
			wantErr: "--snapshot-required requires a value",
		},
		{
			name:    "snapshots followed by long flag",
			args:    []string{"--snapshots", "--pull"},
			wantErr: "--snapshots must be on, off, or inherit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseSvcRunControlFlags(tt.args); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parseSvcRunControlFlags(%v) error = %v, want %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestParseSvcRunControlFlagsRejectsEmptySnapshotEvents(t *testing.T) {
	tests := [][]string{
		{"--snapshot-events=,"},
		{"--snapshot-events=run,"},
		{"--snapshot-events", ",docker-update"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseSvcRunControlFlags(args); err == nil || !strings.Contains(err.Error(), "snapshot events must not contain empty values") {
				t.Fatalf("parseSvcRunControlFlags(%v) error = %v, want empty event error", args, err)
			}
		})
	}
}

func TestParseSvcRunControlFlagsKeepsTerminatorForNonSnapshotArgs(t *testing.T) {
	flags, err := parseSvcRunControlFlags([]string{"--pull", "--", "--snapshot-max-age", "--app-flag"})
	if err != nil {
		t.Fatalf("parseSvcRunControlFlags: %v", err)
	}
	wantArgs := []string{"--pull", "--", "--snapshot-max-age", "--app-flag"}
	if !reflect.DeepEqual(flags.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", flags.Args, wantArgs)
	}
	if flags.SnapshotChange {
		t.Fatal("SnapshotChange = true, want false")
	}
}

func TestSaveRunConfigPreservesSnapshotOverrides(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	required := false
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "old.sh",
		Snapshots:        "off",
		SnapshotKeepLast: 4,
		SnapshotMaxAge:   "48h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run"},
	})
	if err := saveRunConfig(nil, "", filepath.Join(tmp, "new.sh"), []string{"--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 4 || entry.SnapshotMaxAge != "48h" || entry.SnapshotRequired == nil || *entry.SnapshotRequired || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) {
		t.Fatalf("entry = %#v, want preserved snapshot overrides", entry)
	}
}

func TestSaveRunConfigPatchesSnapshotOverrides(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	required := true
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "old.sh",
		Snapshots:        "on",
		SnapshotKeepLast: 7,
		SnapshotMaxAge:   "168h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run", "docker-update"},
	})
	if err := saveRunConfig(nil, "", filepath.Join(tmp, "new.sh"), []string{"--snapshots=off", "--snapshot-max-age=24h", "--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" ||
		entry.SnapshotKeepLast != 7 ||
		entry.SnapshotMaxAge != "24h" ||
		entry.SnapshotRequired == nil ||
		!*entry.SnapshotRequired ||
		!reflect.DeepEqual(entry.SnapshotEvents, []string{"run", "docker-update"}) {
		t.Fatalf("entry = %#v, want patched snapshot fields", entry)
	}
}

func TestSaveRunConfigFieldInheritPreservesOtherSnapshotOverrides(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	required := true
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "old.sh",
		Snapshots:        "on",
		SnapshotKeepLast: 7,
		SnapshotMaxAge:   "168h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run", "docker-update"},
	})
	if err := saveRunConfig(nil, "", filepath.Join(tmp, "new.sh"), []string{"--snapshot-keep-last=inherit", "--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "on" ||
		entry.SnapshotKeepLast != 0 ||
		entry.SnapshotMaxAge != "168h" ||
		entry.SnapshotRequired == nil ||
		!*entry.SnapshotRequired ||
		!reflect.DeepEqual(entry.SnapshotEvents, []string{"run", "docker-update"}) {
		t.Fatalf("entry = %#v, want only keep-last cleared", entry)
	}
}

func TestSaveRunConfigInheritClearsSnapshotOverrides(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	required := true
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "old.sh",
		Snapshots:        "on",
		SnapshotKeepLast: 7,
		SnapshotMaxAge:   "168h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run", "docker-update"},
	})
	if err := saveRunConfig(nil, "", filepath.Join(tmp, "new.sh"), []string{"--snapshots=inherit", "--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if serviceEntryHasSnapshotOverride(entry) {
		t.Fatalf("entry = %#v, want cleared snapshot overrides", entry)
	}
}

func TestServiceSetInheritClearsSnapshotConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	required := true
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return nil
	}
	isTerminalFn = func(int) bool { return false }
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:             "svc-a",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "run.sh",
		Snapshots:        "on",
		SnapshotKeepLast: 5,
		SnapshotMaxAge:   "24h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run"},
	})
	if err := HandleSvcCmd([]string{"service", "set", "--snapshots=inherit"}); err != nil {
		t.Fatalf("HandleSvcCmd: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if serviceEntryHasSnapshotOverride(entry) {
		t.Fatalf("entry = %#v, want cleared snapshot overrides", entry)
	}
}

func TestServiceSetSnapshotFieldInheritClearsOnlyLocalField(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		assert func(t *testing.T, entry ServiceEntry)
	}{
		{
			name: "keep last",
			flag: "--snapshot-keep-last=inherit",
			assert: func(t *testing.T, entry ServiceEntry) {
				t.Helper()
				if entry.SnapshotKeepLast != 0 {
					t.Fatalf("SnapshotKeepLast = %d, want 0", entry.SnapshotKeepLast)
				}
				if entry.Snapshots != "off" || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || !*entry.SnapshotRequired || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) {
					t.Fatalf("entry = %#v, want only keep-last cleared", entry)
				}
			},
		},
		{
			name: "max age",
			flag: "--snapshot-max-age=inherit",
			assert: func(t *testing.T, entry ServiceEntry) {
				t.Helper()
				if entry.SnapshotMaxAge != "" {
					t.Fatalf("SnapshotMaxAge = %q, want empty", entry.SnapshotMaxAge)
				}
				if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotRequired == nil || !*entry.SnapshotRequired || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) {
					t.Fatalf("entry = %#v, want only max-age cleared", entry)
				}
			},
		},
		{
			name: "required",
			flag: "--snapshot-required=inherit",
			assert: func(t *testing.T, entry ServiceEntry) {
				t.Helper()
				if entry.SnapshotRequired != nil {
					t.Fatalf("SnapshotRequired = %#v, want nil", entry.SnapshotRequired)
				}
				if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) {
					t.Fatalf("entry = %#v, want only required cleared", entry)
				}
			},
		},
		{
			name: "events",
			flag: "--snapshot-events=inherit",
			assert: func(t *testing.T, entry ServiceEntry) {
				t.Helper()
				if len(entry.SnapshotEvents) != 0 {
					t.Fatalf("SnapshotEvents = %#v, want empty", entry.SnapshotEvents)
				}
				if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || !*entry.SnapshotRequired {
					t.Fatalf("entry = %#v, want only events cleared", entry)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveSvcCommandGlobals(t)
			tmp := useTempSvcCwd(t)
			serviceOverride = "svc-a"
			loadedPrefs.DefaultHost = "host-a"
			remoteCalled := false
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				remoteCalled = true
				return nil
			}
			isTerminalFn = func(int) bool { return false }
			required := true
			writeSvcBranchConfig(t, tmp, ServiceEntry{
				Name:             "svc-a",
				Host:             "host-a",
				Type:             serviceTypeRun,
				Payload:          "run.sh",
				Snapshots:        "off",
				SnapshotKeepLast: 3,
				SnapshotMaxAge:   "72h",
				SnapshotRequired: &required,
				SnapshotEvents:   []string{"run"},
			})
			if err := HandleSvcCmd([]string{"service", "set", tt.flag}); err != nil {
				t.Fatalf("HandleSvcCmd: %v", err)
			}
			if !remoteCalled {
				t.Fatal("remote exec was not called")
			}
			loaded, err := loadProjectConfigFromCwd()
			if err != nil {
				t.Fatalf("loadProjectConfigFromCwd: %v", err)
			}
			entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
			if !ok {
				t.Fatal("missing service entry")
			}
			tt.assert(t, entry)
			raw, err := os.ReadFile(filepath.Join(tmp, projectConfigName))
			if err != nil {
				t.Fatalf("ReadFile config: %v", err)
			}
			if strings.Contains(string(raw), "inherit") {
				t.Fatalf("saved config = %q, want no literal inherit", string(raw))
			}
		})
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
	if err := saveRunConfig(nil, "", "payload", []string{"--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig no service error: %v", err)
	}
	if err := saveCronConfig(nil, "", "payload", []string{"0", "9", "*", "*", "*"}, nil); err != nil {
		t.Fatalf("saveCronConfig no service error: %v", err)
	}

	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	payload := "run.sh"
	if err := saveRunConfig(nil, "", payload, []string{"--", "--app-flag"}, "", false); err != nil {
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
