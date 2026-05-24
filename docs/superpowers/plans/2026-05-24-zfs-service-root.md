# ZFS Service Root Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow `yeet run` and `yeet service set` to use a ZFS dataset as the service-root identity while catch stores and uses the resolved filesystem mountpoint.

**Architecture:** `--service-root` keeps its existing absolute-path meaning unless `--zfs` is present. With `--zfs`, the service root argument is a ZFS dataset name; catch verifies or creates the dataset, resolves its mountpoint with `zfs get -H -o value mountpoint <dataset>`, stores the dataset in `db.Service.ServiceRootZFS`, and stores the resolved filesystem path in `db.Service.ServiceRoot`. `yeet.toml` remains a client-side replay recipe with `service_root = "<dataset>"` plus `service_root_zfs = true`, while the catch DB remains authoritative for the live resolved path and dataset identity.

**Tech Stack:** Go, `pkg/yargs` CLI parsing, catch RPC exec/PTY commands, JSON DB with generated viewer accessors, `os/exec` for catch-side ZFS commands, `copyutil` tar/extract migration helpers, Go table-driven tests, markdown docs.

---

## Execution Notes

- Work on `main`; the user explicitly requested this.
- The user explicitly authorized commits.
- Do not push root or website commits without separate push authorization.
- Use TDD for behavior changes: write a failing test, run it, implement, rerun, then commit.
- Use `superpowers:subagent-driven-development` for execution if the user chooses the recommended path.
- Do not run implementation subagents in parallel if their write sets overlap. The DB task must land before catch and client tasks that reference `ServiceRootZFS`.
- `pkg/db/db_view.go` and `pkg/db/db_clone.go` are generated; update `pkg/db/db.go`, then run `go generate ./pkg/db`.
- Before editing any subsystem, read that subsystem’s `AGENTS.md`.
- Before editing `website/`, read `website/AGENTS.md`; `website/` is a submodule and its commits remain local until the user authorizes a push.

## File Structure

- Modify `pkg/db/db.go`: add `ServiceRootZFS string` to `db.Service`.
- Modify `pkg/db/migrate.go`: bump the DB data version from 6 to 7 and add a no-op migrator.
- Regenerate `pkg/db/db_view.go` and `pkg/db/db_clone.go`.
- Modify `pkg/db/db_test.go`: cover clone/view/migration for `ServiceRootZFS`.
- Modify `pkg/cli/cli.go`: add `RunFlags.ZFS`, `ServiceSetFlags.ZFS`, parser metadata, and conditional `--service-root` validation.
- Modify `pkg/cli/cli_test.go`: cover `run --zfs`, `service set --zfs`, `--zfs` without root, and non-ZFS absolute-path rejection.
- Modify `cmd/yeet/cli_bridge_test.go` and `cmd/yeet/cli_test.go`: cover bridge/routing with `--zfs`.
- Modify `pkg/yeet/project_config.go`: add `ServiceEntry.ServiceRootZFS bool`.
- Modify `pkg/yeet/run_changes.go`: replace root-only helpers with root options helpers that understand `--zfs`.
- Modify `pkg/yeet/svc_cmd.go`: preserve, rehydrate, save, and update local config with `service_root_zfs`.
- Modify `pkg/yeet/*_test.go`: cover TOML persistence, run rehydration, root option extraction, and service-set local config updates.
- Modify `pkg/catch/catch.go`: extend installer config and service-root preparation to resolve ZFS requests.
- Create `pkg/catch/service_root_zfs.go`: implement dataset existence, creation, mountpoint resolution, mountpoint validation, and test seams.
- Create `pkg/catch/service_root_zfs_test.go`: test ZFS command behavior with a fake runner.
- Modify `pkg/catch/installer_file.go`: carry the resolved root plus dataset identity into DB updates.
- Modify `pkg/catch/tty_install.go`: pass `RunFlags.ZFS` into installer config.
- Modify `pkg/catch/tty_install_test.go` and `pkg/catch/installer_file_test.go`: cover initial run ZFS behavior.
- Modify `pkg/catch/tty_service_set.go`: resolve and migrate ZFS service roots, then update both DB fields atomically after copy/empty succeeds.
- Modify `pkg/catch/tty_service_set_test.go`: cover ZFS migration, existing datasets, created datasets, failure behavior, and type changes.
- Modify `README.md` and website docs under `website/docs/`: document `--zfs` for run and service set.

---

### Task 1: DB Schema And Generated Accessors

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

Expected: note generated file and migration guidance before editing.

- [ ] **Step 2: Write failing DB tests**

In `pkg/db/db_test.go`, extend `TestServiceRootCloneAndView` so the service includes both root fields and assertions for clone/view:

```go
data := &Data{
	DataVersion: CurrentDataVersion,
	Services: map[string]*Service{
		"svc": {
			Name:           "svc",
			ServiceRoot:    "/tank/apps/svc",
			ServiceRootZFS: "tank/apps/svc",
		},
	},
}
clone := data.Clone()
if got := clone.Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
	t.Fatalf("Clone ServiceRootZFS = %q, want tank/apps/svc", got)
}
clone.Services["svc"].ServiceRootZFS = "tank/apps/clone"
if got := data.Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
	t.Fatalf("source ServiceRootZFS was mutated through clone: %q", got)
}
view := data.View()
svc, ok := view.Services().GetOk("svc")
if !ok {
	t.Fatal("missing service view")
}
if got := svc.ServiceRootZFS(); got != "tank/apps/svc" {
	t.Fatalf("View ServiceRootZFS = %q, want tank/apps/svc", got)
}
if got := view.AsStruct().Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
	t.Fatalf("View AsStruct ServiceRootZFS = %q, want tank/apps/svc", got)
}
```

Add a new migration test beside `TestMigrateAddsServiceRootVersion`:

```go
func TestMigrateAddsServiceRootZFSVersion(t *testing.T) {
	path := t.TempDir() + "/db.json"
	writeData(t, path, &Data{
		DataVersion: 6,
		Services: map[string]*Service{
			"svc": {Name: "svc", ServiceRoot: "/srv/apps/svc"},
		},
	})
	store := NewStore(path)
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DataVersion != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion, CurrentDataVersion)
	}
	if got := got.Services["svc"].ServiceRoot; got != "/srv/apps/svc" {
		t.Fatalf("migrated ServiceRoot = %q, want /srv/apps/svc", got)
	}
	if got := got.Services["svc"].ServiceRootZFS; got != "" {
		t.Fatalf("migrated ServiceRootZFS = %q, want empty", got)
	}
	onDisk := readData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if got := onDisk.Services["svc"].ServiceRootZFS; got != "" {
		t.Fatalf("on-disk ServiceRootZFS = %q, want empty", got)
	}
	backup := readData(t, path+".v6.bak")
	if backup.DataVersion != 6 {
		t.Fatalf("backup DataVersion = %d, want 6", backup.DataVersion)
	}
}
```

- [ ] **Step 3: Run the DB tests and verify RED**

Run:

```bash
go test ./pkg/db -run 'Test(ServiceRootCloneAndView|MigrateAddsServiceRootZFSVersion)' -count=1
```

Expected: FAIL because `Service.ServiceRootZFS` and `ServiceView.ServiceRootZFS()` do not exist.

- [ ] **Step 4: Implement the DB field and migrator**

In `pkg/db/db.go`, add the field immediately after `ServiceRoot`:

```go
	// ServiceRootZFS is the ZFS dataset name used to resolve ServiceRoot.
	// Empty means ServiceRoot is a normal filesystem path or the default root.
	ServiceRootZFS string `json:",omitempty"`
```

