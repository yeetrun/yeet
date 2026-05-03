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
	"os"
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
	ensureErr  error
	listErr    map[string]error
	flushErr   map[string]error
	appendErr  map[string]error
	deleteErr  map[string]error
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
	if err := f.listErr[chain]; err != nil {
		return nil, err
	}
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
	if err := f.flushErr[chain]; err != nil {
		return err
	}
	switch chain {
	case preroutingChainName:
		f.prerouting = nil
	case outputChainName:
		f.yeetOutput = nil
	}
	return nil
}

func (f *fakeNatRuleBackend) AppendRule(chain string, rule ...string) error {
	if err := f.appendErr[chain]; err != nil {
		return err
	}
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
	if err := f.deleteErr[chain]; err != nil {
		return err
	}
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

func (f *fakeNatRuleBackend) EnsureChains() error { return f.ensureErr }

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

func TestDesiredPortForwardsFiltersInvalidMappings(t *testing.T) {
	if got := desiredPortForwards(nil); got != nil {
		t.Fatalf("desiredPortForwards(nil) = %#v, want nil", got)
	}

	network := &db.DockerNetwork{
		Endpoints: map[string]*db.DockerEndpoint{
			"app": {EndpointID: "app", IPv4: netip.MustParsePrefix("172.20.0.3/16")},
		},
		PortMap: map[string]*db.EndpointPort{
			"6/80":     nil,
			"17/53":    {EndpointID: "app", Port: 53},
			"1/443":    {EndpointID: "app", Port: 443},
			"not-port": {EndpointID: "app", Port: 1234},
		},
	}

	got := desiredPortForwards(network)
	want := []portForwardRule{
		{Proto: "udp", HostPort: 53, TargetIP: "172.20.0.3", TargetPort: 53},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("desiredPortForwards mismatch (-want +got):\n%s", diff)
	}
}

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
	if diff := cmp.Diff(want, syncs, cmp.AllowUnexported(capturedPortForwardSync{})); diff != "" {
		t.Fatalf("syncs mismatch (-want +got):\n%s", diff)
	}
}

func TestReconcilePortForwardsFromDataJoinsErrors(t *testing.T) {
	data := &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"check-error": {
				NetNS: "/var/run/netns/yeet-check-error-ns",
			},
			"sync-error": {
				NetNS: "/var/run/netns/yeet-sync-error-ns",
			},
		},
	}
	exists := func(path string) (bool, error) {
		if path == "/var/run/netns/yeet-check-error-ns" {
			return false, errors.New("stat failed")
		}
		return true, nil
	}
	sync := func(netns string, desired []portForwardRule) error {
		return errors.New("sync failed")
	}

	err := reconcilePortForwardsFromData(data, exists, sync)
	if err == nil {
		t.Fatal("reconcilePortForwardsFromData returned nil error")
	}
	if !strings.Contains(err.Error(), `check netns "/var/run/netns/yeet-check-error-ns"`) {
		t.Fatalf("error = %q, want check netns context", err)
	}
	if !strings.Contains(err.Error(), `reconcile port forwards for "/var/run/netns/yeet-sync-error-ns"`) {
		t.Fatalf("error = %q, want reconcile context", err)
	}
}

func TestReconcilePortForwardsRejectsNilStore(t *testing.T) {
	if err := ReconcilePortForwards(nil); err == nil {
		t.Fatal("ReconcilePortForwards(nil) returned nil error")
	}
}

