# Native Bubblewrap Sandbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run fresh native binaries, shebang scripts, and timer-backed jobs in a strict Bubblewrap filesystem/process sandbox by default, while preserving every installed native service as `legacy` until an operator explicitly migrates or opts it out with `yeet service set`.

**Architecture:** Catch remains authoritative. Sandbox policy is stored beside artifacts with `staged`, `latest`, and `gen-N` references so the active generation, rollback, schedule changes, and pruning all agree. Generated native systemd units invoke `/usr/bin/bwrap` directly; systemd still owns identity, environment, cgroup, restart behavior, and the selected Yeet network namespace. A single deterministic native-unit renderer is reused by fresh installs and later sandbox, identity, network, root, schedule, and rollback operations. Bubblewrap installation is a Catch-owned, host-serialized prerequisite only for a genuinely fresh Catch install or an activation whose resulting policy is `on`.

**Tech Stack:** Go, `pkg/cli`, command-shaped Catch RPC, JSON-backed generation metadata, TOML project configuration, systemd units/timers, Bubblewrap namespaces, apt on Debian/Ubuntu, GitButler parallel branches, MDX documentation, GitHub release assets.

## Global Constraints

- Treat [the approved design](../specs/2026-08-09-native-bubblewrap-sandbox-design.md) as authoritative. Do not weaken a failure into direct execution.
- Before Task 1, finish, publish, and install the compatibility release `v0.10.16` on both configured production Catch hosts. Verify the deployed Catch binaries came from the published artifacts. Resolve private host identifiers from `AGENTS.local.md`; never commit them.
- Start implementation from current `origin/main` after `v0.10.16` lands. Use an independent GitButler branch named `codex/native-bwrap-sandbox`; do not stack it on, absorb, move, amend, clean, or publish the parallel `service-set-cron` branch.
- Native means `db.ServiceTypeSystemd` with a managed binary artifact, including shebang scripts and generations with a systemd timer. Exclude Catch, `system`, DNS/network/Tailscale helper units, Docker/Compose, and VMs.
- The only authoritative states are generation-local `legacy`, `on`, and `off`. Absence of policy at the active `gen-N` reference means `legacy`; never store a synthetic legacy record and never reinterpret absence as the fresh default.
- Fresh native services default to `on`. Existing services preserve their active policy during run, stage, redeploy, schedule update, root move, network change, identity change, start, restart, and rollback unless the operator uses the sandbox family under `service set`.
- `--sandbox=off` is independent of `--run-as=root`. Root remains sandboxed unless both settings are explicitly selected.
- Read-only exposures accept regular files or directories. Writable exposures accept directories only. Both accept `SOURCE[:DEST]`; the first colon separates fields, and literal colons are unsupported in v1.
- `service set` exposure lists are presence-aware complete lists per mentioned access class. Never remove an existing entry without class-specific `reset`; unmentioned classes remain unchanged.
- Do not add an independent network namespace, cgroup namespace, custom seccomp program, arbitrary device/socket exposure, writable file binds, or a long-lived Catch launcher.
- Bubblewrap's default PID-namespace helper is the reaper. Pass `--unshare-pid`; do **not** pass `--as-pid-1`, which disables that helper. Pass neither `--unshare-net` nor `--unshare-cgroup`.
- A functional policy probe under the target UID/GID is authoritative. Do not disable AppArmor, change user-namespace sysctls, install setuid workarounds, or relax host-wide policy.
- All dependency and policy preflight work must finish before stopping an installed workload or committing a replacement generation. A later service rollback does not uninstall the package.
- `service set` retains the existing `manage` permission; info/sync retain `read`. Add permission tests even though the classification itself does not change.
- Public docs and tests use generic names, `yeetrun.com`, and temporary paths. Do not commit private infrastructure names, service names, usernames, or host paths.
- Use `mise exec -- go ...`; add fuzz coverage for every new parser/path codec. Run focused tests while iterating, the complete suite once on the stable candidate, `pre-commit run --all-files`, `mise run quality`, and one final `mise run quality:goal`. Do not lower or refresh quality goals.
- The feature release is `v0.10.17`. Its upgrade must not install Bubblewrap and must not migrate services. Only published release artifacts may be used for the production-host upgrade and live rollout.

---

### Task 0: Compatibility release and branch gate

**Files:**
- Verify only: `docs/superpowers/plans/2026-08-09-service-set-cron.md`
- Verify only: `website/docs/changelog.mdx`
- Verify only: local `AGENTS.local.md`

**Interfaces:**
- Consumes: published `v0.10.16`, its five platform archives/checksums, and the two configured production host aliases kept outside public files.
- Produces: evidence that `origin/main`, the peeled `v0.10.16` tag, the GitHub release, website gitlink, and both deployed Catch binaries refer to the same release candidate.
- Produces: an independent GitButler implementation branch based on that landed commit.

- [ ] **Step 1: Prove the compatibility release is public and landed**

Run read-only checks:

```bash
git fetch origin --tags
release_commit=$(git rev-list -n1 v0.10.16)
test "$(git rev-parse origin/main)" = "$release_commit"
test "$(git ls-remote origin refs/heads/main | awk '{print $1}')" = "$release_commit"
test "$(git ls-remote origin 'refs/tags/v0.10.16^{}' | awk '{print $1}')" = "$release_commit"
gh release view v0.10.16 --json isDraft,isPrerelease,tagName,targetCommitish,url
git -C website fetch origin
website_commit=$(git rev-parse "$release_commit":website)
git -C website cat-file -e "$website_commit^{commit}"
test -n "$(git -C website branch -r --contains "$website_commit")"
```

Expected: every comparison succeeds; the release is public, non-draft, and non-prerelease. If the cron release is still a parallel branch, stop here and finish that plan first.

- [ ] **Step 2: Verify both hosts from published artifacts**

Follow the `yeet-cli` and live-upgrade instructions, using the two private aliases from `AGENTS.local.md`. For each host require:

```text
RPC version: v0.10.16
go version -m vcs.revision: equals the recorded release_commit value
go version -m vcs.modified: false
active Catch binary checksum: matches the corresponding published Linux archive
```

Do not rebuild from the workspace and do not change any service.

- [ ] **Step 3: Create the independent implementation branch at the safe boundary**

Run:

```bash
but pull --check
but pull
but status
```

The returned status must show `origin/main` at `v0.10.16`, no stack relationship to `service-set-cron`, and the design/plan branch preserved. Task 1's first checkpoint creates `codex/native-bwrap-sandbox` with `but commit ... -c`; do not use a native worktree or raw Git branch command.

---

### Task 1: Shared sandbox CLI grammar, help, bridge, and fuzzing

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Create: `pkg/cli/service_sandbox_fuzz_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`

**Interfaces:**
- Produces: `cli.SandboxExposure`, `cli.SandboxOptions`, `cli.ParseSandboxExposure`, and `cli.FormatSandboxExposure`.
- Produces: `RunFlags.Sandbox` and `ServiceSetFlags.Sandbox` with explicit state/list/reset presence.
- Produces: repeatable `--sandbox-ro` and `--sandbox-rw` in registry-derived client/Catch bridge metadata.
- Invariant: parsing is purely syntactic; Catch performs host existence, type, ownership, access, symlink, and canonicalization checks.

- [ ] **Step 1: Add failing table-driven exposure grammar tests**

Add tests with these exact cases:

```go
func TestParseSandboxExposure(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		allowReset bool
		want       SandboxExposure
		reset      bool
		wantErr    string
	}{
		{name: "same destination", raw: "/srv/shared", want: SandboxExposure{Source: "/srv/shared", Destination: "/srv/shared"}},
		{name: "remapped", raw: "/srv/shared:/opt/input", want: SandboxExposure{Source: "/srv/shared", Destination: "/opt/input"}},
		{name: "reset", raw: "reset", allowReset: true, reset: true},
		{name: "run rejects reset", raw: "reset", wantErr: "reset is only valid with yeet service set"},
		{name: "relative source", raw: "srv/shared", wantErr: "source must be absolute"},
		{name: "relative destination", raw: "/srv/shared:opt/input", wantErr: "destination must be absolute"},
		{name: "dirty destination", raw: "/srv/shared:/opt/../input", wantErr: "destination must be a clean absolute path"},
		{name: "literal colon", raw: "/srv/shared:/opt/input:copy", wantErr: "literal colons are not supported"},
		{name: "empty destination", raw: "/srv/shared:", wantErr: "destination must not be empty"},
	}
}
```

Also assert `FormatSandboxExposure` emits `SOURCE` when source equals destination and `SOURCE:DEST` otherwise.

- [ ] **Step 2: Add failing `run` and `service set` parser tests**

Require:

```go
type SandboxExposure struct {
	Source      string
	Destination string
}

type SandboxOptions struct {
	State         string
	StateSet      bool
	ReadOnly      []SandboxExposure
	ReadOnlySet   bool
	ReadOnlyReset bool
	Writable      []SandboxExposure
	WritableSet   bool
	WritableReset bool
}
```

Cover `on`, `off`, invalid/empty/repeated state, repeatable lists, reset alone, reset plus values, repeated reset, `run` reset rejection, and explicit `off` with dormant lists. Require path flags without an explicit state to remain presence-aware; Catch resolves the implied `on` after loading installed state.

Add one case from every `service set` family and require the sandbox family to be internally combinable but exclusive with cron, identity, network, root/ZFS, publish, and snapshot flags:

```go
if _, _, err := ParseServiceSet([]string{"api", "--sandbox=on", "--run-as=app"}); err == nil ||
	!strings.Contains(err.Error(), "sandbox settings cannot be combined with other service settings") {
	t.Fatalf("unexpected error: %v", err)
}
```

- [ ] **Step 3: Add failing registry/help and bridge tests**

Require the `run` and `service set` help metadata to contain:

```text
--sandbox=on|off
--sandbox-ro=SOURCE[:DEST]
--sandbox-rw=SOURCE[:DEST]
```

Add bridge cases for inline and separate values and repeated binds:

```go
{
	args: []string{"service", "set", "api", "--sandbox", "on", "--sandbox-ro", "/etc/app", "--sandbox-rw=/srv/cache:/cache"},
	wantService: "api",
	want: []string{"service", "set", "--sandbox", "on", "--sandbox-ro", "/etc/app", "--sandbox-rw=/srv/cache:/cache"},
},
```

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseSandbox|TestParseRunSandbox|TestParseServiceSetSandbox|TestBridgeServiceArgsServiceSet|TestCLI.*Help|TestCommandRegistry' -count=1
```

Expected: FAIL because the schemas do not register the sandbox flags.

- [ ] **Step 4: Implement the shared syntax model**

Add parsed schema fields:

```go
// runFlagsParsed and serviceSetFlagsParsed
Sandbox   string   `flag:"sandbox" help:"Native sandbox state: on, off"`
SandboxRO []string `flag:"sandbox-ro" help:"Expose a read-only file or directory as SOURCE[:DEST]; repeat for multiple paths"`
SandboxRW []string `flag:"sandbox-rw" help:"Expose a writable directory as SOURCE[:DEST]; repeat for multiple paths"`
```

Use `orderedFlagValues` so repeated entries retain command order. Implement:

```go
func ParseSandboxExposure(raw string, allowReset bool) (exposure SandboxExposure, reset bool, err error)
func FormatSandboxExposure(exposure SandboxExposure) string
func parseSandboxOptions(parseArgs []string, state string, ro, rw []string, allowReset bool) (SandboxOptions, error)
func (o SandboxOptions) HasChange() bool
```

Normalize the state to lowercase and accept only `on|off`. Reject a reset more than once. Store no `reset` sentinel in either exposure slice.

- [ ] **Step 5: Implement service-set family exclusivity**

Extend `serviceSetChanges` with `sandbox bool`, derive it from `flags.Sandbox.HasChange()`, include it in `any`, and validate it before root-specific validation:

```go
func validateServiceSetSandboxCombination(changes serviceSetChanges) error {
	other := changes
	other.sandbox = false
	if changes.sandbox && other.any() {
		return fmt.Errorf("sandbox settings cannot be combined with other service settings; apply them with separate service set commands")
	}
	return nil
}
```

Update the no-change error to list `--sandbox`.

- [ ] **Step 6: Add fuzz targets and run focused verification**

Create two fuzz targets:

```go
func FuzzParseSandboxExposure(f *testing.F)
func FuzzParseServiceSetSandbox(f *testing.F)
```

Seeds must include same-path, remap, reset, spaces, `..`, repeated colon, empty, `/`, and non-UTF-8 bytes. Successful exposure parses must format and reparse to the same struct. Successful service-set parses must never retain `reset` as a path.

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -count=1
mise exec -- go test ./pkg/cli -run '^$' -fuzz '^FuzzParseSandboxExposure$' -fuzztime=10s
mise exec -- go test ./pkg/cli -run '^$' -fuzz '^FuzzParseServiceSetSandbox$' -fuzztime=10s
```

Expected: PASS with no crash corpus.

- [ ] **Step 7: Create the Task 1 GitButler checkpoint**

Run `but diff`, copy the exact change IDs for only the five Task 1 paths, and pass those IDs to `but commit codex/native-bwrap-sandbox -c -m "cli: parse native sandbox settings" --changes`. Do not include any unassigned or website change.

The returned status must show the branch independent of every other active branch and leave unrelated `website` work untouched.

---

### Task 2: Generation-keyed database and RPC model

**Files:**
- Modify: `pkg/db/db.go`
- Modify: `pkg/db/migrate.go`
- Modify: `pkg/db/db_test.go`
- Modify generated: `pkg/db/db_view.go`
- Modify generated: `pkg/db/db_clone.go`
- Modify: `pkg/db/db_view_test.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`
- Modify: `pkg/catch/installer_service.go`
- Modify: `pkg/catch/installer_service_test.go`
- Modify: `pkg/catch/service_schedule_mutation.go`
- Modify: `pkg/catch/service_schedule_mutation_test.go`

**Interfaces:**
- Produces: `db.ServiceSandboxStore`, `db.ServiceSandboxPolicy`, and `db.ServiceSandboxExposure`.
- Produces: `(*db.Service).SandboxPolicy(gen int) (*ServiceSandboxPolicy, bool)`.
- Produces: `catchrpc.ServiceSandbox` and `catchrpc.ServiceSandboxExposure` for authoritative current-generation reporting.
- Invariant: only `on|off` are stored. Missing `gen-N` policy is reported as `legacy` by Catch.

- [ ] **Step 1: Add failing migration, clone, JSON, and rollback-reference tests**

Add a database fixture:

```go
service := &Service{
	Name: "api", ServiceType: ServiceTypeSystemd, Generation: 4, LatestGeneration: 5,
	Sandbox: &ServiceSandboxStore{Refs: map[ArtifactRef]*ServiceSandboxPolicy{
		Gen(3): {State: "off"},
		Gen(4): {State: "on", ReadOnly: []ServiceSandboxExposure{{Source: "/etc/app", Destination: "/etc/app"}}},
		"latest": {State: "on", ReadOnly: []ServiceSandboxExposure{{Source: "/etc/app", Destination: "/etc/app"}}},
	}},
}
```

Require deep clone independence, JSON round-trip, current generation lookup, older generation lookup, missing generation returning `false`, and view validity. Add a migration test loading `DataVersion: 14` with an existing native service and require `Sandbox == nil` after migration to version 15.

Add installer tests proving staged policy commits to both `latest` and `gen-N`, rollback commits `gen-N` back to `latest`, and pruning drops sandbox refs older than the artifact retention window.

Add a schedule-clone test proving an active `gen-4` policy is copied to the new generation while a missing policy stays missing/legacy.

- [ ] **Step 2: Define the persisted model and bump the schema**

Add these exact types and include all three in the viewer/cloner generator list:

```go
type ServiceSandboxExposure struct {
	Source      string
	Destination string
}

type ServiceSandboxPolicy struct {
	State     string
	ReadOnly  []ServiceSandboxExposure `json:",omitempty"`
	Writable  []ServiceSandboxExposure `json:",omitempty"`
}

type ServiceSandboxStore struct {
	Refs map[ArtifactRef]*ServiceSandboxPolicy `json:",omitempty"`
}

// In Service:
Sandbox *ServiceSandboxStore `json:",omitempty"`
```

Implement lookup by exact `db.Gen(gen)` and return a clone so callers cannot mutate database-backed state accidentally.

Set:

```go
const CurrentDataVersion = 15

// migrators
14: addServiceSandbox,

func addServiceSandbox(*Data) error { return nil }
```

The no-op is intentional: old records remain legacy.

- [ ] **Step 3: Extend generation commit, rollback, clone, and pruning**

In `commitGeneratedServiceRefs`, commit sandbox refs with the same `srcRef`, `dstRefs`, and generation numbers as artifacts:

```go
func commitSandboxRefs(store *db.ServiceSandboxStore, commit generatedServiceCommit) {
	if store == nil || store.Refs == nil {
		return
	}
	policy, ok := store.Refs[db.ArtifactRef(commit.srcRef)]
	if !ok {
		return
	}
	for _, ref := range commit.dstRefs {
		store.Refs[db.ArtifactRef(ref)] = cloneServiceSandboxPolicy(policy)
	}
}
```

Prune old `gen-N` sandbox refs with `shouldKeepArtifactRef`. Update `cloneActiveServiceGeneration` to delete stale `staged`, copy the active policy to `staged` only when present, then let `commitGeneratedServiceRefs` create the next generation. This makes schedule-only generations preserve on/off without turning legacy into explicit state.

