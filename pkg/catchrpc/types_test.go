// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catchrpc

import (
	"bytes"
	"encoding/json"
	"reflect"
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
	raw, err := json.Marshal(ServiceNetwork{
		PortsPresent: true,
		IPWarning:    "configured IP not present in guest",
		RuntimeIPs: []ServiceIP{{
			Label:     "docker",
			IP:        "192.168.48.1",
			Interface: "br0",
		}},
	})
	if err != nil {
		t.Fatalf("Marshal ServiceNetwork: %v", err)
	}
	if !strings.Contains(string(raw), `"ports":[]`) {
		t.Fatalf("ServiceNetwork JSON = %s, want empty ports field", raw)
	}
	if !strings.Contains(string(raw), `"ipWarning":"configured IP not present in guest"`) {
		t.Fatalf("ServiceNetwork JSON = %s, want ip warning field", raw)
	}
	if !strings.Contains(string(raw), `"runtimeIps":[{"label":"docker","ip":"192.168.48.1","interface":"br0"}]`) {
		t.Fatalf("ServiceNetwork JSON = %s, want runtime IPs field", raw)
	}

	var withPorts ServiceNetwork
	if err := json.Unmarshal([]byte(`{"ipWarning":"configured IP not present in guest","ports":[{"hostPort":80,"containerPort":80,"protocol":"tcp"}],"runtimeIps":[{"label":"netns","ip":"10.5.0.1"}]}`), &withPorts); err != nil {
		t.Fatalf("Unmarshal with ports: %v", err)
	}
	if !withPorts.PortsPresent || len(withPorts.Ports) != 1 || withPorts.Ports[0].Protocol != "tcp" || withPorts.IPWarning == "" || len(withPorts.RuntimeIPs) != 1 {
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
			VMMIsolation: "jailer-pending-restart",
			Image:        "vm://ubuntu/26.04",
			ImageVersion: "ubuntu-26.04-amd64-v1",
			CPUs:         4,
			MemoryBytes:  4 << 30,
			Balloon:      ServiceVMBalloon{Mode: "auto", MinBytes: 1 << 30, MinMemory: "1 GB", LastTarget: 512 << 20},
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
		`"vmmIsolation":"jailer-pending-restart"`,
		`"imageVersion":"ubuntu-26.04-amd64-v1"`,
		`"memoryBytes":4294967296`,
		`"balloon":{"mode":"auto","minBytes":1073741824,"minMemory":"1 GB","lastTargetBytes":536870912}`,
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
	if roundTrip.VM.VMMIsolation != "jailer-pending-restart" {
		t.Fatalf("round trip VMM isolation = %q", roundTrip.VM.VMMIsolation)
	}
	if roundTrip.VM.Balloon.Mode != "auto" || roundTrip.VM.Balloon.MinBytes != 1<<30 || roundTrip.VM.Balloon.MinMemory != "1 GB" || roundTrip.VM.Balloon.LastTarget != 512<<20 {
		t.Fatalf("round trip balloon = %#v, want auto 1 GB with target", roundTrip.VM.Balloon)
	}
}

func TestServiceVMVMMIsolationJSONRoundTrip(t *testing.T) {
	for _, want := range []string{"jailer", "jailer-pending-restart"} {
		t.Run(want, func(t *testing.T) {
			raw, err := json.Marshal(ServiceVM{VMMIsolation: want})
			if err != nil {
				t.Fatalf("Marshal ServiceVM: %v", err)
			}
			if !strings.Contains(string(raw), `"vmmIsolation":"`+want+`"`) {
				t.Fatalf("ServiceVM JSON = %s, want VMM isolation %q", raw, want)
			}
			var got ServiceVM
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("Unmarshal ServiceVM: %v", err)
			}
			if got.VMMIsolation != want {
				t.Fatalf("round trip VMM isolation = %q, want %q", got.VMMIsolation, want)
			}
		})
	}
}

func TestHostStorageSetRequestRoundTrip(t *testing.T) {
	req := HostStorageSetRequest{
		DataDir:         &HostStorageTarget{Value: "flash/yeet/data", ZFS: true},
		ServicesRoot:    &HostStorageTarget{Value: "flash/yeet/services", ZFS: true},
		MigrateServices: HostStorageMigrateAll,
		Yes:             true,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		t.Fatalf("Encode HostStorageSetRequest: %v", err)
	}
	var got HostStorageSetRequest
	if err := json.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("Decode HostStorageSetRequest: %v", err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("round trip = %#v, want %#v", got, req)
	}
}

func TestHostStoragePlanCatchActionRoundTrip(t *testing.T) {
	plan := HostStoragePlan{
		Current: HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		Desired: HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		CatchAction: HostStorageCatchAction{
			Move: true,
			From: "/root/data/services/catch",
			To:   "/flash/yeet/services/catch",
		},
		RequiresRestart: true,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(plan); err != nil {
		t.Fatalf("Encode HostStoragePlan: %v", err)
	}
	var got HostStoragePlan
	if err := json.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("Decode HostStoragePlan: %v", err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("round trip = %#v, want %#v", got, plan)
	}
}

func TestHostStoragePlanRepairActionRoundTrip(t *testing.T) {
	plan := HostStoragePlan{
		Current: HostStorageState{DataDir: "/root/data", ServicesRoot: "/root/data/services"},
		Desired: HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		RepairAction: HostStorageRepairAction{
			References:      7,
			DatabaseRefs:    4,
			SystemdRefs:     2,
			ArtifactRefs:    1,
			RegenerateUnits: []string{"api.service", "yeet-vm-devbox.service"},
			RestartServices: []string{"api", "devbox"},
			ValidationRoots: []string{"/root/data", "/root/data/services"},
		},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(plan); err != nil {
		t.Fatalf("Encode HostStoragePlan: %v", err)
	}
	var got HostStoragePlan
	if err := json.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("Decode HostStoragePlan: %v", err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("round trip = %#v, want %#v", got, plan)
	}
}
