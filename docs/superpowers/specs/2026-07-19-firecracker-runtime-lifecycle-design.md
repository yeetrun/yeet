# Firecracker Runtime Lifecycle Design

Date: 2026-07-19

## Status

Approved architecture, awaiting written-spec review.

This design assumes the jailer-only runtime on Yeet `main`: every Firecracker
process launches through the matching jailer and runs as the host `yeet-vm`
account. There is no legacy-root fallback.

This design also removes Yeet's persistent and transient Firecracker memory
snapshot support. VM recovery points are ZFS disk snapshots only.

## Summary

Yeet will publish and manage three independent VM artifact types:

1. a guest base/root filesystem;
2. a guest kernel;
3. a host Firecracker runtime containing an exact Firecracker and jailer pair.

Independently published artifacts do not imply identical upgrade behavior.
Guest package upgrades mutate the active guest disk through the guest's normal
package manager. Guest kernel changes use Yeet's existing guest-selection and
host kernel-sync path on reboot. Host Firecracker runtime changes use a trusted
Catch transaction and never take instructions or artifacts from the guest.

Each VM records one immutable composition lock containing the exact guest base,
active kernel, and host runtime identities it is configured to use. Mutable
catalogs answer which artifacts are currently promoted; the composition lock
answers what a particular VM actually uses.

Normal Catch upgrades may download, validate, inventory, and stage runtime
material, but they do not restart running VMs. The default runtime policy is
manual. An operator may opt into staging a promoted runtime for application on
the VM's next cold start or restart.

## Goals

- Ship Firecracker security and correctness updates without rebuilding guest
  root filesystems or waiting for guest image adoption.
- Keep Firecracker and its matching jailer inseparable throughout publishing,
  caching, selection, launch, rollback, and pruning.
- Preserve immutable, reproducible identities for every configured VM
  component.
- Adopt existing monolithic bundles without restarting their VMs or replacing
  their active disks.
- Support the default `/var/lib/yeet` layout, custom Catch data roots, custom
  service roots, raw disks, and ZFS-backed service and VM storage.
- Keep normal Catch upgrades free of unexpected VM restarts.
- Provide deliberate candidate testing, canarying, and promotion instead of
  moving newly discovered upstream releases directly into production use.
- Remove Firecracker memory snapshot creation, storage, restoration,
  compatibility, and retention from Yeet.
- Keep ZFS disk recovery points useful and simple.

## Non-goals

- Do not change users or package management inside VM guests.
- Do not redesign native-service `--run-as`.
- Do not add a legacy-root Firecracker launch path.
- Do not make Firecracker and jailer independently selectable.
- Do not replace the active root filesystem of an existing VM when a newer
  guest base is published.
- Do not let a guest authorize a host runtime download, switch, restart,
  rollback, or policy change.
- Do not add automatic VM restarts for runtime maintenance.
- Do not provide persistent or resumable Firecracker memory/VMM-state
  checkpoints.
- Do not provide application-consistent VM disk snapshots in this change.
- Do not add a new snapshot backend for raw disks.
- Do not add per-VM host accounts. The shared `yeet-vm` account remains the
  supported runtime identity.

## Current State

Official VM releases are monolithic bundles containing a guest rootfs, guest
kernel, Firecracker, jailer, configuration metadata, and checksums. The mutable
family latest aliases point to immutable bundle versions. Catch caches bundles
under its configured data root and pins an existing VM through
`Service.VM.Image.Version` and paths stored in the database.

Provisioning clones or copies the bundle rootfs into the active VM disk. The
active disk then evolves independently. Updating the image catalog or cache
does not change an existing VM.

Generated VM units currently derive Firecracker and jailer as siblings of the
stored image rootfs path. Existing jailer-only Catch upgrade code inventories
those paths, obtains a matching jailer for legacy bundles, stages units,
atomically replaces them, rolls back failed unit publication, and leaves
running VM processes untouched.

Guest kernels already have a partially independent operational lifecycle. On
a guest-requested reboot, Catch mounts the disk read-only, validates the guest
kernel selection journal and selected paths, copies the kernel into
host-controlled storage, updates the Firecracker configuration and database,
and lets systemd restart the VM.

