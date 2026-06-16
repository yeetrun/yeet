// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMImagesCmdTableShowsCacheState(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v1")
	restore := stubVMImageInspect(t, vmImageCacheState{
		Payload:       testUbuntuVMPayload,
		CachedVersion: "ubuntu-26.04-amd64-v0",
		LatestVersion: "ubuntu-26.04-amd64-v1",
		State:         vmImageCacheStale,
		CachePath:     cachePath,
		ManifestURL:   testDefaultVMImageManifest,
	})
	defer restore()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, nil); err != nil {
		t.Fatalf("vmImagesCmdFunc: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"PAYLOAD",
		"KIND",
		"STATE",
		"VERSION",
		"CACHE",
		testUbuntuVMPayload,
		"builtin",
		string(vmImageCacheStale),
		"ubuntu-26.04-amd64-v1",
		cachePath,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesCmdJSONShowsListRows(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v1")
	restore := stubVMImageInspectMap(t, map[string]vmImageCacheState{
		testUbuntuVMPayload: {
			Payload:       testUbuntuVMPayload,
			CachedVersion: "ubuntu-26.04-amd64-v1",
			LatestVersion: "ubuntu-26.04-amd64-v1",
			State:         vmImageCacheCurrent,
			CachePath:     cachePath,
			ManifestURL:   testDefaultVMImageManifest,
		},
		testNixOSVMPayload: {
			Payload:       testNixOSVMPayload,
			LatestVersion: testNixOSVMImageVersion,
			State:         vmImageCacheMissing,
			ManifestURL:   testNixOSVMImageManifestURL,
		},
	})
	defer restore()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, nil); err != nil {
		t.Fatalf("vmImagesCmdFunc: %v", err)
	}

	var got []vmImageListRowJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if len(got) != 2 {
		t.Fatalf("row count = %d, want 2: %#v", len(got), got)
	}
	byPayload := vmImageRowsByPayload(got)
	want := vmImageListRowJSON{
		Payload:   testUbuntuVMPayload,
		Kind:      "builtin",
		State:     string(vmImageCacheCurrent),
		Version:   "ubuntu-26.04-amd64-v1",
		CachePath: cachePath,
	}
	if byPayload[testUbuntuVMPayload] != want {
		t.Fatalf("ubuntu json row = %#v, want %#v", byPayload[testUbuntuVMPayload], want)
	}
	if byPayload[testNixOSVMPayload].Version != testNixOSVMImageVersion || byPayload[testNixOSVMPayload].State != string(vmImageCacheMissing) {
		t.Fatalf("nixos json row = %#v", byPayload[testNixOSVMPayload])
	}
}

func TestVMImagesCmdListShowsAllOfficialImages(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	restore := stubVMImageInspectMap(t, map[string]vmImageCacheState{
		testUbuntuVMPayload: {
			Payload:       testUbuntuVMPayload,
			LatestVersion: testUbuntuVMImageVersion,
			State:         vmImageCacheCurrent,
		},
		testNixOSVMPayload: {
			Payload:       testNixOSVMPayload,
			LatestVersion: testNixOSVMImageVersion,
			State:         vmImageCacheMissing,
		},
	})
	defer restore()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, nil); err != nil {
		t.Fatalf("vmImagesCmdFunc: %v", err)
	}
	var rows []vmImageListRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	byPayload := vmImageRowsByPayload(rows)
	if byPayload[testUbuntuVMPayload].Version != testUbuntuVMImageVersion {
		t.Fatalf("ubuntu row = %#v", byPayload[testUbuntuVMPayload])
	}
	if byPayload[testNixOSVMPayload].Version != testNixOSVMImageVersion {
		t.Fatalf("nixos row = %#v", byPayload[testNixOSVMPayload])
	}
}

func TestVMImagesCmdCatalogShowsOfficialImagesWithoutCacheInspect(t *testing.T) {
	server := newTestServer(t)
	catalog := vmImageTestCatalog()
	stubVMImageCatalogFetch(t, catalog)
	restore := stubVMImageInspectFail(t)
	defer restore()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"catalog"}); err != nil {
		t.Fatalf("vmImagesCmdFunc catalog: %v", err)
	}

	var rows []vmImageCatalogRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if len(rows) != len(catalog.Images) {
		t.Fatalf("catalog row count = %d, want %d: %#v", len(rows), len(catalog.Images), rows)
	}
	byPayload := vmImageCatalogRowsByPayload(rows)
	if got := byPayload[testUbuntuVMPayload]; got.Kind != "builtin" || got.Name != "Ubuntu 26.04" || got.DefaultUser != "ubuntu" || got.VersionPrefix != "ubuntu-26.04-amd64-" {
		t.Fatalf("ubuntu catalog row = %#v", got)
	}
	if got := byPayload[testNixOSVMPayload]; got.Kind != "builtin" || got.Name != "NixOS 26.05" || got.DefaultUser != "nixos" || got.VersionPrefix != "nixos-26.05-amd64-" {
		t.Fatalf("nixos catalog row = %#v", got)
	}
}

