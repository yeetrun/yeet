# Complete Host Storage Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet host set` complete and repair host storage migrations so active DB refs, generated artifacts, installed units, running services, catch, and local `yeet.toml` all converge on the requested data dir and services root.

**Architecture:** Add a host storage reference layer that builds ordered root mappings, scans DB/artifacts/systemd for active old-root refs, rewrites typed DB state and generated files, regenerates units, restarts affected services, and validates that active old-root refs are gone. Keep catch authoritative for remote state and mutation; keep yeet responsible for rendering, confirmation, and local config edits.

**Tech Stack:** Go via `mise exec`, catch JSON-RPC types in `pkg/catchrpc`, catch-side storage orchestration in `pkg/catch`, local CLI orchestration in `pkg/yeet`, existing `svc.SystemdService` installer, existing VM systemd rendering, GitButler for commits.

---

## File Map

- Create `pkg/catch/host_storage_refs.go`: ordered path mappings, active reference scan structs, DB reference scanning helpers, systemd reference scan helpers, validation result types.
- Create `pkg/catch/host_storage_refs_test.go`: mapping precedence, DB scan, systemd scan, validation classification.
- Create `pkg/catch/host_storage_db_rewrite.go`: typed DB rewrite helpers for artifact refs and VM path fields.
- Create `pkg/catch/host_storage_db_rewrite_test.go`: target `db.json` rewrite tests.
- Create `pkg/catch/host_storage_artifacts.go`: generated artifact rewrite and reinstall helpers for host-level storage repair.
- Create `pkg/catch/host_storage_artifacts_test.go`: generated artifact rewrite and reinstall selection tests.
- Modify `pkg/catch/host_storage.go`: plan repair actions, apply rewrite/reinstall/validation in transaction order, extend result fields.
- Modify `pkg/catch/host_storage_test.go`: apply ordering, repair-only planning, active-reference validation.
- Modify `pkg/catch/vm_systemd.go`: expose a regeneration helper for VM systemd units from typed service state.
- Modify `pkg/catch/vm_runner_test.go` or create `pkg/catch/vm_systemd_test.go`: VM unit regeneration path tests.
- Modify `pkg/catchrpc/types.go` and `pkg/catchrpc/types_test.go`: add host storage repair/reference details to plan/apply wire types.
- Modify `pkg/yeet/host_set.go` and `pkg/yeet/host_set_test.go`: render repair actions and update local config by removing default-root pins.
- Modify `pkg/yeet/project_config.go` and `pkg/yeet/project_config_test.go`: add and test a helper that clears `service_root` and `service_root_zfs`.
- Modify `README.md`, `website/docs`, and `.codex/skills/yeet-cli/references/yeet-help-agent.md` only after behavior lands.

---

## Task 1: Add Ordered Host Storage Path Mappings

**Files:**
- Create: `pkg/catch/host_storage_refs.go`
- Create: `pkg/catch/host_storage_refs_test.go`
- Modify: `pkg/catch/host_storage.go`

- [ ] **Step 1: Write failing mapping tests**

Add this test to `pkg/catch/host_storage_refs_test.go`:

```go
package catch

import (
	"path/filepath"
	"testing"
)

func TestHostStoragePathMapRewritesLongestPrefixFirst(t *testing.T) {
	mappings := hostStoragePathMappings{
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
		{From: "/root/data/services/nginx", To: "/flash/yeet/services/nginx", Reason: hostStoragePathReasonServiceRoot, Service: "nginx"},
	}
	mappings = mappings.Sorted()

	tests := map[string]string{
		"/root/data/services/catch/run/catch":  "/flash/yeet/services/catch/run/catch",
		"/root/data/services/nginx/data/file": "/flash/yeet/services/nginx/data/file",
		"/root/data/tsd/tailscaled-1.2.3":    "/flash/yeet/data/tsd/tailscaled-1.2.3",
		"/root/database/file":                 "/root/database/file",
		"relative/path":                       "relative/path",
	}
	for input, want := range tests {
		got, changed, err := mappings.Rewrite(input)
		if err != nil {
			t.Fatalf("Rewrite(%q) error: %v", input, err)
		}
		if got != filepath.Clean(want) {
			t.Fatalf("Rewrite(%q) = %q, want %q", input, got, want)
		}
		if changed != (input != want && filepath.Clean(input) != filepath.Clean(want)) {
			t.Fatalf("Rewrite(%q) changed = %v", input, changed)
		}
	}
}
```

Run:

```sh
mise exec -- go test ./pkg/catch -run TestHostStoragePathMapRewritesLongestPrefixFirst -count=1
```

Expected: FAIL with undefined `hostStoragePathMappings`.

- [ ] **Step 2: Implement mapping types**

Create `pkg/catch/host_storage_refs.go` with:

```go
package catch

import (
	"path/filepath"
	"slices"
	"strings"
)

type hostStoragePathReason string

const (
	hostStoragePathReasonCatchRoot   hostStoragePathReason = "catch-root"
	hostStoragePathReasonServiceRoot hostStoragePathReason = "service-root"
	hostStoragePathReasonServicesDir hostStoragePathReason = "services-root"
	hostStoragePathReasonDataDir     hostStoragePathReason = "data-dir"
)

type hostStoragePathMapping struct {
	From    string
	To      string
	Reason  hostStoragePathReason
	Service string
}

type hostStoragePathMappings []hostStoragePathMapping

func (m hostStoragePathMappings) Sorted() hostStoragePathMappings {
	out := append(hostStoragePathMappings(nil), m...)
	slices.SortFunc(out, func(a, b hostStoragePathMapping) int {
		aFrom := cleanHostStoragePath(a.From)
		bFrom := cleanHostStoragePath(b.From)
		if len(aFrom) != len(bFrom) {
			return len(bFrom) - len(aFrom)
		}
		if c := strings.Compare(aFrom, bFrom); c != 0 {
			return c
		}
		return strings.Compare(string(a.Reason), string(b.Reason))
	})
	return out
}

func (m hostStoragePathMappings) Rewrite(value string) (string, bool, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return value, false, nil
	}
	for _, mapping := range m.Sorted() {
		rewritten, ok, err := relocatePathUnderRoot(value, mapping.From, mapping.To)
		if err != nil {
			return "", false, err
		}
		if ok {
			return rewritten, cleanHostStoragePath(rewritten) != cleanHostStoragePath(value), nil
		}
	}
	return filepath.Clean(value), false, nil
}
```

- [ ] **Step 3: Add a plan-to-mappings helper**

Add to `pkg/catch/host_storage_refs.go`:

```go
func hostStorageMappingsFromPlan(plan catchrpc.HostStoragePlan) hostStoragePathMappings {
	var mappings hostStoragePathMappings
	if plan.CatchAction.Move {
		mappings = append(mappings, hostStoragePathMapping{
			From:   plan.CatchAction.From,
			To:     plan.CatchAction.To,
			Reason: hostStoragePathReasonCatchRoot,
		})
	}
	for _, move := range plan.ServicesAction.AffectedServices {
		mappings = append(mappings, hostStoragePathMapping{
			From:    move.From,
			To:      move.To,
			Reason:  hostStoragePathReasonServiceRoot,
			Service: move.Name,
		})
	}
	if !hostStoragePathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot) {
		mappings = append(mappings, hostStoragePathMapping{
			From:   plan.Current.ServicesRoot,
			To:     plan.Desired.ServicesRoot,
			Reason: hostStoragePathReasonServicesDir,
		})
	}
	if plan.DataDirAction.Move {
		mappings = append(mappings, hostStoragePathMapping{
			From:   plan.DataDirAction.From,
			To:     plan.DataDirAction.To,
			Reason: hostStoragePathReasonDataDir,
		})
	}
	return mappings.Sorted()
}
```

