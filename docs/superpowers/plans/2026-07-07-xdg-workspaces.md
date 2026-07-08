# XDG Workspaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move yeet client defaults to XDG TOML config and make host-scoped service workspaces resolve from anywhere without silently writing `yeet.toml` in random directories.

**Architecture:** Keep `yeet.toml` as the project source of truth and add a local XDG client config that stores `default_host` plus registered workspace paths. Centralize config loading in `pkg/yeet`, make project config lookup fall back to the workspace that owns the effective catch host, and route write-capable commands through an adoption helper before saving. Add a shared prompt abstraction, then convert all `yeet init` prompts to Charm `huh` in a separate implementation commit.

**Tech Stack:** Go, BurntSushi TOML, Charm `huh`, `github.com/shayne/yargs`, Go `testing`, website MDX docs, GitButler (`but`), `mise exec -- go ...`.

## Global Constraints

- Canonical client config path is `$XDG_CONFIG_HOME/yeet/config.toml`, or `~/.config/yeet/config.toml` when `XDG_CONFIG_HOME` is unset.
- Canonical TOML schema uses `default_host` and `workspaces`.
- Legacy `~/.yeet/prefs.json` migrates once, then the old file is deleted and `~/.yeet` is removed if empty.
- Migration must not discover workspaces from the filesystem.
- `yeet prefs` is removed; `yeet config` replaces it and saves mutations immediately.
- Workspace paths in XDG config are absolute, cleaned paths.
- Catch host names are matched and stored lowercase; service names are not changed.
- One workspace may own many catch hosts; one catch host must not be owned by multiple registered workspaces.
- `yeet.toml` host ownership is derived from top-level `hosts` and service-entry `host` fields.
- Local/upward `yeet.toml` wins over XDG workspace fallback.
- Explicit command-specific `--config` keeps winning for commands that already support it.
- This work must not add a global `--config` flag.
- Read-only commands do not prompt to create workspaces when no project config exists.
- Write-capable commands may run without saving if the user declines workspace adoption, and must warn.
- Non-interactive init does nothing with workspaces unless `--workspace` is passed.
- `yeet init --workspace PATH` creates the directory if needed; `yeet config --workspace` and `--add-workspace` require an existing directory.
- The positional `yeet init root@...` argument is an SSH machine host, not a catch host.
- `yeet status` and `yeet upgrade check` use all hosts from the resolved workspace once a workspace is selected.
- Prompt conversion to Charm `huh` is part of this work but should be reviewed separately from workspace semantics.

---

## Scope Check

This is one feature with five cooperating surfaces: local client config, project config resolution, command/config routing, init workspace setup, and prompt rendering. It is not independent enough to split into separate feature specs because each part needs the same host-to-workspace semantics. The plan splits implementation into reviewable tasks and at least two implementation commits: functional workspace behavior first, prompt-layer conversion second.

## File Structure

- Modify `pkg/yeet/prefs.go`
  - Replace JSON prefs persistence with XDG TOML client config while preserving existing internal host helper names where that limits churn.
  - Add config path resolution, legacy migration, lowercase host normalization, workspace path normalization, config save/load, and `HandleConfig`.
- Replace or remove `pkg/yeet/prefs_test.go`
  - Cover XDG TOML config load/save, migration, cleanup, host override behavior, and `yeet config` command mutations.
- Modify `pkg/yeet/project_config.go`
  - Add host-claim extraction, host normalization on save, workspace lookup by host, seed/update helpers, and known-workspace write helpers.
- Modify `pkg/yeet/project_config_test.go`
  - Cover host claims, lowercase normalization, local config precedence, workspace fallback, duplicate ownership, malformed workspace config, and seed/update behavior.
- Create `pkg/yeet/prompts.go`
  - Add a package-local prompt interface plus the initial simple implementation used by workspace adoption.
- Create `pkg/yeet/prompts_huh.go`
  - Add Charm `huh` prompt implementation in the prompt conversion task.
- Modify `pkg/yeet/init.go`
  - Parse `--workspace` and `--no-workspace`, carry them through `initOptions`, call post-install workspace setup after remote install succeeds, and print next steps.
- Create `pkg/yeet/init_workspace.go`
  - Implement workspace prompt/defaulting, explicit `--workspace`, seed/update `yeet.toml`, and local setup error wrapping.
- Modify `pkg/yeet/init_test.go`
  - Cover init flag parsing, post-success workspace setup, non-interactive behavior, explicit workspace creation, `--no-workspace`, and failure messages.
- Modify `pkg/yeet/init_storage.go`
  - In the prompt conversion task, route storage prompts through the shared prompt layer instead of direct `bufio.Reader` prompt helpers.
- Modify `cmd/yeet/yeet.go`
  - Register `config` instead of `prefs`; keep help/schema behavior safe when config parse fails.
- Modify `cmd/yeet/cli.go`
  - Replace help metadata for `prefs` with `config`; document init workspace flags.
- Modify `cmd/yeet/cli_test.go`
  - Cover `prefs` removal, `config` help, init help flags, and help still working with malformed config.
- Modify `pkg/yeet/svc_cmd.go`
  - Use fallback-aware config resolution and write-known-workspace helpers for service commands.
- Modify `pkg/yeet/run_changes.go`
  - Stop creating arbitrary cwd `yeet.toml` for env-file writes; use the same known-workspace helper.
- Modify `pkg/yeet/run_draft.go`
  - Ensure web and CLI drafts share the same write/no-save persistence path.
- Modify `pkg/yeet/service_sync.go`
  - Let service sync use workspace fallback when no explicit `--config` is passed.
- Modify `pkg/yeet/host_set.go`
  - Let host storage config updates use workspace fallback when no explicit `--config` is passed.
- Modify `pkg/yeet/upgrade_cmd.go` and `pkg/yeet/update_advisory.go`
  - Use workspace fallback so `upgrade check` and update advice see project hosts from the resolved workspace.
- Modify website docs in `README.md`, `website/docs/getting-started/quick-start.mdx`, `website/docs/getting-started/host-setup.mdx`, `website/docs/getting-started/service-workspace.mdx`, `website/docs/concepts/configuration-and-prefs.mdx`, `website/docs/operations/workflows.mdx`, and `website/docs/cli/yeet-cli.mdx`.
- Modify `website/docs/changelog.mdx`
  - Add the breaking-change release note required by the spec.
- Read `website/AGENTS.md` before editing website docs.
  - Commit and push the website submodule docs changes inside `website/` first, then include the root `website` gitlink update in the root branch commit.

---

### Task 0: Branch And Baseline Checks

**Files:**
- Read: `AGENTS.md`
- Read: `AGENTS.local.md`
- Read: `docs/agent/codebase-map.md`
- Read: `pkg/yeet/AGENTS.md`
- Read: `cmd/yeet/AGENTS.md`

**Interfaces:**
- Consumes: approved design spec at `docs/superpowers/specs/2026-07-07-xdg-workspaces-design.md`.
- Produces: confirmed clean starting point for implementation tasks.

- [ ] **Step 1: Check the GitButler base**

Run:

```bash
but pull --check
```

Expected: `Up to date` or a clean pull check. If it reports conflicts or another active branch touching `pkg/yeet`, `cmd/yeet`, `pkg/cli`, `README.md`, or `website/`, stop and ask the user.

- [ ] **Step 2: Inspect dirty changes**

Run:

```bash
but diff
```

Expected: no uncommitted changes, or only approved spec/plan docs on this session branch. Do not discard or move another branch's work.

- [ ] **Step 3: Read local package guidance**

Run:

