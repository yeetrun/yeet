// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

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
		db.ArtifactBinary,
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
		"RemainAfterExit=yes\n",
		"ExecStop=/opt/demo/bin/demo-stop\n",
		"BindPaths=/run/demo/resolv.conf:/etc/resolv.conf\n",
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
}

func TestSystemdServiceInstallCopiesArtifactsRemovesStaleOptionalAndEnablesTimerPrimary(t *testing.T) {
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
	timerSrc := writeTempFile(t, tmp, "demo.timer", "timer unit\n")
	netnsSrc := writeTempFile(t, tmp, "netns.service", "netns unit\n")
	binSrc := writeTempFile(t, tmp, "demo-bin", "binary\n")
	envSrc := writeTempFile(t, tmp, "env", "ENV=1\n")
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
		},
	}
	svc := &SystemdService{cfg: cfg.View(), runDir: runDir, systemdDir: systemdDir}

	if err := svc.Install(); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	assertFileContent(t, filepath.Join(systemdDir, "demo.service"), "service unit\n")
	assertFileContent(t, filepath.Join(systemdDir, "demo.timer"), "timer unit\n")
	assertFileContent(t, filepath.Join(systemdDir, "yeet-demo-ns.service"), "netns unit\n")
	assertFileContent(t, filepath.Join(runDir, "demo"), "binary\n")
	assertFileContent(t, filepath.Join(runDir, "env"), "ENV=1\n")
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
