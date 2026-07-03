// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type statusSnapshotCommandContext func(context.Context, string, ...string) *exec.Cmd

var newStatusSnapshotCommand statusSnapshotCommandContext = exec.CommandContext

type dockerPSStatusRow struct {
	Labels string `json:"Labels"`
	State  string `json:"State"`
}

func parseDockerComposeStatusSnapshot(raw []byte) (map[string]svc.DockerComposeStatus, error) {
	statuses := make(map[string]svc.DockerComposeStatus)
	parsedRows := 0
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		row, ok := parseDockerPSStatusRow(line)
		if !ok {
			continue
		}
		parsedRows++
		labels := parseDockerPSLabels(row.Labels)
		project := labels["com.docker.compose.project"]
		component := labels["com.docker.compose.service"]
		serviceName, ok := statusServiceNameFromComposeProject(project)
		if !ok || component == "" {
			continue
		}
		if statuses[serviceName] == nil {
			statuses[serviceName] = make(svc.DockerComposeStatus)
		}
		statuses[serviceName][component] = svc.DockerComposeStateStatus(row.State)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan docker ps status output: %w", err)
	}
	if parsedRows == 0 && strings.TrimSpace(string(raw)) != "" {
		return nil, fmt.Errorf("no valid docker status rows")
	}
	return statuses, nil
}

func parseDockerPSStatusRow(line string) (dockerPSStatusRow, bool) {
	var row dockerPSStatusRow
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		log.Printf("unexpected docker ps status output: %s", line)
		return dockerPSStatusRow{}, false
	}
	if !row.wellFormed() {
		log.Printf("unexpected docker ps status row: %s", line)
		return dockerPSStatusRow{}, false
	}
	return row, true
}

func (row dockerPSStatusRow) wellFormed() bool {
	return strings.TrimSpace(row.Labels) != "" && strings.TrimSpace(row.State) != ""
}

func parseDockerPSLabels(raw string) map[string]string {
	labels := make(map[string]string)
	for _, field := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		labels[key] = strings.TrimSpace(value)
	}
	return labels
}

func statusServiceNameFromComposeProject(project string) (string, bool) {
	serviceName, ok := strings.CutPrefix(project, "catch-")
	return serviceName, ok && serviceName != ""
}

func parseSystemdShowStatusSnapshot(raw []byte) map[string]svc.Status {
	out := make(map[string]svc.Status)
	current := make(map[string]string)
	flush := func() {
		id := strings.TrimSpace(current["Id"])
		if id != "" {
			out[id] = systemdShowStateStatus(current["LoadState"], current["ActiveState"])
		}
		current = make(map[string]string)
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		current[key] = value
	}
	flush()
	return out
}

func systemdShowStateStatus(loadState, activeState string) svc.Status {
	// The public status model has no failed state; match the old status path by
	// treating known non-active installed systemd states as stopped.
	switch strings.TrimSpace(activeState) {
	case "active":
		return svc.StatusRunning
	}
	if strings.TrimSpace(loadState) == "not-found" {
		return svc.StatusUnknown
	}
	switch strings.TrimSpace(activeState) {
	case "inactive", "failed", "deactivating", "activating":
		return svc.StatusStopped
	}
	return svc.StatusUnknown
}

