# Firecracker Jailer Isolation Design

## Goal

Run every newly provisioned Yeet Firecracker VMM as an unprivileged host
process inside the Firecracker jailer while preserving Catch's existing root
authority for provisioning, networking, snapshots, recovery, and guest-kernel
maintenance. Existing VMs remain unchanged until an operator explicitly
migrates each stopped VM.

This design concerns the host Firecracker process identity. It does not change
guest users and does not make VMs participate in native service `--run-as`.

## Security boundary

The VM systemd unit and `catch vm-run` remain root. The root supervisor owns:

- bridge, veth, TAP, route, and namespace preparation;
- raw-disk and ZVOL provisioning;
- jail resource projection and cleanup;
- the serial-console listener and process supervision;
- Firecracker API and vsock socket exposure;
- snapshot, restore, balloon, and recovery coordination;
- ext4 journal replay and guest-kernel synchronization after reboot;
- systemd unit installation and removal.

The jailer begins as root, creates a private mount namespace and chroot, creates
the jailed KVM and TUN device nodes, drops to the selected UID/GID, and execs
Firecracker. Firecracker receives no supplementary groups and no ambient host
capabilities. Its existing `NoNewPrivs` and restrictive seccomp policy remain
enabled.

The first release deliberately keeps the VMM in the root network namespace.
It can open only TAP devices delegated to its UID. A dedicated network
namespace and per-VM UID/GID mode remain future hardening options.

## Host identity

Yeet creates one static system account named `yeet-vm` on the first operation
that provisions or migrates a jailed VM. The account has:

- a host-allocated system UID and GID;
- no home directory;
- a nologin shell;
- no supplementary groups;
- no membership in `kvm`, `disk`, or networking groups.

Jailer-owned `/dev/kvm` and `/dev/net/tun` nodes and Yeet-owned TAP devices
provide the exact access Firecracker needs. The account is distinct from the
native-workload `yeet-svc` account.

A shared identity minimizes host account churn. Per-VM jails still hide other
VM resources during normal operation. If a Firecracker process escapes both
its sandbox and chroot, the shared UID is not an additional cross-VM boundary;
the launch model therefore keeps identity resolution behind a focused helper
so a future per-VM account mode does not require a jail format change.

## Durable isolation mode

Isolation mode is recorded in a root-owned marker:

```text
<service-root>/run/vmm-isolation
```

The only stored v1 value is `jailer`. An absent marker means `legacy-root`.
This makes old VMs backward-compatible without a database migration and keeps
the setting with the VM during custom service-root, ZFS, snapshot, clone, and
host-storage operations.

New VMs write `jailer`. Existing VMs do not acquire the marker during Catch
upgrade or systemd regeneration. `yeet vm set <vm>
--vmm-isolation=jailer` writes it transactionally while the VM is stopped.
`--vmm-isolation=legacy-root` removes it as an explicit rollback.

## Trusted runtime bundle

Each VM image bundle can carry a `jailer` artifact next to `firecracker`.
The manifest records both names and checksums. The jailer must be from the same
Firecracker release.

Older manifests without `jailer` remain readable so existing root VMs and
cached images are not invalidated. Creating or migrating a jailed VM requires
the jailer artifact and fails clearly if it is absent.

Before every jailed launch, Catch verifies:

- Firecracker and jailer are regular executable files;
- the files and their path ancestry are root-owned;
- neither files nor ancestors are group/other writable;
- no path component is a symlink;
- both binaries report the same semantic release version.

The configured Catch data root remains authoritative. Fresh installations use
`/var/lib/yeet`; custom data roots and root-owned ZFS-backed roots are valid.
No code assumes `/root/yeet-data`.

## Jail layout

Jails are transient and live below the configured Catch data root:

```text
<data-root>/vm-jailer/<exec-name>/<stable-vm-id>/root
```

This keeps the jail executable on hosts that mount `/run` with `noexec`, without
requiring a host mount-policy change. Catch removes each instance directory
after the VMM exits; only the root-owned base hierarchy remains. Fresh defaults
use `/var/lib/yeet/vm-jailer`, while custom and ZFS-backed data roots retain
their configured path.

Catch removes stale mounts and the old jail before launch. The stable jailer
ID is derived from the Yeet service name, restricted to the jailer's supported
character set and length.

The canonical Firecracker config remains outside the jail. Catch recreates
the same absolute paths inside the jail and exposes only the resources named by
that VM's config:

- config file: read-only bind mount;
- kernel and optional initrd: read-only bind mounts;
- raw disk: read-write bind mount, owned by `yeet-vm`;
- ZVOL: a new jailed block-device node with the source major/minor and jailed
  ownership; the host `/dev/zvol` node is unchanged;
- checkpoint base directory: read-write bind mount;
- API and vsock parent directory: writable only by the VMM identity;
- `/dev/kvm` and `/dev/net/tun`: created by jailer.

All projected paths must be absolute, clean, and below the jail root after
translation. Catch rejects parent traversal, symlink sources where a regular
file is expected, unexpected file types, and paths outside the VM's known
resource set. A `/dev/zvol` symlink is resolved with `stat` only to obtain the
underlying block device's major and minor numbers; Catch creates a new device
node inside the jail and never exposes or changes the host symlink or target.

