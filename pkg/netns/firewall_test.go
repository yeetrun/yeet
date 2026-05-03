// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestDetectFirewallBackendFromProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		probe probeResult
		want  FirewallBackend
	}{
		{
			name: "prefers nft when available",
			probe: probeResult{
				HasNFT:          true,
				IPTablesVersion: "v1.8.11 (nf_tables)",
			},
			want: BackendNFT,
		},
		{
			name: "falls back to iptables nft when nft is unavailable",
			probe: probeResult{
				HasNFT:          false,
				IPTablesVersion: "v1.8.11 (nf_tables)",
			},
			want: BackendIPTablesNFT,
		},
		{
			name: "falls back to legacy iptables when nft is unavailable",
			probe: probeResult{
				HasNFT:          false,
				IPTablesVersion: "v1.8.11 (legacy)",
			},
			want: BackendIPTablesLegacy,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := DetectFirewallBackendFromProbe(tt.probe)
			if got != tt.want {
				t.Fatalf("DetectFirewallBackendFromProbe(%+v) = %v, want %v", tt.probe, got, tt.want)
			}
		})
	}
}

func TestRenderRuleset(t *testing.T) {
	t.Parallel()

	spec := FirewallSpec{
		SubnetCIDR: "192.168.100.0/24",
		BridgeIf:   "yeet0",
	}

	tests := []struct {
		name      string
		backend   FirewallBackend
		wantParts []string
	}{
		{
			name:    "nft backend renders native yeet table",
			backend: BackendNFT,
			wantParts: []string{
				"table ip yeet",
			},
		},
		{
			name:    "iptables nft backend renders owned chains",
			backend: BackendIPTablesNFT,
			wantParts: []string{
				"YEET_FORWARD",
				"YEET_POSTROUTING",
			},
		},
		{
			name:    "iptables legacy backend renders owned chains",
			backend: BackendIPTablesLegacy,
			wantParts: []string{
				"YEET_FORWARD",
				"YEET_POSTROUTING",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := RenderFirewallRules(tt.backend, spec)
			for _, wantPart := range tt.wantParts {
				if !strings.Contains(got, wantPart) {
					t.Fatalf("RenderFirewallRules(%v, %+v) missing %q in output:\n%s", tt.backend, spec, wantPart, got)
				}
			}
		})
	}
}

func TestLoadFirewallEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		envv    []string
		want    FirewallConfig
		wantErr string
	}{
		{
			name: "loads explicit backend and default bridge",
			envv: []string{
				"RANGE=192.168.100.0/24",
				"FIREWALL_BACKEND=nft",
				"MALFORMED",
			},
			want: FirewallConfig{
				Backend: BackendNFT,
				Spec: FirewallSpec{
					SubnetCIDR: "192.168.100.0/24",
					BridgeIf:   defaultFirewallBridgeIf,
				},
			},
		},
		{
			name: "loads configured bridge",
			envv: []string{
				"RANGE=10.44.0.0/24",
				"BRIDGE_IF=br-yeet",
				"FIREWALL_BACKEND=iptables-legacy",
			},
			want: FirewallConfig{
				Backend: BackendIPTablesLegacy,
				Spec: FirewallSpec{
					SubnetCIDR: "10.44.0.0/24",
					BridgeIf:   "br-yeet",
				},
			},
		},
		{
			name:    "requires range",
			envv:    []string{"FIREWALL_BACKEND=nft"},
			wantErr: "missing RANGE",
		},
		{
			name: "rejects unsupported backend",
			envv: []string{
				"RANGE=192.168.100.0/24",
				"FIREWALL_BACKEND=pf",
			},
			wantErr: "unsupported firewall backend",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := LoadFirewallEnv(tt.envv)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("LoadFirewallEnv() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFirewallEnv() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("LoadFirewallEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadFirewallEnvDetectsBackendWhenUnset(t *testing.T) {
	withFirewallCommandFakes(t, func(name string) (string, error) {
		if name == "nft" {
			return "/usr/sbin/nft", nil
		}
		return "", exec.ErrNotFound
	}, func(name string, args ...string) ([]byte, error) {
		if commandKey(name, args...) == "nft --version" {
			return []byte("nftables v1.0.9"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", commandKey(name, args...))
	}, nil)

	got, err := LoadFirewallEnv([]string{"RANGE=192.168.100.0/24"})
	if err != nil {
		t.Fatalf("LoadFirewallEnv() returned error: %v", err)
	}
	if got.Backend != BackendNFT {
		t.Fatalf("LoadFirewallEnv() backend = %v, want %v", got.Backend, BackendNFT)
	}
}

func TestProbeFirewallBackend(t *testing.T) {
	tests := []struct {
		name        string
		paths       map[string]bool
		outputs     map[string]string
		outputError map[string]error
		want        probeResult
		wantErr     string
	}{
		{
			name: "uses nft when usable",
			paths: map[string]bool{
				"nft": true,
			},
			outputs: map[string]string{
				"nft --version": "nftables v1.0.9",
			},
			want: probeResult{HasNFT: true},
		},
		{
			name: "falls back to iptables version",
			paths: map[string]bool{
				"iptables": true,
			},
			outputs: map[string]string{
				"iptables --version": "iptables v1.8.11 (nf_tables)",
			},
			want: probeResult{IPTablesVersion: "iptables v1.8.11 (nf_tables)"},
		},
		{
			name: "skips unusable nft",
			paths: map[string]bool{
				"nft":             true,
				"iptables-legacy": true,
			},
			outputs: map[string]string{
				"iptables-legacy --version": "iptables v1.8.11 (legacy)",
			},
			outputError: map[string]error{
				"nft --version": errors.New("nft failed"),
			},
			want: probeResult{IPTablesVersion: "iptables v1.8.11 (legacy)"},
		},
		{
			name:    "fails without a usable backend",
			wantErr: "no usable firewall backend",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			withFirewallCommandFakes(t, lookupFromSet(tt.paths), outputFromMaps(tt.outputs, tt.outputError), nil)

			got, err := probeFirewallBackend()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("probeFirewallBackend() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("probeFirewallBackend() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("probeFirewallBackend() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestIPTablesBinary(t *testing.T) {
	tests := []struct {
		name    string
		backend FirewallBackend
		paths   map[string]bool
		outputs map[string]string
		want    string
		wantErr string
	}{
		{
			name:    "selects iptables nft binary",
			backend: BackendIPTablesNFT,
			paths: map[string]bool{
				"iptables-nft": true,
			},
			outputs: map[string]string{
				"iptables-nft --version": "iptables v1.8.11 (nf_tables)",
			},
			want: "iptables-nft",
		},
		{
			name:    "falls back to iptables for nft backend",
			backend: BackendIPTablesNFT,
			paths: map[string]bool{
				"iptables-nft": true,
				"iptables":     true,
			},
			outputs: map[string]string{
				"iptables-nft --version": "iptables v1.8.11 (legacy)",
				"iptables --version":     "iptables v1.8.11 (nf_tables)",
			},
			want: "iptables",
		},
		{
			name:    "selects iptables legacy binary",
			backend: BackendIPTablesLegacy,
			paths: map[string]bool{
				"iptables-legacy": true,
			},
			outputs: map[string]string{
				"iptables-legacy --version": "iptables v1.8.11 (legacy)",
			},
			want: "iptables-legacy",
		},
		{
			name:    "reports missing nft backend binary",
			backend: BackendIPTablesNFT,
			paths: map[string]bool{
				"iptables": true,
			},
			outputs: map[string]string{
				"iptables --version": "iptables v1.8.11 (legacy)",
			},
			wantErr: "iptables nft backend requested",
		},
		{
			name:    "rejects non iptables backend",
			backend: BackendNFT,
			wantErr: "unsupported",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			withFirewallCommandFakes(t, lookupFromSet(tt.paths), outputFromMaps(tt.outputs, nil), nil)

			got, err := iptablesBinary(tt.backend)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("iptablesBinary() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("iptablesBinary() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("iptablesBinary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureFirewallNFTReplacesTableAndLoadsRules(t *testing.T) {
	var calls []firewallCommandCall
	spec := FirewallSpec{SubnetCIDR: "192.168.100.0/24", BridgeIf: "yeet0"}
	withFirewallCommandFakes(t, lookupFromSet(map[string]bool{"nft": true}), func(name string, args ...string) ([]byte, error) {
		if commandKey(name, args...) == "nft list table ip yeet" {
			return []byte("table ip yeet {}"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", commandKey(name, args...))
	}, func(input []byte, name string, args ...string) error {
		calls = append(calls, firewallCommandCall{Input: string(input), Name: name, Args: append([]string(nil), args...)})
		return nil
	})

	if err := EnsureFirewall(BackendNFT, spec); err != nil {
		t.Fatalf("EnsureFirewall() returned error: %v", err)
	}
	wantCommands := []string{
		"nft delete table ip yeet",
		"nft -f -",
	}
	if got := commandStrings(calls); !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", got, wantCommands)
	}
	if !strings.Contains(calls[1].Input, "table ip yeet") || !strings.Contains(calls[1].Input, spec.SubnetCIDR) {
		t.Fatalf("nft load input missing expected rules:\n%s", calls[1].Input)
	}
}

func TestEnsureFirewallIPTablesInstallsOwnedChainsAndRules(t *testing.T) {
	var calls []firewallCommandCall
	spec := FirewallSpec{SubnetCIDR: "192.168.100.0/24", BridgeIf: "yeet0"}
	withFirewallCommandFakes(t, lookupFromSet(map[string]bool{"iptables-nft": true}), func(name string, args ...string) ([]byte, error) {
		if commandKey(name, args...) == "iptables-nft --version" {
			return []byte("iptables v1.8.11 (nf_tables)"), nil
		}
		return nil, errors.New("missing rule or chain")
	}, func(input []byte, name string, args ...string) error {
		calls = append(calls, firewallCommandCall{Name: name, Args: append([]string(nil), args...)})
		return nil
	})

	if err := EnsureFirewall(BackendIPTablesNFT, spec); err != nil {
		t.Fatalf("EnsureFirewall() returned error: %v", err)
	}
	got := commandStrings(calls)
	for _, want := range []string{
		"iptables-nft -t filter -N YEET_FORWARD",
		"iptables-nft -t filter -A FORWARD -j YEET_FORWARD",
		"iptables-nft -t filter -A YEET_FORWARD -i yeet0 -j ACCEPT",
		"iptables-nft -t filter -A YEET_FORWARD -o yeet0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"iptables-nft -t nat -N YEET_POSTROUTING",
		"iptables-nft -t nat -A POSTROUTING -j YEET_POSTROUTING",
		"iptables-nft -t nat -A YEET_POSTROUTING -s 192.168.100.0/24 ! -d 192.168.100.0/24 -j MASQUERADE",
	} {
		if !containsString(got, want) {
			t.Fatalf("commands missing %q in %#v", want, got)
		}
	}
}

func TestVerifyFirewallNFT(t *testing.T) {
	spec := FirewallSpec{SubnetCIDR: "192.168.100.0/24", BridgeIf: "yeet0"}

	t.Run("accepts expected table state", func(t *testing.T) {
		withFirewallCommandFakes(t, nil, func(name string, args ...string) ([]byte, error) {
			if commandKey(name, args...) == "nft list table ip yeet" {
				return []byte(`table ip yeet
iifname "yeet0" accept
ip saddr 192.168.100.0/24 masquerade`), nil
			}
			return nil, fmt.Errorf("unexpected command: %s", commandKey(name, args...))
		}, nil)

		if err := VerifyFirewall(BackendNFT, spec); err != nil {
			t.Fatalf("VerifyFirewall() returned error: %v", err)
		}
	})

	t.Run("reports missing marker", func(t *testing.T) {
		withFirewallCommandFakes(t, nil, func(name string, args ...string) ([]byte, error) {
			if commandKey(name, args...) == "nft list table ip yeet" {
				return []byte(`table ip yeet
iifname "yeet0" accept`), nil
			}
			return nil, fmt.Errorf("unexpected command: %s", commandKey(name, args...))
		}, nil)

		err := VerifyFirewall(BackendNFT, spec)
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("VerifyFirewall() error = %v, want missing marker error", err)
		}
	})
}

func TestVerifyFirewallIPTablesAcceptsExpectedRules(t *testing.T) {
	spec := FirewallSpec{SubnetCIDR: "192.168.100.0/24", BridgeIf: "yeet0"}
	withFirewallCommandFakes(t, lookupFromSet(map[string]bool{"iptables-nft": true}), func(name string, args ...string) ([]byte, error) {
		switch commandKey(name, args...) {
		case "iptables-nft --version":
			return []byte("iptables v1.8.11 (nf_tables)"), nil
		case "iptables-nft -S YEET_FORWARD":
			return []byte(`-A YEET_FORWARD -i yeet0 -j ACCEPT
-A YEET_FORWARD -o yeet0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT`), nil
		case "iptables-nft -t nat -S YEET_POSTROUTING":
			return []byte("-A YEET_POSTROUTING -s 192.168.100.0/24 ! -d 192.168.100.0/24 -j MASQUERADE"), nil
		case "iptables-nft -S FORWARD":
			return []byte("-A FORWARD -j YEET_FORWARD"), nil
		case "iptables-nft -t nat -S POSTROUTING":
			return []byte("-A POSTROUTING -j YEET_POSTROUTING"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", commandKey(name, args...))
		}
	}, nil)

	if err := VerifyFirewall(BackendIPTablesNFT, spec); err != nil {
		t.Fatalf("VerifyFirewall() returned error: %v", err)
	}
}

func TestCleanupFirewallIPTablesRemovesOwnedRulesAndChains(t *testing.T) {
	var calls []firewallCommandCall
	withFirewallCommandFakes(t, lookupFromSet(map[string]bool{"iptables-legacy": true}), func(name string, args ...string) ([]byte, error) {
		if commandKey(name, args...) == "iptables-legacy --version" {
			return []byte("iptables v1.8.11 (legacy)"), nil
		}
		return []byte("exists"), nil
	}, func(input []byte, name string, args ...string) error {
		calls = append(calls, firewallCommandCall{Name: name, Args: append([]string(nil), args...)})
		return nil
	})

	if err := CleanupFirewall(BackendIPTablesLegacy); err != nil {
		t.Fatalf("CleanupFirewall() returned error: %v", err)
	}
	got := commandStrings(calls)
	for _, want := range []string{
		"iptables-legacy -t filter -D FORWARD -j YEET_FORWARD",
		"iptables-legacy -t filter -F YEET_FORWARD",
		"iptables-legacy -t filter -X YEET_FORWARD",
		"iptables-legacy -t nat -D POSTROUTING -j YEET_POSTROUTING",
		"iptables-legacy -t nat -F YEET_POSTROUTING",
		"iptables-legacy -t nat -X YEET_POSTROUTING",
	} {
		if !containsString(got, want) {
			t.Fatalf("commands missing %q in %#v", want, got)
		}
	}
}

type firewallCommandCall struct {
	Input string
	Name  string
	Args  []string
}

func withFirewallCommandFakes(
	t *testing.T,
	fakeLookPath func(string) (string, error),
	fakeCombinedOutput func(string, ...string) ([]byte, error),
	fakeRunWithInput func([]byte, string, ...string) error,
) {
	t.Helper()

	oldLookPath := lookPath
	oldRunCombinedOutput := runCombinedOutput
	oldRunCommandWithInput := runCommandWithInput
	if fakeLookPath != nil {
		lookPath = fakeLookPath
	}
	if fakeCombinedOutput != nil {
		runCombinedOutput = fakeCombinedOutput
	}
	if fakeRunWithInput != nil {
		runCommandWithInput = fakeRunWithInput
	}
	t.Cleanup(func() {
		lookPath = oldLookPath
		runCombinedOutput = oldRunCombinedOutput
		runCommandWithInput = oldRunCommandWithInput
	})
}

func lookupFromSet(paths map[string]bool) func(string) (string, error) {
	return func(name string) (string, error) {
		if paths[name] {
			return "/usr/sbin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

func outputFromMaps(outputs map[string]string, outputErrors map[string]error) func(string, ...string) ([]byte, error) {
	return func(name string, args ...string) ([]byte, error) {
		key := commandKey(name, args...)
		if err := outputErrors[key]; err != nil {
			return nil, err
		}
		if out, ok := outputs[key]; ok {
			return []byte(out), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", key)
	}
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

func commandStrings(calls []firewallCommandCall) []string {
	got := make([]string, 0, len(calls))
	for _, call := range calls {
		got = append(got, commandKey(call.Name, call.Args...))
	}
	return got
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
