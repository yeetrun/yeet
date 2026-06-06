# VM Image Ubuntu Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore Ubuntu-compatible package-owned filesystem paths in yeet's official fast VM image while preserving the fast Firecracker boot profile.

**Architecture:** Remove the custom `/usr/sbin` merge from the image builder and replace it with rootfs validation that protects Ubuntu package layout. Mirror the canonical script into the public image-builder repository, publish `ubuntu-26.04-amd64-v12`, then update yeet defaults and release a patch after live validation.

**Tech Stack:** Bash image builders, GitHub Actions, Go tests, Firecracker/Ubuntu rootfs validation, yeet/catch live testing on `pve1`, website changelog release flow.

---

## File Structure

Root repo `/Users/shayne/code/yeet`:

- Create `tools/vm-image/AGENTS.md`: local image-policy rules for future agents.
- Modify `tools/vm-image/build_ubuntu_test.go`: string-level regression tests for the Ubuntu-compatible layout policy and v12 default.
- Modify `tools/vm-image/build-ubuntu-26.04.sh`: remove `/usr/sbin` merge, add validation, bump default image version to v12.
- Modify `tools/vm-image/README.md`: remove merged-bin language, describe Ubuntu-compatible package paths, bump current image version to v12.
- Later modify `pkg/catch/vm_image.go`: adopt v12 as the default builtin image after the image release is verified.
- Later modify `pkg/catch/vm_image_test.go`: update default-version assertion to v12.
- Later modify `website/docs/changelog.mdx` in the website submodule for the patch release, then commit the submodule pointer in the root repo.

Image repo `/Users/shayne/code/yeet-vm-images`:

- Create `AGENTS.md`: same image-policy rules for the public image-builder repo.
- Modify `scripts/build-ubuntu-26.04.sh`: mirror the root image builder after tests pass.
- Modify `README.md`: mirror image README language.
- Modify `.github/workflows/build-ubuntu-26.04.yml`: default workflow image version to v12 and invert rootfs verification from merged-bin assertions to Ubuntu-compatible path assertions.

---

### Task 1: Add Image Policy Guardrails In The Root Repo

**Files:**
- Create: `tools/vm-image/AGENTS.md`
- Test: shell readback and private-info scan later through pre-commit

- [ ] **Step 1: Create local image policy instructions**

Create `tools/vm-image/AGENTS.md` with:

```markdown
# VM Image Agent Instructions

This directory owns the yeet Ubuntu VM image builders and documentation.

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
```

- [ ] **Step 2: Verify the local instructions exist**

Run:

```bash
test -s tools/vm-image/AGENTS.md
rg -n "Preserve Ubuntu package and filesystem contracts|Do not do cosmetic status cleanup" tools/vm-image/AGENTS.md
```

Expected: both commands exit 0 and `rg` prints the two policy lines.

- [ ] **Step 3: Commit the guardrails**

Run:

```bash
git add tools/vm-image/AGENTS.md
mise exec -- git commit -m "vm-image: document Ubuntu compatibility policy"
```

Expected: commit succeeds. Pre-commit hooks pass.

---

### Task 2: Add Root Builder Tests For Ubuntu-Compatible Layout

**Files:**
- Modify: `tools/vm-image/build_ubuntu_test.go`
- Test: `go test ./tools/vm-image -count=1`

- [ ] **Step 1: Update the root image test expectations**

Modify `tools/vm-image/build_ubuntu_test.go`.

In `TestFastUbuntuImagePolicyCleansFirecrackerGuestStatus`, change the default
version expectation and remove the merge helper expectation:

