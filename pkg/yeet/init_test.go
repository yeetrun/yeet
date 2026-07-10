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
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseInitArgs(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantPos         []string
		wantGithub      bool
		wantNightly     bool
		wantDocker      bool
		wantVMTools     bool
		wantTSAuth      string
		wantTSClient    string
		wantWorkspace   string
		wantNoWorkspace bool
		wantStorage     initStorageOptions
		wantErr         bool
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
			name:    "parses storage flags",
			args:    []string{"--data-dir=flash/yeet/data", "--services-root=flash/yeet/services", "--zfs", "root@example.com"},
			wantPos: []string{"root@example.com"},
			wantStorage: initStorageOptions{
				DataDir:         "flash/yeet/data",
				DataDirZFS:      true,
				ServicesRoot:    "flash/yeet/services",
				ServicesRootZFS: true,
			},
		},
		{
			name:          "parses workspace flag",
			args:          []string{"--workspace", "~/yeet-services", "root@example.com"},
			wantPos:       []string{"root@example.com"},
			wantWorkspace: "~/yeet-services",
		},
		{
			name:            "parses no workspace flag",
			args:            []string{"--no-workspace", "root@example.com"},
			wantPos:         []string{"root@example.com"},
			wantNoWorkspace: true,
		},
		{
			name:    "rejects workspace and no workspace together",
			args:    []string{"--workspace", "~/yeet-services", "--no-workspace", "root@example.com"},
			wantErr: true,
		},
		{
			name:    "rejects zfs without storage target",
			args:    []string{"--zfs", "root@example.com"},
			wantErr: true,
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
			if opts.workspace != tt.wantWorkspace {
				t.Fatalf("workspace = %q, want %q", opts.workspace, tt.wantWorkspace)
			}
			if opts.noWorkspace != tt.wantNoWorkspace {
				t.Fatalf("noWorkspace = %v, want %v", opts.noWorkspace, tt.wantNoWorkspace)
			}
			if !reflect.DeepEqual(opts.storage, tt.wantStorage) {
				t.Fatalf("storage = %#v, want %#v", opts.storage, tt.wantStorage)
			}
		})
	}
}

