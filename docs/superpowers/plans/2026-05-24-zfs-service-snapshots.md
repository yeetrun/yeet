# ZFS Service Snapshots Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add automatic pre-change ZFS snapshots for ZFS-backed service roots, with catch-owned server defaults, per-service overrides, and bounded retention.

**Architecture:** Catch DB is authoritative for server defaults and service overrides. The yeet client parses user-facing flags, mirrors per-service overrides in `yeet.toml`, and forwards requests to catch; catch resolves the effective policy and runs ZFS commands before risky service mutations. Snapshot creation and pruning live in a focused catch-side unit that uses the existing `zfsCommandRunner` test seam.

**Tech Stack:** Go, yargs-based CLI parsing, catch RPC exec/PTY commands, JSON DB plus generated viewer accessors, OpenZFS CLI commands through `exec.CommandContext`, Go table-driven tests, README and website markdown docs.

---

## Execution Notes

- Work on `main`; the user explicitly requested main branch work earlier in this thread.
- The user has authorized commits in this thread. Do not push until the user explicitly asks for a push.
- Use TDD for behavior changes: write the failing test, run it, make it pass, rerun the relevant package tests, then commit.
- Use `superpowers:subagent-driven-development` for execution unless the user chooses inline execution.
- Do not run tasks in parallel when their write sets overlap. Task 1 must land before tasks that reference DB snapshot policy types.
- Read the local `AGENTS.md` before editing a package. At minimum, read `pkg/db/AGENTS.md`, `pkg/cli/AGENTS.md`, `pkg/catch/AGENTS.md`, `pkg/catchrpc/AGENTS.md`, `pkg/yeet/AGENTS.md`, `pkg/svc/AGENTS.md`, and `website/AGENTS.md` before changing those areas.
- `pkg/db/db_view.go` and `pkg/db/db_clone.go` are generated. Modify `pkg/db/db.go`, then run `go generate ./pkg/db`.
- Keep live hostnames and private paths out of committed docs. Use generic examples like `<host>` and `tank/apps/<svc>`.

## File Structure

- Modify `pkg/db/db.go`: add `SnapshotPolicy`, server defaults on `Data`, and service override on `Service`.
- Modify `pkg/db/migrate.go`: bump the DB data version from 7 to 8 and add a no-op migration that preserves existing services.
- Regenerate `pkg/db/db_view.go` and `pkg/db/db_clone.go`.
- Modify `pkg/db/db_test.go`: cover clone/view/migration for snapshot defaults and service overrides.
- Modify `pkg/catchrpc/types.go`: add wire types for snapshot policy, effective policy, and snapshot defaults responses.
- Modify `pkg/cli/cli.go`: add snapshot flag structs, parsers, command metadata, and flexible `service set` validation.
- Modify `cmd/yeet/cli.go`, `cmd/yeet/yeet.go`, and `cmd/yeet/cli_bridge.go`: add `snapshots defaults show/set` as a top-level no-service group and preserve `service set` bridging for snapshot flags.
- Modify `cmd/yeet/*_test.go` and `pkg/cli/cli_test.go`: cover help, parsing, and bridging for new commands and flags.
- Modify `pkg/yeet/project_config.go`: add per-service snapshot override fields to `yeet.toml`.
- Modify `pkg/yeet/svc_cmd.go`, `pkg/yeet/run_changes.go`, and `pkg/yeet/service_sync.go`: preserve, replay, save, and sync service snapshot overrides.
- Add `pkg/yeet/snapshots_cmd.go`: implement local client handling for `yeet snapshots defaults`.
- Modify `pkg/yeet/*_test.go`: cover `yeet.toml`, replay, local config updates, and sync behavior.
- Add `pkg/catch/service_snapshots.go`: implement policy resolution, duration parsing, snapshot naming, ZFS command construction, list parsing, and pruning.
- Add `pkg/catch/service_snapshots_test.go`: cover policy merge, parse, create, list, prune, and failure semantics without requiring ZFS.
- Modify `pkg/catch/tty_install.go`, `pkg/catch/installer_service.go`, `pkg/catch/tty_ops.go`, and `pkg/catch/tty_service_set.go`: call snapshot hooks before run installs, Docker updates, and ZFS source-root migrations.
- Modify `pkg/catch/service_info.go`: expose snapshot policy data for info and sync.
- Modify `pkg/catch/*_test.go`: cover hook ordering and command behavior.
- Modify README and website docs under `website/docs/`: document automatic ZFS snapshots, server defaults, service overrides, retention, and limitations.

---

### Task 1: DB Snapshot Policy Schema

**Files:**
- Read: `pkg/db/AGENTS.md`
- Modify: `pkg/db/db.go`
- Modify: `pkg/db/migrate.go`
- Regenerate: `pkg/db/db_view.go`
- Regenerate: `pkg/db/db_clone.go`
- Test: `pkg/db/db_test.go`

- [ ] **Step 1: Read DB instructions**

Run:

```bash
sed -n '1,220p' pkg/db/AGENTS.md
```

Expected: note that `Data` is persisted, migrations need tests, and view/clone helpers are generated.

- [ ] **Step 2: Write failing DB tests**

In `pkg/db/db_test.go`, add this test near the existing service-root clone/view tests:

```go
func TestSnapshotPolicyCloneAndView(t *testing.T) {
	enabled := false
	required := true
	keepLast := 3
	data := &Data{
		DataVersion: CurrentDataVersion,
		SnapshotDefaults: &SnapshotPolicy{
			Enabled:  boolPtr(enabled),
			KeepLast: intPtr(keepLast),
			MaxAge:   "72h",
			Events:   []string{"run", "docker-update"},
			Required: boolPtr(required),
		},
		Services: map[string]*Service{
			"svc": {
				Name: "svc",
				SnapshotPolicy: &SnapshotPolicy{
					Enabled: boolPtr(true),
					MaxAge:  "24h",
				},
			},
		},
	}

	clone := data.Clone()
	clone.SnapshotDefaults.MaxAge = "1h"
	clone.Services["svc"].SnapshotPolicy.MaxAge = "2h"
	if got := data.SnapshotDefaults.MaxAge; got != "72h" {
		t.Fatalf("source SnapshotDefaults.MaxAge mutated through clone: %q", got)
	}
	if got := data.Services["svc"].SnapshotPolicy.MaxAge; got != "24h" {
		t.Fatalf("source service SnapshotPolicy.MaxAge mutated through clone: %q", got)
	}

	view := data.View()
	if got := view.SnapshotDefaults().MaxAge(); got != "72h" {
		t.Fatalf("View SnapshotDefaults MaxAge = %q, want 72h", got)
	}
	sv, ok := view.Services().GetOk("svc")
	if !ok {
		t.Fatal("missing service view")
	}
	if got := sv.SnapshotPolicy().MaxAge(); got != "24h" {
		t.Fatalf("View service SnapshotPolicy MaxAge = %q, want 24h", got)
	}
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }
```

Add this migration test near the existing data-version tests:

```go
func TestMigrateAddsSnapshotPolicyVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.json")
	writeData(t, path, &Data{
		DataVersion: 7,
		Services: map[string]*Service{
			"svc": {Name: "svc", ServiceRoot: "/srv/apps/svc", ServiceRootZFS: "tank/apps/svc"},
		},
	})
	store := NewStore(path, filepath.Join(t.TempDir(), "services"))
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DataVersion() != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion(), CurrentDataVersion)
	}
	sv, ok := got.Services().GetOk("svc")
	if !ok {
		t.Fatal("missing migrated service")
	}
	if sv.SnapshotPolicy().Valid() {
		t.Fatalf("service SnapshotPolicy valid = true, want false for inherited policy")
	}
	onDisk := readData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	backup := readData(t, path+".v7.bak")
	if backup.DataVersion != 7 {
		t.Fatalf("backup DataVersion = %d, want 7", backup.DataVersion)
	}
}
```

- [ ] **Step 3: Run the DB tests and verify RED**

Run:

```bash
go test ./pkg/db -run 'Test(SnapshotPolicyCloneAndView|MigrateAddsSnapshotPolicyVersion)' -count=1
```

Expected: FAIL because `SnapshotPolicy`, `Data.SnapshotDefaults`, and `Service.SnapshotPolicy` do not exist.

- [ ] **Step 4: Add DB types and migration**

In `pkg/db/db.go`, add this type near `Service`:

```go
// SnapshotPolicy stores either server defaults or per-service overrides.
// Nil pointer fields mean inherit from the next policy layer.
type SnapshotPolicy struct {
	Enabled  *bool    `json:",omitempty"`
	KeepLast *int     `json:",omitempty"`
	MaxAge   string   `json:",omitempty"`
	Events   []string `json:",omitempty"`
	Required *bool    `json:",omitempty"`
}
```

Update the `go:generate` type list in `pkg/db/db.go` so generated view/clone helpers include `SnapshotPolicy`:

```go
//go:generate go run tailscale.com/cmd/viewer -type=Data,Service,SnapshotPolicy,Volume,ImageRepo,Artifact,DockerNetwork,DockerEndpoint,TailscaleNetwork,EndpointPort --copyright=false
```

Add this field to `Data`:

```go
	SnapshotDefaults *SnapshotPolicy `json:",omitempty"`
```

Add this field to `Service`:

```go
	// SnapshotPolicy overrides catch snapshot defaults for this service.
	// Nil means all snapshot settings inherit from server defaults.
	SnapshotPolicy *SnapshotPolicy `json:",omitempty"`
```

In `pkg/db/migrate.go`, change:

```go
const CurrentDataVersion = 8
```

Add the migrator entry:

```go
	7: addSnapshotPolicy,
```

Add this no-op migrator:

```go
func addSnapshotPolicy(d *Data) error {
	return nil
}
```

- [ ] **Step 5: Regenerate view/clone helpers**

Run:

```bash
go generate ./pkg/db
```

Expected: `pkg/db/db_view.go` has `SnapshotPolicyView` and `DataView.SnapshotDefaults()`, and `pkg/db/db_clone.go` deep-copies snapshot policy pointers and slices.

- [ ] **Step 6: Run DB tests**

Run:

```bash
go test ./pkg/db -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

Run:

```bash
git add pkg/db/db.go pkg/db/migrate.go pkg/db/db_view.go pkg/db/db_clone.go pkg/db/db_test.go
git commit -m "pkg/db: add snapshot policy state"
```

---

### Task 2: RPC And CLI Types

**Files:**
- Read: `pkg/catchrpc/AGENTS.md`
- Read: `pkg/cli/AGENTS.md`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/cli/cli.go`
- Test: `pkg/cli/cli_test.go`

- [ ] **Step 1: Read package instructions**

Run:

```bash
sed -n '1,220p' pkg/catchrpc/AGENTS.md
sed -n '1,220p' pkg/cli/AGENTS.md
```

Expected: note RPC compatibility and CLI parser test guidance.

- [ ] **Step 2: Write failing CLI parser tests**

In `pkg/cli/cli_test.go`, add tests for defaults commands:

```go
func TestParseSnapshotDefaultsSet(t *testing.T) {
	flags, args, err := ParseSnapshotDefaultsSet([]string{"--enabled=false", "--keep-last=3", "--max-age=72h", "--events=run,docker-update", "--required=false"})
	if err != nil {
		t.Fatalf("ParseSnapshotDefaultsSet: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
	if flags.Enabled != "false" || flags.KeepLast != "3" || flags.MaxAge != "72h" || flags.Events != "run,docker-update" || flags.Required != "false" {
		t.Fatalf("flags = %#v", flags)
	}
}

func TestParseSnapshotDefaultsShowRejectsArgs(t *testing.T) {
	if _, err := ParseSnapshotDefaultsShow([]string{"extra"}); err == nil || !strings.Contains(err.Error(), "snapshots defaults show takes no arguments") {
		t.Fatalf("ParseSnapshotDefaultsShow error = %v, want extra args error", err)
	}
}
```

Extend `TestParseServiceSet` or add a new table:

```go
func TestParseServiceSetSnapshotFlags(t *testing.T) {
	flags, args, err := ParseServiceSet([]string{"svc", "--snapshots=off", "--snapshot-keep-last=3", "--snapshot-max-age=72h", "--snapshot-required=false", "--snapshot-events=run"})
	if err != nil {
		t.Fatalf("ParseServiceSet: %v", err)
	}
	if len(args) != 1 || args[0] != "svc" {
		t.Fatalf("args = %#v, want svc", args)
	}
	if flags.Snapshots != "off" || flags.SnapshotKeepLast != "3" || flags.SnapshotMaxAge != "72h" || flags.SnapshotRequired != "false" || flags.SnapshotEvents != "run" {
		t.Fatalf("flags = %#v", flags)
	}
}

func TestParseServiceSetSnapshotOnlyDoesNotRequireServiceRoot(t *testing.T) {
	if _, _, err := ParseServiceSet([]string{"svc", "--snapshots=inherit"}); err != nil {
		t.Fatalf("ParseServiceSet snapshots-only: %v", err)
	}
}

func TestParseServiceSetRejectsEmptySnapshotMode(t *testing.T) {
	if _, _, err := ParseServiceSet([]string{"svc", "--snapshots="}); err == nil || !strings.Contains(err.Error(), "--snapshots must be on, off, or inherit") {
		t.Fatalf("ParseServiceSet error = %v, want snapshots value error", err)
	}
}

func TestParseRunSnapshotFlags(t *testing.T) {
	flags, args, err := ParseRun([]string{"--snapshots=off", "--snapshot-keep-last=3", "--snapshot-max-age=72h", "--snapshot-required=false", "payload.yml"})
	if err != nil {
		t.Fatalf("ParseRun: %v", err)
	}
	if flags.Snapshots != "off" || flags.SnapshotKeepLast != "3" || flags.SnapshotMaxAge != "72h" || flags.SnapshotRequired != "false" {
		t.Fatalf("flags = %#v", flags)
	}
	if !reflect.DeepEqual(args, []string{"payload.yml"}) {
		t.Fatalf("args = %#v", args)
	}
}
```

- [ ] **Step 3: Run CLI parser tests and verify RED**

Run:

```bash
go test ./pkg/cli -run 'TestParse(SnapshotDefaults|ServiceSetSnapshot)' -count=1
```

Expected: FAIL because parser types/functions do not exist and `ParseServiceSet` still requires `--service-root`.

- [ ] **Step 4: Add RPC snapshot wire types**

In `pkg/catchrpc/types.go`, add:

```go
type SnapshotPolicy struct {
	Enabled  *bool    `json:"enabled,omitempty"`
	KeepLast *int     `json:"keepLast,omitempty"`
	MaxAge   string   `json:"maxAge,omitempty"`
	Events   []string `json:"events,omitempty"`
	Required *bool    `json:"required,omitempty"`
}

type EffectiveSnapshotPolicy struct {
	Enabled  bool     `json:"enabled"`
	KeepLast int      `json:"keepLast"`
	MaxAge   string   `json:"maxAge"`
	Events   []string `json:"events,omitempty"`
	Required bool     `json:"required"`
}

type ServiceSnapshots struct {
	Override  *SnapshotPolicy          `json:"override,omitempty"`
	Effective EffectiveSnapshotPolicy `json:"effective,omitempty"`
}

type SnapshotDefaultsResponse struct {
	Defaults  SnapshotPolicy          `json:"defaults,omitempty"`
	Effective EffectiveSnapshotPolicy `json:"effective,omitempty"`
}
```

Add `Snapshots ServiceSnapshots` to `ServiceInfo`.

- [ ] **Step 5: Add CLI flag structs and parsers**

In `pkg/cli/cli.go`, extend `RunFlags` and `ServiceSetFlags`:

```go
	Snapshots          string
	SnapshotKeepLast   string
	SnapshotMaxAge     string
	SnapshotRequired   string
	SnapshotEvents     string
	SnapshotChange     bool
```

For `ServiceSetFlags`, use the same fields:

```go
	Snapshots          string
	SnapshotKeepLast   string
	SnapshotMaxAge     string
	SnapshotRequired   string
	SnapshotEvents     string
	SnapshotChange     bool
```

Add new flag structs:

```go
type SnapshotDefaultsSetFlags struct {
	Enabled  string
	KeepLast string
	MaxAge   string
	Events   string
	Required string
}

type snapshotDefaultsSetFlagsParsed struct {
	Enabled  string `flag:"enabled"`
	KeepLast string `flag:"keep-last"`
	MaxAge   string `flag:"max-age"`
	Events   string `flag:"events"`
	Required string `flag:"required"`
}
```

Extend `runFlagsParsed`:

```go
	Snapshots        string `flag:"snapshots"`
	SnapshotKeepLast string `flag:"snapshot-keep-last"`
	SnapshotMaxAge   string `flag:"snapshot-max-age"`
	SnapshotRequired string `flag:"snapshot-required"`
	SnapshotEvents   string `flag:"snapshot-events"`
```

Extend `serviceSetFlagsParsed`:

```go
	Snapshots        string `flag:"snapshots"`
	SnapshotKeepLast string `flag:"snapshot-keep-last"`
	SnapshotMaxAge   string `flag:"snapshot-max-age"`
	SnapshotRequired string `flag:"snapshot-required"`
	SnapshotEvents   string `flag:"snapshot-events"`
```

Add helper validation:

```go
func normalizeSnapshotMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "on", "off", "inherit":
		return value, nil
	default:
		return "", fmt.Errorf("--snapshots must be on, off, or inherit")
	}
}

func hasAnySnapshotServiceSetFlag(f serviceSetFlagsParsed) bool {
	return strings.TrimSpace(f.Snapshots) != "" ||
		strings.TrimSpace(f.SnapshotKeepLast) != "" ||
		strings.TrimSpace(f.SnapshotMaxAge) != "" ||
		strings.TrimSpace(f.SnapshotRequired) != "" ||
		strings.TrimSpace(f.SnapshotEvents) != ""
}

func hasAnySnapshotRunFlag(f runFlagsParsed) bool {
	return strings.TrimSpace(f.Snapshots) != "" ||
		strings.TrimSpace(f.SnapshotKeepLast) != "" ||
		strings.TrimSpace(f.SnapshotMaxAge) != "" ||
		strings.TrimSpace(f.SnapshotRequired) != "" ||
		strings.TrimSpace(f.SnapshotEvents) != ""
}
```

Change `ParseServiceSet` so root is required only for root changes:

```go
snapshotMode, err := normalizeSnapshotMode(parsed.Flags.Snapshots)
if err != nil {
	return ServiceSetFlags{}, nil, err
}
flags := ServiceSetFlags{
	ServiceRoot:       strings.TrimSpace(parsed.Flags.ServiceRoot),
	ZFS:               parsed.Flags.ZFS,
	Copy:              parsed.Flags.Copy,
	Empty:             parsed.Flags.Empty,
	Snapshots:         snapshotMode,
	SnapshotKeepLast:  strings.TrimSpace(parsed.Flags.SnapshotKeepLast),
	SnapshotMaxAge:    strings.TrimSpace(parsed.Flags.SnapshotMaxAge),
	SnapshotRequired:  strings.TrimSpace(parsed.Flags.SnapshotRequired),
	SnapshotEvents:    strings.TrimSpace(parsed.Flags.SnapshotEvents),
	SnapshotChange:    hasAnySnapshotServiceSetFlag(parsed.Flags),
}
rootChange := flags.ServiceRoot != "" || flags.ZFS
if rootChange && flags.ServiceRoot == "" {
	return ServiceSetFlags{}, nil, fmt.Errorf("--service-root is required when --zfs is set")
}
if rootChange && !flags.ZFS && !filepath.IsAbs(flags.ServiceRoot) {
	return ServiceSetFlags{}, nil, fmt.Errorf("--service-root must be absolute unless --zfs is set")
}
if !rootChange && !flags.SnapshotChange {
	return ServiceSetFlags{}, nil, fmt.Errorf("service set requires --service-root or snapshot settings")
}
if flags.Copy && flags.Empty {
	return ServiceSetFlags{}, nil, fmt.Errorf("cannot use --copy and --empty together")
}
```

In `ParseRun`, populate the snapshot fields from `runFlagsParsed`, normalize `--snapshots` with `normalizeSnapshotMode`, and set `SnapshotChange` with `hasAnySnapshotRunFlag(parsed.Flags)`.

Add parsers:

```go
func ParseSnapshotDefaultsShow(args []string) ([]string, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("snapshots defaults show takes no arguments")
	}
	return nil, nil
}

func ParseSnapshotDefaultsSet(args []string) (SnapshotDefaultsSetFlags, []string, error) {
	parsed, err := parseFlags[snapshotDefaultsSetFlagsParsed](args)
	if err != nil {
		return SnapshotDefaultsSetFlags{}, nil, err
	}
	flags := SnapshotDefaultsSetFlags{
		Enabled:  strings.TrimSpace(parsed.Flags.Enabled),
		KeepLast: strings.TrimSpace(parsed.Flags.KeepLast),
		MaxAge:   strings.TrimSpace(parsed.Flags.MaxAge),
		Events:   strings.TrimSpace(parsed.Flags.Events),
		Required: strings.TrimSpace(parsed.Flags.Required),
	}
	if flags == (SnapshotDefaultsSetFlags{}) {
		return SnapshotDefaultsSetFlags{}, nil, fmt.Errorf("snapshots defaults set requires at least one setting")
	}
	return flags, parsed.Args, nil
}
```

- [ ] **Step 6: Add command metadata**

In `remoteGroupInfos`, add:

```go
	"snapshots": {
		Name:        "snapshots",
		Description: "Manage catch ZFS snapshot defaults",
		Commands: map[string]CommandInfo{
			"defaults": {
				Name:        "defaults",
				Description: "Show or set catch snapshot defaults",
				Usage:       "snapshots defaults show | snapshots defaults set [--enabled=true|false] [--keep-last=N] [--max-age=7d] [--events=run,docker-update] [--required=true|false]",
				Examples: []string{
					"yeet snapshots defaults show",
					"yeet snapshots defaults set --enabled=false",
					"yeet snapshots defaults set --enabled=true --keep-last=5 --max-age=7d",
				},
			},
		},
	},
```

Add `remoteGroupFlagSpecs["snapshots"]["defaults"] = flagSpecsFromStruct(snapshotDefaultsSetFlagsParsed{})`.

Update `service set` usage/examples to include snapshot settings.

- [ ] **Step 7: Run parser tests and commit**

Run:

```bash
go test ./pkg/cli -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catchrpc/types.go pkg/cli/cli.go pkg/cli/cli_test.go
git commit -m "pkg/cli: add snapshot policy flags"
```

---

### Task 3: Client Routing And Project Config

**Files:**
- Read: `cmd/yeet/AGENTS.md`
- Read: `pkg/yeet/AGENTS.md`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/yeet.go`
- Modify: `cmd/yeet/cli_bridge.go`
- Modify: `pkg/yeet/project_config.go`
- Add: `pkg/yeet/snapshots_cmd.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/run_changes.go`
- Test: `cmd/yeet/cli_test.go`
- Test: `cmd/yeet/cli_bridge_test.go`
- Test: `pkg/yeet/project_config_test.go`
- Test: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Read package instructions**

Run:

```bash
sed -n '1,220p' cmd/yeet/AGENTS.md
sed -n '1,220p' pkg/yeet/AGENTS.md
```

Expected: note CLI routing and client orchestration guidance.

- [ ] **Step 2: Write failing routing tests**

In `cmd/yeet/cli_test.go`, add a help test using the existing `run()` pattern:

```go
func TestSnapshotsDefaultsHelpShowsSubcommands(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "snapshots", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("snapshots help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	if !strings.Contains(stdout, "Manage catch ZFS snapshot defaults") ||
		!strings.Contains(stdout, "defaults") {
		t.Fatalf("stdout = %q, want snapshots defaults help", stdout)
	}
}
```

Add a client handler test in a new `pkg/yeet/snapshots_cmd_test.go`:

```go
func TestHandleSvcSnapshotsDefaultsRoutesToSystemService(t *testing.T) {
	preserveSvcCommandGlobals(t)
	var gotService string
	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		return nil
	}
	req := svcCommandRequest{
		Command: svcCommand{
			Name:    "snapshots",
			Args:    []string{"defaults", "show"},
			RawArgs: []string{"snapshots", "defaults", "show"},
		},
	}
	if err := handleSvcSnapshots(context.Background(), req); err != nil {
		t.Fatalf("handleSvcSnapshots: %v", err)
	}
	if gotService != systemServiceName {
		t.Fatalf("service = %q, want %s", gotService, systemServiceName)
	}
	if !reflect.DeepEqual(gotArgs, []string{"snapshots", "defaults", "show"}) {
		t.Fatalf("args = %#v", gotArgs)
	}
}
```

In `cmd/yeet/cli_bridge_test.go`, add:

```go
func TestBridgeServiceArgsServiceSetSnapshotFlags(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	service, host, bridged, ok := bridgeServiceArgs(
		[]string{"service", "set", "--snapshots=off", "--snapshot-keep-last", "3", "sabnzbd"},
		remoteSpecs,
		groupSpecs,
		"",
	)
	if !ok || service != "sabnzbd" || host != "" {
		t.Fatalf("service=%q host=%q ok=%v", service, host, ok)
	}
	want := []string{"service", "set", "--snapshots=off", "--snapshot-keep-last", "3"}
	if !reflect.DeepEqual(bridged, want) {
		t.Fatalf("bridged = %#v, want %#v", bridged, want)
	}
}
```

- [ ] **Step 3: Write failing project config tests**

In `pkg/yeet/project_config_test.go`, add:

```go
func TestProjectConfigSnapshotFieldsRoundTrip(t *testing.T) {
	required := false
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:              "svc",
		Host:              "host-a",
		Type:              serviceTypeRun,
		Payload:           "compose.yml",
		ServiceRoot:       "tank/apps/svc",
		ServiceRootZFS:    true,
		Snapshots:         "off",
		SnapshotKeepLast:  3,
		SnapshotMaxAge:    "72h",
		SnapshotRequired:  &required,
		SnapshotEvents:    []string{"run"},
	})
	loc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), "yeet.toml"), Dir: t.TempDir(), Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	loaded, err := loadProjectConfigFromFile(loc.Path)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || *entry.SnapshotRequired || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) {
		t.Fatalf("entry snapshot fields = %#v", entry)
	}
}
```

In `pkg/yeet/svc_cmd_branch_test.go`, add a service-set local config test:

```go
func TestServiceSetUpdatesSnapshotConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return nil
	}
	isTerminalFn = func(int) bool { return false }
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "svc-a", Host: "host-a", Type: serviceTypeRun, Payload: "run.sh"})
	if err := HandleSvcCmd([]string{"service", "set", "--snapshots=off", "--snapshot-keep-last=3", "--snapshot-max-age=72h", "--snapshot-required=false"}); err != nil {
		t.Fatalf("HandleSvcCmd: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || *entry.SnapshotRequired {
		t.Fatalf("entry = %#v", entry)
	}
}
```

- [ ] **Step 4: Run client tests and verify RED**

Run:

```bash
go test ./cmd/yeet ./pkg/yeet -run 'Test(SnapshotsDefaults|BridgeServiceArgsServiceSetSnapshot|ProjectConfigSnapshot|ServiceSetUpdatesSnapshot)' -count=1
```

Expected: FAIL because routing and project config fields do not exist.

- [ ] **Step 5: Add project config fields and helpers**

In `pkg/yeet/project_config.go`, extend `ServiceEntry`:

```go
	Snapshots        string   `toml:"snapshots,omitempty"`
	SnapshotKeepLast int      `toml:"snapshot_keep_last,omitempty"`
	SnapshotMaxAge   string   `toml:"snapshot_max_age,omitempty"`
	SnapshotRequired *bool    `toml:"snapshot_required,omitempty"`
	SnapshotEvents   []string `toml:"snapshot_events,omitempty"`
```

Add helper methods near other config helpers:

```go
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
```

- [ ] **Step 6: Add snapshot defaults routing**

In `cmd/yeet/cli.go`, add a `snapshots` group:

```go
		"snapshots": {
			Description: "Manage catch ZFS snapshot defaults",
			Commands: map[string]yargs.SubcommandHandler{
				"defaults": handleSnapshotsGroup,
			},
		},
```

In `cmd/yeet/yeet.go`, add:

```go
func handleSnapshotsGroup(ctx context.Context, args []string) error {
	full := append([]string{"snapshots"}, args...)
	yeet.SetServiceOverride(yeet.SystemServiceName())
	return handleRemote(ctx, full)
}
```

In `pkg/yeet/service.go`, add the exported helper used by the CLI package:

```go
func SystemServiceName() string {
	return systemServiceName
}
```

In `cmd/yeet/cli_bridge.go`, add `snapshots` as a local no-bridge group so service argument extraction never treats `show` or `set` as a service:

```go
	"snapshots": {
		"defaults": {},
	},
```

- [ ] **Step 7: Add client command handler for snapshots defaults**

Add `pkg/yeet/snapshots_cmd.go`:

```go
package yeet

import (
	"context"
	"fmt"

	"github.com/yeetrun/yeet/pkg/cli"
)

func handleSvcSnapshots(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) < 2 || req.Command.Args[0] != "defaults" {
		return handleSvcRemote(ctx, req)
	}
	switch req.Command.Args[1] {
	case "show":
		if _, err := cli.ParseSnapshotDefaultsShow(req.Command.Args[2:]); err != nil {
			return err
		}
	case "set":
		if _, _, err := cli.ParseSnapshotDefaultsSet(req.Command.Args[2:]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown snapshots defaults command %q", req.Command.Args[1])
	}
	return execRemoteFn(ctx, systemServiceName, req.Command.RawArgs, nil, false)
}
```

Add handler registration in `svcCommandHandlers`:

```go
	"snapshots": func(ctx context.Context, req svcCommandRequest) error {
		return handleSvcSnapshots(ctx, req)
	},
```

- [ ] **Step 8: Preserve snapshot overrides during run replay and service set**

In `pkg/yeet/run_changes.go`, add snapshot options to stored args:

```go
type snapshotOptions struct {
	Snapshots        string
	KeepLast         int
	MaxAge           string
	Required         *bool
	Events           []string
}

