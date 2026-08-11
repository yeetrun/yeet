// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/fileutil"
	"golang.org/x/sys/unix"
)

const (
	bubblewrapAppArmorParserPath        = "/usr/sbin/apparmor_parser"
	bubblewrapAppArmorProfileDir        = "/etc/apparmor.d"
	bubblewrapAppArmorProfilePath       = "/etc/apparmor.d/yeet-bwrap"
	bubblewrapAppArmorRestrictedUserNS  = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	bubblewrapAppArmorEnabledPath       = "/sys/module/apparmor/parameters/enabled"
	bubblewrapAppArmorProfilesPath      = "/sys/kernel/security/apparmor/profiles"
	bubblewrapAppArmorParentProfile     = "yeet-bwrap"
	bubblewrapAppArmorRestrictedProfile = "yeet-unpriv-bwrap"
	bubblewrapAppArmorExpectedLabel     = "yeet-bwrap//&yeet-unpriv-bwrap (enforce)"
	bubblewrapAppArmorZeroCapabilities  = "0000000000000000"
	bubblewrapAppArmorProfileV1         = `# Managed by Yeet. Policy version: 1
abi <abi/4.0>,
include <tunables/global>

profile yeet-bwrap /usr/bin/bwrap flags=(attach_disconnected) {
  allow capability,
  allow file rwlkm /{**,},
  allow network,
  allow unix,
  allow ptrace,
  allow signal,
  allow mqueue,
  allow io_uring,
  allow userns,
  allow mount,
  allow umount,
  allow pivot_root,
  allow dbus,
  allow px /** -> yeet-bwrap//&yeet-unpriv-bwrap,
}

profile yeet-unpriv-bwrap flags=(attach_disconnected) {
  allow file rwlkm /{**,},
  allow network,
  allow unix,
  allow ptrace,
  allow signal,
  allow mqueue,
  allow io_uring,
  allow userns,
  allow mount,
  allow umount,
  allow pivot_root,
  allow dbus,
  allow pix /** -> &yeet-unpriv-bwrap,
  audit deny capability,
}
`
)

type bubblewrapAppArmorPaths struct {
	trustRoot        string
	osRelease        string
	restrictedUserNS string
	apparmorEnabled  string
	parser           string
	profileDir       string
	profile          string
	profiles         string
}

type bubblewrapAppArmorDeps struct {
	paths       bubblewrapAppArmorPaths
	trustedUID  uint32
	lstat       func(string) (bubblewrapFileStat, error)
	open        func(string, int, os.FileMode) (bubblewrapDependencyFile, error)
	readFile    func(string) ([]byte, error)
	createTemp  func(string, string) (*os.File, error)
	link        func(string, string) error
	remove      func(string) error
	syncDir     func(string) error
	run         func(context.Context, string, []string) ([]byte, error)
	probe       func(context.Context) error
	runEvidence serviceSandboxCommandRunner
	pathPresent func(string) bool
}

func ensureRestrictedBubblewrapAppArmor(ctx context.Context, initial error) error {
	return ensureRestrictedBubblewrapAppArmorWith(ctx, initial, defaultBubblewrapAppArmorDeps())
}

func defaultBubblewrapAppArmorDeps() bubblewrapAppArmorDeps {
	return bubblewrapAppArmorDeps{
		paths: bubblewrapAppArmorPaths{
			trustRoot:        "/",
			osRelease:        "/etc/os-release",
			restrictedUserNS: bubblewrapAppArmorRestrictedUserNS,
			apparmorEnabled:  bubblewrapAppArmorEnabledPath,
			parser:           bubblewrapAppArmorParserPath,
			profileDir:       bubblewrapAppArmorProfileDir,
			profile:          bubblewrapAppArmorProfilePath,
			profiles:         bubblewrapAppArmorProfilesPath,
		},
		trustedUID: 0,
		lstat: func(path string) (bubblewrapFileStat, error) {
			info, err := os.Lstat(path)
			if err != nil {
				return bubblewrapFileStat{}, err
			}
			return bubblewrapStatFromFileInfo(info)
		},
		open: func(path string, flags int, mode os.FileMode) (bubblewrapDependencyFile, error) {
			file, err := os.OpenFile(path, flags, mode)
			if err != nil {
				return nil, err
			}
			return bubblewrapOSFile{File: file}, nil
		},
		readFile:   os.ReadFile,
		createTemp: os.CreateTemp,
		link:       os.Link,
		remove:     os.Remove,
		syncDir:    fileutil.SyncDir,
		run:        runBubblewrapAppArmorCommand,
		probe: func(ctx context.Context) error {
			return probeBubblewrap(ctx, defaultBubblewrapDependencyDeps())
		},
		runEvidence: runBubblewrapAppArmorEvidenceCommand,
		pathPresent: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
	}
}

