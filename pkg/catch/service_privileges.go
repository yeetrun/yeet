// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

func validateNativeServicePrivilegedPorts(service string, publish []string, identity db.ServiceIdentity) error {
	if identity.UID == 0 {
		return nil
	}
	for _, mapping := range normalizePublish(publish) {
		port, declared, err := nativePublishedHostPort(mapping)
		if err != nil {
			return fmt.Errorf("service %q has invalid published port %q: %w", service, mapping, err)
		}
		if declared && port < 1024 {
			return fmt.Errorf(
				"native service %s declares privileged host port %d but runs as %s (%d:%d); use an unprivileged port, put a root-owned proxy in front, or explicitly pass --run-as=root",
				service, port, identity.RequestedUser, identity.UID, identity.GID,
			)
		}
	}
	return nil
}

func nativePublishedHostPort(mapping string) (int, bool, error) {
	mapping = strings.TrimSpace(mapping)
	if slash := strings.LastIndexByte(mapping, '/'); slash >= 0 {
		mapping = mapping[:slash]
	}
	last := strings.LastIndexByte(mapping, ':')
	if last < 0 {
		return 0, false, nil
	}
	prefix := mapping[:last]
	previous := strings.LastIndexByte(prefix, ':')
	host := prefix
	if previous >= 0 {
		host = prefix[previous+1:]
	}
	if dash := strings.IndexByte(host, '-'); dash >= 0 {
		host = host[:dash]
	}
	port, err := strconv.Atoi(host)
	if err != nil || port < 1 || port > 65535 {
		return 0, true, fmt.Errorf("invalid host port %q", host)
	}
	return port, true, nil
}
