// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
)

var fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
	return newRPCClient(host).ServiceInfo(ctx, service)
}

type serviceSyncTarget struct {
	Service       string
	Host          string
	CreateMissing bool
}

type serviceSyncResult struct {
	Target           serviceSyncTarget
	Created          bool
	Root             string
	ZFS              bool
	Snapshots        string
	SnapshotKeepLast int
	SnapshotMaxAge   string
	SnapshotRequired *bool
	SnapshotEvents   []string
	Ports            []string
	PortsSynced      bool
	Skip             string
}

func handleServiceSync(ctx context.Context, req svcCommandRequest) error {
	flags, remaining, err := parseServiceSyncRequest(req.Command.Args[1:], req.Service)
	if err != nil {
		return err
	}
	cfgLoc, err := serviceSyncConfig(req.Config, flags.Config)
	if err != nil {
		return err
	}
	targets, err := serviceSyncTargets(cfgLoc, req, flags, remaining)
	if err != nil {
		return err
	}

	results := make([]serviceSyncResult, 0, len(targets))
	updated := 0
	skipped := 0
	for _, target := range targets {
		result, ok, err := syncOneServiceRoot(ctx, cfgLoc, target)
		if err != nil {
			return err
		}
		results = append(results, result)
		if ok {
			updated++
			continue
		}
		skipped++
	}
	return finishServiceSync(cfgLoc, flags.All, results, updated, skipped)
}

func parseServiceSyncRequest(args []string, service string) (cli.ServiceSyncFlags, []string, error) {
	flags, remaining, err := cli.ParseServiceSync(args)
	if err == nil {
		return flags, remaining, nil
	}
	if strings.TrimSpace(service) == "" || !strings.Contains(err.Error(), "service sync requires a service name or --all") {
		return cli.ServiceSyncFlags{}, nil, err
	}
	argsWithService := append(append([]string{}, args...), strings.TrimSpace(service))
	return cli.ParseServiceSync(argsWithService)
}

func finishServiceSync(cfgLoc *projectConfigLocation, all bool, results []serviceSyncResult, updated, skipped int) error {
	if updated == 0 {
		if all {
			if err := renderServiceSyncResults(os.Stdout, cfgLoc.Path, all, results, updated, skipped); err != nil {
				return err
			}
		}
		return serviceSyncNoUpdatesError(all, results)
	}
	if err := saveProjectConfig(cfgLoc); err != nil {
		return err
	}
	return renderServiceSyncResults(os.Stdout, cfgLoc.Path, all, results, updated, skipped)
}

func serviceSyncConfig(existing *projectConfigLocation, configPath string) (*projectConfigLocation, error) {
	if strings.TrimSpace(configPath) != "" {
		return loadProjectConfigFromFile(configPath)
	}
	if existing == nil || existing.Config == nil {
		return nil, fmt.Errorf("no %s found; run from a project directory or pass --config", projectConfigName)
	}
	return existing, nil
}

func serviceSyncTargets(cfgLoc *projectConfigLocation, req svcCommandRequest, flags cli.ServiceSyncFlags, remaining []string) ([]serviceSyncTarget, error) {
	if flags.All {
		return serviceSyncAllTargets(cfgLoc.Config, req)
	}
	return serviceSyncNamedTarget(cfgLoc.Config, req, remaining)
}

func serviceSyncAllTargets(cfg *ProjectConfig, req svcCommandRequest) ([]serviceSyncTarget, error) {
	host := serviceConfigHost(req.HostOverride)
	if req.HostOverrideSet {
		host = req.HostOverride
	}
	targets := make([]serviceSyncTarget, 0)
	for _, entry := range cfg.Services {
		if entry.Host != host {
			continue
		}
		targets = append(targets, serviceSyncTarget{Service: entry.Name, Host: entry.Host})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Host == targets[j].Host {
			return targets[i].Service < targets[j].Service
		}
		return targets[i].Host < targets[j].Host
	})
	if len(targets) == 0 {
		return nil, fmt.Errorf("no yeet.toml entries for host %s", host)
	}
	return targets, nil
}

