# NetNS Docker NAT Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make yeet replay Docker service-netns DNAT rules from DB state without recreating containers.

**Architecture:** `pkg/dnet` becomes the single owner of netns-level port-forward derivation and replay. Docker lifecycle callbacks mutate DB endpoint/portmap state, then sync the full desired rule set for the affected namespace. Catch startup also asks `pkg/dnet` to replay existing namespace NAT state before the existing stale-veth reconciliation runs.

**Tech Stack:** Go, `net/http`, yeet JSON DB, Docker network-driver callbacks, Linux netns, iptables NAT.

---

## File Structure

- Modify `pkg/dnet/dnet.go`: add aggregate desired-rule helpers, callback request helpers, explicit external-connectivity handlers, and startup reconciliation.
- Modify `pkg/dnet/dnet_test.go`: add focused tests for aggregation, callback replay, external connectivity, and startup grouping.
- Modify `pkg/catch/catch.go`: call dnet startup NAT reconciliation from server startup.
- Modify `pkg/catch/netns_reconcile_test.go`: update startup-order tests and add non-fatal NAT reconciliation failure coverage.
- Leave `cmd/catch/catch.go` unchanged; the startup hook belongs in `pkg/catch` so tests can stub it without starting the command entrypoint.

## Task 1: Aggregate Desired Rules By NetNS

**Files:**
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/dnet/dnet_test.go`

- [ ] **Step 1: Write failing aggregation tests**

Append these tests to `pkg/dnet/dnet_test.go`:

```go
func TestDesiredPortForwardsForNetNSAggregatesAndDedupes(t *testing.T) {
	data := &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"active": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web", Port: 3000},
				},
			},
			"duplicate": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web-copy": {EndpointID: "web-copy", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web-copy", Port: 3000},
				},
			},
			"stale": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"old": {EndpointID: "old", IPv4: netip.MustParsePrefix("172.21.0.99/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3001": {EndpointID: "missing", Port: 3001},
				},
			},
			"other": {
				NetNS: "/var/run/netns/yeet-other-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"app": {EndpointID: "app", IPv4: netip.MustParsePrefix("172.22.0.2/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/8080": {EndpointID: "app", Port: 8080},
				},
			},
		},
	}

	got := desiredPortForwardsForNetNS(data, "/var/run/netns/yeet-demoapp-ns")
	want := []portForwardRule{
		{Proto: "tcp", HostPort: 3000, TargetIP: "172.21.0.4", TargetPort: 3000},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("desiredPortForwardsForNetNS mismatch (-want +got):\n%s", diff)
	}
}

func TestDesiredPortForwardsByNetNSGroupsDeterministically(t *testing.T) {
	data := &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"b": {
				NetNS: "/var/run/netns/yeet-b-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"app": {EndpointID: "app", IPv4: netip.MustParsePrefix("172.22.0.2/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/8080": {EndpointID: "app", Port: 8080},
				},
			},
			"a": {
				NetNS: "/var/run/netns/yeet-a-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"app": {EndpointID: "app", IPv4: netip.MustParsePrefix("172.21.0.2/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "app", Port: 3000},
				},
			},
			"empty": {
				NetNS: "/var/run/netns/yeet-empty-ns",
			},
		},
	}

	got := desiredPortForwardsByNetNS(data)
	want := map[string][]portForwardRule{
		"/var/run/netns/yeet-a-ns": {
			{Proto: "tcp", HostPort: 3000, TargetIP: "172.21.0.2", TargetPort: 3000},
		},
		"/var/run/netns/yeet-b-ns": {
			{Proto: "tcp", HostPort: 8080, TargetIP: "172.22.0.2", TargetPort: 8080},
		},
		"/var/run/netns/yeet-empty-ns": nil,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("desiredPortForwardsByNetNS mismatch (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./pkg/dnet -run 'TestDesiredPortForwards(ForNetNSAggregatesAndDedupes|ByNetNSGroupsDeterministically)' -count=1
```

Expected: fail with `undefined: desiredPortForwardsForNetNS` and `undefined: desiredPortForwardsByNetNS`.

- [ ] **Step 3: Add aggregate helpers**

In `pkg/dnet/dnet.go`, replace the inline sort inside `desiredPortForwards` with a helper and add these functions after `desiredPortForwards`:

```go
func sortPortForwardRules(rules []portForwardRule) {
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Proto != rules[j].Proto {
			return rules[i].Proto < rules[j].Proto
		}
		if rules[i].HostPort != rules[j].HostPort {
			return rules[i].HostPort < rules[j].HostPort
		}
		if rules[i].TargetIP != rules[j].TargetIP {
			return rules[i].TargetIP < rules[j].TargetIP
		}
		return rules[i].TargetPort < rules[j].TargetPort
	})
}

