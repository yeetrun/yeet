// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/copyutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"gopkg.in/yaml.v3"
)

type serviceRootBatchPlan struct {
	Moves []serviceRootMigrationPlan
}

func buildServiceRootMigrationPlan(ctx context.Context, cfg Config, svc db.Service, req serviceRootMigrationRequest) (serviceRootMigrationPlan, error) {
	return buildServiceRootMigrationPlanWithRunner(ctx, cfg, nil, svc, req)
}

func buildServiceRootMigrationPlanWithRunner(ctx context.Context, cfg Config, runner zfsCommandRunner, svc db.Service, req serviceRootMigrationRequest) (serviceRootMigrationPlan, error) {
	oldRoot := filepath.Clean(serviceRootFromConfig(cfg, svc))
	oldRootZFS := svc.ServiceRootZFS
	resolved, err := resolveServiceRootMigrationRequest(ctx, runner, svc.Name, req, oldRoot, oldRootZFS)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	newRoot := filepath.Clean(resolved.Root)
	if err := rejectNoopServiceRootMigration(svc.Name, oldRoot, oldRootZFS, newRoot, resolved.Dataset); err != nil {
		return serviceRootMigrationPlan{}, err
	}
	if rootsAreNested(oldRoot, newRoot) || rootsAreNested(newRoot, oldRoot) {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, newRoot)
	}
	if !req.ZFS {
		newRoot, err = validateRequestedServiceRoot(newRoot)
		if err != nil {
			return serviceRootMigrationPlan{}, err
		}
	}
	newRootExisted, newRootSkeleton, newRootState, err := captureServiceRootMigrationTarget(newRoot)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	return serviceRootMigrationPlan{
		ServiceName: svc.Name, OldRoot: oldRoot, OldRootZFS: oldRootZFS,
		NewRoot: newRoot, NewRootZFS: resolved.Dataset, CreateNewRootZFS: resolved.Created,
		NewRootExisted: newRootExisted, NewRootSkeleton: newRootSkeleton, NewRootState: newRootState,
		GuardSource: serviceRootCopyRequiresGuard(svc),
	}, nil
}

func captureServiceRootMigrationTarget(root string) (bool, bool, []serviceRootTargetPathState, error) {
	_, statErr := os.Lstat(root)
	existed := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, false, nil, fmt.Errorf("inspect target service root %s: %w", root, statErr)
	}
	if !existed {
		return false, false, nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, false, nil, fmt.Errorf("inspect target service root contents %s: %w", root, err)
	}
	skeleton := len(entries) != 0
	state, err := captureServiceRootTargetState(root, skeleton)
	return true, skeleton, state, err
}

func serviceRootCopyRequiresGuard(service db.Service) bool {
	return service.ServiceType == db.ServiceTypeSystemd && service.Name != CatchService &&
		effectiveServiceIdentity(service.View()).Persisted.UID != 0
}

func captureServiceRootTargetState(root string, skeleton bool) ([]serviceRootTargetPathState, error) {
	paths, err := serviceRootTargetStatePaths(root, skeleton)
	if err != nil {
		return nil, err
	}
	state := make([]serviceRootTargetPathState, 0, len(paths))
	for _, rel := range paths {
		entry, err := captureServiceRootTargetPathState(root, rel)
		if err != nil {
			return nil, err
		}
		state = append(state, entry)
	}
	return state, nil
}

func serviceRootTargetStatePaths(root string, skeleton bool) ([]string, error) {
	paths := []string{"."}
	if !skeleton {
		return paths, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("capture target service root entries %s: %w", root, err)
	}
	retrySafe, err := rootIsRetrySafeServiceRootSkeleton(root, entries)
	if err != nil {
		return nil, err
	}
	if !retrySafe {
		return nil, fmt.Errorf("service root %q must be empty or a retry-safe service layout", root)
	}
	for _, path := range serviceDirectoryPlan(root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, rel)
	}
	return paths, nil
}

func captureServiceRootTargetPathState(root, rel string) (serviceRootTargetPathState, error) {
	path := root
	if rel != "." {
		path = filepath.Join(root, rel)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return serviceRootTargetPathState{}, fmt.Errorf("capture target service root state %s: %w", path, err)
	}
	uid, gid, err := nativeServiceFileOwner(info)
	if err != nil {
		return serviceRootTargetPathState{}, err
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		return serviceRootTargetPathState{}, err
	}
	return serviceRootTargetPathState{Path: rel, Mode: info.Mode(), UID: uid, GID: gid, Dev: meta.Dev, Ino: meta.Ino}, nil
}

