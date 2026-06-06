# VM Image Ubuntu Compatibility Design

## Summary

The fast yeet Ubuntu VM image should remain an optimized Ubuntu userspace, not a
forked filesystem layout. Yeet may provide the Firecracker kernel, pre-systemd
init shim, metadata, readiness flow, package policy, and service masks, but
ordinary Ubuntu packages must still find package-owned paths and alternatives in
the places Ubuntu expects.

The immediate fix is to stop merging `/usr/sbin` into `/usr/bin` in the fast
image builder. That merge was added to remove the systemd `unmerged-bin` taint,
but it changed package-owned paths that Ubuntu's cloud image and dpkg still
expect to live under `/usr/sbin`.

## Investigation Result

The upstream Ubuntu 26.04 cloud image currently has a real `/usr/sbin`
directory. Inspection of `resolute-server-cloudimg-amd64.tar.gz` with `debugfs`
showed these paths under `/usr/sbin`, with no matching `/usr/bin` entries:

- `/usr/sbin/sshd`
- `/usr/sbin/agetty`
- `/usr/sbin/unix_chkpwd`
- `/usr/sbin/iptables-nft`

Yeet's fast image builder added `merge_usr_sbin_into_usr_bin`, which moves
entries from `/usr/sbin` into `/usr/bin`, replaces `/usr/sbin` with a symlink
to `bin`, and rewrites `/sbin` to `usr/bin`. A pristine v11 image can still
resolve `/usr/sbin/sshd` through that symlink, but the layout is fragile for
package ownership, package upgrades, third-party software, and archive-based
migrations that restore files into real `/usr/sbin` paths.

The root bug is not Ubuntu's split layout. The root bug is yeet trying to fix a
cosmetic systemd taint with a custom partial merge that Ubuntu did not perform.

## Goals

- Preserve Ubuntu package-owned filesystem paths in official yeet VM images.
- Keep the fast Firecracker boot profile and the yeet-managed kernel/init
  policy.
- Keep pruning and masking services that are unnecessary or wrong inside a yeet
  Firecracker microVM.
- Add validation so future image changes catch package-layout regressions before
  publishing.
- Add local agent guidance near the image scripts so future agents optimize in
  Ubuntu-compatible ways.
- Keep fresh VMs operationally clean: no failed units and no avoidable boot
  errors caused by yeet pruning or config.

## Non-Goals

- Do not implement a custom usrmerge or partial usrmerge.
- Do not chase a cosmetically empty `systemctl status` at the expense of Ubuntu
  compatibility.
- Do not restore Ubuntu kernel, modules, bootloader, initrd, cloud-init, or snap
  support in this pass.
- Do not add a DNS-appliance profile in this pass, although AdGuard-style
  service needs inform the validation.

## Image Policy

Official yeet Ubuntu VM images must preserve Ubuntu package and filesystem
contracts unless Ubuntu's packaging system performs the change.

Allowed optimizations:

- Purge packages that are unnecessary for the yeet Firecracker guest model.
- Mask or disable services that are not useful or do not work in the microVM.
- Pin kernel, module, bootloader, initramfs, and snap packages when the image
  intentionally does not support those features.
- Add yeet-owned files such as `yeet-init`, metadata units, guest readiness
  units, terminfo, documentation, sysctls, and tmpfiles.
- Configure the yeet-managed kernel with built-in features needed by supported
  guest software.

Disallowed optimizations:

- Relocating package-owned binaries or directories.
- Replacing package-owned directories with symlinks for cosmetic status cleanup.
- Editing dpkg-owned alternatives into paths that packages do not own.
- Treating every systemd taint as a bug before classifying its source.

## Status Cleanliness

Fresh yeet VMs should have no failed systemd units and no avoidable boot errors.
That quality bar remains valid.

Systemd warnings and taints must be classified before changing the image:

- Failed units caused by yeet pruning or missing binaries: fix the unit policy
  or keep the required binary.
