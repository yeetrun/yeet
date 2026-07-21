// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

const vmGuestKernelSelectionPath = "/etc/yeet-vm/kernel/selected.json"

var vmGuestKernelVersionPattern = regexp.MustCompile(`^linux-[0-9]+[.][0-9]+([.][0-9]+)*-yeet$`)
var vmGuestKernelSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type vmGuestKernelSelection struct {
	SchemaVersion  int               `json:"schema_version"`
	ReleaseID      string            `json:"release_id,omitempty"`
	ManifestSHA256 string            `json:"manifest_sha256,omitempty"`
	Version        string            `json:"version"`
	Kernel         string            `json:"kernel"`
	KernelConfig   string            `json:"kernel_config,omitempty"`
	SHA256         map[string]string `json:"sha256"`
}

func (s vmGuestKernelSelection) validate() error {
	switch s.SchemaVersion {
	case 1:
		if strings.TrimSpace(s.ReleaseID) != "" || strings.TrimSpace(s.ManifestSHA256) != "" {
			return fmt.Errorf("VM guest kernel selector schema_version 1 must not declare component release identity")
		}
	case 2:
		if !vmKernelIDPattern.MatchString(strings.TrimSpace(s.ReleaseID)) {
			return fmt.Errorf("invalid VM guest kernel release_id %q", s.ReleaseID)
		}
		if !vmGuestKernelSHA256Pattern.MatchString(s.ManifestSHA256) {
			return fmt.Errorf("invalid VM guest kernel manifest_sha256")
		}
		if strings.TrimSpace(s.KernelConfig) == "" {
			return fmt.Errorf("VM guest kernel selector schema_version 2 requires kernel_config")
		}
	default:
		return fmt.Errorf("unsupported VM guest kernel selector schema_version %d", s.SchemaVersion)
	}
	if !vmGuestKernelVersionPattern.MatchString(strings.TrimSpace(s.Version)) {
		return fmt.Errorf("invalid VM guest kernel version %q", s.Version)
	}
	if err := validateGuestKernelPackagePath("kernel path", s.Kernel); err != nil {
		return err
	}
	if strings.TrimSpace(s.KernelConfig) != "" {
		if err := validateGuestKernelPackagePath("kernel config path", s.KernelConfig); err != nil {
			return err
		}
	}
	if !vmGuestKernelSHA256Pattern.MatchString(s.SHA256["vmlinux"]) {
		return fmt.Errorf("invalid VM guest kernel vmlinux sha256")
	}
	if strings.TrimSpace(s.KernelConfig) != "" && !vmGuestKernelSHA256Pattern.MatchString(s.SHA256["kernel.config"]) {
		return fmt.Errorf("invalid VM guest kernel config sha256")
	}
	for name := range s.SHA256 {
		if name != "vmlinux" && name != "kernel.config" {
			return fmt.Errorf("invalid VM guest kernel sha256 key %q", name)
		}
	}
	return nil
}

func validateGuestKernelPackagePath(label, p string) error {
	p = strings.TrimSpace(p)
	if p == "" || strings.Contains(p, "\x00") || !strings.HasPrefix(p, "/") {
		return fmt.Errorf("invalid VM guest kernel %s %q", label, p)
	}
	if path.Clean(p) != p || strings.HasSuffix(p, "/") {
		return fmt.Errorf("invalid VM guest kernel %s %q", label, p)
	}
	if guestKernelPackagePathHasParentTraversal(p) || !guestKernelPackagePathAllowed(p) {
		return fmt.Errorf("invalid VM guest kernel %s %q", label, p)
	}
	return nil
}

func guestKernelPackagePathHasParentTraversal(p string) bool {
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func guestKernelPackagePathAllowed(p string) bool {
	return strings.HasPrefix(p, "/usr/lib/yeet-vm/kernels/") ||
		(strings.HasPrefix(p, "/nix/store/") && strings.Contains(p, "/lib/yeet-vm/kernels/"))
}
