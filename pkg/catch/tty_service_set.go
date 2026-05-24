// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"errors"
	"fmt"
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

type serviceRootMigrationPlan struct {
	ServiceName string
	OldRoot     string
	NewRoot     string
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

	plan, err := e.s.validateServiceRootMigration(e.sn, flags.ServiceRoot)
	if err != nil {
		return err
	}
	mode, err = e.confirmServiceRootMigrationMode(mode, plan)
	if err != nil {
		return err
	}
	return e.s.migrateServiceRoot(plan.ServiceName, plan.OldRoot, plan.NewRoot, mode)
}

func (e *ttyExecer) confirmServiceRootMigrationMode(mode serviceRootMigrationMode, plan serviceRootMigrationPlan) (serviceRootMigrationMode, error) {
	if mode != serviceRootMigrationPrompt {
		return mode, nil
	}
	if !e.isPty {
		return 0, fmt.Errorf("service set --service-root requires --copy or --empty when not running interactively")
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

func (s *Server) validateServiceRootMigration(name, dst string) (serviceRootMigrationPlan, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return serviceRootMigrationPlan{}, fmt.Errorf("service %q not found", name)
		}
		return serviceRootMigrationPlan{}, err
	}
	running, err := isServiceRunningForRootMigration(s, name)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	if running {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate service root while %q is running", name)
	}
	newRoot, err := cleanRequestedServiceRoot(dst)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	oldRoot := filepath.Clean(s.serviceRootFromView(sv))
	if oldRoot == newRoot {
		return serviceRootMigrationPlan{}, fmt.Errorf("service root for %q is already %s", name, oldRoot)
	}
	if rootsAreNested(oldRoot, newRoot) || rootsAreNested(newRoot, oldRoot) {
		return serviceRootMigrationPlan{}, fmt.Errorf("cannot migrate between nested service roots: %s and %s", oldRoot, newRoot)
	}
	newRoot, err = validateRequestedServiceRoot(newRoot)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	return serviceRootMigrationPlan{ServiceName: name, OldRoot: oldRoot, NewRoot: newRoot}, nil
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

func (s *Server) migrateServiceRoot(name, oldRoot, newRoot string, mode serviceRootMigrationMode) error {
	if _, err := s.validateServiceRootMigration(name, newRoot); err != nil {
		return err
	}
	switch mode {
	case serviceRootMigrationCopy:
		if err := s.copyServiceRootMigration(oldRoot, newRoot); err != nil {
			return err
		}
	case serviceRootMigrationEmpty:
		if err := createEmptyServiceRoot(newRoot); err != nil {
			return err
		}
	default:
		return fmt.Errorf("service root migration mode was not selected")
	}
	return s.updateServiceRoot(name, newRoot)
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

func copyServiceRootToStage(srcRoot, stageRoot string) error {
	var buf bytes.Buffer
	if err := copyutil.TarDirectory(&buf, srcRoot, ""); err != nil {
		return fmt.Errorf("archive service root: %w", err)
	}
	if err := copyutil.ExtractTarWithOptions(&buf, stageRoot, copyutil.ExtractOptions{}); err != nil {
		return fmt.Errorf("extract service root archive: %w", err)
	}
	return nil
}

func createEmptyServiceRoot(root string) error {
	if err := removeRetrySafeServiceRootSkeleton(root); err != nil {
		return err
	}
	return ensureDirsForRoot(root, "")
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

func (s *Server) updateServiceRoot(name, newRoot string) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		svc, ok := d.Services[name]
		if !ok {
			return fmt.Errorf("service %q not found", name)
		}
		if filepath.Clean(newRoot) == filepath.Clean(s.defaultServiceRootDir(name)) {
			svc.ServiceRoot = ""
		} else {
			svc.ServiceRoot = newRoot
		}
		return nil
	})
	return err
}
