# NixOS MicroVM Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the official `vm://nixos/26.05` image fit yeet's no-initrd, no-loadable-modules Firecracker model and improve first/second boot performance against the current Ubuntu baseline.

**Architecture:** Keep the change image-side in `/Users/shayne/code/yeet-vm-images`. Add an explicit NixOS microVM profile that disables module-loading work through NixOS declarations, add CI verification for the evaluated boot graph, publish a new NixOS image, and compare first boot plus reboot live on `pve1`.

**Tech Stack:** Nix/NixOS 26.05 modules, Bash verification scripts, GitHub Actions, Firecracker, Linux 7.0 yeet kernel, ext4, systemd, yeet/catch live testing on `pve1`.

---

## Repositories

This plan primarily touches:

- `/Users/shayne/code/yeet-vm-images`

It also adds this plan file in:

- `/Users/shayne/code/yeet`

No `catch` or yeet client code changes are planned. If implementation uncovers a required generic VM image contract change, stop and bring that back for design before touching `/Users/shayne/code/yeet` code.

## File Structure

### `/Users/shayne/code/yeet-vm-images`

- Modify: `nixos/yeet-vm.nix`
  - Declare the official NixOS image's no-loadable-modules boot policy through NixOS options.
  - Disable static `modprobe@...` instances that do not apply to the yeet kernel.
  - Preserve standard `systemd-modules-load` behavior so user `boot.kernelModules` settings work.
  - Keep useful interactive/admin packages.
  - Preserve default `nix-command` and `flakes` support.
  - Set `nix.nixPath` so `sudo nixos-rebuild switch` finds `/etc/nixos/configuration.nix` by default.
- Modify: `scripts/build-linux-kernel.sh`
  - Enable and verify `CONFIG_SECCOMP` and `CONFIG_SECCOMP_FILTER` so Nix's build sandbox works inside yeet-managed kernels.
- Create: `scripts/verify-nixos-26.05.sh`
  - Evaluate NixOS options and fail when module-loading behavior returns.
  - Verify OpenSSH metadata, grow-root ordering, and expected yeet services.
  - Verify `nix-command`, `flakes`, and `nixos-config` remain enabled by default.
- Modify: `.github/workflows/build-nixos-26.05.yml`
  - Bump default image version to `nixos-26.05-amd64-v9`.
  - Run `scripts/verify-nixos-26.05.sh` in CI.
- Modify: `scripts/build-nixos-26.05.sh`
  - Bump default image version to `nixos-26.05-amd64-v9`.
- Modify: `README.md`
  - Document that the NixOS image disables module-loading work because the yeet kernel has required features built in.

## Baseline Evidence

The previous live comparison on `pve1` produced:

```text
Ubuntu 26.04 v13:
  yeet run to ready: 7.74s
  first boot dmesg tail: 1.978s
  second boot dmesg tail: 2.005s

NixOS 26.05 v6:
  yeet run to ready: 11.24s
  first boot dmesg tail: 4.306s
  second boot dmesg tail: 2.593s
```

Current evaluated NixOS values before this implementation:

```bash
nix eval --extra-experimental-features 'nix-command flakes' --json .#nixosConfigurations.yeet-nixos-26_05.config.boot.kernelModules
# ["atkbd","loop"]

nix eval --extra-experimental-features 'nix-command flakes' --json .#nixosConfigurations.yeet-nixos-26_05.config.systemd.services.\"modprobe@fuse\".enable
# true
```

The new image should remove avoidable static `modprobe@...` startup work while preserving normal NixOS module-loading behavior and user overrideability.

---

### Task 1: Add A Failing NixOS MicroVM Profile Verifier

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh`

- [ ] **Step 1: Create the verifier script**

Create `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh` with:

```bash
#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

nix_eval_json() {
	local attr="$1"
	nix eval --extra-experimental-features "nix-command flakes" --json ".#nixosConfigurations.yeet-nixos-26_05.config.${attr}"
}