Add import to `pkg/catch/host_storage_refs.go`:

```go
import "github.com/yeetrun/yeet/pkg/catchrpc"
```

- [ ] **Step 4: Run tests**

Run:

```sh
mise exec -- go test ./pkg/catch -run 'TestHostStoragePathMap|TestHostStorageMappingsFromPlan' -count=1
```

Expected: PASS for the mapping test; no `TestHostStorageMappingsFromPlan` exists yet unless added during implementation.

- [ ] **Step 5: Commit**

Inspect and commit only the task files:

```sh
but diff pkg/catch/host_storage_refs.go pkg/catch/host_storage_refs_test.go
but commit codex/host-storage-reconfigure-design -m "catch: model host storage path mappings" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 2: Scan Active Host Storage References

**Files:**
- Modify: `pkg/catch/host_storage_refs.go`
- Modify: `pkg/catch/host_storage_refs_test.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`

- [ ] **Step 1: Write failing DB and systemd scan tests**

Add to `pkg/catch/host_storage_refs_test.go`:

```go
func TestHostStorageScanDataFindsOldRootRefs(t *testing.T) {
	data := &db.Data{
		Services: map[string]*db.Service{
			"catch": {
				Name:       "catch",
				Generation: 3,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(3): "/root/data/services/catch/bin/catch",
					}},
				},
			},
			"devbox": {
				Name: "devbox",
				VM: &db.VMConfig{
					Image: db.VMImageConfig{RootFS: "/root/data/vm-images/ubuntu/rootfs.ext4"},
				},
			},
		},
	}
	refs := scanHostStorageDataRefs(data, hostStoragePathMappings{{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir}})
	if len(refs) != 2 {
		t.Fatalf("refs len = %d, want 2: %#v", len(refs), refs)
	}
	if refs[0].Service == "" || refs[0].Path == "" || refs[0].Field == "" {
		t.Fatalf("first ref missing classification: %#v", refs[0])
	}
}

func TestHostStorageScanSystemdRefsFindsYeetUnitsOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "yeet-nginx-ns.service"), "ExecStart=/root/data/services/catch/data/service-ns\n")
	writeFile(t, filepath.Join(root, "unrelated.service"), "ExecStart=/root/data/bin/tool\n")

	refs, err := scanHostStorageSystemdRefs(root, hostStoragePathMappings{{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir}})
	if err != nil {
		t.Fatalf("scanHostStorageSystemdRefs error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1: %#v", len(refs), refs)
	}
	if refs[0].Unit != "yeet-nginx-ns.service" {
		t.Fatalf("unit = %q, want yeet-nginx-ns.service", refs[0].Unit)
	}
}
```

Expected failure: undefined `scanHostStorageDataRefs`, `scanHostStorageSystemdRefs`, and `hostStorageReference`.

- [ ] **Step 2: Add reference types**

Add to `pkg/catch/host_storage_refs.go`:

```go
type hostStorageReferenceKind string

const (
	hostStorageReferenceDB      hostStorageReferenceKind = "db"
	hostStorageReferenceSystemd hostStorageReferenceKind = "systemd"
)

type hostStorageReference struct {
	Kind    hostStorageReferenceKind
	Service string
	Field   string
	Path    string
	Unit    string
	File    string
	Line    int
}
```

- [ ] **Step 3: Implement typed DB scanning**

Add to `pkg/catch/host_storage_refs.go`:

```go
func scanHostStorageDataRefs(data *db.Data, mappings hostStoragePathMappings) []hostStorageReference {
	if data == nil {
		return nil
	}
	var refs []hostStorageReference
	for name, service := range data.Services {
		if service == nil {
			continue
		}
		serviceName := service.Name
		if serviceName == "" {
			serviceName = name
		}
		for artifactName, artifact := range service.Artifacts {
			if artifact == nil {
				continue
			}
			for ref, value := range artifact.Refs {
				if hostStorageValueMatchesMappings(value, mappings) {
					refs = append(refs, hostStorageReference{
						Kind:    hostStorageReferenceDB,
						Service: serviceName,
						Field:   "Artifacts." + string(artifactName) + ".Refs." + string(ref),
						Path:    value,
					})
				}
			}
		}
		if service.VM != nil {
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Image.RootFS", service.VM.Image.RootFS, mappings)
			for i, disk := range service.VM.Disks {
				refs = appendHostStoragePathRef(refs, serviceName, fmt.Sprintf("VM.Disks[%d].Path", i), disk.Path, mappings)
			}
		}
	}
	return refs
}

func appendHostStoragePathRef(refs []hostStorageReference, service, field, value string, mappings hostStoragePathMappings) []hostStorageReference {
	if !hostStorageValueMatchesMappings(value, mappings) {
		return refs
	}
	return append(refs, hostStorageReference{Kind: hostStorageReferenceDB, Service: service, Field: field, Path: value})
}

func hostStorageValueMatchesMappings(value string, mappings hostStoragePathMappings) bool {
	_, changed, err := mappings.Rewrite(value)
	return err == nil && changed
}
```

Add imports:

```go
import (
	"fmt"
	"github.com/yeetrun/yeet/pkg/db"
)
```

- [ ] **Step 4: Implement systemd scan**

Add to `pkg/catch/host_storage_refs.go`:

```go
func scanHostStorageSystemdRefs(systemdDir string, mappings hostStoragePathMappings) ([]hostStorageReference, error) {
	var refs []hostStorageReference
	entries, err := os.ReadDir(systemdDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !hostStorageYeetUnitName(entry.Name()) {
			continue
		}
		path := filepath.Join(systemdDir, entry.Name())
		fileRefs, err := scanHostStorageTextFileRefs(path, entry.Name(), mappings)
		if err != nil {
			return nil, err
		}
		refs = append(refs, fileRefs...)
	}
	return refs, nil
}

func hostStorageYeetUnitName(name string) bool {
	return name == CatchService+".service" ||
		strings.HasPrefix(name, "yeet-") ||
		strings.HasPrefix(name, "catch.")
}

func scanHostStorageTextFileRefs(path, unit string, mappings hostStoragePathMappings) ([]hostStorageReference, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	var refs []hostStorageReference
	for i, line := range lines {
		for _, mapping := range mappings.Sorted() {
			if strings.Contains(line, cleanHostStoragePath(mapping.From)) {
				refs = append(refs, hostStorageReference{
					Kind: hostStorageReferenceSystemd,
					Unit: unit,
					File: path,
					Line: i + 1,
					Path: cleanHostStoragePath(mapping.From),
				})
				break
			}
		}
	}
	return refs, nil
}
```

Add imports:

```go
import (
	"errors"
	"os"
)
```

- [ ] **Step 5: Extend RPC plan types with repair summary**

Add to `pkg/catchrpc/types.go`:

```go
type HostStorageRepairAction struct {
	References       int      `json:"references,omitempty"`
	DatabaseRefs     int      `json:"databaseRefs,omitempty"`
	SystemdRefs      int      `json:"systemdRefs,omitempty"`
	RegenerateUnits  []string `json:"regenerateUnits,omitempty"`
	RestartServices  []string `json:"restartServices,omitempty"`
	ValidationRoots  []string `json:"validationRoots,omitempty"`
}
```

Add to `HostStoragePlan`:

```go
RepairAction HostStorageRepairAction `json:"repairAction,omitempty"`
```

Add a round-trip assertion to `pkg/catchrpc/types_test.go` that encodes a plan
with `RepairAction.References = 2` and decodes the same value.

- [ ] **Step 6: Run tests**

Run:

```sh
mise exec -- go test ./pkg/catch ./pkg/catchrpc -run 'TestHostStorageScan|TestHostStorage.*RoundTrip' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
but diff pkg/catch/host_storage_refs.go pkg/catch/host_storage_refs_test.go pkg/catchrpc/types.go pkg/catchrpc/types_test.go
but commit codex/host-storage-reconfigure-design -m "catch: scan host storage references" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 3: Rewrite Target DB Paths During Data Dir Moves

