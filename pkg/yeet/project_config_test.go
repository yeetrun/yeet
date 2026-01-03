// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if entry.Type != serviceTypeRun {
		t.Fatalf("type = %q", entry.Type)
	}
	if entry.Payload != filepath.Join("apps", "compose.yml") {
		t.Fatalf("payload = %q", entry.Payload)
	}
	if len(entry.Args) != len(runArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range runArgs {
		if entry.Args[i] != runArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], runArgs[i])
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
	if entry.Type != serviceTypeRun {
		t.Fatalf("type = %q", entry.Type)
	}
	if len(entry.Args) != len(secondArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range secondArgs {
		if entry.Args[i] != secondArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], secondArgs[i])
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