func materializeServiceRootMigration(ctx context.Context, plan serviceRootMigrationPlan, w io.Writer) error {
	_ = w
	if err := ctx.Err(); err != nil {
		return err
	}
	switch plan.Mode {
	case serviceRootMigrationCopy:
		if plan.GuardSource {
			return materializeGuardedServiceRootMigration(ctx, plan)
		}
		if plan.NewRootZFS != "" {
			return copyServiceRootMigrationIntoMountedRoot(plan.OldRoot, plan.NewRoot)
		}
		return copyServiceRootMigration(plan.OldRoot, plan.NewRoot)
	case serviceRootMigrationEmpty:
		if plan.NewRootZFS != "" {
			return createEmptyMountedServiceRoot(plan.NewRoot)
		}
		return createEmptyServiceRoot(plan.NewRoot)
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
}

func materializeGuardedServiceRootMigration(ctx context.Context, plan serviceRootMigrationPlan) error {
	if err := validateHostControlledServiceRootPath(plan.NewRoot); err != nil {
		return err
	}
	guard, err := newServiceIdentityCopyGuard(ctx, plan.OldRoot, plan.OldRootZFS, nil)
	if err != nil {
		return err
	}
	if plan.NewRootZFS != "" {
		return guard.copyIntoMountedRoot(plan.NewRoot)
	}
	parent := filepath.Dir(plan.NewRoot)
	stage, err := os.MkdirTemp(parent, ".yeet-service-root-")
	if err != nil {
		return fmt.Errorf("create guarded migration stage: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := guard.copyToStage(stage); err != nil {
		return err
	}
	if err := ensureDirsForRoot(stage, ""); err != nil {
		return err
	}
	if err := removeRetrySafeServiceRootSkeleton(plan.NewRoot); err != nil {
		return err
	}
	if err := renameServiceRoot(stage, plan.NewRoot); err != nil {
		return fmt.Errorf("move guarded service root into place: %w", err)
	}
	removeStage = false
	return nil
}

func plannedServiceForRootMigration(cfg Config, plan serviceRootMigrationPlan, oldService *db.Service) (*db.Service, error) {
	updatedService := oldService.Clone()
	if plan.Mode == serviceRootMigrationEmpty {
		updatedService.Artifacts = db.ArtifactStore{}
	} else if err := relocateServiceRootArtifactRefs(updatedService.Artifacts, plan.OldRoot, plan.NewRoot); err != nil {
		return nil, err
	}
	applyServiceRoot(cfg, plan.ServiceName, updatedService, plan.NewRoot, plan.NewRootZFS)
	return updatedService, nil
}

func updatedServiceForRootMigration(cfg Config, plan serviceRootMigrationPlan, oldService *db.Service) (*db.Service, error) {
	updatedService, err := plannedServiceForRootMigration(cfg, plan, oldService)
	if err != nil {
		return nil, err
	}
	if plan.Mode == serviceRootMigrationCopy {
		if err := rewriteCopiedServiceRootArtifacts(updatedService.Artifacts, plan.OldRoot, plan.NewRoot); err != nil {
			return nil, err
		}
	}
	return updatedService, nil
}

func applyServiceRootMigrationRuntimeChanges(ctx context.Context, cfg Config, before db.Service, after db.Service, w io.Writer) error {
	return applyServiceRootMigrationRuntimeChangesForConfigs(ctx, cfg, cfg, before, after, w)
}

func applyServiceRootMigrationRuntimeChangesForConfigs(ctx context.Context, beforeCfg Config, afterCfg Config, before db.Service, after db.Service, w io.Writer) error {
	_ = w
	if err := ctx.Err(); err != nil {
		return err
	}
	beforeServer := &Server{cfg: beforeCfg}
	afterServer := &Server{cfg: afterCfg}
	oldRoot := serviceRootFromConfig(beforeCfg, before)
	newRoot := serviceRootFromConfig(afterCfg, after)
	if err := downDockerComposeForRootMigration(beforeServer, &before, oldRoot); err != nil {
		return err
	}
	if serviceRootMigrationArtifactsCleared(before, after) {
		if serviceRootMigrationHasSystemdArtifacts(&before) {
			if err := uninstallSystemdForRootMigration(beforeServer, &before, oldRoot); err != nil {
				return err
			}
		}
	} else if serviceRootMigrationNeedsSystemdInstall(&before, &after) {
		if err := installSystemdForRootMigration(afterServer, &before, &after, newRoot); err != nil {
			return err
		}
	}
	return nil
}

func serviceRootMigrationArtifactsCleared(before db.Service, after db.Service) bool {
	return len(before.Artifacts) != 0 && len(after.Artifacts) == 0
}

func planServicesRootBatch(ctx context.Context, cfg Config, services []db.Service, oldRoot string, newRoot string, mode catchrpc.HostStorageMigrateServices) (serviceRootBatchPlan, error) {
	if err := validateHostStorageMigrateMode(mode); err != nil {
		return serviceRootBatchPlan{}, err
	}
	oldRoot = hostStorageBatchRoot(cfg.ServicesRoot, oldRoot)
	newRoot = hostStorageBatchRoot("", newRoot)
	if mode == catchrpc.HostStorageMigrateAll && (rootsAreNested(oldRoot, newRoot) || rootsAreNested(newRoot, oldRoot)) {
		return serviceRootBatchPlan{}, fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, newRoot)
	}
	var moves []serviceRootMigrationPlan
	for _, service := range services {
		if err := ctx.Err(); err != nil {
			return serviceRootBatchPlan{}, err
		}
		move, ok, err := planServiceRootBatchMove(cfg, service, oldRoot, newRoot, mode)
		if err != nil {
			return serviceRootBatchPlan{}, err
		}
		if ok {
			moves = append(moves, move)
		}
	}
	slices.SortFunc(moves, func(a, b serviceRootMigrationPlan) int {
		return strings.Compare(a.ServiceName, b.ServiceName)
	})
	return serviceRootBatchPlan{Moves: moves}, nil
}

func planServiceRootBatchMove(cfg Config, service db.Service, oldRoot string, newRoot string, mode catchrpc.HostStorageMigrateServices) (serviceRootMigrationPlan, bool, error) {
	service.Name = strings.TrimSpace(service.Name)
	oldServiceRoot := filepath.Clean(serviceRootFromConfigWithDefault(cfg, service, oldRoot))
	if service.Name == "" {
		return serviceRootMigrationPlan{}, false, fmt.Errorf("service root batch contains a service with no name")
	}
	if strings.TrimSpace(service.ServiceRoot) != "" && !hostStorageRootContains(oldRoot, oldServiceRoot) {
		return serviceRootMigrationPlan{}, false, nil
	}
	if strings.TrimSpace(service.ServiceRoot) == "" || hostStorageRootContains(oldRoot, oldServiceRoot) {
		target := serviceRootBatchTarget(newRoot, service.Name, oldServiceRoot, mode)
		move := serviceRootMigrationPlan{
			ServiceName: service.Name,
			OldRoot:     oldServiceRoot,
			OldRootZFS:  service.ServiceRootZFS,
			NewRoot:     target,
			NewRootZFS:  serviceRootBatchTargetZFS(service, mode),
			GuardSource: serviceRootCopyRequiresGuard(service),
		}
		if mode == catchrpc.HostStorageMigrateAll {
			if err := rejectNoopServiceRootMigration(service.Name, move.OldRoot, move.OldRootZFS, move.NewRoot, move.NewRootZFS); err != nil {
				return serviceRootMigrationPlan{}, false, err
			}
			if rootsAreNested(move.OldRoot, move.NewRoot) || rootsAreNested(move.NewRoot, move.OldRoot) {
				return serviceRootMigrationPlan{}, false, fmt.Errorf("cannot migrate between nested service roots: %s and %s", move.OldRoot, move.NewRoot)
			}
		}
		return move, true, nil
	}
	return serviceRootMigrationPlan{}, false, nil
}

func serviceRootBatchTarget(newRoot, serviceName, oldServiceRoot string, mode catchrpc.HostStorageMigrateServices) string {
	if mode == catchrpc.HostStorageMigrateNone {
		return oldServiceRoot
	}
	return filepath.Join(newRoot, serviceName)
}

func serviceRootBatchTargetZFS(service db.Service, mode catchrpc.HostStorageMigrateServices) string {
	if mode == catchrpc.HostStorageMigrateNone {
		return service.ServiceRootZFS
	}
	return ""
}

func hostStorageBatchRoot(fallback string, root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = fallback
	}
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

func resolveServiceRootMigrationRequest(ctx context.Context, runner zfsCommandRunner, name string, request serviceRootMigrationRequest, oldRoot string, oldRootZFS string) (resolvedServiceRoot, error) {
	if request.ZFS {
		return resolveZFSServiceRootMigrationRequest(ctx, runner, name, request.Root, oldRoot, oldRootZFS)
	}
	newRoot, err := cleanRequestedServiceRoot(request.Root)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	return resolvedServiceRoot{Root: newRoot}, nil
}

func resolveZFSServiceRootMigrationRequest(ctx context.Context, runner zfsCommandRunner, name, requestedDataset, oldRoot, oldRootZFS string) (resolvedServiceRoot, error) {
	dataset := strings.TrimSpace(requestedDataset)
	if dataset == "" {
		return resolvedServiceRoot{}, fmt.Errorf("--service-root is required when --zfs is set")
	}
	if oldRootZFS == dataset {
		return resolvedServiceRoot{}, fmt.Errorf("service %q is already using ZFS dataset %q", name, dataset)
	}

	if runner == nil {
		runner = runZFSCommand
	}
	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	if !exists {
		predictedRoot, err := preflightMissingZFSServiceRootDataset(ctx, runner, name, oldRoot, oldRootZFS, dataset)
		if err != nil {
			return resolvedServiceRoot{}, err
		}
		return resolvedServiceRoot{Root: predictedRoot, Dataset: dataset, ZFS: true, Created: true}, nil
	}

	mountpoint, err := zfsDatasetMountpoint(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	root, _, err := validateZFSMountpoint(mountpoint, zfsServiceRootTarget, exists)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	return resolvedServiceRoot{Root: root, Dataset: dataset, ZFS: true}, nil
}

func serviceRootFromConfig(cfg Config, service db.Service) string {
	return serviceRootFromConfigWithDefault(cfg, service, cfg.ServicesRoot)
}

func serviceRootFromConfigWithDefault(cfg Config, service db.Service, servicesRoot string) string {
	if strings.TrimSpace(service.ServiceRoot) != "" {
		return service.ServiceRoot
	}
	return filepath.Join(servicesRoot, service.Name)
}

func applyServiceRoot(cfg Config, name string, service *db.Service, newRoot, newRootZFS string) {
	service.ServiceRootZFS = newRootZFS
	if newRootZFS == "" && filepath.Clean(newRoot) == filepath.Clean(filepath.Join(cfg.ServicesRoot, name)) {
		service.ServiceRoot = ""
		return
	}
	service.ServiceRoot = newRoot
}

func preflightMissingZFSServiceRootDataset(ctx context.Context, runner zfsCommandRunner, name, oldRoot, oldRootZFS, dataset string) (string, error) {
	slash := strings.LastIndex(dataset, "/")
	if slash <= 0 || slash == len(dataset)-1 {
		return "", fmt.Errorf("new ZFS service root %q must name a child dataset", dataset)
	}
	parentDataset := dataset[:slash]
	childName := dataset[slash+1:]
	parentMountpoint, err := zfsDatasetMountpoint(ctx, runner, parentDataset)
	if err != nil {
		return "", err
	}
	parentRoot, _, err := validateZFSMountpoint(parentMountpoint, zfsServiceRootExisting, true)
	if err != nil {
		return "", err
	}
	predictedRoot := filepath.Join(parentRoot, childName)
	if err := rejectNoopServiceRootMigration(name, oldRoot, oldRootZFS, predictedRoot, dataset); err != nil {
		return "", err
	}
	if rootsAreNested(oldRoot, predictedRoot) || rootsAreNested(predictedRoot, oldRoot) {
		return "", fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, predictedRoot)
	}
	if _, err = validateRequestedServiceRoot(predictedRoot); err != nil {
		return "", err
	}
	return predictedRoot, nil
}

func rejectNoopServiceRootMigration(name, oldRoot, oldRootZFS, newRoot, newRootZFS string) error {
	if oldRoot == newRoot && oldRootZFS == newRootZFS {
		return fmt.Errorf("service root for %q is already %s", name, oldRoot)
	}
	if oldRoot == newRoot {
		return fmt.Errorf("service %q already uses service root %q with a different root type; choose a different target root", name, oldRoot)
	}
	return nil
}

func rootsAreNested(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) refreshServiceRootMigrationPrereqs(oldService, updatedService *db.Service) error {
	if serviceRootMigrationDockerNetNSPrereqsChanged(oldService, updatedService) {
		if err := installDockerPrereqs(s); err != nil {
			return fmt.Errorf("refresh docker prerequisites: %w", err)
		}
	}
	return nil
}

func copyServiceRootMigration(oldRoot, newRoot string) error {
	return copyServiceRootMigrationWithOptions(oldRoot, newRoot, copyutil.TarOptions{})
}

func copyServiceRootMigrationWithOptions(oldRoot, newRoot string, opts copyutil.TarOptions) error {
	parent := filepath.Dir(newRoot)
	stage, err := os.MkdirTemp(parent, ".yeet-service-root-")
	if err != nil {
		return fmt.Errorf("create migration stage: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stage)
		}
	}()

	if err := copyServiceRootToStageWithOptions(oldRoot, stage, opts); err != nil {
		return err
	}
	if err := ensureDirsForRoot(stage, ""); err != nil {
		return err
	}
	if err := removeRetrySafeServiceRootSkeleton(newRoot); err != nil {
		return err
	}
	if err := renameServiceRoot(stage, newRoot); err != nil {
		return fmt.Errorf("move staged service root into place: %w", err)
	}
	removeStage = false
	return nil
}

func copyServiceRootMigrationIntoMountedRoot(oldRoot, newRoot string) error {
	return copyServiceRootMigrationIntoMountedRootWithOptions(oldRoot, newRoot, copyutil.TarOptions{})
}

func copyServiceRootMigrationIntoMountedRootWithOptions(oldRoot, newRoot string, opts copyutil.TarOptions) error {
	retrySafeSkeleton, err := mountedRootIsEmptyOrRetrySafeSkeleton(newRoot)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(newRoot, ".yeet-service-root-")
	if err != nil {
		return fmt.Errorf("create migration stage: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stage)
		}
	}()

	if err := copyServiceRootToStageWithOptions(oldRoot, stage, opts); err != nil {
		return err
	}
	if err := ensureDirsForRoot(stage, ""); err != nil {
		return err
	}
	if retrySafeSkeleton {
		if err := removeMountedRootServiceLayout(newRoot); err != nil {
			return err
		}
	}
	if err := copyutil.MoveTree(stage, newRoot); err != nil {
		return fmt.Errorf("move staged service root contents into place: %w", err)
	}
	removeStage = false
	return nil
}

func copyServiceRootToStage(srcRoot, stageRoot string) error {
	return copyServiceRootToStageWithOptions(srcRoot, stageRoot, copyutil.TarOptions{})
}

func copyServiceRootToStageWithOptions(srcRoot, stageRoot string, opts copyutil.TarOptions) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		if err := copyutil.TarDirectoryWithOptions(pw, srcRoot, "", opts); err != nil {
			_ = pw.CloseWithError(err)
			errCh <- err
			return
		}
		errCh <- pw.Close()
	}()

	extractErr := copyutil.ExtractTarWithOptions(pr, stageRoot, copyutil.ExtractOptions{})
	if extractErr != nil {
		_ = pr.CloseWithError(extractErr)
	}
	archiveErr := <-errCh
	if archiveErr != nil {
		return fmt.Errorf("archive service root: %w", archiveErr)
	}
	if extractErr != nil {
		return fmt.Errorf("extract service root archive: %w", extractErr)
	}
	return nil
}

