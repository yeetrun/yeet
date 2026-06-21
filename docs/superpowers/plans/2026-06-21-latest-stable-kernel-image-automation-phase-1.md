# Latest Stable Kernel Image Automation Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add daily automation that detects kernel.org's latest stable Linux kernel and auto-publishes updated official Ubuntu and NixOS yeet VM image bundles.

**Architecture:** Keep image construction in the existing `yeet-vm-images` build workflows and add a separate scheduler that resolves latest-stable kernel metadata, compares current latest manifests, computes hybrid release tags, and invokes the family builders. Add small compatibility changes in `yeet` so catch accepts hybrid image version tags and preserves kernel provenance fields from manifests.

**Tech Stack:** Bash, jq, GitHub Actions, GitHub CLI, Go tests for catch catalog/manifest parsing, existing yeet VM image build scripts.

---

## File Structure

### `/Users/shayne/code/yeet`

- Modify: `pkg/catch/vm_image.go`
  - Add `ImageRevision`, `UpstreamKernelVersion`, `KernelSourceURL`, and `KernelSourceSHA256` fields to `vmImageManifest`.
- Modify: `pkg/catch/vm_image_catalog.go`
  - Update catalog family matching to accept both `family-v<N>` and `family-kernel-<kernel>-v<N>`.
- Modify: `pkg/catch/vm_image_catalog_test.go`
  - Add tests for hybrid image version matching and pruning lookup compatibility.
- Modify: `pkg/catch/vm_image_test.go`
  - Add tests that new manifest provenance fields decode, validate, and survive cache reads.

### `/Users/shayne/code/yeet-vm-images`

- Create: `scripts/resolve-latest-kernel.sh`
  - Resolve kernel.org latest stable metadata into JSON.
- Create: `scripts/next-image-version.sh`
  - Compute next hybrid release tag from existing release tags.
- Create: `scripts/test-latest-kernel-automation.sh`
  - Run fixture-backed tests for both helper scripts.
- Create: `scripts/testdata/kernel-releases-7.1.1.json`
  - Kernel.org releases fixture.
- Create: `scripts/testdata/kernel-sha256sums-7.x.asc`
  - Checksum fixture for source tarball resolution.
- Create: `scripts/testdata/image-release-tags.txt`
  - Release tag fixture containing old and hybrid formats.
- Modify: `scripts/build-ubuntu-26.04.sh`
  - Add manifest provenance fields and image revision derivation.
- Modify: `scripts/build-nixos-26.05.sh`
  - Add manifest provenance fields and image revision derivation.
- Modify: `scripts/verify-catalog.sh`
  - Accept old and hybrid version formats while catalog latest aliases transition.
- Modify: `.github/workflows/build-ubuntu-26.04.yml`
  - Add `workflow_call`, accept upstream kernel provenance input, pass it to build script, and verify manifest fields.
- Modify: `.github/workflows/build-nixos-26.05.yml`
  - Add `workflow_call`, accept upstream kernel provenance input, pass it to build script, and verify manifest fields.
- Create: `.github/workflows/sync-latest-stable-kernel.yml`
  - Daily/manual scheduler that detects latest stable and invokes family builders.
- Modify: `README.md`
  - Document the scheduled workflow, hybrid version tags, and provenance fields.

## Task 1: Make Catch Accept Hybrid Image Versions

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog_test.go`

- [ ] **Step 1: Write failing hybrid matching tests**

Add this test to `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog_test.go`:

```go
func TestVMImageCatalogMatchesHybridKernelImageVersion(t *testing.T) {
	catalog := vmImageCatalogValidationTestCatalog()
	image, ok := catalog.ImageByVersion("ubuntu-26.04-amd64-kernel-7.1.1-v16")
	if !ok {
		t.Fatalf("ImageByVersion hybrid = ok false, want true")
	}
	if image.Payload != "vm://ubuntu/26.04" {
		t.Fatalf("ImageByVersion hybrid payload = %q, want vm://ubuntu/26.04", image.Payload)
	}
}

func TestVMImageCatalogRejectsMalformedHybridKernelImageVersion(t *testing.T) {
	catalog := vmImageCatalogValidationTestCatalog()
	badVersions := []string{
		"ubuntu-26.04-amd64-kernel-v16",
		"ubuntu-26.04-amd64-kernel-7.1-vx",
		"ubuntu-26.04-amd64-kernel-7.1.1-latest",
		"ubuntu-26.04-amd64-kernel-7.1.1",
	}
	for _, version := range badVersions {
		if image, ok := catalog.ImageByVersion(version); ok {
			t.Fatalf("ImageByVersion(%q) = %#v ok=true, want false", version, image)
		}
	}
}
```

- [ ] **Step 2: Run the failing tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/catch -run 'TestVMImageCatalogMatchesHybridKernelImageVersion|TestVMImageCatalogRejectsMalformedHybridKernelImageVersion' -count=1
```

Expected: `TestVMImageCatalogMatchesHybridKernelImageVersion` fails because `matchesVersion` only accepts numeric suffixes directly after `version_prefix`.

- [ ] **Step 3: Implement hybrid suffix matching**

Replace `matchesVersion` in `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog.go` with:

```go
func (i vmImageCatalogImage) matchesVersion(version string) bool {
	version = strings.TrimSpace(version)
	prefix := strings.TrimSpace(i.VersionPrefix)
	if !strings.HasPrefix(version, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(version, prefix)
	return isNumericVersionSuffix(suffix) || isHybridKernelVersionSuffix(suffix)
}

func isHybridKernelVersionSuffix(suffix string) bool {
	const kernelPrefix = "kernel-"
	if !strings.HasPrefix(suffix, kernelPrefix) {
		return false
	}
	rest := strings.TrimPrefix(suffix, kernelPrefix)
	versionPart, revisionPart, ok := strings.Cut(rest, "-")
	if !ok {
		return false
	}
	if !validUpstreamKernelVersion(versionPart) {
		return false
	}
	return isNumericVersionSuffix(revisionPart)
}

func validUpstreamKernelVersion(version string) bool {
	if version == "" {
		return false
	}
	segments := strings.Split(version, ".")
	if len(segments) < 2 {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
```

- [ ] **Step 4: Run the targeted tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/catch -run 'TestVMImageCatalogMatchesHybridKernelImageVersion|TestVMImageCatalogRejectsMalformedHybridKernelImageVersion|TestVMImageCatalogValidationRejectsNonnumericVersionSuffix' -count=1
```

Expected: all selected tests pass.

- [ ] **Step 5: Commit the catch version compatibility change**

Run:

```bash
cd /Users/shayne/code/yeet
git add pkg/catch/vm_image_catalog.go pkg/catch/vm_image_catalog_test.go
git commit -m "vm: accept kernel image version tags"
```

Expected: commit succeeds in the implementation worktree.

## Task 2: Preserve New Manifest Provenance In Catch

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image_test.go`

- [ ] **Step 1: Write the failing manifest decode test**

Add this test to `/Users/shayne/code/yeet/pkg/catch/vm_image_test.go`:

```go
func TestVMImageManifestPreservesKernelAutomationFields(t *testing.T) {
	raw := []byte(`{
		"name":"yeet-ubuntu-26.04",
		"version":"ubuntu-26.04-amd64-kernel-7.1.1-v16",
		"image_revision":16,
		"architecture":"x86_64",
		"image_profile":"fast",
		"kernel_policy":"yeet-managed",
		"guest_init":"/usr/local/lib/yeet-vm/yeet-init",
		"snap_support":false,
		"kernel":"vmlinux",
		"rootfs":"rootfs.ext4.zst",
		"firecracker":"firecracker",
		"rootfs_size":2147483648,
		"kernel_version":"linux-7.1.1-yeet",
		"upstream_kernel_version":"7.1.1",
		"kernel_source_url":"https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz",
		"kernel_source_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"checksums":{
			"vmlinux":"0000000000000000000000000000000000000000000000000000000000000000",
			"rootfs.ext4.zst":"1111111111111111111111111111111111111111111111111111111111111111",
			"firecracker":"2222222222222222222222222222222222222222222222222222222222222222"
		}
	}`)
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if manifest.ImageRevision != 16 {
		t.Fatalf("image revision = %d, want 16", manifest.ImageRevision)
	}
	if manifest.UpstreamKernelVersion != "7.1.1" {
		t.Fatalf("upstream kernel version = %q, want 7.1.1", manifest.UpstreamKernelVersion)
	}
	if manifest.KernelSourceURL != "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz" {
		t.Fatalf("kernel source URL = %q", manifest.KernelSourceURL)
	}
	if manifest.KernelSourceSHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("kernel source sha = %q", manifest.KernelSourceSHA256)
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/catch -run TestVMImageManifestPreservesKernelAutomationFields -count=1
```

Expected: test fails because `vmImageManifest` does not have the new fields.

- [ ] **Step 3: Add manifest fields**

Update `vmImageManifest` in `/Users/shayne/code/yeet/pkg/catch/vm_image.go`:

```go
type vmImageManifest struct {
	Name                  string            `json:"name"`
	Version               string            `json:"version"`
	ImageRevision         int               `json:"image_revision,omitempty"`
	Architecture          string            `json:"architecture"`
	ImageProfile          string            `json:"image_profile,omitempty"`
	Distro                string            `json:"distro,omitempty"`
	DistroVersion         string            `json:"distro_version,omitempty"`
	DefaultUser           string            `json:"default_user,omitempty"`
	KernelPolicy          string            `json:"kernel_policy,omitempty"`
	GuestInit             string            `json:"guest_init,omitempty"`
	GuestSystemInit       string            `json:"guest_system_init,omitempty"`
	MetadataDriver        string            `json:"metadata_driver,omitempty"`
	SnapSupport           *bool             `json:"snap_support,omitempty"`
	Kernel                string            `json:"kernel"`
	Initrd                string            `json:"initrd,omitempty"`
	RootFS                string            `json:"rootfs"`
	Firecracker           string            `json:"firecracker"`
	RootFSSize            int64             `json:"rootfs_size"`
	KernelVersion         string            `json:"kernel_version,omitempty"`
	UpstreamKernelVersion string            `json:"upstream_kernel_version,omitempty"`
	KernelSourceURL       string            `json:"kernel_source_url,omitempty"`
	KernelSourceSHA256    string            `json:"kernel_source_sha256,omitempty"`
	UbuntuKernelVersion   string            `json:"ubuntu_kernel_version,omitempty"`
	Provenance            map[string]string `json:"provenance,omitempty"`
	Checksums             map[string]string `json:"checksums"`
}
```

