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
	"strconv"
	"strings"
	"sync"
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

func TestVMImageCacheDownloadsOptionalJailerArtifact(t *testing.T) {
	contents := vmImageTestContents()
	contents["jailer"] = []byte("jailer")
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	manifest.Jailer = "jailer"
	manifest.Checksums[manifest.Jailer] = testSHA256Hex(contents[manifest.Jailer])
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			_, _ = w.Write(manifestRaw)
			return
		}
		if content, ok := contents[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			_, _ = w.Write(content)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	paths, err := cache.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if filepath.Base(paths.JailerPath) != "jailer" {
		t.Fatalf("jailer path = %q, want downloaded jailer", paths.JailerPath)
	}
	info, err := os.Stat(paths.JailerPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("jailer mode = %o, want 755", info.Mode().Perm())
	}
	if !reflect.DeepEqual(manifest.artifactNames(), []string{"vmlinux", "rootfs.ext4.zst", "firecracker", "jailer"}) {
		t.Fatalf("artifact names = %#v", manifest.artifactNames())
	}
}

func TestVMImageAssetRequireJailer(t *testing.T) {
	dir := t.TempDir()
	jailer := filepath.Join(dir, "jailer")
	if err := os.WriteFile(jailer, []byte("jailer"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := vmImageAsset{Paths: vmImagePaths{JailerPath: jailer}}
	got, err := asset.RequireJailer()
	if err != nil {
		t.Fatalf("RequireJailer: %v", err)
	}
	if got != jailer {
		t.Fatalf("jailer = %q, want %q", got, jailer)
	}

	_, err = (vmImageAsset{}).RequireJailer()
	if err == nil || !strings.Contains(err.Error(), "matching Firecracker jailer") {
		t.Fatalf("missing jailer error = %v", err)
	}
}

func TestVMImageCacheRetriesTemporaryManifestFetchFailure(t *testing.T) {
	oldDelay := vmImageFetchRetryDelay
	vmImageFetchRetryDelay = 0
	t.Cleanup(func() { vmImageFetchRetryDelay = oldDelay })

	contents := vmImageTestContents()
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var manifestAttempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			manifestAttempts++
			if manifestAttempts == 1 {
				http.Error(w, "temporary gateway timeout", http.StatusGatewayTimeout)
				return
			}
			_, _ = w.Write(manifestRaw)
		default:
			if content, ok := contents[strings.TrimPrefix(r.URL.Path, "/")]; ok {
				_, _ = w.Write(content)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	image, err := cache.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	var gotManifest vmImageManifest
	rawManifest, err := os.ReadFile(image.Manifest)
	if err != nil {
		t.Fatalf("read cached manifest: %v", err)
	}
	if err := json.Unmarshal(rawManifest, &gotManifest); err != nil {
		t.Fatalf("decode cached manifest: %v", err)
	}
	if gotManifest.Version != manifest.Version {
		t.Fatalf("manifest version = %q, want %q", gotManifest.Version, manifest.Version)
	}
	if manifestAttempts != 2 {
		t.Fatalf("manifest attempts = %d, want 2", manifestAttempts)
	}
}

func TestVMImageCacheDoesNotRetryPermanentManifestFetchFailure(t *testing.T) {
	oldDelay := vmImageFetchRetryDelay
	vmImageFetchRetryDelay = 0
	t.Cleanup(func() { vmImageFetchRetryDelay = oldDelay })

	var manifestAttempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestAttempts++
		http.NotFound(w, r)
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	_, err := cache.fetchManifest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("fetchManifest error = %v, want 404", err)
	}
	if manifestAttempts != 1 {
		t.Fatalf("manifest attempts = %d, want 1", manifestAttempts)
	}
}

func TestVMImageCacheRetriesTemporaryArtifactFetchFailure(t *testing.T) {
	oldDelay := vmImageFetchRetryDelay
	vmImageFetchRetryDelay = 0
	t.Cleanup(func() { vmImageFetchRetryDelay = oldDelay })

	contents := vmImageTestContents()
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var rootfsAttempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(manifestRaw)
		case "/rootfs.ext4.zst":
			rootfsAttempts++
			if rootfsAttempts == 1 {
				http.Error(w, "temporary gateway timeout", http.StatusGatewayTimeout)
				return
			}
			_, _ = w.Write(contents["rootfs.ext4.zst"])
		default:
			if content, ok := contents[strings.TrimPrefix(r.URL.Path, "/")]; ok {
				_, _ = w.Write(content)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	image, err := cache.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if image.RootFSPath == "" {
		t.Fatal("rootfs path is empty")
	}
	if rootfsAttempts != 2 {
		t.Fatalf("rootfs attempts = %d, want 2", rootfsAttempts)
	}
}

func TestVMImageCacheUsesYeetUserAgent(t *testing.T) {
	contents := vmImageTestContents()
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.UserAgent())
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(manifestRaw)
		default:
			if content, ok := contents[strings.TrimPrefix(r.URL.Path, "/")]; ok {
				_, _ = w.Write(content)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	if _, err := cache.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("saw no VM image HTTP requests")
	}
	for _, ua := range seen {
		if ua != vmImageHTTPUserAgent {
			t.Fatalf("User-Agent = %q, want %q; all seen: %#v", ua, vmImageHTTPUserAgent, seen)
		}
	}
}

func TestVMImageCachePreservesImagePolicyMetadata(t *testing.T) {
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
				"version":"ubuntu-26.04-amd64-v4",
				"architecture":"x86_64",
				"image_profile":"fast",
				"kernel_policy":"yeet-managed",
				"guest_init":"/usr/local/lib/yeet-vm/yeet-init",
				"snap_support":false,
				"kernel":"vmlinux",
				"rootfs":"rootfs.ext4.zst",
				"firecracker":"firecracker",
				"rootfs_size":2376073216,
				"kernel_version":"linux-7.0-yeet",
				"provenance":{
					"kernel_source":"yeet-managed"
				},
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

	root := t.TempDir()
	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	image, err := cache.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(image.Dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read cached manifest: %v", err)
	}
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode cached manifest: %v", err)
	}
	if manifest.ImageProfile != "fast" || manifest.KernelPolicy != "yeet-managed" || manifest.GuestInit != vmGuestInitPath || manifest.KernelVersion != "linux-7.0-yeet" {
		t.Fatalf("cached manifest policy metadata = %#v", manifest)
	}
	if manifest.SnapSupport == nil || *manifest.SnapSupport {
		t.Fatalf("snap support = %#v, want false", manifest.SnapSupport)
	}
	if manifest.Provenance["kernel_source"] != "yeet-managed" {
		t.Fatalf("provenance = %#v, want kernel source", manifest.Provenance)
	}
	if manifest.Initrd != "" || image.InitrdPath != "" {
		t.Fatalf("initrd = manifest %q path %q, want omitted", manifest.Initrd, image.InitrdPath)
	}
}

func TestVMImageManifestPreservesNixOSFields(t *testing.T) {
	raw := []byte(`{
		"name":"yeet-nixos-26.05",
		"version":"nixos-26.05-amd64-v1",
		"architecture":"x86_64",
		"image_profile":"fast",
		"distro":"nixos",
		"distro_version":"26.05",
		"default_user":"nixos",
		"kernel_policy":"yeet-managed",
		"guest_init":"/usr/local/lib/yeet-vm/yeet-init",
		"guest_system_init":"/run/current-system/init",
		"metadata_driver":"nixos",
		"snap_support":false,
		"kernel":"vmlinux",
		"rootfs":"rootfs.ext4.zst",
		"firecracker":"firecracker",
		"rootfs_size":2147483648,
		"checksums":{
			"vmlinux":"0000000000000000000000000000000000000000000000000000000000000000",
			"rootfs.ext4.zst":"1111111111111111111111111111111111111111111111111111111111111111",
			"firecracker":"2222222222222222222222222222222222222222222222222222222222222222"
		}
	}`)
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if manifest.Distro != "nixos" || manifest.DistroVersion != "26.05" || manifest.MetadataDriver != "nixos" {
		t.Fatalf("manifest NixOS metadata = %#v", manifest)
	}
	if manifest.DefaultUserOr("ubuntu") != "nixos" {
		t.Fatalf("default user = %q", manifest.DefaultUserOr("ubuntu"))
	}
	if manifest.GuestSystemInitOr("/usr/lib/systemd/systemd") != "/run/current-system/init" {
		t.Fatalf("guest system init = %q", manifest.GuestSystemInitOr(""))
	}
}

func TestVMImageManifestPreservesKernelAutomationFields(t *testing.T) {
	raw := []byte(`{
		"name":"yeet-ubuntu-26.04",
		"version":"ubuntu-26.04-amd64-kernel-7.1.1-v16",
		"image_revision":16,
		"architecture":"x86_64",
		"image_profile":"fast",
		"kernel_policy":"yeet-managed",
		"guest_init":"/usr/local/lib/yeet-vm/yeet-init",
		"snap_support":false,
		"kernel":"vmlinux",
		"rootfs":"rootfs.ext4.zst",
		"firecracker":"firecracker",
		"rootfs_size":2147483648,
		"kernel_version":"linux-7.1.1-yeet",
		"upstream_kernel_version":"7.1.1",
		"kernel_source_url":"https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz",
		"kernel_source_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"checksums":{
			"vmlinux":"0000000000000000000000000000000000000000000000000000000000000000",
			"rootfs.ext4.zst":"1111111111111111111111111111111111111111111111111111111111111111",
			"firecracker":"2222222222222222222222222222222222222222222222222222222222222222"
		}
	}`)
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if manifest.ImageRevision != 16 {
		t.Fatalf("image revision = %d, want 16", manifest.ImageRevision)
	}
	if manifest.KernelVersion != "linux-7.1.1-yeet" {
		t.Fatalf("kernel version = %q", manifest.KernelVersion)
	}
	if manifest.UpstreamKernelVersion != "7.1.1" {
		t.Fatalf("upstream kernel version = %q", manifest.UpstreamKernelVersion)
	}
	if manifest.KernelSourceURL != "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz" {
		t.Fatalf("kernel source URL = %q", manifest.KernelSourceURL)
	}
	if manifest.KernelSourceSHA256 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("kernel source sha256 = %q", manifest.KernelSourceSHA256)
	}
}