```go
for _, want := range []string{
	`version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v12}"`,
	"fwupd$",
	"fwupd-signed$",
	"update-notifier-common$",
	"update-manager-core$",
	"xfsprogs$",
	"fwupd.service",
	"fwupd-refresh.service",
	"fwupd-refresh.timer",
	"update-notifier-download.service",
	"update-notifier-download.timer",
	"update-notifier-motd.service",
	"update-notifier-motd.timer",
	"xfs_scrub_all.service",
	"xfs_scrub_all.timer",
	"proc-sys-fs-binfmt_misc.automount",
	"proc-sys-fs-binfmt_misc.mount",
} {
	if !strings.Contains(script, want) {
		t.Fatalf("build script missing %q", want)
	}
}
```

Replace `TestFastUbuntuImagePolicyGuardsSbinMerge` with:

```go
func TestFastUbuntuImagePolicyPreservesUbuntuSbinLayout(t *testing.T) {
	script := readBuildUbuntuScript(t)

	for _, forbidden := range []string{
		"merge_usr_sbin_into_usr_bin",
		"ln -s bin \"$usr_sbin\"",
		"ln -snf usr/bin \"$root/sbin\"",
		"unmergeable /usr/sbin collision",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("build script still contains custom sbin merge fragment %q", forbidden)
		}
	}

	for _, want := range []string{
		"validate_fast_rootfs_ubuntu_compatibility",
		"/usr/sbin must remain an Ubuntu-owned directory",
		"/sbin must keep Ubuntu cloud image target usr/sbin",
		"/usr/sbin/sshd",
		"/usr/sbin/agetty",
		"/usr/sbin/unix_chkpwd",
		"/usr/sbin/iptables-nft",
		"/usr/sbin/xtables-nft-multi",
		"dpkg -S /usr/sbin/sshd",
		"update-alternatives --display iptables",
		"iptables --version",
		"nf_tables",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("build script missing Ubuntu compatibility validation %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails before implementation**

Run:

```bash
go test ./tools/vm-image -count=1
```

Expected: FAIL. The failure should mention the old v11 default, the remaining
custom sbin merge, or missing `validate_fast_rootfs_ubuntu_compatibility`.

- [ ] **Step 3: Commit nothing**

Do not commit after the failing test. Continue to Task 3 so the implementation
and tests land together.

---

### Task 3: Remove The Root Builder Sbin Merge And Add Validation

**Files:**
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`
- Modify: `tools/vm-image/README.md`
- Test: `go test ./tools/vm-image -count=1`
- Test: `bash -n tools/vm-image/build-ubuntu-26.04.sh`

- [ ] **Step 1: Bump the root builder default to v12**

In `tools/vm-image/build-ubuntu-26.04.sh`, change:

```bash
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v11}"
```

to:

```bash
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v12}"
```

- [ ] **Step 2: Remove the merge helper**

Delete these functions from `tools/vm-image/build-ubuntu-26.04.sh`:

```bash
resolve_guest_path() {
	local root="$1"
	local guest_path="$2"
	chroot "$root" /usr/bin/readlink -f "$guest_path" 2>/dev/null || true
}

same_guest_file() {
	local root="$1"
	local left="$2"
	local right="$3"
	if [ -z "$left" ] || [ -z "$right" ]; then
		return 1
	fi
	if [ "$left" = "$right" ]; then
		return 0
	fi
	cmp -s "$root$left" "$root$right"
}

