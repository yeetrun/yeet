// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVMImageCacheDownloadsAndVerifiesManifestArtifacts(t *testing.T) {
	rootfs := []byte("rootfs")
	kernel := []byte("kernel")
	fc := []byte("firecracker")
	sum := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(`{
				"name":"yeet-ubuntu-26.04",
				"version":"ubuntu-26.04-amd64-v1",
				"architecture":"amd64",
				"kernel":"vmlinux",
				"rootfs":"rootfs.ext4.zst",
				"firecracker":"firecracker",
				"rootfs_size":2147483648,
				"checksums":{
					"vmlinux":"` + sum(kernel) + `",
					"rootfs.ext4.zst":"` + sum(rootfs) + `",
					"firecracker":"` + sum(fc) + `"
				}
			}`))
		case "/vmlinux":
			_, _ = w.Write(kernel)
		case "/rootfs.ext4.zst":
			_, _ = w.Write(rootfs)
		case "/firecracker":
			_, _ = w.Write(fc)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	image, err := cache.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, path := range []string{image.KernelPath, image.RootFSPath, image.FirecrackerPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", path, err)
		}
	}
	if filepath.Base(image.RootFSPath) != "rootfs.ext4.zst" {
		t.Fatalf("rootfs path = %q", image.RootFSPath)
	}
}

func TestVMImageCacheDownloadsOptionalInitrdArtifact(t *testing.T) {
	rootfs := []byte("rootfs")
	kernel := []byte("kernel")
	initrd := []byte("initrd")
	fc := []byte("firecracker")
	sum := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(`{
				"name":"yeet-ubuntu-26.04",
				"version":"ubuntu-26.04-amd64-v1",
				"architecture":"amd64",
				"kernel":"vmlinux",
				"initrd":"initrd.img",
				"rootfs":"rootfs.ext4.zst",
				"firecracker":"firecracker",
				"rootfs_size":2376073216,
				"checksums":{
					"vmlinux":"` + sum(kernel) + `",
					"initrd.img":"` + sum(initrd) + `",
					"rootfs.ext4.zst":"` + sum(rootfs) + `",
					"firecracker":"` + sum(fc) + `"
				}
			}`))
		case "/vmlinux":
			_, _ = w.Write(kernel)
		case "/initrd.img":
			_, _ = w.Write(initrd)
		case "/rootfs.ext4.zst":
			_, _ = w.Write(rootfs)
		case "/firecracker":
			_, _ = w.Write(fc)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	image, err := cache.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if image.InitrdPath == "" || filepath.Base(image.InitrdPath) != "initrd.img" {
		t.Fatalf("initrd path = %q, want downloaded initrd.img", image.InitrdPath)
	}
	if got, err := os.ReadFile(image.InitrdPath); err != nil || string(got) != string(initrd) {
		t.Fatalf("initrd content = %q, %v; want %q", got, err, initrd)
	}
}

func TestResolveVMImagePayload(t *testing.T) {
	wantManifestURL := "https://github.com/yeetrun/yeet-vm-images/releases/latest/download/manifest.json"
	if defaultVMImageManifestURL != wantManifestURL {
		t.Fatalf("default manifest URL = %q, want %q", defaultVMImageManifestURL, wantManifestURL)
	}

	got, err := resolveVMImagePayload(vmUbuntu2604Payload)
	if err != nil {
		t.Fatalf("resolveVMImagePayload: %v", err)
	}
	if got != wantManifestURL {
		t.Fatalf("manifest URL = %q, want %q", got, wantManifestURL)
	}

	_, err = resolveVMImagePayload("vm://debian/13")
	if err == nil {
		t.Fatal("resolveVMImagePayload returned nil error for unsupported payload")
	}
	if !strings.Contains(err.Error(), "unsupported VM image payload") || !strings.Contains(err.Error(), "vm://debian/13") || !strings.Contains(err.Error(), vmUbuntu2604Payload) {
		t.Fatalf("error = %v, want unsupported payload with supported value", err)
	}
}

