# Svc-Scoped DNS Design

## Purpose

Yeet should provide simple private service discovery for workloads that opt
into the yeet service network. A workload that includes `svc` should resolve
other `svc` workloads by short name and by `*.yeet.internal`. Workloads that
do not include `svc` should keep their normal LAN, Tailscale, or Docker
resolver behavior.

This corrects the failed assumption in the Docker DNS defaults work: Docker
daemon-wide DNS is too broad because it affects LAN-only, Tailscale-only,
non-yeet, and stale containers that may not be able to route to
`192.168.100.1`.

## Scope

In scope:

- Keep `yeet-dns.service` as the catch-owned DNS server on
  `192.168.100.1:53`.
- Return records only for services and VMs with a stored `SvcNetwork` IP.
- Configure yeet DNS only for workloads whose requested network modes include
  `svc`.
- Preserve ordinary DNS behavior for `lan`, `ts`, `host`, and un-networked
  Docker workloads that do not include `svc`.
- Update user documentation so the story is explicit: service discovery means
  using `svc`.

Out of scope:

- No migration or repair code for hosts that previously received daemon-wide
  Docker DNS defaults. The two production hosts, `root@lab-host` and `root@cloud-host`,
  were fixed by hand.
- No LAN-wide exposure of `*.yeet.internal`.
- No resolver behavior for LAN IPs, Tailscale IPs, or Docker bridge IPs.
- No configurable service subnet in this pass.

## DNS Semantics

`yeet-dns` answers A records for:

- `<service>`
- `<service>.yeet.internal`

The answer is the service or VM's `SvcNetwork` IPv4 address. If a service does
not have a `SvcNetwork` address, it has no yeet DNS record, even if it has a
LAN address, a Tailscale address, or Docker bridge networking.

External names continue to forward through the catch host resolver. Tailnet
names keep the existing Tailscale DNS forwarding behavior. Those forwarding
paths are available only to clients that are intentionally using yeet DNS.

## Docker Daemon Configuration

`catch init` must not configure Docker daemon-level `dns` or `dns-search`.
The install-time Docker helper should only ensure
`features.containerd-snapshotter=true`.

This keeps Docker's default resolver behavior intact for workloads outside the
yeet service network.

## Docker Compose Workloads

Docker containers do not automatically use `/etc/netns/<name>/resolv.conf`.
Docker generates container `/etc/resolv.conf` itself, usually pointing at the
embedded resolver `127.0.0.11`. Because of that, compose workloads that include
`svc` still need a generated compose overlay.

When requested network modes include `svc`, catch should generate
`compose.network` with:

- the existing `networks.default.driver: yeet` and netns driver option
- per-service `dns: [192.168.100.1]`
- per-service `dns_search: [yeet.internal]`

The DNS stanza should be added for every service in the compose payload that
does not already define its own `dns` or `dns_search`. User-defined resolver
configuration wins for that service. If no compose services can be identified,
the install should fail with a clear error rather than silently producing an
incomplete DNS overlay.

When requested network modes do not include `svc`, catch should generate the
netns overlay without yeet DNS stanzas.

## Service Netns Resolver

For non-compose service workloads that include `svc`, catch should write the
netns `resolv.conf` with:

```text
nameserver 192.168.100.1
search yeet.internal
```

For workloads without `svc`, catch should not force yeet DNS into the netns.
LAN-only namespaces should use DHCP-provided resolver behavior. Tailscale-only
tap mode should keep using Tailscale DNS.

For `svc,lan`, the service namespace must keep a route to
`192.168.100.1` and to the service subnet over the `svc` interface, even if
LAN DHCP installs or replaces the default route. Yeet DNS records intentionally
return service-network IPs, so those IPs must remain reachable from mixed
`svc,lan` namespaces.

## VMs

VMs with `svc` should continue receiving yeet DNS on their `svc` interface.
VMs with `svc,lan` should keep yeet DNS scoped to the service-network link so
LAN DNS can remain authoritative for ordinary LAN behavior. LAN-only VMs do
not receive yeet DNS by default.

## Error Handling

The implementation should reject ambiguous DNS delivery:

- If compose DNS injection is requested but the compose services cannot be
  parsed, fail the deploy with a clear message.
- If a compose service already defines `dns` or `dns_search`, leave that
  service untouched and include an info or warning message that it owns DNS.
- If a workload asks for yeet DNS behavior without `svc`, report that yeet DNS
  is available through `--net=svc` or `--net=svc,lan`.

## Documentation

Docs should describe the user model, not the implementation details first:

- Use `svc` when a workload should discover other yeet services.
- Use `svc,lan` when a workload needs both yeet service discovery and LAN
  presence.
- Use `lan` alone when the workload should behave like a normal LAN workload;
  it will not get yeet-local service discovery.
- Yeet DNS records resolve to service-network IPs only.

Container host requirements should no longer claim that `yeet init` configures
Docker daemon DNS defaults.

## Testing

Focused tests should cover:

- Docker install config no longer writes daemon `dns` or `dns-search`.
- Compose overlay generation adds DNS stanzas for all compose services only
  when `svc` is enabled.
- Compose services with explicit `dns` or `dns_search` are preserved.
- LAN-only compose overlays do not include yeet DNS.
- Netns resolver config points at yeet DNS only when `svc` is present.
- `svc,lan` namespace setup preserves a route to the service subnet after LAN
  DHCP.
- DNS lookup only returns `SvcNetwork` IPs.

Live smoke tests should cover:

- A `svc` compose service can resolve another `svc` service by short name and
  by `*.yeet.internal`.
- A `svc,lan` workload can resolve yeet names and still resolve public names.
- A `lan`-only compose service resolves public DNS through normal Docker or LAN
  behavior and does not depend on `192.168.100.1`.
- Existing services on `root@lab-host` and `root@cloud-host` remain healthy after Docker
  daemon DNS is absent.
