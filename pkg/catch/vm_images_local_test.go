// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateLocalVMImageName(t *testing.T) {
	valid := []string{"foo", "foo/bar", "team/ubuntu-fast", "a/b-c_d.1"}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			if err := validateLocalVMImageName(name); err != nil {
				t.Fatalf("validateLocalVMImageName(%q): %v", name, err)
			}
		})
	}

	invalid := []string{"", "ubuntu/26.04", "../foo", "/foo", "foo//bar", "foo/bar/", "foo bar", "Foo/Bar"}
	for _, name := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			if err := validateLocalVMImageName(name); err == nil {
				t.Fatalf("validateLocalVMImageName(%q) returned nil error", name)
			}
		})
	}
}

func TestImportLocalVMImageRootFSOnlyUsesManagedKernel(t *testing.T) {
	managed := fakeManagedVMImageAsset(t)
	managed.Manifest.GuestInit = vmGuestInitPath
	managed.Manifest.KernelVersion = "linux-managed-test"
	called := false
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			called = true
			return managed, nil
		},
		Now: func() time.Time {
			return time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
		},
	}

	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !called {
		t.Fatal("EnsureManagedAsset was not called")
	}
	if ref.Name != "foo/bar" || ref.Payload != "vm://foo/bar" || ref.KernelPolicy != localVMImageKernelPolicyManaged {
		t.Fatalf("ref = %#v, want local managed foo/bar ref", ref)
	}
	if ref.Version == "" || !strings.Contains(ref.Version, "local-foo-bar-") {
		t.Fatalf("ref version = %q, want local foo/bar version", ref.Version)
	}
	assertLocalImageFileContains(t, ref.Root, "rootfs.ext4", "local-rootfs")
	assertLocalImageFileContains(t, ref.Root, "vmlinux", "managed-kernel")
	assertLocalImageFileContains(t, ref.Root, "firecracker", "managed-firecracker")

	var manifest vmImageManifest
	rawManifest, err := os.ReadFile(filepath.Join(ref.Root, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Version != ref.Version {
		t.Fatalf("manifest version = %q, want ref version %q", manifest.Version, ref.Version)
	}
	if manifest.GuestInit != vmGuestInitPath {
		t.Fatalf("manifest guest_init = %q, want %q", manifest.GuestInit, vmGuestInitPath)
	}
	if manifest.KernelVersion != "linux-managed-test" {
		t.Fatalf("manifest kernel_version = %q, want linux-managed-test", manifest.KernelVersion)
	}

	storedRef := decodeLocalRef(t, localVMImageRefPath(importer.CacheRoot, "foo/bar"))
	if !reflect.DeepEqual(storedRef, ref) {
		t.Fatalf("stored ref = %#v, want %#v", storedRef, ref)
	}
}

func TestImportLocalVMImagePreservesBundleManifestFastBootCapability(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	snapSupport := false
	sourceManifest := vmImageManifest{
		Name:            "yeet-test-image",
		Version:         "test-image-v1",
		Architecture:    "x86_64",
		ImageProfile:    "fast",
		Distro:          "nixos",
		DistroVersion:   "26.05",
		DefaultUser:     "nixos",
		KernelPolicy:    "yeet-managed",
		GuestInit:       vmGuestInitPath,
		GuestSystemInit: "/run/current-system/init",
		MetadataDriver:  "nixos",
		SnapSupport:     &snapSupport,
		Kernel:          "vmlinux",
		RootFS:          "rootfs.ext4",
		Firecracker:     "firecracker",
		RootFSSize:      int64(len("local-rootfs")),
		KernelVersion:   "linux-source-test",
		Checksums: map[string]string{
			"vmlinux":     strings.Repeat("a", 64),
			"rootfs.ext4": strings.Repeat("b", 64),
			"firecracker": strings.Repeat("c", 64),
		},
	}
	manifestRaw, err := json.Marshal(sourceManifest)
	if err != nil {
		t.Fatalf("marshal source manifest: %v", err)
	}

	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4":   []byte("local-rootfs"),
			"manifest.json": manifestRaw,
		}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	manifest, err := readLocalVMImageBlobManifest(ref.Root)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.GuestInit != vmGuestInitPath {
		t.Fatalf("manifest guest_init = %q, want %q", manifest.GuestInit, vmGuestInitPath)
	}
	if manifest.SnapSupport == nil || *manifest.SnapSupport {
		t.Fatalf("manifest snap_support = %#v, want false", manifest.SnapSupport)
	}
	if manifest.Distro != "nixos" || manifest.DistroVersion != "26.05" || manifest.DefaultUser != "nixos" || manifest.GuestSystemInit != "/run/current-system/init" || manifest.MetadataDriver != "nixos" {
		t.Fatalf("manifest NixOS metadata fields = %#v", manifest)
	}
	if !vmImageSupportsFastBoot(manifest) {
		t.Fatal("imported manifest does not advertise fast boot")
	}
}

