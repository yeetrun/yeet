// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dnet

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"runtime"

	"github.com/shayne/yeet/pkg/db"
	"github.com/vishvananda/netns"
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

const (
	postroutingChainName = "YEET_POSTROUTING"
	preroutingChainName  = "YEET_PREROUTING"
)

func ensurePreroutingChain() error {
	if err := runCmd("iptables", "-t", "nat", "-L", preroutingChainName); err == nil {
		return nil
	}
	if err := runCmd("iptables", "-t", "nat", "-N", preroutingChainName); err != nil {
		return err
	}
	if err := runCmd("iptables", "-t", "nat", "-A", "PREROUTING", "-j", preroutingChainName); err != nil {
		return err
	}
	if err := runCmd("iptables", "-t", "nat", "-A", preroutingChainName, "-i", "br0", "-j", "RETURN"); err != nil {
		return err
	}
	return nil
}

func ensurePostroutingChain() error {
	if err := runCmd("iptables", "-t", "nat", "-L", postroutingChainName); err == nil {
		return nil
	}
	if err := runCmd("iptables", "-t", "nat", "-N", postroutingChainName); err != nil {
		return err
	}
	if err := runCmd("iptables", "-t", "nat", "-A", "POSTROUTING", "-j", postroutingChainName); err != nil {
		return err
	}
	if err := runCmd("iptables", "-t", "nat", "-I", postroutingChainName, "-m", "addrtype", "!", "--src-type", "LOCAL", "-o", "br0", "-j", "RETURN"); err != nil {
		return err
	}
	if err := runCmd("iptables", "-t", "nat", "-A", postroutingChainName, "-j", "MASQUERADE"); err != nil {
		return err
	}
	return nil
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
	var ep *db.DockerEndpoint
	var toDelete map[string]*db.EndpointPort
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[nid]
		if !ok {
			return fmt.Errorf("network not found")
		}
		netns = n.NetNS
		ep, ok = n.Endpoints[eid]
		if !ok {
			return fmt.Errorf("endpoint not found")
		}
		delete(n.Endpoints, eid)
		for k, pm := range n.PortMap {
			if pm.EndpointID == eid {
				mak.Set(&toDelete, k, pm)
			}
		}
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := p.runInNetNS(netns, func() error {
		for hp, dst := range toDelete {
			if err := p.forwardPort(ep, hp, dst, false); err != nil {
				return err
			}
		}
		if err := runCmd("ip", "link", "del", ifName); err != nil {
			return err
		}
		return nil
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
	ep, ok := n.Endpoints[eid]
	if !ok {
		http.Error(w, "endpoint not found", http.StatusBadRequest)
		return
	}
	gateway := n.IPv4Gateway.Addr()
	gatewayPrefix := n.IPv4Gateway
	netns = n.NetNS

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
		for hp, dst := range n.PortMap {
			if err := p.forwardPort(ep, hp, dst, true); err != nil {
				return err
			}
		}
		return nil
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

func (p *plugin) forwardPort(ep *db.DockerEndpoint, hp string, dst *db.EndpointPort, add bool) error {
	if dst.EndpointID != ep.EndpointID {
		return nil
	}
	if err := ensurePreroutingChain(); err != nil {
		return err
	}
	var hpProto db.ProtoPort
	if err := hpProto.Parse(hp); err != nil {
		return err
	}
	var proto string
	switch hpProto.Proto {
	case 6:
		proto = "tcp"
	case 17:
		proto = "udp"
	default:
		return fmt.Errorf("unsupported protocol: %d", hpProto.Proto)
	}
	var cmd string
	if add {
		cmd = "-A"
	} else {
		cmd = "-D"
	}
	log.Printf("adding forward for %s:%d -> %s:%d", proto, hpProto.Port, ep.IPv4.Addr().String(), dst.Port)
	if err := runCmd("iptables",
		"-t", "nat",
		cmd, preroutingChainName,
		"-p", proto,
		"--dport", fmt.Sprint(hpProto.Port),
		"-j", "DNAT",
		"--to-destination", net.JoinHostPort(ep.IPv4.Addr().String(), fmt.Sprint(dst.Port)),
	); err != nil {
		return err
	}

	if err := runCmd("iptables",
		"-t", "nat",
		cmd, "OUTPUT",
		"-p", proto,
		"-o", "lo",
		"--dport", fmt.Sprint(hpProto.Port),
		"-j", "DNAT",
		"--to-destination", net.JoinHostPort(ep.IPv4.Addr().String(), fmt.Sprint(dst.Port)),
	); err != nil {
		return err
	}
	return nil
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
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		ep, ok := n.Endpoints[req.EndpointID]
		if !ok {
			ep = &db.DockerEndpoint{
				EndpointID: req.EndpointID,
				IPv4:       pfx,
			}
		}
		for k, pm := range dbpm {
			mak.Set(&n.PortMap, k.String(), pm)
		}
		for k, ep := range n.Endpoints {
			if ep.IPv4 == pfx && k != ep.EndpointID {
				delete(n.Endpoints, k)
				// TODO: do we have to update iptables?
			}
		}
		mak.Set(&n.Endpoints, req.EndpointID, ep)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	if _, err := p.db.MutateData(func(d *db.Data) error {
		n, ok := d.DockerNetworks[req.NetworkID]
		if !ok {
			return fmt.Errorf("network not found")
		}
		delete(n.Endpoints, req.EndpointID)
		return nil
	}); err != nil {
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
