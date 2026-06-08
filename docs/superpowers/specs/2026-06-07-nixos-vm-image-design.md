# NixOS VM Image Design

## Summary

Add a second official yeet VM image for NixOS 26.05:

```bash
yeet run devbox vm://nixos/26.05
```

The image should be built and published by `yeet-vm-images` with the same
bundle contract as Ubuntu: `manifest.json`, `vmlinux`, `rootfs.ext4.zst`,
`firecracker`, `kernel.config`, and `checksums.txt`.

This is not only an image-builder change. The current `yeet` VM image code has
one Ubuntu built-in, one global manifest URL, Ubuntu-specific cache/prune
assumptions, and an Ubuntu default SSH user. The implementation should first
turn official VM images into a small registry, then add NixOS as a second
registry entry.

## Current Evidence

NixOS 26.05 is the current stable release as of 2026-06-07. NixOS announced
26.05 on 2026-05-30, and the public download page reports 26.05 as the current
NixOS version.

Relevant references:

- <https://nixos.org/blog/announcements/2026/nixos-2605/>
- <https://nixos.org/download/>
- <https://nixos.org/manual/nixos/stable/>
- <https://raw.githubusercontent.com/NixOS/nixpkgs/nixos-26.05/nixos/lib/make-disk-image.nix>
- <https://raw.githubusercontent.com/NixOS/nixpkgs/nixos-26.05/nixos/modules/system/activation/top-level.nix>
- <https://github.com/microvm-nix/microvm.nix>

The existing `yeet-vm-images` repo is Ubuntu-specific:

- `README.md` describes only `vm://ubuntu/26.04`.
- `scripts/build-ubuntu-26.04.sh` downloads and customizes an Ubuntu cloud
  image.
- `.github/workflows/build-ubuntu-26.04.yml` publishes one Ubuntu image
  release.

The existing `yeet` repo is also Ubuntu-specific in several places:

- `vm://ubuntu/26.04` is the only built-in payload.
- `defaultVMImageManifestURL` previously used the repository-wide latest
  release,
  `https://github.com/yeetrun/yeet-vm-images/releases/latest/download/manifest.json`.
- Cache inspection picks the highest cached manifest globally, not the highest
  cached manifest for a specific official image family.
- Prune recognizes only `ubuntu-26.04-amd64-vN` versions.
- `yeet vm images` lists and updates only the Ubuntu built-in.
- VM metadata and DB state assume the SSH user is `ubuntu`.

## Goals

- Provide an official NixOS 26.05 VM image optimized for yeet and Firecracker.
- Keep the same image bundle shape and verification model as Ubuntu.
- Keep the yeet-managed Firecracker kernel and Firecracker binary model.
- Use the native NixOS system build model rather than mutating an image in
  Ubuntu-style chroot steps.
- Preserve normal NixOS expectations where possible.
- Keep yeet integration declarative from the NixOS side: NixOS owns users,
  OpenSSH, systemd units, network setup hooks, console login, and grow-root
  units through a repo-owned NixOS module.
- Use `nixos` as the default SSH user for the NixOS image.
- Let users manage the guest as a real NixOS system after login, including
  normal userspace/service rebuilds.
- Keep VM creation, SSH readiness, ZFS cloning, clean removal, image update
  prompts, and image pruning correct with multiple official image families.
- Publish from GitHub Actions and test live on `lab-host` with disposable VMs.

## Non-Goals

- Do not replace the Ubuntu image.
- Do not make NixOS unstable or rolling in this pass.
- Do not depend on Proxmox-specific APIs or bridges.
- Do not make `microvm.nix` the runtime manager. It is a useful reference, but
  yeet already owns Firecracker lifecycle, networking, storage, and metadata.
- Do not preinstall Tailscale or application-specific software in the base
  NixOS image.
- Do not add loadable kernel modules in this pass unless testing proves a
  first-class VM use case cannot work with built-in kernel features.
- Do not make local imported images solve official image publishing. Local
  images remain useful for development and custom user images, but
  `vm://nixos/26.05` should work as an official built-in.

## Recommended Approach

Build a NixOS 26.05 root filesystem from a repo-owned Nix configuration, then
package it with the shared yeet Firecracker kernel and Firecracker binary.

