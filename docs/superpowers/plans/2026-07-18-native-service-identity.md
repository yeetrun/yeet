# Native Service Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run new native binaries, scripts, and cron-style systemd services as a managed `yeet-svc` account by default, support explicit `USER[:GROUP]`, and migrate existing native services with durable ownership and unit rollback.

**Architecture:** Catch remains root and is authoritative for account resolution, service type, filesystem ownership, and systemd changes. Each native service persists the requested and numeric identity. New native roots are prepared directly for the final identity; existing services use one locked migration transaction shared by `run --run-as` and `service set --run-as`. Docker, VMs, Catch, and privileged helper units never enter this path.

**Tech Stack:** Go, JSON-backed Catch DB, command-shaped Catch RPC, systemd, Linux UID/GID and xattrs, ZFS snapshots, GitButler, Docusaurus MDX.

## Execution Order and Coordination

- Complete `docs/superpowers/plans/2026-07-18-var-lib-host-migration.md` first. If a service still lives below an inaccessible legacy home, identity migration must stop and print the whole-host or combined root-migration command.
- Wait for the Firecracker jailer task's shared-file handoff before editing `cmd/catch/catch.go`, `pkg/cli/cli.go`, `pkg/catchrpc/types.go`, `pkg/catch/service_info.go`, `cmd/yeet`, or public docs.
- Do not edit VM-specific code. A VM rejects generic `--run-as` and points to the separate `yeet-vm` jailer/guest identity controls.
- Keep command-shaped remote forwarding. Catch parses `run`, `cron`, and `service set` and performs all host account validation.
- Do not use systemd `DynamicUser=`, UID 1000, automatic capabilities, privileged-port sysctls, or supplementary-group management.
- Existing native services with no identity remain legacy root until an operator explicitly supplies `--run-as`.
- Create the implementation branch with GitButler, run `but pull --check` first, and select only this plan's files at every checkpoint.

## Shared Types

Use one persisted identity and one resolved runtime representation throughout the plan:

```go
// pkg/db/db.go
type ServiceIdentity struct {
	RequestedUser  string
	RequestedGroup string
	UID            uint32
	GID            uint32
}

// pkg/catch/service_identity.go
type resolvedServiceIdentity struct {
	Persisted db.ServiceIdentity
	UserName  string
	GroupName string
}
```

`Service.Identity == nil` means one thing only: an old native service whose historical unit runs as root. New native services always persist a non-nil identity, including explicit root.

---

### Task 1: Persisted Native Identity and DB Compatibility

**Files:**
- Modify: `pkg/db/db.go`
- Modify: `pkg/db/migrate.go`
- Modify: `pkg/db/db_test.go`
- Modify: `pkg/db/db_view_test.go`
- Regenerate: `pkg/db/db_view.go`
- Regenerate: `pkg/db/db_clone.go`

**Interfaces:**
- Produces: `db.ServiceIdentity`, `db.Service.Identity *ServiceIdentity`, schema version 13, cloned/viewed identity access.
- Consumes: schema version 12 created by the ISO allocation work; old records remain identity-nil.

- [ ] **Step 1: Write failing schema, clone, view, and migration tests**

Add tests for the exact compatibility rule:

```go
func TestMigrateVersion12LeavesOldServiceIdentityNil(t *testing.T) {
	d := &Data{DataVersion: 12, Services: map[string]*Service{
		"api": {Name: "api", ServiceType: ServiceTypeSystemd},
	}}
	if _, err := migrate(d); err != nil {
		t.Fatal(err)
	}
	if d.DataVersion != 13 || d.Services["api"].Identity != nil {
		t.Fatalf("migrated data = %#v", d)
	}
}

func TestServiceIdentityCloneAndView(t *testing.T) {
	want := &ServiceIdentity{RequestedUser: "app", RequestedGroup: "app", UID: 1002, GID: 1003}
	svc := &Service{Name: "api", Identity: want}
	clone := svc.Clone()
	if clone.Identity == want || *clone.Identity != *want {
		t.Fatalf("clone identity = %#v", clone.Identity)
	}
	view := svc.View()
	if !view.Identity().Valid() || view.Identity().UID() != 1002 || view.Identity().GID() != 1003 {
		t.Fatalf("view identity = %#v", view.Identity())
	}
}
```

Also verify a new explicit-root record round-trips as non-nil and DB backup/migration retry behavior still follows the existing store contract.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/db -run 'Test(MigrateVersion12LeavesOldServiceIdentityNil|ServiceIdentityCloneAndView|ServiceIdentityRoundTrip)' -count=1
```

Expected: compile failures because `ServiceIdentity` and `Service.Identity` do not exist.

- [ ] **Step 3: Add schema version 13 and regenerate helpers**

Add the field and no-op compatibility migrator:

```go
type Service struct {
	Name        string
	ServiceType ServiceType
	Identity    *ServiceIdentity `json:",omitempty"`
	// existing fields remain unchanged
}

const CurrentDataVersion = 13

var migrators = map[int]func(*Data) error{
	// existing entries
	12: addServiceIdentity,
}

func addServiceIdentity(*Data) error { return nil }
```

Add `ServiceIdentity` to both `go:generate` type lists in `pkg/db/db.go` and `pkg/db/db_view.go`, then regenerate rather than hand-editing:

```bash
mise exec -- go generate ./pkg/db
```

- [ ] **Step 4: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/db -count=1
```

