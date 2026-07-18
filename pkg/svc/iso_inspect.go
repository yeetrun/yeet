// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type isoInspectRunner func(context.Context, string, ...string) ([]byte, error)

// ISOInspectOptions is the admitted runtime shape that Docker must match.
type ISOInspectOptions struct {
	ProjectName  string
	ProjectDir   string
	ComposeFiles []string
	NetworkName  string
	ServiceRoot  string
	Components   map[string]netip.Addr
	NewCmd       func(context.Context, string, ...string) *exec.Cmd

	run isoInspectRunner
}

// ISOInspection records the observed ISO Compose runtime and every drift
// finding. Call Verify before treating the workload as ready.
type ISOInspection struct {
	Containers []string
	Addresses  map[string]netip.Addr
	Findings   []string
}

func (i ISOInspection) Verify() error {
	if len(i.Findings) == 0 {
		return nil
	}
	return fmt.Errorf("ISO runtime differs from admitted model: %s", strings.Join(i.Findings, "; "))
}

type isoComposePSContainer = dockerComposePSRow

type isoDockerInspectHostConfig struct {
	NetworkMode       string                     `json:"NetworkMode"`
	PidMode           string                     `json:"PidMode"`
	IpcMode           string                     `json:"IpcMode"`
	CgroupnsMode      string                     `json:"CgroupnsMode"`
	UTSMode           string                     `json:"UTSMode"`
	UsernsMode        string                     `json:"UsernsMode"`
	Privileged        bool                       `json:"Privileged"`
	CapAdd            []string                   `json:"CapAdd"`
	Devices           []json.RawMessage          `json:"Devices"`
	DeviceCgroupRules []string                   `json:"DeviceCgroupRules"`
	DeviceRequests    []json.RawMessage          `json:"DeviceRequests"`
	PortBindings      map[string]json.RawMessage `json:"PortBindings"`
	SecurityOpt       []string                   `json:"SecurityOpt"`
}

type isoDockerInspectEndpoint struct {
	IPAddress         string `json:"IPAddress"`
	GlobalIPv6Address string `json:"GlobalIPv6Address"`
}

type isoDockerInspectContainer struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig      isoDockerInspectHostConfig `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]isoDockerInspectEndpoint `json:"Networks"`
		Ports    map[string]json.RawMessage          `json:"Ports"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

// InspectISOProject compares the actual Docker Compose runtime with the
// admitted component, address, network, privilege, and mount boundary.
func InspectISOProject(ctx context.Context, opts ISOInspectOptions) (ISOInspection, error) {
	inspection := ISOInspection{Addresses: map[string]netip.Addr{}}
	if err := validateISOInspectOptions(opts); err != nil {
		return inspection, err
	}
	run := opts.run
	if run == nil {
		run = defaultISOInspectRunner(opts)
	}
	psRaw, err := run(ctx, "compose-ps")
	if err != nil {
		return inspection, fmt.Errorf("inspect ISO Compose project containers: %w", err)
	}
	ps, err := parseComposePSJSON(psRaw)
	if err != nil {
		return inspection, fmt.Errorf("decode ISO Compose project containers: %w", err)
	}
	ids := inspectISOComposePS(&inspection, opts, ps)
	if len(ids) == 0 {
		return inspection, nil
	}
	inspectRaw, err := run(ctx, "inspect", ids...)
	if err != nil {
		return inspection, fmt.Errorf("inspect ISO Docker containers: %w", err)
	}
	var containers []isoDockerInspectContainer
	if err := json.Unmarshal(inspectRaw, &containers); err != nil {
		return inspection, fmt.Errorf("decode ISO Docker containers: %w", err)
	}
	if err := validateISODockerInspectEvidence(inspectRaw); err != nil {
		return inspection, fmt.Errorf("validate ISO Docker inspection evidence: %w", err)
	}
	inspectISODockerContainers(&inspection, opts, ps, containers)
	return inspection, nil
}

