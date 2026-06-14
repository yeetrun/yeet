# Complete Snapshot Recovery Design

## Goal

Make yeet recovery coherent across service definitions, persistent state, and VM
runtime checkpoints. The user should understand which command rolls back a
deployed definition, which command restores data/runtime state, and which
recovery paths are safe to try before mutating the original service.

The product model is:

```text
service definitions -> generations -> service rollback
service state       -> snapshots   -> clone / restore / protect / delete
VM runtime state    -> checkpoints -> explicit full-state restore
```

This design replaces the earlier incomplete "recovery point lifecycle" with a
coverage-driven feature. The work is not complete until each item in the
coverage matrix is implemented or explicitly documented as a non-goal.

## Current Findings

Generations are a service definition and artifact concept. The database stores
`Generation` and `LatestGeneration` on every service record, but the current VM
path does not make VM generations meaningful:

- normal installer services advance generations and promote artifacts to
  `latest` and `gen-N`
- `yeet rollback` rolls the service DB generation back and reinstalls that
  generation
- VM provisioning writes `VMConfig`, Firecracker files, and systemd units
  directly without advancing service generations
- VM settings changes mutate VM config in place
- VM snapshots currently record generation `0` because VM services do not
  participate in generation commits

Therefore generations should not be presented as a VM recovery mechanism unless
VM config generations are deliberately designed later.

Snapshots are a state recovery concept. They are backed by ZFS service-root
datasets, VM zvol disk snapshots, and optional Firecracker state/memory files
for full VM checkpoints.

## Command Information Architecture

### Service Definitions And Generations

Move generation-oriented commands under `yeet service`.

```bash
yeet service rollback <svc>
yeet service generations <svc>
```

`yeet service rollback <svc>` replaces the top-level `yeet rollback <svc>`.
There should be no compatibility alias unless implementation uncovers a hard
technical blocker. This repo has little external user compatibility burden, and
keeping the old command would preserve conceptual debt.

`yeet service generations <svc>` should show:

- service name and type
- current generation
- latest generation
- available generation refs
- whether rollback is supported

Generation rollback is supported for services that have real generated
artifacts and installer support. VM generation rollback is not supported.

The top-level `yeet rollback` command should be removed from CLI metadata,
local parsing, remote routing, help output, docs, and tests.

### State Recovery

Keep `yeet snapshots` as the canonical state recovery surface:

```bash
yeet snapshots list [svc] [--format=table|json|json-pretty]
yeet snapshots inspect <svc> <snapshot> [--format=table|json|json-pretty]
yeet snapshots create <svc> [--comment=TEXT] [--full]
yeet snapshots clone <svc> <snapshot> <new-svc> [--start]
yeet snapshots restore <svc> <snapshot> [--stop] [--start] [--yes] [--mode=disk|full] [--generation=current|snapshot]
yeet snapshots protect <svc> <snapshot>
yeet snapshots unprotect <svc> <snapshot>
yeet snapshots rm <svc> <snapshot> [--yes]
```

`yeet vm snapshot <vm>` remains an ergonomic alias for
`yeet snapshots create <vm>`. It is a VM-specific shortcut for capture, not a
separate management surface.

## Recovery Point Model

Recovery points are derived from ZFS and current service DB state rather than a
new DB table.

Fields:

- service name
- service type
- storage kind: `service-root-dataset` or `vm-zvol`
- dataset or zvol name
- full ZFS snapshot name
- short snapshot name
- created time
- created-by marker
- event
- optional service generation at capture time
- comment
- checkpoint mode: `service-root`, `disk`, or `full`
- full checkpoint state and memory file paths
- protected flag
- retention label
- available actions

For VM recovery points, generation should not be displayed as a meaningful
rollback axis. New VM snapshot names should avoid a generation suffix if that
can be done without breaking selector handling. Existing `...-g0` snapshot names
must remain selectable. JSON can keep a nullable or omitted generation field for
VM points.

For service-root recovery points, generation remains useful because it records
the deployed definition generation at the time the state snapshot was taken.

## Clone

`clone` is the safe recovery workflow. It lets users inspect, test, or recover
from state without mutating the original service.

```bash
yeet snapshots clone <svc> <snapshot> <new-svc>
yeet snapshots clone <svc> <snapshot> <new-svc> --start
```

Common rules:

- `<new-svc>` must not already exist.
- The selected snapshot must be yeet-owned and belong to `<svc>`.
- Raw-disk VMs and non-ZFS service roots are unsupported.
- Clone should leave the new service stopped unless `--start` is passed. If an
  existing install path starts the service as a side effect, clone must stop it
  before returning or that service type is not eligible for clone yet.
- Clone output should print the source snapshot, new service name, new storage
  location, and next command.

### VM Zvol Clone

For VM zvol snapshots:

- clone the selected zvol snapshot to a new zvol
- copy stable VM config: runtime, image metadata, shape, disk size, SSH user,
  guest metadata model, kernel/initrd/rootfs image refs where still relevant
- generate fresh runtime identity: service name, service root, Firecracker
  config path, sockets, PID file, systemd unit, tap/macvlan interface names,
  service network IP if needed, and MAC addresses where needed
- write VM metadata and Firecracker config for the new service
- install but do not start the VM systemd unit unless `--start` is passed

For a full VM checkpoint, clone defaults to disk clone. It should not claim to
resume memory/state unless full-state clone is explicitly designed later.

### ZFS Service-Root Clone

For ZFS-backed service-root services:

- clone the selected service-root dataset snapshot to a new dataset
- create a new service record for `<new-svc>`
- copy the deploy definition from the original service where safe
- rewrite service name and service root references
- install the new service definition
- leave it stopped unless `--start` is passed when that is possible for the
  service type

Service-root clone must account for generation state. The first implementation
should clone from the current deploy definition unless the recovery point
generation can be mapped to available generation artifacts safely. If it cannot
map the captured generation, it should say that it cloned the state with the
current service definition.

## Restore

`restore` is destructive in-place rollback. It must be conservative.

```bash
yeet snapshots restore <svc> <snapshot>
yeet snapshots restore <svc> <snapshot> --stop
yeet snapshots restore <svc> <snapshot> --stop --start
yeet snapshots restore <svc> <snapshot> --yes
yeet snapshots restore <svc> <snapshot> --generation=snapshot
yeet snapshots restore <vm> <snapshot> --mode=full
```

Common rules:

- The selected snapshot must be yeet-owned and belong to `<svc>`.
- The service must be stopped unless `--stop` is passed.
- Restore prompts for confirmation unless `--yes` is passed.
- Restore creates a pre-restore recovery point before mutating state whenever
  technically possible.
- Restore fails if pre-restore snapshot creation fails, unless a future
  explicitly named force flag is designed. This design does not include that
  force flag.
- Restore reports each action in order.
- Restore does not blindly restart after failure.
- Protected snapshots may be restored from; protection prevents pruning and
  deletion, not use. Confirmation should still mention protected status.

Expected output shape:

```text
Created pre-restore recovery point: ...
Stopped service: ...
Rolled back VM disk: ...
Restored Firecracker state: ...
Started service: ...
Restore complete.
```

### ZFS Service-Root Restore

For service-root datasets, restore rolls back the dataset to the selected
snapshot. The service must be stopped first. If the selected recovery point
records a generation and that generation is available, restore should offer or
perform a matching service generation rollback as part of a complete restore.
If the generation is not available, restore should continue only after making
clear that it is restoring state while leaving the current deployed definition.

Definition rollback must be explicit rather than implicit:

```bash
--generation=current|snapshot
```

Default is `current`, because state restore and definition rollback are separate
recovery axes. `--generation=snapshot` is valid only when the recovery point
records a generation and the service still has the needed generation artifacts.

### VM Disk Restore

For VM zvol snapshots, restore rolls back the VM disk zvol to the selected
snapshot. The VM must be stopped unless `--stop` is passed. Raw-disk VMs are
unsupported.

Disk restore is the default for VM recovery points, including full checkpoints,
unless `--mode=full` is specified.

### Full VM State Restore

Full restore is explicit:

```bash
yeet snapshots restore <vm> <snapshot> --mode=full
```

It restores disk plus Firecracker state and memory when safe.

Required checks:

- selected recovery point mode is `full`
- VM service is stopped
- checkpoint metadata exists and matches the selected zvol snapshot
- Firecracker state file exists
- Firecracker memory file exists
- current VM runtime is Firecracker
- current Firecracker binary/version is compatible with the checkpoint metadata
- VM config still matches the checkpoint enough to resume safely: CPU count,
  memory size, disk path, network device count/order, and machine config