func TestVMImageManifestRejectsInvalidRuntimeMetadata(t *testing.T) {
	base := vmImageTestManifest("ubuntu-26.04-amd64-v1", vmImageTestContents())
	tests := []struct {
		name string
		mut  func(*vmImageManifest)
		want string
	}{
		{
			name: "default user",
			mut:  func(m *vmImageManifest) { m.DefaultUser = "bad user" },
			want: "default_user",
		},
		{
			name: "metadata driver",
			mut:  func(m *vmImageManifest) { m.MetadataDriver = "freebsd" },
			want: "metadata_driver",
		},
		{
			name: "guest system init",
			mut:  func(m *vmImageManifest) { m.GuestSystemInit = "run/current-system/init" },
			want: "guest_system_init",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := base
			tt.mut(&manifest)
			err := manifest.validate()
			if err == nil {
				t.Fatal("validate returned nil error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validate error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVMImageSupportsFastBootRequiresGuestInitCapability(t *testing.T) {
	if vmImageSupportsFastBoot(vmImageManifest{GuestInit: ""}) {
		t.Fatal("vmImageSupportsFastBoot true without guest_init")
	}
	if vmImageSupportsFastBoot(vmImageManifest{GuestInit: "/usr/local/lib/yeet-vm/other-init"}) {
		t.Fatal("vmImageSupportsFastBoot true for different guest_init")
	}
	if !vmImageSupportsFastBoot(vmImageManifest{GuestInit: vmGuestInitPath}) {
		t.Fatal("vmImageSupportsFastBoot false for yeet guest init")
	}
}

func TestVMImageDownloadUpdatesProgressDetail(t *testing.T) {
	content := []byte(strings.Repeat("x", 2048))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vmlinux" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	ui := &recordingVMImageProgressUI{}
	progress := newByteProgress(0)
	cache := vmImageCache{Root: t.TempDir()}
	dst := filepath.Join(t.TempDir(), "vmlinux")
	if err := cache.downloadVerifiedFile(context.Background(), server.URL+"/vmlinux", dst, "vmlinux", testSHA256Hex(content), progress, ui); err != nil {
		t.Fatalf("downloadVerifiedFile: %v", err)
	}
	if got := progress.seen.Load(); got != int64(len(content)) {
		t.Fatalf("download progress seen = %d, want %d", got, len(content))
	}
	assertVMImageProgressDetail(t, ui.detailsSnapshot(), "100% 2.00 KB/2.00 KB")
}

func TestDefaultVMImageEnsureFuncUpdatesProgress(t *testing.T) {
	contents := vmImageTestContents()
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	server := newVMImageArtifactTestServer(t, manifest, contents)
	defer server.Close()
	stubVMImageCatalogFetch(t, vmImageTestCatalog())

	oldPrepare := prepareVMRootFSFunc
	t.Cleanup(func() { prepareVMRootFSFunc = oldPrepare })
	prepareVMRootFSFunc = func(_ context.Context, source string) (string, error) {
		return source, nil
	}

	ui := &recordingVMImageProgressUI{}
	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json"}
	asset, err := vmImageEnsureFunc(context.Background(), cache, testUbuntuVMPayload, ui)
	if err != nil {
		t.Fatalf("vmImageEnsureFunc: %v", err)
	}
	if asset.Manifest.Version != manifest.Version {
		t.Fatalf("asset version = %q, want %q", asset.Manifest.Version, manifest.Version)
	}
	if got := ui.stepNames(); !reflect.DeepEqual(got, []string{"Download VM image"}) {
		t.Fatalf("progress steps = %#v, want Download VM image", got)
	}
	if got := ui.startStopCounts(); got.starts != 1 || got.stops != 1 {
		t.Fatalf("progress start/stop = %#v, want one start and stop", got)
	}
	if got := ui.failuresSnapshot(); len(got) != 0 {
		t.Fatalf("progress failures = %#v, want none", got)
	}
	if got := ui.doneSnapshot(); len(got) != 1 || !strings.Contains(got[0], " @ ") {
		t.Fatalf("progress done = %#v, want final byte rate detail", got)
	}
	assertVMImageProgressDetail(t, ui.detailsSnapshot(), "100%")
}

func TestDefaultVMImageEnsureFuncSkipsDownloadProgressWhenCurrent(t *testing.T) {
	contents := vmImageTestContents()
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	server := newVMImageArtifactTestServer(t, manifest, contents)
	defer server.Close()
	stubVMImageCatalogFetch(t, vmImageTestCatalog())

	oldPrepare := prepareVMRootFSFunc
	t.Cleanup(func() { prepareVMRootFSFunc = oldPrepare })
	prepareVMRootFSFunc = func(_ context.Context, source string) (string, error) {
		return source, nil
	}

	root := t.TempDir()
	dir := writeCachedVMImageManifest(t, root, manifest)
	writeCachedVMImageArtifacts(t, dir, contents)

	ui := &recordingVMImageProgressUI{}
	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	asset, err := vmImageEnsureFunc(context.Background(), cache, testUbuntuVMPayload, ui)
	if err != nil {
		t.Fatalf("vmImageEnsureFunc: %v", err)
	}
	if asset.Manifest.Version != manifest.Version {
		t.Fatalf("asset version = %q, want %q", asset.Manifest.Version, manifest.Version)
	}
	if got := ui.stepNames(); len(got) != 0 {
		t.Fatalf("progress steps = %#v, want none for current cache", got)
	}
	if got := ui.startStopCounts(); got.starts != 0 || got.stops != 0 {
		t.Fatalf("progress start/stop = %#v, want none for current cache", got)
	}
	if got := ui.doneSnapshot(); len(got) != 0 {
		t.Fatalf("progress done = %#v, want none for current cache", got)
	}
}

func TestResolveVMImagePayloadUsesCatalog(t *testing.T) {
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://debian/13",
			Name:           "Debian 13",
			Architecture:   "amd64",
			ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json",
			VersionPrefix:  "debian-13-amd64-",
			DefaultUser:    "debian",
			MetadataDriver: "ubuntu",
		},
	}}
	source, err := resolveVMImagePayloadFromCatalog(" vm://debian/13 ", catalog)
	if err != nil {
		t.Fatalf("resolveVMImagePayloadFromCatalog: %v", err)
	}
	if source.Kind != vmImageSourceRemote || source.ManifestURL != catalog.Images[0].ManifestURL || source.Family.Payload != "vm://debian/13" {
		t.Fatalf("source = %#v", source)
	}
}

func TestResolveVMImagePayloadFromCatalogLocal(t *testing.T) {
	source, err := resolveVMImagePayloadFromCatalog("vm://foo/bar", vmImageTestCatalog())
	if err != nil {
		t.Fatalf("resolveVMImagePayloadFromCatalog local: %v", err)
	}
	if source.Kind != vmImageSourceLocal || source.LocalName != "foo/bar" {
		t.Fatalf("source = %#v, want local foo/bar", source)
	}
}

func TestResolveVMImagePayloadFromCatalogAllowsVersionLikeLocalName(t *testing.T) {
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://ubuntu/26.04",
			Name:           "Ubuntu",
			Architecture:   "amd64",
			ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
			VersionPrefix:  "ubuntu-26.04-amd64-",
			DefaultUser:    "ubuntu",
			MetadataDriver: "ubuntu",
		},
	}}
	source, err := resolveVMImagePayloadFromCatalog("vm://debian/13", catalog)
	if err != nil {
		t.Fatalf("resolveVMImagePayloadFromCatalog local: %v", err)
	}
	if source.Kind != vmImageSourceLocal || source.LocalName != "debian/13" {
		t.Fatalf("source = %#v, want local debian/13", source)
	}
}