func validateISODockerInspectEvidence(raw []byte) error {
	var containers []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &containers); err != nil {
		return err
	}
	for idx, container := range containers {
		if err := validateISOContainerInspectEvidence(container, fmt.Sprintf("containers[%d]", idx)); err != nil {
			return err
		}
	}
	return nil
}

func validateISOContainerInspectEvidence(container map[string]json.RawMessage, path string) error {
	if _, err := requiredISOInspectJSONField(container, path, "Id", false); err != nil {
		return err
	}
	if err := validateISOInspectNestedFields(container, path, "State", []string{"Running"}, false); err != nil {
		return err
	}
	if err := validateISOInspectNestedFields(container, path, "Config", []string{"Labels"}, false); err != nil {
		return err
	}
	if err := validateISOInspectHostEvidence(container, path); err != nil {
		return err
	}
	if err := validateISOInspectNetworkEvidence(container, path); err != nil {
		return err
	}
	return validateISOInspectMountEvidence(container, path)
}

func validateISOInspectNestedFields(object map[string]json.RawMessage, path, name string, fields []string, allowNull bool) error {
	nested, err := requiredISOInspectJSONObject(object, path, name)
	if err != nil {
		return err
	}
	for _, field := range fields {
		if _, err := requiredISOInspectJSONField(nested, path+"."+name, field, allowNull); err != nil {
			return err
		}
	}
	return nil
}

func validateISOInspectHostEvidence(container map[string]json.RawMessage, path string) error {
	host, err := requiredISOInspectJSONObject(container, path, "HostConfig")
	if err != nil {
		return err
	}
	required := []string{"NetworkMode", "PidMode", "IpcMode", "CgroupnsMode", "UTSMode", "UsernsMode", "Privileged", "PortBindings"}
	if err := validateISOInspectFields(host, path+".HostConfig", required, false); err != nil {
		return err
	}
	// Docker represents an empty capability or device list as either [] or
	// null. The key itself remains required evidence from docker inspect.
	nullable := []string{"CapAdd", "Devices", "DeviceCgroupRules", "DeviceRequests", "SecurityOpt"}
	return validateISOInspectFields(host, path+".HostConfig", nullable, true)
}

func validateISOInspectFields(object map[string]json.RawMessage, path string, fields []string, allowNull bool) error {
	for _, field := range fields {
		if _, err := requiredISOInspectJSONField(object, path, field, allowNull); err != nil {
			return err
		}
	}
	return nil
}

func validateISOInspectNetworkEvidence(container map[string]json.RawMessage, path string) error {
	settings, err := requiredISOInspectJSONObject(container, path, "NetworkSettings")
	if err != nil {
		return err
	}
	raw, err := requiredISOInspectJSONField(settings, path+".NetworkSettings", "Networks", false)
	if err != nil {
		return err
	}
	var networks map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &networks); err != nil || networks == nil {
		return fmt.Errorf("%s.NetworkSettings.Networks is not an object", path)
	}
	for name, network := range networks {
		networkPath := path + ".NetworkSettings.Networks." + name
		if err := validateISOInspectFields(network, networkPath, []string{"IPAddress", "GlobalIPv6Address"}, false); err != nil {
			return err
		}
	}
	_, err = requiredISOInspectJSONField(settings, path+".NetworkSettings", "Ports", false)
	return err
}

func validateISOInspectMountEvidence(container map[string]json.RawMessage, path string) error {
	raw, err := requiredISOInspectJSONField(container, path, "Mounts", false)
	if err != nil {
		return err
	}
	var mounts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &mounts); err != nil || mounts == nil {
		return fmt.Errorf("%s.Mounts is not an array", path)
	}
	for idx, mount := range mounts {
		if err := validateISOInspectFields(mount, fmt.Sprintf("%s.Mounts[%d]", path, idx), []string{"Type", "Source", "Destination"}, false); err != nil {
			return err
		}
	}
	return nil
}

