# Tailscale Resolver Fleet Transaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate every persisted Tailscale sidecar to fail-closed resolver isolation without rejecting valid historical generations or leaving a partially changed fleet.

**Architecture:** A deterministic read-only planner classifies each persisted sidecar as one of two exact managed generations and rejects the whole fleet before mutation if any input is invalid. A resolver-specific durable journal then applies every unit change as one fleet transaction, verifies every prior lifecycle state, and either commits the complete fleet or restores it. All later sidecar activation paths share the same guarded-readiness check and stop a replacement process whose verification fails.

**Tech Stack:** Go, JSONL transaction journals, systemd, Linux mount/process verification, Go `testing`, GitButler, mise.

## Global Constraints

- Accept exactly two persisted tuples: historical `run/tailscaled` + `run/tailscaled.env` + `run/tailscaled.json`, or current `bin/tailscaled` + `env/tailscaled.env` + `env/tailscaled.json`.
- Preserve the selected tuple; do not normalize, regenerate, copy, or synthesize another generation during resolver isolation.
- Require the exact stable Catch runner already configured by the server.
- Require `--statedir=.`, the managed run-directory socket, the persisted plain TUN interface, exact argument order, the managed Tailscale working directory, and the exact service namespace.
- Require canonical and installed units to select the same tuple and require managed artifact provenance.
- Require every selected Tailscale config to have `acceptDNS=false`.
- Derive the resolver source from the validated namespace and install it with `BindReadOnlyPaths` plus `PrivateMounts=yes`.
- Complete full-fleet preflight and input revalidation before the first write, stop, reload, restart, or verification action.
- Make the journal durable before the first write.
- Restore every file, mode, owner, and exact active/inactive state on failure; if recovery cannot be proven, retain the journal, block service mutations, and stop unproven replacement sidecars.
- Add no dependency, CLI command, RPC method, permission mapping, or user-facing flag.
- Use TDD: run every named RED before production edits and every named GREEN afterward.
- Run Go commands through `mise exec -- go`.
- Preserve unrelated GitButler skill-updater changes and exclude them from every task commit.
- Keep tests, diagnostics, commits, and release artifacts free of private infrastructure names, local user paths, tailnet domains, secrets, and retained live-environment locations.

---

## File Map

- Create `pkg/catch/tailscale_resolver_plan.go`: persisted-generation classification, deterministic fleet planning, artifact proofs, and revalidation.
- Create `pkg/catch/tailscale_resolver_plan_test.go`: complete historical/current matrix, mixed-layout rejection, deterministic ordering, provenance, and zero-side-effect tests.
- Create `pkg/catch/tailscale_resolver_live_test.go`: opt-in, environment-configured read-only live preflight with aggregate-only output.
- Create `pkg/catch/tailscale_resolver_journal.go`: resolver-specific journal schema, durable append/load/validation, and cleanup.
- Create `pkg/catch/tailscale_resolver_transaction.go`: apply, fleet rollback, lifecycle restoration, and commit.
- Create `pkg/catch/tailscale_resolver_recovery.go`: startup recovery and global mutation blocking.
- Create `pkg/catch/tailscale_resolver_transaction_test.go`: write/reload/restart/verify/rollback fault matrix.
- Create `pkg/catch/tailscale_resolver_recovery_test.go`: crash-phase recovery, corrupt-journal rejection, and mutation blocking.
- Create `pkg/catch/tailscale_resolver_readiness.go`: shared activation precondition.
- Create `pkg/catch/tailscale_resolver_readiness_test.go`: ordinary start, identity, root, and update readiness tests.
- Modify `pkg/catch/netns_reconcile.go`: replace best-effort repairs with plan/apply orchestration while retaining strict parser and guard rewrite.
- Modify `pkg/catch/netns_reconcile_test.go`: remove partial-commit expectations and retain unit-rewrite/parser coverage.
- Modify `pkg/catch/catch.go`: recover before startup migration and store resolver recovery state.
- Modify `pkg/catch/tty_service.go`: gate start, restart, and enable under the existing service-operation lock.
- Modify `pkg/catch/service_identity_migration.go`: gate sidecar activation inside the existing identity transaction.
- Modify `pkg/catch/service_root_migration.go`: validate guarded readiness before committing a reinstalled migrated generation.
- Modify `pkg/catch/tty_ops.go`: gate Tailscale update before binary replacement.
- Modify `pkg/catch/tty_ops_update_test.go`: prove update compatibility for both tuples and blocked readiness.
- Modify `pkg/svc/systemd_tailscale.go`: stop a just-started/restarted sidecar when final verification fails.
- Modify `pkg/svc/systemd_tailscale_test.go`: prove verification-failure cleanup.
- Modify `website/docs/changelog.mdx`: add the final patch-release entry only after all custom and published validation is green.