## Socket compatibility

Firecracker creates the API and vsock Unix sockets at the canonical absolute
paths as seen inside its chroot. Catch creates root-owned host symlinks at the
existing database-recorded paths that point to the corresponding socket paths
under the jail root.

This preserves existing API, agent, balloon, snapshot, and status clients. If
live validation shows that a particular socket replacement path does not
reliably follow symlinks, that socket is replaced with a root-owned Unix-socket
forwarder without changing the canonical Firecracker configuration.

The serial console stays outside the jail and continues through the root Catch
PTY supervisor. Guest-controlled serial data remains a residual root-facing
surface and must continue using bounded journald storage.

## Network behavior

The network planner accepts an optional TAP owner. Jailed and newly created VM
TAPs are created with:

```text
ip tuntap add <tap> mode tap user yeet-vm group yeet-vm
```

Root Firecracker can still use an owned TAP, so applying ownership consistently
does not break `legacy-root` mode. The account is ensured before network setup.

Changing isolation recreates the stopped VM's network even when network modes
do not change. This converts existing ownerless TAPs before the jailed start.

## Launch and process supervision

The VM systemd unit remains a root unit. In jailer mode its `vm-run` command
also receives the matching jailer path and the volatile jail base.

`catch vm-run`:

1. reads any full-restore request;
2. resolves the `yeet-vm` UID/GID;
3. validates the runtime pair;
4. prepares resource projections and socket links;
5. starts jailer under the existing PTY;
6. retains console, snapshot-load, guest-reboot, and exit-code behavior;
7. unmounts projected resources and removes the volatile jail after exit.

Jailer uses cgroup v2 without adding a nested policy. The existing VM systemd
cgroup remains the resource-control boundary. It runs in the foreground with
an explicit file-descriptor limit. V1 does not use `--daemonize`,
`--new-pid-ns`, or `--netns`.

Catch starts jailer as root with an explicitly empty supplementary-group list.
Jailer then drops the Firecracker process to `yeet-vm`; this avoids carrying
Catch's root supplementary groups across the privilege transition.

## Snapshot and restore compatibility

Firecracker snapshots retain external resource names and absolute paths. The
jail mirrors those paths, so the canonical config and the existing
compatibility hash remain unchanged.

The checkpoint base directory is mounted into every jail. Catch creates each
temporary full-checkpoint directory as root and, for a jailed VM, immediately
changes that directory to `yeet-vm` so Firecracker can create the state and
memory files. Migration normalizes existing checkpoint files to the same
identity so old full snapshots can be restored.

Restore requests remain root-owned outside the jail. The root supervisor reads
the request before launch, projects the referenced state and memory paths
through the checkpoint mount, starts Firecracker without `--config-file`, and
calls `/snapshot/load` through the stable host API socket path.

Guest reboot continues to exit Firecracker, run root journal replay and kernel
synchronization, update the canonical config, and let systemd start a fresh
jail containing the new config.

## Migration and rollback

The command is:

```text
yeet vm set <vm> --vmm-isolation=jailer
```

It is an existing `manage` operation. The VM must already be stopped.

Preflight verifies the account, runtime bundle, service-root traversal,
Firecracker config, disk type, checkpoint tree, and systemd paths. Apply then:

1. records the old marker and unit;
2. normalizes only the raw disk and checkpoint tree ownership;
3. removes and recreates the network with the delegated TAP owner;
4. writes the new marker and unit atomically;
5. reloads systemd;
6. commits success.

Failure restores the marker, unit, and prior network plan. Raw disk and
checkpoint ownership deliberately remains delegated: legacy root Firecracker
can still use those resources, while retaining the delegation makes rollback
idempotent and avoids a second recursive ownership rewrite.

Catch never automatically downgrades a VM after jailer mode is selected. An
operator can explicitly use `--vmm-isolation=legacy-root` while the VM is
stopped. Existing VMs are never batch-migrated or prompted during upgrade.

## User-visible status

`yeet info <vm>` includes:

```text
VMM isolation  jailer
```

or `legacy-root`. JSON info exposes the same additive field. New CLI syntax and
the VM manual document the stopped-VM requirement, missing-jailer error, and
explicit rollback.

## Validation

Automated tests cover manifest compatibility, identity creation, trusted path
validation, jail ID/path construction, raw and ZVOL projection, mount cleanup,
socket links, jailer argv, TAP ownership, systemd rendering, migration
rollback, checkpoint ownership, restore, kernel reboot behavior, status, and
custom roots.

Live validation on the VM host uses a disposable canary before existing VMs.
For each migrated VM it proves:

- Firecracker UID/GID is `yeet-vm` and nonzero;
- effective capabilities are empty;
- `NoNewPrivs=1` and restrictive seccomp remain active;
- Firecracker has a private mount namespace;
- the root network namespace is intentionally shared;
- TAP ownership matches `yeet-vm`;
- `/proc/<pid>/root` exposes only the projected resources;
- API, console, vsock agent, SSH, balloon, snapshot, full restore, reboot, and
  stop/start continue working;
- no stale mounts, processes, TAPs, or sockets remain after failure or stop.