func TestVMImagesCmdCatalogUsesRemoteCatalogFamilies(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://debian/13",
			Name:           "Debian 13",
			Architecture:   "amd64",
			ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json",
			VersionPrefix:  "debian-13-amd64-",
			DefaultUser:    "debian",
			MetadataDriver: "ubuntu",
			Default:        true,
		},
	}})

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"catalog"}); err != nil {
		t.Fatalf("vmImagesCmdFunc catalog: %v", err)
	}

	var rows []vmImageCatalogRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Payload != "vm://debian/13" || rows[0].VersionPrefix != "debian-13-amd64-" {
		t.Fatalf("catalog rows = %#v", rows)
	}
}

func TestVMImagesCmdListUsesSingleCatalogSnapshot(t *testing.T) {
	server := newTestServer(t)
	catalog := vmImageSingleFetchCommandCatalog(t, "vm://debian/13", "debian-13-amd64-v1", true)
	calls := stubVMImageCatalogFetchOnce(t, catalog)

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"ls"}); err != nil {
		t.Fatalf("vmImagesCmdFunc ls: %v", err)
	}
	if got := calls(); got != 1 {
		t.Fatalf("catalog fetch calls = %d, want 1", got)
	}
	var rows []vmImageListRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].Payload != "vm://debian/13" || rows[0].Version != "debian-13-amd64-v1" {
		t.Fatalf("list rows = %#v", rows)
	}
}

func TestVMImagesCmdCatalogIncludesLocalImages(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	importer := localVMImageImporter{
		CacheRoot: cacheRoot,
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

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"catalog"}); err != nil {
		t.Fatalf("vmImagesCmdFunc catalog: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"PAYLOAD",
		"KIND",
		"NAME",
		"DEFAULT_USER",
		testUbuntuVMPayload,
		testNixOSVMPayload,
		"vm://foo/bar",
		"local",
		ref.Name,
		"admin",
		ref.KernelPolicy,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("catalog output missing %q:\n%s", want, got)
		}
	}
}

func TestTTYVMImageCacheLeavesManifestURLUnset(t *testing.T) {
	server := newTestServer(t)
	execer := &ttyExecer{s: server}

	cache := execer.vmImageCache()
	if cache.Root != filepath.Join(server.cfg.RootDir, "vm-images") {
		t.Fatalf("cache root = %q, want VM image root under server root", cache.Root)
	}
	if cache.ManifestURL != "" {
		t.Fatalf("manifest URL override = %q, want empty so catalog images select their manifest URL", cache.ManifestURL)
	}
	if got := cache.manifestURL(); got != "" {
		t.Fatalf("manifestURL fallback = %q, want empty", got)
	}
}

func TestVMImagesCmdImportReadsStdinAndPrintsRef(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	restoreEnsure := stubManagedVMImageAsset(t, fakeManagedVMImageAsset(t))
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw: &readWriter{
			Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
			Writer: &out,
		},
	}
	err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table", Stdin: true}, []string{"import", "foo/bar"})
	if err != nil {
		t.Fatalf("vmImagesCmdFunc import: %v", err)
	}
	got := out.String()
	for _, want := range []string{"vm://foo/bar", "local", "imported", "local-foo-bar-"} {
		if !strings.Contains(got, want) {
			t.Fatalf("import output missing %q:\n%s", want, got)
		}
	}
	ref := decodeLocalRef(t, localVMImageRefPath(execer.vmImageCache().Root, "foo/bar"))
	if ref.Payload != "vm://foo/bar" || !strings.Contains(got, ref.Version) || !strings.Contains(got, ref.Root) {
		t.Fatalf("stored ref = %#v, import output = %q", ref, got)
	}
	if ref.KernelPolicy != localVMImageKernelPolicyManaged {
		t.Fatalf("kernel policy = %q, want %q", ref.KernelPolicy, localVMImageKernelPolicyManaged)
	}
	assertLocalImageFileContains(t, ref.Root, ref.RootFS, "local-rootfs")
}

func TestVMImagesCmdImportReadsRawPayloadWhenPTYBypassesInput(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	restoreEnsure := stubManagedVMImageAsset(t, fakeManagedVMImageAsset(t))
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx:            context.Background(),
		s:              server,
		isPty:          true,
		bypassPtyInput: true,
		rawRW: readWriter{
			Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("raw-rootfs")}),
			Writer: io.Discard,
		},
		rw: readWriter{
			Reader: strings.NewReader("not a tar stream"),
			Writer: &out,
		},
	}
	err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table", Stdin: true}, []string{"import", "foo/raw"})
	if err != nil {
		t.Fatalf("vmImagesCmdFunc import: %v", err)
	}
	ref := decodeLocalRef(t, localVMImageRefPath(execer.vmImageCache().Root, "foo/raw"))
	assertLocalImageFileContains(t, ref.Root, ref.RootFS, "raw-rootfs")
}

