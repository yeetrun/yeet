# NetNS Docker NAT Reconciliation Design

## Goal

Make yeet's Docker network plugin converge service namespace NAT state without
depending on container recreation.

For each yeet-managed service network namespace, the `YEET_PREROUTING` and
`YEET_OUTPUT` DNAT chains should be replayable from yeet DB state at any time.
This should repair NAT-only drift while leaving healthy running containers
alone.

## Problem

After the `pve1` reboot and catch update, `hoarder` was reported as down even
though the compose containers were running and healthy. The failure was in the
service network namespace:

- `catch-hoarder-web-1` answered on its container address.
- `yeet-hoarder-ts.service` repeatedly failed to proxy
  `127.0.0.1:3000`.
- `yeet-hoarder-ns` was missing the DNAT rules that map port `3000` to the
  hoarder web container.
- Recreating only the hoarder web container caused Docker to re-trigger the
  plugin callbacks and restored the missing DNAT rules.

This means yeet currently has a NAT reconciliation gap. A service can have
healthy containers, correct-looking veth links, and still be unreachable because
namespace-local NAT rules drifted or were flushed.

## Root Cause

The Docker network plugin currently flushes namespace-wide NAT chains but
computes the desired rules from one Docker network at a time.

In [pkg/dnet/dnet.go](/Users/shayne/code/yeet/pkg/dnet/dnet.go),
`syncNetNSPortForwards` owns the whole `YEET_PREROUTING` and `YEET_OUTPUT`
chains inside a service netns. It flushes both chains before appending desired
DNAT rules.

Its callers, however, pass `desiredPortForwards(n)` for only the network that
triggered the callback:

- `CreateEndpoint`
- `DeleteEndpoint`
- `Join`
- `Leave`

That is the wrong ownership boundary. If a flush affects a whole network
namespace, the desired input must also represent the whole network namespace.

The live host had multiple persisted hoarder `DockerNetwork` records pointing
at the same `NetNS`, while Docker itself only had one current hoarder network.
That stale state shape makes the bug possible, but the code should be correct
even when stale records exist.

There are two related gaps:

1. Docker calls `/NetworkDriver.RevokeExternalConnectivity` during shutdown,
   but yeet does not register that route, so it falls through to `501 Not
   implemented`.
2. Catch startup reconciliation currently checks for stale veth links and can
   recreate containers, but it does not verify or replay NAT rules when links
   are present.

## Requirements

### Functional

1. Yeet must compute desired port forwards at the service netns level before
   flushing namespace-owned NAT chains.
2. A no-port endpoint event must not wipe another live endpoint's port forward
   in the same netns.
3. Stale port mappings whose endpoint owner no longer exists must not produce
   DNAT rules.
4. Catch startup must be able to replay NAT rules for existing service netns
   objects without recreating Docker containers.
5. Docker's `ProgramExternalConnectivity` callback must update the endpoint's
   port mappings and replay the aggregate netns NAT rules.
6. Docker's `RevokeExternalConnectivity` callback must remove the endpoint's
   port mappings and replay the aggregate netns NAT rules.
7. Reconciliation must be idempotent and deterministic.
8. Existing link-level reconciliation must remain in place for stale netns/veth
   problems.
9. Acceptance must include live verification on `pve1` against affected
   services.

### Non-Goals

1. Do not redesign the Docker network plugin topology.
2. Do not replace netns-local iptables NAT with nftables in this change.
3. Do not force-recreate compose containers as the primary NAT repair.
4. Do not add broad health polling or a new long-running repair loop.
5. Do not perform destructive DB cleanup as part of the core fix.

## Recommended Approach

Use convergent netns-level NAT reconciliation.

Introduce a helper in `pkg/dnet` that derives all desired port forward rules
for a given `NetNS` from the full DB:

1. Iterate all persisted `DockerNetworks`.
2. Select networks whose `NetNS` equals the target namespace.
3. For each selected network, reuse the existing desired-port-forward logic
   that skips stale port owners.
4. Sort and dedupe the resulting rules.
5. Pass that aggregate rule set to `syncNetNSPortForwards`.

All callback paths that can affect endpoint membership or port mappings should
replay this aggregate rule set for the affected netns. The invariant is:

> after a Docker network callback completes, the netns NAT table matches the
> DB-derived desired state for that netns.

This approach is preferred because it fixes the ownership mismatch directly and
does not rely on Docker container churn as a side effect.

## Docker Callback Behavior

### CreateEndpoint

`CreateEndpoint` should continue recording endpoint address and portmap state.
After mutation, it should compute desired rules for the endpoint's `NetNS`
across all DB networks with that `NetNS`, then sync the namespace NAT chains.

### DeleteEndpoint

`DeleteEndpoint` should remove the endpoint and its port mappings if present.
It should be tolerant of mappings already removed by
`RevokeExternalConnectivity`. After mutation, it should replay the aggregate
netns NAT rules.

### Join

