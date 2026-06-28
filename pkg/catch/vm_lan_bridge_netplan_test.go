// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const eno1DHCPNetplan = `
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      dhcp4: true
      dhcp6: true
      optional: true
`

const eno1StaticNetplan = `
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      addresses:
        - 192.168.50.22/24
      routes:
        - to: default
          via: 192.168.50.1
      nameservers:
        addresses:
          - 192.168.50.1
          - 1.1.1.1
`

const eno1BridgeDHCPNetplan = `
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      optional: true
  bridges:
    br0:
      interfaces:
        - eno1
      dhcp4: true
      dhcp6: true
      parameters:
        stp: false
        forward-delay: 0
`

const eno1AddresslessBridgeNetplan = `
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      optional: true
  bridges:
    br0:
      interfaces:
        - eno1
      parameters:
        stp: false
        forward-delay: 0
`

func TestRenderVMLANBridgeNetplanMovesDHCPToBridge(t *testing.T) {
	out, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(eno1DHCPNetplan))
	if err != nil {
		t.Fatalf("renderVMLANBridgeNetplan: %v", err)
	}
	text := string(out)
	if !strings.HasPrefix(text, "# Managed by yeet VM LAN bridge preparation; do not edit.\n") {
		t.Fatalf("rendered netplan missing managed header:\n%s", text)
	}
	for _, want := range []string{
		"bridges:",
		"br0:",
		"interfaces:",
		"- eno1",
		"dhcp4: true",
		"dhcp6: true",
		"dhcp4: false",
		"dhcp6: false",
		"stp: false",
		"forward-delay: 0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered netplan missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "eno1:\n      dhcp4: true") {
		t.Fatalf("uplink still owns dhcp4:\n%s", text)
	}
	rendered := parseRenderedNetplan(t, out)
	parent := rendered.Network.Ethernets["eno1"]
	if parent.DHCP4 == nil || *parent.DHCP4 || parent.DHCP6 == nil || *parent.DHCP6 {
		t.Fatalf("parent DHCP = %v/%v, want explicitly disabled", parent.DHCP4, parent.DHCP6)
	}
	if parent.Optional == nil || !*parent.Optional {
		t.Fatalf("parent optional = %v, want preserved true", parent.Optional)
	}
	bridge := rendered.Network.Bridges["br0"]
	if bridge.DHCP4 == nil || !*bridge.DHCP4 || bridge.DHCP6 == nil || !*bridge.DHCP6 {
		t.Fatalf("bridge DHCP = %v/%v, want true/true", bridge.DHCP4, bridge.DHCP6)
	}
}

func TestRenderVMLANBridgeNetplanIsIdempotentForMovedDHCP(t *testing.T) {
	first, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(eno1DHCPNetplan))
	if err != nil {
		t.Fatalf("first renderVMLANBridgeNetplan: %v", err)
	}
	second, err := renderVMLANBridgeNetplan("br0", "eno1", first)
	if err != nil {
		t.Fatalf("second renderVMLANBridgeNetplan: %v", err)
	}
	firstDoc := parseRenderedNetplan(t, first)
	secondDoc := parseRenderedNetplan(t, second)
	firstBridge := firstDoc.Network.Bridges["br0"]
	secondBridge := secondDoc.Network.Bridges["br0"]
	if firstBridge.DHCP4 == nil || !*firstBridge.DHCP4 || firstBridge.DHCP6 == nil || !*firstBridge.DHCP6 {
		t.Fatalf("first bridge DHCP = %v/%v, want true/true", firstBridge.DHCP4, firstBridge.DHCP6)
	}
	if secondBridge.DHCP4 == nil || !*secondBridge.DHCP4 || secondBridge.DHCP6 == nil || !*secondBridge.DHCP6 {
		t.Fatalf("second bridge DHCP = %v/%v, want true/true", secondBridge.DHCP4, secondBridge.DHCP6)
	}
	secondParent := secondDoc.Network.Ethernets["eno1"]
	if secondParent.DHCP4 == nil || *secondParent.DHCP4 || secondParent.DHCP6 == nil || *secondParent.DHCP6 {
		t.Fatalf("second parent DHCP = %v/%v, want explicitly disabled", secondParent.DHCP4, secondParent.DHCP6)
	}
}

func TestRenderVMLANBridgeNetplanKeepsExistingBridgeDHCP(t *testing.T) {
	out, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(eno1BridgeDHCPNetplan))
	if err != nil {
		t.Fatalf("renderVMLANBridgeNetplan: %v", err)
	}
	rendered := parseRenderedNetplan(t, out)
	parent := rendered.Network.Ethernets["eno1"]
	if parent.Optional == nil || !*parent.Optional {
		t.Fatalf("parent optional = %v, want preserved true", parent.Optional)
	}
	if parent.DHCP4 == nil || *parent.DHCP4 || parent.DHCP6 == nil || *parent.DHCP6 {
		t.Fatalf("parent DHCP = %v/%v, want explicitly disabled parent", parent.DHCP4, parent.DHCP6)
	}
	bridge := rendered.Network.Bridges["br0"]
	if bridge.DHCP4 == nil || !*bridge.DHCP4 || bridge.DHCP6 == nil || !*bridge.DHCP6 {
		t.Fatalf("bridge DHCP = %v/%v, want true/true", bridge.DHCP4, bridge.DHCP6)
	}
}

func TestRenderVMLANBridgeNetplanRejectsAddresslessParentAndBridge(t *testing.T) {
	_, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(eno1AddresslessBridgeNetplan))
	if err == nil || !strings.Contains(err.Error(), "no movable netplan config") {
		t.Fatalf("error = %v, want no movable netplan config", err)
	}
}

func TestRenderVMLANBridgeNetplanMovesStaticConfigToBridge(t *testing.T) {
	out, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(eno1StaticNetplan))
	if err != nil {
		t.Fatalf("renderVMLANBridgeNetplan: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"addresses:",
		"- 192.168.50.22/24",
		"routes:",
		"via: 192.168.50.1",
		"nameservers:",
		"- 1.1.1.1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered netplan missing %q:\n%s", want, text)
		}
	}
	rendered := parseRenderedNetplan(t, out)
	parent := rendered.Network.Ethernets["eno1"]
	if parent.DHCP4 == nil || *parent.DHCP4 || parent.DHCP6 == nil || *parent.DHCP6 || len(parent.Addresses) > 0 || len(parent.Routes) > 0 || parent.Nameservers == nil {
		t.Fatalf("parent retained network config: %#v", parent)
	}
	bridge := rendered.Network.Bridges["br0"]
	if got, want := bridge.Addresses, []string{"192.168.50.22/24"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("bridge addresses = %#v, want %#v", got, want)
	}
	if len(bridge.Routes) != 1 || bridge.Routes[0]["via"] != "192.168.50.1" {
		t.Fatalf("bridge routes = %#v, want route via 192.168.50.1", bridge.Routes)
	}
	if bridge.Nameservers == nil {
		t.Fatalf("bridge nameservers missing")
	}
}

func TestRenderVMLANBridgeNetplanRejectsNetworkManager(t *testing.T) {
	_, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(`
