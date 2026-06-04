// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteVMMetadataFiles(t *testing.T) {
	root := t.TempDir()
	cfg := vmMetadataConfig{
		Hostname: "devbox",
		User:     "ubuntu",
		SSHKey:   "ssh-ed25519 AAAATEST user@example",
		Networks: []vmGuestNetwork{
			{Name: "eth0", Mode: "svc", Address: "192.168.100.12/24", Gateway: "192.168.100.254"},
			{Name: "eth1", Mode: "lan", DHCP: true},
		},
	}
	if err := writeVMMetadata(root, cfg); err != nil {
		t.Fatalf("writeVMMetadata: %v", err)
	}
	user, err := os.ReadFile(filepath.Join(root, "metadata", "user"))
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if string(user) != "ubuntu\n" {
		t.Fatalf("user = %q, want ubuntu newline", string(user))
	}
	network, err := os.ReadFile(filepath.Join(root, "metadata", "network.yaml"))
	if err != nil {
		t.Fatalf("read network: %v", err)
	}
	for _, want := range []string{"eth0:", "192.168.100.12/24", "gateway4: 192.168.100.254", "nameservers:", "addresses: [8.8.8.8]", "eth1:", "dhcp4: true"} {
		if !strings.Contains(string(network), want) {
			t.Fatalf("network metadata missing %q:\n%s", want, string(network))
		}
	}
}

func TestWriteVMMetadataFileModes(t *testing.T) {
	root := t.TempDir()
	cfg := vmMetadataConfig{
		Hostname: "devbox",
		User:     "ubuntu",
		SSHKey:   "ssh-ed25519 AAAATEST user@example",
		Networks: []vmGuestNetwork{
			{Name: "eth0", Mode: "svc", Address: "192.168.100.12/24"},
		},
	}
	if err := writeVMMetadata(root, cfg); err != nil {
		t.Fatalf("writeVMMetadata: %v", err)
	}
	tests := []struct {
		name string
		mode os.FileMode
	}{
		{name: "hostname", mode: 0o644},
		{name: "user", mode: 0o644},
		{name: "authorized_keys", mode: 0o600},
		{name: "network.yaml", mode: 0o644},
	}
	for _, tt := range tests {
		info, err := os.Stat(filepath.Join(root, "metadata", tt.name))
		if err != nil {
			t.Fatalf("stat %s: %v", tt.name, err)
		}
		if got := info.Mode().Perm(); got != tt.mode {
			t.Fatalf("%s mode = %v, want %v", tt.name, got, tt.mode)
		}
	}
}

func TestVMGuestNetworkNameserversUsesDefaultNSEnvironment(t *testing.T) {
	t.Setenv("DEFAULT_NS", "1.1.1.1, 9.9.9.9\t8.8.8.8")

	got := vmGuestNetworkNameservers(vmGuestNetwork{Mode: "svc"})
	want := []string{"1.1.1.1", "9.9.9.9", "8.8.8.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nameservers = %#v, want %#v", got, want)
	}
}

func TestVMMetadataRejectsInvalidNetwork(t *testing.T) {
	valid := vmMetadataConfig{
		Hostname: "devbox",
		User:     "ubuntu",
		SSHKey:   "ssh-ed25519 AAAATEST user@example",
		Networks: []vmGuestNetwork{
			{Name: "eth0", Mode: "svc", Address: "192.168.100.12/24", Gateway: "192.168.100.254"},
		},
	}
	tests := []struct {
		name string
		edit func(*vmGuestNetwork)
	}{
		{name: "empty interface", edit: func(n *vmGuestNetwork) { n.Name = "" }},
		{name: "interface injection", edit: func(n *vmGuestNetwork) { n.Name = "eth0:\n  injected: true" }},
		{name: "missing address", edit: func(n *vmGuestNetwork) { n.Address = "" }},
		{name: "invalid address", edit: func(n *vmGuestNetwork) { n.Address = "not-a-prefix" }},
		{name: "invalid gateway", edit: func(n *vmGuestNetwork) { n.Gateway = "192.168.100.1: bad" }},
		{name: "ipv6 gateway", edit: func(n *vmGuestNetwork) { n.Gateway = "2001:db8::1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			cfg.Networks = append([]vmGuestNetwork(nil), valid.Networks...)
			tt.edit(&cfg.Networks[0])
			if err := writeVMMetadata(t.TempDir(), cfg); err == nil {
				t.Fatal("expected metadata validation error")
			}
		})
	}
}