func runArgsWithSnapshotOptions(args []string, opts snapshotOptions) []string {
	out := append([]string{}, args...)
	if opts.Snapshots != "" {
		out = append([]string{"--snapshots=" + opts.Snapshots}, out...)
	}
	if opts.KeepLast != 0 {
		out = append([]string{fmt.Sprintf("--snapshot-keep-last=%d", opts.KeepLast)}, out...)
	}
	if opts.MaxAge != "" {
		out = append([]string{"--snapshot-max-age=" + opts.MaxAge}, out...)
	}
	if opts.Required != nil {
		out = append([]string{fmt.Sprintf("--snapshot-required=%t", *opts.Required)}, out...)
	}
	if len(opts.Events) != 0 {
		out = append([]string{"--snapshot-events=" + strings.Join(opts.Events, ",")}, out...)
	}
	return out
}
```

Update `storedArgs` construction in `runWithChangesTo`:

```go
storedArgs := runArgsWithServiceRootOptions(entry.Args, serviceRootOptions{Root: entry.ServiceRoot, ZFS: entry.ServiceRootZFS})
storedArgs = runArgsWithSnapshotOptions(storedArgs, snapshotOptions{
	Snapshots: entry.Snapshots,
	KeepLast:  entry.SnapshotKeepLast,
	MaxAge:    entry.SnapshotMaxAge,
	Required:  entry.SnapshotRequired,
	Events:    entry.SnapshotEvents,
})
```

In `pkg/yeet/svc_cmd.go`, extend `svcRunControlFlags` and `parsedSvcRun` with snapshot fields:

```go
	Snapshots        string
	SnapshotKeepLast int
	SnapshotMaxAge   string
	SnapshotRequired *bool
	SnapshotEvents   []string
	SnapshotChange   bool
```

In `parseSvcRunControlFlags`, copy the snapshot fields from `cli.ParseRun`:

```go
snapshotRequired, err := parseOptionalBoolFlag(flags.SnapshotRequired, "--snapshot-required")
if err != nil {
	return svcRunControlFlags{}, err
}
snapshotKeepLast, err := parseOptionalPositiveIntFlag(flags.SnapshotKeepLast, "--snapshot-keep-last")
if err != nil {
	return svcRunControlFlags{}, err
}
return svcRunControlFlags{
	Args:              filteredArgs,
	EnvFileArg:        envFileArg,
	EnvFileSet:        envFileSet,
	ServiceRootArg:    rootOpts.Root,
	ServiceRootZFSArg: rootOpts.ZFS,
	ServiceRootSet:    serviceRootSet,
	ForceDeploy:       forceDeploy,
	Snapshots:         flags.Snapshots,
	SnapshotKeepLast:  snapshotKeepLast,
	SnapshotMaxAge:    flags.SnapshotMaxAge,
	SnapshotRequired:  snapshotRequired,
	SnapshotEvents:    splitSnapshotEventList(flags.SnapshotEvents),
	SnapshotChange:    flags.SnapshotChange,
}, nil
```

Add parse helpers near the other run-control helpers:

```go
func parseOptionalBoolFlag(raw, name string) (*bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s value %q", name, raw)
	}
	return &v, nil
}

