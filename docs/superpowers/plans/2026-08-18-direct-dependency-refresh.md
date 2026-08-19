# Direct Dependency Compatibility Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refresh every remaining outdated direct Go dependency to the latest compatible stable release verified at implementation time, with release-note-driven tests and especially broad coverage for the Tailscale jump.

**Architecture:** Upgrade direct modules in compatibility-focused subsystem batches rather than one opaque `go get -u`. Each batch starts with current-version and release-note verification, changes only named direct modules plus necessary transitives, runs the affected package and risk-specific tests, and lands as its own reviewable commit.

**Tech Stack:** Go 1.26.6 modules, TOML 1.6, go-json-experiment, containerd v2, klauspost/compress, miekg/dns v1, Tailscale core/client v2, golang.org/x modules, GitButler, govulncheck, pre-commit.

**Spec:** `docs/superpowers/specs/2026-08-18-modern-terminal-and-dependency-upgrade-design.md`

## Prerequisites

- Begin from landed, green results of both earlier plans:
  - `2026-08-18-charm-v2-terminal-modernization.md`
  - `2026-08-18-go-1-26-source-modernization.md`
- Confirm `github.com/fatih/color` and the legacy Charm core paths are already absent.
- Run `but pull --check` before creating `codex/direct-dependency-refresh`.
- Reconfirm all versions on the implementation date. The targets below are the approved 2026-08-18 snapshot, not permission to downgrade if a newer stable compatible release exists.

## Approved Snapshot

| Module | Current baseline | Approved target |
| --- | --- | --- |
| `github.com/BurntSushi/toml` | `v1.4.1-0.20240526193622-a339e1f7089c` | `v1.6.0` |
| `github.com/Masterminds/semver/v3` | `v3.3.0` | `v3.5.0` |
| `github.com/containerd/containerd/v2` | `v2.3.2` | `v2.3.4` |
| `github.com/go-json-experiment/json` | `v0.0.0-20250813024750-ebf49471dced` | `v0.0.0-20260623181947-01eb4420fa68` |
| `github.com/hugomd/ascii-live` | `v0.0.0-20231008062449-0e53a4799f1e` | `v0.0.0-20250503202505-9695c975e852` |
| `github.com/klauspost/compress` | `v1.18.5` | `v1.19.2` |
| `github.com/miekg/dns` | `v1.1.58` | `v1.1.72` |
| `github.com/tailscale/depaware` | `v0.0.0-20251001183927-9c2ad255ef3f` | `v0.0.0-20260720165112-f20f66241ec6` |
| `golang.org/x/sync` | `v0.21.0` | `v0.22.0` |
| `golang.org/x/sys` | `v0.46.0` | `v0.47.0` |
| `golang.org/x/term` | `v0.44.0` | `v0.45.0` |
| `tailscale.com` | `v1.88.2` | `v1.102.2` |
| `tailscale.com/client/tailscale/v2` | `v2.5.0` | `v2.10.1` |

`github.com/fatih/color` is intentionally absent because the terminal plan removes it. `github.com/miekg/dns` remains on v1; moving to the separately hosted v2 module is explicitly out of scope.

## Global Constraints

- Do not run an unbounded `go get -u ./...` or independently pin transitive modules.
- Upgrade only the direct modules named by each task. Accept transitive changes selected by those modules, inspect them, and do not promote them to direct requirements without a source import or documented tooling need.
- Keep core `tailscale.com` and `tailscale.com/client/tailscale/v2` on compatible current releases in the same commit.
- Preserve TOML decoding/canonical writing, JSON view bytes, compression interoperability, DNS packet behavior, registry leases/content operations, semantic-version decisions, terminal modes, and Catch networking policy.
- Do not migrate miekg/dns to v2, change config schemas, refresh generated baselines, or relax quality goals to make the update pass.
- No live host upgrade, Catch replacement, release, push, or landing on `main` is authorized by this plan alone.
- Use `mise exec -- go ...` for Go commands.

---

## File and Package Map