func ensureRestrictedBubblewrapAppArmorWith(
	ctx context.Context,
	initial error,
	deps bubblewrapAppArmorDeps,
) error {
	compatible, err := restrictedBubblewrapAppArmorHost(deps)
	if err != nil {
		return errors.Join(initial, err)
	}
	if !compatible {
		return initial
	}
	return runRestrictedBubblewrapAppArmorTransaction(ctx, initial, deps)
}

func runRestrictedBubblewrapAppArmorTransaction(
	ctx context.Context,
	initial error,
	deps bubblewrapAppArmorDeps,
) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := inspectBubblewrapAppArmorInitialState(deps)
	if err != nil {
		return errors.Join(initial, err)
	}
	proof, published, err := prepareBubblewrapAppArmorProfile(ctx, deps)
	if err != nil {
		return errors.Join(initial, err)
	}
	loadedBefore := before.complete()
	loadedByCall := !loadedBefore
	loadAttempted := false
	defer func() {
		if retErr == nil {
			return
		}
		retErr = errors.Join(retErr, recoverBubblewrapAppArmorFailure(deps, proof, published, loadedByCall, loadAttempted))
	}()
	if err := validateBubblewrapAppArmorParser(deps); err != nil {
		return err
	}
	loadAttempted = true
	return loadAndVerifyBubblewrapAppArmor(ctx, deps)
}

func inspectBubblewrapAppArmorInitialState(deps bubblewrapAppArmorDeps) (bubblewrapAppArmorInventory, error) {
	if err := validateBubblewrapAppArmorHostPaths(deps); err != nil {
		return bubblewrapAppArmorInventory{}, err
	}
	inventory, err := readBubblewrapAppArmorInventory(deps)
	if err != nil {
		return bubblewrapAppArmorInventory{}, err
	}
	if inventory.partial() {
		return bubblewrapAppArmorInventory{}, errors.New("host AppArmor inventory contains only one Yeet Bubblewrap profile; inspect it manually")
	}
	if err := validateLoadedBubblewrapAppArmorProfilePath(deps, inventory); err != nil {
		return bubblewrapAppArmorInventory{}, err
	}
	return inventory, nil
}

func validateLoadedBubblewrapAppArmorProfilePath(deps bubblewrapAppArmorDeps, inventory bubblewrapAppArmorInventory) error {
	if !inventory.complete() {
		return nil
	}
	_, err := deps.lstat(deps.paths.profile)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(
			"yeet Bubblewrap AppArmor profiles are loaded without the exact managed profile file %s; inspect them manually",
			deps.paths.profile,
		)
	}
	if err != nil {
		return fmt.Errorf("inspect managed Bubblewrap AppArmor profile path: %w", err)
	}
	return nil
}

func loadAndVerifyBubblewrapAppArmor(ctx context.Context, deps bubblewrapAppArmorDeps) error {
	if output, err := deps.run(ctx, deps.paths.parser, []string{"-r", "-K", deps.paths.profile}); err != nil {
		return bubblewrapAppArmorCommandError("load managed Bubblewrap AppArmor profile", output, err)
	}
	after, err := readBubblewrapAppArmorInventory(deps)
	if err != nil {
		return err
	}
	if !after.complete() {
		return errors.New("managed Bubblewrap AppArmor profiles are not both loaded after parser success")
	}
	if err := deps.probe(ctx); err != nil {
		return fmt.Errorf("verify Bubblewrap after managed AppArmor profile load: %w", err)
	}
	return verifyBubblewrapAppArmorSecurity(ctx, deps)
}

