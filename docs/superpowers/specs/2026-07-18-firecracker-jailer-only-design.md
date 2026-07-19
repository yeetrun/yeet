# Firecracker Jailer-Only Runtime Design

## Status

Approved for implementation. This design supersedes the selectable
`jailer`/`legacy-root` runtime described by the earlier Firecracker jailer
design. The implementation keeps the useful part of that work—the jailer—and
removes the security downgrade.

## Goal

Every Firecracker process started by Yeet runs through the matching Firecracker
jailer as the unprivileged `yeet-vm` host account. There is no supported command,
configuration value, missing-file default, or recovery branch that launches
Firecracker directly as root.

Catch upgrades do not restart running VMs just to establish this invariant. A
root Firecracker process that was already running before the upgrade may finish
its current lifetime. The upgrade rewrites its unit so the next launch must use
jailer. If preparation for that launch fails, the VM stays stopped. It never
falls back to root.

## Non-goals

- Do not change users inside VM guests.
- Do not make VMs participate in native-service `--run-as`.
- Do not move the root Catch supervisor, networking, storage, snapshot,
  journal-replay, or guest-kernel maintenance work into `yeet-vm`.
- Do not stop or restart VMs during a normal Catch upgrade.
- Do not add per-VM host accounts in this change. The shared `yeet-vm` account
  remains the supported runtime identity.

## Why remove the mode

The existing implementation treats an absent
`<service-root>/run/vmm-isolation` file as `legacy-root`. That was a useful
bootstrap rule for the first jailer rollout because old VMs had no marker. It is
a poor steady-state rule: deleting one small file silently turns a constrained
VMM back into a root process.

The rollback path also makes the runtime more complicated than it needs to be.
Systemd rendering, network ownership, checkpoint ownership, VM settings, status,
and process launch all branch on a choice that production Yeet should not offer.
Firecracker's production guidance recommends the jailer or constraints at least
as strong as it. Yeet has a working jailer path, so keeping the weaker path is no
longer buying useful compatibility. It is just preserving a downgrade.

