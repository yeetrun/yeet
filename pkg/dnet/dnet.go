// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dnet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/vishvananda/netns"
	"github.com/yeetrun/yeet/pkg/db"
	"tailscale.com/syncs"
	"tailscale.com/types/ptr"
	"tailscale.com/util/mak"
)

type plugin struct {
	db *db.Store

	// netnsSema ensures that only one goroutine is running in a given network namespace at a time.
	netnsSema syncs.Map[string, *syncs.Semaphore]
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Err string `json:"Err"`
}

// SuccessResponse represents a successful response
type SuccessResponse struct {
	Err string `json:"Err"`
}

// runInNetNS runs the given function in the given network namespace.
func (p *plugin) runInNetNS(nsName string, f func() error) error {
	errChan := make(chan error)

	go func() (err error) {
		sem, _ := p.netnsSema.LoadOrInit(nsName, func() *syncs.Semaphore { return ptr.To(syncs.NewSemaphore(1)) })
		sem.Acquire()
		defer sem.Release()
		// Lock the OS thread to ensure the netns change affects this goroutine only
		runtime.LockOSThread()
		defer func() {
			if err == nil {
				runtime.UnlockOSThread()
			}
			errChan <- err
		}()

		// Open the network namespace
		netnsFile, err := os.Open(nsName)
		if err != nil {
			return fmt.Errorf("failed to open netns: %v", err)
		}
		defer netnsFile.Close()

		// Save the current network namespace
		currentNetns, err := netns.Get()
		if err != nil {
			return fmt.Errorf("failed to get current netns: %v", err)
		}
		defer currentNetns.Close()

		// Set the process to the new network namespace
		if err := netns.Set(netns.NsHandle(netnsFile.Fd())); err != nil {
			return fmt.Errorf("failed to set netns: %v", err)
		}

		// Execute any additional setup (e.g., setting up interfaces or IP addresses)
		if err := f(); err != nil {
			return fmt.Errorf("failed to execute command: %v", err)
		}

		// Restore the original network namespace
		if err := netns.Set(currentNetns); err != nil {
			return fmt.Errorf("failed to restore netns: %v", err)
		}

		return nil
	}()

	// Wait for the goroutine to finish
	if err := <-errChan; err != nil {
		return fmt.Errorf("failed to run in netns: %w", err)
	}
	return nil
}

func runCmd(name string, args ...string) error {
	args = withCommandDefaults(name, args)
	fmt.Printf("running %s %v\n", name, args)
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run %s %v: %w", name, args, err)
	}
	return nil
}

func withCommandDefaults(name string, args []string) []string {
	if name != "iptables" {
		return args
	}
	for _, arg := range args {
		if arg == "-w" || arg == "--wait" {
			return args
		}
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, "-w")
	out = append(out, args...)
	return out
}

const (
	postroutingChainName = "YEET_POSTROUTING"
	preroutingChainName  = "YEET_PREROUTING"
	outputChainName      = "YEET_OUTPUT"
)

func ensurePreroutingChain() error {
	if err := runCmd("iptables", "-t", "nat", "-L", preroutingChainName); err != nil {
		if err := runCmd("iptables", "-t", "nat", "-N", preroutingChainName); err != nil {
			return err
		}
	}
	if err := runCmd("iptables", "-t", "nat", "-C", "PREROUTING", "-j", preroutingChainName); err == nil {
		return nil
	}
	if err := runCmd("iptables", "-t", "nat", "-A", "PREROUTING", "-j", preroutingChainName); err != nil {
		return err
	}
	return nil
}

func ensureOutputChain() error {
	if err := runCmd("iptables", "-t", "nat", "-L", outputChainName); err != nil {
		if err := runCmd("iptables", "-t", "nat", "-N", outputChainName); err != nil {
			return err
		}
	}
	if err := runCmd("iptables", "-t", "nat", "-C", "OUTPUT", "-o", "lo", "-j", outputChainName); err == nil {
		return nil
	}
	if err := runCmd("iptables", "-t", "nat", "-A", "OUTPUT", "-o", "lo", "-j", outputChainName); err != nil {
		return err
	}
	return nil
}

