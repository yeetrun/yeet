// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
)

func TestTailscaleMonitorLoopReturnsAfterStableIDStored(t *testing.T) {
	var calls int
	err := runTailscaleMonitorLoop(
		context.Background(),
		func() error {
			calls++
			return nil
		},
		func(error) {
			t.Fatal("backoff should not run after successful stable ID storage")
		},
	)
	if err != nil {
		t.Fatalf("runTailscaleMonitorLoop returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("watch calls = %d, want 1", calls)
	}
}

func TestSystemdIdentityInstallPlanSurfaces(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unitArtifact := filepath.Join(root, "api.service")
	envArtifact := filepath.Join(root, "env-source")
	if err := os.WriteFile(unitArtifact, []byte("[Service]\nWorkingDirectory=/srv/api\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envArtifact, []byte("A=B\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	config := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): unitArtifact}},
			db.ArtifactEnvFile:     {Refs: map[db.ArtifactRef]string{db.Gen(1): envArtifact}},
		},
	}
	service, err := NewSystemdService(nil, config.View(), runDir)
	if err != nil {
		t.Fatal(err)
	}
	oldFlistxattr := systemdInstallTargetFlistxattr
	systemdInstallTargetFlistxattr = func(int, []byte) (int, error) { return 0, nil }
	t.Cleanup(func() { systemdInstallTargetFlistxattr = oldFlistxattr })
	paths := service.InstallTargetPaths()
	if len(paths) < 2 || service.PrimaryUnitPath() == "" || len(service.InstallUnits()) == 0 {
		t.Fatalf("install plan = paths:%v primary:%q units:%v", paths, service.PrimaryUnitPath(), service.InstallUnits())
	}
	states, err := service.InstallTargetStatesExcluding(service.PrimaryUnitPath())
	if err != nil {
		t.Fatalf("install target states: %v", err)
	}
	foundEnv := false
	for _, state := range states {
		if state.Present && state.Size == int64(len("A=B\n")) {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Fatalf("install target states = %#v", states)
	}
	destination := filepath.Join(root, "installed.service")
	if err := writeInstalledSystemdUnit(destination, []byte("[Service]\nUser=app\n"), 0o640); err != nil {
		t.Fatalf("write installed unit: %v", err)
	}
	if info, err := os.Stat(destination); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("installed unit mode = %v, %v", info, err)
	}
	rewritten, err := rewriteInstalledSystemdUnitIdentity("[Service]\nWorkingDirectory=/srv/api\n", "app", "app")
	if err != nil || !strings.Contains(rewritten, systemdIdentityEnvironment("app", "/srv/api")) {
		t.Fatalf("rewritten unit = %q, %v", rewritten, err)
	}
}

func TestRewriteInstalledSystemdUnitIdentityPreservesRecognizedHome(t *testing.T) {
	dataDir := "/srv/api/data"
	for _, tt := range []struct {
		name             string
		workingDirectory string
	}{
		{name: "sandbox on", workingDirectory: "/"},
		{name: "sandbox off", workingDirectory: dataDir},
		{name: "legacy", workingDirectory: dataDir},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := "[Service]\nWorkingDirectory=" + tt.workingDirectory + "\n" +
				"Environment=HOME=" + dataDir + " USER=root LOGNAME=root SHELL=/bin/sh\n"
			rewritten, err := rewriteInstalledSystemdUnitIdentity(raw, "app", "workers")
			if err != nil {
				t.Fatalf("rewrite installed identity: %v", err)
			}
			want := "Environment=HOME=" + dataDir + " USER=app LOGNAME=app SHELL=/bin/sh\n"
			if !strings.Contains(rewritten, want) {
				t.Fatalf("rewritten identity lost recognized HOME; want %q in:\n%s", want, rewritten)
			}
		})
	}
}

