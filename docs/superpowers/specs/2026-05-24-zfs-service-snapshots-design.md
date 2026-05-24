# ZFS Service Snapshots Design

## Goal

Automatically take ZFS snapshots before operations that can make a service start
writing data with new code or new container images.

The feature is for services whose service root is already backed by a ZFS
dataset via `--service-root=<dataset> --zfs`. Non-ZFS service roots are skipped.
The first implementation creates pre-change snapshots only; it does not create
post-success snapshots and does not implement rollback.

## ZFS Behavior Assumptions

The design relies on standard OpenZFS behavior:

- `zfs snapshot dataset@snapname` creates an atomic point-in-time snapshot.
- `zfs list -t snapshot` can enumerate snapshots, including script-friendly
  output with `-H` and explicit fields with `-o`.
- `zfs set` can set user properties on snapshots.
- ZFS user properties are arbitrary application metadata, must contain a colon,
  and can be listed with `zfs list` or `zfs get`.
- `zfs destroy dataset@snapshot` destroys a named snapshot.

References:

- https://openzfs.github.io/openzfs-docs/man/v2.2/8/zfs-snapshot.8.html
- https://openzfs.github.io/openzfs-docs/man/v2.2/8/zfs-list.8.html
- https://openzfs.github.io/openzfs-docs/man/v2.2/8/zfs-set.8.html
- https://openzfs.github.io/openzfs-docs/man/v2.2/7/zfsprops.7.html
- https://openzfs.github.io/openzfs-docs/man/v2.2/8/zfs-destroy.8.html

## Source Of Truth

Catch `db.json` is authoritative for live behavior. It stores:

- server-wide snapshot defaults
- each service's snapshot override
- each service's ZFS identity through `ServiceRootZFS`
- each service's effective filesystem path through `ServiceRoot`

`yeet.toml` is a replay file. It may mirror per-service snapshot overrides so a
service can be recreated from a project directory, but catch does not consult
`yeet.toml` when deciding whether to snapshot. If `yeet.toml` disagrees with
catch, catch wins.

Server-wide defaults belong only in catch DB because they are host policy. A
single `yeet.toml` can target different hosts with different snapshot defaults.

## Snapshot Defaults

The default catch policy is automatic for ZFS-backed services:

```text
enabled: true
events: run, docker-update, service-root-migration
keep_last: 5
max_age: 7d
required: true
```

This makes the default model opt-out per service. If a catch host changes its
server default to `enabled=false`, the model becomes opt-in per service.

The default applies only when `ServiceRootZFS` is non-empty. A non-ZFS service
with snapshots enabled through inherited policy is skipped without error because
there is no dataset to snapshot.

## DB Model

Add snapshot policy fields to the DB with a migration. The exact Go types can be
adjusted during implementation, but the persisted model should distinguish
"inherit" from explicit false/zero values.

Conceptually:

```go
type SnapshotPolicy struct {
	Enabled  *bool    `json:",omitempty"`
	KeepLast *int     `json:",omitempty"`
	MaxAge   string   `json:",omitempty"`
	Events   []string `json:",omitempty"`
	Required *bool    `json:",omitempty"`
}

type Data struct {
	SnapshotDefaults SnapshotPolicy `json:",omitempty"`
	Services         map[string]*Service
	// existing fields...
}

type Service struct {
	SnapshotPolicy SnapshotPolicy `json:",omitempty"`
	// existing fields...
}
```

For server defaults, omitted fields inherit built-in defaults. For service
overrides, omitted fields inherit the effective server default.

`MaxAge` is stored as a string such as `72h` or `7d`. Implementation should
validate and normalize the accepted duration syntax. `keep_last` must be at
least 1 when snapshots are enabled.

## Local `yeet.toml`

`yeet.toml` stores only service-level intent. It should not store server
defaults.

Example override:

```toml
[[services]]
name = "sabnzbd"
host = "yeet-lab"
type = "run"
payload = "sabnzbd.yml"
service_root = "tank/apps/sabnzbd"
service_root_zfs = true
snapshots = "on" # inherit | on | off
snapshot_keep_last = 3
snapshot_max_age = "72h"
snapshot_required = false
```

