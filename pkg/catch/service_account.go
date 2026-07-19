// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

const managedServiceUser = "yeet-svc"

var (
	serviceAccountCommand = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
	serviceAccountStat      = os.Stat
	serviceAccountShellStat = os.Stat
)

func EnsureManagedServiceAccount() (resolvedServiceIdentity, error) {
	account, err := lookupOrCreateManagedServiceAccount()
	if err != nil {
		return resolvedServiceIdentity{}, err
	}
	resolved, err := validateManagedServiceAccountStructure(account)
	if err != nil {
		return resolvedServiceIdentity{}, err
	}
	if err := ensureManagedServicePasswordLocked(); err != nil {
		return resolvedServiceIdentity{}, err
	}
	return resolved, nil
}

func lookupOrCreateManagedServiceAccount() (*user.User, error) {
	account, err := serviceUserLookup(managedServiceUser)
	if err == nil {
		return account, nil
	}
	if !unknownServiceUser(err) {
		return nil, fmt.Errorf("lookup %s system account: %w", managedServiceUser, err)
	}
	if err := ensureManagedServiceGroup(); err != nil {
		return nil, err
	}
	if err := runManagedServiceAccountCommand(
		"useradd", "--system", "--gid", managedServiceUser, "--home-dir", "/nonexistent", "--no-create-home",
		"--shell", "/usr/sbin/nologin", managedServiceUser,
	); err != nil {
		return nil, err
	}
	account, err = serviceUserLookup(managedServiceUser)
	if err != nil {
		return nil, fmt.Errorf("lookup newly created %s system account: %w", managedServiceUser, err)
	}
	return account, nil
}

func ensureManagedServiceGroup() error {
	group, err := serviceGroupLookup(managedServiceUser)
	if unknownServiceGroup(err) {
		return runManagedServiceAccountCommand("groupadd", "--system", managedServiceUser)
	}
	if err != nil {
		return fmt.Errorf("lookup %s system group: %w", managedServiceUser, err)
	}
	gid, parseErr := parseID(group.Gid, "GID")
	if parseErr != nil || group.Name != managedServiceUser || gid == 0 {
		return incompatibleManagedServiceAccount("pre-existing group has invalid GID %q", group.Gid)
	}
	return nil
}

func ensureManagedServicePasswordLocked() error {
	locked, err := managedServiceShadowLocked()
	if err != nil {
		return err
	}
	if locked {
		return nil
	}
	if err := runManagedServiceAccountCommand("usermod", "--lock", managedServiceUser); err != nil {
		return err
	}
	locked, err = managedServiceShadowLocked()
	if err != nil {
		return err
	}
	if !locked {
		return incompatibleManagedServiceAccount("password remains unlocked after usermod --lock")
	}
	return nil
}

