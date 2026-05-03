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
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type closeErrorReader struct {
	reader io.Reader
	err    error
}

func (r *closeErrorReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *closeErrorReader) Close() error {
	return r.err
}

func TestDetectRunChangesSummaries(t *testing.T) {
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "main.py")
	if err := os.WriteFile(payload, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	envHash, err := hashFileSHA256(envFile)
	if err != nil {
		t.Fatalf("hash env: %v", err)
	}

	artifact := func(kind, sha string) *catchrpc.ArtifactHash {
		return &catchrpc.ArtifactHash{Kind: kind, SHA256: sha}
	}

	tests := []struct {
		name      string
		runArgs   []string
		envFile   string
		stored    []string
		response  catchrpc.ArtifactHashesResponse
		supported bool
		want      runChangeSummary
	}{
		{
			name:      "args changed only",
			runArgs:   []string{"--pull"},
			stored:    nil,
			response:  catchrpc.ArtifactHashesResponse{Found: true, Payload: artifact("python", payloadHash)},
			supported: true,
			want: runChangeSummary{
				argsChanged:  true,
				payloadLabel: "python file",
			},
		},
		{
			name:      "matching hashes have no changes",
			envFile:   envFile,
			stored:    []string{},
			response:  catchrpc.ArtifactHashesResponse{Found: true, Payload: artifact("python", payloadHash), Env: artifact("env file", envHash)},
			supported: true,
			want: runChangeSummary{
				payloadLabel: "python file",
			},
		},
		{
			name:      "unsupported remote marks hash-backed artifacts changed",
			envFile:   envFile,
			stored:    []string{},
			supported: false,
			want: runChangeSummary{
				payloadChanged: true,
				envChanged:     true,
				payloadLabel:   "python file",
			},
		},
		{
			name:      "remote kind labels changed payload",
			stored:    []string{},
			response:  catchrpc.ArtifactHashesResponse{Found: true, Payload: artifact("binary", "deadbeef")},
			supported: true,
			want: runChangeSummary{
				payloadChanged: true,
				payloadLabel:   "binary",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
				if service != "svc-a" {
					t.Fatalf("service = %q, want svc-a", service)
				}
				return tt.response, tt.supported, nil
			}

			got, err := detectRunChanges(payload, tt.runArgs, tt.envFile, tt.stored)
			if err != nil {
				t.Fatalf("detectRunChanges error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("summary = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPayloadLabelFromLocal(t *testing.T) {
	oldArch := remoteCatchOSAndArchFn
	defer func() {
		remoteCatchOSAndArchFn = oldArch
	}()

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
		return path
	}

	script := write("run", "#!/bin/sh\necho ok\n")
	compose := write("compose.yml", "services:\n  app:\n    image: busybox\n")
	typescript := write("main.ts", "export const x: number = 1;\n")
	python := write("main.py", "print('ok')\n")
	unknown := write("readme.txt", "hello\n")

	tests := []struct {
		name       string
		payload    string
		remoteKind string
		want       string
	}{
		{name: "remote kind wins", payload: unknown, remoteKind: "docker-compose", want: "docker compose file"},
		{name: "script", payload: script, want: "script"},
		{name: "compose", payload: compose, want: "docker compose file"},
		{name: "typescript", payload: typescript, want: "typescript file"},
		{name: "python", payload: python, want: "python file"},
		{name: "unknown", payload: unknown, want: "payload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := payloadLabelFromLocal(tt.payload, tt.remoteKind)
			if got != tt.want {
				t.Fatalf("payloadLabelFromLocal() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHashReadCloserSHA256ReturnsCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	_, err := hashReadCloserSHA256(&closeErrorReader{
		reader: strings.NewReader("payload"),
		err:    closeErr,
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("hashReadCloserSHA256 error = %v, want %v", err, closeErr)
	}
}

func TestRunWithChangesToReturnsStatusWriteError(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "script",
				SHA256: payloadHash,
			},
		}, true, nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called")
		return nil
	}

	writeErr := errors.New("stdout failed")
	err = runWithChangesTo(errorWriter{err: writeErr}, payload, nil, "", ServiceEntry{}, false)
	if !errors.Is(err, writeErr) {
		t.Fatalf("runWithChangesTo error = %v, want %v", err, writeErr)
	}
}

func TestRunWithChangesNoChangesSkips(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	envHash, err := hashFileSHA256(envFile)
	if err != nil {
		t.Fatalf("hash env: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
			Env: &catchrpc.ArtifactHash{
				Kind:   "env file",
				SHA256: envHash,
			},
		}, true, nil
	}

	calls := 0
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls++
		return nil
	}

	if err := runWithChanges(payload, nil, envFile, ServiceEntry{}, false); err != nil {
		t.Fatalf("runWithChanges error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no remote calls, got %d", calls)
	}
}

func TestRunWithChangesEnvOnly(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
			Env: &catchrpc.ArtifactHash{
				Kind:   "env file",
				SHA256: "deadbeef",
			},
		}, true, nil
	}

	var calls [][]string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls = append(calls, append([]string{}, args...))
		return nil
	}

	if err := runWithChanges(payload, nil, envFile, ServiceEntry{}, false); err != nil {
		t.Fatalf("runWithChanges error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one remote call, got %d", len(calls))
	}
	if len(calls[0]) < 2 || calls[0][0] != "env" || calls[0][1] != "copy" {
		t.Fatalf("expected env copy call, got %v", calls[0])
	}
}

