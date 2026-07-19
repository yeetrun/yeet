// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catch"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/views"
)

func TestPrepareDataDirsAndNewCatchConfig(t *testing.T) {
	root := t.TempDir()
	paths, err := prepareDataDirs(root)
	if err != nil {
		t.Fatalf("prepareDataDirs: %v", err)
	}
	for _, dir := range []string{paths.dataDir, paths.registryDir, paths.servicesDir, paths.mountsDir} {
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat prepared dir %s: %v", dir, err)
		}
		if !st.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}

	cfg := newCatchConfig(paths, "catch-user", "127.0.0.1:0", filepath.Join(root, "containerd.sock"), paths.servicesDir)
	if cfg.DefaultUser != "catch-user" || cfg.InstallUser != "catch-user" {
		t.Fatalf("config users = (%q, %q), want catch-user", cfg.DefaultUser, cfg.InstallUser)
	}
	if cfg.RootDir != root || cfg.ServicesRoot != paths.servicesDir || cfg.MountsRoot != paths.mountsDir {
		t.Fatalf("config paths not wired from prepared dirs: %#v", cfg)
	}
	if cfg.DB == nil {
		t.Fatalf("config DB is nil")
	}
}

func TestResolveCatchStartupPathsSelectsDataDirAndServicesRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultDataDir := filepath.Join(home, "yeet-data")
	customDataDir := filepath.Join(t.TempDir(), "custom-data")
	customServicesRoot := filepath.Join(t.TempDir(), "services")

	tests := []struct {
		name             string
		opts             catchStartupOptions
		wantDataDir      string
		wantServicesRoot string
	}{
		{
			name:             "no data dir uses home yeet data",
			opts:             catchStartupOptions{},
			wantDataDir:      defaultDataDir,
			wantServicesRoot: filepath.Join(defaultDataDir, "services"),
		},
		{
			name:             "custom data dir is preserved",
			opts:             catchStartupOptions{dataDir: customDataDir},
			wantDataDir:      customDataDir,
			wantServicesRoot: filepath.Join(customDataDir, "services"),
		},
		{
			name:             "custom services root overrides default",
			opts:             catchStartupOptions{dataDir: customDataDir, servicesRoot: customServicesRoot},
			wantDataDir:      customDataDir,
			wantServicesRoot: customServicesRoot,
		},
		{
			name:             "missing services root derives from data dir",
			opts:             catchStartupOptions{dataDir: customDataDir, servicesRoot: " \t\n"},
			wantDataDir:      customDataDir,
			wantServicesRoot: filepath.Join(customDataDir, "services"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCatchStartupPaths(tt.opts)
			if err != nil {
				t.Fatalf("resolveCatchStartupPaths: %v", err)
			}
			if got.dataDir != tt.wantDataDir {
				t.Fatalf("dataDir = %q, want %q", got.dataDir, tt.wantDataDir)
			}
			if got.servicesRoot != tt.wantServicesRoot {
				t.Fatalf("servicesRoot = %q, want %q", got.servicesRoot, tt.wantServicesRoot)
			}
			cfg := newCatchConfig(got.paths, "catch-user", "127.0.0.1:0", filepath.Join(t.TempDir(), "containerd.sock"), got.servicesRoot)
			if cfg.ServicesRoot != tt.wantServicesRoot {
				t.Fatalf("Config.ServicesRoot = %q, want %q", cfg.ServicesRoot, tt.wantServicesRoot)
			}
			if cfg.TSNetHost != *tsnetHost {
				t.Fatalf("Config.TSNetHost = %q, want %q", cfg.TSNetHost, *tsnetHost)
			}
		})
	}
}

func TestResolveCatchStartupOptionsResolvesInstallZFSTargets(t *testing.T) {
	oldResolve := resolveCatchInstallZFSTargetFn
	t.Cleanup(func() { resolveCatchInstallZFSTargetFn = oldResolve })

	t.Setenv(catchInstallDataDirZFSEnv, "1")
	t.Setenv(catchInstallServicesRootZFSEnv, "1")
	resolveCatchInstallZFSTargetFn = func(_ context.Context, dataset string) (string, error) {
		switch dataset {
		case "flash/yeet/data":
			return "/flash/yeet/data", nil
		case "flash/yeet/services":
			return "/flash/yeet/services", nil
		default:
			t.Fatalf("unexpected dataset %q", dataset)
			return "", nil
		}
	}

	got, err := resolveCatchStartupOptions("flash/yeet/data", "flash/yeet/services")
	if err != nil {
		t.Fatalf("resolveCatchStartupOptions: %v", err)
	}
	if got.dataDir != "/flash/yeet/data" || got.servicesRoot != "/flash/yeet/services" {
		t.Fatalf("startup opts = %#v, want resolved ZFS mountpoints", got)
	}
}

func TestResolveCatchStartupOptionsDerivesServicesRootFromResolvedZFSDataDir(t *testing.T) {
	oldResolve := resolveCatchInstallZFSTargetFn
	t.Cleanup(func() { resolveCatchInstallZFSTargetFn = oldResolve })

	dataMount := filepath.Join(t.TempDir(), "flash", "yeet", "data")
	t.Setenv(catchInstallDataDirZFSEnv, "1")
	resolveCatchInstallZFSTargetFn = func(_ context.Context, dataset string) (string, error) {
		if dataset != "flash/yeet/data" {
			t.Fatalf("unexpected dataset %q", dataset)
		}
		return dataMount, nil
	}

	opts, err := resolveCatchStartupOptions("flash/yeet/data", "")
	if err != nil {
		t.Fatalf("resolveCatchStartupOptions: %v", err)
	}
	got, err := resolveCatchStartupPaths(opts)
	if err != nil {
		t.Fatalf("resolveCatchStartupPaths: %v", err)
	}
	if got.dataDir != dataMount || got.servicesRoot != filepath.Join(dataMount, "services") {
		t.Fatalf("startup = %#v, want services under resolved data dir", got)
	}
}

func TestValidateAndCheckContainerdSocket(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "containerd.sock")
	if err := os.WriteFile(socket, []byte("socket placeholder"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}
	if err := validateContainerdSocket(socket); err != nil {
		t.Fatalf("validateContainerdSocket existing file: %v", err)
	}
	if err := validateContainerdSocket(""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty socket error = %v, want required", err)
	}
	if err := validateContainerdSocket(filepath.Join(root, "missing.sock")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing socket error = %v, want not found", err)
	}

	dockerCfg := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(dockerCfg, []byte(`{"features":{"containerd-snapshotter":true}}`), 0o600); err != nil {
		t.Fatalf("write docker config: %v", err)
	}
	if err := checkContainerdSnapshotterEnabled(dockerCfg); err != nil {
		t.Fatalf("checkContainerdSnapshotterEnabled: %v", err)
	}
	if err := checkContainerdSnapshotterEnabled(filepath.Join(root, "missing-daemon.json")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing docker config error = %v, want missing", err)
	}
}

func TestInstallMetaReadWriteAndApply(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CATCH_INSTALL_USER", "install-user")
	t.Setenv("CATCH_INSTALL_HOST", "install-host")

	if got := installMetaPath(root); got != filepath.Join(root, "install.json") {
		t.Fatalf("installMetaPath = %q", got)
	}
	if got := detectInstallHost(); got != "install-host" {
		t.Fatalf("detectInstallHost = %q, want install-host", got)
	}
	if err := writeInstallMeta(root); err != nil {
		t.Fatalf("writeInstallMeta: %v", err)
	}
	meta, err := readInstallMeta(root)
	if err != nil {
		t.Fatalf("readInstallMeta: %v", err)
	}
	if meta.InstallUser != "install-user" || meta.InstallHost != "install-host" {
		t.Fatalf("install meta = %#v", meta)
	}

	cfg := &catch.Config{InstallUser: "default-user"}
	applyInstallMeta(cfg, root)
	if cfg.InstallUser != "install-user" || cfg.InstallHost != "install-host" {
		t.Fatalf("applyInstallMeta cfg = %#v", cfg)
	}
}

func TestHandleLocalCommandVersionAndDefault(t *testing.T) {
	var out strings.Builder
	handled, err := handleLocalCommand(nil, &catch.Config{}, t.TempDir(), &out)
	if err != nil {
		t.Fatalf("handleLocalCommand nil args: %v", err)
	}
	if handled || out.Len() != 0 {
		t.Fatalf("nil args handled=%v output=%q, want unhandled empty output", handled, out.String())
	}

	handled, err = handleLocalCommand([]string{"version"}, &catch.Config{}, t.TempDir(), &out)
	if err != nil {
		t.Fatalf("handleLocalCommand version: %v", err)
	}
	if !handled {
		t.Fatalf("version command was not handled")
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatalf("version command did not write output")
	}
}

func TestHandleLocalCommandVMNetworkEnsure(t *testing.T) {
	oldEnsure := ensureVMNetworkFn
	t.Cleanup(func() { ensureVMNetworkFn = oldEnsure })

	cfg := &catch.Config{RootDir: "/srv/catch/data"}
	var gotCfg *catch.Config
	var gotService string
	ensureVMNetworkFn = func(_ context.Context, cfg *catch.Config, service string) error {
		gotCfg = cfg
		gotService = service
		return nil
	}

	handled, err := handleLocalCommand([]string{"vm-network-ensure", "devbox"}, cfg, cfg.RootDir, io.Discard)
	if err != nil {
		t.Fatalf("handleLocalCommand vm-network-ensure: %v", err)
	}
	if !handled {
		t.Fatalf("vm-network-ensure command was not handled")
	}
	if gotCfg != cfg {
		t.Fatalf("ensure config = %#v, want original config", gotCfg)
	}
	if gotService != "devbox" {
		t.Fatalf("ensure service = %q, want devbox", gotService)
	}
}

