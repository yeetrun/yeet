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
var vmGuestSearchDomainPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var vmHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var vmUserPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
var vmGuestChown = os.Chown

type vmMetadataConfig struct {
	Hostname       string
	User           string
	SSHKey         string
	Networks       []vmGuestNetwork
	FastBoot       bool
	MetadataDriver string

	HostKeyDir string
}

type vmGuestNetwork struct {
	Name            string
	Mode            string
	Address         string
	Gateway         string
	DHCP            bool
	Nameservers     []string
	SearchDomains   []string
	DNSDefaultRoute *bool
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
	if err := ensureVMGuestSSHHostKeys(ctx, mountRoot, cfg, runner); err != nil {
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

func ensureVMGuestSSHHostKeys(ctx context.Context, root string, cfg vmMetadataConfig, runner vmCommandRunner) error {
	if strings.TrimSpace(cfg.MetadataDriver) == "nixos" {
		return ensureVMGuestNixOSSSHHostKeys(ctx, root, cfg.HostKeyDir, runner)
	}
	if runner == nil {
		runner = runVMCommand
	}
	if err := restoreVMGuestSSHHostKeys(root, cfg.HostKeyDir); err != nil {
		return err
	}
	if err := runner(ctx, []string{"chroot", root, "ssh-keygen", "-A"}); err != nil {
		return fmt.Errorf("generate VM SSH host keys: %w", err)
	}
	return persistVMGuestSSHHostKeys(root, cfg.HostKeyDir)
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
	switch strings.TrimSpace(cfg.MetadataDriver) {
	case "", "ubuntu":
		return writeVMGuestUbuntuMetadataFiles(root, cfg)
	case "nixos":
		return writeVMGuestNixOSMetadataFiles(root, cfg)
	default:
		return fmt.Errorf("unsupported VM metadata driver %q", cfg.MetadataDriver)
	}
}

func writeVMGuestUbuntuMetadataFiles(root string, cfg vmMetadataConfig) error {
	if err := writeVMGuestBaseFiles(root, cfg); err != nil {
		return err
	}
	if err := writeVMGuestSSHAccess(root, cfg); err != nil {
		return err
	}
	if err := writeVMGuestShellDefaults(root, cfg); err != nil {
		return err
	}
	if err := writeVMGuestPrivileges(root, cfg.User); err != nil {
		return err
	}
	if err := writeVMGuestSerialAutologin(root, cfg.User); err != nil {
		return err
	}
	if err := writeVMGuestReadyUnit(root, cfg.FastBoot); err != nil {
		return err
	}
	if err := writeVMGuestGrowRootUnit(root); err != nil {
		return err
	}
	return maskVMGuestSystemdUnit(root, "systemd-networkd-wait-online.service")
}

func writeVMGuestBaseFiles(root string, cfg vmMetadataConfig) error {
	if !cfg.FastBoot {
		return writeVMGuestLegacyBaseFiles(root, cfg)
	}
	return writeVMGuestFastBaseFiles(root, cfg)
}

func writeVMGuestLegacyBaseFiles(root string, cfg vmMetadataConfig) error {
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

func writeVMGuestFastBaseFiles(root string, cfg vmMetadataConfig) error {
	if err := writeVMGuestFile(root, "etc/hostname", []byte(cfg.Hostname+"\n"), 0o644); err != nil {
		return err
	}
	for _, network := range cfg.Networks {
		rel := filepath.Join("etc", "systemd", "network", "10-yeet-"+network.Name+".network")
		if err := writeVMGuestFile(root, rel, []byte(renderVMNetworkdUnit(network)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func renderVMNetworkdUnit(network vmGuestNetwork) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Match]\nName=%s\n\n[Network]\n", network.Name)
	if network.DHCP {
		b.WriteString("DHCP=ipv4\n")
	} else {
		fmt.Fprintf(&b, "Address=%s\n", network.Address)
		if network.Gateway != "" {
			fmt.Fprintf(&b, "Gateway=%s\n", network.Gateway)
		}
	}
	for _, ns := range vmGuestNetworkNameservers(network) {
		fmt.Fprintf(&b, "DNS=%s\n", ns)
	}
	if domains := vmGuestNetworkSearchDomains(network); len(domains) > 0 {
		fmt.Fprintf(&b, "Domains=%s\n", strings.Join(domains, " "))
	}
	if network.DNSDefaultRoute != nil {
		fmt.Fprintf(&b, "DNSDefaultRoute=%s\n", networkdBool(*network.DNSDefaultRoute))
	}
	if network.Mode == "iso" {
		b.WriteString("LinkLocalAddressing=no\nIPv6AcceptRA=no\n")
	}
	return b.String()
}

func writeVMGuestSSHAccess(root string, cfg vmMetadataConfig) error {
	if err := ensureVMGuestLoginUser(root, cfg.User); err != nil {
		return err
	}
	if err := writeVMGuestAuthorizedKeys(root, "root", cfg.SSHKey, 0, 0, false); err != nil {
		return err
	}
	uid, gid, ok := vmGuestUserIDs(root, cfg.User)
	return writeVMGuestAuthorizedKeys(root, cfg.User, cfg.SSHKey, uid, gid, ok)
}

const (
	vmGuestProfileBegin = "# >>> yeet VM profile >>>"
	vmGuestProfileEnd   = "# <<< yeet VM profile <<<"
	vmGuestBashRCBegin  = "# >>> yeet VM defaults >>>"
	vmGuestBashRCEnd    = "# <<< yeet VM defaults <<<"
)

func writeVMGuestShellDefaults(root string, cfg vmMetadataConfig) error {
	home := filepath.Join(root, "home", cfg.User)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	if err := seedVMGuestHomeFromSkel(root, cfg.User); err != nil {
		return err
	}
	if err := writeVMGuestManagedShellFile(filepath.Join(home, ".profile"), vmGuestProfileBegin, vmGuestProfileEnd, vmGuestProfileBlock()); err != nil {
		return err
	}
	if err := writeVMGuestManagedShellFile(filepath.Join(home, ".bashrc"), vmGuestBashRCBegin, vmGuestBashRCEnd, vmGuestBashRCBlock(cfg.Hostname)); err != nil {
		return err
	}
	return chownVMGuestShellDefaults(root, cfg.User)
}

func seedVMGuestHomeFromSkel(root, user string) error {
	home := filepath.Join(root, "home", user)
	for _, name := range []string{".profile", ".bashrc", ".bash_logout", ".bash_aliases"} {
		if err := seedVMGuestHomeFileFromSkel(root, home, name); err != nil {
			return err
		}
	}
	return nil
}

func seedVMGuestHomeFileFromSkel(root, home, name string) error {
	sourcePath := filepath.Join(root, "etc", "skel", name)
	source, err := os.ReadFile(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	destPath := filepath.Join(home, name)
	dest, err := os.ReadFile(destPath)
	if err == nil && !vmGuestShellFileShouldSeed(name, string(dest)) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := os.WriteFile(destPath, source, mode); err != nil {
		return err
	}
	return os.Chmod(destPath, mode)
}

func vmGuestShellFileShouldSeed(name, raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	switch name {
	case ".profile":
		return vmGuestShellFileIsOnlyManaged(raw, vmGuestProfileBegin, vmGuestProfileEnd)
	case ".bashrc":
		return vmGuestShellFileIsOnlyManaged(raw, vmGuestBashRCBegin, vmGuestBashRCEnd)
	default:
		return false
	}
}

func vmGuestShellFileIsOnlyManaged(raw, begin, end string) bool {
	start := strings.Index(raw, begin)
	if start == -1 {
		return false
	}
	afterStart := raw[start:]
	endOffset := strings.Index(afterStart, end)
	if endOffset == -1 {
		return false
	}
	afterEnd := start + endOffset + len(end)
	return strings.TrimSpace(raw[:start]) == "" && strings.TrimSpace(raw[afterEnd:]) == ""
}

func writeVMGuestManagedShellFile(path, begin, end, block string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data := replaceVMGuestManagedBlock(string(raw), begin, end, block)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}

func replaceVMGuestManagedBlock(raw, begin, end, block string) string {
	managed := begin + "\n" + strings.TrimRight(block, "\n") + "\n" + end + "\n"
	start := strings.Index(raw, begin)
	if start == -1 {
		return appendVMGuestManagedBlock(raw, managed)
	}
	afterStart := raw[start:]
	endOffset := strings.Index(afterStart, end)
	if endOffset == -1 {
		return appendVMGuestManagedBlock(raw, managed)
	}
	after := afterStart[endOffset+len(end):]
	after = strings.TrimPrefix(after, "\n")
	return raw[:start] + managed + after
}

func appendVMGuestManagedBlock(raw, managed string) string {
	if strings.TrimSpace(raw) == "" {
		return managed
	}
	if !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	return raw + managed
}

func chownVMGuestShellDefaults(root, user string) error {
	uid, gid, ok := vmGuestUserIDs(root, user)
	if !ok {
		return nil
	}
	for _, name := range []string{".profile", ".bashrc"} {
		if err := vmGuestChown(filepath.Join(root, "home", user, name), uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func vmGuestProfileBlock() string {
	return `[ -n "${BASH_VERSION:-}" ] && [ -f "$HOME/.bashrc" ] && [ -z "${YEET_VM_BASHRC_SOURCED:-}" ] && . "$HOME/.bashrc"
`
}

func vmGuestBashRCBlock(hostname string) string {
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
if [ "${YEET_VM_BASHRC_SOURCED:-}" = 1 ]; then
	return
fi
export YEET_VM_BASHRC_SOURCED=1

if [ -z "${XDG_RUNTIME_DIR:-}" ] && [ -d "/run/user/$(id -u)" ]; then
	export XDG_RUNTIME_DIR="/run/user/$(id -u)"
fi

case "$-" in
	*i*) ;;
	*) return ;;
esac

HISTCONTROL=ignoreboth
shopt -s histappend
HISTSIZE="${HISTSIZE:-1000}"
HISTFILESIZE="${HISTFILESIZE:-2000}"
shopt -s checkwinsize

[ -x /usr/bin/lesspipe ] && eval "$(SHELL=/bin/sh lesspipe)"

if [ -z "${debian_chroot:-}" ] && [ -r /etc/debian_chroot ]; then
	debian_chroot="$(cat /etc/debian_chroot)"
fi

if command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
	PS1='${debian_chroot:+($debian_chroot)}\[\033[01;32m\]\u@\h\[\033[00m\]:\[\033[01;34m\]\w\[\033[00m\]\$ '
else
	PS1='${debian_chroot:+($debian_chroot)}\u@\h:\w\$ '
fi

case "${TERM:-}" in
xterm*|rxvt*|ghostty*|*-ghostty)
	PS1="\[\e]0;${debian_chroot:+($debian_chroot)}\u@\h: \w\a\]$PS1"
	;;
esac

if command -v dircolors >/dev/null 2>&1; then
	if [ -r "$HOME/.dircolors" ]; then
		eval "$(dircolors -b "$HOME/.dircolors")" 2>/dev/null || true
	else
		eval "$(dircolors -b)" 2>/dev/null || true
	fi
	alias ls='ls --color=auto'
	alias grep='grep --color=auto'
	alias fgrep='fgrep --color=auto'
	alias egrep='egrep --color=auto'
fi

alias ll='ls -alF'
alias la='ls -A'
alias l='ls -CF'

if [ -f "$HOME/.bash_aliases" ]; then
	. "$HOME/.bash_aliases"
fi

if ! shopt -oq posix; then
	if [ -f /usr/share/bash-completion/bash_completion ]; then
		. /usr/share/bash-completion/bash_completion
	elif [ -f /etc/bash_completion ]; then
		. /etc/bash_completion
	fi
fi

printf '\n%%s\n' 'Welcome to yeet VM %s.'
printf '%%s\n' 'The disk is persistent. You have passwordless sudo.'
`, hostname)
}

func ensureVMGuestLoginUser(root, userName string) error {
	uid, gid, ok := vmGuestUserIDs(root, userName)
	if !ok {
		var err error
		uid, gid, err = appendVMGuestLoginUser(root, userName)
		if err != nil {
			return err
		}
	}
	home := filepath.Join(root, "home", userName)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	if err := vmGuestChown(home, uid, gid); err != nil {
		return err
	}
	return os.Chmod(home, 0o755)
}

func appendVMGuestLoginUser(root, userName string) (int, int, error) {
	uid, gid, err := nextVMGuestUserIDs(root)
	if err != nil {
		return 0, 0, err
	}
	if err := appendVMGuestAccountFileLine(root, "etc/passwd", fmt.Sprintf("%s:x:%d:%d:Ubuntu:/home/%s:/bin/bash", userName, uid, gid, userName), 0o644, userName); err != nil {
		return 0, 0, err
	}
	if err := appendVMGuestAccountFileLine(root, "etc/group", fmt.Sprintf("%s:x:%d:", userName, gid), 0o644, userName); err != nil {
		return 0, 0, err
	}
	if err := appendVMGuestAccountFileLine(root, "etc/shadow", fmt.Sprintf("%s:*:1:0:99999:7:::", userName), 0o600, userName); err != nil {
		return 0, 0, err
	}
	if err := appendVMGuestAccountFileLine(root, "etc/gshadow", fmt.Sprintf("%s:!::", userName), 0o600, userName); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func appendVMGuestAccountFileLine(root, rel, line string, mode os.FileMode, key string) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if vmGuestColonFileHasKey(raw, key) {
		return nil
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	raw = append(raw, line...)
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func vmGuestColonFileHasKey(raw []byte, key string) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.SplitN(line, ":", 2)
		if len(fields) > 0 && fields[0] == key {
			return true
		}
	}
	return false
}

func nextVMGuestUserIDs(root string) (int, int, error) {
	usedUIDs, err := vmGuestUsedIDs(filepath.Join(root, "etc", "passwd"), 2)
	if err != nil {
		return 0, 0, err
	}
	usedGIDs, err := vmGuestUsedIDs(filepath.Join(root, "etc", "group"), 2)
	if err != nil {
		return 0, 0, err
	}
	for id := 1000; id < 60000; id++ {
		if !usedUIDs[id] && !usedGIDs[id] {
			return id, id, nil
		}
	}
	return 0, 0, fmt.Errorf("no available VM guest user ID")
}

func vmGuestUsedIDs(path string, field int) (map[int]bool, error) {
	used := map[int]bool{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return used, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) <= field {
			continue
		}
		id, err := strconv.Atoi(fields[field])
		if err == nil {
			used[id] = true
		}
	}
	return used, nil
}

func writeVMGuestPrivileges(root, user string) error {
	sudoers := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", user)
	if err := writeVMGuestFile(root, "etc/sudoers.d/90-yeet-vm-"+user, []byte(sudoers), 0o440); err != nil {
		return err
	}
	return writeVMGuestFile(root, "etc/sysctl.d/90-yeet-vm.conf", []byte("net.ipv4.ping_group_range = 0 2147483647\n"), 0o644)
}

func writeVMGuestReadyUnit(root string, fastBoot bool) error {
	if err := writeVMGuestFile(root, "usr/local/lib/yeet-vm/guest-ready", []byte(vmGuestReadyScript), 0o755); err != nil {
		return err
	}
	if !fastBoot {
		if err := writeVMGuestFile(root, "etc/systemd/system/yeet-guest-ready.service", []byte(vmGuestLegacyReadyService), 0o644); err != nil {
			return err
		}
		if err := writeVMGuestSystemdSymlink(root, "multi-user.target.wants/yeet-guest-ready.service", "../yeet-guest-ready.service"); err != nil {
			return err
		}
		return writeVMGuestSystemdSymlink(root, "multi-user.target.wants/ssh.service", "/usr/lib/systemd/system/ssh.service")
	}
	if err := writeVMGuestFile(root, "etc/systemd/system/yeet-sshd.service", []byte(vmGuestSSHDService), 0o644); err != nil {
		return err
	}
	if err := writeVMGuestFile(root, "etc/systemd/system/yeet-guest-ready.service", []byte(vmGuestReadyService), 0o644); err != nil {
		return err
	}
	if err := writeVMGuestSystemdSymlink(root, "multi-user.target.wants/yeet-sshd.service", "../yeet-sshd.service"); err != nil {
		return err
	}
	if err := writeVMGuestSystemdSymlink(root, "multi-user.target.wants/yeet-guest-ready.service", "../yeet-guest-ready.service"); err != nil {
		return err
	}
	if err := maskVMGuestSystemdUnit(root, "ssh.service"); err != nil {
		return err
	}
	return maskVMGuestSystemdUnit(root, "ssh.socket")
}

func writeVMGuestGrowRootUnit(root string) error {
	if err := writeVMGuestFile(root, "usr/local/lib/yeet-vm/grow-root", []byte(vmGuestGrowRootScript), 0o755); err != nil {
		return err
	}
	if err := writeVMGuestFile(root, "etc/systemd/system/yeet-grow-root.service", []byte(vmGuestGrowRootService), 0o644); err != nil {
		return err
	}
	return writeVMGuestSystemdSymlink(root, "multi-user.target.wants/yeet-grow-root.service", "../yeet-grow-root.service")
}

const vmGuestReadyService = `[Unit]
Description=yeet-ready guest marker
After=yeet-sshd.service
Wants=yeet-sshd.service

[Service]
Type=oneshot
ExecStart=/usr/local/lib/yeet-vm/guest-ready
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`

const vmGuestLegacyReadyService = `[Unit]
Description=yeet-ready guest marker
After=network-online.target ssh.service serial-getty@ttyS0.service
Wants=network-online.target ssh.service serial-getty@ttyS0.service

[Service]
Type=oneshot
ExecStart=/usr/local/lib/yeet-vm/guest-ready
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`

const vmGuestSSHDService = `[Unit]
Description=yeet early SSH daemon
DefaultDependencies=no
After=local-fs.target systemd-sysusers.service network.target
Before=multi-user.target
Wants=network.target
ConditionPathExists=/usr/sbin/sshd

[Service]
Type=exec
RuntimeDirectory=sshd
ExecStartPre=/usr/sbin/sshd -t
ExecStart=/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`

const vmGuestReadyScript = `#!/bin/sh
vm_guest_ssh_ready() {
	ss -H -ltn 'sport = :22' 2>/dev/null | grep -q .
}

emit_yeet_ready() {
	report="$1"
	printf '%s\n' "$report" | while read -r iface ip; do
		printf 'yeet-ip %s %s\n' "$iface" "$ip" >/dev/ttyS0
	done
	first="$(printf '%s\n' "$report" | head -n 1)"
	set -- $first
	printf 'yeet-ready %s %s\n' "$1" "$2" >/dev/ttyS0
	command -v logger >/dev/null && logger "yeet-ready $1 $2" || true
	exit 0
}

for _ in $(seq 1 100); do
	report="$(ip -o -4 addr show scope global 2>/dev/null | awk '{ split($4, a, "/"); print $2 " " a[1] }')"
	if [ -n "$report" ] && vm_guest_ssh_ready; then
		emit_yeet_ready "$report"
	fi
	sleep 0.05
done
i=0
while [ "$i" -lt 55 ]; do
	report="$(ip -o -4 addr show scope global 2>/dev/null | awk '{ split($4, a, "/"); print $2 " " a[1] }')"
	if [ -n "$report" ] && vm_guest_ssh_ready; then
		emit_yeet_ready "$report"
	fi
	i=$((i + 1))
	sleep 1
done
echo yeet-ready-timeout >/dev/ttyS0
exit 1
`

const vmGuestGrowRootService = `[Unit]
Description=yeet grow root filesystem
After=yeet-guest-ready.service
Wants=yeet-guest-ready.service

[Service]
Type=simple
ExecStart=/usr/local/lib/yeet-vm/grow-root
Nice=10
IOSchedulingClass=idle

[Install]
WantedBy=multi-user.target
`

const vmGuestGrowRootScript = `#!/bin/sh
root_source="$(findmnt -n -o SOURCE / 2>/dev/null || true)"
root_fstype="$(findmnt -n -o FSTYPE / 2>/dev/null || true)"

case "$root_source" in
	/dev/*) ;;
	*) exit 0 ;;
esac

if [ "$root_fstype" != "ext4" ]; then
	exit 0
fi

if ! command -v resize2fs >/dev/null 2>&1; then
	exit 0
fi

if resize2fs "$root_source"; then
	command -v logger >/dev/null 2>&1 && logger "yeet-grow-root complete $root_source" || true
	exit 0
fi

command -v logger >/dev/null 2>&1 && logger "yeet-grow-root failed $root_source" || true
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
		if err := vmGuestChown(dir, uid, gid); err != nil {
			return err
		}
		if err := vmGuestChown(path, uid, gid); err != nil {
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
	if err := validateVMGuestNetworkAddress(network); err != nil {
		return err
	}
	if err := validateVMGuestNetworkGateway(network); err != nil {
		return err
	}
	if err := validateVMGuestNetworkNameservers(network); err != nil {
		return err
	}
	return validateVMGuestNetworkSearchDomains(network)
}

func validateVMGuestNetworkAddress(network vmGuestNetwork) error {
	if !network.DHCP {
		if network.Address == "" {
			return fmt.Errorf("VM guest network %s address is required without DHCP", network.Name)
		}
		if _, err := netip.ParsePrefix(network.Address); err != nil {
			return fmt.Errorf("invalid VM guest network %s address %q: %w", network.Name, network.Address, err)
		}
	}
	return nil
}

func validateVMGuestNetworkGateway(network vmGuestNetwork) error {
	if network.Gateway != "" {
		addr, err := netip.ParseAddr(network.Gateway)
		if err != nil {
			return fmt.Errorf("invalid VM guest network %s gateway %q: %w", network.Name, network.Gateway, err)
		}
		if !addr.Is4() {
			return fmt.Errorf("VM guest network %s gateway must be IPv4 for gateway4", network.Name)
		}
	}
	return nil
}

func validateVMGuestNetworkNameservers(network vmGuestNetwork) error {
	for _, dns := range network.Nameservers {
		if _, err := netip.ParseAddr(dns); err != nil {
			return fmt.Errorf("invalid VM guest network %s nameserver %q: %w", network.Name, dns, err)
		}
	}
	return nil
}

func validateVMGuestNetworkSearchDomains(network vmGuestNetwork) error {
	for _, domain := range network.SearchDomains {
		if !vmGuestSearchDomainPattern.MatchString(domain) || strings.Contains(domain, "..") {
			return fmt.Errorf("invalid VM guest network %s search domain %q", network.Name, domain)
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
		if net.Mode == "iso" {
			b.WriteString("      dhcp6: false\n      accept-ra: false\n      link-local: []\n")
		}
		if net.DHCP {
			b.WriteString("      dhcp4: true\n")
			continue
		}
		fmt.Fprintf(&b, "      addresses: [%s]\n", net.Address)
		if net.Gateway != "" {
			fmt.Fprintf(&b, "      gateway4: %s\n", net.Gateway)
		}
		nameservers := vmGuestNetworkNameservers(net)
		searchDomains := vmGuestNetworkSearchDomains(net)
		if len(nameservers) > 0 || len(searchDomains) > 0 {
			fmt.Fprintf(&b, "      nameservers:\n")
			if len(nameservers) > 0 {
				fmt.Fprintf(&b, "        addresses: [%s]\n", strings.Join(nameservers, ", "))
			}
			if len(searchDomains) > 0 {
				fmt.Fprintf(&b, "        search: [%s]\n", strings.Join(searchDomains, ", "))
			}
		}
	}
	return b.String()
}

func vmGuestNetworkNameservers(network vmGuestNetwork) []string {
	if network.Nameservers != nil {
		return network.Nameservers
	}
	if network.Mode != "svc" && network.Gateway == "" {
		return nil
	}
	if dns := strings.TrimSpace(os.Getenv("DEFAULT_NS")); dns != "" {
		return splitVMGuestNameservers(dns)
	}
	return []string{yeetDNSHostIP}
}

func vmGuestNetworkSearchDomains(network vmGuestNetwork) []string {
	if network.SearchDomains != nil {
		return network.SearchDomains
	}
	if len(vmGuestNetworkNameservers(network)) == 0 {
		return nil
	}
	if search := strings.TrimSpace(os.Getenv("DEFAULT_SEARCH_DOMAINS")); search != "" {
		return splitVMGuestSearchDomains(search)
	}
	if strings.TrimSpace(os.Getenv("DEFAULT_NS")) != "" {
		return nil
	}
	return []string{strings.TrimSuffix(yeetDNSDomain, ".")}
}

func splitVMGuestNameservers(raw string) []string {
	return splitVMGuestNetworkList(raw)
}

func splitVMGuestSearchDomains(raw string) []string {
	return splitVMGuestNetworkList(raw)
}

func splitVMGuestNetworkList(raw string) []string {
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

func networkdBool(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