- [ ] **Step 4: Define the wire representation**

Add:

```go
type ServiceSandboxExposure struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type ServiceSandbox struct {
	State     string                   `json:"state"`
	ReadOnly  []ServiceSandboxExposure `json:"readOnly,omitempty"`
	Writable  []ServiceSandboxExposure `json:"writable,omitempty"`
}

// ServiceInfo
Sandbox *ServiceSandbox `json:"sandbox,omitempty"`
```

Native services will always return a non-nil RPC sandbox object after Task 9, including `State: "legacy"`; non-native services return nil.

- [ ] **Step 5: Regenerate and run focused tests**

Run:

```bash
mise exec -- go generate ./pkg/db
mise exec -- gofmt -w pkg/db/db.go pkg/db/migrate.go pkg/db/db_test.go pkg/db/db_view_test.go pkg/catchrpc/types.go pkg/catchrpc/types_test.go pkg/catch/installer_service.go pkg/catch/installer_service_test.go pkg/catch/service_schedule_mutation.go pkg/catch/service_schedule_mutation_test.go
mise exec -- go test ./pkg/db ./pkg/catchrpc ./pkg/catch -run 'Sandbox|CommitGeneratedServiceRefs|CloneActiveServiceGeneration|Schedule' -count=1
```

Expected: PASS, and `git diff --exit-code -- pkg/db/db_view.go pkg/db/db_clone.go` after a second `go generate` proves deterministic generated output.

- [ ] **Step 6: Create the Task 2 checkpoint**

Use `but diff`, select only Task 2 files, and commit to `codex/native-bwrap-sandbox`:

```text
db: track sandbox policy by generation
```

---

### Task 3: Systemd argv codec and independent HOME directory

**Files:**
- Create: `pkg/svc/systemd_exec.go`
- Create: `pkg/svc/systemd_exec_test.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/svc/systemd_test.go`
- Modify: `pkg/catch/vm_runtime_adoption.go`
- Modify: `pkg/catch/vm_runtime_adoption_test.go`

**Interfaces:**
- Produces: `svc.RenderSystemdExecStart(argv []string) (string, error)`.
- Produces: `svc.ParseSystemdExecStart(value string) ([]string, error)`.
- Produces: `svc.SystemdUnit.HomeDirectory`; empty falls back to `WorkingDirectory` for existing callers.
- Invariant: rendering never invokes a shell and parsing/rendering agree on the supported argument domain.

- [ ] **Step 1: Add failing round-trip and injection tests**

Cover:

```go
tests := [][]string{
	{"/usr/bin/app"},
	{"/usr/bin/app", "plain", "two words", "", `quote"here`, `slash\\here`},
	{"/usr/bin/app", "$HOME", "100%", "colon:value", "unicode-✓"},
}
```

For every row, require `ParseSystemdExecStart(RenderSystemdExecStart(row))` to equal the original. Require NUL, CR, and LF to fail. Require `$` and `%` to survive without systemd environment/specifier expansion syntax. Add a unit test with `WorkingDirectory: "/"` and `HomeDirectory: "/srv/api/data"` and require:

```text
WorkingDirectory=/
Environment=HOME=/srv/api/data USER=app LOGNAME=app SHELL=/bin/sh
```

- [ ] **Step 2: Extract and harden the existing systemd parser**

Move the tested quote/backslash parser currently used by VM runtime adoption behind `svc.ParseSystemdExecStart`. Preserve its line-continuation handling at the VM caller. The parser must reject unsupported control characters, unterminated quotes/escapes, and an empty executable.

Implement minimal rendering: leave safe words unquoted; otherwise double systemd `%` and `$`, then use double-quoted C-style escaping for backslash, quote, tab, and other supported bytes. Parsing reverses `%%` and `$$` so render/parse is lossless. Do not use `strings.Join` for generated `ExecStart` after this task.

- [ ] **Step 3: Render the primary command once before executing the template**

Change the template from per-argument concatenation to:

```text
ExecStart={{.ExecStart}}
```

In `writeOutService`, call:

```go
execStart, err := RenderSystemdExecStart(append([]string{u.Executable}, u.Arguments...))
if err != nil {
	return fmt.Errorf("render systemd ExecStart: %w", err)
}
```

Add `ExecStart string` to the anonymous template data. Keep `Executable` and `Arguments` as the public construction fields so existing callers do not change.

Resolve HOME as:

```go
home := u.HomeDirectory
if home == "" {
	home = u.WorkingDirectory
}
```

Render the identity environment only when both `User` and `home` are non-empty.

- [ ] **Step 4: Run focused verification and systemd static parsing**

Run:

```bash
mise exec -- go test ./pkg/svc ./pkg/catch -run 'SystemdExec|SystemdUnit|VMRuntimeAdoptionExec' -count=1
```

On Linux with `systemd-analyze` available, write a generated test unit to a temporary directory and run `systemd-analyze verify` against it; skip only when the binary is genuinely unavailable.

- [ ] **Step 5: Create the Task 3 checkpoint**

Commit only Task 3 files:

```text
svc: render systemd argv safely
```

---

### Task 4: Host-serialized Bubblewrap dependency lifecycle

**Files:**
- Create: `pkg/catch/bubblewrap_dependency.go`
- Create: `pkg/catch/bubblewrap_dependency_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**
- Produces: `catch.EnsureBubblewrap(ctx context.Context) error`.
- Produces: an injected `ensureBubblewrapWith(ctx, deps)` for deterministic tests.
- Produces: a secure host-global flock at `/run/yeet/bubblewrap.ensure.lock`.
- Consumes: `catchInstallDeps.ensureBubblewrap` and `catchInstallDeps.catchAlreadyInstalled` inside the existing Catch install transaction.

- [ ] **Step 1: Add failing trust, install, probe, and serialization tests**

Table-drive these cases:

- trusted `/usr/bin/bwrap` skips apt and runs the probe;
- missing binary runs `apt-get update`, then noninteractive `apt-get install -y bubblewrap`, then re-stats and probes;
- non-regular, non-root-owned, group/world-writable, setuid, or replaced-after-stat binaries fail;
- unsupported OS or missing `/usr/bin/apt-get` returns exact manual install/probe guidance;
- apt failure and functional-probe failure retain the underlying diagnostic and never edit AppArmor/sysctls;
- two goroutines serialize through the lock and the second re-checks after acquiring it;
- canceled lock acquisition returns `context.Canceled` without running apt.

Use injected `lstat`, `open`, `flock`, `run`, and `readOSRelease` functions; no test may install a real package.

- [ ] **Step 2: Implement the secure ensure operation**

Use constants:

```go
const (
	bubblewrapPath     = "/usr/bin/bwrap"
	bubblewrapLockPath = "/run/yeet/bubblewrap.ensure.lock"
	aptGetPath         = "/usr/bin/apt-get"
)
```

Validate the lock directory/file with the same root-owned, one-link, regular-file, mode-0600 and nonblocking-flock pattern used by other Catch host locks. After acquiring the lock, re-run the binary inspection.

Inspect `/usr/bin/bwrap` with `Lstat` plus an opened descriptor/provenance check: regular file, UID 0, one link, no symlink, no setuid/setgid bits, and no group/world write bits. Do not accept PATH alternatives.

If missing on Debian/Ubuntu, run without a shell:

```text
/usr/bin/apt-get update
/usr/bin/apt-get install -y bubblewrap  (with DEBIAN_FRONTEND=noninteractive added to Cmd.Env)
```

Never run apt solely because an already trusted binary's functional probe was denied; return policy guidance instead of repeatedly modifying packages.

- [ ] **Step 3: Implement the generic functional probe**

Build the probe from the same fixed runtime-mount helper Task 5 will reuse. It must include:

```text
--unshare-user --unshare-pid --unshare-ipc --unshare-uts --disable-userns
--uid followed by decimal os.Geteuid() --gid followed by decimal os.Getegid()
--hostname yeet-bwrap-probe
--new-session --die-with-parent
--ro-bind /usr /usr
one --ro-bind source destination pair for each present /bin, /sbin, /lib, and /lib64 tree
--proc /proc --dev /dev --tmpfs /tmp --tmpfs /run
-- /usr/bin/true
```

Do not pass `--unshare-all`, `--unshare-net`, `--share-net`, `--unshare-cgroup`, or `--as-pid-1`. Capture stderr and convert common unknown-option, user-namespace, and AppArmor failures into actionable messages while preserving the raw diagnostic.

- [ ] **Step 4: Gate only genuinely fresh Catch installs**

Extend `catchInstallDeps`:

```go
ensureBubblewrap      func(context.Context) error
catchAlreadyInstalled func(*catch.Config) (bool, error)
```

