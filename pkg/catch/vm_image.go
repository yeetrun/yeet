// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	vmImagePayloadPrefix = "vm://"
	vmImageCacheMissing  = "missing"
	vmImageCacheCurrent  = "current"
	vmImageCacheStale    = "stale"
	vmImageHTTPUserAgent = "yeet-vm-image-fetcher"
)

var vmImageSafeNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var prepareVMRootFSFunc = prepareVMRootFS
var vmRootFSDecompressRunner = runVMRootFSDecompress
var vmImageFetchRetryDelay = 500 * time.Millisecond

type vmImageManifest struct {
	Name                string            `json:"name"`
	Version             string            `json:"version"`
	Architecture        string            `json:"architecture"`
	ImageProfile        string            `json:"image_profile,omitempty"`
	Distro              string            `json:"distro,omitempty"`
	DistroVersion       string            `json:"distro_version,omitempty"`
	DefaultUser         string            `json:"default_user,omitempty"`
	KernelPolicy        string            `json:"kernel_policy,omitempty"`
	GuestInit           string            `json:"guest_init,omitempty"`
	GuestSystemInit     string            `json:"guest_system_init,omitempty"`
	MetadataDriver      string            `json:"metadata_driver,omitempty"`
	SnapSupport         *bool             `json:"snap_support,omitempty"`
	Kernel              string            `json:"kernel"`
	Initrd              string            `json:"initrd,omitempty"`
	RootFS              string            `json:"rootfs"`
	Firecracker         string            `json:"firecracker"`
	RootFSSize          int64             `json:"rootfs_size"`
	KernelVersion       string            `json:"kernel_version,omitempty"`
	UbuntuKernelVersion string            `json:"ubuntu_kernel_version,omitempty"`
	Provenance          map[string]string `json:"provenance,omitempty"`
	Checksums           map[string]string `json:"checksums"`
}

type vmImageCache struct {
	Root        string
	ManifestURL string
	// catalogURL is an internal/test override for the catalog location; catalog
	// entries still require trusted manifest URLs.
	catalogURL string
	Client     *http.Client
}

type vmImageCacheState struct {
	Payload       string `json:"payload"`
	CachedVersion string `json:"cachedVersion,omitempty"`
	LatestVersion string `json:"latestVersion"`
	State         string `json:"state"`
	CachePath     string `json:"cachePath"`
	ManifestURL   string `json:"manifestURL"`
}

type vmImageSourceKind string

const (
	vmImageSourceRemote vmImageSourceKind = "remote"
	vmImageSourceLocal  vmImageSourceKind = "local"
)

type vmImageSource struct {
	Kind        vmImageSourceKind
	ManifestURL string
	LocalName   string
	Family      vmImageCatalogImage
}

type vmImagePaths struct {
	Manifest        string
	Dir             string
	KernelPath      string
	InitrdPath      string
	RootFSPath      string
	FirecrackerPath string
}

type vmImageAsset struct {
	Paths              vmImagePaths
	PreparedRootFSPath string
	Manifest           vmImageManifest
}

func (a vmImageAsset) DiskRootFSPath() string {
	if strings.TrimSpace(a.PreparedRootFSPath) != "" {
		return a.PreparedRootFSPath
	}
	return a.Paths.RootFSPath
}

func vmImageSupportsFastBoot(manifest vmImageManifest) bool {
	return strings.TrimSpace(manifest.GuestInit) == vmGuestInitPath
}

func (m vmImageManifest) DefaultUserOr(fallback string) string {
	if user := strings.TrimSpace(m.DefaultUser); user != "" {
		return user
	}
	if user := strings.TrimSpace(fallback); user != "" {
		return user
	}
	return "ubuntu"
}

func (m vmImageManifest) GuestSystemInitOr(fallback string) string {
	if init := strings.TrimSpace(m.GuestSystemInit); init != "" {
		return init
	}
	if init := strings.TrimSpace(fallback); init != "" {
		return init
	}
	return "/usr/lib/systemd/systemd"
}

func (m vmImageManifest) MetadataDriverOr(fallback string) string {
	if driver := strings.TrimSpace(m.MetadataDriver); driver != "" {
		return driver
	}
	if driver := strings.TrimSpace(fallback); driver != "" {
		return driver
	}
	return "ubuntu"
}