`Join` should continue creating and moving the veth pair, ensuring `br0`, and
setting up postrouting. When it syncs DNAT rules, it should use the aggregate
netns rule set instead of the single network's rules.

### Leave

`Leave` should remove the endpoint and its port mappings if present, delete the
veth interface, and replay the aggregate netns NAT rules. It should be tolerant
of endpoint state already adjusted by revoke/delete ordering.

### ProgramExternalConnectivity

`ProgramExternalConnectivity` should parse the same Docker portmap payload used
by `CreateEndpoint`. It should update port mappings for the endpoint, preserve
the endpoint address already recorded by `CreateEndpoint`, then replay aggregate
netns NAT.

If Docker calls this before the endpoint exists in DB, yeet should return a
clear error. That ordering would indicate an unexpected Docker callback
sequence.

### RevokeExternalConnectivity

`RevokeExternalConnectivity` should be explicitly registered. It should remove
only the endpoint's port mappings, leave endpoint/link state to `Leave` and
`DeleteEndpoint`, and replay aggregate netns NAT.

If Docker calls revoke for an unknown endpoint during shutdown, yeet should
avoid making shutdown noisier. The preferred behavior is to return success
after confirming the network exists and there is no mapping to remove.

## Startup Reconciliation

Add a `pkg/dnet` startup reconciliation entrypoint that:

1. Reads DB state.
2. Groups Docker networks by `NetNS`.
3. For each non-empty netns path, checks whether the namespace path exists.
4. Replays aggregate NAT rules inside existing namespaces.
5. Logs and continues for missing namespaces, because service/link
   reconciliation may still recreate containers or namespaces.

Call this from catch startup after Docker/network prerequisites are installed.
This is separate from existing stale-link reconciliation:

- NAT reconciliation repairs missing rules without touching containers.
- Link reconciliation still handles containers attached to stale namespaces.

This keeps the repair precise and minimizes downtime.

On a fresh boot, some service namespaces may not exist when catch first starts.
That is acceptable. Startup NAT reconciliation should repair existing
namespaces, while Docker callbacks remain responsible for programming rules as
containers are attached later in the boot sequence.

## Error Handling

1. Callback handlers should return Docker-compatible errors for malformed
   requests and unexpected network IDs.
2. Unknown endpoints during revoke/delete/leave should be treated as idempotent
   cleanup where safe, especially during Docker shutdown.
3. NAT reconciliation failures should surface clearly in callback responses.
4. Startup NAT reconciliation should log per-netns failures and continue with
   the remaining namespaces.
5. Sorting and dedupe should make repeated reconciliation produce stable output
   and avoid duplicate DNAT rules.

## Implementation Shape

The primary changes should stay in `pkg/dnet`:

- add aggregate desired-rule helpers
- update Docker callback handlers
- add explicit external-connectivity handlers
- add a startup NAT reconciliation function
- extend existing dnet tests

Catch startup should only call the new dnet reconciliation function. Existing
compose link reconciliation in `pkg/svc` and `pkg/catch` should remain focused
on veth/netns attachment drift.

The Docker plugin HTTP server is started from `cmd/catch`, but the
reconciliation logic should live in `pkg/dnet` so both the HTTP callback path
and startup path share the same rule derivation and sync behavior.

## Testing Strategy

### Automated

Add focused tests in `pkg/dnet` for:

1. Aggregating desired rules across multiple DockerNetwork records that share a
   `NetNS`.
2. Skipping stale port map owners.
3. Ensuring a no-port endpoint event does not wipe another live endpoint's
   port in the same netns.
4. `ProgramExternalConnectivity` updating mappings and replaying aggregate
   rules.
5. `RevokeExternalConnectivity` removing mappings and replaying aggregate
   rules.
6. Startup reconciliation grouping by netns and producing deterministic syncs.

Run targeted package tests and then the full suite:

```bash
go test ./pkg/dnet ./pkg/catch ./pkg/svc
go test ./...
```

### Live Verification

After deploying updated catch to `pve1`:

1. Confirm affected services still run:
   - `hoarder`
   - `plex`
   - `prowlarr`
   - `duplicati`
2. Confirm their public Tailscale URLs respond where applicable.
3. Capture hoarder container IDs and uptime before the repair test.
4. Flush only hoarder's netns `YEET_PREROUTING` and `YEET_OUTPUT` chains.
5. Restart `catch.service`.
6. Confirm hoarder's DNAT rules are restored without recreating the hoarder
   containers.
7. Confirm `https://hoarder.shayne.ts.net` responds.
8. Confirm Docker no longer logs
   `NetworkDriver.RevokeExternalConnectivity: Not implemented` during a
   controlled container stop/start.

## Acceptance Criteria

1. Unit tests cover the stale/shared-netns NAT wipe class.
2. `go test ./...` passes.
3. The updated catch binary deployed to `pve1` restores flushed hoarder NAT
   rules on catch restart without recreating hoarder containers.
4. Previously affected services remain online after deployment.
5. Docker lifecycle logs no longer show missing
   `RevokeExternalConnectivity` support.
