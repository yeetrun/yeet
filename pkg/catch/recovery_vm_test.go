// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

const (
	vmRecoveryDataset  = "flash/yeet/vms/devbox/vm/d-abc/root"
	vmRecoverySnapshot = vmRecoveryDataset + "@yeet-20260613T203100Z-vm-manual-g0"
	vmRecoveryZVOLSize = "10737418240"
)

func TestSnapshotsCloneVMHappyPath(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	logPath := installFakeSystemctl(t)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})
	var out bytes.Buffer

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, &out)
	if err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}

	wantDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	if !hasRecoveryCall(calls, "clone "+vmRecoverySnapshot+" "+wantDataset) {
		t.Fatalf("zfs calls = %#v, want clone to %s", calls, wantDataset)
	}
	cloned := mustService(t, server, "devbox-copy")
	if cloned.ServiceType != db.ServiceTypeVM || cloned.VM == nil {
		t.Fatalf("cloned service = %#v, want VM", cloned)
	}
	if cloned.VM.Disk.Path != "/dev/zvol/"+wantDataset {
		t.Fatalf("cloned VM disk path = %q, want target zvol", cloned.VM.Disk.Path)
	}
	for label, path := range map[string]string{
		"service root":        cloned.ServiceRoot,
		"service root zfs":    cloned.ServiceRootZFS,
		"console socket":      cloned.VM.Console.SocketPath,
		"console log":         cloned.VM.Console.LogPath,
		"api socket":          cloned.VM.Sockets.APISocketPath,
		"pid file":            cloned.VM.PIDFile,
		"target zvol dataset": wantDataset,
	} {
		if hasNameSegment(path, "devbox") {
			t.Fatalf("%s = %q, still contains old devbox segment", label, path)
		}
	}
	if cloned.SvcNetwork != nil {
		t.Fatalf("cloned service network = %#v, want cleared to avoid duplicate IP", cloned.SvcNetwork)
	}
	for _, network := range cloned.VM.Networks {
		if hasNameSegment(network.Tap, "devbox") || hasNameSegment(network.Interface, "devbox") {
			t.Fatalf("cloned network = %#v, want no old service identity", network)
		}
		if network.Mode == "svc" && network.IP.IsValid() {
			t.Fatalf("cloned service network IP = %s, want cleared", network.IP)
		}
		if network.MAC == "02:fc:c0:7d:a0:74" {
			t.Fatalf("cloned network MAC = %s, want regenerated", network.MAC)
		}
	}
	if got := readRecoveryLog(t, logPath); strings.Contains(got, "systemctl start") {
		t.Fatalf("systemctl log = %q, clone without --start should not start VM", got)
	}
}

func TestSnapshotsCloneVMRejectsStartBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	logPath := installFakeSystemctl(t)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{Start: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "starting VM clones is not supported yet; run snapshots clone without --start") {
		t.Fatalf("cloneRecoveryPoint error = %v, want unsupported --start rejection", err)
	}
	if hasRecoveryCall(calls, "clone ") {
		t.Fatalf("zfs calls = %#v, --start rejection should not clone", calls)
	}
	if serviceExists(t, server, "devbox-copy") {
		t.Fatal("devbox-copy exists after --start rejection; want no DB insert")
	}
	if got := readRecoveryLog(t, logPath); strings.Contains(got, "systemctl start") {
		t.Fatalf("systemctl log = %q, --start rejection should not start VM", got)
	}
}

func TestSnapshotsCloneVMRejectsInvalidTargetNamesBeforeMutation(t *testing.T) {
	for _, name := range []string{
		"",
		"   ",
		"catch",
		"sys",
		"system",
		"default",
		"bad/name",
		`bad\name`,
		"bad@name",
		"bad..name",
		" devbox",
		"devbox ",
		"bad name",
		"bad\tname",
		"bad\nname",
	} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			server := newTestServer(t)
			seedVMRecoverySource(t, server)
			beforeServices := serviceCount(t, server)
			var calls []string
			server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
				vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
			})

			err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", name, cli.SnapshotsCloneFlags{}, io.Discard)

			if err == nil || !strings.Contains(err.Error(), "invalid clone target service name") {
				t.Fatalf("cloneRecoveryPoint error = %v, want invalid name rejection", err)
			}
			if len(calls) != 0 {
				t.Fatalf("zfs calls = %#v, invalid target name should not touch ZFS", calls)
			}
			if afterServices := serviceCount(t, server); afterServices != beforeServices {
				t.Fatalf("service count = %d, want unchanged %d after invalid target rejection", afterServices, beforeServices)
			}
		})
	}
}

func TestSnapshotsCloneVMRejectsExistingTarget(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox-copy", func(_ *db.Data, service *db.Service) error {
		service.Name = "devbox-copy"
		service.ServiceType = db.ServiceTypeVM
		service.VM = (&db.VMConfig{}).Clone()
		return nil
	}); err != nil {
		t.Fatalf("seed existing target: %v", err)
	}
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), `service "devbox-copy" already exists`) {
		t.Fatalf("cloneRecoveryPoint error = %v, want existing target rejection", err)
	}
	if hasRecoveryCall(calls, "clone ") {
		t.Fatalf("zfs calls = %#v, existing target should not clone", calls)
	}
}

func TestSnapshotsCloneVMRejectsPreExistingTargetDataset(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return targetDataset + "\n", "", nil
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "target VM zvol dataset "+targetDataset+" already exists") {
		t.Fatalf("cloneRecoveryPoint error = %v, want existing target dataset rejection", err)
	}
	if hasRecoveryCall(calls, "clone ") || hasRecoveryCall(calls, "destroy ") {
		t.Fatalf("zfs calls = %#v, existing target dataset should not clone or destroy", calls)
	}
	if serviceExists(t, server, "devbox-copy") {
		t.Fatal("devbox-copy exists after target dataset rejection; want no DB insert")
	}
}

func TestSnapshotsCloneVMCreatesMissingTargetParentBeforeClone(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	created := map[string]bool{}
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if strings.HasPrefix(command, "list -H -o name ") {
			dataset := strings.TrimPrefix(command, "list -H -o name ")
			switch dataset {
			case "flash/yeet", "flash/yeet/vms":
				return dataset + "\n", "", nil
			case targetDataset, targetParent, "flash/yeet/vms/devbox-copy", "flash/yeet/vms/devbox-copy/vm":
				return "", "dataset does not exist", errors.New("dataset does not exist")
			default:
				return "", "unexpected list " + dataset, errors.New("unexpected list")
			}
		}
		if strings.HasPrefix(command, "create ") {
			dataset := strings.TrimPrefix(command, "create ")
			created[dataset] = true
			return "", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			for _, dataset := range []string{"flash/yeet/vms/devbox-copy", "flash/yeet/vms/devbox-copy/vm", targetParent} {
				if !created[dataset] {
					return "", "cannot create '" + targetDataset + "': parent does not exist", errors.New("parent does not exist")
				}
			}
			if len(created) != 3 {
				return "", "cannot create '" + targetDataset + "': parent does not exist", errors.New("parent does not exist")
			}
			return "", "", nil
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)
	if err != nil {
		t.Fatalf("cloneRecoveryPoint: %v; zfs calls = %#v", err, calls)
	}

	assertExactRecoveryCallOrder(t, calls,
		"list -H -o name "+targetDataset,
		"list -H -o name "+targetParent,
		"list -H -o name flash/yeet",
		"list -H -o name flash/yeet/vms",
		"list -H -o name flash/yeet/vms/devbox-copy",
		"create flash/yeet/vms/devbox-copy",
		"list -H -o name flash/yeet/vms/devbox-copy/vm",
		"create flash/yeet/vms/devbox-copy/vm",
		"create "+targetParent,
		"clone "+vmRecoverySnapshot+" "+targetDataset,
	)
	if hasRecoveryCall(calls, "create -p ") {
		t.Fatalf("zfs calls = %#v, parent creation must not use zfs create -p", calls)
	}
}