func resolveVMImagePayload(payload string) (vmImageSource, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return vmImageSource{}, fmt.Errorf("VM image payload is required")
	}
	if strings.HasPrefix(payload, vmImagePayloadPrefix) {
		name := strings.TrimPrefix(payload, vmImagePayloadPrefix)
		if err := validateLocalVMImageName(name); err != nil {
			return vmImageSource{}, fmt.Errorf("invalid local VM image name %q: %w", name, err)
		}
		return vmImageSource{Kind: vmImageSourceLocal, LocalName: name}, nil
	}
	return vmImageSource{}, fmt.Errorf("unsupported VM image payload %q (expected imported vm://<name>)", payload)
}

func resolveVMImagePayloadFromCatalog(payload string, catalog vmImageCatalog) (vmImageSource, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return vmImageSource{}, fmt.Errorf("VM image payload is required")
	}
	if image, ok := catalog.ImageByPayload(payload); ok {
		return vmImageSource{Kind: vmImageSourceRemote, ManifestURL: image.ManifestURL, Family: image}, nil
	}
	if strings.HasPrefix(payload, vmImagePayloadPrefix) {
		name := strings.TrimPrefix(payload, vmImagePayloadPrefix)
		if err := validateLocalVMImageNameForCatalog(name, catalog); err != nil {
			if reservedVMImageLocalPrefixFromCatalog(name, catalog) {
				return vmImageSource{}, fmt.Errorf("invalid local VM image name %q: %w (supported: %s or imported vm://<name>)", name, err, vmImageCatalogPayloadsForError(catalog))
			}
			return vmImageSource{}, fmt.Errorf("invalid local VM image name %q: %w", name, err)
		}
		return vmImageSource{Kind: vmImageSourceLocal, LocalName: name}, nil
	}
	return vmImageSource{}, fmt.Errorf("unsupported VM image payload %q (supported: %s or imported vm://<name>)", payload, vmImageCatalogPayloadsForError(catalog))
}

func validateLocalVMImageNameForCatalog(name string, catalog vmImageCatalog) error {
	if err := validateLocalVMImageName(name); err != nil {
		return err
	}
	if reservedVMImageLocalPrefixFromCatalog(name, catalog) {
		return fmt.Errorf("local VM image name %q is reserved", name)
	}
	return nil
}

