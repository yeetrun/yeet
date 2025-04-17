// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"tailscale.com/types/lazy"
)

const dockerContainerNamePrefix = "catch"

type DockerComposeStatus map[string]Status

var ErrDockerStatusUnknown = fmt.Errorf("unknown docker status")

var ErrDockerNotFound = fmt.Errorf("docker not found")

type DockerComposeService struct {
	Name                 string
	cfg                  *db.Service
	DataDir              string
	NewCmd               func(name string, arg ...string) *exec.Cmd
	Images               map[db.ImageRepoName]*db.ImageRepo
	InternalRegistryAddr string
	sd                   *SystemdService

	installEnvOnce lazy.SyncValue[error]
}

func do(f ...func() error) error {
	for _, fn := range f {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

// DockerCmd returns the path to the docker binary.
func DockerCmd() (string, error) {
	p, err := exec.LookPath("docker")
	if err != nil {
		return "", ErrDockerNotFound
	}
	return p, nil
}

func (s *DockerComposeService) command(args ...string) (*exec.Cmd, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	nargs := []string{
		"compose",
		"--project-name", s.projectName(s.Name),
		"--project-directory", s.DataDir,
	}
	cf, ok := s.cfg.Artifacts.Gen(db.ArtifactDockerComposeFile, s.cfg.Generation)
	if !ok {
		return nil, fmt.Errorf("compose file not found")
	}
	nargs = append(nargs,
		"--file", cf,
	)
	if cf, ok := s.cfg.Artifacts.Gen(db.ArtifactDockerComposeNetwork, s.cfg.Generation); ok {
		nargs = append(nargs, "--file", cf)
	}

	if err := s.installEnvOnce.Get(func() error {
		if ef, ok := s.cfg.Artifacts.Gen(db.ArtifactEnvFile, s.cfg.Generation); ok {
			return fileutil.CopyFile(ef, filepath.Join(s.DataDir, ".env"))
		}
		os.Remove(filepath.Join(s.DataDir, ".env"))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to copy env file: %v", err)
	}
	args = append(nargs, args...)
	c := s.NewCmd(dockerPath, args...)
	c.Dir = s.DataDir
	return c, nil
}

func (s *DockerComposeService) runCommand(args ...string) error {
	cmd, err := s.command(args...)
	if err != nil {
		return fmt.Errorf("failed to create docker-compose command: %v", err)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run docker command: %v", err)
	}
	return nil
}

func matchingRefs(refs map[db.ImageRepoName]*db.ImageRepo, svcName string, ref db.ImageRef) (matching []string) {
	for rn, ir := range refs {
		if s, _, _ := strings.Cut(string(rn), "/"); s == svcName {
			if _, ok := ir.Refs[ref]; ok {
				matching = append(matching, string(rn))
			}
		}
	}
	return matching
}

// InternalRegistryHost is the domain name for the internal registry.
const InternalRegistryHost = "catchit.dev"

func (s *DockerComposeService) Install() error {
	if err := s.PrePullIfRunning(); err != nil {
		return fmt.Errorf("failed to pre-pull images: %v", err)
	}
	if err := s.Down(); err != nil {
		return fmt.Errorf("failed to stop service: %v", err)
	}
	return s.sd.Install()
}

func (s *DockerComposeService) Up() error {
	s.sd.Start()
	// Ok so this is a bit of a hack. We want to use a nice looking image
	// name catchit.dev/svc/img instead of a weirdo loopback
	// 127.0.0.1:42353/svc/img address or with a random port. So to pull
	// this off we first pull the image from the internal registry with the
	// random address, then retag it with the nice looking address, then
	// remove the image with the random address. This is all a bit of a hack
	// but it works for now. We likely want to replace docker with
	// containerd but we need to figure out how to get the same compose
	// functionality with containerd.
	isInternal, err := s.pullInternalImages()
	if err != nil {
		return err
	}
	return s.runCommand("up", "--pull", pullMode(isInternal), "-d")
}

// Pull pulls the docker images used by this compose service without restarting it.
func (s *DockerComposeService) Pull() error {
	isInternal, err := s.pullInternalImages()
	if err != nil {
		return err
	}
	return s.composePull(isInternal)
}

// Update pulls images (prefetching if running) and recreates containers.
func (s *DockerComposeService) Update() error {
	running, err := s.AnyRunning()
	if err != nil {
		return err
	}
	isInternal, err := s.hasInternalImages()
	if err != nil {
		return err
	}
	if running {
		if err := s.composePull(isInternal); err != nil {
			return err
		}
	}
	if _, err := s.pullInternalImages(); err != nil {
		return err
	}
	return s.runCommand("up", "--pull", pullMode(isInternal), "-d")
}

func (s *DockerComposeService) Remove() error {
	if err := s.Down(); err != nil {
		return fmt.Errorf("failed to stop service: %v", err)
	}
	s.sd.Stop()
	return s.sd.Uninstall()
}

func (s *DockerComposeService) Down() error {
	if ok, err := s.Exists(); err != nil {
		return fmt.Errorf("failed to check if service exists: %v", err)
	} else if !ok {
		return nil
	}
	return s.runCommand("down", "--remove-orphans")
}

func (s *DockerComposeService) Start() error {
	s.sd.Start()
	return s.runCommand("start")
}

func (s *DockerComposeService) Stop() error {
	if ok, err := s.Exists(); err != nil {
		return fmt.Errorf("failed to check if service exists: %v", err)
	} else if !ok {
		return nil
	}
	s.sd.Stop()
	return s.runCommand("stop")
}

func (s *DockerComposeService) Restart() error {
	if ok, err := s.Exists(); err != nil {
		return fmt.Errorf("failed to check if service exists: %v", err)
	} else if !ok {
		return nil
	}
	return s.runCommand("restart")
}

// PrePullIfRunning pulls images while containers are still running to reduce downtime.
func (s *DockerComposeService) PrePullIfRunning() error {
	running, err := s.AnyRunning()
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	return s.Pull()
}

// AnyRunning returns true if any compose container is currently running.
func (s *DockerComposeService) AnyRunning() (bool, error) {
	statuses, err := s.Statuses()
	if err != nil {
		if err == ErrDockerStatusUnknown {
			return false, nil
		}
		return false, err
	}
	for _, status := range statuses {
		if status == StatusRunning {
			return true, nil
		}
	}
	return false, nil
}

func (s *DockerComposeService) Exists() (bool, error) {
	statuses, err := s.Statuses()
	if err != nil {
		if err == ErrDockerStatusUnknown {
			return false, nil
		}
		return false, err
	}
	return len(statuses) > 0, nil
}

func (s *DockerComposeService) Status() (Status, error) {
	return StatusUnknown, fmt.Errorf("not implemented")
}

func (s *DockerComposeService) Statuses() (DockerComposeStatus, error) {
	cmd, err := s.command("ps", "-a",
		"--format", `{{.Label "com.docker.compose.service"}},{{.State}}`)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker-compose command: %v", err)
	}
	cmd.Stdout = nil
	ob, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run docker command: %v (%s)", err, ob)
	}

	output := string(ob)
	if strings.TrimSpace(output) == "" {
		return nil, ErrDockerStatusUnknown
	}

	statuses := make(DockerComposeStatus)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 2 {
			log.Printf("unexpected docker-compose ps output: %s", line)
			continue
		}
		cn := fields[0]
		switch fields[1] {
		case "running":
			statuses[cn] = StatusRunning
		case "restarting":
			statuses[cn] = StatusRunning
		case "exited":
			statuses[cn] = StatusStopped
		case "created", "paused", "dead", "removing":
			statuses[cn] = StatusStopped
		default:
			statuses[cn] = StatusUnknown
		}
	}
	return statuses, nil
}