func TestImportLocalVMImageContentIDIncludesRuntimeMetadata(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}

	firstManifest := localVMImageSourceManifestForTest(t, "ubuntu")
	secondManifest := localVMImageSourceManifestForTest(t, "admin")
	ref1, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4":   []byte("local-rootfs"),
			"manifest.json": firstManifest,
		}),
	})
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	ref2, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4":   []byte("local-rootfs"),
			"manifest.json": secondManifest,
		}),
	})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if ref1.ContentID == ref2.ContentID {
		t.Fatalf("content IDs are equal for different runtime metadata: %s", ref1.ContentID)
	}
	manifest, err := readLocalVMImageBlobManifest(ref2.Root)
	if err != nil {
		t.Fatalf("read second manifest: %v", err)
	}
	if manifest.DefaultUser != "admin" {
		t.Fatalf("second manifest default_user = %q, want admin", manifest.DefaultUser)
	}
}

func TestImportLocalVMImageRejectsLocalKernelWithoutFlag(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}

	_, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4": []byte("local-rootfs"),
			"vmlinux":     []byte("local-kernel"),
		}),
	})
	if err == nil {
		t.Fatal("Import returned nil error")
	}
	if !strings.Contains(err.Error(), "--allow-local-kernel") {
		t.Fatalf("error = %v, want --allow-local-kernel", err)
	}
}

func TestImportLocalVMImageAllowsLocalKernelWithFlag(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}

	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:             "foo/bar",
		AllowLocalKernel: true,
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4": []byte("local-rootfs"),
			"vmlinux":     []byte("local-kernel"),
		}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if ref.KernelPolicy != localVMImageKernelPolicyLocal {
		t.Fatalf("kernel policy = %q, want %q", ref.KernelPolicy, localVMImageKernelPolicyLocal)
	}
	assertLocalImageFileContains(t, ref.Root, "vmlinux", "local-kernel")
}

func TestResolveLocalVMImageAsset(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	asset, err := resolveLocalVMImageAsset(context.Background(), importer.CacheRoot, "foo/bar")
	if err != nil {
		t.Fatalf("resolveLocalVMImageAsset: %v", err)
	}
	if asset.Manifest.Version != ref.Version {
		t.Fatalf("asset manifest version = %q, want ref version %q", asset.Manifest.Version, ref.Version)
	}
	if asset.Paths.Dir != ref.Root {
		t.Fatalf("asset dir = %q, want ref root %q", asset.Paths.Dir, ref.Root)
	}
	if asset.Paths.RootFSPath != filepath.Join(ref.Root, ref.RootFS) || asset.Paths.KernelPath != filepath.Join(ref.Root, "vmlinux") || asset.Paths.FirecrackerPath != filepath.Join(ref.Root, "firecracker") {
		t.Fatalf("asset paths = %#v, want paths under ref root", asset.Paths)
	}
}