Expected: PASS, and `git diff --check -- pkg/db` is clean.

- [ ] **Step 5: Checkpoint only Task 1**

Use `but status --short`, select the six Task 1 files into `codex/native-service-identity`, and checkpoint with `db: persist native service identities`.

---

### Task 2: Managed `yeet-svc` Account and `USER[:GROUP]` Resolution

**Files:**
- Create: `pkg/catch/service_account.go`
- Create: `pkg/catch/service_account_test.go`
- Create: `pkg/catch/service_identity.go`
- Create: `pkg/catch/service_identity_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**
- Produces: `EnsureManagedServiceAccount() (resolvedServiceIdentity, error)`, `resolveServiceIdentity(string) (resolvedServiceIdentity, error)`, `effectiveServiceIdentity(db.ServiceView) resolvedServiceIdentity`, `serviceIdentityClass(*db.ServiceIdentity) string`, and `validateServiceIdentityDrift(db.ServiceIdentity) error`.
- Consumes: Task 1's persisted type. The Catch installer calls the account helper; VM account code remains separate.

- [ ] **Step 1: Write failing parser, lookup, creation, and compatibility tests**

Cover this table exactly:

```go
tests := []struct {
	spec    string
	wantUID uint32
	wantGID uint32
	wantErr string
}{
	{spec: "app", wantUID: 1002, wantGID: 1003},
	{spec: "app:workers", wantUID: 1002, wantGID: 1010},
	{spec: "1002", wantUID: 1002, wantGID: 1003},
	{spec: "70000:70001", wantUID: 70000, wantGID: 70001},
	{spec: "root", wantUID: 0, wantGID: 0},
	{spec: "1009", wantErr: "numeric UID without an account requires a numeric GID"},
	{spec: "missing", wantErr: "user does not exist"},
	{spec: "app:missing", wantErr: "group does not exist"},
	{spec: "app:", wantErr: "group must not be empty"},
	{spec: ":app", wantErr: "user must not be empty"},
}
```

For `yeet-svc`, test compatible reuse and every incompatible property independently: UID/GID zero, wrong primary group, unlocked shadow entry, login shell, existing home directory, and supplementary group membership. Test one creation sequence and re-lookup:

```text
groupadd --system yeet-svc
useradd --system --gid yeet-svc --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin yeet-svc
usermod --lock yeet-svc
```

Accept `/sbin/nologin`, `/usr/sbin/nologin`, or `/bin/false` when present in `/etc/shells`; never accept an interactive shell.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test(ResolveServiceIdentity|ManagedServiceAccount|CatchInstallEnsuresServiceAccount)' -count=1
```

Expected: compile failures for the new account and identity helpers.

- [ ] **Step 3: Implement host-authoritative resolution**

Keep OS access behind injectable functions and return numeric IDs only after bounded parsing:

```go
const managedServiceUser = "yeet-svc"

func parseID(value, kind string) (uint32, error) {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", kind, value, err)
	}
	return uint32(n), nil
}

func effectiveServiceIdentity(sv db.ServiceView) resolvedServiceIdentity {
	if !sv.Valid() || !sv.Identity().Valid() {
		return resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "root", RequestedGroup: "root", UID: 0, GID: 0,
		}, UserName: "root", GroupName: "root"}
	}
	id := *sv.Identity().AsStruct()
	return resolvedServiceIdentity{Persisted: id, UserName: id.RequestedUser, GroupName: id.RequestedGroup}
}
```

For named accounts, use `os/user` plus `getent shadow` and `id -G`. For creation, run the three commands above, then perform the same strict compatibility validation; do not assume the allocated UID/GID. The only identity Yeet creates is `yeet-svc`.

`validateServiceIdentityDrift` re-resolves named user/group values and rejects any UID/GID mismatch before unit installation or service start. Numeric identities with no name-service entry remain valid if the stored numeric values match the request.

- [ ] **Step 4: Ensure the managed account during Catch install/upgrade**

In the Catch install path, call:

```go
if _, err := catch.EnsureManagedServiceAccount(); err != nil {
	return fmt.Errorf("prepare managed native service account: %w", err)
}
```

Run it before installing the Catch unit, not on every daemon startup. Also call it lazily when a new native payload selects the default, so an older Catch installed outside `yeet init` still behaves safely.

- [ ] **Step 5: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test(ResolveServiceIdentity|ServiceIdentityDrift|ManagedServiceAccount|CatchInstallEnsuresServiceAccount)' -count=1
```

Expected: PASS without touching the real host account database.

- [ ] **Step 6: Checkpoint only Task 2**

Use `but status --short`, select the six Task 2 files, and checkpoint with `catch: manage native service account`.

---

### Task 3: CLI, Cron Routing, Project Config, and Unsupported-Type Rejection

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/yeet/run_draft.go`
- Modify: `pkg/yeet/run_draft_test.go`
- Modify: `pkg/yeet/run_draft_validate.go`
- Modify: `pkg/yeet/run_draft_validate_test.go`
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/project_config_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/handle_svc_cmd_test.go`
- Modify: `pkg/yeet/handle_svc_cmd_config_test.go`
- Modify: `pkg/yeet/svc_cmd_routing_test.go`
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/tty_install_test.go`
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_service_set_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`

**Interfaces:**
- Produces: `RunFlags.RunAs/RunAsSet`, `ServiceSetFlags.RunAs/RunAsSet`, `CronFlags`, `ParseCron`, `ServiceEntry.RunAs`, and command-shaped forwarding.
- Consumes: Task 2 resolution on Catch. No structured run-as RPC is added.

- [ ] **Step 1: Add failing parser and bridge tests**

The public flag structs are:

```go
type RunFlags struct {
	// existing fields
	RunAs    string
	RunAsSet bool
}

