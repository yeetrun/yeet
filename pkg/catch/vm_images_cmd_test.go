// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
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
		"STATE",
		"CACHED",
		"LATEST",
		"CACHE",
		vmUbuntu2604Payload,
		string(vmImageCacheStale),
		"ubuntu-26.04-amd64-v0",
		"ubuntu-26.04-amd64-v1",
		cachePath,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesCmdJSONShowsCacheStateObject(t *testing.T) {
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
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, nil); err != nil {
		t.Fatalf("vmImagesCmdFunc: %v", err)
	}

	var got vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if got != want {
		t.Fatalf("json state = %#v, want %#v", got, want)
	}
}

func TestVMImagesCmdUpdateEnsuresImageAndPrintsState(t *testing.T) {
	server := newTestServer(t)
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v3")
	var ensuredPayload string
	restoreEnsure := stubVMImageEnsure(t, func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensuredPayload = payload
		return vmImageAsset{
			Paths: vmImagePaths{Dir: cachePath},
			Manifest: vmImageManifest{
				Version: "ubuntu-26.04-amd64-v3",
			},
		}, nil
	})
	defer restoreEnsure()
	restoreInspect := stubVMImageInspect(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: "ubuntu-26.04-amd64-v3",
		LatestVersion: "ubuntu-26.04-amd64-v3",
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
	for _, want := range []string{vmImageCacheCurrent, "ubuntu-26.04-amd64-v3", cachePath} {
		if !strings.Contains(got, want) {
			t.Fatalf("update output missing %q:\n%s", want, got)
		}
	}
}

func TestVMImagesCmdUpdateJSONSuppressesProgress(t *testing.T) {
	server := newTestServer(t)
	cachePath := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v3")
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
				Version: "ubuntu-26.04-amd64-v3",
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
	if got.State != vmImageCacheCurrent || got.LatestVersion != "ubuntu-26.04-amd64-v3" || got.CachePath != cachePath {
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
	if err == nil || !strings.Contains(err.Error(), "usage: yeet vm images [update]") {
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

	var got vmImageCacheState
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if got != want {
		t.Fatalf("json state = %#v, want %#v", got, want)
	}
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
