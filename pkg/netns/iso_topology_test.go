// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestISOTopologyCommandsRouteProjectThroughPeer(t *testing.T) {
	spec := testISOTopologySpec()
	commands, err := ISOTopologyEnsureCommands(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertISOCommand(t, commands, "ip", "route", "replace", "blackhole", "172.30.0.0/16", "metric", "42760")
	assertISOCommand(t, commands, "ip", "route", "replace", "172.30.128.0/27", "via", "172.30.0.2", "dev", "yi-a1b2c3")
	assertISOCommand(t, commands, "ip", "netns", "exec", "yeet-app-ns", "ip", "route", "replace", "default", "via", "172.30.0.1", "dev", "yo-a1b2c3")
	assertISOCommand(t, commands, "ip", "netns", "exec", "yeet-app-ns", "sysctl", "-w", "net.ipv4.ip_forward=1")
	assertISOCommand(t, commands, "sysctl", "-w", "net.ipv6.conf.yi-a1b2c3.disable_ipv6=1")

	joined := joinISOCommandInput(commands)
	assertContainsAll(t, joined,
		"172.30.128.1", "172.30.0.1:53", "br0", "ct state established,related", "drop", "reject",
	)
	if strings.Contains(joined, "masquerade") || strings.Contains(joined, "MASQUERADE") {
		t.Fatalf("router namespace policy must not masquerade:\n%s", joined)
	}
}

func TestISORouterIPTablesRestoresWaitForXTablesLock(t *testing.T) {
	for _, backend := range []FirewallBackend{BackendIPTablesNFT, BackendIPTablesLegacy} {
		backend := backend
		t.Run(string(backend), func(t *testing.T) {
			spec := testISOTopologySpec()
			spec.Backend = backend
			oldLookPath := lookPath
			oldCombined := runCombinedOutput
			t.Cleanup(func() {
				lookPath = oldLookPath
				runCombinedOutput = oldCombined
			})
			lookPath = func(name string) (string, error) { return "/usr/sbin/" + name, nil }
			runCombinedOutput = func(name string, _ ...string) ([]byte, error) {
				if strings.Contains(name, "legacy") {
					return []byte("iptables v1.8.11 (legacy)"), nil
				}
				return []byte("iptables v1.8.11 (nf_tables)"), nil
			}

			commands, err := ISOTopologyEnsureCommands(spec)
			if err != nil {
				t.Fatal(err)
			}
			var restores int
			for _, command := range commands {
				if !strings.Contains(strings.Join(command.Args, " "), "tables-") || !strings.Contains(strings.Join(command.Args, " "), "-restore") {
					continue
				}
				restores++
				if !slices.Contains(command.Args, "--wait") {
					t.Fatalf("restore command does not wait for xtables lock: %v", command.Args)
				}
			}
			if restores != 2 {
				t.Fatalf("restore commands = %d, want 2", restores)
			}
		})
	}
}

func TestISOTopologyCommandsAddOnlyExplicitTailscaleRoutePolicy(t *testing.T) {
	spec := testISOTopologySpec()
	spec.TailscaleInterface = "ts0"
	commands, err := ISOTopologyEnsureCommands(spec)
	if err != nil {
		t.Fatal(err)
	}
	joined := joinISOCommandInput(commands)
	assertContainsAll(t, joined, "ts0", "172.30.128.0/27")
	if strings.Contains(joined, "100.64.0.0/10") {
		t.Fatalf("router namespace unexpectedly adds a CGNAT exception:\n%s", joined)
	}
}