func TestVMImagesCmdImportUsesDefaultCatalogImage(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://debian/13",
			Name:           "Debian 13",
			Architecture:   "amd64",
			ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json",
			VersionPrefix:  "debian-13-amd64-",
			DefaultUser:    "debian",
			MetadataDriver: "ubuntu",
			Default:        true,
		},
	}})
	var ensured []string
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensured = append(ensured, payload)
		if cache.Root == "" {
			t.Fatal("ensure cache root is empty")
		}
		return fakeManagedVMImageAsset(t), nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw: &readWriter{
			Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
			Writer: &out,
		},
	}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", Stdin: true}, []string{"import", "foo/debian"}); err != nil {
		t.Fatalf("vmImagesCmdFunc import: %v", err)
	}
	if !reflect.DeepEqual(ensured, []string{"vm://debian/13"}) {
		t.Fatalf("ensured payloads = %#v", ensured)
	}
}

func TestVMImagesCmdImportUsesSingleCatalogSnapshot(t *testing.T) {
	server := newTestServer(t)
	catalog := vmImageSingleFetchCommandCatalog(t, "vm://debian/13", "debian-13-amd64-v1", true)
	calls := stubVMImageCatalogFetchOnce(t, catalog)
	stubPrepareVMRootFSIdentity(t)

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw: &readWriter{
			Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("local-rootfs")}),
			Writer: &out,
		},
	}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", Stdin: true}, []string{"import", "foo/snapshot"}); err != nil {
		t.Fatalf("vmImagesCmdFunc import: %v", err)
	}
	if got := calls(); got != 1 {
		t.Fatalf("catalog fetch calls = %d, want 1", got)
	}
	var rows []vmImageListRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].Payload != "vm://foo/snapshot" || rows[0].State != "imported" {
		t.Fatalf("import rows = %#v", rows)
	}
}

func TestVMImagesCmdImportRejectsWithoutStdin(t *testing.T) {
	server := newTestServer(t)
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"import", "foo/bar"})
	if err == nil || !strings.Contains(err.Error(), "use yeet vm images import from the client") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMImagesCmdListShowsLocalImages(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	importer := localVMImageImporter{
		CacheRoot: cacheRoot,
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
	builtinCachePath := filepath.Join(cacheRoot, "ubuntu-26.04-amd64-v1")
	restore := stubVMImageInspectMap(t, map[string]vmImageCacheState{
		testUbuntuVMPayload: {
			Payload:       testUbuntuVMPayload,
			CachedVersion: "ubuntu-26.04-amd64-v0",
			LatestVersion: "ubuntu-26.04-amd64-v1",
			State:         vmImageCacheStale,
			CachePath:     builtinCachePath,
			ManifestURL:   testDefaultVMImageManifest,
		},
		testNixOSVMPayload: {
			Payload:       testNixOSVMPayload,
			LatestVersion: testNixOSVMImageVersion,
			State:         vmImageCacheMissing,
			ManifestURL:   testNixOSVMImageManifestURL,
		},
	})
	defer restore()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json-pretty"}, []string{"ls"}); err != nil {
		t.Fatalf("vmImagesCmdFunc ls: %v", err)
	}

	var rows []vmImageListRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	byPayload := map[string]vmImageListRowJSON{}
	for _, row := range rows {
		byPayload[row.Payload] = row
	}
	builtin := byPayload[testUbuntuVMPayload]
	if builtin.Kind != "builtin" || builtin.State != string(vmImageCacheStale) || builtin.Version != "ubuntu-26.04-amd64-v1" || builtin.CachePath != builtinCachePath {
		t.Fatalf("builtin row = %#v", builtin)
	}
	local := byPayload["vm://foo/bar"]
	if local.Kind != "local" || local.State != "ready" || local.Version != ref.Version || local.CachePath != ref.Root || local.KernelPolicy != ref.KernelPolicy {
		t.Fatalf("local row = %#v, want ref %#v", local, ref)
	}
}

func TestVMImagesCmdRemoveRequiresYes(t *testing.T) {
	server := newTestServer(t)
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &bytes.Buffer{}}
	err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"rm", "foo/bar"})
	if err == nil || !strings.Contains(err.Error(), "rerun with --yes") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMImagesCmdRemoveDeletesLocalImage(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	importer := localVMImageImporter{
		CacheRoot: cacheRoot,
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

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", Yes: true}, []string{"rm", "foo/bar"}); err != nil {
		t.Fatalf("vmImagesCmdFunc rm: %v", err)
	}

	var rows []vmImageListRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].Payload != "vm://foo/bar" || rows[0].Kind != "local" || rows[0].State != "removed" {
		t.Fatalf("remove rows = %#v", rows)
	}
	if _, err := os.Stat(localVMImageRefPath(cacheRoot, "foo/bar")); !os.IsNotExist(err) {
		t.Fatalf("ref stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(ref.Root); !os.IsNotExist(err) {
		t.Fatalf("blob stat err = %v, want not exist", err)
	}
}

