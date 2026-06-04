# Yeet VM v5 Service Trim Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish and adopt `ubuntu-26.04-amd64-v5` with a more aggressively trimmed fast Ubuntu rootfs that removes measured v4 boot overhead while preserving yeet VM behavior.

**Architecture:** Keep this pass image-first. Update the root and image-repo build scripts to purge/mask only high-confidence residual Ubuntu services, harden workflow verification for the cleanup set, publish v5, live-test it on `pve1`, then bump the catch default/docs to v5 after the image exists.

**Tech Stack:** Bash, Go, GitHub Actions, debugfs, systemd, Firecracker, ZFS live validation.

---

## Current Measurements

- v4 guest boot on `pve1`: `1.151s` kernel + `2.205s` userspace = `3.356s`.
- v4 warmed ZFS create: `6.353s` wall, `88ms` disk clone, `3.613s` guest readiness wait.
- v4 top remaining userspace entries included `networkd-dispatcher.service`, `chrony.service`, `ldconfig.service`, `keyboard-setup.service`, `netplan-configure.service`, `sysstat.service`, and `e2scrub_reap.service`.
- exe.dev reference keeps only a very small running set and either removes or masks several of those units, but also masks lower-confidence udev/module units that this pass intentionally defers.

## File Structure

### Root repo: `/Users/shayne/code/yeet`

- Modify `tools/vm-image/build-ubuntu-26.04.sh`: default v5 image version; purge and mask high-confidence residual services; enable `systemd-timesyncd` if already installed.
- Modify `tools/vm-image/README.md`: update current fast bundle version and list v5 trim behavior.
- Later modify `pkg/catch/vm_image.go`: change `defaultVMImageVersion` to v5 after the release is verified.
- Later modify root VM image tests under `pkg/catch/`: update default-version assertions and default-image fixtures from v4 to v5.
- Later modify user-facing docs if they mention the current fast bundle version.

### Image builder repo: `/Users/shayne/code/yeet-vm-images`

- Modify `scripts/build-ubuntu-26.04.sh`: mirror root script changes.
- Modify `.github/workflows/build-ubuntu-26.04.yml`: default workflow version to v5 and verify rootfs package removal/unit masks.
- Modify `README.md`: mirror fast bundle version and v5 trim behavior.

## Task 1: Update Root v5 Image Build Script

**Files:**
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`
- Modify: `tools/vm-image/README.md`

- [ ] **Step 1: Change the root fast image default version**

In `tools/vm-image/build-ubuntu-26.04.sh`, change the version default:

```bash
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v5}"
```

- [ ] **Step 2: Expand the fast purge package regex**

In the `packages="$(dpkg-query ... awk ...)"` line inside the fast-profile
chroot block, replace the awk pattern with this complete pattern:

```bash
'/^(linux-image-|linux-modules-|linux-modules-extra-|linux-headers-|linux-generic|linux-virtual|grub-|shim-signed$|initramfs-tools|snapd$|snap-confine$|squashfs-tools$|cloud-init$|pollinate$|apport$|apport-symptoms$|modemmanager$|udisks2$|multipath-tools$|lvm2$|rsyslog$|ufw$|unattended-upgrades$|open-vm-tools$|open-vm-tools-desktop$|vgauth$|netplan.io$|networkd-dispatcher$|sysstat$|chrony$|plymouth$|plymouth-|keyboard-configuration$|console-setup$)/ { print }'
```

The resulting line should remain one assignment:

```bash
packages="$(dpkg-query -W -f='${binary:Package}\n' 2>/dev/null | awk '/^(linux-image-|linux-modules-|linux-modules-extra-|linux-headers-|linux-generic|linux-virtual|grub-|shim-signed$|initramfs-tools|snapd$|snap-confine$|squashfs-tools$|cloud-init$|pollinate$|apport$|apport-symptoms$|modemmanager$|udisks2$|multipath-tools$|lvm2$|rsyslog$|ufw$|unattended-upgrades$|open-vm-tools$|open-vm-tools-desktop$|vgauth$|netplan.io$|networkd-dispatcher$|sysstat$|chrony$|plymouth$|plymouth-|keyboard-configuration$|console-setup$)/ { print }')"
```

- [ ] **Step 3: Enable lightweight time sync only when present**

After the existing lines that enable `systemd-networkd.service` and
`systemd-resolved.service`, add:

```bash
if [ -e /usr/lib/systemd/system/systemd-timesyncd.service ]; then
	ln -sf /usr/lib/systemd/system/systemd-timesyncd.service /etc/systemd/system/multi-user.target.wants/systemd-timesyncd.service
