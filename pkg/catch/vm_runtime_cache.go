// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmRuntimeManifestFilename = "runtime-manifest.json"
	maxVMRuntimeManifestBytes = 1 << 20
	maxVMRuntimeBinaryBytes   = 256 << 20
)

var vmRuntimeEnsureLocks sync.Map
var inspectVMRuntimeBinaryArchitecture = vmRuntimeBinaryArchitecture

type vmRuntimeCache struct {
	Root             string
	CatalogURL       string
	Client           *http.Client
	publishNoReplace func(parent, staging, final string) error
}

func (c vmRuntimeCache) Ensure(ctx context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
	return c.ensure(ctx, ref, true)
}

func (c vmRuntimeCache) ensure(ctx context.Context, ref vmRuntimeCatalogRef, requireTrustedURL bool) (db.VMRuntimeArtifactConfig, error) {
	manifest, manifestRaw, err := c.fetchValidatedManifest(ctx, ref, requireTrustedURL)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	root, err := validatedVMRuntimeCacheRoot(c.Root)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	final := filepath.Join(root, manifest.Architecture, ref.RuntimeID, ref.ManifestSHA)
	lock := vmRuntimeCacheLock(final)
	lock.Lock()
	defer lock.Unlock()

	parent, err := ensureTrustedVMRuntimeCacheTree(root, manifest.Architecture, ref.RuntimeID)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if _, err := os.Lstat(final); err == nil {
		return validateVMRuntimeCachedArtifact(ctx, final, ref)
	} else if !errors.Is(err, os.ErrNotExist) {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("inspect VM runtime cache target: %w", err)
	}
	return c.publish(ctx, parent, final, manifestRaw, manifest, ref, requireTrustedURL)
}

func (c vmRuntimeCache) fetchValidatedManifest(ctx context.Context, ref vmRuntimeCatalogRef, requireTrustedURL bool) (vmRuntimeManifest, []byte, error) {
	if err := ref.validate(requireTrustedURL); err != nil {
		return vmRuntimeManifest{}, nil, err
	}
	manifestRaw, err := c.downloadManifest(ctx, ref.ManifestURL, requireTrustedURL)
	if err != nil {
		return vmRuntimeManifest{}, nil, err
	}
	manifestDigest := vmRuntimeSHA256Bytes(manifestRaw)
	if manifestDigest != ref.ManifestSHA {
		return vmRuntimeManifest{}, nil, fmt.Errorf("VM runtime manifest digest mismatch: got %s, want %s", manifestDigest, ref.ManifestSHA)
	}
	manifest, err := decodeVMRuntimeManifest(manifestRaw)
	if err != nil {
		return vmRuntimeManifest{}, nil, err
	}
	if err := validateVMRuntimeManifestRef(manifest, ref); err != nil {
		return vmRuntimeManifest{}, nil, err
	}
	return manifest, manifestRaw, nil
}

func validatedVMRuntimeCacheRoot(rawRoot string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(rawRoot))
	if root == "." || strings.TrimSpace(rawRoot) == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("VM runtime cache root must be absolute")
	}
	return root, nil
}

func (c vmRuntimeCache) publish(ctx context.Context, parent, final string, manifestRaw []byte, manifest vmRuntimeManifest, ref vmRuntimeCatalogRef, requireTrustedURL bool) (db.VMRuntimeArtifactConfig, error) {
	staging, err := c.prepareStaging(ctx, parent, manifestRaw, manifest, ref, requireTrustedURL)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := c.publishCacheNoReplace(parent, staging, final); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return validateVMRuntimeCachedArtifact(ctx, final, ref)
		}
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("publish immutable VM runtime cache entry: %w", err)
	}
	published = true
	if err := syncVMRuntimeDirectory(parent); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	return validateVMRuntimeCachedArtifact(ctx, final, ref)
}

func (c vmRuntimeCache) publishCacheNoReplace(parent, staging, final string) error {
	if c.publishNoReplace != nil {
		return c.publishNoReplace(parent, staging, final)
	}
	return publishVMRuntimeCacheNoReplace(parent, staging, final)
}