func stubVMImagePruneCatalogFetch(t *testing.T) {
	t.Helper()
	stubVMImageCatalogFetch(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
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
	}})
}

func TestVMImagesCmdPruneDryRunPreviewsOldCacheWithoutRemoving(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	currentDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v8")

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", DryRun: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune dry-run: %v", err)
	}

	rows := decodeVMImagePruneRows(t, out.Bytes())
	assertPruneRow(t, rows, "cache", "ubuntu-26.04-amd64-v7", "prunable", oldDir)
	assertPruneRow(t, rows, "cache", "ubuntu-26.04-amd64-v8", "current", currentDir)
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("old cache dir should remain after dry-run: %v", err)
	}
}

func TestVMImagesCmdPruneClassifiesCatalogVersionPrefixes(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	stubVMImageCatalogFetch(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{Payload: "vm://debian/13", Name: "Debian 13", Architecture: "amd64", ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json", VersionPrefix: "debian-13-amd64-", DefaultUser: "debian", MetadataDriver: "ubuntu"},
	}})
	oldDebian := seedCachedVMImage(t, cacheRoot, "debian-13-amd64-v1")
	currentDebian := seedCachedVMImage(t, cacheRoot, "debian-13-amd64-v2")
	seedCachedVMImage(t, cacheRoot, "custom-local-v1")

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", DryRun: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune dry-run: %v", err)
	}
	rows := decodeVMImagePruneRows(t, out.Bytes())
	assertPruneRow(t, rows, "cache", "debian-13-amd64-v1", "prunable", oldDebian)
	assertPruneRow(t, rows, "cache", "debian-13-amd64-v2", "current", currentDebian)
	assertPruneRowPayload(t, rows, "debian-13-amd64-v1", "vm://debian/13")
	assertPruneRowPayload(t, rows, "debian-13-amd64-v2", "vm://debian/13")
	for _, row := range rows {
		if row.Version == "custom-local-v1" {
			t.Fatalf("local custom image should not be managed prune row: %#v", row)
		}
	}
}

func TestVMImagesCmdPruneKeepsCurrentVersionPerOfficialFamily(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldUbuntu := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v12")
	currentUbuntu := seedCachedVMImage(t, cacheRoot, testUbuntuVMImageVersion)
	currentNixOS := seedCachedVMImage(t, cacheRoot, testNixOSVMImageVersion)

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", DryRun: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune dry-run: %v", err)
	}

	rows := decodeVMImagePruneRows(t, out.Bytes())
	assertPruneRow(t, rows, "cache", "ubuntu-26.04-amd64-v12", "prunable", oldUbuntu)
	assertPruneRow(t, rows, "cache", testUbuntuVMImageVersion, "current", currentUbuntu)
	assertPruneRow(t, rows, "cache", testNixOSVMImageVersion, "current", currentNixOS)
	assertPruneRowPayload(t, rows, testUbuntuVMImageVersion, testUbuntuVMPayload)
	assertPruneRowPayload(t, rows, testNixOSVMImageVersion, testNixOSVMPayload)
}

func TestVMImagesCmdPruneTableShowsPayload(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	seedCachedVMImage(t, cacheRoot, testUbuntuVMImageVersion)
	seedCachedVMImage(t, cacheRoot, testNixOSVMImageVersion)

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table", DryRun: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune dry-run: %v", err)
	}
	for _, want := range []string{"PAYLOAD", testUbuntuVMPayload, testNixOSVMPayload} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("prune table missing %q:\n%s", want, out.String())
		}
	}
}

func TestVMImagesCmdPrunePromptsAndRemovesOldCache(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	currentDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v8")

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  readWriter{Reader: strings.NewReader("y\n"), Writer: &out},
	}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Remove prunable VM images?") {
		t.Fatalf("prune output missing confirmation prompt:\n%s", got)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old cache dir stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("current cache dir should remain: %v", err)
	}
	for _, want := range []string{"cache", "removed", "ubuntu-26.04-amd64-v7", "current", "ubuntu-26.04-amd64-v8"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prune output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesCmdPruneNoLeavesOldCache(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v8")

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  readWriter{Reader: strings.NewReader("\n"), Writer: &out},
	}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune no: %v", err)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("old cache dir should remain after declining prune: %v", err)
	}
	if strings.Contains(out.String(), "removed") {
		t.Fatalf("declined prune should not report removed rows:\n%s", out.String())
	}
}