func TestResolveVMImagePayloadRejectsUnsupportedPayloadWithCatalogList(t *testing.T) {
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://ubuntu/26.04",
			Name:           "Ubuntu",
			Architecture:   "amd64",
			ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
			VersionPrefix:  "ubuntu-26.04-amd64-",
			DefaultUser:    "ubuntu",
			MetadataDriver: "ubuntu",
		},
	}}
	_, err := resolveVMImagePayloadFromCatalog("oci://unknown/1", catalog)
	if err == nil || !strings.Contains(err.Error(), "supported: vm://ubuntu/26.04 or imported vm://<name>") {
		t.Fatalf("resolve error = %v", err)
	}
}

func TestResolveVMImagePayloadRejectsCatalogReservedLocalPrefixes(t *testing.T) {
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://debian/13",
			Name:           "Debian 13",
			Architecture:   "amd64",
			ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json",
			VersionPrefix:  "debian-13-amd64-",
			DefaultUser:    "debian",
			MetadataDriver: "ubuntu",
		},
	}}
	_, err := resolveVMImagePayloadFromCatalog("vm://debian/custom", catalog)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("resolve error = %v, want reserved prefix", err)
	}
	if !strings.Contains(err.Error(), "supported: vm://debian/13 or imported vm://<name>") {
		t.Fatalf("resolve error = %v, want supported catalog payloads", err)
	}
}

