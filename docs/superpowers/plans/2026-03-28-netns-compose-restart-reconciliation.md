# NetNS Compose Restart Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent netns-backed docker compose services from getting stranded in stale network namespaces by detecting live container/netns mismatches and restarting only the affected compose projects.

**Architecture:** Add a small reconciliation layer to `pkg/svc` that compares the current named service netns against the live netns of containers attached to the yeet-managed compose network. Reuse that logic from docker-compose lifecycle methods for explicit service operations, and add one host-level reconciliation pass in `catch` startup so namespace refreshes on boot or upgrade repair already-stranded projects automatically.

**Tech Stack:** Go, Docker Compose, systemd, Linux network namespaces, yeet/catch service orchestration, website docs.

---

## File Structure

**Files:**
- Create: `pkg/svc/docker_netns.go`
- Create: `pkg/svc/docker_netns_test.go`
- Create: `pkg/catch/netns_reconcile.go`
- Create: `pkg/catch/netns_reconcile_test.go`
- Modify: `pkg/svc/docker.go`
- Modify: `pkg/catch/catch.go`
- Modify: `website/docs/concepts/networking.mdx` (inside the `website/` submodule)
- Modify: `website/docs/operations/troubleshooting.mdx` (inside the `website/` submodule)

**Responsibilities:**
- `pkg/svc/docker_netns.go`: resolve the yeet-managed compose network, inspect matching containers, compare named netns identity vs live container netns identity, and expose a single `ReconcileNetNS` entrypoint.
- `pkg/svc/docker_netns_test.go`: table-driven tests for container selection, mismatch detection, and restart/no-restart decisions.
- `pkg/svc/docker.go`: call the reconciliation helper from compose lifecycle methods without changing unrelated compose behavior.
- `pkg/catch/netns_reconcile.go`: scan yeet-managed docker compose services on the host and run reconciliation after catch startup.
- `pkg/catch/netns_reconcile_test.go`: unit tests for service filtering and host-level orchestration.
- docs: explain the automatic repair behavior and how to inspect it when debugging service reachability after namespace refresh.

### Task 1: Add Failing Tests For Docker NetNS Reconciliation Helpers

**Files:**
- Create: `pkg/svc/docker_netns_test.go`
- Test: `pkg/svc/docker_netns_test.go`

- [ ] **Step 1: Write the failing tests for selecting yeet-managed containers**

