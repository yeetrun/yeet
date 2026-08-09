// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestHandleSvcCmdUsesConfigHost(t *testing.T) {
	oldExec := execRemoteFn
	oldExecOutput := execRemoteOutputFn
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	defer func() {
		execRemoteFn = oldExec
		execRemoteOutputFn = oldExecOutput
		serviceOverride = oldService
		loadedPrefs = oldPrefs
		resetHostOverride()
	}()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "compose.yml",
	})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}

	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "catch"

	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called for status table rendering")
		return nil
	}
	called := false
	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		called = true
		if host != "host-a" {
			t.Fatalf("host = %q, want host-a", host)
		}
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		return []byte(`[{"serviceName":"svc-a","serviceType":"service","components":[{"name":"svc-a","status":"running"}]}]`), nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	os.Stdout = w
	runErr := HandleSvcCmd([]string{"status"})
	_ = w.Close()
	os.Stdout = oldStdout

	if _, readErr := io.ReadAll(r); readErr != nil {
		t.Fatalf("ReadAll error: %v", readErr)
	}
	if runErr != nil {
		t.Fatalf("HandleSvcCmd error: %v", err)
	}
	if !called {
		t.Fatalf("expected execRemoteOutputFn to be called")
	}
	if host, ok := HostOverride(); !ok || host != "host-a" {
		t.Fatalf("host override = %q ok=%v, want host-a true", host, ok)
	}
	if loadedPrefs.DefaultHost != "catch" {
		t.Fatalf("saved default host = %q, want catch", loadedPrefs.DefaultHost)
	}
}

func TestRunFromProjectConfigRehydratesArgs(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldPush := pushAllLocalImagesFn
	oldService := serviceOverride
	oldIsTerminal := isTerminalFn
	oldHashes := fetchRemoteArtifactHashesFn
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		pushAllLocalImagesFn = oldPush
		serviceOverride = oldService
		isTerminalFn = oldIsTerminal
		fetchRemoteArtifactHashesFn = oldHashes
	}()

	serviceOverride = "rssbot"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	pushAllLocalImagesFn = func(context.Context, string, string, string) error {
		return nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "rssbot", "rssbot")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:    "rssbot",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: filepath.Join("rssbot", "rssbot"),
		Args:    []string{"-allowed-chats=314073886,135155078"},
	})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if service != "rssbot" {
			t.Fatalf("service = %q, want rssbot", service)
		}
		if stdin == nil {
			t.Fatalf("expected stdin payload to be provided")
		}
		if tty {
			t.Fatalf("expected tty=false, got true")
		}
		return nil
	}

	if err := runFromProjectConfig(loc, "host-a"); err != nil {
		t.Fatalf("runFromProjectConfig error: %v", err)
	}

	wantArgs := []string{"run", "--", "-allowed-chats=314073886,135155078"}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, gotArgs[i], wantArgs[i])
		}
	}
}

func TestRunFromProjectConfigRehydratesRunAs(t *testing.T) {
	oldExec, oldArch, oldService, oldTerminal, oldHashes := execRemoteFn, remoteCatchOSAndArchFn, serviceOverride, isTerminalFn, fetchRemoteArtifactHashesFn
	t.Cleanup(func() {
		execRemoteFn, remoteCatchOSAndArchFn, serviceOverride, isTerminalFn, fetchRemoteArtifactHashesFn = oldExec, oldArch, oldService, oldTerminal, oldHashes
	})
	serviceOverride = "api"
	remoteCatchOSAndArchFn = func() (string, string, error) { return "linux", "amd64", nil }
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	isTerminalFn = func(int) bool { return false }
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "api"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: &ProjectConfig{Version: 1, Services: []ServiceEntry{{Name: "api", Host: "host-a", Payload: "api", RunAs: "app:workers"}}}}
	var got []string
	execRemoteFn = func(_ context.Context, _ string, args []string, _ io.Reader, _ bool) error {
		got = append([]string{}, args...)
		return nil
	}
	if err := runFromProjectConfig(loc, "host-a"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"run", "--run-as=app:workers"}) {
		t.Fatalf("args = %#v", got)
	}
}