func TestISORouterPolicyRejectsAllNewIngressAndUnapprovedTailscaleForwarding(t *testing.T) {
	spec := testISOTopologySpec()
	spec.TailscaleInterface = "ts0"
	for _, backend := range []FirewallBackend{BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy} {
		backend := backend
		t.Run(string(backend), func(t *testing.T) {
			var ipv4, ipv6 string
			if backend == BackendNFT {
				ipv4, ipv6 = renderNFTISORouterPolicy(spec, "br0")
				assertContainsAll(t, ipv4,
					`iifname != "lo" reject with icmp type admin-prohibited`,
					`iifname "br0" oifname "br0" ip saddr 172.30.128.0/27 ip daddr 172.30.128.0/27 accept`,
					`iifname "br0" ip saddr 172.30.128.0/27 oifname "ts0" accept`,
					`iifname "ts0" oifname "br0" ip daddr 172.30.128.0/27 accept`,
					`iifname { "br0", "yo-a1b2c3", "ts0" } reject with icmp type admin-prohibited`,
					`oifname { "br0", "yo-a1b2c3", "ts0" } reject with icmp type admin-prohibited`,
				)
				assertContainsAll(t, ipv6,
					`iifname != "lo" drop`,
					`iifname { "br0", "yo-a1b2c3", "ts0" } drop`,
					`oifname { "br0", "yo-a1b2c3", "ts0" } drop`,
				)
			} else {
				ipv4, ipv6 = renderIPTablesISORouterPolicy(spec, "br0")
				assertContainsAll(t, ipv4,
					"-A YEET_ISO_R_INPUT ! -i lo -j REJECT --reject-with icmp-admin-prohibited",
					"-A YEET_ISO_R_FORWARD -i br0 -o br0 -s 172.30.128.0/27 -d 172.30.128.0/27 -j ACCEPT",
					"-A YEET_ISO_R_FORWARD -i br0 -s 172.30.128.0/27 -o ts0 -j ACCEPT",
					"-A YEET_ISO_R_FORWARD -i ts0 -o br0 -d 172.30.128.0/27 -j ACCEPT",
					"-A YEET_ISO_R_FORWARD -i ts0 -j REJECT --reject-with icmp-admin-prohibited",
					"-A YEET_ISO_R_FORWARD -o ts0 -j REJECT --reject-with icmp-admin-prohibited",
				)
				assertContainsAll(t, ipv6,
					"-A YEET_ISO_R_INPUT ! -i lo -j DROP",
					"-A YEET_ISO_R_FORWARD -i ts0 -j DROP",
					"-A YEET_ISO_R_FORWARD -o ts0 -j DROP",
				)
			}
			if strings.Contains(ipv4, "100.64.0.0/10") {
				t.Fatalf("router policy added a CGNAT exception:\n%s", ipv4)
			}
		})
	}
}

func TestISORouterNFTPolicyAtomicallyReplacesOwnedTablesOnRepeatedApply(t *testing.T) {
	spec := testISOTopologySpec()
	commands := isoRouterPolicyCommands(spec, "br0")
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })
	present := map[string]bool{}
	var mutations []string
	runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
		if name != "ip" || len(args) < 4 || !slices.Equal(args[:3], []string{"netns", "exec", spec.Allocation.NetNS}) {
			return nil, errors.New("unexpected router command")
		}
		if len(input) == 0 {
			family := args[len(args)-2]
			if !present[family] {
				return nil, errors.New("No such file or directory")
			}
			return []byte("table " + family + " " + isoRouterNFTTable + " {}\n"), nil
		}
		script := string(input)
		mutations = append(mutations, script)
		if strings.Contains(script, "flush ruleset") || strings.Contains(script, "flush table") {
			return nil, errors.New("router apply flushed firewall state")
		}
		family := "ip"
		if strings.Contains(script, "table ip6 "+isoRouterNFTTable) {
			family = "ip6"
		}
		deleting := strings.Contains(script, "delete table "+family+" "+isoRouterNFTTable)
		if deleting != present[family] {
			return nil, errors.New("router owned-table replacement did not match live state")
		}
		if deleting {
			present[family] = false
		}
		if !strings.Contains(script, "table "+family+" "+isoRouterNFTTable) || present[family] {
			return nil, errors.New("router owned table was not declared from an absent state")
		}
		present[family] = true
		return nil, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		for _, command := range commands {
			if err := command.Run(context.Background()); err != nil {
				t.Fatalf("attempt %d: %v", attempt+1, err)
			}
		}
	}
	if got, want := len(mutations), 4; got != want {
		t.Fatalf("mutation count = %d, want %d: %#v", got, want, mutations)
	}
}

