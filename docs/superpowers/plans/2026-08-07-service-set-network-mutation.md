# Service Network Mutation Through `yeet service set` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let existing non-VM services mutate the complete supported network configuration through `yeet service set`, while ordinary `yeet run` redeployments remain available but cannot change networking.

**Architecture:** Add a presence-aware network patch to the existing `service set` parser and persist a desired network configuration separately from effective runtime attachments. Catch applies the patch under the existing service-operation lock through a dedicated transaction that reuses regular and ISO lifecycle primitives; the client updates `yeet.toml` only after remote success. Existing-service `run` compares against Catch's desired state and fails with `service set` guidance before deployment when networking differs.

**Tech Stack:** Go, yargs-based CLI parsing, Catch TTY command routing, JSON-RPC service info, generated DB clone/view types, systemd, Docker Compose, Linux network namespaces, GitButler, pre-commit, mise.

## Global Constraints

- Work directly in the repository checkout; do not create or use a Git worktree.
- Use GitButler for all parent-repository version-control operations. Use normal Git only inside `website/`, as explicitly authorized, and do not push.
- Preserve unrelated branches and changes; disconnected branches are out of scope.
- Live verification is authorized only for uniquely named disposable services on the designated test host, driven from a separate local service-config checkout. Do not inspect, redeploy, upgrade, restart, or otherwise mutate any existing service.
- Do not upgrade, reinstall, restart, or reconfigure the live Catch host unless the user separately authorizes that host-wide mutation after a read-only version/capability check proves it necessary.
- Do not push, open a pull request, release, install, or deploy.
- `yeet service set` network mutation stays on the existing `manage` permission boundary; do not add a new RPC method or permission class.
- VM networking stays under `yeet vm set`.
- `yeet run` keeps network flags for initial deployment. Existing-service redeployments may proceed only when the requested network configuration is unchanged.
- The complete non-VM mutation family is `--net`, `--ts-tags`, `--ts-ver`, `--ts-exit`, `--ts-auth-key`, `--macvlan-parent`, `--macvlan-vlan`, and `--macvlan-mac`.
- `--net` replaces the entire mode set. Other supplied fields patch independently; explicit empty values clear optional persistent fields.
- A resulting mode set containing `ts` must have non-empty effective tags. Valid stored tags may be inherited.
- Tailscale auth keys are non-empty, transient, write-only inputs and must never be persisted, logged, or returned.
- Native and timer ISO networking must not require, infer, reject, or change `--run-as`, UID, GID, root, capabilities, or other privilege policy.
- ISO is a network mode, not a hostile-root containment promise.
- Keep public code, tests, docs, examples, plans, and errors free of private service and host details.
- Use `mise exec -- go ...` for Go commands and follow test-driven development: failing focused test, confirm failure, minimal implementation, confirm pass.
- Run focused tests while iterating. Run the complete suite, quality destination gate, and pre-commit only at coherent checkpoint/final boundaries.

---

## File Structure

### New focused files

- `pkg/catch/service_network_config.go` — desired network normalization, legacy derivation, patch application, and runtime option conversion.
- `pkg/catch/service_network_config_test.go` — pure desired-state and validation tests.
- `pkg/catch/service_network_mutation.go` — Catch service-set transaction planning, activation, rollback, and fail-closed orchestration.
- `pkg/catch/service_network_mutation_test.go` — transition matrix and transaction failure tests.
- `pkg/cli/service_set_network_fuzz_test.go` — bounded parser fuzzing for presence and clear semantics.

### Existing files with primary changes