func recoverBubblewrapAppArmorFailure(
	deps bubblewrapAppArmorDeps,
	proof serviceIdentityPathProof,
	published, loadedByCall, loadAttempted bool,
) error {
	if loadedByCall && loadAttempted {
		return rollbackBubblewrapAppArmorProfile(deps, proof, published)
	}
	if published {
		return removeProvenanceSafeArtifact(proof, "managed Bubblewrap AppArmor profile before load")
	}
	return nil
}

func restrictedBubblewrapAppArmorHost(deps bubblewrapAppArmorDeps) (bool, error) {
	osRelease, err := deps.readFile(deps.paths.osRelease)
	if err != nil {
		return false, nil
	}
	if !bubblewrapOSReleaseHasID(string(osRelease), "ubuntu") {
		return false, nil
	}
	restricted, err := deps.readFile(deps.paths.restrictedUserNS)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Ubuntu restricted user-namespace policy %s: %w", deps.paths.restrictedUserNS, err)
	}
	if strings.TrimSpace(string(restricted)) != "1" {
		return false, nil
	}
	enabled, err := deps.readFile(deps.paths.apparmorEnabled)
	if err != nil {
		return false, fmt.Errorf("read AppArmor enablement %s: %w", deps.paths.apparmorEnabled, err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(enabled)), "Y") {
		return false, fmt.Errorf("ubuntu restricts unprivileged user namespaces but AppArmor is not enabled")
	}
	return true, nil
}

func bubblewrapOSReleaseHasID(raw, expected string) bool {
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "ID" {
			continue
		}
		return strings.EqualFold(strings.Trim(strings.TrimSpace(value), "\""), expected)
	}
	return false
}

func validateBubblewrapAppArmorHostPaths(deps bubblewrapAppArmorDeps) error {
	if err := validateBubblewrapAppArmorDirectoryChain(deps, deps.paths.profileDir); err != nil {
		return err
	}
	if err := validateBubblewrapAppArmorDirectoryChain(deps, filepath.Dir(deps.paths.parser)); err != nil {
		return err
	}
	return validateBubblewrapAppArmorParser(deps)
}

func validateBubblewrapAppArmorDirectoryChain(deps bubblewrapAppArmorDeps, target string) error {
	components, err := bubblewrapAppArmorDirectoryComponents(deps.paths.trustRoot, target)
	if err != nil {
		return err
	}
	for _, current := range components {
		if err := validateBubblewrapAppArmorDirectory(deps, current); err != nil {
			return err
		}
	}
	return nil
}

func bubblewrapAppArmorDirectoryComponents(trustRoot, target string) ([]string, error) {
	root := filepath.Clean(trustRoot)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("AppArmor path %s is outside trusted root %s", target, root)
	}
	components := []string{root}
	if rel == "." {
		return components, nil
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		components = append(components, current)
	}
	return components, nil
}

func validateBubblewrapAppArmorDirectory(deps bubblewrapAppArmorDeps, current string) error {
	stat, err := deps.lstat(current)
	if err != nil {
		return fmt.Errorf("inspect trusted AppArmor directory %s: %w", current, err)
	}
	if !stat.mode.IsDir() {
		return fmt.Errorf("trusted AppArmor path %s is not a directory", current)
	}
	if stat.uid != deps.trustedUID {
		return fmt.Errorf("trusted AppArmor directory %s is owned by UID %d, want %d", current, stat.uid, deps.trustedUID)
	}
	if stat.mode.Perm()&0o022 != 0 {
		return fmt.Errorf("trusted AppArmor directory %s mode is %#o, group/other write must be disabled", current, stat.mode.Perm())
	}
	return nil
}

func validateBubblewrapAppArmorParser(deps bubblewrapAppArmorDeps) (retErr error) {
	pathStat, err := deps.lstat(deps.paths.parser)
	if err != nil {
		return fmt.Errorf("inspect AppArmor parser %s: %w", deps.paths.parser, err)
	}
	if err := validateBubblewrapAppArmorExecutableStat(deps.paths.parser, pathStat, deps.trustedUID); err != nil {
		return err
	}
	file, err := deps.open(deps.paths.parser, os.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open AppArmor parser %s without following links: %w", deps.paths.parser, err)
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	openedStat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened AppArmor parser %s: %w", deps.paths.parser, err)
	}
	if err := validateBubblewrapAppArmorExecutableStat(deps.paths.parser, openedStat, deps.trustedUID); err != nil {
		return err
	}
	if pathStat.dev != openedStat.dev || pathStat.ino != openedStat.ino {
		return fmt.Errorf("AppArmor parser %s changed between path and descriptor inspection", deps.paths.parser)
	}
	return nil
}

