// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type FirewallBackend string

const (
	BackendNFT            FirewallBackend = "nft"
	BackendIPTablesNFT    FirewallBackend = "iptables-nft"
	BackendIPTablesLegacy FirewallBackend = "iptables-legacy"
)

type FirewallSpec struct {
	SubnetCIDR string
	BridgeIf   string
}

type FirewallConfig struct {
	Backend FirewallBackend
	Spec    FirewallSpec
}

type probeResult struct {
	HasNFT          bool
	IPTablesVersion string
}

const defaultFirewallBridgeIf = "yeet0"

var (
	lookPath          = exec.LookPath
	runCombinedOutput = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
	runCommandWithInput = func(input []byte, name string, args ...string) error {
		cmd := exec.Command(name, args...)
		if len(input) > 0 {
			cmd.Stdin = bytes.NewReader(input)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				return fmt.Errorf("failed to run %s %s: %w", name, strings.Join(args, " "), err)
			}
			return fmt.Errorf("failed to run %s %s: %w\n%s", name, strings.Join(args, " "), err, msg)
		}
		return nil
	}
)

func DetectFirewallBackend() (FirewallBackend, error) {
	probe, err := probeFirewallBackend()
	if err != nil {
		return "", err
	}
	backend := DetectFirewallBackendFromProbe(probe)
	if backend == "" {
		return "", fmt.Errorf("no usable firewall backend found")
	}
	return backend, nil
}

func LoadFirewallEnv(envv []string) (FirewallConfig, error) {
	vals := make(map[string]string, len(envv))
	for _, kv := range envv {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		vals[key] = val
	}

	spec := FirewallSpec{
		SubnetCIDR: vals["RANGE"],
		BridgeIf:   vals["BRIDGE_IF"],
	}
	if spec.SubnetCIDR == "" {
		return FirewallConfig{}, fmt.Errorf("missing RANGE in environment")
	}
	if spec.BridgeIf == "" {
		spec.BridgeIf = defaultFirewallBridgeIf
	}

	backend, err := parseFirewallBackend(vals["FIREWALL_BACKEND"])
	if err != nil {
		return FirewallConfig{}, err
	}
	return FirewallConfig{Backend: backend, Spec: spec}, nil
}

func DetectFirewallBackendFromProbe(probe probeResult) FirewallBackend {
	if probe.HasNFT {
		return BackendNFT
	}
	version := strings.ToLower(probe.IPTablesVersion)
	switch {
	case strings.Contains(version, "nf_tables"):
		return BackendIPTablesNFT
	case strings.Contains(version, "legacy"):
		return BackendIPTablesLegacy
	default:
		return ""
	}
}

func parseFirewallBackend(raw string) (FirewallBackend, error) {
	if raw == "" {
		return DetectFirewallBackend()
	}
	backend := FirewallBackend(raw)
	switch backend {
	case BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy:
		return backend, nil
	default:
		return "", fmt.Errorf("unsupported firewall backend %q", raw)
	}
}

func RenderFirewallRules(backend FirewallBackend, spec FirewallSpec) string {
	switch backend {
	case BackendNFT:
		return fmt.Sprintf(`table ip yeet {
	chain forward {
		type filter hook forward priority filter; policy accept;
		iifname "%s" accept
		oifname "%s" ct state related,established accept
	}

	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr %s ip daddr != %s masquerade
	}
}
`, spec.BridgeIf, spec.BridgeIf, spec.SubnetCIDR, spec.SubnetCIDR)
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		return fmt.Sprintf(`*filter
:YEET_FORWARD -
-A FORWARD -j YEET_FORWARD
-A YEET_FORWARD -i %s -j ACCEPT
-A YEET_FORWARD -o %s -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
COMMIT
*nat
:YEET_POSTROUTING -
-A POSTROUTING -j YEET_POSTROUTING
-A YEET_POSTROUTING -s %s ! -d %s -j MASQUERADE
COMMIT
`, spec.BridgeIf, spec.BridgeIf, spec.SubnetCIDR, spec.SubnetCIDR)
	default:
		return ""
	}
}

func EnsureFirewall(backend FirewallBackend, spec FirewallSpec) error {
	switch backend {
	case BackendNFT:
		return ensureNFTFirewall(spec)
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		return ensureIPTablesFirewall(backend, spec)
	default:
		return fmt.Errorf("unsupported firewall backend %q", backend)
	}
}

func VerifyFirewall(backend FirewallBackend, spec FirewallSpec) error {
	switch backend {
	case BackendNFT:
		return verifyNFTFirewall(spec)
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		return verifyIPTablesFirewall(backend, spec)
	default:
		return fmt.Errorf("unsupported firewall backend %q", backend)
	}
}

