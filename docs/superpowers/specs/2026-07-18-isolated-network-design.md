# Isolated Network Design

## Context

Yeet currently offers three managed network modes:

- `svc` provides private Yeet service connectivity, Yeet DNS, and public IPv4
  egress through Catch.
- `lan` places a workload on the host LAN or VLAN.
- `ts` gives a non-VM service its own Tailscale identity.

None of those modes represents an untrusted workload that should have public
internet egress without being able to initiate connections to Catch, the LAN,
the service network, the tailnet, or another workload. Reusing `svc` would
retain a shared layer-2 segment and service discovery. Reusing `lan` would
deliberately grant the access this feature is intended to remove.

Add a new `iso` network mode. It creates an asymmetric boundary:

```text
Catch can initiate to an ISO workload.
An ISO workload cannot initiate to Catch or another private destination.
An ISO workload can initiate to globally reachable public IPv4 destinations.
```

This is a network-isolation feature, not a general-purpose sandbox or an
application-layer egress filter.

## Goals

- Give every ISO VM and container component a stable RFC1918 IPv4 address.
- Keep isolated workloads off shared layer-2 segments with unrelated
  workloads.
- Allow Catch-originated connections to every ISO endpoint on any protocol or
  port.
- Allow workload-originated connections to globally reachable public IPv4
  destinations through Catch NAT.
- Prevent workload-originated connections to Catch, other ISO projects,
  `svc`, LAN/RFC1918, CGNAT/Tailscale, link-local, metadata, loopback,
  multicast, documentation, benchmarking, and reserved address space.
- Give ISO-only workloads a public-only DNS path that does not expose Yeet
  service discovery.
- Preserve explicit Tailscale identity and MagicDNS behavior for non-VM
  services using `iso,ts`.
- Keep addresses stable across workload, Docker, and Catch restarts and across
  ordinary redeployments.
- Fail closed when admission, firewall, routing, DNS, attachment, or
  reconciliation state cannot be verified.
- Support nftables, iptables-nft, and iptables-legacy hosts.
- Make the behavior inspectable through existing `info` and `ip` surfaces.

## Non-Goals

- IPv6 on ISO attachments.
- Public or LAN ingress to an ISO workload.
- A general egress allowlist, domain policy, or application-layer proxy.
- Preventing DNS-over-HTTPS or another protocol tunneled through otherwise
  permitted public traffic.
- Treating an operator-supplied Compose file as untrusted input.
- Protecting against a kernel, hypervisor, Firecracker, Docker, or container
  runtime escape.
- Native binary, script, or cron support while those workloads run as host
  root.
- Compose replicas, runtime scaling, or more than 29 active components in one
  ISO project in the first version.
- Yeet-managed `ts` mode for VMs. A guest may install Tailscale itself.
- Automatically renumbering existing workloads after a pool conflict.

## Threat Model

The trusted control plane consists of:

- the Yeet operator;
- the deployment definition supplied by that operator;
- Catch and its persisted database;
- Docker and the host container runtime;
- Firecracker and the host kernel;
- the Catch host administrator.

The untrusted data plane consists of:

- VM guest code;
- container images;
- Python and TypeScript application code executed in generated containers;
- application processes running inside an admitted Compose project.

A Compose file is trusted deployment code, not an untrusted workload artifact.
Rootful Docker can use a Compose definition to request host networking,
privileged execution, devices, host namespace sharing, bind mounts, daemon
sockets, and host-side providers before an application starts. A network
namespace around the resulting application cannot contain a malicious
deployment definition that has already instructed the host daemon to bypass
that namespace.

ISO therefore admits only a reviewed safe subset of the resolved Compose
application model. The image and application remain untrusted after the
trusted definition has passed admission.

Native binaries, scripts, and cron jobs currently run as root in host-managed
systemd units. Host root can reconfigure or leave a network namespace, so ISO
must reject those payloads rather than imply a security boundary that does not
exist. Add focused comments at the native payload validation and systemd unit
boundaries explaining that non-root service sandboxing is the prerequisite for
a later follow-up.

## User-Facing Network Contract

Supported forms are:

