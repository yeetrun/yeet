# Service Network Mutation Through `yeet service set`

## Context

Yeet accepts network settings during `yeet run`, but an existing service cannot
reliably change them. The client currently locks some network flags after the
initial deployment, and project-local change detection can compare edited
`yeet.toml` arguments with those same edited arguments instead of Catch's
installed state. This can produce `No changes detected` while the service still
uses its previous network.

Network mutation belongs with Yeet's other explicit service mutations. The
existing `yeet service set` command already provides the non-VM mutation
surface, remote Catch execution, `manage` permission boundary, and local
`yeet.toml` synchronization. VMs have a separate `yeet vm set` command.

Catch already has regular network installers plus fail-closed ISO allocation,
transition, cleanup, quarantine, and tombstone machinery. This feature extends
those lifecycle concepts to complete in-place network replacement. It does not
model a network change as service removal or as a payload redeployment.

## Goals

- Let an existing non-VM service mutate its complete network configuration
  through `yeet service set` without removing or recreating the service.
- Support transitions among host, `svc`, `lan`, `ts`, and `iso`, including the
  mode combinations already supported for the service's payload kind.
- Add native binary, script, and timer ISO networking without coupling network
  selection to `--run-as`, UID, or root policy.
- Make every transition immediate, transactional, rollback-safe, and
  fail-closed when neither activation nor restoration can be verified.
- Keep Catch's authoritative desired network configuration distinct from the
  runtime objects that implement the effective network.
- Reject network changes attempted through an existing-service `yeet run` and
  direct operators to `yeet service set`, while allowing unrelated
  redeployments.
- Keep CLI help, project configuration, status/info, README, and the user manual
  consistent with the command behavior.

## Non-Goals

- Changing VM networking through `yeet service set`; VMs continue to use
  `yeet vm set`.
- Treating ISO as containment against a malicious host-root process. ISO is a
  network mode, not a general native-process sandbox.
- Requiring, inferring, or automatically changing a native service identity as
  part of a network mutation.
- Adding new payloads, changing payload type, rebuilding payload content, or
  changing a service root during a network-only mutation.
- Inventing mode combinations for payloads whose topology cannot implement
  them. Such limitations must be based on networking capability, never on the
  process identity.
- Deploying, restarting, upgrading, or otherwise mutating any live service or
  Catch host during development or verification.
- Pushing, opening a pull request, releasing, installing, or deploying.

## Command Surface

`yeet service set <service>` gains the complete non-VM network option family:

```text
--net
--ts-tags
--ts-ver
--ts-exit
--ts-auth-key
--macvlan-parent
--macvlan-vlan
--macvlan-mac
```

Examples:

```bash
yeet service set api --net=iso
yeet service set api --net=ts --ts-tags=tag:production
yeet service set api --net=lan --macvlan-parent=eth0
yeet service set api --net=host --ts-exit=
yeet service set api --run-as=app:app --net=iso
```

The last command requests two independent service settings in one transaction.
Network validation does not require or interpret the identity setting.

The operation remains inside the existing service-management boundary and
requires `manage`. It uses the existing `service set` remote-command path; no
new top-level command, RPC method, or permission class is introduced.

Passing these flags to `yeet service set` for a VM returns guidance to use
`yeet vm set`.

## Patch and Clear Semantics

The command is a patch. Omitted fields retain their authoritative stored
values, while explicitly supplied fields change:

| Flag | Mutation |
| --- | --- |
| `--net` | Replaces the complete mode set. `host` is the explicit unmanaged-host mode and cannot combine with another mode. |
| `--ts-tags` | Replaces the complete tag list. Repeated occurrences form the replacement list; `--ts-tags=` clears it. |
| `--ts-ver` | Sets the Tailscale version; an explicit empty value clears it. |
| `--ts-exit` | Sets the exit node; an explicit empty value clears it. |
| `--ts-auth-key` | Supplies a non-empty, write-only enrollment credential for this mutation. It is neither displayed nor saved in project configuration. |
| `--macvlan-parent` | Sets the parent interface; an explicit empty value clears it. |
| `--macvlan-vlan` | Sets the VLAN; an explicit empty value clears it. |
| `--macvlan-mac` | Sets the MAC address; an explicit empty value clears it. |

`--net=` is invalid; callers use `--net=host` to leave all managed networks.
`--ts-auth-key=` is also invalid because the credential has no stored field to
clear. All other optional network settings use an explicit empty value to
clear.
The parser must preserve flag presence separately from parsed values so omitted
and explicitly empty values remain distinguishable at the client and Catch.

Mode-specific settings are desired configuration, not runtime attachment
pointers. They remain stored when their mode is inactive unless explicitly
cleared. This lets a later transition reuse valid settings while ensuring that
stale runtime objects are still removed.

Whenever the resulting mode set includes `ts`, the resulting Tailscale tags
must be non-empty. Entering `ts` may reuse valid stored tags; otherwise the
command must explicitly provide `--ts-tags`. Clearing tags while `ts` remains
selected is rejected before runtime mutation.