- [ ] **Step 4: Run catch package tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/catch -run 'TestVMImageManifestPreservesKernelAutomationFields|TestVMImageManifestPreservesNixOSFields|TestEnsureVMImage' -count=1
```

Expected: selected tests pass.

- [ ] **Step 5: Commit the manifest compatibility change**

Run:

```bash
cd /Users/shayne/code/yeet
git add pkg/catch/vm_image.go pkg/catch/vm_image_test.go
git commit -m "vm: preserve kernel image provenance"
```

Expected: commit succeeds in the implementation worktree.

## Task 3: Add Kernel And Version Helper Scripts

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/scripts/resolve-latest-kernel.sh`
- Create: `/Users/shayne/code/yeet-vm-images/scripts/next-image-version.sh`
- Create: `/Users/shayne/code/yeet-vm-images/scripts/test-latest-kernel-automation.sh`
- Create: `/Users/shayne/code/yeet-vm-images/scripts/testdata/kernel-releases-7.1.1.json`
- Create: `/Users/shayne/code/yeet-vm-images/scripts/testdata/kernel-sha256sums-7.x.asc`
- Create: `/Users/shayne/code/yeet-vm-images/scripts/testdata/image-release-tags.txt`

- [ ] **Step 1: Add kernel.org fixtures**

Create `/Users/shayne/code/yeet-vm-images/scripts/testdata/kernel-releases-7.1.1.json`:

```json
{
  "latest_stable": {
    "version": "7.1.1"
  },
  "releases": [
    {
      "moniker": "mainline",
      "version": "7.1",
      "iseol": false,
      "source": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.tar.xz",
      "pgp": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.tar.sign",
      "released": {
        "isodate": "2026-06-14"
      }
    },
    {
      "moniker": "stable",
      "version": "7.1.1",
      "iseol": false,
      "source": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz",
      "pgp": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.sign",
      "released": {
        "isodate": "2026-06-19"
      }
    }
  ]
}
```

Create `/Users/shayne/code/yeet-vm-images/scripts/testdata/kernel-sha256sums-7.x.asc`:

```text
0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  linux-7.1.1.tar.xz
aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  linux-7.1.tar.xz
```

Create `/Users/shayne/code/yeet-vm-images/scripts/testdata/image-release-tags.txt`:

```text
ubuntu-26.04-amd64-v14
ubuntu-26.04-amd64-v15
ubuntu-26.04-amd64-latest
nixos-26.05-amd64-v14
nixos-26.05-amd64-latest
ubuntu-26.04-amd64-kernel-7.1.1-v16
nixos-26.05-amd64-kernel-7.1.1-v15
```

- [ ] **Step 2: Add the failing test runner**

Create `/Users/shayne/code/yeet-vm-images/scripts/test-latest-kernel-automation.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

script_source="${BASH_SOURCE[0]}"
script_dir="${script_source%/*}"
if [ "$script_dir" = "$script_source" ]; then
	script_dir="."
fi
repo_root="$(cd "$script_dir/.." && pwd)"

kernel_json="$repo_root/scripts/testdata/kernel-releases-7.1.1.json"
kernel_sums="$repo_root/scripts/testdata/kernel-sha256sums-7.x.asc"
tags="$repo_root/scripts/testdata/image-release-tags.txt"

resolved="$(
	YEET_KERNEL_RELEASES_JSON_URL="file://$kernel_json" \
	YEET_KERNEL_SHA256SUMS_URL="file://$kernel_sums" \
		"$repo_root/scripts/resolve-latest-kernel.sh"
)"

jq -e '
  .moniker == "stable" and
  .version == "7.1.1" and
  .source_url == "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz" and
  .source_sha256 == "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" and
  .released == "2026-06-19"
' <<<"$resolved" >/dev/null

ubuntu_next="$("$repo_root/scripts/next-image-version.sh" ubuntu-26.04-amd64 7.1.2 "$tags")"
jq -e '
  .family == "ubuntu-26.04-amd64" and
  .upstream_kernel_version == "7.1.2" and
  .image_revision == 17 and
  .version == "ubuntu-26.04-amd64-kernel-7.1.2-v17"
' <<<"$ubuntu_next" >/dev/null

nixos_next="$("$repo_root/scripts/next-image-version.sh" nixos-26.05-amd64 7.1.2 "$tags")"
jq -e '
  .family == "nixos-26.05-amd64" and
  .upstream_kernel_version == "7.1.2" and
  .image_revision == 16 and
  .version == "nixos-26.05-amd64-kernel-7.1.2-v16"
' <<<"$nixos_next" >/dev/null
```

- [ ] **Step 3: Run the failing helper tests**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
chmod +x scripts/test-latest-kernel-automation.sh
scripts/test-latest-kernel-automation.sh
```

Expected: fails because the helper scripts do not exist.

- [ ] **Step 4: Implement `resolve-latest-kernel.sh`**

Create `/Users/shayne/code/yeet-vm-images/scripts/resolve-latest-kernel.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

releases_url="${YEET_KERNEL_RELEASES_JSON_URL:-https://www.kernel.org/releases.json}"

require() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

for cmd in awk basename curl dirname jq; do
	require "$cmd"
done

releases_json="$(curl -fsSL --retry 3 "$releases_url")"
latest_version="$(jq -r '.latest_stable.version // empty' <<<"$releases_json")"
if [ -z "$latest_version" ]; then
	echo "kernel.org releases JSON missing latest_stable.version" >&2
	exit 1
fi

release_json="$(jq -c --arg version "$latest_version" '
  [.releases[] | select(.moniker == "stable" and .version == $version and (.iseol | not))][0] // empty
