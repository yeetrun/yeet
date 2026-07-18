# `/var/lib/yeet` Host Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/var/lib/yeet` the default Catch state and service root on fresh hosts, and provide a prompted, rollback-safe migration of the exact historical `$HOME/yeet-data` layout without moving operator-selected roots or ZFS datasets.

**Architecture:** Catch keeps using the configured legacy path long enough to upgrade itself, then the existing host-storage planner/applier performs the move as a durable transaction. The client reconnects after Catch restarts, finalizes validation, and removes the exact legacy tree only after separate cleanup consent. Fresh installs never enter this migration path.

**Tech Stack:** Go, JSON-RPC, systemd, filesystem/ZFS inspection, SSH, GitButler, Docusaurus MDX.

## Execution Order and Coordination

- Execute this plan before `docs/superpowers/plans/2026-07-18-native-service-identity.md`. A non-root native process cannot traverse the historical `/root/yeet-data` default safely.
- Wait for the Firecracker jailer task to hand off its checkpointed shared changes before editing `cmd/catch/catch.go`, `pkg/cli/cli.go`, `pkg/catchrpc/types.go`, or public docs.
- Do not edit VM-specific files. VM roots move only when they are ordinary service roots under the exact legacy services root; VM execution identity remains the Firecracker plan's concern.
- Preserve the existing generic `yeet host set` behavior: custom migrations retain their source data. Automatic deletion is allowed only for a transaction Catch proves originated at the exact recorded historical default.
- Create the implementation branch with GitButler, run `but pull --check` first, and select only this plan's changes at every checkpoint.

## Transaction Contract

The host transaction has these durable phases:

```go
type hostStorageTransactionPhase string

const (
	hostStoragePhasePrepared       hostStorageTransactionPhase = "prepared"
	hostStoragePhaseServicesStopped hostStorageTransactionPhase = "services-stopped"
	hostStoragePhaseTargetReady    hostStorageTransactionPhase = "target-ready"
	hostStoragePhaseCatchSwitched  hostStorageTransactionPhase = "catch-switched"
	hostStoragePhaseValidated      hostStorageTransactionPhase = "validated"
	hostStoragePhaseCleanupPending hostStorageTransactionPhase = "cleanup-pending"
	hostStoragePhaseComplete       hostStorageTransactionPhase = "complete"
	hostStoragePhaseRolledBack     hostStorageTransactionPhase = "rolled-back"
)
```

For the approved migration, a journal such as `/root/yeet-data/migrations/host-storage/01JZQ2F6V6YJST7M24B8H8M4KB/transaction.json` is mirrored to `/var/lib/yeet/migrations/host-storage/01JZQ2F6V6YJST7M24B8H8M4KB/transaction.json`; non-root install homes use the same relative path. Both copies contain the same full transaction ID and phase. The old journal remains authoritative until the target Catch validates successfully.

---

### Task 1: Fresh `/var/lib/yeet` Defaults and Modes

**Files:**
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`
- Modify: `pkg/yeet/init_storage.go`
- Modify: `pkg/yeet/init_test.go`

**Interfaces:**
- Produces: `defaultCatchDataDir = "/var/lib/yeet"`, `prepareDataDirs(string) (catchPaths, error)`, and fresh-init defaults for `/var/lib/yeet` and `/var/lib/yeet/services`.
- Consumes: existing explicit `--data-dir`, `--services-root`, and ZFS resolution paths unchanged.

- [ ] **Step 1: Write failing default and mode tests**

Add table-driven tests that make the desired distinction explicit:

```go
func TestDefaultDataDirUsesVarLib(t *testing.T) {
	if got, want := defaultDataDir(), "/var/lib/yeet"; got != want {
		t.Fatalf("defaultDataDir() = %q, want %q", got, want)
	}
}

func TestPrepareDataDirsAppliesPublicTraversalAndPrivateStateModes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "var-lib-yeet")
	paths, err := prepareDataDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	assertPathMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil { t.Fatal(err) }
		if got := info.Mode().Perm(); got != want { t.Fatalf("%s mode = %04o, want %04o", path, got, want) }
	}
	assertPathMode(paths.dataDir, 0o711)
	assertPathMode(paths.servicesDir, 0o711)
	for _, name := range []string{"backups", "checkpoints", "migrations", "mounts", "registry", "tsd", "tsnet", "vm-images"} {
		assertPathMode(filepath.Join(root, name), 0o700)
	}
}

