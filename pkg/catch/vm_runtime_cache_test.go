// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMRuntimeCacheEnsurePublishesAndRevalidatesImmutableRuntime(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	var _ func(context.Context, vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) = cache.Ensure

	artifact, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if artifact.ID == "" || artifact.Firecracker == "" || artifact.Jailer == "" {
		t.Fatalf("artifact = %#v", artifact)
	}
	if artifact.ID != fixture.ref.RuntimeID || artifact.ManifestSHA256 != fixture.ref.ManifestSHA || artifact.Source != "official" {
		t.Fatalf("artifact identity = %#v", artifact)
	}
	for _, path := range []string{artifact.Firecracker, artifact.Jailer} {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			t.Fatalf("runtime path %s mode = %v", path, info.Mode())
		}
	}

	again, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatalf("revalidate Ensure: %v", err)
	}
	if again != artifact {
		t.Fatalf("revalidated artifact = %#v, want %#v", again, artifact)
	}
	if got := fixture.componentRequests.Load(); got != 2 {
		t.Fatalf("component requests = %d, want 2", got)
	}
}

func TestVMRuntimeCacheRejectsManifestDigestMismatch(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	fixture.ref.ManifestSHA = strings.Repeat("0", 64)
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "manifest digest mismatch") {
		t.Fatalf("Ensure error = %v, want manifest digest mismatch", err)
	}
}

func TestVMRuntimeCacheRejectsComponentDigestMismatch(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	fixture.manifest.Components.Firecracker.SHA256 = strings.Repeat("0", 64)
	fixture.refreshManifest(t)
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "firecracker digest mismatch") {
		t.Fatalf("Ensure error = %v, want component digest mismatch", err)
	}
}

func TestVMRuntimeCacheRejectsMismatchedVersionProbes(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	fixture.jailer = vmRuntimeTestExecutable("Jailer", "v1.15.0")
	fixture.manifest.Components.Jailer.SHA256 = vmRuntimeTestSHA256(fixture.jailer)
	fixture.refreshManifest(t)
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "version output") {
		t.Fatalf("Ensure error = %v, want version output mismatch", err)
	}
}

func TestVMRuntimeCacheRejectsInterruptedDownloadWithoutPublication(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	fixture.interruptJailer.Store(true)
	root := t.TempDir()
	cache := vmRuntimeCache{Root: root, Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "download jailer") {
		t.Fatalf("Ensure error = %v, want interrupted jailer download", err)
	}
	final := filepath.Join(root, "amd64", fixture.ref.RuntimeID, fixture.ref.ManifestSHA)
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		t.Fatalf("final path after interruption: %v", err)
	}
	parent := filepath.Dir(final)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging entries after interruption = %v", entries)
	}
}

func TestVMRuntimeCacheRejectsSymlinkCacheEntry(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	artifact, err := cache.ensure(context.Background(), fixture.ref, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifact.Firecracker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(artifact.Jailer, artifact.Firecracker); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Ensure error = %v, want symbolic link", err)
	}
}

func TestVMRuntimeCacheRejectsGroupWritableParent(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	root := t.TempDir()
	architectureDir := filepath.Join(root, "amd64")
	if err := os.Mkdir(architectureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(architectureDir, 0o775); err != nil {
		t.Fatal(err)
	}
	cache := vmRuntimeCache{Root: root, Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "group or other writable") {
		t.Fatalf("Ensure error = %v, want writable parent", err)
	}
}

func TestVMRuntimeCacheRejectsWrongArchitecture(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	fixture.manifest.Architecture = "arm64"
	fixture.refreshManifest(t)
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("Ensure error = %v, want architecture", err)
	}
}

func TestVMRuntimeCacheRejectsComponentWrongArchitecture(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	original := inspectVMRuntimeBinaryArchitecture
	inspectVMRuntimeBinaryArchitecture = func(string) (string, error) { return "arm64", nil }
	t.Cleanup(func() { inspectVMRuntimeBinaryArchitecture = original })
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	if _, err := cache.ensure(context.Background(), fixture.ref, false); err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("Ensure error = %v, want component architecture", err)
	}
}

func TestVMRuntimeCacheConcurrentEnsurePublishesOnce(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	cache := vmRuntimeCache{Root: t.TempDir(), Client: fixture.server.Client()}
	const callers = 8
	artifacts := make(chan db.VMRuntimeArtifactConfig, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			artifact, err := cache.ensure(context.Background(), fixture.ref, false)
			artifacts <- artifact
			errs <- err
		}()
	}
	wg.Wait()
	close(artifacts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Ensure: %v", err)
		}
	}
	var first db.VMRuntimeArtifactConfig
	for artifact := range artifacts {
		if first.ID == "" {
			first = artifact
			continue
		}
		if artifact != first {
			t.Fatalf("artifact = %#v, want %#v", artifact, first)
		}
	}
	if got := fixture.componentRequests.Load(); got != 2 {
		t.Fatalf("component requests = %d, want one pair", got)
	}
}