func TestISORouterIPTablesJumpsReconcileDuplicatesAndPolicyLines(t *testing.T) {
	oldRun := runISOCommand
	oldLookPath := lookPath
	oldCombined := runCombinedOutput
	t.Cleanup(func() {
		runISOCommand = oldRun
		lookPath = oldLookPath
		runCombinedOutput = oldCombined
	})
	lookPath = func(name string) (string, error) { return "/usr/sbin/" + name, nil }
	runCombinedOutput = func(name string, _ ...string) ([]byte, error) {
		if strings.Contains(name, "legacy") {
			return []byte("iptables v1.8.11 (legacy)"), nil
		}
		return []byte("iptables v1.8.11 (nf_tables)"), nil
	}

	for _, backend := range []FirewallBackend{BackendIPTablesNFT, BackendIPTablesLegacy} {
		backend := backend
		t.Run(string(backend), func(t *testing.T) {
			spec := testISOTopologySpec()
			spec.Backend = backend
			type chainState struct {
				chain, target string
				rules         []string
			}
			states := map[string]*chainState{}
			add := func(bin, table, chain, target string) {
				states[bin+"/"+table+"/"+chain] = &chainState{chain: chain, target: target, rules: []string{
					"-A " + chain + " -j ACCEPT",
					"-A " + chain + " -j " + target,
					"-A " + chain + " -j " + target,
				}}
			}
			bin, err := iptablesBinary(backend)
			if err != nil {
				t.Fatal(err)
			}
			add(bin, "mangle", "PREROUTING", isoRouterMangle)
			add(bin, "nat", "PREROUTING", isoRouterPreRoute)
			add(bin, "filter", "INPUT", isoRouterInput)
			add(bin, "filter", "FORWARD", isoRouterForward)
			ip6bin := strings.Replace(bin, "iptables", "ip6tables", 1)
			add(ip6bin, "filter", "INPUT", isoRouterInput)
			add(ip6bin, "filter", "FORWARD", isoRouterForward)

			mutations := 0
			runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
				if name != "ip" || len(input) != 0 || len(args) < 9 || args[4] != iptablesWaitArg {
					return nil, errors.New("unexpected router jump command")
				}
				state := states[args[3]+"/"+args[6]+"/"+args[8]]
				if state == nil {
					return nil, errors.New("unexpected router jump state")
				}
				switch args[7] {
				case "-S":
					return []byte("-P " + state.chain + " ACCEPT\n" + strings.Join(state.rules, "\n") + "\n"), nil
				case "-D":
					mutations++
					want := "-A " + state.chain + " -j " + args[10]
					for index, rule := range state.rules {
						if rule == want {
							state.rules = append(state.rules[:index], state.rules[index+1:]...)
							return nil, nil
						}
					}
					return nil, errors.New("delete missing router jump")
				case "-I":
					mutations++
					state.rules = append([]string{"-A " + state.chain + " -j " + args[11]}, state.rules...)
					return nil, nil
				default:
					return nil, errors.New("unexpected router jump operation " + args[7])
				}
			}

			commands := isoRouterJumpCommands(spec, backend)
			for attempt := 0; attempt < 2; attempt++ {
				before := mutations
				for _, command := range commands {
					if err := command.Run(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				if attempt == 1 && mutations != before {
					t.Fatalf("second reconciliation mutated stable router jumps: before=%d after=%d", before, mutations)
				}
			}
			for key, state := range states {
				want := "-A " + state.chain + " -j " + state.target
				if state.rules[0] != want {
					t.Errorf("%s first rule = %q, want %q", key, state.rules[0], want)
				}
				count := 0
				for _, rule := range state.rules {
					if rule == want {
						count++
					}
				}
				if count != 1 {
					t.Errorf("%s exact jump count = %d, want 1", key, count)
				}
			}
		})
	}
}

func TestEnsureISOTopologyAppliesAndVerifiesNFT(t *testing.T) {
	spec := testISOTopologySpec()
	want4, want6 := renderNFTISORouterPolicy(spec, "br0")
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })
	mutations := 0
	runISOCommand = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
		if len(input) != 0 {
			mutations++
			return nil, nil
		}
		joined := strings.Join(args, " ")
		switch {
		case name == "ip" && (joined == "route show exact 172.30.0.0/16" || joined == "-o route show exact 172.30.0.0/16"):
			return []byte("blackhole 172.30.0.0/16 metric 42760\n"), nil
		case name == "ip" && joined == "-o link show dev yi-a1b2c3":
			return []byte("4: yi-a1b2c3: <BROADCAST,UP,LOWER_UP> mtu 1500 state UP\n"), nil
		case name == "ip" && joined == "-o -4 address show dev yi-a1b2c3 scope global":
			return []byte("4: yi-a1b2c3 inet 172.30.0.1/30 scope global yi-a1b2c3\n"), nil
		case name == "ip" && (joined == "route show exact 172.30.128.0/27" || joined == "-o route show exact 172.30.128.0/27"):
			return []byte("172.30.128.0/27 via 172.30.0.2 dev yi-a1b2c3\n"), nil
		case name == "ip" && strings.Contains(joined, "ip -o link show dev yo-a1b2c3"):
			return []byte("5: yo-a1b2c3: <BROADCAST,UP,LOWER_UP> mtu 1500 state UP\n"), nil
		case name == "ip" && (strings.Contains(joined, "ip -o address show dev yo-a1b2c3") || strings.Contains(joined, "ip -o -4 address show dev yo-a1b2c3 scope global")):
			return []byte("5: yo-a1b2c3 inet 172.30.0.2/30\n"), nil
		case name == "ip" && (strings.Contains(joined, "ip route show default") || strings.Contains(joined, "ip -o route show exact default")):
			return []byte("default via 172.30.0.1 dev yo-a1b2c3\n"), nil
		case name == "sysctl" && (strings.HasSuffix(joined, ".accept_local") || strings.HasSuffix(joined, ".route_localnet")):
			return []byte("0\n"), nil
		case name == "sysctl" || name == "ip" && strings.Contains(joined, "sysctl -n"):
			return []byte("1\n"), nil
		case name == "ip" && strings.HasSuffix(joined, "nft list table ip "+isoRouterNFTTable):
			return []byte(want4), nil
		case name == "ip" && strings.HasSuffix(joined, "nft list table ip6 "+isoRouterNFTTable):
			return []byte(want6), nil
		default:
			return nil, nil
		}
	}

	if err := EnsureISOTopology(context.Background(), spec); err != nil {
		t.Fatalf("EnsureISOTopology returned error: %v", err)
	}
	if mutations == 0 {
		t.Fatal("EnsureISOTopology applied no router policy")
	}
}