type ServiceSetFlags struct {
	// existing fields
	RunAs    string
	RunAsSet bool
}

type CronFlags struct {
	RunAs    string
	RunAsSet bool
	Schedule string
}
```

Test `--run-as=app`, `--run-as app`, explicit `--run-as=root`, absent versus empty, flags before or after the cron schedule, payload arguments after `--`, repeated-flag rejection, and service-set combined with `--service-root --copy`.

Bridge tests must prove these canonical Catch argv shapes:

```text
run --run-as=app -- --serve
cron --run-as=backup --schedule="0 3 * * *" -- --daily
service set --run-as=app
service set --service-root=/var/lib/yeet/services/api --copy --run-as=app
```

- [ ] **Step 2: Run parser and bridge tests and confirm RED**

```bash
mise exec -- go test ./pkg/cli ./pkg/yeet ./pkg/catch ./cmd/yeet -run 'Test.*(RunAs|ParseCron|Cron.*Bridge|ServiceSet.*RunAs)' -count=1
```

Expected: compile failures because the new fields and parser do not exist.

- [ ] **Step 3: Implement shared parsing with absence preserved**

Add `RunAs string \`flag:"run-as" help:"Run a native service as USER[:GROUP]"\`` to the three parsed schemas and set `RunAsSet` by inspecting the actual argv, not by testing the final string:

```go
func parseRunAs(args []string, value string) (string, bool, error) {
	set := longFlagWasSupplied(args, "--run-as")
	value = strings.TrimSpace(value)
	if set && value == "" {
		return "", true, fmt.Errorf("--run-as requires USER[:GROUP]")
	}
	return value, set, nil
}

func longFlagWasSupplied(args []string, name string) bool {
	for _, arg := range args {
		if arg == "--" { return false }
		if arg == name || strings.HasPrefix(arg, name+"=") { return true }
	}
	return false
}
```

Catch remains authoritative: `ttyCommandHandlers["cron"]` calls `cli.ParseCron`, converts `Schedule` with `cronutil`, and passes `RunAs/RunAsSet` into `FileInstallerCfg`. `runCmdFunc` and `serviceSetCmdFunc` do the same.

Add the distinct installer fields; do not reuse `InstallerCfg.User`:

```go
type FileInstallerCfg struct {
	InstallerCfg
	// existing fields
	RunAs    string
	RunAsSet bool
}
```

- [ ] **Step 4: Persist local intent only after remote success**

Extend both TOML forms and the run draft:

```go
type ServiceEntry struct {
	// existing fields
	RunAs string `toml:"run_as,omitempty"`
}

type RunDraft struct {
	// existing fields
	RunAs    string `json:"runAs,omitempty"`
	RunAsSet bool   `json:"-"`
}
```

An old config with no `run_as` never forwards an explicit root choice for an existing service. A CLI flag wins over config. For a new service, Catch supplies `yeet-svc` even when local config omits the field. Save the requested value after the remote command succeeds. If local TOML writing fails, return:

```text
service identity changed on host.example.com, but yeet.toml was not updated; set run_as = "app:app" for service "api" and retry sync
```

Do not roll back the healthy remote service for this client-side failure.

- [ ] **Step 5: Reject unsupported workload types on Catch**

After payload detection, reject explicit run-as for `docker-compose`, generated Python, and TypeScript services with:

```text
--run-as applies only to native systemd workloads; configure the container image or Compose service "user:" field instead
```

Reject VMs with:

```text
--run-as does not control VM guest or Firecracker jailer identities; use VM guest settings or the VM isolation controls
```

Reject Catch before payload mutation. Existing Docker/VM `service set --run-as` follows the same type-based errors. A flag is never accepted and ignored.

- [ ] **Step 6: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/cli ./pkg/yeet ./pkg/catch ./cmd/yeet -run 'Test.*(RunAs|ParseCron|Cron.*Bridge|ServiceSet.*RunAs|ProjectConfig.*RunAs)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Checkpoint only Task 3**

Use `but status --short`, select the nineteen Task 3 files, and checkpoint with `cli: route native run as identity`.

---

### Task 4: Native Layout Contract and Primary systemd Identity

**Files:**
- Create: `pkg/catch/service_permissions.go`
- Create: `pkg/catch/service_permissions_test.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/catch_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/svc/systemd_test.go`

**Interfaces:**
- Produces: `applyNativeServiceLayout(string, db.ServiceIdentity) error`, `validateNativeServiceLayout(string, db.ServiceIdentity) error`, `SystemdUnit.Group`, direct versioned binary execution, and root-owned stable env/helper artifacts outside writable `run/`.
- Consumes: Task 2 identity resolution and Task 3 installer flags.

- [ ] **Step 1: Add failing unit and ownership-contract tests**

Assert this exact primary unit shape:

```ini
[Service]
ExecStart=/var/lib/yeet/services/api/bin/api-20260718.1 --serve
WorkingDirectory=/var/lib/yeet/services/api/data
User=app
Group=app
EnvironmentFile=-/var/lib/yeet/services/api/env/env
```

