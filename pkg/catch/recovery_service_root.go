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
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	installServiceRootCloneDefinition   = installServiceRootCloneDefinitionDefault
	stopServiceRootClone                = stopServiceRootCloneDefault
	rewriteServiceRootCloneArtifacts    = rewriteServiceRootCloneArtifactsDefault
	reconcileServiceRootCloneIdentity   = reconcileServiceRootCloneIdentityDefault
	reconcileServiceRootRestoreIdentity = reconcileServiceRootRestoreIdentityDefault
	installServiceRootRestoreGeneration = installServiceRootRestoreGenerationDefault
	placeServiceRootRestoreStage        = prepareServiceRootRestoreStageDefault
)

func (s *Server) cloneServiceRootRecoveryPoint(ctx context.Context, service *db.Service, point recoveryPoint, newServiceName string, flags cli.SnapshotsCloneFlags, w io.Writer) error {
	if err := validateServiceRootRecoveryPoint(service, point); err != nil {
		return err
	}
	if err := validateServiceRootCloneStart(flags.Start); err != nil {
		return err
	}
	newServiceName, targetDataset, err := s.planServiceRootRecoveryClone(service, point, newServiceName)
	if err != nil {
		return err
	}
	if err := s.requireServiceRootCloneTargetDatasetAvailable(ctx, targetDataset); err != nil {
		return err
	}
	if err := zfsCloneSnapshot(ctx, s.zfsRunner, point.Name, targetDataset); err != nil {
		return err
	}

	targetRoot, err := zfsDatasetMountpoint(ctx, s.zfsRunner, targetDataset)
	if err != nil {
		return s.cleanupFailedRecoveryClone(ctx, targetDataset, newServiceName, false, err)
	}
	if err := s.materializeServiceRootRecoveryClone(ctx, service, point, newServiceName, targetDataset, targetRoot, flags); err != nil {
		return err
	}
	writef(w, "Created service: %s (stopped).\n", newServiceName)
	writef(w, "Cloned service root: %s\n", targetDataset)
	return nil
}

func (s *Server) materializeServiceRootRecoveryClone(ctx context.Context, service *db.Service, point recoveryPoint, newServiceName string, targetDataset string, targetRoot string, flags cli.SnapshotsCloneFlags) error {
	clonedService, err := cloneServiceRootRecoveryService(service, point, newServiceName, targetDataset, targetRoot)
	if err != nil {
		return s.cleanupFailedRecoveryClone(ctx, targetDataset, newServiceName, false, err)
	}
	if err := currentServiceRootCloneArtifactRewriter()(clonedService.Artifacts, service.ServiceRoot, targetRoot); err != nil {
		return s.cleanupFailedRecoveryClone(ctx, targetDataset, newServiceName, false, err)
	}
	if err := currentServiceRootCloneIdentityReconciler()(ctx, s, clonedService, targetRoot); err != nil {
		return s.cleanupFailedRecoveryClone(ctx, targetDataset, newServiceName, false, err)
	}
	_, err = s.insertRecoveryCloneService(clonedService)
	var insertWarning error
	if err != nil {
		if dbMutationCommitted(err) {
			insertWarning = fmt.Errorf("record cloned service %q: %w", newServiceName, err)
		} else {
			return s.cleanupFailedRecoveryClone(ctx, targetDataset, newServiceName, false, err)
		}
	}
	if err := currentServiceRootCloneDefinitionInstaller()(s, clonedService); err != nil {
		return s.cleanupFailedRecoveryClone(ctx, targetDataset, newServiceName, true, errors.Join(insertWarning, err))
	}
	if !flags.Start {
		if err := currentServiceRootCloneStopper()(s, clonedService); err != nil {
			return errors.Join(insertWarning, fmt.Errorf("created clone %q but failed to stop it: %w", newServiceName, err))
		}
	}
	return insertWarning
}

type serviceRootCloneDefinitionInstaller func(*Server, *db.Service) error
type serviceRootCloneStopper func(*Server, *db.Service) error
type serviceRootCloneArtifactRewriter func(db.ArtifactStore, string, string) error
type serviceRootCloneIdentityReconciler func(context.Context, *Server, *db.Service, string) error

