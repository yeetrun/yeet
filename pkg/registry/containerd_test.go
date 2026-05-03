// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/containerd/containerd/content"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestParseRepositoryName(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		expected struct {
			domain string
			path   string
		}
	}{
		{
			name: "simple docker hub image",
			repo: "nginx",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/nginx",
			},
		},
		{
			name: "docker hub user image",
			repo: "user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "user/app",
			},
		},
		{
			name: "docker hub user image with single slash",
			repo: "alpine",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/alpine",
			},
		},
		{
			name: "custom registry with user and app",
			repo: "registry.example.com/user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "user/app",
			},
		},
		{
			name: "custom registry with single app",
			repo: "registry.example.com/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "app",
			},
		},
		{
			name: "custom registry with nested path",
			repo: "registry.example.com/org/project/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "org/project/app",
			},
		},
		{
			name: "docker hub with explicit domain",
			repo: "docker.io/nginx",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/nginx",
			},
		},
		{
			name: "docker hub with explicit domain and user",
			repo: "docker.io/user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "user/app",
			},
		},
		{
			name: "localhost registry",
			repo: "localhost:5000/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "localhost:5000",
				path:   "app",
			},
		},
		{
			name: "localhost registry with user",
			repo: "localhost:5000/user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "localhost:5000",
				path:   "user/app",
			},
		},
		{
			name: "single word without dots (treated as docker.io)",
			repo: "myapp",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/myapp",
			},
		},
		{
			name: "two words without dots (treated as docker.io)",
			repo: "user/myapp",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "user/myapp",
			},
		},
		{
			name: "empty string",
			repo: "",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "",
			},
		},
		{
			name: "registry with port and complex path",
			repo: "registry.example.com:8080/org/project/subproject/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com:8080",
				path:   "org/project/subproject/app",
			},
		},
		{
			name: "docker hub official image with explicit library",
			repo: "docker.io/library/nginx",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/nginx",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain, path := ParseRepositoryName(tt.repo)

			if domain != tt.expected.domain {
				t.Errorf("ParseRepositoryName(%q) domain = %q, want %q", tt.repo, domain, tt.expected.domain)
			}

			if path != tt.expected.path {
				t.Errorf("ParseRepositoryName(%q) path = %q, want %q", tt.repo, path, tt.expected.path)
			}
		})
	}
}

func TestParseRepositoryNameEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		expected struct {
			domain string
			path   string
		}
	}{
		{
			name: "multiple slashes in path",
			repo: "registry.example.com/a/b/c/d",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "a/b/c/d",
			},
		},
		{
			name: "registry with subdomain",
			repo: "sub.registry.example.com/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "sub.registry.example.com",
				path:   "app",
			},
		},
		{
			name: "registry with IP address",
			repo: "192.168.1.100:5000/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "192.168.1.100:5000",
				path:   "app",
			},
		},
		{
			name: "registry with hyphen in domain",
			repo: "my-registry.example.com/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "my-registry.example.com",
				path:   "app",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain, path := ParseRepositoryName(tt.repo)

			if domain != tt.expected.domain {
				t.Errorf("ParseRepositoryName(%q) domain = %q, want %q", tt.repo, domain, tt.expected.domain)
			}

			if path != tt.expected.path {
				t.Errorf("ParseRepositoryName(%q) path = %q, want %q", tt.repo, path, tt.expected.path)
			}
		})
	}
}

func TestCompleteUploadReleasesLeaseOnSuccess(t *testing.T) {
	store := &ContainerdCacheStorage{
		bgCtx:        context.Background(),
		contentStore: &fakeContentStore{},
	}
	released := 0
	want := digest.FromString("success")
	store.uploads.Store("ok", &containerdUpload{
		writer:  &fakeWriter{digest: want},
		release: func(context.Context) error { released++; return nil },
	})

	got, err := store.CompleteUpload(context.Background(), "ok", want.String())
	if err != nil {
		t.Fatalf("CompleteUpload returned error: %v", err)
	}
	if got != want.String() {
		t.Fatalf("CompleteUpload digest=%q, want %q", got, want.String())
	}
	if released != 1 {
		t.Fatalf("expected release to be called once, got %d", released)
	}
}

