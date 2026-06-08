# NixOS MicroVM Performance Design

## Summary

Improve the official `vm://nixos/26.05` image so it better fits yeet's
Firecracker microVM model and can meet or exceed the current Ubuntu image boot
behavior.

This pass should stay image-side in `yeet-vm-images`. The NixOS image should
remain a real NixOS system built from NixOS modules and usable with
`nixos-rebuild`. Catch should not grow NixOS-specific provisioning behavior in
this pass.

## Current Evidence

Fresh disposable VMs on `lab-host` showed that the kernel path is already similar
between Ubuntu and NixOS:

| Image | `yeet run` to ready | first boot dmesg tail | second boot dmesg tail |
| --- | ---: | ---: | ---: |
| Ubuntu 26.04 v13 | 7.74s | 1.978s | 2.005s |
| NixOS 26.05 v6 | 11.24s | 4.306s | 2.593s |

Both images mounted root and entered `yeet-init` around `0.75-0.78s`. The
NixOS gap starts after `yeet-init` hands off to `/run/current-system/init`.

NixOS first boot logged:

- `Detected first boot`
- `Applying preset policy`
- `Populated /etc with preset unit settings`

NixOS also attempted module-related work despite the yeet kernel being built
without loadable modules:

- `Failed to find module 'autofs4'`
- `Starting Load Kernel Module configfs`
- `Starting Load Kernel Module drm`
- `Starting Load Kernel Module efi_pstore`
- `Starting Load Kernel Module fuse`

The evaluated NixOS config currently includes default kernel modules such as
`atkbd` and `loop`, and `systemd-modules-load` is wanted by
`multi-user.target`.

## Goals

- Make the NixOS image fit yeet's no-initrd, no-loadable-modules Firecracker
  guest model.
- Preserve normal NixOS configuration, package management, and
  `nixos-rebuild` expectations.
- Keep `nix-command` and `flakes` enabled by default so users can inspect and
  rebuild the VM with modern Nix workflows immediately after SSH.
- Keep useful interactive/admin tools such as `htop`, `vim`, `jq`, `curl`,
  `git`, and `wget`; they are not the likely boot bottleneck.
- Reduce first-boot work where NixOS provides a clean, supported way to do so.
- Remove avoidable dmesg noise caused by the image asking for kernel modules
  that cannot exist.
- Add build-time assertions so later image changes do not regress the boot
  graph.
- Compare the resulting image live against Ubuntu using first boot and
  post-reboot measurements.

## Non-Goals

- Do not add NixOS-specific catch provisioning in this pass.
- Do not write fixed per-machine identity into the reusable public image.
- Do not patch package-owned or Nix-store files in the rootfs.
- Do not remove useful interactive packages merely to shrink closure size.
- Do not replace NixOS activation with a custom appliance init path.
- Do not add loadable kernel modules unless a separate capability decision
  changes yeet's kernel policy.

## Design Constraints

The image repository's NixOS policy remains the boundary:

- Use flake-pinned nixpkgs, NixOS modules, and a valid
  `/etc/nixos/configuration.nix`.
- Express boot, networking, users, SSH, packages, and service defaults in the
  NixOS module.
- Keep yeet metadata data-only under `/etc/yeet-vm`.
- Preserve `nixos-rebuild` compatibility.
- Avoid long-running application services in the base image unless they are
  part of the base VM contract.

## Recommended Approach

### 1. Align NixOS With the No-Module Kernel

Declare that the official yeet NixOS image does not use loadable kernel modules
or boot-time module loading. This should be done through NixOS options, not by
editing generated unit files.

The implementation should verify the exact NixOS option set, but the intended
shape is:

- force `boot.kernelModules` and related module lists to empty where safe;
- avoid `systemd-modules-load` being enabled or wanted by normal boot targets;
- prevent default module requests for `autofs4`, `configfs`, `drm`,
  `efi_pstore`, `fuse`, `atkbd`, and `loop` when those are not needed by the
  Firecracker guest;
- preserve built-in kernel features that yeet needs for networking, nftables,
  TUN, ext4, virtio block, and virtio net.

This is the highest-confidence improvement because it directly matches the
kernel policy and removes known log noise without changing user-facing NixOS
semantics.

### 2. Investigate First-Boot Activation and Preset Work

Measure the path from `yeet-init` to systemd on first boot and on reboot. If a
clean NixOS-supported build-time option can precompute harmless state, use it.