The upstream references for this boundary are Firecracker's
[production host setup recommendations](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md#jailer-configuration)
and [jailer operations guide](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md).

## Runtime invariants

The jailer-only implementation has these invariants:

1. `catch vm-run` requires a non-empty Firecracker path, jailer path, jailer
   base, service root, configuration path, API socket, and console socket.
2. The Firecracker and jailer binaries pass the existing trusted-path,
   ownership, permissions, and matching-version checks before every launch.
3. `prepareVMConsoleProcess` always builds a jail and executes jailer. The direct
   `exec.Command(firecracker, ...)` branch does not exist.
4. Generated VM units always pass `--jailer` and `--jailer-base`.
5. VM TAP devices, writable disks, checkpoint directories, API sockets, and
   vsock sockets are delegated only as required by the jailed runtime.
6. Any failed preparation prevents the launch. No error handler retries with
   root Firecracker.

The systemd service and `catch vm-run` supervisor remain root. The jailer starts
with the root authority it needs to create the mount namespace, chroot, and
device nodes, then drops Firecracker to `yeet-vm`. This is the same root boundary
as the current jailer implementation; the change removes the alternate boundary.

## One-way readiness state

The existing marker remains at:

```text
<service-root>/run/vmm-isolation
```

It is no longer an isolation-mode selector. Its only valid content is:

```text
jailer
```

The marker means the VM's durable storage and network resources have completed
the one-way jailer transition. An absent marker means `jailer pending restart`,
not `legacy-root`. An unreadable marker or any other content is an error.

Keeping the existing marker avoids rewriting already migrated VMs and preserves
compatibility with the current jailer-capable Catch release. Helpers and tests
should use readiness language rather than mode language. The marker may be
removed in a later cleanup once every supported upgrade path predates jailer,
but that is not required to remove root launch now.

## Upgrade behavior

`yeet init` already runs the new Catch binary as a temporary installer before it
replaces the installed Catch service. The installer gains a jailer-only VM
upgrade phase before committing the new binary and VM units.

The phase reads the Catch database and builds a plan for every VM:

- resolve the effective service root and configured data root;
- resolve the referenced Firecracker binary, matching jailer, jailer base,
  disk, checkpoints, sockets, and generated systemd unit;
- validate custom service roots and ZFS-backed roots without assuming a default
  path;
- determine whether the readiness marker is present;
- determine whether the VM service is running;
- render the jailer-only replacement unit.

The installer stages all replacement units before changing any live unit. It
then installs the new Catch binary, atomically replaces the VM units, and runs
one `systemctl daemon-reload`. It does not stop, start, or restart a VM.

A running pre-jailer VM therefore keeps its existing Catch supervisor and root
Firecracker process. Replacing the binary and unit on disk does not change an
already executing process. When that service later exits or is restarted,
systemd uses the jailer-only unit and the new Catch binary.

The installer reports the count and names of VMs that are ready and those that
will complete the transition on their next restart. This is operational state,
not a prompt and not a selectable compatibility mode.

## Matching jailers for older bundles

VMs created by older Yeet releases may reference an image bundle whose manifest
contains Firecracker but no jailer. Rewriting that VM's unit without preparing a
matching jailer would merely schedule a boot failure for later, which is a bad
kind of deferred work.

Upgrade preflight resolves this before replacing Catch or any unit:

1. If an executable sibling `jailer` exists, validate it against the referenced
   Firecracker binary.
2. Otherwise, search verified managed cache bundles for a manifest-declared
   jailer whose Firecracker release and architecture match the target binary.
3. If the local cache has no candidate for an official VM, fetch the current
   catalog manifest for that VM family and download only its declared jailer
   artifact into upgrade staging. Do not download an unused root filesystem just
   to acquire one executable.
4. Re-verify the source artifact against its manifest checksum, then copy it
   atomically beside the target Firecracker binary as root-owned mode `0755`.
5. Validate the resulting target pair with the normal trusted-runtime checks.

The old manifest remains readable and does not need to be rewritten. Its extra
root-owned jailer file is runtime compatibility material, not a new image
identity.

If no trusted matching jailer exists, the installer fails before it replaces
the installed Catch binary or any VM unit. The error identifies the affected VM,
Firecracker release, and remediation: refresh the official VM image cache or
re-import a custom image with a matching jailer. The old installation continues
to run unchanged. We do not make the upgrade "succeed" by preserving future root
launches.

## First launch after upgrade

The existing `vm-network-ensure` systemd pre-start command becomes the one-way
transition boundary for a VM without the readiness marker. At that point the old
Firecracker process has exited, so Catch can safely change resources that cannot
be reconciled under a running VMM.

For a pending VM, pre-start performs this transaction:

1. ensure and resolve the static `yeet-vm` account;
2. validate the Firecracker/jailer pair and trusted paths;
3. validate the Firecracker configuration, disk backend, checkpoints, sockets,
   and jail paths;
4. remove stale jail mounts and socket links;
5. delegate the raw disk or prepare the jailed ZVOL device path;
6. normalize checkpoint ownership;
7. remove and recreate the VM network with TAP ownership assigned to
   `yeet-vm`;
8. write the `jailer` readiness marker atomically.

Only then does systemd run `catch vm-run`. If any step fails, pre-start exits
nonzero, the marker remains absent, and Firecracker is not started. Retrying the
service retries the idempotent transition.

For an already ready VM, pre-start keeps the current lightweight network ensure
behavior with `yeet-vm` ownership and does not repeat recursive storage work.

## CLI, RPC, and status

Remove `--vmm-isolation` from `yeet vm set`, shared CLI metadata, parser tests,
generated help, README examples, and the website manual. `vm set` continues to
manage VM shape and networking while the VM is stopped.

`yeet info <vm>` keeps the `VMM isolation` field because it is useful evidence
about the host process boundary. It reports one of:

```text
VMM isolation  jailer
VMM isolation  jailer (pending restart)
```

The JSON field uses stable machine values `jailer` and
`jailer-pending-restart`. There is no `legacy-root` value. A pending result is a
warning that an already running process has not yet crossed the one-way
transition, not an available mode.

No new permission is introduced. VM information remains a `read` operation.
Catch installation and VM mutation keep their existing high-trust boundaries.

## Code removal and simplification

The implementation removes or collapses these branches:

- the `vmIsolationLegacy` constant and two-value validation;
- removal of the readiness marker as a way to select root launch;
- isolation settings in `cli.VMSetFlags` and `vmSettingsPlan`;
- root-versus-jailer systemd rendering;
- root-versus-jailer network ownership;
- root-versus-jailer checkpoint ownership;
- the optional-jailer validation in `VMConsoleProxyConfig`;
- `vmFirecrackerCommand` and the empty-jailer branch in
  `prepareVMConsoleProcess`;
- rollback code that restores a root unit or removes the marker;
- manual and generated-help text describing a root compatibility mode.

Provisioning, host-storage unit regeneration, snapshot/restore, guest reboot,
kernel synchronization, custom roots, and ZFS paths all use the jailer path
unconditionally.

The earlier Firecracker jailer design and implementation plan are superseded.
Once this design has an implementation plan, remove the obsolete files from the
current tree rather than leaving two contradictory runtime designs for future
agents to discover. Git history is already an adequate archive.

## Failure and rollback behavior

There is no runtime rollback to root. Recovery means fixing the jailer input,
ownership, mount, network, or socket error and retrying the VM.

Upgrade failures before the commit point leave the installed Catch binary and
VM units unchanged. Failures while committing staged units restore the previous
unit files and reload systemd. Existing VM processes are never killed as part of
that recovery.

A successfully verified jailer backfilled into an old image directory may
remain after a later upgrade step fails. This is a safe additive side effect:
the old Catch build ignores the extra root-owned file, while a retry can reuse
it. Binary or unit changes do not receive the same exception.

After a VM completes its one-way transition, rolling back to the immediately
previous jailer-capable Catch build remains safe because that build understands
the `jailer` marker and jailer-enabled unit. Rolling back to a root-only Yeet
release is not a supported recovery path after this change.

## Tests

Automated coverage must prove:

- `vm-run` rejects a missing jailer and never constructs a direct Firecracker
  command;
- every generated and regenerated VM unit contains jailer arguments;
- upgrade planning is read-only and handles default, custom, and ZFS roots;
- upgrades rewrite units without calling stop, start, restart, or try-restart;
- a verified same-version jailer is backfilled into an older official bundle;
- a missing or mismatched trusted jailer aborts before binary or unit changes;
- an absent readiness marker produces `jailer-pending-restart`;
- pending pre-start delegates raw disks and checkpoints and recreates TAPs;
- pending pre-start handles ZVOL-backed disks without changing the host ZVOL
  node;
- transition failure leaves the marker absent and prevents VMM launch;
- retry after a partial failure is idempotent;
- ready VMs do not repeat migration work;
- snapshots, full restore, guest reboot, console, API, vsock, SSH, ballooning,
  host-storage moves, and kernel synchronization remain functional;
- CLI help, parsing, RPC status, README, and manual content contain no supported
  `legacy-root` mode.

Run targeted package tests, the full Go suite, pre-commit, race, fuzz, and the
repository quality goal before publication because this change touches process
launch, parsers, RPC status, networking, storage ownership, and orchestration.

## Live rollout and cleanup

Live validation uses the VM-capable host after repository gates pass:

1. install the committed Catch build without restarting VMs;
2. verify every unit on disk contains the matching jailer arguments;
3. verify already running Firecracker PIDs and VM reachability are unchanged;
4. restart one disposable canary and prove the pending transition path;
5. verify the canary Firecracker UID/GID, empty effective capabilities,
   `NoNewPrivs`, seccomp, mount namespace, TAP ownership, sockets, snapshot,
   restore, reboot, console, agent, and SSH behavior;
6. verify every durable VM still runs as `yeet-vm` and remains reachable;
7. remove obsolete pre-jailer unit backup files after checking their exact
   names and confirming the active units are jailer-only;
8. use `yeet vm images prune --dry-run` and the normal confirmed prune flow to
   remove unused pre-jailer cache bundles. Do not remove referenced bundles by
   path.

The final host check rejects any Firecracker process with UID 0 and confirms no
stale jail mounts, processes, TAPs, or sockets remain.

## Documentation

README and manual pages describe the steady state directly: Yeet runs
Firecracker through jailer as `yeet-vm`. They do not describe a mode choice.

Temporal compatibility wording belongs in the changelog or a focused upgrade
note: a VM already running during upgrade keeps running and changes to jailer on
its next restart. That is an upgrade fact, not the product's permanent identity.
