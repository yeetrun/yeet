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

func TestParseInitArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantPos     []string
		wantGithub  bool
		wantNightly bool
		wantErr     bool
	}{
		{
			name:    "strips command name",
			args:    []string{"init", "root@example.com"},
			wantPos: []string{"root@example.com"},
		},
		{
			name:        "parses flags",
			args:        []string{"--from-github", "--nightly", "root@example.com"},
			wantPos:     []string{"root@example.com"},
			wantGithub:  true,
			wantNightly: true,
		},
		{
			name:    "rejects too many args",
			args:    []string{"one", "two"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pos, opts, err := parseInitArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInitArgs failed: %v", err)
			}
			if strings.Join(pos, "\n") != strings.Join(tt.wantPos, "\n") {
				t.Fatalf("pos = %#v, want %#v", pos, tt.wantPos)
			}
			if opts.fromGithub != tt.wantGithub {
				t.Fatalf("fromGithub = %v, want %v", opts.fromGithub, tt.wantGithub)
			}
			if opts.nightly != tt.wantNightly {
				t.Fatalf("nightly = %v, want %v", opts.nightly, tt.wantNightly)
			}
		})
	}
}

func TestHandleInitReturnsParseErrorsBeforeRemoteWork(t *testing.T) {
	err := HandleInit(context.Background(), []string{"one", "two"})
	if err == nil || !strings.Contains(err.Error(), "at most one argument") {
		t.Fatalf("HandleInit error = %v, want parse error", err)
	}
}

func TestBuildCatchCmdDisablesCGOForCrossCompile(t *testing.T) {
	cmd := buildCatchCmd("amd64", "/tmp/repo")

	if got := cmd.Dir; got != "/tmp/repo" {
		t.Fatalf("Dir = %q, want /tmp/repo", got)
	}
	if got := strings.Join(cmd.Args, " "); got != "go build -o catch ./cmd/catch" {
		t.Fatalf("Args = %q", got)
	}

	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{
		"GOOS=linux",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("expected %q in env", want)
		}
	}
}

func TestBuildCatchUsesGoBuildOutput(t *testing.T) {
	gitRoot := t.TempDir()
	fakeCommandInPath(t, "go", "printf 'catch-binary' > catch\n")

	bin, size, err := buildCatch("Linux", "ARM64", gitRoot)
	if err != nil {
		t.Fatalf("buildCatch error: %v", err)
	}
	if bin != filepath.Join(gitRoot, "catch") {
		t.Fatalf("bin = %q, want catch under git root", bin)
	}
	if size != int64(len("catch-binary")) {
		t.Fatalf("size = %d, want fake binary size", size)
	}
}

func TestBuildCatchReportsGoBuildError(t *testing.T) {
	fakeCommandInPath(t, "go", "printf 'compile failed\\n' >&2\nexit 1\n")

	_, _, err := buildCatch("Linux", "amd64", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "failed to build catch binary") {
		t.Fatalf("buildCatch error = %v, want build error", err)
	}
}

func TestNormalizeBuildTarget(t *testing.T) {
	goos, goarch, err := normalizeBuildTarget("Linux", "AMD64", "/tmp/repo")
	if err != nil {
		t.Fatalf("normalizeBuildTarget failed: %v", err)
	}
	if goos != "linux" || goarch != "amd64" {
		t.Fatalf("target = %s/%s, want linux/amd64", goos, goarch)
	}

	if _, _, err := normalizeBuildTarget("Darwin", "arm64", "/tmp/repo"); err == nil {
		t.Fatal("expected non-linux error")
	}
	if _, _, err := normalizeBuildTarget("Linux", "arm64", ""); err == nil {
		t.Fatal("expected missing git root error")
	}
}

func TestFormatBuildDetail(t *testing.T) {
	if got := formatBuildDetail("", "linux/amd64", 0); got != "linux/amd64" {
		t.Fatalf("detail = %q", got)
	}
	got := formatBuildDetail("go version go1.25.0 darwin/arm64", "linux/arm64", 2048)
	want := "go version go1.25.0 darwin/arm64 -> linux/arm64, 2.00 KB"
	if got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
}

func TestShouldUseSudoForInit(t *testing.T) {
	if shouldUseSudoForInit("root@example.com") {
		t.Fatal("root install should not require sudo")
	}

	stderr := captureInitStderr(t, func() {
		if !shouldUseSudoForInit("admin@example.com") {
			t.Fatal("non-root install should require sudo")
		}
	})
	if !strings.Contains(stderr, "sudo will be used") {
		t.Fatalf("stderr = %q, want sudo warning", stderr)
	}
}

