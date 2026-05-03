// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dnet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

	syncPortForwardsFunc func(netns string, desired []portForwardRule) error
	runInNetNSFunc       func(netns string, f func() error) error
	runCommandFunc       func(name string, args ...string) error
	natBackendFunc       func() natRuleBackend
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
	errChan := make(chan error, 1)
	go func() {
		errChan <- p.runInNetNSLocked(nsName, f)
	}()
	if err := <-errChan; err != nil {
		return fmt.Errorf("failed to run in netns: %w", err)
	}
	return nil
}

func (p *plugin) runInNetNSLocked(nsName string, f func() error) error {
	sem, _ := p.netnsSema.LoadOrInit(nsName, func() *syncs.Semaphore { return ptr.To(syncs.NewSemaphore(1)) })
	sem.Acquire()
	defer sem.Release()

	runtime.LockOSThread()
	err := runInNetNSOnLockedThread(nsName, f)
	if err == nil {
		runtime.UnlockOSThread()
	}
	return err
}

func runInNetNSOnLockedThread(nsName string, f func() error) error {
	netnsFile, err := os.Open(nsName)
	if err != nil {
		return fmt.Errorf("failed to open netns: %v", err)
	}
	defer func() { _ = netnsFile.Close() }()

	currentNetns, err := netns.Get()
	if err != nil {
		return fmt.Errorf("failed to get current netns: %v", err)
	}
	defer func() { _ = currentNetns.Close() }()

	if err := setNetNS(netns.NsHandle(netnsFile.Fd())); err != nil {
		return err
	}
	if err := f(); err != nil {
		return fmt.Errorf("failed to execute command: %v", err)
	}
	return restoreNetNS(currentNetns)
}

func setNetNS(handle netns.NsHandle) error {
	if err := netns.Set(handle); err != nil {
		return fmt.Errorf("failed to set netns: %v", err)
	}
	return nil
}

