# Fast Status Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet status` collect host runtime state in bulk and make multi-host `yeet status --format=json` work from project workspaces.

**Architecture:** Keep client aggregation in `pkg/yeet` and catch-side runtime authority in `pkg/catch`. Add small `pkg/svc` helper exports for existing Docker state mapping and systemd primary unit naming, then add focused catch snapshot collectors for Docker and systemd state that assemble the existing `ServiceStatusData` shape.

**Tech Stack:** Go, Docker CLI JSON output, systemd `show`, Go `testing`, GitButler.

---

## Scope Check

This is one feature with three cooperating layers: client status routing,
service helper APIs, and catch status collection. Each task below leaves the
repo in a testable state and avoids changing CLI syntax or adding a new RPC
method.

## File Structure

- Modify `pkg/svc/docker.go`
  - Export `DockerComposeStateStatus` as a thin wrapper around the existing
    Docker Compose state mapping.
- Modify `pkg/svc/docker_test.go`
  - Cover the exported Docker state mapping helper.
- Modify `pkg/svc/systemd.go`
  - Export `(*SystemdService).PrimaryUnit()` as a thin wrapper around the
    existing primary service-or-timer unit selection.
- Modify `pkg/svc/systemd_test.go`
  - Cover primary unit selection for service-backed and timer-backed services.
- Modify `pkg/yeet/svc_cmd.go`
  - Route no-service table, JSON, and JSON-pretty status requests through the
    multi-host aggregator.
- Modify `pkg/yeet/svc_cmd_branch_test.go`
  - Add regression tests for multi-host JSON and JSON-pretty routing.
- Create `pkg/catch/status_snapshot.go`
  - Add Docker JSON-line parsing, systemd `show` parsing, status snapshot
    collection, and service-status assembly helpers.
- Create `pkg/catch/status_snapshot_test.go`
  - Test Docker label grouping, systemd state mapping, and service status
    assembly.
- Modify `pkg/catch/tty_service.go`
  - Wire system-wide status to the snapshot collector while preserving existing
    test hook behavior.
- Modify `pkg/catch/tty_service_test.go`
  - Add a regression test proving system-wide status uses the snapshot path
    when no legacy test hooks are installed.

## Task 0: Workspace Setup

**Files:**
- Read: `AGENTS.md`
- Read: `AGENTS.local.md`
- Read: `docs/agent/codebase-map.md`
- Read: `pkg/yeet/AGENTS.md`
- Read: `pkg/catch/AGENTS.md`
- Read: `pkg/svc/AGENTS.md`

- [ ] **Step 1: Check branch base before implementation**

Run:

```bash
but pull --check
```

Expected: GitButler reports `Up to date`, or reports a clean pull check. If it
reports conflicts or another active branch touching `pkg/yeet`, `pkg/catch`, or
`pkg/svc`, stop and ask the user before continuing.

- [ ] **Step 2: Inspect dirty state**

Run:

```bash
but diff
```

Expected: either no uncommitted changes, or only the approved spec/plan docs
from this session. Do not commit, discard, or move another branch's work.

## Task 1: Expose Existing Service Status Helpers

**Files:**
- Modify: `pkg/svc/docker.go`
- Modify: `pkg/svc/docker_test.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/svc/systemd_test.go`

- [ ] **Step 1: Write failing tests for helper APIs**

Add this test near `TestDockerComposeStatusesStateMapping` in
`pkg/svc/docker_test.go`:

```go
func TestDockerComposeStateStatusExport(t *testing.T) {
	tests := []struct {
		state string
		want  Status
	}{
		{state: "running", want: StatusRunning},
		{state: "restarting", want: StatusRunning},
		{state: "exited", want: StatusStopped},
		{state: "created", want: StatusStopped},
		{state: "paused", want: StatusStopped},
		{state: "dead", want: StatusStopped},
		{state: "removing", want: StatusStopped},
		{state: "mystery", want: StatusUnknown},
	}

	for _, tt := range tests {
		got := DockerComposeStateStatus(tt.state)
		if got != tt.want {
			t.Fatalf("DockerComposeStateStatus(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}
```

Add this test near `TestSystemdServiceInstallPlanOrdersArtifactsAndPrimaryTimer`
in `pkg/svc/systemd_test.go`:

```go
func TestSystemdServicePrimaryUnit(t *testing.T) {
	serviceCfg := db.Service{Name: "demo"}
	service := &SystemdService{cfg: serviceCfg.View()}
	if got := service.PrimaryUnit(); got != "demo.service" {
		t.Fatalf("service PrimaryUnit = %q, want demo.service", got)
	}

	timerCfg := db.Service{
		Name: "demo",
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdTimerFile: testArtifact("timer"),
		},
	}
	timer := &SystemdService{cfg: timerCfg.View()}
	if got := timer.PrimaryUnit(); got != "demo.timer" {
		t.Fatalf("timer PrimaryUnit = %q, want demo.timer", got)
	}
}
```

- [ ] **Step 2: Run the helper tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/svc -run 'Test(DockerComposeStateStatusExport|SystemdServicePrimaryUnit)' -count=1
```

Expected: FAIL with compile errors for undefined
`DockerComposeStateStatus` and `PrimaryUnit`.

- [ ] **Step 3: Add the helper APIs**

In `pkg/svc/docker.go`, add this function directly above the existing
`dockerComposeStateStatus` function:

```go
func DockerComposeStateStatus(state string) Status {
	return dockerComposeStateStatus(state)
}
```

In `pkg/svc/systemd.go`, add this method directly above the existing
`primaryUnit` method:

```go
func (s *SystemdService) PrimaryUnit() string {
	return s.primaryUnit()
}
```

- [ ] **Step 4: Run the helper tests and verify they pass**

Run:

```bash
mise exec -- go test ./pkg/svc -run 'Test(DockerComposeStateStatusExport|SystemdServicePrimaryUnit)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run all service-helper tests**

Run:

```bash
mise exec -- go test ./pkg/svc -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the helper APIs**

Run:

```bash
but diff
```

Expected: only `pkg/svc/docker.go`, `pkg/svc/docker_test.go`,
`pkg/svc/systemd.go`, and `pkg/svc/systemd_test.go` are included. Copy the file
change IDs printed by `but diff`, then run:

```bash
but commit codex/fast-status-snapshots -m "pkg/svc: expose status helper APIs" --changes <ids-from-but-diff>
```

Expected: GitButler creates one commit on `codex/fast-status-snapshots`.

## Task 2: Fix Client Multi-Host JSON Routing

**Files:**
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`

- [ ] **Step 1: Write the failing multi-host JSON routing test**

Add this test near `TestSvcStatusFetchMultiHostAndRemoteFormats` in
`pkg/yeet/svc_cmd_branch_test.go`:

```go
func TestHandleStatusCommandAggregatesProjectJSONFormats(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = ""
	loadedPrefs.DefaultHost = "default-host"

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-b"})
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-b", Host: "host-a"})
	loc := &projectConfigLocation{Config: cfg}

	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called for multi-host JSON status")
		return nil
	}
	fetchStatusForHostFn = func(ctx context.Context, host string, flags cli.StatusFlags) ([]statusService, error) {
		if flags.Format != "json" {
			t.Fatalf("status format = %q, want json", flags.Format)
		}
		return []statusService{{
			ServiceName: "svc-" + host,
			ServiceType: "binary",
			Components:  []statusComponent{{Name: "svc-" + host, Status: "running"}},
		}}, nil
	}

	out, err := captureSvcStdout(t, func() error {
		return handleStatusCommand(context.Background(), []string{"--format=json"}, loc, false)
	})
	if err != nil {
		t.Fatalf("handleStatusCommand returned error: %v", err)
	}
	var decoded []hostStatusData
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("status JSON invalid: %v\n%s", err, out)
	}
	if len(decoded) != 2 || decoded[0].Host != "host-a" || decoded[1].Host != "host-b" {
		t.Fatalf("decoded hosts = %#v, want sorted host-a/host-b", decoded)
	}
}
```

- [ ] **Step 2: Write the failing JSON-pretty routing test**

Add this test next to the JSON routing test:

```go
func TestHandleStatusCommandAggregatesProjectJSONPrettyFormat(t *testing.T) {
	preserveSvcCommandGlobals(t)
	serviceOverride = ""

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a"})
	loc := &projectConfigLocation{Config: cfg}

	fetchStatusForHostFn = func(ctx context.Context, host string, flags cli.StatusFlags) ([]statusService, error) {
		if flags.Format != "json-pretty" {
			t.Fatalf("status format = %q, want json-pretty", flags.Format)
		}
		return []statusService{{ServiceName: "svc-a", ServiceType: "binary"}}, nil
	}

	out, err := captureSvcStdout(t, func() error {
		return handleStatusCommand(context.Background(), []string{"--format=json-pretty"}, loc, false)
	})
	if err != nil {
		t.Fatalf("handleStatusCommand returned error: %v", err)
	}
	if !strings.Contains(out, "\n  {") {
		t.Fatalf("status output is not pretty JSON:\n%s", out)
	}
}
```

- [ ] **Step 3: Run the routing tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestHandleStatusCommandAggregatesProjectJSON' -count=1
```

Expected: FAIL. The JSON test should hit `execRemoteFn` or otherwise fail
because `handleStatusCommand` does not route JSON through `statusMultiHost`.

- [ ] **Step 4: Implement the routing helper**

In `pkg/yeet/svc_cmd.go`, replace `handleStatusCommand` and add the helper
below it:

```go
func handleStatusCommand(ctx context.Context, args []string, cfgLoc *projectConfigLocation, hostOverrideSet bool) error {
	flags, _, err := cli.ParseStatus(args)
	if err != nil {
		return err
	}
	if serviceOverride == "" && shouldAggregateStatusFormat(flags.Format) {
		return statusMultiHost(ctx, statusHosts(cfgLoc, hostOverrideSet), flags)
	}
	if shouldRenderStatusTable(flags.Format) && serviceOverride != "" {
		return renderStatusTableForService(ctx, Host(), serviceOverride)
	}
	svc := getService()
	statusArgs := append([]string{"status"}, args...)
	return execRemoteFn(ctx, svc, statusArgs, nil, true)
}

