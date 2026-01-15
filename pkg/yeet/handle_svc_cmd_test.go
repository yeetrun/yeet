// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/copyutil"
)

func TestHandleSvcCmdDefaultsToStatus(t *testing.T) {
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

	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"

	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called for status table rendering")
		return nil
	}

	var gotArgs []string
	var gotHost string
	var gotService string
	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		gotHost = host
		gotService = service
		gotArgs = append([]string{}, args...)
		return []byte(`[{"serviceName":"svc-a","serviceType":"service","components":[{"name":"svc-a","status":"running"}]}]`), nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	os.Stdout = w
	runErr := HandleSvcCmd([]string{})
	_ = w.Close()
	os.Stdout = oldStdout

	if _, readErr := io.ReadAll(r); readErr != nil {
		t.Fatalf("ReadAll error: %v", readErr)
	}

	if runErr != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", runErr)
	}
	if gotHost != "host-a" {
		t.Fatalf("host = %q, want host-a", gotHost)
	}
	if gotService != "svc-a" {
		t.Fatalf("service = %q, want svc-a", gotService)
	}
	if strings.Join(gotArgs, " ") != "status --format=json" {
		t.Fatalf("expected args [status --format=json], got %#v", gotArgs)
	}
}

func TestHandleStatusSingleHostIncludesHostColumn(t *testing.T) {
	oldExec := execRemoteFn
	oldFetch := fetchStatusForHostFn
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	defer func() {
		execRemoteFn = oldExec
		fetchStatusForHostFn = oldFetch
		serviceOverride = oldService
		loadedPrefs = oldPrefs
		resetHostOverride()
	}()

	serviceOverride = ""
	loadedPrefs.DefaultHost = "host-a"
	SetHostOverride("host-a")

	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called for single-host table status")
		return nil
	}
	fetchStatusForHostFn = func(ctx context.Context, host string, flags cli.StatusFlags) ([]statusService, error) {
		if host != "host-a" {
			t.Fatalf("host = %q, want host-a", host)
		}
		return []statusService{
			{
				ServiceName: "svc-a",
				ServiceType: "docker",
				Components:  []statusComponent{{Name: "c1", Status: "running"}},
			},
		}, nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	os.Stdout = w
	runErr := handleStatusCommand(context.Background(), []string{}, nil, true)
	_ = w.Close()
	os.Stdout = oldStdout

	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("ReadAll error: %v", readErr)
	}
	if runErr != nil {
		t.Fatalf("handleStatusCommand error: %v", runErr)
	}

	output := string(out)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected output, got %q", output)
	}
	if !strings.Contains(lines[0], "HOST") {
		t.Fatalf("expected HOST column, got %q", lines[0])
	}
	if !strings.Contains(output, "host-a") {
		t.Fatalf("expected host value in output, got %q", output)
	}
}

func TestHandleStatusServiceOverrideIncludesHostColumn(t *testing.T) {
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

	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"

	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called for service status table rendering")
		return nil
	}
	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		if host != "host-a" {
			t.Fatalf("host = %q, want host-a", host)
		}
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		if strings.Join(args, " ") != "status --format=json" {
			t.Fatalf("unexpected args: %v", args)
		}
		return []byte(`[{"serviceName":"svc-a","serviceType":"docker","components":[{"name":"c1","status":"running"}]}]`), nil
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe error: %v", err)
	}
	os.Stdout = w
	runErr := handleStatusCommand(context.Background(), []string{}, nil, false)
	_ = w.Close()
	os.Stdout = oldStdout

	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("ReadAll error: %v", readErr)
	}
	if runErr != nil {
		t.Fatalf("handleStatusCommand error: %v", runErr)
	}

	output := string(out)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected output, got %q", output)
	}
	if !strings.Contains(lines[0], "HOST") {
		t.Fatalf("expected HOST column, got %q", lines[0])
	}
	if !strings.Contains(output, "host-a") {
		t.Fatalf("expected host value in output, got %q", output)
	}
	if !strings.Contains(output, "svc-a") {
		t.Fatalf("expected service value in output, got %q", output)
	}
}

