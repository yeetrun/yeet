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