In `pkg/db/migrate.go`, bump the version and add the migrator entry:

```go
const CurrentDataVersion = 7
```

```go
var migrators = map[int]func(*Data) error{ // Start DataVersion -> NextStep
	0: migrateFromZero,
	1: migrateToNetNS,
	2: migrateToMacvlan,
	3: migrateToServiceType,
	4: migrateToRegistry,
	5: addServiceRoot,
	6: addServiceRootZFS,
}
```

Add this function:

```go
func addServiceRootZFS(d *Data) error {
	return nil
}
```

- [ ] **Step 5: Regenerate DB view/clone code**

Run:

```bash
go generate ./pkg/db
```

Expected: `pkg/db/db_view.go` and `pkg/db/db_clone.go` include `ServiceRootZFS`.

- [ ] **Step 6: Run DB tests and commit**

Run:

```bash
go test ./pkg/db -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/db/db.go pkg/db/migrate.go pkg/db/db_view.go pkg/db/db_clone.go pkg/db/db_test.go
git commit -m "pkg/db: store zfs service root identity"
```

---

### Task 2: CLI Parsing And Routing

**Files:**
- Read: `pkg/cli/AGENTS.md`
- Read: `cmd/yeet/AGENTS.md`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`

- [ ] **Step 1: Read CLI instructions**

Run:

```bash
sed -n '1,220p' pkg/cli/AGENTS.md
```

Run:

```bash
sed -n '1,220p' cmd/yeet/AGENTS.md
```

Expected: note parser and bridge testing guidance.

- [ ] **Step 2: Write failing parser tests**

In `pkg/cli/cli_test.go`, extend the run parser test so the args include `--zfs` and assert the parsed flag:

```go
args := []string{
	"--net", "lan",
	"--ts-tags", "tag:app",
	"--service-root", "tank/apps/svc-a",
	"--zfs",
	"payload",
}
flags, rest, err := ParseRun(args)
if err != nil {
	t.Fatalf("ParseRun: %v", err)
}
if flags.ServiceRoot != "tank/apps/svc-a" {
	t.Fatalf("ServiceRoot = %q, want tank/apps/svc-a", flags.ServiceRoot)
}
if !flags.ZFS {
	t.Fatal("ZFS = false, want true")
}
if !reflect.DeepEqual(rest, []string{"payload"}) {
	t.Fatalf("rest = %#v", rest)
}
```

Replace the `ParseServiceSet` relative-root expectation with two cases: non-ZFS rejects relative roots, ZFS accepts dataset roots:

```go
{
	name: "zfs dataset root",
	args: []string{"svc-a", "--service-root=tank/apps/svc-a", "--zfs", "--copy"},
	want: ServiceSetFlags{ServiceRoot: "tank/apps/svc-a", ZFS: true, Copy: true},
},
{
	name:    "zfs without root",
	args:    []string{"svc-a", "--zfs"},
	wantErr: "--service-root is required when --zfs is set",
},
{
	name:    "relative root without zfs",
	args:    []string{"svc-a", "--service-root", "apps/svc-a"},
	wantErr: "--service-root must be absolute unless --zfs is set",
},
```

Add registry assertions:

```go
if _, ok := RemoteFlagSpecs()["run"]["--zfs"]; !ok {
	t.Fatal("run --zfs should be registered")
}
if _, ok := RemoteGroupFlagSpecs()["service"]["set"]["--zfs"]; !ok {
	t.Fatal("service set --zfs should be registered")
}
```

- [ ] **Step 3: Write failing bridge/routing tests**

In `cmd/yeet/cli_bridge_test.go`, add:

```go
{
	name:        "service set zfs root",
	args:        []string{"service", "set", "svc-a", "--service-root=tank/apps/svc-a", "--zfs"},
	wantService: "svc-a",
	wantHost:    "",
	wantBridged: "service set --service-root=tank/apps/svc-a --zfs",
	wantOK:      true,
},
```

In `cmd/yeet/cli_test.go`, add a `prepareCommandRoute` case:

```go
{
	name:        "service set zfs root host target",
	args:        []string{"service@catch-a", "set", "svc-a", "--service-root=tank/apps/svc-a", "--zfs"},
	wantHost:    "catch-a",
	wantService: "svc-a",
	wantArgs:    []string{"service", "set", "--service-root=tank/apps/svc-a", "--zfs"},
	wantBridged: []string{"service", "set", "--service-root=tank/apps/svc-a", "--zfs"},
}
```

- [ ] **Step 4: Run CLI tests and verify RED**

Run:

```bash
go test ./pkg/cli ./cmd/yeet -run 'Test(ParseRunFlagsAndArgs|ParseServiceSetFlags|RemoteFlagSpecs|BridgeServiceArgs|PrepareCommandRoute)' -count=1
```

Expected: FAIL because `ZFS` fields and `--zfs` flag metadata do not exist.

- [ ] **Step 5: Implement CLI fields, metadata, and validation**

In `pkg/cli/cli.go`, add `ZFS bool` to both public structs:

```go
type RunFlags struct {
	Net           string
	TsVer         string
	TsExit        string
	TsTags        []string
	TsAuthKey     string
	MacvlanMac    string
	MacvlanVlan   int
	MacvlanParent string
	Restart       bool
	Pull          bool
	Force         bool
	Publish       []string
	EnvFile       string
	ServiceRoot   string
	ZFS           bool
}

type ServiceSetFlags struct {
	ServiceRoot string
	ZFS         bool
	Copy        bool
	Empty       bool
}
```

Add `ZFS bool` to parsed structs:

```go
type runFlagsParsed struct {
	Net           string   `flag:"net"`
	TsVer         string   `flag:"ts-ver"`
	TsExit        string   `flag:"ts-exit"`
	TsTags        []string `flag:"ts-tags"`
	TsAuthKey     string   `flag:"ts-auth-key"`
	MacvlanMac    string   `flag:"macvlan-mac"`
	MacvlanVlan   int      `flag:"macvlan-vlan"`
	MacvlanParent string   `flag:"macvlan-parent"`
	Restart       bool     `flag:"restart" default:"true"`
	Pull          bool     `flag:"pull"`
	Force         bool     `flag:"force"`
	Publish       []string `flag:"publish" short:"p"`
	EnvFile       string   `flag:"env-file"`
	ServiceRoot   string   `flag:"service-root"`
	ZFS           bool     `flag:"zfs"`
}