Existing validation continues to reject intrinsically incompatible network
topologies, such as `host` combined with another mode, ISO combined with
`svc`/`lan`, or ISO with published ports. Native and timer ISO admission does
not inspect the service user. Native `iso,ts` remains unsupported unless the
native topology independently gains a Tailscale implementation; that is a
networking limitation rather than an identity policy.

## Authoritative Desired and Effective State

Catch persists a service-level desired network configuration containing:

- normalized modes;
- normalized Tailscale tags, version, and exit-node setting; and
- macvlan parent, VLAN, and MAC settings.

The auth key is transient and never enters this record. Existing runtime
records such as service-network allocation, macvlan attachment, Tailscale
runtime state, and ISO allocation remain the effective-state implementation.

For services created before this field exists, Catch derives the initial
desired configuration from persisted runtime state. The migration is lazy and
backward compatible; reading a legacy service must not itself restart it.
Initial deployments populate desired configuration after successful
activation.

The committed desired record changes only after the replacement has activated
and been verified. Transactional staging records such as an ISO reservation
may exist earlier, but their lifecycle state cannot claim readiness. If
activation fails and rollback succeeds, desired configuration remains at the
previous value. If both activation and rollback fail, status shows the service
as stopped or quarantined and reports the failed transition instead of
claiming that either network is effective.

## Client and Configuration Flow

The local client uses the existing `ParseServiceSet` and `handleServiceSet`
path:

1. Parse a presence-aware network patch and validate syntax.
2. Execute the existing remote `service set` command against Catch.
3. Let Catch build, validate, and apply the authoritative mutation.
4. After remote success, rewrite only the explicitly changed network flags in
   the matching `yeet.toml` entry. Never save `--ts-auth-key`.
5. Report the old and new effective configuration.

Network mutation adds no new confirmation flag or prompt. It follows the
existing `service set` interactive and non-interactive behavior.

If the project file has no matching entry or cannot be saved after remote
success, the remote mutation remains authoritative. The client reports the
local synchronization failure and gives the concrete recovery command:

```bash
yeet service sync <service>
```

Local configuration rewriting preserves payload arguments and unrelated run
flags. `--net` replaces only the stored network-mode flag. Each other supplied
network option replaces or clears only its own stored flag occurrences.

## Redeployment Guard

`yeet run` retains all network flags for initial deployment. For an existing
service, it is a payload/configuration redeployment command, not a network
mutation command.

Before an existing-service redeployment, the client obtains Catch's
authoritative desired network configuration and compares it with the resolved
network configuration requested by the run. Catch repeats the guard at the
install boundary so direct or older clients cannot bypass it.

- If the network configuration is unchanged, unrelated redeployment proceeds.
- If any persistent network field differs, the redeployment stops before
  activation and directs the operator to `yeet service set <service> ...`.
- Supplying `--ts-auth-key` during an existing-service run is also redirected
  to `service set`, because the credential is a mutation input with no stable
  value to compare.
- A service-not-found result is an initial deployment and remains allowed.
- Failure to read authoritative network state fails closed instead of assuming
  that no network change exists.

An edited `yeet.toml` therefore cannot produce `No changes detected` when it
disagrees with Catch. It receives the explicit `service set` guidance while
ordinary payload, environment, image, publish, and other redeployments remain
available.

## Catch Mutation Transaction

Catch adds network changes to the existing `serviceSetChanges` orchestration.
Under the existing per-service operation lock it:

1. Loads the current desired configuration, effective runtime records, payload
   kind, artifacts, and generation.
2. Applies the presence-aware patch in memory.
3. Normalizes and validates the complete result before stopping anything.
4. Returns success without restarting when the normalized result is unchanged.
5. Stages a complete replacement without mutating the committed service
   record.
6. Stops the old runtime, activates the staged replacement, and verifies it.
7. Atomically persists the new desired and effective records only after
   verification.
8. Retires old artifacts and reports the old and new configuration.

The network transaction may coexist with another explicitly requested
`service set` mutation, including `--run-as`, but each concern keeps its own
validation. The combined operation must not partially commit one requested
setting while rolling back the other.

### Transition classes

| Current | Desired | Behavior |
| --- | --- | --- |
| regular/host | regular/host | Stage a complete replacement, stop the previous units, activate and verify the replacement, then retire old state. |
| regular/host | ISO | Reserve and verify isolation before workload activation, then retire the previous network. |
| ISO | ISO | Preserve a compatible stable allocation, revalidate topology, and activate the replacement through the ISO lifecycle. |
| ISO | regular/host | Use the existing ISO exit lifecycle: stop the workload, prove isolation cleanup, commit the replacement, then start it. |

A regular replacement removes every stale effective pointer not selected by
the desired modes. Explicit host mode renders a fresh native unit without an
old `NetworkNamespacePath` or managed-network dependency.

