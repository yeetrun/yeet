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
	serviceRoot, err := s.serviceRootDir(CatchService)
	if err != nil {
		return catchrpc.TailscaleSetupResponse{}, fmt.Errorf("failed to resolve catch service root: %w", err)
	}
	path, err := writeTailscaleClientSecretAtServiceRoot(serviceRoot, clientSecret)
	if err != nil {
		return catchrpc.TailscaleSetupResponse{}, err
	}

	verified := false
	if b, err := os.ReadFile(path); err == nil {
		verified = strings.TrimSpace(string(b)) == strings.TrimSpace(clientSecret)
	}

	return catchrpc.TailscaleSetupResponse{
		Path:     path,
		Verified: verified,
	}, nil
}

// WriteCatchTailscaleClientSecret stores the OAuth client secret where catch and
// yeet-managed Tailscale service networking expect to find it.
func WriteCatchTailscaleClientSecret(rootDir string, clientSecret string) (string, error) {
	return writeTailscaleClientSecretAtServiceRoot(filepath.Join(rootDir, "services", CatchService), clientSecret)
}

func writeTailscaleClientSecretAtServiceRoot(serviceRoot string, clientSecret string) (string, error) {
	secret := strings.TrimSpace(clientSecret)
	if secret == "" {
		return "", fmt.Errorf("client secret is required")
	}
	if !strings.HasPrefix(secret, "tskey-client-") {
		return "", fmt.Errorf("invalid client secret (expected tskey-client-...)")
	}
	dataDir := serviceDataDirForRoot(serviceRoot)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create catch data dir: %w", err)
	}
	path := filepath.Join(dataDir, "tailscale.key")
	payload := []byte(secret + "\n")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		return "", fmt.Errorf("failed to write tailscale.key: %w", err)
	}
	return path, nil
}