func serviceSyncNamedTarget(cfg *ProjectConfig, req svcCommandRequest, remaining []string) ([]serviceSyncTarget, error) {
	service := strings.TrimSpace(req.Service)
	hostOverride := req.HostOverride
	hostOverrideSet := req.HostOverrideSet
	if len(remaining) > 0 {
		service = strings.TrimSpace(remaining[0])
	}
	if svc, host, ok := splitServiceHost(service); ok {
		service = strings.TrimSpace(svc)
		hostOverride = strings.TrimSpace(host)
		hostOverrideSet = true
	}
	if service == "" {
		return nil, fmt.Errorf("service sync requires a service name or --all")
	}
	createMissing := hostOverrideSet && strings.TrimSpace(hostOverride) != ""
	host, err := serviceSyncHost(cfg, service, hostOverride, hostOverrideSet)
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.ServiceEntry(service, host); !ok && !createMissing {
		return nil, fmt.Errorf("no yeet.toml entry for %s@%s", service, host)
	}
	return []serviceSyncTarget{{Service: service, Host: host, CreateMissing: createMissing}}, nil
}

func serviceSyncHost(cfg *ProjectConfig, service, hostOverride string, hostOverrideSet bool) (string, error) {
	host := strings.TrimSpace(hostOverride)
	if hostOverrideSet && host != "" {
		return host, nil
	}
	resolved, err := resolveServiceHost(cfg, service)
	if err != nil {
		return "", err
	}
	if resolved != "" {
		SetHost(resolved)
		return resolved, nil
	}
	host = Host()
	if host == "" {
		return "", fmt.Errorf("no yeet.toml entry for %s@%s", service, host)
	}
	return host, nil
}

func syncOneServiceRoot(ctx context.Context, cfgLoc *projectConfigLocation, target serviceSyncTarget) (serviceSyncResult, bool, error) {
	resp, err := fetchServiceInfoForSyncFn(ctx, target.Host, target.Service)
	if err != nil {
		return serviceSyncResult{}, false, err
	}
	result := serviceSyncResult{Target: target}
	if !resp.Found {
		result.Skip = "service not found on catch"
		return result, false, nil
	}
	root, zfs, err := serviceRootForLocalConfig(target.Host, resp.Info)
	if err != nil {
		return serviceSyncResult{}, false, err
	}
	result.Root = root
	result.ZFS = zfs
	if err := syncServiceEntryRoot(cfgLoc.Config, target, resp.Info, root, zfs, &result); err != nil {
		return serviceSyncResult{}, false, err
	}
	if err := syncServiceSnapshotPolicy(cfgLoc.Config, target, resp.Info.Snapshots, &result); err != nil {
		return serviceSyncResult{}, false, err
	}
	if err := syncServicePorts(cfgLoc.Config, target, resp.Info.Network.PortsPresent, resp.Info.Network.Ports, &result); err != nil {
		return serviceSyncResult{}, false, err
	}
	return result, true, nil
}

func syncServiceEntryRoot(cfg *ProjectConfig, target serviceSyncTarget, info catchrpc.ServiceInfo, root string, zfs bool, result *serviceSyncResult) error {
	if _, ok := cfg.ServiceEntry(target.Service, target.Host); !ok {
		if !target.CreateMissing {
			return serviceSyncMissingEntryError(target)
		}
		cfg.SetServiceEntry(serviceEntryFromSyncInfo(target, info))
		result.Created = true
	}
	if !cfg.SetServiceRootForEntry(target.Service, target.Host, root, zfs) {
		return serviceSyncMissingEntryError(target)
	}
	return nil
}

func syncServiceSnapshotPolicy(cfg *ProjectConfig, target serviceSyncTarget, snapshots *catchrpc.ServiceSnapshots, result *serviceSyncResult) error {
	snapshotPolicy, hasSnapshotInfo := serviceSyncSnapshotOverride(snapshots)
	if hasSnapshotInfo {
		applySnapshotPolicyToSyncResult(result, snapshotPolicy)
		if !cfg.SetServiceSnapshotsForEntry(target.Service, target.Host, snapshotPolicy) {
			return serviceSyncMissingEntryError(target)
		}
	}
	return nil
}

