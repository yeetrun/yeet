# Host Storage Reconfiguration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add configurable catch host storage for first install and existing hosts: new installs default to `$HOME/yeet-data`, `yeet init` gets an interactive storage wizard, and `yeet host set` can reconfigure data dir and service root with optional ZFS and safe live migration.

**Architecture:** Keep catch authoritative for host storage state, service stop/move/start, ZFS validation, and daemon reinstallation. Keep yeet authoritative for interactive prompts, flag validation, display, and local project config updates. Add structured host-storage plan/apply RPCs, reuse existing per-service root migration machinery, and route both `yeet init` and `yeet host set` through the same request model.

**Tech Stack:** Go via `mise exec`, yargs CLI metadata, catch JSON-RPC/WebSocket transport, existing catch TTY forwarding, existing ZFS helper functions, Charm Huh for wizard prompts, GitButler for commits.

---

## File Map

- `go.mod`, `go.sum`: add `github.com/charmbracelet/huh` for first-install and host reconfiguration prompts.
- `pkg/catchrpc/types.go`, `pkg/catchrpc/client.go`, `pkg/catchrpc/*_test.go`: shared host storage request/plan/apply types and client methods.
- `pkg/cli/cli.go`, `pkg/cli/*_test.go`: add `host set` command metadata, flags, help text, and validation helpers.
- `cmd/yeet/yeet.go`, `cmd/yeet/cli_bridge.go`, `cmd/yeet/*_test.go`: route `yeet host set` as a host-level command without service-name parsing.
- `pkg/yeet/host_set.go`, `pkg/yeet/host_set_test.go`: parse host flags, prompt for missing choices, call catch plan/apply RPC, confirm disruption, and update local config when needed.
- `pkg/yeet/init_storage.go`, `pkg/yeet/init_storage_test.go`, `pkg/yeet/init.go`: first-install storage wizard, repeat-init detection, installer option plumbing.
- `cmd/catch/catch.go`, `cmd/catch/*_test.go`: default `--data-dir` to `$HOME/yeet-data`, accept explicit `--services-root`, and write both flags into the systemd unit when non-empty.
- `pkg/catch/host_storage.go`, `pkg/catch/host_storage_test.go`: host storage planning and application, data-dir move, services-root all-or-none migration, ZFS dataset creation.
- `pkg/catch/tty_service_set.go`, `pkg/catch/service_root_zfs.go`: extract reusable service-root migration helpers without changing per-service behavior.
- `pkg/catch/rpc.go`, `pkg/catch/authz.go`, `pkg/catch/*_test.go`: register host storage plan/apply RPCs and map them to `manage`.
- `README.md`, `website/docs/**/*.mdx`, `.codex/skills/yeet-cli/references/yeet-help-agent.md`: document the first-install wizard, `yeet host set`, migration modes, ZFS constraints, and local config effects.

---

## Implementation Tasks

### 1. Add shared host storage RPC types and client stubs

- [ ] Add failing tests in `pkg/catchrpc` for JSON method names and request round-tripping.

Create table tests that assert:

```go
func TestHostStorageSetRequestRoundTrip(t *testing.T) {
	req := catchrpc.HostStorageSetRequest{
		DataDir:         &catchrpc.HostStorageTarget{Value: "flash/yeet/data", ZFS: true},
		ServicesRoot:    &catchrpc.HostStorageTarget{Value: "flash/yeet/services", ZFS: true},
		MigrateServices: catchrpc.HostStorageMigrateAll,
		Yes:             true,
	}
	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(req))
	var got catchrpc.HostStorageSetRequest
	require.NoError(t, json.NewDecoder(&buf).Decode(&got))
	require.Equal(t, req, got)
}
```

Expected failure before implementation: undefined `HostStorageSetRequest` and constants.

- [ ] Implement the shared types in `pkg/catchrpc/types.go`.

Use explicit string enums so both CLI and catch can render stable errors:

```go
const (
	RPCMethodHostStoragePlan  = "catch.HostStoragePlan"
	RPCMethodHostStorageApply = "catch.HostStorageApply"
)

type HostStorageMigrateServices string

const (
	HostStorageMigratePrompt HostStorageMigrateServices = ""
	HostStorageMigrateAll    HostStorageMigrateServices = "all"
	HostStorageMigrateNone   HostStorageMigrateServices = "none"
)

type HostStorageTarget struct {
	Value string `json:"value"`
	ZFS   bool   `json:"zfs"`
}

type HostStorageSetRequest struct {
	DataDir         *HostStorageTarget          `json:"dataDir,omitempty"`
	ServicesRoot    *HostStorageTarget          `json:"servicesRoot,omitempty"`
	MigrateServices HostStorageMigrateServices  `json:"migrateServices,omitempty"`
	Yes             bool                        `json:"yes,omitempty"`
}

type HostStoragePlanRequest struct {
	Set HostStorageSetRequest `json:"set"`
}

type HostStorageApplyRequest struct {
	Plan HostStoragePlan `json:"plan"`
	Yes  bool            `json:"yes,omitempty"`
}
```