**Files:**
- Create: `pkg/catch/host_storage_db_rewrite.go`
- Create: `pkg/catch/host_storage_db_rewrite_test.go`
- Modify: `pkg/catch/host_storage.go`

- [ ] **Step 1: Write failing DB rewrite tests**

Create `pkg/catch/host_storage_db_rewrite_test.go`:

```go
package catch

import (
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRewriteHostStorageDataPaths(t *testing.T) {
	data := &db.Data{
		Services: map[string]*db.Service{
			"catch": {
				Name:       "catch",
				Generation: 7,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(7): "/root/data/services/catch/bin/catch",
						"latest": "/root/data/services/catch/bin/catch",
					}},
				},
			},
			"devbox": {
				Name: "devbox",
				VM: &db.VMConfig{
					Image: db.VMImageConfig{RootFS: "/root/data/vm-images/ubuntu/rootfs.ext4"},
					Disks: []db.VMDiskConfig{{Path: "/root/data/services/devbox/data/rootfs.raw"}},
				},
			},
		},
	}
	result, err := rewriteHostStorageDataPaths(data, hostStoragePathMappings{
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	})
	if err != nil {
		t.Fatalf("rewriteHostStorageDataPaths error: %v", err)
	}
	if result.Changed != 4 {
		t.Fatalf("Changed = %d, want 4", result.Changed)
	}
	if got := data.Services["catch"].Artifacts[db.ArtifactBinary].Refs[db.Gen(7)]; got != "/flash/yeet/services/catch/bin/catch" {
		t.Fatalf("catch binary ref = %q", got)
	}
	if got := data.Services["devbox"].VM.Image.RootFS; got != "/flash/yeet/data/vm-images/ubuntu/rootfs.ext4" {
		t.Fatalf("VM rootfs = %q", got)
	}
	if got := data.Services["devbox"].VM.Disks[0].Path; got != "/flash/yeet/data/services/devbox/data/rootfs.raw" {
		t.Fatalf("VM disk = %q", got)
	}
}
```

Expected: FAIL with undefined `rewriteHostStorageDataPaths`.

- [ ] **Step 2: Implement typed DB rewrite**

Create `pkg/catch/host_storage_db_rewrite.go`:

```go
package catch

import (
	"fmt"

	"github.com/yeetrun/yeet/pkg/db"
)

type hostStorageDBRewriteResult struct {
	Changed int
}

func rewriteHostStorageDataPaths(data *db.Data, mappings hostStoragePathMappings) (hostStorageDBRewriteResult, error) {
	var result hostStorageDBRewriteResult
	if data == nil {
		return result, nil
	}
	for serviceName, service := range data.Services {
		if service == nil {
			continue
		}
		for artifactName, artifact := range service.Artifacts {
			if artifact == nil {
				continue
			}
			for ref, value := range artifact.Refs {
				rewritten, changed, err := mappings.Rewrite(value)
				if err != nil {
					return result, fmt.Errorf("rewrite %s artifact %s for %s: %w", artifactName, ref, serviceName, err)
				}
				if changed {
					artifact.Refs[ref] = rewritten
					result.Changed++
				}
			}
		}
		if service.VM != nil {
			changed, err := rewriteHostStoragePathValue(&service.VM.Image.RootFS, mappings)
			if err != nil {
				return result, fmt.Errorf("rewrite VM rootfs for %s: %w", serviceName, err)
			}
			if changed {
				result.Changed++
			}
			for i := range service.VM.Disks {
				changed, err := rewriteHostStoragePathValue(&service.VM.Disks[i].Path, mappings)
				if err != nil {
					return result, fmt.Errorf("rewrite VM disk %d for %s: %w", i, serviceName, err)
				}
				if changed {
					result.Changed++
				}
			}
		}
	}
	return result, nil
}

func rewriteHostStoragePathValue(value *string, mappings hostStoragePathMappings) (bool, error) {
	if value == nil {
		return false, nil
	}
	rewritten, changed, err := mappings.Rewrite(*value)
	if err != nil || !changed {
		return changed, err
	}
	*value = rewritten
	return true, nil
}
```

- [ ] **Step 3: Add apply hook after data dir copy**

In `pkg/catch/host_storage.go`, add this call after `moveDataDir` and before
the artifact and unit repair call introduced in Task 5:

```go
if err := a.rewriteTargetDataStore(ctx, plan, w); err != nil {
	return catchrpc.HostStorageApplyResult{}, err
}
```

Add helper:

```go
func (a *hostStorageApplier) rewriteTargetDataStore(ctx context.Context, plan catchrpc.HostStoragePlan, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mappings := hostStorageMappingsFromPlan(plan)
	if len(mappings) == 0 {
		return nil
	}
	store := a.finalConfig(plan).DB
	if store == nil {
		return fmt.Errorf("host storage rewrite requires target db store")
	}
	_, err := store.MutateData(func(d *db.Data) error {
		result, err := rewriteHostStorageDataPaths(d, mappings)
		if err != nil {
			return err
		}
		if result.Changed > 0 {
			writef(w, "Rewrote %d host storage database reference%s.\n", result.Changed, hostStoragePluralSuffix(result.Changed))
		}
		return nil
	})
	return err
}
```

Add this local helper in `pkg/catch/host_storage.go` or `pkg/catch/host_storage_refs.go`:

```go
func hostStoragePluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
```

Use `hostStoragePluralSuffix` in catch package messages.

- [ ] **Step 4: Run tests**

