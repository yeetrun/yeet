// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/yeetrun/yeet/pkg/fileutil"
)

type bubblewrapAppArmorTestCommand struct {
	path       string
	args       []string
	credential *syscall.Credential
}

type bubblewrapAppArmorTestHost struct {
	t             *testing.T
	root          string
	paths         bubblewrapAppArmorPaths
	commands      []bubblewrapAppArmorTestCommand
	probeCalls    int
	probeErr      error
	evidenceErr   error
	afterDryParse func()
	mu            sync.Mutex
}

func newBubblewrapAppArmorTestHost(t *testing.T) *bubblewrapAppArmorTestHost {
	t.Helper()
	root := t.TempDir()
	paths := bubblewrapAppArmorPaths{
		trustRoot:        root,
		osRelease:        filepath.Join(root, "etc", "os-release"),
		restrictedUserNS: filepath.Join(root, "proc", "sys", "kernel", "apparmor_restrict_unprivileged_userns"),
		apparmorEnabled:  filepath.Join(root, "sys", "module", "apparmor", "parameters", "enabled"),
		parser:           filepath.Join(root, "usr", "sbin", "apparmor_parser"),
		profileDir:       filepath.Join(root, "etc", "apparmor.d"),
		profile:          filepath.Join(root, "etc", "apparmor.d", "yeet-bwrap"),
		profiles:         filepath.Join(root, "sys", "kernel", "security", "apparmor", "profiles"),
	}
	for _, dir := range []string{
		filepath.Dir(paths.osRelease), filepath.Dir(paths.restrictedUserNS), filepath.Dir(paths.apparmorEnabled),
		filepath.Dir(paths.parser), paths.profileDir, filepath.Dir(paths.profiles),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create AppArmor test directory %s: %v", dir, err)
		}
	}
	for path, content := range map[string]string{
		paths.osRelease:        "ID=ubuntu\n",
		paths.restrictedUserNS: "1\n",
		paths.apparmorEnabled:  "Y\n",
		paths.parser:           "test parser\n",
		paths.profiles:         "",
	} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write AppArmor test file %s: %v", path, err)
		}
	}
	if err := os.Chmod(paths.parser, 0o755); err != nil {
		t.Fatalf("chmod AppArmor test parser: %v", err)
	}
	return &bubblewrapAppArmorTestHost{t: t, root: root, paths: paths}
}

func (h *bubblewrapAppArmorTestHost) deps() bubblewrapAppArmorDeps {
	return bubblewrapAppArmorDeps{
		paths:      h.paths,
		trustedUID: uint32(os.Geteuid()),
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
		run: func(_ context.Context, path string, args []string) ([]byte, error) {
			h.recordCommand(bubblewrapAppArmorTestCommand{path: path, args: append([]string(nil), args...)})
			if path != h.paths.parser {
				return nil, fmt.Errorf("unexpected root command %s", path)
			}
			switch {
			case len(args) != 0 && args[0] == "-r":
				return nil, os.WriteFile(h.paths.profiles, []byte("yeet-bwrap (enforce)\nyeet-unpriv-bwrap (enforce)\n"), 0o644)
			case len(args) != 0 && args[0] == "-R":
				return nil, os.WriteFile(h.paths.profiles, nil, 0o644)
			default:
				if h.afterDryParse != nil {
					h.afterDryParse()
				}
				return nil, nil
			}
		},
		probe: func(context.Context) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.probeCalls++
			return h.probeErr
		},
		runEvidence: func(_ context.Context, command serviceSandboxCommand) ([]byte, error) {
			h.recordCommand(bubblewrapAppArmorTestCommand{
				path: command.Path, args: append([]string(nil), command.Arguments...), credential: command.Credential,
			})
			if h.evidenceErr != nil {
				return []byte("evidence failure"), h.evidenceErr
			}
			args := command.Arguments
			if len(args) == 0 {
				return nil, errors.New("missing evidence command")
			}
			switch args[len(args)-1] {
			case "/proc/self/attr/current":
				return []byte("yeet-bwrap//&yeet-unpriv-bwrap (enforce)\n"), nil
			case "/proc/self/status":
				return []byte("CapEff:\t0000000000000000\n"), nil
			case "/usr/bin/true":
				return []byte("unshare: Operation not permitted\n"), errors.New("nested user namespace denied")
			default:
				return nil, fmt.Errorf("unexpected evidence arguments %#v", args)
			}
		},
		pathPresent: func(path string) bool {
			return path == "/bin" || path == "/lib64"
		},
	}
}

