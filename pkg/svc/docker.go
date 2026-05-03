// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"bytes"
	"context"
	"errors"
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
	Name          string
	cfg           *db.Service
	DataDir       string
	NewCmd        func(name string, arg ...string) *exec.Cmd
	NewCmdContext func(ctx context.Context, name string, arg ...string) *exec.Cmd
	sd            dockerSystemdService

	netnsInspector netnsInspector
	installEnvOnce lazy.SyncValue[error]
}

type dockerSystemdService interface {
	Install() error
	Stop() error
	Uninstall() error
	StartAuxiliaryUnits() error
	hasArtifact(db.ArtifactName) bool
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
	return s.commandContext(context.Background(), args...)
}

func (s *DockerComposeService) commandContext(ctx context.Context, args ...string) (*exec.Cmd, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}

	nargs, err := s.composeCommandArgs()
	if err != nil {
		return nil, err
	}

	if err := s.installEnvOnce.Get(s.syncComposeEnvFile); err != nil {
		return nil, fmt.Errorf("failed to copy env file: %v", err)
	}

	args = append(nargs, args...)
	c := s.newDockerCommand(ctx, dockerPath, args...)
	c.Dir = s.DataDir
	return c, nil
}

func (s *DockerComposeService) composeCommandArgs() ([]string, error) {
	args := []string{
		"compose",
		"--project-name", s.projectName(s.Name),
		"--project-directory", s.DataDir,
	}
	cf, ok := s.cfg.Artifacts.Gen(db.ArtifactDockerComposeFile, s.cfg.Generation)
	if !ok {
		return nil, fmt.Errorf("compose file not found")
	}
	args = append(args,
		"--file", cf,
	)
	if cf, ok := s.cfg.Artifacts.Gen(db.ArtifactDockerComposeNetwork, s.cfg.Generation); ok {
		args = append(args, "--file", cf)
	}
	return args, nil
}

func (s *DockerComposeService) syncComposeEnvFile() error {
	envPath := filepath.Join(s.DataDir, ".env")
	if ef, ok := s.cfg.Artifacts.Gen(db.ArtifactEnvFile, s.cfg.Generation); ok {
		return fileutil.CopyFile(ef, envPath)
	}
	return removeStaleComposeEnvFile(envPath)
}

func removeStaleComposeEnvFile(path string) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Printf("failed to remove stale docker compose env file %s: %v", path, err)
	}
	return nil
}

func (s *DockerComposeService) newDockerCommand(ctx context.Context, dockerPath string, args ...string) *exec.Cmd {
	switch {
	case s.NewCmdContext != nil:
		return s.NewCmdContext(ctx, dockerPath, args...)
	case s.NewCmd != nil:
		return s.NewCmd(dockerPath, args...)
	default:
		return exec.CommandContext(ctx, dockerPath, args...)
	}
}

func (s *DockerComposeService) runCommand(args ...string) error {
	return s.runCommandContext(context.Background(), args...)
}

func (s *DockerComposeService) runCommandContext(ctx context.Context, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd, err := s.commandContext(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to create docker-compose command: %v", err)
	}
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("failed to run docker command: %v", err)
	}
	return nil
}

// InternalRegistryHost is the domain name for the internal registry.
const InternalRegistryHost = "catchit.dev"

func (s *DockerComposeService) Install() error {
	return s.InstallWithPull(true)
}

func (s *DockerComposeService) InstallWithPull(pull bool) error {
	if pull {
		if err := s.PrePullIfRunning(); err != nil {
			return fmt.Errorf("failed to pre-pull images: %v", err)
		}
	}
	if err := s.Down(); err != nil {
		return fmt.Errorf("failed to stop service: %v", err)
	}
	return s.sd.Install()
}

func (s *DockerComposeService) Up() error {
	return s.UpWithPull(true)
}

func (s *DockerComposeService) UpWithPull(pull bool) error {
	if s.sd != nil {
		if err := s.sd.StartAuxiliaryUnits(); err != nil {
			return err
		}
		if _, err := s.ReconcileNetNS(context.Background()); err != nil {
			return err
		}
	}
	isInternal, err := s.composeUsesInternalImages()
	if err != nil {
		return err
	}
	args := []string{"up"}
	if pull {
		args = append(args, "--pull", pullMode(isInternal))
	}
	args = append(args, "-d")
	return s.runCommand(args...)
}

// Pull pulls the docker images used by this compose service without restarting it.
func (s *DockerComposeService) Pull() error {
	isInternal, err := s.composeUsesInternalImages()
	if err != nil {
		return err
	}
	if isInternal {
		return nil
	}
	return s.composePull(false)
}

