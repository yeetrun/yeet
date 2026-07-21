// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmGuestBaseManifestFilename = "guest-manifest.json"
	vmGuestBaseRootFSFilename   = "rootfs.ext4.zst"
	maxVMGuestBaseManifestBytes = 1 << 20
	maxVMGuestBaseRootFSBytes   = int64(16 << 30)
)

type vmComponentManifestProvenance struct {
	SourceCommit   string `json:"source_commit"`
	WorkflowRunURL string `json:"workflow_run_url"`
}

type vmGuestBaseManifestRootFS struct {
	URL               string `json:"url"`
	SHA256            string `json:"sha256"`
	UncompressedBytes int64  `json:"uncompressed_bytes"`
}

type vmGuestBaseManifest struct {
	SchemaVersion        int                           `json:"schema_version"`
	GuestBaseID          string                        `json:"guest_base_id"`
	OS                   string                        `json:"os"`
	OSVersion            string                        `json:"os_version"`
	Architecture         string                        `json:"architecture"`
	RootFS               vmGuestBaseManifestRootFS     `json:"rootfs"`
	DefaultKernelChannel string                        `json:"default_kernel_channel"`
	Provenance           vmComponentManifestProvenance `json:"provenance"`
}

type vmGuestBaseArtifact struct {
	Dir            string
	ManifestPath   string
	RootFSPath     string
	ManifestSHA256 string
	Manifest       vmGuestBaseManifest
}

func (a vmGuestBaseArtifact) DBConfig() db.VMGuestBaseConfig {
	return db.VMGuestBaseConfig{
		ID:               a.Manifest.GuestBaseID,
		ManifestSHA256:   a.ManifestSHA256,
		Source:           "official",
		RootFSProvenance: a.Manifest.RootFS.URL,
	}
}

type vmGuestBaseCache struct {
	Root             string
	Client           *http.Client
	publishNoReplace func(parent, staging, final string) error
}

func (c vmGuestBaseCache) Ensure(ctx context.Context, ref vmGuestBaseCatalogRef) (vmGuestBaseArtifact, error) {
	return c.ensure(ctx, ref, true)
}

