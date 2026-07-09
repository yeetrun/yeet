// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/cli"
	yeetpkg "github.com/yeetrun/yeet/pkg/yeet"
)

func TestRunReturnsFailureWhenCommandReturnsError(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStderr := os.Stderr
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stderr = oldStderr
	})

	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	os.Stderr = stderrFile
	os.Args = []string{"yeet", "status"}
	handleSvcCmdFn = func(args []string) error {
		return errors.New("command failed")
	}

	if got := run(); got != 1 {
		t.Fatalf("run exit code = %d, want 1", got)
	}
	if _, err := stderrFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stderr: %v", err)
	}
	rawStderr, err := os.ReadFile(stderrFile.Name())
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if !strings.Contains(string(rawStderr), "command failed") {
		t.Fatalf("stderr = %q, want command failed", string(rawStderr))
	}
}

func TestRunDispatchesHiddenVMSSHProxyCommand(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldHandleVMSSHProxyFn := handleVMSSHProxyFn
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		handleVMSSHProxyFn = oldHandleVMSSHProxyFn
	})

	var gotArgs []string
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("hidden VM SSH proxy should not route through service command: %v", args)
		return nil
	}
	handleVMSSHProxyFn = func(ctx context.Context, args []string) error {
		gotArgs = append([]string{}, args...)
		return nil
	}
	os.Args = []string{"yeet", "--host=yeet-lab", "_vm-ssh-proxy", "devbox", "192.168.100.12", "22"}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if !reflect.DeepEqual(gotArgs, []string{"_vm-ssh-proxy", "devbox", "192.168.100.12", "22"}) {
		t.Fatalf("proxy args = %#v, want hidden command args", gotArgs)
	}
}

func TestRunServiceSetHelpShowsLeafCommand(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "service", "set", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("service set help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	if !strings.Contains(stdout, "Set service settings") {
		t.Fatalf("stdout = %q, want service set command help", stdout)
	}
	if !strings.Contains(stdout, "yeet [GLOBAL OPTIONS] service set <svc> [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--copy|--empty] [--snapshots=on|off|inherit]") {
		t.Fatalf("stdout = %q, want service set usage", stdout)
	}
	if strings.Contains(stdout, "service COMMAND [ARGS...]") {
		t.Fatalf("stdout = %q, got group help instead of service set help", stdout)
	}
}

func TestServiceHelpShowsGenerationCommands(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "service", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("service help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{
		"service rollback <svc>",
		"service generations <svc>",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want service help to include %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "yeet rollback") {
		t.Fatalf("stdout = %q, did not expect top-level rollback help", stdout)
	}

	serviceGroup := buildGroupHandlers()["service"]
	for _, command := range []string{"rollback", "generations"} {
		if _, ok := serviceGroup.Commands[command]; !ok {
			t.Fatalf("service %s should be registered in group handlers", command)
		}
	}
}

func TestRunRemoveHelpShowsOptionsAndPlainAliases(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "rm", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("remove help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{"ALIASES:", "rm", "-y, --yes", "--clean                  Delete service data", "--clean-config", "--clean-data"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "**Aliases**") {
		t.Fatalf("stdout contains markdown alias block:\n%s", stdout)
	}
	if strings.Count(stdout, "ALIASES:") != 1 {
		t.Fatalf("stdout alias sections = %d, want 1:\n%s", strings.Count(stdout, "ALIASES:"), stdout)
	}
}

func TestRunRemoveHelpAgentShowsOptions(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "rm", "--help-agent"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("remove help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{"# yeet remove Agent Context", "### `--clean`", "--clean-config", "--clean-data"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestCopyHelpMentionsVMGuestCopy(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "copy", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("copy help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{
		"Copy files between local paths and service data or VM guests",
		"[--force-proxy] [-avz] <src>... <dst>",
		"yeet copy ./config.yml svc:data/config.yml",
		"yeet copy ./configs/*.yml devbox:~/configs/",
		`yeet copy devbox:"/var/log/*.log" ./logs/`,
		"yeet copy --force-proxy ./configs/ devbox:~/configs/",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("copy help missing %q\n%s", want, stdout)
		}
	}
}

