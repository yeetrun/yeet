# Service Root Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add configurable per-service root directories for initial `yeet run` and a controlled `yeet service set <svc> --service-root=/abs/path` migration path.

**Architecture:** Catch is authoritative for live service roots through `db.Service.ServiceRoot`; empty means the existing default under `<catch-data>/services/<svc>`. The client treats `yeet.toml` as a replay recipe by optionally storing `service_root`, rehydrating it into future runs, and updating it after successful `service set` for matching existing entries. Catch validates roots, derives every service path from the effective root, rejects root changes through `run`, and performs migration with a staged copy plus final atomic rename before updating the DB.

**Tech Stack:** Go, `pkg/yargs` CLI parsing, catch RPC exec/PTY commands, JSON DB with generated viewer accessors, `copyutil` tar/extract helpers, Go table-driven tests, markdown docs.

---

## Execution Notes

- Work on `main`; the user explicitly approved this.
- The user explicitly authorized commits.
- Use TDD for behavior changes: write a failing test, run it, implement, rerun, then commit.
- Do not run implementation subagents in parallel if their write sets overlap.
- `pkg/db/db_view.go` and `pkg/db/db_clone.go` are generated; update `pkg/db/db.go` first, then run `go generate ./pkg/db`.
- `website/` is a submodule. Before editing it, read `website/AGENTS.md` if present.

## File Structure

- Modify `pkg/db/db.go`: add `ServiceRoot` to `db.Service`.
- Modify `pkg/db/migrate.go`: bump the DB data version and add a no-op migrator.
- Regenerate `pkg/db/db_view.go` and `pkg/db/db_clone.go`.
- Modify `pkg/db/*_test.go`: cover clone/view/migration compatibility for `ServiceRoot`.
- Modify `pkg/cli/cli.go`: add `RunFlags.ServiceRoot`, `ServiceSetFlags`, `ParseServiceSet`, and the `service set` remote group metadata.
- Modify `pkg/cli/cli_test.go`: cover run/service-set parsing and registry metadata.
- Modify `cmd/yeet/cli.go`: add the `service` group handler.
- Modify `cmd/yeet/cli_bridge_test.go` and `cmd/yeet/cli_test.go`: cover `service set` bridging/routing.
- Modify `pkg/yeet/project_config.go`: add `ServiceEntry.ServiceRoot`.
- Modify `pkg/yeet/svc_cmd.go`: parse, save, rehydrate, and handle `service_root`; update local config after `service set`.
- Modify `pkg/yeet/run_changes.go`: include service-root changes in run change detection.
- Modify `pkg/yeet/*_test.go`: cover `yeet.toml` persistence, rehydration, and root-only run changes.
- Modify `pkg/catch/catch.go`: add effective root helpers, validation helpers, and root-aware remove/info paths.
- Modify `pkg/catch/installer_file.go`, `pkg/catch/installer_service.go`, `pkg/catch/registry.go`, `pkg/catch/tty_install.go`, `pkg/catch/tty_ops.go`, `pkg/catch/tsns.go`, and other catch path consumers found by `rg 'service(Root|Bin|Run|Data|Env)Dir|ensureDirs' pkg/catch`.
- Create `pkg/catch/tty_service_set.go`: implement `yeet service set` orchestration and safe migration.
- Create/modify `pkg/catch/tty_service_set_test.go`, `pkg/catch/catch_test.go`, `pkg/catch/installer_file_test.go`, `pkg/catch/remove_test.go`, and related tests.
- Modify `README.md` and website docs for the new commands and flags.

---

### Task 1: DB Schema And Generated Accessors

**Files:**
- Modify: `pkg/db/db.go`
- Modify: `pkg/db/migrate.go`
- Regenerate: `pkg/db/db_view.go`
- Regenerate: `pkg/db/db_clone.go`
- Test: `pkg/db/db_test.go` or the existing DB migration/view test file in `pkg/db`

- [ ] **Step 1: Write failing DB tests**

Add tests that prove `ServiceRoot` survives clone/view access and that a v5 DB migrates to the new version without changing existing service roots:

```go
func TestServiceRootCloneAndView(t *testing.T) {
	d := &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"api": {Name: "api", ServiceRoot: "/srv/apps/api"},
		},
	}
	view := d.View()
	if got := view.Services().Get("api").ServiceRoot(); got != "/srv/apps/api" {
		t.Fatalf("ServiceRoot view = %q, want /srv/apps/api", got)
	}
	clone := d.Clone()
	clone.Services["api"].ServiceRoot = "/srv/other/api"
	if got := d.Services["api"].ServiceRoot; got != "/srv/apps/api" {
		t.Fatalf("original ServiceRoot changed to %q", got)
	}
}

func TestMigrateAddsServiceRootVersion(t *testing.T) {
	d := &Data{
		DataVersion: 5,
		Services: map[string]*Service{
			"api": {Name: "api"},
		},
	}
	migrated, err := migrate(d)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !migrated {
		t.Fatal("migrate returned migrated=false, want true")
	}
	if d.DataVersion != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", d.DataVersion, CurrentDataVersion)
	}
	if got := d.Services["api"].ServiceRoot; got != "" {
		t.Fatalf("ServiceRoot = %q, want empty default", got)
	}
}
```

- [ ] **Step 2: Run the DB tests and verify RED**

Run:

```bash
go test ./pkg/db -run 'Test(ServiceRootCloneAndView|MigrateAddsServiceRootVersion)' -count=1
```

Expected: FAIL because `Service.ServiceRoot` and `ServiceView.ServiceRoot()` do not exist.

- [ ] **Step 3: Implement the DB field and migrator**

In `pkg/db/db.go`, add the field near `ServiceType`:

```go
	// ServiceRoot is the absolute service root on the catch host.
	// Empty means filepath.Join(Store.serviceRoot, Name).
	ServiceRoot string `json:",omitempty"`
```

In `pkg/db/migrate.go`, bump the version by one and add a no-op migrator:

```go
const CurrentDataVersion = 6
```

```go
	5: addServiceRoot,
```

```go
func addServiceRoot(d *Data) error {
	return nil
}
```

- [ ] **Step 4: Regenerate DB view/clone code**

Run:

```bash
go generate ./pkg/db
```

Expected: generated code changes in `pkg/db/db_view.go` and `pkg/db/db_clone.go`.

- [ ] **Step 5: Run DB tests and commit**

Run:

```bash
go test ./pkg/db -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/db/db.go pkg/db/migrate.go pkg/db/db_view.go pkg/db/db_clone.go pkg/db/*_test.go
git commit -m "pkg/db: store service roots"
```

---