func TestScheduledRunUsesNormalPipelineForExplicitAndConfiguredPayload(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	oldTerminal := isTerminalFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldInfo := fetchRunChangeServiceInfoFn
	t.Cleanup(func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		isTerminalFn = oldTerminal
		fetchRemoteArtifactHashesFn = oldHashes
		fetchRunChangeServiceInfoFn = oldInfo
	})

	serviceOverride = "owesplit"
	remoteCatchOSAndArchFn = func() (string, string, error) { return "linux", "amd64", nil }
	isTerminalFn = func(int) bool { return false }
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "owesplit")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{"run", "--cron=0 9 15 * *", "--run-as=yeet-svc", "--net=iso", "--", "-live"}

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "explicit payload", args: []string{payload}},
		{name: "config only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loc := &projectConfigLocation{
				Path: filepath.Join(tmp, tt.name+"-"+projectConfigName),
				Dir:  tmp,
				Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
					Name: "owesplit", Host: "host-a", Payload: "owesplit", Schedule: "0 9 15 * *", RunAs: "yeet-svc",
					Args: []string{"--net=iso", "--", "-live"},
				}}},
			}

			var calls [][]string
			execRemoteFn = func(_ context.Context, service string, args []string, stdin io.Reader, _ bool) error {
				if service != "owesplit" || stdin == nil {
					t.Fatalf("remote call = service %q stdin %v", service, stdin)
				}
				calls = append(calls, append([]string{}, args...))
				return nil
			}
			req := svcCommandRequest{
				Command:      svcCommand{Args: tt.args},
				Config:       loc,
				HostOverride: "host-a",
				Service:      "owesplit",
			}
			if err := handleSvcRun(req); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || len(calls[0]) < len(wantPrefix) || !reflect.DeepEqual(calls[0][:len(wantPrefix)], wantPrefix) {
				t.Fatalf("remote calls = %#v, want one call beginning %#v", calls, wantPrefix)
			}
		})
	}
}

func TestScheduledConfigOnlyRunResolvesImageLookingNativePathFromConfigDirectory(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	oldTerminal := isTerminalFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldInfo := fetchRunChangeServiceInfoFn
	t.Cleanup(func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		isTerminalFn = oldTerminal
		fetchRemoteArtifactHashesFn = oldHashes
		fetchRunChangeServiceInfoFn = oldInfo
	})

	serviceOverride = "scheduled-path"
	remoteCatchOSAndArchFn = func() (string, string, error) { return "linux", "amd64", nil }
	isTerminalFn = func(int) bool { return false }
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	configDir := t.TempDir()
	childDir := filepath.Join(configDir, "nested", "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(childDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	for _, payload := range []string{"jobs/backup:daily", "jobs/worker@v2"} {
		t.Run(payload, func(t *testing.T) {
			absolute := filepath.Join(configDir, payload)
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				t.Fatal(err)
			}
			wantPayload := "#!/bin/sh\necho " + payload + "\n"
			if err := os.WriteFile(absolute, []byte(wantPayload), 0o755); err != nil {
				t.Fatal(err)
			}
			loc := &projectConfigLocation{
				Path: filepath.Join(configDir, projectConfigName),
				Dir:  configDir,
				Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
					Name: "scheduled-path", Host: "host-a", Payload: payload, Schedule: "0 9 15 * *",
				}}},
			}

			var gotArgs []string
			gotPayload := ""
			execRemoteFn = func(_ context.Context, service string, args []string, stdin io.Reader, _ bool) error {
				if service != "scheduled-path" {
					t.Fatalf("service = %q", service)
				}
				gotArgs = append([]string{}, args...)
				raw, err := io.ReadAll(stdin)
				if err != nil {
					return err
				}
				gotPayload = string(raw)
				return nil
			}
			if err := runFromProjectConfig(loc, "host-a"); err != nil {
				t.Fatalf("runFromProjectConfig: %v", err)
			}
			if !reflect.DeepEqual(gotArgs, []string{"run", "--cron=0 9 15 * *"}) {
				t.Fatalf("remote args = %#v", gotArgs)
			}
			if gotPayload != wantPayload {
				t.Fatalf("uploaded payload = %q, want config-relative file content %q", gotPayload, wantPayload)
			}
		})
	}
}

