// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

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