Assert auxiliary netns and Tailscale units contain no `User=app` or `Group=app`. Assert modes and owners:

```text
service root  root:app 0750
bin           root:app 0750
env           root:app 0750
bin/api-*     root:app 0750
env/env       root:app 0640
data          app:app  0750
run           app:app  0750
```

Test that `run/` contains no managed binary, env file, Tailscale binary/config, or netns env after native install.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/svc ./pkg/catch -run 'Test(SystemdUnit.*Group|NativeServiceLayout|NativeInstall.*ManagedArtifacts)' -count=1
```

Expected: unit output lacks `Group=`, executes `run/api`, and installs env/helper files below `run/`.

- [ ] **Step 3: Add `Group=` and execute the immutable generation directly**

Extend the template and type:

```go
{{if .User}}User={{.User}}{{end}}
{{if .Group}}Group={{.Group}}{{end}}

type SystemdUnit struct {
	Name  string
	User  string
	Group string
	// existing fields
}
```

Change native unit generation to receive the actual `fileInstallPlan.dst` and resolved identity:

```go
func (i *FileInstaller) newSystemdUnit(exe string) (*svc.SystemdUnit, error) {
	id := i.resolvedIdentity.Persisted
	su := &svc.SystemdUnit{
		Name: i.cfg.ServiceName,
		Executable: exe,
		WorkingDirectory: i.serviceDataDir(),
		Arguments: i.cfg.Args,
		EnvFile: "-" + filepath.Join(i.serviceEnvDir(), "env"),
		Timer: i.cfg.Timer,
		User: id.RequestedUser,
		Group: id.RequestedGroup,
	}
	if i.cfg.ServiceName == CatchService { configureCatchSystemdUnit(su) }
	if err := i.applyNetworkToSystemdUnit(su); err != nil { return nil, err }
	return su, nil
}
```

Add `resolvedIdentity resolvedServiceIdentity` to `FileInstaller`. Catch and privileged helper unit generation leave this field empty and therefore emit no `User=` or `Group=` lines.

Use numeric strings for `User=`/`Group=` when the request was numeric and has no account name. Revalidate UID/GID drift before installing the unit.

- [ ] **Step 4: Separate immutable artifacts from writable runtime state**

For `svc.SystemdService.artifactInstaller`, stop copying `ArtifactBinary` to `run/api` because the unit executes the versioned root-owned source. Change destinations:

```go
root := filepath.Dir(s.runDir)
binDir := filepath.Join(root, "bin")
envDir := filepath.Join(root, "env")

db.ArtifactEnvFile: {dstPath: filepath.Join(envDir, "env")},
db.ArtifactNetNSEnv: {dstPath: filepath.Join(envDir, "netns.env")},
db.ArtifactTSBinary: {dstPath: filepath.Join(binDir, "tailscaled")},
db.ArtifactTSEnv: {dstPath: filepath.Join(envDir, "tailscaled.env")},
db.ArtifactTSConfig: {dstPath: filepath.Join(envDir, "tailscaled.json")},
```

Keep helper units themselves privileged and root-owned. Docker compose services continue using their own existing runner and Compose identity behavior.

- [ ] **Step 5: Implement the mode and ownership contract**

`applyNativeServiceLayout` must use `Lchown`, never follow symlinks, and apply only Yeet-managed directories and artifacts. Preserve a stricter application file mode when it still gives the runtime UID/GID required access; do not recursively add write bits.

Stop using `InstallerCfg.User` as directory ownership. That field describes the caller/install context, not workload identity. New native payloads resolve identity after type detection, create `yeet-svc` lazily when needed, then apply the final contract before service start.

- [ ] **Step 6: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/svc ./pkg/catch -run 'Test(SystemdUnit|NativeServiceLayout|NativeInstall.*ManagedArtifacts|AuxiliaryUnit.*Root)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Checkpoint only Task 4**

Use `but status --short`, select the eight Task 4 files, and checkpoint with `systemd: run native workloads by identity`.

---

### Task 5: Service Operation Lock, Preflight Inventory, Journal, and ZFS Recovery Snapshot

**Files:**
- Create: `pkg/catch/service_operation_lock.go`
- Create: `pkg/catch/service_operation_lock_test.go`
- Create: `pkg/catch/service_identity_journal.go`
- Create: `pkg/catch/service_identity_journal_test.go`
- Modify: `pkg/catch/service_permissions.go`
- Modify: `pkg/catch/service_permissions_test.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/service_snapshots.go`
- Modify: `pkg/catch/service_snapshots_test.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `pkg/catch/tty_service.go`
- Modify: `pkg/catch/remove_test.go`

**Interfaces:**
- Produces: `serviceOperationLocks.Lock(...string) func()`, `inspectServiceIdentityChange`, `serviceIdentityJournal`, `restoreServiceIdentityJournal`, and snapshot event `service-identity-migration`.
- Consumes: Task 4 layout validator and configured service roots/ZFS datasets.

- [ ] **Step 1: Write failing lock ordering and filesystem hazard tests**

The keyed lock must deduplicate and sort names so a host migration can lock many services without deadlock:

```go
func TestServiceOperationLocksSortAndSerialize(t *testing.T) {
	var locks serviceOperationLocks
	releaseA := locks.Lock("worker", "api", "api")
	done := make(chan struct{})
	go func() { release := locks.Lock("api"); release(); close(done) }()
	select { case <-done: t.Fatal("second lock acquired early"); default: }
	releaseA()
	<-done
}
```

Inventory tests cover regular files, symlink `Lchown` without following it, device-boundary/nested-mount detection, nested ZFS datasets, `system.posix_acl_access`, `system.posix_acl_default`, `security.capability`, setuid/setgid bits, and `Nlink > 1`. Reject an external-hard-link ambiguity before changing any inode.

- [ ] **Step 2: Write failing journal crash and restore tests**

Use root-only JSONL outside the service root:

```go
type serviceIdentityJournalHeader struct {
	Version          int                `json:"version"`
	ID               string             `json:"id"`
	Service          string             `json:"service"`
	Root             string             `json:"root"`
	PreviousIdentity *db.ServiceIdentity `json:"previousIdentity,omitempty"`
	TargetIdentity   db.ServiceIdentity  `json:"targetIdentity"`
	PreviousUnit     string              `json:"previousUnit"`
	WasRunning       bool                `json:"wasRunning"`
}

type serviceIdentityInodeRecord struct {
	Path string      `json:"path"`
	UID  uint32      `json:"uid"`
	GID  uint32      `json:"gid"`
	Mode os.FileMode `json:"mode"`
}

type serviceIdentityPhaseRecord struct {
	Phase       string `json:"phase"`
	ZFSSnapshot string `json:"zfsSnapshot,omitempty"`
	Error       string `json:"error,omitempty"`
}
```

Test fsync before the first ownership mutation, bounded batch fsync, corrupt/truncated-line refusal, reverse-order restore, symlink restore with `Lchown`, and journal retention when restore is incomplete.

- [ ] **Step 3: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ServiceOperationLocks|ServiceIdentityInspection|ServiceIdentityJournal|ServiceIdentitySnapshot)' -count=1
```

Expected: compile failures for the lock, inspector, journal, and snapshot event.

- [ ] **Step 4: Implement deterministic preflight and durable journal**

Journal path:

```go
func serviceIdentityJournalPath(rootDir, service, id string) string {
	name := service + "-" + id + ".jsonl"
	return filepath.Join(rootDir, "migrations", "service-identity", name)
}
```

Call `serviceid.Validate(service)` before constructing this path. The immutable header is the first JSONL record; append `serviceIdentityInodeRecord` values and phase records after it. Recovery uses the last valid phase record, so phase changes never require rewriting a sealed inode inventory.

Open with `O_CREATE|O_EXCL|O_WRONLY|O_NOFOLLOW`, mode `0600`, beneath a root-only `0700` directory. Walk with `WalkDir`/`Lstat`, compare `Stat_t.Dev`, inspect xattrs without following symlinks, and record every inode before mutation. Fsync after the header and every 256 records, then fsync once more before sealing the journal.

Nested mounts/datasets may be skipped only when every path required for traversal/read/write already permits the target identity and no ownership change is planned there. Otherwise return the exact mount/dataset and required UID/GID or mode.

- [ ] **Step 5: Add the mandatory ZFS recovery snapshot**

Add:

```go
const snapshotEventServiceIdentityMigration snapshotEvent = "service-identity-migration"
```

For a ZFS-backed service root, call `createServiceSnapshot` directly before ownership mutation even when the normal snapshot policy is disabled. Record the snapshot name in the journal. On success, leave it visible to normal retention; on failure, report it as a recovery handle. Do not automatically roll back the whole dataset.

Register the new event in snapshot parsing, stable output ordering, and the default event set so retained recovery snapshots are pruned by the normal policy after later successful operations.

Use the keyed lock at the outer operation boundary for remove and host-storage migration. Host storage locks its sorted affected-service list once; inner stop/root helpers must not reacquire it. Copy, shell, restore, run, cron, and service-set integration follows in Tasks 6 and 7.

- [ ] **Step 6: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ServiceOperationLocks|ServiceIdentityInspection|ServiceIdentityJournal|ServiceIdentitySnapshot)' -count=1
```

Expected: PASS under the race detector for the lock tests:

```bash
mise exec -- go test -race ./pkg/catch -run 'TestServiceOperationLocks' -count=1
```

- [ ] **Step 7: Checkpoint only Task 5**

Use `but status --short`, select the thirteen Task 5 files, and checkpoint with `catch: journal service identity changes`.

---

### Task 6: One Rollback-Safe Migration Engine for `run` and `service set`

**Files:**
- Create: `pkg/catch/service_identity_migration.go`
- Create: `pkg/catch/service_identity_migration_test.go`
- Create: `pkg/catch/service_identity_recovery.go`
- Create: `pkg/catch/service_identity_recovery_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_service_set_test.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_ops_test.go`
- Modify: `pkg/catch/service_root_migration.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**
- Produces: `serviceIdentityMigrationRequest`, `Server.migrateServiceIdentity`, and combined root/identity transaction behavior.
- Consumes: Tasks 1-5 DB identity, resolver, systemd layout, lock, preflight, journal, snapshot, and existing root-migration machinery.

- [ ] **Step 1: Add failing phase-by-phase migration and rollback tests**

Define one request used by both entry points:

```go
type serviceIdentityMigrationRequest struct {
	Service       string
	Requested     string
	Target        resolvedServiceIdentity
	RootPlan      *serviceRootMigrationPlan
	ReplacementUnit string
	InstallGeneration func(context.Context) error
}

