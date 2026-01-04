// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/shayne/yeet/pkg/cli"
	"github.com/shayne/yeet/pkg/cmdutil"
	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/svc"
)

func (e *ttyExecer) startCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot start system service")
	}
	return e.runAction("start", "Start service", func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Start(); err != nil {
			return fmt.Errorf("failed to start service: %w", err)
		}
		return nil
	})
}

func (e *ttyExecer) stopCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot stop system service")
	}
	return e.runAction("stop", "Stop service", func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Stop(); err != nil {
			return fmt.Errorf("failed to stop service: %w", err)
		}
		return nil
	})
}

func (e *ttyExecer) rollbackCmdFunc() error {
	ui := e.newProgressUI("rollback")
	ui.Start()
	defer ui.Stop()

	ui.StartStep("Select generation")
	_, s, err := e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
		if s.Generation == 0 {
			return fmt.Errorf("no generation to rollback")
		}
		minG := s.LatestGeneration - maxGenerations
		gen := s.Generation - 1
		if gen < minG {
			return fmt.Errorf("generation %d is too old, earliest rollback is %d", gen, minG)
		}
		if gen == 0 {
			return fmt.Errorf("generation %d is the oldest, cannot rollback", s.Generation)
		}
		s.Generation = gen
		return nil
	})
	if err != nil {
		ui.FailStep(err.Error())
		return fmt.Errorf("failed to rollback service: %w", err)
	}
	gen := s.Generation
	ui.DoneStep(fmt.Sprintf("generation=%d", gen))

	ui.StartStep("Install generation")
	cfg := e.installerCfg()
	i, err := e.s.NewInstaller(cfg)
	if err != nil {
		ui.FailStep(err.Error())
		return fmt.Errorf("failed to create installer: %w", err)
	}
	i.NewCmd = e.newCmd
	if err := i.InstallGen(gen); err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep(fmt.Sprintf("generation=%d", gen))
	return nil
}

func (e *ttyExecer) restartCmdFunc() error {
	return e.runAction("restart", "Restart service", func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Restart(); err != nil {
			return fmt.Errorf("failed to restart service: %w", err)
		}
		return nil
	})
}

func (e *ttyExecer) enableCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot install, reserved service name")
	}
	runner, err := e.serviceRunner()
	if err != nil {
		return err
	}
	enabler, ok := runner.(ServiceEnabler)
	if !ok {
		return fmt.Errorf("service does not support enable")
	}
	return enabler.Enable()
}

func (e *ttyExecer) disableCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot disable system service")
	}

	runner, err := e.serviceRunner()
	if err != nil {
		return err
	}
	enabler, ok := runner.(ServiceEnabler)
	if !ok {
		return fmt.Errorf("service does not support disable")
	}
	return enabler.Disable()
}

func (e *ttyExecer) logsCmdFunc(flags cli.LogsFlags) error {
	// We don't support logs on the system service.
	if e.sn == SystemService {
		return fmt.Errorf("cannot show logs for system service")
	}
	// TODO(shayne): Make tailing optional
	runner, err := e.serviceRunner()
	if err != nil {
		return fmt.Errorf("failed to get service runner: %w", err)
	}
	return runner.Logs(&svc.LogOptions{Follow: flags.Follow, Lines: flags.Lines})
}

