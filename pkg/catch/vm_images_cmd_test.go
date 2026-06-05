// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
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
	if err == nil || !strings.Contains(err.Error(), "usage: yeet vm images [ls|update|import <name>|rm <name>]") {
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
