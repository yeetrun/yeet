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
	"strings"
	"testing"
)

func TestPrefsHostSettersAndOverrides(t *testing.T) {
	oldPrefs := loadedPrefs
	oldService := serviceOverride
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	defer func() {
		loadedPrefs = oldPrefs
		serviceOverride = oldService
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
	}()
	loadedPrefs = prefs{DefaultHost: "host-a"}
	serviceOverride = ""
	resetHostOverride()

	SetHost("")
	if Host() != "host-a" || loadedPrefs.changed {
		t.Fatalf("empty SetHost changed prefs: %#v", loadedPrefs)
	}
	SetHost("host-a")
	if loadedPrefs.changed {
		t.Fatalf("same SetHost marked changed: %#v", loadedPrefs)
	}
	SetHost("host-b")
	if Host() != "host-b" || !loadedPrefs.changed {
		t.Fatalf("SetHost host-b prefs = %#v, want changed host-b", loadedPrefs)
	}
	if got, ok := HostOverride(); ok || got != "" {
		t.Fatalf("HostOverride before set = %q %v, want empty false", got, ok)
	}

	SetHostOverride("host-c")
	if got, ok := HostOverride(); !ok || got != "host-c" {
		t.Fatalf("HostOverride = %q %v, want host-c true", got, ok)
	}
	SetServiceOverride("svc-a@host-d")
	if serviceOverride != "svc-a" {
		t.Fatalf("serviceOverride = %q, want svc-a", serviceOverride)
	}
	if got, ok := HostOverride(); !ok || got != "host-d" {
		t.Fatalf("HostOverride after service = %q %v, want host-d true", got, ok)
	}
}

func TestPrefsLoadReadsHomePrefs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := os.Mkdir(filepath.Join(tmp, ".yeet"), 0o755); err != nil {
		t.Fatalf("Mkdir .yeet: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".yeet", "prefs.json"), []byte(`{"defaultHost":"host-a"}`), 0o600); err != nil {
		t.Fatalf("WriteFile prefs: %v", err)
	}

	var p prefs
	if err := p.load(); err != nil {
		t.Fatalf("prefs.load error: %v", err)
	}
	if p.DefaultHost != "host-a" {
		t.Fatalf("DefaultHost = %q, want host-a", p.DefaultHost)
	}
}

func TestPrefsSaveReturnsMkdirError(t *testing.T) {
	tmp := t.TempDir()
	parentFile := filepath.Join(tmp, "not-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile parent: %v", err)
	}
	oldPrefsFile := prefsFile
	prefsFile = filepath.Join(parentFile, "prefs.json")
	t.Cleanup(func() { prefsFile = oldPrefsFile })

	err := (&prefs{DefaultHost: "host-a"}).save()
	if err == nil {
		t.Fatal("prefs.save error = nil, want mkdir error")
	}
}

func TestAsJSONFallsBackForUnsupportedValues(t *testing.T) {
	if got := asJSON(complex(1, 2)); got != "(1+2i)" {
		t.Fatalf("asJSON complex = %q, want fmt fallback", got)
	}
}

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

func TestHandlePrefsSaveReportsSaveError(t *testing.T) {
	tmp := t.TempDir()
	parentFile := filepath.Join(tmp, "not-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile parent: %v", err)
	}
	restore := stubPrefsState(t, prefs{DefaultHost: "host-b", changed: true})
	defer restore()
	oldPrefsFile := prefsFile
	prefsFile = filepath.Join(parentFile, "prefs.json")
	t.Cleanup(func() { prefsFile = oldPrefsFile })

	_, _, err := capturePrefsOutput(t, func() error {
		return HandlePrefs(context.Background(), []string{"--save"})
	})
	if err == nil || !strings.Contains(err.Error(), "failed to save preferences") {
		t.Fatalf("HandlePrefs error = %v, want save error", err)
	}
	if !errors.Is(err, os.ErrExist) && !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("HandlePrefs error = %v, want filesystem cause", err)
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