func TestBuildHelpConfigRegistersConfigAndNotPrefs(t *testing.T) {
	cfg := buildHelpConfig()
	if _, ok := cfg.SubCommands["config"]; !ok {
		t.Fatal("config subcommand missing")
	}
	if _, ok := cfg.SubCommands["prefs"]; ok {
		t.Fatal("prefs subcommand should be removed")
	}
}

func TestRunConfigHostFlagSavesDefaultHost(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir workspace: %v", err)
	}
	cmdArgs := []string{
		"-test.run=TestRunConfigCommandHelper",
		"--",
		"config",
		"--host",
		"Yeet-Lab",
		"--workspace",
		workspace,
	}
	cmd := osexec.Command(os.Args[0], cmdArgs...)
	cmd.Env = configHelperEnv(tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "xdg", "yeet", "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `default_host = "yeet-lab"`) || !strings.Contains(text, workspace) {
		t.Fatalf("config = %q, want saved host and workspace", text)
	}
}

func TestRunConfigCommandHelper(t *testing.T) {
	if os.Getenv("YEET_CONFIG_HELPER") != "1" {
		return
	}

	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	os.Args = append([]string{"yeet"}, argsAfterTestTerminator(os.Args)...)
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("config should not route through service command with args %v", args)
		return nil
	}
	if code := run(); code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
}

func configHelperEnv(tmp string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CATCH_HOST=") ||
			strings.HasPrefix(entry, "HOME=") ||
			strings.HasPrefix(entry, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(entry, "YEET_CONFIG_HELPER=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"YEET_CONFIG_HELPER=1",
		"HOME="+filepath.Join(tmp, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(tmp, "xdg"),
	)
}

func TestSSHHelpMentionsCatchRPCShellBehavior(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "ssh", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("ssh help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	if !strings.Contains(stdout, "Open a catch host shell, a service shell, or a VM guest shell") {
		t.Fatalf("stdout missing ssh shell description:\n%s", stdout)
	}
	if !strings.Contains(stdout, "yeet ssh [OPTIONS] [--force-proxy] [<svc>] [-- <remote-cmd...>]") {
		t.Fatalf("stdout missing ssh usage:\n%s", stdout)
	}
	if strings.Contains(stdout, "ssh-opts") {
		t.Fatalf("stdout still advertises generic ssh options:\n%s", stdout)
	}
}

func TestRunGlobalHelpIncludesUpgrade(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("global help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{"upgrade", "Check for and install yeet/catch updates"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunUpgradeRoutesToLocalHandler(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldHandleUpgradeFn := handleUpgradeFn
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		handleUpgradeFn = oldHandleUpgradeFn
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	os.Args = []string{"yeet", "upgrade", "check"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("upgrade should not use remote handler with args %v", args)
		return nil
	}
	var got []string
	handleUpgradeFn = func(ctx context.Context, args []string) error {
		got = append([]string(nil), args...)
		return nil
	}

	if code := run(); code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
	if !reflect.DeepEqual(got, []string{"upgrade", "check"}) {
		t.Fatalf("upgrade args = %#v, want command args", got)
	}
}

func TestRunUpgradeHelpShowsNightly(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "upgrade", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("upgrade help should not call service handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{
		"[--nightly]",
		"yeet upgrade --nightly",
		"yeet upgrade check --nightly",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunHostSetRoutesToHostHandler(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldHandleHostSetFn := handleHostSetFn
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		handleHostSetFn = oldHandleHostSetFn
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	os.Args = []string{"yeet", "host", "set"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("host set should not route through service command with args %v", args)
		return nil
	}
	var got []string
	handleHostSetFn = func(ctx context.Context, args []string) error {
		got = append([]string(nil), args...)
		return nil
	}

	if code := run(); code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
	if !reflect.DeepEqual(got, []string{"set"}) {
		t.Fatalf("host set args = %#v, want group subcommand args", got)
	}
}

func TestRunHostSetRespectsHostSelection(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		catchHost    string
		wantHost     string
		wantOverride string
	}{
		{
			name:         "global host flag",
			args:         []string{"--host=flag-host", "host", "set"},
			wantHost:     "flag-host",
			wantOverride: "flag-host",
		},
		{
			name:         "catch host environment",
			args:         []string{"host", "set"},
			catchHost:    "env-host",
			wantHost:     "env-host",
			wantOverride: "env-host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdArgs := append([]string{"-test.run=TestRunHostSetHostSelectionHelper", "--"}, tt.args...)
			cmd := osexec.Command(os.Args[0], cmdArgs...)
			cmd.Env = hostSetHelperEnv(tt.catchHost, tt.wantHost, tt.wantOverride)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper failed: %v\n%s", err, out)
			}
		})
	}
}

func TestRunHostSetHostSelectionHelper(t *testing.T) {
	if os.Getenv("YEET_HOST_SET_HELPER") != "1" {
		return
	}

	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldHandleHostSetFn := handleHostSetFn
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		handleHostSetFn = oldHandleHostSetFn
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	os.Args = append([]string{"yeet"}, argsAfterTestTerminator(os.Args)...)
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("host set should not route through service command with args %v", args)
		return nil
	}
	handleHostSetFn = func(ctx context.Context, args []string) error {
		if got, want := yeetpkg.Host(), os.Getenv("YEET_HOST_SET_WANT_HOST"); got != want {
			t.Fatalf("Host = %q, want %q", got, want)
		}
		wantOverride := os.Getenv("YEET_HOST_SET_WANT_OVERRIDE")
		gotOverride, gotOverrideSet := yeetpkg.HostOverride()
		if wantOverride == "" {
			if gotOverrideSet {
				t.Fatalf("HostOverride = %q true, want unset", gotOverride)
			}
			return nil
		}
		if !gotOverrideSet || gotOverride != wantOverride {
			t.Fatalf("HostOverride = %q %v, want %q true", gotOverride, gotOverrideSet, wantOverride)
		}
		return nil
	}

	if code := run(); code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
}

func hostSetHelperEnv(catchHost, wantHost, wantOverride string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CATCH_HOST=") ||
			strings.HasPrefix(entry, "YEET_HOST_SET_HELPER=") ||
			strings.HasPrefix(entry, "YEET_HOST_SET_WANT_HOST=") ||
			strings.HasPrefix(entry, "YEET_HOST_SET_WANT_OVERRIDE=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"YEET_HOST_SET_HELPER=1",
		"YEET_HOST_SET_WANT_HOST="+wantHost,
		"YEET_HOST_SET_WANT_OVERRIDE="+wantOverride,
	)
	if catchHost != "" {
		env = append(env, "CATCH_HOST="+catchHost)
	}
	return env
}

func argsAfterTestTerminator(args []string) []string {
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			return append([]string(nil), args[i+1:]...)
		}
	}
	return nil
}

