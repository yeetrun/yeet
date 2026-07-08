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
	"strings"
	"testing"
)

func TestPrefsHostSettersAndOverrides(t *testing.T) {
	t.Setenv("CATCH_HOST", "")
	restore := stubClientConfigState(t, clientConfig{loaded: true, DefaultHost: "host-a"})
	defer restore()

	SetHost("")
	if Host() != "host-a" {
		t.Fatalf("empty SetHost changed host: %q", Host())
	}

	SetHost("HOST-B")
	if Host() != "host-b" {
		t.Fatalf("Host after SetHost = %q, want host-b", Host())
	}
	if got, ok := HostOverride(); !ok || got != "host-b" {
		t.Fatalf("HostOverride after SetHost = %q %v, want host-b true", got, ok)
	}
	if loadedPrefs.DefaultHost != "host-a" {
		t.Fatalf("saved default host = %q, want host-a", loadedPrefs.DefaultHost)
	}

	resetHostOverride()
	if got, ok := HostOverride(); ok || got != "" {
		t.Fatalf("HostOverride after reset = %q %v, want empty false", got, ok)
	}
	if Host() != "host-a" {
		t.Fatalf("Host after reset = %q, want host-a", Host())
	}

	SetHostOverride("HOST-C")
	if got, ok := HostOverride(); !ok || got != "host-c" {
		t.Fatalf("HostOverride = %q %v, want host-c true", got, ok)
	}
	if loadedPrefs.DefaultHost != "host-a" {
		t.Fatalf("saved default host after SetHostOverride = %q, want host-a", loadedPrefs.DefaultHost)
	}

	SetServiceOverride("svc-a@HOST-D")
	if serviceOverride != "svc-a" {
		t.Fatalf("serviceOverride = %q, want svc-a", serviceOverride)
	}
	if got, ok := HostOverride(); !ok || got != "host-d" {
		t.Fatalf("HostOverride after service = %q %v, want host-d true", got, ok)
	}
}

func TestClientConfigPathUsesXDGConfigHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	path, err := clientConfigPath()
	if err != nil {
		t.Fatalf("clientConfigPath error: %v", err)
	}
	want := filepath.Join(tmp, "yeet", "config.toml")
	if path != want {
		t.Fatalf("clientConfigPath = %q, want %q", path, want)
	}
}

func TestClientConfigPathFallsBackToDotConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	path, err := clientConfigPath()
	if err != nil {
		t.Fatalf("clientConfigPath error: %v", err)
	}
	want := filepath.Join(home, ".config", "yeet", "config.toml")
	if path != want {
		t.Fatalf("clientConfigPath = %q, want %q", path, want)
	}
}

func TestClientConfigSaveLoadToml(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CATCH_HOST", "")
	restore := stubClientConfigState(t, clientConfig{
		loaded:      true,
		DefaultHost: "Yeet-Lab",
		Workspaces: []string{
			filepath.Join(tmp, "workspace"),
		},
	})
	defer restore()
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })

	if err := saveClientConfig(); err != nil {
		t.Fatalf("saveClientConfig error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `default_host = "yeet-lab"`) {
		t.Fatalf("config = %q, want lowercase default_host", text)
	}
	if !strings.Contains(text, `workspaces = ["`+filepath.ToSlash(filepath.Join(tmp, "workspace"))+`"]`) &&
		!strings.Contains(text, `workspaces = ["`+filepath.Join(tmp, "workspace")+`"]`) {
		t.Fatalf("config = %q, want workspace path", text)
	}

	loadedPrefs = clientConfig{}
	if err := ensureClientConfigLoaded(); err != nil {
		t.Fatalf("ensureClientConfigLoaded error: %v", err)
	}
	if Host() != "yeet-lab" {
		t.Fatalf("Host = %q, want yeet-lab", Host())
	}
	if got := workspacePaths(); !reflect.DeepEqual(got, []string{filepath.Join(tmp, "workspace")}) {
		t.Fatalf("workspacePaths = %#v, want workspace", got)
	}
}

func TestClientConfigMigratesLegacyPrefsAndDeletesOldFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CATCH_HOST", "")
	home := filepath.Join(tmp, "home")
	legacyDir := filepath.Join(home, ".yeet")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll legacy dir: %v", err)
	}
	legacyPath := filepath.Join(legacyDir, "prefs.json")
	if err := os.WriteFile(legacyPath, []byte(`{"defaultHost":"Yeet-Lab"}`), 0o600); err != nil {
		t.Fatalf("WriteFile legacy prefs: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	restore := stubClientConfigState(t, clientConfig{})
	defer restore()

	if err := ensureClientConfigLoaded(); err != nil {
		t.Fatalf("ensureClientConfigLoaded error: %v", err)
	}
	if Host() != "yeet-lab" {
		t.Fatalf("Host = %q, want migrated lowercase host", Host())
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy prefs stat err = %v, want removed", err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy dir stat err = %v, want removed when empty", err)
	}
	configPath := filepath.Join(tmp, "xdg", "yeet", "config.toml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile migrated config: %v", err)
	}
	if !strings.Contains(string(raw), `default_host = "yeet-lab"`) {
		t.Fatalf("migrated config = %q, want default_host", string(raw))
	}
}