### Task 1: Exact Generation Classifier and Read-Only Fleet Preflight

**Files:**
- Create: `pkg/catch/tailscale_resolver_plan.go`
- Create: `pkg/catch/tailscale_resolver_plan_test.go`
- Create: `pkg/catch/tailscale_resolver_live_test.go`
- Modify: `pkg/catch/netns_reconcile.go`
- Modify: `pkg/catch/netns_reconcile_test.go`

**Interfaces:**
- Produces:

```go
type tailscaleResolverGenerationLayout string

const (
	tailscaleResolverGenerationHistorical tailscaleResolverGenerationLayout = "historical-run"
	tailscaleResolverGenerationCurrent    tailscaleResolverGenerationLayout = "current-bin-env"
)

type tailscaleResolverGeneration struct {
	Layout           tailscaleResolverGenerationLayout
	Daemon           string
	EnvironmentFile  string
	ConfigFile       string
	SocketFile       string
	WorkingDirectory string
	Interface        string
	Args             []string
}

type tailscaleResolverServiceRecordProof struct {
	Generation        int
	LatestGeneration  int
	ServiceRoot       string
	Interface         string
	TSServiceArtifact string
	TSBinaryArtifact  string
	TSEnvArtifact     string
	TSConfigArtifact  string
}

type tailscaleResolverUnitFilePlan struct {
	Path     string
	Original []byte
	Next     []byte
	Proof    serviceIdentityPathProof
}

type tailscaleResolverServicePlan struct {
	ServiceName string
	UnitName    string
	WasActive   bool
	Record      tailscaleResolverServiceRecordProof
	Generation  tailscaleResolverGeneration
	Files       []tailscaleResolverUnitFilePlan
}

type tailscaleResolverFleetPlan struct {
	Services []tailscaleResolverServicePlan
}

func (s *Server) planTailscaleResolverIsolationFleet(
	ctx context.Context,
	dv *db.DataView,
) (tailscaleResolverFleetPlan, error)

func (s *Server) revalidateTailscaleResolverFleetPlan(
	ctx context.Context,
	plan tailscaleResolverFleetPlan,
) error
```

- Extends `tailscaleResolverUnit` with `environmentFile` and `workingDirectory`.
- Consumes `captureServiceIdentityPathProof`, `ensureTailscaleUnitResolverIsolation`, the persisted `db.Service`, and the exact stable Catch runner.
- `planTailscaleResolverIsolationFleet` is read-only; it may read/stat/hash and query active state, but may not call write or systemctl mutation functions.

- [ ] **Step 1: Write the complete classifier RED tests**

Add these named tests:

```go
func TestPlanTailscaleResolverIsolationFleetAcceptsCompleteHistoricalAndCurrentGenerations(t *testing.T)
func TestPlanTailscaleResolverIsolationFleetAcceptsMultipleHistoricalServices(t *testing.T)
func TestPlanTailscaleResolverIsolationFleetAcceptsHistoricalWritableAndMissingBinds(t *testing.T)
func TestPlanTailscaleResolverIsolationFleetRejectsEveryMixedGenerationTuple(t *testing.T)
func TestPlanTailscaleResolverIsolationFleetRejectsWrongEnvWorkingSocketTunOrOrder(t *testing.T)
func TestPlanTailscaleResolverIsolationFleetRejectsArtifactProvenanceMismatch(t *testing.T)
func TestPlanTailscaleResolverIsolationFleetRequiresAcceptDNSFalse(t *testing.T)
```

