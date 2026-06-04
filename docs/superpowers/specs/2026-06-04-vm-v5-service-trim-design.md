# Yeet VM v5 Service Trim Design

## Goal

Build the next fast Ubuntu VM image as `ubuntu-26.04-amd64-v5` by trimming
remaining measured boot work from the v4 image while preserving yeet's
long-lived Ubuntu VM model: direct kernel boot, `yeet-init`, systemd-managed SSH
readiness, passwordless sudo for the default user, and normal apt behavior for
non-kernel packages.

The v4 live measurement on `pve1` reached:

- `1.151s` kernel + `2.205s` userspace = `3.356s`
- warmed ZFS VM create: `6.353s`
- warmed ZFS clone: `88ms`
- guest readiness wait: `3.613s`

The next pass should target userspace boot work, not the already-solved ZFS
clone path.

## Reference Point

The exe.dev prototype boots much faster:

- `494ms` kernel + `493ms` userspace = `988ms`
- `multi-user.target` after `334ms`

It is more aggressive than yeet should be right now. It masks many systemd
units and runs a custom application-oriented service path. Yeet should borrow
the obvious Ubuntu image trimming, not replace its VM service model.

## Scope

This is an image-build change first:

- Root repo image scripts and docs under `tools/vm-image/`.
- Mirrored image-builder repo scripts and workflow defaults under
  `/Users/shayne/code/yeet-vm-images`.
- Root catch default image version after the v5 image exists and is verified.
- User-facing docs only where they mention the current fast image version or
  boot profile behavior.

No catch RPC, VM readiness, ZFS clone, SSH host-key, or reboot behavior changes
are planned in this pass.

## v5 Cleanup Set

### Purge Packages

The fast image should purge these packages when present:

- `netplan.io`: yeet injects `systemd-networkd` config; v4 still ran
  `netplan-configure.service`.
- `networkd-dispatcher`: v4 spent about `301ms` there and yeet does not use
  dispatcher hooks.
- `sysstat`: v4 spent about `78ms` in `sysstat.service`.
- `chrony`: v4 spent about `170ms`; use `systemd-timesyncd` for lightweight
  clock sync instead.
- `plymouth` and `plymouth-*`: not useful for Firecracker serial VMs.
- `keyboard-configuration` and `console-setup`: v4 spent about `93ms` in
  `keyboard-setup.service`; yeet VMs do not need console keyboard setup.

Keep `openssh-server`, `dbus`, `systemd-logind`, journald, `systemd-networkd`,
and `systemd-resolved`.

### Enable Lightweight Time Sync

If `systemd-timesyncd` is installed in the cloud image, enable it for the fast
profile. If it is not installed, do not install new packages during image build;
just omit chrony and let time sync be absent rather than making the image build
depend on apt package downloads.

### Mask Residual Units

The fast image should mask these units when present:

- `netplan-configure.service`
- `networkd-dispatcher.service`
- `sysstat.service`
- `sysstat-collect.timer`
- `sysstat-summary.timer`
- `e2scrub_reap.service`
- `e2scrub_all.timer`
- `chrony.service`
- `ldconfig.service`
- `keyboard-setup.service`
- `console-setup.service`
- `plymouth*.service`
- `plymouth*.path`
- `plymouth*.socket`

The image already masks apt timers, `fstrim.timer`, `man-db.timer`, cloud-init,
pollinate, NetworkManager, and networkd wait-online. Keep those masks.

### Deferred Masks

Do not mask `modprobe@.service`, `systemd-modules-load.service`, or udev units
in this pass. They are tempting because exe.dev masks them, but they are lower
confidence for a general Ubuntu VM image. Revisit only after v5 measurements
show they are still material and guest functionality remains correct without
them.

## Versioning

Publish the image as `ubuntu-26.04-amd64-v5`.

After the v5 GitHub image workflow succeeds and live `pve1` validation passes,
change the catch default Ubuntu VM image version from v4 to v5. This avoids
pointing users at an unpublished image.

## Validation

Local checks:

- Shell syntax check for image scripts.
- Targeted Go tests for VM image version/default behavior after the default
  changes.
- `pre-commit run --all-files` before root commits that affect code, tooling,
  or docs.

Image workflow checks:

- Build v5 from a pushed yeet commit.
- Verify manifest image version, no initrd, guest init metadata, kernel IP_PNP,
  manifest checksums, and embedded `yeet-init`.
- Add rootfs verification for the v5 cleanup set where cheap:
  - removed package status for `netplan.io`, `networkd-dispatcher`, `chrony`,
    and `sysstat`;
  - masks for representative units such as `ldconfig.service`,
    `netplan-configure.service`, `keyboard-setup.service`, and
    `e2scrub_reap.service`.

Live `pve1` checks:

- Build local yeet and install catch.
- Create a fresh v5 ZFS/LAN VM with `--disk=128g`.
- Confirm `yeet run` uses `ubuntu-26.04-amd64-v5`.
- Confirm immediate `yeet ssh <svc> -- true`.
- Capture `systemd-analyze`, blame, dmesg tail, and guest service state.
- Verify guest reboot recovery.
- Measure `yeet rm --clean-data`.
- Confirm no leftover test VM datasets or host units.

## Success Criteria

- No regression in VM creation, SSH readiness, reboot recovery, or clean
  removal.
- Guest boot remains under `5s`.
- Userspace improves from the v4 `2.205s` measurement.
- The warmed ZFS create path stays near the v4 result unless guest readiness is
  the only material change.

If v5 does not materially improve userspace, keep the robust cleanup only if it
removes measurable dead weight without hurting behavior; otherwise revert the
questionable part and preserve the measurement.
