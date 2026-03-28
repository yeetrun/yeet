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
	loadNetNSFirewallEnv = netns.LoadFirewallEnv
	ensureNetNSFirewall  = netns.EnsureFirewall
	verifyNetNSFirewall  = netns.VerifyFirewall
	cleanupNetNSFirewall = netns.CleanupFirewall
	getEnviron           = os.Environ
)

func handleNetNSFirewallCommand(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: catch netns-firewall <ensure|verify|cleanup>")
	}

	fwEnv, err := loadNetNSFirewallEnv(getEnviron())
	if err != nil {
		return err
	}

	switch args[0] {
	case "ensure":
		return ensureNetNSFirewall(fwEnv.Backend, fwEnv.Spec)
	case "verify":
		return verifyNetNSFirewall(fwEnv.Backend, fwEnv.Spec)
	case "cleanup":
		return cleanupNetNSFirewall(fwEnv.Backend)
	default:
		return fmt.Errorf("unknown netns-firewall command %q", args[0])
	}
}