- `pkg/cli/cli.go`, `pkg/cli/cli_test.go` — complete `service set` flag family, presence bits, validation, help, and VM guidance.
- `pkg/db/db.go`, `pkg/db/db_clone.go`, `pkg/db/db_view.go` — persisted desired network configuration and generated accessors.
- `pkg/catchrpc/types.go`, `pkg/catchrpc/types_test.go` — desired/effective service-info representation.
- `pkg/yeet/svc_cmd.go`, `pkg/yeet/svc_cmd_branch_test.go`, `pkg/yeet/service_sync_test.go` — local config patching, sync recovery, and remote error handling.
- `pkg/yeet/run_changes.go`, `pkg/yeet/run_changes_test.go` — reject existing-service network changes instead of redeploying them.
- `pkg/catch/tty_service_set.go`, `pkg/catch/tty_service_set_test.go` — route network patches through the existing service-set command and `manage` boundary.
- `pkg/catch/installer_file.go`, `pkg/catch/installer_file_test.go` — reusable network staging, normal-run mutation guard, initial desired-state commit, and systemd/Compose adapters.
- `pkg/catch/installer_service.go`, `pkg/catch/installer_service_test.go` — activation and payload-kind handling used by network replacement.
- `pkg/catch/iso_runtime.go`, `pkg/catch/iso_runtime_test.go`, `pkg/catch/iso_runtime_concrete_test.go` — ISO transition/tombstone/quarantine reuse for service-set mutations.
- `pkg/iso/modes.go`, `pkg/iso/modes_test.go`, `pkg/iso/iso_fuzz_test.go` — identity-independent native/timer ISO admission.
- `pkg/svc/systemd.go`, `pkg/svc/systemd_test.go` — network namespace attachment for native services and timer-triggered service units without privilege-policy changes.
- `pkg/netns/iso_topology.go`, `pkg/netns/iso_topology_test.go`, `pkg/netns/iso_integration_linux_test.go`, `pkg/netns/testdata/iso-endpoint/main.go` — direct native/timer ISO topology and functional traffic-policy coverage.
- `pkg/catch/service_info.go`, `pkg/catch/service_info_test.go`, `pkg/yeet/info_cmd.go`, `pkg/yeet/info_cmd_test.go` — desired/effective status and redaction.
- `README.md`, `pkg/cli/cli.go`, `website/docs/concepts/networking.mdx`, `website/docs/concepts/tailscale.mdx`, `website/docs/cli/yeet-cli.mdx`, `website/docs/payloads/containers.mdx`, `website/docs/payloads/binaries.mdx`, and `website/docs/concepts/service-types.mdx` — user-facing command contract.
- `tools/test-iso-network.sh` — optional non-live privileged namespace verification for disposable local Linux environments.

### Obsolete uncommitted documents to remove

- `docs/superpowers/specs/2026-08-06-safe-service-network-mutation-design.md`
- `docs/superpowers/plans/2026-08-06-safe-service-network-mutation.md`

The approved specification is `docs/superpowers/specs/2026-08-07-service-set-network-mutation-design.md` at GitButler commit `f85754c2`.

---

### Task 1: Presence-Aware `service set` Network Flags

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Create: `pkg/cli/service_set_network_fuzz_test.go`

**Interfaces:**
- Consumes: existing `ParseServiceSet(args []string) (ServiceSetFlags, []string, error)`, `orderedFlagValues`, `longFlagWasSupplied`, `validateNetworkModesNotEmpty`, and macvlan validators.
- Produces: the expanded `cli.ServiceSetFlags` and `func (f ServiceSetFlags) HasNetworkChange() bool`, used by the client and Catch tasks.

- [ ] **Step 1: Write failing table tests for every set, patch, and clear form**

Add cases to `TestParseServiceSetFlags` and a focused `TestParseServiceSetNetworkFlags` that assert this public shape:

```go
type ServiceSetFlags struct {
    // existing fields remain unchanged
    Net               string
    NetSet            bool
    TsVer             string
    TsVerSet          bool
    TsExit            string
    TsExitSet         bool
    TsTags            []string
    TsTagsSet         bool
    TsAuthKey         string
    TsAuthKeySet      bool
    MacvlanMac        string
    MacvlanMacSet     bool
    MacvlanVlan       int
    MacvlanVlanSet    bool
    MacvlanParent     string
    MacvlanParentSet  bool
}
```

Cover `--net=iso`, repeated `--ts-tags`, `--ts-tags=`, `--ts-ver=`,
`--ts-exit=`, `--macvlan-parent=`, `--macvlan-vlan=`, and
`--macvlan-mac=`. Assert omitted and empty values differ. Assert
`--net=` and `--ts-auth-key=` fail. Assert service-set network flags are no
longer rejected as VM-only parser flags, while `--vcpus` still points to
`yeet vm set`.

Retain the existing `ParseVMSet` cases for `--net=svc`, `--net=lan`,
`--net=svc,lan`, and `--net=iso`; this task must not move or duplicate VM
network parsing under `service set`.

- [ ] **Step 2: Run the parser tests and confirm the intended failure**

Run:

```bash
mise exec -- go test ./pkg/cli -run 'TestParseServiceSet(Network|Flags)|TestServiceCommandRegistry' -count=1
```

Expected: FAIL because `ServiceSetFlags` lacks presence-aware network fields and `rejectServiceSetVMFlags` still rejects network options.

- [ ] **Step 3: Implement the parser with explicit presence tracking**

Extend `serviceSetFlagsParsed` with string-backed values where empty must be distinguishable, including a string representation for `--macvlan-vlan`. Build final fields from raw `parseArgs`; do not infer presence from a zero value. Implement:

```go
func (f ServiceSetFlags) HasNetworkChange() bool {
    return f.NetSet || f.TsVerSet || f.TsExitSet || f.TsTagsSet ||
        f.TsAuthKeySet || f.MacvlanMacSet || f.MacvlanVlanSet ||
        f.MacvlanParentSet
}
```

`--macvlan-vlan=` yields `MacvlanVlanSet=true` and value `0`; non-empty
values use the existing VLAN range validation. Normalize modes and tags while
preserving a supplied empty tag list. Reject empty `--net` and auth keys.

- [ ] **Step 4: Update service-set help and registry tests**

Add the complete network family to the existing `service set` usage and
examples. Include `yeet service set <svc> --net=iso` and
`yeet service set <svc> --net=ts --ts-tags=tag:app`. Keep VM examples under
`vm set`.

- [ ] **Step 5: Add parser fuzz invariants**

Seed `FuzzParseServiceSetNetwork` with set, clear, repeated-tag, malformed
mode, empty auth key, and VLAN inputs. For every successful parse, reparse a
canonical argument list and assert the value plus every presence bit is
stable. The fuzz target must never log an auth-key value.

- [ ] **Step 6: Run focused parser tests and a short fuzz pass**

```bash
mise exec -- go test ./pkg/cli -run 'TestParseServiceSet|TestServiceCommandRegistry' -count=1
mise exec -- go test ./pkg/cli -run '^$' -fuzz '^FuzzParseServiceSetNetwork$' -fuzztime=5s
```

Expected: PASS.

- [ ] **Step 7: Create a focused GitButler checkpoint**

Run `but diff`, select only the three Task 1 files by their dynamic IDs, and
commit them to `codex/network-config-mutation` with message
`cli: parse service network mutations`. Never omit `--changes`; unrelated
uncommitted files must remain under `zz`.

---

### Task 2: Persist Desired Network Configuration and Expose It Through Service Info

**Files:**
- Modify: `pkg/db/db.go`
- Regenerate: `pkg/db/db_clone.go`
- Regenerate: `pkg/db/db_view.go`
- Create: `pkg/catch/service_network_config.go`
- Create: `pkg/catch/service_network_config_test.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`

**Interfaces:**
- Consumes: `cli.ServiceSetFlags.HasNetworkChange`, `iso.NormalizeModes`, existing runtime `SvcNetwork`, `MacvlanNetwork`, `TailscaleNetwork`, and `ISOAllocation` records.
- Produces: `db.Service.Network *ServiceNetworkConfig`, pure patch/derivation helpers, and `catchrpc.ServiceNetwork.Desired` for Tasks 3–6.

- [ ] **Step 1: Write failing DB round-trip and pure patch tests**

Define the desired persistence type in the tests before implementation:

```go
type ServiceNetworkConfig struct {
    Modes          []string `json:",omitempty"`
    TSVersion      string   `json:",omitempty"`
    TSExitNode     string   `json:",omitempty"`
    TSTags         []string `json:",omitempty"`
    MacvlanParent  string   `json:",omitempty"`
    MacvlanVLAN    int      `json:",omitempty"`
    MacvlanMAC     string   `json:",omitempty"`
}
```

Add table tests for:

- deriving host, `svc`, `lan`, `ts`, and ISO desired state from legacy runtime records;
- `--net` replacing modes while inactive Tailscale/macvlan settings remain;
- individual fields setting and clearing;
- tags being required whenever the resulting modes include `ts`;
- valid stored tags being inherited when entering `ts`;
- auth keys never appearing in the result; and
- network normalization being deterministic and idempotent.

- [ ] **Step 2: Run focused tests and confirm missing types/helpers fail**

```bash
mise exec -- go test ./pkg/db ./pkg/catch ./pkg/catchrpc -run 'Test(ServiceNetworkConfig|ApplyServiceNetworkPatch|LegacyServiceNetwork|ServiceNetworkDesired)' -count=1
```

Expected: FAIL because the desired record and helpers do not exist.

- [ ] **Step 3: Add the desired DB record and generate accessors**

Add this field to `db.Service` and include `ServiceNetworkConfig` in both
generator type lists:

```go
Network *ServiceNetworkConfig `json:",omitempty"`
```

Run:

```bash
mise exec -- go generate ./pkg/db
```

Verify generated clone/view code deep-copies `Modes` and `TSTags`.

- [ ] **Step 4: Implement pure desired-state helpers**

In `pkg/catch/service_network_config.go`, implement these exact boundaries:

```go
func desiredServiceNetworkConfig(sv db.ServiceView) db.ServiceNetworkConfig
func effectiveServiceNetworkConfig(sv db.ServiceView) db.ServiceNetworkConfig
func applyServiceNetworkPatch(current db.ServiceNetworkConfig, flags cli.ServiceSetFlags) (db.ServiceNetworkConfig, error)
func normalizeServiceNetworkConfig(cfg db.ServiceNetworkConfig) (db.ServiceNetworkConfig, error)
func networkOptsFromDesired(cfg db.ServiceNetworkConfig, authKey string) NetworkOpts
```

`desiredServiceNetworkConfig` returns persisted desired state when present and
otherwise derives it without writing. `applyServiceNetworkPatch` uses only
presence bits. Validation checks modes, payload-independent combinations, VLAN,
macvlan/LAN relationships, and the required Tailscale tags.

- [ ] **Step 5: Add desired/effective RPC representation**

Add a non-secret type:

```go
type ServiceNetworkSettings struct {
    Modes          []string `json:"modes,omitempty"`
    TSVersion      string   `json:"tsVersion,omitempty"`
    TSExitNode     string   `json:"tsExitNode,omitempty"`
    TSTags         []string `json:"tsTags,omitempty"`
    MacvlanParent  string   `json:"macvlanParent,omitempty"`
    MacvlanVLAN    int      `json:"macvlanVlan,omitempty"`
    MacvlanMAC     string   `json:"macvlanMac,omitempty"`
}
```

Add `Desired *ServiceNetworkSettings `json:"desired,omitempty"`` to
`catchrpc.ServiceNetwork`. Keep the existing fields as effective/runtime
state for backward compatibility. Update custom JSON marshal helpers and
round-trip tests.

- [ ] **Step 6: Populate service info without mutating legacy services**

`serviceNetworkInfo` must set `Desired` from `desiredServiceNetworkConfig`,
keep `Modes` as effective modes, and omit secrets. Add a test that captures DB
JSON before and after info generation and proves the read did not write a lazy
migration.

- [ ] **Step 7: Run focused tests and generated-code checks**

```bash
mise exec -- go test ./pkg/db ./pkg/catchrpc ./pkg/catch -run 'Test(ServiceNetwork|ApplyServiceNetworkPatch|LegacyServiceNetwork)' -count=1
mise exec -- gofmt -w pkg/catch/service_network_config.go pkg/catch/service_network_config_test.go pkg/catchrpc/types.go pkg/catchrpc/types_test.go pkg/db/db.go
mise exec -- go test ./pkg/db ./pkg/catchrpc ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 8: Create a focused GitButler checkpoint**

Use `but diff` and commit only the Task 2 files to
`codex/network-config-mutation` with message
`catch: persist desired service networks`.

---

### Task 3: Client Config Synchronization and Existing-Run Rejection

**Files:**
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`
- Modify: `pkg/yeet/service_sync_test.go`
- Modify: `pkg/yeet/run_changes.go`
- Modify: `pkg/yeet/run_changes_test.go`
- Modify: `pkg/yeet/test_main_test.go` only if new injected RPC seams need reset.

**Interfaces:**
- Consumes: expanded `cli.ServiceSetFlags` and `catchrpc.ServiceNetwork.Desired`.
- Produces: deterministic `yeet.toml` rewriting, `service sync` recovery, and `rejectExistingRunNetworkChange` used before a redeployment invokes its runner.

- [ ] **Step 1: Write failing local-config patch tests**

Add table tests that begin with payload flags and application arguments, apply
one network patch, and assert only matching network flags change. Required
cases: replace modes, replace repeated tags, clear each optional field, retain
inactive mode settings, and never save `--ts-auth-key`.

Specify this helper:

```go
func serviceSetNetworkRunFlagChanges(flags cli.ServiceSetFlags) (map[string]bool, []runFlagUpdate)
```

Use `rewriteStoredRunArgs` so payload arguments after `--` remain byte-for-byte
equivalent after normalization.

- [ ] **Step 2: Write failing `service sync` tests**

Add tests where Catch reports `Network.Desired` and the local entry is missing
or stale. Assert sync writes the complete persistent network family, removes
obsolete occurrences, and does not invent or persist an auth key. Preserve the
legacy fallback when older Catch omits `Desired`.

- [ ] **Step 3: Write failing redeployment-guard tests**

Replace the prior tests that expected network changes to trigger deployment.
Assert all persistent differences return an error containing:

```text
network changes for existing services require `yeet service set <service> ...`
```

The runner must not be called for changes to modes, tags, version, exit node,
or macvlan fields. An explicit existing-service `--ts-auth-key` also fails.
Unchanged networking plus payload/env/image/argument changes must still call
the normal deployment path. Service-not-found remains an initial deploy.

