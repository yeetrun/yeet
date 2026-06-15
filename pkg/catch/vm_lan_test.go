// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func stubVMNetworkState(t *testing.T, state vmAgentNetworkState, err error) {
	t.Helper()
	old := queryVMNetworkStateFn
	queryVMNetworkStateFn = func(context.Context, string) (vmAgentNetworkState, error) {
		return state, err
	}
	t.Cleanup(func() { queryVMNetworkStateFn = old })
}

func TestServiceInfoIgnoresLegacyLANDiscoveryWithoutAgent(t *testing.T) {
	server := newTestServer(t)
	seedLANVMService(t, server)

	resp, err := server.serviceInfo("devbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Info.VM == nil || resp.Info.VM.SSH == nil {
		t.Fatalf("VM SSH = %#v, want VM SSH info", resp.Info.VM)
	}
	if resp.Info.VM.SSH.Host != "" {
		t.Fatalf("VM SSH host = %q, want empty without agent", resp.Info.VM.SSH.Host)
	}
	if len(resp.Info.Network.IPs) != 0 {
		t.Fatalf("network IPs = %#v, want none without agent", resp.Info.Network.IPs)
	}
	if !strings.Contains(resp.Info.Network.IPError, "agent vsock socket path") {
		t.Fatalf("network IP error = %q, want missing agent socket", resp.Info.Network.IPError)
	}
}

func TestServiceInfoReportsAgentErrorForDynamicVMIP(t *testing.T) {
	stubVMNetworkState(t, vmAgentNetworkState{}, errors.New("connect refused"))

	server := newTestServer(t)
	seedLANVMService(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.VM.Sockets.VsockSocketPath = "/run/devbox/vsock.sock"
		svc.VM.Sockets.VsockGuestCID = vmAgentGuestCID
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}

	resp, err := server.serviceInfo("devbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if !strings.Contains(resp.Info.Network.IPError, "connect refused") {
		t.Fatalf("network IP error = %q, want agent error", resp.Info.Network.IPError)
	}
	if len(resp.Info.Network.IPs) != 0 {
		t.Fatalf("network IPs = %#v, want none on agent error", resp.Info.Network.IPs)
	}
}

func TestServiceInfoIncludesLiveAgentLANIP(t *testing.T) {
	stubVMNetworkState(t, vmAgentNetworkState{Interfaces: []vmAgentInterface{{
		Name: "eth0",
		MAC:  "46:85:dc:3a:06:34",
		Up:   true,
		IPs:  []string{"10.0.4.183"},
	}}}, nil)

	server := newTestServer(t)
	seedLANVMService(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.VM.Sockets.VsockSocketPath = "/run/devbox/vsock.sock"
		svc.VM.Sockets.VsockGuestCID = vmAgentGuestCID
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}

	resp, err := server.serviceInfo("devbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Info.VM == nil || resp.Info.VM.SSH == nil || resp.Info.VM.SSH.Host != "10.0.4.183" {
		t.Fatalf("VM SSH = %#v, want live agent host 10.0.4.183", resp.Info.VM)
	}
	if len(resp.Info.Network.IPs) != 1 || resp.Info.Network.IPs[0].IP != "10.0.4.183" || resp.Info.Network.IPs[0].Source != "agent" {
		t.Fatalf("network IPs = %#v, want agent LAN IP", resp.Info.Network.IPs)
	}
}

func TestServiceInfoKeepsStaticIPWhenAgentUnavailable(t *testing.T) {
	stubVMNetworkState(t, vmAgentNetworkState{}, errors.New("connection refused"))

	server := newTestServer(t)
	seedStaticVMService(t, server)
	configureVMAgentSocket(t, server, "nixbox")

	resp, err := server.serviceInfo("nixbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if len(resp.Info.Network.IPs) != 1 || resp.Info.Network.IPs[0].IP != "192.168.100.16" || resp.Info.Network.IPs[0].Source != "config" {
		t.Fatalf("network IPs = %#v, want configured IP", resp.Info.Network.IPs)
	}
	if resp.Info.Network.IPError != "" {
		t.Fatalf("network IP error = %q, want none for static IP", resp.Info.Network.IPError)
	}
	if !strings.Contains(resp.Info.Network.IPWarning, "connection refused") {
		t.Fatalf("network IP warning = %q, want agent failure warning", resp.Info.Network.IPWarning)
	}
}

func TestServiceInfoReportsStaticIPMismatchFromAgent(t *testing.T) {
	stubVMNetworkState(t, vmAgentNetworkState{Interfaces: []vmAgentInterface{{
		Name: "eth0",
		MAC:  "02:fc:30:3f:bf:b3",
		Up:   true,
		IPs:  []string{"192.168.100.99"},
	}}}, nil)

	server := newTestServer(t)
	seedStaticVMService(t, server)
	configureVMAgentSocket(t, server, "nixbox")

	resp, err := server.serviceInfo("nixbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if len(resp.Info.Network.IPs) != 1 || resp.Info.Network.IPs[0].IP != "192.168.100.16" || resp.Info.Network.IPs[0].Source != "config" {
		t.Fatalf("network IPs = %#v, want configured IP", resp.Info.Network.IPs)
	}
	if resp.Info.Network.IPError != "" {
		t.Fatalf("network IP error = %q, want none for static IP", resp.Info.Network.IPError)
	}
	if !strings.Contains(resp.Info.Network.IPWarning, "192.168.100.16") || !strings.Contains(resp.Info.Network.IPWarning, "192.168.100.99") {
		t.Fatalf("network IP warning = %q, want configured/reported mismatch", resp.Info.Network.IPWarning)
	}
}

