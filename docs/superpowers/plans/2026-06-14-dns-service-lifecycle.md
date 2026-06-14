# DNS Service Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure future `catch` upgrades also refresh the catch-owned DNS server without requiring manual restarts.

**Architecture:** `yeet-dns.service` remains a separate systemd unit that runs the same `catch` binary with the `dns` subcommand. The DNS unit becomes part of the catch lifecycle with `PartOf=catch.service`, while catch startup keeps reconciling the unit idempotently: install, enable, start if inactive, and restart only when the generated unit changes.

**Tech Stack:** Go, systemd unit rendering, catch installer helpers, live host validation through `yeet init` and SSH.

---

### Task 1: Add Lifecycle Tests

**Files:**
- Modify: `pkg/catch/dns_service_test.go`
- Modify: `pkg/svc/systemd_test.go`

- [ ] **Step 1: Add a failing DNS unit dependency assertion**

Update `TestNewYeetDNSUnitRunsCatchDNSCommand` in `pkg/catch/dns_service_test.go` so the expected fragments include:

```go
"PartOf=catch.service\n",
```

Run: `mise exec -- go test ./pkg/catch -run 'TestNewYeetDNSUnitRunsCatchDNSCommand' -count=1`

Expected: FAIL because `PartOf=catch.service` is not rendered yet.

- [ ] **Step 2: Add a failing changed-active restart test**

Add this test to `pkg/catch/dns_service_test.go`:

```go
func TestInstallYeetDNSServiceRestartsChangedActiveService(t *testing.T) {
	systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-dns.service")
	var systemctlCalls [][]string
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: systemdPath,
		unitActive:  func(string) bool { return true },
		systemctl: func(args ...string) error {
			systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
			return nil
		},
	})

	if err := installYeetDNSService("/srv/yeet"); err != nil {
		t.Fatalf("installYeetDNSService: %v", err)
	}

	wantCalls := [][]string{
		{"daemon-reload"},
		{"enable", "yeet-dns.service"},
		{"try-restart", "yeet-dns.service"},
	}
	if !reflect.DeepEqual(systemctlCalls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, wantCalls)
	}
}
```

Run: `mise exec -- go test ./pkg/catch -run 'TestInstallYeetDNSServiceRestartsChangedActiveService' -count=1`

Expected: FAIL because active changed services are currently preserved without restart.

- [ ] **Step 3: Replace the old active-service preservation test with unchanged-active behavior**

Change `TestInstallYeetDNSServicePreservesActiveService` so it first writes the same generated unit to `systemdPath`, then expects only enable:

```go
func TestInstallYeetDNSServicePreservesUnchangedActiveService(t *testing.T) {
	systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-dns.service")
	unitFiles, err := newYeetDNSUnit("/usr/local/bin/catch", "/srv/yeet").WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles: %v", err)
	}
	unitRaw, err := os.ReadFile(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("read generated unit: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(systemdPath), 0o755); err != nil {
		t.Fatalf("create systemd dir: %v", err)
	}
	if err := os.WriteFile(systemdPath, unitRaw, 0o644); err != nil {
		t.Fatalf("write installed unit: %v", err)
	}

	var systemctlCalls [][]string
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: systemdPath,
		unitActive:  func(string) bool { return true },
		systemctl: func(args ...string) error {
			systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
			return nil
		},
	})

	if err := installYeetDNSService("/srv/yeet"); err != nil {
		t.Fatalf("installYeetDNSService: %v", err)
	}

	wantCalls := [][]string{
		{"enable", "yeet-dns.service"},
	}
	if !reflect.DeepEqual(systemctlCalls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, wantCalls)
	}
}
```

Run: `mise exec -- go test ./pkg/catch -run 'TestInstallYeetDNSServicePreservesUnchangedActiveService' -count=1`

Expected: FAIL until the early return is removed and unchanged active units still run `enable`.

### Task 2: Implement Systemd Lifecycle Coupling