func validateBubblewrapAppArmorExecutableStat(path string, stat bubblewrapFileStat, trustedUID uint32) error {
	if !stat.mode.IsRegular() {
		return fmt.Errorf("AppArmor parser %s is not a regular file", path)
	}
	if stat.uid != trustedUID {
		return fmt.Errorf("AppArmor parser %s is owned by UID %d, want %d", path, stat.uid, trustedUID)
	}
	if stat.nlink != 1 {
		return fmt.Errorf("AppArmor parser %s has %d links, want one", path, stat.nlink)
	}
	if stat.mode&(os.ModeSetuid|os.ModeSetgid) != 0 || stat.mode.Perm()&0o022 != 0 || stat.mode.Perm()&0o111 == 0 {
		return fmt.Errorf("AppArmor parser %s mode is %#o, want executable without setid or group/other write", path, stat.mode)
	}
	return nil
}

func prepareBubblewrapAppArmorProfile(
	ctx context.Context,
	deps bubblewrapAppArmorDeps,
) (serviceIdentityPathProof, bool, error) {
	proof, err := captureServiceIdentityPathProof(deps.paths.profile)
	if err != nil {
		return serviceIdentityPathProof{}, false, fmt.Errorf("inspect managed Bubblewrap AppArmor profile: %w", err)
	}
	if proof.Present {
		if err := validateCurrentBubblewrapAppArmorProfile(deps, proof); err != nil {
			return serviceIdentityPathProof{}, false, err
		}
		if err := dryParseBubblewrapAppArmorProfile(ctx, deps, deps.paths.profile); err != nil {
			return serviceIdentityPathProof{}, false, err
		}
		return proof, false, nil
	}
	return publishBubblewrapAppArmorProfile(ctx, deps)
}

func validateCurrentBubblewrapAppArmorProfile(deps bubblewrapAppArmorDeps, proof serviceIdentityPathProof) error {
	if proof.UID != deps.trustedUID || proof.Nlink != 1 || proof.Mode.Perm() != 0o644 || proof.Mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("managed Bubblewrap AppArmor path %s has unsafe ownership or mode", deps.paths.profile)
	}
	raw, err := deps.readFile(deps.paths.profile)
	if err != nil {
		return fmt.Errorf("read managed Bubblewrap AppArmor profile: %w", err)
	}
	if string(raw) != bubblewrapAppArmorProfileV1 {
		return fmt.Errorf("managed Bubblewrap AppArmor path %s contains divergent operator-owned content; preserve it and inspect manually", deps.paths.profile)
	}
	return nil
}

func publishBubblewrapAppArmorProfile(
	ctx context.Context,
	deps bubblewrapAppArmorDeps,
) (_ serviceIdentityPathProof, _ bool, retErr error) {
	tempProof, err := createTemporaryBubblewrapAppArmorProfile(deps)
	if err != nil {
		return serviceIdentityPathProof{}, false, err
	}
	tempOwned := true
	defer func() {
		if tempOwned {
			retErr = errors.Join(retErr, removeProvenanceSafeArtifact(tempProof, "temporary Bubblewrap AppArmor profile"))
		}
	}()
	if err := dryParseBubblewrapAppArmorProfile(ctx, deps, tempProof.Path); err != nil {
		return serviceIdentityPathProof{}, false, err
	}
	finalProof, err := linkBubblewrapAppArmorProfile(deps, tempProof)
	if err != nil {
		return serviceIdentityPathProof{}, false, err
	}
	tempOwned = false
	cleanupFinal := true
	defer func() {
		if cleanupFinal && retErr != nil {
			retErr = errors.Join(retErr, removeProvenanceSafeArtifact(finalProof, "managed Bubblewrap AppArmor profile after publication failure"))
		}
	}()
	if err := deps.syncDir(deps.paths.profileDir); err != nil {
		return serviceIdentityPathProof{}, false, fmt.Errorf("sync managed Bubblewrap AppArmor profile directory: %w", err)
	}
	if err := validateCurrentBubblewrapAppArmorProfile(deps, finalProof); err != nil {
		return serviceIdentityPathProof{}, false, err
	}
	cleanupFinal = false
	return finalProof, true, nil
}