func TestInspectRemoteRejectsManifestOutsideCatalogFamily(t *testing.T) {
	contents := vmImageTestContents()
	manifest := vmImageTestManifest("debian-13-amd64-v1", contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json", Client: server.Client()}
	family := vmImageCatalogImage{Payload: "vm://ubuntu/26.04", ManifestURL: server.URL + "/manifest.json", VersionPrefix: "ubuntu-26.04-amd64-"}
	_, _, err := cache.inspectRemote(context.Background(), "vm://ubuntu/26.04", family)
	if err == nil || !strings.Contains(err.Error(), "does not match catalog version prefix") {
		t.Fatalf("inspect error = %v", err)
	}
}

func TestEnsureVMImageAssetWithProgressRejectsManifestOutsideCatalogFamily(t *testing.T) {
	contents := vmImageTestContents()
	manifest := vmImageTestManifest("debian-13-amd64-v1", contents)
	server := newVMImageArtifactTestServer(t, manifest, contents)
	defer server.Close()

	stubVMImageCatalogFetch(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://ubuntu/26.04",
			Name:           "Ubuntu",
			Architecture:   "amd64",
			ManifestURL:    server.URL + "/manifest.json",
			VersionPrefix:  "ubuntu-26.04-amd64-",
			DefaultUser:    "ubuntu",
			MetadataDriver: "ubuntu",
			Default:        true,
		},
	}})
	oldPrepare := prepareVMRootFSFunc
	prepareVMRootFSFunc = func(_ context.Context, source string) (string, error) {
		return source, nil
	}
	t.Cleanup(func() { prepareVMRootFSFunc = oldPrepare })

	cache := vmImageCache{Root: t.TempDir(), Client: server.Client()}
	_, err := ensureVMImageAssetWithProgress(context.Background(), cache, "vm://ubuntu/26.04", nil)
	if err == nil || !strings.Contains(err.Error(), "does not match catalog version prefix") {
		t.Fatalf("ensure error = %v", err)
	}
}

