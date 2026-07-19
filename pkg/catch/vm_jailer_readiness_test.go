// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"os/user"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestVMJailerReadinessForRoot(t *testing.T) {
	root := t.TempDir()
	got, err := vmJailerReadinessForRoot(root)
	if err != nil || got != vmJailerPendingRestart {
		t.Fatalf("readiness = %q, %v; want %q", got, err, vmJailerPendingRestart)
	}
	if err := markVMJailerReady(root); err != nil {
		t.Fatal(err)
	}
	got, err = vmJailerReadinessForRoot(root)
	if err != nil || got != vmJailerReady {
		t.Fatalf("readiness = %q, %v; want %q", got, err, vmJailerReady)
	}
}

func TestVMJailerReadinessRejectsInvalidMarkerValues(t *testing.T) {
	for _, value := range []string{"unsupported\n", "dynamic\n", "\n"} {
		t.Run(strings.TrimSpace(value), func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(serviceRunDirForRoot(root), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(vmJailerReadinessMarkerPath(root), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := vmJailerReadinessForRoot(root); err == nil {
				t.Fatalf("value %q was accepted", value)
			}
		})
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
	oldNSSLookup := vmRuntimeNSSLookup
	t.Cleanup(func() {
		vmRuntimeUserLookup = oldLookup
		vmRuntimeUserAdd = oldRun
		vmRuntimeNSSLookup = oldNSSLookup
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
	var nssCalls []string
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, validVMRuntimeNSS("812", "813"), &nssCalls)

	identity, err := ensureVMRuntimeIdentity()
	if err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	if identity != (vmRuntimeIdentity{UID: 812, GID: 813}) {
		t.Fatalf("identity = %#v", identity)
	}
	wantNSSCalls := []string{"passwd " + vmRuntimeUser, "group 813", "passwd"}
	if !reflect.DeepEqual(nssCalls, wantNSSCalls) {
		t.Fatalf("NSS lookups = %#v, want %#v", nssCalls, wantNSSCalls)
	}
}

func TestVMRuntimeIdentityCreatesMissingAccountOnce(t *testing.T) {
	oldLookup := vmRuntimeUserLookup
	oldRun := vmRuntimeUserAdd
	oldNSSLookup := vmRuntimeNSSLookup
	t.Cleanup(func() {
		vmRuntimeUserLookup = oldLookup
		vmRuntimeUserAdd = oldRun
		vmRuntimeNSSLookup = oldNSSLookup
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
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, validVMRuntimeNSS("914", "915"), nil)

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
	oldNSSLookup := vmRuntimeNSSLookup
	t.Cleanup(func() {
		vmRuntimeUserLookup = oldLookup
		vmRuntimeUserAdd = oldRun
		vmRuntimeNSSLookup = oldNSSLookup
	})
	vmRuntimeUserAdd = func([]string) error { return nil }
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, validVMRuntimeNSS("812", "813"), nil)

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

func TestVMRuntimeIdentityRejectsInteractiveShell(t *testing.T) {
	stubExistingVMRuntimeUser(t, &user.User{Uid: "812", Gid: "813", Username: vmRuntimeUser})
	records := validVMRuntimeNSS("812", "813")
	records["passwd "+vmRuntimeUser] = vmRuntimeUser + ":x:812:813::/nonexistent:/bin/bash\n"
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, records, nil)

	_, err := ensureVMRuntimeIdentity()
	requireUnsafeVMRuntimeAccountError(t, err, "approved non-login shell")
}

func TestVMRuntimeIdentityRejectsWrongPrimaryGroup(t *testing.T) {
	stubExistingVMRuntimeUser(t, &user.User{Uid: "812", Gid: "813", Username: vmRuntimeUser})
	records := validVMRuntimeNSS("812", "813")
	records["group 813"] = "operators:x:813:\n"
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, records, nil)

	_, err := ensureVMRuntimeIdentity()
	requireUnsafeVMRuntimeAccountError(t, err, "primary group")
}

func TestVMRuntimeIdentityRejectsAnotherUsersPrimaryGID(t *testing.T) {
	stubExistingVMRuntimeUser(t, &user.User{Uid: "812", Gid: "813", Username: vmRuntimeUser})
	records := validVMRuntimeNSS("812", "813")
	records["passwd"] += "alice:x:900:813::/home/alice:/bin/bash\n"
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, records, nil)

	_, err := ensureVMRuntimeIdentity()
	requireUnsafeVMRuntimeAccountError(t, err, "shares primary GID")
}

func TestVMRuntimeIdentityRejectsSupplementaryGroupMember(t *testing.T) {
	stubExistingVMRuntimeUser(t, &user.User{Uid: "812", Gid: "813", Username: vmRuntimeUser})
	records := validVMRuntimeNSS("812", "813")
	records["group 813"] = vmRuntimeUser + ":x:813:alice\n"
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, records, nil)

	_, err := ensureVMRuntimeIdentity()
	requireUnsafeVMRuntimeAccountError(t, err, "supplementary member")
}

