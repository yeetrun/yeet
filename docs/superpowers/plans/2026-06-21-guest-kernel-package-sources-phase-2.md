# Guest Kernel Package Sources Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish yeet-managed kernel package sources and add a catch opt-in path so Ubuntu and NixOS guests can install a newer yeet kernel artifact through normal package mechanisms and use it on the next Firecracker boot.

**Architecture:** Package publication lives in `yeet-vm-images` and produces an apt repository plus a Nix flake package from the same kernel artifacts used by image bundles. Runtime consumption lives in `yeet`: guests install package-owned kernel artifacts and a data-only selector under `/etc/yeet-vm/kernel`, then catch verifies that selector, copies the selected kernel to a host-owned service kernel cache, and rewrites the VM Firecracker config before restart.

**Tech Stack:** Bash, dpkg-deb, dpkg-scanpackages or apt-ftparchive, GPG signing for apt repository metadata, Nix flakes, Go CLI/catch code, Firecracker direct kernel boot.

## Implementation Notes

- The package workflow packages `vmlinux` and `kernel.config` from an immutable
  VM image release instead of rebuilding the kernel, so package bytes match the
  image bundle assets exactly.
- The public Nix source is self-contained under `kernel-packages/`. A flake
  consumed with `?dir=kernel-packages` cannot import files from `../nix`.
- GitHub Pages publishes `/apt` plus `yeet-vm-kernel-flake.tar.gz`; guests can
  use the tarball flake URL directly.
- Documentation is kept in this plan and the image repository README rather
  than adding a separate docs tree to `yeet-vm-images`.

---

## File Structure

### `/Users/shayne/code/yeet`

- Create: `pkg/catch/vm_kernel_selection.go`
  - Parse and validate guest kernel selector JSON mounted from the VM rootfs.
- Create: `pkg/catch/vm_kernel_selection_test.go`
  - Unit tests for selector validation and path safety.
- Create: `pkg/catch/vm_kernel_sync.go`
  - Implement host-side sync from guest-installed kernel package to service runtime config.
- Create: `pkg/catch/vm_kernel_sync_test.go`
  - Tests for selector reading, checksum validation, host cache writes, and Firecracker config rewrite.
- Modify: `pkg/cli/cli.go`
  - Add `yeet vm kernel sync <svc> [--restart]`.
- Modify: `pkg/cli/cli_test.go`
  - Parser tests for the new command and flags.
- Modify: `cmd/yeet/cli_bridge.go`
  - Route the new CLI command to catch RPC or local catch execution.
- Modify: `pkg/catch/vm_provision.go`
  - Reuse kernel selection helpers when a VM is reprovisioned or restarted through catch.
- Modify: `pkg/catch/vm_resize.go`
  - Preserve selected host-side kernel path when VM settings rewrite Firecracker config.

### `/Users/shayne/code/yeet-vm-images`

- Create: `packages/kernel/deb/DEBIAN/control.in`
  - Template for the Ubuntu kernel artifact package.
- Create: `packages/kernel/deb/usr/lib/yeet-vm-kernel/select-kernel`
  - Guest-side selector writer installed by the deb package.
- Create: `scripts/build-kernel-deb.sh`
  - Build a versioned `yeet-vm-kernel` deb from `vmlinux`, `kernel.config`, and metadata.
- Create: `scripts/publish-apt-repo.sh`
  - Generate apt repository indexes and sign Release metadata.
- Create: `kernel-packages/yeet-kernel-package.nix`
  - Nix package for the yeet kernel artifacts and selector manifest.
- Create: `kernel-packages/flake.nix`
  - Public flake entrypoint for NixOS users.
- Create: `kernel-packages/metadata.nix`
  - Generated Nix metadata pointing at published kernel artifacts.
- Create: `.github/workflows/publish-kernel-packages.yml`
  - Build and publish package sources after Phase 1 kernel artifacts are available.
- Modify: `README.md`
  - Guest opt-in documentation for Ubuntu and NixOS.

## Guest Kernel Selector Contract

Package sources write a data-only selector at:

```text
/etc/yeet-vm/kernel/selected.json
```

The selector schema is:

```json
{
  "schema_version": 1,
  "version": "linux-7.1.1-yeet",
  "kernel": "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
  "kernel_config": "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
  "sha256": {
    "vmlinux": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "kernel.config": "1111111111111111111111111111111111111111111111111111111111111111"
  }
}
```

The selector does not replace the VM image manifest. It is an opt-in guest
request that catch validates before it changes the host-side Firecracker kernel
path.

## Task 1: Add Catch Selector Parsing