Allowed directions:

- build the rootfs with NixOS unit enablement state already represented in a
  supported way;
- use NixOS activation or image-building options to avoid repeated or
  first-boot-only preset population;
- document any remaining first-boot work that is inherent to a generic,
  cloneable NixOS image.

Disallowed directions:

- fixed `/etc/machine-id` in the public image;
- catch-side branches such as "if image is NixOS then mutate these distro
  internals";
- rootfs surgery that bypasses NixOS module ownership.

If first-boot state still dominates after image-side work, a later design can
consider a generic manifest-declared provisioning primitive for per-VM identity.
That would need to be OS-neutral and opt-in by image contract, not a NixOS
special case.

### 3. Keep the Base Closure Practical

Do not chase package minimalism in this pass. The current package set is useful
for yeet's target users and is not supported by the dmesg evidence as the main
boot penalty.

Keep:

- `bashInteractive`
- `coreutils`
- `curl`
- `file`
- `gitMinimal`
- `htop`
- `iproute2`
- `iptables`
- `jq`
- `nftables`
- `openssh`
- `procps`
- `sudo`
- `vim`
- `wget`
- Ghostty terminfo

Only remove or replace a package if measurement shows it materially affects
boot or breaks the microVM model.

### 4. Tighten Yeet-Owned Services

Keep yeet services explicit and small:

- hostname metadata;
- networkd metadata;
- root filesystem growth;
- guest readiness.

Optimize these units only where it is clearly safe. For example, avoid adding
new ordering dependencies unless they are required for correctness, and keep
readiness tied to actual SSH/listening IP state.

## Build Validation

Add CI checks in `yeet-vm-images` that fail before publishing if the image
drifts away from the microVM profile.

Minimum checks:

- the evaluated NixOS config has no unwanted boot kernel modules;
- `systemd-modules-load` is not enabled or wanted by the default boot graph;
- OpenSSH still uses `/etc/yeet-vm/authorized_keys.d/%u`;
- yeet network metadata still avoids Bash-only assumptions;
- grow-root still runs before guest readiness;
- the kernel config still has no loadable module support and still includes the
  required built-in networking/router features.

Add a lightweight static check that lists the expected enabled NixOS services
for the base image. If the evaluated service graph cannot be checked reliably,
the implementation plan must explain why and choose a narrower assertion that
still catches module-loading regressions.

## Live Validation

Publish a new NixOS image version and test on `lab-host` with disposable VMs.

For each candidate image:

1. Create a fresh VM with default `svc` networking.
2. Capture first-boot `dmesg`.
3. Capture `systemd-analyze` output if available and useful.
4. Capture `systemctl is-system-running`.
5. Reboot with `sudo reboot`.
6. Verify the boot ID changed.
7. Capture second-boot `dmesg`.
8. Compare against Ubuntu using the same workflow.
9. Remove the VMs with `--clean-data --clean-config`.

The comparison should include:

- `yeet run` wall time to readiness;
- time to root mount;
- time to `yeet-init`;
- time to first systemd log;
- time to systemd banner;
- dmesg tail timestamp;
- warnings/errors related to module loading, first boot, filesystems, or
  Firecracker devices.

## Success Criteria

- NixOS first boot dmesg tail improves from the current 4.306s toward Ubuntu's
  roughly 2.0s baseline. The target is 2.5s or better; if the final result is
  above 2.5s, the remaining gap must be explained with measured evidence.
- NixOS second boot matches or beats the current NixOS v6 second boot of
  2.593s.
- Avoidable module-load dmesg noise is removed.
- No failed units are introduced.
- `nixos-rebuild` remains a supported expectation.
- `sudo nixos-rebuild switch` works in a fresh VM without additional setup.
- `nix-command` and `flakes` are enabled in a fresh VM without extra flags.
- Interactive/admin tools remain available.
- The image build fails if future changes reintroduce module-loading behavior
  incompatible with the yeet kernel.

## Rollout

The rollout should stay in `yeet-vm-images` unless implementation uncovers a
separate, clearly generic yeet VM image contract issue.

Expected rollout:

- commit NixOS module and workflow validation changes;
- publish the next `nixos-26.05-amd64-vN` image from GitHub Actions;
- update lab-host's cached image;
- run the live first-boot/reboot comparison;
- document measured results in the work log and release notes;
- avoid a yeet client/catch release unless no-image code changes become
  necessary.