func (s *DockerComposeService) Logs(opts *LogOptions) error {
	if opts == nil {
		opts = &LogOptions{}
	}
	args := []string{"logs"}
	if opts.Follow {
		args = append(args, "--follow")
	}
	if opts.Lines > 0 {
		args = append(args, "--tail", strconv.Itoa(opts.Lines))
	}
	return s.runCommand(args...)
}

func (s *DockerComposeService) hasInternalImages() (bool, error) {
	usesInternal, err := s.composeUsesInternalImages()
	if err != nil {
		return false, err
	}
	if !usesInternal {
		return false, nil
	}
	return len(matchingRefs(s.Images, s.Name, "latest")) > 0, nil
}

func (s *DockerComposeService) pullInternalImages() (bool, error) {
	usesInternal, err := s.composeUsesInternalImages()
	if err != nil {
		return false, err
	}
	if !usesInternal {
		return false, nil
	}
	isInternal := false
	for _, ref := range matchingRefs(s.Images, s.Name, "latest") {
		isInternal = true
		internalRef := fmt.Sprintf("%s/%s:latest", s.InternalRegistryAddr, ref)
		canonicalRef := fmt.Sprintf("%s/%s:latest", InternalRegistryHost, ref)
		if err := do(
			s.NewCmd("docker", "pull", internalRef).Run,
			s.NewCmd("docker", "tag", internalRef, canonicalRef).Run,
			s.NewCmd("docker", "rmi", internalRef).Run,
		); err != nil {
			log.Printf("docker tag: %v", err)
			return false, fmt.Errorf("failed to tag image: %v", err)
		}
	}
	return isInternal, nil
}

func (s *DockerComposeService) composeUsesInternalImages() (bool, error) {
	cf, ok := s.cfg.Artifacts.Gen(db.ArtifactDockerComposeFile, s.cfg.Generation)
	if !ok {
		return false, fmt.Errorf("compose file not found")
	}
	content, err := os.ReadFile(cf)
	if err != nil {
		return false, fmt.Errorf("failed to read compose file: %w", err)
	}
	needle := []byte(InternalRegistryHost + "/")
	return bytes.Contains(content, needle), nil
}

func (s *DockerComposeService) composePull(isInternal bool) error {
	args := []string{"pull"}
	if isInternal {
		args = append(args, "--ignore-pull-failures")
	}
	return s.runCommand(args...)
}

func pullMode(isInternal bool) string {
	if isInternal {
		// Skip pulling from catchit.dev since it's a virtual registry that doesn't actually exist
		return "never"
	}
	return "always"
}

// projectName returns the docker-compose project name for the given service name.
func (s *DockerComposeService) projectName(sn string) string {
	return fmt.Sprintf("%s-%s", dockerContainerNamePrefix, sn)
}
