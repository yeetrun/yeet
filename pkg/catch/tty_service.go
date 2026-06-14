// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

var errUnhandledServiceType = errors.New("unhandled service type")

func (e *ttyExecer) startCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot start system service")
	}
	target := e.managedTargetLabel()
	return e.runAction("start", "Start "+target, func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Start(); err != nil {
			return fmt.Errorf("failed to start %s: %w", target, err)
		}
		return nil
	})
}

func (e *ttyExecer) stopCmdFunc() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot stop system service")
	}
	target := e.managedTargetLabel()
	return e.runAction("stop", "Stop "+target, func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Stop(); err != nil {
			return fmt.Errorf("failed to stop %s: %w", target, err)
		}
		return nil
	})
}

const vmGenerationRollbackUnsupportedMessage = "VM services do not support generation rollback; use yeet snapshots restore for VM disk or checkpoint recovery"

type serviceGenerationView struct {
	Service           string `json:"service"`
	Type              string `json:"type"`
	CurrentGeneration int    `json:"currentGeneration"`
	LatestGeneration  int    `json:"latestGeneration"`
	RollbackSupported bool   `json:"rollbackSupported"`
}

func (e *ttyExecer) rollbackCmdFunc(serviceName string) error {
	ui := e.newProgressUI("rollback")
	ui.Start()
	defer ui.Stop()

	ui.StartStep("Select generation")
	gen, err := e.rollbackGeneration(serviceName)
	if err != nil {
		ui.FailStep(err.Error())
		return fmt.Errorf("failed to rollback service: %w", err)
	}
	ui.DoneStep(fmt.Sprintf("generation=%d", gen))

	return e.installRollbackGeneration(ui, serviceName, gen)
}

func (e *ttyExecer) rollbackGeneration(serviceName string) (int, error) {
	_, service, err := e.s.cfg.DB.MutateService(serviceName, func(_ *db.Data, s *db.Service) error {
		if s.ServiceType == db.ServiceTypeVM {
			return errors.New(vmGenerationRollbackUnsupportedMessage)
		}
		return selectPreviousGeneration(s)
	})
	if err != nil {
		return 0, err
	}
	return service.Generation, nil
}

func selectPreviousGeneration(s *db.Service) error {
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
}

func (e *ttyExecer) installRollbackGeneration(ui *runUI, serviceName string, gen int) error {
	ui.StartStep("Install generation")
	cfg := e.installerCfg()
	cfg.ServiceName = serviceName
	if err := e.installServiceGeneration(cfg, gen); err != nil {
		ui.FailStep(err.Error())
		return err
	}
	ui.DoneStep(fmt.Sprintf("generation=%d", gen))
	return nil
}

func (e *ttyExecer) serviceGenerationsCmdFunc(serviceName string, flags cli.ServiceGenerationsFlags) error {
	sv, err := e.s.serviceView(serviceName)
	if err != nil {
		return err
	}
	return renderServiceGenerationView(e.rw, flags.Format, serviceGenerationViewFromService(sv))
}

func serviceGenerationViewFromService(sv db.ServiceView) serviceGenerationView {
	serviceType := sv.ServiceType()
	return serviceGenerationView{
		Service:           sv.Name(),
		Type:              string(serviceType),
		CurrentGeneration: sv.Generation(),
		LatestGeneration:  sv.LatestGeneration(),
		RollbackSupported: serviceType != db.ServiceTypeVM,
	}
}

func renderServiceGenerationView(w io.Writer, formatOut string, view serviceGenerationView) error {
	if formatOut == "json" {
		return json.NewEncoder(w).Encode(view)
	}
	if formatOut == "json-pretty" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	return renderServiceGenerationTable(w, view)
}

func renderServiceGenerationTable(w io.Writer, view serviceGenerationView) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SERVICE\tTYPE\tCURRENT\tLATEST\tROLLBACK"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%t\n",
		view.Service,
		view.Type,
		view.CurrentGeneration,
		view.LatestGeneration,
		view.RollbackSupported,
	); err != nil {
		return err
	}
	return tw.Flush()
}