The NixOS image should be produced from declarative Nix files in
`yeet-vm-images`, for example:

```text
nixos/
  flake.nix
  flake.lock
  yeet-vm.nix
scripts/
  build-nixos-26.05.sh
.github/workflows/
  build-nixos-26.05.yml
```

The builder should use NixOS 26.05 inputs, evaluate a minimal x86_64-linux
system, produce a bare ext4 root filesystem suitable for Firecracker's
single-root block device, then compress it as `rootfs.ext4.zst`.

Nixpkgs has two relevant image paths:

- `make-disk-image.nix`, which can produce raw images and supports
  `partitionTableType = "none"` for a bare filesystem image.
- `image.repart`, which is newer and systemd-repart based.

The first implementation should start with `make-disk-image.nix` because it is
closer to yeet's current rootfs contract and supports a bare filesystem image
without introducing a partition table or bootloader.

## Image Configuration

The NixOS image should be a normal, minimal NixOS system configured for yeet's
guest contract.

Required properties:

- Architecture: `x86_64-linux`.
- Root filesystem: ext4 on Firecracker root block device.
- Default user: `nixos`.
- SSH: OpenSSH server available and compatible with yeet metadata injection.
- Networking: systemd-networkd enabled, DHCP usable on guest interfaces, static
  networkd unit injection usable for `svc` networking.
- Shell: bash available for the login user.
- Sudo: passwordless sudo for the injected default user after metadata
  injection.
- Tools required by yeet guest scripts: `ip`, `ss`, `ssh-keygen`, `resize2fs`,
  `findmnt`, `logger`, `tic`, and `systemd`.
- Router-friendly userspace: `nftables`, `iptables` with nft backend where
  NixOS exposes it, and TUN device support.
- Ghostty terminfo installed so `xterm-ghostty` works out of the box.
- Nix and NixOS management tools available, including `nix`, `nixos-rebuild`,
  and a readable base configuration under `/etc/nixos`.
- No distro kernel, bootloader, or initrd is used by the yeet boot path.

The image should not rely on cloud-init. For NixOS, yeet should inject data
only, not Ubuntu-style system files. The NixOS image module should consume
yeet metadata from `/etc/yeet-vm/` and own the services that apply it.

It is acceptable for the NixOS closure to contain normal NixOS kernel-related
store paths if NixOS requires them for evaluation or tooling. The contract is
that Firecracker boots the `vmlinux` from the yeet image manifest, not a guest
bootloader or guest-installed kernel.

## NixOS Guest Contract

After `yeet ssh`, the VM should feel like a small NixOS host, not a generic
Linux rootfs with a Nix store copied into it and not an FHS compatibility layer
bolted on for yeet.

The base image should include:

- `/etc/nixos/configuration.nix` or an equivalent repo-owned flake
  configuration that users can inspect and modify;
- enough nixpkgs/channel/flake setup for a normal user to run a documented
  rebuild command without first reverse-engineering the image;
- `nix-command` and flakes enabled if the image uses a flake-first
  configuration;
- documentation under `/usr/share/doc/yeet-vm-image/` explaining the
  yeet-managed kernel and NixOS rebuild behavior.

The base image should also include a small `yeet-vm` NixOS module that:

- enables OpenSSH using NixOS `services.openssh`;
- authorizes yeet SSH keys through a runtime metadata file such as
  `/etc/yeet-vm/authorized_keys`;
- uses NixOS host-key generation semantics and the standard `/etc/ssh`
  host-key paths, with yeet allowed to preseed those state files when it needs
  to preserve host identity across VM replacement;
- configures systemd-networkd through NixOS and imports yeet-provided
  `.network` files from `/etc/yeet-vm/systemd-network/` into
  `/run/systemd/network/` before `systemd-networkd` starts;
- configures serial console autologin for the `nixos` user through NixOS getty
  options;
- declares the ready marker and grow-root units as NixOS `systemd.services`,
  using Nix store paths for tools instead of `/usr/sbin` or `/usr/bin`
  assumptions;
- reads `/etc/yeet-vm/hostname` during boot and applies it without requiring
  the host-side injector to rewrite NixOS-managed files.

