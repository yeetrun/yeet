// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseInitArgs(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantPos      []string
		wantGithub   bool
		wantNightly  bool
		wantDocker   bool
		wantVMTools  bool
		wantTSAuth   string
		wantTSClient string
		wantErr      bool
	}{
		{
			name:    "strips command name",
			args:    []string{"init", "root@example.com"},
			wantPos: []string{"root@example.com"},
		},
		{
			name:        "parses flags",
			args:        []string{"--from-github", "--nightly", "--install-docker", "--install-vm-tools", "--ts-auth-key=tskey-test", "root@example.com"},
			wantPos:     []string{"root@example.com"},
			wantGithub:  true,
			wantNightly: true,
			wantDocker:  true,
			wantVMTools: true,
			wantTSAuth:  "tskey-test",
		},
		{
			name:         "parses tailscale oauth client secret",
			args:         []string{"--ts-client-secret=tskey-client-test", "root@example.com"},
			wantPos:      []string{"root@example.com"},
			wantTSClient: "tskey-client-test",
		},
		{
			name:    "rejects auth key and client secret together",
			args:    []string{"--ts-auth-key=tskey-auth-test", "--ts-client-secret=tskey-client-test", "root@example.com"},
			wantErr: true,
		},
		{
			name:    "rejects invalid client secret",
			args:    []string{"--ts-client-secret=not-a-client-secret", "root@example.com"},
			wantErr: true,
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
			if opts.installDocker != tt.wantDocker {
				t.Fatalf("installDocker = %v, want %v", opts.installDocker, tt.wantDocker)
			}
			if opts.installVMTools != tt.wantVMTools {
				t.Fatalf("installVMTools = %v, want %v", opts.installVMTools, tt.wantVMTools)
			}
			if opts.tsAuthKey != tt.wantTSAuth {
				t.Fatalf("tsAuthKey = %q, want %q", opts.tsAuthKey, tt.wantTSAuth)
			}
			if opts.tsClientSecret != tt.wantTSClient {
				t.Fatalf("tsClientSecret = %q, want %q", opts.tsClientSecret, tt.wantTSClient)
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

func TestBuildCatchCmdStampsLocalGitVersion(t *testing.T) {
	root := t.TempDir()
	runInitTestGit(t, root, "init")
	runInitTestGit(t, root, "config", "user.email", "test@example.com")
	runInitTestGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runInitTestGit(t, root, "add", "tracked.txt")
	runInitTestGit(t, root, "commit", "-m", "initial")
	short := strings.TrimSpace(runInitTestGit(t, root, "rev-parse", "--short=9", "HEAD"))

	if err := os.WriteFile(filepath.Join(root, "catch"), []byte("untracked build output\n"), 0o644); err != nil {
		t.Fatalf("write untracked catch: %v", err)
	}
	args := strings.Join(buildCatchCmd("amd64", root).Args, " ")
	wantClean := "-X github.com/yeetrun/yeet/pkg/buildinfo.BuildVersion=" + short
	if !strings.Contains(args, wantClean) {
		t.Fatalf("build args = %q, want %q", args, wantClean)
	}
	if strings.Contains(args, short+"+dirty") {
		t.Fatalf("build args = %q, untracked catch should not mark dirty", args)
	}

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	args = strings.Join(buildCatchCmd("amd64", root).Args, " ")
	if !strings.Contains(args, wantClean+"+dirty") {
		t.Fatalf("build args = %q, want dirty stamp %q", args, wantClean+"+dirty")
	}
}

func runInitTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitWorkTreeEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
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

func TestRemoteDockerInstalledUsesBashProbe(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "ssh.log")
	fakeSSHInPath(t, `
printf '%s\n' "$@" > `+strconvQuoteForShell(logFile)+`
if [ "$#" -ne 2 ]; then
	echo "ssh command must pass one quoted remote command" >&2
	exit 127
fi
case "$2" in
	"bash -lc "*"'if command -v docker"*) printf yes ;;
	*) echo "unexpected remote command: $2" >&2; exit 2 ;;
esac
`)

	installed, err := remoteDockerInstalled("root@example.com")
	if err != nil {
		t.Fatalf("remoteDockerInstalled failed: %v", err)
	}
	if !installed {
		t.Fatal("installed = false, want true")
	}
	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read ssh log: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "\nbash -lc ") {
		t.Fatalf("ssh args = %q, want single bash -lc remote command", got)
	}
}

func TestPrepareInitDockerInstallRequiresFlagWhenMissingAndNonInteractive(t *testing.T) {
	oldRemoteDocker := remoteDockerInstalledFn
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteDockerInstalledFn = oldRemoteDocker
		isTerminalFn = oldIsTerminal
	})
	remoteDockerInstalledFn = func(string) (bool, error) { return false, nil }
	isTerminalFn = func(int) bool { return false }
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	installDocker, err := prepareInitDockerInstall(ui, "root@example.com", initOptions{})
	if err == nil || !strings.Contains(err.Error(), "--install-docker") {
		t.Fatalf("prepareInitDockerInstall error = %v, want --install-docker hint", err)
	}
	if installDocker {
		t.Fatal("installDocker = true after non-interactive missing docker error")
	}
}