fi
```

Do not install `systemd-timesyncd`; this image build should not depend on apt
package downloads.

- [ ] **Step 4: Expand the mask list**

Replace the existing `for unit in \` mask list with:

```bash
for unit in \
	apt-daily.timer \
	apt-daily-upgrade.timer \
	e2scrub_all.timer \
	e2scrub_reap.service \
	fstrim.timer \
	man-db.timer \
	motd-news.timer \
	pollinate.service \
	cloud-init.service \
	cloud-config.service \
	cloud-final.service \
	NetworkManager.service \
	NetworkManager-wait-online.service \
	systemd-networkd-wait-online.service \
	netplan-configure.service \
	networkd-dispatcher.service \
	sysstat.service \
	sysstat-collect.timer \
	sysstat-summary.timer \
	chrony.service \
	ldconfig.service \
	keyboard-setup.service \
	console-setup.service \
	plymouth-start.service \
	plymouth-read-write.service \
	plymouth-quit.service \
	plymouth-quit-wait.service \
	plymouth-halt.service \
	plymouth-kexec.service \
	plymouth-poweroff.service \
	plymouth-reboot.service \
	plymouth-switch-root.service \
	plymouth-switch-root-initramfs.service
do
	ln -sf /dev/null "/etc/systemd/system/$unit"
done
```

Leave `modprobe@.service`, `systemd-modules-load.service`, and udev units
unmasked.

- [ ] **Step 5: Update the root image README**

In `tools/vm-image/README.md`, change the current fast bundle version from v4
to v5:

```md
The current fast bundle version is `ubuntu-26.04-amd64-v5`.
```

In the fast profile bullet list, replace the generic cloud-init/pollinate bullet
with:

```md
- purges cloud-init, pollinate, netplan, networkd-dispatcher, chrony, sysstat,
  plymouth, console keyboard setup, and other server-image services that do not
  contribute to yeet VM boot;
- masks residual boot units for netplan, networkd-dispatcher, sysstat,
  e2scrub, ldconfig, keyboard setup, plymouth, and background maintenance
  timers;
```

- [ ] **Step 6: Validate root script syntax and README diff**

Run:

```bash
bash -n tools/vm-image/build-ubuntu-26.04.sh
git diff --check tools/vm-image/build-ubuntu-26.04.sh tools/vm-image/README.md
```

Expected: both commands exit 0 with no output.

- [ ] **Step 7: Commit root image-script changes**

Run:

```bash
git add tools/vm-image/build-ubuntu-26.04.sh tools/vm-image/README.md
mise exec -- git commit -m "vm-image: trim v5 fast image services"
```

Expected: commit succeeds and hooks pass.

## Task 2: Mirror Script Changes and Harden Image Workflow Verification

**Files:**
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`

- [ ] **Step 1: Mirror the root script into the image repo**

Run:

```bash
cp tools/vm-image/build-ubuntu-26.04.sh /Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh
```

Then compare:

```bash
cmp tools/vm-image/build-ubuntu-26.04.sh /Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh
```

Expected: `cmp` exits 0.

- [ ] **Step 2: Update the image repo README**

In `/Users/shayne/code/yeet-vm-images/README.md`, change the current fast bundle
version and workflow input example from v4 to v5:

```md
The current fast bundle version is `ubuntu-26.04-amd64-v5`.
```

```md
- `version`: release and image version, for example `ubuntu-26.04-amd64-v5`
```

Update the fast profile bullet list with the same two v5 cleanup bullets used
in the root image README:

```md
- purges cloud-init, pollinate, netplan, networkd-dispatcher, chrony, sysstat,
  plymouth, console keyboard setup, and other server-image services that do not
  contribute to yeet VM boot;
- masks residual boot units for netplan, networkd-dispatcher, sysstat,
  e2scrub, ldconfig, keyboard setup, plymouth, and background maintenance
  timers;
```

- [ ] **Step 3: Change workflow default version to v5**

In `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`,
change the workflow dispatch `version.default`:

```yaml
default: ubuntu-26.04-amd64-v5
```

- [ ] **Step 4: Add rootfs verification helpers**

In the `Verify bundle` step, after extracting `rootfs.ext4` and dumping
`yeet-init`, add this block before printing the manifest:

```bash
          debugfs_cat() {
            debugfs -R "cat $1" "$RUNNER_TEMP/rootfs.ext4" 2>/dev/null || true
          }
          assert_unit_masked() {
            local unit="$1"
            local link
            link="$(debugfs -R "stat /etc/systemd/system/$unit" "$RUNNER_TEMP/rootfs.ext4" 2>/dev/null | awk -F': ' '/Fast link dest:/ { print $2; exit }')"
            if [ "$link" != "/dev/null" ]; then
              echo "expected $unit to be masked to /dev/null, got ${link:-missing}" >&2
              exit 1
            fi
          }
          assert_package_not_installed() {
            local package="$1"
            if debugfs_cat "/var/lib/dpkg/status" | awk -v pkg="$package" '
              $1 == "Package:" { package = $2 }
              $1 == "Status:" && package == pkg && $0 ~ /install ok installed/ { found = 1 }
              END { exit found ? 0 : 1 }
            '; then
              echo "expected package $package to be absent from fast image" >&2
              exit 1
            fi
          }
          assert_package_not_installed netplan.io
          assert_package_not_installed networkd-dispatcher
          assert_package_not_installed chrony
          assert_package_not_installed sysstat
          assert_unit_masked netplan-configure.service
          assert_unit_masked networkd-dispatcher.service
          assert_unit_masked sysstat.service
          assert_unit_masked e2scrub_reap.service
          assert_unit_masked ldconfig.service
          assert_unit_masked keyboard-setup.service
```

This intentionally checks representative masks rather than every plymouth unit.

- [ ] **Step 5: Validate workflow and script changes**

Run:

```bash
bash -n /Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh
ruby -e 'require "yaml"; YAML.load_file("/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml"); puts "yaml ok"'
git -C /Users/shayne/code/yeet-vm-images diff --check
```

Expected: shell syntax and diff checks pass, Ruby prints `yaml ok`.

- [ ] **Step 6: Commit image repo changes**

Run:

```bash
git -C /Users/shayne/code/yeet-vm-images add scripts/build-ubuntu-26.04.sh .github/workflows/build-ubuntu-26.04.yml README.md
git -C /Users/shayne/code/yeet-vm-images commit -m "image: trim v5 fast image services"
```

Expected: commit succeeds.

## Task 3: Build and Publish v5 Image

**Files:**
- Read: root and image repo git status
- External: GitHub Actions release `ubuntu-26.04-amd64-v5`

- [ ] **Step 1: Run local verification before pushing**

Run:

```bash
mise run guest:init:test
mise run guest:init:build
mise exec -- go test ./pkg/catch ./cmd/catch -count=1
mise exec -- pre-commit run --all-files
```

Expected: all pass.

- [ ] **Step 2: Push root and image repo commits**

Run:

```bash
git status --short --branch
git push origin main
git -C /Users/shayne/code/yeet-vm-images status --short --branch
git -C /Users/shayne/code/yeet-vm-images push origin main
```