func TestVMMetadataRejectsInvalidIdentity(t *testing.T) {
	valid := vmMetadataConfig{
		Hostname: "devbox",
		User:     "ubuntu",
		SSHKey:   "ssh-ed25519 AAAATEST user@example",
		Networks: []vmGuestNetwork{
			{Name: "eth0", Mode: "svc", Address: "192.168.100.12/24"},
		},
	}
	tests := []struct {
		name string
		edit func(*vmMetadataConfig)
	}{
		{name: "empty hostname", edit: func(c *vmMetadataConfig) { c.Hostname = "" }},
		{name: "hostname newline", edit: func(c *vmMetadataConfig) { c.Hostname = "devbox\ninjected" }},
		{name: "hostname control", edit: func(c *vmMetadataConfig) { c.Hostname = "devbox\tbad" }},
		{name: "empty user", edit: func(c *vmMetadataConfig) { c.User = "" }},
		{name: "user newline", edit: func(c *vmMetadataConfig) { c.User = "ubuntu\nroot" }},
		{name: "user control", edit: func(c *vmMetadataConfig) { c.User = "ubuntu\tbad" }},
		{name: "ssh key newline", edit: func(c *vmMetadataConfig) { c.SSHKey = "ssh-ed25519 AAAATEST\nssh-ed25519 OTHER" }},
		{name: "ssh key control", edit: func(c *vmMetadataConfig) { c.SSHKey = "ssh-ed25519 AAAATEST\x00" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.edit(&cfg)
			if err := writeVMMetadata(t.TempDir(), cfg); err == nil {
				t.Fatal("expected metadata validation error")
			}
		})
	}
}

func TestVMSSHKeyFromAuthorizedKeysSkipsOptionsAndComments(t *testing.T) {
	raw := strings.Join([]string{
		"# comment",
		`restrict,from="10.0.0.0/8" ssh-ed25519 AAAATEST user@example`,
		"ssh-rsa AAAAOTHER other@example",
	}, "\n")
	got, ok := vmSSHKeyFromAuthorizedKeys([]byte(raw))
	if !ok {
		t.Fatal("vmSSHKeyFromAuthorizedKeys did not find a key")
	}
	if got != "ssh-ed25519 AAAATEST user@example" {
		t.Fatalf("key = %q", got)
	}
}

func TestDefaultVMSSHKeyUsesEnvironment(t *testing.T) {
	t.Setenv("YEET_VM_SSH_KEY", "ssh-ed25519 AAAAENV env@example")

	got, err := defaultVMSSHKey()
	if err != nil {
		t.Fatalf("defaultVMSSHKey: %v", err)
	}
	if got != "ssh-ed25519 AAAAENV env@example" {
		t.Fatalf("key = %q, want env key", got)
	}
}