' <<<"$releases_json")"
if [ -z "$release_json" ]; then
	echo "kernel.org releases JSON missing stable release entry for $latest_version" >&2
	exit 1
fi

source_url="$(jq -r '.source // empty' <<<"$release_json")"
released="$(jq -r '.released.isodate // empty' <<<"$release_json")"
if [ -z "$source_url" ]; then
	echo "kernel.org release $latest_version missing source URL" >&2
	exit 1
fi

source_dir="$(dirname "$source_url")"
source_name="$(basename "$source_url")"
sha_url="${YEET_KERNEL_SHA256SUMS_URL:-$source_dir/sha256sums.asc}"
sha_sums="$(curl -fsSL --retry 3 "$sha_url")"
source_sha256="$(awk -v name="$source_name" '$2 == name || $2 == "*" name { print $1; exit }' <<<"$sha_sums")"
if ! grep -Eq '^[0-9a-f]{64}$' <<<"$source_sha256"; then
	echo "could not resolve SHA-256 for $source_name from $sha_url" >&2
	exit 1
fi

jq -n \
	--arg moniker "stable" \
	--arg version "$latest_version" \
	--arg source_url "$source_url" \
	--arg source_sha256 "$source_sha256" \
	--arg released "$released" \
	'{
	  moniker: $moniker,
	  version: $version,
	  source_url: $source_url,
	  source_sha256: $source_sha256,
	  released: $released
	}'
```

- [ ] **Step 5: Implement `next-image-version.sh`**

Create `/Users/shayne/code/yeet-vm-images/scripts/next-image-version.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

family="${1:-}"
kernel_version="${2:-}"
tags_file="${3:-}"

if [ -z "$family" ] || [ -z "$kernel_version" ]; then
	echo "usage: scripts/next-image-version.sh <family-prefix> <kernel-version> [tags-file]" >&2
	exit 2
fi

if ! grep -Eq '^[a-z0-9]+-[0-9]+[.][0-9]+-amd64$' <<<"$family"; then
	echo "invalid image family prefix: $family" >&2
	exit 1
fi
if ! grep -Eq '^[0-9]+[.][0-9]+([.][0-9]+)?$' <<<"$kernel_version"; then
	echo "invalid upstream kernel version: $kernel_version" >&2
	exit 1
fi

if [ -n "$tags_file" ]; then
	tags="$(cat "$tags_file")"
else
	tags="$(gh release list --limit 200 --json tagName --jq '.[].tagName')"
fi

max_revision="$(
	awk -v family="$family" '
	  $0 ~ "^" family "-v[0-9]+$" {
	    rev = $0
	    sub("^" family "-v", "", rev)
	    if (rev + 0 > max) max = rev + 0
	  }
	  $0 ~ "^" family "-kernel-[0-9]+[.][0-9]+([.][0-9]+)?-v[0-9]+$" {
	    rev = $0
	    sub("^" family "-kernel-[0-9]+[.][0-9]+([.][0-9]+)?-v", "", rev)
	    if (rev + 0 > max) max = rev + 0
	  }
	  END { print max + 0 }
	' <<<"$tags"
)"
next_revision=$((max_revision + 1))
version="${family}-kernel-${kernel_version}-v${next_revision}"

jq -n \
	--arg family "$family" \
	--arg upstream_kernel_version "$kernel_version" \
	--arg version "$version" \
	--argjson image_revision "$next_revision" \
	'{
	  family: $family,
	  upstream_kernel_version: $upstream_kernel_version,
	  image_revision: $image_revision,
	  version: $version
	}'
```

- [ ] **Step 6: Run the helper tests**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
chmod +x scripts/resolve-latest-kernel.sh scripts/next-image-version.sh scripts/test-latest-kernel-automation.sh
scripts/test-latest-kernel-automation.sh
```

Expected: tests pass with no output.

- [ ] **Step 7: Commit helper scripts and fixtures**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add scripts/resolve-latest-kernel.sh scripts/next-image-version.sh scripts/test-latest-kernel-automation.sh scripts/testdata
git commit -m "images: add latest stable kernel helpers"
```

Expected: commit succeeds.

## Task 4: Add Manifest Provenance To Image Builders

**Files:**
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-nixos-26.05.sh`

- [ ] **Step 1: Add static manifest checks to the helper test**

Append this block to `/Users/shayne/code/yeet-vm-images/scripts/test-latest-kernel-automation.sh`:

```bash
grep -q 'upstream_kernel_version' "$repo_root/scripts/build-ubuntu-26.04.sh"
grep -q 'kernel_source_url' "$repo_root/scripts/build-ubuntu-26.04.sh"
grep -q 'kernel_source_sha256' "$repo_root/scripts/build-ubuntu-26.04.sh"
grep -q 'image_revision' "$repo_root/scripts/build-ubuntu-26.04.sh"
grep -q 'upstream_kernel_version' "$repo_root/scripts/build-nixos-26.05.sh"
grep -q 'kernel_source_url' "$repo_root/scripts/build-nixos-26.05.sh"
grep -q 'kernel_source_sha256' "$repo_root/scripts/build-nixos-26.05.sh"
grep -q 'image_revision' "$repo_root/scripts/build-nixos-26.05.sh"
```

