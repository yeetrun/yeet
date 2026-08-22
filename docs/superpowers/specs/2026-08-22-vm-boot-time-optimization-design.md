# VM Boot-Time Optimization Design

## Summary

Reduce Yeet VM time-to-SSH while retaining Firecracker, the official Ubuntu
and NixOS distribution contracts, and the current security posture. Work from
measured phase boundaries, publish immutable kernel and guest candidates, and
use a disposable KVM canary before moving any stable catalog pointer.

The live baseline shows that Firecracker is not the limiting component. The
current VMM reaches the first kernel log in about 104 ms, while an Ubuntu guest
reaches SSH around 2.6 seconds and reports `yeet-ready` around 2.7 seconds. The
largest confirmed kernel delay is roughly 510 ms in unused i8042/AT keyboard
probing. The remaining time is mainly the systemd path to SSH plus up to 500 ms
of host readiness polling.

## Goals

- Measure VM start, kernel, init, SSH, guest-ready, and multi-user boundaries
  independently instead of treating them as one boot number.
- Reduce warm-boot time to successful SSH to a p50 at or below 2.0 seconds and
  a p95 at or below 2.5 seconds in the first stable improvement wave.
- Continue toward a stretch target of sub-second SSH only when measurements
  justify the additional guest-init and SSH ownership.
- Preserve immediate `yeet ssh <vm> -- true` success after `yeet run` returns.
- Keep Ubuntu package upgrades and NixOS rebuilds functional.
- Preserve kernel security mitigations, Firecracker jailer isolation, guest
  reboot/kernel synchronization, vsock agent behavior, and console recovery.
- Validate raw and ZFS-backed storage plus service and LAN networking on a
  disposable VM-capable canary host.
- Publish immutable candidate artifacts first and promote stable catalog state
  only after recorded live evidence passes every gate.

## Non-Goals

- Do not replace Firecracker with SmolVM or libkrun for boot performance.
- Do not make snapshot restore or prewarmed VM pools the default lifecycle.
- Do not disable CPU vulnerability mitigations, integrity checks, or jailer
  protections for benchmark results.
- Do not optimize application startup such as Docker, Tailscale, or a user's
  workload as part of base guest readiness.
- Do not publish private hostnames, addresses, usernames, or filesystem paths
  in repository scripts, tests, plans, or validation evidence.

## Readiness Contract

The primary product metric is the elapsed host monotonic time from the start of
`yeet-vm-<service>.service` to the first successful public-key SSH command in
the guest. Supporting metrics are:

1. Firecracker process start to first kernel log.
2. First kernel log to `yeet-init` entry.
3. `yeet-init` entry to systemd entry.
4. Systemd entry to port 22 listening.
5. Port 22 listening to `yeet-ready` observation by Catch.
6. `yeet-ready` to `multi-user.target`.
7. Full `yeet run` wall time, reported separately because it may include image
   resolution, download, decompression, provisioning, and disk creation.

Measurements must record p50, p95, minimum, maximum, and every raw sample. A
comparison uses at least 20 warm restarts after one discarded warm-up. First
provision is measured separately. Global host cache dropping is prohibited on
the shared canary.

## Architecture

### Benchmark Harness

Add a generic repository script that accepts the Catch alias, machine SSH
target, service name, iteration count, and output directory through flags or
environment variables. It creates no service implicitly. It restarts only the
explicit disposable VM unit, follows fresh journal output, runs an SSH probe,
captures guest `systemd-analyze` and `dmesg`, and writes neutral JSON evidence.

The script must reject non-disposable service names, require an explicit
machine target, and never call `drop_caches`. Fixture-backed tests cover timing
parsing, percentile calculation, redaction, and incomplete boots.

### Kernel Candidate

Disable `CONFIG_SERIO_I8042` and `CONFIG_KEYBOARD_ATKBD` in the official
Firecracker kernel build and assert both remain disabled after
`olddefconfig`. Build a new immutable packaging revision of the current
upstream kernel. Publish it to the kernel catalog's candidate channel without
moving stable.

Install the exact candidate assets and selector in disposable Ubuntu and NixOS
guests, then use `yeet vm kernel sync <vm> --restart`. This exercises the real
catalog, manifest, checksum, guest selector, host cache, Firecracker config,
and reboot path rather than replacing a `vmlinux` file manually.

### Event-Driven Readiness

Replace the repeated `journalctl` call in the Catch readiness loop with one
cancelable `journalctl --follow` process scoped to the current boot cursor or
timestamp. Race it against the existing vsock agent fallback and return the
first valid report. Preserve the current timeout, configured-interface checks,
stale-marker rejection, and console recovery error.

