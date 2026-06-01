// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var vmGuestNetworkNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
var vmHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var vmUserPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)

type vmMetadataConfig struct {
	Hostname string
	User     string
	SSHKey   string
	Networks []vmGuestNetwork

	HostKeyDir string
}

type vmGuestNetwork struct {
	Name        string
	Mode        string
	Address     string
	Gateway     string
	DHCP        bool
	Nameservers []string
}

type vmGuestMetadataWriter func(string, vmMetadataConfig) error

var defaultVMSSHAuthorizedKeyPaths = func() []string {
	paths := []string{}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		paths = append(paths, filepath.Join(home, ".ssh", "authorized_keys"))
	}
	return append(paths, "/root/.ssh/authorized_keys")
}

func writeVMMetadata(root string, cfg vmMetadataConfig) error {
	if err := validateVMMetadata(cfg); err != nil {
		return err
	}
	dir := filepath.Join(root, "metadata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeVMMetadataFile(filepath.Join(dir, "hostname"), []byte(cfg.Hostname+"\n"), 0o644); err != nil {
		return err
	}
	if err := writeVMMetadataFile(filepath.Join(dir, "user"), []byte(cfg.User+"\n"), 0o644); err != nil {
		return err
	}
	if err := writeVMMetadataFile(filepath.Join(dir, "authorized_keys"), []byte(cfg.SSHKey+"\n"), 0o600); err != nil {
		return err
	}
	return writeVMMetadataFile(filepath.Join(dir, "network.yaml"), []byte(renderVMNetworkYAML(cfg.Networks)), 0o644)
}

func defaultVMSSHKey() (string, error) {
	if key, ok := normalizeVMAuthorizedKeyLine(os.Getenv("YEET_VM_SSH_KEY")); ok {
		return key, nil
	}
	seen := map[string]bool{}
	for _, path := range defaultVMSSHAuthorizedKeyPaths() {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		key, err := firstVMSSHAuthorizedKeyFromFile(path)
		if err == nil {
			return key, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no SSH public key found for VM; set YEET_VM_SSH_KEY or add a key to ~/.ssh/authorized_keys")
}

func firstVMSSHAuthorizedKeyFromFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key, ok := vmSSHKeyFromAuthorizedKeys(raw)
	if !ok {
		return "", fmt.Errorf("no SSH public key found in %s", path)
	}
	return key, nil
}

func vmSSHKeyFromAuthorizedKeys(raw []byte) (string, bool) {
	for _, line := range strings.Split(string(raw), "\n") {
		if key, ok := normalizeVMAuthorizedKeyLine(line); ok {
			return key, true
		}
	}
	return "", false
}

func normalizeVMAuthorizedKeyLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	fields := strings.Fields(line)
	for i, field := range fields {
		if !isVMSSHKeyType(field) || i+1 >= len(fields) {
			continue
		}
		return strings.Join(fields[i:], " "), true
	}
	return "", false
}

func isVMSSHKeyType(value string) bool {
	return strings.HasPrefix(value, "ssh-") ||
		strings.HasPrefix(value, "ecdsa-sha2-") ||
		strings.HasPrefix(value, "sk-ssh-") ||
		strings.HasPrefix(value, "sk-ecdsa-")
}

func injectVMMetadataIntoRootFS(ctx context.Context, diskPath string, cfg vmMetadataConfig) error {
	return injectVMMetadataIntoRootFSWith(ctx, diskPath, cfg, runVMCommand, writeVMGuestMetadataFiles)
}

