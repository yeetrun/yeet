// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmKernelManifestFilename  = "kernel-manifest.json"
	vmKernelFilename          = "vmlinux"
	vmKernelConfigFilename    = "kernel.config"
	maxVMKernelManifestBytes  = 1 << 20
	maxVMKernelBytes          = int64(1 << 30)
	maxVMKernelConfigBytes    = int64(16 << 20)
	vmKernelPackageCatalogURL = "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/kernel-packages/catalog.json"
)

type vmKernelManifestAsset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type vmKernelManifestGuestPackages struct {
	CatalogURL            string `json:"catalog_url"`
	SelectorSchemaVersion int    `json:"selector_schema_version"`
	ReleaseID             string `json:"release_id"`
}

type vmKernelManifest struct {
	SchemaVersion     int                           `json:"schema_version"`
	KernelID          string                        `json:"kernel_id"`
	UpstreamVersion   string                        `json:"upstream_version"`
	PackagingRevision int                           `json:"packaging_revision"`
	Architecture      string                        `json:"architecture"`
	VMLinux           vmKernelManifestAsset         `json:"vmlinux"`
	Config            vmKernelManifestAsset         `json:"config"`
	GuestPackages     vmKernelManifestGuestPackages `json:"guest_packages"`
	Provenance        vmComponentManifestProvenance `json:"provenance"`
}

type vmKernelArtifact struct {
	Dir            string
	ManifestPath   string
	KernelPath     string
	ConfigPath     string
	ManifestSHA256 string
	Manifest       vmKernelManifest
}

func (a vmKernelArtifact) DBConfig() db.VMKernelArtifactConfig {
	return db.VMKernelArtifactConfig{
		ID:             a.Manifest.KernelID,
		ManifestSHA256: a.ManifestSHA256,
		SHA256:         a.Manifest.VMLinux.SHA256,
		Path:           a.KernelPath,
		Source:         "official",
	}
}

type vmKernelArtifactCache struct {
	Root             string
	Client           *http.Client
	publishNoReplace func(parent, staging, final string) error
}

func (c vmKernelArtifactCache) Ensure(ctx context.Context, ref vmKernelCatalogRef) (vmKernelArtifact, error) {
	return c.ensure(ctx, ref, true)
}

