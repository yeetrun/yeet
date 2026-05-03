// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
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

func TestExtractEnvFileFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantEnv   string
		wantArgs  []string
		wantFound bool
		wantErr   string
	}{
		{name: "empty", wantArgs: nil},
		{name: "space value", args: []string{"run", "--env-file", ".env", "--flag"}, wantEnv: ".env", wantArgs: []string{"run", "--flag"}, wantFound: true},
		{name: "equals value", args: []string{"--env-file=.prod", "run"}, wantEnv: ".prod", wantArgs: []string{"run"}, wantFound: true},
		{name: "delimiter stops parsing", args: []string{"--env-file", ".env", "--", "--env-file", "remote"}, wantEnv: ".env", wantArgs: []string{"--", "--env-file", "remote"}, wantFound: true},
		{name: "missing value", args: []string{"--env-file"}, wantErr: "requires a value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEnv, gotArgs, gotFound, err := extractEnvFileFlag(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("extractEnvFileFlag error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractEnvFileFlag error: %v", err)
			}
			if gotEnv != tt.wantEnv || gotFound != tt.wantFound || strings.Join(gotArgs, ",") != strings.Join(tt.wantArgs, ",") {
				t.Fatalf("extractEnvFileFlag = env %q args %#v found %v, want env %q args %#v found %v", gotEnv, gotArgs, gotFound, tt.wantEnv, tt.wantArgs, tt.wantFound)
			}
		})
	}
}

func TestServiceEntryForConfigAndHasServiceConfig(t *testing.T) {
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	defer func() {
		serviceOverride = oldService
		loadedPrefs = oldPrefs
	}()
	loadedPrefs.DefaultHost = "host-a"
	cfg := &ProjectConfig{}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Payload: "run.sh"})
	loc := &projectConfigLocation{Config: cfg}

	serviceOverride = ""
	if hasServiceConfig(loc, "") {
		t.Fatal("hasServiceConfig without service override = true, want false")
	}

	serviceOverride = "svc-a"
	entry, ok := serviceEntryForConfig(loc, "")
	if !ok || entry.Payload != "run.sh" {
		t.Fatalf("serviceEntryForConfig = %#v %v, want saved entry", entry, ok)
	}
	if !hasServiceConfig(loc, "") {
		t.Fatal("hasServiceConfig saved entry = false, want true")
	}
	if hasServiceConfig(loc, "host-b") {
		t.Fatal("hasServiceConfig wrong host = true, want false")
	}
	if hasServiceConfig(nil, "") {
		t.Fatal("hasServiceConfig nil config = true, want false")
	}
}

func TestSaveEnvFileConfigSkipsEmptyInputs(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	serviceOverride = ""
	if err := saveEnvFileConfig(nil, "", ".env"); err != nil {
		t.Fatalf("saveEnvFileConfig empty service error: %v", err)
	}
	serviceOverride = "svc-a"
	if err := saveEnvFileConfig(nil, "", " "); err != nil {
		t.Fatalf("saveEnvFileConfig empty env error: %v", err)
	}
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

func TestWriteRunDeployStatus(t *testing.T) {
	tests := []struct {
		name    string
		summary runChangeSummary
		want    string
	}{
		{name: "payload label", summary: runChangeSummary{payloadChanged: true, payloadLabel: "python file"}, want: "Updated python file\n"},
		{name: "args only", summary: runChangeSummary{argsChanged: true}, want: "Updated run config\n"},
		{name: "no deploy status", summary: runChangeSummary{envChanged: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeRunDeployStatus(&buf, tt.summary); err != nil {
				t.Fatalf("writeRunDeployStatus error: %v", err)
			}
			if buf.String() != tt.want {
				t.Fatalf("output = %q, want %q", buf.String(), tt.want)
			}
		})
	}
}

func TestRunChangeRemoteHashHelpers(t *testing.T) {
	if got, kind := remotePayloadHash(catchrpc.ArtifactHashesResponse{}); got != "" || kind != "" {
		t.Fatalf("remotePayloadHash missing = %q %q, want empty", got, kind)
	}
	resp := catchrpc.ArtifactHashesResponse{
		Found:   true,
		Payload: &catchrpc.ArtifactHash{Kind: "python", SHA256: "payload-sha"},
		Env:     &catchrpc.ArtifactHash{SHA256: "env-sha"},
	}
	if got, kind := remotePayloadHash(resp); got != "payload-sha" || kind != "python" {
		t.Fatalf("remotePayloadHash = %q %q, want payload-sha python", got, kind)
	}
	if got := remoteEnvHash(catchrpc.ArtifactHashesResponse{}); got != "" {
		t.Fatalf("remoteEnvHash missing = %q, want empty", got)
	}
	if got := remoteEnvHash(resp); got != "env-sha" {
		t.Fatalf("remoteEnvHash = %q, want env-sha", got)
	}
}

func TestShouldAlwaysDeployPayload(t *testing.T) {
	tests := []struct {
		payload string
		want    bool
	}{
		{payload: "ghcr.io/example/app:latest", want: true},
		{payload: "/tmp/Dockerfile", want: true},
		{payload: "/tmp/run.sh"},
	}

	for _, tt := range tests {
		t.Run(tt.payload, func(t *testing.T) {
			if got := shouldAlwaysDeployPayload(tt.payload); got != tt.want {
				t.Fatalf("shouldAlwaysDeployPayload(%q) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}

func TestIsRPCMethodNotFound(t *testing.T) {
	if isRPCMethodNotFound(nil) {
		t.Fatal("isRPCMethodNotFound nil = true, want false")
	}
	if !isRPCMethodNotFound(errors.New("rpc error: method not found")) {
		t.Fatal("isRPCMethodNotFound method-not-found = false, want true")
	}
	if isRPCMethodNotFound(errors.New("connection refused")) {
		t.Fatal("isRPCMethodNotFound other error = true, want false")
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