func TestServiceInfoAcceptsMatchingStaticIPFromAgent(t *testing.T) {
	stubVMNetworkState(t, vmAgentNetworkState{Interfaces: []vmAgentInterface{{
		Name: "eth0",
		MAC:  "02:fc:30:3f:bf:b3",
		Up:   true,
		IPs:  []string{"192.168.100.16"},
	}}}, nil)

	server := newTestServer(t)
	seedStaticVMService(t, server)
	configureVMAgentSocket(t, server, "nixbox")

	resp, err := server.serviceInfo("nixbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if len(resp.Info.Network.IPs) != 1 || resp.Info.Network.IPs[0].IP != "192.168.100.16" || resp.Info.Network.IPs[0].Source != "config" {
		t.Fatalf("network IPs = %#v, want configured IP", resp.Info.Network.IPs)
	}
	if resp.Info.Network.IPError != "" || resp.Info.Network.IPWarning != "" {
		t.Fatalf("network IP error/warning = %q/%q, want none", resp.Info.Network.IPError, resp.Info.Network.IPWarning)
	}
}

func TestIPCmdFuncPrintsVMNetworkIPs(t *testing.T) {
	stubVMNetworkState(t, vmAgentNetworkState{Interfaces: []vmAgentInterface{{
		Name: "eth0",
		MAC:  "46:85:dc:3a:06:34",
		Up:   true,
		IPs:  []string{"10.0.4.50"},
	}}}, nil)

	server := newTestServer(t)
	seedLANVMService(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.VM.Sockets.VsockSocketPath = "/run/devbox/vsock.sock"
		svc.VM.Sockets.VsockGuestCID = vmAgentGuestCID
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}
	out := &bytes.Buffer{}
	execer := &ttyExecer{s: server, sn: "devbox", rw: out}

	if err := execer.ipCmdFunc(); err != nil {
		t.Fatalf("ipCmdFunc: %v", err)
	}
	if out.String() != "10.0.4.50\n" {
		t.Fatalf("ip output = %q, want LAN IP", out.String())
	}
}

func TestIPCmdFuncErrorsWhenVMHasNoTrustedIP(t *testing.T) {
	stubVMNetworkState(t, vmAgentNetworkState{}, errors.New("agent unavailable"))

	server := newTestServer(t)
	seedLANVMService(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.VM.Sockets.VsockSocketPath = "/run/devbox/vsock.sock"
		svc.VM.Sockets.VsockGuestCID = vmAgentGuestCID
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}
	out := &bytes.Buffer{}
	execer := &ttyExecer{s: server, sn: "devbox", rw: out}

	err := execer.ipCmdFunc()
	if err == nil || !strings.Contains(err.Error(), "no current IP known") || !strings.Contains(err.Error(), "agent unavailable") {
		t.Fatalf("ipCmdFunc error = %v, want no current IP known with agent error", err)
	}
	if out.String() != "" {
		t.Fatalf("ip output = %q, want empty on error", out.String())
	}
}

func configureVMAgentSocket(t *testing.T, server *Server, service string) {
	t.Helper()
	if _, _, err := server.cfg.DB.MutateService(service, func(_ *db.Data, svc *db.Service) error {
		svc.VM.Sockets.VsockSocketPath = "/run/" + service + "/vsock.sock"
		svc.VM.Sockets.VsockGuestCID = vmAgentGuestCID
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
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

func seedStaticVMService(t *testing.T, server *Server) {
	t.Helper()
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"nixbox": {
			Name:        "nixbox",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Runtime: "firecracker",
				Networks: []db.VMNetworkConfig{{
					Mode:      "svc",
					Interface: "eth0",
					IP:        netip.MustParseAddr("192.168.100.16"),
					MAC:       "02:fc:30:3f:bf:b3",
				}},
				SSH:        db.VMSSHConfig{User: "nixos"},
				Console:    db.VMConsoleConfig{SocketPath: "/run/nixbox/serial.sock"},
				SetupState: "ready",
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
}

func TestVMNetworkIPFromConfigPrefersStaticIP(t *testing.T) {
	network := db.VMNetworkConfig{Mode: "svc", Interface: "eth0", IP: netip.MustParseAddr("192.168.100.12")}
	got := serviceVMNetworkIP(network, map[string]vmDiscoveredIP{"eth0": {IP: "10.0.4.50", Source: "agent"}})
	if got.IP != "192.168.100.12" || got.Source != "config" {
		t.Fatalf("network IP = %q, want static service IP", got)
	}
}