```sh
mise exec -- go test ./pkg/catch -run 'TestRewriteHostStorageDataPaths|TestHostStorage' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
but diff pkg/catch/host_storage_db_rewrite.go pkg/catch/host_storage_db_rewrite_test.go pkg/catch/host_storage.go
but commit codex/host-storage-reconfigure-design -m "catch: rewrite host storage db paths" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 4: Plan Repair-Only Host Storage Actions

**Files:**
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/host_storage_refs.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `pkg/yeet/host_set.go`
- Modify: `pkg/yeet/host_set_test.go`

- [ ] **Step 1: Write failing repair-only planning test**

Add to `pkg/catch/host_storage_test.go`:

```go
func TestHostStoragePlanDetectsRepairOnlyOldRootRefs(t *testing.T) {
	server, store := newHostStorageTestServer(t, hostStorageTestConfig{
		rootDir:      "/flash/yeet/data",
		servicesRoot: "/flash/yeet/services",
	})
	mutateHostStorageData(t, store, func(d *db.Data) {
		d.Services["catch"] = &db.Service{
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
			Generation: 2,
			Artifacts: db.ArtifactStore{
				db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
					db.Gen(2): "/root/data/services/catch/bin/catch",
				}},
			},
		}
	})

	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "/flash/yeet/services"},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage error: %v", err)
	}
	if plan.RepairAction.References == 0 {
		t.Fatalf("RepairAction.References = 0, want repair refs")
	}
	if !plan.RequiresRestart {
		t.Fatalf("RequiresRestart = false, want true for repair")
	}
}
```

Use the existing `host_storage_test.go` fixture helpers. In the current test file, create a temporary store with the same helper used by the existing host storage planning tests, then keep the assertions and request shape shown above.

- [ ] **Step 2: Implement inferred repair mappings**

Add to `pkg/catch/host_storage_refs.go`:

```go
func inferredHostStorageRepairMappings(current catchrpc.HostStorageState, catchRoot string, refs []hostStorageReference) hostStoragePathMappings {
	var mappings hostStoragePathMappings
	hasRootData := false
	hasOldCatch := false
	for _, ref := range refs {
		if strings.Contains(ref.Path, "/root/data") {
			hasRootData = true
		}
		if strings.Contains(ref.Path, "/root/data/services/catch") || strings.Contains(ref.File, "/root/data/services/catch") {
			hasOldCatch = true
		}
	}
	if hasOldCatch && catchRoot != "" && !hostStoragePathsEqual(catchRoot, "/root/data/services/catch") {
		mappings = append(mappings, hostStoragePathMapping{From: "/root/data/services/catch", To: catchRoot, Reason: hostStoragePathReasonCatchRoot})
	}
	if hasRootData && current.DataDir != "" && !hostStoragePathsEqual(current.DataDir, "/root/data") {
		mappings = append(mappings, hostStoragePathMapping{From: "/root/data", To: current.DataDir, Reason: hostStoragePathReasonDataDir})
	}
	return mappings.Sorted()
}
```

This first implementation intentionally handles the known legacy root
`/root/data`. Persisted migration history is outside this implementation plan.

- [ ] **Step 3: Wire repair scan into planning**

In `pkg/catch/host_storage.go`, after normal plan construction, add a planner
method:

```go
func (p *hostStoragePlanner) planRepairAction(ctx context.Context, plan catchrpc.HostStoragePlan) (catchrpc.HostStorageRepairAction, error) {
	if err := ctx.Err(); err != nil {
		return catchrpc.HostStorageRepairAction{}, err
	}
	dv, err := p.hostStorageDataView()
	if err != nil {
		return catchrpc.HostStorageRepairAction{}, err
	}
	data := dv.AsStruct()
	mappings := hostStorageMappingsFromPlan(plan)
	catchRoot, _, err := p.currentCatchRootForHostStorage()
	if err == nil && len(mappings) == 0 {
		currentRefs := scanHostStorageDataRefs(data, hostStoragePathMappings{{From: "/root/data", To: plan.Desired.DataDir, Reason: hostStoragePathReasonDataDir}})
		mappings = inferredHostStorageRepairMappings(plan.Desired, catchRoot, currentRefs)
	}
	if len(mappings) == 0 {
		return catchrpc.HostStorageRepairAction{}, nil
	}
	dbRefs := scanHostStorageDataRefs(data, mappings)
	return catchrpc.HostStorageRepairAction{
		References:      len(dbRefs),
		DatabaseRefs:    len(dbRefs),
		ValidationRoots: hostStorageMappingSources(mappings),
	}, nil
}
```

Add helper:

```go
func hostStorageMappingSources(mappings hostStoragePathMappings) []string {
	var roots []string
	for _, mapping := range mappings {
		roots = append(roots, cleanHostStoragePath(mapping.From))
	}
	return uniqueSortedStrings(roots)
}
```

Set `plan.RepairAction` and update `RequiresRestart`:

```go
repair, err := p.planRepairAction(ctx, plan)
if err != nil {
	return catchrpc.HostStoragePlan{}, err
}
plan.RepairAction = repair
if repair.References > 0 {
	plan.RequiresRestart = true
}
return plan, nil
```

- [ ] **Step 4: Render repair plans in yeet**

In `pkg/yeet/host_set.go`, add:

```go
func renderHostStorageRepairAction(w io.Writer, action catchrpc.HostStorageRepairAction) error {
	if action.References == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "Repair host storage references: %d\n", action.References); err != nil {
		return err
	}
	if action.DatabaseRefs > 0 {
		if _, err := fmt.Fprintf(w, "  Database refs: %d\n", action.DatabaseRefs); err != nil {
			return err
		}
	}
	if action.SystemdRefs > 0 {
		if _, err := fmt.Fprintf(w, "  Systemd refs: %d\n", action.SystemdRefs); err != nil {
			return err
		}
	}
	return nil
}
```

Call it from `renderHostStoragePlanDetails` after catch action rendering.

Update `hostStoragePlanHasChanges`:

```go
plan.RepairAction.References != 0
```

- [ ] **Step 5: Run tests**

```sh
mise exec -- go test ./pkg/catch ./pkg/yeet -run 'TestHostStoragePlanDetectsRepairOnlyOldRootRefs|TestRunHostSetRendersRepair' -count=1
```

Expected: PASS after adding or adapting the yeet render test.

- [ ] **Step 6: Commit**

```sh
but diff pkg/catch/host_storage.go pkg/catch/host_storage_refs.go pkg/catch/host_storage_test.go pkg/yeet/host_set.go pkg/yeet/host_set_test.go
but commit codex/host-storage-reconfigure-design -m "host set: plan storage reference repairs" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 5: Rewrite Generated Artifacts and Reinstall Non-Catch Units

**Files:**
- Create: `pkg/catch/host_storage_artifacts.go`
- Create: `pkg/catch/host_storage_artifacts_test.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/service_root_migration.go`

- [ ] **Step 1: Write failing artifact repair test**

Create `pkg/catch/host_storage_artifacts_test.go`:

```go
package catch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRepairHostStorageGeneratedArtifactsRewritesCurrentTextArtifacts(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, "yeet-nginx-ns.service")
	if err := os.WriteFile(unitPath, []byte("ExecStart=/root/data/services/catch/data/service-ns\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{
		Name:       "nginx",
		Generation: 4,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(4): unitPath}},
		},
	}
	result, err := repairHostStorageGeneratedArtifacts(service, hostStoragePathMappings{
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
	})
	if err != nil {
		t.Fatalf("repairHostStorageGeneratedArtifacts error: %v", err)
	}
	if result.Rewritten != 1 {
		t.Fatalf("Rewritten = %d, want 1", result.Rewritten)
	}
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/root/data") {
		t.Fatalf("artifact still contains old root: %s", raw)
	}
}
```

Expected: FAIL with undefined `repairHostStorageGeneratedArtifacts`.

- [ ] **Step 2: Implement generated artifact repair**

Create `pkg/catch/host_storage_artifacts.go`:

```go
package catch

import (
	"fmt"
	"path/filepath"

	"github.com/yeetrun/yeet/pkg/db"
)

type hostStorageArtifactRepairResult struct {
	Rewritten int
}

func repairHostStorageGeneratedArtifacts(service *db.Service, mappings hostStoragePathMappings) (hostStorageArtifactRepairResult, error) {
	var result hostStorageArtifactRepairResult
	if service == nil {
		return result, nil
	}
	for name, artifact := range service.Artifacts {
		if artifact == nil {
			continue
		}
		path, ok := artifact.Refs[db.Gen(service.Generation)]
		if !ok || path == "" {
			continue
		}
		if !hostStorageArtifactMayContainPaths(name) {
			continue
		}
		if err := rewriteCopiedServiceRootArtifact(name, filepath.Clean(path), mappings[0].From, mappings[0].To); err != nil {
			return result, fmt.Errorf("rewrite generated artifact %s for %s: %w", name, service.Name, err)
		}
		for _, mapping := range mappings.Sorted()[1:] {
			if err := rewriteCopiedServiceRootArtifact(name, filepath.Clean(path), mapping.From, mapping.To); err != nil {
				return result, fmt.Errorf("rewrite generated artifact %s for %s: %w", name, service.Name, err)
			}
		}
		result.Rewritten++
	}
	return result, nil
}

func hostStorageArtifactMayContainPaths(name db.ArtifactName) bool {
	return name == db.ArtifactDockerComposeFile || serviceRootMigrationTextArtifacts[name]
}
```

If `mappings` can be empty, guard before indexing:

```go
if len(mappings) == 0 {
	return result, nil
}
```

- [ ] **Step 3: Add non-catch unit reinstall helper**

