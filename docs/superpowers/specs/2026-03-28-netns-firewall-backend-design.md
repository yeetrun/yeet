# NetNS Firewall Backend Design

## Goal

Modernize yeet's service-network firewall management for new Debian and Ubuntu
hosts without changing the existing netns topology or regressing performance.
The design should prefer native `nft`, support compatibility fallback via
`iptables-nft`, and keep `iptables-legacy` only as a last-resort compatibility
path.

## Context

Today the main `svc` netns path is implemented by:

- [pkg/netns/netns-scripts/yeet-ns](/Users/shayne/code/yeet/pkg/netns/netns-scripts/yeet-ns)
- [pkg/netns/netns-scripts/service-ns](/Users/shayne/code/yeet/pkg/netns/netns-scripts/service-ns)
- [pkg/svc/systemd.go](/Users/shayne/code/yeet/pkg/svc/systemd.go)
- [pkg/catch/installer_file.go](/Users/shayne/code/yeet/pkg/catch/installer_file.go)

That path currently installs firewall rules by appending directly into host
chains with bare `iptables` calls. On the inspected live host, `pve1`, that
path delivered line-rate throughput on a `2.5GbE` link, so the design target is
not a new datapath. The design target is cleaner firewall ownership and better
alignment with modern Debian and Ubuntu defaults.

## Requirements

### Functional

1. New hosts should prefer native `nft` as the firewall backend.
2. New hosts should support `iptables-nft` as a compatibility backend.
3. Hosts that only have `iptables-legacy` should remain usable, but yeet should
   make it clear that the host is on a legacy compatibility path.
4. Yeet must own its firewall objects clearly enough that install, update, and
   cleanup are deterministic.
5. The existing netns topology must remain unchanged in this proposal:
   `yeet-ns`, `yeet0`, `yeet0-peer`, `br0`, service namespaces, and the current
   `svc` subnet model stay as-is.
6. The design must support Debian and Ubuntu systems.
7. Acceptance must include upgrading `catch` and using `yeet` to deploy to
   `root@pve1` to confirm the new code works end to end.

### Non-Goals

1. No in-place firewall backend migration for existing hosts.
2. No redesign of the service subnet addressing model.
3. No replacement of shell-based namespace setup with a full Go netlink rewrite.
4. No expansion into host-wide firewall policy management beyond yeet's own
   forwarding and masquerade needs.

## Supported Host Baseline

This design targets modern default installs on:

- Debian 12+
- Debian 13+
- Ubuntu 22.04+
- Ubuntu 24.04+

Expected host capabilities:

- `iproute2`
- `systemd`
- one usable firewall backend:
  - preferred: `nft`
  - compatibility: `iptables` backed by nf_tables
  - fallback: `iptables-legacy`

## Design Summary

Introduce an explicit firewall backend layer for the service-netns path.
Backend selection becomes a deliberate part of yeet host behavior instead of an
accidental side effect of whatever `iptables` alternative happens to be active.

For new hosts:

- use native `nft` when available
- otherwise use `iptables-nft`
- otherwise allow `iptables-legacy` with a warning

Regardless of backend, yeet should stop installing anonymous one-off rules into
top-level host chains and instead manage a small, yeet-owned firewall slice.

## Backend Selection

### Detection Rules

At install or first-use time, yeet should resolve the active backend in this
order:

1. If `nft` is present and usable, select `nft`.
2. Else, if `iptables` is present and reports an nf_tables backend, select
   `iptables-nft`.
3. Else, if `iptables-legacy` is present, select `iptables-legacy`.
4. Else, fail with a clear host capability error.

### Persistence

Yeet should record the chosen backend in generated runtime state so operators
can inspect what backend a host is using. This can live alongside the yeet
network runtime artifacts that are already generated during host/service setup.

### Operator Signaling

- `nft`: log as the expected modern path.
- `iptables-nft`: log as compatibility mode on a modern kernel backend.
- `iptables-legacy`: log as legacy compatibility mode and warn that this is not
  the intended default for new hosts.

## Firewall Ownership Model

### Native `nft`

For the `nft` backend, yeet should own a dedicated table, for example
`table ip yeet`, containing only yeet-managed objects. That table should hold:

- a yeet-owned forwarding chain for traffic relevant to `yeet0`
- a yeet-owned postrouting chain for service-subnet masquerade

