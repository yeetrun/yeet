# NetNS Stability Follow-Ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the two remaining netns stability gaps by making yeet-owned docker port forwarding converge to the current desired state and making catch's background netns reconciliation cancelable during shutdown.

**Architecture:** Treat the `pkg/dnet` forwarding rules as a reconciled ruleset owned by yeet instead of an append-only side effect of endpoint events, so stale DNAT entries cannot survive endpoint churn or multi-container compose projects. Then thread `context.Context` through the catch startup reconciliation path and the docker/netns command helpers so shutdown can cancel blocked reconciliation work instead of waiting forever.

**Tech Stack:** Go, Docker Compose, yeet docker network plugin, Linux network namespaces, iptables/nft-backed nat chains, systemd, yeet/catch orchestration.

**Commit policy:** Commit steps in this plan assume explicit user authorization has already been granted for this session. If that changes, stop before any new commit.

---

## File Structure

**Files:**
- Create: `pkg/dnet/dnet_test.go`
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/svc/docker_netns.go`
- Modify: `pkg/svc/docker_netns_test.go`
- Modify: `pkg/svc/docker.go`
- Modify: `pkg/svc/docker_test.go`
- Modify: `pkg/catch/netns_reconcile.go`
- Modify: `pkg/catch/netns_reconcile_test.go`
- Modify: `pkg/catch/catch.go`
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`

**Responsibilities:**
- `pkg/dnet/dnet.go`: derive the desired per-netns yeet DNAT rules from DB state and reconcile those rules instead of leaking stale per-endpoint entries.
- `pkg/dnet/dnet_test.go`: table-driven tests for desired-forward computation and stale-rule cleanup behavior.
- `pkg/svc/docker_netns.go`: make netns inspection commands cancelable and expose context-aware reconciliation helpers.
- `pkg/svc/docker.go`: use context-aware compose commands for reconciliation-triggered recreates.
- `pkg/catch/netns_reconcile.go`: run host-level netns reconciliation with a cancelable context and bounded command execution.
- `pkg/catch/catch.go`: keep startup non-blocking while ensuring shutdown cancels background reconciliation cleanly.
- docs: explain yeet-owned service-netns forwarding and the reconciliation behavior that operators should expect.

### Task 1: Add Failing Tests For DNet Forwarding Reconciliation

**Files:**
- Create: `pkg/dnet/dnet_test.go`
- Test: `pkg/dnet/dnet_test.go`

- [ ] **Step 1: Write a failing test for desired port-forward selection**