func TestPrepareInitVMToolsInstallPromptsForInteractiveCapableAptHost(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldConfirm := confirmInitFn
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		confirmInitFn = oldConfirm
		isTerminalFn = oldIsTerminal
	})
	remoteVMHostStatusFn = func(userAtRemote string, goarch string) (initVMHostStatus, error) {
		if userAtRemote != "root@example.com" {
			t.Fatalf("userAtRemote = %q, want root@example.com", userAtRemote)
		}
		if goarch != "amd64" {
			t.Fatalf("goarch = %q, want amd64", goarch)
		}
		return initVMHostStatus{
			AptGet: true,
			KVM:    true,
			TUN:    true,
			MissingCommands: []vmHostCommandRequirement{
				{Command: "qemu-img", Package: "qemu-utils"},
			},
		}, nil
	}
	isTerminalFn = func(int) bool { return true }
	var prompt string
	confirmInitFn = func(_ io.Reader, _ io.Writer, msg string) (bool, error) {
		prompt = msg
		return true, nil
	}
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if !installVMTools {
		t.Fatal("installVMTools = false after prompt confirmation")
	}
	wantPrompt := "VM payloads can run on this host, but VM host packages are missing. Install them now?"
	if prompt != wantPrompt {
		t.Fatalf("prompt = %q, want %q", prompt, wantPrompt)
	}
}

func TestPrepareInitVMToolsInstallWarnsWithFlagWhenCapableAptHostIsNonInteractive(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldConfirm := confirmInitFn
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		confirmInitFn = oldConfirm
		isTerminalFn = oldIsTerminal
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{
			AptGet: true,
			KVM:    true,
			TUN:    true,
			MissingCommands: []vmHostCommandRequirement{
				{Command: "qemu-img", Package: "qemu-utils"},
			},
		}, nil
	}
	isTerminalFn = func(int) bool { return false }
	confirmInitFn = func(io.Reader, io.Writer, string) (bool, error) {
		t.Fatal("confirmInitFn should not be called for non-interactive init")
		return false, nil
	}
	var out strings.Builder
	ui := newInitUI(&out, false, false, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if installVMTools {
		t.Fatal("installVMTools = true for non-interactive init without --install-vm-tools")
	}
	for _, want := range []string{"--install-vm-tools", "qemu-utils", "#host-requirements"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
}

func TestPrepareInitVMToolsInstallWarnsAndContinuesWhenPreflightFails(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldConfirm := confirmInitFn
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		confirmInitFn = oldConfirm
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{}, errors.New("probe failed")
	}
	confirmInitFn = func(io.Reader, io.Writer, string) (bool, error) {
		t.Fatal("confirmInitFn should not be called when VM preflight fails")
		return false, nil
	}
	var out strings.Builder
	ui := newInitUI(&out, false, false, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v, want nil", err)
	}
	if installVMTools {
		t.Fatal("installVMTools = true after optional preflight failure")
	}
	for _, want := range []string{"Warning: could not check VM host packages", "probe failed", "#host-requirements"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
}

func TestPrepareInitVMToolsInstallExplicitFlagSkipsRemotePreflight(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldConfirm := confirmInitFn
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		confirmInitFn = oldConfirm
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		t.Fatal("remoteVMHostStatusFn should not be called when --install-vm-tools is set")
		return initVMHostStatus{}, nil
	}
	confirmInitFn = func(io.Reader, io.Writer, string) (bool, error) {
		t.Fatal("confirmInitFn should not be called when --install-vm-tools is set")
		return false, nil
	}
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{installVMTools: true})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if !installVMTools {
		t.Fatal("installVMTools = false with explicit --install-vm-tools")
	}
}