func TestDefaultVMSSHKeyUsesConfiguredAuthorizedKeyPath(t *testing.T) {
	oldPaths := defaultVMSSHAuthorizedKeyPaths
	defer func() { defaultVMSSHAuthorizedKeyPaths = oldPaths }()

	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, []byte("ssh-ed25519 AAAAFILE file@example\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	t.Setenv("YEET_VM_SSH_KEY", "")
	defaultVMSSHAuthorizedKeyPaths = func() []string { return []string{path} }

	got, err := defaultVMSSHKey()
	if err != nil {
		t.Fatalf("defaultVMSSHKey: %v", err)
	}
	if got != "ssh-ed25519 AAAAFILE file@example" {
		t.Fatalf("key = %q, want file key", got)
	}
}

func TestDefaultVMSSHKeyReportsInvalidAuthorizedKeys(t *testing.T) {
	oldPaths := defaultVMSSHAuthorizedKeyPaths
	defer func() { defaultVMSSHAuthorizedKeyPaths = oldPaths }()

	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, []byte("not-a-key\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	t.Setenv("YEET_VM_SSH_KEY", "")
	defaultVMSSHAuthorizedKeyPaths = func() []string { return []string{path} }

	_, err := defaultVMSSHKey()
	if err == nil || !strings.Contains(err.Error(), "no SSH public key found") {
		t.Fatalf("defaultVMSSHKey error = %v, want invalid file error", err)
	}
}

func TestWriteVMGuestMetadataFiles(t *testing.T) {
	root := t.TempDir()
	stubVMGuestChown(t)
	cfg := validVMMetadataConfig()
	if err := writeVMGuestMetadataFiles(root, cfg); err != nil {
		t.Fatalf("writeVMGuestMetadataFiles: %v", err)
	}
	assertFileContains(t, filepath.Join(root, "etc", "hostname"), "devbox")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "[Match]")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "Name=eth0")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network"), "Address=192.168.100.12/24")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth1.network"), "DHCP=ipv4")
	assertFileContains(t, filepath.Join(root, "home", "ubuntu", ".ssh", "authorized_keys"), "ssh-ed25519 AAAATEST")
	assertFileContains(t, filepath.Join(root, "root", ".ssh", "authorized_keys"), "ssh-ed25519 AAAATEST")
	assertFileContains(t, filepath.Join(root, "etc", "sudoers.d", "90-yeet-vm-ubuntu"), "ubuntu ALL=(ALL) NOPASSWD:ALL")
	assertFileContains(t, filepath.Join(root, "etc", "sysctl.d", "90-yeet-vm.conf"), "net.ipv4.ping_group_range = 0 2147483647")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-sshd.service"), "ExecStart=/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-sshd.service"), "Restart=always")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "yeet-ready")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "After=yeet-sshd.service")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "Wants=yeet-sshd.service")
	assertFileNotContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "network-online.target")
	assertFileNotContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "ssh.service")
	assertFileNotContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "serial-getty")
	assertFileContains(t, filepath.Join(root, "usr", "local", "lib", "yeet-vm", "guest-ready"), "yeet-ready")
	assertFileContains(t, filepath.Join(root, "usr", "local", "lib", "yeet-vm", "guest-ready"), "ip -o -4 addr show scope global")
	assertFileContains(t, filepath.Join(root, "usr", "local", "lib", "yeet-vm", "guest-ready"), "ss -H -ltn")
	assertFileContains(t, filepath.Join(root, "usr", "local", "lib", "yeet-vm", "guest-ready"), "sport = :22")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-grow-root.service"), "After=yeet-guest-ready.service")
	assertFileContains(t, filepath.Join(root, "usr", "local", "lib", "yeet-vm", "grow-root"), "resize2fs")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "serial-getty@ttyS0.service.d", "10-yeet-autologin.conf"), "--autologin ubuntu")
	assertFileMode(t, filepath.Join(root, "etc", "sudoers.d", "90-yeet-vm-ubuntu"), 0o440)
	if _, err := os.Lstat(filepath.Join(root, "etc", "systemd", "system", "multi-user.target.wants", "yeet-guest-ready.service")); err != nil {
		t.Fatalf("guest-ready enable symlink missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc", "systemd", "system", "multi-user.target.wants", "yeet-grow-root.service")); err != nil {
		t.Fatalf("grow-root enable symlink missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc", "systemd", "system", "multi-user.target.wants", "yeet-sshd.service")); err != nil {
		t.Fatalf("yeet-sshd enable symlink missing: %v", err)
	}
	for _, unit := range []string{"ssh.service", "ssh.socket"} {
		target, err := os.Readlink(filepath.Join(root, "etc", "systemd", "system", unit))
		if err != nil || target != "/dev/null" {
			t.Fatalf("%s mask = %q, %v; want /dev/null", unit, target, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(root, "etc", "systemd", "system", "systemd-networkd-wait-online.service")); err != nil || target != "/dev/null" {
		t.Fatalf("systemd-networkd-wait-online mask = %q, %v; want /dev/null", target, err)
	}
}

func TestWriteVMGuestMetadataFilesUsesLegacyGuestConfigWithoutFastBoot(t *testing.T) {
	root := t.TempDir()
	stubVMGuestChown(t)
	cfg := validVMMetadataConfig()
	cfg.FastBoot = false
	if err := writeVMGuestMetadataFiles(root, cfg); err != nil {
		t.Fatalf("writeVMGuestMetadataFiles: %v", err)
	}

	assertFileContains(t, filepath.Join(root, "etc", "netplan", "99-yeet.yaml"), "192.168.100.12/24")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "network-online.target")
	assertFileContains(t, filepath.Join(root, "etc", "systemd", "system", "yeet-guest-ready.service"), "ssh.service")
	if _, err := os.Lstat(filepath.Join(root, "etc", "systemd", "system", "multi-user.target.wants", "ssh.service")); err != nil {
		t.Fatalf("ssh.service enable symlink missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc", "systemd", "system", "yeet-sshd.service")); !os.IsNotExist(err) {
		t.Fatalf("yeet-sshd.service exists for legacy metadata: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc", "systemd", "network", "10-yeet-eth0.network")); !os.IsNotExist(err) {
		t.Fatalf("networkd file exists for legacy metadata: %v", err)
	}
}

