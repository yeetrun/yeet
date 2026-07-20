// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/iso"
)

const (
	isoNFTTable          = "yeet_iso"
	isoIPSetSources      = "yeet_iso_sources"
	isoIPSetNonPublic    = "yeet_iso_nonpublic"
	isoIPTablesMangle    = "YEET_ISO_MANGLE"
	isoIPTablesInput     = "YEET_ISO_INPUT"
	isoIPTablesForward   = "YEET_ISO_FORWARD"
	isoIPTablesPreRoute  = "YEET_ISO_PREROUTE"
	isoIPTablesPostRoute = "YEET_ISO_POSTROUTE"
)

var isoInterfaceNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// ISOEndpoint describes one root-side attachment protected by the global ISO
// policy. Project is empty for VM TAP endpoints.
type ISOEndpoint struct {
	Interface string
	Link      netip.Prefix
	PeerIP    netip.Addr
	Project   netip.Prefix
	Tailscale bool
}

// ISOPolicySpec is the backend-neutral input to the host ISO firewall.
type ISOPolicySpec struct {
	Pool      netip.Prefix
	DNSPort   uint16
	Endpoints []ISOEndpoint
}

// ISOPolicyRules contains the complete desired state for one firewall
// backend. The strings are deliberately inspectable and are applied only to
// Yeet-owned tables, chains, and sets.
type ISOPolicyRules struct {
	Backend   FirewallBackend
	IPv4      string
	IPv6      string
	IPSet     string
	Decisions []string
	Digest    string
}

type isoCommandRunner func(context.Context, []byte, string, ...string) ([]byte, error)

var runISOCommand isoCommandRunner = func(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(input) != 0 {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			return out, fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
		}
		return out, fmt.Errorf("run %s %s: %w: %s", name, strings.Join(args, " "), err, message)
	}
	return out, nil
}

// RenderISOPolicy renders equivalent fail-closed policy for every supported
// backend. Endpoint order never affects the result or digest.
func RenderISOPolicy(backend FirewallBackend, spec ISOPolicySpec) (ISOPolicyRules, error) {
	if err := validateISOPolicySpec(spec); err != nil {
		return ISOPolicyRules{}, err
	}
	endpoints := append([]ISOEndpoint(nil), spec.Endpoints...)
	if err := validateISOEndpoints(spec.Pool, endpoints); err != nil {
		return ISOPolicyRules{}, err
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Interface < endpoints[j].Interface })
	spec.Endpoints = endpoints

	rules, err := renderISOPolicyBackend(backend, spec)
	if err != nil {
		return ISOPolicyRules{}, err
	}
	rules.Backend = backend
	rules.Decisions = []string{
		"drop-invalid", "drop-spoof", "accept-established", "accept-iso-dns",
		"reject-new-host-access", "reject-direct-dns", "reject-non-public",
		"accept-public", "reject-rest", "masquerade-public", "drop-ipv6",
	}
	rules.Digest = digestISOPolicy(rules)
	return rules, nil
}

func validateISOPolicySpec(spec ISOPolicySpec) error {
	pool := spec.Pool
	if !pool.IsValid() || !pool.Addr().Is4() || pool.Bits() != 16 || pool != pool.Masked() {
		return fmt.Errorf("ISO policy requires a canonical IPv4 /16")
	}
	if spec.DNSPort == 0 {
		return fmt.Errorf("ISO DNS port is required")
	}
	return nil
}

func renderISOPolicyBackend(backend FirewallBackend, spec ISOPolicySpec) (ISOPolicyRules, error) {
	switch backend {
	case BackendNFT:
		return renderNFTISOPolicy(spec)
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		return renderIPTablesISOPolicy(backend, spec)
	default:
		return ISOPolicyRules{}, fmt.Errorf("unsupported ISO firewall backend %q", backend)
	}
}

func validateISOEndpoints(pool netip.Prefix, endpoints []ISOEndpoint) error {
	layout, err := iso.NewLayout(pool)
	if err != nil {
		return err
	}
	seen := newISOEndpointIdentitySet(len(endpoints))
	for _, endpoint := range endpoints {
		if !isoInterfaceNameRE.MatchString(endpoint.Interface) {
			return fmt.Errorf("invalid ISO interface %q", endpoint.Interface)
		}
		if err := validateISOEndpointNetwork(layout, endpoint); err != nil {
			return err
		}
		if err := seen.add(endpoint); err != nil {
			return err
		}
	}
	return nil
}

type isoEndpointIdentitySet struct {
	interfaces map[string]string
	links      map[netip.Prefix]string
	peers      map[netip.Addr]string
	projects   map[netip.Prefix]string
}

func newISOEndpointIdentitySet(size int) isoEndpointIdentitySet {
	return isoEndpointIdentitySet{
		interfaces: make(map[string]string, size),
		links:      make(map[netip.Prefix]string, size),
		peers:      make(map[netip.Addr]string, size),
		projects:   make(map[netip.Prefix]string, size),
	}
}