func TestPrepareInitVMToolsInstallWarnsForNonVMCapableHost(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldConfirm := confirmInitFn
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		confirmInitFn = oldConfirm
		isTerminalFn = oldIsTerminal
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{
			AptGet: true,
			KVM:    false,
			TUN:    true,
			MissingCommands: []vmHostCommandRequirement{
				{Command: "qemu-img", Package: "qemu-utils"},
			},
		}, nil
	}
	isTerminalFn = func(int) bool { return true }
	confirmInitFn = func(io.Reader, io.Writer, string) (bool, error) {
		t.Fatal("confirmInitFn should not be called when /dev/kvm is missing")
		return false, nil
	}
	var out strings.Builder
	ui := newInitUI(&out, false, false, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if installVMTools {
		t.Fatal("installVMTools = true for non-VM-capable host")
	}
	if !strings.Contains(out.String(), "Warning: VM support is unavailable on this host: /dev/kvm is missing.") {
		t.Fatalf("output = %q, want /dev/kvm warning", out.String())
	}
}

func TestParseInitVMHostStatus(t *testing.T) {
	t.Run("detects missing commands from successful probe output", func(t *testing.T) {
		output := strings.Join([]string{
			"apt-get=yes",
			"dev-kvm=yes",
			"dev-net-tun=yes",
			"cmd:qemu-img=no",
			"cmd:zstd=yes",
			"cmd:e2fsck=yes",
			"cmd:resize2fs=yes",
			"cmd:mount=yes",
			"cmd:umount=yes",
			"cmd:ip=yes",
		}, "\n")

		status, err := parseInitVMHostStatus(output, "amd64")
		if err != nil {
			t.Fatalf("parseInitVMHostStatus error = %v", err)
		}
		if !status.AptGet || !status.KVM || !status.TUN {
			t.Fatalf("status = %#v, want apt-get, kvm, and tun present", status)
		}
		wantMissing := []vmHostCommandRequirement{{Command: "qemu-img", Package: "qemu-utils"}}
		if !reflect.DeepEqual(status.MissingCommands, wantMissing) {
			t.Fatalf("MissingCommands = %#v, want %#v", status.MissingCommands, wantMissing)
		}
	})

	t.Run("rejects malformed line", func(t *testing.T) {
		if _, err := parseInitVMHostStatus("not-a-key-value-line", "amd64"); err == nil {
			t.Fatal("parseInitVMHostStatus error = nil, want malformed line error")
		}
	})

	t.Run("rejects unknown key", func(t *testing.T) {
		if _, err := parseInitVMHostStatus("unexpected=yes", "amd64"); err == nil {
			t.Fatal("parseInitVMHostStatus error = nil, want unknown key error")
		}
	})
}

