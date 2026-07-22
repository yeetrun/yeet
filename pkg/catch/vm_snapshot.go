// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
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

type vmFirecrackerPauser interface {
	Pause(context.Context, string) error
	Resume(context.Context, string) error
}

var (
	vmSnapshotIsRunning                       = (*Server).IsServiceRunning
	vmSnapshotFirecracker vmFirecrackerPauser = firecrackerSnapshotAPI{}
	vmSnapshotDiskFlusher                     = flushVMSnapshotDisk
)

const vmSnapshotRecoveryTimeout = 30 * time.Second

type vmSnapshotResult struct {
	Name string
}

type vmSnapshotPlan struct {
	Service  *db.Service
	Dataset  string
	Policy   effectivePolicy
	Flags    cli.SnapshotsCreateFlags
	Running  bool
	Socket   string
	DiskPath string
	Snapshot vmFirecrackerPauser
}

func (s *Server) createVMSnapshot(ctx context.Context, name string, flags cli.SnapshotsCreateFlags, w io.Writer) error {
	plan, err := s.newVMSnapshotPlan(name, flags)
	if err != nil {
		return err
	}
	result, err := s.executeVMSnapshotPlan(ctx, name, plan)
	if err != nil {
		return err
	}
	if _, err := s.pruneServiceSnapshotsForDataset(ctx, plan.Dataset, plan.Service, plan.Policy, time.Now(), result.Name); err != nil {
		writeSnapshotWarning(w, "warning: failed to prune VM snapshots for %q: %v\n", name, err)
	}
	printVMSnapshotResult(w, result)
	return nil
}

func (s *Server) newVMSnapshotPlan(name string, flags cli.SnapshotsCreateFlags) (vmSnapshotPlan, error) {
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
	if running && socket == "" {
		return vmSnapshotPlan{}, fmt.Errorf("service %q has no Firecracker API socket", name)
	}
	return vmSnapshotPlan{Service: service, Dataset: dataset, Policy: policy, Flags: flags, Running: running, Socket: socket, DiskPath: vm.Disk.Path, Snapshot: currentVMSnapshotController()}, nil
}

func currentVMSnapshotRunning(s *Server, name string) (bool, error) {
	runningCheck := vmSnapshotIsRunning
	if runningCheck == nil {
		runningCheck = (*Server).IsServiceRunning
	}
	return runningCheck(s, name)
}

func currentVMSnapshotController() vmFirecrackerPauser {
	if vmSnapshotFirecracker != nil {
		return vmSnapshotFirecracker
	}
	return firecrackerSnapshotAPI{}
}

func (s *Server) executeVMSnapshotPlan(ctx context.Context, name string, plan vmSnapshotPlan) (vmSnapshotResult, error) {
	if !plan.Running {
		return s.createPausedVMSnapshot(ctx, plan.Service, plan.Dataset, plan.Flags)
	}
	if err := plan.Snapshot.Pause(ctx, plan.Socket); err != nil {
		return vmSnapshotResult{}, fmt.Errorf("pause VM %q: %w", name, err)
	}
	flushErr := currentVMSnapshotDiskFlusher()(plan.DiskPath)
	var result vmSnapshotResult
	var snapErr error
	if flushErr != nil {
		snapErr = fmt.Errorf("flush VM %q disk before snapshot: %w", name, flushErr)
	} else {
		result, snapErr = s.createPausedVMSnapshot(ctx, plan.Service, plan.Dataset, plan.Flags)
	}
	resumeCtx, cancel := vmSnapshotRecoveryContext(ctx)
	defer cancel()
	resumeErr := plan.Snapshot.Resume(resumeCtx, plan.Socket)
	return finishVMSnapshotResume(name, result, snapErr, resumeErr)
}

func currentVMSnapshotDiskFlusher() func(string) error {
	if vmSnapshotDiskFlusher != nil {
		return vmSnapshotDiskFlusher
	}
	return flushVMSnapshotDisk
}

func flushVMSnapshotDisk(path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if !strings.HasPrefix(path, "/dev/zvol/") || path == "/dev/zvol" {
		return fmt.Errorf("VM snapshot disk is not a ZFS zvol device: %s", path)
	}
	disk, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open VM snapshot disk %s: %w", path, err)
	}
	defer disk.Close()
	info, err := disk.Stat()
	if err != nil {
		return fmt.Errorf("inspect VM snapshot disk %s: %w", path, err)
	}
	if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		return fmt.Errorf("VM snapshot disk is not a block device: %s", path)
	}
	if err := disk.Sync(); err != nil {
		return fmt.Errorf("fsync VM snapshot disk %s: %w", path, err)
	}
	return nil
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

