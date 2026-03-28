// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"

	"github.com/yeetrun/yeet/pkg/netns"
)

var (
	loadFirewallEnv = netns.LoadFirewallEnv
	ensureFirewall  = netns.EnsureFirewall
	verifyFirewall  = netns.VerifyFirewall
	cleanupFirewall = netns.CleanupFirewall
)

func handleNetNSFirewallCommand(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: catch netns-firewall <ensure|verify|cleanup>")
	}

	fwEnv, err := loadFirewallEnv(os.Environ())
	if err != nil {
		return err
	}

	switch args[0] {
	case "ensure":
		return ensureFirewall(fwEnv.Backend, fwEnv.Spec)
	case "verify":
		return verifyFirewall(fwEnv.Backend, fwEnv.Spec)
	case "cleanup":
		return cleanupFirewall(fwEnv.Backend)
	default:
		return fmt.Errorf("unknown netns-firewall command %q", args[0])
	}
}