The change removes 0-500 ms of observation delay and repeated subprocess work.
It does not redefine readiness or claim to make SSH itself start earlier.

### Earlier Systemd SSH

After the kernel and observer changes are measured, shorten only dependencies
shown to be redundant. Ubuntu's root filesystem, SSH keys, accounts, runtime
directory, hostname, and first interface are prepared before systemd. Start by
removing `systemd-sysusers.service` and `network.target` ordering from the
Yeet-owned SSH unit while retaining normal distro `sshd`, PAM, and systemd
supervision.

NixOS must express equivalent ordering through its module rather than rootfs
patches. Each dependency removal is an independent A/B result. Keep a
dependency when its removal saves less than 50 ms p50 or breaks any SSH, PAM,
reboot, networking, rebuild, or package-upgrade test.

### Conditional Udev Experiment

Reprofile after the keyboard-driver removal. Only test masking udev when udev
or device units still contribute at least 100 ms p50 to SSH readiness. The
experiment is candidate-image-only and must prove root disk, vsock, TUN, LAN,
service networking, root growth, reboot, and kernel sync. Any missing device or
hotplug regression rejects the experiment; it is not partially promoted.

### Conditional Pre-Systemd SSH

Only begin this stage when all earlier accepted improvements still leave SSH
p95 above 1.25 seconds. First build a disposable-image spike that starts the
existing distro OpenSSH daemon from `yeet-init` before systemd, matching the
exe.dev readiness ordering while retaining Yeet authentication policy.

The spike is not promotable until it has a supervised ownership and handoff
model with one port-22 listener, clean shutdown/reboot behavior, public-key-only
authentication, working PAM/session semantics or an explicitly reviewed
replacement, and no second independently maintained SSH protocol stack. If
those properties require fragile process adoption, stop at the systemd result
instead of shipping the spike.

## Candidate and Promotion Flow

1. Publish immutable kernel or guest assets.
2. Verify downloaded bytes, checksums, manifest identity, and source commit.
3. Add the exact artifact to the candidate catalog channel only.
4. Install the current Catch candidate on the VM-capable canary when host code
   changes are involved.
5. Exercise disposable Ubuntu and NixOS guests across the required matrix.
6. Compare the candidate against the immediately preceding stable baseline.
7. Reject, retain as candidate for diagnosis, or promote based on gates.
8. After promotion, re-download public catalog and release assets and rerun a
   shorter five-boot smoke test using ordinary stable resolution.

Catalog rollback means selecting the previous immutable identity. Previously
published releases and entries remain available. Disposable test VMs are
removed with data and configuration cleanup after evidence is collected.

## Acceptance Gates

Every promoted wave must satisfy all of these conditions:

- At least 20 measured warm boots with no failed SSH or readiness attempts.
- No p95 regression in Firecracker-to-first-kernel time greater than 10%.
- Candidate SSH p50 improves by at least 50 ms, unless the wave removes a
  correctness problem such as readiness polling latency.
- Ubuntu `apt` metadata refresh and an OpenSSH package reinstall succeed.
- NixOS `nixos-rebuild dry-build --flake /etc/nixos#yeet-vm` succeeds.
- Guest reboot returns to the exact selected kernel and reaches readiness.
- `yeet-agent`, `yeet copy`, console access, `/dev/vsock`, and `/dev/net/tun`
  work.
- Service and LAN networking obtain the expected interface/address behavior.
- Root filesystem growth works on a newly enlarged disposable disk.
- Raw and ZFS-backed disposable VMs both boot and clean up without residue.
- Kernel config retains required Firecracker, ext4, vsock, TUN, netfilter,
  AppArmor, seccomp, cgroup, serial console, and IP autoconfiguration options.
- Guest dmesg shows no new warnings, filesystem errors, kernel panics, or
  security-mitigation regressions attributable to the candidate.

## Rollback

- Kernel: move candidate/stable selection back to the previous immutable
  kernel identity; sync and restart only disposable canaries during iteration.
- Guest base: move the candidate pointer back or discard it; never rewrite a
  release.
- Catch: reinstall the last released Catch build on the canary and confirm
  existing production VMs were not restarted or mutated.
- Live tests: operate only on uniquely named disposable services. Existing
  workload VMs are observation-only throughout this program.

## Authorization

The operator authorized root access to the VM-capable canary, disposable VM
creation and cleanup there, and publication of new immutable kernel artifacts
from `yeet-vm-images`. Stable promotion remains measurement-gated. Repository
content uses neutral host variables; local execution maps them to the
authorized aliases from `AGENTS.local.md`.