func (c vmRuntimeCache) prepareStaging(ctx context.Context, parent string, manifestRaw []byte, manifest vmRuntimeManifest, ref vmRuntimeCatalogRef, requireTrustedURL bool) (staging string, retErr error) {
	staging, err := os.MkdirTemp(parent, "."+ref.ManifestSHA+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create VM runtime staging directory: %w", err)
	}
	cleanupPath := staging
	defer func() {
		if retErr != nil {
			if cleanupErr := os.RemoveAll(cleanupPath); cleanupErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove VM runtime staging directory: %w", cleanupErr))
			}
		}
	}()
	if err := secureVMRuntimeStagingDirectory(staging); err != nil {
		return "", err
	}
	if err := writeVMRuntimeCacheFile(filepath.Join(staging, vmRuntimeManifestFilename), manifestRaw, 0o644); err != nil {
		return "", err
	}
	if err := c.stageComponents(ctx, staging, manifest, ref, requireTrustedURL); err != nil {
		return "", err
	}
	if _, err := validateVMRuntimeArtifactDirectory(ctx, staging, ref, manifestRaw); err != nil {
		return "", err
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return "", fmt.Errorf("set VM runtime directory permissions: %w", err)
	}
	if err := syncVMRuntimeDirectory(staging); err != nil {
		return "", err
	}
	return staging, nil
}

func secureVMRuntimeStagingDirectory(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure VM runtime staging directory: %w", err)
	}
	return validateTrustedVMRuntimeCachePath(path, true)
}

func (c vmRuntimeCache) stageComponents(ctx context.Context, staging string, manifest vmRuntimeManifest, ref vmRuntimeCatalogRef, requireTrustedURL bool) error {
	for _, component := range []struct {
		name string
		want string
	}{
		{name: manifest.Components.Firecracker.Path, want: manifest.Components.Firecracker.SHA256},
		{name: manifest.Components.Jailer.Path, want: manifest.Components.Jailer.SHA256},
	} {
		artifactURL, err := vmRuntimeArtifactURL(ref.ManifestURL, component.name)
		if err != nil {
			return err
		}
		if requireTrustedURL {
			if err := validateTrustedVMRuntimeComponentURL(artifactURL, ref.RuntimeID, component.name); err != nil {
				return err
			}
		}
		if err := c.downloadComponent(ctx, artifactURL, filepath.Join(staging, component.name), component.name, component.want, requireTrustedURL); err != nil {
			return err
		}
	}
	return nil
}

func publishVMRuntimeCacheNoReplace(parent, staging, final string) error {
	parentDir, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open VM runtime cache parent: %w", err)
	}
	defer func() { _ = parentDir.Close() }()
	return renameVMJailerUnitNameNoReplaceAt(
		int(parentDir.Fd()), filepath.Base(staging),
		int(parentDir.Fd()), filepath.Base(final),
	)
}

func (c vmRuntimeCache) FetchCatalog(ctx context.Context) (vmRuntimeCatalog, error) {
	return c.fetchCatalog(ctx, true)
}

func (c vmRuntimeCache) fetchCatalog(ctx context.Context, requireTrustedURL bool) (vmRuntimeCatalog, error) {
	rawURL := strings.TrimSpace(c.CatalogURL)
	if rawURL == "" {
		rawURL = defaultVMRuntimeCatalogURL
	}
	return fetchVMRuntimeCatalogFromURL(ctx, c.httpClient(), rawURL, requireTrustedURL)
}

func (c vmRuntimeCache) downloadManifest(ctx context.Context, rawURL string, requireTrustedURL bool) ([]byte, error) {
	resp, err := c.get(ctx, rawURL, "VM runtime manifest", requireTrustedURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return readLimitedVMRuntimeResponse(resp.Body, maxVMRuntimeManifestBytes, "VM runtime manifest")
}

func (c vmRuntimeCache) downloadComponent(ctx context.Context, rawURL, dst, name, want string, requireTrustedURL bool) error {
	resp, err := c.get(ctx, rawURL, "VM runtime "+name, requireTrustedURL)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged VM runtime %s: %w", name, err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(dst)
		}
	}()
	hasher := sha256.New()
	limited := &io.LimitedReader{R: resp.Body, N: maxVMRuntimeBinaryBytes + 1}
	written, err := io.Copy(io.MultiWriter(file, hasher), limited)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if written > maxVMRuntimeBinaryBytes {
		return fmt.Errorf("download %s: exceeds %d byte limit", name, maxVMRuntimeBinaryBytes)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return fmt.Errorf("VM runtime %s digest mismatch: got %s, want %s", name, got, want)
	}
	if err := file.Chmod(0o755); err != nil {
		return fmt.Errorf("set VM runtime %s permissions: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync VM runtime %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close VM runtime %s: %w", name, err)
	}
	cleanup = false
	return nil
}