func TestSnapshotsCloneVMDestroysCreatedParentsDeepestFirstAfterFailedZFSClone(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if strings.HasPrefix(command, "list -H -o name ") {
			dataset := strings.TrimPrefix(command, "list -H -o name ")
			switch dataset {
			case "flash/yeet", "flash/yeet/vms":
				return dataset + "\n", "", nil
			case targetDataset, targetParent, "flash/yeet/vms/devbox-copy", "flash/yeet/vms/devbox-copy/vm":
				return "", "dataset does not exist", errors.New("dataset does not exist")
			default:
				return "", "unexpected list " + dataset, errors.New("unexpected list")
			}
		}
		if strings.HasPrefix(command, "create ") {
			return "", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			return "", "cannot create '" + targetDataset + "': clone failed", errors.New("clone failed")
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs clone "+vmRecoverySnapshot+" "+targetDataset+" failed: cannot create '"+targetDataset+"': clone failed") {
		t.Fatalf("cloneRecoveryPoint error = %v, want original clone failure", err)
	}
	assertExactRecoveryCallOrder(t, calls,
		"create flash/yeet/vms/devbox-copy",
		"create flash/yeet/vms/devbox-copy/vm",
		"create "+targetParent,
		"clone "+vmRecoverySnapshot+" "+targetDataset,
		"destroy "+targetParent,
		"destroy flash/yeet/vms/devbox-copy/vm",
		"destroy flash/yeet/vms/devbox-copy",
	)
	if hasRecoveryCall(calls, "destroy -r ") {
		t.Fatalf("zfs calls = %#v, parent cleanup must not destroy recursively", calls)
	}
	if serviceExists(t, server, "devbox-copy") {
		t.Fatal("devbox-copy exists after failed zfs clone; want no DB insert")
	}
}

func TestSnapshotsCloneVMDestroysOnlyMissingDescendantsBelowExistingParentAfterFailedZFSClone(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	existingParent := "flash/yeet/vms/devbox-copy"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if strings.HasPrefix(command, "list -H -o name ") {
			dataset := strings.TrimPrefix(command, "list -H -o name ")
			switch dataset {
			case "flash/yeet", "flash/yeet/vms", existingParent:
				return dataset + "\n", "", nil
			case targetDataset, targetParent, "flash/yeet/vms/devbox-copy/vm":
				return "", "dataset does not exist", errors.New("dataset does not exist")
			default:
				return "", "unexpected list " + dataset, errors.New("unexpected list")
			}
		}
		if strings.HasPrefix(command, "create ") {
			return "", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			return "", "cannot create '" + targetDataset + "': clone failed", errors.New("clone failed")
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs clone "+vmRecoverySnapshot+" "+targetDataset+" failed: cannot create '"+targetDataset+"': clone failed") {
		t.Fatalf("cloneRecoveryPoint error = %v, want original clone failure", err)
	}
	assertExactRecoveryCallOrder(t, calls,
		"list -H -o name "+targetParent,
		"list -H -o name "+existingParent,
		"create flash/yeet/vms/devbox-copy/vm",
		"create "+targetParent,
		"clone "+vmRecoverySnapshot+" "+targetDataset,
		"destroy "+targetParent,
		"destroy flash/yeet/vms/devbox-copy/vm",
	)
	if lineIndexEqual(calls, "destroy "+existingParent) >= 0 || hasRecoveryCall(calls, "destroy -r ") {
		t.Fatalf("zfs calls = %#v, failed clone should destroy only operation-created descendants", calls)
	}
	if serviceExists(t, server, "devbox-copy") {
		t.Fatal("devbox-copy exists after failed zfs clone; want no DB insert")
	}
}

func TestSnapshotsCloneVMDoesNotDestroyExistingParentAfterFailedZFSClone(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if command == "list -H -o name "+targetParent {
			return targetParent + "\n", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			return "", "cannot create '" + targetDataset + "': clone failed", errors.New("clone failed")
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs clone "+vmRecoverySnapshot+" "+targetDataset+" failed: cannot create '"+targetDataset+"': clone failed") {
		t.Fatalf("cloneRecoveryPoint error = %v, want original clone failure", err)
	}
	assertExactRecoveryCallOrder(t, calls,
		"list -H -o name "+targetDataset,
		"list -H -o name "+targetParent,
		"clone "+vmRecoverySnapshot+" "+targetDataset,
	)
	if hasRecoveryCall(calls, "destroy -r "+targetParent) || hasRecoveryCall(calls, "destroy -r "+targetDataset) {
		t.Fatalf("zfs calls = %#v, failed clone should not destroy existing parent or uncreated target", calls)
	}
	if serviceExists(t, server, "devbox-copy") {
		t.Fatal("devbox-copy exists after failed zfs clone; want no DB insert")
	}
}

func TestSnapshotsCloneVMDoesNotDestroyConcurrentlyCreatedParentAfterFailedZFSClone(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if strings.HasPrefix(command, "list -H -o name ") {
			dataset := strings.TrimPrefix(command, "list -H -o name ")
			switch dataset {
			case "flash/yeet", "flash/yeet/vms":
				return dataset + "\n", "", nil
			case targetDataset, targetParent, "flash/yeet/vms/devbox-copy", "flash/yeet/vms/devbox-copy/vm":
				return "", "dataset does not exist", errors.New("dataset does not exist")
			default:
				return "", "unexpected list " + dataset, errors.New("unexpected list")
			}
		}
		if command == "create flash/yeet/vms/devbox-copy" {
			return "", "", nil
		}
		if command == "create flash/yeet/vms/devbox-copy/vm" {
			return "", "cannot create 'flash/yeet/vms/devbox-copy/vm': dataset already exists", errors.New("dataset already exists")
		}
		if command == "create "+targetParent {
			return "", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			return "", "cannot create '" + targetDataset + "': clone failed", errors.New("clone failed")
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs clone "+vmRecoverySnapshot+" "+targetDataset+" failed: cannot create '"+targetDataset+"': clone failed") {
		t.Fatalf("cloneRecoveryPoint error = %v, want original clone failure", err)
	}
	assertExactRecoveryCallOrder(t, calls,
		"create flash/yeet/vms/devbox-copy",
		"create flash/yeet/vms/devbox-copy/vm",
		"create "+targetParent,
		"clone "+vmRecoverySnapshot+" "+targetDataset,
		"destroy "+targetParent,
		"destroy flash/yeet/vms/devbox-copy",
	)
	if lineIndexEqual(calls, "destroy flash/yeet/vms/devbox-copy/vm") >= 0 || hasRecoveryCall(calls, "destroy -r ") {
		t.Fatalf("zfs calls = %#v, cleanup must not destroy concurrently-created parent", calls)
	}
}