```bash
yeet run sandbox nginx:alpine --net=iso
yeet run stack ./compose.yml --net=iso
yeet run tailapp ./compose.yml --net=iso,ts
yeet run devbox vm://ubuntu/26.04 --net=iso
```

The compatibility matrix is:

| Payload | `iso` | `iso,ts` | Notes |
| --- | --- | --- | --- |
| VM | yes | no | Guest may install Tailscale itself |
| Remote or locally pushed image | yes | yes | Generated single-service Compose |
| Client-built Dockerfile image | yes | yes | ISO applies to runtime, not the client-side build |
| Python or TypeScript | yes | yes | Generated container-backed service |
| Admitted Compose project | yes | yes | Trusted definition, untrusted containers |
| Native binary or script | no | no | Requires future non-root systemd sandbox |
| Cron/timer | no | no | Requires future non-root systemd sandbox |

`iso` cannot combine with `svc` or `lan`. `iso,svc`, `iso,lan`, and any larger
combination containing them fail during local validation and again at Catch.
VMs support `iso` only as a Yeet-managed mode.

`-p`/`--publish`, `--publish-reset`, and Compose `ports` are rejected for ISO
deployments. ISO endpoint addresses are routed only on Catch, so public port
publication is both unnecessary for host administration and contrary to the
no-public-ingress contract.

Errors identify the exact incompatibility. Compose admission errors include a
field path such as `services.api.network_mode` instead of a generic
"unsupported Compose file" message.

## Address Pool Selection

ISO uses one persisted RFC1918 `/16`. The preferred candidate is
`172.30.0.0/16`. Catch then tries a curated sequence of less commonly used
`172.16.0.0/12` `/16` ranges. It avoids `172.17.0.0/16`, commonly used by
Docker, and `172.31.0.0/16`, commonly used by default AWS VPCs, until no better
candidate remains.

Before selecting a candidate, Catch checks:

- live host routes and addresses;
- named network namespaces and their addresses;
- Docker network/IPAM allocations;
- persisted Yeet network state;
- the proposed aggregate blackhole route;
- existing ISO allocations and cleanup tombstones.

Catch persists the chosen `/16` before making the first workload allocation.
It installs a blackhole route for the aggregate. More-specific routes for live
ISO links and component subnets take precedence, while unused or stale ISO
addresses cannot fall through to a LAN or default route.

The operator may set the pool explicitly before any active, reserved, or
tombstoned allocation exists:

```bash
yeet host set --iso-pool=172.30.0.0/16
```

The host-setting plan validates that the value is an IPv4 `/16` contained by
RFC1918 space and does not overlap live or persisted state. After allocations
exist, a pool change is rejected and reports the workloads or tombstones that
block it. There is no automatic renumbering path.

## Pool Layout and Capacity

The selected `/16` is divided into two fixed halves:

- The lower `/17` supplies point-to-point `/30` links. It supports 8,192
  workload links.
- The upper `/17` supplies container-project `/27` prefixes. It supports 1,024
  container projects with 30 usable addresses per prefix.

Every container project reserves one address as its internal gateway, leaving
29 stable component addresses. ISO v1 rejects a project with more than 29
active components. It also rejects Compose replicas or scaling because one
stable component name must identify one stable address.

The fixed partition deliberately trades some theoretical utilization for
simple collision proofs, stable address capacity, deterministic status, and
straightforward recovery. A future version may add a versioned allocator for
larger projects, but it must not silently resize or renumber an existing
project.

## Persisted State

Host state gains one conceptual `IsoPool` record containing:

- selected `/16`;
- automatic or explicit selection source;
- allocator/policy version;
- aggregate route state;
- allocation counters and conflicts.

Each ISO workload gains one conceptual `IsoAllocation` containing:

- stable outer `/30`;
- Catch and workload-side addresses;
- stable interface identities;
- expected source prefixes for anti-spoofing;
- optional container-project `/27` and gateway;
- component-name-to-address mappings;
- desired network modes;
- lifecycle/reconciliation state;
- cleanup tombstone state and last error.