The installed check reads the Catch service record under the existing Catch install lock and treats an active Catch generation as an upgrade. In `runCatchInstallTransaction`, after acquiring the lock and before `installCatchAndAdoptVMRuntimes`:

```go
installed, err := deps.catchAlreadyInstalled(cfg)
if err != nil {
	return fmt.Errorf("inspect existing Catch install: %w", err)
}
if !installed {
	if err := deps.ensureBubblewrap(ctx); err != nil {
		return fmt.Errorf("prepare Bubblewrap for first Catch install: %w", err)
	}
}
```

Add ordering tests proving an existing Catch upgrade does not call the ensure function and a fresh failure occurs before the Catch installer is created. `yeet upgrade` needs no new flag or client-side package action.

- [ ] **Step 5: Run focused tests and create the checkpoint**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Bubblewrap|HandleInstall|CatchInstall' -count=1
```

Commit:

```text
catch: prepare Bubblewrap progressively
```

---

### Task 5: Sandbox policy normalization, mount plan, and workload probe

**Files:**
- Create: `pkg/catch/service_sandbox.go`
- Create: `pkg/catch/service_sandbox_test.go`
- Create: `pkg/catch/service_sandbox_policy.go`
- Create: `pkg/catch/service_sandbox_policy_test.go`
- Create: `pkg/catch/service_sandbox_probe.go`
- Create: `pkg/catch/service_sandbox_probe_test.go`
- Create: `pkg/catch/service_sandbox_linux_integration_test.go`

**Interfaces:**
- Produces: generation-aware policy lookup and presence-aware patch application.
- Produces: deterministic `serviceSandboxPlan` containing Bubblewrap argv, cwd, HOME, hostname, resolver source, and normalized mounts.
- Produces: target-identity access checks and a harmless real-policy probe.

- [ ] **Step 1: Add failing state/default/patch/replacement tests**

Define internal normalized types:

```go
type serviceSandboxPolicy struct {
	State    string
	ReadOnly []serviceSandboxExposure
	Writable []serviceSandboxExposure
}

type serviceSandboxExposure struct {
	Source      string
	Destination string
}
```

Test:

- absent current generation => `legacy`;
- fresh with no flags => `on`;
- exposure-only fresh => `on`;
- explicit off plus exposures => off with dormant lists;
- existing legacy plus exposure-only => error requiring explicit on/off;
- existing off plus exposure-only => on, while explicit off keeps the edited lists dormant;
- existing on plus exposure-only => stays on;
- unmentioned class preserved;
- mentioned class containing every old entry plus additions succeeds;
- omission without reset errors with directly runnable preservation and replacement commands;
- reset clears only that class and may combine with new entries;
- normalized equality is a no-op;
- exact duplicate and RO/RW destination collision fail.

The omission error test must require both commands, for example:

```text
yeet service set api --sandbox-ro=/etc/app --sandbox-ro=/new/path
yeet service set api --sandbox-ro=reset --sandbox-ro=/new/path
```

When both classes omit values, print both necessary resets and complete lists. Quote shell-sensitive paths.

- [ ] **Step 2: Add failing path, type, canonicalization, and overlap tests**

Cover regular RO file, RO directory, RW directory, RW regular-file rejection, dangling symlink, symlink canonicalization, device, socket, FIFO, inaccessible UID/GID, missing active source, missing dormant source, and every overlap direction.

Mandatory destination fixtures must include runtime roots, payload, data, `/proc`, `/dev`, `/tmp`, `/run`, and each fixed `/etc` destination. Require `/etc/app` not to collide with `/etc/passwd`, but `/etc`, `/usr/local`, a parent of data, or a child of the payload file to fail.

- [ ] **Step 3: Define deterministic request/plan interfaces**

Use:

```go
type serviceSandboxPlanRequest struct {
	Service        string
	Policy         serviceSandboxPolicy
	Payload        string
	DataDir        string
	ResolverSource string
	UID            uint32
	GID            uint32
	Hostname       string
}

type serviceSandboxMount struct {
	Source      string
	Destination string
	Writable    bool
	Kind        string
}

type serviceSandboxPlan struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	HomeDirectory    string
	Mounts           []serviceSandboxMount
}

func buildServiceSandboxPlan(req serviceSandboxPlanRequest) (serviceSandboxPlan, error)
func validateServiceSandboxPolicy(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error)
func probeServiceSandbox(ctx context.Context, plan serviceSandboxPlan, uid, gid uint32) error
```

Keep pure ordering/collision logic separate from filesystem and command dependencies so most tests run on macOS. The stable hostname is the already validated service identifier itself; add a test that the 63-character maximum service name is accepted without truncation or hashing.

- [ ] **Step 4: Implement the fixed filesystem and namespace plan**

Bubblewrap already starts with an empty tmpfs root. Add present runtime trees in this order: `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`. Add present Debian/Ubuntu runtime files/directories at their same destinations:

```text
/etc/ld.so.cache
/etc/ld.so.conf
/etc/ld.so.conf.d
/etc/nsswitch.conf
/etc/passwd
/etc/group
/etc/hosts
/etc/localtime
/etc/timezone
/etc/os-release
/etc/ssl/certs
/etc/ssl/openssl.cnf
/etc/ca-certificates.conf
```

Bind `ResolverSource` to `/etc/resolv.conf`, not the host path when a Yeet network artifact is active. Bind the payload read-only at its managed absolute path and data read-write at its managed absolute path. Then add private `/proc`, `/dev`, `/tmp`, `/run`, followed by sorted operator mounts. Rely only on Bubblewrap's documented creation of missing destination parents at mode 0755; never expose a host parent merely to make a remapped destination exist. Test a nested destination whose parents are absent.

The command prefix is exactly:

```go
args := []string{
	"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--disable-userns",
	"--uid", strconv.FormatUint(uint64(req.UID), 10),
	"--gid", strconv.FormatUint(uint64(req.GID), 10),
	"--hostname", req.Hostname,
	"--new-session", "--die-with-parent",
}
```

Append filesystem operations, `--chdir`, the data directory, then `--`. Do not add `--clearenv`; systemd's environment file and identity variables must reach the payload.

- [ ] **Step 5: Implement active and dormant validation**

For both states, require absolute clean destinations, clean absolute lexical sources, no colon, deterministic sorting, and collision checks. For active `on` only:

- `Lstat` with no dangling source;
- `EvalSymlinks` and persist the canonical source;
- regular file or directory for RO, directory only for RW;
- reject devices, sockets, FIFOs, and other types;
- run `/usr/bin/test` as the requested credential to require read/traverse for RO and read/write/traverse for RW;
- re-stat after access checks before using the source in the final plan.

For off, keep a clean lexical source even if missing and defer type/access/canonicalization until activation.

- [ ] **Step 6: Implement the harmless policy probe and static unit hook**

Clone plan arguments and append `/usr/bin/true` after the existing `--`, never the real payload. Start `/usr/bin/bwrap` with:

```go
cmd.SysProcAttr = &syscall.SysProcAttr{
	Credential: &syscall.Credential{Uid: uid, Gid: gid},
}
```

Capture stderr. Verify that the payload/data/operator bind setup itself succeeds under the workload identity. Provide a separate injected helper:

```go
func verifyGeneratedSystemdUnit(ctx context.Context, path string) error
```

which runs `/usr/bin/systemd-analyze verify` with the supplied unit path as a separate argv element and without a shell.

- [ ] **Step 7: Add a real Linux integration test**

When Linux, `/usr/bin/bwrap`, and user namespaces are available, build a temp request and prove:

- data and `/tmp` are writable;
- RO file is readable but not writable;
- RW directory remap persists a file on the host;
- an unmounted host sentinel is absent;
- PID namespace sees no unrelated host process;
- `HOME` and cwd equal the data directory;
- inherited loopback/network namespace identity is unchanged by Bubblewrap.

Skip with the exact prerequisite reason; never install packages from the test.

- [ ] **Step 8: Run focused tests, fuzz path handling, and checkpoint**

Add `FuzzServiceSandboxDestinationCollisions` and `FuzzServiceSandboxPolicyNormalization`, then run:

```bash
mise exec -- go test ./pkg/catch -run 'ServiceSandbox|BubblewrapPolicy' -count=1
mise exec -- go test ./pkg/catch -run '^$' -fuzz '^FuzzServiceSandboxDestinationCollisions$' -fuzztime=10s
mise exec -- go test ./pkg/catch -run '^$' -fuzz '^FuzzServiceSandboxPolicyNormalization$' -fuzztime=10s
```

Commit:

```text
catch: build native Bubblewrap policy
```

---

### Task 6: Fresh native install, existing-run guard, and generated unit integration

**Files:**
- Create: `pkg/catch/service_sandbox_unit.go`
- Create: `pkg/catch/service_sandbox_unit_test.go`
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/tty_install_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/svc/systemd_test.go`