New full checkpoints should write enough compatibility metadata to make these
checks possible: Firecracker binary identity or version, machine config,
network device summary, disk path, CPU count, memory size, and a VM config
fingerprint. Existing full checkpoints that lack compatibility metadata can
still be restored with `--mode=disk`, but must not be accepted for
`--mode=full`.

If compatibility cannot be proven, fail before mutation when possible. If disk
rollback has already happened and memory/state restore fails, print the
pre-restore recovery point prominently so the user can recover.

The implementation may discover that Firecracker runtime restore is not safe
enough for the first pass. If so, full-state restore must be explicitly recorded
as a non-goal with the exact blocker, not omitted accidentally.

## Safety Rules

- Never mutate a snapshot unless it is tagged as yeet-created for the target
  service.
- Never rollback or destroy a dataset/zvol unless it matches the recovery
  target derived from current service DB state.
- Use exact argv tests for `zfs clone`, `zfs rollback`, `zfs destroy`, and
  property writes.
- Create pre-restore snapshots before destructive restore. If the backend
  supports snapshots and pre-restore snapshot creation fails, restore must stop
  before mutation.
- Do not delete full checkpoint files except when deleting the owning recovery
  point or cleaning up after a failed full-checkpoint creation.
- Cleanup partial clone output where possible.
- If restore stopped a service and restore fails, do not auto-start it unless
  the service state is known to be safe and the user explicitly requested it.

## Documentation And UX

Docs must explain the distinction:

- generations roll back deployed definitions
- snapshots restore service data, VM disks, and VM runtime checkpoints

Required docs:

- CLI reference for `service rollback`, `service generations`, and every
  `snapshots` subcommand
- ZFS concept page with restore and clone examples
- VM payload page with disk vs full checkpoint restore
- operations workflow page showing safe clone-first recovery
- changelog when release is prepared

The web deploy UI should not imply that snapshot management is complete unless
the UI can reach the management flows. If the CLI ships first, website docs
should say snapshot recovery management is currently CLI-first.

## Coverage Matrix

| Area | Feature | Target status |
| --- | --- | --- |
| Service generations | `yeet service rollback <svc>` | Implement |
| Service generations | `yeet service generations <svc>` | Implement |
| Service generations | top-level `yeet rollback` | Remove |
| Service generations | VM generation rollback | Explicit non-goal |
| Snapshot management | list / inspect / create / protect / unprotect / rm | Keep and adjust |
| Snapshot management | VM generation display cleanup | Implement |
| Snapshot recovery | `snapshots clone` for VM zvol | Implement |
| Snapshot recovery | `snapshots clone` for ZFS service root | Implement |
| Snapshot recovery | `snapshots restore` for VM zvol disk | Implement |
| Snapshot recovery | `snapshots restore` for ZFS service root | Implement |
| Snapshot recovery | full VM disk + memory/state restore | Implement if safe; otherwise explicit non-goal with blocker |
| Safety | pre-restore recovery point | Implement |
| Safety | confirmation prompts and `--yes` | Implement |
| Safety | stopped-service guard, `--stop`, `--start` | Implement |
| Safety | yeet-owned target validation | Implement |
| Docs | generations vs snapshots explanation | Implement |
| Docs | CLI, ZFS, VM, operations pages | Implement |
| Verification | local unit and quality gates | Required |
| Verification | live VM zvol clone/restore smoke | Required |
| Verification | live service-root clone/restore smoke | Required |
| Verification | live full checkpoint restore smoke | Required if implemented |

## Implementation Waves

These waves are execution mechanics only. The product should not be called done
until the coverage matrix is closed.

1. CLI IA cleanup and service generation commands.
2. Recovery point model cleanup, including VM generation display and selector
   compatibility for existing `...-g0` snapshots.
3. `snapshots clone` for VM zvols and ZFS service roots.
4. `snapshots restore` for VM zvol disks and ZFS service roots.
5. Full VM state restore or explicit non-goal decision with evidence.
6. Docs, web copy alignment, local tests, live smoke tests, and release notes.

## Implementation Decisions To Confirm During Planning

- VM snapshot names should stop adding generation suffixes for new snapshots if
  selector compatibility and retention parsing remain straightforward.
  Otherwise keep names stable and hide VM generation in human output.
- Full-state restore compatibility should be implemented only after the
  checkpoint metadata above is available. Old checkpoints without that metadata
  are disk-restore-only.
- Clone must return stopped services by default. If a service type cannot be
  installed without starting and cannot be stopped cleanly after install, that
  service type is out of clone scope until its install path is improved.
