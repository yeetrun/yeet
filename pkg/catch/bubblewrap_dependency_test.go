// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type bubblewrapTestCommand struct {
	path       string
	args       []string
	env        []string
	credential *syscall.Credential
}

type bubblewrapTestStatResult struct {
	stat bubblewrapFileStat
	err  error
}

type bubblewrapTestOpenCall struct {
	path  string
	flags int
	mode  os.FileMode
	file  *bubblewrapTestFile
}

type bubblewrapTestMkdirCall struct {
	path string
	mode os.FileMode
}

type bubblewrapTestFile struct {
	stat        bubblewrapFileStat
	statResults []bubblewrapTestStatResult
	statCalls   int
	fd          uintptr
	chmodCalls  []os.FileMode
	chmodErr    error
	closeCalls  int
	closeErr    error
}

func (f *bubblewrapTestFile) Stat() (bubblewrapFileStat, error) {
	f.statCalls++
	if len(f.statResults) != 0 {
		index := min(f.statCalls-1, len(f.statResults)-1)
		result := f.statResults[index]
		return result.stat, result.err
	}
	return f.stat, nil
}
func (f *bubblewrapTestFile) Fd() uintptr { return f.fd }
func (f *bubblewrapTestFile) Chmod(mode os.FileMode) error {
	f.chmodCalls = append(f.chmodCalls, mode)
	if f.chmodErr != nil {
		return f.chmodErr
	}
	f.stat.mode = mode
	return nil
}
func (f *bubblewrapTestFile) Close() error {
	f.closeCalls++
	return f.closeErr
}

type bubblewrapTestHost struct {
	mu sync.Mutex

	binaryPresent    bool
	aptPresent       bool
	binaryLstat      bubblewrapFileStat
	binaryOpened     bubblewrapFileStat
	aptLstat         bubblewrapFileStat
	aptOpened        bubblewrapFileStat
	lockStat         bubblewrapFileStat
	lockStatResults  []bubblewrapTestStatResult
	lockChmodErr     error
	lockCloseErr     error
	lockExists       bool
	lstatResults     map[string][]bubblewrapTestStatResult
	lstatCalls       map[string]int
	openCalls        []bubblewrapTestOpenCall
	mkdirCalls       []bubblewrapTestMkdirCall
	mkdirErr         error
	lockFiles        []*bubblewrapTestFile
	binaryFiles      []*bubblewrapTestFile
	aptFiles         []*bubblewrapTestFile
	osRelease        string
	osReleaseErr     error
	probeOutput      string
	probeErr         error
	aptUpdateOutput  string
	aptUpdateErr     error
	aptInstallOutput string
	aptInstallErr    error
	commands         []bubblewrapTestCommand
	binaryLstatCalls int
	flockCalls       []int
}

func newBubblewrapTestHost() *bubblewrapTestHost {
	trusted := bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 1, dev: 8, ino: 101}
	trustedAPT := bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 1, dev: 8, ino: 202}
	return &bubblewrapTestHost{
		binaryPresent: true,
		aptPresent:    true,
		binaryLstat:   trusted,
		binaryOpened:  trusted,
		aptLstat:      trustedAPT,
		aptOpened:     trustedAPT,
		lockStat:      bubblewrapFileStat{mode: 0o600, uid: 0, nlink: 1, dev: 8, ino: 50},
		lockExists:    true,
		lstatResults:  make(map[string][]bubblewrapTestStatResult),
		lstatCalls:    make(map[string]int),
		osRelease:     "ID=debian\n",
	}
}