func injectVMMetadataIntoRootFSWith(ctx context.Context, diskPath string, cfg vmMetadataConfig, runner vmCommandRunner, writer vmGuestMetadataWriter) (retErr error) {
	if strings.TrimSpace(diskPath) == "" {
		return fmt.Errorf("VM disk path is required for metadata injection")
	}
	if err := validateVMMetadata(cfg); err != nil {
		return err
	}
	if runner == nil {
		runner = runVMCommand
	}
	if writer == nil {
		writer = writeVMGuestMetadataFiles
	}
	mountRoot, err := os.MkdirTemp("", "yeet-vm-rootfs-*")
	if err != nil {
		return fmt.Errorf("create VM rootfs mount dir: %w", err)
	}
	defer func() {
		retErr = joinVMMetadataDeferredError(retErr, os.RemoveAll(mountRoot), "remove VM rootfs mount dir")
	}()
	if err := runner(ctx, vmRootFSMountCommand(diskPath, mountRoot)); err != nil {
		return fmt.Errorf("mount VM rootfs: %w", err)
	}
	defer func() {
		retErr = joinVMMetadataDeferredError(retErr, runner(ctx, []string{"umount", mountRoot}), "unmount VM rootfs")
	}()
	if err := writer(mountRoot, cfg); err != nil {
		return fmt.Errorf("write VM guest metadata: %w", err)
	}
	if err := ensureVMGuestSSHHostKeys(ctx, mountRoot, cfg.HostKeyDir, runner); err != nil {
		return err
	}
	return nil
}

func joinVMMetadataDeferredError(retErr, err error, label string) error {
	if err == nil {
		return retErr
	}
	err = fmt.Errorf("%s: %w", label, err)
	if retErr == nil {
		return err
	}
	return errors.Join(retErr, err)
}

func ensureVMGuestSSHHostKeys(ctx context.Context, root, keyDir string, runner vmCommandRunner) error {
	if runner == nil {
		runner = runVMCommand
	}
	if err := restoreVMGuestSSHHostKeys(root, keyDir); err != nil {
		return err
	}
	if err := runner(ctx, []string{"chroot", root, "ssh-keygen", "-A"}); err != nil {
		return fmt.Errorf("generate VM SSH host keys: %w", err)
	}
	return persistVMGuestSSHHostKeys(root, keyDir)
}

func restoreVMGuestSSHHostKeys(root, keyDir string) error {
	if strings.TrimSpace(keyDir) == "" {
		return nil
	}
	entries, err := filepath.Glob(filepath.Join(keyDir, "ssh_host_*_key*"))
	if err != nil || len(entries) == 0 {
		return err
	}
	sshDir := filepath.Join(root, "etc", "ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		return err
	}
	for _, src := range entries {
		if err := copyVMGuestSSHHostKey(src, filepath.Join(sshDir, filepath.Base(src))); err != nil {
			return err
		}
	}
	return nil
}

func persistVMGuestSSHHostKeys(root, keyDir string) error {
	if strings.TrimSpace(keyDir) == "" {
		return nil
	}
	entries, err := filepath.Glob(filepath.Join(root, "etc", "ssh", "ssh_host_*_key*"))
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no VM SSH host keys generated")
	}
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		return err
	}
	for _, src := range entries {
		if err := copyVMGuestSSHHostKey(src, filepath.Join(keyDir, filepath.Base(src))); err != nil {
			return err
		}
	}
	return nil
}

