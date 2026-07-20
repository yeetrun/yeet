// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRenderISOPolicyOrdersSecurityDecisions(t *testing.T) {
	spec := testISOPolicySpec()
	wantDecisions := []string{
		"drop-invalid", "drop-spoof", "accept-established", "accept-iso-dns",
		"reject-new-host-access", "reject-direct-dns", "reject-non-public",
		"accept-public", "reject-rest", "masquerade-public", "drop-ipv6",
	}
	for _, backend := range []FirewallBackend{BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy} {
		backend := backend
		t.Run(string(backend), func(t *testing.T) {
			rules, err := RenderISOPolicy(backend, spec)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(wantDecisions, rules.Decisions); diff != "" {
				t.Fatalf("decision order (-want +got):\n%s", diff)
			}
			assertContainsAll(t, rules.IPv4,
				"172.30.128.0/27", "100.64.0.0/10", "169.254.0.0/16", "198.18.0.0/15",
				"5353", "53", "853", "MASQUERADE", "yi-a1b2c3",
			)
			if !strings.Contains(strings.ToLower(rules.IPv6), "drop") {
				t.Fatalf("IPv6 policy has no drop:\n%s", rules.IPv6)
			}
			if rules.Digest == "" {
				t.Fatal("policy digest is empty")
			}
			if backend != BackendNFT {
				assertContainsAll(t, rules.IPSet, "hash:net,iface", "yi-a1b2c3", "172.30.0.2/32")
			}
		})
	}
}

func TestRenderISOPolicyBackendsContainConcreteSecurityRulesInOrder(t *testing.T) {
	for _, backend := range []FirewallBackend{BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy} {
		backend := backend
		t.Run(string(backend), func(t *testing.T) {
			rules, err := RenderISOPolicy(backend, testISOPolicySpec())
			if err != nil {
				t.Fatal(err)
			}
			if backend == BackendNFT {
				assertOrderedPolicyText(t, rules.IPv4,
					`iifname @interfaces ct state invalid drop`,
					`iifname "yi-a1b2c3" ip saddr != { 172.30.0.2/32, 172.30.128.0/27 } drop`,
					`iifname @interfaces ct state established,related accept`,
					`iifname @interfaces udp dport 5353 ct original proto-dst 53 accept`,
					`iifname @interfaces reject with icmp type admin-prohibited`,
					`iifname @interfaces tcp dport { 53, 853 } reject with icmp type admin-prohibited`,
					`iifname @interfaces ip daddr @non_public reject with icmp type admin-prohibited`,
					`iifname @interfaces ip daddr != @non_public accept`,
					`oifname @interfaces ct state established,related accept`,
					`oifname @interfaces reject with icmp type admin-prohibited`,
					`ip saddr 172.30.0.0/16 ip daddr != @non_public masquerade`,
				)
				assertContainsAll(t, rules.IPv6, "iifname @interfaces drop", "oifname @interfaces drop")
			} else {
				assertOrderedPolicyText(t, rules.IPv4,
					"-A YEET_ISO_MANGLE -i yi-a1b2c3 -m conntrack --ctstate INVALID -j DROP",
					"-A YEET_ISO_MANGLE -i yi-a1b2c3 -m set --match-set yeet_iso_sources src,src -j RETURN",
					"-A YEET_ISO_MANGLE -i yi-a1b2c3 -j DROP",
					"-A YEET_ISO_INPUT -i yi-a1b2c3 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
					"-A YEET_ISO_INPUT -i yi-a1b2c3 -p udp --dport 5353 -m conntrack --ctorigdstport 53 -j ACCEPT",
					"-A YEET_ISO_INPUT -i yi-a1b2c3 -j REJECT --reject-with icmp-admin-prohibited",
					"-A YEET_ISO_FORWARD -i yi-a1b2c3 -p tcp -m multiport --dports 53,853 -j REJECT",
					"-A YEET_ISO_FORWARD -i yi-a1b2c3 -m set --match-set yeet_iso_nonpublic dst -j REJECT",
					"-A YEET_ISO_FORWARD -i yi-a1b2c3 -m set ! --match-set yeet_iso_nonpublic dst -j ACCEPT",
					"-A YEET_ISO_FORWARD -o yi-a1b2c3 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
					"-A YEET_ISO_FORWARD -o yi-a1b2c3 -j REJECT",
				)
				assertContainsAll(t, rules.IPv4, "-A YEET_ISO_POSTROUTE -s 172.30.0.0/16 -m set ! --match-set yeet_iso_nonpublic dst -j MASQUERADE")
				assertContainsAll(t, rules.IPv6,
					"-A YEET_ISO_INPUT -i yi-a1b2c3 -j DROP",
					"-A YEET_ISO_FORWARD -i yi-a1b2c3 -j DROP",
					"-A YEET_ISO_FORWARD -o yi-a1b2c3 -j DROP",
				)
				assertContainsAll(t, rules.IPSet,
					"add yeet_iso_sources 172.30.0.2/32,yi-a1b2c3",
					"add yeet_iso_sources 172.30.128.0/27,yi-a1b2c3",
					"add yeet_iso_nonpublic 100.64.0.0/10",
				)
			}
			if strings.Contains(rules.IPv4, "100.64.0.0/10 -j ACCEPT") || strings.Contains(rules.IPv4, "100.64.0.0/10 accept") {
				t.Fatalf("%s added a CGNAT allow exception:\n%s", backend, rules.IPv4)
			}
		})
	}
}