func TestInitCatchPassesInstallDockerFlagToRemoteInstall(t *testing.T) {
	var steps []string
	configureSteps := stubInitCatchWorkflow(t)
	configureSteps(&steps)
	installInitCatchFn = func(_ *initUI, _ string, _ bool, installDocker bool, installVMTools bool, _ string, _ string, _ []string) error {
		steps = append(steps, "install-docker:"+boolString(installDocker)+":install-vm-tools:"+boolString(installVMTools))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, installDocker: true, installVMTools: true}); err != nil {
		t.Fatalf("initCatch returned error: %v", err)
	}
	if got := steps[len(steps)-1]; got != "install-docker:true:install-vm-tools:true" {
		t.Fatalf("last step = %q, want install flags true; steps=%#v", got, steps)
	}
}

func TestRemoteCatchInstallArgs(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch-host"

	tests := []struct {
		name           string
		userAtRemote   string
		useSudo        bool
		installDocker  bool
		installVMTools bool
		tsAuthKey      string
		tsClientSecret string
		tsCatchTags    []string
		want           []string
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
			name:         "ts auth key env",
			userAtRemote: "root@example.com",
			tsAuthKey:    "tskey-test",
			want: []string{
				"-t", "root@example.com",
				"env", "CATCH_INSTALL_USER=root", "CATCH_INSTALL_HOST=example.com", "TS_AUTHKEY=tskey-test",
				"./catch", "--tsnet-host=catch-host", "install",
			},
		},
		{
			name:           "tailscale oauth env",
			userAtRemote:   "root@example.com",
			tsClientSecret: "tskey-client-test",
			tsCatchTags:    []string{"tag:catch"},
			want: []string{
				"-t", "root@example.com",
				"env", "CATCH_INSTALL_USER=root", "CATCH_INSTALL_HOST=example.com", "TS_CLIENT_SECRET=tskey-client-test", "TS_CATCH_TAGS=tag:catch",
				"./catch", "--tsnet-host=catch-host", "install",
			},
		},
		{
			name:           "explicit package install env",
			userAtRemote:   "root@example.com",
			installDocker:  true,
			installVMTools: true,
			want: []string{
				"-t", "root@example.com",
				"env", "CATCH_INSTALL_USER=root", "CATCH_INSTALL_HOST=example.com", "CATCH_INSTALL_DOCKER=1", "CATCH_INSTALL_VM_TOOLS=1",
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
			got := remoteCatchInstallArgs(tt.userAtRemote, tt.useSudo, tt.installDocker, tt.installVMTools, tt.tsAuthKey, tt.tsClientSecret, tt.tsCatchTags)
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
	logFile := filepath.Join(t.TempDir(), "ssh.log")
	fakeSSHInPath(t, strings.Join([]string{
		"printf '%s\\n' \"$*\" >> " + strconvQuoteForShell(logFile),
		"case \"$*\" in",
		"  *'.status'*) printf '0' ;;",
		"  *'.log'*)",
		"    printf '%s\\n' 'data dir: /srv/yeet'",
		"    printf '%s\\n' 'tsnet running state path /srv/yeet/tsnet/tailscaled.state'",
		"    printf '%s\\n' 'Warning: docker missing'",
		"    ;;",
		"esac",
	}, "\n")+"\n")
	ui := newInitUI(io.Discard, false, true, "catch-host", "root@example.com", catchServiceName)

	if err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil); err != nil {
		t.Fatalf("installInitCatch error: %v", err)
	}
	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile ssh log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 3 {
		t.Fatalf("ssh calls = %#v, want launch, status, log calls", lines)
	}
	if strings.Contains(lines[0], " -t ") || strings.HasPrefix(lines[0], "-t ") {
		t.Fatalf("async install launch should not force a TTY: %q", lines[0])
	}
	for _, want := range []string{"root@example.com", "nohup", "CATCH_INSTALL_USER=root", "CATCH_INSTALL_HOST=example.com", "--tsnet-host=catch-host"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("launch ssh call missing %q: %q", want, lines[0])
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), ".status") {
		t.Fatalf("ssh calls missing status poll: %#v", lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), ".log") {
		t.Fatalf("ssh calls missing log fetch: %#v", lines)
	}
}

