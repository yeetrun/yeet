# Web Run ZFS Root Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a smart, workload-aware ZFS service-root picker to `yeet run --web` while preserving manual dataset entry.

**Architecture:** Catch owns ZFS discovery because it is the only process with authoritative host storage state. The local `yeet run --web` server exposes an authenticated browser route that proxies typed catch RPC results, and the browser renders suggestions as an input helper only. Existing `RunDraft` validation and deploy execution remain authoritative for the final `--service-root=<dataset> --zfs` contract.

**Tech Stack:** Go `testing`/`httptest`, `catchrpc` JSON-RPC types and client helpers, catch-side injectable `zfsCommandRunner`, embedded static HTML/CSS/JS through `go:embed`, website MDX docs.

---

## Scope Check

The approved spec is one feature with dependent layers:

- Catch ZFS discovery and RPC.
- Local web-run API proxy.
- Browser picker UI.
- User documentation and screenshot refresh.

These belong in one implementation plan because the browser picker cannot be verified without a typed discovery API, and discovery behavior must be tested before the UI depends on it.

Out of scope for this plan:

- Host-level favorite root configuration.
- A full ZFS tree browser.
- Creating missing parent datasets.
- Changing the deploy contract for ZFS service roots.

## File Structure

- Modify `pkg/catchrpc/types.go`: add typed ZFS root discovery request, response, state, and candidate structs.
- Modify `pkg/catchrpc/client.go`: add `ZFSServiceRootCandidates`.
- Modify `pkg/catchrpc/client_test.go`: cover the client helper method and JSON request params.
- Create `pkg/catch/zfs_root_candidates.go`: parse `zfs list`, classify host capability, rank candidates, and build suggested final datasets.
- Create `pkg/catch/zfs_root_candidates_test.go`: cover parsing, capability states, VM/service root scoring, internal dataset exclusion, and suggested dataset construction.
- Modify `pkg/catch/rpc.go`: dispatch `catch.ZFSServiceRootCandidates`.
- Modify `pkg/catch/rpc_test.go`: cover RPC dispatch success and invalid params.
- Create `pkg/yeet/run_web_zfs_roots.go`: local web-run helper that calls the selected catch host and maps method-not-found/network errors to discovery states.
- Modify `pkg/yeet/run_web_api.go`: add `/api/zfs-roots`.
- Modify `pkg/yeet/run_web_api_test.go`: cover route success, selected host, unsupported RPC, host-unreachable, and method restrictions.
- Modify `pkg/yeet/web_run_assets/index.html`: add the service-root picker button and dataset suggestion popover.
- Modify `pkg/yeet/web_run_assets/app.js`: load suggestions, render ranked candidates, fill suggested datasets, and preserve manual edits.
- Modify `pkg/yeet/web_run_assets/styles.css`: style dataset suggestion rows using the existing compact picker language.
- Modify `pkg/yeet/web_run_assets_test.go`: assert required DOM hooks and JS behavior hooks exist.
- Modify `website/docs/cli/yeet-cli.mdx`: document the ZFS picker at a user level.
- Modify the web-run screenshot in the website repo after visual verification.

Use focused commits after each task. The root repo may include a website submodule pointer later, so commit website changes inside `website/` before committing the root pointer.

## Task 1: Add Catch RPC Wire Types

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/client.go`
- Modify: `pkg/catchrpc/client_test.go`

- [ ] **Step 1: Write the failing client helper test**

Add this test near `TestServiceInfoCallsRPC` in `pkg/catchrpc/client_test.go`:

```go
func TestZFSServiceRootCandidatesCallsRPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "catch.ZFSServiceRootCandidates" {
			t.Fatalf("method = %q, want catch.ZFSServiceRootCandidates", req.Method)
		}
		var params ZFSServiceRootCandidatesRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Workload != "vm" || params.Service != "devbox" {
			t.Fatalf("params = %#v, want vm/devbox", params)
		}
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ZFSServiceRootCandidatesResponse{
				State: ZFSRootDiscoveryAvailable,
				Candidates: []ZFSServiceRootCandidate{{
					Dataset:          "flash/yeet/vms",
					Mountpoint:       "/flash/yeet/vms",
					FreeBytes:        1024,
					ChildCount:       4,
					VMChildCount:     4,
					SuggestedDataset: "flash/yeet/vms/devbox",
					Label:            "VM services root",
					Rank:             100,
				}},
			},
		})
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	got, err := NewClient(host, port).ZFSServiceRootCandidates(context.Background(), ZFSServiceRootCandidatesRequest{
		Workload: "vm",
		Service:  "devbox",
	})
	if err != nil {
		t.Fatalf("ZFSServiceRootCandidates returned error: %v", err)
	}
	if got.State != ZFSRootDiscoveryAvailable || len(got.Candidates) != 1 {
		t.Fatalf("response = %#v, want one available candidate", got)
	}
	if got.Candidates[0].SuggestedDataset != "flash/yeet/vms/devbox" {
		t.Fatalf("suggested dataset = %q", got.Candidates[0].SuggestedDataset)
	}
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catchrpc -run TestZFSServiceRootCandidatesCallsRPC -count=1
```

Expected: fail because `ZFSServiceRootCandidatesRequest`, `ZFSRootDiscoveryAvailable`, `ZFSServiceRootCandidate`, and `Client.ZFSServiceRootCandidates` do not exist.

- [ ] **Step 3: Add the wire types**

Add this block in `pkg/catchrpc/types.go` after `ArtifactHashesResponse`:

```go
type ZFSRootDiscoveryState string

const (
	ZFSRootDiscoveryAvailable      ZFSRootDiscoveryState = "available"
	ZFSRootDiscoveryHostUnreachable ZFSRootDiscoveryState = "host-unreachable"
	ZFSRootDiscoveryUnsupportedRPC ZFSRootDiscoveryState = "unsupported-rpc"
	ZFSRootDiscoveryZFSMissing     ZFSRootDiscoveryState = "zfs-missing"
	ZFSRootDiscoveryNoFilesystems  ZFSRootDiscoveryState = "no-filesystems"
	ZFSRootDiscoveryError          ZFSRootDiscoveryState = "error"
)

