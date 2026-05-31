// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func normalizePublish(publish []string) []string {
	ports := make([]string, 0, len(publish))
	for _, entry := range publish {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			ports = append(ports, normalizePublishPort(trimmed))
		}
	}
	return ports
}

func normalizePublishPort(port string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(port), "/tcp"):
		return port[:len(port)-len("/tcp")]
	case strings.HasSuffix(strings.ToLower(port), "/udp"):
		return port[:len(port)-len("/udp")] + "/udp"
	default:
		return port
	}
}

func updateComposePorts(path, serviceName string, publish []string) error {
	ports := normalizePublish(publish)
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return err
	}
	serviceMap, err := composeServiceMap(doc, serviceName)
	if err != nil {
		return err
	}
	if len(ports) == 0 {
		delete(serviceMap, "ports")
	} else {
		serviceMap["ports"] = ports
	}
	updated, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, updated, 0644)
}

func readComposePorts(path, serviceName string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	serviceMap, err := composeServiceMap(doc, serviceName)
	if err != nil {
		return nil, err
	}
	portsRaw, ok := serviceMap["ports"]
	if !ok {
		return nil, nil
	}
	portsList, ok := portsRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("compose service %q ports are malformed", serviceName)
	}
	ports := make([]string, 0, len(portsList))
	for i, entry := range portsList {
		port, ok := entry.(string)
		if !ok {
			return nil, fmt.Errorf("compose service %q ports are malformed at index %d", serviceName, i)
		}
		ports = append(ports, port)
	}
	return normalizePublish(ports), nil
}

func composeServiceMap(doc map[string]any, serviceName string) (map[string]any, error) {
	servicesRaw, ok := doc["services"]
	if !ok {
		return nil, fmt.Errorf("compose file missing services")
	}
	services, ok := servicesRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("compose services are not a map")
	}
	serviceRaw, ok := services[serviceName]
	if !ok {
		return nil, fmt.Errorf("compose service %q not found", serviceName)
	}
	serviceMap, ok := serviceRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("compose service %q is malformed", serviceName)
	}
	return serviceMap, nil
}