func TestSystemdInstallTargetXattrsOnlyIgnoresDarwinProvenance(t *testing.T) {
	for _, tt := range []struct {
		name string
		goos string
		raw  string
		want bool
	}{
		{name: "none", goos: "darwin", raw: "", want: false},
		{name: "Darwin provenance", goos: "darwin", raw: "com.apple.provenance\x00", want: false},
		{name: "Darwin workload xattr", goos: "darwin", raw: "com.apple.provenance\x00user.operator\x00", want: true},
		{name: "Linux provenance", goos: "linux", raw: "com.apple.provenance\x00", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := systemdInstallTargetXattrsUnsupportedForOS(tt.goos, []byte(tt.raw)); got != tt.want {
				t.Fatalf("unsupported xattrs = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSystemdServiceManagedArtifactMetadataUsesPersistedIdentity(t *testing.T) {
	identity := &db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1010}
	service := &SystemdService{cfg: (&db.Service{Name: "demo", Identity: identity}).View()}
	for _, tt := range []struct {
		name     string
		artifact db.ArtifactName
		mode     os.FileMode
		wantUID  uint32
		wantGID  uint32
		wantMode os.FileMode
	}{
		{name: "environment", artifact: db.ArtifactEnvFile, mode: 0o666, wantUID: uint32(os.Geteuid()), wantGID: 1010, wantMode: 0o640},
		{name: "tailscale binary", artifact: db.ArtifactTSBinary, mode: 0o777, wantUID: uint32(os.Geteuid()), wantGID: 1010, wantMode: 0o750},
		{name: "script", artifact: db.ArtifactTypeScriptFile, mode: 0o600, wantUID: 1002, wantGID: 1010, wantMode: 0o600},
	} {
		t.Run(tt.name, func(t *testing.T) {
			uid, gid, mode, ok := service.managedArtifactMetadata(tt.artifact, tt.mode)
			if !ok || uid != tt.wantUID || gid != tt.wantGID || mode != tt.wantMode {
				t.Fatalf("managed artifact metadata = %d:%d %04o, %t; want %d:%d %04o, true", uid, gid, mode, ok, tt.wantUID, tt.wantGID, tt.wantMode)
			}
		})
	}
}

func TestTailscaleMonitorLoopRetriesMissingSocket(t *testing.T) {
	var calls int
	var backoffs int
	err := runTailscaleMonitorLoop(
		context.Background(),
		func() error {
			calls++
			if calls == 1 {
				return os.ErrNotExist
			}
			return nil
		},
		func(error) {
			backoffs++
		},
	)
	if err != nil {
		t.Fatalf("runTailscaleMonitorLoop returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("watch calls = %d, want 2", calls)
	}
	if backoffs != 1 {
		t.Fatalf("backoffs = %d, want 1", backoffs)
	}
}

func TestTailscaleMonitorLoopReturnsNonSocketError(t *testing.T) {
	wantErr := errors.New("watch failed")
	err := runTailscaleMonitorLoop(
		context.Background(),
		func() error {
			return wantErr
		},
		func(error) {
			t.Fatal("backoff should not run for non-socket errors")
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runTailscaleMonitorLoop error = %v, want %v", err, wantErr)
	}
}

func TestSystemdUnitRendersExplicitDependencies(t *testing.T) {
	unit := SystemdUnit{
		Name:       "catch",
		Executable: "/usr/local/bin/catch",
		Wants:      "containerd.service",
		Requires:   "local-fs.target",
		After:      "containerd.service local-fs.target",
		Before:     "yeet-docker-prereqs.target docker.service",
		ExecStartPre: []string{
			"/bin/systemctl is-active --quiet yeet-demo-ns.service",
		},
		ExecStartPost: []string{
			"/bin/sh -c 'i=0; while [ \"$i\" -lt 600 ]; do [ -S /run/docker/plugins/yeet.sock ] && exit 0; i=$((i+1)); sleep 0.1; done; exit 1'",
		},
		WantedBy: "multi-user.target yeet-docker-prereqs.target",
	}

	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Wants=containerd.service\n",
		"Requires=local-fs.target\n",
		"After=containerd.service local-fs.target\n",
		"Before=yeet-docker-prereqs.target docker.service\n",
		"ExecStartPre=/bin/systemctl is-active --quiet yeet-demo-ns.service\n",
		"ExecStartPost=/bin/sh -c 'i=0; while [ \"$i\" -lt 600 ]; do [ -S /run/docker/plugins/yeet.sock ] && exit 0; i=$((i+1)); sleep 0.1; done; exit 1'\n",
		"WantedBy=multi-user.target yeet-docker-prereqs.target\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}

func TestSystemdUnitConditionExecutable(t *testing.T) {
	tests := []struct {
		name                string
		conditionExecutable string
		wantCondition       string
	}{
		{
			name:                "uses condition executable without changing ExecStart",
			conditionExecutable: "/srv/app/bin/tailscaled",
			wantCondition:       "ConditionFileIsExecutable=/srv/app/bin/tailscaled\n",
		},
		{
			name:          "defaults condition executable to ExecStart executable",
			wantCondition: "ConditionFileIsExecutable=/srv/catch/run/catch\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit := SystemdUnit{
				Name:                "yeet-app-ts",
				Executable:          "/srv/catch/run/catch",
				ConditionExecutable: tt.conditionExecutable,
				Arguments: []string{
					"tailscale-resolver-exec",
					"--source",
					"/etc/netns/yeet-app-ns/resolv.conf",
					"--",
					"/srv/app/bin/tailscaled",
					"--tun=ts0",
				},
			}

			paths, err := unit.WriteOutUnitFiles(t.TempDir())
			if err != nil {
				t.Fatalf("WriteOutUnitFiles: %v", err)
			}
			raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			got := string(raw)
			for _, want := range []string{
				tt.wantCondition,
				"ExecStart=/srv/catch/run/catch tailscale-resolver-exec --source /etc/netns/yeet-app-ns/resolv.conf -- /srv/app/bin/tailscaled --tun=ts0\n",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("unit missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestSystemdUnitRendersUserAndGroup(t *testing.T) {
	unit := SystemdUnit{
		Name:             "api",
		Executable:       "/var/lib/yeet/services/api/bin/api-20260718.1",
		Arguments:        []string{"--serve"},
		WorkingDirectory: "/var/lib/yeet/services/api/data",
		User:             "app",
		Group:            "app",
		EnvFile:          "-/var/lib/yeet/services/api/env/env",
	}

	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles: %v", err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"ExecStart=/var/lib/yeet/services/api/bin/api-20260718.1 --serve\n",
		"WorkingDirectory=/var/lib/yeet/services/api/data\n",
		"User=app\n",
		"Group=app\n",
		"EnvironmentFile=-/var/lib/yeet/services/api/env/env\n",
		"Environment=HOME=/var/lib/yeet/services/api/data USER=app LOGNAME=app SHELL=/bin/sh\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "EnvironmentFile=") > strings.Index(got, "Environment=HOME=") {
		t.Fatalf("identity environment must follow EnvironmentFile so it cannot be overridden:\n%s", got)
	}
}

func TestSystemdUnitUsesIndependentHomeDirectory(t *testing.T) {
	unit := SystemdUnit{
		Name:             "api",
		Executable:       "/srv/api/bin/api",
		WorkingDirectory: "/",
		HomeDirectory:    "/srv/api/data",
		User:             "app",
		Group:            "app",
	}

	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles: %v", err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"WorkingDirectory=/\n",
		"Environment=HOME=/srv/api/data USER=app LOGNAME=app SHELL=/bin/sh\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing independent HOME environment line %q:\n%s", want, got)
		}
	}
}

func TestTask6BSystemdUnitRendersBubblewrapServiceWithoutExecutingTimer(t *testing.T) {
	root := t.TempDir()
	payload := filepath.Join(root, "api-1")
	data := filepath.Join(root, "data")
	planArgs := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--disable-userns",
		"--uid", "70000", "--gid", "70001", "--hostname", "api", "--new-session", "--die-with-parent",
		"--ro-bind", payload, payload,
		"--bind", data, data,
		"--chdir", data,
		"--",
	}
	wantArgv := append([]string{"/usr/bin/bwrap"}, planArgs...)
	wantArgv = append(wantArgv, payload, "--serve", "argument with space")
	unit := SystemdUnit{
		Name:                "api",
		Executable:          "/usr/bin/bwrap",
		ConditionExecutable: payload,
		Arguments:           append(planArgs, payload, "--serve", "argument with space"),
		WorkingDirectory:    "/",
		HomeDirectory:       data,
		User:                "70000",
		Group:               "70001",
		NetNS:               "yeet-api-ns",
		Requires:            "yeet-api-ns.service",
		After:               "yeet-api-ns.service",
		Timer:               &TimerConfig{OnCalendar: "hourly", Persistent: true},
	}
	paths, err := unit.WriteOutUnitFiles(root)
	if err != nil {
		t.Fatalf("WriteOutUnitFiles: %v", err)
	}
	serviceRaw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatal(err)
	}
	service := string(serviceRaw)
	task6BAssertSystemdSandboxArgv(t, service, wantArgv)
	task6BAssertSystemdSandboxDirectives(t, service, payload, data)
	task6BAssertSystemdTimer(t, paths[db.ArtifactSystemdTimerFile], payload)
}

func task6BAssertSystemdSandboxArgv(t *testing.T, service string, wantArgv []string) {
	t.Helper()
	value := task6BSystemdExecStartValue(t, service)
	argv, err := ParseSystemdExecStart(value)
	if err != nil {
		t.Fatalf("ParseSystemdExecStart: %v", err)
	}
	if diff := cmp.Diff(wantArgv, argv); diff != "" {
		t.Fatalf("Bubblewrap argv mismatch (-want +got):\n%s", diff)
	}
	separatorCount := 0
	for _, value := range argv {
		if value == "--" {
			separatorCount++
		}
	}
	if separatorCount != 1 {
		t.Fatalf("policy separators = %d, want 1: %#v", separatorCount, argv)
	}
}

func task6BAssertSystemdSandboxDirectives(t *testing.T, service, payload, data string) {
	t.Helper()
	for _, want := range []string{
		"ConditionFileIsExecutable=" + payload + "\n",
		"WorkingDirectory=/\n",
		"Environment=HOME=" + data + " USER=70000 LOGNAME=70000 SHELL=/bin/sh\n",
		"User=70000\n",
		"Group=70001\n",
		"NetworkNamespacePath=/var/run/netns/yeet-api-ns\n",
		"Requires=yeet-api-ns.service\n",
		"After=yeet-api-ns.service\n",
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("service unit missing %q:\n%s", want, service)
		}
	}
	if strings.Contains(service, "BindReadOnlyPaths=") || strings.Contains(service, "PrivateMounts=yes") {
		t.Fatalf("sandboxed service retained systemd resolver mount:\n%s", service)
	}
}

func task6BAssertSystemdTimer(t *testing.T, path, payload string) {
	t.Helper()
	timerRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	timer := string(timerRaw)
	for _, want := range []string{"OnCalendar=hourly\n", "Persistent=true\n", "WantedBy=timers.target\n"} {
		if !strings.Contains(timer, want) {
			t.Fatalf("timer missing %q:\n%s", want, timer)
		}
	}
	for _, forbidden := range []string{"ExecStart=", "/usr/bin/bwrap", payload, "NetworkNamespacePath=", "Requires=yeet-api-ns.service"} {
		if strings.Contains(timer, forbidden) {
			t.Fatalf("timer unit contains workload execution directive %q:\n%s", forbidden, timer)
		}
	}
}

func task6BSystemdExecStartValue(t *testing.T, unit string) string {
	t.Helper()
	var values []string
	inService := false
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inService = line == "[Service]"
			continue
		}
		if inService && strings.HasPrefix(line, "ExecStart=") {
			values = append(values, strings.TrimPrefix(line, "ExecStart="))
		}
	}
	if len(values) != 1 {
		t.Fatalf("Service ExecStart values = %#v, want one\n%s", values, unit)
	}
	return values[0]
}

