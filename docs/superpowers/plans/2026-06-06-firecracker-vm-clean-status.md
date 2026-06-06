# Firecracker VM Clean Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fresh official Yeet Firecracker VMs boot with no failed systemd units and no `systemctl status` taint, while keeping boot args and image policy aligned with a minimal Firecracker guest.

**Architecture:** Treat this as image policy, not runtime cleanup. The official Ubuntu rootfs builder removes or masks services that are meaningless inside a Yeet Firecracker VM, safely normalizes `/usr/sbin` into `/usr/bin` when the rootfs has only equivalent collisions, and publishes a new image version. Yeet then points `vm://ubuntu/26.04` at the new public image version after live validation.

**Tech Stack:** Go catch-side VM image/version tests, Bash VM image builder scripts, GitHub Actions for public VM image build, live Yeet/catch VM tests on a disposable service.

---

### Task 1: Root Cause Tests

**Files:**
- Create: `tools/vm-image/build_ubuntu_test.go`
- Modify: `pkg/catch/vm_boot_test.go`
- Modify: `pkg/catch/vm_image_test.go`

- [ ] **Step 1: Add static image-policy tests**

Add Go tests that read `tools/vm-image/build-ubuntu-26.04.sh` and assert:
- fast image default version is bumped to `ubuntu-26.04-amd64-v8`
- purge policy includes `fwupd`, `fwupd-signed`, `update-notifier-common`, `update-manager-core`, and `xfsprogs`
- mask policy includes `fwupd-refresh.timer`, `update-notifier-download.timer`, `update-notifier-motd.timer`, `xfs_scrub_all.timer`, and `proc-sys-fs-binfmt_misc.automount`
- the script contains a guarded `merge_usr_sbin_into_usr_bin` helper

- [ ] **Step 2: Add boot arg expectations**

Update boot-arg tests so fast boot args contain Yeet-specific values once and do not include redundant `pci=off`, `root=/dev/vda`, or `rw` in the fast-boot prefix if Firecracker/kernel appends those.

- [ ] **Step 3: Verify tests fail**

Run:

```bash
mise exec -- go test ./tools/vm-image ./pkg/catch -run 'TestFastImage|TestVMKernelBootArgs|TestDefaultVMImageVersion' -count=1
```

Expected: FAIL because v8, purge/mask policy, sbin merge helper, and boot-arg expectations are not implemented yet.

### Task 2: Image Policy Cleanup

