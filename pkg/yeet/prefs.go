// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/shayne/yargs"
)

var (
	clientConfigPathFn = defaultClientConfigPath
	legacyPrefsPathFn  = defaultLegacyPrefsPath

	serviceOverride string
	hostOverride    string
	hostOverrideSet bool
	loadedPrefs     clientConfig
)

var (
	_ = setDefaultHost
	_ = setWorkspaces
)

const (
	defaultCatchHost = "catch"
	defaultRPCPort   = 41548
)

type clientConfig struct {
	loaded      bool     `toml:"-"`
	loadErr     error    `toml:"-"`
	changed     bool     `toml:"-"`
	savedHost   string   `toml:"-"`
	warnings    []string `toml:"-"`
	DefaultHost string   `toml:"default_host,omitempty"`
	Workspaces  []string `toml:"workspaces,omitempty"`
}

type legacyPrefsJSON struct {
	DefaultHost string `json:"defaultHost"`
}

type prefs = clientConfig

func init() {
	loadedPrefs.DefaultHost = defaultCatchHost
	loadedPrefs.savedHost = defaultCatchHost
}

func defaultClientConfigPath() (string, error) {
	if base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); base != "" {
		return filepath.Join(base, "yeet", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "yeet", "config.toml"), nil
}

func defaultLegacyPrefsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.Getenv("HOME"), ".yeet", "prefs.json")
	}
	return filepath.Join(home, ".yeet", "prefs.json")
}

func clientConfigPath() (string, error) {
	return clientConfigPathFn()
}

func legacyPrefsPath() string {
	return legacyPrefsPathFn()
}

func normalizeCatchHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func hasExplicitClientConfigState(c clientConfig) bool {
	if c.savedHost != "" && c.savedHost != defaultCatchHost {
		return true
	}
	if c.DefaultHost != "" && c.DefaultHost != defaultCatchHost {
		return true
	}
	return len(c.Workspaces) != 0 || c.changed || c.loadErr != nil || len(c.warnings) != 0
}

func ensureClientConfigLoaded() error {
	if loadedPrefs.loaded {
		return loadedPrefs.loadErr
	}
	if hasExplicitClientConfigState(loadedPrefs) {
		loadedPrefs.loaded = true
		loadedPrefs.normalize()
		if loadedPrefs.savedHost == "" {
			loadedPrefs.savedHost = loadedPrefs.DefaultHost
		}
		if envHost := normalizeCatchHost(os.Getenv("CATCH_HOST")); envHost != "" {
			hostOverride = envHost
			hostOverrideSet = true
		}
		return nil
	}
	loadedPrefs.loaded = true
	if err := loadedPrefs.load(); err != nil {
		loadedPrefs.loadErr = err
	}
	if envHost := normalizeCatchHost(os.Getenv("CATCH_HOST")); envHost != "" {
		hostOverride = envHost
		hostOverrideSet = true
	}
	if loadedPrefs.savedHost == "" {
		loadedPrefs.savedHost = loadedPrefs.DefaultHost
	}
	if loadedPrefs.DefaultHost == "" {
		loadedPrefs.DefaultHost = defaultCatchHost
	}
	return loadedPrefs.loadErr
}

func requireClientConfig() error {
	if err := ensureClientConfigLoaded(); err != nil {
		return err
	}
	return nil
}

func (c *clientConfig) load() error {
	path, err := clientConfigPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		if _, err := toml.Decode(string(raw), c); err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}
		c.normalize()
		c.savedHost = c.DefaultHost
		c.cleanupLegacyPrefs()
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := c.migrateLegacyPrefs(path); err != nil {
		return err
	}
	c.normalize()
	c.savedHost = c.DefaultHost
	return nil
}

func (c *clientConfig) migrateLegacyPrefs(path string) error {
	legacyPath := legacyPrefsPath()
	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var old legacyPrefsJSON
	if err := json.Unmarshal(raw, &old); err != nil {
		return fmt.Errorf("failed to parse %s: %w", legacyPath, err)
	}
	c.DefaultHost = normalizeCatchHost(old.DefaultHost)
	if err := c.saveTo(path); err != nil {
		return err
	}
	c.cleanupLegacyPrefs()
	return nil
}

func (c *clientConfig) cleanupLegacyPrefs() {
	legacyPath := legacyPrefsPath()
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.warnings = append(c.warnings, fmt.Sprintf("Warning: could not remove old prefs file %s: %v", legacyPath, err))
		return
	}
	if err := os.Remove(filepath.Dir(legacyPath)); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, fs.ErrInvalid) {
		if !strings.Contains(err.Error(), "directory not empty") {
			c.warnings = append(c.warnings, fmt.Sprintf("Warning: could not remove old prefs directory %s: %v", filepath.Dir(legacyPath), err))
		}
	}
}

func saveClientConfig() error {
	if err := ensureClientConfigLoaded(); err != nil {
		return err
	}
	path, err := clientConfigPath()
	if err != nil {
		return err
	}
	return loadedPrefs.saveTo(path)
}

func (c *clientConfig) saveTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	payload := clientConfigForTOML(*c)
	payload.normalize()
	if err := toml.NewEncoder(&buf).Encode(payload); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func clientConfigForTOML(c clientConfig) clientConfig {
	host := c.savedHost
	if host == "" {
		host = c.DefaultHost
	}
	return clientConfig{DefaultHost: host, Workspaces: append([]string{}, c.Workspaces...)}
}