- Firecracker-inapplicable services: remove or mask them cleanly.
- Kernel/module warnings from the no-modules policy: avoid them with compatible
  masks or kernel config.
- `unmerged-bin` from the upstream Ubuntu filesystem layout: document or accept
  it unless Ubuntu provides a supported migration path.

The new bar is clean operational state, not forcing every status warning to
disappear by changing distro fundamentals.

## Builder Changes

The fast image builder should:

- Remove `merge_usr_sbin_into_usr_bin` and its call.
- Leave `/usr/sbin` exactly as the source Ubuntu cloud image provides it.
- Leave `/sbin` exactly as the source Ubuntu cloud image provides it unless a
  supported package operation changes it.
- Continue the existing package purge, service mask, yeet-managed kernel,
  `yeet-init`, router-kernel, TUN, sysctl, and Ghostty terminfo work.
- Remove README language that claims yeet merges bin/sbin for clean status.

## Image Validation

Add validation after rootfs customization. The validation should run inside the
mounted rootfs when possible and fail the build on regressions.

Minimum checks:

- `/usr/sbin` is a directory, not a yeet-created symlink.
- `/sbin` still resolves according to the source cloud image layout and is not
  forced to `/usr/bin`.
- `/usr/sbin/sshd`, `/usr/sbin/agetty`, `/usr/sbin/unix_chkpwd`,
  `/usr/sbin/iptables-nft`, and `/usr/sbin/xtables-nft-multi` exist.
- `dpkg -S` reports the expected package ownership for representative
  `/usr/sbin` paths.
- `update-alternatives --display iptables` points to targets that exist.
- `iptables --version` reports the nf_tables backend.
- The router support checks remain covered: TUN device policy, IPv4/IPv6
  forwarding sysctls, nftables userspace, and conntrack mark kernel support.

## Agent Guidance

Add `tools/vm-image/AGENTS.md` with image-specific rules:

- Preserve Ubuntu package and filesystem contracts.
- Optimize boot by removing packages, masking services, adjusting kernel config,
  and changing yeet-owned init/readiness code.
- Do not do cosmetic status cleanup by relocating package-owned files or
  replacing package-owned directories.
- Classify systemd taints before fixing them. Upstream Ubuntu layout warnings
  may be documented or accepted; yeet-caused failed units should be fixed.
- Keep image README, validation, and release notes aligned with any intentional
  image policy change.

## Rollout

Publish the next public image version after the builder and validation changes
land. Then update yeet's default image metadata to the new version and prepare
a patch release.

Live validation should cover:

- Fresh VM create on `pve1`.
- Immediate `yeet ssh`.
- `systemctl --failed --no-pager`.
- `systemctl show -p SystemState -p Tainted -p NFailedUnits`.
- `systemctl status --no-pager` for classification, not blind pass/fail.
- `ssh`, `agetty`, PAM password helper paths, and iptables alternatives.
- `iptables --version` and a CONNMARK rule insertion/deletion.
- Tailscale install/start on a trash VM.
- A normal apt install or reinstall of `openssh-server` or `iptables` to verify
  package operations keep `/usr/sbin` paths valid.
- VM removal with `--clean-data`.

## Release Notes

The changelog should use public, user-facing language:

- Restored Ubuntu-compatible package paths in the official VM image while
  keeping the fast Firecracker boot profile.
- Added image validation to catch filesystem and package-layout regressions
  before publishing VM images.

It should not describe internal process or claim that yeet fixes systemd status
by merging bin/sbin paths.

## Success Criteria

- A fresh official yeet Ubuntu VM preserves Ubuntu's `/usr/sbin` package-owned
  layout.
- VM boot remains fast enough for yeet's microVM goals.
- No failed systemd units are introduced.
- Any remaining `systemctl status` taint is classified and documented rather
  than hidden by custom filesystem reshaping.
- Tailscale and other normal Ubuntu package installs work without path repairs.
- The image builder fails before publish if a future change breaks these
  compatibility checks.