func (seen isoEndpointIdentitySet) add(endpoint ISOEndpoint) error {
	if owner, exists := seen.interfaces[endpoint.Interface]; exists {
		return fmt.Errorf("duplicate ISO interface %q already used by %q", endpoint.Interface, owner)
	}
	seen.interfaces[endpoint.Interface] = endpoint.Interface
	if owner, exists := seen.links[endpoint.Link]; exists {
		return fmt.Errorf("duplicate ISO link %s on interfaces %q and %q", endpoint.Link, owner, endpoint.Interface)
	}
	seen.links[endpoint.Link] = endpoint.Interface
	if owner, exists := seen.peers[endpoint.PeerIP]; exists {
		return fmt.Errorf("duplicate ISO peer %s on interfaces %q and %q", endpoint.PeerIP, owner, endpoint.Interface)
	}
	seen.peers[endpoint.PeerIP] = endpoint.Interface
	if endpoint.Project.IsValid() {
		if owner, exists := seen.projects[endpoint.Project]; exists {
			return fmt.Errorf("duplicate ISO project %s on interfaces %q and %q", endpoint.Project, owner, endpoint.Interface)
		}
		seen.projects[endpoint.Project] = endpoint.Interface
	}
	return nil
}

func validateISOEndpointNetwork(layout iso.Layout, endpoint ISOEndpoint) error {
	link := endpoint.Link.Masked()
	if !link.IsValid() || !link.Addr().Is4() || link.Bits() != 30 || !layout.Links.Contains(link.Addr()) || endpoint.Link != link {
		return fmt.Errorf("ISO endpoint %s requires a canonical in-pool /30 link", endpoint.Interface)
	}
	if endpoint.PeerIP != link.Addr().Next().Next() {
		return fmt.Errorf("ISO endpoint %s peer must be link host two", endpoint.Interface)
	}
	return validateISOEndpointProject(layout, endpoint)
}

func validateISOEndpointProject(layout iso.Layout, endpoint ISOEndpoint) error {
	if !endpoint.Project.IsValid() {
		return nil
	}
	project := endpoint.Project.Masked()
	if !project.Addr().Is4() || project.Bits() != 27 || endpoint.Project != project || !layout.Projects.Contains(project.Addr()) {
		return fmt.Errorf("ISO endpoint %s requires a canonical in-pool /27 project", endpoint.Interface)
	}
	return nil
}

func renderNFTISOPolicy(spec ISOPolicySpec) (ISOPolicyRules, error) {
	interfaces := nftQuotedInterfaces(spec.Endpoints)
	nonPublic := prefixStrings(compactISOPrefixes(iso.NonPublicIPv4Prefixes(spec.Pool)))
	var ipv4 strings.Builder
	fmt.Fprintf(&ipv4, "table ip %s {\n", isoNFTTable)
	writeNFTInterfaceSet(&ipv4, interfaces)
	fmt.Fprintf(&ipv4, "  set non_public { type ipv4_addr; flags interval; elements = { %s } }\n", strings.Join(nonPublic, ", "))
	ipv4.WriteString("  chain prerouting_mangle { type filter hook prerouting priority -160; policy accept;\n")
	ipv4.WriteString("    iifname @interfaces ct state invalid drop\n")
	for _, endpoint := range spec.Endpoints {
		allowed := []string{netip.PrefixFrom(endpoint.PeerIP, 32).String()}
		if endpoint.Project.IsValid() {
			allowed = append(allowed, endpoint.Project.String())
		}
		fmt.Fprintf(&ipv4, "    iifname %q ip saddr != { %s } drop\n", endpoint.Interface, strings.Join(allowed, ", "))
	}
	ipv4.WriteString("  }\n")
	ipv4.WriteString("  chain prerouting_nat { type nat hook prerouting priority -110; policy accept;\n")
	fmt.Fprintf(&ipv4, "    iifname @interfaces udp dport 53 redirect to :%d\n", spec.DNSPort)
	fmt.Fprintf(&ipv4, "    iifname @interfaces tcp dport 53 redirect to :%d\n", spec.DNSPort)
	ipv4.WriteString("  }\n")
	ipv4.WriteString("  chain input { type filter hook input priority -10; policy accept;\n")
	ipv4.WriteString("    iifname @interfaces ct state established,related accept\n")
	fmt.Fprintf(&ipv4, "    iifname @interfaces udp dport %d ct original proto-dst 53 accept\n", spec.DNSPort)
	fmt.Fprintf(&ipv4, "    iifname @interfaces tcp dport %d ct original proto-dst 53 accept\n", spec.DNSPort)
	ipv4.WriteString("    iifname @interfaces reject with icmp type admin-prohibited\n")
	ipv4.WriteString("  }\n")
	ipv4.WriteString("  chain forward { type filter hook forward priority -10; policy accept;\n")
	ipv4.WriteString("    iifname @interfaces ct state established,related accept\n")
	ipv4.WriteString("    iifname @interfaces tcp dport { 53, 853 } reject with icmp type admin-prohibited\n")
	ipv4.WriteString("    iifname @interfaces udp dport { 53, 853 } reject with icmp type admin-prohibited\n")
	ipv4.WriteString("    iifname @interfaces ip daddr @non_public reject with icmp type admin-prohibited\n")
	ipv4.WriteString("    iifname @interfaces ip daddr != @non_public accept\n")
	ipv4.WriteString("    iifname @interfaces reject with icmp type admin-prohibited\n")
	ipv4.WriteString("    oifname @interfaces ct state established,related accept\n")
	ipv4.WriteString("    oifname @interfaces reject with icmp type admin-prohibited\n")
	ipv4.WriteString("  }\n")
	ipv4.WriteString("  chain postrouting { type nat hook postrouting priority 90; policy accept;\n")
	fmt.Fprintf(&ipv4, "    ip saddr %s ip daddr != @non_public masquerade # MASQUERADE public IPv4 only\n", spec.Pool)
	ipv4.WriteString("  }\n}\n")

	var ipv6 strings.Builder
	fmt.Fprintf(&ipv6, "table ip6 %s {\n", isoNFTTable)
	writeNFTInterfaceSet(&ipv6, interfaces)
	ipv6.WriteString("  chain input { type filter hook input priority -10; policy accept; iifname @interfaces drop; }\n")
	ipv6.WriteString("  chain forward { type filter hook forward priority -10; policy accept; iifname @interfaces drop; oifname @interfaces drop; }\n")
	ipv6.WriteString("}\n")
	return ISOPolicyRules{IPv4: ipv4.String(), IPv6: ipv6.String()}, nil
}

