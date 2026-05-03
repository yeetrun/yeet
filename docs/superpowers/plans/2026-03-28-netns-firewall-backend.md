# NetNS Firewall Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move yeet's `svc` netns firewall management to a backend-aware, yeet-owned model that prefers native `nft`, falls back to `iptables-nft`, and still works on legacy hosts without changing the existing netns topology.

**Architecture:** Keep namespace and routing setup in the existing netns scripts, but move firewall backend detection and yeet-owned rule generation into Go. Expose that logic through a small `catch netns-firewall` helper subcommand so the `yeet-ns` oneshot service can keep running at boot while firewall behavior becomes explicit, testable, and idempotent.

**Tech Stack:** Go, systemd, Linux networking (`iproute2`), `nft`, `iptables-nft` / `iptables-legacy`, shell scripts, website docs.

---

## File Structure

**Files:**
- Create: `pkg/netns/firewall.go`
- Create: `pkg/netns/firewall_test.go`
- Create: `cmd/catch/netns_firewall_cmd.go`
- Create: `cmd/catch/netns_firewall_cmd_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `pkg/netns/netns.go`
- Modify: `pkg/netns/netns-scripts/yeet-ns`
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`

**Responsibilities:**
- `pkg/netns/firewall.go`: backend enum, detection, yeet-owned ruleset rendering, ensure/verify/cleanup entrypoints.
- `pkg/netns/firewall_test.go`: table-driven tests for backend detection and backend-specific rule generation.
- `cmd/catch/netns_firewall_cmd.go`: `catch netns-firewall <ensure|verify|cleanup>` plumbing that calls the Go helper.
- `cmd/catch/netns_firewall_cmd_test.go`: dispatch tests for the helper subcommand.
- `pkg/netns/netns.go`: include firewall helper state in the generated `yeet-ns.env`, including the installed `catch` binary path.
- `pkg/netns/netns-scripts/yeet-ns`: keep namespace/routing setup, but replace bare `iptables` calls with the `catch netns-firewall` helper.
- docs: explain new-host backend preference, compatibility fallback, and operator inspection commands.

### Task 1: Add Failing Tests For Firewall Backend Selection

**Files:**
- Create: `pkg/netns/firewall_test.go`
- Test: `pkg/netns/firewall_test.go`

- [ ] **Step 1: Write the failing backend detection tests**

```go
func TestDetectFirewallBackend(t *testing.T) {
	cases := []struct {
		name    string
		probe   probeResult
		want    FirewallBackend
		wantErr string
	}{
		{
			name: "prefer nft when available",
			probe: probeResult{
				HasNFT: true,
			},
			want: BackendNFT,
		},
		{
			name: "fallback to iptables nft backend",
			probe: probeResult{
				IPTablesVersion: "iptables v1.8.11 (nf_tables)",
			},
			want: BackendIPTablesNFT,
		},
		{
			name: "fallback to iptables legacy",
			probe: probeResult{
				IPTablesVersion: "iptables v1.8.11 (legacy)",
			},
			want: BackendIPTablesLegacy,
		},
		{
			name:    "error when nothing usable exists",
			probe:   probeResult{},
			wantErr: "no usable firewall backend",
		},
	}
	// call DetectFirewallBackendFromProbe(...)
}
```

- [ ] **Step 2: Write the failing ruleset rendering tests**

```go
func TestRenderRuleset(t *testing.T) {
	spec := FirewallSpec{
		SubnetCIDR: "192.168.100.0/24",
		BridgeIf:   "yeet0",
	}

	t.Run("nft", func(t *testing.T) {
		got := RenderFirewallRules(BackendNFT, spec)
		if !strings.Contains(got, "table ip yeet") {
			t.Fatalf("missing nft table: %s", got)
		}
	})

	t.Run("iptables", func(t *testing.T) {
		got := RenderFirewallRules(BackendIPTablesNFT, spec)
		if !strings.Contains(got, "YEET_FORWARD") {
			t.Fatalf("missing iptables chain: %s", got)
		}
	})
}
```

- [ ] **Step 3: Run the new tests to verify they fail**

Run:

```bash
go test ./pkg/netns -run 'Test(DetectFirewallBackend|RenderRuleset)' -v
```

Expected: FAIL with undefined `FirewallBackend`, `DetectFirewallBackendFromProbe`, `RenderFirewallRules`, or similar missing-symbol errors.

- [ ] **Step 4: Commit the failing test scaffold**

