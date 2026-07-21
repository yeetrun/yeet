// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestVMKernelArtifactCachePublishesVerifiedKernelUnderDataRoot(t *testing.T) {
	fixture := newVMKernelArtifactCacheFixture(t)
	dataRoot := t.TempDir()
	cache := vmKernelArtifactCache{
		Root:   filepath.Join(dataRoot, "vm-kernels"),
		Client: fixture.server.Client(),
	}
	artifact, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatalf("ensure kernel: %v", err)
	}
	wantDir := filepath.Join(dataRoot, "vm-kernels", "amd64", fixture.ref.KernelID, fixture.ref.ManifestSHA256)
	if artifact.Dir != wantDir || artifact.KernelPath != filepath.Join(wantDir, "vmlinux") || artifact.ConfigPath != filepath.Join(wantDir, "kernel.config") {
		t.Fatalf("artifact paths = %#v, want dir %q", artifact, wantDir)
	}
	if got := artifact.DBConfig(); got.ID != fixture.ref.KernelID || got.ManifestSHA256 != fixture.ref.ManifestSHA256 || got.SHA256 != fixture.manifest.VMLinux.SHA256 || got.Path != artifact.KernelPath || got.Source != "official" {
		t.Fatalf("DB config = %#v", got)
	}
	if got, err := os.ReadFile(artifact.KernelPath); err != nil || string(got) != string(fixture.kernel) {
		t.Fatalf("cached vmlinux = %q, %v", got, err)
	}
	if got, err := os.ReadFile(artifact.ConfigPath); err != nil || string(got) != string(fixture.config) {
		t.Fatalf("cached config = %q, %v", got, err)
	}
	if fixture.kernelRequests.Load() != 1 || fixture.configRequests.Load() != 1 {
		t.Fatalf("payload requests = kernel %d config %d, want 1 each", fixture.kernelRequests.Load(), fixture.configRequests.Load())
	}
}

func TestVMKernelArtifactCacheRejectsPayloadDigestWithoutPublication(t *testing.T) {
	fixture := newVMKernelArtifactCacheFixture(t)
	fixture.manifest.VMLinux.SHA256 = strings.Repeat("0", 64)
	fixture.refresh(t)
	root := t.TempDir()
	cache := vmKernelArtifactCache{Root: root, Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "vmlinux digest mismatch") {
		t.Fatalf("ensure error = %v", err)
	}
	final := filepath.Join(root, "amd64", fixture.ref.KernelID, fixture.ref.ManifestSHA256)
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		t.Fatalf("published invalid kernel: %v", err)
	}
}

func TestVMKernelArtifactManifestRejectsUnknownField(t *testing.T) {
	fixture := newVMKernelArtifactCacheFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(fixture.manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["rootfs"] = "do-not-trust"
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMKernelManifest(raw, false); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown manifest field error = %v", err)
	}
}

func TestVMKernelArtifactManifestRejectsMissingConfigURL(t *testing.T) {
	fixture := newVMKernelArtifactCacheFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(fixture.manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	config := manifest["config"].(map[string]any)
	delete(config, "url")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMKernelManifest(raw, false); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("missing config URL error = %v", err)
	}
}

func FuzzParseVMKernelManifest(f *testing.F) {
	fixture := newVMKernelArtifactCacheFixture(f)
	f.Add(fixture.manifestRaw)
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMKernelManifest(raw, false)
	})
}

type vmKernelArtifactCacheFixture struct {
	server         *httptest.Server
	ref            vmKernelCatalogRef
	manifest       vmKernelManifest
	manifestRaw    []byte
	kernel         []byte
	config         []byte
	kernelRequests atomic.Int64
	configRequests atomic.Int64
}

func newVMKernelArtifactCacheFixture(t testing.TB) *vmKernelArtifactCacheFixture {
	t.Helper()
	fixture := &vmKernelArtifactCacheFixture{
		kernel: []byte("linux kernel fixture"),
		config: []byte("CONFIG_VIRTIO=y\n"),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kernel-manifest.json":
			_, _ = w.Write(fixture.manifestRaw)
		case "/vmlinux":
			fixture.kernelRequests.Add(1)
			_, _ = w.Write(fixture.kernel)
		case "/kernel.config":
			fixture.configRequests.Add(1)
			_, _ = w.Write(fixture.config)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	fixture.manifest = vmKernelManifest{
		SchemaVersion:     1,
		KernelID:          "kernel-linux-7.1.1-yeet-v1",
		UpstreamVersion:   "7.1.1",
		PackagingRevision: 1,
		Architecture:      "amd64",
		VMLinux: vmKernelManifestAsset{
			URL:    fixture.server.URL + "/vmlinux",
			SHA256: vmComponentTestSHA256(fixture.kernel),
		},
		Config: vmKernelManifestAsset{
			URL:    fixture.server.URL + "/kernel.config",
			SHA256: vmComponentTestSHA256(fixture.config),
		},
		GuestPackages: vmKernelManifestGuestPackages{
			CatalogURL:            "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/kernel-packages/catalog.json",
			SelectorSchemaVersion: 2,
			ReleaseID:             "kernel-linux-7.1.1-yeet-v1",
		},
		Provenance: vmComponentManifestProvenance{
			SourceCommit:   strings.Repeat("d", 40),
			WorkflowRunURL: "https://github.com/yeetrun/yeet-vm-images/actions/runs/124",
		},
	}
	fixture.refresh(t)
	return fixture
}

func (f *vmKernelArtifactCacheFixture) refresh(t testing.TB) {
	t.Helper()
	raw, err := json.Marshal(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	f.manifestRaw = raw
	f.ref = vmKernelCatalogRef{
		KernelID:          f.manifest.KernelID,
		UpstreamVersion:   f.manifest.UpstreamVersion,
		PackagingRevision: f.manifest.PackagingRevision,
		Architecture:      f.manifest.Architecture,
		ManifestURL:       f.server.URL + "/kernel-manifest.json",
		ManifestSHA256:    vmComponentTestSHA256(raw),
	}
}