func writeNFTInterfaceSet(out *strings.Builder, interfaces []string) {
	if len(interfaces) == 0 {
		out.WriteString("  set interfaces { type ifname; }\n")
		return
	}
	fmt.Fprintf(out, "  set interfaces { type ifname; elements = { %s } }\n", strings.Join(interfaces, ", "))
}

func nftQuotedInterfaces(endpoints []ISOEndpoint) []string {
	out := make([]string, len(endpoints))
	for index, endpoint := range endpoints {
		out[index] = fmt.Sprintf("%q", endpoint.Interface)
	}
	return out
}

func prefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, prefix.Masked().String())
	}
	return out
}

func compactISOPrefixes(prefixes []netip.Prefix) []netip.Prefix {
	canonical := canonicalISOIPv4Prefixes(prefixes)
	sort.Slice(canonical, func(i, j int) bool { return lessISOPrefix(canonical[i], canonical[j]) })
	out := make([]netip.Prefix, 0, len(canonical))
	for _, candidate := range canonical {
		if !isoPrefixCovered(out, candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func canonicalISOIPv4Prefixes(prefixes []netip.Prefix) []netip.Prefix {
	canonical := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = prefix.Masked()
		if prefix.IsValid() && prefix.Addr().Is4() {
			canonical = append(canonical, prefix)
		}
	}
	return canonical
}

func lessISOPrefix(leftPrefix, rightPrefix netip.Prefix) bool {
	left, right := leftPrefix.Addr().As4(), rightPrefix.Addr().As4()
	for index := range left {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return leftPrefix.Bits() < rightPrefix.Bits()
}

func isoPrefixCovered(existingPrefixes []netip.Prefix, candidate netip.Prefix) bool {
	for _, existing := range existingPrefixes {
		if existing.Bits() <= candidate.Bits() && existing.Contains(candidate.Addr()) {
			return true
		}
	}
	return false
}

func renderIPTablesISOPolicy(backend FirewallBackend, spec ISOPolicySpec) (ISOPolicyRules, error) {
	var sets strings.Builder
	fmt.Fprintf(&sets, "create %s hash:net,iface family inet\n", isoIPSetSources)
	for _, endpoint := range spec.Endpoints {
		writeISOIPSetEndpoint(&sets, endpoint)
	}
	fmt.Fprintf(&sets, "create %s hash:net family inet\n", isoIPSetNonPublic)
	for _, prefix := range prefixStrings(compactISOPrefixes(iso.NonPublicIPv4Prefixes(spec.Pool))) {
		fmt.Fprintf(&sets, "add %s %s\n", isoIPSetNonPublic, prefix)
	}

	var ipv4 strings.Builder
	for _, endpoint := range spec.Endpoints {
		writeIPTablesISOEndpointComment(&ipv4, endpoint)
	}
	fmt.Fprintf(&ipv4, "# ISO non-public destinations %s\n", strings.Join(prefixStrings(compactISOPrefixes(iso.NonPublicIPv4Prefixes(spec.Pool))), ","))
	ipv4.WriteString("*mangle\n")
	fmt.Fprintf(&ipv4, ":%s - [0:0]\n-F %s\n-A PREROUTING -j %s\n", isoIPTablesMangle, isoIPTablesMangle, isoIPTablesMangle)
	for _, endpoint := range spec.Endpoints {
		fmt.Fprintf(&ipv4, "-A %s -i %s -m conntrack --ctstate INVALID -j DROP\n", isoIPTablesMangle, endpoint.Interface)
		fmt.Fprintf(&ipv4, "-A %s -i %s -m set --match-set %s src,src -j RETURN\n", isoIPTablesMangle, endpoint.Interface, isoIPSetSources)
		fmt.Fprintf(&ipv4, "-A %s -i %s -j DROP\n", isoIPTablesMangle, endpoint.Interface)
	}
	ipv4.WriteString("COMMIT\n*nat\n")
	fmt.Fprintf(&ipv4, ":%s - [0:0]\n:%s - [0:0]\n-F %s\n-F %s\n", isoIPTablesPreRoute, isoIPTablesPostRoute, isoIPTablesPreRoute, isoIPTablesPostRoute)
	fmt.Fprintf(&ipv4, "-A PREROUTING -j %s\n-A POSTROUTING -j %s\n", isoIPTablesPreRoute, isoIPTablesPostRoute)
	for _, endpoint := range spec.Endpoints {
		fmt.Fprintf(&ipv4, "-A %s -i %s -p udp --dport 53 -j REDIRECT --to-ports %d\n", isoIPTablesPreRoute, endpoint.Interface, spec.DNSPort)
		fmt.Fprintf(&ipv4, "-A %s -i %s -p tcp --dport 53 -j REDIRECT --to-ports %d\n", isoIPTablesPreRoute, endpoint.Interface, spec.DNSPort)
	}
	fmt.Fprintf(&ipv4, "-A %s -s %s -m set ! --match-set %s dst -j MASQUERADE\n", isoIPTablesPostRoute, spec.Pool, isoIPSetNonPublic)
	ipv4.WriteString("COMMIT\n*filter\n")
	fmt.Fprintf(&ipv4, ":%s - [0:0]\n:%s - [0:0]\n-F %s\n-F %s\n", isoIPTablesInput, isoIPTablesForward, isoIPTablesInput, isoIPTablesForward)
	fmt.Fprintf(&ipv4, "-A INPUT -j %s\n-A FORWARD -j %s\n", isoIPTablesInput, isoIPTablesForward)
	for _, endpoint := range spec.Endpoints {
		fmt.Fprintf(&ipv4, "-A %s -i %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n", isoIPTablesInput, endpoint.Interface)
		fmt.Fprintf(&ipv4, "-A %s -i %s -p udp --dport %d -m conntrack --ctorigdstport 53 -j ACCEPT\n", isoIPTablesInput, endpoint.Interface, spec.DNSPort)
		fmt.Fprintf(&ipv4, "-A %s -i %s -p tcp --dport %d -m conntrack --ctorigdstport 53 -j ACCEPT\n", isoIPTablesInput, endpoint.Interface, spec.DNSPort)
		fmt.Fprintf(&ipv4, "-A %s -i %s -j REJECT --reject-with icmp-admin-prohibited\n", isoIPTablesInput, endpoint.Interface)

		fmt.Fprintf(&ipv4, "-A %s -i %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n", isoIPTablesForward, endpoint.Interface)
		fmt.Fprintf(&ipv4, "-A %s -i %s -p tcp -m multiport --dports 53,853 -j REJECT --reject-with icmp-admin-prohibited\n", isoIPTablesForward, endpoint.Interface)
		fmt.Fprintf(&ipv4, "-A %s -i %s -p udp -m multiport --dports 53,853 -j REJECT --reject-with icmp-admin-prohibited\n", isoIPTablesForward, endpoint.Interface)
		fmt.Fprintf(&ipv4, "-A %s -i %s -m set --match-set %s dst -j REJECT --reject-with icmp-admin-prohibited\n", isoIPTablesForward, endpoint.Interface, isoIPSetNonPublic)
		fmt.Fprintf(&ipv4, "-A %s -i %s -m set ! --match-set %s dst -j ACCEPT\n", isoIPTablesForward, endpoint.Interface, isoIPSetNonPublic)
		fmt.Fprintf(&ipv4, "-A %s -i %s -j REJECT --reject-with icmp-admin-prohibited\n", isoIPTablesForward, endpoint.Interface)
	}
	for _, endpoint := range spec.Endpoints {
		fmt.Fprintf(&ipv4, "-A %s -o %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n", isoIPTablesForward, endpoint.Interface)
		fmt.Fprintf(&ipv4, "-A %s -o %s -j REJECT --reject-with icmp-admin-prohibited\n", isoIPTablesForward, endpoint.Interface)
	}
	ipv4.WriteString("COMMIT\n")

	var ipv6 strings.Builder
	ipv6.WriteString("*filter\n")
	fmt.Fprintf(&ipv6, ":%s - [0:0]\n:%s - [0:0]\n-F %s\n-F %s\n", isoIPTablesInput, isoIPTablesForward, isoIPTablesInput, isoIPTablesForward)
	fmt.Fprintf(&ipv6, "-A INPUT -j %s\n-A FORWARD -j %s\n", isoIPTablesInput, isoIPTablesForward)
	for _, endpoint := range spec.Endpoints {
		fmt.Fprintf(&ipv6, "-A %s -i %s -j DROP\n", isoIPTablesInput, endpoint.Interface)
		fmt.Fprintf(&ipv6, "-A %s -i %s -j DROP\n", isoIPTablesForward, endpoint.Interface)
		fmt.Fprintf(&ipv6, "-A %s -o %s -j DROP\n", isoIPTablesForward, endpoint.Interface)
	}
	ipv6.WriteString("COMMIT\n")
	return ISOPolicyRules{Backend: backend, IPv4: ipv4.String(), IPv6: ipv6.String(), IPSet: sets.String()}, nil
}

func writeISOIPSetEndpoint(out *strings.Builder, endpoint ISOEndpoint) {
	fmt.Fprintf(out, "add %s %s,%s\n", isoIPSetSources, netip.PrefixFrom(endpoint.PeerIP, 32), endpoint.Interface)
	if endpoint.Project.IsValid() {
		fmt.Fprintf(out, "add %s %s,%s\n", isoIPSetSources, endpoint.Project, endpoint.Interface)
	}
}

func writeIPTablesISOEndpointComment(out *strings.Builder, endpoint ISOEndpoint) {
	fmt.Fprintf(out, "# ISO endpoint %s source %s", endpoint.Interface, netip.PrefixFrom(endpoint.PeerIP, 32))
	if endpoint.Project.IsValid() {
		fmt.Fprintf(out, " project %s", endpoint.Project)
	}
	out.WriteByte('\n')
}

// EnsureISOPolicy atomically replaces Yeet-owned policy objects and then
// verifies their complete live digest. Any missing tool or mismatch is fatal.
func EnsureISOPolicy(ctx context.Context, rules ISOPolicyRules) error {
	if rules.Digest == "" || rules.Digest != digestISOPolicy(rules) {
		return fmt.Errorf("ISO firewall policy has an invalid desired digest")
	}
	if err := applyISOIPSets(ctx, rules); err != nil {
		return err
	}
	if err := applyISOIPv4(ctx, rules); err != nil {
		return err
	}
	if err := applyISOIPv6(ctx, rules); err != nil {
		return err
	}
	return VerifyISOPolicy(ctx, rules)
}

func applyISOIPSets(ctx context.Context, rules ISOPolicyRules) error {
	if rules.Backend == BackendNFT {
		return nil
	}
	if rules.Backend != BackendIPTablesNFT && rules.Backend != BackendIPTablesLegacy {
		return fmt.Errorf("unsupported ISO firewall backend %q", rules.Backend)
	}
	script, err := renderAtomicISOIPSetRestore(rules.IPSet)
	if err != nil {
		return err
	}
	_, err = runISOCommand(ctx, []byte(script), "ipset", "restore", "-exist")
	if err != nil {
		return fmt.Errorf("apply ISO ipsets: %w", err)
	}
	return nil
}

func renderAtomicISOIPSetRestore(desired string) (string, error) {
	creates, entries, err := parseISOIPSetRestore(desired)
	if err != nil {
		return "", err
	}
	return renderISOIPSetSwaps(creates, entries)
}

func parseISOIPSetRestore(desired string) (map[string]string, map[string][]string, error) {
	entries := map[string][]string{isoIPSetSources: nil, isoIPSetNonPublic: nil}
	creates := map[string]string{}
	for _, raw := range strings.Split(desired, "\n") {
		line := strings.TrimSpace(raw)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "create":
			if len(fields) < 3 {
				return nil, nil, fmt.Errorf("invalid ISO ipset create line %q", line)
			}
			creates[fields[1]] = strings.Join(fields[2:], " ")
		case "add":
			if len(fields) < 3 {
				return nil, nil, fmt.Errorf("invalid ISO ipset add line %q", line)
			}
			entries[fields[1]] = append(entries[fields[1]], strings.Join(fields[2:], " "))
		default:
			return nil, nil, fmt.Errorf("invalid ISO ipset command %q", line)
		}
	}
	return creates, entries, nil
}

func renderISOIPSetSwaps(creates map[string]string, entries map[string][]string) (string, error) {
	var script strings.Builder
	for _, name := range []string{isoIPSetSources, isoIPSetNonPublic} {
		kind, ok := creates[name]
		if !ok {
			return "", fmt.Errorf("ISO ipset %s is missing", name)
		}
		temp := name + "_next"
		fmt.Fprintf(&script, "create %s %s\ncreate %s %s\nflush %s\n", name, kind, temp, kind, temp)
		for _, entry := range entries[name] {
			fmt.Fprintf(&script, "add %s %s\n", temp, entry)
		}
		fmt.Fprintf(&script, "swap %s %s\ndestroy %s\n", temp, name, temp)
	}
	return script.String(), nil
}

func applyISOIPv4(ctx context.Context, rules ISOPolicyRules) error {
	switch rules.Backend {
	case BackendNFT:
		if err := applyISONFTTable(ctx, "ip", rules.IPv4); err != nil {
			return fmt.Errorf("apply ISO nft IPv4 policy: %w", err)
		}
		return nil
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		restore, _, err := isoIPTablesTools(rules.Backend, false)
		if err != nil {
			return err
		}
		if _, err := runISOCommand(ctx, []byte(iptablesRestoreOwnedChains(rules.IPv4)), restore, iptablesWaitArg, "--noflush"); err != nil {
			return fmt.Errorf("apply ISO IPv4 policy: %w", err)
		}
		return ensureISOIPTablesJumps(ctx, rules.Backend, false)
	default:
		return fmt.Errorf("unsupported ISO firewall backend %q", rules.Backend)
	}
}

func applyISOIPv6(ctx context.Context, rules ISOPolicyRules) error {
	switch rules.Backend {
	case BackendNFT:
		if err := applyISONFTTable(ctx, "ip6", rules.IPv6); err != nil {
			return fmt.Errorf("apply ISO nft IPv6 policy: %w", err)
		}
		return nil
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		restore, _, err := isoIPTablesTools(rules.Backend, true)
		if err != nil {
			return err
		}
		if _, err := runISOCommand(ctx, []byte(iptablesRestoreOwnedChains(rules.IPv6)), restore, iptablesWaitArg, "--noflush"); err != nil {
			return fmt.Errorf("apply ISO IPv6 policy: %w", err)
		}
		return ensureISOIPTablesJumps(ctx, rules.Backend, true)
	default:
		return fmt.Errorf("unsupported ISO firewall backend %q", rules.Backend)
	}
}

func applyISONFTTable(ctx context.Context, family, rendered string) error {
	return applyISONamedNFTTable(ctx, "nft", nil, family, isoNFTTable, rendered)
}

func applyISONamedNFTTable(ctx context.Context, name string, prefix []string, family, table, rendered string) error {
	present, err := isoNamedNFTTableExists(ctx, name, prefix, family, table)
	if err != nil {
		return err
	}
	input := rendered
	if present {
		input = fmt.Sprintf("delete table %s %s\n%s", family, table, rendered)
	}
	args := append(append([]string(nil), prefix...), "-f", "-")
	if _, err := runISOCommand(ctx, []byte(input), name, args...); err != nil {
		return err
	}
	return nil
}

func isoNamedNFTTableExists(ctx context.Context, name string, prefix []string, family, table string) (bool, error) {
	args := append(append([]string(nil), prefix...), "list", "table", family, table)
	_, err := runISOCommand(ctx, nil, name, args...)
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "no such file or directory") {
		return false, nil
	}
	return false, fmt.Errorf("probe ISO nft %s table %s: %w", family, table, err)
}

func iptablesRestoreOwnedChains(rendered string) string {
	var out strings.Builder
	for _, raw := range strings.Split(rendered, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if isISOBuiltinJump(line) {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func isISOBuiltinJump(line string) bool {
	for _, prefix := range []string{"-A PREROUTING -j YEET_ISO_", "-A POSTROUTING -j YEET_ISO_", "-A INPUT -j YEET_ISO_", "-A FORWARD -j YEET_ISO_"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func isoIPTablesTools(backend FirewallBackend, ipv6 bool) (restore, save string, err error) {
	prefix := "iptables"
	if ipv6 {
		prefix = "ip6tables"
	}
	suffix := ""
	switch backend {
	case BackendIPTablesNFT:
		suffix = "-nft"
	case BackendIPTablesLegacy:
		suffix = "-legacy"
	default:
		return "", "", fmt.Errorf("unsupported iptables backend %q", backend)
	}
	return prefix + suffix + "-restore", prefix + suffix + "-save", nil
}

func ensureISOIPTablesJumps(ctx context.Context, backend FirewallBackend, ipv6 bool) error {
	bin, err := iptablesBinary(backend)
	if err != nil {
		return err
	}
	if ipv6 {
		bin = strings.Replace(bin, "iptables", "ip6tables", 1)
	}
	type jump struct{ table, chain, target string }
	jumps := []jump{{"filter", "INPUT", isoIPTablesInput}, {"filter", "FORWARD", isoIPTablesForward}}
	if !ipv6 {
		jumps = append(jumps,
			jump{"mangle", "PREROUTING", isoIPTablesMangle},
			jump{"nat", "PREROUTING", isoIPTablesPreRoute},
			jump{"nat", "POSTROUTING", isoIPTablesPostRoute},
		)
	}
	for _, item := range jumps {
		if err := reconcileISOIPTablesJump(ctx, bin, nil, item.table, item.chain, item.target); err != nil {
			return err
		}
	}
	return nil
}

func reconcileISOIPTablesJump(ctx context.Context, name string, prefix []string, table, chain, target string) error {
	args := func(operation ...string) []string {
		out := append([]string(nil), prefix...)
		out = append(out, iptablesWaitArg, "-t", table)
		return append(out, operation...)
	}
	out, err := runISOCommand(ctx, nil, name, args("-S", chain)...)
	if err != nil {
		return fmt.Errorf("inspect ISO jump %s/%s: %w", table, chain, err)
	}
	count, first := isoIPTablesJumpState(string(out), chain, target)
	if count == 1 && first {
		return nil
	}
	for range count {
		if _, err := runISOCommand(ctx, nil, name, args("-D", chain, "-j", target)...); err != nil {
			return fmt.Errorf("remove stale ISO jump %s/%s: %w", table, chain, err)
		}
	}
	if _, err := runISOCommand(ctx, nil, name, args("-I", chain, "1", "-j", target)...); err != nil {
		return fmt.Errorf("install ISO jump %s/%s: %w", table, chain, err)
	}
	return nil
}

func isoIPTablesJumpState(raw, chain, target string) (count int, first bool) {
	ruleIndex := 0
	want := "-A " + chain + " -j " + target
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" || fields[1] != chain {
			continue
		}
		if strings.Join(fields, " ") == want {
			count++
			if ruleIndex == 0 {
				first = true
			}
		}
		ruleIndex++
	}
	return count, first
}

func firstNonEmptyLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return strings.Join(strings.Fields(line), " ")
		}
	}
	return ""
}

// VerifyISOPolicy reads the Yeet-owned live objects and requires their full
// normalized digest to equal the rendered desired state.
func VerifyISOPolicy(ctx context.Context, want ISOPolicyRules) error {
	live, err := readLiveISOPolicy(ctx, want.Backend)
	if err != nil {
		return err
	}
	if got := digestISOPolicy(live); got != want.Digest {
		return fmt.Errorf("ISO firewall policy digest mismatch: got %s want %s", got, want.Digest)
	}
	return nil
}

func readLiveISOPolicy(ctx context.Context, backend FirewallBackend) (ISOPolicyRules, error) {
	live := ISOPolicyRules{Backend: backend}
	var err error
	switch backend {
	case BackendNFT:
		live.IPv4, err = readISOCommandText(ctx, "nft", "list", "table", "ip", isoNFTTable)
		if err != nil {
			return ISOPolicyRules{}, fmt.Errorf("read live ISO nft IPv4 policy: %w", err)
		}
		live.IPv6, err = readISOCommandText(ctx, "nft", "list", "table", "ip6", isoNFTTable)
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		_, save4, toolsErr := isoIPTablesTools(backend, false)
		if toolsErr != nil {
			return ISOPolicyRules{}, toolsErr
		}
		_, save6, toolsErr := isoIPTablesTools(backend, true)
		if toolsErr != nil {
			return ISOPolicyRules{}, toolsErr
		}
		live.IPv4, err = readISOCommandText(ctx, save4)
		if err == nil {
			live.IPv6, err = readISOCommandText(ctx, save6)
		}
		if err == nil {
			live.IPSet, err = readISOCommandText(ctx, "ipset", "save")
		}
	default:
		return ISOPolicyRules{}, fmt.Errorf("unsupported ISO firewall backend %q", backend)
	}
	if err != nil {
		return ISOPolicyRules{}, fmt.Errorf("read live ISO firewall policy: %w", err)
	}
	return live, nil
}

func readISOCommandText(ctx context.Context, name string, args ...string) (string, error) {
	out, err := runISOCommand(ctx, nil, name, args...)
	return string(out), err
}

func digestISOPolicy(rules ISOPolicyRules) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", rules.Backend,
		canonicalISOFirewallText(rules.Backend, rules.IPv4),
		canonicalISOFirewallText(rules.Backend, rules.IPv6),
		canonicalISOIPSetText(rules.IPSet),
	)
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalISOFirewallText(backend FirewallBackend, raw string) string {
	if backend == BackendNFT {
		return canonicalNFTText(raw)
	}
	return canonicalIPTablesText(raw)
}

