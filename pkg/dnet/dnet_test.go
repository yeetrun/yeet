// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dnet

import (
	"bytes"
	"encoding/json"
	"errors"
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

type recordedCommand struct {
	name string
	args []string
}

func recordingRunner(commands *[]recordedCommand, missingChecks map[string]bool) commandRunner {
	return func(name string, args ...string) error {
		copied := append([]string(nil), args...)
		*commands = append(*commands, recordedCommand{name: name, args: copied})
		if missingChecks[name+" "+strings.Join(args, " ")] {
			return errors.New("missing")
		}
		return nil
	}
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

func TestReconcilePortForwardsFromDataGroupsExistingNetNS(t *testing.T) {
	data := &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"hoarder": {
				NetNS: "/var/run/netns/yeet-hoarder-ns",
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
		return path == "/var/run/netns/yeet-hoarder-ns", nil
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
			netns: "/var/run/netns/yeet-hoarder-ns",
			rules: []portForwardRule{
				{Proto: "tcp", HostPort: 3000, TargetIP: "172.21.0.4", TargetPort: 3000},
			},
		},
	}
	if diff := cmp.Diff(want, syncs, cmp.AllowUnexported(capturedPortForwardSync{})); diff != "" {
		t.Fatalf("syncs mismatch (-want +got):\n%s", diff)
	}
}

type capturedPortForwardSync struct {
	netns string
	rules []portForwardRule
}

func TestCreateNetworkStoresDockerNetwork(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{}, &syncs)

	rr := postJSON(t, p.CreateNetwork, map[string]any{
		"NetworkID": "vaultwarden",
		"Options": map[string]any{
			"com.docker.network.generic": map[string]any{
				"dev.catchit.netns": "/var/run/netns/yeet-vaultwarden-ns",
			},
		},
		"IPv4Data": []map[string]any{
			{
				"Gateway": "172.20.0.1/16",
				"Pool":    "172.20.0.0/16",
			},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("CreateNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}

	dv, err := p.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	got := dv.AsStruct().DockerNetworks["vaultwarden"]
	if got == nil {
		t.Fatal("docker network was not stored")
	}
	if got.NetworkID != "vaultwarden" {
		t.Fatalf("NetworkID = %q, want vaultwarden", got.NetworkID)
	}
	if got.NetNS != "/var/run/netns/yeet-vaultwarden-ns" {
		t.Fatalf("NetNS = %q, want /var/run/netns/yeet-vaultwarden-ns", got.NetNS)
	}
	if got.IPv4Gateway != netip.MustParsePrefix("172.20.0.1/16") {
		t.Fatalf("IPv4Gateway = %v, want 172.20.0.1/16", got.IPv4Gateway)
	}
	if got.IPv4Range != netip.MustParsePrefix("172.20.0.0/16") {
		t.Fatalf("IPv4Range = %v, want 172.20.0.0/16", got.IPv4Range)
	}
}

func TestCreateNetworkRejectsMissingNetNS(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{}, &syncs)

	rr := postJSON(t, p.CreateNetwork, map[string]any{
		"NetworkID": "vaultwarden",
		"IPv4Data": []map[string]any{
			{
				"Gateway": "172.20.0.1/16",
				"Pool":    "172.20.0.0/16",
			},
		},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("CreateNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "NetNS is required") {
		t.Fatalf("CreateNetwork body = %q, want NetNS is required", rr.Body.String())
	}
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

func TestProgramExternalConnectivityUpdatesPortMapAndReplaysAggregateRules(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"hoarder": {
				NetNS: "/var/run/netns/yeet-hoarder-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"web": {EndpointID: "web", IPv4: netip.MustParsePrefix("172.21.0.4/16")},
				},
				PortMap: map[string]*db.EndpointPort{},
			},
			"metrics": {
				NetNS: "/var/run/netns/yeet-hoarder-ns",
				Endpoints: map[string]*db.DockerEndpoint{
					"api": {EndpointID: "api", IPv4: netip.MustParsePrefix("172.21.0.5/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/9090": {EndpointID: "api", Port: 9090},
				},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.ProgramExternalConnectivity, map[string]any{
		"NetworkID":  "hoarder",
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
		{Proto: "tcp", HostPort: 9090, TargetIP: "172.21.0.5", TargetPort: 9090},
	}
	if diff := cmp.Diff(wantRules, syncs[0].rules); diff != "" {
		t.Fatalf("rules mismatch (-want +got):\n%s", diff)
	}

	dv, err := p.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	got := dv.AsStruct().DockerNetworks["hoarder"].PortMap
	want := map[string]*db.EndpointPort{
		"6/3000": {EndpointID: "web", Port: 3000},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(db.EndpointPort{})); diff != "" {
		t.Fatalf("port map mismatch (-want +got):\n%s", diff)
	}
}

func TestJoinNetworkUsesCommandAndNetNSBackends(t *testing.T) {
	root := t.TempDir()
	store := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	if err := store.Set(&db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:       "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID:   "vaultwarden",
				IPv4Gateway: netip.MustParsePrefix("172.20.0.1/16"),
				IPv4Range:   netip.MustParsePrefix("172.20.0.0/16"),
				Endpoints: map[string]*db.DockerEndpoint{
					"abcd1234": {EndpointID: "abcd1234", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/8080": {EndpointID: "abcd1234", Port: 80},
				},
			},
		},
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	var commands []recordedCommand
	backend := &fakeNatRuleBackend{}
	var enteredNetNS []string
	p := &plugin{
		db: store,
		runCommandFunc: recordingRunner(&commands, map[string]bool{
			"ip link show br0":                                   true,
			"iptables -t nat -L YEET_POSTROUTING":                true,
			"iptables -t nat -C POSTROUTING -j YEET_POSTROUTING": true,
			"iptables -t nat -C YEET_POSTROUTING -m addrtype ! --src-type LOCAL -o br0 -j RETURN": true,
			"iptables -t nat -C YEET_POSTROUTING -j MASQUERADE":                                   true,
		}),
		runInNetNSFunc: func(netns string, f func() error) error {
			enteredNetNS = append(enteredNetNS, netns)
			return f()
		},
		natBackendFunc: func() natRuleBackend { return backend },
	}

	rr := postJSON(t, p.JoinNetwork, map[string]any{
		"NetworkID":  "vaultwarden",
		"EndpointID": "abcd1234",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("JoinNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if diff := cmp.Diff([]string{"/var/run/netns/yeet-vaultwarden-ns"}, enteredNetNS); diff != "" {
		t.Fatalf("entered netns mismatch (-want +got):\n%s", diff)
	}

	var resp struct {
		InterfaceName map[string]string `json:"InterfaceName"`
		Gateway       string            `json:"Gateway"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"SrcName": "yv-abcdp", "DstPrefix": "eth"}, resp.InterfaceName); diff != "" {
		t.Fatalf("interface response mismatch (-want +got):\n%s", diff)
	}
	if resp.Gateway != "172.20.0.1" {
		t.Fatalf("gateway = %q, want 172.20.0.1", resp.Gateway)
	}

	wantCommandPrefix := []recordedCommand{
		{name: "ip", args: []string{"link", "add", "yv-abcd", "type", "veth", "peer", "name", "yv-abcdp"}},
		{name: "ip", args: []string{"link", "set", "yv-abcd", "netns", "/var/run/netns/yeet-vaultwarden-ns"}},
		{name: "ip", args: []string{"link", "show", "br0"}},
		{name: "ip", args: []string{"link", "add", "br0", "type", "bridge"}},
		{name: "ip", args: []string{"link", "set", "br0", "up"}},
		{name: "ip", args: []string{"addr", "add", "172.20.0.1/16", "dev", "br0"}},
		{name: "sysctl", args: []string{"-w", "net.ipv4.conf.br0.route_localnet=1"}},
		{name: "ip", args: []string{"link", "set", "yv-abcd", "master", "br0"}},
		{name: "ip", args: []string{"link", "set", "yv-abcd", "up"}},
	}
	if len(commands) < len(wantCommandPrefix) {
		t.Fatalf("command count = %d, want at least %d: %#v", len(commands), len(wantCommandPrefix), commands)
	}
	if diff := cmp.Diff(wantCommandPrefix, commands[:len(wantCommandPrefix)], cmp.AllowUnexported(recordedCommand{})); diff != "" {
		t.Fatalf("command prefix mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{
		"-A YEET_PREROUTING -i br0 -j RETURN",
		"-A YEET_PREROUTING -p tcp -m tcp --dport 8080 -j DNAT --to-destination 172.20.0.2:80",
	}, backend.prerouting); diff != "" {
		t.Fatalf("prerouting rules mismatch (-want +got):\n%s", diff)
	}
}

func TestLeaveNetworkDeletesEndpointAndSyncsPortForwards(t *testing.T) {
	root := t.TempDir()
	store := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	if err := store.Set(&db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
				Endpoints: map[string]*db.DockerEndpoint{
					"abcd1234": {EndpointID: "abcd1234", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
					"efgh5678": {EndpointID: "efgh5678", IPv4: netip.MustParsePrefix("172.20.0.3/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/8080": {EndpointID: "abcd1234", Port: 80},
					"6/9090": {EndpointID: "efgh5678", Port: 90},
				},
			},
		},
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	var commands []recordedCommand
	backend := &fakeNatRuleBackend{}
	var enteredNetNS []string
	p := &plugin{
		db:             store,
		runCommandFunc: recordingRunner(&commands, nil),
		runInNetNSFunc: func(netns string, f func() error) error {
			enteredNetNS = append(enteredNetNS, netns)
			return f()
		},
		natBackendFunc: func() natRuleBackend { return backend },
	}

	rr := postJSON(t, p.LeaveNetwork, map[string]any{
		"NetworkID":  "vaultwarden",
		"EndpointID": "abcd1234",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("LeaveNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if diff := cmp.Diff([]string{"/var/run/netns/yeet-vaultwarden-ns"}, enteredNetNS); diff != "" {
		t.Fatalf("entered netns mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]recordedCommand{
		{name: "ip", args: []string{"link", "del", "yv-abcd"}},
	}, commands, cmp.AllowUnexported(recordedCommand{})); diff != "" {
		t.Fatalf("commands mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{
		"-A YEET_PREROUTING -i br0 -j RETURN",
		"-A YEET_PREROUTING -p tcp -m tcp --dport 9090 -j DNAT --to-destination 172.20.0.3:90",
	}, backend.prerouting); diff != "" {
		t.Fatalf("prerouting rules mismatch (-want +got):\n%s", diff)
	}

	dv, err := p.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	network := dv.AsStruct().DockerNetworks["vaultwarden"]
	if _, ok := network.Endpoints["abcd1234"]; ok {
		t.Fatal("endpoint abcd1234 still exists after leave")
	}
	if _, ok := network.PortMap["6/8080"]; ok {
		t.Fatal("port map for abcd1234 still exists after leave")
	}
}

func TestEndpointPortMapRejectsPortRanges(t *testing.T) {
	_, err := endpointPortMap("web", []portMap{
		{Proto: 6, Port: 3000, HostPort: 3000, HostPortEnd: 3002},
	})
	if err == nil {
		t.Fatal("endpointPortMap returned nil error for port range")
	}
	if !strings.Contains(err.Error(), "unsupported port range") {
		t.Fatalf("endpointPortMap error = %q, want unsupported port range", err)
	}
}

func TestRevokeExternalConnectivityRouteRegistered(t *testing.T) {
	root := t.TempDir()
	store := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	if err := store.Set(&db.Data{}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	handler := New(store)

	raw, err := json.Marshal(map[string]any{
		"NetworkID":  "missing",
		"EndpointID": "web",
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/NetworkDriver.RevokeExternalConnectivity", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("RevokeExternalConnectivity route status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "network not found") {
		t.Fatalf("RevokeExternalConnectivity route body = %q, want network not found", rr.Body.String())
	}
}

func TestRevokeExternalConnectivityRemovesPortMapAndReplaysAggregateRules(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"hoarder": {
				NetNS: "/var/run/netns/yeet-hoarder-ns",
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
		"NetworkID":  "hoarder",
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
	if got := dv.AsStruct().DockerNetworks["hoarder"].PortMap; len(got) != 0 {
		t.Fatalf("port map after revoke = %#v, want empty", got)
	}
}

func TestRevokeExternalConnectivityUnknownEndpointIsIdempotent(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"hoarder": {
				NetNS:     "/var/run/netns/yeet-hoarder-ns",
				Endpoints: map[string]*db.DockerEndpoint{},
				PortMap:   map[string]*db.EndpointPort{},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.RevokeExternalConnectivity, map[string]any{
		"NetworkID":  "hoarder",
		"EndpointID": "missing",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("RevokeExternalConnectivity status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(syncs) != 1 {
		t.Fatalf("sync count = %d, want 1", len(syncs))
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

func TestEnsureBridgeSkipsCreateWhenBridgeExists(t *testing.T) {
	var commands []recordedCommand
	err := ensureBridgeWithRunner(netip.MustParsePrefix("172.20.0.1/16"), recordingRunner(&commands, nil))
	if err != nil {
		t.Fatalf("ensureBridgeWithRunner returned error: %v", err)
	}

	want := []recordedCommand{
		{name: "ip", args: []string{"link", "show", "br0"}},
	}
	if diff := cmp.Diff(want, commands, cmp.AllowUnexported(recordedCommand{})); diff != "" {
		t.Fatalf("commands mismatch (-want +got):\n%s", diff)
	}
}

func TestEnsureBridgeCreatesBridgeWhenMissing(t *testing.T) {
	var commands []recordedCommand
	err := ensureBridgeWithRunner(netip.MustParsePrefix("172.20.0.1/16"), recordingRunner(&commands, map[string]bool{
		"ip link show br0": true,
	}))
	if err != nil {
		t.Fatalf("ensureBridgeWithRunner returned error: %v", err)
	}

	want := []recordedCommand{
		{name: "ip", args: []string{"link", "show", "br0"}},
		{name: "ip", args: []string{"link", "add", "br0", "type", "bridge"}},
		{name: "ip", args: []string{"link", "set", "br0", "up"}},
		{name: "ip", args: []string{"addr", "add", "172.20.0.1/16", "dev", "br0"}},
		{name: "sysctl", args: []string{"-w", "net.ipv4.conf.br0.route_localnet=1"}},
	}
	if diff := cmp.Diff(want, commands, cmp.AllowUnexported(recordedCommand{})); diff != "" {
		t.Fatalf("commands mismatch (-want +got):\n%s", diff)
	}
}

func TestEnsurePostroutingChainAddsMissingRules(t *testing.T) {
	var commands []recordedCommand
	err := ensurePostroutingChainWithRunner(recordingRunner(&commands, map[string]bool{
		"iptables -t nat -L YEET_POSTROUTING":                                                 true,
		"iptables -t nat -C POSTROUTING -j YEET_POSTROUTING":                                  true,
		"iptables -t nat -C YEET_POSTROUTING -m addrtype ! --src-type LOCAL -o br0 -j RETURN": true,
		"iptables -t nat -C YEET_POSTROUTING -j MASQUERADE":                                   true,
	}))
	if err != nil {
		t.Fatalf("ensurePostroutingChainWithRunner returned error: %v", err)
	}

	want := []recordedCommand{
		{name: "iptables", args: []string{"-t", "nat", "-L", postroutingChainName}},
		{name: "iptables", args: []string{"-t", "nat", "-N", postroutingChainName}},
		{name: "iptables", args: []string{"-t", "nat", "-C", "POSTROUTING", "-j", postroutingChainName}},
		{name: "iptables", args: []string{"-t", "nat", "-A", "POSTROUTING", "-j", postroutingChainName}},
		{name: "iptables", args: []string{"-t", "nat", "-C", postroutingChainName, "-m", "addrtype", "!", "--src-type", "LOCAL", "-o", "br0", "-j", "RETURN"}},
		{name: "iptables", args: []string{"-t", "nat", "-I", postroutingChainName, "-m", "addrtype", "!", "--src-type", "LOCAL", "-o", "br0", "-j", "RETURN"}},
		{name: "iptables", args: []string{"-t", "nat", "-C", postroutingChainName, "-j", "MASQUERADE"}},
		{name: "iptables", args: []string{"-t", "nat", "-A", postroutingChainName, "-j", "MASQUERADE"}},
	}
	if diff := cmp.Diff(want, commands, cmp.AllowUnexported(recordedCommand{})); diff != "" {
		t.Fatalf("commands mismatch (-want +got):\n%s", diff)
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
