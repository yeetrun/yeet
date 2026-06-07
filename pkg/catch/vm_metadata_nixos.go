// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func writeVMGuestNixOSMetadataFiles(root string, cfg vmMetadataConfig) error {
	if err := writeVMGuestFile(root, "etc/yeet-vm/hostname", []byte(cfg.Hostname+"\n"), 0o644); err != nil {
		return err
	}
	if err := writeVMGuestFile(root, "etc/yeet-vm/user", []byte(cfg.User+"\n"), 0o644); err != nil {
		return err
	}
	if err := writeVMGuestFile(root, "etc/yeet-vm/authorized_keys", []byte(cfg.SSHKey+"\n"), 0o600); err != nil {
		return err
	}
	for _, network := range cfg.Networks {
		if err := validateVMGuestNetwork(network); err != nil {
			return err
		}
		rel := filepath.Join("etc", "yeet-vm", "systemd-network", "10-yeet-"+network.Name+".network")
		if err := writeVMGuestFile(root, rel, []byte(renderVMNetworkdUnit(network)), 0o644); err != nil {
			return fmt.Errorf("write NixOS VM network metadata: %w", err)
		}
	}
	return nil
}

func ensureVMGuestNixOSSSHHostKeys(ctx context.Context, root, keyDir string, runner vmCommandRunner) error {
	if runner == nil {
		runner = runVMCommand
	}
	if err := restoreVMGuestSSHHostKeys(root, keyDir); err != nil {
		return err
	}
	sshDir := filepath.Join(root, "etc", "ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		return err
	}
	keys := []struct {
		Type string
		Path string
	}{
		{Type: "ed25519", Path: filepath.Join(sshDir, "ssh_host_ed25519_key")},
		{Type: "rsa", Path: filepath.Join(sshDir, "ssh_host_rsa_key")},
	}
	for _, key := range keys {
		if info, err := os.Stat(key.Path); err == nil && info.Size() > 0 {
			continue
		}
		if err := runner(ctx, []string{"ssh-keygen", "-t", key.Type, "-f", key.Path, "-N", ""}); err != nil {
			return fmt.Errorf("generate NixOS VM SSH host key %s: %w", key.Type, err)
		}
	}
	return persistVMGuestSSHHostKeys(root, keyDir)
}