func TestCompleteUploadReleasesLeaseOnAlreadyExists(t *testing.T) {
	store := &ContainerdCacheStorage{
		bgCtx:        context.Background(),
		contentStore: &fakeContentStore{},
	}
	released := 0
	want := digest.FromString("exists")
	store.uploads.Store("exists", &containerdUpload{
		writer:  &fakeWriter{commitErr: errdefs.ErrAlreadyExists},
		release: func(context.Context) error { released++; return nil },
	})

	got, err := store.CompleteUpload(context.Background(), "exists", want.String())
	if err != nil {
		t.Fatalf("CompleteUpload returned error: %v", err)
	}
	if got != want.String() {
		t.Fatalf("CompleteUpload digest=%q, want %q", got, want.String())
	}
	if released != 1 {
		t.Fatalf("expected release to be called once, got %d", released)
	}
}

func TestBlobExistsRequiresReadableContent(t *testing.T) {
	dg := digest.FromString("missing-reader")
	cs := &fakeContentStore{
		info:     map[digest.Digest]content.Info{dg: {Digest: dg}},
		readerAt: map[digest.Digest]content.ReaderAt{dg: nil},
		readerErr: map[digest.Digest]error{
			dg: errdefs.ErrNotFound,
		},
	}
	store := &ContainerdCacheStorage{contentStore: cs}
	if store.BlobExists(context.Background(), dg.String()) {
		t.Fatalf("expected BlobExists to be false when ReaderAt fails")
	}
}

func TestContainerdContentStoreBlobOperations(t *testing.T) {
	ctx := context.Background()
	dg := digest.FromString("blob-data")

	nilStore := &ContainerdCacheStorage{}
	if _, err := nilStore.GetBlob(ctx, dg.String()); err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("GetBlob nil store err=%v, want content store unavailable", err)
	}
	if nilStore.BlobExists(ctx, dg.String()) {
		t.Fatal("BlobExists nil store=true, want false")
	}
	if _, err := nilStore.BlobSize(ctx, dg.String()); err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("BlobSize nil store err=%v, want content store unavailable", err)
	}
	if err := nilStore.DeleteBlob(ctx, dg.String()); err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("DeleteBlob nil store err=%v, want content store unavailable", err)
	}

	cs := &fakeContentStore{
		info: map[digest.Digest]content.Info{
			dg: {Digest: dg, Size: 9},
		},
		readerAt: map[digest.Digest]content.ReaderAt{
			dg: &fakeReaderAt{data: []byte("container")},
		},
	}
	store := &ContainerdCacheStorage{contentStore: cs}

	rc, err := store.GetBlob(ctx, dg.String())
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll blob: %v", err)
	}
	if string(got) != "container" {
		t.Fatalf("blob=%q, want container", got)
	}
	if !store.BlobExists(ctx, dg.String()) {
		t.Fatal("BlobExists=false, want true")
	}
	size, err := store.BlobSize(ctx, dg.String())
	if err != nil {
		t.Fatalf("BlobSize: %v", err)
	}
	if size != 9 {
		t.Fatalf("BlobSize=%d, want 9", size)
	}
	if err := store.DeleteBlob(ctx, dg.String()); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}
	if cs.deleteDigest != dg {
		t.Fatalf("Delete digest=%q, want %q", cs.deleteDigest, dg)
	}
}

func TestNewContainerdCacheStorageReturnsClientError(t *testing.T) {
	if _, err := NewContainerdCacheStorage(""); err == nil || !strings.Contains(err.Error(), "create containerd client") {
		t.Fatalf("NewContainerdCacheStorage empty socket err=%v, want create containerd client", err)
	}
}