type serviceSetFlagsParsed struct {
	ServiceRoot string `flag:"service-root"`
	ZFS         bool   `flag:"zfs"`
	Copy        bool   `flag:"copy"`
	Empty       bool   `flag:"empty"`
}
```

Copy `ZFS` through `ParseRun` and `ParseServiceSet`:

```go
flags := RunFlags{
	Net:           parsed.Flags.Net,
	TsVer:         parsed.Flags.TsVer,
	TsExit:        parsed.Flags.TsExit,
	TsTags:        parsed.Flags.TsTags,
	TsAuthKey:     parsed.Flags.TsAuthKey,
	MacvlanMac:    parsed.Flags.MacvlanMac,
	MacvlanVlan:   parsed.Flags.MacvlanVlan,
	MacvlanParent: parsed.Flags.MacvlanParent,
	Restart:       parsed.Flags.Restart,
	Pull:          parsed.Flags.Pull,
	Force:         parsed.Flags.Force,
	Publish:       parsed.Flags.Publish,
	EnvFile:       parsed.Flags.EnvFile,
	ServiceRoot:   parsed.Flags.ServiceRoot,
	ZFS:           parsed.Flags.ZFS,
}
```

```go
flags := ServiceSetFlags{
	ServiceRoot: strings.TrimSpace(parsed.Flags.ServiceRoot),
	ZFS:         parsed.Flags.ZFS,
	Copy:        parsed.Flags.Copy,
	Empty:       parsed.Flags.Empty,
}
if flags.ServiceRoot == "" {
	if flags.ZFS {
		return ServiceSetFlags{}, nil, fmt.Errorf("--service-root is required when --zfs is set")
	}
	return ServiceSetFlags{}, nil, fmt.Errorf("--service-root is required")
}
if !flags.ZFS && !filepath.IsAbs(flags.ServiceRoot) {
	return ServiceSetFlags{}, nil, fmt.Errorf("--service-root must be absolute unless --zfs is set")
}
if flags.Copy && flags.Empty {
	return ServiceSetFlags{}, nil, fmt.Errorf("cannot use --copy and --empty together")
}
```

Update `remoteGroupInfos["service"].Commands["set"]` usage and examples:

```go
Usage: "service set <svc> --service-root=/abs/path|dataset [--zfs] [--copy|--empty]",
Examples: []string{
	"yeet service set <svc> --service-root=/srv/apps/<svc>",
	"yeet service set <svc> --service-root=tank/apps/<svc> --zfs --copy",
	"yeet service set <svc> --service-root=/srv/apps/<svc> --empty",
},
```

- [ ] **Step 6: Run CLI tests and commit**

Run:

```bash
go test ./pkg/cli ./cmd/yeet -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go
git commit -m "cli: parse zfs service roots"
```

---

### Task 3: Client Project Config And Run Rehydration

**Files:**
- Read: `pkg/yeet/AGENTS.md`
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/run_changes.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/project_config_test.go`
- Modify: `pkg/yeet/run_changes_test.go`
- Modify: `pkg/yeet/handle_svc_cmd_config_test.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Read client orchestration instructions**

Run:

```bash
sed -n '1,260p' pkg/yeet/AGENTS.md
```

Expected: note local config and remote execution testing guidance.

- [ ] **Step 2: Write failing config persistence tests**

In `pkg/yeet/project_config_test.go`, add this new test:

```go
func TestSaveRunConfigStoresZFSServiceRoot(t *testing.T) {
	withTempCwd(t)
	loc := &projectConfigLocation{
		Path: filepath.Join(t.TempDir(), "yeet.toml"),
		Dir:  t.TempDir(),
		Config: &ProjectConfig{
			Version: projectConfigVersion,
		},
	}
	serviceOverride = "svc-a"
	t.Cleanup(func() { serviceOverride = "" })
	if err := saveRunConfig(loc, "host-a", "ghcr.io/example/app:latest", []string{"--service-root", "tank/apps/svc-a", "--zfs", "--pull"}, "tank/apps/svc-a", true); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing saved entry")
	}
	if entry.ServiceRoot != "tank/apps/svc-a" {
		t.Fatalf("service_root = %q, want tank/apps/svc-a", entry.ServiceRoot)
	}
	if !entry.ServiceRootZFS {
		t.Fatal("service_root_zfs = false, want true")
	}
	raw, err := os.ReadFile(loc.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), `service_root = "tank/apps/svc-a"`) {
		t.Fatalf("saved config = %q, want service_root", string(raw))
	}
	if !strings.Contains(string(raw), `service_root_zfs = true`) {
		t.Fatalf("saved config = %q, want service_root_zfs", string(raw))
	}
}
```

- [ ] **Step 3: Write failing root option helper tests**

Replace `TestExtractServiceRootFlag` cases in `pkg/yeet/run_changes_test.go` with `extractServiceRootOptions` expectations:

```go
func TestExtractServiceRootOptions(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    serviceRootOptions
		wantArgs []string
		wantFound bool
		wantErr string
	}{
		{name: "path root", args: []string{"--service-root", "/srv/apps/svc-a", "--pull"}, want: serviceRootOptions{Root: "/srv/apps/svc-a"}, wantArgs: []string{"--pull"}, wantFound: true},
		{name: "zfs root", args: []string{"--service-root=tank/apps/svc-a", "--zfs", "--pull"}, want: serviceRootOptions{Root: "tank/apps/svc-a", ZFS: true}, wantArgs: []string{"--pull"}, wantFound: true},
		{name: "zfs before root", args: []string{"--zfs", "--service-root", "tank/apps/svc-a"}, want: serviceRootOptions{Root: "tank/apps/svc-a", ZFS: true}, wantArgs: []string{}, wantFound: true},
		{name: "payload delimiter", args: []string{"--", "--service-root", "payload"}, wantArgs: []string{"--", "--service-root", "payload"}, wantFound: false},
		{name: "zfs without root", args: []string{"--zfs"}, wantErr: "--zfs requires --service-root"},
		{name: "relative without zfs", args: []string{"--service-root", "apps/svc-a"}, wantErr: "--service-root must be absolute unless --zfs is set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotArgs, gotFound, err := extractServiceRootOptions(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("extractServiceRootOptions error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractServiceRootOptions: %v", err)
			}
			if got != tt.want || !reflect.DeepEqual(gotArgs, tt.wantArgs) || gotFound != tt.wantFound {
				t.Fatalf("extractServiceRootOptions = %#v %#v %v, want %#v %#v %v", got, gotArgs, gotFound, tt.want, tt.wantArgs, tt.wantFound)
			}
		})
	}
}
```

Add a rehydration helper test:

```go
func TestRunArgsWithServiceRootOptions(t *testing.T) {
	got := runArgsWithServiceRootOptions([]string{"--pull"}, serviceRootOptions{Root: "tank/apps/svc-a", ZFS: true})
	want := []string{"--service-root=tank/apps/svc-a", "--zfs", "--pull"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgsWithServiceRootOptions = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 4: Write failing run and service-set config tests**

In `pkg/yeet/handle_svc_cmd_config_test.go`, update the rehydration test to include ZFS:

```go
entry := ServiceEntry{
	Name:           "rssbot",
	Host:           "host-a",
	Payload:        "ghcr.io/example/rssbot:latest",
	ServiceRoot:    "tank/apps/rssbot",
	ServiceRootZFS: true,
	Args:           []string{"--pull"},
}
wantArgs := []string{"run", "--service-root=tank/apps/rssbot", "--zfs", "--pull"}
```

In `pkg/yeet/svc_cmd_branch_test.go`, add:

```go
func TestSvcRunZFSServiceRoot(t *testing.T) {
	loc := &projectConfigLocation{
		Dir: t.TempDir(),
		Config: &ProjectConfig{
			Services: []ServiceEntry{{
				Name:           "app",
				Host:           Host(),
				ServiceRoot:    "tank/apps/stored",
				ServiceRootZFS: true,
			}},
		},
	}
	run, err := parseSvcRun([]string{"app", "--pull"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun stored zfs service-root: %v", err)
	}
	if run.ServiceRoot != "tank/apps/stored" || !run.ServiceRootZFS {
		t.Fatalf("run root = %q zfs=%v, want tank/apps/stored true", run.ServiceRoot, run.ServiceRootZFS)
	}
	if !reflect.DeepEqual(run.Args, []string{"--service-root=tank/apps/stored", "--zfs", "--pull"}) {
		t.Fatalf("run args = %#v", run.Args)
	}
	run, err = parseSvcRun([]string{"app", "--service-root", "tank/apps/explicit", "--zfs"}, loc, "")
	if err != nil {
		t.Fatalf("parseSvcRun explicit zfs service-root: %v", err)
	}
	if run.ServiceRootArg != "tank/apps/explicit" || !run.ServiceRootZFSArg || !run.ServiceRootSet {
		t.Fatalf("explicit root = %q zfsArg=%v set=%v", run.ServiceRootArg, run.ServiceRootZFSArg, run.ServiceRootSet)
	}
}
```

Update service-set config coverage to assert `ServiceRootZFS` changes:

```go
if err := HandleSvcCmd([]string{"service", "set", "--service-root=tank/apps/svc-a", "--zfs", "--copy"}); err != nil {
	t.Fatalf("HandleSvcCmd: %v", err)
}
entry, ok := loc.Config.ServiceEntry("svc-a", Host())
if !ok || entry.ServiceRoot != "tank/apps/svc-a" || !entry.ServiceRootZFS {
	t.Fatalf("entry = %#v, want zfs service root", entry)
}
```

- [ ] **Step 5: Run client tests and verify RED**

Run:

```bash
go test ./pkg/yeet -run 'Test(SaveRunConfigStoresZFSServiceRoot|ExtractServiceRootOptions|RunArgsWithServiceRootOptions|RunFromProjectConfigRehydratesServiceRoot|SvcRunZFSServiceRoot|HandleSvcServiceSet)' -count=1
```

Expected: FAIL because `ServiceRootZFS`, root option helpers, and updated function signatures do not exist.

- [ ] **Step 6: Implement config fields and root option helpers**

In `pkg/yeet/project_config.go`, add the TOML bool:

```go
type ServiceEntry struct {
	Name           string   `toml:"name"`
	Host           string   `toml:"host"`
	Type           string   `toml:"type,omitempty"`
	Payload        string   `toml:"payload,omitempty"`
	EnvFile        string   `toml:"env_file,omitempty"`
	ServiceRoot    string   `toml:"service_root,omitempty"`
	ServiceRootZFS bool     `toml:"service_root_zfs,omitempty"`
	Schedule       string   `toml:"schedule,omitempty"`
	Args           []string `toml:"args,omitempty"`
}
```

Update `SetServiceEntry` so an existing entry is updated with the ZFS bool whenever the service root changes or ZFS is true:

```go
if entry.ServiceRoot != "" {
	c.Services[i].ServiceRoot = entry.ServiceRoot
	c.Services[i].ServiceRootZFS = entry.ServiceRootZFS
}
```

In `pkg/yeet/run_changes.go`, add:

```go
type serviceRootOptions struct {
	Root string
	ZFS  bool
}

func extractServiceRootOptions(args []string) (serviceRootOptions, []string, bool, error) {
	if len(args) == 0 {
		return serviceRootOptions{}, args, false, nil
	}
	out := make([]string, 0, len(args))
	opts := serviceRootOptions{}
	foundRoot := false
	foundZFS := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == "--zfs" {
			opts.ZFS = true
			foundZFS = true
			continue
		}
		if strings.HasPrefix(arg, "--zfs=") {
			value := strings.TrimPrefix(arg, "--zfs=")
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return serviceRootOptions{}, nil, false, fmt.Errorf("invalid --zfs value %q", value)
			}
			opts.ZFS = parsed
			foundZFS = true
			continue
		}
		if arg == "--service-root" {
			if i+1 >= len(args) {
				return serviceRootOptions{}, nil, false, fmt.Errorf("--service-root requires a value")
			}
			opts.Root = strings.TrimSpace(args[i+1])
			foundRoot = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "--service-root=") {
			opts.Root = strings.TrimSpace(strings.TrimPrefix(arg, "--service-root="))
			foundRoot = true
			continue
		}
		out = append(out, arg)
	}
	if foundZFS && !foundRoot {
		return serviceRootOptions{}, nil, false, fmt.Errorf("--zfs requires --service-root")
	}
	if foundRoot && !opts.ZFS && !filepath.IsAbs(opts.Root) {
		return serviceRootOptions{}, nil, false, fmt.Errorf("--service-root must be absolute unless --zfs is set")
	}
	return opts, out, foundRoot || foundZFS, nil
}

func runArgsWithServiceRootOptions(args []string, opts serviceRootOptions) []string {
	args = append([]string{}, args...)
	opts.Root = strings.TrimSpace(opts.Root)
	if opts.Root == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--service-root="+opts.Root)
	if opts.ZFS {
		out = append(out, "--zfs")
	}
	out = append(out, args...)
	return out
}
```

Replace these exact call sites with the new helpers:

```text
pkg/yeet/svc_cmd.go: parseSvcRunControlFlags uses extractServiceRootOptions
pkg/yeet/svc_cmd.go: parseSvcRun uses runArgsWithServiceRootOptions
pkg/yeet/svc_cmd.go: saveRunConfig uses extractServiceRootOptions
pkg/yeet/svc_cmd.go: runFromProjectConfig/runWithChanges path uses runArgsWithServiceRootOptions
pkg/yeet/run_changes.go: runWithChangesTo compares against runArgsWithServiceRootOptions(entry.Args, serviceRootOptions{Root: entry.ServiceRoot, ZFS: entry.ServiceRootZFS})
```

- [ ] **Step 7: Implement run parsing and config saving**

In `pkg/yeet/svc_cmd.go`, extend parsed structs:

```go
type parsedSvcRun struct {
	Payload           string
	Args              []string
	EnvFile           string
	EnvFileArg        string
	EnvFileSet        bool
	ServiceRoot       string
	ServiceRootZFS    bool
	ServiceRootArg    string
	ServiceRootZFSArg bool
	ServiceRootSet    bool
	Entry             ServiceEntry
	ForceDeploy       bool
}

type svcRunControlFlags struct {
	Args              []string
	EnvFileArg        string
	EnvFileSet        bool
	ServiceRootArg    string
	ServiceRootZFSArg bool
	ServiceRootSet    bool
	ForceDeploy       bool
}
```

Update `parseSvcRunControlFlags`:

```go
rootOpts, filteredArgs, serviceRootSet, err := extractServiceRootOptions(runArgs)
if err != nil {
	return svcRunControlFlags{}, err
}
envFileArg, filteredArgs, envFileSet, err := extractEnvFileFlag(filteredArgs)
if err != nil {
	return svcRunControlFlags{}, err
}
forceDeploy, filteredArgs, err := extractForceFlag(filteredArgs)
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
}, nil
```

Update `parseSvcRun` to inherit stored ZFS when there is no explicit root:

```go
serviceRoot := flags.ServiceRootArg
serviceRootZFS := flags.ServiceRootZFSArg
if serviceRoot == "" && hasEntry {
	serviceRoot = entry.ServiceRoot
	serviceRootZFS = entry.ServiceRootZFS
}
filteredArgs := runArgsWithServiceRootOptions(flags.Args, serviceRootOptions{Root: serviceRoot, ZFS: serviceRootZFS})
```

Update `handleSvcRun` and `saveRunConfig` signature:

```go
if err := saveRunConfig(req.Config, req.HostOverride, run.Payload, run.Args, run.ServiceRootArg, run.ServiceRootZFSArg); err != nil {
	return err
}
```

```go
func saveRunConfig(cfgLoc *projectConfigLocation, hostOverride string, payload string, runArgs []string, serviceRoot string, serviceRootZFS bool) error {
	rootOpts, filteredArgs, foundServiceRoot, err := extractServiceRootOptions(runArgs)
	if err != nil {
		return err
	}
	if foundServiceRoot && strings.TrimSpace(serviceRoot) == "" {
		serviceRoot = rootOpts.Root
		serviceRootZFS = rootOpts.ZFS
	}
	entry := ServiceEntry{
		Name:           serviceOverride,
		Host:           host,
		Type:           "",
		Payload:        payloadRel,
		ServiceRoot:    strings.TrimSpace(serviceRoot),
		ServiceRootZFS: serviceRootZFS,
		Args:           normalizeRunArgs(filteredArgs),
	}
```

Update `saveServiceSetConfig`:

```go
func saveServiceSetConfig(cfgLoc *projectConfigLocation, hostOverride string, serviceRoot string, serviceRootZFS bool) error {
	if serviceOverride == "" || strings.TrimSpace(serviceRoot) == "" {
		return nil
	}
	entry, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	if !ok {
		return nil
	}
	entry.ServiceRoot = strings.TrimSpace(serviceRoot)
	entry.ServiceRootZFS = serviceRootZFS
	cfgLoc.Config.SetServiceEntry(entry)
	return saveProjectConfig(cfgLoc)
}
```

And call it from `handleSvcService`:

```go
return saveServiceSetConfig(req.Config, req.HostOverride, flags.ServiceRoot, flags.ZFS)
```

- [ ] **Step 8: Run client tests and commit**

Run:

```bash
go test ./pkg/yeet -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/yeet/project_config.go pkg/yeet/run_changes.go pkg/yeet/svc_cmd.go pkg/yeet/*_test.go
git commit -m "pkg/yeet: persist zfs service roots"
```

---

### Task 4: Catch ZFS Resolver

**Files:**
- Read: `pkg/catch/AGENTS.md`
- Create: `pkg/catch/service_root_zfs.go`
- Create: `pkg/catch/service_root_zfs_test.go`
- Modify: `pkg/catch/catch.go`

- [ ] **Step 1: Read catch instructions**

Run:

```bash
sed -n '1,260p' pkg/catch/AGENTS.md
```

Expected: note catch-side service orchestration and tests guidance.

- [ ] **Step 2: Write failing resolver tests**

Create `pkg/catch/service_root_zfs_test.go`:

```go
package catch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResolveZFSServiceRootExistingDataset(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		switch strings.Join(args, " ") {
		case "list -H -o name tank/apps/svc":
			return "tank/apps/svc\n", "", nil
		case "get -H -o value mountpoint tank/apps/svc":
			return "/tank/apps/svc\n", "", nil
		default:
			return "", "", errors.New("unexpected zfs command")
		}
	}
	got, err := resolveZFSServiceRoot(context.Background(), runner, "tank/apps/svc", zfsServiceRootTarget)
	if err != nil {
		t.Fatalf("resolveZFSServiceRoot: %v", err)
	}
	if got.Dataset != "tank/apps/svc" || got.Root != "/tank/apps/svc" {
		t.Fatalf("resolved = %#v, want dataset tank/apps/svc root /tank/apps/svc", got)
	}
	wantCalls := [][]string{
		{"list", "-H", "-o", "name", "tank/apps/svc"},
		{"get", "-H", "-o", "value", "mountpoint", "tank/apps/svc"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestResolveZFSServiceRootCreatesMissingDataset(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		switch strings.Join(args, " ") {
		case "list -H -o name tank/apps/svc":
			return "", "cannot open 'tank/apps/svc': dataset does not exist\n", errZFSCommandFailed
		case "create tank/apps/svc":
			return "", "", nil
		case "get -H -o value mountpoint tank/apps/svc":
			return "/tank/apps/svc\n", "", nil
		default:
			return "", "", errors.New("unexpected zfs command")
		}
	}
	got, err := resolveZFSServiceRoot(context.Background(), runner, "tank/apps/svc", zfsServiceRootTarget)
	if err != nil {
		t.Fatalf("resolveZFSServiceRoot: %v", err)
	}
	if got.Root != "/tank/apps/svc" {
		t.Fatalf("Root = %q, want /tank/apps/svc", got.Root)
	}
	wantCalls := [][]string{
		{"list", "-H", "-o", "name", "tank/apps/svc"},
		{"create", "tank/apps/svc"},
		{"get", "-H", "-o", "value", "mountpoint", "tank/apps/svc"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestResolveZFSServiceRootErrors(t *testing.T) {
	tests := []struct {
		name    string
		runner  zfsCommandRunner
		wantErr string
	}{
		{
			name: "create fails",
			runner: func(ctx context.Context, args ...string) (string, string, error) {
				if strings.Join(args, " ") == "list -H -o name tank/apps/svc" {
					return "", "dataset does not exist", errZFSCommandFailed
				}
				return "", "parent does not exist", errZFSCommandFailed
			},
			wantErr: "zfs create tank/apps/svc failed: parent does not exist",
		},
		{
			name: "legacy mountpoint",
			runner: func(ctx context.Context, args ...string) (string, string, error) {
				if strings.Join(args, " ") == "list -H -o name tank/apps/svc" {
					return "tank/apps/svc\n", "", nil
				}
				return "legacy\n", "", nil
			},
			wantErr: "unsupported ZFS mountpoint",
		},
		{
			name: "relative mountpoint",
			runner: func(ctx context.Context, args ...string) (string, string, error) {
				if strings.Join(args, " ") == "list -H -o name tank/apps/svc" {
					return "tank/apps/svc\n", "", nil
				}
				return "relative/path\n", "", nil
			},
			wantErr: "ZFS mountpoint",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveZFSServiceRoot(context.Background(), tt.runner, "tank/apps/svc", zfsServiceRootTarget)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("resolveZFSServiceRoot error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 3: Run resolver tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'TestResolveZFSServiceRoot' -count=1
```

Expected: FAIL because resolver types and functions do not exist.

- [ ] **Step 4: Implement the resolver**

Create `pkg/catch/service_root_zfs.go`:

```go
package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errZFSCommandFailed = errors.New("zfs command failed")
var osStat = os.Stat

type zfsCommandRunner func(context.Context, ...string) (stdout string, stderr string, err error)

type zfsServiceRootMode int

const (
	zfsServiceRootTarget zfsServiceRootMode = iota
	zfsServiceRootExisting
)

type resolvedServiceRoot struct {
	Root    string
	Dataset string
	ZFS     bool
}

func runZFSCommand(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), err
	}
	return stdout.String(), stderr.String(), nil
}

func resolveZFSServiceRoot(ctx context.Context, runner zfsCommandRunner, dataset string, mode zfsServiceRootMode) (resolvedServiceRoot, error) {
	dataset = strings.TrimSpace(dataset)
	if dataset == "" {
		return resolvedServiceRoot{}, fmt.Errorf("--service-root is required when --zfs is set")
	}
	if runner == nil {
		runner = runZFSCommand
	}
	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	if !exists {
		if err := zfsCreateDataset(ctx, runner, dataset); err != nil {
			return resolvedServiceRoot{}, err
		}
	}
	mountpoint, err := zfsDatasetMountpoint(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	root, err := validateZFSMountpoint(mountpoint, mode)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	return resolvedServiceRoot{Root: root, Dataset: dataset, ZFS: true}, nil
}

func zfsDatasetExists(ctx context.Context, runner zfsCommandRunner, dataset string) (bool, error) {
	stdout, stderr, err := runner(ctx, "list", "-H", "-o", "name", dataset)
	if err == nil {
		if strings.TrimSpace(stdout) == "" {
			return false, fmt.Errorf("zfs list %s returned no dataset name", dataset)
		}
		return true, nil
	}
	if strings.Contains(stderr, "dataset does not exist") || strings.Contains(stderr, "does not exist") {
		return false, nil
	}
	return false, formatZFSCommandError("zfs list "+dataset, stderr, err)
}

func zfsCreateDataset(ctx context.Context, runner zfsCommandRunner, dataset string) error {
	_, stderr, err := runner(ctx, "create", dataset)
	if err != nil {
		return formatZFSCommandError("zfs create "+dataset, stderr, err)
	}
	return nil
}

func zfsDatasetMountpoint(ctx context.Context, runner zfsCommandRunner, dataset string) (string, error) {
	stdout, stderr, err := runner(ctx, "get", "-H", "-o", "value", "mountpoint", dataset)
	if err != nil {
		return "", formatZFSCommandError("zfs get mountpoint "+dataset, stderr, err)
	}
	return strings.TrimSpace(stdout), nil
}

func validateZFSMountpoint(mountpoint string, mode zfsServiceRootMode) (string, error) {
	mountpoint = strings.TrimSpace(mountpoint)
	if mountpoint == "" || mountpoint == "-" || mountpoint == "legacy" {
		return "", fmt.Errorf("unsupported ZFS mountpoint %q; set a normal mounted mountpoint before using --zfs", mountpoint)
	}
	if !filepath.IsAbs(mountpoint) {
		return "", fmt.Errorf("ZFS mountpoint %q must be absolute", mountpoint)
	}
	cleaned := filepath.Clean(mountpoint)
	if mode == zfsServiceRootExisting {
		info, err := osStat(cleaned)
		if err != nil {
			return "", fmt.Errorf("failed to stat ZFS mountpoint %q: %w", cleaned, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("ZFS mountpoint %q is not a directory", cleaned)
		}
		return cleaned, nil
	}
	return validateRequestedServiceRoot(cleaned)
}

func formatZFSCommandError(command string, stderr string, err error) error {
	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		return fmt.Errorf("%s failed: %s", command, stderr)
	}
	return fmt.Errorf("%s failed: %w", command, err)
}
```

- [ ] **Step 5: Run resolver tests and commit**

Run:

```bash
go test ./pkg/catch -run 'TestResolveZFSServiceRoot' -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch/service_root_zfs.go pkg/catch/service_root_zfs_test.go pkg/catch/catch.go
git commit -m "pkg/catch: resolve zfs service roots"
```

---

### Task 5: Initial Run ZFS Integration

**Files:**
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/tty_install_test.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/catch_test.go`

- [ ] **Step 1: Write failing installer and run tests**

In `pkg/catch/tty_install_test.go`, update `TestRunCmdFuncCopiesServiceRootIntoInstallerConfig`:

```go
flags := cli.RunFlags{ServiceRoot: "tank/apps/svc-run-root", ZFS: true}
if err := execer.runCmdFunc(flags, nil); err != nil {
	t.Fatalf("runCmdFunc: %v", err)
}
if gotCfg.ServiceRoot != "tank/apps/svc-run-root" {
	t.Fatalf("ServiceRoot = %q, want tank/apps/svc-run-root", gotCfg.ServiceRoot)
}
if !gotCfg.ServiceRootZFS {
	t.Fatal("ServiceRootZFS = false, want true")
}
```

In `pkg/catch/installer_file_test.go`, add:

```go
func TestNewFileInstallerPersistsZFSServiceRoot(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	mountpoint := filepath.Join(parent, "svc")
	zfsRunner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: mountpoint, Exists: true},
	})
	server.zfsRunner = zfsRunner.Run
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName:    "svc",
			User:           "",
			ServiceRoot:    "tank/apps/svc",
			ServiceRootZFS: true,
		},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	if got := installer.serviceRoot; got != mountpoint {
		t.Fatalf("installer.serviceRoot = %q, want %q", got, mountpoint)
	}
	if installer.serviceRootZFS != "tank/apps/svc" {
		t.Fatalf("installer.serviceRootZFS = %q, want tank/apps/svc", installer.serviceRootZFS)
	}
	if err := installer.applyInstallPlanToService(&db.Service{Name: "svc"}, fileInstallPlan{}); err != nil {
		t.Fatalf("applyInstallPlanToService: %v", err)
	}
}
```

Add an explicit DB field assertion in `TestNewFileInstallerPersistsZFSServiceRoot` after the installer assertions:

```go
svc := &db.Service{Name: "svc"}
installer.applyInstallServiceRoot(svc)
if svc.ServiceRoot != mountpoint {
	t.Fatalf("ServiceRoot = %q, want %q", svc.ServiceRoot, mountpoint)
}
if svc.ServiceRootZFS != "tank/apps/svc" {
	t.Fatalf("ServiceRootZFS = %q, want tank/apps/svc", svc.ServiceRootZFS)
}
```

In `pkg/catch/catch_test.go`, add cases for existing service root comparisons:

```go
func TestPrepareServiceRootForInstallZFSExistingSameDataset(t *testing.T) {
	server := newTestServer(t)
	mountpoint := filepath.Join(t.TempDir(), "svc")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: mountpoint, Exists: true},
	}).Run
	mutateTestDB(t, server, func(d *db.Data) {
		d.Services["svc"] = &db.Service{Name: "svc", ServiceRoot: "/old/mount", ServiceRootZFS: "tank/apps/svc"}
	})
	got, err := server.prepareServiceRootForInstall("svc", "tank/apps/svc", true)
	if err != nil {
		t.Fatalf("prepareServiceRootForInstall: %v", err)
	}
	if got.Root != mountpoint || got.Dataset != "tank/apps/svc" || !got.ZFS {
		t.Fatalf("resolved = %#v", got)
	}
}
```

- [ ] **Step 2: Add catch test fake**

At the bottom of `pkg/catch/service_root_zfs_test.go`, add:

```go
type fakeZFSDataset struct {
	Mountpoint string
	Exists     bool
	CreateErr  string
}

