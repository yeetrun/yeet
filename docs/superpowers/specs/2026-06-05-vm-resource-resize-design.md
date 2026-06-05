# VM Resource Resize Design

## Summary

Add post-create VM configuration changes through `yeet service set`.

VMs are yeet services, and CPU, memory, disk, and network settings are
persisted service configuration. The first implementation should extend the
existing service settings command instead of adding a separate VM command tree.
VM-specific settings apply only to VM services and require the VM to be stopped.

Example workflow:

```bash
yeet stop devbox
yeet service set devbox --cpus=8 --memory=8g
yeet service set devbox --disk=128g
yeet service set devbox --net=lan --macvlan-parent=vmbr0
yeet start devbox
```

## Goals

- Allow existing VMs to change vCPU count, memory, disk size, and networking.
- Keep VM mutations robust by requiring the VM to be stopped.
- Support raw-file and ZVOL-backed disk growth.
- Reject disk shrink with a clear error.
- Replace VM network configuration safely while stopped.
- Keep catch DB, Firecracker config, guest metadata, and local `yeet.toml`
  replay settings in sync.
- Preserve existing `service set` behavior for service roots, snapshots, and
  Docker published ports.

## Non-Goals

- No hotplug or live VM mutation.
- No disk shrink.
- No automatic stop/start or restart flag in the first implementation.
- No migration between raw and ZVOL disk backends.
- No VM image replacement or rebuild as part of resource resizing.
- No new `yeet vm resize` command in the first implementation.

## Command Surface

Extend `yeet service set <svc>` with VM-specific flags:

```text
--cpus=N
--memory=SIZE
--disk=SIZE
--net=svc|lan|svc,lan
--macvlan-parent=IFACE
--macvlan-vlan=N
--macvlan-mac=MAC
```

These flags are meaningful only for VM services. If a non-VM service receives
one of them, catch should return a clear error such as:

```text
service "api" is not a VM service
```

Omitted flags mean no change. `--net` needs an explicit parser-level
`NetworkChange` marker because an omitted network flag is different from the
VM create default of `svc`.

`yeet vm resize` can be added later as an alias if the command ergonomics feel
worth it, but it should not be necessary for the first pass.

## Stopped-Only Mutation

All VM resource changes require the VM service to be stopped. Firecracker shape
and network devices are defined before process start, and offline disk growth
keeps filesystem repair and resize deterministic.

If the VM is running, `service set` should fail before changing DB state or
host artifacts:

```text
cannot change VM settings while "devbox" is running; stop it first
```

Snapshot-only `service set` should keep its current behavior and should not
start requiring a stopped service.

## CPU And Memory

CPU and memory changes update `db.Service.VM.CPUs` and
`db.Service.VM.MemoryBytes`, then rewrite the VM Firecracker config with the new
machine config. Validation should match VM creation:

- CPU count must be positive.
- Memory size must parse with existing VM size parsing.
- Memory admission should use the existing host budget check.

The systemd unit does not need to change for CPU or memory.

## Disk Growth

Disk changes are grow-only. The current persisted `VM.Disk.Bytes` is the source
of truth for validating the requested size.

For raw disks:

1. Validate requested size is greater than or equal to current size.
2. Resize the raw sparse file with `qemu-img resize`.
3. Run `e2fsck -pf` against the disk path.
4. Run `resize2fs` against the disk path.
5. Update `VM.Disk.Bytes`.

For ZVOL disks:

1. Validate requested size is greater than or equal to current size.
2. Set the clone zvol `volsize` to the requested size.
3. Run `udevadm settle`.
4. Run `e2fsck -pf` against `/dev/zvol/<dataset>`.
5. Run `resize2fs` against `/dev/zvol/<dataset>`.
6. Update `VM.Disk.Bytes`.

No-op requests should be accepted without running disk commands.

If filesystem repair or resize fails, report the failing phase and leave DB
size unchanged. The VM remains stopped for manual recovery.

## Network Replacement

Network changes replace the VM's configured interfaces while stopped.

The implementation should reconstruct an old `vmNetworkPlan` from the stored
`VM.Networks` data and execute cleanup commands. Then it should build a new
network plan from the requested service-set flags, execute setup commands, and
persist the new DB networks.

