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

func TestHandleSvcCmdUsesConfigHost(t *testing.T) {
	oldExec := execRemoteFn
	oldExecOutput := execRemoteOutputFn
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	defer func() {
		execRemoteFn = oldExec
		execRemoteOutputFn = oldExecOutput
		serviceOverride = oldService
		loadedPrefs = oldPrefs
		resetHostOverride()
	}()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "compose.yml",
	})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}

	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "catch"

	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called for status table rendering")
		return nil
	}
	called := false
	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		called = true
		if host != "host-a" {
			t.Fatalf("host = %q, want host-a", host)
		}
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		return []byte(`[{"serviceName":"svc-a","serviceType":"service","components":[{"name":"svc-a","status":"running"}]}]`), nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	os.Stdout = w
	runErr := HandleSvcCmd([]string{"status"})
	_ = w.Close()
	os.Stdout = oldStdout

	if _, readErr := io.ReadAll(r); readErr != nil {
		t.Fatalf("ReadAll error: %v", readErr)
	}
	if runErr != nil {
		t.Fatalf("HandleSvcCmd error: %v", err)
	}
	if !called {
		t.Fatalf("expected execRemoteOutputFn to be called")
	}
	if loadedPrefs.DefaultHost != "host-a" {
		t.Fatalf("host = %q, want host-a", loadedPrefs.DefaultHost)
	}
}

func TestRunFromProjectConfigRehydratesArgs(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldPush := pushAllLocalImagesFn
	oldService := serviceOverride
	oldIsTerminal := isTerminalFn
	oldHashes := fetchRemoteArtifactHashesFn
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		pushAllLocalImagesFn = oldPush
		serviceOverride = oldService
		isTerminalFn = oldIsTerminal
		fetchRemoteArtifactHashesFn = oldHashes
	}()

	serviceOverride = "rssbot"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	pushAllLocalImagesFn = func(string, string, string) error {
		return nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "rssbot", "rssbot")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:    "rssbot",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: filepath.Join("rssbot", "rssbot"),
		Args:    []string{"-allowed-chats=314073886,135155078"},
	})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if service != "rssbot" {
			t.Fatalf("service = %q, want rssbot", service)
		}
		if stdin == nil {
			t.Fatalf("expected stdin payload to be provided")
		}
		if tty {
			t.Fatalf("expected tty=false, got true")
		}
		return nil
	}

	if err := runFromProjectConfig(loc, "host-a"); err != nil {
		t.Fatalf("runFromProjectConfig error: %v", err)
	}

	wantArgs := []string{"run", "--", "-allowed-chats=314073886,135155078"}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, gotArgs[i], wantArgs[i])
		}
	}
}

func TestHandleSvcCmdRunForceUsesStoredPayload(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	oldHashes := fetchRemoteArtifactHashesFn
	oldIsTerminal := isTerminalFn
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		fetchRemoteArtifactHashesFn = oldHashes
		isTerminalFn = oldIsTerminal
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hashFileSHA256 error: %v", err)
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
		}, true, nil
	}

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "run.sh",
	})
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}

	var calls [][]string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls = append(calls, append([]string{}, args...))
		return nil
	}

	if err := HandleSvcCmd([]string{"run", "--force"}); err != nil {
		t.Fatalf("HandleSvcCmd error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one remote call, got %d", len(calls))
	}
	if len(calls[0]) == 0 || calls[0][0] != "run" {
		t.Fatalf("expected run call, got %v", calls[0])
	}
}