type serviceIdentityMigrationResult struct {
	Previous       resolvedServiceIdentity
	Current        resolvedServiceIdentity
	Root           string
	ZFSSnapshot    string
	WasRunning     bool
	Restarted      bool
}
```

Test root to `yeet-svc`, operator user A to B, explicit root, stopped-service preservation, running-service restart, same-identity no-op, and rerun without `RunAsSet` preserving a nil legacy-root identity.

Inject failures at journal seal, ZFS snapshot, stop, ownership change, root copy/switch, unit write, daemon reload, start, target verification, generation install, DB commit, and journal completion. Each test must assert old unit bytes, old DB identity, old ownership/modes, old root, and previous running state after rollback.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestServiceIdentityMigration' -count=1
```

Expected: compile failures because the migration request/result and engine do not exist.

- [ ] **Step 3: Implement one locked state machine**

Expose:

```go
func (s *Server) migrateServiceIdentity(ctx context.Context, req serviceIdentityMigrationRequest, w io.Writer) (serviceIdentityMigrationResult, error)
```

The state machine is fixed:

```text
lock -> type/account/root/port preflight -> record old DB/unit/running state
-> stop if running -> snapshot -> seal ownership journal
-> materialize optional root migration -> apply layout ownership
-> validate and install replacement primary unit -> daemon-reload
-> install optional new generation -> start only if previously running
-> verify unit User/Group/root and running state -> commit DB identity/root/generation
-> append complete phase -> remove journal -> unlock
```

Only the primary native service unit gets the target identity. The transaction must never modify Catch, netns, DNS, Tailscale, mount, VM, or Docker execution identity.

- [ ] **Step 4: Integrate new and existing `run` behavior**

After native payload detection:

```go
switch {
case !i.existingService.Valid() && !i.cfg.RunAsSet:
	i.resolvedIdentity, err = EnsureManagedServiceAccount()
case !i.existingService.Valid() && i.cfg.RunAsSet:
	i.resolvedIdentity, err = resolveServiceIdentity(i.cfg.RunAs)
case i.existingService.Valid() && !i.cfg.RunAsSet:
	i.resolvedIdentity = effectiveServiceIdentity(i.existingService)
case i.existingService.Valid() && i.cfg.RunAsSet:
	i.resolvedIdentity, err = resolveServiceIdentity(i.cfg.RunAs)
}
```

For a new service, apply final ownership directly and persist identity with the first successful generation. For an existing service with an explicit different value, call the migration engine before the new generation commits. Do not infer a migration merely because a local `yeet.toml` has no `run_as`.

Persist the selected identity in the same `MutateService` commit that advances the successful generation. A failed staged upload may leave root-owned versioned artifacts for normal cleanup, but it must not change the effective identity or unit.

- [ ] **Step 5: Integrate `service set` and combined root migration**

Add `identity bool` to `serviceSetChanges`. When root and identity both change, build both plans first and submit one `serviceIdentityMigrationRequest`; do not call the existing root migration and identity migration sequentially.

If a non-root target cannot traverse a legacy parent, return both safe choices:

```text
service root /root/yeet-data/services/api is not traversable by yeet-svc
migrate the exact legacy host layout:
  yeet host set --data-dir=/var/lib/yeet --services-root=/var/lib/yeet/services --migrate-services=all
or move only this service in the same transaction:
  yeet service set api --service-root=/var/lib/yeet/services/api --copy --run-as=yeet-svc
```

- [ ] **Step 6: Implement explicit rollback reporting**

Errors must include service/type, old and requested identities, root/dataset, failed phase, first blocking path/resource, old unit restoration, ownership restoration, running/stopped result, journal path, ZFS snapshot, and one safe retry command. When rollback also fails, join both errors and retain the journal.

At Catch startup, call `recoverServiceIdentityMigrations(ctx)` before accepting mutating requests. It scans root-only journals, locks each service, and rolls back every transaction that did not reach the complete phase. If recovery fails, Catch remains available for reads but rejects mutations for that service with the journal and snapshot paths. Tests must simulate process exit after every durable phase and prove restart restores the previous unit, identity, ownership, root, and running state.

- [ ] **Step 7: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ServiceIdentityMigration|Run.*Identity|ServiceSet.*Identity|CombinedRootIdentity)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Checkpoint only Task 6**

Use `but status --short`, select the thirteen Task 6 files, and checkpoint with `catch: migrate native service identities`.

---

### Task 7: Copy, Service Shell, Restore, Redeploy, and Privileged Ports