func TestVMImageCacheInspectMissing(t *testing.T) {
	latest := vmImageTestManifest("ubuntu-26.04-amd64-v2", vmImageTestContents())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()

	root := t.TempDir()
	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), vmUbuntu2604Payload)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !reflect.DeepEqual(inspectedManifest, latest) {
		t.Fatalf("manifest = %#v, want %#v", inspectedManifest, latest)
	}
	if got.State != vmImageCacheMissing {
		t.Fatalf("state = %q, want %q", got.State, vmImageCacheMissing)
	}
	if got.Payload != vmUbuntu2604Payload || got.CachedVersion != "" || got.LatestVersion != latest.Version {
		t.Fatalf("state versions = %#v", got)
	}
	if got.CachePath != filepath.Join(root, latest.Version) {
		t.Fatalf("cache path = %q, want latest version dir", got.CachePath)
	}
	if got.ManifestURL != server.URL+"/manifest.json" {
		t.Fatalf("manifest URL = %q", got.ManifestURL)
	}
}

func TestVMImageCacheInspectStaleWhenCachedVersionDiffers(t *testing.T) {
	contents := vmImageTestContents()
	latest := vmImageTestManifest("ubuntu-26.04-amd64-v2", contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()

	root := t.TempDir()
	cached := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	cachedDir := writeCachedVMImageManifest(t, root, cached)
	writeCachedVMImageArtifacts(t, cachedDir, contents)

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), vmUbuntu2604Payload)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !reflect.DeepEqual(inspectedManifest, latest) {
		t.Fatalf("manifest = %#v, want %#v", inspectedManifest, latest)
	}
	if got.State != vmImageCacheStale {
		t.Fatalf("state = %q, want %q", got.State, vmImageCacheStale)
	}
	if got.CachedVersion != cached.Version || got.LatestVersion != latest.Version {
		t.Fatalf("versions = cached %q latest %q", got.CachedVersion, got.LatestVersion)
	}
	if got.CachePath != filepath.Join(root, latest.Version) {
		t.Fatalf("cache path = %q, want latest version dir", got.CachePath)
	}
}

func TestVMImageCacheInspectStaleWhenLatestArtifactsIncomplete(t *testing.T) {
	contents := vmImageTestContents()
	latest := vmImageTestManifest("ubuntu-26.04-amd64-v2", contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()

	root := t.TempDir()
	latestDir := writeCachedVMImageManifest(t, root, latest)
	if err := os.WriteFile(filepath.Join(latestDir, latest.Kernel), contents[latest.Kernel], 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), vmUbuntu2604Payload)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !reflect.DeepEqual(inspectedManifest, latest) {
		t.Fatalf("manifest = %#v, want %#v", inspectedManifest, latest)
	}
	if got.State != vmImageCacheStale {
		t.Fatalf("state = %q, want %q", got.State, vmImageCacheStale)
	}
	if got.CachedVersion != latest.Version || got.LatestVersion != latest.Version {
		t.Fatalf("versions = cached %q latest %q", got.CachedVersion, got.LatestVersion)
	}
}

func TestVMImageCacheInspectCurrent(t *testing.T) {
	contents := vmImageTestContents()
	latest := vmImageTestManifest("ubuntu-26.04-amd64-v2", contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()

	root := t.TempDir()
	latestDir := writeCachedVMImageManifest(t, root, latest)
	writeCachedVMImageArtifacts(t, latestDir, contents)

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), vmUbuntu2604Payload)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !reflect.DeepEqual(inspectedManifest, latest) {
		t.Fatalf("manifest = %#v, want %#v", inspectedManifest, latest)
	}
	if got.State != vmImageCacheCurrent {
		t.Fatalf("state = %q, want %q", got.State, vmImageCacheCurrent)
	}
	if got.CachedVersion != latest.Version || got.LatestVersion != latest.Version {
		t.Fatalf("versions = cached %q latest %q", got.CachedVersion, got.LatestVersion)
	}
}