func (h *bubblewrapTestHost) deps() bubblewrapDependencyDeps {
	return bubblewrapDependencyDeps{
		lstat: func(path string) (bubblewrapFileStat, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.lstatCalls[path]++
			if results := h.lstatResults[path]; len(results) != 0 {
				index := min(h.lstatCalls[path]-1, len(results)-1)
				return results[index].stat, results[index].err
			}
			switch path {
			case "/":
				return bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 0, nlink: 1, dev: 8, ino: 1}, nil
			case "/run":
				return bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 0, nlink: 1, dev: 8, ino: 2}, nil
			case "/run/yeet":
				return bubblewrapFileStat{mode: os.ModeDir | 0o700, uid: 0, nlink: 1, dev: 8, ino: 40}, nil
			case "/usr":
				return bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 0, nlink: 1, dev: 8, ino: 3}, nil
			case "/usr/bin":
				return bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 0, nlink: 1, dev: 8, ino: 4}, nil
			case bubblewrapLockPath:
				return h.lockStat, nil
			case bubblewrapPath:
				h.binaryLstatCalls++
				if !h.binaryPresent {
					return bubblewrapFileStat{}, os.ErrNotExist
				}
				return h.binaryLstat, nil
			case aptGetPath:
				if !h.aptPresent {
					return bubblewrapFileStat{}, os.ErrNotExist
				}
				return h.aptLstat, nil
			default:
				return bubblewrapFileStat{}, os.ErrNotExist
			}
		},
		open: func(path string, flags int, mode os.FileMode) (bubblewrapDependencyFile, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			call := bubblewrapTestOpenCall{path: path, flags: flags, mode: mode}
			switch path {
			case bubblewrapLockPath:
				if flags&unix.O_EXCL != 0 && h.lockExists {
					h.openCalls = append(h.openCalls, call)
					return nil, os.ErrExist
				}
				h.lockExists = true
				file := &bubblewrapTestFile{
					stat:        h.lockStat,
					statResults: append([]bubblewrapTestStatResult(nil), h.lockStatResults...),
					fd:          uintptr(50 + len(h.lockFiles)),
					chmodErr:    h.lockChmodErr,
					closeErr:    h.lockCloseErr,
				}
				h.lockFiles = append(h.lockFiles, file)
				call.file = file
				h.openCalls = append(h.openCalls, call)
				return file, nil
			case bubblewrapPath:
				file := &bubblewrapTestFile{stat: h.binaryOpened, fd: uintptr(101 + len(h.binaryFiles))}
				h.binaryFiles = append(h.binaryFiles, file)
				call.file = file
				h.openCalls = append(h.openCalls, call)
				return file, nil
			case aptGetPath:
				file := &bubblewrapTestFile{stat: h.aptOpened, fd: uintptr(202 + len(h.aptFiles))}
				h.aptFiles = append(h.aptFiles, file)
				call.file = file
				h.openCalls = append(h.openCalls, call)
				return file, nil
			default:
				h.openCalls = append(h.openCalls, call)
				return nil, os.ErrNotExist
			}
		},
		mkdir: func(path string, mode os.FileMode) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.mkdirCalls = append(h.mkdirCalls, bubblewrapTestMkdirCall{path: path, mode: mode})
			return h.mkdirErr
		},
		flock: func(_ int, operation int) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.flockCalls = append(h.flockCalls, operation)
			return nil
		},
		run: func(_ context.Context, path string, args, env []string) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.commands = append(h.commands, bubblewrapTestCommand{
				path: path,
				args: append([]string(nil), args...),
				env:  append([]string(nil), env...),
			})
			switch path {
			case bubblewrapPath:
				return []byte(h.probeOutput), h.probeErr
			case aptGetPath:
				if reflect.DeepEqual(args, []string{"update"}) {
					return []byte(h.aptUpdateOutput), h.aptUpdateErr
				}
				if reflect.DeepEqual(args, []string{"install", "-y", "bubblewrap"}) {
					if h.aptInstallErr == nil {
						h.binaryPresent = true
					}
					return []byte(h.aptInstallOutput), h.aptInstallErr
				}
			}
			return nil, errors.New("unexpected command")
		},
		runAs: func(_ context.Context, command serviceSandboxCommand) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.commands = append(h.commands, bubblewrapTestCommand{
				path:       command.Path,
				args:       append([]string(nil), command.Arguments...),
				credential: command.Credential,
			})
			if command.Path == bubblewrapPath {
				return []byte(h.probeOutput), h.probeErr
			}
			return nil, errors.New("unexpected credentialed command")
		},
		pathPresent: func(path string) bool {
			return path == "/bin" || path == "/lib64"
		},
		readOSRelease:          func() (string, error) { return h.osRelease, h.osReleaseErr },
		environ:                func() []string { return []string{"PATH=/trusted"} },
		ensureRestrictedUserNS: func(_ context.Context, initial error) error { return initial },
	}
}

func expectedBubblewrapProbeArgs() []string {
	return []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--disable-userns",
		"--uid", "65534", "--gid", "65534",
		"--hostname", "yeet-bwrap-probe",
		"--new-session", "--die-with-parent",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib64", "/lib64",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run",
		"--", "/usr/bin/true",
	}
}

func TestEnsureBubblewrapTrustedBinarySkipsAptAndRunsExactProbe(t *testing.T) {
	host := newBubblewrapTestHost()
	if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
		t.Fatalf("ensureBubblewrapWith: %v", err)
	}

	if len(host.commands) != 1 {
		t.Fatalf("commands = %#v, want one Bubblewrap probe", host.commands)
	}
	command := host.commands[0]
	if command.path != "/usr/bin/bwrap" || !reflect.DeepEqual(command.args, expectedBubblewrapProbeArgs()) {
		t.Fatalf("probe = %q %#v, want fixed /usr/bin/bwrap argv %#v", command.path, command.args, expectedBubblewrapProbeArgs())
	}
	for _, forbidden := range []string{"--unshare-all", "--unshare-net", "--share-net", "--unshare-cgroup", "--as-pid-1"} {
		if containsBubblewrapArg(command.args, forbidden) {
			t.Fatalf("probe contains forbidden argument %q: %#v", forbidden, command.args)
		}
	}
}

func TestEnsureBubblewrapRunsReadinessProbeAsFixedNonRootCredential(t *testing.T) {
	host := newBubblewrapTestHost()
	if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
		t.Fatalf("ensureBubblewrapWith: %v", err)
	}

	if len(host.commands) != 1 {
		t.Fatalf("commands = %#v, want one Bubblewrap probe", host.commands)
	}
	credential := host.commands[0].credential
	if credential == nil || credential.Uid != 65534 || credential.Gid != 65534 {
		t.Fatalf("credential = %#v, want UID 65534 GID 65534", credential)
	}
}