func TestFinishInitWorkspaceSetupExplicitWorkspaceCreatesSeedAndSavesConfig(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "services")
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "catch"})
	defer restore()
	oldConfigPath := clientConfigPathFn
	clientConfigPathFn = func() (string, error) { return filepath.Join(tmp, "config.toml"), nil }
	t.Cleanup(func() { clientConfigPathFn = oldConfigPath })
	loadedPrefs.DefaultHost = "yeet-lab"

	ui := newInitUI(io.Discard, false, true, "yeet-lab", "root@example.com", catchServiceName)
	if err := finishInitWorkspaceSetup(ui, initOptions{workspace: workspace}); err != nil {
		t.Fatalf("finishInitWorkspaceSetup error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile yeet.toml: %v", err)
	}
	if !strings.Contains(string(raw), `hosts = ["yeet-lab"]`) {
		t.Fatalf("yeet.toml = %q, want host", string(raw))
	}
	configRaw, err := os.ReadFile(filepath.Join(tmp, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if !strings.Contains(string(configRaw), `default_host = "yeet-lab"`) || !strings.Contains(string(configRaw), workspace) {
		t.Fatalf("config = %q, want host and workspace", string(configRaw))
	}
}

func TestFinishInitWorkspaceSetupNoWorkspaceSkipsPrompt(t *testing.T) {
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab"})
	defer restore()
	oldPrompt := activePrompter
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	t.Cleanup(func() { activePrompter = oldPrompt })
	ui := newInitUI(io.Discard, false, true, "yeet-lab", "root@example.com", catchServiceName)

	if err := finishInitWorkspaceSetup(ui, initOptions{noWorkspace: true}); err != nil {
		t.Fatalf("finishInitWorkspaceSetup error: %v", err)
	}
}

func TestFinishInitWorkspaceSetupNoWorkspaceCanSuppressNextSteps(t *testing.T) {
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab"})
	defer restore()
	var out bytes.Buffer
	ui := newInitUI(&out, false, false, "yeet-lab", "root@example.com", catchServiceName)

	if err := finishInitWorkspaceSetup(ui, initOptions{noWorkspace: true, suppressNextSteps: true}); err != nil {
		t.Fatalf("finishInitWorkspaceSetup error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("output = %q, want no next-step guidance", got)
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

type scriptedInitPrompter struct {
	confirmAnswers []bool
	inputAnswers   []string
	secretAnswers  []string
	confirmErr     error
	inputErr       error
	secretErr      error
	confirmPrompts []string
	inputPrompts   []string
	secretPrompts  []string
}

func (p *scriptedInitPrompter) Confirm(msg string, _ bool) (bool, error) {
	p.confirmPrompts = append(p.confirmPrompts, msg)
	if p.confirmErr != nil {
		return false, p.confirmErr
	}
	if len(p.confirmAnswers) == 0 {
		return false, nil
	}
	answer := p.confirmAnswers[0]
	p.confirmAnswers = p.confirmAnswers[1:]
	return answer, nil
}

func (p *scriptedInitPrompter) Input(msg string, _ string) (string, error) {
	p.inputPrompts = append(p.inputPrompts, msg)
	if p.inputErr != nil {
		return "", p.inputErr
	}
	if len(p.inputAnswers) == 0 {
		return "", nil
	}
	answer := p.inputAnswers[0]
	p.inputAnswers = p.inputAnswers[1:]
	return answer, nil
}

func (p *scriptedInitPrompter) Secret(msg string) (string, error) {
	p.secretPrompts = append(p.secretPrompts, msg)
	if p.secretErr != nil {
		return "", p.secretErr
	}
	if len(p.secretAnswers) == 0 {
		return "", nil
	}
	answer := p.secretAnswers[0]
	p.secretAnswers = p.secretAnswers[1:]
	return answer, nil
}

func (*scriptedInitPrompter) SelectWorkspace(string, []string, string) (workspaceSelection, error) {
	return workspaceSelection{}, nil
}

func (*scriptedInitPrompter) SelectDefaultHost([]string, string) (string, error) {
	return "", nil
}

func TestPrepareInitVMToolsInstallPromptsForInteractiveCapableAptHost(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldPrompt := activePrompter
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		activePrompter = oldPrompt
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
	prompter := &scriptedInitPrompter{confirmAnswers: []bool{true}}
	activePrompter = prompter
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if !installVMTools {
		t.Fatal("installVMTools = false after prompt confirmation")
	}
	wantPrompt := "VM payloads can run on this host, but VM host packages are missing. Install them now?"
	if !reflect.DeepEqual(prompter.confirmPrompts, []string{wantPrompt}) {
		t.Fatalf("prompts = %#v, want %#v", prompter.confirmPrompts, []string{wantPrompt})
	}
}

func TestPrepareInitVMToolsInstallWarnsWithFlagWhenCapableAptHostIsNonInteractive(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldPrompt := activePrompter
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		activePrompter = oldPrompt
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
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
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
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{}, errors.New("probe failed")
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
	oldPrompt := activePrompter
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		activePrompter = oldPrompt
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		t.Fatal("remoteVMHostStatusFn should not be called when --install-vm-tools is set")
		return initVMHostStatus{}, nil
	}
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{installVMTools: true})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if !installVMTools {
		t.Fatal("installVMTools = false with explicit --install-vm-tools")
	}
}

func TestPrepareInitVMToolsInstallMarksNonVMCapableHostUnavailableWithoutWarning(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldPrompt := activePrompter
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		activePrompter = oldPrompt
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
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	var out bytes.Buffer
	ui := newInitUI(&out, true, false, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	ui.Stop()
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if installVMTools {
		t.Fatal("installVMTools = true for non-VM-capable host")
	}
	got := out.String()
	if !strings.Contains(got, "Check VM tools (not available)") {
		t.Fatalf("output = %q, want unavailable VM-tools status", got)
	}
	if strings.Contains(got, "/dev/kvm is missing") {
		t.Fatalf("output = %q, want capability warning deferred to installer", got)
	}
}

func TestInitVMWarningsAppearOnceAcrossPreflightAndInstaller(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldPrompt := activePrompter
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		activePrompter = oldPrompt
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
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	var out bytes.Buffer
	ui := newInitUI(&out, true, false, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if installVMTools {
		t.Fatal("installVMTools = true for non-VM-capable host")
	}

	filter := newInitInstallFilter(io.Discard)
	installerOutput := strings.Join([]string{
		"Warning: VM support is unavailable on this host: /dev/kvm is missing. Containers, binaries, and cron jobs still work. See " + hostRequirementsDocsURL,
		"Warning: VM tools are incomplete: missing qemu-img. Install packages: qemu-utils. See " + hostRequirementsDocsURL,
	}, "\n") + "\n"
	if _, err := filter.Write([]byte(installerOutput)); err != nil {
		t.Fatalf("write installer output: %v", err)
	}
	ui.Warn(filter.WarningSummary())
	ui.Stop()

	got := out.String()
	if !strings.Contains(got, "Check VM tools (not available)") {
		t.Fatalf("output = %q, want unavailable VM-tools status", got)
	}
	for text, wantCount := range map[string]int{
		"/dev/kvm is missing":   1,
		"missing qemu-img":      1,
		hostRequirementsDocsURL: 1,
	} {
		if gotCount := strings.Count(got, text); gotCount != wantCount {
			t.Fatalf("strings.Count(output, %q) = %d, want %d\n%s", text, gotCount, wantCount, got)
		}
	}
}

func TestPrepareInitStorageOptionsKeepsExplicitFlags(t *testing.T) {
	oldExisting := remoteInitExistingCatchStorageFn
	oldWizard := runInitStorageWizardFn
	t.Cleanup(func() {
		remoteInitExistingCatchStorageFn = oldExisting
		runInitStorageWizardFn = oldWizard
	})
	remoteInitExistingCatchStorageFn = func(string) (initStorageOptions, bool, error) {
		t.Fatal("remoteInitExistingCatchStorageFn should not be called for explicit storage")
		return initStorageOptions{}, false, nil
	}
	runInitStorageWizardFn = func(io.Reader, io.Writer, initStorageProbe) (initStorageOptions, error) {
		t.Fatal("runInitStorageWizardFn should not be called for explicit storage")
		return initStorageOptions{}, nil
	}
	want := initStorageOptions{DataDir: "/srv/yeet-data"}

	got, err := prepareInitStorageOptions(newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName), "root@example.com", false, initOptions{storage: want})
	if err != nil {
		t.Fatalf("prepareInitStorageOptions: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage = %#v, want %#v", got, want)
	}
}

func TestPrepareInitStorageOptionsPreservesExistingCatchStorage(t *testing.T) {
	oldExisting := remoteInitExistingCatchStorageFn
	oldWizard := runInitStorageWizardFn
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteInitExistingCatchStorageFn = oldExisting
		runInitStorageWizardFn = oldWizard
		isTerminalFn = oldIsTerminal
	})
	want := initStorageOptions{
		DataDir:      "/root/data",
		ServicesRoot: "/srv/yeet-services",
	}
	remoteInitExistingCatchStorageFn = func(string) (initStorageOptions, bool, error) {
		return want, true, nil
	}
	runInitStorageWizardFn = func(io.Reader, io.Writer, initStorageProbe) (initStorageOptions, error) {
		t.Fatal("runInitStorageWizardFn should not be called for existing catch")
		return initStorageOptions{}, nil
	}
	isTerminalFn = func(int) bool { return true }

	got, err := prepareInitStorageOptions(newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName), "root@example.com", false, initOptions{})
	if err != nil {
		t.Fatalf("prepareInitStorageOptions: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage = %#v, want existing catch storage %#v", got, want)
	}
}

func TestPrepareInitStorageOptionsPromptsForFreshInteractiveHost(t *testing.T) {
	oldExisting := remoteInitExistingCatchStorageFn
	oldProbe := remoteInitStorageProbeFn
	oldWizard := runInitStorageWizardFn
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		remoteInitExistingCatchStorageFn = oldExisting
		remoteInitStorageProbeFn = oldProbe
		runInitStorageWizardFn = oldWizard
		isTerminalFn = oldIsTerminal
	})
	remoteInitExistingCatchStorageFn = func(string) (initStorageOptions, bool, error) {
		return initStorageOptions{}, false, nil
	}
	remoteInitStorageProbeFn = func(userAtRemote string, useSudo bool) (initStorageProbe, error) {
		if userAtRemote != "root@example.com" || useSudo {
			t.Fatalf("probe args = %q/%v, want root@example.com/false", userAtRemote, useSudo)
		}
		return initStorageProbe{Home: "/root", ZFSAvailable: true, SuggestedZFSPrefix: "flash/yeet"}, nil
	}
	want := initStorageOptions{DataDir: "flash/yeet/data", DataDirZFS: true}
	runInitStorageWizardFn = func(_ io.Reader, _ io.Writer, probe initStorageProbe) (initStorageOptions, error) {
		if probe.Home != "/root" || !probe.ZFSAvailable || probe.SuggestedZFSPrefix != "flash/yeet" {
			t.Fatalf("probe = %#v, want remote storage probe", probe)
		}
		return want, nil
	}
	isTerminalFn = func(int) bool { return true }

	got, err := prepareInitStorageOptions(newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName), "root@example.com", false, initOptions{})
	if err != nil {
		t.Fatalf("prepareInitStorageOptions: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage = %#v, want %#v", got, want)
	}
}