func TestHandleLocalCommandVMNetworkEnsureRequiresService(t *testing.T) {
	oldEnsure := ensureVMNetworkFn
	t.Cleanup(func() { ensureVMNetworkFn = oldEnsure })
	ensureVMNetworkFn = func(context.Context, *catch.Config, string) error {
		t.Fatal("vm-network-ensure without service should not call ensure")
		return nil
	}

	for _, args := range [][]string{
		{"vm-network-ensure"},
		{"vm-network-ensure", "   "},
	} {
		handled, err := handleLocalCommand(args, &catch.Config{}, t.TempDir(), io.Discard)
		if !handled {
			t.Fatalf("%v was not handled", args)
		}
		if err == nil || !strings.Contains(err.Error(), "service is required") {
			t.Fatalf("handleLocalCommand(%v) error = %v, want service required", args, err)
		}
	}
}

func TestHandleLocalCommandVMLANBridgeStatus(t *testing.T) {
	var out bytes.Buffer
	cfg := &catch.Config{RootDir: t.TempDir()}
	handled, err := handleLocalCommand([]string{"vm-lan-bridge-status"}, cfg, cfg.RootDir, &out)
	if err != nil {
		t.Fatalf("handleLocalCommand vm-lan-bridge-status: %v", err)
	}
	if !handled {
		t.Fatal("vm-lan-bridge-status was not handled")
	}
	if !strings.Contains(out.String(), "VM LAN bridge") {
		t.Fatalf("output = %q, want VM LAN bridge summary", out.String())
	}
}

func TestHandleLocalCommandVMLANBridgePrepareRequiresYes(t *testing.T) {
	var out bytes.Buffer
	cfg := &catch.Config{RootDir: t.TempDir()}
	handled, err := handleLocalCommand([]string{"vm-lan-bridge-prepare"}, cfg, cfg.RootDir, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes is required") {
		t.Fatalf("error = %v, want --yes is required", err)
	}
	if !handled {
		t.Fatal("vm-lan-bridge-prepare was not handled")
	}
}

func TestHandleLocalCommandVMLANBridgePrepareRejectsExtraArgs(t *testing.T) {
	var out bytes.Buffer
	cfg := &catch.Config{RootDir: t.TempDir()}
	handled, err := handleLocalCommand([]string{"vm-lan-bridge-prepare", "--yes", "extra"}, cfg, cfg.RootDir, &out)
	if err == nil || !strings.Contains(err.Error(), "does not accept arguments") {
		t.Fatalf("error = %v, want extra arg rejection", err)
	}
	if !handled {
		t.Fatal("vm-lan-bridge-prepare was not handled")
	}
}

func TestRunCatchProcessHandlesSpecialCommand(t *testing.T) {
	var out strings.Builder
	if err := runCatchProcess([]string{"is-catch"}, &out); err != nil {
		t.Fatalf("runCatchProcess returned error: %v", err)
	}
	if strings.TrimSpace(out.String()) != "yes" {
		t.Fatalf("output = %q, want yes", out.String())
	}
}

func TestRunCatchProcessHandlesLocalCommand(t *testing.T) {
	oldDataDir := *legacyDataDir
	*legacyDataDir = t.TempDir()
	t.Cleanup(func() { *legacyDataDir = oldDataDir })

	var out strings.Builder
	if err := runCatchProcess([]string{"version"}, &out); err != nil {
		t.Fatalf("runCatchProcess returned error: %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("version output is empty")
	}
}

func TestRunCatchProcessDNSRunsWithoutRuntimeValidation(t *testing.T) {
	oldDataDir := *legacyDataDir
	oldRunDNS := runDNSFn
	oldValidateRuntime := validateCatchRuntimeFn
	root := t.TempDir()
	*legacyDataDir = root
	t.Cleanup(func() {
		*legacyDataDir = oldDataDir
		runDNSFn = oldRunDNS
		validateCatchRuntimeFn = oldValidateRuntime
	})

	validateCatchRuntimeFn = func(string) error {
		t.Fatal("dns command should not validate containerd runtime")
		return nil
	}
	var gotCfg *catch.Config
	runDNSFn = func(_ context.Context, cfg *catch.Config) error {
		gotCfg = cfg
		return nil
	}

	if err := runCatchProcess([]string{"dns"}, io.Discard); err != nil {
		t.Fatalf("runCatchProcess dns returned error: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("dns command did not run DNS server")
	}
	if gotCfg.RootDir != root {
		t.Fatalf("DNS config RootDir = %q, want %q", gotCfg.RootDir, root)
	}
}

func TestRunCatchProcessReturnsRuntimeValidationError(t *testing.T) {
	oldDataDir := *legacyDataDir
	oldValidateRuntime := validateCatchRuntimeFn
	*legacyDataDir = t.TempDir()
	wantErr := errors.New("runtime missing")
	validateCatchRuntimeFn = func(string) error { return wantErr }
	t.Cleanup(func() {
		*legacyDataDir = oldDataDir
		validateCatchRuntimeFn = oldValidateRuntime
	})

	err := runCatchProcess(nil, io.Discard)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runCatchProcess error = %v, want %v", err, wantErr)
	}
}

func TestHandleLocalCommandInstallPreparesRuntimeBeforeInstallingService(t *testing.T) {
	oldSetupDocker := setupDockerFn
	oldEnsureSnapshotter := ensureContainerdSnapshotterForInstallFn
	oldValidateRuntime := validateCatchRuntimeFn
	oldDoInstall := doInstallFn
	oldSetupVMHost := setupVMHostFn
	t.Cleanup(func() {
		setupDockerFn = oldSetupDocker
		ensureContainerdSnapshotterForInstallFn = oldEnsureSnapshotter
		validateCatchRuntimeFn = oldValidateRuntime
		doInstallFn = oldDoInstall
		setupVMHostFn = oldSetupVMHost
	})

	var order []string
	setupDockerFn = func() error {
		order = append(order, "docker")
		return nil
	}
	ensureContainerdSnapshotterForInstallFn = func(string) error {
		order = append(order, "snapshotter")
		return nil
	}
	validateCatchRuntimeFn = func(string) error {
		order = append(order, "runtime")
		return nil
	}
	doInstallFn = func(*catch.Config, string) error {
		order = append(order, "install")
		return nil
	}
	setupVMHostFn = func(string) error {
		order = append(order, "vm")
		return nil
	}

	handled, err := handleLocalCommand([]string{"install"}, &catch.Config{}, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatalf("handleLocalCommand install returned error: %v", err)
	}
	if !handled {
		t.Fatal("install command was not handled")
	}
	want := []string{"docker", "snapshotter", "runtime", "install", "vm"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("install order = %#v, want %#v", order, want)
	}
}

func TestHandleSpecialCommandVMRun(t *testing.T) {
	oldRun := runVMConsoleProxy
	defer func() { runVMConsoleProxy = oldRun }()

	var got catch.VMConsoleProxyConfig
	runVMConsoleProxy = func(_ context.Context, cfg catch.VMConsoleProxyConfig) error {
		got = cfg
		return nil
	}

	handled, err := handleSpecialCommand([]string{
		"vm-run",
		"--service", "devbox",
		"--service-root", "/srv/vms/devbox",
		"--disk-path", "/srv/vms/devbox/rootfs.ext4",
		"--firecracker", "/srv/firecracker",
		"--jailer", "/srv/jailer",
		"--jailer-base", "/run/yeet/vm-jailer",
		"--api-sock", "/run/fc.sock",
		"--config-file", "/run/fc.json",
		"--console-sock", "/run/serial.sock",
	}, io.Discard)
	if err != nil {
		t.Fatalf("handleSpecialCommand: %v", err)
	}
	if !handled {
		t.Fatal("vm-run was not handled")
	}
	want := catch.VMConsoleProxyConfig{
		Service:       "devbox",
		ServiceRoot:   "/srv/vms/devbox",
		DiskPath:      "/srv/vms/devbox/rootfs.ext4",
		Firecracker:   "/srv/firecracker",
		Jailer:        "/srv/jailer",
		JailerBase:    "/run/yeet/vm-jailer",
		APISocket:     "/run/fc.sock",
		ConfigFile:    "/run/fc.json",
		ConsoleSocket: "/run/serial.sock",
	}
	if got.OnGuestReboot == nil {
		t.Fatal("OnGuestReboot is nil, want automatic guest kernel sync hook")
	}
	got.OnGuestReboot = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
}

func TestHandleVMRunCommandRequiresJailerFlags(t *testing.T) {
	oldRun := runVMConsoleProxy
	t.Cleanup(func() { runVMConsoleProxy = oldRun })
	called := false
	runVMConsoleProxy = func(context.Context, catch.VMConsoleProxyConfig) error {
		called = true
		return nil
	}

	for _, tt := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "jailer", args: []string{"--jailer-base", "/run/yeet/vm-jailer"}, wantErr: "vm-run requires --jailer"},
		{name: "jailer base", args: []string{"--jailer", "/srv/jailer"}, wantErr: "vm-run requires --jailer-base"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			err := handleVMRunCommand(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if called {
				t.Fatal("runVMConsoleProxy called with missing jailer inputs")
			}
		})
	}
}