func TestRunCallsUpdateAdvisoryAfterSuccessfulCommand(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldMaybePrintUpdateAdvisoryFn := maybePrintUpdateAdvisoryFn
	oldProjectHostCountForAdvisoryFn := projectHostCountForAdvisoryFn
	oldIsTerminalFn := isTerminalFn
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		maybePrintUpdateAdvisoryFn = oldMaybePrintUpdateAdvisoryFn
		projectHostCountForAdvisoryFn = oldProjectHostCountForAdvisoryFn
		isTerminalFn = oldIsTerminalFn
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	os.Args = []string{"yeet", "status"}
	handleSvcCmdFn = func(args []string) error {
		return nil
	}
	isTerminalFn = func(int) bool { return true }
	projectHostCountForAdvisoryFn = func() int { return 3 }
	var gotArgs []string
	var gotExitCode, gotHostCount int
	var gotStdoutTTY, gotStderrTTY bool
	maybePrintUpdateAdvisoryFn = func(w io.Writer, args []string, exitCode int, stdoutTTY bool, stderrTTY bool, projectHostCount int) {
		gotArgs = append([]string(nil), args...)
		gotExitCode = exitCode
		gotStdoutTTY = stdoutTTY
		gotStderrTTY = stderrTTY
		gotHostCount = projectHostCount
	}

	if code := run(); code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}
	if !reflect.DeepEqual(gotArgs, []string{"status"}) || gotExitCode != 0 || !gotStdoutTTY || !gotStderrTTY || gotHostCount != 3 {
		t.Fatalf("advisory args=%#v exit=%d stdoutTTY=%v stderrTTY=%v hosts=%d", gotArgs, gotExitCode, gotStdoutTTY, gotStderrTTY, gotHostCount)
	}
}

