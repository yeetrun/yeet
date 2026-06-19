# VM Service Gateway Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make yeet VMs use `192.168.100.1` as the service-network gateway and verify existing lab-host VMs are surgically refreshed without adding migration code.

**Architecture:** Keep the existing shared `yeet-ns` bridge topology, but separate the VM guest gateway role from the legacy bridge address role. New VM metadata and boot arguments should use the host/DNS/egress IP as the gateway; `192.168.100.254` remains only as the internal bridge address if the current namespace setup still needs it.

**Tech Stack:** Go, Linux network namespaces, Firecracker VM boot args, injected guest metadata, GitButler, lab-host live host verification.

---

## File Structure

- Modify `pkg/catch/vm_network.go`: rename constants and set service-network VM gateway to `192.168.100.1`.
- Modify `pkg/catch/vm_network_test.go`: update gateway expectations and preserve no-broad-route assertions.
- Modify `pkg/catch/vm_boot_test.go`: update kernel `ip=...` boot arg expectation.
- Modify `pkg/catch/vm_metadata_test.go`: update metadata gateway expectations for Ubuntu, NixOS, and YAML output.
- Modify `pkg/catch/vm_resize_test.go`: update any golden boot-arg or metadata assertions using the old gateway.
- Do not modify `pkg/netns/netns-scripts/yeet-ns` unless tests prove it incorrectly describes the VM guest gateway.

## Task 1: Change the VM Gateway Source of Truth

**Files:**
- Modify: `pkg/catch/vm_network.go`
- Test: `pkg/catch/vm_network_test.go`

- [ ] **Step 1: Write the failing gateway expectation**

In `pkg/catch/vm_network_test.go`, change `TestVMSvcNetworkPlanUsesHostBridgeAndYeetNSPeer` to expect the service gateway constant instead of `192.168.100.254`:

```go
if iface.Gateway != vmSvcGuestGateway {
	t.Fatalf("gateway = %q, want %s", iface.Gateway, vmSvcGuestGateway)
}
if vmSvcGuestGateway != "192.168.100.1" {
	t.Fatalf("vmSvcGuestGateway = %q, want 192.168.100.1", vmSvcGuestGateway)
}
if vmSvcBridgeAddress != "192.168.100.254" {
	t.Fatalf("vmSvcBridgeAddress = %q, want 192.168.100.254", vmSvcBridgeAddress)
}
```

- [ ] **Step 2: Run the targeted test and confirm it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestVMSvcNetworkPlanUsesHostBridgeAndYeetNSPeer -count=1
```

Expected: fail to compile or fail assertions because `vmSvcGuestGateway` and `vmSvcBridgeAddress` do not exist yet.

- [ ] **Step 3: Implement the constant split**

In `pkg/catch/vm_network.go`, replace:

```go
const (
	vmSvcGateway  = "192.168.100.254"
	vmSvcNetNS    = "yeet-ns"
	vmSvcNSBridge = "br0"
)
```

with:

```go
const (
	vmSvcGuestGateway  = "192.168.100.1"
	vmSvcBridgeAddress = "192.168.100.254"
	vmSvcNetNS         = "yeet-ns"
	vmSvcNSBridge      = "br0"
)
```

Then change `configureVMSvcNetworkInterface`:

```go
iface.Gateway = vmSvcGuestGateway
```

Replace cleanup references that target the legacy bridge address:

```go
[]string{"ip", "addr", "del", vmSvcBridgeAddress + "/24", "dev", iface.Bridge},
[]string{"ip", "addr", "del", vmSvcBridgeAddress + "/32", "dev", iface.Bridge},
```

Update `isVMSvcGatewayDeleteCommand` to match the bridge address:

```go
(cmd[3] == vmSvcBridgeAddress+"/24" || cmd[3] == vmSvcBridgeAddress+"/32") &&
```

- [ ] **Step 4: Run the targeted test and confirm it passes**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestVMSvcNetworkPlanUsesHostBridgeAndYeetNSPeer -count=1
```

Expected: PASS.

## Task 2: Update Boot Args and Metadata Expectations

**Files:**
- Modify: `pkg/catch/vm_boot_test.go`
- Modify: `pkg/catch/vm_metadata_test.go`
- Modify: `pkg/catch/vm_resize_test.go`
- Test: `pkg/catch`

- [ ] **Step 1: Update kernel boot arg test**

In `pkg/catch/vm_boot_test.go`, change:

```go
want := "ip=192.168.100.12::192.168.100.254:255.255.255.0:devbox:eth0:none"
```

to:

```go
want := "ip=192.168.100.12::192.168.100.1:255.255.255.0:devbox:eth0:none"
```

- [ ] **Step 2: Update metadata gateway expectations**

In `pkg/catch/vm_metadata_test.go`, replace every service-network gateway expectation of `192.168.100.254` with `192.168.100.1`. The expected strings should include:

```go
"gateway4: 192.168.100.1"
```

and for systemd-networkd metadata:

```go
"Gateway=192.168.100.1\n"
```

Keep DNS expectations as:

```go
"DNS=192.168.100.1\n"
```

- [ ] **Step 3: Update VM resize golden boot arg**

In `pkg/catch/vm_resize_test.go`, replace the old boot-arg gateway string:

```go
ip=192.168.100.12::192.168.100.254:255.255.255.0:devbox:eth0:none
```