func (e *ttyExecer) statusCmdFunc(flags cli.StatusFlags) error {
	formatOut := flags.Format

	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	if !dv.Valid() {
		return fmt.Errorf("no services found")
	}

	var statuses []ServiceStatusData

	if e.sn == SystemService {
		systemdStatuses, err := e.s.SystemdStatuses()
		if err != nil {
			return fmt.Errorf("failed to get systemd statuses: %w", err)
		}
		for sn, status := range systemdStatuses {
			service, err := e.s.serviceView(sn)
			if err != nil {
				return err
			}
			statuses = append(statuses, ServiceStatusData{
				ServiceName: sn,
				ServiceType: ServiceDataTypeForService(service),
				ComponentStatus: []ComponentStatusData{
					{
						Name:   sn,
						Status: ComponentStatusFromServiceStatus(status),
					},
				},
			})
		}
		composeStatuses, err := e.s.DockerComposeStatuses()
		if err != nil {
			return fmt.Errorf("failed to get all docker compose statuses: %w", err)
		}
		for sn, cs := range composeStatuses {
			serviceType := ServiceDataTypeDocker
			if service, err := e.s.serviceView(sn); err == nil {
				serviceType = ServiceDataTypeForService(service)
			}
			if len(cs) == 0 {
				statuses = append(statuses, ServiceStatusData{
					ServiceName: sn,
					ServiceType: serviceType,
					ComponentStatus: []ComponentStatusData{
						{
							Name:   sn,
							Status: ComponentStatusUnknown,
						},
					},
				})
				continue
			}
			data := ServiceStatusData{
				ServiceName:     sn,
				ServiceType:     serviceType,
				ComponentStatus: []ComponentStatusData{},
			}
			for cn, status := range cs {
				data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
					Name:   cn,
					Status: ComponentStatusFromServiceStatus(status),
				})
			}
			statuses = append(statuses, data)
		}
	} else {
		service, err := e.s.serviceView(e.sn)
		if err != nil {
			return fmt.Errorf("failed to get service type: %w", err)
		}
		st := service.ServiceType()
		data := ServiceStatusData{
			ServiceName:     e.sn,
			ServiceType:     ServiceDataTypeForService(service),
			ComponentStatus: []ComponentStatusData{},
		}
		switch st {
		case db.ServiceTypeSystemd:
			status, err := e.s.SystemdStatus(e.sn)
			if err != nil {
				return fmt.Errorf("failed to get systemd status: %w", err)
			}
			data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
				Name:   e.sn,
				Status: ComponentStatusFromServiceStatus(status),
			})
		case db.ServiceTypeDockerCompose:
			cs, err := e.s.DockerComposeStatus(e.sn)
			if err != nil {
				if err == svc.ErrDockerStatusUnknown {
					data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
						Name:   e.sn,
						Status: ComponentStatusUnknown,
					})
					break
				}
				return fmt.Errorf("failed to get docker compose statuses: %w", err)
			}
			if len(cs) == 0 {
				data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
					Name:   e.sn,
					Status: ComponentStatusUnknown,
				})
				return nil
			}
			for cn, status := range cs {
				data.ComponentStatus = append(data.ComponentStatus, ComponentStatusData{
					Name:   cn,
					Status: ComponentStatusFromServiceStatus(status),
				})
			}
		}
		statuses = append(statuses, data)
	}
	slices.SortFunc(statuses, func(a, b ServiceStatusData) int {
		return strings.Compare(a.ServiceName, b.ServiceName)
	})
	for _, status := range statuses {
		slices.SortFunc(status.ComponentStatus, func(a, b ComponentStatusData) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	if formatOut == "json" {
		return json.NewEncoder(e.rw).Encode(statuses)
	}
	if formatOut == "json-pretty" {
		encoder := json.NewEncoder(e.rw)
		encoder.SetIndent("", "  ")
		return encoder.Encode(statuses)
	}

	w := tabwriter.NewWriter(e.rw, 0, 0, 3, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "SERVICE\tTYPE\tCONTAINER\tSTATUS\t")

	for _, status := range statuses {
		for _, component := range status.ComponentStatus {
			if status.ServiceType == ServiceDataTypeDocker {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", status.ServiceName, status.ServiceType, component.Name, component.Status)
			} else {
				fmt.Fprintf(w, "%s\t%s\t-\t%s\t\n", status.ServiceName, status.ServiceType, component.Status)
			}
		}
	}
	return nil
}

func (e *ttyExecer) removeCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot remove system service")
	}
	runner, err := e.serviceRunner()
	if err != nil {
		if errors.Is(err, errNoServiceConfigured) {
			report, err := e.s.RemoveService(e.sn)
			if err != nil {
				return fmt.Errorf("failed to cleanup service %q: %w", e.sn, err)
			}
			e.printRemoveWarnings(report)
			e.printf("service %q not found\n", e.sn)
			return nil
		}
		return fmt.Errorf("failed to get service runner: %w", err)
	}
	// Confirm the removal of the service.
	if ok, err := cmdutil.Confirm(e.rw, e.rw, fmt.Sprintf("Are you sure you want to remove service %q?", e.sn)); err != nil {
		return fmt.Errorf("failed to confirm removal: %w", err)
	} else if !ok {
		return nil
	}

	if err := runner.Remove(); err != nil {
		if errors.Is(err, svc.ErrNotInstalled) {
			// Systemd service is not installed
			e.printf("warning: systemd service %q was not installed\n", e.sn)
		} else {
			e.printf("warning: failed to stop/remove service %q: %v\n", e.sn, err)
		}
	}
	report, err := e.s.RemoveService(e.sn)
	if err != nil {
		return fmt.Errorf("failed to cleanup service %q: %w", e.sn, err)
	}
	e.printRemoveWarnings(report)
	return nil
}