func TestVMImagesCmdPruneYesBypassesPrompt(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v8")

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table", Yes: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune --yes: %v", err)
	}
	if strings.Contains(out.String(), "Remove prunable VM images?") {
		t.Fatalf("--yes should bypass prompt:\n%s", out.String())
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old cache dir stat err = %v, want not exist", err)
	}
}

func TestVMImagesCmdPruneKeepsLocalImportedImage(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	seedCachedVMImage(t, cacheRoot, testUbuntuVMImageVersion)
	importer := localVMImageImporter{
		CacheRoot: cacheRoot,
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

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", Yes: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune --yes: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old managed cache dir stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(ref.Root); err != nil {
		t.Fatalf("local imported image root should remain: %v", err)
	}
	if _, err := os.Stat(localVMImageRefPath(cacheRoot, "foo/bar")); err != nil {
		t.Fatalf("local imported image ref should remain: %v", err)
	}
	if strings.Contains(out.String(), ref.Version) {
		t.Fatalf("prune output should not include local image version %q:\n%s", ref.Version, out.String())
	}
}

func TestVMImagesCmdPruneKeepsInUseZFSBaseAndRemovesOldUnreferencedBase(t *testing.T) {
	server := newTestServer(t)
	stubVMImagePruneCatalogFetch(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v8")
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"devbox": {
			Name:        "devbox",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Image: db.VMImageConfig{Payload: testUbuntuVMPayload, Version: "ubuntu-26.04-amd64-v7"},
				Disk:  db.VMDiskConfig{Backend: vmDiskBackendZVOL, Path: "/dev/zvol/flash/yeet/vms/devbox/root"},
			},
		},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	var zfsCalls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		zfsCalls = append(zfsCalls, append([]string(nil), args...))
		switch strings.Join(args, " ") {
		case "list -H -o name -t volume":
			return strings.Join([]string{
				"flash/yeet/vm-images/ubuntu-26.04-amd64-v6/root",
				"flash/yeet/vm-images/ubuntu-26.04-amd64-v7/root",
				"flash/yeet/vm-images/ubuntu-26.04-amd64-v8/root",
			}, "\n") + "\n", "", nil
		case "get -H -o value clones flash/yeet/vm-images/ubuntu-26.04-amd64-v6/root@ubuntu-26.04-amd64-v6":
			return "-\n", "", nil
		case "get -H -o value clones flash/yeet/vm-images/ubuntu-26.04-amd64-v7/root@ubuntu-26.04-amd64-v7":
			return "flash/yeet/vms/devbox/root\n", "", nil
		case "get -H -o value clones flash/yeet/vm-images/ubuntu-26.04-amd64-v8/root@ubuntu-26.04-amd64-v8":
			return "-\n", "", nil
		case "destroy flash/yeet/vm-images/ubuntu-26.04-amd64-v6/root@ubuntu-26.04-amd64-v6",
			"destroy flash/yeet/vm-images/ubuntu-26.04-amd64-v6/root",
			"destroy flash/yeet/vm-images/ubuntu-26.04-amd64-v6":
			return "", "", nil
		default:
			return "", "unexpected zfs command: " + strings.Join(args, " "), errors.New("unexpected zfs command")
		}
	}

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", Yes: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune zfs: %v\n%s", err, out.String())
	}

	rows := decodeVMImagePruneRows(t, out.Bytes())
	assertPruneRow(t, rows, "zfs-base", "ubuntu-26.04-amd64-v6", "removed", "flash/yeet/vm-images/ubuntu-26.04-amd64-v6/root")
	assertPruneRow(t, rows, "zfs-base", "ubuntu-26.04-amd64-v7", "in-use", "flash/yeet/vm-images/ubuntu-26.04-amd64-v7/root")
	assertPruneRow(t, rows, "zfs-base", "ubuntu-26.04-amd64-v8", "current", "flash/yeet/vm-images/ubuntu-26.04-amd64-v8/root")
	for _, forbidden := range [][]string{
		{"destroy", "flash/yeet/vm-images/ubuntu-26.04-amd64-v7/root@ubuntu-26.04-amd64-v7"},
		{"destroy", "-R", "flash/yeet/vm-images/ubuntu-26.04-amd64-v6"},
	} {
		for _, call := range zfsCalls {
			if reflect.DeepEqual(call, forbidden) {
				t.Fatalf("unexpected zfs call %#v in %#v", forbidden, zfsCalls)
			}
		}
	}
}