Use table rows that assert the complete selected tuple, not only the daemon.
For every accepted row, assert `Next` keeps the original daemon, environment
file, config, socket, working directory, and ordered TUN argument while
replacing writable/absent resolver state with the exact guarded read-only
directives.

- [ ] **Step 2: Run classifier tests and verify RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestPlanTailscaleResolverIsolationFleet(Accepts|Rejects|Requires)' -count=1
```

Expected: FAIL to compile because the planner and generation types do not
exist. After only test scaffolding compiles, historical rows fail with the
current env-config expectation.

- [ ] **Step 3: Write deterministic preflight and stale-input RED tests**

```go
func TestPlanTailscaleResolverIsolationFleetInvalidServiceHasZeroSideEffects(t *testing.T)
func TestPlanTailscaleResolverIsolationFleetOrderIsDeterministic(t *testing.T)
func TestTailscaleResolverFleetPlanRevalidateRejectsChangedDBOrFile(t *testing.T)
func TestTailscaleResolverLiveReadOnlyPreflight(t *testing.T)
```

The zero-side-effect test must install counters for every write, stop,
daemon-reload, restart, and verification dependency and require all counters
to remain zero when the last service is invalid. Run the planner with at least
three input permutations and require lexically identical plans/errors.

The live test must skip unless both `YEET_LIVE_DATA_DIR` and
`YEET_LIVE_SERVICES_ROOT` are set, construct a read-only `db.Store`, call only
the planner, and log counts by layout without service names or paths.

- [ ] **Step 4: Run preflight tests and verify RED**

```bash
mise exec -- go test ./pkg/catch -run 'Test(PlanTailscaleResolverIsolationFleetInvalidServiceHasZeroSideEffects|PlanTailscaleResolverIsolationFleetOrderIsDeterministic|TailscaleResolverFleetPlanRevalidateRejectsChangedDBOrFile)' -count=1
```

Expected: FAIL because current collection stops invalid services, iterates a
map directly, and has no fleet revalidation.

- [ ] **Step 5: Implement the minimal exact classifier**

In `pkg/catch/tailscale_resolver_plan.go`, implement one exact tuple builder:

```go
func expectedTailscaleResolverGeneration(
	service db.Service,
	layout tailscaleResolverGenerationLayout,
) (tailscaleResolverGeneration, error)
```

For historical layout, select run daemon/env/config. For current layout,
select bin daemon and env env/config. Both use statedir `.`, run socket, the
persisted plain interface, and `<root>/tailscale`. Compare all fields exactly;
do not accept aliases, optional reordering, or a mixed tuple.

Capture proofs for canonical unit, installed unit, selected daemon, env,
config, and their generation artifacts. Require regular single-link managed
files, matching selected runtime/generation hashes, and explicit
`acceptDNS=false`. Derive the resolver source from the validated namespace.

- [ ] **Step 6: Implement deterministic full-fleet planning**

Collect candidate names, sort them, build every service plan in memory, join
all validation errors, and return no plan if any service fails. Remove the
collection-time call to `failClosedTailscaleResolverIsolationRepair`.

Implement `revalidateTailscaleResolverFleetPlan` by loading a fresh database
view, comparing every `tailscaleResolverServiceRecordProof`, recapturing every
file proof and active/inactive state, and checking the context immediately
before apply. Any difference returns a stale-plan error before mutation.

- [ ] **Step 7: Run Task 1 GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'Test(PlanTailscaleResolverIsolationFleet|TailscaleResolverFleetPlanRevalidate|EnsureTailscaleResolverIsolation)' -count=1
```

Expected: PASS. The opt-in live test remains skipped without its two
environment variables.