```bash
sed -n '1,220p' AGENTS.local.md
sed -n '1,220p' docs/agent/codebase-map.md
sed -n '1,180p' pkg/yeet/AGENTS.md
sed -n '1,180p' cmd/yeet/AGENTS.md
```

Expected: guidance confirms this is `pkg/yeet` client orchestration plus `cmd/yeet` command registration work.

---

### Task 1: Add XDG TOML Client Config And Migration

**Files:**
- Modify: `pkg/yeet/prefs.go`
- Modify: `pkg/yeet/prefs_test.go`

**Interfaces:**
- Consumes: existing `Host()`, `SetHost(string)`, `SetHostOverride(string)`, `HostOverride() (string, bool)`, `SetServiceOverride(string)`, and `resetHostOverride()`.
- Produces:
  - `func normalizeCatchHost(host string) string`
  - `func clientConfigPath() (string, error)`
  - `func legacyPrefsPath() string`
  - `func ensureClientConfigLoaded() error`
  - `func requireClientConfig() error`
  - `func saveClientConfig() error`
  - `func workspacePaths() []string`
  - `func setDefaultHost(host string)`
  - `func setWorkspaces(paths []string) error`
  - existing host override helpers backed by the new client config state.

- [ ] **Step 1: Write failing tests for XDG path resolution and TOML round trip**

Replace the old JSON-oriented tests in `pkg/yeet/prefs_test.go` with tests that keep `TestPrefsHostSettersAndOverrides` but expect lowercase normalization. Add these tests:

```go
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
	restore := stubClientConfigState(t, clientConfig{
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
```

- [ ] **Step 2: Write failing tests for legacy migration and cleanup**

Add these tests to `pkg/yeet/prefs_test.go`:

```go
func TestClientConfigMigratesLegacyPrefsAndDeletesOldFile(t *testing.T) {
	tmp := t.TempDir()
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
```

- [ ] **Step 3: Write failing tests for parse errors and host override semantics**

Add these tests to `pkg/yeet/prefs_test.go`:

```go
func TestClientConfigParseErrorIsRequiredOnDemand(t *testing.T) {
	tmp := t.TempDir()
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
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab"})
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
}
```

- [ ] **Step 4: Implement the client config type and path helpers**

In `pkg/yeet/prefs.go`, replace the old JSON prefs persistence with this structure. Keep the filename for a smaller patch; the user-facing `prefs` command is removed in Task 3.

```go
var (
	clientConfigPathFn = defaultClientConfigPath
	legacyPrefsPathFn = defaultLegacyPrefsPath

	serviceOverride string
	hostOverride    string
	hostOverrideSet bool
	loadedPrefs     clientConfig
)

type clientConfig struct {
	loaded      bool     `toml:"-"`
	loadErr     error    `toml:"-"`
	warnings    []string `toml:"-"`
	DefaultHost string   `toml:"default_host,omitempty"`
	Workspaces  []string `toml:"workspaces,omitempty"`
}

type legacyPrefsJSON struct {
	DefaultHost string `json:"defaultHost"`
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
```

- [ ] **Step 5: Implement lazy load, TOML save, and legacy migration**

Continue in `pkg/yeet/prefs.go` with these functions. Import `bytes`, `encoding/json`, `errors`, `github.com/BurntSushi/toml`, `io/fs`, `strings`, and remove the old `log` import.

```go
func init() {
	loadedPrefs.DefaultHost = defaultCatchHost
}

func ensureClientConfigLoaded() error {
	if loadedPrefs.loaded {
		return loadedPrefs.loadErr
	}
	loadedPrefs.loaded = true
	if err := loadedPrefs.load(); err != nil {
		loadedPrefs.loadErr = err
	}
	if envHost := normalizeCatchHost(os.Getenv("CATCH_HOST")); envHost != "" {
		hostOverride = envHost
		hostOverrideSet = true
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
	c.normalize()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(clientConfigForTOML(*c)); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func clientConfigForTOML(c clientConfig) clientConfig {
	return clientConfig{DefaultHost: c.DefaultHost, Workspaces: append([]string{}, c.Workspaces...)}
}
```

- [ ] **Step 6: Implement normalization and host helper behavior**

Add these functions in `pkg/yeet/prefs.go`:

```go
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
	hostOverride = host
	hostOverrideSet = true
}

func SetHostOverride(host string) {
	SetHost(host)
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
```

Keep `HostOverride`, `resetHostOverride`, and `SetServiceOverride`, but make sure `SetServiceOverride` lowercases any host parsed from `<svc>@<host>`.

- [ ] **Step 7: Add test helpers and run focused tests**

Add this helper near the bottom of `pkg/yeet/prefs_test.go`:

```go
func stubClientConfigState(t *testing.T, next clientConfig) func() {
	t.Helper()
	oldPrefs := loadedPrefs
	oldService := serviceOverride
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	next.loaded = true
	next.normalize()
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
```

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestClientConfig|TestPrefsHostSettersAndOverrides|TestRuntimeHostOverridesDoNotSaveDefaultHost' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the client config layer**

Run:

```bash
but diff
but commit codex/xdg-workspaces-design -m "pkg/yeet: add xdg client config"
```

Expected: GitButler creates a commit containing only `pkg/yeet/prefs.go` and `pkg/yeet/prefs_test.go`.

---

### Task 2: Add Workspace Ownership Helpers To Project Config

**Files:**
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/project_config_test.go`

**Interfaces:**
- Consumes: `normalizeCatchHost(string) string`, `workspacePaths() []string`, existing `ProjectConfig` and `projectConfigLocation`.
- Produces:
  - `func (c *ProjectConfig) ClaimHosts() []string`
  - `func (c *ProjectConfig) NormalizeHosts()`
  - `func workspaceConfigForHost(host string) (*projectConfigLocation, error)`
  - `func workspaceConfigConflicts(host string) ([]string, error)`
  - `func seedWorkspaceConfig(dir string, host string) (*projectConfigLocation, error)`
  - `func ensureProjectConfigHost(loc *projectConfigLocation, host string) error`

- [ ] **Step 1: Write failing tests for host claims and normalization**

Add these tests to `pkg/yeet/project_config_test.go` near the `AllHosts` tests:

```go
func TestProjectConfigClaimHostsUsesHostsAndServices(t *testing.T) {
	cfg := &ProjectConfig{
		Hosts: []string{"Yeet-Lab", " "},
		Services: []ServiceEntry{
			{Name: "plex", Host: "YEET-CLOUD"},
			{Name: "empty", Host: " "},
		},
	}
	got := cfg.ClaimHosts()
	want := []string{"yeet-cloud", "yeet-lab"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaimHosts = %#v, want %#v", got, want)
	}
}

func TestSaveProjectConfigNormalizesHosts(t *testing.T) {
	tmp := t.TempDir()
	cfg := &ProjectConfig{
		Version: projectConfigVersion,
		Hosts:   []string{"Yeet-Lab"},
		Services: []ServiceEntry{{
			Name:    "plex",
			Host:    "YEET-CLOUD",
			Payload: "compose.yml",
		}},
	}
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}
	raw, err := os.ReadFile(loc.Path)
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`hosts = ["yeet-cloud", "yeet-lab"]`, `host = "yeet-cloud"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("config = %q, want %s", text, want)
		}
	}
}
```

- [ ] **Step 2: Write failing tests for workspace lookup**

Add these tests to `pkg/yeet/project_config_test.go`:

```go
func TestWorkspaceConfigForHostSelectsRegisteredWorkspace(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "services")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir workspace: %v", err)
	}
	loc := &projectConfigLocation{
		Path: filepath.Join(workspace, projectConfigName),
		Dir:  workspace,
		Config: &ProjectConfig{
			Version: projectConfigVersion,
			Hosts:   []string{"yeet-lab"},
		},
	}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab", Workspaces: []string{workspace}})
	defer restore()

	got, err := workspaceConfigForHost("YEET-LAB")
	if err != nil {
		t.Fatalf("workspaceConfigForHost error: %v", err)
	}
	if got == nil || got.Dir != workspace {
		t.Fatalf("workspaceConfigForHost = %#v, want workspace", got)
	}
}

func TestWorkspaceConfigForHostErrorsOnDuplicateClaims(t *testing.T) {
	tmp := t.TempDir()
	var workspaces []string
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(tmp, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("Mkdir %s: %v", name, err)
		}
		loc := &projectConfigLocation{
			Path:   filepath.Join(dir, projectConfigName),
			Dir:    dir,
			Config: &ProjectConfig{Version: projectConfigVersion, Hosts: []string{"yeet-lab"}},
		}
		if err := saveProjectConfig(loc); err != nil {
			t.Fatalf("saveProjectConfig %s: %v", name, err)
		}
		workspaces = append(workspaces, dir)
	}
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab", Workspaces: workspaces})
	defer restore()

	_, err := workspaceConfigForHost("yeet-lab")
	if err == nil || !strings.Contains(err.Error(), "claimed by multiple workspaces") {
		t.Fatalf("workspaceConfigForHost error = %v, want duplicate claim", err)
	}
	for _, dir := range workspaces {
		if !strings.Contains(err.Error(), dir) {
			t.Fatalf("error = %v, want path %s", err, dir)
		}
	}
}
```

- [ ] **Step 3: Write failing tests for seed/update helpers**

Add:

```go
func TestSeedWorkspaceConfigCreatesCommentedSeed(t *testing.T) {
	dir := t.TempDir()
	loc, err := seedWorkspaceConfig(dir, "Yeet-Lab")
	if err != nil {
		t.Fatalf("seedWorkspaceConfig error: %v", err)
	}
	if loc.Dir != dir {
		t.Fatalf("Dir = %q, want %q", loc.Dir, dir)
	}
	raw, err := os.ReadFile(filepath.Join(dir, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile seed: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`version = 1`, `hosts = ["yeet-lab"]`, `# [[services]]`, `# payload = "nginx:alpine"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("seed = %q, want %s", text, want)
		}
	}
}

func TestEnsureProjectConfigHostRejectsMalformedExistingConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, projectConfigName), []byte("bad = ["), 0o600); err != nil {
		t.Fatalf("WriteFile bad config: %v", err)
	}
	_, err := seedWorkspaceConfig(dir, "yeet-lab")
	if err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Fatalf("seedWorkspaceConfig error = %v, want parse error", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, projectConfigName))
	if string(raw) != "bad = [" {
		t.Fatalf("bad config was modified: %q", string(raw))
	}
}
```

- [ ] **Step 4: Implement host claim and normalization helpers**

In `pkg/yeet/project_config.go`, add:

```go
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
```

Call `loc.Config.NormalizeHosts()` in `saveProjectConfig` before `sortServiceEntries`.

- [ ] **Step 5: Implement workspace lookup and seed helpers**

Add to `pkg/yeet/project_config.go`:

```go
func workspaceConfigForHost(host string) (*projectConfigLocation, error) {
	host = normalizeCatchHost(host)
	if host == "" {
		host = Host()
	}
	var matches []*projectConfigLocation
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
			matches = append(matches, loc)
			paths = append(paths, loc.Dir)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) > 1 {
		sort.Strings(paths)
		return nil, fmt.Errorf("%s is claimed by multiple workspaces: %s", host, strings.Join(paths, ", "))
	}
	return matches[0], nil
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
```

- [ ] **Step 6: Run targeted project config tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestProjectConfigClaimHosts|TestSaveProjectConfigNormalizesHosts|TestWorkspaceConfigForHost|TestSeedWorkspaceConfig|TestEnsureProjectConfigHost' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit workspace ownership helpers**

Run:

```bash
but diff
but commit codex/xdg-workspaces-design -m "pkg/yeet: map hosts to workspaces"
```

Expected: GitButler creates a commit for `pkg/yeet/project_config.go` and `pkg/yeet/project_config_test.go`.

---

### Task 3: Replace `yeet prefs` With `yeet config`

**Files:**
- Modify: `pkg/yeet/prefs.go`
- Modify: `pkg/yeet/prefs_test.go`
- Modify: `cmd/yeet/yeet.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_test.go`

**Interfaces:**
- Consumes: Task 1 config load/save helpers.
- Produces:
  - `type configFlagsParsed struct`
  - `func HandleConfig(context.Context, []string) error`
  - command registration for `config`
  - no command registration for `prefs`.

- [ ] **Step 1: Add failing config command tests**

In `pkg/yeet/prefs_test.go`, remove the old `HandlePrefs` tests and add:

```go
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
```

- [ ] **Step 2: Implement `HandleConfig`**

In `pkg/yeet/prefs.go`, remove `prefsFlagsParsed` and `HandlePrefs`. Add:

```go
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
```

- [ ] **Step 3: Replace command registration in `cmd/yeet/yeet.go`**

Change:

```go
handlers["prefs"] = yeet.HandlePrefs
```

to:

```go
handlers["config"] = yeet.HandleConfig
```

- [ ] **Step 4: Replace help metadata in `cmd/yeet/cli.go`**

Replace the `prefs` subcommand block with:

```go
subcommands["config"] = yargs.SubCommandInfo{
	Name:        "config",
	Description: "Show or update local yeet client config",
	Usage:       "[--host=<catch-host>] [--workspace=PATH] [--add-workspace=PATH] [--remove-workspace=PATH] [--clear-workspaces]",
	Examples: []string{
		"yeet config",
		"yeet config --host=yeet-lab",
		"yeet config --workspace ~/yeet-services",
		"yeet config --add-workspace ~/lab-services",
		"yeet config --remove-workspace ~/lab-services",
		"yeet config --clear-workspaces",
	},
}
```

- [ ] **Step 5: Add CLI tests for command registration**

In `cmd/yeet/cli_test.go`, add:

```go
func TestBuildHelpConfigRegistersConfigAndNotPrefs(t *testing.T) {
	cfg := buildHelpConfig()
	if _, ok := cfg.SubCommands["config"]; !ok {
		t.Fatal("config subcommand missing")
	}
	if _, ok := cfg.SubCommands["prefs"]; ok {
		t.Fatal("prefs subcommand should be removed")
	}
}
```

- [ ] **Step 6: Run focused command tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestHandleConfig|TestClientConfig|TestPrefsHostSettersAndOverrides' -count=1
mise exec -- go test ./cmd/yeet -run 'TestBuildHelpConfigRegistersConfigAndNotPrefs' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the config command**

Run:

```bash
but diff
but commit codex/xdg-workspaces-design -m "cmd/yeet: replace prefs with config"
```

Expected: GitButler creates a commit for config command code and tests.

---

### Task 4: Wire Workspace Fallback Into Project Config Reads And Writes

**Files:**
- Create: `pkg/yeet/prompts.go`
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/run_changes.go`
- Modify: `pkg/yeet/run_draft.go`
- Modify: `pkg/yeet/service_sync.go`
- Modify: `pkg/yeet/host_set.go`
- Modify: `pkg/yeet/upgrade_cmd.go`
- Modify: `pkg/yeet/update_advisory.go`
- Modify: `pkg/yeet/project_config_test.go`
- Modify: `pkg/yeet/run_changes_test.go`
- Modify: `pkg/yeet/service_sync_test.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

**Interfaces:**
- Consumes: `workspaceConfigForHost`, `seedWorkspaceConfig`, `ensureProjectConfigHost`, `workspacePaths`.
- Produces:
  - `type workspacePromptChoice int`
  - `type yeetPrompter interface`
  - `var activePrompter yeetPrompter`
  - `func loadProjectConfigForCommand(readOnly bool) (*projectConfigLocation, error)`
  - `func projectConfigForWrite(reason string) (*projectConfigLocation, bool, error)`
  - `func warnProjectConfigNotSaved(reason string)`

- [ ] **Step 1: Add the initial prompt abstraction**

Create `pkg/yeet/prompts.go`:

```go
package yeet