func TestContainerdBlobSizeAndDeleteErrors(t *testing.T) {
	ctx := context.Background()
	dg := digest.FromString("errors")

	store := &ContainerdCacheStorage{contentStore: &fakeContentStore{}}
	if _, err := store.BlobSize(ctx, dg.String()); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("BlobSize not found err=%v, want %v", err, ErrBlobNotFound)
	}

	infoErr := errors.New("info failed")
	store = &ContainerdCacheStorage{contentStore: &fakeContentStore{infoErr: map[digest.Digest]error{dg: infoErr}}}
	if _, err := store.BlobSize(ctx, dg.String()); !errors.Is(err, infoErr) {
		t.Fatalf("BlobSize info error=%v, want %v", err, infoErr)
	}

	deleteErr := errors.New("delete failed")
	store = &ContainerdCacheStorage{contentStore: &fakeContentStore{deleteErr: deleteErr}}
	if err := store.DeleteBlob(ctx, dg.String()); !errors.Is(err, deleteErr) {
		t.Fatalf("DeleteBlob error=%v, want %v", err, deleteErr)
	}
}

func TestReadAtCloserAsReaderReadsSequentially(t *testing.T) {
	readerAt := &fakeReaderAt{data: []byte("abcdef")}
	reader := &readAtCloserAsReader{ReaderAt: readerAt, Closer: readerAt}

	buf := make([]byte, 3)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("first Read err=%v, want nil", err)
	}
	if n != 3 || string(buf) != "abc" {
		t.Fatalf("first Read n=%d buf=%q, want 3 abc", n, buf)
	}
	n, err = reader.Read(buf)
	if err != nil {
		t.Fatalf("second Read err=%v, want nil", err)
	}
	if n != 3 || string(buf) != "def" {
		t.Fatalf("second Read n=%d buf=%q, want 3 def", n, buf)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if readerAt.closed != 1 {
		t.Fatalf("closed=%d, want 1", readerAt.closed)
	}
}

func TestContainerdGetManifestByDigest(t *testing.T) {
	ctx := context.Background()
	dg := digest.FromString("manifest")
	data := []byte(`{"schemaVersion":2}`)

	nilStore := &ContainerdCacheStorage{}
	if _, err := nilStore.getManifestByDigest(ctx, dg); err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("getManifestByDigest nil store err=%v, want content store unavailable", err)
	}

	notFoundStore := &ContainerdCacheStorage{contentStore: &fakeContentStore{}}
	if _, err := notFoundStore.getManifestByDigest(ctx, dg); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("getManifestByDigest not found err=%v, want %v", err, ErrManifestNotFound)
	}

	infoErr := errors.New("info failed")
	errorStore := &ContainerdCacheStorage{contentStore: &fakeContentStore{infoErr: map[digest.Digest]error{dg: infoErr}}}
	if _, err := errorStore.getManifestByDigest(ctx, dg); !errors.Is(err, infoErr) {
		t.Fatalf("getManifestByDigest info error=%v, want %v", err, infoErr)
	}

	cs := &fakeContentStore{
		info: map[digest.Digest]content.Info{
			dg: {
				Digest: dg,
				Size:   int64(len(data)),
				Labels: map[string]string{"containerd.io/content/type": ocispec.MediaTypeImageManifest},
			},
		},
		readerAt: map[digest.Digest]content.ReaderAt{
			dg: &fakeReaderAt{data: data},
		},
	}
	store := &ContainerdCacheStorage{contentStore: cs}
	meta, err := store.GetManifest(ctx, "repo", dg.String())
	if err != nil {
		t.Fatalf("GetManifest by digest: %v", err)
	}
	defer meta.Data.Close()
	got, err := io.ReadAll(meta.Data)
	if err != nil {
		t.Fatalf("ReadAll manifest: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("manifest=%q, want %q", got, data)
	}
	if meta.MediaType != ocispec.MediaTypeImageManifest {
		t.Fatalf("MediaType=%q, want %q", meta.MediaType, ocispec.MediaTypeImageManifest)
	}
	if meta.Digest != dg.String() {
		t.Fatalf("Digest=%q, want %q", meta.Digest, dg.String())
	}
	if meta.Size != int64(len(data)) {
		t.Fatalf("Size=%d, want %d", meta.Size, len(data))
	}
}