func TestSystemdUnitPropagatesExecStartRenderError(t *testing.T) {
	unit := SystemdUnit{
		Name:       "api",
		Executable: "/srv/api/bin/api",
		Arguments:  []string{"bad\nargument"},
	}

	root := t.TempDir()
	path := filepath.Join(root, fileutil.ApplyVersion("api.service"))
	want := []byte("existing unit sentinel\n")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("seed unit file: %v", err)
	}

	_, err := unit.WriteOutUnitFiles(root)
	if err == nil || !strings.Contains(err.Error(), "render systemd ExecStart: argument 1 contains line break") {
		t.Fatalf("WriteOutUnitFiles error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unit file after render error: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("unit file mutated after render error (-want +got):\n%s", diff)
	}
}

func TestSystemdUnitDefaultsAfterToRequires(t *testing.T) {
	unit := SystemdUnit{
		Name:       "demo",
		Executable: "/usr/local/bin/demo",
		Requires:   "network-online.target",
	}

	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "After=network-online.target\n") {
		t.Fatalf("unit did not preserve After=Requires default:\n%s", got)
	}
}

func TestSystemdServiceInstallPlanOrdersArtifactsAndPrimaryTimer(t *testing.T) {
	tmp := t.TempDir()
	cfg := db.Service{
		Name:       "demo",
		Generation: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:      testArtifact("svc"),
			db.ArtifactSystemdTimerFile: testArtifact("timer"),
			db.ArtifactNetNSService:     testArtifact("netns"),
			db.ArtifactNetNSEnv:         testArtifact("netns-env"),
			db.ArtifactTSService:        testArtifact("ts"),
			db.ArtifactTSEnv:            testArtifact("ts-env"),
			db.ArtifactTSBinary:         testArtifact("ts-bin"),
			db.ArtifactTSConfig:         testArtifact("ts-config"),
			db.ArtifactBinary:           testArtifact("bin"),
			db.ArtifactEnvFile:          testArtifact("env"),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: tmp}

	plan := svc.installPlan()

	gotArtifacts := make([]db.ArtifactName, 0, len(plan))
	for _, step := range plan {
		gotArtifacts = append(gotArtifacts, step.artifact)
	}
	wantArtifacts := []db.ArtifactName{
		db.ArtifactSystemdUnit,
		db.ArtifactSystemdTimerFile,
		db.ArtifactNetNSService,
		db.ArtifactNetNSEnv,
		db.ArtifactTypeScriptFile,
		db.ArtifactPythonFile,
		db.ArtifactEnvFile,
		db.ArtifactTSService,
		db.ArtifactTSEnv,
		db.ArtifactTSBinary,
		db.ArtifactTSConfig,
	}
	if diff := cmp.Diff(wantArtifacts, gotArtifacts); diff != "" {
		t.Fatalf("install artifact order mismatch (-want +got):\n%s", diff)
	}

	gotUnits := enabledUnitsForInstallPlan(plan, cfg.Artifacts, cfg.Generation)
	wantUnits := []string{"demo.timer", "yeet-demo-ns.service", "yeet-demo-ts.service"}
	if diff := cmp.Diff(wantUnits, gotUnits); diff != "" {
		t.Fatalf("enabled units mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdServiceNativeManagedArtifactPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	svc := &SystemdService{cfg: (&db.Service{Name: "api"}).View(), runDir: filepath.Join(root, "run")}
	installers := svc.artifactInstaller()

	want := map[db.ArtifactName]string{
		db.ArtifactNetNSEnv: filepath.Join(root, "env", "netns.env"),
		db.ArtifactEnvFile:  filepath.Join(root, "env", "env"),
		db.ArtifactTSEnv:    filepath.Join(root, "env", "tailscaled.env"),
		db.ArtifactTSBinary: filepath.Join(root, "bin", "tailscaled"),
		db.ArtifactTSConfig: filepath.Join(root, "env", "tailscaled.json"),
	}
	for artifact, path := range want {
		if got := installers[artifact].dstPath; got != path {
			t.Fatalf("%s destination = %q, want %q", artifact, got, path)
		}
	}
	if _, ok := installers[db.ArtifactBinary]; ok {
		t.Fatal("native binary must execute its immutable generation, not be copied to run/")
	}
	for _, step := range svc.installPlan() {
		if step.artifact == db.ArtifactBinary {
			t.Fatal("native binary unexpectedly present in install plan")
		}
	}
}

func TestCatchSystemdServiceKeepsStableRunnerArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "catch")
	svc := &SystemdService{cfg: (&db.Service{Name: "catch"}).View(), runDir: filepath.Join(root, "run")}
	installers := svc.artifactInstaller()
	installer, ok := installers[db.ArtifactBinary]
	if !ok || installer.dstPath != filepath.Join(root, "run", "catch") {
		t.Fatalf("Catch stable runner installer = %#v ok=%v", installer, ok)
	}
	if got := installers[db.ArtifactEnvFile].dstPath; got != filepath.Join(root, "env", "env") {
		t.Fatalf("Catch env destination = %q, want managed env path", got)
	}
	found := false
	for _, step := range svc.installPlan() {
		found = found || step.artifact == db.ArtifactBinary
	}
	if !found {
		t.Fatal("Catch stable runner missing from install plan")
	}
}

func TestYeetNSSystemdServiceKeepsFlatRuntimeArtifacts(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "runtime")
	systemdDir := filepath.Join(tmp, "systemd")
	for _, dir := range []string{runDir, systemdDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	unitSrc := writeTempFile(t, tmp, "yeet-ns.source.service", "unit\n")
	envSrc := writeTempFile(t, tmp, "yeet-ns.source.env", "RANGE=192.168.100.0/24\n")
	binSrc := writeTempFile(t, tmp, "yeet-ns.source", "binary\n")
	cfg := (&db.Service{
		Name:       "yeet-ns",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: artifactAt(1, unitSrc),
			db.ArtifactEnvFile:     artifactAt(1, envSrc),
			db.ArtifactBinary:      artifactAt(1, binSrc),
		},
	}).View()
	service, err := NewHostSystemdService(nil, cfg, runDir)
	if err != nil {
		t.Fatal(err)
	}
	service.systemdDir = systemdDir

	if _, err := service.StageInstallForReload(); err != nil {
		t.Fatalf("StageInstallForReload: %v", err)
	}
	assertFileContent(t, filepath.Join(runDir, "yeet-ns"), "binary\n")
	assertFileContent(t, filepath.Join(runDir, "env"), "RANGE=192.168.100.0/24\n")
}