func (c vmKernelArtifactCache) ensure(ctx context.Context, ref vmKernelCatalogRef, requireTrustedURL bool) (vmKernelArtifact, error) {
	manifest, manifestRaw, err := c.fetchValidatedManifest(ctx, ref, requireTrustedURL)
	if err != nil {
		return vmKernelArtifact{}, err
	}
	root, err := validatedVMComponentCacheRoot(c.Root, "kernel")
	if err != nil {
		return vmKernelArtifact{}, err
	}
	final := filepath.Join(root, manifest.Architecture, ref.KernelID, ref.ManifestSHA256)
	lock := vmRuntimeCacheLock(final)
	lock.Lock()
	defer lock.Unlock()

	parent, err := ensureTrustedVMRuntimeCacheTree(root, manifest.Architecture, ref.KernelID)
	if err != nil {
		return vmKernelArtifact{}, err
	}
	if _, err := os.Lstat(final); err == nil {
		artifact, validationErr := validateVMKernelArtifactDirectory(final, ref)
		if validationErr == nil {
			return artifact, nil
		}
		if quarantineErr := quarantineVMComponentCacheEntry(parent, final, "kernel"); quarantineErr != nil {
			return vmKernelArtifact{}, errors.Join(validationErr, quarantineErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return vmKernelArtifact{}, fmt.Errorf("inspect VM kernel cache target: %w", err)
	}
	return c.publish(ctx, parent, final, manifestRaw, manifest, ref, requireTrustedURL)
}

func (c vmKernelArtifactCache) fetchValidatedManifest(ctx context.Context, ref vmKernelCatalogRef, requireTrustedURL bool) (vmKernelManifest, []byte, error) {
	if err := ref.validate(requireTrustedURL); err != nil {
		return vmKernelManifest{}, nil, err
	}
	raw, err := fetchVMComponentFile(ctx, c.Client, ref.ManifestURL, "kernel manifest", maxVMKernelManifestBytes, requireTrustedURL)
	if err != nil {
		return vmKernelManifest{}, nil, err
	}
	if got := vmRuntimeSHA256Bytes(raw); got != ref.ManifestSHA256 {
		return vmKernelManifest{}, nil, fmt.Errorf("VM kernel manifest digest mismatch: got %s, want %s", got, ref.ManifestSHA256)
	}
	manifest, err := decodeVMKernelManifest(raw, requireTrustedURL)
	if err != nil {
		return vmKernelManifest{}, nil, err
	}
	if err := validateVMKernelManifestRef(manifest, ref); err != nil {
		return vmKernelManifest{}, nil, err
	}
	return manifest, raw, nil
}

func decodeVMKernelManifest(raw []byte, requireTrustedURL bool) (vmKernelManifest, error) {
	if err := validateVMKernelManifestRequiredJSON(raw); err != nil {
		return vmKernelManifest{}, err
	}
	var manifest vmKernelManifest
	if err := decodeStrictVMRuntimeJSON(raw, &manifest, "VM kernel manifest"); err != nil {
		return vmKernelManifest{}, err
	}
	if err := manifest.validate(requireTrustedURL); err != nil {
		return vmKernelManifest{}, err
	}
	return manifest, nil
}

func validateVMKernelManifestRequiredJSON(raw []byte) error {
	top, err := decodeVMRuntimeJSONObject(raw, "VM kernel manifest")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(top, "VM kernel manifest", "schema_version", "kernel_id", "upstream_version", "packaging_revision", "architecture", "vmlinux", "config", "guest_packages", "provenance"); err != nil {
		return err
	}
	for field, required := range map[string][]string{
		"vmlinux":        {"url", "sha256"},
		"config":         {"url", "sha256"},
		"guest_packages": {"catalog_url", "selector_schema_version", "release_id"},
		"provenance":     {"source_commit", "workflow_run_url"},
	} {
		if err := validateVMComponentRequiredObject(top[field], "VM kernel manifest "+field, required...); err != nil {
			return err
		}
	}
	return nil
}

func (m vmKernelManifest) validate(requireTrustedURL bool) error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM kernel manifest schema_version %d", m.SchemaVersion)
	}
	ref := vmKernelCatalogRef{
		KernelID: m.KernelID, UpstreamVersion: m.UpstreamVersion,
		PackagingRevision: m.PackagingRevision, Architecture: m.Architecture,
		ManifestSHA256: strings.Repeat("0", 64),
	}
	if err := ref.validate(false); err != nil {
		return err
	}
	if !vmRuntimeSHA256Pattern.MatchString(m.VMLinux.SHA256) || !vmRuntimeSHA256Pattern.MatchString(m.Config.SHA256) {
		return fmt.Errorf("VM kernel %s has invalid payload sha256", m.KernelID)
	}
	if m.GuestPackages.CatalogURL != vmKernelPackageCatalogURL ||
		m.GuestPackages.SelectorSchemaVersion != 2 ||
		m.GuestPackages.ReleaseID != m.KernelID {
		return fmt.Errorf("VM kernel %s has invalid guest package selector metadata", m.KernelID)
	}
	if err := m.Provenance.validate(); err != nil {
		return err
	}
	if requireTrustedURL {
		base := "https://github.com/yeetrun/yeet-vm-images/releases/download/" + m.KernelID + "/"
		for _, asset := range []struct {
			name   string
			rawURL string
		}{
			{name: vmKernelFilename, rawURL: m.VMLinux.URL},
			{name: vmKernelConfigFilename, rawURL: m.Config.URL},
		} {
			if err := validateTrustedYeetVMArtifactURL(asset.rawURL, "kernel "+asset.name); err != nil {
				return err
			}
			if asset.rawURL != base+asset.name {
				return fmt.Errorf("untrusted VM kernel %s URL %q", asset.name, asset.rawURL)
			}
		}
	}
	return nil
}

func validateVMKernelManifestRef(manifest vmKernelManifest, ref vmKernelCatalogRef) error {
	if manifest.KernelID != ref.KernelID ||
		manifest.UpstreamVersion != ref.UpstreamVersion ||
		manifest.PackagingRevision != ref.PackagingRevision ||
		manifest.Architecture != ref.Architecture {
		return fmt.Errorf("VM kernel manifest identity does not match catalog entry %s", ref.KernelID)
	}
	return nil
}

