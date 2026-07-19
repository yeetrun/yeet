# Catch Firecracker Runtime Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let Catch securely discover, cache, adopt, stage, apply, roll back, report, and prune immutable Firecracker+jailer runtimes independently of guest image bundles.

**Architecture:** The Catch database stores a per-VM composition lock and runtime lifecycle state. A root-owned runtime descriptor is the durable launch input used by a stable systemd unit; it can describe a staged candidate and previous fallback without changing a running process. `vm-run` verifies exact digests immediately before jailer launch, writes running/trial markers, and falls back to the previous pair if a staged candidate fails before host readiness.

**Tech Stack:** Go, JSON-backed Catch database, systemd, Firecracker+jailer, ZFS, TTY command forwarding, GitHub-hosted runtime catalog/releases.

## Global Constraints

- Depends on completion of disk-only recovery and Tasks 1-5 of the runtime-publishing plan. Tasks 1-10 consume the exact opt-in candidate entry; publishing Tasks 6-7 then produce stable canary evidence, and this plan's Task 11 validates the promoted result.
- Keep `VMConfig.Runtime == "firecracker"` as the provider identity; do not overload it with a VMM artifact ID.
- The database is authoritative. Runtime descriptors, running/trial markers, systemd units, and transaction journals are derived and reconciliable.
- Cross-process database mutations use a stable lock file, reload the latest on-disk database while holding that lock, preserve unrelated changes, and compare the VM fields used by a prepared transaction before committing it.
- Once a descriptor-mode unit has been published, an older Catch generation may be restored only after every affected VM is verified back on its exact prior unit generation and the database is still at the prior state.
- The runtime cache path is `<catch-data-root>/vm-runtimes/<architecture>/<runtime-id>/<manifest-sha256>/`.
- Every runtime selection carries both component digests and every launch revalidates path trust, ownership, permissions, architecture, digests, and matching versions.
- A runtime upgrade never changes the guest disk unless the operator separately restores a disk recovery point.
- A normal Catch upgrade, catalog update, manual stage, or policy stage never restarts a running VM.
- `--restart` is the only runtime-upgrade flag that authorizes immediate downtime.
- The host default is `manual` and `stable`; a VM inherits those values unless it has an explicit override.
- Candidate channel selection is explicit operator opt-in.
- The current previous runtime is retained until a later runtime successfully replaces it; the first implementation has no wall-clock expiry for the active rollback selection.
- The guest-agent/vsock path is never consulted for runtime authorization, selection, artifact verification, or trial success.
- Host runtime observation requires `read`; update/import/upgrade/rollback/policy/protect/unprotect/prune require `manage`.
- Support default and custom Catch data roots, default/custom/ZFS service roots, raw disks, and ZFS zvol disks.
- Tasks 3-8 are one release gate: do not ship or activate production descriptor-mode units until Task 8's open-file artifact verification, readiness, and fallback gates pass.

---

### Task 1: Add composition and runtime lifecycle state to the database

**Files:**
- Modify: `pkg/db/db.go:14-45,150-190`
- Modify: `pkg/db/migrate.go:10-30`
- Modify: `pkg/db/db_test.go:700-1160`
- Modify generated: `pkg/db/db_clone.go`
- Modify generated: `pkg/db/db_view.go`
- Modify: `pkg/db/db_view_test.go`

**Interfaces:**
- Produces: `VMComponentsConfig`, component identity types, runtime lifecycle/trial state, host runtime defaults/protections.
- Consumed by: every later task in this plan and the independent-components plan.

- [x] **Step 1: Write migration and clone/view tests**

Add a version-11 fixture with a VM and verify migration to version 12 preserves `VM.Image`, initializes no false component identity, and defaults policy/channel through accessors rather than writing guessed artifacts. Add round-trip tests for every new field and prove clone/view callers cannot mutate stored maps or slices.

The expected test object is:

```go
components := &VMComponentsConfig{
	GuestBase: VMGuestBaseConfig{ID: "guest-ubuntu-26.04-amd64-v1", ManifestSHA256: strings.Repeat("a", 64), Source: "official"},
	Kernel: VMKernelArtifactConfig{ID: "kernel-linux-7.1.1-yeet-v1", ManifestSHA256: strings.Repeat("b", 64), SHA256: strings.Repeat("c", 64), Path: "/var/lib/yeet/vm-kernels/vmlinux", Source: "official"},
	Runtime: VMRuntimeLifecycleConfig{
		Policy: "manual", Channel: "stable",
		Configured: VMRuntimeArtifactConfig{ID: "firecracker-v1.16.1-yeet-v1", ManifestSHA256: strings.Repeat("d", 64), FirecrackerSHA256: strings.Repeat("e", 64), JailerSHA256: strings.Repeat("f", 64), Firecracker: "/var/lib/yeet/vm-runtimes/amd64/fc/firecracker", Jailer: "/var/lib/yeet/vm-runtimes/amd64/fc/jailer", Source: "official"},
	},
}
```

- [x] **Step 2: Run DB tests and verify failure**

```bash
mise exec -- go test ./pkg/db -run 'TestStoreGetMigratesVersion11VMComponents|TestVMComponentsClone|TestVMComponentsView' -count=1
```

Expected: FAIL because the new types and migrator do not exist.

- [x] **Step 3: Add exact persisted types**

Add these types to `pkg/db/db.go` and add `Components *VMComponentsConfig` to `VMConfig`:

```go
type VMGuestBaseConfig struct {
	ID               string
	ManifestSHA256   string
	Source           string
	RootFSProvenance string `json:",omitempty"`
}

type VMKernelArtifactConfig struct {
	ID             string
	ManifestSHA256 string
	SHA256         string
	Path           string
	Source         string
}

type VMRuntimeArtifactConfig struct {
	ID                string
	ManifestSHA256    string
	FirecrackerSHA256 string
	JailerSHA256      string
	Firecracker       string
	Jailer            string
	Source            string
}

type VMRuntimeTrialConfig struct {
	State         string
	CandidateID   string
	PreviousID    string
	RecoveryPoint string `json:",omitempty"`
	StartedAt     string
	LastError     string `json:",omitempty"`
}

type VMRuntimeLifecycleConfig struct {
	Policy     string
	Channel    string
	Configured VMRuntimeArtifactConfig
	Staged     *VMRuntimeArtifactConfig `json:",omitempty"`
	Previous   *VMRuntimeArtifactConfig `json:",omitempty"`
	Trial      *VMRuntimeTrialConfig    `json:",omitempty"`
}

type VMComponentsConfig struct {
	GuestBase VMGuestBaseConfig
	Kernel    VMKernelArtifactConfig
	Runtime   VMRuntimeLifecycleConfig
}
```

Extend `VMHostConfig`:

```go
type VMHostConfig struct {
	MemoryPolicy       string
	RuntimePolicy      string   `json:",omitempty"`
	RuntimeChannel     string   `json:",omitempty"`
	ProtectedRuntimeIDs []string `json:",omitempty"`
}
```

- [x] **Step 4: Add migration 11 -> 12 and regenerate accessors**

The migrator only initializes an absent `VMHost` container when needed; it must not hash files or invent component IDs:

```go
func addVMComponentLifecycle(*Data) error { return nil }
```

Add `11: addVMComponentLifecycle`, set `CurrentDataVersion = 12`, include all new types in both `go:generate` type lists, then run:

```bash
cd pkg/db
mise exec -- go generate ./...
cd ../..
```

- [x] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/db -count=1
mise exec -- go test ./pkg/db ./pkg/catch ./pkg/dnet -count=1
but commit catch-firecracker-runtime-management -m "db: store VM component lifecycle state"
```

### Task 2: Add strict runtime catalog, manifest, and cache handling

**Files:**
- Create: `pkg/catch/vm_runtime_manifest.go`
- Create: `pkg/catch/vm_runtime_manifest_test.go`
- Create: `pkg/catch/vm_runtime_catalog.go`
- Create: `pkg/catch/vm_runtime_catalog_test.go`
- Create: `pkg/catch/vm_runtime_cache.go`
- Create: `pkg/catch/vm_runtime_cache_test.go`
- Modify: `pkg/catch/vm_image_catalog.go:169-203`
- Modify generated: `cmd/catch/depaware.txt`

**Interfaces:**
- Produces: `vmRuntimeManifest`, `vmRuntimeCatalog`, `vmRuntimeCatalogRef`, and `vmRuntimeCache.Ensure`.
- Consumes: `runtime-catalog.json` and immutable releases from the publishing plan.

- [x] **Step 1: Write parser, trust, and cache adversary tests**

Use `httptest.Server` only for tests that pass `requireTrustedURL=false`. Cover unknown JSON fields, unsupported schema, non-HTTPS URL, wrong repository/path, manifest digest mismatch, path traversal, symlink cache entries, group-writable parent, wrong architecture, component digest mismatch, mismatched version probes, interrupted downloads, and concurrent ensure calls.

The successful interface assertion is:

```go
artifact, err := cache.Ensure(ctx, vmRuntimeCatalogRef{
	RuntimeID: "firecracker-v1.16.1-yeet-v1",
	ManifestURL: server.URL + "/runtime-manifest.json",
	ManifestSHA: strings.Repeat("a", 64),
})
if err != nil {
	t.Fatal(err)
}
if artifact.ID == "" || artifact.Firecracker == "" || artifact.Jailer == "" {
	t.Fatalf("artifact = %#v", artifact)
}
```

- [x] **Step 2: Run focused tests and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeManifest|TestVMRuntimeCatalog|TestVMRuntimeCache' -count=1
```

Expected: FAIL because the runtime types/cache do not exist.

- [x] **Step 3: Implement manifest and catalog contracts**

Use these stable Go types:

```go
type vmRuntimeCatalogRef struct {
	RuntimeID             string `json:"runtime_id"`
	ManifestURL           string `json:"manifest_url"`
	ManifestSHA           string `json:"manifest_sha256"`
	UpstreamVersion       string `json:"upstream_version"`
	Support               string `json:"support"`
	IntegrationURL        string `json:"integration_attestation_url"`
	IntegrationSHA        string `json:"integration_attestation_sha256"`
	CanaryURL             string `json:"canary_attestation_url,omitempty"`
	CanarySHA             string `json:"canary_attestation_sha256,omitempty"`
}

type vmRuntimeCatalogIdentity struct {
	RuntimeID   string `json:"runtime_id"`
	ManifestSHA string `json:"manifest_sha256"`
}

type vmRuntimeCatalogArchitecture struct {
	Runtimes []vmRuntimeCatalogRef                `json:"runtimes"`
	Channels map[string]*vmRuntimeCatalogIdentity `json:"channels"`
}

type vmRuntimeRevocation struct {
	RuntimeID   string `json:"runtime_id"`
	ManifestSHA string `json:"manifest_sha256"`
	Reason      string `json:"reason"`
	RecordedAt  string `json:"recorded_at"`
}

type vmRuntimeCatalog struct {
	SchemaVersion int                                     `json:"schema_version"`
	Architectures map[string]vmRuntimeCatalogArchitecture `json:"architectures"`
	Revocations   []vmRuntimeRevocation                   `json:"revocations"`
}

type vmRuntimeManifest struct {
	SchemaVersion  int                           `json:"schema_version"`
	RuntimeID      string                        `json:"runtime_id"`
	Architecture   string                        `json:"architecture"`
	Upstream       vmRuntimeManifestUpstream     `json:"upstream"`
	Components     vmRuntimeManifestComponents   `json:"components"`
	Classification vmRuntimeManifestClass        `json:"classification"`
	Support        vmRuntimeManifestSupport      `json:"support"`
	Provenance     vmRuntimeManifestProvenance   `json:"provenance"`
}
```

Decode with `json.Decoder.DisallowUnknownFields`, require schema 1, normalize only `x86_64 -> amd64`, and validate every required field/digest. Each channel must resolve to exactly one entry in the same architecture by both runtime ID and manifest digest. Exact `--to` resolution searches `Runtimes`; channel pointers are used only for promoted defaults.

- [x] **Step 4: Implement immutable cache publication**

Define:

```go
type vmRuntimeCache struct {
	Root       string
	CatalogURL string
	Client     *http.Client
}

func (c vmRuntimeCache) Ensure(ctx context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error)
```

Download the manifest first, hash it, create a private staging directory under the final parent, download only declared `firecracker` and `jailer`, verify content/digests/version/architecture, chmod 0755, fsync files and directory, then `os.Rename` to the immutable final path. Existing final paths must be revalidated rather than trusted. Refactor the image URL trust helper into `validateTrustedYeetVMArtifactURL` while preserving a compatibility wrapper for image callers.

- [x] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeManifest|TestVMRuntimeCatalog|TestVMRuntimeCache|TestValidateTrusted' -count=1
but commit catch-firecracker-runtime-management -m "catch: add trusted VM runtime cache"
```

### Task 3: Generalize unit publication and add runtime descriptors

**Files:**
- Create: `pkg/catch/vm_unit_transaction.go`
- Create: `pkg/catch/vm_unit_transaction_test.go`
- Create: `pkg/catch/vm_runtime_descriptor.go`
- Create: `pkg/catch/vm_runtime_descriptor_test.go`
- Modify: `pkg/catch/vm_jailer_upgrade.go:49-680`
- Modify: `pkg/catch/vm_jailer_upgrade_test.go:269-925`
- Modify: `pkg/catch/vm_systemd.go:18-85,111-150`
- Modify: `pkg/catch/vm_systemd_test.go`
- Modify: `pkg/catch/vm_console_proxy.go`
- Modify: `cmd/catch/catch.go:345-385`
- Modify: `cmd/catch/catch_test.go:470-620`

**Interfaces:**
- Produces: reusable `vmUnitTransaction`, root-owned `vmRuntimeDescriptor`, descriptor-based `vm-run` flags.
- Consumes: runtime artifact state from Tasks 1-2.

- [x] **Step 1: Pin existing transaction behavior with generic tests**

Move the existing rename/exchange/rollback/daemon-reload tests to the new file without changing behavior. Require this interface:

```go
type vmUnitSpec struct {
	Service string
	Path    string
	Content []byte
}

func prepareVMUnitTransaction(ctx context.Context, specs []vmUnitSpec, deps vmUnitTransactionDeps) (*vmUnitTransaction, error)
func (tx *vmUnitTransaction) Commit() error
func (tx *vmUnitTransaction) Close() error
```

- [x] **Step 2: Run tests and verify failure before extraction**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMUnitTransaction|TestVMJailerUpgradeCommit' -count=1
```

