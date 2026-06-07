# VM Image Agent Instructions

This directory owns yeet VM image builder helpers and documentation.

## Ubuntu Compatibility

- Preserve Ubuntu package and filesystem contracts. Do not relocate
  package-owned files or replace package-owned directories unless Ubuntu's
  packaging system performs that change.
- Do not do cosmetic status cleanup by moving binaries between `/usr/bin`,
  `/usr/sbin`, `/bin`, or `/sbin`.
- Treat `systemctl status` taints as diagnostic signals. Classify the source
  first: yeet-caused failed units should be fixed, while upstream Ubuntu layout
  warnings may be documented or accepted.
- Optimize boot with compatible mechanisms: package removal, service masks,
  kernel config, yeet-owned init/readiness code, metadata, sysctls, and
  tmpfiles.
- Keep `tools/vm-image/README.md`, build validation, and release notes aligned
  with intentional image policy changes.

## NixOS Compatibility

- Build NixOS images the NixOS way: declare users, services, networking,
  filesystem layout, packages, and yeet integration in NixOS modules.
- Do not patch package-owned paths or mutate the rootfs after the Nix build to
  create behavior that should be expressed in Nix configuration.
- Keep yeet's host-side metadata injection data-only for NixOS guests. Yeet may
  write files under `/etc/yeet-vm`; the NixOS module owns how those files are
  consumed by systemd, users, SSH, sudo, and networkd.
- Preserve normal `nixos-rebuild` expectations inside the guest. Optimizations
  should be compatible module options, service masks, package selections,
  kernel/init integration, and yeet-owned metadata/readiness code.