**Interfaces:**
- Consumes: `cli.RunFlags.Sandbox` into `FileInstallerCfg.Sandbox`.
- Produces: `resolveNativeInstallSandbox`, `validateExistingRunSandbox`, and `renderNativeSandboxUnit`.
- Produces: direct `/usr/bin/bwrap ... -- payload args` units for `on`, direct payload units for `off|legacy`.
- Invariant: the dependency/policy/unit preflight runs after the new payload file exists but before runtime replacement.

- [ ] **Step 1: Add failing fresh/default/preserve/guard tests**

Test binary, shebang script, and timer-backed payloads for:

- truly fresh with no flags => staged/committed `on` policy and Bubblewrap unit;
- fresh explicit off => direct unit plus dormant exposure metadata;
- existing legacy with no flags => direct next generation and no policy ref;
- existing on/off with no flags => exact active policy cloned to next generation;
- existing exact explicit policy => allowed;
- any existing run sandbox drift => error directing to `yeet service set` with the actual shell-quoted service name;
- Catch and helper services never receive policy or Bubblewrap;
- on invokes ensure and probes after the staged payload exists;
- legacy/off/dormant off does not invoke ensure;
- ensure/probe/static-verify failure leaves the old workload running and database generation unchanged.

- [ ] **Step 2: Carry sandbox options through Catch command parsing**

Add to `FileInstallerCfg`:

```go
Sandbox cli.SandboxOptions
```

Set it in `runFileInstallerCfg`. Reject sandbox flags for VM and every payload type that resolves to Docker/Compose with an error that says the flags apply only to native binaries, scripts, and scheduled jobs.

In `newFileInstaller`, call `validateExistingRunSandbox` next to the network guard. It loads the active generation policy, treats omitted sandbox flags as preservation, applies explicit flags in memory, and rejects any normalized difference. Repeat this guard after payload type detection so a direct/older client cannot exploit a type transition.

- [ ] **Step 3: Resolve and stage policy only after native detection**

Add `resolvedSandbox serviceSandboxPolicy` and `resolvedSandboxPresent bool` to `FileInstaller`. `resolveNativeInstallSandbox` follows:

```text
new + no state/list flags -> on
new + list flags, no state -> on
new + explicit off -> off, lists dormant
existing + no flags -> active exact policy or legacy absence
existing + explicit normalized equality -> active exact policy
existing + any difference -> service-set error
```

In `applyInstallPlanToService`, write the resolved on/off policy to `Sandbox.Refs["staged"]`; delete `staged` for legacy. Let `commitGeneratedServiceRefs` create the generation reference.

- [ ] **Step 4: Generate the sandboxed systemd unit**

For on:

```go
su.Executable = bubblewrapPath
su.ConditionExecutable = payload
su.Arguments = append(plan.Arguments, append([]string{"--", payload}, payloadArgs...)...)
su.WorkingDirectory = "/"
su.HomeDirectory = i.serviceDataDir()
```

For off/legacy retain direct payload execution and `WorkingDirectory == HomeDirectory == data`. Select resolver input before clearing systemd-only resolver directives:

```go
resolver := su.ResolvConf
if resolver == "" {
	resolver = "/etc/resolv.conf"
}
```

An on unit binds that source inside Bubblewrap and clears `su.ResolvConf`; it retains `NetNS`, `Requires`, and `After` so systemd selects the network namespace before invoking Bubblewrap.

- [ ] **Step 5: Implement one managed-unit transformer for later mutations**

Use:

```go
type nativeSandboxUnitRequest struct {
	CurrentPolicy serviceSandboxPolicy
	TargetPolicy  serviceSandboxPolicy
	Identity      db.ServiceIdentity
	Payload       string
	DataDir       string
	Resolver      string
	Hostname      string
}

func renderNativeSandboxUnit(raw string, req nativeSandboxUnitRequest) (unit string, plan *serviceSandboxPlan, err error)
```

Parse exactly one `[Service]` `ExecStart` with `svc.ParseSystemdExecStart`. For current on, require `/usr/bin/bwrap`, find the single policy `--`, and recover payload argv after it. For current off/legacy, treat the complete argv as payload argv. Verify argv[0] matches the active binary artifact.

Rewrite only `ExecStart`, `WorkingDirectory`, the recognized identity `Environment=HOME=... USER=... LOGNAME=... SHELL=/bin/sh`, resolver `BindReadOnlyPaths`, and resolver-owned `PrivateMounts`. Preserve timers, dependencies, restart settings, arbitrary environment directives, and final newline. Target on uses Bubblewrap and no systemd resolver bind; target off uses the payload directly and restores the effective resolver bind when needed.

- [ ] **Step 6: Place activation preflight before runtime mutation**

In `installOnClose`, after `installPreparedFile` has materialized the staged immutable payload but before `configureAndStageInstall` or any transition stop, call:

```go
func (i *FileInstaller) preflightNativeSandbox(ctx context.Context, plan fileInstallPlan) error
```

When on, it runs `EnsureBubblewrap`, validates/canonicalizes the complete policy, regenerates the staged unit with canonical sources, runs the harmless UID/GID policy probe, and statically verifies the unit. Update the staged policy with canonical sources before generation commit. When legacy/off, it is a no-op.

- [ ] **Step 7: Run focused tests and checkpoint**

```bash
mise exec -- go test ./pkg/catch ./pkg/svc -run 'Sandbox|FileInstaller.*Native|RunFileInstaller|SystemdUnit' -count=1
```

Commit:

```text
catch: sandbox fresh native workloads
```

---

### Task 7: Transactional `service set` sandbox mutation

