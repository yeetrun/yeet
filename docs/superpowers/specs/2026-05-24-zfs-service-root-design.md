# ZFS Service Root Design

## Goal

Allow a custom `--service-root` to be backed by a ZFS dataset. With ZFS enabled,
the user supplies a dataset name instead of a filesystem path:

```bash
yeet run vaultwarden ./compose.yml --service-root=tank/apps/vaultwarden --zfs
```

Catch creates the dataset when it does not already exist, resolves the dataset
mountpoint, and uses that resolved filesystem path as the actual service root.
This lets service roots live in ZFS datasets while preserving the service-root
path model introduced by the custom service-root work.

## CLI Semantics

Add `--zfs` to both initial run and service-setting mutation flows:

```bash
yeet run vaultwarden ./compose.yml --service-root=tank/apps/vaultwarden --zfs
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --copy
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --empty
```

Without `--zfs`, `--service-root` remains an absolute filesystem path on the
catch host.

With `--zfs`, `--service-root` is a ZFS dataset name. It is not required to be an
absolute path. Catch treats the dataset name as remote state and never executes
it through a shell.

`--zfs` is valid only when `--service-root` is present. A command that supplies
`--zfs` without `--service-root` should fail locally when possible, and catch
should also reject it defensively.

## Source Of Truth

Catch remains the source of truth for live service state. The catch DB should
store both:

```go
ServiceRoot    string // resolved filesystem path used by catch
ServiceRootZFS string // ZFS dataset name, empty for non-ZFS roots
```

`ServiceRoot` stays the effective path for all existing path resolution:

```text
<service-root>/
  bin/
  run/
  env/
  data/
```

`ServiceRootZFS` records that the path came from a ZFS dataset. Empty means the
normal filesystem-root behavior.

`yeet.toml` remains a replay recipe. It should store the user intent:

```toml
[[services]]
name = "vaultwarden"
host = "host-a"
payload = "./compose.yml"
service_root = "tank/apps/vaultwarden"
service_root_zfs = true
```

For non-ZFS roots, `service_root` stays an absolute filesystem path and
`service_root_zfs` is omitted.

## ZFS Resolution

Add a catch-side resolver for service-root requests:

```go
type serviceRootRequest struct {
	Raw string
	ZFS bool
}

type resolvedServiceRoot struct {
	Path       string
	ZFSDataset string
}
```

For non-ZFS requests, keep the current absolute path validation.

For ZFS requests:

1. Require a non-empty dataset name.
2. Check whether the dataset exists:
   ```bash
   zfs list -H -o name tank/apps/vaultwarden
   ```
3. If the dataset does not exist, create it with plain `zfs create`:
   ```bash
   zfs create tank/apps/vaultwarden
   ```
   Do not use `zfs create -p`. Parent dataset creation should be explicit, and
   ZFS should return the authoritative error if the parent does not exist.
4. Resolve the dataset mountpoint:
   ```bash
   zfs get -H -o value mountpoint tank/apps/vaultwarden
   ```
5. Reject mountpoint values that are empty, `-`, or `legacy`. Catch needs a real
   mounted filesystem path.
6. Validate the resolved mountpoint according to the operation:
   - New service or migration target: use the existing service-root filesystem
     target rules. The final root may be missing, empty, or a retry-safe managed
     skeleton, but must not be a file or non-empty unrelated directory.
   - Existing service rerun with the same stored dataset: require a real
     mounted filesystem path, but do not require it to be empty. It already
     contains the service files.

The resolver should use injectable command execution for tests. It should call
ZFS through `exec.Command`-style APIs with arguments, not via shell strings.

Existing datasets are accepted. If `zfs list` finds the dataset, catch should not
try to create it again.

## Initial Run

For a new service:

- `yeet run ... --service-root=/abs/path` keeps current filesystem behavior.
- `yeet run ... --service-root=tank/apps/svc --zfs` resolves the dataset to a
  mountpoint, validates that mountpoint, creates service dirs there, and stores:
  - `ServiceRoot = <resolved mountpoint>`
  - `ServiceRootZFS = "tank/apps/svc"`

Default roots should still not be persisted in `ServiceRoot`. ZFS-backed roots
must persist both the dataset and the resolved mountpoint.

For an existing service:

- Omitted root flags use the DB values.
- Matching ZFS dataset reruns should succeed even when the mounted dataset
  already contains service files. Catch should re-resolve the mountpoint and may
  update `ServiceRoot` if the dataset's mountpoint changed.
- A different ZFS dataset should be rejected and point to:
  ```bash
  yeet service set <svc> --service-root=<dataset> --zfs
  ```
- A filesystem root supplied for a ZFS-backed service should be rejected as an
  intentional root-type change that must go through `yeet service set`.
