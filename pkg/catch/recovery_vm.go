// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

var (
	vmRestoreTempSuffixFunc   = generateRandomSnapshotSuffix
	vmRestoreCopyRunner       = copyVMRestoreZVOL
	vmRestoreZVOLDeviceWaiter = waitForVMRestoreZVOLDevices

	vmFullRestoreResultWaitTimeout  = 30 * time.Second
	vmFullRestoreResultWaitInterval = 100 * time.Millisecond
	vmRestoreZVOLDeviceWaitTimeout  = 10 * time.Second
	vmRestoreZVOLDeviceWaitInterval = 50 * time.Millisecond
)

func (s *Server) cloneVMRecoveryPoint(ctx context.Context, service *db.Service, point recoveryPoint, newServiceName string, flags cli.SnapshotsCloneFlags, w io.Writer) error {
	if err := validateVMRecoveryPoint(service, point); err != nil {
		return err
	}
	if err := validateVMCloneStart(flags.Start); err != nil {
		return err
	}
	newServiceName, targetDataset, err := s.planVMRecoveryClone(service, point, newServiceName)
	if err != nil {
		return err
	}
	if err := s.requireVMCloneTargetDatasetAvailable(ctx, targetDataset); err != nil {
		return err
	}
	createdParentDatasets, err := s.ensureVMCloneTargetParentDatasets(ctx, targetDataset)
	if err != nil {
		return err
	}
	if err := zfsCloneSnapshot(ctx, s.zfsRunner, point.Name, targetDataset); err != nil {
		if len(createdParentDatasets) > 0 {
			return vmCloneParentCleanupError(createdParentDatasets, err, destroyVMCloneParentDatasets(ctx, s.zfsRunner, createdParentDatasets))
		}
		return err
	}

	clonedService := cloneVMRecoveryService(service, newServiceName, targetDataset)
	inserted, err := s.insertRecoveryCloneService(clonedService)
	if err != nil {
		return s.cleanupFailedVMClone(ctx, targetDataset, createdParentDatasets, newServiceName, inserted, err)
	}
	writef(w, "Created VM service: %s (stopped).\n", newServiceName)
	writef(w, "Cloned VM disk: %s\n", targetDataset)
	return nil
}

func validateVMCloneStart(start bool) error {
	if start {
		return fmt.Errorf("starting VM clones is not supported yet; run snapshots clone without --start")
	}
	return nil
}

func (s *Server) planVMRecoveryClone(service *db.Service, point recoveryPoint, newServiceName string) (string, string, error) {
	newServiceName = strings.TrimSpace(newServiceName)
	if err := validateRecoveryCloneServiceName(newServiceName); err != nil {
		return "", "", err
	}
	if err := s.requireRecoveryCloneTargetAvailable(newServiceName); err != nil {
		return "", "", err
	}
	targetDataset, err := vmRecoveryCloneDataset(point.Dataset, service.Name, newServiceName)
	if err != nil {
		return "", "", err
	}
	return newServiceName, targetDataset, nil
}

func (s *Server) restoreVMRecoveryPoint(ctx context.Context, service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
	if err := validateVMRecoveryPoint(service, point); err != nil {
		return err
	}
	if err := validateVMRestoreMode(flags.Mode); err != nil {
		return err
	}
	if normalizedVMRestoreMode(flags.Mode) == recoveryModeFull {
		return s.restoreFullVMRecoveryPoint(ctx, service, point, flags, rw)
	}
	return s.restoreDiskVMRecoveryPoint(ctx, service, point, flags, rw)
}

func (s *Server) restoreDiskVMRecoveryPoint(ctx context.Context, service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
	confirmed, err := confirmVMRestore(service, point, flags, rw)
	if err != nil || !confirmed {
		return err
	}
	running, err := s.vmRestoreRunningState(service.Name, flags.Stop)
	if err != nil {
		return err
	}
	if err := stopVMForRestore(service.Name, running, flags.Stop, rw); err != nil {
		return err
	}

	preRestore, err := s.createPreRestoreVMSnapshot(ctx, service, point, rw)
	if err != nil {
		return err
	}
	writef(rw, "Pre-restore recovery point: %s\n", preRestore)
	if err := s.restoreVMZVOLFromSnapshot(ctx, point); err != nil {
		return err
	}
	writef(rw, "Restored VM disk: %s\n", point.Name)
	if err := startVMAfterRestore(service.Name, flags.Start, rw); err != nil {
		return err
	}
	writef(rw, "Restore complete.\n")
	return nil
}