The image repository already publishes canonical kernel releases with source,
configuration, build fingerprint, and asset checksums. Its scheduled stable
kernel workflow still rebuilds complete Ubuntu and NixOS bundles and injects a
hard-coded Firecracker version.

Yeet's released snapshot implementation supports persistent Firecracker state
and memory files, but this design removes that feature. The current disk-only
path also creates and deletes a temporary full Firecracker snapshot to flush
block backing files. That internal memory snapshot operation is removed too.

## Lifecycle Boundaries

### Guest base/root filesystem

The guest base is an immutable provisioning artifact. Its manifest describes
the rootfs, distro, image profile, default guest user, guest initialization and
metadata protocols, expected architecture, compatibility requirements,
checksums, and provenance.

Publishing a new guest base affects new VM resolution and explicit recreate or
migration workflows. It never rewrites an existing active VM disk. Ubuntu
package upgrades and NixOS rebuilds remain guest operations.

### Guest kernel

The guest kernel is an immutable, independently published artifact. The
existing canonical kernel manifest is the starting contract. It records the
upstream version and source digest, kernel configuration, build fingerprint,
architecture, Yeet packaging revision, kernel and configuration digests, and
publisher provenance.

New VMs resolve a promoted kernel into their composition lock. Existing VMs
continue to update their active kernel through the guest selection journal and
host synchronization on reboot. The host treats guest selection as untrusted
input: paths and digests are validated, the disk is mounted read-only, and the
selected kernel is copied into host-controlled storage before launch.

### Host Firecracker runtime

A host runtime is one immutable artifact containing exactly one Firecracker
binary and its matching jailer. The pair has one identity and cannot be mixed
with components from another runtime release.

Runtime upgrades are Catch operations. The guest cannot select a runtime,
channel, URL, digest, path, or restart behavior.

## Artifact Identities and Manifests

`yeet-vm-images` remains the sole publisher of official guest, kernel, and
runtime artifacts and catalogs.

### Guest base manifest

The guest base manifest contains:

- manifest schema version;
- immutable guest base ID and packaging revision;
- architecture, distro, distro version, and image profile;
- rootfs asset name, size, digest, and filesystem expectations;
- guest init, agent protocol, metadata driver, and default-user metadata;
- source and build provenance;
- compatibility requirements for supported kernel and runtime capabilities.

It does not contain Firecracker or jailer binaries.

### Kernel manifest

The kernel manifest contains:

- manifest schema version;
- immutable kernel release ID and Yeet packaging revision;
- architecture and upstream kernel version;
- source URL and digest;
- kernel configuration URL and digest;
- build fingerprint and publisher commit;
- kernel image and configuration asset digests;
- compatibility metadata needed by guest bases or the host launcher.

### Runtime manifest

The runtime manifest contains:

- manifest schema version;
- immutable Yeet runtime ID, such as
  `firecracker-v1.16.1-yeet-v1`;
- architecture;
- upstream Firecracker version, tag, and resolved commit;
- upstream release archive URL and SHA-256;
- upstream checksum-sidecar URL and value;
- Firecracker asset name and SHA-256;
- jailer asset name and SHA-256;
- exact expected output from both version probes;
- upstream tag-signature verification information when available;
- ingest workflow, repository, and commit provenance;
- production-release and default-seccomp classification.

Promotion state is not embedded in the immutable manifest. Integration and
canary attestations belong to the promotion record that points at the immutable
manifest, because those attestations are produced after candidate publication.

### Per-VM composition lock

Catch stores exact component identities for each VM:

- guest base ID, manifest digest, source, and active rootfs provenance;
- active kernel release, digest, and host path;
- configured runtime ID, manifest digest, Firecracker digest, jailer digest,
  and host path;
- runtime policy and channel;
- staged, previous, and trial runtime state where applicable.

The existing `VM.Runtime` string continues to identify the provider
(`firecracker`). The component record uses a distinct VMM-runtime field rather
than overloading that provider field.

The database is authoritative. Systemd units and root-owned runtime descriptor
files are derived artifacts that Catch can reconcile from the database and
transaction journal.

## Catalogs and Promotion

The existing guest family catalog remains the entry point for `vm://...`
payloads. It may reference versioned guest, kernel, and runtime subcatalogs
rather than expanding one mutable document indefinitely.

Catalog responsibilities are:

- guest catalog: promoted guest base per payload family;
- kernel catalog: promoted canonical kernel per architecture and supported
  compatibility track;
- runtime catalog: detected candidates, promoted stable runtime, support/EOL
  metadata, revoked releases, and immutable manifest URLs.

A candidate release is never promoted merely because discovery and static
verification succeeded. Stable promotion changes a catalog pointer through a
reviewed action. It does not mutate or republish the immutable artifact.

Promoted immutable release tags cannot be overwritten. A changed ingest,
manifest, or verification result requires a new Yeet packaging revision.

## Firecracker Release Discovery and Verification

A scheduled workflow in `yeet-vm-images` queries the official
`firecracker-microvm/firecracker` GitHub Releases API. It accepts only releases
that are not drafts or prereleases and whose tags exactly match stable semantic
versions. It rejects unexpected repositories, asset names, architectures, and
version shapes.

Discovery creates an unpromoted candidate and performs these checks:

1. download the expected upstream archive and checksum sidecar;
2. require the downloaded archive digest to match the sidecar and GitHub
   release-asset digest;
3. resolve and record the tag's exact commit;
4. verify the tag signature against maintained trusted signer information when
   the tag is signed;
5. require explicit human approval for a missing signature or unknown signer
   rotation;
6. inspect the archive as untrusted input and accept only the expected regular
   Firecracker and jailer files;
7. verify upstream internal checksums;
8. verify ELF architecture and executable shape;
9. run Firecracker and jailer version probes and require the exact same
   upstream version;
10. record final component digests and provenance in an immutable runtime
    manifest;
11. publish the candidate without changing the stable catalog.

The workflow ingests upstream release binaries rather than rebuilding
Firecracker. Yeet's provenance must say that explicitly.

Firecracker's release support policy is also imported into runtime status. A
runtime may be usable while unsupported, but unsupported/EOL state is always
visible and never described as current.

## Runtime Cache and Trusted Paths

Official runtimes are cached under the configured Catch data root, for example:

```text
<catch-data-root>/vm-runtimes/<architecture>/<runtime-id>/<manifest-digest>/
```

The cache contains the manifest, Firecracker, and jailer. It is independent of
service roots and guest image cache directories.

Every download is staged into a private temporary directory, verified before
publication, fsynced as appropriate, and atomically renamed into its immutable
final path. Published cache entries are root-owned, are not group/other
writable, and contain no symlinked runtime inputs.

Every launch reuses the existing trusted-path and matching-version checks.
Jailer launches as root only long enough to build the jail and drop Firecracker
to `yeet-vm`.

Custom runtimes are imported explicitly through the host control plane. They
must satisfy the same pair, version, architecture, ownership, permissions, and
digest rules. An imported/custom guest bundle is not silently switched to an
official runtime.

## Existing VM Adoption

Adoption is logical before it is physical. It does not replace an active disk
or restart a VM.

For each existing VM, Catch:

1. resolves the effective service root, configured Catch data root, stored
   image paths, and installed systemd unit;
2. discovers the exact Firecracker and jailer used for the next launch;
3. validates the pair, architecture, ownership, permissions, and version;
4. hashes the binaries and synthesizes a legacy runtime identity;
5. records logical guest base, kernel, and runtime component identities;
6. renders the unit from the new configured runtime selection;
7. stages all database and unit changes before committing any of them;
8. publishes the transaction without stopping or restarting the VM.

A legacy monolithic directory may initially back all three logical component
references. It is not deleted until component-aware pruning proves that no
guest base, kernel, runtime, ZFS clone, or active transaction references it.

Official legacy runtimes may later be materialized into the runtime cache by
hard link, reflink, or verified copy when filesystem and ownership constraints
permit. Physical deduplication is not required for correctness.

Existing v1.14.3 runtimes are adopted as-is and reported as EOL with the
promoted stable upgrade available. Adoption alone does not stage the upgrade.

## Runtime Policy and Commands

The supported policy values are:

- `manual`: report availability; change nothing until an operator asks;
- `stage-on-restart`: download and stage the promoted stable runtime, but do
  not restart a running VM. Apply it only on a later cold start or restart.

There is no automatic-restart policy.

The CLI surface is:

```text
yeet vm runtime status [<vm>]
yeet vm runtime update
yeet vm runtime import <name> <dir>
yeet vm runtime upgrade <vm> [--to VERSION] [--channel stable|candidate] [--restart]
yeet vm runtime rollback <vm> [--restart]
yeet vm runtime policy defaults show
yeet vm runtime policy defaults set manual|stage-on-restart [--channel stable|candidate]
yeet vm runtime policy <vm> inherit|manual|stage-on-restart [--channel stable|candidate]
yeet vm runtime prune [--dry-run]
```

`yeet vm runtime update` refreshes the trusted runtime catalog and cache only.
It does not select a runtime for a VM. `yeet vm images update` retains its
guest-image-only meaning.

For a running VM, `runtime upgrade` stages the requested promoted runtime by
default. `--restart` explicitly authorizes downtime and a trial boot. For a
stopped VM, the command updates the configured runtime but does not implicitly
start the VM.

The host default is `manual` on the stable channel. Each VM inherits that
default unless it has an explicit override. Candidate-channel use is always an
operator opt-in and is intended for canary hosts or VMs.

Observation requires `read`. Runtime update, upgrade, rollback, policy, import,
and prune operations require `manage`.

## Upgrade Transaction

Runtime mutation uses one Catch transaction engine for manual upgrades,
policy-driven staging, first adoption, rollback, and recovery after
interruption.

The durable state distinguishes:

- running runtime: the identity of the current Firecracker process, when one
  exists;
- configured runtime: the identity the next normal launch will use;
- staged runtime: a verified candidate waiting for the next launch;
- previous runtime: the last known-good launcher retained for rollback;
- trial state: prepared, starting, healthy, failed, or rolled back.

The transaction performs:

1. resolve only from a trusted catalog or explicit host import;
2. download and fully verify the immutable runtime pair;
3. validate host architecture, jailer requirements, paths, disk, network, and
   generated launch configuration;
4. acquire the Catch/VM mutation lock;
5. record previous, configured, staged, and transaction identities durably;
6. stage root-owned descriptor and systemd unit replacements;
7. revalidate source inodes and digests immediately before publication;
8. atomically publish derived files and run one `systemctl daemon-reload`;
9. commit database state or restore the previous generation on failure;
10. leave a reconciliable transaction journal until the trial is resolved.

Rewriting the unit does not affect an already running Firecracker process.

On a trial start, the normal jailer preflight runs again. Host-side readiness
requires the expected Firecracker/jailer identities, a stable systemd runner,
the jailer isolation marker, and no immediate process failure. Guest-agent SSH
readiness may be used as a liveness signal, but never as authorization or proof
that the guest is trustworthy.

If the trial fails before readiness, Catch restores the previous configured
runtime and unit and allows systemd to start the previous pair. The failed
candidate remains cached for diagnosis unless it is explicitly pruned or
revoked.

## Rollback and Disk Recovery

Runtime rollback and guest-disk rollback are separate operations.

Launcher rollback is available for every VM: restore the previous exact
Firecracker+jailer selection and derived unit. A failed candidate boot uses
the same active guest disk unless a disk restore is explicitly performed.

For a ZFS zvol-backed VM, an explicit `--restart` runtime upgrade creates a
protected pre-upgrade disk recovery point before stopping the VM. After a
successful readiness trial, Catch unprotects that recovery point and ordinary
snapshot retention applies. If the trial fails, the recovery point remains
protected and is reported for explicit operator disposition. If the candidate
boot modifies the guest disk before failing, the operator may restore that disk
recovery point separately.

Raw disks have no Yeet snapshot backend. Runtime rollback restores the
launcher only and preserves guest disk writes. This limitation is shown before
an explicit restart upgrade when no disk recovery point can be created.

## Disk-Only Snapshot Semantics

Yeet supports VM disk recovery points only for ZFS zvol-backed disks.

For a running VM, creation performs:

1. pause the VM through the Firecracker API;
2. create one atomic ZFS snapshot of the VM zvol;
3. resume the VM through a cancellation-resistant recovery context;
4. apply retention and protection policy.

The operation does not call Firecracker `CreateSnapshot`, does not create a
memory file, and does not create a VMM-state file.