// Update pulls images (prefetching if running) and recreates containers.
func (s *DockerComposeService) Update() error {
	running, err := s.AnyRunning()
	if err != nil {
		return err
	}
	isInternal, err := s.composeUsesInternalImages()
	if err != nil {
		return err
	}
	if running && !isInternal {
		if err := s.composePull(false); err != nil {
			return err
		}
	}
	return s.runCommand("up", "--pull", pullMode(isInternal), "-d")
}

func (s *DockerComposeService) Remove() error {
	if err := s.Down(); err != nil {
		return fmt.Errorf("failed to stop service: %v", err)
	}
	stopErr := s.stopSystemdService()
	if s.sd == nil {
		return stopErr
	}
	return joinErrors(stopErr, s.sd.Uninstall())
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
	if s.sd != nil {
		if err := s.sd.StartAuxiliaryUnits(); err != nil {
			return err
		}
		if _, err := s.ReconcileNetNS(context.Background()); err != nil {
			return err
		}
	}
	return s.runCommand("start")
}

func (s *DockerComposeService) Stop() error {
	if ok, err := s.Exists(); err != nil {
		return fmt.Errorf("failed to check if service exists: %v", err)
	} else if !ok {
		return nil
	}
	stopErr := s.stopSystemdService()
	return joinErrors(s.runCommand("stop"), stopErr)
}

func (s *DockerComposeService) stopSystemdService() error {
	if s.sd == nil {
		return nil
	}
	if err := s.sd.Stop(); err != nil {
		return fmt.Errorf("failed to stop systemd service: %w", err)
	}
	return nil
}

func joinErrors(errs ...error) error {
	nonNil := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			nonNil = append(nonNil, err)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	if len(nonNil) == 1 {
		return nonNil[0]
	}
	return errors.Join(nonNil...)
}

func (s *DockerComposeService) Restart() error {
	if ok, err := s.Exists(); err != nil {
		return fmt.Errorf("failed to check if service exists: %v", err)
	} else if !ok {
		return nil
	}
	if s.sd != nil {
		if err := s.sd.StartAuxiliaryUnits(); err != nil {
			return err
		}
		restarted, err := s.ReconcileNetNS(context.Background())
		if err != nil {
			return err
		}
		if restarted {
			return nil
		}
	}
	return s.runCommand("restart")
}

func (s *DockerComposeService) ReconcileNetNS(ctx context.Context) (bool, error) {
	if s.sd == nil || !s.sd.hasArtifact(db.ArtifactNetNSService) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	containers, err := s.getNetNSInspector().ProjectContainers(ctx, s.composeProjectName())
	if err != nil {
		return false, err
	}
	selected := selectNetNSContainers(containers, s.defaultNetworkName())
	if len(selected) == 0 {
		return false, nil
	}
	linkNames, err := s.getNetNSInspector().NamedNetNSLinkNames(ctx, s.namedNetNSPath())
	if err != nil {
		return false, err
	}
	if !needsNetNSRecreate(linkNames, selected, s.defaultNetworkName()) {
		return false, nil
	}
	return true, s.runCommandContext(ctx, "up", "-d", "--force-recreate")
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

	return parseDockerComposeStatuses(string(ob))
}

func parseDockerComposeStatuses(output string) (DockerComposeStatus, error) {
	if strings.TrimSpace(output) == "" {
		return nil, ErrDockerStatusUnknown
	}

	statuses := make(DockerComposeStatus)
	for _, line := range splitNonEmptyLines(output) {
		cn, status, ok := parseDockerComposeStatusLine(line)
		if !ok {
			log.Printf("unexpected docker-compose ps output: %s", line)
			continue
		}
		statuses[cn] = status
	}
	return statuses, nil
}

func parseDockerComposeStatusLine(line string) (string, Status, bool) {
	fields := strings.Split(line, ",")
	if len(fields) != 2 {
		return "", StatusUnknown, false
	}
	return fields[0], dockerComposeStateStatus(fields[1]), true
}

func dockerComposeStateStatus(state string) Status {
	switch state {
	case "running", "restarting":
		return StatusRunning
	case "exited", "created", "paused", "dead", "removing":
		return StatusStopped
	default:
		return StatusUnknown
	}
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

func (s *DockerComposeService) composeProjectName() string {
	return s.projectName(s.Name)
}

func (s *DockerComposeService) defaultNetworkName() string {
	return s.composeProjectName() + "_default"
}

func (s *DockerComposeService) namedNetNSPath() string {
	return filepath.Join("/var/run/netns", "yeet-"+s.Name+"-ns")
}

func (s *DockerComposeService) getNetNSInspector() netnsInspector {
	if s.netnsInspector != nil {
		return s.netnsInspector
	}
	return linuxNetNSInspector{}
}