Plan response fields:

```go
type HostStoragePlan struct {
	Current          HostStorageState           `json:"current"`
	Desired          HostStorageState           `json:"desired"`
	DataDirAction    HostStorageDataDirAction   `json:"dataDirAction"`
	ServicesAction   HostStorageServicesAction  `json:"servicesAction"`
	Warnings         []string                   `json:"warnings,omitempty"`
	RequiresRestart  bool                       `json:"requiresRestart,omitempty"`
}

type HostStorageState struct {
	DataDir      string `json:"dataDir"`
	DataDirZFS   bool   `json:"dataDirZfs,omitempty"`
	ServicesRoot string `json:"servicesRoot"`
	ServicesZFS  bool   `json:"servicesZfs,omitempty"`
}

type HostStorageDataDirAction struct {
	Move bool   `json:"move"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type HostStorageServicesAction struct {
	Mode             HostStorageMigrateServices `json:"mode"`
	From             string                     `json:"from,omitempty"`
	To               string                     `json:"to,omitempty"`
	AffectedServices []HostStorageServiceMove   `json:"affectedServices,omitempty"`
}

type HostStorageServiceMove struct {
	Name       string `json:"name"`
	From       string `json:"from"`
	To         string `json:"to"`
	WasRunning bool   `json:"wasRunning"`
}
```

- [ ] Add typed client methods in `pkg/catchrpc/client.go`:

```go
func (c *Client) HostStoragePlan(ctx context.Context, req HostStoragePlanRequest) (HostStoragePlan, error) {
	var out HostStoragePlan
	err := c.Call(ctx, RPCMethodHostStoragePlan, req, &out)
	return out, err
}

func (c *Client) HostStorageApply(ctx context.Context, req HostStorageApplyRequest) (HostStorageApplyResult, error) {
	var out HostStorageApplyResult
	err := c.Call(ctx, RPCMethodHostStorageApply, req, &out)
	return out, err
}
```

- [ ] Run:

```sh
mise exec -- go test ./pkg/catchrpc
```

Expected output includes `ok` for `./pkg/catchrpc`.

- [ ] Commit this task with GitButler after inspecting only the files above:

```sh
but diff pkg/catchrpc/types.go pkg/catchrpc/client.go pkg/catchrpc
but commit codex/host-storage-reconfigure-design -m "catchrpc: add host storage RPC types"
```

### 2. Add CLI metadata and validation for `yeet host set`

- [ ] Add failing parser/metadata tests in `pkg/cli`.

Assert that `yeet host set --help` includes:

- `--data-dir`
- `--services-root`
- `--zfs`
- `--migrate-services`
- `--config`
- `--yes`

Assert validation rejects unsupported migration modes:

```go
func TestHostSetRejectsInvalidMigrateServices(t *testing.T) {
	_, _, err := cli.ParseHostSet([]string{"--migrate-services=some"})
	require.ErrorContains(t, err, "all or none")
}
```

Expected failure before implementation: command not registered.

- [ ] Add CLI structs, parser, and metadata in `pkg/cli/cli.go`.

Use a public normalized flag struct and a private parsed struct:

```go
type HostSetFlags struct {
	DataDir         string
	ServicesRoot    string
	ZFS             bool
	MigrateServices string
	Config          string
	Yes             bool
}

