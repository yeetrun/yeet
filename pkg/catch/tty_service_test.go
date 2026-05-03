// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type failingStatusWriter struct {
	err error
}

func (w failingStatusWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRenderServiceStatusesTableOutput(t *testing.T) {
	statuses := []ServiceStatusData{
		{
			ServiceName: "timer",
			ServiceType: ServiceDataTypeCron,
			ComponentStatus: []ComponentStatusData{
				{Name: "timer", Status: ComponentStatusStopped},
			},
		},
		{
			ServiceName: "web",
			ServiceType: ServiceDataTypeDocker,
			ComponentStatus: []ComponentStatusData{
				{Name: "api", Status: ComponentStatusRunning},
			},
		},
	}

	var out bytes.Buffer
	if err := renderServiceStatuses(&out, "", statuses); err != nil {
		t.Fatalf("renderServiceStatuses: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered line count = %d, want 3\n%s", len(lines), out.String())
	}
	wantFields := [][]string{
		{"SERVICE", "TYPE", "CONTAINER", "STATUS"},
		{"timer", "cron", "-", "stopped"},
		{"web", "docker", "api", "running"},
	}
	for i, want := range wantFields {
		if got := strings.Fields(lines[i]); !reflect.DeepEqual(got, want) {
			t.Fatalf("line %d fields = %#v, want %#v\n%s", i, got, want, out.String())
		}
	}
}

func TestRenderServiceStatusesTableReturnsWriterError(t *testing.T) {
	writeErr := errors.New("write failed")
	err := renderServiceStatuses(failingStatusWriter{err: writeErr}, "", []ServiceStatusData{
		{
			ServiceName: "web",
			ServiceType: ServiceDataTypeDocker,
			ComponentStatus: []ComponentStatusData{
				{Name: "api", Status: ComponentStatusRunning},
			},
		},
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("renderServiceStatuses error = %v, want %v", err, writeErr)
	}
}

func TestWriteServiceStatusRowReturnsWriterError(t *testing.T) {
	writeErr := errors.New("row write failed")
	err := writeServiceStatusRow(
		failingStatusWriter{err: writeErr},
		ServiceStatusData{ServiceName: "web", ServiceType: ServiceDataTypeDocker},
		ComponentStatusData{Name: "api", Status: ComponentStatusRunning},
	)
	if !errors.Is(err, writeErr) {
		t.Fatalf("writeServiceStatusRow error = %v, want %v", err, writeErr)
	}
}

func TestSystemdLogArgs(t *testing.T) {
	got := systemdLogArgs("web", &svc.LogOptions{Follow: true, Lines: 25})
	want := []string{"--no-pager", "--output=cat", "--follow", "--lines=25", "--unit=web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemdLogArgs = %#v, want %#v", got, want)
	}
}

func TestSystemdLogArgsWithNilOptions(t *testing.T) {
	got := systemdLogArgs("web", nil)
	want := []string{"--no-pager", "--output=cat", "--unit=web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemdLogArgs = %#v, want %#v", got, want)
	}
}

func TestSelectPreviousGeneration(t *testing.T) {
	service := &db.Service{Generation: 3, LatestGeneration: 4}
	if err := selectPreviousGeneration(service); err != nil {
		t.Fatalf("selectPreviousGeneration: %v", err)
	}
	if service.Generation != 2 {
		t.Fatalf("Generation = %d, want 2", service.Generation)
	}
}

func TestSelectPreviousGenerationRejectsTooOldGeneration(t *testing.T) {
	service := &db.Service{Generation: 2, LatestGeneration: maxGenerations + 3}
	err := selectPreviousGeneration(service)
	if err == nil || !strings.Contains(err.Error(), "earliest rollback") {
		t.Fatalf("selectPreviousGeneration error = %v, want earliest rollback error", err)
	}
}