func TestSnapshotsCloneVMDestroysAfterSuccessfulCloneDBInsertFailure(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if command == "list -H -o name "+targetParent {
			return targetParent + "\n", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			if _, _, err := server.cfg.DB.MutateService("devbox-copy", func(_ *db.Data, service *db.Service) error {
				service.Name = "devbox-copy"
				service.ServiceType = db.ServiceTypeVM
				service.VM = (&db.VMConfig{}).Clone()
				return nil
			}); err != nil {
				t.Fatalf("seed concurrent target service: %v", err)
			}
			return "", "", nil
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), `service "devbox-copy" already exists`) {
		t.Fatalf("cloneRecoveryPoint error = %v, want DB insert failure", err)
	}
	if !hasRecoveryCall(calls, "destroy -r "+targetDataset) {
		t.Fatalf("zfs calls = %#v, want cleanup destroy after DB insert failure", calls)
	}
	if !serviceExists(t, server, "devbox-copy") {
		t.Fatal("concurrent devbox-copy service was removed after DB insert race")
	}
}

func TestSnapshotsCloneVMDestroysCreatedParentsAfterSuccessfulCloneDBInsertFailure(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	existingParent := "flash/yeet/vms/devbox-copy"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if strings.HasPrefix(command, "list -H -o name ") {
			dataset := strings.TrimPrefix(command, "list -H -o name ")
			switch dataset {
			case "flash/yeet", "flash/yeet/vms", existingParent:
				return dataset + "\n", "", nil
			case targetDataset, targetParent, "flash/yeet/vms/devbox-copy/vm":
				return "", "dataset does not exist", errors.New("dataset does not exist")
			default:
				return "", "unexpected list " + dataset, errors.New("unexpected list")
			}
		}
		if strings.HasPrefix(command, "create ") {
			return "", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			if _, _, err := server.cfg.DB.MutateService("devbox-copy", func(_ *db.Data, service *db.Service) error {
				service.Name = "devbox-copy"
				service.ServiceType = db.ServiceTypeVM
				service.VM = (&db.VMConfig{}).Clone()
				return nil
			}); err != nil {
				t.Fatalf("seed concurrent target service: %v", err)
			}
			return "", "", nil
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), `service "devbox-copy" already exists`) {
		t.Fatalf("cloneRecoveryPoint error = %v, want DB insert failure", err)
	}
	assertExactRecoveryCallOrder(t, calls,
		"create flash/yeet/vms/devbox-copy/vm",
		"create "+targetParent,
		"clone "+vmRecoverySnapshot+" "+targetDataset,
		"destroy -r "+targetDataset,
		"destroy "+targetParent,
		"destroy flash/yeet/vms/devbox-copy/vm",
	)
	if lineIndexEqual(calls, "destroy "+existingParent) >= 0 || lineIndexEqual(calls, "destroy -r "+existingParent) >= 0 {
		t.Fatalf("zfs calls = %#v, DB insert cleanup should not destroy pre-existing parent", calls)
	}
	if !serviceExists(t, server, "devbox-copy") {
		t.Fatal("concurrent devbox-copy service was removed after DB insert race")
	}
}

func TestSnapshotsCloneVMRemovesInsertedServiceAfterDBSaveFailure(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	dbPath := filepath.Join(server.cfg.RootDir, "db.json")
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove db file: %v", err)
	}
	if err := os.Mkdir(dbPath, 0o755); err != nil {
		t.Fatalf("make db path unsaveable: %v", err)
	}
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if command == "list -H -o name "+targetParent {
			return targetParent + "\n", "", nil
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "failed to save data") {
		t.Fatalf("cloneRecoveryPoint error = %v, want DB save failure", err)
	}
	if !hasRecoveryCall(calls, "destroy -r "+targetDataset) {
		t.Fatalf("zfs calls = %#v, want cleanup destroy after DB save failure", calls)
	}
	if serviceExists(t, server, "devbox-copy") {
		t.Fatal("devbox-copy remains in memory after DB save failure cleanup")
	}
}

func TestSnapshotsCloneVMRemovesInsertedServiceAfterDBSaveFailureWithParentCleanupFailure(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	dbPath := filepath.Join(server.cfg.RootDir, "db.json")
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove db file: %v", err)
	}
	if err := os.Mkdir(dbPath, 0o755); err != nil {
		t.Fatalf("make db path unsaveable: %v", err)
	}
	targetDataset := "flash/yeet/vms/devbox-copy/vm/d-abc/root"
	targetParent := "flash/yeet/vms/devbox-copy/vm/d-abc"
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if command == "list -H -o name "+targetDataset {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if strings.HasPrefix(command, "list -H -o name ") {
			dataset := strings.TrimPrefix(command, "list -H -o name ")
			switch dataset {
			case "flash/yeet", "flash/yeet/vms":
				return dataset + "\n", "", nil
			case targetDataset, targetParent, "flash/yeet/vms/devbox-copy", "flash/yeet/vms/devbox-copy/vm":
				return "", "dataset does not exist", errors.New("dataset does not exist")
			default:
				return "", "unexpected list " + dataset, errors.New("unexpected list")
			}
		}
		if strings.HasPrefix(command, "create ") {
			return "", "", nil
		}
		if command == "clone "+vmRecoverySnapshot+" "+targetDataset {
			return "", "", nil
		}
		if command == "destroy -r "+targetDataset {
			return "", "", nil
		}
		if command == "destroy "+targetParent {
			return "", "cannot destroy '" + targetParent + "': dataset is busy", errors.New("dataset is busy")
		}
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "failed to save data") {
		t.Fatalf("cloneRecoveryPoint error = %v, want DB save failure", err)
	}
	if !strings.Contains(err.Error(), "dataset is busy") {
		t.Fatalf("cloneRecoveryPoint error = %v, want parent cleanup failure", err)
	}
	assertExactRecoveryCallOrder(t, calls,
		"create flash/yeet/vms/devbox-copy",
		"create flash/yeet/vms/devbox-copy/vm",
		"create "+targetParent,
		"clone "+vmRecoverySnapshot+" "+targetDataset,
		"destroy -r "+targetDataset,
		"destroy "+targetParent,
	)
	if serviceExists(t, server, "devbox-copy") {
		t.Fatal("devbox-copy remains in memory after DB save failure cleanup with parent cleanup failure")
	}
}

func TestSnapshotsCloneVMRejectsUnsupportedZVOLLayout(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	const dataset = "flash/yeet/shared/root"
	const snapshot = dataset + "@yeet-20260613T203100Z-vm-manual-g0"
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Disk.Path = "/dev/zvol/" + dataset
		return nil
	}); err != nil {
		t.Fatalf("set unsupported disk path: %v", err)
	}
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		dataset: vmRecoverySnapshotLine(snapshot, "devbox", recoveryModeDisk),
	})

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "unsupported VM zvol layout") {
		t.Fatalf("cloneRecoveryPoint error = %v, want unsupported layout rejection", err)
	}
	if hasRecoveryCall(calls, "clone ") {
		t.Fatalf("zfs calls = %#v, unsupported layout should not clone", calls)
	}
}

