// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package db

import (
	"fmt"
	"log"

	"tailscale.com/util/mak"
)

const CurrentDataVersion = 5

var migrators = map[int]func(*Data) error{ // Start DataVersion -> NextStep
	3: reinit,
	4: addDockerEndpoints,
}

func reinit(d *Data) error {
	log.Fatal("Migration required but not supported, please delete the db file")
	return fmt.Errorf("unreachable")
}

func addDockerEndpoints(d *Data) error {
	for _, net := range d.DockerNetworks {
		for k, ep := range net.EndpointAddrs {
			mak.Set(&net.Endpoints, k, &DockerEndpoint{
				EndpointID: k,
				IPv4:       ep,
			})
		}
		net.EndpointAddrs = nil
	}
	return nil
}