func TestNetnsPathExists(t *testing.T) {
	if ok, err := netnsPathExists(""); err != nil || ok {
		t.Fatalf("netnsPathExists(empty) = %v, %v; want false, nil", ok, err)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if ok, err := netnsPathExists(missing); err != nil || ok {
		t.Fatalf("netnsPathExists(missing) = %v, %v; want false, nil", ok, err)
	}

	existing := filepath.Join(t.TempDir(), "netns")
	if err := os.WriteFile(existing, nil, 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	if ok, err := netnsPathExists(existing); err != nil || !ok {
		t.Fatalf("netnsPathExists(existing) = %v, %v; want true, nil", ok, err)
	}
}

func TestRunInNetNSMissingPathReturnsError(t *testing.T) {
	p := &plugin{}
	called := false
	err := p.runInNetNS(filepath.Join(t.TempDir(), "missing"), func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("runInNetNS returned nil error")
	}
	if called {
		t.Fatal("runInNetNS called function for missing namespace")
	}
	if !strings.Contains(err.Error(), "failed to run in netns") || !strings.Contains(err.Error(), "failed to open netns") {
		t.Fatalf("runInNetNS error = %q, want wrapped open error", err)
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

func TestCreateNetworkRejectsMissingIPv4Data(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{}, &syncs)

	rr := postJSON(t, p.CreateNetwork, map[string]any{
		"NetworkID": "vaultwarden",
		"Options": map[string]any{
			"com.docker.network.generic": map[string]any{
				"dev.catchit.netns": "/var/run/netns/yeet-vaultwarden-ns",
			},
		},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("CreateNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "IPv4Data is required") {
		t.Fatalf("CreateNetwork body = %q, want IPv4Data is required", rr.Body.String())
	}
}

func TestCreateNetworkRejectsDuplicateNetwork(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:       "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID:   "vaultwarden",
				IPv4Gateway: netip.MustParsePrefix("172.20.0.1/16"),
				IPv4Range:   netip.MustParsePrefix("172.20.0.0/16"),
			},
		},
	}, &syncs)

	rr := postJSON(t, p.CreateNetwork, map[string]any{
		"NetworkID": "vaultwarden",
		"Options": map[string]any{
			"com.docker.network.generic": map[string]any{
				"dev.catchit.netns": "/var/run/netns/yeet-vaultwarden-ns",
			},
		},
		"IPv4Data": []map[string]any{
			{"Gateway": "172.20.0.1/16", "Pool": "172.20.0.0/16"},
		},
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("CreateNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "network already exists") {
		t.Fatalf("CreateNetwork body = %q, want network already exists", rr.Body.String())
	}
}

func TestDecodePluginRequestRejectsInvalidJSON(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{}, &syncs)

	rr := postRaw(t, p.CreateNetwork, "{")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("CreateNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func newTestPlugin(t *testing.T, data *db.Data, syncs *[]capturedPortForwardSync) *plugin {
	t.Helper()
	store := newTestStore(t, data)
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

func newTestStore(t *testing.T, data *db.Data) *db.Store {
	t.Helper()
	root := t.TempDir()
	store := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	if data == nil {
		data = &db.Data{}
	}
	if err := store.Set(data); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	return store
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

func postRaw(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func installFakeIptables(t *testing.T, scriptBody string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "iptables.log")
	scriptPath := filepath.Join(dir, "iptables")
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> \"$IPTABLES_LOG\"\n" + scriptBody
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("os.WriteFile fake iptables: %v", err)
	}
	t.Setenv("IPTABLES_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readCommandLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("os.ReadFile command log: %v", err)
	}
	return splitNonEmptyLines(string(raw))
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
	if diff := cmp.Diff(want, syncs[0], cmp.AllowUnexported(capturedPortForwardSync{})); diff != "" {
		t.Fatalf("sync mismatch (-want +got):\n%s", diff)
	}
}

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
			"metrics": {
				NetNS: "/var/run/netns/yeet-demoapp-ns",
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
		{Proto: "tcp", HostPort: 9090, TargetIP: "172.21.0.5", TargetPort: 9090},
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

func TestJoinNetworkRejectsMissingNetworkAndEndpoint(t *testing.T) {
	store := newTestStore(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:       "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID:   "vaultwarden",
				IPv4Gateway: netip.MustParsePrefix("172.20.0.1/16"),
				Endpoints:   map[string]*db.DockerEndpoint{},
			},
		},
	})
	p := &plugin{db: store}

	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "missing network",
			body: map[string]any{"NetworkID": "missing", "EndpointID": "abcd1234"},
			want: "network not found",
		},
		{
			name: "missing endpoint",
			body: map[string]any{"NetworkID": "vaultwarden", "EndpointID": "abcd1234"},
			want: "endpoint not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := postJSON(t, p.JoinNetwork, tt.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("JoinNetwork status = %d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Fatalf("JoinNetwork body = %q, want %q", rr.Body.String(), tt.want)
			}
		})
	}
}