func TestRemoteCatchInstallArgs(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch-host"

	tests := []struct {
		name         string
		userAtRemote string
		useSudo      bool
		want         []string
	}{
		{
			name:         "root user preserves install env",
			userAtRemote: "root@example.com",
			want: []string{
				"-t", "root@example.com",
				"env", "CATCH_INSTALL_USER=root", "CATCH_INSTALL_HOST=example.com",
				"./catch", "--tsnet-host=catch-host", "install",
			},
		},
		{
			name:         "sudo host only",
			userAtRemote: "example.com",
			useSudo:      true,
			want: []string{
				"-t", "example.com",
				"sudo",
				"env", "CATCH_INSTALL_HOST=example.com",
				"./catch", "--tsnet-host=catch-host", "install",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := remoteCatchInstallArgs(tt.userAtRemote, tt.useSudo)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("remoteCatchInstallArgs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCatchInstallEnv(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "user and host", in: "root@example.com", want: []string{"CATCH_INSTALL_USER=root", "CATCH_INSTALL_HOST=example.com"}},
		{name: "host only", in: "example.com", want: []string{"CATCH_INSTALL_HOST=example.com"}},
		{name: "empty", in: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catchInstallEnv(tt.in)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("env = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveInitCatchSourceFromGitHub(t *testing.T) {
	got, err := resolveInitCatchSource(initOptions{fromGithub: true})
	if err != nil {
		t.Fatalf("resolveInitCatchSource returned error: %v", err)
	}
	if !got.useGithub || got.reason != "using GitHub release" {
		t.Fatalf("source = %#v, want GitHub release source", got)
	}
}

func TestVerifyInitSSHSkipsNonTerminal(t *testing.T) {
	oldIsTerminal := isTerminalFn
	defer func() { isTerminalFn = oldIsTerminal }()
	isTerminalFn = func(int) bool { return false }

	if err := verifyInitSSH(nil, "root@example.com"); err != nil {
		t.Fatalf("verifyInitSSH returned error: %v", err)
	}
}

func TestDetectInitHostUsesRemoteUname(t *testing.T) {
	fakeSSHInPath(t, "printf 'Linux\\nx86_64\\n'\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	systemName, goarch, err := detectInitHost(ui, "root@example.com")
	if err != nil {
		t.Fatalf("detectInitHost error: %v", err)
	}
	if systemName != "Linux" || goarch != "amd64" {
		t.Fatalf("detectInitHost = %s/%s, want Linux/amd64", systemName, goarch)
	}
}

func TestRemoteHostOSAndArchRejectsUnexpectedOutput(t *testing.T) {
	fakeSSHInPath(t, "printf 'Linux\\n'\n")

	_, _, err := remoteHostOSAndArch("root@example.com")
	if err == nil || !strings.Contains(err.Error(), "unexpected output") {
		t.Fatalf("remoteHostOSAndArch error = %v, want unexpected output", err)
	}
}

func TestPreflightSSHHostKey(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fakeSSHInPath(t, "exit 0\n")
		if err := preflightSSHHostKey("root@example.com"); err != nil {
			t.Fatalf("preflightSSHHostKey error: %v", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		fakeSSHInPath(t, "exit 1\n")
		err := preflightSSHHostKey("root@example.com")
		if err == nil || !strings.Contains(err.Error(), "ssh preflight failed") {
			t.Fatalf("preflightSSHHostKey error = %v, want preflight error", err)
		}
	})
}

func TestChmodInitCatchUsesSSH(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "ssh.log")
	fakeSSHInPath(t, "printf '%s\\n' \"$*\" > "+strconvQuoteForShell(logFile)+"\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	if err := chmodInitCatch(ui, "root@example.com"); err != nil {
		t.Fatalf("chmodInitCatch error: %v", err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile ssh log: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "root@example.com chmod +x ./catch" {
		t.Fatalf("ssh args = %q", got)
	}
}

func TestChmodInitCatchReportsSSHError(t *testing.T) {
	fakeSSHInPath(t, "exit 1\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	err := chmodInitCatch(ui, "root@example.com")
	if err == nil || !strings.Contains(err.Error(), "failed to make catch binary executable") {
		t.Fatalf("chmodInitCatch error = %v, want chmod error", err)
	}
}

func TestInstallInitCatchUsesFilteredSSHOutput(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch-host"
	fakeSSHInPath(t, strings.Join([]string{
		"printf '%s\\n' 'data dir: /srv/yeet'",
		"printf '%s\\n' 'tsnet running state path /srv/yeet/tsnet/tailscaled.state'",
		"printf '%s\\n' 'Warning: docker missing'",
	}, "\n")+"\n")
	ui := newInitUI(io.Discard, false, true, "catch-host", "root@example.com", catchServiceName)

	if err := installInitCatch(ui, "root@example.com", false); err != nil {
		t.Fatalf("installInitCatch error: %v", err)
	}
}

func TestInstallInitCatchReportsSSHError(t *testing.T) {
	fakeSSHInPath(t, "exit 1\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	err := installInitCatch(ui, "root@example.com", false)
	if err == nil || !strings.Contains(err.Error(), "failed to run catch binary") {
		t.Fatalf("installInitCatch error = %v, want install error", err)
	}
}

func TestHasLocalCatchDir(t *testing.T) {
	tmp := t.TempDir()
	if hasLocalCatchDir(tmp) {
		t.Fatal("hasLocalCatchDir = true before cmd/catch exists")
	}
	if err := os.MkdirAll(filepath.Join(tmp, "cmd", "catch"), 0o755); err != nil {
		t.Fatalf("MkdirAll cmd/catch: %v", err)
	}
	if !hasLocalCatchDir(tmp) {
		t.Fatal("hasLocalCatchDir = false after cmd/catch exists")
	}
}

func TestLocalCatchRepoRootAndGoVersion(t *testing.T) {
	root, ok, err := localCatchRepoRoot()
	if err != nil {
		t.Fatalf("localCatchRepoRoot error: %v", err)
	}
	if !ok {
		t.Skip("git checkout with cmd/catch not available")
	}
	if !hasLocalCatchDir(root) {
		t.Fatalf("root %q does not contain cmd/catch", root)
	}

	version, err := localGoVersion(root)
	if err != nil {
		t.Fatalf("localGoVersion error: %v", err)
	}
	if !strings.HasPrefix(version, "go version ") {
		t.Fatalf("localGoVersion = %q, want go version output", version)
	}
}

func TestRemoveFileBestEffort(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "catch")
	if err := os.WriteFile(path, []byte("bin"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	removeFileBestEffort(path)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat removed file error = %v, want not exist", err)
	}

	removeFileBestEffort(filepath.Join(tmp, "missing"))
}

func TestInitCatchUsesGitHubSource(t *testing.T) {
	restore := stubInitCatchWorkflow(t)
	var steps []string
	restore(&steps)
	resolveInitCatchSourceFn = func(opts initOptions) (initCatchSource, error) {
		if !opts.fromGithub || !opts.nightly {
			t.Fatalf("opts = %#v, want from github nightly", opts)
		}
		steps = append(steps, "resolve")
		return initCatchSource{useGithub: true, reason: "using GitHub release"}, nil
	}
	detectInitHostFn = func(_ *initUI, userAtRemote string) (string, string, error) {
		if userAtRemote != "root@example.com" {
			t.Fatalf("detect user = %q", userAtRemote)
		}
		steps = append(steps, "detect")
		return "Linux", "amd64", nil
	}
	downloadInitCatchFn = func(_ *initUI, userAtRemote, systemName, goarch string, nightly bool) error {
		steps = append(steps, strings.Join([]string{"download", userAtRemote, systemName, goarch, boolString(nightly)}, ":"))
		return nil
	}
	buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource) error {
		t.Fatal("buildAndUploadInitCatchFn should not be called for GitHub source")
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, nightly: true}); err != nil {
		t.Fatalf("initCatch error: %v", err)
	}

	want := []string{
		"resolve",
		"verify",
		"detect",
		"download:root@example.com:Linux:amd64:true",
		"chmod",
		"install:false",
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestInitCatchBuildsAndUploadsLocalSource(t *testing.T) {
	restore := stubInitCatchWorkflow(t)
	var steps []string
	restore(&steps)
	source := initCatchSource{gitRoot: "/repo", goVersion: "go version go1.25.0 darwin/arm64"}
	resolveInitCatchSourceFn = func(initOptions) (initCatchSource, error) {
		steps = append(steps, "resolve")
		return source, nil
	}
	detectInitHostFn = func(*initUI, string) (string, string, error) {
		steps = append(steps, "detect")
		return "Linux", "arm64", nil
	}
	downloadInitCatchFn = func(*initUI, string, string, string, bool) error {
		t.Fatal("downloadInitCatchFn should not be called for local source")
		return nil
	}
	buildAndUploadInitCatchFn = func(_ *initUI, userAtRemote, systemName, goarch string, gotSource initCatchSource) error {
		if gotSource != source {
			t.Fatalf("source = %#v, want %#v", gotSource, source)
		}
		steps = append(steps, strings.Join([]string{"build-upload", userAtRemote, systemName, goarch}, ":"))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{}); err != nil {
		t.Fatalf("initCatch error: %v", err)
	}

	want := []string{"resolve", "verify", "detect", "build-upload:root@example.com:Linux:arm64", "chmod", "install:false"}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestInitCatchStopsWhenSourceFails(t *testing.T) {
	restore := stubInitCatchWorkflow(t)
	wantErr := errors.New("source failed")
	resolveInitCatchSourceFn = func(initOptions) (initCatchSource, error) {
		return initCatchSource{}, wantErr
	}
	verifyInitSSHFn = func(*initUI, string) error {
		t.Fatal("verifyInitSSHFn should not be called after source failure")
		return nil
	}
	restore(nil)

	if err := initCatch("root@example.com", initOptions{}); !errors.Is(err, wantErr) {
		t.Fatalf("initCatch error = %v, want %v", err, wantErr)
	}
}

func TestInitCatchStopsWhenInstallPrepFails(t *testing.T) {
	restore := stubInitCatchWorkflow(t)
	wantErr := errors.New("chmod failed")
	var steps []string
	restore(&steps)
	chmodInitCatchFn = func(*initUI, string) error {
		steps = append(steps, "chmod")
		return wantErr
	}
	installInitCatchFn = func(*initUI, string, bool) error {
		t.Fatal("installInitCatchFn should not be called after chmod failure")
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true}); !errors.Is(err, wantErr) {
		t.Fatalf("initCatch error = %v, want %v", err, wantErr)
	}
	if strings.Join(steps, "\n") != strings.Join([]string{"verify", "detect", "download", "chmod"}, "\n") {
		t.Fatalf("steps = %#v", steps)
	}
}

func stubInitCatchWorkflow(t *testing.T) func(*[]string) {
	t.Helper()
	oldUIConfig := CurrentUIConfig()
	oldResolve := resolveInitCatchSourceFn
	oldVerify := verifyInitSSHFn
	oldDetect := detectInitHostFn
	oldDownload := downloadInitCatchFn
	oldBuildAndUpload := buildAndUploadInitCatchFn
	oldChmod := chmodInitCatchFn
	oldInstall := installInitCatchFn
	SetUIConfig(UIConfig{Progress: "quiet"})
	t.Cleanup(func() {
		SetUIConfig(oldUIConfig)
		resolveInitCatchSourceFn = oldResolve
		verifyInitSSHFn = oldVerify
		detectInitHostFn = oldDetect
		downloadInitCatchFn = oldDownload
		buildAndUploadInitCatchFn = oldBuildAndUpload
		chmodInitCatchFn = oldChmod
		installInitCatchFn = oldInstall
	})

	resolveInitCatchSourceFn = func(initOptions) (initCatchSource, error) {
		return initCatchSource{useGithub: true, reason: "using GitHub release"}, nil
	}
	verifyInitSSHFn = func(*initUI, string) error { return nil }
	detectInitHostFn = func(*initUI, string) (string, string, error) { return "Linux", "amd64", nil }
	downloadInitCatchFn = func(*initUI, string, string, string, bool) error { return nil }
	buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource) error { return nil }
	chmodInitCatchFn = func(*initUI, string) error { return nil }
	installInitCatchFn = func(*initUI, string, bool) error { return nil }

	return func(steps *[]string) {
		if steps == nil {
			return
		}
		verifyInitSSHFn = func(*initUI, string) error {
			*steps = append(*steps, "verify")
			return nil
		}
		detectInitHostFn = func(*initUI, string) (string, string, error) {
			*steps = append(*steps, "detect")
			return "Linux", "amd64", nil
		}
		downloadInitCatchFn = func(*initUI, string, string, string, bool) error {
			*steps = append(*steps, "download")
			return nil
		}
		buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource) error {
			*steps = append(*steps, "build-upload")
			return nil
		}
		chmodInitCatchFn = func(*initUI, string) error {
			*steps = append(*steps, "chmod")
			return nil
		}
		installInitCatchFn = func(_ *initUI, _ string, useSudo bool) error {
			*steps = append(*steps, "install:"+boolString(useSudo))
			return nil
		}
	}
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func captureInitStderr(t *testing.T, fn func()) string {
	t.Helper()
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr Pipe error: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = oldStderr
		_ = r.Close()
	}()

	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("stderr close error: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("stderr ReadAll error: %v", err)
	}
	return string(out)
}

func fakeSSHInPath(t *testing.T, body string) string {
	t.Helper()
	return fakeCommandInPath(t, "ssh", body)
}

func fakeCommandInPath(t *testing.T, name, body string) string {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake command bin: %v", err)
	}
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", binDir)
	return path
}

func strconvQuoteForShell(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