merge_usr_sbin_into_usr_bin() {
	local root="$1"
	local usr_bin="$root/usr/bin"
	local usr_sbin="$root/usr/sbin"
	if [ -L "$usr_sbin" ] || [ ! -d "$usr_sbin" ]; then
		return
	fi
	install -d -m 0755 "$usr_bin"
	while IFS= read -r src; do
		local name dst src_guest dst_guest src_real dst_real
		name="$(basename "$src")"
		dst="$usr_bin/$name"
		src_guest="/usr/sbin/$name"
		dst_guest="/usr/bin/$name"
		if [ ! -e "$dst" ] && [ ! -L "$dst" ]; then
			mv "$src" "$usr_bin/"
			continue
		fi
		src_real="$(resolve_guest_path "$root" "$src_guest")"
		dst_real="$(resolve_guest_path "$root" "$dst_guest")"
		case "$dst_real" in
		/usr/sbin/* | /sbin/*)
			rm -f "$dst"
			mv "$src" "$usr_bin/"
			;;
		/usr/bin/*)
			if same_guest_file "$root" "$src_real" "$dst_real"; then
				rm -f "$src"
			else
				echo "unmergeable /usr/sbin collision: $src_guest -> ${src_real:-?}, $dst_guest -> ${dst_real:-?}" >&2
				exit 1
			fi
			;;
		*)
			echo "unmergeable /usr/sbin collision: $src_guest -> ${src_real:-?}, $dst_guest -> ${dst_real:-?}" >&2
			exit 1
			;;
		esac
	done < <(find "$usr_sbin" -mindepth 1 -maxdepth 1 -print)
	rmdir "$usr_sbin"
	ln -s bin "$usr_sbin"
	ln -snf usr/bin "$root/sbin"
}
```

Delete the call:

```bash
merge_usr_sbin_into_usr_bin "$rootfs_mount"
```

- [ ] **Step 3: Add Ubuntu compatibility validation**

Insert this function where the deleted helper functions were:

```bash
validate_fast_rootfs_ubuntu_compatibility() {
	local root="$1"

	if [ -L "$root/usr/sbin" ] || [ ! -d "$root/usr/sbin" ]; then
		echo "/usr/sbin must remain an Ubuntu-owned directory" >&2
		exit 1
	fi

	local sbin_target
	sbin_target="$(chroot "$root" /usr/bin/readlink /sbin 2>/dev/null || true)"
	if [ "$sbin_target" != "usr/sbin" ]; then
		echo "/sbin must keep Ubuntu cloud image target usr/sbin, got ${sbin_target:-missing}" >&2
		exit 1
	fi

	for path in \
		/usr/sbin/sshd \
		/usr/sbin/agetty \
		/usr/sbin/unix_chkpwd \
		/usr/sbin/iptables-nft \
		/usr/sbin/xtables-nft-multi
	do
		if [ ! -e "$root$path" ]; then
			echo "missing Ubuntu package-owned path $path" >&2
			exit 1
		fi
	done

	for path in \
		/usr/sbin/sshd \
		/usr/sbin/agetty \
		/usr/sbin/unix_chkpwd \
		/usr/sbin/iptables-nft \
		/usr/sbin/xtables-nft-multi
	do
		if ! chroot "$root" /usr/bin/dpkg -S "$path" >/dev/null; then
			echo "dpkg ownership missing for $path" >&2
			exit 1
		fi
	done

	chroot "$root" /usr/bin/dpkg -S /usr/sbin/sshd >/dev/null
	chroot "$root" /usr/bin/update-alternatives --display iptables >/dev/null

	for path in /usr/sbin/iptables /usr/sbin/iptables-restore /usr/sbin/iptables-save; do
		local target
		target="$(chroot "$root" /usr/bin/readlink -f "$path" 2>/dev/null || true)"
		if [ -z "$target" ] || [ ! -e "$root$target" ]; then
			echo "iptables alternative $path resolves to missing target ${target:-missing}" >&2
			exit 1
		fi
	done

	if ! chroot "$root" /usr/sbin/iptables --version | grep -q 'nf_tables'; then
		echo "iptables must use the nf_tables backend" >&2
		exit 1
	fi
}
```

- [ ] **Step 4: Call validation before unmounting**

Replace the old merge call area with:

```bash
	validate_fast_rootfs_ubuntu_compatibility "$rootfs_mount"
	rm -f "$rootfs_mount/usr/sbin/policy-rc.d"
	cleanup_rootfs_mount
	rootfs_mount=""
}
```

- [ ] **Step 5: Update the root image README**

In `tools/vm-image/README.md`, change the current fast bundle version to v12:

```markdown
The current fast bundle version is `ubuntu-26.04-amd64-v12`.
```

Replace the merged-bin bullet:

```markdown
- merges `/usr/sbin` into `/usr/bin` after validating collisions so systemd's
  fresh VM status is not tainted by a split bin/sbin layout;
```

with:

```markdown
- preserves Ubuntu package-owned filesystem paths such as `/usr/sbin` so normal
  Ubuntu packages and alternatives keep working inside yeet VMs;
```

- [ ] **Step 6: Run root builder checks**

Run:

```bash
bash -n tools/vm-image/build-ubuntu-26.04.sh
go test ./tools/vm-image -count=1
git diff --check -- tools/vm-image
```

Expected: all commands pass.

- [ ] **Step 7: Commit the root builder fix**

Run:

```bash
git add tools/vm-image/build_ubuntu_test.go tools/vm-image/build-ubuntu-26.04.sh tools/vm-image/README.md
mise exec -- git commit -m "vm-image: preserve Ubuntu sbin layout"
```

Expected: commit succeeds. Pre-commit hooks pass.

---

### Task 4: Mirror The Builder Fix Into The Image Repository

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/AGENTS.md`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`

- [ ] **Step 1: Create image repo instructions**

Create `/Users/shayne/code/yeet-vm-images/AGENTS.md` with:

```markdown
# VM Image Repository Instructions

This repository builds and publishes official yeet Ubuntu VM image bundles.

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
- Keep `README.md`, build validation, workflow defaults, and release notes
  aligned with intentional image policy changes.
```

- [ ] **Step 2: Mirror canonical script and README**

Run:

```bash
cp tools/vm-image/build-ubuntu-26.04.sh /Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh
cp tools/vm-image/README.md /Users/shayne/code/yeet-vm-images/README.md
```

Then adjust `/Users/shayne/code/yeet-vm-images/README.md` command examples so
they keep using image-repo paths:

```markdown
scripts/build-linux-kernel.sh dist/kernel-linux-7.0
cd ../yeet
mise run guest:init:build
cd ../yeet-vm-images
sudo YEET_VM_KERNEL_PATH="$PWD/dist/kernel-linux-7.0/vmlinux" \
  YEET_VM_KERNEL_VERSION=linux-7.0-yeet \
  YEET_VM_INIT_PATH="$PWD/../yeet/guest/yeet-init/target/x86_64-unknown-linux-musl/release/yeet-init" \
  YEET_VM_GHOSTTY_TERMINFO="$PWD/../yeet/pkg/catch/xterm-ghostty.terminfo" \
  scripts/build-ubuntu-26.04.sh
```

- [ ] **Step 3: Update workflow default version**

In `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`,
change:

```yaml
default: ubuntu-26.04-amd64-v11
```

to:

```yaml
default: ubuntu-26.04-amd64-v12
```

- [ ] **Step 4: Replace workflow merged-bin assertions**

In the workflow `Verify bundle` step, keep `assert_symlink_target` and add:

```bash
assert_debugfs_type() {
  local path="$1"
  local want="$2"
  local got
  got="$(debugfs -R "stat $path" "$RUNNER_TEMP/rootfs.ext4" 2>/dev/null | awk '
    /Type:/ {
      for (i = 1; i <= NF; i++) {
        if ($i == "Type:") {
          print $(i + 1)
          exit
        }
      }
    }
  ')"
  if [ "$got" != "$want" ]; then
    echo "expected $path type $want, got ${got:-missing}" >&2
    exit 1
  fi
}
assert_path_exists() {
  local path="$1"
  if ! debugfs -R "stat $path" "$RUNNER_TEMP/rootfs.ext4" >/dev/null 2>&1; then
    echo "expected $path to exist" >&2
    exit 1
  fi
}
assert_dpkg_list_contains() {
  local package="$1"
  local path="$2"
  if ! debugfs_cat "/var/lib/dpkg/info/${package}.list" | grep -qx "$path"; then
    echo "expected package $package to own $path" >&2
    exit 1
  fi
}
```

Replace:

```bash
assert_symlink_target /usr/sbin bin
assert_symlink_target /sbin usr/bin
```

with:

```bash
assert_debugfs_type /usr/sbin directory
assert_symlink_target /sbin usr/sbin
for path in \
  /usr/sbin/sshd \
  /usr/sbin/agetty \
  /usr/sbin/unix_chkpwd \
  /usr/sbin/iptables-nft \
  /usr/sbin/xtables-nft-multi
do
  assert_path_exists "$path"
done
assert_dpkg_list_contains openssh-server /usr/sbin/sshd
assert_dpkg_list_contains util-linux /usr/sbin/agetty
assert_dpkg_list_contains libpam-modules-bin /usr/sbin/unix_chkpwd
assert_dpkg_list_contains iptables /usr/sbin/iptables-nft
assert_dpkg_list_contains iptables /usr/sbin/xtables-nft-multi
```

- [ ] **Step 5: Run image repo static checks**

Run:

```bash
bash -n /Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh
git -C /Users/shayne/code/yeet-vm-images diff --check
rg -n "merge_usr_sbin_into_usr_bin|assert_symlink_target /usr/sbin bin|usr/bin.*sbin layout" /Users/shayne/code/yeet-vm-images && exit 1 || true
```

Expected: `bash -n` and `diff --check` pass. The `rg` command produces no
matches and exits through `true`.

- [ ] **Step 6: Commit and push image repo builder changes**

Run:

```bash
git -C /Users/shayne/code/yeet-vm-images status --short --branch
git -C /Users/shayne/code/yeet-vm-images add AGENTS.md scripts/build-ubuntu-26.04.sh README.md .github/workflows/build-ubuntu-26.04.yml
git -C /Users/shayne/code/yeet-vm-images commit -m "image: preserve Ubuntu sbin layout"
git -C /Users/shayne/code/yeet-vm-images push origin main
```

Expected: image repo commit and push succeed.

---

### Task 5: Build And Publish The v12 Public VM Image

**Files:**
- External: GitHub Actions workflow in `yeetrun/yeet-vm-images`
- External: GitHub release `ubuntu-26.04-amd64-v12`

- [ ] **Step 1: Push root commits needed by the image workflow**

Run:

```bash
git status --short --branch
git push origin main
```

Expected: root `main` is pushed. Only the pre-existing untracked `yeet.toml`
may remain locally.

- [ ] **Step 2: Dispatch the image workflow**

Run:

```bash
gh workflow run "Build Ubuntu 26.04 VM image" \
  -R yeetrun/yeet-vm-images \
  -f version=ubuntu-26.04-amd64-v12 \
  -f yeet_ref=main \
  -f overwrite_release=false
```

Expected: workflow dispatch succeeds.

- [ ] **Step 3: Watch the workflow run**

Run:

```bash
run_id="$(gh run list -R yeetrun/yeet-vm-images --workflow "Build Ubuntu 26.04 VM image" --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$run_id" -R yeetrun/yeet-vm-images --exit-status
```

Expected: workflow exits 0.

- [ ] **Step 4: Verify the published image release**

Run:

```bash
gh release view ubuntu-26.04-amd64-v12 \
  -R yeetrun/yeet-vm-images \
  --json tagName,targetCommitish,publishedAt,assets \
  --jq '{tagName,targetCommitish,publishedAt,assets:[.assets[].name]}'
```

Expected: output includes assets `manifest.json`, `vmlinux`,
`rootfs.ext4.zst`, `firecracker`, `kernel.config`, and `checksums.txt`.

---

### Task 6: Adopt v12 In Yeet After Image Verification

**Files:**
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `tools/vm-image/README.md` if the image release revealed a version mismatch
- Test: `go test ./pkg/catch ./tools/vm-image -count=1`

- [ ] **Step 1: Update default image version**

In `pkg/catch/vm_image.go`, change:

```go
defaultVMImageVersion     = "ubuntu-26.04-amd64-v11"
```

to:

```go
defaultVMImageVersion     = "ubuntu-26.04-amd64-v12"
```

- [ ] **Step 2: Update default-version test**

In `pkg/catch/vm_image_test.go`, update
`TestDefaultVMImageVersionUsesLatestFastBundle` to expect v12:

```go
func TestDefaultVMImageVersionUsesLatestFastBundle(t *testing.T) {
	if defaultVMImageVersion != "ubuntu-26.04-amd64-v12" {
		t.Fatalf("default VM image version = %q, want ubuntu-26.04-amd64-v12", defaultVMImageVersion)
	}
}
```

- [ ] **Step 3: Run targeted tests**

Run:

```bash
go test ./pkg/catch ./tools/vm-image -count=1
git diff --check -- pkg/catch tools/vm-image
```

Expected: tests and diff check pass.

- [ ] **Step 4: Commit root adoption**

Run:

```bash
git add pkg/catch/vm_image.go pkg/catch/vm_image_test.go tools/vm-image/README.md
mise exec -- git commit -m "vm: adopt Ubuntu-compatible v12 image"
```

Expected: commit succeeds. Pre-commit hooks pass.

---

### Task 7: Live Validate v12 On pve1

**Files:**
- No source files unless validation exposes a bug
- Live host: `yeet-pve1`
- Test VM names: `vmcompat-v12`, `vmcompat-ts-v12`

- [ ] **Step 1: Build local yeet and upgrade catch on pve1**

Run:

```bash
go build -o /tmp/yeet-v12-compat ./cmd/yeet
/tmp/yeet-v12-compat --host=yeet-pve1 init root@pve1
```

Expected: catch installs successfully.

- [ ] **Step 2: Create a fresh ZFS LAN VM using v12**

Run:

```bash
/tmp/yeet-v12-compat --host=yeet-pve1 --progress=plain run vmcompat-v12 vm://ubuntu/26.04 \
  --service-root=flash/yeet/vms/vmcompat-v12 \
  --zfs \
  --disk=16g \
  --net=lan \
  --image-policy=update
```

Expected: output contains `Image: vm://ubuntu/26.04 (ubuntu-26.04-amd64-v12)`
and finishes with `VM vmcompat-v12 is running.`

- [ ] **Step 3: Verify SSH and systemd operational state**

Run:

```bash
/tmp/yeet-v12-compat --host=yeet-pve1 ssh vmcompat-v12 -- '
set -eu
uname -r
systemctl --failed --no-pager
systemctl show -p SystemState -p Tainted -p NFailedUnits
systemctl status --no-pager | sed -n "1,45p"
'
```

Expected:

- `uname -r` prints the yeet kernel.
- `systemctl --failed` shows `0 loaded units listed`.
- `NFailedUnits=0`.
- If `Tainted=unmerged-bin` appears, record it as the expected upstream Ubuntu
  layout warning, not as a failure.

- [ ] **Step 4: Verify Ubuntu package paths in the guest**

Run:

```bash
/tmp/yeet-v12-compat --host=yeet-pve1 ssh vmcompat-v12 -- '
set -eu
test -d /usr/sbin
test ! -L /usr/sbin
test "$(readlink /sbin)" = "usr/sbin"
for p in /usr/sbin/sshd /usr/sbin/agetty /usr/sbin/unix_chkpwd /usr/sbin/iptables-nft /usr/sbin/xtables-nft-multi; do
  test -e "$p"
  dpkg -S "$p" >/dev/null
done
update-alternatives --display iptables >/dev/null
iptables --version | grep -q nf_tables
sudo iptables -t mangle -I PREROUTING 1 -m conntrack --ctstate ESTABLISHED,RELATED -j CONNMARK --restore-mark --nfmask 0xff0000 --ctmask 0xff0000
sudo iptables -t mangle -D PREROUTING -m conntrack --ctstate ESTABLISHED,RELATED -j CONNMARK --restore-mark --nfmask 0xff0000 --ctmask 0xff0000
'
```

Expected: command exits 0.

- [ ] **Step 5: Verify apt reinstall keeps package paths valid**

Run:

```bash
/tmp/yeet-v12-compat --host=yeet-pve1 ssh vmcompat-v12 -- '
set -eu
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --reinstall openssh-server iptables
test -e /usr/sbin/sshd
test -e /usr/sbin/iptables-nft
dpkg -S /usr/sbin/sshd /usr/sbin/iptables-nft >/dev/null
systemctl --failed --no-pager
'
```

Expected: reinstall succeeds and no failed systemd units appear.

- [ ] **Step 6: Verify Tailscale install and start on a trash VM**

Run:

```bash
/tmp/yeet-v12-compat --host=yeet-pve1 --progress=plain run vmcompat-ts-v12 vm://ubuntu/26.04 \
  --service-root=flash/yeet/vms/vmcompat-ts-v12 \
  --zfs \
  --disk=16g \
  --net=lan \
  --image-policy=cached
/tmp/yeet-v12-compat --host=yeet-pve1 ssh vmcompat-ts-v12 -- '
set -eu
curl -fsSL https://tailscale.com/install.sh | sh
sudo systemctl enable --now tailscaled
systemctl is-active tailscaled
iptables --version | grep -q nf_tables
'
```

Expected: Tailscale installs, `tailscaled` is active, and iptables still reports
the nf_tables backend.

If an auth-key test is needed, run this without writing the key to disk:

```bash
: "${TAILSCALE_AUTHKEY:?set TAILSCALE_AUTHKEY in this shell only}"
/tmp/yeet-v12-compat --host=yeet-pve1 ssh vmcompat-ts-v12 -- "sudo tailscale up --authkey='$TAILSCALE_AUTHKEY' --hostname=yeet-vmcompat-ts-v12"
/tmp/yeet-v12-compat --host=yeet-pve1 ssh vmcompat-ts-v12 -- "tailscale status --peers=false"
/tmp/yeet-v12-compat --host=yeet-pve1 ssh vmcompat-ts-v12 -- "sudo tailscale logout"
```

Expected: `tailscale up` succeeds and the node is logged out before cleanup.

- [ ] **Step 7: Clean up live test VMs**

Run:

```bash
/tmp/yeet-v12-compat --host=yeet-pve1 rm vmcompat-v12 --clean-data --yes
/tmp/yeet-v12-compat --host=yeet-pve1 rm vmcompat-ts-v12 --clean-data --yes
ssh root@pve1 'zfs list -H -o name | grep -E "flash/yeet/vms/(vmcompat-v12|vmcompat-ts-v12)" && exit 1 || true'
```

Expected: both VMs are removed and no matching ZFS datasets remain.

---

### Task 8: Patch Release And Public Docs

**Files:**
- Modify: `website/docs/changelog.mdx`
- Modify: root `website` submodule pointer
- External: root git tag `v0.5.11`

- [ ] **Step 1: Confirm next patch version**

Run:

```bash
git tag --list 'v*' --sort=-version:refname | sed -n '1,5p'
```

Expected: latest tag is `v0.5.10`. Use `v0.5.11`. If a newer tag exists, bump
one patch beyond the newest tag and adjust all commands in this task.

- [ ] **Step 2: Update website changelog**

In `website/docs/changelog.mdx`, add a new top entry for `v0.5.11` dated
`June 6, 2026`:

```mdx
## June 6, 2026

### v0.5.11

- Restored Ubuntu-compatible package paths in the official VM image while
  keeping the fast Firecracker boot profile.
- Added VM image validation to catch filesystem and package-layout regressions
  before publishing.
```

- [ ] **Step 3: Commit and push website changelog**

Run:

```bash
git -C website diff --check
git -C website add docs/changelog.mdx
git -C website commit -m "docs: update changelog for v0.5.11"
git -C website push origin main
```

Expected: website commit and push succeed.

- [ ] **Step 4: Commit root release prep**

Run:

```bash
git add website
mise exec -- git commit -m "release: prepare v0.5.11"
```

Expected: root release commit succeeds. Pre-commit hooks pass.

- [ ] **Step 5: Tag and push release**

Run:

```bash
git tag -a v0.5.11 -m "v0.5.11"
git push origin main
git push origin v0.5.11
```

Expected: root main and tag push succeed.

- [ ] **Step 6: Final release verification**

Run:

```bash
git status --short --branch
git -C website status --short --branch
git ls-remote --tags origin v0.5.11
gh release view ubuntu-26.04-amd64-v12 -R yeetrun/yeet-vm-images --json tagName,assets --jq '{tagName,assets:[.assets[].name]}'
```

Expected:

- Root repo is clean except for any pre-existing untracked `yeet.toml`.
- Website repo is clean.
- `v0.5.11` tag exists on origin.
- `ubuntu-26.04-amd64-v12` image release has all expected assets.

---

## Self-Review Checklist

- Spec coverage: Tasks cover guardrails, root builder fix, validation,
  image-repo mirror, public image build, yeet default adoption, live pve1
  validation, changelog, patch release, and final verification.
- TDD: Task 2 writes failing root tests before Task 3 implementation. Workflow
  validation is updated before publishing the public image. Live validation is
  explicit before default adoption release is treated complete.
- Compatibility: The plan removes the custom sbin merge and asserts package
  ownership under `/usr/sbin` in both build-time and live validation.
- Scope: DNS-appliance profile work is intentionally out of scope; Tailscale is
  validated only as package install/start and optional auth-key connectivity.