func TestHandleSvcCmdLogsRequiresService(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		serviceOverride = oldService
	}()

	serviceOverride = ""
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called without a service name")
		return nil
	}

	err := HandleSvcCmd([]string{"logs"})
	if err == nil {
		t.Fatalf("expected missing service error")
	}
	if !strings.Contains(err.Error(), "logs requires a service name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleSvcCmdCronSplitsQuotedExpression(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldService := serviceOverride
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		serviceOverride = oldService
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	bin := filepath.Join(tmp, "owesplit")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write temp binary: %v", err)
	}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	if err := HandleSvcCmd([]string{"cron", bin, "0 9 15 * *", "--", "-live"}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
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
	isTerminalFn = func(int) bool { return false }

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

func TestRunBinaryTTYDependsOnTerminal(t *testing.T) {
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
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write temp binary: %v", err)
	}

	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotTTY = tty
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		if _, err := io.ReadAll(stdin); err != nil {
			t.Fatalf("failed to read stdin payload: %v", err)
		}
		return nil
	}

	isTerminalFn = func(int) bool { return false }
	if err := runRun(bin, nil); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}
	if gotTTY {
		t.Fatalf("expected tty=false when not a terminal")
	}

	isTerminalFn = func(int) bool { return true }
	if err := runRun(bin, nil); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}
	if !gotTTY {
		t.Fatalf("expected tty=true when terminal")
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

func TestCronTTYDependsOnTerminal(t *testing.T) {
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
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	bin := buildTestELF(t, tmp)

	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotTTY = tty
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		if _, err := io.ReadAll(stdin); err != nil {
			t.Fatalf("failed to read stdin payload: %v", err)
		}
		return nil
	}

	isTerminalFn = func(int) bool { return false }
	if err := runCron(bin, []string{"0", "9", "*", "*", "*"}, nil); err != nil {
		t.Fatalf("runCron returned error: %v", err)
	}
	if gotTTY {
		t.Fatalf("expected tty=false when not a terminal")
	}

	isTerminalFn = func(int) bool { return true }
	if err := runCron(bin, []string{"0", "9", "*", "*", "*"}, nil); err != nil {
		t.Fatalf("runCron returned error: %v", err)
	}
	if !gotTTY {
		t.Fatalf("expected tty=true when terminal")
	}
}

