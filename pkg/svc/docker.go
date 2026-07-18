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
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"tailscale.com/types/lazy"
)

const dockerContainerNamePrefix = "catch"

type DockerComposeStatus map[string]Status

const dockerProjectLabelStopMax = 15 * time.Second

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

// ResolveConfigJSON resolves the exact Compose files used by this service
// generation into Docker Compose's canonical JSON application model.
func (s *DockerComposeService) ResolveConfigJSON(ctx context.Context) ([]byte, error) {
	args, err := s.composeCommandArgs()
	if err != nil {
		return nil, err
	}
	return ResolveComposeJSON(ctx, ComposeResolveOptions{
		ProjectName: s.composeProjectName(),
		ProjectDir:  s.DataDir,
		Files:       composeFilesFromArgs(args),
		NewCmd:      s.NewCmdContext,
	})
}

func composeFilesFromArgs(args []string) []string {
	var files []string
	for idx := 0; idx+1 < len(args); idx++ {
		if args[idx] != "--file" {
			continue
		}
		files = append(files, args[idx+1])
		idx++
	}
	return files
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

// InstallDefinition installs only the generated auxiliary systemd units. ISO
// lifecycle orchestration calls this after policy/topology verification and
// keeps container creation and execution in later explicit phases.
func (s *DockerComposeService) InstallDefinition() error {
	if s.sd == nil {
		return nil
	}
	return s.sd.Install()
}

// Create materializes the admitted Compose project without starting workload
// processes. This is the ISO network-attachment boundary.
func (s *DockerComposeService) Create(ctx context.Context) error {
	return s.runCommandContext(ctx, "create")
}

// StartAuxiliaryUnits starts only the generated namespace/Tailscale units.
func (s *DockerComposeService) StartAuxiliaryUnits() error {
	if s.sd == nil {
		return nil
	}
	return s.sd.StartAuxiliaryUnits()
}

func (s *DockerComposeService) StopAuxiliaryUnits() error {
	if s.sd == nil {
		return nil
	}
	return s.sd.Stop()
}

// UpDetached starts the previously admitted project without implicitly
// starting auxiliary units or reconciling another network boundary.
func (s *DockerComposeService) UpDetached(ctx context.Context, pull bool) error {
	args := []string{"up"}
	if pull {
		isInternal, err := s.composeUsesInternalImages()
		if err != nil {
			return err
		}
		args = append(args, "--pull", pullMode(isInternal))
	}
	args = append(args, "-d")
	return s.runCommandContext(ctx, args...)
}

// DownRemoveOrphans is the bounded cleanup primitive used after ISO runtime
// inspection fails. Compose down is idempotent when the project is absent.
func (s *DockerComposeService) DownRemoveOrphans(ctx context.Context) error {
	return s.runCommandContext(ctx, "down", "--remove-orphans")
}

// StopProjectContainers removes every container carrying this exact Compose
// project label. The bounded label-only stop runs first and does not depend on
// readable generation artifacts; Compose cleanup runs second for networks and
// orphans. This order keeps quarantine fail closed even when Compose input is
// missing, malformed, or slow to process.
func (s *DockerComposeService) StopProjectContainers(ctx context.Context) error {
	labelCtx, labelCancel := dockerProjectLabelStopContext(ctx)
	labelErr := s.stopProjectContainersByLabel(labelCtx)
	labelCancel()
	downErr := s.DownRemoveOrphans(ctx)
	if labelErr == nil || downErr == nil {
		return nil
	}
	return errors.Join(labelErr, downErr)
}

func dockerProjectLabelStopContext(parent context.Context) (context.Context, context.CancelFunc) {
	budget := dockerProjectLabelStopMax
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(parent)
		}
		if half := remaining / 2; half < budget {
			budget = half
		}
	}
	return context.WithTimeout(parent, budget)
}

func (s *DockerComposeService) stopProjectContainersByLabel(ctx context.Context) error {
	ids, err := s.projectContainerIDs(ctx)
	if err != nil || len(ids) == 0 {
		return err
	}
	dockerPath, err := DockerCmd()
	if err != nil {
		return err
	}
	args := append([]string{"rm", "--force"}, ids...)
	output, err := s.newDockerCommand(ctx, dockerPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("force-remove Compose project %q containers: %w: %s", s.composeProjectName(), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *DockerComposeService) projectContainerIDs(ctx context.Context) ([]string, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	filter := "label=com.docker.compose.project=" + s.composeProjectName()
	cmd := s.newDockerCommand(ctx, dockerPath, "ps", "-aq", "--filter", filter)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list Compose project %q containers by label: %w: %s", s.composeProjectName(), err, strings.TrimSpace(stderr.String()))
	}
	ids := strings.Fields(string(output))
	for _, id := range ids {
		if !validDockerContainerID(id) {
			return nil, fmt.Errorf("list Compose project %q containers returned invalid ID %q", s.composeProjectName(), id)
		}
	}
	return ids, nil
}

func validDockerContainerID(id string) bool {
	if len(id) < 12 || len(id) > 64 {
		return false
	}
	for _, char := range id {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func (s *DockerComposeService) VerifyProjectAbsent(ctx context.Context) error {
	ids, err := s.projectContainerIDs(ctx)
	if err != nil {
		return err
	}
	if len(ids) != 0 {
		return fmt.Errorf("compose project %q still has containers %v", s.composeProjectName(), ids)
	}
	return nil
}

func (s *DockerComposeService) VerifyDefaultNetworkAbsent(ctx context.Context) error {
	dockerPath, err := DockerCmd()
	if err != nil {
		return err
	}
	cmd := s.newDockerCommand(ctx, dockerPath, "network", "ls", "--filter", "name=^"+s.defaultNetworkName()+"$", "--format", "{{.Name}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect Docker network absence: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("docker network %q still exists", s.defaultNetworkName())
	}
	return nil
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
	return s.PullContext(context.Background())
}

func (s *DockerComposeService) PullContext(ctx context.Context) error {
	isInternal, err := s.composeUsesInternalImages()
	if err != nil {
		return err
	}
	if isInternal {
		return nil
	}
	return s.composePullContext(ctx, false)
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
	return s.UpWithPull(false)
}

func (s *DockerComposeService) Stop() error {
	exists, existsErr := s.Exists()
	stopErr := s.stopSystemdService()
	if existsErr != nil {
		return joinErrors(fmt.Errorf("failed to check if service exists: %v", existsErr), stopErr)
	}
	if !exists {
		return stopErr
	}
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
		return s.Start()
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
	return s.AnyRunningContext(context.Background())
}

func (s *DockerComposeService) AnyRunningContext(ctx context.Context) (bool, error) {
	statuses, err := s.StatusesContext(ctx)
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
	return s.StatusesContext(context.Background())
}

func (s *DockerComposeService) StatusesContext(ctx context.Context) (DockerComposeStatus, error) {
	cmd, err := s.commandContext(ctx, "ps", "-a",
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

func DockerComposeStateStatus(state string) Status {
	return dockerComposeStateStatus(state)
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
	return s.composePullContext(context.Background(), isInternal)
}

func (s *DockerComposeService) composePullContext(ctx context.Context, isInternal bool) error {
	args := []string{"pull"}
	if isInternal {
		args = append(args, "--ignore-pull-failures")
	}
	return s.runCommandContext(ctx, args...)
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
	return ComposeProjectName(sn)
}

// ComposeProjectName returns the Docker Compose project identity used for a
// Yeet service by both installation admission and runtime operations.
func ComposeProjectName(service string) string {
	return dockerContainerNamePrefix + "-" + service
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