func TestEnsureBubblewrapRepairsOnlyFailedNonRootProbe(t *testing.T) {
	host := newBubblewrapTestHost()
	host.probeErr = errors.New("uid map denied")
	deps := host.deps()
	repairCalls := 0
	deps.ensureRestrictedUserNS = func(_ context.Context, initial error) error {
		repairCalls++
		if !errors.Is(initial, host.probeErr) {
			t.Fatalf("initial error = %v, want wrapped %v", initial, host.probeErr)
		}
		return nil
	}

	if err := ensureBubblewrapWith(context.Background(), deps); err != nil {
		t.Fatalf("ensureBubblewrapWith: %v", err)
	}
	if repairCalls != 1 {
		t.Fatalf("restricted-userns repair calls = %d, want 1", repairCalls)
	}
}

func TestEnsureBubblewrapLockOpenAndCleanupContract(t *testing.T) {
	baseFlags := os.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW

	t.Run("creates secure lock", func(t *testing.T) {
		host := newBubblewrapTestHost()
		host.lockExists = false
		if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
			t.Fatalf("ensureBubblewrapWith: %v", err)
		}
		calls := bubblewrapTestOpenCallsForPath(host, bubblewrapLockPath)
		if len(calls) != 1 {
			t.Fatalf("lock open calls = %#v, want one exclusive create", calls)
		}
		want := []bubblewrapTestOpenCall{{path: bubblewrapLockPath, flags: baseFlags | os.O_CREATE | os.O_EXCL, mode: 0o600, file: calls[0].file}}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("lock open calls = %#v, want %#v", calls, want)
		}
		file := calls[0].file
		if !reflect.DeepEqual(file.chmodCalls, []os.FileMode{0o600}) {
			t.Fatalf("created lock chmod calls = %#v, want [0600]", file.chmodCalls)
		}
		if file.statCalls != 2 {
			t.Fatalf("created lock stat calls = %d, want validation before and after flock", file.statCalls)
		}
		assertBubblewrapTestLockCleanup(t, host, true)
	})

	t.Run("opens existing secure lock", func(t *testing.T) {
		host := newBubblewrapTestHost()
		if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
			t.Fatalf("ensureBubblewrapWith: %v", err)
		}
		calls := bubblewrapTestOpenCallsForPath(host, bubblewrapLockPath)
		if len(calls) != 2 {
			t.Fatalf("lock open calls = %#v, want exclusive create then existing open", calls)
		}
		if calls[0].flags != baseFlags|os.O_CREATE|os.O_EXCL || calls[0].mode != 0o600 || calls[0].file != nil {
			t.Fatalf("exclusive create call = %#v, want exact flags/mode and EEXIST", calls[0])
		}
		if calls[1].flags != baseFlags || calls[1].mode != 0 || calls[1].file == nil {
			t.Fatalf("existing lock open call = %#v, want exact flags and mode zero", calls[1])
		}
		if len(calls[1].file.chmodCalls) != 0 {
			t.Fatalf("existing lock chmod calls = %#v, want none", calls[1].file.chmodCalls)
		}
		if calls[1].file.statCalls != 2 {
			t.Fatalf("existing lock stat calls = %d, want validation before and after flock", calls[1].file.statCalls)
		}
		assertBubblewrapTestLockCleanup(t, host, true)
	})
}