func dedupePortForwardRules(rules []portForwardRule) []portForwardRule {
	if len(rules) == 0 {
		return nil
	}
	sortPortForwardRules(rules)
	out := rules[:0]
	for _, rule := range rules {
		if len(out) > 0 && out[len(out)-1] == rule {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func desiredPortForwardsForNetNS(d *db.Data, netns string) []portForwardRule {
	if d == nil || netns == "" {
		return nil
	}
	var rules []portForwardRule
	for _, network := range d.DockerNetworks {
		if network == nil || network.NetNS != netns {
			continue
		}
		rules = append(rules, desiredPortForwards(network)...)
	}
	return dedupePortForwardRules(rules)
}

func desiredPortForwardsByNetNS(d *db.Data) map[string][]portForwardRule {
	out := map[string][]portForwardRule{}
	if d == nil {
		return out
	}
	for _, network := range d.DockerNetworks {
		if network == nil || network.NetNS == "" {
			continue
		}
		if _, ok := out[network.NetNS]; !ok {
			out[network.NetNS] = nil
		}
	}
	for netns := range out {
		out[netns] = desiredPortForwardsForNetNS(d, netns)
	}
	return out
}
```

Update the end of `desiredPortForwards` to call:

```go
	sortPortForwardRules(rules)
	return rules
```

- [ ] **Step 4: Run tests and verify they pass**

Run:

```bash
go test ./pkg/dnet -run 'TestDesiredPortForwards(ForNetNSAggregatesAndDedupes|ByNetNSGroupsDeterministically|SkipsStalePortOwners)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/dnet/dnet.go pkg/dnet/dnet_test.go
git commit -m "dnet: derive port forwards by netns"
```

## Task 2: Make Existing Callbacks Replay Aggregate Rules

**Files:**
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/dnet/dnet_test.go`

- [ ] **Step 1: Write failing callback replay test**

Add imports to `pkg/dnet/dnet_test.go`:

```go
import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)
```

Append this helper and test:

```go
type capturedPortForwardSync struct {
	netns string
	rules []portForwardRule
}

func newTestPlugin(t *testing.T, data *db.Data, syncs *[]capturedPortForwardSync) *plugin {
	t.Helper()
	root := t.TempDir()
	store := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	if data == nil {
		data = &db.Data{}
	}
	if err := store.Set(data); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	return &plugin{
		db: store,
		syncPortForwardsFunc: func(netns string, desired []portForwardRule) error {
			*syncs = append(*syncs, capturedPortForwardSync{
				netns: netns,
				rules: append([]portForwardRule(nil), desired...),
			})
			return nil
		},
	}
}

func postJSON(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCreateEndpointReplaysAggregateNetNSRules(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"active": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web", Port: 3000},
				},
			},
			"sidecar-network": {
				NetNS:     "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{},
				PortMap:   map[string]*db.EndpointPort{},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.CreateEndpoint, map[string]any{
		"NetworkID":  "sidecar-network",
		"EndpointID": "chrome",
		"Interface": map[string]any{
			"Address": "172.21.0.2/16",
		},
		"Options": map[string]any{
			"com.docker.network.portmap": []any{},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("CreateEndpoint status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(syncs) != 1 {
		t.Fatalf("sync count = %d, want 1", len(syncs))
	}
	want := capturedPortForwardSync{
		netns: "/var/run/netns/yeet-demoapp-ns",
		rules: []portForwardRule{
			{Proto: "tcp", HostPort: 3000, TargetIP: "172.21.0.4", TargetPort: 3000},
		},
	}
	if diff := cmp.Diff(want, syncs[0]); diff != "" {
		t.Fatalf("sync mismatch (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
go test ./pkg/dnet -run TestCreateEndpointReplaysAggregateNetNSRules -count=1
```

Expected: fail because `plugin.syncPortForwardsFunc` does not exist, then fail because existing code syncs only the callback network.

- [ ] **Step 3: Add test hook and aggregate sync helper**

Modify the `plugin` struct in `pkg/dnet/dnet.go`:

```go
type plugin struct {
	db *db.Store

	// netnsSema ensures that only one goroutine is running in a given network namespace at a time.
	netnsSema syncs.Map[string, *syncs.Semaphore]

	syncPortForwardsFunc func(netns string, desired []portForwardRule) error
}
```

Replace `syncPortForwards` with:

```go
func (p *plugin) syncPortForwards(netns string, desired []portForwardRule) error {
	if p.syncPortForwardsFunc != nil {
		return p.syncPortForwardsFunc(netns, desired)
	}
	return p.runInNetNS(netns, func() error {
		return syncNetNSPortForwards(netns, desired, iptablesBackend{})
	})
}
```

In `CreateEndpoint`, change:

```go
			desired = desiredPortForwards(n)
```

to:

```go
			desired = desiredPortForwardsForNetNS(d, netns)
```

In `DeleteEndpoint`, change:

```go
			desired = desiredPortForwards(n)
```

to:

```go
			desired = desiredPortForwardsForNetNS(d, netns)
```

In `LeaveNetwork`, change:

```go
			desired = desiredPortForwards(n)
```

to:

```go
			desired = desiredPortForwardsForNetNS(d, netns)
```

In `JoinNetwork`, change:

```go
	desired := desiredPortForwards(n)
```

to:

```go
	desired := desiredPortForwardsForNetNS(d, netns)
```

- [ ] **Step 4: Run focused tests**

Run:

```bash
go test ./pkg/dnet -run 'Test(CreateEndpointReplaysAggregateNetNSRules|DesiredPortForwards)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/dnet/dnet.go pkg/dnet/dnet_test.go
git commit -m "dnet: replay aggregate port forwards in callbacks"
```

## Task 3: Implement External Connectivity Callbacks

**Files:**
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/dnet/dnet_test.go`

- [ ] **Step 1: Write failing external connectivity tests**

Append these tests to `pkg/dnet/dnet_test.go`:

```go
func TestProgramExternalConnectivityUpdatesPortMapAndReplaysAggregateRules(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"demoapp": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.ProgramExternalConnectivity, map[string]any{
		"NetworkID":  "demoapp",
		"EndpointID": "web",
		"Options": map[string]any{
			"com.docker.network.portmap": []map[string]any{
				{"Proto": 6, "Port": 3000, "HostPort": 3000, "HostPortEnd": 3000},
			},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("ProgramExternalConnectivity status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(syncs) != 1 {
		t.Fatalf("sync count = %d, want 1", len(syncs))
	}
	wantRules := []portForwardRule{
		{Proto: "tcp", HostPort: 3000, TargetIP: "172.21.0.4", TargetPort: 3000},
	}
	if diff := cmp.Diff(wantRules, syncs[0].rules); diff != "" {
		t.Fatalf("rules mismatch (-want +got):\n%s", diff)
	}

	dv, err := p.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	got := dv.AsStruct().DockerNetworks["demoapp"].PortMap
	want := map[string]*db.EndpointPort{
		"6/3000": {EndpointID: "web", Port: 3000},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(db.EndpointPort{})); diff != "" {
		t.Fatalf("port map mismatch (-want +got):\n%s", diff)
	}
}

func TestRevokeExternalConnectivityRemovesPortMapAndReplaysAggregateRules(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"demoapp": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web", Port: 3000},
				},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.RevokeExternalConnectivity, map[string]any{
		"NetworkID":  "demoapp",
		"EndpointID": "web",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("RevokeExternalConnectivity status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(syncs) != 1 {
		t.Fatalf("sync count = %d, want 1", len(syncs))
	}
	if len(syncs[0].rules) != 0 {
		t.Fatalf("rules after revoke = %#v, want none", syncs[0].rules)
	}

	dv, err := p.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	if got := dv.AsStruct().DockerNetworks["demoapp"].PortMap; len(got) != 0 {
		t.Fatalf("port map after revoke = %#v, want empty", got)
	}
}

func TestRevokeExternalConnectivityUnknownEndpointIsIdempotent(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"demoapp": {
				NetNS:     "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{},
				PortMap:   map[string]*db.EndpointPort{},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.RevokeExternalConnectivity, map[string]any{
		"NetworkID":  "demoapp",
		"EndpointID": "missing",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("RevokeExternalConnectivity status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(syncs) != 1 {
		t.Fatalf("sync count = %d, want 1", len(syncs))
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./pkg/dnet -run 'Test(ProgramExternalConnectivity|RevokeExternalConnectivity)' -count=1
```

Expected: fail with undefined handler methods.

- [ ] **Step 3: Add shared portmap parsing and handler methods**

Add these helpers after the `portMap` type in `pkg/dnet/dnet.go`:

```go
func endpointPortMap(endpointID string, portMaps []portMap) (map[db.ProtoPort]*db.EndpointPort, error) {
	dbpm := make(map[db.ProtoPort]*db.EndpointPort)
	for _, pm := range portMaps {
		if pm.Proto != 6 && pm.Proto != 17 {
			return nil, fmt.Errorf("unsupported protocol")
		}
		dbpm[db.ProtoPort{Proto: pm.Proto, Port: pm.HostPort}] = &db.EndpointPort{
			EndpointID: endpointID,
			Port:       pm.Port,
		}
	}
	return dbpm, nil
}

func setEndpointPortMappings(n *db.DockerNetwork, endpointID string, mappings map[db.ProtoPort]*db.EndpointPort) {
	removeEndpointPortMappings(n, endpointID)
	for k, pm := range mappings {
		mak.Set(&n.PortMap, k.String(), pm)
	}
}
```

In `CreateEndpoint`, replace the existing manual protocol validation and `dbpm` creation with:

```go
		dbpm, err := endpointPortMap(req.EndpointID, req.Options.PortMap)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
```

Then replace:

```go
			removeEndpointPortMappings(n, req.EndpointID)
			for k, pm := range dbpm {
				mak.Set(&n.PortMap, k.String(), pm)
			}
```

with:

```go
			setEndpointPortMappings(n, req.EndpointID, dbpm)
```

Add these request and handler definitions before `PluginActivate`:

```go
type endpointConnectivityRequest struct {
	NetworkID  string `json:"NetworkID"`
	EndpointID string `json:"EndpointID"`
	Options    struct {
		PortMap []portMap `json:"com.docker.network.portmap"`
	} `json:"Options"`
}

func (p *plugin) ProgramExternalConnectivity(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req endpointConnectivityRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dbpm, err := endpointPortMap(req.EndpointID, req.Options.PortMap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var netns string
	var desired []portForwardRule
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		if _, ok := n.Endpoints[req.EndpointID]; !ok {
			return fmt.Errorf("endpoint not found")
		}
		netns = n.NetNS
		setEndpointPortMappings(n, req.EndpointID, dbpm)
		desired = desiredPortForwardsForNetNS(d, netns)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := p.syncPortForwards(netns, desired); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(SuccessResponse{})
}

func (p *plugin) RevokeExternalConnectivity(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req struct {
		NetworkID  string `json:"NetworkID"`
		EndpointID string `json:"EndpointID"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var netns string
	var desired []portForwardRule
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		removeEndpointPortMappings(n, req.EndpointID)
		desired = desiredPortForwardsForNetNS(d, netns)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := p.syncPortForwards(netns, desired); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(SuccessResponse{})
}
```

Update `New`:

```go
	mux.HandleFunc("/NetworkDriver.ProgramExternalConnectivity", p.ProgramExternalConnectivity)
	mux.HandleFunc("/NetworkDriver.RevokeExternalConnectivity", p.RevokeExternalConnectivity)
```

- [ ] **Step 4: Run focused tests**

Run:

```bash
go test ./pkg/dnet -run 'Test(ProgramExternalConnectivity|RevokeExternalConnectivity|CreateEndpointReplaysAggregateNetNSRules)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/dnet/dnet.go pkg/dnet/dnet_test.go
git commit -m "dnet: handle external connectivity callbacks"
```

## Task 4: Add Dnet Startup NAT Reconciliation

**Files:**
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/dnet/dnet_test.go`

- [ ] **Step 1: Write failing startup reconciliation test**

Append this test to `pkg/dnet/dnet_test.go`:

```go
func TestReconcilePortForwardsFromDataGroupsExistingNetNS(t *testing.T) {
	data := &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"demoapp": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web", Port: 3000},
				},
			},
			"missing": {
				NetNS: "/var/run/netns/yeet-missing-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"app": {EndpointID: "app", IPv4: netip.MustParsePrefix("172.22.0.2/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/8080": {EndpointID: "app", Port: 8080},
				},
			},
		},
	}

	exists := func(path string) (bool, error) {
		return path == "/var/run/netns/yeet-demoapp-ns", nil
	}
	var syncs []capturedPortForwardSync
	sync := func(netns string, desired []portForwardRule) error {
		syncs = append(syncs, capturedPortForwardSync{
			netns: netns,
			rules: append([]portForwardRule(nil), desired...),
		})
		return nil
	}

	if err := reconcilePortForwardsFromData(data, exists, sync); err != nil {
		t.Fatalf("reconcilePortForwardsFromData returned error: %v", err)
	}
	want := []capturedPortForwardSync{
		{
			netns: "/var/run/netns/yeet-demoapp-ns",
			rules: []portForwardRule{
				{Proto: "tcp", HostPort: 3000, TargetIP: "172.21.0.4", TargetPort: 3000},
			},
		},
	}
	if diff := cmp.Diff(want, syncs); diff != "" {
		t.Fatalf("syncs mismatch (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
go test ./pkg/dnet -run TestReconcilePortForwardsFromDataGroupsExistingNetNS -count=1
```

Expected: fail with `undefined: reconcilePortForwardsFromData`.

- [ ] **Step 3: Implement startup reconciliation**

Add `log` to the imports in `pkg/dnet/dnet.go`.

Add these helpers after `desiredPortForwardsByNetNS`:

```go
type netnsExistsFunc func(path string) (bool, error)
type netnsPortForwardSyncFunc func(netns string, desired []portForwardRule) error

func netnsPathExists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func reconcilePortForwardsFromData(d *db.Data, exists netnsExistsFunc, sync netnsPortForwardSyncFunc) error {
	byNetNS := desiredPortForwardsByNetNS(d)
	netnsPaths := make([]string, 0, len(byNetNS))
	for netns := range byNetNS {
		netnsPaths = append(netnsPaths, netns)
	}
	sort.Strings(netnsPaths)

	var errs []error
	for _, netns := range netnsPaths {
		ok, err := exists(netns)
		if err != nil {
			errs = append(errs, fmt.Errorf("check netns %q: %w", netns, err))
			continue
		}
		if !ok {
			log.Printf("skipping docker port forward reconciliation for missing netns %q", netns)
			continue
		}
		if err := sync(netns, byNetNS[netns]); err != nil {
			errs = append(errs, fmt.Errorf("reconcile port forwards for %q: %w", netns, err))
		}
	}
	return errors.Join(errs...)
}

func ReconcilePortForwards(store *db.Store) error {
	if store == nil {
		return fmt.Errorf("nil db store")
	}
	dv, err := store.Get()
	if err != nil {
		return err
	}
	p := &plugin{db: store}
	return reconcilePortForwardsFromData(dv.AsStruct(), netnsPathExists, p.syncPortForwards)
}
```

- [ ] **Step 4: Run dnet tests**

Run:

```bash
go test ./pkg/dnet -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/dnet/dnet.go pkg/dnet/dnet_test.go
git commit -m "dnet: add startup port forward reconciliation"
```

## Task 5: Wire NAT Reconciliation Into Catch Startup

**Files:**
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/netns_reconcile_test.go`

- [ ] **Step 1: Write/update startup tests**

In `pkg/catch/netns_reconcile_test.go`, update `TestServerStartRunsNetNSReconciliation` so the expected call order includes NAT reconciliation before service link reconciliation:

```go
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		calls = append(calls, "nat-reconcile")
		return nil
	}
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()
```

Change the expected diff in that test to:

```go
	if diff := cmp.Diff([]string{"install", "docker-prereqs", "nat-reconcile", "reconcile:docker-netns"}, calls); diff != "" {
		t.Fatalf("unexpected startup call order (-want +got):\n%s", diff)
	}
```

Append this test:

```go
func TestServerStartLogsNATReconciliationFailureNonFatally(t *testing.T) {
	s := newTestServer(t)
	logs := captureLogs(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		},
	})

	prevInstall := installYeetNSService
	installYeetNSService = func() error { return nil }
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })

	prevNAT := reconcileDockerNetNSPortForwards
	reconciledNAT := make(chan struct{})
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		close(reconciledNAT)
		return errors.New("nat exploded")
	}
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	reconciledLinks := make(chan struct{})
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(context.Context) (bool, error) {
				close(reconciledLinks)
				return false, nil
			},
		}, nil
	}

	s.Start()
	t.Cleanup(s.Shutdown)

	select {
	case <-reconciledNAT:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for NAT reconciliation to run")
	}
	select {
	case <-reconciledLinks:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for link reconciliation to run")
	}

	out := logs.String()
	if !strings.Contains(out, "docker netns NAT reconciliation failed: nat exploded") {
		t.Fatalf("missing NAT failure log:\n%s", out)
	}
}
```

For existing startup tests that stub `installYeetNSService` and `installDockerPrereqs`, add this neutral NAT stub if the test output becomes flaky or hits real dnet behavior:

```go
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error { return nil }
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./pkg/catch -run 'TestServerStart(RunsNetNSReconciliation|LogsNATReconciliationFailureNonFatally)' -count=1
```

Expected: fail with `undefined: reconcileDockerNetNSPortForwards`.

- [ ] **Step 3: Wire the startup hook**

Add `github.com/yeetrun/yeet/pkg/dnet` to `pkg/catch/catch.go` imports.

Add this package-level variable near `installYeetNSService` usage or before `Start`:

```go
var reconcileDockerNetNSPortForwards = dnet.ReconcilePortForwards
```

Update the reconciliation goroutine in `Start`:

```go
	s.waitGroup.Go(func() {
		if err := reconcileDockerNetNSPortForwards(s.cfg.DB); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("docker netns NAT reconciliation failed: %v", err)
		}
		if err := s.reconcileNetNSBackedDockerServices(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("netns reconciliation failed: %v", err)
		}
	})
```

- [ ] **Step 4: Run catch startup tests**

Run:

```bash
go test ./pkg/catch -run 'TestServerStart' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/catch/catch.go pkg/catch/netns_reconcile_test.go
git commit -m "catch: replay docker netns nat on startup"
```

## Task 6: Full Local Verification

**Files:**
- Modify only if tests expose required fixes in files touched by Tasks 1-5.

- [ ] **Step 1: Format Go files**

Run:

```bash
gofmt -w pkg/dnet/dnet.go pkg/dnet/dnet_test.go pkg/catch/catch.go pkg/catch/netns_reconcile_test.go
```

Expected: no output.

- [ ] **Step 2: Run targeted tests**

Run:

```bash
go test ./pkg/dnet ./pkg/catch ./pkg/svc -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full test suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Inspect git status**

Run:

```bash
git status --short
```

Expected: either clean, or only intended files from test-driven fixes.

- [ ] **Step 5: Confirm verification left no uncommitted changes**

Run:

```bash
git status --short
```

Expected: clean. If this is not clean, inspect the diff and either commit the intentional fix with an `area: summary` message or stop and report the unexpected files.

## Task 7: Deploy To Pve1 And Verify NAT Repair

**Files:**
- No repo file changes expected.

- [ ] **Step 1: Install updated catch on edge-a**

Run:

```bash
go run ./cmd/yeet --progress=plain init root@yeet-edge-a
```

Expected: command completes successfully and installs the updated catch binary.

- [ ] **Step 2: Confirm affected service status**

Run:

```bash
go run ./cmd/yeet --host yeet-edge-a status | rg '^(demoapp|media|indexer|backup)'
```

Expected: all four services show `running`.

- [ ] **Step 3: Capture demoapp container IDs and NAT rules before test**

Run:

```bash
ssh -o HostKeyAlias=edge-a -o UpdateHostKeys=no root@yeet-edge-a "docker ps --filter name=catch-demoapp --format '{{.Names}} {{.ID}} {{.Status}}'; echo __NAT__; ip netns exec yeet-demoapp-ns iptables -t nat -S YEET_PREROUTING; echo __OUTPUT__; ip netns exec yeet-demoapp-ns iptables -t nat -S YEET_OUTPUT"
```

Expected: demoapp containers are up, and NAT includes `--dport 3000`.

- [ ] **Step 4: Flush demoapp netns NAT chains**

Run:

```bash
ssh -o HostKeyAlias=edge-a -o UpdateHostKeys=no root@yeet-edge-a "ip netns exec yeet-demoapp-ns iptables -t nat -F YEET_PREROUTING; ip netns exec yeet-demoapp-ns iptables -t nat -F YEET_OUTPUT; ip netns exec yeet-demoapp-ns iptables -t nat -S YEET_PREROUTING; ip netns exec yeet-demoapp-ns iptables -t nat -S YEET_OUTPUT"
```

Expected: `YEET_PREROUTING` and `YEET_OUTPUT` have no demoapp `--dport 3000` DNAT rule.

- [ ] **Step 5: Restart catch only**

Run:

```bash
ssh -o HostKeyAlias=edge-a -o UpdateHostKeys=no root@yeet-edge-a "systemctl restart catch.service && sleep 3 && systemctl is-active catch.service"
```

Expected: `active`.

- [ ] **Step 6: Confirm NAT restored without demoapp recreation**

Run:

```bash
ssh -o HostKeyAlias=edge-a -o UpdateHostKeys=no root@yeet-edge-a "docker ps --filter name=catch-demoapp --format '{{.Names}} {{.ID}} {{.Status}}'; echo __NAT__; ip netns exec yeet-demoapp-ns iptables -t nat -S YEET_PREROUTING; echo __OUTPUT__; ip netns exec yeet-demoapp-ns iptables -t nat -S YEET_OUTPUT"
```

Expected:

- demoapp container IDs match Step 3
- NAT includes `--dport 3000 -j DNAT --to-destination 172.21.0.4:3000`

- [ ] **Step 7: Confirm public demoapp URL responds**

Run:

```bash
curl -k -sS -L -o /dev/null -w '%{http_code} %{url_effective}\n' https://demoapp.example.ts.net
```

Expected: `200 https://demoapp.example.ts.net/signin`.

- [ ] **Step 8: Confirm revoke route is implemented**

Run a controlled recreate of the demoapp web container:

```bash
ssh -o HostKeyAlias=edge-a -o UpdateHostKeys=no root@yeet-edge-a "since=\$(date --iso-8601=seconds); docker restart catch-demoapp-web-1 >/tmp/demoapp-restart.out; sleep 2; cat /tmp/demoapp-restart.out; journalctl -u catch -u docker --since \"\$since\" --no-pager | grep -F 'NetworkDriver.RevokeExternalConnectivity: Not implemented' || true"
```

Expected: restart prints `catch-demoapp-web-1`; grep prints nothing.

- [ ] **Step 9: Confirm live verification left no repo changes**

Run:

```bash
git status --short
```

Expected: clean. If files changed unexpectedly, inspect them and do not commit unrelated changes.
