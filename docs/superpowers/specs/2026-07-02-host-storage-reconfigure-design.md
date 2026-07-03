# Host Storage Configuration and Reconfiguration

## Summary

Yeet should make catch storage explicit and configurable. New interactive
installs should guide users through the storage layout instead of silently using
`data/`, and existing installs should have a careful host-level reconfiguration
path that can move catch state, change the default services root, and optionally
migrate services.

The new first-install default is `$HOME/yeet-data` on the remote host. Repeat
`yeet init` runs are upgrades and should preserve the existing storage paths
without asking storage questions.

## Goals

- Change the default catch data directory for new installs and direct catch
  startup without `--data-dir` to `$HOME/yeet-data`.
- Add an interactive storage wizard to first-time `yeet init <machine-host>`
  when stdin/stdout are TTYs.
- Preserve existing install paths on repeat `yeet init` with no storage prompts.
- Support filesystem paths and ZFS datasets for the catch data directory.
- Support a host-level services root, with filesystem and ZFS dataset-prefix
  modes.
- Add `yeet host set` as the host-level reconfiguration command.
- Let `yeet host set` accept explicit flags, prompt for missing decisions in a
  TTY, and confirm disruptive work unless `--yes` is passed.
- Offer all-or-none migration for services affected by a services-root change.
- Keep `yeet service set <svc> --service-root ...` as the one-off service
  migration path.
- Update local `yeet.toml` entries when service migration changes roots and a
  matching entry is known.

## Non-Goals

- Do not prompt for storage during repeat catch upgrades.
- Do not delete the old data directory as part of this work.
- Do not add per-service selection inside host-wide migration. The host flow is
  all or none.
- Do not create a large storage-profile abstraction.
- Do not make `yeet host set` an interactive wizard. It is flag-driven with
  targeted prompts for missing or destructive decisions.

## User-Facing Commands

### First Install

```text
yeet init root@host
```

On a first install in a TTY, yeet runs a storage wizard before installing catch.
The wizard always appears, even when the user keeps all defaults.

The default path shown in the wizard is `$HOME/yeet-data`, resolved on the
remote host before writing the systemd unit. catch still starts with an absolute
path such as:

```text
--data-dir=/root/yeet-data
```

If the target already has catch installed, `yeet init` behaves as an upgrade:
it preserves the installed `--data-dir` and services root and does not ask
storage questions.

### Host Reconfiguration

```text
yeet host set [--data-dir PATH_OR_DATASET]
              [--services-root PATH_OR_DATASET_PREFIX]
              [--zfs]
              [--migrate-services=all|none]
              [--config PATH]
              [--yes]
```

`--data-dir` changes the catch data directory.

`--services-root` changes the host default root used for service roots. This is
plural because it is host-level state. The existing per-service command remains
singular:

```text
yeet service set <svc> --service-root PATH_OR_DATASET [--zfs] [--copy|--empty]
```

`--zfs` applies to every supplied storage target in that command. If both
`--data-dir` and `--services-root` are supplied with `--zfs`, both values are
treated as ZFS dataset names or dataset prefixes. Mixed filesystem and ZFS
changes should be performed as separate commands.

`--migrate-services=all|none` controls services affected by a services-root
change. If omitted in a TTY, yeet prompts. If omitted outside a TTY and affected
services exist, the command fails with guidance.

`--config PATH` optionally points at the local `yeet.toml` to update after
service migration. If omitted, yeet uses the project config discovered from the
current directory when one exists.

`--yes` skips confirmation prompts but does not relax validation.

## Storage Wizard

The first-install wizard asks storage questions in layers:

1. Use `$HOME/yeet-data` for catch data?
2. If not, and ZFS is detected, use a ZFS dataset for catch data?
3. Choose the filesystem path or dataset. If a dataset does not exist, yeet
   says it will create it.
4. Use `$DATA_DIR/services` for services?
5. If not, and ZFS is detected, use a ZFS dataset prefix for service roots?
6. Choose the filesystem path or dataset prefix.

The UI should use Charm Huh forms, selects, text inputs, placeholders, and
confirm prompts behind a small yeet-owned interface. Non-TTY paths and tests
should not depend on an actual terminal UI.

If ZFS is not detected, the wizard keeps the advanced route filesystem-only and
does not mention ZFS as an available choice. If ZFS is detected, suggestions
should come from the existing ZFS root candidate logic where practical.

Suggested layouts should be contextual. If the catch data dir is a ZFS dataset,
the services-root suggestions should favor nearby sibling or nested layouts:

```text
data dir dataset: flash/yeet/data
services root suggestion: flash/yeet/services
```

For filesystem defaults:

```text
data dir: $HOME/yeet-data
services root: $HOME/yeet-data/services
```

For ZFS services roots, the chosen value is a dataset prefix. New default
service roots are created under that prefix as per-service datasets such as:

```text
flash/yeet/services/radarr
flash/yeet/services/plex
```

## CLI Validation

Valid filesystem examples:

```text
yeet host set --data-dir /srv/yeet-data
yeet host set --services-root /srv/yeet-services
yeet host set --data-dir /srv/yeet-data --services-root /srv/yeet-services
```

Valid ZFS examples:

```text
yeet host set --data-dir flash/yeet/data --zfs
yeet host set --services-root flash/yeet/services --zfs
yeet host set --data-dir flash/yeet/data --services-root flash/yeet/services --zfs
```

Invalid mixed example:

```text
yeet host set --data-dir flash/yeet/data --services-root /srv/services --zfs
```

The error should make the rule clear:

```text
--zfs applies to both --data-dir and --services-root in this command.
For mixed filesystem/ZFS storage, run separate commands.
```

When `--zfs` is not set, `--data-dir` and `--services-root` values must be
absolute filesystem paths. When `--zfs` is set, both values must be dataset
names or dataset prefixes, not absolute paths.

## Reconfiguration Plan

`yeet host set` should build and display a plan before changing remote state.
The plan should include:

- current data dir
- target data dir
- current services root
- target services root
- whether each target is filesystem or ZFS
- ZFS datasets that will be created
- services affected by a services-root change
- services that will be stopped and restarted
- whether catch will restart
- local `yeet.toml` file that will be updated
- local config entries that cannot be updated automatically

If the plan includes data movement, service restarts, or catch restart, yeet
confirms before applying it unless `--yes` is present.

Non-TTY mode must have enough flags to make every decision explicit.

## Data Directory Changes

Changing the catch data directory requires daemon-level work:

1. Resolve the target path. For ZFS, create the dataset if needed and resolve
   its mountpoint.
2. Stop or quiesce catch enough to copy its state safely.
3. Copy catch state from the old data dir to the new data dir, preserving
   `db.json`, Tailscale tsnet state, registry/cache state, VM images, mounts,
   and install metadata.
4. Write or reinstall the catch systemd unit with the new absolute
   `--data-dir`.
5. Schedule catch to restart after the apply RPC returns. The catch daemon
   cannot synchronously restart its own systemd unit and then continue the same
   RPC handler reliably.
6. Have the yeet client reconnect and verify the new daemon answers
   `catch.Info` and reports the target paths.
7. Leave the old data dir in place.

The implementation should be conservative about failures. If the new daemon
does not come up, the error should explain what was changed and where the old
data still lives. A later cleanup flag can remove old data, but this design does
not include it.

## Services Root Changes

Services affected by a services-root change are services whose effective root is
under the old host services root and whose DB entry does not intentionally point
elsewhere. The plan should treat already explicit custom service roots as
unaffected.

If `--migrate-services=all` or the TTY prompt is accepted:

1. Record which affected services are running.
2. Stop affected services.
3. Move each service root to the new services root. The implementation may use
   a staged copy plus cleanup when a direct rename is not possible, such as
   cross-device or ZFS dataset moves.
4. For ZFS services-root mode, create per-service datasets under the chosen
   prefix and use their mountpoints.
5. Rewrite catch DB service roots and artifact references.
6. Reinstall systemd/compose artifacts as needed.
7. Refresh prerequisites that depend on service roots.
8. Restart services that were running before migration.

If `--migrate-services=none` or the TTY prompt is declined:

1. Leave existing service files in place.
2. Write explicit per-service roots for affected services so they continue to
   live under the old paths.
3. Change only the host default services root for future services.

The all-or-none rule keeps host reconfiguration understandable. Users who want
to move one service should use `yeet service set <svc> --service-root ...`.

## Local Config Updates

When host reconfiguration changes service roots, yeet should update local
`yeet.toml` entries only when it can match `service@host` confidently.

If `--config PATH` is supplied, use that file. If omitted, use the project
config discovered from the current directory. If no config is found, or if
specific entries are missing, the remote operation can still succeed. Yeet
should print exact follow-up commands such as:

```text
yeet service sync <svc> --config ~/yeet-services/yeet.toml
yeet service sync --all --config ~/yeet-services/yeet.toml
```

Existing `service sync` behavior already knows how to write `service_root` and
`service_root_zfs` from catch service info. Host reconfiguration should reuse
that local config path where possible instead of creating a second config
writer.

## Architecture

### Client Init Storage

Add a focused client-side storage module for init:

```text
pkg/yeet/init_storage.go
```

