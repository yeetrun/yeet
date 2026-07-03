// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"gopkg.in/yaml.v3"
)

type hostStorageArtifactRepairResult struct {
	Rewritten                int
	SystemdArtifactsRepaired bool
}

type hostStorageArtifactPathForRepair struct {
	Path    string
	Current bool
}

func hostStorageGeneratedArtifactRewriteMappings(mappings hostStoragePathMappings) hostStoragePathMappings {
	return mappings.Sorted()
}

func repairHostStorageGeneratedArtifacts(service *db.Service, mappings hostStoragePathMappings) (hostStorageArtifactRepairResult, error) {
	var result hostStorageArtifactRepairResult
	if service == nil || len(mappings) == 0 {
		return result, nil
	}
	artifactNames := make([]db.ArtifactName, 0, len(service.Artifacts))
	for name := range service.Artifacts {
		artifactNames = append(artifactNames, name)
	}
	slices.SortFunc(artifactNames, func(a, b db.ArtifactName) int {
		return strings.Compare(string(a), string(b))
	})
	for _, name := range artifactNames {
		artifactResult, err := repairHostStorageGeneratedArtifact(service, name, mappings)
		if err != nil {
			return result, err
		}
		result.Rewritten += artifactResult.Rewritten
		if artifactResult.SystemdArtifactsRepaired {
			result.SystemdArtifactsRepaired = true
		}
	}
	return result, nil
}

func repairHostStorageGeneratedArtifact(service *db.Service, name db.ArtifactName, mappings hostStoragePathMappings) (hostStorageArtifactRepairResult, error) {
	var result hostStorageArtifactRepairResult
	if !hostStorageArtifactMayContainPaths(name) {
		return result, nil
	}
	paths := hostStorageGeneratedArtifactPathsForRepair(service.Artifacts[name], service.Generation, service.LatestGeneration)
	if len(paths) == 0 {
		return result, nil
	}
	for _, artifactPath := range paths {
		rewritten, err := repairHostStorageGeneratedArtifactPath(service, name, artifactPath, mappings)
		if err != nil {
			return result, err
		}
		if !rewritten {
			continue
		}
		result.Rewritten++
		result.SystemdArtifactsRepaired = result.SystemdArtifactsRepaired || hostStorageArtifactNeedsSystemdInstall(name)
	}
	return result, nil
}

func hostStorageGeneratedArtifactPathsForRepair(artifact *db.Artifact, generation, latestGeneration int) []hostStorageArtifactPathForRepair {
	if artifact == nil {
		return nil
	}
	currentRef, currentPath, hasCurrent := currentHostStorageArtifactRef(artifact, generation, latestGeneration)
	var paths []hostStorageArtifactPathForRepair
	seen := map[string]int{}
	add := func(path string, current bool) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if idx, ok := seen[path]; ok {
			paths[idx].Current = paths[idx].Current || current
			return
		}
		seen[path] = len(paths)
		paths = append(paths, hostStorageArtifactPathForRepair{Path: path, Current: current})
	}
	if hasCurrent {
		add(currentPath, true)
	}
	refs := make([]db.ArtifactRef, 0, len(artifact.Refs))
	for ref := range artifact.Refs {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b db.ArtifactRef) int {
		return strings.Compare(string(a), string(b))
	})
	currentPath = filepath.Clean(currentPath)
	for _, ref := range refs {
		path := artifact.Refs[ref]
		current := hasCurrent && (ref == currentRef || filepath.Clean(strings.TrimSpace(path)) == currentPath)
		add(path, current)
	}
	return paths
}

func repairHostStorageGeneratedArtifactPath(service *db.Service, name db.ArtifactName, artifactPath hostStorageArtifactPathForRepair, mappings hostStoragePathMappings) (bool, error) {
	path := filepath.Clean(artifactPath.Path)
	refs, err := scanHostStorageTextFileRefs(path, string(name), mappings)
	if errors.Is(err, os.ErrNotExist) && !artifactPath.Current {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scan generated artifact %s for %s: %w", name, service.Name, err)
	}
	if len(refs) == 0 {
		return false, nil
	}
	if err := rewriteHostStorageGeneratedArtifactPath(name, path, mappings); err != nil {
		return false, fmt.Errorf("rewrite generated artifact %s for %s: %w", name, service.Name, err)
	}
	return true, nil
}