import (
	"fmt"
	"os"
)

type workspacePromptChoice int

const (
	workspacePromptUseCurrent workspacePromptChoice = iota
	workspacePromptUseKnown
	workspacePromptRunOnce
)

type workspaceSelection struct {
	Choice workspacePromptChoice
	Path   string
}

type yeetPrompter interface {
	Confirm(msg string, def bool) (bool, error)
	SelectWorkspace(host string, paths []string, current string) (workspaceSelection, error)
	Input(msg string, def string) (string, error)
	Secret(msg string) (string, error)
}

var activePrompter yeetPrompter = plainPrompter{}

type plainPrompter struct{}

func (plainPrompter) Confirm(msg string, def bool) (bool, error) {
	return cmdutil.Confirm(os.Stdin, os.Stdout, msg)
}

func (plainPrompter) SelectWorkspace(host string, paths []string, current string) (workspaceSelection, error) {
	if len(paths) == 1 {
		ok, err := plainPrompter{}.Confirm(fmt.Sprintf("No workspace is associated with %s. Use %s for %s?", host, paths[0], host), true)
		if err != nil || !ok {
			return workspaceSelection{Choice: workspacePromptRunOnce}, err
		}
		return workspaceSelection{Choice: workspacePromptUseKnown, Path: paths[0]}, nil
	}
	ok, err := plainPrompter{}.Confirm(fmt.Sprintf("Use %s as a yeet workspace?", current), true)
	if err != nil || !ok {
		return workspaceSelection{Choice: workspacePromptRunOnce}, err
	}
	return workspaceSelection{Choice: workspacePromptUseCurrent, Path: current}, nil
}

func (plainPrompter) Input(msg string, def string) (string, error) {
	fmt.Fprintf(os.Stdout, "%s [%s]: ", msg, def)
	var value string
	if _, err := fmt.Fscanln(os.Stdin, &value); err != nil && err.Error() != "unexpected newline" {
		return "", err
	}
	if value == "" {
		return def, nil
	}
	return value, nil
}

func (plainPrompter) Secret(msg string) (string, error) {
	fmt.Fprintf(os.Stdout, "%s: ", msg)
	var value string
	if _, err := fmt.Fscanln(os.Stdin, &value); err != nil {
		return "", err
	}
	return value, nil
}
```

Add the missing `cmdutil` import. The final Charm implementation replaces this behavior in Task 6.

- [ ] **Step 2: Add failing tests for workspace fallback and no-save writes**

In `pkg/yeet/project_config_test.go`, add:

```go
func TestLoadProjectConfigFromCwdFallsBackToWorkspaceForHost(t *testing.T) {
	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	random := filepath.Join(tmp, "random")
	workspace := filepath.Join(tmp, "workspace")
	if err := os.Mkdir(random, 0o755); err != nil {
		t.Fatalf("Mkdir random: %v", err)
	}
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir workspace: %v", err)
	}
	if err := os.Chdir(random); err != nil {
		t.Fatalf("Chdir random: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	if _, err := seedWorkspaceConfig(workspace, "yeet-lab"); err != nil {
		t.Fatalf("seedWorkspaceConfig: %v", err)
	}
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab", Workspaces: []string{workspace}})
	defer restore()

	loc, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	if loc == nil || loc.Dir != workspace {
		t.Fatalf("location = %#v, want workspace fallback", loc)
	}
}

func TestLoadProjectConfigFromCwdLocalConfigWins(t *testing.T) {
	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	local := filepath.Join(tmp, "local")
	workspace := filepath.Join(tmp, "workspace")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatalf("Mkdir local: %v", err)
	}
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir workspace: %v", err)
	}
	if _, err := seedWorkspaceConfig(local, "yeet-lab"); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	if _, err := seedWorkspaceConfig(workspace, "yeet-lab"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := os.Chdir(local); err != nil {
		t.Fatalf("Chdir local: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab", Workspaces: []string{workspace}})
	defer restore()

	loc, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	if loc == nil || loc.Dir != local {
		t.Fatalf("location = %#v, want local config", loc)
	}
}
```

In `pkg/yeet/run_changes_test.go`, add:

```go
func TestSaveEnvFileConfigSkipsPersistenceWhenWorkspaceDeclined(t *testing.T) {
	oldService := serviceOverride
	serviceOverride = "api"
	defer func() { serviceOverride = oldService }()
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab"})
	defer restore()
	var warned string
	oldWarn := warnProjectConfigNotSavedFn
	warnProjectConfigNotSavedFn = func(reason string) { warned = reason }
	t.Cleanup(func() { warnProjectConfigNotSavedFn = oldWarn })
	oldPrompt := activePrompter
	activePrompter = fakePrompter{selection: workspaceSelection{Choice: workspacePromptRunOnce}}
	t.Cleanup(func() { activePrompter = oldPrompt })

	if err := saveEnvFileConfig(nil, "yeet-lab", ".env"); err != nil {
		t.Fatalf("saveEnvFileConfig error: %v", err)
	}
	if warned == "" {
		t.Fatal("warning not emitted")
	}
}
```

In `pkg/yeet/project_config_test.go`, add a test for registering an existing local `yeet.toml` when no workspace is configured:

```go
func TestProjectConfigForWritePromptsToRegisterLocalConfig(t *testing.T) {
	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	if _, err := seedWorkspaceConfig(tmp, "yeet-lab"); err != nil {
		t.Fatalf("seedWorkspaceConfig: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir tmp: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab"})
	defer restore()
	oldPrompt := activePrompter
	activePrompter = fakePrompter{confirm: true}
	t.Cleanup(func() { activePrompter = oldPrompt })

	loc, saved, err := projectConfigForWrite("service")
	if err != nil {
		t.Fatalf("projectConfigForWrite error: %v", err)
	}
	if !saved || loc == nil || loc.Dir != tmp {
		t.Fatalf("loc saved = %#v %v, want local config registered", loc, saved)
	}
	if got := workspacePaths(); !reflect.DeepEqual(got, []string{tmp}) {
		t.Fatalf("workspacePaths = %#v, want local dir", got)
	}
}
```

Add a simple fake prompter in `pkg/yeet/prefs_test.go` or a new shared test helper file:

```go
type fakePrompter struct {
	confirm   bool
	input     string
	secret    string
	selection workspaceSelection
	err       error
}

func (f fakePrompter) Confirm(string, bool) (bool, error) { return f.confirm, f.err }
func (f fakePrompter) Input(string, string) (string, error) { return f.input, f.err }
func (f fakePrompter) Secret(string) (string, error) { return f.secret, f.err }
func (f fakePrompter) SelectWorkspace(string, []string, string) (workspaceSelection, error) {
	return f.selection, f.err
}
```

- [ ] **Step 3: Implement fallback-aware `loadProjectConfigFromCwd`**

Change `loadProjectConfigFromCwd` in `pkg/yeet/project_config.go` to:

```go
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
```

Keep `loadProjectConfigFromDir` as local/upward only so tests and explicit callers can bypass workspace fallback when needed.

- [ ] **Step 4: Implement known-workspace write helper**

Add to `pkg/yeet/project_config.go`:

```go
var warnProjectConfigNotSavedFn = warnProjectConfigNotSaved