func TestHandleSpecialCommandVMRunExitsWithRebootCode(t *testing.T) {
	oldRun := runVMConsoleProxy
	oldExit := exitProcess
	t.Cleanup(func() {
		runVMConsoleProxy = oldRun
		exitProcess = oldExit
	})

	var exitCode int
	runVMConsoleProxy = func(context.Context, catch.VMConsoleProxyConfig) error {
		return catch.ErrVMGuestReboot
	}
	exitProcess = func(code int) {
		exitCode = code
		panic("exit intercepted")
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected intercepted exit")
		}
		if recovered != "exit intercepted" {
			t.Fatalf("panic = %#v, want exit intercepted", recovered)
		}
		if exitCode != catch.VMGuestRebootExitCode {
			t.Fatalf("exit code = %d, want %d", exitCode, catch.VMGuestRebootExitCode)
		}
	}()

	_, _ = handleSpecialCommand([]string{"vm-run", "--firecracker", "/fc", "--jailer", "/jailer", "--jailer-base", "/jails", "--api-sock", "/api", "--config-file", "/cfg", "--console-sock", "/serial"}, io.Discard)
}

func TestHandleSpecialCommandVMRunExitsWithRestoreLoadFailureCode(t *testing.T) {
	oldRun := runVMConsoleProxy
	oldExit := exitProcess
	t.Cleanup(func() {
		runVMConsoleProxy = oldRun
		exitProcess = oldExit
	})

	var exitCode int
	runVMConsoleProxy = func(context.Context, catch.VMConsoleProxyConfig) error {
		return fmt.Errorf("context: %w", catch.ErrVMRestoreLoadFailed)
	}
	exitProcess = func(code int) {
		exitCode = code
		panic("exit intercepted")
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("exit was not intercepted")
		}
		if exitCode != catch.VMRestoreLoadFailedExitCode {
			t.Fatalf("exit code = %d, want %d", exitCode, catch.VMRestoreLoadFailedExitCode)
		}
	}()
	_, _ = handleSpecialCommand([]string{"vm-run", "--firecracker", "/fc", "--jailer", "/jailer", "--jailer-base", "/jails", "--api-sock", "/api", "--config-file", "/cfg", "--console-sock", "/serial"}, io.Discard)
}

func TestLoopbackAndTSNetServerConfig(t *testing.T) {
	if got := loopbackForAddr(netip.MustParseAddr("100.64.0.1")); got != ipv4Loopback {
		t.Fatalf("IPv4 loopback = %v, want %v", got, ipv4Loopback)
	}
	if got := loopbackForAddr(netip.MustParseAddr("fd7a:115c:a1e0::1")); got != ipv6Loopback {
		t.Fatalf("IPv6 loopback = %v, want %v", got, ipv6Loopback)
	}

	oldHost, oldPort := *tsnetHost, *tsnetPort
	defer func() {
		*tsnetHost = oldHost
		*tsnetPort = oldPort
	}()
	*tsnetHost = "catch-test"
	*tsnetPort = 4242
	root := t.TempDir()
	ts := newTSNetServer(root)
	if ts.Dir != filepath.Join(root, "tsnet") || ts.Hostname != "catch-test" || ts.Port != 4242 {
		t.Fatalf("tsnet server = %#v", ts)
	}
}

func TestInitTSNetReturnsNilWhenDisabled(t *testing.T) {
	oldHost := *tsnetHost
	t.Cleanup(func() {
		*tsnetHost = oldHost
	})
	*tsnetHost = ""

	got, err := initTSNet(t.TempDir())
	if err != nil {
		t.Fatalf("initTSNet error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("initTSNet() = %#v, want nil when tsnet host is empty", got)
	}
}

func TestValidateTSNetInstallAuthRequiresCredentialWithoutState(t *testing.T) {
	err := validateTSNetInstallAuth(false, "", "")
	if err == nil || !strings.Contains(err.Error(), "requires a Tailscale OAuth client secret or auth key") {
		t.Fatalf("validateTSNetInstallAuth error = %v, want credential requirement", err)
	}

	if err := validateTSNetInstallAuth(true, "", ""); err != nil {
		t.Fatalf("validateTSNetInstallAuth existing state error = %v", err)
	}
	if err := validateTSNetInstallAuth(false, "tskey-auth-test", ""); err != nil {
		t.Fatalf("validateTSNetInstallAuth auth key error = %v", err)
	}
	if err := validateTSNetInstallAuth(false, "", "tskey-client-test"); err != nil {
		t.Fatalf("validateTSNetInstallAuth client secret error = %v", err)
	}
}

func TestCatchTSNetStateExists(t *testing.T) {
	root := t.TempDir()
	if catchTSNetStateExists(root) {
		t.Fatal("catchTSNetStateExists = true before state file exists")
	}
	statePath := catchTSNetStatePath(root)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("MkdirAll state dir: %v", err)
	}
	if err := os.WriteFile(statePath, []byte("state"), 0o600); err != nil {
		t.Fatalf("WriteFile state: %v", err)
	}
	if !catchTSNetStateExists(root) {
		t.Fatal("catchTSNetStateExists = false after state file exists")
	}
}

func TestCatchTailscaleTagsFromEnv(t *testing.T) {
	if got := catchTailscaleTagsFromEnv(""); !reflect.DeepEqual(got, []string{defaultCatchTag}) {
		t.Fatalf("default tags = %#v", got)
	}
	got := catchTailscaleTagsFromEnv(" tag:catch,tag:app ,, ")
	want := []string{"tag:catch", "tag:app"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %#v, want %#v", got, want)
	}
}

func TestPrepareCatchTSNetAuthMintsAndStoresOAuthSecret(t *testing.T) {
	oldGenerate := generateCatchTailscaleAuthKeyFn
	oldWrite := writeCatchTailscaleClientSecretFn
	t.Cleanup(func() {
		generateCatchTailscaleAuthKeyFn = oldGenerate
		writeCatchTailscaleClientSecretFn = oldWrite
	})

	root := t.TempDir()
	t.Setenv("TS_AUTHKEY", "")
	t.Setenv("TS_CLIENT_SECRET", " tskey-client-test ")
	t.Setenv("TS_CATCH_TAGS", "tag:catch,tag:app")

	var storedSecret string
	generateCatchTailscaleAuthKeyFn = func(_ context.Context, clientSecret string, tags []string) (string, error) {
		if clientSecret != "tskey-client-test" {
			t.Fatalf("clientSecret = %q, want trimmed secret", clientSecret)
		}
		wantTags := []string{"tag:catch", "tag:app"}
		if !reflect.DeepEqual(tags, wantTags) {
			t.Fatalf("tags = %#v, want %#v", tags, wantTags)
		}
		return "tskey-auth-generated", nil
	}
	writeCatchTailscaleClientSecretFn = func(rootDir string, clientSecret string) (string, error) {
		if rootDir != root {
			t.Fatalf("rootDir = %q, want %q", rootDir, root)
		}
		storedSecret = clientSecret
		return filepath.Join(rootDir, "services", catch.CatchService, "data", "tailscale.key"), nil
	}

	authKey, err := prepareCatchTSNetAuth(root)
	if err != nil {
		t.Fatalf("prepareCatchTSNetAuth returned error: %v", err)
	}
	if authKey != "tskey-auth-generated" {
		t.Fatalf("authKey = %q, want generated key", authKey)
	}
	if storedSecret != "tskey-client-test" {
		t.Fatalf("storedSecret = %q, want trimmed secret", storedSecret)
	}
}

func TestPrepareCatchTSNetAuthWrapsOAuthRejection(t *testing.T) {
	oldGenerate := generateCatchTailscaleAuthKeyFn
	t.Cleanup(func() {
		generateCatchTailscaleAuthKeyFn = oldGenerate
	})
	t.Setenv("TS_CLIENT_SECRET", "tskey-client-test")
	wantErr := errors.New("tag:catch not allowed")
	generateCatchTailscaleAuthKeyFn = func(context.Context, string, []string) (string, error) {
		return "", wantErr
	}

	_, err := prepareCatchTSNetAuth(t.TempDir())
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "tailscale OAuth setup failed") {
		t.Fatalf("prepareCatchTSNetAuth error = %v, want wrapped OAuth failure", err)
	}
}

func TestValidateCatchTSNetSelfRequiresTaggedNode(t *testing.T) {
	if err := validateCatchTSNetSelf(testTSNetSelf("catch.shayne.ts.net.", "tag:catch")); err != nil {
		t.Fatalf("validateCatchTSNetSelf tagged error = %v, want nil", err)
	}

	for _, self := range []*ipnstate.PeerStatus{
		nil,
		testTSNetSelf("catch.shayne.ts.net."),
	} {
		err := validateCatchTSNetSelf(self)
		if !errors.Is(err, errCatchTSNetUntagged) {
			t.Fatalf("validateCatchTSNetSelf(%#v) error = %v, want errCatchTSNetUntagged", self, err)
		}
	}
}

func TestValidateCatchTSNetSelfGivesSetupGuidance(t *testing.T) {
	err := validateCatchTSNetSelf(testTSNetSelf("catch.example.ts.net."))
	if !errors.Is(err, errCatchTSNetUntagged) {
		t.Fatalf("validateCatchTSNetSelf error = %v, want errCatchTSNetUntagged", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"tagOwners",
		"tag:catch",
		"rerun yeet init",
		"--ts-auth-key=<key>",
		"https://yeetrun.com/docs/concepts/tailscale",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("setup guidance missing %q:\n%s", want, msg)
		}
	}
}

