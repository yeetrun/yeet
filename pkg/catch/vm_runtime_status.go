// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/serviceid"
	"golang.org/x/sys/unix"
)

const (
	vmRuntimeRunningMarkerSchemaVersion = 1
	vmRuntimeRunningMarkerMaxSize       = 64 << 10
)

type vmRuntimeRunningMarker struct {
	SchemaVersion     int    `json:"schemaVersion"`
	Service           string `json:"service"`
	DescriptorSHA256  string `json:"descriptorSHA256"`
	LaunchID          string `json:"launchId"`
	RuntimeID         string `json:"runtimeId"`
	ManifestSHA256    string `json:"manifestSHA256"`
	FirecrackerSHA256 string `json:"firecrackerSHA256"`
	JailerSHA256      string `json:"jailerSHA256"`
	RunnerPID         int    `json:"runnerPid"`
	ChildPID          int    `json:"childPid"`
	StartedAt         string `json:"startedAt"`
}

type vmRuntimeStatusIdentity struct {
	ID                string `json:"id"`
	ManifestSHA256    string `json:"manifestSHA256,omitempty"`
	FirecrackerSHA256 string `json:"firecrackerSHA256"`
	JailerSHA256      string `json:"jailerSHA256"`
	Source            string `json:"source"`
	UpstreamVersion   string `json:"upstreamVersion,omitempty"`
	Support           string `json:"support"`
}

