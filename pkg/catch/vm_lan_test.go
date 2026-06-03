// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestParseVMGuestIPReports(t *testing.T) {
	got := parseVMGuestIPReports([]byte(`
guest-ready
yeet-ip eth0 10.0.4.123
yeet-ready eth0 10.0.4.178
yeet-ip eth1 192.168.100.12
`))
	if got["eth0"] != "10.0.4.178" || got["eth1"] != "192.168.100.12" {
		t.Fatalf("reports = %#v, want eth0 ready IP and eth1 IP", got)
	}
}

func TestParseIPNeighForMAC(t *testing.T) {
	got := parseIPNeighForMAC([]byte(`
10.0.4.145 dev vmbr0 lladdr bc:24:11:1c:a5:9e REACHABLE
10.0.4.50 dev vmbr0 lladdr 46:85:dc:3a:06:34 STALE
`), "46:85:dc:3a:06:34")
	if got != "10.0.4.50" {
		t.Fatalf("IP = %q, want 10.0.4.50", got)
	}
}

func TestServiceInfoIncludesDiscoveredLANIPForVMSSH(t *testing.T) {
	oldDiscover := discoverVMLANIPsFn
	defer func() { discoverVMLANIPsFn = oldDiscover }()
	discoverVMLANIPsFn = func(string, db.VMConfigView) map[string]string {
		return map[string]string{"eth0": "10.0.4.50"}
	}

	server := newTestServer(t)
	seedLANVMService(t, server)

	resp, err := server.serviceInfo("devbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Info.VM == nil || resp.Info.VM.SSH == nil || resp.Info.VM.SSH.Host != "10.0.4.50" {
		t.Fatalf("VM SSH = %#v, want LAN host 10.0.4.50", resp.Info.VM)
	}
	if len(resp.Info.Network.IPs) != 1 || resp.Info.Network.IPs[0].IP != "10.0.4.50" || resp.Info.Network.IPs[0].Label != "lan" {
		t.Fatalf("network IPs = %#v, want LAN IP", resp.Info.Network.IPs)
	}
}

func TestIPCmdFuncPrintsVMNetworkIPs(t *testing.T) {
	oldDiscover := discoverVMLANIPsFn
	defer func() { discoverVMLANIPsFn = oldDiscover }()
	discoverVMLANIPsFn = func(string, db.VMConfigView) map[string]string {
		return map[string]string{"eth0": "10.0.4.50"}
	}

	server := newTestServer(t)
	seedLANVMService(t, server)
	out := &bytes.Buffer{}
	execer := &ttyExecer{s: server, sn: "devbox", rw: out}

	if err := execer.ipCmdFunc(); err != nil {
		t.Fatalf("ipCmdFunc: %v", err)
	}
	if out.String() != "10.0.4.50\n" {
		t.Fatalf("ip output = %q, want LAN IP", out.String())
	}
}

func seedLANVMService(t *testing.T, server *Server) {
	t.Helper()
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"devbox": {
			Name:        "devbox",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Runtime: "firecracker",
				Networks: []db.VMNetworkConfig{{
					Mode:      "lan",
					Interface: "eth0",
					Parent:    "vmbr0",
					MAC:       "46:85:dc:3a:06:34",
				}},
				SSH:        db.VMSSHConfig{User: "ubuntu"},
				Console:    db.VMConsoleConfig{SocketPath: "/run/devbox/serial.sock"},
				SetupState: "ready",
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
}

func TestVMNetworkIPFromConfigPrefersStaticIP(t *testing.T) {
	network := db.VMNetworkConfig{Mode: "svc", Interface: "eth0", IP: netip.MustParseAddr("192.168.100.12")}
	got := serviceVMNetworkIP(network, map[string]string{"eth0": "10.0.4.50"})
	if got != "192.168.100.12" {
		t.Fatalf("network IP = %q, want static service IP", got)
	}
}