func TestRollbackCmdFuncSelectsPreviousGenerationAndInstallsWithHook(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "svc-rollback", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/g1.service", db.Gen(2): "/tmp/g2.service", "latest": "/tmp/g3.service"}},
	})
	if _, _, err := server.cfg.DB.MutateService("svc-rollback", func(_ *db.Data, s *db.Service) error {
		s.Generation = 3
		s.LatestGeneration = 3
		return nil
	}); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	var installedGen int
	execer := &ttyExecer{
		ctx:      context.Background(),
		s:        server,
		sn:       "svc-rollback",
		rw:       &bytes.Buffer{},
		progress: catchrpc.ProgressQuiet,
		serviceInstallGenFunc: func(cfg InstallerCfg, gen int) error {
			if cfg.ServiceName != "svc-rollback" {
				t.Fatalf("install service = %q, want svc-rollback", cfg.ServiceName)
			}
			installedGen = gen
			return nil
		},
	}

	if err := execer.rollbackCmdFunc(); err != nil {
		t.Fatalf("rollbackCmdFunc returned error: %v", err)
	}
	if installedGen != 2 {
		t.Fatalf("installed generation = %d, want 2", installedGen)
	}
	sv, err := server.serviceView("svc-rollback")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.AsStruct().Generation; got != 2 {
		t.Fatalf("stored generation = %d, want 2", got)
	}
}

func TestServiceActionCommandsUseRunner(t *testing.T) {
	tests := []struct {
		name string
		run  func(*ttyExecer) error
		want []string
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc, want: []string{"start"}},
		{name: "stop", run: (*ttyExecer).stopCmdFunc, want: []string{"stop"}},
		{name: "restart", run: (*ttyExecer).restartCmdFunc, want: []string{"restart"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx:      context.Background(),
				sn:       "svc-a",
				rw:       &bytes.Buffer{},
				progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) {
					return runner, nil
				},
			}

			if err := tc.run(execer); err != nil {
				t.Fatalf("%s command returned error: %v", tc.name, err)
			}
			if !reflect.DeepEqual(runner.calls, tc.want) {
				t.Fatalf("runner calls = %#v, want %#v", runner.calls, tc.want)
			}
		})
	}
}

func TestServiceActionCommandsRejectReservedNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		sn   string
		run  func(*ttyExecer) error
		want string
	}{
		{name: "start sys", sn: SystemService, run: (*ttyExecer).startCmdFunc, want: "cannot start system service"},
		{name: "stop catch", sn: CatchService, run: (*ttyExecer).stopCmdFunc, want: "cannot stop system service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(&ttyExecer{sn: tc.sn, rw: &bytes.Buffer{}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestServiceActionCommandReturnsRunnerError(t *testing.T) {
	startErr := errors.New("start failed")
	runner := &recordingServiceRunner{errs: map[string]error{"start": startErr}}
	execer := &ttyExecer{
		ctx:      context.Background(),
		sn:       "svc-a",
		rw:       &bytes.Buffer{},
		progress: catchrpc.ProgressQuiet,
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	err := execer.startCmdFunc()
	if err == nil {
		t.Fatal("expected start error")
	}
	if !errors.Is(err, startErr) {
		t.Fatalf("start error = %v, want %v", err, startErr)
	}
}

func TestEnableDisableCommandsUseServiceEnabler(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*ttyExecer) error
		want []string
	}{
		{name: "enable", run: (*ttyExecer).enableCmdFunc, want: []string{"enable"}},
		{name: "disable", run: (*ttyExecer).disableCmdFunc, want: []string{"disable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				sn: "svc-a",
				serviceRunnerFn: func() (ServiceRunner, error) {
					return runner, nil
				},
			}

			if err := tc.run(execer); err != nil {
				t.Fatalf("%s returned error: %v", tc.name, err)
			}
			if !reflect.DeepEqual(runner.calls, tc.want) {
				t.Fatalf("runner calls = %#v, want %#v", runner.calls, tc.want)
			}
		})
	}
}

func TestEnableCommandRejectsRunnerWithoutEnableSupport(t *testing.T) {
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return basicServiceRunner{}, nil
		},
	}

	err := execer.enableCmdFunc()
	if err == nil || !strings.Contains(err.Error(), "service does not support enable") {
		t.Fatalf("enable error = %v, want unsupported enable", err)
	}
}

func TestEnableDisableCommandsRejectReservedNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		sn   string
		run  func(*ttyExecer) error
		want string
	}{
		{name: "enable system", sn: SystemService, run: (*ttyExecer).enableCmdFunc, want: "cannot install, reserved service name"},
		{name: "disable catch", sn: CatchService, run: (*ttyExecer).disableCmdFunc, want: "cannot disable system service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(&ttyExecer{sn: tc.sn})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDisableCommandRejectsRunnerWithoutDisableSupport(t *testing.T) {
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return basicServiceRunner{}, nil
		},
	}

	err := execer.disableCmdFunc()
	if err == nil || !strings.Contains(err.Error(), "service does not support disable") {
		t.Fatalf("disable error = %v, want unsupported disable", err)
	}
}