func TestRunWithChangesNoChangesForceDeploys(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	envHash, err := hashFileSHA256(envFile)
	if err != nil {
		t.Fatalf("hash env: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
			Env: &catchrpc.ArtifactHash{
				Kind:   "env file",
				SHA256: envHash,
			},
		}, true, nil
	}

	var calls [][]string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls = append(calls, append([]string{}, args...))
		return nil
	}

	entry := ServiceEntry{Args: []string{"--pull"}}
	if err := runWithChanges(payload, []string{"--pull"}, envFile, entry, true); err != nil {
		t.Fatalf("runWithChanges error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one remote call, got %d", len(calls))
	}
	if len(calls[0]) < 2 || calls[0][0] != "run" || calls[0][1] != "--pull" {
		t.Fatalf("expected run call with --pull, got %v", calls[0])
	}
}

func TestSaveEnvFileConfigStoresRelativePath(t *testing.T) {
	oldService := serviceOverride
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		serviceOverride = oldService
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	envPath := filepath.Join(tmp, "prod.env")
	if err := os.WriteFile(envPath, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: &ProjectConfig{Version: projectConfigVersion}}
	if err := saveEnvFileConfig(loc, "host-a", envPath); err != nil {
		t.Fatalf("saveEnvFileConfig error: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if entry.EnvFile != "prod.env" {
		t.Fatalf("env file = %q, want %q", entry.EnvFile, "prod.env")
	}
}

func TestEnsureLockedRunFlagsRejectsChanges(t *testing.T) {
	entry := ServiceEntry{
		Name: "svc-a",
		Host: "host-a",
		Args: []string{"--net=ts", "--ts-tags=tag:a"},
	}
	if err := ensureLockedRunFlags(entry, []string{"--net=ts", "--ts-tags=tag:a"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := ensureLockedRunFlags(entry, []string{"--net=lan"}); err == nil {
		t.Fatalf("expected error for --net change")
	}
	if err := ensureLockedRunFlags(entry, []string{"--net=ts", "--ts-tags=tag:b"}); err == nil {
		t.Fatalf("expected error for --ts-tags change")
	}
}

func TestExtractForceFlag(t *testing.T) {
	force, args, err := extractForceFlag([]string{"--pull", "--force", "--", "--force"})
	if err != nil {
		t.Fatalf("extractForceFlag error: %v", err)
	}
	if !force {
		t.Fatalf("expected force to be true")
	}
	want := []string{"--pull", "--", "--force"}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d (%v)", len(args), len(want), args)
	}
	for i := range args {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestExtractForceFlagInvalidValue(t *testing.T) {
	if _, _, err := extractForceFlag([]string{"--force=not-a-bool"}); err == nil {
		t.Fatalf("expected invalid --force value error")
	}
}