func TestVMRuntimeIdentityRejectsMalformedNSSOutput(t *testing.T) {
	stubExistingVMRuntimeUser(t, &user.User{Uid: "812", Gid: "813", Username: vmRuntimeUser})
	records := validVMRuntimeNSS("812", "813")
	records["passwd "+vmRuntimeUser] = vmRuntimeUser + ":x:812\n"
	vmRuntimeNSSLookup = stubVMRuntimeNSSLookup(t, records, nil)

	_, err := ensureVMRuntimeIdentity()
	requireUnsafeVMRuntimeAccountError(t, err, "malformed passwd record")
}

func TestValidatedVMRuntimePasswdRecordRejectsUnsafeLookupResults(t *testing.T) {
	identity := vmRuntimeIdentity{UID: 812, GID: 813}
	tests := []struct {
		name      string
		output    string
		lookupErr error
		wantError string
	}{
		{name: "lookup failure", lookupErr: errors.New("lookup failure"), wantError: "cannot verify its passwd record: lookup failure"},
		{name: "malformed output", output: "malformed\n", wantError: "malformed passwd record"},
		{name: "multiple records", output: validVMRuntimeNSS("812", "813")["passwd "+vmRuntimeUser] + "other:x:900:901::/nonexistent:/bin/false\n", wantError: "returned 2 records"},
		{name: "wrong account name", output: "other:x:812:813::/nonexistent:/bin/false\n", wantError: "record is named"},
		{name: "wrong IDs", output: vmRuntimeUser + ":x:900:901::/nonexistent:/bin/false\n", wantError: "passwd UID:GID is 900:901"},
		{name: "interactive shell", output: vmRuntimeUser + ":x:812:813::/nonexistent:/bin/bash\n", wantError: "approved non-login shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubVMRuntimeNSSResponse(t, "passwd "+vmRuntimeUser, tt.output, tt.lookupErr)
			_, err := validatedVMRuntimePasswdRecord(identity)
			requireUnsafeVMRuntimeAccountError(t, err, tt.wantError)
		})
	}
}

func TestValidateVMRuntimePrimaryGroupRejectsUnsafeLookupResults(t *testing.T) {
	identity := vmRuntimeIdentity{UID: 812, GID: 813}
	tests := []struct {
		name      string
		output    string
		lookupErr error
		wantError string
	}{
		{name: "lookup failure", lookupErr: errors.New("lookup failure"), wantError: "cannot verify its primary group: lookup failure"},
		{name: "malformed output", output: "malformed\n", wantError: "malformed group record"},
		{name: "multiple records", output: vmRuntimeUser + ":x:813:\nother:x:814:\n", wantError: "returned 2 records"},
		{name: "wrong group", output: "operators:x:813:\n", wantError: "primary group is"},
		{name: "supplementary member", output: vmRuntimeUser + ":x:813:alice\n", wantError: "supplementary member"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubVMRuntimeNSSResponse(t, "group 813", tt.output, tt.lookupErr)
			err := validateVMRuntimePrimaryGroup(identity)
			requireUnsafeVMRuntimeAccountError(t, err, tt.wantError)
		})
	}
}