func restoreNetNS(handle netns.NsHandle) error {
	if err := netns.Set(handle); err != nil {
		return fmt.Errorf("failed to restore netns: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("failed to write docker network plugin response: %v", err)
	}
}

func decodePluginRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	body := requestLogger(r)
	if err := json.Unmarshal(body, v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

type commandRunner func(name string, args ...string) error

func (p *plugin) commandRunner() commandRunner {
	if p.runCommandFunc != nil {
		return p.runCommandFunc
	}
	return runCmd
}

func (p *plugin) natBackend() natRuleBackend {
	if p.natBackendFunc != nil {
		return p.natBackendFunc()
	}
	return iptablesBackend{}
}

func (p *plugin) inNetNS(netns string, f func() error) error {
	if p.runInNetNSFunc != nil {
		return p.runInNetNSFunc(netns, f)
	}
	return p.runInNetNS(netns, f)
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

func ensurePostroutingChainWithRunner(run commandRunner) error {
	if err := ensureNatChain(run, postroutingChainName); err != nil {
		return err
	}
	rules := []natEnsureRule{
		{checkChain: "POSTROUTING", addMode: "-A", addChain: "POSTROUTING", args: []string{"-j", postroutingChainName}},
		{checkChain: postroutingChainName, addMode: "-I", addChain: postroutingChainName, args: []string{"-m", "addrtype", "!", "--src-type", "LOCAL", "-o", "br0", "-j", "RETURN"}},
		{checkChain: postroutingChainName, addMode: "-A", addChain: postroutingChainName, args: []string{"-j", "MASQUERADE"}},
	}
	for _, rule := range rules {
		if err := ensureNatRule(run, rule); err != nil {
			return err
		}
	}
	return nil
}

type natEnsureRule struct {
	checkChain string
	addMode    string
	addChain   string
	args       []string
}

func ensureNatChain(run commandRunner, chain string) error {
	if err := run("iptables", "-t", "nat", "-L", chain); err == nil {
		return nil
	}
	return run("iptables", "-t", "nat", "-N", chain)
}

func ensureNatRule(run commandRunner, rule natEnsureRule) error {
	checkArgs := append([]string{"-t", "nat", "-C", rule.checkChain}, rule.args...)
	if err := run("iptables", checkArgs...); err == nil {
		return nil
	}
	addArgs := append([]string{"-t", "nat", rule.addMode, rule.addChain}, rule.args...)
	return run("iptables", addArgs...)
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

type netnsExistsFunc func(path string) (bool, error)
type netnsPortForwardSyncFunc func(netns string, desired []portForwardRule) error

func netnsPathExists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func reconcilePortForwardsFromData(d *db.Data, exists netnsExistsFunc, sync netnsPortForwardSyncFunc) error {
	byNetNS := desiredPortForwardsByNetNS(d)
	netnsPaths := make([]string, 0, len(byNetNS))
	for netns := range byNetNS {
		netnsPaths = append(netnsPaths, netns)
	}
	sort.Strings(netnsPaths)

	var errs []error
	for _, netns := range netnsPaths {
		ok, err := exists(netns)
		if err != nil {
			errs = append(errs, fmt.Errorf("check netns %q: %w", netns, err))
			continue
		}
		if !ok {
			log.Printf("skipping docker port forward reconciliation for missing netns %q", netns)
			continue
		}
		if err := sync(netns, byNetNS[netns]); err != nil {
			errs = append(errs, fmt.Errorf("reconcile port forwards for %q: %w", netns, err))
		}
	}
	return errors.Join(errs...)
}

func ReconcilePortForwards(store *db.Store) error {
	if store == nil {
		return fmt.Errorf("nil db store")
	}
	dv, err := store.Get()
	if err != nil {
		return err
	}
	p := &plugin{db: store}
	return reconcilePortForwardsFromData(dv.AsStruct(), netnsPathExists, func(netns string, _ []portForwardRule) error {
		return p.syncCurrentPortForwards(netns)
	})
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
	if err := deleteLegacyDirectOutputRules(netns, backend); err != nil {
		return err
	}
	if err := replaceManagedPortForwardChains(netns, desired, backend); err != nil {
		return err
	}
	return nil
}

func deleteLegacyDirectOutputRules(netns string, backend natRuleBackend) error {
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
	return nil
}

func replaceManagedPortForwardChains(netns string, desired []portForwardRule, backend natRuleBackend) error {
	if err := backend.FlushChain(preroutingChainName); err != nil {
		return fmt.Errorf("flush prerouting chain for %q: %w", netns, err)
	}
	if err := backend.FlushChain(outputChainName); err != nil {
		return fmt.Errorf("flush output chain for %q: %w", netns, err)
	}
	if err := backend.AppendRule(preroutingChainName, "-i", "br0", "-j", "RETURN"); err != nil {
		return fmt.Errorf("append bridge guard for %q: %w", netns, err)
	}
	return appendDesiredPortForwardRules(netns, desired, backend)
}

func appendDesiredPortForwardRules(netns string, desired []portForwardRule, backend natRuleBackend) error {
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

func (p *plugin) currentPortForwardsForNetNS(netns string) ([]portForwardRule, error) {
	dv, err := p.db.Get()
	if err != nil {
		return nil, err
	}
	return desiredPortForwardsForNetNS(dv.AsStruct(), netns), nil
}

func (p *plugin) syncCurrentPortForwards(netns string) error {
	if p.syncPortForwardsFunc != nil {
		desired, err := p.currentPortForwardsForNetNS(netns)
		if err != nil {
			return err
		}
		return p.syncPortForwardsFunc(netns, desired)
	}
	return p.inNetNS(netns, func() error {
		desired, err := p.currentPortForwardsForNetNS(netns)
		if err != nil {
			return err
		}
		return syncNetNSPortForwards(netns, desired, p.natBackend())
	})
}

func (p *plugin) LeaveNetwork(w http.ResponseWriter, r *http.Request) {
	var req endpointNetworkRequest
	if !decodePluginRequest(w, r, &req) {
		return
	}

	leave, err := p.leaveNetworkState(req.NetworkID, req.EndpointID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := p.leaveNetwork(leave); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, SuccessResponse{})
}

type endpointNetworkRequest struct {
	NetworkID  string `json:"NetworkID"`
	EndpointID string `json:"EndpointID"`
}

type leaveNetworkState struct {
	netns  string
	ifName string
}

func (p *plugin) leaveNetworkState(networkID, endpointID string) (leaveNetworkState, error) {
	var netns string
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[networkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		_, ok = n.Endpoints[endpointID]
		if !ok {
			return fmt.Errorf("endpoint not found")
		}
		delete(n.Endpoints, endpointID)
		removeEndpointPortMappings(n, endpointID)
		return nil
	}); err != nil {
		return leaveNetworkState{}, err
	}
	return leaveNetworkState{netns: netns, ifName: "yv-" + endpointID[:4]}, nil
}

func (p *plugin) leaveNetwork(leave leaveNetworkState) error {
	return p.inNetNS(leave.netns, func() error {
		var errs []error
		desired, err := p.currentPortForwardsForNetNS(leave.netns)
		if err != nil {
			errs = append(errs, err)
		} else if err := syncNetNSPortForwards(leave.netns, desired, p.natBackend()); err != nil {
			errs = append(errs, err)
		}
		if err := p.commandRunner()("ip", "link", "del", leave.ifName); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	})
}

type portMap struct {
	Proto       int    `json:"Proto"`
	IP          string `json:"IP"`
	Port        uint16 `json:"Port"`
	HostIP      string `json:"HostIP"`
	HostPort    uint16 `json:"HostPort"`
	HostPortEnd uint16 `json:"HostPortEnd"`
}

func endpointPortMap(endpointID string, portMaps []portMap) (map[db.ProtoPort]*db.EndpointPort, error) {
	dbpm := make(map[db.ProtoPort]*db.EndpointPort)
	for _, pm := range portMaps {
		if pm.Proto != 6 && pm.Proto != 17 {
			return nil, fmt.Errorf("unsupported protocol")
		}
		if pm.HostPortEnd != 0 && pm.HostPortEnd != pm.HostPort {
			return nil, fmt.Errorf("unsupported port range")
		}
		dbpm[db.ProtoPort{Proto: pm.Proto, Port: pm.HostPort}] = &db.EndpointPort{
			EndpointID: endpointID,
			Port:       pm.Port,
		}
	}
	return dbpm, nil
}

func setEndpointPortMappings(n *db.DockerNetwork, endpointID string, mappings map[db.ProtoPort]*db.EndpointPort) {
	removeEndpointPortMappings(n, endpointID)
	for k, pm := range mappings {
		mak.Set(&n.PortMap, k.String(), pm)
	}
}

func ensureBridgeWithRunner(addr netip.Prefix, run commandRunner) error {
	if err := run("ip", "link", "show", "br0"); err == nil {
		return nil
	}
	for _, cmd := range bridgeCreateCommands(addr) {
		if err := run(cmd.name, cmd.args...); err != nil {
			return err
		}
	}
	return nil
}

type commandSpec struct {
	name string
	args []string
}

func bridgeCreateCommands(addr netip.Prefix) []commandSpec {
	return []commandSpec{
		{name: "ip", args: []string{"link", "add", "br0", "type", "bridge"}},
		{name: "ip", args: []string{"link", "set", "br0", "up"}},
		{name: "ip", args: []string{"addr", "add", addr.String(), "dev", "br0"}},
		{name: "sysctl", args: []string{"-w", "net.ipv4.conf.br0.route_localnet=1"}},
	}
}

func (p *plugin) JoinNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NetworkID  string `json:"NetworkID"`
		EndpointID string `json:"EndpointID"`
	}
	if !decodePluginRequest(w, r, &req) {
		return
	}

	join, status, err := p.joinNetworkState(req.NetworkID, req.EndpointID)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if err := p.joinNetwork(join); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]any{
		"InterfaceName": map[string]string{
			"SrcName":   join.peerName,
			"DstPrefix": "eth",
		},
		"Gateway": join.gateway.String(),
	}
	writeJSON(w, resp)
}

type joinNetworkState struct {
	netns         string
	ifName        string
	peerName      string
	gateway       netip.Addr
	gatewayPrefix netip.Prefix
}

func (p *plugin) joinNetworkState(networkID, endpointID string) (joinNetworkState, int, error) {
	dv, err := p.db.Get()
	if err != nil {
		return joinNetworkState{}, http.StatusInternalServerError, err
	}
	n, ok := dv.AsStruct().DockerNetworks[networkID]
	if !ok {
		return joinNetworkState{}, http.StatusBadRequest, fmt.Errorf("network not found")
	}
	if _, ok := n.Endpoints[endpointID]; !ok {
		return joinNetworkState{}, http.StatusBadRequest, fmt.Errorf("endpoint not found")
	}
	ifName := "yv-" + endpointID[:4]
	return joinNetworkState{
		netns:         n.NetNS,
		ifName:        ifName,
		peerName:      ifName + "p",
		gateway:       n.IPv4Gateway.Addr(),
		gatewayPrefix: n.IPv4Gateway,
	}, http.StatusOK, nil
}

func (p *plugin) joinNetwork(join joinNetworkState) error {
	run := p.commandRunner()
	if err := addJoinVeth(run, join); err != nil {
		return err
	}
	return p.inNetNS(join.netns, func() error {
		return p.configureJoinedNetwork(join)
	})
}

func addJoinVeth(run commandRunner, join joinNetworkState) error {
	if err := run("ip", "link", "add", join.ifName, "type", "veth", "peer", "name", join.peerName); err != nil {
		return err
	}
	return run("ip", "link", "set", join.ifName, "netns", join.netns)
}

func (p *plugin) configureJoinedNetwork(join joinNetworkState) error {
	run := p.commandRunner()
	if err := ensureBridgeWithRunner(join.gatewayPrefix, run); err != nil {
		return err
	}
	if err := run("ip", "link", "set", join.ifName, "master", "br0"); err != nil {
		return err
	}
	if err := run("ip", "link", "set", join.ifName, "up"); err != nil {
		return err
	}
	if err := ensurePostroutingChainWithRunner(run); err != nil {
		return err
	}
	desired, err := p.currentPortForwardsForNetNS(join.netns)
	if err != nil {
		return err
	}
	return syncNetNSPortForwards(join.netns, desired, p.natBackend())
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
	writeJSON(w, SuccessResponse{})
}

type createNetworkRequest struct {
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

func (req createNetworkRequest) validate() error {
	if req.Options.Generic.NetNS == "" {
		return fmt.Errorf("NetNS is required")
	}
	if len(req.IPv4Data) == 0 {
		return fmt.Errorf("IPv4Data is required")
	}
	return nil
}

func (req createNetworkRequest) dockerNetwork() *db.DockerNetwork {
	return &db.DockerNetwork{
		NetNS:       req.Options.Generic.NetNS,
		NetworkID:   req.NetworkID,
		IPv4Gateway: req.IPv4Data[0].Gateway,
		IPv4Range:   req.IPv4Data[0].Pool,
	}
}

// CreateNetwork creates a network
func (p *plugin) CreateNetwork(w http.ResponseWriter, r *http.Request) {
	var req createNetworkRequest
	if !decodePluginRequest(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := p.db.MutateData(func(d *db.Data) error {
		if _, ok := d.DockerNetworks[req.NetworkID]; ok {
			return fmt.Errorf("network already exists")
		}
		mak.Set(&d.DockerNetworks, req.NetworkID, req.dockerNetwork())
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, SuccessResponse{})
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
	dbpm, err := endpointPortMap(req.EndpointID, req.Options.PortMap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pfx := req.Interface.Address
	var netns string
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
		setEndpointPortMappings(n, req.EndpointID, dbpm)
		for k, existing := range n.Endpoints {
			if existing.IPv4 == pfx && k != req.EndpointID {
				removeEndpointPortMappings(n, k)
				delete(n.Endpoints, k)
			}
		}
		mak.Set(&n.Endpoints, req.EndpointID, ep)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.syncCurrentPortForwards(netns); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, SuccessResponse{})
}

type endpointConnectivityRequest struct {
	NetworkID  string `json:"NetworkID"`
	EndpointID string `json:"EndpointID"`
	Options    struct {
		PortMap []portMap `json:"com.docker.network.portmap"`
	} `json:"Options"`
}

func (p *plugin) ProgramExternalConnectivity(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req endpointConnectivityRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dbpm, err := endpointPortMap(req.EndpointID, req.Options.PortMap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var netns string
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		if _, ok := n.Endpoints[req.EndpointID]; !ok {
			return fmt.Errorf("endpoint not found")
		}
		netns = n.NetNS
		setEndpointPortMappings(n, req.EndpointID, dbpm)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.syncCurrentPortForwards(netns); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, SuccessResponse{})
}

func (p *plugin) RevokeExternalConnectivity(w http.ResponseWriter, r *http.Request) {
	body := requestLogger(r)
	var req endpointConnectivityRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var netns string
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		removeEndpointPortMappings(n, req.EndpointID)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.syncCurrentPortForwards(netns); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, SuccessResponse{})
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
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		removeEndpointPortMappings(n, req.EndpointID)
		delete(n.Endpoints, req.EndpointID)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := p.syncCurrentPortForwards(netns); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, SuccessResponse{})
}

// PluginActivate activates the plugin by declaring its capabilities
func (p *plugin) PluginActivate(w http.ResponseWriter, r *http.Request) {
	requestLogger(r)
	resp := map[string][]string{
		"Implements": {"NetworkDriver"},
	}
	fmt.Println("Activating plugin")
	writeJSON(w, resp)
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
		writeJSON(w, SuccessResponse{})
	})
	mux.HandleFunc("/NetworkDriver.ProgramExternalConnectivity", p.ProgramExternalConnectivity)
	mux.HandleFunc("/NetworkDriver.RevokeExternalConnectivity", p.RevokeExternalConnectivity)
	mux.HandleFunc("/NetworkDriver.GetCapabilities", func(w http.ResponseWriter, r *http.Request) {
		requestLogger(r)
		resp := map[string]string{
			"Scope":             "local",
			"ConnectivityScope": "local",
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		requestLogger(r)
		http.Error(w, "Not implemented", http.StatusNotImplemented)
	})
	return mux
}