type fakeZFSRunner map[string]fakeZFSDataset

func (f fakeZFSRunner) Run(ctx context.Context, args ...string) (string, string, error) {
	if len(args) == 5 && reflect.DeepEqual(args[:4], []string{"list", "-H", "-o", "name"}) {
		ds := args[4]
		if data, ok := f[ds]; ok && data.Exists {
			return ds + "\n", "", nil
		}
		return "", "dataset does not exist", errZFSCommandFailed
	}
	if len(args) == 2 && args[0] == "create" {
		ds := args[1]
		data := f[ds]
		if data.CreateErr != "" {
			return "", data.CreateErr, errZFSCommandFailed
		}
		data.Exists = true
		if data.Mountpoint == "" {
			data.Mountpoint = "/" + ds
		}
		f[ds] = data
		return "", "", nil
	}
	if len(args) == 6 && reflect.DeepEqual(args[:5], []string{"get", "-H", "-o", "value", "mountpoint"}) {
		ds := args[5]
		data, ok := f[ds]
		if !ok || !data.Exists {
			return "", "dataset does not exist", errZFSCommandFailed
		}
		return data.Mountpoint + "\n", "", nil
	}
	return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
}
```

- [ ] **Step 3: Run catch tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'Test(RunCmdFuncCopiesServiceRootIntoInstallerConfig|NewFileInstallerPersistsZFSServiceRoot|PrepareServiceRootForInstallZFS)' -count=1
```