func TestSnapshotsCloneVMRejectsAmbiguousZVOLLayout(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	const dataset = "flash/yeet/devbox/vm/devbox/root"
	const snapshot = dataset + "@yeet-20260613T203100Z-vm-manual-g0"
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Disk.Path = "/dev/zvol/" + dataset
		return nil
	}); err != nil {
		t.Fatalf("set ambiguous disk path: %v", err)
	}
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		dataset: vmRecoverySnapshotLine(snapshot, "devbox", recoveryModeDisk),
	})

	err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "ambiguous VM zvol layout") {
		t.Fatalf("cloneRecoveryPoint error = %v, want ambiguous layout rejection", err)
	}
	if hasRecoveryCall(calls, "clone ") {
		t.Fatalf("zfs calls = %#v, ambiguous layout should not clone", calls)
	}
}

func TestSnapshotsCloneVMClearsTSNet(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.TSNet = &db.TailscaleNetwork{
			Interface: "tailscale0",
			Version:   "1.2.3",
			Tags:      []string{"tag:vm"},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed TSNet: %v", err)
	}
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	if err := server.cloneRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", "devbox-copy", cli.SnapshotsCloneFlags{}, io.Discard); err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}

	if cloned := mustService(t, server, "devbox-copy"); cloned.TSNet != nil {
		t.Fatalf("cloned TSNet = %#v, want nil to avoid duplicate identity", cloned.TSNet)
	}
}

func TestSnapshotsRestoreVMRequiresStopForRunningVM(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusRunning)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "VM devbox is running; pass --stop to stop it before restore") {
		t.Fatalf("restoreRecoveryPoint error = %v, want --stop requirement", err)
	}
	if hasRecoveryCall(calls, "rollback ") || hasRecoveryCall(calls, "snapshot ") {
		t.Fatalf("zfs calls = %#v, running rejection should not mutate", calls)
	}
}

func TestSnapshotsRestoreVMCloneCopiesSelectedSnapshot(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	oldWaiter := vmRestoreZVOLDeviceWaiter
	vmRestoreZVOLDeviceWaiter = func(_ context.Context, devices ...string) error {
		appendRecoveryLog(t, logPath, "wait-zvol "+strings.Join(devices, " "))
		return nil
	}
	t.Cleanup(func() { vmRestoreZVOLDeviceWaiter = oldWaiter })
	oldController := vmSnapshotFirecracker
	vmSnapshotFirecracker = &recordingVMFirecracker{calls: &[]string{}}
	t.Cleanup(func() { vmSnapshotFirecracker = oldController })
	server.zfsRunner = vmRecoveryLoggedZFSRunner(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})
	var out bytes.Buffer

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Stop: true, Yes: true}, &out)
	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v", err)
	}

	lines := readRecoveryLogLines(t, logPath)
	assertCallOrder(t, lines, "systemctl stop yeet-vm-devbox.service", "zfs snapshot")
	cloneLine := requireLinePrefix(t, lines, "zfs clone "+vmRecoverySnapshot+" ")
	tempDataset := strings.TrimPrefix(cloneLine, "zfs clone "+vmRecoverySnapshot+" ")
	tempSizeLine := "zfs get -Hp -o value volsize " + tempDataset
	activeSizeLine := "zfs get -Hp -o value volsize " + vmRecoveryDataset
	waitLine := "wait-zvol /dev/zvol/" + tempDataset + " /dev/zvol/" + vmRecoveryDataset
	copyLine := "dd if=/dev/zvol/" + tempDataset + " of=/dev/zvol/" + vmRecoveryDataset
	assertCallOrder(t, lines,
		"zfs snapshot",
		"zfs clone "+vmRecoverySnapshot+" "+tempDataset,
		waitLine,
		copyLine,
		"zfs destroy -r "+tempDataset,
	)
	tempSizeIdx := lineIndexEqual(lines, tempSizeLine)
	activeSizeIdx := lineIndexEqual(lines, activeSizeLine)
	waitIdx := lineIndexEqual(lines, waitLine)
	copyIdx := lineIndexContaining(lines, copyLine)
	if tempSizeIdx < 0 || activeSizeIdx < 0 || waitIdx < 0 || copyIdx < 0 || !(tempSizeIdx < activeSizeIdx && activeSizeIdx < waitIdx && waitIdx < copyIdx) {
		t.Fatalf("calls = %#v, want temp and active zvol size queries and device wait before copy", lines)
	}
	if hasRecoveryCall(lines, "zfs rollback ") {
		t.Fatalf("calls = %#v, restore should not use zfs rollback", lines)
	}
	for _, want := range []string{
		"Pre-restore recovery point:",
		"Stopped service: devbox",
		"Restored VM disk: " + vmRecoverySnapshot,
		"Restore complete.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, missing %q", out.String(), want)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "com.yeetrun:comment=pre-restore before yeet-20260613T203100Z-vm-manual-g0") {
		t.Fatalf("calls = %#v, want pre-restore snapshot comment", lines)
	}
}

func TestSnapshotsRestoreVMPreRestoreSnapshotRunsWhenRoutineSnapshotsDisabled(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.SnapshotPolicy = &db.SnapshotPolicy{Enabled: boolPointer(false)}
		return nil
	}); err != nil {
		t.Fatalf("disable snapshot policy: %v", err)
	}
	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	server.zfsRunner = vmRecoveryLoggedZFSRunner(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true}, ioDiscardReadWriter{})
	lines := readRecoveryLogLines(t, logPath)

	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v; calls = %#v", err, lines)
	}
	snapshotLine := requireLinePrefix(t, lines, "zfs snapshot ")
	if !strings.Contains(snapshotLine, "com.yeetrun:event=vm-manual") ||
		!strings.Contains(snapshotLine, "com.yeetrun:comment=pre-restore before yeet-20260613T203100Z-vm-manual-g0") {
		t.Fatalf("pre-restore snapshot command = %q, want VM manual safety snapshot with restore comment", snapshotLine)
	}
}

func TestSnapshotsRestoreVMCopyFailureDestroysTemp(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := filepath.Join(t.TempDir(), "restore.log")
	server.zfsRunner = vmRecoveryLoggedZFSRunner(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})
	copyFailure := errors.New("copy failed after temp clone")
	oldCopyRunner := vmRestoreCopyRunner
	var copiedFrom string
	var copiedTo string
	vmRestoreCopyRunner = func(_ context.Context, sourceDevice string, targetDevice string) error {
		copiedFrom = sourceDevice
		copiedTo = targetDevice
		return copyFailure
	}
	t.Cleanup(func() { vmRestoreCopyRunner = oldCopyRunner })

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true}, ioDiscardReadWriter{})
	lines := readRecoveryLogLines(t, logPath)

	if !errors.Is(err, copyFailure) {
		t.Fatalf("restoreRecoveryPoint error = %v, want wrapped copy failure; calls = %#v", err, lines)
	}
	cloneLine := requireLinePrefix(t, lines, "zfs clone "+vmRecoverySnapshot+" ")
	tempDataset := strings.TrimPrefix(cloneLine, "zfs clone "+vmRecoverySnapshot+" ")
	if copiedFrom != "/dev/zvol/"+tempDataset || copiedTo != "/dev/zvol/"+vmRecoveryDataset {
		t.Fatalf("copy = %q -> %q, want temp zvol to active zvol", copiedFrom, copiedTo)
	}
	if !hasRecoveryCall(lines, "zfs destroy -r "+tempDataset) {
		t.Fatalf("calls = %#v, want temp clone destroy after copy failure", lines)
	}
	if hasRecoveryCall(lines, "zfs rollback ") {
		t.Fatalf("calls = %#v, restore should not use zfs rollback", lines)
	}
}

