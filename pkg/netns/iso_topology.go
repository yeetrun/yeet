// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

const (
	isoAggregateRouteMetric = "42760"
	isoRouterNFTTable       = "yeet_iso_router"
	isoRouterMangle         = "YEET_ISO_R_MANGLE"
	isoRouterInput          = "YEET_ISO_R_INPUT"
	isoRouterForward        = "YEET_ISO_R_FORWARD"
	isoRouterPreRoute       = "YEET_ISO_R_PREROUTE"
)

// ISOCommand is an inspectable privileged command. CheckArgs makes an
// insertion idempotent: if the check succeeds, Args is not run.
type ISOCommand struct {
	Name           string
	Args           []string
	Input          string
	CheckArgs      []string
	CheckFirstRule string
	IgnoreExists   bool
	IgnoreNotFound bool
	nftReplace     *isoNFTTableReplacement
	jump           *isoIPTablesJump
}

type isoNFTTableReplacement struct {
	Prefix   []string
	Family   string
	Table    string
	Rendered string
}

type isoIPTablesJump struct {
	Prefix []string
	Table  string
	Chain  string
	Target string
}

func (command ISOCommand) Run(ctx context.Context) error {
	if command.nftReplace != nil {
		replace := command.nftReplace
		return applyISONamedNFTTable(ctx, command.Name, replace.Prefix, replace.Family, replace.Table, replace.Rendered)
	}
	if command.jump != nil {
		jump := command.jump
		return reconcileISOIPTablesJump(ctx, command.Name, jump.Prefix, jump.Table, jump.Chain, jump.Target)
	}
	if command.checkSatisfied(ctx) {
		return nil
	}
	_, err := runISOCommand(ctx, []byte(command.Input), command.Name, command.Args...)
	if err == nil || command.mayIgnore(err) {
		return nil
	}
	return err
}

func (command ISOCommand) checkSatisfied(ctx context.Context) bool {
	if len(command.CheckArgs) == 0 {
		return false
	}
	out, err := runISOCommand(ctx, nil, command.Name, command.CheckArgs...)
	return err == nil && (command.CheckFirstRule == "" || firstNonEmptyLine(string(out)) == command.CheckFirstRule)
}

func (command ISOCommand) mayIgnore(err error) bool {
	return command.IgnoreExists && isISOAlreadyExists(err) || command.IgnoreNotFound && command.NotFound(err)
}

func (command ISOCommand) NotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") || strings.Contains(message, "cannot find") ||
		strings.Contains(message, "no such") || strings.Contains(message, "does not exist")
}

func isISOAlreadyExists(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file exists") || strings.Contains(message, "already exists")
}

// ISOTopologySpec describes one routed service namespace. Backend defaults to
// nft only for pure command rendering; Catch always supplies the detected
// backend before executing commands.
type ISOTopologySpec struct {
	Backend            FirewallBackend
	Pool               netip.Prefix
	Allocation         db.ISOAllocation
	TailscaleInterface string
}