Expected: FAIL because installer config and server preparation do not carry ZFS.

- [ ] **Step 4: Implement installer config and server resolution**

In `pkg/catch/catch.go`, extend `InstallerCfg`:

```go
	// ServiceRoot is either an absolute filesystem root or, when ServiceRootZFS
	// is true, a ZFS dataset name to resolve on the catch host.
	ServiceRoot string
	// ServiceRootZFS treats ServiceRoot as a ZFS dataset name.
	ServiceRootZFS bool
```

In `pkg/catch/catch.go`, add this runner seam to `type Server struct` after `serviceRootDirFunc`:

```go
	zfsRunner zfsCommandRunner
```

Change `prepareServiceRootForInstall` signature and body:

```go
func (s *Server) prepareServiceRootForInstall(sn, requested string, requestedZFS bool) (resolvedServiceRoot, error) {
	sv, err := s.serviceView(sn)
	if err != nil && !errors.Is(err, errServiceNotFound) {
		return resolvedServiceRoot{}, err
	}
	if requestedZFS {
		resolved, err := resolveZFSServiceRoot(context.Background(), s.zfsRunner, requested, zfsServiceRootTarget)
		if err != nil {
			return resolvedServiceRoot{}, err
		}
		if sv.Valid() {
			if sv.ServiceRootZFS() == resolved.Dataset {
				existing, err := resolveZFSServiceRoot(context.Background(), s.zfsRunner, resolved.Dataset, zfsServiceRootExisting)
				if err != nil {
					return resolvedServiceRoot{}, err
				}
				return existing, nil
			}
			return resolvedServiceRoot{}, fmt.Errorf(
				"service %q already uses service root %q; change it with: yeet service set %s --service-root=%s --zfs",
				sn,
				s.serviceRootFromView(sv),
				sn,
				resolved.Dataset,
			)
		}
		return resolved, nil
	}
	if sv.Valid() {
		effective := s.serviceRootFromView(sv)
		if requested == "" {
			return resolvedServiceRoot{Root: effective}, nil
		}
		cleaned, err := cleanRequestedServiceRoot(requested)
		if err != nil {
			return resolvedServiceRoot{}, err
		}
		if sv.ServiceRootZFS() != "" {
			return resolvedServiceRoot{}, fmt.Errorf(
				"service %q already uses ZFS service root %q; change it with: yeet service set %s --service-root=%s",
				sn,
				sv.ServiceRootZFS(),
				sn,
				cleaned,
			)
		}
		if cleaned == filepath.Clean(effective) {
			return resolvedServiceRoot{Root: cleaned}, nil
		}
		return resolvedServiceRoot{}, fmt.Errorf(
			"service %q already uses service root %q; change it with: yeet service set %s --service-root=%s",
			sn,
			effective,
			sn,
			cleaned,
		)
	}
	if requested == "" {
		return resolvedServiceRoot{Root: s.defaultServiceRootDir(sn)}, nil
	}
	root, err := validateRequestedServiceRoot(requested)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	return resolvedServiceRoot{Root: root}, nil
}
```