func (c vmGuestBaseCache) ensure(ctx context.Context, ref vmGuestBaseCatalogRef, requireTrustedURL bool) (vmGuestBaseArtifact, error) {
	manifest, manifestRaw, err := c.fetchValidatedManifest(ctx, ref, requireTrustedURL)
	if err != nil {
		return vmGuestBaseArtifact{}, err
	}
	root, err := validatedVMComponentCacheRoot(c.Root, "guest-base")
	if err != nil {
		return vmGuestBaseArtifact{}, err
	}
	final := filepath.Join(root, manifest.Architecture, ref.GuestBaseID, ref.ManifestSHA256)
	lock := vmRuntimeCacheLock(final)
	lock.Lock()
	defer lock.Unlock()

	parent, err := ensureTrustedVMRuntimeCacheTree(root, manifest.Architecture, ref.GuestBaseID)
	if err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if _, err := os.Lstat(final); err == nil {
		artifact, validationErr := validateVMGuestBaseArtifactDirectory(final, ref)
		if validationErr == nil {
			return artifact, nil
		}
		if quarantineErr := quarantineVMComponentCacheEntry(parent, final, "guest-base"); quarantineErr != nil {
			return vmGuestBaseArtifact{}, errors.Join(validationErr, quarantineErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return vmGuestBaseArtifact{}, fmt.Errorf("inspect VM guest-base cache target: %w", err)
	}
	return c.publish(ctx, parent, final, manifestRaw, manifest, ref, requireTrustedURL)
}

func (c vmGuestBaseCache) fetchValidatedManifest(ctx context.Context, ref vmGuestBaseCatalogRef, requireTrustedURL bool) (vmGuestBaseManifest, []byte, error) {
	if err := ref.validate(requireTrustedURL); err != nil {
		return vmGuestBaseManifest{}, nil, err
	}
	raw, err := fetchVMComponentFile(ctx, c.Client, ref.ManifestURL, "guest-base manifest", maxVMGuestBaseManifestBytes, requireTrustedURL)
	if err != nil {
		return vmGuestBaseManifest{}, nil, err
	}
	if got := vmRuntimeSHA256Bytes(raw); got != ref.ManifestSHA256 {
		return vmGuestBaseManifest{}, nil, fmt.Errorf("VM guest-base manifest digest mismatch: got %s, want %s", got, ref.ManifestSHA256)
	}
	manifest, err := decodeVMGuestBaseManifest(raw, requireTrustedURL)
	if err != nil {
		return vmGuestBaseManifest{}, nil, err
	}
	if err := validateVMGuestBaseManifestRef(manifest, ref); err != nil {
		return vmGuestBaseManifest{}, nil, err
	}
	return manifest, raw, nil
}

func decodeVMGuestBaseManifest(raw []byte, requireTrustedURL bool) (vmGuestBaseManifest, error) {
	if err := validateVMGuestBaseManifestRequiredJSON(raw); err != nil {
		return vmGuestBaseManifest{}, err
	}
	var manifest vmGuestBaseManifest
	if err := decodeStrictVMRuntimeJSON(raw, &manifest, "VM guest-base manifest"); err != nil {
		return vmGuestBaseManifest{}, err
	}
	if err := manifest.validate(requireTrustedURL); err != nil {
		return vmGuestBaseManifest{}, err
	}
	return manifest, nil
}

func validateVMGuestBaseManifestRequiredJSON(raw []byte) error {
	top, err := decodeVMRuntimeJSONObject(raw, "VM guest-base manifest")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(top, "VM guest-base manifest", "schema_version", "guest_base_id", "os", "os_version", "architecture", "rootfs", "default_kernel_channel", "provenance"); err != nil {
		return err
	}
	if err := validateVMComponentRequiredObject(top["rootfs"], "VM guest-base manifest rootfs", "url", "sha256", "uncompressed_bytes"); err != nil {
		return err
	}
	return validateVMComponentRequiredObject(top["provenance"], "VM guest-base manifest provenance", "source_commit", "workflow_run_url")
}

func validateVMComponentRequiredObject(raw []byte, label string, fields ...string) error {
	object, err := decodeVMRuntimeJSONObject(raw, label)
	if err != nil {
		return err
	}
	return requireVMRuntimeJSONFields(object, label, fields...)
}

func (m vmGuestBaseManifest) validate(requireTrustedURL bool) error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM guest-base manifest schema_version %d", m.SchemaVersion)
	}
	ref := vmGuestBaseCatalogRef{
		GuestBaseID: m.GuestBaseID, OS: m.OS, OSVersion: m.OSVersion,
		Architecture: m.Architecture, ManifestSHA256: strings.Repeat("0", 64),
	}
	if err := ref.validate(false); err != nil {
		return err
	}
	if !vmRuntimeSHA256Pattern.MatchString(m.RootFS.SHA256) {
		return fmt.Errorf("VM guest-base %s has invalid rootfs sha256", m.GuestBaseID)
	}
	if m.RootFS.UncompressedBytes <= 0 {
		return fmt.Errorf("VM guest-base %s has invalid rootfs uncompressed_bytes", m.GuestBaseID)
	}
	switch m.DefaultKernelChannel {
	case "stable", "candidate":
	default:
		return fmt.Errorf("VM guest-base %s has invalid default_kernel_channel %q", m.GuestBaseID, m.DefaultKernelChannel)
	}
	if err := m.Provenance.validate(); err != nil {
		return err
	}
	if requireTrustedURL {
		want := "https://github.com/yeetrun/yeet-vm-images/releases/download/" + m.GuestBaseID + "/" + vmGuestBaseRootFSFilename
		if err := validateTrustedYeetVMArtifactURL(m.RootFS.URL, "guest-base rootfs"); err != nil {
			return err
		}
		if m.RootFS.URL != want {
			return fmt.Errorf("untrusted VM guest-base rootfs URL %q", m.RootFS.URL)
		}
	}
	return nil
}

func (p vmComponentManifestProvenance) validate() error {
	if !vmRuntimeCommitPattern.MatchString(p.SourceCommit) {
		return fmt.Errorf("VM component manifest has invalid source_commit")
	}
	const workflowPrefix = "https://github.com/yeetrun/yeet-vm-images/actions/runs/"
	runID := strings.TrimPrefix(p.WorkflowRunURL, workflowPrefix)
	if runID == p.WorkflowRunURL || !vmRuntimeWorkflowPattern.MatchString(runID) {
		return fmt.Errorf("VM component manifest has invalid workflow_run_url")
	}
	return nil
}