func TestCachedVMImageManifestSelectsHighestValidCachedManifest(t *testing.T) {
	root := t.TempDir()
	contents := vmImageTestContents()
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v1", contents))
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v10", contents))
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v2", contents))

	got, dir, ok, err := latestCachedVMImageManifest(root)
	if err != nil {
		t.Fatalf("latestCachedVMImageManifest: %v", err)
	}
	if !ok {
		t.Fatal("latestCachedVMImageManifest found no cached manifest")
	}
	if got.Version != "ubuntu-26.04-amd64-v10" {
		t.Fatalf("version = %q, want highest valid manifest", got.Version)
	}
	if dir != filepath.Join(root, got.Version) {
		t.Fatalf("dir = %q, want matching cache dir", dir)
	}
}

func TestCachedVMImageManifestIgnoresInvalidCachedManifests(t *testing.T) {
	root := t.TempDir()
	invalidDir := filepath.Join(root, "ubuntu-26.04-amd64-v99")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("mkdir invalid cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, "manifest.json"), []byte(`{"version":"ubuntu-26.04-amd64-v99"}`), 0o644); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	contents := vmImageTestContents()
	valid := vmImageTestManifest("ubuntu-26.04-amd64-v2", contents)
	writeCachedVMImageManifest(t, root, valid)

	got, _, ok, err := latestCachedVMImageManifest(root)
	if err != nil {
		t.Fatalf("latestCachedVMImageManifest: %v", err)
	}
	if !ok {
		t.Fatal("latestCachedVMImageManifest found no cached manifest")
	}
	if got.Version != valid.Version {
		t.Fatalf("version = %q, want valid manifest", got.Version)
	}
}

func TestCachedVMImageArtifactsReady(t *testing.T) {
	dir := t.TempDir()
	contents := vmImageTestContents()
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v2", contents)
	manifest.Initrd = "initrd.img"
	contents[manifest.Initrd] = []byte("initrd")
	manifest.Checksums[manifest.Initrd] = testSHA256Hex(contents[manifest.Initrd])

	if cachedVMImageArtifactsReady(dir, manifest) {
		t.Fatal("cachedVMImageArtifactsReady = true with no artifacts")
	}
	for _, name := range manifest.artifactNames() {
		if err := os.WriteFile(filepath.Join(dir, name), contents[name], 0o644); err != nil {
			t.Fatalf("write artifact %s: %v", name, err)
		}
	}
	if !cachedVMImageArtifactsReady(dir, manifest) {
		t.Fatal("cachedVMImageArtifactsReady = false with matching artifacts")
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.RootFS), []byte("corrupt"), 0o644); err != nil {
		t.Fatalf("corrupt rootfs: %v", err)
	}
	if cachedVMImageArtifactsReady(dir, manifest) {
		t.Fatal("cachedVMImageArtifactsReady = true with checksum mismatch")
	}
}

func TestVMImageCacheArtifactURLFromLatestManifestRoute(t *testing.T) {
	cache := vmImageCache{ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/latest/download/manifest.json"}
	got, err := cache.artifactURL("vmlinux")
	if err != nil {
		t.Fatalf("artifactURL: %v", err)
	}
	want := "https://github.com/yeetrun/yeet-vm-images/releases/latest/download/vmlinux"
	if got != want {
		t.Fatalf("artifact URL = %q, want %q", got, want)
	}
}

func TestEnsureVMImageAssetPreparesCompressedRootFS(t *testing.T) {
	emptySum := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(`{
				"name":"yeet-ubuntu-26.04",
				"version":"ubuntu-26.04-amd64-v1",
				"architecture":"amd64",
				"kernel":"vmlinux",
				"rootfs":"rootfs.ext4.zst",
				"firecracker":"firecracker",
				"rootfs_size":2147483648,
				"checksums":{
					"vmlinux":"` + emptySum + `",
					"rootfs.ext4.zst":"` + emptySum + `",
					"firecracker":"` + emptySum + `"
				}
			}`))
		case "/vmlinux", "/rootfs.ext4.zst", "/firecracker":
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	versionDir := filepath.Join(root, "ubuntu-26.04-amd64-v1")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatalf("mkdir version dir: %v", err)
	}

	oldPrepare := prepareVMRootFSFunc
	t.Cleanup(func() { prepareVMRootFSFunc = oldPrepare })
	var gotSource string
	prepareVMRootFSFunc = func(_ context.Context, source string) (string, error) {
		gotSource = source
		return strings.TrimSuffix(source, ".zst"), nil
	}

	asset, err := ensureVMImageAsset(context.Background(), vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"})
	if err != nil {
		t.Fatalf("ensureVMImageAsset: %v", err)
	}
	if gotSource != filepath.Join(versionDir, "rootfs.ext4.zst") {
		t.Fatalf("prepare rootfs source = %q", gotSource)
	}
	if asset.DiskRootFSPath() != filepath.Join(versionDir, "rootfs.ext4") {
		t.Fatalf("disk rootfs = %q, want decompressed path", asset.DiskRootFSPath())
	}
}