func TestInitStorageOptionsFromCatchUnit(t *testing.T) {
	unit := strings.Join([]string{
		"[Unit]",
		"[Service]",
		"ExecStart=/root/data/services/catch/run/catch --data-dir=/root/data --services-root=/srv/yeet-services --tsnet-host=yeet-lab",
	}, "\n")

	got := initStorageOptionsFromCatchUnit(unit)
	want := initStorageOptions{
		DataDir:      "/root/data",
		ServicesRoot: "/srv/yeet-services",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage = %#v, want %#v", got, want)
	}
}

func TestRunInitStorageWizardDefaultsToHomeYeetData(t *testing.T) {
	got, err := runInitStorageWizardWithPrompter(&scriptedInitPrompter{confirmAnswers: []bool{true, true}}, initStorageProbe{Home: "/root"})
	if err != nil {
		t.Fatalf("runInitStorageWizard: %v", err)
	}
	want := initStorageOptions{DataDir: "/root/yeet-data"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage = %#v, want %#v", got, want)
	}
}

func TestRunInitStorageWizardCanSelectZFSDataAndServicesDatasets(t *testing.T) {
	prompter := &scriptedInitPrompter{
		confirmAnswers: []bool{false, true, false, true},
		inputAnswers:   []string{"flash/yeet/data", "flash/yeet/services"},
	}
	got, err := runInitStorageWizardWithPrompter(prompter, initStorageProbe{
		Home:               "/root",
		ZFSAvailable:       true,
		SuggestedZFSPrefix: "flash/yeet",
	})
	if err != nil {
		t.Fatalf("runInitStorageWizard: %v", err)
	}
	want := initStorageOptions{
		DataDir:         "flash/yeet/data",
		DataDirZFS:      true,
		ServicesRoot:    "flash/yeet/services",
		ServicesRootZFS: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage = %#v, want %#v", got, want)
	}
	for _, wantPrompt := range []string{
		"Use /root/yeet-data for catch data?",
		"Use a ZFS dataset for catch data?",
		"Keep services under the catch data dir?",
		"Use a ZFS dataset for services?",
	} {
		if !slices.Contains(prompter.confirmPrompts, wantPrompt) {
			t.Fatalf("confirm prompts = %#v, want %q", prompter.confirmPrompts, wantPrompt)
		}
	}
	for _, wantPrompt := range []string{"Catch data dataset", "Services dataset"} {
		if !slices.Contains(prompter.inputPrompts, wantPrompt) {
			t.Fatalf("input prompts = %#v, want %q", prompter.inputPrompts, wantPrompt)
		}
	}
}