func requiredISOInspectJSONObject(object map[string]json.RawMessage, path, field string) (map[string]json.RawMessage, error) {
	raw, err := requiredISOInspectJSONField(object, path, field, false)
	if err != nil {
		return nil, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("%s.%s is not an object", path, field)
	}
	return value, nil
}

func requiredISOInspectJSONField(object map[string]json.RawMessage, path, field string, allowNull bool) (json.RawMessage, error) {
	raw, ok := object[field]
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%s.%s is missing", path, field)
	}
	if !allowNull && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s.%s is null", path, field)
	}
	return raw, nil
}

func validateISOInspectOptions(opts ISOInspectOptions) error {
	if strings.TrimSpace(opts.ProjectName) == "" {
		return fmt.Errorf("ISO inspection requires a Compose project name")
	}
	if strings.TrimSpace(opts.NetworkName) == "" {
		return fmt.Errorf("ISO inspection requires a generated network name")
	}
	if strings.TrimSpace(opts.ServiceRoot) == "" || !filepath.IsAbs(opts.ServiceRoot) {
		return fmt.Errorf("ISO inspection requires an absolute service root")
	}
	if len(opts.Components) == 0 {
		return fmt.Errorf("ISO inspection requires admitted components")
	}
	for name, address := range opts.Components {
		if strings.TrimSpace(name) == "" || !address.Is4() {
			return fmt.Errorf("ISO inspection component %q has invalid IPv4 address %v", name, address)
		}
	}
	return nil
}

func inspectISOComposePS(inspection *ISOInspection, opts ISOInspectOptions, ps []isoComposePSContainer) []string {
	counts := map[string]int{}
	idCounts := map[string]int{}
	var ids []string
	for _, container := range ps {
		container.ID = strings.TrimSpace(container.ID)
		if container.ID == "" {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has no container ID", container.Service))
		} else {
			idCounts[container.ID]++
			if idCounts[container.ID] == 1 {
				ids = append(ids, container.ID)
			} else {
				inspection.Findings = append(inspection.Findings, fmt.Sprintf("duplicate container ID %q in Compose project", container.ID))
			}
		}
		if _, ok := opts.Components[container.Service]; !ok {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("unexpected ISO component %q", container.Service))
			continue
		}
		counts[container.Service]++
		if !strings.EqualFold(container.State, "running") {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q is not running", container.Service))
		}
	}
	for _, component := range sortedISOComponentNames(opts.Components) {
		if counts[component] != 1 {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q requires exactly one running container, found %d", component, counts[component]))
		}
	}
	return ids
}

func inspectISODockerContainers(inspection *ISOInspection, opts ISOInspectOptions, ps []isoComposePSContainer, containers []isoDockerInspectContainer) {
	psByID := make(map[string]isoComposePSContainer, len(ps))
	for _, container := range ps {
		if container.ID != "" {
			psByID[container.ID] = container
		}
	}
	seen := map[string]int{}
	for _, container := range containers {
		psContainer, ok := psByID[container.ID]
		if !ok {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("docker inspect returned unexpected container %q", container.ID))
			continue
		}
		component := psContainer.Service
		seen[component]++
		inspection.Containers = append(inspection.Containers, container.ID)
		inspectISODockerContainer(inspection, opts, component, container)
	}
	for _, component := range sortedISOComponentNames(opts.Components) {
		if seen[component] != 1 {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q requires exactly one inspected container, found %d", component, seen[component]))
		}
	}
}

func inspectISODockerContainer(inspection *ISOInspection, opts ISOInspectOptions, component string, container isoDockerInspectContainer) {
	if !container.State.Running {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q inspected container is not running", component))
	}
	if container.Config.Labels["com.docker.compose.project"] != opts.ProjectName || container.Config.Labels["com.docker.compose.service"] != component {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has incorrect Compose identity labels", component))
	}
	inspectISOHostConfig(inspection, opts, component, container)
	inspectISONetworkSettings(inspection, opts, component, container)
	inspectISOMounts(inspection, opts, component, container)
}

