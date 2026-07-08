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
	Ports            []string `toml:"ports,omitempty"`
	Schedule         string   `toml:"schedule,omitempty"`
	Args             []string `toml:"args,omitempty"`
}

type projectConfigTOML struct {
	Version  int                `toml:"version,omitempty"`
	Hosts    []string           `toml:"hosts,omitempty"`
	Services []serviceEntryTOML `toml:"services,omitempty"`
}

type serviceEntryTOML struct {
	Name             string   `toml:"name"`
	Host             string   `toml:"host"`
	Type             string   `toml:"type,omitempty"`
	Payload          string   `toml:"payload,omitempty"`
	PayloadKind      string   `toml:"payload_kind,omitempty"`
	EnvFile          string   `toml:"env_file,omitempty"`
	ServiceRoot      string   `toml:"service_root,omitempty"`
	ServiceRootZFS   bool     `toml:"service_root_zfs,omitempty"`
	Snapshots        string   `toml:"snapshots,omitempty"`
	SnapshotKeepLast *int     `toml:"snapshot_keep_last,omitempty"`
	SnapshotMaxAge   string   `toml:"snapshot_max_age,omitempty"`
	SnapshotRequired *bool    `toml:"snapshot_required,omitempty"`
	SnapshotEvents   []string `toml:"snapshot_events,omitempty"`
	Ports            []string `toml:"ports,omitempty"`
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
	loc, err := loadProjectConfigFromDir(cwd)
	if err != nil || loc != nil {
		return loc, err
	}
	if err := requireClientConfig(); err != nil {
		return nil, err
	}
	return workspaceConfigForHost(Host())
}

func loadProjectConfigForCommandFromCwd() (*projectConfigLocation, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	loc, err := loadProjectConfigFromDir(cwd)
	if err != nil || loc == nil {
		if err != nil {
			return nil, err
		}
		if err := requireClientConfig(); err != nil {
			return nil, err
		}
		return workspaceConfigForHost(Host())
	}
	if err := adoptLocalProjectConfig(loc); err != nil {
		return nil, err
	}
	return loc, nil
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

var warnProjectConfigNotSavedFn = warnProjectConfigNotSaved

func projectConfigForWrite(reason string) (*projectConfigLocation, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, false, err
	}
	local, err := loadProjectConfigFromDir(cwd)
	if err != nil {
		return nil, false, err
	}
	if local != nil {
		return projectConfigWriteLocal(local)
	}
	if err := requireClientConfig(); err != nil {
		return nil, false, err
	}
	return projectConfigWriteWorkspace(reason, cwd)
}

func projectConfigWriteLocal(local *projectConfigLocation) (*projectConfigLocation, bool, error) {
	if err := adoptLocalProjectConfig(local); err != nil {
		return nil, false, err
	}
	return local, true, nil
}

func adoptLocalProjectConfig(local *projectConfigLocation) error {
	if local == nil || !canPromptForWorkspace() {
		return nil
	}
	if err := ensureClientConfigLoaded(); err != nil {
		return nil
	}
	if workspacePathRegistered(local.Dir) {
		return nil
	}
	ok, err := activePrompter.Confirm(fmt.Sprintf("Use %s as a yeet workspace?", local.Dir), true)
	if err != nil {
		return err
	}
	if ok {
		if err := addWorkspacePath(local.Dir); err != nil {
			return err
		}
		if err := adoptDefaultHostForLocalProjectConfig(local.Config); err != nil {
			return err
		}
		return saveClientConfig()
	}
	return nil
}

func adoptDefaultHostForLocalProjectConfig(cfg *ProjectConfig) error {
	hosts := claimHostsForDefaultAdoption(cfg)
	if len(hosts) == 0 || defaultHostAdoptionBlocked(hosts) {
		return nil
	}
	if len(hosts) == 1 {
		return confirmSingleDefaultHost(hosts[0])
	}
	return selectDefaultHostForLocalProjectConfig(hosts)
}

func claimHostsForDefaultAdoption(cfg *ProjectConfig) []string {
	if cfg == nil {
		return nil
	}
	return cfg.ClaimHosts()
}

func defaultHostAdoptionBlocked(hosts []string) bool {
	if _, ok := HostOverride(); ok {
		return true
	}
	current := normalizeCatchHost(loadedPrefs.DefaultHost)
	for _, host := range hosts {
		if host == current {
			return true
		}
	}
	return false
}

func confirmSingleDefaultHost(host string) error {
	ok, err := activePrompter.Confirm(fmt.Sprintf("Set %s as the default catch host?", host), true)
	if err != nil {
		return err
	}
	if ok {
		setDefaultHost(host)
	}
	return nil
}

func selectDefaultHostForLocalProjectConfig(hosts []string) error {
	current := normalizeCatchHost(loadedPrefs.DefaultHost)
	host, err := activePrompter.SelectDefaultHost(hosts, current)
	if err != nil {
		return err
	}
	host = normalizeCatchHost(host)
	if host == "" {
		return nil
	}
	for _, claimed := range hosts {
		if host == claimed {
			setDefaultHost(host)
			return nil
		}
	}
	return fmt.Errorf("default catch host must be one of: %s", strings.Join(hosts, ", "))
}

