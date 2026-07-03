// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"errors"
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
	"github.com/yeetrun/yeet/pkg/netns"
)

const (
	vmSvcGuestGateway  = netns.ServiceHostIP
	vmSvcBridgeAddress = netns.ServiceGatewayIP
	vmSvcNetNS         = "yeet-ns"
	vmSvcNSBridge      = "br0"
)

var errVMLANBridgePreparationRequired = errors.New("VM LAN bridge preparation required")
var resolveHostVMLANBridgeFn = resolveHostVMLANBridge

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
	plan.applyGuestRoutePolicy()
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
	iface.Gateway = vmSvcGuestGateway
}

func configureVMLANNetworkInterface(iface *vmNetworkInterfacePlan, short string, idx int, in vmNetworkInputs) {
	iface.Tap = fmt.Sprintf("yvm-%s-l%d", short, idx)
	iface.Parent = strings.TrimSpace(in.LANParent)
	if in.LANVLAN != 0 && strings.TrimSpace(in.LANBridge) != "" {
		iface.Bridge = strings.TrimSpace(in.LANBridge)
	} else if in.LANVLAN != 0 {
		iface.Bridge = vmGeneratedVLANBridgeName(iface.Parent, in.LANVLAN)
		iface.VLANDevice = vmGeneratedVLANDeviceName(iface.Parent, in.LANVLAN)
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
	hasLAN := p.hasNetworkMode("lan")
	for _, iface := range p.Interfaces {
		var dnsDefaultRoute *bool
		if hasLAN && iface.Mode == "svc" {
			value := false
			dnsDefaultRoute = &value
		}
		out = append(out, vmGuestNetwork{
			Name:            iface.GuestName,
			Mode:            iface.Mode,
			Address:         iface.GuestIP,
			Gateway:         iface.Gateway,
			DHCP:            iface.DHCP,
			DNSDefaultRoute: dnsDefaultRoute,
		})
	}
	return out
}

func (p vmNetworkPlan) hasNetworkMode(mode string) bool {
	for _, iface := range p.Interfaces {
		if iface.Mode == mode {
			return true
		}
	}
	return false
}

func (p *vmNetworkPlan) applyGuestRoutePolicy() {
	if !p.hasNetworkMode("lan") {
		return
	}
	for i := range p.Interfaces {
		if p.Interfaces[i].Mode == "svc" {
			p.Interfaces[i].Gateway = ""
		}
	}
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

func vmGeneratedVLANBase(parent string, vlan int) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSpace(parent)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(vlan)))
	return fmt.Sprintf("yvm-%08x", h.Sum32())
}

func vmGeneratedVLANBridgeName(parent string, vlan int) string {
	return vmGeneratedVLANBase(parent, vlan) + "-b0"
}

func vmGeneratedVLANDeviceName(parent string, vlan int) string {
	return vmGeneratedVLANBase(parent, vlan) + "-v0"
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
		if err := resolveDefaultVMLANParent(input); err != nil {
			return err
		}
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

func resolveDefaultVMLANParent(input *vmNetworkInputs) error {
	plan, err := resolveHostVMLANBridgeFn()
	if err != nil {
		if plan.NeedsPrepare || errors.Is(err, errVMLANBridgePreparationRequired) {
			return vmLANBridgePreparationRequiredError(plan, err)
		}
		return fmt.Errorf("resolve VM LAN bridge: %w", err)
	}
	if plan.Ready {
		bridge := strings.TrimSpace(plan.Bridge)
		if bridge == "" {
			return fmt.Errorf("resolve VM LAN bridge: ready plan did not select a bridge")
		}
		input.LANParent = bridge
		input.LANParentIsBridge = true
		return nil
	}
	if plan.NeedsPrepare {
		return vmLANBridgePreparationRequiredError(plan, nil)
	}
	return fmt.Errorf("resolve VM LAN bridge: no usable bridge selected")
}

func resolveHostVMLANBridge() (vmLANBridgePlan, error) {
	plan, err := PlanVMLANBridge("")
	if err != nil {
		return vmLANBridgePlan{}, err
	}
	return vmLANBridgePlan{
		Ready:        plan.Ready,
		NeedsPrepare: plan.NeedsPrepare,
		Bridge:       plan.Bridge,
		Parent:       plan.Parent,
		Renderer: vmLANRenderer{
			Name:      plan.Renderer.Name,
			Supported: plan.Renderer.Supported,
			Reason:    plan.Renderer.Reason,
		},
		Reason: plan.Reason,
	}, nil
}

func vmLANBridgePreparationRequiredError(plan vmLANBridgePlan, cause error) error {
	cause = vmLANBridgePreparationCause(cause)
	bridge := strings.TrimSpace(plan.Bridge)
	parent := strings.TrimSpace(plan.Parent)
	switch {
	case bridge != "" && parent != "":
		return fmt.Errorf("VM LAN bridge preparation required for bridge %q from parent %q: %w", bridge, parent, cause)
	case bridge != "":
		return fmt.Errorf("VM LAN bridge preparation required for bridge %q: %w", bridge, cause)
	case parent != "":
		return fmt.Errorf("VM LAN bridge preparation required from parent %q: %w", parent, cause)
	default:
		return fmt.Errorf("VM LAN bridge preparation required: %w", cause)
	}
}

func vmLANBridgePreparationCause(cause error) error {
	if cause == nil {
		return errVMLANBridgePreparationRequired
	}
	if errors.Is(cause, errVMLANBridgePreparationRequired) {
		return cause
	}
	return errors.Join(errVMLANBridgePreparationRequired, cause)
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
			if bridge == vmGeneratedVLANBridgeName(parent, vlan) {
				return parent, "", nil
			}
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
		if bridge == vmGeneratedVLANBridgeName(uplink, vlan) {
			return uplink, "", nil
		}
		return bridge, bridge, nil
	}
	return uplink, "", nil
}