func TestSnapshotsRestoreVMRejectsZVOLSizeMismatchBeforeCopy(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	server.zfsRunner = vmRecoveryLoggedZFSRunnerWithSizes(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	}, map[string]string{
		vmRecoveryDataset: "8589934592",
	}, "10737418240")

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true}, ioDiscardReadWriter{})
	lines := readRecoveryLogLines(t, logPath)

	if err == nil || !strings.Contains(err.Error(), "VM zvol size mismatch") {
		t.Fatalf("restoreRecoveryPoint error = %v, want zvol size mismatch; calls = %#v", err, lines)
	}
	cloneLine := requireLinePrefix(t, lines, "zfs clone "+vmRecoverySnapshot+" ")
	tempDataset := strings.TrimPrefix(cloneLine, "zfs clone "+vmRecoverySnapshot+" ")
	if !hasRecoveryCall(lines, "zfs destroy -r "+tempDataset) {
		t.Fatalf("calls = %#v, want temp clone destroy after size mismatch", lines)
	}
	if strings.Contains(readRecoveryLog(t, logPath), "dd ") {
		t.Fatalf("calls = %#v, size mismatch should not copy", lines)
	}
}

func TestSnapshotsRestoreVMSizeQueryFailureDestroysTempBeforeCopy(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		appendRecoveryLog(t, logPath, "zfs "+command)
		if isRecoverySnapshotList(args) {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if strings.HasPrefix(command, "get -Hp -o value volsize ") {
			return "", "volsize unavailable", errors.New("volsize unavailable")
		}
		return "", "", nil
	}

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true}, ioDiscardReadWriter{})
	lines := readRecoveryLogLines(t, logPath)

	if err == nil || !strings.Contains(err.Error(), "zfs get volsize") || !strings.Contains(err.Error(), "volsize unavailable") {
		t.Fatalf("restoreRecoveryPoint error = %v, want volsize query failure; calls = %#v", err, lines)
	}
	cloneLine := requireLinePrefix(t, lines, "zfs clone "+vmRecoverySnapshot+" ")
	tempDataset := strings.TrimPrefix(cloneLine, "zfs clone "+vmRecoverySnapshot+" ")
	if !hasRecoveryCall(lines, "zfs destroy -r "+tempDataset) {
		t.Fatalf("calls = %#v, want temp clone destroy after size query failure", lines)
	}
	if strings.Contains(readRecoveryLog(t, logPath), "dd ") {
		t.Fatalf("calls = %#v, size query failure should not copy", lines)
	}
}

func TestSnapshotsRestoreVMPreRestoreFailureStopsBeforeNoMoreMutation(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		appendRecoveryLog(t, logPath, "zfs "+command)
		if strings.HasPrefix(command, "list ") {
			return vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk), "", nil
		}
		if strings.HasPrefix(command, "snapshot ") {
			return "", "snapshot failed", errors.New("snapshot failed")
		}
		return "", "", nil
	}

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Stop: true, Yes: true}, ioDiscardReadWriter{})
	lines := readRecoveryLogLines(t, logPath)

	if err == nil || !strings.Contains(err.Error(), "zfs snapshot") {
		t.Fatalf("restoreRecoveryPoint error = %v, want pre-restore snapshot failure; calls = %#v", err, lines)
	}
	assertCallOrder(t, lines, "systemctl stop yeet-vm-devbox.service", "zfs snapshot")
	for _, forbidden := range []string{"zfs clone ", "dd ", "zfs destroy ", "zfs rollback "} {
		if hasRecoveryCall(lines, forbidden) {
			t.Fatalf("calls = %#v, pre-restore failure should not run %q", lines, forbidden)
		}
	}
}

func TestSnapshotsRestoreVMFullModeFailsBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	seedFullVMCheckpointMetadata(t, root, "devbox", vmRecoverySnapshot)
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "full checkpoint metadata is missing compatibility fields") {
		t.Fatalf("restoreRecoveryPoint error = %v, want old metadata compatibility rejection", err)
	}
	if hasRecoveryCall(calls, "snapshot ") || hasRecoveryCall(calls, "clone ") || hasRecoveryCall(calls, "destroy ") || hasRecoveryCall(calls, "rollback ") {
		t.Fatalf("zfs calls = %#v, full mode should not mutate disk", calls)
	}
	if got := readRecoveryLog(t, logPath); strings.Contains(got, "systemctl stop") || strings.Contains(got, "dd ") {
		t.Fatalf("system command log = %q, full mode should not stop or copy", got)
	}
}