type ZFSServiceRootCandidatesRequest struct {
	Workload string `json:"workload,omitempty"`
	Service  string `json:"service,omitempty"`
}

type ZFSServiceRootCandidate struct {
	Dataset          string `json:"dataset"`
	Mountpoint       string `json:"mountpoint,omitempty"`
	FreeBytes        int64  `json:"freeBytes,omitempty"`
	ChildCount       int    `json:"childCount,omitempty"`
	VMChildCount     int    `json:"vmChildCount,omitempty"`
	ServiceChildCount int    `json:"serviceChildCount,omitempty"`
	SuggestedDataset string `json:"suggestedDataset,omitempty"`
	Label            string `json:"label,omitempty"`
	Rank             int    `json:"rank,omitempty"`
}

type ZFSServiceRootCandidatesResponse struct {
	State      ZFSRootDiscoveryState     `json:"state"`
	Candidates []ZFSServiceRootCandidate `json:"candidates,omitempty"`
	Warnings   []string                  `json:"warnings,omitempty"`
}
```

Run `gofmt -w pkg/catchrpc/types.go` after adding the block.

- [ ] **Step 4: Add the client helper**

Add this method at the end of `pkg/catchrpc/client.go`, after `ServiceInfo`:

```go
func (c *Client) ZFSServiceRootCandidates(ctx context.Context, req ZFSServiceRootCandidatesRequest) (ZFSServiceRootCandidatesResponse, error) {
	var resp ZFSServiceRootCandidatesResponse
	err := c.Call(ctx, "catch.ZFSServiceRootCandidates", req, &resp)
	return resp, err
}
```

Run `gofmt -w pkg/catchrpc/client.go`.

- [ ] **Step 5: Run the focused package test**

Run:

```bash
mise exec -- go test ./pkg/catchrpc -run TestZFSServiceRootCandidatesCallsRPC -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

Run:

```bash
git add pkg/catchrpc/types.go pkg/catchrpc/client.go pkg/catchrpc/client_test.go
git commit -m "catchrpc: add zfs root discovery types" -- pkg/catchrpc/types.go pkg/catchrpc/client.go pkg/catchrpc/client_test.go
```

## Task 2: Implement Catch-Side ZFS Candidate Discovery

**Files:**
- Create: `pkg/catch/zfs_root_candidates.go`
- Create: `pkg/catch/zfs_root_candidates_test.go`

- [ ] **Step 1: Write failing discovery tests**

Create `pkg/catch/zfs_root_candidates_test.go`:

```go
package catch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestZFSServiceRootCandidatesRanksVMRoot(t *testing.T) {
	out := strings.Join([]string{
		"flash\tfilesystem\t/flash\t1000\t400\t100\t-\ton\toff",
		"flash/yeet\tfilesystem\t/flash/yeet\t1000\t300\t1\t-\ton\toff",
		"flash/yeet/vms\tfilesystem\t/flash/yeet/vms\t1000\t30\t1\t-\ton\toff",
		"flash/yeet/vms/devbox\tfilesystem\t/flash/yeet/vms/devbox\t1000\t10\t1\t-\ton\toff",
		"flash/yeet/vms/devbox/root\tvolume\t-\t1000\t10\t10\tflash/yeet/vm-images/ubuntu/root@snap\t-\toff",
		"flash/yeet/vm-images\tfilesystem\t/flash/yeet/vm-images\t1000\t20\t1\t-\ton\toff",
		"flash/yeet/vm-images/ubuntu\tfilesystem\t/flash/yeet/vm-images/ubuntu\t1000\t20\t1\t-\ton\toff",
		"flash/yeet/vm-images/ubuntu/root\tvolume\t-\t1000\t20\t20\t-\t-\toff",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "vm",
		Service:  "devbox",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if resp.State != catchrpc.ZFSRootDiscoveryAvailable {
		t.Fatalf("state = %q, want available", resp.State)
	}
	if len(resp.Candidates) == 0 {
		t.Fatal("no candidates returned")
	}
	if resp.Candidates[0].Dataset != "flash/yeet/vms" {
		t.Fatalf("top candidate = %#v, want flash/yeet/vms", resp.Candidates[0])
	}
	if resp.Candidates[0].SuggestedDataset != "flash/yeet/vms/devbox" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
	for _, candidate := range resp.Candidates {
		if strings.Contains(candidate.Dataset, "/vm-images") {
			t.Fatalf("internal vm-images dataset returned: %#v", candidate)
		}
	}
}

func TestZFSServiceRootCandidatesRanksServiceRootForCompose(t *testing.T) {
	out := strings.Join([]string{
		"flash\tfilesystem\t/flash\t1000\t400\t100\t-\ton\toff",
		"flash/yeet\tfilesystem\t/flash/yeet\t1000\t300\t1\t-\ton\toff",
		"flash/yeet/radarr\tfilesystem\t/flash/yeet/radarr\t1000\t10\t10\t-\ton\toff",
		"flash/yeet/sonarr\tfilesystem\t/flash/yeet/sonarr\t1000\t10\t10\t-\ton\toff",
		"flash/yeet/vms\tfilesystem\t/flash/yeet/vms\t1000\t30\t1\t-\ton\toff",
		"flash/yeet/vms/devbox\tfilesystem\t/flash/yeet/vms/devbox\t1000\t10\t1\t-\ton\toff",
		"flash/yeet/vms/devbox/root\tvolume\t-\t1000\t10\t10\tflash/yeet/vm-images/ubuntu/root@snap\t-\toff",
	}, "\n") + "\n"
	resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(out, "", nil), catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "compose",
		Service:  "radarr",
	})
	if err != nil {
		t.Fatalf("zfsServiceRootCandidates: %v", err)
	}
	if len(resp.Candidates) < 2 {
		t.Fatalf("candidates = %#v, want at least two", resp.Candidates)
	}
	if resp.Candidates[0].Dataset != "flash/yeet" {
		t.Fatalf("top candidate = %#v, want flash/yeet", resp.Candidates[0])
	}
	if resp.Candidates[0].SuggestedDataset != "flash/yeet/radarr" {
		t.Fatalf("suggested = %q", resp.Candidates[0].SuggestedDataset)
	}
}

func TestZFSServiceRootCandidatesCapabilityStates(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		err    error
		want   catchrpc.ZFSRootDiscoveryState
	}{
		{name: "zfs missing", stderr: "zfs: command not found", err: errors.New("exec: zfs: executable file not found"), want: catchrpc.ZFSRootDiscoveryZFSMissing},
		{name: "no filesystems", stdout: "tank/root\tvolume\t-\t1\t1\t1\t-\t-\toff\n", want: catchrpc.ZFSRootDiscoveryNoFilesystems},
		{name: "command error", stderr: "permission denied", err: errZFSCommandFailed, want: catchrpc.ZFSRootDiscoveryError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := zfsServiceRootCandidates(context.Background(), fakeZFSListRunner(tt.stdout, tt.stderr, tt.err), catchrpc.ZFSServiceRootCandidatesRequest{})
			if err != nil {
				t.Fatalf("zfsServiceRootCandidates returned error: %v", err)
			}
			if resp.State != tt.want {
				t.Fatalf("state = %q, want %q response=%#v", resp.State, tt.want, resp)
			}
		})
	}
}

func TestSuggestedZFSDatasetUsesTrailingSlashWithoutService(t *testing.T) {
	if got := suggestedZFSDataset("flash/yeet/vms", ""); got != "flash/yeet/vms/" {
		t.Fatalf("suggested empty service = %q", got)
	}
	if got := suggestedZFSDataset("flash/yeet/vms/", " devbox "); got != "flash/yeet/vms/devbox" {
		t.Fatalf("suggested service = %q", got)
	}
}

func TestParseZFSRootCandidateRowsRejectsMalformedRows(t *testing.T) {
	_, err := parseZFSRootCandidateRows("flash\tfilesystem\n")
	if err == nil || !strings.Contains(err.Error(), "invalid zfs list row") {
		t.Fatalf("parse error = %v, want invalid row", err)
	}
}

func fakeZFSListRunner(stdout, stderr string, err error) zfsCommandRunner {
	return func(ctx context.Context, args ...string) (string, string, error) {
		want := []string{"list", "-H", "-p", "-o", "name,type,mountpoint,available,used,refer,origin,canmount,readonly", "-t", "filesystem,volume"}
		if !reflect.DeepEqual(args, want) {
			return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
		}
		return stdout, stderr, err
	}
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestZFSServiceRootCandidates|TestSuggestedZFSDataset|TestParseZFSRootCandidateRows' -count=1
```