func TestVerifyISOTopologyRequiresExactAddressesRoutesLinksAndEverySysctl(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(map[string]string)
	}{
		{name: "root address drift", edit: func(outputs map[string]string) {
			outputs["ip -o -4 address show dev yi-a1b2c3 scope global"] = "4: yi-a1b2c3 inet 172.30.0.2/30 scope global yi-a1b2c3\n"
		}},
		{name: "root link down", edit: func(outputs map[string]string) {
			outputs["ip -o link show dev yi-a1b2c3"] = "4: yi-a1b2c3: <BROADCAST> mtu 1500 state DOWN\n"
		}},
		{name: "duplicate project route", edit: func(outputs map[string]string) {
			outputs["ip -o route show exact 172.30.128.0/27"] += "172.30.128.0/27 via 172.30.0.3 dev yi-a1b2c3 metric 99\n"
		}},
		{name: "peer address substring collision", edit: func(outputs map[string]string) {
			outputs["ip netns exec yeet-app-ns ip -o -4 address show dev yo-a1b2c3 scope global"] = "5: yo-a1b2c3 inet 172.30.0.20/30 scope global yo-a1b2c3\n"
		}},
		{name: "peer link down", edit: func(outputs map[string]string) {
			outputs["ip netns exec yeet-app-ns ip -o link show dev yo-a1b2c3"] = "5: yo-a1b2c3: <BROADCAST> mtu 1500 state DOWN\n"
		}},
		{name: "duplicate default route", edit: func(outputs map[string]string) {
			outputs["ip netns exec yeet-app-ns ip -o route show exact default"] += "default via 172.30.0.3 dev yo-a1b2c3 metric 99\n"
		}},
		{name: "root accept local enabled", edit: func(outputs map[string]string) {
			outputs["sysctl -n net.ipv4.conf.yi-a1b2c3.accept_local"] = "1\n"
		}},
		{name: "router peer IPv6 enabled", edit: func(outputs map[string]string) {
			outputs["ip netns exec yeet-app-ns sysctl -n net.ipv6.conf.yo-a1b2c3.disable_ipv6"] = "0\n"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := testISOTopologySpec()
			want4, want6 := renderNFTISORouterPolicy(spec, "br0")
			outputs := exactISOTopologyOutputs(want4, want6)
			tt.edit(outputs)
			oldRun := runISOCommand
			t.Cleanup(func() { runISOCommand = oldRun })
			runISOCommand = func(_ context.Context, _ []byte, name string, args ...string) ([]byte, error) {
				key := strings.TrimSpace(name + " " + strings.Join(args, " "))
				if output, ok := outputs[key]; ok {
					return []byte(output), nil
				}
				return nil, errors.New("unexpected probe " + key)
			}
			if err := VerifyISOTopology(context.Background(), spec); err == nil {
				t.Fatal("VerifyISOTopology returned nil error")
			}
		})
	}
}