- TOML: `pkg/yeet/prefs.go`, `pkg/yeet/project_config.go`, `pkg/yeet/prefs_fuzz_test.go`, project-config tests.
- Experimental JSON: `pkg/db/db_view.go`, `pkg/db/db_view_test.go`.
- Compression: `pkg/compress`, `pkg/codecutil`.
- DNS: `pkg/catch/dns*.go`, `pkg/catch/iso_dns*.go`, their tests and fuzz target.
- Containerd: `pkg/registry/containerd.go`, `pkg/registry/containerd_test.go`.
- Semver: Catch Tailscale updater/resolver, VM kernel sync, runtime policy, and tsns code.
- X modules: Docker outdated concurrency, terminal detection, Linux syscalls across Catch/DB/netns/service packages.
- Tailscale: `cmd/catch`, `pkg/catch`, `pkg/dnet`, `pkg/netns`, `pkg/registry`, `pkg/svc`, and Yeet's host/setup paths.
- Tooling/animation: `tools/tools.go` and `pkg/yeet/skirt.go`.

### Task 1: Reconfirm stable targets and capture the dependency baseline

**Files:**

- Read: `go.mod`, `go.sum`, `.mise.toml`
- Generated locally only: `.tmp/dependency-refresh/before-modules.txt`
- Generated locally only: `.tmp/dependency-refresh/before-vuln.txt`
- Generated locally only: `.tmp/dependency-refresh/before-yeet`, `.tmp/dependency-refresh/before-catch`

**Interfaces:**

- Consumes: the landed prior plans and live upstream module metadata.
- Produces: an auditable before-state and a final exact target list for Tasks 2-6.

- [ ] **Step 1: Confirm a clean, current base**

Run:

```bash
but pull --check
but status
mise exec -- go version
mise exec -- go mod verify
```

Expected: the session branch starts clean, Go is 1.26.6, and the module cache verifies.

- [ ] **Step 2: Query live direct-module versions**

For every module in the approved snapshot, run `mise exec -- go list -m -versions MODULE` (or `-json -u` for pseudo-version modules). Confirm the newest non-prerelease release or newest intended pseudo-version and record any target newer than the snapshot.

Do not select an RC, beta, major-version migration, or separately hosted successor without a new design decision.

- [ ] **Step 3: Re-read release notes for the final deltas**

At minimum, inspect the official notes for:

- TOML parsing/default-version changes;
- semver comparison or prerelease fixes;
- containerd client/content/lease and checkpoint defaults;
- JSON API/encoding compatibility;
- compression decoder, dictionary, concurrency, race, and architecture fixes;
- DNS message/transport/parser fixes;
- Tailscale security, DNS, Serve/Funnel, userspace networking, client API, and memory fixes.

Update this plan before coding if a newly selected version introduces a migration not covered here.

- [ ] **Step 4: Capture baseline graph, vulnerabilities, tests, and binary sizes**

Run:

```bash
mkdir -p .tmp/dependency-refresh
mise exec -- go list -m all > .tmp/dependency-refresh/before-modules.txt
mise run vuln | tee .tmp/dependency-refresh/before-vuln.txt
mise exec -- go test ./... -count=1
mise exec -- go build -o .tmp/dependency-refresh/before-yeet ./cmd/yeet
mise exec -- go build -o .tmp/dependency-refresh/before-catch ./cmd/catch
stat -f '%N %z' .tmp/dependency-refresh/before-yeet .tmp/dependency-refresh/before-catch
```

Expected: baseline tests pass and govulncheck reports zero reachable vulnerabilities.

- [ ] **Step 5: Record the final target table in the implementation notes**

Do not edit `go.mod` in this task. If the live targets match the plan, proceed. If not, record the version and official release-note URL that justified each adjustment.

### Task 2: Upgrade TOML and experimental JSON with format characterization

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify tests only if needed: `pkg/yeet/prefs_fuzz_test.go`, `pkg/yeet/project_config_test.go`, `pkg/db/db_view_test.go`

