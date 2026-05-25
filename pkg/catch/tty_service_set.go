// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/copyutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"gopkg.in/yaml.v3"
)

type serviceRootMigrationMode int

const (
	serviceRootMigrationPrompt serviceRootMigrationMode = iota
	serviceRootMigrationCopy
	serviceRootMigrationEmpty
)

type serviceRootMigrationRequest struct {
	Root string
	ZFS  bool
}

type serviceRootMigrationPlan struct {
	ServiceName string
	OldRoot     string
	OldRootZFS  string
	NewRoot     string
	NewRootZFS  string
}

var (
	isServiceRunningForRootMigration = func(s *Server, name string) (bool, error) {
		return s.IsServiceRunning(name)
	}
	renameServiceRoot                 = os.Rename
	downDockerComposeForRootMigration = (*Server).downDockerComposeForRootMigration
	installSystemdForRootMigration    = (*Server).installSystemdForRootMigration
	uninstallSystemdForRootMigration  = (*Server).uninstallSystemdForRootMigration
)

func (e *ttyExecer) serviceCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("service requires a command")
	}
	switch args[0] {
	case "set":
		flags, rest, err := cli.ParseServiceSet(args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("unexpected service set args: %s", strings.Join(rest, " "))
		}
		return e.serviceSetCmdFunc(flags)
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func (e *ttyExecer) serviceSetCmdFunc(flags cli.ServiceSetFlags) error {
	rootChange := strings.TrimSpace(flags.ServiceRoot) != "" || flags.ZFS
	if flags.SnapshotChange {
		if err := validateServiceSnapshotFlags(flags); err != nil {
			return err
		}
	}
	if rootChange {
		if err := e.serviceSetRoot(flags); err != nil {
			return err
		}
	}
	if flags.SnapshotChange {
		return e.s.updateServiceSnapshotPolicy(e.sn, flags)
	}
	if !rootChange {
		return fmt.Errorf("service set requires --service-root or snapshot settings")
	}
	return nil
}

func (e *ttyExecer) serviceSetRoot(flags cli.ServiceSetFlags) error {
	mode := serviceRootMigrationPrompt
	if flags.Copy {
		mode = serviceRootMigrationCopy
	}
	if flags.Empty {
		mode = serviceRootMigrationEmpty
	}
	if mode == serviceRootMigrationPrompt && !e.isPty {
		return serviceRootMigrationModeRequiredError()
	}

	request := serviceRootMigrationRequest{Root: flags.ServiceRoot, ZFS: flags.ZFS}
	plan, err := e.s.validateServiceRootMigration(e.sn, request)
	if err != nil {
		return err
	}
	mode, err = e.confirmServiceRootMigrationMode(mode, plan)
	if err != nil {
		return err
	}
	return e.s.migrateServiceRootWithPlanWriter(plan, mode, e.rw)
}

func (s *Server) updateServiceSnapshotPolicy(name string, flags cli.ServiceSetFlags) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		service, ok := d.Services[name]
		if !ok {
			return fmt.Errorf("service %q not found", name)
		}
		if err := validateSnapshotInheritExclusive(flags); err != nil {
			return err
		}
		return applySnapshotFlagsToService(service, flags)
	})
	return err
}

func applySnapshotFlagsToService(service *db.Service, flags cli.ServiceSetFlags) error {
	if err := validateServiceSnapshotFlags(flags); err != nil {
		return err
	}
	if flags.Snapshots == "inherit" {
		service.SnapshotPolicy = nil
		return nil
	}
	policy := service.SnapshotPolicy
	if policy == nil {
		policy = &db.SnapshotPolicy{}
	}
	if err := applyServiceSnapshotFlags(policy, flags); err != nil {
		return err
	}
	service.SnapshotPolicy = policy
	return nil
}

func validateServiceSnapshotFlags(flags cli.ServiceSetFlags) error {
	if err := validateSnapshotInheritExclusive(flags); err != nil {
		return err
	}
	return applyServiceSnapshotFlags(&db.SnapshotPolicy{}, flags)
}

func validateSnapshotInheritExclusive(flags cli.ServiceSetFlags) error {
	if flags.Snapshots != "inherit" {
		return nil
	}
	if flags.SnapshotKeepLast == "" && flags.SnapshotMaxAge == "" && flags.SnapshotRequired == "" && flags.SnapshotEvents == "" {
		return nil
	}
	return fmt.Errorf("--snapshots=inherit cannot be combined with field-level snapshot flags")
}