Expected: generic tests FAIL; existing jailer tests still PASS.

- [x] **Step 3: Extract the generic unit transaction**

Move only filesystem transaction responsibilities out of `vm_jailer_upgrade.go`: trusted directory opens, locking, staged file creation, identity revalidation, publish, rollback, cleanup, and one daemon-reload. `VMJailerUpgrade` becomes a compatibility wrapper around `*vmUnitTransaction` plus its current summary. Preserve every TOCTOU and rollback test.

- [x] **Step 4: Add a durable runtime descriptor and stable unit**

Use this descriptor contract:

```go
type vmRuntimeDescriptor struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Service       string                      `json:"service"`
	Configured    db.VMRuntimeArtifactConfig  `json:"configured"`
	Staged        *db.VMRuntimeArtifactConfig `json:"staged,omitempty"`
	Previous      *db.VMRuntimeArtifactConfig `json:"previous,omitempty"`
	Trial         bool                        `json:"trial"`
}
```

Write it root-owned mode 0600 with create-temp, fsync, chmod/chown, identity revalidation, rename, and parent fsync. Store it at `filepath.Join(serviceDataDirForRoot(serviceRoot), "vmm-runtime.json")`.

The first successful no-replace rename or exchange is the irreversible publication boundary. A later validation or parent-sync failure leaves the canonical name untouched, preserves any displaced descriptor under its unique recovery name, and returns a typed retained-publication error carrying the canonical and recovery paths. Callers must reread and reconcile that state; they must never parse error text or perform a second rollback rename against the canonical name.

Change the unit to pass `--runtime-descriptor`, `--runtime-running-marker`, and `--runtime-trial-result` rather than Firecracker/jailer paths:

```text
vm-run --service NAME --service-root ROOT --disk-path DISK \
  --runtime-descriptor DATA/vmm-runtime.json \
  --runtime-running-marker RUN/vmm-runtime-running.json \
  --runtime-trial-result RUN/vmm-runtime-trial-result.json \
  --jailer-base JAILER_BASE --api-sock API --config-file CONFIG --console-sock CONSOLE
```

Task 3 adds descriptor launch as an explicit second mode; it does not globally
cut over existing units yet. Descriptor mode requires all three descriptor,
running-marker, and trial-result paths and forbids explicit Firecracker/jailer
paths. The compatibility mode continues to require the exact Firecracker and
jailer pair and forbids descriptor paths. Reject every mixed or partial mode.
Keep current provisioning and the `VMJailerUpgrade` compatibility wrapper on
the explicit-pair renderer during Task 3. Task 4 is the first production
publisher of descriptor units for adopted existing VMs; the independent
components plan owns atomic descriptor/unit publication for newly composed VMs.

`cmd/catch.handleVMRunCommand` reads and validates the descriptor before constructing `VMConsoleProxyConfig` in descriptor mode, while preserving current explicit-pair behavior in compatibility mode.

- [x] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestVMUnitTransaction|TestVMJailerUpgrade|TestVMRuntimeDescriptor|TestRenderVMSystemdUnit|TestHandleSpecialCommandVMRun' -count=1
but commit catch-firecracker-runtime-management -m "catch: launch VMs from runtime descriptors"
```

### Task 4: Adopt existing monolithic VMs without restart

**Files:**
- Modify: `pkg/db/db.go`
- Modify: `pkg/db/db_test.go`
- Create: `pkg/catch/vm_runtime_adoption.go`
- Create: `pkg/catch/vm_runtime_adoption_test.go`
- Create: `pkg/catch/vm_runtime_adoption_provenance.go`
- Create: `pkg/catch/vm_runtime_adoption_provenance_test.go`
- Create: `pkg/catch/vm_runtime_journal.go`
- Create: `pkg/catch/vm_runtime_journal_test.go`
- Modify: `pkg/catch/vm_runtime_descriptor.go`
- Modify: `pkg/catch/vm_runtime_descriptor_test.go`
- Modify: `pkg/catch/vm_unit_transaction.go`
- Modify: `pkg/catch/vm_unit_transaction_test.go`
- Modify: `pkg/catch/catch.go:200-225`
- Modify: `pkg/catch/catch_test.go`
- Modify: `pkg/catch/vm_jailer_upgrade.go:680-1280`
- Modify: `cmd/catch/catch.go:1100-1265`
- Modify: `cmd/catch/catch_test.go:1400-1640`

**Interfaces:**
- Produces: `PrepareVMRuntimeAdoption(context.Context, *Config) (*VMRuntimeAdoption, error)` with `Commit`, `Close`, `Summary`, and conservative Catch-rollback-safety reporting.
- Consumes: existing jailer-only inventory, DB component types, descriptor/unit transaction.

- [x] **Step 1: Write adoption transaction tests**

Cover official manifests, legacy bundles without manifests, custom imported bundles, default/custom service roots, default/custom data roots, ZFS service roots, a running VM, a stopped VM, DB save failure, descriptor publish failure, unit publish failure, daemon-reload failure, interruption after every journal state, a new Catch startup racing the still-active installer, two stores mutating the database concurrently, relevant VM state changing after preparation, and no restart/systemctl start/stop calls.

Prove an ordinary commit error permits Catch rollback only after the database and every affected unit have been verified at their exact prior states. Inject failure after descriptor publication, after unit publication, after DB commit, and during compensating unit restoration. After DB commit, or when exact prior-state restoration is incomplete, require the new Catch generation to remain installed and retain the journal for recovery.

Assert a legacy runtime identity is deterministic:

```go
wantID := "legacy-firecracker-" + firecrackerVersion + "-" + firecrackerSHA + "-jailer-" + jailerSHA
```

- [x] **Step 2: Run and verify failure**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestPrepareVMRuntimeAdoption|TestVMRuntimeAdoption|TestCatchInstall.*RuntimeAdoption' -count=1
```

Expected: FAIL because adoption and its journal do not exist.

- [x] **Step 3: Implement logical adoption**

For each existing VM:

1. resolve effective roots, active disk, stored immutable rootfs, loaded unit/drop-ins, Firecracker config, Firecracker, and matching jailer;
2. validate root ownership, modes, regular files, architecture, matching versions, and component SHA-256;
3. hash exact installed manifest/receipt/local-ref bytes when valid, otherwise create a canonical measured provenance record;
4. populate `VM.Components.GuestBase` from the immutable provisioning base, `VM.Components.Kernel` from the kernel selected by `firecracker.json`, and configured runtime from the exact effective unit pair;
5. write the descriptor and stable unit without stopping or restarting the VM.

Do not copy the runtime into the new cache during logical adoption. Its `Source` is `official-legacy`, `custom-legacy`, or `local-legacy`, and paths continue to reference the immutable monolithic bundle.

Adopt the full composition now rather than persisting a partial `Components` object. Hash exact installed manifest bytes; never re-marshal them for identity. A component may use that manifest digest only when its currently configured paths and measured checksums exactly match the manifest. Source is per-component: a service-local synced kernel is `custom-legacy` even when guest base and runtime retain stronger provenance. The launch-time kernel source of truth is `firecracker.json`, not a stale `VM.Image.Kernel`; accept an automatic mismatch only for the exact trusted service-local kernel-sync path and reconcile the compatibility field in the atomic DB mutation.