func TestLogsCommandPassesOptionsToRunner(t *testing.T) {
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	if err := execer.logsCmdFunc(cli.LogsFlags{Follow: true, Lines: 42}); err != nil {
		t.Fatalf("logsCmdFunc returned error: %v", err)
	}
	if runner.logOptions == nil {
		t.Fatal("expected log options")
	}
	if !runner.logOptions.Follow || runner.logOptions.Lines != 42 {
		t.Fatalf("log options = %#v, want follow and 42 lines", runner.logOptions)
	}
}

func TestLogsCommandPropagatesRunnerError(t *testing.T) {
	logErr := errors.New("logs failed")
	runner := &recordingServiceRunner{errs: map[string]error{"logs": logErr}}
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	err := execer.logsCmdFunc(cli.LogsFlags{})
	if err == nil || !errors.Is(err, logErr) {
		t.Fatalf("logsCmdFunc error = %v, want %v", err, logErr)
	}
}

func TestLogsCommandRejectsSystemService(t *testing.T) {
	err := (&ttyExecer{sn: SystemService}).logsCmdFunc(cli.LogsFlags{})
	if err == nil || !strings.Contains(err.Error(), "cannot show logs for system service") {
		t.Fatalf("logs error = %v, want system service error", err)
	}
}

func TestStatusCmdFuncRendersSystemStatusesWithoutLiveCommands(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "timer", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/timer.timer"}},
	})
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})

	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: SystemService,
		rw: &out,
		systemdStatusesFunc: func() (map[string]svc.Status, error) {
			return map[string]svc.Status{"timer": svc.StatusStopped}, nil
		},
		dockerComposeStatusesFunc: func() (map[string]svc.DockerComposeStatus, error) {
			return map[string]svc.DockerComposeStatus{
				"web": {"api": svc.StatusRunning},
			}, nil
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("status lines = %d, want 3\n%s", len(lines), out.String())
	}
	if got := strings.Fields(lines[1]); !reflect.DeepEqual(got, []string{"timer", "cron", "-", "stopped"}) {
		t.Fatalf("timer row = %#v\n%s", got, out.String())
	}
	if got := strings.Fields(lines[2]); !reflect.DeepEqual(got, []string{"web", "docker", "api", "running"}) {
		t.Fatalf("web row = %#v\n%s", got, out.String())
	}
}

func TestSingleDockerComposeStatusUnknownRendersUnknownComponent(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "web",
		rw: &out,
		dockerComposeStatusFunc: func(string) (svc.DockerComposeStatus, error) {
			return nil, svc.ErrDockerStatusUnknown
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "web") || !strings.Contains(got, "unknown") {
		t.Fatalf("status output = %q, want unknown web component", got)
	}
}

func TestSingleDockerComposeStatusWithNoComponentsSkipsRender(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "web",
		rw: &out,
		dockerComposeStatusFunc: func(string) (svc.DockerComposeStatus, error) {
			return svc.DockerComposeStatus{}, nil
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("status output = %q, want no render", got)
	}
}

func TestSingleSystemdStatusUsesStatusHook(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "timer", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/timer.timer"}},
	})
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "timer",
		rw: &out,
		systemdStatusFunc: func(sn string) (svc.Status, error) {
			if sn != "timer" {
				t.Fatalf("systemd status service = %q, want timer", sn)
			}
			return svc.StatusRunning, nil
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "timer") || !strings.Contains(got, "running") {
		t.Fatalf("status output = %q, want running timer", got)
	}
}

func TestSingleDockerComposeStatusPropagatesUnexpectedError(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})
	statusErr := errors.New("docker failed")
	execer := &ttyExecer{
		s:  server,
		sn: "web",
		rw: &bytes.Buffer{},
		dockerComposeStatusFunc: func(string) (svc.DockerComposeStatus, error) {
			return nil, statusErr
		},
	}

	err := execer.statusCmdFunc(cli.StatusFlags{})
	if err == nil || !errors.Is(err, statusErr) {
		t.Fatalf("statusCmdFunc error = %v, want %v", err, statusErr)
	}
}

