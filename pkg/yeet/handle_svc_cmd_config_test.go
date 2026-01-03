// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleSvcCmdUsesConfigHost(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	defer func() {
		execRemoteFn = oldExec
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
	loadedPrefs.Host = "catch"

	called := false
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		called = true
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		return nil
	}

	if err := HandleSvcCmd([]string{"status"}); err != nil {
		t.Fatalf("HandleSvcCmd error: %v", err)
	}
	if !called {
		t.Fatalf("expected execRemoteFn to be called")
	}
	if loadedPrefs.Host != "host-a" {
		t.Fatalf("host = %q, want host-a", loadedPrefs.Host)
	}
}