func applyServiceSnapshotFlags(policy *db.SnapshotPolicy, flags cli.ServiceSetFlags) error {
	applyServiceSnapshotModeFlag(policy, flags.Snapshots)
	if err := applyServiceSnapshotKeepLastFlag(policy, flags.SnapshotKeepLast); err != nil {
		return err
	}
	if err := applyServiceSnapshotMaxAgeFlag(policy, flags.SnapshotMaxAge); err != nil {
		return err
	}
	if err := applyServiceSnapshotRequiredFlag(policy, flags.SnapshotRequired); err != nil {
		return err
	}
	return applyServiceSnapshotEventsFlag(policy, flags.SnapshotEvents)
}

func applyServiceSnapshotModeFlag(policy *db.SnapshotPolicy, value string) {
	switch value {
	case "on":
		v := true
		policy.Enabled = &v
	case "off":
		v := false
		policy.Enabled = &v
	}
}

func applyServiceSnapshotKeepLastFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.KeepLast = nil
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return fmt.Errorf("--snapshot-keep-last must be a positive integer or inherit")
	}
	policy.KeepLast = &n
	return nil
}

func applyServiceSnapshotMaxAgeFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.MaxAge = ""
		return nil
	}
	if _, err := parseSnapshotMaxAge(value); err != nil {
		return err
	}
	policy.MaxAge = value
	return nil
}

func applyServiceSnapshotRequiredFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.Required = nil
		return nil
	}
	v, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("invalid --snapshot-required value %q", value)
	}
	policy.Required = &v
	return nil
}

func applyServiceSnapshotEventsFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.Events = nil
		return nil
	}
	events, err := parseSnapshotEvents(value)
	if err != nil {
		return err
	}
	policy.Events = events
	return nil
}

func (e *ttyExecer) confirmServiceRootMigrationMode(mode serviceRootMigrationMode, plan serviceRootMigrationPlan) (serviceRootMigrationMode, error) {
	if mode != serviceRootMigrationPrompt {
		return mode, nil
	}
	if !e.isPty {
		return 0, serviceRootMigrationModeRequiredError()
	}
	ok, err := cmdutil.Confirm(e.rw, e.rw, fmt.Sprintf("Copy existing service files from %s to %s?", plan.OldRoot, plan.NewRoot))
	if err != nil {
		return 0, err
	}
	if ok {
		return serviceRootMigrationCopy, nil
	}
	return serviceRootMigrationEmpty, nil
}

func serviceRootMigrationModeRequiredError() error {
	return fmt.Errorf("service set --service-root requires --copy or --empty when not running interactively")
}

func (s *Server) validateServiceRootMigration(name string, request serviceRootMigrationRequest) (serviceRootMigrationPlan, error) {
	sv, err := s.stoppedServiceForRootMigration(name)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	oldRoot := filepath.Clean(s.serviceRootFromView(sv))
	oldRootZFS := sv.ServiceRootZFS()
	resolved, err := s.resolveServiceRootMigrationRequest(name, request, oldRoot, oldRootZFS)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	newRoot := filepath.Clean(resolved.Root)
	if err := rejectNoopServiceRootMigration(name, oldRoot, oldRootZFS, newRoot, resolved.Dataset); err != nil {
		return serviceRootMigrationPlan{}, err
	}
	if rootsAreNested(oldRoot, newRoot) || rootsAreNested(newRoot, oldRoot) {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, newRoot)
	}
	if !request.ZFS {
		newRoot, err = validateRequestedServiceRoot(newRoot)
		if err != nil {
			return serviceRootMigrationPlan{}, err
		}
	}
	return serviceRootMigrationPlan{ServiceName: name, OldRoot: oldRoot, OldRootZFS: oldRootZFS, NewRoot: newRoot, NewRootZFS: resolved.Dataset}, nil
}

func (s *Server) stoppedServiceForRootMigration(name string) (db.ServiceView, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return db.ServiceView{}, fmt.Errorf("service %q not found", name)
		}
		return db.ServiceView{}, err
	}
	running, err := isServiceRunningForRootMigration(s, name)
	if err != nil {
		return db.ServiceView{}, err
	}
	if running {
		return db.ServiceView{}, fmt.Errorf("cannot migrate service root while %q is running", name)
	}
	return sv, nil
}