Omitted snapshot fields mean inherit. `snapshots = "inherit"` is equivalent to
omitting the field, but the parser may accept it for readability.

`yeet service sync` should mirror the catch service override back into
`yeet.toml`:

- inherited service policy removes local snapshot override fields
- explicit on/off writes `snapshots = "on"` or `snapshots = "off"`
- explicit retention overrides write only the fields that are overridden

`yeet run` from a stored config should send the per-service override to catch
when snapshot fields are present. If no snapshot fields are present, catch uses
the server default.

## CLI

Add a top-level `snapshots` group for server defaults:

```bash
yeet snapshots defaults show
yeet snapshots defaults set --enabled=true --keep-last=5 --max-age=7d
yeet snapshots defaults set --enabled=false
```

These commands target the selected catch host, using the same host selection
rules as other remote commands.

Extend `service set` for per-service overrides:

```bash
yeet service set sabnzbd --snapshots=off
yeet service set sabnzbd --snapshots=on
yeet service set sabnzbd --snapshots=inherit
yeet service set sabnzbd --snapshot-keep-last=3 --snapshot-max-age=72h
yeet service set sabnzbd --snapshot-required=false
```

Snapshot-only `service set` changes do not require the service to be stopped.
If the same command also changes `--service-root`, the existing service-root
migration rules still apply.

`yeet service set` should update a matching local `yeet.toml` entry when one is
found, and should print the existing sync hint when no matching entry exists.

Create-time flags on `yeet run` are optional for v1. The default experience does
not require them because ZFS services inherit the server default. Stored
`yeet.toml` overrides must still be passed through during replay.

## Snapshot Events

V1 supports these events:

- `run`: before a new service generation is installed or restarted
- `docker-update`: before Docker compose recreates containers for newer images
- `service-root-migration`: before a service-root migration when the source root
  is already ZFS-backed

For a brand-new service, there is no previous service state to protect, so catch
does not create a snapshot before first install.

For `run`, the useful protection point is before the new code starts. The
implementation should create the snapshot as late as practical before committing
the generation and restarting the service. If the upload path has already staged
new artifact files in the service root, that is acceptable for v1 because the
snapshot's main purpose is to preserve service data before new code runs.

For `docker-update`, pre-pulling images can happen before the snapshot because
it does not mutate the service root. The snapshot must happen before
`docker compose up --pull ... -d` can recreate containers.

For `service-root-migration`, snapshot only the source dataset when the existing
service is ZFS-backed. Migrating from a plain filesystem root to a ZFS root has
no source dataset to snapshot.

## Snapshot Creation

Resolve the effective policy at operation time:

1. Load the service from catch DB.
2. If `ServiceRootZFS` is empty, skip.
3. Merge built-in defaults, server defaults, and service override.
4. If disabled, skip.
5. If the event is not enabled, skip.
6. Create a pre-change snapshot.
7. Run the risky operation.
8. Prune old yeet-owned snapshots for the same service and dataset.

Snapshot names should be deterministic and readable:

```text
tank/apps/sabnzbd@yeet-20260524T184233Z-run-g12
tank/apps/sabnzbd@yeet-20260524T190101Z-docker-update-g12
```

If a timestamp collision occurs, append a short random suffix.

Create snapshots with ZFS user properties when possible:

```bash
zfs snapshot \
  -o com.yeetrun:created-by=catch \
  -o com.yeetrun:service=sabnzbd \
  -o com.yeetrun:event=docker-update \
  -o com.yeetrun:generation=12 \
  -o com.yeetrun:policy-version=1 \
  tank/apps/sabnzbd@yeet-20260524T190101Z-docker-update-g12
```

The `com.yeetrun:*` namespace follows the OpenZFS guidance that programmatic
user properties should use a reverse-DNS-style module name.

The default is non-recursive snapshots. Recursive snapshots can be added later
if users intentionally create child datasets under a service root.

## Failure Semantics

If snapshots are enabled and `required=true`, snapshot creation failure aborts
the operation before the deploy, Docker update, or migration continues.

