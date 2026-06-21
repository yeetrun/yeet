# Auto-Sync Guest Kernel On Reboot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `apt upgrade && sudo reboot` and `nixos-rebuild switch && sudo reboot` boot the guest-selected yeet kernel without requiring `yeet vm kernel sync`.

**Architecture:** Keep the guest package contract data-only: it writes `/etc/yeet-vm/kernel/selected.json`. Catch already detects guest reboot from the VM serial console and exits `vm-run` with systemd's restart-forcing code; before that exit, `vm-run` syncs the selected kernel from the stopped guest rootfs and updates Firecracker config so the next Firecracker process boots it. The explicit `yeet vm kernel sync` command remains as an operator fallback.

**Tech Stack:** Go catch VM runner, Firecracker systemd unit rendering, ext4 rootfs mount helpers, Bash deb postinst helper, NixOS activation script, Go unit tests, shell package tests.

---

## File Structure

- Modify `pkg/catch/vm_console_proxy.go`: add optional reboot callback support to `RunVMConsoleProxy`.
- Modify `cmd/catch/catch.go`: parse internal `vm-run` flags for service root, service name, and disk path; attach the reboot auto-sync hook.
- Modify `pkg/catch/vm_systemd.go`: include internal metadata flags in generated VM systemd units.
- Modify `pkg/catch/vm_kernel_sync.go`: expose a stopped-rootfs auto-sync helper that skips cleanly when no selector exists.
- Modify `pkg/catch/vm_provision.go`: pass the service root and disk path to the systemd unit renderer.
- Modify tests in `pkg/catch` and `cmd/catch`: cover callback timing, generated systemd flags, conservative failure behavior, and selected-kernel sync.
- Modify `../yeet-vm-images/packages/kernel/deb/usr/lib/yeet-vm-kernel/sync-message`: replace manual sync instructions with reboot instructions.
- Modify `../yeet-vm-images/kernel-packages/flake.nix`: replace Nix activation manual sync instructions with reboot instructions.
- Modify `../yeet-vm-images/scripts/test-kernel-packages.sh`: assert reboot guidance and reject normal-path manual sync guidance.
- Modify `../yeet-vm-images/README.md`: document automatic reboot-time sync and keep manual CLI as fallback.

### Task 1: Catch Reboot Hook Tests

**Files:**
- Modify: `pkg/catch/vm_console_test.go`
- Modify: `pkg/catch/vm_systemd_test.go`
- Modify: `pkg/catch/vm_kernel_sync_test.go`
- Modify: `cmd/catch/catch_test.go`

- [x] **Step 1: Write failing tests**

Add tests requiring `VMConsoleProxyConfig` service metadata, an `OnGuestReboot` hook, generated systemd `vm-run` metadata flags, and an exported auto-sync helper.

- [x] **Step 2: Run red tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVMConsoleProxyRunsRebootHookBeforeReturningReboot|TestRunVMConsoleProxyStillReturnsRebootWhenRebootHookFails|TestRenderVMSystemdUnit|TestAutoSyncVMGuestKernelOnReboot' -count=1
mise exec -- go test ./cmd/catch -run TestHandleSpecialCommandVMRun -count=1
```

Expected result observed: build failures for missing `VMConsoleProxyConfig` fields and missing `AutoSyncVMGuestKernelOnReboot`.

### Task 2: Catch Reboot-Time Kernel Sync

**Files:**
- Modify: `pkg/catch/vm_console_proxy.go`
- Modify: `cmd/catch/catch.go`
- Modify: `pkg/catch/vm_systemd.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_kernel_sync.go`

- [x] **Step 1: Add reboot hook plumbing**

Add these fields to `VMConsoleProxyConfig`:

```go
Service       string
ServiceRoot   string
DiskPath      string
OnGuestReboot func(context.Context, VMConsoleProxyConfig) error
```

After `waitVMConsoleProcess` returns `ErrVMGuestReboot`, call `OnGuestReboot` if it is non-nil. Log hook errors to stderr and still return `ErrVMGuestReboot`.

- [x] **Step 2: Wire systemd and `vm-run`**

Add internal `vm-run` flags:

```text
--service <name>
--service-root <path>
--disk-path <path>
```

Render them from the VM provisioning plan and pass them through `cmd/catch` to `VMConsoleProxyConfig`.

- [x] **Step 3: Reuse existing sync logic conservatively**

Add `AutoSyncVMGuestKernelOnReboot(ctx, cfg)` that calls the existing checksum-verified rootfs sync path, updates the Firecracker config, and returns nil when no selector exists.

- [x] **Step 4: Run green focused tests**

Run the focused package tests and expect pass.

### Task 3: Guest Package UX

**Files:**
- Modify: `../yeet-vm-images/packages/kernel/deb/usr/lib/yeet-vm-kernel/sync-message`
- Modify: `../yeet-vm-images/kernel-packages/flake.nix`
- Modify: `../yeet-vm-images/scripts/test-kernel-packages.sh`
- Modify: `../yeet-vm-images/README.md`

- [x] **Step 1: Write failing package tests**

Change package validation to require:

```text
Reboot this VM to boot the selected kernel.
```

and reject `yeet vm kernel sync` in the normal package helper message.

- [x] **Step 2: Run red package test**

Run:

```bash
scripts/test-kernel-packages.sh
```

Expected result observed: fail because the helper still printed the manual sync command.

- [x] **Step 3: Update messages and docs**

Change the deb helper and Nix activation script to tell the user to reboot. Update README to describe reboot-time auto-sync and document `yeet vm kernel sync <service-name> --restart` as fallback.

- [x] **Step 4: Run green package test**

Run:

```bash
scripts/test-kernel-packages.sh
```

Expected result observed: pass.

### Task 4: Verification, Commit, Push, Deploy

**Files:**
- All touched files from Tasks 1-3.

- [x] **Step 1: Format Go code**

Run:

```bash
mise exec -- gofmt -w pkg/catch/vm_console_proxy.go pkg/catch/vm_console_test.go pkg/catch/vm_systemd.go pkg/catch/vm_systemd_test.go pkg/catch/vm_kernel_sync.go pkg/catch/vm_kernel_sync_test.go pkg/catch/vm_provision.go cmd/catch/catch.go cmd/catch/catch_test.go
```

- [x] **Step 2: Focused verification**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -count=1
```

- [x] **Step 3: Full verification**

Run:

```bash
mise exec -- go test ./... -count=1
git diff --check
scripts/test-kernel-packages.sh
nix flake check ./kernel-packages
mise run lint
```

- [ ] **Step 4: Commit and push**

Commit `yeet` and `yeet-vm-images` changes on `main`, push both `origin/main`, and verify local and remote refs match.

- [ ] **Step 5: Deploy catch**

Update catch on both live hosts:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
CATCH_HOST=yeet-hetz mise exec -- go run ./cmd/yeet init root@hetz
```

Verify deployed binary revisions with `go version -m` on copied remote binaries or a trustworthy `yeet version` path.