Add a separate regression proving `yeet vm set <vm> --net=lan` continues to
route through the VM command and update the stored VM network flags. The
non-VM run guard must not intercept or replace the VM mutation workflow.

- [ ] **Step 4: Run focused client tests and confirm failures**

```bash
mise exec -- go test ./pkg/yeet -run 'Test(ServiceSet.*Network|ServiceSync.*Network|Run.*Network)' -count=1
```

Expected: FAIL because config rewriting covers no network fields and
`detectRemoteNetworkChange` still marks a deployment change.

- [ ] **Step 5: Implement config rewrite and partial-success errors**

Call `serviceSetNetworkRunFlagChanges` from `applyServiceSetConfigFlags`.
When remote mutation succeeds but saving fails, return an error that states the
remote network changed and includes the exact recovery command
`yeet service sync <service>` with the existing host/config qualifiers. Keep
existing identity-specific messaging only for identity-only mutations.

- [ ] **Step 6: Replace network redeployment with rejection**

Replace `runChangeSummary.networkChanged` and `detectRemoteNetworkChange` with:

```go
func rejectExistingRunNetworkChange(ctx context.Context, entry ServiceEntry, runArgs []string) error
func requestedRunNetworkSettings(runArgs []string) (catchrpc.ServiceNetworkSettings, bool, error)
func authoritativeRunNetworkSettings(network catchrpc.ServiceNetwork) catchrpc.ServiceNetworkSettings
```

Call the rejection before payload execution. Compare normalized persistent
fields against `Network.Desired`, falling back to effective fields for an older
Catch. Treat any service-info error as fatal. Preserve initial-deploy behavior
when `Found` is false.

- [ ] **Step 7: Implement network-aware `service sync`**

Convert desired RPC settings to canonical stored run flags using the same
rewrite helper as `service set`. If `Desired` is absent, derive from effective
RPC fields. Never manufacture an auth-key flag.

- [ ] **Step 8: Run focused and package tests**

```bash
mise exec -- go test ./pkg/yeet -run 'Test(ServiceSet|ServiceSync|Run.*Network)' -count=1
mise exec -- go test ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 9: Create a focused GitButler checkpoint**

Use dynamic IDs from `but diff`; commit only Task 3 files with message
`yeet: route network changes through service set`.

---

### Task 4: Catch Service-Set Transaction and Regular Network Transitions

**Files:**
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_service_set_test.go`
- Create: `pkg/catch/service_network_mutation.go`
- Create: `pkg/catch/service_network_mutation_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/installer_service.go`
- Modify: `pkg/catch/installer_service_test.go`

**Interfaces:**
- Consumes: Task 2 desired-state helpers, existing service-operation lock, installer generation/artifact helpers, and existing exhaustive fail-closed unit stopping.
- Produces: `Server.updateServiceNetworkLocked`, a pure transaction runner, initial desired-state persistence, and an authoritative Catch-side run guard.

- [ ] **Step 1: Write failing command-routing and permission tests**

Extend `serviceSetChanges` with `network bool` sourced from
`flags.HasNetworkChange()`. Assert network-only service set is accepted under
the existing `manage` route, a missing service fails before staging, and VM
services receive `use yeet vm set` guidance.

Add explicit tests that root and non-root native services reach the same
network mutation callback and that no identity resolver is invoked.

- [ ] **Step 2: Write the pure transaction failure matrix first**

Define:

```go
type serviceNetworkMutationSteps interface {
    Stage(context.Context) error
    StopPrevious(context.Context) error
    Activate(context.Context) error
    Verify(context.Context) error
    Commit(context.Context) error
    Restore(context.Context) error
    FailClosed(context.Context) error
}

func runServiceNetworkMutation(ctx context.Context, steps serviceNetworkMutationSteps) error
```

Tests must prove validation/staging failure never stops the old runtime;
activation or verification failure restores it; commit failure restores it;
and restore failure invokes `FailClosed` and joins all errors.