func inspectISOHostConfig(inspection *ISOInspection, opts ISOInspectOptions, component string, container isoDockerInspectContainer) {
	host := container.HostConfig
	if host.NetworkMode != opts.NetworkName {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q uses network mode %q instead of generated network %q", component, host.NetworkMode, opts.NetworkName))
	}
	for name, mode := range map[string]string{"pid": host.PidMode, "ipc": host.IpcMode, "cgroup": host.CgroupnsMode, "uts": host.UTSMode, "user": host.UsernsMode} {
		if !safeISOContainerNamespaceMode(name, mode) {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q uses unsafe %s namespace mode %q", component, name, mode))
		}
	}
	inspectISOHostPrivileges(inspection, component, host)
}

func inspectISOHostPrivileges(inspection *ISOInspection, component string, host isoDockerInspectHostConfig) {
	if host.Privileged {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q is privileged", component))
	}
	if len(host.CapAdd) != 0 {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q adds capabilities %v", component, host.CapAdd))
	}
	if len(host.Devices) != 0 {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has host devices", component))
	}
	if len(host.DeviceCgroupRules) != 0 {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has device cgroup rules", component))
	}
	if len(host.DeviceRequests) != 0 {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has device requests", component))
	}
	if len(host.PortBindings) != 0 {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has published port bindings", component))
	}
	if len(host.SecurityOpt) != 0 {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has security options %v", component, host.SecurityOpt))
	}
}

func safeISOContainerNamespaceMode(kind, mode string) bool {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		return true
	}
	return kind == "cgroup" && mode == "private"
}

func inspectISONetworkSettings(inspection *ISOInspection, opts ISOInspectOptions, component string, container isoDockerInspectContainer) {
	networks := container.NetworkSettings.Networks
	inspectISONetworkCount(inspection, opts, component, networks)
	network, ok := networks[opts.NetworkName]
	if !ok {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q is not attached to generated network %q", component, opts.NetworkName))
		return
	}
	inspectISONetworkAddress(inspection, opts, component, network)
	if strings.TrimSpace(network.GlobalIPv6Address) != "" {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has IPv6 address %q", component, network.GlobalIPv6Address))
	}
	for port, bindings := range container.NetworkSettings.Ports {
		trimmed := strings.TrimSpace(string(bindings))
		if trimmed != "" && trimmed != "null" && trimmed != "[]" {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has published port %s", component, port))
		}
	}
}

func inspectISONetworkCount(inspection *ISOInspection, opts ISOInspectOptions, component string, networks map[string]isoDockerInspectEndpoint) {
	if len(networks) == 1 {
		return
	}
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q is attached to networks %v; only generated network %q is allowed", component, names, opts.NetworkName))
}

func inspectISONetworkAddress(inspection *ISOInspection, opts ISOInspectOptions, component string, network isoDockerInspectEndpoint) {
	address, err := netip.ParseAddr(network.IPAddress)
	want := opts.Components[component]
	if err != nil || address != want {
		inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q IPv4 address is %q, want %s", component, network.IPAddress, want))
		return
	}
	inspection.Addresses[component] = address
}

func inspectISOMounts(inspection *ISOInspection, opts ISOInspectOptions, component string, container isoDockerInspectContainer) {
	for _, mount := range container.Mounts {
		if !strings.EqualFold(mount.Type, "bind") {
			continue
		}
		if isISORuntimeHostControlPath(mount.Source) {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q has host-control bind %q", component, mount.Source))
			continue
		}
		_, err := ValidateISOHostSource(opts.ServiceRoot, mount.Source)
		if err != nil {
			inspection.Findings = append(inspection.Findings, fmt.Sprintf("component %q runtime bind %q is unsafe: %v", component, mount.Source, err))
		}
	}
}

