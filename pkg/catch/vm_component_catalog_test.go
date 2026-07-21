// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestVMComponentCatalogStrictDecodeAndImmutableChannelResolution(t *testing.T) {
	guestCatalog, err := decodeVMGuestBaseCatalog(vmGuestBaseCatalogFixture(t), true)
	if err != nil {
		t.Fatalf("decode guest catalog: %v", err)
	}
	guest, ok := guestCatalog.GuestBaseForChannel("ubuntu-26.04-amd64", "stable")
	if !ok {
		t.Fatal("stable guest channel did not resolve")
	}
	if guest.GuestBaseID != "guest-ubuntu-26.04-amd64-v1" || guest.ManifestSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("stable guest = %#v", guest)
	}
	if byID, ok := guestCatalog.GuestBaseByID(guest.GuestBaseID); !ok || byID != guest {
		t.Fatalf("guest by ID = %#v ok=%v", byID, ok)
	}

	kernelCatalog, err := decodeVMKernelCatalog(vmKernelCatalogFixture(t), true)
	if err != nil {
		t.Fatalf("decode kernel catalog: %v", err)
	}
	kernel, ok := kernelCatalog.KernelForChannel("amd64", "stable")
	if !ok {
		t.Fatal("stable kernel channel did not resolve")
	}
	if kernel.KernelID != "kernel-linux-7.1.1-yeet-v1" || kernel.ManifestSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("stable kernel = %#v", kernel)
	}
	if byID, ok := kernelCatalog.KernelByID(kernel.KernelID); !ok || byID != kernel {
		t.Fatalf("kernel by ID = %#v ok=%v", byID, ok)
	}
}

func TestVMComponentCatalogRejectsUnknownAndConflictingFields(t *testing.T) {
	var guest map[string]any
	if err := json.Unmarshal(vmGuestBaseCatalogFixture(t), &guest); err != nil {
		t.Fatal(err)
	}
	guest["unexpected"] = true
	raw, err := json.Marshal(guest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMGuestBaseCatalog(raw, true); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown guest field error = %v", err)
	}

	var kernel map[string]any
	if err := json.Unmarshal(vmKernelCatalogFixture(t), &kernel); err != nil {
		t.Fatal(err)
	}
	channels := kernel["channels"].(map[string]any)
	amd64 := channels["amd64"].(map[string]any)
	stable := amd64["stable"].(map[string]any)
	stable["manifest_sha256"] = strings.Repeat("f", 64)
	raw, err = json.Marshal(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMKernelCatalog(raw, true); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("conflicting kernel channel error = %v", err)
	}
}

func TestVMComponentCatalogRejectsMissingNestedChannelField(t *testing.T) {
	var guest map[string]any
	if err := json.Unmarshal(vmGuestBaseCatalogFixture(t), &guest); err != nil {
		t.Fatal(err)
	}
	channels := guest["channels"].(map[string]any)
	ubuntu := channels["ubuntu-26.04-amd64"].(map[string]any)
	delete(ubuntu, "candidate")
	raw, err := json.Marshal(guest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMGuestBaseCatalog(raw, true); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("missing candidate error = %v", err)
	}
}

func TestVMComponentCatalogFetchesAllAdditiveCatalogs(t *testing.T) {
	runtimeRaw, err := json.Marshal(validVMRuntimeCatalog())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/guest-catalog.json":
			_, _ = w.Write(vmGuestBaseCatalogFixture(t))
		case "/kernel-catalog.json":
			_, _ = w.Write(vmKernelCatalogFixture(t))
		case "/runtime-catalog.json":
			_, _ = w.Write(runtimeRaw)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	refs := vmImageComponentCatalogs{
		GuestBases: server.URL + "/guest-catalog.json",
		Kernels:    server.URL + "/kernel-catalog.json",
		Runtimes:   server.URL + "/runtime-catalog.json",
	}
	got, err := fetchVMComponentCatalogs(context.Background(), server.Client(), refs, false)
	if err != nil {
		t.Fatalf("fetch component catalogs: %v", err)
	}
	if _, ok := got.GuestBases.GuestBaseForChannel("ubuntu-26.04-amd64", "stable"); !ok {
		t.Fatal("fetched guest stable channel did not resolve")
	}
	if _, ok := got.Kernels.KernelForChannel("amd64", "stable"); !ok {
		t.Fatal("fetched kernel stable channel did not resolve")
	}
	if _, ok := got.Runtimes.RuntimeForChannel("amd64", "stable"); !ok {
		t.Fatal("fetched runtime stable channel did not resolve")
	}
}

func TestVMComponentCacheRootsUseConfiguredCatchDataRoot(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "custom-data")
	server := &Server{cfg: Config{RootDir: dataRoot}}
	if got, want := server.vmGuestBaseCache().Root, filepath.Join(dataRoot, "vm-guest-bases"); got != want {
		t.Fatalf("guest cache root = %q, want %q", got, want)
	}
	if got, want := server.vmKernelArtifactCache().Root, filepath.Join(dataRoot, "vm-kernels"); got != want {
		t.Fatalf("kernel cache root = %q, want %q", got, want)
	}
}

func vmGuestBaseCatalogFixture(t testing.TB) []byte {
	t.Helper()
	return []byte(`{
		"schema_version": 1,
		"guest_bases": [{
			"guest_base_id": "guest-ubuntu-26.04-amd64-v1",
			"os": "ubuntu",
			"os_version": "26.04",
			"architecture": "amd64",
			"manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/guest-ubuntu-26.04-amd64-v1/guest-manifest.json",
			"manifest_sha256": "` + strings.Repeat("a", 64) + `"
		}],
		"channels": {
			"nixos-26.05-amd64": {"stable": null, "candidate": null},
			"ubuntu-26.04-amd64": {
				"stable": {
					"guest_base_id": "guest-ubuntu-26.04-amd64-v1",
					"manifest_sha256": "` + strings.Repeat("a", 64) + `"
				},
				"candidate": null
			}
		}
	}`)
}

func vmKernelCatalogFixture(t testing.TB) []byte {
	t.Helper()
	return []byte(`{
		"schema_version": 1,
		"kernels": [{
			"kernel_id": "kernel-linux-7.1.1-yeet-v1",
			"upstream_version": "7.1.1",
			"packaging_revision": 1,
			"architecture": "amd64",
			"manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/kernel-linux-7.1.1-yeet-v1/kernel-manifest.json",
			"manifest_sha256": "` + strings.Repeat("b", 64) + `"
		}],
		"channels": {
			"amd64": {
				"stable": {
					"kernel_id": "kernel-linux-7.1.1-yeet-v1",
					"manifest_sha256": "` + strings.Repeat("b", 64) + `"
				},
				"candidate": null
			}
		}
	}`)
}

func FuzzParseVMGuestBaseCatalog(f *testing.F) {
	f.Add(vmGuestBaseCatalogFixture(f))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMGuestBaseCatalog(raw, true)
	})
}

func FuzzParseVMKernelCatalog(f *testing.F) {
	f.Add(vmKernelCatalogFixture(f))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMKernelCatalog(raw, true)
	})
}