func createEmptyServiceRoot(root string) error {
	if err := removeRetrySafeServiceRootSkeleton(root); err != nil {
		return err
	}
	return ensureDirsForRoot(root, "")
}

func createEmptyMountedServiceRoot(root string) error {
	retrySafeSkeleton, err := mountedRootIsEmptyOrRetrySafeSkeleton(root)
	if err != nil {
		return err
	}
	if retrySafeSkeleton {
		if err := removeMountedRootServiceLayout(root); err != nil {
			return err
		}
	}
	return ensureDirsForRoot(root, "")
}

func mountedRootIsEmptyOrRetrySafeSkeleton(root string) (bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		return false, fmt.Errorf("failed to stat ZFS mountpoint %q: %w", root, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("ZFS mountpoint %q is not a directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, fmt.Errorf("failed to read service root %q: %w", root, err)
	}
	if len(entries) == 0 {
		return false, nil
	}
	retrySafeSkeleton, err := rootIsRetrySafeServiceRootSkeleton(root, entries)
	if err != nil {
		return false, err
	}
	if !retrySafeSkeleton {
		return false, fmt.Errorf("service root %q must be empty", root)
	}
	return true, nil
}

func removeMountedRootServiceLayout(root string) error {
	for _, dir := range serviceDirectoryPlan(root) {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove retry-safe service root skeleton %q: %w", dir, err)
		}
	}
	return nil
}

