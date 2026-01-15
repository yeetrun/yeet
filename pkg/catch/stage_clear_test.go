// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	cdb "github.com/yeetrun/yeet/pkg/db"
)

func TestStageClearRemovesStagedRefsAndFiles(t *testing.T) {
	server := newTestServer(t)
	stageDir := t.TempDir()

	stageFile := filepath.Join(stageDir, "staged.bin")
	stageKeep := filepath.Join(stageDir, "keep.bin")
	latestFile := filepath.Join(stageDir, "latest.bin")
	if err := os.WriteFile(stageFile, []byte("staged"), 0o644); err != nil {
		t.Fatalf("write stage file: %v", err)
	}
	if err := os.WriteFile(stageKeep, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write keep file: %v", err)
	}
	if err := os.WriteFile(latestFile, []byte("latest"), 0o644); err != nil {
		t.Fatalf("write latest file: %v", err)
	}

	_, _, err := server.cfg.DB.MutateService("svc-stage", func(d *cdb.Data, s *cdb.Service) error {
		s.ServiceType = cdb.ServiceTypeSystemd
		s.Artifacts = cdb.ArtifactStore{
			cdb.ArtifactBinary: {
				Refs: map[cdb.ArtifactRef]string{
					"staged": stageFile,
					"latest": latestFile,
				},
			},
			cdb.ArtifactEnvFile: {
				Refs: map[cdb.ArtifactRef]string{
					"staged": stageKeep,
					"latest": stageKeep,
				},
			},
		}
		if d.Images == nil {
			d.Images = map[cdb.ImageRepoName]*cdb.ImageRepo{}
		}
		d.Images[cdb.ImageRepoName("svc-stage")] = &cdb.ImageRepo{
			Refs: map[cdb.ImageRef]cdb.ImageManifest{
				"staged": {ContentType: "application/vnd.oci.image.manifest.v1+json", BlobHash: "sha256:deadbeef"},
				"latest": {ContentType: "application/vnd.oci.image.manifest.v1+json", BlobHash: "sha256:cafebabe"},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mutate service: %v", err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "svc-stage",
		rw: &out,
	}
	if err := execer.clearStage(); err != nil {
		t.Fatalf("clear stage: %v", err)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	sv, ok := dv.Services().GetOk("svc-stage")
	if !ok {
		t.Fatalf("service missing")
	}
	for name, art := range sv.AsStruct().Artifacts {
		if art == nil || art.Refs == nil {
			continue
		}
		if _, ok := art.Refs[cdb.ArtifactRef("staged")]; ok {
			t.Fatalf("staged ref still present for %s", name)
		}
	}
	if dv.AsStruct().Images != nil {
		repo := dv.AsStruct().Images[cdb.ImageRepoName("svc-stage")]
		if repo != nil {
			if _, ok := repo.Refs[cdb.ImageRef("staged")]; ok {
				t.Fatalf("staged image ref still present")
			}
		}
	}

	if _, err := os.Stat(stageFile); !os.IsNotExist(err) {
		t.Fatalf("expected staged file removed, got err=%v", err)
	}
	if _, err := os.Stat(stageKeep); err != nil {
		t.Fatalf("expected shared staged file to remain: %v", err)
	}
}

func TestStageClearNoStagedChanges(t *testing.T) {
	server := newTestServer(t)
	_, _, err := server.cfg.DB.MutateService("svc-empty", func(_ *cdb.Data, s *cdb.Service) error {
		s.ServiceType = cdb.ServiceTypeSystemd
		s.Artifacts = cdb.ArtifactStore{
			cdb.ArtifactBinary: {Refs: map[cdb.ArtifactRef]string{"latest": "/tmp/latest.bin"}},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mutate service: %v", err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "svc-empty",
		rw: &out,
	}
	if err := execer.clearStage(); err != nil {
		t.Fatalf("clear stage: %v", err)
	}
}