func projectConfigForWrite(reason string) (*projectConfigLocation, bool, error) {
	if err := requireClientConfig(); err != nil {
		return nil, false, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, false, err
	}
	local, err := loadProjectConfigFromDir(cwd)
	if err != nil {
		return nil, false, err
	}
	if local != nil {
		if !workspacePathRegistered(local.Dir) && canPromptForWorkspace() {
			ok, err := activePrompter.Confirm(fmt.Sprintf("Use %s as a yeet workspace?", local.Dir), true)
			if err != nil {
				return nil, false, err
			}
			if ok {
				if err := addWorkspacePath(local.Dir); err != nil {
					return nil, false, err
				}
				if err := saveClientConfig(); err != nil {
					return nil, false, err
				}
			}
		}
		return local, true, nil
	}
	loc, err := workspaceConfigForHost(Host())
	if err != nil || loc != nil {
		return loc, loc != nil, err
	}
	if !canPromptForWorkspace() {
		warnProjectConfigNotSavedFn(reason)
		return nil, false, nil
	}
	bare := bareWorkspacePaths()
	selection, err := activePrompter.SelectWorkspace(Host(), bare, cwd)
	if err != nil {
		return nil, false, err
	}
	if selection.Choice == workspacePromptRunOnce {
		warnProjectConfigNotSavedFn(reason)
		return nil, false, nil
	}
	dir := selection.Path
	if dir == "" || selection.Choice == workspacePromptUseCurrent {
		dir = cwd
	}
	loc, err = seedWorkspaceConfig(dir, Host())
	if err != nil {
		return nil, false, err
	}
	if err := addWorkspacePath(loc.Dir); err != nil {
		return nil, false, err
	}
	if err := saveClientConfig(); err != nil {
		return nil, false, err
	}
	return loc, true, nil
}

func canPromptForWorkspace() bool {
	return isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
}