func TestInstallInitCatchReportsSSHError(t *testing.T) {
	fakeSSHInPath(t, "exit 1\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil)
	if err == nil || !strings.Contains(err.Error(), "failed to run catch binary") {
		t.Fatalf("installInitCatch error = %v, want install error", err)
	}
}

func TestInstallInitCatchReportsRemoteInstallStatusError(t *testing.T) {
	fakeSSHInPath(t, strings.Join([]string{
		"case \"$*\" in",
		"  *'.status'*) printf '7' ;;",
		"  *'.log'*) printf '%s\\n' 'remote install failed' ;;",
		"esac",
	}, "\n")+"\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil)
	if err == nil || !strings.Contains(err.Error(), "failed to run catch binary") {
		t.Fatalf("installInitCatch error = %v, want install error", err)
	}
}

func TestInstallInitCatchRetriesHungStatusPoll(t *testing.T) {
	restore := overrideInitInstallTiming(t, time.Millisecond, 2*time.Second, 500*time.Millisecond)
	defer restore()
	tmp := t.TempDir()
	counter := filepath.Join(tmp, "status-count")
	fakeSSHInPath(t, strings.Join([]string{
		"case \"$*\" in",
		"  *cat*'.status'*)",
		"    if [ ! -f " + strconvQuoteForShell(counter) + " ]; then",
		"      printf 1 > " + strconvQuoteForShell(counter),
		"      sleep 1",
		"    fi",
		"    printf '0'",
		"    ;;",
		"  *'.log'*) printf '%s\\n' 'Service \"catch\" installed' ;;",
		"esac",
	}, "\n")+"\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	if err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil); err != nil {
		t.Fatalf("installInitCatch error: %v", err)
	}
}

func TestInstallInitCatchWithTailscaleRetryPromptsAfterCredentialError(t *testing.T) {
	oldInstall := installInitCatchFn
	oldIsTerminal := isTerminalFn
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	t.Cleanup(func() {
		installInitCatchFn = oldInstall
		isTerminalFn = oldIsTerminal
		os.Stdin = oldStdin
		os.Stdout = oldStdout
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	os.Stdin = inR
	os.Stdout = outW
	isTerminalFn = func(int) bool { return true }
	if _, err := inW.WriteString("tskey-client-good\n"); err != nil {
		t.Fatalf("write prompt input: %v", err)
	}
	_ = inW.Close()

	var installs int
	var gotSecret string
	installInitCatchFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ string, tsClientSecret string, tsCatchTags []string) error {
		installs++
		if installs == 1 {
			return errTailscaleCredentialRequired
		}
		gotSecret = tsClientSecret
		if !reflect.DeepEqual(tsCatchTags, []string{defaultCatchTag}) {
			t.Fatalf("tsCatchTags = %#v, want tag:catch", tsCatchTags)
		}
		return nil
	}

	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)
	if err := installInitCatchWithTailscaleRetry(ui, "root@example.com", false, false, false, initOptions{}); err != nil {
		t.Fatalf("installInitCatchWithTailscaleRetry error: %v", err)
	}
	if err := outW.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	_, _ = io.ReadAll(outR)
	if installs != 2 {
		t.Fatalf("installs = %d, want 2", installs)
	}
	if gotSecret != "tskey-client-good" {
		t.Fatalf("secret = %q, want prompted secret", gotSecret)
	}
}

func TestWaitDetachedInitCatchInstallStreamsLogsWhileStatusIsPending(t *testing.T) {
	restore := overrideInitInstallTiming(t, 10*time.Millisecond, 500*time.Millisecond, 500*time.Millisecond)
	defer restore()
	fakeSSHInPath(t, strings.Join([]string{
		"case \"$*\" in",
		"  *'.status'*) ;;",
		"  *'.log'*) printf '%s\\n' 'tailscale OAuth setup failed: tag:catch not allowed' ;;",
		"esac",
	}, "\n")+"\n")
	var out strings.Builder
	filter := newInitInstallFilter(&out)

	_, err := waitDetachedInitCatchInstall("root@example.com", initInstallSession{
		LogPath:    "/tmp/yeet-test.log",
		StatusPath: "/tmp/yeet-test.status",
	}, filter)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("waitDetachedInitCatchInstall error = %v, want timeout", err)
	}
	if !strings.Contains(out.String(), "tailscale OAuth setup failed") {
		t.Fatalf("streamed output = %q, want OAuth error", out.String())
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
		if !opts.fromGithub || !opts.nightly || opts.releaseVersion != "v0.6.1" {
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
	downloadInitCatchFn = func(_ *initUI, userAtRemote, systemName, goarch string, nightly bool, version string) error {
		steps = append(steps, strings.Join([]string{"download", userAtRemote, systemName, goarch, boolString(nightly), version}, ":"))
		return nil
	}
	buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource) error {
		t.Fatal("buildAndUploadInitCatchFn should not be called for GitHub source")
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, nightly: true, releaseVersion: "v0.6.1"}); err != nil {
		t.Fatalf("initCatch error: %v", err)
	}

	want := []string{
		"resolve",
		"verify",
		"detect",
		"download:root@example.com:Linux:amd64:true:v0.6.1",
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
	downloadInitCatchFn = func(*initUI, string, string, string, bool, string) error {
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
	installInitCatchFn = func(*initUI, string, bool, bool, bool, string, string, []string) error {
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
	oldPrepareDocker := prepareInitDockerInstallFn
	oldPrepareVMTools := prepareInitVMToolsInstallFn
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
		prepareInitDockerInstallFn = oldPrepareDocker
		prepareInitVMToolsInstallFn = oldPrepareVMTools
	})

	resolveInitCatchSourceFn = func(initOptions) (initCatchSource, error) {
		return initCatchSource{useGithub: true, reason: "using GitHub release"}, nil
	}
	verifyInitSSHFn = func(*initUI, string) error { return nil }
	detectInitHostFn = func(*initUI, string) (string, string, error) { return "Linux", "amd64", nil }
	downloadInitCatchFn = func(*initUI, string, string, string, bool, string) error { return nil }
	buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource) error { return nil }
	chmodInitCatchFn = func(*initUI, string) error { return nil }
	installInitCatchFn = func(*initUI, string, bool, bool, bool, string, string, []string) error { return nil }
	prepareInitDockerInstallFn = func(_ *initUI, _ string, opts initOptions) (bool, error) {
		return opts.installDocker, nil
	}
	prepareInitVMToolsInstallFn = func(_ *initUI, _ string, _ string, opts initOptions) (bool, error) {
		return opts.installVMTools, nil
	}

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
		downloadInitCatchFn = func(*initUI, string, string, string, bool, string) error {
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
		installInitCatchFn = func(_ *initUI, _ string, useSudo bool, installDocker bool, installVMTools bool, _ string, _ string, _ []string) error {
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

func overrideInitInstallTiming(t *testing.T, pollInterval, installTimeout, sshTimeout time.Duration) func() {
	t.Helper()
	oldPollInterval := initInstallPollInterval
	oldInstallTimeout := initInstallTimeout
	oldSSHTimeout := initInstallSSHTimeout
	initInstallPollInterval = pollInterval
	initInstallTimeout = installTimeout
	initInstallSSHTimeout = sshTimeout
	return func() {
		initInstallPollInterval = oldPollInterval
		initInstallTimeout = oldInstallTimeout
		initInstallSSHTimeout = oldSSHTimeout
	}
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