Expected: fail because the discovery functions do not exist.

- [ ] **Step 3: Implement the discovery file**

Create `pkg/catch/zfs_root_candidates.go` with these exported behavior seams and helpers:

```go
package catch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

const maxZFSRootCandidates = 12

type zfsRootCandidateRow struct {
	Name       string
	Type       string
	Mountpoint string
	Available  int64
	Used       int64
	Refer      int64
	Origin     string
	Canmount   string
	Readonly   string
}

type zfsRootCandidateNode struct {
	row      zfsRootCandidateRow
	parent   string
	children []string
}

func (s *Server) zfsServiceRootCandidates(ctx context.Context, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
	return zfsServiceRootCandidates(ctx, s.zfsRunner, req)
}

func zfsServiceRootCandidates(ctx context.Context, runner zfsCommandRunner, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	stdout, stderr, err := runner(ctx, "list", "-H", "-p", "-o", "name,type,mountpoint,available,used,refer,origin,canmount,readonly", "-t", "filesystem,volume")
	if err != nil {
		return zfsDiscoveryErrorResponse(stderr, err), nil
	}
	rows, err := parseZFSRootCandidateRows(stdout)
	if err != nil {
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryError, Warnings: []string{err.Error()}}, nil
	}
	tree := buildZFSRootCandidateTree(rows)
	candidates := rankZFSRootCandidates(tree, req)
	if len(candidates) == 0 {
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryNoFilesystems}, nil
	}
	if len(candidates) > maxZFSRootCandidates {
		candidates = candidates[:maxZFSRootCandidates]
	}
	return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryAvailable, Candidates: candidates}, nil
}

func zfsDiscoveryErrorResponse(stderr string, err error) catchrpc.ZFSServiceRootCandidatesResponse {
	msg := strings.TrimSpace(stderr)
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if zfsDiscoveryMissing(err, msg) {
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryZFSMissing, Warnings: []string{"zfs is not installed or not available"}}
	}
	if msg == "" {
		msg = "zfs discovery failed"
	}
	return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryError, Warnings: []string{msg}}
}

func zfsDiscoveryMissing(err error, msg string) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "command not found") || strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file")
}

func parseZFSRootCandidateRows(stdout string) ([]zfsRootCandidateRow, error) {
	var rows []zfsRootCandidateRow
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 9 {
			return nil, fmt.Errorf("invalid zfs list row %q", line)
		}
		available, err := parseZFSInt(fields[3])
		if err != nil {
			return nil, fmt.Errorf("invalid zfs available value %q: %w", fields[3], err)
		}
		used, err := parseZFSInt(fields[4])
		if err != nil {
			return nil, fmt.Errorf("invalid zfs used value %q: %w", fields[4], err)
		}
		refer, err := parseZFSInt(fields[5])
		if err != nil {
			return nil, fmt.Errorf("invalid zfs refer value %q: %w", fields[5], err)
		}
		rows = append(rows, zfsRootCandidateRow{
			Name:       fields[0],
			Type:       fields[1],
			Mountpoint: fields[2],
			Available:  available,
			Used:       used,
			Refer:      refer,
			Origin:     fields[6],
			Canmount:   fields[7],
			Readonly:   fields[8],
		})
	}
	return rows, nil
}

func parseZFSInt(raw string) (int64, error) {
	if raw == "-" {
		return 0, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

func buildZFSRootCandidateTree(rows []zfsRootCandidateRow) map[string]*zfsRootCandidateNode {
	tree := make(map[string]*zfsRootCandidateNode, len(rows))
	for _, row := range rows {
		tree[row.Name] = &zfsRootCandidateNode{row: row, parent: zfsDatasetParent(row.Name)}
	}
	for name, node := range tree {
		if parent := node.parent; parent != "" {
			if parentNode := tree[parent]; parentNode != nil {
				parentNode.children = append(parentNode.children, name)
			}
		}
	}
	return tree
}

func zfsDatasetParent(name string) string {
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		return name[:idx]
	}
	return ""
}

func rankZFSRootCandidates(tree map[string]*zfsRootCandidateNode, req catchrpc.ZFSServiceRootCandidatesRequest) []catchrpc.ZFSServiceRootCandidate {
	var out []catchrpc.ZFSServiceRootCandidate
	for name, node := range tree {
		if !zfsRootCandidateUsable(node.row) || zfsRootCandidateInternal(name) {
			continue
		}
		candidate := buildZFSRootCandidate(tree, node, req)
		if candidate.Rank <= 0 {
			continue
		}
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank > out[j].Rank
		}
		return out[i].Dataset < out[j].Dataset
	})
	return out
}

func zfsRootCandidateUsable(row zfsRootCandidateRow) bool {
	if row.Type != "filesystem" {
		return false
	}
	if row.Mountpoint == "" || row.Mountpoint == "-" || row.Mountpoint == "legacy" {
		return false
	}
	if row.Canmount == "off" || row.Readonly == "on" {
		return false
	}
	return true
}

func zfsRootCandidateInternal(name string) bool {
	return strings.HasSuffix(name, "/vm-images") || strings.Contains(name, "/vm-images/")
}

func buildZFSRootCandidate(tree map[string]*zfsRootCandidateNode, node *zfsRootCandidateNode, req catchrpc.ZFSServiceRootCandidatesRequest) catchrpc.ZFSServiceRootCandidate {
	childCount, vmChildCount, serviceChildCount := zfsRootCandidateChildCounts(tree, node)
	workload := strings.TrimSpace(req.Workload)
	rank := zfsRootCandidateRank(node.row.Name, workload, childCount, vmChildCount, serviceChildCount)
	return catchrpc.ZFSServiceRootCandidate{
		Dataset:           node.row.Name,
		Mountpoint:        node.row.Mountpoint,
		FreeBytes:         node.row.Available,
		ChildCount:        childCount,
		VMChildCount:      vmChildCount,
		ServiceChildCount: serviceChildCount,
		SuggestedDataset:  suggestedZFSDataset(node.row.Name, req.Service),
		Label:             zfsRootCandidateLabel(workload, childCount, vmChildCount, serviceChildCount),
		Rank:              rank,
	}
}

func zfsRootCandidateChildCounts(tree map[string]*zfsRootCandidateNode, node *zfsRootCandidateNode) (childCount int, vmChildCount int, serviceChildCount int) {
	for _, childName := range node.children {
		child := tree[childName]
		if child == nil || child.row.Type != "filesystem" || zfsRootCandidateInternal(childName) {
			continue
		}
		childCount++
		if zfsRootCandidateHasRootVolume(tree, childName) {
			vmChildCount++
			continue
		}
		if zfsRootCandidateUsable(child.row) {
			serviceChildCount++
		}
	}
	return childCount, vmChildCount, serviceChildCount
}

func zfsRootCandidateHasRootVolume(tree map[string]*zfsRootCandidateNode, dataset string) bool {
	root := tree[dataset+"/root"]
	return root != nil && root.row.Type == "volume"
}

func zfsRootCandidateRank(name, workload string, childCount, vmChildCount, serviceChildCount int) int {
	if childCount == 0 {
		return 0
	}
	rank := childCount
	switch workload {
	case "vm":
		rank += vmChildCount * 25
		rank += serviceChildCount * 2
		if strings.HasSuffix(name, "/vms") {
			rank += 20
		}
		if strings.Contains(name, "/yeet/vms") {
			rank += 10
		}
	default:
		rank += serviceChildCount * 20
		rank += vmChildCount * 2
		if strings.HasSuffix(name, "/yeet") || strings.HasSuffix(name, "/apps") || strings.HasSuffix(name, "/services") {
			rank += 20
		}
		if strings.HasSuffix(name, "/vms") {
			rank -= 15
		}
	}
	if !strings.Contains(name, "/") {
		rank -= 10
	}
	return rank
}

func zfsRootCandidateLabel(workload string, childCount, vmChildCount, serviceChildCount int) string {
	if workload == "vm" && vmChildCount > 0 {
		return "VM services root"
	}
	if serviceChildCount > 0 {
		return "Services root"
	}
	if childCount > 0 {
		return "Dataset root"
	}
	return "ZFS dataset"
}

func suggestedZFSDataset(root, service string) string {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	service = strings.Trim(strings.TrimSpace(service), "/")
	if service == "" {
		return root + "/"
	}
	return root + "/" + service
}
```