func (c vmRuntimeCache) get(ctx context.Context, rawURL, label string, requireTrustedURL bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", label, err)
	}
	req.Header.Set("User-Agent", vmImageHTTPUserAgent)
	resp, err := trustedVMRuntimeHTTPClient(c.httpClient(), requireTrustedURL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, fmt.Errorf("fetch %s: %s", label, resp.Status)
	}
	return resp, nil
}

func (c vmRuntimeCache) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func validateVMRuntimeManifestRef(manifest vmRuntimeManifest, ref vmRuntimeCatalogRef) error {
	if manifest.RuntimeID != ref.RuntimeID {
		return fmt.Errorf("VM runtime manifest runtime_id %q does not match catalog %q", manifest.RuntimeID, ref.RuntimeID)
	}
	if manifest.Upstream.Version != ref.UpstreamVersion {
		return fmt.Errorf("VM runtime manifest upstream version %q does not match catalog %q", manifest.Upstream.Version, ref.UpstreamVersion)
	}
	if manifest.Support.State != ref.Support {
		return fmt.Errorf("VM runtime manifest support %q does not match catalog %q", manifest.Support.State, ref.Support)
	}
	return nil
}

func ensureTrustedVMRuntimeCacheTree(root string, components ...string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create VM runtime cache root: %w", err)
	}
	if err := validateTrustedVMRuntimeCachePath(root, true); err != nil {
		return "", err
	}
	current := root
	for _, component := range components {
		child, err := ensureTrustedVMRuntimeCacheChild(current, component)
		if err != nil {
			return "", err
		}
		current = child
	}
	return current, nil
}

func ensureTrustedVMRuntimeCacheChild(parent, component string) (string, error) {
	if component == "" || filepath.Base(component) != component || component == "." || component == ".." {
		return "", fmt.Errorf("invalid VM runtime cache path component %q", component)
	}
	child := filepath.Join(parent, component)
	if err := os.Mkdir(child, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create VM runtime cache directory %s: %w", child, err)
	}
	if err := validateTrustedVMRuntimeCachePath(child, true); err != nil {
		return "", err
	}
	return child, nil
}

func validateTrustedVMRuntimeCachePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect VM runtime cache path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("VM runtime cache path %s is a symbolic link", path)
	}
	if err := validateVMRuntimeCachePathType(path, info, directory); err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("VM runtime cache path %s is group or other writable", path)
	}
	return validateVMRuntimeCachePathOwner(path, info)
}

func validateVMRuntimeCachePathType(path string, info os.FileInfo, directory bool) error {
	if directory && !info.IsDir() {
		return fmt.Errorf("VM runtime cache path %s must be a directory", path)
	}
	if !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("VM runtime cache path %s must be a regular file", path)
	}
	return nil
}

func validateVMRuntimeCachePathOwner(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect VM runtime cache owner %s", path)
	}
	uid := uint32(os.Geteuid())
	if stat.Uid != 0 && stat.Uid != uid {
		return fmt.Errorf("VM runtime cache path %s is owned by UID %d, want %d", path, stat.Uid, uid)
	}
	return nil
}

func validateVMRuntimeCachedArtifact(ctx context.Context, dir string, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
	if err := validateTrustedVMRuntimeCachePath(dir, true); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	manifestPath := filepath.Join(dir, vmRuntimeManifestFilename)
	if err := validateTrustedVMRuntimeCachePath(manifestPath, false); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("read cached VM runtime manifest: %w", err)
	}
	return validateVMRuntimeArtifactDirectory(ctx, dir, ref, raw)
}

func validateVMRuntimeArtifactDirectory(ctx context.Context, dir string, ref vmRuntimeCatalogRef, manifestRaw []byte) (db.VMRuntimeArtifactConfig, error) {
	if vmRuntimeSHA256Bytes(manifestRaw) != ref.ManifestSHA {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("cached VM runtime manifest digest mismatch")
	}
	manifest, err := decodeVMRuntimeManifest(manifestRaw)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if err := validateVMRuntimeManifestRef(manifest, ref); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if err := validateVMRuntimeCacheEntries(dir); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}

	firecracker := filepath.Join(dir, manifest.Components.Firecracker.Path)
	jailer := filepath.Join(dir, manifest.Components.Jailer.Path)
	firecrackerOutput, err := validateVMRuntimeCachedComponent(ctx, vmRuntimeCachedComponent{
		name: "firecracker", path: firecracker, architecture: manifest.Architecture,
		wantDigest: manifest.Components.Firecracker.SHA256, versionOutput: manifest.Components.Firecracker.VersionOutput,
	})
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	jailerOutput, err := validateVMRuntimeCachedComponent(ctx, vmRuntimeCachedComponent{
		name: "jailer", path: jailer, architecture: manifest.Architecture,
		wantDigest: manifest.Components.Jailer.SHA256, versionOutput: manifest.Components.Jailer.VersionOutput,
	})
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if err := validateVMJailerPairVersion(firecrackerOutput, jailerOutput); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	return db.VMRuntimeArtifactConfig{
		ID:                manifest.RuntimeID,
		ManifestSHA256:    ref.ManifestSHA,
		FirecrackerSHA256: manifest.Components.Firecracker.SHA256,
		JailerSHA256:      manifest.Components.Jailer.SHA256,
		Firecracker:       firecracker,
		Jailer:            jailer,
		Source:            "official",
	}, nil
}