func TestResolveVMImagePayloadLocal(t *testing.T) {
	source, err := resolveVMImagePayload("vm://foo/bar")
	if err != nil {
		t.Fatalf("resolveVMImagePayload local: %v", err)
	}
	if source.Kind != vmImageSourceLocal || source.LocalName != "foo/bar" {
		t.Fatalf("source = %#v, want local foo/bar", source)
	}
}

func TestResolveVMImagePayloadWithoutCatalogTreatsCatalogStylePayloadAsLocal(t *testing.T) {
	source, err := resolveVMImagePayload(testUbuntuVMPayload)
	if err != nil {
		t.Fatalf("resolveVMImagePayload local: %v", err)
	}
	if source.Kind != vmImageSourceLocal || source.LocalName != "ubuntu/26.04" {
		t.Fatalf("source = %#v, want local ubuntu/26.04", source)
	}
}

func TestResolveVMImagePayloadRejectsInvalidLocalName(t *testing.T) {
	_, err := resolveVMImagePayload("vm://Foo/Bar")
	if err == nil || !strings.Contains(err.Error(), "invalid local VM image name") {
		t.Fatalf("error = %v, want invalid local image name", err)
	}
}

func TestEnsureVMImageAssetWithProgressUsesLocalRef(t *testing.T) {
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	root := t.TempDir()
	importer := localVMImageImporter{
		CacheRoot: root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	asset, err := ensureVMImageAssetWithProgress(context.Background(), vmImageCache{Root: root}, "vm://foo/bar", nil)
	if err != nil {
		t.Fatalf("ensureVMImageAssetWithProgress: %v", err)
	}
	if asset.Manifest.Version != ref.Version {
		t.Fatalf("version = %q, want %q", asset.Manifest.Version, ref.Version)
	}
}

func TestEnsureVMImageAssetWithProgressUsesLocalRefWhenCatalogFetchFails(t *testing.T) {
	root := t.TempDir()
	importer := localVMImageImporter{
		CacheRoot: root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	orig := fetchVMImageCatalogFunc
	fetchVMImageCatalogFunc = func(context.Context, *http.Client) (vmImageCatalog, error) {
		return vmImageCatalog{}, context.Canceled
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })

	asset, err := ensureVMImageAssetWithProgress(context.Background(), vmImageCache{Root: root}, "vm://foo/bar", nil)
	if err != nil {
		t.Fatalf("ensureVMImageAssetWithProgress: %v", err)
	}
	if asset.Manifest.Version != ref.Version {
		t.Fatalf("version = %q, want %q", asset.Manifest.Version, ref.Version)
	}
}

func TestEnsureVMImageAssetWithProgressReportsUnknownLocalRef(t *testing.T) {
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	_, err := ensureVMImageAssetWithProgress(context.Background(), vmImageCache{Root: t.TempDir()}, "vm://foo/bar", nil)
	if err == nil || !strings.Contains(err.Error(), "import it with `yeet vm images import foo/bar`") {
		t.Fatalf("error = %v, want import hint", err)
	}
}

func TestVMImageCacheInspectLocalRefWhenCatalogFetchFails(t *testing.T) {
	root := t.TempDir()
	importer := localVMImageImporter{
		CacheRoot: root,
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	orig := fetchVMImageCatalogFunc
	fetchVMImageCatalogFunc = func(context.Context, *http.Client) (vmImageCatalog, error) {
		return vmImageCatalog{}, context.Canceled
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })

	state, manifest, err := (vmImageCache{Root: root}).Inspect(context.Background(), "vm://foo/bar")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Payload != "vm://foo/bar" || state.State != vmImageCacheCurrent || state.CachedVersion != ref.Version || state.LatestVersion != ref.Version {
		t.Fatalf("state = %#v, want current local ref", state)
	}
	if manifest.Version != ref.Version {
		t.Fatalf("manifest version = %q, want %q", manifest.Version, ref.Version)
	}
}

func TestVMImageCacheInspectMissing(t *testing.T) {
	latest := vmImageTestManifest(testUbuntuVMImageVersion, vmImageTestContents())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()
	stubVMImageCatalogFetch(t, vmImageTestCatalog())

	root := t.TempDir()
	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), testUbuntuVMPayload)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !reflect.DeepEqual(inspectedManifest, latest) {
		t.Fatalf("manifest = %#v, want %#v", inspectedManifest, latest)
	}
	if got.State != vmImageCacheMissing {
		t.Fatalf("state = %q, want %q", got.State, vmImageCacheMissing)
	}
	if got.Payload != testUbuntuVMPayload || got.CachedVersion != "" || got.LatestVersion != latest.Version {
		t.Fatalf("state versions = %#v", got)
	}
	if got.CachePath != filepath.Join(root, latest.Version) {
		t.Fatalf("cache path = %q, want latest version dir", got.CachePath)
	}
	if got.ManifestURL != server.URL+"/manifest.json" {
		t.Fatalf("manifest URL = %q", got.ManifestURL)
	}
}