func TestHandleSvcCmdRunPullBeforePayload(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldPush := pushAllLocalImagesFn
	oldService := serviceOverride
	oldIsTerminal := isTerminalFn
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		pushAllLocalImagesFn = oldPush
		serviceOverride = oldService
		isTerminalFn = oldIsTerminal
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	pushAllLocalImagesFn = func(string, string, string) error {
		return nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	compose := filepath.Join(tmp, "compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  app:\n    image: alpine\n"), 0o600); err != nil {
		t.Fatalf("failed to write compose: %v", err)
	}

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
		return nil
	}

	if err := HandleSvcCmd([]string{"run", "--pull", compose}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	wantArgs := []string{"run", "--pull"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	if !bytes.Contains(gotPayload, []byte("services:")) {
		t.Fatalf("expected compose payload, got %q", string(gotPayload))
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

func TestRunRemoteImageWithPublish(t *testing.T) {
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
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		if tty {
			t.Fatalf("expected tty=false, got true")
		}
		return nil
	}

	image := "nginx:latest"
	if err := runRun(image, []string{"-p", "8000:80"}); err != nil {
		t.Fatalf("runRun returned error: %v", err)
	}

	wantArgs := []string{"run", "-p", "8000:80"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
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

	if err := runCron(bin, []string{"0", "9", "*", "*", "*"}, nil); err != nil {
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

func TestHandleSvcCmdCopyUploadFile(t *testing.T) {
	oldExec := execRemoteFn
	defer func() {
		execRemoteFn = oldExec
	}()

	tmp := t.TempDir()
	src := filepath.Join(tmp, "config.yml")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
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
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		gz, err := gzip.NewReader(stdin)
		if err != nil {
			t.Fatalf("failed to read gzip: %v", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("failed to read tar: %v", err)
		}
		if hdr.Name != "app.yml" {
			t.Fatalf("expected tar entry app.yml, got %q", hdr.Name)
		}
		return nil
	}

	if err := HandleSvcCmd([]string{"copy", src, "svc-a:configs/app.yml"}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	if gotService != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", gotService)
	}
	wantArgs := []string{"copy", "--to", "configs", "--archive", "--compress"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	if gotStdin == nil {
		t.Fatalf("expected stdin to be provided")
	}
}

func TestHandleSvcCmdCopyUploadDirRecursiveContents(t *testing.T) {
	oldExec := execRemoteFn
	defer func() {
		execRemoteFn = oldExec
	}()

	tmp := t.TempDir()
	src := filepath.Join(tmp, "configs")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "app.yml"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	var gotArgs []string
	var gotNames []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		gz, err := gzip.NewReader(stdin)
		if err != nil {
			t.Fatalf("failed to read gzip: %v", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("failed to read tar: %v", err)
			}
			if strings.HasSuffix(hdr.Name, "/") {
				continue
			}
			gotNames = append(gotNames, hdr.Name)
		}
		return nil
	}

	srcWithSlash := src + string(os.PathSeparator)
	if err := HandleSvcCmd([]string{"copy", "-r", srcWithSlash, "svc-a:configs/"}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	wantArgs := []string{"copy", "--to", "configs", "--archive", "--compress"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	if len(gotNames) != 1 || gotNames[0] != "app.yml" {
		t.Fatalf("expected archive to contain app.yml, got %v", gotNames)
	}
}

func TestHandleSvcCmdCopyUploadDirRecursiveKeepsDir(t *testing.T) {
	oldExec := execRemoteFn
	defer func() {
		execRemoteFn = oldExec
	}()

	tmp := t.TempDir()
	src := filepath.Join(tmp, "configs")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "app.yml"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	var gotNames []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		if stdin == nil {
			t.Fatalf("expected stdin to be provided")
		}
		gz, err := gzip.NewReader(stdin)
		if err != nil {
			t.Fatalf("failed to read gzip: %v", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("failed to read tar: %v", err)
			}
			if strings.HasSuffix(hdr.Name, "/") {
				continue
			}
			gotNames = append(gotNames, hdr.Name)
		}
		return nil
	}

	if err := HandleSvcCmd([]string{"copy", "-r", src, "svc-a:configs/"}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	if len(gotNames) != 1 || gotNames[0] != "configs/app.yml" {
		t.Fatalf("expected archive to contain configs/app.yml, got %v", gotNames)
	}
}

func TestHandleSvcCmdCopyDownloadFile(t *testing.T) {
	oldStream := execRemoteStreamFn
	defer func() {
		execRemoteStreamFn = oldStream
	}()

	tmp := t.TempDir()
	dest := filepath.Join(tmp, "out.txt")

	var gotService string
	var gotArgs []string
	execRemoteStreamFn = func(ctx context.Context, service string, args []string, stdin io.Reader) (io.ReadCloser, <-chan error, error) {
		gotService = service
		gotArgs = append([]string{}, args...)
		srcTmp := t.TempDir()
		src := filepath.Join(srcTmp, "remote.txt")
		if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
			return nil, nil, err
		}
		var tarBuf bytes.Buffer
		if err := copyutil.TarFile(&tarBuf, src, "remote.txt"); err != nil {
			return nil, nil, err
		}
		var gzBuf bytes.Buffer
		gz := gzip.NewWriter(&gzBuf)
		if _, err := gz.Write(tarBuf.Bytes()); err != nil {
			gz.Close()
			return nil, nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, nil, err
		}
		var buf bytes.Buffer
		if err := copyutil.WriteHeader(&buf, "file", "remote.txt"); err != nil {
			return nil, nil, err
		}
		buf.Write(gzBuf.Bytes())
		done := make(chan error, 1)
		done <- nil
		return io.NopCloser(bytes.NewReader(buf.Bytes())), done, nil
	}

	if err := HandleSvcCmd([]string{"copy", "svc-a:remote.txt", dest}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	if gotService != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", gotService)
	}
	wantArgs := []string{"copy", "--from", "remote.txt", "--archive", "--compress"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if string(out) != "payload" {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestHandleSvcCmdCopyDownloadDirRecursive(t *testing.T) {
	oldStream := execRemoteStreamFn
	defer func() {
		execRemoteStreamFn = oldStream
	}()

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "app.yml"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	var tarBuf bytes.Buffer
	if err := copyutil.TarDirectory(&tarBuf, srcDir, ""); err != nil {
		t.Fatalf("failed to build tar: %v", err)
	}

	tmp := t.TempDir()
	dest := filepath.Join(tmp, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	execRemoteStreamFn = func(ctx context.Context, service string, args []string, stdin io.Reader) (io.ReadCloser, <-chan error, error) {
		var buf bytes.Buffer
		if err := copyutil.WriteHeader(&buf, "dir", "configs"); err != nil {
			return nil, nil, err
		}
		var gzBuf bytes.Buffer
		gz := gzip.NewWriter(&gzBuf)
		if _, err := gz.Write(tarBuf.Bytes()); err != nil {
			gz.Close()
			return nil, nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, nil, err
		}
		buf.Write(gzBuf.Bytes())
		done := make(chan error, 1)
		done <- nil
		return io.NopCloser(bytes.NewReader(buf.Bytes())), done, nil
	}

	if err := HandleSvcCmd([]string{"copy", "-r", "svc-a:configs", dest}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	outPath := filepath.Join(dest, "configs", "app.yml")
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if string(out) != "payload" {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestHandleSvcCmdEnvCopyUsesExecRemote(t *testing.T) {
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
	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		gotStdin = stdin
		gotTTY = tty
		return nil
	}

	if err := HandleSvcCmd([]string{"env", "copy", src}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	if gotService != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", gotService)
	}
	wantArgs := []string{"env", "copy"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	if gotStdin == nil {
		t.Fatalf("expected stdin to be provided")
	}
	if gotTTY {
		t.Fatalf("expected tty=false, got true")
	}
}

func TestHandleSvcCmdEnvSetUsesExecRemote(t *testing.T) {
	oldExec := execRemoteFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"

	var gotService string
	var gotArgs []string
	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		gotTTY = tty
		return nil
	}

	if err := HandleSvcCmd([]string{"env", "set", "FOO=bar", "FOO=baz", "PORT=8080"}); err != nil {
		t.Fatalf("HandleSvcCmd returned error: %v", err)
	}

	if gotService != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", gotService)
	}
	wantArgs := []string{"env", "set", "FOO=baz", "PORT=8080"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, gotArgs)
	}
	if !gotTTY {
		t.Fatalf("expected tty=true, got false")
	}
}
