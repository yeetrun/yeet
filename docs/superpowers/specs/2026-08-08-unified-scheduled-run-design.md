# Unified Scheduled Workloads Through `yeet run`

## Goal

Make scheduling an immutable native-workload mode selected through
`yeet run --cron=<expression>`, remove the separate `yeet cron` command, and
let scheduled native services use the same identity, network, storage,
snapshot, payload, and configuration paths as every other `yeet run`
deployment.

The motivating service is `owesplit` on `yeet-pve1`. It is a native systemd
oneshot with a timer, runs as `yeet-svc:yeet-svc`, and uses ISO networking. Its
server-side generation is healthy, but its local payload is missing and the
split client command model caused `yeet run` to misclassify the missing binary
as a local container image.

## Selected Approach

Scheduling is an attribute of a native `run` deployment, not a separate
payload kind or top-level command.

```bash
yeet run owesplit ./owesplit/owesplit \
  --cron="0 9 15 * *" \
  --run-as=yeet-svc \
  --net=iso \
  -- -live
```

The change is intentionally greenfield:

- remove `yeet cron` from client and Catch command registries;
- remove its parser, bridge, remote dispatcher, help, examples, and tests;
- do not retain an alias, deprecation shim, or old wire-command handler; and
- make old `type = "cron"` project entries invalid rather than maintaining a
  compatibility mode.

Two alternatives were rejected. Keeping an internal cron command behind
`run` would preserve duplicate parsing and installation flows. Keeping
`type = "cron"` beside `schedule` would preserve two sources of truth for the
same workload mode.

## Command Contract

`yeet run` gains a presence-aware string flag:

```text
--cron="<five-field crontab expression>"
```

The value must contain exactly five cron fields and must pass the existing
cron-to-systemd-calendar validation. An explicitly empty value is invalid.
Arguments after `--` remain payload arguments and are never interpreted as
Yeet flags.

Examples:

```bash
yeet run backup ./backup --cron="0 3 * * *"
yeet run backup ./backup --cron="30 2 * * 1" --run-as=backup -- -full
yeet run report ./report --cron="0 9 1 * *" --net=iso -- -live
```

The operation remains a `manage` permission because it installs or changes a
service. No new RPC method or permission class is introduced; the existing
remote command transport forwards the extended `run` command.

## Immutable Scheduled Mode

A native service becomes scheduled when it is first created or updated with
`--cron`. Once Catch has committed a timer for a service, that service remains
scheduled for its lifetime.

- Supplying `--cron` for an ordinary native service converts it into a
  scheduled native service.
- Supplying a different `--cron` value for a scheduled service replaces its
  schedule.
- Omitting `--cron` when redeploying a scheduled service preserves the
  committed schedule.
- Omitting `--cron` for a new or ordinary service retains ordinary continuous
  service behavior.
- There is no reset, clear, disable, or conversion-back flag. Converting a
  scheduled service into an ordinary service requires removing and recreating
  it.
- A scheduled service may only receive a native binary or script payload.
  Compose files, images, Dockerfiles, generated Python or TypeScript
  containers, and VMs are rejected before activation.

The client enforces these rules when it has enough local and service-info
context. Catch repeats the authoritative checks against the installed service
record and timer artifacts so an older or direct client cannot change the
workload kind accidentally.

## Unified Client Flow

Scheduling becomes orthogonal to payload kind in the run draft. A native file
with a schedule is a scheduled workload; `cron` is no longer a payload kind.

For an explicit payload update such as:

```bash
yeet run owesplit ./owesplit/owesplit
```

the client loads the matching project entry and rehydrates its stored
schedule, run identity, network settings, storage, snapshots, and payload
arguments. It validates the payload as an existing native file and executes
the ordinary run pipeline with the effective schedule.

For a config-only update:

```bash
yeet run owesplit
```

the same run pipeline resolves the configured payload and effective fields.
There is no type-based redirect to another command.

Payload normalization uses the effective native scheduled mode before image
heuristics. A missing configured or explicit file therefore reports the
actual `stat`/missing-file error. It cannot fall through to local-image
classification and produce container-specific `--run-as` guidance.

The web deploy UI follows the same model: scheduling is an optional field on a
native binary or script deployment, not a separate scheduled-job payload
card. Web and CLI drafts share validation and execution.

## Project Configuration

The project entry uses `schedule` as the sole local marker for scheduled mode:

```toml
[[services]]
name = "owesplit"
host = "yeet-pve1"
payload = "owesplit/owesplit"
run_as = "yeet-svc"
service_root = "flash/yeet/services/owesplit"
service_root_zfs = true
schedule = "0 9 15 * *"
args = ["--net=iso", "--", "-live"]
```

`type = "cron"` is removed. A non-empty valid schedule means scheduled; an
entry without a schedule is ordinary unless its live Catch service is already
scheduled, in which case Catch's immutable-mode rule prevents accidental
conversion and the client reports the configuration drift.