Run:

```bash
gofmt -w pkg/catch/zfs_root_candidates.go pkg/catch/zfs_root_candidates_test.go
```

- [ ] **Step 4: Run the focused catch tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestZFSServiceRootCandidates|TestSuggestedZFSDataset|TestParseZFSRootCandidateRows' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

Run:

```bash
git add pkg/catch/zfs_root_candidates.go pkg/catch/zfs_root_candidates_test.go
git commit -m "catch: rank zfs service root candidates" -- pkg/catch/zfs_root_candidates.go pkg/catch/zfs_root_candidates_test.go
```

## Task 3: Expose Discovery Through Catch RPC

**Files:**
- Modify: `pkg/catch/rpc.go`
- Modify: `pkg/catch/rpc_test.go`

- [ ] **Step 1: Write the failing RPC dispatch test**

Add this test near the other RPC tests in `pkg/catch/rpc_test.go`:

```go
func TestRPCZFSServiceRootCandidates(t *testing.T) {
	server := newTestServer(t)
	server.zfsRunner = fakeZFSListRunner(strings.Join([]string{
		"flash\tfilesystem\t/flash\t1000\t400\t100\t-\ton\toff",
		"flash/yeet\tfilesystem\t/flash/yeet\t1000\t300\t1\t-\ton\toff",
		"flash/yeet/vms\tfilesystem\t/flash/yeet/vms\t1000\t30\t1\t-\ton\toff",
		"flash/yeet/vms/devbox\tfilesystem\t/flash/yeet/vms/devbox\t1000\t10\t1\t-\ton\toff",
		"flash/yeet/vms/devbox/root\tvolume\t-\t1000\t10\t10\tflash/yeet/vm-images/ubuntu/root@snap\t-\toff",
	}, "\n")+"\n", "", nil)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	params, err := json.Marshal(catchrpc.ZFSServiceRootCandidatesRequest{Workload: "vm", Service: "newbox"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.ZFSServiceRootCandidates",
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp catchrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcResp.Error)
	}
	var result catchrpc.ZFSServiceRootCandidatesResponse
	b, _ := json.Marshal(rpcResp.Result)
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.State != catchrpc.ZFSRootDiscoveryAvailable || len(result.Candidates) == 0 {
		t.Fatalf("result = %#v, want available candidates", result)
	}
	if got := result.Candidates[0].SuggestedDataset; got != "flash/yeet/vms/newbox" {
		t.Fatalf("suggested dataset = %q", got)
	}
}
```

