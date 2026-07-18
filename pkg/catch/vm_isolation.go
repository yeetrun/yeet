// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	vmIsolationLegacy     = "legacy-root"
	vmIsolationJailer     = "jailer"
	vmIsolationMarkerName = "vmm-isolation"
	vmRuntimeUser         = "yeet-vm"
	vmRuntimeNologin      = "/usr/sbin/nologin"
)

type vmRuntimeIdentity struct {
	UID int
	GID int
}

var (
	vmJailerUnsafePattern = regexp.MustCompile(`[^a-z0-9]+`)
	vmRuntimeUserLookup   = user.Lookup
	vmRuntimeUserAdd      = func(args []string) error {
		cmd := exec.Command("useradd", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("create %s system account: %w: %s", vmRuntimeUser, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

func vmIsolationMarkerPath(root string) string {
	return filepath.Join(serviceRunDirForRoot(root), vmIsolationMarkerName)
}

func vmIsolationModeForRoot(root string) (string, error) {
	raw, err := os.ReadFile(vmIsolationMarkerPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return vmIsolationLegacy, nil
	}
	if err != nil {
		return "", fmt.Errorf("read VM isolation mode: %w", err)
	}
	mode := strings.TrimSpace(string(raw))
	if err := validateVMIsolationMode(mode); err != nil {
		return "", err
	}
	return mode, nil
}

func writeVMIsolationMode(root, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if err := validateVMIsolationMode(mode); err != nil {
		return err
	}
	path := vmIsolationMarkerPath(root)
	if mode == vmIsolationLegacy {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove VM isolation marker: %w", err)
		}
		return nil
	}
	if err := writeVMFileAtomic(path, []byte(mode+"\n"), 0o600); err != nil {
		return fmt.Errorf("write VM isolation marker: %w", err)
	}
	return nil
}

func validateVMIsolationMode(mode string) error {
	switch mode {
	case vmIsolationLegacy, vmIsolationJailer:
		return nil
	default:
		return fmt.Errorf("unsupported VM isolation mode %q; use %s or %s", mode, vmIsolationJailer, vmIsolationLegacy)
	}
}

func writeVMFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ensureVMRuntimeIdentity() (vmRuntimeIdentity, error) {
	account, err := vmRuntimeUserLookup(vmRuntimeUser)
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return vmRuntimeIdentity{}, fmt.Errorf("lookup %s system account: %w", vmRuntimeUser, err)
		}
		args := []string{"--system", "--no-create-home", "--shell", vmRuntimeNologin, "--user-group", vmRuntimeUser}
		if err := vmRuntimeUserAdd(args); err != nil {
			return vmRuntimeIdentity{}, err
		}
		account, err = vmRuntimeUserLookup(vmRuntimeUser)
		if err != nil {
			return vmRuntimeIdentity{}, fmt.Errorf("lookup newly created %s system account: %w", vmRuntimeUser, err)
		}
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid <= 0 {
		return vmRuntimeIdentity{}, fmt.Errorf("%s has invalid non-root UID %q", vmRuntimeUser, account.Uid)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid <= 0 {
		return vmRuntimeIdentity{}, fmt.Errorf("%s has invalid non-root GID %q", vmRuntimeUser, account.Gid)
	}
	return vmRuntimeIdentity{UID: uid, GID: gid}, nil
}

func vmJailerID(service string) string {
	raw := strings.TrimSpace(service)
	base := strings.Trim(vmJailerUnsafePattern.ReplaceAllString(strings.ToLower(raw), "-"), "-")
	if base == "" {
		base = "vm"
	}
	safeRaw := strings.ToLower(raw) == base
	if safeRaw && len("yeet-"+base) <= 64 {
		return "yeet-" + base
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:12]
	const maxBase = 46
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	if base == "" {
		base = "vm"
	}
	return "yeet-" + base + "-" + digest
}