**Files:**
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`
- Modify: `tools/vm-image/README.md`

- [ ] **Step 1: Bump image builder default to v8**

Set:

```bash
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v8}"
```

- [ ] **Step 2: Purge Firecracker-irrelevant packages**

Extend the fast profile purge regex with:

```text
fwupd$|fwupd-signed$|update-notifier-common$|update-manager-core$|xfsprogs$
```

- [ ] **Step 3: Mask residual timers/units defensively**

Add masks for:

```text
fwupd.service
fwupd-refresh.service
fwupd-refresh.timer
update-notifier-download.service
update-notifier-download.timer
update-notifier-motd.service
update-notifier-motd.timer
xfs_scrub_all.timer
xfs_scrub_all.service
proc-sys-fs-binfmt_misc.automount
proc-sys-fs-binfmt_misc.mount
```

- [ ] **Step 4: Add safe sbin merge helper**

Implement `merge_usr_sbin_into_usr_bin "$rootfs_mount"` after package cleanup. The helper must:
- no-op when `/usr/sbin` is already a symlink
- iterate with `find` so broken symlinks are handled
- move unique `/usr/sbin/*` entries into `/usr/bin`
- remove duplicate symlinks that already resolve into `/usr/bin`
- replace `/usr/bin/*` compatibility symlinks that resolve into `/usr/sbin` or `/sbin` with the real `/usr/sbin/*` entry
- fail the build on any non-equivalent collision
- replace `/usr/sbin` with `bin` symlink and `/sbin` with `usr/bin`

- [ ] **Step 5: Document v8 policy**

Update VM image README to v8 and mention that fast images remove firmware/update notifier/XFS scrub services and use a merged bin/sbin layout to keep fresh `systemctl status` clean.

- [ ] **Step 6: Verify tests pass for image policy**

Run:

```bash
mise exec -- go test ./tools/vm-image -count=1
```

Expected: PASS.

### Task 3: Boot Argument Hygiene

**Files:**
- Modify: `pkg/catch/vm_boot.go`
- Modify: `pkg/catch/vm_boot_test.go`
- Modify: affected `pkg/catch/*_test.go` fixtures if expectations change

- [ ] **Step 1: Keep legacy images unchanged**

Leave `vmLegacyKernelBootArgs` untouched for stock/non-guest-init images.

- [ ] **Step 2: Narrow fast boot args**

Make `vmKernelBootArgs` pass only the arguments Yeet owns for fast images:

```text
console=ttyS0 reboot=k panic=1 init=/usr/local/lib/yeet-vm/yeet-init ip=... yeet.hostname=... yeet.iface=...
```

Do not include `pci=off root=/dev/vda rw` in fast args because Firecracker/kernel appends those root/MMIO arguments in the running guest.

- [ ] **Step 3: Verify catch tests pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMKernelBootArgs|TestRunVMProvision|TestServiceSetVM' -count=1
```

Expected: PASS.

### Task 4: Public Image Build and Live Validation

**Files:**
- Mirror image script/README changes into `/tmp/yeet-vm-images-check`
- No public docs should include private hostnames or local paths

- [ ] **Step 1: Commit root image/boot changes**

Run targeted tests, stage, commit:

```bash
git add tools/vm-image pkg/catch docs/superpowers/plans/2026-06-06-firecracker-vm-clean-status.md
git commit -m "vm: clean Firecracker guest status"
```

- [ ] **Step 2: Update image repo**

Copy the builder changes into `/tmp/yeet-vm-images-check`, commit, push, and start the VM image workflow for v8.

- [ ] **Step 3: Live test v8 on a disposable VM**

After the image workflow publishes `ubuntu-26.04-amd64-v8`, build/install current Yeet/catch if needed and run a disposable VM. Verify:

```bash
systemctl show -p SystemState -p Tainted -p NFailedUnits
systemctl --failed --no-pager
systemctl status --no-pager | sed -n '1,40p'
cat /proc/cmdline
```

Expected:
- `SystemState=running`
- `Tainted=` or no taint value
- `NFailedUnits=0`
- no failed units
- command line has no duplicated `pci=off root=/dev/vda rw` from Yeet fast args

### Task 5: Yeet v8 Default and Patch Release

**Files:**
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `website/docs/changelog.mdx`

- [ ] **Step 1: Point Yeet at v8**

Set `defaultVMImageVersion = "ubuntu-26.04-amd64-v8"` and update tests.

- [ ] **Step 2: Run quality gates**

Run:

```bash
mise exec -- go test ./pkg/catch ./tools/vm-image -count=1
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 3: Update changelog for next patch**

Add a user-facing v0.5.4 entry that says fresh VMs use the new clean-status image and Yeet now defaults to it. Avoid internal process language.

- [ ] **Step 4: Commit website and root release**

Commit/push website, commit root submodule pointer plus version/default changes, create annotated `v0.5.4`, push main and tag.

- [ ] **Step 5: Verify release**

Check:

```bash
git status --short --branch
git -C website status --short --branch
git ls-remote --tags origin v0.5.4
gh run list --limit 5
```

Expected: clean repos, remote tag exists, release workflow succeeds.

---

## Self-Review

- Spec coverage: failed units, `unmerged-bin`, Firecracker boot args, live validation, image publication, and patch release are covered.
- Placeholder scan: no TBD or unspecified implementation steps remain.
- Type consistency: Go test/package paths and Bash helper names match the implementation targets.