func TestVMImageCacheInspectReturnsCatalogFetchError(t *testing.T) {
	contents := vmImageTestContents()
	manifest := vmImageTestManifest(testUbuntuVMImageVersion, contents)
	server := newVMImageArtifactTestServer(t, manifest, contents)
	defer server.Close()

	orig := fetchVMImageCatalogFunc
	fetchVMImageCatalogFunc = func(context.Context, *http.Client) (vmImageCatalog, error) {
		return vmImageCatalog{}, context.Canceled
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })

	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json", Client: server.Client()}
	_, _, err := cache.Inspect(context.Background(), testUbuntuVMPayload)
	if err != context.Canceled {
		t.Fatalf("Inspect error = %v, want catalog fetch error", err)
	}
}

func TestVMImageCacheInspectStaleWhenCachedVersionDiffers(t *testing.T) {
	contents := vmImageTestContents()
	latest := vmImageTestManifest("ubuntu-26.04-amd64-v3", contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()
	stubVMImageCatalogFetch(t, vmImageTestCatalog())

	root := t.TempDir()
	cached := vmImageTestManifest("ubuntu-26.04-amd64-v1", contents)
	cachedDir := writeCachedVMImageManifest(t, root, cached)
	writeCachedVMImageArtifacts(t, cachedDir, contents)

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), testUbuntuVMPayload)
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
	latest := vmImageTestManifest(testUbuntuVMImageVersion, contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()
	stubVMImageCatalogFetch(t, vmImageTestCatalog())

	root := t.TempDir()
	latestDir := writeCachedVMImageManifest(t, root, latest)
	if err := os.WriteFile(filepath.Join(latestDir, latest.Kernel), contents[latest.Kernel], 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), testUbuntuVMPayload)
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
	latest := vmImageTestManifest(testUbuntuVMImageVersion, contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	}))
	defer server.Close()
	stubVMImageCatalogFetch(t, vmImageTestCatalog())

	root := t.TempDir()
	latestDir := writeCachedVMImageManifest(t, root, latest)
	writeCachedVMImageArtifacts(t, latestDir, contents)

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, inspectedManifest, err := cache.Inspect(context.Background(), testUbuntuVMPayload)
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