Responsibilities:

- determine whether init is first install or repeat upgrade
- run the TTY wizard for first installs
- probe remote ZFS availability and candidates
- normalize filesystem and ZFS answers into install options
- keep Huh behind an interface for tests

The first-install detection should prefer current catch info or install
metadata when available. If catch is not reachable but install artifacts exist,
the install path should still avoid silently overwriting storage.

### Host Set Client

Add:

```text
pkg/yeet/host_set.go
```

Responsibilities:

- parse and validate `yeet host set`
- enforce `--zfs` mixed-mode rules
- request or build a remote host-storage plan
- render plan output
- prompt for missing migration choices and final confirmation
- apply the remote plan
- update local `yeet.toml` when possible

### Catch Storage Plan and Apply

Add:

```text
pkg/catch/host_storage.go
```

Responsibilities:

- represent current and target host storage
- resolve filesystem paths and ZFS datasets
- create ZFS datasets when approved by the plan
- identify services affected by services-root changes
- apply data-dir and services-root changes
- preserve old data by default
- verify post-apply catch state after reconnect when catch restart is scheduled

### Catch Startup

`cmd/catch` should default `--data-dir` to `$HOME/yeet-data` when the flag is
not supplied. It should also accept a configurable services root in addition to
`--data-dir`. The services-root default remains:

```text
$DATA_DIR/services
```

The catch info response should report both paths so clients can reason about
current state:

```json
{
  "rootDir": "/root/yeet-data",
  "servicesDir": "/root/yeet-data/services"
}
```

If persisted host storage defaults are needed beyond startup flags, add a
focused DB schema field and migration. Schema changes must bump
`CurrentDataVersion` and regenerate db view/clone helpers.

### RPC and Permissions

Prefer existing remote command execution only if it can safely represent a
structured plan/apply flow. If not, add explicit RPC methods and types:

```text
catch.HostStoragePlan
catch.HostStorageApply
```

Both require `manage`. The existing ZFS candidate discovery remains `read`.
Catch-mediated host shell remains `ssh`.

## Error Handling

- Missing ZFS command should produce a clear "ZFS not available" message and
  fall back to filesystem choices in init.
- Invalid dataset names or absolute paths under `--zfs` should fail before any
  remote mutation.
- Existing non-empty filesystem targets should fail unless they are known
  retry-safe skeletons.
- Data-dir and services-root changes should reject nested source/target roots
  when migration could recursively copy itself.
- Service migration should stop on plan/apply drift, such as a service root
  changing after planning.
- If a service fails to restart after migration, report that service explicitly
  and preserve enough state for manual recovery.
- Local config update failures should not hide successful remote changes; they
  should report the config error and exact sync guidance.

## Documentation

Update user-facing docs in the same implementation work:

- README install examples for the new `$HOME/yeet-data` default.
- Website installation page for the first-install storage wizard.
- Manual page for `yeet host set`.
- Examples for filesystem storage, all-ZFS storage, and mixed storage using
  separate commands.
- Guidance that `yeet service set <svc> --service-root ...` is the one-off
  service migration command.
- Warning that old data directories are preserved by default.

Docs should use `yeetrun.com` for public URLs.

## Testing

CLI parser tests:

- valid filesystem `host set` flags
- valid ZFS `host set` flags
- invalid mixed filesystem/ZFS command
- non-TTY missing `--migrate-services`
- `--yes` skips confirmation but not validation

Init tests:

- first install in TTY runs the storage wizard
- repeat init preserves existing paths and does not prompt
- default data dir is `$HOME/yeet-data`
- accepted defaults produce `$HOME/yeet-data/services`
- ZFS detected path suggests datasets and creates missing datasets in the plan

Catch tests:

- `--data-dir` plus default services root derivation
- explicit services root startup flag
- `catch.Info` reports data and services roots after reconnect
- ZFS dataset resolution for data dir and services prefix
- host storage plan affected-service discovery
- data-dir move preserves tsnet state and old data dir
- services-root migration all stops, moves, rewrites, and restarts services
- services-root migration none pins old service roots
- drift detection between plan and apply

Client config tests:

- migrated services update matching `service@host` entries
- `--config PATH` targets the requested config
- missing config prints sync guidance
- missing entries print per-service sync guidance

Authorization tests:

- host storage plan/apply RPCs require `manage`
- ZFS candidate discovery remains `read`
- host shell remains `ssh`

## Open Decisions

No unresolved product decisions remain for this spec. Implementation may still
choose whether the remote plan/apply is represented as structured RPC or an
existing catch-side command, but it must preserve the same validation,
permission, prompt, and safety behavior described here.