func validateVMRecoveryPoint(service *db.Service, point recoveryPoint) error {
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return fmt.Errorf("service %q is not a VM service", point.Service)
	}
	if point.StorageKind != recoveryStorageVMZVOL || point.ServiceType != string(db.ServiceTypeVM) {
		return fmt.Errorf("recovery point %s is not a VM zvol recovery point", point.ShortName)
	}
	return nil
}

func validateVMRestoreMode(raw string) error {
	mode := strings.TrimSpace(raw)
	if mode == "" || mode == recoveryModeDisk {
		return nil
	}
	if mode == recoveryModeFull {
		return nil
	}
	return fmt.Errorf("unsupported VM restore mode %q", mode)
}

func normalizedVMRestoreMode(raw string) string {
	mode := strings.TrimSpace(raw)
	if mode == "" {
		return recoveryModeDisk
	}
	return mode
}

func (s *Server) restoreFullVMRecoveryPoint(ctx context.Context, service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
	metadata, err := s.planFullVMRestore(service, point)
	if err != nil {
		return err
	}
	confirmed, err := confirmFullVMRestore(service, point, flags, rw)
	if err != nil || !confirmed {
		return err
	}
	running, err := s.vmRestoreRunningState(service.Name, flags.Stop)
	if err != nil {
		return err
	}
	if err := stopVMForRestore(service.Name, running, flags.Stop, rw); err != nil {
		return err
	}

	preRestore, err := s.restoreFullVMDiskAndScheduleState(ctx, service, point, metadata, rw)
	if err != nil {
		return err
	}
	if err := startVMAfterRestore(service.Name, true, rw); err != nil {
		return fmt.Errorf("start VM %s for full restore from %s failed after disk restore; pre-restore recovery point: %s: %w", service.Name, point.ShortName, preRestore, err)
	}
	if err := s.waitForFullVMStateRestore(ctx, service, preRestore); err != nil {
		return err
	}
	writef(rw, "Restored full VM state: %s\n", point.ShortName)
	writef(rw, "Restore complete.\n")
	return nil
}

func (s *Server) restoreFullVMDiskAndScheduleState(ctx context.Context, service *db.Service, point recoveryPoint, metadata vmCheckpointMetadata, rw io.ReadWriter) (string, error) {
	preRestore, err := s.createPreRestoreVMSnapshot(ctx, service, point, rw)
	if err != nil {
		return "", err
	}
	writef(rw, "Pre-restore recovery point: %s\n", preRestore)
	preparedRestore, err := prepareFullVMStateRestore(service, metadata)
	if err != nil {
		return preRestore, fmt.Errorf("prepare full VM state restore; pre-restore recovery point: %s: %w", preRestore, err)
	}
	defer preparedRestore.cleanup()
	if err := s.restoreVMZVOLFromSnapshot(ctx, point); err != nil {
		return "", err
	}
	writef(rw, "Restored VM disk: %s\n", point.Name)
	if err := preparedRestore.publish(); err != nil {
		return preRestore, fullVMPostDiskRestoreError(point, preRestore, err)
	}
	writef(rw, "Scheduled full VM state restore: %s\n", point.ShortName)
	return preRestore, nil
}

func fullVMPostDiskRestoreError(point recoveryPoint, preRestore string, err error) error {
	return fmt.Errorf("full VM restore from %s failed after disk restore; pre-restore recovery point: %s: %w", point.ShortName, preRestore, err)
}

func (s *Server) planFullVMRestore(service *db.Service, point recoveryPoint) (vmCheckpointMetadata, error) {
	if point.Mode != recoveryModeFull {
		return vmCheckpointMetadata{}, fmt.Errorf("recovery point %s is not a full VM checkpoint", point.ShortName)
	}
	metadata, err := s.fullVMRestoreMetadata(service, point)
	if err != nil {
		return vmCheckpointMetadata{}, err
	}
	current, err := s.vmCheckpointCompatibility(service, *service.VM.Clone())
	if err != nil {
		return vmCheckpointMetadata{}, err
	}
	if err := validateFullVMCheckpointCompatibility(metadata, current); err != nil {
		return vmCheckpointMetadata{}, err
	}
	return metadata, nil
}

