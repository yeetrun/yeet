// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yeetrun/yeet/pkg/catchrpc"
)

const (
	projectConfigName    = "yeet.toml"
	projectConfigVersion = 1
)

type ProjectConfig struct {
	Version  int            `toml:"version,omitempty"`
	Hosts    []string       `toml:"hosts,omitempty"`
	Services []ServiceEntry `toml:"services,omitempty"`
}

type ServiceEntry struct {
	Name             string   `toml:"name"`
	Host             string   `toml:"host"`
	Type             string   `toml:"type,omitempty"`
	Payload          string   `toml:"payload,omitempty"`
	PayloadKind      string   `toml:"payload_kind,omitempty"`
	EnvFile          string   `toml:"env_file,omitempty"`
	ServiceRoot      string   `toml:"service_root,omitempty"`
	ServiceRootZFS   bool     `toml:"service_root_zfs,omitempty"`
	Snapshots        string   `toml:"snapshots,omitempty"`
	SnapshotKeepLast int      `toml:"snapshot_keep_last,omitempty"`
	SnapshotMaxAge   string   `toml:"snapshot_max_age,omitempty"`
	SnapshotRequired *bool    `toml:"snapshot_required,omitempty"`
	SnapshotEvents   []string `toml:"snapshot_events,omitempty"`
	Schedule         string   `toml:"schedule,omitempty"`
	Args             []string `toml:"args,omitempty"`
}

type projectConfigLocation struct {
	Path   string
	Dir    string
	Config *ProjectConfig
}

func (e *ServiceEntry) ClearSnapshotOverride() {
	e.Snapshots = ""
	e.SnapshotKeepLast = 0
	e.SnapshotMaxAge = ""
	e.SnapshotRequired = nil
	e.SnapshotEvents = nil
}

func serviceEntryHasSnapshotOverride(e ServiceEntry) bool {
	return e.Snapshots != "" || e.SnapshotKeepLast != 0 || e.SnapshotMaxAge != "" || e.SnapshotRequired != nil || len(e.SnapshotEvents) != 0
}

func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

func cloneStringSlice(values []string) []string {
	return append([]string{}, values...)
}

var createProjectConfigFileFn = func(path string) (io.WriteCloser, error) {
	return os.Create(path)
}

func loadProjectConfigFromCwd() (*projectConfigLocation, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return loadProjectConfigFromDir(cwd)
}

func loadOrCreateProjectConfigFromCwd() (*projectConfigLocation, error) {
	cfg, err := loadProjectConfigFromCwd()
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return &projectConfigLocation{
		Path:   filepath.Join(cwd, projectConfigName),
		Dir:    cwd,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}, nil
}

func loadProjectConfigFromDir(startDir string) (*projectConfigLocation, error) {
	path, err := findProjectConfigPath(startDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cfg ProjectConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	if cfg.Version == 0 {
		cfg.Version = projectConfigVersion
	}
	return &projectConfigLocation{Path: path, Dir: filepath.Dir(path), Config: &cfg}, nil
}

func loadProjectConfigFromFile(path string) (*projectConfigLocation, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("no %s found; run from a project directory or pass --config", projectConfigName)
	}
	expanded, err := expandUserConfigPath(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s found at %s", projectConfigName, abs)
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; pass the path to %s", abs, projectConfigName)
	}
	var cfg ProjectConfig
	if _, err := toml.DecodeFile(abs, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", abs, err)
	}
	if cfg.Version == 0 {
		cfg.Version = projectConfigVersion
	}
	return &projectConfigLocation{Path: abs, Dir: filepath.Dir(abs), Config: &cfg}, nil
}

func expandUserConfigPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func findProjectConfigPath(startDir string) (string, error) {
	dir := filepath.Clean(startDir)
	for {
		path := filepath.Join(dir, projectConfigName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}

func saveProjectConfig(loc *projectConfigLocation) error {
	if loc == nil || loc.Config == nil {
		return nil
	}
	if loc.Config.Version == 0 {
		loc.Config.Version = projectConfigVersion
	}
	sortServiceEntries(loc.Config.Services)
	if err := os.MkdirAll(filepath.Dir(loc.Path), 0o755); err != nil {
		return err
	}
	f, err := createProjectConfigFileFn(loc.Path)
	if err != nil {
		return err
	}
	return encodeProjectConfig(f, loc.Config)
}

func encodeProjectConfig(w io.WriteCloser, cfg *ProjectConfig) (err error) {
	defer func() {
		if closeErr := w.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	encoder := toml.NewEncoder(w)
	return encoder.Encode(cfg)
}

func (c *ProjectConfig) AllHosts() []string {
	if c == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, host := range c.Hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		seen[host] = struct{}{}
	}
	for _, entry := range c.Services {
		host := strings.TrimSpace(entry.Host)
		if host == "" {
			continue
		}
		seen[host] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func (c *ProjectConfig) ServiceHosts(service string) []string {
	if c == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, entry := range c.Services {
		if entry.Name != service {
			continue
		}
		host := strings.TrimSpace(entry.Host)
		if host == "" {
			continue
		}
		seen[host] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func (c *ProjectConfig) ServiceEntry(service, host string) (ServiceEntry, bool) {
	if c == nil {
		return ServiceEntry{}, false
	}
	for _, entry := range c.Services {
		if entry.Name == service && entry.Host == host {
			entry.Args = cloneStringSlice(entry.Args)
			entry.SnapshotRequired = cloneBoolPtr(entry.SnapshotRequired)
			entry.SnapshotEvents = cloneStringSlice(entry.SnapshotEvents)
			return entry, true
		}
	}
	return ServiceEntry{}, false
}

func (c *ProjectConfig) SetServiceEntry(entry ServiceEntry) {
	entry.Args = cloneStringSlice(entry.Args)
	entry.SnapshotRequired = cloneBoolPtr(entry.SnapshotRequired)
	entry.SnapshotEvents = cloneStringSlice(entry.SnapshotEvents)
	for i := range c.Services {
		if c.Services[i].Name == entry.Name && c.Services[i].Host == entry.Host {
			c.Services[i].Type = entry.Type
			c.Services[i].Payload = entry.Payload
			c.Services[i].PayloadKind = entry.PayloadKind
			c.Services[i].Schedule = entry.Schedule
			c.Services[i].Args = cloneStringSlice(entry.Args)
			if entry.EnvFile != "" {
				c.Services[i].EnvFile = entry.EnvFile
			}
			if entry.ServiceRoot != "" {
				c.Services[i].ServiceRoot = entry.ServiceRoot
				c.Services[i].ServiceRootZFS = entry.ServiceRootZFS
			}
			c.Services[i].Snapshots = entry.Snapshots
			c.Services[i].SnapshotKeepLast = entry.SnapshotKeepLast
			c.Services[i].SnapshotMaxAge = entry.SnapshotMaxAge
			c.Services[i].SnapshotRequired = cloneBoolPtr(entry.SnapshotRequired)
			c.Services[i].SnapshotEvents = cloneStringSlice(entry.SnapshotEvents)
			c.addHost(entry.Host)
			sortServiceEntries(c.Services)
			return
		}
	}
	c.Services = append(c.Services, entry)
	c.addHost(entry.Host)
	sortServiceEntries(c.Services)
}

func (c *ProjectConfig) SetServiceRootForEntry(service, host, root string, zfs bool) bool {
	if c == nil {
		return false
	}
	root = strings.TrimSpace(root)
	for i := range c.Services {
		if c.Services[i].Name != service || c.Services[i].Host != host {
			continue
		}
		c.Services[i].ServiceRoot = root
		c.Services[i].ServiceRootZFS = root != "" && zfs
		sortServiceEntries(c.Services)
		return true
	}
	return false
}

func (c *ProjectConfig) SetServiceSnapshotsForEntry(service, host string, policy *catchrpc.SnapshotPolicy) bool {
	entry, ok := c.ServiceEntry(service, host)
	if !ok {
		return false
	}
	entry.ClearSnapshotOverride()
	if policy != nil {
		if policy.Enabled != nil {
			if *policy.Enabled {
				entry.Snapshots = "on"
			} else {
				entry.Snapshots = "off"
			}
		}
		if policy.KeepLast != nil {
			entry.SnapshotKeepLast = *policy.KeepLast
		}
		entry.SnapshotMaxAge = strings.TrimSpace(policy.MaxAge)
		if policy.Required != nil {
			required := *policy.Required
			entry.SnapshotRequired = &required
		}
		entry.SnapshotEvents = append([]string{}, policy.Events...)
	}
	c.SetServiceEntry(entry)
	return true
}

func (c *ProjectConfig) RemoveServiceEntry(service, host string) bool {
	if c == nil {
		return false
	}
	removed := false
	out := c.Services[:0]
	for _, entry := range c.Services {
		if entry.Name == service && entry.Host == host {
			removed = true
			continue
		}
		out = append(out, entry)
	}
	if removed {
		c.Services = out
		sortServiceEntries(c.Services)
	}
	return removed
}

func (c *ProjectConfig) addHost(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	for _, existing := range c.Hosts {
		if existing == host {
			return
		}
	}
	c.Hosts = append(c.Hosts, host)
	sort.Strings(c.Hosts)
}

func sortServiceEntries(entries []ServiceEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Host < entries[j].Host
	})
}

func resolvePayloadPath(configDir, payload string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return payload
	}
	if filepath.IsAbs(payload) {
		return payload
	}
	return filepath.Join(configDir, payload)
}

func resolvePayloadPathForEntry(configDir string, entry ServiceEntry) string {
	if strings.TrimSpace(entry.PayloadKind) == "local-image" {
		return strings.TrimSpace(entry.Payload)
	}
	return resolvePayloadPath(configDir, entry.Payload)
}

func resolveEnvFilePath(configDir, envFile string) string {
	envFile = strings.TrimSpace(envFile)
	if envFile == "" {
		return envFile
	}
	if filepath.IsAbs(envFile) {
		return envFile
	}
	return filepath.Join(configDir, envFile)
}

func relativePayloadPath(configDir, payload string) string {
	return relativePayloadPathForKind(configDir, payload, "")
}

func relativePayloadPathForKind(configDir, payload string, payloadKind string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return payload
	}
	if strings.TrimSpace(payloadKind) == "local-image" {
		return payload
	}
	if looksLikeImageRef(payload) {
		return payload
	}
	abs := payload
	if !filepath.IsAbs(payload) {
		if cwd, err := os.Getwd(); err == nil {
			abs = filepath.Join(cwd, payload)
		} else {
			abs = filepath.Clean(payload)
		}
	}
	rel, err := filepath.Rel(configDir, abs)
	if err != nil {
		return payload
	}
	return filepath.Clean(rel)
}

func relativeEnvFilePath(configDir, envFile string) string {
	envFile = strings.TrimSpace(envFile)
	if envFile == "" {
		return envFile
	}
	abs := envFile
	if !filepath.IsAbs(envFile) {
		if cwd, err := os.Getwd(); err == nil {
			abs = filepath.Join(cwd, envFile)
		} else {
			abs = filepath.Clean(envFile)
		}
	}
	rel, err := filepath.Rel(configDir, abs)
	if err != nil {
		return envFile
	}
	return filepath.Clean(rel)
}