If replacement activation fails, Catch restores the previous database record,
artifacts, enablement, and runtime intent. If restoration fails, Catch
best-effort stops every deduplicated current and previous primary, timer,
namespace, and Tailscale unit, aggregates stop errors, and then separately
verifies that none remain active. The returned error contains both the original
activation failure and rollback/fail-closed failures.

ISO entry, exit, and cleanup continue to use allocation phases, tombstones,
quarantine, verified absence, and reconciliation. Cleanup uncertainty never
falls back to a weaker network or starts the replacement.

## Native and Timer ISO Networking

A native binary or script uses the direct ISO `/30` topology already designed
for a single endpoint. Catch owns namespace, veth, routing, DNS, firewall, and
cleanup setup. The generated systemd service joins the prepared namespace with
`NetworkNamespacePath=` and orders itself after the ISO gate.

Network attachment does not change `User=`, `Group=`, capabilities, filesystem
access, or other privilege policy. Root and non-root services follow the same
network path. The feature promises network placement and policy for ordinary
workload traffic; it does not claim to contain a malicious host-root process.

For a timer-backed native workload, the generated `.service` joins the ISO
namespace while the `.timer` remains the scheduling unit. Applying the
mutation restarts/re-enables the timer as appropriate but does not invoke the
job merely to prove the transition. Catch verifies the namespace/gate and the
timer's installed/active intent separately.

Compose services continue using their existing ISO project/router lifecycle.
Payload-specific topology restrictions remain enforced consistently on initial
deployment and mutation.

## Status and Information

Service-info responses expose normalized desired and effective network modes
and applicable non-secret Tailscale/macvlan settings. Plain `yeet info` shows
the effective modes, including explicit `host`. When desired and effective
state diverge during a failed or quarantined transition, both are visible with
the lifecycle/error state.

Tailscale auth keys are never returned, logged, rendered in command previews,
or written to `yeet.toml`. Existing redaction helpers cover command errors and
draft/debug surfaces.

## Documentation

CLI help, README examples, and the website user manual describe:

- `yeet run` network flags as initial-deployment configuration;
- `yeet service set` as the in-place non-VM mutation command;
- the complete patch and clear semantics;
- the required Tailscale tag invariant;
- `yeet vm set` as the separate VM command; and
- ISO as network isolation, without a hostile-root containment claim.

Public material contains no private service, hostname, address, username, or
deployment details.

## Test-Driven Verification

Implementation starts with failing focused tests for each behavior, followed
by the smallest code change that makes them pass. Coverage includes:

- table-driven parsing tests for every network flag, repeated tags, omitted
  versus explicitly empty values, aliases, help, and VM rejection;
- parser/normalizer fuzz coverage for modes, tags, macvlan inputs, and service
  set patch application;
- local command routing, existing `manage` permission mapping, remote argument
  preservation, and config rewriting without auth-key persistence;
- desired-state migration and JSON round trips for legacy and current service
  records;
- Tailscale tag retention, replacement, clearing, and required-tag failures;
- redeployment guards proving changed network settings fail with `service set`
  guidance while unchanged-network and unrelated redeployments proceed;
- Catch transition matrices across host, `svc`, `lan`, `ts`, ISO, and valid
  combinations for native, timer, and Compose services;
- immediate restart, stable compatible allocations, stale pointer removal, and
  explicit-host systemd regeneration;
- activation failure, successful rollback, rollback failure, exhaustive final
  stopping, ISO tombstone/quarantine, and startup reconciliation;
- native and timer ISO rendering without identity checks or implicit privilege
  changes;
- privileged Linux integration proving ordinary native traffic follows ISO
  public-egress and private/service/other-ISO denial policy, without claiming
  hostile-root containment;
- service-info/plain-info desired/effective display and credential redaction;
  and
- documentation/help examples that remain synchronized.

Focused package, race, fuzz, and integration tests run while iterating. On the
stable candidate, run the complete Go suite and repository pre-commit hooks
once immediately before the local GitButler commit. No test may target or
mutate a live Yeet service.

## Alternatives Considered

### Mutate networking through `yeet run`

This overloads deployment with an operational mutation, caused the original
desired/effective detection bug, and conflicts with the established `service
set`/`vm set` command family. It is rejected.

### Synthesize an internal redeployment from `service set`

This would reuse install parsing but couples a network-only operation to
payload generations and may accidentally change payload or deployment state.
The dedicated transaction reuses lower-level installers and lifecycle helpers
without pretending to redeploy content.

### Add a new network RPC

The existing remote `service set` path already supplies command parsing,
authorization, locking, and project synchronization. A new RPC would add a
protocol and permission surface without a user-facing benefit.

### Require non-root execution for native ISO

Non-root execution is useful for hostile-workload containment, but it is a
separate service-identity policy. Requiring it here would conflate privilege
management with network mutation and prevent otherwise valid trusted native
services from selecting ISO. This design deliberately keeps the concerns
independent.