The names are conceptual. The implementation may fit them into existing DB
views and records, but the state must remain explicit, versioned, and available
without inspecting live interfaces.

Ordinary stop, start, update, Docker recreation, and Catch restart preserve the
allocation. Removal releases addresses only after live cleanup is verified.
Cloning always creates a fresh allocation.

## VM Topology

An ISO VM receives one dedicated `/30`:

```text
Catch host address  <---- TAP / tiny per-VM bridge ---->  guest address
       .1                                                .2
```

The host owns link creation, the host address, the route, source validation,
and firewall policy. The guest receives static IPv4 configuration through the
existing VM provisioning/metadata path and uses the host address as its
default gateway and DNS server.

The VM is not attached to the shared `svc` bridge or a LAN bridge. Its TAP is
not bridged to any unrelated VM or service. The small per-VM bridge remains an
adapter detail if the current Firecracker setup requires it; it must not create
a shared broadcast domain.

Catch-originated traffic can reach the guest on every protocol and port. The
guest can return traffic for those connections but cannot initiate a new flow
to Catch.

`yeet ssh <iso-vm>` automatically selects the existing Catch proxy path because
the workstation normally has no route to the RFC1918 ISO address. Guest SSH
authorization still comes from the guest key.

## Container Project Topology

A container-backed Yeet service is one isolation unit. It receives:

- one outer `/30` between the Catch root namespace and a service-owned router
  namespace;
- one unique `/27` for the project's Docker default network;
- one stable address per resolved Compose service.

Conceptually:

```text
Catch root namespace
  |
  | stable /30
  |
service router namespace
  |
  | unique routed /27
  |
  +-- app
  +-- database
  +-- worker
  +-- sidecars
```

Catch installs a route for the project `/27` through the service side of the
outer `/30`. The service router forwards between the outer link and the
project bridge under ISO policy. This makes every component directly reachable
from Catch without published ports or a fabricated "primary container".

The generated Compose overlay defines the Yeet driver as the only network,
assigns the persisted project subnet/gateway, and assigns each component its
persisted address. Components within the same admitted Compose project may
communicate because they are parts of one isolation unit. Components in other
Yeet projects cannot communicate.

Generated image, Dockerfile, Python, and TypeScript payloads use the same path
with a single component address.

## Compose Admission

Catch performs admission before pulling, building, creating a network, or
starting a container:

1. Run `docker compose config --format json` on the operator definition to let
   Docker resolve interpolation, includes, extensions, profiles, and short
   syntax into its canonical application model.
2. Decode that model with a versioned ISO-safe profile. Unknown service-level
   fields fail closed until classified.
3. Generate the ISO network/DNS/address overlay.
4. Resolve the complete merged model again.
5. Re-run the safe-profile and topology validation on the exact model that
   `docker compose up` will use.
6. Start only after network policy and attachment verification succeed.
7. Inspect created containers and stop the project if actual network,
   namespace, privilege, or component state differs from the admitted model.

The validator uses Docker's canonical JSON output rather than implementing a
second Compose/YAML parser.

At minimum, ISO rejects:

- `ports` and any Yeet published-port state;
- `network_mode`, alternate service networks, external networks in use,
  `links`, and `external_links`;
- `privileged`, `cap_add`, host PID/IPC/cgroup/user namespaces, devices,
  device-cgroup rules, and unconfined security profiles;
- custom DNS and search-domain settings that bypass ISO DNS;
- Docker, containerd, CRI, network-namespace, or equivalent host-control
  socket/handle mounts;
- bind mounts outside the service root;
- `volumes_from` references that cross the admitted project boundary;
- Catch-side Compose `build` execution and host-side providers;
- replica counts, scaling, and more than 29 active components;
- any component that is not attached exclusively to the generated ISO default
  network;
- unknown service-level fields not yet classified by the current safe-profile
  version.

The safe profile permits ordinary application configuration such as prebuilt
images, commands, entrypoints, users, working directories, environment,
health checks, restart behavior, resource limits, logging, dependencies,
labels, read-only filesystems, temporary filesystems, safe secrets/configs,
named project volumes, and service-root-confined storage.