The intended NixOS update contract is:

- `nixos-rebuild switch` can change userspace packages, services, systemd
  configuration, users, and NixOS options that do not depend on replacing the
  boot kernel.
- Reboot keeps using yeet's manifest kernel and `yeet-init`, then chains into
  the current NixOS system init path inside the guest.
- Updating the Firecracker boot kernel happens by updating the official yeet VM
  image, not by installing or selecting a NixOS guest kernel.

If this contract requires a specific documented command, prefer making that
command explicit in the VM docs over silently shipping a NixOS image where
ordinary rebuild expectations fail.

## Init Model

Ubuntu currently uses `yeet-init` as the kernel `init=` target and `yeet-init`
execs `/usr/lib/systemd/systemd`. That is not the right default for NixOS.

NixOS builds a system closure whose `init` entry performs stage 2 activation
and then starts systemd. Bypassing that path risks skipping NixOS activation
semantics.

Add manifest fields for the next init path and metadata model, for example:

```json
{
  "guest_init": "/usr/local/lib/yeet-vm/yeet-init",
  "guest_system_init": "/run/current-system/init",
  "metadata_driver": "nixos"
}
```

Then update `yeet-init` to:

1. read `yeet.system_init=<path>` from the kernel command line, or use a
   compiled default;
2. perform the current yeet pre-systemd work;
3. exec the selected system init path.

Ubuntu manifests can set `guest_system_init` to `/usr/lib/systemd/systemd`.
NixOS manifests should set it to `/run/current-system/init`.

The Firecracker boot args should include the current `init=` value for
`yeet-init` plus the new `yeet.system_init=` value when the manifest provides
one.

## Manifest Contract

Extend the manifest conservatively:

```json
{
  "name": "yeet-nixos-26.05",
  "version": "nixos-26.05-amd64-v1",
  "architecture": "x86_64",
  "image_profile": "fast",
  "distro": "nixos",
  "distro_version": "26.05",
  "default_user": "nixos",
  "kernel_policy": "yeet-managed",
  "guest_init": "/usr/local/lib/yeet-vm/yeet-init",
  "guest_system_init": "/run/current-system/init",
  "metadata_driver": "nixos",
  "snap_support": false,
  "kernel": "vmlinux",
  "rootfs": "rootfs.ext4.zst",
  "firecracker": "firecracker",
  "rootfs_size": 123,
  "kernel_version": "linux-7.0-yeet",
  "checksums": {}
}
```

Existing fields remain valid. New fields are optional for older images, but
official image builds should include them.

`default_user` removes the current Ubuntu hard-code. Existing Ubuntu images can
continue to work by falling back to `ubuntu` when the field is absent.

`guest_system_init` removes the current assumption that all fast images can exec
`/usr/lib/systemd/systemd`.

`metadata_driver` tells catch which metadata injector to use. Existing Ubuntu
images should keep using the default Ubuntu-compatible systemd/FHS writer.
NixOS images should use a NixOS-specific data-only injector.

## Official Image Registry

Replace the single built-in image constant with a registry of official image
families.

Each registry entry should include:

- Payload ref, for example `vm://ubuntu/26.04`.
- Display name.
- Manifest URL.
- Managed version prefix, for example `ubuntu-26.04-amd64-`.
- Default user fallback.
- Reserved local import name prefix.

Initial entries:

```text
vm://ubuntu/26.04 -> ubuntu-26.04-amd64-latest
vm://nixos/26.05  -> nixos-26.05-amd64-latest
```

This registry should drive:

- payload resolution;
- unsupported payload error messages;
- `yeet vm images` listing;
- `yeet vm images update`;
- cache inspection;
- stale image prompts;
- local import reserved names;
- managed image prune;
- ZFS image-base prune.

## Release URL Model

Do not keep using repo-global GitHub `releases/latest` for official images.
With two image families, the repo-global latest release can point at the wrong
distro.

Use per-family channel releases:

```text
ubuntu-26.04-amd64-latest
nixos-26.05-amd64-latest
```

The manifest URLs become:

```text
https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json
https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json
```

