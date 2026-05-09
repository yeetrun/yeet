# Docker Outdated Design

## Goal

Add `yeet docker outdated` to report Docker compose services whose running
containers are behind the current upstream registry image, without pulling
images or restarting services.

The command should feel like `yeet status`: host-aware, sorted, readable in a
table by default, and able to run across all configured hosts in parallel.

## Requirements

### Functional

1. `yeet docker outdated` checks Docker compose services across all configured
   hosts from `yeet.toml`, or the current `CATCH_HOST` when no project config
   hosts are available.
2. Host checks run concurrently, matching the existing `yeet status` fan-out
   behavior.
3. `yeet docker outdated <svc>` checks only one service.
4. `yeet docker outdated <svc>@<host>` and `docker@<host> outdated <svc>` use
   the existing service and command host routing.
5. The command is read-only. It must not run `docker pull`, `docker compose up`,
   `docker compose update`, or any command that refreshes local images or
   changes containers.
6. Outdated means the running container image digest differs from the current
   upstream registry digest for the compose-declared image reference.
7. Default table output includes only interesting rows: updates, unknowns, and
   errors. Current images are omitted.
8. JSON output returns the same filtered row set as the table output.
9. Internal yeet registry images under `catchit.dev/...` are not upstream
   update candidates. Host-wide output skips them; scoped output returns an
   `unknown` row with an internal-image reason.

### Non-Goals

1. Do not implement update automation in this command.
2. Do not add a new first-class JSON-RPC method unless the existing
   `catchrpc.Exec` path proves insufficient.
3. Do not add Watchtower, Diun, or another external updater dependency.
4. Do not attempt to solve every registry authentication edge case in the first
   implementation. Auth failures should be reported clearly.
5. Do not report non-Docker service freshness.

## Recommended Approach

Add a read-only catch-side Docker inspector behind a new remote group command,
`docker outdated`, and add a local yeet-side multi-host renderer for the
unscoped form.

This follows existing repository guidance:

- `yeet` handles global routing, host fan-out, and user-facing rendering.
- `catch` remains authoritative for remote command and flag parsing.
- Docker behavior stays in catch/pkg service code near existing compose
  lifecycle helpers.
- The existing RPC bridge carries the command through `catchrpc.Exec`.

The design intentionally avoids `docker compose pull`. Docker documents
`compose pull` as the command that pulls service images. The read-only data
sources are `docker compose config --images`, `docker compose ps --format=json`,
local image/container inspection, and registry manifest inspection.

Reference docs:

- https://docs.docker.com/reference/cli/docker/compose/pull/
- https://docs.docker.com/reference/cli/docker/compose/config/
- https://docs.docker.com/reference/cli/docker/compose/ps/
- https://docs.docker.com/reference/cli/docker/buildx/imagetools/inspect/

## User Interface

### Commands

```bash
yeet docker outdated
yeet docker outdated <svc>
yeet docker outdated <svc>@<host>
yeet docker@<host> outdated <svc>
yeet docker outdated --format=json
yeet docker outdated --format=json-pretty
```

### Table Output

Default output:

```text
SERVICE   HOST        CONTAINER   IMAGE                 RUNNING          LATEST           STATUS
web       host-a      app         ghcr.io/acme/app:v1   sha256:old...    sha256:new...    update available
media     host-b      server      linuxserver/foo:latest sha256:abc...    -                unknown: registry auth failed
```

Only update, unknown, and error rows are shown. If no rows match, the command
prints the header only.

### JSON Output

JSON returns a list of host result objects, each with filtered rows:

```json
[
  {
    "host": "host-a",
    "containers": [
      {
        "serviceName": "web",
        "containerName": "app",
        "image": "ghcr.io/acme/app:v1",
        "runningDigest": "sha256:old",
        "latestDigest": "sha256:new",
        "status": "update available"
      }
    ]
  }
]
```

Catch-side scoped JSON returns the filtered container rows for that one service.
Local unscoped JSON wraps those rows by host, mirroring `yeet status`.

## Data Flow

For each Docker compose service on catch:

1. Resolve compose-declared image references with `docker compose config
   --images`.
2. List compose containers with `docker compose ps --format=json`.
3. Inspect running containers/images to find the current image identity and
   digest.
4. Resolve the upstream registry digest for each declared image reference
   without pulling.
5. Compare running digest to latest upstream digest.
6. Keep only rows with `update available`, `unknown`, or `error`.
7. Render or encode the filtered rows.