type hostSetFlagsParsed struct {
	DataDir         string `flag:"data-dir" help:"Set catch data directory path or ZFS dataset"`
	ServicesRoot    string `flag:"services-root" help:"Set default root for service directories or ZFS dataset prefix"`
	ZFS             bool   `flag:"zfs" help:"Treat supplied storage targets as ZFS datasets or dataset prefixes"`
	MigrateServices string `flag:"migrate-services" help:"Service migration mode: all, none"`
	Config          string `flag:"config" help:"Path to yeet.toml to update after service migration"`
	Yes             bool   `flag:"yes" short:"y" help:"Confirm disruptive host storage changes without prompting"`
}
```

- [ ] Add host group metadata to `remoteGroupInfos`:

```go
"host": {
	Name:        "host",
	Description: "Manage catch host settings",
	Commands: map[string]CommandInfo{
		"set": {
			Name:        "set",
			Description: "Configure catch host storage",
			Usage:       "host set [--data-dir=PATH_OR_DATASET] [--services-root=PATH_OR_DATASET_PREFIX] [--zfs] [--migrate-services=all|none] [--config=PATH] [--yes]",
			Examples: []string{
				"yeet host set --data-dir=$HOME/yeet-data",
				"yeet host set --services-root=$HOME/yeet-data/services2 --migrate-services=none",
				"yeet host set --zfs --data-dir=flash/yeet/data --services-root=flash/yeet/services --migrate-services=all",
			},
			FlagsSchema: hostSetFlagsParsed{},
		},
	},
},
```

- [ ] Add host flag specs to `remoteGroupFlagSpecs`:

```go
"host": {
	"set": flagSpecsFromStruct(hostSetFlagsParsed{}),
},
```

- [ ] Add parser and validation helpers:

```go
func ParseHostSet(args []string) (HostSetFlags, []string, error) {
	result, err := parseFlags[hostSetFlagsParsed](args)
	if err != nil {
		return HostSetFlags{}, nil, err
	}
	flags := HostSetFlags{
		DataDir:         strings.TrimSpace(result.Flags.DataDir),
		ServicesRoot:    strings.TrimSpace(result.Flags.ServicesRoot),
		ZFS:             result.Flags.ZFS,
		MigrateServices: strings.TrimSpace(result.Flags.MigrateServices),
		Config:          strings.TrimSpace(result.Flags.Config),
		Yes:             result.Flags.Yes,
	}
	if err := ValidateHostSetFlags(flags); err != nil {
		return HostSetFlags{}, nil, err
	}
	return flags, result.Args, nil
}

func ValidateHostSetFlags(f HostSetFlags) error {
	if f.DataDir == "" && f.ServicesRoot == "" {
		return fmt.Errorf("host set requires --data-dir, --services-root, or interactive input")
	}
	switch f.MigrateServices {
	case "", "all", "none":
		return nil
	default:
		return fmt.Errorf("--migrate-services must be all or none")
	}
}
```

The interactive path may pass empty targets only when stdin/stdout are TTYs; that TTY decision belongs in `pkg/yeet`, not `pkg/cli`.

- [ ] Run:

```sh
mise exec -- go test ./pkg/cli
```

Expected output includes `ok` for `./pkg/cli`.

- [ ] Commit:

```sh
but diff pkg/cli/cli.go pkg/cli
but commit codex/host-storage-reconfigure-design -m "cli: describe host storage command"
```

### 3. Route `yeet host set` on the client side

- [ ] Add tests for command dispatch in `cmd/yeet`.

Cover:

- `host set` is accepted as a top-level command.
- `host set` is not interpreted as a service command with service name `set`.
- host-level flags still respect `--host` and `CATCH_HOST`.

Use existing tests around `cmd/yeet/cli_test.go` and `cmd/yeet/cli_bridge_test.go` for matching setup and assertions.

- [ ] Register the group handler in `cmd/yeet/cli.go`.

Add a `host` group to `buildGroupHandlers`:

```go
"host": {
	Description: "Manage catch host settings",
	Commands: map[string]yargs.SubcommandHandler{
		"set": yeet.HandleHostSet,
	},
},
```

- [ ] Teach `cmd/yeet/cli_bridge.go` that `host` is host-level.

Add `host set` to the host-level command registry so it is not treated as a service command:

```go
var serviceBridgeHostLevelGroupCommands = map[string]map[string]struct{}{
	"host": {
		"set": {},
	},
	"vm": {
		"memory": {},
	},
}
```

- [ ] Add `pkg/yeet/host_set.go` with the command entrypoint:

```go
func HandleHostSet(ctx context.Context, args []string) error {
	flags, remaining, err := cli.ParseHostSet(args)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("unexpected host set args: %s", strings.Join(remaining, " "))
	}
	return runHostSet(ctx, flags)
}
```

- [ ] Run:

```sh
mise exec -- go test ./cmd/yeet ./pkg/yeet ./pkg/cli
```

Expected output includes `ok` for all three packages.

- [ ] Commit:

```sh
but diff cmd/yeet pkg/yeet pkg/cli
but commit codex/host-storage-reconfigure-design -m "cmd/yeet: route host set command"
```

### 4. Change catch startup defaults and service-root install flags

- [ ] Add tests around default path selection in `cmd/catch`.

Test cases:

- No `--data-dir` resolves to `$HOME/yeet-data`.
- `--data-dir /tmp/custom` keeps `/tmp/custom`.
- `--services-root /srv/yeet/services` sets `catch.Config.ServicesRoot` to that path.
- Missing `--services-root` keeps `$dataDir/services`.

Use `t.Setenv("HOME", home)` and a helper that returns resolved paths without starting the daemon.

- [ ] Replace the current `must.Get(filepath.Abs("data"))` default with a home-based default.

Add:

```go
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, "yeet-data")
	}
	abs, err := filepath.Abs("yeet-data")
	if err == nil {
		return abs
	}
	return "yeet-data"
}
```

Use it for the `--data-dir` flag default.

- [ ] Add `--services-root` to `cmd/catch/catch.go`.

Keep the zero value as "derive from data dir":

```go
servicesRoot := flag.String("services-root", "", "default root for service directories")
```

Update `newCatchConfig`:

```go
cfg := catch.Config{
	RootDir: dataDir,
}
if strings.TrimSpace(*servicesRoot) != "" {
	cfg.ServicesRoot = *servicesRoot
}
```

Keep `prepareDataDirs` creating `$dataDir/services` for compatibility, even when `ServicesRoot` is separate.

- [ ] Update `catchFileInstallerConfig` and systemd unit generation so explicit install choices write:

```sh
--data-dir=<resolved-data-dir>
--services-root=<resolved-services-root>
```

Do not write `--services-root` when it equals the derived `$dataDir/services`; this keeps upgraded units compact.

- [ ] Run:

```sh
mise exec -- go test ./cmd/catch ./pkg/catch
```

Expected output includes `ok` for both packages.

- [ ] Commit:

```sh
but diff cmd/catch pkg/catch
but commit codex/host-storage-reconfigure-design -m "catch: default data dir to yeet-data"
```

### 5. Implement catch-side host storage planning

- [ ] Add focused tests in `pkg/catch/host_storage_test.go`.

Cover planning without mutating the filesystem:

- Same data dir and services root produces no move actions.
- Changing only services root with `MigrateServices=all` lists services whose root is under the old host root and omits services with custom roots.
- Changing services root with `MigrateServices=none` returns no file moves and returns per-service persistence actions.
- `--zfs` with path-like values is rejected when `ZFS` is true.
- Missing ZFS datasets are marked for creation in the plan.

- [ ] Add `pkg/catch/host_storage.go`.

Start with pure planning functions:

```go
type hostStoragePlanner struct {
	config Config
	store  *db.Store
	fs     hostStorageFS
	zfs    zfsRunner
}

