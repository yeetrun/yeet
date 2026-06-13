# Snapshot Recovery Lifecycle Design

## Goal

Turn yeet snapshots into a usable recovery lifecycle for homelab operators.
Users should be able to see what recovery points exist, understand what each
one can do, safely inspect state before destructive changes, restore when
needed, and keep important recovery points from retention pruning.

The product model is:

```text
service -> recovery points -> inspect / clone / restore / delete / protect
```

Snapshots remain backed by ZFS where that is the right storage primitive, but
the user-facing feature is service recovery, not raw ZFS administration.

## Product Context

Yeet manages long-lived services across payload types: compose apps, container
images, Dockerfiles, binaries, cron jobs, and VMs. Operators use it to deploy,
upgrade, resize, debug, and recover homelab workloads. Snapshot features should
therefore answer operational questions:

- What recovery points do I have for this service?
- What caused this recovery point to be created?
- Can I inspect it without changing the current service?
- Can I roll back an upgrade or risky edit?
- Which recovery points will retention keep or prune?

The current implementation can create service-root snapshots for some
pre-change operations and can create manual ZFS-backed VM snapshots. It does
not yet provide a cohesive way to list, inspect, clone, restore, protect, or
delete recovery points.

## Design Direction

Add a shared `yeet snapshots` lifecycle surface that is type-aware.

For service-root payloads, a recovery point represents a ZFS dataset snapshot
of the service root. For VMs, a recovery point represents a ZFS zvol snapshot
of the VM disk, optionally paired with Firecracker state and memory files from
a full checkpoint.

The CLI should present one recovery vocabulary across payload types while
allowing each payload type to expose only the actions that are safe and
supported.

Keep `yeet vm snapshot <vm>` as an ergonomic alias for VM users, but make the
top-level `yeet snapshots` command the canonical management surface.

## Command Surface

### List

```bash
yeet snapshots list
yeet snapshots list <svc>
yeet snapshots list --format=json
```

Lists recovery points across services or for one service. Table output should
favor scanability:

- service
- type
- created
- age
- mode (`disk`, `full`, `service-root`)
- event
- protected
- comment

JSON output should include stable machine-readable fields: service name,
service type, storage kind, full ZFS snapshot name, short snapshot name,
creation time, event, generation, comment, checkpoint mode, protected status,
retention status, and available actions.

### Inspect

```bash
yeet snapshots inspect <svc> <snapshot>
yeet snapshots inspect <svc> <snapshot> --format=json
```

Shows exact details for one recovery point:

- service and payload type
- source dataset or zvol
- full ZFS snapshot name
- created time, event, generation, comment
- checkpoint mode
- full checkpoint file paths if present
- whether the recovery point is protected
- whether retention would currently prune it
- supported actions for this recovery point

The `<snapshot>` argument accepts the full ZFS snapshot name or the short
`yeet-...` suffix when it is unambiguous for the service.

### Create

```bash
yeet snapshots create <svc>
yeet snapshots create <svc> --comment "before upgrade"
yeet snapshots create <vm> --full --comment "checkpoint before risky change"
```

Creates a manual recovery point. For VMs, this is equivalent to the existing
`yeet vm snapshot <vm>` command. For ZFS-backed service-root payloads, this
creates a service-root snapshot with event `manual`.

`--full` is valid only for VMs. Non-VM payloads should fail with a direct error
that full checkpoints are VM-only.

### Clone

```bash
yeet snapshots clone <svc> <snapshot> <new-svc>
yeet snapshots clone <svc> <snapshot> <new-svc> --start
```

Clone is the preferred safe recovery workflow. It lets users inspect or recover
data without mutating the original service.

First implementation wave should support VM disk snapshots:

- create a new VM service using a clone of the selected zvol snapshot
- copy the original VM metadata that is still valid for a clone: image ref,
  shape, kernel/initrd/runtime settings, guest metadata model, and networking
  defaults
- generate fresh runtime identity where needed: service name, service root,
  Firecracker config, sockets, tap/macvlan interfaces, and systemd unit
- create the cloned VM stopped by default; start it only when `--start` is set

For full VM checkpoints, clone should initially clone the disk snapshot only
and report that memory/state resume is not part of clone unless later support
is implemented. The checkpoint files remain inspectable metadata.

Service-root clone can be added later using the same command surface, but it is
not required in the first implementation wave.

### Restore

```bash
yeet snapshots restore <svc> <snapshot>
yeet snapshots restore <svc> <snapshot> --stop --start
yeet snapshots restore <svc> <snapshot> --yes
```

Restore is destructive in-place rollback. It must be safe by default:

- refuse to restore a running service unless the user passes `--stop` or stops
  it first
- show a confirmation prompt unless `--yes` is set
- create a pre-restore recovery point when possible before rolling back
- clearly print the recovery point restored and the pre-restore snapshot name

For VMs, restore rolls back the VM zvol to the selected disk snapshot. Disk
restore is the default even if the selected recovery point has a full
checkpoint. Restoring Firecracker memory/state should be a separate explicit
future option because runtime compatibility, device state, and Firecracker
version checks need careful handling.

For service-root payloads, restore rolls back the service-root dataset when the
service is stopped. Restore should not be offered for services without a
ZFS-backed service root.

