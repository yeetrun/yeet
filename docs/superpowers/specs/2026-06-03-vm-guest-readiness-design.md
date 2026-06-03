# VM Guest Readiness Design

## Summary

Make VM deploys wait for a normal guest readiness signal before reporting that
the VM is running.

Today `yeet run <svc> vm://ubuntu/26.04` finishes as soon as the Firecracker
systemd unit restarts successfully. That only proves the host-side VM process
started. The guest may still be booting, acquiring a LAN address, and starting
SSH. A user can immediately run `yeet ssh <svc>` and hit a stale or not-yet
reachable address, then retry a few seconds later and succeed.

The fix is to strengthen the guest-side readiness marker that yeet already
injects into VM images. The marker should be emitted only after the guest has
reached systemd's normal network and SSH startup points and has a global IPv4
address. The catch deploy path should wait for a fresh marker from the current
VM start before printing `VM <svc> is running`.

## Goals

- Make `yeet run ... vm://...` return only after the guest is practically ready
  for `yeet ssh`.
- Avoid TCP probing or client-side SSH simulation in the first implementation.
- Use systemd ordering and the existing serial/journal signal path.
- Ignore stale readiness or IP lines from previous VM starts.
- Keep the user-facing deploy output simple and accurate.
- Fail with a clear console hint if the guest never reports readiness.

## Non-Goals

- No proof that the user's local machine can route to a LAN VM address.
- No SSH authentication or handshake probe.
- No new CLI flag for readiness behavior in the first implementation.
- No migration requirement for already-running VMs.
- No broader VM health system beyond deploy-time readiness.

## Current Behavior

The VM provision flow writes metadata, configures networking, installs the
systemd unit, commits the service as ready, restarts the unit, and immediately
prints:

```text
VM devbox is running.
SSH: yeet ssh devbox
```

The guest image contains a `yeet-guest-ready.service`, but it currently emits
`guest-ready` early and then reports `yeet-ip` lines asynchronously. Service
info discovers LAN IPs by reading recent VM journal output and, if needed,
neighbor entries. Because the journal scan is not scoped to the current VM
start, `yeet ssh` can briefly see an old address or no current address.

## Guest Readiness Marker

The injected `yeet-guest-ready.service` should become the source of truth for
deploy readiness. It should be ordered after the normal boot dependencies that
matter for SSH:

```ini
After=network-online.target ssh.service serial-getty@ttyS0.service
Wants=network-online.target ssh.service serial-getty@ttyS0.service
```

The guest script should synchronously wait for at least one global IPv4 address
before emitting the authoritative marker. The marker should include interface
and IP:

```text
yeet-ready eth0 10.0.4.178
```

`ServiceInfo` should treat `yeet-ready <iface> <ip>` as an IP report for that
interface. The script may also continue to emit `yeet-ip <iface> <ip>` lines
afterward for diagnostics and address updates, but deploy readiness should key
off `yeet-ready`.

This signal means systemd has run the readiness unit after SSH and
network-online, and the guest has an IPv4 address. It does not prove end-to-end
client routing, but it removes the practical early-return race while staying
close to standard boot readiness semantics.

## Deploy Flow

After `restartVMSystemdUnit` succeeds, the VM provision flow should:

1. Print a concise progress line such as `Waiting for guest readiness...`.
2. Wait for a fresh `yeet-ready <iface> <ip>` marker from the current VM start.
3. Accept the marker only if it refers to a configured VM interface.
4. Continue to `printVMNextCommands` only after the marker is observed.
5. Fail with a clear message if readiness times out.

The existing `VM <svc> is running` line should be printed only after the guest
marker is observed. `--restart=false` behavior should remain unchanged: yeet
should install the VM and print the existing "ready, start with..." message
without waiting for a guest boot that did not happen.

## Freshness

The wait must not trust stale journal output. It should establish a freshness
boundary at VM restart time, then wait for lines written after that point.

The first implementation should capture a journal cursor for the VM unit
immediately before restarting the VM and then wait with `journalctl
--after-cursor` semantics. If the host has no cursor yet for a brand-new unit,
the wait can fall back to a timestamp captured immediately before restart. The
important property is that old `yeet-ready` or `yeet-ip` lines from previous
boots cannot satisfy the new deploy.

Service info can continue to parse recent logs for normal display, but deploy
readiness should use the fresh wait path.

## Error Handling

If the guest never emits readiness within the timeout, the deploy should fail
with enough context to help the user recover:

```text
VM devbox started, but guest readiness was not reported within 60s; use `yeet vm console devbox`
```

The VM service may still be running in this case. The error should not roll back
the service record or destroy data, because the host-side VM start succeeded and
the console is the right recovery path.

If journal access fails, report that directly rather than pretending the guest
is ready. If the marker contains an invalid IP or unknown interface, ignore it
and keep waiting until timeout.

## Data Flow

Guest:

- `yeet-guest-ready.service` runs after SSH and network-online.
- The guest script waits for a global IPv4 address.
- The script writes `yeet-ready <iface> <ip>` to `/dev/ttyS0`.

Host:

- `vm-run` forwards Firecracker console output to the VM unit stdout.
- systemd journals the VM console output.
- VM provision waits on fresh journal output for `yeet-ready`.
- Service info parses `yeet-ready` and `yeet-ip` lines so later `yeet ssh`
  calls use the same current address.

Client:

- No new client-side readiness behavior is required.
- `yeet ssh` keeps using `ServiceInfo` and the existing VM SSH option planning.

## Tests

Add focused unit tests for:

- parsing `yeet-ready <iface> <ip>` markers.
- treating `yeet-ready` as an IP report in service info.
- rejecting malformed readiness lines.
- guest metadata containing `network-online.target` and SSH ordering.
- guest readiness script waiting for IP before emitting `yeet-ready`.
- deploy waiting after VM restart before printing `VM <svc> is running`.
- stale pre-restart readiness lines not satisfying deploy readiness.
- timeout errors including the VM console hint.
- `--restart=false` skipping guest readiness wait.

Run targeted tests for `pkg/catch` after implementation. Before merging, run a
live pve1 VM deploy and immediately run:

```bash
yeet ssh devbox -- true
```

The immediate SSH command should succeed without needing a retry.

## Acceptance Criteria

- `yeet run ... vm://...` does not print `VM <svc> is running` until fresh guest
  readiness is observed.
- Immediate `yeet ssh <svc>` after VM deploy works on the pve1 LAN case.
- Old journal IPs from previous VM boots cannot satisfy the readiness wait.
- Readiness timeout leaves the VM available for console debugging and reports a
  clear error.
- Existing service info and SSH option behavior remain compatible.
