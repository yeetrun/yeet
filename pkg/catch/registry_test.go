// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/registry"
	"github.com/shayne/yeet/pkg/svc"
)

type stubInstaller struct {
	buf    bytes.Buffer
	closed bool
	failed bool
}

func (s *stubInstaller) Write(p []byte) (int, error) {
	return s.buf.Write(p)
}

func (s *stubInstaller) Close() error {
	s.closed = true
	return nil
}

func (s *stubInstaller) Fail() {
	s.failed = true
}

func TestRegistryPutManifestStagesLatest(t *testing.T) {
	server := newTestServer(t)
	storage := server.registry.storage
	var gotCfg FileInstallerCfg
	inst := &stubInstaller{}
	storage.newInstaller = func(_ *Server, cfg FileInstallerCfg) (registryInstaller, error) {
		gotCfg = cfg
		return inst, nil
	}

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	digest, err := storage.PutManifest(context.Background(), "svc/app", "latest", manifest, "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest missing sha256 prefix: %q", digest)
	}
	if !gotCfg.StageOnly {
		t.Fatalf("StageOnly = false, want true")
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	d := dv.AsStruct()
	ir, ok := d.Images[db.ImageRepoName("svc/app")]
	if !ok {
		t.Fatalf("missing repo entry")
	}
	if _, ok := ir.Refs[db.ImageRef("staged")]; !ok {
		t.Fatalf("missing staged ref")
	}
	if _, ok := ir.Refs[db.ImageRef("latest")]; ok {
		t.Fatalf("unexpected latest ref")
	}
	if !strings.Contains(inst.buf.String(), "image: "+svc.InternalRegistryHost+"/svc/app") {
		t.Fatalf("compose missing image reference: %q", inst.buf.String())
	}
}

func TestRegistryPutManifestRunInstalls(t *testing.T) {
	server := newTestServer(t)
	storage := server.registry.storage
	var gotCfg FileInstallerCfg
	storage.newInstaller = func(_ *Server, cfg FileInstallerCfg) (registryInstaller, error) {
		gotCfg = cfg
		return &stubInstaller{}, nil
	}

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	_, err := storage.PutManifest(context.Background(), "svc/app", "run", manifest, "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if gotCfg.StageOnly {
		t.Fatalf("StageOnly = true, want false")
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	d := dv.AsStruct()
	ir, ok := d.Images[db.ImageRepoName("svc/app")]
	if !ok {
		t.Fatalf("missing repo entry")
	}
	if _, ok := ir.Refs[db.ImageRef("staged")]; !ok {
		t.Fatalf("missing staged ref")
	}
	if _, ok := ir.Refs[db.ImageRef("run")]; !ok {
		t.Fatalf("missing run ref")
	}
}

func TestRegistryPutManifestRejectsInvalid(t *testing.T) {
	server := newTestServer(t)
	storage := server.registry.storage

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	if _, err := storage.PutManifest(context.Background(), "svc/app/extra", "latest", manifest, "application/vnd.oci.image.manifest.v1+json"); err == nil {
		t.Fatalf("expected invalid repo error")
	}
	if _, err := storage.PutManifest(context.Background(), "svc/app", "nope", manifest, "application/vnd.oci.image.manifest.v1+json"); err == nil {
		t.Fatalf("expected invalid tag error")
	}
}

func TestRegistryLoopbackWriteRejected(t *testing.T) {
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "http://example/v2/svc/app/manifests/latest", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	server.registry.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

type recordingStorage struct {
	repo      string
	reference string
	mediaType string
}

func (r *recordingStorage) GetBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	return nil, registry.ErrBlobNotFound
}

func (r *recordingStorage) BlobSize(ctx context.Context, digest string) (int64, error) {
	return 0, registry.ErrBlobNotFound
}

func (r *recordingStorage) BlobExists(ctx context.Context, digest string) bool {
	return false
}

func (r *recordingStorage) DeleteBlob(ctx context.Context, digest string) error {
	return nil
}

func (r *recordingStorage) GetManifest(ctx context.Context, repo, reference string) (*registry.ManifestMetadata, error) {
	return nil, registry.ErrManifestNotFound
}

func (r *recordingStorage) PutManifest(ctx context.Context, repo, reference string, data []byte, mediaType string) (string, error) {
	r.repo = repo
	r.reference = reference
	r.mediaType = mediaType
	return "sha256:deadbeef", nil
}

func (r *recordingStorage) ManifestExists(ctx context.Context, repo, reference string) bool {
	return false
}

func (r *recordingStorage) DeleteManifest(ctx context.Context, repo, reference string) error {
	return nil
}

func (r *recordingStorage) NewUpload(ctx context.Context) (*registry.UploadSession, error) {
	return &registry.UploadSession{UUID: "u"}, nil
}

func (r *recordingStorage) GetUpload(ctx context.Context, uuid string) (*registry.UploadSession, error) {
	return &registry.UploadSession{UUID: uuid}, nil
}

func (r *recordingStorage) CopyChunk(ctx context.Context, uuid string, rd io.Reader) (*registry.UploadSession, error) {
	return &registry.UploadSession{UUID: uuid}, nil
}

func (r *recordingStorage) CompleteUpload(ctx context.Context, uuid, expectedDigest string) (string, error) {
	return expectedDigest, nil
}

func (r *recordingStorage) AbortUpload(ctx context.Context, uuid string) error {
	return nil
}

func TestInternalRegistryStoragePrefixesRepo(t *testing.T) {
	server := newTestServer(t)
	rec := &recordingStorage{}
	inst := &stubInstaller{}
	storage := &internalRegistryStorage{
		s:          server,
		base:       rec,
		repoPrefix: svc.InternalRegistryHost,
		newInstaller: func(_ *Server, cfg FileInstallerCfg) (registryInstaller, error) {
			return inst, nil
		},
	}
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	if _, err := storage.PutManifest(context.Background(), "svc/app", "run", manifest, "application/vnd.oci.image.manifest.v1+json"); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	wantRepo := svc.InternalRegistryHost + "/svc/app"
	if rec.repo != wantRepo {
		t.Fatalf("repo = %q, want %q", rec.repo, wantRepo)
	}
}