- [ ] **Step 2: Run the failing static checks**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
scripts/test-latest-kernel-automation.sh
```

Expected: fails because the builders do not yet include the new manifest fields.

- [ ] **Step 3: Add common environment variables to both builders**

Add this block near the existing `kernel_version` variables in both `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh` and `/Users/shayne/code/yeet-vm-images/scripts/build-nixos-26.05.sh`:

```bash
upstream_kernel_version="${YEET_VM_UPSTREAM_KERNEL_VERSION:-}"
kernel_source_url="${YEET_KERNEL_SOURCE_URL:-}"
kernel_source_sha256="${YEET_KERNEL_SOURCE_SHA256:-}"
image_revision="${YEET_VM_IMAGE_REVISION:-}"
```

- [ ] **Step 4: Add image revision fallback to both builders**

Add this helper near the other small shell helpers in both builder scripts:

```bash
image_revision_from_version() {
	local raw="$1"
	case "$raw" in
		*-v[0-9]*)
			printf '%s\n' "${raw##*-v}"
			;;
		*)
			printf '%s\n' ""
			;;
	esac
}

if [ -z "$image_revision" ]; then
	image_revision="$(image_revision_from_version "$version")"
fi
if [ -n "$image_revision" ] && ! printf '%s\n' "$image_revision" | grep -Eq '^[0-9]+$'; then
	echo "invalid YEET_VM_IMAGE_REVISION=$image_revision" >&2
	exit 1
fi
```

- [ ] **Step 5: Write Ubuntu manifest fields**

In `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`, add these top-level JSON fields after `"version": "$version",`:

```json
  "image_revision": ${image_revision:-0},
```

Add these top-level JSON fields after `"kernel_version": "$kernel_version",`:

```json
  "upstream_kernel_version": "$upstream_kernel_version",
  "kernel_source_url": "$kernel_source_url",
  "kernel_source_sha256": "$kernel_source_sha256",
```

Expected: for scheduled builds, the values are non-empty; for local legacy builds, the string fields are empty and `image_revision` falls back to parsed version or `0`.

- [ ] **Step 6: Write NixOS manifest fields**

In `/Users/shayne/code/yeet-vm-images/scripts/build-nixos-26.05.sh`, add the same top-level JSON fields after `"version": "$version",` and after `"kernel_version": "$kernel_version",`.

- [ ] **Step 7: Run static checks and shell parse checks**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
scripts/test-latest-kernel-automation.sh
bash -n scripts/build-ubuntu-26.04.sh scripts/build-nixos-26.05.sh
```

Expected: all checks pass.

- [ ] **Step 8: Commit builder provenance changes**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add scripts/build-ubuntu-26.04.sh scripts/build-nixos-26.05.sh scripts/test-latest-kernel-automation.sh
git commit -m "images: record kernel automation provenance"
```

Expected: commit succeeds.

## Task 5: Make Build Workflows Reusable And Provenance-Aware

**Files:**
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`

- [ ] **Step 1: Add reusable workflow inputs**

For both build workflow files, add an `on.workflow_call.inputs` block mirroring the existing `workflow_dispatch.inputs`. Include these additional inputs:

```yaml
      upstream_kernel_version:
        description: Upstream Linux kernel version, without the linux- prefix or yeet localversion.
        required: true
        type: string
      image_revision:
        description: Numeric per-family image revision.
        required: true
        type: string
```

For existing inputs in `workflow_call`, include explicit `type` values. For booleans, use `type: boolean`; for all others, use `type: string`.

- [ ] **Step 2: Add manual dispatch defaults for new inputs**

For both workflow files under `workflow_dispatch.inputs`, add:

```yaml
      upstream_kernel_version:
        description: Upstream Linux kernel version, without the linux- prefix or yeet localversion.
        required: true
        default: "7.0"
      image_revision:
        description: Numeric per-family image revision.
        required: true
        default: "0"
```

- [ ] **Step 3: Pass new environment variables to builders**

In both workflow `env:` blocks, add:

```yaml
      YEET_VM_UPSTREAM_KERNEL_VERSION: ${{ inputs.upstream_kernel_version }}
      YEET_VM_IMAGE_REVISION: ${{ inputs.image_revision }}
```

In the Ubuntu `sudo env` call, add:

```bash
            YEET_VM_UPSTREAM_KERNEL_VERSION="$YEET_VM_UPSTREAM_KERNEL_VERSION" \
            YEET_VM_IMAGE_REVISION="$YEET_VM_IMAGE_REVISION" \
```

The NixOS builder already runs without `sudo`, so environment variables from the job are visible to `scripts/build-nixos-26.05.sh`.

- [ ] **Step 4: Verify manifest provenance in both workflows**

In both workflow `Verify bundle` steps, extend the existing `jq -e` manifest expression with:

```jq
            (.image_revision | type == "number") and
            .upstream_kernel_version == env.YEET_VM_UPSTREAM_KERNEL_VERSION and
            .kernel_source_url == env.YEET_KERNEL_SOURCE_URL and
            .kernel_source_sha256 == env.YEET_KERNEL_SOURCE_SHA256 and
```

- [ ] **Step 5: Parse workflow YAML**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
ruby -e 'require "yaml"; ARGV.each { |f| YAML.load_file(f); puts "ok #{f}" }' .github/workflows/build-ubuntu-26.04.yml .github/workflows/build-nixos-26.05.yml
```

Expected:

```text
ok .github/workflows/build-ubuntu-26.04.yml
ok .github/workflows/build-nixos-26.05.yml
```

- [ ] **Step 6: Commit reusable workflow changes**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add .github/workflows/build-ubuntu-26.04.yml .github/workflows/build-nixos-26.05.yml
git commit -m "images: make image builders reusable"
```