func currentServiceRootCloneDefinitionInstaller() serviceRootCloneDefinitionInstaller {
	if installServiceRootCloneDefinition != nil {
		return installServiceRootCloneDefinition
	}
	return installServiceRootCloneDefinitionDefault
}

func currentServiceRootCloneStopper() serviceRootCloneStopper {
	if stopServiceRootClone != nil {
		return stopServiceRootClone
	}
	return stopServiceRootCloneDefault
}

func currentServiceRootCloneArtifactRewriter() serviceRootCloneArtifactRewriter {
	if rewriteServiceRootCloneArtifacts != nil {
		return rewriteServiceRootCloneArtifacts
	}
	return rewriteServiceRootCloneArtifactsDefault
}

func currentServiceRootCloneIdentityReconciler() serviceRootCloneIdentityReconciler {
	if reconcileServiceRootCloneIdentity != nil {
		return reconcileServiceRootCloneIdentity
	}
	return reconcileServiceRootCloneIdentityDefault
}

func installServiceRootCloneDefinitionDefault(s *Server, service *db.Service) error {
	if service == nil {
		return fmt.Errorf("cloned service is required")
	}
	installer, err := s.NewInstaller(InstallerCfg{
		ServiceName: service.Name,
		ClientOut:   io.Discard,
	})
	if err != nil {
		return fmt.Errorf("create installer for cloned service %s: %w", service.Name, err)
	}
	if err := installer.installDefinitionOnly(service.Clone()); err != nil {
		return fmt.Errorf("install cloned service definition %s: %w", service.Name, err)
	}
	return nil
}

func rewriteServiceRootCloneArtifactsDefault(artifacts db.ArtifactStore, oldRoot, newRoot string) error {
	return rewriteCopiedServiceRootArtifacts(artifacts, oldRoot, newRoot)
}

func stopServiceRootCloneDefault(s *Server, service *db.Service) error {
	if service == nil {
		return fmt.Errorf("cloned service is required")
	}
	runner, err := s.serviceRootRestoreRunner(service)
	if err != nil {
		return fmt.Errorf("create stop runner for cloned service %s: %w", service.Name, err)
	}
	if err := runner.Stop(); err != nil {
		return fmt.Errorf("stop cloned service %s: %w", service.Name, err)
	}
	return nil
}

func validateServiceRootRecoveryPoint(service *db.Service, point recoveryPoint) error {
	serviceName := point.Service
	if service != nil && strings.TrimSpace(service.Name) != "" {
		serviceName = service.Name
	}
	if service == nil || strings.TrimSpace(service.ServiceRootZFS) == "" {
		return fmt.Errorf("service %s is not backed by a ZFS service root", serviceName)
	}
	if point.StorageKind != recoveryStorageServiceRoot {
		return fmt.Errorf("recovery point %s is not a service-root recovery point", point.ShortName)
	}
	return nil
}

func validateServiceRootCloneStart(start bool) error {
	if start {
		return fmt.Errorf("starting service-root clones is not supported yet; run snapshots clone without --start")
	}
	return nil
}

func (s *Server) restoreServiceRootRecoveryPoint(ctx context.Context, service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
	if err := validateServiceRootRecoveryPoint(service, point); err != nil {
		return err
	}
	confirmed, err := confirmServiceRootRestore(service, point, flags, rw)
	if err != nil || !confirmed {
		return err
	}
	if err := s.prepareServiceRootForRestore(ctx, service, point, flags, rw); err != nil {
		return err
	}
	if err := s.restoreServiceRootFromSnapshot(ctx, service, point); err != nil {
		return err
	}
	writef(rw, "Restored service root: %s\n", point.Name)
	if err := s.finishServiceRootRestore(ctx, service, point, flags, rw); err != nil {
		return err
	}
	writef(rw, "Restore complete.\n")
	return nil
}

func (s *Server) prepareServiceRootForRestore(ctx context.Context, service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
	running, err := s.serviceRootRestoreRunningState(service.Name, flags.Stop)
	if err != nil {
		return err
	}
	if err := s.stopServiceRootForRestore(service, running, flags.Stop, rw); err != nil {
		return err
	}
	preRestore, err := s.createPreRestoreServiceRootSnapshot(ctx, service, point)
	if err != nil {
		return err
	}
	writef(rw, "Pre-restore recovery point: %s\n", preRestore)
	return nil
}

