# VM Design

## Summary

Add experimental, long-lived named VMs to yeet using the existing
`yeet run` model.

The v0 user-facing flow is:

```bash
yeet run devbox vm://ubuntu/26.04 --net=svc
yeet run devbox vm://ubuntu/26.04 --net=lan
yeet run devbox
yeet start devbox
yeet stop devbox
yeet restart devbox
yeet info devbox
yeet ssh devbox
yeet vm console devbox
```

The only v0 guest payload is yeet's own optimized Ubuntu 26.04 VM image.
Arbitrary user kernels, root filesystems, and operating systems are out of
scope for v0.

## Goals

- Let users create and manage long-lived named VMs with the same high-level
  lifecycle as yeet services.
- Keep VMs experimental and scoped while preserving the existing service
  workflow.
- Boot a yeet-owned Ubuntu VM on `yeet-lab` into a usable serial console in
  under five seconds.
- Provide automatic SSH setup and make `yeet ssh <name>` enter the guest when
  the named target is a VM.
- Support Debian and Ubuntu hosts with minimal global host changes.
- Use Firecracker directly without libvirt, Kata, containerd VM integration, or
  Proxmox-specific APIs.
- Support yeet-managed NAT networking and LAN-visible networking.
- Use sparse storage by default and integrate cleanly with ZFS when the service
  root is ZFS-backed.

## Non-Goals

- No arbitrary user-supplied rootfs, kernel, ISO, or cloud image in v0.
- No Windows, non-Linux guests, or non-x86_64 host support in v0.
- No libvirt, virt-manager, cloud-hypervisor, Kata, or Kubernetes integration.
- No DNS or name-to-name VM/service discovery in v0.
- No live CPU, memory, disk, or network shape changes in v0.
- No automatic choice of a ZFS pool when the user has not requested a ZFS root.
- No host without KVM fallback. Hosts without `/dev/kvm` should fail clearly.

## Existing Context

`yeet run` already accepts several payload shapes: binaries, scripts, Docker
Compose files, Dockerfiles, remote container images, and local container images.
The client stores replayable run recipes in `yeet.toml`, while catch remains
authoritative for live deployed state.

Network modes already include:

- `svc`: a yeet-managed NAT service network under `192.168.100.0/24`.
- `lan`: a macvlan-backed LAN-visible mode with parent, VLAN, and MAC options.
- `ts`: Tailscale-backed networking.

The service network is implemented with a persistent `yeet-ns` namespace,
bridge, host route, and firewall rules. Individual services get stable service
IPs and service-specific namespace plumbing. VMs should reuse the same
addressing and firewall model where practical, but Firecracker requires TAP
devices instead of Docker network driver integration.

Current host observations:

- `yeet-lab` is Debian 13 / Proxmox with 12 logical CPUs, about 31 GiB RAM,
  `/dev/kvm`, `qemu-img`, and ZFS pools including `flash`.
- `yeet-cloud` is Ubuntu 24.04 with 2 logical CPUs and about 1.9 GiB RAM, but it
  does not expose `/dev/kvm`. VM commands should fail there until KVM or
  nested virtualization is available.

## CLI

VM creation and updates use `yeet run` with a VM payload URI:

```bash
yeet run devbox vm://ubuntu/26.04 --net=svc
```

`vm://ubuntu/26.04` means "use yeet's optimized Ubuntu 26.04 VM bundle".
The scheme avoids ambiguity with Docker image names and keeps the payload
self-describing in config.

The local config entry should look like:

```toml
[[services]]
name = "devbox"
host = "yeet-lab"
type = "vm"
payload = "vm://ubuntu/26.04"
args = ["--net=svc"]
```

The catch DB service type should also be `vm`. Firecracker remains an
implementation detail; user-facing type values, commands, and info output
should say `vm` or `VM`.

VM-only commands live under a new `vm` group:

```bash
yeet vm console devbox
```

Only `console` is required for v0. Future VM-specific commands such as image
cache management can be added under the same group.

Existing lifecycle commands should work:

```bash
yeet start devbox
yeet stop devbox
yeet restart devbox
yeet status devbox
yeet info devbox
yeet remove devbox
yeet ssh devbox
```

`yeet ssh <name>` should inspect service info. If the target is a VM, it should
SSH into the guest. If the target is a normal service, it keeps the existing
behavior of entering the service data directory on the host.

## Runtime

Use Firecracker directly in v0.

Catch owns:

- Firecracker binary installation or cache.
- Firecracker config generation.
- VM systemd unit generation.
- VM lifecycle commands.
- VM serial console socket.
- Guest metadata injection.
- VM state in the catch DB.

