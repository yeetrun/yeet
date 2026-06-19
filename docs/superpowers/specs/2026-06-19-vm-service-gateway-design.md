# VM Service Gateway Cleanup Design

## Context

VMs on the yeet service network currently receive `192.168.100.254` as their
guest default gateway. That address lives on `br0` inside the shared
`yeet-ns` namespace. The same namespace then routes default traffic to
`192.168.100.1` on the same bridge, where the host-side `yeet0` endpoint
provides DNS and egress.

This works, but it creates an avoidable extra hop. Linux correctly emits ICMP
redirects telling guests to use `192.168.100.1` directly:

```text
From _gateway (192.168.100.254) Redirect Host(New nexthop: 192.168.100.1)
```

The correct long-term model is simpler: VMs should use `192.168.100.1` as the
service-network gateway because that is already the service-network DNS and
egress point.

## Goals

- Make new and reconfigured VMs use `192.168.100.1` as the service-network
  gateway.
- Keep yeet service-network DNS at `192.168.100.1`.
- Preserve VM-to-service traffic by keeping the existing shared `yeet-ns`
  bridge topology.
- Avoid a general migration framework. The only existing VM host is
  `root@lab-host`, so existing VM cleanup can be handled with one-off surgical
  host changes after the code is correct.

## Non-Goals

- Do not add compatibility migration code for historical VM metadata.
- Do not redesign the full service-network address plan.
- Do not change Docker compose service-network DNS behavior.
- Do not remove `192.168.100.254` from the shared namespace unless it is proven
  unused by the current bridge setup.

## Design

Use separate names for the two service-network roles:

- `192.168.100.1`: host gateway, DNS, and VM service-network gateway.
- `192.168.100.254`: legacy shared namespace bridge address, retained only for
  the `yeet-ns` bridge if needed.

VM service-network planning should set the guest gateway to `192.168.100.1`.
That value flows into:

- kernel boot arguments for catalog images that consume static `ip=...`
  settings;
- injected VM network metadata for images that use guest metadata files;
- `yeet vm set` or other VM config regeneration paths that rebuild network
  metadata.

The shared `yeet-ns` script can continue to expose `192.168.100.254/32` on
`br0` as an internal bridge address, but it should no longer be described or
used as the VM guest gateway.

## Existing VM Handling

After the code lands, update `root@lab-host` VMs directly. The acceptable approach
is to regenerate VM network metadata or edit the generated guest network config
so service-network interfaces use `192.168.100.1` as their gateway, then reboot
or otherwise refresh the affected VMs.

No persistent migration logic should be added for this cleanup.

## Verification

Automated checks:

- Update VM network tests so service-network plans, boot args, and metadata
  expect `192.168.100.1` as the guest gateway.
- Keep tests that prevent broad host routes from returning to per-VM bridges.
- Run targeted VM network and metadata tests.
- Run the full Go test suite before integration.

Live checks on `root@lab-host`:

- A VM on `svc` networking can resolve service names through
  `192.168.100.1`.
- `ping google.com` from the VM succeeds without ICMP redirect output.
- `curl http://sonarr:8989`, `tracepath sonarr`, and `ping sonarr` can be run
  repeatedly from the VM without hanging the VM SSH session.
- VM-to-service and internet egress both continue to work.