func TestValidateStartedTSNetStatusClosesUntaggedServer(t *testing.T) {
	closer := &recordingCloser{}

	err := validateStartedTSNetStatus(closer, &ipnstate.Status{Self: testTSNetSelf("catch.example.ts.net.")})
	if !errors.Is(err, errCatchTSNetUntagged) {
		t.Fatalf("validateStartedTSNetStatus error = %v, want errCatchTSNetUntagged", err)
	}
	if !closer.closed {
		t.Fatal("validateStartedTSNetStatus did not close untagged server")
	}
}

type recordingCloser struct {
	closed bool
}

func (c *recordingCloser) Close() error {
	c.closed = true
	return nil
}

func testTSNetSelf(dnsName string, tags ...string) *ipnstate.PeerStatus {
	view := views.SliceOf(tags)
	return &ipnstate.PeerStatus{DNSName: dnsName, Tags: &view}
}

func TestTSNetAssignedNameWarning(t *testing.T) {
	if got := tsnetAssignedNameWarning("catch", "catch.shayne.ts.net."); got != "" {
		t.Fatalf("warning = %q, want empty for matching assigned name", got)
	}

	got := tsnetAssignedNameWarning("catch", "catch-1.shayne.ts.net.")
	for _, want := range []string{"Warning:", "requested Tailscale hostname \"catch\"", "assigned \"catch-1\"", "--host=catch-1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning missing %q:\n%s", want, got)
		}
	}
}

func TestProxyConnPairCopiesBetweenConnections(t *testing.T) {
	backendApp, backendProxy := net.Pipe()
	clientApp, clientProxy := net.Pipe()
	defer backendApp.Close()
	defer clientApp.Close()

	deadline := time.Now().Add(2 * time.Second)
	for _, conn := range []net.Conn{backendApp, backendProxy, clientApp, clientProxy} {
		if err := conn.SetDeadline(deadline); err != nil {
			t.Fatalf("SetDeadline: %v", err)
		}
	}

	done := make(chan struct{})
	go func() {
		proxyConnPair(backendProxy, clientProxy)
		close(done)
	}()

	writeErr := make(chan error, 1)
	go func() {
		_, err := clientApp.Write([]byte("ping"))
		writeErr <- err
	}()

	buf := make([]byte, len("ping"))
	if _, err := io.ReadFull(backendApp, buf); err != nil {
		t.Fatalf("ReadFull backend: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("proxied payload = %q, want ping", string(buf))
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("client write: %v", err)
	}
	clientApp.Close()
	backendApp.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("proxyConnPair did not return after peers closed")
	}
}

func TestDetectInstallUserFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		currentUser string
		want        string
	}{
		{
			name: "explicit install user wins",
			env: map[string]string{
				"CATCH_INSTALL_USER": "catch-user",
				"SUDO_USER":          "sudo-user",
				"USER":               "env-user",
			},
			currentUser: "current-user",
			want:        "catch-user",
		},
		{
			name: "sudo user before user",
			env: map[string]string{
				"SUDO_USER": "sudo-user",
				"USER":      "env-user",
			},
			currentUser: "current-user",
			want:        "sudo-user",
		},
		{
			name:        "current user fallback",
			env:         map[string]string{},
			currentUser: "current-user",
			want:        "current-user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectInstallUserFromEnv(func(key string) string {
				return tt.env[key]
			}, func() (string, error) {
				return tt.currentUser, nil
			})
			if got != tt.want {
				t.Fatalf("detectInstallUserFromEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectInstallUserFromEnvCurrentUserError(t *testing.T) {
	got := detectInstallUserFromEnv(func(string) string { return "" }, func() (string, error) {
		return "", errors.New("boom")
	})
	if got != "" {
		t.Fatalf("detectInstallUserFromEnv() = %q, want empty string", got)
	}
}

func TestVerifyContainerdSnapshotterConfig(t *testing.T) {
	if err := verifyContainerdSnapshotterConfig([]byte(`{"features":{"containerd-snapshotter":true}}`), "daemon.json"); err != nil {
		t.Fatalf("verifyContainerdSnapshotterConfig returned error: %v", err)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "invalid json", raw: `{`, want: "failed to parse"},
		{name: "missing features", raw: `{}`, want: "missing features.containerd-snapshotter=true"},
		{name: "disabled snapshotter", raw: `{"features":{"containerd-snapshotter":false}}`, want: "must set features.containerd-snapshotter=true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyContainerdSnapshotterConfig([]byte(tt.raw), "daemon.json")
			if err == nil {
				t.Fatalf("verifyContainerdSnapshotterConfig succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("verifyContainerdSnapshotterConfig error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestWriteContainerdSnapshotterConfigCreatesAndPreservesDockerConfig(t *testing.T) {
	root := t.TempDir()
	missingCfg := filepath.Join(root, "missing", "daemon.json")
	changed, err := writeContainerdSnapshotterConfig(missingCfg)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig missing config: %v", err)
	}
	if !changed {
		t.Fatal("missing config changed=false, want true")
	}
	raw, err := os.ReadFile(missingCfg)
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	if err := verifyContainerdSnapshotterConfig(raw, missingCfg); err != nil {
		t.Fatalf("created config did not verify: %v", err)
	}

	existingCfg := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(existingCfg, []byte(`{"log-driver":"journald","features":{"buildkit":true}}`), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	changed, err = writeContainerdSnapshotterConfig(existingCfg)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig existing config: %v", err)
	}
	if !changed {
		t.Fatal("existing config changed=false, want true")
	}
	raw, err = os.ReadFile(existingCfg)
	if err != nil {
		t.Fatalf("read updated config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("updated config json: %v", err)
	}
	if cfg["log-driver"] != "journald" {
		t.Fatalf("log-driver = %#v, want journald", cfg["log-driver"])
	}
	features := cfg["features"].(map[string]any)
	if features["buildkit"] != true || features["containerd-snapshotter"] != true {
		t.Fatalf("features = %#v, want buildkit and containerd-snapshotter", features)
	}

	changed, err = writeContainerdSnapshotterConfig(existingCfg)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig already enabled: %v", err)
	}
	if changed {
		t.Fatal("already enabled config changed=true, want false")
	}
}

func TestWriteContainerdSnapshotterConfigDoesNotSetDockerDNS(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(cfgPath, []byte(`{"log-driver":"journald","features":{"buildkit":true},"dns":["8.8.8.8"],"dns-search":["lan"]}`), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	changed, err := writeContainerdSnapshotterConfig(cfgPath)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig: %v", err)
	}
	if !changed {
		t.Fatal("writeContainerdSnapshotterConfig changed=false, want true")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read docker config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("docker config json: %v", err)
	}
	if cfg["log-driver"] != "journald" {
		t.Fatalf("log-driver = %#v, want journald", cfg["log-driver"])
	}
	if got := fmt.Sprint(cfg["dns"]); got != "[8.8.8.8]" {
		t.Fatalf("dns = %#v, want existing daemon DNS preserved", cfg["dns"])
	}
	if got := fmt.Sprint(cfg["dns-search"]); got != "[lan]" {
		t.Fatalf("dns-search = %#v, want existing daemon search preserved", cfg["dns-search"])
	}
	features := cfg["features"].(map[string]any)
	if features["buildkit"] != true || features["containerd-snapshotter"] != true {
		t.Fatalf("features = %#v, want buildkit and containerd-snapshotter", features)
	}
}

func TestWriteContainerdSnapshotterConfigCreatesOnlySnapshotterFeature(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "docker", "daemon.json")

	changed, err := writeContainerdSnapshotterConfig(cfgPath)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig: %v", err)
	}
	if !changed {
		t.Fatal("writeContainerdSnapshotterConfig changed=false, want true")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read docker config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("docker config json: %v", err)
	}
	if _, ok := cfg["dns"]; ok {
		t.Fatalf("dns = %#v, want no daemon DNS default", cfg["dns"])
	}
	if _, ok := cfg["dns-search"]; ok {
		t.Fatalf("dns-search = %#v, want no daemon search default", cfg["dns-search"])
	}
	features := cfg["features"].(map[string]any)
	if features["containerd-snapshotter"] != true {
		t.Fatalf("features = %#v, want containerd-snapshotter=true", features)
	}
}

func TestWriteContainerdSnapshotterConfigRemovesOldYeetDNSDefaults(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(cfgPath, []byte(`{"features":{"containerd-snapshotter":true},"dns":["192.168.100.1"],"dns-search":["yeet.internal"]}`), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	changed, err := writeContainerdSnapshotterConfig(cfgPath)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig: %v", err)
	}
	if !changed {
		t.Fatal("writeContainerdSnapshotterConfig changed=false, want true")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read docker config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("docker config json: %v", err)
	}
	if _, ok := cfg["dns"]; ok {
		t.Fatalf("dns = %#v, want old yeet daemon DNS removed", cfg["dns"])
	}
	if _, ok := cfg["dns-search"]; ok {
		t.Fatalf("dns-search = %#v, want old yeet daemon search removed", cfg["dns-search"])
	}
	features := cfg["features"].(map[string]any)
	if features["containerd-snapshotter"] != true {
		t.Fatalf("features = %#v, want containerd-snapshotter=true", features)
	}
}

func TestEnsureContainerdSnapshotterForInstallPreservesDockerDNS(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(cfgPath, []byte(`{"features":{"buildkit":true},"dns":["8.8.8.8"],"dns-search":["lan"]}`), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := ensureContainerdSnapshotterForInstall(cfgPath); err != nil {
		t.Fatalf("ensureContainerdSnapshotterForInstall: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read docker config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("docker config json: %v", err)
	}
	if got := fmt.Sprint(cfg["dns"]); got != "[8.8.8.8]" {
		t.Fatalf("dns = %#v, want existing daemon DNS preserved", cfg["dns"])
	}
	if got := fmt.Sprint(cfg["dns-search"]); got != "[lan]" {
		t.Fatalf("dns-search = %#v, want existing daemon search preserved", cfg["dns-search"])
	}
	features := cfg["features"].(map[string]any)
	if features["buildkit"] != true || features["containerd-snapshotter"] != true {
		t.Fatalf("features = %#v, want buildkit and containerd-snapshotter", features)
	}
}

func TestCheckContainerdSnapshotterEnabledReadAndParseErrors(t *testing.T) {
	root := t.TempDir()
	dirPath := filepath.Join(root, "daemon-dir")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir daemon dir: %v", err)
	}
	if err := checkContainerdSnapshotterEnabled(dirPath); err == nil || !strings.Contains(err.Error(), "failed to read") {
		t.Fatalf("directory config error = %v, want failed to read", err)
	}

	badJSON := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(badJSON, []byte("{"), 0o600); err != nil {
		t.Fatalf("write bad docker config: %v", err)
	}
	if err := checkContainerdSnapshotterEnabled(badJSON); err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Fatalf("bad json error = %v, want failed to parse", err)
	}
}

func TestSetupDockerSkipsInstallWhenDockerPresent(t *testing.T) {
	var stderr bytes.Buffer
	confirmed := false
	ran := false

	err := setupDockerWith(dockerSetupDeps{
		dockerCmd: func() (string, error) {
			return "/usr/bin/docker", nil
		},
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			confirmed = true
			return true, nil
		},
		stderr:     &stderr,
		stdin:      strings.NewReader(""),
		scriptURL:  "http://127.0.0.1/docker.sh",
		httpClient: http.DefaultClient,
		runScript: func(string) error {
			ran = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("setupDockerWith returned error: %v", err)
	}
	if confirmed {
		t.Fatalf("setupDockerWith prompted even though docker was available")
	}
	if ran {
		t.Fatalf("setupDockerWith ran installer even though docker was available")
	}
	if stderr.Len() != 0 {
		t.Fatalf("setupDockerWith wrote stderr = %q, want empty", stderr.String())
	}
}

func TestSetupDockerDeclineFailsWithoutInstalling(t *testing.T) {
	var stderr bytes.Buffer
	ran := false

	err := setupDockerWith(dockerSetupDeps{
		dockerCmd: func() (string, error) {
			return "", errors.New("missing")
		},
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			return false, nil
		},
		stderr:     &stderr,
		stdin:      strings.NewReader("n\n"),
		scriptURL:  "http://127.0.0.1/docker.sh",
		httpClient: http.DefaultClient,
		runScript: func(string) error {
			ran = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "docker is required") {
		t.Fatalf("setupDockerWith error = %v, want docker required", err)
	}
	if ran {
		t.Fatalf("setupDockerWith ran installer after declined confirmation")
	}
	if got := stderr.String(); !strings.Contains(got, "Warning: docker is required but not installed") {
		t.Fatalf("setupDockerWith stderr = %q, want docker warning", got)
	}
}

func TestSetupDockerDownloadsAndRunsConfirmedScript(t *testing.T) {
	const script = "echo installing docker\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docker.sh" {
			t.Fatalf("request path = %q, want /docker.sh", r.URL.Path)
		}
		_, _ = io.WriteString(w, script)
	}))
	defer server.Close()

	var stderr bytes.Buffer
	var ranPath string
	err := setupDockerWith(dockerSetupDeps{
		dockerCmd: func() (string, error) {
			return "", errors.New("missing")
		},
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			return true, nil
		},
		stderr:     &stderr,
		stdin:      strings.NewReader("y\n"),
		scriptURL:  server.URL + "/docker.sh",
		httpClient: server.Client(),
		runScript: func(path string) error {
			ranPath = path
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(raw) != script {
				return fmt.Errorf("script content = %q, want %q", string(raw), script)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("setupDockerWith returned error: %v", err)
	}
	if ranPath == "" {
		t.Fatalf("setupDockerWith did not run installer")
	}
	if _, err := os.Stat(ranPath); !os.IsNotExist(err) {
		t.Fatalf("installer temp path still exists or stat failed: %v", err)
	}
}

func TestSetupDockerEnvInstallsWithoutPrompt(t *testing.T) {
	const script = "echo installing docker\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, script)
	}))
	defer server.Close()

	confirmed := false
	ran := false
	err := setupDockerWith(dockerSetupDeps{
		dockerCmd: func() (string, error) {
			return "", errors.New("missing")
		},
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			confirmed = true
			return false, nil
		},
		getenv: func(key string) string {
			if key == "CATCH_INSTALL_DOCKER" {
				return "1"
			}
			return ""
		},
		stderr:     io.Discard,
		stdin:      strings.NewReader(""),
		scriptURL:  server.URL + "/docker.sh",
		httpClient: server.Client(),
		runScript: func(string) error {
			ran = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("setupDockerWith returned error: %v", err)
	}
	if confirmed {
		t.Fatal("setupDockerWith prompted despite CATCH_INSTALL_DOCKER=1")
	}
	if !ran {
		t.Fatal("setupDockerWith did not run installer")
	}
}

func TestNormalizeDockerSetupDepsFillsDefaults(t *testing.T) {
	deps := normalizeDockerSetupDeps(dockerSetupDeps{})
	if deps.dockerCmd == nil || deps.confirm == nil || deps.stdin == nil || deps.stderr == nil || deps.getenv == nil ||
		deps.scriptURL == "" || deps.httpClient == nil || deps.runScript == nil {
		t.Fatalf("normalizeDockerSetupDeps left default unset: %#v", deps)
	}
}

func TestConfirmDockerInstallErrorPaths(t *testing.T) {
	writeErr := errors.New("write failed")
	_, err := confirmDockerInstall(strings.NewReader(""), writerFunc(func([]byte) (int, error) {
		return 0, writeErr
	}), func(io.Reader, io.Writer, string) (bool, error) {
		t.Fatalf("confirm should not be called after warning write failure")
		return false, nil
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("confirmDockerInstall write error = %v, want %v", err, writeErr)
	}

	confirmErr := errors.New("confirm failed")
	var out strings.Builder
	_, err = confirmDockerInstall(strings.NewReader(""), &out, func(io.Reader, io.Writer, string) (bool, error) {
		return false, confirmErr
	})
	if !errors.Is(err, confirmErr) || !strings.Contains(err.Error(), "failed to confirm") {
		t.Fatalf("confirmDockerInstall confirm error = %v, want wrapped %v", err, confirmErr)
	}
}

func TestDownloadDockerInstallScriptErrorPaths(t *testing.T) {
	if err := downloadDockerInstallScript(http.DefaultClient, "://bad-url", io.Discard); err == nil || !strings.Contains(err.Error(), "failed to create") {
		t.Fatalf("bad URL error = %v, want failed to create", err)
	}

	wantErr := errors.New("network down")
	err := downloadDockerInstallScript(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, wantErr
	}), "http://example.test/docker.sh", io.Discard)
	if !errors.Is(err, wantErr) {
		t.Fatalf("download error = %v, want %v", err, wantErr)
	}

	bodyErr := errors.New("body failed")
	err = downloadDockerInstallScript(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{Body: io.NopCloser(errReader{err: bodyErr})}, nil
	}), "http://example.test/docker.sh", io.Discard)
	if !errors.Is(err, bodyErr) {
		t.Fatalf("body error = %v, want %v", err, bodyErr)
	}
}

func TestExecuteDockerInstallScriptWrapsErrors(t *testing.T) {
	wantErr := errors.New("script failed")
	err := executeDockerInstallScript(func(string) error { return wantErr }, "/tmp/install.sh")
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "failed to run") {
		t.Fatalf("executeDockerInstallScript error = %v, want wrapped %v", err, wantErr)
	}
}

func TestCatchFileInstallerConfigWritesCustomServicesRoot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	servicesRoot := filepath.Join(t.TempDir(), "services")

	got := catchFileInstallerConfig(selectCatchInstallMode(dataDir, servicesRoot, "catch-test")).Args
	want := []string{
		fmt.Sprintf("--data-dir=%v", dataDir),
		fmt.Sprintf("--services-root=%v", servicesRoot),
		"--tsnet-host=catch-test",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installer args = %#v, want %#v", got, want)
	}
}

func TestCatchFileInstallerConfigOmitsDerivedServicesRoot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	got := catchFileInstallerConfig(selectCatchInstallMode(dataDir, filepath.Join(dataDir, "services"), "catch-test")).Args
	want := []string{
		fmt.Sprintf("--data-dir=%v", dataDir),
		"--tsnet-host=catch-test",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installer args = %#v, want %#v", got, want)
	}
}