func (s *Server) finishServiceRootRestore(ctx context.Context, service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) error {
	if err := reconcileServiceRootRestoreIdentity(ctx, s, service, rw); err != nil {
		return err
	}
	if err := s.restoreServiceRootSnapshotGeneration(service.Name, point, flags.Generation); err != nil {
		return err
	}
	if flags.Generation == "snapshot" {
		writef(rw, "Restored service definition generation: %d\n", *point.Generation)
	}
	return s.startServiceRootAfterRestore(service, flags.Start, rw)
}

func confirmServiceRootRestore(service *db.Service, point recoveryPoint, flags cli.SnapshotsRestoreFlags, rw io.ReadWriter) (bool, error) {
	if flags.Yes {
		return true, nil
	}
	ok, err := cmdutil.Confirm(rw, rw, fmt.Sprintf("Restore service root %s from %s?", service.Name, point.ShortName))
	if err != nil {
		return false, fmt.Errorf("failed to confirm service root restore: %w", err)
	}
	if !ok {
		writef(rw, "Restore cancelled.\n")
		return false, nil
	}
	return true, nil
}

func (s *Server) serviceRootRestoreRunningState(name string, stop bool) (bool, error) {
	running, err := s.IsServiceRunning(name)
	if err != nil {
		return false, err
	}
	if running && !stop {
		return false, fmt.Errorf("service %s is running; pass --stop to stop it before restore", name)
	}
	return running, nil
}

func (s *Server) stopServiceRootForRestore(service *db.Service, running bool, stop bool, w io.Writer) error {
	if !running || !stop {
		return nil
	}
	runner, err := s.serviceRootRestoreRunner(service)
	if err != nil {
		return err
	}
	if err := runner.Stop(); err != nil {
		return err
	}
	writef(w, "Stopped service: %s\n", service.Name)
	return nil
}

func (s *Server) startServiceRootAfterRestore(service *db.Service, start bool, w io.Writer) error {
	if !start {
		return nil
	}
	runner, err := s.serviceRootRestoreRunner(service)
	if err != nil {
		return err
	}
	if err := runner.Start(); err != nil {
		return err
	}
	writef(w, "Started service: %s\n", service.Name)
	return nil
}

func (s *Server) serviceRootRestoreRunner(service *db.Service) (ServiceRunner, error) {
	execer := &ttyExecer{s: s, sn: service.Name}
	return execer.serviceRunnerForType(service.ServiceType)
}

func (s *Server) createPreRestoreServiceRootSnapshot(ctx context.Context, service *db.Service, point recoveryPoint) (string, error) {
	return createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service:    service.Name,
		Dataset:    service.ServiceRootZFS,
		Event:      snapshotEventManual,
		Generation: intPointer(service.Generation),
		Comment:    "pre-restore before " + point.ShortName,
		Checkpoint: recoveryModeServiceRoot,
	})
}

func (s *Server) restoreServiceRootFromSnapshot(ctx context.Context, service *db.Service, point recoveryPoint) error {
	tempDataset, err := serviceRootRestoreTempDataset(point.Dataset)
	if err != nil {
		return err
	}
	if err := zfsCloneSnapshot(ctx, s.zfsRunner, point.Name, tempDataset); err != nil {
		return err
	}

	tempRoot, err := zfsDatasetMountpoint(ctx, s.zfsRunner, tempDataset)
	if err != nil {
		return serviceRootRestoreTempCleanupError(tempDataset, err, zfsDestroyDataset(ctx, s.zfsRunner, tempDataset))
	}

	activeRoot := s.serviceRootFromView(service.View())
	copyErr := restoreServiceRootFromCloneMountpointContext(ctx, tempRoot, activeRoot)
	destroyErr := zfsDestroyDataset(ctx, s.zfsRunner, tempDataset)
	if copyErr != nil {
		return serviceRootRestoreTempCleanupError(tempDataset, copyErr, destroyErr)
	}
	if destroyErr != nil {
		return fmt.Errorf("destroy temporary service-root restore dataset %s: %w", tempDataset, destroyErr)
	}
	return nil
}

func restoreServiceRootFromCloneMountpoint(sourceRoot, targetRoot string) error {
	return restoreServiceRootFromCloneMountpointContext(context.Background(), sourceRoot, targetRoot)
}

