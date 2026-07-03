# Complete Host Storage Migration

## Summary

`yeet host set` should complete a host storage migration, not only change the
catch unit and selected service roots. A successful migration must leave the
host in a coherent steady state: catch runs from the requested roots, affected
services have been restarted from regenerated units, the current catch database
does not reference old roots for active state, installed systemd units do not
reference old roots, and the local `yeet.toml` is updated without unnecessary
per-service pins.

The live `yeet-pve1` migration showed the gap. The requested data dir and
services root moved to `/flash/yeet/data` and `/flash/yeet/services`, and catch
was moved under `/flash/yeet/services/catch`, but `/root/data` remained active
through generated namespace units, VM units, VM image paths, tailscale artifact
refs, and old catch artifact refs in `db.json`.

## Goals

- Extend host storage planning so it discovers every active reference to the old
  catch data dir, services root, catch service root, and moved service roots.
- Rewrite current `db.json` references during a data-dir move.
- Rewrite copied generated artifacts that contain old absolute roots.
- Reinstall or regenerate systemd, network namespace, tailscale, and VM units
  whose contents depend on moved roots.
- Restart all affected non-catch services after their roots, artifacts, or
  installed units are rewritten.
- Reinstall and restart catch after the target database and units are ready.
- Make `yeet host set` repair-aware: if the desired roots already match but old
  active references remain, the command should plan and apply a repair instead
  of reporting no-op.
- When the requested services root is a ZFS dataset, put each migrated service
  root on its own child dataset under that services-root dataset, including the
  `catch` service itself.
- Update local `yeet.toml` so migrated services that now match the host default
  services root are unpinned where possible.
- Add a post-apply validation step that proves no active references to the old
  roots remain before reporting success.
- Add a read-only host summary through `yeet info` and `yeet info --host=...`
  so users can see the active data dir, services root, ZFS backing, and workload
  inventory without starting a migration.
- Keep old files on disk by default until the user explicitly runs a cleanup
  step or confirms cleanup.

## Non-Goals

- Do not delete old storage as part of the main migration path.
- Do not migrate or rewrite historical backup files under old data dirs.
- Do not rewrite arbitrary user-authored payload files outside yeet-managed
  generated artifacts and local `yeet.toml`.
- Do not introduce per-service selection into host-wide migration. The host flow
  remains all-or-none for services affected by a services-root change.
- Do not convert existing explicit per-service ZFS roots, such as
  `flash/yeet/plex`, into the new default services root unless they are already
  under the old default root being migrated.

## Current Gaps

### Data Dir Copy Does Not Rewrite State

The current data-dir apply path copies the old data dir to the new data dir and
then reinstalls catch. It does not rewrite the copied `db.json`. As a result,
records such as artifact refs, VM image rootfs paths, and tailscale binary refs
can still point at the old data dir.

### Generated Units Are Path-Stamped

Namespace units and VM units contain absolute paths to the catch runner, data
dir, service root, VM image cache, sockets, and generated helper files. Copying
files is not enough. The installed units must be rewritten or regenerated, then
systemd must be reloaded and the affected units restarted.

### Catch Root Is a Dependency of Other Units

Moving catch from `/root/data/services/catch` to
`/flash/yeet/services/catch` updates `catch.service`, but existing units may
still call the old catch binary for namespace helpers or VM launch commands.
The catch root move must therefore be a first-class path mapping used by the
broader artifact and unit rewrite pass.

### Client Config Update Pins Defaults

The current local config update writes each migrated service's new absolute
root. If the moved root equals `<host services root>/<service>`, that creates an
unnecessary explicit pin. The better steady state is to remove `service_root`
and `service_root_zfs` for entries that now use the host default root.

### ZFS Services Root Lacks Child Datasets

A host-wide ZFS services root should be a dataset prefix, not a single dataset
containing ordinary service directories. The desired layout is:

```text
flash/yeet/services
flash/yeet/services/catch
flash/yeet/services/nginx
flash/yeet/services/nodecast
```