**Files:**
- Create: `/Users/shayne/code/yeet/pkg/catch/vm_kernel_selection.go`
- Create: `/Users/shayne/code/yeet/pkg/catch/vm_kernel_selection_test.go`

- [ ] **Step 1: Write selector validation tests**

Create `/Users/shayne/code/yeet/pkg/catch/vm_kernel_selection_test.go`:

```go
package catch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVMGuestKernelSelectionValidates(t *testing.T) {
	raw := []byte(`{
		"schema_version":1,
		"version":"linux-7.1.1-yeet",
		"kernel":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":{
			"vmlinux":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"kernel.config":"1111111111111111111111111111111111111111111111111111111111111111"
		}
	}`)
	var selection vmGuestKernelSelection
	if err := json.Unmarshal(raw, &selection); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := selection.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestVMGuestKernelSelectionRejectsUnsafePaths(t *testing.T) {
	selection := vmGuestKernelSelection{
		SchemaVersion: 1,
		Version:       "linux-7.1.1-yeet",
		Kernel:        "/tmp/vmlinux",
		KernelConfig:  "/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		SHA256: map[string]string{
			"vmlinux":       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"kernel.config": "1111111111111111111111111111111111111111111111111111111111111111",
		},
	}
	err := selection.validate()
	if err == nil || !strings.Contains(err.Error(), "kernel path") {
		t.Fatalf("validate error = %v, want kernel path error", err)
	}
}
```

- [ ] **Step 2: Run the failing selector tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/catch -run 'TestVMGuestKernelSelection' -count=1
```

Expected: fails because `vmGuestKernelSelection` is not defined.

- [ ] **Step 3: Implement selector validation**

Create `/Users/shayne/code/yeet/pkg/catch/vm_kernel_selection.go`:

```go
package catch

import (
	"fmt"
	"regexp"
	"strings"
)

const vmGuestKernelSelectionPath = "/etc/yeet-vm/kernel/selected.json"
const vmGuestKernelPackageRoot = "/usr/lib/yeet-vm/kernels/"

var vmGuestKernelVersionPattern = regexp.MustCompile(`^linux-[0-9]+[.][0-9]+([.][0-9]+)?-yeet$`)
var vmGuestKernelSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type vmGuestKernelSelection struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	Kernel        string            `json:"kernel"`
	KernelConfig  string            `json:"kernel_config,omitempty"`
	SHA256        map[string]string `json:"sha256"`
}

func (s vmGuestKernelSelection) validate() error {
	if s.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM guest kernel selector schema_version %d", s.SchemaVersion)
	}
	if !vmGuestKernelVersionPattern.MatchString(strings.TrimSpace(s.Version)) {
		return fmt.Errorf("invalid VM guest kernel version %q", s.Version)
	}
	if err := validateGuestKernelPackagePath("kernel path", s.Kernel); err != nil {
		return err
	}
	if strings.TrimSpace(s.KernelConfig) != "" {
		if err := validateGuestKernelPackagePath("kernel config path", s.KernelConfig); err != nil {
			return err
		}
	}
	if !vmGuestKernelSHA256Pattern.MatchString(s.SHA256["vmlinux"]) {
		return fmt.Errorf("invalid VM guest kernel vmlinux sha256")
	}
	if strings.TrimSpace(s.KernelConfig) != "" && !vmGuestKernelSHA256Pattern.MatchString(s.SHA256["kernel.config"]) {
		return fmt.Errorf("invalid VM guest kernel config sha256")
	}
	return nil
}

func validateGuestKernelPackagePath(label, p string) error {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, vmGuestKernelPackageRoot) || strings.Contains(p, "\x00") || strings.Contains(p, "..") {
		return fmt.Errorf("invalid VM guest kernel %s %q", label, p)
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("invalid VM guest kernel %s %q", label, p)
	}
	return nil
}
```

- [ ] **Step 4: Run selector tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/catch -run 'TestVMGuestKernelSelection' -count=1
```

Expected: selector tests pass.

- [ ] **Step 5: Commit selector parser**

Run:

```bash
cd /Users/shayne/code/yeet
git add pkg/catch/vm_kernel_selection.go pkg/catch/vm_kernel_selection_test.go
git commit -m "vm: parse guest kernel selector"
```

Expected: commit succeeds.

## Task 2: Add Host-Side Kernel Sync Command

**Files:**
- Create: `/Users/shayne/code/yeet/pkg/catch/vm_kernel_sync.go`
- Create: `/Users/shayne/code/yeet/pkg/catch/vm_kernel_sync_test.go`
- Modify: `/Users/shayne/code/yeet/pkg/cli/cli.go`
- Modify: `/Users/shayne/code/yeet/pkg/cli/cli_test.go`
- Modify: `/Users/shayne/code/yeet/cmd/yeet/cli_bridge.go`