func rewriteHostStorageGeneratedArtifactPath(name db.ArtifactName, path string, mappings hostStoragePathMappings) error {
	if name == db.ArtifactDockerComposeFile {
		return rewriteHostStorageGeneratedComposeArtifact(path, mappings)
	}
	if serviceRootMigrationTextArtifacts[name] {
		return rewriteHostStorageGeneratedTextArtifact(path, mappings)
	}
	return nil
}

func rewriteHostStorageGeneratedTextArtifact(path string, mappings hostStoragePathMappings) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("artifact path is a directory")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rewritten, changed, err := rewriteHostStoragePathCandidates(content, mappings)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return writeFileAtomically(path, rewritten, info.Mode().Perm())
}

func rewriteHostStoragePathCandidates(content []byte, mappings hostStoragePathMappings) ([]byte, bool, error) {
	var out bytes.Buffer
	changed := false
	written := 0
	for idx := 0; idx < len(content); {
		start, end, next, ok := nextHostStoragePathCandidate(content, idx)
		if !ok {
			idx = next
			continue
		}
		rewritten, didChange, err := mappings.Rewrite(string(content[start:end]))
		if err != nil {
			return nil, false, err
		}
		if !didChange {
			idx = next
			continue
		}
		if !changed {
			out.Grow(len(content))
			changed = true
		}
		out.Write(content[written:start])
		out.WriteString(rewritten)
		written = end
		idx = next
	}
	if !changed {
		return content, false, nil
	}
	out.Write(content[written:])
	return out.Bytes(), true, nil
}

func nextHostStoragePathCandidate(content []byte, idx int) (start int, end int, next int, ok bool) {
	if content[idx] != '/' || !rootPathBoundaryBefore(content, idx) {
		return 0, 0, idx + 1, false
	}
	end = idx + 1
	for end < len(content) && isRootPathByte(content[end]) {
		end++
	}
	if !rootPathBoundaryAfter(content, end) {
		return 0, 0, end, false
	}
	return idx, end, end, true
}

func rewriteHostStorageGeneratedComposeArtifact(path string, mappings hostStoragePathMappings) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("artifact path is a directory")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return fmt.Errorf("parse compose yaml: %w", err)
	}
	changed, err := rewriteHostStorageComposeVolumeMappings(&doc, mappings)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	rewritten, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal compose yaml: %w", err)
	}
	return writeFileAtomically(path, rewritten, info.Mode().Perm())
}

func rewriteHostStorageComposeVolumeMappings(doc *yaml.Node, mappings hostStoragePathMappings) (bool, error) {
	root := yamlDocumentRoot(doc)
	services := yamlMappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return false, nil
	}
	changed := false
	for i := 1; i < len(services.Content); i += 2 {
		service := services.Content[i]
		volumes := yamlMappingValue(service, "volumes")
		if volumes == nil || volumes.Kind != yaml.SequenceNode {
			continue
		}
		for _, volume := range volumes.Content {
			volumeChanged, err := rewriteHostStorageComposeVolumeMapping(volume, mappings)
			if err != nil {
				return false, err
			}
			changed = changed || volumeChanged
		}
	}
	return changed, nil
}

func rewriteHostStorageComposeVolumeMapping(volume *yaml.Node, mappings hostStoragePathMappings) (bool, error) {
	switch volume.Kind {
	case yaml.ScalarNode:
		rewritten, changed, err := rewriteHostStorageComposeVolumeString(volume.Value, mappings)
		if err != nil || !changed {
			return changed, err
		}
		volume.Value = rewritten
		return true, nil
	case yaml.MappingNode:
		return rewriteHostStorageComposeVolumeMappingSource(volume, mappings)
	default:
		return false, nil
	}
}

