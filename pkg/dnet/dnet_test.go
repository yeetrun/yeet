// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dnet

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

type fakeNatRuleBackend struct {
	prerouting []string
	yeetOutput []string
	output     []string
}

func (f *fakeNatRuleBackend) ListChain(chain string) ([]string, error) {
	switch chain {
	case preroutingChainName:
		return append([]string(nil), f.prerouting...), nil
	case outputChainName:
		return append([]string(nil), f.yeetOutput...), nil
	case "OUTPUT":
		return append([]string(nil), f.output...), nil
	default:
		return nil, nil
	}
}

func (f *fakeNatRuleBackend) FlushChain(chain string) error {
	switch chain {
	case preroutingChainName:
		f.prerouting = nil
	case outputChainName:
		f.yeetOutput = nil
	}
	return nil
}

func (f *fakeNatRuleBackend) AppendRule(chain string, rule ...string) error {
	line := "-A " + chain + " " + strings.Join(rule, " ")
	switch chain {
	case preroutingChainName:
		f.prerouting = append(f.prerouting, line)
	case outputChainName:
		f.yeetOutput = append(f.yeetOutput, line)
	case "OUTPUT":
		f.output = append(f.output, line)
	}
	return nil
}

func (f *fakeNatRuleBackend) DeleteRule(chain string, rule ...string) error {
	line := "-A " + chain + " " + strings.Join(rule, " ")
	switch chain {
	case "OUTPUT":
		f.output = deleteRuleLine(f.output, line)
	case preroutingChainName:
		f.prerouting = deleteRuleLine(f.prerouting, line)
	case outputChainName:
		f.yeetOutput = deleteRuleLine(f.yeetOutput, line)
	}
	return nil
}

func (f *fakeNatRuleBackend) EnsureChains() error { return nil }

func deleteRuleLine(lines []string, target string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == target {
			continue
		}
		out = append(out, line)
	}
	return out
}

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
	want := []portForwardRule{
		{Proto: "tcp", HostPort: 80, TargetIP: "172.20.0.3", TargetPort: 80},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("desiredPortForwards mismatch (-want +got):\n%s", diff)
	}
}

func TestDesiredPortForwardsForNetNSAggregatesAndDedupes(t *testing.T) {
	data := &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"active": {
				NetNS: "/var/run/netns/yeet-hoarder-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web", Port: 3000},
				},
			},
			"duplicate": {
				NetNS: "/var/run/netns/yeet-hoarder-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web-copy": {EndpointID: "web-copy", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web-copy", Port: 3000},
				},
			},
			"stale": {
				NetNS: "/var/run/netns/yeet-hoarder-ns",
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

	got := desiredPortForwardsForNetNS(data, "/var/run/netns/yeet-hoarder-ns")
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
				NetNS: "/var/run/netns/yeet-hoarder-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/3000": {EndpointID: "web", Port: 3000},
				},
			},
			"sidecar-network": {
				NetNS:     "/var/run/netns/yeet-hoarder-ns",
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
		netns: "/var/run/netns/yeet-hoarder-ns",
		rules: []portForwardRule{
			{Proto: "tcp", HostPort: 3000, TargetIP: "172.21.0.4", TargetPort: 3000},
		},
	}
	if diff := cmp.Diff(want, syncs[0], cmp.AllowUnexported(capturedPortForwardSync{})); diff != "" {
		t.Fatalf("sync mismatch (-want +got):\n%s", diff)
	}
}

func TestRemoveEndpointPortMappings(t *testing.T) {
	network := &db.DockerNetwork{
		PortMap: map[string]*db.EndpointPort{
			"6/80": {EndpointID: "app", Port: 80},
			"6/81": {EndpointID: "backup", Port: 81},
		},
	}

	removeEndpointPortMappings(network, "app")

	if diff := cmp.Diff(map[string]*db.EndpointPort{
		"6/81": {EndpointID: "backup", Port: 81},
	}, network.PortMap, cmp.AllowUnexported(db.EndpointPort{})); diff != "" {
		t.Fatalf("removeEndpointPortMappings mismatch (-want +got):\n%s", diff)
	}
}

func TestSyncNetNSPortForwardsRemovesStaleRules(t *testing.T) {
	backend := &fakeNatRuleBackend{
		prerouting: []string{
			"-A YEET_PREROUTING -i br0 -j RETURN",
			"-A YEET_PREROUTING -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.2:80",
			"-A YEET_PREROUTING -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.3:80",
		},
		output: []string{
			"-A OUTPUT -p tcp -m tcp -o lo --dport 80 -j DNAT --to-destination 172.20.0.2:80",
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
		t.Fatalf("unexpected yeet output rules (-want +got):\n%s", diff)
	}

	if len(backend.output) != 0 {
		t.Fatalf("expected legacy direct OUTPUT rules to be removed, got %#v", backend.output)
	}
}
