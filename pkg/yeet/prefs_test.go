// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandlePrefsWarnsWhenPrefsChanged(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "host-a", changed: true})
	defer restore()

	stdout, stderr, err := capturePrefsOutput(t, func() error {
		return HandlePrefs(context.Background(), []string{"prefs"})
	})
	if err != nil {
		t.Fatalf("HandlePrefs error: %v", err)
	}
	if !strings.Contains(stdout, `"defaultHost": "host-a"`) {
		t.Fatalf("stdout = %q, want defaultHost JSON", stdout)
	}
	if !strings.Contains(stderr, "Use --save to save the prefs") {
		t.Fatalf("stderr = %q, want save warning", stderr)
	}
}

func TestHandlePrefsSaveReportsNoChanges(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "host-a"})
	defer restore()

	stdout, stderr, err := capturePrefsOutput(t, func() error {
		return HandlePrefs(context.Background(), []string{"--save"})
	})
	if err != nil {
		t.Fatalf("HandlePrefs error: %v", err)
	}
	if !strings.Contains(stdout, `"defaultHost": "host-a"`) {
		t.Fatalf("stdout = %q, want defaultHost JSON", stdout)
	}
	if !strings.Contains(stderr, "No changes to save") {
		t.Fatalf("stderr = %q, want no changes message", stderr)
	}
}

func TestHandlePrefsSaveWritesPrefsFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "prefs.json")
	restore := stubPrefsState(t, prefs{DefaultHost: "host-b", changed: true})
	defer restore()
	oldPrefsFile := prefsFile
	prefsFile = path
	t.Cleanup(func() { prefsFile = oldPrefsFile })

	stdout, stderr, err := capturePrefsOutput(t, func() error {
		return HandlePrefs(context.Background(), []string{"prefs", "--save"})
	})
	if err != nil {
		t.Fatalf("HandlePrefs error: %v", err)
	}
	if !strings.Contains(stdout, `"defaultHost": "host-b"`) {
		t.Fatalf("stdout = %q, want defaultHost JSON", stdout)
	}
	if !strings.Contains(stderr, "Prefs saved") {
		t.Fatalf("stderr = %q, want saved message", stderr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile prefs error: %v", err)
	}
	if !strings.Contains(string(b), `"defaultHost": "host-b"`) {
		t.Fatalf("prefs file = %q, want saved host", b)
	}
}

func stubPrefsState(t *testing.T, next prefs) func() {
	t.Helper()
	oldPrefs := loadedPrefs
	loadedPrefs = next
	return func() {
		loadedPrefs = oldPrefs
	}
}

func capturePrefsOutput(t *testing.T, fn func() error) (string, string, error) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout Pipe error: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr Pipe error: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		_ = stdoutR.Close()
		_ = stderrR.Close()
	}()

	runErr := fn()
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("stdout close error: %v", err)
	}
	if err := stderrW.Close(); err != nil {
		t.Fatalf("stderr close error: %v", err)
	}
	stdout, readOutErr := io.ReadAll(stdoutR)
	if readOutErr != nil {
		t.Fatalf("stdout ReadAll error: %v", readOutErr)
	}
	stderr, readErrErr := io.ReadAll(stderrR)
	if readErrErr != nil {
		t.Fatalf("stderr ReadAll error: %v", readErrErr)
	}
	return string(stdout), string(stderr), runErr
}