- `--zfs` supplied for a non-ZFS-backed service should also be rejected unless
  the user goes through `yeet service set`.

## Service Set Migration

`yeet service set` must support ZFS roots:

```bash
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --copy
```

The command should keep the existing migration rules:

- The service must exist.
- The service must be stopped.
- Non-interactive use requires `--copy` or `--empty`.
- Interactive prompt defaults to copy; answering no performs an empty migration.
- Old root remains in place after success.
- DB update happens only after filesystem migration succeeds.

ZFS changes add a target-resolution step before migration:

- Non-ZFS target: validate absolute filesystem root as today.
- ZFS target: ensure or create dataset, resolve mountpoint, validate mountpoint.

No-op detection should consider both identity and path:

- Same ZFS dataset for a ZFS-backed service is a no-op.
- Same filesystem path for a non-ZFS service is a no-op.
- Switching between ZFS and non-ZFS should be allowed only through
  `yeet service set`, but the resolved filesystem paths must still be checked
  for nested-root hazards.

The existing copy and empty migration implementations can operate on the
resolved filesystem path. After success:

- ZFS target:
  - `ServiceRoot = <resolved mountpoint>`
  - `ServiceRootZFS = <dataset>`
- Non-ZFS target:
  - `ServiceRoot = <filesystem path or empty if default>`
  - `ServiceRootZFS = ""`

If `zfs create` succeeds but later file migration fails, catch should leave the
dataset in place and keep the DB unchanged. Automatically destroying a dataset is
too destructive for the first implementation.

## Error Handling

Errors should identify whether the failing value is a filesystem root or a ZFS
dataset.

Examples:

```text
--zfs requires --service-root
zfs dataset is required when --zfs is set
failed to create zfs dataset "tank/apps/vaultwarden": <zfs stderr>
zfs dataset "tank/apps/vaultwarden" has mountpoint "legacy"; expected a mounted filesystem path
service "vaultwarden" already uses zfs dataset "tank/apps/vaultwarden"
service root for "vaultwarden" is zfs dataset "tank/apps/vaultwarden"; use `yeet service set vaultwarden --service-root=... --zfs` to change it
```

ZFS command failures should include stderr when available.

## Tests

Add focused tests for:

- CLI parsing:
  - `run --service-root=tank/apps/app --zfs`
  - `service set --service-root=tank/apps/app --zfs --copy`
  - `--zfs` without `--service-root` rejected
  - non-ZFS `--service-root` still requires absolute paths
- Client config:
  - `yeet.toml` stores `service_root_zfs = true`
  - stored ZFS roots rehydrate as `--service-root=<dataset> --zfs`
  - service-root-only ZFS changes reach catch
  - `service set --zfs` updates matching local config and does not create new
    config entries
- DB:
  - `ServiceRootZFS` clone/view/migration coverage
- ZFS resolver:
  - existing dataset accepted without create
  - missing dataset runs plain `zfs create <dataset>`
  - create failure returns stderr
  - mountpoint `legacy`, `-`, and empty values rejected
  - resolved mountpoint validation rejects invalid filesystem roots
- Initial run:
  - new ZFS root persists dataset and resolved mountpoint
  - rerun same dataset succeeds
  - rerun different dataset rejects with `service set ... --zfs`
  - non-ZFS/ZFS type changes reject outside `service set`
- Migration:
  - `service set --zfs --copy` creates/resolves dataset, copies into mountpoint,
    preserves metadata, updates DB after rename, and leaves old root in place
  - `service set --zfs --empty` creates layout and stores dataset
  - non-TTY ZFS migration still requires `--copy` or `--empty`
  - failed file migration after dataset creation leaves dataset in place and DB
    unchanged

## Documentation

Update README and website docs with examples:

```bash
yeet run vaultwarden ./compose.yml --service-root=tank/apps/vaultwarden --zfs
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --copy
yeet service set vaultwarden --service-root=tank/apps/vaultwarden --zfs --empty
```

Docs should state:

- With `--zfs`, `--service-root` is a ZFS dataset name.
- Without `--zfs`, `--service-root` is an absolute filesystem path.
- Existing datasets are accepted.
- Missing datasets are created with plain `zfs create`, not `zfs create -p`.
- Catch resolves the dataset mountpoint with `zfs get` and stores the resolved
  filesystem path in its DB.
- Service-root migration rules still apply: service stopped, old root left in
  place, non-interactive use requires `--copy` or `--empty`.

## Out Of Scope

- Destroying datasets on failed migrations.
- Automatic parent dataset creation with `zfs create -p`.
- ZFS property customization such as compression, recordsize, quota, or
  encryption.
- Supporting `legacy` or unmounted ZFS datasets.
- Automatically detecting whether a normal filesystem path is on ZFS.