func ensureNFTFirewall(spec FirewallSpec) error {
	if err := deleteNFTTable(); err != nil {
		return err
	}
	return runCommandWithInput([]byte(RenderFirewallRules(BackendNFT, spec)), "nft", "-f", "-")
}

func ensureIPTablesFirewall(backend FirewallBackend, spec FirewallSpec) error {
	bin, err := iptablesBinary(backend)
	if err != nil {
		return err
	}
	return runFirewallSteps([]func() error{
		func() error { return ensureIPTablesChain(bin, "filter", "YEET_FORWARD") },
		func() error { return ensureIPTablesRule(bin, "filter", "FORWARD", "-j", "YEET_FORWARD") },
		func() error {
			return ensureIPTablesRule(bin, "filter", "YEET_FORWARD", "-i", spec.BridgeIf, "-j", "ACCEPT")
		},
		func() error {
			return ensureIPTablesRule(bin, "filter", "YEET_FORWARD", "-o", spec.BridgeIf, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		},
		func() error { return ensureIPTablesChain(bin, "nat", "YEET_POSTROUTING") },
		func() error { return ensureIPTablesRule(bin, "nat", "POSTROUTING", "-j", "YEET_POSTROUTING") },
		func() error {
			return ensureIPTablesRule(bin, "nat", "YEET_POSTROUTING", "-s", spec.SubnetCIDR, "!", "-d", spec.SubnetCIDR, "-j", "MASQUERADE")
		},
	})
}

func runFirewallSteps(steps []func() error) error {
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func verifyNFTFirewall(spec FirewallSpec) error {
	out, err := commandOutput("nft", "list", "table", "ip", "yeet")
	if err != nil {
		return err
	}
	return verifyOutputContains("nft firewall state", out, []string{
		"table ip yeet",
		spec.BridgeIf,
		spec.SubnetCIDR,
		"masquerade",
	})
}

func verifyOutputContains(label, out string, markers []string) error {
	for _, marker := range markers {
		if !strings.Contains(out, marker) {
			return fmt.Errorf("missing %q in %s", marker, label)
		}
	}
	return nil
}

type firewallStateCheck struct {
	args []string
	want string
	err  error
}

func verifyIPTablesFirewall(backend FirewallBackend, spec FirewallSpec) error {
	bin, err := iptablesBinary(backend)
	if err != nil {
		return err
	}
	return verifyFirewallStateChecks(bin, []firewallStateCheck{
		{
			args: []string{"-S", "YEET_FORWARD"},
			want: "-A YEET_FORWARD -i " + spec.BridgeIf + " -j ACCEPT",
			err:  errors.New("missing yeet forward ingress rule"),
		},
		{
			args: []string{"-S", "YEET_FORWARD"},
			want: "-A YEET_FORWARD -o " + spec.BridgeIf + " -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
			err:  errors.New("missing yeet forward return rule"),
		},
		{
			args: []string{"-t", "nat", "-S", "YEET_POSTROUTING"},
			want: fmt.Sprintf("-A YEET_POSTROUTING -s %s ! -d %s -j MASQUERADE", spec.SubnetCIDR, spec.SubnetCIDR),
			err:  errors.New("missing yeet postrouting masquerade rule"),
		},
		{
			args: []string{"-S", "FORWARD"},
			want: "-A FORWARD -j YEET_FORWARD",
			err:  errors.New("missing yeet forward jump rule"),
		},
		{
			args: []string{"-t", "nat", "-S", "POSTROUTING"},
			want: "-A POSTROUTING -j YEET_POSTROUTING",
			err:  errors.New("missing yeet postrouting jump rule"),
		},
	})
}

func verifyFirewallStateChecks(bin string, checks []firewallStateCheck) error {
	for _, check := range checks {
		out, err := commandOutput(bin, check.args...)
		if err != nil {
			return err
		}
		if !strings.Contains(out, check.want) {
			return check.err
		}
	}
	return nil
}

func CleanupFirewall(backend FirewallBackend) error {
	switch backend {
	case BackendNFT:
		return deleteNFTTable()
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		bin, err := iptablesBinary(backend)
		if err != nil {
			return err
		}
		if err := deleteIPTablesRuleIfPresent(bin, "filter", "FORWARD", "-j", "YEET_FORWARD"); err != nil {
			return err
		}
		if err := deleteIPTablesChain(bin, "filter", "YEET_FORWARD"); err != nil {
			return err
		}
		if err := deleteIPTablesRuleIfPresent(bin, "nat", "POSTROUTING", "-j", "YEET_POSTROUTING"); err != nil {
			return err
		}
		return deleteIPTablesChain(bin, "nat", "YEET_POSTROUTING")
	default:
		return fmt.Errorf("unsupported firewall backend %q", backend)
	}
}

func probeFirewallBackend() (probeResult, error) {
	var probe probeResult

	if hasUsableCommand("nft", "--version") {
		probe.HasNFT = true
	}
	for _, candidate := range []string{"iptables-nft", "iptables", "iptables-legacy"} {
		if !commandExists(candidate) {
			continue
		}
		out, err := runCombinedOutput(candidate, "--version")
		if err == nil {
			probe.IPTablesVersion = strings.TrimSpace(string(out))
			break
		}
	}
	if DetectFirewallBackendFromProbe(probe) == "" {
		return probe, fmt.Errorf("no usable firewall backend found")
	}
	return probe, nil
}

func deleteNFTTable() error {
	if !commandExists("nft") {
		return fmt.Errorf("nft command not found")
	}
	exists, err := nftTableExists()
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return runCommandWithInput(nil, "nft", "delete", "table", "ip", "yeet")
}

func ensureIPTablesChain(bin, table, chain string) error {
	if _, err := commandOutput(bin, "-t", table, "-S", chain); err == nil {
		return nil
	}
	return runCommandWithInput(nil, bin, "-t", table, "-N", chain)
}

func deleteIPTablesChain(bin, table, chain string) error {
	if _, err := commandOutput(bin, "-t", table, "-S", chain); err != nil {
		return nil
	}
	if err := runCommandWithInput(nil, bin, "-t", table, "-F", chain); err != nil {
		return err
	}
	return runCommandWithInput(nil, bin, "-t", table, "-X", chain)
}

func ensureIPTablesRule(bin, table, chain string, rule ...string) error {
	args := append([]string{"-t", table, "-C", chain}, rule...)
	if _, err := commandOutput(bin, args...); err == nil {
		return nil
	}
	args = append([]string{"-t", table, "-A", chain}, rule...)
	return runCommandWithInput(nil, bin, args...)
}

func deleteIPTablesRuleIfPresent(bin, table, chain string, rule ...string) error {
	args := append([]string{"-t", table, "-C", chain}, rule...)
	if _, err := commandOutput(bin, args...); err != nil {
		return nil
	}
	args = append([]string{"-t", table, "-D", chain}, rule...)
	return runCommandWithInput(nil, bin, args...)
}

func iptablesBinary(backend FirewallBackend) (string, error) {
	switch backend {
	case BackendIPTablesNFT:
		for _, candidate := range []string{"iptables-nft", "iptables"} {
			if version, err := iptablesVersion(candidate); err == nil && strings.Contains(strings.ToLower(version), "nf_tables") {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("iptables nft backend requested but no iptables binary found")
	case BackendIPTablesLegacy:
		for _, candidate := range []string{"iptables-legacy", "iptables"} {
			if version, err := iptablesVersion(candidate); err == nil && strings.Contains(strings.ToLower(version), "legacy") {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("iptables legacy backend requested but no iptables binary found")
	default:
		return "", fmt.Errorf("iptables binary lookup is unsupported for firewall backend %q", backend)
	}
}

func commandExists(name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func hasUsableCommand(name string, args ...string) bool {
	if !commandExists(name) {
		return false
	}
	_, err := runCombinedOutput(name, args...)
	return err == nil
}

func iptablesVersion(name string) (string, error) {
	if !commandExists(name) {
		return "", fmt.Errorf("%s command not found", name)
	}
	out, err := runCombinedOutput(name, "--version")
	if err != nil {
		return "", fmt.Errorf("failed to run %s --version: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func nftTableExists() (bool, error) {
	out, err := runCombinedOutput("nft", "list", "table", "ip", "yeet")
	if err == nil {
		return true, nil
	}
	msg := strings.TrimSpace(string(out))
	if strings.Contains(strings.ToLower(msg), "no such file") {
		return false, nil
	}
	if msg == "" {
		return false, fmt.Errorf("failed to inspect nft table ip yeet: %w", err)
	}
	return false, fmt.Errorf("failed to inspect nft table ip yeet: %w\n%s", err, msg)
}

func commandOutput(name string, args ...string) (string, error) {
	out, err := runCombinedOutput(name, args...)
	msg := strings.TrimSpace(string(out))
	if err != nil {
		if msg == "" {
			return "", fmt.Errorf("failed to run %s %s: %w", name, strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("failed to run %s %s: %w\n%s", name, strings.Join(args, " "), err, msg)
	}
	return msg, nil
}
