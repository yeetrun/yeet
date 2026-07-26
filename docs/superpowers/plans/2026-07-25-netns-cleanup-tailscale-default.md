# Network Namespace Cleanup and Tailscale Default Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove per-service resolver state during namespace teardown and use Tailscale `1.101.284` for new services.

**Architecture:** Keep cleanup in the existing `service-ns` lifecycle script and make its resolver root injectable only for safe behavior tests. Keep Tailscale version selection in `tailscaleNetworkFromOpts`, backed by one named default constant.

**Tech Stack:** Go 1.26, embedded Bash scripts, systemd, Docker, Tailscale, GitButler, GitHub Actions.

## Global Constraints

- Use focused tests while iterating; run full and heavy gates once on the stable release candidate.
- Delete only `resolv.conf` and remove the namespace directory only when empty.
- Preserve explicit Tailscale version overrides.
- Publish `v0.10.10` only after custom-build and official-artifact live verification.

---

### Task 1: Prove namespace resolver cleanup

**Files:**
- Modify: `pkg/netns/netns_test.go`
- Modify: `pkg/netns/netns-scripts/service-ns`

**Interfaces:**
- Consumes: `SERVICE_NAME`, the `cleanup` argument, and `NETNS_ETC_DIR`.
- Produces: idempotent removal of `<NETNS_ETC_DIR>/yeet-<service>-ns/resolv.conf`.

- [ ] **Step 1: Write the failing behavior tests**

Add tests that write the embedded script to a temporary executable, place a
fake successful `ip` command first in `PATH`, and invoke:

```go
cmd := exec.Command(scriptPath, "cleanup")
cmd.Env = append(os.Environ(),
	"SERVICE_NAME=cleanup-test",
	"NETNS_ETC_DIR="+netNSEtcDir,
	"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
)
```

One test requires the resolver directory to disappear. A second places a
`keep` file beside `resolv.conf` and requires `keep` and the directory to
remain while `resolv.conf` disappears.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/netns -run 'TestServiceNSScriptCleanup' -count=1
```

Expected: both tests fail because `resolv.conf` remains.

- [ ] **Step 3: Implement surgical cleanup**

Default `NETNS_ETC_DIR` to `/etc/netns`, use it in resolver setup, and add this
after namespace deletion:

```bash
resolver_dir="$NETNS_ETC_DIR/$NS_NAME"
rm -f "$resolver_dir/resolv.conf"
rmdir "$resolver_dir" 2>/dev/null || true
```

- [ ] **Step 4: Run focused namespace tests and verify GREEN**

```bash
mise exec -- go test ./pkg/netns -count=1
```

### Task 2: Update the new-service Tailscale default

**Files:**
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/installer_file.go`

**Interfaces:**
- Consumes: `TailscaleOpts.Version`.
- Produces: `db.TailscaleNetwork.Version`, defaulting to `1.101.284`.

- [ ] **Step 1: Write the failing version-selection test**

```go
func TestTailscaleNetworkFromOptsUsesCurrentDefault(t *testing.T) {
	got, _ := tailscaleNetworkFromOpts(TailscaleOpts{})
	if got.Version != "1.101.284" {
		t.Fatalf("Version = %q, want %q", got.Version, "1.101.284")
	}
}
```

Include a second assertion that `TailscaleOpts{Version: "1.2.3"}` still
selects `1.2.3`.

- [ ] **Step 2: Run the test and verify RED**

```bash
mise exec -- go test ./pkg/catch -run TestTailscaleNetworkFromOptsUsesCurrentDefault -count=1
```

Expected: failure showing `1.77.33`.

- [ ] **Step 3: Implement the named default**

Define:

```go
const defaultTailscaleVersion = "1.101.284"
```

Use the constant in `tailscaleNetworkFromOpts`, leaving explicit overrides
unchanged.

- [ ] **Step 4: Run focused Catch and service tests**

```bash
mise exec -- go test ./pkg/catch ./pkg/svc -count=1
```

### Task 3: Verify the live host and clean historical remnants

**Files:**
- No repository files.

**Interfaces:**
- Consumes: custom Catch build and a configured Catch host.
- Produces: live proof of cleanup and a host with no proven-stale resolver directories.

- [ ] **Step 1: Install the custom Catch build**

```bash
CATCH_HOST=<catch-host> mise exec -- go run ./cmd/yeet init root@<machine-host>
```

- [ ] **Step 2: Run a disposable lifecycle**

Deploy one unique `nginx:latest` service with `--net=ts,svc`, confirm it uses
Tailscale `1.101.284`, then remove it with `--yes --clean`.

- [ ] **Step 3: Audit the disposable service**

Require absence from Catch, local `yeet.toml`, service root/ZFS, systemd,
Docker, `/run/netns`, `/etc/netns`, Tailscale status, and authoritative
MagicDNS.

- [ ] **Step 4: Remove historical stale resolver directories**

For each candidate, re-check that the service root, systemd unit, active
namespace, and namespace mount are absent and that the directory contains
only `resolv.conf`. Delete that file and remove the now-empty directory.

### Task 4: Run the final release gate

**Files:**
- No repository files unless a gate exposes a defect.

- [ ] **Step 1: Run the full Go suite**

```bash
mise exec -- go test ./... -count=1
```

- [ ] **Step 2: Run pre-commit**

```bash
mise exec -- pre-commit run --all-files
```

- [ ] **Step 3: Run the heavy destination gate once**

```bash
mise run quality:goal
```

### Task 5: Publish and deploy `v0.10.10`

**Files:**
- Modify: `website/docs/changelog.mdx`
- Modify: root `website` gitlink

- [ ] **Step 1: Add the user-facing changelog entry**

Add a `v0.10.10` section dated July 25, 2026 with bullets for complete
network-namespace cleanup and Tailscale `1.101.284` on new services.

- [ ] **Step 2: Commit and push the website repository**

Commit and push the changelog inside `website`, then verify its remote main
advertises the exact commit.

- [ ] **Step 3: Commit and land the root release**

Use GitButler to produce one signed root commit containing implementation,
tests, design/plan, and the website gitlink. Land it directly on current
`origin/main` after `but pull --check`.

- [ ] **Step 4: Tag and publish**

Create and push annotated tag `v0.10.10`, watch Prepare Release and Release
through success, and verify all published checksums and build metadata.

- [ ] **Step 5: Deploy the official Catch artifact**

Use the published `v0.10.10` Yeet client to install the official Catch artifact
on `root@<machine-host>`. Verify the deployed checksum and `vcs.revision`, then repeat
the disposable cleanup audit.
