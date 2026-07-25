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
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

var tailscaleResolverWriteManagedFile = writeTailscaleResolverManagedFile

type tailscaleResolverFleetTransaction struct {
	server  *Server
	ctx     context.Context
	plan    tailscaleResolverFleetPlan
	header  tailscaleResolverJournalHeader
	journal *tailscaleResolverJournal
}

func (s *Server) applyTailscaleResolverIsolationFleet(
	ctx context.Context,
	plan tailscaleResolverFleetPlan,
) error {
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
	names := make([]string, 0, len(plan.Services))
	for _, service := range plan.Services {
		names = append(names, service.ServiceName)
	}
	release := s.serviceOperationLocks.Lock(names...)
	defer release()
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
	if err := s.revalidateTailscaleResolverFleetPlan(ctx, plan); err != nil {
		return err
	}
	changed := changedTailscaleResolverFleetPlan(plan)
	if len(changed.Services) == 0 {
		return nil
	}
	header, err := newTailscaleResolverJournalHeader(changed)
	if err != nil {
		return err
	}
	journal, err := createTailscaleResolverJournal(s.cfg.RootDir, header)
	if err != nil {
		return s.blockTailscaleResolverRecovery(err)
	}
	transaction := tailscaleResolverFleetTransaction{
		server:  s,
		ctx:     ctx,
		plan:    changed,
		header:  header,
		journal: journal,
	}
	return transaction.run()
}

func (transaction *tailscaleResolverFleetTransaction) run() error {
	if err := transaction.journal.AppendPhase(tailscaleResolverPhasePrepared); err != nil {
		return transaction.fail(err)
	}
	if err := transaction.writeFiles(); err != nil {
		return transaction.fail(err)
	}
	if err := transaction.journal.AppendPhase(tailscaleResolverPhaseFilesWritten); err != nil {
		return transaction.fail(err)
	}
	if err := catchSystemctl("daemon-reload"); err != nil {
		return transaction.fail(fmt.Errorf(
			"systemctl daemon-reload after tailscale resolver fleet write: %w",
			err,
		))
	}
	if err := transaction.journal.AppendPhase(tailscaleResolverPhaseDaemonReloaded); err != nil {
		return transaction.fail(err)
	}
	if err := transaction.server.activateAndVerifyTailscaleResolverFleet(
		transaction.ctx,
		transaction.header.Services,
		true,
	); err != nil {
		return transaction.fail(err)
	}
	if err := transaction.journal.AppendPhase(tailscaleResolverPhaseServicesVerified); err != nil {
		return transaction.fail(err)
	}
	if err := transaction.journal.AppendPhase(tailscaleResolverPhaseCommitted); err != nil {
		return transaction.fail(err)
	}
	return transaction.finish()
}

func (transaction *tailscaleResolverFleetTransaction) writeFiles() error {
	filesByPath := tailscaleResolverPlanFilesByPath(transaction.plan)
	for _, record := range transaction.header.Files {
		file, ok := filesByPath[record.Path]
		if !ok {
			return fmt.Errorf(
				"tailscale resolver journal path %s is absent from its fleet plan",
				record.Path,
			)
		}
		if _, err := tailscaleResolverWriteManagedFile(
			file.Root,
			file.Relative,
			file.Proof,
			file.Next,
		); err != nil {
			return fmt.Errorf("write tailscale resolver unit %s: %w", file.Path, err)
		}
	}
	return nil
}

func (transaction *tailscaleResolverFleetTransaction) fail(cause error) error {
	closeErr := transaction.journal.Close()
	rollbackErr := transaction.server.rollbackTailscaleResolverJournal(
		context.WithoutCancel(transaction.ctx),
		transaction.journal.Path(),
		transaction.header,
	)
	if rollbackErr == nil {
		return errors.Join(cause, closeErr)
	}
	block := transaction.server.blockTailscaleResolverRecovery(fmt.Errorf(
		"rollback tailscale resolver transaction %s: %w",
		transaction.journal.Path(),
		rollbackErr,
	))
	return errors.Join(cause, closeErr, block)
}