nix_eval_raw() {
	local attr="$1"
	nix eval --extra-experimental-features "nix-command flakes" --raw ".#nixosConfigurations.yeet-nixos-26_05.config.${attr}"
}

assert_json() {
	local attr="$1"
	local jq_filter="$2"
	local message="$3"
	nix_eval_json "$attr" | jq -e "$jq_filter" >/dev/null || {
		echo "$message" >&2
		echo "attr: $attr" >&2
		echo "want: $jq_filter" >&2
		echo "got:" >&2
		nix_eval_json "$attr" >&2
		exit 1
	}
}

assert_raw_equals() {
	local attr="$1"
	local want="$2"
	local got
	got="$(nix_eval_raw "$attr")"
	if [ "$got" != "$want" ]; then
		echo "unexpected $attr: got $got, want $want" >&2
		exit 1
	fi
}

assert_json "nix.settings.experimental-features" 'index("nix-command") != null and index("flakes") != null' "nix-command and flakes must be enabled by default"
assert_json "nix.nixPath" 'index("nixpkgs=flake:nixpkgs") != null and index("nixos-config=/etc/nixos/configuration.nix") != null' "nixos-rebuild must find nixpkgs and /etc/nixos/configuration.nix by default"

for unit in \
	'modprobe@configfs' \
	'modprobe@drm' \
	'modprobe@efi_pstore' \
	'modprobe@fuse'
do
	assert_json "systemd.services.\"${unit}\".enable" '. == false' "$unit must be disabled in the no-module microVM profile"
done

assert_raw_equals "services.openssh.authorizedKeysCommand" "none"
nix_eval_json "services.openssh.authorizedKeysFiles" \
	| jq -e 'index("/etc/yeet-vm/authorized_keys.d/%u") != null' >/dev/null

networkd_metadata_script="$(nix_eval_raw "systemd.services.yeet-networkd-metadata.script")"
if printf '%s\n' "$networkd_metadata_script" | grep -q 'compgen'; then
	echo "yeet-networkd-metadata must not depend on Bash-only compgen" >&2
	exit 1
fi

grow_root_script="$(nix_eval_raw "systemd.services.yeet-grow-root.script")"
printf '%s\n' "$grow_root_script" | grep -q 'resize2fs "$root_source"'
nix_eval_json "systemd.services.yeet-grow-root.before" \
	| jq -e 'index("yeet-guest-ready.service") != null' >/dev/null

for service in \
	"sshd" \
	"systemd-networkd" \
	"systemd-resolved" \
	"yeet-metadata-hostname" \
	"yeet-networkd-metadata" \
	"yeet-grow-root" \
	"yeet-guest-ready"
do
	nix_eval_json "systemd.services.${service}.enable" | jq -e '. == true' >/dev/null || {
		echo "expected service ${service} to be enabled" >&2
		exit 1
	}
done

override_probe="$(
	nix eval --impure --extra-experimental-features "nix-command flakes" --json --expr '
let
  flake = builtins.getFlake "path:'"$repo_root"'";
  modprobeUnits = [
    "modprobe@configfs"
    "modprobe@drm"
    "modprobe@efi_pstore"
    "modprobe@fuse"
  ];
  cfg = (flake.nixosConfigurations.yeet-nixos-26_05.extendModules {
    modules = [
      ({ ... }: {
        boot.kernelModules = [ "dummy" ];
        systemd.services = builtins.listToAttrs (map
          (name: {
            inherit name;
            value = {
              enable = true;
              wantedBy = [ "sysinit.target" ];
            };
          })
          modprobeUnits);
      })
    ];
  }).config;