- [ ] **Step 3: Run focused tests and confirm missing orchestration fails**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ServiceSetNetwork|RunServiceNetworkMutation)' -count=1
```

Expected: FAIL because network changes are absent from service-set orchestration.

- [ ] **Step 4: Implement service-set planning under the existing lock**

Add:

```go
func (s *Server) updateServiceNetworkLocked(ctx context.Context, name string, flags cli.ServiceSetFlags, out io.Writer) error
func (s *Server) planServiceNetworkMutation(ctx context.Context, name string, flags cli.ServiceSetFlags) (*serviceNetworkMutationPlan, error)
```

The plan snapshots the current service/generation/runtime intent, derives
current desired settings, applies and validates the patch, returns a no-op for
equal normalized state, and prepares `NetworkOpts` including the transient auth
key. It must not write DB state or stop units during planning.

Call this from `serviceSetCmdFunc` while its operation lock is held. Permit an
explicit `--run-as` plus network mutation by producing one replacement native
unit and using the existing identity migration rollback machinery; network
validation must not inspect that identity. Reject any other combination whose
existing mutation path cannot participate atomically, with an error directing
the operator to issue separate `service set` commands.

- [ ] **Step 5: Extract reusable staging from `FileInstaller`**

Move transaction-neutral network replacement logic out of the upload path into
the new mutation file or small installer helpers. A service-set mutation must
reuse the current generation's installed payload and artifacts; it must not
receive, hash, replace, or increment payload content merely to change network.
Keep explicit-host unit rendering fresh so an old `NetworkNamespacePath` cannot
survive.

- [ ] **Step 6: Implement regular-to-regular activation and rollback**

Adapt the existing `runRegularNetworkMutation`,
`restoreExistingRegularService`, and
`stopRegularMutationUnitsFailClosed` behavior to the dedicated transaction.
Tests must cover host↔`svc`, host↔`lan`, host↔`ts`, `svc,ts`↔host,
Tailscale tag/version/exit changes, stable compatible service/Tailscale/macvlan
identity, stale pointer removal, and no-op behavior.

On final fail-closed stop, unconditionally attempt every deduplicated current
and prior primary, timer, namespace, and Tailscale unit; aggregate stop errors;
then perform a separate full active-state verification pass.

- [ ] **Step 7: Persist desired state on successful initial install and guard normal redeploys**

Initial installation writes `Service.Network` only after successful activation.
For existing services, normal `FileInstaller` preparation compares the incoming
persistent network settings with `desiredServiceNetworkConfig`. A difference
returns the same `yeet service set` guidance before staging. The internal
service-set transaction uses its dedicated path rather than a boolean bypass
accepted from RPC input.

- [ ] **Step 8: Run focused Catch tests and race the transaction seam**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ServiceSetNetwork|RunServiceNetworkMutation|RegularNetworkMutation|ExistingRunNetwork)' -count=1
mise exec -- go test -race ./pkg/catch -run 'TestRunServiceNetworkMutation|TestServiceSetNetwork' -count=1
```

Expected: PASS.

- [ ] **Step 9: Create a focused GitButler checkpoint**

Use `but diff` dynamic IDs and commit only Task 4 files with message
`catch: mutate regular service networks transactionally`.

---

### Task 5: ISO Transitions for Native, Timer, and Compose Services

**Files:**
- Modify: `pkg/iso/modes.go`
- Modify: `pkg/iso/modes_test.go`
- Modify: `pkg/iso/iso_fuzz_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/installer_service.go`
- Modify: `pkg/catch/installer_service_test.go`
- Modify: `pkg/catch/iso_allocator.go`
- Modify: `pkg/catch/iso_allocator_test.go`
- Modify: `pkg/catch/iso_runtime.go`
- Modify: `pkg/catch/iso_runtime_test.go`
- Modify: `pkg/catch/iso_runtime_concrete_test.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/svc/systemd_test.go`
- Modify: `pkg/netns/iso_topology.go`
- Modify: `pkg/netns/iso_topology_test.go`
- Modify: `pkg/netns/iso_integration_linux_test.go`
- Modify: `pkg/netns/testdata/iso-endpoint/main.go`
- Modify: `tools/test-iso-network.sh`

**Interfaces:**
- Consumes: Task 4 mutation transaction and existing `transitionFromISO`, allocation, tombstone, quarantine, and reconciliation interfaces.
- Produces: identity-independent native/timer `iso` admission and service-set regular↔ISO/ISO↔ISO transitions.

- [ ] **Step 1: Rewrite admission tests before implementation**

Delete expectations for explicit non-root identity, root rejection, and timer
rejection. Add table cases proving native binary, script, root, non-root, and
timer payloads accept `iso`. Retain networking-based rejections: host mixed
with another mode, ISO with `svc`/`lan`, ISO with published ports, and native
or timer `iso,ts` until that topology exists.

Simplify `iso.NetworkRequest` by removing `NativeIdentity`; use only payload,
modes, and published-port facts.

- [ ] **Step 2: Run ISO tests and confirm old identity gates fail**

```bash
mise exec -- go test ./pkg/iso ./pkg/catch -run 'Test.*ISO.*(Native|Timer|Network)' -count=1
```

Expected: FAIL on the existing non-root and cron admission checks.