func assertOrderedPolicyText(t *testing.T, text string, fragments ...string) {
	t.Helper()
	position := -1
	for _, fragment := range fragments {
		next := strings.Index(text[position+1:], fragment)
		if next < 0 {
			t.Fatalf("policy missing ordered fragment %q after byte %d:\n%s", fragment, position, text)
		}
		position += next + 1
	}
}

func TestRenderISOPolicyRejectsIncompleteOrDuplicateEndpoints(t *testing.T) {
	base := testISOPolicySpec()
	tests := []struct {
		name string
		edit func(*ISOPolicySpec)
	}{
		{name: "pool", edit: func(s *ISOPolicySpec) { s.Pool = netip.MustParsePrefix("172.30.0.0/24") }},
		{name: "dns", edit: func(s *ISOPolicySpec) { s.DNSPort = 0 }},
		{name: "interface", edit: func(s *ISOPolicySpec) { s.Endpoints[0].Interface = "" }},
		{name: "peer", edit: func(s *ISOPolicySpec) { s.Endpoints[0].PeerIP = netip.Addr{} }},
		{name: "duplicate", edit: func(s *ISOPolicySpec) { s.Endpoints = append(s.Endpoints, s.Endpoints[0]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := base
			spec.Endpoints = append([]ISOEndpoint(nil), base.Endpoints...)
			tt.edit(&spec)
			if _, err := RenderISOPolicy(BackendNFT, spec); err == nil {
				t.Fatal("RenderISOPolicy returned nil error")
			}
		})
	}
}

func TestRenderISOPolicyRejectsEndpointsOutsideAllocatedLayoutHalves(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*ISOEndpoint)
	}{
		{name: "link in project half", edit: func(e *ISOEndpoint) {
			e.Link = netip.MustParsePrefix("172.30.128.0/30")
			e.PeerIP = netip.MustParseAddr("172.30.128.2")
		}},
		{name: "peer is not link host two", edit: func(e *ISOEndpoint) { e.PeerIP = netip.MustParseAddr("172.30.0.1") }},
		{name: "project in link half", edit: func(e *ISOEndpoint) { e.Project = netip.MustParsePrefix("172.30.64.0/27") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := testISOPolicySpec()
			tt.edit(&spec.Endpoints[0])
			if _, err := RenderISOPolicy(BackendNFT, spec); err == nil {
				t.Fatal("RenderISOPolicy returned nil error")
			}
		})
	}
}

func TestRenderISOPolicyRejectsDuplicateNetworkIdentitiesForEveryBackend(t *testing.T) {
	second := ISOEndpoint{
		Interface: "yi-d4e5f6",
		Link:      netip.MustParsePrefix("172.30.0.4/30"),
		PeerIP:    netip.MustParseAddr("172.30.0.6"),
		Project:   netip.MustParsePrefix("172.30.128.32/27"),
	}
	for _, tt := range []struct {
		name string
		edit func(*ISOEndpoint, ISOEndpoint)
	}{
		{name: "link and peer", edit: func(endpoint *ISOEndpoint, first ISOEndpoint) {
			endpoint.Link = first.Link
			endpoint.PeerIP = first.PeerIP
		}},
		{name: "project", edit: func(endpoint *ISOEndpoint, first ISOEndpoint) {
			endpoint.Project = first.Project
		}},
	} {
		for _, backend := range []FirewallBackend{BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy} {
			t.Run(tt.name+"/"+string(backend), func(t *testing.T) {
				spec := testISOPolicySpec()
				duplicate := second
				tt.edit(&duplicate, spec.Endpoints[0])
				spec.Endpoints = append(spec.Endpoints, duplicate)
				if _, err := RenderISOPolicy(backend, spec); err == nil {
					t.Fatal("RenderISOPolicy returned nil error for duplicate network identity")
				}
			})
		}
	}
}