func TestSnapshotsRestoreVMFullRejectsCPUMemoryMismatchBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.CPUs = 8
		service.VM.MemoryBytes = 8 << 30
		return nil
	}); err != nil {
		t.Fatalf("mutate VM shape: %v", err)
	}
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "checkpoint CPU or memory does not match current VM config") {
		t.Fatalf("restoreRecoveryPoint error = %v, want CPU/memory compatibility rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullRejectsDiskPathMismatchBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		metadata["diskPath"] = "/dev/zvol/flash/yeet/vms/devbox/vm/d-old/root"
	})
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "checkpoint disk path does not match current VM config") {
		t.Fatalf("restoreRecoveryPoint error = %v, want disk path compatibility rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullRejectsVMConfigHashMismatchBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.SSH.User = "root"
		return nil
	}); err != nil {
		t.Fatalf("mutate VM config: %v", err)
	}
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "checkpoint VM config hash does not match current VM config") {
		t.Fatalf("restoreRecoveryPoint error = %v, want VM config hash compatibility rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullRejectsBalloonConfigHashMismatchBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Balloon = db.VMBalloonConfig{Mode: vmBalloonModeAuto, MinBytes: 1 << 30, StatsIntervalSeconds: vmBalloonDefaultStatsIntervalSeconds}
		return nil
	}); err != nil {
		t.Fatalf("seed VM balloon config: %v", err)
	}
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Balloon.MinBytes = 2 << 30
		return nil
	}); err != nil {
		t.Fatalf("mutate VM balloon config: %v", err)
	}
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "checkpoint balloon config hash does not match current VM config") {
		t.Fatalf("restoreRecoveryPoint error = %v, want balloon config hash compatibility rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullRejectsMissingCheckpointFilesBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	_, memoryPath := seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, nil)
	if err := os.Remove(memoryPath); err != nil {
		t.Fatalf("remove checkpoint memory file: %v", err)
	}
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "full checkpoint state or memory file is missing") {
		t.Fatalf("restoreRecoveryPoint error = %v, want missing checkpoint file rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullRejectsMissingFirecrackerIdentityBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, nil)
	identity := installVMRecoveryFirecrackerLauncher(t, root, "Firecracker v1.7.0-test")
	stubVMRecoveryFirecrackerVersion(t, identity.Version)
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "full checkpoint metadata is missing compatibility fields") {
		t.Fatalf("restoreRecoveryPoint error = %v, want missing Firecracker identity rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullRejectsFirecrackerSHAMismatchBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	identity := installVMRecoveryFirecrackerLauncher(t, root, "Firecracker v1.7.0-test")
	stubVMRecoveryFirecrackerVersion(t, identity.Version)
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		metadata["firecrackerSha256"] = "sha256:" + strings.Repeat("0", 64)
		metadata["firecrackerVersion"] = identity.Version
	})
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "checkpoint Firecracker binary hash does not match current launcher") {
		t.Fatalf("restoreRecoveryPoint error = %v, want Firecracker SHA mismatch rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullRejectsFirecrackerVersionMismatchBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	identity := installVMRecoveryFirecrackerLauncher(t, root, "Firecracker v1.7.0-test")
	stubVMRecoveryFirecrackerVersion(t, identity.Version)
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		metadata["firecrackerSha256"] = identity.SHA256
		metadata["firecrackerVersion"] = "Firecracker v9.9.9-other"
	})
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "checkpoint Firecracker version does not match current launcher") {
		t.Fatalf("restoreRecoveryPoint error = %v, want Firecracker version mismatch rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestValidateFullVMCheckpointFirecrackerIdentityNormalizesVersionNoise(t *testing.T) {
	err := validateFullVMCheckpointFirecrackerIdentity(
		vmCheckpointMetadata{
			FirecrackerSha256:  "sha256:" + strings.Repeat("a", 64),
			FirecrackerVersion: "Firecracker v1.14.3\n\n2026-06-14T11:38:52.280711996 [anonymous-instance:main] Firecracker exiting successfully. exit_code=0",
		},
		vmCheckpointCompatibility{
			FirecrackerSha256:  "sha256:" + strings.Repeat("a", 64),
			FirecrackerVersion: "Firecracker v1.14.3",
		},
	)
	if err != nil {
		t.Fatalf("validateFullVMCheckpointFirecrackerIdentity: %v, want noisy metadata version accepted", err)
	}
}

func TestValidateFullVMCheckpointRequiresBalloonConfigHash(t *testing.T) {
	metadata := vmCheckpointMetadata{
		Mode:              recoveryModeFull,
		FirecrackerState:  "/tmp/state",
		FirecrackerMemory: "/tmp/memory",
		MachineConfigHash: "sha256:" + strings.Repeat("a", 64),
		NetworkConfigHash: "sha256:" + strings.Repeat("b", 64),
		DiskPath:          "/dev/zvol/tank/vms/devbox/root",
		VCPU:              4,
		MemoryMiB:         4096,
		VMConfigHash:      "sha256:" + strings.Repeat("c", 64),
	}

	if metadata.hasFullCompatibilityFields() {
		t.Fatalf("hasFullCompatibilityFields = true without balloonConfigHash; want false")
	}
}

func TestSnapshotsRestoreVMFullReportsMissingBalloonCompatibilityField(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		delete(metadata, "balloonConfigHash")
	})
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "balloonConfigHash") {
		t.Fatalf("restoreRecoveryPoint error = %v, want missing balloonConfigHash compatibility rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMFullCompatibleCheckpointRestoresDiskSchedulesStateLoadAndStarts(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	identity := installVMRecoveryFirecrackerLauncher(t, root, "Firecracker v1.7.0-test")
	statePath, memoryPath := seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		metadata["firecrackerSha256"] = identity.SHA256
		metadata["firecrackerVersion"] = identity.Version
	})
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	resultPath := vmFullRestoreResultPath(filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"))
	t.Setenv("YEET_TEST_VM_RESTORE_RESULT", resultPath)
	var calls []string
	zfsRunner := vmRecoveryLoggedZFSRunner(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		return zfsRunner(ctx, args...)
	}

	var out bytes.Buffer
	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Stop: true, Yes: true, Mode: recoveryModeFull}, &out)
	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v; zfs calls = %#v; system calls = %q", err, calls, readRecoveryLog(t, logPath))
	}

	lines := readRecoveryLogLines(t, logPath)
	assertCallOrder(t, lines, "systemctl stop yeet-vm-devbox.service", "zfs snapshot", "systemctl daemon-reload", "dd ", "systemctl start yeet-vm-devbox.service")
	requestPath := vmFullRestoreRequestPath(filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"))
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatalf("read full restore request: %v", err)
	}
	var request vmFullRestoreRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatalf("decode full restore request: %v", err)
	}
	if request.StatePath != statePath || request.MemoryPath != memoryPath || !request.Resume {
		t.Fatalf("restore request = %#v, want state=%q memory=%q resume=true", request, statePath, memoryPath)
	}
	output := out.String()
	for _, want := range []string{
		"Pre-restore recovery point:",
		"Restored VM disk: " + vmRecoverySnapshot,
		"Scheduled full VM state restore: yeet-20260613T203100Z",
		"Started service: devbox",
		"Restored full VM state: yeet-20260613T203100Z",
		"Restore complete.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestSnapshotsRestoreVMFullRequiresRestorableSystemdUnitBeforeDiskMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	identity := installVMRecoveryFirecrackerLauncher(t, root, "Firecracker v1.7.0-test")
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		metadata["firecrackerSha256"] = identity.SHA256
		metadata["firecrackerVersion"] = identity.Version
	})
	unitPath := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName("devbox"))
	rawUnit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read VM systemd unit: %v", err)
	}
	brokenUnit := strings.ReplaceAll(string(rawUnit), "RestartForceExitStatus=75\n", "")
	brokenUnit = strings.ReplaceAll(brokenUnit, "RestartPreventExitStatus=76\n", "")
	if err := os.WriteFile(unitPath, []byte(brokenUnit), 0o644); err != nil {
		t.Fatalf("write broken VM systemd unit: %v", err)
	}

	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	zfsRunner := vmRecoveryLoggedZFSRunner(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		return zfsRunner(ctx, args...)
	}

	err = server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})
	if err == nil {
		t.Fatal("restoreRecoveryPoint error = nil, want systemd restore-prevent failure")
	}
	for _, want := range []string{
		"does not contain RestartForceExitStatus=75",
		"pre-restore recovery point:",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("restoreRecoveryPoint error = %v, want %q", err, want)
		}
	}
	if !hasRecoveryCall(calls, "snapshot ") {
		t.Fatalf("zfs calls = %#v, want pre-restore snapshot", calls)
	}
	if strings.Contains(readRecoveryLog(t, logPath), "dd ") {
		t.Fatalf("system command log = %q, disk copy should not run before restore startup is prepared", readRecoveryLog(t, logPath))
	}
}

func TestSnapshotsRestoreVMFullStartWithoutLoadResultFailsWithPreRestorePoint(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	identity := installVMRecoveryFirecrackerLauncher(t, root, "Firecracker v1.7.0-test")
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, func(metadata map[string]any) {
		metadata["firecrackerSha256"] = identity.SHA256
		metadata["firecrackerVersion"] = identity.Version
	})
	withVMRecoveryStatus(t, svc.StatusRunning)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	zfsRunner := vmRecoveryLoggedZFSRunner(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeFull),
	})
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		return zfsRunner(ctx, args...)
	}

	oldTimeout := vmFullRestoreResultWaitTimeout
	oldInterval := vmFullRestoreResultWaitInterval
	vmFullRestoreResultWaitTimeout = 50 * time.Millisecond
	vmFullRestoreResultWaitInterval = time.Millisecond
	t.Cleanup(func() {
		vmFullRestoreResultWaitTimeout = oldTimeout
		vmFullRestoreResultWaitInterval = oldInterval
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Stop: true, Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})
	if err == nil {
		t.Fatal("restoreRecoveryPoint error = nil, want missing restore-load result error")
	}
	for _, want := range []string{
		"full VM state restore did not report completion",
		"pre-restore recovery point:",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("restoreRecoveryPoint error = %v, want %q", err, want)
		}
	}
	if !hasRecoveryCall(calls, "snapshot ") || !strings.Contains(readRecoveryLog(t, logPath), "systemctl start yeet-vm-devbox.service") {
		t.Fatalf("expected disk restore and VM start before load-result failure; zfs calls=%#v log=%q", calls, readRecoveryLog(t, logPath))
	}
}