type vmComponentStatusIdentity struct {
	ID               string `json:"id"`
	ManifestSHA256   string `json:"manifestSHA256,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	Source           string `json:"source"`
	Path             string `json:"path,omitempty"`
	RootFSProvenance string `json:"rootfsProvenance,omitempty"`
}

type vmRuntimeStatusRow struct {
	Service           string                    `json:"service"`
	GuestBase         vmComponentStatusIdentity `json:"guestBase"`
	Kernel            vmComponentStatusIdentity `json:"kernel"`
	Running           *vmRuntimeStatusIdentity  `json:"running,omitempty"`
	Configured        vmRuntimeStatusIdentity   `json:"configured"`
	Staged            *vmRuntimeStatusIdentity  `json:"staged,omitempty"`
	Previous          *vmRuntimeStatusIdentity  `json:"previous,omitempty"`
	Policy            string                    `json:"policy"`
	Channel           string                    `json:"channel"`
	LatestPromoted    *vmRuntimeStatusIdentity  `json:"latestPromoted,omitempty"`
	JailerIsolation   string                    `json:"jailerIsolation"`
	State             string                    `json:"state"`
	LastTransition    string                    `json:"lastTransition,omitempty"`
	RecommendedAction string                    `json:"recommendedAction,omitempty"`
}

type vmRuntimeUnitState struct {
	ActiveState string
	MainPID     int
}

type vmRuntimeStatusView uint8

const (
	vmRuntimeStatusFleetView vmRuntimeStatusView = iota
	vmRuntimeStatusDetailView
)

func readVMRuntimeUnitState(ctx context.Context, unit string) (vmRuntimeUnitState, error) {
	active, err := systemctlVMRuntimeAdoptionProperty(ctx, unit, "ActiveState")
	if err != nil {
		return vmRuntimeUnitState{}, err
	}
	rawPID, err := systemctlVMRuntimeAdoptionProperty(ctx, unit, "MainPID")
	if err != nil {
		return vmRuntimeUnitState{}, err
	}
	pid, err := strconv.Atoi(rawPID)
	if err != nil || pid < 0 {
		return vmRuntimeUnitState{}, fmt.Errorf("systemctl MainPID for %s is invalid", unit)
	}
	return vmRuntimeUnitState{ActiveState: active, MainPID: pid}, nil
}

func (s *Server) printVMRuntimeStatus(ctx context.Context, w io.Writer, serviceName, format string) error {
	rows, err := s.vmRuntimeStatusRows(ctx, serviceName)
	if err != nil {
		return err
	}
	view := vmRuntimeStatusFleetView
	if serviceName != "" {
		view = vmRuntimeStatusDetailView
	}
	return renderVMRuntimeStatus(w, format, rows, view)
}

func (s *Server) vmRuntimeStatusRows(ctx context.Context, serviceName string) ([]vmRuntimeStatusRow, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	data := dv.AsStruct()
	names := adoptedVMRuntimeStatusNames(data, serviceName)
	if serviceName != "" && len(names) == 0 {
		return nil, fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return []vmRuntimeStatusRow{}, nil
	}

	deps := s.runtimeCommandDependencies()
	catalog, catalogErr := deps.fetchCatalog(ctx)
	rows := make([]vmRuntimeStatusRow, 0, len(names))
	for _, name := range names {
		service := data.Services[name]
		row, err := s.vmRuntimeStatusRow(ctx, deps, data.VMHost, service, catalog, catalogErr)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func adoptedVMRuntimeStatusNames(data *db.Data, selected string) []string {
	names := make([]string, 0, len(data.Services))
	for name, service := range data.Services {
		if service != nil && service.ServiceType == db.ServiceTypeVM && service.VM != nil && service.VM.Components != nil && (selected == "" || name == selected) {
			names = append(names, name)
		}
	}
	return names
}

func (s *Server) vmRuntimeStatusRow(ctx context.Context, deps vmRuntimeCommandDeps, host *db.VMHostConfig, service *db.Service, catalog vmRuntimeCatalog, catalogErr error) (vmRuntimeStatusRow, error) {
	runtimeState := service.VM.Components.Runtime
	row, err := initialVMRuntimeStatusRow(host, service, catalog, catalogErr)
	if err != nil {
		return vmRuntimeStatusRow{}, err
	}

	root := serviceRootFromConfig(s.cfg, *service)
	jailerAction := classifyVMRuntimeJailerStatus(&row, root, deps)

	unit, err := deps.unitState(ctx, service.Name)
	if err != nil {
		row.State = "unit-status-error"
		row.RecommendedAction = "inspect the VM systemd unit"
		return vmRuntimeFinalizeStatusRow(row, jailerAction), nil
	}
	if unit.ActiveState != "active" {
		return classifyStoppedVMRuntimeStatus(row, jailerAction, root, service.Name, runtimeState, deps, catalogErr), nil
	}
	return classifyActiveVMRuntimeStatus(row, jailerAction, root, service.Name, runtimeState, unit, deps, catalog, catalogErr), nil
}

func initialVMRuntimeStatusRow(host *db.VMHostConfig, service *db.Service, catalog vmRuntimeCatalog, catalogErr error) (vmRuntimeStatusRow, error) {
	runtimeState := service.VM.Components.Runtime
	policy, err := effectiveVMRuntimePolicyFor(host, &runtimeState)
	if err != nil {
		return vmRuntimeStatusRow{}, fmt.Errorf("VM runtime policy for %s: %w", service.Name, err)
	}
	row := vmRuntimeStatusRow{
		Service: service.Name,
		GuestBase: vmComponentStatusIdentity{
			ID: service.VM.Components.GuestBase.ID, ManifestSHA256: service.VM.Components.GuestBase.ManifestSHA256,
			Source: service.VM.Components.GuestBase.Source, RootFSProvenance: service.VM.Components.GuestBase.RootFSProvenance,
		},
		Kernel: vmComponentStatusIdentity{
			ID: service.VM.Components.Kernel.ID, ManifestSHA256: service.VM.Components.Kernel.ManifestSHA256,
			SHA256: service.VM.Components.Kernel.SHA256, Source: service.VM.Components.Kernel.Source, Path: service.VM.Components.Kernel.Path,
		},
		Configured: vmRuntimeStatusIdentityForArtifact(runtimeState.Configured, catalog, catalogErr),
		Policy:     policy.Mode, Channel: policy.Channel, State: "unknown",
	}
	if runtimeState.Staged != nil {
		identity := vmRuntimeStatusIdentityForArtifact(*runtimeState.Staged, catalog, catalogErr)
		row.Staged = &identity
	}
	if runtimeState.Previous != nil {
		identity := vmRuntimeStatusIdentityForArtifact(*runtimeState.Previous, catalog, catalogErr)
		row.Previous = &identity
	}
	if runtimeState.Trial != nil {
		row.LastTransition = runtimeState.Trial.StartedAt
	}
	if catalogErr == nil {
		if latest, ok := catalog.RuntimeForChannel("amd64", policy.Channel); ok {
			identity := vmRuntimeStatusIdentityForCatalog(latest)
			row.LatestPromoted = &identity
		}
	}
	return row, nil
}

func classifyVMRuntimeJailerStatus(row *vmRuntimeStatusRow, root string, deps vmRuntimeCommandDeps) string {
	readiness, err := deps.jailerState(root, deps.expectedUID, deps.expectedGID)
	if err != nil {
		row.JailerIsolation = "error"
		return "repair the host jailer readiness marker"
	}
	row.JailerIsolation = string(readiness)
	if readiness == vmJailerPendingRestart {
		return "restart the VM when downtime is acceptable to activate jailer isolation"
	}
	return ""
}

func classifyStoppedVMRuntimeStatus(row vmRuntimeStatusRow, jailerAction, root, serviceName string, runtimeState db.VMRuntimeLifecycleConfig, deps vmRuntimeCommandDeps, catalogErr error) vmRuntimeStatusRow {
	if terminal, err := trustedVMRuntimeTerminalTrialStatus(root, serviceName, runtimeState, deps.expectedUID, deps.expectedGID); err == nil && terminal != nil {
		row.State = string(terminal.Outcome)
		row.LastTransition = terminal.CompletedAt
		row.RecommendedAction = "inspect the terminal runtime trial result and both host runtime artifacts"
		return vmRuntimeFinalizeStatusRow(row, jailerAction)
	}
	row.State = "stopped"
	vmRuntimeClassifyCatalogState(&row, runtimeState, catalogErr)
	return vmRuntimeFinalizeStatusRow(row, jailerAction)
}

func classifyActiveVMRuntimeStatus(row vmRuntimeStatusRow, jailerAction, root, serviceName string, runtimeState db.VMRuntimeLifecycleConfig, unit vmRuntimeUnitState, deps vmRuntimeCommandDeps, catalog vmRuntimeCatalog, catalogErr error) vmRuntimeStatusRow {
	marker, ok := liveVMRuntimeStatusMarker(&row, root, serviceName, unit, deps)
	if !ok {
		return vmRuntimeFinalizeStatusRow(row, jailerAction)
	}
	artifact, relation, ok := matchVMRuntimeRunningMarker(marker, runtimeState)
	if !ok {
		running := vmRuntimeStatusIdentityForMarker(marker, catalog, catalogErr)
		row.Running = &running
		row.State = "running-config-diverged"
		row.RecommendedAction = "inspect the runtime marker and configured runtime digests"
		return vmRuntimeFinalizeStatusRow(row, jailerAction)
	}
	running := vmRuntimeStatusIdentityForArtifact(artifact, catalog, catalogErr)
	row.Running = &running
	switch relation {
	case "configured":
		row.State = "current"
	case "staged":
		row.State = "trial"
	case "previous":
		row.State = "running-previous"
		row.RecommendedAction = "inspect the most recent runtime trial before retrying"
	}
	vmRuntimeClassifyCatalogState(&row, runtimeState, catalogErr)
	return vmRuntimeFinalizeStatusRow(row, jailerAction)
}

func liveVMRuntimeStatusMarker(row *vmRuntimeStatusRow, root, serviceName string, unit vmRuntimeUnitState, deps vmRuntimeCommandDeps) (vmRuntimeRunningMarker, bool) {
	if unit.MainPID <= 0 || !deps.processAlive(unit.MainPID) {
		row.State = "stale-runner"
		row.RecommendedAction = "inspect the active VM unit and runner process"
		return vmRuntimeRunningMarker{}, false
	}
	markerPath := filepath.Join(serviceRunDirForRoot(root), vmRuntimeRunningMarkerFileName)
	marker, err := readTrustedVMRuntimeRunningMarker(markerPath, serviceName, deps.expectedUID, deps.expectedGID)
	if err != nil {
		row.State = "missing-or-untrusted-marker"
		row.RecommendedAction = "restart the VM when downtime is acceptable to establish a trusted runtime marker"
		return vmRuntimeRunningMarker{}, false
	}
	if marker.RunnerPID != unit.MainPID || !deps.processAlive(marker.RunnerPID) {
		row.State = "stale-runner"
		row.RecommendedAction = "inspect the active VM unit and runtime marker"
		return vmRuntimeRunningMarker{}, false
	}
	if !deps.processAlive(marker.ChildPID) {
		row.State = "stale-child"
		row.RecommendedAction = "inspect the Firecracker child process and active VM unit"
		return vmRuntimeRunningMarker{}, false
	}
	return marker, true
}

func trustedVMRuntimeTerminalTrialStatus(root, service string, runtimeState db.VMRuntimeLifecycleConfig, uid, gid uint32) (*vmRuntimeTrialResult, error) {
	deps := defaultVMRuntimeControlFileDeps()
	deps.uid = uid
	deps.gid = gid
	result, _, err := readTrustedVMRuntimeTrialResult(filepath.Join(serviceRunDirForRoot(root), vmRuntimeTrialResultFileName), service, deps)
	if err != nil {
		return nil, err
	}
	if result.Outcome != vmRuntimeTrialFailedNoFallback || runtimeState.Staged == nil ||
		!result.Candidate.matches(*runtimeState.Staged) || !result.Configured.matches(runtimeState.Configured) {
		return nil, fmt.Errorf("terminal VM runtime trial result does not match current selections")
	}
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(
		filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName), service, uid, gid,
	)
	if err != nil || snapshot.SHA256 != result.DescriptorSHA256 {
		return nil, errors.Join(err, fmt.Errorf("terminal VM runtime trial result generation is stale"))
	}
	return &result, nil
}

func vmRuntimeFinalizeStatusRow(row vmRuntimeStatusRow, extraAction string) vmRuntimeStatusRow {
	extraAction = strings.TrimSpace(extraAction)
	if extraAction == "" {
		return row
	}
	if row.RecommendedAction == "" {
		row.RecommendedAction = extraAction
	} else if row.RecommendedAction != extraAction {
		row.RecommendedAction += "; " + extraAction
	}
	return row
}

func vmRuntimeClassifyCatalogState(row *vmRuntimeStatusRow, runtimeState db.VMRuntimeLifecycleConfig, catalogErr error) {
	switch row.State {
	case "current", "stopped", "unknown":
	default:
		return
	}
	if runtimeState.Trial != nil && strings.TrimSpace(runtimeState.Trial.State) != "" {
		classifyVMRuntimeTrialStatus(row, runtimeState.Trial)
		return
	}
	if catalogErr != nil {
		classifyUnavailableVMRuntimeCatalog(row)
		return
	}
	classifyVMRuntimeCatalogSupport(row)
}

func classifyVMRuntimeTrialStatus(row *vmRuntimeStatusRow, trial *db.VMRuntimeTrialConfig) {
	row.State = trial.State
	if trial.LastError != "" {
		row.RecommendedAction = "inspect the failed runtime trial and rollback state"
	}
}

func classifyUnavailableVMRuntimeCatalog(row *vmRuntimeStatusRow) {
	if row.State == "current" || row.State == "unknown" {
		row.State = "catalog-unavailable"
	}
	row.RecommendedAction = "retry after the trusted runtime catalog is available"
}

func classifyVMRuntimeCatalogSupport(row *vmRuntimeStatusRow) {
	switch row.Configured.Support {
	case "revoked":
		row.State = "revoked"
		row.RecommendedAction = "stage a supported runtime and restart when downtime is acceptable"
	case "eol", "deprecated":
		row.State = row.Configured.Support
		row.RecommendedAction = "stage the promoted runtime"
	case "legacy-unlisted":
		row.State = "legacy-unlisted"
		row.RecommendedAction = "stage an official promoted runtime"
	case "local":
		row.State = "local"
	case "unlisted":
		row.State = "unlisted"
		row.RecommendedAction = "inspect the configured runtime identity before changing it"
	default:
		if row.LatestPromoted != nil && !vmRuntimeStatusIdentityEqual(row.Configured, *row.LatestPromoted) {
			row.State = "update-available"
			row.RecommendedAction = "run yeet vm runtime upgrade " + row.Service
		}
	}
}

func vmRuntimeStatusIdentityForArtifact(artifact db.VMRuntimeArtifactConfig, catalog vmRuntimeCatalog, catalogErr error) vmRuntimeStatusIdentity {
	identity := vmRuntimeStatusIdentity{
		ID: artifact.ID, ManifestSHA256: artifact.ManifestSHA256,
		FirecrackerSHA256: artifact.FirecrackerSHA256, JailerSHA256: artifact.JailerSHA256,
		Source: artifact.Source,
	}
	if catalogErr != nil {
		identity.Support = "catalog-unavailable"
		return identity
	}
	if artifact.Source != "official" {
		identity.Support = "legacy-unlisted"
		if strings.HasPrefix(artifact.Source, "local:") {
			identity.Support = "local"
		}
		return identity
	}
	ref, ok := catalog.RuntimeByID("amd64", artifact.ID)
	if !ok || ref.ManifestSHA != artifact.ManifestSHA256 {
		identity.Support = "unlisted"
		return identity
	}
	identity.UpstreamVersion = ref.UpstreamVersion
	identity.Support = ref.Support
	return identity
}

func vmRuntimeStatusIdentityForCatalog(ref vmRuntimeCatalogRef) vmRuntimeStatusIdentity {
	return vmRuntimeStatusIdentity{
		ID: ref.RuntimeID, ManifestSHA256: ref.ManifestSHA, Source: "official",
		UpstreamVersion: ref.UpstreamVersion, Support: ref.Support,
	}
}

func vmRuntimeStatusIdentityForMarker(marker vmRuntimeRunningMarker, catalog vmRuntimeCatalog, catalogErr error) vmRuntimeStatusIdentity {
	identity := vmRuntimeStatusIdentity{
		ID: marker.RuntimeID, ManifestSHA256: marker.ManifestSHA256,
		FirecrackerSHA256: marker.FirecrackerSHA256, JailerSHA256: marker.JailerSHA256,
		Source: "host-marker", Support: "unlisted",
	}
	if catalogErr != nil {
		identity.Support = "catalog-unavailable"
		return identity
	}
	if ref, ok := catalog.RuntimeByID("amd64", marker.RuntimeID); ok && ref.ManifestSHA == marker.ManifestSHA256 {
		identity.UpstreamVersion = ref.UpstreamVersion
		identity.Support = ref.Support
	}
	return identity
}

func vmRuntimeStatusIdentityEqual(left, right vmRuntimeStatusIdentity) bool {
	return left.ID == right.ID && left.ManifestSHA256 == right.ManifestSHA256
}

func vmRuntimeStatusRuntimeEquivalent(left, right vmRuntimeStatusIdentity) bool {
	return vmRuntimeStatusIdentityEqual(left, right) &&
		left.FirecrackerSHA256 == right.FirecrackerSHA256 && left.JailerSHA256 == right.JailerSHA256
}

func matchVMRuntimeRunningMarker(marker vmRuntimeRunningMarker, runtimeState db.VMRuntimeLifecycleConfig) (db.VMRuntimeArtifactConfig, string, bool) {
	candidates := []struct {
		artifact *db.VMRuntimeArtifactConfig
		relation string
	}{
		{artifact: &runtimeState.Configured, relation: "configured"},
		{artifact: runtimeState.Staged, relation: "staged"},
		{artifact: runtimeState.Previous, relation: "previous"},
	}
	for _, candidate := range candidates {
		if candidate.artifact != nil && vmRuntimeMarkerMatchesArtifact(marker, *candidate.artifact) {
			return *candidate.artifact, candidate.relation, true
		}
	}
	return db.VMRuntimeArtifactConfig{}, "", false
}

func vmRuntimeMarkerMatchesArtifact(marker vmRuntimeRunningMarker, artifact db.VMRuntimeArtifactConfig) bool {
	return marker.RuntimeID == artifact.ID && marker.ManifestSHA256 == artifact.ManifestSHA256 &&
		marker.FirecrackerSHA256 == artifact.FirecrackerSHA256 && marker.JailerSHA256 == artifact.JailerSHA256
}

func readTrustedVMRuntimeRunningMarker(path, service string, uid, gid uint32) (vmRuntimeRunningMarker, error) {
	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(path), uid)
	if err != nil {
		return vmRuntimeRunningMarker{}, fmt.Errorf("open VM runtime marker parent: %w", err)
	}
	defer func() { _ = dir.Close() }()
	name := filepath.Base(path)
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return vmRuntimeRunningMarker{}, fmt.Errorf("open VM runtime marker without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return vmRuntimeRunningMarker{}, fmt.Errorf("bind VM runtime marker file descriptor")
	}
	id, stat, err := validateOpenVMRuntimeMarker(file, uid, gid)
	if err != nil {
		return vmRuntimeRunningMarker{}, closeVMJailerFileOnError(file, err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, vmRuntimeRunningMarkerMaxSize+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return vmRuntimeRunningMarker{}, errors.Join(err, closeErr)
	}
	if len(raw) > vmRuntimeRunningMarkerMaxSize {
		return vmRuntimeRunningMarker{}, fmt.Errorf("VM runtime marker exceeds %d bytes", vmRuntimeRunningMarkerMaxSize)
	}
	if err := validateVMRuntimeDescriptorName(dir, name, id, stat, uid, gid); err != nil {
		return vmRuntimeRunningMarker{}, fmt.Errorf("VM runtime marker changed while it was read: %w", err)
	}
	return decodeVMRuntimeRunningMarker(raw, service)
}

func validateOpenVMRuntimeMarker(file *os.File, uid, gid uint32) (vmJailerFileIdentity, unix.Stat_t, error) {
	id, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, err
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("VM runtime marker is not a regular file")
	}
	if stat.Uid != uid || stat.Gid != gid {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("VM runtime marker owner is %d:%d, want %d:%d", stat.Uid, stat.Gid, uid, gid)
	}
	if uint32(stat.Mode)&0o777 != 0o600 {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("VM runtime marker permissions are %o, want 0600", uint32(stat.Mode)&0o777)
	}
	return id, stat, nil
}

func decodeVMRuntimeRunningMarker(raw []byte, expectedService string) (vmRuntimeRunningMarker, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return vmRuntimeRunningMarker{}, fmt.Errorf("decode VM runtime marker: %w", err)
	}
	if err := validateVMRuntimeRunningMarkerFields(fields); err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	marker, err := decodeVMRuntimeRunningMarkerStrict(raw)
	if err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	if err := validateVMRuntimeRunningMarker(marker, expectedService); err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	return marker, nil
}

func validateVMRuntimeRunningMarkerFields(fields map[string]json.RawMessage) error {
	required := []string{"schemaVersion", "service", "descriptorSHA256", "launchId", "runtimeId", "manifestSHA256", "firecrackerSHA256", "jailerSHA256", "runnerPid", "childPid", "startedAt"}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("VM runtime marker missing required field %q", name)
		}
	}
	for name := range fields {
		if !slices.Contains(required, name) {
			return fmt.Errorf("VM runtime marker contains unknown field %q", name)
		}
	}
	return nil
}

func decodeVMRuntimeRunningMarkerStrict(raw []byte) (vmRuntimeRunningMarker, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var marker vmRuntimeRunningMarker
	if err := decoder.Decode(&marker); err != nil {
		return vmRuntimeRunningMarker{}, fmt.Errorf("decode VM runtime marker: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return vmRuntimeRunningMarker{}, err
	}
	return marker, nil
}

func validateVMRuntimeRunningMarker(marker vmRuntimeRunningMarker, expectedService string) error {
	if err := validateVMRuntimeRunningMarkerIdentity(marker, expectedService); err != nil {
		return err
	}
	if err := validateVMRuntimeRunningMarkerDigests(marker); err != nil {
		return err
	}
	return validateVMRuntimeRunningMarkerProcess(marker)
}

func validateVMRuntimeRunningMarkerIdentity(marker vmRuntimeRunningMarker, expectedService string) error {
	if marker.SchemaVersion != vmRuntimeRunningMarkerSchemaVersion {
		return fmt.Errorf("unsupported VM runtime marker schemaVersion %d", marker.SchemaVersion)
	}
	if err := serviceid.Validate(marker.Service); err != nil || marker.Service != expectedService {
		return fmt.Errorf("VM runtime marker service %q does not match %q", marker.Service, expectedService)
	}
	if !isLowerSHA256(marker.DescriptorSHA256) || !isLowerSHA256(marker.LaunchID) {
		return fmt.Errorf("VM runtime marker generation or launch ID is invalid")
	}
	if strings.TrimSpace(marker.RuntimeID) == "" || marker.RuntimeID != strings.TrimSpace(marker.RuntimeID) {
		return fmt.Errorf("VM runtime marker runtimeId is invalid")
	}
	return nil
}

func validateVMRuntimeRunningMarkerDigests(marker vmRuntimeRunningMarker) error {
	if marker.ManifestSHA256 != "" && !isLowerSHA256(marker.ManifestSHA256) {
		return fmt.Errorf("VM runtime marker manifestSHA256 is invalid")
	}
	if !isLowerSHA256(marker.FirecrackerSHA256) || !isLowerSHA256(marker.JailerSHA256) {
		return fmt.Errorf("VM runtime marker component digest is invalid")
	}
	return nil
}

func validateVMRuntimeRunningMarkerProcess(marker vmRuntimeRunningMarker) error {
	if marker.RunnerPID <= 0 || marker.ChildPID <= 0 {
		return fmt.Errorf("VM runtime marker process IDs are invalid")
	}
	if _, err := time.Parse(time.RFC3339, marker.StartedAt); err != nil {
		return fmt.Errorf("VM runtime marker startedAt is invalid: %w", err)
	}
	return nil
}

func renderVMRuntimeStatus(w io.Writer, format string, rows []vmRuntimeStatusRow, view vmRuntimeStatusView) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(rows)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case "", "table":
		if view == vmRuntimeStatusDetailView {
			return renderVMRuntimeStatusDetail(w, rows)
		}
		return renderVMRuntimeStatusFleet(w, rows)
	default:
		return fmt.Errorf("unsupported VM runtime status format %q", format)
	}
}

const vmRuntimeStatusHumanWidth = 100

func renderVMRuntimeStatusDetail(w io.Writer, rows []vmRuntimeStatusRow) error {
	for index, row := range rows {
		if index > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := renderVMRuntimeStatusDetailRow(w, row); err != nil {
			return err
		}
	}
	return nil
}

func renderVMRuntimeStatusDetailRow(w io.Writer, row vmRuntimeStatusRow) error {
	if err := writeVMRuntimeStatusDetailHeader(w, row); err != nil {
		return err
	}
	if err := writeVMRuntimeStatusDetailComponents(w, row); err != nil {
		return err
	}
	if err := writeVMRuntimeStatusDetailRuntime(w, row); err != nil {
		return err
	}
	if err := writeVMRuntimeStatusDetailMetadata(w, row); err != nil {
		return err
	}
	return writeVMRuntimeStatusDetailAction(w, row.RecommendedAction)
}

func writeVMRuntimeStatusDetailHeader(w io.Writer, row vmRuntimeStatusRow) error {
	_, err := fmt.Fprintf(w, "%s  %s\n\n", vmRuntimeStatusServiceDisplay(row.Service), vmRuntimeStatusHumanState(row.State))
	return err
}

func writeVMRuntimeStatusDetailComponents(w io.Writer, row vmRuntimeStatusRow) error {
	section := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(section, "  Guest base:\t%s\n  Kernel:\t%s\n",
		vmRuntimeStatusComponentSummary(row.GuestBase), vmRuntimeStatusComponentSummary(row.Kernel)); err != nil {
		return err
	}
	return section.Flush()
}

func writeVMRuntimeStatusDetailRuntime(w io.Writer, row vmRuntimeStatusRow) error {
	if _, err := fmt.Fprintln(w, "\n  Runtime"); err != nil {
		return err
	}
	running := vmRuntimeStatusRuntimeDetail(row.Running, true)
	configured := vmRuntimeStatusRuntimeDetail(&row.Configured, true)
	if row.Running != nil && vmRuntimeStatusRuntimeEquivalent(*row.Running, row.Configured) {
		configured = "same as running"
	}
	section := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(section, "    Running:\t%s\n    Configured:\t%s\n    Staged:\t%s\n    Previous:\t%s\n",
		running, configured, vmRuntimeStatusRuntimeDetail(row.Staged, true), vmRuntimeStatusRuntimeDetail(row.Previous, true)); err != nil {
		return err
	}
	return section.Flush()
}

func writeVMRuntimeStatusDetailMetadata(w io.Writer, row vmRuntimeStatusRow) error {
	section := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
	if _, err := fmt.Fprintf(section, "\n  Policy:\t%s\n  Promoted:\t%s\n  Isolation:\t%s\n  Last change:\t%s\n",
		vmRuntimeStatusPolicyDisplay(row), vmRuntimeStatusRuntimeDetail(row.LatestPromoted, false),
		vmRuntimeStatusValueOrDash(row.JailerIsolation), vmRuntimeStatusTransitionDisplay(row.LastTransition)); err != nil {
		return err
	}
	return section.Flush()
}

func writeVMRuntimeStatusDetailAction(w io.Writer, action string) error {
	if action == "" {
		return nil
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return writeVMRuntimeStatusWrapped(w, "  Action: ", sentenceCaseVMRuntimeStatusAction(action))
}

func vmRuntimeStatusTransitionDisplay(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return vmRuntimeStatusBoundedID(value)
	}
	return parsed.UTC().Format("2006-01-02 15:04:05 UTC")
}

func vmRuntimeStatusValueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return vmRuntimeStatusBoundedID(value)
}

func writeVMRuntimeStatusWrapped(w io.Writer, prefix, value string) error {
	continuation := strings.Repeat(" ", len([]rune(prefix)))
	width := vmRuntimeStatusHumanWidth - len([]rune(prefix))
	if width < 20 {
		width = 20
	}
	for index, line := range vmRuntimeStatusWrapWords(value, width) {
		linePrefix := prefix
		if index > 0 {
			linePrefix = continuation
		}
		if _, err := fmt.Fprintln(w, linePrefix+line); err != nil {
			return err
		}
	}
	return nil
}

func vmRuntimeStatusWrapWords(value string, width int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	current := ""
	for _, word := range words {
		wordRunes := []rune(word)
		if len(wordRunes) > width {
			if current != "" {
				lines = append(lines, current)
			}
			for len(wordRunes) > width {
				lines = append(lines, string(wordRunes[:width]))
				wordRunes = wordRunes[width:]
			}
			current = string(wordRunes)
			continue
		}
		if current == "" {
			current = word
			continue
		}
		if len([]rune(current))+1+len([]rune(word)) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func renderVMRuntimeStatusFleet(w io.Writer, rows []vmRuntimeStatusRow) error {
	if err := writeVMRuntimeStatusFleetTable(w, rows); err != nil {
		return err
	}
	if err := writeVMRuntimeStatusFleetPromotion(w, rows); err != nil {
		return err
	}
	return writeVMRuntimeStatusFleetActions(w, vmRuntimeStatusActionRows(rows))
}

func writeVMRuntimeStatusFleetTable(w io.Writer, rows []vmRuntimeStatusRow) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "VM\tRUNNING\tCONFIGURED\tSTAGED\tPOLICY\tSTATE"); err != nil {
		return err
	}
	for _, row := range rows {
		running := "unverified"
		if row.Running != nil {
			running = vmRuntimeStatusRuntimeSummary(row.Running)
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n",
			vmRuntimeStatusServiceDisplay(row.Service), running, vmRuntimeStatusRuntimeSummary(&row.Configured),
			vmRuntimeStatusRuntimeSummary(row.Staged), vmRuntimeStatusPolicySummary(row),
			vmRuntimeStatusHumanState(row.State)); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	return nil
}

func writeVMRuntimeStatusFleetPromotion(w io.Writer, rows []vmRuntimeStatusRow) error {
	if promoted, channel, ok := sharedVMRuntimeStatusPromotion(rows); ok {
		_, err := fmt.Fprintf(w, "\nPromoted %s runtime: %s\n", vmRuntimeStatusBoundedID(channel), vmRuntimeStatusPromotionSummary(promoted))
		return err
	}
	return nil
}

func vmRuntimeStatusActionRows(rows []vmRuntimeStatusRow) []vmRuntimeStatusRow {
	actions := make([]vmRuntimeStatusRow, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.RecommendedAction) != "" {
			actions = append(actions, row)
		}
	}
	return actions
}

func writeVMRuntimeStatusFleetActions(w io.Writer, actions []vmRuntimeStatusRow) error {
	if len(actions) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\nNeeds attention:"); err != nil {
		return err
	}
	for _, row := range actions {
		if err := writeVMRuntimeStatusWrapped(w, "  "+vmRuntimeStatusServiceDisplay(row.Service)+": ", sentenceCaseVMRuntimeStatusAction(row.RecommendedAction)); err != nil {
			return err
		}
	}
	return nil
}

func sharedVMRuntimeStatusPromotion(rows []vmRuntimeStatusRow) (*vmRuntimeStatusIdentity, string, bool) {
	if len(rows) == 0 || rows[0].LatestPromoted == nil || rows[0].Channel == "" {
		return nil, "", false
	}
	want := rows[0].LatestPromoted
	channel := rows[0].Channel
	for _, row := range rows[1:] {
		if row.LatestPromoted == nil || row.Channel != channel || !vmRuntimeStatusIdentityEqual(*want, *row.LatestPromoted) {
			return nil, "", false
		}
	}
	return want, channel, true
}

const (
	vmRuntimeStatusFingerprintLength = 12
	vmRuntimeStatusFallbackIDMax     = 48
	vmRuntimeStatusSummaryMax        = 48
	vmRuntimeStatusHumanLabelMax     = 64
	vmRuntimeStatusMetadataMax       = 24
	vmRuntimeStatusVersionPartMax    = 10
)

func vmRuntimeStatusRuntimeSummary(identity *vmRuntimeStatusIdentity) string {
	if identity == nil {
		return "-"
	}
	label := strings.TrimPrefix(vmRuntimeStatusRuntimeVersion(identity), "v")
	if label == "" {
		label = vmRuntimeStatusBoundedID(identity.ID)
	}
	if source := vmRuntimeStatusSourceSummary(identity.Source); source != "" {
		return vmRuntimeStatusCompleteLabelWithin(label, vmRuntimeStatusSummaryMax, vmRuntimeStatusBoundedText(source, vmRuntimeStatusMetadataMax))
	}
	return vmRuntimeStatusCompleteLabelWithin(label, vmRuntimeStatusSummaryMax)
}

func vmRuntimeStatusRuntimeDetail(identity *vmRuntimeStatusIdentity, includeFingerprint bool) string {
	if identity == nil {
		return "-"
	}
	label := vmRuntimeStatusRuntimeRelease(identity)
	if label == "" {
		label = vmRuntimeStatusBoundedID(identity.ID)
	}
	qualifiers := []string{}
	if includeFingerprint && isLowerSHA256(identity.ManifestSHA256) {
		qualifiers = append(qualifiers, "["+identity.ManifestSHA256[:vmRuntimeStatusFingerprintLength]+"]")
	}
	metadata := []string{}
	if source := vmRuntimeStatusSourceDetail(identity.Source); source != "" {
		metadata = append(metadata, source)
	}
	if support := vmRuntimeStatusSupportDetail(identity.Support); support != "" {
		metadata = append(metadata, support)
	}
	if len(metadata) > 0 {
		metadataLabel := vmRuntimeStatusBoundedText(strings.Join(metadata, ", "), vmRuntimeStatusMetadataMax)
		qualifiers = append(qualifiers, "("+metadataLabel+")")
	}
	return vmRuntimeStatusCompleteLabel(label, qualifiers...)
}

func vmRuntimeStatusPromotionSummary(identity *vmRuntimeStatusIdentity) string {
	if identity == nil {
		return "-"
	}
	label := vmRuntimeStatusRuntimeRelease(identity)
	if label != "" {
		return vmRuntimeStatusCompleteLabel(strings.TrimPrefix(label, "v"))
	}
	return vmRuntimeStatusCompleteLabel(vmRuntimeStatusBoundedID(identity.ID))
}

func vmRuntimeStatusRuntimeVersion(identity *vmRuntimeStatusIdentity) string {
	if identity == nil {
		return ""
	}
	if version := strings.TrimSpace(identity.UpstreamVersion); vmRuntimeStatusReasonableVersion(version) {
		return version
	}
	if match := legacyVMRuntimePolicyIDPattern.FindStringSubmatch(identity.ID); vmRuntimeStatusReasonableLegacyMatch(match) {
		return "v" + strings.Join(match[1:], ".")
	}
	if match := officialVMRuntimePolicyIDPattern.FindStringSubmatch(identity.ID); vmRuntimeStatusReasonableOfficialMatch(match) {
		return match[1]
	}
	return ""
}

func vmRuntimeStatusRuntimeRelease(identity *vmRuntimeStatusIdentity) string {
	if identity == nil {
		return ""
	}
	if match := officialVMRuntimePolicyIDPattern.FindStringSubmatch(identity.ID); vmRuntimeStatusReasonableOfficialMatch(match) {
		return match[1] + " / yeet-v" + match[2]
	}
	if match := legacyVMRuntimePolicyIDPattern.FindStringSubmatch(identity.ID); vmRuntimeStatusReasonableLegacyMatch(match) {
		return "v" + strings.Join(match[1:], ".")
	}
	return ""
}

func vmRuntimeStatusComponentSummary(identity vmComponentStatusIdentity) string {
	if strings.TrimSpace(identity.ID) == "" {
		return "-"
	}
	label := vmRuntimeStatusTrimDigestSuffix(identity.ID)
	if strings.HasPrefix(label, "legacy-guest-") {
		label = vmRuntimeStatusCollapseRepeatedPrefix(strings.TrimPrefix(label, "legacy-guest-"))
	} else if strings.HasPrefix(label, "legacy-kernel-") {
		label = strings.TrimPrefix(label, "legacy-kernel-")
	} else if strings.HasPrefix(label, "guest-") {
		label = strings.TrimPrefix(label, "guest-")
	} else if strings.HasPrefix(label, "kernel-") {
		label = strings.TrimPrefix(label, "kernel-")
	}
	label = vmRuntimeStatusBoundedID(label)
	if source := vmRuntimeStatusSourceDetail(identity.Source); source != "" {
		return vmRuntimeStatusCompleteLabel(label, "("+vmRuntimeStatusBoundedText(source, vmRuntimeStatusMetadataMax)+")")
	}
	return vmRuntimeStatusCompleteLabel(label)
}

func vmRuntimeStatusTrimDigestSuffix(value string) string {
	const suffixLength = 65
	if len(value) >= suffixLength && value[len(value)-suffixLength] == '-' && isLowerSHA256(value[len(value)-64:]) {
		return value[:len(value)-suffixLength]
	}
	return value
}

func vmRuntimeStatusCollapseRepeatedPrefix(value string) string {
	for offset := strings.IndexByte(value, '-'); offset >= 0; {
		prefix := value[:offset]
		if strings.HasPrefix(value[offset+1:], prefix+"-") {
			return value[offset+1:]
		}
		next := strings.IndexByte(value[offset+1:], '-')
		if next < 0 {
			break
		}
		offset += next + 1
	}
	return value
}

func vmRuntimeStatusBoundedID(value string) string {
	return vmRuntimeStatusBoundedText(value, vmRuntimeStatusFallbackIDMax)
}

func vmRuntimeStatusServiceDisplay(value string) string {
	value = vmRuntimeStatusSingleLine(value)
	if serviceid.Validate(value) == nil {
		return value
	}
	return vmRuntimeStatusBoundedID(value)
}

func vmRuntimeStatusBoundedText(value string, limit int) string {
	value = vmRuntimeStatusSingleLine(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func vmRuntimeStatusSingleLine(value string) string {
	value = strings.ToValidUTF8(value, "�")
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

func vmRuntimeStatusCompleteLabel(base string, qualifiers ...string) string {
	return vmRuntimeStatusCompleteLabelWithin(base, vmRuntimeStatusHumanLabelMax, qualifiers...)
}

func vmRuntimeStatusCompleteLabelWithin(base string, limit int, qualifiers ...string) string {
	base = vmRuntimeStatusSingleLine(base)
	clean := make([]string, 0, len(qualifiers))
	for _, qualifier := range qualifiers {
		if qualifier = vmRuntimeStatusSingleLine(qualifier); qualifier != "" {
			clean = append(clean, qualifier)
		}
	}
	if len(clean) == 0 {
		return vmRuntimeStatusBoundedText(base, limit)
	}
	qualifier := strings.Join(clean, " ")
	maxQualifier := limit - 4
	if len([]rune(qualifier)) > maxQualifier {
		qualifier = vmRuntimeStatusBoundedText(qualifier, maxQualifier)
	}
	if base == "" {
		return qualifier
	}
	baseLimit := limit - 1 - len([]rune(qualifier))
	return vmRuntimeStatusBoundedText(base, baseLimit) + " " + qualifier
}

func vmRuntimeStatusReasonableVersion(version string) bool {
	if !vmRuntimeVersionPattern.MatchString(version) {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(version, "v"), ".") {
		if len(part) > vmRuntimeStatusVersionPartMax {
			return false
		}
	}
	return true
}

func vmRuntimeStatusReasonableOfficialMatch(match []string) bool {
	return len(match) == 3 && vmRuntimeStatusReasonableVersion(match[1]) && len(match[2]) <= vmRuntimeStatusVersionPartMax
}

func vmRuntimeStatusReasonableLegacyMatch(match []string) bool {
	if len(match) != 4 {
		return false
	}
	for _, part := range match[1:] {
		if len(part) > vmRuntimeStatusVersionPartMax {
			return false
		}
	}
	return true
}

func vmRuntimeStatusSourceSummary(source string) string {
	switch {
	case source == "official":
		return "official"
	case strings.HasSuffix(source, "-legacy"):
		return "legacy"
	case strings.HasPrefix(source, "local:"):
		return "local"
	default:
		return vmRuntimeStatusWords(source)
	}
}

func vmRuntimeStatusSourceDetail(source string) string {
	if strings.HasPrefix(source, "local:") {
		return "local"
	}
	return vmRuntimeStatusWords(source)
}

func vmRuntimeStatusSupportDetail(support string) string {
	switch support {
	case "", "legacy-unlisted", "local":
		return ""
	default:
		return vmRuntimeStatusWords(support)
	}
}

func vmRuntimeStatusWords(value string) string {
	return vmRuntimeStatusBoundedID(strings.ReplaceAll(vmRuntimeStatusSingleLine(value), "-", " "))
}

func vmRuntimeStatusHumanState(state string) string {
	switch state {
	case "":
		return "unknown"
	case "current":
		return "healthy"
	case "missing-or-untrusted-marker":
		return "marker unverified"
	case "running-config-diverged":
		return "config diverged"
	case "failed-rolled-back":
		return "failed, rolled back"
	default:
		return vmRuntimeStatusWords(state)
	}
}

func vmRuntimeStatusPolicySummary(row vmRuntimeStatusRow) string {
	if row.Policy == "" && row.Channel == "" {
		return "-"
	}
	if row.Channel == "" {
		return vmRuntimeStatusCompleteLabelWithin(row.Policy, vmRuntimeStatusSummaryMax)
	}
	if row.Policy == "" {
		return vmRuntimeStatusCompleteLabelWithin(row.Channel, vmRuntimeStatusSummaryMax)
	}
	return vmRuntimeStatusCompleteLabelWithin(vmRuntimeStatusSingleLine(row.Policy)+"/"+vmRuntimeStatusSingleLine(row.Channel), vmRuntimeStatusSummaryMax)
}

func vmRuntimeStatusPolicyDisplay(row vmRuntimeStatusRow) string {
	if row.Policy == "" && row.Channel == "" {
		return "-"
	}
	if row.Channel == "" {
		return vmRuntimeStatusCompleteLabel(row.Policy)
	}
	if row.Policy == "" {
		return vmRuntimeStatusCompleteLabel(row.Channel)
	}
	return vmRuntimeStatusCompleteLabel(vmRuntimeStatusSingleLine(row.Policy) + " / " + vmRuntimeStatusSingleLine(row.Channel))
}

func sentenceCaseVMRuntimeStatusAction(action string) string {
	action = vmRuntimeStatusSingleLine(action)
	if action == "" {
		return ""
	}
	runes := []rune(action)
	runes[0] = unicode.ToUpper(runes[0])
	if runes[len(runes)-1] != '.' {
		runes = append(runes, '.')
	}
	return string(runes)
}
