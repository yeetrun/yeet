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
			ports = append(ports, trimmed)
		}
	}
	return ports
}

func updateComposePorts(path, serviceName string, publish []string) error {
	ports := normalizePublish(publish)
	if len(ports) == 0 {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return err
	}
	servicesRaw, ok := doc["services"]
	if !ok {
		return fmt.Errorf("compose file missing services")
	}
	services, ok := servicesRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("compose services are not a map")
	}
	serviceRaw, ok := services[serviceName]
	if !ok {
		return fmt.Errorf("compose service %q not found", serviceName)
	}
	serviceMap, ok := serviceRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("compose service %q is malformed", serviceName)
	}
	serviceMap["ports"] = ports
	services[serviceName] = serviceMap
	doc["services"] = services
	updated, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, updated, 0644)
}