// ISOTopologyEnsureCommands returns the deterministic root/router command
// sequence. VM allocations intentionally receive only the aggregate route;
// their TAP attachment is implemented by the VM lifecycle.
func ISOTopologyEnsureCommands(spec ISOTopologySpec) ([]ISOCommand, error) {
	if err := validateISOTopologySpec(spec); err != nil {
		return nil, err
	}
	commands := []ISOCommand{{
		Name: "ip", Args: []string{"route", "replace", "blackhole", spec.Pool.String(), "metric", isoAggregateRouteMetric},
	}}
	if spec.Allocation.Kind == string(iso.PayloadVM) {
		return commands, nil
	}
	allocation := spec.Allocation
	bridge := isoTopologyBridge(allocation)
	hostCIDR := netip.PrefixFrom(allocation.HostIP, allocation.Link.Bits()).String()
	peerCIDR := netip.PrefixFrom(allocation.PeerIP, allocation.Link.Bits()).String()
	commands = append(commands,
		ISOCommand{Name: "ip", Args: []string{"netns", "add", allocation.NetNS}, IgnoreExists: true},
		ISOCommand{Name: "ip", Args: []string{"link", "add", allocation.Interface, "type", "veth", "peer", "name", allocation.PeerInterface}, IgnoreExists: true},
		ISOCommand{Name: "ip", Args: []string{"link", "set", allocation.PeerInterface, "netns", allocation.NetNS}, IgnoreNotFound: true},
		ISOCommand{Name: "ip", Args: []string{"address", "replace", hostCIDR, "dev", allocation.Interface}},
		ISOCommand{Name: "ip", Args: []string{"link", "set", allocation.Interface, "up"}},
		ISOCommand{Name: "sysctl", Args: []string{"-w", "net.ipv4.ip_forward=1"}},
		ISOCommand{Name: "sysctl", Args: []string{"-w", "net.ipv4.conf." + allocation.Interface + ".forwarding=1"}},
		ISOCommand{Name: "sysctl", Args: []string{"-w", "net.ipv4.conf." + allocation.Interface + ".rp_filter=1"}},
		ISOCommand{Name: "sysctl", Args: []string{"-w", "net.ipv4.conf." + allocation.Interface + ".accept_local=0"}},
		ISOCommand{Name: "sysctl", Args: []string{"-w", "net.ipv4.conf." + allocation.Interface + ".route_localnet=0"}},
		ISOCommand{Name: "sysctl", Args: []string{"-w", "net.ipv6.conf." + allocation.Interface + ".disable_ipv6=1"}},
		isoNetNSCommand(allocation.NetNS, "ip", "link", "set", "lo", "up"),
		isoNetNSCommand(allocation.NetNS, "ip", "address", "replace", peerCIDR, "dev", allocation.PeerInterface),
		isoNetNSCommand(allocation.NetNS, "ip", "link", "set", allocation.PeerInterface, "up"),
		isoNetNSCommand(allocation.NetNS, "sysctl", "-w", "net.ipv4.ip_forward=1"),
		isoNetNSCommand(allocation.NetNS, "sysctl", "-w", "net.ipv4.conf.all.rp_filter=1"),
		isoNetNSCommand(allocation.NetNS, "sysctl", "-w", "net.ipv4.conf.default.rp_filter=1"),
		isoNetNSCommand(allocation.NetNS, "sysctl", "-w", "net.ipv4.conf."+allocation.PeerInterface+".rp_filter=1"),
		isoNetNSCommand(allocation.NetNS, "sysctl", "-w", "net.ipv6.conf.all.disable_ipv6=1"),
		isoNetNSCommand(allocation.NetNS, "sysctl", "-w", "net.ipv6.conf.default.disable_ipv6=1"),
		isoNetNSCommand(allocation.NetNS, "sysctl", "-w", "net.ipv6.conf."+allocation.PeerInterface+".disable_ipv6=1"),
		ISOCommand{Name: "ip", Args: []string{"route", "replace", allocation.Project.String(), "via", allocation.PeerIP.String(), "dev", allocation.Interface}},
		isoNetNSCommand(allocation.NetNS, "ip", "route", "replace", "default", "via", allocation.HostIP.String(), "dev", allocation.PeerInterface),
	)
	commands = append(commands, isoRouterPolicyCommands(spec, bridge)...)
	return commands, nil
}

func isoNetNSCommand(namespace, name string, args ...string) ISOCommand {
	allArgs := []string{"netns", "exec", namespace, name}
	allArgs = append(allArgs, args...)
	return ISOCommand{Name: "ip", Args: allArgs}
}

func validateISOTopologySpec(spec ISOTopologySpec) error {
	layout, err := validateISOTopologyPoolAndBackend(spec)
	if err != nil {
		return err
	}
	if err := validateISOTopologyLink(layout, spec.Allocation); err != nil {
		return err
	}
	if spec.Allocation.Kind == string(iso.PayloadVM) {
		return validateISOVMTopology(spec.Allocation)
	}
	return validateISORouterTopology(layout, spec)
}

func validateISOTopologyPoolAndBackend(spec ISOTopologySpec) (iso.Layout, error) {
	pool := spec.Pool.Masked()
	if !pool.IsValid() || !pool.Addr().Is4() || pool.Bits() != 16 || spec.Pool != pool {
		return iso.Layout{}, fmt.Errorf("ISO topology requires a canonical IPv4 /16")
	}
	switch spec.Backend {
	case "", BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy:
		return iso.NewLayout(pool)
	default:
		return iso.Layout{}, fmt.Errorf("unsupported ISO firewall backend %q", spec.Backend)
	}
}

func validateISOTopologyLink(layout iso.Layout, allocation db.ISOAllocation) error {
	link := allocation.Link.Masked()
	if !link.IsValid() || !link.Addr().Is4() || link.Bits() != 30 || allocation.Link != link || !layout.Links.Contains(link.Addr()) {
		return fmt.Errorf("ISO topology requires an in-pool /30 link")
	}
	if allocation.HostIP != link.Addr().Next() || allocation.PeerIP != link.Addr().Next().Next() {
		return fmt.Errorf("ISO topology link addresses must be host one and peer two")
	}
	if !isoInterfaceNameRE.MatchString(allocation.Interface) {
		return fmt.Errorf("invalid ISO root interface %q", allocation.Interface)
	}
	return nil
}

func validateISOVMTopology(allocation db.ISOAllocation) error {
	if allocation.Project.IsValid() || allocation.Gateway.IsValid() || allocation.NetNS != "" || allocation.Bridge != "" ||
		len(allocation.Components) != 0 || len(allocation.RetiredComponents) != 0 {
		return fmt.Errorf("ISO VM topology must not contain router or project state")
	}
	return nil
}

