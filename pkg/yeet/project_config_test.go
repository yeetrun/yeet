// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type closeErrorBuffer struct {
	bytes.Buffer
	err error
}

func (w *closeErrorBuffer) Close() error {
	return w.err
}

func TestSaveProjectConfigReturnsCloseError(t *testing.T) {
	oldCreate := createProjectConfigFileFn
	defer func() {
		createProjectConfigFileFn = oldCreate
	}()

	closeErr := errors.New("close failed")
	createProjectConfigFileFn = func(string) (io.WriteCloser, error) {
		return &closeErrorBuffer{err: closeErr}, nil
	}

	tmp := t.TempDir()
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveProjectConfig(loc); !errors.Is(err, closeErr) {
		t.Fatalf("saveProjectConfig error = %v, want %v", err, closeErr)
	}
}

func TestRemoveServiceConfig(t *testing.T) {
	oldService := serviceOverride
	defer func() {
		serviceOverride = oldService
	}()

	tests := []struct {
		name        string
		service     string
		host        string
		wantSaved   bool
		wantRemoved bool
	}{
		{
			name:        "removes matching service and host",
			service:     "svc-a",
			host:        "host-a",
			wantSaved:   true,
			wantRemoved: true,
		},
		{
			name:        "skips missing host",
			service:     "svc-a",
			host:        "missing",
			wantSaved:   false,
			wantRemoved: false,
		},
		{
			name:        "skips empty service override",
			service:     "",
			host:        "host-a",
			wantSaved:   false,
			wantRemoved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceOverride = tt.service
			tmp := t.TempDir()
			cfg := &ProjectConfig{Version: projectConfigVersion}
			cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Payload: "run-a"})
			cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-b", Payload: "run-b"})
			cfg.SetServiceEntry(ServiceEntry{Name: "svc-b", Host: "host-a", Payload: "run-c"})
			loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}

			if err := removeServiceConfig(loc, tt.host); err != nil {
				t.Fatalf("removeServiceConfig error: %v", err)
			}
			_, hasRemovedTarget := loc.Config.ServiceEntry("svc-a", "host-a")
			if hasRemovedTarget == tt.wantRemoved {
				t.Fatalf("removed target present = %v, want %v", hasRemovedTarget, !tt.wantRemoved)
			}
			if _, ok := loc.Config.ServiceEntry("svc-a", "host-b"); !ok {
				t.Fatalf("expected svc-a@host-b to remain")
			}
			if _, ok := loc.Config.ServiceEntry("svc-b", "host-a"); !ok {
				t.Fatalf("expected svc-b@host-a to remain")
			}

			_, statErr := os.Stat(loc.Path)
			if tt.wantSaved && statErr != nil {
				t.Fatalf("expected config file to be saved: %v", statErr)
			}
			if !tt.wantSaved && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("expected no saved config file, stat error = %v", statErr)
			}
		})
	}
}

func TestSaveRunConfigCreatesToml(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	runArgs := []string{"--net=svc", "--", "--flag", "value"}
	wantArgs := []string{"--net=svc", "--flag", "value"}
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveRunConfig(loc, "host-a", payload, runArgs); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	if loaded == nil || loaded.Config == nil {
		t.Fatalf("expected config to be saved")
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if entry.Type != "" {
		t.Fatalf("type = %q", entry.Type)
	}
	if entry.Payload != filepath.Join("apps", "compose.yml") {
		t.Fatalf("payload = %q", entry.Payload)
	}
	if len(entry.Args) != len(wantArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range wantArgs {
		if entry.Args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], wantArgs[i])
		}
	}
}

func TestSaveRunConfigOverwritesArgs(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "bin")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}

	firstArgs := []string{"--", "--flagA", "valueA", "--bool-flag", "posArg"}
	if err := saveRunConfig(loc, "host-a", payload, firstArgs); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	secondArgs := []string{"--", "--flagB", "valueB", "--bool-flag2", "posArg2"}
	wantArgs := []string{"--flagB", "valueB", "--bool-flag2", "posArg2"}
	if err := saveRunConfig(loc, "host-a", payload, secondArgs); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if entry.Type != "" {
		t.Fatalf("type = %q", entry.Type)
	}
	if len(entry.Args) != len(wantArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range wantArgs {
		if entry.Args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], wantArgs[i])
		}
	}
}

func TestSaveCronConfigCreatesToml(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	serviceOverride = "svc-cron"
	payload := filepath.Join(tmp, "apps", "owesplit")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	cronFields := []string{"0", "9", "15", "*", "*"}
	binArgs := []string{"-live"}
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveCronConfig(loc, "host-a", payload, cronFields, binArgs); err != nil {
		t.Fatalf("saveCronConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-cron", "host-a")
	if !ok {
		t.Fatalf("expected cron config to be saved")
	}
	if entry.Type != serviceTypeCron {
		t.Fatalf("type = %q", entry.Type)
	}
	if entry.Payload != filepath.Join("apps", "owesplit") {
		t.Fatalf("payload = %q", entry.Payload)
	}
	if entry.Schedule != "0 9 15 * *" {
		t.Fatalf("schedule = %q", entry.Schedule)
	}
	if len(entry.Args) != len(binArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range binArgs {
		if entry.Args[i] != binArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], binArgs[i])
		}
	}
}

func TestProjectConfigAllHostsDedupesTrimsAndSorts(t *testing.T) {
	cfg := &ProjectConfig{
		Hosts: []string{" host-b ", "", "host-a", "host-b"},
		Services: []ServiceEntry{
			{Name: "svc-a", Host: "host-c"},
			{Name: "svc-b", Host: " host-a "},
			{Name: "svc-c", Host: " "},
		},
	}

	got := cfg.AllHosts()
	if gotString := strings.Join(got, ","); gotString != "host-a,host-b,host-c" {
		t.Fatalf("AllHosts = %#v", got)
	}
}

func TestProjectConfigAllHostsNilConfig(t *testing.T) {
	var cfg *ProjectConfig
	if got := cfg.AllHosts(); got != nil {
		t.Fatalf("AllHosts = %#v, want nil", got)
	}
}