- [ ] **Step 2: Run the focused RPC test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRPCZFSServiceRootCandidates -count=1
```

Expected: fail with RPC method not found.

- [ ] **Step 3: Add RPC dispatch and handler**

In `pkg/catch/rpc.go`, add this case in `dispatchRPC`:

```go
case "catch.ZFSServiceRootCandidates":
	return s.handleRPCZFSServiceRootCandidates(req)
```

Add this handler near the other RPC handlers:

```go
func (s *Server) handleRPCZFSServiceRootCandidates(req catchrpc.Request) catchrpc.Response {
	var params catchrpc.ZFSServiceRootCandidatesRequest
	if rpcErr := decodeRPCParams(req.Params, &params); rpcErr != nil {
		return responseFromRPCError(req.ID, rpcErr)
	}
	resp, err := s.zfsServiceRootCandidates(context.Background(), params)
	if err != nil {
		return newRPCError(req.ID, catchrpc.ErrInternal, "failed to list zfs service roots", err.Error())
	}
	return newRPCResponse(req.ID, resp)
}
```

Run:

```bash
gofmt -w pkg/catch/rpc.go
```

- [ ] **Step 4: Run the focused RPC test**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRPCZFSServiceRootCandidates|TestRPCMethodNotFound' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

Run:

```bash
git add pkg/catch/rpc.go pkg/catch/rpc_test.go
git commit -m "catch: expose zfs root discovery rpc" -- pkg/catch/rpc.go pkg/catch/rpc_test.go
```

## Task 4: Add The Local Web API Proxy

**Files:**
- Create: `pkg/yeet/run_web_zfs_roots.go`
- Modify: `pkg/yeet/run_web_api.go`
- Modify: `pkg/yeet/run_web_api_test.go`

- [ ] **Step 1: Write failing web API tests**

Add these tests in `pkg/yeet/run_web_api_test.go` near the other API route tests:

```go
func TestRunWebAPIZFSRootsUsesSelectedHost(t *testing.T) {
	oldFetch := fetchRunWebZFSRootCandidatesFn
	defer func() { fetchRunWebZFSRootCandidatesFn = oldFetch }()
	var gotHost string
	var gotReq catchrpc.ZFSServiceRootCandidatesRequest
	fetchRunWebZFSRootCandidatesFn = func(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
		gotHost = host
		gotReq = req
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State: catchrpc.ZFSRootDiscoveryAvailable,
			Candidates: []catchrpc.ZFSServiceRootCandidate{{
				Dataset:          "flash/yeet/vms",
				SuggestedDataset: "flash/yeet/vms/devbox",
			}},
		}, nil
	}

	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/zfs-roots?workload=vm&service=devbox", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotHost != "yeet-lab" {
		t.Fatalf("host = %q, want yeet-lab", gotHost)
	}
	if gotReq.Workload != "vm" || gotReq.Service != "devbox" {
		t.Fatalf("request = %#v, want vm/devbox", gotReq)
	}
	var resp catchrpc.ZFSServiceRootCandidatesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.State != catchrpc.ZFSRootDiscoveryAvailable || len(resp.Candidates) != 1 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestRunWebAPIZFSRootsMapsErrorsToStates(t *testing.T) {
	oldFetch := fetchRunWebZFSRootCandidatesFn
	defer func() { fetchRunWebZFSRootCandidatesFn = oldFetch }()
	tests := []struct {
		name string
		err  error
		want catchrpc.ZFSRootDiscoveryState
	}{
		{name: "method not found", err: errors.New("rpc error -32601: method not found"), want: catchrpc.ZFSRootDiscoveryUnsupportedRPC},
		{name: "connection refused", err: errors.New("dial tcp 127.0.0.1:8868: connect: connection refused"), want: catchrpc.ZFSRootDiscoveryHostUnreachable},
		{name: "other", err: errors.New("boom"), want: catchrpc.ZFSRootDiscoveryError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRunWebZFSRootCandidatesFn = func(context.Context, string, catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
				return catchrpc.ZFSServiceRootCandidatesResponse{}, tt.err
			}
			s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir(), Bootstrap: runWebBootstrap{SelectedHost: "host-a"}})
			rec := runWebAPIRequest(t, s, http.MethodGet, "/api/zfs-roots?workload=compose&service=app", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var resp catchrpc.ZFSServiceRootCandidatesResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.State != tt.want {
				t.Fatalf("state = %q, want %q response=%#v", resp.State, tt.want, resp)
			}
		})
	}
}