func TestRunInitStorageWizardNonZFSHostUsesFilesystemServicesRoot(t *testing.T) {
	prompter := &scriptedInitPrompter{
		confirmAnswers: []bool{true, false},
		inputAnswers:   []string{"/srv/custom-services"},
	}
	got, err := runInitStorageWizardWithPrompter(prompter, initStorageProbe{
		Home:         "/root",
		ZFSAvailable: false,
	})
	if err != nil {
		t.Fatalf("runInitStorageWizard: %v", err)
	}
	want := initStorageOptions{
		DataDir:      "/root/yeet-data",
		ServicesRoot: "/srv/custom-services",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(prompter.confirmPrompts, []string{
		"Use /root/yeet-data for catch data?",
		"Keep services under the catch data dir?",
	}) {
		t.Fatalf("confirm prompts = %#v", prompter.confirmPrompts)
	}
	if !reflect.DeepEqual(prompter.inputPrompts, []string{"Services root"}) {
		t.Fatalf("input prompts = %#v, want filesystem services root prompt only", prompter.inputPrompts)
	}
	if got.ServicesRootZFS {
		t.Fatalf("ServicesRootZFS = true, want false")
	}
}

func TestRetryInitTailscaleClientSecretUsesPromptLayer(t *testing.T) {
	oldPrompt := activePrompter
	oldIsTerminal := isTerminalFn
	activePrompter = fakePrompter{secret: "tskey-client-new"}
	isTerminalFn = func(int) bool { return true }
	t.Cleanup(func() {
		activePrompter = oldPrompt
		isTerminalFn = oldIsTerminal
	})
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	secret, err := retryInitTailscaleClientSecret(ui, 1, errors.New("tailscale OAuth setup failed"))
	if err != nil {
		t.Fatalf("retryInitTailscaleClientSecret error: %v", err)
	}
	if secret != "tskey-client-new" {
		t.Fatalf("secret = %q, want prompted secret", secret)
	}
}

func TestRemoteInitStorageProbeDetectsHomeAndZFS(t *testing.T) {
	oldOK := remoteInitStorageCommandOKFn
	oldOutput := remoteInitStorageOutputFn
	t.Cleanup(func() {
		remoteInitStorageCommandOKFn = oldOK
		remoteInitStorageOutputFn = oldOutput
	})
	remoteInitStorageCommandOKFn = func(userAtRemote, script string) (bool, error) {
		if userAtRemote != "root@example.com" || script != "command -v zfs >/dev/null 2>&1" {
			t.Fatalf("command ok args = %q/%q", userAtRemote, script)
		}
		return true, nil
	}
	remoteInitStorageOutputFn = func(_ string, script string) (string, error) {
		switch script {
		case "printf '%s\\n' \"$HOME\"":
			return "/home/admin\n", nil
		case "zfs list -H -d 0 -o name -t filesystem 2>/dev/null | head -n 1":
			return "flash\n", nil
		default:
			t.Fatalf("unexpected script %q", script)
			return "", nil
		}
	}

	got, err := remoteInitStorageProbe("root@example.com", false)
	if err != nil {
		t.Fatalf("remoteInitStorageProbe: %v", err)
	}
	want := initStorageProbe{Home: "/home/admin", ZFSAvailable: true, SuggestedZFSPrefix: "flash/yeet"}
	if got != want {
		t.Fatalf("probe = %#v, want %#v", got, want)
	}
}

func TestRemoteInitStorageProbeUsesRootHomeForSudoWithoutZFS(t *testing.T) {
	oldOK := remoteInitStorageCommandOKFn
	oldOutput := remoteInitStorageOutputFn
	t.Cleanup(func() {
		remoteInitStorageCommandOKFn = oldOK
		remoteInitStorageOutputFn = oldOutput
	})
	remoteInitStorageOutputFn = func(_, script string) (string, error) {
		t.Fatalf("remoteInitStorageOutputFn should not be called for sudo home, got %q", script)
		return "", nil
	}
	remoteInitStorageCommandOKFn = func(_, script string) (bool, error) {
		if script != "command -v zfs >/dev/null 2>&1" {
			t.Fatalf("script = %q, want zfs command probe", script)
		}
		return false, nil
	}

	got, err := remoteInitStorageProbe("admin@example.com", true)
	if err != nil {
		t.Fatalf("remoteInitStorageProbe: %v", err)
	}
	want := initStorageProbe{Home: "/root"}
	if got != want {
		t.Fatalf("probe = %#v, want %#v", got, want)
	}
}

func TestSuggestedInitServicesRootPath(t *testing.T) {
	tests := []struct {
		name    string
		storage initStorageOptions
		probe   initStorageProbe
		want    string
	}{
		{
			name:    "filesystem data dir sibling",
			storage: initStorageOptions{DataDir: "/srv/yeet-data"},
			probe:   initStorageProbe{Home: "/root"},
			want:    "/srv/yeet-services",
		},
		{
			name:    "zfs data dir falls back to home",
			storage: initStorageOptions{DataDir: "flash/yeet/data", DataDirZFS: true},
			probe:   initStorageProbe{Home: "/root"},
			want:    "/root/yeet-services",
		},
		{
			name:    "missing data dir uses home",
			storage: initStorageOptions{},
			probe:   initStorageProbe{Home: "/home/admin"},
			want:    "/home/admin/yeet-services",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := suggestedInitServicesRootPath(tt.storage, tt.probe); got != tt.want {
				t.Fatalf("suggestedInitServicesRootPath = %q, want %q", got, tt.want)
			}
		})
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

func TestParseInitVMLANBridgePlan(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want initVMLANBridgePlan
	}{
		{
			name: "needs prepare",
			in:   "VM LAN bridge plan: bridge=br0 parent=eno1 ready=false needs_prepare=true reason=default route is on a supported physical LAN uplink\n",
			want: initVMLANBridgePlan{
				Bridge:       "br0",
				Parent:       "eno1",
				Ready:        false,
				NeedsPrepare: true,
				Reason:       "default route is on a supported physical LAN uplink",
			},
		},
		{
			name: "ready",
			in:   "debug line\nVM LAN bridge plan: bridge=br0 parent=eno1 ready=true needs_prepare=false reason=default route is already on a bridge\n",
			want: initVMLANBridgePlan{
				Bridge:       "br0",
				Parent:       "eno1",
				Ready:        true,
				NeedsPrepare: false,
				Reason:       "default route is already on a bridge",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseInitVMLANBridgePlan(tt.in)
			if err != nil {
				t.Fatalf("parseInitVMLANBridgePlan: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("plan = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseInitVMLANBridgePlanRejectsMalformedOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "missing prefix",
			in:   "bridge=br0 parent=eno1 ready=true needs_prepare=false reason=ready",
			want: "missing",
		},
		{
			name: "malformed field",
			in:   "VM LAN bridge plan: bridge=br0 parent ready=true needs_prepare=false reason=ready",
			want: "malformed field",
		},
		{
			name: "bad bool",
			in:   "VM LAN bridge plan: bridge=br0 parent=eno1 ready=yes needs_prepare=false reason=ready",
			want: "ready",
		},
		{
			name: "unknown field",
			in:   "VM LAN bridge plan: bridge=br0 parent=eno1 ready=true unexpected=false needs_prepare=false reason=ready",
			want: "unknown field",
		},
		{
			name: "missing required bool",
			in:   "VM LAN bridge plan: bridge=br0 parent=eno1 ready=true reason=ready",
			want: "missing ready or needs_prepare",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseInitVMLANBridgePlan(tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseInitVMLANBridgePlan error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRemoteVMLANBridgePlanUsesConfiguredCatchStagingBinaryAndStorage(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "ssh.log")
	fakeSSHInPath(t, strings.Join([]string{
		"printf '%s\\n' \"$*\" > " + strconvQuoteForShell(logFile),
		"printf 'VM LAN bridge plan: bridge=br0 parent=eth0 ready=true needs_prepare=false reason=ready\\n'",
	}, "\n"))
	storage := withInitCatchRemoteBinary(initStorageOptions{
		DataDir:      "/flash/yeet/data",
		ServicesRoot: "/flash/yeet/services",
	}, false)

	plan, err := remoteVMLANBridgePlan("root@example.com", storage)
	if err != nil {
		t.Fatalf("remoteVMLANBridgePlan: %v", err)
	}
	if !plan.Ready || plan.Bridge != "br0" || plan.Parent != "eth0" {
		t.Fatalf("plan = %#v", plan)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile ssh log: %v", err)
	}
	want := "root@example.com " + storage.remoteCatchBinary + " --data-dir=/flash/yeet/data --services-root=/flash/yeet/services vm-lan-bridge-plan"
	if got := strings.TrimSpace(string(b)); got != want {
		t.Fatalf("ssh args = %q, want %q", got, want)
	}
}

func TestInitCatchPassesInstallDockerFlagToRemoteInstall(t *testing.T) {
	var steps []string
	configureSteps := stubInitCatchWorkflow(t)
	configureSteps(&steps)
	installInitCatchFn = func(_ *initUI, _ string, _ bool, installDocker bool, installVMTools bool, _ string, _ string, _ []string, prepareVMLANBridge bool, skipVMLANBridge bool, _ initStorageOptions) error {
		steps = append(steps, "install-docker:"+boolString(installDocker)+":install-vm-tools:"+boolString(installVMTools)+":prepare-vm-lan-bridge:"+boolString(prepareVMLANBridge)+":skip-vm-lan-bridge:"+boolString(skipVMLANBridge))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, installDocker: true, installVMTools: true, prepareVMLANBridge: true, noWorkspace: true}); err != nil {
		t.Fatalf("initCatch returned error: %v", err)
	}
	if got := steps[len(steps)-1]; got != "install-docker:true:install-vm-tools:true:prepare-vm-lan-bridge:true:skip-vm-lan-bridge:false" {
		t.Fatalf("last step = %q, want install flags true; steps=%#v", got, steps)
	}
}

func TestInitCatchPlansStorageBeforeDownloadAndPassesToRemoteInstall(t *testing.T) {
	var steps []string
	configureSteps := stubInitCatchWorkflow(t)
	configureSteps(&steps)
	wantStorage := initStorageOptions{
		DataDir:      "/srv/yeet-data",
		ServicesRoot: "/srv/yeet-services",
	}
	prepareInitStorageOptionsFn = func(_ *initUI, userAtRemote string, useSudo bool, opts initOptions) (initStorageOptions, error) {
		steps = append(steps, "storage:"+userAtRemote+":"+boolString(useSudo)+":"+opts.storage.DataDir)
		return wantStorage, nil
	}
	installInitCatchFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ string, _ string, _ []string, _ bool, _ bool, storage initStorageOptions) error {
		if storage.DataDir != wantStorage.DataDir || storage.ServicesRoot != wantStorage.ServicesRoot {
			t.Fatalf("storage = %#v, want data/services from %#v", storage, wantStorage)
		}
		if !strings.HasPrefix(storage.remoteCatchBinary, "/srv/yeet-services/catch/run/catch.install.") {
			t.Fatalf("remote catch binary = %q, want unique staging path under services root", storage.remoteCatchBinary)
		}
		steps = append(steps, "install-storage:"+storage.DataDir+":"+storage.ServicesRoot)
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, storage: initStorageOptions{DataDir: "/flag/data"}, noWorkspace: true}); err != nil {
		t.Fatalf("initCatch returned error: %v", err)
	}

	want := []string{
		"verify",
		"detect",
		"storage:root@example.com:false:/flag/data",
		"download",
		"chmod",
		"install-storage:/srv/yeet-data:/srv/yeet-services",
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestInitCatchPromptsForVMLANBridgeAfterChmodAndPassesPrepareIntent(t *testing.T) {
	var steps []string
	configureSteps := stubInitCatchWorkflow(t)
	configureSteps(&steps)
	isTerminalFn = func(int) bool { return true }
	remoteVMHostStatusFn = func(userAtRemote string, goarch string) (initVMHostStatus, error) {
		steps = append(steps, "vm-status:"+userAtRemote+":"+goarch)
		return initVMHostStatus{KVM: true, TUN: true}, nil
	}
	remoteVMLANBridgePlanFn = func(userAtRemote string, _ initStorageOptions) (initVMLANBridgePlan, error) {
		steps = append(steps, "bridge-plan:"+userAtRemote)
		return initVMLANBridgePlan{Bridge: "br0", Parent: "eno1", NeedsPrepare: true}, nil
	}
	oldPrompt := activePrompter
	activePrompter = &scriptedInitPrompter{
		confirmAnswers: []bool{true},
	}
	t.Cleanup(func() { activePrompter = oldPrompt })
	prompter := activePrompter.(*scriptedInitPrompter)
	installInitCatchFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ string, _ string, _ []string, prepareVMLANBridge bool, skipVMLANBridge bool, _ initStorageOptions) error {
		for _, msg := range prompter.confirmPrompts {
			steps = append(steps, "confirm:"+msg)
		}
		steps = append(steps, "install:prepare="+boolString(prepareVMLANBridge)+":skip="+boolString(skipVMLANBridge))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, installVMTools: true, noWorkspace: true}); err != nil {
		t.Fatalf("initCatch returned error: %v", err)
	}

	want := []string{
		"verify",
		"detect",
		"download",
		"chmod",
		"vm-status:root@example.com:amd64",
		"bridge-plan:root@example.com",
		"confirm:Prepare br0 for VM LAN networking during init?",
		"install:prepare=true:skip=false",
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestInitCatchVMLANBridgeDeclinePassesSkipIntent(t *testing.T) {
	var steps []string
	configureSteps := stubInitCatchWorkflow(t)
	configureSteps(&steps)
	isTerminalFn = func(int) bool { return true }
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{KVM: true, TUN: true}, nil
	}
	remoteVMLANBridgePlanFn = func(string, initStorageOptions) (initVMLANBridgePlan, error) {
		return initVMLANBridgePlan{Bridge: "br0", Parent: "eno1", NeedsPrepare: true}, nil
	}
	oldPrompt := activePrompter
	activePrompter = fakePrompter{confirm: false}
	t.Cleanup(func() { activePrompter = oldPrompt })
	installInitCatchFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ string, _ string, _ []string, prepareVMLANBridge bool, skipVMLANBridge bool, _ initStorageOptions) error {
		steps = append(steps, "install:prepare="+boolString(prepareVMLANBridge)+":skip="+boolString(skipVMLANBridge))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, installVMTools: true, noWorkspace: true}); err != nil {
		t.Fatalf("initCatch returned error: %v", err)
	}
	if got := steps[len(steps)-1]; got != "install:prepare=false:skip=true" {
		t.Fatalf("last step = %q, want skip intent; steps=%#v", got, steps)
	}
}

func TestInitCatchVMLANBridgeReadyPlanDoesNotPrompt(t *testing.T) {
	var steps []string
	configureSteps := stubInitCatchWorkflow(t)
	configureSteps(&steps)
	isTerminalFn = func(int) bool { return true }
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{KVM: true, TUN: true}, nil
	}
	remoteVMLANBridgePlanFn = func(string, initStorageOptions) (initVMLANBridgePlan, error) {
		steps = append(steps, "bridge-plan")
		return initVMLANBridgePlan{Bridge: "br0", Ready: true}, nil
	}
	oldPrompt := activePrompter
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	t.Cleanup(func() { activePrompter = oldPrompt })
	installInitCatchFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ string, _ string, _ []string, prepareVMLANBridge bool, skipVMLANBridge bool, _ initStorageOptions) error {
		steps = append(steps, "install:prepare="+boolString(prepareVMLANBridge)+":skip="+boolString(skipVMLANBridge))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, installVMTools: true, noWorkspace: true}); err != nil {
		t.Fatalf("initCatch returned error: %v", err)
	}
	if got := steps[len(steps)-1]; got != "install:prepare=false:skip=false" {
		t.Fatalf("last step = %q, want no bridge intent; steps=%#v", got, steps)
	}
}