func TestReadManifestBlobErrors(t *testing.T) {
	ctx := context.Background()
	dg := digest.FromString("manifest")

	if _, err := readManifestBlob(ctx, &fakeContentStore{readerErr: map[digest.Digest]error{dg: errdefs.ErrNotFound}}, ocispec.Descriptor{Digest: dg}); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("readManifestBlob not found err=%v, want %v", err, ErrManifestNotFound)
	}
	readErr := errors.New("read failed")
	if _, err := readManifestBlob(ctx, &fakeContentStore{readerErr: map[digest.Digest]error{dg: readErr}}, ocispec.Descriptor{Digest: dg}); !errors.Is(err, readErr) {
		t.Fatalf("readManifestBlob error=%v, want %v", err, readErr)
	}
}

func TestCompleteUploadMarksContentRoot(t *testing.T) {
	dg := digest.FromString("root-me")
	cs := &fakeContentStore{
		info:      map[digest.Digest]content.Info{},
		readerAt:  map[digest.Digest]content.ReaderAt{},
		readerErr: map[digest.Digest]error{},
	}
	store := &ContainerdCacheStorage{
		bgCtx:        context.Background(),
		contentStore: cs,
	}
	store.uploads.Store("root", &containerdUpload{
		writer:  &fakeWriter{digest: dg},
		release: func(context.Context) error { return nil },
	})

	got, err := store.CompleteUpload(context.Background(), "root", dg.String())
	if err != nil {
		t.Fatalf("CompleteUpload returned error: %v", err)
	}
	if got != dg.String() {
		t.Fatalf("CompleteUpload digest=%q, want %q", got, dg.String())
	}
	if cs.updateDigest != dg {
		t.Fatalf("markContentRoot digest=%q, want %q", cs.updateDigest, dg)
	}
	if cs.updateLabel != "containerd.io/gc.root" {
		t.Fatalf("markContentRoot label=%q, want containerd.io/gc.root", cs.updateLabel)
	}
}

func TestContainerdUploadSessionBranches(t *testing.T) {
	ctx := context.Background()
	store := &ContainerdCacheStorage{contentStore: &fakeContentStore{}}

	if _, err := store.GetUpload(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "upload not found") {
		t.Fatalf("GetUpload missing err=%v, want upload not found", err)
	}
	if _, err := store.CopyChunk(ctx, "missing", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "upload not found") {
		t.Fatalf("CopyChunk missing err=%v, want upload not found", err)
	}
	if _, err := store.CompleteUpload(ctx, "missing", digest.FromString("missing").String()); err == nil || !strings.Contains(err.Error(), "upload not found") {
		t.Fatalf("CompleteUpload missing err=%v, want upload not found", err)
	}
	if err := store.AbortUpload(ctx, "missing"); err != nil {
		t.Fatalf("AbortUpload missing err=%v, want nil", err)
	}

	writer := &fakeWriter{}
	store.uploads.Store("copy", &containerdUpload{writer: writer})
	session, err := store.CopyChunk(ctx, "copy", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("CopyChunk: %v", err)
	}
	if session.Written != 5 {
		t.Fatalf("CopyChunk Written=%d, want 5", session.Written)
	}
	session, err = store.GetUpload(ctx, "copy")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if session.Written != 5 {
		t.Fatalf("GetUpload Written=%d, want 5", session.Written)
	}

	writeErr := errors.New("write failed")
	store.uploads.Store("write-fail", &containerdUpload{writer: &fakeWriter{writeErr: writeErr}})
	if _, err := store.CopyChunk(ctx, "write-fail", strings.NewReader("x")); !errors.Is(err, writeErr) {
		t.Fatalf("CopyChunk write err=%v, want %v", err, writeErr)
	}

	statusErr := errors.New("status failed")
	store.uploads.Store("status-fail", &containerdUpload{writer: &fakeWriter{statusErr: statusErr}})
	if _, err := store.CopyChunk(ctx, "status-fail", strings.NewReader("x")); !errors.Is(err, statusErr) {
		t.Fatalf("CopyChunk status err=%v, want %v", err, statusErr)
	}
	store.uploads.Store("get-status-fail", &containerdUpload{writer: &fakeWriter{statusErr: statusErr}})
	if _, err := store.GetUpload(ctx, "get-status-fail"); !errors.Is(err, statusErr) {
		t.Fatalf("GetUpload status err=%v, want %v", err, statusErr)
	}
}