func TestPrepareVMRootFSRunsZstdForCompressedRootFS(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "rootfs.ext4.zst")
	if err := os.WriteFile(source, []byte("compressed"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	oldRunner := vmRootFSDecompressRunner
	t.Cleanup(func() { vmRootFSDecompressRunner = oldRunner })
	var gotArgs []string
	vmRootFSDecompressRunner = func(_ context.Context, name string, args ...string) error {
		gotArgs = append([]string{name}, args...)
		out := args[len(args)-2]
		return os.WriteFile(out, []byte("rootfs"), 0o644)
	}

	got, err := prepareVMRootFS(context.Background(), source)
	if err != nil {
		t.Fatalf("prepareVMRootFS: %v", err)
	}
	if got != filepath.Join(root, "rootfs.ext4") {
		t.Fatalf("prepared path = %q", got)
	}
	if !reflect.DeepEqual(gotArgs[:4], []string{"zstd", "-d", "-f", "--no-progress"}) {
		t.Fatalf("zstd args = %#v", gotArgs)
	}
}

func TestVMImageCacheRejectsChecksumMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(`{
				"name":"yeet-ubuntu-26.04",
				"version":"ubuntu-26.04-amd64-v1",
				"architecture":"amd64",
				"kernel":"vmlinux",
				"rootfs":"rootfs.ext4.zst",
				"firecracker":"firecracker",
				"rootfs_size":2147483648,
				"checksums":{
					"vmlinux":"0000000000000000000000000000000000000000000000000000000000000000",
					"rootfs.ext4.zst":"0000000000000000000000000000000000000000000000000000000000000000",
					"firecracker":"0000000000000000000000000000000000000000000000000000000000000000"
				}
			}`))
		case "/vmlinux", "/rootfs.ext4.zst", "/firecracker":
			_, _ = w.Write([]byte("wrong"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	_, err := cache.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure returned nil error")
	}
	if !strings.Contains(err.Error(), "vmlinux") || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want vmlinux checksum mismatch", err)
	}
}

func TestVMImageCachePreservesCachedArtifactWhenRefreshChecksumFails(t *testing.T) {
	cached := []byte("cached kernel")
	replacement := []byte("replacement kernel")
	corrupt := []byte("corrupt replacement")
	sum := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(`{
				"name":"yeet-ubuntu-26.04",
				"version":"ubuntu-26.04-amd64-v1",
				"architecture":"amd64",
				"kernel":"vmlinux",
				"rootfs":"rootfs.ext4.zst",
				"firecracker":"firecracker",
				"rootfs_size":2147483648,
				"checksums":{
					"vmlinux":"` + sum(replacement) + `",
					"rootfs.ext4.zst":"` + sum([]byte("rootfs")) + `",
					"firecracker":"` + sum([]byte("firecracker")) + `"
				}
			}`))
		case "/vmlinux":
			_, _ = w.Write(corrupt)
		case "/rootfs.ext4.zst":
			_, _ = w.Write([]byte("rootfs"))
		case "/firecracker":
			_, _ = w.Write([]byte("firecracker"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	kernelPath := filepath.Join(root, "ubuntu-26.04-amd64-v1", "vmlinux")
	if err := os.MkdirAll(filepath.Dir(kernelPath), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(kernelPath, cached, 0o644); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	_, err := cache.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure returned nil error")
	}
	if !strings.Contains(err.Error(), "vmlinux") || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want vmlinux checksum mismatch", err)
	}
	got, err := os.ReadFile(kernelPath)
	if err != nil {
		t.Fatalf("read cached kernel: %v", err)
	}
	if string(got) != string(cached) {
		t.Fatalf("cached kernel = %q, want %q", got, cached)
	}
}

