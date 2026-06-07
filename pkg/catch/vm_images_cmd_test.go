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
	restore := stubVMImageInspect(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: "ubuntu-26.04-amd64-v1",
		LatestVersion: "ubuntu-26.04-amd64-v1",
		State:         vmImageCacheCurrent,
		CachePath:     cachePath,
		ManifestURL:   defaultVMImageManifestURL,
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
	if len(got) != 1 {
		t.Fatalf("row count = %d, want 1: %#v", len(got), got)
	}
	want := vmImageListRowJSON{
		Payload:   vmUbuntu2604Payload,
		Kind:      "builtin",
		State:     string(vmImageCacheCurrent),
		Version:   "ubuntu-26.04-amd64-v1",
		CachePath: cachePath,
	}
	if got[0] != want {
		t.Fatalf("json row = %#v, want %#v", got[0], want)
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
	restore := stubVMImageInspect(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: "ubuntu-26.04-amd64-v0",
		LatestVersion: "ubuntu-26.04-amd64-v1",
		State:         vmImageCacheStale,
		CachePath:     builtinCachePath,
		ManifestURL:   defaultVMImageManifestURL,
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
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion)
	var ensuredPayload string
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensuredPayload = payload
		return vmImageAsset{
			Paths: vmImagePaths{Dir: cachePath},
			Manifest: vmImageManifest{
				Version: defaultVMImageVersion,
			},
		}, nil
	})
	defer restoreEnsure()
	restoreInspect := stubVMImageInspect(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: defaultVMImageVersion,
		LatestVersion: defaultVMImageVersion,
		State:         vmImageCacheCurrent,
		CachePath:     cachePath,
		ManifestURL:   defaultVMImageManifestURL,
	})
	defer restoreInspect()

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "table"}, []string{"update"}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if ensuredPayload != vmUbuntu2604Payload {
		t.Fatalf("ensure payload = %q, want %q", ensuredPayload, vmUbuntu2604Payload)
	}
	got := out.String()
	for _, want := range []string{vmImageCacheCurrent, defaultVMImageVersion, cachePath} {
		if !strings.Contains(got, want) {
			t.Fatalf("update output missing %q:\n%s", want, got)
		}
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
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update"}); err != nil {
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
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update"}); err != nil {
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
	if err == nil || !strings.Contains(err.Error(), "usage: yeet vm images [ls|update|import <name>|rm <name>|prune]") {
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
	restore := stubVMImageInspect(t, want)
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
	if len(got) != 1 {
		t.Fatalf("row count = %d, want 1: %#v", len(got), got)
	}
	wantRow := vmImageListRowJSON{
		Payload:   want.Payload,
		Kind:      "builtin",
		State:     string(want.State),
		Version:   want.LatestVersion,
		CachePath: want.CachePath,
	}
	if got[0] != wantRow {
		t.Fatalf("json row = %#v, want %#v", got[0], wantRow)
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

type vmImagePruneRowJSON struct {
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
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

func stubVMImageInspect(t *testing.T, state vmImageCacheState) func() {
	t.Helper()
	old := vmImageInspectFunc
	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		if payload != vmUbuntu2604Payload {
			t.Fatalf("inspect payload = %q, want %q", payload, vmUbuntu2604Payload)
		}
		wantRoot := filepath.Join(cache.Root)
		if wantRoot == "" {
			t.Fatal("inspect cache root is empty")
		}
		return state, vmImageManifest{Version: state.LatestVersion}, nil
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
