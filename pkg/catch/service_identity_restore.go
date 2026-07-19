// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yeetrun/yeet/pkg/db"
)

type serviceIdentityTreeReconcileOps struct {
	inspect  func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error)
	apply    func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error
	validate func(string, db.ServiceIdentity) error
}

func reconcileServiceRootRestoreIdentityDefault(ctx context.Context, server *Server, service *db.Service, w io.Writer) error {
	identity, ok, err := persistedNativeServiceIdentity(service)
	if err != nil || !ok {
		return err
	}
	if server == nil {
		return fmt.Errorf("reconcile restored service %q identity: catch server is unavailable", service.Name)
	}
	_, err = server.migrateServiceIdentity(ctx, serviceIdentityMigrationRequest{
		Service:        service.Name,
		Requested:      identity.RequestedUser + ":" + identity.RequestedGroup,
		Target:         resolvedServiceIdentity{Persisted: identity},
		ForceReconcile: true,
	}, w)
	if err != nil {
		return fmt.Errorf("reconcile restored service %q identity: %w", service.Name, err)
	}
	return nil
}

func reconcileServiceRootCloneIdentityDefault(ctx context.Context, server *Server, service *db.Service, root string) error {
	identity, ok, err := persistedNativeServiceIdentity(service)
	if err != nil || !ok {
		return err
	}
	if server == nil {
		return fmt.Errorf("reconcile cloned service %q identity: catch server is unavailable", service.Name)
	}
	return reconcileUnpublishedNativeServiceTree(ctx, service, root, identity, server.zfsRunner, serviceIdentityTreeReconcileOps{
		inspect:  inspectServiceIdentityChange,
		apply:    serviceIdentityApplyMutation,
		validate: validateNativeServiceLayout,
	})
}

func persistedNativeServiceIdentity(service *db.Service) (db.ServiceIdentity, bool, error) {
	if service == nil || service.ServiceType != db.ServiceTypeSystemd || service.Name == CatchService || service.Name == SystemService || service.Identity == nil {
		return db.ServiceIdentity{}, false, nil
	}
	identity := *service.Identity
	if err := validateServiceIdentityDrift(identity); err != nil {
		return db.ServiceIdentity{}, false, fmt.Errorf("validate service %q identity: %w", service.Name, err)
	}
	return identity, true, nil
}

func reconcileUnpublishedNativeServiceTree(
	ctx context.Context,
	service *db.Service,
	root string,
	identity db.ServiceIdentity,
	runner zfsCommandRunner,
	ops serviceIdentityTreeReconcileOps,
) error {
	if ops.inspect == nil || ops.apply == nil || ops.validate == nil {
		return fmt.Errorf("reconcile cloned service identity requires inspection, mutation, and validation operations")
	}
	root = filepath.Clean(root)
	if err := validateHostControlledServiceRootPath(root); err != nil {
		return fmt.Errorf("validate cloned service %q identity root: %w", service.Name, err)
	}
	req := serviceIdentityInspectionRequest{Root: root, Dataset: service.ServiceRootZFS, Target: identity, ZFSRunner: runner}
	inspection, err := ops.inspect(ctx, req)
	if err != nil {
		return fmt.Errorf("inspect cloned service %q identity: %w", service.Name, err)
	}
	if err := applyUnpublishedServiceIdentityInspection(root, inspection, ops.apply); err != nil {
		return fmt.Errorf("apply cloned service %q identity: %w", service.Name, err)
	}
	return verifyUnpublishedNativeServiceTree(ctx, service, root, identity, req, ops)
}

func verifyUnpublishedNativeServiceTree(ctx context.Context, service *db.Service, root string, identity db.ServiceIdentity, req serviceIdentityInspectionRequest, ops serviceIdentityTreeReconcileOps) error {
	remaining, err := ops.inspect(ctx, req)
	if err != nil {
		return fmt.Errorf("verify cloned service %q identity: %w", service.Name, err)
	}
	if len(remaining.Records) != 0 || len(remaining.Mutations) != 0 {
		return fmt.Errorf("verify cloned service %q identity: %d ownership mutations remain", service.Name, len(remaining.Mutations))
	}
	if err := ops.validate(root, identity); err != nil {
		return fmt.Errorf("verify cloned service %q layout: %w", service.Name, err)
	}
	return nil
}

func applyUnpublishedServiceIdentityInspection(
	root string,
	inspection serviceIdentityInspection,
	apply func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error,
) error {
	if len(inspection.Records) != len(inspection.Mutations) {
		return fmt.Errorf("service identity inspection does not contain a complete mutation inventory")
	}
	for index, mutation := range inspection.Mutations {
		record := inspection.Records[index]
		rel, err := filepath.Rel(root, mutation.Path)
		if err != nil || rel != record.Path || record.Dev != mutation.Dev || record.Ino != mutation.Ino || (record.Mode&os.ModeSymlink != 0) != mutation.Symlink {
			return fmt.Errorf("service identity mutation %s does not match inspected inode %q", mutation.Path, record.Path)
		}
		if err := apply(root, rel, record, mutation.UID, mutation.GID, mutation.Mode, mutation.ChangeMode); err != nil {
			return fmt.Errorf("set service identity owner %s to %d:%d: %w", mutation.Path, mutation.UID, mutation.GID, err)
		}
	}
	return nil
}