func validateVMGuestBaseManifestRef(manifest vmGuestBaseManifest, ref vmGuestBaseCatalogRef) error {
	if manifest.GuestBaseID != ref.GuestBaseID ||
		manifest.OS != ref.OS ||
		manifest.OSVersion != ref.OSVersion ||
		manifest.Architecture != ref.Architecture {
		return fmt.Errorf("VM guest-base manifest identity does not match catalog entry %s", ref.GuestBaseID)
	}
	return nil
}

func (c vmGuestBaseCache) publish(ctx context.Context, parent, final string, manifestRaw []byte, manifest vmGuestBaseManifest, ref vmGuestBaseCatalogRef, requireTrustedURL bool) (vmGuestBaseArtifact, error) {
	staging, err := os.MkdirTemp(parent, "."+ref.ManifestSHA256+".tmp-")
	if err != nil {
		return vmGuestBaseArtifact{}, fmt.Errorf("create VM guest-base staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := secureVMComponentStagingDirectory(staging, "guest-base"); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if err := writeVMRuntimeCacheFile(filepath.Join(staging, vmGuestBaseManifestFilename), manifestRaw, 0o644); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if err := downloadVMComponentFile(
		ctx, c.Client, manifest.RootFS.URL, filepath.Join(staging, vmGuestBaseRootFSFilename),
		"guest-base rootfs", manifest.RootFS.SHA256, maxVMGuestBaseRootFSBytes, 0o644, requireTrustedURL,
	); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if _, err := validateVMGuestBaseArtifactDirectory(staging, ref); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return vmGuestBaseArtifact{}, fmt.Errorf("set VM guest-base cache directory permissions: %w", err)
	}
	if err := syncVMRuntimeDirectory(staging); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	publish := c.publishNoReplace
	if publish == nil {
		publish = publishVMRuntimeCacheNoReplace
	}
	if err := publish(parent, staging, final); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return validateVMGuestBaseArtifactDirectory(final, ref)
		}
		return vmGuestBaseArtifact{}, fmt.Errorf("publish immutable VM guest-base cache entry: %w", err)
	}
	published = true
	if err := syncVMRuntimeDirectory(parent); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	return validateVMGuestBaseArtifactDirectory(final, ref)
}

func validateVMGuestBaseArtifactDirectory(dir string, ref vmGuestBaseCatalogRef) (vmGuestBaseArtifact, error) {
	if err := validateVMComponentDirectory(dir, "guest-base"); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if err := validateVMComponentDirectoryEntries(dir, []string{vmGuestBaseManifestFilename, vmGuestBaseRootFSFilename}, "guest-base"); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	manifestPath := filepath.Join(dir, vmGuestBaseManifestFilename)
	raw, err := readVMComponentCacheFile(manifestPath, 0o644, "guest-base manifest")
	if err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if vmRuntimeSHA256Bytes(raw) != ref.ManifestSHA256 {
		return vmGuestBaseArtifact{}, fmt.Errorf("cached VM guest-base manifest digest mismatch")
	}
	manifest, err := decodeVMGuestBaseManifest(raw, false)
	if err != nil {
		return vmGuestBaseArtifact{}, err
	}
	if err := validateVMGuestBaseManifestRef(manifest, ref); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	rootfsPath := filepath.Join(dir, vmGuestBaseRootFSFilename)
	if err := validateVMComponentCachedPayload(rootfsPath, 0o644, manifest.RootFS.SHA256, "guest-base rootfs"); err != nil {
		return vmGuestBaseArtifact{}, err
	}
	return vmGuestBaseArtifact{
		Dir: dir, ManifestPath: manifestPath, RootFSPath: rootfsPath,
		ManifestSHA256: ref.ManifestSHA256, Manifest: manifest,
	}, nil
}

func validatedVMComponentCacheRoot(rawRoot, label string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(rawRoot))
	if root == "." || strings.TrimSpace(rawRoot) == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("VM %s cache root must be absolute", label)
	}
	return root, nil
}

func secureVMComponentStagingDirectory(path, label string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure VM %s staging directory: %w", label, err)
	}
	return validateVMComponentDirectory(path, label)
}

