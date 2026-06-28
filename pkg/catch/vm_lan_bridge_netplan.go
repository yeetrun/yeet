// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type netplanDocument struct {
	Network netplanNetwork `yaml:"network"`
}

type netplanNetwork struct {
	Version   int                     `yaml:"version"`
	Renderer  string                  `yaml:"renderer,omitempty"`
	Ethernets map[string]netplanIface `yaml:"ethernets,omitempty"`
	Bridges   map[string]netplanIface `yaml:"bridges,omitempty"`
}

type netplanIface struct {
	Interfaces     []string                 `yaml:"interfaces,omitempty"`
	DHCP4          *bool                    `yaml:"dhcp4,omitempty"`
	DHCP6          *bool                    `yaml:"dhcp6,omitempty"`
	Optional       *bool                    `yaml:"optional,omitempty"`
	Addresses      []string                 `yaml:"addresses,omitempty"`
	Routes         []map[string]interface{} `yaml:"routes,omitempty"`
	Nameservers    map[string]interface{}   `yaml:"nameservers,omitempty"`
	MTU            int                      `yaml:"mtu,omitempty"`
	Parameters     map[string]interface{}   `yaml:"parameters,omitempty"`
	ClearAddresses bool                     `yaml:"-"`
	ClearRoutes    bool                     `yaml:"-"`
}

func renderVMLANBridgeNetplan(bridge, parent string, input []byte) ([]byte, error) {
	bridge = strings.TrimSpace(bridge)
	parent = strings.TrimSpace(parent)
	doc, err := parseVMLANBridgeNetplan(input)
	if err != nil {
		return nil, err
	}
	if err := validateNetplanBridgeInputs(doc, bridge, parent); err != nil {
		return nil, err
	}

	if err := moveNetplanUplinkConfigToBridge(&doc.Network, bridge, parent); err != nil {
		return nil, err
	}
	out, err := encodeNetplanDocument(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(vmLANBridgeNetplanMarker+"\n"), out...), nil
}

func parseVMLANBridgeNetplan(input []byte) (netplanDocument, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(input, &root); err != nil {
		return netplanDocument{}, fmt.Errorf("parse netplan: %w", err)
	}
	if err := validateNetplanShape(&root); err != nil {
		return netplanDocument{}, err
	}

	var doc netplanDocument
	if err := yaml.Unmarshal(input, &doc); err != nil {
		return netplanDocument{}, fmt.Errorf("parse netplan: %w", err)
	}
	if doc.Network.Version != 2 {
		return netplanDocument{}, fmt.Errorf("netplan network.version %d is not supported for automatic VM LAN bridge preparation", doc.Network.Version)
	}
	if err := validateNetplanRenderer(doc.Network.Renderer); err != nil {
		return netplanDocument{}, err
	}
	return doc, nil
}

func validateNetplanShape(root *yaml.Node) error {
	doc := netplanYAMLDocumentContent(root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		return fmt.Errorf("unsupported netplan document")
	}
	for _, entry := range netplanYAMLMappingEntries(doc) {
		if entry.key != "network" {
			return fmt.Errorf("unsupported netplan top-level key %q", entry.key)
		}
	}
	network := netplanYAMLMappingValue(doc, "network")
	if network == nil || network.Kind != yaml.MappingNode {
		return fmt.Errorf("unsupported netplan document without network section")
	}
	for _, entry := range netplanYAMLMappingEntries(network) {
		if err := validateNetplanNetworkEntry(entry.key, entry.value); err != nil {
			return err
		}
	}
	return nil
}

func validateNetplanNetworkEntry(key string, value *yaml.Node) error {
	switch key {
	case "version", "renderer":
		return nil
	case "ethernets":
		return validateNetplanInterfaces("ethernets", value, supportedNetplanEthernetKeys())
	case "bridges":
		return validateNetplanInterfaces("bridges", value, supportedNetplanBridgeKeys())
	default:
		return fmt.Errorf("unsupported netplan network key %q", key)
	}
}