Do not install or depend on libvirt, Kata, cloud-hypervisor, containerd VM
runtimes, or Proxmox APIs. The host requirement is Linux KVM plus the narrow
set of Linux networking and storage tools yeet already uses or explicitly
manages.

Each VM runs as a systemd-managed Firecracker process. Catch should generate a
durable service unit and artifact set so start, stop, restart, status, rollback,
remove, and startup reconciliation can work like other yeet-managed services.

## Image Bundle

The v0 image is a yeet-owned optimized Ubuntu 26.04 image bundle, hosted from a
GitHub release or another yeet-owned GitHub asset location.

Bundle contents should be versioned and checksummed:

```text
yeet-ubuntu-26.04-amd64-v0/
  firecracker
  vmlinux
  rootfs.ext4.zst
  manifest.json
  checksums.txt
```

`manifest.json` should describe:

- image name and version
- Ubuntu version
- architecture
- kernel command line requirements
- uncompressed rootfs size
- recommended minimum memory and disk
- checksums for each artifact

The hot boot path should not run stock cloud-init. Yeet should own the rootfs
and inject deterministic first-boot metadata before the first boot:

- hostname
- authorized SSH keys
- default username
- network config
- machine-id reset or seed
- serial console configuration
- guest-ready marker setup

The guest should boot through Firecracker direct kernel boot with an ext4 root
device and serial console enabled. The v0 boot acceptance target is serial
console or a guest-ready marker in under five seconds on `yeet-lab`. SSH
readiness should be automatic, but can be measured separately because LAN DHCP
may add latency.

Ubuntu 26.04 LTS is current in the 2026 cycle. Ubuntu's official release notes
and release index list Ubuntu 26.04 LTS as an available LTS release.

References:

- https://documentation.ubuntu.com/release-notes/26.04/
- https://documentation.ubuntu.com/project/release-team/list-of-releases/
- https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md

## Sizing Defaults

Defaults should create a comfortable Ubuntu server VM on capable hosts, while
clamping down on small VPS hosts. Do not choose the smallest possible VM
defaults just because Firecracker can run tiny guests.

CPU default:

```text
host CPUs == 1    -> 1 vCPU
host CPUs 2..7    -> 2 vCPU
host CPUs >= 8    -> 4 vCPU
```

Allow CPU overcommit across VMs by default. If a single VM explicitly requests
more vCPUs than host logical CPUs, return a warning or require an explicit
override in a later flag.

Memory default:

```text
host RAM <= 2 GiB   -> 512 MiB
host RAM <= 4 GiB   -> 1 GiB
host RAM < 16 GiB   -> 2 GiB
host RAM >= 16 GiB  -> 4 GiB
```

Do not overcommit memory by default. Starting a VM should check the sum of
running yeet VM memory plus the requested VM against a host reserve:

```text
host reserve = max(1 GiB, min(4 GiB, 10% of host RAM))
```

If the request exceeds the available memory budget, reject with a clear error.
An explicit memory-overcommit escape hatch can be considered later.

Disk defaults:

```text
ZFS available >= 512 GiB  -> 128 GiB sparse zvol
ZFS available >= 128 GiB  -> 64 GiB sparse zvol
ZFS available >= 64 GiB   -> 32 GiB sparse zvol
ZFS available >= 32 GiB   -> 16 GiB sparse zvol
otherwise                 -> 8 GiB or reject if too tight

Non-ZFS available >= 48 GiB -> 32 GiB sparse raw
Non-ZFS available >= 24 GiB -> 16 GiB sparse raw
Non-ZFS available >= 12 GiB -> 8 GiB sparse raw
otherwise                   -> reject
```

Example defaults:

`yeet-lab` with ZFS:

```bash
yeet run devbox vm://ubuntu/26.04 --net=svc --service-root=flash/yeet/vms/devbox --zfs
```

```text
cpus:   4
memory: 4 GiB
disk:   128 GiB sparse zvol
```

`yeet-lab` without ZFS:

```bash
yeet run devbox vm://ubuntu/26.04 --net=svc
```

```text
cpus:   4
memory: 4 GiB
disk:   32 GiB sparse raw file
```

`yeet-cloud` today:

```text
reject: /dev/kvm is missing
```

If `yeet-cloud` exposed KVM with its current size:

```text
cpus:   2
memory: 512 MiB
disk:   8 GiB sparse raw file
```

Override flags:

```bash
yeet run devbox vm://ubuntu/26.04 --cpus=2 --memory=2g --disk=16g
```

These flags are VM-only run flags. Supplying them for non-VM payloads should be
rejected.

## Storage

Use sparse storage by default.

On non-ZFS roots, the VM root disk is a sparse raw ext4 file under the service
root, for example:

```text
<service-root>/data/rootfs.raw
```