### Task 2: CLI Metadata, Client Config, And Run Rehydration

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/run_changes.go`
- Modify: `pkg/yeet/project_config_test.go`
- Modify: `pkg/yeet/handle_svc_cmd_config_test.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`
- Modify: `pkg/yeet/run_changes_test.go`

- [ ] **Step 1: Write failing CLI tests**

Extend `pkg/cli/cli_test.go` so `ParseRun` accepts `--service-root`, and add a service-set parser test:

```go
func TestParseServiceSetFlags(t *testing.T) {
	flags, args, err := ParseServiceSet([]string{"--service-root=/srv/apps/api", "--copy"})
	if err != nil {
		t.Fatalf("ParseServiceSet: %v", err)
	}
	if flags.ServiceRoot != "/srv/apps/api" || !flags.Copy || flags.Empty {
		t.Fatalf("flags = %#v, want root copy", flags)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}

	if _, _, err := ParseServiceSet([]string{"--service-root=relative"}); err == nil {
		t.Fatal("ParseServiceSet relative root error = nil")
	}
	if _, _, err := ParseServiceSet([]string{"--service-root=/srv/api", "--copy", "--empty"}); err == nil {
		t.Fatal("ParseServiceSet --copy --empty error = nil")
	}
	if _, _, err := ParseServiceSet([]string{"--copy"}); err == nil {
		t.Fatal("ParseServiceSet missing --service-root error = nil")
	}
}
```

Add registry assertions:

```go
if !RemoteFlagSpecs()["run"]["--service-root"].ConsumesValue {
	t.Fatal("run --service-root should consume a value")
}
if _, ok := RemoteGroupInfos()["service"].Commands["set"]; !ok {
	t.Fatal("service set should be registered")
}
if !RemoteGroupFlagSpecs()["service"]["set"]["--service-root"].ConsumesValue {
	t.Fatal("service set --service-root should consume a value")
}
```

- [ ] **Step 2: Write failing routing tests**

Add bridge coverage to `cmd/yeet/cli_bridge_test.go`:

```go
func TestBridgeServiceArgsServiceSetGroup(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	for _, args := range [][]string{
		{"service", "set", "svc-a", "--service-root=/srv/apps/svc-a"},
		{"service", "set", "--service-root", "/srv/apps/svc-a", "svc-a"},
	} {
		service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
		if !ok {
			t.Fatalf("expected bridge for %#v", args)
		}
		if service != "svc-a" || host != "" {
			t.Fatalf("bridge target = service=%q host=%q, want svc-a empty", service, host)
		}
		if got := strings.Join(bridged, " "); got != "service set --service-root=/srv/apps/svc-a" && got != "service set --service-root /srv/apps/svc-a" {
			t.Fatalf("bridged = %#v", bridged)
		}
	}
}
```

Add a `prepareCommandRoute` case in `cmd/yeet/cli_test.go`:

```go
got := prepareCommandRoute([]string{"service@catch-a", "set", "svc-a", "--service-root=/srv/apps/svc-a"}, "")
if got.host != "catch-a" || got.service != "svc-a" {
	t.Fatalf("route = %#v, want host catch-a service svc-a", got)
}
if !reflect.DeepEqual(got.args, []string{"service", "set", "--service-root=/srv/apps/svc-a"}) {
	t.Fatalf("args = %#v", got.args)
}
```

- [ ] **Step 3: Write failing client config/run tests**

Add tests covering separate `service_root` storage and rehydration:

```go
func TestSaveRunConfigStoresServiceRoot(t *testing.T) {
	withServiceOverride(t, "api")
	dir := t.TempDir()
	loc := &projectConfigLocation{Path: filepath.Join(dir, "yeet.toml"), Dir: dir, Config: &ProjectConfig{Version: projectConfigVersion}}
	payload := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(payload, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveRunConfig(loc, "host-a", payload, []string{"--net=host", "--", "--app"}, "/srv/apps/api"); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("api", "host-a")
	if !ok {
		t.Fatal("service entry missing")
	}
	if entry.ServiceRoot != "/srv/apps/api" {
		t.Fatalf("ServiceRoot = %q, want /srv/apps/api", entry.ServiceRoot)
	}
	if slices.Contains(entry.Args, "--service-root") {
		t.Fatalf("Args include service-root: %#v", entry.Args)
	}
}
```

Add a `runFromProjectConfigWithForce` test that expects `run --service-root /srv/apps/api -- <payload args>` to be sent to `runWithChanges`.

Add `pkg/yeet/run_changes_test.go` coverage:

```go
func TestDetectRunChangesServiceRootOnlyRequiresRun(t *testing.T) {
	summary, err := detectRunChanges("image.example/app:latest", []string{"--service-root=/srv/new/api"}, "", []string{"--service-root=/srv/old/api"})
	if err != nil {
		t.Fatalf("detectRunChanges: %v", err)
	}
	if !summary.argsChanged || !summary.requiresRun() {
		t.Fatalf("summary = %#v, want argsChanged requiring run", summary)
	}
}
```

- [ ] **Step 4: Run targeted tests and verify RED**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet -run 'Test(ParseRun|ParseServiceSet|RemoteRegistryMetadata|BridgeServiceArgsServiceSet|PrepareCommandRoute|SaveRunConfigStoresServiceRoot|RunFromProjectConfig|DetectRunChangesServiceRootOnly)' -count=1
```

Expected: FAIL for missing types, parser fields, routing, and `saveRunConfig` signature.

- [ ] **Step 5: Implement CLI metadata and parsers**

In `pkg/cli/cli.go`, add:

```go
type ServiceSetFlags struct {
	ServiceRoot string
	Copy        bool
	Empty       bool
}
```

Add to `RunFlags` and `runFlagsParsed`:

```go
	ServiceRoot string
```

```go
	ServiceRoot string `flag:"service-root"`
```

Add parsed service-set flags:

```go
type serviceSetFlagsParsed struct {
	ServiceRoot string `flag:"service-root"`
	Copy        bool   `flag:"copy"`
	Empty       bool   `flag:"empty"`
}
```

Add `service` group metadata:

```go
"service": {
	Name:        "service",
	Description: "Mutate service settings that require explicit migration",
	Commands: map[string]CommandInfo{
		"set": {
			Name:        "set",
			Description: "Change service settings such as the service root",
			Usage:       "service set <svc> --service-root=/abs/path [--copy|--empty]",
			Examples: []string{
				"yeet service set <svc> --service-root=/srv/apps/<svc>",
				"yeet service set <svc> --service-root=/srv/apps/<svc> --copy",
				"yeet service set <svc> --service-root=/srv/apps/<svc> --empty",
			},
			ArgsSchema: ServiceArgs{},
		},
	},
},
```

Add group flag specs:

```go
"service": {
	"set": flagSpecsFromStruct(serviceSetFlagsParsed{}),
},
```

Add parser:

```go
func ParseServiceSet(args []string) (ServiceSetFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[serviceSetFlagsParsed](parseArgs)
	if err != nil {
		return ServiceSetFlags{}, nil, err
	}
	flags := ServiceSetFlags{
		ServiceRoot: strings.TrimSpace(parsed.Flags.ServiceRoot),
		Copy:        parsed.Flags.Copy,
		Empty:       parsed.Flags.Empty,
	}
	if flags.Copy && flags.Empty {
		return ServiceSetFlags{}, nil, fmt.Errorf("cannot use --copy and --empty together")
	}
	if flags.ServiceRoot == "" {
		return ServiceSetFlags{}, nil, fmt.Errorf("--service-root is required")
	}
	if !filepath.IsAbs(flags.ServiceRoot) {
		return ServiceSetFlags{}, nil, fmt.Errorf("--service-root must be absolute: %s", flags.ServiceRoot)
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}
```

Import `path/filepath` in `pkg/cli/cli.go`.

- [ ] **Step 6: Implement client routing and config**

In `cmd/yeet/cli.go`, add a service group:

```go
"service": {
	Description: "Mutate service settings that require explicit migration",
	Commands: map[string]yargs.SubcommandHandler{
		"set": handleServiceGroup,
	},
},
```

Add:

```go
func handleServiceGroup(ctx context.Context, args []string) error {
	return yeet.HandleSvcCmd(append([]string{"service"}, args...))
}
```

In `pkg/yeet/project_config.go`, add:

```go
	ServiceRoot string `toml:"service_root,omitempty"`
```

Preserve it in `SetServiceEntry`:

```go
			if entry.ServiceRoot != "" {
				c.Services[i].ServiceRoot = entry.ServiceRoot
			}
```

In `pkg/yeet/run_changes.go`, add `extractServiceRootFlag` and `runArgsWithServiceRoot` following `extractEnvFileFlag`:

```go
func extractServiceRootFlag(args []string) (string, []string, bool, error) {
	out := make([]string, 0, len(args))
	var serviceRoot string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == "--service-root" {
			if i+1 >= len(args) {
				return "", nil, false, fmt.Errorf("--service-root requires a value")
			}
			serviceRoot = args[i+1]
			found = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "--service-root=") {
			serviceRoot = strings.TrimPrefix(arg, "--service-root=")
			found = true
			continue
		}
		out = append(out, arg)
	}
	return serviceRoot, out, found, nil
}

func runArgsWithServiceRoot(serviceRoot string, args []string) []string {
	serviceRoot = strings.TrimSpace(serviceRoot)
	if serviceRoot == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--service-root", serviceRoot)
	out = append(out, args...)
	return out
}
```

In `pkg/yeet/svc_cmd.go`, extend `parsedSvcRun`:

```go
	ServiceRoot    string
	ServiceRootArg string
	ServiceRootSet bool
```

Parse before env/force:

```go
	serviceRootArg, filteredArgs, serviceRootSet, err := extractServiceRootFlag(runArgs)
	if err != nil {
		return parsedSvcRun{}, err
	}
	envFileArg, filteredArgs, envFileSet, err := extractEnvFileFlag(filteredArgs)
```

Use stored root when omitted:

```go
	serviceRoot := serviceRootArg
	if serviceRoot == "" && hasEntry {
		serviceRoot = entry.ServiceRoot
	}
	filteredArgs = runArgsWithServiceRoot(serviceRoot, filteredArgs)
```

Save it separately:

```go
if err := saveRunConfig(req.Config, req.HostOverride, run.Payload, normalizeRunArgs(run.Args), run.ServiceRootArg); err != nil {
	return err
}
```

Update `saveRunConfig` signature to include `serviceRoot string`, and set:

```go
	ServiceRoot: strings.TrimSpace(serviceRoot),
```

Make `runFromProjectConfigWithForce` call:

```go
	runArgs := runArgsWithServiceRoot(stored.Entry.ServiceRoot, rehydrateRunArgs(stored.Entry.Args))
	return runWithChanges(payload, runArgs, envFile, stored.Entry, forceDeploy)
```

Add `service` handling to `svcCommandHandlers` so successful remote set updates local config only when an entry exists:

```go
"service": func(ctx context.Context, req svcCommandRequest) error {
	return handleSvcService(ctx, req)
},
```

```go
func handleSvcService(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) == 0 || req.Command.Args[0] != "set" {
		return handleSvcRemote(ctx, req)
	}
	flags, _, err := cli.ParseServiceSet(req.Command.Args[1:])
	if err != nil {
		return err
	}
	if err := execRemoteFn(ctx, req.Service, req.Command.RawArgs, nil, serviceSetWantsTTY(flags)); err != nil {
		return err
	}
	return updateServiceRootConfigIfPresent(req.Config, req.HostOverride, flags.ServiceRoot)
}

func serviceSetWantsTTY(flags cli.ServiceSetFlags) bool {
	return !flags.Copy && !flags.Empty && isTerminalFn(int(os.Stdin.Fd())) && isTerminalFn(int(os.Stdout.Fd()))
}
```

Implement `updateServiceRootConfigIfPresent` by loading the current service entry and saving only when it exists.

- [ ] **Step 7: Run tests and commit**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go pkg/yeet/project_config.go pkg/yeet/svc_cmd.go pkg/yeet/run_changes.go pkg/yeet/*_test.go
git commit -m "cmd/yeet: add service root client plumbing"
```

---

### Task 3: Catch Effective Root Plumbing

**Files:**
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_service.go`
- Modify: `pkg/catch/registry.go`
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tsns.go`
- Modify: catch tests that currently call `serviceRootDir`, `serviceBinDir`, `serviceRunDir`, `serviceDataDir`, `serviceEnvDir`, or `ensureDirs`

- [ ] **Step 1: Write failing path resolver tests**

In `pkg/catch/catch_test.go`, add:

```go
func TestServiceDirectoryPlanUsesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom-api")
	got := serviceDirectoryPlan(root)
	want := []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "data"),
		filepath.Join(root, "env"),
		filepath.Join(root, "run"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceDirectoryPlan = %#v, want %#v", got, want)
	}
}

func TestServiceRootDirUsesDBServiceRoot(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "api")
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		service.Name = "api"
		service.ServiceRoot = customRoot
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}
	got, err := server.serviceRootDir("api")
	if err != nil {
		t.Fatalf("serviceRootDir: %v", err)
	}
	if got != customRoot {
		t.Fatalf("serviceRootDir = %q, want %q", got, customRoot)
	}
}
```

Add a service-info test asserting `Paths.Root` reports the custom root.

- [ ] **Step 2: Run catch path tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'Test(ServiceDirectoryPlanUsesRoot|ServiceRootDirUsesDBServiceRoot|ServiceInfo)' -count=1
```

Expected: FAIL because root helpers are name-only and ignore DB state.

- [ ] **Step 3: Implement effective root helpers**

In `pkg/catch/catch.go`, replace the root helpers with error-returning effective helpers:

```go
func (s *Server) defaultServiceRootDir(sn string) string {
	return filepath.Join(s.cfg.ServicesRoot, sn)
}

func (s *Server) serviceRootFromView(sv db.ServiceView) string {
	if root := strings.TrimSpace(sv.ServiceRoot()); root != "" {
		return root
	}
	return s.defaultServiceRootDir(sv.Name())
}

func (s *Server) serviceRootDir(sn string) (string, error) {
	sv, err := s.serviceView(sn)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return s.defaultServiceRootDir(sn), nil
		}
		return "", err
	}
	return s.serviceRootFromView(sv), nil
}

func serviceDirectoryPlan(serviceRoot string) []string {
	return []string{
		filepath.Join(serviceRoot, "bin"),
		filepath.Join(serviceRoot, "data"),
		filepath.Join(serviceRoot, "env"),
		filepath.Join(serviceRoot, "run"),
	}
}
```

Add convenience helpers that return `(string, error)`:

```go
func (s *Server) serviceBinDir(sn string) (string, error) {
	root, err := s.serviceRootDir(sn)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin"), nil
}
```

Repeat for `serviceRunDir`, `serviceDataDir`, and `serviceEnvDir`.

Add root-based helpers for hot paths that already have a `db.ServiceView`:

```go
func serviceBinDirForRoot(root string) string  { return filepath.Join(root, "bin") }
func serviceRunDirForRoot(root string) string  { return filepath.Join(root, "run") }
func serviceDataDirForRoot(root string) string { return filepath.Join(root, "data") }
func serviceEnvDirForRoot(root string) string  { return filepath.Join(root, "env") }
```

Update `ensureDirs`:

```go
func (s *Server) ensureDirsForRoot(root, uname string) error {
	for _, dir := range serviceDirectoryPlan(root) {
		if err := ensureServiceDir(dir, uname); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Update catch path consumers**

Use `rg 'service(Root|Bin|Run|Data|Env)Dir|ensureDirs' pkg/catch` and update each caller to either:

```go
root, err := s.serviceRootDir(sn)
if err != nil {
	return err
}
```

or, when a `db.ServiceView` is already available:

```go
root := s.serviceRootFromView(sv)
```

Use root-derived paths for:

- `dockerComposeService`
- `systemdService`
- service info
- tailscale/netns artifacts
- registry generated compose mounts
- file installer temp paths and artifact paths
- installer prune paths
- rollback install paths
- copy source/destination roots

For `RemoveService`, capture the root before deleting the DB row:

```go
root, err := s.serviceRootDir(name)
if err != nil {
	report.addWarning(fmt.Errorf("failed to load service root for %q: %w", name, err))
	root = s.defaultServiceRootDir(name)
}
if err := s.removeServiceFromDB(name); err != nil {
	return report, fmt.Errorf("failed to remove service from db: %w", err)
}
s.removeServiceDirs(report, root)
```

Change `removeServiceDirs` to accept a root:

```go
func (s *Server) removeServiceDirs(report *RemoveReport, root string) {
	dirs, err := filepath.Glob(filepath.Join(root, "*"))
	...
}
```

- [ ] **Step 5: Run catch tests and commit**

Run:

```bash
go test ./pkg/catch ./pkg/svc ./pkg/catchrpc -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch pkg/svc pkg/catchrpc
git commit -m "pkg/catch: resolve paths from service roots"
```

---

### Task 4: Initial Run Service Root Validation And Persistence

**Files:**
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/installer_service.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/tty_install_test.go`

- [ ] **Step 1: Write failing validation tests**

In `pkg/catch/catch_test.go`, add:

```go
func TestValidateRequestedServiceRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "api")
	got, err := validateRequestedServiceRoot(root)
	if err != nil {
		t.Fatalf("validateRequestedServiceRoot: %v", err)
	}
	if got != root {
		t.Fatalf("clean root = %q, want %q", got, root)
	}
	for _, bad := range []string{"relative/api", filepath.Join(parent, "missing-parent", "api")} {
		if _, err := validateRequestedServiceRoot(bad); err == nil {
			t.Fatalf("validateRequestedServiceRoot(%q) error = nil", bad)
		}
	}
}

func TestValidateRequestedServiceRootRejectsNonEmptyFinalDir(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "api")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRequestedServiceRoot(root); err == nil {
		t.Fatal("validateRequestedServiceRoot non-empty dir error = nil")
	}
}
```

In `pkg/catch/installer_file_test.go`, add tests that:

- `NewFileInstaller` with `FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "api", ServiceRoot: customRoot}}` creates `bin`, `run`, `env`, and `data` under `customRoot`.
- The DB service record stores `ServiceRoot`.
- Re-running with the same root succeeds.
- Re-running with a different root fails with an error containing `yeet service set api --service-root=`.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'TestValidateRequestedServiceRoot|TestNewFileInstaller.*ServiceRoot|TestRunCmdFunc.*ServiceRoot' -count=1
```

Expected: FAIL for missing `ServiceRoot` installer config and validation helpers.

- [ ] **Step 3: Add service root to installer config**

In `pkg/catch/catch.go`, add to `InstallerCfg`:

```go
	// ServiceRoot overrides the default root for a new service.
	ServiceRoot string
```

In `pkg/catch/tty_install.go`, when building `FileInstallerCfg` from `cli.RunFlags`, set:

```go
	ServiceRoot: flags.ServiceRoot,
```

In `pkg/catch/installer_service.go`, carry `InstallerCfg.ServiceRoot` into any DB mutation that creates or updates `db.Service`.

- [ ] **Step 4: Implement root validation and existing-service rejection**

Add helpers in `pkg/catch/catch.go`:

```go
func validateRequestedServiceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", nil
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("service root must be absolute: %s", root)
	}
	clean := filepath.Clean(root)
	parent := filepath.Dir(clean)
	st, err := os.Stat(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("service root parent does not exist: %s", parent)
		}
		return "", fmt.Errorf("stat service root parent %s: %w", parent, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("service root parent is not a directory: %s", parent)
	}
	empty, err := rootIsMissingOrEmpty(clean)
	if err != nil {
		return "", err
	}
	if !empty {
		return "", fmt.Errorf("destination service root is not empty: %s", clean)
	}
	return clean, nil
}
```

Add:

```go
func (s *Server) prepareServiceRootForInstall(sn, requested string) (string, error) {
	sv, err := s.serviceView(sn)
	if err == nil {
		effective := s.serviceRootFromView(sv)
		requested = strings.TrimSpace(requested)
		if requested == "" || filepath.Clean(requested) == effective {
			return effective, nil
		}
		return "", fmt.Errorf("service root for %q is already %s; got %s\nuse `yeet service set %s --service-root=%s` to migrate it", sn, effective, requested, sn, requested)
	}
	if !errors.Is(err, errServiceNotFound) {
		return "", err
	}
	if requested == "" {
		return s.defaultServiceRootDir(sn), nil
	}
	return validateRequestedServiceRoot(requested)
}
```

Use `prepareServiceRootForInstall` before creating temp files or staging artifacts in `NewFileInstaller`. Ensure `ServiceRoot` is set before artifact paths are derived:

```go
root, err := s.prepareServiceRootForInstall(cfg.ServiceName, cfg.ServiceRoot)
if err != nil {
	return nil, err
}
cfg.ServiceRoot = root
if err := s.ensureDirsForRoot(root, cfg.User); err != nil {
	return nil, err
}
```

When mutating a newly created service:

```go
if cfg.ServiceRoot != "" && cfg.ServiceRoot != s.defaultServiceRootDir(cfg.ServiceName) {
	service.ServiceRoot = cfg.ServiceRoot
}
```

- [ ] **Step 5: Run catch tests and commit**

Run:

```bash
go test ./pkg/catch -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch
git commit -m "pkg/catch: validate custom service roots on run"
```

---

### Task 5: Service Set Migration Command

**Files:**
- Create: `pkg/catch/tty_service_set.go`
- Create: `pkg/catch/tty_service_set_test.go`
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/remove_test.go`
- Modify: `pkg/copyutil` only if a new copy helper is needed

- [ ] **Step 1: Write failing service-set tests**

Create `pkg/catch/tty_service_set_test.go` with focused tests:

```go
func TestServiceSetRootRejectsMissingService(t *testing.T) {
	server := newTestServer(t)
	execer := newTestTTYExecer(t, server, "missing", nil)
	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{ServiceRoot: filepath.Join(t.TempDir(), "missing"), Empty: true})
	if err == nil || !strings.Contains(err.Error(), "service \"missing\" not found") {
		t.Fatalf("error = %v, want missing service", err)
	}
}

func TestServiceSetRootNonTTYRequiresCopyOrEmpty(t *testing.T) {
	server, oldRoot := newServiceWithRootForMigration(t, "api", "")
	newRoot := filepath.Join(t.TempDir(), "api")
	execer := newTestTTYExecer(t, server, "api", nil)
	execer.isPty = false
	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{ServiceRoot: newRoot})
	if err == nil || !strings.Contains(err.Error(), "requires --copy or --empty") {
		t.Fatalf("error = %v, want non-tty explicit mode; oldRoot=%s", err, oldRoot)
	}
}

func TestServiceSetRootCopyStagesThenRenamesAndUpdatesDB(t *testing.T) {
	server, oldRoot := newServiceWithRootForMigration(t, "api", "")
	if err := os.WriteFile(filepath.Join(oldRoot, "data", "state.db"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	newRoot := filepath.Join(parent, "api")
	execer := newTestTTYExecer(t, server, "api", nil)
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{ServiceRoot: newRoot, Copy: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc: %v", err)
	}
	assertFileContent(t, filepath.Join(newRoot, "data", "state.db"), "state")
	if _, err := os.Stat(filepath.Join(oldRoot, "data", "state.db")); err != nil {
		t.Fatalf("old root should remain: %v", err)
	}
	sv, err := server.serviceView("api")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.ServiceRoot(); got != newRoot {
		t.Fatalf("ServiceRoot = %q, want %q", got, newRoot)
	}
}

func TestServiceSetRootEmptyUpdatesDBWithoutCopy(t *testing.T) {
	server, oldRoot := newServiceWithRootForMigration(t, "api", "")
	newRoot := filepath.Join(t.TempDir(), "api")
	execer := newTestTTYExecer(t, server, "api", nil)
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{ServiceRoot: newRoot, Empty: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc: %v", err)
	}
	for _, child := range []string{"bin", "data", "env", "run"} {
		if st, err := os.Stat(filepath.Join(newRoot, child)); err != nil || !st.IsDir() {
			t.Fatalf("missing %s in new root: stat=%v err=%v", child, st, err)
		}
	}
	if _, err := os.Stat(oldRoot); err != nil {
		t.Fatalf("old root should remain: %v", err)
	}
}
```

Add a table-driven test named `TestServiceSetRootRejectsInvalidMigrationPlans` with these rows and assertions:

```go
tests := []struct {
	name    string
	setup   func(t *testing.T, server *Server, oldRoot string) string
	flags   cli.ServiceSetFlags
	wantErr string
}{
	{
		name: "running service",
		setup: func(t *testing.T, server *Server, oldRoot string) string {
			return filepath.Join(t.TempDir(), "api")
		},
		flags:   cli.ServiceSetFlags{Empty: true},
		wantErr: "cannot migrate service root while",
	},
	{
		name: "missing parent",
		setup: func(t *testing.T, server *Server, oldRoot string) string {
			return filepath.Join(t.TempDir(), "missing", "api")
		},
		flags:   cli.ServiceSetFlags{Empty: true},
		wantErr: "parent does not exist",
	},
	{
		name: "non empty destination",
		setup: func(t *testing.T, server *Server, oldRoot string) string {
			root := filepath.Join(t.TempDir(), "api")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return root
		},
		flags:   cli.ServiceSetFlags{Empty: true},
		wantErr: "not empty",
	},
	{
		name: "new root inside old root",
		setup: func(t *testing.T, server *Server, oldRoot string) string {
			return filepath.Join(oldRoot, "child")
		},
		flags:   cli.ServiceSetFlags{Empty: true},
		wantErr: "nested service roots",
	},
	{
		name: "old root inside new root",
		setup: func(t *testing.T, server *Server, oldRoot string) string {
			return filepath.Dir(oldRoot)
		},
		flags:   cli.ServiceSetFlags{Empty: true},
		wantErr: "nested service roots",
	},
}
```

Add these standalone tests with concrete assertions:

- `TestServiceSetRootTTYDeclineLeavesDBAndFilesystemUnchanged`: use `rw := bytes.NewBufferString("n\n")`, set `isPty: true`, run without `--copy` or `--empty`, then assert `ServiceRoot` and both roots are unchanged.
- `TestServiceSetRootRenameFailureLeavesDBOldRoot`: make the destination parent unwritable when the platform honors mode bits, or create an existing non-empty destination after validation through an injected rename seam; assert the DB still points at the old root.
- `TestServiceSetRootCopyPreservesModeMtimeAndSymlink`: create a mode `0o700` directory, a file with fixed `mtime`, and a symlink in the old root; run `--copy`; assert mode, mtime, and symlink target in the new root.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'TestServiceSetRoot' -count=1
```

Expected: FAIL because `serviceSetCmdFunc` and migration helpers do not exist.

- [ ] **Step 3: Register catch-side command**

In `pkg/catch/tty_exec.go`, add:

```go
"service": func(e *ttyExecer, args []string) error {
	return e.serviceCmdFunc(args)
},
```

Create `pkg/catch/tty_service_set.go`:

```go
package catch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/copyutil"
)

type serviceRootCopyMode int

const (
	copyModePrompt serviceRootCopyMode = iota
	copyModeCopy
	copyModeEmpty
)

type serviceRootMigrationOptions struct {
	NewRoot string
	Mode    serviceRootCopyMode
	User    string
}

type serviceRootMigrationPlan struct {
	ServiceName string
	OldRoot     string
	NewRoot     string
}
```

Add dispatch:

```go
func (e *ttyExecer) serviceCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("service requires a command")
	}
	switch args[0] {
	case "set":
		flags, rest, err := cli.ParseServiceSet(args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("unexpected service set args: %s", strings.Join(rest, " "))
		}
		return e.serviceSetCmdFunc(flags)
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}
```

- [ ] **Step 4: Implement prompts and migration orchestration**

Add:

```go
func (e *ttyExecer) serviceSetCmdFunc(flags cli.ServiceSetFlags) error {
	mode := copyModePrompt
	if flags.Copy {
		mode = copyModeCopy
	}
	if flags.Empty {
		mode = copyModeEmpty
	}
	plan, err := e.s.validateServiceRootMigration(e.sn, flags.ServiceRoot)
	if err != nil {
		return err
	}
	mode, err = e.confirmServiceRootCopy(mode, plan.OldRoot, plan.NewRoot)
	if err != nil {
		return err
	}
	return e.s.migrateServiceRoot(e.sn, serviceRootMigrationOptions{
		NewRoot: plan.NewRoot,
		Mode:    mode,
		User:    e.user,
	})
}

func (e *ttyExecer) confirmServiceRootCopy(mode serviceRootCopyMode, oldRoot, newRoot string) (serviceRootCopyMode, error) {
	if mode != copyModePrompt {
		return mode, nil
	}
	if !e.isPty {
		return 0, fmt.Errorf("service set --service-root requires --copy or --empty when not running interactively")
	}
	ok, err := cmdutil.Confirm(e.rw, e.rw, fmt.Sprintf("Copy existing service files from %s to %s?", oldRoot, newRoot))
	if err != nil {
		return 0, err
	}
	if ok {
		return copyModeCopy, nil
	}
	return copyModeEmpty, nil
}
```

Add stopped-service validation:

```go
func (s *Server) validateServiceRootMigration(name, dst string) (serviceRootMigrationPlan, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return serviceRootMigrationPlan{}, fmt.Errorf("service %q not found", name)
		}
		return serviceRootMigrationPlan{}, err
	}
	running, err := s.IsServiceRunning(name)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	if running {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate service root while %q is running", name)
	}
	newRoot, err := validateRequestedServiceRoot(dst)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	oldRoot := s.serviceRootFromView(sv)
	if oldRoot == newRoot {
		return serviceRootMigrationPlan{}, fmt.Errorf("service root for %q is already %s", name, oldRoot)
	}
	if rootsAreNested(oldRoot, newRoot) || rootsAreNested(newRoot, oldRoot) {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, newRoot)
	}
	return serviceRootMigrationPlan{ServiceName: name, OldRoot: oldRoot, NewRoot: newRoot}, nil
}
```

- [ ] **Step 5: Implement safe copy and DB update**

Add helpers:

```go
func (s *Server) migrateServiceRoot(name string, opts serviceRootMigrationOptions) error {
	plan, err := s.validateServiceRootMigration(name, opts.NewRoot)
	if err != nil {
		return err
	}
	switch opts.Mode {
	case copyModeCopy:
		if err := s.copyServiceRootMigration(plan, opts.User); err != nil {
			return err
		}
	case copyModeEmpty:
		if err := os.Mkdir(plan.NewRoot, 0o755); err != nil {
			return fmt.Errorf("create service root %s: %w", plan.NewRoot, err)
		}
		if err := s.ensureDirsForRoot(plan.NewRoot, opts.User); err != nil {
			return err
		}
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
	return s.updateServiceRoot(name, plan.NewRoot)
}

func (s *Server) copyServiceRootMigration(plan serviceRootMigrationPlan, user string) error {
	stage, err := os.MkdirTemp(filepath.Dir(plan.NewRoot), ".yeet-migrate-"+plan.ServiceName+"-")
	if err != nil {
		return fmt.Errorf("create migration stage: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := copyServiceRootToStage(plan.OldRoot, stage); err != nil {
		return err
	}
	if err := s.ensureDirsForRoot(stage, user); err != nil {
		return err
	}
	if err := os.Rename(stage, plan.NewRoot); err != nil {
		return fmt.Errorf("move staged service root into place: %w", err)
	}
	removeStage = false
	return nil
}

func copyServiceRootToStage(srcRoot, stageRoot string) error {
	var buf bytes.Buffer
	if err := copyutil.TarDirectory(&buf, srcRoot, ""); err != nil {
		return fmt.Errorf("archive service root: %w", err)
	}
	if err := copyutil.ExtractTarWithOptions(&buf, stageRoot, copyutil.ExtractOptions{}); err != nil {
		return fmt.Errorf("extract service root archive: %w", err)
	}
	return nil
}

func (s *Server) updateServiceRoot(name, newRoot string) error {
	_, _, err := s.cfg.DB.MutateService(name, func(_ *db.Data, svc *db.Service) error {
		svc.ServiceRoot = newRoot
		return nil
	})
	return err
}
```

Add filesystem predicates:

```go
func rootIsMissingOrEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func rootsAreNested(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [ ] **Step 6: Run migration tests and commit**

Run:

```bash
go test ./pkg/catch -run 'TestServiceSetRoot|TestRemoveService' -count=1
go test ./pkg/catch ./pkg/copyutil -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch pkg/copyutil
git commit -m "pkg/catch: add service root migration command"
```

---

### Task 6: User Docs

**Files:**
- Modify: `README.md`
- Modify: website manual files that document `yeet run`, service management commands, and workflows
- Modify: website CLI reference files if generated or manually maintained

- [ ] **Step 1: Inspect local website instructions**

Run:

```bash
test ! -f website/AGENTS.md || sed -n '1,220p' website/AGENTS.md
rg -n "yeet run|service root|docker update|env set|remove" README.md website/docs website/src || true
```

Expected: locate the manual pages to update.

- [ ] **Step 2: Update docs**

Document:

```bash
yeet run vaultwarden ./compose.yml --service-root=/srv/apps/vaultwarden
yeet service set vaultwarden --service-root=/srv/apps/vaultwarden --copy
yeet service set vaultwarden --service-root=/srv/apps/vaultwarden --empty
```

Include these behavior points in user-facing language:

- `--service-root` is an absolute path on the catch host.
- The parent must already exist; yeet can create the final service directory.
- The root contains `bin`, `run`, `env`, and `data`.
- `yeet run` can set the initial root, but cannot move an existing service.
- Moving a root uses `yeet service set`, requires the service to be stopped, and leaves the old root in place.
- Non-interactive migration requires `--copy` or `--empty`.

- [ ] **Step 3: Run docs checks available locally**

Run:

```bash
pre-commit run --all-files
```

Expected: PASS. If the website has its own package scripts and dependencies are already installed, also run the relevant docs lint command shown by `website/AGENTS.md`.

- [ ] **Step 4: Commit docs**

If website files changed, commit inside the website submodule first:

```bash
cd website
git add .
git commit -m "docs: document service roots"
cd ..
git add website
```

Then commit root docs:

```bash
git add README.md
git commit -m "docs: document service roots"
```

If website files did not change, commit only the repo docs:

```bash
git add README.md
git commit -m "docs: document service roots"
```

---

### Task 7: Full Verification And Plan Completion

**Files:**
- Modify: `docs/superpowers/plans/2026-05-23-service-root.md`

- [ ] **Step 1: Run targeted verification**

Run:

```bash
go test ./pkg/db ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch ./pkg/copyutil ./pkg/svc ./pkg/catchrpc -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full tests**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Mark plan tasks complete**

Edit this plan and check off every completed task step. Do not mark a step done unless its command ran or its code change is present.

- [ ] **Step 5: Commit plan status**

Run:

```bash
git add docs/superpowers/plans/2026-05-23-service-root.md
git commit -m "docs: add service root implementation plan"
```

Expected: commit succeeds and `git status --short` is clean or contains only intentional uncommitted user changes.