func TestUserNamedYeetNSCannotSelectFlatRuntimeArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "yeet-ns")
	service, err := NewSystemdService(nil, (&db.Service{Name: "yeet-ns"}).View(), filepath.Join(root, "run"))
	if err != nil {
		t.Fatal(err)
	}
	installers := service.artifactInstaller()
	if got := installers[db.ArtifactEnvFile].dstPath; got != filepath.Join(root, "env", "env") {
		t.Fatalf("ordinary service env destination = %q, want managed layout", got)
	}
	if _, ok := installers[db.ArtifactBinary]; ok {
		t.Fatal("ordinary service named yeet-ns selected the host-daemon binary compatibility path")
	}
}

func TestSystemdServicePrimaryUnit(t *testing.T) {
	serviceCfg := db.Service{Name: "demo"}
	service := &SystemdService{cfg: serviceCfg.View()}
	if got := service.PrimaryUnit(); got != "demo.service" {
		t.Fatalf("service PrimaryUnit = %q, want demo.service", got)
	}

	timerCfg := db.Service{
		Name: "demo", Generation: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdTimerFile: testArtifact("timer"),
		},
	}
	timer := &SystemdService{cfg: timerCfg.View()}
	if got := timer.PrimaryUnit(); got != "demo.timer" {
		t.Fatalf("timer PrimaryUnit = %q, want demo.timer", got)
	}

	stagedTimerCfg := db.Service{
		Name: "demo", Generation: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdTimerFile: artifactAt(4, "/tmp/timer-4"),
		},
	}
	stagedTimer := &SystemdService{cfg: stagedTimerCfg.View()}
	if got := stagedTimer.PrimaryUnit(); got != "demo.service" {
		t.Fatalf("service with only a staged timer PrimaryUnit = %q, want demo.service", got)
	}
}

func TestSystemdServiceAuxiliaryCleanupPlansAreOptionalAndOrdered(t *testing.T) {
	cfg := db.Service{
		Name:       "demo",
		Generation: 7,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:  testArtifact("svc"),
			db.ArtifactNetNSService: testArtifact("netns"),
			db.ArtifactTSService:    testArtifact("ts"),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: t.TempDir()}

	wantStop := []string{"demo.service", "yeet-demo-ts.service", "yeet-demo-ns.service"}
	if diff := cmp.Diff(wantStop, svc.stopUnits()); diff != "" {
		t.Fatalf("stop units mismatch (-want +got):\n%s", diff)
	}

	wantUninstall := []string{"demo.service", "yeet-demo-ns.service", "yeet-demo-ts.service"}
	if diff := cmp.Diff(wantUninstall, svc.uninstallDisableUnits()); diff != "" {
		t.Fatalf("uninstall disable units mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdUnitRendersTimerAndServiceOptions(t *testing.T) {
	unit := SystemdUnit{
		Name:             "demo",
		User:             "nobody",
		Executable:       "/opt/demo/bin/demo",
		Arguments:        []string{"--config", "/etc/demo/config.json"},
		OneShot:          true,
		StopCmd:          "/opt/demo/bin/demo-stop",
		Timer:            &TimerConfig{OnCalendar: "hourly", Persistent: true},
		EnvFile:          "/run/demo/env",
		WorkingDirectory: "/srv/demo",
		NetNS:            "demo-ns",
		Requires:         "yeet-demo-ns.service",
		After:            "yeet-demo-ns.service",
		ResolvConf:       "/run/demo/resolv.conf",
	}

	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	serviceRaw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile service returned error: %v", err)
	}
	timerRaw, err := os.ReadFile(paths[db.ArtifactSystemdTimerFile])
	if err != nil {
		t.Fatalf("ReadFile timer returned error: %v", err)
	}

	service := string(serviceRaw)
	for _, want := range []string{
		"ExecStart=/opt/demo/bin/demo --config /etc/demo/config.json\n",
		"Type=oneshot\n",
		"WorkingDirectory=/srv/demo\n",
		"Restart=on-failure\n",
		"User=nobody\n",
		"EnvironmentFile=/run/demo/env\n",
		"NetworkNamespacePath=/var/run/netns/demo-ns\n",
		"Requires=yeet-demo-ns.service\n",
		"After=yeet-demo-ns.service\n",
		"RemainAfterExit=yes\n",
		"ExecStop=/opt/demo/bin/demo-stop\n",
		"BindReadOnlyPaths=/run/demo/resolv.conf:/etc/resolv.conf\n",
		"PrivateMounts=yes\n",
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("service unit missing %q:\n%s", want, service)
		}
	}
	timer := string(timerRaw)
	for _, want := range []string{
		"OnCalendar=hourly\n",
		"Persistent=true\n",
		"WantedBy=timers.target\n",
	} {
		if !strings.Contains(timer, want) {
			t.Fatalf("timer unit missing %q:\n%s", want, timer)
		}
	}
	for _, forbidden := range []string{
		"NetworkNamespacePath=", "BindReadOnlyPaths=", "Requires=yeet-demo-ns.service", "After=yeet-demo-ns.service",
	} {
		if strings.Contains(timer, forbidden) {
			t.Fatalf("timer unit contains service-only networking directive %q:\n%s", forbidden, timer)
		}
	}
	for _, forbidden := range []string{"NoNewPrivileges=", "CapabilityBoundingSet=", "RestrictNamespaces=", "RestrictAddressFamilies="} {
		if strings.Contains(service, forbidden) {
			t.Fatalf("service unit contains unrequested privilege policy %q:\n%s", forbidden, service)
		}
	}
}

