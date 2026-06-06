# Yeet Ubuntu VM Image

The v0 VM payload is `vm://ubuntu/26.04`.

The current fast bundle version is `ubuntu-26.04-amd64-v9`. It is built from
the official Ubuntu 26.04 cloud image, boots a yeet-managed kernel under
Firecracker direct kernel boot, uses `/usr/local/lib/yeet-vm/yeet-init` as the
pre-systemd init shim, and omits `initrd.img`.

Release asset names:

- `manifest.json`
- `vmlinux`
- `rootfs.ext4.zst`
- `firecracker`
- `kernel.config`
- `checksums.txt`

The manifest URL used by catch is:

`https://github.com/yeetrun/yeet-vm-images/releases/latest/download/manifest.json`

## Fast Profile

The default build profile is `fast`. It requires a kernel that already has the
Firecracker boot path built in. The kernel builder pins the Firecracker microVM
config revision used by yeet's no-initrd direct-boot image and enables kernel IP
autoconfiguration for the first VM interface. It also builds in TUN, netfilter,
conntrack, nftables, nft NAT/masquerade, and the nft compatibility support
needed by Ubuntu's `iptables-nft` userspace so guest-installed router software
can run without loadable kernel modules:

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
- keeps `iptables` and `nftables` userspace tools installed for guest-managed
  firewalls and routers. On Ubuntu, the default `iptables` command uses the
  nftables backend;
- writes `/etc/sysctl.d/99-yeet-vm-router.conf` with IPv4 forwarding enabled;
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
- merges `/usr/sbin` into `/usr/bin` after validating collisions so systemd's
  fresh VM status is not tainted by a split bin/sbin layout;
- masks snapd units because the fast image intentionally does not support
  snaps.

The fast profile does not preinstall Tailscale or any other overlay network
agent. Users can install and manage those services inside the VM using normal
Ubuntu packages.

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