func TestEnsureBubblewrapValidatesLockDirectoryChainAndCreationRace(t *testing.T) {
	t.Run("trusted existing chain", func(t *testing.T) {
		host := newBubblewrapTestHost()
		if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
			t.Fatal(err)
		}
		if host.lstatCalls["/run"] != 1 || host.lstatCalls[bubblewrapLockDir] != 1 || len(host.mkdirCalls) != 0 {
			t.Fatalf("directory inspections/mkdir = %d/%d/%#v, want trusted existing chain", host.lstatCalls["/run"], host.lstatCalls[bubblewrapLockDir], host.mkdirCalls)
		}
	})

	t.Run("creates missing directory at 0700 and revalidates", func(t *testing.T) {
		host := newBubblewrapTestHost()
		host.lockExists = false
		host.lstatResults[bubblewrapLockDir] = []bubblewrapTestStatResult{
			{err: os.ErrNotExist},
			{stat: bubblewrapFileStat{mode: os.ModeDir | 0o700, uid: 0, nlink: 1}},
		}
		if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
			t.Fatal(err)
		}
		want := []bubblewrapTestMkdirCall{{path: bubblewrapLockDir, mode: 0o700}}
		if !reflect.DeepEqual(host.mkdirCalls, want) || host.lstatCalls[bubblewrapLockDir] != 2 {
			t.Fatalf("mkdir/reinspection = %#v/%d, want %#v/2", host.mkdirCalls, host.lstatCalls[bubblewrapLockDir], want)
		}
	})

	for _, test := range []struct {
		name string
		path string
		stat bubblewrapFileStat
	}{
		{name: "run non-directory", path: "/run", stat: bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 1}},
		{name: "run non-root-owned", path: "/run", stat: bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 1000, nlink: 1}},
		{name: "run writable", path: "/run", stat: bubblewrapFileStat{mode: os.ModeDir | 0o775, uid: 0, nlink: 1}},
		{name: "yeet non-directory", path: bubblewrapLockDir, stat: bubblewrapFileStat{mode: 0o700, uid: 0, nlink: 1}},
		{name: "yeet non-root-owned", path: bubblewrapLockDir, stat: bubblewrapFileStat{mode: os.ModeDir | 0o700, uid: 1000, nlink: 1}},
		{name: "yeet writable", path: bubblewrapLockDir, stat: bubblewrapFileStat{mode: os.ModeDir | 0o720, uid: 0, nlink: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.lstatResults[test.path] = []bubblewrapTestStatResult{{stat: test.stat}}
			if err := ensureBubblewrapWith(context.Background(), host.deps()); err == nil {
				t.Fatalf("ensureBubblewrapWith accepted unsafe %s", test.path)
			}
			if len(host.lockFiles) != 0 || len(host.commands) != 0 {
				t.Fatalf("unsafe directory lock files/commands = %d/%#v, want none", len(host.lockFiles), host.commands)
			}
		})
	}

	t.Run("creation race is revalidated and rejected", func(t *testing.T) {
		host := newBubblewrapTestHost()
		host.mkdirErr = os.ErrExist
		host.lstatResults[bubblewrapLockDir] = []bubblewrapTestStatResult{
			{err: os.ErrNotExist},
			{stat: bubblewrapFileStat{mode: os.ModeSymlink | 0o777, uid: 1000, nlink: 1}},
		}
		if err := ensureBubblewrapWith(context.Background(), host.deps()); err == nil {
			t.Fatal("ensureBubblewrapWith accepted unsafe lock-directory creation race")
		}
		if len(host.mkdirCalls) != 1 || len(host.lockFiles) != 0 || len(host.commands) != 0 {
			t.Fatalf("creation race mkdir/locks/commands = %#v/%d/%#v", host.mkdirCalls, len(host.lockFiles), host.commands)
		}
	})
}

func TestEnsureBubblewrapMissingBinaryInstallsThenReinspectsAndProbes(t *testing.T) {
	host := newBubblewrapTestHost()
	host.binaryPresent = false

	if err := ensureBubblewrapWith(context.Background(), host.deps()); err != nil {
		t.Fatalf("ensureBubblewrapWith: %v", err)
	}

	want := []bubblewrapTestCommand{
		{path: "/usr/bin/apt-get", args: []string{"update"}},
		{path: "/usr/bin/apt-get", args: []string{"install", "-y", "bubblewrap"}, env: []string{"PATH=/trusted", "DEBIAN_FRONTEND=noninteractive"}},
		{path: "/usr/bin/bwrap", args: expectedBubblewrapProbeArgs(), credential: &syscall.Credential{Uid: 65534, Gid: 65534}},
	}
	if !reflect.DeepEqual(host.commands, want) {
		t.Fatalf("commands = %#v, want %#v", host.commands, want)
	}
	if host.binaryLstatCalls != 2 {
		t.Fatalf("Bubblewrap lstat calls = %d, want missing check plus post-install reinspection", host.binaryLstatCalls)
	}
	for _, path := range []string{"/", "/usr", "/usr/bin", aptGetPath} {
		if host.lstatCalls[path] != 2 {
			t.Fatalf("apt trust lstat calls for %s = %d, want one immediately before each apt invocation", path, host.lstatCalls[path])
		}
	}
	if len(host.aptFiles) != 2 {
		t.Fatalf("opened apt descriptors = %d, want one provenance check before each apt invocation", len(host.aptFiles))
	}
	for index, file := range host.aptFiles {
		if file.closeCalls != 1 {
			t.Fatalf("apt descriptor %d close calls = %d, want 1", index, file.closeCalls)
		}
	}
	for _, call := range bubblewrapTestOpenCallsForPath(host, aptGetPath) {
		wantFlags := os.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if call.flags != wantFlags || call.mode != 0 {
			t.Fatalf("apt provenance open = %#v, want flags %#x and mode 0", call, wantFlags)
		}
	}
}

