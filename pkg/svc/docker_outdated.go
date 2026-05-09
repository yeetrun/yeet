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

type dockerComposeDeclaredImages struct {
	byService map[string]string
	byRepo    map[string]string
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

func parseComposeConfigImages(output []byte) (dockerComposeDeclaredImages, error) {
	var config struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &config); err != nil {
		return dockerComposeDeclaredImages{}, fmt.Errorf("parse docker compose config JSON: %w", err)
	}
	declared := dockerComposeDeclaredImages{
		byService: make(map[string]string, len(config.Services)),
		byRepo:    make(map[string]string, len(config.Services)),
	}
	for serviceName, service := range config.Services {
		image := strings.TrimSpace(service.Image)
		if image == "" {
			continue
		}
		declared.byService[serviceName] = image
		repo := imageRepositoryName(image)
		if _, ok := declared.byRepo[repo]; !ok {
			declared.byRepo[repo] = image
		}
	}
	return declared, nil
}

func (d dockerComposeDeclaredImages) imageForContainer(container dockerComposePSRow) (string, bool) {
	if image, ok := d.byService[container.Service]; ok {
		return image, true
	}
	image, ok := d.byRepo[imageRepositoryName(container.Image)]
	return image, ok
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
	if isImplicitDockerHubNamespace(parts[0]) {
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

func isImplicitDockerHubNamespace(firstPathComponent string) bool {
	return firstPathComponent != "localhost" && !strings.ContainsAny(firstPathComponent, ".:")
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
			if err := desc.Digest.Validate(); err != nil {
				return "", false, fmt.Errorf("matching manifest descriptor for %s/%s has invalid digest %q: %w", osName, arch, digest, err)
			}
			return digest, true, nil
		}
	}
	return "", false, nil
}

func (s *DockerComposeService) Outdated(ctx context.Context, opts DockerOutdatedOptions) ([]DockerOutdatedRow, error) {
	declared, err := s.composeDeclaredImages(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := s.composeContainers(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]DockerOutdatedRow, 0, len(containers))
	for _, container := range containers {
		if container.State != "" && container.State != "running" {
			continue
		}
		base := dockerOutdatedRowForComposeContainer(s.Name, container)
		if declaredImage, ok := declared.imageForContainer(container); ok {
			base.Image = declaredImage
		}
		if isInternalRegistryImage(container.Image) || isInternalRegistryImage(base.Image) {
			if !isInternalRegistryImage(base.Image) {
				base.Image = container.Image
			}
			rows = appendFilteredDockerOutdatedRow(rows, base, opts)
			continue
		}
		row := s.outdatedRowForContainer(ctx, container, declared)
		rows = appendFilteredDockerOutdatedRow(rows, row, opts)
	}
	return rows, nil
}

func appendFilteredDockerOutdatedRow(rows []DockerOutdatedRow, row DockerOutdatedRow, opts DockerOutdatedOptions) []DockerOutdatedRow {
	filtered := filterDockerOutdatedRow(row, opts)
	if filtered == nil {
		return rows
	}
	return append(rows, *filtered)
}

func (s *DockerComposeService) composeDeclaredImages(ctx context.Context) (dockerComposeDeclaredImages, error) {
	out, err := s.readonlyComposeOutput(ctx, "config", "--format", "json")
	if err != nil {
		return dockerComposeDeclaredImages{}, fmt.Errorf("docker compose config --format json: %w", err)
	}
	return parseComposeConfigImages(out)
}

func (s *DockerComposeService) composeContainers(ctx context.Context) ([]dockerComposePSRow, error) {
	out, err := s.readonlyComposeOutput(ctx, "ps", "--format=json")
	if err != nil {
		return nil, fmt.Errorf("docker compose ps --format=json: %w", err)
	}
	return parseComposePSJSON(out)
}

func (s *DockerComposeService) readonlyComposeOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd, err := s.readonlyComposeCommandContext(ctx, args...)
	if err != nil {
		return nil, err
	}
	return cmd.Output()
}

func (s *DockerComposeService) dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	cmd := s.newDockerCommand(ctx, dockerPath, args...)
	cmd.Dir = s.DataDir
	return cmd.Output()
}