The live `yeet-pve1` migration reached `/flash/yeet/services`, but left
`catch`, `nginx`, `nodecast`, and other service roots as directories under the
parent dataset. This means snapshots, quotas, recovery points, and future
per-service ZFS behavior do not have the expected dataset boundary.

### Host Storage Is Not Directly Inspectable

`yeet info <svc>` already shows detailed service state and includes a small host
section, but bare `yeet info` currently has no host meaning. After a storage
migration the user needs one stable command that answers "what roots does this
catch host use now?" and "how many workloads are running here?" without knowing
a service name or invoking a mutating `yeet host set` plan.

## Design

### Host Storage Path Map

Planning should produce an ordered set of path mappings. Mappings are applied
longest-prefix first so more-specific roots win before broader roots:

1. old catch service root -> new catch service root
2. each moved service root -> new service root
3. old services root -> new services root
4. old data dir -> new data dir

Each mapping records:

- source root
- target root
- reason (`catch-root`, `service-root`, `services-root`, `data-dir`)
- service name when applicable
- whether files are copied by this migration or only references are rewritten

The path map is used consistently for database rewriting, generated artifact
rewriting, installed systemd unit rewriting, validation, and result rendering.

### Per-Service ZFS Datasets Under Host Services Root

When `yeet host set --zfs --services-root=<dataset> --migrate-services=all`
is used, planning resolves `<dataset>` to its mountpoint and keeps the dataset
name as the services-root dataset prefix. Every affected service move should
record both:

- `To`: the filesystem mountpoint, `<services-root-mountpoint>/<service>`
- `ToZFS`: the child dataset, `<services-root-dataset>/<service>`

The catch service root uses the same rule:

- `To`: `<services-root-mountpoint>/catch`
- `ToZFS`: `<services-root-dataset>/catch`

Apply creates any missing child datasets before materializing service roots and
persists `ServiceRootZFS` in `db.json` for each moved service and for `catch`.
Existing explicit per-service ZFS roots outside the old default services root
remain untouched.

Already-populated directories at the final mountpoint are a separate in-place
datasetization case. A safe implementation must stage the existing directory
contents outside the future mountpoint, create or mount the child dataset, move
the staged contents into the mounted dataset, and only then update `db.json`.
The command must not claim this is complete by only setting the parent services
root to ZFS. If this staging path is not available, the plan should say that
child datasets are missing and refuse to silently report a clean ZFS services
root.

### Discovery

The host storage plan should inspect the current catch database and installed
systemd units before apply. It should classify discovered references into these
categories:

- current DB refs under old roots
- generated artifact files that need text rewriting
- compose files that need structured bind-mount rewriting
- installed systemd units that need reinstall or rewrite
- VM services whose systemd units or VM image paths depend on old roots
- services that must restart because their root, generated artifacts, units, or
  VM launch configuration changed
- stale old-root files that are safe to leave as cleanup candidates

The plan output should show counts and names rather than dumping every path by
default. Detailed output can be added later, but the structured plan and tests
should preserve the full details for callers.

### Database Rewrite

After copying the data dir and before restarting catch against it, apply should
rewrite the target `db.json` in place. The rewrite should operate on typed
database structures, not ad hoc JSON string replacement.

The initial rewrite scope is:

- `Service.Artifacts[*].Refs[*]`
- `Service.VM.Image.RootFS`
- VM disk paths and other VM path fields stored in `db.Service`
- image or volume state fields that are absolute paths under a moved root

The rewrite should be explicit and tested. If a new persisted absolute path is
added later, a test should fail until it is included in the host storage
rewrite helper.

### Generated Artifact Rewrite

For generated artifact files copied with a moved root, reuse the existing
service root migration rewrite behavior:

- compose files are parsed and only bind mount source paths are rewritten
- text artifacts use path-boundary replacement
- binary artifacts are not rewritten

For artifacts that were not under a moved service root but still refer to moved
roots, add a host-level artifact repair pass. It should rewrite only
yeet-generated artifact files referenced by current DB artifact refs and should
skip missing historical refs unless the current generation depends on them.

### Systemd Reinstall and Regeneration

Systemd should be reconciled from the rewritten database and generated
artifacts:

- systemd-backed services reinstall their primary unit, timer, tailscale unit,
  and namespace unit through the existing `svc.SystemdService` installer.
- Docker compose services with namespace artifacts reinstall the namespace
  prereq unit and any tailscale unit generated for the service.
- VM services regenerate their VM systemd unit from the current DB and desired
  config, rather than relying on string replacement in `/etc/systemd/system`.
- `systemctl daemon-reload` runs once after unit writes.

String rewrite of installed unit files is acceptable only as a fallback for a
unit type that cannot yet be regenerated. The preferred path is regeneration
from typed state.

### Restart Ordering

Apply should use a single ordered transaction:

1. acquire the host storage lock
2. validate that the plan is still current
3. prepare ZFS datasets, including per-service child datasets for ZFS services
   roots
4. identify and stop affected non-catch services
5. copy or materialize moved service roots
6. copy the data dir when requested
7. rewrite the target database
8. rewrite generated artifacts
9. reinstall or regenerate affected non-catch units
10. reload systemd
11. start previously running non-catch services
12. reinstall the catch unit using the desired roots
13. schedule catch restart
14. reconnect and verify catch reports the desired roots
15. validate no active old-root references remain

If a failure happens before services are restarted, the error should name the
failed step and the services left stopped. If a failure happens after the data
dir copy but before catch restart, the error should include the old data dir and
the command needed to restore the old catch unit or retry.

### Repair-Aware No-Op

`yeet host set` should not treat matching desired roots as complete by itself.
When the requested data dir and services root already match current catch
config, planning should still scan for active old-root references if enough
history exists to infer old roots from DB refs or installed units. If stale
active refs exist, the plan should render a repair action:

```text
Repair host storage references:
  /root/data -> /flash/yeet/data
  /root/data/services/catch -> /flash/yeet/services/catch
Regenerate units: 25
Restart services: 25
```

The first implementation can infer repair mappings from old references that
match known legacy roots:

- any `/root/data` reference when current data dir is not `/root/data`
- any old catch service root reference when current catch root differs
- any old services root reference when current services root differs

Future versions can persist migration history for exact repair mapping.

### Local Config Update

The apply result should include enough host context for the client to update
`yeet.toml` cleanly:

- desired services root
- migrated service moves
- services now using the host default root
- services not found in local config

For a matching local service entry:

- if the migrated root equals `<desired services root>/<service>`, remove
  `service_root` and `service_root_zfs`
- if the migrated root is not the default, set `service_root` to the moved root
  and preserve the correct ZFS flag
- if the service is not in local config, report it as skipped

This keeps local config aligned with the host without pinning default roots.

### Validation

Post-apply validation must check active state, not stale backups:

- current target `db.json` has no old-root refs in current service state
- installed units under `/etc/systemd/system` and active wants links do not
  contain old-root refs for yeet-owned active units
- running Docker container bind mounts do not use old roots
- catch reports desired data dir and services root
- previously running services are running again

The validation result should distinguish:

- `activeRefs`: must be zero for success
- `cleanupCandidates`: old files or historical refs that are not active
- `skippedHistoricalRefs`: old generation refs or backups left untouched

If `activeRefs` is non-zero, apply should fail after rendering a focused list of
the first few active references and leave cleanup disabled.

### Host Info Summary

Bare `yeet info` should become a host-level summary for the selected catch
host. `yeet info --host=yeet-pve1` should do the same for an explicit host.
Supplying a service argument keeps the current service behavior, so
`yeet info catch` still means service info for the `catch` service.

The host summary should include:

- selected host alias
- catch version, OS, architecture, install user, and install host when known
- data dir and services root from the running catch config
- detected ZFS dataset for the data dir, services root, and catch service root
  when ZFS is available
- service inventory counts for all named yeet services
- VM inventory counts as a subtype of services
- running, stopped, and unhealthy counts derived from the same status logic used
  by `yeet status`
- warnings when inventory or ZFS detection is unavailable

Plain output should stay compact:

```text
Host
  Host:           yeet-pve1
  Catch:          v0.x.x (linux/amd64)
  Data dir:       /flash/yeet/data (zfs flash/yeet/data)
  Services root:  /flash/yeet/services (zfs prefix flash/yeet/services)
  Catch root:     /flash/yeet/services/catch (zfs flash/yeet/services/catch)

Inventory
  Services:  18 total, 17 running, 1 stopped, 0 unhealthy
  VMs:       4 total, 3 running, 1 stopped, 0 unhealthy
```

JSON output should expose structured host fields and inventory counts. The
service-info JSON shape should remain unchanged when a service argument is
present.

The server-side read model should be generic Linux/catch state, not
Proxmox-specific. ZFS detection should map configured mountpoints to datasets
from `zfs list -H -o name,mountpoint` when available. The RPC or command path
used for host inventory is read-only and must be classified with the `read`
permission.

### Cleanup

Cleanup is a separate, explicit operation after validation. It can be added as:

```text
yeet host cleanup-storage --old-root /root/data
```

or as a follow-up prompt from `yeet host set` after a fully clean validation.
Cleanup should never run automatically under `--yes` in the first
implementation.

## User Experience

The main command remains:

```text
yeet host set --zfs \
  --data-dir=flash/yeet/data \
  --services-root=flash/yeet/services \
  --migrate-services=all \
  --config=/path/to/yeet.toml
```

The plan should include the complete operation:

```text
Host storage plan for yeet-pve1
Data dir: /root/data -> /flash/yeet/data (ZFS)
Services root: /root/data/services -> /flash/yeet/services (ZFS)
Migrate services: 5
Move catch service root: /root/data/services/catch -> /flash/yeet/services/catch
Rewrite database refs: 177
Regenerate systemd units: 25
Restart services: 25
Catch restart required.
```

If roots already match but old active refs remain:

```text
Host storage plan for yeet-pve1
Repair host storage references: 177
Regenerate systemd units: 25
Restart services: 25
Catch restart required.
```

After migration, users can inspect the steady state without starting another
plan:

```text
yeet info --host=yeet-pve1
```

If validation succeeds:

```text
Validated host storage migration: no active references to /root/data remain.
Old storage retained for cleanup: /root/data
```

## Testing

Unit tests should cover:

- path mapping order and prefix behavior
- ZFS services-root planning that creates a child dataset for each migrated
  service and for `catch`
- apply persistence of `ServiceRootZFS` for migrated services and `catch`
- typed DB rewrite for artifact refs, VM image rootfs, and VM disk paths
- generated artifact rewrite for compose, systemd, namespace, and tailscale
  artifacts
- plan rendering for data-dir move, services-root move, catch-root move, and
  repair-only plans
- apply ordering with faked systemd/service operations
- local config update that removes default-root pins
- validation failure when active refs remain
- validation success when only cleanup candidates remain
- host `yeet info` rendering for storage roots, detected ZFS datasets, service
  counts, VM counts, and JSON output
- service `yeet info <svc>` compatibility after making the service argument
  optional

Focused live validation on `yeet-pve1` should verify:

- `yeet host set ...` completes
- `zfs list -r flash/yeet/services` shows one child dataset per migrated
  service, including `flash/yeet/services/catch`
- `yeet host set ...` immediately after completion reports no changes
- `grep -R /root/data /etc/systemd/system /run/systemd/system` finds no active
  yeet-owned unit refs
- target `db.json` has no current active `/root/data` refs
- `docker inspect` bind mounts have no `/root/data` sources
- `yeet --host yeet-pve1 status --progress=plain` shows all previously running
  services running
- `yeet info --host=yeet-pve1` reports `/flash/yeet/data`,
  `/flash/yeet/services`, the ZFS backing datasets, and the expected service
  and VM counts

## Rollout Strategy

Implement the complete migration layer behind the existing `yeet host set`
surface. Do not add another user-facing migration command for normal use.

The first live run on a host that was partially migrated should be treated as a
repair migration. It should use the current desired roots and the old-root refs
found in DB and systemd to build the repair plan.

After the repair migration validates cleanly, leave `/root/data` in place and
report it as retained cleanup material. Cleanup can be designed and implemented
as a separate follow-up.