func (s *Server) resolveServiceRootMigrationRequest(name string, request serviceRootMigrationRequest, oldRoot string, oldRootZFS string) (resolvedServiceRoot, error) {
	if request.ZFS {
		return s.resolveZFSServiceRootMigrationRequest(name, request.Root, oldRoot, oldRootZFS)
	}
	newRoot, err := cleanRequestedServiceRoot(request.Root)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	return resolvedServiceRoot{Root: newRoot}, nil
}

func (s *Server) resolveZFSServiceRootMigrationRequest(name, requestedDataset, oldRoot, oldRootZFS string) (resolvedServiceRoot, error) {
	dataset := strings.TrimSpace(requestedDataset)
	if dataset == "" {
		return resolvedServiceRoot{}, fmt.Errorf("--service-root is required when --zfs is set")
	}
	if oldRootZFS == dataset {
		return resolvedServiceRoot{}, fmt.Errorf("service %q is already using ZFS dataset %q", name, dataset)
	}

	runner := s.zfsRunner
	if runner == nil {
		runner = runZFSCommand
	}
	ctx := context.Background()
	exists, err := zfsDatasetExists(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	if !exists {
		if err := preflightMissingZFSServiceRootDataset(ctx, runner, name, oldRoot, oldRootZFS, dataset); err != nil {
			return resolvedServiceRoot{}, err
		}
		if err := zfsCreateDataset(ctx, runner, dataset); err != nil {
			return resolvedServiceRoot{}, err
		}
	}

	mountpoint, err := zfsDatasetMountpoint(ctx, runner, dataset)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	root, err := validateZFSMountpoint(mountpoint, zfsServiceRootTarget)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	return resolvedServiceRoot{Root: root, Dataset: dataset, ZFS: true}, nil
}

func preflightMissingZFSServiceRootDataset(ctx context.Context, runner zfsCommandRunner, name, oldRoot, oldRootZFS, dataset string) error {
	slash := strings.LastIndex(dataset, "/")
	if slash <= 0 || slash == len(dataset)-1 {
		return nil
	}
	parentDataset := dataset[:slash]
	childName := dataset[slash+1:]
	parentMountpoint, err := zfsDatasetMountpoint(ctx, runner, parentDataset)
	if err != nil {
		return err
	}
	parentRoot, err := validateZFSMountpoint(parentMountpoint, zfsServiceRootExisting)
	if err != nil {
		return err
	}
	predictedRoot := filepath.Join(parentRoot, childName)
	if err := rejectNoopServiceRootMigration(name, oldRoot, oldRootZFS, predictedRoot, dataset); err != nil {
		return err
	}
	if rootsAreNested(oldRoot, predictedRoot) || rootsAreNested(predictedRoot, oldRoot) {
		return fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, predictedRoot)
	}
	_, err = validateRequestedServiceRoot(predictedRoot)
	return err
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

func (s *Server) migrateServiceRoot(name string, request serviceRootMigrationRequest, mode serviceRootMigrationMode) error {
	plan, err := s.validateServiceRootMigration(name, request)
	if err != nil {
		return err
	}
	return s.migrateServiceRootWithPlan(plan, mode)
}

func (s *Server) migrateServiceRootWithPlan(plan serviceRootMigrationPlan, mode serviceRootMigrationMode) error {
	return s.migrateServiceRootWithPlanWriter(plan, mode, io.Discard)
}

func (s *Server) migrateServiceRootWithPlanWriter(plan serviceRootMigrationPlan, mode serviceRootMigrationMode, w io.Writer) error {
	oldService, err := s.serviceForRootMigrationPlan(plan)
	if err != nil {
		return err
	}
	return s.withServiceSnapshot(context.Background(), snapshotOperation{
		Service: oldService,
		Event:   snapshotEventServiceRootMigration,
		Writer:  w,
		Operation: func() error {
			if err := s.materializeServiceRootMigration(plan, mode); err != nil {
				return err
			}

			updatedService, err := s.updatedServiceForRootMigration(plan, mode, oldService)
			if err != nil {
				return err
			}
			if err := s.applyServiceRootMigrationRuntimeChanges(plan, mode, oldService, updatedService); err != nil {
				return err
			}
			if err := s.updateMigratedServiceRoot(plan, updatedService); err != nil {
				return err
			}
			return s.refreshServiceRootMigrationPrereqs(oldService, updatedService)
		},
	})
}

func (s *Server) materializeServiceRootMigration(plan serviceRootMigrationPlan, mode serviceRootMigrationMode) error {
	switch mode {
	case serviceRootMigrationCopy:
		if plan.NewRootZFS != "" {
			return copyServiceRootMigrationIntoMountedRoot(plan.OldRoot, plan.NewRoot)
		}
		return s.copyServiceRootMigration(plan.OldRoot, plan.NewRoot)
	case serviceRootMigrationEmpty:
		if plan.NewRootZFS != "" {
			return createEmptyMountedServiceRoot(plan.NewRoot)
		}
		return createEmptyServiceRoot(plan.NewRoot)
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
}

func (s *Server) updatedServiceForRootMigration(plan serviceRootMigrationPlan, mode serviceRootMigrationMode, oldService *db.Service) (*db.Service, error) {
	updatedService := oldService.Clone()
	if mode == serviceRootMigrationEmpty {
		updatedService.Artifacts = db.ArtifactStore{}
	} else if err := relocateServiceRootArtifactRefs(updatedService.Artifacts, plan.OldRoot, plan.NewRoot); err != nil {
		return nil, err
	} else if err := rewriteCopiedServiceRootArtifacts(updatedService.Artifacts, plan.OldRoot, plan.NewRoot); err != nil {
		return nil, err
	}
	s.applyServiceRoot(plan.ServiceName, updatedService, plan.NewRoot, plan.NewRootZFS)
	return updatedService, nil
}

func (s *Server) applyServiceRootMigrationRuntimeChanges(plan serviceRootMigrationPlan, mode serviceRootMigrationMode, oldService, updatedService *db.Service) error {
	if err := s.validateServiceRootMigrationPlanCurrent(plan); err != nil {
		return err
	}
	if err := downDockerComposeForRootMigration(s, oldService, plan.OldRoot); err != nil {
		return err
	}
	if mode == serviceRootMigrationEmpty {
		if serviceRootMigrationHasSystemdArtifacts(oldService) {
			if err := uninstallSystemdForRootMigration(s, oldService, plan.OldRoot); err != nil {
				return err
			}
		}
	} else if serviceRootMigrationNeedsSystemdInstall(oldService, updatedService) {
		if err := installSystemdForRootMigration(s, oldService, updatedService, plan.NewRoot); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) refreshServiceRootMigrationPrereqs(oldService, updatedService *db.Service) error {
	if serviceRootMigrationDockerNetNSPrereqsChanged(oldService, updatedService) {
		if err := installDockerPrereqs(s); err != nil {
			return fmt.Errorf("refresh docker prerequisites: %w", err)
		}
	}
	return nil
}

func (s *Server) validateServiceRootMigrationPlanCurrent(plan serviceRootMigrationPlan) error {
	_, err := s.serviceForRootMigrationPlan(plan)
	return err
}

func (s *Server) serviceForRootMigrationPlan(plan serviceRootMigrationPlan) (*db.Service, error) {
	sv, err := s.stoppedServiceForRootMigration(plan.ServiceName)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(s.serviceRootFromView(sv)) != filepath.Clean(plan.OldRoot) || sv.ServiceRootZFS() != plan.OldRootZFS {
		return nil, fmt.Errorf("service root for %q changed during migration planning", plan.ServiceName)
	}
	return sv.AsStruct(), nil
}

func (s *Server) copyServiceRootMigration(oldRoot, newRoot string) error {
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

	if err := copyServiceRootToStage(oldRoot, stage); err != nil {
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

	if err := copyServiceRootToStage(oldRoot, stage); err != nil {
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
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		if err := copyutil.TarDirectory(pw, srcRoot, ""); err != nil {
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

func (s *Server) applyServiceRoot(name string, service *db.Service, newRoot, newRootZFS string) {
	service.ServiceRootZFS = newRootZFS
	if newRootZFS == "" && filepath.Clean(newRoot) == filepath.Clean(s.defaultServiceRootDir(name)) {
		service.ServiceRoot = ""
	} else {
		service.ServiceRoot = newRoot
	}
}

func (s *Server) updateMigratedServiceRoot(plan serviceRootMigrationPlan, updatedService *db.Service) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		currentService, ok := d.Services[plan.ServiceName]
		if !ok {
			return fmt.Errorf("service %q not found", plan.ServiceName)
		}
		if filepath.Clean(s.serviceRootFromView(currentService.View())) != filepath.Clean(plan.OldRoot) || currentService.ServiceRootZFS != plan.OldRootZFS {
			return fmt.Errorf("service root for %q changed during migration planning", plan.ServiceName)
		}
		d.Services[plan.ServiceName] = updatedService.Clone()
		return nil
	})
	return err
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
	return !isRootPathByte(content[idx-1])
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