func TestRunCronHelpAgentUsesSingleServicePlaceholder(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "cron", "--help-agent"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("cron help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	wantUsage := `yeet [GLOBAL_OPTIONS] cron <SERVICE> FILE "<cron expr>" [-- <args...>]`
	if !strings.Contains(stdout, wantUsage) {
		t.Fatalf("stdout missing usage %q:\n%s", wantUsage, stdout)
	}
	if strings.Contains(stdout, `SVC FILE "<cron expr>"`) {
		t.Fatalf("stdout contains duplicate service placeholder:\n%s", stdout)
	}
}

func TestRunListHostsHelpAgentNotesLocalTailscaleRequirement(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "list-hosts", "--help-agent"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("list-hosts help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	if !strings.Contains(stdout, "requires a local Tailscale client") {
		t.Fatalf("stdout missing local Tailscale note:\n%s", stdout)
	}
}

func TestRunInitHelpAgentUsesMachineHostAndVMToolsFlag(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "init", "--help-agent"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("init help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{"--install-vm-tools", "--ts-client-secret", "--workspace", "--no-workspace", "root@<machine-host>"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "root@<host>") {
		t.Fatalf("stdout still uses ambiguous host placeholder:\n%s", stdout)
	}
}

func TestSnapshotsDefaultsHelpShowsSubcommands(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "snapshots", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("snapshots help should not call handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{
		"Manage service recovery points and snapshot defaults",
		"defaults",
		"list",
		"inspect",
		"create",
		"clone",
		"restore",
		"rm",
		"protect",
		"unprotect",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want snapshots help to include %q", stdout, want)
		}
	}
}

func TestParseGlobalFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantVal string
		wantSvc string
		wantOut []string
	}{
		{
			name:    "consumes separate value",
			args:    []string{"--host", "catch", "status"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "consumes equals value",
			args:    []string{"status", "--host=catch"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "last value wins",
			args:    []string{"--host", "one", "--host", "two", "status"},
			wantVal: "two",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "stops at double dash",
			args:    []string{"--host", "catch", "--", "--host", "ignored"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"--", "--host", "ignored"},
		},
		{
			name:    "unknown flags are preserved",
			args:    []string{"--unknown", "x", "--host", "catch"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"--unknown", "x"},
		},
		{
			name:    "service flag parsed",
			args:    []string{"--service", "svc-a", "status"},
			wantVal: "",
			wantSvc: "svc-a",
			wantOut: []string{"status"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, out, err := parseGlobalFlags(tt.args)
			if err != nil {
				t.Fatalf("parseGlobalFlags error: %v", err)
			}
			if flags.Host != tt.wantVal {
				t.Fatalf("Host = %q, want %q", flags.Host, tt.wantVal)
			}
			if flags.Service != tt.wantSvc {
				t.Fatalf("Service = %q, want %q", flags.Service, tt.wantSvc)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("out = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}

func TestResolveGlobalOverrides(t *testing.T) {
	tests := []struct {
		name     string
		flags    globalFlagsParsed
		wantHost string
		wantSvc  string
	}{
		{
			name:     "host only",
			flags:    globalFlagsParsed{Host: "catch-a"},
			wantHost: "catch-a",
		},
		{
			name:    "service only",
			flags:   globalFlagsParsed{Service: "svc-a"},
			wantSvc: "svc-a",
		},
		{
			name:     "qualified service overrides host",
			flags:    globalFlagsParsed{Host: "catch-a", Service: "svc-a@catch-b"},
			wantHost: "catch-b",
			wantSvc:  "svc-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGlobalOverrides(tt.flags)
			if got.host != tt.wantHost {
				t.Fatalf("host = %q, want %q", got.host, tt.wantHost)
			}
			if got.service != tt.wantSvc {
				t.Fatalf("service = %q, want %q", got.service, tt.wantSvc)
			}
		})
	}
}

func TestPrepareCommandRoute(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		service     string
		wantArgs    []string
		wantHost    string
		wantService string
		wantBridged []string
	}{
		{
			name:        "rewrites env set shorthand",
			args:        []string{"env", "svc-a", "FOO=bar"},
			wantArgs:    []string{"env", "set", "FOO=bar"},
			wantService: "svc-a",
			wantBridged: []string{"env", "set", "FOO=bar"},
		},
		{
			name:        "splits host from command",
			args:        []string{"status@catch-a", "svc-a"},
			wantArgs:    []string{"status", "svc-a"},
			wantHost:    "catch-a",
			wantService: "",
			wantBridged: nil,
		},
		{
			name:        "events host defaults all services",
			args:        []string{"events@catch-a"},
			wantArgs:    []string{"events", "--all"},
			wantHost:    "catch-a",
			wantService: "",
			wantBridged: nil,
		},
		{
			name:        "honors existing service override",
			args:        []string{"status", "--format", "json"},
			service:     "svc-override",
			wantArgs:    []string{"status", "--format", "json"},
			wantService: "svc-override",
			wantBridged: []string{"status", "--format", "json"},
		},
		{
			name:        "service group host routes service set",
			args:        []string{"service@catch-a", "set", "svc-a", "--service-root=/srv/apps/svc-a"},
			wantArgs:    []string{"service", "set", "--service-root=/srv/apps/svc-a"},
			wantHost:    "catch-a",
			wantService: "svc-a",
			wantBridged: []string{"service", "set", "--service-root=/srv/apps/svc-a"},
		},
		{
			name:        "service set zfs root host target",
			args:        []string{"service@catch-a", "set", "svc-a", "--service-root=tank/apps/svc-a", "--zfs"},
			wantHost:    "catch-a",
			wantService: "svc-a",
			wantArgs:    []string{"service", "set", "--service-root=tank/apps/svc-a", "--zfs"},
			wantBridged: []string{"service", "set", "--service-root=tank/apps/svc-a", "--zfs"},
		},
		{
			name:        "service rollback host target",
			args:        []string{"service@catch-a", "rollback", "svc-a"},
			wantHost:    "catch-a",
			wantService: "svc-a",
			wantArgs:    []string{"service", "rollback"},
			wantBridged: []string{"service", "rollback"},
		},
		{
			name:        "service generations format host target",
			args:        []string{"service@catch-a", "generations", "svc-a", "--format=json"},
			wantHost:    "catch-a",
			wantService: "svc-a",
			wantArgs:    []string{"service", "generations", "--format=json"},
			wantBridged: []string{"service", "generations", "--format=json"},
		},
		{
			name:     "snapshots defaults is unscoped remote group",
			args:     []string{"snapshots@catch-a", "defaults", "show"},
			wantHost: "catch-a",
			wantArgs: []string{"snapshots", "defaults", "show"},
		},
		{
			name:     "snapshots lifecycle is unscoped remote group",
			args:     []string{"snapshots@catch-a", "inspect", "svc-a", "yeet-abc", "--format", "json"},
			wantHost: "catch-a",
			wantArgs: []string{"snapshots", "inspect", "svc-a", "yeet-abc", "--format", "json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prepareCommandRoute(tt.args, tt.service)
			if !reflect.DeepEqual(got.args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", got.args, tt.wantArgs)
			}
			if got.host != tt.wantHost {
				t.Fatalf("host = %q, want %q", got.host, tt.wantHost)
			}
			if got.service != tt.wantService {
				t.Fatalf("service = %q, want %q", got.service, tt.wantService)
			}
			if !reflect.DeepEqual(got.bridgedArgs, tt.wantBridged) {
				t.Fatalf("bridgedArgs = %#v, want %#v", got.bridgedArgs, tt.wantBridged)
			}
		})
	}
}

func TestPrepareCommandRouteShortArgsAndGroupHost(t *testing.T) {
	got := prepareCommandRoute(nil, "")
	if len(got.args) != 0 || got.host != "" || got.service != "" || got.bridgedArgs != nil {
		t.Fatalf("empty route = %#v, want zero route", got)
	}

	got = prepareCommandRoute([]string{"docker@catch-a", "update", "svc-a"}, "")
	if got.host != "catch-a" {
		t.Fatalf("host = %q, want catch-a", got.host)
	}
	if !reflect.DeepEqual(got.args, []string{"docker", "update", "svc-a"}) {
		t.Fatalf("args = %#v, want unbridged docker update service", got.args)
	}
	if got.service != "" {
		t.Fatalf("service = %q, want empty", got.service)
	}

	got = prepareCommandRoute([]string{"docker@catch-a", "update", "svc-a", "svc-b"}, "")
	if got.host != "catch-a" {
		t.Fatalf("host = %q, want catch-a", got.host)
	}
	if !reflect.DeepEqual(got.args, []string{"docker", "update", "svc-a", "svc-b"}) {
		t.Fatalf("args = %#v, want unbridged docker update services", got.args)
	}
	if got.service != "" {
		t.Fatalf("service = %q, want empty", got.service)
	}

	got = prepareCommandRoute([]string{"docker@catch-a", "outdated", "svc-a"}, "")
	if got.host != "catch-a" {
		t.Fatalf("host = %q, want catch-a", got.host)
	}
	if !reflect.DeepEqual(got.args, []string{"docker", "outdated"}) {
		t.Fatalf("args = %#v, want bridged docker outdated", got.args)
	}
	if got.service != "svc-a" {
		t.Fatalf("service = %q, want svc-a", got.service)
	}
}

func TestBridgeWithOverride(t *testing.T) {
	remoteSpecs := map[string]map[string]cli.FlagSpec{
		"status": {},
	}
	groupSpecs := map[string]map[string]map[string]cli.FlagSpec{
		"env": {
			"get": {},
		},
	}
	tests := []struct {
		name        string
		args        []string
		wantOK      bool
		wantService string
		wantBridged []string
	}{
		{name: "remote command", args: []string{"status", "--json"}, wantOK: true, wantService: "svc-a", wantBridged: []string{"status", "--json"}},
		{name: "remote group command", args: []string{"env", "get", "FOO"}, wantOK: true, wantService: "svc-a", wantBridged: []string{"env", "get", "FOO"}},
		{name: "unknown command", args: []string{"local"}, wantOK: false},
		{name: "group without subcommand", args: []string{"env"}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _, bridged, ok := bridgeWithOverride(tt.args, remoteSpecs, groupSpecs, "svc-a")
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if service != tt.wantService {
				t.Fatalf("service = %q, want %q", service, tt.wantService)
			}
			if !reflect.DeepEqual(bridged, tt.wantBridged) {
				t.Fatalf("bridged = %#v, want %#v", bridged, tt.wantBridged)
			}
		})
	}
}

func TestBridgeHelpersCoverTerminatorAndShortFlags(t *testing.T) {
	flags := map[string]cli.FlagSpec{
		"-n":       {ConsumesValue: true},
		"--format": {ConsumesValue: true},
	}
	if got := serviceIndexAfterTerminator([]string{"status", "--", "svc-a"}, 1); got != 2 {
		t.Fatalf("serviceIndexAfterTerminator = %d, want 2", got)
	}
	if got := serviceIndexAfterTerminator([]string{"status", "--"}, 1); got != -1 {
		t.Fatalf("serviceIndexAfterTerminator without value = %d, want -1", got)
	}
	if skip, ok := flagTokenSkip("-n", flags); !ok || skip != 1 {
		t.Fatalf("flagTokenSkip -n = (%d, %v), want (1, true)", skip, ok)
	}
	if skip, ok := flagTokenSkip("-n5", flags); !ok || skip != 0 {
		t.Fatalf("flagTokenSkip -n5 = (%d, %v), want (0, true)", skip, ok)
	}
	if skip, ok := flagTokenSkip("-", flags); ok || skip != 0 {
		t.Fatalf("flagTokenSkip - = (%d, %v), want (0, false)", skip, ok)
	}
	if got := removeArgAt([]string{"a", "b"}, 5); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("removeArgAt out of range = %#v", got)
	}
	if got := removeArgAt([]string{"a", "b"}, 0); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("removeArgAt first arg = %#v, want [b]", got)
	}
	if skip, ok := flagTokenSkip("--format=json", flags); !ok || skip != 0 {
		t.Fatalf("flagTokenSkip --format=json = (%d, %v), want (0, true)", skip, ok)
	}
}

func TestGroupHandlersWrapRemoteCommands(t *testing.T) {
	oldBridgedArgs := bridgedArgs
	oldHandleSvcCmdFn := handleSvcCmdFn
	defer func() {
		bridgedArgs = oldBridgedArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
	}()

	var got []string
	handleSvcCmdFn = func(args []string) error {
		got = append([]string(nil), args...)
		return nil
	}

	if err := handleDockerGroup(context.Background(), []string{"logs", "svc-a"}); err != nil {
		t.Fatalf("handleDockerGroup: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"docker", "logs", "svc-a"}) {
		t.Fatalf("docker group args = %#v", got)
	}

	if err := handleEnvGroup(context.Background(), []string{"get", "FOO"}); err != nil {
		t.Fatalf("handleEnvGroup: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"env", "get", "FOO"}) {
		t.Fatalf("env group args = %#v", got)
	}

	dockerGroup := buildGroupHandlers()["docker"]
	if _, ok := dockerGroup.Commands["outdated"]; !ok {
		t.Fatal("docker outdated should be registered in group handlers")
	}
}

func TestParseListHostsFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantTags []string
	}{
		{
			name:     "default tags",
			args:     []string{},
			wantTags: []string{"tag:catch"},
		},
		{
			name:     "comma-separated tags",
			args:     []string{"list-hosts", "--tags", "tag:a,tag:b"},
			wantTags: []string{"tag:a", "tag:b"},
		},
		{
			name:     "repeated tags",
			args:     []string{"list-hosts", "--tags", "tag:a", "--tags", "tag:b"},
			wantTags: []string{"tag:a", "tag:b"},
		},
		{
			name:     "ignores unknown flags",
			args:     []string{"list-hosts", "--tags", "tag:a", "--unknown", "x"},
			wantTags: []string{"tag:a"},
		},
		{
			name:     "stops at double dash",
			args:     []string{"list-hosts", "--tags", "tag:a", "--", "--tags", "tag:b"},
			wantTags: []string{"tag:a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := parseListHostsFlags(tt.args)
			if err != nil {
				t.Fatalf("parseListHostsFlags error: %v", err)
			}
			if !reflect.DeepEqual(flags.Tags, tt.wantTags) {
				t.Fatalf("Tags = %#v, want %#v", flags.Tags, tt.wantTags)
			}
		})
	}
}

func TestApplyGlobalUIFlagsAdditionalModesAndErrors(t *testing.T) {
	if err := applyGlobalUIFlags(globalFlagsParsed{TTY: true}); err != nil {
		t.Fatalf("applyGlobalUIFlags tty: %v", err)
	}
	if err := applyGlobalUIFlags(globalFlagsParsed{Progress: "not-a-mode"}); err == nil {
		t.Fatalf("applyGlobalUIFlags succeeded for invalid progress mode")
	}
}

func TestRewriteEnvSetArgsNoopCases(t *testing.T) {
	tests := [][]string{
		{"env", "svc-a"},
		{"status", "svc-a", "FOO=bar"},
		{"env", "svc-a", "FOO"},
	}
	for _, args := range tests {
		got := rewriteEnvSetArgs(append([]string(nil), args...))
		if !reflect.DeepEqual(got, args) {
			t.Fatalf("rewriteEnvSetArgs(%v) = %v, want unchanged", args, got)
		}
	}
}

func TestGroupHandlersCoverRemoteGroupInfos(t *testing.T) {
	groups := buildGroupHandlers()
	for groupName, info := range cli.RemoteGroupInfos() {
		group, ok := groups[groupName]
		if !ok {
			t.Fatalf("missing group handler for %q", groupName)
		}
		for cmdName := range info.Commands {
			if _, ok := group.Commands[cmdName]; !ok {
				t.Fatalf("missing handler for group %q command %q", groupName, cmdName)
			}
		}
	}
}

func TestEnvCopyAlias(t *testing.T) {
	helpConfig := buildHelpConfig()
	args := yargs.ApplyAliases([]string{"env", "cp", "svc", "file"}, helpConfig)
	if len(args) < 2 || args[1] != "copy" {
		t.Fatalf("expected alias to resolve to copy, got %v", args)
	}
}

func TestCopyAlias(t *testing.T) {
	helpConfig := buildHelpConfig()
	args := yargs.ApplyAliases([]string{"cp", "src", "dst"}, helpConfig)
	if len(args) == 0 || args[0] != "copy" {
		t.Fatalf("expected alias to resolve to copy, got %v", args)
	}
}

func TestRewriteEnvSetArgs(t *testing.T) {
	args := rewriteEnvSetArgs([]string{"env", "svc-a", "FOO="})
	want := []string{"env", "set", "svc-a", "FOO="}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected args: %v", args)
	}

	args = rewriteEnvSetArgs([]string{"env", "show", "svc-a"})
	want = []string{"env", "show", "svc-a"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected args: %v", args)
	}
}