func parseOptionalPositiveIntFlag(raw, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}
```

When a stored entry has snapshot overrides and explicit run flags do not set snapshot fields, rehydrate them into the remote run args:

```go
snapOpts := snapshotOptions{
	Snapshots: entry.Snapshots,
	KeepLast:  entry.SnapshotKeepLast,
	MaxAge:    entry.SnapshotMaxAge,
	Required:  entry.SnapshotRequired,
	Events:    entry.SnapshotEvents,
}
if flags.SnapshotChange {
	snapOpts = snapshotOptions{
		Snapshots: flags.Snapshots,
		KeepLast:  flags.SnapshotKeepLast,
		MaxAge:    flags.SnapshotMaxAge,
		Required:  flags.SnapshotRequired,
		Events:    flags.SnapshotEvents,
	}
}
filteredArgs = runArgsWithSnapshotOptions(filteredArgs, snapOpts)
```

In `pkg/yeet/svc_cmd.go`, replace `saveServiceSetConfig` with a helper that updates root fields only when `flags.ServiceRoot != ""`, updates snapshot fields when `flags.SnapshotChange`, and clears snapshot fields when `flags.Snapshots == "inherit"`.

Use this core update:

```go
func applyServiceSetConfigFlags(entry *ServiceEntry, flags cli.ServiceSetFlags) error {
	if strings.TrimSpace(flags.ServiceRoot) != "" {
		entry.ServiceRoot = strings.TrimSpace(flags.ServiceRoot)
		entry.ServiceRootZFS = flags.ZFS
	}
	if !flags.SnapshotChange {
		return nil
	}
	if flags.Snapshots == "inherit" {
		entry.ClearSnapshotOverride()
		return nil
	}
	if flags.Snapshots != "" {
		entry.Snapshots = flags.Snapshots
	}
	if flags.SnapshotKeepLast != "" {
		n, err := strconv.Atoi(flags.SnapshotKeepLast)
		if err != nil || n < 1 {
			return fmt.Errorf("--snapshot-keep-last must be a positive integer")
		}
		entry.SnapshotKeepLast = n
	}
	if flags.SnapshotMaxAge != "" {
		entry.SnapshotMaxAge = flags.SnapshotMaxAge
	}
	if flags.SnapshotRequired != "" {
		v, err := strconv.ParseBool(flags.SnapshotRequired)
		if err != nil {
			return fmt.Errorf("invalid --snapshot-required value %q", flags.SnapshotRequired)
		}
		entry.SnapshotRequired = &v
	}
	if flags.SnapshotEvents != "" {
		entry.SnapshotEvents = splitSnapshotEventList(flags.SnapshotEvents)
	}
	return nil
}
```

Update `saveRunConfig` so it preserves an existing entry's snapshot override fields unless the current run explicitly supplied snapshot flags. Use this merge before `SetServiceEntry`:

```go
if existing, ok := loc.Config.ServiceEntry(serviceOverride, host); ok {
	entry.Snapshots = existing.Snapshots
	entry.SnapshotKeepLast = existing.SnapshotKeepLast
	entry.SnapshotMaxAge = existing.SnapshotMaxAge
	if existing.SnapshotRequired != nil {
		required := *existing.SnapshotRequired
		entry.SnapshotRequired = &required
	}
	entry.SnapshotEvents = append([]string{}, existing.SnapshotEvents...)
}
if snapshotChange {
	entry.Snapshots = snapshotOpts.Snapshots
	entry.SnapshotKeepLast = snapshotOpts.KeepLast
	entry.SnapshotMaxAge = snapshotOpts.MaxAge
	entry.SnapshotRequired = snapshotOpts.Required
	entry.SnapshotEvents = append([]string{}, snapshotOpts.Events...)
}
```

- [ ] **Step 9: Run client tests and commit**

Run:

```bash
go test ./cmd/yeet ./pkg/yeet -count=1
```

Expected: PASS.

Commit:

```bash
git add cmd/yeet/cli.go cmd/yeet/yeet.go cmd/yeet/cli_bridge.go cmd/yeet/*_test.go pkg/yeet/project_config.go pkg/yeet/snapshots_cmd.go pkg/yeet/svc_cmd.go pkg/yeet/run_changes.go pkg/yeet/*_test.go
git commit -m "pkg/yeet: route snapshot policy commands"
```

---

### Task 4: Catch Snapshot Policy Engine

**Files:**
- Read: `pkg/catch/AGENTS.md`
- Add: `pkg/catch/service_snapshots.go`
- Test: `pkg/catch/service_snapshots_test.go`

- [ ] **Step 1: Read catch instructions**

Run:

```bash
sed -n '1,220p' pkg/catch/AGENTS.md
```

Expected: note catch command behavior should be testable through stubs.

- [ ] **Step 2: Write failing policy tests**

Create `pkg/catch/service_snapshots_test.go` with tests for defaults, overrides, duration parsing, and event checks:

```go
func TestEffectiveSnapshotPolicyDefaults(t *testing.T) {
	got, err := effectiveSnapshotPolicy(nil, nil)
	if err != nil {
		t.Fatalf("effectiveSnapshotPolicy: %v", err)
	}
	if !got.Enabled || got.KeepLast != 5 || got.MaxAge != 7*24*time.Hour || !got.Required {
		t.Fatalf("policy = %#v", got)
	}
	if !got.Allows(snapshotEventRun) || !got.Allows(snapshotEventDockerUpdate) || !got.Allows(snapshotEventServiceRootMigration) {
		t.Fatalf("default events = %#v", got.Events)
	}
}

func TestEffectiveSnapshotPolicyServiceOverride(t *testing.T) {
	enabled := false
	keep := 3
	required := false
	got, err := effectiveSnapshotPolicy(&db.SnapshotPolicy{Enabled: boolPtr(true), KeepLast: intPtr(8), MaxAge: "14d"}, &db.SnapshotPolicy{
		Enabled:  &enabled,
		KeepLast: &keep,
		MaxAge:   "72h",
		Events:   []string{"run"},
		Required: &required,
	})
	if err != nil {
		t.Fatalf("effectiveSnapshotPolicy: %v", err)
	}
	if got.Enabled || got.KeepLast != 3 || got.MaxAge != 72*time.Hour || got.Required {
		t.Fatalf("policy = %#v", got)
	}
	if !got.Allows(snapshotEventRun) || got.Allows(snapshotEventDockerUpdate) {
		t.Fatalf("events = %#v", got.Events)
	}
}

func TestParseSnapshotMaxAge(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr string
	}{
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "72h", want: 72 * time.Hour},
		{in: "0", wantErr: "must be positive"},
		{in: "bad", wantErr: "invalid snapshot max age"},
	}
	for _, tt := range tests {
		got, err := parseSnapshotMaxAge(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parseSnapshotMaxAge(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("parseSnapshotMaxAge(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
}
```

- [ ] **Step 3: Write failing ZFS command and prune tests**

Add tests to the same file:

```go
func TestCreateServiceSnapshotCommand(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "", nil
	}
	req := snapshotCreateRequest{
		Service:    "svc-a",
		Dataset:    "tank/apps/svc-a",
		Event:      snapshotEventDockerUpdate,
		Generation: 12,
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}
	name, err := createServiceSnapshot(context.Background(), runner, req)
	if err != nil {
		t.Fatalf("createServiceSnapshot: %v", err)
	}
	if name != "tank/apps/svc-a@yeet-20260524T184233Z-docker-update-g12" {
		t.Fatalf("snapshot = %q", name)
	}
	wantPrefix := []string{
		"snapshot",
		"-o", "com.yeetrun:created-by=catch",
		"-o", "com.yeetrun:service=svc-a",
		"-o", "com.yeetrun:event=docker-update",
		"-o", "com.yeetrun:generation=12",
		"-o", "com.yeetrun:policy-version=1",
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0][:len(wantPrefix)], wantPrefix) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestPruneSnapshotSelection(t *testing.T) {
	now := time.Date(2026, 5, 24, 20, 0, 0, 0, time.UTC)
	snaps := []listedSnapshot{
		{Name: "tank/apps/svc@yeet-old", Created: now.Add(-8 * 24 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@manual", Created: now.Add(-30 * 24 * time.Hour), CreatedBy: "", Service: ""},
		{Name: "tank/apps/svc@yeet-new-1", Created: now.Add(-1 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-2", Created: now.Add(-2 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-3", Created: now.Add(-3 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-4", Created: now.Add(-4 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-5", Created: now.Add(-5 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-6", Created: now.Add(-6 * time.Hour), CreatedBy: "catch", Service: "svc"},
	}
	got := snapshotsToPrune(snaps, effectivePolicy{KeepLast: 5, MaxAge: 7 * 24 * time.Hour}, now, "tank/apps/svc@yeet-new-1")
	want := []string{"tank/apps/svc@yeet-old", "tank/apps/svc@yeet-new-6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshotsToPrune = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 4: Run snapshot tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'Test(EffectiveSnapshotPolicy|ParseSnapshotMaxAge|CreateServiceSnapshot|PruneSnapshot)' -count=1
```

Expected: FAIL because `service_snapshots.go` does not exist.

- [ ] **Step 5: Implement policy and command helpers**

Create `pkg/catch/service_snapshots.go` with these core declarations:

```go
package catch

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

type snapshotEvent string

const (
	snapshotEventRun                  snapshotEvent = "run"
	snapshotEventDockerUpdate         snapshotEvent = "docker-update"
	snapshotEventServiceRootMigration snapshotEvent = "service-root-migration"
	defaultSnapshotMaxAge                           = 7 * 24 * time.Hour
	defaultSnapshotKeepLast                         = 5
)

type effectivePolicy struct {
	Enabled  bool
	KeepLast int
	MaxAge   time.Duration
	Events   map[snapshotEvent]struct{}
	Required bool
}

func (p effectivePolicy) Allows(event snapshotEvent) bool {
	_, ok := p.Events[event]
	return ok
}
```

Implement `effectiveSnapshotPolicy(server, service *db.SnapshotPolicy) (effectivePolicy, error)` by:

1. Starting with built-in defaults.
2. Applying non-nil server fields.
3. Applying non-nil service fields.
4. Parsing `MaxAge` with `parseSnapshotMaxAge`.
5. Validating `KeepLast >= 1` when enabled.
6. Validating events are one of the three constants.

Implement `parseSnapshotMaxAge`:

```go
func parseSnapshotMaxAge(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultSnapshotMaxAge, nil
	}
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid snapshot max age %q", raw)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid snapshot max age %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("snapshot max age must be positive")
	}
	return d, nil
}
```

Implement snapshot creation using argument slices, never shell strings:

```go
type snapshotCreateRequest struct {
	Service    string
	Dataset    string
	Event      snapshotEvent
	Generation int
	Now        time.Time
}

func createServiceSnapshot(ctx context.Context, runner zfsCommandRunner, req snapshotCreateRequest) (string, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	name := req.Dataset + "@" + snapshotShortName(req)
	args := []string{
		"snapshot",
		"-o", "com.yeetrun:created-by=catch",
		"-o", "com.yeetrun:service=" + req.Service,
		"-o", "com.yeetrun:event=" + string(req.Event),
		"-o", fmt.Sprintf("com.yeetrun:generation=%d", req.Generation),
		"-o", "com.yeetrun:policy-version=1",
		name,
	}
	_, stderr, err := runner(ctx, args...)
	if err != nil {
		return "", formatZFSCommandError("zfs snapshot "+name, stderr, err)
	}
	return name, nil
}

var snapshotNameCleaner = regexp.MustCompile(`[^A-Za-z0-9_.:-]+`)

func snapshotShortName(req snapshotCreateRequest) string {
	event := snapshotNameCleaner.ReplaceAllString(string(req.Event), "_")
	return fmt.Sprintf("yeet-%s-%s-g%d", req.Now.UTC().Format("20060102T150405Z"), event, req.Generation)
}
```

- [ ] **Step 6: Implement list and prune helpers**

Add:

```go
type listedSnapshot struct {
	Name      string
	Created   time.Time
	CreatedBy string
	Service   string
}

func listServiceSnapshots(ctx context.Context, runner zfsCommandRunner, dataset string) ([]listedSnapshot, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	stdout, stderr, err := runner(ctx, "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,com.yeetrun:created-by,com.yeetrun:service", "-s", "creation", dataset)
	if err != nil {
		return nil, formatZFSCommandError("zfs list snapshots "+dataset, stderr, err)
	}
	return parseListedSnapshots(stdout)
}

func parseListedSnapshots(raw string) ([]listedSnapshot, error) {
	var out []listedSnapshot
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("invalid zfs snapshot list row %q", line)
		}
		epoch, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid zfs snapshot creation %q: %w", fields[1], err)
		}
		out = append(out, listedSnapshot{
			Name:      fields[0],
			Created:   time.Unix(epoch, 0).UTC(),
			CreatedBy: zfsPropertyValue(fields[2]),
			Service:   zfsPropertyValue(fields[3]),
		})
	}
	return out, nil
}

func zfsPropertyValue(raw string) string {
	if raw == "-" {
		return ""
	}
	return raw
}
```

Add prune selection:

```go
func snapshotsToPrune(snaps []listedSnapshot, policy effectivePolicy, now time.Time, current string) []string {
	owned := make([]listedSnapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap.Name == current {
			continue
		}
		if !strings.Contains(snap.Name, "@yeet-") || snap.CreatedBy != "catch" {
			continue
		}
		owned = append(owned, snap)
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Created.After(owned[j].Created) })
	var prune []string
	for i, snap := range owned {
		outsideKeep := i >= policy.KeepLast
		olderThanMax := now.Sub(snap.Created) > policy.MaxAge
		if outsideKeep || olderThanMax {
			prune = append(prune, snap.Name)
		}
	}
	return prune
}
```

When calling `snapshotsToPrune` from a service-specific path, filter by `snap.Service == service` before destroying.

Add destroy:

```go
func destroySnapshot(ctx context.Context, runner zfsCommandRunner, name string) error {
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "destroy", name)
	if err != nil {
		return formatZFSCommandError("zfs destroy "+name, stderr, err)
	}
	return nil
}
```

- [ ] **Step 7: Run snapshot engine tests and commit**

Run:

```bash
go test ./pkg/catch -run 'Test(EffectiveSnapshotPolicy|ParseSnapshotMaxAge|CreateServiceSnapshot|PruneSnapshot)' -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch/service_snapshots.go pkg/catch/service_snapshots_test.go
git commit -m "pkg/catch: add zfs snapshot policy engine"
```

---

### Task 5: Catch Snapshot Commands And Service Info

**Files:**
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/service_info.go`
- Test: `pkg/catch/tty_ops_test.go`
- Test: `pkg/catch/service_info_test.go`

- [ ] **Step 1: Write failing command tests**

In `pkg/catch/tty_ops_test.go`, add:

```go
func TestSnapshotsDefaultsShow(t *testing.T) {
	server := newTestServer(t)
	keep := 3
	enabled := false
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.SnapshotDefaults = &db.SnapshotPolicy{Enabled: &enabled, KeepLast: &keep, MaxAge: "72h"}
		return nil
	}); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"defaults", "show"}); err != nil {
		t.Fatalf("snapshotsCmdFunc: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "enabled = false") || !strings.Contains(got, "keep_last = 3") || !strings.Contains(got, "max_age = \"72h\"") {
		t.Fatalf("output = %q", got)
	}
}

func TestSnapshotsDefaultsSetPersistsPolicy(t *testing.T) {
	server := newTestServer(t)
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	err := execer.snapshotsCmdFunc([]string{"defaults", "set", "--enabled=false", "--keep-last=3", "--max-age=72h", "--required=false"})
	if err != nil {
		t.Fatalf("snapshotsCmdFunc: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	def := dv.SnapshotDefaults()
	if !def.Valid() || def.Enabled().Get() || def.KeepLast().Get() != 3 || def.MaxAge() != "72h" || def.Required().Get() {
		t.Fatalf("defaults = %#v", def.AsStruct())
	}
}
```

In `pkg/catch/service_info_test.go`, add:

```go
func TestServiceInfoIncludesSnapshotPolicy(t *testing.T) {
	server := newTestServer(t)
	enabled := false
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"svc-info": {
			Name:           "svc-info",
			ServiceRoot:    "/tank/apps/svc-info",
			ServiceRootZFS: "tank/apps/svc-info",
			SnapshotPolicy: &db.SnapshotPolicy{Enabled: &enabled, MaxAge: "72h"},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	resp, err := server.serviceInfo("svc-info")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Info.Snapshots.Override == nil || resp.Info.Snapshots.Override.Enabled == nil || *resp.Info.Snapshots.Override.Enabled {
		t.Fatalf("override = %#v", resp.Info.Snapshots.Override)
	}
	if resp.Info.Snapshots.Effective.MaxAge != "72h" {
		t.Fatalf("effective = %#v", resp.Info.Snapshots.Effective)
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'Test(SnapshotsDefaults|ServiceInfoIncludesSnapshotPolicy)' -count=1
```

Expected: FAIL because `snapshotsCmdFunc` and service info snapshot fields do not exist.

- [ ] **Step 3: Add snapshots command dispatch**

In `pkg/catch/tty_exec.go`, add:

```go
	"snapshots": func(e *ttyExecer, args []string) error {
		return e.snapshotsCmdFunc(args)
	},
```

In `pkg/catch/tty_ops.go`, add:

```go
func (e *ttyExecer) snapshotsCmdFunc(args []string) error {
	if len(args) < 2 || args[0] != "defaults" {
		return fmt.Errorf("snapshots requires defaults show or defaults set")
	}
	switch args[1] {
	case "show":
		if _, err := cli.ParseSnapshotDefaultsShow(args[2:]); err != nil {
			return err
		}
		return e.showSnapshotDefaults()
	case "set":
		flags, rest, err := cli.ParseSnapshotDefaultsSet(args[2:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("unexpected snapshots defaults args: %s", strings.Join(rest, " "))
		}
		return e.setSnapshotDefaults(flags)
	default:
		return fmt.Errorf("unknown snapshots defaults command %q", args[1])
	}
}
```

Add `showSnapshotDefaults` to read DB defaults, resolve effective policy, and print:

```text
enabled = true
keep_last = 5
max_age = "7d"
events = ["run", "docker-update", "service-root-migration"]
required = true
```

Add `setSnapshotDefaults` to parse and persist fields. Use these parser helpers:

```go
func applySnapshotDefaultsFlags(policy *db.SnapshotPolicy, flags cli.SnapshotDefaultsSetFlags) error {
	if policy == nil {
		policy = &db.SnapshotPolicy{}
	}
	if flags.Enabled != "" {
		v, err := strconv.ParseBool(flags.Enabled)
		if err != nil {
			return fmt.Errorf("invalid --enabled value %q", flags.Enabled)
		}
		policy.Enabled = &v
	}
	if flags.KeepLast != "" {
		n, err := strconv.Atoi(flags.KeepLast)
		if err != nil || n < 1 {
			return fmt.Errorf("--keep-last must be a positive integer")
		}
		policy.KeepLast = &n
	}
	if flags.MaxAge != "" {
		if _, err := parseSnapshotMaxAge(flags.MaxAge); err != nil {
			return err
		}
		policy.MaxAge = flags.MaxAge
	}
	if flags.Required != "" {
		v, err := strconv.ParseBool(flags.Required)
		if err != nil {
			return fmt.Errorf("invalid --required value %q", flags.Required)
		}
		policy.Required = &v
	}
	if flags.Events != "" {
		events, err := parseSnapshotEvents(flags.Events)
		if err != nil {
			return err
		}
		policy.Events = events
	}
	return nil
}
```

- [ ] **Step 4: Add service info conversion**

In `pkg/catch/service_info.go`, set `info.Snapshots`:

```go
snapshots, err := s.serviceSnapshotInfo(dv, sv)
if err != nil {
	return resp, err
}
info.Snapshots = snapshots
```

Add:

```go
func (s *Server) serviceSnapshotInfo(dv *db.DataView, sv db.ServiceView) (catchrpc.ServiceSnapshots, error) {
	serverPolicy := snapshotPolicyPtrFromView(dv.SnapshotDefaults())
	servicePolicy := snapshotPolicyPtrFromView(sv.SnapshotPolicy())
	effective, err := effectiveSnapshotPolicy(serverPolicy, servicePolicy)
	if err != nil {
		return catchrpc.ServiceSnapshots{}, err
	}
	return catchrpc.ServiceSnapshots{
		Override:  snapshotPolicyRPC(servicePolicy),
		Effective: effectiveSnapshotPolicyRPC(effective),
	}, nil
}
```

Implement `snapshotPolicyPtrFromView`, `snapshotPolicyRPC`, and `effectiveSnapshotPolicyRPC` by copying pointer values into fresh pointers so callers cannot mutate DB state.

- [ ] **Step 5: Run command/info tests and commit**

Run:

```bash
go test ./pkg/catch -run 'Test(SnapshotsDefaults|ServiceInfoIncludesSnapshotPolicy)' -count=1
go test ./pkg/catch -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch/tty_exec.go pkg/catch/tty_ops.go pkg/catch/tty_ops_test.go pkg/catch/service_info.go pkg/catch/service_info_test.go
git commit -m "pkg/catch: expose snapshot policy settings"
```

---

### Task 6: Service Set And Run Snapshot Overrides

**Files:**
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/installer_file.go`
- Test: `pkg/catch/tty_service_set_test.go`
- Test: `pkg/catch/tty_install_test.go`
- Test: `pkg/catch/installer_file_test.go`
- Modify: `pkg/yeet/service_sync.go`
- Test: `pkg/yeet/service_sync_test.go`

- [ ] **Step 1: Write failing catch service-set tests**

In `pkg/catch/tty_service_set_test.go`, add:

```go
func TestServiceSetSnapshotOnlyDoesNotRequireStoppedService(t *testing.T) {
	server := newTestServer(t)
	name := "svc-snap"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {Name: name, ServiceRoot: "/srv/apps/svc-snap"},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	oldRunning := isServiceRunningForRootMigration
	defer func() { isServiceRunningForRootMigration = oldRunning }()
	isServiceRunningForRootMigration = func(*Server, string) (bool, error) {
		return true, nil
	}
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Snapshots: "off", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, _ := dv.Services().GetOk(name)
	if got := sv.SnapshotPolicy().Enabled().Get(); got {
		t.Fatalf("snapshot enabled = true, want false")
	}
}

func TestServiceSetSnapshotInheritClearsOverride(t *testing.T) {
	server := newTestServer(t)
	name := "svc-snap"
	enabled := false
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {Name: name, SnapshotPolicy: &db.SnapshotPolicy{Enabled: &enabled, MaxAge: "72h"}},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Snapshots: "inherit", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc: %v", err)
	}
	dv, _ := server.cfg.DB.Get()
	sv, _ := dv.Services().GetOk(name)
	if sv.SnapshotPolicy().Valid() {
		t.Fatalf("SnapshotPolicy valid = true, want false")
	}
}
```

- [ ] **Step 2: Write failing service sync tests**

In `pkg/yeet/service_sync_test.go`, add:

```go
func TestServiceSyncMirrorsSnapshotOverride(t *testing.T) {
	preserveServiceSyncGlobals(t)
	tmp := useTempSvcCwd(t)
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "host-a", Type: serviceTypeRun, Payload: "compose.yml"})
	enabled := false
	keep := 3
	required := false
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			Name: service,
			Paths: catchrpc.ServicePaths{ServiceRootZFS: "tank/apps/sonarr"},
			Snapshots: catchrpc.ServiceSnapshots{
				Override: &catchrpc.SnapshotPolicy{Enabled: &enabled, KeepLast: &keep, MaxAge: "72h", Required: &required},
			},
		}}, nil
	}
	if err := handleServiceSync(context.Background(), svcCommandRequest{Config: mustLoadProjectConfig(t), Service: "sonarr"}); err != nil {
		t.Fatalf("handleServiceSync: %v", err)
	}
	loaded, _ := loadProjectConfigFromCwd()
	entry, _ := loaded.Config.ServiceEntry("sonarr", "host-a")
	if entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || *entry.SnapshotRequired {
		t.Fatalf("entry = %#v", entry)
	}
}
```

- [ ] **Step 3: Write failing run persistence tests**

In `pkg/catch/tty_install_test.go`, add a test beside `TestRunCmdFuncCopiesServiceRootIntoInstallerConfig`:

```go
func TestRunCmdFuncCopiesSnapshotPolicyIntoInstallerConfig(t *testing.T) {
	var gotCfg FileInstallerCfg
	execer := &ttyExecer{
		s:              newTestServer(t),
		sn:             "svc-run-snapshot",
		rawRW:          bytes.NewBufferString("binary-payload"),
		rw:             &bytes.Buffer{},
		bypassPtyInput: true,
		installFunc: func(_ string, _ io.Reader, cfg FileInstallerCfg) error {
			gotCfg = cfg
			return nil
		},
	}

	flags := cli.RunFlags{Snapshots: "off", SnapshotKeepLast: "3", SnapshotMaxAge: "72h", SnapshotRequired: "false", SnapshotChange: true}
	if err := execer.runCmdFunc(flags, nil); err != nil {
		t.Fatalf("runCmdFunc returned error: %v", err)
	}
	if gotCfg.SnapshotPolicy == nil || gotCfg.SnapshotPolicy.Enabled == nil || *gotCfg.SnapshotPolicy.Enabled {
		t.Fatalf("SnapshotPolicy Enabled = %#v, want false", gotCfg.SnapshotPolicy)
	}
	if gotCfg.SnapshotPolicy.KeepLast == nil || *gotCfg.SnapshotPolicy.KeepLast != 3 || gotCfg.SnapshotPolicy.MaxAge != "72h" {
		t.Fatalf("SnapshotPolicy = %#v", gotCfg.SnapshotPolicy)
	}
	if gotCfg.SnapshotPolicy.Required == nil || *gotCfg.SnapshotPolicy.Required {
		t.Fatalf("SnapshotPolicy Required = %#v, want false", gotCfg.SnapshotPolicy.Required)
	}
}
```

In `pkg/catch/installer_file_test.go`, add:

```go
func TestNewFileInstallerPersistsSnapshotPolicy(t *testing.T) {
	server := newTestServer(t)
	enabled := false
	keep := 3
	required := false
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		ServiceName: "svc-snapshot-policy",
		NoBinary:    true,
		SnapshotPolicy: &db.SnapshotPolicy{
			Enabled:  &enabled,
			KeepLast: &keep,
			MaxAge:   "72h",
			Required: &required,
		},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, ok := dv.Services().GetOk("svc-snapshot-policy")
	if !ok {
		t.Fatal("missing service")
	}
	if sv.SnapshotPolicy().Enabled().Get() || sv.SnapshotPolicy().KeepLast().Get() != 3 || sv.SnapshotPolicy().MaxAge() != "72h" || sv.SnapshotPolicy().Required().Get() {
		t.Fatalf("SnapshotPolicy = %#v", sv.SnapshotPolicy().AsStruct())
	}
}
```

- [ ] **Step 4: Run tests and verify RED**

Run:

```bash
go test ./pkg/catch ./pkg/yeet -run 'Test(ServiceSetSnapshot|ServiceSyncMirrorsSnapshot|RunCmdFuncCopiesSnapshot|NewFileInstallerPersistsSnapshot)' -count=1
```

Expected: FAIL because service-set update, run config persistence, and sync logic do not handle snapshot policies.

- [ ] **Step 5: Implement catch service-set override updates**

In `pkg/catch/tty_service_set.go`, split service set handling:

```go
func (e *ttyExecer) serviceSetCmdFunc(flags cli.ServiceSetFlags) error {
	rootChange := strings.TrimSpace(flags.ServiceRoot) != "" || flags.ZFS
	if rootChange {
		if err := e.serviceSetRoot(flags); err != nil {
			return err
		}
	}
	if flags.SnapshotChange {
		return e.s.updateServiceSnapshotPolicy(e.sn, flags)
	}
	if !rootChange {
		return fmt.Errorf("service set requires --service-root or snapshot settings")
	}
	return nil
}
```

Move the current root migration body into `serviceSetRoot`.

Add:

```go
func (s *Server) updateServiceSnapshotPolicy(name string, flags cli.ServiceSetFlags) error {
	_, _, err := s.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		if flags.Snapshots == "inherit" {
			service.SnapshotPolicy = nil
			return nil
		}
		policy := service.SnapshotPolicy
		if policy == nil {
			policy = &db.SnapshotPolicy{}
		}
		if err := applyServiceSnapshotFlags(policy, flags); err != nil {
			return err
		}
		service.SnapshotPolicy = policy
		return nil
	})
	return err
}
```

Implement `applyServiceSnapshotFlags` with the same validation rules as server defaults, but accept `inherit` for individual fields by clearing pointers or slices:

```go
func applyServiceSnapshotFlags(policy *db.SnapshotPolicy, flags cli.ServiceSetFlags) error {
	if flags.Snapshots == "on" {
		v := true
		policy.Enabled = &v
	}
	if flags.Snapshots == "off" {
		v := false
		policy.Enabled = &v
	}
	if flags.SnapshotKeepLast != "" {
		if flags.SnapshotKeepLast == "inherit" {
			policy.KeepLast = nil
		} else {
			n, err := strconv.Atoi(flags.SnapshotKeepLast)
			if err != nil || n < 1 {
				return fmt.Errorf("--snapshot-keep-last must be a positive integer or inherit")
			}
			policy.KeepLast = &n
		}
	}
	if flags.SnapshotMaxAge != "" {
		if flags.SnapshotMaxAge == "inherit" {
			policy.MaxAge = ""
		} else if _, err := parseSnapshotMaxAge(flags.SnapshotMaxAge); err != nil {
			return err
		} else {
			policy.MaxAge = flags.SnapshotMaxAge
		}
	}
	if flags.SnapshotRequired != "" {
		if flags.SnapshotRequired == "inherit" {
			policy.Required = nil
		} else {
			v, err := strconv.ParseBool(flags.SnapshotRequired)
			if err != nil {
				return fmt.Errorf("invalid --snapshot-required value %q", flags.SnapshotRequired)
			}
			policy.Required = &v
		}
	}
	if flags.SnapshotEvents != "" {
		if flags.SnapshotEvents == "inherit" {
			policy.Events = nil
		} else {
			events, err := parseSnapshotEvents(flags.SnapshotEvents)
			if err != nil {
				return err
			}
			policy.Events = events
		}
	}
	return nil
}
```

- [ ] **Step 6: Implement run override persistence**

In `pkg/catch/installer_file.go`, add to `FileInstallerCfg`:

```go
	SnapshotPolicy *db.SnapshotPolicy
```

In `pkg/catch/tty_install.go`, set it in `runCmdFunc`:

```go
policy, err := snapshotPolicyFromRunFlags(flags)
if err != nil {
	return err
}
cfg.SnapshotPolicy = policy
```

Add:

```go
func snapshotPolicyFromRunFlags(flags cli.RunFlags) (*db.SnapshotPolicy, error) {
	if !flags.SnapshotChange {
		return nil, nil
	}
	policy := &db.SnapshotPolicy{}
	setFlags := cli.ServiceSetFlags{
		Snapshots:        flags.Snapshots,
		SnapshotKeepLast: flags.SnapshotKeepLast,
		SnapshotMaxAge:   flags.SnapshotMaxAge,
		SnapshotRequired: flags.SnapshotRequired,
		SnapshotEvents:   flags.SnapshotEvents,
		SnapshotChange:   true,
	}
	if err := applyServiceSnapshotFlags(policy, setFlags); err != nil {
		return nil, err
	}
	return policy, nil
}
```

In `pkg/catch/installer_file.go`, update `applyInstallPlanToService` so run-supplied overrides persist:

```go
if i.cfg.SnapshotPolicy != nil {
	s.SnapshotPolicy = i.cfg.SnapshotPolicy.Clone()
}
```

- [ ] **Step 7: Implement service sync mirroring**

In `pkg/yeet/service_sync.go`, extend `serviceSyncResult` with snapshot fields:

```go
	Snapshots        string
	SnapshotKeepLast int
	SnapshotMaxAge   string
	SnapshotRequired *bool
	SnapshotEvents   []string
```

After syncing service root, call `SetSnapshotFieldsForEntry` on project config. Add a helper in `project_config.go`:

```go
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
```

- [ ] **Step 8: Run tests and commit**

Run:

```bash
go test ./pkg/catch ./pkg/yeet -run 'Test(ServiceSetSnapshot|ServiceSyncMirrorsSnapshot|RunCmdFuncCopiesSnapshot|NewFileInstallerPersistsSnapshot)' -count=1
go test ./pkg/catch ./pkg/yeet -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch/tty_service_set.go pkg/catch/tty_service_set_test.go pkg/catch/tty_install.go pkg/catch/tty_install_test.go pkg/catch/installer_file.go pkg/catch/installer_file_test.go pkg/yeet/service_sync.go pkg/yeet/service_sync_test.go pkg/yeet/project_config.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go
git commit -m "service: support snapshot policy overrides"
```

---

### Task 7: Hook Snapshots Into Risky Operations

**Files:**
- Modify: `pkg/catch/installer_service.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_service_set.go`
- Test: `pkg/catch/installer_service_test.go`
- Test: `pkg/catch/tty_ops_test.go`
- Test: `pkg/catch/tty_service_set_test.go`

- [ ] **Step 1: Write failing run hook test**

In `pkg/catch/installer_service_test.go`, add:

```go
func TestInstallGenSnapshotsBeforeDockerInstall(t *testing.T) {
	server := newTestServer(t)
	var calls [][]string
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		if args[0] == "snapshot" {
			return "", "", nil
		}
		if args[0] == "list" {
			return "", "", nil
		}
		return "", "", nil
	}
	installer := &Installer{
		s: server,
		icfg: InstallerCfg{ServiceName: "api", Pull: false},
	}
	service := &db.Service{
		Name:             "api",
		ServiceType:      db.ServiceTypeDockerCompose,
		ServiceRoot:      filepath.Join(t.TempDir(), "api"),
		ServiceRootZFS:   "tank/apps/api",
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"staged": filepath.Join(t.TempDir(), "compose.yml")}},
		},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": service}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	if err := os.WriteFile(service.Artifacts[db.ArtifactDockerComposeFile].Refs["staged"], []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	oldRunInstallPhase := runInstallPhaseForSnapshot
	t.Cleanup(func() { runInstallPhaseForSnapshot = oldRunInstallPhase })
	runInstallPhaseForSnapshot = func(_ *Installer, _ *db.Service) error {
		if len(calls) == 0 || calls[0][0] != "snapshot" {
			t.Fatalf("snapshot was not first call: %#v", calls)
		}
		return nil
	}
	if err := installer.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
}
```

- [ ] **Step 2: Write failing Docker update hook test**

In `pkg/catch/tty_ops_test.go`, add:

```go
func TestDockerUpdateSnapshotsBeforeComposeUpdate(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"api": {
			Name:           "api",
			ServiceType:    db.ServiceTypeDockerCompose,
			ServiceRoot:    root,
			ServiceRootZFS: "tank/apps/api",
			Generation:     2,
			Artifacts: db.ArtifactStore{
				db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(2): filepath.Join(root, "compose.yml")}},
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	var calls []string
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, args[0])
		return "", "", nil
	}
	oldDockerComposeUpdate := dockerComposeUpdate
	t.Cleanup(func() { dockerComposeUpdate = oldDockerComposeUpdate })
	dockerComposeUpdate = func(*svc.DockerComposeService) error {
		if len(calls) == 0 || calls[0] != "snapshot" {
			t.Fatalf("snapshot did not happen before update: %#v", calls)
		}
		return nil
	}
	execer := &ttyExecer{s: server, sn: "api", rw: &bytes.Buffer{}, ctx: context.Background()}
	if err := execer.dockerUpdateCmdFunc(); err != nil {
		t.Fatalf("dockerUpdateCmdFunc: %v", err)
	}
}
```

- [ ] **Step 3: Write failing migration hook test**

In `pkg/catch/tty_service_set_test.go`, add:

```go
func TestServiceRootMigrationSnapshotsOldZFSDataset(t *testing.T) {
	server := newTestServer(t)
	oldRoot := t.TempDir()
	newRoot := filepath.Join(t.TempDir(), "new")
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"svc": {Name: "svc", ServiceRoot: oldRoot, ServiceRootZFS: "tank/apps/svc"},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	var snapshotted bool
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		if args[0] == "snapshot" {
			snapshotted = true
			return "", "", nil
		}
		if args[0] == "list" {
			return "", "", nil
		}
		return "", "", nil
	}
	plan := serviceRootMigrationPlan{ServiceName: "svc", OldRoot: oldRoot, OldRootZFS: "tank/apps/svc", NewRoot: newRoot}
	if err := server.migrateServiceRootWithPlan(plan, serviceRootMigrationEmpty); err != nil {
		t.Fatalf("migrateServiceRootWithPlan: %v", err)
	}
	if !snapshotted {
		t.Fatal("expected old ZFS dataset snapshot before migration")
	}
}
```

- [ ] **Step 4: Run hook tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'Test(InstallGenSnapshots|DockerUpdateSnapshots|ServiceRootMigrationSnapshots)' -count=1
```

Expected: FAIL because hook calls do not exist.

- [ ] **Step 5: Add a shared snapshot wrapper**

In `pkg/catch/service_snapshots.go`, add:

```go
type snapshotOperation struct {
	Service    *db.Service
	Event      snapshotEvent
	Writer     io.Writer
	Operation  func() error
}

func (s *Server) withServiceSnapshot(ctx context.Context, op snapshotOperation) error {
	if op.Service == nil || strings.TrimSpace(op.Service.ServiceRootZFS) == "" {
		return op.Operation()
	}
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	serverPolicy := snapshotPolicyPtrFromView(dv.SnapshotDefaults())
	policy, err := effectiveSnapshotPolicy(serverPolicy, op.Service.SnapshotPolicy)
	if err != nil {
		return err
	}
	if !policy.Enabled || !policy.Allows(op.Event) {
		return op.Operation()
	}
	snapshotName, err := createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service:    op.Service.Name,
		Dataset:    op.Service.ServiceRootZFS,
		Event:      op.Event,
		Generation: op.Service.Generation,
		Now:        time.Now(),
	})
	if err != nil {
		if policy.Required {
			return err
		}
		writeSnapshotWarning(op.Writer, "warning: zfs snapshot failed: %v\n", err)
	} else {
		writeSnapshotWarning(op.Writer, "Created ZFS snapshot %s\n", snapshotName)
	}
	opErr := op.Operation()
	if snapshotName != "" {
		if err := s.pruneServiceSnapshots(ctx, op.Service, policy, snapshotName); err != nil {
			writeSnapshotWarning(op.Writer, "warning: zfs snapshot prune failed: %v\n", err)
		}
	}
	if opErr != nil && snapshotName != "" {
		writeSnapshotWarning(op.Writer, "Recovery snapshot: %s\n", snapshotName)
	}
	return opErr
}
```

Implement `writeSnapshotWarning` as a nil-safe `fmt.Fprintf`.

- [ ] **Step 6: Hook run install**

In `pkg/catch/installer_service.go`, add the test seam near the other package-level service helpers:

```go
var runInstallPhaseForSnapshot = (*Installer).runInstallPhase
```

Then wrap the install phase in `doInstall`:

```go
if err := si.s.withServiceSnapshot(context.Background(), snapshotOperation{
	Service:   s,
	Event:     snapshotEventRun,
	Writer:    si.icfg.Printer,
	Operation: func() error { return runInstallPhaseForSnapshot(si, s) },
}); err != nil {
	return err
}
```

Keep brand-new service skip behavior by having `withServiceSnapshot` skip when `Generation == 1 && LatestGeneration == 1` or by checking whether there was a previous generation before `commitGen`.

- [ ] **Step 7: Hook Docker update**

In `pkg/catch/tty_ops.go`, add the test seam near `dockerUpdateCmdFunc`:

```go
var dockerComposeUpdate = (*svc.DockerComposeService).Update
```

Then load the service before running the update:

```go
sv, err := e.s.serviceView(e.sn)
if err != nil {
	ui.FailStep(err.Error())
	return err
}
service := sv.AsStruct()
err = e.s.withServiceSnapshot(e.ctx, snapshotOperation{
	Service: service,
	Event:   snapshotEventDockerUpdate,
	Writer:  e.rw,
	Operation: func() error {
		return dockerComposeUpdate(docker)
	},
})
if err != nil {
	ui.FailStep(err.Error())
	return err
}
```

- [ ] **Step 8: Hook service-root migration**

In `pkg/catch/tty_service_set.go`, wrap the materialize/runtime/DB update block in `migrateServiceRootWithPlan` only when `oldService.ServiceRootZFS != ""`:

```go
operation := func() error {
	if err := s.materializeServiceRootMigration(plan, mode); err != nil {
		return err
	}
	updatedService, err := s.updatedServiceForRootMigration(plan, mode, oldService)
	if err != nil {
		return err
	}
	if err := s.applyServiceRootMigrationRuntimeChanges(plan, mode, oldService, updatedService); err != nil {
		return err
	}
	if err := s.updateMigratedServiceRoot(plan, updatedService); err != nil {
		return err
	}
	return s.refreshServiceRootMigrationPrereqs(oldService, updatedService)
}
return s.withServiceSnapshot(context.Background(), snapshotOperation{
	Service:   oldService,
	Event:     snapshotEventServiceRootMigration,
	Writer:    io.Discard,
	Operation: operation,
})
```

- [ ] **Step 9: Run hook tests and commit**

Run:

```bash
go test ./pkg/catch -run 'Test(InstallGenSnapshots|DockerUpdateSnapshots|ServiceRootMigrationSnapshots)' -count=1
go test ./pkg/catch -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch/service_snapshots.go pkg/catch/installer_service.go pkg/catch/installer_service_test.go pkg/catch/tty_ops.go pkg/catch/tty_ops_test.go pkg/catch/tty_service_set.go pkg/catch/tty_service_set_test.go
git commit -m "pkg/catch: snapshot before service mutations"
```

---

### Task 8: Docs, Website, And Final Verification

**Files:**
- Read: `website/AGENTS.md`
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/concepts/data-layout.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/changelog.mdx`

- [ ] **Step 1: Read website instructions**

Run:

```bash
sed -n '1,220p' website/AGENTS.md
```

Expected: note website docs and submodule commit rules.

- [ ] **Step 2: Update README**

Add a short section near the existing service-root/ZFS documentation:

```markdown
### Automatic ZFS snapshots

When a service uses a ZFS service root, catch takes a pre-change snapshot before
new payload deployments, Docker image updates, and ZFS-backed service-root
migrations. The default policy is enabled for ZFS-backed services, keeps the
newest 5 yeet-created snapshots, and prunes snapshots older than 7 days.

```bash
yeet snapshots defaults show
yeet snapshots defaults set --enabled=false
yeet service set vaultwarden --snapshots=off
yeet service set vaultwarden --snapshots=on --snapshot-keep-last=3 --snapshot-max-age=72h
```

Catch stores the live policy in its DB. `yeet.toml` stores only per-service
overrides so a service can be replayed from the project directory.
```

- [ ] **Step 3: Update website CLI docs**

In `website/docs/cli/yeet-cli.mdx`, add `snapshots defaults` under service/operation commands:

```mdx
## `yeet snapshots defaults`

Show or change the catch host's default ZFS snapshot policy.

```bash
yeet snapshots defaults show
yeet snapshots defaults set --enabled=true --keep-last=5 --max-age=7d
yeet snapshots defaults set --enabled=false
```

The default applies to ZFS-backed service roots. Services can inherit it, turn
snapshots on or off explicitly, or override retention:

```bash
yeet service set vaultwarden --snapshots=off
yeet service set vaultwarden --snapshots=on --snapshot-keep-last=3 --snapshot-max-age=72h
yeet service set vaultwarden --snapshots=inherit
```
```

- [ ] **Step 4: Update data layout and workflows docs**

In `website/docs/concepts/data-layout.mdx`, add:

```mdx
ZFS-backed service roots also get yeet-managed pre-change snapshots by default.
Catch creates snapshots on the service root dataset before new payload
deployments, Docker image updates, and ZFS-backed service-root migrations. Catch
only prunes snapshots it created and identifies them with `com.yeetrun:*` ZFS
user properties.
```

In `website/docs/operations/workflows.mdx`, add:

```mdx
### Snapshot policy

Snapshot defaults are catch host policy:

```bash
yeet snapshots defaults show
yeet snapshots defaults set --enabled=false
```

Per-service overrides live with the service settings:

```bash
yeet service set sonarr --snapshots=off
yeet service sync sonarr --config ~/yeet-services/yeet.toml
```
```

- [ ] **Step 5: Update changelog**

In `website/docs/changelog.mdx`, add an unreleased/new version entry only if this implementation is being prepared for release in the same branch. Use wording like:

```mdx
- Added automatic pre-change ZFS snapshots for ZFS-backed service roots, with catch-wide defaults and per-service overrides.
```

If no release is being cut in this work session, leave the changelog unchanged and record that decision in the final response.

- [ ] **Step 6: Run full verification**

Run:

```bash
go test ./pkg/db ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch ./pkg/svc -count=1
go test ./... -count=1
pre-commit run --all-files
```

Expected: all commands PASS.

- [ ] **Step 7: Optional live verification on a ZFS-capable catch host**

Use a disposable service name and dataset on a host that already has a ZFS parent dataset:

```bash
CATCH_HOST=<host> go run ./cmd/yeet init root@<host>
CATCH_HOST=<host> go run ./cmd/yeet snapshots defaults show
CATCH_HOST=<host> go run ./cmd/yeet run snap-test ./example/helloworld.sh --service-root=tank/apps/snap-test --zfs
CATCH_HOST=<host> go run ./cmd/yeet run snap-test ./example/helloworld.sh --force
ssh root@<host> "zfs list -t snapshot -o name,com.yeetrun:service,com.yeetrun:event tank/apps/snap-test"
CATCH_HOST=<host> go run ./cmd/yeet rm snap-test --yes
```

Expected: a `yeet-...-run-...` snapshot exists after the forced redeploy and has `com.yeetrun:service=snap-test`.

- [ ] **Step 8: Commit docs and final code state**

If website docs changed, commit inside the submodule first:

```bash
cd website
git add docs/cli/yeet-cli.mdx docs/concepts/data-layout.mdx docs/operations/workflows.mdx docs/changelog.mdx
git commit -m "docs: add zfs snapshot policy docs"
cd ..
```

Then commit root changes:

```bash
git add README.md website
git commit -m "docs: document zfs service snapshots"
```

If no website docs changed, commit only the root docs:

```bash
git add README.md
git commit -m "docs: document zfs service snapshots"
```

Do not push until the user explicitly asks for a push.

---

## Final Quality Gate

After all tasks are complete, run:

```bash
go test ./... -count=1
pre-commit run --all-files
git status --short --branch
```

Expected:

- `go test ./... -count=1` PASS
- `pre-commit run --all-files` PASS
- `git status --short --branch` shows only intentional committed state or website submodule changes already committed as described above

## Implementation Order

1. Task 1 DB schema and generated accessors.
2. Task 2 RPC and CLI types.
3. Task 3 client routing and project config.
4. Task 4 catch snapshot policy engine.
5. Task 5 catch commands and service info.
6. Task 6 service overrides and sync.
7. Task 7 mutation hooks.
8. Task 8 docs and verification.