func (e *ttyExecer) printRemoveWarnings(report *RemoveReport) {
	if report == nil {
		return
	}
	for _, warn := range report.Warnings {
		e.printf("warning: %v\n", warn)
	}
}

// ServiceRunner is an interface for the minimal set of methods required to
// manage a service.
type ServiceRunner interface {
	SetNewCmd(func(string, ...string) *exec.Cmd)

	Start() error
	Stop() error
	Restart() error

	Logs(opts *svc.LogOptions) error

	Remove() error
}

// ServiceEnabler is an interface extension for services that can be enabled and
// disabled.
type ServiceEnabler interface {
	Enable() error
	Disable() error
}

func (e *ttyExecer) serviceRunner() (ServiceRunner, error) {
	if e.serviceRunnerFn != nil {
		return e.serviceRunnerFn()
	}
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service type: %w", err)
	}
	var service ServiceRunner
	switch st {
	case db.ServiceTypeSystemd:
		systemd, err := e.s.systemdService(e.sn)
		if err != nil {
			return nil, err
		}
		service = &systemdServiceRunner{SystemdService: systemd}
	case db.ServiceTypeDockerCompose:
		docker, err := e.s.dockerComposeService(e.sn)
		if err != nil {
			return nil, err
		}
		service = &dockerComposeServiceRunner{DockerComposeService: docker}
	default:
		return nil, fmt.Errorf("unhandled service type %q", st)
	}
	if service != nil {
		service.SetNewCmd(e.newCmd)
	}
	return service, nil
}

type systemdServiceRunner struct {
	*svc.SystemdService
	newCmd func(string, ...string) *exec.Cmd
}

func (s *systemdServiceRunner) SetNewCmd(f func(string, ...string) *exec.Cmd) {
	s.newCmd = f
}

func (s *systemdServiceRunner) Start() error {
	return s.SystemdService.Start()
}

func (s *systemdServiceRunner) Stop() error {
	return s.SystemdService.Stop()
}

func (s *systemdServiceRunner) Restart() error {
	return s.SystemdService.Restart()
}

// Enable enables the service and starts it.
func (s *systemdServiceRunner) Enable() error {
	if err := s.SystemdService.Enable(); err != nil {
		return err
	}
	return s.SystemdService.Start()
}

// Disable stops and disables the service.
func (s *systemdServiceRunner) Disable() error {
	if err := s.SystemdService.Stop(); err != nil {
		return err
	}
	return s.SystemdService.Disable()
}

func (s *systemdServiceRunner) Logs(opts *svc.LogOptions) error {
	if opts == nil {
		opts = &svc.LogOptions{}
	}
	args := []string{"--no-pager", "--output=cat"}
	if opts.Follow {
		args = append(args, "--follow")
	}
	if opts.Lines > 0 {
		args = append(args, "--lines="+strconv.Itoa(opts.Lines))
	}
	args = append(args, "--unit="+s.SystemdService.Name())
	c := s.newCmd("journalctl", args...)
	if err := c.Start(); err != nil {
		return fmt.Errorf("failed to start journalctl: %w", err)
	}
	if err := c.Wait(); err != nil {
		return fmt.Errorf("failed to wait for journalctl: %w", err)
	}
	return nil
}

func (s *systemdServiceRunner) Remove() error {
	if err := s.SystemdService.Stop(); err != nil {
		return err
	}
	return s.SystemdService.Uninstall()
}

type dockerComposeServiceRunner struct {
	*svc.DockerComposeService
}

func (s *dockerComposeServiceRunner) SetNewCmd(f func(string, ...string) *exec.Cmd) {
	s.NewCmd = f
}

func (s *dockerComposeServiceRunner) Start() error {
	return s.DockerComposeService.Start()
}

func (s *dockerComposeServiceRunner) Stop() error {
	return s.DockerComposeService.Stop()
}

func (s *dockerComposeServiceRunner) Restart() error {
	return s.DockerComposeService.Restart()
}

func (s *dockerComposeServiceRunner) Logs(opts *svc.LogOptions) error {
	return s.DockerComposeService.Logs(opts)
}

func (s *dockerComposeServiceRunner) Remove() error {
	return s.DockerComposeService.Remove()
}
