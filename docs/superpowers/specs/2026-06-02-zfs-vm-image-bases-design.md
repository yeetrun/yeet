# ZFS VM Image Bases Design

## Summary

Move ZFS-backed VM image bases out of individual service roots and into a
shared, same-pool image cache layout.

Today a ZFS VM created with:

```bash
yeet run devbox@yeet-pve1 vm://ubuntu/26.04 --service-root=flash/yeet/vms/devbox --zfs --disk=128g --net=lan
```

uses a base zvol under `flash/yeet/vms/devbox`. Removing the service with
`--clean-data` can remove that reusable base, so recreating the VM has to write
the root filesystem into a new zvol again. That makes `Preparing disk...` slow
and blurs ownership between service data and image cache data.

The new model creates one shared ZFS base zvol per pool and VM image version.
Each VM disk is a clone of that base snapshot. Existing VMs keep using the
image version they were created from. New VMs can use newer image versions
after the user accepts the normal image update prompt.

## Goals

- Make repeated ZFS VM creation fast after the first VM on a pool/image version.
- Keep ZFS clones on the same pool as the VM service root.
- Avoid host-level configuration for the normal case.
- Keep service cleanup from deleting shared image bases.
- Preserve existing VM behavior until the VM is explicitly removed or rebuilt.
- Make slow first-use ZFS work visible through useful progress messages.
- Support future VM image versions without mutating existing VMs.

## Non-Goals

- No automatic migration of existing running VMs to the new base layout.
- No prewarming command for ZFS bases.
- No automatic pruning of old shared image bases in the first implementation.
- No cross-pool ZFS clones.
- No automatic rebuild of existing VMs when a new VM image version appears.

## Storage Layout

For ZFS-backed VM service roots, catch derives the VM image base location from
the root dataset's pool. The pool is the first component of the service root
dataset.

Example:

```text
service root dataset: flash/yeet/vms/devbox
pool:                 flash
shared base root:     flash/yeet/vm-images
image base zvol:      flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root
base snapshot:        flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root@ubuntu-26.04-amd64-v1
VM disk clone:        flash/yeet/vms/devbox/vm/<short-id>/root
```

For a different pool, yeet creates an independent base on that pool:

```text
tank/apps/devbox -> tank/yeet/vm-images/<image-version>/root
```

The shared image base is image-cache state. The service root owns only the VM
clone dataset and service artifacts.

The raw-file VM disk backend keeps using the existing file cache under catch's
data root. This design changes only the ZFS zvol base placement.

## Deploy Flow

On `yeet run <name> vm://... --zfs`, the remote VM provision flow should:

1. Resolve the VM image request to a concrete manifest version.
2. If a newer image is available than the server-side file cache, prompt before
   creating the new VM.
3. Download the VM image only when the selected version is missing or stale.
4. Resolve the ZFS service root dataset and derive the pool-local shared base.
5. Check for the base snapshot for the selected image version.
6. If the snapshot is missing, create the base zvol, write the root filesystem
   into it, and snapshot it.
7. Clone the VM disk from the shared base snapshot.
8. Set the requested disk size and resize the filesystem.
9. Continue with metadata injection, Firecracker config, networking, systemd,
   and startup.

If the selected image file cache and shared ZFS base already exist, deploy
should skip image download and base creation. It should clone and resize only.

## Progress UI

The CLI should not print `Download VM image` when no image download happens.

Disk preparation should expose phase-level progress so a slow first use is not
opaque. The first ZFS VM on a pool/image may show:

```text
Preparing ZFS image base...
Writing image to ZFS base...
Cloning VM disk...
Expanding filesystem...
```

Later ZFS VMs using the same pool and image version should skip the base-writing
messages and show only the clone/resize work.

Errors should include the phase that failed and enough ZFS context to identify
the remaining dataset or snapshot.

## Image Versioning

Image versions are immutable. A VM cloned from
`ubuntu-26.04-amd64-v1@ubuntu-26.04-amd64-v1` stays tied to that base snapshot
until the VM is removed and recreated.

