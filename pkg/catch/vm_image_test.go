// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
				"version":"ubuntu-26.04-amd64-v0",
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

func TestEnsureVMImageAssetPreparesCompressedRootFS(t *testing.T) {
	emptySum := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write([]byte(`{
				"name":"yeet-ubuntu-26.04",
				"version":"ubuntu-26.04-amd64-v0",
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
	versionDir := filepath.Join(root, "ubuntu-26.04-amd64-v0")
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
				"version":"ubuntu-26.04-amd64-v0",
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
				"version":"ubuntu-26.04-amd64-v0",
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
	kernelPath := filepath.Join(root, "ubuntu-26.04-amd64-v0", "vmlinux")
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
	tests := []string{".", " ubuntu-26.04-amd64-v0", "ubuntu-26.04-amd64-v0 "}
	for _, version := range tests {
		t.Run(version, func(t *testing.T) {
			err := validateVMImageCacheDirName(version)
			if err == nil {
				t.Fatal("validateVMImageCacheDirName returned nil error")
			}
		})
	}
}