func TestVMImageCacheInspectIgnoresOtherOfficialFamilies(t *testing.T) {
	contents := vmImageTestContents()
	nixosLatest := vmImageTestManifest("nixos-26.05-amd64-v2", contents)
	nixosLatest.Name = "yeet-nixos-26.05"
	server := newVMImageArtifactTestServer(t, nixosLatest, contents)
	defer server.Close()
	stubVMImageCatalogFetch(t, vmImageTestCatalog())

	root := t.TempDir()
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v99", contents))
	nixosCached := vmImageTestManifest("nixos-26.05-amd64-v1", contents)
	nixosCached.Name = "yeet-nixos-26.05"
	nixosDir := writeCachedVMImageManifest(t, root, nixosCached)
	writeCachedVMImageArtifacts(t, nixosDir, contents)

	cache := vmImageCache{Root: root, ManifestURL: server.URL + "/manifest.json"}
	got, _, err := cache.Inspect(context.Background(), testNixOSVMPayload)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got.CachedVersion != "nixos-26.05-amd64-v1" || got.LatestVersion != "nixos-26.05-amd64-v2" {
		t.Fatalf("state = %#v, want NixOS family only", got)
	}
}

func TestVMImageCacheStateJSONUsesPublicFieldNames(t *testing.T) {
	raw, err := json.Marshal(vmImageCacheState{
		Payload:       testUbuntuVMPayload,
		CachedVersion: "ubuntu-26.04-amd64-v1",
		LatestVersion: "ubuntu-26.04-amd64-v3",
		State:         vmImageCacheStale,
		CachePath:     "/var/lib/yeet/vm-images/ubuntu-26.04-amd64-v3",
		ManifestURL:   testDefaultVMImageManifest,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(raw)
	for _, want := range []string{"cachedVersion", "latestVersion", "cachePath", "manifestURL"} {
		if !strings.Contains(text, `"`+want+`"`) {
			t.Fatalf("json %s missing %q", text, want)
		}
	}
	for _, unwanted := range []string{"cached_version", "latest_version", "cache_path", "manifest_url"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("json %s contains legacy field %q", text, unwanted)
		}
	}
}

func TestCachedVMImageManifestSelectsHighestValidCachedManifest(t *testing.T) {
	root := t.TempDir()
	contents := vmImageTestContents()
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v1", contents))
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v10", contents))
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v3", contents))

	image, ok := vmImageTestCatalog().ImageByPayload(testUbuntuVMPayload)
	if !ok {
		t.Fatal("missing Ubuntu catalog entry")
	}
	got, dir, ok, err := latestCachedVMImageManifest(root, image)
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

func TestCachedVMImageManifestSelectsHighestHybridCachedManifestAcrossLegacyTransition(t *testing.T) {
	root := t.TempDir()
	contents := vmImageTestContents()
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v15", contents))
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-kernel-7.1.0-v14", contents))
	latestDir := writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-kernel-7.1.1-v16", contents))

	image, ok := vmImageTestCatalog().ImageByPayload(testUbuntuVMPayload)
	if !ok {
		t.Fatal("missing Ubuntu catalog entry")
	}
	got, dir, ok, err := latestCachedVMImageManifest(root, image)
	if err != nil {
		t.Fatalf("latestCachedVMImageManifest: %v", err)
	}
	if !ok {
		t.Fatal("latestCachedVMImageManifest found no cached manifest")
	}
	if got.Version != "ubuntu-26.04-amd64-kernel-7.1.1-v16" {
		t.Fatalf("version = %q, want highest hybrid cached manifest", got.Version)
	}
	if dir != latestDir {
		t.Fatalf("dir = %q, want %q", dir, latestDir)
	}
}

func TestCompareVMImageVersionsTreatsEqualRevisionSuffixesAsEqual(t *testing.T) {
	legacy := "ubuntu-26.04-amd64-v16"
	hybrid := "ubuntu-26.04-amd64-kernel-7.1.1-v16"
	if got := compareVMImageVersions(legacy, hybrid); got != 0 {
		t.Fatalf("compareVMImageVersions(%q, %q) = %d, want 0", legacy, hybrid, got)
	}
	if got := compareVMImageVersions(hybrid, legacy); got != 0 {
		t.Fatalf("compareVMImageVersions(%q, %q) = %d, want 0", hybrid, legacy, got)
	}
}

func TestLatestCachedVMImageManifestFiltersByOfficialFamily(t *testing.T) {
	root := t.TempDir()
	contents := vmImageTestContents()
	writeCachedVMImageManifest(t, root, vmImageTestManifest("ubuntu-26.04-amd64-v99", contents))
	writeCachedVMImageManifest(t, root, vmImageTestManifest("nixos-26.05-amd64-v1", contents))
	writeCachedVMImageManifest(t, root, vmImageTestManifest("nixos-26.05-amd64-v3", contents))

	image, ok := vmImageTestCatalog().ImageByPayload(testNixOSVMPayload)
	if !ok {
		t.Fatal("missing NixOS catalog entry")
	}
	got, _, ok, err := latestCachedVMImageManifest(root, image)
	if err != nil {
		t.Fatalf("latestCachedVMImageManifest: %v", err)
	}
	if !ok || got.Version != "nixos-26.05-amd64-v3" {
		t.Fatalf("manifest = %#v ok=%v, want NixOS v3", got, ok)
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
	valid := vmImageTestManifest("ubuntu-26.04-amd64-v3", contents)
	writeCachedVMImageManifest(t, root, valid)

	image, ok := vmImageTestCatalog().ImageByPayload(testUbuntuVMPayload)
	if !ok {
		t.Fatal("missing Ubuntu catalog entry")
	}
	got, _, ok, err := latestCachedVMImageManifest(root, image)
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
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v3", contents)
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

func TestVMImageCacheArtifactURLFromStableManifestRoute(t *testing.T) {
	cache := vmImageCache{ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json"}
	got, err := cache.artifactURL("vmlinux")
	if err != nil {
		t.Fatalf("artifactURL: %v", err)
	}
	want := "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/vmlinux"
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

func stubVMImageCatalogFetch(t *testing.T, catalog vmImageCatalog) {
	t.Helper()
	orig := fetchVMImageCatalogFunc
	fetchVMImageCatalogFunc = func(context.Context, *http.Client) (vmImageCatalog, error) {
		return catalog, nil
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })
}

func vmImageTestCatalog() vmImageCatalog {
	return vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			{
				Payload:        testUbuntuVMPayload,
				Name:           "Ubuntu 26.04",
				Architecture:   "amd64",
				ManifestURL:    testDefaultVMImageManifest,
				VersionPrefix:  "ubuntu-26.04-amd64-",
				DefaultUser:    "ubuntu",
				MetadataDriver: "ubuntu",
				Default:        true,
			},
			{
				Payload:        testNixOSVMPayload,
				Name:           "NixOS 26.05",
				Architecture:   "amd64",
				ManifestURL:    testNixOSVMImageManifestURL,
				VersionPrefix:  "nixos-26.05-amd64-",
				DefaultUser:    "nixos",
				MetadataDriver: "nixos",
			},
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

func newVMImageArtifactTestServer(t *testing.T, manifest vmImageManifest, contents map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			if err := json.NewEncoder(w).Encode(manifest); err != nil {
				t.Fatalf("encode manifest: %v", err)
			}
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		content, ok := contents[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content)
	}))
}

type recordingVMImageProgressUI struct {
	mu      sync.Mutex
	starts  int
	stops   int
	steps   []string
	details []string
	done    []string
	fails   []string
}

func (u *recordingVMImageProgressUI) Start() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.starts++
}

func (u *recordingVMImageProgressUI) Stop() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stops++
}

func (u *recordingVMImageProgressUI) Suspend() {}

func (u *recordingVMImageProgressUI) StartStep(name string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.steps = append(u.steps, name)
}

func (u *recordingVMImageProgressUI) UpdateDetail(detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.details = append(u.details, detail)
}

func (u *recordingVMImageProgressUI) DoneStep(detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.done = append(u.done, detail)
}

func (u *recordingVMImageProgressUI) FailStep(detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fails = append(u.fails, detail)
}

func (u *recordingVMImageProgressUI) Printer(format string, args ...any) {}

func (u *recordingVMImageProgressUI) detailsSnapshot() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.details...)
}

func (u *recordingVMImageProgressUI) stepNames() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.steps...)
}

func (u *recordingVMImageProgressUI) failuresSnapshot() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.fails...)
}

func (u *recordingVMImageProgressUI) doneSnapshot() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.done...)
}

func (u *recordingVMImageProgressUI) startStopCounts() struct {
	starts int
	stops  int
} {
	u.mu.Lock()
	defer u.mu.Unlock()
	return struct {
		starts int
		stops  int
	}{starts: u.starts, stops: u.stops}
}

func assertVMImageProgressDetail(t *testing.T, details []string, want string) {
	t.Helper()
	for _, detail := range details {
		if strings.Contains(detail, want) {
			return
		}
	}
	t.Fatalf("progress details %#v missing %q", details, want)
}