func canonicalNFTText(raw string) string {
	var fields []string
	for _, line := range strings.Split(raw, "\n") {
		line, _, _ = strings.Cut(line, "#")
		lineFields := canonicalNFTPriority(strings.Fields(line))
		for index := 0; index < len(lineFields); index++ {
			if lineFields[index] == "handle" && index+1 < len(lineFields) {
				index++
				continue
			}
			token := strings.TrimSuffix(lineFields[index], ";")
			if token == "" || token == "type" && len(fields) != 0 && fields[len(fields)-1] == "icmp" {
				continue
			}
			fields = append(fields, canonicalNFTAddressToken(token))
		}
	}
	return strings.Join(fields, " ")
}

func canonicalNFTAddressToken(token string) string {
	core := strings.TrimSuffix(token, ",")
	suffix := strings.TrimPrefix(token, core)
	prefix, err := netip.ParsePrefix(core)
	if err == nil && prefix.Bits() == prefix.Addr().BitLen() {
		return prefix.Addr().String() + suffix
	}
	return token
}

func canonicalNFTPriority(fields []string) []string {
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] != "priority" {
			continue
		}
		name := strings.TrimSuffix(fields[index+1], ";")
		base, ok := map[string]int{
			"raw": -300, "conntrack": -200, "mangle": -150, "dstnat": -100,
			"filter": 0, "security": 50, "srcnat": 100,
		}[name]
		if !ok {
			continue
		}
		end := index + 2
		value := base
		semicolon := strings.HasSuffix(fields[index+1], ";")
		if end+1 < len(fields) && (fields[end] == "+" || fields[end] == "-") {
			offsetToken := fields[end+1]
			offset, err := strconv.Atoi(strings.TrimSuffix(offsetToken, ";"))
			if err != nil {
				continue
			}
			if fields[end] == "-" {
				value -= offset
			} else {
				value += offset
			}
			semicolon = strings.HasSuffix(offsetToken, ";")
			end += 2
		}
		normalized := strconv.Itoa(value)
		if semicolon {
			normalized += ";"
		}
		fields = append(fields[:index+1], append([]string{normalized}, fields[end:]...)...)
	}
	return fields
}