func TestCompleteUploadCommitAndReleaseErrors(t *testing.T) {
	ctx := context.Background()
	dg := digest.FromString("commit")

	commitErr := errors.New("commit failed")
	store := &ContainerdCacheStorage{bgCtx: ctx, contentStore: &fakeContentStore{}}
	store.uploads.Store("commit-fail", &containerdUpload{
		writer:  &fakeWriter{commitErr: commitErr},
		release: func(context.Context) error { return nil },
	})
	if _, err := store.CompleteUpload(ctx, "commit-fail", dg.String()); !errors.Is(err, commitErr) {
		t.Fatalf("CompleteUpload commit err=%v, want %v", err, commitErr)
	}

	releaseErr := errors.New("release failed")
	store.uploads.Store("release-fail", &containerdUpload{
		writer:  &fakeWriter{digest: dg},
		release: func(context.Context) error { return releaseErr },
	})
	if _, err := store.CompleteUpload(ctx, "release-fail", dg.String()); !errors.Is(err, releaseErr) {
		t.Fatalf("CompleteUpload release err=%v, want %v", err, releaseErr)
	}
}

func TestContainerdLabelUpdates(t *testing.T) {
	ctx := context.Background()
	dg := digest.FromString("labels")

	nilStore := &ContainerdCacheStorage{}
	if err := nilStore.markContentRoot(dg); err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("markContentRoot nil store err=%v, want content store unavailable", err)
	}
	if err := nilStore.updateManifestLabels(ctx, dg, map[string]string{}); err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("updateManifestLabels nil store err=%v, want content store unavailable", err)
	}

	updateErr := errors.New("update failed")
	store := &ContainerdCacheStorage{contentStore: &fakeContentStore{updateErr: updateErr}}
	if err := store.markContentRoot(dg); !errors.Is(err, updateErr) {
		t.Fatalf("markContentRoot update err=%v, want %v", err, updateErr)
	}
	if err := store.updateManifestLabels(ctx, dg, map[string]string{"k": "v"}); !errors.Is(err, updateErr) {
		t.Fatalf("updateManifestLabels update err=%v, want %v", err, updateErr)
	}

	cs := &fakeContentStore{}
	store = &ContainerdCacheStorage{contentStore: cs}
	if err := store.updateManifestLabels(ctx, dg, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("updateManifestLabels: %v", err)
	}
	if cs.updateDigest != dg {
		t.Fatalf("update digest=%q, want %q", cs.updateDigest, dg)
	}
	if got := cs.updateInfo.Labels["k"]; got != "v" {
		t.Fatalf("updated label k=%q, want v", got)
	}
}

func TestBuildContainerdManifestInfoForImageManifest(t *testing.T) {
	configDigest := digest.FromString("config")
	layerDigest := digest.FromString("layer")
	data := mustManifestJSON(t, ocispec.Manifest{
		Config: ocispec.Descriptor{Digest: configDigest},
		Layers: []ocispec.Descriptor{
			{Digest: layerDigest},
		},
	})

	got, err := buildContainerdManifestInfo("registry.example.com/team/app", data, ocispec.MediaTypeImageManifest)
	if err != nil {
		t.Fatalf("buildContainerdManifestInfo returned error: %v", err)
	}
	if got.repo != "registry.example.com/team/app" {
		t.Fatalf("repo=%q, want registry.example.com/team/app", got.repo)
	}
	wantLabels := map[string]string{
		"containerd.io/distribution.source.registry.example.com": "team/app",
		"containerd.io/content/type":                             ocispec.MediaTypeImageManifest,
		"containerd.io/gc.ref.content.config":                    configDigest.String(),
		"containerd.io/gc.ref.content.l.0":                       layerDigest.String(),
	}
	if !reflect.DeepEqual(got.labels, wantLabels) {
		t.Fatalf("labels=%v, want %v", got.labels, wantLabels)
	}
}

