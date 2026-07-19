// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type vmJailerReadiness string

const (
	vmJailerReady               vmJailerReadiness = "jailer"
	vmJailerPendingRestart      vmJailerReadiness = "jailer-pending-restart"
	vmJailerReadinessMarkerName                   = "vmm-isolation"
	vmRuntimeUser                                 = "yeet-vm"
	vmRuntimeNologin                              = "/usr/sbin/nologin"
)

type vmRuntimeIdentity struct {
	UID int
	GID int
}

type vmRuntimePasswdRecord struct {
	Name  string
	UID   int
	GID   int
	Shell string
}

type vmRuntimeGroupRecord struct {
	Name    string
	GID     int
	Members []string
}

var (
	vmJailerUnsafePattern = regexp.MustCompile(`[^a-z0-9]+`)
	vmRuntimeUserLookup   = user.Lookup
	vmRuntimeUserAdd      = func(args []string) error {
		cmd := exec.Command("useradd", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("create %s system account: %w: %s", vmRuntimeUser, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	vmRuntimeNSSLookup = func(args ...string) ([]byte, error) {
		cmd := exec.Command("getent", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("getent %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
)

func vmJailerReadinessMarkerPath(root string) string {
	return filepath.Join(serviceRunDirForRoot(root), vmJailerReadinessMarkerName)
}

func vmJailerReadinessForRoot(root string) (vmJailerReadiness, error) {
	raw, err := os.ReadFile(vmJailerReadinessMarkerPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return vmJailerPendingRestart, nil
	}
	if err != nil {
		return "", fmt.Errorf("read VM jailer readiness: %w", err)
	}
	if strings.TrimSpace(string(raw)) != string(vmJailerReady) {
		return "", fmt.Errorf("unsupported VM jailer readiness marker %q", strings.TrimSpace(string(raw)))
	}
	return vmJailerReady, nil
}

func markVMJailerReady(root string) error {
	if err := writeVMFileAtomic(vmJailerReadinessMarkerPath(root), []byte("jailer\n"), 0o600); err != nil {
		return fmt.Errorf("write VM jailer readiness: %w", err)
	}
	return nil
}

func writeVMFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ensureVMRuntimeIdentity() (vmRuntimeIdentity, error) {
	account, err := vmRuntimeUserLookup(vmRuntimeUser)
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return vmRuntimeIdentity{}, fmt.Errorf("lookup %s system account: %w", vmRuntimeUser, err)
		}
		args := []string{"--system", "--no-create-home", "--shell", vmRuntimeNologin, "--user-group", vmRuntimeUser}
		if err := vmRuntimeUserAdd(args); err != nil {
			return vmRuntimeIdentity{}, err
		}
		account, err = vmRuntimeUserLookup(vmRuntimeUser)
		if err != nil {
			return vmRuntimeIdentity{}, fmt.Errorf("lookup newly created %s system account: %w", vmRuntimeUser, err)
		}
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid <= 0 {
		return vmRuntimeIdentity{}, fmt.Errorf("%s has invalid non-root UID %q", vmRuntimeUser, account.Uid)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid <= 0 {
		return vmRuntimeIdentity{}, fmt.Errorf("%s has invalid non-root GID %q", vmRuntimeUser, account.Gid)
	}
	identity := vmRuntimeIdentity{UID: uid, GID: gid}
	if err := validateVMRuntimeIdentityNSS(identity); err != nil {
		return vmRuntimeIdentity{}, err
	}
	return identity, nil
}

func validateVMRuntimeIdentityNSS(identity vmRuntimeIdentity) error {
	account, err := validatedVMRuntimePasswdRecord(identity)
	if err != nil {
		return err
	}
	if err := validateVMRuntimePrimaryGroup(identity); err != nil {
		return err
	}
	return validateVMRuntimePasswdEnumeration(identity, account)
}

func validatedVMRuntimePasswdRecord(identity vmRuntimeIdentity) (vmRuntimePasswdRecord, error) {
	passwdRaw, err := vmRuntimeNSSLookup("passwd", vmRuntimeUser)
	if err != nil {
		return vmRuntimePasswdRecord{}, unsafeVMRuntimeAccountError("cannot verify its passwd record: %v", err)
	}
	passwdRecords, err := parseVMRuntimePasswdRecords(passwdRaw)
	if err != nil {
		return vmRuntimePasswdRecord{}, unsafeVMRuntimeAccountError("%v", err)
	}
	if len(passwdRecords) != 1 {
		return vmRuntimePasswdRecord{}, unsafeVMRuntimeAccountError("passwd lookup returned %d records, want exactly one", len(passwdRecords))
	}
	account := passwdRecords[0]
	if account.Name != vmRuntimeUser {
		return vmRuntimePasswdRecord{}, unsafeVMRuntimeAccountError("passwd record is named %q, want %q", account.Name, vmRuntimeUser)
	}
	if account.UID != identity.UID || account.GID != identity.GID {
		return vmRuntimePasswdRecord{}, unsafeVMRuntimeAccountError(
			"passwd UID:GID is %d:%d, but account lookup returned %d:%d",
			account.UID, account.GID, identity.UID, identity.GID,
		)
	}
	if !approvedVMRuntimeShell(account.Shell) {
		return vmRuntimePasswdRecord{}, unsafeVMRuntimeAccountError("passwd record shell %q is not an approved non-login shell", account.Shell)
	}
	return account, nil
}

func validateVMRuntimePrimaryGroup(identity vmRuntimeIdentity) error {
	groupRaw, err := vmRuntimeNSSLookup("group", strconv.Itoa(identity.GID))
	if err != nil {
		return unsafeVMRuntimeAccountError("cannot verify its primary group: %v", err)
	}
	groupRecords, err := parseVMRuntimeGroupRecords(groupRaw)
	if err != nil {
		return unsafeVMRuntimeAccountError("%v", err)
	}
	if len(groupRecords) != 1 {
		return unsafeVMRuntimeAccountError("primary group lookup returned %d records, want exactly one", len(groupRecords))
	}
	group := groupRecords[0]
	if group.Name != vmRuntimeUser || group.GID != identity.GID {
		return unsafeVMRuntimeAccountError(
			"primary group is %q with GID %d, want %q with GID %d",
			group.Name, group.GID, vmRuntimeUser, identity.GID,
		)
	}
	for _, member := range group.Members {
		if member != vmRuntimeUser {
			return unsafeVMRuntimeAccountError("unrelated user %q is a supplementary member of primary group %q", member, vmRuntimeUser)
		}
	}
	return nil
}

func validateVMRuntimePasswdEnumeration(identity vmRuntimeIdentity, account vmRuntimePasswdRecord) error {
	allPasswdRaw, err := vmRuntimeNSSLookup("passwd")
	if err != nil {
		return unsafeVMRuntimeAccountError("cannot verify that its primary GID is dedicated: %v", err)
	}
	allPasswdRecords, err := parseVMRuntimePasswdRecords(allPasswdRaw)
	if err != nil {
		return unsafeVMRuntimeAccountError("%v", err)
	}
	matchedAccount := false
	for _, other := range allPasswdRecords {
		if other.Name == vmRuntimeUser {
			if matchedAccount || other != account {
				return unsafeVMRuntimeAccountError("passwd enumeration is inconsistent with the %q account record", vmRuntimeUser)
			}
			matchedAccount = true
			continue
		}
		if other.GID == identity.GID {
			return unsafeVMRuntimeAccountError("unrelated user %q shares primary GID %d", other.Name, identity.GID)
		}
	}
	if !matchedAccount {
		return unsafeVMRuntimeAccountError("passwd enumeration does not contain %q", vmRuntimeUser)
	}
	return nil
}

func approvedVMRuntimeShell(shell string) bool {
	switch shell {
	case "/usr/sbin/nologin", "/sbin/nologin", "/usr/bin/false", "/bin/false":
		return true
	default:
		return false
	}
}

func parseVMRuntimePasswdRecords(raw []byte) ([]vmRuntimePasswdRecord, error) {
	lines, err := vmRuntimeNSSLines(raw, "passwd")
	if err != nil {
		return nil, err
	}
	records := make([]vmRuntimePasswdRecord, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) != 7 || fields[0] == "" {
			return nil, fmt.Errorf("malformed passwd record %q", line)
		}
		uid, err := parseVMRuntimeNSSID(fields[2])
		if err != nil {
			return nil, fmt.Errorf("malformed passwd UID in record %q: %w", line, err)
		}
		gid, err := parseVMRuntimeNSSID(fields[3])
		if err != nil {
			return nil, fmt.Errorf("malformed passwd GID in record %q: %w", line, err)
		}
		records = append(records, vmRuntimePasswdRecord{Name: fields[0], UID: uid, GID: gid, Shell: fields[6]})
	}
	return records, nil
}

func parseVMRuntimeGroupRecords(raw []byte) ([]vmRuntimeGroupRecord, error) {
	lines, err := vmRuntimeNSSLines(raw, "group")
	if err != nil {
		return nil, err
	}
	records := make([]vmRuntimeGroupRecord, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) != 4 || fields[0] == "" {
			return nil, fmt.Errorf("malformed group record %q", line)
		}
		gid, err := parseVMRuntimeNSSID(fields[2])
		if err != nil {
			return nil, fmt.Errorf("malformed group GID in record %q: %w", line, err)
		}
		var members []string
		if fields[3] != "" {
			for _, member := range strings.Split(fields[3], ",") {
				if member == "" {
					return nil, fmt.Errorf("malformed group member list in record %q", line)
				}
				members = append(members, member)
			}
		}
		records = append(records, vmRuntimeGroupRecord{Name: fields[0], GID: gid, Members: members})
	}
	return records, nil
}

func vmRuntimeNSSLines(raw []byte, database string) ([]string, error) {
	text := strings.TrimRight(string(raw), "\r\n")
	if text == "" {
		return nil, fmt.Errorf("malformed %s lookup output: empty result", database)
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
		if lines[i] == "" {
			return nil, fmt.Errorf("malformed %s lookup output: empty record", database)
		}
	}
	return lines, nil
}

func parseVMRuntimeNSSID(value string) (int, error) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, fmt.Errorf("invalid numeric ID %q", value)
	}
	valueID, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric ID %q: %w", value, err)
	}
	return valueID, nil
}

func unsafeVMRuntimeAccountError(format string, args ...any) error {
	reason := fmt.Sprintf(format, args...)
	return fmt.Errorf(
		"existing %s system account is unsafe: %s; manually recreate it as a dedicated account after confirming it is unused: useradd --system --no-create-home --shell %s --user-group %s",
		vmRuntimeUser, reason, vmRuntimeNologin, vmRuntimeUser,
	)
}

func vmJailerID(service string) string {
	raw := strings.TrimSpace(service)
	base := strings.Trim(vmJailerUnsafePattern.ReplaceAllString(strings.ToLower(raw), "-"), "-")
	if base == "" {
		base = "vm"
	}
	safeRaw := strings.ToLower(raw) == base
	if safeRaw && len("yeet-"+base) <= 64 {
		return "yeet-" + base
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:12]
	const maxBase = 46
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	if base == "" {
		base = "vm"
	}
	return "yeet-" + base + "-" + digest
}