```bash
git add pkg/netns/firewall_test.go
git commit -m "pkg/netns: add firewall backend tests"
```

### Task 2: Implement Firewall Backend Detection And Yeet-Owned Rule Rendering

**Files:**
- Create: `pkg/netns/firewall.go`
- Modify: `pkg/netns/firewall_test.go`
- Test: `pkg/netns/firewall_test.go`

- [ ] **Step 1: Add backend enums, probe types, and render helpers**

```go
type FirewallBackend string

const (
	BackendNFT            FirewallBackend = "nft"
	BackendIPTablesNFT    FirewallBackend = "iptables-nft"
	BackendIPTablesLegacy FirewallBackend = "iptables-legacy"
)

type FirewallSpec struct {
	SubnetCIDR string
	BridgeIf   string
}

type probeResult struct {
	HasNFT          bool
	IPTablesVersion string
}
```

- [ ] **Step 2: Implement detection logic in the preferred order**

```go
func DetectFirewallBackendFromProbe(p probeResult) (FirewallBackend, error) {
	switch {
	case p.HasNFT:
		return BackendNFT, nil
	case strings.Contains(p.IPTablesVersion, "(nf_tables)"):
		return BackendIPTablesNFT, nil
	case strings.Contains(p.IPTablesVersion, "(legacy)"):
		return BackendIPTablesLegacy, nil
	default:
		return "", fmt.Errorf("no usable firewall backend")
	}
}
```

- [ ] **Step 3: Implement backend-specific yeet-owned ruleset rendering**

```go
func RenderFirewallRules(backend FirewallBackend, spec FirewallSpec) string {
	switch backend {
	case BackendNFT:
		return fmt.Sprintf(`
table ip yeet {
  chain forward {
    type filter hook forward priority 0;
    iifname "%s" accept
    oifname "%s" ct state related,established accept
  }
  chain postrouting {
    type nat hook postrouting priority srcnat;
    ip saddr %s ip daddr != %s masquerade
  }
}`, spec.BridgeIf, spec.BridgeIf, spec.SubnetCIDR, spec.SubnetCIDR)
	default:
		return strings.Join([]string{
			"*nat",
			":YEET_POSTROUTING - [0:0]",
			fmt.Sprintf("-A YEET_POSTROUTING -s %s ! -d %s -j MASQUERADE", spec.SubnetCIDR, spec.SubnetCIDR),
			"COMMIT",
		}, "\n")
	}
}
```

- [ ] **Step 4: Add ensure/verify/cleanup helpers that execute rendered commands**

```go
func EnsureFirewall(ctx context.Context, backend FirewallBackend, spec FirewallSpec) error
func VerifyFirewall(ctx context.Context, backend FirewallBackend, spec FirewallSpec) error
func CleanupFirewall(ctx context.Context, backend FirewallBackend, spec FirewallSpec) error
```

Use one execution path for `nft` and one for iptables-style chains. Keep the
ownership model explicit:

- nft owns `table ip yeet`
- iptables owns `YEET_FORWARD` and `YEET_POSTROUTING`

- [ ] **Step 5: Run the package tests to verify they pass**

Run:

```bash
go test ./pkg/netns -run 'Test(DetectFirewallBackend|RenderRuleset)' -v
```

Expected: PASS

- [ ] **Step 6: Run the broader package tests**

Run:

```bash
go test ./pkg/netns -v
```

Expected: PASS

- [ ] **Step 7: Commit the backend implementation**

```bash
git add pkg/netns/firewall.go pkg/netns/firewall_test.go
git commit -m "pkg/netns: add firewall backend abstraction"
```

### Task 3: Expose The Firewall Helper Through `catch`

**Files:**
- Create: `cmd/catch/netns_firewall_cmd.go`
- Create: `cmd/catch/netns_firewall_cmd_test.go`
- Modify: `cmd/catch/catch.go`
- Test: `cmd/catch/netns_firewall_cmd_test.go`

- [ ] **Step 1: Write the failing command dispatch tests**

```go
func TestHandleNetNSFirewallCommand(t *testing.T) {
	err := handleNetNSFirewallCommand([]string{"ensure"})
	if err == nil {
		t.Fatal("expected missing env/config error until helper is wired")
	}
}
```

- [ ] **Step 2: Run the command test to verify it fails**

Run:

```bash
go test ./cmd/catch -run TestHandleNetNSFirewallCommand -v
```

Expected: FAIL because `handleNetNSFirewallCommand` does not exist yet.

