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
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	bubblewrapPath     = "/usr/bin/bwrap"
	bubblewrapLockPath = "/run/yeet/bubblewrap.ensure.lock"
	aptGetPath         = "/usr/bin/apt-get"

	bubblewrapLockDir          = "/run/yeet"
	bubblewrapLockPollInterval = 10 * time.Millisecond
)

type bubblewrapFileStat struct {
	mode  os.FileMode
	uid   uint32
	nlink uint64
	dev   uint64
	ino   uint64
}

type bubblewrapDependencyFile interface {
	Stat() (bubblewrapFileStat, error)
	Fd() uintptr
	Chmod(os.FileMode) error
	Close() error
}

type bubblewrapOSFile struct {
	*os.File
}

func (f bubblewrapOSFile) Stat() (bubblewrapFileStat, error) {
	info, err := f.File.Stat()
	if err != nil {
		return bubblewrapFileStat{}, err
	}
	return bubblewrapStatFromFileInfo(info)
}

type bubblewrapDependencyDeps struct {
	lstat         func(string) (bubblewrapFileStat, error)
	open          func(string, int, os.FileMode) (bubblewrapDependencyFile, error)
	mkdir         func(string, os.FileMode) error
	flock         func(int, int) error
	run           func(context.Context, string, []string, []string) ([]byte, error)
	geteuid       func() int
	getegid       func() int
	pathPresent   func(string) bool
	readOSRelease func() (string, error)
	environ       func() []string
}

// EnsureBubblewrap makes the fixed host Bubblewrap binary ready for native
// sandbox activation without weakening host-wide security policy.
func EnsureBubblewrap(ctx context.Context) error {
	return ensureBubblewrapWith(ctx, defaultBubblewrapDependencyDeps())
}

func defaultBubblewrapDependencyDeps() bubblewrapDependencyDeps {
	return bubblewrapDependencyDeps{
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
		mkdir: os.Mkdir,
		flock: unix.Flock,
		run: func(ctx context.Context, path string, args, env []string) ([]byte, error) {
			command := exec.CommandContext(ctx, path, args...)
			if env != nil {
				command.Env = env
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			err := command.Run()
			return stderr.Bytes(), err
		},
		geteuid: os.Geteuid,
		getegid: os.Getegid,
		pathPresent: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		readOSRelease: readBubblewrapOSRelease,
		environ:       os.Environ,
	}
}

func ensureBubblewrapWith(ctx context.Context, deps bubblewrapDependencyDeps) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := acquireBubblewrapDependencyLock(ctx, deps)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, release())
	}()

	installed, err := inspectTrustedBubblewrap(deps)
	if err != nil {
		return err
	}
	if !installed {
		if err := installBubblewrap(ctx, deps); err != nil {
			return err
		}
		installed, err = inspectTrustedBubblewrap(deps)
		if err != nil {
			return fmt.Errorf("inspect /usr/bin/bwrap after package installation: %w", err)
		}
		if !installed {
			return fmt.Errorf("trusted Bubblewrap package installation completed without providing /usr/bin/bwrap")
		}
	}
	return probeBubblewrap(ctx, deps)
}

func acquireBubblewrapDependencyLock(ctx context.Context, deps bubblewrapDependencyDeps) (func() error, error) {
	file, err := openValidatedBubblewrapDependencyLock(deps)
	if err != nil {
		return nil, err
	}
	if err := waitForBubblewrapDependencyLock(ctx, deps, int(file.Fd())); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := revalidateAcquiredBubblewrapDependencyLock(ctx, deps, file); err != nil {
		return nil, errors.Join(err, releaseBubblewrapDependencyLock(deps, file))
	}
	return func() error {
		return releaseBubblewrapDependencyLock(deps, file)
	}, nil
}

