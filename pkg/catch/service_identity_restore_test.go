// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestReconcileUnpublishedNativeServiceTreeOwnsDescendantsBeforePublish(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(serviceDataDirForRoot(root), "nested", "state.db")
	if err := os.MkdirAll(filepath.Dir(nested), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("state"), 0o640); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	service := &db.Service{
		Name: "api-copy", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root,
		ServiceRootZFS: "tank/yeet/services/api-copy", Identity: &identity,
	}
	inspectCalls := 0
	var appliedPath string
	ops := serviceIdentityTreeReconcileOps{
		inspect: func(_ context.Context, req serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			inspectCalls++
			if req.Root != root || req.Dataset != service.ServiceRootZFS || req.Target != identity {
				t.Fatalf("inspection request = %#v", req)
			}
			if inspectCalls > 1 {
				return serviceIdentityInspection{}, nil
			}
			return serviceIdentityInspection{
				Records:   []serviceIdentityInodeRecord{{Path: filepath.Join("data", "nested", "state.db"), Mode: 0o640, Dev: 9, Ino: 10}},
				Mutations: []serviceIdentityMutation{{Path: nested, UID: identity.UID, GID: identity.GID, Mode: 0o640, Dev: 9, Ino: 10}},
			}, nil
		},
		apply: func(gotRoot, rel string, _ serviceIdentityInodeRecord, uid, gid uint32, _ os.FileMode, _ bool) error {
			if gotRoot != root || uid != identity.UID || gid != identity.GID {
				t.Fatalf("apply = root:%q rel:%q ids:%d:%d", gotRoot, rel, uid, gid)
			}
			appliedPath = rel
			return nil
		},
		validate: func(gotRoot string, gotIdentity db.ServiceIdentity) error {
			if gotRoot != root || gotIdentity != identity {
				t.Fatalf("validate = root:%q identity:%#v", gotRoot, gotIdentity)
			}
			return nil
		},
	}

	if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, ops); err != nil {
		t.Fatalf("reconcileUnpublishedNativeServiceTree: %v", err)
	}
	if appliedPath != filepath.Join("data", "nested", "state.db") {
		t.Fatalf("applied descendant = %q", appliedPath)
	}
	if inspectCalls != 2 {
		t.Fatalf("inspection calls = %d, want apply plus verification scan", inspectCalls)
	}
}

func TestServiceIdentityMigrationForceReconcileIsNotNoop(t *testing.T) {
	identity := db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "app", UID: 1001, GID: 1001}
	service := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Identity: &identity}
	migration := &serviceIdentityMigration{
		req:      serviceIdentityMigrationRequest{ForceReconcile: true},
		previous: service,
		target:   service.Clone(),
	}
	if migration.isNoop() {
		t.Fatal("forced ownership reconciliation was treated as a no-op")
	}
}

func TestServiceIdentityRestoreAndCloneReconcileFailures(t *testing.T) {
	identity := db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "app", UID: 1001, GID: 1001}
	service := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Identity: &identity}
	if err := reconcileServiceRootRestoreIdentityDefault(context.Background(), nil, service, os.Stdout); err == nil {
		t.Fatal("restore reconciliation accepted a missing server")
	}
	if err := reconcileServiceRootCloneIdentityDefault(context.Background(), nil, service, "/var/lib/yeet/services/api"); err == nil {
		t.Fatal("clone reconciliation accepted a missing server")
	}
	if err := reconcileServiceRootRestoreIdentityDefault(context.Background(), nil, &db.Service{Name: "compose", ServiceType: db.ServiceTypeDockerCompose}, os.Stdout); err != nil {
		t.Fatalf("ineligible restore reconciliation = %v", err)
	}

	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	baseOps := serviceIdentityTreeReconcileOps{
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:    func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error { return nil },
		validate: func(string, db.ServiceIdentity) error { return nil },
	}
	if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, serviceIdentityTreeReconcileOps{}); err == nil {
		t.Fatal("missing reconcile operations were accepted")
	}
	if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, "relative", identity, nil, baseOps); err == nil {
		t.Fatal("relative reconcile root was accepted")
	}

	t.Run("inspection", func(t *testing.T) {
		ops := baseOps
		ops.inspect = func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, errors.New("inspect failed")
		}
		if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, ops); err == nil {
			t.Fatal("inspection failure was ignored")
		}
	})
	t.Run("incomplete inventory", func(t *testing.T) {
		ops := baseOps
		ops.inspect = func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{Records: []serviceIdentityInodeRecord{{Path: "data"}}}, nil
		}
		if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, ops); err == nil {
			t.Fatal("incomplete mutation inventory was accepted")
		}
	})
	t.Run("mutation", func(t *testing.T) {
		ops := baseOps
		ops.inspect = func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{
				Records:   []serviceIdentityInodeRecord{{Path: "data", Dev: 1, Ino: 2}},
				Mutations: []serviceIdentityMutation{{Path: filepath.Join(root, "data"), Dev: 1, Ino: 2}},
			}, nil
		}
		ops.apply = func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error {
			return errors.New("apply failed")
		}
		if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, ops); err == nil {
			t.Fatal("mutation failure was ignored")
		}
	})
	t.Run("verification inspection", func(t *testing.T) {
		ops := baseOps
		calls := 0
		ops.inspect = func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			calls++
			if calls == 2 {
				return serviceIdentityInspection{}, errors.New("verify failed")
			}
			return serviceIdentityInspection{}, nil
		}
		if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, ops); err == nil {
			t.Fatal("verification inspection failure was ignored")
		}
	})
	t.Run("remaining mutations", func(t *testing.T) {
		ops := baseOps
		calls := 0
		ops.inspect = func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			calls++
			if calls == 2 {
				return serviceIdentityInspection{Mutations: []serviceIdentityMutation{{Path: filepath.Join(root, "data")}}}, nil
			}
			return serviceIdentityInspection{}, nil
		}
		if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, ops); err == nil {
			t.Fatal("remaining mutations were accepted")
		}
	})
	t.Run("layout validation", func(t *testing.T) {
		ops := baseOps
		ops.validate = func(string, db.ServiceIdentity) error { return errors.New("invalid layout") }
		if err := reconcileUnpublishedNativeServiceTree(context.Background(), service, root, identity, nil, ops); err == nil {
			t.Fatal("layout validation failure was ignored")
		}
	})
}
