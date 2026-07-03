# Fast status host snapshots

## Goal

`yeet status` should stay fast as a host accumulates services, and `yeet status
--format=json` should work from a multi-host `yeet.toml` workspace. The command
should collect runtime state once per host, map that state onto configured yeet
services, and preserve the existing table and JSON output shape.

## Current Findings

In `/Users/shayne/yeet-services`, plain `yeet status` rendered 38 services
across `yeet-lab` and `yeet-cloud` in roughly 1.1 to 1.8 seconds. Timed host
runs showed `yeet-lab` dominates at roughly 1.15 to 1.62 seconds, while
`yeet-cloud` finishes in roughly 0.36 to 0.42 seconds.

The client already queries configured hosts concurrently for table status, so
the main delay is catch-side collection on each host. On `yeet-lab`, catch
currently checks service groups serially and checks Docker Compose services by
running one `docker compose ps` command per configured compose service. A
single host-level `docker ps -a --filter label=com.docker.compose.project
--format "{{json .}}"` returned all compose containers there in roughly 0.07
to 0.12 seconds.

There is also a correctness bug: from a multi-host project, `yeet status
--format=json` falls through to the single remote system-service path instead
of the multi-host aggregation path, and can fail with `remote exit 1`.

## Behavior

For no-service status requests, table, `json`, and `json-pretty` formats should
all use the same project-aware host aggregation path. If `yeet.toml` lists
multiple hosts, JSON output should be an array of host objects:

```json
[
  {
    "host": "yeet-cloud",
    "services": []
  },
  {
    "host": "yeet-lab",
    "services": []
  }
]
```

Single-service status should keep its existing behavior: it asks the selected
host for that service's status and renders that service view.

The catch-side status command should keep returning the existing service status
objects. Docker services with no matching containers remain `unknown`, matching
the current user-visible behavior. Systemd, cron, and VM statuses continue to
map to `running`, `stopped`, or `unknown` using the same component model.

Authorization remains `read`. No new command should require `manage` or `ssh`.

## Architecture

### Client Routing

Update `pkg/yeet` status routing so any supported render format with no service
override calls `statusMultiHost(ctx, statusHosts(...), flags)`. The routing
decision should not be tied to table rendering. The existing `statusMultiHost`
JSON encoder can stay as the output path.

Unsupported formats should continue to flow through existing parser and remote
error behavior unless this work uncovers an existing format validation pattern
that should be reused.

### Catch Status Snapshot

Introduce a catch-side host status snapshot collector used by
`ttyExecer.systemStatusData`. The collector should read the catch DB once,
derive the configured services by type, collect host runtime state in bulk, and
then assemble `[]ServiceStatusData`.

The collector should keep small, testable units:

- Docker runtime snapshot: collect all compose containers with one Docker CLI
  command and parse compose project and service labels.
- Systemd runtime snapshot: collect configured systemd unit states in one
  batched systemd call.
- VM runtime snapshot: collect configured VM systemd unit states in the same
  batched systemd machinery, using `yeet-vm-<name>.service`.
- Status assembly: map runtime snapshots back to configured service names and
  output the existing component status structs.

### Docker Snapshot

Use host-level Docker state instead of per-service compose commands. Docker
containers created by yeet compose services already carry
`com.docker.compose.project` and `com.docker.compose.service` labels. The
collector should:

1. Run `docker ps -a --filter label=com.docker.compose.project --format
   "{{json .}}"`.
2. Parse each JSON line.
3. Keep rows whose compose project name matches the yeet project convention
   `catch-<service>`.
4. Group by yeet service name, with component names from
   `com.docker.compose.service`.
5. Map Docker states through the existing `dockerComposeStateStatus` semantics.

This avoids reading compose files, syncing `.env`, and spawning one compose
process per service just to observe status.

### Systemd And VM Snapshot

Use one batched systemd query for configured systemd services, cron services,
and VMs. The implementation should use `systemctl show
--property=Id,LoadState,ActiveState,SubState <units...>` so catch can distinguish
active units, inactive units, failed units, and configured units that are not
loaded without spawning one `systemctl is-active` process per service.

The assembly layer should translate:

- `ActiveState=active` to `running`
- inactive, failed, deactivating, and not-found units to `stopped` when the
  old path would have returned stopped
- malformed or missing command output to `unknown` only when the old path would
  have treated the status as unknown

Systemd service unit naming should continue to come from `SystemdService` so
timer-backed cron services use the timer unit and binaries use the service
unit. VM units should continue to use `vmSystemdUnitName`.

## Data Flow

1. `cmd/yeet` routes `status` to `pkg/yeet`.
2. `pkg/yeet` parses status flags and detects whether a service override is
   present.
3. For no-service table or JSON formats, `pkg/yeet` gets the project hosts and
   calls `statusMultiHost`.
4. `statusMultiHost` concurrently fetches `status --format=json` from each
   host's catch system service.
5. Catch builds one host status snapshot from DB, Docker, and systemd state.
6. Catch renders the existing service status JSON.
7. The client aggregates host results, sorts hosts and rows as it does today,
   and renders table or JSON.

## Error Handling

Docker command failure should still fail host status with context. This keeps
real host-level Docker breakage visible.

Systemd command failure should fail host status with context if the command
itself cannot run. Individual missing units should map through the existing
status semantics instead of failing the whole command.

Malformed Docker JSON lines should not crash the whole host if the parser can
skip them with a log entry and still return valid rows, matching the current
compose status parser's tolerance for malformed lines. If the entire Docker
output is unusable, the host status should fail.

If one host fails in a multi-host request, the current client behavior of
returning the first host error can remain unchanged.

## Testing

Add focused unit tests in the existing packages:

- `pkg/yeet`: no-service `--format=json` and `--format=json-pretty` use
  `statusMultiHost` and produce host-grouped JSON from project hosts.
- `pkg/catch`: Docker JSON-line parsing maps compose labels to yeet service
  names and preserves running/stopped/unknown state mapping.
- `pkg/catch`: host snapshot assembly marks configured Docker services without
  containers as unknown.
- `pkg/catch`: batched systemd output maps binary, cron, and VM services to the
  same component statuses as the old single-service checks.
- Existing single-service status tests keep passing.

## Verification

Run targeted tests first:

```bash
mise exec -- go test ./pkg/yeet ./pkg/catch ./pkg/svc
```

Then run the full suite before integration:

```bash
mise exec -- go test ./...
```

For live verification, run from `/Users/shayne/yeet-services`:

```bash
/usr/bin/time -p yeet status
/usr/bin/time -p yeet status --format=json >/tmp/yeet-status.json
/usr/bin/time -p yeet --host yeet-lab status --format=json >/tmp/yeet-lab-status.json
```

The JSON command should succeed for the multi-host workspace. The plain status
command should be materially faster than the current 1.1 to 1.8 second range,
with the biggest improvement on `yeet-lab`.

## Documentation

No user-facing syntax changes are planned. If the manual or help text currently
documents JSON status output, update it only if needed to clarify the
multi-host host-grouped shape.

## Out Of Scope

This design does not add a new RPC method, status cache, streaming status
protocol, partial-host output mode, or Docker health reporting. It only changes
how catch collects the same status data and fixes client routing for existing
JSON formats.