### Delete

```bash
yeet snapshots rm <svc> <snapshot>
yeet snapshots rm <svc> <snapshot> --yes
```

Deletes a recovery point and any companion checkpoint files owned by yeet. It
requires confirmation unless `--yes` is set.

Delete must refuse protected recovery points until the user unprotects them.
It must never delete snapshots that are not tagged as yeet-created for the
target service.

### Protect

```bash
yeet snapshots protect <svc> <snapshot>
yeet snapshots unprotect <svc> <snapshot>
```

Marks a recovery point as protected from yeet retention pruning and from
accidental manual deletion. Protection should be stored as a ZFS user property
on the snapshot:

```text
com.yeetrun:protected=true
```

Unprotect clears or sets that property to false. Retention pruning should skip
protected snapshots.

## Data Model

Introduce an internal `RecoveryPoint` model shared by service-root and VM
snapshot code.

Fields:

- service name
- service type
- storage kind: `service-root-dataset` or `vm-zvol`
- dataset or zvol name
- full ZFS snapshot name
- short snapshot name
- creation time
- yeet-created marker
- event
- generation
- comment
- checkpoint mode: `service-root`, `disk`, or `full`
- checkpoint metadata paths for full VM checkpoints
- protected flag
- available actions

The model should be derived from ZFS snapshot listings and yeet metadata rather
than from a separate database table. The DB should remain the source of truth
for current service identity, service type, service root, VM disk path, and VM
runtime metadata.

## Snapshot Identity and Selection

Users should not need to copy long zvol snapshot names for common workflows.

Accepted snapshot selectors:

- full ZFS snapshot name
- short `yeet-...` snapshot suffix
- unique prefix of the short name when unambiguous for that service

Ambiguous selectors should fail and show matching candidates.

## Safety Rules

All actions must verify the selected snapshot is yeet-owned and belongs to the
requested service.

Clone and restore must validate payload type and storage backend before making
changes.

Restore must require a stopped service unless an explicit stop/start option is
provided. If yeet stops a service for restore and restore fails, it should not
blindly restart unless the service state is known to be safe.

VM disk restore must operate only on zvol-backed VMs. Raw disk VMs should fail
with a clear message.

Full checkpoint files are not enough to promise live-state restore. Until that
is implemented, inspect should display them and restore should explicitly say
that disk rollback is supported while memory/state restore is not.

## Implementation Waves

### Wave 1: Discovery and Management

- `yeet snapshots list`
- `yeet snapshots inspect`
- `yeet snapshots create`
- `yeet snapshots rm`
- `yeet snapshots protect`
- `yeet snapshots unprotect`
- retention skips protected snapshots
- keep `yeet vm snapshot` as an alias for VM create

This wave makes existing snapshots usable and understandable.

### Wave 2: VM Recovery

- `yeet snapshots clone <vm> <snapshot> <new-vm>`
- `yeet snapshots restore <vm> <snapshot>`
- pre-restore safety snapshot
- stopped/running validation and `--stop --start`
- live test on disposable ZFS-backed VMs

This wave completes the main VM recovery story without promising full
Firecracker state restore.

### Wave 3: Service-Root Recovery

- service-root `restore` for compose, image, Dockerfile, and binary services
  with ZFS-backed roots
- optional service-root clone when there is a clear service-definition story
- docs that explain clone-first recovery and destructive restore

Cron jobs and non-ZFS services should show clear unsupported-action messages.

### Future: Full VM State Restore

Full Firecracker memory/state restore should be treated as a separate advanced
feature. It needs compatibility checks for Firecracker version, kernel/initrd,
machine config, block device path, network devices, and checkpoint file
presence. The current full checkpoint metadata should be designed so this
future command can consume it.

## Docs and UX

Docs should explain snapshots as recovery points, not ZFS internals first.

The VM docs should show:

- create a recovery point before risky work
- list and inspect recovery points
- clone a VM from a recovery point for safe inspection
- restore in place only when intentionally rolling back

The ZFS concept docs should explain that ZFS is the storage backend and that
yeet adds metadata, retention, and safety checks.

Command help should make destructive behavior explicit for `restore` and `rm`.

## Testing

Unit tests should cover:

- parsing snapshot selectors
- listing and filtering yeet-owned snapshots
- protected snapshot retention behavior
- inspect output for disk, full, and service-root recovery points
- delete refusal for protected and non-yeet snapshots
- VM clone plan validation
- VM restore refusal while running without `--stop`
- VM restore creates a pre-restore snapshot when possible
- unsupported payload/storage combinations

Live tests should use `yeet-pve1` with disposable ZFS-backed VMs:

- create VM, create snapshots, list and inspect
- protect a snapshot and verify retention skips it
- clone from a disk snapshot and boot or inspect the clone
- restore a stopped VM from a snapshot
- clean up VM services and verify zvols/snapshots are removed

## Out of Scope

- Browser UI for snapshot management.
- Raw disk VM snapshotting.
- Non-ZFS snapshot backends.
- Automatic VM snapshots before every VM setting change.
- Full Firecracker memory/state restore in the first implementation waves.
- Backups or replication to another host.
