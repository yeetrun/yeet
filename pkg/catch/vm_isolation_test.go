// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestVMIsolationModeDefaultsLegacyAndRoundTripsJailer(t *testing.T) {
	root := t.TempDir()

	mode, err := vmIsolationModeForRoot(root)
	if err != nil {
		t.Fatalf("read absent isolation marker: %v", err)
	}
	if mode != vmIsolationLegacy {
		t.Fatalf("absent isolation mode = %q, want %q", mode, vmIsolationLegacy)
	}

	if err := writeVMIsolationMode(root, vmIsolationJailer); err != nil {
		t.Fatalf("write jailer isolation marker: %v", err)
	}
	mode, err = vmIsolationModeForRoot(root)
	if err != nil {
		t.Fatalf("read jailer isolation marker: %v", err)
	}
	if mode != vmIsolationJailer {
		t.Fatalf("isolation mode = %q, want %q", mode, vmIsolationJailer)
	}
	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(root), vmIsolationMarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != vmIsolationJailer+"\n" {
		t.Fatalf("marker = %q, want jailer newline", raw)
	}

	if err := writeVMIsolationMode(root, vmIsolationLegacy); err != nil {
		t.Fatalf("restore legacy isolation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(serviceRunDirForRoot(root), vmIsolationMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy marker still exists: %v", err)
	}
}

func TestVMIsolationModeRejectsUnknownMarker(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(serviceRunDirForRoot(root), vmIsolationMarkerName)
	if err := writeVMFile(path, []byte("surprise\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vmIsolationModeForRoot(root); err == nil || !strings.Contains(err.Error(), "unsupported VM isolation mode") {
		t.Fatalf("unknown marker error = %v", err)
	}
	if err := writeVMIsolationMode(root, "surprise"); err == nil || !strings.Contains(err.Error(), "unsupported VM isolation mode") {
		t.Fatalf("unknown write error = %v", err)
	}
}

func TestVMJailerIDIsStableSafeAndBounded(t *testing.T) {
	if got := vmJailerID("devbox"); got != "yeet-devbox" {
		t.Fatalf("short jailer ID = %q, want yeet-devbox", got)
	}
	service := "A VM/name with spaces and punctuation !!! " + strings.Repeat("long", 30)
	left := vmJailerID(service)
	right := vmJailerID(service)
	if left != right {
		t.Fatalf("jailer ID is not stable: %q != %q", left, right)
	}
	if len(left) > 64 {
		t.Fatalf("jailer ID length = %d, want <= 64: %q", len(left), left)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(left) {
		t.Fatalf("jailer ID contains unsupported characters: %q", left)
	}
	if left == vmJailerID(service+"different") {
		t.Fatalf("different long names collided at %q", left)
	}
}

func TestVMRuntimeIdentityUsesExistingAccount(t *testing.T) {
	oldLookup := vmRuntimeUserLookup
	oldRun := vmRuntimeUserAdd
	t.Cleanup(func() {
		vmRuntimeUserLookup = oldLookup
		vmRuntimeUserAdd = oldRun
	})
	vmRuntimeUserLookup = func(name string) (*user.User, error) {
		if name != vmRuntimeUser {
			t.Fatalf("lookup name = %q, want %q", name, vmRuntimeUser)
		}
		return &user.User{Uid: "812", Gid: "813", Username: name}, nil
	}
	vmRuntimeUserAdd = func(args []string) error {
		t.Fatalf("useradd unexpectedly called with %#v", args)
		return nil
	}

	identity, err := ensureVMRuntimeIdentity()
	if err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	if identity != (vmRuntimeIdentity{UID: 812, GID: 813}) {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestVMRuntimeIdentityCreatesMissingAccountOnce(t *testing.T) {
	oldLookup := vmRuntimeUserLookup
	oldRun := vmRuntimeUserAdd
	t.Cleanup(func() {
		vmRuntimeUserLookup = oldLookup
		vmRuntimeUserAdd = oldRun
	})
	lookups := 0
	vmRuntimeUserLookup = func(name string) (*user.User, error) {
		lookups++
		if lookups == 1 {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Uid: "914", Gid: "915", Username: name}, nil
	}
	var gotArgs []string
	vmRuntimeUserAdd = func(args []string) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}

	identity, err := ensureVMRuntimeIdentity()
	if err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	if identity != (vmRuntimeIdentity{UID: 914, GID: 915}) {
		t.Fatalf("identity = %#v", identity)
	}
	wantArgs := []string{"--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", vmRuntimeUser}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("useradd args = %#v, want %#v", gotArgs, wantArgs)
	}
	if lookups != 2 {
		t.Fatalf("lookups = %d, want 2", lookups)
	}
}

func TestVMRuntimeIdentityRejectsRootAndInvalidIDs(t *testing.T) {
	oldLookup := vmRuntimeUserLookup
	oldRun := vmRuntimeUserAdd
	t.Cleanup(func() {
		vmRuntimeUserLookup = oldLookup
		vmRuntimeUserAdd = oldRun
	})
	vmRuntimeUserAdd = func([]string) error { return nil }

	for _, account := range []*user.User{
		{Uid: "0", Gid: "813", Username: vmRuntimeUser},
		{Uid: "812", Gid: "0", Username: vmRuntimeUser},
		{Uid: "not-a-number", Gid: "813", Username: vmRuntimeUser},
	} {
		vmRuntimeUserLookup = func(string) (*user.User, error) { return account, nil }
		if _, err := ensureVMRuntimeIdentity(); err == nil {
			t.Fatalf("account %#v unexpectedly accepted", account)
		}
	}
}