func exactISOTopologyOutputs(want4, want6 string) map[string]string {
	outputs := map[string]string{
		"ip route show exact 172.30.0.0/16":                                          "blackhole 172.30.0.0/16 metric 42760\n",
		"ip -o route show exact 172.30.0.0/16":                                       "blackhole 172.30.0.0/16 metric 42760\n",
		"ip -o link show dev yi-a1b2c3":                                              "4: yi-a1b2c3: <BROADCAST,UP,LOWER_UP> mtu 1500 state UP\n",
		"ip -o -4 address show dev yi-a1b2c3 scope global":                           "4: yi-a1b2c3 inet 172.30.0.1/30 scope global yi-a1b2c3\n",
		"ip route show exact 172.30.128.0/27":                                        "172.30.128.0/27 via 172.30.0.2 dev yi-a1b2c3\n",
		"ip -o route show exact 172.30.128.0/27":                                     "172.30.128.0/27 via 172.30.0.2 dev yi-a1b2c3\n",
		"ip netns exec yeet-app-ns ip -o address show dev yo-a1b2c3":                 "5: yo-a1b2c3 inet 172.30.0.2/30 scope global yo-a1b2c3\n",
		"ip netns exec yeet-app-ns ip -o -4 address show dev yo-a1b2c3 scope global": "5: yo-a1b2c3 inet 172.30.0.2/30 scope global yo-a1b2c3\n",
		"ip netns exec yeet-app-ns ip -o link show dev yo-a1b2c3":                    "5: yo-a1b2c3: <BROADCAST,UP,LOWER_UP> mtu 1500 state UP\n",
		"ip netns exec yeet-app-ns ip route show default":                            "default via 172.30.0.1 dev yo-a1b2c3\n",
		"ip netns exec yeet-app-ns ip -o route show exact default":                   "default via 172.30.0.1 dev yo-a1b2c3\n",
		"sysctl -n net.ipv4.ip_forward":                                              "1\n",
		"sysctl -n net.ipv4.conf.yi-a1b2c3.forwarding":                               "1\n",
		"sysctl -n net.ipv4.conf.yi-a1b2c3.rp_filter":                                "1\n",
		"sysctl -n net.ipv4.conf.yi-a1b2c3.accept_local":                             "0\n",
		"sysctl -n net.ipv4.conf.yi-a1b2c3.route_localnet":                           "0\n",
		"sysctl -n net.ipv6.conf.yi-a1b2c3.disable_ipv6":                             "1\n",
		"ip netns exec yeet-app-ns sysctl -n net.ipv4.ip_forward":                    "1\n",
		"ip netns exec yeet-app-ns sysctl -n net.ipv4.conf.all.rp_filter":            "1\n",
		"ip netns exec yeet-app-ns sysctl -n net.ipv4.conf.default.rp_filter":        "1\n",
		"ip netns exec yeet-app-ns sysctl -n net.ipv4.conf.yo-a1b2c3.rp_filter":      "1\n",
		"ip netns exec yeet-app-ns sysctl -n net.ipv6.conf.all.disable_ipv6":         "1\n",
		"ip netns exec yeet-app-ns sysctl -n net.ipv6.conf.default.disable_ipv6":     "1\n",
		"ip netns exec yeet-app-ns sysctl -n net.ipv6.conf.yo-a1b2c3.disable_ipv6":   "1\n",
		"ip netns exec yeet-app-ns nft list table ip " + isoRouterNFTTable:           want4,
		"ip netns exec yeet-app-ns nft list table ip6 " + isoRouterNFTTable:          want6,
	}
	return outputs
}

