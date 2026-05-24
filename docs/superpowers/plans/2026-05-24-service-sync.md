# Service Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `yeet service sync` so local `yeet.toml` entries can pull the authoritative live service-root and ZFS identity from catch.

**Architecture:** Keep catch as the source of truth and make the client perform an explicit pull into an existing project config. Extend `catch.ServiceInfo` with stored root identity, route `service sync` locally, fetch per-service info over RPC, and update only the syncable TOML fields. Preserve the existing `service set` remote mutation path while warning when no local TOML entry was updated.

**Tech Stack:** Go, yargs command metadata, catch JSON-RPC, BurntSushi TOML, existing `pkg/yeet` project config helpers, website MDX docs.

---

## File Map

- `pkg/cli/cli.go`: Add `ServiceSyncFlags`, `serviceSyncFlagsParsed`, `ServiceSyncArgs`, command metadata, flag specs, and `ParseServiceSync`.
- `pkg/cli/cli_test.go`: Add parser coverage for `service sync`.
- `cmd/yeet/cli_bridge.go`: Mark `service sync` as local so the CLI does not strip the service name or bridge the command to catch.
- `cmd/yeet/cli_bridge_test.go`: Add routing coverage proving `service sync` stays local.
- `pkg/catchrpc/types.go`: Extend `ServicePaths` with `EffectiveRoot`, `ServiceRoot`, and `ServiceRootZFS` while leaving `Root` as the backward-compatible effective root.
- `pkg/catch/service_info.go`: Populate the new service path fields from the DB service view.
- `pkg/catch/service_info_test.go`: Cover filesystem, ZFS, and default-root service-info fields.
- `pkg/yeet/project_config.go`: Add explicit config file loading and a service-root update helper that can clear `service_root` and `service_root_zfs`.
- `pkg/yeet/project_config_test.go`: Cover config loading and root clearing.
- `pkg/yeet/service_sync.go`: New local sync implementation and small test seams.
- `pkg/yeet/service_sync_test.go`: Unit tests for named sync, `--all`, skips, missing config, missing entries, root clearing, and ZFS dataset writing.
- `pkg/yeet/svc_cmd.go`: Route `service sync` locally and print the `service set` drift warning when TOML is not updated.
- `pkg/yeet/svc_cmd_branch_test.go`: Extend `service set` tests for the new warning and the no-warning path.
- `README.md`: Document `yeet service sync`.
- `website/docs/cli/yeet-cli.mdx`: Add command reference.
- `website/docs/operations/workflows.mdx`: Add the drift-repair workflow.
- `website/docs/concepts/data-layout.mdx`: Explain catch DB vs local TOML.
- `.codex/skills/yeet-cli/references/yeet-help-llm.md`: Refresh command-help reference for `service sync`.