- [ ] **Step 3: Make ISO validation depend only on network topology**

Implement native and timer validation as ISO-only mode validation. Remove UID
resolution from `normalizeNetworkForServiceType`, installer admission, runtime
reconciliation, and allocator checks. Do not remove or change the independent
service identity feature itself.

Update fuzz seeds and invariants so root/native/timer ISO inputs are accepted;
the fuzz target should assert only topology and published-port invariants.

- [ ] **Step 4: Render native and timer namespace attachment without privilege changes**

Keep `NetworkNamespacePath=`, the ISO gate dependency, public-only resolver,
and activation order on the generated `.service`. Do not add or force `User=`,
`Group=`, `NoNewPrivileges`, capability sets, `RestrictNamespaces`, filesystem
sandboxing, or address-family restrictions as part of networking.

For timers, attach networking to the invoked `.service`; keep `.timer` as the
scheduling unit. Mutation restarts/re-enables the timer according to prior
intent without running the job as a verification side effect.

- [ ] **Step 5: Reuse ISO lifecycle for every transition direction**

Connect the Task 4 plan to:

- regular→ISO: reserve/stage/verify the boundary before starting workload;
- ISO→ISO: preserve compatible stable allocation, revalidate, and activate;
- ISO→regular/host: stop workload, clean and verify absence, commit replacement,
  then start; and
- any failure: restore the prior runtime when possible, otherwise leave the
  service stopped/quarantined with tombstone state until cleanup is proven.

Compose continues through its project/router path. Native and timer endpoints
use one direct `/30` and no Docker project bridge.

- [ ] **Step 6: Replace hostile-root tests with functional network-policy tests**

The privileged helper should prove ordinary traffic behavior: public IPv4
egress succeeds, while Catch/private-host, service-network, gateway service,
and other-ISO endpoints are denied. Remove assertions that a hostile root
process cannot call `setns`, mutate links/routes, or escape the namespace.

Privileged tests must skip explicitly unless running as Linux root with the
required tools. They must use disposable namespaces only and never target a
live Yeet service.

- [ ] **Step 7: Run focused, fuzz, compile, and race checks**

```bash
mise exec -- go test ./pkg/iso ./pkg/svc ./pkg/netns ./pkg/catch -run 'Test.*(ISO|NetworkMutation|Systemd)' -count=1
mise exec -- go test ./pkg/iso -run '^$' -fuzz '^FuzzValidateNetwork$' -fuzztime=5s
mise exec -- go test -race ./pkg/catch ./pkg/netns -run 'Test.*(ISO|NetworkMutation)' -count=1
GOOS=linux GOARCH=amd64 mise exec -- go test -c ./pkg/netns -o /tmp/yeet-netns.test
GOOS=linux GOARCH=amd64 mise exec -- go build -o /tmp/yeet-iso-endpoint ./pkg/netns/testdata/iso-endpoint
```

Expected: focused/fuzz/race tests PASS; Linux binaries compile. Do not execute
the Linux integration binary on macOS and do not install missing tools.

- [ ] **Step 8: Create a focused GitButler checkpoint**

Commit only Task 5 files using dynamic `but diff` IDs with message
`catch: support identity-independent native ISO networking`.

---

### Task 6: Status, Documentation, Website Commit, and Final Verification

**Files:**
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`
- Modify: `README.md`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/concepts/tailscale.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/payloads/containers.mdx`
- Modify: `website/docs/payloads/binaries.mdx`
- Modify: `website/docs/concepts/service-types.mdx`
- Delete: `docs/superpowers/specs/2026-08-06-safe-service-network-mutation-design.md`
- Delete: `docs/superpowers/plans/2026-08-06-safe-service-network-mutation.md`

**Interfaces:**
- Consumes: desired/effective RPC state and completed mutation behavior.
- Produces: accurate operator output, public documentation, a local website commit, a clean parent GitButler history, and final verification evidence.

- [ ] **Step 1: Write failing info-output and redaction tests**

Plain output must always show effective modes, including `host`. If desired and
effective differ, print both plus ISO lifecycle/error state. JSON must expose
the non-secret desired settings. Seed an auth key in command input and assert
it is absent from errors, previews, RPC JSON, info output, and saved config.

- [ ] **Step 2: Run focused info tests and confirm failures**

```bash
mise exec -- go test ./pkg/catch ./pkg/catchrpc ./pkg/yeet -run 'Test.*(Network.*Info|Info.*Network|AuthKey.*Redact)' -count=1
```

- [ ] **Step 3: Implement concise desired/effective rendering**