For `svc` networking, yeet should reuse the existing reserved service IP when
one is already present and reserve one if the VM did not previously have one.
For `lan`, yeet should use the requested parent/MAC/VLAN flags and the same
validation rules as VM creation. Unsupported modes still fail; VMs support
`svc` and `lan`.

Network replacement must also rewrite guest metadata and Firecracker config so
the next VM start sees the new interface list and boot arguments.

## Artifact Rewrite

After any VM resource change, catch should rewrite the durable host artifacts
that depend on the changed settings:

- Firecracker JSON for CPU, memory, disk path, boot args, and interfaces.
- Guest metadata for network definitions.
- Injected rootfs metadata when network metadata changes.

The implementation should reuse existing rendering helpers where possible.
If a resource change does not affect metadata, avoid remounting and injecting
the rootfs.

## Local Config Sync

`yeet service set` already updates a matching local `yeet.toml` entry after the
remote command succeeds. VM resource flags should join that path.

The local entry should keep using stored run args rather than new top-level TOML
fields. Updating service settings should rewrite only the VM control flags:

- replace or add `--cpus`
- replace or add `--memory`
- replace or add `--disk`
- replace or add `--net`
- replace or add LAN-related flags

Payload args after `--` must be preserved. Existing service-root, publish, and
snapshot config behavior should remain unchanged.

If no local matching entry exists, the existing sync hint behavior is enough.

## Error Handling

Important errors should be explicit and actionable:

- Missing service: `service "devbox" not found`
- Wrong service type: `service "api" is not a VM service`
- Running VM: `cannot change VM settings while "devbox" is running; stop it first`
- Disk shrink: `VM disk shrink is not supported`
- Invalid size: use the existing VM size parser error
- Unsupported network mode: use the existing VM network validation error
- Disk command failure: include command output and the disk phase
- Network cleanup/setup failure: include the failing command

The command should avoid committing partial DB changes. Host artifacts may be
partially changed by failed external commands, but DB state should remain the
previous known-good VM configuration unless the full mutation succeeds.

## Tests

Add focused tests for:

- parsing new `service set` VM flags, including `NetworkChange`.
- CLI bridge behavior for VM flags before and after the service name.
- `service set` rejecting VM flags on non-VM services.
- stopped-only enforcement for VM resource changes.
- CPU and memory DB updates and Firecracker config rewrite.
- disk shrink rejection.
- raw disk growth command plan.
- ZVOL disk growth command plan.
- no-op disk resize skipping external disk commands.
- network replacement cleaning old devices, setting up new devices, and
  persisting new DB networks.
- service-set local config rewriting VM run flags while preserving payload args.
- existing root, publish, and snapshot service-set tests remaining unchanged.

Run targeted package tests for:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch
```

Before merging, run the full Go suite:

```bash
go test ./...
```

## Live Verification

Use a disposable VM on lab-host:

```bash
yeet run trash-vm@yeet-lab vm://ubuntu/26.04 --service-root=flash/yeet/vms/trash-vm --zfs --disk=8g --net=svc
yeet stop trash-vm@yeet-lab
yeet service set trash-vm@yeet-lab --cpus=2 --memory=2g --disk=16g
yeet start trash-vm@yeet-lab
yeet info trash-vm@yeet-lab
yeet ssh trash-vm@yeet-lab -- df -h /
yeet stop trash-vm@yeet-lab
yeet service set trash-vm@yeet-lab --net=lan
yeet start trash-vm@yeet-lab
yeet info trash-vm@yeet-lab
yeet rm trash-vm@yeet-lab --clean-data --clean-config
```

Success means info reports the new CPU, memory, disk, and network values; the
guest filesystem grows to the requested size; and the VM starts cleanly after
each mutation.

## Acceptance Criteria

- Existing VMs can change CPU, memory, grow disk, and replace networking with
  `yeet service set`.
- VM resource changes fail cleanly while the VM is running.
- Disk shrink is rejected.
- Raw and ZVOL disk growth both work.
- Firecracker config, guest metadata, catch DB, and local `yeet.toml` stay in
  sync after successful changes.
- Existing non-VM service-set behavior is unchanged.
- Unit tests and live lab-host verification pass.
