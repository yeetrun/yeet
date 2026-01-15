// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func (s *Server) setupTailscale(clientSecret string) (catchrpc.TailscaleSetupResponse, error) {
	secret := strings.TrimSpace(clientSecret)
	if secret == "" {
		return catchrpc.TailscaleSetupResponse{}, fmt.Errorf("client secret is required")
	}
	if !strings.HasPrefix(secret, "tskey-client-") {
		return catchrpc.TailscaleSetupResponse{}, fmt.Errorf("invalid client secret (expected tskey-client-...)")
	}

	dataDir := s.serviceDataDir(CatchService)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return catchrpc.TailscaleSetupResponse{}, fmt.Errorf("failed to create catch data dir: %w", err)
	}
	path := filepath.Join(dataDir, "tailscale.key")
	payload := []byte(secret + "\n")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		return catchrpc.TailscaleSetupResponse{}, fmt.Errorf("failed to write tailscale.key: %w", err)
	}

	verified := false
	if b, err := os.ReadFile(path); err == nil {
		verified = strings.TrimSpace(string(b)) == secret
	}

	return catchrpc.TailscaleSetupResponse{
		Path:     path,
		Verified: verified,
	}, nil
}