Keep existing effective `Network modes` output for the healthy common case.
Add `Desired network modes` only when it differs. Render Tailscale version,
exit node, tags, and macvlan values without interfaces/stable IDs being mistaken
for desired configuration. Reuse existing auth-key redaction helpers.

- [ ] **Step 4: Update README, CLI help, and website manual together**

Replace every claim that rerunning `yeet run` mutates networking. Document:

```bash
yeet service set <svc> --net=iso
yeet service set <svc> --net=ts --ts-tags=tag:app
yeet service set <svc> --net=host
yeet service set <svc> --ts-exit=
```

State patch/clear semantics, required Tailscale tags, `vm set` separation,
immediate restart, `service sync` recovery, and the existing-run error. Describe
native/timer ISO as networking without a root-containment claim. Keep all
examples generic and use `yeetrun.com`.

Include a direct VM contrast:

```bash
yeet vm set <vm> --net=lan
yeet vm set <vm> --net=svc,lan --macvlan-parent=vmbr0
```

State that `yeet service set` network flags are for non-VM services and that
all existing VM network changes remain under `yeet vm set`.

- [ ] **Step 5: Remove obsolete run-based planning documents**

Delete only the two listed uncommitted 2026-08-06 documents. Preserve the
approved 2026-08-07 spec and this plan.

- [ ] **Step 6: Run documentation/help and touched-package tests**

```bash
mise exec -- go test ./cmd/yeet ./pkg/cli ./pkg/catchrpc ./pkg/yeet ./pkg/catch ./pkg/iso ./pkg/svc ./pkg/netns -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit website changes locally without pushing**

Verify `git -C website status --short --branch` contains only this task's docs.
Use normal Git only inside `website/`, as authorized:

```bash
git -C website add docs/concepts/networking.mdx docs/concepts/tailscale.mdx docs/cli/yeet-cli.mdx docs/payloads/containers.mdx docs/payloads/binaries.mdx docs/concepts/service-types.mdx
git -C website commit -m "docs: explain service network mutation"
```

Do not push. Record the resulting website commit and verify the parent diff
shows only that intended gitlink movement.

- [ ] **Step 8: Run the final verification boundary once on the stable candidate**

```bash
mise exec -- go test ./... -count=1
mise run quality:goal
mise exec -- pre-commit run --all-files
```

Also run the bounded parser/network fuzz targets for at least five seconds and
the focused Catch/netns race tests from Tasks 4–5 if `quality:goal` does not
already exercise the exact targets. Every command must pass before the final
implementation commit. Do not refresh baselines to hide findings.

- [ ] **Step 9: Smoke-test disposable services on the designated live host and clean up**

After the stable candidate passes the local verification boundary, build and
use the candidate `yeet` client from the separate service-config checkout with
an explicit disposable-test target. First perform read-only version/capability checks.
If testing requires a host-wide Catch upgrade or restart, stop and request
separate authorization before changing the host.

Use unique disposable service and directory names. Exercise one Compose/image
service through reasonable regular and ISO transitions, including returning to
host networking. Exercise one native binary through host → ISO → host. Verify
status/info desired and effective state plus actual reachability/isolation after
each transition. Confirm that a `yeet run` redeployment with changed networking
fails with `yeet service set` guidance while an unrelated redeployment remains
allowed. Exercise Tailscale only when safe credentials and required tags are
already available; never print or persist an auth key in evidence.

On both success and failure, remove the disposable services with their data and
config, remove their local directories, and audit Catch plus local state for the
unique names. Do not touch any pre-existing service. Record exact
commands and redacted results in the Task 6 report.

- [ ] **Step 10: Request code review and address only verified findings**

Use `superpowers:requesting-code-review` against the approved spec and this
plan. Review specifically for command-boundary correctness, full flag-family
coverage, rollback/fail-closed behavior, ISO lifecycle reuse, privilege-policy
decoupling, auth-key leakage, and accidental live/private references. Apply
review feedback through focused red/green tests and rerun only invalidated
checks, then rerun the final required gate if code changed.

- [ ] **Step 11: Tidy and commit the completed implementation with GitButler**

Create `but oplog snapshot -m "before network mutation history cleanup"` before
large history edits. Use GitButler to absorb/squash the implementation
checkpoints into a clean final shape while keeping the approved design commit
separate. Use dynamic file IDs to include only this feature and the intended
website gitlink. Suggested implementation message:

```text
service: mutate network configuration through service set
```

Do not include unrelated branches or changes. Do not push. Report separately:
the design commit, implementation commit(s), local-only website commit, hook
and test evidence, the disposable live-smoke matrix and cleanup proof,
confirmation that no existing live service or Catch host was changed, and the
fact that nothing was pushed, released, or installed.
