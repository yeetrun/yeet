// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/registry"
	"github.com/yeetrun/yeet/pkg/svc"
)

type stubInstaller struct {
	buf      bytes.Buffer
	writeErr error
	closeErr error
	closed   bool
	failed   bool
}

func (s *stubInstaller) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.buf.Write(p)
}

func (s *stubInstaller) Close() error {
	s.closed = true
	if s.closeErr != nil {
		return s.closeErr
	}
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
	repo               string
	reference          string
	mediaType          string
	blobDigest         string
	blobSizeDigest     string
	blobExistsDigest   string
	deleteBlobDigest   string
	blob               []byte
	blobSize           int64
	blobExists         bool
	manifestRepo       string
	manifestRef        string
	manifest           *registry.ManifestMetadata
	manifestExistsRepo string
	manifestExistsRef  string
	manifestExists     bool
	deletedRepo        string
	deletedRef         string
	deleteManifest     error
	deleteBlobError    error
	newUploadCalled    bool
	getUploadUUID      string
	copyChunkUUID      string
	copyChunkData      string
	completeUUID       string
	completeDigest     string
	abortUUID          string
}

func (r *recordingStorage) GetBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	r.blobDigest = digest
	if r.blob == nil {
		return nil, registry.ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(r.blob)), nil
}

func (r *recordingStorage) BlobSize(ctx context.Context, digest string) (int64, error) {
	r.blobSizeDigest = digest
	if r.blobSize == 0 {
		return 0, registry.ErrBlobNotFound
	}
	return r.blobSize, nil
}

func (r *recordingStorage) BlobExists(ctx context.Context, digest string) bool {
	r.blobExistsDigest = digest
	return r.blobExists
}

func (r *recordingStorage) DeleteBlob(ctx context.Context, digest string) error {
	r.deleteBlobDigest = digest
	return r.deleteBlobError
}

func (r *recordingStorage) GetManifest(ctx context.Context, repo, reference string) (*registry.ManifestMetadata, error) {
	r.manifestRepo = repo
	r.manifestRef = reference
	if r.manifest == nil {
		return nil, registry.ErrManifestNotFound
	}
	return r.manifest, nil
}

func (r *recordingStorage) PutManifest(ctx context.Context, repo, reference string, data []byte, mediaType string) (string, error) {
	r.repo = repo
	r.reference = reference
	r.mediaType = mediaType
	return "sha256:deadbeef", nil
}

func (r *recordingStorage) ManifestExists(ctx context.Context, repo, reference string) bool {
	r.manifestExistsRepo = repo
	r.manifestExistsRef = reference
	return r.manifestExists
}

func (r *recordingStorage) DeleteManifest(ctx context.Context, repo, reference string) error {
	r.deletedRepo = repo
	r.deletedRef = reference
	return r.deleteManifest
}

func (r *recordingStorage) NewUpload(ctx context.Context) (*registry.UploadSession, error) {
	r.newUploadCalled = true
	return &registry.UploadSession{UUID: "u"}, nil
}

func (r *recordingStorage) GetUpload(ctx context.Context, uuid string) (*registry.UploadSession, error) {
	r.getUploadUUID = uuid
	return &registry.UploadSession{UUID: uuid}, nil
}

func (r *recordingStorage) CopyChunk(ctx context.Context, uuid string, rd io.Reader) (*registry.UploadSession, error) {
	r.copyChunkUUID = uuid
	b, _ := io.ReadAll(rd)
	r.copyChunkData = string(b)
	return &registry.UploadSession{UUID: uuid}, nil
}

func (r *recordingStorage) CompleteUpload(ctx context.Context, uuid, expectedDigest string) (string, error) {
	r.completeUUID = uuid
	r.completeDigest = expectedDigest
	return expectedDigest, nil
}