func TestWriteVMGuestMetadataFilesCreatesMissingLoginUser(t *testing.T) {
	root := t.TempDir()
	stubVMGuestChown(t)
	seedVMGuestAccountFiles(t, root)

	cfg := validVMMetadataConfig()
	if err := writeVMGuestMetadataFiles(root, cfg); err != nil {
		t.Fatalf("writeVMGuestMetadataFiles: %v", err)
	}

	assertFileContains(t, filepath.Join(root, "etc", "passwd"), "ubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash")
	assertFileContains(t, filepath.Join(root, "etc", "group"), "ubuntu:x:1000:")
	assertFileContains(t, filepath.Join(root, "etc", "shadow"), "ubuntu:*:")
	assertFileContains(t, filepath.Join(root, "etc", "gshadow"), "ubuntu:!::")
	assertFileContains(t, filepath.Join(root, "home", "ubuntu", ".ssh", "authorized_keys"), "ssh-ed25519 AAAATEST")
}

func TestInjectVMMetadataIntoRootFSMountsAndUnmounts(t *testing.T) {
	var commands [][]string
	var wroteRoot string
	cfg := validVMMetadataConfig()
	err := injectVMMetadataIntoRootFSWith(context.Background(), "/srv/devbox/rootfs.raw", cfg, func(_ context.Context, cmd []string) error {
		commands = append(commands, append([]string(nil), cmd...))
		return nil
	}, func(root string, got vmMetadataConfig) error {
		wroteRoot = root
		if !reflect.DeepEqual(got, cfg) {
			t.Fatalf("metadata cfg = %#v, want %#v", got, cfg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("injectVMMetadataIntoRootFSWith: %v", err)
	}
	if len(commands) != 3 {
		t.Fatalf("commands = %#v, want mount, host-key generation, and umount", commands)
	}
	if commands[0][0] != "mount" || !reflect.DeepEqual(commands[0][1:4], []string{"-o", "loop,rw", "/srv/devbox/rootfs.raw"}) {
		t.Fatalf("mount command = %#v", commands[0])
	}
	if !reflect.DeepEqual(commands[1], []string{"chroot", wroteRoot, "ssh-keygen", "-A"}) {
		t.Fatalf("host key command = %#v, wrote root %q", commands[1], wroteRoot)
	}
	if commands[2][0] != "umount" || commands[2][1] != wroteRoot {
		t.Fatalf("umount command = %#v, wrote root %q", commands[2], wroteRoot)
	}
}

func TestInjectVMMetadataIntoRootFSRejectsMissingDiskPath(t *testing.T) {
	err := injectVMMetadataIntoRootFSWith(context.Background(), " ", validVMMetadataConfig(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "disk path is required") {
		t.Fatalf("error = %v, want missing disk path", err)
	}
}

func TestInjectVMMetadataIntoRootFSReportsMountFailure(t *testing.T) {
	err := injectVMMetadataIntoRootFSWith(context.Background(), "/srv/devbox/rootfs.raw", validVMMetadataConfig(), func(context.Context, []string) error {
		return errors.New("mount failed")
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "mount VM rootfs") {
		t.Fatalf("error = %v, want mount failure", err)
	}
}

func TestInjectVMMetadataIntoRootFSReportsWriterAndUnmountFailures(t *testing.T) {
	var commands int
	err := injectVMMetadataIntoRootFSWith(context.Background(), "/srv/devbox/rootfs.raw", validVMMetadataConfig(), func(_ context.Context, cmd []string) error {
		commands++
		if len(cmd) > 0 && cmd[0] == "umount" {
			return errors.New("umount failed")
		}
		return nil
	}, func(string, vmMetadataConfig) error {
		return errors.New("writer failed")
	})
	if err == nil || !strings.Contains(err.Error(), "write VM guest metadata") || !strings.Contains(err.Error(), "unmount VM rootfs") {
		t.Fatalf("error = %v, want writer and unmount failures", err)
	}
	if commands != 2 {
		t.Fatalf("commands = %d, want mount and umount", commands)
	}
}

func TestEnsureVMGuestSSHHostKeysRestoresPersistedKeys(t *testing.T) {
	root := t.TempDir()
	keyDir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatalf("mkdir key dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "ssh_host_ed25519_key"), []byte("persisted-private"), 0o600); err != nil {
		t.Fatalf("write persisted private key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "ssh_host_ed25519_key.pub"), []byte("persisted-public"), 0o644); err != nil {
		t.Fatalf("write persisted public key: %v", err)
	}

	var commands [][]string
	err := ensureVMGuestSSHHostKeys(context.Background(), root, keyDir, func(_ context.Context, cmd []string) error {
		commands = append(commands, append([]string(nil), cmd...))
		return nil
	})
	if err != nil {
		t.Fatalf("ensureVMGuestSSHHostKeys: %v", err)
	}
	if !reflect.DeepEqual(commands, [][]string{{"chroot", root, "ssh-keygen", "-A"}}) {
		t.Fatalf("commands = %#v", commands)
	}
	assertFileContains(t, filepath.Join(root, "etc", "ssh", "ssh_host_ed25519_key"), "persisted-private")
	assertFileContains(t, filepath.Join(root, "etc", "ssh", "ssh_host_ed25519_key.pub"), "persisted-public")
}

func TestEnsureVMGuestSSHHostKeysPersistsGeneratedKeys(t *testing.T) {
	root := t.TempDir()
	keyDir := filepath.Join(t.TempDir(), "keys")

	err := ensureVMGuestSSHHostKeys(context.Background(), root, keyDir, func(_ context.Context, cmd []string) error {
		if !reflect.DeepEqual(cmd, []string{"chroot", root, "ssh-keygen", "-A"}) {
			t.Fatalf("host key command = %#v", cmd)
		}
		sshDir := filepath.Join(root, "etc", "ssh")
		if err := os.MkdirAll(sshDir, 0o755); err != nil {
			t.Fatalf("mkdir ssh dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sshDir, "ssh_host_ed25519_key"), []byte("generated-private"), 0o600); err != nil {
			t.Fatalf("write generated private key: %v", err)
		}
		return os.WriteFile(filepath.Join(sshDir, "ssh_host_ed25519_key.pub"), []byte("generated-public"), 0o644)
	})
	if err != nil {
		t.Fatalf("ensureVMGuestSSHHostKeys: %v", err)
	}
	assertFileContains(t, filepath.Join(keyDir, "ssh_host_ed25519_key"), "generated-private")
	assertFileContains(t, filepath.Join(keyDir, "ssh_host_ed25519_key.pub"), "generated-public")
}

func TestVMRootFSMountCommandUsesPlainBlockMountForDevices(t *testing.T) {
	got := vmRootFSMountCommand("/dev/zvol/flash/yeet/vms/devbox/root", "/mnt/root")
	want := []string{"mount", "-o", "rw", "/dev/zvol/flash/yeet/vms/devbox/root", "/mnt/root"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mount command = %#v, want %#v", got, want)
	}
}

func validVMMetadataConfig() vmMetadataConfig {
	return vmMetadataConfig{
		Hostname: "devbox",
		User:     "ubuntu",
		SSHKey:   "ssh-ed25519 AAAATEST user@example",
		FastBoot: true,
		Networks: []vmGuestNetwork{
			{Name: "eth0", Mode: "svc", Address: "192.168.100.12/24", Gateway: "192.168.100.254"},
			{Name: "eth1", Mode: "lan", DHCP: true},
		},
	}
}

func stubVMGuestChown(t *testing.T) {
	t.Helper()
	old := vmGuestChown
	t.Cleanup(func() { vmGuestChown = old })
	vmGuestChown = func(string, int, int) error {
		return nil
	}
}

func seedVMGuestAccountFiles(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"etc/passwd":  "root:x:0:0:root:/root:/bin/bash\n",
		"etc/group":   "root:x:0:\nsudo:x:27:\n",
		"etc/shadow":  "root:*:1:0:99999:7:::\n",
		"etc/gshadow": "root:*::\nsudo:*::\n",
	}
	for rel, data := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		mode := os.FileMode(0o644)
		if strings.Contains(filepath.Base(path), "shadow") {
			mode = 0o600
		}
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %v, want %v", path, got, want)
	}
}
