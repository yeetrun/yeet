// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dnet

import (
	"net/netip"
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
