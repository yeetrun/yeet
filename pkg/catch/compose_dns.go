// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/netns"
	"gopkg.in/yaml.v3"
)

type composeDNSService struct {
	Name           string
	CustomResolver bool
}

type composeDNSOverlayService struct {
	DNS       []string `yaml:"dns,omitempty"`
	DNSSearch []string `yaml:"dns_search,omitempty"`
}

type composeNetworkOverlay struct {
	Services map[string]composeDNSOverlayService `yaml:"services,omitempty"`
	Networks map[string]composeOverlayNetwork    `yaml:"networks"`
}

type composeOverlayNetwork struct {
	Driver     string            `yaml:"driver"`
	DriverOpts map[string]string `yaml:"driver_opts"`
}

func composeDNSServices(raw []byte) ([]composeDNSService, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse compose yaml: %w", err)
	}
	root := yamlDocumentRoot(&doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose file root is not a map")
	}
	services := yamlMappingValue(root, "services")
	if services == nil {
		return nil, fmt.Errorf("compose file missing services")
	}
	if services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose services are not a map")
	}
	out := make([]composeDNSService, 0, len(services.Content)/2)
	for idx := 0; idx+1 < len(services.Content); idx += 2 {
		key := services.Content[idx]
		value := services.Content[idx+1]
		if key.Value == "" {
			return nil, fmt.Errorf("compose service name is empty")
		}
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("compose service %q is malformed", key.Value)
		}
		out = append(out, composeDNSService{
			Name:           key.Value,
			CustomResolver: composeServiceHasResolver(value, nil),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}
	return out, nil
}

func renderDockerComposeNetwork(env netns.Service, services []composeDNSService) (string, error) {
	overlay := composeNetworkOverlay{
		Networks: map[string]composeOverlayNetwork{
			"default": {
				Driver: "yeet",
				DriverOpts: map[string]string{
					"dev.catchit.netns": filepath.Join("/var/run/netns", env.NetNS()),
				},
			},
		},
	}
	if env.ServiceIP.IsValid() {
		for _, service := range services {
			if service.CustomResolver {
				continue
			}
			if overlay.Services == nil {
				overlay.Services = map[string]composeDNSOverlayService{}
			}
			overlay.Services[service.Name] = composeDNSOverlayService{
				DNS:       []string{yeetDNSHostIP},
				DNSSearch: []string{strings.TrimSuffix(yeetDNSDomain, ".")},
			}
		}
	}
	raw, err := yaml.Marshal(overlay)
	if err != nil {
		return "", fmt.Errorf("marshal compose network overlay: %w", err)
	}
	return string(raw), nil
}

func composeServiceHasResolver(node *yaml.Node, seen map[*yaml.Node]bool) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	if seen == nil {
		seen = map[*yaml.Node]bool{}
	}
	if seen[node] {
		return false
	}
	seen[node] = true
	for idx := 0; idx+1 < len(node.Content); idx += 2 {
		key := node.Content[idx]
		value := node.Content[idx+1]
		switch key.Value {
		case "dns", "dns_search":
			return true
		case "<<":
			if composeMergedServiceHasResolver(value, seen) {
				return true
			}
		}
	}
	return false
}

func composeMergedServiceHasResolver(node *yaml.Node, seen map[*yaml.Node]bool) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode {
		return composeMergedServiceHasResolver(node.Alias, seen)
	}
	if node.Kind == yaml.MappingNode {
		return composeServiceHasResolver(node, seen)
	}
	if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if composeMergedServiceHasResolver(item, seen) {
				return true
			}
		}
	}
	return false
}