func TestSystemStatusDataPropagatesStatusErrors(t *testing.T) {
	systemdErr := errors.New("systemd failed")
	execer := &ttyExecer{
		systemdStatusesFunc: func() (map[string]svc.Status, error) {
			return nil, systemdErr
		},
	}
	if _, err := execer.systemStatusData(); err == nil || !errors.Is(err, systemdErr) {
		t.Fatalf("systemStatusData systemd error = %v, want %v", err, systemdErr)
	}

	dockerErr := errors.New("docker failed")
	execer = &ttyExecer{
		s: newTestServer(t),
		systemdStatusesFunc: func() (map[string]svc.Status, error) {
			return map[string]svc.Status{}, nil
		},
		dockerComposeStatusesFunc: func() (map[string]svc.DockerComposeStatus, error) {
			return nil, dockerErr
		},
	}
	if _, err := execer.systemStatusData(); err == nil || !errors.Is(err, dockerErr) {
		t.Fatalf("systemStatusData docker error = %v, want %v", err, dockerErr)
	}
}

func TestServiceDataTypeOrDockerFallsBackForMissingService(t *testing.T) {
	execer := &ttyExecer{s: newTestServer(t)}
	if got := execer.serviceDataTypeOrDocker("missing"); got != ServiceDataTypeDocker {
		t.Fatalf("serviceDataTypeOrDocker = %s, want docker", got)
	}
}

func TestStatusCmdFuncWithEmptyDBRendersEmptyTable(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  newTestServer(t),
		sn: SystemService,
		rw: &out,
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := strings.Fields(out.String()); !reflect.DeepEqual(got, []string{"SERVICE", "TYPE", "CONTAINER", "STATUS"}) {
		t.Fatalf("status output fields = %#v, want header only\n%s", got, out.String())
	}
}

func TestRenderServiceStatusesJSON(t *testing.T) {
	statuses := []ServiceStatusData{
		serviceStatusWithComponent("web", ServiceDataTypeDocker, "api", svc.StatusRunning),
	}
	var out bytes.Buffer
	if err := renderServiceStatuses(&out, "json", statuses); err != nil {
		t.Fatalf("render json: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `"serviceName":"web"`) {
		t.Fatalf("json output = %q, want web service", got)
	}
}

func TestRenderServiceStatusesJSONPretty(t *testing.T) {
	statuses := []ServiceStatusData{
		serviceStatusWithComponent("web", ServiceDataTypeDocker, "api", svc.StatusRunning),
	}
	var out bytes.Buffer
	if err := renderServiceStatuses(&out, "json-pretty", statuses); err != nil {
		t.Fatalf("render json-pretty: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\n  {") || !strings.Contains(got, `"serviceName": "web"`) {
		t.Fatalf("json-pretty output = %q, want indented web service", got)
	}
}

func TestRenderServiceStatusesJSONReturnsWriterError(t *testing.T) {
	writeErr := errors.New("json write failed")
	err := renderServiceStatuses(failingStatusWriter{err: writeErr}, "json", []ServiceStatusData{
		serviceStatusWithComponent("web", ServiceDataTypeDocker, "api", svc.StatusRunning),
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("render json error = %v, want %v", err, writeErr)
	}
}

func TestSortServiceStatusesSortsServicesAndComponents(t *testing.T) {
	statuses := []ServiceStatusData{
		{
			ServiceName: "web",
			ServiceType: ServiceDataTypeDocker,
			ComponentStatus: []ComponentStatusData{
				{Name: "worker", Status: ComponentStatusRunning},
				{Name: "api", Status: ComponentStatusStopped},
			},
		},
		{
			ServiceName:     "api",
			ServiceType:     ServiceDataTypeService,
			ComponentStatus: []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}},
		},
	}

	sortServiceStatuses(statuses)

	if statuses[0].ServiceName != "api" || statuses[1].ServiceName != "web" {
		t.Fatalf("service order = %#v", statuses)
	}
	if got := statuses[1].ComponentStatus[0].Name; got != "api" {
		t.Fatalf("first web component = %q, want api", got)
	}
}

func TestRemoveServiceWithoutRunnerCleansConfig(t *testing.T) {
	server := newTestServer(t)
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "missing",
		rw: &out,
	}

	if err := execer.removeServiceWithoutRunner(errNoServiceConfigured); err != nil {
		t.Fatalf("removeServiceWithoutRunner returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `service "missing" not found`) {
		t.Fatalf("output = %q, want not found message", got)
	}
}

func TestRemoveCmdFuncWithYesRemovesRunnerAndConfig(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "svc-remove", db.ServiceType("unknown"), db.ArtifactStore{})
	runner := &recordingServiceRunner{}
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "svc-remove",
		rw: &out,
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatalf("removeCmdFunc returned error: %v", err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"remove"}) {
		t.Fatalf("runner calls = %#v, want remove", runner.calls)
	}
	if _, err := server.serviceView("svc-remove"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
	if got := out.String(); !strings.Contains(got, "warning:") {
		t.Fatalf("remove output = %q, want cleanup warning", got)
	}
}