with:

```go
ip=192.168.100.12::192.168.100.1:255.255.255.0:devbox:eth0:none
```

- [ ] **Step 4: Run metadata and boot tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'Test(VMKernelBootArgsIncludesStaticSvcIP|RenderVMNetworkYAML|WriteVMGuest|VMResize)' -count=1
```

Expected: PASS. If the regex misses some renamed tests, run the full package test in the next step.

- [ ] **Step 5: Run full catch package tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

## Task 3: Verify Broader Repo Quality and Commit Code

**Files:**
- Modify only the files changed by Tasks 1 and 2.

- [ ] **Step 1: Format touched Go files**

Run:

```bash
mise exec -- gofmt -w pkg/catch/vm_network.go pkg/catch/vm_network_test.go pkg/catch/vm_boot_test.go pkg/catch/vm_metadata_test.go pkg/catch/vm_resize_test.go
```

- [ ] **Step 2: Run the full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Commit the code changes**

Use GitButler:

```bash
but status -fv
```

From the `but status -fv` output, collect the file IDs for these exact files
if they changed:

```text
pkg/catch/vm_network.go
pkg/catch/vm_network_test.go
pkg/catch/vm_boot_test.go
pkg/catch/vm_metadata_test.go
pkg/catch/vm_resize_test.go
```

Then commit those IDs in one comma-separated `--changes` argument, for example
if the IDs are `aa`, `bb`, `cc`, `dd`, and `ee`:

```bash
but commit vm-svc-gateway-cleanup -m "vm: use host service gateway" --changes aa,bb,cc,dd,ee
```

Expected: branch `vm-svc-gateway-cleanup` has the spec commit plus a coherent
code commit, with no unassigned changes.

## Task 4: Deploy Catch and Refresh Existing lab-host VMs

**Files:**
- No repo file changes expected.

- [ ] **Step 1: Install the new catch build on lab-host**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
```

Expected: catch uploads and installs successfully.

- [ ] **Step 2: Inspect current VMs and generated metadata**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet status
ssh root@lab-host 'find /root/.local/share/yeet -path "*metadata/network.yaml" -o -path "*etc/yeet-vm/systemd-network/10-yeet-eth0.network" 2>/dev/null | sort'
```

Expected: identify the VMs that still reference `192.168.100.254`.

- [ ] **Step 3: Surgically update lab-host VM guest network config**

For each affected VM, prefer a yeet-native config regeneration command if one exists for stopped VMs. If not, stop the VM and edit only generated guest network config that contains the old gateway:

```bash
ssh root@lab-host 'grep -R "192.168.100.254" -n /root/.local/share/yeet /var/lib/yeet 2>/dev/null || true'
```

Replace only VM guest gateway entries from `192.168.100.254` to `192.168.100.1`. Do not rewrite unrelated files.

- [ ] **Step 4: Reboot or restart affected VMs**

Use yeet commands when available. First list VM service names:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet status
```

For each affected VM name shown by `status`, run the matching stop/start
commands. If `nixbox` is affected, the commands are:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm stop nixbox
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet vm start nixbox
```

If the CLI names differ, inspect `yeet vm --help` and use the matching
stop/start command for the same affected VM names.

- [ ] **Step 5: Verify guest routes**

From an affected VM such as `nixbox`, run:

```bash
ip route
resolvectl dns || cat /etc/resolv.conf
ping -c 5 google.com
```

Expected: default route uses `192.168.100.1`, DNS includes `192.168.100.1`, and ping succeeds without `Redirect Host`.

## Task 5: Live Service-Network Regression Checks

**Files:**
- No repo file changes expected.

- [ ] **Step 1: Verify service DNS from VM**

Run inside a refreshed VM:

```bash
getent hosts sonarr
```

Expected: `sonarr` resolves to a `192.168.100.x` service-network IP.

- [ ] **Step 2: Repeatedly test curl, tracepath, and ping**

Run inside the VM:

```bash
for i in 1 2 3; do
  curl --max-time 10 -fsS -o /dev/null -w "curl $i code=%{http_code} time=%{time_total}\n" http://sonarr:8989
  tracepath -m 5 sonarr
  ping -c 3 sonarr
done
```

Expected: commands finish without hanging SSH, curl returns a successful HTTP status, tracepath completes or reports a normal terminal path, and ping completes.

- [ ] **Step 3: Check lab-host sidecar and namespace state**

Run:

```bash
ssh root@lab-host 'ip netns exec yeet-ns ip route show; ip route show 192.168.100.0/24; systemctl --no-pager --failed "yeet-*"; true'
```

Expected: route state is coherent and no relevant yeet service units are failed.

## Task 6: Land and Push

**Files:**
- No additional edits expected.

- [ ] **Step 1: Check final state**

Run:

```bash
but status -fv
git log --oneline --decorate --max-count=5
```

Expected: no unassigned changes; branch has the intended spec and code commits.

- [ ] **Step 2: Finish to main and origin/main**

Follow `AGENTS.md` GitButler finish policy. Squash/reword if needed, verify current `origin/main`, then land the session work on local `main` and `origin/main`.

- [ ] **Step 3: Final verification**

Run:

```bash
but status -fv
git ls-remote origin refs/heads/main
```

Expected: GitButler clean and `origin/main` contains the final session commit.