**Files:**
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/catch/dns_service.go`
- Modify: `pkg/catch/dns_service_test.go`

- [ ] **Step 1: Add `PartOf` support to the shared systemd unit renderer**

Add a `PartOf string` field to `svc.SystemdUnit` and render it in the `[Unit]` section:

```go
{{if .PartOf}}PartOf={{.PartOf}}{{end}}
```

Set `PartOf: "catch.service"` in `newYeetDNSUnit`.

Run: `mise exec -- go test ./pkg/catch -run 'TestNewYeetDNSUnitRunsCatchDNSCommand' -count=1`

Expected: PASS.

- [ ] **Step 2: Restart active DNS only when the installed unit changed**

Remove the early `if !changed && alreadyActive { return nil }` branch from `installYeetDNSService`. In `installGeneratedYeetDNSService`, after `enable`, use this flow:

```go
if alreadyActive {
	if changed {
		if err := catchSystemctl("try-restart", "yeet-dns.service"); err != nil {
			return fmt.Errorf("failed to restart yeet-dns service: %w", err)
		}
	}
	return nil
}
if err := catchSystemctl("start", "yeet-dns.service"); err != nil {
	return fmt.Errorf("failed to start yeet-dns service: %w", err)
}
return nil
```

Run: `mise exec -- go test ./pkg/catch -run 'TestInstallYeetDNSService(RestartsChangedActiveService|PreservesUnchangedActiveService|InstallsAndStarts|PropagatesStartErrors)' -count=1`

Expected: PASS.

### Task 3: Verify Locally

**Files:**
- Test: `pkg/catch`
- Test: `pkg/svc`

- [ ] **Step 1: Format changed Go files**

Run: `gofmt -w pkg/catch/dns_service.go pkg/catch/dns_service_test.go pkg/svc/systemd.go pkg/svc/systemd_test.go`

Expected: no output.

- [ ] **Step 2: Run targeted package tests**

Run: `mise exec -- go test ./pkg/svc ./pkg/catch -count=1`

Expected: PASS for both packages.

- [ ] **Step 3: Run the full Go suite**

Run: `mise exec -- go test ./... -count=1`

Expected: PASS.

### Task 4: Deploy And Surgically Repair Live Hosts

**Files:**
- Runtime-only changes on `root@hetz`
- Runtime-only changes on `root@pve1`

- [ ] **Step 1: Install updated catch on both hosts**

Run:

```bash
CATCH_HOST=yeet-hetz mise exec -- go run ./cmd/yeet init root@hetz
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
```

Expected: both installs complete successfully.

- [ ] **Step 2: Verify unit files contain catch lifecycle coupling**

Run:

```bash
ssh root@hetz "grep -E '^(PartOf|Requires|After|ExecStart)=' /etc/systemd/system/yeet-dns.service"
ssh root@pve1 "grep -E '^(PartOf|Requires|After|ExecStart)=' /etc/systemd/system/yeet-dns.service"
```

Expected: both hosts include `PartOf=catch.service`, `Requires=yeet-ns.service`, `After=yeet-ns.service`, and the `catch ... dns` command.

- [ ] **Step 3: Verify restart propagation manually**

Run:

```bash
ssh root@hetz "before=\$(systemctl show -p MainPID --value yeet-dns.service); systemctl restart catch.service; sleep 2; after=\$(systemctl show -p MainPID --value yeet-dns.service); printf 'hetz before=%s after=%s\n' \"\$before\" \"\$after\"; test \"\$before\" != \"\$after\""
ssh root@pve1 "before=\$(systemctl show -p MainPID --value yeet-dns.service); systemctl restart catch.service; sleep 2; after=\$(systemctl show -p MainPID --value yeet-dns.service); printf 'pve1 before=%s after=%s\n' \"\$before\" \"\$after\"; test \"\$before\" != \"\$after\""
```

Expected: DNS PID changes on both hosts, showing `PartOf=catch.service` restart propagation works.

- [ ] **Step 4: Verify service health and DNS resolution**

Run:

```bash
ssh root@hetz "systemctl is-active catch yeet-dns.service && ss -lunp 'sport = :53' && dig @192.168.100.1 beszel A +short && dig @192.168.100.1 uptime-kuma.yeet.internal A +short"
ssh root@pve1 "systemctl is-active catch yeet-dns.service && ss -lunp 'sport = :53' && dig @192.168.100.1 nixbox A +short && dig @192.168.100.1 nixbox.yeet.internal A +short"
```

Expected: both services active, catch listening on `192.168.100.1:53`, and yeet-local names resolve.

### Task 5: Commit Session Changes

**Files:**
- Commit only this plan and the DNS/systemd lifecycle code/tests.

- [ ] **Step 1: Inspect GitButler state**

Run: `but status -fv`

Expected: this session's files appear assignable to `codex/dns-service-lifecycle`; unrelated active-branch files remain separate.

- [ ] **Step 2: Commit with GitButler**

Run the `but commit` command with the change IDs for this session only:

```bash
but commit codex/dns-service-lifecycle -m "catch: refresh dns service with catch upgrades" --changes <ids>
```

Expected: a single session commit is created with no unrelated files included.
