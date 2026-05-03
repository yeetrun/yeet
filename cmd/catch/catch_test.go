// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
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

	cfg := newCatchConfig(paths, "catch-user", "127.0.0.1:0", filepath.Join(root, "containerd.sock"))
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

	if got := initTSNet(t.TempDir()); got != nil {
		t.Fatalf("initTSNet() = %#v, want nil when tsnet host is empty", got)
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

func TestSetupDockerDeclineSkipsInstall(t *testing.T) {
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
	if err != nil {
		t.Fatalf("setupDockerWith returned error: %v", err)
	}
	if ran {
		t.Fatalf("setupDockerWith ran installer after declined confirmation")
	}
	if got := stderr.String(); !strings.Contains(got, "Warning: docker is recommended but not installed") {
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

func TestNormalizeDockerSetupDepsFillsDefaults(t *testing.T) {
	deps := normalizeDockerSetupDeps(dockerSetupDeps{})
	if deps.dockerCmd == nil || deps.confirm == nil || deps.stdin == nil || deps.stderr == nil ||
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

func TestDoInstallWritesCurrentExecutableWithGeneratedServiceConfig(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &catch.Config{}
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
		initTSNet: func(dir string) installTSNet {
			if dir != dataDir {
				t.Fatalf("initTSNet dir = %q, want %q", dir, dataDir)
			}
			return ts
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
		initTSNet:        func(string) installTSNet { return ts },
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
		logf:      func(string, ...any) {},
		tsnetHost: func() string { return "catch-test" },
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
		initTSNet:        func(string) installTSNet { return nil },
		newInstaller: func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) {
			newInstallerCalled = true
			return nil, nil
		},
		executable: func() (string, error) { return "/tmp/catch-bin", nil },
		readFile:   func(string) ([]byte, error) { return []byte("binary"), nil },
		logf:       func(string, ...any) {},
		tsnetHost:  func() string { return "catch-test" },
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
		initTSNet:        func(string) installTSNet { return ts },
		newInstaller: func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) {
			return nil, wantErr
		},
		executable: func() (string, error) { return "/tmp/catch-bin", nil },
		readFile:   func(string) ([]byte, error) { return []byte("binary"), nil },
		logf:       func(string, ...any) {},
		tsnetHost:  func() string { return "catch-test" },
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
	closed   bool
	closeErr error
	failed   bool
	writeErr error
}

func (f *fakeCatchInstaller) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.Buffer.Write(p)
}

func (f *fakeCatchInstaller) Close() error {
	f.closed = true
	return f.closeErr
}

func (f *fakeCatchInstaller) Fail() {
	f.failed = true
}

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
