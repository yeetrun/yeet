# Go 1.26 Source Modernization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the 50 current `modernize` findings with behavior-preserving Go 1.26 idioms, lock existing RPC JSON bytes before tag cleanup, and make `modernize` part of the permanent lint gate.

**Architecture:** Treat the current diagnostic set as a closed migration rather than an open-ended refactor. Land wire-format and concurrency-sensitive changes behind characterization tests first, then apply mechanical language and standard-library improvements in small package groups, and enable the linter only after a zero-finding scan.

**Tech Stack:** Go 1.26.6, golangci-lint v2.12.2 `modernize`, standard-library `maps`, `slices`, `reflect`, `sync/atomic`, GitButler, pre-commit.

**Spec:** `docs/superpowers/specs/2026-08-18-modern-terminal-and-dependency-upgrade-design.md`

## Prerequisites

- Begin from a landed, green `codex/charm-v2-terminal-modernization` result.
- Confirm `go.mod` and `.mise.toml` both select Go 1.26.6.
- Run `but pull --check` before creating `codex/go-1-26-source-modernization`.
- Re-run the bounded `modernize` command and compare it with the 50-finding inventory in this plan. If the count or locations changed because `main` moved, update the plan inventory before editing.

## Global Constraints

- Preserve JSON field presence, RPC request IDs, persisted data, loop ordering, error behavior, and command output.
- For ineffective `omitempty` options on non-pointer structs, remove only the ineffective option. Do not adopt `omitzero` unless existing bytes are already omitted and a test proves it.
- Preserve atomic ordering and uniqueness when replacing `sync/atomic` free functions with typed atomics.
- Use `go fix -diff ./...` as a review aid only. Do not apply unrelated fixes or bulk-accept semantic changes.
- Keep every commit limited to one compatibility surface or one mechanical package group.
- Do not refresh dependencies, deploy Catch, cut a release, push, or land on `main` as part of this plan without separate authorization.
- Run Go commands through `mise exec -- go ...`.

---

## Diagnostic Inventory

The synchronized baseline contains exactly these 50 findings:

| Category | Count | Files |
| --- | ---: | --- |
| `newexpr` | 19 | `cmd/yeet/cli.go`, Catch recovery/snapshot/tsns/netplan files, `pkg/dnet/dnet.go`, `pkg/yeet/run_draft.go` |
| `slicescontains` | 6 | Catch identity/ZFS, ISO, service resolver/systemd, remote exec |
| `minmax` | 4 | Catch byte progress, worker/retry bounds, VM balloon reclaim |
| `mapsloop` | 3 | Catch installer and service-network mutation |
| `rangeint` | 3 | Catch installer and ISO allocator |
| `omitzero` | 3 | `pkg/catchrpc/types.go` |
| `slicesbackward` | 3 | copyutil, db, svc |
| `forvar` | 3 | netns, Docker outdated, service command |
| `any` | 3 | Catch installer and VM netplan |
| `atomictypes` | 1 | `pkg/catchrpc/client.go` |
| `fmtappendf` | 1 | `pkg/catchrpc/client.go` |
| `reflecttypefor` | 1 | `pkg/cli/cli.go` |
| **Total** | **50** | |

Use this exact command for the baseline and completion checks:

```bash
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./...
```

Before implementation it must report 50 issues. At completion it must exit 0 with no output.

## File Structure

- Modify `pkg/catchrpc/types.go` and `pkg/catchrpc/types_test.go` for JSON tag preservation.
- Modify `pkg/catchrpc/client.go` and `pkg/catchrpc/client_test.go` for typed request IDs and `fmt.Appendf`.
- Modify `cmd/yeet/cli.go`, `pkg/cli/cli.go`, `pkg/dnet/dnet.go`, and `pkg/yeet/run_draft.go` for small language/API updates.
- Modify the listed `pkg/catch` files for bounds, range, map, slice, and pointer modernizations.
- Modify `pkg/copyutil/tar.go`, `pkg/db/db.go`, `pkg/iso/modes.go`, `pkg/netns/firewall.go`, and the listed `pkg/svc` and `pkg/yeet` files for collection/loop improvements.
- Modify `.golangci.yml` to enable `modernize` permanently.

### Task 1: Lock and preserve catchrpc JSON field behavior

**Files:**

- Modify: `pkg/catchrpc/types_test.go`
- Modify: `pkg/catchrpc/types.go:99-106,278-292`

**Interfaces:**