- [ ] **Step 3: Add a dedicated helper command file**

```go
func handleNetNSFirewallCommand(args []string) error {
	backend, spec, err := netns.LoadFirewallEnv(os.Environ())
	if err != nil {
		return err
	}
	switch args[0] {
	case "ensure":
		return netns.EnsureFirewall(context.Background(), backend, spec)
	case "verify":
		return netns.VerifyFirewall(context.Background(), backend, spec)
	case "cleanup":
		return netns.CleanupFirewall(context.Background(), backend, spec)
	default:
		return fmt.Errorf("unknown netns-firewall action %q", args[0])
	}
}
```

- [ ] **Step 4: Wire the new subcommand into `cmd/catch/catch.go`**

Insert the fast-path dispatch before daemon startup:

```go
if len(flag.Args()) > 0 && flag.Args()[0] == "netns-firewall" {
	if err := handleNetNSFirewallCommand(flag.Args()[1:]); err != nil {
		log.Fatal(err)
	}
	return
}
```

- [ ] **Step 5: Run the command tests to verify they pass**

Run:

```bash
go test ./cmd/catch -run TestHandleNetNSFirewallCommand -v
```

Expected: PASS

- [ ] **Step 6: Run a build to verify the helper path compiles**

Run:

```bash
go build ./cmd/catch
```

Expected: exit 0

- [ ] **Step 7: Commit the helper command**

```bash
git add cmd/catch/catch.go cmd/catch/netns_firewall_cmd.go cmd/catch/netns_firewall_cmd_test.go
git commit -m "cmd/catch: add netns firewall helper command"
```

### Task 4: Wire Backend Selection Into `yeet-ns` Setup

**Files:**
- Modify: `pkg/netns/netns.go`
- Modify: `pkg/netns/netns-scripts/yeet-ns`
- Test: `pkg/netns/firewall_test.go`

- [ ] **Step 1: Extend the generated yeet namespace env with firewall state**

Add fields like:

```go
type yeetNSEnv struct {
	Range            string `env:"RANGE"`
	HostIP           string `env:"HOST_IP"`
	BridgeIP         string `env:"BRIDGE_IP"`
	YeetIP           string `env:"YEET_IP"`
	FirewallBackend  string `env:"FIREWALL_BACKEND"`
	CatchBin         string `env:"CATCH_BIN"`
}
```

Populate them during `InstallYeetNSService()` by probing the host and resolving
the installed `catch` binary path.

- [ ] **Step 2: Replace direct `iptables` manipulation in `yeet-ns`**

Keep the topology work, but replace the firewall section with helper calls:

```bash
"$CATCH_BIN" netns-firewall ensure
"$CATCH_BIN" netns-firewall verify
```

Retain:

- namespace creation
- bridge creation
- `yeet0` / `yeet0-peer`
- routing
- `net.ipv4.ip_forward=1`

Remove:

- bare `iptables -A ...` calls

- [ ] **Step 3: Add cleanup behavior if the helper exposes it**

If the script grows a cleanup path later, call:

```bash
"$CATCH_BIN" netns-firewall cleanup
```

only for yeet-owned firewall objects, never broad host firewall cleanup.

- [ ] **Step 4: Run focused tests**

Run:

```bash
go test ./pkg/netns ./cmd/catch -v
```

Expected: PASS

- [ ] **Step 5: Run a full build for the touched binaries**

Run:

```bash
go build ./cmd/catch
go build ./cmd/yeet
```

Expected: both commands exit 0

- [ ] **Step 6: Commit the wiring changes**

```bash
git add pkg/netns/netns.go pkg/netns/netns-scripts/yeet-ns
git commit -m "pkg/netns: route yeet firewall setup through catch helper"
```

### Task 5: Update User And Operator Docs