func TestISOTopologyIPTablesPolicyIsOwnedAndVerified(t *testing.T) {
	spec := testISOTopologySpec()
	spec.Backend = BackendIPTablesNFT
	oldLookPath := lookPath
	oldCombined := runCombinedOutput
	oldRun := runISOCommand
	t.Cleanup(func() {
		lookPath = oldLookPath
		runCombinedOutput = oldCombined
		runISOCommand = oldRun
	})
	lookPath = func(name string) (string, error) { return "/usr/sbin/" + name, nil }
	runCombinedOutput = func(string, ...string) ([]byte, error) {
		return []byte("iptables v1.8.11 (nf_tables)"), nil
	}
	commands, err := ISOTopologyEnsureCommands(spec)
	if err != nil {
		t.Fatal(err)
	}
	joined := joinISOCommandInput(commands)
	assertContainsAll(t, joined, "--to-destination 172.30.0.1:53", "YEET_ISO_R_FORWARD")
	if strings.Contains(joined, "MASQUERADE") {
		t.Fatalf("router namespace policy must not masquerade:\n%s", joined)
	}

	want4, want6 := renderIPTablesISORouterPolicy(spec, "br0")
	live4 := want4
	runISOCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "ip6tables-nft-save") {
			return []byte(want6), nil
		}
		return []byte(live4), nil
	}
	if err := verifyISORouterPolicy(context.Background(), spec); err != nil {
		t.Fatalf("verifyISORouterPolicy exact live state: %v", err)
	}
	live4 = strings.Replace(want4, "-A FORWARD -j YEET_ISO_R_FORWARD", "-A FORWARD -j ACCEPT\n-A FORWARD -j YEET_ISO_R_FORWARD", 1)
	if err := verifyISORouterPolicy(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("lowered router jump error = %v, want digest mismatch", err)
	}
}

