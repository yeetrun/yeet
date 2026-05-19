// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
)

type dockerUpdateTarget struct {
	Service string
	Host    string
}

func dockerUpdateServicesFromRequest(remaining []string) ([]string, error) {
	if serviceOverride != "" && len(remaining) > 0 {
		return nil, fmt.Errorf("docker update takes either --service or service arguments, not both")
	}
	if len(remaining) > 0 {
		return append([]string(nil), remaining...), nil
	}
	if serviceOverride != "" {
		return []string{serviceOverride}, nil
	}
	return nil, missingServiceError([]string{"docker", "update"})
}

func handleDockerUpdateCommand(ctx context.Context, req svcCommandRequest) error {
	flags, remaining, err := cli.ParseDockerUpdate(req.Command.Args[1:])
	if err != nil {
		return err
	}
	if flags.Outdated {
		if len(remaining) > 0 || serviceOverride != "" || (req.Service != "" && req.Service != systemServiceName) {
			return fmt.Errorf("docker update --outdated does not take a service; use yeet docker update <svc> for one service")
		}
		return dockerUpdateOutdatedMultiHost(ctx, statusHosts(req.Config, req.HostOverrideSet))
	}
	services, err := dockerUpdateServicesFromRequest(remaining)
	if err != nil {
		return err
	}
	return dockerUpdateExplicitTargets(ctx, req, services)
}

func dockerUpdateExplicitTargets(ctx context.Context, req svcCommandRequest, services []string) error {
	targets, errs := resolveDockerUpdateTargets(services, req.Config, req.HostOverride, req.HostOverrideSet)
	showMarkers := len(targets) > 1
	for _, target := range targets {
		if showMarkers {
			if err := dockerUpdateOutdatedLine(os.Stdout, "==> %s/%s\n", target.Host, target.Service); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := updateDockerServiceForHostFn(ctx, target.Host, target.Service); err != nil {
			errs = append(errs, err)
			if showMarkers {
				if writeErr := dockerUpdateOutdatedLine(os.Stdout, "==> %s/%s failed: %v\n", target.Host, target.Service, err); writeErr != nil {
					errs = append(errs, writeErr)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func resolveDockerUpdateTargets(services []string, cfgLoc *projectConfigLocation, hostOverride string, hostOverrideSet bool) ([]dockerUpdateTarget, []error) {
	targets := make([]dockerUpdateTarget, 0, len(services))
	errs := make([]error, 0)
	seen := make(map[string]struct{})
	for _, raw := range services {
		target, err := resolveDockerUpdateTarget(raw, cfgLoc, hostOverride, hostOverrideSet)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		key := target.Host + "/" + target.Service
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return targets, errs
}

func resolveDockerUpdateTarget(raw string, cfgLoc *projectConfigLocation, hostOverride string, hostOverrideSet bool) (dockerUpdateTarget, error) {
	raw = strings.TrimSpace(raw)
	service, host, qualified := splitServiceHost(raw)
	service = strings.TrimSpace(service)
	host = strings.TrimSpace(host)
	if service == "" {
		return dockerUpdateTarget{}, fmt.Errorf("docker update service name cannot be empty")
	}
	if qualified {
		return dockerUpdateTarget{Service: service, Host: host}, nil
	}
	if hostOverrideSet {
		return dockerUpdateTarget{Service: service, Host: hostOverride}, nil
	}
	if cfgLoc != nil && cfgLoc.Config != nil {
		resolved, err := resolveServiceHost(cfgLoc.Config, service)
		if err != nil {
			return dockerUpdateTarget{}, err
		}
		if resolved != "" {
			return dockerUpdateTarget{Service: service, Host: resolved}, nil
		}
	}
	return dockerUpdateTarget{Service: service, Host: Host()}, nil
}

func updateDockerServiceForHost(ctx context.Context, host string, service string) error {
	return withTemporaryHost(host, func() error {
		if err := execRemoteFn(ctx, service, []string{"docker", "update"}, nil, true); err != nil {
			return fmt.Errorf("%s/%s: %w", host, service, err)
		}
		return nil
	})
}

func withTemporaryHost(host string, fn func() error) error {
	oldPrefs := loadedPrefs
	loadedPrefs.DefaultHost = host
	defer func() {
		loadedPrefs = oldPrefs
	}()
	return fn()
}