func TestResolveLocalVMImageAssetRejectsTamperedArtifact(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	writeFile(t, filepath.Join(ref.Root, ref.RootFS), "tampered-rootfs", 0o644)

	_, err = resolveLocalVMImageAsset(context.Background(), importer.CacheRoot, "foo/bar")
	if err == nil {
		t.Fatal("resolveLocalVMImageAsset returned nil error for tampered artifact")
	}
	if !strings.Contains(err.Error(), "checksum") && !strings.Contains(err.Error(), "content") {
		t.Fatalf("error = %v, want checksum or content integrity failure", err)
	}
}

func TestResolveLocalVMImageAssetAcceptsLegacyContentID(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name: "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{
			"rootfs.ext4":   []byte("local-rootfs"),
			"manifest.json": localVMImageSourceManifestForTest(t, "admin"),
		}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	manifest, err := readLocalVMImageBlobManifest(ref.Root)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	legacyID, err := legacyLocalVMImageContentIDForTest(
		ref.Name,
		filepath.Join(ref.Root, ref.RootFS),
		filepath.Join(ref.Root, ref.Kernel),
		filepath.Join(ref.Root, ref.Firecracker),
		localVMImageCapabilitiesFromManifest(manifest),
	)
	if err != nil {
		t.Fatalf("legacy content ID: %v", err)
	}
	legacyRoot := filepath.Join(importer.CacheRoot, "local", "blobs", legacyID)
	if err := os.Rename(ref.Root, legacyRoot); err != nil {
		t.Fatalf("move blob to legacy root: %v", err)
	}
	ref.ContentID = legacyID
	ref.Root = legacyRoot
	if err := writeLocalVMImageRef(localVMImageRefPath(importer.CacheRoot, ref.Name), ref); err != nil {
		t.Fatalf("write legacy ref: %v", err)
	}

	asset, err := resolveLocalVMImageAsset(context.Background(), importer.CacheRoot, ref.Name)
	if err != nil {
		t.Fatalf("resolve legacy local image: %v", err)
	}
	if asset.Manifest.DefaultUser != "admin" {
		t.Fatalf("resolved default_user = %q, want admin", asset.Manifest.DefaultUser)
	}
}

func TestRemoveLocalVMImageDeletesRefAndUnreferencedBlob(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	refPath := localVMImageRefPath(importer.CacheRoot, "foo/bar")

	if err := removeLocalVMImage(importer.CacheRoot, "foo/bar"); err != nil {
		t.Fatalf("removeLocalVMImage: %v", err)
	}
	if _, err := os.Stat(refPath); !os.IsNotExist(err) {
		t.Fatalf("ref path stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(ref.Root); !os.IsNotExist(err) {
		t.Fatalf("blob dir stat err = %v, want not exist", err)
	}
}

func TestRemoveLocalVMImagePreservesSharedBlobButRejectsAliasResolve(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := writeLocalVMImageRef(localVMImageRefPath(importer.CacheRoot, "team/alias"), ref); err != nil {
		t.Fatalf("write alias ref: %v", err)
	}

	if err := removeLocalVMImage(importer.CacheRoot, "foo/bar"); err != nil {
		t.Fatalf("removeLocalVMImage: %v", err)
	}
	if _, err := os.Stat(ref.Root); err != nil {
		t.Fatalf("shared blob stat: %v", err)
	}
	_, err = resolveLocalVMImageAsset(context.Background(), importer.CacheRoot, "team/alias")
	if err == nil {
		t.Fatal("resolve alias returned nil error")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("resolve alias error = %v, want ref path/name mismatch", err)
	}
}

func TestResolveLocalVMImageAssetRejectsPayloadMismatch(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	ref.Payload = "vm://other/name"
	if err := writeLocalVMImageRef(localVMImageRefPath(importer.CacheRoot, "foo/bar"), ref); err != nil {
		t.Fatalf("write mismatched ref: %v", err)
	}

	_, err = resolveLocalVMImageAsset(context.Background(), importer.CacheRoot, "foo/bar")
	if err == nil {
		t.Fatal("resolve payload mismatch returned nil error")
	}
	if !strings.Contains(err.Error(), "payload") {
		t.Fatalf("resolve payload mismatch error = %v, want payload mismatch", err)
	}
}

func TestListLocalVMImagesSortsByName(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	for _, name := range []string{"team/b", "alpha/a"} {
		if _, err := importer.Import(context.Background(), localVMImageImportRequest{
			Name:   name,
			Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs-" + name)}),
		}); err != nil {
			t.Fatalf("Import(%q): %v", name, err)
		}
	}

	refs, err := listLocalVMImages(importer.CacheRoot)
	if err != nil {
		t.Fatalf("listLocalVMImages: %v", err)
	}
	got := make([]string, 0, len(refs))
	for _, ref := range refs {
		got = append(got, ref.Name)
	}
	want := []string{"alpha/a", "team/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
}

func TestImportLocalVMImageRejectsInvalidExistingBlob(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	req := localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
	}
	ref, err := importer.Import(context.Background(), req)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	writeFile(t, filepath.Join(ref.Root, ref.RootFS), "corrupt-rootfs", 0o644)

	req.Reader = localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")})
	_, err = importer.Import(context.Background(), req)
	if err == nil {
		t.Fatal("Import returned nil error for invalid existing blob")
	}
	if !strings.Contains(err.Error(), "existing local VM image blob") {
		t.Fatalf("error = %v, want existing blob integrity error", err)
	}
}

func TestImportLocalVMImageRequiresOneRootFS(t *testing.T) {
	importer := localVMImageImporter{
		CacheRoot: t.TempDir(),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	tests := map[string]map[string][]byte{
		"missing":  {"vmlinux": []byte("kernel")},
		"multiple": {"rootfs.ext4": []byte("rootfs"), "rootfs.ext4.zst": []byte("compressed-rootfs")},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := importer.Import(context.Background(), localVMImageImportRequest{
				Name:   "foo/bar",
				Reader: localVMImageBundleTar(t, files),
			})
			if err == nil {
				t.Fatal("Import returned nil error")
			}
			if !strings.Contains(err.Error(), "rootfs") {
				t.Fatalf("error = %v, want rootfs", err)
			}
		})
	}
}