func ensurePostroutingChain() error {
	if err := runCmd("iptables", "-t", "nat", "-L", postroutingChainName); err != nil {
		if err := runCmd("iptables", "-t", "nat", "-N", postroutingChainName); err != nil {
			return err
		}
	}
	if err := runCmd("iptables", "-t", "nat", "-C", "POSTROUTING", "-j", postroutingChainName); err != nil {
		if err := runCmd("iptables", "-t", "nat", "-A", "POSTROUTING", "-j", postroutingChainName); err != nil {
			return err
		}
	}
	if err := runCmd("iptables", "-t", "nat", "-C", postroutingChainName, "-m", "addrtype", "!", "--src-type", "LOCAL", "-o", "br0", "-j", "RETURN"); err != nil {
		if err := runCmd("iptables", "-t", "nat", "-I", postroutingChainName, "-m", "addrtype", "!", "--src-type", "LOCAL", "-o", "br0", "-j", "RETURN"); err != nil {
			return err
		}
	}
	if err := runCmd("iptables", "-t", "nat", "-C", postroutingChainName, "-j", "MASQUERADE"); err != nil {
		if err := runCmd("iptables", "-t", "nat", "-A", postroutingChainName, "-j", "MASQUERADE"); err != nil {
			return err
		}
	}
	return nil
}

type portForwardRule struct {
	Proto      string
	HostPort   uint16
	TargetIP   string
	TargetPort uint16
}

type natRuleBackend interface {
	ListChain(chain string) ([]string, error)
	FlushChain(chain string) error
	AppendRule(chain string, rule ...string) error
	DeleteRule(chain string, rule ...string) error
	EnsureChains() error
}

type iptablesBackend struct{}

func (iptablesBackend) ListChain(chain string) ([]string, error) {
	cmd := exec.Command("iptables", withCommandDefaults("iptables", []string{"-t", "nat", "-S", chain})...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list chain %q: %w", chain, err)
	}
	return splitNonEmptyLines(string(output)), nil
}

func (iptablesBackend) FlushChain(chain string) error {
	return runCmd("iptables", "-t", "nat", "-F", chain)
}

func (iptablesBackend) AppendRule(chain string, rule ...string) error {
	args := []string{"-t", "nat", "-A", chain}
	args = append(args, rule...)
	return runCmd("iptables", args...)
}

func (iptablesBackend) DeleteRule(chain string, rule ...string) error {
	args := []string{"-t", "nat", "-D", chain}
	args = append(args, rule...)
	return runCmd("iptables", args...)
}

func (iptablesBackend) EnsureChains() error {
	if err := ensurePreroutingChain(); err != nil {
		return err
	}
	return ensureOutputChain()
}

func desiredPortForwards(n *db.DockerNetwork) []portForwardRule {
	if n == nil {
		return nil
	}
	rules := make([]portForwardRule, 0, len(n.PortMap))
	for key, pm := range n.PortMap {
		if pm == nil {
			continue
		}
		ep, ok := n.Endpoints[pm.EndpointID]
		if !ok || ep == nil {
			continue
		}
		var hpProto db.ProtoPort
		if err := hpProto.Parse(key); err != nil {
			continue
		}
		proto := protoName(hpProto.Proto)
		if proto == "" {
			continue
		}
		rules = append(rules, portForwardRule{
			Proto:      proto,
			HostPort:   hpProto.Port,
			TargetIP:   ep.IPv4.Addr().String(),
			TargetPort: pm.Port,
		})
	}
	sortPortForwardRules(rules)
	return rules
}

func sortPortForwardRules(rules []portForwardRule) {
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Proto != rules[j].Proto {
			return rules[i].Proto < rules[j].Proto
		}
		if rules[i].HostPort != rules[j].HostPort {
			return rules[i].HostPort < rules[j].HostPort
		}
		if rules[i].TargetIP != rules[j].TargetIP {
			return rules[i].TargetIP < rules[j].TargetIP
		}
		return rules[i].TargetPort < rules[j].TargetPort
	})
}

