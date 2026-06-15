# VM Vsock Agent Design

## Goal

Add a general observe-only guest agent for yeet VMs, carried over Firecracker
virtio-vsock. The first supported capability is live network-state reporting so
`yeet ip <vm>` and `yeet ssh <vm>` ask the VM for its current non-loopback IP
addresses instead of relying on stale journal output, host neighbor tables, or
persisted dynamic addresses.

This design intentionally starts as observe-only. It creates a reusable guest
control-plane boundary without adding host-to-guest command execution.

## Context

Current VM IP discovery has three sources:

- configured static VM network IPs, primarily for service networking;
- serial/journal markers such as `yeet-ip` and `yeet-ready`;
- host neighbor-table lookup for LAN interfaces by MAC address.

Those sources are fragile for dynamic LAN DHCP. Journal markers can age out,
neighbor entries can disappear, and persisting a DHCP address as configured
state can become wrong after a lease change. The correct operational source for
`yeet ip` and `yeet ssh` is live guest state.

The repo already has a Rust guest integration point:

- `guest/yeet-init` is built with the repo Rust toolchain and embedded in
  official VM images.
- `yeet-vm-images` already consumes that Rust output for Ubuntu and NixOS
  image builds.
- catch owns Firecracker config rendering and guest metadata injection.

Firecracker supports a virtio-vsock device that mediates host Unix sockets and
guest AF_VSOCK sockets. For host-initiated connections, the host connects to
the Firecracker vsock Unix socket and sends `CONNECT <port>`. For guest-
initiated connections, the guest connects to host CID `2` and the configured
port. The Firecracker docs also note the required guest kernel support:
`CONFIG_VIRTIO_VSOCKETS=y`.

References:

- Firecracker vsock documentation:
  https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
- firecracker-containerd design note on vsock as a runtime-agent channel:
  https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/design-approaches.md

## Architecture

V1 uses a host-initiated observe-only agent.

`catch` configures a Firecracker vsock device for each new VM and records the
runtime vsock socket path and guest CID in VM metadata. A new Rust
`yeet-agent` binary runs as a normal systemd service inside official VM images.
The agent listens on a well-known vsock port and serves a small request/response
protocol.

For commands that need current addresses, catch opens the Firecracker host-side
vsock Unix socket, sends `CONNECT <agent-port>`, then sends a network-state
request. The agent responds with current non-loopback interface addresses from
inside the guest.

Live vsock query is authoritative for operational commands. catch can persist a
response as observed telemetry with source and timestamp, but observed data is
not configured state and must not silently override a failed live query for
SSH.

## Components

### `yeet-init`

`yeet-init` remains a small pre-systemd boot shim. It keeps its current boot
responsibilities and continues emitting serial `yeet-ip` markers for boot
diagnostics and compatibility.

It must not become a long-running daemon.

### `yeet-agent`

`yeet-agent` is a new Rust guest binary under `guest/yeet-agent`.

V1 responsibilities:

- listen on the yeet agent vsock port;
- answer `hello`, `ping`, and `network_state` requests;
- read current guest network state from Linux tools or kernel interfaces;
- return only usable non-loopback addresses;
- reject unknown protocol versions and request types clearly;
- keep resource use low enough for small microVMs.

V1 non-goals:

- command execution;
- file copy;
- package or service management;
- host-initiated mutation of guest state;
- authentication beyond the VM-local vsock boundary.

### catch VM Agent Client

catch gets a focused VM agent boundary in a new `vm_agent.go`.

Responsibilities:

- render Firecracker vsock config;
- derive per-VM vsock paths from service runtime paths;
- dial the Firecracker host-side vsock Unix socket;
- perform Firecracker's host-initiated `CONNECT <port>` handshake;
- frame JSON requests and responses;
- validate protocol version, request IDs, response types, and timeouts;
- expose typed helpers such as `queryVMNetworkState`;
- map returned network state to `ServiceIP` and VM SSH host selection.

## Protocol

Use newline-delimited JSON over one vsock stream. This avoids gRPC or ttrpc in
v1, keeps the guest binary small, and leaves the protocol easy to test with
plain fixtures.

The v1 agent listens on vsock port `7788`. The value is a yeet-owned constant,
not a user-facing API. It can become configurable later if another legitimate
guest service needs the same port.

Every request includes:

- `protocol`: integer protocol version, initially `1`;
- `type`: request type;
- `request_id`: caller-generated opaque string.

Every response includes:

- `protocol`;
- `type`;
- `request_id`;
- either result fields or an `error` object.

Example network-state request:

