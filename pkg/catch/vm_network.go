// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmSvcGateway  = "192.168.100.254"
	vmSvcNetNS    = "yeet-ns"
	vmSvcNSBridge = "br0"
)

type vmNetworkInputs struct {
	ServiceIP         string
	LANParent         string
	LANParentIsBridge bool
	LANBridge         string
	LANVLAN           int
	LANMAC            string
}

type vmNetworkPlan struct {
	Service    string
	Interfaces []vmNetworkInterfacePlan
}

type vmNetworkInterfacePlan struct {
	Mode       string
	GuestName  string
	Tap        string
	Bridge     string
	Parent     string
	VLANDevice string
	MAC        string
	GuestIP    string
	Gateway    string
	DHCP       bool
	VLAN       int
}

type vmNetworkCommandRunner func([]string) error

type vmNetworkCommandMode string

const (
	vmNetworkCommandModeSetup   vmNetworkCommandMode = "setup"
	vmNetworkCommandModeCleanup vmNetworkCommandMode = "cleanup"
)

func newVMNetworkPlan(service string, modes []string, in vmNetworkInputs) vmNetworkPlan {
	short := shortVMName(service)
	plan := vmNetworkPlan{Service: service}
	for _, mode := range vmNetworkModes(modes) {
		if mode == "" {
			continue
		}
		idx := len(plan.Interfaces)
		iface := newVMNetworkInterfacePlan(short, mode, idx, in)
		if iface.MAC == "" {
			iface.MAC = vmGuestMAC(service, mode, len(plan.Interfaces))
		}
		plan.Interfaces = append(plan.Interfaces, iface)
	}
	return plan
}

func newVMNetworkInterfacePlan(short, mode string, idx int, in vmNetworkInputs) vmNetworkInterfacePlan {
	iface := vmNetworkInterfacePlan{
		Mode:      mode,
		GuestName: fmt.Sprintf("eth%d", idx),
	}
	switch mode {
	case "svc":
		configureVMSvcNetworkInterface(&iface, short, idx, in)
	case "lan":
		configureVMLANNetworkInterface(&iface, short, idx, in)
	}
	return iface
}

func configureVMSvcNetworkInterface(iface *vmNetworkInterfacePlan, short string, idx int, in vmNetworkInputs) {
	iface.Tap = fmt.Sprintf("yvm-%s-s%d", short, idx)
	iface.Bridge = fmt.Sprintf("yvm-%s-b%d", short, idx)
	if strings.TrimSpace(in.ServiceIP) != "" {
		iface.GuestIP = strings.TrimSpace(in.ServiceIP) + "/24"
	}
	iface.Gateway = vmSvcGateway
}

func configureVMLANNetworkInterface(iface *vmNetworkInterfacePlan, short string, idx int, in vmNetworkInputs) {
	iface.Tap = fmt.Sprintf("yvm-%s-l%d", short, idx)
	iface.Parent = strings.TrimSpace(in.LANParent)
	if in.LANVLAN != 0 && strings.TrimSpace(in.LANBridge) != "" {
		iface.Bridge = strings.TrimSpace(in.LANBridge)
	} else if in.LANVLAN != 0 {
		iface.Bridge = fmt.Sprintf("yvm-%s-b%d", short, idx)
		iface.VLANDevice = fmt.Sprintf("yvm-%s-v%d", short, idx)
	} else if in.LANParentIsBridge {
		iface.Bridge = iface.Parent
	}
	iface.MAC = strings.TrimSpace(in.LANMAC)
	iface.DHCP = true
	iface.VLAN = in.LANVLAN
}

func (p vmNetworkPlan) DBNetworks() []db.VMNetworkConfig {
	out := make([]db.VMNetworkConfig, 0, len(p.Interfaces))
	for _, iface := range p.Interfaces {
		cfg := db.VMNetworkConfig{
			Mode:      iface.Mode,
			Interface: iface.GuestName,
			Tap:       iface.Tap,
			MAC:       iface.MAC,
			Parent:    iface.Parent,
			VLAN:      iface.VLAN,
		}
		if iface.GuestIP != "" {
			if pfx, err := netip.ParsePrefix(iface.GuestIP); err == nil {
				cfg.IP = pfx.Addr()
			}
		}
		out = append(out, cfg)
	}
	return out
}