- [ ] **Step 1: Write CLI parser test**

Add this test to `/Users/shayne/code/yeet/pkg/cli/cli_test.go`:

```go
func TestParseVMKernelSync(t *testing.T) {
	flags, args, err := ParseVMKernel([]string{"sync", "devbox", "--restart"})
	if err != nil {
		t.Fatalf("ParseVMKernel: %v", err)
	}
	if strings.Join(args, " ") != "sync devbox" {
		t.Fatalf("args = %v, want sync devbox", args)
	}
	if !flags.Restart {
		t.Fatal("Restart = false, want true")
	}
}
```

- [ ] **Step 2: Run the failing CLI test**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/cli -run TestParseVMKernelSync -count=1
```

Expected: fails because `ParseVMKernel` and flags are not defined.

- [ ] **Step 3: Add CLI command and parser**

In `/Users/shayne/code/yeet/pkg/cli/cli.go`, add:

```go
type VMKernelFlags struct {
	Restart bool
}

func ParseVMKernel(args []string) (VMKernelFlags, []string, error) {
	var flags VMKernelFlags
	fs := flag.NewFlagSet("vm kernel", flag.ContinueOnError)
	fs.BoolVar(&flags.Restart, "restart", false, "restart the VM after syncing the selected kernel")
	if err := fs.Parse(args); err != nil {
		return VMKernelFlags{}, nil, err
	}
	remaining := fs.Args()
	if len(remaining) != 2 || remaining[0] != "sync" {
		return VMKernelFlags{}, nil, fmt.Errorf("usage: vm kernel sync <svc> [--restart]")
	}
	return flags, remaining, nil
}
```

Add the command metadata under the existing `vm` command:

```go
"kernel": {
	Name:        "kernel",
	Description: "Manage guest-selected VM kernels",
	Usage:       "vm kernel sync <svc> [--restart]",
	Examples: []string{
		"yeet vm kernel sync devbox",
		"yeet vm kernel sync devbox --restart",
	},
	ArgsSchema: ServiceArgs{},
	FlagsSchema: VMKernelFlags{},
},
```

- [ ] **Step 4: Implement sync behavior test**

Create `/Users/shayne/code/yeet/pkg/catch/vm_kernel_sync_test.go`:

```go
package catch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncGuestSelectedKernelCopiesVerifiedKernel(t *testing.T) {
	root := t.TempDir()
	mountRoot := filepath.Join(root, "mnt")
	if err := os.MkdirAll(filepath.Join(mountRoot, "etc/yeet-vm/kernel"), 0o755); err != nil {
		t.Fatalf("mkdir selector: %v", err)
	}
	kernelDir := filepath.Join(mountRoot, "usr/lib/yeet-vm/kernels/linux-7.1.1-yeet")
	if err := os.MkdirAll(kernelDir, 0o755); err != nil {
		t.Fatalf("mkdir kernel: %v", err)
	}
	kernelBytes := []byte("kernel")
	configBytes := []byte("config")
	if err := os.WriteFile(filepath.Join(kernelDir, "vmlinux"), kernelBytes, 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kernelDir, "kernel.config"), configBytes, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	selector := `{
		"schema_version":1,
		"version":"linux-7.1.1-yeet",
		"kernel":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":{
			"vmlinux":"` + sha256Hex(kernelBytes) + `",
			"kernel.config":"` + sha256Hex(configBytes) + `"
		}
	}`
	if err := os.WriteFile(filepath.Join(mountRoot, "etc/yeet-vm/kernel/selected.json"), []byte(selector), 0o644); err != nil {
		t.Fatalf("write selector: %v", err)
	}

	out, err := syncGuestSelectedKernelFromMountedRoot(context.Background(), root, "devbox", mountRoot)
	if err != nil {
		t.Fatalf("syncGuestSelectedKernelFromMountedRoot: %v", err)
	}
	if out.Version != "linux-7.1.1-yeet" {
		t.Fatalf("version = %q, want linux-7.1.1-yeet", out.Version)
	}
	if !strings.HasSuffix(out.HostKernelPath, "/devbox/linux-7.1.1-yeet/vmlinux") {
		t.Fatalf("host kernel path = %q", out.HostKernelPath)
	}
	if _, err := os.Stat(out.HostKernelPath); err != nil {
		t.Fatalf("stat copied kernel: %v", err)
	}
}
```

- [ ] **Step 5: Implement host sync helper**

Create `/Users/shayne/code/yeet/pkg/catch/vm_kernel_sync.go` with:

```go
package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type vmKernelSyncResult struct {
	Version        string
	HostKernelPath string
	HostConfigPath string
}