func TestExtractLocalVMImageBundleRejectsSymlinkWriteEscape(t *testing.T) {
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "owned")
	writeFile(t, outsideFile, "original", 0o644)

	bundle := localVMImageOrderedBundleTar(t, []localVMImageTarEntry{
		{Name: "rootfs.ext4", Typeflag: tar.TypeSymlink, Linkname: outsideFile},
		{Name: "rootfs.ext4", Body: []byte("evil-rootfs")},
	})
	err := extractLocalVMImageBundle(bundle, t.TempDir())
	if err == nil {
		t.Fatal("extractLocalVMImageBundle returned nil error")
	}
	got, readErr := os.ReadFile(outsideFile)
	if readErr != nil {
		t.Fatalf("read outside file: %v", readErr)
	}
	if string(got) != "original" {
		t.Fatalf("outside file = %q, want original content", got)
	}
}

func TestExtractLocalVMImageBundleRejectsRegularFileBelowSymlink(t *testing.T) {
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "rootfs.ext4")

	bundle := localVMImageOrderedBundleTar(t, []localVMImageTarEntry{
		{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: outsideDir},
		{Name: "escape/rootfs.ext4", Body: []byte("evil-rootfs")},
	})
	err := extractLocalVMImageBundle(bundle, t.TempDir())
	if err == nil {
		t.Fatal("extractLocalVMImageBundle returned nil error")
	}
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("outside file stat err = %v, want not exist", err)
	}
}