Expected: both pushes succeed.

- [ ] **Step 3: Trigger the v5 workflow**

Run:

```bash
yeet_ref="$(git rev-parse HEAD)"
gh workflow run build-ubuntu-26.04.yml \
  -R yeetrun/yeet-vm-images \
  -f version=ubuntu-26.04-amd64-v5 \
  -f yeet_ref="$yeet_ref" \
  -f overwrite_release=true
```

Then watch the run:

```bash
run_id="$(gh run list -R yeetrun/yeet-vm-images --workflow build-ubuntu-26.04.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch -R yeetrun/yeet-vm-images "$run_id"
```

Expected: workflow succeeds and publishes release `ubuntu-26.04-amd64-v5`.

- [ ] **Step 4: Confirm release assets**

Run:

```bash
gh release view ubuntu-26.04-amd64-v5 -R yeetrun/yeet-vm-images --json tagName,targetCommitish,publishedAt,assets --jq '{tagName,targetCommitish,publishedAt,assets:[.assets[].name]}'
```

Expected: release exists with `manifest.json`, `vmlinux`, `rootfs.ext4.zst`,
`firecracker`, `kernel.config`, and `checksums.txt`.

## Task 4: Live-Test v5 on pve1 Before Adopting It

**Files:**
- Write: `.tmp/vm-v5-measurements.md`

- [ ] **Step 1: Build local yeet and install catch**

Run:

```bash
mise exec -- go build -o /tmp/yeet-vmfast-v5 ./cmd/yeet
mise exec -- /tmp/yeet-vmfast-v5 init root@pve1
```

Expected: catch installs successfully. If ambient Go mismatch appears, the
`mise exec --` prefix is missing.

- [ ] **Step 2: Create a fresh v5 ZFS/LAN VM with tracing**

Run this Python timing wrapper:

```bash
mkdir -p .tmp
python3 - <<'PY'
import os
import pathlib
import subprocess
import sys
import time

svc = "bootv5-" + time.strftime("%H%M%S")
log_path = pathlib.Path(".tmp") / f"{svc}-run.log"
meta_path = pathlib.Path(".tmp") / f"{svc}-run-meta.env"
cmd = [
    "/tmp/yeet-vmfast-v5", "run", f"{svc}@yeet-pve1", "vm://ubuntu/26.04",
    f"--service-root=flash/yeet/vms/{svc}",
    "--zfs",
    "--disk=128g",
    "--net=lan",
    "--image-policy=update",
]
env = os.environ.copy()
env["YEET_TRACE"] = "1"
start = time.monotonic()
first = None
with log_path.open("w", encoding="utf-8") as log:
    log.write("$ " + " ".join(cmd) + "\n")
    log.flush()
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1, env=env)
    assert proc.stdout is not None
    for line in proc.stdout:
        now = time.monotonic()
        if first is None:
            first = now
        sys.stdout.write(line)
        sys.stdout.flush()
        log.write(line)
        log.flush()
    rc = proc.wait()
end = time.monotonic()
with meta_path.open("w", encoding="utf-8") as meta:
    meta.write(f"svc={svc}\n")
    meta.write(f"log={log_path}\n")
    meta.write(f"return_code={rc}\n")
    meta.write(f"first_output_seconds={(first - start) if first is not None else -1:.3f}\n")
    meta.write(f"run_wall_seconds={end - start:.3f}\n")
print(f"svc={svc}")
print(f"first_output_seconds={(first - start) if first is not None else -1:.3f}")
print(f"run_wall_seconds={end - start:.3f}")
print(f"return_code={rc}")
raise SystemExit(rc)
PY
```

Expected:

- Output contains `Image: vm://ubuntu/26.04 (ubuntu-26.04-amd64-v5)`.
- `VM <svc> is running.`
- Trace includes image update/cache, disk provision, systemd install, and guest
  readiness wait durations.