func (r *recordingStorage) AbortUpload(ctx context.Context, uuid string) error {
	r.abortUUID = uuid
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

func TestInternalRegistryDeleteManifestDigestRemovesMatchingRefs(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Images: map[db.ImageRepoName]*db.ImageRepo{
			"svc/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run":    {BlobHash: "sha256:deadbeef"},
					"staged": {BlobHash: "sha256:deadbeef"},
					"old":    {BlobHash: "sha256:other"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	rec := &recordingStorage{}
	storage := &internalRegistryStorage{s: server, base: rec, repoPrefix: svc.InternalRegistryHost}

	if err := storage.DeleteManifest(context.Background(), "svc/app", "sha256:deadbeef"); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if rec.deletedRepo != svc.InternalRegistryHost+"/svc/app" || rec.deletedRef != "sha256:deadbeef" {
		t.Fatalf("base delete = (%q, %q), want prefixed digest delete", rec.deletedRepo, rec.deletedRef)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	refs := dv.AsStruct().Images["svc/app"].Refs
	if _, ok := refs["run"]; ok {
		t.Fatalf("run ref should be removed")
	}
	if _, ok := refs["staged"]; ok {
		t.Fatalf("staged ref should be removed")
	}
	if _, ok := refs["old"]; !ok {
		t.Fatalf("unmatched ref should remain")
	}
}

func TestInternalRegistryDeleteManifestTagRemovesRef(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Images: map[db.ImageRepoName]*db.ImageRepo{
			"svc/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run": {BlobHash: "sha256:run"},
					"old": {BlobHash: "sha256:old"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	rec := &recordingStorage{}
	storage := &internalRegistryStorage{s: server, base: rec, repoPrefix: svc.InternalRegistryHost}

	if err := storage.DeleteManifest(context.Background(), "svc/app", "run"); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if rec.deletedRepo != svc.InternalRegistryHost+"/svc/app" || rec.deletedRef != "run" {
		t.Fatalf("base delete = (%q, %q), want prefixed tag delete", rec.deletedRepo, rec.deletedRef)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	refs := dv.AsStruct().Images["svc/app"].Refs
	if _, ok := refs["run"]; ok {
		t.Fatalf("run ref should be removed")
	}
	if _, ok := refs["old"]; !ok {
		t.Fatalf("old ref should remain")
	}
}

func TestInternalRegistryStorageDelegatesBlobAndUploadMethods(t *testing.T) {
	server := newTestServer(t)
	rec := &recordingStorage{blob: []byte("blob"), blobSize: 4, blobExists: true}
	storage := &internalRegistryStorage{s: server, base: rec}
	ctx := context.Background()

	blob, err := storage.GetBlob(ctx, "sha256:blob")
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	raw, err := io.ReadAll(blob)
	if err != nil {
		t.Fatalf("ReadAll blob: %v", err)
	}
	if string(raw) != "blob" || rec.blobDigest != "sha256:blob" {
		t.Fatalf("blob delegation = %q digest %q", raw, rec.blobDigest)
	}
	size, err := storage.BlobSize(ctx, "sha256:blob")
	if err != nil {
		t.Fatalf("BlobSize: %v", err)
	}
	if size != 4 || rec.blobSizeDigest != "sha256:blob" {
		t.Fatalf("blob size delegation = %d digest %q", size, rec.blobSizeDigest)
	}
	if !storage.BlobExists(ctx, "sha256:blob") || rec.blobExistsDigest != "sha256:blob" {
		t.Fatalf("BlobExists delegation failed")
	}
	if err := storage.DeleteBlob(ctx, "sha256:blob"); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}
	if rec.deleteBlobDigest != "sha256:blob" {
		t.Fatalf("delete blob digest = %q", rec.deleteBlobDigest)
	}
	if upload, err := storage.NewUpload(ctx); err != nil || upload.UUID != "u" || !rec.newUploadCalled {
		t.Fatalf("NewUpload = %#v, %v called=%v", upload, err, rec.newUploadCalled)
	}
	if upload, err := storage.GetUpload(ctx, "u1"); err != nil || upload.UUID != "u1" || rec.getUploadUUID != "u1" {
		t.Fatalf("GetUpload = %#v, %v uuid=%q", upload, err, rec.getUploadUUID)
	}
	if _, err := storage.CopyChunk(ctx, "u2", strings.NewReader("chunk")); err != nil {
		t.Fatalf("CopyChunk: %v", err)
	}
	if rec.copyChunkUUID != "u2" || rec.copyChunkData != "chunk" {
		t.Fatalf("copy chunk = uuid %q data %q", rec.copyChunkUUID, rec.copyChunkData)
	}
	if got, err := storage.CompleteUpload(ctx, "u3", "sha256:done"); err != nil || got != "sha256:done" {
		t.Fatalf("CompleteUpload = %q, %v", got, err)
	}
	if rec.completeUUID != "u3" || rec.completeDigest != "sha256:done" {
		t.Fatalf("complete upload = uuid %q digest %q", rec.completeUUID, rec.completeDigest)
	}
	if err := storage.AbortUpload(ctx, "u4"); err != nil {
		t.Fatalf("AbortUpload: %v", err)
	}
	if rec.abortUUID != "u4" {
		t.Fatalf("abort uuid = %q", rec.abortUUID)
	}
}

func TestInternalRegistryStorageGetManifestByTagUsesStoredDigest(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Images: map[db.ImageRepoName]*db.ImageRepo{
			"svc/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run": {BlobHash: "sha256:run", ContentType: "application/vnd.oci.image.manifest.v1+json"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	rec := &recordingStorage{
		manifest: &registry.ManifestMetadata{Digest: "sha256:run"},
	}
	storage := &internalRegistryStorage{s: server, base: rec, repoPrefix: svc.InternalRegistryHost}

	got, err := storage.GetManifest(context.Background(), "svc/app", "run")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if got.Digest != "sha256:run" {
		t.Fatalf("digest = %q, want sha256:run", got.Digest)
	}
	if rec.manifestRepo != svc.InternalRegistryHost+"/svc/app" || rec.manifestRef != "sha256:run" {
		t.Fatalf("base manifest lookup = %q %q", rec.manifestRepo, rec.manifestRef)
	}
}

func TestInternalRegistryStorageManifestExistsHandlesTagsAndDigests(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Images: map[db.ImageRepoName]*db.ImageRepo{
			"svc/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run": {BlobHash: "sha256:run"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	rec := &recordingStorage{manifestExists: true}
	storage := &internalRegistryStorage{s: server, base: rec, repoPrefix: svc.InternalRegistryHost}

	if !storage.ManifestExists(context.Background(), "svc/app", "run") {
		t.Fatal("tag manifest should exist from db ref")
	}
	if !storage.ManifestExists(context.Background(), "svc/app", "sha256:run") {
		t.Fatal("digest manifest should delegate to base")
	}
	if rec.manifestExistsRepo != svc.InternalRegistryHost+"/svc/app" || rec.manifestExistsRef != "sha256:run" {
		t.Fatalf("base exists lookup = %q %q", rec.manifestExistsRepo, rec.manifestExistsRef)
	}
}

func TestInternalRegistryStageComposeUsesPreviousGenerationFile(t *testing.T) {
	server := newTestServer(t)
	prev := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(prev, []byte("services:\n  old:\n    image: old\n"), 0o644); err != nil {
		t.Fatalf("write previous compose: %v", err)
	}
	data := &db.Data{
		Services: map[string]*db.Service{
			"svc": {
				Name:       "svc",
				Generation: 2,
				Artifacts: db.ArtifactStore{
					db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): prev}},
				},
			},
		},
	}
	inst := &stubInstaller{}
	storage := &internalRegistryStorage{
		s: server,
		newInstaller: func(_ *Server, cfg FileInstallerCfg) (registryInstaller, error) {
			return inst, nil
		},
	}

	if err := storage.stageCompose(data, "svc", "svc/app", true); err != nil {
		t.Fatalf("stageCompose: %v", err)
	}
	if got := inst.buf.String(); got != "services:\n  old:\n    image: old\n" {
		t.Fatalf("compose content = %q", got)
	}
	if !inst.closed || inst.failed {
		t.Fatalf("installer closed=%v failed=%v", inst.closed, inst.failed)
	}
}

func TestInternalRegistryStageComposeFailsAndClosesInstallerOnReadError(t *testing.T) {
	server := newTestServer(t)
	data := &db.Data{
		Services: map[string]*db.Service{
			"svc": {
				Name:       "svc",
				Generation: 2,
				Artifacts: db.ArtifactStore{
					db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): filepath.Join(t.TempDir(), "missing.yml")}},
				},
			},
		},
	}
	inst := &stubInstaller{}
	storage := &internalRegistryStorage{
		s: server,
		newInstaller: func(_ *Server, cfg FileInstallerCfg) (registryInstaller, error) {
			return inst, nil
		},
	}

	err := storage.stageCompose(data, "svc", "svc/app", true)
	if err == nil || !strings.Contains(err.Error(), "failed to read previous generation compose file") {
		t.Fatalf("stageCompose error = %v", err)
	}
	if !inst.failed || !inst.closed {
		t.Fatalf("installer failed=%v closed=%v, want both true", inst.failed, inst.closed)
	}
}

func TestInternalRegistryStageComposeFailsInstallerWriteAndClose(t *testing.T) {
	server := newTestServer(t)
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")

	for _, tt := range []struct {
		name    string
		inst    *stubInstaller
		wantErr error
		failed  bool
	}{
		{name: "write", inst: &stubInstaller{writeErr: writeErr}, wantErr: writeErr, failed: true},
		{name: "close", inst: &stubInstaller{closeErr: closeErr}, wantErr: closeErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storage := &internalRegistryStorage{
				s: server,
				newInstaller: func(_ *Server, cfg FileInstallerCfg) (registryInstaller, error) {
					return tt.inst, nil
				},
			}
			err := storage.stageCompose(&db.Data{}, "svc", "svc/app", true)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("stageCompose error = %v, want %v", err, tt.wantErr)
			}
			if tt.inst.failed != tt.failed {
				t.Fatalf("installer failed=%v, want %v", tt.inst.failed, tt.failed)
			}
		})
	}
}