Add to `pkg/catch/host_storage_artifacts.go`:

```go
func reinstallHostStorageServiceUnits(ctx context.Context, cfg Config, service *db.Service) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.Name == CatchService {
		return nil
	}
	if !serviceRootMigrationNeedsSystemdInstall(service, service) {
		return nil
	}
	root := serviceRootFromConfig(cfg, *service)
	systemdService, err := svc.NewSystemdService(cfg.DB, service.View(), serviceRunDirForRoot(root))
	if err != nil {
		return fmt.Errorf("load systemd service %s: %w", service.Name, err)
	}
	if err := systemdService.Install(); err != nil {
		return fmt.Errorf("install systemd service %s: %w", service.Name, err)
	}
	return nil
}
```

Add imports:

```go
import (
	"context"
	"github.com/yeetrun/yeet/pkg/svc"
)
```

- [ ] **Step 4: Apply artifact repair after DB rewrite**

In `pkg/catch/host_storage.go`, add:

```go
if err := a.repairGeneratedArtifactsAndUnits(ctx, plan, w); err != nil {
	return catchrpc.HostStorageApplyResult{}, err
}
```

Add helper:

```go
func (a *hostStorageApplier) repairGeneratedArtifactsAndUnits(ctx context.Context, plan catchrpc.HostStoragePlan, w io.Writer) error {
	mappings := hostStorageMappingsFromPlan(plan)
	if len(mappings) == 0 && plan.RepairAction.References == 0 {
		return nil
	}
	cfg := a.finalConfig(plan)
	dv, err := cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("load rewritten data for artifact repair: %w", err)
	}
	for _, sv := range dv.Services().All() {
		service := sv.AsStruct()
		if service == nil || service.Name == CatchService {
			continue
		}
		if _, err := repairHostStorageGeneratedArtifacts(service, mappings); err != nil {
			return err
		}
		if err := reinstallHostStorageServiceUnits(ctx, cfg, service); err != nil {
			return err
		}
	}
	writef(w, "Repaired generated host storage artifacts.\n")
	return nil
}
```

- [ ] **Step 5: Run tests**

```sh
mise exec -- go test ./pkg/catch -run 'TestRepairHostStorageGeneratedArtifacts|TestHostStorage' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
but diff pkg/catch/host_storage_artifacts.go pkg/catch/host_storage_artifacts_test.go pkg/catch/host_storage.go pkg/catch/service_root_migration.go
but commit codex/host-storage-reconfigure-design -m "catch: repair generated storage artifacts" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 6: Regenerate VM Systemd Units From Typed State

**Files:**
- Modify: `pkg/catch/vm_systemd.go`
- Create: `pkg/catch/vm_systemd_test.go`
- Modify: `pkg/catch/host_storage_artifacts.go`
- Modify: `pkg/catch/host_storage_artifacts_test.go`

- [ ] **Step 1: Write failing VM regeneration test**

Create `pkg/catch/vm_systemd_test.go`:

```go
package catch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRewriteVMSystemdUnitForHostStorage(t *testing.T) {
	systemdDir := t.TempDir()
	oldDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldDir })

	service := &db.Service{
		Name:        "devbox",
		ServiceType: db.ServiceTypeVM,
		ServiceRoot: "/flash/yeet/vms/devbox",
		VM: &db.VMConfig{
			Image: db.VMImageConfig{RootFS: "/flash/yeet/data/vm-images/ubuntu/rootfs.ext4"},
		},
	}
	cfg := Config{RootDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"}
	if err := rewriteVMSystemdUnitForHostStorage(cfg, service, "/flash/yeet/services/catch/run/catch"); err != nil {
		t.Fatalf("rewriteVMSystemdUnitForHostStorage error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(systemdDir, vmSystemdUnitName("devbox")))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	for _, want := range []string{"/flash/yeet/data", "/flash/yeet/services/catch/run/catch", "/flash/yeet/vms/devbox"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %s:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "/root/data") {
		t.Fatalf("unit contains old root:\n%s", unit)
	}
}
```

Expected: FAIL with undefined `rewriteVMSystemdUnitForHostStorage`.

- [ ] **Step 2: Implement VM unit regeneration helper**

Add to `pkg/catch/vm_systemd.go`:

```go
func rewriteVMSystemdUnitForHostStorage(cfg Config, service *db.Service, runner string) error {
	if service == nil || service.VM == nil {
		return nil
	}
	root := serviceRootFromConfig(cfg, *service)
	runDir := serviceRunDirForRoot(root)
	dataDir := serviceDataDirForRoot(root)
	unit := renderVMSystemdUnit(vmSystemdConfig{
		Service:          service.Name,
		Runner:           runner,
		DataDir:          cfg.RootDir,
		ServiceRoot:      root,
		DiskPath:         service.VM.Image.RootFS,
		Firecracker:      filepath.Join(filepath.Dir(service.VM.Image.RootFS), "firecracker"),
		ConfigPath:       filepath.Join(runDir, "firecracker.json"),
		APISocket:        filepath.Join(runDir, "firecracker.sock"),
		ConsoleSocket:    filepath.Join(runDir, "serial.sock"),
		VsockSocket:      filepath.Join(runDir, "vsock.sock"),
		WorkingDirectory: dataDir,
	})
	return writeVMSystemdUnitAtomic(filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service.Name)), []byte(unit), 0o644)
}
```

Add import:

```go
import "github.com/yeetrun/yeet/pkg/db"
```

Derive the Firecracker path from the VM image root directory in this task because the current VM systemd renderer stores absolute Firecracker paths in the unit, not a separate persisted DB field.

- [ ] **Step 3: Call VM unit regeneration during artifact repair**

In `pkg/catch/host_storage_artifacts.go`, add:

```go
func repairHostStorageVMUnit(cfg Config, service *db.Service, catchRunner string) error {
	if service == nil || service.ServiceType != db.ServiceTypeVM {
		return nil
	}
	return rewriteVMSystemdUnitForHostStorage(cfg, service, catchRunner)
}
```

In `repairGeneratedArtifactsAndUnits`, compute:

```go
catchRunner := filepath.Join(serviceRootFromConfig(cfg, db.Service{Name: CatchService}), "run", "catch")
```

Then call:

```go
if err := repairHostStorageVMUnit(cfg, service, catchRunner); err != nil {
	return err
}
```

- [ ] **Step 4: Run tests**

```sh
mise exec -- go test ./pkg/catch -run 'TestRewriteVMSystemdUnitForHostStorage|TestRepairHostStorage' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
but diff pkg/catch/vm_systemd.go pkg/catch/vm_systemd_test.go pkg/catch/host_storage_artifacts.go pkg/catch/host_storage_artifacts_test.go
but commit codex/host-storage-reconfigure-design -m "catch: regenerate VM units during storage repair" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 7: Validate No Active Old-Root References Remain

**Files:**
- Modify: `pkg/catch/host_storage_refs.go`
- Modify: `pkg/catch/host_storage_refs_test.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/yeet/host_set.go`

- [ ] **Step 1: Write failing validation tests**

Add to `pkg/catch/host_storage_refs_test.go`:

```go
func TestValidateHostStorageNoActiveRefsFailsWithSystemdRef(t *testing.T) {
	systemdDir := t.TempDir()
	writeFile(t, filepath.Join(systemdDir, "yeet-devbox.service"), "ExecStart=/root/data/services/catch/run/catch\n")
	data := &db.Data{Services: map[string]*db.Service{}}
	result, err := validateHostStorageNoActiveRefs(data, systemdDir, hostStoragePathMappings{
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	})
	if err == nil {
		t.Fatalf("validateHostStorageNoActiveRefs error = nil, want active ref error")
	}
	if result.ActiveRefs != 1 {
		t.Fatalf("ActiveRefs = %d, want 1", result.ActiveRefs)
	}
}
```

