// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"testing"

	"github.com/yeetrun/yeet/pkg/netns"
)

func TestHandleNetNSFirewallCommand(t *testing.T) {
	t.Parallel()

	cfg := netns.FirewallConfig{
		Backend: netns.BackendNFT,
		Spec: netns.FirewallSpec{
			SubnetCIDR: "192.168.100.0/24",
			BridgeIf:   "yeet0",
		},
	}

	t.Run("ensure", func(t *testing.T) {
		restore := stubNetNSFirewallHelpers(cfg)
		defer restore()

		called := ""
		ensureFirewall = func(backend netns.FirewallBackend, spec netns.FirewallSpec) error {
			called = "ensure"
			if backend != cfg.Backend || spec != cfg.Spec {
				t.Fatalf("ensureFirewall got (%v, %+v), want (%v, %+v)", backend, spec, cfg.Backend, cfg.Spec)
			}
			return nil
		}

		if err := handleNetNSFirewallCommand([]string{"ensure"}); err != nil {
			t.Fatalf("handleNetNSFirewallCommand returned error: %v", err)
		}
		if called != "ensure" {
			t.Fatalf("ensureFirewall was not called")
		}
	})

	t.Run("verify", func(t *testing.T) {
		restore := stubNetNSFirewallHelpers(cfg)
		defer restore()

		called := ""
		verifyFirewall = func(backend netns.FirewallBackend, spec netns.FirewallSpec) error {
			called = "verify"
			if backend != cfg.Backend || spec != cfg.Spec {
				t.Fatalf("verifyFirewall got (%v, %+v), want (%v, %+v)", backend, spec, cfg.Backend, cfg.Spec)
			}
			return nil
		}

		if err := handleNetNSFirewallCommand([]string{"verify"}); err != nil {
			t.Fatalf("handleNetNSFirewallCommand returned error: %v", err)
		}
		if called != "verify" {
			t.Fatalf("verifyFirewall was not called")
		}
	})

	t.Run("cleanup", func(t *testing.T) {
		restore := stubNetNSFirewallHelpers(cfg)
		defer restore()

		called := ""
		cleanupFirewall = func(backend netns.FirewallBackend) error {
			called = "cleanup"
			if backend != cfg.Backend {
				t.Fatalf("cleanupFirewall got %v, want %v", backend, cfg.Backend)
			}
			return nil
		}

		if err := handleNetNSFirewallCommand([]string{"cleanup"}); err != nil {
			t.Fatalf("handleNetNSFirewallCommand returned error: %v", err)
		}
		if called != "cleanup" {
			t.Fatalf("cleanupFirewall was not called")
		}
	})

	t.Run("unknown action", func(t *testing.T) {
		restore := stubNetNSFirewallHelpers(cfg)
		defer restore()

		if err := handleNetNSFirewallCommand([]string{"bogus"}); err == nil {
			t.Fatalf("handleNetNSFirewallCommand succeeded for unknown action")
		}
	})

	t.Run("load env error", func(t *testing.T) {
		restore := stubNetNSFirewallHelpers(cfg)
		defer restore()

		loadFirewallEnv = func(_ []string) (netns.FirewallConfig, error) {
			return netns.FirewallConfig{}, errors.New("boom")
		}

		if err := handleNetNSFirewallCommand([]string{"ensure"}); err == nil {
			t.Fatalf("handleNetNSFirewallCommand succeeded when env loading failed")
		}
	})
}

func stubNetNSFirewallHelpers(cfg netns.FirewallConfig) func() {
	prevLoad := loadFirewallEnv
	prevEnsure := ensureFirewall
	prevVerify := verifyFirewall
	prevCleanup := cleanupFirewall

	loadFirewallEnv = func(_ []string) (netns.FirewallConfig, error) {
		return cfg, nil
	}
	ensureFirewall = func(netns.FirewallBackend, netns.FirewallSpec) error { return nil }
	verifyFirewall = func(netns.FirewallBackend, netns.FirewallSpec) error { return nil }
	cleanupFirewall = func(netns.FirewallBackend) error { return nil }

	return func() {
		loadFirewallEnv = prevLoad
		ensureFirewall = prevEnsure
		verifyFirewall = prevVerify
		cleanupFirewall = prevCleanup
	}
}