In `pkg/catch/installer_file.go`, store resolved fields:

```go
resolvedRoot, err := s.prepareServiceRootForInstall(cfg.ServiceName, cfg.ServiceRoot, cfg.ServiceRootZFS)
if err != nil {
	return nil, fmt.Errorf("failed to prepare service root: %w", err)
}
cfg.ServiceRoot = resolvedRoot.Root
```

In `pkg/catch/installer_file.go`, add `serviceRootZFS` after the existing `serviceRoot` field:

```go
	serviceRootZFS string
```

Initialize:

```go
serviceRoot:    resolvedRoot.Root,
serviceRootZFS: resolvedRoot.Dataset,
```

Update DB write:

```go
func (i *FileInstaller) applyInstallServiceRoot(s *db.Service) {
	s.ServiceRootZFS = i.serviceRootZFS
	if filepath.Clean(i.serviceRoot) == filepath.Clean(i.s.defaultServiceRootDir(i.cfg.ServiceName)) && i.serviceRootZFS == "" {
		s.ServiceRoot = ""
		return
	}
	s.ServiceRoot = i.serviceRoot
}
```

In `pkg/catch/tty_install.go`, set config:

```go
cfg.ServiceRoot = flags.ServiceRoot
cfg.ServiceRootZFS = flags.ZFS
```