func copyVMGuestSSHHostKey(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if strings.HasSuffix(src, ".pub") {
		mode = 0o644
	}
	if err := os.WriteFile(dst, raw, mode); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func vmRootFSMountCommand(diskPath, mountRoot string) []string {
	if strings.HasPrefix(diskPath, "/dev/") {
		return []string{"mount", "-o", "rw", diskPath, mountRoot}
	}
	return []string{"mount", "-o", "loop,rw", diskPath, mountRoot}
}

func writeVMGuestMetadataFiles(root string, cfg vmMetadataConfig) error {
	if err := validateVMMetadata(cfg); err != nil {
		return err
	}
	if err := writeVMGuestBaseFiles(root, cfg); err != nil {
		return err
	}
	if err := writeVMGuestSSHAccess(root, cfg); err != nil {
		return err
	}
	if err := writeVMGuestPrivileges(root, cfg.User); err != nil {
		return err
	}
	if err := writeVMGuestSerialAutologin(root, cfg.User); err != nil {
		return err
	}
	if err := writeVMGuestReadyUnit(root); err != nil {
		return err
	}
	return maskVMGuestSystemdUnit(root, "systemd-networkd-wait-online.service")
}

func writeVMGuestBaseFiles(root string, cfg vmMetadataConfig) error {
	files := []struct {
		rel  string
		data []byte
		mode os.FileMode
	}{
		{rel: "etc/hostname", data: []byte(cfg.Hostname + "\n"), mode: 0o644},
		{rel: "etc/netplan/99-yeet.yaml", data: []byte(renderVMNetworkYAML(cfg.Networks)), mode: 0o644},
	}
	for _, file := range files {
		if err := writeVMGuestFile(root, file.rel, file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func writeVMGuestSSHAccess(root string, cfg vmMetadataConfig) error {
	if err := writeVMGuestAuthorizedKeys(root, "root", cfg.SSHKey, 0, 0, false); err != nil {
		return err
	}
	uid, gid, ok := vmGuestUserIDs(root, cfg.User)
	return writeVMGuestAuthorizedKeys(root, cfg.User, cfg.SSHKey, uid, gid, ok)
}

func writeVMGuestPrivileges(root, user string) error {
	sudoers := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", user)
	if err := writeVMGuestFile(root, "etc/sudoers.d/90-yeet-vm-"+user, []byte(sudoers), 0o440); err != nil {
		return err
	}
	return writeVMGuestFile(root, "etc/sysctl.d/90-yeet-vm.conf", []byte("net.ipv4.ping_group_range = 0 2147483647\n"), 0o644)
}

func writeVMGuestReadyUnit(root string) error {
	if err := writeVMGuestFile(root, "usr/local/lib/yeet-vm/guest-ready", []byte(vmGuestReadyScript), 0o755); err != nil {
		return err
	}
	if err := writeVMGuestFile(root, "etc/systemd/system/yeet-guest-ready.service", []byte(vmGuestReadyService), 0o644); err != nil {
		return err
	}
	if err := writeVMGuestSystemdSymlink(root, "multi-user.target.wants/yeet-guest-ready.service", "../yeet-guest-ready.service"); err != nil {
		return err
	}
	return writeVMGuestSystemdSymlink(root, "multi-user.target.wants/ssh.service", "/usr/lib/systemd/system/ssh.service")
}

const vmGuestReadyService = `[Unit]
Description=yeet guest ready marker
After=network.target ssh.service serial-getty@ttyS0.service
Wants=network.target ssh.service serial-getty@ttyS0.service

[Service]
Type=oneshot
ExecStart=/usr/local/lib/yeet-vm/guest-ready
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`

const vmGuestReadyScript = `#!/bin/sh
echo guest-ready >/dev/ttyS0
command -v logger >/dev/null && logger guest-ready || true
(
	seen=""
	i=0
	while [ "$i" -lt 60 ]; do
		report="$(ip -o -4 addr show scope global 2>/dev/null | awk '{ split($4, a, "/"); print "yeet-ip " $2 " " a[1] }')"
		if [ -n "$report" ] && [ "$report" != "$seen" ]; then
			printf '%s\n' "$report" >/dev/ttyS0
			seen="$report"
		fi
		i=$((i + 1))
		sleep 1
	done
) &
exit 0
`

func writeVMGuestSerialAutologin(root, user string) error {
	data := fmt.Sprintf(`[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin %s --noclear --keep-baud 115200,38400,9600 %%I $TERM
`, user)
	return writeVMGuestFile(root, "etc/systemd/system/serial-getty@ttyS0.service.d/10-yeet-autologin.conf", []byte(data), 0o644)
}

func writeVMGuestSystemdSymlink(root, rel, target string) error {
	path := filepath.Join(root, "etc", "systemd", "system", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(target, path)
}

func maskVMGuestSystemdUnit(root, unit string) error {
	return writeVMGuestSystemdSymlink(root, unit, "/dev/null")
}

func writeVMGuestAuthorizedKeys(root, userName, key string, uid, gid int, chown bool) error {
	dir := filepath.Join(root, "home", userName, ".ssh")
	if userName == "root" {
		dir = filepath.Join(root, "root", ".ssh")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if chown {
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func writeVMGuestFile(root, rel string, data []byte, mode os.FileMode) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func vmGuestUserIDs(root, userName string) (int, int, bool) {
	raw, err := os.ReadFile(filepath.Join(root, "etc/passwd"))
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 || fields[0] != userName {
			continue
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if uidErr != nil || gidErr != nil {
			return 0, 0, false
		}
		return uid, gid, true
	}
	return 0, 0, false
}

func writeVMMetadataFile(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func validateVMMetadata(cfg vmMetadataConfig) error {
	if !vmHostnamePattern.MatchString(cfg.Hostname) || strings.Contains(cfg.Hostname, "..") {
		return fmt.Errorf("invalid VM hostname %q", cfg.Hostname)
	}
	if !vmUserPattern.MatchString(cfg.User) {
		return fmt.Errorf("invalid VM user %q", cfg.User)
	}
	if cfg.SSHKey == "" || strings.ContainsFunc(cfg.SSHKey, isVMMetadataControlChar) {
		return fmt.Errorf("invalid VM SSH key")
	}
	for _, network := range cfg.Networks {
		if err := validateVMGuestNetwork(network); err != nil {
			return err
		}
	}
	return nil
}

func validateVMGuestNetwork(network vmGuestNetwork) error {
	if !vmGuestNetworkNamePattern.MatchString(network.Name) {
		return fmt.Errorf("invalid VM guest network interface %q", network.Name)
	}
	if !network.DHCP {
		if network.Address == "" {
			return fmt.Errorf("VM guest network %s address is required without DHCP", network.Name)
		}
		if _, err := netip.ParsePrefix(network.Address); err != nil {
			return fmt.Errorf("invalid VM guest network %s address %q: %w", network.Name, network.Address, err)
		}
	}
	if network.Gateway != "" {
		addr, err := netip.ParseAddr(network.Gateway)
		if err != nil {
			return fmt.Errorf("invalid VM guest network %s gateway %q: %w", network.Name, network.Gateway, err)
		}
		if !addr.Is4() {
			return fmt.Errorf("VM guest network %s gateway must be IPv4 for gateway4", network.Name)
		}
	}
	for _, dns := range network.Nameservers {
		if _, err := netip.ParseAddr(dns); err != nil {
			return fmt.Errorf("invalid VM guest network %s nameserver %q: %w", network.Name, dns, err)
		}
	}
	return nil
}

func isVMMetadataControlChar(r rune) bool {
	return r < 0x20 || r == 0x7f
}

func renderVMNetworkYAML(networks []vmGuestNetwork) string {
	var b strings.Builder
	b.WriteString("network:\n  version: 2\n  ethernets:\n")
	for _, net := range networks {
		fmt.Fprintf(&b, "    %s:\n", net.Name)
		if net.DHCP {
			b.WriteString("      dhcp4: true\n")
			continue
		}
		fmt.Fprintf(&b, "      addresses: [%s]\n", net.Address)
		if net.Gateway != "" {
			fmt.Fprintf(&b, "      gateway4: %s\n", net.Gateway)
		}
		if nameservers := vmGuestNetworkNameservers(net); len(nameservers) > 0 {
			fmt.Fprintf(&b, "      nameservers:\n")
			fmt.Fprintf(&b, "        addresses: [%s]\n", strings.Join(nameservers, ", "))
		}
	}
	return b.String()
}

func vmGuestNetworkNameservers(network vmGuestNetwork) []string {
	if len(network.Nameservers) > 0 {
		return network.Nameservers
	}
	if network.Mode != "svc" && network.Gateway == "" {
		return nil
	}
	if dns := strings.TrimSpace(os.Getenv("DEFAULT_NS")); dns != "" {
		return splitVMGuestNameservers(dns)
	}
	return []string{"8.8.8.8"}
}

func splitVMGuestNameservers(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}