func TestExplicitCronReplacesConfiguredScheduleInRemoteRunAndPersistedConfig(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	oldTerminal := isTerminalFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldInfo := fetchRunChangeServiceInfoFn
	t.Cleanup(func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		isTerminalFn = oldTerminal
		fetchRemoteArtifactHashesFn = oldHashes
		fetchRunChangeServiceInfoFn = oldInfo
	})

	serviceOverride = "backup"
	remoteCatchOSAndArchFn = func() (string, string, error) { return "linux", "amd64", nil }
	isTerminalFn = func(int) bool { return false }
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "backup.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	loc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name: "backup", Host: "host-a", Payload: "backup.sh", Schedule: "0 3 * * *",
		}}},
	}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatal(err)
	}

	var gotArgs []string
	execRemoteFn = func(_ context.Context, _ string, args []string, stdin io.Reader, _ bool) error {
		if stdin == nil {
			t.Fatal("scheduled file run did not upload its payload")
		}
		gotArgs = append([]string{}, args...)
		return nil
	}
	request := svcCommandRequest{
		Command:      svcCommand{Args: []string{payload, "--cron=30 4 * * *"}},
		Config:       loc,
		HostOverride: "host-a",
		Service:      "backup",
	}
	if err := handleSvcRun(request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotArgs, []string{"run", "--cron=30 4 * * *"}) {
		t.Fatalf("remote args = %#v, want explicit replacement schedule", gotArgs)
	}
	reloaded, err := loadProjectConfigFromDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reloaded.Config.ServiceEntry("backup", "host-a")
	if !ok || entry.Schedule != "30 4 * * *" {
		t.Fatalf("persisted entry = %#v, ok=%v", entry, ok)
	}
}

func TestRunFromProjectConfigRehydratesServiceRoot(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	oldIsTerminal := isTerminalFn
	oldHashes := fetchRemoteArtifactHashesFn
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		isTerminalFn = oldIsTerminal
		fetchRemoteArtifactHashesFn = oldHashes
	}()

	serviceOverride = "rssbot"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "rssbot")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:           "rssbot",
		Host:           "host-a",
		Type:           serviceTypeRun,
		Payload:        "rssbot",
		ServiceRoot:    "tank/apps/rssbot",
		ServiceRootZFS: true,
		Args:           []string{"--pull"},
	})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	if err := runFromProjectConfig(loc, "host-a"); err != nil {
		t.Fatalf("runFromProjectConfig error: %v", err)
	}
	wantArgs := []string{"run", "--service-root=tank/apps/rssbot", "--zfs", "--pull"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestHandleSvcCmdRunForceUsesStoredPayload(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	oldHashes := fetchRemoteArtifactHashesFn
	oldIsTerminal := isTerminalFn
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		fetchRemoteArtifactHashesFn = oldHashes
		isTerminalFn = oldIsTerminal
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hashFileSHA256 error: %v", err)
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
		}, true, nil
	}

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "run.sh",
	})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}

	var calls [][]string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls = append(calls, append([]string{}, args...))
		return nil
	}

	if err := HandleSvcCmd([]string{"run", "--force"}); err != nil {
		t.Fatalf("HandleSvcCmd error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one remote call, got %d", len(calls))
	}
	if len(calls[0]) == 0 || calls[0][0] != "run" {
		t.Fatalf("expected run call, got %v", calls[0])
	}
}