func TestClientConfigExistingTomlWinsAndCleansLegacy(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CATCH_HOST", "")
	home := filepath.Join(tmp, "home")
	xdg := filepath.Join(tmp, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "yeet"), 0o755); err != nil {
		t.Fatalf("MkdirAll xdg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "yeet", "config.toml"), []byte(`default_host = "new-host"`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile xdg config: %v", err)
	}
	legacyDir := filepath.Join(home, ".yeet")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll legacy dir: %v", err)
	}
	legacyPath := filepath.Join(legacyDir, "prefs.json")
	if err := os.WriteFile(legacyPath, []byte(`{"defaultHost":"old-host"}`), 0o600); err != nil {
		t.Fatalf("WriteFile legacy prefs: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	restore := stubClientConfigState(t, clientConfig{})
	defer restore()

	if err := ensureClientConfigLoaded(); err != nil {
		t.Fatalf("ensureClientConfigLoaded error: %v", err)
	}
	if Host() != "new-host" {
		t.Fatalf("Host = %q, want existing TOML host", Host())
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy prefs stat err = %v, want best-effort cleanup", err)
	}
}

func TestClientConfigParseErrorIsRequiredOnDemand(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CATCH_HOST", "")
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"), []byte("default_host = ["), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	restore := stubClientConfigState(t, clientConfig{})
	defer restore()

	if got := Host(); got != defaultCatchHost {
		t.Fatalf("Host before requiring config = %q, want default catch host", got)
	}
	err := requireClientConfig()
	if err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Fatalf("requireClientConfig error = %v, want parse error", err)
	}
}

func TestRuntimeHostOverridesDoNotSaveDefaultHost(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CATCH_HOST", "")
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	restore := stubClientConfigState(t, clientConfig{loaded: true, DefaultHost: "yeet-lab"})
	defer restore()

	SetHostOverride("Yeet-Cloud")
	if got, ok := HostOverride(); !ok || got != "yeet-cloud" {
		t.Fatalf("HostOverride = %q %v, want yeet-cloud true", got, ok)
	}
	if Host() != "yeet-cloud" {
		t.Fatalf("Host = %q, want runtime override host", Host())
	}
	if err := saveClientConfig(); err != nil {
		t.Fatalf("saveClientConfig error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if strings.Contains(string(raw), "yeet-cloud") {
		t.Fatalf("saved config = %q, runtime override should not be persisted", string(raw))
	}
	if !strings.Contains(string(raw), `default_host = "yeet-lab"`) {
		t.Fatalf("saved config = %q, want persisted saved default host", string(raw))
	}
}

func TestClientConfigCatchHostEnvOverrideDoesNotSaveDefaultHost(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CATCH_HOST", "Yeet-Cloud")
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	restore := stubClientConfigState(t, clientConfig{})
	defer restore()
	loadedPrefs.DefaultHost = "yeet-lab"

	if err := ensureClientConfigLoaded(); err != nil {
		t.Fatalf("ensureClientConfigLoaded error: %v", err)
	}
	if got, ok := HostOverride(); !ok || got != "yeet-cloud" {
		t.Fatalf("HostOverride from env = %q %v, want yeet-cloud true", got, ok)
	}
	if Host() != "yeet-cloud" {
		t.Fatalf("Host = %q, want env override host", Host())
	}
	if loadedPrefs.DefaultHost != "yeet-lab" {
		t.Fatalf("saved default host after env override = %q, want yeet-lab", loadedPrefs.DefaultHost)
	}
	if err := saveClientConfig(); err != nil {
		t.Fatalf("saveClientConfig error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if strings.Contains(string(raw), "yeet-cloud") {
		t.Fatalf("saved config = %q, env override should not be persisted", string(raw))
	}
	if !strings.Contains(string(raw), `default_host = "yeet-lab"`) {
		t.Fatalf("saved config = %q, want persisted saved default host", string(raw))
	}
}

func TestHostOverrideLoadsCatchHostEnvFirst(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CATCH_HOST", "Yeet-Cloud")
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"), []byte(`default_host = "yeet-lab"`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	restore := stubClientConfigState(t, clientConfig{})
	defer restore()

	if got, ok := HostOverride(); !ok || got != "yeet-cloud" {
		t.Fatalf("HostOverride before Host = %q %v, want yeet-cloud true", got, ok)
	}
	if loadedPrefs.DefaultHost != "yeet-lab" {
		t.Fatalf("saved default host after HostOverride = %q, want yeet-lab", loadedPrefs.DefaultHost)
	}
}

func TestHandleConfigPrintsToml(t *testing.T) {
	tmp := t.TempDir()
	restore := stubClientConfigState(t, clientConfig{
		DefaultHost: "yeet-lab",
		Workspaces:  []string{tmp},
	})
	defer restore()

	stdout, stderr, err := capturePrefsOutput(t, func() error {
		return HandleConfig(context.Background(), []string{"config"})
	})
	if err != nil {
		t.Fatalf("HandleConfig error: %v", err)
	}
	if !strings.Contains(stdout, `default_host = "yeet-lab"`) || !strings.Contains(stdout, `workspaces = ["`+tmp+`"]`) {
		t.Fatalf("stdout = %q, want TOML config", stdout)
	}
	if strings.Contains(stdout, "config path") {
		t.Fatalf("stdout = %q, path should not pollute parseable TOML", stdout)
	}
	_ = stderr
}

func TestHandleConfigMutationsSaveImmediately(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir workspace: %v", err)
	}
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "catch"})
	defer restore()

	_, _, err := capturePrefsOutput(t, func() error {
		return HandleConfig(context.Background(), []string{"--host", "Yeet-Lab", "--workspace", workspace})
	})
	if err != nil {
		t.Fatalf("HandleConfig mutation error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `default_host = "yeet-lab"`) || !strings.Contains(text, workspace) {
		t.Fatalf("saved config = %q, want host and workspace", text)
	}
}

func TestHandleConfigWorkspaceRequiresExistingDirectory(t *testing.T) {
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "catch"})
	defer restore()
	_, _, err := capturePrefsOutput(t, func() error {
		return HandleConfig(context.Background(), []string{"--workspace", filepath.Join(t.TempDir(), "missing")})
	})
	if err == nil || !strings.Contains(err.Error(), "workspace must be an existing directory") {
		t.Fatalf("HandleConfig error = %v, want existing directory error", err)
	}
}