func revalidateAcquiredBubblewrapDependencyLock(ctx context.Context, deps bubblewrapDependencyDeps, file bubblewrapDependencyFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("reinspect acquired Bubblewrap dependency lock %s: %w", bubblewrapLockPath, err)
	}
	if err := validateBubblewrapLockStat(stat); err != nil {
		return err
	}
	pathStat, err := deps.lstat(bubblewrapLockPath)
	if err != nil {
		return fmt.Errorf("reinspect acquired Bubblewrap dependency lock path %s: %w", bubblewrapLockPath, err)
	}
	if err := validateBubblewrapLockStat(pathStat); err != nil {
		return err
	}
	if pathStat.dev != stat.dev || pathStat.ino != stat.ino {
		return fmt.Errorf("bubblewrap dependency lock path %s no longer names the acquired lock", bubblewrapLockPath)
	}
	return ctx.Err()
}

func openValidatedBubblewrapDependencyLock(deps bubblewrapDependencyDeps) (bubblewrapDependencyFile, error) {
	if err := validateBubblewrapLockDirectory(deps); err != nil {
		return nil, err
	}
	flags := os.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	file, created, err := openBubblewrapDependencyLock(deps, flags)
	if err != nil {
		return nil, err
	}
	if created {
		if err := file.Chmod(0o600); err != nil {
			return nil, errors.Join(fmt.Errorf("secure Bubblewrap dependency lock %s: %w", bubblewrapLockPath, err), file.Close())
		}
	}
	stat, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect Bubblewrap dependency lock %s: %w", bubblewrapLockPath, err), file.Close())
	}
	if err := validateBubblewrapLockStat(stat); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func waitForBubblewrapDependencyLock(ctx context.Context, deps bubblewrapDependencyDeps, fd int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := deps.flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock Bubblewrap dependency lifecycle: %w", err)
		}
		if err := waitForBubblewrapDependencyLockPoll(ctx); err != nil {
			return err
		}
	}
}

func waitForBubblewrapDependencyLockPoll(ctx context.Context) error {
	timer := time.NewTimer(bubblewrapLockPollInterval)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func releaseBubblewrapDependencyLock(deps bubblewrapDependencyDeps, file bubblewrapDependencyFile) error {
	unlockErr := deps.flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock Bubblewrap dependency lifecycle: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close Bubblewrap dependency lock: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}

func validateBubblewrapLockDirectory(deps bubblewrapDependencyDeps) error {
	runStat, err := deps.lstat("/run")
	if err != nil {
		return fmt.Errorf("inspect Bubblewrap lock parent /run: %w", err)
	}
	if err := validateBubblewrapLockDirectoryStat("/run", runStat); err != nil {
		return err
	}
	stat, err := deps.lstat(bubblewrapLockDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := deps.mkdir(bubblewrapLockDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create Bubblewrap lock directory %s: %w", bubblewrapLockDir, err)
		}
		stat, err = deps.lstat(bubblewrapLockDir)
	}
	if err != nil {
		return fmt.Errorf("inspect Bubblewrap lock directory %s: %w", bubblewrapLockDir, err)
	}
	return validateBubblewrapLockDirectoryStat(bubblewrapLockDir, stat)
}

func validateBubblewrapLockDirectoryStat(path string, stat bubblewrapFileStat) error {
	if !stat.mode.IsDir() {
		return fmt.Errorf("host Bubblewrap lock directory %s is not a directory", path)
	}
	if stat.uid != 0 {
		return fmt.Errorf("host Bubblewrap lock directory %s is owned by UID %d, want root", path, stat.uid)
	}
	if stat.mode.Perm()&0o022 != 0 {
		return fmt.Errorf("host Bubblewrap lock directory %s mode is %#o, group/other write must be disabled", path, stat.mode.Perm())
	}
	return nil
}

func openBubblewrapDependencyLock(deps bubblewrapDependencyDeps, flags int) (bubblewrapDependencyFile, bool, error) {
	file, err := deps.open(bubblewrapLockPath, flags|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return file, true, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, false, fmt.Errorf("create Bubblewrap dependency lock %s: %w", bubblewrapLockPath, err)
	}
	file, err = deps.open(bubblewrapLockPath, flags, 0)
	if err != nil {
		return nil, false, fmt.Errorf("open existing Bubblewrap dependency lock %s: %w", bubblewrapLockPath, err)
	}
	return file, false, nil
}

func validateBubblewrapLockStat(stat bubblewrapFileStat) error {
	if !stat.mode.IsRegular() {
		return fmt.Errorf("host Bubblewrap dependency lock %s is not a regular file", bubblewrapLockPath)
	}
	if stat.uid != 0 {
		return fmt.Errorf("host Bubblewrap dependency lock %s is owned by UID %d, want root", bubblewrapLockPath, stat.uid)
	}
	if stat.nlink != 1 {
		return fmt.Errorf("host Bubblewrap dependency lock %s has %d links, want one", bubblewrapLockPath, stat.nlink)
	}
	if stat.mode.Perm() != 0o600 || stat.mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("host Bubblewrap dependency lock %s mode is %#o, want 0600", bubblewrapLockPath, stat.mode)
	}
	return nil
}