Classify without network access. `official-legacy` requires a root-owned receipt binding catalog URL+digest, payload, exact version, and manifest URL+digest; future image downloads create this receipt atomically, while an older bundle without it falls to `custom-legacy`. `local-legacy` requires an exact valid local ref, blob `ContentID`, version, root, manifest, and recomputed content identity. A contradictory receipt/ref blocks adoption rather than downgrading. Every other fully measured bundle is `custom-legacy`.

For a genuinely manifest-less custom bundle, require `VM.Image.RootFS` to be the separate immutable prepared rootfs used for provisioning, distinct from the active disk by path and device/inode. A published manifest's compressed rootfs digest is not evidence for this decompressed prepared rootfs. Hash the prepared rootfs, configured kernel/config/initrd, Firecracker, and jailer into a fixed-struct schema-1 `yeet.vm.legacy-composition` JSON record (all fields present, normalized `amd64`, lowercase digests, compact `json.Marshal`, no trailing newline). Persist the exact record before use at `<catch-data-root>/vm-component-provenance/sha256/<composition-digest>.json`, and use its digest only as the synthetic composition/manifest binding. Component IDs remain independently keyed by their own full SHA-256 values. Never hash the mutable raw disk or ZVOL. If no separate immutable guest base exists, or a present manifest is invalid, leave `Components` nil and the exact explicit-pair unit active with `adoption-blocked` status.

Use deterministic IDs:

```text
legacy-guest-<normalized-name>-<normalized-version>-<rootfs-sha256>
legacy-kernel-<normalized-kernel-version>-<kernel-sha256>
legacy-firecracker-<parsed-version>-<firecracker-sha256>-jailer-<jailer-sha256>
```

Normalize general ID segments by trimming/lowercasing, replacing maximal non-`[a-z0-9]` runs with `-`, and trimming `-`. Use the persisted absolute service mountpoint for custom/ZFS roots during inventory; do not call a resolver that can create a missing ZFS dataset.

- [x] **Step 4: Make descriptor, unit, and DB publication recoverable**

Journal directory:

```text
<catch-data-root>/vm-runtime-transactions/
  transaction-<transaction-id>.json
  <service>.json
```

Use one durable marker for the entire sorted fleet cohort, plus one self-contained member record per service. The marker binds exact old/new member digests, deterministic bound stage names, and deterministic tombstone names. Journal states are `prepared`, `derived-published`, and `database-committed`; durable marker operations cover prepare, transition, and removal. Store old/new component state plus old/new descriptor and unit digests, not executable bytes.

The journal must also retain a schema/transaction ID, old-service precondition digest, timestamps, and the exact small derived configuration payloads needed for recovery: old/new descriptor and unit contents, existence flags, mode, owner, and digest. Historical explicit units are not guaranteed to be reproducible by the current renderer, and an unadopted VM has no old Components or descriptor. Never copy Firecracker, jailer, kernel, or guest-disk bytes into the journal.

The versioned old-service precondition covers the runtime-relevant stored Service/VM projection (`ServiceRoot`, VM runtime/setup/image/disk/component selection, and compatibility kernel field), data/services/service roots, runner/jailer-base and derived paths, loaded unit/drop-ins/reload/active/PID evidence, descriptor/unit/config/ref/receipt/manifest digests, and ordered file evidence (path, existence, device/inode, size, mode, UID/GID, mtime-ns, SHA-256) for immutable rootfs, kernel/config/initrd, Firecracker, and jailer. Record active-disk path/backend/size/filesystem identity but no content digest. Exclude unrelated controller-owned telemetry such as balloon target observations so the newly started Catch cannot invalidate its own adoption; preserve those fields from the latest database view. Use two validations: the complete old precondition immediately before publishing derived files, then a commit-time validator that requires the exact old DB projection, unchanged non-derived artifact/source/drop-in/process evidence, and the exact new descriptor plus effective descriptor-mode unit. Any relevant drift blocks adoption.

Recovery rules:

```text
DB old + prepared/derived-published -> restore old descriptor/unit generation
DB new + any journal              -> reconcile new descriptor/unit generation
DB mixed/neither old nor new      -> fail closed and retain journal
```

Write and fsync the marker before publishing member records, and fsync every record before publishing derived state. Publish bound stages with atomic no-replace rename and directory sync. Remove a cohort only through explicit `BeginRemoval`, a fresh DB-and-derived agreement check, then `CommitRemoval`; an ordinary retry must never silently cross this boundary.

Hold a root-owned runtime-transaction lock from preparation through `Close`. Startup recovery runs asynchronously so it cannot deadlock the systemd restart that starts the new Catch generation, waits behind that lock, and completes before later runtime reconciliation. Publish the descriptor before its unit. On DB-old recovery, first restore and reload the exact prior explicit unit. Then restore an old descriptor if one existed. If none existed, remove only the exact descriptor identity created by this transaction; if identity-safe removal or exact verification is impossible, report Catch rollback unsafe and retain recovery state. On DB-new recovery, restore the new descriptor before the new unit.

Extend the unit transaction so a successful unit publication can still restore and verify its exact prior bytes, mode, UID, and GID while its directory locks remain held. Fsync each unit directory after publication and restoration. Add a context-aware raw descriptor transaction helper that can publish, restore, classify, and identity-safely remove the exact journal bytes; decoded re-rendering is not sufficient for recovery.

Make `db.Store.MutateData` serialize writers through a stable lock file and reload the latest on-disk state under that lock before applying the callback. Fsync the replacement database and its parent directory before publishing the refreshed in-memory view. Adoption performs one fleet-wide compare-and-mutate against the exact VM inputs inventoried during preparation so unrelated DB changes are preserved, relevant drift fails closed, and the fleet cannot become partially adopted. Final removal uses the database's locked view from a journal-owned finalizer: hold the journal mutex, enter `WithLatestDataLocked`, verify DB-new plus exact new descriptor and unit state, and unlink/fsync the cohort marker inside that callback before releasing the database lock. Cleanup of already-unlinked member tombstones is retryable post-finalization. Writers of `VM.Components`, `VM.Image.Kernel`, descriptors, and adopted units still join the root runtime-transaction lock discipline so stale whole-record updates cannot regress an adopted VM after finalization.

Before DB commit, any failure must either restore and verify every exact old unit plus DB-old state or report that Catch rollback is unsafe. After DB commit, journal-state or cleanup failure retains the new Catch generation and journal and is surfaced as a recoverable warning; it must not trigger rollback to a Catch binary that cannot interpret descriptor-mode units.

Implementation checkpoint: cross-process database serialization is complete in `bd369fa`; the durable cohort journal is complete in `0151d06` with its canonical descriptor-path correction in `3530162`; fixed-schema provenance, independent full-SHA component identities, immutable-file evidence, and raw/ZVOL metadata-only evidence are complete in `6fd1311`; strict offline inventory of effective unit/config/component state is complete in `713e8a2`; the locked latest-data finalizer view is complete in `305d79e`; exact recoverable descriptor/unit transactions are complete in `0953476`; and the fleet adoption coordinator, journal-driven unit reconciliation, locked DB finalization, startup recovery primitive, and conservative rollback-safety reporting are complete in `b076dc2`. Production install/startup adoption and shared install locking are complete in `94c64ee`; path-migration writers join the transaction boundary in `fb8350c`; and kernel sync, recovery clone, removal, and compatibility jailer-upgrade paths are guarded in `9c80867`. Each accepted slice passed independent review, focused race coverage, full package tests, and vet. A VM already running during no-restart adoption can still execute the old reboot hook once; Task 7's pre-start reconciliation boundary remains a release blocker before descriptor-mode units can ship.