func TestInitCatchVMLANBridgePlanFailureWarnsAndDoesNotBlockInstall(t *testing.T) {
	var steps []string
	configureSteps := stubInitCatchWorkflow(t)
	configureSteps(&steps)
	isTerminalFn = func(int) bool { return true }
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{KVM: true, TUN: true}, nil
	}
	remoteVMLANBridgePlanFn = func(string, initStorageOptions) (initVMLANBridgePlan, error) {
		return initVMLANBridgePlan{}, errors.New("unsupported renderer")
	}
	oldPrompt := activePrompter
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	t.Cleanup(func() { activePrompter = oldPrompt })
	installInitCatchFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ string, _ string, _ []string, prepareVMLANBridge bool, skipVMLANBridge bool, _ initStorageOptions) error {
		steps = append(steps, "install:prepare="+boolString(prepareVMLANBridge)+":skip="+boolString(skipVMLANBridge))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, installVMTools: true, noWorkspace: true}); err != nil {
		t.Fatalf("initCatch returned error: %v", err)
	}
	if got := steps[len(steps)-1]; got != "install:prepare=false:skip=true" {
		t.Fatalf("last step = %q, want install with skip intent; steps=%#v", got, steps)
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
		prepareBridge  bool
		skipBridge     bool
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
			got := remoteCatchInstallArgs(tt.userAtRemote, tt.useSudo, tt.installDocker, tt.installVMTools, tt.tsAuthKey, tt.tsClientSecret, tt.tsCatchTags, tt.prepareBridge, tt.skipBridge, initStorageOptions{})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("remoteCatchInstallArgs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRemoteCatchInstallCommandCanRequestVMLANBridgePrep(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch-host"

	args := remoteCatchInstallCommand("root@example.com", false, true, true, "", "", nil, true, false, initStorageOptions{})
	want := []string{
		"env",
		"CATCH_INSTALL_USER=root",
		"CATCH_INSTALL_HOST=example.com",
		"CATCH_INSTALL_DOCKER=1",
		"CATCH_INSTALL_VM_TOOLS=1",
		"CATCH_PREPARE_VM_LAN_BRIDGE=1",
		"./catch",
		"--tsnet-host=catch-host",
		"install",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteCatchInstallCommand = %#v, want %#v", args, want)
	}
}

func TestRemoteCatchInstallCommandCanSkipVMLANBridgePrep(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch-host"

	args := remoteCatchInstallCommand("root@example.com", false, false, true, "", "", nil, false, true, initStorageOptions{})
	want := []string{
		"env",
		"CATCH_INSTALL_USER=root",
		"CATCH_INSTALL_HOST=example.com",
		"CATCH_INSTALL_VM_TOOLS=1",
		"CATCH_SKIP_VM_LAN_BRIDGE=1",
		"./catch",
		"--tsnet-host=catch-host",
		"install",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteCatchInstallCommand = %#v, want %#v", args, want)
	}
}

func TestRemoteCatchInstallCommandIncludesStorageOptions(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch-host"

	storage := withInitCatchRemoteBinary(initStorageOptions{
		DataDir:         "flash/yeet/data",
		DataDirZFS:      true,
		ServicesRoot:    "flash/yeet/services",
		ServicesRootZFS: true,
	}, false)
	args := remoteCatchInstallCommand("root@example.com", false, false, false, "", "", nil, false, false, storage)
	want := []string{
		"env",
		"CATCH_INSTALL_USER=root",
		"CATCH_INSTALL_HOST=example.com",
		"CATCH_INSTALL_DATA_DIR_ZFS=1",
		"CATCH_INSTALL_SERVICES_ROOT_ZFS=1",
		"./catch",
		"--data-dir=flash/yeet/data",
		"--services-root=flash/yeet/services",
		"--tsnet-host=catch-host",
		"install",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteCatchInstallCommand = %#v, want %#v", args, want)
	}
}

func TestRemoteCatchInstallCommandUsesConfiguredCatchStagingBinary(t *testing.T) {
	oldPrefs := loadedPrefs
	defer func() { loadedPrefs = oldPrefs }()
	loadedPrefs.DefaultHost = "catch-host"

	storage := withInitCatchRemoteBinary(initStorageOptions{
		DataDir:      "/flash/yeet/data",
		ServicesRoot: "/flash/yeet/services",
	}, false)
	args := remoteCatchInstallCommand("root@example.com", false, false, false, "", "", nil, false, false, storage)
	want := []string{
		"env",
		"CATCH_INSTALL_USER=root",
		"CATCH_INSTALL_HOST=example.com",
		storage.remoteCatchBinary,
		"--data-dir=/flash/yeet/data",
		"--services-root=/flash/yeet/services",
		"--tsnet-host=catch-host",
		"install",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remoteCatchInstallCommand = %#v, want %#v", args, want)
	}
}

func TestWithInitCatchRemoteBinaryUsesExistingAbsoluteServiceRoot(t *testing.T) {
	storage := withInitCatchRemoteBinary(initStorageOptions{
		DataDir:      "/flash/yeet/data",
		ServicesRoot: "/flash/yeet/services",
	}, false)
	wantPrefix := "/flash/yeet/services/catch/run/catch.install."
	if !strings.HasPrefix(storage.remoteCatchBinary, wantPrefix) {
		t.Fatalf("remote catch binary = %q, want prefix %q", storage.remoteCatchBinary, wantPrefix)
	}
}

func TestWithInitCatchRemoteBinaryUsesUniqueCatchInstallerPath(t *testing.T) {
	input := initStorageOptions{
		DataDir:      "/flash/yeet/data",
		ServicesRoot: "/flash/yeet/services",
	}
	first := withInitCatchRemoteBinary(input, false).remoteCatchBinary
	second := withInitCatchRemoteBinary(input, false).remoteCatchBinary
	if first == second {
		t.Fatalf("remote catch binary reused staging path %q", first)
	}
	for _, got := range []string{first, second} {
		if !strings.HasPrefix(got, "/flash/yeet/services/catch/run/catch.install.") {
			t.Fatalf("remote catch binary = %q, want unique catch.install staging path", got)
		}
	}
}

func TestWithInitCatchRemoteBinaryFallsBackForSudoOrDatasets(t *testing.T) {
	for _, tt := range []struct {
		name    string
		storage initStorageOptions
		useSudo bool
	}{
		{
			name:    "sudo",
			storage: initStorageOptions{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
			useSudo: true,
		},
		{
			name:    "zfs dataset",
			storage: initStorageOptions{DataDir: "flash/yeet/data", DataDirZFS: true, ServicesRoot: "flash/yeet/services", ServicesRootZFS: true},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storage := withInitCatchRemoteBinary(tt.storage, tt.useSudo)
			if storage.remoteCatchBinary != "" {
				t.Fatalf("remote catch binary = %q, want default home staging", storage.remoteCatchBinary)
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

	if err := chmodInitCatch(ui, "root@example.com", "./catch"); err != nil {
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

	err := chmodInitCatch(ui, "root@example.com", "./catch")
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

	if err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil, false, false, initStorageOptions{}); err != nil {
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

	err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil, false, false, initStorageOptions{})
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

	err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil, false, false, initStorageOptions{})
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

	if err := installInitCatch(ui, "root@example.com", false, false, false, "", "", nil, false, false, initStorageOptions{}); err != nil {
		t.Fatalf("installInitCatch error: %v", err)
	}
}

func TestInstallInitCatchWithTailscaleRetryPromptsAfterCredentialError(t *testing.T) {
	oldInstall := installInitCatchFn
	oldPrompt := activePrompter
	oldIsTerminal := isTerminalFn
	t.Cleanup(func() {
		installInitCatchFn = oldInstall
		activePrompter = oldPrompt
		isTerminalFn = oldIsTerminal
	})
	activePrompter = fakePrompter{secret: "tskey-client-good"}
	isTerminalFn = func(int) bool { return true }

	var installs int
	var gotSecret string
	installInitCatchFn = func(_ *initUI, _ string, _ bool, _ bool, _ bool, _ string, tsClientSecret string, tsCatchTags []string, _ bool, _ bool, _ initStorageOptions) error {
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
	if installs != 2 {
		t.Fatalf("installs = %d, want 2", installs)
	}
	if gotSecret != "tskey-client-good" {
		t.Fatalf("secret = %q, want prompted secret", gotSecret)
	}
}

func TestWaitDetachedInitCatchInstallStreamsLogsWhileStatusIsPending(t *testing.T) {
	restore := overrideInitInstallTiming(t, 10*time.Millisecond, 2*time.Second, 2*time.Second)
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

func TestWaitDetachedInitCatchInstallToleratesBridgePrepReadFailures(t *testing.T) {
	oldRead := readRemoteInitInstallFileFn
	oldStream := streamRemoteInitInstallLogFn
	defer func() {
		readRemoteInitInstallFileFn = oldRead
		streamRemoteInitInstallLogFn = oldStream
	}()
	var reads int
	readRemoteInitInstallFileFn = func(_ string, path string) (string, error) {
		reads++
		if reads < 3 {
			return "", errors.New("ssh: connection reset")
		}
		if strings.HasSuffix(path, ".status") {
			return "0", nil
		}
		return "Preparing VM LAN bridge\nVM LAN bridge ready\n", nil
	}
	streamRemoteInitInstallLogFn = func(_ string, _ initInstallSession, _ *initInstallFilter, lastLog *string) error {
		*lastLog = "Preparing VM LAN bridge\n"
		return nil
	}

	status, err := waitDetachedInitCatchInstall("root@example.com", initInstallSession{LogPath: "/tmp/x.log", StatusPath: "/tmp/x.status"}, newInitInstallFilter(io.Discard))
	if err != nil {
		t.Fatalf("waitDetachedInitCatchInstall: %v", err)
	}
	if status != "0" {
		t.Fatalf("status = %q, want 0", status)
	}
}

func TestWaitDetachedInitCatchInstallFinalStatusSurfacesLogReadFailure(t *testing.T) {
	oldRead := readRemoteInitInstallFileFn
	oldStream := streamRemoteInitInstallLogFn
	defer func() {
		readRemoteInitInstallFileFn = oldRead
		streamRemoteInitInstallLogFn = oldStream
	}()
	readRemoteInitInstallFileFn = func(_ string, path string) (string, error) {
		if strings.HasSuffix(path, ".status") {
			return "7", nil
		}
		return "", nil
	}
	streamRemoteInitInstallLogFn = func(_ string, _ initInstallSession, _ *initInstallFilter, _ *string) error {
		return errors.New("ssh: connection reset")
	}

	status, err := waitDetachedInitCatchInstall("root@example.com", initInstallSession{LogPath: "/tmp/x.log", StatusPath: "/tmp/x.status"}, newInitInstallFilter(io.Discard))
	if err == nil || !strings.Contains(err.Error(), "ssh: connection reset") {
		t.Fatalf("waitDetachedInitCatchInstall status/error = %q/%v, want log read error", status, err)
	}
}

func TestWaitDetachedInitCatchInstallReportsBridgePrepTimeoutGuidance(t *testing.T) {
	restoreTiming := overrideInitInstallTiming(t, time.Millisecond, 5*time.Millisecond, time.Millisecond)
	defer restoreTiming()
	oldRead := readRemoteInitInstallFileFn
	oldStream := streamRemoteInitInstallLogFn
	defer func() {
		readRemoteInitInstallFileFn = oldRead
		streamRemoteInitInstallLogFn = oldStream
	}()
	readRemoteInitInstallFileFn = func(_ string, _ string) (string, error) {
		return "", nil
	}
	streamRemoteInitInstallLogFn = func(_ string, _ initInstallSession, _ *initInstallFilter, lastLog *string) error {
		*lastLog = "Preparing VM LAN bridge\n"
		return nil
	}

	_, err := waitDetachedInitCatchInstall("root@example.com", initInstallSession{LogPath: "/tmp/x.log", StatusPath: "/tmp/x.status"}, newInitInstallFilter(io.Discard))
	if err == nil || !strings.Contains(err.Error(), "VM LAN bridge preparation may still be finishing; rerun `yeet init root@example.com` to verify or resume setup") {
		t.Fatalf("waitDetachedInitCatchInstall error = %v, want VM LAN bridge timeout guidance", err)
	}
}

func TestWaitDetachedInitCatchInstallBridgePrepWarningUsesNormalTimeout(t *testing.T) {
	restoreTiming := overrideInitInstallTiming(t, time.Millisecond, 5*time.Millisecond, time.Millisecond)
	defer restoreTiming()
	oldRead := readRemoteInitInstallFileFn
	oldStream := streamRemoteInitInstallLogFn
	defer func() {
		readRemoteInitInstallFileFn = oldRead
		streamRemoteInitInstallLogFn = oldStream
	}()
	readRemoteInitInstallFileFn = func(_ string, _ string) (string, error) {
		return "", nil
	}
	streamRemoteInitInstallLogFn = func(_ string, _ initInstallSession, _ *initInstallFilter, lastLog *string) error {
		*lastLog = "Warning: VM LAN bridge br0 needs preparation\n"
		return nil
	}

	_, err := waitDetachedInitCatchInstall("root@example.com", initInstallSession{LogPath: "/tmp/x.log", StatusPath: "/tmp/x.status"}, newInitInstallFilter(io.Discard))
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for catch install to finish") {
		t.Fatalf("waitDetachedInitCatchInstall error = %v, want normal timeout", err)
	}
	if strings.Contains(err.Error(), "VM LAN bridge preparation may still be finishing") {
		t.Fatalf("waitDetachedInitCatchInstall error = %v, should not use bridge prep guidance", err)
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
	downloadInitCatchFn = func(_ *initUI, userAtRemote, systemName, goarch string, nightly bool, version string, _ string) error {
		steps = append(steps, strings.Join([]string{"download", userAtRemote, systemName, goarch, boolString(nightly), version}, ":"))
		return nil
	}
	buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource, string) error {
		t.Fatal("buildAndUploadInitCatchFn should not be called for GitHub source")
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, nightly: true, releaseVersion: "v0.6.1", noWorkspace: true}); err != nil {
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
	downloadInitCatchFn = func(*initUI, string, string, string, bool, string, string) error {
		t.Fatal("downloadInitCatchFn should not be called for local source")
		return nil
	}
	buildAndUploadInitCatchFn = func(_ *initUI, userAtRemote, systemName, goarch string, gotSource initCatchSource, _ string) error {
		if gotSource != source {
			t.Fatalf("source = %#v, want %#v", gotSource, source)
		}
		steps = append(steps, strings.Join([]string{"build-upload", userAtRemote, systemName, goarch}, ":"))
		return nil
	}

	if err := initCatch("root@example.com", initOptions{noWorkspace: true}); err != nil {
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

	if err := initCatch("root@example.com", initOptions{noWorkspace: true}); !errors.Is(err, wantErr) {
		t.Fatalf("initCatch error = %v, want %v", err, wantErr)
	}
}

func TestInitCatchStopsWhenInstallPrepFails(t *testing.T) {
	restore := stubInitCatchWorkflow(t)
	wantErr := errors.New("chmod failed")
	var steps []string
	restore(&steps)
	chmodInitCatchFn = func(*initUI, string, string) error {
		steps = append(steps, "chmod")
		return wantErr
	}
	installInitCatchFn = func(*initUI, string, bool, bool, bool, string, string, []string, bool, bool, initStorageOptions) error {
		t.Fatal("installInitCatchFn should not be called after chmod failure")
		return nil
	}

	if err := initCatch("root@example.com", initOptions{fromGithub: true, noWorkspace: true}); !errors.Is(err, wantErr) {
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
	oldCleanup := cleanupInitCatchBinaryFn
	oldPrepareDocker := prepareInitDockerInstallFn
	oldPrepareVMTools := prepareInitVMToolsInstallFn
	oldPrepareStorage := prepareInitStorageOptionsFn
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldRemoteVMLANBridgePlan := remoteVMLANBridgePlanFn
	oldPrompt := activePrompter
	oldIsTerminal := isTerminalFn
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
		cleanupInitCatchBinaryFn = oldCleanup
		prepareInitDockerInstallFn = oldPrepareDocker
		prepareInitVMToolsInstallFn = oldPrepareVMTools
		prepareInitStorageOptionsFn = oldPrepareStorage
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		remoteVMLANBridgePlanFn = oldRemoteVMLANBridgePlan
		activePrompter = oldPrompt
		isTerminalFn = oldIsTerminal
	})

	resolveInitCatchSourceFn = func(initOptions) (initCatchSource, error) {
		return initCatchSource{useGithub: true, reason: "using GitHub release"}, nil
	}
	verifyInitSSHFn = func(*initUI, string) error { return nil }
	detectInitHostFn = func(*initUI, string) (string, string, error) { return "Linux", "amd64", nil }
	downloadInitCatchFn = func(*initUI, string, string, string, bool, string, string) error { return nil }
	buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource, string) error { return nil }
	chmodInitCatchFn = func(*initUI, string, string) error { return nil }
	installInitCatchFn = func(*initUI, string, bool, bool, bool, string, string, []string, bool, bool, initStorageOptions) error {
		return nil
	}
	cleanupInitCatchBinaryFn = func(*initUI, string, string) {}
	prepareInitDockerInstallFn = func(_ *initUI, _ string, opts initOptions) (bool, error) {
		return opts.installDocker, nil
	}
	prepareInitVMToolsInstallFn = func(_ *initUI, _ string, _ string, opts initOptions) (bool, error) {
		return opts.installVMTools, nil
	}
	prepareInitStorageOptionsFn = func(_ *initUI, _ string, _ bool, opts initOptions) (initStorageOptions, error) {
		return opts.storage, nil
	}
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{KVM: true, TUN: true}, nil
	}
	remoteVMLANBridgePlanFn = func(string, initStorageOptions) (initVMLANBridgePlan, error) {
		return initVMLANBridgePlan{Ready: true, Bridge: "br0"}, nil
	}
	activePrompter = fakePrompter{confirm: false}
	isTerminalFn = func(int) bool { return false }

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
		downloadInitCatchFn = func(*initUI, string, string, string, bool, string, string) error {
			*steps = append(*steps, "download")
			return nil
		}
		buildAndUploadInitCatchFn = func(*initUI, string, string, string, initCatchSource, string) error {
			*steps = append(*steps, "build-upload")
			return nil
		}
		chmodInitCatchFn = func(*initUI, string, string) error {
			*steps = append(*steps, "chmod")
			return nil
		}
		installInitCatchFn = func(_ *initUI, _ string, useSudo bool, installDocker bool, installVMTools bool, _ string, _ string, _ []string, _ bool, _ bool, _ initStorageOptions) error {
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