- Consumes: `encoding/json` behavior for non-pointer struct fields.
- Produces: unchanged bytes for `ServiceInfoResponse` and `HostStoragePlan` while eliminating three ineffective tag options.

- [ ] **Step 1: Add failing-or-characterizing field-presence tests before changing tags**

Add table-driven tests that marshal zero-value compatibility objects and assert:

- `ServiceInfoResponse{}` contains an `"info"` object, because the current non-pointer `ServiceInfo` field is never omitted;
- a zero-value `HostStoragePlan` contains a `"repairAction"` object, because the current non-pointer repair field is never omitted;
- the existing `TestHostStoragePlanLegacyMetadataOmittedWhenEmpty` continues to prove that zero `"estimate"` and `"legacy"` fields are absent due to `omitzero`.

Also unmarshal the produced bytes and compare the decoded values so the test covers both field presence and compatibility.

- [ ] **Step 2: Run the characterization tests before editing tags**

Run:

```bash
mise exec -- go test ./pkg/catchrpc -run 'Test(ServiceInfoResponse|HostStoragePlan.*(FieldPresence|Omitted|RoundTrip))' -count=1
```

Expected: the new tests pass against the old tags, proving the intended bytes are the current behavior.

- [ ] **Step 3: Remove only the ineffective options**

Make these exact tag changes:

```go
Info         ServiceInfo             `json:"info"`
RepairAction HostStorageRepairAction `json:"repairAction"`
Estimate     HostStorageEstimate     `json:"estimate,omitzero"`
```

Leave `Legacy` as `json:"legacy,omitempty,omitzero"` unless the live linter also identifies it. Do not change any field type to a pointer and do not add omission behavior.

- [ ] **Step 4: Verify bytes and the three diagnostics**

Run:

```bash
mise exec -- go test ./pkg/catchrpc -count=1
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./pkg/catchrpc/...
```

Expected: all catchrpc tests pass. The JSON tag diagnostics are gone; the client implementation diagnostics remain until Task 2.

- [ ] **Step 5: Commit the compatibility-preserving tag cleanup**

Commit only `pkg/catchrpc/types.go` and `pkg/catchrpc/types_test.go` on `codex/go-1-26-source-modernization` with:

```text
pkg/catchrpc: preserve explicit struct fields
```

### Task 2: Modernize catchrpc request ID generation safely

**Files:**

- Modify: `pkg/catchrpc/client.go:10-35,45-90`
- Modify: `pkg/catchrpc/client_test.go`

**Interfaces:**

- Consumes: concurrent `Client.Call` operations.
- Produces: monotonically unique decimal JSON-RPC IDs using `atomic.Uint64` and `fmt.Appendf`.

- [ ] **Step 1: Add a concurrent request-ID test**

Exercise one client from multiple goroutines against an `httptest.Server`, decode every request ID as an integer, and assert that the collected set contains each value from 1 through N exactly once. Keep N modest enough for routine race runs.

Add or retain a direct `buildRPCRequestPayload` assertion that request ID `42` is encoded as the JSON string bytes `42`, not a quoted string and not a floating-point value.

- [ ] **Step 2: Verify the new tests on the existing implementation**

Run:

```bash
mise exec -- go test -race ./pkg/catchrpc -run 'Test.*(Concurrent.*ID|RequestPayload)' -count=1
```

Expected: tests pass against the current free-function atomic implementation, establishing the contract.

- [ ] **Step 3: Apply the two bounded client changes**

Change the client field to:

```go
nextID atomic.Uint64
```

Use `c.nextID.Add(1)` at the call site. Replace `[]byte(fmt.Sprintf("%d", id))` with:

```go
fmt.Appendf(nil, "%d", id)
```

Do not change request sequencing, timeouts, error handling, or payload types.

- [ ] **Step 4: Verify package behavior and race safety**

Run:

```bash
mise exec -- go test ./pkg/catchrpc -count=1
mise exec -- go test -race ./pkg/catchrpc -count=1
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./pkg/catchrpc/...
```

Expected: all commands pass and catchrpc has zero `modernize` findings.

- [ ] **Step 5: Commit the client modernization**

Commit the client files with:

```text
pkg/catchrpc: modernize request IDs
```

### Task 3: Modernize bounds, ranges, and redundant loop variables

**Files:**