The recovery point is crash-consistent, not application-consistent. Pausing
stops new guest CPU execution, while the ZFS snapshot captures one atomic disk
state. Yeet does not claim that guest filesystems, databases, or applications
were quiesced. A future application-consistent feature would require an
explicit, separately designed guest quiesce contract.

The user-visible snapshot surface removes:

- `yeet snapshots create --full`;
- `yeet snapshots restore --mode=full`;
- the VM restore `--mode` flag, because disk is the only supported VM mode;
- memory/state paths from recovery-point table and JSON output.

Disk create, list, inspect, protect, unprotect, clone, restore, remove, and
retention remain supported.

The Firecracker jail no longer binds or delegates a service-data checkpoint
directory. Firecracker always launches with its normal configuration file;
there is no restore mode, restore request, restore result, or restore-specific
systemd exit status.

## Legacy Memory Checkpoints

Memory checkpoint support has shipped in previous Yeet releases, so removal
must not silently delete possible operator data even though no current use is
known.

Before rollout, live validation inventories:

- ZFS recovery points with `com.yeetrun:checkpoint=full`;
- `<service-data>/checkpoints` directories;
- Firecracker state, memory, and metadata files.

If the inventory is empty, no compatibility path is needed.

If any legacy entries exist, rollout to that host is blocked. The operator must
explicitly archive or remove the memory and VMM-state files and either remove
the associated recovery point or retag its retained ZFS snapshot as disk-only.
Catch does not perform automatic destructive cleanup. The inventory is rerun,
and rollout proceeds only when no memory checkpoint directories or `full` tags
remain.

There is no ongoing compatibility reader, status mode, restore path, or runtime
pin for legacy memory checkpoints after rollout.

## Runtime Retention and Pruning

A runtime cache entry is retained while referenced by any of:

- a running VM;
- a configured VM;
- a staged VM;
- a previous/rollback VM selection still inside its retention window;
- an active or interrupted runtime transaction;
- the current promoted stable catalog entry;
- an explicit operator protection.

Memory checkpoints do not participate in runtime retention.

Pruning is component-aware. It must not delete a monolithic legacy directory
until all logical guest-base, kernel, runtime, ZFS-base, and transaction
references are gone. Unknown or malformed references fail closed and keep the
artifact.

`yeet vm images prune` continues to manage guest-image and ZFS-base material.
`yeet vm runtime prune` manages host runtime material. Kernel pruning remains
bound to active kernel references and the kernel synchronization lifecycle.

## Guest/Vsock Trust Boundary

The guest is untrusted. The existing vsock transport associates a Unix socket
with one VM, but it does not authenticate the guest agent binary or guest root.
Any sufficiently privileged guest process can imitate agent responses.

The host may query the guest for network or readiness information. Those
responses are liveness and discovery hints only.

The first runtime lifecycle version adds no guest-to-host runtime request.
Specifically, the guest cannot provide or influence:

- runtime URL, version, digest, path, or binary;
- runtime channel or promotion state;
- upgrade or rollback authorization;
- host cache pruning;
- VM restart timing;
- host runtime policy.

A natural guest reboot may cause systemd to apply a runtime that the trusted
host has already staged. Discovery and download do not occur in the reboot
critical path.

## Status and Operator Experience

Runtime status reports, for each VM:

- running Firecracker version and short digest, when discoverable;
- configured Firecracker version and short digest;
- staged and previous versions;
- exact jailer match and jailer-isolation readiness;
- policy and selected catalog channel;
- latest promoted runtime;
- current, update-available, unsupported, EOL, revoked, trial, failed, or
  rollback state;
- last transition result and actionable remediation.

Jailer isolation readiness and runtime currency remain distinct fields. A VM
can be correctly jailed while using an EOL runtime.

Errors identify the VM, component identity, failed invariant, and safe next
action. No error branch launches Firecracker directly or silently switches an
imported runtime to an official one.

## Integration, Canary, and Promotion

Candidate runtime integration requires a Linux KVM environment representative
of production. Static workflow validation alone cannot promote a runtime.

The integration matrix covers:

- exact Firecracker/jailer version and digest matching;
- trusted-path, ownership, permissions, symlink, and architecture failures;
- jailer launch and drop to `yeet-vm`;
- current Ubuntu and NixOS guest bases;
- current and previous promoted kernels;
- raw and ZFS zvol disks;
- default and custom Catch data roots;
- default, custom, and ZFS-backed service roots;
- service, LAN, and Tailscale networking;
- guest reboot and host kernel synchronization;
- crash-consistent disk snapshot create, restore, clone, protection, and
  pruning;
- running-VM staging without restart;
- stopped-VM selection without implicit start;
- explicit restart trial success;
- failed trial launcher rollback;
- Catch interruption at each transaction publication boundary;
- catalog, manifest, archive, component-digest, and signer failures;
- imported/custom runtime behavior;
- legacy monolithic adoption and component-aware pruning.

The matrix deliberately excludes Firecracker memory snapshot creation and
restore.

After integration passes, a candidate enters an opt-in canary catalog channel.
Canary validation performs repeated boot, natural reboot, kernel sync, disk
snapshot/restore, networking, explicit runtime rollback, and Catch restart
cycles on production-like hosts. Promotion is a reviewed action that records
the candidate manifest and integration/canary evidence.

A security emergency may shorten the soak through an explicit reviewed
override. It does not bypass artifact verification or silently restart VMs.

Revoking a promoted runtime prevents new selection and raises status on
existing users. Revocation does not kill or restart a running VM.

## Delivery Boundaries

The architecture can be delivered in approval-gated slices:

1. remove Firecracker memory snapshot/create/restore support and establish
   disk-only recovery semantics;
2. add component identities, dual-read legacy adoption, runtime manifests,
   cache, and read-only status;
3. publish verified runtime candidates without stable promotion;
4. add manual runtime stage, apply, trial, and rollback transactions;
5. validate and adopt existing VMs without restart;
6. add the opt-in `stage-on-restart` policy;
7. stop embedding runtime binaries in newly published guest bases;
8. complete independent guest-base/kernel resolution and component-aware
   pruning;
9. enable deliberate stable promotion after KVM integration and canary proof.

Each slice keeps legacy monolithic bundles bootable through the jailer until
the composition lock and runtime cache have adopted them.

## Repository Ownership and Likely Overlap

The Yeet repository owns:

- database schema, migration, clones, and generated views;
- Catch runtime catalog/cache consumption;
- existing-VM inventory and adoption;
- systemd rendering and unit transactions;
- jailer validation, preparation, readiness, and launch;
- runtime transaction journal, apply, trial, rollback, status, and pruning;
- provisioning and composition-lock persistence;
- guest kernel synchronization integration;
- disk-only recovery points and removal of memory snapshot support;
- RPC types, TTY authorization, CLI parsing/routing/help, and operator output;
- custom data/service-root migration and path rewriting;
- local non-authoritative `tools/vm-image` contract fixtures;
- user documentation and release notes when implementation lands.

The `yeet-vm-images` repository owns:

- guest base, kernel, and runtime manifest schemas;
- official catalogs and promotion state;
- Firecracker release discovery and verification;
- immutable runtime ingest and publication;
- canonical kernel publication;
- guest image builders after component separation;
- KVM integration workflow inputs and published attestations;
- candidate, canary, promotion, and revocation workflows;
- release publishing helpers and immutable-tag enforcement.

The existing jailer-only Catch upgrade transaction is reused rather than
replaced. Runtime selection becomes a new source for the exact pair that the
same trusted launch path validates.

## Upstream Constraints

This design follows Firecracker's documented constraints:

- semantic Firecracker versions and finite supported release lines;
- patch releases are recommended for critical and security fixes;
- production launch through the jailer included with the release or equally
  restrictive constraints;
- trusted, non-user-writable jailer inputs;
- production release binaries with default restrictive seccomp filters;
- guests treated as untrusted workloads;
- disk state managed independently from Firecracker memory/VMM state.

Primary references:

- [Firecracker release policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md)
- [Firecracker v1.16.1 release](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.16.1)
- [Firecracker production host setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)
- [Firecracker jailer operations](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)
- [Firecracker seccomp](https://github.com/firecracker-microvm/firecracker/blob/main/docs/seccomp.md)
- [Firecracker snapshot support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)
- [Firecracker v1.16.1 API](https://github.com/firecracker-microvm/firecracker/blob/v1.16.1/src/firecracker/swagger/firecracker.yaml)