- [ ] **Step 5: Run catch tests and commit**

Run:

```bash
go test ./pkg/catch -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch/catch.go pkg/catch/installer_file.go pkg/catch/tty_install.go pkg/catch/*_test.go
git commit -m "pkg/catch: install zfs service roots"
```

---

### Task 6: `yeet service set --zfs` Migration

**Files:**
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_service_set_test.go`

- [ ] **Step 1: Write failing migration tests**

In `pkg/catch/tty_service_set_test.go`, add:

```go
func TestServiceSetZFSMigrationCopy(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "config.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot, Exists: true},
	}).Run
	mutateTestDB(t, server, func(d *db.Data) {
		d.Services[name] = &db.Service{Name: name, ServiceRoot: oldRoot}
	})
	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}
	assertServiceRoot(t, server, name, newRoot)
	assertServiceRootZFS(t, server, name, "tank/apps/svc")
	if got, err := os.ReadFile(filepath.Join(serviceDataDirForRoot(newRoot), "config.txt")); err != nil || string(got) != "ok" {
		t.Fatalf("copied config = %q err=%v, want ok nil", got, err)
	}
}

func TestServiceSetZFSMigrationCreatesDatasetAndLeavesDBOnCopyFailure(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old-missing")
	newRoot := filepath.Join(t.TempDir(), "new")
	runner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot},
	})
	server.zfsRunner = runner.Run
	mutateTestDB(t, server, func(d *db.Data) {
		d.Services[name] = &db.Service{Name: name, ServiceRoot: oldRoot}
	})
	err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true}, serviceRootMigrationCopy)
	if err == nil || !strings.Contains(err.Error(), "archive service root") {
		t.Fatalf("migrateServiceRoot error = %v, want archive failure", err)
	}
	if !runner["tank/apps/svc"].Exists {
		t.Fatal("dataset was not created before migration failure")
	}
	assertServiceRoot(t, server, name, oldRoot)
	assertServiceRootZFS(t, server, name, "")
}