func (s *Server) fullVMRestoreMetadata(service *db.Service, point recoveryPoint) (vmCheckpointMetadata, error) {
	dir := vmCheckpointDir(s.serviceRootFromService(service), point.ShortName)
	metadata, ok := readVMCheckpointMetadata(filepath.Join(dir, "metadata.json"), service.Name, point.Name)
	if !ok || !metadata.hasFullCompatibilityFields() {
		return vmCheckpointMetadata{}, fmt.Errorf("full checkpoint metadata is missing compatibility fields")
	}
	if !checkpointFilesExist(metadata.FirecrackerState, metadata.FirecrackerMemory) {
		return vmCheckpointMetadata{}, fmt.Errorf("full checkpoint state or memory file is missing")
	}
	return metadata, nil
}

func validateFullVMCheckpointCompatibility(metadata vmCheckpointMetadata, current vmCheckpointCompatibility) error {
	if err := validateFullVMCheckpointShape(metadata, current); err != nil {
		return err
	}
	if err := validateFullVMCheckpointDiskPath(metadata, current); err != nil {
		return err
	}
	if err := validateFullVMCheckpointConfigHashes(metadata, current); err != nil {
		return err
	}
	return validateFullVMCheckpointFirecrackerIdentity(metadata, current)
}

func validateFullVMCheckpointShape(metadata vmCheckpointMetadata, current vmCheckpointCompatibility) error {
	if metadata.VCPU != current.VCPU || metadata.MemoryMiB != current.MemoryMiB {
		return fmt.Errorf("checkpoint CPU or memory does not match current VM config")
	}
	return nil
}

func validateFullVMCheckpointDiskPath(metadata vmCheckpointMetadata, current vmCheckpointCompatibility) error {
	if strings.TrimSpace(metadata.DiskPath) != strings.TrimSpace(current.DiskPath) {
		return fmt.Errorf("checkpoint disk path does not match current VM config")
	}
	return nil
}

func validateFullVMCheckpointConfigHashes(metadata vmCheckpointMetadata, current vmCheckpointCompatibility) error {
	if strings.TrimSpace(metadata.MachineConfigHash) != strings.TrimSpace(current.MachineConfigHash) {
		return fmt.Errorf("checkpoint machine config hash does not match current Firecracker config")
	}
	if strings.TrimSpace(metadata.NetworkConfigHash) != strings.TrimSpace(current.NetworkConfigHash) {
		return fmt.Errorf("checkpoint network config hash does not match current Firecracker config")
	}
	if strings.TrimSpace(metadata.VMConfigHash) != strings.TrimSpace(current.VMConfigHash) {
		return fmt.Errorf("checkpoint VM config hash does not match current VM config")
	}
	return nil
}

func validateFullVMCheckpointFirecrackerIdentity(metadata vmCheckpointMetadata, current vmCheckpointCompatibility) error {
	currentSHA := strings.TrimSpace(current.FirecrackerSha256)
	currentVersion := stableFirecrackerVersionLine(current.FirecrackerVersion)
	metadataSHA := strings.TrimSpace(metadata.FirecrackerSha256)
	metadataVersion := stableFirecrackerVersionLine(metadata.FirecrackerVersion)
	if currentSHA != "" && metadataSHA == "" {
		return fmt.Errorf("full checkpoint metadata is missing compatibility fields")
	}
	if currentVersion != "" && metadataVersion == "" {
		return fmt.Errorf("full checkpoint metadata is missing compatibility fields")
	}
	if currentSHA != "" && metadataSHA != currentSHA {
		return fmt.Errorf("checkpoint Firecracker binary hash does not match current launcher")
	}
	if currentVersion != "" && metadataVersion != currentVersion {
		return fmt.Errorf("checkpoint Firecracker version does not match current launcher")
	}
	return nil
}

func confirmVMRestore(service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) (bool, error) {
	if flags.Yes {
		return true, nil
	}
	ok, err := cmdutil.Confirm(rw, rw, fmt.Sprintf("Restore VM disk %s from %s?", service.Name, point.ShortName))
	if err != nil {
		return false, fmt.Errorf("failed to confirm VM disk restore: %w", err)
	}
	if !ok {
		writef(rw, "Restore cancelled.\n")
		return false, nil
	}
	return true, nil
}

type preparedFullVMStateRestore struct {
	requestPath       string
	stagedRequestPath string
}