- [x] **Step 5: Replace install-time jailer-only preparation with adoption and commit**

Keep the public install summary fields for jailer readiness, but back them with `VMRuntimeAdoption`. Replace the unconditional Catch-generation rollback on adoption error with the transaction's conservative rollback-safety result. Add startup journal recovery as the first VM runtime reconciliation step. Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestVMRuntimeAdoption|TestCatchInstall|TestVMJailerUpgrade' -count=1
but commit catch-firecracker-runtime-management -m "catch: adopt legacy VM runtimes without restart"
```

### Task 5: Add runtime CLI parsing, routing, and authorization

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Create: `pkg/yeet/vm_runtime_cmd.go`
- Create: `pkg/yeet/vm_runtime_cmd_test.go`
- Modify: `cmd/yeet/cli_bridge.go:16-235`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `pkg/catch/tty_authz.go:100-170`
- Modify: `pkg/catch/tty_authz_test.go`
- Modify: `pkg/catch/tty_vm.go:34-100`
- Modify: `pkg/catch/tty_exec.go:280-350`

**Interfaces:**
- Produces: `ParseVMRuntime`, client routing, Catch dispatch, and permission classification.
- Consumes: no runtime behavior yet; commands can return a clear not-implemented error until Task 6.

**Completed:** commit `2c3dbae` adds the strict shared grammar, flags-before-action-aware routing and authorization, system-service forcing for host actions, per-VM service reconstruction, PTY-safe deterministic import streaming, and delimiter regression coverage. Focused tests, touched-package tests, vet, focused race tests, the full repository suite, and independent review passed. The repository-wide pre-commit license hook passed; its gofmt hook retained the known false failure on any dirty Go worktree after `gofmt` and `git diff --check` were clean.

- [x] **Step 1: Write parser and permission tables**

Cover this exact command surface:

```text
yeet vm runtime status [<vm>] [--format=table|json|json-pretty]
yeet vm runtime update
yeet vm runtime import <name> <dir>
yeet vm runtime upgrade <vm> [--to VERSION] [--channel=stable|candidate] [--restart]
yeet vm runtime rollback <vm> [--restart]
yeet vm runtime policy defaults show
yeet vm runtime policy defaults set manual|stage-on-restart [--channel=stable|candidate]
yeet vm runtime policy <vm> inherit|manual|stage-on-restart [--channel=stable|candidate]
yeet vm runtime protect <runtime-id>
yeet vm runtime unprotect <runtime-id>
yeet vm runtime prune [--dry-run]
```

Reject empty values, unknown channels/policies, `--restart` on status/update/import/policy/prune, and missing VM/name/directory arguments.

- [x] **Step 2: Run parser/bridge/authz tests and verify failure**

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -run 'VMRuntime|RuntimeCommandPermissions|BridgeVMRuntime' -count=1
```

Expected: FAIL because the group is unregistered.

- [x] **Step 3: Add shared parser types and metadata**

Use one parser result:

```go
type VMRuntimeFlags struct {
	Format  string
	To      string
	Channel string
	Policy  string
	Restart bool
	DryRun  bool
}

func ParseVMRuntime(args []string) (VMRuntimeFlags, []string, error)
```

Register help/usage/examples in `RemoteCommandRegistry`. Keep sub-action strings in constants rather than duplicating literals.

- [x] **Step 4: Route service and host actions correctly**

`status <vm>`, `upgrade`, `rollback`, and per-VM `policy` use the service bridge and remove `<vm>` into `e.sn`. Host-wide `status`, `update`, `import`, policy defaults, protect/unprotect, and prune route through the system service. The local import handler validates `<dir>`, creates a deterministic tar stream containing only `runtime-manifest.json`, `firecracker`, and `jailer`, and forwards it without PTY input.

Permission mapping:

```go
case "status":
	return newPermissionSet(permissionRead), nil
case "update", "import", "upgrade", "rollback", "policy", "protect", "unprotect", "prune":
	return newPermissionSet(permissionManage), nil
```

- [x] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -run 'VMRuntime|RuntimeCommandPermissions|BridgeVMRuntime' -count=1
but commit catch-firecracker-runtime-management -m "cli: route VM runtime lifecycle commands"
```

### Task 6: Implement status, catalog update, import, policy, and protection

**Files:**
- Create: `pkg/catch/vm_runtime_cmd.go`
- Create: `pkg/catch/vm_runtime_cmd_test.go`
- Create: `pkg/catch/vm_runtime_status.go`
- Create: `pkg/catch/vm_runtime_status_test.go`
- Create: `pkg/catch/vm_runtime_import.go`
- Create: `pkg/catch/vm_runtime_import_test.go`
- Create: `pkg/catch/vm_runtime_reconcile.go`
- Create: `pkg/catch/vm_runtime_reconcile_test.go`
- Modify: `pkg/catch/vm_runtime_descriptor.go`
- Modify: `pkg/catch/catch.go:213-220`

**Interfaces:**
- Produces: read/status renderer, catalog refresh/cache ensure, local import, defaults/per-VM policy updates, protection state.
- Consumes: parser/routing from Task 5 and state/cache from Tasks 1-4.

- [x] **Step 1: Write status and mutation tests**

Cover adopted current/EOL runtime, stable update available, candidate opt-in, exact digest output, running/configured divergence, staged/previous/trial/failure, jailer mismatch, revoked runtime, missing marker, stale marker PID, import permission/path/symlink/digest failures, host defaults, per-VM inherit, and protection idempotence.

JSON status must expose:

```go
type vmRuntimeStatusRow struct {
	Service           string                   `json:"service"`
	Running           *vmRuntimeStatusIdentity `json:"running,omitempty"`
	Configured        vmRuntimeStatusIdentity  `json:"configured"`
	Staged            *vmRuntimeStatusIdentity `json:"staged,omitempty"`
	Previous          *vmRuntimeStatusIdentity `json:"previous,omitempty"`
	Policy            string                   `json:"policy"`
	Channel           string                   `json:"channel"`
	LatestPromoted    *vmRuntimeStatusIdentity `json:"latestPromoted,omitempty"`
	JailerIsolation   string                   `json:"jailerIsolation"`
	State             string                   `json:"state"`
	LastTransition    string                   `json:"lastTransition,omitempty"`
	RecommendedAction string                   `json:"recommendedAction,omitempty"`
}
```

- [x] **Step 2: Run and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeStatus|TestVMRuntimeUpdate|TestVMRuntimeImport|TestVMRuntimePolicy|TestVMRuntimeProtection' -count=1
```

Expected: FAIL because handlers do not exist.

- [x] **Step 3: Implement status without guest trust**

Read the running marker only when it is root-owned, mode 0600, the recorded runner PID is alive, and systemd reports the VM active. Compare exact IDs/digests against DB state. Derive support/EOL/revocation from the verified catalog. Report jailer readiness separately from currency.

Do not call `queryVMGuestReady`, `queryVMNetworkState`, or any vsock function from status or selection code.

- [x] **Step 4: Implement update/import/policy/protection**