**Files:**
- Create: `pkg/catch/service_sandbox_mutation.go`
- Create: `pkg/catch/service_sandbox_mutation_test.go`
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_service_set_test.go`
- Modify: `pkg/catch/tty_authz_test.go`
- Modify: `pkg/catch/service_identity_migration.go`
- Modify: `pkg/catch/service_identity_migration_test.go`

**Interfaces:**
- Produces: `(*Server).updateServiceSandboxLocked(ctx, name, options, out) error`.
- Produces: `serviceSandboxMutationPlan` and a `serviceIdentityMigrationRequest` using the existing journaled generation transaction.
- Invariant: a semantic no-op creates no generation; every other successful change creates exactly one generation and restores prior runtime intent on failure.

- [ ] **Step 1: Add failing eligibility, routing, authorization, and no-op tests**

Require only native managed systemd services. Reject missing, Catch, helper, Compose, VM, inconsistent generation/artifact, and unreadable unit/payload records before ensure or mutation.

Dispatch:

```go
err := execer.dispatch([]string{"service", "set", "--sandbox=on"})
```

must observe the existing per-service operation lock and exactly `permissionManage`. Info remains read-only via the existing RPC.

Test no-op on/off/lists returns without ensure, unit write, stop, generation change, or output claiming a restart.

- [ ] **Step 2: Add failing omission/reset and dormant-edit tests at the remote boundary**

The Catch-side tests—not only parser tests—must require:

- legacy exposure-only rejection;
- omission error before dependency work;
- preservation command contains every current plus requested entry;
- replacement command contains only necessary reset tokens and desired entries;
- off-to-off dormant edit stores a new generation but does not ensure Bubblewrap;
- off-to-on canonicalizes and ensures;
- on-to-off preserves dormant lists without ensuring;
- legacy-to-off creates a generation/restart even though both execute directly.

- [ ] **Step 3: Build a complete next generation**

Define:

```go
type serviceSandboxMutationPlan struct {
	previous        *db.Service
	target          *db.Service
	identity        resolvedServiceIdentity
	replacement     string
	generationPaths []string
	intent          []serviceIdentityPathState
	units           []string
	stage           func(context.Context) error
	stagedUnit      string
	noOp            bool
}
```

Clone active artifacts/policy to the next generation with the generalized schedule helper. Apply the requested policy only at the new generation. Render a fresh versioned primary unit artifact through `renderNativeSandboxUnit`, replace the target generation's systemd-unit ref, and leave the previous record byte-for-byte unchanged.

- [ ] **Step 4: Preflight before constructing the migration request**

When target on:

1. `EnsureBubblewrap`;
2. canonicalize/validate all sources for the existing identity;
3. rebuild exact mount argv and target unit;
4. run the harmless UID/GID probe;
5. run `systemd-analyze verify` on the staged unit.

When target off, perform syntax/collision validation and static unit verification only. Any failure removes only the newly staged unit artifact, leaves the package if installed, and does not stop the old unit.

- [ ] **Step 5: Apply with the existing journaled generation transaction**

Build:

```go
serviceIdentityMigrationRequest{
	Service: plan.previous.Name,
	Requested: plan.identity.Persisted.RequestedUser + ":" + plan.identity.Persisted.RequestedGroup,
	Target: plan.identity,
	TargetService: plan.target,
	ReplacementUnit: plan.replacement,
	StageGeneration: plan.stage,
	GenerationPaths: plan.generationPaths,
	GenerationIntents: plan.intent,
	GenerationUnits: plan.units,
	PreserveTargetServiceIdentity: true,
}
```

Call `migrateServiceIdentityLocked` because `service set` already owns the service lock. Extend transaction messages from identity-specific wording where necessary so generation-only sandbox failures do not claim an identity change, without weakening existing recovery checks.

For timer-backed services, use the timer as the primary runtime unit, preserve enabled/waiting intent, and set `ExpectProcess=false`; never start the payload service as verification.

- [ ] **Step 6: Add failure-injection and timer non-execution tests**

Inject failure:

- before stop (ensure, canonicalization, probe, static verify);
- after stop during unit install;
- during daemon reload/enable/start;
- during target verification;
- during rollback restoration.

Require exact previous database record, unit/timer bytes, enablement, running/inactive state, and sandbox generation after every recoverable failure. For a far-future timer, use a payload counter and require it remains untouched through successful migration and rollback.

- [ ] **Step 7: Wire service-set routing and output**

Add `sandbox` to `serviceSetChanges`. In `serviceSetCmdFunc`, route the sandbox family before network/non-network paths:

```go
if changes.sandbox {
	return e.applyServiceSetSandboxChange(flags)
}
```

The callback invokes `updateServiceSandboxLockedForServiceSet(ctx, e.s, e.sn, flags.Sandbox, e.rw)`. Do not add a prompt.

- [ ] **Step 8: Run focused/race tests and checkpoint**

```bash
mise exec -- go test ./pkg/catch -run 'ServiceSetSandbox|ServiceSandboxMutation|TTYAuthorization|ServiceIdentityMigration' -count=1
mise exec -- go test -race ./pkg/catch -run 'ServiceSandboxMutation|ServiceSetSandbox' -count=1
```

Commit:

```text
catch: mutate sandbox policy transactionally
```

---

### Task 8: Preserve sandbox truth across identity, network, storage, schedule, start, and rollback

**Files:**
- Modify: `pkg/catch/service_identity_migration.go`
- Modify: `pkg/catch/service_identity_migration_test.go`
- Modify: `pkg/catch/service_network_mutation.go`
- Modify: `pkg/catch/service_network_mutation_test.go`
- Modify: `pkg/catch/service_schedule_mutation.go`
- Modify: `pkg/catch/service_schedule_mutation_test.go`
- Modify: `pkg/catch/service_root_migration.go`
- Modify: `pkg/catch/tty_service_set_test.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `pkg/catch/tty_service.go`
- Modify: `pkg/catch/tty_service_test.go`
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/svc/systemd_test.go`

**Interfaces:**
- Consumes: the active/target generation's sandbox policy in every native unit rewrite.
- Produces: `(*Server).preflightSandboxGenerationActivation(ctx, service, gen) error`.
- Invariant: any operation that changes identity, resolver source, service root, or active generation regenerates/probes the Bubblewrap command before runtime mutation.

- [ ] **Step 1: Add cross-operation regression tests before production edits**

Create on/off/legacy fixtures and require:

- identity change updates systemd `User/Group` and Bubblewrap `--uid/--gid`, while `HOME` remains data and `WorkingDirectory` remains `/` for on;
- network change updates `NetworkNamespacePath` and the Bubblewrap resolver source without adding systemd resolver binds for on;
- host network/off restores the normal systemd resolver behavior;
- root move rewrites payload, data, resolver, HOME, and exposure sources only when they are inside the moved service root;
- schedule-only mutation copies the current policy to the new generation and does not alter Bubblewrap argv;
- rollback from on to legacy/off and back reports/activates the target generation policy;
- `start` and `restart` ensure/probe on, while legacy/off skip ensure;
- removed Bubblewrap causes an on activation to fail before changing generation or stopping an already running service.

- [ ] **Step 2: Preserve HOME independently during installed-unit identity rewrites**

Update both installed identity rewriters to parse the recognized identity environment's existing HOME. Use it when present; fall back to `WorkingDirectory` only for legacy direct units. `rewriteServiceIdentityUnit` must set target on to `WorkingDirectory=/` and HOME to the resolved service data directory, not overwrite HOME with `/`.

- [ ] **Step 3: Re-render Bubblewrap for identity changes**

After `RenderedPrimaryUnit` applies target `User/Group`, call `renderNativeSandboxUnit` with current/target policy and target numeric identity. Probe the target policy before constructing `serviceIdentityMigrationRequest`. The sandbox family remains exclusive with identity in one CLI call, but later identity-only calls must preserve and revalidate on.

- [ ] **Step 4: Make network mutation sandbox-aware**

Change `stageOwnedRegularNetworkSystemdUnit`/`renderRegularNetworkSystemdUnit` to accept previous and target service records rather than only artifact presence. Preserve the network dependency renderer, then call the native sandbox renderer with the new resolver artifact.

For target on, `appendRegularNetworkServiceRuntime` emits only `NetworkNamespacePath`; the resolver path is in Bubblewrap argv. For target legacy/off, retain a `BindReadOnlyPaths` mapping from the selected resolver artifact to `/etc/resolv.conf` and `PrivateMounts=yes`.

Atomic network+identity updates perform one final sandbox render/probe using both target settings.

- [ ] **Step 5: Cover root/host-storage relocation**

Ensure copied unit byte rewriting recognizes `ArtifactNetNSResolv` and the data directory. The root reference rewriter must update Bubblewrap source and same-path destination values inside the moved service root while leaving external operator exposures unchanged. Add the sandbox store to database clone/rewrite equality tests; policy exposure strings under the old service root must be relocated during an explicit service-root move, not during a host-wide root move that preserves the service root.

- [ ] **Step 6: Preflight rollback/start/restart activations**

Refactor rollback selection so it computes the target generation without mutating the database, calls:

```go
func (s *Server) preflightSandboxGenerationActivation(ctx context.Context, service *db.Service, gen int) error
```

and only then commits/installs the target generation with an expected-current-generation guard. If target policy is on, ensure, validate, probe, and static-verify its unit. If absent/off, skip dependency installation.

Call the same helper from Yeet-managed native `start` and `restart` before systemd. External `systemctl` remains outside Yeet's dependency lifecycle.

- [ ] **Step 7: Run broad focused and race verification**

```bash
mise exec -- go test ./pkg/svc ./pkg/catch -run 'Sandbox|Identity|NetworkMutation|ServiceRoot|HostStorage|Schedule|Rollback|Start|Restart' -count=1
mise exec -- go test -race ./pkg/catch -run 'Sandbox|NetworkMutation|IdentityMigration|Rollback' -count=1
```

Expected: PASS; existing legacy fixtures retain byte-compatible direct units.

- [ ] **Step 8: Create the Task 8 checkpoint**

Commit:

```text
catch: preserve sandbox across service changes
```

---

### Task 9: Client config, run guard, sync, and operator-visible info

**Files:**
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/project_config_test.go`
- Modify: `pkg/yeet/run_changes.go`
- Modify: `pkg/yeet/run_changes_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`
- Modify: `pkg/yeet/svc_cmd_routing_test.go`
- Modify: `pkg/yeet/service_sync.go`
- Modify: `pkg/yeet/service_sync_test.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`

**Interfaces:**
- Produces TOML fields: `sandbox`, `sandbox_ro`, and `sandbox_rw`.
- Produces authoritative `serviceSandboxInfo` from active generation state.
- Produces one shared existing-run server-info fetch for protected network and sandbox comparisons.
- Invariant: local config changes only after remote success and stores Catch's canonical result, never the raw request/reset tokens.

- [ ] **Step 1: Add failing TOML round-trip and clone tests**

Extend both public and TOML structs:

```go
Sandbox   string   `toml:"sandbox,omitempty"`
SandboxRO []string `toml:"sandbox_ro,omitempty"`
SandboxRW []string `toml:"sandbox_rw,omitempty"`
```

Test on/off/legacy, same-path and remapped exposure formatting, list clone independence, stable encoding order, and absence compatibility. `legacy` is valid in TOML but never emitted as a run flag; it records authoritative installed state.

- [ ] **Step 2: Rehydrate stored sandbox options for run**

Add:

```go
func runArgsWithSandboxOptions(args []string, entry ServiceEntry) []string
func sandboxEntryFromServiceInfo(info *catchrpc.ServiceSandbox) (state string, ro, rw []string, err error)
```

For on/off, prepend the concrete `--sandbox=on` or `--sandbox=off` value and repeated canonical binds before `--`. For legacy, prepend nothing. Ensure control flags are removed before storing payload args. Fresh successful native deployment fetches `ServiceInfo` and writes the explicit resolved state; do not guess on from local config when Catch says the service already existed.