func TestRunWebAPIZFSRootsRejectsBadMethods(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/zfs-roots", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
```

- [ ] **Step 2: Run the focused API tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWebAPIZFSRoots' -count=1
```

Expected: fail because `/api/zfs-roots` and `fetchRunWebZFSRootCandidatesFn` do not exist.

- [ ] **Step 3: Add the fetch helper**

Create `pkg/yeet/run_web_zfs_roots.go`:

```go
package yeet

import (
	"context"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

var fetchRunWebZFSRootCandidatesFn = fetchRunWebZFSRootCandidates

func fetchRunWebZFSRootCandidates(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
	return newRPCClient(host).ZFSServiceRootCandidates(ctx, req)
}

func runWebZFSRootErrorResponse(err error) catchrpc.ZFSServiceRootCandidatesResponse {
	if err == nil {
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryError}
	}
	msg := err.Error()
	if isRPCMethodNotFound(err) {
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryUnsupportedRPC, Warnings: []string{"this catch version does not support zfs root discovery"}}
	}
	if runWebZFSRootHostUnreachable(msg) {
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryHostUnreachable, Warnings: []string{msg}}
	}
	return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryError, Warnings: []string{msg}}
}

func runWebZFSRootHostUnreachable(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "network is unreachable")
}
```

Run:

```bash
gofmt -w pkg/yeet/run_web_zfs_roots.go
```

- [ ] **Step 4: Add the route**

In `pkg/yeet/run_web_api.go`, register the route in `newRunWebServer`:

```go
s.mux.HandleFunc("/api/zfs-roots", s.handleZFSRoots)
```

Add this handler after `handleFiles`:

```go
func (s *runWebServer) handleZFSRoots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		host = strings.TrimSpace(s.cfg.Bootstrap.SelectedHost)
	}
	if host == "" {
		host = Host()
	}
	req := catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: strings.TrimSpace(r.URL.Query().Get("workload")),
		Service:  strings.TrimSpace(r.URL.Query().Get("service")),
	}
	ctx, cancel := runWebHandlerContext(s.cfg.Context, r.Context())
	defer cancel()
	resp, err := fetchRunWebZFSRootCandidatesFn(ctx, host, req)
	if err != nil {
		writeRunWebJSON(w, http.StatusOK, runWebZFSRootErrorResponse(err))
		return
	}
	writeRunWebJSON(w, http.StatusOK, resp)
}
```

Run:

```bash
gofmt -w pkg/yeet/run_web_api.go
```

- [ ] **Step 5: Run the focused API tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWebAPIZFSRoots' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

Run:

```bash
git add pkg/yeet/run_web_zfs_roots.go pkg/yeet/run_web_api.go pkg/yeet/run_web_api_test.go
git commit -m "yeet: proxy zfs root suggestions to web run" -- pkg/yeet/run_web_zfs_roots.go pkg/yeet/run_web_api.go pkg/yeet/run_web_api_test.go
```

## Task 5: Add The Browser Picker UI

**Files:**
- Modify: `pkg/yeet/web_run_assets/index.html`
- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/web_run_assets/styles.css`
- Modify: `pkg/yeet/web_run_assets_test.go`

- [ ] **Step 1: Write failing web asset assertions**

In `pkg/yeet/web_run_assets_test.go`, add these required snippets to `TestWebRunAssetsExposeFirstDeployFields`:

```go
for _, id := range []string{
	`id="serviceRootPicker"`,
	`id="zfsRootPicker"`,
	`id="zfsRootList"`,
	`id="zfsRootStatus"`,
} {
	if !strings.Contains(string(index), id) {
		t.Fatalf("index missing %s", id)
	}
}
for _, snippet := range []string{
	"zfsRootState",
	"loadZFSRoots",
	"renderZFSRootCandidates",
	"showZFSRootPicker",
	"hideZFSRootPicker",
	"pickZFSRootCandidate",
	"state.pickedZFSRoot",
	"/api/zfs-roots?",
	"syncPickedZFSRootValue",
} {
	if !strings.Contains(string(app), snippet) {
		t.Fatalf("app missing zfs picker hook %s", snippet)
	}
}
for _, snippet := range []string{
	".zfs-root-row",
	".zfs-root-meta",
	".zfs-root-status",
} {
	if !strings.Contains(string(styles), snippet) {
		t.Fatalf("styles missing %s", snippet)
	}
}
```

- [ ] **Step 2: Run the focused asset test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: fail because the picker DOM and JS hooks do not exist.

- [ ] **Step 3: Update the service-root markup**

In `pkg/yeet/web_run_assets/index.html`, replace the service-root input inside `.root-control` with:

```html
<label for="serviceRoot" data-field="serviceRoot">
  <span id="storageModeLabel">Service root <button type="button" class="help" tabindex="-1" data-help="Leave empty to use the catch default root. Enter an absolute filesystem path, or a dataset name when ZFS is enabled." aria-label="Help for service root">?</button></span>
  <div class="picker-field">
    <input id="serviceRoot" name="serviceRoot" autocomplete="off" spellcheck="false">
    <button type="button" id="serviceRootPicker" class="quiet-button picker-trigger" hidden>Pick</button>
  </div>
  <span class="field-error" id="serviceRootError"></span>
</label>
```

After the existing `filePicker` popover, add:

```html
<div class="field-picker" id="zfsRootPicker" role="listbox" hidden>
  <div class="browser-head">
    <span>ZFS roots</span>
    <span class="zfs-root-status" id="zfsRootStatus"></span>
  </div>
  <div id="zfsRootList" class="picker-list" aria-label="ZFS service root suggestions"></div>
</div>
```

- [ ] **Step 4: Add picker state and helpers**

In `pkg/yeet/web_run_assets/app.js`, add these fields to `state`:

```js
zfsRootState: "idle",
zfsRootCandidates: [],
zfsRootLoadKey: "",
zfsRootSeq: 0,
pickedZFSRoot: null,
```

Add these helper functions near the file picker functions:

```js
function zfsRootQueryKey() {
  return [
    $("host").value.trim(),
    selectedWorkload(),
    $("service").value.trim(),
  ].join("\n");
}

async function loadZFSRoots(force = false) {
  if (!$("zfs").checked || $("serviceRoot").disabled) return;
  const key = zfsRootQueryKey();
  if (!force && key === state.zfsRootLoadKey && state.zfsRootState !== "error") return;
  state.zfsRootLoadKey = key;
  state.zfsRootState = "loading";
  renderZFSRootCandidates();
  const seq = ++state.zfsRootSeq;
  const params = new URLSearchParams({
    host: $("host").value.trim(),
    workload: selectedWorkload(),
    service: $("service").value.trim(),
  });
  try {
    const res = await api(`/api/zfs-roots?${params.toString()}`);
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    if (seq !== state.zfsRootSeq) return;
    state.zfsRootState = data.state || "error";
    state.zfsRootCandidates = data.candidates || [];
    renderZFSRootCandidates(data.warnings || []);
  } catch (error) {
    if (seq !== state.zfsRootSeq) return;
    state.zfsRootState = "error";
    state.zfsRootCandidates = [];
    renderZFSRootCandidates([error.message || "Could not load ZFS roots"]);
  }
}

function renderZFSRootCandidates(warnings = []) {
  const status = $("zfsRootStatus");
  const list = $("zfsRootList");
  if (state.zfsRootState === "loading") {
    status.textContent = "Loading";
    list.replaceChildren(emptyPickerState("Loading ZFS roots"));
    return;
  }
  const message = zfsRootStateMessage(state.zfsRootState, warnings);
  status.textContent = state.zfsRootState === "available" ? `${state.zfsRootCandidates.length} found` : "";
  const rows = state.zfsRootCandidates.map((candidate) => zfsRootCandidateRow(candidate));
  if (!rows.length) rows.push(emptyPickerState(message || "No ZFS roots found"));
  list.replaceChildren(...rows);
}

function zfsRootStateMessage(stateName, warnings) {
  if (warnings.length) return warnings[0];
  switch (stateName) {
    case "host-unreachable":
      return "Could not reach this host";
    case "unsupported-rpc":
      return "This catch version does not support ZFS root discovery";
    case "zfs-missing":
      return "ZFS is not installed on this host";
    case "no-filesystems":
      return "No ZFS filesystem datasets were found on this host";
    case "error":
      return "Could not load ZFS roots";
    default:
      return "";
  }
}

function emptyPickerState(text) {
  const empty = document.createElement("div");
  empty.className = "empty-state";
  empty.textContent = text;
  return empty;
}

function zfsRootCandidateRow(candidate) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "zfs-root-row";
  button.setAttribute("role", "option");
  const name = document.createElement("span");
  name.className = "file-name";
  name.textContent = candidate.dataset || candidate.suggestedDataset || "";
  const meta = document.createElement("span");
  meta.className = "zfs-root-meta";
  meta.textContent = zfsRootCandidateMeta(candidate);
  button.append(name, meta);
  button.addEventListener("click", () => pickZFSRootCandidate(candidate));
  return button;
}