func removeRetrySafeServiceRootSkeleton(root string) error {
	empty, err := rootIsMissingOrEmpty(root)
	if err != nil {
		return err
	}
	if empty {
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("remove retry-safe service root skeleton %q: %w", root, err)
		}
	}
	return nil
}

func relocateServiceRootArtifactRefs(artifacts db.ArtifactStore, oldRoot, newRoot string) error {
	for name, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		for ref, path := range artifact.Refs {
			relocated, ok, err := relocatePathUnderRoot(path, oldRoot, newRoot)
			if err != nil {
				return fmt.Errorf("relocate %s artifact ref %s: %w", name, ref, err)
			}
			if ok {
				artifact.Refs[ref] = relocated
			}
		}
	}
	return nil
}

func relocatePathUnderRoot(path, oldRoot, newRoot string) (string, bool, error) {
	path = filepath.Clean(path)
	oldRoot = filepath.Clean(oldRoot)
	newRoot = filepath.Clean(newRoot)
	if !filepath.IsAbs(path) {
		return "", false, nil
	}
	rel, err := filepath.Rel(oldRoot, path)
	if err != nil {
		return "", false, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, nil
	}
	if rel == "." {
		return newRoot, true, nil
	}
	return filepath.Join(newRoot, rel), true, nil
}