func restoreServiceRootFromCloneMountpointContext(ctx context.Context, sourceRoot, targetRoot string) error {
	sourceRoot = filepath.Clean(sourceRoot)
	targetRoot = filepath.Clean(targetRoot)
	if err := requireSafeServiceRootPath(sourceRoot, "temporary service-root clone mountpoint"); err != nil {
		return err
	}
	if err := requireSafeServiceRootPath(targetRoot, "active service root"); err != nil {
		return err
	}
	if sourceRoot == targetRoot {
		return fmt.Errorf("temporary service-root clone mountpoint must differ from active service root")
	}

	parent := filepath.Dir(targetRoot)
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return fmt.Errorf("create active service root %s: %w", targetRoot, err)
	}
	stage, err := os.MkdirTemp(parent, ".yeet-service-root-restore-")
	if err != nil {
		return fmt.Errorf("create service-root restore stage: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			removeAllBestEffort(stage)
		}
	}()

	guard, err := newServiceIdentityCopyGuard(ctx, sourceRoot, "", nil)
	if err != nil {
		return fmt.Errorf("guard temporary service-root clone: %w", err)
	}
	if err := guard.copyToStage(stage); err != nil {
		return fmt.Errorf("copy temporary service-root clone: %w", err)
	}
	removeStage = false
	return placeAndCleanupServiceRootRestoreStage(stage, targetRoot)
}

func prepareServiceRootRestoreStageDefault(_, _ string) error {
	return nil
}

func placeAndCleanupServiceRootRestoreStage(stage, targetRoot string) error {
	stage = filepath.Clean(stage)
	targetRoot = filepath.Clean(targetRoot)
	if err := requireSafeServiceRootPath(stage, "completed service-root restore stage"); err != nil {
		return err
	}
	if err := requireSafeServiceRootPath(targetRoot, "active service root"); err != nil {
		return err
	}
	if stage == targetRoot {
		return fmt.Errorf("completed service-root restore stage must differ from active service root")
	}

	backup, err := reserveServiceRootRestoreBackupPath(targetRoot)
	if err != nil {
		return fmt.Errorf("prepare service-root restore backup; restored data remains staged at %s: %w", stage, err)
	}
	keepBackup := true
	defer func() {
		if !keepBackup {
			removeAllBestEffort(backup)
		}
	}()

	if err := moveServiceRootRestoreContents(targetRoot, backup, true, false); err != nil {
		return failServiceRootRestoreBackup(targetRoot, backup, stage, err, &keepBackup)
	}
	if err := placeServiceRootRestoreStage(stage, targetRoot); err != nil {
		return failServiceRootRestoreAfterMutation("prepare restored service-root contents for placement", targetRoot, backup, stage, err, &keepBackup)
	}
	if err := clearServiceRootRestoreContents(targetRoot, backup); err != nil {
		return failServiceRootRestoreAfterMutation("clear active service-root contents before placement", targetRoot, backup, stage, err, &keepBackup)
	}
	if err := copyServiceRootToStage(stage, targetRoot); err != nil {
		action := fmt.Sprintf("copy restored service-root contents from %s into %s", stage, targetRoot)
		return failServiceRootRestoreAfterMutation(action, targetRoot, backup, stage, err, &keepBackup)
	}
	keepBackup = false
	removeAllBestEffort(backup)
	removeAllBestEffort(stage)
	removeServiceRootRestoreScratchBestEffort(targetRoot)
	return nil
}

func reserveServiceRootRestoreBackupPath(parent string) (string, error) {
	backup, err := os.MkdirTemp(parent, ".yeet-service-root-restore-backup-")
	if err != nil {
		return "", err
	}
	return backup, nil
}

func failServiceRootRestoreBackup(targetRoot, backup, stage string, cause error, keepBackup *bool) error {
	if restoreErr := moveServiceRootRestoreContents(backup, targetRoot, false, true); restoreErr != nil {
		return fmt.Errorf("backup active service-root contents from %s to %s; original active contents may be split between %s and %s and restored data remains staged at %s; failed to restore active contents: %v: %w", targetRoot, backup, targetRoot, backup, stage, restoreErr, cause)
	}
	*keepBackup = false
	return fmt.Errorf("backup active service-root contents from %s to %s; restored data remains staged at %s: %w", targetRoot, backup, stage, cause)
}