func (s *DockerComposeService) outdatedRowForContainer(ctx context.Context, container dockerComposePSRow, declared dockerComposeDeclaredImages) DockerOutdatedRow {
	row := dockerOutdatedRowForComposeContainer(s.Name, container)
	declaredImage, ok := declared.imageForContainer(container)
	if !ok {
		row.Status = DockerOutdatedUnknown
		row.Reason = "image not declared by compose config"
		return row
	}
	row.Image = declaredImage
	inspect, err := s.inspectContainerImage(ctx, container.ID, declaredImage)
	if err != nil {
		row.Status = DockerOutdatedError
		row.Reason = err.Error()
		return row
	}
	row.RunningDigest = inspect.runningDigest
	latest, err := s.latestImageDigest(ctx, declaredImage, inspect.os, inspect.architecture)
	if err != nil {
		row.Status = DockerOutdatedError
		row.Reason = err.Error()
		return row
	}
	row.LatestDigest = latest
	return compareDockerOutdatedRow(row)
}

func dockerOutdatedRowForComposeContainer(serviceName string, container dockerComposePSRow) DockerOutdatedRow {
	row := DockerOutdatedRow{
		ServiceName:   serviceName,
		ContainerID:   container.ID,
		ContainerName: container.Service,
		Image:         container.Image,
	}
	if row.ContainerName == "" {
		row.ContainerName = container.Name
	}
	return row
}

type runningImageInspect struct {
	runningDigest string
	os            string
	architecture  string
}

func filterDockerOutdatedRow(row DockerOutdatedRow, opts DockerOutdatedOptions) *DockerOutdatedRow {
	if isInternalRegistryImage(row.Image) {
		if !opts.IncludeInternal {
			return nil
		}
		row.Status = DockerOutdatedUnknown
		row.Reason = "internal image"
		return &row
	}
	if row.Status == "" {
		row = compareDockerOutdatedRow(row)
	}
	if row.Status == DockerOutdatedCurrent {
		return nil
	}
	return &row
}

func (s *DockerComposeService) inspectContainerImage(ctx context.Context, containerID, image string) (runningImageInspect, error) {
	out, err := s.dockerOutput(ctx, "inspect", containerID)
	if err != nil {
		return runningImageInspect{}, fmt.Errorf("docker inspect container: %w", err)
	}
	var containers []struct {
		Image string `json:"Image"`
	}
	if err := json.Unmarshal(out, &containers); err != nil {
		return runningImageInspect{}, fmt.Errorf("parse docker inspect container: %w", err)
	}
	if len(containers) == 0 {
		return runningImageInspect{}, fmt.Errorf("parse docker inspect container: empty result")
	}
	imageOut, err := s.dockerOutput(ctx, "image", "inspect", containers[0].Image)
	if err != nil {
		return runningImageInspect{}, fmt.Errorf("docker image inspect: %w", err)
	}
	var images []dockerImageInspectRow
	if err := json.Unmarshal(imageOut, &images); err != nil {
		return runningImageInspect{}, fmt.Errorf("parse docker image inspect: %w", err)
	}
	if len(images) == 0 {
		return runningImageInspect{}, fmt.Errorf("parse docker image inspect: empty result")
	}
	return runningImageInspect{
		runningDigest: selectRepoDigestForImage(images[0].RepoDigests, image),
		os:            images[0].OS,
		architecture:  images[0].Architecture,
	}, nil
}

func (s *DockerComposeService) latestImageDigest(ctx context.Context, image, osName, arch string) (string, error) {
	if pinned, rawDigest, ok := strings.Cut(image, "@"); ok && strings.TrimSpace(pinned) != "" {
		parsed, err := parseDockerDigest(rawDigest)
		if err != nil {
			return "", fmt.Errorf("invalid pinned image digest %q: %w", rawDigest, err)
		}
		return parsed, nil
	}
	raw, err := s.dockerOutput(ctx, "buildx", "imagetools", "inspect", image, "--raw")
	if err != nil {
		return "", fmt.Errorf("inspect upstream image: %w", err)
	}
	digest, ok, err := platformDigestFromRawManifest(raw, osName, arch)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no upstream digest for platform %s/%s", osName, arch)
	}
	return digest, nil
}

func parseDockerDigest(rawDigest string) (string, error) {
	var desc ocispec.Descriptor
	payload, err := json.Marshal(struct {
		Digest string `json:"digest"`
	}{Digest: rawDigest})
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(payload, &desc); err != nil {
		return "", err
	}
	if err := desc.Digest.Validate(); err != nil {
		return "", err
	}
	return desc.Digest.String(), nil
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