func inspectTrustedBubblewrap(deps bubblewrapDependencyDeps) (installed bool, err error) {
	return inspectTrustedExecutable(deps, bubblewrapPath)
}

func inspectTrustedExecutable(deps bubblewrapDependencyDeps, path string) (installed bool, err error) {
	pathStat, err := deps.lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := validateTrustedExecutableStat(path, pathStat); err != nil {
		return false, err
	}
	file, err := deps.open(path, os.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("open trusted %s: %w", path, err)
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	openedStat, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect opened %s: %w", path, err)
	}
	if err := validateTrustedExecutableStat(path, openedStat); err != nil {
		return false, err
	}
	if pathStat.dev != openedStat.dev || pathStat.ino != openedStat.ino {
		return false, fmt.Errorf("%s was replaced between path inspection and descriptor validation", path)
	}
	return true, nil
}

func validateTrustedExecutableStat(path string, stat bubblewrapFileStat) error {
	if !stat.mode.IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if stat.uid != 0 {
		return fmt.Errorf("%s is owned by UID %d, want root", path, stat.uid)
	}
	if stat.nlink != 1 {
		return fmt.Errorf("%s has %d links, want one", path, stat.nlink)
	}
	if stat.mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("%s has setuid or setgid mode bits", path)
	}
	if stat.mode.Perm()&0o022 != 0 {
		return fmt.Errorf("%s mode is %#o, group/other write must be disabled", path, stat.mode.Perm())
	}
	return nil
}

func installBubblewrap(ctx context.Context, deps bubblewrapDependencyDeps) error {
	osRelease, err := deps.readOSRelease()
	if err != nil {
		return fmt.Errorf("read host OS release before installing Bubblewrap: %w. %s", err, bubblewrapManualInstallGuidance(deps))
	}
	if !bubblewrapAPTSupported(osRelease) {
		return fmt.Errorf("required Bubblewrap binary is missing at /usr/bin/bwrap; automatic installation is supported only on Debian and Ubuntu. %s", bubblewrapManualInstallGuidance(deps))
	}
	if err := requireTrustedAPTGet(deps); err != nil {
		return err
	}
	if output, err := deps.run(ctx, aptGetPath, []string{"update"}, nil); err != nil {
		return bubblewrapCommandError("update apt package metadata for Bubblewrap", err, output)
	}
	env := append(append([]string(nil), deps.environ()...), "DEBIAN_FRONTEND=noninteractive")
	if err := requireTrustedAPTGet(deps); err != nil {
		return err
	}
	if output, err := deps.run(ctx, aptGetPath, []string{"install", "-y", "bubblewrap"}, env); err != nil {
		return bubblewrapCommandError("install the bubblewrap package", err, output)
	}
	return nil
}

func requireTrustedAPTGet(deps bubblewrapDependencyDeps) error {
	installed, err := inspectTrustedAPTGet(deps)
	if err != nil {
		return fmt.Errorf("cannot safely execute %s while installing Bubblewrap: %w. %s", aptGetPath, err, bubblewrapManualInstallGuidance(deps))
	}
	if !installed {
		return fmt.Errorf("required Bubblewrap binary is missing at /usr/bin/bwrap and /usr/bin/apt-get is unavailable. %s", bubblewrapManualInstallGuidance(deps))
	}
	return nil
}

func inspectTrustedAPTGet(deps bubblewrapDependencyDeps) (bool, error) {
	for _, path := range []string{"/", "/usr", "/usr/bin"} {
		stat, err := deps.lstat(path)
		if err != nil {
			return false, fmt.Errorf("inspect trusted apt-get directory %s: %w", path, err)
		}
		if err := validateTrustedAPTDirectoryStat(path, stat); err != nil {
			return false, err
		}
	}
	return inspectTrustedExecutable(deps, aptGetPath)
}