func validateISORouterTopology(layout iso.Layout, spec ISOTopologySpec) error {
	a := spec.Allocation
	if err := validateISORouterInterfaces(a); err != nil {
		return err
	}
	if err := validateISOProjectNetwork(layout, a); err != nil {
		return err
	}
	if strings.TrimSpace(a.NetNS) == "" {
		return fmt.Errorf("ISO topology namespace is required")
	}
	if spec.TailscaleInterface != "" && !isoInterfaceNameRE.MatchString(spec.TailscaleInterface) {
		return fmt.Errorf("invalid ISO Tailscale interface %q", spec.TailscaleInterface)
	}
	return nil
}

func validateISORouterInterfaces(allocation db.ISOAllocation) error {
	if !isoInterfaceNameRE.MatchString(allocation.PeerInterface) || !isoInterfaceNameRE.MatchString(isoTopologyBridge(allocation)) {
		return fmt.Errorf("invalid ISO router interface")
	}
	return nil
}

func validateISOProjectNetwork(layout iso.Layout, allocation db.ISOAllocation) error {
	project := allocation.Project.Masked()
	if !project.IsValid() || !project.Addr().Is4() || project.Bits() != 27 || allocation.Project != project || !layout.Projects.Contains(project.Addr()) {
		return fmt.Errorf("ISO topology requires an in-pool /27 project")
	}
	if allocation.Gateway != project.Addr().Next() {
		return fmt.Errorf("ISO topology project gateway must be host one")
	}
	return nil
}

func isoTopologyBridge(allocation db.ISOAllocation) string {
	if strings.TrimSpace(allocation.Bridge) == "" {
		return "br0"
	}
	return allocation.Bridge
}

func isoRouterPolicyCommands(spec ISOTopologySpec, bridge string) []ISOCommand {
	backend := spec.Backend
	if backend == "" {
		backend = BackendNFT
	}
	switch backend {
	case BackendNFT:
		ipv4, ipv6 := renderNFTISORouterPolicy(spec, bridge)
		prefix := []string{"netns", "exec", spec.Allocation.NetNS, "nft"}
		return []ISOCommand{
			{Name: "ip", Input: ipv4, nftReplace: &isoNFTTableReplacement{Prefix: prefix, Family: "ip", Table: isoRouterNFTTable, Rendered: ipv4}},
			{Name: "ip", Input: ipv6, nftReplace: &isoNFTTableReplacement{Prefix: prefix, Family: "ip6", Table: isoRouterNFTTable, Rendered: ipv6}},
		}
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		ipv4, ipv6 := renderIPTablesISORouterPolicy(spec, bridge)
		restore4, _, _ := isoIPTablesTools(backend, false)
		restore6, _, _ := isoIPTablesTools(backend, true)
		commands := []ISOCommand{
			{Name: "ip", Args: []string{"netns", "exec", spec.Allocation.NetNS, restore4, "--noflush"}, Input: iptablesRestoreOwnedRouterChains(ipv4)},
			{Name: "ip", Args: []string{"netns", "exec", spec.Allocation.NetNS, restore6, "--noflush"}, Input: iptablesRestoreOwnedRouterChains(ipv6)},
		}
		commands = append(commands, isoRouterJumpCommands(spec, backend)...)
		return commands
	default:
		return []ISOCommand{{Name: "false", Args: []string{"unsupported-ISO-firewall-backend", string(backend)}}}
	}
}

