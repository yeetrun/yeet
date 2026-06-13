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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

type vmFirecrackerSnapshotter interface {
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	CreateFullSnapshot(context.Context, string, string, string) error
}

var (
	vmSnapshotIsRunning                            = (*Server).IsServiceRunning
	vmSnapshotFirecracker vmFirecrackerSnapshotter = firecrackerSnapshotAPI{}
)

const vmSnapshotRecoveryTimeout = 30 * time.Second

type vmSnapshotResult struct {
	Name       string
	StatePath  string
	MemoryPath string
}

type vmSnapshotPlan struct {
	Service  *db.Service
	VM       db.VMConfig
	Dataset  string
	Policy   effectivePolicy
	Flags    cli.VMSnapshotFlags
	Running  bool
	Socket   string
	Snapshot vmFirecrackerSnapshotter
}

func (s *Server) createVMSnapshot(ctx context.Context, name string, flags cli.VMSnapshotFlags, w io.Writer) error {
	plan, err := s.newVMSnapshotPlan(name, flags)
	if err != nil {
		return err
	}
	result, err := s.executeVMSnapshotPlan(ctx, name, plan)
	if err != nil {
		return err
	}
	pruned, err := s.pruneServiceSnapshotsForDataset(ctx, plan.Dataset, plan.Service, plan.Policy, time.Now(), result.Name)
	if err != nil {
		writeSnapshotWarning(w, "warning: failed to prune VM snapshots for %q: %v\n", name, err)
	}
	if err := s.pruneVMCheckpointDirsForSnapshots(plan.Service, pruned); err != nil {
		writeSnapshotWarning(w, "warning: failed to prune VM checkpoint files for %q: %v\n", name, err)
	}
	printVMSnapshotResult(w, result)
	return nil
}

func (s *Server) newVMSnapshotPlan(name string, flags cli.VMSnapshotFlags) (vmSnapshotPlan, error) {
	service, vm, err := s.vmSnapshotService(name)
	if err != nil {
		return vmSnapshotPlan{}, err
	}
	dataset, err := vmSnapshotDataset(vm.Disk)
	if err != nil {
		return vmSnapshotPlan{}, err
	}
	policy, err := s.serviceSnapshotPolicy(service)
	if err != nil {
		return vmSnapshotPlan{}, err
	}
	if !policy.Enabled {
		return vmSnapshotPlan{}, fmt.Errorf("VM snapshots are disabled for %q; enable snapshots for the service or inherit enabled defaults", name)
	}
	running, err := currentVMSnapshotRunning(s, name)
	if err != nil {
		return vmSnapshotPlan{}, err
	}
	socket := strings.TrimSpace(vm.Sockets.APISocketPath)
	if err := validateVMSnapshotRuntime(name, flags, running, socket); err != nil {
		return vmSnapshotPlan{}, err
	}
	return vmSnapshotPlan{
		Service:  service,
		VM:       vm,
		Dataset:  dataset,
		Policy:   policy,
		Flags:    flags,
		Running:  running,
		Socket:   socket,
		Snapshot: currentVMSnapshotController(),
	}, nil
}

func currentVMSnapshotRunning(s *Server, name string) (bool, error) {
	runningCheck := vmSnapshotIsRunning
	if runningCheck == nil {
		runningCheck = (*Server).IsServiceRunning
	}
	return runningCheck(s, name)
}

func validateVMSnapshotRuntime(name string, flags cli.VMSnapshotFlags, running bool, socket string) error {
	if flags.Full && !running {
		return fmt.Errorf("full VM checkpoints require %q to be running", name)
	}
	if running && socket == "" {
		return fmt.Errorf("service %q has no Firecracker API socket", name)
	}
	return nil
}

func currentVMSnapshotController() vmFirecrackerSnapshotter {
	if vmSnapshotFirecracker != nil {
		return vmSnapshotFirecracker
	}
	return firecrackerSnapshotAPI{}
}