func rewriteCopiedServiceRootArtifacts(artifacts db.ArtifactStore, oldRoot, newRoot string) error {
	for name, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		paths := map[string]struct{}{}
		for _, path := range artifact.Refs {
			if _, ok, err := relocatePathUnderRoot(path, newRoot, newRoot); err != nil {
				return fmt.Errorf("check %s artifact path %s: %w", name, path, err)
			} else if !ok {
				continue
			}
			paths[filepath.Clean(path)] = struct{}{}
		}
		for path := range paths {
			if err := rewriteCopiedServiceRootArtifact(name, path, oldRoot, newRoot); err != nil {
				return fmt.Errorf("rewrite %s artifact %s: %w", name, path, err)
			}
		}
	}
	return nil
}

func rewriteCopiedServiceRootArtifact(name db.ArtifactName, path, oldRoot, newRoot string) error {
	if name == db.ArtifactDockerComposeFile {
		return rewriteComposeFileRoot(path, oldRoot, newRoot)
	}
	if !serviceRootMigrationTextArtifacts[name] {
		return nil
	}
	return rewriteFileRoot(path, oldRoot, newRoot)
}

var serviceRootMigrationTextArtifacts = map[db.ArtifactName]bool{
	db.ArtifactDockerComposeNetwork: true,
	db.ArtifactSystemdUnit:          true,
	db.ArtifactSystemdTimerFile:     true,
	db.ArtifactNetNSService:         true,
	db.ArtifactNetNSEnv:             true,
	db.ArtifactTSService:            true,
	db.ArtifactTSEnv:                true,
}

