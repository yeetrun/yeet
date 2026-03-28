// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type composeContainer struct {
	ID       string
	PID      int
	Networks []string
	NetNSID  string
}

type netnsInspector interface {
	NamedNetNSID(path string) (string, error)
	ProjectContainers(project string) ([]composeContainer, error)
}

type linuxNetNSInspector struct{}

func (linuxNetNSInspector) NamedNetNSID(path string) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", fmt.Errorf("read netns %q: %w", path, err)
	}
	return target, nil
}

func (linuxNetNSInspector) ProjectContainers(project string) ([]composeContainer, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}

	psCmd := exec.Command(dockerPath, "compose", "--project-name", project, "ps", "-q")
	output, err := psCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose ps -q for %q: %w", project, err)
	}

	containerIDs := splitNonEmptyLines(string(output))
	if len(containerIDs) == 0 {
		return nil, nil
	}

	args := []string{
		"inspect",
		"--format",
		`{{.Id}}{{"\t"}}{{.State.Pid}}{{"\t"}}{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}`,
	}
	args = append(args, containerIDs...)
	inspectCmd := exec.Command(dockerPath, args...)
	inspectOutput, err := inspectCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect for %q: %w", project, err)
	}

	lines := splitNonEmptyLines(string(inspectOutput))
	containers := make([]composeContainer, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected docker inspect output: %q", line)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse container pid %q: %w", fields[1], err)
		}
		netnsID, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "ns/net"))
		if err != nil {
			return nil, fmt.Errorf("read container netns for %q: %w", fields[0], err)
		}
		containers = append(containers, composeContainer{
			ID:       fields[0],
			PID:      pid,
			Networks: strings.Fields(fields[2]),
			NetNSID:  netnsID,
		})
	}
	return containers, nil
}

func selectNetNSContainers(containers []composeContainer, network string) []composeContainer {
	selected := make([]composeContainer, 0, len(containers))
	for _, container := range containers {
		for _, attached := range container.Networks {
			if attached == network {
				selected = append(selected, container)
				break
			}
		}
	}
	return selected
}

func needsNetNSRestart(namedID string, containers []composeContainer) bool {
	if len(containers) == 0 {
		return false
	}
	for _, container := range containers {
		if container.NetNSID != namedID {
			return true
		}
	}
	return false
}

func splitNonEmptyLines(output string) []string {
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