func TestServiceSetRejectsNoopAcrossRootTypes(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	root := filepath.Join(t.TempDir(), "svc")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: root, Exists: true},
	}).Run
	mutateTestDB(t, server, func(d *db.Data) {
		d.Services[name] = &db.Service{Name: name, ServiceRoot: root}
	})
	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true})
	if err == nil || !strings.Contains(err.Error(), "already uses service root") {
		t.Fatalf("validateServiceRootMigration error = %v, want same path different identity rejection", err)
	}
}
```

Add helper:

```go
func assertServiceRootZFS(t *testing.T, server *Server, name, want string) {
	t.Helper()
	d, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	svc, ok := d.Services().GetOk(name)
	if !ok {
		t.Fatalf("service %q missing", name)
	}
	if got := svc.ServiceRootZFS(); got != want {
		t.Fatalf("ServiceRootZFS = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run migration tests and verify RED**

Run:

```bash
go test ./pkg/catch -run 'TestServiceSetZFS|TestServiceSetRejectsNoopAcrossRootTypes' -count=1
```

Expected: FAIL because migration request structs and ZFS DB updates do not exist.

- [ ] **Step 3: Implement migration request and plan**

In `pkg/catch/tty_service_set.go`, add:

```go
type serviceRootMigrationRequest struct {
	Root string
	ZFS  bool
}
```

Extend `serviceRootMigrationPlan`:

```go
type serviceRootMigrationPlan struct {
	ServiceName string
	OldRoot     string
	NewRoot     string
	NewRootZFS  string
}
```

Update `serviceSetCmdFunc`:

```go
request := serviceRootMigrationRequest{Root: flags.ServiceRoot, ZFS: flags.ZFS}
plan, err := e.s.validateServiceRootMigration(e.sn, request)
if err != nil {
	return err
}
mode, err = e.confirmServiceRootMigrationMode(mode, plan)
if err != nil {
	return err
}
return e.s.migrateServiceRoot(plan.ServiceName, request, mode)
```

Update `validateServiceRootMigration` signature and resolution:

```go
func (s *Server) validateServiceRootMigration(name string, request serviceRootMigrationRequest) (serviceRootMigrationPlan, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return serviceRootMigrationPlan{}, fmt.Errorf("service %q not found", name)
		}
		return serviceRootMigrationPlan{}, err
	}
	running, err := isServiceRunningForRootMigration(s, name)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	if running {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate service root while %q is running", name)
	}
	oldRoot := filepath.Clean(s.serviceRootFromView(sv))
	var resolved resolvedServiceRoot
	if request.ZFS {
		resolved, err = resolveZFSServiceRoot(context.Background(), s.zfsRunner, request.Root, zfsServiceRootTarget)
		if err != nil {
			return serviceRootMigrationPlan{}, err
		}
	} else {
		newRoot, err := cleanRequestedServiceRoot(request.Root)
		if err != nil {
			return serviceRootMigrationPlan{}, err
		}
		newRoot, err = validateRequestedServiceRoot(newRoot)
		if err != nil {
			return serviceRootMigrationPlan{}, err
		}
		resolved = resolvedServiceRoot{Root: newRoot}
	}
	newRoot := filepath.Clean(resolved.Root)
	if oldRoot == newRoot && sv.ServiceRootZFS() == resolved.Dataset {
		return serviceRootMigrationPlan{}, fmt.Errorf("service root for %q is already %s", name, oldRoot)
	}
	if oldRoot == newRoot && sv.ServiceRootZFS() != resolved.Dataset {
		return serviceRootMigrationPlan{}, fmt.Errorf("service %q already uses service root %q with a different root type; choose a different target root", name, oldRoot)
	}
	if rootsAreNested(oldRoot, newRoot) || rootsAreNested(newRoot, oldRoot) {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, newRoot)
	}
	return serviceRootMigrationPlan{ServiceName: name, OldRoot: oldRoot, NewRoot: newRoot, NewRootZFS: resolved.Dataset}, nil
}
```

- [ ] **Step 4: Implement migration execution and DB update**

Update `migrateServiceRoot`:

```go
func (s *Server) migrateServiceRoot(name string, request serviceRootMigrationRequest, mode serviceRootMigrationMode) error {
	plan, err := s.validateServiceRootMigration(name, request)
	if err != nil {
		return err
	}
	switch mode {
	case serviceRootMigrationCopy:
		if err := s.copyServiceRootMigration(plan.OldRoot, plan.NewRoot); err != nil {
			return err
		}
	case serviceRootMigrationEmpty:
		if err := createEmptyServiceRoot(plan.NewRoot); err != nil {
			return err
		}
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
	return s.updateServiceRoot(plan.ServiceName, plan.NewRoot, plan.NewRootZFS)
}
```

Update `updateServiceRoot`:

```go
func (s *Server) updateServiceRoot(name, newRoot, newRootZFS string) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		svc, ok := d.Services[name]
		if !ok {
			return fmt.Errorf("service %q not found", name)
		}
		svc.ServiceRootZFS = newRootZFS
		if newRootZFS == "" && filepath.Clean(newRoot) == filepath.Clean(s.defaultServiceRootDir(name)) {
			svc.ServiceRoot = ""
		} else {
			svc.ServiceRoot = newRoot
		}
		return nil
	})
	return err
}
```

Update all existing test calls:

```go
server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy)
```

```go
server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
```

- [ ] **Step 5: Run migration tests and commit**

Run:

```bash
go test ./pkg/catch -run 'TestServiceSet|TestServiceRootMigration|TestValidateServiceRootMigration' -count=1
```

Expected: PASS.

Run:

```bash
go test ./pkg/catch -count=1
```

Expected: PASS.

Commit:

```bash
git add pkg/catch/tty_service_set.go pkg/catch/tty_service_set_test.go
git commit -m "pkg/catch: migrate zfs service roots"
```

---

### Task 7: Docs And User-Facing Help

**Files:**
- Read: `.codex/skills/yeet-docs/SKILL.md`
- Read: `website/AGENTS.md`
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/getting-started/quick-start.mdx`
- Modify: `website/docs/concepts/data-layout.mdx`

- [ ] **Step 1: Read docs instructions**

Run:

```bash
sed -n '1,240p' .codex/skills/yeet-docs/SKILL.md
```

Run:

```bash
sed -n '1,240p' website/AGENTS.md
```

Expected: note website submodule workflow and docs style.

- [ ] **Step 2: Update README examples**

In `README.md`, update the service-root section with these examples:

```markdown
yeet run vaultwarden ./compose.yml --service-root=/srv/apps/vaultwarden
yeet run vaultwarden ./compose.yml --service-root=tank/apps/vaultwarden --zfs
```

Add this explanatory paragraph:

```markdown
Without `--zfs`, `--service-root` must be an absolute filesystem path on the
catch host. With `--zfs`, `--service-root` is a ZFS dataset name such as
`tank/apps/vaultwarden`; catch accepts an existing dataset or runs
`zfs create tank/apps/vaultwarden`, then uses the dataset mountpoint as the
service root. Parent datasets must already exist.
```

Update migration examples:

```markdown
yeet service set vaultwarden --service-root=/mnt/fast/vaultwarden --copy
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --copy
yeet service set vaultwarden --service-root=/mnt/fast/vaultwarden --empty
```

- [ ] **Step 3: Update website docs**

In each website file below, add the ZFS run example next to the existing filesystem-root run example, add the ZFS migration example next to the existing `service set --copy` example when that file has migration examples, and add the explanatory note from this step after the first service-root paragraph:

```text
website/docs/cli/yeet-cli.mdx
website/docs/operations/workflows.mdx
website/docs/getting-started/quick-start.mdx
website/docs/concepts/data-layout.mdx
```

Use this exact note:

```markdown
For ZFS-backed service roots, pass `--zfs` and use the dataset name as
`--service-root`; catch resolves the dataset mountpoint and stores both the
dataset identity and the resolved filesystem path.
```

- [ ] **Step 4: Run docs checks and commit website locally**

Run from the root repo:

```bash
pre-commit run --files README.md website/docs/cli/yeet-cli.mdx website/docs/operations/workflows.mdx website/docs/getting-started/quick-start.mdx website/docs/concepts/data-layout.mdx
```

Expected: PASS.

Run:

```bash
git -C website status --short
```

Expected: website docs files are modified.

Commit inside `website/`:

```bash
git -C website add docs/cli/yeet-cli.mdx docs/operations/workflows.mdx docs/getting-started/quick-start.mdx docs/concepts/data-layout.mdx
git -C website commit -m "docs: document zfs service roots"
```

Expected: local website commit is created. Do not push it.

- [ ] **Step 5: Commit root docs and submodule pointer**

Run:

```bash
git add README.md website
git commit -m "docs: document zfs service roots"
```

Expected: root commit includes README and the updated website submodule pointer.

---

### Task 8: Verification And Cleanup

**Files:**
- No planned source edits.
- Use changed files from Tasks 1-7.

- [ ] **Step 1: Run targeted test suites**

Run:

```bash
go test ./pkg/db ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go suite**

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

- [ ] **Step 4: Inspect final state**

Run:

```bash
git status --short --branch
```

Expected: root branch is `main`; root has no unstaged changes. `website` may show a new committed submodule pointer in root history and no uncommitted website docs changes.

Run:

```bash
git log --oneline -8
```

Expected: recent commits show DB, CLI, client config, catch resolver, install integration, service-set migration, docs, and final cleanup commits.

- [ ] **Step 5: Summarize completion**

Report:

```text
Implemented ZFS service roots for run and service set.
Verified with:
- go test ./pkg/db ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1
- go test ./... -count=1
- pre-commit run --all-files

Root branch: main
Push status: not pushed
Website submodule: local docs commit, not pushed
```
