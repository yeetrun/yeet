// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"strings"
)

func resolveServiceHost(cfg *ProjectConfig, service string) (string, error) {
	if cfg == nil || service == "" {
		return "", nil
	}
	hosts := cfg.ServiceHosts(service)
	if len(hosts) == 0 {
		return "", nil
	}
	if len(hosts) == 1 {
		return hosts[0], nil
	}
	return "", ambiguousServiceError(service, hosts)
}

func ambiguousServiceError(service string, hosts []string) error {
	choices := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if host == "" {
			continue
		}
		choices = append(choices, service+"@"+host)
	}
	return fmt.Errorf("service %q is configured for multiple hosts: %s", service, strings.Join(choices, ", "))
}
