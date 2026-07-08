// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	cleanup, err := isolateYeetTestEnvironment()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func isolateYeetTestEnvironment() (func(), error) {
	oldHome, hadHome := os.LookupEnv("HOME")
	oldXDGConfigHome, hadXDGConfigHome := os.LookupEnv("XDG_CONFIG_HOME")
	oldCatchHost, hadCatchHost := os.LookupEnv("CATCH_HOST")

	root, err := os.MkdirTemp("", "yeet-test-env-*")
	if err != nil {
		return nil, err
	}
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	if err := os.MkdirAll(home, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.Setenv("HOME", home); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.Setenv("XDG_CONFIG_HOME", xdg); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.Unsetenv("CATCH_HOST"); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}

	return func() {
		restoreEnv("HOME", oldHome, hadHome)
		restoreEnv("XDG_CONFIG_HOME", oldXDGConfigHome, hadXDGConfigHome)
		restoreEnv("CATCH_HOST", oldCatchHost, hadCatchHost)
		_ = os.RemoveAll(root)
	}, nil
}

func restoreEnv(key, value string, ok bool) {
	if ok {
		_ = os.Setenv(key, value)
		return
	}
	_ = os.Unsetenv(key)
}