func TestExtractLocalVMImageBundleRejectsUnsupportedEntries(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "rootfs.ext4", Mode: 0o644, Size: int64(len("rootfs"))}); err != nil {
		t.Fatalf("write rootfs header: %v", err)
	}
	if _, err := tw.Write([]byte("rootfs")); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "socket", Mode: 0o644, Typeflag: tar.TypeFifo}); err != nil {
		t.Fatalf("write fifo header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	err := extractLocalVMImageBundle(bytes.NewReader(buf.Bytes()), t.TempDir())
	if err == nil {
		t.Fatal("extractLocalVMImageBundle returned nil error")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v, want unsupported entry", err)
	}
}

func TestValidateLocalVMImageSymlinkAllowsInternalTargetAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "files", "kernel"), "kernel", 0o644)
	internalLink := filepath.Join(root, "vmlinux")
	if err := os.Symlink(filepath.Join("files", "kernel"), internalLink); err != nil {
		t.Fatalf("create internal symlink: %v", err)
	}
	if err := validateLocalVMImageSymlink(root, internalLink); err != nil {
		t.Fatalf("validateLocalVMImageSymlink internal: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "kernel")
	writeFile(t, outside, "outside-kernel", 0o644)
	escapeLink := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escapeLink); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}
	if err := validateLocalVMImageSymlink(root, escapeLink); err == nil {
		t.Fatal("validateLocalVMImageSymlink escape returned nil error")
	}
}

func fakeManagedVMImageAsset(t *testing.T) vmImageAsset {
	t.Helper()
	dir := t.TempDir()
	kernel := filepath.Join(dir, "vmlinux")
	firecracker := filepath.Join(dir, "firecracker")
	rootfs := filepath.Join(dir, "rootfs.ext4")
	writeFile(t, kernel, "managed-kernel", 0o644)
	writeFile(t, firecracker, "managed-firecracker", 0o755)
	writeFile(t, rootfs, "managed-rootfs", 0o644)
	return vmImageAsset{
		Paths: vmImagePaths{
			Dir:             dir,
			KernelPath:      kernel,
			FirecrackerPath: firecracker,
			RootFSPath:      rootfs,
		},
		Manifest: vmImageManifest{
			Version: "managed-test",
		},
	}
}

func localVMImageBundleTar(t *testing.T, files map[string][]byte) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("write tar file %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func localVMImageSourceManifestForTest(t *testing.T, defaultUser string) []byte {
	t.Helper()
	raw, err := json.Marshal(vmImageManifest{
		RootFS:         "rootfs.ext4",
		DefaultUser:    defaultUser,
		MetadataDriver: "ubuntu",
	})
	if err != nil {
		t.Fatalf("marshal source manifest: %v", err)
	}
	return raw
}

func legacyLocalVMImageContentIDForTest(name, rootFSPath, kernelPath, firecrackerPath string, capabilities localVMImageManifestCapabilities) (string, error) {
	h := sha256.New()
	if _, err := h.Write([]byte(name)); err != nil {
		return "", err
	}
	parts := []string{
		capabilities.ImageProfile,
		capabilities.KernelPolicy,
		capabilities.GuestInit,
		fmt.Sprintf("%t", capabilities.SnapSupportSet),
		fmt.Sprintf("%t", capabilities.SnapSupport),
		capabilities.KernelVersion,
		capabilities.UbuntuKernelVersion,
	}
	for _, part := range parts {
		if _, err := h.Write([]byte{0}); err != nil {
			return "", err
		}
		if _, err := h.Write([]byte(part)); err != nil {
			return "", err
		}
	}
	for _, path := range []string{rootFSPath, kernelPath, firecrackerPath} {
		if _, err := h.Write([]byte{0}); err != nil {
			return "", err
		}
		if err := hashLocalVMImageFile(h, path); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type localVMImageTarEntry struct {
	Name     string
	Body     []byte
	Typeflag byte
	Linkname string
}

func localVMImageOrderedBundleTar(t *testing.T, entries []localVMImageTarEntry) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		typeflag := entry.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     entry.Name,
			Mode:     0o644,
			Size:     int64(len(entry.Body)),
			Typeflag: typeflag,
			Linkname: entry.Linkname,
		}
		if typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", entry.Name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write(entry.Body); err != nil {
				t.Fatalf("write tar file %s: %v", entry.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func assertLocalImageFileContains(t *testing.T, root, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if !strings.Contains(string(got), want) {
		t.Fatalf("%s content = %q, want containing %q", name, got, want)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func decodeLocalRef(t *testing.T, path string) localVMImageRef {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read local ref: %v", err)
	}
	var ref localVMImageRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatalf("decode local ref: %v", err)
	}
	return ref
}