Expected: commit succeeds.

## Task 6: Add Scheduled Latest Stable Kernel Workflow

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/.github/workflows/sync-latest-stable-kernel.yml`

- [ ] **Step 1: Create scheduler workflow**

Create `/Users/shayne/code/yeet-vm-images/.github/workflows/sync-latest-stable-kernel.yml`:

```yaml
name: Sync latest stable Linux kernel VM images

on:
  schedule:
    - cron: "17 9 * * *"
  workflow_dispatch:
    inputs:
      force:
        description: Build even if latest manifests already use the detected kernel.
        required: true
        type: boolean
        default: false
      yeet_ref:
        description: yeet repository ref used to build guest tools.
        required: true
        default: main

permissions:
  contents: write

jobs:
  detect:
    name: Detect latest stable kernel
    runs-on: ubuntu-24.04
    outputs:
      kernel_version: ${{ steps.detect.outputs.kernel_version }}
      kernel_source_url: ${{ steps.detect.outputs.kernel_source_url }}
      kernel_source_sha256: ${{ steps.detect.outputs.kernel_source_sha256 }}
      ubuntu_build: ${{ steps.detect.outputs.ubuntu_build }}
      ubuntu_version: ${{ steps.detect.outputs.ubuntu_version }}
      ubuntu_revision: ${{ steps.detect.outputs.ubuntu_revision }}
      nixos_build: ${{ steps.detect.outputs.nixos_build }}
      nixos_version: ${{ steps.detect.outputs.nixos_version }}
      nixos_revision: ${{ steps.detect.outputs.nixos_revision }}
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Install dependencies
        run: |
          sudo apt-get update
          sudo apt-get install -y curl jq

      - name: Detect image work
        id: detect
        env:
          FORCE: ${{ github.event.inputs.force || 'false' }}
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          kernel_json="$(scripts/resolve-latest-kernel.sh)"
          kernel_version="$(jq -r '.version' <<<"$kernel_json")"
          kernel_source_url="$(jq -r '.source_url' <<<"$kernel_json")"
          kernel_source_sha256="$(jq -r '.source_sha256' <<<"$kernel_json")"

          current_upstream_kernel() {
            local manifest="$1"
            local upstream
            upstream="$(jq -r '.upstream_kernel_version // empty' "$manifest")"
            if [ -n "$upstream" ]; then
              printf '%s\n' "$upstream"
              return
            fi
            jq -r '.kernel_version // empty' "$manifest" | sed -E 's/^linux-//; s/-yeet$//'
          }

          curl -fsSL --retry 3 \
            https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json \
            -o ubuntu-latest-manifest.json
          curl -fsSL --retry 3 \
            https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json \
            -o nixos-latest-manifest.json

          ubuntu_current="$(current_upstream_kernel ubuntu-latest-manifest.json)"
          nixos_current="$(current_upstream_kernel nixos-latest-manifest.json)"
          ubuntu_build=false
          nixos_build=false
          if [ "$FORCE" = "true" ] || [ "$ubuntu_current" != "$kernel_version" ]; then
            ubuntu_build=true
          fi
          if [ "$FORCE" = "true" ] || [ "$nixos_current" != "$kernel_version" ]; then
            nixos_build=true
          fi

          ubuntu_next="$(scripts/next-image-version.sh ubuntu-26.04-amd64 "$kernel_version")"
          nixos_next="$(scripts/next-image-version.sh nixos-26.05-amd64 "$kernel_version")"

          {
            echo "kernel_version=$kernel_version"
            echo "kernel_source_url=$kernel_source_url"
            echo "kernel_source_sha256=$kernel_source_sha256"
            echo "ubuntu_build=$ubuntu_build"
            echo "ubuntu_version=$(jq -r '.version' <<<"$ubuntu_next")"
            echo "ubuntu_revision=$(jq -r '.image_revision' <<<"$ubuntu_next")"
            echo "nixos_build=$nixos_build"
            echo "nixos_version=$(jq -r '.version' <<<"$nixos_next")"
            echo "nixos_revision=$(jq -r '.image_revision' <<<"$nixos_next")"
          } >>"$GITHUB_OUTPUT"

          {
            echo "## Latest stable kernel detection"
            echo
            echo "- Detected kernel: \`$kernel_version\`"
            echo "- Ubuntu current kernel: \`$ubuntu_current\`"
            echo "- Ubuntu action: \`$ubuntu_build\`"
            echo "- Ubuntu next version: \`$(jq -r '.version' <<<"$ubuntu_next")\`"
            echo "- NixOS current kernel: \`$nixos_current\`"
            echo "- NixOS action: \`$nixos_build\`"
            echo "- NixOS next version: \`$(jq -r '.version' <<<"$nixos_next")\`"
          } >>"$GITHUB_STEP_SUMMARY"

  build-ubuntu:
    name: Build Ubuntu latest stable kernel image
    needs: detect
    if: needs.detect.outputs.ubuntu_build == 'true'
    uses: ./.github/workflows/build-ubuntu-26.04.yml
    with:
      version: ${{ needs.detect.outputs.ubuntu_version }}
      yeet_ref: ${{ github.event.inputs.yeet_ref || 'main' }}
      kernel_version: ${{ needs.detect.outputs.kernel_version }}
      upstream_kernel_version: ${{ needs.detect.outputs.kernel_version }}
      image_revision: ${{ needs.detect.outputs.ubuntu_revision }}
      kernel_source_url: ${{ needs.detect.outputs.kernel_source_url }}
      kernel_source_sha256: ${{ needs.detect.outputs.kernel_source_sha256 }}
      kernel_config_url: https://raw.githubusercontent.com/firecracker-microvm/firecracker/86a2559b26a4b9a05405aeaa58bab0f7261d71bc/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config
      firecracker_version: v1.14.3
      zstd_level: "10"
      overwrite_release: false
      publish_latest_alias: true
      latest_alias: ubuntu-26.04-amd64-latest
    secrets: inherit

  build-nixos:
    name: Build NixOS latest stable kernel image
    needs: detect
    if: needs.detect.outputs.nixos_build == 'true'
    uses: ./.github/workflows/build-nixos-26.05.yml
    with:
      version: ${{ needs.detect.outputs.nixos_version }}
      yeet_ref: ${{ github.event.inputs.yeet_ref || 'main' }}
      kernel_version: ${{ needs.detect.outputs.kernel_version }}
      upstream_kernel_version: ${{ needs.detect.outputs.kernel_version }}
      image_revision: ${{ needs.detect.outputs.nixos_revision }}
      kernel_source_url: ${{ needs.detect.outputs.kernel_source_url }}
      kernel_source_sha256: ${{ needs.detect.outputs.kernel_source_sha256 }}
      kernel_config_url: https://raw.githubusercontent.com/firecracker-microvm/firecracker/86a2559b26a4b9a05405aeaa58bab0f7261d71bc/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config
      firecracker_version: v1.14.3
      zstd_level: "10"
      overwrite_release: false
      publish_latest_alias: true
      latest_alias: nixos-26.05-amd64-latest
    secrets: inherit