func shouldAggregateStatusFormat(format string) bool {
	switch strings.TrimSpace(format) {
	case "", "table", "json", "json-pretty":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 5: Run the routing tests and verify they pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestHandleStatusCommandAggregatesProjectJSON|TestSvcStatusFetchMultiHostAndRemoteFormats|TestSvcHandleStatusRemoteAndParseErrors' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run client package tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the client routing fix**

Run:

```bash
but diff
```

Expected: only `pkg/yeet/svc_cmd.go` and
`pkg/yeet/svc_cmd_branch_test.go` are included. Copy the file change IDs, then
run:

```bash
but commit codex/fast-status-snapshots -m "pkg/yeet: aggregate status json across hosts" --changes <ids-from-but-diff>
```

Expected: GitButler creates one commit on `codex/fast-status-snapshots`.

## Task 3: Add Catch Snapshot Parsers And Collectors

**Files:**
- Create: `pkg/catch/status_snapshot.go`
- Create: `pkg/catch/status_snapshot_test.go`

- [ ] **Step 1: Write failing Docker snapshot parser tests**

Create `pkg/catch/status_snapshot_test.go` with this initial content:

```go
package catch

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/svc"
)

func TestParseDockerComposeStatusSnapshotGroupsYeetProjects(t *testing.T) {
	raw := strings.Join([]string{
		`{"State":"running","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=api"}`,
		`{"State":"exited","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=worker"}`,
		`{"State":"running","Labels":"com.docker.compose.project=other,com.docker.compose.service=ignored"}`,
		`{"State":"mystery","Labels":"com.docker.compose.project=catch-db,com.docker.compose.service=postgres"}`,
		`bad-json`,
	}, "\n")

	got, err := parseDockerComposeStatusSnapshot([]byte(raw))
	if err != nil {
		t.Fatalf("parseDockerComposeStatusSnapshot returned error: %v", err)
	}
	want := map[string]svc.DockerComposeStatus{
		"web": {
			"api":    svc.StatusRunning,
			"worker": svc.StatusStopped,
		},
		"db": {
			"postgres": svc.StatusUnknown,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docker snapshot = %#v, want %#v", got, want)
	}
}

func TestParseDockerComposeStatusSnapshotRejectsOnlyMalformedOutput(t *testing.T) {
	_, err := parseDockerComposeStatusSnapshot([]byte("bad-json\nalso-bad\n"))
	if err == nil || !strings.Contains(err.Error(), "no valid docker status rows") {
		t.Fatalf("error = %v, want no valid docker status rows", err)
	}
}

func TestParseDockerComposeStatusSnapshotAllowsOnlyNonYeetProjects(t *testing.T) {
	raw := []byte(`{"State":"running","Labels":"com.docker.compose.project=other,com.docker.compose.service=api"}`)
	got, err := parseDockerComposeStatusSnapshot(raw)
	if err != nil {
		t.Fatalf("parseDockerComposeStatusSnapshot returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("docker snapshot = %#v, want empty map", got)
	}
}
```

- [ ] **Step 2: Write failing systemd parser tests**

Append these tests to `pkg/catch/status_snapshot_test.go`:

```go
func TestParseSystemdShowStatusSnapshotMapsUnitStates(t *testing.T) {
	raw := strings.Join([]string{
		"Id=api.service",
		"LoadState=loaded",
		"ActiveState=active",
		"SubState=running",
		"",
		"Id=worker.service",
		"LoadState=loaded",
		"ActiveState=failed",
		"SubState=failed",
		"",
		"Id=missing.service",
		"LoadState=not-found",
		"ActiveState=inactive",
		"SubState=dead",
		"",
		"Id=odd.service",
		"LoadState=loaded",
		"ActiveState=",
		"SubState=",
	}, "\n")

	got := parseSystemdShowStatusSnapshot([]byte(raw))
	want := map[string]svc.Status{
		"api.service":     svc.StatusRunning,
		"worker.service":  svc.StatusStopped,
		"missing.service": svc.StatusStopped,
		"odd.service":     svc.StatusUnknown,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemd snapshot = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 3: Run the snapshot parser tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestParse(DockerComposeStatusSnapshot|SystemdShowStatusSnapshot)' -count=1
```

Expected: FAIL with undefined parser functions.

- [ ] **Step 4: Add `pkg/catch/status_snapshot.go` parser code**

Create `pkg/catch/status_snapshot.go` with this content:

```go
package catch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/svc"
)

type statusSnapshotCommandContext func(context.Context, string, ...string) *exec.Cmd

var newStatusSnapshotCommand statusSnapshotCommandContext = exec.CommandContext

type dockerPSStatusRow struct {
	Labels string `json:"Labels"`
	State  string `json:"State"`
}

func parseDockerComposeStatusSnapshot(raw []byte) (map[string]svc.DockerComposeStatus, error) {
	statuses := make(map[string]svc.DockerComposeStatus)
	parsedRows := 0
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row dockerPSStatusRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			log.Printf("unexpected docker ps status output: %s", line)
			continue
		}
		parsedRows++
		labels := parseDockerPSLabels(row.Labels)
		project := labels["com.docker.compose.project"]
		component := labels["com.docker.compose.service"]
		serviceName, ok := statusServiceNameFromComposeProject(project)
		if !ok || component == "" {
			continue
		}
		if statuses[serviceName] == nil {
			statuses[serviceName] = make(svc.DockerComposeStatus)
		}
		statuses[serviceName][component] = svc.DockerComposeStateStatus(row.State)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan docker ps status output: %w", err)
	}
	if parsedRows == 0 && strings.TrimSpace(string(raw)) != "" {
		return nil, fmt.Errorf("no valid docker status rows")
	}
	return statuses, nil
}

func parseDockerPSLabels(raw string) map[string]string {
	labels := make(map[string]string)
	for _, field := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		labels[key] = strings.TrimSpace(value)
	}
	return labels
}

func statusServiceNameFromComposeProject(project string) (string, bool) {
	serviceName, ok := strings.CutPrefix(project, "catch-")
	return serviceName, ok && serviceName != ""
}

func parseSystemdShowStatusSnapshot(raw []byte) map[string]svc.Status {
	out := make(map[string]svc.Status)
	current := make(map[string]string)
	flush := func() {
		id := strings.TrimSpace(current["Id"])
		if id != "" {
			out[id] = systemdShowStateStatus(current["LoadState"], current["ActiveState"])
		}
		current = make(map[string]string)
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		current[key] = value
	}
	flush()
	return out
}

func systemdShowStateStatus(loadState, activeState string) svc.Status {
	switch strings.TrimSpace(activeState) {
	case "active":
		return svc.StatusRunning
	case "inactive", "failed", "deactivating", "activating":
		return svc.StatusStopped
	}
	if strings.TrimSpace(loadState) == "not-found" {
		return svc.StatusStopped
	}
	return svc.StatusUnknown
}
```

- [ ] **Step 5: Add command collectors to `pkg/catch/status_snapshot.go`**

Append this code to the same file:

```go
func collectDockerComposeStatusSnapshot(ctx context.Context, newCmd statusSnapshotCommandContext) (map[string]svc.DockerComposeStatus, error) {
	cmd := newCmd(ctx, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project", "--format", "{{json .}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps status snapshot: %w", err)
	}
	statuses, err := parseDockerComposeStatusSnapshot(out)
	if err != nil {
		return nil, fmt.Errorf("parse docker ps status snapshot: %w", err)
	}
	return statuses, nil
}

func collectSystemdStatusSnapshot(ctx context.Context, newCmd statusSnapshotCommandContext, units []string) (map[string]svc.Status, error) {
	units = sortedUniqueNonEmpty(units)
	if len(units) == 0 {
		return map[string]svc.Status{}, nil
	}
	args := append([]string{"show", "--property=Id,LoadState,ActiveState,SubState"}, units...)
	cmd := newCmd(ctx, "systemctl", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show status snapshot: %w", err)
	}
	return parseSystemdShowStatusSnapshot(out), nil
}

func sortedUniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}
```

- [ ] **Step 6: Run snapshot parser tests and verify they pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestParse(DockerComposeStatusSnapshot|SystemdShowStatusSnapshot)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the snapshot parsers**

Run:

```bash
but diff
```

Expected: only `pkg/catch/status_snapshot.go` and
`pkg/catch/status_snapshot_test.go` are included. Copy the file change IDs, then
run:

```bash
but commit codex/fast-status-snapshots -m "pkg/catch: parse status snapshots" --changes <ids-from-but-diff>
```

Expected: GitButler creates one commit on `codex/fast-status-snapshots`.

## Task 4: Assemble Service Status From Snapshots

**Files:**
- Modify: `pkg/catch/status_snapshot.go`
- Modify: `pkg/catch/status_snapshot_test.go`

- [ ] **Step 1: Write failing service assembly test**

Append this test to `pkg/catch/status_snapshot_test.go`:

```go
func TestBuildStatusDataFromSnapshotsMapsConfiguredServices(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/web.yml"}},
	})
	seedService(t, server, "missing-web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/missing.yml"}},
	})
	seedService(t, server, "api", db.ServiceTypeSystemd, nil)
	seedService(t, server, "timer", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/timer.timer"}},
	})
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB returned error: %v", err)
	}
	statuses, err := server.buildStatusDataFromSnapshots(dv, map[string]svc.DockerComposeStatus{
		"web": {"api": svc.StatusRunning},
	}, map[string]svc.Status{
		"api.service":             svc.StatusRunning,
		"timer.timer":             svc.StatusStopped,
		"yeet-vm-devbox.service":  svc.StatusRunning,
	})
	if err != nil {
		t.Fatalf("buildStatusDataFromSnapshots returned error: %v", err)
	}

	got := statusByName(statuses)
	assertComponents(t, got["web"], []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}})
	assertComponents(t, got["missing-web"], []ComponentStatusData{{Name: "missing-web", Status: ComponentStatusUnknown}})
	assertComponents(t, got["api"], []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}})
	assertComponents(t, got["timer"], []ComponentStatusData{{Name: "timer", Status: ComponentStatusStopped}})
	assertComponents(t, got["devbox"], []ComponentStatusData{{Name: "devbox", Status: ComponentStatusRunning}})
}

func statusByName(statuses []ServiceStatusData) map[string]ServiceStatusData {
	out := make(map[string]ServiceStatusData, len(statuses))
	for _, status := range statuses {
		out[status.ServiceName] = status
	}
	return out
}

func assertComponents(t *testing.T, status ServiceStatusData, want []ComponentStatusData) {
	t.Helper()
	if !reflect.DeepEqual(status.ComponentStatus, want) {
		t.Fatalf("%s components = %#v, want %#v", status.ServiceName, status.ComponentStatus, want)
	}
}
```

Add this import to `pkg/catch/status_snapshot_test.go`:

```go
	"github.com/yeetrun/yeet/pkg/db"
```

- [ ] **Step 2: Run the service assembly test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestBuildStatusDataFromSnapshotsMapsConfiguredServices -count=1
```

Expected: FAIL with undefined `buildStatusDataFromSnapshots`.

- [ ] **Step 3: Add service assembly code**

Append this code to `pkg/catch/status_snapshot.go`:

```go
func (s *Server) buildStatusDataFromSnapshots(dv *db.DataView, dockerStatuses map[string]svc.DockerComposeStatus, unitStatuses map[string]svc.Status) ([]ServiceStatusData, error) {
	services := dv.AsStruct().Services
	statuses := make([]ServiceStatusData, 0, len(services))

	for _, name := range serviceNamesByType(services, db.ServiceTypeSystemd) {
		status, err := s.systemdSnapshotStatusData(name, unitStatuses)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	for _, name := range serviceNamesByType(services, db.ServiceTypeDockerCompose) {
		statuses = append(statuses, composeServiceStatusData(name, s.serviceDataTypeOrDockerSnapshot(name), dockerStatuses[name]))
	}
	for _, name := range serviceNamesByType(services, db.ServiceTypeVM) {
		status := unitStatuses[vmSystemdUnitName(name)]
		if status == "" {
			status = svc.StatusUnknown
		}
		statuses = append(statuses, serviceStatusWithComponent(name, ServiceDataTypeVM, name, status))
	}
	return statuses, nil
}

func (s *Server) systemdSnapshotStatusData(name string, unitStatuses map[string]svc.Status) (ServiceStatusData, error) {
	service, err := s.serviceView(name)
	if err != nil {
		return ServiceStatusData{}, err
	}
	unit, err := s.primaryUnitForServiceView(service)
	if err != nil {
		return ServiceStatusData{}, err
	}
	status := unitStatuses[unit]
	if status == "" {
		status = svc.StatusUnknown
	}
	return serviceStatusWithComponent(name, ServiceDataTypeForService(service), name, status), nil
}

func (s *Server) serviceDataTypeOrDockerSnapshot(name string) ServiceDataType {
	if service, err := s.serviceView(name); err == nil {
		return ServiceDataTypeForService(service)
	}
	return ServiceDataTypeDocker
}

func (s *Server) primaryUnitForServiceView(service db.ServiceView) (string, error) {
	root := s.serviceRootFromView(service)
	systemd, err := svc.NewSystemdService(s.cfg.DB, service, serviceRunDirForRoot(root))
	if err != nil {
		return "", fmt.Errorf("load systemd service %s: %w", service.Name(), err)
	}
	return systemd.PrimaryUnit(), nil
}

func (s *Server) statusSnapshotUnitNames(dv *db.DataView) ([]string, error) {
	services := dv.AsStruct().Services
	units := make([]string, 0)
	for _, name := range serviceNamesByType(services, db.ServiceTypeSystemd) {
		service, err := s.serviceView(name)
		if err != nil {
			return nil, err
		}
		unit, err := s.primaryUnitForServiceView(service)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	for _, name := range serviceNamesByType(services, db.ServiceTypeVM) {
		units = append(units, vmSystemdUnitName(name))
	}
	return sortedUniqueNonEmpty(units), nil
}
```

Add this import to `pkg/catch/status_snapshot.go`:

```go
	"github.com/yeetrun/yeet/pkg/db"
```

- [ ] **Step 4: Run the service assembly test and verify it passes**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestBuildStatusDataFromSnapshotsMapsConfiguredServices -count=1
```

Expected: PASS.

- [ ] **Step 5: Run all snapshot tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'Test(ParseDockerComposeStatusSnapshot|ParseSystemdShowStatusSnapshot|BuildStatusDataFromSnapshots)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit snapshot assembly**

Run:

```bash
but diff
```

Expected: only `pkg/catch/status_snapshot.go` and
`pkg/catch/status_snapshot_test.go` are included. Copy the file change IDs, then
run:

```bash
but commit codex/fast-status-snapshots -m "pkg/catch: assemble status snapshots" --changes <ids-from-but-diff>
```

Expected: GitButler creates one commit on `codex/fast-status-snapshots`.

## Task 5: Wire Catch Status To Snapshot Collection

**Files:**
- Modify: `pkg/catch/status_snapshot.go`
- Modify: `pkg/catch/tty_service.go`
- Modify: `pkg/catch/tty_service_test.go`

- [ ] **Step 1: Add a failing regression test for snapshot-backed status**

Append this test to `pkg/catch/tty_service_test.go`:

```go
func TestSystemStatusDataUsesSnapshotCollectorWithoutLegacyHooks(t *testing.T) {
	oldNewStatusSnapshotCommand := newStatusSnapshotCommand
	t.Cleanup(func() { newStatusSnapshotCommand = oldNewStatusSnapshotCommand })

	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/web.yml"}},
	})
	seedService(t, server, "api", db.ServiceTypeSystemd, nil)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	newStatusSnapshotCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "docker":
			return fakeStatusSnapshotCommand(t, `{"State":"running","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=app"}`)
		case "systemctl":
			got := append([]string{name}, args...)
			joined := strings.Join(got, " ")
			if !strings.Contains(joined, "api.service") || !strings.Contains(joined, "yeet-vm-devbox.service") {
				t.Fatalf("systemctl args = %q, want api and VM units", joined)
			}
			return fakeStatusSnapshotCommand(t, strings.Join([]string{
				"Id=api.service",
				"LoadState=loaded",
				"ActiveState=active",
				"SubState=running",
				"",
				"Id=yeet-vm-devbox.service",
				"LoadState=loaded",
				"ActiveState=inactive",
				"SubState=dead",
			}, "\n"))
		default:
			t.Fatalf("unexpected command %s %v", name, args)
			return fakeStatusSnapshotCommand(t, "")
		}
	}

	statuses, err := (&ttyExecer{s: server, sn: SystemService}).systemStatusData()
	if err != nil {
		t.Fatalf("systemStatusData returned error: %v", err)
	}
	got := statusByName(statuses)
	assertComponents(t, got["web"], []ComponentStatusData{{Name: "app", Status: ComponentStatusRunning}})
	assertComponents(t, got["api"], []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}})
	assertComponents(t, got["devbox"], []ComponentStatusData{{Name: "devbox", Status: ComponentStatusStopped}})
}
```

Add helper command support near the bottom of `pkg/catch/tty_service_test.go`:

```go
func fakeStatusSnapshotCommand(t *testing.T, output string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestStatusSnapshotFakeCommand", "--", output)
	cmd.Env = append(os.Environ(), "GO_WANT_STATUS_SNAPSHOT_HELPER=1")
	return cmd
}

func TestStatusSnapshotFakeCommand(t *testing.T) {
	if os.Getenv("GO_WANT_STATUS_SNAPSHOT_HELPER") != "1" {
		return
	}
	if len(os.Args) > 0 {
		fmt.Print(os.Args[len(os.Args)-1])
	}
	os.Exit(0)
}
```

Add these imports to the existing import block in `pkg/catch/tty_service_test.go`:

```go
	"fmt"
	"os"
```

- [ ] **Step 2: Run the snapshot wiring test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestSystemStatusDataUsesSnapshotCollectorWithoutLegacyHooks -count=1
```

Expected: FAIL because `systemStatusData` still uses the legacy per-service
status path.

- [ ] **Step 3: Add snapshot collection orchestration**

Append this code to `pkg/catch/status_snapshot.go`:

```go
func (s *Server) collectStatusSnapshot(ctx context.Context, newCmd statusSnapshotCommandContext) ([]ServiceStatusData, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get status snapshot services: %w", err)
	}
	services := dv.AsStruct().Services
	dockerStatuses := map[string]svc.DockerComposeStatus{}
	if len(serviceNamesByType(services, db.ServiceTypeDockerCompose)) > 0 {
		dockerStatuses, err = collectDockerComposeStatusSnapshot(ctx, newCmd)
		if err != nil {
			return nil, err
		}
	}
	units, err := s.statusSnapshotUnitNames(dv)
	if err != nil {
		return nil, err
	}
	unitStatuses, err := collectSystemdStatusSnapshot(ctx, newCmd, units)
	if err != nil {
		return nil, err
	}
	return s.buildStatusDataFromSnapshots(dv, dockerStatuses, unitStatuses)
}
```

- [ ] **Step 4: Preserve legacy test hooks and wire production status**

In `pkg/catch/tty_service.go`, replace `systemStatusData` with this version
and move the old body into `legacySystemStatusData`:

```go
func (e *ttyExecer) systemStatusData() ([]ServiceStatusData, error) {
	if e.hasLegacyStatusHooks() {
		return e.legacySystemStatusData()
	}
	if e.s == nil {
		return nil, fmt.Errorf("status snapshot server is nil")
	}
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return e.s.collectStatusSnapshot(ctx, newStatusSnapshotCommand)
}

func (e *ttyExecer) hasLegacyStatusHooks() bool {
	return e.systemdStatusFunc != nil ||
		e.systemdStatusesFunc != nil ||
		e.dockerComposeStatusFunc != nil ||
		e.dockerComposeStatusesFunc != nil
}

func (e *ttyExecer) legacySystemStatusData() ([]ServiceStatusData, error) {
	statuses, err := e.systemdStatusData()
	if err != nil {
		return nil, err
	}
	composeStatuses, err := e.dockerComposeStatusData()
	if err != nil {
		return nil, err
	}
	vmStatuses, err := e.vmStatusData()
	if err != nil {
		return nil, err
	}
	statuses = append(statuses, composeStatuses...)
	return append(statuses, vmStatuses...), nil
}
```

- [ ] **Step 5: Run catch status tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestSystemStatusData|TestStatusCmdFunc|TestRenderServiceStatuses|TestSingle.*Status' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run full catch package tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit snapshot wiring**

Run:

```bash
but diff
```

Expected: only `pkg/catch/status_snapshot.go`,
`pkg/catch/tty_service.go`, and `pkg/catch/tty_service_test.go` are included.
Copy the file change IDs, then run:

```bash
but commit codex/fast-status-snapshots -m "pkg/catch: collect status snapshots in bulk" --changes <ids-from-but-diff>
```

Expected: GitButler creates one commit on `codex/fast-status-snapshots`.

## Task 6: Package Verification And Live Timing

**Files:**
- Verify: `pkg/svc`
- Verify: `pkg/yeet`
- Verify: `pkg/catch`
- Verify live command behavior from `/Users/shayne/yeet-services`

- [ ] **Step 1: Run targeted package tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./pkg/catch ./pkg/svc -count=1
```

Expected: PASS.

- [ ] **Step 2: Run command-routing tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 4: Install updated catch on live hosts before timing**

Run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
CATCH_HOST=yeet-hetz mise exec -- go run ./cmd/yeet init root@hetz
```

Expected: both commands complete successfully. If either host fails to install,
stop and report the failing command and output before doing live timing.

- [ ] **Step 5: Verify multi-host JSON from the services workspace**

Run:

```bash
cd /Users/shayne/yeet-services
/usr/bin/time -p yeet status --format=json >/tmp/yeet-status.json
jq 'length' /tmp/yeet-status.json
```

Expected: `yeet status --format=json` exits 0, and `jq 'length'` prints `2`
for the two configured hosts.

- [ ] **Step 6: Time plain multi-host status**

Run:

```bash
cd /Users/shayne/yeet-services
for i in 1 2 3; do /usr/bin/time -p yeet status >/tmp/yeet-status-table-$i.txt; done
```

Expected: all three runs exit 0. The wall-clock times should be materially
below the measured 1.1 to 1.8 second baseline.

- [ ] **Step 7: Time the pve1 host path**

Run:

```bash
cd /Users/shayne/yeet-services
for i in 1 2 3; do /usr/bin/time -p yeet --host yeet-pve1 status --format=json >/tmp/yeet-status-pve1-$i.json; done
```

Expected: all three runs exit 0 and the `real` times are materially below the
measured 1.15 to 1.62 second baseline for `yeet-pve1`.

- [ ] **Step 8: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: PASS. If the Go hooks need the repo-managed toolchain, rerun with:

```bash
mise exec -- pre-commit run --all-files
```

- [ ] **Step 9: Commit verification fixes if any were needed**

Run:

```bash
but diff
```

Expected: no uncommitted changes. If verification required small fixes, commit
only those changed files with:

```bash
but commit codex/fast-status-snapshots -m "status: finish snapshot verification fixes" --changes <ids-from-but-diff>
```

Expected: either no commit is needed, or GitButler creates one final fix commit
on `codex/fast-status-snapshots`.
