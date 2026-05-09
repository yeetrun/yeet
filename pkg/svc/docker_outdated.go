// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type DockerOutdatedStatus string

const (
	DockerOutdatedUpdateAvailable DockerOutdatedStatus = "update available"
	DockerOutdatedCurrent         DockerOutdatedStatus = "current"
	DockerOutdatedUnknown         DockerOutdatedStatus = "unknown"
	DockerOutdatedError           DockerOutdatedStatus = "error"
)

type DockerOutdatedOptions struct {
	IncludeInternal bool
}

type DockerOutdatedRow struct {
	ServiceName   string               `json:"serviceName"`
	ContainerID   string               `json:"containerID,omitempty"`
	ContainerName string               `json:"containerName"`
	Image         string               `json:"image"`
	RunningDigest string               `json:"runningDigest,omitempty"`
	LatestDigest  string               `json:"latestDigest,omitempty"`
	Status        DockerOutdatedStatus `json:"status"`
	Reason        string               `json:"reason,omitempty"`
}

type dockerComposePSRow struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	Image   string `json:"Image"`
	State   string `json:"State"`
}

type dockerImageInspectRow struct {
	ID           string   `json:"Id"`
	RepoDigests  []string `json:"RepoDigests"`
	Architecture string   `json:"Architecture"`
	OS           string   `json:"Os"`
}

func parseComposeImages(output string) []string {
	lines := splitNonEmptyLines(output)
	images := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		image := strings.TrimSpace(line)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		images = append(images, image)
	}
	return images
}

func parseComposePSJSON(output []byte) ([]dockerComposePSRow, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil, nil
	}
	if output[0] == '[' {
		var rows []dockerComposePSRow
		if err := json.Unmarshal(output, &rows); err != nil {
			return nil, fmt.Errorf("parse docker compose ps JSON array: %w", err)
		}
		return rows, nil
	}
	dec := json.NewDecoder(bytes.NewReader(output))
	rows := make([]dockerComposePSRow, 0)
	for {
		var row dockerComposePSRow
		if err := dec.Decode(&row); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse docker compose ps JSON line: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func selectRepoDigestForImage(repoDigests []string, image string) string {
	repo := imageRepositoryName(image)
	for _, candidate := range repoDigests {
		candidateRepo, digest, ok := strings.Cut(candidate, "@")
		if !ok {
			continue
		}
		if imageRepositoryName(candidateRepo) == repo {
			return digest
		}
	}
	return ""
}

func imageRepositoryName(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if repo, _, ok := strings.Cut(image, "@"); ok {
		image = repo
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon > lastSlash {
		image = image[:lastColon]
	}
	parts := strings.Split(image, "/")
	if len(parts) == 1 {
		return "docker.io/library/" + image
	}
	if !strings.ContainsAny(parts[0], ".:") {
		return "docker.io/" + image
	}
	if parts[0] == "index.docker.io" {
		parts[0] = "docker.io"
	}
	if parts[0] == "docker.io" && len(parts) == 2 {
		return "docker.io/library/" + parts[1]
	}
	if parts[0] == "docker.io" {
		return strings.Join(parts, "/")
	}
	return image
}

func isInternalRegistryImage(image string) bool {
	return strings.HasPrefix(imageRepositoryName(image), InternalRegistryHost+"/")
}

func compareDockerOutdatedRow(row DockerOutdatedRow) DockerOutdatedRow {
	if row.RunningDigest == "" {
		row.Status = DockerOutdatedUnknown
		row.Reason = "missing running digest"
		return row
	}
	if row.LatestDigest == "" {
		row.Status = DockerOutdatedUnknown
		row.Reason = "missing latest digest"
		return row
	}
	if row.RunningDigest == row.LatestDigest {
		row.Status = DockerOutdatedCurrent
		row.Reason = ""
		return row
	}
	row.Status = DockerOutdatedUpdateAvailable
	row.Reason = ""
	return row
}

func digestFromManifestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func platformDigestFromRawManifest(raw []byte, osName, arch string) (string, bool, error) {
	var index struct {
		MediaType string               `json:"mediaType"`
		Manifests []ocispec.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return "", false, fmt.Errorf("parse manifest: %w", err)
	}
	if len(index.Manifests) == 0 {
		return digestFromManifestBytes(raw), true, nil
	}
	for _, desc := range index.Manifests {
		if desc.Platform == nil {
			continue
		}
		if desc.Platform.OS == osName && desc.Platform.Architecture == arch {
			digest := desc.Digest.String()
			if digest == "" {
				return "", false, fmt.Errorf("matching manifest descriptor for %s/%s has empty digest", osName, arch)
			}
			return digest, true, nil
		}
	}
	return "", false, nil
}

func (s *DockerComposeService) readonlyComposeCommandContext(ctx context.Context, args ...string) (*exec.Cmd, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	nargs, err := s.composeCommandArgs()
	if err != nil {
		return nil, err
	}
	args = append(nargs, args...)
	cmd := s.newDockerCommand(ctx, dockerPath, args...)
	cmd.Dir = s.DataDir
	return cmd, nil
}
