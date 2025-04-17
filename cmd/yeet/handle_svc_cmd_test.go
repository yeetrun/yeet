// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/codecutil"
)

func TestHandleSvcCmdDefaultsToStatus(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	if err := handleSvcCmd([]string{}); err != nil {
		t.Fatalf("handleSvcCmd returned error: %v", err)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "status" {
		t.Fatalf("expected default args [status], got %#v", gotArgs)
	}
}

func TestHandleSvcCmdCronSplitsQuotedExpression(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "owesplit")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write temp binary: %v", err)
	}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	if err := handleSvcCmd([]string{"cron", bin, "0 9 15 * *", "--", "-live"}); err != nil {
		t.Fatalf("handleSvcCmd returned error: %v", err)
	}

	want := []string{"cron", "0", "9", "15", "*", "*", "-live"}
	if len(gotArgs) != len(want) {
		t.Fatalf("expected args %v, got %v", want, gotArgs)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, gotArgs)
		}
	}
}

func TestRunUsesRunCommandWithStdin(t *testing.T) {
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
	pushAllLocalImagesFn = func(string, string, string) error {
		return nil
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "run.sh")
	contents := []byte("#!/bin/sh\necho ok\n")
	if err := os.WriteFile(bin, contents, 0o700); err != nil {
		t.Fatalf("failed to write temp binary: %v", err)
	}

	var gotService string
	var gotArgs []string
	var gotPayload []byte
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		payload, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatalf("failed to read stdin payload: %v", err)
		}
		gotPayload = payload
		if tty {
			t.Fatalf("expected tty=false, got true")
		}
		return nil
	}

	if err := runRun(bin, []string{"--net=svc,ts", "--ts-tags=tag:app"}); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}

	if gotService != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", gotService)
	}
	wantArgs := []string{"run", "--net=svc,ts", "--ts-tags=tag:app"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	if !bytes.Equal(gotPayload, contents) {
		t.Fatalf("expected raw script payload, got %q", string(gotPayload))
	}
	if len(gotPayload) >= 4 && bytes.Equal(gotPayload[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatalf("unexpected zstd payload for script")
	}
}

func TestRunComposeTTYDependsOnTerminal(t *testing.T) {
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
	pushAllLocalImagesFn = func(string, string, string) error {
		return nil
	}

	tmp := t.TempDir()
	compose := filepath.Join(tmp, "compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  app:\n    image: alpine\n"), 0o600); err != nil {
		t.Fatalf("failed to write compose: %v", err)
	}

	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotTTY = tty
		return nil
	}

	isTerminalFn = func(int) bool { return false }
	if err := runRun(compose, nil); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}
	if gotTTY {
		t.Fatalf("expected tty=false when not a terminal")
	}

	isTerminalFn = func(int) bool { return true }
	if err := runRun(compose, nil); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}
	if !gotTTY {
		t.Fatalf("expected tty=true when terminal and compose")
	}
}

func TestRunBinaryUploadsZstd(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	bin := buildTestELF(t, tmp)
	expectedPayload, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("failed to read test binary: %v", err)
	}

	var gotPayload []byte
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		payload, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatalf("failed to read stdin payload: %v", err)
		}
		gotPayload = payload
		return nil
	}

	if err := runRun(bin, nil); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}
	if len(gotPayload) < 4 || !bytes.Equal(gotPayload[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatalf("expected zstd payload, got %x", gotPayload[:4])
	}

	compPath := filepath.Join(tmp, "payload.zst")
	if err := os.WriteFile(compPath, gotPayload, 0o600); err != nil {
		t.Fatalf("failed to write compressed payload: %v", err)
	}
	outPath := filepath.Join(tmp, "payload.bin")
	if err := codecutil.ZstdDecompress(compPath, outPath); err != nil {
		t.Fatalf("failed to decompress payload: %v", err)
	}
	decoded, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read decompressed payload: %v", err)
	}
	if !bytes.Equal(decoded, expectedPayload) {
		t.Fatalf("decompressed payload mismatch")
	}
}

