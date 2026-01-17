// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
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
	Name     string   `toml:"name"`
	Host     string   `toml:"host"`
	Type     string   `toml:"type,omitempty"`
	Payload  string   `toml:"payload,omitempty"`
	EnvFile  string   `toml:"env_file,omitempty"`
	Schedule string   `toml:"schedule,omitempty"`
	Args     []string `toml:"args,omitempty"`
}

type projectConfigLocation struct {
	Path   string
	Dir    string
	Config *ProjectConfig
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
	f, err := os.Create(loc.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := toml.NewEncoder(f)
	return encoder.Encode(loc.Config)
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
			entry.Args = append([]string{}, entry.Args...)
			return entry, true
		}
	}
	return ServiceEntry{}, false
}

func (c *ProjectConfig) SetServiceEntry(entry ServiceEntry) {
	entry.Args = append([]string{}, entry.Args...)
	for i := range c.Services {
		if c.Services[i].Name == entry.Name && c.Services[i].Host == entry.Host {
			c.Services[i].Type = entry.Type
			c.Services[i].Payload = entry.Payload
			c.Services[i].Schedule = entry.Schedule
			c.Services[i].Args = append([]string{}, entry.Args...)
			if entry.EnvFile != "" {
				c.Services[i].EnvFile = entry.EnvFile
			}
			c.addHost(entry.Host)
			sortServiceEntries(c.Services)
			return
		}
	}
	c.Services = append(c.Services, entry)
	c.addHost(entry.Host)
	sortServiceEntries(c.Services)
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
	payload = strings.TrimSpace(payload)
	if payload == "" {
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
