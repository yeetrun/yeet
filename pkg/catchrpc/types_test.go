// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catchrpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServiceInfoSnapshotsOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(ServiceInfo{Name: "svc"})
	if err != nil {
		t.Fatalf("Marshal ServiceInfo: %v", err)
	}
	if strings.Contains(string(raw), "snapshots") {
		t.Fatalf("ServiceInfo JSON = %s, want no snapshots field", raw)
	}
}

func TestServiceInfoSnapshotsIncludePopulated(t *testing.T) {
	raw, err := json.Marshal(ServiceInfo{
		Name: "svc",
		Snapshots: &ServiceSnapshots{
			Effective: EffectiveSnapshotPolicy{
				Enabled:  true,
				KeepLast: 3,
				MaxAge:   "72h",
				Required: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal ServiceInfo: %v", err)
	}
	if !strings.Contains(string(raw), `"snapshots"`) {
		t.Fatalf("ServiceInfo JSON = %s, want snapshots field", raw)
	}
}

func TestServiceNetworkPortsPresenceRoundTrip(t *testing.T) {
	raw, err := json.Marshal(ServiceNetwork{PortsPresent: true, IPWarning: "configured IP not present in guest"})
	if err != nil {
		t.Fatalf("Marshal ServiceNetwork: %v", err)
	}
	if !strings.Contains(string(raw), `"ports":[]`) {
		t.Fatalf("ServiceNetwork JSON = %s, want empty ports field", raw)
	}
	if !strings.Contains(string(raw), `"ipWarning":"configured IP not present in guest"`) {
		t.Fatalf("ServiceNetwork JSON = %s, want ip warning field", raw)
	}

	var withPorts ServiceNetwork
	if err := json.Unmarshal([]byte(`{"ipWarning":"configured IP not present in guest","ports":[{"hostPort":80,"containerPort":80,"protocol":"tcp"}]}`), &withPorts); err != nil {
		t.Fatalf("Unmarshal with ports: %v", err)
	}
	if !withPorts.PortsPresent || len(withPorts.Ports) != 1 || withPorts.Ports[0].Protocol != "tcp" || withPorts.IPWarning == "" {
		t.Fatalf("withPorts = %#v, want present tcp port", withPorts)
	}

	var omitted ServiceNetwork
	if err := json.Unmarshal([]byte(`{"svcIp":"192.168.100.2"}`), &omitted); err != nil {
		t.Fatalf("Unmarshal omitted ports: %v", err)
	}
	if omitted.PortsPresent {
		t.Fatalf("omitted = %#v, want PortsPresent false", omitted)
	}
}

func TestServiceInfoVMJSONRoundTrip(t *testing.T) {
	info := ServiceInfo{
		Name: "devbox",
		VM: &ServiceVM{
			Runtime:      "firecracker",
			Image:        "vm://ubuntu/26.04",
			ImageVersion: "ubuntu-26.04-amd64-v1",
			CPUs:         4,
			MemoryBytes:  4 << 30,
			DiskBytes:    128 << 30,
			DiskBackend:  "zvol",
			DiskPath:     "flash/yeet/vms/devbox/root",
			SSH:          &ServiceVMSSH{User: "ubuntu", Host: "devbox.local"},
			Console:      &ServiceVMConsole{Available: true, SocketPath: "/run/yeet/devbox/serial.sock"},
			Networks:     []ServiceVMNetwork{{Mode: "svc", Interface: "tap0", IP: "10.0.0.10", MAC: "02:00:00:00:00:10"}},
			SetupState:   "ready",
		},
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal ServiceInfo: %v", err)
	}
	for _, want := range []string{
		`"vm"`,
		`"imageVersion":"ubuntu-26.04-amd64-v1"`,
		`"memoryBytes":4294967296`,
		`"diskBackend":"zvol"`,
		`"socketPath":"/run/yeet/devbox/serial.sock"`,
		`"setupState":"ready"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("ServiceInfo JSON = %s, want %s", raw, want)
		}
	}
	var roundTrip ServiceInfo
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("Unmarshal ServiceInfo: %v", err)
	}
	if roundTrip.VM == nil || roundTrip.VM.Image != "vm://ubuntu/26.04" || roundTrip.VM.SSH.User != "ubuntu" || !roundTrip.VM.Console.Available {
		t.Fatalf("round trip VM = %#v", roundTrip.VM)
	}
}
