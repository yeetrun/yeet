// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
)

func TestHandleSvcCmdRoutesRemoteFallbackCommands(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		execRemoteFn = oldExec
		serviceOverride = oldService
		_ = os.Chdir(cwd)
	}()

	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	serviceOverride = "svc-a"

	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown command", args: []string{"restart", "--force"}},
		{name: "stage with multiple args", args: []string{"stage", "one", "two"}},
		{name: "env passthrough", args: []string{"env", "list"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotService string
			var gotArgs []string
			var gotTTY bool
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				gotService = service
				gotArgs = append([]string{}, args...)
				gotTTY = tty
				if stdin != nil {
					t.Fatalf("expected nil stdin")
				}
				return nil
			}

			if err := HandleSvcCmd(tt.args); err != nil {
				t.Fatalf("HandleSvcCmd returned error: %v", err)
			}
			if gotService != "svc-a" {
				t.Fatalf("service = %q, want svc-a", gotService)
			}
			if !reflect.DeepEqual(gotArgs, tt.args) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.args)
			}
			if !gotTTY {
				t.Fatalf("tty = false, want true")
			}
		})
	}
}

func TestHandleServiceSetRunAsReportsConfigPartialSuccess(t *testing.T) {
	oldExec, oldCreate, oldService := execRemoteFn, createProjectConfigFileFn, serviceOverride
	t.Cleanup(func() { execRemoteFn, createProjectConfigFileFn, serviceOverride = oldExec, oldCreate, oldService })
	serviceOverride = "api"
	tmp := t.TempDir()
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: &ProjectConfig{Version: 1, Services: []ServiceEntry{{Name: "api", Host: "host.example.com"}}}}
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error { return nil }
	createProjectConfigFileFn = func(string) (io.WriteCloser, error) { return nil, errors.New("disk full") }
	err := handleServiceSet(context.Background(), svcCommandRequest{Command: svcCommand{Name: "service", Args: []string{"set", "--run-as=app:app"}, RawArgs: []string{"service", "set", "--run-as=app:app"}}, Config: loc, HostOverride: "host.example.com", Service: "api"})
	want := `service identity changed on host.example.com, but yeet.toml was not updated; set run_as = "app:app" for service "api" and retry sync`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestServiceSetCronReportsConfigPartialSuccess(t *testing.T) {
	oldExec, oldCreate, oldService := execRemoteFn, createProjectConfigFileFn, serviceOverride
	t.Cleanup(func() { execRemoteFn, createProjectConfigFileFn, serviceOverride = oldExec, oldCreate, oldService })
	serviceOverride = "api"
	tmp := t.TempDir()
	loc := &projectConfigLocation{Path: filepath.Join(tmp, "config with spaces", projectConfigName), Dir: tmp, Config: &ProjectConfig{Version: 1, Services: []ServiceEntry{{Name: "api", Host: "host.example.com", Schedule: "0 1 * * *"}}}}
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error { return nil }
	createProjectConfigFileFn = func(string) (io.WriteCloser, error) { return nil, errors.New("disk full") }
	err := handleServiceSet(context.Background(), svcCommandRequest{Command: svcCommand{Name: "service", Args: []string{"set", "--cron=30 2 * * *"}, RawArgs: []string{"service", "set", "--cron=30 2 * * *"}}, Config: loc, HostOverride: "host.example.com", HostOverrideSet: true, Service: "api"})
	want := "remote schedule changed, but failed to update yeet.toml: disk full; recover with `yeet --host host.example.com service sync api --config '" + loc.Path + "'`"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestServiceSetCronRemoteExitMentionsCatchUpgrade(t *testing.T) {
	oldExec, oldService := execRemoteFn, serviceOverride
	t.Cleanup(func() { execRemoteFn, serviceOverride = oldExec, oldService })
	serviceOverride = "api"
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error {
		return remoteExitError{code: 1, output: "Error: unknown flag: --cron\nUsage: yeet service set <svc> [flags]\n"}
	}
	err := handleServiceSet(context.Background(), svcCommandRequest{Command: svcCommand{Name: "service", Args: []string{"set", "--cron=30 2 * * *"}, RawArgs: []string{"service", "set", "--cron=30 2 * * *"}}, Service: "api"})
	if err == nil {
		t.Fatal("handleServiceSet error = nil, want remote exit")
	}
	for _, want := range []string{"remote exit 1", "schedule updates require", "yeet init"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want containing %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "yeet run") {
		t.Fatalf("error = %q, must not recommend yeet run", err)
	}
}

func TestServiceSetCronRemoteExitPreservesAuthoritativeRejection(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantUpgrade bool
	}{
		{
			name:   "scheduled native only",
			output: "Error: service api: --cron only updates an existing scheduled native service; deploy a scheduled native payload with yeet run\n",
		},
		{name: "invalid timer state", output: "Error: service api timer is in unsupported state failed/failed\n"},
		{name: "old catch unknown flag with usage", output: "Error: unknown flag: --cron\nUsage: yeet service set <svc> [flags]\n", wantUpgrade: true},
		{name: "old catch unknown argument with usage", output: "Unknown argument: cron\nUsage: yeet service set <svc>\n", wantUpgrade: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := wrapServiceSetRemoteError(remoteExitError{code: 1, output: tt.output}, cli.ServiceSetFlags{CronSet: true})
			if err == nil {
				t.Fatal("wrapServiceSetRemoteError error = nil, want remote exit")
			}
			gotUpgrade := strings.Contains(err.Error(), "schedule updates require a newer catch")
			if gotUpgrade != tt.wantUpgrade {
				t.Fatalf("wrapServiceSetRemoteError = %q, upgrade=%t, want %t", err, gotUpgrade, tt.wantUpgrade)
			}
			if !tt.wantUpgrade && err.Error() != "remote exit 1" {
				t.Fatalf("authoritative rejection wrapper = %q, want unmodified remote exit", err)
			}
		})
	}
}