func TestEnsureBubblewrapRejectsUnsafeAptWithoutAnyCommand(t *testing.T) {
	trusted := newBubblewrapTestHost().aptLstat
	for _, test := range []struct {
		name   string
		lstat  bubblewrapFileStat
		opened bubblewrapFileStat
	}{
		{name: "symlink", lstat: bubblewrapFileStat{mode: os.ModeSymlink | 0o777, uid: 0, nlink: 1}},
		{name: "non-regular", lstat: bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 0, nlink: 1}},
		{name: "non-root-owned", lstat: bubblewrapFileStat{mode: 0o755, uid: 1000, nlink: 1}},
		{name: "multi-link", lstat: bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 2}},
		{name: "setuid", lstat: bubblewrapFileStat{mode: os.ModeSetuid | 0o755, uid: 0, nlink: 1}},
		{name: "setgid", lstat: bubblewrapFileStat{mode: os.ModeSetgid | 0o755, uid: 0, nlink: 1}},
		{name: "group-writable", lstat: bubblewrapFileStat{mode: 0o775, uid: 0, nlink: 1}},
		{name: "world-writable", lstat: bubblewrapFileStat{mode: 0o757, uid: 0, nlink: 1}},
		{name: "replaced-across-lstat-open", lstat: trusted, opened: bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 1, dev: trusted.dev, ino: trusted.ino + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.binaryPresent = false
			host.aptLstat = test.lstat
			if test.opened.mode != 0 || test.opened.nlink != 0 {
				host.aptOpened = test.opened
			} else {
				host.aptOpened = test.lstat
			}
			err := ensureBubblewrapWith(context.Background(), host.deps())
			if err == nil || !strings.Contains(err.Error(), "Install the bubblewrap package at /usr/bin/bwrap manually") {
				t.Fatalf("unsafe apt error = %v, want manual install/probe guidance", err)
			}
			if len(host.commands) != 0 {
				t.Fatalf("unsafe apt commands = %#v, want no apt or probe", host.commands)
			}
			assertBubblewrapTestLockCleanup(t, host, true)
		})
	}
}

func TestEnsureBubblewrapRejectsUnsafeAptDirectoryChainWithoutAnyCommand(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		stat bubblewrapFileStat
	}{
		{name: "root non-directory", path: "/", stat: bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 1}},
		{name: "usr symlink", path: "/usr", stat: bubblewrapFileStat{mode: os.ModeSymlink | 0o777, uid: 0, nlink: 1}},
		{name: "usr non-root-owned", path: "/usr", stat: bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 1000, nlink: 1}},
		{name: "usr group-writable", path: "/usr", stat: bubblewrapFileStat{mode: os.ModeDir | 0o775, uid: 0, nlink: 1}},
		{name: "usr-bin world-writable", path: "/usr/bin", stat: bubblewrapFileStat{mode: os.ModeDir | 0o757, uid: 0, nlink: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.binaryPresent = false
			host.lstatResults[test.path] = []bubblewrapTestStatResult{{stat: test.stat}}
			err := ensureBubblewrapWith(context.Background(), host.deps())
			if err == nil || !strings.Contains(err.Error(), "Install the bubblewrap package at /usr/bin/bwrap manually") {
				t.Fatalf("unsafe apt directory error = %v, want manual guidance", err)
			}
			if len(host.commands) != 0 {
				t.Fatalf("unsafe apt directory commands = %#v, want no apt or probe", host.commands)
			}
			assertBubblewrapTestLockCleanup(t, host, true)
		})
	}
}

func TestEnsureBubblewrapRevalidatesAptBeforeInstall(t *testing.T) {
	host := newBubblewrapTestHost()
	host.binaryPresent = false
	host.lstatResults[aptGetPath] = []bubblewrapTestStatResult{
		{stat: host.aptLstat},
		{stat: bubblewrapFileStat{mode: 0o775, uid: 0, nlink: 1, dev: 8, ino: 202}},
	}
	err := ensureBubblewrapWith(context.Background(), host.deps())
	if err == nil || !strings.Contains(err.Error(), "Install the bubblewrap package at /usr/bin/bwrap manually") {
		t.Fatalf("apt replacement error = %v, want manual guidance", err)
	}
	want := []bubblewrapTestCommand{{path: aptGetPath, args: []string{"update"}}}
	if !reflect.DeepEqual(host.commands, want) {
		t.Fatalf("commands after apt replacement = %#v, want update only and no install/probe", host.commands)
	}
	assertBubblewrapTestLockCleanup(t, host, true)
}

func TestEnsureBubblewrapRejectsUntrustedBinaryWithoutAptOrProbe(t *testing.T) {
	trusted := newBubblewrapTestHost().binaryLstat
	for _, test := range []struct {
		name   string
		lstat  bubblewrapFileStat
		opened bubblewrapFileStat
	}{
		{name: "non-regular", lstat: bubblewrapFileStat{mode: os.ModeDir | 0o755, uid: 0, nlink: 1}},
		{name: "non-root-owned", lstat: bubblewrapFileStat{mode: 0o755, uid: 1000, nlink: 1}},
		{name: "group-writable", lstat: bubblewrapFileStat{mode: 0o775, uid: 0, nlink: 1}},
		{name: "world-writable", lstat: bubblewrapFileStat{mode: 0o757, uid: 0, nlink: 1}},
		{name: "setuid", lstat: bubblewrapFileStat{mode: os.ModeSetuid | 0o755, uid: 0, nlink: 1}},
		{name: "setgid", lstat: bubblewrapFileStat{mode: os.ModeSetgid | 0o755, uid: 0, nlink: 1}},
		{name: "multi-link", lstat: bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 2}},
		{name: "symlink", lstat: bubblewrapFileStat{mode: os.ModeSymlink | 0o777, uid: 0, nlink: 1}},
		{name: "replaced-after-lstat", lstat: trusted, opened: bubblewrapFileStat{mode: 0o755, uid: 0, nlink: 1, dev: trusted.dev, ino: trusted.ino + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.binaryLstat = test.lstat
			if test.opened.mode != 0 || test.opened.nlink != 0 {
				host.binaryOpened = test.opened
			} else {
				host.binaryOpened = test.lstat
			}
			if err := ensureBubblewrapWith(context.Background(), host.deps()); err == nil {
				t.Fatal("ensureBubblewrapWith accepted untrusted /usr/bin/bwrap")
			}
			if len(host.commands) != 0 {
				t.Fatalf("commands after untrusted binary = %#v, want none", host.commands)
			}
			assertBubblewrapTestLockCleanup(t, host, true)
		})
	}
}