func prepareFullVMStateRestore(service *db.Service, metadata vmCheckpointMetadata) (*preparedFullVMStateRestore, error) {
	if service == nil || service.VM == nil {
		return nil, fmt.Errorf("VM service is required for full restore")
	}
	apiSocket := strings.TrimSpace(service.VM.Sockets.APISocketPath)
	if apiSocket == "" {
		return nil, fmt.Errorf("VM %s has no Firecracker API socket", service.Name)
	}
	if err := ensureVMSystemdRestorePrevent(service.Name); err != nil {
		return nil, err
	}
	if err := removeVMFullRestoreResult(vmFullRestoreResultPath(apiSocket)); err != nil {
		return nil, err
	}
	requestPath := vmFullRestoreRequestPath(apiSocket)
	if err := os.Remove(requestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale full restore request: %w", err)
	}
	request := vmFullRestoreRequest{
		StatePath:  metadata.FirecrackerState,
		MemoryPath: metadata.FirecrackerMemory,
		Resume:     true,
	}
	stagedRequestPath, err := stageVMFullRestoreRequest(requestPath, request)
	if err != nil {
		return nil, err
	}
	return &preparedFullVMStateRestore{
		requestPath:       requestPath,
		stagedRequestPath: stagedRequestPath,
	}, nil
}

func stageVMFullRestoreRequest(requestPath string, request vmFullRestoreRequest) (string, error) {
	dir := filepath.Dir(requestPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create full restore request directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".firecracker-restore-*.json")
	if err != nil {
		return "", fmt.Errorf("stage full restore request: %w", err)
	}
	stagedPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(stagedPath)
		return "", fmt.Errorf("stage full restore request: %w", err)
	}
	if err := writeVMFullRestoreRequest(stagedPath, request); err != nil {
		_ = os.Remove(stagedPath)
		return "", err
	}
	return stagedPath, nil
}

func (p *preparedFullVMStateRestore) publish() error {
	if p == nil || p.stagedRequestPath == "" {
		return fmt.Errorf("full restore request is not staged")
	}
	if err := os.Rename(p.stagedRequestPath, p.requestPath); err != nil {
		return fmt.Errorf("publish full restore request: %w", err)
	}
	p.stagedRequestPath = ""
	return nil
}

func (p *preparedFullVMStateRestore) cleanup() {
	if p == nil || p.stagedRequestPath == "" {
		return
	}
	_ = os.Remove(p.stagedRequestPath)
}