func TestWaitForFullVMStateRestoreToleratesTransientPartialResult(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	installFakeSystemctl(t)
	service := mustService(t, server, "devbox")
	resultPath := vmFullRestoreResultPath(service.VM.Sockets.APISocketPath)
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o755); err != nil {
		t.Fatalf("mkdir restore result dir: %v", err)
	}
	if err := os.WriteFile(resultPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("write partial restore result: %v", err)
	}

	oldTimeout := vmFullRestoreResultWaitTimeout
	oldInterval := vmFullRestoreResultWaitInterval
	vmFullRestoreResultWaitTimeout = 5 * time.Second
	vmFullRestoreResultWaitInterval = time.Millisecond
	t.Cleanup(func() {
		vmFullRestoreResultWaitTimeout = oldTimeout
		vmFullRestoreResultWaitInterval = oldInterval
	})
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = writeVMFullRestoreResult(resultPath, vmFullRestoreResult{Status: vmFullRestoreStatusSuccess})
	}()

	if err := server.waitForFullVMStateRestore(context.Background(), service, "pre-restore-point"); err != nil {
		t.Fatalf("waitForFullVMStateRestore: %v", err)
	}
}

func TestSnapshotsRestoreVMFullRequiresFullRecoveryPointBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedCompatibleFullVMCheckpointMetadata(t, server, root, vmRecoverySnapshot, nil)
	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Mode: recoveryModeFull}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "is not a full VM checkpoint") {
		t.Fatalf("restoreRecoveryPoint error = %v, want full checkpoint rejection", err)
	}
	assertNoFullVMRestoreMutation(t, calls, logPath)
}

func TestSnapshotsRestoreVMPreRestoreDoesNotPrune(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusStopped)
	logPath := installFakeSystemctl(t)
	installFakeDD(t, logPath)
	server.zfsRunner = vmRecoveryLoggedZFSRunner(t, logPath, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})

	if err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true}, ioDiscardReadWriter{}); err != nil {
		t.Fatalf("restoreRecoveryPoint: %v", err)
	}

	lines := readRecoveryLogLines(t, logPath)
	snapshotIdx := lineIndexContaining(lines, "zfs snapshot")
	if snapshotIdx < 0 {
		t.Fatalf("calls = %#v, missing pre-restore snapshot", lines)
	}
	for _, line := range lines[snapshotIdx+1:] {
		if strings.HasPrefix(line, "zfs list ") {
			t.Fatalf("calls = %#v, pre-restore snapshot should not trigger retention pruning list", lines)
		}
	}
}

func TestSnapshotsRestoreVMConfirmationDeclinedCancels(t *testing.T) {
	server := newTestServer(t)
	seedVMRecoverySource(t, server)
	withVMRecoveryStatus(t, svc.StatusStopped)
	var calls []string
	server.zfsRunner = vmRecoveryZFSRunner(t, &calls, map[string]string{
		vmRecoveryDataset: vmRecoverySnapshotLine(vmRecoverySnapshot, "devbox", recoveryModeDisk),
	})
	rw := bytes.NewBufferString("n\n")

	err := server.restoreRecoveryPoint(context.Background(), "devbox", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{}, rw)
	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v", err)
	}

	if !strings.Contains(rw.String(), "Restore cancelled.") {
		t.Fatalf("output = %q, want cancellation message", rw.String())
	}
	if hasRecoveryCall(calls, "rollback ") || hasRecoveryCall(calls, "snapshot ") {
		t.Fatalf("zfs calls = %#v, declined confirmation should not mutate", calls)
	}
}

func seedVMRecoverySource(t *testing.T, server *Server) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "services", "devbox")
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.ServiceRoot = "/srv/yeet/services/devbox"
		service.ServiceRootZFS = "flash/yeet/services/devbox"
		service.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.12")}
		service.VM.Console.SocketPath = "/run/yeet/devbox/serial.sock"
		service.VM.Console.LogPath = "/run/yeet/devbox/serial.log"
		service.VM.Sockets.APISocketPath = "/run/yeet/devbox/firecracker.sock"
		service.VM.PIDFile = "/run/yeet/devbox/firecracker.pid"
		service.VM.Networks = []db.VMNetworkConfig{{
			Mode:      "svc",
			Interface: "eth0",
			Tap:       "yvm-devbox-s0",
			MAC:       "02:fc:c0:7d:a0:74",
			IP:        netip.MustParseAddr("192.168.100.12"),
		}}
		return nil
	}); err != nil {
		t.Fatalf("seed VM recovery source: %v", err)
	}
}

func vmRecoverySnapshotLine(snapshot string, serviceName string, mode string) string {
	return fmt.Sprintf("%s\t1781382660\tcatch\t%s\tvm-manual\t0\tbefore upgrade\t%s\tfalse\n", snapshot, serviceName, mode)
}

func vmRecoveryZFSRunner(t *testing.T, calls *[]string, lists map[string]string) zfsCommandRunner {
	t.Helper()
	return func(_ context.Context, args ...string) (string, string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if len(args) > 0 && args[0] == "list" {
			if out, ok := lists[args[len(args)-1]]; ok {
				return out, "", nil
			}
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		return "", "", nil
	}
}

func vmRecoveryLoggedZFSRunner(t *testing.T, logPath string, lists map[string]string) zfsCommandRunner {
	t.Helper()
	return vmRecoveryLoggedZFSRunnerWithSizes(t, logPath, lists, map[string]string{
		vmRecoveryDataset: vmRecoveryZVOLSize,
	}, vmRecoveryZVOLSize)
}

func vmRecoveryLoggedZFSRunnerWithSizes(t *testing.T, logPath string, lists map[string]string, sizes map[string]string, defaultSize string) zfsCommandRunner {
	t.Helper()
	return func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		appendRecoveryLog(t, logPath, "zfs "+command)
		if len(args) > 0 && args[0] == "list" {
			return lists[args[len(args)-1]], "", nil
		}
		if strings.HasPrefix(command, "get -Hp -o value volsize ") {
			dataset := strings.TrimPrefix(command, "get -Hp -o value volsize ")
			if size, ok := sizes[dataset]; ok {
				return size + "\n", "", nil
			}
			return defaultSize + "\n", "", nil
		}
		return "", "", nil
	}
}

func isRecoverySnapshotList(args []string) bool {
	if len(args) <= 4 || args[0] != "list" {
		return false
	}
	hasType := false
	hasSnapshot := false
	for _, arg := range args {
		if arg == "-t" {
			hasType = true
		}
		if arg == "snapshot" {
			hasSnapshot = true
		}
	}
	return hasType && hasSnapshot
}

func withVMRecoveryStatus(t *testing.T, status svc.Status) {
	t.Helper()
	old := serverVMStatusFunc
	serverVMStatusFunc = func(string) (svc.Status, error) { return status, nil }
	t.Cleanup(func() { serverVMStatusFunc = old })
}

type vmRecoveryFirecrackerIdentity struct {
	SHA256  string
	Version string
}

