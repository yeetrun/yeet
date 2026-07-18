# Firecracker Jailer Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Launch new Yeet Firecracker VMMs through the matching Firecracker jailer as the unprivileged `yeet-vm` host identity, support explicit one-VM migration, and migrate the live VMs on `yeet-lab` safely.

**Architecture:** Root Catch remains the VM supervisor and prepares one transient chroot per VM under `<data-root>/vm-jailer`. It projects only the VM's canonical kernel, disk, checkpoint, API, and vsock paths, then runs jailer in the foreground; a root-owned marker under the service root persists `jailer` mode without changing the shared VM database schema.

**Tech Stack:** Go, Firecracker v1.14.3 jailer, systemd, Linux mount/network namespaces, TAP, KVM, ZFS ZVOLs, Unix sockets, GitButler.

## Global Constraints

- VMs remain excluded from native workload `--run-as`; use the distinct static `yeet-vm` identity.
- Existing VMs remain `legacy-root` until explicit stopped-VM migration through `yeet vm set <vm> --vmm-isolation=jailer`.
- New VM manifests may include a same-version `jailer`; old manifests remain readable but cannot create or migrate a jailed VM.
- Fresh Catch storage defaults to `/var/lib/yeet`, but every path is derived from configured data and service roots; custom roots and ZFS remain supported.
- The VM systemd unit, network helpers, storage operations, snapshots, restore, console supervisor, and guest-kernel synchronization remain root.
- Jailer runs in the foreground with cgroup v2, no daemonization, no new PID namespace, and no dedicated network namespace in v1.
- Catch must not silently fall back to a root Firecracker process after jailer mode is selected.
- `vm set` remains a `manage` operation; status and info remain `read` operations.
- Do not commit or deploy unrelated ISO-network or native-run-as workspace changes.

---

### Task 1: Isolation Mode and Host Identity

**Files:**
- Create: `pkg/catch/vm_isolation.go`
- Create: `pkg/catch/vm_isolation_test.go`

**Interfaces:**
- Produces: `vmIsolationModeForRoot(string) (string, error)`, `writeVMIsolationMode(string, string) error`, `ensureVMRuntimeIdentity() (vmRuntimeIdentity, error)`, and `vmJailerID(string) string`.
- Consumes: `serviceRunDirForRoot` and `writeVMFile`.

- [ ] **Step 1: Write failing marker, ID, and identity tests**

Cover absent marker as `legacy-root`, a `jailer` round trip, invalid marker
rejection, removal on `legacy-root`, stable jailer IDs restricted to
`[A-Za-z0-9-]` and 64 bytes, existing-account lookup, and one `useradd` call
when lookup reports an unknown user.

The wished-for public shape is:

```go
const (
	vmIsolationLegacy = "legacy-root"
	vmIsolationJailer = "jailer"
	vmRuntimeUser      = "yeet-vm"
)

type vmRuntimeIdentity struct {
	UID int
	GID int
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestVM(Isolation|RuntimeIdentity|JailerID)' -count=1
```

Expected: compile failure because the new constants and helpers do not exist.

- [ ] **Step 3: Implement marker and identity helpers**

Use `<service-root>/run/vmm-isolation`, atomic `writeVMFile`, and removal for
legacy mode. Wrap `user.Lookup` and `exec.Command` in package variables so
tests do not mutate host accounts. Create the account with:

```text
useradd --system --no-create-home --shell /usr/sbin/nologin --user-group yeet-vm
```

Reject UID/GID zero and negative or unparsable IDs. Hash long/unsafe service
names into a stable jailer-compatible suffix.

- [ ] **Step 4: Run focused tests and confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'TestVM(Isolation|RuntimeIdentity|JailerID)' -count=1
```

Expected: PASS.

### Task 2: Paired Jailer Image Artifact

**Files:**
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `pkg/catch/vm_images_local.go`
- Modify: `pkg/catch/vm_images_local_test.go`
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`

**Interfaces:**
- Produces: `vmImageManifest.Jailer`, `vmImagePaths.JailerPath`, and `vmImageAsset.RequireJailer() (string, error)`.
- Consumes: existing manifest checksum and artifact-fetching machinery.

- [ ] **Step 1: Add failing manifest tests**

Test that a manifest with `jailer` downloads and verifies both executables,
that `artifactNames()` includes it, that an old manifest without it still
validates, and that `RequireJailer` returns actionable guidance for an old
bundle.

