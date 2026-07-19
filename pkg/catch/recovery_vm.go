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
	"strconv"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmRestoreTempSuffixFunc         = generateRandomSnapshotSuffix
	vmRestoreCopyRunner             = copyVMRestoreZVOL
	vmRestoreZVOLDeviceWaiter       = waitForVMRestoreZVOLDevices
	vmRestoreZVOLDeviceWaitTimeout  = 10 * time.Second
	vmRestoreZVOLDeviceWaitInterval = 50 * time.Millisecond
	mutateRecoveryCloneData         = (*db.Store).MutateData
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
	_, err = s.insertRecoveryCloneService(clonedService)
	if err != nil {
		if dbMutationCommitted(err) {
			return fmt.Errorf("record cloned VM service %q: %w", newServiceName, err)
		}
		return s.cleanupFailedVMClone(ctx, targetDataset, createdParentDatasets, newServiceName, false, err)
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
	if point.Mode == retiredVMCheckpointMode {
		return fmt.Errorf("recovery point %s uses retired full VM checkpoint format and cannot be restored", point.ShortName)
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
	}
	return ok, nil
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
	flags := cli.SnapshotsCreateFlags{Comment: "pre-restore before " + point.ShortName}
	vm := *service.VM.Clone()
	dataset, err := vmSnapshotDataset(vm.Disk)
	if err != nil {
		return "", err
	}
	result, err := s.createPausedVMSnapshot(ctx, service, dataset, flags)
	return result.Name, err
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
	_, err := mutateRecoveryCloneData(s.cfg.DB, func(d *db.Data) error {
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

func dbMutationCommitted(err error) bool {
	var publishedErr *db.PostPublicationError
	return errors.As(err, &publishedErr) && publishedErr.MutationCommitted
}

func (s *Server) cleanupFailedVMClone(ctx context.Context, targetDataset string, parentDatasets []string, serviceName string, removeService bool, cause error) error {
	var cleanupErrs []error
	if removeService {
		removeErr := s.removeFailedVMCloneService(serviceName, targetDataset)
		if removeErr != nil {
			cleanupErrs = append(cleanupErrs, removeErr)
			if !dbMutationCommitted(removeErr) {
				return vmCloneCleanupResult(cause, cleanupErrs)
			}
		}
	}
	if err := zfsDestroyDataset(ctx, s.zfsRunner, targetDataset); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("destroy cloned dataset %s: %w", targetDataset, err))
	} else {
		cleanupErrs = appendCleanupError(cleanupErrs, destroyVMCloneParentDatasetCleanup(ctx, s.zfsRunner, parentDatasets))
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
	clearISOCloneState(cloned)
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
