// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunFilePayloadComposeBranches(t *testing.T) {
	tests := []struct {
		name            string
		pushLocalImages bool
		args            []string
		pushErr         error
		wantErr         string
		wantPushLocal   bool
		wantExec        bool
		wantExecArgs    []string
	}{
		{
			name:            "local compose rejects publish",
			pushLocalImages: true,
			args:            []string{"-p", "8080:80"},
			wantErr:         "-p/--publish is not supported for docker compose payloads",
		},
		{
			name:         "remote image compose allows publish",
			args:         []string{"-p", "8080:80"},
			wantExec:     true,
			wantExecArgs: []string{"run", "-p", "8080:80"},
		},
		{
			name:            "local compose pushes images",
			pushLocalImages: true,
			wantPushLocal:   true,
			wantExec:        true,
			wantExecArgs:    []string{"run"},
		},
		{
			name:            "push error is wrapped",
			pushLocalImages: true,
			pushErr:         errors.New("registry unavailable"),
			wantErr:         "failed to push all local images",
			wantPushLocal:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldExec := execRemoteFn
			oldArch := remoteCatchOSAndArchFn
			oldPush := pushAllLocalImagesFn
			oldService := serviceOverride
			oldIsTerminal := isTerminalFn
			defer func() {
				execRemoteFn = oldExec
				remoteCatchOSAndArchFn = oldArch
				pushAllLocalImagesFn = oldPush
				serviceOverride = oldService
				isTerminalFn = oldIsTerminal
			}()

			serviceOverride = "svc-a"
			remoteCatchOSAndArchFn = func() (string, string, error) {
				return "linux", "amd64", nil
			}
			isTerminalFn = func(int) bool { return false }

			tmp := t.TempDir()
			compose := filepath.Join(tmp, "compose.yml")
			if err := os.WriteFile(compose, []byte("services:\n  app:\n    image: alpine\n"), 0o600); err != nil {
				t.Fatalf("write compose: %v", err)
			}

			pushCalled := false
			pushAllLocalImagesFn = func(service, goos, goarch string) error {
				pushCalled = true
				if service != "svc-a" || goos != "linux" || goarch != "amd64" {
					t.Fatalf("pushAllLocalImagesFn = (%q, %q, %q), want (svc-a, linux, amd64)", service, goos, goarch)
				}
				return tt.pushErr
			}

			var gotExecArgs []string
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				if service != "svc-a" {
					t.Fatalf("exec service = %q, want svc-a", service)
				}
				if stdin == nil {
					t.Fatalf("expected stdin payload")
				}
				if tty {
					t.Fatalf("expected tty=false")
				}
				gotExecArgs = append([]string{}, args...)
				return nil
			}

			ok, err := runFilePayload(compose, tt.args, tt.pushLocalImages)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				if ok {
					t.Fatalf("ok = true, want false")
				}
			} else if !ok {
				t.Fatalf("ok = false, want true")
			}
			if pushCalled != tt.wantPushLocal {
				t.Fatalf("pushCalled = %v, want %v", pushCalled, tt.wantPushLocal)
			}
			if (gotExecArgs != nil) != tt.wantExec {
				t.Fatalf("exec called = %v, want %v", gotExecArgs != nil, tt.wantExec)
			}
			if tt.wantExec && !reflect.DeepEqual(gotExecArgs, tt.wantExecArgs) {
				t.Fatalf("exec args = %#v, want %#v", gotExecArgs, tt.wantExecArgs)
			}
		})
	}
}

func TestRunFilePayloadErrorPaths(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	oldIsTerminal := isTerminalFn
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		isTerminalFn = oldIsTerminal
	}()

	serviceOverride = "svc-a"
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	script := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "", "", errors.New("arch unavailable")
	}
	if ok, err := runFilePayload(script, nil, true); ok || err == nil || !strings.Contains(err.Error(), "arch unavailable") {
		t.Fatalf("runFilePayload arch error = (%v, %v), want ok=false and arch error", ok, err)
	}

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return errors.New("remote refused")
	}
	if ok, err := runFilePayload(script, nil, true); ok || err == nil || !strings.Contains(err.Error(), "failed to run service") {
		t.Fatalf("runFilePayload exec error = (%v, %v), want ok=false and wrapped exec error", ok, err)
	}
}