func TestSystemdServiceInstallCopiesArtifactsRemovesStaleOptionalAndEnablesTimerPrimary(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "run")
	for _, dir := range []string{systemdDir, runDir, filepath.Join(tmp, "bin"), filepath.Join(tmp, "env")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	systemctlLog := installFakeSystemctl(t, tmp)

	serviceSrc := writeTempFile(t, tmp, "demo.service", "service unit\n")
	timerSrc := writeTempFile(t, tmp, "demo.timer", "timer unit\n")
	netnsSrc := writeTempFile(t, tmp, "netns.service", "netns unit\n")
	binSrc := writeTempFile(t, tmp, "demo-bin", "binary\n")
	envSrc := writeTempFile(t, tmp, "source.env", "ENV=1\n")
	netnsEnvSrc := writeTempFile(t, tmp, "source-netns.env", "NETNS=1\n")
	tsEnvSrc := writeTempFile(t, tmp, "source-tailscaled.env", "TS=1\n")
	tsBinarySrc := writeTempFile(t, tmp, "source-tailscaled", "tailscaled\n")
	tsConfigSrc := writeTempFile(t, tmp, "source-tailscaled.json", "{}\n")
	stalePython := filepath.Join(runDir, "main.py")
	if err := os.WriteFile(stalePython, []byte("stale\n"), 0644); err != nil {
		t.Fatalf("failed to write stale python artifact: %v", err)
	}
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:      artifactAt(1, serviceSrc),
			db.ArtifactSystemdTimerFile: artifactAt(1, timerSrc),
			db.ArtifactNetNSService:     artifactAt(1, netnsSrc),
			db.ArtifactBinary:           artifactAt(1, binSrc),
			db.ArtifactEnvFile:          artifactAt(1, envSrc),
			db.ArtifactNetNSEnv:         artifactAt(1, netnsEnvSrc),
			db.ArtifactTSEnv:            artifactAt(1, tsEnvSrc),
			db.ArtifactTSBinary:         artifactAt(1, tsBinarySrc),
			db.ArtifactTSConfig:         artifactAt(1, tsConfigSrc),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}

	if err := svc.Install(); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	assertFileContent(t, filepath.Join(systemdDir, "demo.service"), "service unit\n")
	assertFileContent(t, filepath.Join(systemdDir, "demo.timer"), "timer unit\n")
	assertFileContent(t, filepath.Join(systemdDir, "yeet-demo-ns.service"), "netns unit\n")
	if _, err := os.Stat(filepath.Join(runDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("immutable binary was copied to run/: %v", err)
	}
	assertFileContent(t, filepath.Join(tmp, "env", "env"), "ENV=1\n")
	assertFileContent(t, filepath.Join(tmp, "env", "netns.env"), "NETNS=1\n")
	assertFileContent(t, filepath.Join(tmp, "env", "tailscaled.env"), "TS=1\n")
	assertFileContent(t, filepath.Join(tmp, "bin", "tailscaled"), "tailscaled\n")
	assertFileContent(t, filepath.Join(tmp, "env", "tailscaled.json"), "{}\n")
	for _, name := range []string{"env", "netns.env", "tailscaled.env", "tailscaled", "tailscaled.json"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); !os.IsNotExist(err) {
			t.Fatalf("managed artifact %s remained in run/: %v", name, err)
		}
	}
	if _, err := os.Stat(stalePython); !os.IsNotExist(err) {
		t.Fatalf("stale optional artifact stat error = %v, want not exist", err)
	}

	gotLog := readSystemctlLog(t, systemctlLog)
	wantLog := []string{
		"daemon-reload",
		"enable demo.timer",
		"enable yeet-demo-ns.service",
	}
	if diff := cmp.Diff(wantLog, gotLog); diff != "" {
		t.Fatalf("systemctl log mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdServiceInstallWithActivationCheckStagesBeforeSystemctl(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "run")
	for _, dir := range []string{systemdDir, runDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	systemctlLog := installFakeSystemctl(t, tmp)
	serviceSrc := writeTempFile(t, tmp, "demo.service", "service unit\n")
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: artifactAt(1, serviceSrc),
		},
	}
	service := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}
	checkErr := errors.New("resolver proof changed")

	err := service.InstallWithActivationCheck(func() error {
		assertFileContent(t, filepath.Join(systemdDir, "demo.service"), "service unit\n")
		if raw, readErr := os.ReadFile(systemctlLog); readErr == nil {
			t.Fatalf("systemctl calls before activation check completed = %q", raw)
		} else if !os.IsNotExist(readErr) {
			t.Fatalf("read systemctl log before activation check: %v", readErr)
		}
		return checkErr
	})
	if !errors.Is(err, checkErr) {
		t.Fatalf("InstallWithActivationCheck error = %v, want %v", err, checkErr)
	}
	if raw, readErr := os.ReadFile(systemctlLog); readErr == nil {
		t.Fatalf("systemctl calls after rejected activation check = %q, want none", raw)
	} else if !os.IsNotExist(readErr) {
		t.Fatalf("read systemctl log after rejected activation check: %v", readErr)
	}
}

func TestSystemdServiceInstallWithActivationCheckRestoresEveryDestinationOnRejection(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "service", "run")
	for _, dir := range []string{systemdDir, runDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	systemctlLog := installFakeSystemctl(t, tmp)
	cfg := db.Service{Name: "catch", Generation: 1, Artifacts: db.ArtifactStore{}}
	service := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}
	plan := service.installPlan()

	type priorState struct {
		present bool
		content string
		mode    os.FileMode
	}
	prior := make(map[string]priorState, len(plan))
	for index, step := range plan {
		if err := os.MkdirAll(filepath.Dir(step.dstPath), 0o755); err != nil {
			t.Fatal(err)
		}
		switch index % 3 {
		case 0: // Existing destination replaced by a generated artifact.
			content := "prior-" + string(step.artifact)
			mode := os.FileMode(0o600 + index%8)
			if err := os.WriteFile(step.dstPath, []byte(content), mode); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(step.dstPath)
			if err != nil {
				t.Fatal(err)
			}
			source := writeTempFile(t, tmp, "source-"+string(step.artifact), "replacement\n")
			cfg.Artifacts[step.artifact] = artifactAt(1, source)
			prior[step.dstPath] = priorState{present: true, content: content, mode: info.Mode().Perm()}
		case 1: // Existing optional destination removed because its artifact is absent.
			content := "optional-" + string(step.artifact)
			mode := os.FileMode(0o640 + index%8)
			if err := os.WriteFile(step.dstPath, []byte(content), mode); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(step.dstPath)
			if err != nil {
				t.Fatal(err)
			}
			prior[step.dstPath] = priorState{present: true, content: content, mode: info.Mode().Perm()}
		case 2: // Newly introduced destination.
			source := writeTempFile(t, tmp, "source-"+string(step.artifact), "introduced\n")
			cfg.Artifacts[step.artifact] = artifactAt(1, source)
			prior[step.dstPath] = priorState{}
		}
	}
	service.cfg = cfg.View()
	checkErr := errors.New("resolver proof changed")

	err := service.InstallWithActivationCheck(func() error { return checkErr })
	if !errors.Is(err, checkErr) {
		t.Fatalf("InstallWithActivationCheck error = %v, want %v", err, checkErr)
	}
	for path, want := range prior {
		info, statErr := os.Stat(path)
		if !want.present {
			if !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("introduced destination %s survived rejection: %v", path, statErr)
			}
			continue
		}
		if statErr != nil {
			t.Errorf("restored destination %s: %v", path, statErr)
			continue
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Errorf("read restored destination %s: %v", path, readErr)
			continue
		}
		if string(raw) != want.content || info.Mode().Perm() != want.mode.Perm() {
			t.Errorf("restored destination %s = (%q, %04o), want (%q, %04o)",
				path, raw, info.Mode().Perm(), want.content, want.mode.Perm())
		}
	}
	if raw, readErr := os.ReadFile(systemctlLog); readErr == nil {
		t.Fatalf("systemctl calls after rejected activation check = %q, want none", raw)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func TestSystemdServiceInstallWithActivationCheckJoinsRestoreFailure(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "service", "run")
	for _, dir := range []string{systemdDir, runDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = installFakeSystemctl(t, tmp)
	source := writeTempFile(t, tmp, "demo.service", "replacement\n")
	cfg := db.Service{Name: "demo", Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit: artifactAt(1, source),
	}}
	service := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}
	destination := service.servicePath()
	if err := os.WriteFile(destination, []byte("prior\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	checkErr := errors.New("resolver proof changed")

	err := service.InstallWithActivationCheck(func() error {
		if removeErr := os.Remove(destination); removeErr != nil {
			return removeErr
		}
		if removeErr := os.Remove(systemdDir); removeErr != nil {
			return removeErr
		}
		if writeErr := os.WriteFile(systemdDir, []byte("blocks restore"), 0o600); writeErr != nil {
			return writeErr
		}
		return checkErr
	})
	if !errors.Is(err, checkErr) {
		t.Fatalf("InstallWithActivationCheck error = %v, want joined %v", err, checkErr)
	}
	if err == checkErr || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("InstallWithActivationCheck error = %v, want surfaced restore failure", err)
	}
}

func TestSystemdServiceStageInstallForReloadCopiesArtifactsWithoutSystemctl(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "run")
	for _, dir := range []string{systemdDir, runDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	systemctlLog := installFakeSystemctl(t, tmp)
	serviceSrc := writeTempFile(t, tmp, "demo.service", "service unit\n")
	netnsSrc := writeTempFile(t, tmp, "netns.service", "netns unit\n")
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:  artifactAt(1, serviceSrc),
			db.ArtifactNetNSService: artifactAt(1, netnsSrc),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}

	units, err := svc.StageInstallForReload()
	if err != nil {
		t.Fatalf("StageInstallForReload returned error: %v", err)
	}

	assertFileContent(t, filepath.Join(systemdDir, "demo.service"), "service unit\n")
	assertFileContent(t, filepath.Join(systemdDir, "yeet-demo-ns.service"), "netns unit\n")
	wantUnits := []string{"demo.service", "yeet-demo-ns.service"}
	if diff := cmp.Diff(wantUnits, units); diff != "" {
		t.Fatalf("units mismatch (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(systemctlLog); !os.IsNotExist(err) {
		t.Fatalf("systemctl log stat error = %v, want no systemctl calls", err)
	}
}