func TestValidateVMRuntimePasswdEnumerationRejectsUnsafeLookupResults(t *testing.T) {
	identity := vmRuntimeIdentity{UID: 812, GID: 813}
	account := vmRuntimePasswdRecord{Name: vmRuntimeUser, UID: 812, GID: 813, Shell: vmRuntimeNologin}
	valid := validVMRuntimeNSS("812", "813")["passwd"]
	tests := []struct {
		name      string
		output    string
		lookupErr error
		wantError string
	}{
		{name: "lookup failure", lookupErr: errors.New("lookup failure"), wantError: "cannot verify that its primary GID is dedicated: lookup failure"},
		{name: "malformed output", output: "malformed\n", wantError: "malformed passwd record"},
		{name: "duplicate account", output: valid + valid, wantError: "enumeration is inconsistent"},
		{name: "mismatched account", output: vmRuntimeUser + ":x:812:813::/nonexistent:/bin/false\n", wantError: "enumeration is inconsistent"},
		{name: "missing account", output: "other:x:900:901::/nonexistent:/bin/false\n", wantError: "does not contain"},
		{name: "shared primary GID", output: valid + "other:x:900:813::/nonexistent:/bin/false\n", wantError: "shares primary GID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubVMRuntimeNSSResponse(t, "passwd", tt.output, tt.lookupErr)
			err := validateVMRuntimePasswdEnumeration(identity, account)
			requireUnsafeVMRuntimeAccountError(t, err, tt.wantError)
		})
	}
}

func stubExistingVMRuntimeUser(t *testing.T, account *user.User) {
	t.Helper()
	oldLookup := vmRuntimeUserLookup
	oldRun := vmRuntimeUserAdd
	oldNSSLookup := vmRuntimeNSSLookup
	t.Cleanup(func() {
		vmRuntimeUserLookup = oldLookup
		vmRuntimeUserAdd = oldRun
		vmRuntimeNSSLookup = oldNSSLookup
	})
	vmRuntimeUserLookup = func(name string) (*user.User, error) {
		if name != vmRuntimeUser {
			t.Fatalf("lookup name = %q, want %q", name, vmRuntimeUser)
		}
		return account, nil
	}
	vmRuntimeUserAdd = func(args []string) error {
		t.Fatalf("useradd unexpectedly called with %#v", args)
		return nil
	}
}

func validVMRuntimeNSS(uid, gid string) map[string]string {
	passwd := vmRuntimeUser + ":x:" + uid + ":" + gid + "::/nonexistent:" + vmRuntimeNologin + "\n"
	return map[string]string{
		"passwd " + vmRuntimeUser: passwd,
		"group " + gid:            vmRuntimeUser + ":x:" + gid + ":\n",
		"passwd":                  passwd,
	}
}

func stubVMRuntimeNSSLookup(t *testing.T, records map[string]string, calls *[]string) func(...string) ([]byte, error) {
	t.Helper()
	return func(args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if calls != nil {
			*calls = append(*calls, key)
		}
		value, ok := records[key]
		if !ok {
			t.Fatalf("unexpected NSS lookup %q", key)
		}
		return []byte(value), nil
	}
}

func stubVMRuntimeNSSResponse(t *testing.T, wantKey, output string, lookupErr error) {
	t.Helper()
	oldLookup := vmRuntimeNSSLookup
	vmRuntimeNSSLookup = func(args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if key != wantKey {
			t.Fatalf("NSS lookup = %q, want %q", key, wantKey)
		}
		return []byte(output), lookupErr
	}
	t.Cleanup(func() { vmRuntimeNSSLookup = oldLookup })
}

func requireUnsafeVMRuntimeAccountError(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("unsafe VM runtime account was accepted")
	}
	for _, want := range []string{
		reason,
		"manually recreate",
		"useradd --system --no-create-home --shell /usr/sbin/nologin --user-group " + vmRuntimeUser,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}
