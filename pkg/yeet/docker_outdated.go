// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/cli"
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
	Running   string
	Latest    string
	Status    string
}

var fetchDockerOutdatedForHostFn = fetchDockerOutdatedForHost

func handleDockerOutdatedCommand(ctx context.Context, args []string, cfgLoc *projectConfigLocation, hostOverrideSet bool) error {
	if len(args) == 0 || args[0] != "outdated" {
		return handleSvcRemote(ctx, svcCommandRequest{
			Command: svcCommand{Name: "docker", Args: args, RawArgs: append([]string{"docker"}, args...)},
			Config:  cfgLoc,
			Service: getService(),
		})
	}
	flags, remaining, err := cli.ParseDockerOutdated(args[1:])
	if err != nil {
		return err
	}
	if len(remaining) > 1 {
		return fmt.Errorf("docker outdated takes at most one service")
	}
	if _, err := dockerOutdatedFormat(flags.Format); err != nil {
		return err
	}
	service := serviceOverride
	if len(remaining) == 1 {
		service = remaining[0]
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
	type hostResult struct {
		host string
		rows []dockerOutdatedRow
		err  error
	}

	results := make([]dockerOutdatedHostData, 0, len(hosts))
	ch := make(chan hostResult, len(hosts))
	for _, host := range hosts {
		host := host
		go func() {
			rows, err := fetchDockerOutdatedForHostFn(ctx, host, service, flags)
			ch <- hostResult{host: host, rows: rows, err: err}
		}()
	}
	for range hosts {
		res := <-ch
		if res.err != nil {
			return res.err
		}
		results = append(results, dockerOutdatedHostData{Host: res.host, Rows: res.rows})
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
	if _, err := fmt.Fprintln(tw, "SERVICE\tHOST\tCONTAINER\tIMAGE\tRUNNING\tLATEST\tSTATUS"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Service,
			row.Host,
			row.Container,
			row.Image,
			row.Running,
			row.Latest,
			row.Status,
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
			status := row.Status
			if row.Reason != "" {
				status += ": " + row.Reason
			}
			rows = append(rows, dockerOutdatedRenderRow{
				Service:   row.ServiceName,
				Host:      result.Host,
				Container: dash(row.ContainerName),
				Image:     dash(row.Image),
				Running:   dash(row.RunningDigest),
				Latest:    dash(row.LatestDigest),
				Status:    dash(status),
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