func TestISOCommandRunChecksAndClassifiesIdempotentErrors(t *testing.T) {
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })
	calls := 0
	runISOCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		calls++
		if slices.Equal(args, []string{"check"}) {
			return []byte("-A INPUT -j YEET_ISO\n"), nil
		}
		return nil, errors.New("unexpected mutation")
	}
	command := ISOCommand{Name: "iptables", Args: []string{"insert"}, CheckArgs: []string{"check"}, CheckFirstRule: "-A INPUT -j YEET_ISO"}
	if err := command.Run(context.Background()); err != nil || calls != 1 {
		t.Fatalf("checked command = (%v, %d calls), want nil and one call", err, calls)
	}

	runISOCommand = func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("file exists")
	}
	if err := (ISOCommand{Name: "ip", IgnoreExists: true}).Run(context.Background()); err != nil {
		t.Fatalf("IgnoreExists command: %v", err)
	}
	runISOCommand = func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("device not found")
	}
	if err := (ISOCommand{Name: "ip", IgnoreNotFound: true}).Run(context.Background()); err != nil {
		t.Fatalf("IgnoreNotFound command: %v", err)
	}
}

func TestISOTopologyRejectsAllocationOutsidePool(t *testing.T) {
	spec := testISOTopologySpec()
	spec.Allocation.Project = netip.MustParsePrefix("192.168.50.0/27")
	if _, err := ISOTopologyEnsureCommands(spec); err == nil {
		t.Fatal("ISOTopologyEnsureCommands returned nil error")
	}
}

func TestISOTopologyRejectsInvalidTopologyFields(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ISOTopologySpec)
	}{
		{name: "backend", edit: func(s *ISOTopologySpec) { s.Backend = FirewallBackend("unknown") }},
		{name: "pool", edit: func(s *ISOTopologySpec) { s.Pool = netip.MustParsePrefix("172.30.0.0/24") }},
		{name: "link", edit: func(s *ISOTopologySpec) { s.Allocation.Link = netip.MustParsePrefix("172.30.0.0/29") }},
		{name: "link addresses", edit: func(s *ISOTopologySpec) { s.Allocation.PeerIP = s.Allocation.HostIP }},
		{name: "root interface", edit: func(s *ISOTopologySpec) { s.Allocation.Interface = "bad interface" }},
		{name: "peer interface", edit: func(s *ISOTopologySpec) { s.Allocation.PeerInterface = "bad interface" }},
		{name: "project", edit: func(s *ISOTopologySpec) { s.Allocation.Project = netip.MustParsePrefix("172.30.128.0/28") }},
		{name: "gateway", edit: func(s *ISOTopologySpec) { s.Allocation.Gateway = netip.MustParseAddr("172.30.129.1") }},
		{name: "namespace", edit: func(s *ISOTopologySpec) { s.Allocation.NetNS = " " }},
		{name: "tailscale interface", edit: func(s *ISOTopologySpec) { s.TailscaleInterface = "bad interface" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := testISOTopologySpec()
			tt.edit(&spec)
			if _, err := ISOTopologyEnsureCommands(spec); err == nil {
				t.Fatal("ISOTopologyEnsureCommands returned nil error")
			}
		})
	}
}

func TestISOTopologyRejectsNonAllocatorAddressShape(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*ISOTopologySpec)
	}{
		{name: "link in upper half", edit: func(s *ISOTopologySpec) {
			s.Allocation.Link = netip.MustParsePrefix("172.30.128.0/30")
			s.Allocation.HostIP = netip.MustParseAddr("172.30.128.1")
			s.Allocation.PeerIP = netip.MustParseAddr("172.30.128.2")
		}},
		{name: "host and peer reversed", edit: func(s *ISOTopologySpec) {
			s.Allocation.HostIP, s.Allocation.PeerIP = s.Allocation.PeerIP, s.Allocation.HostIP
		}},
		{name: "project in lower half", edit: func(s *ISOTopologySpec) {
			s.Allocation.Project = netip.MustParsePrefix("172.30.64.0/27")
			s.Allocation.Gateway = netip.MustParseAddr("172.30.64.1")
		}},
		{name: "gateway is not project host one", edit: func(s *ISOTopologySpec) {
			s.Allocation.Gateway = netip.MustParseAddr("172.30.128.2")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := testISOTopologySpec()
			tt.edit(&spec)
			if _, err := ISOTopologyEnsureCommands(spec); err == nil {
				t.Fatal("ISOTopologyEnsureCommands returned nil error")
			}
		})
	}
}

