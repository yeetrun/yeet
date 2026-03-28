// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"fmt"
	"os/exec"
	"strings"
)

type composeContainer struct {
	ID                 string
	NetworkEndpointIDs map[string]string
}

type netnsInspector interface {
	NamedNetNSLinkNames(path string) ([]string, error)
	ProjectContainers(project string) ([]composeContainer, error)
}

type linuxNetNSInspector struct{}

var (
	netnsExecCmd = exec.Command
)

func (linuxNetNSInspector) NamedNetNSLinkNames(path string) ([]string, error) {
	cmd := netnsExecCmd("nsenter", "--net="+path, "ip", "-o", "link", "show")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list netns links for %q: %w", path, err)
	}
	lines := splitNonEmptyLines(string(output))
	links := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) < 2 {
			return nil, fmt.Errorf("unexpected ip link output: %q", line)
		}
		name := strings.TrimSpace(fields[1])
		if idx := strings.IndexByte(name, '@'); idx >= 0 {
			name = name[:idx]
		}
		links = append(links, name)
	}
	return links, nil
}

func (linuxNetNSInspector) ProjectContainers(project string) ([]composeContainer, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}

	psCmd := netnsExecCmd(dockerPath, "compose", "--project-name", project, "ps", "-q")
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
		`{{.Id}}{{"\t"}}{{range $name, $network := .NetworkSettings.Networks}}{{printf "%s=%s " $name $network.EndpointID}}{{end}}`,
	}
	args = append(args, containerIDs...)
	inspectCmd := netnsExecCmd(dockerPath, args...)
	inspectOutput, err := inspectCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect for %q: %w", project, err)
	}

	lines := splitNonEmptyRawLines(string(inspectOutput))
	containers := make([]composeContainer, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected docker inspect output: %q", line)
		}
		networks := map[string]string{}
		for _, network := range strings.Fields(fields[1]) {
			name, endpointID, ok := strings.Cut(network, "=")
			if !ok {
				return nil, fmt.Errorf("unexpected docker network entry: %q", network)
			}
			networks[name] = endpointID
		}
		containers = append(containers, composeContainer{
			ID:                 fields[0],
			NetworkEndpointIDs: networks,
		})
	}
	return containers, nil
}

func selectNetNSContainers(containers []composeContainer, network string) []composeContainer {
	selected := make([]composeContainer, 0, len(containers))
	for _, container := range containers {
		if _, ok := container.NetworkEndpointIDs[network]; ok {
			selected = append(selected, container)
		}
	}
	return selected
}

func needsNetNSRecreate(linkNames []string, containers []composeContainer, network string) bool {
	if len(containers) == 0 {
		return false
	}
	links := map[string]struct{}{}
	for _, linkName := range linkNames {
		links[linkName] = struct{}{}
	}
	for _, container := range containers {
		endpointID := container.NetworkEndpointIDs[network]
		if len(endpointID) < 4 {
			return true
		}
		if _, ok := links["yv-"+endpointID[:4]]; !ok {
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

func splitNonEmptyRawLines(output string) []string {
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