func runManagedServiceAccountCommand(name string, args ...string) error {
	out, err := serviceAccountCommand(name, args...)
	if err != nil {
		return fmt.Errorf("prepare %s system account with %s: %w: %s", managedServiceUser, name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func validateManagedServiceAccountStructure(account *user.User) (resolvedServiceIdentity, error) {
	uid, gid, err := validateManagedServiceAccountIDs(account)
	if err != nil {
		return resolvedServiceIdentity{}, err
	}
	if err := validateManagedServicePrimaryGroup(account.Gid, gid); err != nil {
		return resolvedServiceIdentity{}, err
	}
	passwd, err := managedServicePasswdRecord()
	if err != nil {
		return resolvedServiceIdentity{}, err
	}
	if err := validateManagedServicePasswd(account, passwd, uid, gid); err != nil {
		return resolvedServiceIdentity{}, err
	}
	if err := validateManagedServiceGroups(gid); err != nil {
		return resolvedServiceIdentity{}, err
	}

	return resolvedServiceIdentity{
		Persisted: db.ServiceIdentity{
			RequestedUser: managedServiceUser, RequestedGroup: managedServiceUser, UID: uid, GID: gid,
		},
		UserName: managedServiceUser, GroupName: managedServiceUser,
	}, nil
}

func validateManagedServiceAccountIDs(account *user.User) (uint32, uint32, error) {
	if account == nil || account.Username != managedServiceUser {
		return 0, 0, incompatibleManagedServiceAccount("account lookup returned an unexpected user")
	}
	uid, err := parseID(account.Uid, "UID")
	if err != nil || uid == 0 {
		return 0, 0, incompatibleManagedServiceAccount("must have a valid non-root UID, got %q", account.Uid)
	}
	gid, err := parseID(account.Gid, "GID")
	if err != nil || gid == 0 {
		return 0, 0, incompatibleManagedServiceAccount("must have a valid non-root GID, got %q", account.Gid)
	}
	return uid, gid, nil
}

func validateManagedServicePrimaryGroup(accountGID string, gid uint32) error {
	group, err := serviceGroupLookupID(accountGID)
	if err != nil {
		return incompatibleManagedServiceAccount("cannot resolve primary group GID %d: %v", gid, err)
	}
	groupGID, err := parseID(group.Gid, "GID")
	if err != nil || group.Name != managedServiceUser || groupGID != gid {
		return incompatibleManagedServiceAccount("primary group is %q with GID %q, want %q with GID %d", group.Name, group.Gid, managedServiceUser, gid)
	}
	return nil
}

func validateManagedServicePasswd(account *user.User, passwd managedPasswd, uid, gid uint32) error {
	if passwd.uid != uid || passwd.gid != gid || passwd.home != account.HomeDir {
		return incompatibleManagedServiceAccount("passwd record does not match account lookup")
	}
	if err := validateManagedServiceShell(passwd.shell); err != nil {
		return err
	}
	return validateManagedServiceHome(account.HomeDir)
}

func validateManagedServiceHome(home string) error {
	if home == "" {
		return incompatibleManagedServiceAccount("home directory must be an absent path")
	}
	if _, err := serviceAccountStat(home); err == nil {
		return incompatibleManagedServiceAccount("home directory %q exists", home)
	} else if !errors.Is(err, os.ErrNotExist) {
		return incompatibleManagedServiceAccount("cannot verify home directory %q is absent: %v", home, err)
	}
	return nil
}

type managedPasswd struct {
	uid   uint32
	gid   uint32
	home  string
	shell string
}

func managedServicePasswdRecord() (managedPasswd, error) {
	out, err := serviceAccountCommand("getent", "passwd", managedServiceUser)
	if err != nil {
		return managedPasswd{}, incompatibleManagedServiceAccount("cannot read passwd record: %v: %s", err, strings.TrimSpace(string(out)))
	}
	line, err := singleManagedServiceRecord(out, "passwd")
	if err != nil {
		return managedPasswd{}, err
	}
	fields := strings.Split(line, ":")
	if len(fields) != 7 || fields[0] != managedServiceUser {
		return managedPasswd{}, incompatibleManagedServiceAccount("malformed passwd record %q", line)
	}
	uid, err := parseID(fields[2], "UID")
	if err != nil {
		return managedPasswd{}, incompatibleManagedServiceAccount("malformed passwd record %q: %v", line, err)
	}
	gid, err := parseID(fields[3], "GID")
	if err != nil {
		return managedPasswd{}, incompatibleManagedServiceAccount("malformed passwd record %q: %v", line, err)
	}
	return managedPasswd{uid: uid, gid: gid, home: fields[5], shell: fields[6]}, nil
}

func validateManagedServiceShell(shell string) error {
	switch shell {
	case "/sbin/nologin", "/usr/sbin/nologin", "/bin/false":
	default:
		return incompatibleManagedServiceAccount("shell %q is not an approved non-login shell", shell)
	}
	info, err := serviceAccountShellStat(shell)
	if err != nil {
		return incompatibleManagedServiceAccount("cannot inspect approved non-login shell %q: %v", shell, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return incompatibleManagedServiceAccount("approved non-login shell %q is not a safe executable", shell)
	}
	return nil
}

func managedServiceShadowLocked() (bool, error) {
	out, err := serviceAccountCommand("getent", "shadow", managedServiceUser)
	if err != nil {
		return false, incompatibleManagedServiceAccount("cannot read shadow entry: %v: %s", err, strings.TrimSpace(string(out)))
	}
	line, err := singleManagedServiceRecord(out, "shadow")
	if err != nil {
		return false, err
	}
	fields := strings.Split(line, ":")
	if len(fields) < 2 || fields[0] != managedServiceUser {
		return false, incompatibleManagedServiceAccount("malformed shadow entry %q", line)
	}
	return strings.HasPrefix(fields[1], "!") || strings.HasPrefix(fields[1], "*"), nil
}

func validateManagedServiceGroups(primaryGID uint32) error {
	out, err := serviceAccountCommand("id", "-G", managedServiceUser)
	if err != nil {
		return incompatibleManagedServiceAccount("cannot inspect supplementary groups: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) != 1 {
		return incompatibleManagedServiceAccount("supplementary groups are present: %q", strings.TrimSpace(string(out)))
	}
	gid, err := parseID(fields[0], "GID")
	if err != nil || gid != primaryGID {
		return incompatibleManagedServiceAccount("group membership %q does not contain only primary GID %d", strings.TrimSpace(string(out)), primaryGID)
	}
	return nil
}

func singleManagedServiceRecord(raw []byte, kind string) (string, error) {
	text := strings.TrimRight(string(raw), "\r\n")
	if text == "" || strings.ContainsAny(text, "\r\n") {
		return "", incompatibleManagedServiceAccount("malformed %s lookup output %q", kind, text)
	}
	return text, nil
}

func incompatibleManagedServiceAccount(format string, args ...any) error {
	return fmt.Errorf("existing %s system account is incompatible: %s", managedServiceUser, fmt.Sprintf(format, args...))
}