```go
func TestDesiredPortForwardsSkipsStalePortOwners(t *testing.T) {
	network := &db.DockerNetwork{
		NetNS: "yeet-vaultwarden-ns",
		Endpoints: map[string]*db.DockerEndpoint{
			"app":    {EndpointID: "app", IPv4: netip.MustParsePrefix("172.20.0.3/16")},
			"backup": {EndpointID: "backup", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
		},
		PortMap: map[string]*db.EndpointPort{
			"6/80": {EndpointID: "app", Port: 80},
			"6/81": {EndpointID: "stale-owner", Port: 81},
		},
	}

	got := desiredPortForwards(network)
	want := []portForwardRule{{Proto: "tcp", HostPort: 80, TargetIP: "172.20.0.3", TargetPort: 80}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("desiredPortForwards mismatch (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Write a failing test for stale rule cleanup**

```go
func TestSyncNetNSPortForwardsRemovesStaleRules(t *testing.T) {
	backend := &fakeIPTables{
		prerouting: []string{
			"-A YEET_PREROUTING -i br0 -j RETURN",
			"-A YEET_PREROUTING -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.2:80",
			"-A YEET_PREROUTING -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.3:80",
		},
		output: []string{
			"-A OUTPUT -o lo -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.2:80",
		},
	}

	err := syncNetNSPortForwards("yeet-vaultwarden-ns", []portForwardRule{
		{Proto: "tcp", HostPort: 80, TargetIP: "172.20.0.3", TargetPort: 80},
	}, backend)
	if err != nil {
		t.Fatalf("syncNetNSPortForwards returned error: %v", err)
	}
	if diff := cmp.Diff([]string{
		"-A YEET_PREROUTING -i br0 -j RETURN",
		"-A YEET_PREROUTING -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.3:80",
	}, backend.prerouting); diff != "" {
		t.Fatalf("unexpected prerouting rules (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{
		"-A YEET_OUTPUT -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.3:80",
	}, backend.yeetOutput); diff != "" {
		t.Fatalf("unexpected output rules (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 3: Run the new dnet tests to verify they fail**

Run:

```bash
go test ./pkg/dnet -run 'Test(DesiredPortForwards|SyncNetNSPortForwards)' -v
```

Expected: FAIL with undefined helpers such as `desiredPortForwards`, `syncNetNSPortForwards`, or missing fake iptables support.

- [ ] **Step 4: Commit the failing dnet tests**

```bash
git add pkg/dnet/dnet_test.go
git commit -m "pkg/dnet: add forwarding reconciliation tests"
```

### Task 2: Implement DNet Forwarding Reconciliation And Stale Rule Cleanup

**Files:**
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/dnet/dnet_test.go`
- Test: `pkg/dnet/dnet_test.go`

- [ ] **Step 1: Add explicit rule model and desired-state helpers**

Add small helpers near the existing forwarding code:

```go
type portForwardRule struct {
	Proto      string
	HostPort   uint16
	TargetIP   string
	TargetPort uint16
}

type natRuleBackend interface {
	List(chain string) ([]string, error)
	Append(chain string, rule ...string) error
	Delete(chain string, rule ...string) error
	Flush(chain string) error
	EnsureYeetChains() error
	RemoveLegacyOutputDNAT() error
}

func desiredPortForwards(n *db.DockerNetwork) []portForwardRule
func desiredPortForwardsForNetNS(d *db.Data, netns string) []portForwardRule
func syncNetNSPortForwards(netns string, desired []portForwardRule, backend natRuleBackend) error
```

Implementation notes:
- derive desired rules from `n.PortMap` plus the current `n.Endpoints`
- skip port mappings whose `EndpointID` no longer exists
- preserve or recreate the existing `YEET_PREROUTING -i br0 -j RETURN` guard before any DNAT entries
- sort deterministically by protocol, host port, then target so test expectations are stable

- [ ] **Step 2: Replace append-only forwarding with yeet-owned chain reconciliation**

Refactor the current `ensurePreroutingChain`, `ensurePostroutingChain`, and `forwardPort` path so the service netns owns:

```go
const (
	preroutingChainName  = "YEET_PREROUTING"
	outputChainName      = "YEET_OUTPUT"
	postroutingChainName = "YEET_POSTROUTING"
)

type iptablesBackend struct{}
```

Implementation notes:
- keep `YEET_PREROUTING` and `YEET_POSTROUTING`
- add `YEET_OUTPUT` plus a single stable jump from `OUTPUT -o lo`
- on each sync, flush yeet-owned forward chains and repopulate from `desired`
- add a one-time cleanup for old direct `OUTPUT ... DNAT` rules of the previous yeet shape inside the service netns before repopulating `YEET_OUTPUT`
- call `syncNetNSPortForwards` after `CreateEndpoint`, `DeleteEndpoint`, `Join`, and `Leave` DB mutations, using the current netns as the source of truth
- use the injectable `natRuleBackend` in tests and `iptablesBackend` in production so the TDD path can replace the current hard-coded `runCmd` behavior

- [ ] **Step 3: Remove stale `PortMap` ownership when endpoints leave**

Tighten the endpoint lifecycle:

```go
func removeEndpointPortMappings(n *db.DockerNetwork, endpointID string)
```

Implementation notes:
- when an endpoint is deleted or leaves, remove any `n.PortMap` entries still owned by that endpoint
- when `CreateEndpoint` receives a replacement mapping for an endpoint, clear any older `n.PortMap` entries owned by that same endpoint before inserting the new ones
- when an endpoint IP is replaced for the same network, remove or overwrite stale owner references before sync
- preserve the current behavior for networks that have no published ports

- [ ] **Step 4: Run the targeted dnet tests**

Run:

```bash
go test ./pkg/dnet -run 'Test(DesiredPortForwards|SyncNetNSPortForwards)' -v
```

Expected: PASS

- [ ] **Step 5: Add a regression test for the Vaultwarden-shaped scenario**

Extend `pkg/dnet/dnet_test.go` with a case that models:
- a service network with one published app endpoint
- a second sidecar endpoint with no published ports
- stale old DNAT rules targeting the sidecar IP

Expected result: sync removes the sidecar-targeting rules and leaves only the app-targeting rule.

- [ ] **Step 6: Run the full dnet package tests**

Run:

```bash
go test ./pkg/dnet -v
```

Expected: PASS

- [ ] **Step 7: Commit the dnet reconciliation fix**

```bash
git add pkg/dnet/dnet.go pkg/dnet/dnet_test.go
git commit -m "pkg/dnet: reconcile yeet port forwarding rules"
```

### Task 3: Add Failing Tests For Cancelable NetNS Reconciliation

**Files:**
- Modify: `pkg/svc/docker_netns_test.go`
- Modify: `pkg/svc/docker_test.go`
- Modify: `pkg/catch/netns_reconcile_test.go`
- Test: `pkg/svc/docker_netns_test.go`
- Test: `pkg/catch/netns_reconcile_test.go`

- [ ] **Step 1: Add a failing test for cancelable netns inspector commands**

```go
func TestLinuxNetNSInspectorNamedNetNSLinkNamesHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (linuxNetNSInspector{}).NamedNetNSLinkNames(ctx, "/var/run/netns/yeet-svc-a-ns")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("NamedNetNSLinkNames error = %v, want context.Canceled", err)
	}
}

func TestLinuxNetNSInspectorProjectContainersHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (linuxNetNSInspector{}).ProjectContainers(ctx, "catch-svc-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProjectContainers error = %v, want context.Canceled", err)
	}
}
```

- [ ] **Step 2: Replace the shutdown wait test with a cancellation test**

Add or rewrite the catch-side test:

```go
func TestServerShutdownCancelsNetNSReconciliation(t *testing.T) {
	s := newTestServer(t)
	addTestNetNSDockerService(t, s, "docker-netns")

	reconcileStarted := make(chan struct{})
	reconcileCtxDone := make(chan struct{})
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(ctx context.Context) (bool, error) {
				close(reconcileStarted)
				<-ctx.Done()
				close(reconcileCtxDone)
				return false, ctx.Err()
			},
		}, nil
	}

	go s.Start()
	<-reconcileStarted
	s.Shutdown()
	<-reconcileCtxDone
}
```

Expected behavior:
- `Start()` still returns promptly
- `Shutdown()` no longer waits for a manual release channel
- reconciliation exits via context cancellation

- [ ] **Step 3: Run the targeted cancellation tests to verify they fail**

Run:

```bash
go test ./pkg/svc -run 'Test(LinuxNetNSInspectorNamedNetNSLinkNamesHonorsContextCancel|LinuxNetNSInspectorProjectContainersHonorsContextCancel)' -v
go test ./pkg/catch -run 'TestServerShutdownCancelsNetNSReconciliation' -v
```

Expected: FAIL with signature mismatches or missing context-aware reconcile hooks.

- [ ] **Step 4: Commit the failing cancellation tests**

```bash
git add pkg/svc/docker_netns_test.go pkg/svc/docker_test.go pkg/catch/netns_reconcile_test.go
git commit -m "pkg/catch: add cancelable reconciliation tests"
```

### Task 4: Implement Context-Aware Reconciliation And Shutdown Hardening

**Files:**
- Modify: `pkg/svc/docker_netns.go`
- Modify: `pkg/svc/docker.go`
- Modify: `pkg/svc/docker_test.go`
- Modify: `pkg/svc/docker_netns_test.go`
- Modify: `pkg/catch/netns_reconcile.go`
- Modify: `pkg/catch/netns_reconcile_test.go`
- Modify: `pkg/catch/catch.go`

- [ ] **Step 1: Thread context through the netns inspector and docker compose helpers**

Change the service-side interfaces:

```go
type netnsInspector interface {
	NamedNetNSLinkNames(ctx context.Context, path string) ([]string, error)
	ProjectContainers(ctx context.Context, project string) ([]composeContainer, error)
}

