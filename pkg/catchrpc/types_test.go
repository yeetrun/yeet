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
	raw, err := json.Marshal(ServiceNetwork{PortsPresent: true})
	if err != nil {
		t.Fatalf("Marshal ServiceNetwork: %v", err)
	}
	if !strings.Contains(string(raw), `"ports":[]`) {
		t.Fatalf("ServiceNetwork JSON = %s, want empty ports field", raw)
	}

	var withPorts ServiceNetwork
	if err := json.Unmarshal([]byte(`{"ports":[{"hostPort":80,"containerPort":80,"protocol":"tcp"}]}`), &withPorts); err != nil {
		t.Fatalf("Unmarshal with ports: %v", err)
	}
	if !withPorts.PortsPresent || len(withPorts.Ports) != 1 || withPorts.Ports[0].Protocol != "tcp" {
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