func syncGuestSelectedKernelFromMountedRoot(_ context.Context, serviceRoot, service, mountRoot string) (vmKernelSyncResult, error) {
	raw, err := os.ReadFile(filepath.Join(mountRoot, strings.TrimPrefix(vmGuestKernelSelectionPath, "/")))
	if err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("read guest kernel selector: %w", err)
	}
	var selection vmGuestKernelSelection
	if err := json.Unmarshal(raw, &selection); err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("decode guest kernel selector: %w", err)
	}
	if err := selection.validate(); err != nil {
		return vmKernelSyncResult{}, err
	}

	srcKernel := filepath.Join(mountRoot, strings.TrimPrefix(selection.Kernel, "/"))
	if err := verifyFileSHA256(srcKernel, selection.SHA256["vmlinux"]); err != nil {
		return vmKernelSyncResult{}, err
	}
	dstDir := filepath.Join(serviceRunDirForRoot(serviceRoot), "kernels", service, selection.Version)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return vmKernelSyncResult{}, fmt.Errorf("create host kernel cache: %w", err)
	}
	dstKernel := filepath.Join(dstDir, "vmlinux")
	if err := copyFileMode(srcKernel, dstKernel, 0o644); err != nil {
		return vmKernelSyncResult{}, err
	}

	var dstConfig string
	if strings.TrimSpace(selection.KernelConfig) != "" {
		srcConfig := filepath.Join(mountRoot, strings.TrimPrefix(selection.KernelConfig, "/"))
		if err := verifyFileSHA256(srcConfig, selection.SHA256["kernel.config"]); err != nil {
			return vmKernelSyncResult{}, err
		}
		dstConfig = filepath.Join(dstDir, "kernel.config")
		if err := copyFileMode(srcConfig, dstConfig, 0o644); err != nil {
			return vmKernelSyncResult{}, err
		}
	}
	return vmKernelSyncResult{Version: selection.Version, HostKernelPath: dstKernel, HostConfigPath: dstConfig}, nil
}

func verifyFileSHA256(path, want string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s", path, got, want)
	}
	return nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, raw, mode); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
```

Add `sha256Hex` test helper to `vm_kernel_sync_test.go`:

```go
func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
```

Import `crypto/sha256` and `encoding/hex` in the test file.

- [ ] **Step 6: Wire CLI routing**

Route `yeet vm kernel sync <svc>` in `/Users/shayne/code/yeet/cmd/yeet/cli_bridge.go` to a new catch method that mounts the VM rootfs, calls `syncGuestSelectedKernelFromMountedRoot`, rewrites `firecracker.json`, and restarts when `--restart` is set.

The generated Firecracker config must replace only:

```json
"kernel_image_path": "<host kernel cache path>"
```

It must preserve boot args, rootfs drive, network interfaces, machine config, and vsock.

- [ ] **Step 7: Run targeted tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/cli -run TestParseVMKernelSync -count=1
go test ./pkg/catch -run 'TestVMGuestKernelSelection|TestSyncGuestSelectedKernel' -count=1
```

Expected: tests pass.

- [ ] **Step 8: Commit host-side sync**

Run:

```bash
cd /Users/shayne/code/yeet
git add pkg/catch/vm_kernel_selection.go pkg/catch/vm_kernel_selection_test.go pkg/catch/vm_kernel_sync.go pkg/catch/vm_kernel_sync_test.go pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge.go
git commit -m "vm: sync guest-selected kernel"
```

Expected: commit succeeds.

## Task 3: Build Ubuntu Kernel Deb Package

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/packages/kernel/deb/DEBIAN/control.in`
- Create: `/Users/shayne/code/yeet-vm-images/packages/kernel/deb/usr/lib/yeet-vm-kernel/select-kernel`
- Create: `/Users/shayne/code/yeet-vm-images/scripts/build-kernel-deb.sh`

- [ ] **Step 1: Add Debian control template**

Create `/Users/shayne/code/yeet-vm-images/packages/kernel/deb/DEBIAN/control.in`:

```text
Package: yeet-vm-kernel
Version: @DEB_VERSION@
Section: kernel
Priority: optional
Architecture: amd64
Maintainer: yeet <maintainers@yeetrun.com>
Description: yeet Firecracker VM kernel artifact
 This package installs a yeet-managed Firecracker kernel artifact and writes a
 data-only selector that catch can use to boot the VM with this kernel on the
 next host-side kernel sync.
```

- [ ] **Step 2: Add guest selector script**

Create `/Users/shayne/code/yeet-vm-images/packages/kernel/deb/usr/lib/yeet-vm-kernel/select-kernel`:

```bash
#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [ -z "$version" ]; then
	echo "usage: select-kernel <linux-version-yeet>" >&2
	exit 2