- [ ] **Step 8: Commit Task 1**

```bash
but diff
but commit codex/tailscale-resolver-v0108 -m "catch: preflight tailscale resolver fleet"
```

Expected: one reviewable commit containing only Task 1 files; unrelated
GitButler updater files remain uncommitted.

### Task 2: Durable Fleet Journal, Apply, Rollback, and Recovery

**Files:**
- Create: `pkg/catch/tailscale_resolver_journal.go`
- Create: `pkg/catch/tailscale_resolver_transaction.go`
- Create: `pkg/catch/tailscale_resolver_recovery.go`
- Create: `pkg/catch/tailscale_resolver_transaction_test.go`
- Create: `pkg/catch/tailscale_resolver_recovery_test.go`
- Modify: `pkg/catch/netns_reconcile.go`
- Modify: `pkg/catch/catch.go`

**Interfaces:**
- Consumes Task 1's `tailscaleResolverFleetPlan` and `revalidate`.
- Produces:

```go
const tailscaleResolverJournalVersion = 1

type tailscaleResolverJournalHeader struct {
	Version  int                              `json:"version"`
	ID       string                           `json:"id"`
	Services []tailscaleResolverJournalService `json:"services"`
	Files    []tailscaleResolverJournalFile    `json:"files"`
}

type tailscaleResolverJournalService struct {
	ServiceName string `json:"serviceName"`
	UnitName    string `json:"unitName"`
	WasActive   bool   `json:"wasActive"`
}

type tailscaleResolverJournalFile struct {
	Path          string                   `json:"path"`
	Original      []byte                   `json:"original"`
	Next          []byte                   `json:"next"`
	OriginalProof serviceIdentityPathProof `json:"originalProof"`
}

func (s *Server) applyTailscaleResolverIsolationFleet(
	ctx context.Context,
	plan tailscaleResolverFleetPlan,
) error

func (s *Server) recoverTailscaleResolverIsolation(ctx context.Context) error
func (s *Server) checkTailscaleResolverMutationAllowed() error
```

- Journal phases are exactly `prepared`, `files-written`,
  `daemon-reloaded`, `services-verified`, and `committed`.
- Reuse existing path-proof, directory-sync, and atomic unit-write patterns
  directly; do not generalize the service-identity journal.

- [ ] **Step 1: Write transaction failure RED tests**

```go
func TestTailscaleResolverFleetTransactionRollsBackEveryWriteFailure(t *testing.T)
func TestTailscaleResolverFleetTransactionRollsBackDaemonReloadFailure(t *testing.T)
func TestTailscaleResolverFleetTransactionRollsBackFirstMiddleAndLastVerificationFailure(t *testing.T)
func TestTailscaleResolverFleetTransactionPreservesExactActiveAndInactiveState(t *testing.T)
func TestTailscaleResolverFleetTransactionRevalidatesBeforeFirstWrite(t *testing.T)
```

Each failure test must use at least three services and compare every original
byte slice, mode, owner proof, and active flag after rollback.

- [ ] **Step 2: Run transaction tests and verify RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestTailscaleResolverFleetTransaction' -count=1
```

Expected: FAIL to compile because the transaction and journal do not exist.

- [ ] **Step 3: Write crash recovery and mutation-block RED tests**

```go
func TestRecoverTailscaleResolverIsolationFromEveryNonTerminalPhase(t *testing.T)
func TestRecoverTailscaleResolverIsolationRemovesCommittedJournal(t *testing.T)
func TestRecoverTailscaleResolverIsolationRejectsCorruptDuplicateOrSymlinkJournal(t *testing.T)
func TestRecoverTailscaleResolverIsolationBlocksMutationsWhenRollbackCannotBeProven(t *testing.T)
func TestRecoverTailscaleResolverIsolationStopsUnprovenReplacementSidecars(t *testing.T)
func TestRecoverTailscaleResolverIsolationAllowsReadAndExplicitRecoveryWhileBlocked(t *testing.T)
```

Expected recovery is idempotent: running it twice yields the same restored
files and active states and leaves no journal after proven recovery.

- [ ] **Step 4: Run recovery tests and verify RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestRecoverTailscaleResolverIsolation' -count=1
```

