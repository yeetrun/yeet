// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/svc"
)

func TestRunTailscaleResolverExecVerifiesBeforeExec(t *testing.T) {
	source := "/etc/netns/yeet-api-ns/resolv.conf"
	executable := "/usr/local/lib/yeet/tailscaled"
	wantArgs := []string{executable, "--state=/run/tailscaled.state", "--socket=/run/tailscaled.sock"}
	wantEnv := []string{"HOME=/root", "TS_STATE_DIR=/run/yeet"}
	var calls []string

	err := runTailscaleResolverExec([]string{"--source", source, "--", executable, wantArgs[1], wantArgs[2]}, tailscaleResolverExecDeps{
		stat: func(path string) (os.FileInfo, error) {
			if path != executable {
				t.Fatalf("stat path = %q, want %q", path, executable)
			}
			return resolverExecFileInfo{name: "tailscaled", mode: 0o755}, nil
		},
		mountOverlay: func(gotSource, gotExecutable string) error {
			calls = append(calls, "mount")
			if gotSource != source || gotExecutable != executable {
				t.Fatalf("mount overlay paths = %q, %q, want %q, %q", gotSource, gotExecutable, source, executable)
			}
			return nil
		},
		verify: func(probe svc.ResolverMountProbe) error {
			calls = append(calls, "verify")
			want := svc.ResolverMountProbe{
				SourcePath:    source,
				TargetPath:    "/etc/resolv.conf",
				MountInfoPath: "/proc/self/mountinfo",
				MountPoint:    "/etc/resolv.conf",
			}
			if probe != want {
				t.Fatalf("probe = %#v, want %#v", probe, want)
			}
			return nil
		},
		exec: func(path string, args []string, env []string) error {
			calls = append(calls, "exec")
			if path != executable {
				t.Fatalf("exec path = %q, want %q", path, executable)
			}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Fatalf("exec args = %#v, want %#v", args, wantArgs)
			}
			if !reflect.DeepEqual(env, wantEnv) {
				t.Fatalf("exec environment = %#v, want %#v", env, wantEnv)
			}
			return nil
		},
		environ: func() []string { return wantEnv },
	})
	if err != nil {
		t.Fatalf("runTailscaleResolverExec: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"mount", "verify", "exec"}) {
		t.Fatalf("calls = %#v, want mount and verify before exec", calls)
	}
}

func TestRunTailscaleResolverExecStopsWhenOverlayMountFails(t *testing.T) {
	mountErr := errors.New("overlay unavailable")
	err := runTailscaleResolverExec([]string{
		"--source", "/etc/netns/yeet-api-ns/resolv.conf",
		"--", "/usr/local/lib/yeet/tailscaled",
	}, tailscaleResolverExecDeps{
		stat: func(string) (os.FileInfo, error) {
			return resolverExecFileInfo{name: "tailscaled", mode: 0o755}, nil
		},
		mountOverlay: func(string, string) error { return mountErr },
		verify: func(svc.ResolverMountProbe) error {
			t.Fatal("verify called after resolver overlay mount failure")
			return nil
		},
		exec: func(string, []string, []string) error {
			t.Fatal("exec called after resolver overlay mount failure")
			return nil
		},
		environ: func() []string { return nil },
	})
	if !errors.Is(err, mountErr) {
		t.Fatalf("runTailscaleResolverExec error = %v, want wrapped %v", err, mountErr)
	}
}

func TestRunTailscaleResolverExecRejectsInvalidArguments(t *testing.T) {
	const source = "/etc/netns/yeet-api-ns/resolv.conf"
	const executable = "/usr/local/lib/yeet/tailscaled"

	tests := []struct {
		name string
		args []string
		info os.FileInfo
	}{
		{name: "missing source", args: []string{"--", executable}},
		{name: "missing separator", args: []string{"--source", source, executable}},
		{name: "extra launcher flag", args: []string{"--source", source, "--verbose", "--", executable}},
		{name: "relative source", args: []string{"--source", "etc/netns/yeet-api-ns/resolv.conf", "--", executable}},
		{name: "relative executable", args: []string{"--source", source, "--", "tailscaled"}},
		{name: "source traversal", args: []string{"--source", "/etc/netns/yeet-api-ns/../yeet-api-ns/resolv.conf", "--", executable}},
		{name: "source nul", args: []string{"--source", source + "\x00", "--", executable}},
		{name: "executable traversal", args: []string{"--source", source, "--", "/usr/local/lib/yeet/../yeet/tailscaled"}},
		{name: "executable nul", args: []string{"--source", source, "--", executable + "\x00"}},
		{name: "wrong namespace", args: []string{"--source", "/etc/netns/app-ns/resolv.conf", "--", executable}},
		{name: "wrong source name", args: []string{"--source", "/etc/netns/yeet-api-ns/hosts", "--", executable}},
		{name: "wrong daemon basename", args: []string{"--source", source, "--", "/usr/local/lib/yeet/daemon"}},
		{name: "non-regular daemon", args: []string{"--source", source, "--", executable}, info: resolverExecFileInfo{name: "tailscaled", mode: os.ModeDir | 0o755}},
		{name: "non-executable daemon", args: []string{"--source", source, "--", executable}, info: resolverExecFileInfo{name: "tailscaled", mode: 0o644}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := tt.info
			if info == nil {
				info = resolverExecFileInfo{name: "tailscaled", mode: 0o755}
			}
			err := runTailscaleResolverExec(tt.args, tailscaleResolverExecDeps{
				stat: func(string) (os.FileInfo, error) { return info, nil },
				verify: func(svc.ResolverMountProbe) error {
					t.Fatal("verify called for invalid resolver launcher arguments")
					return nil
				},
				exec: func(string, []string, []string) error {
					t.Fatal("exec called for invalid resolver launcher arguments")
					return nil
				},
				environ: func() []string { return nil },
			})
			if err == nil {
				t.Fatal("runTailscaleResolverExec succeeded for invalid arguments")
			}
		})
	}
}

func TestRunTailscaleResolverExecReturnsExecError(t *testing.T) {
	wantErr := errors.New("exec failed")
	err := runTailscaleResolverExec([]string{
		"--source", "/etc/netns/yeet-api-ns/resolv.conf",
		"--", "/usr/local/lib/yeet/tailscaled",
	}, tailscaleResolverExecDeps{
		stat:         func(string) (os.FileInfo, error) { return resolverExecFileInfo{name: "tailscaled", mode: 0o755}, nil },
		mountOverlay: func(string, string) error { return nil },
		verify:       func(svc.ResolverMountProbe) error { return nil },
		exec:         func(string, []string, []string) error { return wantErr },
		environ:      func() []string { return nil },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runTailscaleResolverExec error = %v, want wrapped %v", err, wantErr)
	}
}

func TestTailscaleResolverExecIsLocalOnly(t *testing.T) {
	if _, registered := ttyCommandHandlers["tailscale-resolver-exec"]; registered {
		t.Fatal("local resolver launcher must not be a TTY command")
	}
}

type resolverExecFileInfo struct {
	name string
	mode os.FileMode
}

func (f resolverExecFileInfo) Name() string      { return f.name }
func (resolverExecFileInfo) Size() int64         { return 0 }
func (f resolverExecFileInfo) Mode() os.FileMode { return f.mode }
func (resolverExecFileInfo) ModTime() time.Time  { return time.Time{} }
func (f resolverExecFileInfo) IsDir() bool       { return f.mode.IsDir() }
func (resolverExecFileInfo) Sys() any            { return nil }