## Task 1: CLI Metadata, Parsing, And Local Routing

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge.go`
- Modify: `cmd/yeet/cli_bridge_test.go`

- [ ] **Step 1: Write failing parser tests**

Add this test in `pkg/cli/cli_test.go` after `TestParseServiceSetFlags`:

```go
func TestParseServiceSyncFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    ServiceSyncFlags
		wantOut []string
		wantErr string
	}{
		{
			name:    "single service",
			args:    []string{"sonarr"},
			want:    ServiceSyncFlags{},
			wantOut: []string{"sonarr"},
		},
		{
			name:    "all",
			args:    []string{"--all"},
			want:    ServiceSyncFlags{All: true},
			wantOut: nil,
		},
		{
			name:    "config before service",
			args:    []string{"--config", "./yeet.toml", "sonarr"},
			want:    ServiceSyncFlags{Config: "./yeet.toml"},
			wantOut: []string{"sonarr"},
		},
		{
			name:    "config equals",
			args:    []string{"sonarr", "--config=./yeet.toml"},
			want:    ServiceSyncFlags{Config: "./yeet.toml"},
			wantOut: []string{"sonarr"},
		},
		{name: "all plus service", args: []string{"--all", "sonarr"}, wantErr: "--all cannot be combined with a service name"},
		{name: "missing service and all", args: nil, wantErr: "service sync requires a service name or --all"},
		{name: "too many services", args: []string{"sonarr", "radarr"}, wantErr: "service sync accepts one service name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, out, err := ParseServiceSync(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseServiceSync error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseServiceSync error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("args = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}
```

- [ ] **Step 2: Write failing bridge tests**

Add `service sync` to the local-command table test in `cmd/yeet/cli_bridge_test.go` by extending `TestBridgeServiceArgsDoesNotBridgeLocalOrEmptyCommands`:

```go
tests := [][]string{
	nil,
	{"copy", "src", "dst"},
	{"docker", "push", "svc-a", "image:tag"},
	{"service", "sync", "svc-a"},
	{"service", "sync", "--all"},
	{"service", "sync", "--config", "./yeet.toml", "svc-a"},
	{"docker"},
	{"unknown", "svc-a"},
	{"env", "bogus", "svc-a"},
}
```

Add a direct regression test after `TestBridgeServiceArgsServiceSet`:

```go
func TestBridgeServiceArgsServiceSyncDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"service", "sync", "svc-a", "--config", "./yeet.toml"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok || service != "" || host != "" || bridged != nil {
		t.Fatalf("bridgeServiceArgs service sync = service=%q host=%q bridged=%v ok=%v, want no bridge", service, host, bridged, ok)
	}
}
```

- [ ] **Step 3: Run parser and bridge tests to verify they fail**

Run:

```bash
go test ./pkg/cli ./cmd/yeet -run 'TestParseServiceSyncFlags|TestBridgeServiceArgsDoesNotBridgeLocalOrEmptyCommands|TestBridgeServiceArgsServiceSyncDoesNotBridge'
```

Expected: `pkg/cli` fails because `ServiceSyncFlags` and `ParseServiceSync` do not exist, and `cmd/yeet` fails until `service sync` is listed as local.

- [ ] **Step 4: Implement parser and metadata**

In `pkg/cli/cli.go`, add the public flag type near `ServiceSetFlags`:

```go
type ServiceSyncFlags struct {
	All    bool
	Config string
}
```

Add the parsed flag struct near `serviceSetFlagsParsed`:

```go
type serviceSyncFlagsParsed struct {
	All    bool   `flag:"all"`
	Config string `flag:"config"`
}
```

Add the optional positional args type near `ServiceArgs`:

```go
type ServiceSyncArgs struct {
	Service ServiceName `pos:"0?" help:"Service name"`
}
```

Add `sync` to `remoteGroupInfos["service"].Commands`:

```go
"sync": {
	Name:        "sync",
	Description: "Sync local yeet.toml service settings from catch",
	Usage:       "service sync <svc> [--config=PATH] | service sync --all [--config=PATH]",
	Examples: []string{
		"yeet service sync <svc>",
		"yeet service sync --all",
		"yeet service sync <svc> --config ~/yeet-services/yeet.toml",
	},
	ArgsSchema: ServiceSyncArgs{},
},
```

Add `sync` to `remoteGroupFlagSpecs["service"]`:

```go
"sync": flagSpecsFromStruct(serviceSyncFlagsParsed{}),
```

Add this function after `ParseServiceSet`:

```go
func ParseServiceSync(args []string) (ServiceSyncFlags, []string, error) {
	specs := remoteGroupFlagSpecs["service"]["sync"]
	parseArgs, extraArgs := splitArgsForParsing(args, specs)
	parsed, err := parseFlags[serviceSyncFlagsParsed](parseArgs)
	if err != nil {
		return ServiceSyncFlags{}, nil, err
	}
	flags := ServiceSyncFlags{
		All:    parsed.Flags.All,
		Config: strings.TrimSpace(parsed.Flags.Config),
	}
	argsOut := append(parsed.Args, extraArgs...)
	if flags.All && len(argsOut) > 0 {
		return ServiceSyncFlags{}, nil, fmt.Errorf("--all cannot be combined with a service name")
	}
	if !flags.All && len(argsOut) == 0 {
		return ServiceSyncFlags{}, nil, fmt.Errorf("service sync requires a service name or --all")
	}
	if len(argsOut) > 1 {
		return ServiceSyncFlags{}, nil, fmt.Errorf("service sync accepts one service name")
	}
	return flags, argsOut, nil
}
```

- [ ] **Step 5: Mark `service sync` as local**

In `cmd/yeet/cli_bridge.go`, update `localGroupCommands`:

```go
var localGroupCommands = map[string]map[string]struct{}{
	"docker": {
		"push": {},
	},
	"service": {
		"sync": {},
	},
}
```

- [ ] **Step 6: Run tests and format**

Run:

```bash
gofmt -w pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge.go cmd/yeet/cli_bridge_test.go
go test ./pkg/cli ./cmd/yeet -run 'TestParseServiceSyncFlags|TestBridgeServiceArgsDoesNotBridgeLocalOrEmptyCommands|TestBridgeServiceArgsServiceSyncDoesNotBridge'
```

Expected: tests pass.

- [ ] **Step 7: Commit**

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge.go cmd/yeet/cli_bridge_test.go
git commit -m "cli: add service sync command metadata"
```

## Task 2: Service Info Root Identity

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`

- [ ] **Step 1: Write failing service-info tests**

Replace `TestServiceInfoPathsRootUsesDBServiceRoot` in `pkg/catch/service_info_test.go` with:

```go
func TestServiceInfoPathsIncludeRootIdentity(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "custom-info")
	zfsRoot := filepath.Join(t.TempDir(), "zfs-info")
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"fs-info": {
				Name:        "fs-info",
				ServiceType: db.ServiceTypeSystemd,
				ServiceRoot: customRoot,
			},
			"zfs-info": {
				Name:           "zfs-info",
				ServiceType:    db.ServiceTypeSystemd,
				ServiceRoot:    zfsRoot,
				ServiceRootZFS: "tank/apps/zfs-info",
			},
			"default-info": {
				Name:        "default-info",
				ServiceType: db.ServiceTypeSystemd,
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	fsResp, err := server.serviceInfo("fs-info")
	if err != nil {
		t.Fatalf("serviceInfo fs-info: %v", err)
	}
	if fsResp.Info.Paths.Root != customRoot || fsResp.Info.Paths.EffectiveRoot != customRoot {
		t.Fatalf("filesystem effective roots = %#v, want %q", fsResp.Info.Paths, customRoot)
	}
	if fsResp.Info.Paths.ServiceRoot != customRoot {
		t.Fatalf("filesystem ServiceRoot = %q, want %q", fsResp.Info.Paths.ServiceRoot, customRoot)
	}
	if fsResp.Info.Paths.ServiceRootZFS != "" {
		t.Fatalf("filesystem ServiceRootZFS = %q, want empty", fsResp.Info.Paths.ServiceRootZFS)
	}

	zfsResp, err := server.serviceInfo("zfs-info")
	if err != nil {
		t.Fatalf("serviceInfo zfs-info: %v", err)
	}
	if zfsResp.Info.Paths.Root != zfsRoot || zfsResp.Info.Paths.EffectiveRoot != zfsRoot {
		t.Fatalf("zfs effective roots = %#v, want %q", zfsResp.Info.Paths, zfsRoot)
	}
	if zfsResp.Info.Paths.ServiceRoot != zfsRoot {
		t.Fatalf("zfs ServiceRoot = %q, want %q", zfsResp.Info.Paths.ServiceRoot, zfsRoot)
	}
	if zfsResp.Info.Paths.ServiceRootZFS != "tank/apps/zfs-info" {
		t.Fatalf("zfs ServiceRootZFS = %q, want tank/apps/zfs-info", zfsResp.Info.Paths.ServiceRootZFS)
	}

	defaultResp, err := server.serviceInfo("default-info")
	if err != nil {
		t.Fatalf("serviceInfo default-info: %v", err)
	}
	wantDefault := server.defaultServiceRootDir("default-info")
	if defaultResp.Info.Paths.Root != wantDefault || defaultResp.Info.Paths.EffectiveRoot != wantDefault {
		t.Fatalf("default effective roots = %#v, want %q", defaultResp.Info.Paths, wantDefault)
	}
	if defaultResp.Info.Paths.ServiceRoot != "" || defaultResp.Info.Paths.ServiceRootZFS != "" {
		t.Fatalf("default stored roots = %#v, want empty stored root fields", defaultResp.Info.Paths)
	}
}
```

- [ ] **Step 2: Run service-info tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestServiceInfoPathsIncludeRootIdentity'
```

