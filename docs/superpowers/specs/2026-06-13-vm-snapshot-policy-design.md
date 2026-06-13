# VM Snapshot Policy Design

## Goal

Make VM snapshots in `yeet run --web` match the mental model users have for
VMs: snapshots protect the VM disk by default, retention follows the same
snapshot policy vocabulary as other payload types, and advanced users can ask
for a full Firecracker state and memory checkpoint.

The web UI should stop exposing container/service lifecycle options that do not
map to VMs. In particular, VM users should not see `run` or `docker-update`
snapshot events because VM snapshots are manual in this design.

## Current State

`yeet run --web` sends one snapshot draft shape for every non-cron workload:

- mode
- keep last
- max age
- required
- events

That works for service-root snapshots that are created automatically before
container updates or service-root migrations. It is confusing for VMs because
the useful user action is an operator-triggered VM snapshot, not a Docker update
event.

VMs already have a remote command group under `yeet vm`, DB-backed VM disk
metadata, VM systemd start/stop/status helpers, and Firecracker API socket
paths recorded in service state. ZFS-backed VMs store the runtime disk as a
zvol path under `VM.Disk.Path`.

Firecracker supports pause/resume and full snapshot creation through its API.
The relevant API shape is `PATCH /vm` with `Paused`/`Resumed`, then
`PUT /snapshot/create` for full state and memory snapshots.

Reference: https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md

## Product Decisions

Use the existing snapshot policy model for VM retention and enablement. Do not
add a separate VM snapshot policy model for v1.

For VMs, snapshot policy applies to manual `yeet vm snapshot` commands only.
It does not configure automatic snapshots before `yeet run`, `docker update`,
or `yeet vm set`.

Disk-only zvol snapshots are the default because that is what most users expect
from a VM snapshot in this product. Full state and memory checkpoints are an
explicit power-user option.

Raw-disk VMs are unsupported for `yeet vm snapshot` in v1. The command should
fail with a direct message that VM snapshots require a ZFS zvol-backed VM.

## Web UI

For non-VM workloads, keep the existing `Snapshots` section behavior.

For VMs, render a VM-specific version of the section:

- Summary label: `VM snapshots`
- Mode label: `Policy`
- Keep last label: `Keep last`
- Max age label: `Max age`
- Help text: retention policy for `yeet vm snapshot`; disk snapshots use the
  VM zvol

Hide these VM fields:

- `Events`, because event choices are automatic service lifecycle concepts.
- `Required`, because manual snapshot commands fail or succeed directly. The
  field is meaningful for automatic pre-change snapshots, not for manual
  operator-triggered snapshots.

The web draft may continue to store the shared snapshot fields internally. For
VM payloads it should not send events or required values from hidden controls.
Validation should reject VM snapshot events or required values if a client sends
them directly.

## CLI UX

Add a remote VM command:

```sh
yeet vm snapshot <vm>
yeet vm snapshot <vm> --comment "before package upgrade"
yeet vm snapshot <vm> --full --comment "checkpoint before risky change"
```

Flags:

- `--comment TEXT`: attach human context to snapshot metadata.
- `--full`: also create Firecracker state and memory checkpoint files.

The default command creates a disk-only snapshot.

## Command Behavior

`yeet vm snapshot <vm>` runs on catch through the existing `vm` remote command
group.

The catch-side command flow is:

1. Load the service and require `ServiceTypeVM`.
2. Require `VM.SetupState == "ready"`.
3. Require `VM.Disk.Backend == "zvol"`.
4. Resolve the zvol dataset from `VM.Disk.Path`, trimming `/dev/zvol/` when
   present.
5. Check whether the VM systemd unit is running.
6. If running, pause the VM through the Firecracker API socket.
7. Create a ZFS snapshot of the VM zvol with yeet metadata.
8. If `--full` is set, create Firecracker state and memory files using
   `/snapshot/create`.
9. If the command paused the VM, resume it before returning.
10. Prune yeet-owned snapshots for that VM according to the effective snapshot
    policy.
11. Print the snapshot name and any full checkpoint file paths.

If snapshot creation fails after pause, the command must attempt resume before
returning the error. If resume fails after a snapshot succeeds, return an error
that includes the created snapshot name so the operator has a recovery marker.

Stopped VMs do not need Firecracker pause/resume. Disk-only snapshots can be
created directly. Full checkpoints require a running VM and should fail clearly
when the VM is stopped.

## Snapshot Metadata

ZFS snapshots should reuse the existing yeet-owned metadata pattern and add
VM-specific context where useful:

- `com.yeetrun:created-by=catch`
- `com.yeetrun:service=<vm>`
- `com.yeetrun:event=vm-manual`
- `com.yeetrun:policy-version=1`
- `com.yeetrun:comment=<comment>` when provided
- `com.yeetrun:checkpoint=full` when `--full` is used

The snapshot name should be readable and collision-resistant, following the
existing `yeet-<timestamp>-<event>-g<generation>` style. The `vm-manual` event
name should be reserved for VM manual snapshots.

Full checkpoints need companion metadata because Firecracker state and memory
files are plain files, not ZFS snapshots. Store a small metadata file beside the
checkpoint files with the shared snapshot name, service, comment, created time,
zvol snapshot name, Firecracker state file, and memory file.

## Retention

Retention applies only to yeet-owned snapshots for the same VM zvol and service.
The effective policy comes from server defaults plus the service override, as it
does for other service snapshots.

The manual command should honor:

- enabled/disabled policy
- keep last
- max age

If policy is disabled, the command should fail with a message explaining that VM
snapshots are disabled for the service unless the user passes a future explicit
override. No override flag is part of this v1 design.

Pruning must never delete the snapshot created by the current command. It must
not delete user-created snapshots.

## Out of Scope

- Automatic VM snapshots before `yeet vm set`, VM image refresh, or `yeet run`.
- Rollback/restore commands.
- Raw disk snapshot support.
- Diff checkpoints.
- Recursive dataset snapshots.
- Guest filesystem freeze/thaw hooks.

These can be added later without changing the v1 user contract.

## Testing

Add focused tests for:

- web assets hide VM snapshot events and required controls
- run draft validation rejects VM snapshot events/required values
- CLI parser and registry entries for `yeet vm snapshot`
- catch VM snapshot rejects non-VM services
- catch VM snapshot rejects raw-disk VMs
- zvol path normalization from `/dev/zvol/...`
- pause, snapshot, resume command ordering for running VMs
- resume attempted after snapshot failure
- full checkpoint calls Firecracker snapshot creation and writes metadata
- retention pruning keeps the current VM snapshot

Manual live testing should use a disposable ZFS-backed VM on `yeet-lab`.