func (h *bubblewrapAppArmorTestHost) recordCommand(command bubblewrapAppArmorTestCommand) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands = append(h.commands, command)
}

func TestEnsureRestrictedBubblewrapAppArmorHostRouting(t *testing.T) {
	initial := errors.New("uid map denied")
	for _, test := range []struct {
		name       string
		osRelease  string
		restricted string
		wantRepair bool
	}{
		{name: "Debian returns original probe error", osRelease: "ID=debian\n", restricted: "1\n"},
		{name: "unrestricted Ubuntu returns original probe error", osRelease: "ID=ubuntu\n", restricted: "0\n"},
		{name: "restricted Ubuntu enters managed profile", osRelease: "ID=ubuntu\n", restricted: "1\n", wantRepair: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newBubblewrapAppArmorTestHost(t)
			if err := os.WriteFile(host.paths.osRelease, []byte(test.osRelease), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(host.paths.restrictedUserNS, []byte(test.restricted), 0o644); err != nil {
				t.Fatal(err)
			}
			err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), initial, host.deps())
			if !test.wantRepair {
				if !errors.Is(err, initial) {
					t.Fatalf("error = %v, want original %v", err, initial)
				}
				if len(host.commands) != 0 || host.probeCalls != 0 {
					t.Fatalf("incompatible host commands/probes = %#v/%d, want none", host.commands, host.probeCalls)
				}
				if _, statErr := os.Lstat(host.paths.profile); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("incompatible host profile stat = %v, want absent", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("restricted Ubuntu repair: %v", err)
			}
		})
	}
}

func TestEnsureRestrictedBubblewrapAppArmorPublishesLoadsAndVerifiesExactProfile(t *testing.T) {
	host := newBubblewrapAppArmorTestHost(t)
	if err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), errors.New("uid map denied"), host.deps()); err != nil {
		t.Fatalf("ensureRestrictedBubblewrapAppArmorWith: %v", err)
	}
	raw, err := os.ReadFile(host.paths.profile)
	if err != nil {
		t.Fatalf("read managed profile: %v", err)
	}
	if string(raw) != bubblewrapAppArmorProfileV1 {
		t.Fatalf("managed profile differs from policy version 1:\n%s", raw)
	}
	if host.probeCalls != 1 {
		t.Fatalf("functional re-probes = %d, want 1", host.probeCalls)
	}
	if len(host.commands) != 5 {
		t.Fatalf("commands = %#v, want dry parse, load, and three security probes", host.commands)
	}
	if !reflect.DeepEqual(host.commands[0].args[:3], []string{"-Q", "-K", "--abort-on-error"}) {
		t.Fatalf("dry parse args = %#v", host.commands[0].args)
	}
	if !reflect.DeepEqual(host.commands[1].args, []string{"-r", "-K", host.paths.profile}) {
		t.Fatalf("load args = %#v", host.commands[1].args)
	}
	for _, command := range host.commands[2:] {
		if command.credential == nil || command.credential.Uid != 65534 || command.credential.Gid != 65534 {
			t.Fatalf("security probe credential = %#v, want 65534:65534", command.credential)
		}
	}
}

