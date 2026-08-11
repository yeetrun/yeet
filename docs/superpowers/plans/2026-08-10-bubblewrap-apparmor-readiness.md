# Bubblewrap AppArmor Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make fresh Catch setup and the first sandbox-on activation prove Bubblewrap works as a non-root workload, installing one exact Yeet-owned AppArmor profile only on compatible restricted Ubuntu hosts.

**Architecture:** Keep `EnsureBubblewrap` and its existing host-global lock as the only readiness entry point. Run the generic probe as numeric UID/GID `65534:65534`; if it fails on restricted Ubuntu, a small AppArmor helper safely publishes and loads `/etc/apparmor.d/yeet-bwrap`, then repeats functional and security probes. Existing fresh-init and service activation callers remain unchanged.

**Tech Stack:** Go 1.26, Linux Bubblewrap, AppArmor parser/profile ABI 4.0, systemd, GitButler, MDX documentation.

## Global Constraints

- Never disable AppArmor or change a host-wide user-namespace sysctl.
- Never add setuid bits or file capabilities to `/usr/bin/bwrap`.
- Use the fixed readiness credential UID `65534`, GID `65534`; real activations retain their exact service UID/GID probe.
- Manage only `/etc/apparmor.d/yeet-bwrap` with exact policy-version-1 content; preserve and reject divergent content.
- Version 1 supports only an absent file or exact current content. Policy migration machinery is deferred.
- Debian and unrestricted Ubuntu stay on the package-only path.
- Existing Catch upgrades, `legacy`, `off`, dormant `off` edits, and non-native workloads stay inert.
- Do not change RPC types, permissions, CLI syntax, or any existing service activation caller.
- Keep public files free of private hostnames, service names, hashes, and local paths.

---

### Task 1: Run the readiness probe as a real non-root process

**Files:**
- Modify: `pkg/catch/bubblewrap_dependency.go`
- Modify: `pkg/catch/bubblewrap_dependency_test.go`

**Interfaces:**
- Produces: `const bubblewrapProbeUID = 65534` and `const bubblewrapProbeGID = 65534`.
- Produces: `bubblewrapDependencyDeps.runAs(context.Context, serviceSandboxCommand) ([]byte, error)`.
- Produces: `bubblewrapDependencyDeps.ensureRestrictedUserNS(context.Context, error) error`, called only after the non-root functional probe fails.
- Preserves: `EnsureBubblewrap(context.Context) error` and every existing caller.

- [ ] **Step 1: Write the failing credential and dispatch tests**

Add behavior tests that fail if the probe executes without the fixed outer credential or if a successful probe enters AppArmor handling:

```go
func TestEnsureBubblewrapRunsReadinessProbeAsFixedNonRootCredential(t *testing.T) {
	host := newBubblewrapTestHost()
	if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
		t.Fatal(err)
	}
	got := host.commands[0].credential
	if got == nil || got.Uid != 65534 || got.Gid != 65534 {
		t.Fatalf("credential = %#v, want 65534:65534", got)
	}
}

func TestEnsureBubblewrapRepairsOnlyFailedNonRootProbe(t *testing.T) {
	host := newBubblewrapTestHost()
	host.probeErr = errors.New("uid map denied")
	deps := host.deps()
	calls := 0
	deps.ensureRestrictedUserNS = func(_ context.Context, initial error) error {
		calls++
		if !errors.Is(initial, host.probeErr) { t.Fatalf("initial = %v", initial) }
		return nil
	}
	if err := ensureBubblewrapWith(context.Background(), deps); err != nil { t.Fatal(err) }
	if calls != 1 { t.Fatalf("repair calls = %d, want 1", calls) }
}
```

- [ ] **Step 2: Run the focused RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^TestEnsureBubblewrap(RunsReadinessProbeAsFixedNonRootCredential|RepairsOnlyFailedNonRootProbe)$' -count=1
```

Expected: FAIL because the dependency runner carries no outer credential and has no restricted-userns repair seam.

- [ ] **Step 3: Implement the minimal credential-aware probe**

Use the existing `serviceSandboxCommand` runner instead of creating another process abstraction:

```go
const (
	bubblewrapProbeUID uint32 = 65534
	bubblewrapProbeGID uint32 = 65534
)

diagnostic, err := deps.runAs(ctx, serviceSandboxCommand{
	Path: bubblewrapPath,
	Arguments: bubblewrapProbeArgs(int(bubblewrapProbeUID), int(bubblewrapProbeGID), deps.pathPresent),
	Credential: &syscall.Credential{Uid: bubblewrapProbeUID, Gid: bubblewrapProbeGID},
})
```

After package inspection/installation, return immediately on success; otherwise pass the exact functional error to `ensureRestrictedUserNS`. Keep apt execution on the existing root runner.

- [ ] **Step 4: Run the focused GREEN and existing dependency matrix**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^TestEnsureBubblewrap' -count=1
```