Hook attachment can be implemented either through yeet-owned base chains or via
stable jump points from the host hook chains, whichever proves clearer in code
and more robust in testing. The important property is that all yeet rules live
under yeet-owned nft objects.

### `iptables-nft` Fallback

For the compatibility backend, yeet should mirror the same ownership model
using explicit chains, for example:

- `YEET_FORWARD`
- `YEET_POSTROUTING`

Yeet should install only the minimal jump rules needed from `FORWARD` and
`POSTROUTING` into those chains.

### `iptables-legacy` Fallback

Legacy compatibility should use the same explicit-chain ownership model as the
`iptables-nft` fallback. This keeps operational semantics consistent even when
the backend is less modern.

## Rules In Scope

This proposal only covers the rules needed for the current service-netns
datapath:

- allow forwarding from `yeet0`
- allow related and established return traffic back out `yeet0`
- masquerade traffic sourced from the yeet service subnet when it leaves that
  subnet

This proposal does not make yeet the host's primary firewall manager and does
not attempt to manage host-wide input or output policy.

## Implementation Shape

### Control Boundary

Add a small firewall abstraction in Go that is responsible for:

- backend detection
- installing yeet-owned rules
- verifying expected yeet-owned rules exist
- removing yeet-owned rules

The existing namespace topology setup remains where it is today:

- [pkg/netns/netns-scripts/yeet-ns](/Users/shayne/code/yeet/pkg/netns/netns-scripts/yeet-ns)
- [pkg/netns/netns-scripts/service-ns](/Users/shayne/code/yeet/pkg/netns/netns-scripts/service-ns)

The firewall behavior should be moved out of ad hoc bare `iptables` shell
snippets and into a backend-aware control path, even if the shell entrypoints
remain the outer orchestration layer for namespace setup.

### Idempotence

Install, restart, and cleanup must be idempotent:

- repeated apply should not duplicate rules
- cleanup should remove only yeet-owned objects
- validation should be able to repair missing yeet-owned objects safely

## Validation And Failure Behavior

At install or start time, yeet should validate:

- selected firewall backend
- `net.ipv4.ip_forward=1`
- required interfaces exist and are up
- yeet-owned chains or tables exist and contain the expected rules

Failure behavior:

- fail if no usable firewall backend exists
- fail if the host cannot establish required forwarding/NAT state
- warn, but do not fail, when the resolved backend is `iptables-legacy`

## Testing Strategy

### Unit And Integration Coverage

Add automated coverage for:

- backend detection
- generated nft payloads or command invocations
- generated `iptables-nft` chain/rule behavior
- generated `iptables-legacy` chain/rule behavior
- cleanup idempotence

### Live Validation

Acceptance must include end-to-end testing against the real host `pve1`:

1. Upgrade `catch` on `root@pve1` with the new code.
2. Use `yeet` to deploy or update a service that exercises the `svc` netns path.
3. Verify namespace creation, routes, and yeet-owned firewall objects on the
   host.
4. Verify outbound connectivity from the service.
5. Verify service-to-service or host-to-service connectivity on the yeet subnet.
6. Repeat apply or restart to confirm idempotence.
7. Run an `iperf3` sanity check to confirm the design did not materially regress
   throughput.

The design should treat this live validation path as mandatory before claiming
the work complete.

## Documentation Changes

Update the user and operator docs to say:

- new hosts prefer `nft`
- `iptables-nft` is the compatibility path
- `iptables-legacy` is supported only as a compatibility fallback
- how to inspect the active backend
- how to inspect yeet-owned firewall objects

## Rollout Notes

Because this proposal is for new hosts only, rollout can stay simple:

- new installs select the best available backend
- existing hosts are not migrated automatically
- hosts already pinned to `iptables-legacy` continue to work unless the operator
  later chooses to reinitialize or reconfigure them

## Acceptance Criteria

This design is satisfied when:

1. A new Debian or Ubuntu host chooses `nft` by default when available.
2. A host without usable native `nft` but with nf_tables-backed `iptables`
   works via `iptables-nft`.
3. A legacy-only host still works with a warning.
4. Yeet-owned firewall objects are cleanly isolated from non-yeet rules.
5. Repeated install or restart does not duplicate yeet firewall state.
6. `catch` can be upgraded and a real service can be deployed to `pve1` using
   `yeet`, with the resulting service network confirmed to function correctly.
7. Throughput on the tested path remains in line with the host's physical link
   baseline.