- [ ] **Step 2: Confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMImage.*Jailer|TestLocalVMImage.*Jailer' -count=1
```

Expected: compile failure for the new fields/helper.

- [ ] **Step 3: Implement additive manifest support**

Add:

```go
Jailer string `json:"jailer,omitempty"`
```

to the manifest, include it in checksum validation only when non-empty, fetch
it beside Firecracker, chmod both `0755`, and return `JailerPath`. Local bundle
installation copies and hashes an optional `jailer` file. `RequireJailer`
checks a non-empty existing executable path and does not invent a system-wide
fallback.

Update the Ubuntu builder to install the release tarball's matching jailer as
`jailer`, add it to `manifest.json`, `checksums`, and `checksums.txt`.

- [ ] **Step 4: Confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMImage.*Jailer|TestLocalVMImage.*Jailer' -count=1
bash -n tools/vm-image/build-ubuntu-26.04.sh
```

Expected: PASS.

### Task 3: TAP Delegation

**Files:**
- Modify: `pkg/catch/vm_network.go`
- Modify: `pkg/catch/vm_network_test.go`
- Modify: `pkg/catch/vm_network_reconcile.go`
- Modify: `pkg/catch/vm_network_reconcile_test.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_resize.go`

**Interfaces:**
- Produces: `vmNetworkPlan.WithTapOwner(user, group string) vmNetworkPlan`.
- Consumes: `ensureVMRuntimeIdentity` before any plan with an owner is executed.

- [ ] **Step 1: Write failing command-planner tests**

For service, LAN bridge, and generated VLAN bridge plans, assert the TAP
command is exactly:

```text
ip tuntap add <tap> mode tap user yeet-vm group yeet-vm
```

when an owner is set and remains the old command when it is not.

- [ ] **Step 2: Confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMNetwork.*TapOwner' -count=1
```

Expected: compile failure because `WithTapOwner` does not exist.

- [ ] **Step 3: Implement optional TAP owner and callers**

Store owner names on `vmNetworkPlan` without persisting them in DB. Append
`user` and `group` to every `ip tuntap add` command. New VM provisioning uses
the owner. Isolation migration marks the unchanged network as changed so the
old TAP is removed and recreated. Reconciliation ensures the account before
emitting owned setup commands.

- [ ] **Step 4: Confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMNetwork.*TapOwner|TestEnsureVMNetwork' -count=1
```

Expected: PASS.

### Task 4: Jail Resource Planner and Trusted Runtime Validation

**Files:**
- Create: `pkg/catch/vm_jailer.go`
- Create: `pkg/catch/vm_jailer_test.go`

**Interfaces:**
- Produces: `prepareVMJail(VMConsoleProxyConfig, vmRuntimeIdentity, *vmFullRestoreRequest) (*preparedVMJail, error)` and `(*preparedVMJail).Command(context.Context, bool) *exec.Cmd`, `Cleanup() error`.
- Consumes: canonical Firecracker config, service root, disk path, API socket, and optional restore paths.

- [ ] **Step 1: Write failing pure-planner tests**

Test jail-root construction, identical absolute in-jail paths, jailer argv,
config parsing, raw-disk and ZVOL resource classification, socket-link targets,
checkpoint base projection, path traversal rejection, symlink rejection,
root-owned executable validation, mismatched versions, and deepest-first stale
mount cleanup parsing.

- [ ] **Step 2: Confirm RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMJail' -count=1
```

Expected: compile failure for `prepareVMJail` and planner types.

- [ ] **Step 3: Implement the planner and side-effect boundary**

Use `golang.org/x/sys/unix` for block-device `mknod`, bind mounts, read-only
remounts, ownership, and detached unmount cleanup. Parse `/proc/self/mountinfo`
for stale mounts below the jail root. Build the command as:

```go
[]string{
	"--id", vmJailerID(cfg.Service),
	"--exec-file", cfg.Firecracker,
	"--uid", strconv.Itoa(identity.UID),
	"--gid", strconv.Itoa(identity.GID),
	"--chroot-base-dir", cfg.JailerBase,
	"--cgroup-version", "2",
	"--resource-limit", "no-file=4096",
	"--",
	"--api-sock", cfg.APISocket,
}
```

Append `--config-file` only for a cold boot. Keep mount and filesystem calls
behind package variables for unit tests. Create root-owned host symlinks for
API and vsock paths and remove them during cleanup.

- [ ] **Step 4: Confirm GREEN**

```bash
mise exec -- go test ./pkg/catch -run 'TestVMJail' -count=1
```

Expected: PASS without requiring root or mounts.

### Task 5: Console Supervisor and Systemd Launch

**Files:**
- Modify: `pkg/catch/vm_console_proxy.go`
- Modify: `pkg/catch/vm_console_test.go`
- Modify: `pkg/catch/vm_systemd.go`
- Modify: `pkg/catch/vm_systemd_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**
- Extends: `VMConsoleProxyConfig` with `Jailer` and `JailerBase`.
- Extends: `vmSystemdConfig` with `Jailer` and `Isolation`.