Run control flags and payload arguments continue using the established
`args` representation, but the `--` boundary is preserved whenever payload
arguments follow control flags. This makes `--net=iso` a Yeet network setting
and `-live` an application argument. Info rendering and change detection split
the stored arguments at that boundary.

Saving a scheduled deployment records the normalized five-field schedule.
Updating a scheduled entry without an explicit `--cron` preserves its
schedule. `service set` and `service sync` use the normal run-argument rewrite
path, so network settings no longer enter a cron-only payload-argument array.

## Catch Installation Flow

The Catch `run` parser receives the schedule and its presence bit. The native
file installer receives a timer configuration in the same request as the
payload, identity, network, storage, and service arguments.

For a new scheduled service, Catch:

1. detects and validates a native binary or script;
2. validates and converts the cron expression;
3. resolves the requested or managed native identity;
4. prepares the requested network, including native ISO when selected;
5. stages the binary, systemd service, timer, and network artifacts as one
   generation; and
6. activates and commits the generation through the existing installer and
   rollback machinery.

For an existing scheduled service, an omitted schedule is reconstructed from
the authoritative committed timer rather than treated as a request to remove
it. A supplied schedule stages a replacement timer. Existing identity and
network mutation guards remain in force: unchanged values may accompany a
redeploy, while an actual existing-service network change still goes through
`yeet service set`.

If validation, staging, activation, or verification fails, the existing
generation and network rollback remains authoritative. No partial timer,
identity, or ISO state may be reported as active.

## Status and Info

User-facing status continues to call a native service with a committed timer
a `cron service`. This is an observed runtime description, not a deploy
command or project payload type.

`yeet info` separately reports:

- the saved schedule from `yeet.toml`;
- the effective server timer and next activation;
- the saved and effective run identity;
- client control flags versus payload arguments; and
- the effective network, including ISO allocation and state.

When local configuration omits the schedule for a live scheduled service,
info reports drift and gives the concrete `schedule = "..."` correction. It
does not suggest `yeet cron`.

## Testing

Implementation follows test-driven development. Focused regressions cover:

- `ParseRun` accepting one non-empty `--cron` value and rejecting malformed,
  empty, or repeated values;
- top-level and bridge help exposing `run --cron` and containing no `cron`
  command;
- remote authorization classifying scheduled run as `manage`;
- run drafts treating schedule as independent of native payload kind;
- explicit and config-only `yeet run` preserving a stored schedule;
- a supplied schedule updating an existing scheduled service;
- Catch preserving an installed timer when `--cron` is omitted;
- Catch rejecting scheduled-to-ordinary and scheduled-to-non-native
  conversions;
- native `--cron`, `--run-as`, and `--net=iso` reaching one installer request;
- project configuration round trips without `type = "cron"` and with a
  preserved `--` argument boundary;
- `service set` and `service sync` keeping network controls distinct from
  payload arguments;
- missing scheduled payloads producing a file error rather than image or
  container guidance; and
- web deploy using the native-plus-schedule model.

Focused package tests run throughout implementation. Before committing the
code, run the full Go suite and pre-commit. Because this changes the parser,
RPC command boundary, native service orchestration, and ISO behavior, run the
repository's destination quality gate on the stable candidate.

## Documentation

Update CLI help, generated agent help, README examples, and the website manual
to make `yeet run --cron` the only scheduled deployment workflow. Remove all
`yeet cron` examples and describe scheduled mode as immutable until service
removal. Document that native scheduled workloads support `--run-as` and the
native-compatible network modes, including ISO.

## Live `owesplit` Recovery and Verification

After tests pass:

1. Copy the active binary generation from
   `/flash/yeet/services/owesplit/bin/owesplit-20260103153229` on `root@pve1`
   to `~/yeet-services/owesplit/owesplit`.
2. Verify its SHA-256 is
   `bf3625a463858bde7e4b367fbf6eaa43d63d35f8bafbbbec950606d70c7a39ee`.
3. Rewrite the `owesplit` project entry to remove `type = "cron"`, restore
   `run_as = "yeet-svc"`, retain the schedule, and canonically store
   `--net=iso -- -live`.
4. Install the updated Catch build on `yeet-pve1` and use the matching client
   to deploy the restored payload through `yeet run --cron`.
5. Verify the committed unit and timer, executable checksum, `User` and
   `Group`, data-directory access, timer schedule, namespace attachment, ISO
   ready state, IP allocation, and public-only DNS.
6. Do not manually start the job because `-live` may cause external side
   effects. Successful timer installation and non-executing runtime checks are
   the live acceptance boundary.

The work is complete when `owesplit` has a healthy waiting timer, executes the
restored binary as `yeet-svc:yeet-svc` inside its existing ISO network, its
local configuration can reproduce that state through `yeet run`, and no
client, Catch, help, test, or documentation surface retains `yeet cron`.