// ValidateISOHostSource resolves the service root and source before applying
// the canonical ISO host-boundary rule shared by admission and runtime
// inspection. It returns the resolved source only when it stays beneath the
// service root and does not overlap host-managed control state.
func ValidateISOHostSource(serviceRoot, source string) (string, error) {
	root, err := resolveISORealPath(serviceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve service root: %w", err)
	}
	resolvedSource, err := resolveISORealPath(source)
	if err != nil {
		return "", fmt.Errorf("resolve canonical host path: %w", err)
	}
	if !isoHostPathWithin(root, resolvedSource) {
		return "", fmt.Errorf("host path resolves outside the service root")
	}
	for _, name := range isoHostManagedPathNames {
		reserved := filepath.Join(root, name)
		if resolved, err := filepath.EvalSymlinks(reserved); err == nil {
			reserved = resolved
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve host-managed %s path: %w", name, err)
		}
		if isoHostPathsOverlap(resolvedSource, reserved) {
			return "", fmt.Errorf("host path overlaps host-managed service %s state", name)
		}
	}
	if err := validateISOSpecialHostSource(resolvedSource); err != nil {
		return "", err
	}
	return resolvedSource, nil
}

var (
	statISOHostSource             = os.Stat
	inspectISONamespaceHostSource = isISONamespaceHostSource
)

func validateISOSpecialHostSource(source string) error {
	info, err := statISOHostSource(source)
	if err != nil {
		return fmt.Errorf("inspect canonical host resource: %w", err)
	}
	if info.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
		return fmt.Errorf("block devices, character devices, FIFOs, and sockets are not allowed")
	}
	isNamespace, err := inspectISONamespaceHostSource(source)
	if err != nil {
		return fmt.Errorf("inspect canonical host resource filesystem: %w", err)
	}
	if isNamespace {
		return fmt.Errorf("namespace handles are not allowed")
	}
	return nil
}

var isoHostManagedPathNames = [...]string{"run", "bin", "tailscale"}

func resolveISORealPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path is not absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func isoHostPathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isoHostPathsOverlap(left, right string) bool {
	return isoHostPathWithin(left, right) || isoHostPathWithin(right, left)
}

func isISORuntimeHostControlPath(path string) bool {
	clean := strings.ToLower(filepath.Clean(path))
	for _, suffix := range []string{"docker.sock", "containerd.sock", "podman.sock", "crio.sock", "dockershim.sock"} {
		if strings.HasSuffix(clean, string(filepath.Separator)+suffix) {
			return true
		}
	}
	return strings.Contains(clean, string(filepath.Separator)+"proc"+string(filepath.Separator)) && strings.Contains(clean, string(filepath.Separator)+"ns"+string(filepath.Separator))
}

func sortedISOComponentNames(components map[string]netip.Addr) []string {
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func defaultISOInspectRunner(opts ISOInspectOptions) isoInspectRunner {
	return func(ctx context.Context, operation string, ids ...string) ([]byte, error) {
		dockerPath, err := DockerCmd()
		if err != nil {
			return nil, err
		}
		var args []string
		switch operation {
		case "compose-ps":
			args = []string{"compose", "--project-name", opts.ProjectName}
			if opts.ProjectDir != "" {
				args = append(args, "--project-directory", opts.ProjectDir)
			}
			for _, file := range opts.ComposeFiles {
				args = append(args, "--file", file)
			}
			args = append(args, "ps", "--format", "json", "--no-trunc")
		case "inspect":
			args = append([]string{"inspect"}, ids...)
		default:
			return nil, fmt.Errorf("unknown ISO Docker inspection operation %q", operation)
		}
		var cmd *exec.Cmd
		if opts.NewCmd != nil {
			cmd = opts.NewCmd(ctx, dockerPath, args...)
		} else {
			cmd = exec.CommandContext(ctx, dockerPath, args...)
		}
		if opts.ProjectDir != "" {
			cmd.Dir = opts.ProjectDir
		}
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("docker %s: %w", operation, err)
		}
		return output, nil
	}
}