function zfsRootCandidateMeta(candidate) {
  const parts = [];
  if (candidate.label) parts.push(candidate.label);
  if (candidate.vmChildCount) parts.push(`${candidate.vmChildCount} VMs`);
  else if (candidate.serviceChildCount) parts.push(`${candidate.serviceChildCount} services`);
  else if (candidate.childCount) parts.push(`${candidate.childCount} children`);
  if (candidate.freeBytes) parts.push(`${formatBytes(candidate.freeBytes)} free`);
  return parts.join(" | ");
}

function formatBytes(bytes) {
  const units = ["B", "K", "M", "G", "T", "P"];
  let value = Number(bytes) || 0;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)}${units[unit]}`;
}

function pickZFSRootCandidate(candidate) {
  const value = candidate.suggestedDataset || `${(candidate.dataset || "").replace(/\/+$/, "")}/${$("service").value.trim()}`;
  $("serviceRoot").value = value;
  state.pickedZFSRoot = {
    dataset: candidate.dataset || "",
    value,
  };
  hideZFSRootPicker();
  update();
}

function syncPickedZFSRootValue() {
  if (!state.pickedZFSRoot) return;
  if ($("serviceRoot").value.trim() !== state.pickedZFSRoot.value) {
    state.pickedZFSRoot = null;
    return;
  }
  const service = $("service").value.trim();
  const root = state.pickedZFSRoot.dataset.replace(/\/+$/, "");
  const next = service ? `${root}/${service}` : `${root}/`;
  $("serviceRoot").value = next;
  state.pickedZFSRoot.value = next;
}

function showZFSRootPicker() {
  if (!$("zfs").checked || $("serviceRoot").disabled) return;
  const input = $("serviceRoot");
  const picker = $("zfsRootPicker");
  const rect = input.getBoundingClientRect();
  picker.style.left = `${Math.max(12, rect.left)}px`;
  picker.style.top = `${Math.min(window.innerHeight - 340, rect.bottom + 6)}px`;
  picker.style.width = `${Math.max(360, rect.width)}px`;
  picker.hidden = false;
  loadZFSRoots();
}

function hideZFSRootPicker() {
  $("zfsRootPicker").hidden = true;
}
```