func validateNetplanInterfaces(section string, node *yaml.Node, supported map[string]bool) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("unsupported netplan %s section", section)
	}
	for _, ifaceEntry := range netplanYAMLMappingEntries(node) {
		iface := ifaceEntry.key
		config := ifaceEntry.value
		if config.Kind != yaml.MappingNode {
			return fmt.Errorf("unsupported netplan %s.%s value", section, iface)
		}
		for _, configEntry := range netplanYAMLMappingEntries(config) {
			if !supported[configEntry.key] {
				return fmt.Errorf("unsupported netplan %s.%s key %q", section, iface, configEntry.key)
			}
			if section == "bridges" && configEntry.key == "parameters" {
				if err := validateNetplanBridgeParameters(iface, configEntry.value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateNetplanBridgeParameters(iface string, node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("unsupported netplan bridges.%s.parameters value", iface)
	}
	supported := map[string]bool{"stp": true, "forward-delay": true}
	for _, entry := range netplanYAMLMappingEntries(node) {
		if !supported[entry.key] {
			return fmt.Errorf("unsupported netplan bridges.%s.parameters key %q", iface, entry.key)
		}
	}
	return nil
}

func supportedNetplanEthernetKeys() map[string]bool {
	return map[string]bool{
		"dhcp4":       true,
		"dhcp6":       true,
		"optional":    true,
		"addresses":   true,
		"routes":      true,
		"nameservers": true,
		"mtu":         true,
	}
}

func supportedNetplanBridgeKeys() map[string]bool {
	keys := supportedNetplanEthernetKeys()
	keys["interfaces"] = true
	keys["parameters"] = true
	return keys
}

func netplanYAMLDocumentContent(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	return node
}

func netplanYAMLMappingValue(node *yaml.Node, key string) *yaml.Node {
	for _, entry := range netplanYAMLMappingEntries(node) {
		if entry.key == key {
			return entry.value
		}
	}
	return nil
}

func netplanRawDefinesEthernet(input []byte, parent string) bool {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return false
	}
	var root yaml.Node
	if err := yaml.Unmarshal(input, &root); err != nil {
		return false
	}
	doc := netplanYAMLDocumentContent(&root)
	network := netplanYAMLMappingValue(doc, "network")
	ethernets := netplanYAMLMappingValue(network, "ethernets")
	if ethernets == nil || ethernets.Kind != yaml.MappingNode {
		return false
	}
	return netplanYAMLMappingValue(ethernets, parent) != nil
}

type netplanYAMLMappingEntry struct {
	key   string
	value *yaml.Node
}

func netplanYAMLMappingEntries(node *yaml.Node) []netplanYAMLMappingEntry {
	if node == nil {
		return nil
	}
	out := make([]netplanYAMLMappingEntry, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		out = append(out, netplanYAMLMappingEntry{key: node.Content[i].Value, value: node.Content[i+1]})
	}
	return out
}

func validateNetplanBridgeInputs(doc netplanDocument, bridge, parent string) error {
	if bridge == "" {
		return fmt.Errorf("VM LAN bridge name is required")
	}
	if parent == "" {
		return fmt.Errorf("VM LAN bridge parent interface is required")
	}
	if _, ok := doc.Network.Ethernets[parent]; !ok {
		return fmt.Errorf("netplan ethernet %q was not found", parent)
	}
	existing, ok := doc.Network.Bridges[bridge]
	if ok && !slices.Equal(existing.Interfaces, []string{parent}) {
		return fmt.Errorf("netplan bridge %q already exists with interfaces %v", bridge, existing.Interfaces)
	}
	return nil
}

func moveNetplanUplinkConfigToBridge(network *netplanNetwork, bridge, parent string) error {
	uplink := network.Ethernets[parent]
	existing := network.Bridges[bridge]
	bridgeIface := existing
	bridgeIface.Interfaces = []string{parent}
	if netplanIfaceHasMovableConfig(uplink) {
		bridgeIface.DHCP4 = uplink.DHCP4
		bridgeIface.DHCP6 = uplink.DHCP6
		bridgeIface.Optional = uplink.Optional
		bridgeIface.Addresses = uplink.Addresses
		bridgeIface.Routes = uplink.Routes
		bridgeIface.Nameservers = uplink.Nameservers
		bridgeIface.MTU = uplink.MTU
	} else if !netplanIfaceHasMovableConfig(bridgeIface) {
		return fmt.Errorf("no movable netplan config found on %q or bridge %q", parent, bridge)
	}
	if bridgeIface.Parameters == nil {
		bridgeIface.Parameters = make(map[string]interface{})
	}
	bridgeIface.Parameters["stp"] = false
	bridgeIface.Parameters["forward-delay"] = 0

	uplink.DHCP4 = netplanBool(false)
	uplink.DHCP6 = netplanBool(false)
	uplink.Addresses = nil
	uplink.Routes = nil
	uplink.ClearAddresses = true
	uplink.ClearRoutes = true
	if uplink.Nameservers != nil {
		uplink.Nameservers = map[string]interface{}{"addresses": []interface{}{}}
	}
	uplink.MTU = 0

	if network.Bridges == nil {
		network.Bridges = make(map[string]netplanIface)
	}
	network.Ethernets[parent] = uplink
	network.Bridges[bridge] = bridgeIface
	return nil
}

func netplanIfaceHasMovableConfig(iface netplanIface) bool {
	return (iface.DHCP4 != nil && *iface.DHCP4) ||
		(iface.DHCP6 != nil && *iface.DHCP6) ||
		len(iface.Addresses) > 0 ||
		len(iface.Routes) > 0 ||
		netplanNameserversHasConfig(iface.Nameservers) ||
		iface.MTU != 0
}

func netplanNameserversHasConfig(nameservers map[string]interface{}) bool {
	for _, value := range nameservers {
		if !netplanValueIsEmpty(value) {
			return true
		}
	}
	return false
}

func netplanValueIsEmpty(value interface{}) bool {
	switch v := value.(type) {
	case nil:
		return true
	case []interface{}:
		return len(v) == 0
	case []string:
		return len(v) == 0
	default:
		return false
	}
}

func encodeNetplanDocument(doc netplanDocument) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("render netplan: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("render netplan: %w", err)
	}
	return out.Bytes(), nil
}

func netplanBool(v bool) *bool {
	return &v
}

func (iface netplanIface) MarshalYAML() (interface{}, error) {
	out := map[string]interface{}{}
	addNetplanStringList(out, "interfaces", iface.Interfaces)
	addNetplanBoolPtr(out, "dhcp4", iface.DHCP4)
	addNetplanBoolPtr(out, "dhcp6", iface.DHCP6)
	addNetplanBoolPtr(out, "optional", iface.Optional)
	addNetplanClearableStringList(out, "addresses", iface.Addresses, iface.ClearAddresses)
	addNetplanClearableRouteList(out, "routes", iface.Routes, iface.ClearRoutes)
	addNetplanMap(out, "nameservers", iface.Nameservers)
	addNetplanInt(out, "mtu", iface.MTU)
	addNetplanMap(out, "parameters", iface.Parameters)
	return out, nil
}

func addNetplanStringList(out map[string]interface{}, key string, values []string) {
	if len(values) > 0 {
		out[key] = values
	}
}

func addNetplanBoolPtr(out map[string]interface{}, key string, value *bool) {
	if value != nil {
		out[key] = *value
	}
}

func addNetplanClearableStringList(out map[string]interface{}, key string, values []string, clear bool) {
	if clear {
		out[key] = []string{}
		return
	}
	addNetplanStringList(out, key, values)
}

func addNetplanClearableRouteList(out map[string]interface{}, key string, values []map[string]interface{}, clear bool) {
	if clear {
		out[key] = []map[string]interface{}{}
		return
	}
	if len(values) > 0 {
		out[key] = values
	}
}

func addNetplanMap(out map[string]interface{}, key string, values map[string]interface{}) {
	if values != nil {
		out[key] = values
	}
}

func addNetplanInt(out map[string]interface{}, key string, value int) {
	if value != 0 {
		out[key] = value
	}
}

func validateNetplanRenderer(renderer string) error {
	switch strings.TrimSpace(renderer) {
	case "", "networkd":
		return nil
	case "NetworkManager":
		return fmt.Errorf("NetworkManager renderer is not supported for automatic VM LAN bridge preparation")
	default:
		return fmt.Errorf("netplan renderer %q is not supported for automatic VM LAN bridge preparation", renderer)
	}
}