func (s *Server) executeVMSnapshotPlan(ctx context.Context, name string, plan vmSnapshotPlan) (vmSnapshotResult, error) {
	if !plan.Running {
		return s.createPausedVMSnapshot(ctx, plan.Service, plan.VM, plan.Dataset, plan.Flags, plan.Snapshot)
	}
	if err := plan.Snapshot.Pause(ctx, plan.Socket); err != nil {
		return vmSnapshotResult{}, fmt.Errorf("pause VM %q: %w", name, err)
	}
	result, snapErr := s.createPausedVMSnapshot(ctx, plan.Service, plan.VM, plan.Dataset, plan.Flags, plan.Snapshot)
	resumeCtx, cancel := vmSnapshotRecoveryContext(ctx)
	defer cancel()
	resumeErr := plan.Snapshot.Resume(resumeCtx, plan.Socket)
	return finishVMSnapshotResume(name, result, snapErr, resumeErr)
}

func vmSnapshotRecoveryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), vmSnapshotRecoveryTimeout)
}

func finishVMSnapshotResume(name string, result vmSnapshotResult, snapErr, resumeErr error) (vmSnapshotResult, error) {
	if resumeErr != nil && snapErr != nil {
		return vmSnapshotResult{}, fmt.Errorf("%v; additionally failed to resume VM %q: %w", snapErr, name, resumeErr)
	}
	if resumeErr != nil {
		return vmSnapshotResult{}, fmt.Errorf("created VM snapshot %s but failed to resume VM %q: %w", result.Name, name, resumeErr)
	}
	if snapErr != nil {
		return vmSnapshotResult{}, snapErr
	}
	return result, nil
}

func (s *Server) vmSnapshotService(name string) (*db.Service, db.VMConfig, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, db.VMConfig{}, err
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		return nil, db.VMConfig{}, fmt.Errorf("service %q not found", name)
	}
	service := sv.AsStruct()
	if service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return nil, db.VMConfig{}, fmt.Errorf("service %q is not a VM service", name)
	}
	if strings.TrimSpace(service.VM.SetupState) != "ready" {
		return nil, db.VMConfig{}, fmt.Errorf("VM %q is not ready", name)
	}
	return service, *service.VM.Clone(), nil
}

func vmSnapshotDataset(disk db.VMDiskConfig) (string, error) {
	if disk.Backend != vmDiskBackendZVOL {
		return "", fmt.Errorf("VM snapshot requires a ZFS zvol-backed VM")
	}
	dataset := strings.TrimPrefix(strings.TrimSpace(disk.Path), "/dev/zvol/")
	dataset = strings.TrimPrefix(dataset, "/")
	if dataset == "" {
		return "", fmt.Errorf("VM zvol path is required")
	}
	return dataset, nil
}

func (s *Server) createPausedVMSnapshot(ctx context.Context, service *db.Service, vm db.VMConfig, dataset string, flags cli.VMSnapshotFlags, controller vmFirecrackerSnapshotter) (vmSnapshotResult, error) {
	now := time.Now()
	checkpoint := "disk"
	if flags.Full {
		checkpoint = "full"
	}
	name, err := createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service:    service.Name,
		Dataset:    dataset,
		Event:      snapshotEventVMManual,
		Generation: service.Generation,
		Now:        now,
		Comment:    flags.Comment,
		Checkpoint: checkpoint,
	})
	if err != nil {
		return vmSnapshotResult{}, err
	}

	result := vmSnapshotResult{Name: name}
	if !flags.Full {
		return result, nil
	}

	dir := vmCheckpointDir(s.serviceRootFromService(service), vmSnapshotShortName(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result, s.failFullVMSnapshot(ctx, name, "", fmt.Errorf("create VM checkpoint directory: %w", err))
	}
	result.StatePath = filepath.Join(dir, "firecracker-state.bin")
	result.MemoryPath = filepath.Join(dir, "memory.bin")
	if err := controller.CreateFullSnapshot(ctx, vm.Sockets.APISocketPath, result.StatePath, result.MemoryPath); err != nil {
		return result, s.failFullVMSnapshot(ctx, name, dir, fmt.Errorf("create full VM checkpoint: %w", err))
	}
	if err := writeVMCheckpointMetadata(dir, service.Name, flags.Comment, name, result, now); err != nil {
		return result, s.failFullVMSnapshot(ctx, name, dir, err)
	}
	return result, nil
}

