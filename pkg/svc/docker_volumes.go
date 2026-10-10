// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const dockerVolumeRemovalFile = "docker-volume-removal.json"
const composeProjectLabel = "com.docker.compose.project"

type dockerRemovalVolume struct {
	Name      string
	CreatedAt string
	Anonymous bool
}

type dockerVolumeRemovalPlan struct {
	Project string
	Volumes []dockerRemovalVolume
}

type dockerVolumeInfo struct {
	Name      string
	CreatedAt string
	Driver    string
	Scope     string
	Labels    map[string]string
	Options   map[string]string
}

type dockerVolumeContainer struct {
	Config     struct{ Labels map[string]string }
	Mounts     []struct{ Type, Name, Destination string }
	HostConfig struct {
		Binds       []string
		Mounts      []struct{ Type, Source, Target string }
		VolumesFrom []string
	}
}

// PrepareVolumeRemoval persists ownership evidence in the managed run directory
// before containers disappear. A saved plan is reused after partial removal.
func (s *DockerComposeService) PrepareVolumeRemoval(ctx context.Context) error {
	if _, err := s.loadVolumeRemovalPlan(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	external, err := s.externalComposeVolumes(ctx)
	if err != nil {
		return err
	}
	ids, err := s.projectContainerIDs(ctx)
	if err != nil {
		return err
	}
	names, err := s.dockerOutput(ctx, "volume", "ls", "-q", "--filter", "label="+composeProjectLabel+"="+s.composeProjectName())
	if err != nil {
		return err
	}
	candidates := make(map[string]bool)
	for _, name := range strings.Fields(string(names)) {
		candidates[name] = false
	}
	if err := s.addAnonymousVolumeCandidates(ctx, ids, candidates); err != nil {
		return err
	}
	plan, err := s.buildVolumeRemovalPlan(ctx, candidates, external, ids)
	if err != nil {
		return err
	}
	return s.saveVolumeRemovalPlan(plan)
}

func (s *DockerComposeService) buildVolumeRemovalPlan(ctx context.Context, candidates, external map[string]bool, ids []string) (dockerVolumeRemovalPlan, error) {
	plan := dockerVolumeRemovalPlan{Project: s.composeProjectName()}
	for _, name := range sortedVolumeNames(candidates) {
		if !validRemovalVolumeName(name) {
			return plan, fmt.Errorf("invalid Docker volume name %q", name)
		}
		if external[name] {
			continue
		}
		volume, err := s.inspectRemovalVolume(ctx, name)
		if err != nil {
			return plan, err
		}
		anonymous := candidates[name]
		if !volumeOwnedByProject(volume, plan.Project, anonymous) {
			continue
		}
		shared, err := s.volumeHasOtherContainers(ctx, name, ids)
		if err != nil {
			return plan, err
		}
		if !shared {
			plan.Volumes = append(plan.Volumes, dockerRemovalVolume{Name: name, CreatedAt: volume.CreatedAt, Anonymous: anonymous})
		}
	}
	return plan, nil
}

func sortedVolumeNames(candidates map[string]bool) []string {
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (s *DockerComposeService) externalComposeVolumes(ctx context.Context) (map[string]bool, error) {
	cmd, err := s.commandContext(ctx, "config", "--format", "json")
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve removal volumes: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var config struct {
		Volumes map[string]struct {
			Name     string
			External bool
		}
	}
	if err := json.Unmarshal(output, &config); err != nil {
		return nil, fmt.Errorf("decode removal volumes: %w", err)
	}
	external := make(map[string]bool)
	for key, volume := range config.Volumes {
		if volume.External {
			name := volume.Name
			if name == "" {
				name = key
			}
			external[name] = true
		}
	}
	return external, nil
}

func (s *DockerComposeService) addAnonymousVolumeCandidates(ctx context.Context, ids []string, candidates map[string]bool) error {
	for _, id := range ids {
		output, err := s.dockerOutput(ctx, "container", "inspect", id)
		if err != nil {
			return err
		}
		var containers []dockerVolumeContainer
		if err := json.Unmarshal(output, &containers); err != nil {
			return fmt.Errorf("decode volume mounts: %w", err)
		}
		if len(containers) != 1 || containers[0].Config.Labels[composeProjectLabel] != s.composeProjectName() {
			return fmt.Errorf("container %q changed ownership during removal", id)
		}
		container := containers[0]
		for _, mount := range container.Mounts {
			if mount.Type == "volume" && anonymousVolumeMount(container, mount.Name, mount.Destination) {
				candidates[mount.Name] = true
			}
		}
	}
	return nil
}

// Anonymous mounts have no explicit source. The random Docker name alone is
// insufficient: a named or external volume can also have a 64-character name.
func anonymousVolumeMount(container dockerVolumeContainer, name, target string) bool {
	if len(name) != 64 || !validDockerContainerID(name) || len(container.HostConfig.VolumesFrom) != 0 {
		return false
	}
	for _, bind := range container.HostConfig.Binds {
		parts := strings.Split(bind, ":")
		if len(parts) >= 2 && parts[1] == target {
			return false
		}
	}
	for _, mount := range container.HostConfig.Mounts {
		if mount.Target == target {
			return mount.Type == "volume" && mount.Source == ""
		}
	}
	// Image-declared VOLUME mounts do not appear in HostConfig.Mounts.
	return true
}

func volumeOwnedByProject(volume dockerVolumeInfo, project string, anonymous bool) bool {
	if !validRemovalVolumeName(volume.Name) || volume.CreatedAt == "" || volume.Driver != "local" || volume.Scope != "local" || len(volume.Options) != 0 {
		return false
	}
	owner := volume.Labels[composeProjectLabel]
	if anonymous {
		return owner == "" && len(volume.Name) == 64 && validDockerContainerID(volume.Name)
	}
	return owner == project && volume.Labels["com.docker.compose.volume"] != ""
}

func validRemovalVolumeName(name string) bool {
	if name == "" {
		return false
	}
	for i, char := range name {
		if asciiVolumeNameChar(char) {
			continue
		}
		if i > 0 && strings.ContainsRune("_.-", char) {
			continue
		}
		return false
	}
	return true
}

func asciiVolumeNameChar(char rune) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')
}

func (s *DockerComposeService) inspectRemovalVolume(ctx context.Context, name string) (dockerVolumeInfo, error) {
	output, err := s.dockerOutput(ctx, "volume", "inspect", name)
	if err != nil {
		return dockerVolumeInfo{}, err
	}
	var volumes []dockerVolumeInfo
	if err := json.Unmarshal(output, &volumes); err != nil {
		return dockerVolumeInfo{}, fmt.Errorf("decode removal volume: %w", err)
	}
	if len(volumes) != 1 || volumes[0].Name != name {
		return dockerVolumeInfo{}, fmt.Errorf("unexpected inspection for volume %q", name)
	}
	return volumes[0], nil
}

func (s *DockerComposeService) volumeHasOtherContainers(ctx context.Context, name string, projectIDs []string) (bool, error) {
	output, err := s.dockerOutput(ctx, "ps", "-aq", "--filter", "volume="+name)
	if err != nil {
		return false, err
	}
	for _, id := range strings.Fields(string(output)) {
		if !validDockerContainerID(id) {
			return false, fmt.Errorf("invalid volume user container ID %q", id)
		}
		if !slices.Contains(projectIDs, id) {
			return true, nil
		}
	}
	return false, nil
}

// RemoveVolumes only deletes volumes captured before teardown. Docker's
// non-forced removal provides the final protection against concurrent mounts.
// Keep the plan until the caller deletes the service's managed run directory.
func (s *DockerComposeService) RemoveVolumes(ctx context.Context) error {
	plan, err := s.loadVolumeRemovalPlan()
	if err != nil {
		return err
	}
	output, err := s.dockerOutput(ctx, "volume", "ls", "-q")
	if err != nil {
		return err
	}
	existing := strings.Fields(string(output))
	for _, pending := range plan.Volumes {
		if !slices.Contains(existing, pending.Name) {
			continue
		}
		if err := s.removePlannedVolume(ctx, plan.Project, pending); err != nil {
			return err
		}
	}
	return nil
}

func (s *DockerComposeService) removePlannedVolume(ctx context.Context, project string, pending dockerRemovalVolume) error {
	volume, err := s.inspectRemovalVolume(ctx, pending.Name)
	if err != nil {
		return err
	}
	if volume.CreatedAt != pending.CreatedAt || !volumeOwnedByProject(volume, project, pending.Anonymous) {
		return fmt.Errorf("volume %q changed identity or ownership; service retained for recovery", pending.Name)
	}
	shared, err := s.volumeHasOtherContainers(ctx, pending.Name, nil)
	if err != nil {
		return err
	}
	if shared {
		return fmt.Errorf("volume %q is now in use; service retained for recovery", pending.Name)
	}
	_, err = s.dockerOutput(ctx, "volume", "rm", pending.Name)
	if err != nil {
		return fmt.Errorf("remove volume %q (service retained for retry): %w", pending.Name, err)
	}
	return nil
}

func (s *DockerComposeService) volumeRemovalPath() (string, error) {
	if s.RunDir == "" {
		return "", fmt.Errorf("managed run directory required for volume removal")
	}
	return filepath.Join(s.RunDir, dockerVolumeRemovalFile), nil
}

func (s *DockerComposeService) loadVolumeRemovalPlan() (dockerVolumeRemovalPlan, error) {
	path, err := s.volumeRemovalPath()
	if err != nil {
		return dockerVolumeRemovalPlan{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return dockerVolumeRemovalPlan{}, err
	}
	return parseVolumeRemovalPlan(data, s.composeProjectName())
}

func parseVolumeRemovalPlan(data []byte, project string) (dockerVolumeRemovalPlan, error) {
	var plan dockerVolumeRemovalPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return plan, fmt.Errorf("decode volume removal plan: %w", err)
	}
	if plan.Project != project {
		return plan, fmt.Errorf("volume removal plan belongs to a different project")
	}
	for _, volume := range plan.Volumes {
		if !validRemovalVolumeName(volume.Name) || volume.CreatedAt == "" {
			return plan, fmt.Errorf("invalid volume removal plan entry")
		}
	}
	return plan, nil
}

func (s *DockerComposeService) saveVolumeRemovalPlan(plan dockerVolumeRemovalPlan) error {
	path, err := s.volumeRemovalPath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.RunDir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.RunDir, ".volume-removal-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	writeErr := writeVolumeRemovalPlan(file, data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(s.RunDir)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func writeVolumeRemovalPlan(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}