Expected: compile fails because `EffectiveRoot`, `ServiceRoot`, and `ServiceRootZFS` are not defined on `catchrpc.ServicePaths`.

- [ ] **Step 3: Extend RPC types**

In `pkg/catchrpc/types.go`, replace `ServicePaths` with:

```go
type ServicePaths struct {
	// Root is the effective filesystem root. It is kept for existing clients.
	Root           string `json:"root,omitempty"`
	EffectiveRoot  string `json:"effectiveRoot,omitempty"`
	ServiceRoot    string `json:"serviceRoot,omitempty"`
	ServiceRootZFS string `json:"serviceRootZfs,omitempty"`
}
```

- [ ] **Step 4: Populate service-info fields**

In `pkg/catch/service_info.go`, change the paths assignment inside `serviceInfo`:

```go
effectiveRoot := s.serviceRootFromView(sv)
info := catchrpc.ServiceInfo{
	Name:             sn,
	ServiceType:      string(sv.ServiceType()),
	DataType:         string(ServiceDataTypeForService(sv)),
	Generation:       sv.Generation(),
	LatestGeneration: sv.LatestGeneration(),
	Paths: catchrpc.ServicePaths{
		Root:           effectiveRoot,
		EffectiveRoot:  effectiveRoot,
		ServiceRoot:    sv.ServiceRoot(),
		ServiceRootZFS: sv.ServiceRootZFS(),
	},
}
```

- [ ] **Step 5: Run service-info tests and format**

Run:

```bash
gofmt -w pkg/catchrpc/types.go pkg/catch/service_info.go pkg/catch/service_info_test.go
go test ./pkg/catch ./pkg/catchrpc -run 'TestServiceInfoPathsIncludeRootIdentity|TestServiceInfoCallsRPC'
```

Expected: tests pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/catchrpc/types.go pkg/catch/service_info.go pkg/catch/service_info_test.go
git commit -m "catchrpc: expose service root identity"
```

## Task 3: Project Config Helpers For Sync

**Files:**
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/project_config_test.go`

- [ ] **Step 1: Write failing project config tests**

Add these tests in `pkg/yeet/project_config_test.go` near the other config load/save tests:

```go
func TestLoadProjectConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.toml")
	loc := &projectConfigLocation{
		Path: path,
		Dir:  dir,
		Config: &ProjectConfig{
			Version: projectConfigVersion,
			Services: []ServiceEntry{{
				Name:    "sonarr",
				Host:    "yeet-pve1",
				Type:    serviceTypeRun,
				Payload: "compose.yml",
			}},
		},
	}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}

	loaded, err := loadProjectConfigFromFile(path)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	if loaded.Path != path {
		t.Fatalf("Path = %q, want %q", loaded.Path, path)
	}
	if loaded.Dir != dir {
		t.Fatalf("Dir = %q, want %q", loaded.Dir, dir)
	}
	if _, ok := loaded.Config.ServiceEntry("sonarr", "yeet-pve1"); !ok {
		t.Fatalf("loaded config missing sonarr entry: %#v", loaded.Config)
	}
}

func TestLoadProjectConfigFromFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadProjectConfigFromFile(filepath.Join(dir, "missing.toml")); err == nil || !strings.Contains(err.Error(), "no yeet.toml found at") {
		t.Fatalf("missing config error = %v, want no yeet.toml found", err)
	}
	if _, err := loadProjectConfigFromFile(dir); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory config error = %v, want directory rejection", err)
	}
}

func TestProjectConfigSetServiceRootForEntryCanClear(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:           "sonarr",
		Host:           "yeet-pve1",
		Type:           serviceTypeRun,
		Payload:        "compose.yml",
		ServiceRoot:    "flash/yeet/sonarr",
		ServiceRootZFS: true,
	})

	if !cfg.SetServiceRootForEntry("sonarr", "yeet-pve1", "/srv/apps/sonarr", false) {
		t.Fatalf("SetServiceRootForEntry filesystem = false, want true")
	}
	entry, _ := cfg.ServiceEntry("sonarr", "yeet-pve1")
	if entry.ServiceRoot != "/srv/apps/sonarr" || entry.ServiceRootZFS {
		t.Fatalf("filesystem entry = %#v, want root without zfs", entry)
	}

	if !cfg.SetServiceRootForEntry("sonarr", "yeet-pve1", "", false) {
		t.Fatalf("SetServiceRootForEntry clear = false, want true")
	}
	entry, _ = cfg.ServiceEntry("sonarr", "yeet-pve1")
	if entry.ServiceRoot != "" || entry.ServiceRootZFS {
		t.Fatalf("cleared entry = %#v, want empty root and false zfs", entry)
	}

	if cfg.SetServiceRootForEntry("missing", "yeet-pve1", "/srv/apps/missing", false) {
		t.Fatalf("SetServiceRootForEntry missing = true, want false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./pkg/yeet -run 'TestLoadProjectConfigFromFile|TestProjectConfigSetServiceRootForEntryCanClear'
```