Expected: FAIL because Catch has no resolver journal discovery or recovery
block.

- [ ] **Step 5: Implement the durable resolver journal**

Create a root-owned `0600` JSONL journal below
`<data-dir>/migrations/tailscale-resolver/`. Validate version, unique clean
managed paths, unique services, single-link regular journal metadata, maximum
record size, and legal phase order. Fsync the header and parent directory
before returning from creation; fsync every phase record before its side
effect.

- [ ] **Step 6: Implement fleet apply and full rollback**

Acquire all planned service-operation locks in lexical order, revalidate, then
create the journal. Atomically write all files, perform one daemon reload,
restart and verify only prior-active services, prove inactive services stayed
inactive, and append `committed` only after complete verification.

On any failure, stop transaction-touched replacements, restore every original
file with its proof/mode/owner, reload systemd, restore exact lifecycle state,
and verify the restored fleet. Remove the journal only after proven commit or
proven rollback.

- [ ] **Step 7: Implement startup recovery and global blocking**

Add resolver recovery state to `Server`. Discover and recover resolver
journals in `NewUnstartedServer` before startup reconciliation. If any journal
is corrupt or rollback cannot be proven, retain it, set one global resolver
mutation block, and keep unproven replacements stopped.

Enforce the block at the central Catch `manage` operation boundary and at
internal/background service mutation entrypoints that bypass that boundary.
Read-only status, logs, diagnostics, and the explicit startup recovery path
must remain available. This reuses existing permission classification; it adds
no command, RPC, or permission.

Replace the old write/reload/restart loop in
`reconcileTailscaleResolverIsolation` with:

```go
plan, err := s.planTailscaleResolverIsolationFleet(ctx, dv)
if err != nil {
	return err
}
return s.applyTailscaleResolverIsolationFleet(ctx, plan)
```

- [ ] **Step 8: Run Task 2 GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'Test(TailscaleResolverFleetTransaction|RecoverTailscaleResolverIsolation|ReconcileTailscaleResolverIsolation)' -count=1
```

Expected: PASS, including every injected phase failure and crash recovery.

- [ ] **Step 9: Commit Task 2**

```bash
but diff
but commit codex/tailscale-resolver-v0108 -m "catch: transact tailscale resolver fleet"
```

Expected: one journal/transaction/recovery commit with no unrelated files.

### Task 3: Shared Guarded-Readiness Activation Boundary

**Files:**
- Create: `pkg/catch/tailscale_resolver_readiness.go`
- Create: `pkg/catch/tailscale_resolver_readiness_test.go`
- Modify: `pkg/catch/tty_service.go`
- Modify: `pkg/catch/service_identity_migration.go`
- Modify: `pkg/catch/service_root_migration.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_ops_update_test.go`
- Modify: `pkg/svc/systemd_tailscale.go`
- Modify: `pkg/svc/systemd_tailscale_test.go`

**Interfaces:**
- Consumes Task 1 classification and Task 2 global mutation block.
- Produces:

```go
func (s *Server) ensureTailscaleResolverReadyForActivation(
	ctx context.Context,
	serviceName string,
) error