func canonicalIPTablesText(raw string) string {
	state := isoIPTablesCanonicalState{tables: map[string][]string{}, builtinRuleIndex: map[string]int{}}
	for _, rawLine := range strings.Split(raw, "\n") {
		state.consume(strings.TrimSpace(rawLine))
	}
	var kept []string
	for _, tableName := range []string{"*raw", "*mangle", "*nat", "*filter"} {
		for _, line := range canonicalIPTablesTable(state.tables[tableName]) {
			kept = append(kept, tableName+" "+line)
		}
	}
	return strings.Join(kept, "\n")
}

func canonicalIPTablesTable(lines []string) []string {
	var declarations []string
	rules := map[string][]string{}
	for _, line := range lines {
		if strings.HasPrefix(line, ":") {
			declarations = append(declarations, line)
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "-A" {
			rules[fields[1]] = append(rules[fields[1]], line)
		}
	}
	sort.Strings(declarations)
	chains := make([]string, 0, len(rules))
	for chain := range rules {
		chains = append(chains, chain)
	}
	sort.Strings(chains)
	out := append([]string(nil), declarations...)
	for _, chain := range chains {
		out = append(out, rules[chain]...)
	}
	return out
}

type isoIPTablesCanonicalState struct {
	table            string
	tables           map[string][]string
	builtinRuleIndex map[string]int
}

func (state *isoIPTablesCanonicalState) consume(line string) {
	if strings.HasPrefix(line, "*") {
		state.table = line
		return
	}
	if isISOChainDeclaration(line) {
		state.tables[state.table] = append(state.tables[state.table], strings.Fields(line)[0])
		return
	}
	if strings.HasPrefix(line, "-F YEET_ISO_") || !strings.HasPrefix(line, "-A ") {
		return
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return
	}
	chain := fields[1]
	key := state.table + "\x00" + chain
	if strings.Contains(line, "YEET_ISO_") {
		normalized := canonicalIPTablesRule(fields)
		if isISOBuiltinChain(chain) {
			normalized += fmt.Sprintf(" first=%t", state.builtinRuleIndex[key] == 0)
		}
		state.tables[state.table] = append(state.tables[state.table], normalized)
	}
	state.builtinRuleIndex[key]++
}

func canonicalIPTablesRule(fields []string) string {
	normalized := canonicalIPTablesFields(fields, iptablesProtocol(fields))
	if len(normalized) < 2 {
		return strings.Join(normalized, " ")
	}
	jump := slicesIndex(normalized, "-j")
	if jump < 0 {
		jump = len(normalized)
	}
	clauses := canonicalIPTablesMatchClauses(normalized[2:jump])
	out := append([]string{normalized[0], normalized[1]}, clauses...)
	return strings.Join(append(out, normalized[jump:]...), " ")
}

func iptablesProtocol(fields []string) string {
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] == "-p" {
			return fields[index+1]
		}
	}
	return ""
}