**Interfaces:**

- Consumes: preference/workspace TOML, project service TOML, and DB view JSON.
- Produces: the same accepted legacy inputs and canonical output with TOML 1.6 and the current go-json-experiment API.

- [ ] **Step 1: Add release-note-specific TOML tests before upgrading**

Retain all current round-trip tests and add focused cases for:

- legacy Yeet configuration still decoding under TOML 1.1 defaults;
- duplicate scalar/table keys returning an error instead of silently overwriting;
- large finite floats, if any Yeet config field accepts them, round-tripping without precision drift;
- canonical save output remaining byte-stable for an existing representative project config.

Do not add TOML 1.1-only syntax to Yeet's own saved format unless users already depend on it.

- [ ] **Step 2: Add JSON view byte and clone characterization**

In `pkg/db/db_view_test.go`, retain `TestViewJSONRoundTripAndInitializationRules` and add an exact representative JSON assertion covering nested service, VM, identity, and sandbox views. Prove unknown-field/error behavior that Yeet relies on before the library update.

- [ ] **Step 3: Upgrade exactly the two modules**

Using the live-confirmed targets, run the equivalent of:

```bash
mise exec -- go get github.com/BurntSushi/toml@v1.6.0 github.com/go-json-experiment/json@v0.0.0-20260623181947-01eb4420fa68
mise exec -- go mod tidy
```

Inspect `go.mod` and `go.sum`; unrelated direct requirements must not move.

- [ ] **Step 4: Run focused parser, round-trip, and fuzz tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./pkg/db -count=1
mise exec -- go test ./pkg/yeet -run '^Test(ProjectConfig|LoadProjectConfig|SaveProjectConfig|.*Prefs)' -count=1
mise exec -- go test ./pkg/db -run '^Test.*(View|JSON)' -count=1
mise exec -- go test ./pkg/yeet -run '^$' -fuzz '^FuzzClientConfigTOMLMatches$' -fuzztime=20s
```

Expected: canonical bytes and legacy decode tests pass; the bounded fuzz run finds no crash or semantic mismatch.

- [ ] **Step 5: Commit the format-library batch**

Commit the module files and only format-compatibility tests with:

```text
deps: refresh configuration codecs
```

### Task 3: Upgrade compression and DNS libraries

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify tests only if needed: `pkg/compress/compress_test.go`, `pkg/codecutil/codecutil_test.go`, `pkg/catch/dns_test.go`, `pkg/catch/iso_dns_test.go`

**Interfaces:**

- Consumes: gzip/deflate/Zstandard HTTP and file streams plus untrusted DNS packets.
- Produces: interoperable round trips, unchanged content negotiation, deterministic DNS filtering/routing, and no new races.

- [ ] **Step 1: Strengthen release-note-specific characterization**

Before upgrading, ensure tests cover:

- Zstandard round trips for empty, small, and multi-block payloads;
- invalid/truncated Zstandard input returning errors without partial-success claims;
- gzip/deflate/Zstandard request and response negotiation;
- DNS truncation retry over TCP, malformed multi-question packets, SVCB/HTTPS hints, DNSSEC records, and resolver fallback/error ordering.

Use existing tests where they already cover the contract; add only missing decoder/dictionary or concurrency cases motivated by the final release notes.

- [ ] **Step 2: Upgrade the latest v1-compatible compression and DNS modules**

Run the equivalent live-confirmed command:

```bash
mise exec -- go get github.com/klauspost/compress@v1.19.2 github.com/miekg/dns@v1.1.72
mise exec -- go mod tidy
```

Verify `github.com/miekg/dns` remains the v1 module path.

- [ ] **Step 3: Run focused package and race tests**

Run:

```bash
mise exec -- go test ./pkg/compress ./pkg/codecutil ./pkg/catch -run 'Test(ResponseWriter|Decompress|Zstd|DNS|ISODNS)' -count=1
mise exec -- go test -race ./pkg/compress ./pkg/codecutil ./pkg/catch -run 'Test(ResponseWriter|Decompress|Zstd|DNS|ISODNS)' -count=1
mise exec -- go test ./pkg/catch -run '^$' -fuzz '^FuzzFilterISODNSMessage$' -fuzztime=20s
```

Expected: round trips and DNS packet behavior remain green with no race or fuzz finding.

- [ ] **Step 4: Run full affected-package tests**

Run:

```bash
mise exec -- go test ./pkg/compress ./pkg/codecutil ./pkg/catch -count=1
```

- [ ] **Step 5: Commit the network-codec batch**

Commit module files and any focused tests with:

```text
deps: update compression and DNS
```

### Task 4: Upgrade semver and containerd client libraries

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify tests only if needed: `pkg/catch/tty_ops_update_test.go`, resolver/kernel/runtime policy tests, `pkg/registry/containerd_test.go`

**Interfaces:**

- Consumes: Tailscale/VM version strings and containerd client/content-store APIs.
- Produces: unchanged version selection and registry lease, upload, manifest, label, and cleanup behavior.

- [ ] **Step 1: Characterize semantic-version edge cases**

Add table cases at the owning helpers for the final semver release-note fixes: prerelease ordering, abbreviated/invalid versions Yeet accepts or rejects, and equality/current-version decisions. Do not broaden accepted CLI input merely because the library can parse it.

- [ ] **Step 2: Establish the containerd API boundary**

Run and retain tests for upload commit/release, already-exists handling, content readability, manifest resolution, label updates, abort cleanup, and background-context cancellation. Confirm with `rg` that Yeet does not use CRI checkpoint restore; no checkpoint behavior change should be observable.

- [ ] **Step 3: Upgrade the two modules**

Run the equivalent live-confirmed command:

```bash
mise exec -- go get github.com/Masterminds/semver/v3@v3.5.0 github.com/containerd/containerd/v2@v2.3.4
mise exec -- go mod tidy
```

Inspect compiler/API changes rather than adding adapters preemptively.

- [ ] **Step 4: Verify Catch version policy and registry operations**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'Test.*(TSUpdate|Tailscale.*Version|Kernel|Runtime.*Policy|ParseTSUpdateTarget)' -count=1
mise exec -- go test ./pkg/registry -count=1
mise exec -- go test -race ./pkg/registry -count=1
```