func TestJoinNetworkReturnsInternalErrorWhenCommandFails(t *testing.T) {
	store := newTestStore(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:       "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID:   "vaultwarden",
				IPv4Gateway: netip.MustParsePrefix("172.20.0.1/16"),
				Endpoints: map[string]*db.DockerEndpoint{
					"abcd1234": {EndpointID: "abcd1234", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
				},
			},
		},
	})
	p := &plugin{
		db: store,
		runCommandFunc: func(name string, args ...string) error {
			return errors.New("ip link add failed")
		},
		runInNetNSFunc: func(netns string, f func() error) error {
			t.Fatal("runInNetNSFunc should not be called when addJoinVeth fails")
			return nil
		},
	}

	rr := postJSON(t, p.JoinNetwork, map[string]any{
		"NetworkID":  "vaultwarden",
		"EndpointID": "abcd1234",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("JoinNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ip link add failed") {
		t.Fatalf("JoinNetwork body = %q, want command error", rr.Body.String())
	}
}

func TestJoinNetworkReturnsInternalErrorWhenNetNSEntryFails(t *testing.T) {
	store := newTestStore(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:       "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID:   "vaultwarden",
				IPv4Gateway: netip.MustParsePrefix("172.20.0.1/16"),
				Endpoints: map[string]*db.DockerEndpoint{
					"abcd1234": {EndpointID: "abcd1234", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
				},
			},
		},
	})
	var commands []recordedCommand
	p := &plugin{
		db:             store,
		runCommandFunc: recordingRunner(&commands, nil),
		runInNetNSFunc: func(netns string, f func() error) error {
			return errors.New("enter netns failed")
		},
	}

	rr := postJSON(t, p.JoinNetwork, map[string]any{
		"NetworkID":  "vaultwarden",
		"EndpointID": "abcd1234",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("JoinNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "enter netns failed") {
		t.Fatalf("JoinNetwork body = %q, want netns error", rr.Body.String())
	}
	want := []recordedCommand{
		{name: "ip", args: []string{"link", "add", "yv-abcd", "type", "veth", "peer", "name", "yv-abcdp"}},
		{name: "ip", args: []string{"link", "set", "yv-abcd", "netns", "/var/run/netns/yeet-vaultwarden-ns"}},
	}
	if diff := cmp.Diff(want, commands, cmp.AllowUnexported(recordedCommand{})); diff != "" {
		t.Fatalf("commands mismatch (-want +got):\n%s", diff)
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

func TestLeaveNetworkRejectsMissingNetworkAndEndpoint(t *testing.T) {
	store := newTestStore(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
				Endpoints: map[string]*db.DockerEndpoint{},
			},
		},
	})
	p := &plugin{db: store}

	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "missing network",
			body: map[string]any{"NetworkID": "missing", "EndpointID": "abcd1234"},
			want: "network not found",
		},
		{
			name: "missing endpoint",
			body: map[string]any{"NetworkID": "vaultwarden", "EndpointID": "abcd1234"},
			want: "endpoint not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := postJSON(t, p.LeaveNetwork, tt.body)
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("LeaveNetwork status = %d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Fatalf("LeaveNetwork body = %q, want %q", rr.Body.String(), tt.want)
			}
		})
	}
}