func TestSystemdServiceInstallTargetStatesExcludingCapturesPresentAndAbsentArtifacts(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "run")
	for _, dir := range []string{systemdDir, runDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	unitSrc := writeTempFile(t, tmp, "demo.service", "service unit\n")
	netnsSrc := writeTempFile(t, tmp, "netns.service", "netns unit\n")
	oldFlistxattr := systemdInstallTargetFlistxattr
	systemdInstallTargetFlistxattr = func(int, []byte) (int, error) { return 0, nil }
	t.Cleanup(func() { systemdInstallTargetFlistxattr = oldFlistxattr })
	cfg := db.Service{
		Name: "demo", Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:  artifactAt(1, unitSrc),
			db.ArtifactNetNSService: artifactAt(1, netnsSrc),
		},
	}
	service := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}
	excluded := filepath.Join(systemdDir, "demo.service")
	states, err := service.InstallTargetStatesExcluding(excluded)
	if err != nil {
		t.Fatalf("InstallTargetStatesExcluding: %v", err)
	}
	if len(states) == 0 {
		t.Fatal("transaction intent returned no auxiliary paths")
	}
	var present, absent bool
	for _, state := range states {
		if state.Path == excluded {
			t.Fatalf("excluded primary unit remained in states: %#v", states)
		}
		if state.Present {
			present = true
			if state.Size == 0 || state.SHA256 == "" || state.Nlink != 1 {
				t.Fatalf("present state lacks exact provenance: %#v", state)
			}
		} else {
			absent = true
		}
	}
	if !present || !absent {
		t.Fatalf("states = %#v, want both present and absent auxiliary artifacts", states)
	}
}

func TestSystemdServiceStageInstallEnforcesPersistedIdentityOnStaleUnitArtifact(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	root := filepath.Join(tmp, "service")
	runDir := filepath.Join(root, "run")
	binDir := filepath.Join(root, "bin")
	envDir := filepath.Join(root, "env")
	for _, dir := range []string{systemdDir, runDir, binDir, envDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binarySrc := writeTempFile(t, binDir, "demo-1", "binary\n")
	envSrc := writeTempFile(t, envDir, "env-1", "TOKEN=value\n")
	legacyBinary := filepath.Join(runDir, "demo")
	legacyEnv := filepath.Join(runDir, "env")
	unitSrc := writeTempFile(t, binDir, "demo.source.service",
		"[Unit]\nConditionFileIsExecutable="+legacyBinary+"\nDescription=demo\n[Service]\nExecStart="+legacyBinary+" --serve\nEnvironmentFile=-"+legacyEnv+"\nUser=root\nGroup=root\n[Install]\nWantedBy=multi-user.target\n")
	identity := &db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1010}
	cfg := db.Service{
		Name: "demo", Generation: 1, Identity: identity,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: artifactAt(1, unitSrc),
			db.ArtifactBinary:      artifactAt(1, binarySrc),
			db.ArtifactEnvFile:     artifactAt(1, envSrc),
		},
	}
	service := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}
	oldChown := systemdInstallArtifactChown
	t.Cleanup(func() { systemdInstallArtifactChown = oldChown })
	var chowns []string
	systemdInstallArtifactChown = func(file *os.File, uid, gid int) error {
		chowns = append(chowns, fmt.Sprintf("%s=%d:%d", filepath.Base(file.Name()), uid, gid))
		return nil
	}
	rendered, err := service.RenderedPrimaryUnit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageInstallForReload(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(systemdDir, "demo.service"))
	if err != nil {
		t.Fatal(err)
	}
	if rendered != string(raw) {
		t.Fatalf("rendered primary unit differs from installed unit:\nrendered:\n%s\ninstalled:\n%s", rendered, raw)
	}
	for _, unwanted := range []string{legacyBinary, legacyEnv, "User=root", "Group=root"} {
		if strings.Contains(string(raw), unwanted) {
			t.Fatalf("installed unit retained legacy value %q:\n%s", unwanted, raw)
		}
	}
	for _, want := range []string{
		"ConditionFileIsExecutable=" + binarySrc + "\n",
		"ExecStart=" + binarySrc + " --serve\n",
		"EnvironmentFile=-" + filepath.Join(envDir, "env") + "\n",
		"User=app\n",
		"Group=workers\n",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("installed unit missing %q:\n%s", want, raw)
		}
	}
	if got, err := os.ReadFile(filepath.Join(envDir, "env")); err != nil || string(got) != "TOKEN=value\n" {
		t.Fatalf("managed environment = %q, %v", got, err)
	}
	if diff := cmp.Diff([]string{fmt.Sprintf("env=%d:1010", os.Geteuid())}, chowns); diff != "" {
		t.Fatalf("managed artifact chowns mismatch (-want +got):\n%s", diff)
	}
	if info, err := os.Stat(filepath.Join(envDir, "env")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("managed environment mode = %v, %v; want 0640", info, err)
	}
}

func TestSystemdServiceLifecycleUsesConfiguredSystemdDir(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "run")
	systemctlLog := installFakeSystemctl(t, tmp)
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdTimerFile: artifactAt(1, filepath.Join(tmp, "demo.timer.artifact")),
			db.ArtifactNetNSService:     artifactAt(1, filepath.Join(tmp, "netns.artifact")),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}

	status, err := svc.Status()
	if err != nil {
		t.Fatalf("Status returned error for missing timer: %v", err)
	}
	if status != StatusUnknown {
		t.Fatalf("Status for missing timer = %v, want %v", status, StatusUnknown)
	}

	writeTempSystemdFile(t, systemdDir, "demo.service")
	writeTempSystemdFile(t, systemdDir, "demo.timer")
	writeTempSystemdFile(t, systemdDir, "yeet-demo-ns.service")
	status, err = svc.Status()
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusRunning {
		t.Fatalf("Status = %v, want %v", status, StatusRunning)
	}

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if err := svc.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if err := svc.Disable(); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	if err := svc.Uninstall(); err != nil {
		t.Fatalf("Uninstall returned error: %v", err)
	}

	gotLog := readSystemctlLog(t, systemctlLog)
	for _, want := range []string{
		"is-active demo.timer",
		"stop demo.timer",
		"stop demo.service",
		"stop yeet-demo-ns.service",
		"start yeet-demo-ns.service",
		"start demo.timer",
		"restart demo.timer",
		"disable demo.timer",
		"disable --now demo.timer",
		"disable --now yeet-demo-ns.service",
		"daemon-reload",
	} {
		if !containsLine(gotLog, want) {
			t.Fatalf("systemctl log missing %q:\n%s", want, strings.Join(gotLog, "\n"))
		}
	}
	for _, path := range []string{
		filepath.Join(systemdDir, "demo.service"),
		filepath.Join(systemdDir, "demo.timer"),
		filepath.Join(systemdDir, "yeet-demo-ns.service"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unit file %s stat error = %v, want not exist", path, err)
		}
	}
}