```json
{"protocol":1,"type":"network_state","request_id":"r1"}
```

Example network-state response:

```json
{"protocol":1,"type":"network_state","request_id":"r1","interfaces":[{"name":"eth0","mac":"bc:24:11:45:b6:a2","up":true,"ips":["10.0.4.183"]}]}
```

Address filtering:

- exclude loopback interfaces;
- exclude loopback addresses;
- v1 returns IPv4 addresses only, matching current SSH/IP behavior;
- IPv6 is intentionally reserved for a later protocol-compatible extension;
- preserve interface names so catch can match configured VM networks.

## Command Behavior

### `yeet ip <vm>`

Preferred source order:

1. configured static network IPs;
2. live `yeet-agent` network-state query;
3. compatibility fallback from fresh journal markers;
4. compatibility fallback from host neighbor table;
5. no IP known.

The command must not print a stale observed dynamic IP when live query fails.
If no trusted source exists, it should return a clear error instead of an empty
successful output.

### `yeet ssh <vm>`

Preferred source order:

1. configured static service-network IP;
2. live `yeet-agent` network-state query;
3. compatibility fallback that is at least as trustworthy as current behavior;
4. fail closed with a clear reason.

`yeet ssh` should not silently use a stale observed DHCP address. A DHCP lease
can still change between query and SSH connection, but that window is small and
is the correct tradeoff for a direct SSH command.

### `yeet info <vm>`

`yeet info` stores source metadata internally and keeps the human output compact
in v1. The JSON form can expose agent availability and source fields because it
is the better debugging surface for automation and tests.

## Compatibility

New VM images include `yeet-agent`, a systemd unit, and guest kernel support for
virtio-vsock.

Existing VMs without vsock metadata or without the agent continue through the
legacy fallback path. The fallback path remains useful for old VMs and for
diagnosing agent failures, but it is no longer the primary source for dynamic
LAN addresses.

Image verification must check:

- `yeet-agent` is installed;
- the `yeet-agent` systemd unit is enabled;
- `/dev/vsock` support exists or the image kernel config includes
  `CONFIG_VIRTIO_VSOCKETS=y`;
- Ubuntu and NixOS image docs describe the guest agent contract.

## Error Handling

Agent query errors are classified:

- no vsock metadata;
- Firecracker vsock socket missing;
- connect handshake failed;
- agent not listening;
- timeout;
- protocol version mismatch;
- malformed JSON;
- response request ID mismatch;
- agent returned no usable addresses.

Interactive timeouts are short. A dead VM or broken agent must not make
`yeet ip` or `yeet ssh` hang.

Fallback use is explicit in code. It is acceptable to use configured
static IPs without contacting the agent. It is not acceptable to store a
discovered DHCP address into the configured `IP` field.

## Observed State

If catch persists observed agent output, it uses a separate observed
state model, not `VMNetworkConfig.IP`.

Observed records include:

- interface name;
- IP address;
- source, such as `agent`, `journal`, or `neighbor`;
- timestamp;
- optional MAC address.

Observed state is useful for diagnostics and status, but must not become the
silent authority for `yeet ssh` when the live agent is unavailable.

## Testing

Rust tests:

- parse network address command output or kernel data;
- filter loopback and unusable addresses;
- produce stable JSON responses;
- reject unsupported protocol versions and unknown requests.

Go unit tests:

- Firecracker vsock JSON rendering;
- host-initiated `CONNECT <port>` handshake handling;
- request/response framing and validation;
- timeout behavior;
- fallback ordering for `yeet ip`;
- SSH host selection from live agent results;
- failure when only stale dynamic observed data exists.

Image tests:

- Ubuntu image contains `yeet-agent` and enables its systemd unit;
- NixOS image contains `yeet-agent` and enables its systemd unit through the
  NixOS module;
- kernel config has virtio-vsock guest support.

Live smoke test:

- create a disposable VM on `pve1`;
- verify `yeet ip <vm>` returns the live LAN address through the agent;
- remove journal and neighbor-table evidence where safe;
- verify `yeet ip <vm>` still works through the live vsock query;
- verify `yeet ssh <vm>` selects the same live address.

## Rollout

1. Add protocol and catch-side agent client tests.
2. Add Firecracker vsock config support for new VM provision plans.
3. Add the Rust `yeet-agent` crate and tests.
4. Add image build integration for Ubuntu and NixOS.
5. Wire `yeet ip`, `yeet ssh`, and VM info paths to prefer live agent state.
6. Preserve and test legacy fallback behavior.
7. Run a live `pve1` disposable VM smoke test.