func TestHandleConfigRemoveWorkspaceDoesNotRequireExistingDirectory(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "missing")
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "catch", Workspaces: []string{missing}})
	defer restore()
	if _, _, err := capturePrefsOutput(t, func() error {
		return HandleConfig(context.Background(), []string{"--remove-workspace", missing})
	}); err != nil {
		t.Fatalf("HandleConfig remove missing workspace error: %v", err)
	}
	if got := workspacePaths(); len(got) != 0 {
		t.Fatalf("workspacePaths = %#v, want empty", got)
	}
}

func capturePrefsOutput(t *testing.T, fn func() error) (string, string, error) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	runErr := fn()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	if err := stderrW.Close(); err != nil {
		t.Fatalf("stderr close: %v", err)
	}
	stdout, err := io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("stdout read: %v", err)
	}
	if err := stdoutR.Close(); err != nil {
		t.Fatalf("stdout reader close: %v", err)
	}
	stderr, err := io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("stderr read: %v", err)
	}
	if err := stderrR.Close(); err != nil {
		t.Fatalf("stderr reader close: %v", err)
	}
	return string(stdout), string(stderr), runErr
}

func stubClientConfigState(t *testing.T, next clientConfig) func() {
	t.Helper()
	oldPrefs := loadedPrefs
	oldService := serviceOverride
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	if next.loaded || next.loadErr != nil || next.DefaultHost != "" || len(next.Workspaces) != 0 || len(next.warnings) != 0 || next.changed || next.savedHost != "" {
		next.loaded = true
		next.normalize()
		if next.savedHost == "" {
			next.savedHost = next.DefaultHost
		}
	}
	loadedPrefs = next
	serviceOverride = ""
	hostOverride = ""
	hostOverrideSet = false
	return func() {
		loadedPrefs = oldPrefs
		serviceOverride = oldService
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
	}
}

func stubPrefsState(t *testing.T, next prefs) func() {
	t.Helper()
	oldPrefs := loadedPrefs
	oldService := serviceOverride
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	next.loaded = true
	next.normalize()
	if next.savedHost == "" {
		next.savedHost = next.DefaultHost
	}
	loadedPrefs = next
	serviceOverride = ""
	hostOverride = ""
	hostOverrideSet = false
	return func() {
		loadedPrefs = oldPrefs
		serviceOverride = oldService
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
	}
}