fi

kernel_dir="/usr/lib/yeet-vm/kernels/$version"
kernel="$kernel_dir/vmlinux"
config="$kernel_dir/kernel.config"
selector_dir="/etc/yeet-vm/kernel"
selector="$selector_dir/selected.json"

if [ ! -r "$kernel" ]; then
	echo "missing kernel artifact: $kernel" >&2
	exit 1
fi
if [ ! -r "$config" ]; then
	echo "missing kernel config artifact: $config" >&2
	exit 1
fi

install -d -m 0755 "$selector_dir"
kernel_sha="$(sha256sum "$kernel" | awk '{ print $1 }')"
config_sha="$(sha256sum "$config" | awk '{ print $1 }')"
tmp="$(mktemp "$selector_dir/.selected.json.XXXXXX")"
cat >"$tmp" <<JSON
{
  "schema_version": 1,
  "version": "$version",
  "kernel": "$kernel",
  "kernel_config": "$config",
  "sha256": {
    "vmlinux": "$kernel_sha",
    "kernel.config": "$config_sha"
  }
}
JSON
chmod 0644 "$tmp"
mv "$tmp" "$selector"
```

- [ ] **Step 3: Add deb builder script**

Create `/Users/shayne/code/yeet-vm-images/scripts/build-kernel-deb.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

kernel_dir="${1:-}"
out_dir="${2:-dist/kernel-packages/deb}"
version="${YEET_VM_KERNEL_VERSION:-}"

if [ -z "$kernel_dir" ] || [ -z "$version" ]; then
	echo "usage: YEET_VM_KERNEL_VERSION=linux-X.Y.Z-yeet scripts/build-kernel-deb.sh <kernel-dir> [out-dir]" >&2
	exit 2
fi

for cmd in dpkg-deb install sed sha256sum; do
	if ! command -v "$cmd" >/dev/null 2>&1; then
		echo "missing required command: $cmd" >&2
		exit 1
	fi
done

work_dir="$(mktemp -d)"
cleanup() {
	rm -rf "$work_dir"
}
trap cleanup EXIT

pkg_root="$work_dir/yeet-vm-kernel"
install -d -m 0755 "$pkg_root/DEBIAN"
install -d -m 0755 "$pkg_root/usr/lib/yeet-vm/kernels/$version"
install -d -m 0755 "$pkg_root/usr/lib/yeet-vm-kernel"

deb_version="${version#linux-}"
deb_version="${deb_version%-yeet}"
sed "s/@DEB_VERSION@/$deb_version/" packages/kernel/deb/DEBIAN/control.in >"$pkg_root/DEBIAN/control"
install -m 0755 packages/kernel/deb/usr/lib/yeet-vm-kernel/select-kernel "$pkg_root/usr/lib/yeet-vm-kernel/select-kernel"
install -m 0644 "$kernel_dir/vmlinux" "$pkg_root/usr/lib/yeet-vm/kernels/$version/vmlinux"
install -m 0644 "$kernel_dir/kernel.config" "$pkg_root/usr/lib/yeet-vm/kernels/$version/kernel.config"

cat >"$pkg_root/DEBIAN/postinst" <<POSTINST
#!/usr/bin/env bash
set -euo pipefail
/usr/lib/yeet-vm-kernel/select-kernel "$version"
POSTINST
chmod 0755 "$pkg_root/DEBIAN/postinst"

mkdir -p "$out_dir"
dpkg-deb --build --root-owner-group "$pkg_root" "$out_dir/yeet-vm-kernel_${deb_version}_amd64.deb"
sha256sum "$out_dir/yeet-vm-kernel_${deb_version}_amd64.deb" >"$out_dir/yeet-vm-kernel_${deb_version}_amd64.deb.sha256"
```

- [ ] **Step 4: Run syntax checks**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
chmod +x packages/kernel/deb/usr/lib/yeet-vm-kernel/select-kernel scripts/build-kernel-deb.sh
bash -n packages/kernel/deb/usr/lib/yeet-vm-kernel/select-kernel scripts/build-kernel-deb.sh
```

Expected: syntax checks pass.

- [ ] **Step 5: Commit deb package builder**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add packages/kernel/deb scripts/build-kernel-deb.sh
git commit -m "packages: build Ubuntu kernel deb"
```

Expected: commit succeeds.

## Task 4: Publish Apt Repository

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/scripts/publish-apt-repo.sh`
- Create: `/Users/shayne/code/yeet-vm-images/.github/workflows/publish-kernel-packages.yml`

- [ ] **Step 1: Add apt repository publisher**