func (p vmNetworkPlan) MetadataNetworks() []vmGuestNetwork {
	out := make([]vmGuestNetwork, 0, len(p.Interfaces))
	for _, iface := range p.Interfaces {
		out = append(out, vmGuestNetwork{
			Name:    iface.GuestName,
			Mode:    iface.Mode,
			Address: iface.GuestIP,
			Gateway: iface.Gateway,
			DHCP:    iface.DHCP,
		})
	}
	return out
}

func (p vmNetworkPlan) FirecrackerInterfaces() []firecrackerNetworkInterface {
	out := make([]firecrackerNetworkInterface, 0, len(p.Interfaces))
	for _, iface := range p.Interfaces {
		out = append(out, firecrackerNetworkInterface{
			IfaceID:     iface.GuestName,
			HostDevName: iface.Tap,
			GuestMAC:    iface.MAC,
		})
	}
	return out
}

func vmNetworkModes(modes []string) []string {
	var out []string
	for _, raw := range modes {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func shortVMName(service string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(service) {
		if out, ok := vmNameRune(r); ok {
			b.WriteRune(out)
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "v"
	}
	suffix := vmNetworkNameHash(service)
	baseLen := 8 - len(suffix) - 1
	if baseLen < 1 {
		baseLen = 1
	}
	if len(base) > baseLen {
		base = strings.Trim(base[:baseLen], "-")
		if base == "" {
			base = "v"
		}
	}
	return base + "-" + suffix
}

func vmNameRune(r rune) (rune, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return r, true
	case r >= '0' && r <= '9':
		return r, true
	case r == '-' || r == '_' || unicode.IsSpace(r) || r == '.':
		return '-', true
	default:
		return 0, false
	}
}

func vmNetworkNameHash(service string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(service))
	return fmt.Sprintf("%06x", h.Sum32()&0xffffff)
}

func vmGuestMAC(service, mode string, idx int) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(service))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(mode))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(idx)))
	sum := h.Sum32()
	return fmt.Sprintf("02:fc:%02x:%02x:%02x:%02x", byte(sum>>24), byte(sum>>16), byte(sum>>8), byte(sum))
}

func resolveVMLANNetworkInput(input *vmNetworkInputs) error {
	if input == nil {
		return fmt.Errorf("VM LAN network input is required")
	}
	if input.LANParent == "" {
		parent, err := hostDefaultRouteInterfaceFn()
		if err != nil {
			return fmt.Errorf("resolve VM LAN parent: %w", err)
		}
		input.LANParent = parent
	}
	if input.LANVLAN != 0 {
		parent, bridge, err := vmLANVLANParentAndBridge(input.LANParent, input.LANVLAN)
		if err != nil {
			return err
		}
		input.LANParent = parent
		input.LANBridge = bridge
	}
	input.LANParentIsBridge = vmLANParentIsBridge(input.LANParent)
	return nil
}

func vmLANVLANParentAndBridge(parent string, vlan int) (string, string, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "", "", fmt.Errorf("VM LAN network parent is required for VLAN")
	}
	if !vmLANParentIsBridge(parent) {
		bridge, ok, err := vmLANExistingVLANBridgeFn(parent, vlan)
		if err != nil {
			return "", "", err
		}
		if ok {
			return bridge, bridge, nil
		}
		return parent, "", nil
	}
	uplink, err := vmLANBridgeUplinkFn(parent)
	if err != nil {
		return "", "", fmt.Errorf("resolve VM LAN VLAN uplink for bridge %q: %w", parent, err)
	}
	bridge, ok, err := vmLANExistingVLANBridgeFn(uplink, vlan)
	if err != nil {
		return "", "", err
	}
	if ok {
		return bridge, bridge, nil
	}
	return uplink, "", nil
}

var vmLANBridgeUplinkFn = vmLANBridgeUplink

func vmLANBridgeUplink(bridge string) (string, error) {
	bridge = strings.TrimSpace(bridge)
	if bridge == "" {
		return "", fmt.Errorf("bridge name is required")
	}
	entries, err := os.ReadDir(filepath.Join("/sys/class/net", bridge, "brif"))
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return vmLANBridgeUplinkFromNames(names, vmNetDeviceHasHardware)
}