Expected: compile fails because `loadProjectConfigFromFile` and `SetServiceRootForEntry` do not exist.

- [ ] **Step 3: Add explicit config loading**

In `pkg/yeet/project_config.go`, add this function after `loadProjectConfigFromDir`:

```go
func loadProjectConfigFromFile(path string) (*projectConfigLocation, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("no yeet.toml found; run from a project directory or pass --config")
	}
	expanded, err := expandUserConfigPath(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no yeet.toml found at %s", abs)
		}
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory; pass the path to yeet.toml", abs)
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
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}
```

- [ ] **Step 4: Add root update helper that can clear fields**

In `pkg/yeet/project_config.go`, add this method after `SetServiceEntry`:

```go
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
```

- [ ] **Step 5: Run tests and format**

Run:

```bash
gofmt -w pkg/yeet/project_config.go pkg/yeet/project_config_test.go
go test ./pkg/yeet -run 'TestLoadProjectConfigFromFile|TestProjectConfigSetServiceRootForEntryCanClear'
```

Expected: tests pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/yeet/project_config.go pkg/yeet/project_config_test.go
git commit -m "yeet: add service config sync helpers"
```

## Task 4: Implement `yeet service sync`

**Files:**
- Create: `pkg/yeet/service_sync.go`
- Create: `pkg/yeet/service_sync_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Write failing sync implementation tests**

Create `pkg/yeet/service_sync_test.go`:

```go
package yeet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestServiceSyncNamedWritesZFSDatasetToConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	loadedPrefs.DefaultHost = "yeet-pve1"
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "yeet-pve1", Type: serviceTypeRun, Payload: "compose.yml"})
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "yeet-pve1" || service != "sonarr" {
			t.Fatalf("fetch host=%q service=%q, want yeet-pve1 sonarr", host, service)
		}
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{
				Root:           "/flash/yeet/sonarr",
				EffectiveRoot:  "/flash/yeet/sonarr",
				ServiceRoot:    "/flash/yeet/sonarr",
				ServiceRootZFS: "flash/yeet/sonarr",
			}},
		}, nil
	}

	out, err := captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "sync", "sonarr"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd service sync: %v", err)
	}
	if !strings.Contains(out, "Updated sonarr@yeet-pve1") || !strings.Contains(out, `service_root = "flash/yeet/sonarr"`) || !strings.Contains(out, "service_root_zfs = true") {
		t.Fatalf("output = %q, want updated zfs fields", out)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("sonarr", "yeet-pve1")
	if !ok || entry.ServiceRoot != "flash/yeet/sonarr" || !entry.ServiceRootZFS {
		t.Fatalf("entry = %#v, want zfs dataset root", entry)
	}
}

func TestServiceSyncNamedUsesExplicitConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	cwd := useTempSvcCwd(t)
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, projectConfigName)
	loc := &projectConfigLocation{Path: configPath, Dir: configDir, Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{Name: "radarr", Host: "yeet-pve1", Type: serviceTypeRun, Payload: "compose.yml"})
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	loadedPrefs.DefaultHost = "yeet-pve1"
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info:  catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{Root: "/srv/apps/radarr", EffectiveRoot: "/srv/apps/radarr", ServiceRoot: "/srv/apps/radarr"}},
		}, nil
	}

	if err := HandleSvcCmd([]string{"service", "sync", "radarr", "--config", configPath}); err != nil {
		t.Fatalf("HandleSvcCmd explicit config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, projectConfigName)); !os.IsNotExist(err) {
		t.Fatalf("sync with --config should not create cwd yeet.toml, stat err=%v", err)
	}
	loaded, err := loadProjectConfigFromFile(configPath)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	entry, _ := loaded.Config.ServiceEntry("radarr", "yeet-pve1")
	if entry.ServiceRoot != "/srv/apps/radarr" || entry.ServiceRootZFS {
		t.Fatalf("entry = %#v, want filesystem root", entry)
	}
}

func TestServiceSyncClearsDefaultRoot(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	loadedPrefs.DefaultHost = "yeet-pve1"
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:           "sonarr",
		Host:           "yeet-pve1",
		Type:           serviceTypeRun,
		Payload:        "compose.yml",
		ServiceRoot:    "flash/yeet/sonarr",
		ServiceRootZFS: true,
	})
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info:  catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{Root: "/root/data/services/sonarr", EffectiveRoot: "/root/data/services/sonarr"}},
		}, nil
	}

	if err := HandleSvcCmd([]string{"service", "sync", "sonarr"}); err != nil {
		t.Fatalf("HandleSvcCmd service sync clear: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if strings.Contains(string(raw), "service_root") || strings.Contains(string(raw), "service_root_zfs") {
		t.Fatalf("config = %q, want service root fields omitted", string(raw))
	}
}

func TestServiceSyncAllUpdatesAndSkipsMissing(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	loadedPrefs.DefaultHost = "yeet-pve1"
	writeSvcBranchConfig(t, tmp,
		ServiceEntry{Name: "sonarr", Host: "yeet-pve1", Type: serviceTypeRun, Payload: "sonarr.yml"},
		ServiceEntry{Name: "radarr", Host: "yeet-pve1", Type: serviceTypeRun, Payload: "radarr.yml"},
		ServiceEntry{Name: "lidarr", Host: "other-host", Type: serviceTypeRun, Payload: "lidarr.yml"},
	)
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		switch service {
		case "sonarr":
			return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{ServiceRoot: "/srv/apps/sonarr"}}}, nil
		case "radarr":
			return catchrpc.ServiceInfoResponse{Found: false, Message: "service not found"}, nil
		default:
			t.Fatalf("unexpected fetch for %s@%s", service, host)
			return catchrpc.ServiceInfoResponse{}, nil
		}
	}

	out, err := captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "sync", "--all"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd service sync --all: %v", err)
	}
	if !strings.Contains(out, "Updated sonarr@yeet-pve1") || !strings.Contains(out, "Skipped radarr@yeet-pve1: service not found on catch") || !strings.Contains(out, "1 updated, 1 skipped") {
		t.Fatalf("output = %q, want update and skip summary", out)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	sonarr, _ := loaded.Config.ServiceEntry("sonarr", "yeet-pve1")
	if sonarr.ServiceRoot != "/srv/apps/sonarr" || sonarr.ServiceRootZFS {
		t.Fatalf("sonarr = %#v, want filesystem root", sonarr)
	}
	if _, ok := loaded.Config.ServiceEntry("lidarr", "other-host"); !ok {
		t.Fatalf("other-host entry should be untouched")
	}
}

func TestServiceSyncErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		args    []string
		wantErr string
	}{
		{
			name:    "no config",
			setup:   func(t *testing.T) { useTempSvcCwd(t); loadedPrefs.DefaultHost = "yeet-pve1" },
			args:    []string{"service", "sync", "sonarr"},
			wantErr: "no yeet.toml found; run from a project directory or pass --config",
		},
		{
			name: "no matching entry",
			setup: func(t *testing.T) {
				tmp := useTempSvcCwd(t)
				loadedPrefs.DefaultHost = "yeet-pve1"
				writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "radarr", Host: "yeet-pve1", Type: serviceTypeRun, Payload: "compose.yml"})
			},
			args:    []string{"service", "sync", "sonarr"},
			wantErr: "no yeet.toml entry for sonarr@yeet-pve1",
		},
		{
			name: "remote missing",
			setup: func(t *testing.T) {
				tmp := useTempSvcCwd(t)
				loadedPrefs.DefaultHost = "yeet-pve1"
				writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "yeet-pve1", Type: serviceTypeRun, Payload: "compose.yml"})
				fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
					return catchrpc.ServiceInfoResponse{Found: false, Message: "service not found"}, nil
				}
			},
			args:    []string{"service", "sync", "sonarr"},
			wantErr: `service "sonarr" not found on yeet-pve1`,
		},
		{
			name: "fetch error",
			setup: func(t *testing.T) {
				tmp := useTempSvcCwd(t)
				loadedPrefs.DefaultHost = "yeet-pve1"
				writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "yeet-pve1", Type: serviceTypeRun, Payload: "compose.yml"})
				fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
					return catchrpc.ServiceInfoResponse{}, errors.New("rpc unavailable")
				}
			},
			args:    []string{"service", "sync", "sonarr"},
			wantErr: "rpc unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveSvcCommandGlobals(t)
			tt.setup(t)
			err := HandleSvcCmd(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("HandleSvcCmd error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Extend global test preservation**

In `pkg/yeet/svc_cmd_branch_test.go`, add the new fetch seam to `preserveSvcCommandGlobals`:

```go
oldFetchServiceInfoForSync := fetchServiceInfoForSyncFn
```

and in the cleanup function:

```go
fetchServiceInfoForSyncFn = oldFetchServiceInfoForSync
```

- [ ] **Step 3: Run sync tests to verify they fail**

Run:

```bash
go test ./pkg/yeet -run 'TestServiceSync'
```

Expected: compile fails because `fetchServiceInfoForSyncFn`, `service_sync.go`, and the `service sync` handler do not exist.

- [ ] **Step 4: Add the sync implementation**

Create `pkg/yeet/service_sync.go`:

```go
package yeet

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
)

var fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
	return newRPCClient(host).ServiceInfo(ctx, service)
}

type serviceSyncTarget struct {
	Service string
	Host    string
}

type serviceSyncResult struct {
	Target serviceSyncTarget
	Root   string
	ZFS    bool
	Skip   string
}