func rewriteFileRoot(path, oldRoot, newRoot string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("artifact path is a directory")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rewritten, changed := replaceRootPathReferences(content, oldRoot, newRoot)
	if !changed {
		return nil
	}
	return writeFileAtomically(path, rewritten, info.Mode().Perm())
}

func rewriteComposeFileRoot(path, oldRoot, newRoot string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("artifact path is a directory")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rewritten, changed, err := rewriteComposeRootReferences(content, oldRoot, newRoot)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return writeFileAtomically(path, rewritten, info.Mode().Perm())
}

func rewriteComposeRootReferences(content []byte, oldRoot, newRoot string) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, false, fmt.Errorf("parse compose yaml: %w", err)
	}
	changed, err := rewriteComposeVolumeRoots(&doc, oldRoot, newRoot)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return content, false, nil
	}
	rewritten, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, fmt.Errorf("marshal compose yaml: %w", err)
	}
	return rewritten, true, nil
}

func rewriteComposeVolumeRoots(doc *yaml.Node, oldRoot, newRoot string) (bool, error) {
	root := yamlDocumentRoot(doc)
	services := yamlMappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return false, nil
	}
	changed := false
	for i := 1; i < len(services.Content); i += 2 {
		service := services.Content[i]
		volumes := yamlMappingValue(service, "volumes")
		if volumes == nil || volumes.Kind != yaml.SequenceNode {
			continue
		}
		for _, volume := range volumes.Content {
			volumeChanged, err := rewriteComposeVolumeRoot(volume, oldRoot, newRoot)
			if err != nil {
				return false, err
			}
			changed = changed || volumeChanged
		}
	}
	return changed, nil
}

func yamlDocumentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func rewriteComposeVolumeRoot(volume *yaml.Node, oldRoot, newRoot string) (bool, error) {
	switch volume.Kind {
	case yaml.ScalarNode:
		rewritten, changed, err := rewriteComposeVolumeString(volume.Value, oldRoot, newRoot)
		if err != nil || !changed {
			return changed, err
		}
		volume.Value = rewritten
		return true, nil
	case yaml.MappingNode:
		return rewriteComposeVolumeMappingRoot(volume, oldRoot, newRoot)
	default:
		return false, nil
	}
}

func rewriteComposeVolumeMappingRoot(volume *yaml.Node, oldRoot, newRoot string) (bool, error) {
	if typ := yamlMappingValue(volume, "type"); typ != nil && !strings.EqualFold(typ.Value, "bind") {
		return false, nil
	}
	for _, key := range []string{"source", "src"} {
		source := yamlMappingValue(volume, key)
		if source == nil || source.Kind != yaml.ScalarNode {
			continue
		}
		relocated, ok, err := relocatePathUnderRoot(source.Value, oldRoot, newRoot)
		if err != nil || !ok {
			return false, err
		}
		source.Value = relocated
		return true, nil
	}
	return false, nil
}

func rewriteComposeVolumeString(volume, oldRoot, newRoot string) (string, bool, error) {
	if rewritten, changed, err := rewriteComposeKeyValueVolumeString(volume, oldRoot, newRoot); changed || err != nil {
		return rewritten, changed, err
	}
	source, rest, ok := strings.Cut(volume, ":")
	if !ok || source == "" {
		return volume, false, nil
	}
	relocated, ok, err := relocatePathUnderRoot(source, oldRoot, newRoot)
	if err != nil || !ok {
		return volume, false, err
	}
	return relocated + ":" + rest, true, nil
}

func rewriteComposeKeyValueVolumeString(volume, oldRoot, newRoot string) (string, bool, error) {
	parts := strings.Split(volume, ",")
	changed := false
	for i, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || (key != "source" && key != "src") {
			continue
		}
		relocated, ok, err := relocatePathUnderRoot(value, oldRoot, newRoot)
		if err != nil {
			return "", false, err
		}
		if ok {
			parts[i] = key + "=" + relocated
			changed = true
		}
	}
	if !changed {
		return volume, false, nil
	}
	return strings.Join(parts, ","), true, nil
}

func replaceRootPathReferences(content []byte, oldRoot, newRoot string) ([]byte, bool) {
	oldRoot = filepath.Clean(oldRoot)
	if oldRoot == "." || oldRoot == "" {
		return content, false
	}
	old := []byte(oldRoot)
	replacement := []byte(filepath.Clean(newRoot))
	var out bytes.Buffer
	changed := false
	start := 0
	for {
		idx := bytes.Index(content[start:], old)
		if idx < 0 {
			break
		}
		idx += start
		if rootPathReferenceAt(content, idx, len(old)) {
			out.Write(content[start:idx])
			out.Write(replacement)
			start = idx + len(old)
			changed = true
			continue
		}
		out.Write(content[start : idx+1])
		start = idx + 1
	}
	if !changed {
		return content, false
	}
	out.Write(content[start:])
	return out.Bytes(), true
}

func rootPathReferenceAt(content []byte, idx, size int) bool {
	return rootPathBoundaryBefore(content, idx) && rootPathBoundaryAfter(content, idx+size)
}

func rootPathBoundaryBefore(content []byte, idx int) bool {
	if idx == 0 {
		return true
	}
	if content[idx] == '/' && content[idx-1] == '-' && systemdEnvironmentFilePrefixBefore(content, idx-1) {
		return true
	}
	return !isRootPathByte(content[idx-1])
}

func systemdEnvironmentFilePrefixBefore(content []byte, dashIdx int) bool {
	lineStart := bytes.LastIndexByte(content[:dashIdx], '\n') + 1
	prefix := strings.TrimSpace(string(content[lineStart:dashIdx]))
	return strings.HasSuffix(prefix, "EnvironmentFile=")
}