func canonicalIPTablesFields(fields []string, protocol string) []string {
	normalized := make([]string, 0, len(fields))
	for index := 0; index < len(fields); index++ {
		if fields[index] == "-m" && index+1 < len(fields) && fields[index+1] == protocol && (protocol == "tcp" || protocol == "udp") {
			index++
			continue
		}
		token := fields[index]
		if token == "--ctstate" && index+1 < len(fields) {
			normalized = append(normalized, token)
			states := strings.Split(fields[index+1], ",")
			sort.Strings(states)
			normalized = append(normalized, strings.Join(states, ","))
			index++
			continue
		}
		normalized = append(normalized, canonicalIPTablesToken(token))
	}
	return normalized
}

func canonicalIPTablesMatchClauses(fields []string) []string {
	var clauses []string
	for index := 0; index < len(fields); {
		clause := []string{}
		if fields[index] == "!" {
			clause = append(clause, "!")
			index++
			if index == len(fields) {
				clauses = append(clauses, strings.Join(clause, " "))
				break
			}
		}
		option := fields[index]
		clause = append(clause, option)
		index++
		arity := 1
		if option == "--match-set" {
			arity = 2
		}
		for arity > 0 && index < len(fields) && fields[index] != "!" && !strings.HasPrefix(fields[index], "-") {
			clause = append(clause, fields[index])
			index++
			arity--
		}
		clauses = append(clauses, strings.Join(clause, " "))
	}
	sort.Strings(clauses)
	return clauses
}