in {
  bootKernelModules = cfg.boot.kernelModules;
  systemdModulesLoadEnable = cfg.systemd.services.systemd-modules-load.enable;
  systemdModulesLoadWantedBy = cfg.systemd.services.systemd-modules-load.wantedBy;
  modprobeUnits = builtins.listToAttrs (map
    (name:
      let
        service = builtins.getAttr name cfg.systemd.services;
      in
      {
        inherit name;
        value = {
          enable = service.enable;
          wantedBy = service.wantedBy;
        };
      })
    modprobeUnits);
}
'
)"
printf '%s\n' "$override_probe" | jq -e '
  (.bootKernelModules | index("dummy") != null) and
  .systemdModulesLoadEnable == true and
  (.systemdModulesLoadWantedBy | index("multi-user.target") != null) and
  (.modprobeUnits | length == 4) and
  ([.modprobeUnits[] | select(.enable == true and (.wantedBy | index("sysinit.target") != null))] | length == 4)
' >/dev/null || {
	echo "NixOS yeet microVM defaults must remain overrideable by user configuration" >&2
	printf '%s\n' "$override_probe" >&2
	exit 1
}

echo "NixOS 26.05 yeet microVM profile verified"
```

- [ ] **Step 2: Make the verifier executable**

Run:

```bash
chmod +x scripts/verify-nixos-26.05.sh
```

- [ ] **Step 3: Run the verifier and confirm it fails on current `main`**

Run:

```bash
scripts/verify-nixos-26.05.sh
```

Expected: FAIL before implementation. The failure should mention one of the `modprobe@...` services.

- [ ] **Step 4: Commit nothing**

Do not commit the failing verifier by itself. Continue to Task 2 so the verifier and image change land together.

---

### Task 2: Declare The No-Module NixOS MicroVM Profile

**Files:**
- Modify: `/Users/shayne/code/yeet-vm-images/nixos/yeet-vm.nix`
- Test: `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh`

- [ ] **Step 1: Add disabled modprobe service definitions**

In `nixos/yeet-vm.nix`, inside the existing `let` block after `guestReady`, add:

```nix
  disabledModprobeServices =
    lib.genAttrs [
      "modprobe@configfs"
      "modprobe@drm"
      "modprobe@efi_pstore"
      "modprobe@fuse"
    ] (_: {
      enable = lib.mkDefault false;
    });
```

- [ ] **Step 2: Keep the `boot` declaration overrideable**

Leave the existing `boot` block as:

```nix
  boot = {
    initrd.enable = false;
    loader.grub.enable = false;
    loader.systemd-boot.enable = false;
    tmp.cleanOnBoot = true;
  };
