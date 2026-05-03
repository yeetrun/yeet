// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"errors"
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