func syncServicePorts(cfg *ProjectConfig, target serviceSyncTarget, portsPresent bool, servicePorts []catchrpc.ServicePort, result *serviceSyncResult) error {
	if portsPresent {
		ports := servicePortsForConfig(servicePorts)
		result.Ports = ports
		result.PortsSynced = true
		if !setServicePortsForEntry(cfg, target.Service, target.Host, ports) {
			return serviceSyncMissingEntryError(target)
		}
	}
	return nil
}

func serviceSyncMissingEntryError(target serviceSyncTarget) error {
	return fmt.Errorf("no yeet.toml entry for %s@%s", target.Service, target.Host)
}

func serviceEntryFromSyncInfo(target serviceSyncTarget, info catchrpc.ServiceInfo) ServiceEntry {
	entry := ServiceEntry{
		Name: target.Service,
		Host: target.Host,
		Type: strings.TrimSpace(info.ServiceType),
	}
	if info.VM != nil || entry.Type == serviceTypeVM {
		entry.Type = serviceTypeVM
		entry.PayloadKind = serviceTypeVM
		if info.VM != nil {
			entry.Payload = strings.TrimSpace(info.VM.Image)
			entry.Args = serviceSyncVMArgs(info.VM)
		}
	}
	return entry
}

func serviceSyncVMArgs(vm *catchrpc.ServiceVM) []string {
	if vm == nil {
		return nil
	}
	var args []string
	if vm.CPUs > 0 {
		args = append(args, "--cpus="+strconv.Itoa(vm.CPUs))
	}
	if memory := serviceSyncSizeArg(vm.MemoryBytes); memory != "" {
		args = append(args, "--memory="+memory)
	}
	if disk := serviceSyncSizeArg(vm.DiskBytes); disk != "" {
		args = append(args, "--disk="+disk)
	}
	if modes := serviceSyncVMNetworkModes(vm.Networks); len(modes) != 0 {
		args = append(args, "--net="+strings.Join(modes, ","))
	}
	return args
}

func serviceSyncSizeArg(bytes int64) string {
	switch {
	case bytes <= 0:
		return ""
	case bytes%(1<<30) == 0:
		return strconv.FormatInt(bytes>>30, 10) + "g"
	case bytes%(1<<20) == 0:
		return strconv.FormatInt(bytes>>20, 10) + "m"
	default:
		return strconv.FormatInt(bytes, 10)
	}
}

func serviceSyncVMNetworkModes(networks []catchrpc.ServiceVMNetwork) []string {
	seen := map[string]struct{}{}
	var modes []string
	for _, network := range networks {
		mode := strings.TrimSpace(network.Mode)
		if mode == "" {
			continue
		}
		if _, ok := seen[mode]; ok {
			continue
		}
		seen[mode] = struct{}{}
		modes = append(modes, mode)
	}
	return modes
}

func setServicePortsForEntry(cfg *ProjectConfig, service, host string, ports []string) bool {
	entry, ok := cfg.ServiceEntry(service, host)
	if !ok {
		return false
	}
	entry.Ports = normalizePublishPorts(ports)
	cfg.SetServiceEntry(entry)
	return true
}

func servicePortsForConfig(ports []catchrpc.ServicePort) []string {
	out := make([]string, 0, len(ports))
	for _, port := range ports {
		if raw := strings.TrimSpace(port.Raw); raw != "" {
			out = append(out, raw)
			continue
		}
		mapping := servicePortForConfig(port)
		if mapping != "" {
			out = append(out, mapping)
		}
	}
	return normalizePublishPorts(out)
}

func servicePortForConfig(port catchrpc.ServicePort) string {
	if port.HostPort == 0 || port.ContainerPort == 0 {
		return ""
	}
	protocol := strings.TrimSpace(port.Protocol)
	if protocol == "" {
		protocol = "tcp"
	}
	mapping := fmt.Sprintf("%d:%d", port.HostPort, port.ContainerPort)
	if strings.TrimSpace(port.HostIP) != "" {
		mapping = strings.TrimSpace(port.HostIP) + ":" + mapping
	}
	if protocol != "tcp" {
		mapping += "/" + protocol
	}
	return mapping
}