Create `/Users/shayne/code/yeet-vm-images/scripts/publish-apt-repo.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

deb_dir="${1:-dist/kernel-packages/deb}"
repo_dir="${2:-dist/kernel-packages/apt}"
suite="${YEET_APT_SUITE:-stable}"
component="${YEET_APT_COMPONENT:-main}"
arch="${YEET_APT_ARCH:-amd64}"

for cmd in apt-ftparchive gpg install mkdir; do
	if ! command -v "$cmd" >/dev/null 2>&1; then
		echo "missing required command: $cmd" >&2
		exit 1
	fi
done

pool_dir="$repo_dir/pool/$component"
binary_dir="$repo_dir/dists/$suite/$component/binary-$arch"
install -d -m 0755 "$pool_dir" "$binary_dir"
install -m 0644 "$deb_dir"/*.deb "$pool_dir/"

apt-ftparchive packages "$pool_dir" >"$binary_dir/Packages"
gzip -kf "$binary_dir/Packages"
apt-ftparchive release "$repo_dir/dists/$suite" >"$repo_dir/dists/$suite/Release"

if [ -n "${YEET_APT_GPG_KEY_ID:-}" ]; then
	gpg --batch --yes --armor --detach-sign \
		--local-user "$YEET_APT_GPG_KEY_ID" \
		-o "$repo_dir/dists/$suite/Release.gpg" \
		"$repo_dir/dists/$suite/Release"
	gpg --batch --yes --clearsign \
		--local-user "$YEET_APT_GPG_KEY_ID" \
		-o "$repo_dir/dists/$suite/InRelease" \
		"$repo_dir/dists/$suite/Release"
fi
```

- [ ] **Step 2: Add package workflow**

Create `/Users/shayne/code/yeet-vm-images/.github/workflows/publish-kernel-packages.yml`:

```yaml
name: Publish kernel package sources

on:
  workflow_dispatch:
    inputs:
      kernel_version:
        description: Upstream Linux kernel version to package.
        required: true
      kernel_source_url:
        description: Linux kernel source tarball URL.
        required: true
      kernel_source_sha256:
        description: Linux kernel source tarball SHA-256.
        required: true

permissions:
  contents: write
  pages: write
  id-token: write

jobs:
  packages:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - name: Install dependencies
        run: |
          sudo apt-get update
          sudo apt-get install -y apt-utils bc bison build-essential curl dpkg-dev dwarves flex gpg jq libelf-dev libssl-dev xz-utils
      - name: Build kernel
        env:
          YEET_KERNEL_VERSION: ${{ inputs.kernel_version }}
          YEET_KERNEL_SOURCE_URL: ${{ inputs.kernel_source_url }}
          YEET_KERNEL_SOURCE_SHA256: ${{ inputs.kernel_source_sha256 }}
        run: scripts/build-linux-kernel.sh "dist/kernel-linux-${{ inputs.kernel_version }}"
      - name: Build deb
        env:
          YEET_VM_KERNEL_VERSION: linux-${{ inputs.kernel_version }}-yeet
        run: scripts/build-kernel-deb.sh "dist/kernel-linux-${{ inputs.kernel_version }}"
      - name: Publish apt repo
        run: scripts/publish-apt-repo.sh
      - name: Prepare Pages artifact
        run: |
          mkdir -p dist/kernel-packages/site/apt
          cp -a dist/kernel-packages/apt/. dist/kernel-packages/site/apt/
      - name: Configure Pages
        uses: actions/configure-pages@v5
      - name: Upload Pages artifact
        uses: actions/upload-pages-artifact@v3
        with:
          path: dist/kernel-packages/site

  deploy:
    runs-on: ubuntu-24.04
    needs: packages
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - name: Deploy Pages
        id: deployment
        uses: actions/deploy-pages@v4
```

- [ ] **Step 3: Run syntax checks**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
chmod +x scripts/publish-apt-repo.sh
bash -n scripts/publish-apt-repo.sh
ruby -e 'require "yaml"; YAML.load_file(".github/workflows/publish-kernel-packages.yml"); puts "ok"'
```

Expected:

```text
ok
```

- [ ] **Step 4: Commit apt publisher**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add scripts/publish-apt-repo.sh .github/workflows/publish-kernel-packages.yml
git commit -m "packages: publish apt kernel repository"
```

Expected: commit succeeds.

## Task 5: Publish Nix Package Source

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/nix/yeet-kernel-package.nix`
- Create: `/Users/shayne/code/yeet-vm-images/kernel-packages/flake.nix`

- [ ] **Step 1: Add Nix kernel package**

Create `/Users/shayne/code/yeet-vm-images/nix/yeet-kernel-package.nix`:

```nix
{ stdenvNoCC
, lib
, kernelVersion
, vmlinux
, kernelConfig
, vmlinuxSha256Raw
, kernelConfigSha256Raw
}:

stdenvNoCC.mkDerivation {
  pname = "yeet-vm-kernel";
  version = kernelVersion;

  dontUnpack = true;

  installPhase = ''
    runHook preInstall
    install -D -m0644 ${vmlinux} $out/lib/yeet-vm/kernels/linux-${kernelVersion}-yeet/vmlinux
    install -D -m0644 ${kernelConfig} $out/lib/yeet-vm/kernels/linux-${kernelVersion}-yeet/kernel.config
    install -D -m0644 /dev/stdin $out/share/yeet-vm/kernel/selected.json <<JSON
    {
      "schema_version": 1,
      "version": "linux-${kernelVersion}-yeet",
      "kernel": "$out/lib/yeet-vm/kernels/linux-${kernelVersion}-yeet/vmlinux",
      "kernel_config": "$out/lib/yeet-vm/kernels/linux-${kernelVersion}-yeet/kernel.config",
      "sha256": {
        "vmlinux": "${vmlinuxSha256Raw}",
        "kernel.config": "${kernelConfigSha256Raw}"
      }
    }
    JSON
    runHook postInstall
  '';

  meta = {
    description = "yeet Firecracker VM kernel artifact";
    platforms = lib.platforms.linux;
  };
}
```

- [ ] **Step 2: Add public flake entrypoint**

Create `/Users/shayne/code/yeet-vm-images/kernel-packages/metadata.nix`:

```nix
{
  kernelVersion = "7.1.1";
  vmlinuxUrl = "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-kernel-7.1.1-v16/vmlinux";
  kernelConfigUrl = "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-kernel-7.1.1-v16/kernel.config";
  vmlinuxHash = "sha256-ASNFZ4mrze8BI0VniavN7wEjRWeJq83vASNFZ4mrze8=";
  kernelConfigHash = "sha256-ERERERERERERERERERERERERERERERERERERERERERE=";
  vmlinuxSha256Raw = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
  kernelConfigSha256Raw = "1111111111111111111111111111111111111111111111111111111111111111";
}
```

The package workflow rewrites this generated metadata file before publishing the
flake source to the package site.

Create `/Users/shayne/code/yeet-vm-images/kernel-packages/flake.nix` with a package and NixOS module that exposes the selector under `/etc/yeet-vm/kernel/selected.json`:

```nix
{
  description = "yeet VM kernel package source";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
      metadata = import ./metadata.nix;
      vmlinux = pkgs.fetchurl {
        url = metadata.vmlinuxUrl;
        hash = metadata.vmlinuxHash;
      };
      kernelConfig = pkgs.fetchurl {
        url = metadata.kernelConfigUrl;
        hash = metadata.kernelConfigHash;
      };
      kernelPackage = pkgs.callPackage ../nix/yeet-kernel-package.nix {
        inherit vmlinux kernelConfig;
        inherit (metadata) kernelVersion vmlinuxSha256Raw kernelConfigSha256Raw;
      };
    in
    {
      packages.${system}.default = kernelPackage;
      nixosModules.default = moduleArgs:
        let
          inherit (moduleArgs) config lib;
        in
        {
          options.services.yeetVmKernel.enable = lib.mkEnableOption "yeet VM kernel selector";
          config = lib.mkIf config.services.yeetVmKernel.enable {
            environment.systemPackages = [ kernelPackage ];
            environment.etc."yeet-vm/kernel/selected.json".source =
              "${kernelPackage}/share/yeet-vm/kernel/selected.json";
          };
        };
    };
}
```

- [ ] **Step 3: Run Nix parse check**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
nix --extra-experimental-features "nix-command flakes" flake show ./kernel-packages
```

Expected: flake output lists `packages.x86_64-linux.default` and `nixosModules.default`.

