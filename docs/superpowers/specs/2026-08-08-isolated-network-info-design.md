# Isolated Network Service Information Design

## Goal

Make `yeet info <service>` describe an isolated network as one coherent network
configuration. Healthy native output should show the effective mode, endpoint,
namespace, egress, and DNS without repeating internal lifecycle terminology:

```text
Network
  Network modes:  iso
  IP:             172.16.0.6
  Namespace:      yeet-228b5863ba-ns
  Egress:         public IPv4 via NAT
  DNS:            public-only
```

The hashed namespace name remains the authoritative runtime identity. It keeps
Linux interface names within `IFNAMSIZ`, ties the namespace and veth identities
together, and avoids colliding with regular service namespaces during a
transition or rollback.

## Compatibility boundary

The persisted database model and the existing `iso` JSON object remain intact.
Internal `ISO*` Go identifiers also remain intact. These are compatibility and
implementation boundaries, not user-facing language.

`catchrpc.ServiceISO` gains an optional `namespace` field. Old clients ignore
it, and new clients tolerate old Catch responses where it is absent. Existing
`modes`, `state`, allocation, and component fields keep their wire names.

## Catch projection

Catch projects the authoritative allocation into the RPC response:

- native binary and timer allocations expose `PeerIP` as the single `service`
  endpoint;
- VM allocations expose `PeerIP` as the single `vm` endpoint;
- Compose allocations continue to expose their stable, sorted component
  endpoints;
- allocations with a named namespace expose it through `namespace`;
- no host-side interface name, host IP, auth key, or other secret is added to
  the public response.

The endpoint projection is based on allocation kind instead of whether the
component map happens to be populated. This closes the native omission without
changing Compose or VM addressing.

## Plain rendering

The top-level `Network modes` row is the only mode row. The nested allocation
must not produce another modes row.

Endpoint labels are concise:

- one `service` or `vm` endpoint is rendered as `IP`;
- named Compose endpoints are rendered as `IP (<component>)` in stable order;
- the namespace, when present, is rendered as `Namespace`.

A healthy `ready` lifecycle is implicit and hidden. Any other non-empty state
is rendered as `Network state`, and an allocation error is rendered as
`Network error`. Egress and DNS retain their current meaning.

VM rendering uses the same isolation-detail renderer so its peer address is
not lost. Non-isolated VM output remains unchanged.

## Terminology

Public prose uses `iso` only for the literal network mode token. Descriptions,
labels, help, and errors use “isolated network” or “isolation”; they do not
expand the internal acronym into public `ISO` terminology.

The audit covers the directly related service-info output, Catch host-info
summary, CLI help, host network-pool messages, README, and current manual
guidance. Internal type names, database field names, generated unit identities,
and compatible JSON keys are not renamed.

## Safety and verification

The response exposes only already-persisted addressing and namespace identity.
Tests cover native, timer, VM, and multi-component Compose projections; old and
new JSON round trips; healthy and abnormal plain rendering; redaction; and the
absence of duplicate or uppercase public isolation labels.

Live checks are read-only. This work does not restart, redeploy, or otherwise
mutate any service.