func TestRenderISOPolicyAllowsDistinctVMEndpointsWithoutProjects(t *testing.T) {
	spec := testISOPolicySpec()
	spec.Endpoints[0].Project = netip.Prefix{}
	spec.Endpoints = append(spec.Endpoints, ISOEndpoint{
		Interface: "yi-d4e5f6",
		Link:      netip.MustParsePrefix("172.30.0.4/30"),
		PeerIP:    netip.MustParseAddr("172.30.0.6"),
	})
	for _, backend := range []FirewallBackend{BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy} {
		if _, err := RenderISOPolicy(backend, spec); err != nil {
			t.Fatalf("RenderISOPolicy(%s) rejected reduced VM endpoints: %v", backend, err)
		}
	}
}

func TestEnsureISOPolicyFailsClosedAndVerifiesLiveDigest(t *testing.T) {
	rules, err := RenderISOPolicy(BackendNFT, testISOPolicySpec())
	if err != nil {
		t.Fatal(err)
	}
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })

	var calls []string
	runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		if name != "nft" {
			t.Fatalf("unexpected command %s", name)
		}
		if len(input) != 0 {
			return nil, nil
		}
		if strings.Contains(strings.Join(args, " "), "table ip6") {
			return []byte(rules.IPv6), nil
		}
		return []byte(rules.IPv4), nil
	}
	if err := EnsureISOPolicy(context.Background(), rules); err != nil {
		t.Fatalf("EnsureISOPolicy returned error: %v", err)
	}
	if len(calls) != 6 {
		t.Fatalf("commands = %v, want two existence probes, two atomic applies, and two live reads", calls)
	}

	runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
		if len(input) != 0 {
			return nil, nil
		}
		return []byte("table drifted {}"), nil
	}
	err = EnsureISOPolicy(context.Background(), rules)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("drift error = %v, want digest mismatch", err)
	}

	want := errors.New("restore failed")
	runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
		return nil, want
	}
	if err := EnsureISOPolicy(context.Background(), rules); !errors.Is(err, want) {
		t.Fatalf("restore error = %v, want %v", err, want)
	}
}

func TestEnsureISOPolicyNFTAtomicallyReplacesOwnedTablesOnRepeatedApply(t *testing.T) {
	rules, err := RenderISOPolicy(BackendNFT, testISOPolicySpec())
	if err != nil {
		t.Fatal(err)
	}
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })

	present := map[string]bool{}
	var mutations []string
	runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
		if name != "nft" {
			return nil, errors.New("unexpected command " + name)
		}
		if len(input) == 0 {
			family := args[len(args)-2]
			if !present[family] {
				return nil, errors.New("No such file or directory")
			}
			if family == "ip6" {
				return []byte(rules.IPv6), nil
			}
			return []byte(rules.IPv4), nil
		}

		script := string(input)
		mutations = append(mutations, script)
		if strings.Contains(script, "flush ruleset") || strings.Contains(script, "flush table") {
			return nil, errors.New("ISO apply flushed firewall state")
		}
		family := "ip"
		if strings.Contains(script, "table ip6 "+isoNFTTable) {
			family = "ip6"
		}
		deleting := strings.Contains(script, "delete table "+family+" "+isoNFTTable)
		if deleting != present[family] {
			return nil, errors.New("owned-table replacement did not match live state")
		}
		if deleting {
			present[family] = false
		}
		declaration := "table " + family + " " + isoNFTTable
		if !strings.Contains(script, declaration) || present[family] {
			return nil, errors.New("owned table was not declared exactly once from an absent state")
		}
		present[family] = true
		return nil, nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := EnsureISOPolicy(context.Background(), rules); err != nil {
			t.Fatalf("EnsureISOPolicy attempt %d: %v", attempt+1, err)
		}
	}
	if got, want := len(mutations), 4; got != want {
		t.Fatalf("mutation count = %d, want %d", got, want)
	}
	if strings.Contains(mutations[0], "delete table") || strings.Contains(mutations[1], "delete table") {
		t.Fatalf("first apply deleted an absent owned table: %#v", mutations[:2])
	}
	if !strings.Contains(mutations[2], "delete table ip "+isoNFTTable) ||
		!strings.Contains(mutations[3], "delete table ip6 "+isoNFTTable) {
		t.Fatalf("second apply did not replace both owned tables: %#v", mutations[2:])
	}
}

