// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRewriteHostStorageGeneratedArtifactsRewritesCurrentTextArtifacts(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, "yeet-nginx-ns.service")
	composePath := filepath.Join(root, "compose.yml")
	binaryPath := filepath.Join(root, "nginx")
	if err := os.WriteFile(unitPath, []byte("ExecStart=/root/data/services/catch/data/service-ns\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    volumes:\n      - /root/data/services/nginx/data:/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("/root/data/services/nginx/bin/nginx"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{
		Name:       "nginx",
		Generation: 4,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(4): unitPath}},
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(4): composePath,
					db.Gen(3): filepath.Join(root, "old-compose.yml"),
				},
			},
			db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{db.Gen(4): binaryPath}},
		},
	}

	result, err := repairHostStorageGeneratedArtifacts(service, hostStoragePathMappings{
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
		{From: "/root/data/services", To: "/flash/yeet/services", Reason: hostStoragePathReasonServicesDir},
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	})
	if err != nil {
		t.Fatalf("repairHostStorageGeneratedArtifacts error: %v", err)
	}
	if result.Rewritten != 2 {
		t.Fatalf("Rewritten = %d, want 2", result.Rewritten)
	}
	for _, path := range []string{unitPath, composePath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "/root/data") {
			t.Fatalf("%s still contains old root: %s", path, raw)
		}
	}
	raw, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "/root/data/services/nginx/bin/nginx" {
		t.Fatalf("binary artifact was rewritten: %q", raw)
	}
}

func TestRewriteHostStorageGeneratedArtifactsRewritesComposeMappingSources(t *testing.T) {
	root := t.TempDir()
	composePath := filepath.Join(root, "compose.yml")
	if err := os.WriteFile(composePath, []byte(`services:
  app:
    volumes:
      - type: bind
        source: /root/data/services/nginx/data
        target: /data
      - type: volume
        source: /root/data/services/nginx/named
        target: /named
`), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{
		Name:       "nginx",
		Generation: 2,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(2): composePath}},
		},
	}

	result, err := repairHostStorageGeneratedArtifacts(service, hostStoragePathMappings{
		{From: "/root/data/services", To: "/flash/yeet/services", Reason: hostStoragePathReasonServicesDir},
	})
	if err != nil {
		t.Fatalf("repairHostStorageGeneratedArtifacts error: %v", err)
	}
	if result.Rewritten != 1 {
		t.Fatalf("Rewritten = %d, want 1", result.Rewritten)
	}
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "source: /flash/yeet/services/nginx/data") {
		t.Fatalf("compose bind source was not rewritten: %s", text)
	}
	if !strings.Contains(text, "source: /root/data/services/nginx/named") {
		t.Fatalf("non-bind source was rewritten: %s", text)
	}
}

func TestRewriteHostStorageGeneratedArtifactsRewritesOlderExistingTextArtifacts(t *testing.T) {
	root := t.TempDir()
	currentUnitPath := filepath.Join(root, "yeet-api-ns-current.service")
	oldUnitPath := filepath.Join(root, "yeet-api-ns-old.service")
	missingOldUnitPath := filepath.Join(root, "missing-old.service")
	for _, path := range []string{currentUnitPath, oldUnitPath} {
		if err := os.WriteFile(path, []byte("ExecStart=/root/data/services/catch/data/service-ns\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	service := &db.Service{
		Name:             "api",
		Generation:       4,
		LatestGeneration: 4,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{
				db.Gen(2): missingOldUnitPath,
				db.Gen(3): oldUnitPath,
				db.Gen(4): currentUnitPath,
				"latest":  currentUnitPath,
			}},
		},
	}

	result, err := repairHostStorageGeneratedArtifacts(service, hostStoragePathMappings{
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
	})
	if err != nil {
		t.Fatalf("repairHostStorageGeneratedArtifacts error: %v", err)
	}
	if result.Rewritten != 2 {
		t.Fatalf("Rewritten = %d, want 2", result.Rewritten)
	}
	for _, path := range []string{currentUnitPath, oldUnitPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "/root/data") {
			t.Fatalf("%s still contains old root: %s", path, raw)
		}
	}
}
