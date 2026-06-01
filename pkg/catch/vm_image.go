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
)

const defaultVMImageVersion = "ubuntu-26.04-amd64-v1"
const defaultVMImageManifestURL = "https://github.com/yeetrun/yeet-vm-images/releases/download/" + defaultVMImageVersion + "/manifest.json"

var vmImageSafeNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var prepareVMRootFSFunc = prepareVMRootFS
var vmRootFSDecompressRunner = runVMRootFSDecompress

type vmImageManifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Architecture string            `json:"architecture"`
	Kernel       string            `json:"kernel"`
	Initrd       string            `json:"initrd,omitempty"`
	RootFS       string            `json:"rootfs"`
	Firecracker  string            `json:"firecracker"`
	RootFSSize   int64             `json:"rootfs_size"`
	Checksums    map[string]string `json:"checksums"`
}

type vmImageCache struct {
	Root        string
	ManifestURL string
	Client      *http.Client
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

func ensureVMImageAsset(ctx context.Context, cache vmImageCache) (vmImageAsset, error) {
	paths, err := cache.Ensure(ctx)
	if err != nil {
		return vmImageAsset{}, err
	}
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
	manifest, err := c.fetchManifest(ctx)
	if err != nil {
		return vmImagePaths{}, err
	}
	if err := manifest.validate(); err != nil {
		return vmImagePaths{}, err
	}
	root := strings.TrimSpace(c.Root)
	if root == "" {
		return vmImagePaths{}, fmt.Errorf("VM image cache root is required")
	}
	dir := filepath.Join(root, manifest.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return vmImagePaths{}, fmt.Errorf("create VM image cache dir: %w", err)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	paths, err := c.ensureArtifacts(ctx, dir, manifest)
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

func (c vmImageCache) ensureArtifacts(ctx context.Context, dir string, manifest vmImageManifest) (vmImagePaths, error) {
	kernelPath, err := c.ensureArtifact(ctx, dir, manifest, manifest.Kernel)
	if err != nil {
		return vmImagePaths{}, err
	}
	var initrdPath string
	if strings.TrimSpace(manifest.Initrd) != "" {
		initrdPath, err = c.ensureArtifact(ctx, dir, manifest, manifest.Initrd)
		if err != nil {
			return vmImagePaths{}, err
		}
	}
	rootFSPath, err := c.ensureArtifact(ctx, dir, manifest, manifest.RootFS)
	if err != nil {
		return vmImagePaths{}, err
	}
	firecrackerPath, err := c.ensureArtifact(ctx, dir, manifest, manifest.Firecracker)
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
	manifestURL := strings.TrimSpace(c.ManifestURL)
	if manifestURL == "" {
		manifestURL = defaultVMImageManifestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return vmImageManifest{}, fmt.Errorf("create VM image manifest request: %w", err)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return vmImageManifest{}, fmt.Errorf("fetch VM image manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vmImageManifest{}, fmt.Errorf("fetch VM image manifest: %s", resp.Status)
	}
	var manifest vmImageManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return vmImageManifest{}, fmt.Errorf("decode VM image manifest: %w", err)
	}
	return manifest, nil
}

func (c vmImageCache) ensureArtifact(ctx context.Context, dir string, manifest vmImageManifest, artifactName string) (string, error) {
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
	if err := c.downloadVerifiedFile(ctx, artifactURL, dst, artifactName, want); err != nil {
		return "", err
	}
	return dst, nil
}

func (c vmImageCache) downloadVerifiedFile(ctx context.Context, rawURL, dst, artifactName, want string) error {
	resp, err := c.downloadArtifactResponse(ctx, rawURL)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return installVerifiedVMImageArtifact(resp.Body, dst, artifactName, want)
}

func (c vmImageCache) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func (c vmImageCache) downloadArtifactResponse(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create VM image artifact request: %w", err)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download VM image artifact %q: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, fmt.Errorf("download VM image artifact %q: %s", rawURL, resp.Status)
	}
	return resp, nil
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
	manifestURL := strings.TrimSpace(c.ManifestURL)
	if manifestURL == "" {
		manifestURL = defaultVMImageManifestURL
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

func (m vmImageManifest) artifactNames() []string {
	names := []string{m.Kernel}
	if strings.TrimSpace(m.Initrd) != "" {
		names = append(names, m.Initrd)
	}
	return append(names, m.RootFS, m.Firecracker)
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
