// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"os/user"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagedServiceAccountReusesCompatibleAccount(t *testing.T) {
	for _, shell := range []string{"/sbin/nologin", "/usr/sbin/nologin", "/bin/false"} {
		t.Run(shell, func(t *testing.T) {
			fixture := stubManagedServiceAccount(t)
			fixture.passwd = managedPasswdRecord("991", "992", shell)

			got, err := EnsureManagedServiceAccount()
			if err != nil {
				t.Fatalf("EnsureManagedServiceAccount: %v", err)
			}
			if got.Persisted.UID != 991 || got.Persisted.GID != 992 || got.Persisted.RequestedUser != managedServiceUser || got.Persisted.RequestedGroup != managedServiceUser {
				t.Fatalf("managed identity = %#v", got)
			}
			if len(fixture.creationCalls) != 0 {
				t.Fatalf("creation commands = %#v, want none", fixture.creationCalls)
			}
		})
	}
}

func TestManagedServiceAccountRejectsIncompatibleProperties(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*managedServiceAccountFixture)
		wantErr string
	}{
		{
			name: "zero UID",
			change: func(f *managedServiceAccountFixture) {
				f.account.Uid = "0"
				f.passwd = managedPasswdRecord("0", "992", "/usr/sbin/nologin")
			},
			wantErr: "non-root UID",
		},
		{
			name: "zero GID",
			change: func(f *managedServiceAccountFixture) {
				f.account.Gid = "0"
				f.group.Gid = "0"
				f.passwd = managedPasswdRecord("991", "0", "/usr/sbin/nologin")
				f.groupIDs = "0\n"
			},
			wantErr: "non-root GID",
		},
		{
			name: "wrong primary group",
			change: func(f *managedServiceAccountFixture) {
				f.group.Name = "operators"
			},
			wantErr: "primary group",
		},
		{
			name: "login shell",
			change: func(f *managedServiceAccountFixture) {
				f.passwd = managedPasswdRecord("991", "992", "/bin/bash")
			},
			wantErr: "non-login shell",
		},
		{
			name: "existing home directory",
			change: func(f *managedServiceAccountFixture) {
				f.homeStatErr = nil
			},
			wantErr: "home directory",
		},
		{
			name: "supplementary group membership",
			change: func(f *managedServiceAccountFixture) {
				f.groupIDs = "992 20\n"
			},
			wantErr: "supplementary groups",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := stubManagedServiceAccount(t)
			tt.change(fixture)
			_, err := EnsureManagedServiceAccount()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("EnsureManagedServiceAccount error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestManagedServiceAccountRejectsUnsafeNologinShell(t *testing.T) {
	fixture := stubManagedServiceAccount(t)
	fixture.shellMode = 0o777
	_, err := EnsureManagedServiceAccount()
	if err == nil || !strings.Contains(err.Error(), "not a safe executable") {
		t.Fatalf("EnsureManagedServiceAccount error = %v, want unsafe executable error", err)
	}
}

func TestManagedServiceAccountLocksCompatibleUnlockedAccount(t *testing.T) {
	fixture := stubManagedServiceAccount(t)
	fixture.shadow = managedServiceUser + ":$6$hash:20000:0:99999:7:::\n"
	fixture.lockOnUsermod = true

	if _, err := EnsureManagedServiceAccount(); err != nil {
		t.Fatalf("EnsureManagedServiceAccount: %v", err)
	}
	want := [][]string{{"usermod", "--lock", managedServiceUser}}
	if !reflect.DeepEqual(fixture.creationCalls, want) {
		t.Fatalf("account commands = %#v, want %#v", fixture.creationCalls, want)
	}
}

func TestManagedServiceAccountRejectsAccountThatRemainsUnlocked(t *testing.T) {
	fixture := stubManagedServiceAccount(t)
	fixture.shadow = managedServiceUser + ":$6$hash:20000:0:99999:7:::\n"

	_, err := EnsureManagedServiceAccount()
	if err == nil || !strings.Contains(err.Error(), "remains unlocked") {
		t.Fatalf("EnsureManagedServiceAccount error = %v, want remains unlocked", err)
	}
}

func TestManagedServiceAccountCreatesAndRelooksUp(t *testing.T) {
	fixture := stubManagedServiceAccount(t)
	lookups := 0
	serviceUserLookup = func(name string) (*user.User, error) {
		lookups++
		if lookups == 1 {
			return nil, user.UnknownUserError(name)
		}
		copy := *fixture.account
		return &copy, nil
	}

	got, err := EnsureManagedServiceAccount()
	if err != nil {
		t.Fatalf("EnsureManagedServiceAccount: %v", err)
	}
	if got.Persisted.UID != 991 || got.Persisted.GID != 992 {
		t.Fatalf("managed identity = %#v", got)
	}
	wantCalls := [][]string{
		{"groupadd", "--system", managedServiceUser},
		{"useradd", "--system", "--gid", managedServiceUser, "--home-dir", "/nonexistent", "--no-create-home", "--shell", "/usr/sbin/nologin", managedServiceUser},
	}
	if !reflect.DeepEqual(fixture.creationCalls, wantCalls) {
		t.Fatalf("creation commands = %#v, want %#v", fixture.creationCalls, wantCalls)
	}
	if lookups != 2 {
		t.Fatalf("user lookups = %d, want 2", lookups)
	}
}

func TestManagedServiceAccountReusesCompatibleGroupAfterInterruptedCreate(t *testing.T) {
	fixture := stubManagedServiceAccount(t)
	fixture.groupPresent = true
	lookups := 0
	serviceUserLookup = func(name string) (*user.User, error) {
		lookups++
		if lookups == 1 {
			return nil, user.UnknownUserError(name)
		}
		copy := *fixture.account
		return &copy, nil
	}

	if _, err := EnsureManagedServiceAccount(); err != nil {
		t.Fatalf("EnsureManagedServiceAccount: %v", err)
	}
	want := [][]string{{"useradd", "--system", "--gid", managedServiceUser, "--home-dir", "/nonexistent", "--no-create-home", "--shell", "/usr/sbin/nologin", managedServiceUser}}
	if !reflect.DeepEqual(fixture.creationCalls, want) {
		t.Fatalf("account commands = %#v, want %#v", fixture.creationCalls, want)
	}
}

type managedServiceAccountFixture struct {
	account       *user.User
	group         *user.Group
	passwd        string
	shadow        string
	groupIDs      string
	shellMode     os.FileMode
	homeStatErr   error
	groupPresent  bool
	lockOnUsermod bool
	creationCalls [][]string
}

func stubManagedServiceAccount(t *testing.T) *managedServiceAccountFixture {
	t.Helper()
	oldUserLookup := serviceUserLookup
	oldGroupLookup := serviceGroupLookup
	oldGroupLookupID := serviceGroupLookupID
	oldCommand := serviceAccountCommand
	oldStat := serviceAccountStat
	oldShellStat := serviceAccountShellStat
	t.Cleanup(func() {
		serviceUserLookup = oldUserLookup
		serviceGroupLookup = oldGroupLookup
		serviceGroupLookupID = oldGroupLookupID
		serviceAccountCommand = oldCommand
		serviceAccountStat = oldStat
		serviceAccountShellStat = oldShellStat
	})

	fixture := &managedServiceAccountFixture{
		account:     &user.User{Username: managedServiceUser, Uid: "991", Gid: "992", HomeDir: "/nonexistent"},
		group:       &user.Group{Name: managedServiceUser, Gid: "992"},
		passwd:      managedPasswdRecord("991", "992", "/usr/sbin/nologin"),
		shadow:      managedServiceUser + ":!:20000:0:99999:7:::\n",
		groupIDs:    "992\n",
		shellMode:   0o755,
		homeStatErr: &os.PathError{Op: "stat", Path: "/nonexistent", Err: os.ErrNotExist},
	}
	serviceUserLookup = func(name string) (*user.User, error) {
		if name != managedServiceUser {
			t.Fatalf("user lookup = %q, want %q", name, managedServiceUser)
		}
		copy := *fixture.account
		return &copy, nil
	}
	serviceGroupLookup = func(name string) (*user.Group, error) {
		if name != managedServiceUser {
			t.Fatalf("group lookup = %q, want %q", name, managedServiceUser)
		}
		if !fixture.groupPresent {
			return nil, user.UnknownGroupError(name)
		}
		copy := *fixture.group
		return &copy, nil
	}
	serviceGroupLookupID = func(id string) (*user.Group, error) {
		if id != fixture.account.Gid {
			t.Fatalf("group lookup ID = %q, want %q", id, fixture.account.Gid)
		}
		copy := *fixture.group
		return &copy, nil
	}
	serviceAccountCommand = func(name string, args ...string) ([]byte, error) {
		call := append([]string{name}, args...)
		switch strings.Join(call, " ") {
		case "getent passwd " + managedServiceUser:
			return []byte(fixture.passwd), nil
		case "getent shadow " + managedServiceUser:
			return []byte(fixture.shadow), nil
		case "id -G " + managedServiceUser:
			return []byte(fixture.groupIDs), nil
		default:
			fixture.creationCalls = append(fixture.creationCalls, call)
			if strings.Join(call, " ") == "groupadd --system "+managedServiceUser {
				fixture.groupPresent = true
			}
			if strings.Join(call, " ") == "usermod --lock "+managedServiceUser && fixture.lockOnUsermod {
				fixture.shadow = managedServiceUser + ":!:20000:0:99999:7:::\n"
			}
			return nil, nil
		}
	}
	serviceAccountStat = func(path string) (os.FileInfo, error) {
		if path != fixture.account.HomeDir {
			t.Fatalf("stat path = %q, want %q", path, fixture.account.HomeDir)
		}
		if fixture.homeStatErr != nil {
			return nil, fixture.homeStatErr
		}
		return fakeServiceAccountFileInfo{}, nil
	}
	serviceAccountShellStat = func(path string) (os.FileInfo, error) {
		return fakeServiceAccountFileInfo{name: path, mode: fixture.shellMode}, nil
	}
	return fixture
}

func managedPasswdRecord(uid, gid, shell string) string {
	return managedServiceUser + ":x:" + uid + ":" + gid + "::/nonexistent:" + shell + "\n"
}

type fakeServiceAccountFileInfo struct {
	name string
	mode os.FileMode
}

func (f fakeServiceAccountFileInfo) Name() string {
	if f.name != "" {
		return f.name
	}
	return "nonexistent"
}
func (fakeServiceAccountFileInfo) Size() int64 { return 0 }
func (f fakeServiceAccountFileInfo) Mode() os.FileMode {
	if f.mode != 0 {
		return f.mode
	}
	return os.ModeDir | 0o755
}
func (fakeServiceAccountFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeServiceAccountFileInfo) IsDir() bool        { return true }
func (fakeServiceAccountFileInfo) Sys() any           { return nil }