func TestISOTopologyVMOnlyOwnsAggregateRoute(t *testing.T) {
	spec := testISOTopologySpec()
	spec.Allocation.Kind = "vm"
	spec.Allocation.Project = netip.Prefix{}
	spec.Allocation.Gateway = netip.Addr{}
	spec.Allocation.PeerInterface = ""
	spec.Allocation.NetNS = ""
	spec.Allocation.Bridge = ""
	commands, err := ISOTopologyEnsureCommands(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || !slices.Equal(commands[0].Args, []string{"route", "replace", "blackhole", "172.30.0.0/16", "metric", "42760"}) {
		t.Fatalf("VM topology commands = %#v", commands)
	}
}

func TestVerifyISOTopologyAbsentFailsClosedOnProbeError(t *testing.T) {
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })
	want := errors.New("permission denied")
	runISOCommand = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return nil, want
	}
	err := VerifyISOTopologyAbsent(context.Background(), testISOTopologySpec())
	if !errors.Is(err, want) {
		t.Fatalf("VerifyISOTopologyAbsent error = %v, want %v", err, want)
	}
}

func TestVerifyISOTopologyAbsentAcceptsOnlyCompleteAbsence(t *testing.T) {
	oldRun := runISOCommand
	t.Cleanup(func() { runISOCommand = oldRun })
	runISOCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "link show dev yi-a1b2c3":
			return []byte("Device \"yi-a1b2c3\" does not exist.\n"), errors.New("device not found")
		case joined == "netns list", joined == "route show exact 172.30.128.0/27":
			return nil, nil
		default:
			return nil, errors.New("unexpected probe " + joined)
		}
	}
	if err := VerifyISOTopologyAbsent(context.Background(), testISOTopologySpec()); err != nil {
		t.Fatalf("VerifyISOTopologyAbsent returned error: %v", err)
	}
}

func testISOTopologySpec() ISOTopologySpec {
	return ISOTopologySpec{
		Backend: BackendNFT,
		Pool:    netip.MustParsePrefix("172.30.0.0/16"),
		Allocation: db.ISOAllocation{
			Kind:          "compose",
			Link:          netip.MustParsePrefix("172.30.0.0/30"),
			HostIP:        netip.MustParseAddr("172.30.0.1"),
			PeerIP:        netip.MustParseAddr("172.30.0.2"),
			Project:       netip.MustParsePrefix("172.30.128.0/27"),
			Gateway:       netip.MustParseAddr("172.30.128.1"),
			Interface:     "yi-a1b2c3",
			PeerInterface: "yo-a1b2c3",
			NetNS:         "yeet-app-ns",
			Bridge:        "br0",
		},
	}
}

func assertISOCommand(t *testing.T, commands []ISOCommand, name string, args ...string) {
	t.Helper()
	for _, command := range commands {
		if command.Name == name && strings.Join(command.Args, "\x00") == strings.Join(args, "\x00") {
			return
		}
	}
	t.Fatalf("missing command %s %s in %#v", name, strings.Join(args, " "), commands)
}

func joinISOCommandInput(commands []ISOCommand) string {
	var out strings.Builder
	for _, command := range commands {
		out.WriteString(command.Input)
		out.WriteByte('\n')
	}
	return out.String()
}
