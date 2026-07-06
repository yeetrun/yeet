# Yeet VM Image Helpers

This directory contains local development helpers for Yeet VM image work. It is
not the public source of truth for official image releases.

Official Ubuntu and NixOS image bundles are built and published from
[yeetrun/yeet-vm-images](https://github.com/yeetrun/yeet-vm-images). That
repository owns the release workflows, catalog, manifests, root filesystem
builders, Firecracker kernel assets, package-source defaults, and image policy.

Yeet consumes the catalog at:

`https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json`

Each catalog entry points at a stable latest manifest release. Publishing a new
image version updates the immutable release and the matching `*-latest` release;
Yeet does not need a code change for normal official image version bumps.

## Local Helpers

The scripts here are for Yeet development and focused local debugging:

- `build-linux-kernel.sh` builds a Firecracker-ready kernel for local image
  work.
- `build-ubuntu-26.04.sh` builds a local Ubuntu rootfs bundle with the same
  basic boot model Yeet expects.
- `assets/xterm-ghostty.terminfo` carries Ghostty's `xterm-ghostty` terminfo so
  local builds can embed that TERM value for convenience.

Use these helpers when you are changing Yeet's VM boot/provisioning code or need
a local test image. Use `yeetrun/yeet-vm-images` when you are changing official
images, package-source defaults, published image versions, or catalog state.

## Release Shape

Official bundles use the same shape Yeet expects in the image cache:

- `manifest.json`
- `vmlinux`
- `rootfs.ext4.zst`
- `firecracker`
- `kernel.config`
- `checksums.txt`

Imported local bundles can be smaller. See
[VMs](https://yeetrun.com/docs/payloads/vms#bring-your-own-microvm-image) for
the user-facing import format.