A Dockerfile passed directly to `yeet run` is built on the client before the
resulting image is transferred. ISO applies to the deployed runtime, not the
client-side build. Compose `build` is rejected because it would execute build
steps through the Catch Docker daemon before the ISO runtime boundary exists.

## Packet Policy

ISO uses stateful policy. The decision order is:

1. Silently drop malformed, invalid, or source-spoofed traffic.
2. Accept `ESTABLISHED` and `RELATED` traffic.
3. Accept locally originated Catch traffic to live ISO VM and component
   addresses on every protocol and port.
4. Accept same-project component traffic inside its project namespace.
5. Accept workload DNS to the dedicated ISO resolver on TCP/UDP port 53.
6. For `iso,ts`, accept tailnet traffic through that service's Tailscale
   interface and identity.
7. Reject workload traffic to non-public IPv4 destinations.
8. Reject direct public DNS and DNS-over-TLS that bypass the ISO resolver.
9. Accept remaining globally reachable public IPv4 traffic and masquerade it
   at Catch.
10. Reject everything else.

Policy-denied destinations use an active reject so applications fail quickly.
Malformed traffic and source spoofing drop silently.

The root policy distinguishes locally originated Catch traffic from forwarded
traffic arriving from LAN, Docker, `svc`, or another namespace. A LAN client
does not gain access merely because Catch has a route to an ISO address.

Per-interface source validation binds each outer ISO interface to its expected
link address and, for container projects, its assigned component prefix. A
workload cannot spoof Catch, another project, or a public source. Strict
reverse-path validation and layer-2 source checks apply where the backend and
VM TAP path require them.

Rule storage should keep chain shape nearly constant as workloads grow. Native
nftables implementations should use sets or maps for interfaces, prefixes, and
allocations. Iptables backends should use the closest supported set-based
representation and derive both renderers from the same policy data.

## Public IPv4 Classification

Public egress means globally reachable IPv4, not simply "not RFC1918." Keep one
reviewed, vendored destination classification derived from the IANA IPv4
Special-Purpose Address Registry. Both nftables and iptables paths use the same
source data and tests.

The denied set includes at least:

- current and future ISO pool space;
- all RFC1918 space;
- `100.64.0.0/10` shared/CGNAT space;
- loopback and link-local space;
- protocol-assignment and special-purpose ranges;
- documentation and benchmarking ranges;
- multicast, reserved, and limited broadcast space.

There is no runtime registry fetch. Updating the vendored classification is an
explicit reviewed code change. The policy description remains positive even
though the firewall implementation uses ordered special-purpose exclusions.

Public egress is otherwise protocol- and port-agnostic except for the DNS
enforcement exceptions. This design cannot identify DNS-over-HTTPS inside
permitted HTTPS traffic and does not claim to do so.

## DNS

ISO-only workloads use a dedicated Catch DNS listener or view. It is separate
from Yeet service discovery and:

- has no `yeet.internal` search domain;
- refuses Yeet-local forward and reverse zones;
- forwards ordinary external queries through the Catch upstream resolver;
- refuses or removes non-global and IPv6 address answers, including address
  hints where supported;
- listens only on ISO-reachable addresses;
- accepts requests only from expected ISO source prefixes;
- supports TCP and UDP port 53.

VMs use the host address of their `/30` as the resolver. Container components
use the project router, which forwards to the dedicated Catch listener. Direct
TCP/UDP 53 and DNS-over-TLS to public destinations are rejected. The packet
firewall remains authoritative even if a response contains an address the DNS
filter did not understand.

IPv6 is disabled in generated VM and Compose network configuration and dropped
on ISO links. The design does not synthesize or forward AAAA connectivity.

## Tailscale Combination

`iso,ts` is valid for non-VM services. The service-owned Tailscale daemon runs
inside the project router namespace:

- its control and data-plane connections reach public endpoints through ISO;
- ordinary default-route traffic continues to use ISO NAT;
- tailnet and advertised subnet routes use the service Tailscale interface;
- MagicDNS and tailnet split DNS use Tailscale DNS;
- the outer ISO path continues to reject direct CGNAT/private routing;
- tailnet access is attributed to the service identity and governed by tailnet
  policy, not inherited from Catch.