func TestHandleSvcCmdLogsRoutesWithoutTTY(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		execRemoteFn = oldExec
		serviceOverride = oldService
		_ = os.Chdir(cwd)
	}()

	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	serviceOverride = "svc-a"

	var gotService string
	var gotArgs []string
	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		gotTTY = tty
		if stdin != nil {
			t.Fatalf("expected nil stdin")
		}
		return nil
	}

	if err := HandleSvcCmd([]string{"logs", "--lines", "50"}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}
	if gotService != "svc-a" {
		t.Fatalf("service = %q, want svc-a", gotService)
	}
	wantArgs := []string{"logs", "--lines", "50"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotTTY {
		t.Fatalf("tty = true, want false")
	}
}

func TestHandleSvcCmdRemoveRoutes(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantRemote  []string
		wantRemoved bool
	}{
		{
			name:        "clean config filters local flag",
			args:        []string{"remove", "--clean-config", "--yes"},
			wantRemote:  []string{"remove", "--yes"},
			wantRemoved: true,
		},
		{
			name:       "yes skips local prompt",
			args:       []string{"remove", "--yes"},
			wantRemote: []string{"remove", "--yes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldExec := execRemoteFn
			oldService := serviceOverride
			oldPrefs := loadedPrefs
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("Getwd error: %v", err)
			}
			defer func() {
				execRemoteFn = oldExec
				serviceOverride = oldService
				loadedPrefs = oldPrefs
				resetHostOverride()
				_ = os.Chdir(cwd)
			}()

			tmp := t.TempDir()
			if err := os.Chdir(tmp); err != nil {
				t.Fatalf("Chdir error: %v", err)
			}
			serviceOverride = "svc-a"
			loadedPrefs.DefaultHost = "host-a"

			cfg := &ProjectConfig{Version: projectConfigVersion}
			cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
			loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
			if err := saveProjectConfig(loc); err != nil {
				t.Fatalf("saveProjectConfig error: %v", err)
			}

			var gotRemote []string
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				if service != "svc-a" {
					t.Fatalf("service = %q, want svc-a", service)
				}
				gotRemote = append([]string{}, args...)
				return nil
			}

			if err := HandleSvcCmd(tt.args); err != nil {
				t.Fatalf("HandleSvcCmd returned error: %v", err)
			}
			if !reflect.DeepEqual(gotRemote, tt.wantRemote) {
				t.Fatalf("remote args = %#v, want %#v", gotRemote, tt.wantRemote)
			}
			loaded, err := loadProjectConfigFromCwd()
			if err != nil {
				t.Fatalf("loadProjectConfigFromCwd error: %v", err)
			}
			_, hasEntry := loaded.Config.ServiceEntry("svc-a", "host-a")
			if hasEntry == tt.wantRemoved {
				t.Fatalf("config entry present = %v, want %v", hasEntry, !tt.wantRemoved)
			}
		})
	}
}