func reservedVMImageLocalPrefixFromCatalog(name string, catalog vmImageCatalog) bool {
	name = strings.TrimSpace(name)
	for _, image := range catalog.Images {
		officialName := strings.TrimPrefix(strings.TrimSpace(image.Payload), vmImagePayloadPrefix)
		prefix := strings.SplitN(officialName, "/", 2)[0] + "/"
		if prefix != "/" && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func ensureVMImageAsset(ctx context.Context, cache vmImageCache) (vmImageAsset, error) {
	return ensureVMImageAssetFromCache(ctx, cache, nil, nil)
}

func ensureVMImageAssetWithProgress(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (asset vmImageAsset, retErr error) {
	source, err := cache.resolveVMImagePayload(ctx, payload)
	if err != nil {
		return vmImageAsset{}, err
	}
	if source.Kind == vmImageSourceLocal {
		return resolveLocalVMImageAssetForPayload(ctx, cache.Root, source.LocalName)
	}
	cache = cache.withManifestURL(source.ManifestURL)
	if ui != nil {
		state, _, err := cache.inspectRemote(ctx, payload, source.Family)
		if err != nil {
			return vmImageAsset{}, err
		}
		if state.State == vmImageCacheCurrent {
			return cachedVMImageAsset(ctx, cache, state.CachedVersion)
		}
	}

	var progress *byteProgress
	if ui != nil {
		progress = newByteProgress(0)
		ui.Start()
		ui.StartStep("Download VM image")
		defer func() {
			if retErr != nil {
				ui.FailStep(retErr.Error())
			} else {
				ui.DoneStep(progress.finalDetail())
			}
			ui.Stop()
		}()
	}
	return ensureVMImageAssetFromCatalog(ctx, cache, source.Family, progress, ui)
}

func resolveLocalVMImageAssetForPayload(ctx context.Context, cacheRoot, name string) (vmImageAsset, error) {
	if strings.TrimSpace(cacheRoot) == "" {
		return vmImageAsset{}, fmt.Errorf("VM image cache root is required")
	}
	asset, err := resolveLocalVMImageAsset(ctx, cacheRoot, name)
	if err != nil {
		return vmImageAsset{}, localVMImagePayloadError(name, err)
	}
	return asset, nil
}

func localVMImagePayloadError(name string, err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("local VM image %q is not imported; import it with `yeet vm images import %s`: %w", name, name, err)
	}
	return err
}

func ensureVMImageAssetFromCache(ctx context.Context, cache vmImageCache, progress *byteProgress, ui ProgressUI) (vmImageAsset, error) {
	paths, err := cache.ensure(ctx, progress, ui)
	if err != nil {
		return vmImageAsset{}, err
	}
	return vmImageAssetFromPaths(ctx, paths)
}

func ensureVMImageAssetFromCatalog(ctx context.Context, cache vmImageCache, family vmImageCatalogImage, progress *byteProgress, ui ProgressUI) (vmImageAsset, error) {
	paths, err := cache.ensureCatalogFamily(ctx, family, progress, ui)
	if err != nil {
		return vmImageAsset{}, err
	}
	return vmImageAssetFromPaths(ctx, paths)
}

func vmImageAssetFromPaths(ctx context.Context, paths vmImagePaths) (vmImageAsset, error) {
	raw, err := os.ReadFile(paths.Manifest)
	if err != nil {
		return vmImageAsset{}, fmt.Errorf("read VM image manifest: %w", err)
	}
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return vmImageAsset{}, fmt.Errorf("decode VM image manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return vmImageAsset{}, err
	}
	preparedRootFS, err := prepareVMRootFSFunc(ctx, paths.RootFSPath)
	if err != nil {
		return vmImageAsset{}, err
	}
	return vmImageAsset{Paths: paths, PreparedRootFSPath: preparedRootFS, Manifest: manifest}, nil
}

func prepareVMRootFS(ctx context.Context, source string) (string, error) {
	target, compressed := vmRootFSDecompressedPath(source)
	if !compressed {
		return source, nil
	}
	if readyVMRootFS(target, source) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("create VM rootfs dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp VM rootfs: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close temp VM rootfs: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := vmRootFSDecompressRunner(ctx, "zstd", "-d", "-f", "--no-progress", "-o", tmpPath, source); err != nil {
		return "", fmt.Errorf("decompress VM rootfs: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return "", fmt.Errorf("install decompressed VM rootfs: %w", err)
	}
	cleanup = false
	return target, nil
}

func runVMRootFSDecompress(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if len(output) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
}

func vmRootFSDecompressedPath(source string) (string, bool) {
	for _, suffix := range []string{".zst", ".zstd"} {
		if strings.HasSuffix(source, suffix) {
			return strings.TrimSuffix(source, suffix), true
		}
	}
	return source, false
}

func readyVMRootFS(target, source string) bool {
	targetInfo, err := os.Stat(target)
	if err != nil || targetInfo.Size() == 0 {
		return false
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return true
	}
	return !targetInfo.ModTime().Before(sourceInfo.ModTime())
}

func (c vmImageCache) Ensure(ctx context.Context) (vmImagePaths, error) {
	return c.ensure(ctx, nil, nil)
}

func (c vmImageCache) ensure(ctx context.Context, progress *byteProgress, ui ProgressUI) (vmImagePaths, error) {
	manifest, err := c.fetchValidatedManifest(ctx)
	if err != nil {
		return vmImagePaths{}, err
	}
	return c.ensureManifest(ctx, manifest, progress, ui)
}

func (c vmImageCache) ensureCatalogFamily(ctx context.Context, family vmImageCatalogImage, progress *byteProgress, ui ProgressUI) (vmImagePaths, error) {
	manifest, err := c.fetchValidatedManifest(ctx)
	if err != nil {
		return vmImagePaths{}, err
	}
	if err := validateVMImageManifestCatalogFamily(manifest, family, ""); err != nil {
		return vmImagePaths{}, err
	}
	return c.ensureManifest(ctx, manifest, progress, ui)
}

func (c vmImageCache) fetchValidatedManifest(ctx context.Context) (vmImageManifest, error) {
	manifest, err := c.fetchManifest(ctx)
	if err != nil {
		return vmImageManifest{}, err
	}
	if err := manifest.validate(); err != nil {
		return vmImageManifest{}, err
	}
	return manifest, nil
}

func (c vmImageCache) ensureManifest(ctx context.Context, manifest vmImageManifest, progress *byteProgress, ui ProgressUI) (vmImagePaths, error) {
	root := strings.TrimSpace(c.Root)
	if root == "" {
		return vmImagePaths{}, fmt.Errorf("VM image cache root is required")
	}
	dir := filepath.Join(root, manifest.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return vmImagePaths{}, fmt.Errorf("create VM image cache dir: %w", err)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	paths, err := c.ensureArtifacts(ctx, dir, manifest, progress, ui)
	if err != nil {
		return vmImagePaths{}, err
	}
	paths.Manifest = manifestPath
	paths.Dir = dir
	if err := writeManifestFile(manifestPath, manifest); err != nil {
		return vmImagePaths{}, err
	}

	return paths, nil
}

func (c vmImageCache) Inspect(ctx context.Context, payload string) (vmImageCacheState, vmImageManifest, error) {
	payload = strings.TrimSpace(payload)
	source, err := c.resolveVMImagePayload(ctx, payload)
	if err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	if source.Kind == vmImageSourceLocal {
		return c.inspectLocal(ctx, payload, source.LocalName)
	}
	return c.withManifestURL(source.ManifestURL).inspectRemote(ctx, payload, source.Family)
}

func (c vmImageCache) resolveVMImagePayload(ctx context.Context, payload string) (vmImageSource, error) {
	localSource, localExists, err := c.importedLocalVMImageSource(payload)
	if err != nil {
		return vmImageSource{}, err
	}
	catalog, err := c.FetchCatalog(ctx)
	if err != nil {
		if localExists {
			return localSource, nil
		}
		return vmImageSource{}, err
	}
	source, err := resolveVMImagePayloadFromCatalog(payload, catalog)
	if err != nil {
		return vmImageSource{}, err
	}
	return source, nil
}

func (c vmImageCache) importedLocalVMImageSource(payload string) (vmImageSource, bool, error) {
	payload = strings.TrimSpace(payload)
	if !strings.HasPrefix(payload, vmImagePayloadPrefix) {
		return vmImageSource{}, false, nil
	}
	name := strings.TrimPrefix(payload, vmImagePayloadPrefix)
	if err := validateLocalVMImageName(name); err != nil {
		return vmImageSource{}, false, fmt.Errorf("invalid local VM image name %q: %w", name, err)
	}
	exists, err := localVMImageRefExists(c.Root, name)
	if err != nil {
		return vmImageSource{}, false, err
	}
	if !exists {
		return vmImageSource{}, false, nil
	}
	return vmImageSource{Kind: vmImageSourceLocal, LocalName: name}, true, nil
}

func (c vmImageCache) inspectLocal(ctx context.Context, payload, name string) (vmImageCacheState, vmImageManifest, error) {
	asset, err := resolveLocalVMImageAssetForPayload(ctx, c.Root, name)
	if err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	state := vmImageCacheState{
		Payload:       payload,
		CachedVersion: asset.Manifest.Version,
		LatestVersion: asset.Manifest.Version,
		State:         vmImageCacheCurrent,
		CachePath:     asset.Paths.Dir,
	}
	return state, asset.Manifest, nil
}

func (c vmImageCache) inspectRemote(ctx context.Context, payload string, family vmImageCatalogImage) (vmImageCacheState, vmImageManifest, error) {
	manifestURL := c.manifestURL()
	latestManifest, err := c.fetchManifest(ctx)
	if err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	if err := latestManifest.validate(); err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	if err := validateVMImageManifestCatalogFamily(latestManifest, family, payload); err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	root := strings.TrimSpace(c.Root)
	if root == "" {
		return vmImageCacheState{}, vmImageManifest{}, fmt.Errorf("VM image cache root is required")
	}

	state := vmImageCacheState{
		Payload:       payload,
		LatestVersion: latestManifest.Version,
		State:         vmImageCacheMissing,
		CachePath:     filepath.Join(root, latestManifest.Version),
		ManifestURL:   manifestURL,
	}
	cachedManifest, cachedDir, ok, err := latestCachedVMImageManifest(root, family)
	if err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	if !ok {
		return state, latestManifest, nil
	}

	state.CachedVersion = cachedManifest.Version
	if cachedManifest.Version != latestManifest.Version {
		state.State = vmImageCacheStale
		return state, latestManifest, nil
	}
	if cachedVMImageArtifactsReady(cachedDir, latestManifest) {
		state.State = vmImageCacheCurrent
		return state, latestManifest, nil
	}
	state.State = vmImageCacheStale
	return state, latestManifest, nil
}

func validateVMImageManifestCatalogFamily(manifest vmImageManifest, family vmImageCatalogImage, payload string) error {
	if family.matchesVersion(manifest.Version) {
		return nil
	}
	if strings.TrimSpace(payload) == "" {
		payload = strings.TrimSpace(family.Payload)
	}
	return fmt.Errorf("VM image manifest version %q does not match catalog version prefix %q for %s", manifest.Version, family.VersionPrefix, payload)
}

func (c vmImageCache) ensureArtifacts(ctx context.Context, dir string, manifest vmImageManifest, progress *byteProgress, ui ProgressUI) (vmImagePaths, error) {
	kernelPath, err := c.ensureArtifact(ctx, dir, manifest, manifest.Kernel, progress, ui)
	if err != nil {
		return vmImagePaths{}, err
	}
	var initrdPath string
	if strings.TrimSpace(manifest.Initrd) != "" {
		initrdPath, err = c.ensureArtifact(ctx, dir, manifest, manifest.Initrd, progress, ui)
		if err != nil {
			return vmImagePaths{}, err
		}
	}
	rootFSPath, err := c.ensureArtifact(ctx, dir, manifest, manifest.RootFS, progress, ui)
	if err != nil {
		return vmImagePaths{}, err
	}
	firecrackerPath, err := c.ensureArtifact(ctx, dir, manifest, manifest.Firecracker, progress, ui)
	if err != nil {
		return vmImagePaths{}, err
	}
	if err := os.Chmod(firecrackerPath, 0o755); err != nil {
		return vmImagePaths{}, fmt.Errorf("chmod firecracker: %w", err)
	}

	return vmImagePaths{
		KernelPath:      kernelPath,
		InitrdPath:      initrdPath,
		RootFSPath:      rootFSPath,
		FirecrackerPath: firecrackerPath,
	}, nil
}

func (c vmImageCache) fetchManifest(ctx context.Context) (vmImageManifest, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		manifest, retry, err := c.fetchManifestOnce(ctx)
		if err == nil {
			return manifest, nil
		}
		lastErr = err
		if !retry || attempt == 3 {
			return vmImageManifest{}, err
		}
		if err := sleepVMImageRetry(ctx, vmImageFetchRetryDelay); err != nil {
			return vmImageManifest{}, err
		}
	}
	return vmImageManifest{}, lastErr
}

func (c vmImageCache) fetchManifestOnce(ctx context.Context) (vmImageManifest, bool, error) {
	manifestURL := c.manifestURL()
	if manifestURL == "" {
		return vmImageManifest{}, false, fmt.Errorf("VM image manifest URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return vmImageManifest{}, false, fmt.Errorf("create VM image manifest request: %w", err)
	}
	req.Header.Set("User-Agent", vmImageHTTPUserAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return vmImageManifest{}, true, fmt.Errorf("fetch VM image manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vmImageManifest{}, resp.StatusCode >= 500, fmt.Errorf("fetch VM image manifest: %s", resp.Status)
	}
	var manifest vmImageManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return vmImageManifest{}, false, fmt.Errorf("decode VM image manifest: %w", err)
	}
	return manifest, false, nil
}

func sleepVMImageRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait to retry VM image fetch: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func (c vmImageCache) ensureArtifact(ctx context.Context, dir string, manifest vmImageManifest, artifactName string, progress *byteProgress, ui ProgressUI) (string, error) {
	if err := validateVMImageArtifactName(artifactName); err != nil {
		return "", err
	}
	want := manifest.Checksums[artifactName]
	dst := filepath.Join(dir, artifactName)
	if got, err := sha256File(dst); err == nil {
		if strings.EqualFold(got, want) {
			return dst, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("verify cached VM image artifact %q: %w", artifactName, err)
	}

	artifactURL, err := c.artifactURL(artifactName)
	if err != nil {
		return "", err
	}
	if err := c.downloadVerifiedFile(ctx, artifactURL, dst, artifactName, want, progress, ui); err != nil {
		return "", err
	}
	return dst, nil
}

func (c vmImageCache) downloadVerifiedFile(ctx context.Context, rawURL, dst, artifactName, want string, progress *byteProgress, ui ProgressUI) error {
	resp, err := c.downloadArtifactResponse(ctx, rawURL)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	reader := io.Reader(resp.Body)
	if progress != nil {
		reader = progress.reader(reader)
	}
	var detailProgress *byteProgress
	var stopProgress func()
	if ui != nil {
		total := int64(0)
		if resp.ContentLength > 0 {
			total = resp.ContentLength
		}
		detailProgress = newByteProgress(total)
		reader = detailProgress.reader(reader)
		stopProgress = startByteProgressUpdates(ui, detailProgress)
		defer stopProgress()
	}
	if err := installVerifiedVMImageArtifact(reader, dst, artifactName, want); err != nil {
		return err
	}
	if detailProgress != nil {
		ui.UpdateDetail(detailProgress.detail())
	}
	return nil
}

func startByteProgressUpdates(ui ProgressUI, progress *byteProgress) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(byteProgressInterval)
		defer ticker.Stop()
		ui.UpdateDetail(progress.detail())
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ui.UpdateDetail(progress.detail())
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (c vmImageCache) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func (c vmImageCache) FetchCatalog(ctx context.Context) (vmImageCatalog, error) {
	if strings.TrimSpace(c.catalogURL) != "" {
		return fetchVMImageCatalogFromURL(ctx, c.httpClient(), c.catalogURL, false)
	}
	return fetchVMImageCatalogFunc(ctx, c.httpClient())
}

func (c vmImageCache) manifestURL() string {
	return strings.TrimSpace(c.ManifestURL)
}

func (c vmImageCache) withManifestURL(manifestURL string) vmImageCache {
	if strings.TrimSpace(c.ManifestURL) == "" {
		c.ManifestURL = strings.TrimSpace(manifestURL)
	}
	return c
}

func (c vmImageCache) downloadArtifactResponse(ctx context.Context, rawURL string) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, retry, err := c.downloadArtifactResponseOnce(ctx, rawURL)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retry || attempt == 3 {
			return nil, err
		}
		if err := sleepVMImageRetry(ctx, vmImageFetchRetryDelay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c vmImageCache) downloadArtifactResponseOnce(ctx context.Context, rawURL string) (*http.Response, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create VM image artifact request: %w", err)
	}
	req.Header.Set("User-Agent", vmImageHTTPUserAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("download VM image artifact %q: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, resp.StatusCode >= 500, fmt.Errorf("download VM image artifact %q: %s", rawURL, resp.Status)
	}
	return resp, false, nil
}

func installVerifiedVMImageArtifact(r io.Reader, dst, artifactName, want string) error {
	tmpPath, err := writeTempVMImageArtifact(r, dst)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := verifyVMImageArtifactChecksum(tmpPath, artifactName, want); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("install VM image artifact: %w", err)
	}
	cleanup = false
	return nil
}

func writeTempVMImageArtifact(r io.Reader, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create VM image artifact dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp VM image artifact: %w", err)
	}
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp VM image artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("close temp VM image artifact: %w", err)
	}
	return tmp.Name(), nil
}

func verifyVMImageArtifactChecksum(path, artifactName, want string) error {
	got, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("verify downloaded VM image artifact %q: %w", artifactName, err)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("VM image artifact %q checksum mismatch: got %s, want %s", artifactName, got, want)
	}
	return nil
}

func (c vmImageCache) artifactURL(artifactName string) (string, error) {
	if err := validateVMImageArtifactName(artifactName); err != nil {
		return "", err
	}
	manifestURL := c.manifestURL()
	if manifestURL == "" {
		return "", fmt.Errorf("VM image manifest URL is required")
	}
	u, err := url.Parse(manifestURL)
	if err != nil {
		return "", fmt.Errorf("parse VM image manifest URL: %w", err)
	}
	u.Path = path.Join(path.Dir(u.Path), url.PathEscape(artifactName))
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (m vmImageManifest) validate() error {
	if err := m.validateRequiredFields(); err != nil {
		return err
	}
	if m.RootFSSize <= 0 {
		return fmt.Errorf("VM image manifest rootfs_size must be positive")
	}
	if len(m.Checksums) == 0 {
		return fmt.Errorf("VM image manifest missing checksums")
	}
	if err := validateVMImageCacheDirName(m.Version); err != nil {
		return err
	}
	if err := m.validateRuntimeMetadata(); err != nil {
		return err
	}
	return m.validateArtifactChecksums()
}

func (m vmImageManifest) validateRequiredFields() error {
	required := map[string]string{
		"name":         m.Name,
		"version":      m.Version,
		"architecture": m.Architecture,
		"kernel":       m.Kernel,
		"rootfs":       m.RootFS,
		"firecracker":  m.Firecracker,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("VM image manifest missing %s", field)
		}
	}
	return nil
}

func (m vmImageManifest) validateArtifactChecksums() error {
	for _, artifactName := range m.artifactNames() {
		if err := validateVMImageArtifactName(artifactName); err != nil {
			return err
		}
		checksum := strings.TrimSpace(m.Checksums[artifactName])
		if checksum == "" {
			return fmt.Errorf("VM image manifest missing checksum for %q", artifactName)
		}
		if len(checksum) != sha256.Size*2 {
			return fmt.Errorf("VM image manifest checksum for %q has invalid length", artifactName)
		}
		if _, err := hex.DecodeString(checksum); err != nil {
			return fmt.Errorf("VM image manifest checksum for %q is invalid: %w", artifactName, err)
		}
	}
	return nil
}

func (m vmImageManifest) validateRuntimeMetadata() error {
	if user := strings.TrimSpace(m.DefaultUser); user != "" && !vmUserPattern.MatchString(user) {
		return fmt.Errorf("VM image manifest default_user %q is invalid", m.DefaultUser)
	}
	switch strings.TrimSpace(m.MetadataDriver) {
	case "", "ubuntu", "nixos":
	default:
		return fmt.Errorf("VM image manifest metadata_driver %q is unsupported", m.MetadataDriver)
	}
	if err := validateVMGuestSystemInit(m.GuestSystemInit); err != nil {
		return fmt.Errorf("VM image manifest guest_system_init: %w", err)
	}
	return nil
}

func (m vmImageManifest) artifactNames() []string {
	names := []string{m.Kernel}
	if strings.TrimSpace(m.Initrd) != "" {
		names = append(names, m.Initrd)
	}
	return append(names, m.RootFS, m.Firecracker)
}

func latestCachedVMImageManifest(root string, family vmImageCatalogImage) (vmImageManifest, string, bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return vmImageManifest{}, "", false, fmt.Errorf("VM image cache root is required")
	}
	entries, err := readVMImageCacheEntries(root)
	if err != nil {
		return vmImageManifest{}, "", false, err
	}

	var best vmImageManifest
	var bestDir string
	found := false
	for _, entry := range entries {
		manifest, dir, ok, err := cachedVMImageManifestFromEntry(root, entry)
		if err != nil {
			return vmImageManifest{}, "", false, err
		}
		if !ok || !family.matchesVersion(manifest.Version) {
			continue
		}
		if !found || compareVMImageVersions(manifest.Version, best.Version) > 0 {
			best = manifest
			bestDir = dir
			found = true
		}
	}
	return best, bestDir, found, nil
}