func TestCanonicalNFTTextNormalizesNamedPriorityExpressions(t *testing.T) {
	numeric := "chain c { type filter hook prerouting priority -160; }\n" +
		"chain d { type nat hook prerouting priority -110; }\n" +
		"chain e { type filter hook input priority -10; }\n" +
		"chain f { type nat hook postrouting priority 90; }"
	symbolic := "chain c { type filter hook prerouting priority mangle - 10; }\n" +
		"chain d { type nat hook prerouting priority dstnat - 10; }\n" +
		"chain e { type filter hook input priority filter - 10; }\n" +
		"chain f { type nat hook postrouting priority srcnat - 10; }"
	if got, want := canonicalNFTText(symbolic), canonicalNFTText(numeric); got != want {
		t.Fatalf("symbolic priority canonicalization mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestCanonicalNFTTextNormalizesKernelFormatting(t *testing.T) {
	rendered := `table ip yeet_iso {
set interfaces { type ifname; elements = { "yi-app" } }
set non_public { type ipv4_addr; flags interval; elements = { 192.0.0.8/32, 172.30.0.2/32 } }
chain input { type filter hook input priority -10; policy accept; iifname @interfaces reject with icmp type admin-prohibited; }
}`
	live := `table ip yeet_iso {
set interfaces { type ifname elements = { "yi-app" } }
set non_public { type ipv4_addr flags interval elements = { 192.0.0.8, 172.30.0.2 } }
chain input { type filter hook input priority filter - 10; policy accept; iifname @interfaces reject with icmp admin-prohibited }
}`
	if got, want := canonicalNFTText(live), canonicalNFTText(rendered); got != want {
		t.Fatalf("kernel formatting canonicalization mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestCanonicalIPTablesTextNormalizesSaveFormatting(t *testing.T) {
	rendered := `*filter
:YEET_ISO_INPUT - [0:0]
:YEET_ISO_FORWARD - [0:0]
-A INPUT -j YEET_ISO_INPUT
-A FORWARD -j YEET_ISO_FORWARD
-A YEET_ISO_INPUT -i yi-app -p udp --dport 5353 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
-A YEET_ISO_FORWARD -i yi-app -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
COMMIT`
	live := `*filter
:YEET_ISO_FORWARD - [0:0]
:YEET_ISO_INPUT - [0:0]
-A INPUT -j YEET_ISO_INPUT
-A FORWARD -j YEET_ISO_FORWARD
-A YEET_ISO_FORWARD -i yi-app -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A YEET_ISO_INPUT -i yi-app -p udp -m udp --dport 5353 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
COMMIT`
	if got, want := canonicalIPTablesText(live), canonicalIPTablesText(rendered); got != want {
		t.Fatalf("iptables-save canonicalization mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCanonicalIPTablesTextNormalizesMatchOrderAndHostPrefix(t *testing.T) {
	rendered := `*nat
:YEET_ISO_PREROUTE - [0:0]
-A YEET_ISO_PREROUTE -i br0 -d 172.30.128.1 -p udp --dport 53 -j DNAT --to-destination 172.30.0.1:53
COMMIT`
	live := `*nat
:YEET_ISO_PREROUTE - [0:0]
-A YEET_ISO_PREROUTE -d 172.30.128.1/32 -i br0 -p udp -m udp --dport 53 -j DNAT --to-destination 172.30.0.1:53
COMMIT`
	if got, want := canonicalIPTablesText(live), canonicalIPTablesText(rendered); got != want {
		t.Fatalf("iptables match canonicalization mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCanonicalISOIPSetTextNormalizesHostPrefixes(t *testing.T) {
	rendered := "create yeet_iso_sources hash:net,iface family inet\nadd yeet_iso_sources 172.30.0.2/32,yi-app\n"
	live := "create yeet_iso_sources hash:net,iface family inet hashsize 1024\nadd yeet_iso_sources 172.30.0.2,yi-app\n"
	if got, want := canonicalISOIPSetText(live), canonicalISOIPSetText(rendered); got != want {
		t.Fatalf("ipset canonicalization mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestVerifyISOPolicyDetectsIPTablesRuleOrderAndFirstJumpDrift(t *testing.T) {
	rules, err := RenderISOPolicy(BackendIPTablesNFT, testISOPolicySpec())
	if err != nil {
		t.Fatal(err)
	}
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })
	live4 := rules.IPv4
	runISOCommand = func(_ context.Context, _ []byte, name string, _ ...string) ([]byte, error) {
		switch name {
		case "iptables-nft-save":
			return []byte(live4), nil
		case "ip6tables-nft-save":
			return []byte(rules.IPv6), nil
		case "ipset":
			return []byte(rules.IPSet), nil
		default:
			return nil, errors.New("unexpected command " + name)
		}
	}
	if err := VerifyISOPolicy(context.Background(), rules); err != nil {
		t.Fatalf("VerifyISOPolicy exact live state: %v", err)
	}

	first := "-A YEET_ISO_FORWARD -i yi-a1b2c3 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
	second := "-A YEET_ISO_FORWARD -i yi-a1b2c3 -p tcp -m multiport --dports 53,853 -j REJECT --reject-with icmp-admin-prohibited"
	live4 = strings.Replace(live4, first+"\n"+second, second+"\n"+first, 1)
	if err := VerifyISOPolicy(context.Background(), rules); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("reordered policy error = %v, want digest mismatch", err)
	}

	live4 = strings.Replace(rules.IPv4, "-A FORWARD -j YEET_ISO_FORWARD", "-A FORWARD -j ACCEPT\n-A FORWARD -j YEET_ISO_FORWARD", 1)
	if err := VerifyISOPolicy(context.Background(), rules); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("lowered jump error = %v, want digest mismatch", err)
	}
}

func TestEnsureISOPolicyIPTablesAppliesSetsThenAtomicRestores(t *testing.T) {
	rules, err := RenderISOPolicy(BackendIPTablesNFT, testISOPolicySpec())
	if err != nil {
		t.Fatal(err)
	}
	oldRun := runISOCommand
	oldLookPath := lookPath
	oldCombined := runCombinedOutput
	t.Cleanup(func() {
		runISOCommand = oldRun
		lookPath = oldLookPath
		runCombinedOutput = oldCombined
	})
	lookPath = func(name string) (string, error) { return "/usr/sbin/" + name, nil }
	runCombinedOutput = func(name string, args ...string) ([]byte, error) {
		return []byte("iptables v1.8.11 (nf_tables)"), nil
	}
	var mutating []string
	runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if len(input) != 0 {
			mutating = append(mutating, name+" "+joined)
			return nil, nil
		}
		if strings.Contains(joined, " -S ") || strings.HasSuffix(joined, "-S INPUT") || strings.HasSuffix(joined, "-S FORWARD") || strings.HasSuffix(joined, "-S PREROUTING") || strings.HasSuffix(joined, "-S POSTROUTING") {
			fields := strings.Fields(joined)
			chain := fields[len(fields)-1]
			target := map[string]string{
				"INPUT": isoIPTablesInput, "FORWARD": isoIPTablesForward,
				"PREROUTING": isoIPTablesMangle, "POSTROUTING": isoIPTablesPostRoute,
			}[chain]
			if chain == "PREROUTING" && strings.Contains(joined, "-t nat") {
				target = isoIPTablesPreRoute
			}
			return []byte("-A " + chain + " -j " + target + "\n"), nil
		}
		switch name {
		case "iptables-nft-save":
			return []byte(rules.IPv4), nil
		case "ip6tables-nft-save":
			return []byte(rules.IPv6), nil
		case "ipset":
			return []byte(rules.IPSet), nil
		default:
			return nil, errors.New("unexpected read command " + name + " " + joined)
		}
	}
	if err := EnsureISOPolicy(context.Background(), rules); err != nil {
		t.Fatalf("EnsureISOPolicy returned error: %v", err)
	}
	wantPrefix := []string{"ipset restore -exist", "iptables-nft-restore --wait --noflush", "ip6tables-nft-restore --wait --noflush"}
	if diff := cmp.Diff(wantPrefix, mutating); diff != "" {
		t.Fatalf("mutation order (-want +got):\n%s", diff)
	}
}

func TestEnsureISOIPTablesJumpsReconcilesPolicyLinesStaleOrderAndDuplicates(t *testing.T) {
	oldRun := runISOCommand
	oldLookPath := lookPath
	oldCombined := runCombinedOutput
	t.Cleanup(func() {
		runISOCommand = oldRun
		lookPath = oldLookPath
		runCombinedOutput = oldCombined
	})
	lookPath = func(name string) (string, error) { return "/usr/sbin/" + name, nil }
	runCombinedOutput = func(name string, args ...string) ([]byte, error) {
		if strings.Contains(name, "legacy") {
			return []byte("iptables v1.8.11 (legacy)"), nil
		}
		return []byte("iptables v1.8.11 (nf_tables)"), nil
	}

	for _, backend := range []FirewallBackend{BackendIPTablesNFT, BackendIPTablesLegacy} {
		for _, ipv6 := range []bool{false, true} {
			backend, ipv6 := backend, ipv6
			t.Run(string(backend)+map[bool]string{false: "/ipv4", true: "/ipv6"}[ipv6], func(t *testing.T) {
				type chainState struct {
					chain  string
					target string
					rules  []string
				}
				states := map[string]*chainState{}
				add := func(table, chain, target string) {
					key := table + "/" + chain
					states[key] = &chainState{chain: chain, target: target, rules: []string{
						"-A " + chain + " -j ACCEPT",
						"-A " + chain + " -j " + target,
						"-A " + chain + " -j " + target,
					}}
				}
				add("filter", "INPUT", isoIPTablesInput)
				add("filter", "FORWARD", isoIPTablesForward)
				if !ipv6 {
					add("mangle", "PREROUTING", isoIPTablesMangle)
					add("nat", "PREROUTING", isoIPTablesPreRoute)
					add("nat", "POSTROUTING", isoIPTablesPostRoute)
				}

				mutations := 0
				runISOCommand = func(_ context.Context, input []byte, _ string, args ...string) ([]byte, error) {
					if len(input) != 0 {
						return nil, errors.New("unexpected restore input")
					}
					if len(args) < 5 || args[0] != iptablesWaitArg {
						return nil, errors.New("iptables jump command did not wait for xtables lock")
					}
					table := args[2]
					chain := args[4]
					state := states[table+"/"+chain]
					if state == nil {
						return nil, errors.New("unexpected chain")
					}
					switch args[3] {
					case "-S":
						return []byte("-P " + chain + " ACCEPT\n" + strings.Join(state.rules, "\n") + "\n"), nil
					case "-D":
						mutations++
						want := "-A " + chain + " -j " + args[6]
						for index, rule := range state.rules {
							if rule == want {
								state.rules = append(state.rules[:index], state.rules[index+1:]...)
								return nil, nil
							}
						}
						return nil, errors.New("delete missing jump")
					case "-I":
						mutations++
						state.rules = append([]string{"-A " + chain + " -j " + args[7]}, state.rules...)
						return nil, nil
					default:
						return nil, errors.New("unexpected operation " + args[3])
					}
				}

				if err := ensureISOIPTablesJumps(context.Background(), backend, ipv6); err != nil {
					t.Fatal(err)
				}
				firstMutations := mutations
				if err := ensureISOIPTablesJumps(context.Background(), backend, ipv6); err != nil {
					t.Fatal(err)
				}
				if mutations != firstMutations {
					t.Fatalf("second reconciliation mutated stable jumps: first=%d total=%d", firstMutations, mutations)
				}
				for key, state := range states {
					want := "-A " + state.chain + " -j " + state.target
					if got := state.rules[0]; got != want {
						t.Errorf("%s first rule = %q, want %q", key, got, want)
					}
					count := 0
					for _, rule := range state.rules {
						if rule == want {
							count++
						}
					}
					if count != 1 {
						t.Errorf("%s exact jump count = %d, want 1: %#v", key, count, state.rules)
					}
				}
			})
		}
	}
}

func testISOPolicySpec() ISOPolicySpec {
	return ISOPolicySpec{
		Pool:    netip.MustParsePrefix("172.30.0.0/16"),
		DNSPort: 5353,
		Endpoints: []ISOEndpoint{{
			Interface: "yi-a1b2c3",
			Link:      netip.MustParsePrefix("172.30.0.0/30"),
			PeerIP:    netip.MustParseAddr("172.30.0.2"),
			Project:   netip.MustParsePrefix("172.30.128.0/27"),
		}},
	}
}

func assertContainsAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered policy missing %q:\n%s", want, got)
		}
	}
}