`runtime update` fetches and validates one catalog snapshot and ensures the promoted stable runtime cache entry plus any already configured/staged/previous runtime entries. It does not invoke policy reconciliation, select a runtime for a VM, change `VM.Image`, rewrite a descriptor/unit, or restart a service.

Import assigns source `local:<name>`, requires the same strict manifest/pair/digest/ownership checks, publishes under the runtime cache, and refuses collisions whose digest differs. Policy defaults live in `Data.VMHost`; VM override `inherit` stores empty policy/channel. Protection stores unique sorted runtime IDs in `VMHost.ProtectedRuntimeIDs`.

Task 6 persists and validates `stage-on-restart`, but does not apply it yet. Task 9 owns the first call to policy reconciliation because it depends on Task 7's atomic staging transaction. Task 9 must download and verify an uncached selection as part of the explicit policy operation and must leave the prior policy and VM selections intact on failure. The intermediate stack through Task 8 is not releasable with `stage-on-restart` exposed as complete behavior.

- [x] **Step 5: Reconcile status at server startup and commit**

Add `reconcileVMRuntimeState` to `Server.reconcileRuntimeState`; it repairs descriptors/units from DB, but performs no network download and no restart. Task 8 adds the strict generation-bound trial-result schema, producer, and DB-changing consumer together; Task 6 must not consume an underspecified marker.

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeStatus|TestVMRuntimeUpdate|TestVMRuntimeImport|TestVMRuntimePolicy|TestVMRuntimeProtection|TestReconcileVMRuntimeState' -count=1
but commit catch-firecracker-runtime-management -m "catch: report and configure VM runtimes"
```

### Task 7: Implement atomic staging and stopped-VM selection

**Files:**
- Create: `pkg/catch/vm_runtime_transaction.go`
- Create: `pkg/catch/vm_runtime_transaction_test.go`
- Modify: `pkg/catch/vm_runtime_journal.go`
- Modify: `pkg/catch/vm_runtime_cmd.go`
- Modify: `pkg/catch/vm_systemd.go`

**Interfaces:**
- Produces: `stageVMRuntime(context.Context, string, vmRuntimeCatalogRef) (vmRuntimeTransitionResult, error)`.
- Consumes: cache, DB, journal, descriptor/unit transaction.

- [x] **Step 1: Write transition-boundary tests**

For running and stopped VMs, inject failure before/after cache ensure, lock, journal write, descriptor publish, unit publish, daemon reload, DB save, and journal removal. Assert restart/start/stop are never called without `--restart`. Assert source inodes and digests are rechecked immediately before descriptor publication.

- [x] **Step 2: Run and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestStageVMRuntime|TestRecoverVMRuntimeTransaction' -count=1
```

Expected: FAIL because the transaction is absent.

- [x] **Step 3: Implement the transition state machine**

Use one per-VM mutation lock and these state changes:

```go
type vmRuntimeTransitionResult struct {
	Service          string
	RunningUnchanged bool
	Configured       db.VMRuntimeArtifactConfig
	Staged           *db.VMRuntimeArtifactConfig
	Previous         *db.VMRuntimeArtifactConfig
}
```

For both running and stopped VMs, leave `Configured` and `Previous` unchanged, set only the candidate as `Staged`, and write a trial descriptor that selects staged first and configured on early failure. This preserves the pre-existing rollback target until the candidate proves healthy. The stopped VM tries the staged candidate on its next operator-authorized start; staging does not start it. Persist DB state only after derived state is safely published; recover according to Task 4 journal rules.

- [x] **Step 4: Implement exact selection rules**

`--to VERSION` resolves only an exact runtime ID or exact upstream version in the selected trusted catalog channel. With neither `--to` nor `--channel`, use effective policy channel. Imported/custom VMs do not silently switch to an official runtime; require an exact `--to local:<name>` or an explicit official runtime ID.

- [x] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/catch -run 'TestStageVMRuntime|TestRecoverVMRuntimeTransaction|TestResolveVMRuntimeUpgradeTarget' -count=1
but commit catch-firecracker-runtime-management -m "catch: stage VM runtime transitions atomically"
```

Implemented in `f6704da`. The transition reuses the Task 4 journal and
descriptor/unit compensators, holds the root journal flock, recovers older
transactions before selection, rechecks immutable runtime evidence immediately
before descriptor publication, and changes only `Runtime.Staged`. Active raw
disk timestamps are intentionally excluded from transition drift checks while
path, backend, size, inode/device, type, and ownership remain bound. Exact
idempotent staging validates the descriptor and canonical unit instead of
trusting DB state alone. The old explicit-mode reboot hook now consults the
host DB under the same root lock and refuses kernel/config mutation after
adoption, closing the already-running pre-adoption process race.

Selection distinguishes an explicit `--channel` from a policy-derived channel,
so deliberate candidate staging does not require changing the VM policy.
Local aliases are re-resolved from the durable host alias while the transaction
lock is held. `--restart` remains fenced until Task 8 owns trial launch and
fallback.

Verification: focused and focused `-race` tests, package vet, the full test
suite, both command builds, and `git diff --check` passed. Independent review
found and then cleared explicit channel binding, mutable active-disk timestamps,
and idempotent derived-state validation. Pre-commit's license check passed; the
repository's dirty-tree
gofmt hook reported the expected existing-diff failure, so the already-gofmt'd
and manually verified commit used `--no-hooks`.

### Task 8: Add trial launch, readiness, automatic fallback, and explicit restart

**Files:**
- Create: `pkg/catch/vm_runtime_launch.go`
- Create: `pkg/catch/vm_runtime_launch_test.go`
- Create: `pkg/catch/vm_runtime_trial.go`
- Create: `pkg/catch/vm_runtime_trial_test.go`
- Modify: `pkg/catch/vm_console_proxy.go`
- Modify: `pkg/catch/vm_console_test.go`
- Modify: `pkg/catch/vm_jailer.go:266-380`
- Modify: `pkg/catch/vm_jailer_readiness.go`
- Modify: `pkg/catch/vm_runtime_reconcile.go`
- Modify: `pkg/catch/vm_runtime_reconcile_test.go`
- Modify: `pkg/catch/catch.go:183-220`
- Modify: `cmd/catch/catch.go:345-385`
- Modify: `pkg/catch/service_snapshots.go:15-35`

**Interfaces:**
- Produces: verified launch selection, running/trial markers, candidate fallback, `upgrade --restart`, and rollback.
- Consumes: staged descriptor, configured fallback, and retained previous runtime from Task 7.

- [x] **Step 1: Write malicious and failure-path tests**

Cover digest replacement between descriptor read and launch, Firecracker/jailer mismatch, missing configured fallback, candidate process exit before API readiness, candidate process exit during stabilization, successful API readiness, fallback launch, fallback failure, running marker cleanup, canceled command, ZFS pre-upgrade snapshot, raw-disk warning, disk writes preserved during launcher rollback, and background/startup consumption of healthy and failed trial results.

Add this trust-boundary test:

```go
func TestVMRuntimeTrialDoesNotUseGuestAgentAsAuthority(t *testing.T) {
	deps := defaultVMRuntimeTrialDeps()
	deps.queryGuestReady = func(context.Context, string) (vmAgentGuestReadyState, error) {
		t.Fatal("runtime trial queried untrusted guest agent")
		return vmAgentGuestReadyState{}, nil
	}
	// A host-ready candidate succeeds without consulting the guest.
}
```

- [x] **Step 2: Run and verify failure**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestSelectVMRuntimeLaunch|TestVMRuntimeTrial|TestWatchVMRuntimeTrialResults|TestRunVMConsoleProxy.*Runtime|TestVMRuntimeRestartUpgrade|TestVMRuntimeRollback' -count=1
```