func (c vmKernelArtifactCache) publish(ctx context.Context, parent, final string, manifestRaw []byte, manifest vmKernelManifest, ref vmKernelCatalogRef, requireTrustedURL bool) (vmKernelArtifact, error) {
	staging, err := os.MkdirTemp(parent, "."+ref.ManifestSHA256+".tmp-")
	if err != nil {
		return vmKernelArtifact{}, fmt.Errorf("create VM kernel staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := secureVMComponentStagingDirectory(staging, "kernel"); err != nil {
		return vmKernelArtifact{}, err
	}
	if err := writeVMRuntimeCacheFile(filepath.Join(staging, vmKernelManifestFilename), manifestRaw, 0o644); err != nil {
		return vmKernelArtifact{}, err
	}
	for _, asset := range []struct {
		name   string
		rawURL string
		want   string
		limit  int64
	}{
		{name: vmKernelFilename, rawURL: manifest.VMLinux.URL, want: manifest.VMLinux.SHA256, limit: maxVMKernelBytes},
		{name: vmKernelConfigFilename, rawURL: manifest.Config.URL, want: manifest.Config.SHA256, limit: maxVMKernelConfigBytes},
	} {
		if err := downloadVMComponentFile(
			ctx, c.Client, asset.rawURL, filepath.Join(staging, asset.name),
			"kernel "+asset.name, asset.want, asset.limit, 0o644, requireTrustedURL,
		); err != nil {
			return vmKernelArtifact{}, err
		}
	}
	if _, err := validateVMKernelArtifactDirectory(staging, ref); err != nil {
		return vmKernelArtifact{}, err
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return vmKernelArtifact{}, fmt.Errorf("set VM kernel cache directory permissions: %w", err)
	}
	if err := syncVMRuntimeDirectory(staging); err != nil {
		return vmKernelArtifact{}, err
	}
	publish := c.publishNoReplace
	if publish == nil {
		publish = publishVMRuntimeCacheNoReplace
	}
	if err := publish(parent, staging, final); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return validateVMKernelArtifactDirectory(final, ref)
		}
		return vmKernelArtifact{}, fmt.Errorf("publish immutable VM kernel cache entry: %w", err)
	}
	published = true
	if err := syncVMRuntimeDirectory(parent); err != nil {
		return vmKernelArtifact{}, err
	}
	return validateVMKernelArtifactDirectory(final, ref)
}

func validateVMKernelArtifactDirectory(dir string, ref vmKernelCatalogRef) (vmKernelArtifact, error) {
	if err := validateVMComponentDirectory(dir, "kernel"); err != nil {
		return vmKernelArtifact{}, err
	}
	if err := validateVMComponentDirectoryEntries(dir, []string{vmKernelManifestFilename, vmKernelFilename, vmKernelConfigFilename}, "kernel"); err != nil {
		return vmKernelArtifact{}, err
	}
	manifestPath := filepath.Join(dir, vmKernelManifestFilename)
	raw, err := readVMComponentCacheFile(manifestPath, 0o644, "kernel manifest")
	if err != nil {
		return vmKernelArtifact{}, err
	}
	if vmRuntimeSHA256Bytes(raw) != ref.ManifestSHA256 {
		return vmKernelArtifact{}, fmt.Errorf("cached VM kernel manifest digest mismatch")
	}
	manifest, err := decodeVMKernelManifest(raw, false)
	if err != nil {
		return vmKernelArtifact{}, err
	}
	if err := validateVMKernelManifestRef(manifest, ref); err != nil {
		return vmKernelArtifact{}, err
	}
	kernelPath := filepath.Join(dir, vmKernelFilename)
	configPath := filepath.Join(dir, vmKernelConfigFilename)
	if err := validateVMComponentCachedPayload(kernelPath, 0o644, manifest.VMLinux.SHA256, "kernel vmlinux"); err != nil {
		return vmKernelArtifact{}, err
	}
	if err := validateVMComponentCachedPayload(configPath, 0o644, manifest.Config.SHA256, "kernel config"); err != nil {
		return vmKernelArtifact{}, err
	}
	return vmKernelArtifact{
		Dir: dir, ManifestPath: manifestPath, KernelPath: kernelPath, ConfigPath: configPath,
		ManifestSHA256: ref.ManifestSHA256, Manifest: manifest,
	}, nil
}