func TestBuildContainerdManifestInfoForImageIndex(t *testing.T) {
	manifestDigest := digest.FromString("manifest")
	data := mustManifestJSON(t, ocispec.Index{
		Manifests: []ocispec.Descriptor{
			{Digest: manifestDigest},
		},
	})

	got, err := buildContainerdManifestInfo("alpine", data, ocispec.MediaTypeImageIndex)
	if err != nil {
		t.Fatalf("buildContainerdManifestInfo returned error: %v", err)
	}
	if got.repo != "docker.io/library/alpine" {
		t.Fatalf("repo=%q, want docker.io/library/alpine", got.repo)
	}
	wantLabels := map[string]string{
		"containerd.io/distribution.source.docker.io": "library/alpine",
		"containerd.io/content/type":                  ocispec.MediaTypeImageIndex,
		"containerd.io/gc.ref.content.m.0":            manifestDigest.String(),
	}
	if !reflect.DeepEqual(got.labels, wantLabels) {
		t.Fatalf("labels=%v, want %v", got.labels, wantLabels)
	}
}

func TestBuildContainerdManifestInfoRejectsUnsupportedMediaType(t *testing.T) {
	_, err := buildContainerdManifestInfo("alpine", []byte("{}"), "application/example")
	if err == nil {
		t.Fatal("expected error for unsupported media type")
	}
}

func TestContainerdManifestInfoAliasesAndDigestHelpers(t *testing.T) {
	configDigest := digest.FromString("docker-config")
	layerDigest := digest.FromString("docker-layer")
	data := mustManifestJSON(t, ocispec.Manifest{
		Config: ocispec.Descriptor{Digest: configDigest},
		Layers: []ocispec.Descriptor{{Digest: layerDigest}},
	})

	info, err := buildContainerdManifestInfo("docker.io/user/app", data, "application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		t.Fatalf("buildContainerdManifestInfo docker manifest: %v", err)
	}
	dg := digestFromBytes(data)
	img := info.image("latest", dg, int64(len(data)))
	if img.Name != "docker.io/user/app:latest" {
		t.Fatalf("image name=%q, want docker.io/user/app:latest", img.Name)
	}
	if img.Target.Digest != dg || img.Target.Size != int64(len(data)) {
		t.Fatalf("image target=%+v, want digest %s size %d", img.Target, dg, len(data))
	}
	if got := info.snapshotLabels()["containerd.io/distribution.source.docker.io"]; got != "user/app" {
		t.Fatalf("snapshot source label=%q, want user/app", got)
	}
	if got := info.labels["containerd.io/gc.ref.content.l.0"]; got != layerDigest.String() {
		t.Fatalf("layer label=%q, want %s", got, layerDigest)
	}

	indexDigest := digest.FromString("docker-index")
	indexData := mustManifestJSON(t, ocispec.Index{
		Manifests: []ocispec.Descriptor{{Digest: indexDigest}},
	})
	info, err = buildContainerdManifestInfo("registry.example.com/app", indexData, "application/vnd.docker.distribution.manifest.list.v2+json")
	if err != nil {
		t.Fatalf("buildContainerdManifestInfo docker index: %v", err)
	}
	if got := info.labels["containerd.io/gc.ref.content.m.0"]; got != indexDigest.String() {
		t.Fatalf("index label=%q, want %s", got, indexDigest)
	}

	if _, err := buildContainerdManifestInfo("app", []byte("{"), ocispec.MediaTypeImageManifest); err == nil || !strings.Contains(err.Error(), "unmarshal manifest") {
		t.Fatalf("invalid manifest err=%v, want unmarshal manifest", err)
	}
	if _, err := buildContainerdManifestInfo("app", []byte("{"), ocispec.MediaTypeImageIndex); err == nil || !strings.Contains(err.Error(), "unmarshal index") {
		t.Fatalf("invalid index err=%v, want unmarshal index", err)
	}
}

func TestContainerdCloseCancelsBackgroundContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &ContainerdCacheStorage{bgCtx: ctx, cancelBg: cancel}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("background context was not canceled")
	}
}