Expected: FAIL because launch selection/trial handling is absent.

- [x] **Step 3: Verify and mark every launch**

Immediately before `exec.CommandContext`:

1. open Firecracker and jailer with no-follow semantics;
2. require root ownership and no group/other write;
3. hash the open files and compare descriptor digests;
4. verify ELF architecture and exact matching version probes;
5. run the existing jailer trusted-path preflight;
6. launch only through jailer as `yeet-vm`.

After the child starts, write a root-owned running marker containing runtime ID, both digests, runner PID, child PID, and start time. Remove it only if its inode and PID still belong to the exiting runner.

- [x] **Step 4: Implement trial readiness and fallback**

Host readiness requires the expected running marker, API socket readiness, jailer isolation marker, active runner, and five seconds without immediate exit. Guest-agent readiness may be displayed elsewhere but is not a gate.

If a staged candidate fails before readiness, start the still-configured runtime in the same `vm-run` supervisor, write a `failed-rolled-back` trial result, and keep that process running. If fallback also fails, write `failed-no-fallback` and return a terminal systemd failure whose exit status suppresses automatic restart. On success write `healthy` and continue supervising the candidate.

Start an injected-ticker background watcher from `NewServer`, alongside startup reconciliation, to consume root-owned trial results while Catch remains running. Under the per-VM mutation lock:

- `healthy` moves old `Configured` to `Previous`, promotes `Staged` to `Configured`, clears `Staged`, records the successful transition, and rewrites the descriptor without restarting the process;
- `failed-rolled-back` leaves `Configured` and `Previous` unchanged, clears `Staged`, records the failure, and rewrites the descriptor to the running configured runtime;
- `failed-no-fallback` leaves all selections intact for diagnosis and reports operator action required.

The same reconciler runs once at startup so a result written while Catch was down is not lost. Marker ownership, mode, descriptor generation, runtime IDs/digests, and live running PID must all agree before a result changes the database.

- [x] **Step 5: Implement explicit restart and launcher rollback**

For a ZFS zvol, create a protected disk recovery point with event `vm-runtime-upgrade` before stopping the VM. For raw disks, print that only launcher rollback is available. Then restart and wait for the trial result. On healthy, wait for the watcher/reconciler to promote the staged runtime, then unprotect the disk recovery point so normal retention applies. On `failed-rolled-back`, wait for reconciliation to clear staging, verify the configured pair is running, leave the disk recovery point protected, and report it; never restore disk automatically. On `failed-no-fallback`, keep the disk recovery point protected and return the full trial error.

`runtime rollback` selects `Previous`, stages it through the same transaction, and only restarts when `--restart` is present.

Run and commit:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestSelectVMRuntimeLaunch|TestVMRuntimeTrial|TestWatchVMRuntimeTrialResults|TestRunVMConsoleProxy.*Runtime|TestVMRuntimeRestartUpgrade|TestVMRuntimeRollback' -count=1
but commit catch-firecracker-runtime-management -m "catch: trial and roll back VM runtimes"
```

Implemented in `4fc8363`. Descriptor selection, fallback preflight, child launch,
and marker publication share the runtime root lock, while host readiness runs
without holding it. Every launch binds an exact descriptor generation, fresh
launch lease, immutable Firecracker/jailer evidence, matching version probes,
and a root-owned no-follow marker. Candidate failure falls back inside the same
supervisor; descriptor drift on either launch edge remains restartable rather
than becoming a stale terminal failure. Terminal results are generation-checked
after journal recovery and exact-removed when replaced or stale.

Explicit restarts serialize per VM, release the root lock before systemd,
protect ZFS disk recovery points, preserve raw-disk writes, and retain failed
recovery points for operators. Healthy points are unprotected by both the
foreground waiter and startup/background reconciliation; an older healthy
point must reconcile before a new trial can overwrite its DB reference.
Rollback stages the exact persisted `Previous` runtime. The trial dependency
surface contains no guest-agent or vsock authority.

Verification: focused and focused `-race` tests, the full repository suite,
package vet, both command builds, license check, and `git diff --check` passed.
Independent review cleared launch/stage races, terminal-result generation,
custom-root systemd flags, fallback restart behavior, and recovery-point
handoff. The known dirty-tree gofmt hook was bypassed only after manual gofmt
and all verification gates passed.

### Task 9: Add stage-on-restart policy and Catch-upgrade reconciliation

**Files:**
- Create: `pkg/catch/vm_runtime_policy.go`
- Create: `pkg/catch/vm_runtime_policy_test.go`
- Modify: `pkg/catch/vm_kernel_sync.go`
- Modify: `pkg/catch/vm_kernel_sync_test.go`
- Modify: `pkg/catch/vm_console_proxy.go`
- Modify: `pkg/catch/vm_jailer_upgrade.go`
- Modify: `cmd/catch/catch.go:1180-1265`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**
- Produces: policy staging at trusted host control points and coexistence with natural guest reboot/kernel sync.
- Consumes: verified catalog/cache state, staging transaction, descriptor-based systemd unit.

- [x] **Step 1: Write policy/no-restart tests**

Cover inherited manual, explicit manual, inherited stage-on-restart, per-VM override, stable/candidate channel, current runtime, EOL runtime, revoked candidate, custom runtime, running VM, stopped VM, catalog/cache failure, Catch install, and natural guest reboot. Assert no policy path calls `Start`, `Stop`, or `Restart`.

- [x] **Step 2: Run and verify failure**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestReconcileVMRuntimePolicy|TestCatchUpgradeDoesNotRestartVMs|TestAutoSyncVMGuestKernel.*Runtime' -count=1
```

Expected: FAIL because policy reconciliation is absent.

- [x] **Step 3: Implement effective policy and staging**

Use:

```go
type effectiveVMRuntimePolicy struct {
	Mode    string
	Channel string
}

func effectiveVMRuntimePolicyFor(host *db.VMHostConfig, vm *db.VMRuntimeLifecycleConfig) (effectiveVMRuntimePolicy, error)
```

Empty host fields mean `manual`/`stable`; empty VM fields mean inherit. `reconcileVMRuntimePolicy` stages only when mode is `stage-on-restart`, a newer promoted non-revoked runtime exists, and the VM source allows official upgrades. It performs no restart.

- [x] **Step 4: Preserve the reboot trust boundary**

The guest reboot hook remains the sole kernel-sync hook. Runtime discovery/download/policy are not called from `AutoSyncVMGuestKernelOnReboot`. The already-written host descriptor determines whether the next systemd start tries a staged runtime. Descriptor-managed adopted VMs retain the fail-closed kernel-sync guard until Independent VM Components Task 7 adds host-catalog-backed component reconciliation; Task 9 proves that this guard returns before changing the staged runtime descriptor, database lifecycle, Firecracker config, or unit state.

- [x] **Step 5: Integrate normal Catch upgrade and commit**