- [ ] **Step 3: Verify immediate SSH and guest boot profile**

Use the service name from `.tmp/bootv5-*-run-meta.env`:

```bash
. .tmp/<svc>-run-meta.env
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- true
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- systemd-analyze | tee ".tmp/${svc}-systemd-analyze.txt"
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- 'systemd-analyze blame | head -n 30' | tee ".tmp/${svc}-systemd-blame.txt"
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- 'dmesg | tail -n 80' | tee ".tmp/${svc}-dmesg-tail.txt"
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- systemctl status yeet-sshd.service yeet-guest-ready.service --no-pager | tee ".tmp/${svc}-guest-services.txt"
```

Expected:

- SSH succeeds immediately.
- `systemd-analyze` is under `5s`.
- Userspace is lower than v4's `2.205s`.
- `yeet-sshd.service` is active/running.
- `yeet-guest-ready.service` is active/exited.

- [ ] **Step 4: Verify the trimmed units inside the guest**

Run:

```bash
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- 'for p in netplan.io networkd-dispatcher chrony sysstat; do dpkg-query -W "$p" >/dev/null 2>&1 && echo "installed:$p" || echo "absent:$p"; done' | tee ".tmp/${svc}-package-trim.txt"
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- 'for u in netplan-configure.service networkd-dispatcher.service sysstat.service e2scrub_reap.service ldconfig.service keyboard-setup.service chrony.service; do printf "%s " "$u"; systemctl is-enabled "$u" 2>/dev/null || true; done' | tee ".tmp/${svc}-unit-trim.txt"
```

Expected:

- Package output shows `absent:` for all four packages.
- Unit output shows `masked` or `not-found` for all listed units.

- [ ] **Step 5: Verify reboot recovery**

Run:

```bash
/tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- sudo reboot || true
sleep 3
ssh root@pve1 "systemctl is-active yeet-vm-$svc.service" | tee ".tmp/${svc}-reboot-unit-active.txt"
start="$(date +%s)"
ok=0
for i in $(seq 1 45); do
  if /tmp/yeet-vmfast-v5 ssh "$svc@yeet-pve1" -- true >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 1
done
end="$(date +%s)"
test "$ok" = 1
ssh root@pve1 "systemctl status yeet-vm-$svc.service --no-pager" | tee ".tmp/${svc}-reboot-host-unit.txt"
echo "reboot_ssh_wait_seconds=$((end-start))" | tee ".tmp/${svc}-reboot-meta.txt"
```

Expected:

- Host unit remains `active`.
- SSH returns within 45 seconds.

- [ ] **Step 6: Measure clean removal**

Run:

```bash
remove_log=".tmp/${svc}-remove.log"
remove_meta=".tmp/${svc}-remove-meta.txt"
start="$(python3 - <<'PY'
import time
print(f"{time.monotonic():.9f}")
PY
)"
expect <<EOF | tee "$remove_log"
set timeout 120
spawn /tmp/yeet-vmfast-v5 rm ${svc}@yeet-pve1 --clean-data
expect "Are you sure you want to remove VM"
send "y\r"
expect "Remove \"${svc}\" from yeet.toml?"
send "y\r"
expect eof
set status [wait]
exit [lindex \$status 3]
EOF
end="$(python3 - <<'PY'
import time
print(f"{time.monotonic():.9f}")
PY
)"
python3 - <<PY | tee "$remove_meta"
start = float("$start")
end = float("$end")
print(f"remove_wall_seconds={end-start:.3f}")
PY
ssh root@pve1 "zfs list -H -o name -r flash/yeet/vms/$svc 2>/dev/null || true" | tee ".tmp/${svc}-zfs-leftovers.txt"
ssh root@pve1 "systemctl list-units --all 'yeet-vm-$svc.service' --no-legend || true" | tee ".tmp/${svc}-unit-leftovers.txt"
```

Expected:

- Removal exits 0.
- Leftover dataset and unit files are empty.

- [ ] **Step 7: Write the v5 measurement report**

Create `.tmp/vm-v5-measurements.md` with:

```md
# VM v5 Live Measurements

Date: 2026-06-04
Host: yeet-pve1 / pve1
Image release: ubuntu-26.04-amd64-v5

## Create

- First output:
- Full `yeet run`:
- Image cache/update:
- Disk provision:
- Guest readiness wait:
- Image used:

## Guest Boot

- `systemd-analyze`:
- Top blame entries:
- Trimmed packages:
- Trimmed units:

## Reboot And Removal

- Reboot host unit:
- SSH return:
- `yeet rm --clean-data`:
- Leftovers:

## Comparison With v4

- v4 userspace: `2.205s`
- v5 userspace:
- v4 warmed create: `6.353s`
- v5 create:
```

Fill every value from the captured `.tmp/${svc}-*.txt` and run log files.

## Task 5: Adopt v5 After Live Validation

**Files:**
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `pkg/catch/vm_images_cmd_test.go`
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `tools/vm-image/README.md` if not already updated
- Modify: `README.md` only if the fast profile wording needs a v5-specific note
- Modify: `website/docs/` only if current-version wording exists there

- [ ] **Step 1: Change the root default image version**

In `pkg/catch/vm_image.go`, change:

```go
defaultVMImageVersion = "ubuntu-26.04-amd64-v5"
```

- [ ] **Step 2: Update default-version tests**

In `pkg/catch/vm_image_test.go`, update:

```go
if defaultVMImageVersion != "ubuntu-26.04-amd64-v5" {
	t.Fatalf("default VM image version = %q, want ubuntu-26.04-amd64-v5", defaultVMImageVersion)
}
```

Update any test fixtures that assert the latest/default fast bundle from v4 to
v5. Leave stale-cache fixtures using older versions unchanged.

- [ ] **Step 3: Run targeted version tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMImage|TestVMImages|TestRunVM.*Image|TestRunVMProvisionSuccessWritesArtifactsAndDB' -count=1
```

Expected: pass.

- [ ] **Step 4: Run final root verification**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -count=1
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
```

Expected: all pass.

- [ ] **Step 5: Commit root v5 adoption**

Run:

```bash
git add pkg/catch/vm_image.go pkg/catch/vm_image_test.go pkg/catch/vm_images_cmd_test.go pkg/catch/vm_provision_test.go README.md website tools/vm-image/README.md
mise exec -- git commit -m "vm: default ubuntu image to v5"
```

If `README.md` or `website` were not modified, omit them from `git add`.

- [ ] **Step 6: Push final root commit**

Run:

```bash
git status --short --branch
git push origin main
```

Expected: root `main` is pushed and clean.

## Task 6: Final Review And Cleanup

**Files:**
- Read: all changed files and measurement report

- [ ] **Step 1: Verify all repos are clean**

Run:

```bash
git status --short --branch
git -C /Users/shayne/code/yeet-vm-images status --short --branch
git -C website status --short --branch
```

Expected: all are clean and on `main...origin/main`.

- [ ] **Step 2: Confirm pve1 cleanup**

Run:

```bash
ssh root@pve1 "zfs list -H -o name -r flash/yeet/vms 2>/dev/null | grep -E 'bootv5' || true; systemctl list-units --all 'yeet-vm-bootv5*.service' --no-legend || true"
```

Expected: no output for test VMs.

- [ ] **Step 3: Dispatch final review**

Ask a reviewer to inspect the root repo, image repo, v5 release, and
`.tmp/vm-v5-measurements.md` for missed regressions. Expected result:
`APPROVED` or concrete findings.

- [ ] **Step 4: Report outcome**

Summarize:

- v5 release URL and workflow run.
- v5 boot and create measurements.
- comparison against v4 and exe.dev.
- tests run.
- any remaining above-the-cut-line boot costs.