```

Do not hard-force `boot.kernelModules`, `boot.initrd.kernelModules`,
`boot.initrd.availableKernelModules`, or `boot.extraModulePackages` to empty.
The yeet kernel itself is verified as no-loadable-modules by the workflow
kernel config checks, while users must remain able to customize NixOS options
and run `sudo nixos-rebuild switch`.

- [ ] **Step 3: Add module-load service policy**

Inside `systemd.services`, change:

```nix
    services = {
      yeet-metadata-hostname = {
```

to:

```nix
    services = disabledModprobeServices // {
      yeet-metadata-hostname = {
```

- [ ] **Step 4: Format the Nix file**

Run:

```bash
nix --extra-experimental-features "nix-command flakes" run nixpkgs#nixpkgs-fmt -- nixos/yeet-vm.nix
```

Expected: command exits 0 and only formatting changes are applied.

- [ ] **Step 5: Run the verifier**

Run:

```bash
scripts/verify-nixos-26.05.sh
```

Expected: PASS and prints:

```text
NixOS 26.05 yeet microVM profile verified
```

- [ ] **Step 6: Run targeted Nix eval checks**

Run:

```bash
nix eval --extra-experimental-features 'nix-command flakes' --json .#nixosConfigurations.yeet-nixos-26_05.config.boot.kernelModules
nix eval --extra-experimental-features 'nix-command flakes' --json .#nixosConfigurations.yeet-nixos-26_05.config.systemd.services.\"modprobe@fuse\".enable
nix eval --extra-experimental-features 'nix-command flakes' --json .#nixosConfigurations.yeet-nixos-26_05.config.nix.settings.experimental-features
```

Expected output:

```json
["atkbd","loop"]
false
["nix-command","flakes"]
```

- [ ] **Step 7: Commit the no-module profile**

Run:

```bash
git add nixos/yeet-vm.nix scripts/verify-nixos-26.05.sh
git commit -m "image: disable NixOS module loading"
```

Expected: commit succeeds. If hooks run, they pass.

---

### Task 3: Wire The Verifier Into CI And Bump The NixOS Image Version

**Files:**
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-nixos-26.05.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`

- [ ] **Step 1: Bump the NixOS builder default version**

In `scripts/build-nixos-26.05.sh`, change:

```bash
version="${YEET_VM_IMAGE_VERSION:-nixos-26.05-amd64-v6}"
```

to:

```bash
version="${YEET_VM_IMAGE_VERSION:-nixos-26.05-amd64-v9}"
```

- [ ] **Step 2: Bump the workflow default version**

In `.github/workflows/build-nixos-26.05.yml`, change:

```yaml
        default: nixos-26.05-amd64-v6
```

to:

```yaml
        default: nixos-26.05-amd64-v9
```

- [ ] **Step 3: Call the verifier in the workflow**

In the `Check Nix definitions` step, after the `statix` command, add:

```yaml
          scripts/verify-nixos-26.05.sh
```

The step should end as:

```yaml
      - name: Check Nix definitions
        run: |
          nix --extra-experimental-features "nix-command flakes" run nixpkgs#deadnix -- --fail .
          nix --extra-experimental-features "nix-command flakes" run nixpkgs#nixpkgs-fmt -- --check .
          nix --extra-experimental-features "nix-command flakes" run nixpkgs#statix -- check .
          scripts/verify-nixos-26.05.sh
```

- [ ] **Step 4: Update README NixOS policy text**

In `README.md`, in the `## NixOS 26.05` section under `The NixOS module:`, ensure the bullet list includes:

```markdown
- disables Firecracker-inapplicable static `modprobe@...` startup units while
  leaving NixOS `systemd-modules-load` available for user-managed
  `boot.kernelModules` settings;
```

Also update any current NixOS version mention from `nixos-26.05-amd64-v6` to:

```text
nixos-26.05-amd64-v9
```

- [ ] **Step 5: Run formatting and static checks**

Run:

```bash
nix --extra-experimental-features "nix-command flakes" run nixpkgs#nixpkgs-fmt -- --check flake.nix nixos/yeet-vm.nix
scripts/verify-nixos-26.05.sh
python3 - <<'PY'
from pathlib import Path
import yaml
for path in [Path(".github/workflows/build-nixos-26.05.yml")]:
    yaml.safe_load(path.read_text())
print("workflow yaml ok")
PY
bash -n scripts/build-nixos-26.05.sh scripts/verify-nixos-26.05.sh
```

Expected:

```text
NixOS 26.05 yeet microVM profile verified
workflow yaml ok
```

All commands exit 0.

- [ ] **Step 6: Commit CI and docs updates**

Run:

```bash
git add .github/workflows/build-nixos-26.05.yml scripts/build-nixos-26.05.sh README.md
git commit -m "ci: verify NixOS microVM profile"
```

Expected: commit succeeds. If hooks run, they pass.

---

### Task 4: Run Local Image Repository Quality Gates

**Files:**
- Verify only

- [ ] **Step 1: Run Nix lint**

Run:

```bash
mise run lint
```

Expected: deadnix, nixpkgs-fmt, and statix checks pass.

- [ ] **Step 2: Run the Nix flake check without building all systems**

Run:

```bash
nix flake check --extra-experimental-features "nix-command flakes" --all-systems --no-build
```

Expected: command exits 0.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: all hooks pass.

- [ ] **Step 4: Confirm image repo status**

Run:

```bash
git status --short --branch
```

Expected: clean working tree on `main`, ahead of `origin/main` by the commits from Tasks 2 and 3.

---

### Task 5: Publish The New NixOS Image From GitHub Actions

**Files:**
- No local file edits

- [ ] **Step 1: Push image repository commits**

Run:

```bash
git push origin main
```

Expected: push succeeds.

- [ ] **Step 2: Dispatch the NixOS image workflow**

Run:

```bash
gh workflow run build-nixos-26.05.yml \
  --repo yeetrun/yeet-vm-images \
  -f version=nixos-26.05-amd64-v9 \
  -f yeet_ref=main \
  -f firecracker_version=v1.14.3 \
  -f kernel_version=7.0 \
  -f kernel_source_url=https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.0.tar.xz \
  -f kernel_source_sha256=bb7f6d80b387c757b7d14bb93028fcb90f793c5c0d367736ee815a100b3891f0 \
  -f kernel_config_url=https://raw.githubusercontent.com/firecracker-microvm/firecracker/86a2559b26a4b9a05405aeaa58bab0f7261d71bc/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config \
  -f zstd_level=10 \
  -f overwrite_release=false \
  -f publish_latest_alias=true \
  -f latest_alias=nixos-26.05-amd64-latest
```

Expected: command exits 0.

- [ ] **Step 3: Identify the workflow run**

Run:

```bash
gh run list --repo yeetrun/yeet-vm-images --workflow build-nixos-26.05.yml --limit 3
```

Expected: the newest run is for `Build NixOS 26.05 VM image` on `main`.

- [ ] **Step 4: Watch the workflow**

Run:

```bash
gh run watch <run-id> --repo yeetrun/yeet-vm-images --exit-status
```

Expected: workflow completes successfully. If it fails, inspect:

```bash
gh run view <run-id> --repo yeetrun/yeet-vm-images --log-failed
```

Fix the failure with a new commit, push, and re-run the workflow using the same `version` only when the failed run did not publish a release. If the failed run published a partial release/tag, re-run with `-f overwrite_release=true` after confirming the partial release is for `nixos-26.05-amd64-v9`.

- [ ] **Step 5: Confirm the release**

Run:

```bash
gh release view nixos-26.05-amd64-v9 --repo yeetrun/yeet-vm-images --json tagName,isDraft,isPrerelease,url,publishedAt
gh release view nixos-26.05-amd64-latest --repo yeetrun/yeet-vm-images --json tagName,isDraft,isPrerelease,url,publishedAt
```

Expected: both releases exist, are not draft, and are not prerelease.

---

### Task 6: Update pve1 Cache And Run First-Boot/Reboot Comparison

**Files:**
- No local file edits

- [ ] **Step 1: Update the NixOS image cache on pve1**

From `/Users/shayne/code/yeet`, run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet vm images update vm://nixos/26.05
```

Expected output includes:

```text
vm://nixos/26.05   current   nixos-26.05-amd64-v9   nixos-26.05-amd64-v9
```

- [ ] **Step 2: Remove stale disposable comparison VMs**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet rm cmp-ubuntu-perf-0608 --clean-data --clean-config --yes >/dev/null 2>&1 || true
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet rm cmp-nixos-perf-0608 --clean-data --clean-config --yes >/dev/null 2>&1 || true
```

Expected: both commands exit 0.

- [ ] **Step 3: Boot fresh Ubuntu and NixOS VMs**

Run:

```bash
/usr/bin/time -p env CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet run cmp-ubuntu-perf-0608 vm://ubuntu/26.04 --net=svc
/usr/bin/time -p env CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet run cmp-nixos-perf-0608 vm://nixos/26.05 --net=svc
```

Expected: both commands reach `Waiting for guest readiness...` and finish with `VM ... is running.` Record each `real` time.

- [ ] **Step 4: Capture first-boot dmesg logs**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-ubuntu-perf-0608 -- sudo dmesg > /tmp/yeet-ubuntu-perf-first-dmesg.txt
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- sudo dmesg > /tmp/yeet-nixos-perf-first-dmesg.txt
```

Expected: both files are non-empty.

- [ ] **Step 5: Record boot IDs**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-ubuntu-perf-0608 -- cat /proc/sys/kernel/random/boot_id > /tmp/yeet-ubuntu-perf-first-boot-id.txt
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- cat /proc/sys/kernel/random/boot_id > /tmp/yeet-nixos-perf-first-boot-id.txt
```

Expected: each file contains one UUID.

- [ ] **Step 6: Reboot both guests**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-ubuntu-perf-0608 -- sudo reboot >/tmp/yeet-ubuntu-perf-reboot.out 2>/tmp/yeet-ubuntu-perf-reboot.err || true
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- sudo reboot >/tmp/yeet-nixos-perf-reboot.out 2>/tmp/yeet-nixos-perf-reboot.err || true
```

Expected: commands return after SSH drops or before SSH drops.

- [ ] **Step 7: Wait for changed boot IDs**

Run:

```bash
bash -lc '
set -euo pipefail
wait_changed() {
  svc="$1"
  old_file="$2"
  old="$(grep -Eo "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" "$old_file" | tail -n 1)"
  for i in $(seq 1 90); do
    out=$(CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh "$svc" -- cat /proc/sys/kernel/random/boot_id 2>/dev/null || true)
    id=$(printf "%s\n" "$out" | grep -Eo "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" | tail -n 1)
    if [ -n "$id" ] && [ "$id" != "$old" ]; then
      printf "%s %s %ss\n" "$svc" "$id" "$((i * 2))"
      return 0
    fi
    sleep 2
  done
  printf "%s did not return with a changed boot id\n" "$svc" >&2
  return 1
}
wait_changed cmp-ubuntu-perf-0608 /tmp/yeet-ubuntu-perf-first-boot-id.txt
wait_changed cmp-nixos-perf-0608 /tmp/yeet-nixos-perf-first-boot-id.txt
'
```

Expected: both services print a new boot ID and elapsed wait time.

- [ ] **Step 8: Capture second-boot dmesg logs**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-ubuntu-perf-0608 -- sudo dmesg > /tmp/yeet-ubuntu-perf-second-dmesg.txt
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- sudo dmesg > /tmp/yeet-nixos-perf-second-dmesg.txt
```

Expected: both files are non-empty.

- [ ] **Step 9: Parse timing milestones**

Run:

```bash
python3 - <<'PY'
from pathlib import Path
import re

files = {
    "ubuntu first": Path("/tmp/yeet-ubuntu-perf-first-dmesg.txt"),
    "ubuntu second": Path("/tmp/yeet-ubuntu-perf-second-dmesg.txt"),
    "nixos first": Path("/tmp/yeet-nixos-perf-first-dmesg.txt"),
    "nixos second": Path("/tmp/yeet-nixos-perf-second-dmesg.txt"),
}
patterns = [
    ("root mount", "EXT4-fs (vda): mounted filesystem"),
    ("yeet-init", "Run /usr/local/lib/yeet-vm/yeet-init"),
    ("systemd first", "systemd[1]:"),
    ("systemd banner", " running in system mode "),
    ("module warning", "Failed to find module"),
    ("modprobe unit", "Starting Load Kernel Module"),
    ("first boot", "Detected first boot"),
    ("preset", "Applying preset policy"),
    ("populated etc", "Populated /etc with preset unit settings"),
    ("journal flush", "Received client request to flush runtime journal"),
]

def ts(line):
    m = re.match(r"\[\s*([0-9]+(?:\.[0-9]+)?)\]", line)
    return float(m.group(1)) if m else None

for label, path in files.items():
    lines = path.read_text(errors="replace").splitlines()
    timestamps = [ts(line) for line in lines if ts(line) is not None]
    print(f"== {label} ==")
    print(f"lines={len(lines)} last_ts={timestamps[-1] if timestamps else 'none'}")
    for name, pattern in patterns:
        hit = next((line for line in lines if pattern in line), None)
        if hit:
            print(f"{name:14} {ts(hit):8.6f} {hit}")
    print()
PY
```

Expected:

- NixOS first boot dmesg tail is improved from the previous 4.306s.
- NixOS second boot is at or below 2.593s.
- NixOS logs do not include `Failed to find module` or `Starting Load Kernel Module` for the disabled module units.

- [ ] **Step 10: Capture system state**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- /run/current-system/sw/bin/systemctl is-system-running
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- /run/current-system/sw/bin/systemctl --failed --no-pager
```

Expected:

```text
running
0 loaded units listed.
```

- [ ] **Step 11: Verify Nix command and flakes defaults**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- /run/current-system/sw/bin/nix show-config > /tmp/yeet-nixos-perf-nix-show-config.txt
grep -E '^experimental-features = .*nix-command.*flakes' /tmp/yeet-nixos-perf-nix-show-config.txt
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- /run/current-system/sw/bin/nix flake --help > /tmp/yeet-nixos-perf-nix-flake-help.txt
```

Expected:

- `grep` prints the `experimental-features` line with both `nix-command` and `flakes`.
- `nix flake --help` exits 0 without requiring `--extra-experimental-features`.

- [ ] **Step 12: Verify users can rebuild immediately**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- sudo nixos-rebuild switch
```

Expected:

- command exits 0;
- output includes a normal NixOS rebuild activation path;
- output does not say `experimental Nix feature 'nix-command' is disabled`;
- output does not say it cannot find `/etc/nixos/configuration.nix`.

- [ ] **Step 13: Confirm rebuild did not break readiness or system health**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- /run/current-system/sw/bin/systemctl is-system-running
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet ssh cmp-nixos-perf-0608 -- /run/current-system/sw/bin/systemctl --failed --no-pager
```

Expected:

```text
running
0 loaded units listed.
```

- [ ] **Step 14: Clean up comparison VMs**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet rm cmp-ubuntu-perf-0608 --clean-data --clean-config --yes
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet rm cmp-nixos-perf-0608 --clean-data --clean-config --yes
ssh root@pve1 'systemctl list-unit-files "yeet-cmp-ubuntu-perf-0608.service" "yeet-cmp-nixos-perf-0608.service" --no-legend --no-pager 2>/dev/null || true; ls -ld /root/data/services/cmp-ubuntu-perf-0608 /root/data/services/cmp-nixos-perf-0608 2>/dev/null || true'
```

Expected: removals exit 0 and the final SSH command prints no leftover unit or service directory.

---

### Task 7: Record Results And Final Status

**Files:**
- No required file edits

- [ ] **Step 1: Confirm image cache state**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet vm images
```

Expected output includes:

```text
vm://nixos/26.05    builtin    current   nixos-26.05-amd64-v9
```

- [ ] **Step 2: Confirm repository cleanliness**

Run:

```bash
git -C /Users/shayne/code/yeet-vm-images status --short --branch
git -C /Users/shayne/code/yeet status --short --branch
```

Expected:

- `yeet-vm-images` is clean and matches `origin/main`.
- `yeet` is clean except for committed local plan/spec commits if they have not been pushed.

- [ ] **Step 3: Summarize evidence**

Prepare a concise final note with:

- image commits pushed;
- workflow run URL and conclusion;
- NixOS release URL;
- pve1 cache version;
- first boot and second boot comparison table;
- module warning status;
- failed-unit status;
- cleanup status.

Do not claim NixOS meets the 2.5s first-boot target unless the parsed dmesg evidence shows it. If it remains above 2.5s, include the measured remaining gap and the likely first-boot activation cause.