```go
func TestSelectNetNSContainers(t *testing.T) {
	project := "catch-demo"
	network := project + "_default"

	containers := []composeContainer{
		{ID: "app", PID: 101, Networks: []string{network}},
		{ID: "worker", PID: 202, Networks: []string{network, "extra"}},
		{ID: "sidecar", PID: 303, Networks: []string{"bridge"}},
	}

	got := selectNetNSContainers(containers, network)
	if diff := cmp.Diff([]composeContainer{containers[0], containers[1]}, got); diff != "" {
		t.Fatalf("selected containers mismatch (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Write the failing tests for mismatch and no-op decisions**

```go
func TestNeedsNetNSRestart(t *testing.T) {
	cases := []struct {
		name      string
		namedID   string
		containers []composeContainer
		want      bool
	}{
		{
			name:    "matching selected containers",
			namedID: "net:[4026533001]",
			containers: []composeContainer{
				{ID: "app", NetNSID: "net:[4026533001]"},
			},
			want: false,
		},
		{
			name:    "mismatch requires restart",
			namedID: "net:[4026533009]",
			containers: []composeContainer{
				{ID: "app", NetNSID: "net:[4026533001]"},
			},
			want: true,
		},
		{
			name:      "no selected containers",
			namedID:   "net:[4026533009]",
			containers: nil,
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsNetNSRestart(tc.namedID, tc.containers)
			if got != tc.want {
				t.Fatalf("needsNetNSRestart() = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Write the failing test for `ReconcileNetNS` calling compose restart only on mismatch**

```go
func TestDockerComposeServiceReconcileNetNS(t *testing.T) {
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: nginx:latest\n", recordCmd(t, &[]cmdCall{}))
	svc.netnsInspector = fakeNetNSInspector{
		namedID: "net:[4026534010]",
		containers: []composeContainer{
			{ID: "app", PID: 1001, Networks: []string{"catch-svc-a_default"}, NetNSID: "net:[4026533010]"},
		},
	}

	restarted, err := svc.ReconcileNetNS()
	if err != nil {
		t.Fatalf("ReconcileNetNS returned error: %v", err)
	}
	if !restarted {
		t.Fatal("expected restart when container netns is stale")
	}
}
```

- [ ] **Step 4: Run the new tests to verify they fail**

Run:

```bash
go test ./pkg/svc -run 'Test(SelectNetNSContainers|NeedsNetNSRestart|DockerComposeServiceReconcileNetNS)' -v
```

Expected: FAIL with undefined symbols such as `composeContainer`, `selectNetNSContainers`, `needsNetNSRestart`, or `ReconcileNetNS`.

- [ ] **Step 5: Commit the failing test scaffold**

```bash
git add pkg/svc/docker_netns_test.go
git commit -m "pkg/svc: add netns reconciliation tests"
```

### Task 2: Implement Service-Level NetNS Reconciliation In `pkg/svc`

**Files:**
- Create: `pkg/svc/docker_netns.go`
- Modify: `pkg/svc/docker.go`
- Modify: `pkg/svc/docker_test.go`
- Modify: `pkg/svc/docker_netns_test.go`
- Test: `pkg/svc/docker_netns_test.go`
- Test: `pkg/svc/docker_test.go`

- [ ] **Step 1: Add the inspection types and the helper surface**

```go
type composeContainer struct {
	ID       string
	PID      int
	Networks []string
	NetNSID  string
}

type netnsInspector interface {
	NamedNetNSID(path string) (string, error)
	ProjectContainers(project string) ([]composeContainer, error)
}

type linuxNetNSInspector struct{}
```

Implementation notes:
- derive the named netns path from the existing generated network config: `/var/run/netns/yeet-<svc>-ns`
- derive the yeet-managed compose network name from the existing compose project naming rule: `catch-<svc>_default`
- keep the helper Linux-only in behavior, but ordinary Go code is fine because catch only runs on Linux

- [ ] **Step 2: Implement live inspection for named netns and project containers**

```go
func (linuxNetNSInspector) NamedNetNSID(path string) (string, error)
func (linuxNetNSInspector) ProjectContainers(project string) ([]composeContainer, error)
func selectNetNSContainers(containers []composeContainer, network string) []composeContainer
func needsNetNSRestart(namedID string, containers []composeContainer) bool
```

Implementation notes:
- use one docker inspect pass that returns container ID, PID, and attached network names for the project containers
- read container netns identity from `/proc/<pid>/ns/net`
- compare only containers attached to `catch-<svc>_default`
- if no selected running containers exist, return `false`

- [ ] **Step 3: Add a single reconciliation entrypoint on `DockerComposeService`**

```go
func (s *DockerComposeService) ReconcileNetNS() (bool, error) {
	if !s.sd.hasArtifact(db.ArtifactNetNSService) {
		return false, nil
	}
	namedID, err := s.netnsInspector.NamedNetNSID(filepath.Join("/var/run/netns", "yeet-"+s.Name+"-ns"))
	if err != nil {
		return false, err
	}
	containers, err := s.netnsInspector.ProjectContainers(s.projectName(s.Name))
	if err != nil {
		return false, err
	}
	selected := selectNetNSContainers(containers, s.projectName(s.Name)+"_default")
	if !needsNetNSRestart(namedID, selected) {
		return false, nil
	}
	return true, s.runCommand("restart")
}
```

Use a small helper on `DockerComposeService` if needed to avoid duplicating:
- named netns path resolution
- compose project name resolution
- default network name resolution

- [ ] **Step 4: Call reconciliation from the explicit compose lifecycle paths**

Update these methods in `pkg/svc/docker.go`:

```go
func (s *DockerComposeService) UpWithPull(pull bool) error
func (s *DockerComposeService) Start() error
func (s *DockerComposeService) Restart() error
```

Required behavior:
- after ensuring the supporting systemd units are up, call `ReconcileNetNS`
- if reconciliation restarts the project, continue with the method's normal compose action only when still needed
- do not change non-netns docker compose services

Concrete rule for this first implementation:
- `UpWithPull`: run reconciliation before `docker compose up -d`
- `Restart`: if reconciliation already restarted the project, return success without issuing a second restart
- `Start`: run reconciliation first, then keep the existing `docker compose start` behavior

- [ ] **Step 5: Extend the existing docker helper process for inspection-based tests**

Update `pkg/svc/docker_test.go` and the new test file so the fake docker helper can answer:
- `docker compose ps -q`
- `docker inspect`

Use env vars such as:

```bash
HELPER_DOCKER_PSQ_OUTPUT
HELPER_DOCKER_INSPECT_OUTPUT
```

and keep the current helper-process style rather than adding large test-only production seams.

- [ ] **Step 6: Run the targeted `pkg/svc` tests**

Run:

```bash
go test ./pkg/svc -run 'Test(SelectNetNSContainers|NeedsNetNSRestart|DockerComposeServiceReconcileNetNS|DockerComposeUpWithoutPull|DockerComposeStatusesStateMapping)' -v
```

Expected: PASS

- [ ] **Step 7: Run the full `pkg/svc` package tests**

Run:

```bash
go test ./pkg/svc -v
```

Expected: PASS

- [ ] **Step 8: Commit the service-layer implementation**

```bash
git add pkg/svc/docker.go pkg/svc/docker_test.go pkg/svc/docker_netns.go pkg/svc/docker_netns_test.go
git commit -m "pkg/svc: reconcile compose projects with service netns"
```

### Task 3: Add Host-Level Reconciliation In `catch`

**Files:**
- Create: `pkg/catch/netns_reconcile.go`
- Create: `pkg/catch/netns_reconcile_test.go`
- Modify: `pkg/catch/catch.go`
- Test: `pkg/catch/netns_reconcile_test.go`

- [ ] **Step 1: Write the failing tests for service filtering and host-level reconciliation**

```go
func TestReconcileNetNSBackedDockerServices(t *testing.T) {
	s := newTestServer(t)
	svcs := []db.Service{
		{Name: "docker-netns", ServiceType: db.ServiceTypeDockerCompose, Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		}},
		{Name: "docker-plain", ServiceType: db.ServiceTypeDockerCompose},
		{Name: "systemd-netns", ServiceType: db.ServiceTypeSystemd},
	}

	var called []string
	s.newDockerComposeService = func(name string) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: name,
			reconcile: func() (bool, error) {
				called = append(called, name)
				return name == "docker-netns", nil
			},
		}, nil
	}

	if err := s.reconcileNetNSBackedDockerServices(); err != nil {
		t.Fatalf("reconcileNetNSBackedDockerServices returned error: %v", err)
	}
	if diff := cmp.Diff([]string{"docker-netns"}, called); diff != "" {
		t.Fatalf("unexpected reconciled services (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Add a failing test for `Server.Start` invoking the host-level pass after `InstallYeetNSService`**

```go
func TestServerStartRunsNetNSReconciliation(t *testing.T) {
	var installed bool
	var reconciled bool

	installYeetNSService = func() error {
		installed = true
		return nil
	}

	s := NewUnstartedServer(testConfig(t))
	s.reconcileNetNSBackedDockerServicesFn = func() error {
		reconciled = true
		return nil
	}

	s.Start()
	t.Cleanup(s.Shutdown)

	if !installed || !reconciled {
		t.Fatalf("start did not run install=%v reconcile=%v", installed, reconciled)
	}
}
```

- [ ] **Step 3: Run the new catch tests to verify they fail**

Run:

```bash
go test ./pkg/catch -run 'Test(ReconcileNetNSBackedDockerServices|ServerStartRunsNetNSReconciliation)' -v
```

Expected: FAIL with undefined helpers such as `reconcileNetNSBackedDockerServices` or missing injection points.

- [ ] **Step 4: Implement the host-level reconciliation helper**

Create `pkg/catch/netns_reconcile.go` with a small interface and scan helper:

```go
type dockerNetNSReconciler interface {
	ReconcileNetNS() (bool, error)
}

func (s *Server) reconcileNetNSBackedDockerServices() error
```

Implementation notes:
- load the DB once
- filter to `db.ServiceTypeDockerCompose`
- require `db.ArtifactNetNSService` for the current generation
- instantiate the docker compose service and call `ReconcileNetNS`
- log which services were restarted

- [ ] **Step 5: Wire the startup hook in `pkg/catch/catch.go`**

Replace the direct `netns.InstallYeetNSService()` call with injected package variables so tests can stub them:

```go
var installYeetNSService = netns.InstallYeetNSService
```

and call reconciliation immediately after install:

```go
if err := installYeetNSService(); err != nil {
	log.Fatalf("Failed to install bridge service: %v", err)
}
if err := s.reconcileNetNSBackedDockerServices(); err != nil {
	log.Printf("netns reconciliation failed: %v", err)
}
```

For this first implementation, keep startup non-fatal on reconciliation errors, but make the failure loud in the logs.

- [ ] **Step 6: Run the targeted catch tests**

Run:

```bash
go test ./pkg/catch -run 'Test(ReconcileNetNSBackedDockerServices|ServerStartRunsNetNSReconciliation)' -v
```

Expected: PASS

- [ ] **Step 7: Run the broader catch package tests**

Run:

```bash
go test ./pkg/catch -v
```

Expected: PASS

- [ ] **Step 8: Commit the catch-side orchestration**

```bash
git add pkg/catch/catch.go pkg/catch/netns_reconcile.go pkg/catch/netns_reconcile_test.go
git commit -m "pkg/catch: reconcile stale compose netns on startup"
```

### Task 4: Update Docs And Run Local Verification

**Files:**
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`

- [ ] **Step 1: Update the networking docs**

Add a short section describing the new behavior:

```md
When yeet refreshes a service netns, netns-backed docker compose services now
check whether their running containers still match the current named namespace.
If they do not, yeet restarts the affected compose project automatically.
```

- [ ] **Step 2: Update troubleshooting with the operator checks**

Document the debugging commands:

```bash
ssh root@<host> 'docker ps --filter label=com.docker.compose.project=catch-<svc>'
ssh root@<host> 'stat -Lc %i /var/run/netns/yeet-<svc>-ns'
ssh root@<host> 'docker inspect <container> --format "{{.State.Pid}}"'
ssh root@<host> 'readlink /proc/<pid>/ns/net'
journalctl -u catch --since "10 minutes ago"
```

- [ ] **Step 3: Run the full local test suite**

Run:

```bash
go test ./...
go build ./cmd/catch
go build ./cmd/yeet
```

Expected: PASS

- [ ] **Step 4: Commit docs and local verification**

```bash
(
  cd website &&
  git add docs/concepts/networking.mdx docs/operations/troubleshooting.mdx &&
  git commit -m "docs: explain compose netns reconciliation"
)
```

### Task 5: Live Verification On `edge-a` And `edge-b`

**Files:**
- Modify: none
- Test: live hosts `yeet-edge-a`, `yeet-edge-b`

- [ ] **Step 1: Update `catch` on both hosts**

Run:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet init root@edge-a
CATCH_HOST=yeet-edge-b go run ./cmd/yeet init root@edge-b
```

Expected: both hosts install the new catch binary cleanly.

- [ ] **Step 2: Create a temporary compose payload for verification**

Use the same payload for both hosts:

```bash
tmpdir=$(mktemp -d)
cat >"$tmpdir/compose.yml" <<'EOF'
services:
  app:
    image: nginx:alpine
EOF
```

- [ ] **Step 3: Deploy one temp service on each host with `--net=svc`**

Run:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet run netns-reconcile-check-edge-a "$tmpdir/compose.yml" --net=svc
CATCH_HOST=yeet-edge-b go run ./cmd/yeet run netns-reconcile-check-edge-b "$tmpdir/compose.yml" --net=svc
```

Expected: both services deploy and show as running.

- [ ] **Step 4: Record the named netns and container netns identities on each host**

Run:

```bash
ssh root@edge-a 'ns=yeet-netns-reconcile-check-edge-a-ns; cid=$(docker ps -q --filter label=com.docker.compose.project=catch-netns-reconcile-check-edge-a | head -n1); pid=$(docker inspect "$cid" --format "{{.State.Pid}}"); printf "named=%s\ncontainer=%s\n" "$(ip netns exec "$ns" readlink /proc/self/ns/net)" "$(readlink /proc/$pid/ns/net)"'

ssh root@edge-b 'ns=yeet-netns-reconcile-check-edge-b-ns; cid=$(docker ps -q --filter label=com.docker.compose.project=catch-netns-reconcile-check-edge-b | head -n1); pid=$(docker inspect "$cid" --format "{{.State.Pid}}"); printf "named=%s\ncontainer=%s\n" "$(ip netns exec "$ns" readlink /proc/self/ns/net)" "$(readlink /proc/$pid/ns/net)"'
```

Expected: named and container identities match before the forced refresh.

- [ ] **Step 5: Force a service-netns refresh to create the stale-container scenario**

Run:

```bash
ssh root@edge-a 'systemctl restart yeet-netns-reconcile-check-edge-a-ns.service'
ssh root@edge-b 'systemctl restart yeet-netns-reconcile-check-edge-b-ns.service'
```

Expected before catch reconciliation: the named netns identity changes while the currently running container still shows the old netns identity.

- [ ] **Step 6: Trigger the host-level reconciliation path**

Run:

```bash
ssh root@edge-a 'systemctl restart catch'
ssh root@edge-b 'systemctl restart catch'
```

Expected: catch startup runs the scan and restarts only the affected temp compose service on each host.

- [ ] **Step 7: Verify the services were repaired**

Run:

```bash
ssh root@edge-a 'ns=yeet-netns-reconcile-check-edge-a-ns; cid=$(docker ps -q --filter label=com.docker.compose.project=catch-netns-reconcile-check-edge-a | head -n1); pid=$(docker inspect "$cid" --format "{{.State.Pid}}"); printf "named=%s\ncontainer=%s\n" "$(ip netns exec "$ns" readlink /proc/self/ns/net)" "$(readlink /proc/$pid/ns/net)"; journalctl -u catch --since "5 minutes ago" --no-pager | tail -n 50'

ssh root@edge-b 'ns=yeet-netns-reconcile-check-edge-b-ns; cid=$(docker ps -q --filter label=com.docker.compose.project=catch-netns-reconcile-check-edge-b | head -n1); pid=$(docker inspect "$cid" --format "{{.State.Pid}}"); printf "named=%s\ncontainer=%s\n" "$(ip netns exec "$ns" readlink /proc/self/ns/net)" "$(readlink /proc/$pid/ns/net)"; journalctl -u catch --since "5 minutes ago" --no-pager | tail -n 50'
```

Expected:
- the running container netns now matches the named netns again
- catch logs mention the affected service being reconciled or restarted

- [ ] **Step 8: Verify explicit service operations still behave correctly**

Run:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet restart --service netns-reconcile-check-edge-a
CATCH_HOST=yeet-edge-b go run ./cmd/yeet restart --service netns-reconcile-check-edge-b
```

Expected: both commands succeed without unnecessary extra restarts when the project is already in the correct namespace.

- [ ] **Step 9: Clean up the temporary services**

Run interactively:

```bash
CATCH_HOST=yeet-edge-a go run ./cmd/yeet rm netns-reconcile-check-edge-a
CATCH_HOST=yeet-edge-b go run ./cmd/yeet rm netns-reconcile-check-edge-b
```

Expected: both services are removed cleanly.

- [ ] **Step 10: Commit the final verified state**

```bash
git status --short
git add pkg/svc/docker.go pkg/svc/docker_test.go pkg/svc/docker_netns.go pkg/svc/docker_netns_test.go pkg/catch/catch.go pkg/catch/netns_reconcile.go pkg/catch/netns_reconcile_test.go website
git commit -m "pkg/svc: repair stale compose netns after refresh"
```