func vmLANBridgeUplinkFromNames(names []string, hasHardware func(string) bool) (string, error) {
	var hardware []string
	var fallback []string
	for _, name := range names {
		if skipVMBridgeUplinkCandidate(name) {
			continue
		}
		if hasHardware != nil && hasHardware(name) {
			hardware = append(hardware, name)
			continue
		}
		fallback = append(fallback, name)
	}
	sort.Strings(hardware)
	if len(hardware) > 0 {
		return hardware[0], nil
	}
	sort.Strings(fallback)
	if len(fallback) > 0 {
		return fallback[0], nil
	}
	return "", fmt.Errorf("no suitable bridge uplink found")
}

func skipVMBridgeUplinkCandidate(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "lo" {
		return true
	}
	for _, prefix := range []string{"yvm-", "tap", "veth", "br-", "docker"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func vmNetDeviceHasHardware(name string) bool {
	_, err := os.Stat(filepath.Join("/sys/class/net", name, "device"))
	return err == nil
}

var vmLANExistingVLANBridgeFn = vmLANExistingVLANBridge

func vmLANExistingVLANBridge(parent string, vlan int) (string, bool, error) {
	f, err := os.Open("/proc/net/vlan/config")
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer closeAndLog(f, "/proc/net/vlan/config")
	return vmLANExistingVLANBridgeFromConfig(parent, vlan, f, vmNetDeviceMaster)
}

func vmLANExistingVLANBridgeFromConfig(parent string, vlan int, r io.Reader, masterFn func(string) string) (string, bool, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" || vlan == 0 {
		return "", false, nil
	}
	wantVLAN := strconv.Itoa(vlan)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		name, ok := vmLANVLANConfigDevice(scanner.Text(), parent, wantVLAN)
		if !ok {
			continue
		}
		return vmLANBridgeForExistingVLANDevice(parent, wantVLAN, name, masterFn)
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func vmLANVLANConfigDevice(line, parent, wantVLAN string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[1] != "|" || fields[3] != "|" {
		return "", false
	}
	if fields[2] != wantVLAN || fields[4] != parent {
		return "", false
	}
	return fields[0], true
}

func vmLANBridgeForExistingVLANDevice(parent, vlan, name string, masterFn func(string) string) (string, bool, error) {
	master := ""
	if masterFn != nil {
		master = strings.TrimSpace(masterFn(name))
	}
	if master == "" {
		return "", false, fmt.Errorf("VLAN %s on %s already exists as %s but is not attached to a bridge", vlan, parent, name)
	}
	if !vmLANParentIsBridge(master) {
		return "", false, fmt.Errorf("VLAN %s on %s already exists as %s but master %s is not a bridge", vlan, parent, name, master)
	}
	return master, true, nil
}

func vmNetDeviceMaster(name string) string {
	target, err := os.Readlink(filepath.Join("/sys/class/net", strings.TrimSpace(name), "master"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func vmServiceGuestRoute(ip string) string {
	pfx, err := netip.ParsePrefix(strings.TrimSpace(ip))
	if err == nil {
		return pfx.Addr().String() + "/32"
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err == nil {
		return addr.String() + "/32"
	}
	return ""
}

func (p vmNetworkPlan) SetupCommands() [][]string {
	var cmds [][]string
	short := shortVMName(p.Service)
	for i, iface := range p.Interfaces {
		switch iface.Mode {
		case "svc":
			hostPeer := fmt.Sprintf("yvm-%s-v%d", short, i)
			nsPeer := fmt.Sprintf("yvm-%s-n%d", short, i)
			cmds = append(cmds,
				[]string{"ip", "link", "add", iface.Bridge, "type", "bridge"},
				[]string{"ip", "tuntap", "add", iface.Tap, "mode", "tap"},
				[]string{"ip", "link", "set", iface.Tap, "master", iface.Bridge},
				[]string{"ip", "addr", "del", vmSvcGateway + "/24", "dev", iface.Bridge},
				[]string{"ip", "addr", "add", vmSvcGateway + "/32", "dev", iface.Bridge},
				[]string{"ip", "link", "set", iface.Bridge, "up"},
				[]string{"ip", "link", "set", iface.Tap, "up"},
			)
			if route := vmServiceGuestRoute(iface.GuestIP); route != "" {
				cmds = append(cmds, []string{"ip", "route", "replace", route, "dev", iface.Bridge, "src", vmSvcGateway})
			}
			cmds = append(cmds,
				[]string{"ip", "link", "add", hostPeer, "type", "veth", "peer", "name", nsPeer},
				[]string{"ip", "link", "set", nsPeer, "netns", vmSvcNetNS},
				[]string{"ip", "link", "set", hostPeer, "master", iface.Bridge},
				[]string{"ip", "link", "set", hostPeer, "up"},
				[]string{"ip", "netns", "exec", vmSvcNetNS, "ip", "link", "set", nsPeer, "master", vmSvcNSBridge},
				[]string{"ip", "netns", "exec", vmSvcNetNS, "ip", "link", "set", nsPeer, "up"},
			)
		case "lan":
			if iface.VLAN != 0 && iface.VLANDevice != "" {
				cmds = append(cmds,
					[]string{"ip", "link", "add", "link", iface.Parent, "name", iface.VLANDevice, "type", "vlan", "id", strconv.Itoa(iface.VLAN)},
					[]string{"ip", "link", "add", iface.Bridge, "type", "bridge"},
					[]string{"ip", "link", "set", iface.VLANDevice, "master", iface.Bridge},
					[]string{"ip", "link", "set", iface.VLANDevice, "up"},
					[]string{"ip", "link", "set", iface.Bridge, "up"},
					[]string{"ip", "tuntap", "add", iface.Tap, "mode", "tap"},
					[]string{"ip", "link", "set", iface.Tap, "master", iface.Bridge},
					[]string{"ip", "link", "set", iface.Tap, "up"},
				)
				continue
			}
			if iface.Bridge == "" {
				cmds = append(cmds, unsupportedVMNetworkCommand(iface))
				continue
			}
			cmds = append(cmds,
				[]string{"ip", "tuntap", "add", iface.Tap, "mode", "tap"},
				[]string{"ip", "link", "set", iface.Tap, "master", iface.Bridge},
				[]string{"ip", "link", "set", iface.Tap, "up"},
			)
		}
	}
	return cmds
}

func (p vmNetworkPlan) CleanupCommands() [][]string {
	var cmds [][]string
	short := shortVMName(p.Service)
	for i := len(p.Interfaces) - 1; i >= 0; i-- {
		iface := p.Interfaces[i]
		switch iface.Mode {
		case "svc":
			hostPeer := fmt.Sprintf("yvm-%s-v%d", short, i)
			if route := vmServiceGuestRoute(iface.GuestIP); route != "" {
				cmds = append(cmds, []string{"ip", "route", "del", route, "dev", iface.Bridge})
			}
			cmds = append(cmds,
				[]string{"ip", "link", "del", hostPeer},
				[]string{"ip", "link", "del", iface.Tap},
				[]string{"ip", "link", "del", iface.Bridge},
			)
		case "lan":
			cmds = append(cmds, []string{"ip", "link", "del", iface.Tap})
			if iface.VLANDevice != "" {
				cmds = append(cmds,
					[]string{"ip", "link", "del", iface.VLANDevice},
					[]string{"ip", "link", "del", iface.Bridge},
				)
			}
		}
	}
	return cmds
}

func (p vmNetworkPlan) ExecuteSetup(run vmNetworkCommandRunner) error {
	if err := p.validateExecutable(); err != nil {
		return err
	}
	return runVMNetworkCommands(run, p.SetupCommands(), vmNetworkCommandModeSetup)
}

func (p vmNetworkPlan) ExecuteCleanup(run vmNetworkCommandRunner) error {
	return runVMNetworkCommands(run, p.CleanupCommands(), vmNetworkCommandModeCleanup)
}

func (p vmNetworkPlan) validateExecutable() error {
	for _, iface := range p.Interfaces {
		if iface.Mode != "lan" {
			continue
		}
		if iface.VLAN != 0 {
			if iface.Bridge == "" {
				return fmt.Errorf("VM LAN network bridge is required for VLAN %d", iface.VLAN)
			}
			if iface.VLANDevice != "" && iface.Parent == "" {
				return fmt.Errorf("VM LAN network parent is required for VLAN %d", iface.VLAN)
			}
			continue
		}
		if iface.Bridge == "" {
			if iface.Parent == "" {
				return fmt.Errorf("VM LAN network parent is required; non-bridge LAN parents are unsupported")
			}
			return fmt.Errorf("VM LAN network parent %q is not a bridge; non-bridge LAN parents are unsupported", iface.Parent)
		}
	}
	return nil
}

func runVMNetworkCommands(run vmNetworkCommandRunner, cmds [][]string, mode vmNetworkCommandMode) error {
	if run == nil {
		run = execVMNetworkCommand
	}
	for _, cmd := range cmds {
		if len(cmd) == 0 {
			continue
		}
		if err := run(cmd); err != nil {
			if isIgnorableVMNetworkCommandError(mode, cmd, err) {
				continue
			}
			return fmt.Errorf("run %q: %w", strings.Join(cmd, " "), err)
		}
	}
	return nil
}

func execVMNetworkCommand(args []string) error {
	if len(args) == 0 {
		return nil
	}
	output, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err == nil {
		return nil
	}
	if len(output) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
}

func unsupportedVMNetworkCommand(iface vmNetworkInterfacePlan) []string {
	parent := iface.Parent
	if parent == "" {
		parent = "<empty>"
	}
	return []string{"yeet-vm-network-unsupported", fmt.Sprintf("VM LAN network parent %q is not a bridge; non-bridge LAN parents are unsupported", parent)}
}

func isIgnorableVMNetworkCommandError(mode vmNetworkCommandMode, cmd []string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	switch mode {
	case vmNetworkCommandModeSetup:
		return isIgnorableVMNetworkSetupError(cmd, text)
	case vmNetworkCommandModeCleanup:
		return isIdempotentVMNetworkCleanupCommand(cmd) && vmNetworkMissingDeviceError(text)
	default:
		return false
	}
}

func isIgnorableVMNetworkSetupError(cmd []string, text string) bool {
	if isVMSvcLegacyGatewayDeleteCommand(cmd) && vmNetworkMissingAddressError(text) {
		return true
	}
	if isIdempotentVMNetworkSetupCommand(cmd) && vmNetworkAlreadyConfiguredError(text) {
		return true
	}
	return isReplayVMNetworkNamespaceMove(cmd) && vmNetworkMissingDeviceError(text)
}

func vmNetworkAlreadyConfiguredError(text string) bool {
	return strings.Contains(text, "exists") ||
		strings.Contains(text, "address already assigned") ||
		strings.Contains(text, "device or resource busy")
}

func isIdempotentVMNetworkSetupCommand(cmd []string) bool {
	if len(cmd) < 4 || cmd[0] != "ip" {
		return false
	}
	if isVMNetworkVLANAddCommand(cmd) {
		return false
	}
	return len(cmd) >= 5 && vmNetworkSetupVerb(cmd[1], cmd[2])
}

func isVMNetworkVLANAddCommand(cmd []string) bool {
	if len(cmd) < 10 {
		return false
	}
	return cmd[0] == "ip" &&
		cmd[1] == "link" &&
		cmd[2] == "add" &&
		cmd[3] == "link" &&
		slicesContains(cmd, "type") &&
		slicesContains(cmd, "vlan")
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func vmNetworkSetupVerb(group, action string) bool {
	switch group + "/" + action {
	case "link/add", "tuntap/add", "addr/add":
		return true
	default:
		return false
	}
}

func isIdempotentVMNetworkCleanupCommand(cmd []string) bool {
	return len(cmd) >= 4 && cmd[0] == "ip" && (cmd[1] == "link" || cmd[1] == "route") && cmd[2] == "del"
}

func vmNetworkMissingDeviceError(text string) bool {
	return strings.Contains(text, "cannot find device") ||
		strings.Contains(text, "does not exist") ||
		strings.Contains(text, "no such device") ||
		strings.Contains(text, "no such process")
}

func vmNetworkMissingAddressError(text string) bool {
	return vmNetworkMissingDeviceError(text) ||
		strings.Contains(text, "cannot assign requested address") ||
		strings.Contains(text, "address not found")
}

func isVMSvcLegacyGatewayDeleteCommand(cmd []string) bool {
	return len(cmd) == 6 &&
		cmd[0] == "ip" &&
		cmd[1] == "addr" &&
		cmd[2] == "del" &&
		cmd[3] == vmSvcGateway+"/24" &&
		cmd[4] == "dev"
}

func isReplayVMNetworkNamespaceMove(cmd []string) bool {
	return len(cmd) >= 6 &&
		cmd[0] == "ip" &&
		cmd[1] == "link" &&
		cmd[2] == "set" &&
		strings.HasPrefix(cmd[3], "yvm-") &&
		cmd[4] == "netns" &&
		cmd[5] == vmSvcNetNS
}