func failServiceRootRestoreAfterMutation(action, targetRoot, backup, stage string, cause error, keepBackup *bool) error {
	if restoreErr := restoreServiceRootRestoreBackup(targetRoot, backup); restoreErr != nil {
		return fmt.Errorf("%s; original active contents remain at %s and restored data remains staged at %s; failed to restore active contents: %v: %w", action, backup, stage, restoreErr, cause)
	}
	*keepBackup = false
	return fmt.Errorf("%s; restored data remains staged at %s: %w", action, stage, cause)
}

func restoreServiceRootRestoreBackup(targetRoot, backup string) error {
	if err := clearServiceRootRestoreContents(targetRoot, backup); err != nil {
		return err
	}
	return moveServiceRootRestoreContents(backup, targetRoot, false, true)
}

func moveServiceRootRestoreContents(srcRoot, dstRoot string, excludeScratch bool, removeSourceRoot bool) error {
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(srcRoot, entry.Name())
		if excludeScratch && isServiceRootRestoreScratchName(entry.Name()) {
			continue
		}
		if err := renameServiceRoot(srcPath, filepath.Join(dstRoot, entry.Name())); err != nil {
			return err
		}
	}
	if removeSourceRoot {
		return os.Remove(srcRoot)
	}
	return nil
}

func clearServiceRootRestoreContents(root string, preservePaths ...string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	preserve := make(map[string]struct{}, len(preservePaths))
	for _, path := range preservePaths {
		preserve[filepath.Clean(path)] = struct{}{}
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if _, ok := preserve[filepath.Clean(path)]; ok {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func removeServiceRootRestoreScratchBestEffort(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if isServiceRootRestoreScratchName(entry.Name()) {
			removeAllBestEffort(filepath.Join(root, entry.Name()))
		}
	}
}

func isServiceRootRestoreScratchName(name string) bool {
	return strings.HasPrefix(name, ".yeet-service-root-restore-")
}

func requireSafeServiceRootPath(path string, label string) error {
	if strings.TrimSpace(path) == "" || path == "." {
		return fmt.Errorf("%s path is required", label)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s path %q must be absolute", label, path)
	}
	if path == string(os.PathSeparator) {
		return fmt.Errorf("refusing to use filesystem root as %s", label)
	}
	return nil
}

func serviceRootRestoreTempDataset(activeDataset string) (string, error) {
	activeDataset = strings.Trim(strings.TrimSpace(activeDataset), "/")
	if activeDataset == "" {
		return "", fmt.Errorf("active service-root dataset is required")
	}
	suffix, err := generateRandomSnapshotSuffix()
	if err != nil {
		return "", fmt.Errorf("generate temporary service-root restore dataset suffix: %w", err)
	}
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return "", fmt.Errorf("temporary service-root restore dataset suffix is required")
	}
	return activeDataset + "-restore-" + suffix, nil
}

func serviceRootRestoreTempCleanupError(tempDataset string, cause error, destroyErr error) error {
	if destroyErr != nil {
		return fmt.Errorf("%w; cleanup failed: destroy temporary service-root restore dataset %s: %w", cause, tempDataset, destroyErr)
	}
	return cause
}

func (s *Server) restoreServiceRootSnapshotGeneration(serviceName string, point recoveryPoint, generationMode string) error {
	if generationMode != "snapshot" {
		return nil
	}
	if point.Generation == nil {
		return fmt.Errorf("snapshot %s does not record a service generation", point.ShortName)
	}
	gen := *point.Generation
	service, err := s.serviceRootSnapshotGenerationService(serviceName, gen)
	if err != nil {
		return err
	}
	if err := currentServiceRootRestoreGenerationInstaller()(s, service, gen); err != nil {
		return fmt.Errorf("install service %s generation %d: %w", serviceName, gen, err)
	}
	return s.commitServiceRootSnapshotGenerationService(serviceName, service)
}

func (s *Server) serviceRootSnapshotGenerationService(serviceName string, gen int) (*db.Service, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	current, ok := dv.Services().GetOk(serviceName)
	if !ok {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}
	service := current.AsStruct()
	if service.ServiceType == db.ServiceTypeVM {
		return nil, errors.New(vmGenerationRollbackUnsupportedMessage)
	}
	if err := validateServiceRootSnapshotGenerationArtifacts(service, gen); err != nil {
		return nil, err
	}
	commitGeneratedServiceRefs(nil, service, serviceName, generatedServiceCommit{
		srcRef:           string(db.Gen(gen)),
		dstRefs:          []string{"latest"},
		generation:       gen,
		latestGeneration: service.LatestGeneration,
	})
	return service, nil
}

func validateServiceRootSnapshotGenerationArtifacts(service *db.Service, gen int) error {
	if service == nil {
		return fmt.Errorf("service generation %d definition is required", gen)
	}
	required, err := requiredServiceRootSnapshotGenerationArtifacts(service.ServiceType)
	if err != nil {
		return err
	}
	for _, name := range required {
		artifact, ok := service.Artifacts[name]
		if !ok || artifact == nil || artifact.Refs == nil {
			return fmt.Errorf("snapshot generation %d is missing retained artifact ref %s (%s) for service %s; restore with --generation=current or choose a snapshot whose service generation artifacts are still retained", gen, name, db.Gen(gen), service.Name)
		}
		if _, ok := artifact.Refs[db.Gen(gen)]; !ok {
			return fmt.Errorf("snapshot generation %d is missing retained artifact ref %s (%s) for service %s; restore with --generation=current or choose a snapshot whose service generation artifacts are still retained", gen, name, db.Gen(gen), service.Name)
		}
	}
	return nil
}

func requiredServiceRootSnapshotGenerationArtifacts(serviceType db.ServiceType) ([]db.ArtifactName, error) {
	switch serviceType {
	case db.ServiceTypeSystemd:
		return []db.ArtifactName{db.ArtifactSystemdUnit}, nil
	case db.ServiceTypeDockerCompose:
		return []db.ArtifactName{db.ArtifactDockerComposeFile}, nil
	case db.ServiceTypeVM:
		return nil, errors.New(vmGenerationRollbackUnsupportedMessage)
	default:
		return nil, fmt.Errorf("unknown service type: %v", serviceType)
	}
}

func (s *Server) commitServiceRootSnapshotGenerationService(serviceName string, service *db.Service) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		if _, ok := d.Services[serviceName]; !ok {
			return fmt.Errorf("service %q not found", serviceName)
		}
		d.Services[serviceName] = service.Clone()
		return nil
	})
	return err
}