func (s *DockerComposeService) runCommandContext(ctx context.Context, args ...string) error
func (s *DockerComposeService) ReconcileNetNS(ctx context.Context) (bool, error)
```

Implementation notes:
- keep existing non-context lifecycle helpers delegating to `context.Background()` where appropriate
- use `exec.CommandContext` for the inspector commands and compose recreates
- return `ctx.Err()` when cancellation wins

- [ ] **Step 2: Make host-level reconciliation cancelable**

Update the catch-side interface and runner:

```go
type dockerNetNSReconciler interface {
	ReconcileNetNS(ctx context.Context) (bool, error)
}

func (s *Server) reconcileNetNSBackedDockerServices(ctx context.Context) error
```

Implementation notes:
- pass `s.ctx` from the startup goroutine
- treat `context.Canceled` during shutdown as a normal exit, not an error log
- keep per-service error logging for real reconciliation failures

- [ ] **Step 3: Tighten `Start` and `Shutdown` semantics**

Adjust `pkg/catch/catch.go`:

```go
s.waitGroup.Go(func() {
	if err := s.reconcileNetNSBackedDockerServices(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("netns reconciliation failed: %v", err)
	}
})
```

Implementation notes:
- `Start()` must remain prompt even if reconciliation blocks
- `Shutdown()` should cancel the shared context and then wait for goroutines to exit
- the shutdown guarantee should now depend on command cancellation rather than ad hoc release channels

- [ ] **Step 4: Run the targeted cancellation tests**

Run:

```bash
go test ./pkg/svc -run 'Test(LinuxNetNSInspectorNamedNetNSLinkNamesHonorsContextCancel|LinuxNetNSInspectorProjectContainersHonorsContextCancel|DockerComposeServiceRestartShortCircuitsAfterReconcileRestart)' -v
go test ./pkg/catch -run 'Test(ServerStartReturnsBeforeNetNSReconciliationFinishes|ServerShutdownCancelsNetNSReconciliation|ServerStartRunsNetNSReconciliation)' -v
```

Expected: PASS

- [ ] **Step 5: Run the broader touched-package tests**

Run:

```bash
go test ./pkg/svc -v
go test ./pkg/catch -v
```

Expected: PASS

- [ ] **Step 6: Commit the cancelability hardening**

```bash
git add pkg/svc/docker_netns.go pkg/svc/docker.go pkg/svc/docker_test.go pkg/svc/docker_netns_test.go pkg/catch/netns_reconcile.go pkg/catch/netns_reconcile_test.go pkg/catch/catch.go
git commit -m "pkg/catch: cancel background netns reconciliation"
```

### Task 5: Update Docs And Run End-To-End Verification

**Files:**
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`