func (transaction *tailscaleResolverFleetTransaction) finish() error {
	if err := transaction.journal.Close(); err != nil {
		return transaction.server.blockTailscaleResolverRecovery(err)
	}
	if err := removeTailscaleResolverJournal(transaction.journal.Path()); err != nil {
		return transaction.server.blockTailscaleResolverRecovery(err)
	}
	transaction.server.clearTailscaleResolverMutationBlock()
	return nil
}

func changedTailscaleResolverFleetPlan(plan tailscaleResolverFleetPlan) tailscaleResolverFleetPlan {
	changed := tailscaleResolverFleetPlan{CatchRunner: plan.CatchRunner}
	for _, service := range plan.Services {
		serviceChanged := false
		for _, file := range service.Files {
			if !bytes.Equal(file.Original, file.Next) {
				serviceChanged = true
				break
			}
		}
		if serviceChanged {
			changed.Services = append(changed.Services, service)
		}
	}
	return changed
}

func tailscaleResolverPlanFilesByPath(
	plan tailscaleResolverFleetPlan,
) map[string]tailscaleResolverUnitFilePlan {
	files := make(map[string]tailscaleResolverUnitFilePlan)
	for _, service := range plan.Services {
		for _, file := range service.Files {
			files[file.Path] = file
		}
	}
	return files
}

func writeTailscaleResolverManagedFile(
	root, relative string,
	expected serviceIdentityPathProof,
	content []byte,
) (proof serviceIdentityPathProof, retErr error) {
	if err := validateServiceIdentityPathProofRecord(expected, expected.Path); err != nil {
		return serviceIdentityPathProof{}, err
	}
	if !expected.Present {
		return serviceIdentityPathProof{}, fmt.Errorf("tailscale resolver unit %s must already exist", expected.Path)
	}
	parent, name, closeParent, err := openServiceIdentityMutationParent(root, relative)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	defer closeParent()
	if err := validateTailscaleResolverWriteTarget(parent, name, expected); err != nil {
		return serviceIdentityPathProof{}, err
	}
	staged, err := createTailscaleResolverStagedFile(parent, name, expected.Path)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, staged.cleanup())
	}()
	if err := staged.writeAndSync(expected, content); err != nil {
		return serviceIdentityPathProof{}, err
	}
	return staged.publish(expected, content)
}

type tailscaleResolverStagedFile struct {
	parent    int
	target    string
	temp      string
	path      string
	file      *os.File
	closed    bool
	published bool
}

func validateTailscaleResolverWriteTarget(
	parent int,
	name string,
	expected serviceIdentityPathProof,
) error {
	actual, err := captureServiceIdentityPathProofFromParent(parent, name, expected.Path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("tailscale resolver unit %s changed before atomic write", expected.Path)
	}
	return nil
}

func createTailscaleResolverStagedFile(
	parent int,
	name string,
	path string,
) (*tailscaleResolverStagedFile, error) {
	id, err := tailscaleResolverNewJournalID()
	if err != nil {
		return nil, err
	}
	tmpName := "." + name + ".yeet-resolver-" + id
	fd, err := unix.Openat(
		parent,
		tmpName,
		unix.O_RDWR|unix.O_CLOEXEC|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("create staged tailscale resolver unit for %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(filepath.Dir(path), tmpName))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap staged tailscale resolver unit for %s", path)
	}
	return &tailscaleResolverStagedFile{
		parent: parent,
		target: name,
		temp:   tmpName,
		path:   path,
		file:   file,
	}, nil
}

func (staged *tailscaleResolverStagedFile) cleanup() error {
	var err error
	if !staged.closed {
		err = staged.close()
	}
	if !staged.published {
		_ = unix.Unlinkat(staged.parent, staged.temp, 0)
	}
	return err
}