func TestVMImagesCmdUpdateEnsuresImageAndPrintsState(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", testNixOSVMImageVersion)
	var ensuredPayloads []string
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensuredPayloads = append(ensuredPayloads, payload)
		return vmImageAsset{
			Paths: vmImagePaths{Dir: cachePath},
			Manifest: vmImageManifest{
				Version: testNixOSVMImageVersion,
			},
		}, nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"update", testNixOSVMPayload}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if !reflect.DeepEqual(ensuredPayloads, []string{testNixOSVMPayload}) {
		t.Fatalf("ensure payloads = %#v", ensuredPayloads)
	}
	got := out.String()
	for _, want := range []string{testNixOSVMPayload, vmImageCacheCurrent, testNixOSVMImageVersion, cachePath} {
		if !strings.Contains(got, want) {
			t.Fatalf("update output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesUpdateWithoutArgsUsesCatalogPayloads(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:        "vm://debian/13",
			Name:           "Debian 13",
			Architecture:   "amd64",
			ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json",
			VersionPrefix:  "debian-13-amd64-",
			DefaultUser:    "debian",
			MetadataDriver: "ubuntu",
			Default:        true,
		},
	}})
	var seen []string
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		seen = append(seen, payload)
		return fakeVMImageAssetVersion(t, "debian-13-amd64-v1")
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update"}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if !reflect.DeepEqual(seen, []string{"vm://debian/13"}) {
		t.Fatalf("updated payloads = %#v", seen)
	}
}

func TestVMImagesUpdateWithoutArgsFetchesCatalogForUpdateAndPrune(t *testing.T) {
	server := newTestServer(t)
	catalog := vmImageSingleFetchCommandCatalog(t, "vm://debian/13", "debian-13-amd64-v1", true)
	calls := stubVMImageCatalogFetchCounting(t, catalog)
	stubPrepareVMRootFSIdentity(t)

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update"}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if got := calls(); got != 2 {
		t.Fatalf("catalog fetch calls = %d, want 2", got)
	}
	var state vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if state.Payload != "vm://debian/13" || state.ManifestURL != catalog.Images[0].ManifestURL {
		t.Fatalf("state = %#v", state)
	}
}

func TestVMImagesUpdatePayloadFetchesCatalogForUpdateAndPrune(t *testing.T) {
	server := newTestServer(t)
	catalog := vmImageSingleFetchCommandCatalog(t, "vm://debian/13", "debian-13-amd64-v1", true)
	calls := stubVMImageCatalogFetchCounting(t, catalog)
	stubPrepareVMRootFSIdentity(t)

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update", "vm://debian/13"}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if got := calls(); got != 2 {
		t.Fatalf("catalog fetch calls = %d, want 2", got)
	}
	var state vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if state.Payload != "vm://debian/13" || state.ManifestURL != catalog.Images[0].ManifestURL {
		t.Fatalf("state = %#v", state)
	}
}

func TestVMImagesCmdUpdateAllOfficialImagesByDefault(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	var ensured []string
	restoreEnsure := stubVMImageEnsure(t, func(_ context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensured = append(ensured, payload)
		version := testUbuntuVMImageVersion
		if payload == testNixOSVMPayload {
			version = testNixOSVMImageVersion
		}
		return vmImageAsset{
			Paths:    vmImagePaths{Dir: filepath.Join(cache.Root, version)},
			Manifest: vmImageManifest{Version: version},
		}, nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update"}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if !reflect.DeepEqual(ensured, []string{testNixOSVMPayload, testUbuntuVMPayload}) {
		t.Fatalf("ensured = %#v", ensured)
	}
	var states []vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &states); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if len(states) != 2 || states[0].Payload != testNixOSVMPayload || states[1].Payload != testUbuntuVMPayload {
		t.Fatalf("states = %#v", states)
	}
}

func TestVMImagesCmdUpdatePrunesOldCacheAfterRefresh(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	currentDir := seedCachedVMImage(t, cacheRoot, testUbuntuVMImageVersion)
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		if payload != testUbuntuVMPayload {
			t.Fatalf("ensure payload = %q, want %q", payload, testUbuntuVMPayload)
		}
		return vmImageAsset{
			Paths:    vmImagePaths{Dir: currentDir},
			Manifest: vmImageManifest{Version: testUbuntuVMImageVersion},
		}, nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update", testUbuntuVMPayload}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old cache dir stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("current cache dir should remain: %v", err)
	}
}

func TestVMImagesCmdUpdateJSONSuppressesProgress(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", testUbuntuVMImageVersion)
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		if ui != nil {
			ui.Start()
			ui.StartStep("Download VM image")
			ui.UpdateDetail("progress should not precede json")
			ui.DoneStep("done")
			ui.Stop()
		}
		return vmImageAsset{
			Paths: vmImagePaths{Dir: cachePath},
			Manifest: vmImageManifest{
				Version: testUbuntuVMImageVersion,
			},
		}, nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update", testUbuntuVMPayload}); err != nil {
		t.Fatalf("vmImagesCmdFunc update json: %v", err)
	}

	var got vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if got.State != vmImageCacheCurrent || got.LatestVersion != testUbuntuVMImageVersion || got.CachePath != cachePath {
		t.Fatalf("json state = %#v", got)
	}
	if strings.Contains(out.String(), "progress should not precede json") {
		t.Fatalf("json output contains progress text: %q", out.String())
	}
}

