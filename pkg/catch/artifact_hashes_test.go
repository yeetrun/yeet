// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestPayloadArtifactPath(t *testing.T) {
	tests := []struct {
		name     string
		service  *db.Service
		wantPath string
		wantKind string
	}{
		{
			name: "docker compose prefers typescript",
			service: serviceWithArtifacts(db.ServiceTypeDockerCompose, map[db.ArtifactName]string{
				db.ArtifactTypeScriptFile:    "/tmp/main.ts",
				db.ArtifactPythonFile:        "/tmp/main.py",
				db.ArtifactDockerComposeFile: "/tmp/compose.yml",
			}),
			wantPath: "/tmp/main.ts",
			wantKind: "typescript",
		},
		{
			name: "docker compose falls through to python",
			service: serviceWithArtifacts(db.ServiceTypeDockerCompose, map[db.ArtifactName]string{
				db.ArtifactPythonFile:        "/tmp/main.py",
				db.ArtifactDockerComposeFile: "/tmp/compose.yml",
			}),
			wantPath: "/tmp/main.py",
			wantKind: "python",
		},
		{
			name: "docker compose falls through to compose file",
			service: serviceWithArtifacts(db.ServiceTypeDockerCompose, map[db.ArtifactName]string{
				db.ArtifactDockerComposeFile: "/tmp/compose.yml",
			}),
			wantPath: "/tmp/compose.yml",
			wantKind: "docker compose",
		},
		{
			name: "systemd binary",
			service: serviceWithArtifacts(db.ServiceTypeSystemd, map[db.ArtifactName]string{
				db.ArtifactBinary: "/tmp/svc",
			}),
			wantPath: "/tmp/svc",
			wantKind: "binary",
		},
		{
			name:     "no payload",
			service:  serviceWithArtifacts(db.ServiceTypeSystemd, nil),
			wantPath: "",
			wantKind: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotKind := payloadArtifactPath(tt.service.View())
			if gotPath != tt.wantPath || gotKind != tt.wantKind {
				t.Fatalf("payloadArtifactPath() = (%q, %q), want (%q, %q)", gotPath, gotKind, tt.wantPath, tt.wantKind)
			}
		})
	}

	gotPath, gotKind := payloadArtifactPath(db.ServiceView{})
	if gotPath != "" || gotKind != "" {
		t.Fatalf("invalid service payloadArtifactPath() = (%q, %q), want empty", gotPath, gotKind)
	}
}

func TestArtifactHashes(t *testing.T) {
	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "main.py")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(payloadPath, []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := newTestServer(t)
	service := serviceWithArtifacts(db.ServiceTypeDockerCompose, map[db.ArtifactName]string{
		db.ArtifactPythonFile: payloadPath,
		db.ArtifactEnvFile:    envPath,
	})
	service.Name = "api"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": service}}); err != nil {
		t.Fatal(err)
	}

	got, err := server.artifactHashes("api")
	if err != nil {
		t.Fatalf("artifactHashes: %v", err)
	}
	if !got.Found {
		t.Fatal("artifactHashes did not find service")
	}
	assertArtifactHash(t, got.Payload, "python", "print('hi')\n")
	assertArtifactHash(t, got.Env, "env file", "A=1\n")
}

func TestArtifactHashesMissingService(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{}}); err != nil {
		t.Fatal(err)
	}

	got, err := server.artifactHashes("missing")
	if err != nil {
		t.Fatalf("artifactHashes: %v", err)
	}
	if got.Found || got.Message != "service not found" {
		t.Fatalf("artifactHashes missing = %+v, want not found message", got)
	}
}

func TestHashFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFileSHA256(path)
	if err != nil {
		t.Fatalf("hashFileSHA256: %v", err)
	}
	if want := sha256Hex("payload"); got != want {
		t.Fatalf("hashFileSHA256 = %q, want %q", got, want)
	}

	got, err = hashFileSHA256(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatalf("hashFileSHA256 missing: %v", err)
	}
	if got != "" {
		t.Fatalf("hashFileSHA256 missing = %q, want empty", got)
	}
}

func serviceWithArtifacts(serviceType db.ServiceType, paths map[db.ArtifactName]string) *db.Service {
	artifacts := db.ArtifactStore{}
	for name, path := range paths {
		artifacts[name] = &db.Artifact{Refs: map[db.ArtifactRef]string{"latest": path}}
	}
	return &db.Service{
		Name:        "svc",
		ServiceType: serviceType,
		Artifacts:   artifacts,
	}
}

func assertArtifactHash(t *testing.T, got *catchrpc.ArtifactHash, wantKind, content string) {
	t.Helper()
	if got == nil {
		t.Fatalf("artifact hash is nil, want kind %q", wantKind)
	}
	if got.Kind != wantKind {
		t.Fatalf("artifact kind = %q, want %q", got.Kind, wantKind)
	}
	if want := sha256Hex(content); got.SHA256 != want {
		t.Fatalf("artifact hash = %q, want %q", got.SHA256, want)
	}
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
