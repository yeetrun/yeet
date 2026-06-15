// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startFakeVsockAgent(t *testing.T, response string) string {
	t.Helper()
	socketPath, requests := startFakeVsockAgentWithRequests(t, response)
	t.Cleanup(func() {
		got := <-requests
		if got.Protocol != vmAgentProtocolVersion || got.Type != "network_state" || got.RequestID != vmAgentRequestID {
			t.Fatalf("agent request = %#v, want network_state protocol request", got)
		}
	})
	return socketPath
}

func startFakeVsockAgentWithRequests(t *testing.T, response string) (string, <-chan vmAgentRequest) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "yeet-vsock-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	socketPath := filepath.Join(dir, "vsock.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	})
	requests := make(chan vmAgentRequest, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			requests <- vmAgentRequest{}
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		line, err := r.ReadString('\n')
		if err != nil {
			requests <- vmAgentRequest{}
			return
		}
		if strings.TrimSpace(line) != "CONNECT 7788" {
			requests <- vmAgentRequest{}
			return
		}
		_, _ = conn.Write([]byte("OK 1024\n"))
		var req vmAgentRequest
		if err := json.NewDecoder(r).Decode(&req); err != nil {
			requests <- vmAgentRequest{}
			return
		}
		requests <- req
		_, _ = conn.Write([]byte(response + "\n"))
	}()
	return socketPath, requests
}

func startFakeVsockAgentWithoutAck(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "yeet-vsock-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	socketPath := filepath.Join(dir, "vsock.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadString('\n')
		time.Sleep(vmAgentQueryTimeout)
	}()
	return socketPath
}

func TestQueryVMNetworkStateUsesVsockConnectAndJSONProtocol(t *testing.T) {
	socketPath := startFakeVsockAgent(t, `{"protocol":1,"type":"network_state","request_id":"test","interfaces":[{"name":"eth0","mac":"02:fc:00:00:00:12","up":true,"ips":["10.0.4.183"]}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := queryVMNetworkState(ctx, socketPath)
	if err != nil {
		t.Fatalf("queryVMNetworkState: %v", err)
	}
	if len(got.Interfaces) != 1 {
		t.Fatalf("interfaces = %#v, want one", got.Interfaces)
	}
	if got.Interfaces[0].Name != "eth0" || got.Interfaces[0].IPs[0] != "10.0.4.183" {
		t.Fatalf("network state = %#v, want eth0 10.0.4.183", got)
	}
	if ips := vmAgentNetworkStateIPs(got); ips["eth0"] != "10.0.4.183" {
		t.Fatalf("network state IPs = %#v, want eth0 10.0.4.183", ips)
	}
}

func TestQueryVMNetworkStateRejectsProtocolMismatch(t *testing.T) {
	socketPath := startFakeVsockAgent(t, `{"protocol":2,"type":"network_state","request_id":"test","interfaces":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := queryVMNetworkState(ctx, socketPath)
	if err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("queryVMNetworkState error = %v, want protocol version error", err)
	}
}

func TestQueryVMNetworkStateRejectsNoUsableAddresses(t *testing.T) {
	socketPath := startFakeVsockAgent(t, `{"protocol":1,"type":"network_state","request_id":"test","interfaces":[{"name":"lo","up":true,"ips":["127.0.0.1"]}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := queryVMNetworkState(ctx, socketPath)
	if err == nil || !strings.Contains(err.Error(), "no usable addresses") {
		t.Fatalf("queryVMNetworkState error = %v, want no usable addresses", err)
	}
}

func TestQueryVMNetworkStateHonorsContextCancellationAfterConnect(t *testing.T) {
	socketPath := startFakeVsockAgentWithoutAck(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := queryVMNetworkState(ctx, socketPath)
	if err == nil {
		t.Fatal("queryVMNetworkState error = nil, want cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("queryVMNetworkState elapsed = %s, want prompt cancellation", elapsed)
	}
}

func TestUsableVMAgentInterfacesRejectsUnroutableAddresses(t *testing.T) {
	got := usableVMAgentInterfaces([]vmAgentInterface{{
		Name: "eth0",
		Up:   true,
		IPs:  []string{"0.0.0.0", "224.0.0.1", "255.255.255.255", "10.0.4.183"},
	}})
	if len(got) != 1 || len(got[0].IPs) != 1 || got[0].IPs[0] != "10.0.4.183" {
		t.Fatalf("usable interfaces = %#v, want only routable IPv4", got)
	}
}