func validateTrustedAPTDirectoryStat(path string, stat bubblewrapFileStat) error {
	if !stat.mode.IsDir() {
		return fmt.Errorf("trusted apt-get directory %s is not a directory", path)
	}
	if stat.uid != 0 {
		return fmt.Errorf("trusted apt-get directory %s is owned by UID %d, want root", path, stat.uid)
	}
	if stat.mode.Perm()&0o022 != 0 {
		return fmt.Errorf("trusted apt-get directory %s mode is %#o, group/other write must be disabled", path, stat.mode.Perm())
	}
	return nil
}

func bubblewrapAPTSupported(osRelease string) bool {
	for _, line := range strings.Split(osRelease, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "ID" && key != "ID_LIKE" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"")
		for _, id := range strings.Fields(value) {
			if strings.EqualFold(id, "debian") || strings.EqualFold(id, "ubuntu") {
				return true
			}
		}
	}
	return false
}

func probeBubblewrap(ctx context.Context, deps bubblewrapDependencyDeps) error {
	args := bubblewrapProbeArgs(deps.geteuid(), deps.getegid(), deps.pathPresent)
	output, err := deps.run(ctx, bubblewrapPath, args, nil)
	if err == nil {
		return nil
	}
	raw := string(output)
	lower := strings.ToLower(raw)
	guidance := "inspect host user-namespace and AppArmor policy without disabling either globally"
	switch {
	case strings.Contains(lower, "unknown option") || strings.Contains(lower, "unrecognized option"):
		guidance = "install a compatible Bubblewrap version that supports the required namespace options"
	case strings.Contains(lower, "apparmor"):
		guidance = "inspect the AppArmor policy for /usr/bin/bwrap without disabling AppArmor globally"
	case strings.Contains(lower, "user namespace") || strings.Contains(lower, "new namespace") || strings.Contains(lower, "operation not permitted"):
		guidance = "inspect host user-namespace policy without relaxing it globally"
	}
	return fmt.Errorf("functional Bubblewrap probe failed; %s; stderr: %s: %w", guidance, raw, err)
}

func bubblewrapProbeArgs(uid, gid int, pathPresent func(string) bool) []string {
	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--disable-userns",
		"--uid", strconv.Itoa(uid), "--gid", strconv.Itoa(gid),
		"--hostname", "yeet-bwrap-probe",
		"--new-session", "--die-with-parent",
	}
	args = append(args, bubblewrapFixedRuntimeMountArgs(pathPresent)...)
	return append(args,
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run",
		"--", "/usr/bin/true",
	)
}

func bubblewrapFixedRuntimeMountArgs(pathPresent func(string) bool) []string {
	args := []string{"--ro-bind", "/usr", "/usr"}
	for _, path := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		if pathPresent(path) {
			args = append(args, "--ro-bind", path, path)
		}
	}
	return args
}

func bubblewrapManualInstallGuidance(deps bubblewrapDependencyDeps) string {
	command := append([]string{bubblewrapPath}, bubblewrapProbeArgs(deps.geteuid(), deps.getegid(), deps.pathPresent)...)
	return "Install the bubblewrap package at /usr/bin/bwrap manually, then verify host user-namespace and AppArmor policy with: " + strings.Join(command, " ")
}

func bubblewrapCommandError(action string, err error, output []byte) error {
	return fmt.Errorf("%s: %w; diagnostic: %s", action, err, string(output))
}

func readBubblewrapOSRelease() (string, error) {
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		raw, err := os.ReadFile(path)
		if err == nil {
			return string(raw), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

func bubblewrapStatFromFileInfo(info os.FileInfo) (bubblewrapFileStat, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return bubblewrapFileStat{}, fmt.Errorf("unsupported file metadata for %s", info.Name())
	}
	return bubblewrapFileStat{
		mode:  info.Mode(),
		uid:   stat.Uid,
		nlink: uint64(stat.Nlink),
		dev:   uint64(stat.Dev),
		ino:   uint64(stat.Ino),
	}, nil
}
