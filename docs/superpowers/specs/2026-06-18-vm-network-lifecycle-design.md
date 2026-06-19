# VM Network Lifecycle Design

## Goal

Make VM networking correct by construction instead of relying on best-effort
cleanup after failures. VM network runtime state should be fully derived from
durable service intent, repaired before VM start, and cleaned up when it is no
longer owned by any VM.

This spec focuses on Firecracker VM networking. Docker compose network
namespaces and NAT have a separate reconciler and should be audited separately
after the VM lifecycle is fixed.

## Invariants

1. **Intent is durable.** The catch DB is the source of truth for intended VM
   networking. Host links, routes, netns peers, bridges, and tap devices are
   runtime state.
2. **Runtime state is fully derivable.** Given a VM service record, yeet can
   compute every VM-owned network device and route needed for Firecracker.
3. **Ownership is explicit.** `yvm-*` is reserved for yeet VM runtime devices.
   Yeet may mutate only names that match its full VM naming grammar and must
   never mutate external parent devices such as physical NICs, user bridges,
   Docker bridges, or arbitrary non-`yvm` links.
4. **Transitions are transactional at the yeet level.** VM create and VM network
   mutation must end with DB intent and host runtime state agreeing, or must
   restore the last committed DB-owned runtime state.
5. **Start is self-healing.** Starting a VM must synchronously ensure its
   DB-owned network state exists before Firecracker starts. Host reboot must not
   depend on an async cleanup race.
6. **Cleanup is conservative but complete.** Yeet deletes unowned VM runtime
   state only when it is provably in the reserved VM namespace, including
   service-network links and LAN taps.
7. **Diagnostics are first-class.** The desired/live diff used for repair must
   also support a check mode that reports missing, stale, conflicting, and
   miswired state without mutating the host.

## Scope

In scope:

- Make `yeet run <name> vm://...` create-only for VMs.
- Move VM changes to `yeet vm set` and other VM-specific commands.
- Replace VM network side-effect cleanup with a desired/live reconciler.
- Repair VM startup and reboot behavior by ensuring network state before
  Firecracker starts.
- Clean unowned `yvm-*` VM devices across service networking and LAN networking.
- Add tests and live smoke coverage for create, mutation, remove, catch restart,
  host-style reboot simulation, and VM-to-service reachability.

Out of scope:

- General Docker compose netns/NAT redesign.
- User-facing service generation behavior.
- Storage snapshot lifecycle changes.
- Automatic VM image or disk replacement through `yeet run`.

## User-Facing Semantics

### VM creation

`yeet run <name> vm://...` creates a new VM. If `<name>` already exists as a VM,
the command fails with an actionable error that directs users to `yeet vm set`
for supported mutable settings or to remove/recreate for immutable settings.

If `<name>` exists as a non-VM service, the existing type-conflict behavior is
preserved.

### VM mutation

`yeet vm set <name>` is the mutation path for supported mutable VM settings:

- CPU count
- memory
- disk grow
- network mode
- LAN parent, VLAN, and MAC settings
- future mutable VM settings

Network, disk, and shape changes continue to require the VM to be stopped.

### Remove

Removing a VM stops it and removes all DB-owned runtime network state before the
DB record is removed. Cleanup warnings remain visible and actionable, and the
startup reconciler can finish safe cleanup later if the first attempt is
partially blocked.

## Architecture

Add a VM network lifecycle layer that models desired and live state separately.

### Desired State

`VMNetworkDesiredState` is built from DB VM records. It contains the service
name, interface index, mode, tap name, bridge name, generated VLAN device, svc
IP route, LAN parent, and netns peer names.

Desired state must cover all VM-owned link kinds:

- `b`: generated bridge
- `s`: Firecracker tap for service networking
- `v`: root namespace veth peer
- `n`: yeet service namespace veth peer
- `l`: Firecracker tap for LAN networking

### Live State

`VMNetworkLiveState` is collected from host runtime state:

- root namespace links
- root namespace routes relevant to VM service IPs
- yeet service namespace links
- link master/bridge membership for VM-owned links

The collector should be testable through injectable command runners and parsers.

### Diff And Plan

`VMNetworkReconciler` compares desired and live state and produces a structured
plan. Plan actions include:

- `Ensure`: create or repair DB-owned runtime state.
- `Cleanup`: remove unowned reserved VM runtime state.
- `Transition`: move from old desired state to new desired state for `vm set`.
- `Check`: report drift without mutation.

