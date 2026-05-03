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
