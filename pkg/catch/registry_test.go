// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
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