func dedupePortForwardRules(rules []portForwardRule) []portForwardRule {
	if len(rules) == 0 {
		return nil
	}
	sortPortForwardRules(rules)
	out := rules[:0]
	for _, rule := range rules {
		if len(out) > 0 && out[len(out)-1] == rule {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func desiredPortForwardsForNetNS(d *db.Data, netns string) []portForwardRule {
	if d == nil || netns == "" {
		return nil
	}
	var rules []portForwardRule
	for _, network := range d.DockerNetworks {
		if network == nil || network.NetNS != netns {
			continue
		}
		rules = append(rules, desiredPortForwards(network)...)
	}
	return dedupePortForwardRules(rules)
}

func desiredPortForwardsByNetNS(d *db.Data) map[string][]portForwardRule {
	out := map[string][]portForwardRule{}
	if d == nil {
		return out
	}
	for _, network := range d.DockerNetworks {
		if network == nil || network.NetNS == "" {
			continue
		}
		if _, ok := out[network.NetNS]; !ok {
			out[network.NetNS] = nil
		}
	}
	for netns := range out {
		out[netns] = desiredPortForwardsForNetNS(d, netns)
	}
	return out
}

func removeEndpointPortMappings(n *db.DockerNetwork, endpointID string) {
	if n == nil {
		return
	}
	for key, pm := range n.PortMap {
		if pm != nil && pm.EndpointID == endpointID {
			delete(n.PortMap, key)
		}
	}
}

func syncNetNSPortForwards(netns string, desired []portForwardRule, backend natRuleBackend) error {
	if err := backend.EnsureChains(); err != nil {
		return fmt.Errorf("ensure yeet nat chains for %q: %w", netns, err)
	}

	outputRules, err := backend.ListChain("OUTPUT")
	if err != nil {
		return fmt.Errorf("list output rules for %q: %w", netns, err)
	}
	for _, rule := range outputRules {
		if !isLegacyDirectOutputRule(rule) {
			continue
		}
		args, err := chainRuleArgs(rule, "OUTPUT")
		if err != nil {
			return fmt.Errorf("parse legacy output rule %q for %q: %w", rule, netns, err)
		}
		if err := backend.DeleteRule("OUTPUT", args...); err != nil {
			return fmt.Errorf("delete legacy output rule %q for %q: %w", rule, netns, err)
		}
	}

	if err := backend.FlushChain(preroutingChainName); err != nil {
		return fmt.Errorf("flush prerouting chain for %q: %w", netns, err)
	}
	if err := backend.FlushChain(outputChainName); err != nil {
		return fmt.Errorf("flush output chain for %q: %w", netns, err)
	}
	if err := backend.AppendRule(preroutingChainName, "-i", "br0", "-j", "RETURN"); err != nil {
		return fmt.Errorf("append bridge guard for %q: %w", netns, err)
	}
	for _, rule := range desired {
		dnat := dnatRuleArgs(rule)
		if err := backend.AppendRule(preroutingChainName, dnat...); err != nil {
			return fmt.Errorf("append prerouting rule for %q: %w", netns, err)
		}
		if err := backend.AppendRule(outputChainName, dnat...); err != nil {
			return fmt.Errorf("append output rule for %q: %w", netns, err)
		}
	}
	return nil
}

func protoName(proto int) string {
	switch proto {
	case 6:
		return "tcp"
	case 17:
		return "udp"
	default:
		return ""
	}
}

func dnatRuleArgs(rule portForwardRule) []string {
	return []string{
		"-p", rule.Proto,
		"-m", rule.Proto,
		"--dport", strconv.Itoa(int(rule.HostPort)),
		"-j", "DNAT",
		"--to-destination", net.JoinHostPort(rule.TargetIP, strconv.Itoa(int(rule.TargetPort))),
	}
}

func isLegacyDirectOutputRule(rule string) bool {
	fields := strings.Fields(rule)
	if len(fields) < 3 || fields[0] != "-A" || fields[1] != "OUTPUT" {
		return false
	}
	hasLoopbackOut := false
	hasDNAT := false
	for i := 2; i < len(fields)-1; i++ {
		switch fields[i] {
		case "-o":
			if fields[i+1] == "lo" {
				hasLoopbackOut = true
			}
		case "-j":
			if fields[i+1] == "DNAT" {
				hasDNAT = true
			}
		}
	}
	return hasLoopbackOut && hasDNAT
}

func chainRuleArgs(rule, chain string) ([]string, error) {
	fields := strings.Fields(rule)
	if len(fields) < 3 || fields[0] != "-A" || fields[1] != chain {
		return nil, fmt.Errorf("unexpected chain rule %q", rule)
	}
	return fields[2:], nil
}

func splitNonEmptyLines(output string) []string {
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (p *plugin) syncPortForwards(netns string, desired []portForwardRule) error {
	return p.runInNetNS(netns, func() error {
		return syncNetNSPortForwards(netns, desired, iptablesBackend{})
	})
}

func (p *plugin) LeaveNetwork(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	nid := req["NetworkID"].(string)
	eid := req["EndpointID"].(string)
	ifName := "yv-" + eid[:4]
	var netns string
	var desired []portForwardRule
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[nid]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		_, ok = n.Endpoints[eid]
		if !ok {
			return fmt.Errorf("endpoint not found")
		}
		delete(n.Endpoints, eid)
		removeEndpointPortMappings(n, eid)
		desired = desiredPortForwards(n)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := p.runInNetNS(netns, func() error {
		var errs []error
		if err := syncNetNSPortForwards(netns, desired, iptablesBackend{}); err != nil {
			errs = append(errs, err)
		}
		if err := runCmd("ip", "link", "del", ifName); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(SuccessResponse{})
}

type portMap struct {
	Proto       int    `json:"Proto"`
	IP          string `json:"IP"`
	Port        uint16 `json:"Port"`
	HostIP      string `json:"HostIP"`
	HostPort    uint16 `json:"HostPort"`
	HostPortEnd uint16 `json:"HostPortEnd"`
}

func ensureBridge(addr netip.Prefix) error {
	if err := runCmd("ip", "link", "show", "br0"); err == nil {
		return nil
	}
	if err := runCmd("ip", "link", "add", "br0", "type", "bridge"); err != nil {
		return err
	}
	if err := runCmd("ip", "link", "set", "br0", "up"); err != nil {
		return err
	}
	if err := runCmd("ip", "addr", "add", addr.String(), "dev", "br0"); err != nil {
		return err
	}
	if err := runCmd("sysctl", "-w", "net.ipv4.conf.br0.route_localnet=1"); err != nil {
		return err
	}
	return nil
}

func (p *plugin) JoinNetwork(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req struct {
		NetworkID  string `json:"NetworkID"`
		EndpointID string `json:"EndpointID"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	nid := req.NetworkID
	eid := req.EndpointID

	var netns string
	dv, err := p.db.Get()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d := dv.AsStruct()
	n, ok := d.DockerNetworks[nid]
	if !ok {
		http.Error(w, "network not found", http.StatusBadRequest)
		return
	}
	if _, ok := n.Endpoints[eid]; !ok {
		http.Error(w, "endpoint not found", http.StatusBadRequest)
		return
	}
	gateway := n.IPv4Gateway.Addr()
	gatewayPrefix := n.IPv4Gateway
	netns = n.NetNS
	desired := desiredPortForwards(n)

	ifName := "yv-" + eid[:4]
	peerName := ifName + "p"
	if err := runCmd("ip", "link", "add", ifName, "type", "veth", "peer", "name", peerName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := runCmd("ip", "link", "set", ifName, "netns", netns); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := p.runInNetNS(netns, func() error {
		if err := ensureBridge(gatewayPrefix); err != nil {
			return err
		}
		if err := runCmd("ip", "link", "set", ifName, "master", "br0"); err != nil {
			return err
		}
		if err := runCmd("ip", "link", "set", ifName, "up"); err != nil {
			return err
		}
		if err := ensurePostroutingChain(); err != nil {
			return err
		}
		return syncNetNSPortForwards(netns, desired, iptablesBackend{})
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]any{
		"InterfaceName": map[string]string{
			"SrcName":   peerName,
			"DstPrefix": "eth",
		},
		"Gateway": gateway.String(),
	}
	json.NewEncoder(w).Encode(resp)
}

func (p *plugin) DeleteNetwork(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req struct {
		NetworkID string `json:"NetworkID"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := p.db.MutateData(func(d *db.Data) error {
		dn, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		if len(dn.Endpoints) > 0 {
			return fmt.Errorf("network still has endpoints")
		}
		delete(d.DockerNetworks, req.NetworkID)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(SuccessResponse{})
}

// CreateNetwork creates a network
func (p *plugin) CreateNetwork(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req struct {
		NetworkID string `json:"NetworkID"`
		Options   struct {
			Generic struct {
				NetNS string `json:"dev.catchit.netns"`
			} `json:"com.docker.network.generic"`
		} `json:"Options"`
		IPv4Data []struct {
			AddressSpace string       `json:"AddressSpace"`
			Gateway      netip.Prefix `json:"Gateway"`
			Pool         netip.Prefix `json:"Pool"`
		} `json:"IPv4Data"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Options.Generic.NetNS == "" {
		http.Error(w, "NetNS is required", http.StatusBadRequest)
		return
	}
	if len(req.IPv4Data) == 0 {
		http.Error(w, "IPv4Data is required", http.StatusBadRequest)
		return
	}
	if _, err := p.db.MutateData(func(d *db.Data) error {
		if _, ok := d.DockerNetworks[req.NetworkID]; ok {
			return fmt.Errorf("network already exists")
		}
		mak.Set(&d.DockerNetworks, req.NetworkID, &db.DockerNetwork{
			NetNS:       req.Options.Generic.NetNS,
			NetworkID:   req.NetworkID,
			IPv4Gateway: req.IPv4Data[0].Gateway,
			IPv4Range:   req.IPv4Data[0].Pool,
		})
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(SuccessResponse{})
}

// CreateEndpoint creates a network endpoint
func (p *plugin) CreateEndpoint(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req struct {
		NetworkID  string `json:"NetworkID"`
		EndpointID string `json:"EndpointID"`
		Interface  struct {
			Address     netip.Prefix `json:"Address"`
			AddressIPv6 netip.Prefix `json:"AddressIPv6"`
			MacAddress  string       `json:"MacAddress"`
		} `json:"Interface"`
		Options struct {
			PortMap []portMap `json:"com.docker.network.portmap"`
		}
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, pm := range req.Options.PortMap {
		if pm.Proto != 6 && pm.Proto != 17 {
			http.Error(w, "unsupported protocol", http.StatusBadRequest)
			return
		}
	}
	dbpm := make(map[db.ProtoPort]*db.EndpointPort)
	for _, pm := range req.Options.PortMap {
		dbpm[db.ProtoPort{Proto: pm.Proto, Port: pm.HostPort}] = &db.EndpointPort{EndpointID: req.EndpointID, Port: pm.Port}
	}
	pfx := req.Interface.Address
	var netns string
	var desired []portForwardRule
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		ep, ok := n.Endpoints[req.EndpointID]
		if !ok {
			ep = &db.DockerEndpoint{
				EndpointID: req.EndpointID,
				IPv4:       pfx,
			}
		} else {
			ep.IPv4 = pfx
		}
		removeEndpointPortMappings(n, req.EndpointID)
		for k, pm := range dbpm {
			mak.Set(&n.PortMap, k.String(), pm)
		}
		for k, existing := range n.Endpoints {
			if existing.IPv4 == pfx && k != req.EndpointID {
				removeEndpointPortMappings(n, k)
				delete(n.Endpoints, k)
			}
		}
		mak.Set(&n.Endpoints, req.EndpointID, ep)
		desired = desiredPortForwards(n)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.syncPortForwards(netns, desired); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(SuccessResponse{})
}

// DeleteEndpoint deletes a network endpoint
func (p *plugin) DeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req struct {
		NetworkID  string `json:"NetworkID"`
		EndpointID string `json:"EndpointID"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var netns string
	var desired []portForwardRule
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		removeEndpointPortMappings(n, req.EndpointID)
		delete(n.Endpoints, req.EndpointID)
		desired = desiredPortForwards(n)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := p.syncPortForwards(netns, desired); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(SuccessResponse{})
}

// PluginActivate activates the plugin by declaring its capabilities
func (p *plugin) PluginActivate(w http.ResponseWriter, r *http.Request) {
	requestLogger(r)
	resp := map[string][]string{
		"Implements": {"NetworkDriver"},
	}
	fmt.Println("Activating plugin")
	json.NewEncoder(w).Encode(resp)
}

func requestLogger(r *http.Request) []byte {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fmt.Println("Failed to read request body:", err)
		return nil
	}
	fmt.Printf("Received request: %s %s %s\n", r.Method, r.URL.Path, string(body))
	return body
}

func New(db *db.Store) http.Handler {
	p := &plugin{
		db: db,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/Plugin.Activate", p.PluginActivate)
	mux.HandleFunc("/NetworkDriver.CreateNetwork", p.CreateNetwork)
	mux.HandleFunc("/NetworkDriver.DeleteNetwork", p.DeleteNetwork)
	mux.HandleFunc("/NetworkDriver.CreateEndpoint", p.CreateEndpoint)
	mux.HandleFunc("/NetworkDriver.DeleteEndpoint", p.DeleteEndpoint)
	mux.HandleFunc("/NetworkDriver.Join", p.JoinNetwork)
	mux.HandleFunc("/NetworkDriver.Leave", p.LeaveNetwork)
	mux.HandleFunc("/NetworkDriver.EndpointOperInfo", func(w http.ResponseWriter, r *http.Request) {
		requestLogger(r)
		json.NewEncoder(w).Encode(SuccessResponse{})
	})
	mux.HandleFunc("/NetworkDriver.ProgramExternalConnectivity", func(w http.ResponseWriter, r *http.Request) {
		requestLogger(r)
		json.NewEncoder(w).Encode(SuccessResponse{})
	})
	mux.HandleFunc("/NetworkDriver.GetCapabilities", func(w http.ResponseWriter, r *http.Request) {
		requestLogger(r)
		resp := map[string]string{
			"Scope":             "local",
			"ConnectivityScope": "local",
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		requestLogger(r)
		http.Error(w, "Not implemented", http.StatusNotImplemented)
	})
	return mux
}