The workflows can still publish immutable version releases such as
`ubuntu-26.04-amd64-v14` and `nixos-26.05-amd64-v1`. They should also update
the per-family channel release with the current assets. This keeps user hosts
checking a stable URL while preserving versioned release history.

## Cache And Prune Behavior

Cache inspection must be scoped by official image family.

When inspecting `vm://nixos/26.05`, Ubuntu cached manifests must be ignored.
When inspecting `vm://ubuntu/26.04`, NixOS cached manifests must be ignored.

Prune should classify managed images by registry family:

- current for each family is the latest cached or ZFS base version for that
  family;
- in-use versions are never removed;
- prunable entries are old, managed, unused versions in a known family;
- local imported images are not pruned by official image prune.

ZFS shared image bases already include the version in the dataset path:

```text
flash/yeet/vm-images/nixos-26.05-amd64-v1/root
```

The parser should accept all registry-managed version prefixes, not only
Ubuntu.

## Metadata And Guest Compatibility

The metadata writer should stop assuming every VM image wants the `ubuntu`
user.

Use `manifest.default_user` when present:

- plan metadata user;
- DB SSH user;
- serial autologin user;
- home directory shell defaults;
- authorized keys;
- sudo access model.

The current metadata writer writes Ubuntu/FHS-style files into the mounted
rootfs. That remains correct for Ubuntu. It should not be the NixOS path.

For NixOS, catch should write only yeet metadata and state files:

- `/etc/yeet-vm/hostname`
- `/etc/yeet-vm/user`
- `/etc/yeet-vm/authorized_keys`
- `/etc/yeet-vm/systemd-network/*.network`
- `/etc/ssh/ssh_host_*` when preserving or preseeding host identity

The NixOS module owns the system interpretation of those files. If a future
distro needs a different model, add another metadata driver instead of growing
path-specific branches in the Ubuntu writer.

## Image Repo Workflow

Add a NixOS workflow mirroring the Ubuntu one:

1. checkout `yeet-vm-images`;
2. checkout `yeet` at `yeet_ref`;
3. install Nix on the GitHub runner;
4. build `guest/yeet-init`;
5. build the shared yeet-managed kernel;
6. build the NixOS rootfs from the pinned Nix flake;
7. normalize ext4 features for host e2fsprogs compatibility;
8. package Firecracker;
9. write manifest and checksums;
10. verify rootfs contents and manifest fields;
11. publish immutable and per-family channel releases.

The README should become multi-image documentation rather than "Yeet Ubuntu VM
Image" documentation. Ubuntu-specific compatibility guidance should stay, but
NixOS should get its own image policy section.

## Validation

Build-time validation should fail before publish if the NixOS image is missing
the yeet guest contract.

Minimum rootfs checks:

- manifest `name`, `version`, `distro`, `distro_version`, `default_user`,
  `guest_init`, and `guest_system_init`;
- no `initrd.img` in the bundle;
- `yeet-init` exists and matches the manifest checksum;
- `guest_system_init` target resolves inside the rootfs;
- default user exists or can be safely injected;
- `nix`, `nixos-rebuild`, and the documented base NixOS configuration are
  present;
- NixOS module declares OpenSSH, metadata import, ready marker, grow-root,
  console autologin, and networking behavior;
- NixOS module uses Nix store package paths for guest services rather than
  requiring `/usr/sbin` or `/usr/bin` compatibility symlinks;
- `sshd`, `ssh-keygen`, `ip`, `ss`, `findmnt`, `resize2fs`, `logger`,
  `systemd`, `bash`, `sudo`, `nft`, and `iptables` are available through the
  NixOS system closure;
- Ghostty terminfo exists;
- `/dev/net/tun` tmpfiles or equivalent exists;
- IPv4 and IPv6 forwarding defaults are configured;
- systemd-networkd is enabled or ready for yeet-injected network units;
- ext4 features are compatible with host tooling used by yeet.

Minimum kernel checks stay shared with Ubuntu:

- modules disabled;
- virtio-mmio, virtio block, virtio net;
- ext4;
- serial console;
- kernel IP autoconfiguration and DHCP;
- TUN;
- IPv6;
- nftables, conntrack, NAT, masquerade, and connmark support.