func (s *Server) ensureTailscaleResolverReadyForRecord(
	ctx context.Context,
	service db.Service,
) error
```

- Both functions are read-only and return success only when canonical and
  installed units are already the exact guarded form, selected artifacts are
  proven, and `acceptDNS=false`.

- [ ] **Step 1: Write activation/readiness RED tests**

```go
func TestTailscaleResolverReadinessGatesStartRestartAndEnable(t *testing.T)
func TestTailscaleResolverReadinessGatesServiceIdentityActivation(t *testing.T)
func TestTailscaleResolverReadinessGatesServiceRootCommit(t *testing.T)
func TestTailscaleResolverReadinessGatesUpdateBeforeBinaryReplacement(t *testing.T)
func TestTailscaleResolverReadinessAcceptsHistoricalAndCurrentGuardedTuples(t *testing.T)
func TestTailscaleResolverVerificationFailureStopsReplacement(t *testing.T)
```

Every rejected path must assert zero start/restart/enable/binary-copy calls.
The verification-failure test must assert the just-started unit is stopped.

- [ ] **Step 2: Run readiness tests and verify RED**

```bash
mise exec -- go test ./pkg/catch ./pkg/svc -run 'TestTailscaleResolver(Readiness|VerificationFailure)' -count=1
```

Expected: FAIL because activation paths do not share a precondition and
`StartTailscaleSidecar`/`RestartTailscaleSidecar` leave a failed replacement
running.

- [ ] **Step 3: Implement the read-only readiness helper**

Build a single-service plan using the same Task 1 classifier. Return an error
if the plan contains a file change, if the global recovery block is set, or if
installed/canonical proofs no longer match. Never apply migration from an
activation path.

- [ ] **Step 4: Wire every Catch activation path**

Under existing service locks:

- call readiness before ordinary start, restart, and enable;
- call readiness before the identity transaction starts the Tailscale unit;
- call record-based readiness after migrated-root unit installation and before
  database commit;
- call readiness before Tailscale update replaces the selected daemon.

Do not add command syntax, RPC fields, or permissions.

- [ ] **Step 5: Stop failed replacements in `pkg/svc`**

After a successful systemctl start/restart, if final sidecar verification
fails, issue `systemctl stop <tailscale-unit>` and return
`errors.Join(verificationErr, stopErr)`. A successful verification keeps the
existing behavior.

- [ ] **Step 6: Run Task 3 GREEN**

```bash
mise exec -- go test ./pkg/catch ./pkg/svc -run 'Test(TailscaleResolverReadiness|TailscaleResolverVerificationFailure|ApplyTSUpdate|ServiceIdentity.*Tailscale|ServiceRoot.*Tailscale|StartTailscaleSidecar|RestartTailscaleSidecar)' -count=1
```

Expected: PASS for both exact tuples and every guarded activation boundary.

- [ ] **Step 7: Commit Task 3**

```bash
but diff
but commit codex/tailscale-resolver-v0108 -m "catch: gate tailscale sidecar activation"
```

Expected: one activation-boundary commit with no CLI/RPC/permission changes.

### Task 4: Release Quality, Exact-Candidate Review, and Read-Only Live Preflight

**Files:**
- Verify: all Task 1-3 files
- Verify: `pkg/catch/...`
- Verify: `pkg/svc/...`

**Interfaces:**
- Consumes the three independently reviewable commits.
- Produces one exact candidate commit SHA and a read-only fleet-preflight result
  for that exact source.

- [ ] **Step 1: Run focused and package gates**

```bash
mise exec -- go test ./pkg/catch ./pkg/svc -run 'Test(TailscaleResolver|ReconcileTailscaleResolver|RecoverTailscaleResolver|StartTailscaleSidecar|RestartTailscaleSidecar|ApplyTSUpdate)' -count=1
mise exec -- go test ./pkg/catch ./pkg/svc ./cmd/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run race, full-repository, formatting, and release-quality gates**