func (s *Server) failFullVMSnapshot(ctx context.Context, snapshotName string, checkpointDir string, cause error) error {
	cleanupCtx, cancel := vmSnapshotRecoveryContext(ctx)
	defer cancel()
	var cleanupErrs []error
	if snapshotName != "" {
		if err := destroySnapshot(cleanupCtx, s.zfsRunner, snapshotName); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("destroy incomplete VM zvol snapshot %s: %w", snapshotName, err))
		}
	}
	if checkpointDir != "" {
		if err := os.RemoveAll(checkpointDir); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove incomplete VM checkpoint directory %s: %w", checkpointDir, err))
		}
	}
	if cleanupErr := errors.Join(cleanupErrs...); cleanupErr != nil {
		return fmt.Errorf("create full VM checkpoint for snapshot %s: %w; cleanup failed: %w", snapshotName, cause, cleanupErr)
	}
	return fmt.Errorf("create full VM checkpoint for snapshot %s: %w", snapshotName, cause)
}

func (s *Server) serviceRootFromService(service *db.Service) string {
	if service == nil {
		return s.defaultServiceRootDir("")
	}
	if strings.TrimSpace(service.ServiceRoot) != "" {
		return service.ServiceRoot
	}
	return s.defaultServiceRootDir(service.Name)
}

func vmCheckpointDir(root string, shortName string) string {
	return filepath.Join(serviceDataDirForRoot(root), "checkpoints", shortName)
}

func (s *Server) pruneVMCheckpointDirsForSnapshots(service *db.Service, snapshotNames []string) error {
	if len(snapshotNames) == 0 {
		return nil
	}
	root := s.serviceRootFromService(service)
	var errs []error
	for _, snapshotName := range snapshotNames {
		dir := vmCheckpointDir(root, vmSnapshotShortName(snapshotName))
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove VM checkpoint directory %s: %w", dir, err))
		}
	}
	return errors.Join(errs...)
}

func vmSnapshotShortName(name string) string {
	if idx := strings.LastIndex(name, "@"); idx >= 0 && idx+1 < len(name) {
		return name[idx+1:]
	}
	return name
}

func printVMSnapshotResult(w io.Writer, result vmSnapshotResult) {
	if w == nil {
		return
	}
	writef(w, "VM snapshot: %s\n", result.Name)
	if result.StatePath != "" {
		writef(w, "Firecracker state: %s\n", result.StatePath)
		writef(w, "Firecracker memory: %s\n", result.MemoryPath)
	}
}

type firecrackerSnapshotAPI struct{}

func (firecrackerSnapshotAPI) Pause(ctx context.Context, socket string) error {
	return firecrackerPatchVMState(ctx, socket, "Paused")
}

func (firecrackerSnapshotAPI) Resume(ctx context.Context, socket string) error {
	return firecrackerPatchVMState(ctx, socket, "Resumed")
}

func (firecrackerSnapshotAPI) CreateFullSnapshot(ctx context.Context, socket string, statePath string, memPath string) error {
	body := map[string]string{
		"snapshot_type": "Full",
		"snapshot_path": statePath,
		"mem_file_path": memPath,
	}
	return firecrackerJSON(ctx, socket, http.MethodPut, "http://unix/snapshot/create", body)
}

func firecrackerPatchVMState(ctx context.Context, socket string, state string) error {
	return firecrackerJSON(ctx, socket, http.MethodPatch, "http://unix/vm", map[string]string{"state": state})
}

func firecrackerJSON(ctx context.Context, socket string, method string, url string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("firecracker API %s %s returned %s", method, url, resp.Status)
	}
	return nil
}

func writeVMCheckpointMetadata(dir string, service string, comment string, zvolSnapshot string, result vmSnapshotResult, created time.Time) error {
	metadata := map[string]string{
		"service":           service,
		"comment":           strings.TrimSpace(comment),
		"zvolSnapshot":      zvolSnapshot,
		"firecrackerState":  result.StatePath,
		"firecrackerMemory": result.MemoryPath,
		"createdBy":         "catch",
		"createdAt":         created.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0o644)
}