```

- [ ] **Step 2: Parse workflow YAML**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
ruby -e 'require "yaml"; YAML.load_file(".github/workflows/sync-latest-stable-kernel.yml"); puts "ok"'
```

Expected:

```text
ok
```

- [ ] **Step 3: Commit scheduler workflow**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add .github/workflows/sync-latest-stable-kernel.yml
git commit -m "images: schedule latest stable kernel builds"
```

Expected: commit succeeds.

## Task 7: Update Catalog Validation And README

**Files:**
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/verify-catalog.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`

- [ ] **Step 1: Update catalog version validation**

In `/Users/shayne/code/yeet-vm-images/scripts/verify-catalog.sh`, replace the manifest version test with a regex that accepts old and hybrid versions:

```bash
	version_prefix_regex="${version_prefix//./\\.}"
	if ! jq -e --arg version_re "^${version_prefix_regex}(v[0-9]+|kernel-[0-9]+[.][0-9]+([.][0-9]+)?-v[0-9]+)$" '
	  (.version | type == "string") and
	  (.version | test($version_re))
	' "$manifest" >/dev/null; then
		echo "$payload manifest version $version does not match prefix $version_prefix" >&2
		exit 1
	fi
```

- [ ] **Step 2: Add manifest provenance validation**

In the same manifest loop, extend the final `jq -e` expression:

```jq
	  (.guest_init == "/usr/local/lib/yeet-vm/yeet-init") and
	  (.guest_agent == "/usr/local/lib/yeet-vm/yeet-agent") and
	  (.guest_agent_sha256 | test("^[0-9a-f]{64}$")) and
	  (.checksums | type == "object") and
	  ((.upstream_kernel_version | not) or (.upstream_kernel_version | test("^[0-9]+[.][0-9]+([.][0-9]+)?$"))) and
	  ((.kernel_source_sha256 | not) or (.kernel_source_sha256 | test("^[0-9a-f]{64}$")))
```

- [ ] **Step 3: Update README**

In `/Users/shayne/code/yeet-vm-images/README.md`, add a short section after the catalog paragraph:

```markdown
## Automatic Kernel Refresh

The `Sync latest stable Linux kernel VM images` workflow runs daily and can also
be manually dispatched. It reads kernel.org's latest stable release metadata,
compares it to the current Ubuntu and NixOS latest manifests, and only builds
families whose latest alias is behind.

Automatically published immutable versions include both upstream kernel and
image revision, for example:

`ubuntu-26.04-amd64-kernel-7.1.1-v16`

The final `v<N>` remains the per-family image revision. This allows publishing a
new image revision for the same kernel when rootfs policy, Firecracker, or yeet
guest tools change. Stable latest aliases keep their existing names, so catch
continues to resolve `vm://ubuntu/26.04` and `vm://nixos/26.05` through
`catalog.json`.
```

- [ ] **Step 4: Run validation**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
scripts/test-latest-kernel-automation.sh
scripts/verify-catalog.sh
```

Expected: helper tests pass and catalog validation succeeds against the current published latest aliases.

- [ ] **Step 5: Commit docs and validation**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add README.md scripts/verify-catalog.sh
git commit -m "images: document automatic kernel refresh"
```

Expected: commit succeeds.

## Task 8: Full Local Verification

**Files:**
- No planned file edits.

- [ ] **Step 1: Run yeet catch compatibility tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/catch -count=1
```

Expected: package tests pass.

