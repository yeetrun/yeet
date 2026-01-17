// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

func (s *Server) artifactHashes(service string) (catchrpc.ArtifactHashesResponse, error) {
	sv, err := s.serviceView(service)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return catchrpc.ArtifactHashesResponse{Found: false, Message: "service not found"}, nil
		}
		return catchrpc.ArtifactHashesResponse{}, err
	}
	resp := catchrpc.ArtifactHashesResponse{Found: true}
	payloadPath, payloadKind := payloadArtifactPath(sv)
	if payloadPath != "" {
		hash, err := hashFileSHA256(payloadPath)
		if err != nil {
			return catchrpc.ArtifactHashesResponse{}, err
		}
		if hash != "" {
			resp.Payload = &catchrpc.ArtifactHash{Kind: payloadKind, SHA256: hash}
		}
	}
	if envPath, ok := latestArtifactPath(sv, db.ArtifactEnvFile); ok && envPath != "" {
		hash, err := hashFileSHA256(envPath)
		if err != nil {
			return catchrpc.ArtifactHashesResponse{}, err
		}
		if hash != "" {
			resp.Env = &catchrpc.ArtifactHash{Kind: "env file", SHA256: hash}
		}
	}
	return resp, nil
}

func payloadArtifactPath(sv db.ServiceView) (string, string) {
	if !sv.Valid() {
		return "", ""
	}
	serviceType := sv.ServiceType()
	svc := sv.AsStruct()
	if serviceType == db.ServiceTypeDockerCompose {
		if p, ok := latestArtifactPathFromStore(svc.Artifacts, db.ArtifactTypeScriptFile); ok {
			return p, "typescript"
		}
		if p, ok := latestArtifactPathFromStore(svc.Artifacts, db.ArtifactPythonFile); ok {
			return p, "python"
		}
		if p, ok := latestArtifactPathFromStore(svc.Artifacts, db.ArtifactDockerComposeFile); ok {
			return p, "docker compose"
		}
	}
	if serviceType == db.ServiceTypeSystemd {
		if p, ok := latestArtifactPathFromStore(svc.Artifacts, db.ArtifactBinary); ok {
			return p, "binary"
		}
	}
	return "", ""
}

func latestArtifactPath(sv db.ServiceView, name db.ArtifactName) (string, bool) {
	if !sv.Valid() {
		return "", false
	}
	return latestArtifactPathFromStore(sv.AsStruct().Artifacts, name)
}

func latestArtifactPathFromStore(artifacts db.ArtifactStore, name db.ArtifactName) (string, bool) {
	art, ok := artifacts[name]
	if !ok || art == nil || art.Refs == nil {
		return "", false
	}
	path, ok := art.Refs[db.ArtifactRef("latest")]
	return path, ok
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