func installVMRecoveryFirecrackerLauncher(t *testing.T, root string, version string) vmRecoveryFirecrackerIdentity {
	t.Helper()
	firecrackerBinary := filepath.Join(root, "firecracker")
	firecrackerBytes := []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo " + strconv.Quote(version) + "; exit 0; fi\nexit 1\n")
	if err := os.WriteFile(firecrackerBinary, firecrackerBytes, 0o755); err != nil {
		t.Fatalf("write firecracker binary: %v", err)
	}
	systemdDir := t.TempDir()
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		DataDir:          "/srv/catch/data",
		ServicesRoot:     "/srv/services",
		ServiceRoot:      root,
		DiskPath:         filepath.Join(serviceDataDirForRoot(root), "rootfs.raw"),
		Firecracker:      firecrackerBinary,
		Jailer:           filepath.Join(root, "jailer"),
		JailerBase:       filepath.Join(root, "jails"),
		ConfigPath:       filepath.Join(serviceRunDirForRoot(root), "firecracker.json"),
		APISocket:        filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"),
		ConsoleSocket:    filepath.Join(serviceRunDirForRoot(root), "serial.sock"),
		WorkingDirectory: root,
	})
	if err != nil {
		t.Fatalf("render VM systemd unit: %v", err)
	}
	assertJailerOnlyVMUnit(t, unit)
	if err := os.WriteFile(filepath.Join(systemdDir, vmSystemdUnitName("devbox")), []byte(unit), 0o644); err != nil {
		t.Fatalf("write VM systemd unit: %v", err)
	}
	sum := sha256.Sum256(firecrackerBytes)
	return vmRecoveryFirecrackerIdentity{
		SHA256:  "sha256:" + hex.EncodeToString(sum[:]),
		Version: version,
	}
}

func stubVMRecoveryFirecrackerVersion(t *testing.T, version string) {
	t.Helper()
	oldVersionFunc := vmCheckpointFirecrackerVersionFunc
	vmCheckpointFirecrackerVersionFunc = func(string) (string, error) {
		return version, nil
	}
	t.Cleanup(func() {
		vmCheckpointFirecrackerVersionFunc = oldVersionFunc
	})
}

func seedCompatibleFullVMCheckpointMetadata(t *testing.T, server *Server, root string, snapshotName string, edit func(map[string]any)) (string, string) {
	t.Helper()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	service := mustService(t, server, "devbox")
	vm := *service.VM.Clone()
	dir := vmCheckpointDir(root, vmSnapshotShortName(snapshotName))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	statePath := filepath.Join(dir, "firecracker-state.bin")
	memoryPath := filepath.Join(dir, "memory.bin")
	if err := os.WriteFile(statePath, []byte("state"), 0o644); err != nil {
		t.Fatalf("write checkpoint state: %v", err)
	}
	if err := os.WriteFile(memoryPath, []byte("memory"), 0o644); err != nil {
		t.Fatalf("write checkpoint memory: %v", err)
	}
	var cfg firecrackerConfig
	rawConfig, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(root), "firecracker.json"))
	if err != nil {
		t.Fatalf("read firecracker config: %v", err)
	}
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		t.Fatalf("decode firecracker config: %v", err)
	}
	balloonHash, vmHash, err := vmCheckpointConfigHashes(vm)
	if err != nil {
		t.Fatalf("VM checkpoint config hashes: %v", err)
	}
	metadata := map[string]any{
		"service":           "devbox",
		"zvolSnapshot":      snapshotName,
		"firecrackerState":  statePath,
		"firecrackerMemory": memoryPath,
		"createdBy":         "catch",
		"createdAt":         "2026-06-13T20:31:00Z",
		"mode":              recoveryModeFull,
		"machineConfigHash": canonicalJSONHashForRecoveryTest(t, cfg.MachineConfig),
		"networkConfigHash": canonicalJSONHashForRecoveryTest(t, cfg.NetworkInterfaces),
		"diskPath":          vm.Disk.Path,
		"vcpu":              vm.CPUs,
		"memoryMiB":         int(vm.MemoryBytes >> 20),
		"balloonConfigHash": balloonHash,
		"vmConfigHash":      vmHash,
	}
	if edit != nil {
		edit(metadata)
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("marshal checkpoint metadata: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0o644); err != nil {
		t.Fatalf("write checkpoint metadata: %v", err)
	}
	return statePath, memoryPath
}

func canonicalJSONHashForRecoveryTest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal canonical JSON: %v", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func assertNoFullVMRestoreMutation(t *testing.T, zfsCalls []string, logPath string) {
	t.Helper()
	for _, forbidden := range []string{"snapshot ", "clone ", "destroy ", "rollback "} {
		if hasRecoveryCall(zfsCalls, forbidden) {
			t.Fatalf("zfs calls = %#v, full mode should not run %q", zfsCalls, forbidden)
		}
	}
	if got := readRecoveryLog(t, logPath); strings.Contains(got, "systemctl stop") || strings.Contains(got, "dd ") {
		t.Fatalf("system command log = %q, full mode should not stop or copy", got)
	}
}

func installFakeSystemctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	script := "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> " + strconv.Quote(logPath) + "\nif [ \"$1\" = \"start\" ] && [ -n \"$YEET_TEST_VM_RESTORE_RESULT\" ]; then\n\tprintf '{\"status\":\"success\"}\\n' > \"$YEET_TEST_VM_RESTORE_RESULT\"\nfi\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func installFakeDD(t *testing.T, logPath string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'dd %s\\n' \"$*\" >> " + strconv.Quote(logPath) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dd: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func appendRecoveryLog(t *testing.T, path string, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open recovery log: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("close recovery log: %v", err)
		}
	}()
	if _, err := fmt.Fprintln(f, line); err != nil {
		t.Fatalf("append recovery log: %v", err)
	}
}

func readRecoveryLog(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read recovery log: %v", err)
	}
	return string(raw)
}

func readRecoveryLogLines(t *testing.T, path string) []string {
	t.Helper()
	raw := strings.TrimSpace(readRecoveryLog(t, path))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func mustService(t *testing.T, server *Server, name string) *db.Service {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		t.Fatalf("service %q not found", name)
	}
	return sv.AsStruct()
}

func serviceExists(t *testing.T, server *Server, name string) bool {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	_, ok := dv.Services().GetOk(name)
	return ok
}

func serviceCount(t *testing.T, server *Server) int {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	return dv.Services().Len()
}

func hasRecoveryCall(calls []string, needle string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, needle) {
			return true
		}
	}
	return false
}

func assertExactRecoveryCallOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	last := -1
	for _, expected := range want {
		idx := lineIndexEqual(calls, expected)
		if idx < 0 {
			t.Fatalf("calls = %#v, missing %q", calls, expected)
		}
		if idx <= last {
			t.Fatalf("calls = %#v, want %q after prior matched calls", calls, expected)
		}
		last = idx
	}
}

func requireLinePrefix(t *testing.T, lines []string, prefix string) string {
	t.Helper()
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("lines = %#v, missing prefix %q", lines, prefix)
	return ""
}

func lineIndexContaining(lines []string, needle string) int {
	for i, line := range lines {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

func lineIndexEqual(lines []string, want string) int {
	for i, line := range lines {
		if line == want {
			return i
		}
	}
	return -1
}

func hasNameSegment(value string, name string) bool {
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == name {
			return true
		}
	}
	return false
}

type ioDiscardReadWriter struct{}

func (ioDiscardReadWriter) Read([]byte) (int, error)    { return 0, io.EOF }
func (ioDiscardReadWriter) Write(p []byte) (int, error) { return len(p), nil }