On ZFS-backed service roots, use zvols. A vdev is not the right abstraction; a
zvol provides the block device Firecracker needs.

Recommended ZFS layout:

```text
flash/yeet/base/ubuntu-26.04@<image-version>
flash/yeet/vms/devbox/root
```

The VM zvol should be a sparse clone from the yeet base image snapshot where
possible, resized when `--disk` is larger than the base. This gives fast VM
creation, low initial disk use, cheap snapshots, and a natural path to future
rollback.

If the host has ZFS but the user does not pass `--service-root=<dataset> --zfs`
or configure a future default ZFS dataset, yeet should not guess a pool.

Disk creation must be staged:

1. Resolve the target root and storage backend.
2. Ensure the base image is downloaded and verified.
3. Create the disk or zvol.
4. Inject metadata.
5. Generate Firecracker config and systemd artifacts.
6. Commit catch DB state only after required artifacts exist.

If disk creation succeeds and later setup fails, keep the disk and report setup
as incomplete rather than deleting possible user data.

## Networking

Support:

```bash
--net=svc
--net=lan
--net=svc,lan
```

`--net=svc` should reuse the existing yeet service network if practical:

- `192.168.100.0/24`
- stable per-VM IP from catch DB
- host route and NAT through yeet-managed network/firewall state
- service-to-VM and VM-to-service connectivity by IP

The implementation differs from Docker services. Firecracker needs TAP devices:

```text
Firecracker guest eth0
  virtio-net
    tap device
      yeet-managed namespace or bridge path
        yeet-ns / host NAT
```

The first implementation should try to extend the current `yeet-ns` model
instead of creating a separate VM NAT island. If that proves too brittle,
falling back to a separate VM NAT bridge is acceptable only after documenting
the tradeoff that services and VMs would not share one IP network.

`--net=lan` should make the VM directly reachable from the LAN. Reuse the
existing service LAN semantics:

- default parent from the host default route
- optional `--macvlan-parent`
- optional `--macvlan-vlan`
- optional `--macvlan-mac`

The v0 guest should use DHCP on LAN. Catch should record the observed LAN IP
when it can discover it.

For `--net=svc,lan`, the guest gets two NICs:

```text
eth0: svc network, stable yeet IP
eth1: LAN network, DHCP
```

`yeet ssh <vm>` should prefer the stable `svc` IP when present. If the VM has
only LAN networking, use the discovered LAN IP.

DNS and name-based service-to-VM discovery are out of scope for v0.

## Data Model

Add a `vm` service type to the catch DB.

The service should store VM-specific configuration. The exact struct can be
embedded on `db.Service` or referenced through a nested optional field, but it
must be clone/view safe and migration tested.

Fields needed for v0:

```text
runtime: firecracker
image payload: vm://ubuntu/26.04
image version or digest
cpus
memory bytes
disk bytes
disk backend: raw or zvol
disk path or zvol path
network interfaces
ssh user
authorized key material or key reference
serial socket path
firecracker API socket path
pid file path
setup state
```

`yeet.toml` remains a replay recipe and should store the user-facing payload,
VM type, and run args. Catch remains authoritative for live VM state and
resolved host paths.

`ServiceInfo` should expose VM information so `yeet info` and `yeet ssh` do not
need to parse artifacts.

## Lifecycle

For a new VM:

1. Client recognizes `vm://ubuntu/26.04` and stores `type = "vm"`.
2. Client forwards the run request through the existing catch exec bridge.
3. Catch validates host capabilities.
4. Catch resolves sizing defaults and storage backend.
5. Catch downloads and verifies the image bundle if missing.
6. Catch creates the root disk or zvol.
7. Catch injects guest metadata.
8. Catch renders Firecracker config and systemd unit.
9. Catch commits DB state and starts the VM unless `--restart=false` is passed.

For an existing VM:

- `yeet run devbox` replays the stored VM config.
- `yeet run devbox vm://ubuntu/26.04 ...` updates the VM definition.
- CPU, memory, disk, image, and network shape changes require the VM to be
  stopped in v0.
- Disk size can grow, but shrinking is rejected.

Existing lifecycle commands map to the VM systemd unit:

```text
start    -> systemctl start yeet VM unit
stop     -> systemctl stop yeet VM unit
restart  -> systemctl restart yeet VM unit
status   -> systemd status plus DB metadata
remove   -> stop VM, remove unit/artifacts, preserve disk unless existing
            remove semantics explicitly permit deleting managed data
```

`yeet vm console <name>` connects to the VM serial socket or pty. It should fail
clearly when the named service is not `type = "vm"`.

## Info Output

`yeet info <vm>` should show VM-specific fields:

```text
Service
  Name: devbox
  Type: VM
  Status: running

VM
  Runtime: firecracker
  Image: ubuntu 26.04
  CPU: 4
  Memory: 4 GiB
  Disk: 128 GiB zvol
  Console: available
  SSH: ubuntu@192.168.100.12

Network
  svc: 192.168.100.12
  lan: 10.x.x.x
```

JSON info should expose exact structured values, including bytes for memory and
disk sizes.

## SSH

`yeet ssh <name>` should branch on service type after fetching service info:

- normal service: existing behavior, SSH to host and `cd` into service data
  directory
- VM: SSH into the guest

The guest image should include an SSH server or equivalent lightweight SSH
daemon enabled by default. Yeet injects an authorized key during metadata setup.
The default guest user should be stable, likely `ubuntu`.

When multiple VM IPs exist, choose in order:

1. svc network IP
2. Tailscale IP if added later
3. LAN IP

If no IP is known, print a clear message that suggests `yeet vm console <name>`.

## Error Handling

Errors should identify host limitations and avoid partial-state claims.

Examples:

```text
VMs require Linux KVM; /dev/kvm is missing on host "yeet-cloud"
VMs require x86_64/amd64 in v0; got arm64
Firecracker bundle download failed: <error>
not enough memory to start "devbox": requested 4 GiB, available budget 2 GiB
VM shape changes require the VM to be stopped
LAN mode requires a parent interface; failed to detect host default route
service "api" is type "docker-compose"; vm console requires type "vm"
```

Failures before DB commit should leave no live VM record. Failures after disk
creation should keep the disk and return a message that explains the incomplete
setup and next cleanup step.

## Testing

Focused unit tests:

- CLI parsing:
  - `vm://ubuntu/26.04`
  - `--cpus`
  - `--memory`
  - `--disk`
  - `yeet vm console <name>`
- Client run draft/config:
  - VM payload saves `type = "vm"`
  - stored VM config rehydrates through `yeet run <name>`
  - VM-only flags reject non-VM payloads
- Sizing:
  - lab-host-like profile defaults to 4 vCPU, 4 GiB RAM
  - small KVM host defaults to 2 vCPU, 512 MiB RAM
  - no-KVM host rejects before setup
  - memory admission enforces host reserve
- Storage:
  - ZFS root selects sparse zvol
  - non-ZFS root selects sparse raw file
  - disk default clamps by available space
  - disk shrink rejects
- Firecracker:
  - config rendering
  - systemd unit rendering
  - serial socket path rendering
- Networking:
  - svc TAP plan
  - LAN TAP/macvlan plan
  - `svc,lan` two-interface plan
  - SSH target selection prefers svc IP
- Service info:
  - VM fields included in RPC response
  - plain info renders VM section
  - JSON info includes structured CPU, memory, disk, image, network, and SSH
- Lifecycle:
  - start/stop/restart command construction
  - shape changes require stopped VM
  - remove preserves disk according to existing data-preservation semantics

Live v0 validation target:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet init root@lab-host
CATCH_HOST=yeet-lab go run ./cmd/yeet run devbox vm://ubuntu/26.04 --net=svc --service-root=flash/yeet/vms/devbox --zfs
CATCH_HOST=yeet-lab go run ./cmd/yeet vm console devbox
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh devbox
CATCH_HOST=yeet-lab go run ./cmd/yeet info devbox
```

Acceptance:

- On `yeet-lab`, the VM reaches a usable serial login or guest-ready marker in
  under five seconds from systemd starting Firecracker.
- `yeet ssh devbox` connects without manual key setup once the guest network is
  ready.
- `--net=svc` VMs can talk to each other by IP.
- `--net=svc` VMs and existing `svc` services can talk by IP if the shared
  network implementation is practical.
- `--net=lan` VMs are reachable directly from another LAN machine.
- `yeet-cloud` reports missing KVM clearly and does not create a partial VM.

## Documentation

When implementation lands, update README and website docs with:

- `yeet run <name> vm://ubuntu/26.04`
- `--net=svc` and `--net=lan` examples
- `--cpus`, `--memory`, and `--disk` examples
- ZFS-backed VM root examples
- `yeet vm console`
- `yeet ssh` behavior for VMs
- KVM host requirement
- experimental status

## Open Implementation Notes

- The preferred `svc` design is one shared yeet network for services and VMs.
  If Firecracker TAP integration with the existing `yeet-ns` model proves
  brittle, document the reason before falling back to a separate VM bridge.
- The image builder for the yeet Ubuntu rootfs is a separate deliverable. It
  should produce a reproducible rootfs/kernel bundle and publish checksums.
- A future design can add arbitrary user rootfs/kernel support after the owned
  image path proves the lifecycle, network, console, and SSH integration.