- [ ] **Step 2: Run yeet-vm-images helper and catalog tests**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
scripts/test-latest-kernel-automation.sh
scripts/verify-catalog.sh
bash -n scripts/*.sh
ruby -e 'require "yaml"; Dir[".github/workflows/*.yml"].sort.each { |f| YAML.load_file(f); puts "ok #{f}" }'
```

Expected: all commands pass.

- [ ] **Step 3: Run Nix checks when Nix is available**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
if command -v nix >/dev/null 2>&1; then
  mise run lint
  YEET_SOURCE_PATH=/Users/shayne/code/yeet scripts/verify-nixos-26.05.sh
else
  echo "nix unavailable; skipped Nix checks"
fi
```

Expected: checks pass if Nix is installed; otherwise the command prints the skip line.

- [ ] **Step 4: Commit no-op verification evidence if changes were made**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git status --short
cd /Users/shayne/code/yeet
git status --short
```

Expected: only intentional changes from completed tasks are present. If there are unstaged generated changes, inspect them before proceeding.

## Task 9: Publish And Verify Phase 1

**Files:**
- No planned file edits unless publication exposes a defect.

- [ ] **Step 1: Push implementation branches**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git push origin HEAD
cd /Users/shayne/code/yeet
git push origin HEAD
```

Expected: both branches push successfully.

- [ ] **Step 2: Merge or otherwise land the implementation according to repo workflow**

Use the repository's normal review path. The scheduler only runs on the default branch after the workflow file lands.

Expected: default branch contains both the catch compatibility changes and the `yeet-vm-images` scheduler changes.

- [ ] **Step 3: Manually dispatch the scheduler**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
gh workflow run "Sync latest stable Linux kernel VM images" --ref main -f force=true -f yeet_ref=main
```

Expected: workflow starts successfully.

- [ ] **Step 4: Watch the workflow**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
run_id="$(gh run list --workflow "Sync latest stable Linux kernel VM images" --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$run_id" --exit-status
```

Expected: scheduler and any invoked builder jobs pass.

- [ ] **Step 5: Verify published latest manifests**

Run:

```bash
ubuntu_manifest="$(mktemp)"
nixos_manifest="$(mktemp)"
curl -fsSL https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json -o "$ubuntu_manifest"
curl -fsSL https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json -o "$nixos_manifest"
jq -e '.version | test("^ubuntu-26[.]04-amd64-kernel-[0-9]+[.][0-9]+([.][0-9]+)?-v[0-9]+$")' "$ubuntu_manifest"
jq -e '.version | test("^nixos-26[.]05-amd64-kernel-[0-9]+[.][0-9]+([.][0-9]+)?-v[0-9]+$")' "$nixos_manifest"
jq -e '.image_revision > 0 and (.upstream_kernel_version | test("^[0-9]+[.][0-9]+([.][0-9]+)?$")) and (.kernel_source_sha256 | test("^[0-9a-f]{64}$"))' "$ubuntu_manifest"
jq -e '.image_revision > 0 and (.upstream_kernel_version | test("^[0-9]+[.][0-9]+([.][0-9]+)?$")) and (.kernel_source_sha256 | test("^[0-9a-f]{64}$"))' "$nixos_manifest"
```

Expected: all `jq` checks pass.

- [ ] **Step 6: Verify release assets exist**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
for version in \
  "$(jq -r '.version' "$ubuntu_manifest")" \
  "$(jq -r '.version' "$nixos_manifest")" \
  ubuntu-26.04-amd64-latest \
  nixos-26.05-amd64-latest
do
  gh release view "$version" --json assets --jq '.assets[].name' \
    | sort \
    | grep -Eq '^checksums.txt$'
  gh release view "$version" --json assets --jq '.assets[].name' \
    | sort \
    | grep -Eq '^manifest.json$'
  gh release view "$version" --json assets --jq '.assets[].name' \
    | sort \
    | grep -Eq '^vmlinux$'
done
```

Expected: each immutable and latest release exposes the expected core assets.

- [ ] **Step 7: Live update and boot smoke VMs through yeet/catch**

Run:

```bash
cd /Users/shayne/code/yeet
CATCH_HOST=yeet-lab go run ./cmd/yeet vm images update vm://ubuntu/26.04
CATCH_HOST=yeet-lab go run ./cmd/yeet vm images update vm://nixos/26.05
CATCH_HOST=yeet-lab go run ./cmd/yeet run ubuntu-kernel-smoke vm://ubuntu/26.04 --image-policy=update
CATCH_HOST=yeet-lab go run ./cmd/yeet run nixos-kernel-smoke vm://nixos/26.05 --image-policy=update
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh ubuntu-kernel-smoke -- 'uname -r && whoami && systemctl --failed --no-pager && systemctl show -p SystemState -p Tainted -p NFailedUnits && nft --version && iptables --version && test -c /dev/net/tun'
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh nixos-kernel-smoke -- 'uname -r && whoami && systemctl --failed --no-pager && systemctl show -p SystemState -p Tainted -p NFailedUnits && nixos-version && nix --version && nft --version && iptables --version && test -c /dev/net/tun'
```

Expected: both VMs boot, `uname -r` reports the newly detected `linux-<version>-yeet` kernel, users are `ubuntu` and `nixos`, failed units are empty, and tool checks exit 0.

- [ ] **Step 8: Clean up smoke VMs**

Run:

```bash
cd /Users/shayne/code/yeet
CATCH_HOST=yeet-lab go run ./cmd/yeet rm ubuntu-kernel-smoke --clean-data
CATCH_HOST=yeet-lab go run ./cmd/yeet rm nixos-kernel-smoke --clean-data
```

Expected: smoke VMs and data are removed.