func TestEnsureRestrictedBubblewrapAppArmorAcceptsExactCurrentProfile(t *testing.T) {
	host := newBubblewrapAppArmorTestHost(t)
	if err := os.WriteFile(host.paths.profile, []byte(bubblewrapAppArmorProfileV1), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(host.paths.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), errors.New("uid map denied"), host.deps()); err != nil {
		t.Fatalf("ensure exact current profile: %v", err)
	}
	after, err := os.ReadFile(host.paths.profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("exact current profile changed:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestEnsureRestrictedBubblewrapAppArmorPreservesDivergentProfile(t *testing.T) {
	host := newBubblewrapAppArmorTestHost(t)
	const operatorPolicy = "# operator policy\n"
	if err := os.WriteFile(host.paths.profile, []byte(operatorPolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), errors.New("uid map denied"), host.deps())
	if err == nil || !strings.Contains(err.Error(), "divergent") {
		t.Fatalf("error = %v, want divergent managed-path error", err)
	}
	raw, readErr := os.ReadFile(host.paths.profile)
	if readErr != nil || string(raw) != operatorPolicy {
		t.Fatalf("operator policy = %q, %v; want preserved", raw, readErr)
	}
	if len(host.commands) != 0 || host.probeCalls != 0 {
		t.Fatalf("divergent policy commands/probes = %#v/%d, want none", host.commands, host.probeCalls)
	}
}

func TestEnsureRestrictedBubblewrapAppArmorRejectsLoadedProfilesWithoutManagedFile(t *testing.T) {
	host := newBubblewrapAppArmorTestHost(t)
	loaded := []byte("yeet-bwrap (enforce)\nyeet-unpriv-bwrap (enforce)\n")
	if err := os.WriteFile(host.paths.profiles, loaded, 0o644); err != nil {
		t.Fatal(err)
	}
	err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), errors.New("uid map denied"), host.deps())
	if err == nil || !strings.Contains(err.Error(), "loaded without the exact managed profile file") {
		t.Fatalf("error = %v, want ambiguous loaded-profile error", err)
	}
	if _, statErr := os.Lstat(host.paths.profile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("managed profile stat = %v, want absent", statErr)
	}
	after, readErr := os.ReadFile(host.paths.profiles)
	if readErr != nil || !reflect.DeepEqual(after, loaded) {
		t.Fatalf("loaded profile inventory = %q, %v; want preserved %q", after, readErr, loaded)
	}
	if len(host.commands) != 0 || host.probeCalls != 0 {
		t.Fatalf("ambiguous loaded profile commands/probes = %#v/%d, want none", host.commands, host.probeCalls)
	}
}

func TestEnsureRestrictedBubblewrapAppArmorRollsBackNewProfileWhenPostLoadProbeFails(t *testing.T) {
	host := newBubblewrapAppArmorTestHost(t)
	host.probeErr = errors.New("post-load probe denied")
	err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), errors.New("uid map denied"), host.deps())
	if !errors.Is(err, host.probeErr) {
		t.Fatalf("error = %v, want post-load probe error %v", err, host.probeErr)
	}
	if _, statErr := os.Lstat(host.paths.profile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("managed profile stat after rollback = %v, want absent", statErr)
	}
	profiles, readErr := os.ReadFile(host.paths.profiles)
	if readErr != nil || len(profiles) != 0 {
		t.Fatalf("loaded profiles after rollback = %q, %v; want none", profiles, readErr)
	}
	entries, readErr := os.ReadDir(host.paths.profileDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("profile directory after rollback = %#v, %v; want empty", entries, readErr)
	}
}

func TestEnsureRestrictedBubblewrapAppArmorRemovesPublishedProfileWhenDirectorySyncFails(t *testing.T) {
	host := newBubblewrapAppArmorTestHost(t)
	syncErr := errors.New("sync profile directory")
	deps := host.deps()
	deps.syncDir = func(string) error { return syncErr }
	err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), errors.New("uid map denied"), deps)
	if !errors.Is(err, syncErr) {
		t.Fatalf("error = %v, want directory sync error %v", err, syncErr)
	}
	if _, statErr := os.Lstat(host.paths.profile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("managed profile stat after publication failure = %v, want absent", statErr)
	}
	entries, readErr := os.ReadDir(host.paths.profileDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("profile directory after publication failure = %#v, %v; want empty", entries, readErr)
	}
}

func TestEnsureRestrictedBubblewrapAppArmorRemovesPublishedProfileWhenParserChangesBeforeLoad(t *testing.T) {
	host := newBubblewrapAppArmorTestHost(t)
	host.afterDryParse = func() {
		if err := os.Chmod(host.paths.parser, 0o777); err != nil {
			t.Fatalf("make parser unsafe after dry parse: %v", err)
		}
		host.afterDryParse = nil
	}
	err := ensureRestrictedBubblewrapAppArmorWith(context.Background(), errors.New("uid map denied"), host.deps())
	if err == nil || !strings.Contains(err.Error(), "group/other write") {
		t.Fatalf("error = %v, want parser replacement validation error", err)
	}
	if _, statErr := os.Lstat(host.paths.profile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("managed profile stat after parser change = %v, want absent", statErr)
	}
	profiles, readErr := os.ReadFile(host.paths.profiles)
	if readErr != nil || len(profiles) != 0 {
		t.Fatalf("loaded profiles after parser change = %q, %v; want none", profiles, readErr)
	}
}