func (s *Server) createPausedVMSnapshot(ctx context.Context, service *db.Service, dataset string, flags cli.SnapshotsCreateFlags) (vmSnapshotResult, error) {
	name, err := createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service: service.Name, Dataset: dataset, Event: snapshotEventVMManual,
		Now: time.Now(), Comment: flags.Comment, Checkpoint: recoveryModeDisk,
	})
	if err != nil {
		return vmSnapshotResult{}, err
	}
	return vmSnapshotResult{Name: name}, nil
}

// createVMRuntimeUpgradeRecoveryPoint creates a disk-only recovery point
// independently of the service snapshot policy. It is born protected so a
// concurrent retention pass cannot prune it before the runtime trial finishes.
func (s *Server) createVMRuntimeUpgradeRecoveryPoint(ctx context.Context, service *db.Service, w io.Writer) (string, error) {
	if service == nil || service.VM == nil {
		return "", fmt.Errorf("VM runtime upgrade requires a VM service")
	}
	if service.VM.Disk.Backend != vmDiskBackendZVOL {
		writeSnapshotWarning(w, "warning: VM %q uses a raw disk; only launcher rollback is available for this runtime trial\n", service.Name)
		return "", nil
	}
	dataset, err := vmSnapshotDataset(service.VM.Disk)
	if err != nil {
		return "", err
	}
	running, err := currentVMSnapshotRunning(s, service.Name)
	if err != nil {
		return "", err
	}
	socket := strings.TrimSpace(service.VM.Sockets.APISocketPath)
	if running && socket == "" {
		return "", fmt.Errorf("service %q has no Firecracker API socket", service.Name)
	}
	create := func(snapshotCtx context.Context) (string, error) {
		return createServiceSnapshot(snapshotCtx, s.zfsRunner, snapshotCreateRequest{
			Service: service.Name, Dataset: dataset, Event: snapshotEventVMRuntimeUpgrade,
			Now: time.Now(), Checkpoint: recoveryModeDisk, Protected: true,
		})
	}
	if !running {
		return create(ctx)
	}
	return createPausedVMRuntimeUpgradeRecoveryPoint(ctx, service.Name, socket, service.VM.Disk.Path, create)
}

func createPausedVMRuntimeUpgradeRecoveryPoint(ctx context.Context, serviceName, socket, diskPath string, create func(context.Context) (string, error)) (string, error) {
	controller := currentVMSnapshotController()
	if err := controller.Pause(ctx, socket); err != nil {
		return "", fmt.Errorf("pause VM %q before runtime upgrade recovery point: %w", serviceName, err)
	}
	flushErr := currentVMSnapshotDiskFlusher()(diskPath)
	var name string
	var snapshotErr error
	if flushErr != nil {
		snapshotErr = fmt.Errorf("flush VM %q disk before runtime upgrade recovery point: %w", serviceName, flushErr)
	} else {
		name, snapshotErr = create(ctx)
	}
	resumeCtx, cancel := vmSnapshotRecoveryContext(ctx)
	defer cancel()
	resumeErr := controller.Resume(resumeCtx, socket)
	if resumeErr != nil && snapshotErr != nil {
		return "", fmt.Errorf("%v; additionally failed to resume VM %q: %w", snapshotErr, serviceName, resumeErr)
	}
	if resumeErr != nil {
		return "", fmt.Errorf("created protected VM runtime recovery point %s but failed to resume VM %q: %w", name, serviceName, resumeErr)
	}
	if snapshotErr != nil {
		return "", snapshotErr
	}
	return name, nil
}

func vmSnapshotShortName(name string) string {
	if idx := strings.LastIndex(name, "@"); idx >= 0 && idx+1 < len(name) {
		return name[idx+1:]
	}
	return name
}

func printVMSnapshotResult(w io.Writer, result vmSnapshotResult) {
	if w != nil {
		writef(w, "VM snapshot: %s\n", result.Name)
	}
}

type firecrackerSnapshotAPI struct{}

func (firecrackerSnapshotAPI) Pause(ctx context.Context, socket string) error {
	return firecrackerPatchVMState(ctx, socket, "Paused")
}

func (firecrackerSnapshotAPI) Resume(ctx context.Context, socket string) error {
	return firecrackerPatchVMState(ctx, socket, "Resumed")
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
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}}
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