func TestContainerdDigestManifestExistsAndDeleteUseContentStore(t *testing.T) {
	ctx := context.Background()
	dg := digest.FromString("digest-only")
	cs := &fakeContentStore{
		info: map[digest.Digest]content.Info{dg: {Digest: dg}},
	}
	store := &ContainerdCacheStorage{contentStore: cs}

	if !store.ManifestExists(ctx, "repo", dg.String()) {
		t.Fatal("ManifestExists digest=false, want true")
	}
	if err := store.DeleteManifest(ctx, "repo", dg.String()); err != nil {
		t.Fatalf("DeleteManifest digest: %v", err)
	}
	if cs.deleteDigest != dg {
		t.Fatalf("delete digest=%q, want %q", cs.deleteDigest, dg)
	}

	missingStore := &ContainerdCacheStorage{contentStore: &fakeContentStore{deleteErr: errdefs.ErrNotFound}}
	if missingStore.ManifestExists(ctx, "repo", dg.String()) {
		t.Fatal("ManifestExists missing digest=true, want false")
	}
	if err := missingStore.DeleteManifest(ctx, "repo", dg.String()); err != nil {
		t.Fatalf("DeleteManifest missing digest: %v", err)
	}

	nilStore := &ContainerdCacheStorage{}
	if err := nilStore.DeleteManifest(ctx, "repo", dg.String()); err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("DeleteManifest nil store err=%v, want content store unavailable", err)
	}

	deleteErr := errors.New("delete failed")
	errorStore := &ContainerdCacheStorage{contentStore: &fakeContentStore{deleteErr: deleteErr}}
	if err := errorStore.DeleteManifest(ctx, "repo", dg.String()); !errors.Is(err, deleteErr) {
		t.Fatalf("DeleteManifest delete err=%v, want %v", err, deleteErr)
	}
}

func TestAbortUploadRemovesSessionAndAbortsContainerdRef(t *testing.T) {
	cs := &fakeContentStore{}
	store := &ContainerdCacheStorage{contentStore: cs}
	writer := &fakeWriter{}
	released := 0
	store.uploads.Store("upload-id", &containerdUpload{
		writer:  writer,
		release: func(context.Context) error { released++; return nil },
	})

	if err := store.AbortUpload(context.Background(), "upload-id"); err != nil {
		t.Fatalf("AbortUpload returned error: %v", err)
	}
	if writer.closed != 1 {
		t.Fatalf("writer closed %d times, want 1", writer.closed)
	}
	if released != 1 {
		t.Fatalf("release called %d times, want 1", released)
	}
	if cs.abortRef != "upload-upload-id" {
		t.Fatalf("abort ref=%q, want upload-upload-id", cs.abortRef)
	}
	if _, ok := store.uploads.Load("upload-id"); ok {
		t.Fatal("upload still stored after abort")
	}
}

func TestAbortUploadReturnsReleaseError(t *testing.T) {
	wantErr := errors.New("release failed")
	store := &ContainerdCacheStorage{contentStore: &fakeContentStore{}}
	store.uploads.Store("upload-id", &containerdUpload{
		writer:  &fakeWriter{},
		release: func(context.Context) error { return wantErr },
	})

	err := store.AbortUpload(context.Background(), "upload-id")
	if !errors.Is(err, wantErr) {
		t.Fatalf("AbortUpload error=%v, want %v", err, wantErr)
	}
}

func TestAbortUploadReturnsAbortError(t *testing.T) {
	wantErr := errors.New("abort failed")
	store := &ContainerdCacheStorage{contentStore: &fakeContentStore{abortErr: wantErr}}
	store.uploads.Store("upload-id", &containerdUpload{
		writer:  &fakeWriter{},
		release: func(context.Context) error { return nil },
	})

	err := store.AbortUpload(context.Background(), "upload-id")
	if !errors.Is(err, wantErr) {
		t.Fatalf("AbortUpload error=%v, want %v", err, wantErr)
	}
}

func TestAbortUploadReturnsContentStoreUnavailableAfterRelease(t *testing.T) {
	store := &ContainerdCacheStorage{}
	store.uploads.Store("upload-id", &containerdUpload{
		writer:  &fakeWriter{},
		release: func(context.Context) error { return nil },
	})

	err := store.AbortUpload(context.Background(), "upload-id")
	if err == nil || !strings.Contains(err.Error(), "content store unavailable") {
		t.Fatalf("AbortUpload err=%v, want content store unavailable", err)
	}
}

func mustManifestJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

type fakeWriter struct {
	digest    digest.Digest
	commitErr error
	status    content.Status
	statusErr error
	writeErr  error
	closeErr  error
	closed    int
}