func (s *Server) waitForFullVMStateRestore(ctx context.Context, service *db.Service, preRestore string) error {
	if service == nil || service.VM == nil {
		return fmt.Errorf("VM service is required for full restore")
	}
	apiSocket := strings.TrimSpace(service.VM.Sockets.APISocketPath)
	if apiSocket == "" {
		return fmt.Errorf("VM %s has no Firecracker API socket", service.Name)
	}
	resultPath := vmFullRestoreResultPath(apiSocket)
	deadline := time.Now().Add(vmFullRestoreResultWaitTimeout)
	runner := &vmRunner{name: service.Name}
	for {
		result, ok, err := readVMFullRestoreResult(resultPath)
		if err != nil {
			if !ok {
				return fmt.Errorf("full VM state restore did not report completion; pre-restore recovery point: %s: %w", preRestore, err)
			}
		}
		if ok && err == nil {
			if result.Status == vmFullRestoreStatusSuccess {
				return nil
			}
			message := strings.TrimSpace(result.Error)
			if message == "" {
				message = "unknown restore-load failure"
			}
			return fmt.Errorf("full VM state restore failed after disk restore; pre-restore recovery point: %s: %s", preRestore, message)
		}
		status, err := runner.Status()
		if err == nil && status != svc.StatusRunning {
			return fmt.Errorf("full VM state restore did not report completion and VM is %s; pre-restore recovery point: %s", status, preRestore)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("full VM state restore did not report completion; pre-restore recovery point: %s: %w", preRestore, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("full VM state restore did not report completion; pre-restore recovery point: %s", preRestore)
		}
		time.Sleep(vmFullRestoreResultWaitInterval)
	}
}

func confirmFullVMRestore(service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) (bool, error) {
	if flags.Yes {
		return true, nil
	}
	ok, err := cmdutil.Confirm(rw, rw, fmt.Sprintf("Restore full VM state %s from %s?", service.Name, point.ShortName))
	if err != nil {
		return false, fmt.Errorf("failed to confirm full VM restore: %w", err)
	}
	if !ok {
		writef(rw, "Restore cancelled.\n")
		return false, nil
	}
	return true, nil
}

func (s *Server) vmRestoreRunningState(name string, stop bool) (bool, error) {
	running, err := s.IsServiceRunning(name)
	if err != nil {
		return false, err
	}
	if running && !stop {
		return false, fmt.Errorf("VM %s is running; pass --stop to stop it before restore", name)
	}
	return running, nil
}

func stopVMForRestore(name string, running bool, stop bool, w io.Writer) error {
	if !running || !stop {
		return nil
	}
	runner := &vmRunner{name: name}
	if err := runner.Stop(); err != nil {
		return err
	}
	writef(w, "Stopped service: %s\n", name)
	return nil
}

func startVMAfterRestore(name string, start bool, w io.Writer) error {
	if !start {
		return nil
	}
	runner := &vmRunner{name: name}
	if err := runner.Start(); err != nil {
		return err
	}
	writef(w, "Started service: %s\n", name)
	return nil
}

func (s *Server) createPreRestoreVMSnapshot(ctx context.Context, service *db.Service, point recoveryPoint, w io.Writer) (string, error) {
	flags := cli.VMSnapshotFlags{Comment: "pre-restore before " + point.ShortName}
	vm := *service.VM.Clone()
	dataset, err := vmSnapshotDataset(vm.Disk)
	if err != nil {
		return "", err
	}
	result, err := s.createPausedVMSnapshot(ctx, service, vm, dataset, flags, currentVMSnapshotController(), nil, false)
	return result.Name, err
}

func (s *Server) restoreVMZVOLFromSnapshot(ctx context.Context, point recoveryPoint) error {
	tempDataset, err := vmRestoreTempDataset(point.Dataset)
	if err != nil {
		return err
	}
	if err := zfsCloneSnapshot(ctx, s.zfsRunner, point.Name, tempDataset); err != nil {
		return err
	}

	if err := s.validateVMRestoreZVOLSizes(ctx, tempDataset, point.Dataset); err != nil {
		return vmRestoreTempCleanupError(tempDataset, err, zfsDestroyDataset(ctx, s.zfsRunner, tempDataset))
	}

	sourceDevice := zvolDevicePath(tempDataset)
	targetDevice := zvolDevicePath(point.Dataset)
	if err := currentVMRestoreZVOLDeviceWaiter()(ctx, sourceDevice, targetDevice); err != nil {
		return vmRestoreTempCleanupError(tempDataset, err, zfsDestroyDataset(ctx, s.zfsRunner, tempDataset))
	}

	copyErr := currentVMRestoreCopyRunner()(ctx, sourceDevice, targetDevice)
	destroyErr := zfsDestroyDataset(ctx, s.zfsRunner, tempDataset)
	if copyErr != nil {
		return vmRestoreTempCleanupError(tempDataset, copyErr, destroyErr)
	}
	if destroyErr != nil {
		return fmt.Errorf("destroy temporary VM restore dataset %s: %w", tempDataset, destroyErr)
	}
	return nil
}

func (s *Server) validateVMRestoreZVOLSizes(ctx context.Context, tempDataset string, activeDataset string) error {
	tempSize, err := zfsZVOLSizeBytes(ctx, s.zfsRunner, tempDataset)
	if err != nil {
		return err
	}
	activeSize, err := zfsZVOLSizeBytes(ctx, s.zfsRunner, activeDataset)
	if err != nil {
		return err
	}
	if tempSize != activeSize {
		return fmt.Errorf("VM zvol size mismatch: temporary restore dataset %s is %d bytes, active dataset %s is %d bytes", tempDataset, tempSize, activeDataset, activeSize)
	}
	return nil
}

func vmRestoreTempCleanupError(tempDataset string, cause error, destroyErr error) error {
	if destroyErr != nil {
		return fmt.Errorf("%w; cleanup failed: destroy temporary VM restore dataset %s: %w", cause, tempDataset, destroyErr)
	}
	return cause
}

func zfsZVOLSizeBytes(ctx context.Context, runner zfsCommandRunner, dataset string) (int64, error) {
	dataset = strings.TrimSpace(dataset)
	if err := requireZFSDatasetName(dataset); err != nil {
		return 0, err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	stdout, stderr, err := runner(ctx, "get", "-Hp", "-o", "value", "volsize", dataset)
	if err != nil {
		return 0, formatZFSCommandError("zfs get volsize "+dataset, stderr, err)
	}
	raw := strings.TrimSpace(stdout)
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid ZFS volsize %q for dataset %s: %w", raw, dataset, err)
	}
	if size <= 0 {
		return 0, fmt.Errorf("invalid ZFS volsize %d for dataset %s", size, dataset)
	}
	return size, nil
}

func vmRestoreTempDataset(activeDataset string) (string, error) {
	activeDataset = strings.Trim(strings.TrimSpace(activeDataset), "/")
	if activeDataset == "" {
		return "", fmt.Errorf("active VM zvol dataset is required")
	}
	suffix, err := currentVMRestoreTempSuffixFunc()()
	if err != nil {
		return "", fmt.Errorf("generate temporary VM restore dataset suffix: %w", err)
	}
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return "", fmt.Errorf("temporary VM restore dataset suffix is required")
	}
	return activeDataset + "-restore-" + suffix, nil
}

