# Service Sync Design

## Goal

Add a pull-style command that updates local `yeet.toml` service entries from
the live catch service state:

```bash
yeet service sync sonarr
yeet service sync --all
yeet service sync sonarr --config ~/yeet-services/yeet.toml
```

This closes the gap created when a live service is changed from outside the
project directory. For example, if `sonarr` is migrated on catch with:

```bash
yeet service set sonarr --service-root=flash/yeet/sonarr --zfs --copy
```

then `yeet service sync sonarr --config ~/yeet-services/yeet.toml` should update
the matching local entry to:

```toml
service_root = "flash/yeet/sonarr"
service_root_zfs = true
```

## Source Of Truth

Catch remains the source of truth for live remote service state. The catch DB
stores the effective filesystem root and, for ZFS-backed roots, the dataset
name.

`yeet.toml` remains a replay recipe. It stores enough intent to recreate a
service later, including the user-facing service root:

- Non-ZFS service root: `service_root = "/abs/path"`
- ZFS service root: `service_root = "tank/apps/service"` plus
  `service_root_zfs = true`
- Default catch root: omit both fields

If catch and `yeet.toml` disagree, catch wins for live operations. Sync is the
explicit command that pulls the authoritative live values back into the local
recipe.

## Command Semantics

Add a `sync` subcommand under the existing `service` command group:

```bash
yeet service sync <svc>
yeet service sync --all
```

Use the same target-host behavior as other service commands:

- `--host` selects the catch host.
- Without `--host`, normal host resolution applies.
- A service name plus host identifies a local `yeet.toml` entry.

Add an optional config-file selector:

```bash
yeet service sync sonarr --config ~/yeet-services/yeet.toml
```

When `--config` is omitted, yeet searches for `yeet.toml` from the current
working directory, matching the existing project-config behavior.

`--all` should update all existing entries in the selected `yeet.toml` that
target the resolved host. It should not mean "import every service from catch",
because catch does not know the original local payload path.

## Local Config Behavior

By default, sync updates only existing local service entries. It should not
invent entries because a complete replay recipe requires local fields that catch
does not know, such as payload path, env file path, schedule, and local args.

For a named service:

- If a matching `name + host` entry exists, update the syncable fields.
- If no matching entry exists, return a clear error:
  ```text
  no yeet.toml entry for sonarr@yeet-lab; run with --config from the project directory or add the service first
  ```

For `--all`:

- Update matching entries that still exist on catch.
- Report entries skipped because the remote service is missing.
- Leave unsupported or unknown local fields untouched.

The first implementation should sync only service-root fields:

- `service_root`
- `service_root_zfs`

This keeps the command focused on the service-root/ZFS problem. Later, the same
command can sync other catch-owned settings such as network mode, Tailscale
settings, or other `yeet service set` mutations.

## Remote Data Needed

The client needs the catch-side service root identity, not just the mounted
filesystem path.

Extend the service-info data returned by catch so the client can distinguish:

- default root
- custom filesystem root
- ZFS dataset root

The information should include:

```go
EffectiveRoot string // resolved filesystem path, useful for display
ServiceRoot   string // stored filesystem root, empty for default roots
ServiceRootZFS string // dataset name, empty for non-ZFS roots
```

For sync, the client writes:

- If `ServiceRootZFS != ""`: `service_root = ServiceRootZFS`,
  `service_root_zfs = true`
- Else if `ServiceRoot != ""`: `service_root = ServiceRoot`,
  `service_root_zfs` omitted/false
- Else: clear local `service_root` and `service_root_zfs`

This is important because a ZFS-backed service may have an effective filesystem
root like `/flash/yeet/sonarr`, but the replay recipe must store the dataset
name `flash/yeet/sonarr`.

## Service Set Warning

Keep the existing behavior where `yeet service set` updates a matching local
`yeet.toml` entry when one is found.

When no matching local entry is updated, print a follow-up hint after the remote
mutation succeeds:

```text
Updated catch service settings. No matching yeet.toml entry was updated.
Run from the project directory, or run:
  yeet service sync sonarr --config ~/yeet-services/yeet.toml
```

This avoids silently leaving the replay recipe stale while still respecting the
rule that `service set` should not create local project config from scratch.

## Error Handling

Prefer direct, actionable errors:

```text
no yeet.toml found; run from a project directory or pass --config
```

```text
--all cannot be combined with a service name
```

```text
service sync requires a service name or --all
```

```text
service "sonarr" not found on yeet-lab
```

```text
no yeet.toml entry for sonarr@yeet-lab
```

For `--all`, missing remote services should be summarized instead of failing the
whole command, unless every requested entry failed.

## Output

Default output should be concise and script-friendly enough for humans:

```text
Updated sonarr@yeet-lab in /Users/shayne/yeet-services/yeet.toml
  service_root = "flash/yeet/sonarr"
  service_root_zfs = true
```

For `--all`, print one line per updated or skipped entry and a final summary:

```text
Updated sonarr@yeet-lab
Skipped radarr@yeet-lab: service not found on catch
1 updated, 1 skipped
```

## Tests

Add focused tests for:

- CLI parsing and bridging:
  - `yeet service sync sonarr`
  - `yeet service sync --all`
  - `yeet service sync sonarr --config ./yeet.toml`
  - invalid combinations such as `--all sonarr`
- Project config updates:
  - ZFS service writes dataset name plus `service_root_zfs = true`
  - filesystem service writes absolute `service_root` and omits
    `service_root_zfs`
  - default service root clears both local fields
  - missing matching entry fails for named sync
  - `--all` updates existing matching entries and reports skips
- RPC/service-info data:
  - catch exposes both effective root and ZFS dataset identity
  - non-ZFS roots do not set the ZFS field
- `yeet service set` hint:
  - matching local entry still updates as today
  - no matching local entry prints the sync hint after successful remote update

## Out Of Scope

Do not import arbitrary remote catch services into `yeet.toml` in the first
implementation. That needs a separate design because catch does not know local
payload paths or whether the user wants those services in the current project.

Do not sync networks, schedules, args, payload paths, or env files yet. The
command should be structured so these can be added later, but this first version
only solves service-root drift.