- Modify: `pkg/catch/byte_progress.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/iso_allocator.go`
- Modify: `pkg/catch/vm_balloon_controller.go`
- Modify: `pkg/netns/firewall.go`
- Modify: `pkg/yeet/docker_outdated.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: focused tests adjacent to those files only when existing coverage does not exercise the changed branch.

**Interfaces:**

- Consumes: existing retry bounds, worker minimums, allocation order, reclaim caps, and per-iteration closures.
- Produces: the same values and iteration order via `min`, `max`, range-over-int, and Go 1.22+ loop semantics.

- [ ] **Step 1: Identify and run focused branch tests**

Use `rg` to map each changed helper to its adjacent tests, then run the smallest matching tests for byte-progress ETA, Catch worker/retry clamping, ISO allocation exhaustion/order, VM balloon reclaim, firewall rules, Docker outdated host fan-out, and multi-host service commands.

- [ ] **Step 2: Add characterization tests only for uncovered boundaries**

Required boundaries are negative ETA clamped to zero, worker/retry values clamped to one, reclaim limited to the candidate's free bytes, first-free ISO index selection, and closure/goroutine capture of distinct host/CIDR values.

- [ ] **Step 3: Apply the mechanical replacements**

- use `max(eta, 0)`, `max(workers, 1)`, and `max(attempts, 1)`;
- use `min(candidate.FreeBytes, reclaim)` for balloon reclaim;
- use `for attempt := range 1024`, `for index := range iso.MaxLinks`, and `for index := range iso.MaxProjects`;
- remove only the three redundant `x := x` loop copies identified by the linter;
- change `...interface{}` and `map[string]interface{}` to `...any` and `map[string]any`.

Preserve every loop body and break/continue point.

- [ ] **Step 4: Verify the affected packages and findings**

Run:

```bash
mise exec -- go test ./pkg/catch ./pkg/netns ./pkg/yeet -count=1
mise exec -- go test -race ./pkg/catch ./pkg/netns ./pkg/yeet -run 'Test.*(Progress|Worker|Retry|Allocator|Balloon|Firewall|Outdated|Hosts)' -count=1
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./pkg/catch/... ./pkg/netns/... ./pkg/yeet/...
```

Expected: tests pass. Remaining findings in those packages are limited to categories assigned to later tasks.

- [ ] **Step 5: Commit the bounded loop modernization**

Commit the files touched by this task with:

```text
go: modernize bounds and loops
```

### Task 4: Modernize collections and type inspection

**Files:**

- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/service_identity_migration.go`
- Modify: `pkg/catch/service_network_mutation.go`
- Modify: `pkg/catch/zfs_root_candidates.go`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/copyutil/tar.go`
- Modify: `pkg/db/db.go`
- Modify: `pkg/iso/modes.go`
- Modify: `pkg/svc/resolver_mount.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/svc/systemd_tailscale.go`
- Modify: `pkg/yeet/exec_remote.go`
- Modify: adjacent tests for order, membership, and duplicate behavior.

**Interfaces:**

- Consumes: maps and slices whose traversal or membership semantics are already tested.
- Produces: identical map contents, reverse order, membership decisions, and service-type matching via Go 1.26 standard-library helpers.

- [ ] **Step 1: Lock ordering and predicate semantics**

Run existing package tests first. Add focused tests where needed to prove:

- `slices.Backward` consumers still visit elements from last to first;
- `slices.ContainsFunc` stops on the same predicate and returns the same result;
- `maps.Copy` preserves destination entries not overwritten by the source and source values win on duplicate keys;
- service-name argument recognition still accepts `ServiceName` and `[]ServiceName` only.

- [ ] **Step 2: Apply collection helpers one diagnostic at a time**

Use:

- `maps.Copy` for the three direct key/value copy loops;
- `slices.Contains` or `slices.ContainsFunc` for the six membership loops;
- `for index, value := range slices.Backward(values)` for the three reverse loops, retaining the original index when used;
- `reflect.TypeFor[ServiceName]()` in `pkg/cli/cli.go`.

Do not replace loops that perform side effects beyond membership or copying.

- [ ] **Step 3: Run focused and package tests**

Run:

```bash
mise exec -- go test ./pkg/catch ./pkg/cli ./pkg/copyutil ./pkg/db ./pkg/iso ./pkg/svc ./pkg/yeet -count=1
```

Expected: all packages pass with unchanged order-sensitive assertions.

- [ ] **Step 4: Review the standard-library diff**

Run:

```bash
mise exec -- go fix -diff ./pkg/catch ./pkg/cli ./pkg/copyutil ./pkg/db ./pkg/iso ./pkg/svc ./pkg/yeet
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./pkg/catch/... ./pkg/cli/... ./pkg/copyutil/... ./pkg/db/... ./pkg/iso/... ./pkg/svc/... ./pkg/yeet/...
```

Expected: `go fix -diff` shows no additional in-scope modernization missed by the task, or any remaining diff is explicitly reviewed and deferred. Remaining linter findings are pointer-expression items assigned to Task 5.

- [ ] **Step 5: Commit collection and reflection updates**

Commit this task's files with:

```text
go: use modern collection helpers
```

### Task 5: Replace one-line pointer helpers with Go 1.26 `new(expr)`

**Files:**

- Modify: `cmd/yeet/cli.go`
- Modify: `pkg/catch/recovery_points.go`
- Modify: `pkg/catch/recovery_service_root.go`
- Modify: `pkg/catch/service_identity_migration.go`
- Modify: `pkg/catch/service_snapshots.go`
- Modify: `pkg/catch/tsns.go`
- Modify: `pkg/catch/vm_lan_bridge_netplan.go`
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/yeet/run_draft.go`
- Modify: adjacent tests that inspect optional pointer values.

