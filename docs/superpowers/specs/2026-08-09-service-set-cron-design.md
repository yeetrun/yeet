# Updating Scheduled Services Through `yeet service set`

## Context

Scheduled native services are deployed through the unified run surface:

```bash
yeet run <svc> <payload> --cron="<schedule>"
```

That command is appropriate when the payload is available locally and the
operator is redeploying it. Changing only the schedule should not require the
payload, however, and should not depend on the directory from which the command
is run. The existing `yeet service set` command is the established surface for
mutating an installed service without supplying its payload.

Schedule state is also a workload-mode boundary. An ordinary service must never
become scheduled merely because an operator used a setting flag. Catch must
therefore decide eligibility from the active installed generation, not from
local configuration, historical timers, staged artifacts, or client claims.

## Goals

- Let an operator change the schedule of an existing scheduled native service
  without providing or uploading its payload.
- Use `yeet service set <svc> --cron="<schedule>"` as the sole schedule-only
  mutation command.
- Preserve the installed payload and every setting other than the schedule.
- Prevent ordinary native, container, VM, missing, historical-only, and
  staged-only services from being converted to scheduled services.
- Keep Catch authoritative while synchronizing a matching local `yeet.toml`
  entry after remote success.
- Preserve the existing `manage` permission boundary and per-service operation
  serialization.
- Keep help, README examples, the website manual, and release notes aligned.

## Non-Goals

- Adding a `yeet set` alias or restoring the removed top-level `yeet cron`
  command.
- Creating a scheduled service without a payload.
- Clearing a schedule or converting a scheduled service back to an ordinary
  service. Once scheduled, the service remains scheduled.
- Changing the payload, payload kind, arguments, identity, storage, network,
  environment, publication, or snapshot policy in the same operation.
- Editing a timer in place outside Catch's generation and rollback lifecycle.
- Starting the payload merely to validate or apply a schedule change.

## Command Contract

`yeet service set` gains one mutation flag:

```text
--cron="<five-field crontab>"
```

Example:

```bash
yeet service set reports --cron="30 2 * * *"
```

The value is required, must be non-empty, and must pass the same schedule
parser and normalization used by `yeet run --cron`. There is no reset or clear
form. Repeating `--cron` is invalid.

Schedule mutation is exclusive with every other `service set` mutation flag.
Targeting and presentation options, such as host selection or output format,
remain available. An operator who also needs to change identity, networking,
storage, publication, or snapshots performs that mutation in a separate
command. This keeps the schedule transaction narrow and prevents an unrelated
setting from partially succeeding.

The command remains on the existing remote `service set` route and requires
`manage`. It does not add a new RPC method or permission class.

## Eligibility and Authoritative State

Catch validates eligibility while holding the target service's operation lock.
The active installed generation must have both:

- a native service unit; and
- the corresponding active-generation timer artifact and scheduled metadata.

The active generation is the only source of truth. A timer belonging only to
an older generation, a staged transaction, an abandoned file, or local
`yeet.toml` cannot make the service eligible.

Catch rejects the request before mutation when the target is:

- missing;
- an ordinary native service;
- a Docker or Compose service;
- a VM;
- inconsistent or unreadable; or
- represented as scheduled only by historical or staged state.

Rejection explains that `--cron` only updates an already scheduled native
service and does not convert workload type. A failed request leaves units,
artifacts, database records, generation, and local configuration unchanged.

## Mutation Transaction

After validation, Catch creates a normal new service generation from the
active generation's server-side state. It reuses the installed payload and
preserves its checksum, kind, arguments, identity, roots, environment,
publication, snapshot settings, network configuration, and runtime artifacts.
Only the normalized schedule changes.

Under the existing per-service operation lock, Catch:

1. Loads and validates the active scheduled-native generation.
2. Parses and normalizes the requested schedule.
3. Returns success without creating a generation when the schedule is already
   equal after normalization.
4. Stages a replacement generation that reuses the active server-side payload
   and configuration.
5. Installs and verifies the replacement timer and service unit through the
   existing scheduled-native lifecycle.
6. Commits the new generation only after verification.
7. Rolls back to the previous generation on failure, retaining enough state for
   a safe retry when cleanup is incomplete.

The operation must not upload a payload, resolve a local payload path, or run
the workload as part of schedule validation. It may perform the unit lifecycle
actions required to install the new generation, but it does not intentionally
trigger the scheduled payload.

## Client and Configuration Flow

The client uses the existing service-command bridge:

1. Parse and validate the exclusive `--cron` mutation.
2. Send `service set <svc> --cron=<schedule>` to Catch.
3. Treat Catch's result as authoritative.
4. After remote success, update only `schedule` in the matching `yeet.toml`
   service entry.

No project file or local payload is required to perform the remote mutation.
If no matching project entry exists, the remote change succeeds and the client
prints guidance to run:

```bash
yeet service sync <svc>
```

If the project entry exists but cannot be saved, the client reports that the
remote schedule changed while local synchronization failed and gives the same
recovery command. Local synchronization never rewrites payload arguments or
other service fields.

## Errors and Compatibility

- `yeet run <svc> <payload> --cron="..."` remains the deployment and
  redeployment surface when a payload is supplied.
- Config-only `yeet run <svc>` continues to rehydrate the stored schedule and
  payload through the unified run pipeline.
- `yeet service set <svc> --cron="..."` is the payload-free schedule-only
  mutation surface.
- Older Catch servers that do not recognize the flag return the normal upgrade
  guidance; the client must not emulate the mutation by redeploying.
- Direct or older clients cannot bypass the Catch-side active-generation and
  permission checks.

## Verification

Tests cover:

- CLI help, parsing, normalization, empty/repeated values, and exclusivity;
- client bridge routing and `manage` permission classification;
- local configuration update, missing-entry guidance, and save-failure partial
  success reporting;
- active scheduled-native success and normalized no-op behavior;
- preservation of payload checksum, arguments, identity, storage, network,
  environment, publication, and snapshot state;
- rejection of ordinary native, Docker, VM, missing, historical-only, and
  staged-only services without state changes;
- rollback and retry behavior when replacement installation fails; and
- generated help, README, website manual, and release-asset consistency.

Before release, a disposable scheduled native service uses a far-future
schedule on an isolated network. The smoke test changes its schedule through
`yeet service set`, verifies the installed timer and local config changed,
proves no workload execution occurred, verifies an ordinary disposable service
is rejected without conversion, and removes all disposable units, networking,
storage, and configuration. The existing `<protected-scheduled-service>` is not run or used
as the mutation target.

## Release Integration

This feature is included in the pending `v0.10.16` patch release. The latest
changelog entry must describe both the unified scheduled-run workflow and the
payload-free `yeet service set <svc> --cron="..."` update path in user-facing
terms. The website commit, root gitlink, release commit, annotated tag,
published artifacts, checksums, embedded version metadata, and live Catch
upgrade are verified as one release candidate after the implementation and
smoke test are complete.