func (p *hostStoragePlanner) Plan(ctx context.Context, req catchrpc.HostStoragePlanRequest) (catchrpc.HostStoragePlan, error) {
	current := p.currentState()
	desired, err := p.resolveDesiredState(ctx, current, req.Set)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	services, err := p.planServiceRootChange(ctx, current, desired, req.Set.MigrateServices)
	if err != nil {
		return catchrpc.HostStoragePlan{}, err
	}
	return catchrpc.HostStoragePlan{
		Current:         current,
		Desired:         desired,
		DataDirAction:   planDataDirAction(current, desired),
		ServicesAction:  services,
		RequiresRestart: current.DataDir != desired.DataDir || current.ServicesRoot != desired.ServicesRoot,
	}, nil
}
```

- [ ] Add validation for homogeneous `--zfs` use.

Rules:

- If a target has `ZFS=true`, its value must be a ZFS dataset name, not an absolute or relative filesystem path.
- If both data dir and services root are supplied with `ZFS=true`, both must be datasets or dataset prefixes.
- Mixed filesystem and ZFS targets in one request return this user-facing error:

```text
mixed filesystem and ZFS storage changes must be run separately
```

Use `filepath.IsAbs`, leading `./`, leading `../`, and a simple dataset-name validator that rejects empty segments and path-cleaning changes. Keep deeper existence checks in the ZFS helper layer.

- [ ] Resolve ZFS targets using existing functions.

For data dir:

- Dataset value is the dataset to mount at the final data dir.
- If missing, plan a create action.
- Resolve mountpoint through `zfs get -H -o value mountpoint`.

For services root:

- Dataset value is a prefix such as `flash/yeet/services`.
- Each service path is resolved by appending the service name to the dataset prefix when migrating all.
- Host default root path is the mountpoint of the prefix dataset.

- [ ] Run:

```sh
mise exec -- go test ./pkg/catch -run 'TestHostStoragePlan|TestZFS'
```

Expected output includes passing host-storage planning tests and existing ZFS tests.

- [ ] Commit:

```sh
but diff pkg/catch/host_storage.go pkg/catch/host_storage_test.go pkg/catch/service_root_zfs.go
but commit codex/host-storage-reconfigure-design -m "catch: plan host storage changes"
```

### 6. Extract reusable service-root migration primitives

- [ ] Add regression tests for existing `yeet service set --service-root` behavior before extracting.

In `pkg/catch`, cover:

- Copy mode preserves service artifacts.
- Empty mode writes the new root without copying old content.
- Runtime changes still reinstall/restart as before.

Run the tests and confirm they pass before modifying code:

```sh
mise exec -- go test ./pkg/catch -run 'TestServiceRootMigration'
```

- [ ] Extract pure and filesystem helpers from `pkg/catch/tty_service_set.go`.

Move reusable helpers to `pkg/catch/service_root_migration.go`:

```go
func buildServiceRootMigrationPlan(ctx context.Context, cfg Config, svc db.Service, req serviceRootMigrationRequest) (serviceRootMigrationPlan, error)