func currentVMRestoreTempSuffixFunc() func() (string, error) {
	if vmRestoreTempSuffixFunc != nil {
		return vmRestoreTempSuffixFunc
	}
	return generateRandomSnapshotSuffix
}

type vmRestoreCopyFunc func(context.Context, string, string) error
type vmRestoreZVOLDeviceWaitFunc func(context.Context, ...string) error

func currentVMRestoreCopyRunner() vmRestoreCopyFunc {
	if vmRestoreCopyRunner != nil {
		return vmRestoreCopyRunner
	}
	return copyVMRestoreZVOL
}

func currentVMRestoreZVOLDeviceWaiter() vmRestoreZVOLDeviceWaitFunc {
	if vmRestoreZVOLDeviceWaiter != nil {
		return vmRestoreZVOLDeviceWaiter
	}
	return waitForVMRestoreZVOLDevices
}

func copyVMRestoreZVOL(ctx context.Context, sourceDevice string, targetDevice string) error {
	cmd := exec.CommandContext(ctx, "dd", "if="+sourceDevice, "of="+targetDevice, "bs=16M", "conv=fsync")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy VM disk %s to %s: %w\n%s", sourceDevice, targetDevice, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func waitForVMRestoreZVOLDevices(ctx context.Context, devices ...string) error {
	settleVMRestoreZVOLDevices(ctx)
	if _, err := os.Stat("/dev/zvol"); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	deadline := time.Now().Add(vmRestoreZVOLDeviceWaitTimeout)
	for {
		missingDevice, missingErr := firstMissingVMRestoreZVOLDevice(devices)
		if missingDevice == "" {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for VM zvol device %s: %w", missingDevice, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for VM zvol device %s: %w", missingDevice, missingErr)
		}
		time.Sleep(vmRestoreZVOLDeviceWaitInterval)
	}
}

func settleVMRestoreZVOLDevices(ctx context.Context) {
	command := vmZVOLSettleCommand()
	if len(command) == 0 {
		return
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	_ = cmd.Run()
}

func firstMissingVMRestoreZVOLDevice(devices []string) (string, error) {
	for _, device := range devices {
		device = strings.TrimSpace(device)
		if device == "" {
			continue
		}
		if _, err := os.Stat(device); err != nil {
			return device, err
		}
	}
	return "", nil
}

func zvolDevicePath(dataset string) string {
	return "/dev/zvol/" + strings.Trim(strings.TrimSpace(dataset), "/")
}

func (s *Server) requireVMCloneTargetDatasetAvailable(ctx context.Context, targetDataset string) error {
	runner := s.zfsRunner
	if runner == nil {
		runner = runZFSCommand
	}
	exists, err := zfsDatasetExists(ctx, runner, targetDataset)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("target VM zvol dataset %s already exists", targetDataset)
	}
	return nil
}

func (s *Server) ensureVMCloneTargetParentDatasets(ctx context.Context, targetDataset string) ([]string, error) {
	runner := s.zfsRunner
	if runner == nil {
		runner = runZFSCommand
	}
	parentDataset, err := zfsParentDataset(targetDataset)
	if err != nil {
		return nil, err
	}
	if parentDataset == "" {
		return nil, nil
	}
	exists, err := zfsDatasetExists(ctx, runner, parentDataset)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, nil
	}
	created, err := zfsCreateMissingParentDatasets(ctx, runner, parentDataset)
	if err != nil {
		if len(created) > 0 {
			return nil, vmCloneParentCleanupError(created, err, destroyVMCloneParentDatasets(ctx, runner, created))
		}
		return nil, err
	}
	return created, nil
}