func serviceSyncSnapshotOverride(snapshots *catchrpc.ServiceSnapshots) (*catchrpc.SnapshotPolicy, bool) {
	if snapshots == nil {
		return nil, false
	}
	return snapshots.Override, true
}

func applySnapshotPolicyToSyncResult(result *serviceSyncResult, policy *catchrpc.SnapshotPolicy) {
	if policy == nil {
		return
	}
	if policy.Enabled != nil {
		if *policy.Enabled {
			result.Snapshots = "on"
		} else {
			result.Snapshots = "off"
		}
	}
	if policy.KeepLast != nil {
		result.SnapshotKeepLast = *policy.KeepLast
	}
	result.SnapshotMaxAge = strings.TrimSpace(policy.MaxAge)
	if policy.Required != nil {
		required := *policy.Required
		result.SnapshotRequired = &required
	}
	result.SnapshotEvents = append([]string{}, policy.Events...)
}

func serviceRootForLocalConfig(host string, info catchrpc.ServiceInfo) (string, bool, error) {
	if root := strings.TrimSpace(info.Paths.ServiceRootZFS); root != "" {
		return root, true, nil
	}
	if root := strings.TrimSpace(info.Paths.ServiceRoot); root != "" {
		return root, false, nil
	}
	if root := strings.TrimSpace(info.Paths.EffectiveRoot); root != "" {
		return "", false, nil
	}
	if root := strings.TrimSpace(info.Paths.Root); root != "" {
		return "", false, fmt.Errorf("catch on %s does not expose service root identity; upgrade catch before running service sync", host)
	}
	return "", false, nil
}

func serviceSyncNoUpdatesError(all bool, results []serviceSyncResult) error {
	if !all && len(results) == 1 && results[0].Skip != "" {
		return fmt.Errorf("service %q not found on %s", results[0].Target.Service, results[0].Target.Host)
	}
	if all {
		return fmt.Errorf("no services synced")
	}
	return fmt.Errorf("service sync made no changes")
}

func renderServiceSyncResults(w io.Writer, configPath string, all bool, results []serviceSyncResult, updated, skipped int) error {
	for _, result := range results {
		if err := renderServiceSyncResult(w, configPath, all, result); err != nil {
			return err
		}
	}
	if all {
		if _, err := fmt.Fprintf(w, "%d updated, %d skipped\n", updated, skipped); err != nil {
			return err
		}
	}
	return nil
}

func renderServiceSyncResult(w io.Writer, configPath string, all bool, result serviceSyncResult) error {
	target := result.Target.Service + "@" + result.Target.Host
	if result.Skip != "" {
		_, err := fmt.Fprintf(w, "Skipped %s: %s\n", target, result.Skip)
		return err
	}
	verb := "Updated"
	if result.Created {
		verb = "Created"
	}
	if all {
		_, err := fmt.Fprintf(w, "%s %s\n", verb, target)
		return err
	}
	return renderServiceSyncDetail(w, configPath, verb, target, result)
}

func renderServiceSyncDetail(w io.Writer, configPath string, verb string, target string, result serviceSyncResult) error {
	if _, err := fmt.Fprintf(w, "%s %s in %s\n", verb, target, configPath); err != nil {
		return err
	}
	if result.Root == "" {
		if _, err := fmt.Fprintln(w, "  service_root = <default>"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(w, "  service_root = %q\n", result.Root); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  service_root_zfs = %t\n", result.ZFS); err != nil {
		return err
	}
	if result.PortsSynced {
		if _, err := fmt.Fprintf(w, "  ports = [%s]\n", formatServiceSyncPorts(result.Ports)); err != nil {
			return err
		}
	}
	return nil
}

func formatServiceSyncPorts(ports []string) string {
	if len(ports) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(ports))
	for _, port := range ports {
		quoted = append(quoted, fmt.Sprintf("%q", port))
	}
	return strings.Join(quoted, ", ")
}