If `required=false`, catch prints a warning and continues.

Prune failures are warnings. They should not fail the original operation because
the service mutation has already happened.

If the risky operation fails after snapshot creation, catch leaves the snapshot
in place and reports its name. There is no automatic rollback in v1.

Snapshots are crash-consistent, not application-quiesced. Catch does not pause
or flush application writes before taking a snapshot. A future feature can add
quiesce hooks or stop-before-snapshot behavior if needed.

## Retention

Retention applies only to snapshots created by this feature. Catch never prunes
user-created snapshots.

Default retention:

```text
keep_last: 5
max_age: 7d
```

`keep_last` is a cap, not a floor. After creating a snapshot, catch lists
yeet-owned snapshots for the same dataset and service, newest first, then prunes
any snapshot that is either:

- outside the newest `keep_last` snapshots, or
- older than `max_age`

Catch must never prune the snapshot just created for the current operation. If a
future manual prune path runs without creating a new snapshot first, it should
still keep the newest yeet-owned snapshot as a last recovery marker.

Safe pruning requires both:

- snapshot name has the `yeet-` prefix
- snapshot metadata includes `com.yeetrun:created-by=catch` and matching
  `com.yeetrun:service`

If older snapshots were created by a previous version that used the `yeet-`
prefix but has no properties, catch should not delete them automatically. A
manual cleanup command can be designed later if needed.

## Implementation Boundaries

Add a focused catch-side snapshot unit:

- `pkg/catch/service_snapshots.go`
- policy resolution
- duration validation
- snapshot naming
- ZFS command construction
- snapshot list parsing
- prune decisions

Use the existing `zfsCommandRunner` abstraction from `service_root_zfs.go` so
unit tests do not require ZFS.

Hook points:

- `Installer.InstallGen` or the closest pre-install point for `run`
- `ttyExecer.dockerUpdateCmdFunc` for Docker updates
- `migrateServiceRoot` for service-root migration

The implementation should keep command execution catch-side. The yeet client
parses local flags and forwards requests; catch validates and executes against
its local ZFS state.

## Status And Info

Extend service info to expose:

- effective snapshot policy
- service snapshot override
- ZFS dataset name
- latest yeet-created snapshot name, if cheaply available

The latest snapshot can be omitted from v1 if it would make `yeet info` slow.
Policy exposure is required so `yeet service sync` can mirror service override
settings into `yeet.toml`.

## Tests

Add focused tests for:

- DB migration, clone, and view helpers for snapshot defaults and service
  overrides
- server default plus service override policy resolution
- duration parsing for `7d`, `72h`, invalid values, and zero/negative values
- CLI parser tests for `snapshots defaults show/set`
- CLI parser and bridge tests for new `service set` snapshot flags
- `yeet.toml` encode/decode and replay of snapshot overrides
- `yeet service sync` mirroring inherited, on, off, and retention overrides
- snapshot naming, collision handling, and user property command construction
- ZFS list parsing and prune selection
- retention boundaries: newest 5 kept, older-than-7d pruned, current snapshot
  never pruned
- non-ZFS service skip behavior
- required snapshot failures abort before mutation
- optional snapshot failures warn and continue
- prune failures warn after operation
- hook ordering for run, Docker update, and service-root migration

## Documentation

Update README and website docs with:

- automatic snapshots for ZFS service roots
- server defaults with `yeet snapshots defaults`
- per-service overrides with `yeet service set`
- retention defaults and pruning behavior
- catch DB as source of truth and `yeet.toml` as replay config
- snapshot limitations: no automatic rollback and no application quiescing

The changelog should describe this as a new ZFS safety feature, not as a change
to an already-existing snapshot system.

## Out Of Scope

- Automatic rollback.
- Post-success snapshots.
- Recursive snapshots.
- Application quiesce hooks.
- Snapshotting non-ZFS filesystem roots.
- Importing or pruning user-created snapshots.
- A manual cleanup command for legacy/unlabeled yeet-prefixed snapshots.
- Named reusable policies beyond the server default and per-service override.