Plans are executable through the existing `vmNetworkCommandRunner` style so tests
can assert exact commands without invoking host networking.

## Lifecycle Integration

### Provision

New VM provision writes durable DB intent only after disk, metadata, config,
systemd staging, and network ensure succeed. If a failure happens after network
runtime state is created but before DB commit, provision cleans the newly-created
reserved VM runtime state.

Because VM names are create-only, provision does not have to preserve or update
an existing VM network.

### VM Set

`yeet vm set` builds an old desired state from current DB and a new desired state
from requested flags. It applies a transition plan:

1. Validate the VM is stopped.
2. Build new metadata and Firecracker config.
3. Ensure new VM network runtime state.
4. Verify the new runtime state is usable.
5. Write metadata/config.
6. Commit DB changes.
7. Remove old runtime state that is no longer owned.

If steps 3-6 fail, the command removes newly-created runtime state and restores
the old DB-owned state before returning the error.

### Catch Startup

Catch startup runs VM network reconciliation after the shared service namespace
is available. It ensures all DB-owned VM runtime networking exists and removes
unowned reserved VM runtime state.

Startup reconciliation must be idempotent and safe to run repeatedly.

### VM Systemd Start

Each VM systemd unit gets a synchronous pre-start ensure step before Firecracker
launches. This prevents a reboot race where VM units start before async catch
startup reconciliation recreates tap devices.

The pre-start ensure should repair only the named VM's DB-owned network state.
It must fail clearly if desired state cannot be reconciled.

### Remove

Remove uses desired state from DB to delete all owned VM runtime state. If a
cleanup command fails because a link is already gone, the failure is ignored. If
cleanup fails for another reason, remove records a warning and proceeds through
the existing removal flow so later reconciliation can safely finish cleanup.

## Conservative Ownership Rules

Cleanup is allowed only for names that match the generated VM naming grammar:

```text
yvm-<short-vm-name>-<kind><index>
```

`<kind>` must be one of `b`, `s`, `v`, `n`, or `l`; `<index>` must be decimal.

Cleanup must not delete:

- external bridge parents
- physical interfaces
- Docker bridges
- non-`yvm` veths or taps
- `yvm-*` names that are still owned by any DB VM desired state

If live state is ambiguous, the reconciler reports a conflict instead of
guessing.

## Diagnostics

Expose a check path that uses the same desired/live diff as reconciliation.
This can start as an internal helper tested in Go. A later CLI can surface it as
`yeet vm network check` or part of `yeet diagnostics`.

The report should distinguish:

- missing DB-owned links
- missing DB-owned routes
- stale unowned reserved links
- stale unowned reserved routes
- unexpected bridge membership
- missing yeet service namespace peer
- conflicts that require manual review

This diagnostic path should also support the current VM-to-service reachability
debugging: DNS can resolve correctly while service-network traffic still fails,
so tests and checks should verify both name resolution and TCP reachability over
the service network.

## Testing Strategy

Unit tests:

- `yeet run` rejects an existing VM service before provisioning artifacts.
- provision cleans newly-created network state when a post-network step fails.
- `vm set` rolls back to old runtime state when metadata/config/DB commit fails.
- startup reconciliation ensures DB-owned svc links, LAN taps, generated VLAN
  links, and VM service routes.
- startup reconciliation removes unowned `b`, `s`, `v`, `n`, and `l` links.
- cleanup refuses to plan deletes for non-VM-owned names.
- systemd rendering includes the VM network ensure pre-start step.

Integration and live smoke tests:

- create a disposable VM with `svc` networking and confirm service DNS plus TCP
  reachability.
- create a disposable VM with `svc,lan` networking and confirm both interfaces
  remain usable.
- stop a VM, delete its runtime links manually, start it, and confirm pre-start
  ensure recreates the links before Firecracker starts.
- plant fake unowned `yvm-*` service and LAN links, restart catch, and confirm
  reconciliation removes them without touching DB-owned links.
- change a stopped VM's network settings through `yeet vm set`, then verify old
  links are gone and new links match DB.
- remove a disposable VM and verify no `yvm-*` links for that VM remain.

## Follow-Up Audit

After the VM network lifecycle is corrected, run a separate audit of non-VM
service networking:

- Docker compose service namespace veth lifecycle
- NAT and DNS reconciliation
- remove/redeploy behavior
- catch restart and host reboot behavior

That audit should become a separate spec only if it finds real issues. The VM
fix should not be blocked on redesigning non-VM networking.