Expected: FAIL with undefined `validateHostStorageNoActiveRefs`.

- [ ] **Step 2: Implement validation result**

Add to `pkg/catch/host_storage_refs.go`:

```go
type hostStorageValidationResult struct {
	ActiveRefs int
	DBRefs     int
	SystemdRefs int
	Examples   []hostStorageReference
}

func validateHostStorageNoActiveRefs(data *db.Data, systemdDir string, mappings hostStoragePathMappings) (hostStorageValidationResult, error) {
	var result hostStorageValidationResult
	dbRefs := scanHostStorageDataRefs(data, mappings)
	systemdRefs, err := scanHostStorageSystemdRefs(systemdDir, mappings)
	if err != nil {
		return result, err
	}
	result.DBRefs = len(dbRefs)
	result.SystemdRefs = len(systemdRefs)
	result.ActiveRefs = result.DBRefs + result.SystemdRefs
	result.Examples = append(result.Examples, dbRefs...)
	result.Examples = append(result.Examples, systemdRefs...)
	if len(result.Examples) > 5 {
		result.Examples = result.Examples[:5]
	}
	if result.ActiveRefs != 0 {
		return result, fmt.Errorf("host storage validation found %d active old-root reference%s", result.ActiveRefs, hostStoragePluralSuffix(result.ActiveRefs))
	}
	return result, nil
}
```

- [ ] **Step 3: Add validation to apply after catch verification**

In `pkg/catch/host_storage.go`, after `reinstallRestartAndVerifyCatch` succeeds,
call:

```go
validation, err := a.validateHostStorageApply(ctx, plan)
if err != nil {
	return catchrpc.HostStorageApplyResult{}, err
}
result.Validation = catchrpc.HostStorageValidationResult{
	ActiveRefs:  validation.ActiveRefs,
	DatabaseRefs: validation.DBRefs,
	SystemdRefs: validation.SystemdRefs,
}
```

Add helper:

```go
func (a *hostStorageApplier) validateHostStorageApply(ctx context.Context, plan catchrpc.HostStoragePlan) (hostStorageValidationResult, error) {
	if err := ctx.Err(); err != nil {
		return hostStorageValidationResult{}, err
	}
	mappings := hostStorageMappingsFromPlan(plan)
	if len(mappings) == 0 && plan.RepairAction.References == 0 {
		return hostStorageValidationResult{}, nil
	}
	cfg := a.finalConfig(plan)
	dv, err := cfg.DB.Get()
	if err != nil {
		return hostStorageValidationResult{}, fmt.Errorf("load data for host storage validation: %w", err)
	}
	return validateHostStorageNoActiveRefs(dv.AsStruct(), systemdSystemDir, mappings)
}
```

- [ ] **Step 4: Add RPC validation result**

In `pkg/catchrpc/types.go`:

```go
type HostStorageValidationResult struct {
	ActiveRefs   int `json:"activeRefs,omitempty"`
	DatabaseRefs int `json:"databaseRefs,omitempty"`
	SystemdRefs  int `json:"systemdRefs,omitempty"`
}
```

Add to `HostStorageApplyResult`:

```go
Validation HostStorageValidationResult `json:"validation,omitempty"`
```

Render success in `pkg/yeet/host_set.go`:

```go
if result.Validation.ActiveRefs == 0 && (result.Validation.DatabaseRefs != 0 || result.Validation.SystemdRefs != 0) {
	_, err := fmt.Fprintln(w, "Validated host storage migration: no active old-root references remain.")
	return err
}
```

- [ ] **Step 5: Run tests**