func zfsParentDataset(dataset string) (string, error) {
	dataset = strings.Trim(strings.TrimSpace(dataset), "/")
	if err := requireZFSDatasetName(dataset); err != nil {
		return "", err
	}
	idx := strings.LastIndex(dataset, "/")
	if idx < 0 {
		return "", nil
	}
	return dataset[:idx], nil
}

func zfsCreateMissingParentDatasets(ctx context.Context, runner zfsCommandRunner, parentDataset string) ([]string, error) {
	prefixes, err := zfsDatasetPathPrefixes(parentDataset)
	if err != nil {
		return nil, err
	}
	created := make([]string, 0, len(prefixes))
	for _, candidate := range prefixes[1:] {
		exists, err := zfsDatasetExists(ctx, runner, candidate)
		if err != nil {
			return created, err
		}
		if exists {
			continue
		}
		didCreate, err := zfsCreateDatasetOwned(ctx, runner, candidate)
		if err != nil {
			return created, err
		}
		if didCreate {
			created = append(created, candidate)
		}
	}
	return created, nil
}

func zfsDatasetPathPrefixes(dataset string) ([]string, error) {
	dataset = strings.Trim(strings.TrimSpace(dataset), "/")
	if err := requireZFSDatasetName(dataset); err != nil {
		return nil, err
	}
	parts := strings.Split(dataset, "/")
	prefixes := make([]string, 0, len(parts))
	for i := range parts {
		prefixes = append(prefixes, strings.Join(parts[:i+1], "/"))
	}
	return prefixes, nil
}

func zfsCreateDatasetOwned(ctx context.Context, runner zfsCommandRunner, dataset string) (bool, error) {
	dataset = strings.TrimSpace(dataset)
	if err := requireZFSDatasetName(dataset); err != nil {
		return false, err
	}
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "create", dataset)
	if err != nil {
		if zfsCreateDatasetAlreadyExists(stderr) {
			return false, nil
		}
		return false, formatZFSCommandError("zfs create "+dataset, stderr, err)
	}
	return true, nil
}

func zfsCreateDatasetAlreadyExists(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "already exists")
}

func destroyVMCloneParentDatasets(ctx context.Context, runner zfsCommandRunner, datasets []string) error {
	for i := len(datasets) - 1; i >= 0; i-- {
		if err := zfsDestroyDatasetNonRecursive(ctx, runner, datasets[i]); err != nil {
			return err
		}
	}
	return nil
}

func vmCloneParentCleanupError(parentDatasets []string, cause error, destroyErr error) error {
	if destroyErr != nil {
		return fmt.Errorf("%w; cleanup failed: destroy VM clone parent datasets %s: %w", cause, strings.Join(parentDatasets, ", "), destroyErr)
	}
	return cause
}

func (s *Server) requireRecoveryCloneTargetAvailable(name string) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	if _, ok := dv.Services().GetOk(name); ok {
		return fmt.Errorf("service %q already exists", name)
	}
	return nil
}

func (s *Server) insertRecoveryCloneService(service *db.Service) (bool, error) {
	inserted := false
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		if d.Services == nil {
			d.Services = map[string]*db.Service{}
		}
		if _, ok := d.Services[service.Name]; ok {
			return fmt.Errorf("service %q already exists", service.Name)
		}
		d.Services[service.Name] = service.Clone()
		inserted = true
		return nil
	})
	return inserted, err
}

func (s *Server) cleanupFailedVMClone(ctx context.Context, targetDataset string, parentDatasets []string, serviceName string, removeService bool, cause error) error {
	var cleanupErrs []error
	if err := zfsDestroyDataset(ctx, s.zfsRunner, targetDataset); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("destroy cloned dataset %s: %w", targetDataset, err))
	} else {
		cleanupErrs = appendCleanupError(cleanupErrs, destroyVMCloneParentDatasetCleanup(ctx, s.zfsRunner, parentDatasets))
		if removeService {
			cleanupErrs = appendCleanupError(cleanupErrs, s.removeFailedVMCloneService(serviceName, targetDataset))
		}
	}
	return vmCloneCleanupResult(cause, cleanupErrs)
}

func destroyVMCloneParentDatasetCleanup(ctx context.Context, runner zfsCommandRunner, parentDatasets []string) error {
	if len(parentDatasets) == 0 {
		return nil
	}
	if err := destroyVMCloneParentDatasets(ctx, runner, parentDatasets); err != nil {
		return fmt.Errorf("destroy VM clone parent datasets %s: %w", strings.Join(parentDatasets, ", "), err)
	}
	return nil
}