func (f *fakeWriter) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.status.Offset += int64(len(p))
	return len(p), nil
}

func (f *fakeWriter) Close() error {
	f.closed++
	return f.closeErr
}

func (f *fakeWriter) Digest() digest.Digest { return f.digest }

func (f *fakeWriter) Commit(_ context.Context, _ int64, expected digest.Digest, _ ...content.Opt) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	if f.digest == "" {
		f.digest = expected
	}
	return nil
}

func (f *fakeWriter) Status() (content.Status, error) {
	if f.statusErr != nil {
		return content.Status{}, f.statusErr
	}
	return f.status, nil
}

func (f *fakeWriter) Truncate(size int64) error {
	f.status.Offset = size
	return nil
}

type fakeContentStore struct {
	info      map[digest.Digest]content.Info
	infoErr   map[digest.Digest]error
	readerAt  map[digest.Digest]content.ReaderAt
	readerErr map[digest.Digest]error

	updateDigest digest.Digest
	updateLabel  string
	updateValue  string
	updateInfo   content.Info
	updateErr    error

	deleteDigest digest.Digest
	deleteErr    error

	abortRef string
	abortErr error
}

func (f *fakeContentStore) Info(_ context.Context, dg digest.Digest) (content.Info, error) {
	if f.infoErr != nil {
		if err, ok := f.infoErr[dg]; ok && err != nil {
			return content.Info{}, err
		}
	}
	if info, ok := f.info[dg]; ok {
		return info, nil
	}
	return content.Info{}, errdefs.ErrNotFound
}

func (f *fakeContentStore) ReaderAt(_ context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	if f.readerErr != nil {
		if err, ok := f.readerErr[desc.Digest]; ok && err != nil {
			return nil, err
		}
	}
	if f.readerAt != nil {
		if ra, ok := f.readerAt[desc.Digest]; ok && ra != nil {
			return ra, nil
		}
	}
	return nil, errdefs.ErrNotFound
}

func (f *fakeContentStore) Update(_ context.Context, info content.Info, _ ...string) (content.Info, error) {
	if f.updateErr != nil {
		return content.Info{}, f.updateErr
	}
	f.updateDigest = info.Digest
	f.updateInfo = info
	if info.Labels != nil {
		if v, ok := info.Labels["containerd.io/gc.root"]; ok {
			f.updateLabel = "containerd.io/gc.root"
			f.updateValue = v
		}
	}
	return info, nil
}

func (f *fakeContentStore) Delete(_ context.Context, dg digest.Digest) error {
	f.deleteDigest = dg
	return f.deleteErr
}

func (f *fakeContentStore) Abort(_ context.Context, ref string) error {
	f.abortRef = ref
	return f.abortErr
}

type fakeReaderAt struct {
	data    []byte
	closed  int
	readErr error
}

func (f *fakeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeReaderAt) Close() error {
	f.closed++
	return nil
}

func (f *fakeReaderAt) Size() int64 {
	return int64(len(f.data))
}

// TestParseRepositoryNameConsistency tests that the function is consistent
// with Docker's repository naming conventions.
func TestParseRepositoryNameConsistency(t *testing.T) {
	// Test that official Docker Hub images get the library prefix.
	officialImages := []string{"nginx", "alpine", "ubuntu", "redis", "postgres"}

	for _, img := range officialImages {
		domain, path := ParseRepositoryName(img)
		if domain != "docker.io" {
			t.Errorf("ParseRepositoryName(%q) domain = %q, want docker.io", img, domain)
		}
		if path != "library/"+img {
			t.Errorf("ParseRepositoryName(%q) path = %q, want library/%s", img, path, img)
		}
	}

	// Test that user images on Docker Hub don't get the library prefix.
	userImages := []string{"user/app", "company/service", "org/project"}

	for _, img := range userImages {
		domain, path := ParseRepositoryName(img)
		if domain != "docker.io" {
			t.Errorf("ParseRepositoryName(%q) domain = %q, want docker.io", img, domain)
		}
		if path != img {
			t.Errorf("ParseRepositoryName(%q) path = %q, want %s", img, path, img)
		}
	}
}