func handleServiceSync(ctx context.Context, req svcCommandRequest) error {
	flags, remaining, err := cli.ParseServiceSync(req.Command.Args[1:])
	if err != nil {
		return err
	}
	cfgLoc, err := serviceSyncConfig(req.Config, flags.Config)
	if err != nil {
		return err
	}
	targets, err := serviceSyncTargets(cfgLoc, req, flags, remaining)
	if err != nil {
		return err
	}
	results := make([]serviceSyncResult, 0, len(targets))
	updated := 0
	skipped := 0
	for _, target := range targets {
		result, ok, err := syncOneServiceRoot(ctx, cfgLoc, target)
		if err != nil {
			return err
		}
		results = append(results, result)
		if ok {
			updated++
		} else {
			skipped++
		}
	}
	if updated == 0 {
		return serviceSyncNoUpdatesError(flags.All, results)
	}
	if err := saveProjectConfig(cfgLoc); err != nil {
		return err
	}
	return renderServiceSyncResults(os.Stdout, cfgLoc.Path, flags.All, results, updated, skipped)
}

func serviceSyncConfig(existing *projectConfigLocation, configPath string) (*projectConfigLocation, error) {
	if strings.TrimSpace(configPath) != "" {
		return loadProjectConfigFromFile(configPath)
	}
	if existing == nil || existing.Config == nil {
		return nil, fmt.Errorf("no yeet.toml found; run from a project directory or pass --config")
	}
	return existing, nil
}

func serviceSyncTargets(cfgLoc *projectConfigLocation, req svcCommandRequest, flags cli.ServiceSyncFlags, remaining []string) ([]serviceSyncTarget, error) {
	if flags.All {
		host := serviceConfigHost(req.HostOverride)
		if req.HostOverrideSet {
			host = req.HostOverride
		}
		targets := make([]serviceSyncTarget, 0)
		for _, entry := range cfgLoc.Config.Services {
			if entry.Host == host {
				targets = append(targets, serviceSyncTarget{Service: entry.Name, Host: entry.Host})
			}
		}
		sort.Slice(targets, func(i, j int) bool {
			if targets[i].Host == targets[j].Host {
				return targets[i].Service < targets[j].Service
			}
			return targets[i].Host < targets[j].Host
		})
		if len(targets) == 0 {
			return nil, fmt.Errorf("no yeet.toml entries for host %s", host)
		}
		return targets, nil
	}
	service := strings.TrimSpace(req.Service)
	if len(remaining) > 0 {
		service = strings.TrimSpace(remaining[0])
	}
	if service == "" {
		return nil, fmt.Errorf("service sync requires a service name or --all")
	}
	host, err := serviceSyncHost(cfgLoc.Config, service, req.HostOverride, req.HostOverrideSet)
	if err != nil {
		return nil, err
	}
	if _, ok := cfgLoc.Config.ServiceEntry(service, host); !ok {
		return nil, fmt.Errorf("no yeet.toml entry for %s@%s", service, host)
	}
	return []serviceSyncTarget{{Service: service, Host: host}}, nil
}

func serviceSyncHost(cfg *ProjectConfig, service, hostOverride string, hostOverrideSet bool) (string, error) {
	host := strings.TrimSpace(hostOverride)
	if hostOverrideSet && host != "" {
		return host, nil
	}
	resolved, err := resolveServiceHost(cfg, service)
	if err != nil {
		return "", err
	}
	if resolved != "" {
		SetHost(resolved)
		return resolved, nil
	}
	host = Host()
	if host == "" {
		return "", fmt.Errorf("no yeet.toml entry for %s@%s", service, host)
	}
	return host, nil
}

func syncOneServiceRoot(ctx context.Context, cfgLoc *projectConfigLocation, target serviceSyncTarget) (serviceSyncResult, bool, error) {
	resp, err := fetchServiceInfoForSyncFn(ctx, target.Host, target.Service)
	if err != nil {
		return serviceSyncResult{}, false, err
	}
	result := serviceSyncResult{Target: target}
	if !resp.Found {
		result.Skip = "service not found on catch"
		return result, false, nil
	}
	root, zfs := serviceRootForLocalConfig(resp.Info)
	result.Root = root
	result.ZFS = zfs
	if !cfgLoc.Config.SetServiceRootForEntry(target.Service, target.Host, root, zfs) {
		return serviceSyncResult{}, false, fmt.Errorf("no yeet.toml entry for %s@%s", target.Service, target.Host)
	}
	return result, true, nil
}

func serviceRootForLocalConfig(info catchrpc.ServiceInfo) (string, bool) {
	if strings.TrimSpace(info.Paths.ServiceRootZFS) != "" {
		return strings.TrimSpace(info.Paths.ServiceRootZFS), true
	}
	if strings.TrimSpace(info.Paths.ServiceRoot) != "" {
		return strings.TrimSpace(info.Paths.ServiceRoot), false
	}
	return "", false
}

func serviceSyncNoUpdatesError(all bool, results []serviceSyncResult) error {
	if !all && len(results) == 1 && results[0].Skip != "" {
		return fmt.Errorf("service %q not found on %s", results[0].Target.Service, results[0].Target.Host)
	}
	if all {
		return fmt.Errorf("no services synced")
	}
	return fmt.Errorf("service sync made no changes")
}

