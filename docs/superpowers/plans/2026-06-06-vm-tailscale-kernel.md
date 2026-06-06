# VM Tailscale Kernel Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a Yeet Ubuntu VM image that can install and run services such as Tailscale on the Yeet-managed Firecracker kernel.

**Architecture:** Keep the default fast image as a no-modules microVM image and build required router/networking support into the managed kernel. Add rootfs policy for TUN and IPv4 forwarding, validate image artifacts in CI, then update Yeet's default image version and release notes after live validation.

**Tech Stack:** Go tests, Bash image builders, Linux kernel config, Ubuntu rootfs customization, GitHub Actions, Yeet/catch live VM validation.

---

### Task 1: Root VM Image Tests

**Files:**
- Modify: `tools/vm-image/build_ubuntu_test.go`

- [ ] **Step 1: Add failing tests for v9, rootfs router policy, and kernel config policy**

Add test assertions that require:

```go
`version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v9}"`
"iptables$"
"nftables$"
"99-yeet-vm-router.conf"
"net.ipv4.ip_forward = 1"
"yeet-vm-tun.conf"
"/dev/net/tun"
"CONFIG_TUN"
"CONFIG_NETFILTER"
"CONFIG_IP_NF_IPTABLES"
"CONFIG_IP_NF_NAT"
"CONFIG_NF_TABLES"
```

- [ ] **Step 2: Run the tests and verify they fail**

Run:

```bash
go test ./tools/vm-image -count=1
```

Expected: FAIL because the root builders still default to v8 and do not include the new router/Tailscale policy.

### Task 2: Root Image Builder Changes

**Files:**
- Modify: `tools/vm-image/build-linux-kernel.sh`
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`
- Modify: `tools/vm-image/README.md`

- [ ] **Step 1: Enable built-in kernel networking support**

In `build-linux-kernel.sh`, keep `CONFIG_MODULES=n` and add `scripts/config --enable` entries for TUN, netfilter, conntrack, NAT, iptables compatibility, nftables, and the xtables matches/targets required by common router software.

- [ ] **Step 2: Validate the new kernel config**

Add `require_config CONFIG_* y` assertions for every newly required built-in kernel feature, so a future kernel/config drift fails the image build.

- [ ] **Step 3: Add rootfs router defaults**

In `build-ubuntu-26.04.sh`, default to `ubuntu-26.04-amd64-v9`, keep modules disabled, keep module-loading units masked, add `iptables` and `nftables` to the fast image package set, write `/etc/sysctl.d/99-yeet-vm-router.conf` with IPv4 forwarding enabled, and write a tmpfiles rule for `/dev/net/tun`.

- [ ] **Step 4: Update VM image README**

Document v9, built-in TUN/netfilter/router support, no preinstalled Tailscale, no modules, and the rootfs router defaults.

- [ ] **Step 5: Run root image tests**

Run:

```bash
bash -n tools/vm-image/build-linux-kernel.sh tools/vm-image/build-ubuntu-26.04.sh
go test ./tools/vm-image -count=1
```

Expected: PASS.

### Task 3: External Image Publisher

**Files:**
- Modify: image publisher `scripts/build-linux-kernel.sh`
- Modify: image publisher `scripts/build-ubuntu-26.04.sh`
- Modify: image publisher `.github/workflows/build-ubuntu-26.04.yml`
- Modify: image publisher `README.md`

- [ ] **Step 1: Mirror root image builder changes into the publisher repo**

Copy the updated kernel builder, Ubuntu builder, and README content into the image publisher repo.

- [ ] **Step 2: Update publisher workflow defaults and verification**

Set the workflow default image version to `ubuntu-26.04-amd64-v9` and verify the published `kernel.config` and rootfs contain the new TUN/netfilter/router policy.

- [ ] **Step 3: Validate publisher files**

Run:

```bash
bash -n scripts/build-linux-kernel.sh scripts/build-ubuntu-26.04.sh
ruby -e 'require "yaml"; YAML.load_file(".github/workflows/build-ubuntu-26.04.yml"); puts "yaml ok"'
git diff --check
```

Expected: PASS.

### Task 4: Publish and Live Validate v9

**Files:**
- No code edits expected after this task unless validation finds a bug.

- [ ] **Step 1: Commit and push publisher changes**

Commit the publisher repo changes and push `main`.

- [ ] **Step 2: Run the image workflow**

Dispatch the Ubuntu 26.04 image workflow for `ubuntu-26.04-amd64-v9` using the current Yeet `main` ref.

- [ ] **Step 3: Live validate on lab-host**

Create a disposable VM on lab-host with the v9 image and validate:

```bash
test -e /dev/net/tun
test -e /proc/net/netfilter
iptables -V
iptables -t nat -L -n
nft list tables
sysctl net.ipv4.ip_forward
curl -fsSL https://tailscale.com/install.sh | sh
systemctl restart tailscaled
systemctl is-active tailscaled
```

Expected: the kernel/rootfs surfaces exist, iptables/nft work, IPv4 forwarding is enabled, Tailscale installs, and `tailscaled` remains active.

### Task 5: Yeet Default and Patch Release

**Files:**
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`
- Modify: `website/docs/changelog.mdx`

- [ ] **Step 1: Add failing test for Yeet default v9**

Update `TestDefaultVMImageVersionUsesLatestFastBundle` to expect `ubuntu-26.04-amd64-v9`, then run:

```bash
go test ./pkg/catch -run TestDefaultVMImageVersionUsesLatestFastBundle -count=1
```

Expected: FAIL because the default remains v8.

- [ ] **Step 2: Point Yeet at v9**

Set `defaultVMImageVersion = "ubuntu-26.04-amd64-v9"` and rerun the targeted test.

- [ ] **Step 3: Update changelog**

Add the next patch release with a concise user-facing bullet noting that `vm://ubuntu/26.04` supports Tailscale-style router services on the Yeet-managed kernel.

- [ ] **Step 4: Run release verification**

Run:

```bash
go test ./tools/vm-image ./pkg/catch -count=1
pre-commit run --all-files
git -C website diff --check
```

Expected: PASS.

- [ ] **Step 5: Commit, tag, and push**

Commit website changes in the website repo, commit root changes, create the next patch tag, push `main`, and push the tag.