func (staged *tailscaleResolverStagedFile) close() error {
	staged.closed = true
	return staged.file.Close()
}

func (staged *tailscaleResolverStagedFile) writeAndSync(
	expected serviceIdentityPathProof,
	content []byte,
) error {
	n, err := staged.file.Write(content)
	if err != nil {
		return fmt.Errorf("write staged tailscale resolver unit %s: %w", staged.path, err)
	}
	if n != len(content) {
		return fmt.Errorf("write staged tailscale resolver unit %s: %w", staged.path, io.ErrShortWrite)
	}
	if err := staged.file.Chmod(expected.Mode.Perm()); err != nil {
		return fmt.Errorf("chmod staged tailscale resolver unit %s: %w", staged.path, err)
	}
	if err := staged.file.Chown(int(expected.UID), int(expected.GID)); err != nil {
		return fmt.Errorf("chown staged tailscale resolver unit %s: %w", staged.path, err)
	}
	if err := staged.file.Sync(); err != nil {
		return fmt.Errorf("sync staged tailscale resolver unit %s: %w", staged.path, err)
	}
	if err := staged.close(); err != nil {
		return fmt.Errorf("close staged tailscale resolver unit %s: %w", staged.path, err)
	}
	return nil
}

func (staged *tailscaleResolverStagedFile) publish(
	expected serviceIdentityPathProof,
	content []byte,
) (serviceIdentityPathProof, error) {
	actual, err := captureServiceIdentityPathProofFromParent(staged.parent, staged.target, staged.path)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	if !reflect.DeepEqual(actual, expected) {
		return serviceIdentityPathProof{}, fmt.Errorf(
			"tailscale resolver unit %s changed while staging replacement",
			staged.path,
		)
	}
	if err := unix.Renameat(staged.parent, staged.temp, staged.parent, staged.target); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("publish tailscale resolver unit %s: %w", staged.path, err)
	}
	staged.published = true
	if err := unix.Fsync(staged.parent); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf(
			"sync tailscale resolver unit parent for %s: %w",
			staged.path,
			err,
		)
	}
	proof, err := captureServiceIdentityPathProofFromParent(staged.parent, staged.target, staged.path)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	want := serviceIdentityDesiredFileState(
		expected.Path,
		content,
		expected.Mode,
		expected.UID,
		expected.GID,
	)
	if !serviceIdentityPathMatchesState(proof, want) {
		return serviceIdentityPathProof{}, fmt.Errorf(
			"published tailscale resolver unit %s has unexpected state",
			expected.Path,
		)
	}
	return proof, nil
}

func (s *Server) rollbackTailscaleResolverJournal(
	ctx context.Context,
	path string,
	header tailscaleResolverJournalHeader,
) error {
	if err := validateTailscaleResolverJournalHeader(header); err != nil {
		return err
	}
	if err := s.stopTailscaleResolverJournalServices(header.Services); err != nil {
		return err
	}
	current, err := preflightTailscaleResolverRollbackFiles(header.Files)
	if err != nil {
		return err
	}
	if err := restoreTailscaleResolverRollbackFiles(header.Files, current); err != nil {
		return err
	}
	if err := catchSystemctl("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload after tailscale resolver rollback: %w", err)
	}
	if err := s.activateAndVerifyTailscaleResolverFleet(ctx, header.Services, false); err != nil {
		return err
	}
	if err := verifyTailscaleResolverRollbackFiles(header.Files); err != nil {
		return err
	}
	return removeTailscaleResolverJournal(path)
}

