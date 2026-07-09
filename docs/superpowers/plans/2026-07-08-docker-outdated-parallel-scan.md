# Docker Outdated Parallel Scan Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make host-wide `yeet docker outdated` and the discovery phase of `yeet docker update --outdated` scan Docker compose services concurrently on each catch host.

**Architecture:** Keep the client path unchanged because it already fans out hosts concurrently. Add a small catch-side worker pool under `Server.DockerComposeOutdatedAll`, with an internal worker limit of 8. Preserve existing row semantics by converting per-service failures into `DockerOutdatedError` rows and sorting all rows before rendering.

**Tech Stack:** Go, `pkg/catch`, `pkg/svc`, existing `testing` package, existing GitButler workflow.

## Global Constraints

- Preserve current `yeet docker outdated` table and JSON output shape.
- Preserve current `yeet docker update --outdated` service selection.
- Do not add a CLI flag for concurrency.
- Keep service-scoped `yeet docker outdated SERVICE` on the existing single-service path.
- Host-wide service scan failures remain row-level errors.
- Database load failures remain host-level errors.
- Context cancellation returns the context error instead of partial rows.
- Do not make Docker update execution parallel in this change.
- Use `mise exec -- go ...` for Go commands.
- Use GitButler for commits.

---

## File Structure

- Modify `pkg/catch/catch.go`
  - Add `dockerComposeOutdatedAllWorkerLimit`.
  - Add `dockerComposeOutdatedServiceScan`.
  - Add unexported helper `dockerComposeOutdatedAll`.
  - Change `Server.DockerComposeOutdatedAll` to collect service names and call the helper.

- Modify `pkg/catch/catch_test.go`
  - Add focused tests for bounded concurrency, row-level service errors, sorting, empty service lists, and context cancellation.
  - Add small local test helpers for timeout-based channel waits and atomic max tracking.

No docs or CLI help changes are required because behavior and syntax do not change.

---

### Task 1: Add Bounded Catch-Side Parallel Scanning

**Files:**
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/catch_test.go`

**Interfaces:**
- Consumes: `sortDockerOutdatedRows(rows []svc.DockerOutdatedRow)`, `serviceNamesByType(services map[string]*db.Service, serviceType db.ServiceType) []string`, and `(*Server).DockerComposeOutdated(ctx context.Context, sn string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error)`.
- Produces:
  - `const dockerComposeOutdatedAllWorkerLimit = 8`
  - `type dockerComposeOutdatedServiceScan func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error)`
  - `func dockerComposeOutdatedAll(ctx context.Context, serviceNames []string, scan dockerComposeOutdatedServiceScan) ([]svc.DockerOutdatedRow, error)`

- [ ] **Step 1: Write failing tests for helper behavior**

In `pkg/catch/catch_test.go`, add imports for `fmt`, `sync/atomic`, and `time`.

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/tailcfg"
)
```

Add these tests near `TestStatusesServiceNamesByTypeFiltersAndSorts`:

```go
func TestDockerComposeOutdatedAllScansWithBoundedConcurrency(t *testing.T) {
	serviceCount := dockerComposeOutdatedAllWorkerLimit + 3
	serviceNames := make([]string, 0, serviceCount)
	for i := 0; i < serviceCount; i++ {
		serviceNames = append(serviceNames, fmt.Sprintf("svc-%02d", i))
	}

	started := make(chan string, serviceCount)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32

	scan := func(ctx context.Context, sn string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		if opts.IncludeInternal {
			t.Errorf("IncludeInternal = true, want false for host-wide scan")
		}
		current := active.Add(1)
		recordDockerOutdatedMaxActive(&maxActive, current)
		started <- sn
		select {
		case <-release:
		case <-ctx.Done():
			active.Add(-1)
			return nil, ctx.Err()
		}
		active.Add(-1)
		return []svc.DockerOutdatedRow{{
			ServiceName:   sn,
			ContainerName: "app",
			Image:         "ghcr.io/acme/" + sn + ":latest",
			Status:        svc.DockerOutdatedUpdateAvailable,
		}}, nil
	}

	type result struct {
		rows []svc.DockerOutdatedRow
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := dockerComposeOutdatedAll(context.Background(), serviceNames, scan)
		done <- result{rows: rows, err: err}
	}()

	for i := 0; i < dockerComposeOutdatedAllWorkerLimit; i++ {
		waitForDockerOutdatedStart(t, started)
	}
	select {
	case sn := <-started:
		t.Fatalf("scan for %q started before worker limit released", sn)
	default:
	}
	select {
	case got := <-done:
		t.Fatalf("scan returned before release: rows=%#v err=%v", got.rows, got.err)
	default:
	}

	close(release)
	got := waitForDockerOutdatedResult(t, done)
	if got.err != nil {
		t.Fatalf("dockerComposeOutdatedAll: %v", got.err)
	}
	if gotMax := int(maxActive.Load()); gotMax != dockerComposeOutdatedAllWorkerLimit {
		t.Fatalf("max active scans = %d, want %d", gotMax, dockerComposeOutdatedAllWorkerLimit)
	}
	if len(got.rows) != serviceCount {
		t.Fatalf("rows = %d, want %d", len(got.rows), serviceCount)
	}
	for i, row := range got.rows {
		if row.ServiceName != serviceNames[i] {
			t.Fatalf("row %d service = %q, want %q", i, row.ServiceName, serviceNames[i])
		}
	}
}

func TestDockerComposeOutdatedAllPreservesErrorRowsAndSorts(t *testing.T) {
	serviceNames := []string{"zeta", "bad", "alpha"}
	scan := func(_ context.Context, sn string, _ svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		if sn == "bad" {
			return nil, errors.New("registry unavailable")
		}
		return []svc.DockerOutdatedRow{{
			ServiceName:   sn,
			ContainerName: "app",
			Image:         "ghcr.io/acme/" + sn + ":latest",
			Status:        svc.DockerOutdatedUpdateAvailable,
		}}, nil
	}

	rows, err := dockerComposeOutdatedAll(context.Background(), serviceNames, scan)
	if err != nil {
		t.Fatalf("dockerComposeOutdatedAll: %v", err)
	}
	gotServices := []string{rows[0].ServiceName, rows[1].ServiceName, rows[2].ServiceName}
	if !reflect.DeepEqual(gotServices, []string{"alpha", "bad", "zeta"}) {
		t.Fatalf("service order = %v", gotServices)
	}
	if rows[1].Status != svc.DockerOutdatedError || rows[1].Reason != "registry unavailable" {
		t.Fatalf("error row = %#v", rows[1])
	}
}

func TestDockerComposeOutdatedAllContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan string, 2)
	scan := func(ctx context.Context, sn string, _ svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		started <- sn
		<-ctx.Done()
		return nil, ctx.Err()
	}

	type result struct {
		rows []svc.DockerOutdatedRow
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := dockerComposeOutdatedAll(ctx, []string{"alpha", "beta"}, scan)
		done <- result{rows: rows, err: err}
	}()

	waitForDockerOutdatedStart(t, started)
	cancel()
	got := waitForDockerOutdatedResult(t, done)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", got.err)
	}
	if len(got.rows) != 0 {
		t.Fatalf("rows = %#v, want none on cancellation", got.rows)
	}
}

func TestDockerComposeOutdatedAllEmptyServices(t *testing.T) {
	called := false
	rows, err := dockerComposeOutdatedAll(context.Background(), nil, func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("dockerComposeOutdatedAll: %v", err)
	}
	if called {
		t.Fatal("scan called for empty service list")
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want none", rows)
	}
}
```

Add these helpers near the new tests:

```go
func recordDockerOutdatedMaxActive(maxActive *atomic.Int32, current int32) {
	for {
		previous := maxActive.Load()
		if current <= previous || maxActive.CompareAndSwap(previous, current) {
			return
		}
	}
}

func waitForDockerOutdatedStart(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case sn := <-started:
		return sn
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for docker outdated scan to start")
		return ""
	}
}

func waitForDockerOutdatedResult[T any](t *testing.T, done <-chan T) T {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for docker outdated scan result")
		var zero T
		return zero
	}
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestDockerComposeOutdatedAll(ScansWithBoundedConcurrency|PreservesErrorRowsAndSorts|ContextCancellation|EmptyServices)' -count=1
```

Expected: FAIL at compile time because `dockerComposeOutdatedAll` and `dockerComposeOutdatedAllWorkerLimit` do not exist.

- [ ] **Step 3: Implement the helper and worker pool**

In `pkg/catch/catch.go`, add this near `var errNoServiceConfigured`:

```go
const dockerComposeOutdatedAllWorkerLimit = 8

type dockerComposeOutdatedServiceScan func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error)
```

Replace `Server.DockerComposeOutdatedAll` with:

```go
func (s *Server) DockerComposeOutdatedAll(ctx context.Context) ([]svc.DockerOutdatedRow, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %v", err)
	}
	serviceNames := serviceNamesByType(dv.AsStruct().Services, db.ServiceTypeDockerCompose)
	return dockerComposeOutdatedAll(ctx, serviceNames, func(ctx context.Context, sn string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		return s.DockerComposeOutdated(ctx, sn, opts)
	})
}
```

Add this helper below `Server.DockerComposeOutdatedAll`:

```go
func dockerComposeOutdatedAll(ctx context.Context, serviceNames []string, scan dockerComposeOutdatedServiceScan) ([]svc.DockerOutdatedRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(serviceNames) == 0 {
		return nil, nil
	}

	workers := dockerComposeOutdatedAllWorkerLimit
	if workers < 1 {
		workers = 1
	}
	if workers > len(serviceNames) {
		workers = len(serviceNames)
	}

	jobs := make(chan string)
	results := make(chan []svc.DockerOutdatedRow, len(serviceNames))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sn := range jobs {
				if err := ctx.Err(); err != nil {
					return
				}
				serviceRows, err := scan(ctx, sn, svc.DockerOutdatedOptions{})
				if err != nil {
					serviceRows = []svc.DockerOutdatedRow{{
						ServiceName: sn,
						Status:      svc.DockerOutdatedError,
						Reason:      err.Error(),
					}}
				}
				select {
				case results <- serviceRows:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	sendErr := error(nil)
sendLoop:
	for _, sn := range serviceNames {
		select {
		case jobs <- sn:
		case <-ctx.Done():
			sendErr = ctx.Err()
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	close(results)

	if sendErr != nil {
		return nil, sendErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rows := make([]svc.DockerOutdatedRow, 0)
	for serviceRows := range results {
		rows = append(rows, serviceRows...)
	}
	sortDockerOutdatedRows(rows)
	return rows, nil
}
```

- [ ] **Step 4: Format changed Go files**

Run:

```bash
mise exec -- gofmt -w pkg/catch/catch.go pkg/catch/catch_test.go
```

Expected: command exits 0 and rewrites only formatting.

- [ ] **Step 5: Run the focused tests and verify they pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestDockerComposeOutdatedAll(ScansWithBoundedConcurrency|PreservesErrorRowsAndSorts|ContextCancellation|EmptyServices)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run Docker-related package tests**

Run:

```bash
mise exec -- go test ./pkg/catch ./pkg/svc ./pkg/yeet -run 'Test.*Docker' -count=1
```

Expected: PASS.

- [ ] **Step 7: Run broader touched package tests**

Run:

```bash
mise exec -- go test ./pkg/catch ./pkg/svc ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the implementation**

Run:

```bash
but commit codex/docker-outdated-parallel-scan -c -m "catch: parallelize docker outdated scans"
```

Expected: GitButler creates a commit containing `pkg/catch/catch.go` and `pkg/catch/catch_test.go`.

---

### Task 2: Verify Live Read-Only Performance

**Files:**
- No file changes.

**Interfaces:**
- Consumes: committed `Server.DockerComposeOutdatedAll` concurrency implementation.
- Produces: measured before/after evidence for the final report.

- [ ] **Step 1: Build a local test binary**

Run:

```bash
mise exec -- go build -o /tmp/yeet-docker-outdated-parallel ./cmd/yeet
```

Expected: command exits 0 and creates `/tmp/yeet-docker-outdated-parallel`.

- [ ] **Step 2: Verify the live test host variable is set**

Run from a service workspace:

```bash
: "${YEET_OUTDATED_TEST_HOST:?set YEET_OUTDATED_TEST_HOST to a Docker-heavy catch host from local instructions}"
```

Expected: command exits 0. If it exits non-zero, set `YEET_OUTDATED_TEST_HOST` in the shell and rerun this step.

- [ ] **Step 3: Run a host-scoped live scan**

Run from a service workspace:

```bash
/usr/bin/time -p /tmp/yeet-docker-outdated-parallel --host="$YEET_OUTDATED_TEST_HOST" docker outdated
```

Expected: output columns stay `SERVICE HOST CONTAINER IMAGE UPDATE`. Runtime should be substantially below the pre-change host-wide baseline from the design spec.

- [ ] **Step 4: Run the normal all-host live scan**

Run from a service workspace:

```bash
/usr/bin/time -p /tmp/yeet-docker-outdated-parallel docker outdated
```

Expected: output columns stay `SERVICE HOST CONTAINER IMAGE UPDATE`. Runtime should be dominated by the slowest host rather than the sum of services on that host.

- [ ] **Step 5: Confirm update discovery still selects the same services without updating**

Run from a service workspace:

```bash
/tmp/yeet-docker-outdated-parallel docker outdated --format=json | jq -r '.[].rows[] | select(.status == "update available") | [.serviceName, .containerName, .image] | @tsv'
```

Expected: service/container/image rows match the released `yeet docker outdated --format=json` selection for `update available` rows.

- [ ] **Step 6: Record final verification notes**

Add the measured live runtimes and the test commands from Tasks 1 and 2 to the final implementation summary. Do not commit a measurement artifact.