func renderNFTISORouterPolicy(spec ISOTopologySpec, bridge string) (string, string) {
	a := spec.Allocation
	restrictedInterfaces := []string{fmt.Sprintf("%q", bridge), fmt.Sprintf("%q", a.PeerInterface)}
	if spec.TailscaleInterface != "" {
		restrictedInterfaces = append(restrictedInterfaces, fmt.Sprintf("%q", spec.TailscaleInterface))
	}
	restrictedSet := strings.Join(restrictedInterfaces, ", ")
	var ipv4 strings.Builder
	fmt.Fprintf(&ipv4, "table ip %s {\n", isoRouterNFTTable)
	ipv4.WriteString("  chain prerouting_mangle { type filter hook prerouting priority -160; policy accept;\n")
	fmt.Fprintf(&ipv4, "    iifname %q ip saddr != %s drop\n", bridge, a.Project)
	ipv4.WriteString("  }\n")
	ipv4.WriteString("  chain prerouting_nat { type nat hook prerouting priority -110; policy accept;\n")
	fmt.Fprintf(&ipv4, "    iifname %q ip daddr %s udp dport 53 dnat to %s:53\n", bridge, a.Gateway, a.HostIP)
	fmt.Fprintf(&ipv4, "    iifname %q ip daddr %s tcp dport 53 dnat to %s:53\n", bridge, a.Gateway, a.HostIP)
	ipv4.WriteString("  }\n")
	ipv4.WriteString("  chain input { type filter hook input priority -10; policy accept;\n")
	ipv4.WriteString("    ct state established,related accept\n")
	ipv4.WriteString("    iifname != \"lo\" reject with icmp type admin-prohibited\n")
	ipv4.WriteString("  }\n")
	ipv4.WriteString("  chain forward { type filter hook forward priority -10; policy accept;\n")
	ipv4.WriteString("    ct state established,related accept\n")
	fmt.Fprintf(&ipv4, "    iifname %q oifname %q ip saddr %s ip daddr %s accept\n", bridge, bridge, a.Project, a.Project)
	fmt.Fprintf(&ipv4, "    iifname %q oifname %q ip daddr %s accept\n", a.PeerInterface, bridge, a.Project)
	fmt.Fprintf(&ipv4, "    iifname %q ip saddr %s oifname %q accept\n", bridge, a.Project, a.PeerInterface)
	if spec.TailscaleInterface != "" {
		fmt.Fprintf(&ipv4, "    iifname %q ip saddr %s oifname %q accept\n", bridge, a.Project, spec.TailscaleInterface)
		fmt.Fprintf(&ipv4, "    iifname %q oifname %q ip daddr %s accept\n", spec.TailscaleInterface, bridge, a.Project)
	}
	fmt.Fprintf(&ipv4, "    iifname { %s } reject with icmp type admin-prohibited\n", restrictedSet)
	fmt.Fprintf(&ipv4, "    oifname { %s } reject with icmp type admin-prohibited\n", restrictedSet)
	ipv4.WriteString("  }\n}\n")

	var ipv6 strings.Builder
	fmt.Fprintf(&ipv6, "table ip6 %s {\n", isoRouterNFTTable)
	ipv6.WriteString("  chain input { type filter hook input priority -10; policy accept; iifname != \"lo\" drop; }\n")
	fmt.Fprintf(&ipv6, "  chain forward { type filter hook forward priority -10; policy accept; iifname { %s } drop; oifname { %s } drop; }\n", restrictedSet, restrictedSet)
	ipv6.WriteString("}\n")
	return ipv4.String(), ipv6.String()
}

func renderIPTablesISORouterPolicy(spec ISOTopologySpec, bridge string) (string, string) {
	a := spec.Allocation
	restrictedInterfaces := []string{bridge, a.PeerInterface}
	if spec.TailscaleInterface != "" {
		restrictedInterfaces = append(restrictedInterfaces, spec.TailscaleInterface)
	}
	var ipv4 strings.Builder
	ipv4.WriteString("*mangle\n")
	fmt.Fprintf(&ipv4, ":%s - [0:0]\n-F %s\n-A PREROUTING -j %s\n", isoRouterMangle, isoRouterMangle, isoRouterMangle)
	fmt.Fprintf(&ipv4, "-A %s -i %s ! -s %s -j DROP\nCOMMIT\n", isoRouterMangle, bridge, a.Project)
	ipv4.WriteString("*nat\n")
	fmt.Fprintf(&ipv4, ":%s - [0:0]\n-F %s\n-A PREROUTING -j %s\n", isoRouterPreRoute, isoRouterPreRoute, isoRouterPreRoute)
	fmt.Fprintf(&ipv4, "-A %s -i %s -d %s -p udp --dport 53 -j DNAT --to-destination %s:53\n", isoRouterPreRoute, bridge, a.Gateway, a.HostIP)
	fmt.Fprintf(&ipv4, "-A %s -i %s -d %s -p tcp --dport 53 -j DNAT --to-destination %s:53\nCOMMIT\n", isoRouterPreRoute, bridge, a.Gateway, a.HostIP)
	ipv4.WriteString("*filter\n")
	fmt.Fprintf(&ipv4, ":%s - [0:0]\n:%s - [0:0]\n-F %s\n-F %s\n-A INPUT -j %s\n-A FORWARD -j %s\n", isoRouterInput, isoRouterForward, isoRouterInput, isoRouterForward, isoRouterInput, isoRouterForward)
	fmt.Fprintf(&ipv4, "-A %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n", isoRouterInput)
	fmt.Fprintf(&ipv4, "-A %s ! -i lo -j REJECT --reject-with icmp-admin-prohibited\n", isoRouterInput)
	fmt.Fprintf(&ipv4, "-A %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n", isoRouterForward)
	fmt.Fprintf(&ipv4, "-A %s -i %s -o %s -s %s -d %s -j ACCEPT\n", isoRouterForward, bridge, bridge, a.Project, a.Project)
	fmt.Fprintf(&ipv4, "-A %s -i %s -o %s -d %s -j ACCEPT\n", isoRouterForward, a.PeerInterface, bridge, a.Project)
	fmt.Fprintf(&ipv4, "-A %s -i %s -s %s -o %s -j ACCEPT\n", isoRouterForward, bridge, a.Project, a.PeerInterface)
	if spec.TailscaleInterface != "" {
		fmt.Fprintf(&ipv4, "-A %s -i %s -s %s -o %s -j ACCEPT\n", isoRouterForward, bridge, a.Project, spec.TailscaleInterface)
		fmt.Fprintf(&ipv4, "-A %s -i %s -o %s -d %s -j ACCEPT\n", isoRouterForward, spec.TailscaleInterface, bridge, a.Project)
	}
	for _, iface := range restrictedInterfaces {
		fmt.Fprintf(&ipv4, "-A %s -i %s -j REJECT --reject-with icmp-admin-prohibited\n", isoRouterForward, iface)
	}
	for index, iface := range restrictedInterfaces {
		fmt.Fprintf(&ipv4, "-A %s -o %s -j REJECT --reject-with icmp-admin-prohibited\n", isoRouterForward, iface)
		if index == len(restrictedInterfaces)-1 {
			ipv4.WriteString("COMMIT\n")
		}
	}

	var ipv6 strings.Builder
	ipv6.WriteString("*filter\n")
	fmt.Fprintf(&ipv6, ":%s - [0:0]\n:%s - [0:0]\n-F %s\n-F %s\n-A INPUT -j %s\n-A FORWARD -j %s\n", isoRouterInput, isoRouterForward, isoRouterInput, isoRouterForward, isoRouterInput, isoRouterForward)
	fmt.Fprintf(&ipv6, "-A %s ! -i lo -j DROP\n", isoRouterInput)
	for _, iface := range restrictedInterfaces {
		fmt.Fprintf(&ipv6, "-A %s -i %s -j DROP\n", isoRouterForward, iface)
		fmt.Fprintf(&ipv6, "-A %s -o %s -j DROP\n", isoRouterForward, iface)
	}
	ipv6.WriteString("COMMIT\n")
	return ipv4.String(), ipv6.String()
}

