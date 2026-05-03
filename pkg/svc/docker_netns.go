// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type composeContainer struct {
	ID                 string
	NetworkEndpointIDs map[string]string
}

type netnsInspector interface {
	NamedNetNSLinkNames(ctx context.Context, path string) ([]string, error)
	ProjectContainers(ctx context.Context, project string) ([]composeContainer, error)
}

type linuxNetNSInspector struct{}

func (linuxNetNSInspector) NamedNetNSLinkNames(ctx context.Context, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "nsenter", "--net="+path, "ip", "-o", "link", "show")
	output, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
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

func (linuxNetNSInspector) ProjectContainers(ctx context.Context, project string) ([]composeContainer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}

	containerIDs, err := projectContainerIDs(ctx, dockerPath, project)
	if err != nil {
		return nil, err
	}
	if len(containerIDs) == 0 {
		return nil, nil
	}

	return inspectProjectContainers(ctx, dockerPath, project, containerIDs)
}

func projectContainerIDs(ctx context.Context, dockerPath, project string) ([]string, error) {
	psCmd := exec.CommandContext(ctx, dockerPath, "compose", "--project-name", project, "ps", "-q")
	output, err := psCmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("docker compose ps -q for %q: %w", project, err)
	}
	return splitNonEmptyLines(string(output)), nil
}

func inspectProjectContainers(ctx context.Context, dockerPath, project string, containerIDs []string) ([]composeContainer, error) {
	inspectCmd := exec.CommandContext(ctx, dockerPath, composeContainerInspectArgs(containerIDs)...)
	inspectOutput, err := inspectCmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("docker inspect for %q: %w", project, err)
	}
	return parseComposeContainerInspectOutput(string(inspectOutput))
}

func composeContainerInspectArgs(containerIDs []string) []string {
	args := []string{
		"inspect",
		"--format",
		`{{.Id}}{{"\t"}}{{range $name, $network := .NetworkSettings.Networks}}{{printf "%s=%s " $name $network.EndpointID}}{{end}}`,
	}
	return append(args, containerIDs...)
}

func parseComposeContainerInspectOutput(output string) ([]composeContainer, error) {
	lines := splitNonEmptyRawLines(output)
	containers := make([]composeContainer, 0, len(lines))
	for _, line := range lines {
		container, err := parseComposeContainerInspectLine(line)
		if err != nil {
			return nil, err
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func parseComposeContainerInspectLine(line string) (composeContainer, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != 2 {
		return composeContainer{}, fmt.Errorf("unexpected docker inspect output: %q", line)
	}
	networks, err := parseComposeNetworkEndpointIDs(fields[1])
	if err != nil {
		return composeContainer{}, err
	}
	return composeContainer{
		ID:                 fields[0],
		NetworkEndpointIDs: networks,
	}, nil
}

func parseComposeNetworkEndpointIDs(output string) (map[string]string, error) {
	networks := map[string]string{}
	for _, network := range strings.Fields(output) {
		name, endpointID, ok := strings.Cut(network, "=")
		if !ok {
			return nil, fmt.Errorf("unexpected docker network entry: %q", network)
		}
		networks[name] = endpointID
	}
	return networks, nil
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