- [ ] **Step 4: Commit Nix package source**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add nix/yeet-kernel-package.nix kernel-packages/flake.nix kernel-packages/metadata.nix
git commit -m "packages: expose kernel package flake"
```

Expected: commit succeeds.

## Task 6: Document Guest Opt-In

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/docs/kernel-packages.md`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`

- [ ] **Step 1: Add package docs**

Create `/Users/shayne/code/yeet-vm-images/docs/kernel-packages.md`:

````markdown
# Yeet VM Kernel Packages

Yeet VM image bundles still provide the boot kernel by default. Kernel packages
are an opt-in path for existing VMs that want to install a newer yeet-managed
kernel artifact through normal guest package tools and then ask catch to use it
on the next boot.

## Ubuntu

Add the apt source published by this repository, install `yeet-vm-kernel`, then
run the host-side sync command:

```bash
sudo apt update
sudo apt install yeet-vm-kernel
```

From the host with access to catch:

```bash
yeet vm kernel sync <svc> --restart
```

## NixOS

Add the yeet kernel flake as an input, enable the module, rebuild, then run the
host-side sync command:

```nix
{
  inputs.yeet-vm-kernel.url = "github:yeetrun/yeet-vm-images?dir=kernel-packages";
}
```

```nix
{
  imports = [ inputs.yeet-vm-kernel.nixosModules.default ];
  services.yeetVmKernel.enable = true;
}
```

From the host with access to catch:

```bash
yeet vm kernel sync <svc> --restart
```
````

- [ ] **Step 2: Link docs from README**

Add this paragraph to `/Users/shayne/code/yeet-vm-images/README.md`:

```markdown
Kernel package sources for existing guests are documented in
`docs/kernel-packages.md`. Package installation alone does not change the
Firecracker boot kernel; use `yeet vm kernel sync <svc> --restart` after the
guest installs or rebuilds with a new yeet kernel package.
```

- [ ] **Step 3: Commit docs**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add docs/kernel-packages.md README.md
git commit -m "docs: describe guest kernel packages"
```

Expected: commit succeeds.

## Task 7: Full Phase 2 Verification

**Files:**
- No planned file edits unless verification exposes a defect.

- [ ] **Step 1: Run local tests**

Run:

```bash
cd /Users/shayne/code/yeet
go test ./pkg/cli ./pkg/catch -count=1
cd /Users/shayne/code/yeet-vm-images
bash -n scripts/build-kernel-deb.sh scripts/publish-apt-repo.sh packages/kernel/deb/usr/lib/yeet-vm-kernel/select-kernel
```

Expected: all tests and syntax checks pass.

- [ ] **Step 2: Build a local deb from existing kernel artifacts**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
scripts/build-linux-kernel.sh dist/kernel-linux-package-smoke
YEET_VM_KERNEL_VERSION=linux-7.1.1-yeet scripts/build-kernel-deb.sh dist/kernel-linux-package-smoke dist/kernel-packages/deb
dpkg-deb --info dist/kernel-packages/deb/yeet-vm-kernel_7.1.1_amd64.deb
dpkg-deb --contents dist/kernel-packages/deb/yeet-vm-kernel_7.1.1_amd64.deb | grep /usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux
```

Expected: deb metadata is readable and contents include the kernel artifact.

- [ ] **Step 3: Build apt repository indexes**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
scripts/publish-apt-repo.sh dist/kernel-packages/deb dist/kernel-packages/apt
test -s dist/kernel-packages/apt/dists/stable/main/binary-amd64/Packages
test -s dist/kernel-packages/apt/dists/stable/Release
```

Expected: repository indexes exist.

- [ ] **Step 4: Verify Nix package source**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
nix --extra-experimental-features "nix-command flakes" flake show ./kernel-packages
```

Expected: flake exposes the package and module.

- [ ] **Step 5: Verify inside live Ubuntu and NixOS guests**

Run after package sources are published:

```bash
cd /Users/shayne/code/yeet
CATCH_HOST=yeet-lab go run ./cmd/yeet run ubuntu-kernel-package-smoke vm://ubuntu/26.04 --image-policy=update
CATCH_HOST=yeet-lab go run ./cmd/yeet run nixos-kernel-package-smoke vm://nixos/26.05 --image-policy=update
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh ubuntu-kernel-package-smoke -- 'sudo apt update && sudo apt install -y yeet-vm-kernel && test -s /etc/yeet-vm/kernel/selected.json'
CATCH_HOST=yeet-lab go run ./cmd/yeet vm kernel sync ubuntu-kernel-package-smoke --restart
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh ubuntu-kernel-package-smoke -- 'uname -r'
```

For NixOS, add the package flake to `/etc/nixos/configuration.nix`, rebuild, then run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet vm kernel sync nixos-kernel-package-smoke --restart
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh nixos-kernel-package-smoke -- 'uname -r && test -s /etc/yeet-vm/kernel/selected.json'
```

Expected: both guests reboot with the selected yeet kernel reported by `uname -r`.

- [ ] **Step 6: Clean up smoke VMs**

Run:

```bash
cd /Users/shayne/code/yeet
CATCH_HOST=yeet-lab go run ./cmd/yeet rm ubuntu-kernel-package-smoke --clean-data
CATCH_HOST=yeet-lab go run ./cmd/yeet rm nixos-kernel-package-smoke --clean-data
```

Expected: smoke VMs and data are removed.
