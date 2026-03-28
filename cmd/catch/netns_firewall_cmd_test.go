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

	cfg := netns.FirewallEnv{
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
		ensureNetNSFirewall = func(backend netns.FirewallBackend, spec netns.FirewallSpec) error {
			called = "ensure"
			if backend != cfg.Backend || spec != cfg.Spec {
				t.Fatalf("ensureNetNSFirewall got (%v, %+v), want (%v, %+v)", backend, spec, cfg.Backend, cfg.Spec)
			}
			return nil
		}

		if err := handleNetNSFirewallCommand([]string{"ensure"}); err != nil {
			t.Fatalf("handleNetNSFirewallCommand returned error: %v", err)
		}
		if called != "ensure" {
			t.Fatalf("ensureNetNSFirewall was not called")
		}
	})

	t.Run("verify", func(t *testing.T) {
		restore := stubNetNSFirewallHelpers(cfg)
		defer restore()

		called := ""
		verifyNetNSFirewall = func(backend netns.FirewallBackend, spec netns.FirewallSpec) error {
			called = "verify"
			if backend != cfg.Backend || spec != cfg.Spec {
				t.Fatalf("verifyNetNSFirewall got (%v, %+v), want (%v, %+v)", backend, spec, cfg.Backend, cfg.Spec)
			}
			return nil
		}

		if err := handleNetNSFirewallCommand([]string{"verify"}); err != nil {
			t.Fatalf("handleNetNSFirewallCommand returned error: %v", err)
		}
		if called != "verify" {
			t.Fatalf("verifyNetNSFirewall was not called")
		}
	})

	t.Run("cleanup", func(t *testing.T) {
		restore := stubNetNSFirewallHelpers(cfg)
		defer restore()

		called := ""
		cleanupNetNSFirewall = func(backend netns.FirewallBackend) error {
			called = "cleanup"
			if backend != cfg.Backend {
				t.Fatalf("cleanupNetNSFirewall got %v, want %v", backend, cfg.Backend)
			}
			return nil
		}

		if err := handleNetNSFirewallCommand([]string{"cleanup"}); err != nil {
			t.Fatalf("handleNetNSFirewallCommand returned error: %v", err)
		}
		if called != "cleanup" {
			t.Fatalf("cleanupNetNSFirewall was not called")
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

		loadNetNSFirewallEnv = func(_ []string) (netns.FirewallEnv, error) {
			return netns.FirewallEnv{}, errors.New("boom")
		}

		if err := handleNetNSFirewallCommand([]string{"ensure"}); err == nil {
			t.Fatalf("handleNetNSFirewallCommand succeeded when env loading failed")
		}
	})
}

func stubNetNSFirewallHelpers(cfg netns.FirewallEnv) func() {
	prevLoad := loadNetNSFirewallEnv
	prevEnsure := ensureNetNSFirewall
	prevVerify := verifyNetNSFirewall
	prevCleanup := cleanupNetNSFirewall

	loadNetNSFirewallEnv = func(_ []string) (netns.FirewallEnv, error) {
		return cfg, nil
	}
	ensureNetNSFirewall = func(netns.FirewallBackend, netns.FirewallSpec) error { return nil }
	verifyNetNSFirewall = func(netns.FirewallBackend, netns.FirewallSpec) error { return nil }
	cleanupNetNSFirewall = func(netns.FirewallBackend) error { return nil }

	return func() {
		loadNetNSFirewallEnv = prevLoad
		ensureNetNSFirewall = prevEnsure
		verifyNetNSFirewall = prevVerify
		cleanupNetNSFirewall = prevCleanup
	}
}
