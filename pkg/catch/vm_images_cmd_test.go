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
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v1")
	restore := stubVMImageInspect(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: "ubuntu-26.04-amd64-v0",
		LatestVersion: "ubuntu-26.04-amd64-v1",
		State:         vmImageCacheStale,
		CachePath:     cachePath,
		ManifestURL:   defaultVMImageManifestURL,
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
		vmUbuntu2604Payload,
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
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v1")
	restore := stubVMImageInspectMap(t, map[string]vmImageCacheState{
		vmUbuntu2604Payload: {
			Payload:       vmUbuntu2604Payload,
			CachedVersion: "ubuntu-26.04-amd64-v1",
			LatestVersion: "ubuntu-26.04-amd64-v1",
			State:         vmImageCacheCurrent,
			CachePath:     cachePath,
			ManifestURL:   defaultVMImageManifestURL,
		},
		vmNixOS2605Payload: {
			Payload:       vmNixOS2605Payload,
			LatestVersion: "nixos-26.05-amd64-v1",
			State:         vmImageCacheMissing,
			ManifestURL:   nixos2605VMImageManifestURL,
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
		Payload:   vmUbuntu2604Payload,
		Kind:      "builtin",
		State:     string(vmImageCacheCurrent),
		Version:   "ubuntu-26.04-amd64-v1",
		CachePath: cachePath,
	}
	if byPayload[vmUbuntu2604Payload] != want {
		t.Fatalf("ubuntu json row = %#v, want %#v", byPayload[vmUbuntu2604Payload], want)
	}
	if byPayload[vmNixOS2605Payload].Version != "nixos-26.05-amd64-v1" || byPayload[vmNixOS2605Payload].State != string(vmImageCacheMissing) {
		t.Fatalf("nixos json row = %#v", byPayload[vmNixOS2605Payload])
	}
}

func TestVMImagesCmdListShowsAllOfficialImages(t *testing.T) {
	server := newTestServer(t)
	restore := stubVMImageInspectMap(t, map[string]vmImageCacheState{
		vmUbuntu2604Payload: {
			Payload:       vmUbuntu2604Payload,
			LatestVersion: "ubuntu-26.04-amd64-v14",
			State:         vmImageCacheCurrent,
		},
		vmNixOS2605Payload: {
			Payload:       vmNixOS2605Payload,
			LatestVersion: "nixos-26.05-amd64-v1",
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
	if byPayload[vmUbuntu2604Payload].Version != "ubuntu-26.04-amd64-v14" {
		t.Fatalf("ubuntu row = %#v", byPayload[vmUbuntu2604Payload])
	}
	if byPayload[vmNixOS2605Payload].Version != "nixos-26.05-amd64-v1" {
		t.Fatalf("nixos row = %#v", byPayload[vmNixOS2605Payload])
	}
}

func TestVMImagesCmdCatalogShowsOfficialImagesWithoutCacheInspect(t *testing.T) {
	server := newTestServer(t)
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
	if len(rows) != len(officialVMImages) {
		t.Fatalf("catalog row count = %d, want %d: %#v", len(rows), len(officialVMImages), rows)
	}
	byPayload := vmImageCatalogRowsByPayload(rows)
	if got := byPayload[vmUbuntu2604Payload]; got.Kind != "builtin" || got.Name != "Ubuntu 26.04" || got.DefaultUser != "ubuntu" || got.VersionPrefix != "ubuntu-26.04-amd64-" {
		t.Fatalf("ubuntu catalog row = %#v", got)
	}
	if got := byPayload[vmNixOS2605Payload]; got.Kind != "builtin" || got.Name != "NixOS 26.05" || got.DefaultUser != "nixos" || got.VersionPrefix != "nixos-26.05-amd64-" {
		t.Fatalf("nixos catalog row = %#v", got)
	}
}

func TestVMImagesCmdCatalogIncludesLocalImages(t *testing.T) {
	server := newTestServer(t)
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
		vmUbuntu2604Payload,
		vmNixOS2605Payload,
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
		t.Fatalf("manifest URL override = %q, want empty so official images can select their registry URL", cache.ManifestURL)
	}
	if got := cache.manifestURL(); got != defaultVMImageManifestURL {
		t.Fatalf("manifestURL fallback = %q, want %q", got, defaultVMImageManifestURL)
	}
}

func TestVMImagesCmdImportReadsStdinAndPrintsRef(t *testing.T) {
	server := newTestServer(t)
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
		vmUbuntu2604Payload: {
			Payload:       vmUbuntu2604Payload,
			CachedVersion: "ubuntu-26.04-amd64-v0",
			LatestVersion: "ubuntu-26.04-amd64-v1",
			State:         vmImageCacheStale,
			CachePath:     builtinCachePath,
			ManifestURL:   defaultVMImageManifestURL,
		},
		vmNixOS2605Payload: {
			Payload:       vmNixOS2605Payload,
			LatestVersion: "nixos-26.05-amd64-v1",
			State:         vmImageCacheMissing,
			ManifestURL:   nixos2605VMImageManifestURL,
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
	builtin := byPayload[vmUbuntu2604Payload]
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

func TestVMImagesCmdPruneDryRunPreviewsOldCacheWithoutRemoving(t *testing.T) {
	server := newTestServer(t)
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

func TestVMImagesCmdPruneKeepsCurrentVersionPerOfficialFamily(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldUbuntu := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v12")
	currentUbuntu := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v14")
	currentNixOS := seedCachedVMImage(t, cacheRoot, "nixos-26.05-amd64-v1")

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", DryRun: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune dry-run: %v", err)
	}

	rows := decodeVMImagePruneRows(t, out.Bytes())
	assertPruneRow(t, rows, "cache", "ubuntu-26.04-amd64-v12", "prunable", oldUbuntu)
	assertPruneRow(t, rows, "cache", "ubuntu-26.04-amd64-v14", "current", currentUbuntu)
	assertPruneRow(t, rows, "cache", "nixos-26.05-amd64-v1", "current", currentNixOS)
	assertPruneRowPayload(t, rows, "ubuntu-26.04-amd64-v14", vmUbuntu2604Payload)
	assertPruneRowPayload(t, rows, "nixos-26.05-amd64-v1", vmNixOS2605Payload)
}

func TestVMImagesCmdPruneTableShowsPayload(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v14")
	seedCachedVMImage(t, cacheRoot, "nixos-26.05-amd64-v1")

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table", DryRun: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune dry-run: %v", err)
	}
	for _, want := range []string{"PAYLOAD", vmUbuntu2604Payload, vmNixOS2605Payload} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("prune table missing %q:\n%s", want, out.String())
		}
	}
}

func TestVMImagesCmdPrunePromptsAndRemovesOldCache(t *testing.T) {
	server := newTestServer(t)
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
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	seedCachedVMImage(t, cacheRoot, defaultVMImageVersion)
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
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v8")
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"devbox": {
			Name:        "devbox",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Image: db.VMImageConfig{Payload: vmUbuntu2604Payload, Version: "ubuntu-26.04-amd64-v7"},
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
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", "nixos-26.05-amd64-v1")
	var ensuredPayloads []string
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensuredPayloads = append(ensuredPayloads, payload)
		return vmImageAsset{
			Paths: vmImagePaths{Dir: cachePath},
			Manifest: vmImageManifest{
				Version: "nixos-26.05-amd64-v1",
			},
		}, nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"update", vmNixOS2605Payload}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if !reflect.DeepEqual(ensuredPayloads, []string{vmNixOS2605Payload}) {
		t.Fatalf("ensure payloads = %#v", ensuredPayloads)
	}
	got := out.String()
	for _, want := range []string{vmNixOS2605Payload, vmImageCacheCurrent, "nixos-26.05-amd64-v1", cachePath} {
		if !strings.Contains(got, want) {
			t.Fatalf("update output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesCmdUpdateAllOfficialImagesByDefault(t *testing.T) {
	server := newTestServer(t)
	var ensured []string
	restoreEnsure := stubVMImageEnsure(t, func(_ context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensured = append(ensured, payload)
		version := defaultVMImageVersion
		if payload == vmNixOS2605Payload {
			version = "nixos-26.05-amd64-v1"
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
	if !reflect.DeepEqual(ensured, []string{vmUbuntu2604Payload, vmNixOS2605Payload}) {
		t.Fatalf("ensured = %#v", ensured)
	}
	var states []vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &states); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if len(states) != 2 || states[0].Payload != vmUbuntu2604Payload || states[1].Payload != vmNixOS2605Payload {
		t.Fatalf("states = %#v", states)
	}
}

func TestVMImagesCmdUpdatePrunesOldCacheAfterRefresh(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v7")
	currentDir := seedCachedVMImage(t, cacheRoot, defaultVMImageVersion)
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		if payload != vmUbuntu2604Payload {
			t.Fatalf("ensure payload = %q, want %q", payload, vmUbuntu2604Payload)
		}
		return vmImageAsset{
			Paths:    vmImagePaths{Dir: currentDir},
			Manifest: vmImageManifest{Version: defaultVMImageVersion},
		}, nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update", vmUbuntu2604Payload}); err != nil {
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
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion)
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
				Version: defaultVMImageVersion,
			},
		}, nil
	})
	defer restoreEnsure()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update", vmUbuntu2604Payload}); err != nil {
		t.Fatalf("vmImagesCmdFunc update json: %v", err)
	}

	var got vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if got.State != vmImageCacheCurrent || got.LatestVersion != defaultVMImageVersion || got.CachePath != cachePath {
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
	want := vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: "ubuntu-26.04-amd64-v1",
		LatestVersion: "ubuntu-26.04-amd64-v1",
		State:         vmImageCacheCurrent,
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v1"),
		ManifestURL:   defaultVMImageManifestURL,
	}
	restore := stubVMImageInspectMap(t, map[string]vmImageCacheState{
		vmUbuntu2604Payload: want,
		vmNixOS2605Payload: {
			Payload:       vmNixOS2605Payload,
			LatestVersion: "nixos-26.05-amd64-v1",
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
	if byPayload[vmUbuntu2604Payload] != wantRow {
		t.Fatalf("json row = %#v, want %#v", byPayload[vmUbuntu2604Payload], wantRow)
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
		vmUbuntu2604Payload: state,
		vmNixOS2605Payload: {
			Payload:       vmNixOS2605Payload,
			LatestVersion: "nixos-26.05-amd64-v1",
			State:         vmImageCacheMissing,
		},
	})
}

func stubVMImageInspectMap(t *testing.T, states map[string]vmImageCacheState) func() {
	t.Helper()
	old := vmImageInspectFunc
	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		state, ok := states[payload]
		if !ok {
			t.Fatalf("unexpected inspect payload %q", payload)
		}
		if strings.TrimSpace(cache.Root) == "" {
			t.Fatal("inspect cache root is empty")
		}
		return state, vmImageManifest{Version: state.LatestVersion}, nil
	}
	return func() { vmImageInspectFunc = old }
}

func stubVMImageInspectFail(t *testing.T) func() {
	t.Helper()
	old := vmImageInspectFunc
	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		t.Fatalf("catalog should not inspect cache state for %q", payload)
		return vmImageCacheState{}, vmImageManifest{}, nil
	}
	return func() { vmImageInspectFunc = old }
}

func stubVMImageEnsure(t *testing.T, fn func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error)) func() {
	t.Helper()
	old := vmImageEnsureFunc
	vmImageEnsureFunc = fn
	return func() { vmImageEnsureFunc = old }
}

func stubManagedVMImageAsset(t *testing.T, asset vmImageAsset) func() {
	t.Helper()
	return stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		if payload != vmUbuntu2604Payload {
			t.Fatalf("ensure payload = %q, want %q", payload, vmUbuntu2604Payload)
		}
		if cache.Root == "" {
			t.Fatal("ensure cache root is empty")
		}
		return asset, nil
	})
}