func canonicalIPTablesToken(token string) string {
	prefix, err := netip.ParsePrefix(token)
	if err == nil && prefix.Bits() == prefix.Addr().BitLen() {
		return prefix.Addr().String()
	}
	return token
}

func isISOChainDeclaration(line string) bool {
	if !strings.HasPrefix(line, ":") {
		return false
	}
	name := strings.TrimPrefix(strings.Fields(line)[0], ":")
	return strings.HasPrefix(name, "YEET_ISO_")
}

func isISOBuiltinChain(chain string) bool {
	switch chain {
	case "INPUT", "FORWARD", "PREROUTING", "POSTROUTING":
		return true
	default:
		return false
	}
}

func canonicalISOIPSetText(raw string) string {
	var kept []string
	for _, rawLine := range strings.Split(raw, "\n") {
		fields := strings.Fields(rawLine)
		if len(fields) < 3 || fields[1] != isoIPSetSources && fields[1] != isoIPSetNonPublic {
			continue
		}
		switch fields[0] {
		case "create":
			line := strings.Join(fields[:3], " ")
			if index := slicesIndex(fields, "family"); index >= 0 && index+1 < len(fields) {
				line += " family " + fields[index+1]
			}
			kept = append(kept, line)
		case "add":
			fields[2] = canonicalIPSetAddressToken(fields[2])
			kept = append(kept, strings.Join(fields, " "))
		}
	}
	sort.Strings(kept)
	return strings.Join(kept, "\n")
}

func canonicalIPSetAddressToken(token string) string {
	network, iface, hasInterface := strings.Cut(token, ",")
	prefix, err := netip.ParsePrefix(network)
	if err == nil && prefix.Bits() == prefix.Addr().BitLen() {
		network = prefix.Addr().String()
	}
	if hasInterface {
		return network + "," + iface
	}
	return network
}

func slicesIndex(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}
