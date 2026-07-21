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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestVMGuestBaseCachePublishesImmutableArtifactUnderDataRoot(t *testing.T) {
	fixture := newVMGuestBaseCacheFixture(t)
	dataRoot := t.TempDir()
	cache := vmGuestBaseCache{
		Root:   filepath.Join(dataRoot, "vm-guest-bases"),
		Client: fixture.server.Client(),
	}
	artifact, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatalf("ensure guest base: %v", err)
	}
	wantDir := filepath.Join(dataRoot, "vm-guest-bases", "amd64", fixture.ref.GuestBaseID, fixture.ref.ManifestSHA256)
	if artifact.Dir != wantDir || artifact.RootFSPath != filepath.Join(wantDir, "rootfs.ext4.zst") {
		t.Fatalf("artifact paths = %#v, want dir %q", artifact, wantDir)
	}
	if artifact.ManifestSHA256 != fixture.ref.ManifestSHA256 || artifact.Manifest.GuestBaseID != fixture.ref.GuestBaseID {
		t.Fatalf("artifact identity = %#v", artifact)
	}
	if got := artifact.DBConfig(); got.ID != fixture.ref.GuestBaseID || got.ManifestSHA256 != fixture.ref.ManifestSHA256 || got.Source != "official" {
		t.Fatalf("DB config = %#v", got)
	}
	if got, err := os.ReadFile(artifact.RootFSPath); err != nil || string(got) != string(fixture.rootfs) {
		t.Fatalf("cached rootfs = %q, %v", got, err)
	}
	if got := fixture.rootfsRequests.Load(); got != 1 {
		t.Fatalf("rootfs requests = %d, want 1", got)
	}

	again, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatalf("revalidate guest base: %v", err)
	}
	if again.Dir != artifact.Dir {
		t.Fatalf("revalidated artifact dir = %q, want %q", again.Dir, artifact.Dir)
	}
	if got := fixture.rootfsRequests.Load(); got != 1 {
		t.Fatalf("rootfs requests after revalidation = %d, want 1", got)
	}
}

func TestVMGuestBaseCacheQuarantinesCorruptPublishedEntry(t *testing.T) {
	fixture := newVMGuestBaseCacheFixture(t)
	root := t.TempDir()
	cache := vmGuestBaseCache{Root: root, Client: fixture.server.Client()}
	artifact, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact.RootFSPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	repaired, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatalf("repair guest cache: %v", err)
	}
	if got, err := os.ReadFile(repaired.RootFSPath); err != nil || string(got) != string(fixture.rootfs) {
		t.Fatalf("repaired rootfs = %q, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(repaired.Dir))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "." + fixture.ref.ManifestSHA256 + ".quarantine-"
	found := false
	for _, entry := range entries {
		found = found || strings.HasPrefix(entry.Name(), prefix)
	}
	if !found {
		t.Fatalf("cache entries = %v, want quarantine prefix %q", entries, prefix)
	}
	if got := fixture.rootfsRequests.Load(); got != 2 {
		t.Fatalf("rootfs requests = %d, want redownload after quarantine", got)
	}
}

func TestVMGuestBaseCacheConcurrentEnsureDownloadsOnce(t *testing.T) {
	fixture := newVMGuestBaseCacheFixture(t)
	cache := vmGuestBaseCache{Root: t.TempDir(), Client: fixture.server.Client()}
	const callers = 6
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cache.ensure(context.Background(), fixture.ref, false)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure: %v", err)
		}
	}
	if got := fixture.rootfsRequests.Load(); got != 1 {
		t.Fatalf("rootfs requests = %d, want 1", got)
	}
}

func TestVMGuestBaseCacheRejectsSymlinkRoot(t *testing.T) {
	fixture := newVMGuestBaseCacheFixture(t)
	parent := t.TempDir()
	target := t.TempDir()
	root := filepath.Join(parent, "vm-guest-bases")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	cache := vmGuestBaseCache{Root: root, Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink cache root error = %v", err)
	}
}

func TestVMGuestBaseManifestRejectsUnknownField(t *testing.T) {
	fixture := newVMGuestBaseCacheFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(fixture.manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["firecracker"] = "do-not-trust"
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMGuestBaseManifest(raw, false); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown manifest field error = %v", err)
	}
}

func TestVMGuestBaseManifestRejectsMissingRootFSURL(t *testing.T) {
	fixture := newVMGuestBaseCacheFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(fixture.manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	rootfs := manifest["rootfs"].(map[string]any)
	delete(rootfs, "url")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMGuestBaseManifest(raw, false); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("missing rootfs URL error = %v", err)
	}
}

func FuzzParseVMGuestBaseManifest(f *testing.F) {
	fixture := newVMGuestBaseCacheFixture(f)
	f.Add(fixture.manifestRaw)
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMGuestBaseManifest(raw, false)
	})
}

type vmGuestBaseCacheFixture struct {
	server         *httptest.Server
	ref            vmGuestBaseCatalogRef
	manifest       vmGuestBaseManifest
	manifestRaw    []byte
	rootfs         []byte
	rootfsRequests atomic.Int64
}

func newVMGuestBaseCacheFixture(t testing.TB) *vmGuestBaseCacheFixture {
	t.Helper()
	fixture := &vmGuestBaseCacheFixture{rootfs: []byte("compressed guest rootfs fixture")}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/guest-manifest.json":
			_, _ = w.Write(fixture.manifestRaw)
		case "/rootfs.ext4.zst":
			fixture.rootfsRequests.Add(1)
			_, _ = w.Write(fixture.rootfs)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	fixture.manifest = vmGuestBaseManifest{
		SchemaVersion:        1,
		GuestBaseID:          "guest-ubuntu-26.04-amd64-v1",
		OS:                   "ubuntu",
		OSVersion:            "26.04",
		Architecture:         "amd64",
		DefaultKernelChannel: "stable",
		RootFS: vmGuestBaseManifestRootFS{
			URL:               fixture.server.URL + "/rootfs.ext4.zst",
			SHA256:            vmComponentTestSHA256(fixture.rootfs),
			UncompressedBytes: 4096,
		},
		Provenance: vmComponentManifestProvenance{
			SourceCommit:   strings.Repeat("c", 40),
			WorkflowRunURL: "https://github.com/yeetrun/yeet-vm-images/actions/runs/123",
		},
	}
	fixture.refresh(t)
	return fixture
}

func (f *vmGuestBaseCacheFixture) refresh(t testing.TB) {
	t.Helper()
	raw, err := json.Marshal(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	f.manifestRaw = raw
	f.ref = vmGuestBaseCatalogRef{
		GuestBaseID:    f.manifest.GuestBaseID,
		OS:             f.manifest.OS,
		OSVersion:      f.manifest.OSVersion,
		Architecture:   f.manifest.Architecture,
		ManifestURL:    f.server.URL + "/guest-manifest.json",
		ManifestSHA256: vmComponentTestSHA256(raw),
	}
}

func vmComponentTestSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