## Live Test Plan

After publishing a test NixOS image, validate on `lab-host` with disposable VMs.

Raw disk:

```bash
yeet run nixos-smoke@yeet-lab vm://nixos/26.05
yeet ssh nixos-smoke@yeet-lab
yeet rm nixos-smoke --clean-data
```

ZFS and LAN:

```bash
yeet run nixos-zfs@yeet-lab vm://nixos/26.05 \
  --service-root=flash/yeet/vms/nixos-zfs \
  --zfs \
  --disk=128g \
  --net=lan
yeet ssh nixos-zfs@yeet-lab
yeet rm nixos-zfs --clean-data
```

Inside the guest:

```bash
whoami
hostname
uname -a
systemctl --failed --no-pager
systemctl show -p SystemState -p Tainted -p NFailedUnits
nixos-version
nix --version
nixos-rebuild dry-activate
ip addr
ss -ltn
sudo true
infocmp -x xterm-ghostty
nft --version
iptables --version
test -c /dev/net/tun
df -h /
```

Also verify:

- immediate `yeet ssh` works after `yeet run` returns;
- `yeet vm console` works;
- `yeet info` reports `SSH.User = nixos`;
- raw disk resize/grow-root works;
- ZFS clone creation skips base writes after the first VM on the same
  pool/image version;
- `yeet vm images` shows both official images;
- `yeet vm images update` can update the selected image family;
- `yeet vm images prune --dry-run` separates Ubuntu and NixOS versions;
- `yeet rm --clean-data` removes service-owned state without deleting shared
  image bases that are still current or in use.

## User Documentation

Website and CLI docs should show both official images:

```bash
yeet run devbox vm://ubuntu/26.04
yeet run lab vm://nixos/26.05
```

VM docs should explain:

- Ubuntu is the familiar default Linux VM image.
- NixOS is available for users who want a Nix-native guest.
- Both are official yeet images with the same Firecracker runtime and image
  cache behavior.
- Official images use yeet-managed kernels; users update the boot kernel by
  updating the VM image, not by installing guest kernel packages.
- Custom local images remain supported for users who want their own rootfs.

The changelog should be public-facing:

- Added an official NixOS 26.05 VM image at `vm://nixos/26.05`.
- `yeet vm images` now handles multiple official VM image families.
- VM image update and prune behavior now scopes cache state by image family.

Do not describe the feature as "for maintainers before publishing public
images." Public users should see custom images as a way to run their own rootfs.

## Alternatives Considered

### Adapt An Official NixOS Cloud Image

This would mirror the Ubuntu script by downloading a NixOS image and mutating
it. It is less attractive because NixOS is meant to be generated from
declarative system configuration, and post-hoc mutation fights the distro.

### Use microvm.nix As The Image Runtime

`microvm.nix` is a strong reference for NixOS microVMs and Firecracker, but it
would overlap with yeet's runtime responsibilities. Yeet already manages
Firecracker config, networking, storage, metadata, lifecycle, and service DB
state. Pulling in another runtime abstraction would add complexity without
solving the official image contract.

### Ship Only A Local Importable NixOS Bundle

This is useful for development, but it does not meet the product goal. Users
should be able to run `vm://nixos/26.05` without manually importing an image.

## Acceptance Criteria

- `yeet-vm-images` can build and publish `nixos-26.05-amd64-v1` from GitHub
  Actions.
- The NixOS image bundle uses the same artifact and checksum contract as
  Ubuntu.
- `yeet` resolves both `vm://ubuntu/26.04` and `vm://nixos/26.05` as official
  built-ins.
- Official image manifest URLs are family-scoped, not repo-global latest.
- Cache inspection and pruning are family-aware.
- `yeet vm images` lists both official images and local imports.
- New NixOS VMs use SSH user `nixos`.
- `yeet-init` can chain to the NixOS system init path without bypassing NixOS
  activation.
- Raw and ZFS NixOS VMs boot, become SSH-ready, resize the root filesystem, and
  remove cleanly on `lab-host`.
- Fresh NixOS VMs have no yeet-caused failed systemd units.
- Documentation and changelog describe the feature from the public user's
  perspective.