**Files:**
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/tty_install_test.go`
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/catch/tty_test.go`
- Modify: `pkg/catch/recovery_service_root.go`
- Modify: `pkg/catch/recovery_service_root_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Create: `pkg/catch/service_privileges.go`
- Create: `pkg/catch/service_privileges_test.go`

**Interfaces:**
- Produces: identity-aware copy/shell/restore/redeploy and `validateNativePrivilegedPorts`.
- Consumes: persisted effective identity and Task 4 layout helper.

- [ ] **Step 1: Add failing operational-path tests**

Prove:

- `copy --to` files and extracted directories end as runtime UID/GID without following archive symlinks;
- `copy --from` remains a root Catch read and preserves archive metadata;
- service shell uses the persisted UID/GID, clears supplementary groups, sets `HOME` and working directory to service `data/`, and uses `/bin/sh` for `yeet-svc` despite its nologin account;
- legacy identity-nil shell remains root until explicit migration;
- VM shell still rejects in favor of guest SSH;
- restore and rollback reapply the persisted ownership contract;
- ordinary redeploy without `--run-as` preserves the persisted identity.

- [ ] **Step 2: Add failing privileged-port tests**

Cover Compose short syntax forms accepted by the existing publish parser:

```go
tests := []struct {
	publish []string
	wantErr string
}{
	{publish: []string{"80:8080"}, wantErr: "host port 80"},
	{publish: []string{"127.0.0.1:443:8443/tcp"}, wantErr: "host port 443"},
	{publish: []string{"8080:8080"}},
	{publish: []string{"53"}}, // container-only syntax does not declare a host bind
}
```

Root UID bypasses the rejection. Non-root failures occur before stop, ownership, unit, or DB mutation.

- [ ] **Step 3: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ServiceCopy.*Identity|ServiceShell.*Identity|Recovery.*Identity|Redeploy.*Identity|NativePrivilegedPorts)' -count=1
```

Expected: tests report root-created files, root shell credentials, missing restore ownership, and no low-port rejection.

- [ ] **Step 4: Apply identity to copy and shell safely**

After a successful copy-to move, walk only the newly written destination and `Lchown` it to the persisted UID/GID. Reject archive device nodes and any hard-link or symlink target that escapes the destination.

For shell commands, set:

```go
cmd.Dir = serviceDataDir
cmd.Env = serviceShellEnvironment(os.Environ(), serviceDataDir)
cmd.SysProcAttr = &syscall.SysProcAttr{
	Credential: &syscall.Credential{
		Uid: identity.Persisted.UID,
		Gid: identity.Persisted.GID,
		NoSetGroups: true,
	},
}
```

Define `serviceShellEnvironment` to remove existing `HOME`, `USER`, `LOGNAME`, and `SHELL` entries, preserve non-identity variables, and append values such as `HOME=/var/lib/yeet/services/api/data`, `USER=app`, `LOGNAME=app`, and `SHELL=/bin/sh` from the resolved service identity.

Use `/bin/sh` for an interactive service shell when no command was supplied. Preserve the existing `ssh` permission classification.

Acquire the keyed service-operation lock for the full lifetime of copy-to, service shell, restore, and redeploy. Remove already acquired it in Task 5. Place each lock at one outer boundary and pass unlocked internal helpers so rollback cannot deadlock itself.

- [ ] **Step 5: Reapply ownership after restore and redeploy**

Call `applyNativeServiceLayout` after a service-root restore is materialized but before its unit starts. Validate stored name-to-ID drift before any start. Redeploy uses the existing persisted identity unless `RunAsSet` is true; environment-only and stage-only updates must not reset identity.

- [ ] **Step 6: Reject declared privileged ports before mutation**

Expose:

```go
func validateNativePrivilegedPorts(publish []string, id db.ServiceIdentity) error
```

For UID nonzero and a declared host port below 1024, return:

```text
native service api declares privileged host port 80 but runs as app (1002:1003); use an unprivileged port, put a root-owned proxy in front, or explicitly pass --run-as=root
```

Do not grant `CAP_NET_BIND_SERVICE` and do not change `net.ipv4.ip_unprivileged_port_start`.

- [ ] **Step 7: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ServiceCopy.*Identity|ServiceShell.*Identity|Recovery.*Identity|Redeploy.*Identity|NativePrivilegedPorts)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Checkpoint only Task 7**

Use `but status --short`, select the ten Task 7 files, and checkpoint with `catch: honor identity in service operations`.

---

### Task 8: Info, Sync, Permissions, Diagnostics, Docs, and Live Proof

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`
- Modify: `pkg/catch/tty_authz_test.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`
- Modify: `pkg/yeet/service_sync.go`
- Modify: `pkg/yeet/service_sync_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/handle_svc_cmd_test.go`
- Modify: `pkg/yeet/handle_svc_cmd_config_test.go`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `README.md`
- Modify: `website/docs/concepts/service-types.mdx`
- Modify: `website/docs/getting-started/host-setup.mdx`
- Modify: `website/docs/getting-started/service-workspace.mdx`
- Modify: `website/docs/getting-started/first-run-validation.mdx`

**Interfaces:**
- Produces: structured identity info/classification/drift, sync to `run_as`, actionable journal diagnostics, and complete user documentation.
- Consumes: all prior tasks and the Firecracker shared-file checkpoint.

- [ ] **Step 1: Add failing info, sync, authorization, and diagnostic tests**

Add the RPC shape:

```go
type ServiceIdentity struct {
	RequestedUser  string `json:"requestedUser,omitempty"`
	RequestedGroup string `json:"requestedGroup,omitempty"`
	UID            uint32 `json:"uid"`
	GID            uint32 `json:"gid"`
	Class          string `json:"class,omitempty"`
	Mismatch       string `json:"mismatch,omitempty"`
}