Expected: version decisions and all content/lease lifecycle tests pass.

- [ ] **Step 5: Commit the runtime-client batch**

Commit module files and compatibility tests with:

```text
deps: update runtime client libraries
```

### Task 5: Upgrade utility, terminal, and tooling modules

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Verify source: `pkg/yeet/skirt.go`, `pkg/yeet/docker_outdated.go`, terminal-detection call sites, syscall-heavy packages, `tools/tools.go`

**Interfaces:**

- Consumes: synchronization primitives, Unix APIs, terminal detection, ascii-live assets, and depaware tooling.
- Produces: unchanged builds and tests on supported Darwin/Linux architectures.

- [ ] **Step 1: Upgrade the five direct utility/tooling modules together**

Using live-confirmed targets, run the equivalent of:

```bash
mise exec -- go get github.com/hugomd/ascii-live@v0.0.0-20250503202505-9695c975e852 github.com/tailscale/depaware@v0.0.0-20260720165112-f20f66241ec6 golang.org/x/sync@v0.22.0 golang.org/x/sys@v0.47.0 golang.org/x/term@v0.45.0
mise exec -- go mod tidy
```

Keep `depaware` direct because `tools/tools.go` imports it.

- [ ] **Step 2: Inspect source and selected graph changes**

Check for API changes in semaphore/fan-out code, terminal raw-mode/detection code, Unix ownership/filesystem/netns calls, and the skirt animation. Inspect transitive movement caused by Tailscale tooling but do not preempt the core Tailscale update in Task 6.

- [ ] **Step 3: Run broad utility and platform tests**

Run:

```bash
mise exec -- go test ./cmd/yeet ./pkg/catch ./pkg/copyutil ./pkg/db ./pkg/dnet ./pkg/fileutil ./pkg/netns ./pkg/svc ./pkg/yeet -count=1
mise exec -- go test -race ./pkg/dnet ./pkg/yeet -run 'Test.*(Outdated|Exec|Terminal|Skirt)' -count=1
GOOS=linux GOARCH=amd64 mise exec -- go build ./cmd/yeet ./cmd/catch
GOOS=linux GOARCH=arm64 mise exec -- go build ./cmd/yeet ./cmd/catch
```

Expected: host tests pass and both Linux architectures compile.

- [ ] **Step 4: Verify dependency tooling**

Run the repository's depaware/quality hook through:

```bash
mise run quality
```

If the full quality command is prohibitively redundant at this intermediate point, run the named depaware subtask from `mise tasks` and reserve full `mise run quality` for Task 7. Record which one ran.

- [ ] **Step 5: Commit the utility batch**

Commit the module files and only necessary compatibility fixes with:

```text
deps: refresh Go utility modules
```

### Task 6: Upgrade Tailscale core and client together

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify source only for required API migrations in `cmd/catch`, `pkg/catch`, `pkg/dnet`, `pkg/netns`, `pkg/registry`, `pkg/svc`, and `pkg/yeet`.
- Modify adjacent tests for any required compatibility adapter.

**Interfaces:**

- Consumes: Tailscale local/client APIs, tsnet, userspace networking, DNS resolver integration, Serve/certificate helpers, auth keys, and managed tailscaled processes.
- Produces: the same Yeet topology, authorization, DNS, resolver, sidecar lifecycle, update, and host-connection behavior on current Tailscale libraries.

- [ ] **Step 1: Map every Tailscale import to its owning test before upgrading**

Use `rg -l 'tailscale.com/'` and group imports by:

- Catch startup and authorization;
- tsnet/service namespace creation;
- ISO and ordinary DNS/resolver policy;
- managed tailscaled install/update/unit verification;
- Tailscale client v2 auth-key/API use;
- dnet/netns and service-side socket/process verification;
- Yeet host discovery/setup and certificate/Serve behavior.

Record any import without an adjacent focused test and add a characterization test before editing that API call.

- [ ] **Step 2: Run the pre-upgrade Tailscale matrix**

Run:

```bash
mise exec -- go test ./cmd/catch ./pkg/catch ./pkg/dnet ./pkg/netns ./pkg/registry ./pkg/svc ./pkg/yeet -count=1
mise exec -- go test -race ./pkg/catch ./pkg/dnet ./pkg/netns ./pkg/svc ./pkg/yeet -run 'Test.*(Tailscale|TS|Resolver|DNS|NetNS|Sidecar|Auth)' -count=1
```

Expected: the matrix is green before module movement.

- [ ] **Step 3: Upgrade both Tailscale direct modules in one operation**

Using the live-confirmed compatible versions, run the equivalent of:

```bash
mise exec -- go get tailscale.com@v1.102.2 tailscale.com/client/tailscale/v2@v2.10.1
mise exec -- go mod tidy
```

Do not update one without the other. Inspect every transitive diff and any compiler error before changing source.

- [ ] **Step 4: Make only required API adaptations**

For every source change, add or update the nearest behavior test first. Preserve:

- `accept-dns=false` in isolated service namespaces;
- current public/tailnet route topology;
- safe resolver guard paths and fail-closed readiness checks;
- selected managed binary/unit verification and update rollback;
- auth-key policy and client error guidance;
- Catch operation permissions and RPC payloads;
- Serve/certificate socket paths and host discovery.

Do not use the upgrade to redesign Tailscale topology or adopt newly available APIs without a separate approved design.

- [ ] **Step 5: Run the post-upgrade focused and broad matrix**

Run:

```bash
mise exec -- go test ./cmd/catch ./pkg/catch ./pkg/dnet ./pkg/netns ./pkg/registry ./pkg/svc ./pkg/yeet -count=1
mise exec -- go test -race ./pkg/catch ./pkg/dnet ./pkg/netns ./pkg/svc ./pkg/yeet -run 'Test.*(Tailscale|TS|Resolver|DNS|NetNS|Sidecar|Auth)' -count=1
GOOS=linux GOARCH=amd64 mise exec -- go build ./cmd/yeet ./cmd/catch
GOOS=linux GOARCH=arm64 mise exec -- go build ./cmd/yeet ./cmd/catch
```

Expected: all tests pass and both Catch/Yeet binaries cross-build for Linux amd64 and arm64.

- [ ] **Step 6: Commit the Tailscale batch independently**

Commit module files, required API adaptations, and their tests with:

```text
deps: upgrade Tailscale libraries
```

### Task 7: Prove the final graph, quality, and remaining drift

**Files:**

- Generated locally only: `.tmp/dependency-refresh/after-modules.txt`
- Generated locally only: `.tmp/dependency-refresh/after-updates.txt`
- Generated locally only: `.tmp/dependency-refresh/after-yeet`, `.tmp/dependency-refresh/after-catch`
- Modify source/tests only for verified regressions attributable to this plan.

**Interfaces:**

- Consumes: all completed dependency batches.
- Produces: a clean, auditable candidate with current direct dependencies and an explicit report of any intentional drift.

- [ ] **Step 1: Verify direct selections and module integrity**

Run:

```bash
mise exec -- go mod tidy
mise exec -- go mod verify
mise exec -- go list -m all > .tmp/dependency-refresh/after-modules.txt
mise exec -- go list -m -u all | tee .tmp/dependency-refresh/after-updates.txt
git diff -- go.mod go.sum
```

Expected: every approved direct module selects the live-confirmed target, no removed direct dependency reappears, and remaining `-u` markers are either transitive selections or explicitly documented major/prerelease deferrals.

- [ ] **Step 2: Compare module graphs and binary sizes**

Run:

```bash
diff -u .tmp/dependency-refresh/before-modules.txt .tmp/dependency-refresh/after-modules.txt || true
mise exec -- go build -o .tmp/dependency-refresh/after-yeet ./cmd/yeet
mise exec -- go build -o .tmp/dependency-refresh/after-catch ./cmd/catch
stat -f '%N %z' .tmp/dependency-refresh/before-yeet .tmp/dependency-refresh/after-yeet .tmp/dependency-refresh/before-catch .tmp/dependency-refresh/after-catch
```

Review every direct and large transitive graph change. Record binary-size deltas and investigate unexpected multi-megabyte growth.

- [ ] **Step 3: Run final repository gates once on the stable candidate**

Run:

```bash
mise exec -- go test ./... -count=1
mise exec -- go test -race ./pkg/catchrpc ./pkg/catch ./pkg/compress ./pkg/codecutil ./pkg/db ./pkg/dnet ./pkg/netns ./pkg/registry ./pkg/svc ./pkg/yeet -count=1
mise exec -- pre-commit run --all-files
mise run quality
mise run vuln
git diff --check
```

Expected: all gates pass, quality ratchets are unchanged or improved, and govulncheck reports zero reachable vulnerabilities.

- [ ] **Step 4: Confirm no unintended major migrations or stale direct modules**

Run targeted graph checks for all approved modules. Specifically prove:

- Charm remains on `charm.land/.../v2` from the prior plan;
- miekg/dns remains on latest v1;
- Tailscale core and client/v2 match the final compatible pair;
- `fatih/color` remains absent;
- no direct module in the approved table still has a stable same-major update.

- [ ] **Step 5: Commit final test-only corrections if needed**

Absorb corrections into the batch that introduced them when its commit is still unpublished. Create no empty “finalize” commit.

- [ ] **Step 6: Review branch state without publishing or deploying**

Run `but status` and `but show codex/direct-dependency-refresh`. Confirm the branch contains only this dependency project, is based on current `origin/main`, and has no uncommitted changes. Do not push, land, install Catch, or run a live host upgrade without separate authorization.