Catch install/adoption may refresh and stage policy runtimes only after the legacy checkpoint gate and artifact verification. The explicit policy setter also invokes the same reconciler after recording a `stage-on-restart` choice. Neither path is invoked by `runtime update`. Their commits rewrite descriptors/units but must not restart any VM. Network failure leaves current runtime/unit intact and reports a warning unless a required adoption invariant failed.

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestReconcileVMRuntimePolicy|TestCatchUpgradeDoesNotRestartVMs|TestAutoSyncVMGuestKernel.*Runtime|TestVMJailerUpgrade' -count=1
but commit catch-firecracker-runtime-management -m "catch: stage runtime policy at restart boundaries"
```

### Task 10: Implement component-aware runtime pruning and path migration

**Files:**
- Create: `pkg/catch/vm_runtime_prune.go`
- Create: `pkg/catch/vm_runtime_prune_test.go`
- Modify: `pkg/catch/vm_runtime_cmd.go`
- Modify: `pkg/catch/host_storage_refs.go:340-380`
- Modify: `pkg/catch/host_storage_refs_test.go`
- Modify: `pkg/catch/host_storage_db_rewrite.go:60-90`
- Modify: `pkg/catch/host_storage_db_rewrite_test.go`
- Modify: `pkg/catch/catch.go:1000-1060`
- Modify: `pkg/catch/remove_test.go`

**Interfaces:**
- Produces: fail-closed runtime reference collection, dry-run/remove rendering, data-root rewrite support, jail cleanup from selected runtime.
- Consumes: DB lifecycle state, running markers, transaction journals, stable catalog, host protections.

- [x] **Step 1: Write reference classification tests**

Cover configured, staged, previous, running, active transaction, stable catalog, protected, revoked-but-configured, unreferenced official, unreferenced imported, malformed manifest, symlink, unknown directory, and a monolithic directory also referenced by guest/kernel/ZFS base. Unknown/malformed entries must be kept with reason `unknown-reference`.

- [x] **Step 2: Run and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimePrune|TestHostStorage.*VMRuntime|TestCleanupVMJail.*Runtime' -count=1
```

Expected: FAIL because prune and path references are absent.

- [x] **Step 3: Implement fail-closed pruning**

Define:

```go
type vmRuntimePruneRow struct {
	RuntimeID string `json:"runtimeId"`
	Path      string `json:"path"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
}
```

Retain any entry referenced by configured/staged/previous/running/journal/stable/protection. `Previous` remains referenced until a later healthy transition replaces it. Delete only a validated immutable cache leaf with no references, using no-follow identity checks before rename-to-quarantine and removal. `--dry-run` and real prune must calculate the same rows.

- [x] **Step 4: Support data/service-root changes and removal**

Add every runtime artifact path and descriptor/journal path to host-storage reference analysis and DB rewrite. Service-root migration regenerates descriptor/unit paths without changing selected runtime. VM removal builds its jail cleanup plan from the configured/running component runtime, not `filepath.Dir(VM.Image.RootFS)`.

- [x] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimePrune|TestHostStorage.*VMRuntime|TestCleanupVMJail.*Runtime' -count=1
but commit catch-firecracker-runtime-management -m "catch: prune and migrate VM runtime state"
```

### Task 11: Complete docs, broad tests, and live canary validation

**Files:**
- Create: `scripts/test-firecracker-runtime-integration.sh`
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-agent.md`

**Interfaces:**
- Consumes: completed runtime command surface and behavior.
- Produces: a repository-owned integration driver, evergreen operator
  documentation, and release-ready validation evidence.

- [x] **Step 1: Add the repository-owned runtime integration driver**

Create `scripts/test-firecracker-runtime-integration.sh` for the exact pinned
Yeet checkout consumed by the image repository's KVM workflow. Its inputs are
verified candidate runtime, Ubuntu/NixOS guest, current/previous kernel, work
directory, and result-matrix paths. It must use the real Catch command surface
implemented by Tasks 1-10 to create isolated disposable VMs and exercise
jailer-only launch, configured UID/GID drop, readiness, natural reboot,
networking, disk-only recovery, raw and ZFS storage, custom data/service roots,
runtime trial/fallback, Catch restart, and cleanup. It emits the closed passed
matrix only after every representative scenario succeeds. Provide fixture-backed
command-sequence tests, fail if the candidate pair is not the running pair, and
provide no direct-Firecracker or memory-snapshot fallback.

- [x] **Step 2: Update help/manual tests and docs together**

Document running/configured/staged/previous distinctions, manual default, stage-on-restart behavior, candidate opt-in, EOL/revoked states, custom imports, ZFS pre-upgrade recovery points, raw-disk limitation, and the fact that guest package/reboot activity cannot request a host runtime update.

- [x] **Step 3: Run targeted and full verification**

```bash
mise exec -- go test ./pkg/db ./pkg/cli ./pkg/catchrpc ./cmd/yeet ./pkg/yeet ./pkg/catch ./cmd/catch -count=1
mise exec -- go test ./... -count=1
mise run race
mise run quality:goal
git diff --check
mise exec -- pre-commit run --all-files
```

Expected: all reachable project gates PASS; do not lower baselines.

- [ ] **Step 4: Prove adoption and staging on a production-like host**

With approved private environment variables set:

```bash
test -n "${YEET_VALIDATION_HOST:?set YEET_VALIDATION_HOST}"
yeet --host "$YEET_VALIDATION_HOST" vm runtime status --format=json-pretty
yeet --host "$YEET_VALIDATION_HOST" vm runtime update
```

Before and after the Catch update, record each running VM's systemd main PID. Verify all PIDs are unchanged, existing v1.14.3 runtimes are adopted and reported `legacy-unlisted` plus update-available unless an exact catalog support classification exists, and official current runtimes report exact matching Firecracker/jailer digests.

- [ ] **Step 5: Prove manual trial, natural restart, rollback, roots, and storage**

Use disposable VMs to validate:

- running stage without restart;
- natural guest reboot consumes only a pre-staged host runtime;
- explicit `--restart` healthy trial;
- deliberately broken candidate falls back to previous runtime;
- explicit rollback;
- ZFS protected pre-upgrade recovery point and separate disk restore;
- raw-disk launcher rollback warning;
- default/custom data root;
- default/custom/ZFS service root;
- Ubuntu and NixOS guests;
- kernel sync while a runtime is staged;
- Catch restart with prepared/derived-published/database-committed journals.

After each case, inspect status JSON, systemd unit/descriptor ownership, running marker, jailer drop UID/GID, and cache references.

Run the repository-owned integration driver with the exact unlisted runtime
manifest and artifact IDs selected for publication. Return its passed matrix to
the image-repository workflow, publish immutable integration evidence, and only
then open the reviewed candidate-catalog promotion PR.

- [ ] **Step 6: Commit docs and validation fixes only after all gates pass**

After explicit authorization to push the website submodule, commit and push it first, then commit the root docs/gitlink and any narrow test-derived fixes through GitButler. Do not push the root repository, promote another runtime, or deploy beyond the approved validation host without separate authorization.

```bash
git -C website diff --check
git -C website add docs/cli/yeet-cli.mdx docs/payloads/vms.mdx docs/operations/workflows.mdx
git -C website commit -m "docs: explain VM runtime lifecycle controls"
git -C website push origin HEAD
but commit catch-firecracker-runtime-management -m "docs: explain VM runtime lifecycle controls"
```