func iptablesRestoreOwnedRouterChains(rendered string) string {
	var out strings.Builder
	for _, raw := range strings.Split(rendered, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "-A INPUT -j YEET_ISO_R_") ||
			strings.HasPrefix(line, "-A FORWARD -j YEET_ISO_R_") || strings.HasPrefix(line, "-A PREROUTING -j YEET_ISO_R_") {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func isoRouterJumpCommands(spec ISOTopologySpec, backend FirewallBackend) []ISOCommand {
	bin, err := iptablesBinary(backend)
	if err != nil {
		return []ISOCommand{{Name: "false", Args: []string{err.Error()}}}
	}
	ns := []string{"netns", "exec", spec.Allocation.NetNS}
	type jump struct{ table, chain, target string }
	jump4 := []jump{{"mangle", "PREROUTING", isoRouterMangle}, {"nat", "PREROUTING", isoRouterPreRoute}, {"filter", "INPUT", isoRouterInput}, {"filter", "FORWARD", isoRouterForward}}
	var commands []ISOCommand
	for _, item := range jump4 {
		prefix := append(append([]string{}, ns...), bin)
		commands = append(commands, ISOCommand{Name: "ip", jump: &isoIPTablesJump{Prefix: prefix, Table: item.table, Chain: item.chain, Target: item.target}})
	}
	ip6bin := strings.Replace(bin, "iptables", "ip6tables", 1)
	for _, item := range []jump{{"filter", "INPUT", isoRouterInput}, {"filter", "FORWARD", isoRouterForward}} {
		prefix := append(append([]string{}, ns...), ip6bin)
		commands = append(commands, ISOCommand{Name: "ip", jump: &isoIPTablesJump{Prefix: prefix, Table: item.table, Chain: item.chain, Target: item.target}})
	}
	return commands
}

// EnsureISOTopology creates and verifies one routed namespace after the root
// policy has already been verified by Catch.
func EnsureISOTopology(ctx context.Context, spec ISOTopologySpec) error {
	commands, err := ISOTopologyEnsureCommands(spec)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err := command.Run(ctx); err != nil {
			return fmt.Errorf("ensure ISO topology: %w", err)
		}
	}
	return VerifyISOTopology(ctx, spec)
}

// VerifyISOTopology proves the aggregate and more-specific routes, namespace
// attachment, source controls, and router firewall are present.
func VerifyISOTopology(ctx context.Context, spec ISOTopologySpec) error {
	if err := validateISOTopologySpec(spec); err != nil {
		return err
	}
	if err := verifyISOAggregateRoute(ctx, spec.Pool); err != nil {
		return err
	}
	if spec.Allocation.Kind == string(iso.PayloadVM) {
		return nil
	}
	if err := verifyISORouteAndLinkState(ctx, spec.Allocation); err != nil {
		return err
	}
	if err := verifyISOSysctlState(ctx, spec.Allocation); err != nil {
		return err
	}
	return verifyISORouterPolicy(ctx, spec)
}

func verifyISOAggregateRoute(ctx context.Context, pool netip.Prefix) error {
	aggregate, err := readISOCommandText(ctx, "ip", "-o", "route", "show", "exact", pool.String())
	if err != nil {
		return fmt.Errorf("verify ISO aggregate route: %w", err)
	}
	lines := isoNonEmptyLines(aggregate)
	if len(lines) != 1 {
		return fmt.Errorf("verify ISO aggregate route: got %d exact routes, want 1", len(lines))
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 || fields[0] != "blackhole" || fields[1] != pool.String() || isoFieldValueCount(fields, "metric", isoAggregateRouteMetric) != 1 {
		return fmt.Errorf("verify ISO aggregate route: unexpected route %q", lines[0])
	}
	return nil
}

func verifyISORouteAndLinkState(ctx context.Context, allocation db.ISOAllocation) error {
	if err := verifyISOLinkUp(ctx, "root link", []string{"-o", "link", "show", "dev", allocation.Interface}, allocation.Interface); err != nil {
		return err
	}
	hostCIDR := netip.PrefixFrom(allocation.HostIP, allocation.Link.Bits()).String()
	if err := verifyISOAddress(ctx, "root address", []string{"-o", "-4", "address", "show", "dev", allocation.Interface, "scope", "global"}, allocation.Interface, hostCIDR); err != nil {
		return err
	}
	if err := verifyISORoute(ctx, "project route", []string{"-o", "route", "show", "exact", allocation.Project.String()}, allocation.Project.String(), allocation.PeerIP.String(), allocation.Interface); err != nil {
		return err
	}
	nsPrefix := []string{"netns", "exec", allocation.NetNS, "ip"}
	if err := verifyISOLinkUp(ctx, "namespace peer link", append(append([]string{}, nsPrefix...), "-o", "link", "show", "dev", allocation.PeerInterface), allocation.PeerInterface); err != nil {
		return err
	}
	peerCIDR := netip.PrefixFrom(allocation.PeerIP, allocation.Link.Bits()).String()
	if err := verifyISOAddress(ctx, "namespace peer address", append(append([]string{}, nsPrefix...), "-o", "-4", "address", "show", "dev", allocation.PeerInterface, "scope", "global"), allocation.PeerInterface, peerCIDR); err != nil {
		return err
	}
	if err := verifyISORoute(ctx, "namespace default route", append(append([]string{}, nsPrefix...), "-o", "route", "show", "exact", "default"), "default", allocation.HostIP.String(), allocation.PeerInterface); err != nil {
		return err
	}
	return nil
}

func verifyISOLinkUp(ctx context.Context, label string, args []string, iface string) error {
	out, err := readISOCommandText(ctx, "ip", args...)
	if err != nil {
		return fmt.Errorf("verify ISO %s: %w", label, err)
	}
	lines := isoNonEmptyLines(out)
	if len(lines) != 1 {
		return fmt.Errorf("verify ISO %s: got %d links, want 1", label, len(lines))
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 3 || isoOutputInterface(fields[1]) != iface || !isoLinkFlagsContain(fields[2], "UP") {
		return fmt.Errorf("verify ISO %s: link is not exactly %s up: %q", label, iface, lines[0])
	}
	return nil
}

func verifyISOAddress(ctx context.Context, label string, args []string, iface, cidr string) error {
	out, err := readISOCommandText(ctx, "ip", args...)
	if err != nil {
		return fmt.Errorf("verify ISO %s: %w", label, err)
	}
	lines := isoNonEmptyLines(out)
	if len(lines) != 1 {
		return fmt.Errorf("verify ISO %s: got %d addresses, want 1", label, len(lines))
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 4 || isoOutputInterface(fields[1]) != iface || isoFieldValueCount(fields, "inet", cidr) != 1 {
		return fmt.Errorf("verify ISO %s: unexpected address %q", label, lines[0])
	}
	return nil
}

func verifyISORoute(ctx context.Context, label string, args []string, destination, via, dev string) error {
	out, err := readISOCommandText(ctx, "ip", args...)
	if err != nil {
		return fmt.Errorf("verify ISO %s: %w", label, err)
	}
	lines := isoNonEmptyLines(out)
	if len(lines) != 1 {
		return fmt.Errorf("verify ISO %s: got %d exact routes, want 1", label, len(lines))
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 || fields[0] != destination || isoFieldValueCount(fields, "via", via) != 1 || isoFieldValueCount(fields, "dev", dev) != 1 || slicesIndex(fields, "nexthop") >= 0 {
		return fmt.Errorf("verify ISO %s: unexpected route %q", label, lines[0])
	}
	return nil
}

func isoNonEmptyLines(raw string) []string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func isoFieldValueCount(fields []string, field, value string) int {
	count := 0
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] == field && fields[index+1] == value {
			count++
		}
	}
	return count
}

func isoOutputInterface(raw string) string {
	raw = strings.TrimSuffix(raw, ":")
	iface, _, _ := strings.Cut(raw, "@")
	return iface
}

func isoLinkFlagsContain(raw, want string) bool {
	raw = strings.Trim(raw, "<>")
	for _, flag := range strings.Split(raw, ",") {
		if flag == want {
			return true
		}
	}
	return false
}

func verifyISOSysctlState(ctx context.Context, allocation db.ISOAllocation) error {
	for _, check := range []struct {
		label string
		name  string
		args  []string
		want  string
	}{
		{label: "root forwarding", name: "sysctl", args: []string{"-n", "net.ipv4.ip_forward"}, want: "1"},
		{label: "root interface forwarding", name: "sysctl", args: []string{"-n", "net.ipv4.conf." + allocation.Interface + ".forwarding"}, want: "1"},
		{label: "root source validation", name: "sysctl", args: []string{"-n", "net.ipv4.conf." + allocation.Interface + ".rp_filter"}, want: "1"},
		{label: "root accept local", name: "sysctl", args: []string{"-n", "net.ipv4.conf." + allocation.Interface + ".accept_local"}, want: "0"},
		{label: "root route localnet", name: "sysctl", args: []string{"-n", "net.ipv4.conf." + allocation.Interface + ".route_localnet"}, want: "0"},
		{label: "root IPv6 disable", name: "sysctl", args: []string{"-n", "net.ipv6.conf." + allocation.Interface + ".disable_ipv6"}, want: "1"},
		{label: "router forwarding", name: "ip", args: []string{"netns", "exec", allocation.NetNS, "sysctl", "-n", "net.ipv4.ip_forward"}, want: "1"},
		{label: "router all source validation", name: "ip", args: []string{"netns", "exec", allocation.NetNS, "sysctl", "-n", "net.ipv4.conf.all.rp_filter"}, want: "1"},
		{label: "router default source validation", name: "ip", args: []string{"netns", "exec", allocation.NetNS, "sysctl", "-n", "net.ipv4.conf.default.rp_filter"}, want: "1"},
		{label: "router peer source validation", name: "ip", args: []string{"netns", "exec", allocation.NetNS, "sysctl", "-n", "net.ipv4.conf." + allocation.PeerInterface + ".rp_filter"}, want: "1"},
		{label: "router all IPv6 disable", name: "ip", args: []string{"netns", "exec", allocation.NetNS, "sysctl", "-n", "net.ipv6.conf.all.disable_ipv6"}, want: "1"},
		{label: "router default IPv6 disable", name: "ip", args: []string{"netns", "exec", allocation.NetNS, "sysctl", "-n", "net.ipv6.conf.default.disable_ipv6"}, want: "1"},
		{label: "router peer IPv6 disable", name: "ip", args: []string{"netns", "exec", allocation.NetNS, "sysctl", "-n", "net.ipv6.conf." + allocation.PeerInterface + ".disable_ipv6"}, want: "1"},
	} {
		out, runErr := readISOCommandText(ctx, check.name, check.args...)
		if runErr != nil {
			return fmt.Errorf("verify ISO %s: %w", check.label, runErr)
		}
		if strings.TrimSpace(out) != check.want {
			return fmt.Errorf("verify ISO %s: got %q want %s", check.label, strings.TrimSpace(out), check.want)
		}
	}
	return nil
}

func verifyISORouterPolicy(ctx context.Context, spec ISOTopologySpec) error {
	backend := spec.Backend
	if backend == "" {
		backend = BackendNFT
	}
	if backend == BackendNFT {
		return verifyNFTISORouterPolicy(ctx, spec)
	}
	return verifyIPTablesISORouterPolicy(ctx, spec, backend)
}

func verifyNFTISORouterPolicy(ctx context.Context, spec ISOTopologySpec) error {
	want4, want6 := renderNFTISORouterPolicy(spec, isoTopologyBridge(spec.Allocation))
	got4, err := readISOCommandText(ctx, "ip", "netns", "exec", spec.Allocation.NetNS, "nft", "list", "table", "ip", isoRouterNFTTable)
	if err == nil {
		var got6 string
		got6, err = readISOCommandText(ctx, "ip", "netns", "exec", spec.Allocation.NetNS, "nft", "list", "table", "ip6", isoRouterNFTTable)
		if err == nil && !isoRouterPolicyMatches(got4, got6, want4, want6, canonicalNFTText) {
			err = fmt.Errorf("router policy digest mismatch")
		}
	}
	if err != nil {
		return fmt.Errorf("verify ISO router policy: %w", err)
	}
	return nil
}

func verifyIPTablesISORouterPolicy(ctx context.Context, spec ISOTopologySpec, backend FirewallBackend) error {
	want4, want6 := renderIPTablesISORouterPolicy(spec, isoTopologyBridge(spec.Allocation))
	_, save4, err := isoIPTablesTools(backend, false)
	if err != nil {
		return err
	}
	_, save6, err := isoIPTablesTools(backend, true)
	if err != nil {
		return err
	}
	got4, err := readISOCommandText(ctx, "ip", "netns", "exec", spec.Allocation.NetNS, save4)
	if err == nil {
		var got6 string
		got6, err = readISOCommandText(ctx, "ip", "netns", "exec", spec.Allocation.NetNS, save6)
		if err == nil && !isoRouterPolicyMatches(got4, got6, want4, want6, canonicalISORouterIPTables) {
			err = fmt.Errorf("router policy digest mismatch")
		}
	}
	if err != nil {
		return fmt.Errorf("verify ISO router policy: %w", err)
	}
	return nil
}

func isoRouterPolicyMatches(got4, got6, want4, want6 string, canonical func(string) string) bool {
	return canonical(got4) == canonical(want4) && canonical(got6) == canonical(want6)
}

func canonicalISORouterIPTables(raw string) string {
	return canonicalIPTablesText(raw)
}

// ISOTopologyRemoveCommands tears down only the per-service route, veth, and
// namespace. The aggregate blackhole is pool-owned and remains installed.
func ISOTopologyRemoveCommands(spec ISOTopologySpec) []ISOCommand {
	a := spec.Allocation
	if a.Kind == string(iso.PayloadVM) {
		return nil
	}
	return []ISOCommand{
		{Name: "ip", Args: []string{"route", "delete", a.Project.String(), "via", a.PeerIP.String(), "dev", a.Interface}, IgnoreNotFound: true},
		{Name: "ip", Args: []string{"link", "delete", a.Interface}, IgnoreNotFound: true},
		{Name: "ip", Args: []string{"netns", "delete", a.NetNS}, IgnoreNotFound: true},
	}
}

// RemoveISOTopology removes and verifies a per-service topology.
func RemoveISOTopology(ctx context.Context, spec ISOTopologySpec) error {
	if err := validateISOTopologySpec(spec); err != nil {
		return err
	}
	for _, command := range ISOTopologyRemoveCommands(spec) {
		if err := command.Run(ctx); err != nil && !command.NotFound(err) {
			return fmt.Errorf("remove ISO topology: %w", err)
		}
	}
	return VerifyISOTopologyAbsent(ctx, spec)
}

// VerifyISOTopologyAbsent proves no per-service attachment remains.
func VerifyISOTopologyAbsent(ctx context.Context, spec ISOTopologySpec) error {
	if spec.Allocation.Kind == string(iso.PayloadVM) {
		return verifyISORootLinkAbsent(ctx, spec.Allocation.Interface)
	}
	a := spec.Allocation
	if err := verifyISORootLinkAbsent(ctx, a.Interface); err != nil {
		return err
	}
	if err := verifyISONamespaceAbsent(ctx, a.NetNS); err != nil {
		return err
	}
	return verifyISOProjectRouteAbsent(ctx, a.Project)
}

func verifyISORootLinkAbsent(ctx context.Context, iface string) error {
	_, err := readISOCommandText(ctx, "ip", "link", "show", "dev", iface)
	if err == nil {
		return fmt.Errorf("ISO root interface %s still exists", iface)
	}
	if !(ISOCommand{}).NotFound(err) {
		return fmt.Errorf("verify ISO root interface absence: %w", err)
	}
	return nil
}

func verifyISONamespaceAbsent(ctx context.Context, namespace string) error {
	namespaces, err := readISOCommandText(ctx, "ip", "netns", "list")
	if err != nil {
		return fmt.Errorf("verify ISO namespace absence: %w", err)
	}
	for _, line := range strings.Split(namespaces, "\n") {
		if fields := strings.Fields(line); len(fields) != 0 && fields[0] == namespace {
			return fmt.Errorf("ISO namespace %s still exists", namespace)
		}
	}
	return nil
}

func verifyISOProjectRouteAbsent(ctx context.Context, project netip.Prefix) error {
	route, err := readISOCommandText(ctx, "ip", "route", "show", "exact", project.String())
	if err != nil {
		return fmt.Errorf("verify ISO project route absence: %w", err)
	}
	if strings.TrimSpace(route) != "" {
		return fmt.Errorf("ISO project route %s still exists", project)
	}
	return nil
}