Expected: PASS. Existing apt, lock, cancellation, unsafe-path, and diagnostics tests remain green with fixed `65534:65534` expectations.

---

### Task 2: Install one exact AppArmor profile on restricted Ubuntu

**Files:**
- Create: `pkg/catch/bubblewrap_apparmor.go`
- Create: `pkg/catch/bubblewrap_apparmor_test.go`
- Modify: `pkg/catch/bubblewrap_dependency.go`

**Interfaces:**
- Produces: `ensureRestrictedBubblewrapAppArmor(context.Context, error) error` as the production `ensureRestrictedUserNS` implementation.
- Produces: package-private `ensureRestrictedBubblewrapAppArmorWith(context.Context, error, bubblewrapAppArmorDeps) error` for deterministic tests.
- Produces: exact constants for `/usr/sbin/apparmor_parser`, `/etc/apparmor.d/yeet-bwrap`, the restricted-userns sysctl, AppArmor enablement, and kernel profile inventory.
- Consumes: existing `captureServiceIdentityPathProof`, `removeProvenanceSafeArtifact`, `fileutil.SyncDir`, and `serviceSandboxCommand` rather than adding another provenance or command framework.

- [ ] **Step 1: Write the failing host-routing tests**

Cover these literal outcomes in a table:

```go
tests := []struct {
	name string
	osRelease string
	restricted string
	wantProfile bool
	wantInitial bool
}{
	{name: "Debian returns original probe error", osRelease: "ID=debian\n", wantInitial: true},
	{name: "unrestricted Ubuntu returns original probe error", osRelease: "ID=ubuntu\n", restricted: "0\n", wantInitial: true},
	{name: "restricted Ubuntu enters managed profile", osRelease: "ID=ubuntu\n", restricted: "1\n", wantProfile: true},
}
```

Assert Debian/unrestricted Ubuntu execute no parser and write no profile path.

- [ ] **Step 2: Run the host-routing RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^TestEnsureRestrictedBubblewrapAppArmorHostRouting$' -count=1
```

Expected: FAIL because the AppArmor helper does not exist.

- [ ] **Step 3: Add the exact policy and compatibility gate**

Create one focused file containing the version-1 profile from the approved design. Parse `/etc/os-release` with the existing exact-key pattern; require Ubuntu, AppArmor enabled, restricted-userns equal to `1`, a trusted parser, and a trusted profile directory. Return the original probe error on Debian/unrestricted hosts. Return a joined diagnostic on a compatible host whose parser or trusted paths are unavailable.

- [ ] **Step 4: Write failing profile publication and conflict tests**

Using a real `t.TempDir()` tree with the test UID as the configured trusted owner and a fake parser runner, prove:

```go
func TestEnsureRestrictedBubblewrapAppArmorPublishesLoadsAndVerifiesExactProfile(t *testing.T)
func TestEnsureRestrictedBubblewrapAppArmorAcceptsExactCurrentProfile(t *testing.T)
func TestEnsureRestrictedBubblewrapAppArmorPreservesDivergentProfile(t *testing.T)
func TestEnsureRestrictedBubblewrapAppArmorRollsBackNewProfileWhenPostLoadProbeFails(t *testing.T)
```

The success test must assert dry-parse before publication, no-replace publication, parser replace-load, both kernel profile names, a fixed-credential functional re-probe, exact stacked child label, zero `CapEff`, and a denied nested-userns attempt. The conflict test must compare literal before/after bytes and execute no load. The rollback test must leave the final path and both profile names absent.

- [ ] **Step 5: Run the publication RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^TestEnsureRestrictedBubblewrapAppArmor' -count=1
```

Expected: FAIL because safe publication, load, verification, and rollback are not implemented.

- [ ] **Step 6: Implement the minimal version-1 transaction**

Implement only these states:

```text
probe passes                         -> no AppArmor read/write
probe fails + incompatible host      -> original functional error
probe fails + profile absent         -> dry-parse, publish no-clobber, load, verify
probe fails + exact current profile  -> dry-parse, replace-load, verify
probe fails + divergent profile      -> preserve and fail
```

Publish with a same-directory temporary file, `os.Link(temp, final)` for atomic no-replace behavior, unlink the temporary name, and synchronize the directory. Capture the published file's durable proof. On a post-load failure, unload the two Yeet profiles, verify they are absent, and remove only the captured file through `removeProvenanceSafeArtifact`.

Do not implement older-profile migration, package extraction, alternate parser discovery, operator override includes, or profile garbage collection.

