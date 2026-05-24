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
	"github.com/yeetrun/yeet/pkg/copyutil"
	"github.com/yeetrun/yeet/pkg/db"
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
	renameServiceRoot = os.Rename
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
	return e.s.migrateServiceRootWithPlan(plan, mode)
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
		if err := preflightMissingZFSServiceRootDataset(ctx, runner, oldRoot, dataset); err != nil {
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

func preflightMissingZFSServiceRootDataset(ctx context.Context, runner zfsCommandRunner, oldRoot, dataset string) error {
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
	if err := s.validateServiceRootMigrationPlanCurrent(plan); err != nil {
		return err
	}
	var err error
	switch mode {
	case serviceRootMigrationCopy:
		if plan.NewRootZFS != "" {
			err = copyServiceRootMigrationIntoMountedRoot(plan.OldRoot, plan.NewRoot)
		} else {
			err = s.copyServiceRootMigration(plan.OldRoot, plan.NewRoot)
		}
		if err != nil {
			return err
		}
	case serviceRootMigrationEmpty:
		if plan.NewRootZFS != "" {
			err = createEmptyMountedServiceRoot(plan.NewRoot)
		} else {
			err = createEmptyServiceRoot(plan.NewRoot)
		}
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
	return s.updateServiceRoot(plan.ServiceName, plan.NewRoot, plan.NewRootZFS)
}

func (s *Server) validateServiceRootMigrationPlanCurrent(plan serviceRootMigrationPlan) error {
	sv, err := s.stoppedServiceForRootMigration(plan.ServiceName)
	if err != nil {
		return err
	}
	if filepath.Clean(s.serviceRootFromView(sv)) != filepath.Clean(plan.OldRoot) || sv.ServiceRootZFS() != plan.OldRootZFS {
		return fmt.Errorf("service root for %q changed during migration planning", plan.ServiceName)
	}
	return nil
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

func (s *Server) updateServiceRoot(name, newRoot, newRootZFS string) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		svc, ok := d.Services[name]
		if !ok {
			return fmt.Errorf("service %q not found", name)
		}
		svc.ServiceRootZFS = newRootZFS
		if newRootZFS == "" && filepath.Clean(newRoot) == filepath.Clean(s.defaultServiceRootDir(name)) {
			svc.ServiceRoot = ""
		} else {
			svc.ServiceRoot = newRoot
		}
		return nil
	})
	return err
}