func TestDoInstallOrdersJailerUpgradeAroundCatchInstall(t *testing.T) {
	var got []string
	ts := &fakeInstallTSNet{}
	inst := &fakeCatchInstaller{rollbackAvailable: true, closeHook: func() { got = append(got, "install-catch") }}
	deps := successfulCatchInstallDeps(ts, inst)
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		got = append(got, "prepare-vm-units")
		return &fakeCatchVMJailerUpgrade{commit: func() error {
			got = append(got, "commit-vm-units")
			return nil
		}, summary: catch.VMJailerUpgradeSummary{Ready: []string{"alpha"}}}, nil
	}

	if err := doInstallWith(&catch.Config{}, t.TempDir(), deps); err != nil {
		t.Fatalf("doInstallWith: %v", err)
	}
	want := []string{"prepare-vm-units", "install-catch", "commit-vm-units"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestDoInstallJailerPreparationFailureLeavesCatchUntouched(t *testing.T) {
	prepareErr := errors.New("ensure VM runtime identity")
	installerCalls := 0
	inst := &fakeCatchInstaller{}
	deps := successfulCatchInstallDeps(&fakeInstallTSNet{}, inst)
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		return nil, prepareErr
	}
	deps.newInstaller = func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) {
		installerCalls++
		return inst, nil
	}

	err := runCatchInstallTransaction(&catch.Config{}, t.TempDir(), deps)
	if !errors.Is(err, prepareErr) {
		t.Fatalf("runCatchInstallTransaction error = %v, want %v", err, prepareErr)
	}
	if installerCalls != 0 {
		t.Fatalf("Catch installer creation calls = %d, want 0", installerCalls)
	}
	if inst.closeCalls != 0 || inst.Len() != 0 {
		t.Fatalf("Catch installer state after preparation failure = close calls %d, bytes %d; want untouched", inst.closeCalls, inst.Len())
	}
}

func TestDoInstallJailerCommitFailureRollsBackCatchGeneration(t *testing.T) {
	commitErr := errors.New("replace VM unit")
	rollbackErr := errors.New("restore Catch generation")
	var order []string
	rollbackCalls := 0
	inst := &fakeCatchInstaller{rollbackAvailable: true, rollbackHook: func() error {
		rollbackCalls++
		order = append(order, "rollback-catch")
		return rollbackErr
	}}
	upgradeCloseCalls := 0
	deps := successfulCatchInstallDeps(&fakeInstallTSNet{}, inst)
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		return &fakeCatchVMJailerUpgrade{
			commit: func() error {
				order = append(order, "restore-vm-units")
				return commitErr
			},
			close: func() error {
				upgradeCloseCalls++
				return nil
			},
			summary: catch.VMJailerUpgradeSummary{Ready: []string{"alpha"}},
		}, nil
	}

	err := doInstallWith(&catch.Config{}, t.TempDir(), deps)
	if !errors.Is(err, commitErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("doInstallWith error = %v, want commit and rollback errors", err)
	}
	if rollbackCalls != 1 {
		t.Fatalf("Catch generation rollback calls = %d, want 1", rollbackCalls)
	}
	if upgradeCloseCalls != 1 {
		t.Fatalf("upgrade Close calls = %d, want 1", upgradeCloseCalls)
	}
	if !reflect.DeepEqual(order, []string{"restore-vm-units", "rollback-catch"}) {
		t.Fatalf("rollback order = %v, want VM units before Catch", order)
	}
}

func TestDoInstallCatchInstallFailureClosesStagedJailerUpgradeWithoutCommit(t *testing.T) {
	staged := filepath.Join(t.TempDir(), "vm-unit.staged")
	if err := os.WriteFile(staged, []byte("unit"), 0o644); err != nil {
		t.Fatalf("WriteFile staged unit: %v", err)
	}
	installErr := errors.New("install Catch")
	commitCalls := 0
	closeCalls := 0
	installer := &fakeCatchInstaller{rollbackAvailable: true, closeErr: installErr}
	deps := successfulCatchInstallDeps(&fakeInstallTSNet{}, installer)
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		return &fakeCatchVMJailerUpgrade{
			commit: func() error {
				commitCalls++
				return nil
			},
			close: func() error {
				closeCalls++
				return os.Remove(staged)
			},
			summary: catch.VMJailerUpgradeSummary{Ready: []string{"alpha"}},
		}, nil
	}

	err := doInstallWith(&catch.Config{}, t.TempDir(), deps)
	if !errors.Is(err, installErr) {
		t.Fatalf("doInstallWith error = %v, want %v", err, installErr)
	}
	if commitCalls != 0 {
		t.Fatalf("VM unit Commit calls = %d, want 0", commitCalls)
	}
	if closeCalls != 1 {
		t.Fatalf("VM unit Close calls = %d, want 1", closeCalls)
	}
	if installer.closeCalls != 1 {
		t.Fatalf("Catch installer Close calls = %d, want one install attempt", installer.closeCalls)
	}
	if _, statErr := os.Stat(staged); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged unit still exists after Catch install failure: %v", statErr)
	}
}

func TestDoInstallJoinsCatchInstallAndJailerCleanupErrors(t *testing.T) {
	installErr := errors.New("install Catch")
	cleanupErr := errors.New("remove staged VM units")
	tsCloseErr := errors.New("close install tsnet")
	deps := successfulCatchInstallDeps(&fakeInstallTSNet{closeErr: tsCloseErr}, &fakeCatchInstaller{rollbackAvailable: true, closeErr: installErr})
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		return &fakeCatchVMJailerUpgrade{
			commit:  func() error { return nil },
			close:   func() error { return cleanupErr },
			summary: catch.VMJailerUpgradeSummary{Ready: []string{"alpha"}},
		}, nil
	}

	err := doInstallWith(&catch.Config{}, t.TempDir(), deps)
	if !errors.Is(err, installErr) || !errors.Is(err, cleanupErr) || !errors.Is(err, tsCloseErr) {
		t.Fatalf("doInstallWith error = %v, want install and all cleanup errors", err)
	}
}

func TestDoInstallNonemptyJailerUpgradeRequiresCatchRollbackGeneration(t *testing.T) {
	installCalls := 0
	commitCalls := 0
	installer := &fakeCatchInstaller{closeHook: func() { installCalls++ }}
	deps := successfulCatchInstallDeps(&fakeInstallTSNet{}, installer)
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		return &fakeCatchVMJailerUpgrade{
			commit: func() error {
				commitCalls++
				return nil
			},
			summary: catch.VMJailerUpgradeSummary{Ready: []string{"alpha"}},
		}, nil
	}

	err := doInstallWith(&catch.Config{}, t.TempDir(), deps)
	if err == nil || !strings.Contains(err.Error(), "no previous Catch generation") {
		t.Fatalf("doInstallWith error = %v, want rollback availability failure", err)
	}
	if installCalls != 0 {
		t.Fatalf("Catch install calls = %d, want 0", installCalls)
	}
	if commitCalls != 0 {
		t.Fatalf("VM unit commit calls = %d, want 0", commitCalls)
	}
	if !installer.failed || installer.closeCalls != 1 {
		t.Fatalf("installer failed/closeCalls = %t/%d, want failed cleanup close once", installer.failed, installer.closeCalls)
	}
}

func TestDoInstallEmptyJailerUpgradeAllowsInitialCatchInstall(t *testing.T) {
	installCalls := 0
	installer := &fakeCatchInstaller{closeHook: func() { installCalls++ }}
	deps := successfulCatchInstallDeps(&fakeInstallTSNet{}, installer)
	deps.prepareVMJailerUpgrade = emptyCatchVMJailerUpgrade

	if err := doInstallWith(&catch.Config{}, t.TempDir(), deps); err != nil {
		t.Fatalf("doInstallWith: %v", err)
	}
	if installCalls != 1 {
		t.Fatalf("Catch install calls = %d, want 1", installCalls)
	}
}

func TestDoInstallHoldsInstallLockThroughUpgradeClose(t *testing.T) {
	var order []string
	installer := &fakeCatchInstaller{rollbackAvailable: true, closeHook: func() { order = append(order, "install-catch") }}
	deps := successfulCatchInstallDeps(&fakeInstallTSNet{}, installer)
	deps.acquireInstallLock = func(context.Context, string) (io.Closer, error) {
		order = append(order, "acquire-install-lock")
		return closeFunc(func() error {
			order = append(order, "release-install-lock")
			return nil
		}), nil
	}
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		order = append(order, "prepare-vm-units")
		return &fakeCatchVMJailerUpgrade{
			commit: func() error {
				order = append(order, "commit-vm-units")
				return nil
			},
			close: func() error {
				order = append(order, "close-vm-upgrade")
				return nil
			},
			summary: catch.VMJailerUpgradeSummary{Ready: []string{"alpha"}},
		}, nil
	}

	if err := doInstallWith(&catch.Config{}, t.TempDir(), deps); err != nil {
		t.Fatalf("doInstallWith: %v", err)
	}
	want := []string{"acquire-install-lock", "prepare-vm-units", "install-catch", "commit-vm-units", "close-vm-upgrade", "release-install-lock"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestAcquireCatchInstallLockSerializesAndHonorsCancellation(t *testing.T) {
	dataDir := t.TempDir()
	uid := uint32(os.Geteuid())
	first, err := acquireCatchInstallLock(context.Background(), dataDir, uid)
	if err != nil {
		t.Fatalf("acquire first install lock: %v", err)
	}
	defer func() { _ = first.Close() }()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireCatchInstallLock(canceled, dataDir, uid); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock error = %v, want context canceled", err)
	}

	acquired := make(chan io.Closer, 1)
	errs := make(chan error, 1)
	go func() {
		lock, err := acquireCatchInstallLock(context.Background(), dataDir, uid)
		if err != nil {
			errs <- err
			return
		}
		acquired <- lock
	}()
	select {
	case lock := <-acquired:
		_ = lock.Close()
		t.Fatal("second transaction acquired install lock before first released it")
	case err := <-errs:
		t.Fatalf("second transaction lock failed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first install lock: %v", err)
	}
	select {
	case lock := <-acquired:
		if err := lock.Close(); err != nil {
			t.Fatalf("release second install lock: %v", err)
		}
	case err := <-errs:
		t.Fatalf("second transaction lock failed after release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("second transaction did not acquire install lock after release")
	}
}

func TestAcquireCatchInstallLockValidatesDataRootOwnerAndMode(t *testing.T) {
	t.Run("owner", func(t *testing.T) {
		if lock, err := acquireCatchInstallLock(context.Background(), t.TempDir(), uint32(os.Geteuid()+1)); err == nil {
			_ = lock.Close()
			t.Fatal("acquireCatchInstallLock accepted unexpected owner")
		}
	})
	t.Run("mode", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := os.Chmod(dataDir, 0o777); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
		if lock, err := acquireCatchInstallLock(context.Background(), dataDir, uint32(os.Geteuid())); err == nil {
			_ = lock.Close()
			t.Fatal("acquireCatchInstallLock accepted group/other-writable data root")
		}
	})
}