func TestVMRuntimeCacheCompetingPublishersUseNoReplaceWinner(t *testing.T) {
	fixture := newVMRuntimeCacheFixture(t)
	root := t.TempDir()
	parent, err := ensureTrustedVMRuntimeCacheTree(root, fixture.manifest.Architecture, fixture.ref.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(parent, fixture.ref.ManifestSHA)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var successes atomic.Int64
	var conflicts atomic.Int64
	cache := vmRuntimeCache{
		Root:   root,
		Client: fixture.server.Client(),
		publishNoReplace: func(parent, staging, final string) error {
			ready <- struct{}{}
			<-release
			err := publishVMRuntimeCacheNoReplace(parent, staging, final)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, syscall.EEXIST):
				conflicts.Add(1)
			}
			return err
		},
	}
	type result struct {
		artifact db.VMRuntimeArtifactConfig
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			artifact, publishErr := cache.publish(
				context.Background(), parent, final, fixture.manifestRaw,
				fixture.manifest, fixture.ref, false,
			)
			results <- result{artifact: artifact, err: publishErr}
		}()
	}
	<-ready
	<-ready
	close(release)

	var winner db.VMRuntimeArtifactConfig
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("publish: %v", result.err)
		}
		if winner.ID == "" {
			winner = result.artifact
		} else if result.artifact != winner {
			t.Fatalf("loser artifact = %#v, want revalidated winner %#v", result.artifact, winner)
		}
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful no-replace publications = %d, want 1", got)
	}
	if got := conflicts.Load(); got != 1 {
		t.Fatalf("EEXIST no-replace publications = %d, want 1", got)
	}
	if winner.ID != fixture.ref.RuntimeID || winner.ManifestSHA256 != fixture.ref.ManifestSHA {
		t.Fatalf("winner = %#v", winner)
	}
}

type vmRuntimeCacheFixture struct {
	server            *httptest.Server
	ref               vmRuntimeCatalogRef
	manifest          vmRuntimeManifest
	manifestRaw       []byte
	firecracker       []byte
	jailer            []byte
	componentRequests atomic.Int64
	interruptJailer   atomic.Bool
}

func newVMRuntimeCacheFixture(t *testing.T) *vmRuntimeCacheFixture {
	t.Helper()
	originalArchitectureInspector := inspectVMRuntimeBinaryArchitecture
	inspectVMRuntimeBinaryArchitecture = func(string) (string, error) { return "amd64", nil }
	t.Cleanup(func() { inspectVMRuntimeBinaryArchitecture = originalArchitectureInspector })
	fixture := &vmRuntimeCacheFixture{
		manifest:    validVMRuntimeManifest(),
		firecracker: vmRuntimeTestExecutable("Firecracker", "v1.16.1"),
		jailer:      vmRuntimeTestExecutable("Jailer", "v1.16.1"),
	}
	fixture.manifest.Components.Firecracker.SHA256 = vmRuntimeTestSHA256(fixture.firecracker)
	fixture.manifest.Components.Jailer.SHA256 = vmRuntimeTestSHA256(fixture.jailer)
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runtime-manifest.json":
			_, _ = w.Write(fixture.manifestRaw)
		case "/firecracker":
			fixture.componentRequests.Add(1)
			_, _ = w.Write(fixture.firecracker)
		case "/jailer":
			fixture.componentRequests.Add(1)
			if fixture.interruptJailer.Load() {
				w.Header().Set("Content-Length", fmt.Sprint(len(fixture.jailer)+100))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(fixture.jailer[:len(fixture.jailer)/2])
				return
			}
			_, _ = w.Write(fixture.jailer)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	fixture.refreshManifest(t)
	return fixture
}

func (f *vmRuntimeCacheFixture) refreshManifest(t testing.TB) {
	t.Helper()
	f.manifestRaw = marshalVMRuntimeTestJSON(t, f.manifest)
	f.ref = vmRuntimeCatalogRef{
		RuntimeID:       f.manifest.RuntimeID,
		ManifestURL:     f.server.URL + "/runtime-manifest.json",
		ManifestSHA:     vmRuntimeTestSHA256(f.manifestRaw),
		UpstreamVersion: f.manifest.Upstream.Version,
		Support:         f.manifest.Support.State,
	}
}

func vmRuntimeTestExecutable(name, version string) []byte {
	return []byte("#!/bin/sh\nprintf '%s %s\\n' '" + name + "' '" + version + "'\n")
}

func vmRuntimeTestSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