func projectConfigWriteWorkspace(reason string, cwd string) (*projectConfigLocation, bool, error) {
	loc, err := workspaceConfigForHost(Host())
	if err != nil || loc != nil {
		return loc, loc != nil, err
	}
	dir, ok, err := projectConfigWriteWorkspaceDir(reason, cwd)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, err
	}
	loc, err = seedWorkspaceConfig(dir, Host())
	if err != nil {
		return nil, false, err
	}
	if err := registerWorkspacePath(loc.Dir); err != nil {
		return nil, false, err
	}
	return loc, true, nil
}

func projectConfigWriteWorkspaceDir(reason string, cwd string) (string, bool, error) {
	if !canPromptForWorkspace() {
		warnProjectConfigNotSavedFn(reason)
		return "", false, nil
	}
	selection, err := activePrompter.SelectWorkspace(Host(), selectableWorkspacePaths(), cwd)
	if err != nil {
		return "", false, err
	}
	if selection.Choice == workspacePromptRunOnce {
		warnProjectConfigNotSavedFn(reason)
		return "", false, nil
	}
	dir := selection.Path
	if dir == "" || selection.Choice == workspacePromptUseCurrent {
		dir = cwd
	}
	return dir, true, nil
}

func canPromptForWorkspace() bool {
	return isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
}

func selectableWorkspacePaths() []string {
	return workspacePathsByConfigPresence(true)
}

func workspacePathsByConfigPresence(includeConfigured bool) []string {
	var out []string
	for _, workspace := range workspacePaths() {
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			continue
		}
		cfgPath := filepath.Join(workspace, projectConfigName)
		if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
			out = append(out, workspace)
			continue
		} else if err == nil && includeConfigured {
			out = append(out, workspace)
		}
	}
	return out
}

func addWorkspacePath(path string) error {
	workspace, err := normalizeWorkspacePath(path)
	if err != nil {
		return err
	}
	loadedPrefs.Workspaces = append(loadedPrefs.Workspaces, workspace)
	loadedPrefs.normalize()
	return nil
}

func registerWorkspacePath(path string) error {
	if err := addWorkspacePath(path); err != nil {
		return err
	}
	return saveClientConfig()
}

func workspacePathRegistered(path string) bool {
	workspace, err := normalizeWorkspacePath(path)
	if err != nil {
		return false
	}
	for _, existing := range workspacePaths() {
		if existing == workspace {
			return true
		}
	}
	return false
}

func warnProjectConfigNotSaved(reason string) {
	fmt.Fprintf(os.Stderr, "Warning: %s config was not saved; set a workspace with `yeet config --workspace PATH` to enable client-side persistence.\n", reason)
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
	loc.Config.NormalizeHosts()
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
	return encoder.Encode(projectConfigForTOML(cfg))
}

func projectConfigForTOML(cfg *ProjectConfig) projectConfigTOML {
	if cfg == nil {
		return projectConfigTOML{}
	}
	out := projectConfigTOML{
		Version: cfg.Version,
		Hosts:   cloneStringSlice(cfg.Hosts),
	}
	out.Services = make([]serviceEntryTOML, 0, len(cfg.Services))
	for _, entry := range cfg.Services {
		out.Services = append(out.Services, serviceEntryForTOML(entry))
	}
	return out
}