func successfulCatchInstallDeps(ts installTSNet, inst catchServiceInstaller) catchInstallDeps {
	return catchInstallDeps{
		writeInstallMeta: func(string) error { return nil },
		initTSNet:        func(string) (installTSNet, error) { return ts, nil },
		newInstaller: func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) {
			return inst, nil
		},
		executable: func() (string, error) { return "/tmp/catch-bin", nil },
		readFile:   func(string) ([]byte, error) { return []byte("binary"), nil },
		logf:       func(string, ...any) {},
		tsnetHost:  func() string { return "catch-test" },
		acquireInstallLock: func(context.Context, string) (io.Closer, error) {
			return closeFunc(func() error { return nil }), nil
		},
	}
}

func TestDoInstallWritesCurrentExecutableWithGeneratedServiceConfig(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &catch.Config{ServicesRoot: filepath.Join(dataDir, "services")}
	ts := &fakeInstallTSNet{}
	inst := &fakeCatchInstaller{}

	var metaDir string
	var gotCfg *catch.Config
	var gotInstallerCfg catch.FileInstallerCfg
	err := doInstallWith(cfg, dataDir, catchInstallDeps{
		writeInstallMeta: func(dir string) error {
			metaDir = dir
			return nil
		},
		initTSNet: func(dir string) (installTSNet, error) {
			if dir != dataDir {
				t.Fatalf("initTSNet dir = %q, want %q", dir, dataDir)
			}
			return ts, nil
		},
		newInstaller: func(cfg *catch.Config, installerCfg catch.FileInstallerCfg) (catchServiceInstaller, error) {
			gotCfg = cfg
			gotInstallerCfg = installerCfg
			return inst, nil
		},
		executable: func() (string, error) {
			return "/tmp/catch-bin", nil
		},
		readFile: func(path string) ([]byte, error) {
			if path != "/tmp/catch-bin" {
				t.Fatalf("readFile path = %q, want /tmp/catch-bin", path)
			}
			return []byte("binary"), nil
		},
		logf: func(string, ...any) {},
		tsnetHost: func() string {
			return "catch-test"
		},
		prepareVMJailerUpgrade: emptyCatchVMJailerUpgrade,
		acquireInstallLock: func(context.Context, string) (io.Closer, error) {
			return closeFunc(func() error { return nil }), nil
		},
	})
	if err != nil {
		t.Fatalf("doInstallWith returned error: %v", err)
	}
	if metaDir != dataDir {
		t.Fatalf("writeInstallMeta dir = %q, want %q", metaDir, dataDir)
	}
	if gotCfg != cfg {
		t.Fatalf("newInstaller cfg = %p, want %p", gotCfg, cfg)
	}
	if gotInstallerCfg.ServiceName != catch.CatchService {
		t.Fatalf("installer service = %q, want %q", gotInstallerCfg.ServiceName, catch.CatchService)
	}
	wantArgs := []string{
		fmt.Sprintf("--data-dir=%v", dataDir),
		"--tsnet-host=catch-test",
	}
	if !reflect.DeepEqual(gotInstallerCfg.Args, wantArgs) {
		t.Fatalf("installer args = %#v, want %#v", gotInstallerCfg.Args, wantArgs)
	}
	if got := inst.String(); got != "binary" {
		t.Fatalf("installer wrote %q, want binary", got)
	}
	if inst.failed {
		t.Fatalf("installer was marked failed")
	}
	if !inst.closed {
		t.Fatalf("installer was not closed")
	}
	if !ts.closed {
		t.Fatalf("tsnet server was not closed")
	}
}

func TestDoInstallExecutableErrorFailsInstaller(t *testing.T) {
	dataDir := t.TempDir()
	ts := &fakeInstallTSNet{}
	inst := &fakeCatchInstaller{}

	err := doInstallWith(&catch.Config{}, dataDir, catchInstallDeps{
		writeInstallMeta: func(string) error { return nil },
		initTSNet:        func(string) (installTSNet, error) { return ts, nil },
		newInstaller: func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) {
			return inst, nil
		},
		executable: func() (string, error) {
			return "", errors.New("boom")
		},
		readFile: func(string) ([]byte, error) {
			t.Fatalf("readFile should not be called after executable error")
			return nil, nil
		},
		logf:                   func(string, ...any) {},
		tsnetHost:              func() string { return "catch-test" },
		prepareVMJailerUpgrade: emptyCatchVMJailerUpgrade,
		acquireInstallLock: func(context.Context, string) (io.Closer, error) {
			return closeFunc(func() error { return nil }), nil
		},
	})
	if err == nil {
		t.Fatalf("doInstallWith succeeded")
	}
	if !strings.Contains(err.Error(), "failed to get executable path") {
		t.Fatalf("doInstallWith error = %q, want executable path failure", err)
	}
	if !inst.failed {
		t.Fatalf("installer was not marked failed")
	}
	if !inst.closed {
		t.Fatalf("installer was not closed")
	}
	if !ts.closed {
		t.Fatalf("tsnet server was not closed")
	}
}

func TestDoInstallRequiresTSNet(t *testing.T) {
	newInstallerCalled := false
	err := doInstallWith(&catch.Config{}, t.TempDir(), catchInstallDeps{
		writeInstallMeta: func(string) error { return nil },
		initTSNet:        func(string) (installTSNet, error) { return nil, nil },
		newInstaller: func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) {
			newInstallerCalled = true
			return nil, nil
		},
		executable:             func() (string, error) { return "/tmp/catch-bin", nil },
		readFile:               func(string) ([]byte, error) { return []byte("binary"), nil },
		logf:                   func(string, ...any) {},
		tsnetHost:              func() string { return "catch-test" },
		prepareVMJailerUpgrade: emptyCatchVMJailerUpgrade,
		acquireInstallLock: func(context.Context, string) (io.Closer, error) {
			return closeFunc(func() error { return nil }), nil
		},
	})
	if err == nil {
		t.Fatalf("doInstallWith succeeded")
	}
	if !strings.Contains(err.Error(), "failed to initialize tsnet") {
		t.Fatalf("doInstallWith error = %q, want tsnet failure", err)
	}
	if newInstallerCalled {
		t.Fatalf("newInstaller was called after tsnet initialization failed")
	}
}

func TestDoInstallValidationAndInstallerErrors(t *testing.T) {
	if err := doInstallWith(nil, t.TempDir(), catchInstallDeps{logf: func(string, ...any) {}}); err == nil || !strings.Contains(err.Error(), "catch config is required") {
		t.Fatalf("nil config error = %v, want config required", err)
	}

	ts := &fakeInstallTSNet{}
	wantErr := errors.New("installer failed")
	err := doInstallWith(&catch.Config{}, t.TempDir(), catchInstallDeps{
		writeInstallMeta: func(string) error { return nil },
		initTSNet:        func(string) (installTSNet, error) { return ts, nil },
		newInstaller: func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) {
			return nil, wantErr
		},
		executable:             func() (string, error) { return "/tmp/catch-bin", nil },
		readFile:               func(string) ([]byte, error) { return []byte("binary"), nil },
		logf:                   func(string, ...any) {},
		tsnetHost:              func() string { return "catch-test" },
		prepareVMJailerUpgrade: emptyCatchVMJailerUpgrade,
		acquireInstallLock: func(context.Context, string) (io.Closer, error) {
			return closeFunc(func() error { return nil }), nil
		},
	})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "failed to create installer") {
		t.Fatalf("installer error = %v, want wrapped %v", err, wantErr)
	}
	if !ts.closed {
		t.Fatalf("tsnet server was not closed after installer error")
	}
}