func TestSystemdServiceUninstallReturnsAuxiliaryDisableError(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	installFakeSystemctl(t, tmp)
	t.Setenv("YEET_SYSTEMCTL_FAIL", "disable --now yeet-demo-ns.service")
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: artifactAt(1, filepath.Join(tmp, "netns.artifact")),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: filepath.Join(tmp, "run"), systemdDir: systemdDir}
	writeTempSystemdFile(t, systemdDir, "demo.service")
	writeTempSystemdFile(t, systemdDir, "yeet-demo-ns.service")

	err := svc.Uninstall()
	if err == nil || !strings.Contains(err.Error(), "disable --now yeet-demo-ns.service") {
		t.Fatalf("Uninstall error = %v, want auxiliary disable failure", err)
	}
	if _, statErr := os.Stat(svc.netnsServicePath()); statErr != nil {
		t.Fatalf("failed auxiliary unit was removed: %v", statErr)
	}
}

func TestSystemdServiceUninstallRetriesRemainingScheduledPrimaryUnit(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	systemctlLog := installFakeSystemctl(t, tmp)
	t.Setenv("YEET_SYSTEMCTL_FAIL", "disable --now demo.service")
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdTimerFile: artifactAt(1, filepath.Join(tmp, "demo.timer.artifact")),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: filepath.Join(tmp, "run"), systemdDir: systemdDir}
	writeTempSystemdFile(t, systemdDir, "demo.service")
	writeTempSystemdFile(t, systemdDir, "demo.timer")

	err := svc.Uninstall()
	if err == nil || !strings.Contains(err.Error(), "disable --now demo.service") {
		t.Fatalf("first Uninstall error = %v, want service disable failure", err)
	}
	if _, statErr := os.Stat(svc.timerPath()); !os.IsNotExist(statErr) {
		t.Fatalf("successfully removed timer stat error = %v, want not exist", statErr)
	}
	if _, statErr := os.Stat(svc.servicePath()); statErr != nil {
		t.Fatalf("failed service unit was removed: %v", statErr)
	}

	t.Setenv("YEET_SYSTEMCTL_FAIL", "")
	if err := svc.Uninstall(); err != nil {
		t.Fatalf("retry Uninstall returned error: %v", err)
	}
	if _, statErr := os.Stat(svc.servicePath()); !os.IsNotExist(statErr) {
		t.Fatalf("service stat after retry = %v, want not exist", statErr)
	}
	wantLog := []string{
		"disable --now demo.timer",
		"disable --now demo.service",
		"disable --now demo.service",
		"daemon-reload",
	}
	if diff := cmp.Diff(wantLog, readSystemctlLog(t, systemctlLog)); diff != "" {
		t.Fatalf("systemctl log mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdServiceUninstallRemovesDanglingUnitSymlink(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	installFakeSystemctl(t, tmp)
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	servicePath := filepath.Join(systemdDir, "demo.service")
	if err := os.Symlink(filepath.Join(tmp, "missing.service"), servicePath); err != nil {
		t.Fatal(err)
	}
	svc := &SystemdService{
		cfg:        (&db.Service{Name: "demo", Generation: 1}).View(),
		runDir:     filepath.Join(tmp, "run"),
		systemdDir: systemdDir,
	}

	if err := svc.Uninstall(); err != nil {
		t.Fatalf("Uninstall returned error: %v", err)
	}
	if _, err := os.Lstat(servicePath); !os.IsNotExist(err) {
		t.Fatalf("dangling service symlink stat error = %v, want not exist", err)
	}
}

func TestSystemdServiceUninstallReturnsUnitInspectionError(t *testing.T) {
	tmp := t.TempDir()
	installFakeSystemctl(t, tmp)
	systemdDir := filepath.Join(tmp, "not-a-directory")
	if err := os.WriteFile(systemdDir, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &SystemdService{
		cfg:        (&db.Service{Name: "demo", Generation: 1}).View(),
		runDir:     filepath.Join(tmp, "run"),
		systemdDir: systemdDir,
	}

	err := svc.Uninstall()
	if err == nil || !strings.Contains(err.Error(), "inspect systemd unit") {
		t.Fatalf("Uninstall error = %v, want unit inspection failure", err)
	}
}

func TestSystemdServiceEnableUsesPrimaryUnit(t *testing.T) {
	tmp := t.TempDir()
	systemctlLog := installFakeSystemctl(t, tmp)
	cfg := db.Service{Name: "demo", Generation: 1}
	svc := &SystemdService{cfg: cfg.View(), runDir: filepath.Join(tmp, "run"), systemdDir: filepath.Join(tmp, "systemd")}

	if err := svc.Enable(); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}

	gotLog := readSystemctlLog(t, systemctlLog)
	if diff := cmp.Diff([]string{"enable demo.service"}, gotLog); diff != "" {
		t.Fatalf("systemctl log mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdServiceStartAuxiliaryUnitsVerifiesTailscaleBeforeMonitoring(t *testing.T) {
	tmp := t.TempDir()
	systemctlLog := installFakeSystemctl(t, tmp)
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: artifactAt(1, filepath.Join(tmp, "netns.service")),
			db.ArtifactTSService:    artifactAt(1, filepath.Join(tmp, "tailscale.service")),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: filepath.Join(tmp, "run"), systemdDir: filepath.Join(tmp, "systemd")}
	if err := os.MkdirAll(svc.systemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := svc.artifactInstaller()[db.ArtifactTSBinary].dstPath
	unit := "[Service]\nExecStart=" + binaryPath + " --statedir=. --tun=ts0\n" +
		"BindReadOnlyPaths=/run/demo/resolv.conf:/etc/resolv.conf\n"
	if err := os.WriteFile(svc.tailscaledServicePath(), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("tailscaled"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldDeps := tailscaleLifecycleDepsFn
	var verified int
	tailscaleLifecycleDepsFn = func() tailscaleLifecycleDeps {
		return tailscaleLifecycleDeps{
			mainPID: func(string) (int, error) { return 101, nil },
			stat:    lifecycleMatchingStat(svc, 101),
			status:  lifecycleReadyStatus,
			verifyMount: func(ResolverMountProbe) error {
				verified++
				return nil
			},
			wait: func(context.Context, time.Duration) error { return nil },
		}
	}
	t.Cleanup(func() { tailscaleLifecycleDepsFn = oldDeps })

	oldMonitor := monitorTailscaleFn
	monitorStarted := make(chan struct{}, 1)
	monitorTailscaleFn = func(*SystemdService) error {
		if verified != 2 {
			t.Errorf("resolver verification samples = %d, want 2 before monitor starts", verified)
		}
		monitorStarted <- struct{}{}
		return nil
	}
	t.Cleanup(func() { monitorTailscaleFn = oldMonitor })

	if err := svc.StartAuxiliaryUnits(); err != nil {
		t.Fatalf("StartAuxiliaryUnits returned error: %v", err)
	}
	select {
	case <-monitorStarted:
	case <-time.After(time.Second):
		t.Fatal("monitor did not start after final Tailscale verification")
	}
	gotLog := readSystemctlLog(t, systemctlLog)
	if diff := cmp.Diff([]string{"start yeet-demo-ns.service", "start yeet-demo-ts.service"}, gotLog); diff != "" {
		t.Fatalf("systemctl log mismatch (-want +got):\n%s", diff)
	}
}

func TestTailscaleResolverBindSourceFromFile(t *testing.T) {
	tmp := t.TempDir()

	got, err := tailscaleResolverBindSourceFromFile(filepath.Join(tmp, "missing.service"))
	if err != nil {
		t.Fatalf("missing unit returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("missing unit source = %q, want empty", got)
	}

	unitPath := writeTempFile(t, tmp, "tailscale.service", "[Service]\nBindReadOnlyPaths=/run/demo/resolv.conf:/etc/resolv.conf\n")
	got, err = tailscaleResolverBindSourceFromFile(unitPath)
	if err != nil {
		t.Fatalf("read unit returned error: %v", err)
	}
	if got != "/run/demo/resolv.conf" {
		t.Fatalf("resolver bind source = %q, want /run/demo/resolv.conf", got)
	}

	_, err = tailscaleResolverBindSourceFromFile(tmp)
	if err == nil {
		t.Fatal("directory unit path returned nil error")
	}
}

func TestSystemdServiceRestartStartsInactiveService(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	systemctlLog := installFakeSystemctl(t, tmp)
	cfg := db.Service{Name: "stopped", Generation: 1}
	svc := &SystemdService{cfg: cfg.View(), runDir: filepath.Join(tmp, "run"), systemdDir: systemdDir}
	writeTempSystemdFile(t, systemdDir, "stopped.service")

	if err := svc.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}

	gotLog := readSystemctlLog(t, systemctlLog)
	wantLog := []string{
		"is-active stopped.service",
		"stop stopped.service",
		"start stopped.service",
	}
	if diff := cmp.Diff(wantLog, gotLog); diff != "" {
		t.Fatalf("systemctl log mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdServiceInstallWrapsDaemonReloadError(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	runDir := filepath.Join(tmp, "run")
	for _, dir := range []string{systemdDir, runDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	installFakeSystemctl(t, tmp)
	t.Setenv("YEET_SYSTEMCTL_FAIL", "daemon-reload")
	serviceSrc := writeTempFile(t, tmp, "demo.service", "service unit\n")
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: artifactAt(1, serviceSrc),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}

	err := svc.Install()
	if err == nil || !strings.Contains(err.Error(), "failed to reload systemd") {
		t.Fatalf("Install error = %v, want daemon-reload wrapper", err)
	}
}

func TestSystemdServiceStopLogsAuxiliaryStopErrors(t *testing.T) {
	tmp := t.TempDir()
	systemdDir := filepath.Join(tmp, "systemd")
	systemctlLog := installFakeSystemctl(t, tmp)
	t.Setenv("YEET_SYSTEMCTL_FAIL", "stop yeet-demo-ns.service")
	cfg := db.Service{
		Name:       "demo",
		Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: artifactAt(1, filepath.Join(tmp, "netns.artifact")),
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: filepath.Join(tmp, "run"), systemdDir: systemdDir}
	writeTempSystemdFile(t, systemdDir, "demo.service")

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	gotLog := readSystemctlLog(t, systemctlLog)
	for _, want := range []string{
		"stop demo.service",
		"stop yeet-demo-ns.service",
	} {
		if !containsLine(gotLog, want) {
			t.Fatalf("systemctl log missing %q:\n%s", want, strings.Join(gotLog, "\n"))
		}
	}
}

func testArtifact(name string) *db.Artifact {
	return &db.Artifact{
		Refs: map[db.ArtifactRef]string{
			db.Gen(3): "/tmp/" + name + "-3",
			db.Gen(7): "/tmp/" + name + "-7",
		},
	}
}

func artifactAt(gen int, path string) *db.Artifact {
	return &db.Artifact{
		Refs: map[db.ArtifactRef]string{
			db.Gen(gen): path,
		},
	}
}

func installFakeSystemctl(t *testing.T, tmp string) string {
	t.Helper()
	systemctlLog := filepath.Join(tmp, "systemctl.log")
	systemctlPath := filepath.Join(tmp, "systemctl")
	script := `#!/bin/sh
if [ -n "$YEET_SYSTEMCTL_LOG" ]; then
	printf '%s\n' "$*" >> "$YEET_SYSTEMCTL_LOG"
fi
if [ -n "$YEET_SYSTEMCTL_FAIL" ] && [ "$*" = "$YEET_SYSTEMCTL_FAIL" ]; then
	printf 'systemctl failed for %s\n' "$*"
	exit 1
fi
case "$*" in
	"is-active demo.service"|"is-active demo.timer")
		exit 0
		;;
	"is-active "*)
		exit 3
		;;
esac
exit 0
`
	if err := os.WriteFile(systemctlPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake systemctl: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YEET_SYSTEMCTL_LOG", systemctlLog)
	return systemctlLog
}

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}

func writeTempSystemdFile(t *testing.T, systemdDir, name string) {
	t.Helper()
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		t.Fatalf("failed to create systemd dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(systemdDir, name), []byte("unit\n"), 0644); err != nil {
		t.Fatalf("failed to write unit %s: %v", name, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

func readSystemctlLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read systemctl log: %v", err)
	}
	return splitNonEmptyLines(string(raw))
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func TestNewISONetworkUnitGatesServiceOnVerifiedBoundary(t *testing.T) {
	unit, err := NewISONetworkUnit("app", "/usr/local/bin/catch", "/var/lib/yeet")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		"Description=yeet ISO network for app\n",
		"Before=app.service\n",
		"After=network-online.target docker.service\n",
		"Wants=network-online.target\n",
		"Type=oneshot\n",
		"RemainAfterExit=yes\n",
		"ExecStart=/usr/local/bin/catch -data-dir /var/lib/yeet iso-network-ensure app\n",
		"ExecStop=/usr/local/bin/catch -data-dir /var/lib/yeet iso-network-clean app\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ISO network unit missing %q:\n%s", want, got)
		}
	}
}

func TestSystemdServiceRenderRewritesISONetworkGateToStableCatchRunner(t *testing.T) {
	service, err := NewSystemdService(
		nil,
		(&db.Service{Name: "app", Generation: 1}).View(),
		"/srv/app/run",
		WithTailscaleGuardRunner("/srv/catch/run/catch"),
	)
	if err != nil {
		t.Fatal(err)
	}
	const oldRunner = "/srv/catch/bin/catch-old"
	raw := "[Unit]\nDescription=yeet ISO network gate for app\n" +
		"ConditionFileIsExecutable=" + oldRunner + "\n\n" +
		"[Service]\nExecStart=" + oldRunner + " -data-dir /srv/yeet iso-network-ensure app\n" +
		"ExecStop=" + oldRunner + " -data-dir /srv/yeet iso-network-clean app\n"

	rendered, err := service.renderSystemdUnitContent(installStep{artifact: db.ArtifactNetNSService}, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ConditionFileIsExecutable=/srv/catch/run/catch",
		"ExecStart=/srv/catch/run/catch -data-dir /srv/yeet iso-network-ensure app",
		"ExecStop=/srv/catch/run/catch -data-dir /srv/yeet iso-network-clean app",
	} {
		if !strings.Contains(string(rendered), want) {
			t.Fatalf("rendered ISO gate missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(string(rendered), oldRunner) {
		t.Fatalf("rendered ISO gate retained old Catch runner:\n%s", rendered)
	}
}

func TestNewISONetworkUnitRejectsAmbiguousArguments(t *testing.T) {
	for _, test := range []struct{ service, catchBin, dataDir string }{
		{service: "", catchBin: "/usr/bin/catch", dataDir: "/var/lib/yeet"},
		{service: "bad name", catchBin: "/usr/bin/catch", dataDir: "/var/lib/yeet"},
		{service: "app", catchBin: "/path with space/catch", dataDir: "/var/lib/yeet"},
		{service: "app", catchBin: "/usr/bin/catch", dataDir: "/path with space"},
	} {
		if _, err := NewISONetworkUnit(test.service, test.catchBin, test.dataDir); err == nil {
			t.Fatalf("NewISONetworkUnit(%q, %q, %q) returned nil error", test.service, test.catchBin, test.dataDir)
		}
	}
}
