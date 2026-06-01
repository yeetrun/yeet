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
			pushAllLocalImagesFn = func(ctx context.Context, service, goos, goarch string) error {
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

func TestRunRunContextWithOutputUsesWriterAwarePayloadSeams(t *testing.T) {
	oldDockerfile := tryRunDockerfileWithOutputFn
	oldFile := tryRunFileWithOutputFn
	oldRemoteImage := tryRunRemoteImageWithOutputFn
	oldDocker := tryRunDockerWithOutputFn
	defer func() {
		tryRunDockerfileWithOutputFn = oldDockerfile
		tryRunFileWithOutputFn = oldFile
		tryRunRemoteImageWithOutputFn = oldRemoteImage
		tryRunDockerWithOutputFn = oldDocker
	}()

	tryRunDockerfileWithOutputFn = func(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
		return false, nil
	}
	tryRunFileWithOutputFn = func(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
		return false, nil
	}
	tryRunRemoteImageWithOutputFn = func(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
		if payload != "registry.example/app:latest" {
			t.Fatalf("payload = %q, want registry.example/app:latest", payload)
		}
		_, _ = io.WriteString(stdout, "remote image output\n")
		return true, nil
	}
	tryRunDockerWithOutputFn = func(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
		t.Fatalf("docker fallback should not run")
		return false, nil
	}

	var out strings.Builder
	if err := runRunContextWithOutput(context.Background(), &out, "registry.example/app:latest", nil); err != nil {
		t.Fatalf("runRunContextWithOutput: %v", err)
	}
	if got := out.String(); got != "remote image output\n" {
		t.Fatalf("output = %q, want remote image output", got)
	}
}

func TestTryRunVMPayloadExecsRemoteRunWithPayloadArgument(t *testing.T) {
	oldExec := execRemoteDirectFn
	defer func() { execRemoteDirectFn = oldExec }()

	var gotSvc string
	var gotArgs []string
	execRemoteDirectFn = func(ctx context.Context, svc string, args []string, stdin io.Reader, tty bool) error {
		gotSvc = svc
		gotArgs = append([]string{}, args...)
		if stdin != nil {
			t.Fatalf("stdin = %#v, want nil", stdin)
		}
		if !tty {
			t.Fatal("tty = false, want true")
		}
		return nil
	}

	oldService := serviceOverride
	serviceOverride = "devbox"
	defer func() { serviceOverride = oldService }()

	ok, err := tryRunVMPayloadContext(context.Background(), "vm://ubuntu/26.04", []string{"--net=svc", "--cpus=4"})
	if err != nil {
		t.Fatalf("tryRunVMPayloadContext: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if gotSvc != "devbox" {
		t.Fatalf("service = %q", gotSvc)
	}
	wantArgs := []string{"run", "--net=svc", "--cpus=4", "vm://ubuntu/26.04"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestTryRunVMPayloadRejectsPayloadArgs(t *testing.T) {
	ok, err := tryRunVMPayloadContext(context.Background(), "vm://ubuntu/26.04", []string{"--net=svc", "--", "--inside"})
	if !ok {
		t.Fatal("ok = false, want true for VM payload")
	}
	if err == nil || !strings.Contains(err.Error(), "VM payloads do not accept payload args") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunLocalImagePayloadContextWithOutputUsesWriterAwareDockerSeam(t *testing.T) {
	oldDocker := tryRunDockerWithOutputFn
	defer func() {
		tryRunDockerWithOutputFn = oldDocker
	}()

	tryRunDockerWithOutputFn = func(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
		if payload != "local/app:latest" {
			t.Fatalf("payload = %q, want local/app:latest", payload)
		}
		_, _ = io.WriteString(stdout, "local image output\n")
		return true, nil
	}

	var out strings.Builder
	if err := runLocalImagePayloadContextWithOutput(context.Background(), &out, "local/app:latest", []string{"--net=svc"}); err != nil {
		t.Fatalf("runLocalImagePayloadContextWithOutput: %v", err)
	}
	if got := out.String(); got != "local image output\n" {
		t.Fatalf("output = %q, want local image output", got)
	}
}

func TestStageDockerArgsWithOutputWritesFailureToProvidedWriter(t *testing.T) {
	oldExec := execRemoteToFn
	defer func() {
		execRemoteToFn = oldExec
	}()

	execRemoteToFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		return errors.New("stage failed")
	}

	var out strings.Builder
	err := stageDockerArgsWithOutput(context.Background(), &out, "svc-a", []string{"--net=svc"})
	if err == nil || !strings.Contains(err.Error(), "failed to stage args") {
		t.Fatalf("error = %v, want failed to stage args", err)
	}
	if got := out.String(); !strings.Contains(got, "failed to stage args: stage failed") {
		t.Fatalf("output = %q, want stage failure", got)
	}
}

func TestStageDockerArgsWithOutputReturnsWriterFailure(t *testing.T) {
	oldExec := execRemoteToFn
	defer func() {
		execRemoteToFn = oldExec
	}()

	execRemoteToFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		return errors.New("stage failed")
	}

	writeErr := errors.New("write failed")
	err := stageDockerArgsWithOutput(context.Background(), errorWriter{err: writeErr}, "svc-a", []string{"--net=svc"})
	if !errors.Is(err, writeErr) {
		t.Fatalf("error = %v, want writer error", err)
	}
	if !strings.Contains(err.Error(), "failed to stage args") {
		t.Fatalf("error = %v, want stage failure context", err)
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