func rootPathBoundaryAfter(content []byte, idx int) bool {
	if idx >= len(content) {
		return true
	}
	return content[idx] == '/' || !isRootPathNameByte(content[idx])
}

func isRootPathByte(c byte) bool {
	return c == '/' || isRootPathNameByte(c)
}

func isRootPathNameByte(c byte) bool {
	return c == '-' || c == '_' || c == '.' || ('0' <= c && c <= '9') || ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z')
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".yeet-rewrite-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func (s *Server) downDockerComposeForRootMigration(service *db.Service, oldRoot string) error {
	if !serviceRootMigrationHasDockerCompose(service) {
		return nil
	}
	composeService, err := svc.NewDockerComposeService(s.cfg.DB, service.View(), serviceDataDirForRoot(oldRoot), serviceRunDirForRoot(oldRoot))
	if err != nil {
		return fmt.Errorf("load old docker compose project: %w", err)
	}
	if err := composeService.Down(); err != nil {
		return fmt.Errorf("remove old docker compose project: %w", err)
	}
	return nil
}

func serviceRootMigrationHasDockerCompose(service *db.Service) bool {
	if service == nil || service.ServiceType != db.ServiceTypeDockerCompose {
		return false
	}
	return serviceRootMigrationHasGeneratedArtifact(service, db.ArtifactDockerComposeFile)
}

func (s *Server) installSystemdForRootMigration(_ *db.Service, updatedService *db.Service, newRoot string) error {
	systemdService, err := svc.NewSystemdService(s.cfg.DB, updatedService.View(), serviceRunDirForRoot(newRoot))
	if err != nil {
		return fmt.Errorf("load migrated systemd artifacts: %w", err)
	}
	if err := systemdService.Install(); err != nil {
		return fmt.Errorf("install migrated systemd artifacts: %w", err)
	}
	return nil
}

func (s *Server) uninstallSystemdForRootMigration(oldService *db.Service, oldRoot string) error {
	systemdService, err := svc.NewSystemdService(s.cfg.DB, oldService.View(), serviceRunDirForRoot(oldRoot))
	if err != nil {
		return fmt.Errorf("load old systemd artifacts: %w", err)
	}
	if err := systemdService.Uninstall(); err != nil {
		return fmt.Errorf("uninstall old systemd artifacts: %w", err)
	}
	return nil
}

func serviceRootMigrationNeedsSystemdInstall(oldService, updatedService *db.Service) bool {
	return serviceRootMigrationHasSystemdArtifacts(oldService) || serviceRootMigrationHasSystemdArtifacts(updatedService)
}

func serviceRootMigrationDockerNetNSPrereqsChanged(oldService, updatedService *db.Service) bool {
	return serviceRootMigrationHasDockerNetNSPrereq(oldService) != serviceRootMigrationHasDockerNetNSPrereq(updatedService)
}

func serviceRootMigrationHasDockerNetNSPrereq(service *db.Service) bool {
	if service == nil || service.ServiceType != db.ServiceTypeDockerCompose {
		return false
	}
	return serviceRootMigrationHasGeneratedArtifact(service, db.ArtifactNetNSService)
}

func serviceRootMigrationHasSystemdArtifacts(service *db.Service) bool {
	if service == nil {
		return false
	}
	for artifact := range serviceRootMigrationSystemdArtifacts {
		if serviceRootMigrationHasGeneratedArtifact(service, artifact) {
			return true
		}
	}
	return false
}

func serviceRootMigrationHasGeneratedArtifact(service *db.Service, name db.ArtifactName) bool {
	if service == nil {
		return false
	}
	artifact := service.Artifacts[name]
	if artifact == nil {
		return false
	}
	_, ok := artifact.Refs[db.Gen(service.Generation)]
	return ok
}

var serviceRootMigrationSystemdArtifacts = map[db.ArtifactName]bool{
	db.ArtifactSystemdUnit:      true,
	db.ArtifactSystemdTimerFile: true,
	db.ArtifactNetNSService:     true,
	db.ArtifactNetNSEnv:         true,
	db.ArtifactTypeScriptFile:   true,
	db.ArtifactPythonFile:       true,
	db.ArtifactBinary:           true,
	db.ArtifactEnvFile:          true,
	db.ArtifactTSService:        true,
	db.ArtifactTSEnv:            true,
	db.ArtifactTSBinary:         true,
	db.ArtifactTSConfig:         true,
}