type serviceRootRestoreGenerationInstaller func(*Server, *db.Service, int) error

func currentServiceRootRestoreGenerationInstaller() serviceRootRestoreGenerationInstaller {
	if installServiceRootRestoreGeneration != nil {
		return installServiceRootRestoreGeneration
	}
	return installServiceRootRestoreGenerationDefault
}

func installServiceRootRestoreGenerationDefault(s *Server, service *db.Service, gen int) error {
	if service == nil {
		return fmt.Errorf("service generation %d definition is required", gen)
	}
	installer, err := s.NewInstaller(InstallerCfg{
		ServiceName: service.Name,
		ClientOut:   io.Discard,
	})
	if err != nil {
		return fmt.Errorf("create installer for service %s generation %d: %w", service.Name, gen, err)
	}
	return installer.installDefinitionOnly(service.Clone())
}

func (s *Server) planServiceRootRecoveryClone(service *db.Service, point recoveryPoint, newServiceName string) (string, string, error) {
	newServiceName = strings.TrimSpace(newServiceName)
	if err := validateRecoveryCloneServiceName(newServiceName); err != nil {
		return "", "", err
	}
	if err := s.requireRecoveryCloneTargetAvailable(newServiceName); err != nil {
		return "", "", err
	}
	targetDataset, err := serviceRootRecoveryCloneDataset(point.Dataset, service.Name, newServiceName)
	if err != nil {
		return "", "", err
	}
	return newServiceName, targetDataset, nil
}

func serviceRootRecoveryCloneDataset(sourceDataset string, sourceServiceName string, newServiceName string) (string, error) {
	sourceDataset = strings.Trim(strings.TrimSpace(sourceDataset), "/")
	if sourceDataset == "" {
		return "", fmt.Errorf("source service-root dataset is required")
	}
	replaced, count := replaceServiceNameSegment(sourceDataset, sourceServiceName, newServiceName)
	switch count {
	case 0:
		return "", fmt.Errorf("unsupported service-root dataset layout %q; expected service name segment %q", sourceDataset, sourceServiceName)
	case 1:
		return replaced, nil
	default:
		return "", fmt.Errorf("ambiguous service-root dataset layout %q; expected exactly one service name segment %q", sourceDataset, sourceServiceName)
	}
}