func TestLeaveNetworkReturnsInternalErrorWhenNetNSEntryFails(t *testing.T) {
	store := newTestStore(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
				Endpoints: map[string]*db.DockerEndpoint{
					"abcd1234": {EndpointID: "abcd1234", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/8080": {EndpointID: "abcd1234", Port: 80},
				},
			},
		},
	})
	p := &plugin{
		db:             store,
		runCommandFunc: recordingRunner(&[]recordedCommand{}, nil),
		runInNetNSFunc: func(netns string, f func() error) error {
			return errors.New("enter netns failed")
		},
	}

	rr := postJSON(t, p.LeaveNetwork, map[string]any{
		"NetworkID":  "vaultwarden",
		"EndpointID": "abcd1234",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("LeaveNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "enter netns failed") {
		t.Fatalf("LeaveNetwork body = %q, want netns error", rr.Body.String())
	}
}

func TestLeaveNetworkJoinsSyncAndCommandErrors(t *testing.T) {
	store := newTestStore(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
				Endpoints: map[string]*db.DockerEndpoint{
					"other": {EndpointID: "other", IPv4: netip.MustParsePrefix("172.20.0.3/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/9090": {EndpointID: "other", Port: 90},
				},
			},
		},
	})
	p := &plugin{
		db: store,
		runCommandFunc: func(name string, args ...string) error {
			return errors.New("ip link del failed")
		},
		runInNetNSFunc: func(netns string, f func() error) error {
			return f()
		},
		natBackendFunc: func() natRuleBackend {
			return &fakeNatRuleBackend{ensureErr: errors.New("ensure chains failed")}
		},
	}

	err := p.leaveNetwork(leaveNetworkState{
		netns:  "/var/run/netns/yeet-vaultwarden-ns",
		ifName: "yv-abcd",
	})
	if err == nil {
		t.Fatal("leaveNetwork returned nil error")
	}
	if !strings.Contains(err.Error(), "ensure chains failed") || !strings.Contains(err.Error(), "ip link del failed") {
		t.Fatalf("leaveNetwork error = %q, want sync and command errors", err)
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

func TestEndpointPortMapRejectsUnsupportedProtocol(t *testing.T) {
	_, err := endpointPortMap("web", []portMap{
		{Proto: 1, Port: 3000, HostPort: 3000},
	})
	if err == nil {
		t.Fatal("endpointPortMap returned nil error for unsupported protocol")
	}
	if !strings.Contains(err.Error(), "unsupported protocol") {
		t.Fatalf("endpointPortMap error = %q, want unsupported protocol", err)
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

func TestDeleteNetworkRemovesEmptyNetwork(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
			},
		},
	}, &syncs)

	rr := postJSON(t, p.DeleteNetwork, map[string]any{
		"NetworkID": "vaultwarden",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("DeleteNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	dv, err := p.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	if _, ok := dv.AsStruct().DockerNetworks["vaultwarden"]; ok {
		t.Fatal("network still exists after delete")
	}
}

func TestDeleteNetworkRejectsNetworkWithEndpoints(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
				Endpoints: map[string]*db.DockerEndpoint{
					"app": {EndpointID: "app", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
				},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.DeleteNetwork, map[string]any{
		"NetworkID": "vaultwarden",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("DeleteNetwork status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "network still has endpoints") {
		t.Fatalf("DeleteNetwork body = %q, want endpoint error", rr.Body.String())
	}
}

func TestDeleteEndpointRemovesEndpointAndSyncsPortForwards(t *testing.T) {
	var syncs []capturedPortForwardSync
	p := newTestPlugin(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
				Endpoints: map[string]*db.DockerEndpoint{
					"app":   {EndpointID: "app", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
					"other": {EndpointID: "other", IPv4: netip.MustParsePrefix("172.20.0.3/16")},
				},
				PortMap: map[string]*db.EndpointPort{
					"6/8080": {EndpointID: "app", Port: 80},
					"6/9090": {EndpointID: "other", Port: 90},
				},
			},
		},
	}, &syncs)

	rr := postJSON(t, p.DeleteEndpoint, map[string]any{
		"NetworkID":  "vaultwarden",
		"EndpointID": "app",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("DeleteEndpoint status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(syncs) != 1 {
		t.Fatalf("sync count = %d, want 1", len(syncs))
	}
	wantRules := []portForwardRule{
		{Proto: "tcp", HostPort: 9090, TargetIP: "172.20.0.3", TargetPort: 90},
	}
	if diff := cmp.Diff(wantRules, syncs[0].rules); diff != "" {
		t.Fatalf("sync rules mismatch (-want +got):\n%s", diff)
	}
	dv, err := p.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	network := dv.AsStruct().DockerNetworks["vaultwarden"]
	if _, ok := network.Endpoints["app"]; ok {
		t.Fatal("endpoint still exists after delete")
	}
	if _, ok := network.PortMap["6/8080"]; ok {
		t.Fatal("port map still exists after delete")
	}
}

func TestDeleteEndpointReturnsSyncError(t *testing.T) {
	store := newTestStore(t, &db.Data{
		DockerNetworks: map[string]*db.DockerNetwork{
			"vaultwarden": {
				NetNS:     "/var/run/netns/yeet-vaultwarden-ns",
				NetworkID: "vaultwarden",
				Endpoints: map[string]*db.DockerEndpoint{
					"app": {EndpointID: "app", IPv4: netip.MustParsePrefix("172.20.0.2/16")},
				},
			},
		},
	})
	p := &plugin{
		db: store,
		syncPortForwardsFunc: func(netns string, desired []portForwardRule) error {
			return errors.New("sync failed")
		},
	}

	rr := postJSON(t, p.DeleteEndpoint, map[string]any{
		"NetworkID":  "vaultwarden",
		"EndpointID": "app",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("DeleteEndpoint status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "sync failed") {
		t.Fatalf("DeleteEndpoint body = %q, want sync error", rr.Body.String())
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

func TestWithCommandDefaultsAddsIptablesWait(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
		want []string
	}{
		{
			name: "non iptables unchanged",
			cmd:  "ip",
			args: []string{"link", "show"},
			want: []string{"link", "show"},
		},
		{
			name: "iptables adds wait",
			cmd:  "iptables",
			args: []string{"-t", "nat", "-L"},
			want: []string{"-w", "-t", "nat", "-L"},
		},
		{
			name: "iptables keeps short wait",
			cmd:  "iptables",
			args: []string{"-w", "-t", "nat", "-L"},
			want: []string{"-w", "-t", "nat", "-L"},
		},
		{
			name: "iptables keeps long wait",
			cmd:  "iptables",
			args: []string{"--wait", "-t", "nat", "-L"},
			want: []string{"--wait", "-t", "nat", "-L"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withCommandDefaults(tt.cmd, tt.args)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("withCommandDefaults mismatch (-want +got):\n%s", diff)
			}
		})
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

func TestIptablesBackendCommandPlanningUsesFakeBinary(t *testing.T) {
	logPath := installFakeIptables(t, `
if [ "$*" = "-w -t nat -S YEET_PREROUTING" ]; then
  printf '%s\n\n' "-A YEET_PREROUTING -i br0 -j RETURN"
  exit 0
fi
case "$*" in
  "-w -t nat -L YEET_PREROUTING"|"-w -t nat -L YEET_OUTPUT"|"-w -t nat -C PREROUTING -j YEET_PREROUTING"|"-w -t nat -C OUTPUT -o lo -j YEET_OUTPUT")
    exit 1
    ;;
esac
exit 0
`)
	backend := iptablesBackend{}

	rules, err := backend.ListChain(preroutingChainName)
	if err != nil {
		t.Fatalf("ListChain: %v", err)
	}
	if diff := cmp.Diff([]string{"-A YEET_PREROUTING -i br0 -j RETURN"}, rules); diff != "" {
		t.Fatalf("ListChain rules mismatch (-want +got):\n%s", diff)
	}
	if err := backend.FlushChain(preroutingChainName); err != nil {
		t.Fatalf("FlushChain: %v", err)
	}
	if err := backend.AppendRule(preroutingChainName, "-i", "br0", "-j", "RETURN"); err != nil {
		t.Fatalf("AppendRule: %v", err)
	}
	if err := backend.DeleteRule("OUTPUT", "-o", "lo", "-j", "DNAT"); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if err := backend.EnsureChains(); err != nil {
		t.Fatalf("EnsureChains: %v", err)
	}

	wantCommands := []string{
		"-w -t nat -S YEET_PREROUTING",
		"-w -t nat -F YEET_PREROUTING",
		"-w -t nat -A YEET_PREROUTING -i br0 -j RETURN",
		"-w -t nat -D OUTPUT -o lo -j DNAT",
		"-w -t nat -L YEET_PREROUTING",
		"-w -t nat -N YEET_PREROUTING",
		"-w -t nat -C PREROUTING -j YEET_PREROUTING",
		"-w -t nat -A PREROUTING -j YEET_PREROUTING",
		"-w -t nat -L YEET_OUTPUT",
		"-w -t nat -N YEET_OUTPUT",
		"-w -t nat -C OUTPUT -o lo -j YEET_OUTPUT",
		"-w -t nat -A OUTPUT -o lo -j YEET_OUTPUT",
	}
	if diff := cmp.Diff(wantCommands, readCommandLog(t, logPath)); diff != "" {
		t.Fatalf("iptables commands mismatch (-want +got):\n%s", diff)
	}
}

func TestIptablesBackendListChainWrapsCommandError(t *testing.T) {
	installFakeIptables(t, "exit 2\n")

	_, err := iptablesBackend{}.ListChain("MISSING")
	if err == nil {
		t.Fatal("ListChain returned nil error")
	}
	if !strings.Contains(err.Error(), `list chain "MISSING"`) {
		t.Fatalf("ListChain error = %q, want chain context", err)
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

func TestSyncNetNSPortForwardsPropagatesBackendErrors(t *testing.T) {
	desired := []portForwardRule{
		{Proto: "tcp", HostPort: 80, TargetIP: "172.20.0.2", TargetPort: 80},
	}
	tests := []struct {
		name    string
		backend *fakeNatRuleBackend
		want    string
	}{
		{
			name:    "ensure chains",
			backend: &fakeNatRuleBackend{ensureErr: errors.New("ensure failed")},
			want:    "ensure yeet nat chains",
		},
		{
			name:    "list legacy output",
			backend: &fakeNatRuleBackend{listErr: map[string]error{"OUTPUT": errors.New("list failed")}},
			want:    "list output rules",
		},
		{
			name: "delete legacy output",
			backend: &fakeNatRuleBackend{
				output: []string{
					"-A OUTPUT -o lo -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.20.0.2:80",
				},
				deleteErr: map[string]error{"OUTPUT": errors.New("delete failed")},
			},
			want: "delete legacy output rule",
		},
		{
			name:    "flush prerouting",
			backend: &fakeNatRuleBackend{flushErr: map[string]error{preroutingChainName: errors.New("flush failed")}},
			want:    "flush prerouting chain",
		},
		{
			name:    "flush output",
			backend: &fakeNatRuleBackend{flushErr: map[string]error{outputChainName: errors.New("flush failed")}},
			want:    "flush output chain",
		},
		{
			name:    "append bridge guard",
			backend: &fakeNatRuleBackend{appendErr: map[string]error{preroutingChainName: errors.New("append failed")}},
			want:    "append bridge guard",
		},
		{
			name:    "append output rule",
			backend: &fakeNatRuleBackend{appendErr: map[string]error{outputChainName: errors.New("append failed")}},
			want:    "append output rule",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := syncNetNSPortForwards("yeet-vaultwarden-ns", desired, tt.backend)
			if err == nil {
				t.Fatal("syncNetNSPortForwards returned nil error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("syncNetNSPortForwards error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestRuleParsingHelpers(t *testing.T) {
	if isLegacyDirectOutputRule("-A OUTPUT -o eth0 -j DNAT") {
		t.Fatal("eth0 output rule detected as legacy loopback rule")
	}
	if isLegacyDirectOutputRule("-A INPUT -o lo -j DNAT") {
		t.Fatal("non-OUTPUT rule detected as legacy output rule")
	}

	got, err := chainRuleArgs("-A OUTPUT -o lo -j DNAT", "OUTPUT")
	if err != nil {
		t.Fatalf("chainRuleArgs: %v", err)
	}
	if diff := cmp.Diff([]string{"-o", "lo", "-j", "DNAT"}, got); diff != "" {
		t.Fatalf("chainRuleArgs mismatch (-want +got):\n%s", diff)
	}
	if _, err := chainRuleArgs("-A PREROUTING -j DNAT", "OUTPUT"); err == nil {
		t.Fatal("chainRuleArgs returned nil error for wrong chain")
	}
}

func TestNewRoutesHandleBasicPluginEndpoints(t *testing.T) {
	handler := New(newTestStore(t, &db.Data{}))

	tests := []struct {
		path   string
		status int
		want   string
	}{
		{path: "/Plugin.Activate", status: http.StatusOK, want: `"NetworkDriver"`},
		{path: "/NetworkDriver.GetCapabilities", status: http.StatusOK, want: `"Scope":"local"`},
		{path: "/NetworkDriver.EndpointOperInfo", status: http.StatusOK, want: `"Err":""`},
		{path: "/not-implemented", status: http.StatusNotImplemented, want: "Not implemented"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader("{}"))
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tt.status {
				t.Fatalf("%s status = %d body=%s", tt.path, rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Fatalf("%s body = %q, want %q", tt.path, rr.Body.String(), tt.want)
			}
		})
	}
}

type errorReadCloser struct{}

func (errorReadCloser) Read(p []byte) (int, error) {
	return 0, errors.New("read failed")
}

func (errorReadCloser) Close() error { return nil }

func TestRequestLoggerHandlesReadError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = errorReadCloser{}

	if got := requestLogger(req); got != nil {
		t.Fatalf("requestLogger = %#v, want nil", got)
	}
}