network:
  version: 2
  renderer: NetworkManager
  ethernets:
    eno1:
      dhcp4: true
`))
	if err == nil || !strings.Contains(err.Error(), "NetworkManager renderer is not supported") {
		t.Fatalf("error = %v, want NetworkManager unsupported", err)
	}
}

func TestRenderVMLANBridgeNetplanRejectsUnsupportedNetworkSection(t *testing.T) {
	_, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(`
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      dhcp4: true
  vlans:
    eno1.10:
      id: 10
      link: eno1
`))
	if err == nil || !strings.Contains(err.Error(), `unsupported netplan network key "vlans"`) {
		t.Fatalf("error = %v, want unsupported netplan vlans", err)
	}
}

func TestRenderVMLANBridgeNetplanRejectsUnsupportedParentField(t *testing.T) {
	_, err := renderVMLANBridgeNetplan("br0", "eno1", []byte(`
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      gateway4: 192.168.50.1
`))
	if err == nil || !strings.Contains(err.Error(), `unsupported netplan ethernets.eno1 key "gateway4"`) {
		t.Fatalf("error = %v, want unsupported netplan gateway4", err)
	}
}

func TestNetplanBool(t *testing.T) {
	value := netplanBool(true)
	if value == nil || !*value {
		t.Fatalf("netplanBool(true) = %v, want pointer to true", value)
	}
}

func parseRenderedNetplan(t *testing.T, data []byte) netplanDocument {
	t.Helper()
	var doc netplanDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse rendered netplan: %v\n%s", err, data)
	}
	return doc
}