func (c *clientConfig) normalize() {
	c.DefaultHost = normalizeCatchHost(c.DefaultHost)
	if c.DefaultHost == "" {
		c.DefaultHost = defaultCatchHost
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(c.Workspaces))
	for _, path := range c.Workspaces {
		normalized, err := normalizeWorkspacePath(path)
		if err != nil || normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	c.Workspaces = out
}

func normalizeWorkspacePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	expanded, err := expandUserConfigPath(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func workspacePaths() []string {
	_ = ensureClientConfigLoaded()
	return append([]string{}, loadedPrefs.Workspaces...)
}

func setDefaultHost(host string) {
	host = normalizeCatchHost(host)
	if host == "" {
		return
	}
	_ = ensureClientConfigLoaded()
	loadedPrefs.DefaultHost = host
	loadedPrefs.savedHost = host
	loadedPrefs.changed = true
}

func setWorkspaces(paths []string) error {
	_ = ensureClientConfigLoaded()
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		p, err := normalizeWorkspacePath(path)
		if err != nil {
			return err
		}
		if p != "" {
			normalized = append(normalized, p)
		}
	}
	loadedPrefs.Workspaces = normalized
	loadedPrefs.normalize()
	return nil
}

func SetHost(host string) {
	host = normalizeCatchHost(host)
	if host == "" {
		return
	}
	_ = ensureClientConfigLoaded()
	hostOverride = host
	hostOverrideSet = true
}

func SetHostOverride(host string) {
	SetHost(host)
}

func HostOverride() (string, bool) {
	_ = ensureClientConfigLoaded()
	if !hostOverrideSet {
		return "", false
	}
	return hostOverride, true
}

func resetHostOverride() {
	currentHost := loadedPrefs.DefaultHost
	currentOverride := hostOverride
	hadOverride := hostOverrideSet || currentOverride != ""
	hostOverride = ""
	hostOverrideSet = false
	if !hadOverride {
		if currentHost != "" {
			loadedPrefs.DefaultHost = currentHost
			return
		}
		loadedPrefs.DefaultHost = defaultCatchHost
		return
	}
	if loadedPrefs.savedHost != "" {
		loadedPrefs.DefaultHost = loadedPrefs.savedHost
		return
	}
	if currentHost != "" && currentHost != normalizeCatchHost(currentOverride) {
		loadedPrefs.DefaultHost = currentHost
		return
	}
	loadedPrefs.DefaultHost = defaultCatchHost
}

func Host() string {
	_ = ensureClientConfigLoaded()
	if hostOverrideSet {
		return hostOverride
	}
	if loadedPrefs.DefaultHost == "" {
		return defaultCatchHost
	}
	return loadedPrefs.DefaultHost
}

func SetServiceOverride(service string) {
	svc, host, ok := splitServiceHost(service)
	if ok && host != "" {
		SetHostOverride(normalizeCatchHost(host))
	}
	serviceOverride = svc
}

type configFlagsParsed struct {
	Host            string `flag:"host"`
	Workspace       string `flag:"workspace"`
	AddWorkspace    string `flag:"add-workspace"`
	RemoveWorkspace string `flag:"remove-workspace"`
	ClearWorkspaces bool   `flag:"clear-workspaces"`
}

func HandleConfig(_ context.Context, args []string) error {
	if len(args) > 0 && args[0] == "config" {
		args = args[1:]
	}
	if err := requireClientConfig(); err != nil {
		return err
	}
	result, err := yargs.ParseFlags[configFlagsParsed](args)
	if err != nil {
		return err
	}
	if len(result.Args) > 0 || len(result.RemainingArgs) > 0 {
		return fmt.Errorf("config does not take positional arguments")
	}
	changed, err := applyConfigFlags(result.Flags)
	if err != nil {
		return err
	}
	if changed {
		if err := saveClientConfig(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	}
	fmt.Print(clientConfigTOMLString(loadedPrefs))
	return nil
}

func applyConfigFlags(flags configFlagsParsed) (bool, error) {
	changed := false
	if host := normalizeCatchHost(flags.Host); host != "" {
		loadedPrefs.DefaultHost = host
		changed = true
	}
	if strings.TrimSpace(flags.Workspace) != "" {
		workspace, err := existingWorkspaceDir(flags.Workspace)
		if err != nil {
			return false, err
		}
		loadedPrefs.Workspaces = []string{workspace}
		changed = true
	}
	if strings.TrimSpace(flags.AddWorkspace) != "" {
		workspace, err := existingWorkspaceDir(flags.AddWorkspace)
		if err != nil {
			return false, err
		}
		loadedPrefs.Workspaces = append(loadedPrefs.Workspaces, workspace)
		changed = true
	}
	if strings.TrimSpace(flags.RemoveWorkspace) != "" {
		workspace, err := normalizeWorkspacePath(flags.RemoveWorkspace)
		if err != nil {
			return false, err
		}
		loadedPrefs.Workspaces = removeWorkspacePath(loadedPrefs.Workspaces, workspace)
		changed = true
	}
	if flags.ClearWorkspaces {
		loadedPrefs.Workspaces = nil
		changed = true
	}
	if changed {
		loadedPrefs.normalize()
		loadedPrefs.savedHost = loadedPrefs.DefaultHost
	}
	return changed, nil
}

func existingWorkspaceDir(path string) (string, error) {
	workspace, err := normalizeWorkspacePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workspace must be an existing directory: %s", workspace)
	}
	return workspace, nil
}

func removeWorkspacePath(paths []string, remove string) []string {
	out := paths[:0]
	for _, path := range paths {
		if path != remove {
			out = append(out, path)
		}
	}
	return out
}

func clientConfigTOMLString(c clientConfig) string {
	var buf bytes.Buffer
	_ = toml.NewEncoder(&buf).Encode(clientConfigForTOML(c))
	return buf.String()
}
