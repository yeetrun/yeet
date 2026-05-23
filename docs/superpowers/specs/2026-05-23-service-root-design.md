# Service Root Design

## Goal

Allow a service to use a custom remote root directory for all yeet-managed
service files:

```bash
yeet run vaultwarden ./compose.yml --service-root=/srv/apps/vaultwarden
```

The custom root replaces the current default service root:

```text
<catch-data>/services/<svc>/
```

with:

```text
/srv/apps/vaultwarden/
  bin/
  run/
  env/
  data/
```

This should keep service artifacts, runtime files, environment files, and
persistent app data together when a user wants the service to live on a
specific mount or host directory.

## Source Of Truth

Catch is the source of truth for live remote service state. The catch database
must store the effective service root because catch needs to resolve service
paths without depending on a particular client or `yeet.toml`.

`yeet.toml` remains a replay recipe. It should optionally store
`service_root`, so a user can recreate a service on a fresh host or after
removal:

```toml
[[services]]
name = "vaultwarden"
host = "host-a"
payload = "./compose.yml"
service_root = "/srv/apps/vaultwarden"
```

If catch DB and `yeet.toml` disagree for an existing service, catch wins.
`yeet run` must reject the mismatch and direct the user to
`yeet service set <svc> --service-root=...` for an intentional migration.

## Data Model

Add `ServiceRoot string` to `db.Service`. Empty means the current default:

```text
<catch-data>/services/<svc>
```

Non-empty values must be cleaned absolute remote paths.

Add a path resolver on the catch side:

```go
defaultServiceRoot(svc) = filepath.Join(cfg.ServicesRoot, svc)
effectiveServiceRoot(service) = service.ServiceRoot if set, else defaultServiceRoot(service.Name)
```

Every service-specific path should be derived from the effective root:

- `bin/`
- `run/`
- `env/`
- `data/`

`ServiceInfo.Paths.Root` should report the effective root. Existing clients that
derive the data directory as `Paths.Root + "/data"` then follow custom roots
without needing a new RPC field.

## Initial Run

`yeet run` gets a new flag:

```bash
yeet run <svc> <payload> --service-root=/abs/path
```

The client should pass `--service-root` through to catch and persist it in
`yeet.toml` as `service_root`. Re-running from stored config should rehydrate
the stored root and send it to catch.

Catch validates the root before generating artifact paths or writing service
state:

- The path must be absolute.
- The path must be cleaned.
- The parent directory must already exist.
- Catch may create the final service root directory.
- If the final service root already exists for a new service, it must be empty.
- Catch must create or ensure `bin`, `run`, `env`, and `data` under the root.

For a new service, catch persists the accepted root to the service record.

For an existing service:

- Omitted `--service-root` uses the catch DB value.
- A matching `--service-root` proceeds normally.
- A different `--service-root` is rejected with an error that points to
  `yeet service set`.

## Service Set Command

Add a `service` command group with:

```bash
yeet service set <svc> --service-root=/abs/path
```

This command is the controlled mutation path for settings that are normally
chosen during initial `yeet run`. The first supported setting is
`--service-root`; future settings could include network options such as
`--net`, `--ts-tags`, or LAN options.

The command runs on catch and requires:

- The service exists.
- The service is not running.
- The new root is an absolute cleaned path.
- The new root parent exists.
- The new root is not the same as the current root.
- The new root is not inside the current root, and the current root is not
  inside the new root.
- The destination root is missing or empty. Non-empty destinations are rejected.

After a successful remote `service set`, the client should update
`service_root` in a matching local `yeet.toml` entry when one is present. It
should not create a new `yeet.toml` only because `service set` was run.

## Migration Flow

Changing `--service-root` should be interactive by default and scriptable with
explicit flags.

Interactive flow:

```text
Copy existing service files from /old/root to /new/root? [Y/n]
```

Scripted flow should require one explicit choice when stdin is not a TTY:

- `--copy`: copy the existing service root to the new root.
- `--empty`: create the new managed layout without copying old files.

The recommended default is copy. `--empty` should warn that the service may not
be startable until it is re-run or manually populated.

## Safe Copy Semantics

The copy path must work across filesystems and devices. The copy operation
itself cannot be atomic across filesystems, so catch should make the destination
cutover atomic instead:

1. Create a hidden staging directory under the destination parent, for example
   `/srv/apps/.yeet-migrate-vaultwarden-*`.
2. Copy the full old service root into staging, preserving directory structure
   and file modes.
3. Ensure required subdirectories exist in staging: `bin`, `run`, `env`, and
   `data`.
4. Atomically rename staging to the destination root on the destination
   filesystem.
5. Update `ServiceRoot` in catch DB only after the rename succeeds.
6. Leave the old root in place after a successful migration.

Leaving the old root is intentional. It gives the user a rollback point and
avoids destructive cleanup in the first implementation. A later
`--remove-old` option can remove the old root after verification.

## Command Behavior

Remote service operations should always use the catch DB effective root:

- install and update
- restart, start, stop, rollback, enable, and disable
- Docker compose project directory and command working directory
- systemd service working directory
- generated Python and TypeScript `/data` mounts
- generated image-ref compose `./:/data` behavior
- `yeet copy`
- `yeet ssh <svc>`
- `yeet info`
- `yeet remove`

For `yeet remove`, preserve the effective root's `data/` directory and remove
only yeet-managed non-data children, matching current behavior.

## Errors

Prefer direct errors that identify the authoritative state and the remediation.

Examples:

```text
service root for "vaultwarden" is already /srv/apps/vaultwarden; got /srv/other/vaultwarden
use `yeet service set vaultwarden --service-root=/srv/other/vaultwarden` to migrate it
```

```text
service root parent does not exist: /srv/apps
```

```text
cannot migrate service root while "vaultwarden" is running
```

```text
destination service root is not empty: /srv/apps/vaultwarden
```

## Tests

Add focused tests for:

- `pkg/cli` parsing `--service-root` for `run`.
- `pkg/cli` metadata and parsing for `service set <svc> --service-root=...`.
- `cmd/yeet` bridge behavior for `yeet service set <svc> ...`.
- `pkg/db` clone/view coverage for `ServiceRoot`.
- Catch path resolver defaults and custom roots.
- Initial install persisting a custom root before artifact paths are generated.
- Existing-service `yeet run` accepting matching roots and rejecting mismatches.
- Root validation: absolute path, cleaned path, existing parent, and nested-root
  rejection.
- Migration requiring a stopped service.
- Migration with `--copy` using staged copy and destination rename before DB
  update.
- Migration with `--empty` creating the managed layout.
- Migration preserving the old root after success.
- `yeet.toml` save and rehydrate behavior for `service_root`.
- `yeet info` showing both server root and saved client root when present.
- `yeet remove` preserving `data/` under the effective root.

## Documentation

Update the user-facing docs when this is implemented:

- CLI reference for `yeet run --service-root`.
- CLI reference for `yeet service set`.
- Configuration docs for `service_root` in `yeet.toml`.
- Data layout docs to explain default and custom service roots.
- Operations workflows with an example using a pre-existing parent directory.