func TestEnsureBubblewrapMissingBinaryReturnsExactManualGuidance(t *testing.T) {
	const probe = "/usr/bin/bwrap --unshare-user --unshare-pid --unshare-ipc --unshare-uts --disable-userns --uid 65534 --gid 65534 --hostname yeet-bwrap-probe --new-session --die-with-parent --ro-bind /usr /usr --ro-bind /bin /bin --ro-bind /lib64 /lib64 --proc /proc --dev /dev --tmpfs /tmp --tmpfs /run -- /usr/bin/true"
	for _, test := range []struct {
		name      string
		osRelease string
		apt       bool
		want      string
	}{
		{
			name:      "unsupported OS",
			osRelease: "ID=fedora\n",
			apt:       true,
			want:      "required Bubblewrap binary is missing at /usr/bin/bwrap; automatic installation is supported only on Debian and Ubuntu. Install the bubblewrap package at /usr/bin/bwrap manually, then verify host user-namespace and AppArmor policy with: " + probe,
		},
		{
			name:      "missing apt-get",
			osRelease: "ID=ubuntu\nID_LIKE=debian\n",
			apt:       false,
			want:      "required Bubblewrap binary is missing at /usr/bin/bwrap and /usr/bin/apt-get is unavailable. Install the bubblewrap package at /usr/bin/bwrap manually, then verify host user-namespace and AppArmor policy with: " + probe,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.binaryPresent = false
			host.aptPresent = test.apt
			host.osRelease = test.osRelease
			err := ensureBubblewrapWith(context.Background(), host.deps())
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
			if len(host.commands) != 0 {
				t.Fatalf("manual-install case ran commands: %#v", host.commands)
			}
			assertBubblewrapTestLockCleanup(t, host, true)
		})
	}
}

func TestEnsureBubblewrapPreservesAptAndProbeDiagnosticsWithoutPolicyMutation(t *testing.T) {
	t.Run("apt failure", func(t *testing.T) {
		host := newBubblewrapTestHost()
		host.binaryPresent = false
		host.aptUpdateOutput = "repository metadata unavailable"
		host.aptUpdateErr = errors.New("exit status 100")
		err := ensureBubblewrapWith(context.Background(), host.deps())
		if err == nil || !strings.Contains(err.Error(), "repository metadata unavailable") || !strings.Contains(err.Error(), "exit status 100") {
			t.Fatalf("apt error = %v, want raw output and exit diagnostic", err)
		}
		assertOnlyBubblewrapDependencyCommands(t, host.commands)
		assertBubblewrapTestLockCleanup(t, host, true)
	})

	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "unknown option", raw: "bwrap: Unknown option --disable-userns", want: "compatible Bubblewrap version"},
		{name: "user namespace", raw: "bwrap: No permissions to create new namespace: Operation not permitted", want: "user-namespace policy"},
		{name: "AppArmor", raw: "apparmor denied user namespace creation", want: "AppArmor policy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.probeOutput = test.raw
			host.probeErr = errors.New("exit status 1")
			err := ensureBubblewrapWith(context.Background(), host.deps())
			if err == nil || !strings.Contains(err.Error(), test.raw) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("probe error = %v, want raw diagnostic and %q guidance", err, test.want)
			}
			if len(host.commands) != 1 || host.commands[0].path != bubblewrapPath {
				t.Fatalf("probe failure commands = %#v, want only trusted Bubblewrap probe", host.commands)
			}
			assertOnlyBubblewrapDependencyCommands(t, host.commands)
			assertBubblewrapTestLockCleanup(t, host, true)
		})
	}
}