Host-wide checks iterate only Docker compose services from the catch database.
Scoped checks first confirm the requested service exists and is a Docker compose
service.

## Digest Resolution

The comparison should prefer immutable digests:

1. If the running container was created from an image with a repo digest for the
   declared reference, use that digest as `runningDigest`.
2. If the container image only resolves to a local image ID and no repo digest
   is available, return an `unknown` row rather than guessing.
3. Resolve upstream digest using Docker registry metadata inspection, preferably
   `docker buildx imagetools inspect --raw` or `docker manifest inspect` through
   a small wrapper that can be unit tested.
4. For multi-platform manifest lists, verify the upstream reference includes
   the host platform, then compare the upstream reference/index digest to the
   running `RepoDigests` entry. Do not compare a repository digest to a platform
   child manifest digest.
5. Digest-pinned compose images (`image@sha256:...`) are current if the running
   digest matches the pinned digest. They are not checked for tag drift.

## Error Handling

Per-image failures should become row-level output when possible:

- registry auth failure
- unsupported registry output
- missing running digest
- missing declared image
- unsupported multi-platform comparison

Host-level failures should behave like `yeet status`: if a host cannot be
queried, the command returns an error for that host fetch. Scoped mode can
return direct command errors because the user asked about one service.

Non-Docker scoped services return:

```text
service "name" is not a docker compose service
```

Host-wide output skips non-Docker services.

## Components

### `pkg/cli`

Add Docker outdated metadata and parsing:

- `DockerOutdatedFlags`
- `ParseDockerOutdated`
- `docker outdated` in remote group command metadata
- flag specs for `--format`

Supported formats: `table`, `json`, `json-pretty`.

### `cmd/yeet`

Add `outdated` to the Docker group handler map so the command routes through
the same bridge as `docker pull` and `docker update`.

Ensure `docker push` remains local-only.

### `pkg/yeet`

Add local handling for unscoped `docker outdated`:

- choose hosts using the same project-config logic as `status`
- fetch each host concurrently
- request catch JSON with `docker outdated --format=json`
- render a host-aware table by default
- encode host-grouped JSON for JSON formats

Scoped commands should continue through the normal service bridge.

### `pkg/catch`

Add catch-side command handling:

- `docker outdated` dispatch
- service type filtering
- row rendering and JSON encoding
- row-level unknown/error handling

### `pkg/svc`

Add Docker compose inspection helpers:

- declared images from compose config
- compose container listing from compose ps JSON
- container/image digest inspection
- upstream registry digest inspection

Keep command construction injectable so tests can prove the command is
read-only.

### Documentation

Update user-facing docs in the implementation session:

- README Docker quickstart note
- website CLI docs for `yeet docker outdated`
- relevant operation/workflow docs if they mention Docker update checks

## Testing Strategy

### CLI Routing

1. `yeet docker outdated` routes to local multi-host handling without requiring
   a service name.
2. `yeet docker outdated <svc>` bridges the service argument.
3. `yeet docker outdated <svc>@<host>` sets the host override.
4. `yeet docker@<host> outdated <svc>` works with group host routing.
5. `docker push` remains local-only.

### Catch and Service Logic

1. Parse `docker compose config --images` output.
2. Parse `docker compose ps --format=json` output.
3. Detect update/current/unknown/error rows.
4. Skip internal registry images in host-wide mode.
5. Include internal registry unknown rows in scoped mode.
6. Reject scoped non-Docker services.
7. Verify the command plan never invokes pull/up/update.

### Local Rendering and Fan-Out

1. Fetch multiple hosts concurrently.
2. Sort host and row output deterministically.
3. Render only interesting rows.
4. Encode JSON and JSON-pretty output.
5. Preserve useful errors for failed host fetches.

### Verification Commands

Run targeted tests for touched packages, then the full suite:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch ./pkg/svc
go test ./...
```

Because this touches CLI behavior and Docker command safety, run the repo's
normal local quality gate before committing implementation changes:

```bash
pre-commit run --all-files
```

## Acceptance Criteria

1. `yeet docker outdated` checks all configured hosts in parallel and prints a
   status-like table of only updates, unknowns, and errors.
2. `yeet docker outdated <svc>` checks only that service.
3. No check path pulls images, recreates containers, restarts services, or
   mutates catch state.
4. Up-to-date images are omitted from default output.
5. JSON formats are available and deterministic.
6. Internal yeet registry images do not produce false update reports.
7. Tests cover routing, parsing, digest comparison, rendering, and read-only
   command safety.