func TestInitStorageWizardDefaultsToVarLibYeet(t *testing.T) {
	p := &scriptedInitPrompter{confirmAnswers: []bool{true, true}}
	got, err := runInitStorageWizardWithPrompter(p, initStorageProbe{Home: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/var/lib/yeet" || got.ServicesRoot != "" {
		t.Fatalf("storage = %#v", got)
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

```bash
mise exec -- go test ./cmd/catch ./pkg/yeet -run 'Test(DefaultDataDirUsesVarLib|PrepareDataDirsAppliesPublicTraversalAndPrivateStateModes|InitStorageWizardDefaultsToVarLibYeet)' -count=1
```

Expected: the path tests report `$HOME/yeet-data`, and the mode tests report `0700` for paths that must be traversable.

- [ ] **Step 3: Implement the fresh defaults without rewriting explicit configuration**

Use constants and an explicit mode map rather than deriving security modes from one blanket `MkdirAll` call:

```go
const defaultCatchDataDir = "/var/lib/yeet"

func defaultDataDir() string { return defaultCatchDataDir }

func prepareDataDirs(dataDir string) (catchPaths, error) {
	paths := catchPaths{
		dataDir: dataDir,
		dbPath: filepath.Join(dataDir, "db.json"),
		registryDir: filepath.Join(dataDir, "registry"),
		servicesDir: filepath.Join(dataDir, "services"),
		mountsDir: filepath.Join(dataDir, "mounts"),
	}
	dirs := map[string]os.FileMode{
		paths.dataDir: 0o711,
		paths.servicesDir: 0o711,
		paths.registryDir: 0o700,
		paths.mountsDir: 0o700,
	}
	for _, name := range []string{"backups", "checkpoints", "migrations", "tsd", "tsnet", "vm-images"} {
		dirs[filepath.Join(dataDir, name)] = 0o700
	}
	for dir, mode := range dirs {
		_, statErr := os.Stat(dir)
		created := errors.Is(statErr, os.ErrNotExist)
		if statErr != nil && !created { return catchPaths{}, statErr }
		if err := os.MkdirAll(dir, mode); err != nil {
			return catchPaths{}, err
		}
		enforceDefault := filepath.Clean(dataDir) == defaultCatchDataDir
		if created || enforceDefault {
			if err := os.Chmod(dir, mode); err != nil {
				return catchPaths{}, err
			}
		}
	}
	return paths, nil
}
```

Spell the `Chmod` branch out with a named error in production code so the snippet's intended rule is unambiguous: enforce final modes for newly created directories and the managed `/var/lib/yeet` default, but do not silently rewrite an existing operator-selected custom tree during an ordinary Catch restart. Native identity preflight later reports traversal changes required for such a custom path.

In the init wizard, replace only the historical default:

```go
const defaultInitDataDir = "/var/lib/yeet"
const defaultCustomInitDataDir = "/srv/yeet"

func promptInitDataStorageWithPrompter(prompter yeetPrompter, probe initStorageProbe) (initStorageOptions, error) {
	storage := initStorageOptions{}
	useDefaultData, err := prompter.Confirm("Use /var/lib/yeet for catch data?", true)
	if err != nil {
		return initStorageOptions{}, err
	}
	if useDefaultData {
		storage.DataDir = defaultInitDataDir
		return storage, nil
	}
	return promptInitCustomDataStorageWithPrompter(prompter, storage, probe, defaultCustomInitDataDir)
}
```

Keep `resolveCatchStartupOptions` and `prepareInitStorageOptions` authoritative for explicit filesystem and ZFS values.

- [ ] **Step 4: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./cmd/catch ./pkg/yeet -run 'Test(DefaultDataDir|PrepareDataDirs|InitStorage)' -count=1
```

Expected: PASS, including existing explicit path and ZFS cases.

- [ ] **Step 5: Checkpoint only Task 1**

```bash
but status --short
```

Select only `cmd/catch/catch.go`, `cmd/catch/catch_test.go`, `pkg/yeet/init_storage.go`, and `pkg/yeet/init_storage_test.go` into `codex/var-lib-host-migration`, then create the checkpoint message `catch: default fresh state to var lib`.

---

### Task 2: Exact Legacy Detection, Estimates, and Cleanup Eligibility

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/client.go`
- Modify: `pkg/catchrpc/client_test.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `pkg/catch/host_storage_refs.go`
- Modify: `pkg/catch/host_storage_refs_test.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/catch_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**
- Produces: `HostStorageLegacyPlan`, `HostStorageEstimate`, `HostStoragePlan.Legacy`, and `HostStorageApplyResult.TransactionID`.
- Consumes: recorded `Config.InstallUser`, install metadata, `HostStorageState`, existing service-root/ZFS discovery, and existing stale-plan validation.

- [ ] **Step 1: Add failing RPC round-trip and planner tests**

Define the wire contract in tests first:

```go
type HostStorageEstimate struct {
	BytesToCopy uint64 `json:"bytesToCopy,omitempty"`
	BytesFree   uint64 `json:"bytesFree,omitempty"`
}

type HostStorageLegacyPlan struct {
	Eligible       bool     `json:"eligible,omitempty"`
	SourceRoot     string   `json:"sourceRoot,omitempty"`
	TargetRoot     string   `json:"targetRoot,omitempty"`
	PreservedRoots []string `json:"preservedRoots,omitempty"`
	CleanupAllowed bool     `json:"cleanupAllowed,omitempty"`
	BlockingMounts []string `json:"blockingMounts,omitempty"`
}
```

Cover all classifications:

```go
func TestHostStoragePlanMarksExactRecordedLegacyDefault(t *testing.T) {
	// Config.RootDir is /root/yeet-data and install metadata records both the
	// root install user and /root as that account's installation home.
	plan := planLegacyMove(t, "/root", "/root/yeet-data")
	if !plan.Legacy.Eligible || !plan.Legacy.CleanupAllowed {
		t.Fatalf("legacy = %#v", plan.Legacy)
	}
	if plan.Legacy.SourceRoot != "/root/yeet-data" || plan.Legacy.TargetRoot != "/var/lib/yeet" {
		t.Fatalf("legacy paths = %#v", plan.Legacy)
	}
}

func TestHostStoragePlanDoesNotGuessCustomPathIsLegacy(t *testing.T) {
	plan := planLegacyMove(t, "/root", "/srv/yeet")
	if plan.Legacy.Eligible || plan.Legacy.CleanupAllowed {
		t.Fatalf("custom root classified as legacy: %#v", plan.Legacy)
	}
}

func TestHostStoragePlanRejectsNestedMountDuringLegacyCleanup(t *testing.T) {
	plan, err := planLegacyMoveWithMount(t, "/root/yeet-data/mounts/media")
	if err == nil || !strings.Contains(err.Error(), "mounted path") {
		t.Fatalf("err = %v, plan = %#v", err, plan)
	}
}
```

Also verify `/root/data` is never cleanup-eligible, ZFS roots are preserved, explicit roots outside the old default service tree appear in `PreservedRoots`, byte estimates are stable, insufficient space fails before mutation, and JSON omits empty legacy metadata for old clients.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch -run 'Test(HostStorage.*Legacy|HostStorage.*Estimate|HostStorage.*NestedMount)' -count=1
```

Expected: compile failures because `HostStorageLegacyPlan`, `HostStorageEstimate`, and `HostStoragePlan.Legacy` do not exist.

- [ ] **Step 3: Add the typed plan fields and exact detector**

Extend the RPC types with these fields:

```go
type HostStoragePlan struct {
	Current             HostStorageState          `json:"current"`
	Desired             HostStorageState          `json:"desired"`
	DataDirAction       HostStorageDataDirAction  `json:"dataDirAction"`
	ServicesAction      HostStorageServicesAction `json:"servicesAction"`
	CatchAction         HostStorageCatchAction    `json:"catchAction"`
	RepairAction        HostStorageRepairAction   `json:"repairAction,omitempty"`
	ZFSDatasetsToCreate []string                  `json:"zfsDatasetsToCreate,omitempty"`
	Warnings            []string                  `json:"warnings,omitempty"`
	RequiresRestart     bool                      `json:"requiresRestart,omitempty"`
	Estimate            HostStorageEstimate       `json:"estimate,omitempty"`
	Legacy              HostStorageLegacyPlan     `json:"legacy,omitempty"`
}

type HostStorageApplyResult struct {
	TransactionID    string                   `json:"transactionId,omitempty"`
	MigratedServices []HostStorageServiceMove `json:"migratedServices,omitempty"`
	Restarted        bool                     `json:"restarted,omitempty"`
	RestartScheduled bool                     `json:"restartScheduled,omitempty"`
	Validation       HostStorageValidation    `json:"validation,omitempty"`
}
```

Implement exact classification around one helper:

```go
func legacyDefaultDataDir(installHome string) string {
	return filepath.Clean(filepath.Join(installHome, "yeet-data"))
}

func isExactLegacyDefault(current catchrpc.HostStorageState, installHome string) bool {
	return !current.DataDirZFS &&
		filepath.Clean(current.DataDir) == legacyDefaultDataDir(installHome)
}
```

Remove `/root/data` from any legacy-default helper. It may remain a repair reference for old databases, but it must never authorize recursive deletion.

Extend `installMeta` and `catch.Config` with `InstallHome`. During install or upgrade, resolve the selected install user's home with `os/user` and persist it in `install.json`; old metadata remains readable. If an old record has no home, the planner may re-resolve `InstallUser`, but it must make cleanup ineligible when that lookup fails rather than infer a parent directory from the current path.

Walk mountinfo and ZFS mountpoints before setting `CleanupAllowed`. If a mounted filesystem or nested dataset exists under the source, return a preflight error that names it and prints the existing explicit storage migration command. Calculate bytes with `Lstat` and do not descend into mountpoints; calculate free bytes with `unix.Statfs` at the nearest existing target parent.

- [ ] **Step 4: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch -run 'Test(HostStorage.*Legacy|HostStorage.*Estimate|HostStorage.*NestedMount|HostStoragePlan)' -count=1
```

Expected: PASS; existing generic host-storage plans remain cleanup-ineligible.

- [ ] **Step 5: Checkpoint only Task 2**

Use `but status --short`, select the eleven Task 2 files into `codex/var-lib-host-migration`, and checkpoint with `catch: classify exact legacy storage`.

---

### Task 3: Durable Host Transaction Journal and Rollback Inputs

**Files:**
- Create: `pkg/catch/host_storage_transaction.go`
- Create: `pkg/catch/host_storage_transaction_test.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/catch_test.go`

**Interfaces:**
- Produces: `hostStorageTransaction`, `createHostStorageTransaction`, `loadHostStorageTransaction`, `advanceHostStorageTransaction`, `rollbackHostStorageTransaction`, and `resumeHostStorageTransaction`.
- Consumes: the Task 2 plan, the current database path, generated unit paths, current running-state discovery, and existing host-storage mutation operations.

- [ ] **Step 1: Write failing journal durability and recovery tests**

Use a versioned record with no implicit fields:

```go
type hostStorageTransaction struct {
	Version          int                         `json:"version"`
	ID               string                      `json:"id"`
	Phase            hostStorageTransactionPhase `json:"phase"`
	Plan             catchrpc.HostStoragePlan    `json:"plan"`
	SourceJournal    string                      `json:"sourceJournal"`
	TargetJournal    string                      `json:"targetJournal,omitempty"`
	DatabaseBackup   string                      `json:"databaseBackup"`
	UnitBackups      map[string]string           `json:"unitBackups"`
	PreviouslyRunning []string                   `json:"previouslyRunning"`
	StoppedServices  []string                    `json:"stoppedServices,omitempty"`
	RestartedServices []string                   `json:"restartedServices,omitempty"`
	CatchAuthority   string                      `json:"catchAuthority"`
	LastError        string                      `json:"lastError,omitempty"`
}
```

Add tests for atomic creation, file and parent mode `0600`/`0700`, fsync before phase change returns, target-journal mirroring, corrupt journal rejection, old DB/unit restoration, prior running-state restoration, cleanup-pending resume, and startup refusal when an unfinished journal needs operator recovery.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestHostStorageTransaction' -count=1
```

Expected: compile failure because the transaction types and helpers do not exist.

- [ ] **Step 3: Implement the journal and explicit rollback state**

Use atomic write plus directory fsync for every phase transition:

```go
func hostStorageTransactionPath(dataDir, id string) string {
	return filepath.Join(dataDir, "migrations", "host-storage", id, "transaction.json")
}

func advanceHostStorageTransaction(tx *hostStorageTransaction, phase hostStorageTransactionPhase) error {
	tx.Phase = phase
	if err := writeHostStorageTransactionFile(tx.SourceJournal, tx); err != nil {
		return err
	}
	if tx.TargetJournal != "" {
		if err := writeHostStorageTransactionFile(tx.TargetJournal, tx); err != nil {
			return err
		}
	}
	return fsyncParent(tx.SourceJournal)
}

func writeHostStorageTransactionFile(path string, tx *hostStorageTransaction) error {
	raw, err := json.MarshalIndent(tx, "", "  ")
	if err != nil { return err }
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { return err }
	tmp, err := os.CreateTemp(filepath.Dir(path), ".transaction-*")
	if err != nil { return err }
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil { _ = tmp.Close(); return err }
	if _, err := tmp.Write(raw); err != nil { _ = tmp.Close(); return err }
	if err := tmp.Sync(); err != nil { _ = tmp.Close(); return err }
	if err := tmp.Close(); err != nil { return err }
	if err := os.Rename(tmpName, path); err != nil { return err }
	return fsyncParent(path)
}
```

Add `fsyncParent(path string) error` in the same file: open `filepath.Dir(path)`, call `Sync`, and close it while preserving both sync and close failures.

At creation time, copy the source DB to `db.before.json`, copy every affected systemd unit into `units/`, record the exact running service set, and write `CatchAuthority` as `source`. Do this before the first stop or path rewrite.

After copying legacy state into the target and before switching Catch, reapply the Task 1 target layout: `/var/lib/yeet` and `services` are `0711`; Catch control directories are root-owned `0700`; DB, install metadata, keys, and transaction files are root-owned `0600`. Old permissive or home-directory modes must not survive merely because the bytes came from the legacy tree.

Make rollback consume only the journal, not recomputed state:

```go
func (a *hostStorageApplier) rollbackHostStorageTransaction(ctx context.Context, tx *hostStorageTransaction, cause error, w io.Writer) error
```

It must stop replacement units, restore unit bytes and the DB backup, reload systemd, reinstall/restart Catch at the source when it had switched, restart only `PreviouslyRunning`, validate the source Catch and service state, mark `rolled-back`, and return an error containing both the original failure and rollback failure when both exist.

During Catch startup, scan both configured `RootDir` and the target recorded by any source journal. Automatically resume only `cleanup-pending`; for other incomplete phases, keep Catch serving read operations but reject new host-storage mutation with the journal path and a recovery command.

- [ ] **Step 4: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'TestHostStorage(Transaction|Rollback|Resume)' -count=1
```

Expected: PASS, including crash simulations after each journal phase.

- [ ] **Step 5: Checkpoint only Task 3**

Use `but status --short`, select the six Task 3 files, and checkpoint with `catch: journal host storage migrations`.

---

### Task 4: Restart, Reconnect, Finalize, and Exact Legacy Cleanup

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/client.go`
- Modify: `pkg/catchrpc/client_test.go`
- Modify: `pkg/catch/rpc.go`
- Modify: `pkg/catch/rpc_test.go`
- Modify: `pkg/catch/authz.go`
- Modify: `pkg/catch/authz_test.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/yeet/host_set.go`
- Modify: `pkg/yeet/host_set_test.go`
- Create: `pkg/yeet/host_cleanup.go`
- Create: `pkg/yeet/host_cleanup_test.go`
- Modify: `cmd/yeet/yeet.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `cmd/yeet/cli_bridge.go`
- Modify: `cmd/yeet/cli_bridge_test.go`

**Interfaces:**
- Produces: `catch.HostStorageFinalize`, `catch.HostStorageCleanup`, `yeet host cleanup --from=PATH --yes`, and client reconnect/finalization after Catch restart.
- Consumes: Task 3 transactions and existing `HostStoragePlan`/`HostStorageApply` methods.

- [ ] **Step 1: Add failing RPC, authorization, parser, and client-flow tests**

Define the final wire API:

```go
const (
	RPCMethodHostStorageFinalize = "catch.HostStorageFinalize"
	RPCMethodHostStorageCleanup  = "catch.HostStorageCleanup"
)

type HostStorageFinalizeRequest struct {
	TransactionID string `json:"transactionId"`
}

type HostStorageFinalizeResult struct {
	TransactionID string                `json:"transactionId"`
	Validation    HostStorageValidation `json:"validation"`
	CleanupPending bool                 `json:"cleanupPending,omitempty"`
}

type HostStorageCleanupRequest struct {
	From string `json:"from"`
	Yes  bool   `json:"yes"`
}

type HostStorageCleanupResult struct {
	TransactionID string `json:"transactionId"`
	Removed       string `json:"removed"`
}
```

Tests must prove:

```go
func TestRPCMethodPermissionsHostStorageFinalizeAndCleanupRequireManage(t *testing.T) {
	for _, method := range []string{catchrpc.RPCMethodHostStorageFinalize, catchrpc.RPCMethodHostStorageCleanup} {
		got, err := rpcMethodPermissions(method)
		if err != nil || !got.has(permissionManage) || got.has(permissionSSH) {
			t.Fatalf("%s permissions = %v, %v", method, got, err)
		}
	}
}

func TestHostStorageCleanupRejectsUnjournaledSource(t *testing.T) {
	_, err := server.CleanupHostStorage(ctx, catchrpc.HostStorageCleanupRequest{From: "/srv/unjournaled", Yes: true})
	if err == nil || !strings.Contains(err.Error(), "validated host storage transaction") {
		t.Fatalf("err = %v", err)
	}
}
```

Also cover reconnect retry with bounded backoff, target Catch path validation, stale transaction IDs, cleanup without `--yes`, cleanup before finalize, explicit cleanup of a validated generic custom migration, symlink source rejection, mount/device boundary refusal, deletion failure leaving `cleanup-pending`, and a second cleanup call resuming successfully.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/cli ./pkg/yeet -run 'Test(HostStorageFinalize|HostStorageCleanup|RPCMethodPermissionsHostStorage|RunHostSetReconnect)' -count=1
```

Expected: compile failures for the new RPC methods and `host cleanup` parser.

- [ ] **Step 3: Implement server finalization and cleanup**

Register both methods in `dispatchRPCWithContext`, classify both as `manage`, and expose:

```go
func (s *Server) FinalizeHostStorage(ctx context.Context, req catchrpc.HostStorageFinalizeRequest) (catchrpc.HostStorageFinalizeResult, error)
func (s *Server) CleanupHostStorage(ctx context.Context, req catchrpc.HostStorageCleanupRequest) (catchrpc.HostStorageCleanupResult, error)
```

`FinalizeHostStorage` loads the target journal, verifies Catch reports the desired paths, verifies every regenerated unit has no active source reference, verifies each previously running service is active, and verifies intentionally stopped services remain stopped. It advances to `cleanup-pending` for cleanup-eligible exact legacy transactions, `validated` for a generic migration that retains a journaled source tree, and `complete` when there is no old tree to clean.

If final validation fails before the transaction reaches `validated`, invoke `rollbackHostStorageTransaction`, reconnect to Catch at the source path, and report both validation and rollback results. Once validation has advanced the transaction to `cleanup-pending`, deletion failure does not roll the healthy host back.

`CleanupHostStorage` resolves a validated transaction by canonical `From`, requires `Yes`, re-runs all final validation, rejects symlinks and any device or mount crossing, and removes with a file-descriptor-relative walker rooted at the exact journaled source. Never call `os.RemoveAll` on an unchecked string. The init/upgrade flow invokes it automatically only for `Legacy.CleanupAllowed`; a generic custom migration requires its own later explicit `host cleanup` command. After deletion, mark the target journal `complete` and keep the small target-side transaction record as audit state.

- [ ] **Step 4: Implement client reconnect and the cleanup command**

Extend the client interface:

```go
type hostStorageClient interface {
	HostStoragePlan(context.Context, catchrpc.HostStoragePlanRequest) (catchrpc.HostStoragePlan, error)
	HostStorageApply(context.Context, catchrpc.HostStorageApplyRequest) (catchrpc.HostStorageApplyResult, error)
	HostStorageFinalize(context.Context, catchrpc.HostStorageFinalizeRequest) (catchrpc.HostStorageFinalizeResult, error)
	HostStorageCleanup(context.Context, catchrpc.HostStorageCleanupRequest) (catchrpc.HostStorageCleanupResult, error)
}
```

When apply returns `TransactionID`, wait up to 60 seconds for the same Catch identity to answer, then call finalize. Use exponential delays capped at two seconds and print the current phase. Add `HostCleanupFlags{From string; Yes bool}`. `--from` is always required; `--yes` skips an interactive confirmation. The command is:

```text
yeet host cleanup --from=/root/yeet-data --yes
```

It must render the exact path and ask interactively when `--yes` is absent; non-interactive use without `--yes` fails before RPC.

- [ ] **Step 5: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/cli ./pkg/yeet -run 'Test(HostStorageFinalize|HostStorageCleanup|RPCMethodPermissionsHostStorage|RunHostSetReconnect|ParseHostCleanup)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Checkpoint only Task 4**

Use `but status --short`, select the twenty Task 4 files, and checkpoint with `host: finalize and clean storage migrations`.

---

### Task 5: Prompted Init/Upgrade Migration and Non-Interactive Guidance

**Files:**
- Modify: `pkg/yeet/init.go`
- Modify: `pkg/yeet/init_test.go`
- Modify: `pkg/yeet/init_storage.go`

**Interfaces:**
- Produces: post-upgrade `runInitLegacyStorageMigration`, one consent prompt for apply/finalize/cleanup, and exact non-interactive commands.
- Consumes: Tasks 2-4 RPCs, the already-upgraded Catch still running from its old configured path, and terminal detection.

- [ ] **Step 1: Add failing fresh, upgrade, decline, and non-interactive tests**

Represent the post-install candidate explicitly:

```go
type initLegacyStorageCandidate struct {
	Eligible   bool
	SourceRoot string
	TargetRoot string
	Plan       catchrpc.HostStoragePlan
}
```

Test this sequence:

```go
func TestFinalizeInitCatchUpgradesBeforeLegacyMigration(t *testing.T) {
	var calls []string
	installInitCatchWithTailscaleRetryFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ initOptions) error {
		calls = append(calls, "install")
		return nil
	}
	runInitLegacyStorageMigrationFn = func(_ *initUI, _ string, _ initStorageOptions) error {
		calls = append(calls, "migrate")
		return nil
	}
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)
	if err := finalizeInitCatch(ui, "root@example.com", "amd64", false, false, false, initOptions{}); err != nil { t.Fatal(err) }
	if diff := cmp.Diff([]string{"install", "migrate"}, calls); diff != "" { t.Fatal(diff) }
}
```

Add these injectable function variables with the exact signatures used by the test:

```go
var installInitCatchWithTailscaleRetryFn = installInitCatchWithTailscaleRetry
var runInitLegacyStorageMigrationFn = runInitLegacyStorageMigration
```

Cover fresh install to `/var/lib/yeet` with no migration prompt, exact `/home/ubuntu/yeet-data`, custom `/srv/yeet` with no prompt, ZFS with no prompt, decline preserving the old host, non-interactive output, apply failure rollback text, cleanup failure reported as inactive sensitive state, and rerun resuming cleanup.

- [ ] **Step 2: Run focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet -run 'Test(FinalizeInitCatch.*Legacy|InitLegacyStorage|InitStorage.*VarLib)' -count=1
```

Expected: compile failures for `initLegacyStorageCandidate` and `runInitLegacyStorageMigrationFn`.

- [ ] **Step 3: Implement post-upgrade planning and explicit consent**

Keep the old storage options for the Catch install. After the upgraded Catch is healthy, request this plan:

```go
catchrpc.HostStoragePlanRequest{Set: catchrpc.HostStorageSetRequest{
	DataDir: &catchrpc.HostStorageTarget{Value: "/var/lib/yeet"},
	ServicesRoot: &catchrpc.HostStorageTarget{Value: "/var/lib/yeet/services"},
	MigrateServices: catchrpc.HostStorageMigrateAll,
}}
```

Continue only when `plan.Legacy.Eligible && plan.Legacy.CleanupAllowed`. Render old/new paths, affected and preserved services, byte estimate, target free space, running services, and rollback boundary. Ask:

```text
Move Yeet's legacy state to /var/lib/yeet and remove the old tree after verification? [Y/n]
```

On yes, call apply, reconnect/finalize, then cleanup with the journaled source. On no, leave the upgraded Catch on the old path and print the commands.

For non-interactive init/upgrade, print exactly:

```text
Legacy Yeet state remains at /root/yeet-data.
Migrate it explicitly:
  yeet host set --data-dir=/var/lib/yeet --services-root=/var/lib/yeet/services --migrate-services=all --yes
After Catch reconnects and validates the new state, remove the inactive legacy tree:
  yeet host cleanup --from=/root/yeet-data --yes
```

Substitute the recorded install home; do not assume `/root`.

- [ ] **Step 4: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet -run 'Test(FinalizeInitCatch.*Legacy|InitLegacyStorage|InitStorage.*VarLib|Init.*NonInteractive)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Checkpoint only Task 5**

Use `but status --short`, select the three Task 5 files, and checkpoint with `yeet: offer legacy var lib migration`.

---

### Task 6: User Documentation, Full Verification, and Disposable-Host Proof

**Files:**
- Modify: `README.md`
- Modify: `website/docs/getting-started/host-setup.mdx`
- Modify: `website/docs/getting-started/first-run-validation.mdx`
- Modify: `website/docs/concepts/service-types.mdx`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`

**Interfaces:**
- Produces: accurate help/manual text for defaults, upgrade prompt, explicit migration, cleanup, custom roots, and ZFS preservation.
- Consumes: the completed host-storage behavior from Tasks 1-5.

- [ ] **Step 1: Add failing help and documentation assertions**

Add exact CLI assertions for:

```text
--data-dir string         Catch state directory (default /var/lib/yeet)
--services-root string    Default service root (default: data directory/services)
yeet host cleanup --from=PATH [--yes]
```

Add documentation checks that require `/var/lib/yeet`, `yeet host cleanup`, `custom roots are preserved`, and `ZFS datasets are not copied or deleted implicitly`, while rejecting new examples that recommend `$HOME/yeet-data`.

- [ ] **Step 2: Run doc-facing tests and confirm RED**

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'Test.*(Help|Usage|HostCleanup)' -count=1
```

Expected: FAIL because the help does not yet advertise the new default and cleanup command.

- [ ] **Step 3: Update README and website manual**

Document the operator workflow verbatim:

```bash
yeet init root@host

# Explicit migration when init could not prompt:
yeet host set \
  --data-dir=/var/lib/yeet \
  --services-root=/var/lib/yeet/services \
  --migrate-services=all \
  --yes
yeet host cleanup --from=/root/yeet-data --yes
```

Explain that the cleanup command refuses arbitrary paths, verifies the new Catch and service state again, and resumes safely after deletion-only failure. Keep examples on `yeetrun.com` and do not publish private hostnames.

- [ ] **Step 4: Commit and publish the website change only after approval**

Run website checks available in the submodule. Commit and push inside `website/` only when the user authorizes publication; then include the exact published website gitlink in the root implementation branch. Until then, keep the website edit uncommitted and report that state distinctly.

- [ ] **Step 5: Run repository gates**

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/cli ./pkg/yeet ./cmd/catch ./cmd/yeet -count=1
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: every command exits 0. Fix the implementation or tests rather than weakening a gate or refreshing a baseline.

- [ ] **Step 6: Prove both success and rollback on disposable systemd hosts**

On an ordinary filesystem host, create an exact legacy installation containing one running native service, one stopped service, one Docker service, Catch registry state, and a custom service root outside the default tree. Record `systemctl is-active`, database/service roots, inode ownership, and checksums. Upgrade with an interactive TTY, accept the migration, and verify:

```bash
test -d /var/lib/yeet
test ! -e /root/yeet-data
systemctl is-active catch.service
systemctl is-active migration-running.service
systemctl is-enabled migration-stopped.service
```

Repeat with an injected failure after Catch switch and prove Catch, units, paths, and prior running state return to the legacy tree. On a ZFS-capable disposable host, prove a custom dataset and a nested dataset are preserved and the guided cleanup refuses to cross either boundary.

- [ ] **Step 7: Final checkpoint**

Run `but pull --check`, inspect `but diff` for private host data and unrelated VM/ISO changes, and checkpoint only this plan's root files plus the published website gitlink with `host: migrate legacy state to var lib`.

Do not push or land on `main` without the user's explicit authorization.

## Completion Checklist

- Fresh Catch installs default to `/var/lib/yeet`; explicit filesystem and ZFS roots still win.
- The prompt appears only for the exact recorded `$HOME/yeet-data` default.
- The upgraded Catch stays on the old path until the transaction begins.
- Apply, Catch restart, reconnect, validation, and cleanup are distinct durable phases.
- Generic host migrations never gain implicit deletion.
- Exact legacy cleanup cannot follow symlinks, cross mounts, or delete an arbitrary path.
- Every pre-commit failure rolls back; deletion-only failure remains cleanup-pending and resumable.
- Running/stopped state, custom roots, Docker services, VM service roots, and ZFS preservation have tests.
- `HostStorageFinalize` and `HostStorageCleanup` require `manage` permission.
- README, CLI help, and website manual agree.