func validateVMRuntimeCacheEntries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read VM runtime cache directory: %w", err)
	}
	wantEntries := map[string]struct{}{vmRuntimeManifestFilename: {}, "firecracker": {}, "jailer": {}}
	if len(entries) != len(wantEntries) {
		return fmt.Errorf("VM runtime cache directory has unexpected entries")
	}
	for _, entry := range entries {
		if _, ok := wantEntries[entry.Name()]; !ok {
			return fmt.Errorf("VM runtime cache directory has unexpected entry %q", entry.Name())
		}
	}
	return nil
}

type vmRuntimeCachedComponent struct {
	name          string
	path          string
	architecture  string
	wantDigest    string
	versionOutput string
}

func validateVMRuntimeCachedComponent(ctx context.Context, component vmRuntimeCachedComponent) (string, error) {
	if err := validateTrustedVMRuntimeCachePath(component.path, false); err != nil {
		return "", err
	}
	info, err := os.Lstat(component.path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm() != 0o755 {
		return "", fmt.Errorf("VM runtime %s permissions are %04o, want 0755", component.name, info.Mode().Perm())
	}
	gotDigest, err := sha256File(component.path)
	if err != nil {
		return "", fmt.Errorf("hash VM runtime %s: %w", component.name, err)
	}
	if gotDigest != component.wantDigest {
		return "", fmt.Errorf("VM runtime %s digest mismatch: got %s, want %s", component.name, gotDigest, component.wantDigest)
	}
	architecture, err := inspectVMRuntimeBinaryArchitecture(component.path)
	if err != nil {
		return "", fmt.Errorf("inspect VM runtime %s architecture: %w", component.name, err)
	}
	if architecture != component.architecture {
		return "", fmt.Errorf("VM runtime %s architecture %q does not match manifest %q", component.name, architecture, component.architecture)
	}
	output, err := probeVMRuntimeVersion(ctx, component.path)
	if err != nil {
		return "", err
	}
	if trimmedVMRuntimeVersionOutput(output) != component.versionOutput {
		return "", fmt.Errorf("VM runtime %s version output %q does not match manifest %q", component.name, trimmedVMRuntimeVersionOutput(output), component.versionOutput)
	}
	return output, nil
}

func vmRuntimeBinaryArchitecture(path string) (string, error) {
	binary, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = binary.Close() }()
	if binary.Class != elf.ELFCLASS64 {
		return "", fmt.Errorf("unsupported ELF class %s", binary.Class)
	}
	switch binary.Machine {
	case elf.EM_X86_64:
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported ELF machine %s", binary.Machine)
	}
}

func writeVMRuntimeCacheFile(path string, raw []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create VM runtime cache file: %w", err)
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return fmt.Errorf("write VM runtime cache file: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set VM runtime cache file permissions: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync VM runtime cache file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close VM runtime cache file: %w", err)
	}
	return nil
}

func syncVMRuntimeDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open VM runtime directory for sync: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync VM runtime directory: %w", err)
	}
	return nil
}

func validateTrustedVMRuntimeComponentURL(rawURL, runtimeID, component string) error {
	if component != "firecracker" && component != "jailer" {
		return fmt.Errorf("invalid VM runtime component %q", component)
	}
	if err := validateTrustedYeetVMArtifactURL(rawURL, "runtime "+component); err != nil {
		return err
	}
	want := "https://github.com/yeetrun/yeet-vm-images/releases/download/" + runtimeID + "/" + component
	if rawURL != want {
		return fmt.Errorf("untrusted VM runtime %s URL %q", component, rawURL)
	}
	return nil
}

func vmRuntimeCacheLock(path string) *sync.Mutex {
	lock, _ := vmRuntimeEnsureLocks.LoadOrStore(path, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func vmRuntimeSHA256Bytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