- [ ] **Step 1: Write failing launch tests**

Assert legacy mode still directly invokes Firecracker; jailer mode calls the
preparer, starts the returned foreground jailer under PTY, preserves snapshot
load and reboot hooks, and always cleans the jail. Assert jailed systemd units
pass `--jailer` and `--jailer-base`, while legacy units do not.

- [ ] **Step 2: Confirm RED**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test.*(Jailer|VMRun)' -count=1
```

Expected: compile failure for the new config fields and command-line flags.

- [ ] **Step 3: Integrate jailed launch**

Read the restore request before jail preparation. In jailer mode resolve the
identity, prepare the jail, defer cleanup, and start the jailer command. Keep
the direct Firecracker command unchanged in legacy mode. Add `vm-run`
arguments `--jailer` and `--jailer-base`; the unit's root status and existing
exit codes do not change.

Regeneration reads the isolation marker from the actual service root and
derives the jailer beside Firecracker. New units use the manifest path.

- [ ] **Step 4: Confirm GREEN**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test.*(Jailer|VMRun|RenderVMSystemd)' -count=1
```

Expected: PASS.

### Task 6: New VM Default and Explicit Migration Transaction

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/catch/tty_vm.go`
- Modify: `pkg/catch/tty_ops_test.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `pkg/catch/vm_resize.go`
- Modify: `pkg/catch/vm_resize_test.go`

**Interfaces:**
- Extends: `cli.VMSetFlags` and `vmSetFlagsParsed` with `VMMIsolation string` using `flag:"vmm-isolation"`.
- Consumes: isolation, identity, network owner, image, and systemd helpers from earlier tasks.

- [ ] **Step 1: Write failing parser and transaction tests**

Test accepted values `jailer` and `legacy-root`, rejection of other values,
the stopped-VM requirement, missing-jailer preflight, same-version validation,
new-VM jailed default, marker/unit/network apply, and rollback after unit or
network failure.

- [ ] **Step 2: Confirm RED**

```bash
mise exec -- go test ./pkg/cli ./pkg/catch -run 'Test.*VMMIsolation|Test.*JailedVMProvision' -count=1
```

Expected: compile failure for `VMMIsolation`.

- [ ] **Step 3: Implement parser, provisioning, and migration**

Normalize the flag to lowercase. Include it in `hasVMSetChange`. In
`vmSettingsPlan`, retain old/new isolation, old unit bytes, old marker state,
old ownership, and the prospective unit. Preflight before any mutation.
Apply ownership, network, marker, unit, and daemon reload in that order with a
rollback object restoring every completed phase.

New VM provisioning calls `RequireJailer`, ensures `yeet-vm`, uses owned TAPs,
renders the jailed unit, and writes the `jailer` marker before start.

- [ ] **Step 4: Confirm GREEN**

```bash
mise exec -- go test ./pkg/cli ./pkg/catch -run 'Test.*VMMIsolation|Test.*JailedVMProvision' -count=1
```

Expected: PASS.

### Task 7: Snapshot, Restore, Status, Storage, and Removal Compatibility

**Files:**
- Modify: `pkg/catch/vm_snapshot.go`
- Modify: `pkg/catch/vm_snapshot_test.go`
- Modify: `pkg/catch/recovery_vm.go`
- Modify: `pkg/catch/recovery_vm_test.go`
- Modify: `pkg/catch/vm_kernel_sync_test.go`
- Modify: `pkg/catch/host_storage_db_rewrite_test.go`
- Modify: `pkg/catch/remove_test.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`

**Interfaces:**
- Adds: `catchrpc.ServiceVM.VMMIsolation string` with JSON name `vmmIsolation`.
- Consumes: `vmIsolationModeForRoot` and `ensureVMRuntimeIdentity`.

- [ ] **Step 1: Write failing compatibility tests**

Assert jailed full-snapshot temporary directories are owned by `yeet-vm`, old
checkpoint paths remain unchanged, full restore uses the same canonical paths,
kernel synchronization rewrites the canonical config only, marker files move
with service roots, removal clears stale jail state, and info renders both
isolation values.

- [ ] **Step 2: Confirm RED**