func (e *ttyExecer) installService(cfg InstallerCfg) error {
	if e.serviceInstallFunc != nil {
		return e.serviceInstallFunc(cfg)
	}
	i, err := e.s.NewInstaller(cfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	i.NewCmd = e.newCmd
	return i.Install()
}

func (e *ttyExecer) installServiceGeneration(cfg InstallerCfg, gen int) error {
	if e.serviceInstallGenFunc != nil {
		return e.serviceInstallGenFunc(cfg, gen)
	}
	i, err := e.s.NewInstaller(cfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	i.NewCmd = e.newCmd
	return i.InstallGen(gen)
}

func (e *ttyExecer) restartCmdFunc() error {
	target := e.managedTargetLabel()
	return e.runAction("restart", "Restart "+target, func() error {
		runner, err := e.serviceRunner()
		if err != nil {
			return fmt.Errorf("failed to get service runner: %w", err)
		}
		if err := runner.Restart(); err != nil {
			return fmt.Errorf("failed to restart %s: %w", target, err)
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
	if err := e.ensureServicesAvailable(); err != nil {
		return err
	}
	statuses, render, err := e.statusData()
	if err != nil {
		return err
	}
	if !render {
		return nil
	}
	sortServiceStatuses(statuses)
	return renderServiceStatuses(e.rw, flags.Format, statuses)
}

func (e *ttyExecer) ensureServicesAvailable() error {
	dv, err := e.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	if !dv.Valid() {
		return fmt.Errorf("no services found")
	}
	return nil
}

func (e *ttyExecer) statusData() ([]ServiceStatusData, bool, error) {
	if e.sn == SystemService {
		statuses, err := e.systemStatusData()
		return statuses, true, err
	}
	status, render, err := e.singleServiceStatusData()
	if err != nil {
		return nil, false, err
	}
	return []ServiceStatusData{status}, render, nil
}

func (e *ttyExecer) systemStatusData() ([]ServiceStatusData, error) {
	statuses, err := e.systemdStatusData()
	if err != nil {
		return nil, err
	}
	composeStatuses, err := e.dockerComposeStatusData()
	if err != nil {
		return nil, err
	}
	vmStatuses, err := e.vmStatusData()
	if err != nil {
		return nil, err
	}
	statuses = append(statuses, composeStatuses...)
	return append(statuses, vmStatuses...), nil
}

func (e *ttyExecer) systemdStatusData() ([]ServiceStatusData, error) {
	systemdStatuses, err := e.systemdStatuses()
	if err != nil {
		return nil, fmt.Errorf("failed to get systemd statuses: %w", err)
	}
	statuses := make([]ServiceStatusData, 0, len(systemdStatuses))
	for sn, status := range systemdStatuses {
		data, err := e.systemdServiceStatusData(sn, status)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, data)
	}
	return statuses, nil
}

func (e *ttyExecer) systemdServiceStatusData(sn string, status svc.Status) (ServiceStatusData, error) {
	service, err := e.s.serviceView(sn)
	if err != nil {
		return ServiceStatusData{}, err
	}
	return serviceStatusWithComponent(sn, ServiceDataTypeForService(service), sn, status), nil
}

func (e *ttyExecer) dockerComposeStatusData() ([]ServiceStatusData, error) {
	composeStatuses, err := e.dockerComposeStatuses()
	if err != nil {
		return nil, fmt.Errorf("failed to get all docker compose statuses: %w", err)
	}
	statuses := make([]ServiceStatusData, 0, len(composeStatuses))
	for sn, status := range composeStatuses {
		statuses = append(statuses, composeServiceStatusData(sn, e.serviceDataTypeOrDocker(sn), status))
	}
	return statuses, nil
}

func (e *ttyExecer) vmStatusData() ([]ServiceStatusData, error) {
	dv, err := e.s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get VM services: %w", err)
	}
	names := serviceNamesByType(dv.AsStruct().Services, db.ServiceTypeVM)
	statuses := make([]ServiceStatusData, 0, len(names))
	for _, sn := range names {
		status, err := e.vmStatus(sn)
		if err != nil {
			return nil, fmt.Errorf("failed to get VM status for %s: %w", sn, err)
		}
		statuses = append(statuses, serviceStatusWithComponent(sn, ServiceDataTypeVM, sn, status))
	}
	return statuses, nil
}

func (e *ttyExecer) serviceDataTypeOrDocker(sn string) ServiceDataType {
	if service, err := e.s.serviceView(sn); err == nil {
		return ServiceDataTypeForService(service)
	}
	return ServiceDataTypeDocker
}

func (e *ttyExecer) singleServiceStatusData() (ServiceStatusData, bool, error) {
	service, err := e.s.serviceView(e.sn)
	if err != nil {
		return ServiceStatusData{}, false, fmt.Errorf("failed to get service type: %w", err)
	}
	data := serviceStatusData(e.sn, ServiceDataTypeForService(service))
	return e.populateSingleServiceStatus(data, service.ServiceType())
}

func (e *ttyExecer) populateSingleServiceStatus(data ServiceStatusData, serviceType db.ServiceType) (ServiceStatusData, bool, error) {
	switch serviceType {
	case db.ServiceTypeSystemd:
		return e.addSingleSystemdStatus(data)
	case db.ServiceTypeDockerCompose:
		return e.addSingleDockerComposeStatus(data)
	case db.ServiceTypeVM:
		return e.addSingleVMStatus(data)
	default:
		return data, true, nil
	}
}

func (e *ttyExecer) addSingleSystemdStatus(data ServiceStatusData) (ServiceStatusData, bool, error) {
	status, err := e.systemdStatus(e.sn)
	if err != nil {
		return ServiceStatusData{}, false, fmt.Errorf("failed to get systemd status: %w", err)
	}
	data.ComponentStatus = []ComponentStatusData{componentStatusData(e.sn, status)}
	return data, true, nil
}

func (e *ttyExecer) addSingleDockerComposeStatus(data ServiceStatusData) (ServiceStatusData, bool, error) {
	statuses, err := e.dockerComposeStatus(e.sn)
	if err != nil {
		return e.handleDockerComposeStatusError(data, err)
	}
	if len(statuses) == 0 {
		data.ComponentStatus = []ComponentStatusData{unknownComponentStatus(e.sn)}
		return data, false, nil
	}
	data.ComponentStatus = componentStatuses(statuses)
	return data, true, nil
}

func (e *ttyExecer) addSingleVMStatus(data ServiceStatusData) (ServiceStatusData, bool, error) {
	status, err := e.vmStatus(e.sn)
	if err != nil {
		return ServiceStatusData{}, false, fmt.Errorf("failed to get VM status: %w", err)
	}
	data.ComponentStatus = []ComponentStatusData{componentStatusData(e.sn, status)}
	return data, true, nil
}

func (e *ttyExecer) handleDockerComposeStatusError(data ServiceStatusData, err error) (ServiceStatusData, bool, error) {
	if err == svc.ErrDockerStatusUnknown {
		data.ComponentStatus = []ComponentStatusData{unknownComponentStatus(e.sn)}
		return data, true, nil
	}
	return ServiceStatusData{}, false, fmt.Errorf("failed to get docker compose statuses: %w", err)
}

func (e *ttyExecer) systemdStatus(sn string) (svc.Status, error) {
	if e.systemdStatusFunc != nil {
		return e.systemdStatusFunc(sn)
	}
	return e.s.SystemdStatus(sn)
}

func (e *ttyExecer) vmStatus(sn string) (svc.Status, error) {
	if e.systemdStatusFunc != nil {
		return e.systemdStatusFunc(vmSystemdUnitName(sn))
	}
	runner := &vmRunner{name: sn}
	runner.SetNewCmd(e.newCmd)
	return runner.Status()
}

func (e *ttyExecer) systemdStatuses() (map[string]svc.Status, error) {
	if e.systemdStatusesFunc != nil {
		return e.systemdStatusesFunc()
	}
	return e.s.SystemdStatuses()
}

func (e *ttyExecer) dockerComposeStatus(sn string) (svc.DockerComposeStatus, error) {
	if e.dockerComposeStatusFunc != nil {
		return e.dockerComposeStatusFunc(sn)
	}
	return e.s.DockerComposeStatus(sn)
}

func (e *ttyExecer) dockerComposeStatuses() (map[string]svc.DockerComposeStatus, error) {
	if e.dockerComposeStatusesFunc != nil {
		return e.dockerComposeStatusesFunc()
	}
	return e.s.DockerComposeStatuses()
}

func serviceStatusData(name string, serviceType ServiceDataType) ServiceStatusData {
	return ServiceStatusData{
		ServiceName:     name,
		ServiceType:     serviceType,
		ComponentStatus: []ComponentStatusData{},
	}
}

func serviceStatusWithComponent(serviceName string, serviceType ServiceDataType, componentName string, status svc.Status) ServiceStatusData {
	data := serviceStatusData(serviceName, serviceType)
	data.ComponentStatus = []ComponentStatusData{componentStatusData(componentName, status)}
	return data
}

func composeServiceStatusData(serviceName string, serviceType ServiceDataType, statuses svc.DockerComposeStatus) ServiceStatusData {
	if len(statuses) == 0 {
		data := serviceStatusData(serviceName, serviceType)
		data.ComponentStatus = []ComponentStatusData{unknownComponentStatus(serviceName)}
		return data
	}
	data := serviceStatusData(serviceName, serviceType)
	data.ComponentStatus = componentStatuses(statuses)
	return data
}

func componentStatusData(name string, status svc.Status) ComponentStatusData {
	return ComponentStatusData{
		Name:   name,
		Status: ComponentStatusFromServiceStatus(status),
	}
}

func unknownComponentStatus(name string) ComponentStatusData {
	return ComponentStatusData{
		Name:   name,
		Status: ComponentStatusUnknown,
	}
}

func componentStatuses(statuses svc.DockerComposeStatus) []ComponentStatusData {
	components := make([]ComponentStatusData, 0, len(statuses))
	for name, status := range statuses {
		components = append(components, componentStatusData(name, status))
	}
	return components
}

func sortServiceStatuses(statuses []ServiceStatusData) {
	slices.SortFunc(statuses, func(a, b ServiceStatusData) int {
		return strings.Compare(a.ServiceName, b.ServiceName)
	})
	for _, status := range statuses {
		slices.SortFunc(status.ComponentStatus, func(a, b ComponentStatusData) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
}

func renderServiceStatuses(w io.Writer, formatOut string, statuses []ServiceStatusData) error {
	if formatOut == "json" {
		return json.NewEncoder(w).Encode(statuses)
	}
	if formatOut == "json-pretty" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(statuses)
	}
	return renderServiceStatusTable(w, statuses)
}

func renderServiceStatusTable(w io.Writer, statuses []ServiceStatusData) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if err := writeServiceStatusHeader(tw); err != nil {
		return err
	}
	if err := writeServiceStatusRows(tw, statuses); err != nil {
		return err
	}
	return tw.Flush()
}

func writeServiceStatusHeader(w io.Writer) error {
	_, err := fmt.Fprintln(w, "SERVICE\tTYPE\tCONTAINER\tSTATUS\t")
	return err
}

func writeServiceStatusRows(w io.Writer, statuses []ServiceStatusData) error {
	for _, status := range statuses {
		if err := writeServiceStatusComponents(w, status); err != nil {
			return err
		}
	}
	return nil
}

func writeServiceStatusComponents(w io.Writer, status ServiceStatusData) error {
	for _, component := range status.ComponentStatus {
		if err := writeServiceStatusRow(w, status, component); err != nil {
			return err
		}
	}
	return nil
}

func writeServiceStatusRow(w io.Writer, status ServiceStatusData, component ComponentStatusData) error {
	if status.ServiceType == ServiceDataTypeDocker {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", status.ServiceName, status.ServiceType, component.Name, component.Status)
		return err
	}
	_, err := fmt.Fprintf(w, "%s\t%s\t-\t%s\t\n", status.ServiceName, status.ServiceType, component.Status)
	return err
}

func (e *ttyExecer) removeCmdFunc(flags cli.RemoveFlags) error {
	doneValidate := e.traceBlock("remove validate")
	if err := e.validateServiceRemoval(); err != nil {
		doneValidate()
		return err
	}
	doneValidate()
	doneRunnerLookup := e.traceBlock("remove service runner")
	runner, err := e.serviceRunner()
	doneRunnerLookup()
	if err != nil {
		return e.removeServiceWithoutRunner(flags, err)
	}
	doneConfirm := e.traceBlock("remove confirm")
	ok, err := e.confirmServiceRemoval(flags.Yes)
	doneConfirm()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	doneRunnerRemove := e.traceBlock("remove runner")
	e.removeRunner(runner)
	doneRunnerRemove()
	return e.removeServiceConfig(flags)
}

func (e *ttyExecer) validateServiceRemoval() error {
	if e.sn == SystemService || e.sn == CatchService {
		return fmt.Errorf("cannot remove system service")
	}
	return nil
}

func (e *ttyExecer) removeServiceWithoutRunner(flags cli.RemoveFlags, err error) error {
	if !errors.Is(err, errNoServiceConfigured) && !errors.Is(err, errUnhandledServiceType) {
		return fmt.Errorf("failed to get service runner: %w", err)
	}
	runnerErr := err
	report, err := e.s.RemoveServiceWithOptions(e.sn, RemoveOptions{CleanData: flags.CleanData, Trace: e.tracef})
	if err != nil {
		return fmt.Errorf("failed to cleanup %s %q: %w", e.managedTargetLabel(), e.sn, err)
	}
	e.printRemoveWarnings(report)
	if errors.Is(runnerErr, errNoServiceConfigured) {
		e.printf("service %q not found\n", e.sn)
	}
	return nil
}

func (e *ttyExecer) confirmServiceRemoval(yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	ok, err := cmdutil.Confirm(e.rw, e.rw, fmt.Sprintf("Are you sure you want to remove %s %q?", e.managedTargetLabel(), e.sn))
	if err != nil {
		return false, fmt.Errorf("failed to confirm removal: %w", err)
	}
	return ok, nil
}

func (e *ttyExecer) removeRunner(runner ServiceRunner) {
	if err := runner.Remove(); err != nil {
		if errors.Is(err, svc.ErrNotInstalled) {
			e.printf("warning: %s %q was not installed\n", e.managedTargetLabel(), e.sn)
		} else {
			e.printf("warning: failed to stop/remove %s %q: %v\n", e.managedTargetLabel(), e.sn, err)
		}
	}
}

func (e *ttyExecer) removeServiceConfig(flags cli.RemoveFlags) error {
	report, err := e.s.RemoveServiceWithOptions(e.sn, RemoveOptions{CleanData: flags.CleanData, Trace: e.tracef})
	if err != nil {
		return fmt.Errorf("failed to cleanup %s %q: %w", e.managedTargetLabel(), e.sn, err)
	}
	e.printRemoveWarnings(report)
	return nil
}

func (e *ttyExecer) managedTargetLabel() string {
	if e.s == nil {
		return "service"
	}
	service, err := e.s.serviceView(e.sn)
	if err != nil {
		return "service"
	}
	if service.ServiceType() == db.ServiceTypeVM {
		return "VM"
	}
	return "service"
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
	service, err := e.newServiceRunner()
	if err != nil {
		return nil, err
	}
	service.SetNewCmd(e.newCmd)
	return service, nil
}

func (e *ttyExecer) newServiceRunner() (ServiceRunner, error) {
	st, err := e.s.serviceType(e.sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service type: %w", err)
	}
	return e.serviceRunnerForType(st)
}

func (e *ttyExecer) serviceRunnerForType(st db.ServiceType) (ServiceRunner, error) {
	switch st {
	case db.ServiceTypeSystemd:
		return e.systemdRunner()
	case db.ServiceTypeDockerCompose:
		return e.dockerComposeRunner()
	case db.ServiceTypeVM:
		return &vmRunner{name: e.sn}, nil
	default:
		return nil, fmt.Errorf("%w %q", errUnhandledServiceType, st)
	}
}

func (e *ttyExecer) systemdRunner() (ServiceRunner, error) {
	systemd, err := e.s.systemdService(e.sn)
	if err != nil {
		return nil, err
	}
	return &systemdServiceRunner{SystemdService: systemd}, nil
}

func (e *ttyExecer) dockerComposeRunner() (ServiceRunner, error) {
	docker, err := e.s.dockerComposeService(e.sn)
	if err != nil {
		return nil, err
	}
	return &dockerComposeServiceRunner{DockerComposeService: docker}, nil
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
	c := s.newCmd("journalctl", systemdLogArgs(s.Name(), opts)...)
	if err := c.Start(); err != nil {
		return fmt.Errorf("failed to start journalctl: %w", err)
	}
	if err := c.Wait(); err != nil {
		return fmt.Errorf("failed to wait for journalctl: %w", err)
	}
	return nil
}

func systemdLogArgs(unit string, opts *svc.LogOptions) []string {
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
	return append(args, "--unit="+unit)
}

func (s *systemdServiceRunner) Remove() error {
	if err := s.SystemdService.Stop(); err != nil {
		return err
	}
	return s.Uninstall()
}

type dockerComposeServiceRunner struct {
	*svc.DockerComposeService
}

func (s *dockerComposeServiceRunner) SetNewCmd(f func(string, ...string) *exec.Cmd) {
	s.NewCmd = f
	s.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return f(name, args...)
	}
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