type ServiceInfo struct {
	// existing fields
	Identity *ServiceIdentity `json:"identity,omitempty"`
}
```

Classes are exactly `legacy-root`, `managed`, `operator`, and `explicit-root`. Docker and VM info omit generic identity. Tests cover human table and JSON output, name/UID drift, sync adding/updating `run_as`, sync leaving Docker/VM config alone, and local config write failure after remote commit.

Authorization tests must prove `run`, `cron`, and `service set --run-as` require `manage`; service shell continues to require `ssh`; info/sync reads require `read` until sync writes local disk only.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet ./pkg/cli ./cmd/yeet -run 'Test.*(ServiceIdentityInfo|RunAsSync|RunAsPermission|RunAsDiagnostic)' -count=1
```

Expected: compile failures for RPC identity and output assertions.

- [ ] **Step 3: Expose effective identity and drift without mutating state**

For native services, `serviceInfoWithContext` maps nil DB identity to legacy root and otherwise calls `validateServiceIdentityDrift`. Drift is returned as `Mismatch`, not as a failed read. Before start/migration, the same drift is a hard error.

Human output includes:

```text
Run as: yeet-svc:yeet-svc (system UID 997, GID 997; managed)
```

or:

```text
Run as: app:app (UID 1002, GID 1003; operator)
Identity warning: user app now resolves to UID 1012; service remains stopped
```

Sync writes the canonical requested user and group into `run_as` for native entries and reports the local file changed. Catch remains authoritative.

- [ ] **Step 4: Add permission-failure diagnostics**

When a non-root unit fails to start, fetch bounded recent journal output and include selected UID/GID, root, and likely resource classes. The final error format is:

```text
service api failed to start as app:app (1002:1003)
systemd: api.service: Failed at step EXEC spawning /var/lib/yeet/services/api/bin/api-20260718.1: Permission denied
check service data permissions, privileged ports, devices, and absolute host paths
the previous generation and identity were restored
retry: yeet run api ./api --run-as=app
```

Redact environment values and cap journal text to 8 KiB.

- [ ] **Step 5: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet ./pkg/cli ./cmd/yeet -run 'Test.*(ServiceIdentityInfo|RunAsSync|RunAsPermission|RunAsDiagnostic)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Update CLI help, README, and website manual**

Document these examples:

```bash
yeet run api ./api                         # new native service defaults to yeet-svc
yeet run api ./api --run-as=app:app
yeet cron backup ./backup "0 3 * * *" --run-as=backup
yeet service set api --run-as=yeet-svc
yeet service set api --run-as=root
yeet service set api \
  --service-root=/var/lib/yeet/services/api \
  --copy \
  --run-as=yeet-svc
```

Explain that existing services are unchanged until explicit migration; `yeet-svc` is shared and not cross-service isolation; Docker uses Compose `user:`; VMs use guest/jailer controls; only the primary native unit drops privilege; low ports need an unprivileged port, proxy, or explicit root. Keep examples on `yeetrun.com` and publish no private infrastructure.

- [ ] **Step 7: Commit and publish website docs only after approval**

Run website checks available in the submodule. Commit and push inside `website/` only when the user authorizes publication; then include the exact published gitlink in the root implementation branch. Until then, distinguish uncommitted website work from the root branch status.

- [ ] **Step 8: Run repository quality gates**

```bash
mise exec -- go test ./pkg/db ./pkg/catchrpc ./pkg/cli ./pkg/svc ./pkg/catch ./pkg/yeet ./cmd/catch ./cmd/yeet -count=1
mise exec -- go test -race ./pkg/svc ./pkg/catch -count=1
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: every command exits 0. Fix findings rather than weakening a gate or refreshing a baseline.

- [ ] **Step 9: Prove the behavior on disposable systemd and ZFS hosts**

On an ordinary disposable host, obtain the real PID with `systemctl show --property MainPID --value api.service` and verify its `/proc/$pid/status`, root-owned binary/env immutability, service-owned data/run writes, cron execution, service shell credentials, copy ownership, redeploy preservation, explicit root, low-port rejection, Docker/VM rejection, and rollback after injected start failure.

On a ZFS-capable disposable host, migrate an existing root native service, prove the mandatory `service-identity-migration` snapshot exists, test nested dataset refusal/preservation, inject rollback, and verify the ownership journal and snapshot are named in recovery output.

Do not use the Firecracker live rollout hosts for generic native identity proof until that task has completed and the user explicitly authorizes those service changes.

- [ ] **Step 10: Final checkpoint**

Run `but pull --check`, inspect `but diff` for private host details and unrelated Firecracker/ISO changes, and checkpoint only this plan's root files plus the published website gitlink with `services: default native workloads to yeet svc`.

Do not push or land on `main` without explicit user authorization.

## Completion Checklist

- No `DynamicUser=` and no fixed UID 1000 behavior exists.
- Catch creates/reuses only a strictly compatible host-allocated `yeet-svc` account.
- New native binaries, scripts, and timers persist `yeet-svc`; old identity-nil native services remain root.
- `run`, `cron`, and `service set` share Catch-side parsing and one migration engine.
- Docker, generated container workloads, VMs, and Catch reject generic run-as clearly.
- Managed executable/env/helper artifacts are outside writable `run/`.
- Only the primary native unit contains `User=` and `Group=`.
- Identity migration is locked, preflighted, journaled, snapshotted on ZFS, verified, and rollback-safe.
- Combined root/identity changes are one transaction.
- Copy, shell, restore, redeploy, info, sync, diagnostics, and low-port checks honor persisted identity.
- Mutation requires `manage`; service shell requires `ssh`; reads require `read`.
- CLI help, README, and website manual agree with live behavior.
