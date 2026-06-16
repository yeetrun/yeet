# Yeet VM Images

Catalog examples include:

- `vm://ubuntu/26.04`
- `vm://nixos/26.05`

This directory contains the shared Firecracker kernel helper and the Ubuntu
rootfs builder used by yeet. The release workflows that publish public bundles
live in `github.com/yeetrun/yeet-vm-images`.

The fast Ubuntu bundle is built from the official Ubuntu 26.04 cloud image,
boots a yeet-managed kernel under Firecracker direct kernel boot, uses
`/usr/local/lib/yeet-vm/yeet-init` as the pre-systemd init shim, and omits
`initrd.img`.

Release asset names:

- `manifest.json`
- `vmlinux`
- `rootfs.ext4.zst`
- `firecracker`
- `kernel.config`
- `checksums.txt`

Official VM image families are discovered from:

`https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json`

Each catalog entry points at a stable latest manifest release. Publishing a new
image version updates the immutable release and the matching `*-latest` release;
`yeet` does not need a code change for version bumps.

## Ubuntu Fast Profile

The default build profile is `fast`. It requires a kernel that already has the
Firecracker boot path built in. The kernel builder pins the Firecracker microVM
config revision used by yeet's no-initrd direct-boot image and enables kernel IP
autoconfiguration for the first VM interface. It also builds in TUN, IPv6,
netfilter, conntrack, conntrack marks, nftables, nft NAT/masquerade, and the
nft compatibility support needed by Ubuntu's `iptables-nft` userspace so
guest-installed router software can run without loadable kernel modules:

```bash
tools/vm-image/build-linux-kernel.sh dist/kernel-linux-7.0
mise run guest:init:build
sudo YEET_VM_KERNEL_PATH="$PWD/dist/kernel-linux-7.0/vmlinux" \
  YEET_VM_KERNEL_VERSION=linux-7.0-yeet \
  YEET_VM_INIT_PATH="$PWD/guest/yeet-init/target/x86_64-unknown-linux-musl/release/yeet-init" \
  tools/vm-image/build-ubuntu-26.04.sh
```

The fast profile customizes the Ubuntu rootfs before compression:

- purges Ubuntu kernel, module, header, bootloader, initramfs, and snap
  packages;
- writes `/etc/apt/preferences.d/99-yeet-managed-kernel` to keep those packages
  from returning during guest apt upgrades;
- writes `/usr/share/doc/yeet-vm-image/kernel.md` explaining that the boot
  kernel is supplied by the yeet VM image bundle and that nftables-oriented
  router kernel features are built in rather than loaded as modules;
- writes `/usr/share/doc/yeet-vm-image/init.md` explaining the pre-systemd
  `yeet-init` path and readiness flow;
- installs the Rust `yeet-init` binary into `/usr/local/lib/yeet-vm/yeet-init`;
- compiles Ghostty's `xterm-ghostty` terminfo into `/etc/terminfo` so terminal
  applications recognize that TERM value out of the box;
- keeps `iptables`, `nftables`, and `rsync` userspace tools installed for
  guest-managed firewalls, routers, and `yeet copy` guest file sync. On Ubuntu,
  the default `iptables` command uses the nftables backend;
- writes `/etc/sysctl.d/99-yeet-vm-router.conf` with IPv4 and IPv6 forwarding
  enabled;
- writes `/etc/tmpfiles.d/yeet-vm-tun.conf` so `/dev/net/tun` is present for
  guest-managed tunneling software;
- enables kernel IP autoconfiguration for the first VM interface;
- uses systemd-networkd and `yeet-sshd.service` instead of netplan and the
  stock `ssh.service` for VM readiness;
- purges cloud-init, pollinate, fwupd, update-notifier, xfsprogs, netplan,
  networkd-dispatcher, chrony, sysstat, plymouth, console keyboard setup, and
  other server-image services that do not contribute to yeet VM boot;
- masks residual boot units for netplan, networkd-dispatcher, sysstat,
  e2scrub, XFS scrub, fwupd refresh, update notifier, binfmt_misc, ldconfig,
  keyboard setup, plymouth, module loading, and background maintenance timers;
- preserves Ubuntu package-owned filesystem paths such as `/usr/sbin` so normal
  Ubuntu packages and alternatives keep working inside yeet VMs;
- normalizes the root filesystem to a conservative ext4 feature set so common
  LTS host tooling can check, resize, and mount VM disks during provisioning;
- masks snapd units because the fast image intentionally does not support
  snaps.

The fast profile does not preinstall Tailscale or any other overlay network
agent. Users can install and manage those services inside the VM using normal
Ubuntu packages.

## NixOS Profile

The NixOS image is built from NixOS configuration, not from rootfs patching.
The image declares the yeet default user, SSH configuration, networkd units,
readiness service, shell defaults, and `/etc/yeet-vm` metadata consumers in
NixOS modules. Yeet writes only data under `/etc/yeet-vm` for NixOS guests:
hostname, authorized keys, and generated network files.

NixOS guests use the same yeet-managed Firecracker kernel and init shim boot
model as Ubuntu, with `yeet.system_init=/run/current-system/init` passed to
`yeet-init`. Guest package and service changes should be made with normal
NixOS configuration and `nixos-rebuild`.

## Stock Profile

For debugging or reproducing the old v1-style image, use the stock profile:

```bash
YEET_VM_IMAGE_PROFILE=stock \
  YEET_VM_IMAGE_VERSION=ubuntu-26.04-amd64-v1 \
  tools/vm-image/build-ubuntu-26.04.sh
```

The stock profile extracts Ubuntu's generic kernel from the cloud image and
includes `initrd.img`. It does not apply the yeet-managed kernel or no-snap
rootfs policy.