- [ ] **Step 3: Refactor existing-run protected-setting validation to one fetch**

Replace the network-only guard with:

```go
func rejectExistingRunProtectedChanges(ctx context.Context, entry ServiceEntry, runArgs []string) error
```

Fetch `ServiceInfo` once. Network comparison retains current behavior. Sandbox comparison treats omitted sandbox flags as exact preservation; explicit flags are applied to the authoritative policy and compared after normalization. Any actual difference prints the equivalent `yeet service set` sandbox command. Catch's Task 6 guard remains authoritative.

- [ ] **Step 4: Save service-set results from Catch, not request flags**

After a successful sandbox mutation, fetch `ServiceInfo` and replace all three local sandbox fields with the returned state/lists. If the project entry is missing, print the existing exact `yeet service sync` command. If the fetch/save fails after remote success, return an error that clearly says Catch changed and includes the recovery command. Do not store `reset`.

- [ ] **Step 5: Add sandbox state to service sync**

Extend `serviceSyncResult` with sandbox state/lists and a `SandboxSynced` marker. For native services require non-nil sandbox info and set the three fields. Reject sandbox info on non-native types. New `--host` create-missing entries must include the authoritative sandbox state even when it is legacy.

- [ ] **Step 6: Report active-generation sandbox info from Catch**

Implement:

```go
func serviceSandboxInfo(sv db.ServiceView) *catchrpc.ServiceSandbox
```

Return nil for non-native/Catch/helper services. For an eligible native service, look up `SandboxPolicy(sv.Generation())`; missing becomes `{State:"legacy"}`. Clone and sort exposures deterministically. Add it to `serviceInfoWithContext` before runtime discovery.

- [ ] **Step 7: Render plain and JSON info**

In the server section show:

```text
Sandbox: on|off|legacy
Sandbox read-only: /source, /source:/dest
Sandbox writable: /source, /source:/dest
```

For legacy add one concise row:

```text
Sandbox migration: yeet service set SERVICE --sandbox=on  (or --sandbox=off)
```

JSON uses the typed RPC object. The client section may show saved state, but label it as project config so it cannot be confused with active server truth.

- [ ] **Step 8: Run focused tests and checkpoint**

```bash
mise exec -- go test ./pkg/yeet ./pkg/catch ./pkg/catchrpc -run 'Sandbox|RunChanges|ServiceSync|ServiceInfo|Info|ProjectConfig|ServiceSet' -count=1
```

Commit:

```text
client: sync native sandbox policy
```

---

### Task 10: CLI reference, README, manual, troubleshooting, and release notes

**Files:**
- Modify: `README.md`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-agent.md`
- Modify: `pkg/yeet/release_assets_test.go`
- Create: `website/docs/concepts/native-sandboxing.mdx`
- Modify: `website/docs/concepts/service-types.mdx`
- Modify: `website/docs/concepts/configuration-and-prefs.mdx`
- Modify: `website/docs/getting-started/host-setup.mdx`
- Modify: `website/docs/getting-started/service-workspace.mdx`
- Modify: `website/docs/getting-started/first-run-validation.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/payloads/cron-jobs.mdx`
- Modify: `website/docs/changelog.mdx`

**Interfaces:**
- Produces: evergreen manual for current behavior and a standalone `v0.10.17` migration/release entry.
- Produces: regenerated agent help matching the parser registry.
- Invariant: no private live-rollout details appear in public files.

- [ ] **Step 1: Add failing release-surface assertions**

Extend `TestReleaseAssetsMatchCurrentCLI` to require README and generated help to contain the three flags, `legacy`, and `service set`, while rejecting `--sandbox=legacy` as a runnable example. Require docs to distinguish writable directories from RO files/directories.

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestReleaseAssetsMatchCurrentCLI -count=1
```

Expected: FAIL before docs/reference generation.

- [ ] **Step 2: Document the evergreen command contract**

The README and native sandbox page must state directly:

- fresh native binary/script/timer services use Bubblewrap by default;
- existing native services remain legacy until explicit one-by-one choice;
- service data is RW, payload/runtime are RO, `/tmp` and `/run` are private;
- `/root`, `/home`, `/var`, `/sys`, and other services are absent by default;
- RO supports file/dir, RW supports dir, and optional remap is `SOURCE:DEST`;
- mentioned lists are complete and guarded; show preservation and reset commands;
- `--sandbox=off` is the escape hatch and independent of `--run-as=root`/network mode;
- user/PID/IPC/UTS are new, network/cgroup are inherited;
- this is not VM isolation, especially for root workloads.

Use generic examples only:

```bash
yeet run api ./api --sandbox-ro=/etc/api --sandbox-rw=/srv/api-cache:/cache
yeet service set api --sandbox=on
yeet service set api --sandbox-ro=reset --sandbox-ro=/etc/api
yeet service set api --sandbox=off
```

- [ ] **Step 3: Document progressive dependency behavior and troubleshooting**

State the exact triggers: fresh Catch install, new/resulting-on native activation, and service-set resulting on. Explicitly state Catch/Yeet upgrades, legacy, off, and dormant off edits do not install Bubblewrap.

Troubleshooting covers:

```bash
sudo apt-get update
sudo apt-get install -y bubblewrap
/usr/bin/bwrap --version
```

and tells operators to inspect AppArmor/user-namespace policy without disabling it globally. Explain that unsupported/old packages need an operator-managed compatible Bubblewrap rather than a Yeet sysctl workaround.

- [ ] **Step 4: Update first-run/disposable validation and config docs**

Add `sandbox`, `sandbox_ro`, and `sandbox_rw` to `yeet.toml` documentation. Extend first-run validation with a disposable native service that proves data write, hidden host sentinel, info state, one remapped RO file, one RW directory, explicit off, and cleanup. For cron, state that changing sandbox policy replaces/verifies timer units without running the job.

- [ ] **Step 5: Regenerate help and prepare `v0.10.17` changelog**

Run:

```bash
tools/generate-yeet-help-agent.sh
```

Add `### v0.10.17` under the current date with at most three user-facing bullets covering default-on fresh native sandboxes, explicit legacy migration/off escape hatch and bind controls, and progressive dependency installation. Do not mention implementation files, GitButler, host aliases, test services, or submodule mechanics.

- [ ] **Step 6: Verify and checkpoint root docs**

```bash
mise exec -- go test ./pkg/yeet -run TestReleaseAssetsMatchCurrentCLI -count=1
mise exec -- go run ./cmd/yeet run --help-agent
mise exec -- go run ./cmd/yeet service set --help-agent
```

Commit root README/help/test changes to `codex/native-bwrap-sandbox`:

```text
docs: explain native sandbox controls
```

Keep website changes uncommitted inside the submodule until Task 11's release boundary; do not absorb unrelated website work.

---

### Task 11: Complete gates, review, GitButler landing, and `v0.10.17` publication

**Files:**
- Verify: all changed Go/docs/generated files
- Commit/push inside submodule: Task 10 website files
- Publish root: feature/spec/plan/README/help/test plus website gitlink
- Publish: annotated tag and GitHub release `v0.10.17`

**Interfaces:**
- Consumes: all task checkpoints and website changes.
- Produces: one reviewed root release-candidate commit based on current `origin/main`, a published website commit, landed `origin/main`, annotated tag, verified release assets, and no installation/migration side effect from upgrading Catch.

- [ ] **Step 1: Run the stable candidate test and quality gates**

Run once after all implementation/docs edits are stable:

```bash
mise exec -- go test ./...
mise exec -- go test -race ./pkg/cli ./pkg/svc ./pkg/db ./pkg/catchrpc ./pkg/catch ./pkg/yeet
pre-commit run --all-files
mise run quality
mise run quality:goal
```

Also run every new fuzz target for at least 30 seconds. Expected: all PASS, zero race findings, zero lint/CRAP regressions, coverage/mutation goals retained.

- [ ] **Step 2: Request independent code review**

Invoke `superpowers:requesting-code-review`. Require reviewers to focus on:

- generation-local state and rollback truth;
- mount collision/TOCTOU handling and systemd escaping;
- dependency trigger negatives;
- actual-UID probe ordering before stop;
- root/network/identity/schedule cross-mutations;
- timer non-execution and rollback completeness;
- config updates only after remote success.

Resolve findings with targeted tests, amend the owning unpublished checkpoint, then rerun affected focused/race tests. Rerun the full gates only if commits changed after Step 1.

- [ ] **Step 3: Prove upgrade-negative behavior in an isolated install test**

Using test fixtures or a disposable Linux VM, start from `v0.10.16` with one legacy native service and no Bubblewrap package. Upgrade Catch to the candidate through the normal install path. Require:

```text
Bubblewrap still absent
service generation unchanged
service unit ExecStart still direct
service remains running
yeet info reports legacy
```

Then run a fresh disposable native service and require only that action to install/probe Bubblewrap and report on.

- [ ] **Step 4: Commit and push the website repository**

Inside `website`, verify the diff contains only Task 10 website files. Use raw Git only within the website repository as allowed by policy:

```bash
git -C website status --short --branch
git -C website diff --check
git -C website add docs/concepts/native-sandboxing.mdx docs/concepts/service-types.mdx docs/concepts/configuration-and-prefs.mdx docs/getting-started/host-setup.mdx docs/getting-started/service-workspace.mdx docs/getting-started/first-run-validation.mdx docs/operations/workflows.mdx docs/operations/troubleshooting.mdx docs/cli/yeet-cli.mdx docs/payloads/cron-jobs.mdx docs/changelog.mdx
git -C website commit -m "docs: publish native sandboxing"
git -C website push origin HEAD:main
```

Verify the website HEAD is advertised by its remote and the root diff shows only the intended gitlink range.

- [ ] **Step 5: Tidy the GitButler feature branch**

Run `but pull --check`; if clean and limited to this branch, `but pull`. Create `but oplog snapshot -m "before native sandbox history cleanup"`. Use `but status` IDs to squash/reword the feature checkpoints into a clean release-candidate shape while preserving the already approved design/plan history as appropriate.

Make one focused GitButler attempt to commit only the root website gitlink. If GitButler cannot materialize the pushed gitlink, use only the root policy's narrow signed gitlink exception at the final authorized publication boundary.

Verify the candidate diff contains only this feature, its tests/docs/generated files, and the published website pointer. Preserve every unrelated branch and dirty file.

- [ ] **Step 6: Land the release candidate on main**

Confirm explicit finish-and-push authorization is present in the execution task, then use the root `AGENTS.md` direct squash-to-main flow. Prefer the fast path when the branch is one commit on current `origin/main`. Record the exact candidate commit printed by `but status` in `release_commit`, then run:

```bash
git push origin "$release_commit":main
```

Treat a non-fast-forward rejection as the race check: `but pull`, rerun invalidated tests, and retry only when clean. Reconcile GitButler, update only local main if stale, and verify local `main`, `origin/main`, and `git ls-remote origin refs/heads/main` equal the landed commit. Clean only this session's integrated branch.

- [ ] **Step 7: Tag and publish `v0.10.17`**

Tag the landed commit, never the synthetic workspace commit:

```bash
release_commit=$(git rev-parse origin/main)
git tag -a v0.10.17 "$release_commit" -m "v0.10.17"
git push origin v0.10.17
```

Watch Release and Nightly workflows to completion. Require a public non-draft/non-prerelease GitHub release targeting the landed commit. Download every archive/checksum, verify checksums, archive members, executable bits, version output, and `go version -m` revision with `vcs.modified=false`.

- [ ] **Step 8: Upgrade both Catch hosts without triggering dependency or migration**

Use the extracted published `v0.10.17` client and the private aliases in `AGENTS.local.md`. Before and after each upgrade record:

- Bubblewrap package/path presence (absence may remain absent; independent preexistence may remain present);
- every native service's generation and sandbox state;
- direct/Bubblewrap `ExecStart` shape;
- status of running and timer-backed services;
- deployed Catch checksum/version/revision.

Require upgrade alone to change none of the service policy/generation/unit facts and not install Bubblewrap. Stop before live migration if either host differs.

---

### Task 12: Disposable-first live validation and one-by-one production migration

**Files:**
- No committed repository files
- Temporary local payloads under a directory created with `mktemp -d`
- Disposable services/runtime state on both configured production Catch hosts

**Interfaces:**
- Consumes: published `v0.10.17` client and Catch daemons.
- Produces: host-specific Bubblewrap evidence, complete disposable cleanup, and verified one-by-one production native-service migrations.
- Stop condition: the first failed disposable assertion or production migration ends the rollout before touching another service.

- [ ] **Step 1: Inventory without changing services**

For each configured host, collect JSON status/info for all services and build a private rollout table containing service name, type, generation, running/timer intent, identity, network mode, root, sandbox state, and exposure requirements. Exclude Catch, helpers, Docker/Compose, and VMs. Do not commit this inventory.

- [ ] **Step 2: Create disposable payloads and host bind fixtures**

Create unique generic services per host with a run-specific suffix. Use:

- a long-running shebang script that writes cwd/HOME/data/tmp results and verifies a chosen host sentinel is absent;
- an explicit-off script that requires `/etc/hostname`, to induce an activation failure/rollback when moved to on without that exposure;
- a far-future timer script that increments a counter only if executed;
- host-side RO file/dir and RW dir fixtures outside the service root, created surgically through Catch host shell access.

Record exact fixture paths/inodes before deployment so cleanup cannot target an unresolved variable or broad directory.

- [ ] **Step 3: Validate fresh default and dependency installation on both hosts**

Deploy the first disposable native service without `--sandbox`. Require:

- `yeet info` reports on;
- `/usr/bin/bwrap` is trusted and the functional probe succeeds;
- dependency installation occurs only if it was absent;
- data and private `/tmp` are writable;
- payload/runtime are not writable;
- `/root`, `/home`, `/var`, `/sys`, another service root, and host sentinel are absent;
- cwd and HOME equal service data;
- PID namespace hides host processes;
- systemd cgroup and selected host network remain inherited.

- [ ] **Step 4: Validate bind rules, remapping, reset guards, root, network, and off**

Exercise RO file, RO directory, RW directory remapped to another destination, and prove writes persist only through the RW directory. Require writable-file and overlap attempts to fail before restart.

Create an existing RO+RW list, submit an omission, and compare Catch's two printed commands. Run the preservation form, then the class-specific reset form, and verify info/config synchronization.

Run disposables with host and ISO network modes and require connectivity matches the network setting. Run `--run-as=root --sandbox=on` and prove the filesystem view remains restricted. Separately run `--sandbox=off` and prove direct host visibility without claiming it is safe isolation.

- [ ] **Step 5: Prove live rollback and timer non-execution**

For the explicit-off disposable that requires `/etc/hostname`, run:

```bash
yeet service set "$sandbox_smoke_service" --sandbox=on
```

without exposing the file. The target payload must fail, the transaction must restore the prior off generation/unit/running state, and logs/info must show the rollback. Then retry with the required RO file and succeed.

For the far-future timer, migrate off/on and change exposures. Require the timer remains enabled/waiting and its counter file never appears during migration or verification.

- [ ] **Step 6: Remove all disposable state and prove cleanup**

Remove each disposable with the normal clean-data path, then remove only the exact host fixture paths whose recorded inode still matches. Verify no service records, units, timers, network namespaces, generated roots, fixture files, or running processes remain. Keep the Bubblewrap package; package removal is not part of the service transaction or cleanup.

- [ ] **Step 7: Migrate production native services one at a time**

Using the private inventory order, for exactly one service at a time:

1. inspect logs/config and identify the minimum extra paths;
2. set `service_name` to that one inventory entry and run `yeet service set "$service_name" --sandbox=on` with only proven RO/RW exposures;
3. verify info state/generation, unit argv, status/timer intent, logs, network reachability, data writes, and workload-specific behavior;
4. sync local project config and verify canonical exposures;
5. continue only after the service is healthy.

On the first failure, stop. Leave untouched services legacy, preserve rollback evidence, and do not use a batch loop or automatic migration.

- [ ] **Step 8: Record final evidence**

Produce a private rollout report containing release/tag/workflow/artifact proof, per-host Bubblewrap probe result, disposable matrix, cleanup proof, each migrated service's before/after generation and behavior, all remaining legacy/off services, and any follow-up. Do not add private identifiers to the public repository.

---

## Plan Completion Review

- [ ] Every approved requirement has a task and test: tri-state generations, default-on fresh services, no auto migration, off escape hatch, RO file/dir, RW dir, remap, reset guard, process namespaces, inherited network/cgroup, progressive installation, transactional rollback, timer safety, config sync, info, docs, release, disposable-first rollout, and one-by-one migration.
- [ ] No task stores sandbox policy as a single current-service field; all authoritative lookup is by active generation.
- [ ] No path uses a shell to invoke Bubblewrap, apt, systemd verification, or the payload.
- [ ] No dependency trigger runs merely on Yeet/Catch upgrade, legacy/off service activity, or dormant off edits.
- [ ] Cross-feature unit rewriting preserves `HOME=data` while `WorkingDirectory=/` and updates numeric UID/GID and resolver source when their owning settings change.
- [ ] Public files contain no private host/service identity and no placeholder implementation code.
- [ ] GitButler operations preserve parallel branches and raw Git writes are limited to documented website/tag/direct-main exceptions.