func TestEnsureBubblewrapSerializesAndRechecksAfterLock(t *testing.T) {
	host := newBubblewrapTestHost()
	deps := host.deps()
	var lockMu sync.Mutex
	locked := false
	secondBlocked := make(chan struct{})
	var blockedOnce sync.Once
	deps.flock = func(_ int, operation int) error {
		lockMu.Lock()
		defer lockMu.Unlock()
		if operation&unix.LOCK_UN != 0 {
			locked = false
			return nil
		}
		if locked {
			blockedOnce.Do(func() { close(secondBlocked) })
			return unix.EWOULDBLOCK
		}
		locked = true
		return nil
	}
	firstProbeStarted := make(chan struct{})
	releaseFirstProbe := make(chan struct{})
	var probeMu sync.Mutex
	probeCalls := 0
	originalRunAs := deps.runAs
	deps.runAs = func(ctx context.Context, command serviceSandboxCommand) ([]byte, error) {
		if command.Path == bubblewrapPath {
			probeMu.Lock()
			probeCalls++
			call := probeCalls
			probeMu.Unlock()
			if call == 1 {
				close(firstProbeStarted)
				select {
				case <-releaseFirstProbe:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		return originalRunAs(ctx, command)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- ensureBubblewrapWith(context.Background(), deps) }()
	select {
	case <-firstProbeStarted:
	case <-time.After(time.Second):
		t.Fatal("first ensure did not reach probe")
	}
	go func() { errCh <- ensureBubblewrapWith(context.Background(), deps) }()
	select {
	case <-secondBlocked:
	case <-time.After(time.Second):
		t.Fatal("second ensure did not contend on the host-global lock")
	}
	host.mu.Lock()
	lstatCallsWhileLocked := host.binaryLstatCalls
	host.mu.Unlock()
	if lstatCallsWhileLocked != 1 {
		t.Fatalf("binary inspections before first lock release = %d, want only first holder", lstatCallsWhileLocked)
	}
	close(releaseFirstProbe)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("serialized ensure: %v", err)
		}
	}
	lockMu.Lock()
	stillLocked := locked
	lockMu.Unlock()
	if stillLocked {
		t.Fatal("serialized ensure left the host-global lock held")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.binaryLstatCalls != 2 {
		t.Fatalf("serialized binary inspections = %d, want one per lock holder", host.binaryLstatCalls)
	}
	if len(host.lockFiles) != 2 {
		t.Fatalf("serialized lock files = %d, want one per contender", len(host.lockFiles))
	}
	for index, file := range host.lockFiles {
		if file.closeCalls != 1 {
			t.Fatalf("serialized lock file %d close calls = %d, want 1", index, file.closeCalls)
		}
	}
}

func TestEnsureBubblewrapCanceledLockAcquisitionDoesNotRunApt(t *testing.T) {
	host := newBubblewrapTestHost()
	host.binaryPresent = false
	deps := host.deps()
	flockAttempted := make(chan struct{})
	var attemptedOnce sync.Once
	deps.flock = func(_ int, operation int) error {
		if operation&unix.LOCK_UN != 0 {
			return nil
		}
		attemptedOnce.Do(func() { close(flockAttempted) })
		return unix.EWOULDBLOCK
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- ensureBubblewrapWith(ctx, deps) }()
	select {
	case <-flockAttempted:
	case <-time.After(time.Second):
		t.Fatal("ensure did not attempt the contended lock")
	}
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureBubblewrapWith error = %v, want context.Canceled", err)
	}
	if len(host.commands) != 0 {
		t.Fatalf("canceled ensure commands = %#v, want none", host.commands)
	}
	assertBubblewrapTestLockCleanup(t, host, false)
}

func TestEnsureBubblewrapRechecksCancellationAfterFlock(t *testing.T) {
	host := newBubblewrapTestHost()
	host.binaryPresent = false
	ctx, cancel := context.WithCancel(context.Background())
	deps := host.deps()
	originalFlock := deps.flock
	deps.flock = func(fd int, operation int) error {
		err := originalFlock(fd, operation)
		if err == nil && operation == unix.LOCK_EX|unix.LOCK_NB {
			cancel()
		}
		return err
	}

	err := ensureBubblewrapWith(ctx, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureBubblewrapWith error = %v, want context.Canceled immediately after flock", err)
	}
	if len(host.commands) != 0 {
		t.Fatalf("post-flock cancellation commands = %#v, want none", host.commands)
	}
	assertBubblewrapTestLockCleanup(t, host, true)
}

func TestEnsureBubblewrapRevalidatesOpenedLockAfterFlock(t *testing.T) {
	for _, test := range []struct {
		name      string
		postFlock bubblewrapTestStatResult
		want      string
	}{
		{
			name:      "unsafe metadata",
			postFlock: bubblewrapTestStatResult{stat: bubblewrapFileStat{mode: 0o600, uid: 0, nlink: 2, dev: 8, ino: 50}},
			want:      "has 2 links, want one",
		},
		{
			name:      "metadata inspection failure",
			postFlock: bubblewrapTestStatResult{err: errors.New("fstat failed")},
			want:      "fstat failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.lockStatResults = []bubblewrapTestStatResult{{stat: host.lockStat}, test.postFlock}
			err := ensureBubblewrapWith(context.Background(), host.deps())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("post-flock lock revalidation error = %v, want %q", err, test.want)
			}
			if len(host.commands) != 0 {
				t.Fatalf("post-flock lock revalidation commands = %#v, want none", host.commands)
			}
			assertBubblewrapTestLockCleanup(t, host, true)
		})
	}
}