- [ ] **Step 7: Run AppArmor GREEN plus mutation-sensitive failure cases**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^(TestEnsureRestrictedBubblewrapAppArmor|TestEnsureBubblewrap)' -count=1
```

Expected: PASS with no temp/quarantine leak and no command after cancellation.

---

### Task 3: Preserve fresh-init and activation boundaries

**Files:**
- Modify: `cmd/catch/catch_test.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/service_sandbox_mutation_test.go`

**Interfaces:**
- Consumes: unchanged `EnsureBubblewrap(context.Context) error`.
- Preserves: fresh install ensures before installer construction; existing Catch upgrade never ensures; sandbox-on ensures before service staging; legacy/off paths never ensure.

- [ ] **Step 1: Add one fresh-init readiness regression**

Extend `TestCatchInstallBubblewrapGateRunsInsideInstallTransaction` with an event list proving a fresh readiness failure occurs after the Catch install lock and before account preparation or installer construction:

```go
want := []string{"acquire-install-lock", "inspect-catch-generation", "ensure-host-readiness", "release-install-lock"}
```

- [ ] **Step 2: Add one activation regression for the repaired host path**

Use the existing installer/service-set seams to return an injected AppArmor readiness error and assert byte-identical service DB/unit/artifacts plus zero runtime calls. Keep existing legacy/off negative tests unchanged.

- [ ] **Step 3: Run the boundary selectors**

Run:

```bash
mise exec -- go test ./cmd/catch -run '^TestCatchInstallBubblewrapGateRunsInsideInstallTransaction$' -count=1
mise exec -- go test ./pkg/catch -run 'Bubblewrap|Sandbox.*(Preflight|Mutation)|Legacy|Dormant' -count=1
```

Expected: PASS; no production activation caller changes are needed.

---

### Task 4: Update the operator documentation without changing CLI syntax

**Files:**
- Modify: `README.md`
- Modify: `website/docs/concepts/native-sandboxing.mdx`
- Modify: `website/docs/getting-started/host-setup.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`

**Interfaces:**
- Documents: `/etc/apparmor.d/yeet-bwrap`, restricted Ubuntu automatic setup, package-only Debian behavior, upgrade inertness, divergent-file failure, and the prohibition on global policy relaxation.
- Preserves: every existing command and flag example.

- [ ] **Step 1: Update only the dependency and troubleshooting paragraphs**

State that Catch installs the exact profile only when a compatible restricted Ubuntu host needs it; Debian remains package-only; ordinary Catch upgrades are inert; divergent profile content is preserved and reported; Yeet never disables AppArmor or changes global sysctls. Do not add changelog content or alter generated CLI help because command syntax is unchanged.

- [ ] **Step 2: Run existing release-asset and formatting checks**

Run:

```bash
mise exec -- go test ./pkg/yeet -run '^TestReleaseAssetsMatchCurrentCLI$' -count=1
git diff --check
git -C website diff --check
rg -n "private[-]host|/User[s]/|yeet-(hetz|pve1)" README.md website/docs .codex/skills
```

Expected: the existing release-asset test and diff checks PASS; private scan returns no new private infrastructure text. Do not add a prose source-text test: command syntax and help metadata are unchanged.

---

### Task 5: Verify and prepare the branch for review

**Files:**
- Verify all files changed by Tasks 1-4.

**Interfaces:**
- Produces: one reviewable GitButler branch with design, plan, production code, behavioral tests, and root docs.
- Defers: website commit/push and root gitlink publication until explicit publication authorization.

- [ ] **Step 1: Run focused and race tests**

```bash
mise exec -- go test ./cmd/catch ./pkg/catch ./pkg/yeet -count=1
mise exec -- go test -race ./cmd/catch ./pkg/catch ./pkg/yeet -run 'Bubblewrap|AppArmor|CatchInstall|ReleaseAssets' -count=1
```

- [ ] **Step 2: Run formatting, static, complexity, and Linux checks**

```bash
mise exec -- gofmt -w pkg/catch/bubblewrap_dependency.go pkg/catch/bubblewrap_dependency_test.go pkg/catch/bubblewrap_apparmor.go pkg/catch/bubblewrap_apparmor_test.go
git diff --check
mise exec -- staticcheck ./cmd/catch ./pkg/catch ./pkg/yeet
mise exec -- golangci-lint run --config .golangci.yml --new-from-rev 8aa9730d
bwrap_gate_dir=$(mktemp -d /tmp/yeet-bwrap-readiness.XXXXXX)
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 mise exec -- go test -c ./pkg/catch -o "$bwrap_gate_dir/catch.test"
unlink "$bwrap_gate_dir/catch.test"
rmdir "$bwrap_gate_dir"
```

- [ ] **Step 3: Run the complete task gate once**

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
```

Expected: all tests and hooks PASS. Do not run `quality:goal`; this change does not alter the quality tooling, parser/RPC surface, or release candidate.

- [ ] **Step 4: Commit intentionally with GitButler**

Use `but diff`, commit only root-repository task paths to `codex/bubblewrap-apparmor-readiness`, and leave unrelated work untouched. Keep the website submodule dirty and uncommitted unless the user separately authorizes its commit and push.

- [ ] **Step 5: Request code review and resolve findings test-first**

Review the exact committed object for spec, quality, and scope. For every confirmed finding, capture a focused behavioral RED before changing production, rerun proportional gates, amend the unpublished implementation commit, and repeat review until approved.
