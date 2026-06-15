# Docker DNS Defaults Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make new catch installs configure Docker containers to inherit catch DNS by default, reject hosts where the fixed `192.168.100.0/24` service subnet conflicts, and verify Docker, yeet-local, public, and tailnet DNS on live hosts.

**Architecture:** Catch keeps its own DNS server at `192.168.100.1`, Docker daemon defaults point containers at that resolver with `yeet.internal` search, and catch DNS becomes the split resolver for yeet names, tailnet names, and public upstreams. Host subnet validation runs before installing catch services that claim the fixed service subnet so operators get a hard error instead of an ambiguous routing conflict.

**Tech Stack:** Go, miekg/dns, Docker daemon `daemon.json`, systemd, yeet live hosts.

---

### Task 1: Docker Daemon DNS Defaults

**Files:**
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

- [x] **Step 1: Add failing tests for daemon DNS config**

Add tests beside `TestWriteContainerdSnapshotterConfig` asserting that install-time Docker config writes `dns: ["192.168.100.1"]` and `dns-search: ["yeet.internal"]`, preserves unrelated keys, and does not report changed when already configured.

- [x] **Step 2: Run focused tests and verify failure**

Run: `mise exec -- go test ./cmd/catch -run 'TestWriteContainerdSnapshotterConfig|TestWriteDockerInstallConfig' -count=1`

Expected: fail because the Docker DNS install helper does not exist or does not set DNS fields.

- [x] **Step 3: Implement a combined install helper**

Replace the single-purpose snapshotter writer with an install config helper that reads `/etc/docker/daemon.json`, ensures `features.containerd-snapshotter=true`, sets missing or different Docker DNS defaults to `192.168.100.1` and `yeet.internal`, writes atomically through the existing JSON writer, and restarts Docker only when the file changed.

- [x] **Step 4: Verify focused tests pass**

Run: `mise exec -- go test ./cmd/catch -run 'TestWriteContainerdSnapshotterConfig|TestWriteDockerInstallConfig|TestEnsureContainerdSnapshotterForInstall' -count=1`

Expected: pass.

### Task 2: Fixed Service Subnet Conflict Guard

**Files:**
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/netns/netns.go`

- [x] **Step 1: Add failing tests for subnet conflict**

Add tests asserting `configureNetwork()` fails when a host route or interface already covers `192.168.100.0/24`, and allows the known yeet-owned service interface once present.

- [x] **Step 2: Run focused tests and verify failure**

Run: `mise exec -- go test ./pkg/catch -run 'TestConfigureNetworkRejectsSvcSubnetConflict|TestConfigureNetworkAllowsExistingYeetSvcSubnet' -count=1`

Expected: fail because no conflict check exists.

- [x] **Step 3: Implement conflict detection**

Add a small catch-side helper that reads `ip -j addr` and `ip -j route`, parses CIDRs with `net/netip`, rejects overlapping non-yeet entries for `192.168.100.0/24`, and runs before writing netns artifacts for services using `svc`.

- [x] **Step 4: Verify focused tests pass**

Run: `mise exec -- go test ./pkg/catch -run 'TestConfigureNetworkRejectsSvcSubnetConflict|TestConfigureNetworkAllowsExistingYeetSvcSubnet' -count=1`

Expected: pass.

### Task 3: Tailnet Split Forwarding

**Files:**
- Modify: `pkg/catch/dns.go`
- Modify: `pkg/catch/dns_test.go`

- [x] **Step 1: Add failing tests for tailnet forwarding**

Add tests showing `*.ts.net` and current tailnet domains route to `100.100.100.100`, while public names keep using host resolver upstreams.

- [x] **Step 2: Run focused tests and verify failure**

Run: `mise exec -- go test ./pkg/catch -run 'TestYeetDNSHandler|TestForwardDNS' -count=1`

Expected: fail because tailnet forwarding is not routed specially.

- [x] **Step 3: Implement split forwarding**

Teach the DNS handler to route `ts.net`, `*.ts.net`, and host resolver search domains ending in `.ts.net` to Tailscale DNS at `100.100.100.100:53`, while leaving yeet-local and public forwarding behavior intact.

- [x] **Step 4: Verify focused tests pass**

Run: `mise exec -- go test ./pkg/catch -run 'TestYeetDNSHandler|TestForwardDNS' -count=1`

Expected: pass.

### Task 4: Docs and Broad Verification

**Files:**
- Modify: `website/docs/concepts/dns.mdx`
- Modify: `website/docs/payloads/containers.mdx`

- [x] **Step 1: Update docs**

Document that Docker hosts configured by `yeet init --install-docker` default containers to catch DNS unless compose explicitly sets DNS, and that `192.168.100.0/24` is required/reserved by v1.

- [x] **Step 2: Run package tests**

Run: `mise exec -- go test ./cmd/catch ./pkg/catch ./pkg/netns -count=1`

Expected: pass.

- [x] **Step 3: Run full tests**

Run: `mise exec -- go test ./... -count=1`

Expected: pass.

- [x] **Step 4: Run docs whitespace check**

Run: `git -C website diff --check`

Expected: no output.

### Task 5: Live Host Deployment and Smoke Tests

**Files:**
- No repo files.

- [x] **Step 1: Patch and restart Docker on both live hosts**

Use `ssh root@pve1` and `ssh root@hetz` to merge the daemon defaults, restart Docker if needed, and verify `/etc/docker/daemon.json` contains `dns` and `dns-search`.

- [x] **Step 2: Install updated catch on both hosts**

Run:
`CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1`
`CATCH_HOST=yeet-hetz mise exec -- go run ./cmd/yeet init root@hetz`

Expected: both complete without subnet conflicts.

- [x] **Step 3: Test disposable svc,ts compose resolution**

Deploy unique disposable compose services on pve1 and hetz without `dns` fields. From inside each container verify short yeet name, `*.yeet.internal`, public DNS, and tailnet DNS all resolve. Remove disposable services with `--clean-data --clean-config`.

- [x] **Step 4: Verify existing services are healthy**

Run `yeet status` for both catch hosts, check Docker for unhealthy/restarting containers, and verify Uptime Kuma active monitors remain green on hetz.