func TestVMImageCacheRejectsUnsafeManifestVersion(t *testing.T) {
	artifact := []byte("artifact")
	sum := sha256.Sum256(artifact)
	checksum := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(`{
				"name":"yeet-ubuntu-26.04",
				"version":"../outside-cache",
				"architecture":"amd64",
				"kernel":"vmlinux",
				"rootfs":"rootfs.ext4.zst",
				"firecracker":"firecracker",
				"rootfs_size":2147483648,
				"checksums":{
					"vmlinux":"` + checksum + `",
					"rootfs.ext4.zst":"` + checksum + `",
					"firecracker":"` + checksum + `"
				}
			}`))
		case "/vmlinux", "/rootfs.ext4.zst", "/firecracker":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	_, err := cache.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure returned nil error")
	}
	if !strings.Contains(err.Error(), "version") || !strings.Contains(err.Error(), "cache directory") {
		t.Fatalf("error = %v, want unsafe version error", err)
	}
}

func TestVMImageCacheRejectsUnsafeArtifactNames(t *testing.T) {
	tests := []string{
		"",
		".",
		"../vmlinux",
		"/vmlinux",
		"dir/vmlinux",
		`dir\vmlinux`,
		"./vmlinux",
		" vmlinux",
		"vmlinux ",
		"manifest.json",
		"vm linux",
		"vmlinux@sha",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateVMImageArtifactName(name)
			if err == nil {
				t.Fatal("validateVMImageArtifactName returned nil error")
			}
		})
	}
}

func TestVMImageCacheRejectsUnsafeVersionNames(t *testing.T) {
	tests := []string{".", " ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v1 "}
	for _, version := range tests {
		t.Run(version, func(t *testing.T) {
			err := validateVMImageCacheDirName(version)
			if err == nil {
				t.Fatal("validateVMImageCacheDirName returned nil error")
			}
		})
	}
}

func vmImageTestContents() map[string][]byte {
	return map[string][]byte{
		"vmlinux":         []byte("kernel"),
		"rootfs.ext4.zst": []byte("rootfs"),
		"firecracker":     []byte("firecracker"),
	}
}

func vmImageTestManifest(version string, contents map[string][]byte) vmImageManifest {
	return vmImageManifest{
		Name:         "yeet-ubuntu-26.04",
		Version:      version,
		Architecture: "amd64",
		Kernel:       "vmlinux",
		RootFS:       "rootfs.ext4.zst",
		Firecracker:  "firecracker",
		RootFSSize:   2147483648,
		Checksums: map[string]string{
			"vmlinux":         testSHA256Hex(contents["vmlinux"]),
			"rootfs.ext4.zst": testSHA256Hex(contents["rootfs.ext4.zst"]),
			"firecracker":     testSHA256Hex(contents["firecracker"]),
		},
	}
}

func writeCachedVMImageManifest(t *testing.T, root string, manifest vmImageManifest) string {
	t.Helper()
	dir := filepath.Join(root, manifest.Version)
	if err := writeManifestFile(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write cached manifest: %v", err)
	}
	return dir
}

func writeCachedVMImageArtifacts(t *testing.T, dir string, contents map[string][]byte) {
	t.Helper()
	for name, content := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("write cached artifact %s: %v", name, err)
		}
	}
}

func testSHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