func TestEnsureBubblewrapRejectsCanonicalLockReplacementAfterFlock(t *testing.T) {
	host := newBubblewrapTestHost()
	host.lstatResults[bubblewrapLockPath] = []bubblewrapTestStatResult{{
		stat: bubblewrapFileStat{mode: 0o600, uid: 0, nlink: 1, dev: 8, ino: 51},
	}}

	err := ensureBubblewrapWith(context.Background(), host.deps())
	if err == nil || !strings.Contains(err.Error(), "no longer names the acquired lock") {
		t.Fatalf("canonical lock replacement error = %v, want path/descriptor divergence rejection", err)
	}
	if len(host.commands) != 0 {
		t.Fatalf("canonical lock replacement commands = %#v, want no apt or probe", host.commands)
	}
	assertBubblewrapTestLockCleanup(t, host, true)
}

func TestEnsureBubblewrapRejectsUnsafeExistingLock(t *testing.T) {
	for _, test := range []struct {
		name string
		stat bubblewrapFileStat
	}{
		{name: "non-regular", stat: bubblewrapFileStat{mode: os.ModeDir | 0o600, uid: 0, nlink: 1}},
		{name: "non-root-owned", stat: bubblewrapFileStat{mode: 0o600, uid: 1000, nlink: 1}},
		{name: "multi-link", stat: bubblewrapFileStat{mode: 0o600, uid: 0, nlink: 2}},
		{name: "permissive mode", stat: bubblewrapFileStat{mode: 0o644, uid: 0, nlink: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			host.lockStat = test.stat
			err := ensureBubblewrapWith(context.Background(), host.deps())
			if err == nil {
				t.Fatal("ensureBubblewrapWith accepted unsafe host-global lock")
			}
			if len(host.flockCalls) != 0 || len(host.commands) != 0 {
				t.Fatalf("unsafe lock flock/commands = %#v/%#v, want none", host.flockCalls, host.commands)
			}
			assertBubblewrapTestLockCleanup(t, host, false)
		})
	}
}

func TestEnsureBubblewrapClosesLockOnAcquisitionFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*bubblewrapTestHost, *bubblewrapDependencyDeps)
	}{
		{
			name: "created lock chmod",
			mutate: func(host *bubblewrapTestHost, _ *bubblewrapDependencyDeps) {
				host.lockExists = false
				host.lockChmodErr = errors.New("chmod denied")
			},
		},
		{
			name: "opened lock stat",
			mutate: func(host *bubblewrapTestHost, _ *bubblewrapDependencyDeps) {
				host.lockStatResults = []bubblewrapTestStatResult{{err: errors.New("fstat failed")}}
			},
		},
		{
			name: "fatal flock",
			mutate: func(_ *bubblewrapTestHost, deps *bubblewrapDependencyDeps) {
				deps.flock = func(_ int, _ int) error { return unix.EBADF }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapTestHost()
			deps := host.deps()
			test.mutate(host, &deps)
			if err := ensureBubblewrapWith(context.Background(), deps); err == nil {
				t.Fatal("ensureBubblewrapWith succeeded, want acquisition failure")
			}
			if len(host.commands) != 0 {
				t.Fatalf("acquisition failure commands = %#v, want none", host.commands)
			}
			assertBubblewrapTestLockCleanup(t, host, false)
		})
	}
}

func containsBubblewrapArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func assertOnlyBubblewrapDependencyCommands(t *testing.T, commands []bubblewrapTestCommand) {
	t.Helper()
	for _, command := range commands {
		if command.path != bubblewrapPath && command.path != aptGetPath {
			t.Fatalf("dependency lifecycle ran policy-mutating or shell command: %#v", command)
		}
	}
}

func bubblewrapTestOpenCallsForPath(host *bubblewrapTestHost, path string) []bubblewrapTestOpenCall {
	host.mu.Lock()
	defer host.mu.Unlock()
	var calls []bubblewrapTestOpenCall
	for _, call := range host.openCalls {
		if call.path == path {
			calls = append(calls, call)
		}
	}
	return calls
}

func assertBubblewrapTestLockCleanup(t *testing.T, host *bubblewrapTestHost, wantUnlock bool) {
	t.Helper()
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.lockFiles) != 1 {
		t.Fatalf("opened lock files = %d, want 1", len(host.lockFiles))
	}
	if host.lockFiles[0].closeCalls != 1 {
		t.Fatalf("lock close calls = %d, want 1", host.lockFiles[0].closeCalls)
	}
	unlocks := 0
	for _, operation := range host.flockCalls {
		if operation&unix.LOCK_UN != 0 {
			unlocks++
		}
	}
	wantUnlocks := 0
	if wantUnlock {
		wantUnlocks = 1
	}
	if unlocks != wantUnlocks {
		t.Fatalf("lock unlock calls = %d, want %d; all flock calls %#v", unlocks, wantUnlocks, host.flockCalls)
	}
}