- [ ] **Step 1: Update networking docs for yeet-owned forwarding**

Document that netns-backed docker services now reconcile their yeet-owned DNAT rules from current endpoint state, so stale sidecar-targeting rules are removed automatically during endpoint churn or service recreation.

- [ ] **Step 2: Update troubleshooting docs for reconciliation and shutdown behavior**

Document the operator checks:

```bash
ssh root@<host> 'ip netns exec yeet-<svc>-ns iptables -t nat -S'
ssh root@<host> 'journalctl -u catch --since "10 minutes ago" --no-pager'
ssh root@<host> 'docker ps --filter label=com.docker.compose.project=catch-<svc>'
```

Call out that catch startup is prompt, background reconciliation may still restart affected services, and shutdown/restart should no longer hang on blocked reconciliation commands.

- [ ] **Step 3: Run full local verification**

Run:

```bash
go test ./...
go build ./cmd/catch
go build ./cmd/yeet
```

Expected: PASS

- [ ] **Step 4: Deploy and verify on `pve1`**

Run:

```bash
CATCH_HOST=yeet-pve1 go run ./cmd/yeet init root@pve1
```

Verify:
- `bw.ss.ht` is reachable again through the normal path
- `ip netns exec yeet-vaultwarden-ns iptables -t nat -S` shows only the app-targeting port `80` DNAT rules
- restarting `yeet-vaultwarden-ns.service` followed by `systemctl restart catch` does not reintroduce stale sidecar-targeting rules
- `CATCH_HOST=yeet-pve1 go run ./cmd/yeet status` returns promptly after restart
- rerun the compose/netns reconciliation acceptance on one representative `svc` docker service such as `smokeping`: restart `yeet-smokeping-ns.service`, confirm the service breaks, restart `catch`, and verify yeet automatically recreates only the affected compose project and restores reachability

- [ ] **Step 5: Deploy and verify on `hetz`**

Run:

```bash
CATCH_HOST=yeet-hetz go run ./cmd/yeet init root@hetz
```

Verify:
- `CATCH_HOST=yeet-hetz go run ./cmd/yeet status` returns promptly after restart
- restart one representative `svc` docker namespace (for example `uptime-kuma`) and then restart `catch`
- catch logs show automatic project reconciliation for the affected service
- the service becomes reachable again after reconciliation
- catch shutdown/restart remains prompt during the background scan

- [ ] **Step 6: Commit docs and final verified state**

```bash
(
  cd website &&
  git add docs/concepts/networking.mdx docs/operations/troubleshooting.mdx &&
  git commit -m "docs: explain netns forwarding reconciliation"
)
git add pkg/dnet/dnet.go pkg/dnet/dnet_test.go pkg/svc/docker_netns.go pkg/svc/docker_netns_test.go pkg/svc/docker.go pkg/svc/docker_test.go pkg/catch/netns_reconcile.go pkg/catch/netns_reconcile_test.go pkg/catch/catch.go website
git commit -m "pkg/netns: harden service forwarding and reconciliation shutdown"
```