func validateVMComponentDirectory(path, label string) error {
	if err := validateTrustedVMRuntimeCachePath(path, true); err != nil {
		return fmt.Errorf("validate VM %s cache directory: %w", label, err)
	}
	return nil
}

func validateVMComponentDirectoryEntries(dir string, want []string, label string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read VM %s cache directory: %w", label, err)
	}
	if len(entries) != len(want) {
		return fmt.Errorf("VM %s cache directory has unexpected entries", label)
	}
	allowed := make(map[string]struct{}, len(want))
	for _, name := range want {
		allowed[name] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return fmt.Errorf("VM %s cache directory has unexpected entry %q", label, entry.Name())
		}
	}
	return nil
}

func readVMComponentCacheFile(path string, mode os.FileMode, label string) ([]byte, error) {
	if err := validateTrustedVMRuntimeCachePath(path, false); err != nil {
		return nil, fmt.Errorf("validate VM %s cache file: %w", label, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect VM %s cache file: %w", label, err)
	}
	if info.Mode().Perm() != mode {
		return nil, fmt.Errorf("VM %s cache file permissions are %04o, want %04o", label, info.Mode().Perm(), mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read VM %s cache file: %w", label, err)
	}
	return raw, nil
}

func validateVMComponentCachedPayload(path string, mode os.FileMode, want, label string) error {
	if _, err := readVMComponentCacheFile(path, mode, label); err != nil {
		return err
	}
	got, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("hash VM %s: %w", label, err)
	}
	if got != want {
		return fmt.Errorf("VM %s digest mismatch: got %s, want %s", label, got, want)
	}
	return nil
}

func fetchVMComponentFile(ctx context.Context, client *http.Client, rawURL, label string, limit int64, requireTrustedURL bool) ([]byte, error) {
	resp, err := getVMComponent(ctx, client, rawURL, label, requireTrustedURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	limited := &io.LimitedReader{R: resp.Body, N: limit + 1}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read VM %s: %w", label, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("VM %s exceeds %d byte limit", label, limit)
	}
	return raw, nil
}

func downloadVMComponentFile(ctx context.Context, client *http.Client, rawURL, dst, label, want string, limit int64, mode os.FileMode, requireTrustedURL bool) error {
	resp, err := getVMComponent(ctx, client, rawURL, label, requireTrustedURL)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged VM %s: %w", label, err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(dst)
		}
	}()
	hasher := sha256.New()
	limited := &io.LimitedReader{R: resp.Body, N: limit + 1}
	written, err := io.Copy(io.MultiWriter(file, hasher), limited)
	if err != nil {
		return fmt.Errorf("download VM %s: %w", label, err)
	}
	if written > limit {
		return fmt.Errorf("download VM %s exceeds %d byte limit", label, limit)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return fmt.Errorf("VM %s digest mismatch: got %s, want %s", label, got, want)
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set VM %s permissions: %w", label, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync VM %s: %w", label, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close VM %s: %w", label, err)
	}
	cleanup = false
	return nil
}

func getVMComponent(ctx context.Context, client *http.Client, rawURL, label string, requireTrustedURL bool) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create VM %s request: %w", label, err)
	}
	req.Header.Set("User-Agent", vmImageHTTPUserAgent)
	resp, err := trustedVMRuntimeHTTPClient(client, requireTrustedURL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch VM %s: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, fmt.Errorf("fetch VM %s: %s", label, resp.Status)
	}
	return resp, nil
}

func quarantineVMComponentCacheEntry(parent, final, label string) error {
	var token [8]byte
	for range 8 {
		if _, err := rand.Read(token[:]); err != nil {
			return fmt.Errorf("allocate VM %s cache quarantine name: %w", label, err)
		}
		quarantine := filepath.Join(parent, "."+filepath.Base(final)+".quarantine-"+hex.EncodeToString(token[:]))
		err := publishVMRuntimeCacheNoReplace(parent, final, quarantine)
		if err == nil {
			return syncVMRuntimeDirectory(parent)
		}
		if !errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("quarantine corrupt VM %s cache entry: %w", label, err)
		}
	}
	return fmt.Errorf("allocate unique VM %s cache quarantine name", label)
}