func serviceEntryForTOML(entry ServiceEntry) serviceEntryTOML {
	out := serviceEntryTOML{
		Name:             entry.Name,
		Host:             entry.Host,
		Type:             entry.Type,
		Payload:          entry.Payload,
		PayloadKind:      entry.PayloadKind,
		EnvFile:          entry.EnvFile,
		ServiceRoot:      entry.ServiceRoot,
		ServiceRootZFS:   entry.ServiceRootZFS,
		Snapshots:        entry.Snapshots,
		SnapshotMaxAge:   entry.SnapshotMaxAge,
		SnapshotRequired: cloneBoolPtr(entry.SnapshotRequired),
		SnapshotEvents:   cloneStringSlice(entry.SnapshotEvents),
		Ports:            cloneStringSlice(entry.Ports),
		Schedule:         entry.Schedule,
		Args:             cloneStringSlice(entry.Args),
	}
	if entry.SnapshotKeepLast != 0 {
		keepLast := entry.SnapshotKeepLast
		out.SnapshotKeepLast = &keepLast
	}
	return out
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

func (c *ProjectConfig) ClaimHosts() []string {
	if c == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, host := range c.Hosts {
		if h := normalizeCatchHost(host); h != "" {
			seen[h] = struct{}{}
		}
	}
	for _, entry := range c.Services {
		if h := normalizeCatchHost(entry.Host); h != "" {
			seen[h] = struct{}{}
		}
	}
	hosts := make([]string, 0, len(seen))
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func (c *ProjectConfig) NormalizeHosts() {
	if c == nil {
		return
	}
	for i := range c.Services {
		c.Services[i].Host = normalizeCatchHost(c.Services[i].Host)
	}
	c.Hosts = c.ClaimHosts()
}

func projectConfigClaimsHost(cfg *ProjectConfig, host string) bool {
	if cfg == nil {
		return false
	}
	host = normalizeCatchHost(host)
	if host == "" {
		return false
	}
	for _, claimed := range cfg.ClaimHosts() {
		if claimed == host {
			return true
		}
	}
	return false
}

func workspaceConfigForHost(host string) (*projectConfigLocation, error) {
	host = normalizeCatchHost(host)
	if host == "" {
		host = Host()
	}
	paths, err := workspaceConfigConflicts(host)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > 1 {
		return nil, fmt.Errorf("%s is claimed by multiple workspaces: %s", host, strings.Join(paths, ", "))
	}
	workspace := paths[0]
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return nil, nil
	}
	return loadProjectConfigFromFile(filepath.Join(workspace, projectConfigName))
}

func workspaceConfigConflicts(host string) ([]string, error) {
	host = normalizeCatchHost(host)
	if host == "" {
		host = Host()
	}
	var paths []string
	for _, workspace := range workspacePaths() {
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			continue
		}
		loc, err := loadProjectConfigFromFile(filepath.Join(workspace, projectConfigName))
		if err != nil {
			if strings.Contains(err.Error(), "no yeet.toml found at") {
				continue
			}
			return nil, err
		}
		if projectConfigClaimsHost(loc.Config, host) {
			paths = append(paths, loc.Dir)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func seedWorkspaceConfig(dir string, host string) (*projectConfigLocation, error) {
	workspace, err := normalizeWorkspacePath(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(workspace, projectConfigName)
	if _, err := os.Stat(path); err == nil {
		loc, err := loadProjectConfigFromFile(path)
		if err != nil {
			return nil, err
		}
		if err := ensureProjectConfigHost(loc, host); err != nil {
			return nil, err
		}
		return loc, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	host = normalizeCatchHost(host)
	cfg := &ProjectConfig{Version: projectConfigVersion, Hosts: []string{host}}
	loc := &projectConfigLocation{Path: path, Dir: workspace, Config: cfg}
	raw := fmt.Sprintf("version = 1\nhosts = [%q]\n\n# [[services]]\n# name = \"hello\"\n# host = %q\n# payload = \"nginx:alpine\"\n# args = [\"-p\", \"18080:80\"]\n", host, host)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		return nil, err
	}
	return loc, nil
}

func ensureProjectConfigHost(loc *projectConfigLocation, host string) error {
	if loc == nil || loc.Config == nil {
		return nil
	}
	host = normalizeCatchHost(host)
	if host == "" || projectConfigClaimsHost(loc.Config, host) {
		return nil
	}
	loc.Config.addHost(host)
	return saveProjectConfig(loc)
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
			entry.Ports = cloneStringSlice(entry.Ports)
			return entry, true
		}
	}
	return ServiceEntry{}, false
}

func (c *ProjectConfig) SetServiceEntry(entry ServiceEntry) {
	entry.Args = cloneStringSlice(entry.Args)
	entry.SnapshotRequired = cloneBoolPtr(entry.SnapshotRequired)
	entry.SnapshotEvents = cloneStringSlice(entry.SnapshotEvents)
	entry.Ports = cloneStringSlice(entry.Ports)
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
			c.Services[i].Ports = cloneStringSlice(entry.Ports)
			c.addHost(entry.Host)
			sortServiceEntries(c.Services)
			return
		}
	}
	c.Services = append(c.Services, entry)
	c.addHost(entry.Host)
	sortServiceEntries(c.Services)
}

func (c *ProjectConfig) ReplaceServiceEntry(entry ServiceEntry) {
	entry.Args = cloneStringSlice(entry.Args)
	entry.SnapshotRequired = cloneBoolPtr(entry.SnapshotRequired)
	entry.SnapshotEvents = cloneStringSlice(entry.SnapshotEvents)
	entry.Ports = cloneStringSlice(entry.Ports)
	for i := range c.Services {
		if c.Services[i].Name == entry.Name && c.Services[i].Host == entry.Host {
			c.Services[i] = entry
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
	if looksLikeImageRef(payload) {
		return payload
	}
	if filepath.IsAbs(payload) {
		return payload
	}
	return filepath.Join(configDir, payload)
}

func resolvePayloadPathForEntry(configDir string, entry ServiceEntry) string {
	if strings.TrimSpace(entry.PayloadKind) == serviceTypeVM || isVMPayload(entry.Payload) {
		return strings.TrimSpace(entry.Payload)
	}
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
	if strings.TrimSpace(payloadKind) == serviceTypeVM || isVMPayload(payload) {
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
