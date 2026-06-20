# Service Network Public Egress Design

## Context

The yeet service network is documented as a private service network. In
practice, the host firewall currently masquerades all traffic from
`192.168.100.0/24` to any destination outside that subnet. On a catch host with
Tailscale enabled, this means a service-network-only path can reach tailnet
addresses through the catch host's `tailscale0` interface and can potentially
reach other host-routed private networks.

The observed example was a VM with `--net=svc,lan`. It had no Tailscale
interface, but a curl to a MagicDNS HTTPS service worked because:

- the MagicDNS name resolved to a `100.64.0.0/10` tailnet address.
- The guest routed that address through its service-network gateway,
  `192.168.100.1`.
- The catch host routed the packet to `tailscale0`.
- yeet's broad service-network masquerade made the flow appear to come from the
  catch host's Tailscale IP.

That behavior is too permissive for the `svc` network model. A workload should
only inherit LAN or tailnet access when it explicitly joins `lan` or `ts`.

## Goals

- Define `svc` as private service networking plus public IPv4 internet egress.
- Preserve service-to-service reachability inside `192.168.100.0/24`.
- Preserve public IPv4 internet egress through host NAT.
- Prevent `svc` traffic from reaching private, shared, local, reserved, or
  otherwise non-public IPv4 destinations through the catch host.
- Keep `lan` semantics unchanged: a workload on `lan` is a LAN host and follows
  LAN/router/firewall policy.
- Keep `ts` semantics unchanged: a workload on `ts` has its own Tailscale
  identity and follows tailnet ACLs.
- Support both nft and iptables firewall backends.

## Non-Goals

- Do not change the service-network subnet or topology.
- Do not add per-service allowlists or a new egress policy language.
- Do not change LAN or Tailscale service identity behavior.
- Do not dynamically fetch IANA registries at runtime.
- Do not implement IPv6 policy. This design is intentionally IPv4-only. Leave
  comments near the policy list explaining where future IPv6 work should extend
  the same model.

## Policy Model

The service-network invariant is:

```text
svc can reach:
  - the yeet service subnet
  - globally reachable IPv4 destinations through NAT

svc cannot reach:
  - non-public IPv4 destinations through host-routed egress
```

This should be described as positive service-network semantics, even though the
firewall implementation necessarily uses explicit non-public destination
guards.

The non-public destination set should be a small reviewed list derived from
IANA/RFC special-purpose IPv4 ranges. The initial list is:

- `0.0.0.0/8`
- `10.0.0.0/8`
- `100.64.0.0/10`
- `127.0.0.0/8`
- `169.254.0.0/16`
- `172.16.0.0/12`
- `192.0.0.0/24`
- `192.0.2.0/24`
- `192.168.0.0/16`
- `198.18.0.0/15`
- `198.51.100.0/24`
- `203.0.113.0/24`
- `224.0.0.0/4`
- `240.0.0.0/4`
- `255.255.255.255/32`

The list must include the motivating cases:

- RFC1918 private-use networks:
  - `10.0.0.0/8`
  - `172.16.0.0/12`
  - `192.168.0.0/16`, while still allowing yeet's own
    `192.168.100.0/24` service subnet
- RFC6598 shared address space / Tailscale CGNAT:
  - `100.64.0.0/10`

The service subnet exception must be handled by rule order: allow
`192.168.100.0/24` service-subnet traffic before rejecting the broader
`192.168.0.0/16` private range.

## Implementation Shape

Keep the existing service-network topology:

- `192.168.100.0/24` service subnet
- `yeet0` on the host at `192.168.100.1`
- shared `yeet-ns` namespace and `br0`
- current firewall installer path in `pkg/netns/firewall.go`

Change the generated firewall policy in `pkg/netns/firewall.go`.

For nft:

- keep `table ip yeet`
- accept service-net traffic to `192.168.100.0/24`
- reject service-net traffic to non-public IPv4 destinations
- accept remaining service-net traffic as public IPv4 egress
- keep masquerade for service-net traffic that is allowed to leave the service
  subnet

For iptables backends:

- keep `YEET_FORWARD` and `YEET_POSTROUTING`
- add the equivalent ordered service-subnet accept, non-public reject, and
  remaining service-net accept rules in `YEET_FORWARD`
- keep the masquerade rule in `YEET_POSTROUTING`

The non-public IPv4 destination list should live in one Go helper or data
structure. Rendering for nft and iptables should iterate over that single list
so the backends cannot drift.

Use `REJECT` rather than silent drop for blocked forward traffic so failures
are quick and understandable during debugging.

## Expected Behavior

For a workload with only `svc`:

- can reach `192.168.100.0/24` service peers
- can resolve yeet service DNS through the service-network resolver
- can reach public IPv4 internet destinations
- cannot reach RFC1918 LANs through host routing
- cannot reach Tailscale CGNAT addresses through the catch host

For a workload with `lan`:

- LAN behavior is unchanged
- the workload may reach whatever the LAN allows through its LAN interface

For a workload with `ts`:

- tailnet behavior is unchanged
- the workload reaches tailnet destinations as its own Tailscale identity

For a workload with `svc,lan`:

- service-network egress is still constrained
- LAN egress remains governed by LAN policy

## Tests

Automated tests should cover:

- nft rendering includes a guard for every non-public IPv4 destination.
- iptables-nft and iptables-legacy rendering include the same guard set.
- masquerade remains present for service subnet traffic to destinations outside
  the service subnet.
- verification checks look for representative non-public destination guards as
  well as masquerade.
- the non-public IPv4 list includes RFC1918 and `100.64.0.0/10`.
- comments or tests explicitly state IPv6 is intentionally out of scope.

## Live Verification

After implementation, verify on a live catch host:

- `catch netns-firewall ensure` installs the updated rules.
- From a service-network VM, curl to a MagicDNS name that resolves to a
  `100.64.0.0/10` tailnet address fails.
- From a service-network VM, public IPv4 internet egress still works.
- From a service-network VM, service subnet access to another
  `192.168.100.x` service still works.
- A workload explicitly on `lan` keeps normal LAN behavior.
- A workload explicitly on `ts` reaches tailnet resources through its own
  Tailscale identity rather than the catch host's identity.

## Documentation

Update networking documentation to clarify:

- `svc` means private service network plus public IPv4 internet egress.
- `svc` does not inherit host LAN or host tailnet reachability.
- Use `lan` when the workload should be a LAN host.
- Use `ts` when the workload should have a Tailscale identity.
- IPv6 service-network egress policy is not implemented yet.