func rewriteHostStorageComposeVolumeMappingSource(volume *yaml.Node, mappings hostStoragePathMappings) (bool, error) {
	if typ := yamlMappingValue(volume, "type"); typ != nil && !strings.EqualFold(typ.Value, "bind") {
		return false, nil
	}
	for _, key := range []string{"source", "src"} {
		source := yamlMappingValue(volume, key)
		if source == nil || source.Kind != yaml.ScalarNode {
			continue
		}
		rewritten, changed, err := mappings.Rewrite(source.Value)
		if err != nil || !changed {
			return changed, err
		}
		source.Value = rewritten
		return true, nil
	}
	return false, nil
}

func rewriteHostStorageComposeVolumeString(volume string, mappings hostStoragePathMappings) (string, bool, error) {
	if rewritten, changed, err := rewriteHostStorageComposeKeyValueVolumeString(volume, mappings); changed || err != nil {
		return rewritten, changed, err
	}
	source, rest, ok := strings.Cut(volume, ":")
	if !ok || source == "" {
		return volume, false, nil
	}
	rewritten, changed, err := mappings.Rewrite(source)
	if err != nil || !changed {
		return volume, changed, err
	}
	return rewritten + ":" + rest, true, nil
}

func rewriteHostStorageComposeKeyValueVolumeString(volume string, mappings hostStoragePathMappings) (string, bool, error) {
	parts := strings.Split(volume, ",")
	changed := false
	for i, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || (key != "source" && key != "src") {
			continue
		}
		rewritten, didChange, err := mappings.Rewrite(value)
		if err != nil {
			return "", false, err
		}
		if didChange {
			parts[i] = key + "=" + rewritten
			changed = true
		}
	}
	if !changed {
		return volume, false, nil
	}
	return strings.Join(parts, ","), true, nil
}

func hostStorageArtifactMayContainPaths(name db.ArtifactName) bool {
	return name == db.ArtifactDockerComposeFile || serviceRootMigrationTextArtifacts[name]
}

func hostStorageArtifactNeedsSystemdInstall(name db.ArtifactName) bool {
	switch name {
	case db.ArtifactSystemdUnit,
		db.ArtifactSystemdTimerFile,
		db.ArtifactNetNSService,
		db.ArtifactNetNSEnv,
		db.ArtifactTSService,
		db.ArtifactTSEnv:
		return true
	default:
		return false
	}
}

func serviceHasHostStorageSystemdArtifacts(service *db.Service) bool {
	if service == nil {
		return false
	}
	for name, artifact := range service.Artifacts {
		if !hostStorageArtifactNeedsSystemdInstall(name) {
			continue
		}
		if _, path, ok := currentHostStorageArtifactRef(artifact, service.Generation, service.LatestGeneration); ok && strings.TrimSpace(path) != "" {
			return true
		}
	}
	return false
}

func reinstallHostStorageServiceUnits(ctx context.Context, cfg Config, service *db.Service) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !shouldReinstallHostStorageServiceUnits(service) {
		return nil, nil
	}
	systemdService, err := newHostStorageSystemdService(cfg, service)
	if err != nil {
		return nil, err
	}
	units, err := systemdService.StageInstallForReload()
	if err != nil {
		return nil, fmt.Errorf("stage systemd service %s: %w", service.Name, err)
	}
	return units, nil
}

func shouldReinstallHostStorageServiceUnits(service *db.Service) bool {
	if service == nil {
		return false
	}
	if hostStorageSelfManagedService(service.Name) {
		return false
	}
	if service.ServiceType == db.ServiceTypeVM {
		return false
	}
	return serviceHasHostStorageSystemdArtifacts(service)
}

func newHostStorageSystemdService(cfg Config, service *db.Service) (*svc.SystemdService, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("host storage unit reinstall requires a db store")
	}
	root := serviceRootFromConfig(cfg, *service)
	systemdService, err := svc.NewSystemdService(cfg.DB, service.View(), serviceRunDirForRoot(root))
	if err != nil {
		return nil, fmt.Errorf("load systemd service %s: %w", service.Name, err)
	}
	return systemdService, nil
}