**Files:**
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`

- [ ] **Step 1: Document backend preference on new hosts**

Add a short section to `website/docs/concepts/networking.mdx` that states:

- yeet prefers native `nft` on new hosts
- `iptables-nft` is the compatibility path
- `iptables-legacy` is supported only as a fallback

- [ ] **Step 2: Document operator inspection commands**

Add troubleshooting examples like:

```bash
ssh root@<host> nft list table ip yeet
ssh root@<host> iptables -S YEET_FORWARD
ssh root@<host> iptables -S YEET_POSTROUTING
ssh root@<host> systemctl status yeet-ns
```

- [ ] **Step 3: Run any docs formatting or build checks the repo already uses**

Run:

```bash
git diff -- website/docs/concepts/networking.mdx website/docs/operations/troubleshooting.mdx
```

Expected: only the intended docs changes appear.

- [ ] **Step 4: Commit the docs changes**

```bash
git add website/docs/concepts/networking.mdx website/docs/operations/troubleshooting.mdx
git commit -m "website: document netns firewall backend selection"
```

### Task 6: Run Local Verification And Full Test Sweep

**Files:**
- Test: `pkg/netns/firewall_test.go`
- Test: `cmd/catch/netns_firewall_cmd_test.go`

- [ ] **Step 1: Run the focused test packages**

Run:

```bash
go test ./pkg/netns ./cmd/catch -v
```

Expected: PASS

- [ ] **Step 2: Run the full repository test suite**

Run:

```bash
go test ./...
```

Expected: PASS

- [ ] **Step 3: Verify the worktree only contains intended changes**

Run:

```bash
git status --short
```

Expected: only the firewall/backend/docs changes appear.

### Task 7: End-To-End Validation On `edge-a`

**Files:**
- Verify live host state, no repo file edits expected

- [ ] **Step 1: Upgrade `catch` on `edge-a` from the current checkout**

Run:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet init root@edge-a
```

Expected: the remote host installs the new `catch` successfully.

- [ ] **Step 2: Deploy a temporary `svc`-network test service**

Run:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet run netfw-check nginx:latest --net=svc
```

Expected: deploy succeeds and the service reaches running state.

- [ ] **Step 3: Capture the service IP and inspect yeet-owned firewall state**

Run:

```bash
svc_ip=$(CATCH_HOST=yeet-edge-a go run ./cmd/yeet ip netfw-check | tr -d '\r')
ssh root@edge-a "ip netns list; nft list table ip yeet || true; iptables -S YEET_FORWARD || true; iptables -S YEET_POSTROUTING || true"
```

Expected:

- `netfw-check` namespace exists
- `nft list table ip yeet` succeeds on the preferred path, or the yeet-owned
  iptables chains exist on the fallback path

- [ ] **Step 4: Verify service reachability from the host**

Run:

```bash
svc_ip=$(CATCH_HOST=yeet-edge-a go run ./cmd/yeet ip netfw-check | tr -d '\r')
ssh root@edge-a "curl -fsSI http://${svc_ip}:80 | head -n 1"
```

Expected: `HTTP/1.1 200 OK` or equivalent success line from nginx.

- [ ] **Step 5: Re-run the deploy to prove idempotence**

Run:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet run netfw-check nginx:latest --net=svc
ssh root@edge-a "nft list table ip yeet || true; iptables -S YEET_FORWARD || true"
```

Expected: deploy succeeds again and the yeet-owned firewall state is not duplicated.

- [ ] **Step 6: Run a throughput sanity check**

Run:

```bash
svc_ip=$(CATCH_HOST=yeet-edge-a go run ./cmd/yeet ip netfw-check | tr -d '\r')
ssh root@edge-a "rm -f /tmp/iperf-netfw.log; nohup ip netns exec yeet-netfw-check-ns iperf3 -s -1 -B ${svc_ip} -p 25202 > /tmp/iperf-netfw.log 2>&1 < /dev/null &"
ssh root@edge-a "iptables -t nat -C PREROUTING -i vmbr0 -p tcp --dport 25212 -j DNAT --to-destination ${svc_ip}:25202 2>/dev/null || iptables -t nat -I PREROUTING 1 -i vmbr0 -p tcp --dport 25212 -j DNAT --to-destination ${svc_ip}:25202"
ssh shayne@macstudio.local 'nix shell nixpkgs#iperf3 -c iperf3 -c 10.0.4.2 -p 25212 -P 4 -t 10 -O 2 -f g'
ssh root@edge-a "while iptables -t nat -C PREROUTING -i vmbr0 -p tcp --dport 25212 -j DNAT --to-destination ${svc_ip}:25202 2>/dev/null; do iptables -t nat -D PREROUTING -i vmbr0 -p tcp --dport 25212 -j DNAT --to-destination ${svc_ip}:25202; done"
```

Expected: the measured throughput stays in line with the previously observed
`~2.35 Gbit/s` baseline on the same `2.5GbE` link, and the temporary DNAT rule
is removed after the check.

- [ ] **Step 7: Clean up the test service**

Run:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet rm netfw-check --yes --clean-config
```

Expected: service is removed and yeet-owned per-service networking artifacts are cleaned up.