func collectDockerComposeStatusSnapshot(ctx context.Context, newCmd statusSnapshotCommandContext) (map[string]svc.DockerComposeStatus, error) {
	cmd := newCmd(ctx, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project", "--format", "{{json .}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps status snapshot: %w", err)
	}
	statuses, err := parseDockerComposeStatusSnapshot(out)
	if err != nil {
		return nil, fmt.Errorf("parse docker ps status snapshot: %w", err)
	}
	return statuses, nil
}

func collectSystemdStatusSnapshot(ctx context.Context, newCmd statusSnapshotCommandContext, units []string) (map[string]svc.Status, error) {
	units = sortedUniqueNonEmpty(units)
	if len(units) == 0 {
		return map[string]svc.Status{}, nil
	}
	args := append([]string{"show", "--property=Id,LoadState,ActiveState,SubState"}, units...)
	cmd := newCmd(ctx, "systemctl", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show status snapshot: %w", err)
	}
	return parseSystemdShowStatusSnapshot(out), nil
}

func (s *Server) collectStatusSnapshot(ctx context.Context, newCmd statusSnapshotCommandContext) ([]ServiceStatusData, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get status snapshot services: %w", err)
	}
	services := dv.AsStruct().Services
	dockerStatuses := map[string]svc.DockerComposeStatus{}
	if len(serviceNamesByType(services, db.ServiceTypeDockerCompose)) > 0 {
		dockerStatuses, err = collectDockerComposeStatusSnapshot(ctx, newCmd)
		if err != nil {
			return nil, err
		}
	}
	units, err := s.statusSnapshotUnitNames(dv)
	if err != nil {
		return nil, err
	}
	unitStatuses, err := collectSystemdStatusSnapshot(ctx, newCmd, units)
	if err != nil {
		return nil, err
	}
	return s.buildStatusDataFromSnapshots(dv, dockerStatuses, unitStatuses)
}

func sortedUniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func (s *Server) buildStatusDataFromSnapshots(dv *db.DataView, dockerStatuses map[string]svc.DockerComposeStatus, unitStatuses map[string]svc.Status) ([]ServiceStatusData, error) {
	services := dv.AsStruct().Services
	statuses := make([]ServiceStatusData, 0, len(services))

	for _, name := range serviceNamesByType(services, db.ServiceTypeSystemd) {
		service, err := serviceViewFromDataView(dv, name)
		if err != nil {
			return nil, err
		}
		status, err := s.systemdSnapshotStatusData(service, unitStatuses)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	for _, name := range serviceNamesByType(services, db.ServiceTypeDockerCompose) {
		service, err := serviceViewFromDataView(dv, name)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, composeServiceStatusData(name, ServiceDataTypeForService(service), dockerStatuses[name]))
	}
	for _, name := range serviceNamesByType(services, db.ServiceTypeVM) {
		status := unitStatuses[vmSystemdUnitName(name)]
		if status == "" {
			status = svc.StatusUnknown
		}
		statuses = append(statuses, serviceStatusWithComponent(name, ServiceDataTypeVM, name, status))
	}
	return statuses, nil
}

func (s *Server) systemdSnapshotStatusData(service db.ServiceView, unitStatuses map[string]svc.Status) (ServiceStatusData, error) {
	unit, err := s.primaryUnitForServiceView(service)
	if err != nil {
		return ServiceStatusData{}, err
	}
	status := unitStatuses[unit]
	if status == "" {
		status = svc.StatusUnknown
	}
	name := service.Name()
	return serviceStatusWithComponent(name, ServiceDataTypeForService(service), name, status), nil
}

func serviceViewFromDataView(dv *db.DataView, name string) (db.ServiceView, error) {
	if dv == nil || !dv.Valid() {
		return db.ServiceView{}, fmt.Errorf("db is invalid")
	}
	service, ok := dv.Services().GetOk(name)
	if !ok {
		return db.ServiceView{}, errServiceNotFound
	}
	return service, nil
}

func (s *Server) primaryUnitForServiceView(service db.ServiceView) (string, error) {
	root := s.serviceRootFromView(service)
	systemd, err := svc.NewSystemdService(s.cfg.DB, service, serviceRunDirForRoot(root))
	if err != nil {
		return "", fmt.Errorf("load systemd service %s: %w", service.Name(), err)
	}
	return systemd.PrimaryUnit(), nil
}

func (s *Server) statusSnapshotUnitNames(dv *db.DataView) ([]string, error) {
	services := dv.AsStruct().Services
	units := make([]string, 0)
	for _, name := range serviceNamesByType(services, db.ServiceTypeSystemd) {
		service, err := serviceViewFromDataView(dv, name)
		if err != nil {
			return nil, err
		}
		unit, err := s.primaryUnitForServiceView(service)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	for _, name := range serviceNamesByType(services, db.ServiceTypeVM) {
		units = append(units, vmSystemdUnitName(name))
	}
	return sortedUniqueNonEmpty(units), nil
}