func readVMImageCacheEntries(root string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read VM image cache root: %w", err)
	}
	return entries, nil
}

func cachedVMImageManifestFromEntry(root string, entry os.DirEntry) (vmImageManifest, string, bool, error) {
	if !entry.IsDir() {
		return vmImageManifest{}, "", false, nil
	}
	dirName := entry.Name()
	if err := validateVMImageCacheDirName(dirName); err != nil {
		return vmImageManifest{}, "", false, nil
	}
	dir := filepath.Join(root, dirName)
	manifest, ok, err := readCachedVMImageManifest(dir)
	if err != nil || !ok || manifest.Version != dirName {
		return vmImageManifest{}, "", false, err
	}
	return manifest, dir, true, nil
}

func readCachedVMImageManifest(dir string) (vmImageManifest, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return vmImageManifest{}, false, nil
		}
		return vmImageManifest{}, false, fmt.Errorf("read cached VM image manifest: %w", err)
	}
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return vmImageManifest{}, false, nil
	}
	if err := manifest.validate(); err != nil {
		return vmImageManifest{}, false, nil
	}
	return manifest, true, nil
}

func cachedVMImageArtifactsReady(dir string, manifest vmImageManifest) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	for _, artifactName := range manifest.artifactNames() {
		if err := validateVMImageArtifactName(artifactName); err != nil {
			return false
		}
		want := strings.TrimSpace(manifest.Checksums[artifactName])
		if want == "" {
			return false
		}
		got, err := sha256File(filepath.Join(dir, artifactName))
		if err != nil || !strings.EqualFold(got, want) {
			return false
		}
	}
	return true
}