```bash
mise exec -- go test ./pkg/catch ./pkg/catchrpc ./pkg/yeet -run 'Test.*(VMMIsolation|Jailed.*Snapshot|Jailer.*Restore)' -count=1
```

Expected: compile failure for the additive info field and ownership hook.

- [ ] **Step 3: Implement compatibility hooks**

After `os.MkdirTemp` for a jailed VM, chown and chmod the new checkpoint
directory for `yeet-vm`. Migration normalizes existing checkpoint contents.
Service info resolves the marker using the already computed effective root.
Removal calls stale jail cleanup before deleting VM data. Do not include the
isolation marker or jailer path in the Firecracker configuration hash.

- [ ] **Step 4: Confirm GREEN**

```bash
mise exec -- go test ./pkg/catch ./pkg/catchrpc ./pkg/yeet -run 'Test.*(VMMIsolation|Jailed.*Snapshot|Jailer.*Restore)' -count=1
```

Expected: PASS.

### Task 8: Documentation and Generated Help

**Files:**
- Modify: `README.md`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-agent.md`
- Modify: root `website` gitlink after the website commit is published

**Interfaces:**
- Documents: new VM default, explicit migration, stopped requirement, status,
  legacy rollback, and matching jailer bundle.

- [ ] **Step 1: Update CLI help and user manual**

Add `--vmm-isolation=jailer|legacy-root` to `vm set` usage and one generic
example. Explain that this is the host VMM identity, not the guest login user or
native `--run-as`. Do not publish private hostnames or paths.

- [ ] **Step 2: Regenerate and verify help**

```bash
tools/generate-yeet-help-agent.sh
git -C website diff --check
rg -n "private[-]host|/Users/" README.md website/docs .codex/skills
```

Expected: generator exits zero, diff check is clean, and private scan finds no
new private examples.

- [ ] **Step 3: Commit and publish the website change**

Use the website repository's permitted focused raw-git exception only after
verification, then commit the root gitlink through GitButler. Do not add a
changelog entry without a release request.

### Task 9: Repository Verification and GitButler Checkpoint

**Files:** all task-owned files only.

- [ ] **Step 1: Run targeted packages**

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catchrpc ./pkg/catch ./cmd/catch -count=1
```

- [ ] **Step 2: Run full and heavy gates**

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run race
mise run fuzz
mise run quality:goal
```

Expected: every command exits zero. Fix any task-caused finding; do not refresh
baselines or suppress failures.

- [ ] **Step 3: Commit only this task**

Run `but diff`, select only Firecracker-jailer files/hunks, and create the
dedicated `codex/firecracker-jailer` branch. Leave ISO-network, GitButler skill,
and native-identity changes untouched.

### Task 10: Isolated Live Rollout on `yeet-lab`

**Files:** no repository edits unless a live failure first gains a regression
test.

- [ ] **Step 1: Build an isolated Catch revision**

Build from the committed `codex/firecracker-jailer` tree, not the union GitButler
workspace. Verify build metadata reports that commit before installation.

- [ ] **Step 2: Install matching jailer artifacts**

For every Firecracker version referenced by a VM on `root@lab-host`, fetch the
official release tarball, verify its published `SHA256SUMS`, install the
matching jailer root-owned and `0755` beside Firecracker, and verify both
`--version` outputs match.

- [ ] **Step 3: Upgrade Catch and deploy a disposable canary**

Install the isolated Catch revision on `yeet-lab`. Create one uniquely named
disposable VM from the cached image, verify boot, agent, SSH, console, snapshot,
full restore, and guest reboot, then remove it and confirm no jail mounts or
processes remain.

- [ ] **Step 4: Migrate existing VMs one at a time**

For each VM discovered immediately before rollout:

```text
yeet stop <vm>
yeet vm set <vm> --vmm-isolation=jailer
yeet start <vm>
yeet status <vm>
yeet info <vm>
yeet ssh <vm> -- true
```

Do not stop the next VM until the current VM is healthy. If migration or boot
fails, stop it, use `--vmm-isolation=legacy-root`, start it, verify recovery,
and stop the rollout until the failure has a regression test and fix.

- [ ] **Step 5: Verify the final host boundary**

For every VM Firecracker PID, verify nonzero `yeet-vm` UID/GID, empty
`CapEff`, `NoNewPrivs: 1`, seccomp mode `2`, a mount namespace distinct from
Catch, the expected root network namespace, owned TAPs, stable API/vsock
sockets, and no stale jail mounts. Re-run VM status and SSH for all VMs.