This is an explicit exception to the ISO-only "public internet and nothing
else" rule. Selecting `ts` says the workload should also have tailnet access.

VMs do not support Yeet-managed `iso,ts`. A VM guest can install Tailscale
itself using its permitted public egress. That guest-owned overlay is outside
Yeet VM network-mode management.

## Lifecycle and Failure Handling

Create and update order is security-sensitive:

1. Validate payload and network-mode compatibility.
2. Resolve and admit Compose when applicable.
3. Reserve and persist all addresses and identities.
4. Install aggregate and per-project DNS, firewall, source validation,
   blackhole, and route state.
5. Verify the installed policy.
6. Create the veth or VM TAP attachment.
7. Start and verify Tailscale when requested.
8. Start the untrusted workload last.

If any step before workload start fails, live state is removed in reverse
order. The persisted allocation remains reserved so a retry is stable and an
uncertain address is never assigned elsewhere.

Catch startup reconciles ISO state before starting or restarting ISO
workloads. Reconciliation checks:

- pool and route conflicts;
- aggregate blackhole state;
- expected interfaces, addresses, routes, and namespace identities;
- source-validation entries;
- firewall and NAT policy;
- DNS listener readiness;
- Docker network, component address, and privilege state;
- Tailscale namespace/interface state when requested.

Missing or unverifiable security state causes the workload to remain stopped
or become quarantined. Catch repairs and verifies the boundary before
restarting it. It never leaves a known untrusted workload running through a
partially reconstructed path.

Stopping or restarting a workload preserves allocations. Explicitly changing
away from ISO is a two-phase transition: establish the new desired mode, stop
the old attachment, verify cleanup, then release ISO state. A failed
transition retains enough old state for safe retry or rollback.

Removal order is:

1. stop workload execution;
2. stop the service Tailscale sidecar if present;
3. detach Docker endpoints or the VM TAP;
4. remove project and outer routes/links;
5. remove firewall, source-validation, NAT, and DNS state;
6. verify that no live endpoint uses the allocation;
7. release DB allocations.

Failed cleanup leaves a tombstone and blocks address reuse. Catch reports the
stuck resource and retries cleanup during reconciliation.

## Observability

`yeet info catch` reports:

- selected ISO pool and automatic/explicit source;
- allocator/policy version;
- link and project capacity/usage;
- active, reserved, quarantined, and tombstoned counts;
- current pool or reconciliation conflicts.

`yeet info <service>` reports:

- requested and effective ISO modes;
- ready, stopped, degraded, or quarantined state;
- public-egress and DNS policy;
- endpoint and component mappings;
- Tailscale state for `iso,ts`;
- the last admission or reconciliation error.

`yeet ip <service>` prints only user-connectable VM or component addresses with
stable labels. Outer router/link addresses are runtime details and remain in
the detailed `info`/JSON representation rather than the default endpoint list.

## Authorization

ISO mutation is high-trust service and host management:

- `manage` is required to create, update, remove, transition, repair, or
  configure ISO state.
- `manage` is required for `yeet host set --iso-pool` and its plan/apply RPCs.
- `read` is required to inspect pool state, workload state, component
  addresses, and VM connection metadata.
- VM guest SSH uses `read` for connection metadata and the guest SSH key for
  login. It does not require Catch's host/service shell `ssh` permission.

Every new or changed CLI command, RPC, web action, exec route, and background
operation must have an explicit permission classification and positive and
negative authorization tests.

## Alternatives Considered

### Shared isolated bridge with protected ports

A shared bridge with bridge-port isolation is simpler and more address
efficient, but the security claim depends on every layer-2 control remaining
correct. ARP, neighbor discovery, broadcast behavior, and backend differences
make it easier to accidentally recreate peer connectivity. Dedicated links and
project namespaces make the topology express the policy.

### Reuse `svc` and block peers in the firewall

`svc` intentionally provides a shared service subnet and Yeet DNS. Firewall
rules above that shared layer-2 segment cannot reliably create the singular
boundary ISO promises. It would also couple the new security contract to
existing service-network compatibility behavior.