func createTemporaryBubblewrapAppArmorProfile(deps bubblewrapAppArmorDeps) (serviceIdentityPathProof, error) {
	file, err := deps.createTemp(deps.paths.profileDir, ".yeet-bwrap-")
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("create temporary Bubblewrap AppArmor profile: %w", err)
	}
	tempPath := file.Name()
	if _, err := file.WriteString(bubblewrapAppArmorProfileV1); err != nil {
		_ = file.Close()
		_ = deps.remove(tempPath)
		return serviceIdentityPathProof{}, fmt.Errorf("write temporary Bubblewrap AppArmor profile: %w", err)
	}
	writeErr := file.Chmod(0o644)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = deps.remove(tempPath)
		return serviceIdentityPathProof{}, fmt.Errorf("finalize temporary Bubblewrap AppArmor profile: %w", err)
	}
	tempProof, err := captureServiceIdentityPathProof(tempPath)
	if err != nil {
		_ = deps.remove(tempPath)
		return serviceIdentityPathProof{}, err
	}
	return tempProof, nil
}

func linkBubblewrapAppArmorProfile(
	deps bubblewrapAppArmorDeps,
	tempProof serviceIdentityPathProof,
) (serviceIdentityPathProof, error) {
	if err := deps.link(tempProof.Path, deps.paths.profile); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("publish managed Bubblewrap AppArmor profile without replacing %s: %w", deps.paths.profile, err)
	}
	linkedProof := tempProof
	linkedProof.Nlink++
	if err := validateServiceIdentityPathProof(linkedProof); err != nil {
		return serviceIdentityPathProof{}, err
	}
	if err := removeProvenanceSafeArtifact(linkedProof, "temporary Bubblewrap AppArmor profile link"); err != nil {
		return serviceIdentityPathProof{}, err
	}
	finalProof := linkedProof
	finalProof.Path = deps.paths.profile
	finalProof.Nlink--
	if err := validateServiceIdentityPathProof(finalProof); err != nil {
		return serviceIdentityPathProof{}, errors.Join(
			err,
			removeProvenanceSafeArtifact(finalProof, "managed Bubblewrap AppArmor profile after publication validation failure"),
		)
	}
	return finalProof, nil
}

func dryParseBubblewrapAppArmorProfile(ctx context.Context, deps bubblewrapAppArmorDeps, path string) error {
	if err := validateBubblewrapAppArmorParser(deps); err != nil {
		return err
	}
	output, err := deps.run(ctx, deps.paths.parser, []string{"-Q", "-K", "--abort-on-error", path})
	if err != nil {
		return bubblewrapAppArmorCommandError("dry-parse managed Bubblewrap AppArmor profile", output, err)
	}
	return nil
}

type bubblewrapAppArmorInventory struct {
	parent     bool
	restricted bool
}

func (i bubblewrapAppArmorInventory) complete() bool { return i.parent && i.restricted }
func (i bubblewrapAppArmorInventory) partial() bool  { return i.parent != i.restricted }

func readBubblewrapAppArmorInventory(deps bubblewrapAppArmorDeps) (bubblewrapAppArmorInventory, error) {
	raw, err := deps.readFile(deps.paths.profiles)
	if err != nil {
		return bubblewrapAppArmorInventory{}, fmt.Errorf("read loaded AppArmor profile inventory %s: %w", deps.paths.profiles, err)
	}
	return bubblewrapAppArmorInventory{
		parent:     bubblewrapAppArmorInventoryContains(raw, bubblewrapAppArmorParentProfile),
		restricted: bubblewrapAppArmorInventoryContains(raw, bubblewrapAppArmorRestrictedProfile),
	}, nil
}