func (s *Server) removeFailedVMCloneService(serviceName string, targetDataset string) error {
	if _, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		service, ok := d.Services[serviceName]
		if ok && recoveryCloneServiceMatchesTarget(service, targetDataset) {
			delete(d.Services, serviceName)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("remove cloned service %q: %w", serviceName, err)
	}
	return nil
}

func appendCleanupError(cleanupErrs []error, err error) []error {
	if err != nil {
		return append(cleanupErrs, err)
	}
	return cleanupErrs
}

func vmCloneCleanupResult(cause error, cleanupErrs []error) error {
	if cleanupErr := errors.Join(cleanupErrs...); cleanupErr != nil {
		return fmt.Errorf("%w; cleanup failed: %w", cause, cleanupErr)
	}
	return cause
}

func cloneVMRecoveryService(source *db.Service, newServiceName string, targetDataset string) *db.Service {
	cloned := source.Clone()
	cloned.Name = newServiceName
	cloned.Generation = 0
	cloned.LatestGeneration = 0
	cloned.Artifacts = nil
	cloned.SvcNetwork = nil
	cloned.Macvlan = nil
	cloned.TSNet = nil
	cloned.ServiceRoot = replaceOrClearServiceSegment(cloned.ServiceRoot, source.Name, newServiceName)
	cloned.ServiceRootZFS = replaceOrClearServiceSegment(cloned.ServiceRootZFS, source.Name, newServiceName)
	cloned.VM.Disk.Path = "/dev/zvol/" + targetDataset
	cloned.VM.Console.SocketPath = replaceOrClearServiceSegment(cloned.VM.Console.SocketPath, source.Name, newServiceName)
	cloned.VM.Console.LogPath = replaceOrClearServiceSegment(cloned.VM.Console.LogPath, source.Name, newServiceName)
	cloned.VM.Sockets.APISocketPath = replaceOrClearServiceSegment(cloned.VM.Sockets.APISocketPath, source.Name, newServiceName)
	cloned.VM.PIDFile = replaceOrClearServiceSegment(cloned.VM.PIDFile, source.Name, newServiceName)
	cloned.VM.Networks = cloneVMRecoveryNetworks(newServiceName, source.VM.Networks)
	return cloned
}

func cloneVMRecoveryNetworks(newServiceName string, source []db.VMNetworkConfig) []db.VMNetworkConfig {
	if len(source) == 0 {
		return nil
	}
	modes := make([]string, 0, len(source))
	input := vmNetworkInputs{}
	for _, network := range source {
		mode := strings.TrimSpace(network.Mode)
		if mode == "" {
			continue
		}
		modes = append(modes, mode)
		if mode == "lan" {
			input.LANParent = strings.TrimSpace(network.Parent)
			input.LANVLAN = network.VLAN
		}
	}
	if len(modes) == 0 {
		return nil
	}
	return newVMNetworkPlan(newServiceName, modes, input).DBNetworks()
}

func vmRecoveryCloneDataset(sourceDataset string, sourceServiceName string, newServiceName string) (string, error) {
	sourceDataset = strings.Trim(strings.TrimSpace(sourceDataset), "/")
	if sourceDataset == "" {
		return "", fmt.Errorf("source VM zvol dataset is required")
	}
	replaced, count := replaceServiceNameSegment(sourceDataset, sourceServiceName, newServiceName)
	switch count {
	case 0:
		return "", fmt.Errorf("unsupported VM zvol layout %q; expected service name segment %q", sourceDataset, sourceServiceName)
	case 1:
		return replaced, nil
	default:
		return "", fmt.Errorf("ambiguous VM zvol layout %q; expected exactly one service name segment %q", sourceDataset, sourceServiceName)
	}
}

func replaceOrClearServiceSegment(value string, sourceServiceName string, newServiceName string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	replaced, count := replaceServiceNameSegment(value, sourceServiceName, newServiceName)
	if count != 1 {
		return ""
	}
	return replaced
}

func replaceServiceNameSegment(value string, sourceServiceName string, newServiceName string) (string, int) {
	parts := strings.Split(value, "/")
	replaced := 0
	for i, part := range parts {
		if part == sourceServiceName {
			parts[i] = newServiceName
			replaced++
		}
	}
	if replaced == 0 {
		return value, 0
	}
	return strings.Join(parts, "/"), replaced
}