func TestVMImagesCmdRejectsInvalidAction(t *testing.T) {
	server := newTestServer(t)
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &bytes.Buffer{}}
	err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"refresh"})
	if err == nil || !strings.Contains(err.Error(), "usage: yeet vm images [ls|catalog|update|import <name>|rm <name>|prune]") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMCmdFuncRoutesImagesAndParsesFormat(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalogFetch(t, vmImageTestCatalog())
	want := vmImageCacheState{
		Payload:       testUbuntuVMPayload,
		CachedVersion: "ubuntu-26.04-amd64-v1",
		LatestVersion: "ubuntu-26.04-amd64-v1",
		State:         vmImageCacheCurrent,
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v1"),
		ManifestURL:   testDefaultVMImageManifest,
	}
	restore := stubVMImageInspectMap(t, map[string]vmImageCacheState{
		testUbuntuVMPayload: want,
		testNixOSVMPayload: {
			Payload:       testNixOSVMPayload,
			LatestVersion: testNixOSVMImageVersion,
			State:         vmImageCacheMissing,
		},
	})
	defer restore()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmCmdFunc([]string{"images", "--format=json"}); err != nil {
		t.Fatalf("vmCmdFunc: %v", err)
	}

	var got []vmImageListRowJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if len(got) != 2 {
		t.Fatalf("row count = %d, want 2: %#v", len(got), got)
	}
	byPayload := vmImageRowsByPayload(got)
	wantRow := vmImageListRowJSON{
		Payload:   want.Payload,
		Kind:      "builtin",
		State:     string(want.State),
		Version:   want.LatestVersion,
		CachePath: want.CachePath,
	}
	if byPayload[testUbuntuVMPayload] != wantRow {
		t.Fatalf("json row = %#v, want %#v", byPayload[testUbuntuVMPayload], wantRow)
	}
}

type vmImageListRowJSON struct {
	Payload      string `json:"payload"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Version      string `json:"version,omitempty"`
	CachePath    string `json:"cachePath,omitempty"`
	KernelPolicy string `json:"kernelPolicy,omitempty"`
}

type vmImageCatalogRowJSON struct {
	Payload       string `json:"payload"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	DefaultUser   string `json:"defaultUser,omitempty"`
	VersionPrefix string `json:"versionPrefix,omitempty"`
	KernelPolicy  string `json:"kernelPolicy,omitempty"`
}