func restoreTailscaleResolverRollbackFiles(
	files []tailscaleResolverJournalFile,
	current map[string]serviceIdentityPathProof,
) error {
	var restoreErrs []error
	for _, file := range files {
		actual := current[file.Path]
		if serviceIdentityPathStateEqual(actual, file.OriginalProof) {
			continue
		}
		root, relative := tailscaleResolverAbsoluteLocation(file.Path)
		if _, err := tailscaleResolverWriteManagedFile(
			root,
			relative,
			actual,
			file.Original,
		); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore tailscale resolver unit %s: %w", file.Path, err))
		}
	}
	if err := errors.Join(restoreErrs...); err != nil {
		return err
	}
	return nil
}

func (s *Server) stopTailscaleResolverJournalServices(
	services []tailscaleResolverJournalService,
) error {
	var errs []error
	for _, service := range services {
		if err := catchSystemctl("stop", service.UnitName); err != nil {
			errs = append(errs, fmt.Errorf("stop unproven tailscale resolver replacement %s: %w", service.UnitName, err))
		}
	}
	return errors.Join(errs...)
}

func preflightTailscaleResolverRollbackFiles(
	files []tailscaleResolverJournalFile,
) (map[string]serviceIdentityPathProof, error) {
	current := make(map[string]serviceIdentityPathProof, len(files))
	var errs []error
	for _, file := range files {
		root, relative := tailscaleResolverAbsoluteLocation(file.Path)
		actual, err := captureServiceIdentityPathProofAt(root, relative, file.Path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next := serviceIdentityDesiredFileState(
			file.Path,
			file.Next,
			file.OriginalProof.Mode,
			file.OriginalProof.UID,
			file.OriginalProof.GID,
		)
		if !serviceIdentityPathStateEqual(actual, file.OriginalProof) &&
			!serviceIdentityPathMatchesState(actual, next) {
			errs = append(errs, fmt.Errorf(
				"tailscale resolver unit %s matches neither original nor transaction replacement state",
				file.Path,
			))
			continue
		}
		current[file.Path] = actual
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return current, nil
}

func verifyTailscaleResolverRollbackFiles(files []tailscaleResolverJournalFile) error {
	var errs []error
	for _, file := range files {
		root, relative := tailscaleResolverAbsoluteLocation(file.Path)
		actual, err := captureServiceIdentityPathProofAt(root, relative, file.Path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !serviceIdentityPathStateEqual(actual, file.OriginalProof) {
			errs = append(errs, fmt.Errorf("tailscale resolver unit %s was not restored exactly", file.Path))
		}
	}
	return errors.Join(errs...)
}

func tailscaleResolverAbsoluteLocation(path string) (string, string) {
	return string(filepath.Separator), strings.TrimPrefix(path, string(filepath.Separator))
}

func (s *Server) activateAndVerifyTailscaleResolverFleet(
	ctx context.Context,
	services []tailscaleResolverJournalService,
	restart bool,
) error {
	for _, service := range services {
		if service.WasActive {
			var err error
			if restart {
				err = s.restartTailscaleSidecarForServiceLocked(ctx, service.ServiceName)
			} else {
				err = s.startTailscaleSidecarForService(ctx, service.ServiceName)
			}
			if err != nil {
				return fmt.Errorf("restore tailscale resolver lifecycle for %q: %w", service.ServiceName, err)
			}
			if err := s.verifyTailscaleSidecarForService(ctx, service.ServiceName); err != nil {
				return fmt.Errorf("verify tailscale resolver sidecar for %q: %w", service.ServiceName, err)
			}
			continue
		}
		active, err := tailscaleResolverUnitActive(service.UnitName)
		if err != nil {
			return fmt.Errorf("verify inactive tailscale resolver sidecar %s: %w", service.UnitName, err)
		}
		if active {
			return fmt.Errorf("inactive tailscale resolver sidecar %s became active", service.UnitName)
		}
	}
	return nil
}

func (s *Server) startTailscaleSidecarForService(ctx context.Context, name string) error {
	service, err := s.systemdService(name)
	if err != nil {
		return fmt.Errorf("load systemd service %q: %w", name, err)
	}
	return startTailscaleSystemdSidecar(ctx, service)
}