```bash
mise exec -- go test -race ./pkg/catch ./pkg/svc -count=1
mise exec -- go test ./... -count=1
mise exec -- gofmt -d $(rg --files pkg/catch pkg/svc -g '*.go')
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: PASS, empty gofmt output, zero private-info findings, and all quality
goals met without lowering baselines.

- [ ] **Step 3: Perform exact-candidate review**

Record `git rev-parse HEAD`, verify it contains only the three planned commits
after the assigned base, and review that exact diff using
`superpowers:requesting-code-review`. Treat findings that identify broadened
unit grammar, weakened rollback, an omitted mutation path, or leaked private
data as release-blocking.

If review requires changes, add RED first, amend the owning unpublished task
commit with GitButler, rerun Steps 1-2, and review the rewritten exact SHA.

- [ ] **Step 4: Build the exact read-only preflight binary**

```bash
mise exec -- go test -c ./pkg/catch -o /tmp/catch-resolver-preflight.test
mise exec -- go version -m /tmp/catch-resolver-preflight.test
shasum -a 256 /tmp/catch-resolver-preflight.test
```

Expected: build metadata identifies the exact candidate and `vcs.modified`
is false. If unrelated worktree dirt makes it true, build from a clean
detached archive of the exact SHA without altering the GitButler workspace.

- [ ] **Step 5: Run read-only live fleet preflight**

Transfer the exact test binary through the already-approved operator channel,
set `YEET_LIVE_DATA_DIR` and `YEET_LIVE_SERVICES_ROOT` only in the remote
process environment, and run:

```bash
YEET_LIVE_DATA_DIR="$YEET_LIVE_DATA_DIR" \
YEET_LIVE_SERVICES_ROOT="$YEET_LIVE_SERVICES_ROOT" \
./catch-resolver-preflight.test \
  -test.run '^TestTailscaleResolverLiveReadOnlyPreflight$' -test.v
```

Expected: PASS with aggregate counts for historical/current layouts, zero
writes/stops/reloads/restarts, and no service names, paths, domains, or secrets
in retained evidence. Remove the uploaded binary after verifying its hash and
result.

### Task 5: Resume Full Matrices and Release Only After Green

**Files:**
- Modify after validation: `website/docs/changelog.mdx`
- Verify: exact candidate source, binaries, tag, workflows, and published artifacts

**Interfaces:**
- Consumes Task 4's exact reviewed SHA and successful read-only live preflight.
- Produces a patch release only after custom-candidate and published-artifact
  matrices both pass.

- [ ] **Step 1: Resume the existing custom-candidate matrices**

Use the existing resolver-isolation Task 8-10 runbook and a fresh disposable
identity. First migrate and audit every prior service, then cover multiple
disposable workloads, public DNS, Tailscale DNS, HTTP, HTTPS/certificate
issuance, early and 60-second samples, start/stop/restart, Catch restart,
Tailscale update, service identity, and service-root lifecycle.

Expected: every prior and disposable sidecar is active when expected, uses the
selected managed daemon, has LocalAPI readiness, has `acceptDNS=false`, and
sees the expected resolver inode through the highest read-only mount.

- [ ] **Step 2: Exercise transaction failure and recovery on disposable services**

Use only disposable workloads to inject plan-staleness, write, reload,
restart, verification, and interrupted-journal failures. Prove either complete
rollback to exact prior bytes/modes/state or a retained journal, global
mutation block, and stopped unproven replacements. Clear each injected state
through the implemented recovery path.

- [ ] **Step 3: Prepare the patch release**

Use the `yeet-release` skill. Inspect the actual previous-tag-to-candidate
commit range, add one concise user-facing changelog entry describing reliable
historical sidecar migration and full-fleet rollback, publish the website
commit, and verify the root gitlink points to that advertised commit.

Run all Task 4 gates again on the final release commit. Do not tag or push
until explicit release authorization is present in the executing task.

- [ ] **Step 4: Land, tag, publish, and watch every workflow**

Land the clean release commit on local and remote `main`, create the annotated
patch tag, push it, and watch Prepare Release and Release through completion.
Verify the remote main ref, tag ref, GitHub release, package artifact, checksums,
and clean workspace state independently.

- [ ] **Step 5: Repeat the full matrix with published artifacts**

Install only the published client/Catch artifacts and repeat the prior-service
audit, disposable DNS/HTTP/HTTPS lifecycle matrix, 60-second stability sample,
Catch restart, update path, and transaction recovery proof.

Expected: the published artifact matches the tag and passes the same matrix as
the exact custom candidate. Any discrepancy blocks completion and requires a
new corrective patch rather than altering the published tag.