func bareWorkspacePaths() []string {
	var out []string
	for _, workspace := range workspacePaths() {
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(workspace, projectConfigName)); os.IsNotExist(err) {
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
```

- [ ] **Step 5: Replace silent create-on-cwd writes**

Change `runConfigLocation` in `pkg/yeet/svc_cmd.go`:

```go
func runConfigLocation(cfgLoc *projectConfigLocation) (*projectConfigLocation, error) {
	if cfgLoc != nil {
		return cfgLoc, nil
	}
	loc, _, err := projectConfigForWrite("service")
	return loc, err
}
```

In `saveRunConfigWithPayloadKind`, after calling `runConfigLocation`, add:

```go
if loc == nil {
	return nil
}
```

Change `saveCronConfig` in `pkg/yeet/svc_cmd.go`:

```go
loc := cfgLoc
if loc == nil {
	var err error
	loc, _, err = projectConfigForWrite("cron")
	if err != nil {
		return err
	}
	if loc == nil {
		return nil
	}
}
```

Change `saveEnvFileConfig` in `pkg/yeet/run_changes.go` the same way:

```go
loc := cfgLoc
if loc == nil {
	var err error
	loc, _, err = projectConfigForWrite("env")
	if err != nil {
		return err
	}
	if loc == nil {
		return nil
	}
}
```

In `pkg/yeet/run_draft_test.go` or the existing web-run tests, add a regression that `yeet run --web` calls `projectConfigForWrite` before browser bootstrap when no config path is explicit, and that a declined workspace selection still launches with no config path and emits the no-save warning.

- [ ] **Step 6: Update service sync and host-set config lookup**

In `pkg/yeet/service_sync.go`, keep explicit `--config` behavior and let fallback-aware `existing` flow through:

```go
func serviceSyncConfig(existing *projectConfigLocation, configPath string) (*projectConfigLocation, error) {
	if strings.TrimSpace(configPath) != "" {
		return loadProjectConfigFromFile(configPath)
	}
	if existing == nil {
		return nil, fmt.Errorf("no %s found; run from a project directory or pass --config", projectConfigName)
	}
	return existing, nil
}
```

In `pkg/yeet/host_set.go`, keep `hostSetConfigLocation` using `loadProjectConfigFromCwd()` so it gets workspace fallback.

- [ ] **Step 7: Update upgrade/advisory project-host reads**

`pkg/yeet/upgrade_cmd.go` and `pkg/yeet/update_advisory.go` already call `loadProjectConfigFromCwd`; after Step 3 they get fallback behavior. Add regression tests in `pkg/yeet/upgrade_check_test.go` or existing status tests that seed a workspace and assert `upgradeKnownHosts` includes all workspace hosts.

Use this test body:

```go
func TestUpgradeKnownHostsUsesWorkspaceFallback(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if _, err := seedWorkspaceConfig(workspace, "yeet-lab"); err != nil {
		t.Fatalf("seedWorkspaceConfig: %v", err)
	}
	loc, err := loadProjectConfigFromFile(filepath.Join(workspace, projectConfigName))
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	loc.Config.addHost("yeet-cloud")
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab", Workspaces: []string{workspace}})
	defer restore()
	cwd, _ := os.Getwd()
	random := filepath.Join(tmp, "random")
	if err := os.Mkdir(random, 0o755); err != nil {
		t.Fatalf("Mkdir random: %v", err)
	}
	if err := os.Chdir(random); err != nil {
		t.Fatalf("Chdir random: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	cfgLoc, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	got := upgradeKnownHosts(cfgLoc, false)
	want := []string{"yeet-cloud", "yeet-lab"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upgradeKnownHosts = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 8: Run focused workspace tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestLoadProjectConfigFromCwd|TestSaveEnvFileConfigSkipsPersistence|TestServiceSyncErrors|TestUpgradeKnownHostsUsesWorkspaceFallback' -count=1
```

Expected: PASS. Update existing no-config service sync expectations only if workspace fallback changes them for the seeded test setup; do not weaken missing-config errors for true no-workspace cases.

- [ ] **Step 9: Commit workspace resolution and persistence behavior**

Run:

```bash
but diff
but commit codex/xdg-workspaces-design -m "pkg/yeet: resolve configured workspaces"
```

Expected: GitButler creates a commit for workspace resolution, prompt abstraction baseline, and tests.

---

### Task 5: Add Init Workspace Flags And Post-Install Setup

**Files:**
- Modify: `pkg/yeet/init.go`
- Create: `pkg/yeet/init_workspace.go`
- Modify: `pkg/yeet/init_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_test.go`

**Interfaces:**
- Consumes: `seedWorkspaceConfig`, `addWorkspacePath`, `saveClientConfig`, `setDefaultHost`, `activePrompter`.
- Produces:
  - `initFlagsParsed.Workspace string`
  - `initFlagsParsed.NoWorkspace bool`
  - `initOptions.workspace string`
  - `initOptions.noWorkspace bool`
  - `func finishInitWorkspaceSetup(ui *initUI, opts initOptions) error`
  - `func initWorkspaceDefault() string`

- [ ] **Step 1: Add failing init parse tests**

In `pkg/yeet/init_test.go`, add cases to `TestParseInitArgs`:

```go
{
	name:    "parses workspace flag",
	args:    []string{"--workspace", "~/yeet-services", "root@example.com"},
	wantPos: []string{"root@example.com"},
	wantWorkspace: "~/yeet-services",
},
{
	name:    "parses no workspace flag",
	args:    []string{"--no-workspace", "root@example.com"},
	wantPos: []string{"root@example.com"},
	wantNoWorkspace: true,
},
{
	name:    "rejects workspace and no workspace together",
	args:    []string{"--workspace", "~/yeet-services", "--no-workspace", "root@example.com"},
	wantErr: true,
},
```

Add fields to the test struct:

```go
wantWorkspace   string
wantNoWorkspace bool
```

Add assertions:

```go
if opts.workspace != tt.wantWorkspace {
	t.Fatalf("workspace = %q, want %q", opts.workspace, tt.wantWorkspace)
}
if opts.noWorkspace != tt.wantNoWorkspace {
	t.Fatalf("noWorkspace = %v, want %v", opts.noWorkspace, tt.wantNoWorkspace)
}
```

- [ ] **Step 2: Add failing post-install workspace tests**

Add to `pkg/yeet/init_test.go`:

```go
func TestFinishInitWorkspaceSetupExplicitWorkspaceCreatesSeedAndSavesConfig(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "services")
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "catch"})
	defer restore()
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	loadedPrefs.DefaultHost = "yeet-lab"

	ui := newInitUI(io.Discard, false, true, "yeet-lab", "root@example.com", catchServiceName)
	if err := finishInitWorkspaceSetup(ui, initOptions{workspace: workspace}); err != nil {
		t.Fatalf("finishInitWorkspaceSetup error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile yeet.toml: %v", err)
	}
	if !strings.Contains(string(raw), `hosts = ["yeet-lab"]`) {
		t.Fatalf("yeet.toml = %q, want host", string(raw))
	}
	configRaw, err := os.ReadFile(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if !strings.Contains(string(configRaw), `default_host = "yeet-lab"`) || !strings.Contains(string(configRaw), workspace) {
		t.Fatalf("config = %q, want host and workspace", string(configRaw))
	}
}

func TestFinishInitWorkspaceSetupNoWorkspaceSkipsPrompt(t *testing.T) {
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab"})
	defer restore()
	oldPrompt := activePrompter
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	t.Cleanup(func() { activePrompter = oldPrompt })
	ui := newInitUI(io.Discard, false, true, "yeet-lab", "root@example.com", catchServiceName)

	if err := finishInitWorkspaceSetup(ui, initOptions{noWorkspace: true}); err != nil {
		t.Fatalf("finishInitWorkspaceSetup error: %v", err)
	}
}
```

- [ ] **Step 3: Parse init workspace flags**

In `pkg/yeet/init.go`, extend structs:

```go
type initFlagsParsed struct {
	FromGithub     bool   `flag:"from-github"`
	Nightly        bool   `flag:"nightly"`
	InstallDocker  bool   `flag:"install-docker"`
	InstallVMTools bool   `flag:"install-vm-tools"`
	TSAuthKey      string `flag:"ts-auth-key"`
	TSClientSecret string `flag:"ts-client-secret"`
	DataDir        string `flag:"data-dir"`
	ServicesRoot   string `flag:"services-root"`
	ZFS            bool   `flag:"zfs"`
	Workspace      string `flag:"workspace"`
	NoWorkspace    bool   `flag:"no-workspace"`
}

type initOptions struct {
	fromGithub         bool
	nightly            bool
	installDocker      bool
	installVMTools     bool
	prepareVMLANBridge bool
	skipVMLANBridge    bool
	noWorkspace        bool
	workspace          string
	tsAuthKey          string
	tsClientSecret     string
	releaseVersion     string
	storage            initStorageOptions
}
```

In `initOptionsFromParsedFlags`, set `workspace` and `noWorkspace`. In `validateInitArgs`, add:

```go
if strings.TrimSpace(opts.workspace) != "" && opts.noWorkspace {
	return fmt.Errorf("--workspace and --no-workspace cannot be used together")
}
```

- [ ] **Step 4: Add `init_workspace.go`**

Create `pkg/yeet/init_workspace.go`:

```go
package yeet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func finishInitWorkspaceSetup(ui *initUI, opts initOptions) error {
	if opts.noWorkspace {
		printInitNextSteps(ui, "")
		return nil
	}
	workspace := strings.TrimSpace(opts.workspace)
	if workspace == "" {
		if !canPromptForWorkspace() {
			printInitNextSteps(ui, "")
			return nil
		}
		value, err := activePrompter.Input("Service workspace", initWorkspaceDefault())
		if err != nil {
			return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
		}
		workspace = value
	}
	loc, err := seedWorkspaceConfig(workspace, Host())
	if err != nil {
		return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
	}
	setDefaultHost(Host())
	if err := addWorkspacePath(loc.Dir); err != nil {
		return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
	}
	if err := saveClientConfig(); err != nil {
		return fmt.Errorf("catch installed successfully, but local workspace setup failed: %w", err)
	}
	printInitNextSteps(ui, loc.Dir)
	return nil
}

func initWorkspaceDefault() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("~", "yeet-services")
	}
	return filepath.Join(home, "yeet-services")
}

func printInitNextSteps(ui *initUI, workspace string) {
	if strings.TrimSpace(workspace) != "" {
		ui.Info(fmt.Sprintf("Next: cd %s", workspace))
		ui.Info("Try: yeet run -p 18080:80 hello nginx:alpine")
		return
	}
	ui.Info("Try: yeet run -p 18080:80 hello nginx:alpine")
	ui.Info("Without a workspace, this run will not save client-side service config. Set one with: yeet config --workspace PATH")
}
```

- [ ] **Step 5: Call workspace setup after remote install succeeds**

In `initCatch`, after `cleanupInitCatchBinaryFn(...)` and before `return nil`, add:

```go
if err := finishInitWorkspaceSetup(ui, opts); err != nil {
	return err
}
```

Make sure this runs only after `installInitCatchWithTailscaleRetry` succeeds.

- [ ] **Step 6: Update init help**

In `cmd/yeet/cli.go`, add workspace flags to init usage:

```go
Usage: "[--from-github] [--nightly] [--install-docker] [--install-vm-tools] [--workspace=PATH] [--no-workspace] [--data-dir=PATH_OR_DATASET] [--services-root=PATH_OR_DATASET] [--zfs] [--ts-client-secret=<secret>] [--ts-auth-key=<key>] [ROOT@MACHINE-HOST]",
```

Add examples:

```go
"yeet init --workspace ~/yeet-services root@<machine-host>",
"yeet init --no-workspace root@<machine-host>",
```

Add or update `cmd/yeet/cli_test.go` to assert init help includes `--workspace` and `--no-workspace`.

- [ ] **Step 7: Run focused init tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestParseInitArgs|TestFinishInitWorkspaceSetup|TestInitCatch' -count=1
mise exec -- go test ./cmd/yeet -run 'Test.*Init.*Help|TestBuildHelpConfigRegistersConfigAndNotPrefs' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit init workspace setup**

Run:

```bash
but diff
but commit codex/xdg-workspaces-design -m "pkg/yeet: seed workspaces during init"
```

Expected: GitButler creates a commit for init workspace setup and tests.

---

### Task 6: Convert Init Prompts To Charm `huh`

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `pkg/yeet/prompts.go`
- Create: `pkg/yeet/prompts_huh.go`
- Modify: `pkg/yeet/init.go`
- Modify: `pkg/yeet/init_storage.go`
- Modify: `pkg/yeet/init_test.go`

**Interfaces:**
- Consumes: `yeetPrompter` from Task 4.
- Produces: Charm-backed `huhPrompter`, all init prompts routed through `activePrompter`.

- [ ] **Step 1: Add Charm dependency**

Run:

```bash
mise exec -- go get github.com/charmbracelet/huh@latest
```

Expected: `go.mod` and `go.sum` update. If `go get` selects a version that fails the repo's Go version, pick the newest compatible `huh` version and document the exact selected version in the commit body.

- [ ] **Step 2: Implement the Charm prompter**

Create `pkg/yeet/prompts_huh.go`:

```go
package yeet

import (
	"fmt"

	"github.com/charmbracelet/huh"
)

type huhPrompter struct{}

func newDefaultPrompter() yeetPrompter {
	return huhPrompter{}
}

func (huhPrompter) Confirm(msg string, def bool) (bool, error) {
	value := def
	form := huh.NewConfirm().
		Title(msg).
		Value(&value)
	if err := form.Run(); err != nil {
		return false, err
	}
	return value, nil
}

func (huhPrompter) Input(msg string, def string) (string, error) {
	value := def
	form := huh.NewInput().
		Title(msg).
		Value(&value).
		Placeholder(def)
	if err := form.Run(); err != nil {
		return "", err
	}
	if value == "" {
		return def, nil
	}
	return value, nil
}

func (huhPrompter) Secret(msg string) (string, error) {
	var value string
	form := huh.NewInput().
		Title(msg).
		EchoMode(huh.EchoModePassword).
		Value(&value)
	if err := form.Run(); err != nil {
		return "", err
	}
	return value, nil
}

func (huhPrompter) SelectWorkspace(host string, paths []string, current string) (workspaceSelection, error) {
	options := make([]huh.Option[workspaceSelection], 0, len(paths)+2)
	for _, path := range paths {
		options = append(options, huh.NewOption(path, workspaceSelection{Choice: workspacePromptUseKnown, Path: path}))
	}
	options = append(options,
		huh.NewOption(fmt.Sprintf("%s (current directory)", current), workspaceSelection{Choice: workspacePromptUseCurrent, Path: current}),
		huh.NewOption("Run once without saving", workspaceSelection{Choice: workspacePromptRunOnce}),
	)
	var selected workspaceSelection
	form := huh.NewSelect[workspaceSelection]().
		Title(fmt.Sprintf("No workspace is associated with %s. Choose a workspace.", host)).
		Options(options...).
		Value(&selected)
	if err := form.Run(); err != nil {
		return workspaceSelection{}, err
	}
	return selected, nil
}
```

In `pkg/yeet/prompts.go`, change:

```go
var activePrompter yeetPrompter = plainPrompter{}
```

to:

```go
var activePrompter yeetPrompter = newDefaultPrompter()
```

Keep `plainPrompter` only if tests or non-TTY fallback need it.

- [ ] **Step 3: Convert Docker, VM, bridge, and Tailscale init prompts**

In `pkg/yeet/init.go`, replace direct `cmdutil.Confirm` or `confirmInitFn` calls in these functions:

- `prepareInitDockerInstall`
- `prepareInitVMToolsInstall`
- `applyInitVMLANBridgePlan`
- `retryInitTailscaleClientSecret`

Use:

```go
ok, err := activePrompter.Confirm("Docker is required on the remote host. Install Docker now?", false)
```

and:

```go
next, err := activePrompter.Secret("Tailscale OAuth client secret")
```

Remove `confirmInitFn` if no tests still need it; tests should stub `activePrompter`.

- [ ] **Step 4: Convert storage prompts**

In `pkg/yeet/init_storage.go`, replace `runInitStorageWizard`'s `bufio.Reader` prompt plumbing with active prompter calls. Keep the function signature so existing tests can still call it:

```go
func runInitStorageWizard(in io.Reader, out io.Writer, probe initStorageProbe) (initStorageOptions, error) {
	probe = normalizeInitStorageProbe(probe)
	if _, err := fmt.Fprintln(out, "Storage setup"); err != nil {
		return initStorageOptions{}, err
	}
	return runInitStorageWizardWithPrompter(activePrompter, probe)
}

func runInitStorageWizardWithPrompter(prompter yeetPrompter, probe initStorageProbe) (initStorageOptions, error) {
	storage := initStorageOptions{}
	defaultDataDir := filepath.Join(probe.Home, "yeet-data")
	useDefaultData, err := prompter.Confirm(fmt.Sprintf("Use %s for catch data?", defaultDataDir), true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useDefaultData {
		storage.DataDir = defaultDataDir
	} else if probe.ZFSAvailable {
		useZFS, err := prompter.Confirm("Use a ZFS dataset for catch data?", true)
		if err != nil {
			return initStorageOptions{}, err
		}
		if useZFS {
			storage.DataDir, err = prompter.Input("Catch data dataset", suggestedInitDataDataset(probe))
			if err != nil {
				return initStorageOptions{}, err
			}
			storage.DataDirZFS = true
		} else {
			storage.DataDir, err = prompter.Input("Catch data directory", defaultDataDir)
			if err != nil {
				return initStorageOptions{}, err
			}
		}
	} else {
		storage.DataDir, err = prompter.Input("Catch data directory", defaultDataDir)
		if err != nil {
			return initStorageOptions{}, err
		}
	}
	keepServicesUnderData, err := prompter.Confirm("Keep services under the catch data dir?", true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if keepServicesUnderData {
		return storage, nil
	}
	useZFS, err := prompter.Confirm("Use a ZFS dataset for services?", storage.DataDirZFS)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useZFS {
		storage.ServicesRoot, err = prompter.Input("Services dataset", suggestedInitServicesDataset(storage, probe))
		if err != nil {
			return initStorageOptions{}, err
		}
		storage.ServicesRootZFS = true
		return storage, nil
	}
	storage.ServicesRoot, err = prompter.Input("Services root", suggestedInitServicesRootPath(storage, probe))
	if err != nil {
		return initStorageOptions{}, err
	}
	return storage, nil
}
```

After this lands, remove now-unused helper functions `promptInitYesNo`, `promptInitValue`, and `readInitPromptLine` only if no tests still call them.

- [ ] **Step 5: Update init prompt tests to stub `activePrompter`**

Replace tests that stub `confirmInitFn` or provide input strings to storage wizard with `fakePrompter`. For example, change Docker prompt tests to:

```go
oldPrompt := activePrompter
activePrompter = fakePrompter{confirm: true}
t.Cleanup(func() { activePrompter = oldPrompt })
```

For storage wizard tests, call:

```go
got, err := runInitStorageWizardWithPrompter(fakePrompter{confirm: true}, initStorageProbe{Home: "/root"})
```

Add one test that asserts `activePrompter.Secret` is used for Tailscale retry:

```go
func TestRetryInitTailscaleClientSecretUsesPromptLayer(t *testing.T) {
	oldPrompt := activePrompter
	activePrompter = fakePrompter{secret: "tskey-client-new"}
	t.Cleanup(func() { activePrompter = oldPrompt })
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	secret, err := retryInitTailscaleClientSecret(ui, 1, errors.New("tailscale OAuth setup failed"))
	if err != nil {
		t.Fatalf("retryInitTailscaleClientSecret error: %v", err)
	}
	if secret != "tskey-client-new" {
		t.Fatalf("secret = %q, want prompted secret", secret)
	}
}
```

- [ ] **Step 6: Run focused prompt tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestPrepareInit|TestRunInitStorageWizard|TestRetryInitTailscaleClientSecret|TestFinishInitWorkspaceSetup|TestParseInitArgs' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit Charm prompt conversion**

Run:

```bash
but diff
but commit codex/xdg-workspaces-design -m "pkg/yeet: use charm prompts for init"
```

Expected: GitButler creates a separate commit for prompt dependency and init prompt conversion.

---

### Task 7: Update README And Website Docs

**Files:**
- Read: `website/AGENTS.md`
- Modify: `README.md`
- Modify: `website/docs/getting-started/quick-start.mdx`
- Modify: `website/docs/getting-started/host-setup.mdx`
- Modify: `website/docs/getting-started/service-workspace.mdx`
- Modify: `website/docs/concepts/configuration-and-prefs.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/changelog.mdx`

**Interfaces:**
- Consumes: final command behavior from Tasks 1-6.
- Produces: user-facing docs aligned with `yeet config`, XDG config, workspace fallback, and init workspace prompts.

- [ ] **Step 1: Read website guidance and confirm submodule state**

Run:

```bash
sed -n '1,220p' website/AGENTS.md
git -C website status --short --branch
```

Expected: website guidance is loaded and the submodule is on a usable branch. If `git -C website status` shows unrelated dirty files, stop and ask before editing website docs.

- [ ] **Step 2: Update README quick start**

In `README.md`, replace the old prefs save example:

```bash
yeet prefs --host=<catch-host> --save
```

with:

```bash
yeet config --host=<catch-host>
```

In the service workspace section, add one clear sentence:

```md
After setup, yeet can remember this workspace in `$XDG_CONFIG_HOME/yeet/config.toml`, so commands from other directories can still find the right `yeet.toml`.
```

Keep one `cd ~/yeet-services` example so relative payload paths remain natural.

- [ ] **Step 3: Update getting started docs**

In `website/docs/getting-started/quick-start.mdx`, change "Create a service workspace" to explain that interactive init offers `~/yeet-services`, and keep `cd ~/yeet-services` as the examples' working directory.

In `website/docs/getting-started/host-setup.mdx`, replace:

```bash
yeet prefs --host=morpheus-catch --save
```

with:

```bash
yeet config --host=morpheus-catch
```

Add examples for:

```bash
yeet init --workspace ~/yeet-services root@<machine-host>
yeet init --no-workspace root@<machine-host>
```

- [ ] **Step 4: Rewrite configuration docs**

In `website/docs/concepts/configuration-and-prefs.mdx`, update title/copy from "prefs" to "configuration". The schema block should be:

```toml
default_host = "morpheus-catch"
workspaces = ["/Users/alex/yeet-services"]
```

Commands block:

```bash
yeet config
yeet config --host=<catch-host>
yeet config --workspace ~/yeet-services
yeet config --add-workspace ~/lab-services
yeet config --remove-workspace ~/lab-services
yeet config --clear-workspaces
```

Explain that host ownership belongs in `yeet.toml`:

```toml
version = 1
hosts = ["morpheus-catch", "trinity-catch"]
```

- [ ] **Step 5: Update workspace and workflow docs**

In `website/docs/getting-started/service-workspace.mdx`, replace statements that say yeet reads/writes only the current directory with:

```md
Yeet first looks for `yeet.toml` in or above the current directory. If none is found, it uses the configured workspace that owns the target catch host.
```

In `website/docs/operations/workflows.mdx`, replace `yeet prefs --host=<catch-host> --save` with `yeet config --host=<catch-host>`.

- [ ] **Step 6: Update CLI manual**

In `website/docs/cli/yeet-cli.mdx`, replace the `prefs` command section with:

````mdx
### `config`

Show or update local yeet client config.

```bash
yeet config
yeet config --host=<catch-host>
yeet config --workspace ~/yeet-services
yeet config --add-workspace ~/lab-services
yeet config --remove-workspace ~/lab-services
yeet config --clear-workspaces
```
````

Add `--workspace` and `--no-workspace` to the `init` section.

- [ ] **Step 7: Add changelog bullet**

In `website/docs/changelog.mdx`, add the breaking-change bullet under the next release section:

```md
- Client preferences moved from `~/.yeet/prefs.json` to `$XDG_CONFIG_HOME/yeet/config.toml`, and `yeet config` replaces `yeet prefs`. Existing prefs migrate automatically on first run.
```

Do not mention old-file deletion in the changelog.

- [ ] **Step 8: Run docs grep checks**

Run:

```bash
rg -n "yeet prefs|prefs\\.json|~/.yeet/prefs|--save" README.md website/docs
```

Expected: no old user-facing `yeet prefs` instructions remain. If `prefs` appears as historical text, verify it is intentional and not an instruction.

- [ ] **Step 9: Commit and push website docs, then commit root docs**

First commit and push the website submodule changes from inside `website/`:

```bash
git -C website status --short
git -C website diff
git -C website add docs/getting-started/quick-start.mdx docs/getting-started/host-setup.mdx docs/getting-started/service-workspace.mdx docs/concepts/configuration-and-prefs.mdx docs/operations/workflows.mdx docs/cli/yeet-cli.mdx docs/changelog.mdx
git -C website commit -m "docs: document xdg workspaces"
git -C website push
```

Then verify and commit the root README and website gitlink:

Run:

```bash
git diff --submodule=log -- website
but diff
but commit codex/xdg-workspaces-design -m "docs: document xdg workspaces"
```

Expected: the website commit is advertised by its remote, `git diff --submodule=log -- website` shows only the intended website commit range, and GitButler creates a root docs commit for `README.md` plus the `website` gitlink.

---

### Task 8: Full Verification And Cleanup

**Files:**
- Verify: all touched files.

**Interfaces:**
- Consumes: all previous task outputs.
- Produces: verified feature branch ready for review or implementation landing.

- [ ] **Step 1: Run targeted package tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet ./pkg/cli -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Run release-grade quality gate**

Run:

```bash
mise run quality:goal
```

Expected: PASS. If this gate takes long enough to surface unrelated existing failures, record the exact failing tool and stop for user direction rather than lowering a goal or refreshing a baseline.

- [ ] **Step 5: Run isolated manual config smoke checks**

Build or run through the repo toolchain:

```bash
tmp="$(mktemp -d)"
workspace="$tmp/services"
mkdir -p "$workspace"
XDG_CONFIG_HOME="$tmp/xdg" mise exec -- go run ./cmd/yeet config --host Yeet-Lab --workspace "$workspace"
XDG_CONFIG_HOME="$tmp/xdg" mise exec -- go run ./cmd/yeet config
cat "$tmp/xdg/yeet/config.toml"
```

Expected:

```text
default_host = "yeet-lab"
workspaces = ["<absolute workspace path>"]
```

- [ ] **Step 6: Confirm removed command behavior**

Run:

```bash
XDG_CONFIG_HOME="$(mktemp -d)" mise exec -- go run ./cmd/yeet prefs
```

Expected: command fails as unknown or prints no registered `prefs` help. It must not write config.

- [ ] **Step 7: Inspect final diff**

Run:

```bash
but diff
```

Expected: no uncommitted changes. If changes remain, either commit them to the appropriate existing branch commit with `but commit codex/xdg-workspaces-design -m "<area>: <summary>"` or explain why they should remain uncommitted.

---

## Self-Review Checklist

- Spec coverage:
  - XDG config path and TOML schema: Task 1.
  - Legacy prefs migration and old-file deletion: Task 1.
  - `yeet config` replacement and `prefs` removal: Task 3.
  - Host-to-workspace ownership from `yeet.toml`: Task 2.
  - Local config precedence and workspace fallback: Task 4.
  - No silent arbitrary `yeet.toml` writes: Task 4.
  - Init `--workspace`, `--no-workspace`, seed file, and post-success setup: Task 5.
  - Charm prompt conversion for all init prompts: Task 6.
  - Docs and changelog: Task 7.
  - Verification: Task 8.
- Placeholder scan: this plan intentionally names files, functions, tests, commands, expected outcomes, and commit messages.
- Type consistency:
  - `clientConfig` owns `DefaultHost` and `Workspaces`.
  - `workspaceSelection` and `workspacePromptChoice` are produced by `yeetPrompter.SelectWorkspace`.
  - `projectConfigForWrite` returns `(*projectConfigLocation, bool, error)` and callers skip persistence when the location is nil.
  - `seedWorkspaceConfig` creates or updates a workspace `yeet.toml`.
  - `finishInitWorkspaceSetup` runs after remote init success.
