// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/svc"
	"golang.org/x/sync/errgroup"
)

type dockerOutdatedHostData struct {
	Host string              `json:"host"`
	Rows []dockerOutdatedRow `json:"rows"`
}

type dockerOutdatedRow struct {
	ServiceName   string `json:"serviceName"`
	ContainerID   string `json:"containerID,omitempty"`
	ContainerName string `json:"containerName"`
	Image         string `json:"image"`
	RunningDigest string `json:"runningDigest,omitempty"`
	LatestDigest  string `json:"latestDigest,omitempty"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
}

type dockerOutdatedRenderRow struct {
	Service   string
	Host      string
	Container string
	Image     string
	Update    string
}

var fetchDockerOutdatedForHostFn = fetchDockerOutdatedForHost
var updateDockerServiceForHostFn = updateDockerServiceForHost

func handleDockerOutdatedCommand(ctx context.Context, args []string, cfgLoc *projectConfigLocation, hostOverrideSet bool) error {
	if len(args) == 0 || args[0] != "outdated" {
		return handleSvcRemote(ctx, svcCommandRequest{
			Command: svcCommand{Name: "docker", Args: args, RawArgs: append([]string{"docker"}, args...)},
			Config:  cfgLoc,
			Service: getService(),
		})
	}
	flags, service, err := parseDockerOutdatedLocalArgs(args[1:])
	if err != nil {
		return err
	}
	if service == "" {
		return dockerOutdatedMultiHost(ctx, statusHosts(cfgLoc, hostOverrideSet), "", flags)
	}
	if shouldRenderDockerOutdatedTable(flags.Format) {
		rows, err := fetchDockerOutdatedForHostFn(ctx, Host(), service, flags)
		if err != nil {
			return err
		}
		return renderDockerOutdatedTables(os.Stdout, []dockerOutdatedHostData{{Host: Host(), Rows: rows}})
	}
	return execRemoteFn(ctx, service, dockerOutdatedRemoteArgs(flags), nil, true)
}

func handleDockerUpdateCommand(ctx context.Context, req svcCommandRequest) error {
	flags, remaining, err := cli.ParseDockerUpdate(req.Command.Args[1:])
	if err != nil {
		return err
	}
	if !flags.Outdated {
		return handleSvcRemote(ctx, req)
	}
	if len(remaining) > 0 || serviceOverride != "" || (req.Service != "" && req.Service != systemServiceName) {
		return fmt.Errorf("docker update --outdated does not take a service; use yeet docker update <svc> for one service")
	}
	return dockerUpdateOutdatedMultiHost(ctx, statusHosts(req.Config, req.HostOverrideSet))
}

func parseDockerOutdatedLocalArgs(args []string) (cli.DockerOutdatedFlags, string, error) {
	flags, remaining, err := cli.ParseDockerOutdated(args)
	if err != nil {
		return cli.DockerOutdatedFlags{}, "", err
	}
	if len(remaining) > 1 {
		return cli.DockerOutdatedFlags{}, "", fmt.Errorf("docker outdated takes at most one service argument")
	}
	if len(remaining) == 1 {
		if serviceOverride != "" {
			return cli.DockerOutdatedFlags{}, "", fmt.Errorf("docker outdated takes at most one service argument")
		}
		return cli.DockerOutdatedFlags{}, "", fmt.Errorf("docker outdated positional service arguments must be resolved before local handling")
	}
	if _, err := dockerOutdatedFormat(flags.Format); err != nil {
		return cli.DockerOutdatedFlags{}, "", err
	}
	return flags, serviceOverride, nil
}

func dockerUpdateOutdatedMultiHost(ctx context.Context, hosts []string) error {
	hosts = append([]string(nil), hosts...)
	sort.Strings(hosts)
	errs := make([]error, 0)
	for _, host := range hosts {
		if err := dockerUpdateOutdatedHost(ctx, os.Stdout, host); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func dockerUpdateOutdatedHost(ctx context.Context, w io.Writer, host string) error {
	rows, err := fetchDockerOutdatedForHostFn(ctx, host, "", cli.DockerOutdatedFlags{})
	if err != nil {
		if writeErr := dockerUpdateOutdatedLine(w, "==> %s: error: %v\n", host, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	services := outdatedServiceNames(rows)
	if len(services) == 0 {
		return dockerUpdateOutdatedLine(w, "==> %s: no updates\n", host)
	}
	errs := make([]error, 0)
	for _, service := range services {
		if err := dockerUpdateOutdatedLine(w, "==> %s/%s\n", host, service); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := updateDockerServiceForHostFn(ctx, host, service); err != nil {
			errs = append(errs, err)
			if writeErr := dockerUpdateOutdatedLine(w, "==> %s/%s failed: %v\n", host, service, err); writeErr != nil {
				errs = append(errs, writeErr)
			}
		}
	}
	return errors.Join(errs...)
}

func dockerUpdateOutdatedLine(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func outdatedServiceNames(rows []dockerOutdatedRow) []string {
	seen := make(map[string]struct{})
	for _, row := range rows {
		if row.Status != string(svc.DockerOutdatedUpdateAvailable) {
			continue
		}
		service := strings.TrimSpace(row.ServiceName)
		if service == "" {
			continue
		}
		seen[service] = struct{}{}
	}
	services := make([]string, 0, len(seen))
	for service := range seen {
		services = append(services, service)
	}
	sort.Strings(services)
	return services
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

func dockerOutdatedFormat(format string) (string, error) {
	format = strings.TrimSpace(format)
	switch format {
	case "", "table":
		return "table", nil
	case "json", "json-pretty":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported docker outdated format %q", format)
	}
}

func shouldRenderDockerOutdatedTable(format string) bool {
	normalized, err := dockerOutdatedFormat(format)
	return err == nil && normalized == "table"
}

func dockerOutdatedMultiHost(ctx context.Context, hosts []string, service string, flags cli.DockerOutdatedFlags) error {
	format, err := dockerOutdatedFormat(flags.Format)
	if err != nil {
		return err
	}

	results := make([]dockerOutdatedHostData, 0, len(hosts))
	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	for _, host := range hosts {
		host := host
		group.Go(func() error {
			rows, err := fetchDockerOutdatedForHostFn(groupCtx, host, service, flags)
			if err != nil {
				return err
			}
			mu.Lock()
			results = append(results, dockerOutdatedHostData{Host: host, Rows: rows})
			mu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Host < results[j].Host
	})
	if format == "json" || format == "json-pretty" {
		enc := json.NewEncoder(os.Stdout)
		if format == "json-pretty" {
			enc.SetIndent("", "  ")
		}
		return enc.Encode(results)
	}
	return renderDockerOutdatedTables(os.Stdout, results)
}

func fetchDockerOutdatedForHost(ctx context.Context, host string, service string, _ cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
	targetService := service
	if targetService == "" {
		targetService = systemServiceName
	}
	payload, err := execRemoteOutputFn(ctx, host, targetService, []string{"docker", "outdated", "--format=json"}, nil)
	if err != nil {
		return nil, fmt.Errorf("docker outdated on %s: %w", host, err)
	}
	var rows []dockerOutdatedRow
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, fmt.Errorf("docker outdated on %s returned invalid JSON: %w", host, err)
	}
	return rows, nil
}

func renderDockerOutdatedTables(w io.Writer, results []dockerOutdatedHostData) error {
	rows := flattenDockerOutdatedRows(results)
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SERVICE\tHOST\tCONTAINER\tIMAGE\tUPDATE"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.Service,
			row.Host,
			row.Container,
			row.Image,
			row.Update,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func flattenDockerOutdatedRows(results []dockerOutdatedHostData) []dockerOutdatedRenderRow {
	rows := make([]dockerOutdatedRenderRow, 0)
	for _, result := range results {
		for _, row := range result.Rows {
			rows = append(rows, dockerOutdatedRenderRow{
				Service:   row.ServiceName,
				Host:      result.Host,
				Container: dash(row.ContainerName),
				Image:     svc.CompactDockerOutdatedImageRef(row.Image),
				Update:    svc.CompactDockerOutdatedStatus(svc.DockerOutdatedStatus(row.Status), row.Reason),
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Service != rows[j].Service {
			return rows[i].Service < rows[j].Service
		}
		if rows[i].Host != rows[j].Host {
			return rows[i].Host < rows[j].Host
		}
		if rows[i].Container != rows[j].Container {
			return rows[i].Container < rows[j].Container
		}
		return rows[i].Image < rows[j].Image
	})
	return rows
}

func dockerOutdatedRemoteArgs(flags cli.DockerOutdatedFlags) []string {
	args := []string{"docker", "outdated"}
	if strings.TrimSpace(flags.Format) != "" {
		args = append(args, "--format="+strings.TrimSpace(flags.Format))
	}
	return args
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