type vmImagePruneRowJSON struct {
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Payload string `json:"payload,omitempty"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func vmImageRowsByPayload(rows []vmImageListRowJSON) map[string]vmImageListRowJSON {
	out := make(map[string]vmImageListRowJSON, len(rows))
	for _, row := range rows {
		out[row.Payload] = row
	}
	return out
}

func vmImageCatalogRowsByPayload(rows []vmImageCatalogRowJSON) map[string]vmImageCatalogRowJSON {
	out := make(map[string]vmImageCatalogRowJSON, len(rows))
	for _, row := range rows {
		out[row.Payload] = row
	}
	return out
}

func seedCachedVMImage(t *testing.T, root, version string) string {
	t.Helper()
	contents := vmImageTestContents()
	manifest := vmImageTestManifest(version, contents)
	dir := writeCachedVMImageManifest(t, root, manifest)
	writeCachedVMImageArtifacts(t, dir, contents)
	return dir
}

func decodeVMImagePruneRows(t *testing.T, raw []byte) []vmImagePruneRowJSON {
	t.Helper()
	var rows []vmImagePruneRowJSON
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode prune rows: %v\n%s", err, string(raw))
	}
	return rows
}

func assertPruneRow(t *testing.T, rows []vmImagePruneRowJSON, kind, version, state, path string) {
	t.Helper()
	for _, row := range rows {
		if row.Kind == kind && row.Version == version {
			if row.State != state || row.Path != path {
				t.Fatalf("row %s %s = %#v, want state=%q path=%q", kind, version, row, state, path)
			}
			return
		}
	}
	t.Fatalf("missing prune row kind=%s version=%s in %#v", kind, version, rows)
}

func assertPruneRowPayload(t *testing.T, rows []vmImagePruneRowJSON, version, payload string) {
	t.Helper()
	for _, row := range rows {
		if row.Version == version {
			if row.Payload != payload {
				t.Fatalf("row %s payload = %q, want %q", version, row.Payload, payload)
			}
			return
		}
	}
	t.Fatalf("missing prune row version=%s in %#v", version, rows)
}

func stubVMImageInspect(t *testing.T, state vmImageCacheState) func() {
	t.Helper()
	return stubVMImageInspectMap(t, map[string]vmImageCacheState{
		testUbuntuVMPayload: state,
		testNixOSVMPayload: {
			Payload:       testNixOSVMPayload,
			LatestVersion: testNixOSVMImageVersion,
			State:         vmImageCacheMissing,
		},
	})
}

func stubVMImageInspectMap(t *testing.T, states map[string]vmImageCacheState) func() {
	t.Helper()
	old := vmImageInspectFunc
	oldCatalog := vmImageInspectCatalogFunc
	inspect := func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		state, ok := states[payload]
		if !ok {
			t.Fatalf("unexpected inspect payload %q", payload)
		}
		if strings.TrimSpace(cache.Root) == "" {
			t.Fatal("inspect cache root is empty")
		}
		return state, vmImageManifest{Version: state.LatestVersion}, nil
	}
	vmImageInspectFunc = inspect
	vmImageInspectCatalogFunc = func(ctx context.Context, cache vmImageCache, image vmImageCatalogImage) (vmImageCacheState, vmImageManifest, error) {
		return inspect(ctx, cache.withManifestURL(image.ManifestURL), image.Payload)
	}
	return func() {
		vmImageInspectFunc = old
		vmImageInspectCatalogFunc = oldCatalog
	}
}

func stubVMImageInspectFail(t *testing.T) func() {
	t.Helper()
	old := vmImageInspectFunc
	oldCatalog := vmImageInspectCatalogFunc
	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		t.Fatalf("catalog should not inspect cache state for %q", payload)
		return vmImageCacheState{}, vmImageManifest{}, nil
	}
	vmImageInspectCatalogFunc = func(ctx context.Context, cache vmImageCache, image vmImageCatalogImage) (vmImageCacheState, vmImageManifest, error) {
		t.Fatalf("catalog should not inspect cache state for %q", image.Payload)
		return vmImageCacheState{}, vmImageManifest{}, nil
	}
	return func() {
		vmImageInspectFunc = old
		vmImageInspectCatalogFunc = oldCatalog
	}
}

func stubVMImageEnsure(t *testing.T, fn func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error)) func() {
	t.Helper()
	old := vmImageEnsureFunc
	oldCatalog := vmImageEnsureCatalogFunc
	vmImageEnsureFunc = fn
	vmImageEnsureCatalogFunc = func(ctx context.Context, cache vmImageCache, image vmImageCatalogImage, ui ProgressUI) (vmImageAsset, error) {
		return fn(ctx, cache.withManifestURL(image.ManifestURL), image.Payload, ui)
	}
	return func() {
		vmImageEnsureFunc = old
		vmImageEnsureCatalogFunc = oldCatalog
	}
}

func stubVMImageCatalogFetchOnce(t *testing.T, catalog vmImageCatalog) func() int {
	t.Helper()
	orig := fetchVMImageCatalogFunc
	calls := 0
	fetchVMImageCatalogFunc = func(context.Context, *http.Client) (vmImageCatalog, error) {
		calls++
		if calls > 1 {
			return vmImageCatalog{}, errors.New("unexpected second VM image catalog fetch")
		}
		return catalog, nil
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })
	return func() int { return calls }
}

func stubVMImageCatalogFetchCounting(t *testing.T, catalog vmImageCatalog) func() int {
	t.Helper()
	orig := fetchVMImageCatalogFunc
	calls := 0
	fetchVMImageCatalogFunc = func(context.Context, *http.Client) (vmImageCatalog, error) {
		calls++
		return catalog, nil
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })
	return func() int { return calls }
}

func vmImageSingleFetchCommandCatalog(t *testing.T, payload, version string, def bool) vmImageCatalog {
	t.Helper()
	contents := vmImageTestContents()
	manifest := vmImageTestManifest(version, contents)
	artifactServer := newVMImageArtifactTestServer(t, manifest, contents)
	t.Cleanup(artifactServer.Close)
	return vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{{
			Payload:        payload,
			Name:           payload,
			Architecture:   "amd64",
			ManifestURL:    artifactServer.URL + "/manifest.json",
			VersionPrefix:  strings.TrimSuffix(version, "v1"),
			DefaultUser:    "debian",
			MetadataDriver: "ubuntu",
			Default:        def,
		}},
	}
}

func stubPrepareVMRootFSIdentity(t *testing.T) {
	t.Helper()
	oldPrepare := prepareVMRootFSFunc
	prepareVMRootFSFunc = func(_ context.Context, source string) (string, error) {
		return source, nil
	}
	t.Cleanup(func() { prepareVMRootFSFunc = oldPrepare })
}

func stubManagedVMImageAsset(t *testing.T, asset vmImageAsset) func() {
	t.Helper()
	return stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		if payload != testUbuntuVMPayload {
			t.Fatalf("ensure payload = %q, want %q", payload, testUbuntuVMPayload)
		}
		if cache.Root == "" {
			t.Fatal("ensure cache root is empty")
		}
		return asset, nil
	})
}