When a new image version is available, new VM creation should prompt the user to
update before deploying. If the user accepts, the new VM uses the newer file
cache and the corresponding shared ZFS base. If the user declines, the new VM
uses the currently cached image version.

Each image version has its own shared base zvol and snapshot. Old bases are left
in place until an explicit prune feature is added later.

## Cleanup

`yeet rm <vm> --clean-data` should destroy service-owned VM state only:

- the Firecracker/systemd artifacts for the service
- the VM clone dataset under the service root
- the service root dataset when it is safe and service-owned

It must not destroy shared image bases under `<pool>/yeet/vm-images`.

Cleanup must still understand the legacy per-service base layout. If an older
VM has a base under its service root, `--clean-data` may remove that legacy base
because it is service-owned in the old model. New VM creation should never place
shared image bases under service roots.

When ZFS cleanup fails because a dataset is busy or a snapshot still has clones,
the command should report what remains and leave enough information for a
follow-up cleanup. Removing the service from `yeet.toml` should remain a
separate confirmation and should not hide remote cleanup failures.

## Data Model

The VM disk plan should distinguish:

- `BaseDataset`: the shared image base zvol for ZFS disks
- `BaseSnapshot`: the immutable snapshot used for cloning
- `Path`: the service-owned VM clone dataset
- `ImageVersion`: the manifest version selected for this VM

The clone path can remain under the resolved service root dataset. The base
dataset should be derived from the service root pool and image version.

The catch DB should continue to store enough VM disk state to remove and inspect
the VM. Persisting the image version and clone dataset is sufficient for normal
cleanup. Persisting the base snapshot is useful for diagnostics and future prune
logic, but cleanup must not depend on deleting it.

## Legacy Behavior

Current VMs are not sacred and do not require migration. The implementation
should be compatible enough to remove or inspect them, then use the new shared
base layout for newly created ZFS VMs.

If an existing VM's clone originates from a service-root-scoped snapshot, keep
that VM running as-is. If it is removed with `--clean-data`, cleanup should tear
down the old service-root tree as far as ZFS allows.

## Error Handling

ZFS command failures should include stderr when available and should identify
whether the failing object is a shared image base, shared snapshot, or
service-owned VM clone.

Useful failures include:

```text
failed to prepare ZFS image base "flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root": <zfs stderr>
failed to clone VM disk from "flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root@ubuntu-26.04-amd64-v1": <zfs stderr>
failed to destroy VM disk dataset "flash/yeet/vms/devbox/vm/d-ea1055/root": <zfs stderr>
```

The deploy path should fail rather than silently falling back to raw disks when
the user requested `--zfs`.

## Tests

Add focused unit tests for:

- deriving `flash/yeet/vm-images/<version>/root` from
  `flash/yeet/vms/devbox`
- deriving an independent base for another pool
- keeping raw backend behavior unchanged
- skipping ZFS base steps when the shared snapshot exists
- running ZFS base steps only when the shared snapshot is missing
- clone step generation against the shared snapshot
- cleanup preserving shared image bases
- cleanup still handling legacy service-root-scoped bases
- progress rendering for download, base preparation, clone, and resize phases
- update prompting before creating a new VM when a newer image version exists

Run targeted package tests for `pkg/catch` and any touched CLI/client packages.
Before merging implementation, run the full Go suite and an end-to-end ZFS VM
deploy on `yeet-pve1` that verifies:

- first ZFS VM on the pool creates the shared base
- second ZFS VM on the same pool does not rewrite the image base
- `yeet rm --clean-data` removes the VM clone without deleting the shared base
- recreating the VM no longer spends time writing the base image

## Acceptance Criteria

- New ZFS VMs use shared same-pool image bases.
- Repeated ZFS VM creation from the same image version skips base-image writes.
- Image download progress appears only when an image is actually downloaded.
- Existing VMs keep their original image version.
- Service cleanup does not delete shared image bases.
- Legacy per-service ZFS bases can still be cleaned up when the old VM is
  removed.
- User-facing errors make failed ZFS cleanup or deploy work recoverable.
