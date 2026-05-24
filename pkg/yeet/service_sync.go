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
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
)

var fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
	return newRPCClient(host).ServiceInfo(ctx, service)
}

type serviceSyncTarget struct {
	Service string
	Host    string
}

type serviceSyncResult struct {
	Target serviceSyncTarget
	Root   string
	ZFS    bool
	Skip   string
}

func handleServiceSync(ctx context.Context, req svcCommandRequest) error {
	flags, remaining, err := cli.ParseServiceSync(req.Command.Args[1:])
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
	if updated == 0 {
		return serviceSyncNoUpdatesError(flags.All, results)
	}
	if err := saveProjectConfig(cfgLoc); err != nil {
		return err
	}
	return renderServiceSyncResults(os.Stdout, cfgLoc.Path, flags.All, results, updated, skipped)
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
	host, err := serviceSyncHost(cfg, service, hostOverride, hostOverrideSet)
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.ServiceEntry(service, host); !ok {
		return nil, fmt.Errorf("no yeet.toml entry for %s@%s", service, host)
	}
	return []serviceSyncTarget{{Service: service, Host: host}}, nil
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
	root, zfs := serviceRootForLocalConfig(resp.Info)
	result.Root = root
	result.ZFS = zfs
	if !cfgLoc.Config.SetServiceRootForEntry(target.Service, target.Host, root, zfs) {
		return serviceSyncResult{}, false, fmt.Errorf("no yeet.toml entry for %s@%s", target.Service, target.Host)
	}
	return result, true, nil
}

func serviceRootForLocalConfig(info catchrpc.ServiceInfo) (string, bool) {
	if root := strings.TrimSpace(info.Paths.ServiceRootZFS); root != "" {
		return root, true
	}
	if root := strings.TrimSpace(info.Paths.ServiceRoot); root != "" {
		return root, false
	}
	return "", false
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
	if all {
		_, err := fmt.Fprintf(w, "Updated %s\n", target)
		return err
	}
	return renderServiceSyncDetail(w, configPath, target, result)
}

func renderServiceSyncDetail(w io.Writer, configPath string, target string, result serviceSyncResult) error {
	if _, err := fmt.Fprintf(w, "Updated %s in %s\n", target, configPath); err != nil {
		return err
	}
	if result.Root == "" {
		if _, err := fmt.Fprintln(w, "  service_root = <default>"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(w, "  service_root = %q\n", result.Root); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  service_root_zfs = %t\n", result.ZFS)
	return err
}
