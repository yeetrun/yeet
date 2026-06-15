// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

const (
	vmAgentProtocolVersion = 1
	vmAgentPort            = 7788
	vmAgentGuestCID        = 3
	vmAgentVsockID         = "yeet-agent"
	vmAgentRequestID       = "test"
	vmAgentQueryTimeout    = 1500 * time.Millisecond
)

type vmAgentRequest struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

type vmAgentError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type vmAgentResponse struct {
	Protocol   int                `json:"protocol"`
	Type       string             `json:"type"`
	RequestID  string             `json:"request_id"`
	Interfaces []vmAgentInterface `json:"interfaces,omitempty"`
	Error      *vmAgentError      `json:"error,omitempty"`
}

type vmAgentInterface struct {
	Name string   `json:"name"`
	MAC  string   `json:"mac,omitempty"`
	Up   bool     `json:"up"`
	IPs  []string `json:"ips,omitempty"`
}

type vmAgentNetworkState struct {
	Interfaces []vmAgentInterface
}

func queryVMNetworkState(ctx context.Context, socketPath string) (vmAgentNetworkState, error) {
	conn, r, cleanup, err := connectVMAgent(ctx, socketPath)
	if err != nil {
		return vmAgentNetworkState{}, err
	}
	defer cleanup()

	req := vmAgentRequest{
		Protocol:  vmAgentProtocolVersion,
		Type:      "network_state",
		RequestID: vmAgentRequestID,
	}
	if err := sendVMAgentRequest(conn, r, req); err != nil {
		return vmAgentNetworkState{}, err
	}
	var resp vmAgentResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return vmAgentNetworkState{}, fmt.Errorf("read VM agent response: %w", err)
	}
	if err := validateVMAgentNetworkStateResponse(resp, req); err != nil {
		return vmAgentNetworkState{}, err
	}
	return vmAgentNetworkState{Interfaces: usableVMAgentInterfaces(resp.Interfaces)}, nil
}

func connectVMAgent(ctx context.Context, socketPath string) (net.Conn, *bufio.Reader, func(), error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil, nil, nil, fmt.Errorf("VM agent vsock socket path is empty")
	}
	queryCtx, cancel := context.WithTimeout(ctx, vmAgentQueryTimeout)
	var d net.Dialer
	conn, err := d.DialContext(queryCtx, "unix", socketPath)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("connect VM agent vsock: %w", err)
	}
	if deadline, ok := queryCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(vmAgentQueryTimeout))
	}
	cancelConn := make(chan struct{})
	go func() {
		select {
		case <-queryCtx.Done():
			_ = conn.Close()
		case <-cancelConn:
		}
	}()
	cleanup := func() {
		close(cancelConn)
		cancel()
		_ = conn.Close()
	}
	return conn, bufio.NewReader(conn), cleanup, nil
}

func sendVMAgentRequest(conn net.Conn, r *bufio.Reader, req vmAgentRequest) error {
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", vmAgentPort); err != nil {
		return fmt.Errorf("send VM agent connect: %w", err)
	}
	ack, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read VM agent connect ack: %w", err)
	}
	if !strings.HasPrefix(ack, "OK ") {
		return fmt.Errorf("VM agent connect rejected: %s", strings.TrimSpace(ack))
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send VM agent request: %w", err)
	}
	return nil
}

func validateVMAgentNetworkStateResponse(resp vmAgentResponse, req vmAgentRequest) error {
	if resp.Protocol != vmAgentProtocolVersion {
		return fmt.Errorf("VM agent protocol version = %d, want %d", resp.Protocol, vmAgentProtocolVersion)
	}
	if resp.Type != req.Type {
		return fmt.Errorf("VM agent response type = %q, want %q", resp.Type, req.Type)
	}
	if resp.RequestID != req.RequestID {
		return fmt.Errorf("VM agent response request_id = %q, want %q", resp.RequestID, req.RequestID)
	}
	if resp.Error != nil {
		return fmt.Errorf("VM agent error %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(usableVMAgentInterfaces(resp.Interfaces)) == 0 {
		return fmt.Errorf("VM agent returned no usable addresses")
	}
	return nil
}

func usableVMAgentInterfaces(in []vmAgentInterface) []vmAgentInterface {
	out := make([]vmAgentInterface, 0, len(in))
	for _, iface := range in {
		if strings.TrimSpace(iface.Name) == "" || strings.TrimSpace(iface.Name) == "lo" || !iface.Up {
			continue
		}
		usableIPs := usableVMAgentIPs(iface.IPs)
		if len(usableIPs) == 0 {
			continue
		}
		iface.IPs = usableIPs
		out = append(out, iface)
	}
	return out
}

func usableVMAgentIPs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		ip, ok := usableVMAgentIP(raw)
		if ok {
			out = append(out, ip)
		}
	}
	return out
}

func usableVMAgentIP(raw string) (string, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return "", false
	}
	return ip.String(), true
}

func vmAgentNetworkStateIPs(state vmAgentNetworkState) map[string]string {
	out := map[string]string{}
	for _, iface := range state.Interfaces {
		if len(iface.IPs) == 0 {
			continue
		}
		out[strings.TrimSpace(iface.Name)] = iface.IPs[0]
	}
	return out
}