func renderServiceSyncResults(w interface{ Write([]byte) (int, error) }, configPath string, all bool, results []serviceSyncResult, updated, skipped int) error {
	for _, result := range results {
		target := result.Target.Service + "@" + result.Target.Host
		if result.Skip != "" {
			if _, err := fmt.Fprintf(w, "Skipped %s: %s\n", target, result.Skip); err != nil {
				return err
			}
			continue
		}
		if all {
			if _, err := fmt.Fprintf(w, "Updated %s\n", target); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "Updated %s in %s\n", target, configPath); err != nil {
			return err
		}
		if result.Root == "" {
			if _, err := fmt.Fprintln(w, "  service_root = <default>"); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(w, "  service_root = %q\n", result.Root); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  service_root_zfs = %t\n", result.ZFS); err != nil {
			return err
		}
	}
	if all {
		if _, err := fmt.Fprintf(w, "%d updated, %d skipped\n", updated, skipped); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Route `service sync` locally**

In `pkg/yeet/svc_cmd.go`, change `handleSvcService`:

```go
func handleSvcService(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) == 0 {
		return handleSvcRemote(ctx, req)
	}
	switch req.Command.Args[0] {
	case "sync":
		return handleServiceSync(ctx, req)
	case "set":
		return handleServiceSet(ctx, req)
	default:
		return handleSvcRemote(ctx, req)
	}
}
```

Add `handleServiceSet` below it using the current `set` body:

```go
func handleServiceSet(ctx context.Context, req svcCommandRequest) error {
	flags, _, err := cli.ParseServiceSet(req.Command.Args[1:])
	if err != nil {
		return err
	}
	tty := !flags.Copy && !flags.Empty && isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
	if err := execRemoteFn(ctx, req.Service, req.Command.RawArgs, nil, tty); err != nil {
		return err
	}
	updated, err := saveServiceSetConfig(req.Config, req.HostOverride, flags.ServiceRoot, flags.ZFS)
	if err != nil {
		return err
	}
	if !updated {
		return printServiceSetSyncHint(os.Stdout, req.Service)
	}
	return nil
}
```

Change `saveServiceSetConfig` signature and body:

```go
func saveServiceSetConfig(cfgLoc *projectConfigLocation, hostOverride string, serviceRoot string, serviceRootZFS bool) (bool, error) {
	if serviceOverride == "" || strings.TrimSpace(serviceRoot) == "" {
		return false, nil
	}
	entry, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	if !ok {
		return false, nil
	}
	entry.ServiceRoot = strings.TrimSpace(serviceRoot)
	entry.ServiceRootZFS = serviceRootZFS
	cfgLoc.Config.SetServiceEntry(entry)
	return true, saveProjectConfig(cfgLoc)
}
```

Add the warning helper near `saveServiceSetConfig`:

```go
func printServiceSetSyncHint(w io.Writer, service string) error {
	if _, err := fmt.Fprintln(w, "Updated catch service settings. No matching yeet.toml entry was updated."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "Run from the project directory, or run:"); err != nil {
		return err
	}
	if strings.TrimSpace(service) == "" {
		_, err := fmt.Fprintln(w, "  yeet service sync <svc> --config ~/yeet-services/yeet.toml")
		return err
	}
	_, err := fmt.Fprintf(w, "  yeet service sync %s --config ~/yeet-services/yeet.toml\n", service)
	return err
}
```

- [ ] **Step 6: Adjust existing call sites and tests for new return value**

Run:

```bash
rg -n "saveServiceSetConfig" pkg/yeet
```

For any direct test call, update the assertion to handle `(bool, error)`. Keep behavior unchanged: matching entries return `true, nil`; missing config or missing entry return `false, nil`.

- [ ] **Step 7: Run sync tests**

Run:

```bash
gofmt -w pkg/yeet/service_sync.go pkg/yeet/service_sync_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go
go test ./pkg/yeet -run 'TestServiceSync|TestServiceSetUpdatesExistingConfigOnly'
```

Expected: sync tests pass, and the existing service-set config update test still passes after any expectation updates for the new warning output.

- [ ] **Step 8: Commit**

```bash
git add pkg/yeet/service_sync.go pkg/yeet/service_sync_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go
git commit -m "yeet: sync service roots from catch"
```

## Task 5: Service Set Drift Warning Tests

**Files:**
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Split warning assertions out of the existing service-set test**

In `TestServiceSetUpdatesExistingConfigOnly`, capture stdout around the first missing-config call:

```go
out, err := captureSvcStdout(t, func() error {
	return HandleSvcCmd([]string{"service", "set", "--service-root=/srv/apps/missing"})
})
if err != nil {
	t.Fatalf("HandleSvcCmd missing config error: %v", err)
}
if !strings.Contains(out, "No matching yeet.toml entry was updated") || !strings.Contains(out, "yeet service sync svc-a --config ~/yeet-services/yeet.toml") {
	t.Fatalf("service set hint = %q, want sync hint", out)
}
```

Then capture stdout around the successful matching-entry calls and assert no hint:

```go
out, err = captureSvcStdout(t, func() error {
	return HandleSvcCmd([]string{"service", "set", "--service-root=tank/apps/svc-a", "--zfs", "--copy"})
})
if err != nil {
	t.Fatalf("HandleSvcCmd existing config error: %v", err)
}
if strings.Contains(out, "No matching yeet.toml entry was updated") {
	t.Fatalf("service set output = %q, want no sync hint for matching config", out)
}
```

Use the same pattern for the non-ZFS matching call.

- [ ] **Step 2: Run service-set warning tests**

Run:

```bash
gofmt -w pkg/yeet/svc_cmd_branch_test.go
go test ./pkg/yeet -run 'TestServiceSetUpdatesExistingConfigOnly'
```

Expected: test passes and proves the hint appears only when no local config entry was updated.

- [ ] **Step 3: Commit**

```bash
git add pkg/yeet/svc_cmd_branch_test.go
git commit -m "yeet: warn when service set leaves config unsynced"
```

## Task 6: Docs And Help Reference

**Files:**
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/concepts/data-layout.mdx`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-llm.md`

- [ ] **Step 1: Update README**

In `README.md`, after the `yeet service set` examples, add:

````markdown
If you moved a service from outside the project directory, sync the live root
identity back into the local replay config:

```bash
yeet service sync vaultwarden
yeet service sync vaultwarden --config ~/yeet-services/yeet.toml
yeet service sync --all --config ~/yeet-services/yeet.toml
```

Catch remains the source of truth for the live service. `yeet service sync`
updates only existing entries in `yeet.toml`; it does not import arbitrary
catch services because catch does not know the local payload or env file paths.
For ZFS-backed roots, the local config stores the dataset name with
`service_root_zfs = true`.
````

- [ ] **Step 2: Update website CLI reference**

In `website/docs/cli/yeet-cli.mdx`, after the `service set` section, add:

````markdown
### service sync

Pull catch-owned service settings into an existing local `yeet.toml` entry.
The first syncable settings are `service_root` and `service_root_zfs`.

```bash
yeet service sync vaultwarden
yeet service sync vaultwarden --config ~/yeet-services/yeet.toml
yeet service sync --all --config ~/yeet-services/yeet.toml
```

Use this after a live service is changed from another directory or another
machine. Catch is the source of truth for live state; `yeet.toml` is the replay
recipe. Sync updates matching entries only and does not import every service
from catch.
````

- [ ] **Step 3: Update workflow docs**

In `website/docs/operations/workflows.mdx`, after the service-root migration explanation, add:

````markdown
If the migration command was run outside the directory that contains the
service's `yeet.toml`, update the local replay config afterward:

```bash
yeet service sync vaultwarden --config ~/yeet-services/yeet.toml
```

For a config file with several services on the selected host:

```bash
yeet service sync --all --config ~/yeet-services/yeet.toml
```
````

- [ ] **Step 4: Update data-layout docs**

In `website/docs/concepts/data-layout.mdx`, add this paragraph near the existing service-root source-of-truth text:

```markdown
Catch stores the authoritative live root in its DB, including both the mounted
filesystem path and the ZFS dataset name when `--zfs` is used. `yeet.toml`
stores the user-facing replay intent. If those drift because the service was
mutated from a different directory, run `yeet service sync <svc>` to pull the
root identity back into the local config.
```

- [ ] **Step 5: Refresh CLI help reference**

Update `.codex/skills/yeet-cli/references/yeet-help-llm.md` so the service group lists both commands:

```markdown
- `service set`: Set service settings
- `service sync`: Sync local yeet.toml service settings from catch
```

Add a group command section:

````markdown
## Group Command: service sync

# yeet service sync

Sync local yeet.toml service settings from catch

## Usage

```text
yeet [GLOBAL OPTIONS] service sync <svc> [--config=PATH]
yeet [GLOBAL OPTIONS] service sync --all [--config=PATH]
```

## Options

- `--all`: Sync all existing entries in the selected yeet.toml for the target host.
- `--config`: Path to the yeet.toml file to update.

## Examples

```bash
yeet service sync <svc>
yeet service sync --all
yeet service sync <svc> --config ~/yeet-services/yeet.toml
```
````

- [ ] **Step 6: Run docs checks**

Run:

```bash
rg -n "service sync|service_root_zfs|yeet.toml" README.md website/docs .codex/skills/yeet-cli/references/yeet-help-llm.md
```

Expected: new command appears in README, website docs, and the help reference.

- [ ] **Step 7: Commit website submodule changes**

Run:

```bash
cd website
git status --short
git add docs/cli/yeet-cli.mdx docs/operations/workflows.mdx docs/concepts/data-layout.mdx
git commit -m "docs: document service sync"
cd ..
```

Expected: website commit succeeds and the parent repo shows a modified `website` submodule pointer.

- [ ] **Step 8: Commit parent docs changes**

```bash
git add README.md .codex/skills/yeet-cli/references/yeet-help-llm.md website
git commit -m "docs: document service sync"
```

## Task 7: Full Verification

**Files:**
- No edits expected.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/catchrpc ./pkg/catch ./pkg/yeet
```

Expected: all listed packages pass.

- [ ] **Step 2: Run full test suite**

Run:

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: all hooks pass.

- [ ] **Step 4: Manual CLI smoke tests**

Build the client:

```bash
go build ./cmd/yeet
```

Run help commands:

```bash
./yeet service --help
./yeet service sync --help
```

Expected:

- `service --help` lists both `set` and `sync`.
- `service sync --help` shows `--all`, `--config`, and the usage with `<svc>` or `--all`.

- [ ] **Step 5: Optional live smoke test against pve1**

Only run this if the current checkout is intended to touch the live host during execution. From `~/yeet-services`, run:

```bash
yeet --host yeet-pve1 service sync sonarr --config ~/yeet-services/yeet.toml
```

Expected:

```text
Updated sonarr@yeet-pve1 in /Users/shayne/yeet-services/yeet.toml
  service_root = "flash/yeet/sonarr"
  service_root_zfs = true
```

Then inspect:

```bash
rg -n 'name = "sonarr"|service_root|service_root_zfs' ~/yeet-services/yeet.toml
```

Expected: the `sonarr` entry stores `service_root = "flash/yeet/sonarr"` and `service_root_zfs = true`.

- [ ] **Step 6: Final status**

Run:

```bash
git status --short --branch
git log --oneline -5
```

Expected: branch is `main`; uncommitted changes are either absent or are intentional generated/live-test config changes outside this repo. The top commits include the service-sync implementation and docs commits.