func bubblewrapAppArmorInventoryContains(raw []byte, name string) bool {
	prefix := name + " ("
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

func verifyBubblewrapAppArmorSecurity(ctx context.Context, deps bubblewrapAppArmorDeps) error {
	label, err := runBubblewrapAppArmorEvidence(ctx, deps, "/usr/bin/cat", "/proc/self/attr/current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(label)) != bubblewrapAppArmorExpectedLabel {
		return fmt.Errorf("bubblewrap child AppArmor label is %q, want %q", strings.TrimSpace(string(label)), bubblewrapAppArmorExpectedLabel)
	}
	capabilities, err := runBubblewrapAppArmorEvidence(ctx, deps, "/usr/bin/grep", "^CapEff:", "/proc/self/status")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(capabilities))
	if len(fields) != 2 || fields[0] != "CapEff:" || fields[1] != bubblewrapAppArmorZeroCapabilities {
		return fmt.Errorf("bubblewrap child effective capabilities are %q, want zero", strings.TrimSpace(string(capabilities)))
	}
	if diagnostic, err := runBubblewrapAppArmorEvidence(ctx, deps, "/usr/bin/unshare", "--user", "--map-root-user", "/usr/bin/true"); err == nil {
		return errors.New("bubblewrap child unexpectedly created a nested user namespace")
	} else if ctx.Err() != nil {
		return ctx.Err()
	} else if len(diagnostic) == 0 {
		return errors.New("bubblewrap nested user-namespace denial returned no diagnostic")
	}
	return nil
}

func runBubblewrapAppArmorEvidence(
	ctx context.Context,
	deps bubblewrapAppArmorDeps,
	workload ...string,
) ([]byte, error) {
	args := bubblewrapProbeArgs(bubblewrapProbeUID, bubblewrapProbeGID, deps.pathPresent)
	args = append(args[:len(args)-1], workload...)
	diagnostic, err := deps.runEvidence(ctx, serviceSandboxCommand{
		Path:       bubblewrapPath,
		Arguments:  args,
		Credential: &syscall.Credential{Uid: bubblewrapProbeUID, Gid: bubblewrapProbeGID},
	})
	if err != nil {
		return diagnostic, fmt.Errorf("run managed Bubblewrap AppArmor security probe %q; diagnostic: %s: %w", workload, diagnostic, err)
	}
	return diagnostic, nil
}

func rollbackBubblewrapAppArmorProfile(
	deps bubblewrapAppArmorDeps,
	proof serviceIdentityPathProof,
	published bool,
) error {
	if err := validateBubblewrapAppArmorParser(deps); err != nil {
		return fmt.Errorf("cannot safely roll back managed Bubblewrap AppArmor profile: %w", err)
	}
	output, err := deps.run(context.Background(), deps.paths.parser, []string{"-R", "-K", deps.paths.profile})
	if err != nil {
		return bubblewrapAppArmorCommandError("unload managed Bubblewrap AppArmor profile during rollback", output, err)
	}
	inventory, err := readBubblewrapAppArmorInventory(deps)
	if err != nil {
		return err
	}
	if inventory.parent || inventory.restricted {
		return errors.New("managed Bubblewrap AppArmor profile remained loaded after rollback")
	}
	if !published {
		return nil
	}
	return removeProvenanceSafeArtifact(proof, "managed Bubblewrap AppArmor profile")
}

func runBubblewrapAppArmorCommand(ctx context.Context, path string, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	var diagnostic bytes.Buffer
	command.Stdout = &diagnostic
	command.Stderr = &diagnostic
	err := command.Run()
	return diagnostic.Bytes(), err
}

func runBubblewrapAppArmorEvidenceCommand(ctx context.Context, request serviceSandboxCommand) ([]byte, error) {
	command := exec.CommandContext(ctx, request.Path, request.Arguments...)
	if request.Credential != nil {
		credential := *request.Credential
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &credential}
	}
	var diagnostic bytes.Buffer
	command.Stdout = &diagnostic
	command.Stderr = &diagnostic
	err := command.Run()
	return diagnostic.Bytes(), err
}

func bubblewrapAppArmorCommandError(action string, output []byte, err error) error {
	return fmt.Errorf("%s; diagnostic: %s: %w", action, output, err)
}