func TestRemoveCmdFuncReturnsRunnerSetupError(t *testing.T) {
	runnerErr := errors.New("runner failed")
	execer := &ttyExecer{
		sn: "svc-remove",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return nil, runnerErr
		},
	}

	err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true})
	if err == nil || !errors.Is(err, runnerErr) {
		t.Fatalf("removeCmdFunc error = %v, want %v", err, runnerErr)
	}
}

func TestRemoveCmdFuncDeclineSkipsRunnerRemoval(t *testing.T) {
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		sn: "svc-decline",
		rw: readWriter{Reader: strings.NewReader("n\n"), Writer: &bytes.Buffer{}},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc returned error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.calls)
	}
}

func TestConfirmServiceRemovalCanDecline(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{
		sn: "svc-decline",
		rw: readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
	}

	ok, err := execer.confirmServiceRemoval(false)
	if err != nil {
		t.Fatalf("confirmServiceRemoval returned error: %v", err)
	}
	if ok {
		t.Fatal("confirmServiceRemoval = true, want false")
	}
	if got := out.String(); !strings.Contains(got, "Are you sure") {
		t.Fatalf("prompt output = %q, want confirmation prompt", got)
	}
}

func TestRemoveRunnerPrintsWarnings(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "not installed", err: svc.ErrNotInstalled, want: "was not installed"},
		{name: "other error", err: errors.New("stop failed"), want: "failed to stop/remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingServiceRunner{errs: map[string]error{"remove": tc.err}}
			var out bytes.Buffer
			execer := &ttyExecer{sn: "svc-remove", rw: &out}

			execer.removeRunner(runner)
			if got := out.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("remove warning = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServiceRunnerUsesOverride(t *testing.T) {
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	got, err := execer.serviceRunner()
	if err != nil {
		t.Fatalf("serviceRunner returned error: %v", err)
	}
	if got != runner {
		t.Fatalf("serviceRunner = %T, want override runner", got)
	}
}

func TestServiceRunnerForTypeRejectsUnknownType(t *testing.T) {
	execer := &ttyExecer{}
	if _, err := execer.serviceRunnerForType(db.ServiceType("unknown")); err == nil || !strings.Contains(err.Error(), "unhandled service type") {
		t.Fatalf("serviceRunnerForType error = %v, want unhandled type", err)
	}
}

func seedService(t *testing.T, server *Server, name string, serviceType db.ServiceType, artifacts db.ArtifactStore) {
	t.Helper()
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = serviceType
		s.Generation = 1
		s.LatestGeneration = 1
		s.Artifacts = artifacts
		return nil
	}); err != nil {
		t.Fatalf("seed service %q: %v", name, err)
	}
}

type recordingServiceRunner struct {
	calls      []string
	errs       map[string]error
	logOptions *svc.LogOptions
}

func (r *recordingServiceRunner) SetNewCmd(func(string, ...string) *exec.Cmd) {}

func (r *recordingServiceRunner) Start() error {
	r.calls = append(r.calls, "start")
	return r.errs["start"]
}

func (r *recordingServiceRunner) Stop() error {
	r.calls = append(r.calls, "stop")
	return r.errs["stop"]
}

func (r *recordingServiceRunner) Restart() error {
	r.calls = append(r.calls, "restart")
	return r.errs["restart"]
}

func (r *recordingServiceRunner) Logs(opts *svc.LogOptions) error {
	r.calls = append(r.calls, "logs")
	r.logOptions = opts
	return r.errs["logs"]
}

func (r *recordingServiceRunner) Remove() error {
	r.calls = append(r.calls, "remove")
	return r.errs["remove"]
}

func (r *recordingServiceRunner) Enable() error {
	r.calls = append(r.calls, "enable")
	return r.errs["enable"]
}

func (r *recordingServiceRunner) Disable() error {
	r.calls = append(r.calls, "disable")
	return r.errs["disable"]
}

type basicServiceRunner struct{}

func (basicServiceRunner) SetNewCmd(func(string, ...string) *exec.Cmd) {}
func (basicServiceRunner) Start() error                                { return nil }
func (basicServiceRunner) Stop() error                                 { return nil }
func (basicServiceRunner) Restart() error                              { return nil }
func (basicServiceRunner) Logs(*svc.LogOptions) error                  { return nil }
func (basicServiceRunner) Remove() error                               { return nil }