var vmLANBridgeUplinkFn = vmLANBridgeUplink
var vmLANLiveBridgeExistsFn = vmLANLiveBridgeExists

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

func vmLANLiveBridgeExists(bridge string) bool {
	bridge = strings.TrimSpace(bridge)
	if bridge == "" {
		return false
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", bridge, "bridge"))
	return err == nil
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
	parts := strings.Split(line, "|")
	if len(parts) < 3 {
		return "", false
	}
	name := strings.TrimSpace(parts[0])
	vlanFields := strings.Fields(parts[1])
	parentFields := strings.Fields(parts[2])
	if name == "" || len(vlanFields) == 0 || len(parentFields) == 0 {
		return "", false
	}
	if vlanFields[0] != wantVLAN || parentFields[0] != parent {
		return "", false
	}
	return name, true
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
				[]string{"ip", "addr", "del", vmSvcBridgeAddress + "/24", "dev", iface.Bridge},
				[]string{"ip", "addr", "del", vmSvcBridgeAddress + "/32", "dev", iface.Bridge},
				[]string{"ip", "link", "set", iface.Bridge, "up"},
				[]string{"ip", "link", "set", iface.Tap, "up"},
			)
			if route := vmServiceGuestRoute(iface.GuestIP); route != "" {
				cmds = append(cmds, []string{"ip", "route", "del", route, "dev", iface.Bridge})
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
		}
	}
	return cmds
}