func materializeServiceRootMigration(ctx context.Context, plan serviceRootMigrationPlan, w io.Writer) error

func applyServiceRootMigrationRuntimeChanges(ctx context.Context, cfg Config, before db.Service, after db.Service, w io.Writer) error
```

Keep `tty_service_set.go` as orchestration only: parse service-specific input, call extracted helpers, write TTY progress.

- [ ] Add batch migration helpers for host storage:

```go
type serviceRootBatchPlan struct {
	Moves []serviceRootMigrationPlan
}

func planServicesRootBatch(ctx context.Context, cfg Config, services []db.Service, oldRoot string, newRoot string, mode catchrpc.HostStorageMigrateServices) (serviceRootBatchPlan, error)
```

Rules:

- A service is affected only when `ServiceRoot` is empty or has the old default root prefix.
- A custom service root outside the old host root is skipped.
- In `none` mode, no files move; instead, the service receives an explicit root equal to its previous effective root.
- In `all` mode, every affected service gets a new root under the desired root. If any plan fails, return an error before stopping any service.

- [ ] Run:

```sh
mise exec -- go test ./pkg/catch -run 'TestServiceRootMigration|TestHostStorage'
```

Expected output includes passing per-service regression tests and host batch planning tests.

- [ ] Commit:

```sh
but diff pkg/catch/tty_service_set.go pkg/catch/service_root_migration.go pkg/catch/host_storage.go pkg/catch/*_test.go
but commit codex/host-storage-reconfigure-design -m "catch: reuse service root migration logic"
```

### 7. Implement catch-side host storage apply

- [ ] Add apply tests with fake service manager and fake filesystem.

Cover:

- `migrate all` stops all affected running services before moving files.
- A failed stop prevents file moves and restarts.
- A failed move leaves services stopped and returns exact recovery text.
- Successful migration restarts only services that were running before.
- `migrate none` writes explicit service roots and does not move service directories.
- Data-dir change copies catch state, rewrites installer config, schedules catch restart, and leaves old data in place.

- [ ] Add `ApplyHostStoragePlan` in `pkg/catch/host_storage.go`.

High-level sequence:

```go
func (a *hostStorageApplier) Apply(ctx context.Context, plan catchrpc.HostStoragePlan, yes bool, w io.Writer) (catchrpc.HostStorageApplyResult, error) {
	if err := a.validatePlanStillCurrent(plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.prepareZFS(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.stopAffectedServices(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.moveServiceRoots(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.applyServiceDBUpdates(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.moveDataDir(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.restartPreviouslyRunningServices(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	if err := a.reinstallCatchUnit(ctx, plan); err != nil {
		return catchrpc.HostStorageApplyResult{}, err
	}
	return a.scheduleCatchRestart(ctx, plan)
}
```

- [ ] Make data-dir move conservative.

Rules:

- Copy state to the new data dir; do not delete the old data dir.
- Refuse to copy into a non-empty directory unless it contains a compatible catch DB and registry.
- Install catch unit with new `--data-dir` and `--services-root` values.
- Restart any previously running migrated user services before catch restarts
  itself.
- Schedule catch restart after unit rewrite as the final server-side action.
  The reconnecting yeet client verifies `catch.Info` after the daemon returns.
- If client-side verification fails after reconnect, report a recovery message
  that names the old and new data dirs.

- [ ] Make service-root migration all-or-none.

Rules:

- Build and validate every per-service move first.
- Stop every affected service before moving any directory.
- If moving service N fails, do not attempt the remaining moves.
- Restart only services that were running before the operation after all DB/artifact updates have succeeded.
- For `none`, write explicit service roots so each affected service remains under the old root.

- [ ] Run:

```sh
mise exec -- go test ./pkg/catch -run 'TestHostStorageApply|TestServiceRootMigration'
```

Expected output includes passing apply tests and migration regressions.

- [ ] Commit:

```sh
but diff pkg/catch/host_storage.go pkg/catch/service_root_migration.go pkg/catch/*_test.go
but commit codex/host-storage-reconfigure-design -m "catch: apply host storage changes"
```

### 8. Expose host storage RPCs with manage permission

- [ ] Add RPC dispatch and authorization tests.

Cover:

- `catch.HostStoragePlan` maps to `manage`.
- `catch.HostStorageApply` maps to `manage`.
- A read-only token cannot call either method.
- A manage token can call plan and apply.

- [ ] Update `pkg/catch/rpc.go`.

Register:

```go
case catchrpc.RPCMethodHostStoragePlan:
	return s.handleHostStoragePlan(ctx, raw)
case catchrpc.RPCMethodHostStorageApply:
	return s.handleHostStorageApply(ctx, raw)
```

Handler shape:

```go
func (s *RPCServer) handleHostStoragePlan(ctx context.Context, raw json.RawMessage) (any, error) {
	var req catchrpc.HostStoragePlanRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return s.catch.PlanHostStorage(ctx, req)
}
```

- [ ] Update `pkg/catch/authz.go`.

Add both methods to the manage-required map. Do not classify these as `ssh`; catch is performing host mutation through its own daemon authority.

- [ ] Run:

```sh
mise exec -- go test ./pkg/catch ./pkg/catchrpc
```

Expected output includes `ok` for both packages.

- [ ] Commit:

```sh
but diff pkg/catch/rpc.go pkg/catch/authz.go pkg/catch pkg/catchrpc
but commit codex/host-storage-reconfigure-design -m "catch: expose host storage rpc"
```

### 9. Implement `yeet host set`

- [ ] Add client-side tests in `pkg/yeet/host_set_test.go`.

Cover:

- Non-TTY with no storage flags returns usage text.
- `--data-dir` only calls plan/apply with only `DataDir`.
- `--services-root --migrate-services=all --yes` skips confirmation and applies.
- `--zfs --data-dir /tmp/yeet` returns the mixed target error before RPC.
- `--zfs --data-dir flash/yeet/data --services-root flash/yeet/services` sends both targets as ZFS.
- When `--migrate-services` is omitted and TTY is available, the prompt asks all-or-none.
- When apply returns migrated services, local config is updated or exact `yeet service sync` commands are printed.

- [ ] Implement flag-to-request conversion:

```go
func hostSetRequestFromFlags(flags cli.HostSetFlags, interactive bool) (catchrpc.HostStorageSetRequest, error) {
	req := catchrpc.HostStorageSetRequest{
		MigrateServices: catchrpc.HostStorageMigrateServices(flags.MigrateServices),
		Yes:             flags.Yes,
	}
	if flags.DataDir != "" {
		req.DataDir = &catchrpc.HostStorageTarget{Value: flags.DataDir, ZFS: flags.ZFS}
	}
	if flags.ServicesRoot != "" {
		req.ServicesRoot = &catchrpc.HostStorageTarget{Value: flags.ServicesRoot, ZFS: flags.ZFS}
	}
	if req.DataDir == nil && req.ServicesRoot == nil && !interactive {
		return req, fmt.Errorf("host set requires --data-dir or --services-root when not running interactively")
	}
	return req, validateHostStorageTargets(req)
}
```

- [ ] Render the plan before confirmation.

Include:

- current and desired data dir
- current and desired services root
- ZFS create actions
- services that will stop and move
- services that will be pinned to old roots in `none` mode
- local `yeet.toml` path that will be updated after apply

- [ ] Confirm disruptive actions unless `--yes`.

Use existing confirmation style for consistency:

```text
This will stop 3 services, move their service roots, reinstall catch, and restart services that were running. Continue?
```

For TTY mode, use Huh confirm once Huh has been introduced by the init wizard task. Until then, keep this behind an internal confirm interface so tests do not depend on a terminal.

- [ ] Apply and update local config.

After `HostStorageApply` succeeds:

- If `--config` is supplied, load that file.
- If not supplied, load project config from current working directory using existing project config resolution.
- For each service moved to a new explicit root, update the matching service entry root.
- If a matching service entry is absent, print:

```text
Service <name> moved on the host but was not found in <config>. Run:
  yeet service sync <name> --config <config>
```

- [ ] Run:

```sh
mise exec -- go test ./pkg/yeet ./cmd/yeet ./pkg/cli
```

Expected output includes `ok` for all three packages.

- [ ] Commit:

```sh
but diff pkg/yeet cmd/yeet pkg/cli
but commit codex/host-storage-reconfigure-design -m "yeet: add host set command"
```

### 10. Add first-install storage wizard to `yeet init`

- [ ] Add `github.com/charmbracelet/huh` dependency.

Run:

```sh
mise exec -- go get github.com/charmbracelet/huh@latest
```

Verify `go.mod` and `go.sum` changed only for Charm dependencies and their transitive dependencies.

- [ ] Add tests for first-install versus upgrade behavior.

In `pkg/yeet/init_storage_test.go`, cover:

- New host with TTY calls storage wizard.
- Existing host upgrade preserves reported data dir and services root without prompts.
- New host without TTY uses `$HOME/yeet-data` and `$HOME/yeet-data/services`.
- If ZFS candidates exist, wizard offers filesystem defaults and ZFS choices.
- If data dir uses ZFS, service root suggestion starts from the same dataset family.

- [ ] Add a small prompt interface around Huh.

Keep Huh isolated so tests can use a fake prompt runner:

```go
type storagePromptRunner interface {
	RunStorageWizard(ctx context.Context, input initStorageInput) (initStorageSelection, error)
}

type huhStoragePromptRunner struct {
	in  io.Reader
	out io.Writer
}
```

- [ ] Implement the wizard in `pkg/yeet/init_storage.go`.

Wizard flow:

1. Show data dir choice with default `$HOME/yeet-data`.
2. If ZFS candidates exist, ask whether to use ZFS for the data dir.
3. If ZFS data dir is selected, ask for dataset value with suggestions such as `flash/yeet/data`.
4. Ask whether services should live under the data dir. Default answer is yes, with suggested root `$DATA_DIR/services`.
5. If services are separate and ZFS candidates exist, ask whether to use ZFS for service roots.
6. If service ZFS is selected, suggest a sibling dataset prefix such as `flash/yeet/services`.
7. Summarize selected data dir, services root, ZFS datasets that will be created, and ask to continue.

Use Huh groups and fields:

```go
form := huh.NewForm(
	huh.NewGroup(
		huh.NewInput().
			Title("Catch data directory").
			Value(&selection.DataDir).
			Suggestions([]string{defaultDataDir}),
	),
)
```

- [ ] Wire selection into install plan.

Extend the existing init/install option type with:

```go
type HostStorageInstallOptions struct {
	DataDir      string
	DataDirZFS   bool
	ServicesRoot string
	ServicesZFS  bool
}
```

Pass these options through the remote catch installer so first install writes the selected flags into the catch unit and creates needed datasets before daemon start.

- [ ] Preserve upgrade behavior.

Before prompting, determine whether catch already responds to `catch.Info` or the installer reports an existing catch unit. If existing, skip storage prompts and keep the current paths. A subsequent `yeet init` must not ask storage questions.

- [ ] Run:

```sh
mise exec -- go test ./pkg/yeet ./cmd/yeet ./cmd/catch ./pkg/catch
```

Expected output includes `ok` for all four packages.

- [ ] Commit:

```sh
but diff go.mod go.sum pkg/yeet cmd/yeet cmd/catch pkg/catch
but commit codex/host-storage-reconfigure-design -m "yeet: prompt for host storage on init"
```

### 11. Add one-off service migration command polish

- [ ] Review existing `yeet service set <svc> --service-root` behavior against the approved host-storage design.

Ensure the command has documented flags:

```text
--service-root PATH_OR_DATASET
--zfs
--copy
--empty
```

- [ ] Add or update tests so the one-off path stays independent from `yeet host set`.

Cover:

- A per-service ZFS migration can be run even when the host default root is filesystem-backed.
- Per-service filesystem migration can be run even when the host default root is ZFS-backed.
- Host-level mixed ZFS/filesystem requests still fail in one command.

- [ ] Update help text if needed so users can discover the one-off path outside the host flow:

```text
Use `yeet service set <service> --service-root ...` to migrate one service outside host-wide migration.
```

- [ ] Run:

```sh
mise exec -- go test ./pkg/cli ./pkg/yeet ./pkg/catch
```

Expected output includes `ok` for all three packages.

- [ ] Commit:

```sh
but diff pkg/cli pkg/yeet pkg/catch
but commit codex/host-storage-reconfigure-design -m "yeet: clarify one-off service root migration"
```

### 12. Update user documentation and generated help references

- [ ] Update README quickstart/init sections.

Document:

- New first-install default: `$HOME/yeet-data`.
- `yeet init` prompts on first install.
- Repeat `yeet init` preserves existing storage without asking.
- `yeet host set` changes host defaults.
- `yeet service set` migrates one service.

- [ ] Update website manual pages inside `website/`.

Likely files:

- `website/docs/getting-started*.mdx`
- `website/docs/reference*.mdx`
- `website/docs/guides*.mdx`

Use `yeetrun.com` for public examples and avoid private hostnames.

- [ ] Regenerate CLI help reference:

```sh
tools/generate-yeet-help-agent.sh
```

Verify `.codex/skills/yeet-cli/references/yeet-help-agent.md` includes `yeet host set`.

- [ ] Add docs tests or link checks if existing docs tooling exposes them.

At minimum run:

```sh
mise exec -- go test ./pkg/cli
```

Expected output includes `ok` for `./pkg/cli`.

- [ ] Commit website docs inside the website submodule first if files under `website/` changed.

Use the website repo's current branch and raw git only for the submodule repository if GitButler resolves back to the parent workspace:

```sh
git -C website status --short --branch
git -C website diff
git -C website commit -am "docs: describe host storage configuration"
```

Do not push the website commit unless the user asks for publication.

- [ ] Commit root docs and gitlink changes with GitButler:

```sh
but diff README.md .codex/skills/yeet-cli/references/yeet-help-agent.md website
but commit codex/host-storage-reconfigure-design -m "docs: document host storage configuration"
```

### 13. Run final verification

- [ ] Run targeted package tests for touched areas:

```sh
mise exec -- go test ./pkg/catchrpc ./pkg/cli ./cmd/yeet ./cmd/catch ./pkg/yeet ./pkg/catch
```

Expected output includes `ok` for every listed package.

- [ ] Run the full suite:

```sh
mise exec -- go test ./... -count=1
```

Expected output includes no failures.

- [ ] Run pre-commit:

```sh
mise exec -- pre-commit run --all-files
```

Expected output: every hook passes.

- [ ] Because this touches RPC, service orchestration, path handling, and host migration, run the destination quality gate before asking to land:

```sh
mise run quality:goal
```

Expected output: quality goal passes without lowering thresholds.

- [ ] Run manual smoke tests against a disposable local or VM catch host.

Minimum smoke cases:

```sh
mise exec -- go run ./cmd/yeet init root@host
mise exec -- go run ./cmd/yeet --host host info
mise exec -- go run ./cmd/yeet --host host host set --services-root "$HOME/yeet-data/services2" --migrate-services=none
mise exec -- go run ./cmd/yeet --host host host set --services-root "$HOME/yeet-data/services" --migrate-services=all
```

If ZFS is available on the disposable host:

```sh
mise exec -- go run ./cmd/yeet --host host host set --zfs --data-dir flash/yeet/data --services-root flash/yeet/services --migrate-services=all
```

Verify after each run:

```sh
mise exec -- go run ./cmd/yeet --host host info
mise exec -- go run ./cmd/yeet --host host status
```

- [ ] Capture known limitations in the final response.

Call out any skipped live ZFS smoke test, unavailable disposable host, or docs submodule publication status. Do not claim release readiness unless full verification and requested publication steps are complete.

---

## Acceptance Criteria

- New direct catch startup and new first install default to `$HOME/yeet-data`.
- First-time interactive `yeet init` asks storage questions; repeat init/upgrades keep current storage without asking.
- The wizard supports filesystem defaults, ZFS data dir, separate services root, and ZFS services dataset prefix.
- `yeet host set` supports `--data-dir`, `--services-root`, `--zfs`, `--migrate-services=all|none`, `--config`, and `--yes`.
- Host-level `--zfs` applies to every supplied target; mixed filesystem/ZFS changes in one command fail with a clear error.
- Services-root changes offer all-or-none migration.
- `all` stops affected services, moves roots, updates DB/artifacts/systemd/compose, and restarts services that were running.
- `none` leaves files in place and writes explicit service roots so services continue to live under the old root.
- Data-dir changes copy catch state, reinstall catch with the new path, schedule restart, verify `catch.Info` from the reconnecting client, and leave the old directory in place.
- Local `yeet.toml` is updated after host service migration when entries are present, and missing entries get exact `yeet service sync` guidance.
- `yeet service set <svc> --service-root ...` remains the one-off migration path.
- RPC permissions for host storage plan/apply are `manage` and are tested.
- README, website docs, CLI help metadata, and yeet help reference are synchronized.