func compareVMImageVersions(a, b string) int {
	if a == b {
		return 0
	}
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		at, an, nextA := nextVMImageVersionToken(a, ai)
		bt, bn, nextB := nextVMImageVersionToken(b, bi)
		if cmp := compareVMImageVersionTokens(at, an, bt, bn); cmp != 0 {
			return cmp
		}
		ai, bi = nextA, nextB
	}
	return compareVMImageVersionRemainder(ai, len(a), bi, len(b))
}

func compareVMImageVersionTokens(a string, aNumber bool, b string, bNumber bool) int {
	if aNumber && bNumber {
		return compareVMImageVersionNumbers(a, b)
	}
	return compareVMImageVersionStrings(a, b)
}

func compareVMImageVersionStrings(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareVMImageVersionRemainder(ai, aLen, bi, bLen int) int {
	if ai == aLen && bi == bLen {
		return 0
	}
	if ai == aLen {
		return -1
	}
	return 1
}

func nextVMImageVersionToken(version string, start int) (string, bool, int) {
	isNumber := isASCIIDigit(version[start])
	end := start + 1
	for end < len(version) && isASCIIDigit(version[end]) == isNumber {
		end++
	}
	return version[start:end], isNumber, end
}

func compareVMImageVersionNumbers(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if a == "" {
		a = "0"
	}
	if b == "" {
		b = "0"
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func validateVMImageCacheDirName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("VM image manifest version is required")
	}
	if name != strings.TrimSpace(name) || name == "." || filepath.Clean(name) != name || filepath.IsAbs(name) || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) || !vmImageSafeNamePattern.MatchString(name) {
		return fmt.Errorf("VM image manifest version %q must be a single cache directory name", name)
	}
	return nil
}

func validateVMImageArtifactName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("VM image artifact name is required")
	}
	if name != strings.TrimSpace(name) || name == "." || filepath.Clean(name) != name || filepath.IsAbs(name) || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) || !vmImageSafeNamePattern.MatchString(name) {
		return fmt.Errorf("VM image artifact %q must be a single filename", name)
	}
	if name == "manifest.json" {
		return fmt.Errorf("VM image artifact %q is reserved for cache metadata", name)
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeManifestFile(path string, manifest vmImageManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create VM image manifest dir: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp VM image manifest: %w", err)
	}
	tmpPath := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		_ = f.Close()
		return fmt.Errorf("write VM image manifest: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write VM image manifest: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install VM image manifest: %w", err)
	}
	cleanup = false
	return nil
}