func vmNetworkLinkBase(name string) (string, string, bool) {
	name = strings.TrimSpace(strings.SplitN(name, "@", 2)[0])
	if !strings.HasPrefix(name, "yvm-") {
		return "", "", false
	}
	lastDash := strings.LastIndex(name, "-")
	if lastDash <= len("yvm-") || lastDash == len(name)-1 {
		return "", "", false
	}
	suffix := name[lastDash+1:]
	if len(suffix) < 2 || !strings.ContainsRune("bsvnl", rune(suffix[0])) {
		return "", "", false
	}
	for _, r := range suffix[1:] {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return name[:lastDash], suffix, true
}

func vmServiceNetworkLinkBase(name string) (string, string, bool) {
	return vmNetworkLinkBase(name)
}

var vmNetworkExistingVLANDeviceMatchesFn = vmNetworkExistingVLANDeviceMatches

func vmNetworkExistingVLANDeviceMatches(parent, name string, vlan int) (bool, error) {
	f, err := os.Open("/proc/net/vlan/config")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closeAndLog(f, "/proc/net/vlan/config")
	return vmNetworkExistingVLANDeviceMatchesFromConfig(parent, name, vlan, f)
}

func vmNetworkExistingVLANDeviceMatchesFromConfig(parent, name string, vlan int, r io.Reader) (bool, error) {
	parent = strings.TrimSpace(parent)
	name = strings.TrimSpace(name)
	if parent == "" || name == "" || vlan == 0 {
		return false, nil
	}
	wantVLAN := strconv.Itoa(vlan)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		gotName, ok := vmLANVLANConfigDevice(scanner.Text(), parent, wantVLAN)
		if ok && gotName == name {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
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
				return fmt.Errorf("VM LAN network parent is required; prepare a host bridge with `yeet init` or run `yeet run ... --net=lan` interactively so yeet can prepare one")
			}
			return fmt.Errorf("VM LAN network parent %q is not a bridge; prepare a host bridge with `yeet init` or recreate/update the VM with `yeet run ... --net=lan` interactively so yeet can prepare one", iface.Parent)
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
	if isVMSvcGatewayDeleteCommand(cmd) && vmNetworkMissingAddressError(text) {
		return true
	}
	if isVMSvcGuestRouteDeleteCommand(cmd) && vmNetworkMissingDeviceError(text) {
		return true
	}
	if vmNetworkAlreadyConfiguredError(text) {
		if isVMNetworkVLANAddCommand(cmd) {
			return existingVMNetworkVLANAddCommandMatches(cmd)
		}
		if isIdempotentVMNetworkSetupCommand(cmd) {
			return true
		}
	}
	return isReplayVMNetworkNamespaceMove(cmd) && vmNetworkMissingDeviceError(text)
}

func existingVMNetworkVLANAddCommandMatches(cmd []string) bool {
	parent, name, vlan, ok := vmNetworkVLANAddCommandDetails(cmd)
	if !ok {
		return false
	}
	matches, err := vmNetworkExistingVLANDeviceMatchesFn(parent, name, vlan)
	return err == nil && matches
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
	return len(cmd) >= 5 && vmNetworkSetupVerb(cmd[1], cmd[2])
}

func isVMNetworkVLANAddCommand(cmd []string) bool {
	_, _, _, ok := vmNetworkVLANAddCommandDetails(cmd)
	return ok
}

var vmNetworkVLANAddCommandFixedArgs = []struct {
	idx   int
	value string
}{
	{0, "ip"},
	{1, "link"},
	{2, "add"},
	{3, "link"},
	{5, "name"},
	{7, "type"},
	{8, "vlan"},
	{9, "id"},
}

func vmNetworkVLANAddCommandDetails(cmd []string) (string, string, int, bool) {
	if !vmNetworkVLANAddCommandShape(cmd) {
		return "", "", 0, false
	}
	parent := strings.TrimSpace(cmd[4])
	name := strings.TrimSpace(cmd[6])
	vlan, ok := vmNetworkVLANID(cmd[10])
	if parent == "" || !ok || !vmNetworkGeneratedVLANDeviceName(name) {
		return "", "", 0, false
	}
	return parent, name, vlan, true
}

func vmNetworkVLANAddCommandShape(cmd []string) bool {
	if len(cmd) != 11 {
		return false
	}
	for _, fixed := range vmNetworkVLANAddCommandFixedArgs {
		if cmd[fixed.idx] != fixed.value {
			return false
		}
	}
	return true
}

func vmNetworkVLANID(raw string) (int, bool) {
	vlan, err := strconv.Atoi(raw)
	return vlan, err == nil && vlan != 0
}

func vmNetworkGeneratedVLANDeviceName(name string) bool {
	if _, suffix, ok := vmNetworkLinkBase(name); !ok || suffix[0] != 'v' {
		return false
	}
	return true
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
	return isRootVMNetworkDeleteCommand(cmd) || isNetNSVMNetworkDeleteCommand(cmd)
}

func isRootVMNetworkDeleteCommand(cmd []string) bool {
	return len(cmd) >= 4 &&
		cmd[0] == "ip" &&
		(cmd[1] == "link" || cmd[1] == "route") &&
		cmd[2] == "del"
}

func isNetNSVMNetworkDeleteCommand(cmd []string) bool {
	return len(cmd) >= 8 &&
		cmd[0] == "ip" &&
		cmd[1] == "netns" &&
		cmd[2] == "exec" &&
		cmd[3] == vmSvcNetNS &&
		cmd[4] == "ip" &&
		(cmd[5] == "link" || cmd[5] == "route") &&
		cmd[6] == "del"
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

func isVMSvcGatewayDeleteCommand(cmd []string) bool {
	return len(cmd) == 6 &&
		cmd[0] == "ip" &&
		cmd[1] == "addr" &&
		cmd[2] == "del" &&
		(cmd[3] == vmSvcBridgeAddress+"/24" || cmd[3] == vmSvcBridgeAddress+"/32") &&
		cmd[4] == "dev"
}

func isVMSvcGuestRouteDeleteCommand(cmd []string) bool {
	if len(cmd) != 6 ||
		cmd[0] != "ip" ||
		cmd[1] != "route" ||
		cmd[2] != "del" ||
		cmd[4] != "dev" {
		return false
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(cmd[3]))
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 {
		return false
	}
	_, suffix, ok := vmNetworkLinkBase(cmd[5])
	return ok && strings.HasPrefix(suffix, "b")
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