func (s *Server) requireServiceRootCloneTargetDatasetAvailable(ctx context.Context, targetDataset string) error {
	runner := s.zfsRunner
	if runner == nil {
		runner = runZFSCommand
	}
	exists, err := zfsDatasetExists(ctx, runner, targetDataset)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("target service-root dataset %s already exists", targetDataset)
	}
	return nil
}

func cloneServiceRootRecoveryService(source *db.Service, point recoveryPoint, newServiceName string, targetDataset string, targetRoot string) (*db.Service, error) {
	cloned := source.Clone()
	clearISOCloneState(cloned)
	cloned.Name = newServiceName
	cloned.Generation = 1
	cloned.LatestGeneration = 1
	cloned.SvcNetwork = nil
	cloned.Macvlan = nil
	cloned.TSNet = nil
	cloned.ServiceRootZFS = targetDataset
	cloned.ServiceRoot = targetRoot
	if err := normalizeServiceRootCloneArtifactRefs(cloned.Artifacts, source.ServiceRoot, targetRoot, source.Generation, source.LatestGeneration, point.Generation); err != nil {
		return nil, err
	}
	return cloned, nil
}

func normalizeServiceRootCloneArtifactRefs(artifacts db.ArtifactStore, oldRoot, newRoot string, generation int, latestGeneration int, snapshotGeneration *int) error {
	for name, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		path, ok := serviceRootCloneArtifactPath(artifact, generation, latestGeneration, snapshotGeneration)
		artifact.Refs = map[db.ArtifactRef]string{}
		if !ok {
			continue
		}
		relocated, relocatedOK, err := relocatePathUnderRoot(path, oldRoot, newRoot)
		if err != nil {
			return fmt.Errorf("relocate %s artifact clone ref: %w", name, err)
		}
		if relocatedOK {
			path = relocated
		}
		artifact.Refs["latest"] = path
		artifact.Refs[db.Gen(1)] = path
	}
	return nil
}

func serviceRootCloneArtifactPath(artifact *db.Artifact, generation int, latestGeneration int, snapshotGeneration *int) (string, bool) {
	if artifact == nil {
		return "", false
	}
	if snapshotGeneration != nil {
		if path, ok := artifact.Refs[db.Gen(*snapshotGeneration)]; ok {
			return path, true
		}
	}
	return currentServiceArtifactPath(artifact, generation, latestGeneration)
}

func (s *Server) cleanupFailedRecoveryClone(ctx context.Context, targetDataset string, serviceName string, removeService bool, cause error) error {
	var cleanupErrs []error
	if removeService {
		_, err := mutateRecoveryCloneData(s.cfg.DB, func(d *db.Data) error {
			service, ok := d.Services[serviceName]
			if ok && recoveryCloneServiceMatchesTarget(service, targetDataset) {
				delete(d.Services, serviceName)
			}
			return nil
		})
		if err != nil {
			removeErr := fmt.Errorf("remove cloned service %q: %w", serviceName, err)
			cleanupErrs = append(cleanupErrs, removeErr)
			if !dbMutationCommitted(removeErr) {
				return fmt.Errorf("%w; cleanup failed: %w", cause, errors.Join(cleanupErrs...))
			}
		}
	}
	if err := zfsDestroyDataset(ctx, s.zfsRunner, targetDataset); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("destroy cloned dataset %s: %w", targetDataset, err))
	}
	if cleanupErr := errors.Join(cleanupErrs...); cleanupErr != nil {
		return fmt.Errorf("%w; cleanup failed: %w", cause, cleanupErr)
	}
	return cause
}

func recoveryCloneServiceMatchesTarget(service *db.Service, targetDataset string) bool {
	if service == nil {
		return false
	}
	if service.ServiceRootZFS == targetDataset {
		return true
	}
	if service.VM == nil {
		return false
	}
	return strings.TrimPrefix(service.VM.Disk.Path, "/dev/zvol/") == targetDataset
}