```sh
mise exec -- go test ./pkg/catch ./pkg/catchrpc ./pkg/yeet -run 'TestValidateHostStorageNoActiveRefs|TestHostStorage|TestRunHostSet' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
but diff pkg/catch/host_storage_refs.go pkg/catch/host_storage_refs_test.go pkg/catch/host_storage.go pkg/catchrpc/types.go pkg/yeet/host_set.go
but commit codex/host-storage-reconfigure-design -m "host set: validate storage reference repair" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 8: Restart Every Affected Service

**Files:**
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/host_storage_refs.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `pkg/catchrpc/types.go`

- [ ] **Step 1: Write failing affected-service restart test**

Add to `pkg/catch/host_storage_test.go`:

```go
func TestHostStorageApplyRestartsRepairAffectedServices(t *testing.T) {
	server, store := newHostStorageTestServer(t, hostStorageTestConfig{
		rootDir:      "/flash/yeet/data",
		servicesRoot: "/flash/yeet/services",
	})
	mutateHostStorageData(t, store, func(d *db.Data) {
		d.Services["nginx"] = &db.Service{
			Name:        "nginx",
			ServiceType: db.ServiceTypeDockerCompose,
			Generation: 1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/flash/yeet/services/nginx/run/netns.service"}},
			},
		}
	})
	var stopped, started []string
	server.hostStorageTestOps = hostStorageApplyOperations{
		isServiceRunning: func(context.Context, string) (bool, error) { return true, nil },
		runnerForService: func(_ context.Context, name string) (ServiceRunner, error) {
			return hostStorageFakeRunner{
				stop:  func() error { stopped = append(stopped, name); return nil },
				start: func() error { started = append(started, name); return nil },
			}, nil
		},
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		Desired: catchrpc.HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      1,
			RestartServices: []string{"nginx"},
		},
		RequiresRestart: true,
	}
	_, err := server.ApplyHostStoragePlan(context.Background(), plan, true, io.Discard)
	if err != nil {
		t.Fatalf("ApplyHostStoragePlan error: %v", err)
	}
	if !slices.Equal(stopped, []string{"nginx"}) || !slices.Equal(started, []string{"nginx"}) {
		t.Fatalf("stopped=%v started=%v, want nginx restart", stopped, started)
	}
}
```

Use the same fake operation injection pattern already used by `host_storage_test.go` for service stop/start tests. The required behavior is that repair-only affected services stop before repair and restart after unit regeneration.

- [ ] **Step 2: Compute affected services from refs**

Add to `pkg/catch/host_storage_refs.go`:

```go
func hostStorageAffectedServicesFromRefs(refs []hostStorageReference) []string {
	seen := map[string]bool{}
	for _, ref := range refs {
		name := strings.TrimSpace(ref.Service)
		if name == "" || hostStorageSelfManagedService(name) {
			continue
		}
		seen[name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
```

- [ ] **Step 3: Include repair restarts in plan and apply moves**

In `planRepairAction`, set:

```go
RestartServices: hostStorageAffectedServicesFromRefs(dbRefs),
```

In `prepareApply`, build restart-only moves for `plan.RepairAction.RestartServices`
in addition to service-root moves:

```go
serviceMoves = append(serviceMoves, a.buildRepairRestartMoves(plan.RepairAction.RestartServices)...)
```

Add:

```go
func (a *hostStorageApplier) buildRepairRestartMoves(names []string) []hostStorageServiceApplyMove {
	var moves []hostStorageServiceApplyMove
	for _, name := range names {
		if strings.TrimSpace(name) == "" || hostStorageSelfManagedService(name) {
			continue
		}
		moves = append(moves, hostStorageServiceApplyMove{
			move: catchrpc.HostStorageServiceMove{Name: name},
			plan: serviceRootMigrationPlan{ServiceName: name, Mode: serviceRootMigrationEmpty},
		})
	}
	return moves
}
```

Update `moveServiceRoots` to skip moves with empty `OldRoot` and `NewRoot`:

```go
if move.plan.OldRoot == "" && move.plan.NewRoot == "" {
	continue
}
```

- [ ] **Step 4: Run tests**

```sh
mise exec -- go test ./pkg/catch -run 'TestHostStorageApplyRestartsRepairAffectedServices|TestHostStorage' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
but diff pkg/catch/host_storage.go pkg/catch/host_storage_refs.go pkg/catch/host_storage_test.go pkg/catchrpc/types.go
but commit codex/host-storage-reconfigure-design -m "catch: restart storage repair services" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 9: Update Local `yeet.toml` Without Pinning Host Defaults

**Files:**
- Modify: `pkg/yeet/host_set.go`
- Modify: `pkg/yeet/host_set_test.go`
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/project_config_test.go`

- [ ] **Step 1: Write failing config update test**

Add to `pkg/yeet/host_set_test.go`:

```go
func TestApplyHostStorageConfigMovesClearsDefaultServiceRoot(t *testing.T) {
	cfg := &ProjectConfig{Services: []ProjectServiceConfig{{
		Name:           "nginx",
		Host:           "yeet-pve1",
		ServiceRoot:    "/root/data/services/nginx",
		ServiceRootZFS: true,
	}}}
	updated, skipped := applyHostStorageConfigMoves(cfg, "yeet-pve1", "/flash/yeet/services", []catchrpc.HostStorageServiceMove{{
		Name: "nginx",
		From: "/root/data/services/nginx",
		To:   "/flash/yeet/services/nginx",
	}})
	if updated != 1 || skipped != 0 {
		t.Fatalf("updated=%d skipped=%d, want 1,0", updated, skipped)
	}
	got := cfg.Services[0]
	if got.ServiceRoot != "" || got.ServiceRootZFS {
		t.Fatalf("service root was not cleared for default root: %#v", got)
	}
}
```

Expected: FAIL because the existing helper sets `/flash/yeet/services/nginx`.

- [ ] **Step 2: Add project config clear helper**

In `pkg/yeet/project_config.go`, add or adapt a method:

```go
func (cfg *ProjectConfig) ClearServiceRootForEntry(name, host string) bool {
	if cfg == nil {
		return false
	}
	for i := range cfg.Services {
		if cfg.Services[i].Name == name && hostMatchesProjectService(cfg.Services[i].Host, host) {
			cfg.Services[i].ServiceRoot = ""
			cfg.Services[i].ServiceRootZFS = false
			return true
		}
	}
	return false
}
```

If `hostMatchesProjectService` does not exist, use the same host comparison
helper used by `SetServiceRootForEntry`.

- [ ] **Step 3: Pass desired services root into config update**

Change signatures in `pkg/yeet/host_set.go`:

```go
return updateHostStorageConfig(hostSetStdout, flags.Config, host, plan.Desired.ServicesRoot, result)
```

```go
func updateHostStorageConfig(w io.Writer, configPath string, host string, desiredServicesRoot string, result catchrpc.HostStorageApplyResult) error
```

```go
func applyHostStorageConfigMoves(cfg *ProjectConfig, host string, desiredServicesRoot string, moves []catchrpc.HostStorageServiceMove) (int, int)
```

In `applyHostStorageConfigMoves`:

```go
targetDefault := filepath.Join(filepath.Clean(desiredServicesRoot), move.Name)
if desiredServicesRoot != "" && filepath.Clean(move.To) == targetDefault {
	if cfg.ClearServiceRootForEntry(move.Name, host) {
		updated++
	} else {
		skipped++
	}
	continue
}
```

Keep the existing `SetServiceRootForEntry` path for non-default moved roots.

- [ ] **Step 4: Run tests**

```sh
mise exec -- go test ./pkg/yeet -run 'TestApplyHostStorageConfigMoves|TestProjectConfig.*ServiceRoot' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
but diff pkg/yeet/host_set.go pkg/yeet/host_set_test.go pkg/yeet/project_config.go pkg/yeet/project_config_test.go
but commit codex/host-storage-reconfigure-design -m "host set: avoid pinning default service roots" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 10: Put Host-Wide ZFS Services on Per-Service Child Datasets

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catch/host_storage.go`
- Modify: `pkg/catch/host_storage_test.go`
- Modify: `docs/superpowers/specs/2026-07-03-host-storage-complete-migration-design.md`

- [ ] **Step 1: Write failing ZFS services-root planning test**

Add a host storage planning test where `--services-root` resolves from a ZFS
dataset such as `tank/yeet/services`. The plan must create these datasets:

```text
tank/yeet/services
tank/yeet/services/api
tank/yeet/services/catch
```

The affected user service move must include `ToZFS: "tank/yeet/services/api"`,
and the catch move must include `ToZFS: "tank/yeet/services/catch"`.

Run:

```sh
mise exec -- go test ./pkg/catch -run TestHostStoragePlanZFSServicesRootCreatesPerServiceDatasets -count=1
```

Expected: FAIL until `ToZFS` is carried through planning.

- [ ] **Step 2: Carry child dataset names through the RPC plan**

Extend the host storage wire plan:

```go
type HostStorageCatchAction struct {
	Move  bool   `json:"move,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	ToZFS string `json:"toZfs,omitempty"`
}

type HostStorageServiceMove struct {
	Name       string `json:"name"`
	From       string `json:"from"`
	To         string `json:"to"`
	ToZFS      string `json:"toZfs,omitempty"`
	WasRunning bool   `json:"wasRunning"`
}
```

Keep the services-root dataset name from ZFS resolution and use it as the child
dataset prefix for every `migrate-services=all` service move. Add missing child
datasets to `ZFSDatasetsToCreate`, skipping children that already exist.

- [ ] **Step 3: Persist `ServiceRootZFS` during apply**

Write an apply test that starts with a service under the old default root and a
catch service under the old catch root, applies a plan with `ToZFS` child
datasets, and verifies both DB rows are updated:

```text
api.ServiceRootZFS == tank/yeet/services/api
catch.ServiceRootZFS == tank/yeet/services/catch
```

Then populate `serviceRootMigrationPlan.NewRootZFS` from the planned `ToZFS`
for user services and catch.

- [ ] **Step 4: Document the in-place datasetization case**

Update the design spec to distinguish a forward services-root move from an
already-populated in-place conversion such as `/flash/yeet/services/api`
becoming the mountpoint for `flash/yeet/services/api`. The latter needs a safe
stage-create-mount-copy sequence and must not be treated as complete just
because the parent services root is ZFS.

- [ ] **Step 5: Run tests**

```sh
mise exec -- go test ./pkg/catch -run 'TestHostStoragePlanZFSServicesRootCreatesPerServiceDatasets|TestHostStorageApplyPersistsPerServiceZFSDatasets' -count=1
mise exec -- go test ./pkg/catch ./pkg/catchrpc -count=1
```

Expected: all packages report `ok`.

- [ ] **Step 6: Commit**

```sh
but diff pkg/catch/host_storage.go pkg/catch/host_storage_test.go pkg/catchrpc/types.go docs/superpowers/specs/2026-07-03-host-storage-complete-migration-design.md docs/superpowers/plans/2026-07-03-host-storage-complete-migration.md
but commit codex/host-storage-reconfigure-design -m "catch: use child zfs datasets for host services root" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 11: Add Host-Level `yeet info`

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/client.go`
- Modify: `pkg/catchrpc/client_test.go`
- Modify: `pkg/catch/authz.go`
- Modify: `pkg/catch/rpc.go`
- Modify: `pkg/catch/rpc_test.go`
- Modify: `pkg/catch/info.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`

- [ ] **Step 1: Make the `info` service argument optional**

Add CLI parser and bridge tests proving:

```sh
yeet info
yeet info --host=yeet-pve1
yeet info --format=json
yeet info catch
```

`yeet info` without a service routes to host info. `yeet info <svc>` keeps the
existing service-info path, including `yeet info catch` as service info for the
`catch` service. Global `--host` must work in the user-facing form
`yeet info --host=yeet-pve1`.

Run:

```sh
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'Test.*Info' -count=1
```

Expected: parser and bridge coverage fails until the argument is optional and
the no-service route is distinct.

- [ ] **Step 2: Add a read-only host info response**

Add typed wire structs for host info and inventory counts. The response should
include:

```go
type HostInfoResponse struct {
	Host      string            `json:"host,omitempty"`
	Catch     HostCatchInfo     `json:"catch,omitempty"`
	Storage   HostStorageInfo   `json:"storage,omitempty"`
	Inventory HostInventoryInfo `json:"inventory,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
}
```

Exact field names can follow existing catchrpc style, but they must cover:

- catch version, OS, architecture, install user, and install host
- data dir and services root from the running catch config
- catch service root when known
- detected ZFS dataset for data dir, services root, and catch root
- service counts for all named yeet services
- VM counts as a subtype of services
- running, stopped, and unhealthy counts

Register the RPC or read command path with `read` permission in
`pkg/catch/authz.go`. Do not make it depend on Proxmox-specific state.

- [ ] **Step 3: Implement generic catch-side collection**

Collect the host fields from the running catch config and the inventory from
the catch DB plus the same status helpers used by `yeet status`. Detect ZFS
backing by mapping configured paths to the most specific mountpoint from:

```sh
zfs list -H -o name,mountpoint
```

If ZFS is unavailable, omit the dataset fields and include a warning only when
the user explicitly asked for JSON or the detection failed unexpectedly. Normal
non-ZFS hosts should render cleanly.

Services counts include every named yeet service. VM counts are a subtype count,
not an additional total added on top of services.

- [ ] **Step 4: Render host info in `pkg/yeet/info_cmd.go`**

When no service argument is present, fetch host info and render:

```text
Host
  Host:           yeet-pve1
  Catch:          v0.x.x (linux/amd64)
  Data dir:       /flash/yeet/data (zfs flash/yeet/data)
  Services root:  /flash/yeet/services (zfs prefix flash/yeet/services)
  Catch root:     /flash/yeet/services/catch (zfs flash/yeet/services/catch)

Inventory
  Services:  18 total, 17 running, 1 stopped, 0 unhealthy
  VMs:       4 total, 3 running, 1 stopped, 0 unhealthy
```

`--format=json` and `--format=json-pretty` should emit only the host-info JSON
shape for no-service calls. Existing service-info JSON must remain unchanged
when a service argument is present.

- [ ] **Step 5: Run focused tests**

```sh
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/catchrpc ./pkg/catch ./pkg/yeet -run 'Test.*Info|Test.*HostInfo|TestRPC.*Info|TestRPCMethodPermissions' -count=1
```

Expected: all packages report `ok`.

- [ ] **Step 6: Commit**

```sh
but diff
but commit codex/host-storage-reconfigure-design -m "yeet: show host info without a service" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

## Task 12: Documentation, Full Verification, and Live Repair Check

**Files:**
- Modify: `README.md`
- Modify: website manual pages that document host storage
- Modify: `.codex/skills/yeet-cli/references/yeet-help-agent.md`
- Modify: `pkg/catch/host_storage_test.go`, `pkg/yeet/host_set_test.go`, and `cmd/yeet/cli_bridge_test.go` for final integration coverage

- [ ] **Step 1: Update user docs**

Document that `yeet host set` now validates active references and may run a
repair when the roots already match. Include this exact CLI example:

```sh
yeet --host yeet-pve1 host set --zfs \
  --data-dir=flash/yeet/data \
  --services-root=flash/yeet/services \
  --migrate-services=all \
  --config=/path/to/yeet.toml
```

Document that old storage is retained until explicit cleanup and that a clean
migration means active DB refs, yeet-owned systemd units, Docker bind mounts,
and running services no longer depend on old roots.

Document `yeet info` as host info when no service is supplied:

```sh
yeet info
yeet info --host=yeet-pve1
```

Document that `yeet info <svc>` remains service info and that the host summary
includes data dir, services root, ZFS backing when detected, service counts, and
VM counts.

- [ ] **Step 2: Regenerate agent help if CLI help changed**

Run:

```sh
tools/generate-yeet-help-agent.sh
```

Expected: `.codex/skills/yeet-cli/references/yeet-help-agent.md` updates only
if host help text changed.

- [ ] **Step 3: Run targeted tests**

```sh
mise exec -- go test ./pkg/catch ./pkg/catchrpc ./pkg/yeet ./pkg/cli ./cmd/yeet -count=1
```

Expected: all packages report `ok`.

- [ ] **Step 4: Run full tests and gates**

```sh
mise exec -- go test ./...
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: all commands pass. If `quality:goal` exposes an unrelated race or
flake, investigate with `superpowers:systematic-debugging` before changing code.

- [ ] **Step 5: Live repair validation on `yeet-pve1`**

Deploy catch from the workspace:

```sh
mise exec -- go run ./cmd/yeet --host yeet-pve1 --progress=plain init root@pve1
```

Run the repair-aware host set command:

```sh
mise exec -- go run ./cmd/yeet --host yeet-pve1 host set --zfs \
  --data-dir=flash/yeet/data \
  --services-root=flash/yeet/services \
  --migrate-services=all \
  --config=/Users/shayne/yeet-services/yeet.toml
```

Expected output includes either a repair plan followed by validation success, or
`No host storage changes to apply.` if the host has already been repaired by an
earlier run.

- [ ] **Step 6: Verify live old-root references are gone**

Run:

```sh
ssh root@pve1 'bash -lc "grep -R /root/data /etc/systemd/system /run/systemd/system 2>/dev/null || true"'
```

Expected: no output for active yeet-owned unit files after repair. If old
disabled historical unit files remain, classify them as cleanup candidates and
do not delete them in this task.

Run:

```sh
ssh root@pve1 'bash -lc "docker ps -q | xargs -r docker inspect --format \"{{.Name}} {{range .Mounts}}{{.Source}} {{end}}\" | grep /root/data || true"'
```

Expected: no output.

Run:

```sh
mise exec -- go run ./cmd/yeet --host yeet-pve1 status --progress=plain
mise exec -- go run ./cmd/yeet info --host=yeet-pve1
```

Expected: all previously running services are running.
The info output reports `/flash/yeet/data`, `/flash/yeet/services`, the ZFS
backing datasets, and the expected service and VM counts.

- [ ] **Step 7: Commit docs and final fixes**

```sh
but diff
but commit codex/host-storage-reconfigure-design -m "docs: explain complete host storage migration" --changes FILE_IDS_FROM_PRECEDING_BUT_DIFF
```

If implementation fixes remain uncommitted, inspect and commit them with a
separate message that names the affected subsystem.

---

## Self-Review Checklist

- [ ] `yeet host set` can plan a repair even when desired roots already match.
- [ ] ZFS services-root migrations create one child dataset per migrated
      service and for `catch`.
- [ ] Target `db.json` is rewritten through typed DB fields, not raw JSON string replacement.
- [ ] Generated artifacts are rewritten only when yeet owns the artifact path.
- [ ] VM units are regenerated from typed state.
- [ ] Non-catch services affected by root changes or repair refs stop and restart.
- [ ] Catch is reinstalled and restarted after target DB/artifacts/units are ready.
- [ ] Validation fails if active old-root refs remain.
- [ ] Local `yeet.toml` clears roots that match the host default instead of pinning them.
- [ ] Bare `yeet info` shows host storage and inventory; `yeet info <svc>`
      remains service info.
- [ ] Old storage deletion is not automatic.
- [ ] Tests cover repair-only, full move, and config cleanup paths.