- [ ] **Step 5: Wire picker visibility and events**

In `syncWorkloadUI`, after updating ZFS labels, add:

```js
const zfsPickerEnabled = $("zfs").checked && !$("serviceRoot").disabled;
$("serviceRootPicker").hidden = !zfsPickerEnabled;
if (!zfsPickerEnabled) hideZFSRootPicker();
```

At the start of `update()`, before `const draft = buildDraft();`, add:

```js
syncPickedZFSRootValue();
```

In the existing input listener setup, ensure service-root manual edits clear picker ownership:

```js
$("serviceRoot").addEventListener("input", () => {
  if (state.pickedZFSRoot && $("serviceRoot").value.trim() !== state.pickedZFSRoot.value) {
    state.pickedZFSRoot = null;
  }
});
```

Add these event listeners near the other bottom-of-file listeners:

```js
$("serviceRootPicker").addEventListener("click", showZFSRootPicker);
$("zfs").addEventListener("change", () => {
  state.zfsRootLoadKey = "";
  hideZFSRootPicker();
  update();
});
document.addEventListener("click", (event) => {
  if (event.target.closest("#zfsRootPicker") || event.target.closest("#serviceRootPicker")) return;
  hideZFSRootPicker();
});
```

If there is already a `zfs` change listener, merge the body instead of adding a duplicate listener.

- [ ] **Step 6: Add compact styles**

In `pkg/yeet/web_run_assets/styles.css`, add:

```css
.zfs-root-row {
  width: 100%;
  min-height: 42px;
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 3px;
  padding: 7px 8px;
  border: 0;
  border-radius: 6px;
  color: var(--text);
  background: transparent;
  text-align: left;
}

.zfs-root-row:hover,
.zfs-root-row:focus-visible {
  background: var(--surface-2);
}

.zfs-root-meta,
.zfs-root-status {
  overflow: hidden;
  color: var(--quiet);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
```

- [ ] **Step 7: Run the focused web asset test**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets/styles.css pkg/yeet/web_run_assets_test.go
git commit -m "yeet: add web run zfs root picker" -- pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets/styles.css pkg/yeet/web_run_assets_test.go
```

## Task 6: Update User Docs And Website Screenshot

**Files:**
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/public/images/web-run-deploy.png`
- Modify: root repo website submodule pointer after website commit

- [ ] **Step 1: Inspect website instructions and screenshot references**

Run:

```bash
sed -n '1,220p' website/AGENTS.md
rg -n "web run|--web|web-run-deploy.png" website/docs website/src -g'*.mdx' -g'*.md' -g'*.tsx'
ls -l website/public/images/web-run-deploy.png
```

Expected: confirm the existing manual section and screenshot reference before editing.

- [ ] **Step 2: Update the CLI manual copy**

In `website/docs/cli/yeet-cli.mdx`, update the `yeet run --web` section with concise user-facing text:

```mdx
When ZFS is enabled, the service root field accepts a dataset name. The web UI
can suggest dataset roots from the selected host, ranked for the selected
workload. For example, VM workloads may suggest `flash/yeet/vms` and fill
`flash/yeet/vms/<service>`. Manual dataset entry always remains available, which
is useful on hosts without ZFS discovery support or when you want a custom
dataset layout.
```

Keep the wording focused on user behavior and avoid internal RPC or scoring details.

- [ ] **Step 3: Refresh the screenshot**

Start the local web flow from the root repo:

```bash
mise exec -- go run ./cmd/yeet run --web
```

Open the printed URL, select a VM workload, enable ZFS, click the service-root picker, and capture a screenshot that shows the ZFS suggestions. Save it over `website/public/images/web-run-deploy.png`.

Expected visual checks:

- The picker is visible only after ZFS is enabled.
- Text does not overlap at desktop width.
- The service-root input remains editable.
- The screenshot still looks like the actual app, not a staged marketing mockup.

- [ ] **Step 4: Commit and push website changes**

From `website/`:

```bash
git status --short
git add docs/cli/yeet-cli.mdx public/images/web-run-deploy.png
git commit -m "docs: update web run zfs picker docs"
git push
```

- [ ] **Step 5: Commit root submodule pointer**

From the root repo:

```bash
git status --short
git add website
git commit -m "docs: update website for zfs root picker" -- website
```

Expected: root commit includes only the website submodule pointer.

## Task 7: Local And Live Verification

**Files:**
- No source edits expected. If verification finds a bug, add a focused fix task before committing.

- [ ] **Step 1: Run package tests for changed areas**

Run:

```bash
mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -count=1
```

Expected: pass.

- [ ] **Step 2: Run the full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: pass.

- [ ] **Step 3: Install catch on the VM-capable live host**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
```

Expected: catch installs successfully.

- [ ] **Step 4: Verify discovery directly through the web API or a small local command**

Start web run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run --web
```

Open the printed URL and verify:

- VM workload with ZFS ranks `flash/yeet/vms` above `flash/yeet`.
- Compose workload with ZFS ranks `flash/yeet` above `flash/yeet/vms`.
- Selecting `flash/yeet/vms` with service `codex-zfs-picker` fills `flash/yeet/vms/codex-zfs-picker`.
- Manual edits are not overwritten after typing a custom dataset.

- [ ] **Step 5: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: pass.

- [ ] **Step 6: Push all commits**

Run from the root repo:

```bash
git status --short --branch
git push
```

Expected: current branch pushes cleanly. If website changes were made, website must already be pushed from `website/`.