func TestRunRemoteImageUsesComposePayload(t *testing.T) {
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
	pushAllLocalImagesFn = func(string, string, string) error {
		return nil
	}
	isTerminalFn = func(int) bool { return false }

	var gotArgs []string
	var gotPayload []byte
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		payload, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatalf("failed to read stdin payload: %v", err)
		}
		gotPayload = payload
		if tty {
			t.Fatalf("expected tty=false, got true")
		}
		return nil
	}

	image := "ghcr.io/org/app:latest"
	if err := runRun(image, []string{"--net=svc"}); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}

	wantArgs := []string{"run", "--net=svc"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	payloadStr := string(gotPayload)
	if !strings.Contains(payloadStr, "services:\n  svc-a:") {
		t.Fatalf("expected compose service name in payload, got %q", payloadStr)
	}
	if !strings.Contains(payloadStr, "image: "+image) {
		t.Fatalf("expected image %q in payload, got %q", image, payloadStr)
	}
	if !strings.Contains(payloadStr, "volumes:\n      - \"./:/data\"") {
		t.Fatalf("expected data volume mapping in payload, got %q", payloadStr)
	}
}

func TestStageBinaryUploadsZstd(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	bin := buildTestELF(t, tmp)
	expectedPayload, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("failed to read test binary: %v", err)
	}

	var gotPayload []byte
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		payload, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatalf("failed to read stdin payload: %v", err)
		}
		gotPayload = payload
		return nil
	}

	if err := runStageBinary(bin); err != nil {
		t.Fatalf("runStageBinary returned error: %v", err)
	}
	if len(gotPayload) < 4 || !bytes.Equal(gotPayload[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatalf("expected zstd payload, got %x", gotPayload[:4])
	}

	compPath := filepath.Join(tmp, "payload.zst")
	if err := os.WriteFile(compPath, gotPayload, 0o600); err != nil {
		t.Fatalf("failed to write compressed payload: %v", err)
	}
	outPath := filepath.Join(tmp, "payload.bin")
	if err := codecutil.ZstdDecompress(compPath, outPath); err != nil {
		t.Fatalf("failed to decompress payload: %v", err)
	}
	decoded, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read decompressed payload: %v", err)
	}
	if !bytes.Equal(decoded, expectedPayload) {
		t.Fatalf("decompressed payload mismatch")
	}
}

func TestCronBinaryUploadsZstd(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	bin := buildTestELF(t, tmp)
	expectedPayload, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("failed to read test binary: %v", err)
	}

	var gotPayload []byte
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		payload, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatalf("failed to read stdin payload: %v", err)
		}
		gotPayload = payload
		return nil
	}

	if err := runCron(bin, []string{"0", "9", "*", "*", "*"}); err != nil {
		t.Fatalf("runCron returned error: %v", err)
	}
	if len(gotPayload) < 4 || !bytes.Equal(gotPayload[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatalf("expected zstd payload, got %x", gotPayload[:4])
	}

	compPath := filepath.Join(tmp, "payload.zst")
	if err := os.WriteFile(compPath, gotPayload, 0o600); err != nil {
		t.Fatalf("failed to write compressed payload: %v", err)
	}
	outPath := filepath.Join(tmp, "payload.bin")
	if err := codecutil.ZstdDecompress(compPath, outPath); err != nil {
		t.Fatalf("failed to decompress payload: %v", err)
	}
	decoded, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read decompressed payload: %v", err)
	}
	if !bytes.Equal(decoded, expectedPayload) {
		t.Fatalf("decompressed payload mismatch")
	}
}

func buildTestELF(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not found in PATH")
	}
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}
	initCmd := exec.Command("go", "mod", "init", "example.com/yeet-test")
	initCmd.Dir = dir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod init failed: %v (%s)", err, bytes.TrimSpace(out))
	}
	binPath := filepath.Join(dir, "app")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = dir
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v (%s)", err, bytes.TrimSpace(out))
	}
	return binPath
}

func TestRunCopyEnvUsesCopyCommand(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	tmp := t.TempDir()
	src := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(src, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp env file: %v", err)
	}

	var gotService string
	var gotArgs []string
	var gotStdin io.Reader
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		gotStdin = stdin
		if tty {
			t.Fatalf("expected tty=false, got true")
		}
		return nil
	}

	if err := runCopy(src, "env"); err != nil {
		t.Fatalf("runCopy returned error: %v", err)
	}

	if gotService != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", gotService)
	}
	wantArgs := []string{"copy", "env"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	if gotStdin == nil {
		t.Fatalf("expected stdin to be provided")
	}
}

func TestRunCopyDataDirAppendsBaseName(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	tmp := t.TempDir()
	src := filepath.Join(tmp, "payload.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	if err := runCopy(src, "./data/"); err != nil {
		t.Fatalf("runCopy returned error: %v", err)
	}

	wantArgs := []string{"copy", "data/payload.txt"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
}
