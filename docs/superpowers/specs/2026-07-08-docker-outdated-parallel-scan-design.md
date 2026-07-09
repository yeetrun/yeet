# Docker Outdated Parallel Scan Design

## Goal

Make the normal review workflow fast:

```bash
yeet docker outdated
yeet docker update --outdated
```

The first command should be quick enough to run before deciding whether to
batch-update services. The second command should benefit from the same faster
discovery path without changing which services are considered updateable.

## Context

The existing Docker outdated design defines the command semantics: catch checks
Docker compose services without pulling images, compares running digests with
upstream registry digests, omits current rows, and reports per-service scan
issues as rows when possible.

The current implementation already fans out hosts concurrently in the client.
The slow path is inside one catch host: `Server.DockerComposeOutdatedAll` scans
Docker compose services one at a time.

## Live Measurements

Measurements were taken from a service workspace against a live catch host with
23 Docker services.

| Probe | Time |
| --- | ---: |
| Current `yeet --host=<catch-host> docker outdated` | 23.47s |
| Current host-wide scan after warm probes | 23.59s |
| Serial service-scoped scans across all Docker services | 25.08s |
| Service-scoped scans with 4 workers | 6.84s, repeat 7.14s |
| Service-scoped scans with 8 workers | 3.18s, repeat 3.64s |
| Service-scoped scans with 12 workers | 2.49s, repeat 2.39s |
| Service-scoped scans with 16 workers | 2.64s |
| `docker buildx imagetools inspect` for 29 unique images, serial | 14.50s |
| Same image digest probe with 4 workers | 3.70s |
| Same image digest probe with 8 workers | 1.88s |
| Same image digest probe with 12 workers | 1.55s |
| `docker inspect` for 29 containers, serial | 0.34s |

The results point at registry digest checks and serial per-service orchestration
as the dominant cost. Docker daemon metadata calls are not the bottleneck.

## Recommended Design

Add bounded catch-side concurrency to host-wide Docker outdated scans.

`Server.DockerComposeOutdatedAll` should:

1. Load the catch database and collect Docker compose service names as it does
   today.
2. Scan services concurrently with a fixed internal worker limit.
3. Convert each per-service scan failure into the same error row shape used
   today.
4. Wait for all started service scans to finish.
5. Sort rows with the existing Docker outdated sort before returning.

The initial worker limit should be 8 per host. Live probes showed most of the
gain by 8 workers, with only a small additional gain at 12 and no gain at 16.
Eight is also a conservative default for avoiding unnecessary pressure on
Docker, DNS, and upstream registries.

This limit should be an internal constant for the first implementation, not a
CLI flag. Users should get faster default behavior without another tuning knob.

## Data Flow

The client flow remains unchanged:

1. `yeet docker outdated` resolves the configured host set.
2. The client queries hosts concurrently.
3. Each host runs `docker outdated --format=json` through the existing catch
   command path.
4. The client renders the existing host-aware table or JSON output.

The catch host flow changes only at the all-services boundary:

1. `docker outdated` on the system service calls `DockerComposeOutdatedAll`.
2. `DockerComposeOutdatedAll` distributes Docker compose service names to a
   bounded worker group.
3. Each worker calls the existing scoped `DockerComposeOutdated` logic for a
   service.
4. Returned rows and per-service error rows are collected and sorted.

Scoped `yeet docker outdated <svc>` should continue to use the existing single
service path. It can share helper code if the implementation needs it, but
service-scoped speed is not the primary goal.

## Error Handling

Host-wide scans should preserve current behavior:

- A service scan failure becomes one `DockerOutdatedError` row with the service
  name and error reason.
- One failed service does not cancel other service scans.
- A database load failure remains a host-level error.
- Context cancellation should stop pending and in-flight worker work promptly
  and return the context error instead of partial rows.

Worker goroutines must not write directly to output. They should return rows to
the coordinator so sorting and rendering remain deterministic.

## `docker update --outdated`

`yeet docker update --outdated` already calls the outdated scan first, then
updates services with `update available` rows. The faster host-wide scan should
make the review and update workflows faster without changing update selection.

The update phase itself should remain sequential in this design. Updating
services is mutating and riskier than scanning. Parallel updates can be a
separate design if needed later.

## Alternatives Considered

### Host-wide Docker batching

A deeper redesign could collect Docker compose, container, image, and registry
state once per host, then resolve every service from that shared snapshot. This
might eventually be faster and reduce duplicated Docker commands, but it has a
larger correctness surface around compose project boundaries, internal images,
and row-level errors.

### Cross-run cache

A cache of upstream image digests across invocations could make repeated scans
faster, but it introduces freshness and invalidation semantics. The live
measurements show bounded concurrency already turns the common case from about
24 seconds into a few seconds, so a persistent cache is not needed for the first
pass.

### Unbounded concurrency

Unbounded service or image checks are not appropriate. The live probes showed
no meaningful improvement beyond 12 concurrent service scans, and unbounded
registry calls would be harder to reason about under failures or rate limits.

## Testing

Add focused tests near the changed catch behavior:

1. Host-wide Docker outdated scans run multiple services concurrently.
2. The worker limit is respected.
3. Per-service failures become sorted error rows and do not fail the whole
   host scan.
4. Database load failures still return a host-level error.
5. Context cancellation stops work and returns a cancellation error.
6. Output ordering remains deterministic after concurrent scans.

Existing tests for `pkg/svc`, `pkg/catch`, and `pkg/yeet` should keep covering
digest comparison, internal image handling, table rendering, JSON rendering,
and `docker update --outdated` service selection.

## Verification Plan

Implementation verification should include:

```bash
mise exec -- go test ./pkg/catch ./pkg/svc ./pkg/yeet -run 'Test.*Docker' -count=1
mise exec -- go test ./pkg/catch ./pkg/svc ./pkg/yeet -count=1
```

Then run live read-only checks from a service workspace:

```bash
/usr/bin/time -p yeet --host=<catch-host> docker outdated
/usr/bin/time -p yeet docker outdated
```

The expected result is no output-shape change and a substantial reduction in
host-wide scan time on the observed multi-service host, with a target around
3-5 seconds for the observed service set.