### One `/30` and one IP for every Compose project

A Compose project may contain several independent containers. One address
cannot identify every component without ambiguous port ownership, a primary
container convention, or mandatory publication rules. A routed project `/27`
keeps direct Catch reachability and stable component identities.

### Silently ignore conflicting Compose settings

The host Docker daemon applies the Compose model. `network_mode: host`, an
external network, or a host-control socket can bypass the Yeet network rather
than harmlessly fail. Admission must reject conflicts before start.

### Allow root native processes with a warning

Host root can alter or leave the namespace, so a warning would turn a security
boundary into operator folklore. Rejecting native root payloads is the only
honest first version. Non-root systemd sandboxing is a separate prerequisite.

## Automated Tests

Unit and table-driven tests cover:

- CLI, web-draft, RPC, and Catch network-mode validation;
- payload compatibility and combination errors;
- preferred/fallback pool selection and live collision checks;
- `/16` partition math, capacity, exhaustion, stable reuse, cloning, and
  concurrent allocation;
- component mapping, project limits, tombstones, and two-phase transitions;
- Compose canonical-model decoding and safe-profile admission;
- interpolation, includes, extensions, profiles, ports, alternate networks,
  privileges, sockets, builds, providers, unknown fields, and scaling;
- nftables, iptables-nft, and iptables-legacy rendering from one policy model;
- public/special-purpose range classification and rule ordering;
- source spoofing, Catch-originated ingress, same-project traffic, DNS,
  Tailscale exceptions, NAT, and IPv6 rejection;
- DNS filtering and Yeet-local-zone refusal;
- lifecycle failure injection after every transition;
- idempotent crash recovery, reconciliation, quarantine, and cleanup;
- status/RPC JSON round trips and backward compatibility;
- positive and negative `read`, `manage`, and VM SSH permission mappings.

Fuzz network-mode normalization, address/prefix allocation inputs, Compose
canonical-model decoding, DNS response filtering, and all touched network/RPC
input codecs. Commit minimized corpus entries for defects found.

Privileged Linux integration tests create real namespaces, veths, routes,
listeners, and firewall rules and prove:

- Catch can initiate to an ISO endpoint;
- public IPv4 and ISO DNS work;
- Catch, LAN, `svc`, metadata, CGNAT, another ISO project, direct DNS, and IPv6
  are unreachable from the workload;
- same-project components communicate;
- source spoofing fails;
- stateful replies work;
- both nftables and available iptables backends enforce equivalent behavior.

## Live Acceptance

On a real Catch host, deploy two ISO container projects and one ISO VM. Verify:

- Catch reaches every component address and the VM on arbitrary test ports;
- `yeet ssh <iso-vm>` succeeds through the Catch proxy;
- workloads reach public IPv4 through NAT;
- ISO DNS resolves public names but not Yeet-local/private answers;
- direct public DNS, Catch, LAN, `svc`, metadata, CGNAT, and the other ISO
  project fail;
- IPv6 egress fails;
- `iso,ts` reaches MagicDNS and tailnet services through its own identity while
  ordinary public egress still uses ISO;
- component and VM addresses remain stable across workload, Docker, and Catch
  restarts;
- deliberate policy drift or pool conflict quarantines the workload;
- unsafe Compose definitions fail before container start;
- removal cleans live state before address reuse;
- a simulated partial cleanup leaves and later clears a tombstone.

Before integration, run targeted tests plus:

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Run applicable race and fuzz targets for touched parser, allocator, RPC,
reconciliation, and network state-machine code.

## Documentation

Update all user-facing surfaces in the same implementation session:

- CLI help and examples;
- README network-mode summary;
- the website networking concept page;
- container and VM payload pages;
- DNS and Tailscale pages;
- permissions/access-grants documentation;
- `info`, `ip`, and troubleshooting guidance;
- web-run network-mode choices and validation messages.

The documentation must state the trust boundary, supported payload matrix,
Compose safe-profile behavior, IPv4-only limitation, Catch-only ingress,
DNS-over-HTTPS limitation, and the fact that native root workloads require a
future non-root sandbox.