func TestWriteCurrentExecutableReadAndWriteErrorsFailInstaller(t *testing.T) {
	readErr := errors.New("read failed")
	inst := &fakeCatchInstaller{}
	err := writeCurrentExecutable(inst, catchInstallDeps{
		executable: func() (string, error) { return "/tmp/catch-bin", nil },
		readFile:   func(string) ([]byte, error) { return nil, readErr },
	})
	if !errors.Is(err, readErr) || !inst.failed {
		t.Fatalf("read error = %v failed=%v, want wrapped read error and failed installer", err, inst.failed)
	}

	writeErr := errors.New("write failed")
	inst = &fakeCatchInstaller{writeErr: writeErr}
	err = writeCurrentExecutable(inst, catchInstallDeps{
		executable: func() (string, error) { return "/tmp/catch-bin", nil },
		readFile:   func(string) ([]byte, error) { return []byte("binary"), nil },
	})
	if !errors.Is(err, writeErr) || !inst.failed {
		t.Fatalf("write error = %v failed=%v, want wrapped write error and failed installer", err, inst.failed)
	}
}

func TestNormalizeCatchInstallDepsFillsDefaults(t *testing.T) {
	deps := normalizeCatchInstallDeps(catchInstallDeps{})
	if deps.writeInstallMeta == nil || deps.initTSNet == nil || deps.newInstaller == nil ||
		deps.executable == nil || deps.readFile == nil || deps.logf == nil || deps.tsnetHost == nil {
		t.Fatalf("normalizeCatchInstallDeps left default unset: %#v", deps)
	}
}

func TestInstallMetaFallbacksAndErrors(t *testing.T) {
	t.Setenv("CATCH_INSTALL_HOST", "")
	if got := detectInstallHost(); got == "" {
		t.Fatalf("detectInstallHost returned empty fallback hostname")
	}
	if got, err := currentUsername(); err != nil || got == "" {
		t.Fatalf("currentUsername = %q, %v; want non-empty username", got, err)
	}

	if _, err := readInstallMeta(t.TempDir()); err == nil {
		t.Fatalf("readInstallMeta succeeded for missing metadata")
	}
	root := t.TempDir()
	if err := os.WriteFile(installMetaPath(root), []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid install meta: %v", err)
	}
	if _, err := readInstallMeta(root); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("readInstallMeta invalid error = %v, want JSON error", err)
	}

	cfg := &catch.Config{InstallUser: "default-user", InstallHost: "default-host"}
	applyInstallMeta(cfg, t.TempDir())
	if cfg.InstallUser != "default-user" || cfg.InstallHost != "default-host" {
		t.Fatalf("applyInstallMeta changed cfg on missing metadata: %#v", cfg)
	}
}

type fakeInstallTSNet struct {
	closed   bool
	closeErr error
}

func (f *fakeInstallTSNet) Close() error {
	f.closed = true
	return f.closeErr
}

type fakeCatchInstaller struct {
	bytes.Buffer
	closed            bool
	closeCalls        int
	closeErr          error
	failed            bool
	writeErr          error
	closeHook         func()
	rollbackHook      func() error
	rollbackAvailable bool
}

func (f *fakeCatchInstaller) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.Buffer.Write(p)
}

func (f *fakeCatchInstaller) Close() error {
	f.closed = true
	f.closeCalls++
	if !f.failed && f.closeHook != nil {
		f.closeHook()
	}
	return f.closeErr
}

func (f *fakeCatchInstaller) RollbackInstalledGenerationAvailable() bool {
	return f.rollbackAvailable
}

func (f *fakeCatchInstaller) RollbackInstalledGeneration() error {
	if f.rollbackHook == nil {
		return nil
	}
	return f.rollbackHook()
}

func (f *fakeCatchInstaller) Fail() {
	f.failed = true
}

type fakeCatchVMJailerUpgrade struct {
	commit  func() error
	close   func() error
	summary catch.VMJailerUpgradeSummary
}

func (f *fakeCatchVMJailerUpgrade) Commit() error {
	if f.commit == nil {
		return nil
	}
	return f.commit()
}

func (f *fakeCatchVMJailerUpgrade) Close() error {
	if f.close == nil {
		return nil
	}
	return f.close()
}

func (f *fakeCatchVMJailerUpgrade) Summary() catch.VMJailerUpgradeSummary {
	return f.summary
}

func emptyCatchVMJailerUpgrade(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
	return &fakeCatchVMJailerUpgrade{}, nil
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

func TestHandleSpecialCommand(t *testing.T) {
	var out strings.Builder
	handled, err := handleSpecialCommand([]string{"is-catch"}, &out)
	if err != nil {
		t.Fatalf("handleSpecialCommand returned error: %v", err)
	}
	if !handled {
		t.Fatalf("handleSpecialCommand did not handle is-catch")
	}
	if got := strings.TrimSpace(out.String()); got != "yes" {
		t.Fatalf("handleSpecialCommand output = %q, want yes", got)
	}

	out.Reset()
	handled, err = handleSpecialCommand(nil, &out)
	if err != nil {
		t.Fatalf("handleSpecialCommand returned error for no args: %v", err)
	}
	if handled {
		t.Fatalf("handleSpecialCommand handled no args")
	}
	if out.Len() != 0 {
		t.Fatalf("handleSpecialCommand wrote output for no args: %q", out.String())
	}

	out.Reset()
	handled, err = handleSpecialCommand([]string{"unknown"}, &out)
	if err != nil {
		t.Fatalf("handleSpecialCommand returned error for unknown command: %v", err)
	}
	if handled {
		t.Fatalf("handleSpecialCommand handled unknown command")
	}
}

func TestListenDockerPluginSocketRemovesStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "yeet-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sock := filepath.Join(dir, "plugins", "yeet.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	ln, err := listenDockerPluginSocket(sock)
	if err != nil {
		t.Fatalf("listenDockerPluginSocket: %v", err)
	}
	defer logClose("test unix listener", ln)
	defer logRemove(sock)

	if got := ln.Addr().String(); got != sock {
		t.Fatalf("listener addr = %q, want %q", got, sock)
	}
}

func TestListenDockerPluginSocketRefusesLiveSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "yeet-live-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sock := filepath.Join(dir, "plugins", "yeet.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on live socket: %v", err)
	}
	defer logClose("live test listener", ln)

	if _, err := listenDockerPluginSocket(sock); err == nil || !strings.Contains(err.Error(), "already accepting connections") {
		t.Fatalf("listenDockerPluginSocket live socket error = %v, want already accepting connections", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("live socket was removed: %v", err)
	}
}

func TestAcquireCatchServerLockExcludesSecondServer(t *testing.T) {
	dir := t.TempDir()
	lock, err := acquireCatchServerLock(dir)
	if err != nil {
		t.Fatalf("acquireCatchServerLock first: %v", err)
	}

	if second, err := acquireCatchServerLock(dir); err == nil {
		_ = second.Close()
		t.Fatal("second acquireCatchServerLock succeeded, want already running error")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquireCatchServerLock error = %v, want already running", err)
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	lock, err = acquireCatchServerLock(dir)
	if err != nil {
		t.Fatalf("acquireCatchServerLock after close: %v", err)
	}
	defer logClose("test catch server lock", lock)
}

func TestDockerPluginSocketAndListenErrors(t *testing.T) {
	if got := dockerPluginSocket(); got != filepath.Join("/run/docker/plugins", "yeet.sock") {
		t.Fatalf("dockerPluginSocket = %q", got)
	}

	root := t.TempDir()
	parentFile := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	if _, err := listenDockerPluginSocket(filepath.Join(parentFile, "yeet.sock")); err == nil || !strings.Contains(err.Error(), "failed to create socket dir") {
		t.Fatalf("listenDockerPluginSocket parent-file error = %v, want socket dir error", err)
	}

	nonEmptyDir := filepath.Join(root, "non-empty")
	if err := os.Mkdir(nonEmptyDir, 0o755); err != nil {
		t.Fatalf("mkdir non-empty: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}
	if err := removeStaleSocket(nonEmptyDir); err == nil || !strings.Contains(err.Error(), "failed to remove stale socket") {
		t.Fatalf("removeStaleSocket non-empty dir error = %v, want wrapped remove error", err)
	}
}

func TestCloseAndRemoveHelpers(t *testing.T) {
	var target error
	closeErr := errors.New("close failed")
	assignOrLogClose(&target, "test closer", closeErrorerFunc(func() error {
		return closeErr
	}))
	if !errors.Is(target, closeErr) {
		t.Fatalf("assignOrLogClose target = %v, want closeErr", target)
	}

	original := errors.New("original")
	target = original
	assignOrLogClose(&target, "test closer", closeErrorerFunc(func() error {
		return closeErr
	}))
	if !errors.Is(target, original) {
		t.Fatalf("assignOrLogClose replaced existing target: %v", target)
	}

	logClose("closed file", closeErrorerFunc(func() error {
		return os.ErrClosed
	}))
	logClose("clean closer", closeErrorerFunc(func() error {
		return nil
	}))
	logClose("failing closer", closeErrorerFunc(func() error {
		return closeErr
	}))

	missing := filepath.Join(t.TempDir(), "missing")
	logRemove(missing)
	if err := removeStaleSocket(missing); err != nil {
		t.Fatalf("removeStaleSocket missing: %v", err)
	}
	existing := filepath.Join(t.TempDir(), "stale.sock")
	if err := os.WriteFile(existing, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale socket: %v", err)
	}
	if err := removeStaleSocket(existing); err != nil {
		t.Fatalf("removeStaleSocket existing: %v", err)
	}
	if _, err := os.Stat(existing); !os.IsNotExist(err) {
		t.Fatalf("stale socket still exists or stat failed: %v", err)
	}
}

type closeErrorerFunc func() error

func (f closeErrorerFunc) Close() error {
	return f()
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) {
	return f(p)
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}