**Interfaces:**

- Consumes: value expressions currently passed through one-line pointer helpers.
- Produces: newly allocated pointers with the same pointed-to values and ownership semantics.

- [ ] **Step 1: Run optional-value tests before editing**

Run tests covering TTY override parsing, recovery/snapshot plans, Tailscale namespace config, netplan DHCP flags, DNet semaphore initialization, and run-draft restart flags.

- [ ] **Step 2: Replace each reported helper call**

Replace only the 14 reported call sites with `new(expr)`. Remove `boolPtr`, `boolPointer`, `intPointer`, `netplanBool`, and `runDraftBool` only after `rg` proves they have no remaining callers. Replace `ptr.To` only at the three linter-reported sites; do not remove a third-party import if other calls remain in the file or package.

- [ ] **Step 3: Format and verify focused packages**

Run:

```bash
mise exec -- gofmt -w cmd/yeet/cli.go pkg/catch/recovery_points.go pkg/catch/recovery_service_root.go pkg/catch/service_identity_migration.go pkg/catch/service_snapshots.go pkg/catch/tsns.go pkg/catch/vm_lan_bridge_netplan.go pkg/dnet/dnet.go pkg/yeet/run_draft.go
mise exec -- go test ./cmd/yeet ./pkg/catch ./pkg/dnet ./pkg/yeet -count=1
```

- [ ] **Step 4: Prove the bounded diagnostic set is empty**

Run:

```bash
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./...
```

Expected: exit 0 with no findings. If new findings appear because the preceding edits exposed another simplification, review them against the behavior constraints before changing code.

- [ ] **Step 5: Commit pointer-expression modernization**

Commit this task's files with:

```text
go: adopt expression-based allocation
```

### Task 6: Enable `modernize` and run the final quality gates

**Files:**

- Modify: `.golangci.yml`
- Modify: source or tests only for verified regressions uncovered by final gates.

**Interfaces:**

- Consumes: zero findings from Tasks 1-5.
- Produces: a permanent repository rule preventing reintroduction of outdated patterns.

- [ ] **Step 1: Enable the linter**

Add `modernize` to `linters.enable` in `.golangci.yml`:

```yaml
linters:
  default: standard
  enable:
    - cyclop
    - gocognit
    - gocyclo
    - modernize
```

- [ ] **Step 2: Run lint through both the focused and normal paths**

Run:

```bash
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./...
mise exec -- golangci-lint run --config .golangci.yml ./...
```

Expected: both commands exit 0.

- [ ] **Step 3: Run final repository verification once**

Run:

```bash
mise exec -- go test ./... -count=1
mise exec -- go test -race ./pkg/catchrpc ./pkg/catch ./pkg/netns ./pkg/yeet -count=1
mise exec -- pre-commit run --all-files
mise run quality
mise run vuln
git diff --check
```

Expected: every command passes, quality ratchets remain intact, and govulncheck reports zero reachable vulnerabilities.

- [ ] **Step 4: Commit the permanent lint rule and final corrections**

Commit `.golangci.yml` and only final corrections belonging to this plan with:

```text
quality: enforce Go modernization
```

- [ ] **Step 5: Review branch state without publishing**

Run `but status` and `but show codex/go-1-26-source-modernization`. Confirm the branch contains only this plan, is based on current `origin/main`, and has no uncommitted changes. Do not push or land without user authorization.
