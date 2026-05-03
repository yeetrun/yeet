// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"golang.org/x/sync/errgroup"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/logtail/backoff"
)

type Status string

const (
	StatusRunning Status = "Running"
	StatusStopped Status = "Stopped"
	StatusUnknown Status = "Unknown"
)

// TimerConfig provides the setup for a Timer. The OnCalendar field is required.
type TimerConfig struct {
	Description string `json:",omitempty"` // Description of the timer.
	OnCalendar  string // Run on a calendar event.
	Persistent  bool   // Ensures missed timer events run after system resumes from downtime.
}

const (
	systemdServiceTemplate = `[Unit]
ConditionFileIsExecutable={{.Executable}}
{{if .Wants}}Wants={{.Wants}}{{end}}
{{if .Requires}}Requires={{.Requires}}{{end}}
{{if .Before}}Before={{.Before}}{{end}}
{{if .After}}After={{.After}}{{else if .Requires}}After={{.Requires}}{{end}}

[Service]
{{range .ExecStartPre}}ExecStartPre={{.}}{{end}}
ExecStart={{.Executable}}{{range .Arguments}} {{.}}{{end}}
{{range .ExecStartPost}}ExecStartPost={{.}}{{end}}
{{if or .OneShot .Timer}}Type=oneshot{{end}}
{{if .WorkingDirectory}}WorkingDirectory={{.WorkingDirectory}}{{end}}
{{if .Restart}}Restart={{.Restart}}{{end}}
RestartSec=1
RestartSteps=10
RestartMaxDelaySec=60
{{if .User}}User={{.User}}{{end}}
{{if .EnvFile}}EnvironmentFile={{.EnvFile}}{{end}}
{{if .NetNS}}NetworkNamespacePath=/var/run/netns/{{.NetNS}}{{end}}
{{if .OneShot}}RemainAfterExit=yes{{end}}
{{if .StopCmd}}ExecStop={{.StopCmd}}{{end}}
{{if .ResolvConf}}
BindPaths={{.ResolvConf}}:/etc/resolv.conf
PrivateMounts=yes
{{end}}
[Install]
WantedBy={{.WantedBy}}
`
	systemdTimerTemplate = `[Unit]

[Timer]
OnCalendar={{.OnCalendar}}
Persistent={{.Persistent}}

[Install]
WantedBy=timers.target
`
)

var (
	systemdServiceTmpl = template.Must(template.New("systemdService").Parse(systemdServiceTemplate))
	systemdTimerTmpl   = template.Must(template.New("systemdTimer").Parse(systemdTimerTemplate))
)

type SystemdUnit struct {
	Name string // Required name of the service. No spaces suggested.

	// User is the user to run the service as.
	User string

	// Executable is the path to the executable to run or the command to run.
	Executable string

	// Arguments are the arguments to pass to the service.
	Arguments []string

	// OneShot, when true, will run the service as a oneshot service.
	OneShot bool

	// StopCmd is the command to run to stop the service.
	StopCmd string

	// Timer, when set, will defer running of the service to a separate timer
	// unit. This is used for `cron` like functionality. If Timer is nil, the
	// service is configured normally.
	Timer *TimerConfig

	// EnvFile is the path to an environment file.
	EnvFile string

	// WorkingDirectory is the working directory for the service.
	WorkingDirectory string

	// NetNS is the network namespace the service is in.
	// If empty, the service is on the host network.
	NetNS string

	// Requires is a list of services that this service requires to run.
	// For multiple services, separate with spaces.
	Requires string

	// Wants is a weaker dependency list than Requires.
	// For multiple services, separate with spaces.
	Wants string

	// After controls service ordering. If empty, Requires is used to preserve
	// the historical "requires also means after" behavior of this generator.
	After string

	// Before controls reverse service ordering.
	Before string

	// ExecStartPre commands run before ExecStart.
	ExecStartPre []string

	// ExecStartPost commands run after ExecStart and participate in systemd
	// ordering constraints.
	ExecStartPost []string

	// WantedBy controls the [Install] target list. If empty, multi-user.target
	// is used.
	WantedBy string

	// ResolvConf is the path to the resolv.conf file to use.
	ResolvConf string
}

func (u *SystemdUnit) serviceUnit() string {
	return u.Name + ".service"
}

func (u *SystemdUnit) timerUnit() string {
	return u.Name + ".timer"
}

func (u *SystemdUnit) WriteOutUnitFiles(root string) (map[db.ArtifactName]string, error) {
	servicePath := filepath.Join(root, fileutil.ApplyVersion(u.serviceUnit()))
	if err := u.writeOutService(servicePath); err != nil {
		return nil, err
	}
	paths := map[db.ArtifactName]string{
		db.ArtifactSystemdUnit: servicePath,
	}

	if u.Timer != nil {
		timerPath := filepath.Join(root, fileutil.ApplyVersion(u.timerUnit()))
		if err := u.writeOutTimer(timerPath); err != nil {
			return nil, err
		}
		paths[db.ArtifactSystemdTimerFile] = timerPath
	}

	return paths, nil
}

func (u *SystemdUnit) writeOutService(path string) (err error) {
	// Timer units do not support "always" or "on-success" restarts
	restartDefault := "always"
	if u.Timer != nil || u.OneShot {
		restartDefault = "on-failure"
	}
	wantedBy := u.WantedBy
	if wantedBy == "" {
		wantedBy = "multi-user.target"
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer closeFile(f, &err)
	return systemdServiceTmpl.Execute(f, struct {
		*SystemdUnit
		Restart  string
		WantedBy string
	}{
		u,
		restartDefault,
		wantedBy,
	})
}

func (u *SystemdUnit) writeOutTimer(path string) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer closeFile(f, &err)
	return systemdTimerTmpl.Execute(f, u.Timer)
}

func closeFile(f *os.File, err *error) {
	if closeErr := f.Close(); closeErr != nil && *err == nil {
		*err = closeErr
	}
}

type SystemdService struct {
	db         *db.Store
	cfg        db.ServiceView
	runDir     string
	systemdDir string
}

func (s *SystemdService) Name() string {
	return s.cfg.Name()
}

func (s *SystemdService) run(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	if out, err := cmd.Output(); err != nil {
		return fmt.Errorf("failed to run systemctl %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return nil
}

type artifactInstall struct {
	dstPath string
	unit    string

	primaryUnitIfAvailable bool
}

type installStep struct {
	artifact db.ArtifactName
	artifactInstall
}

func (s *SystemdService) artifactInstaller() map[db.ArtifactName]artifactInstall {
	return map[db.ArtifactName]artifactInstall{
		db.ArtifactSystemdUnit:      {dstPath: s.servicePath(), unit: s.serviceUnit()},
		db.ArtifactSystemdTimerFile: {dstPath: s.timerPath(), unit: s.timerUnit(), primaryUnitIfAvailable: true},

		db.ArtifactNetNSService: {dstPath: s.netnsServicePath(), unit: s.netnsServiceUnit()},
		db.ArtifactNetNSEnv:     {dstPath: filepath.Join(s.runDir, "netns.env")},

		db.ArtifactTypeScriptFile: {dstPath: filepath.Join(s.runDir, "main.ts")},
		db.ArtifactPythonFile:     {dstPath: filepath.Join(s.runDir, "main.py")},
		db.ArtifactBinary:         {dstPath: filepath.Join(s.runDir, s.Name())},
		db.ArtifactEnvFile:        {dstPath: filepath.Join(s.runDir, "env")},

		db.ArtifactTSService: {dstPath: s.tailscaledServicePath(), unit: s.tailscaledServiceUnit()},
		db.ArtifactTSEnv:     {dstPath: filepath.Join(s.runDir, "tailscaled.env")},
		db.ArtifactTSBinary:  {dstPath: filepath.Join(s.runDir, "tailscaled")},
		db.ArtifactTSConfig:  {dstPath: filepath.Join(s.runDir, "tailscaled.json")},
	}
}

func (s *SystemdService) installPlan() []installStep {
	installPaths := s.artifactInstaller()
	artifactOrder := []db.ArtifactName{
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
	plan := make([]installStep, 0, len(artifactOrder))
	for _, artifact := range artifactOrder {
		plan = append(plan, installStep{
			artifact:        artifact,
			artifactInstall: installPaths[artifact],
		})
	}
	return plan
}

func enabledUnitsForInstallPlan(plan []installStep, af db.ArtifactStore, gen int) []string {
	units := []string{}
	for _, step := range plan {
		if _, ok := af.Gen(step.artifact, gen); !ok || step.unit == "" {
			continue
		}
		log.Printf("adding unit %s to enable list", step.unit)
		if step.primaryUnitIfAvailable && len(units) > 0 {
			units[0] = step.unit
			continue
		}
		units = append(units, step.unit)
	}
	return units
}

func (s *SystemdService) installArtifacts(plan []installStep) error {
	af := s.cfg.AsStruct().Artifacts
	for _, step := range plan {
		srcPath, ok := af.Gen(step.artifact, s.cfg.Generation())
		if !ok {
			log.Printf("no %s artifact to install", step.artifact)
			if err := removeOptionalArtifact(step.dstPath); err != nil {
				return err
			}
			continue
		}
		log.Printf("copying %s to %s", srcPath, step.dstPath)
		if err := fileutil.CopyFile(srcPath, step.dstPath); err != nil {
			return err
		}
	}
	return nil
}

func removeOptionalArtifact(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove optional artifact %s: %v", path, err)
	} else if err == nil {
		log.Printf("removed optional artifact %s", path)
	}
	return nil
}

func (s *SystemdService) Install() error {
	plan := s.installPlan()
	if err := s.installArtifacts(plan); err != nil {
		return err
	}

	if err := s.run("daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}

	for _, unit := range enabledUnitsForInstallPlan(plan, s.cfg.AsStruct().Artifacts, s.cfg.Generation()) {
		if err := s.run("enable", unit); err != nil {
			return fmt.Errorf("failed to enable %s: %v", unit, err)
		}
	}
	return nil
}

func (s *SystemdService) serviceUnit() string {
	return s.Name() + ".service"
}

func (s *SystemdService) timerUnit() string {
	return s.Name() + ".timer"
}

func (s *SystemdService) netnsServiceUnit() string {
	return "yeet-" + s.Name() + "-ns.service"
}

func (s *SystemdService) tailscaledServiceUnit() string {
	return "yeet-" + s.Name() + "-ts.service"
}

func (s *SystemdService) servicePath() string {
	return filepath.Join(s.systemdSystemDir(), s.serviceUnit())
}

func (s *SystemdService) tailscaledServicePath() string {
	return filepath.Join(s.systemdSystemDir(), s.tailscaledServiceUnit())
}

func (s *SystemdService) timerPath() string {
	return filepath.Join(s.systemdSystemDir(), s.timerUnit())
}

func (s *SystemdService) netnsServicePath() string {
	return filepath.Join(s.systemdSystemDir(), s.netnsServiceUnit())
}

func (s *SystemdService) systemdSystemDir() string {
	if s.systemdDir != "" {
		return s.systemdDir
	}
	return "/etc/systemd/system"
}

func (s *SystemdService) isInstalled() bool {
	if s.isTimer() && !fileExists(s.timerPath()) {
		return false
	}
	return fileExists(s.servicePath())
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (s *SystemdService) isTimer() bool {
	_, ok := s.cfg.Artifacts().GetOk(db.ArtifactSystemdTimerFile)
	return ok
}

func (s *SystemdService) primaryUnit() string {
	if s.isTimer() {
		return s.timerUnit()
	}
	return s.serviceUnit()
}

func (s *SystemdService) Uninstall() error {
	if err := s.disableAndRemovePrimaryUnitIfInstalled(); err != nil {
		return err
	}
	for _, unit := range s.uninstallDisableUnits()[1:] {
		s.disableNowForCleanup(unit)
	}
	if err := s.removeAuxiliaryUnitFiles(); err != nil {
		return err
	}
	return s.run("daemon-reload")
}

func (s *SystemdService) disableAndRemovePrimaryUnitIfInstalled() error {
	if !s.isInstalled() {
		return nil
	}
	if err := s.run("disable", "--now", s.primaryUnit()); err != nil {
		return err
	}
	return removeFilesIfPresent(s.timerPath(), s.servicePath())
}

func (s *SystemdService) removeAuxiliaryUnitFiles() error {
	return removeFilesIfPresent(s.netnsServicePath(), s.tailscaledServicePath())
}

func removeFilesIfPresent(paths ...string) error {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *SystemdService) uninstallDisableUnits() []string {
	return []string{s.primaryUnit(), s.netnsServiceUnit(), s.tailscaledServiceUnit()}
}

func (s *SystemdService) disableNowForCleanup(unit string) {
	if err := s.run("disable", "--now", unit); err != nil {
		log.Printf("failed to disable optional unit %s during cleanup: %v", unit, err)
	}
}

func (s *SystemdService) Status() (Status, error) {
	if !s.isInstalled() {
		return StatusUnknown, nil
	}
	if err := s.run("is-active", s.primaryUnit()); err != nil {
		return StatusStopped, nil
	}
	return StatusRunning, nil
}

func (s *SystemdService) isActive(unit string) bool {
	if err := s.run("is-active", unit); err != nil {
		return false
	}
	return true
}

func (s *SystemdService) monitorTailscale() (err error) {
	log.Printf("monitoring tailscale for %s", s.Name())
	sock := filepath.Join(s.runDir, "tailscaled.sock")
	lc := local.Client{
		Socket:        sock,
		UseSocketOnly: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	bo := backoff.NewBackoff("tailscale monitor", log.Printf, time.Minute)
	for {
		if err := s.monitorTailscaleBus(ctx, &lc, bo); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			log.Printf("tailscaled socket not found, retrying")
			bo.BackOff(ctx, err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

func (s *SystemdService) monitorTailscaleBus(ctx context.Context, lc *local.Client, bo *backoff.Backoff) (err error) {
	bus, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialNetMap)
	if err != nil {
		return tailscaleWatchError(ctx, err)
	}
	defer closeIPNBus(bus, &err)
	bo.BackOff(ctx, nil)
	return s.storeTailscaleStableID(bus)
}

func tailscaleWatchError(ctx context.Context, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func closeIPNBus(bus *local.IPNBusWatcher, err *error) {
	if closeErr := bus.Close(); closeErr != nil && *err == nil {
		*err = closeErr
	}
}

func (s *SystemdService) storeTailscaleStableID(bus *local.IPNBusWatcher) error {
	for {
		msg, err := bus.Next()
		if err != nil {
			return err
		}
		if msg.NetMap == nil {
			continue
		}
		log.Printf("got netmap")
		_, _, err = s.db.MutateService(s.cfg.Name(), func(d *db.Data, s *db.Service) error {
			s.TSNet.StableID = msg.NetMap.SelfNode.StableID()
			return nil
		})
		return err
	}
}

func (s *SystemdService) Start() error {
	if err := s.StartAuxiliaryUnits(); err != nil {
		return err
	}
	return s.run("start", s.primaryUnit())
}

func (s *SystemdService) StartAuxiliaryUnits() error {
	af := s.cfg.AsStruct().Artifacts
	var wg errgroup.Group
	if _, ok := af.Gen(db.ArtifactNetNSService, s.cfg.Generation()); ok {
		wg.Go(func() error {
			if err := s.run("start", s.netnsServiceUnit()); err != nil {
				return err
			}
			return nil
		})
	}
	if _, ok := af.Gen(db.ArtifactTSService, s.cfg.Generation()); ok {
		wg.Go(func() error {
			log.Printf("starting tailscaled for %s", s.Name())
			if err := s.run("start", s.tailscaledServiceUnit()); err != nil {
				return err
			}
			go func() {
				if err := s.monitorTailscale(); err != nil {
					log.Printf("failed to monitor tailscale: %v", err)
				}
			}()
			return nil
		})
	}
	return wg.Wait()
}

func (s *SystemdService) hasArtifact(a db.ArtifactName) bool {
	af, ok := s.cfg.Artifacts().GetOk(a)
	if !ok {
		return false
	}
	_, ok = af.Refs().GetOk(db.Gen(s.cfg.Generation()))
	return ok
}

func (s *SystemdService) Stop() error {
	if err := s.stopPrimaryIfInstalled(); err != nil {
		return err
	}
	s.stopAuxiliaryUnitsForCleanup()
	return nil
}

func (s *SystemdService) stopPrimaryIfInstalled() error {
	if s.isInstalled() {
		for _, unit := range s.primaryStopUnits() {
			if err := s.run("stop", unit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SystemdService) stopAuxiliaryUnitsForCleanup() {
	for _, unit := range s.auxiliaryStopUnits() {
		if err := s.run("stop", unit); err != nil {
			log.Printf("failed to stop optional unit %s during cleanup: %v", unit, err)
		}
	}
}

func (s *SystemdService) stopUnits() []string {
	units := s.primaryStopUnits()
	return append(units, s.auxiliaryStopUnits()...)
}

func (s *SystemdService) primaryStopUnits() []string {
	units := []string{s.primaryUnit()}
	if s.isTimer() {
		// Also stop the service if it's a timer.
		units = append(units, s.serviceUnit())
	}
	return units
}

func (s *SystemdService) auxiliaryStopUnits() []string {
	units := []string{}
	if s.hasArtifact(db.ArtifactTSService) {
		units = append(units, s.tailscaledServiceUnit())
	}
	if s.hasArtifact(db.ArtifactNetNSService) {
		units = append(units, s.netnsServiceUnit())
	}
	return units
}

func (s *SystemdService) Restart() error {
	if s.isActive(s.primaryUnit()) {
		if s.isTimer() {
			if err := s.run("stop", s.serviceUnit()); err != nil {
				return err
			}
		}
		return s.run("restart", s.primaryUnit())
	}
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

func (s *SystemdService) Enable() error {
	return s.run("enable", s.primaryUnit())
}

func (s *SystemdService) Disable() error {
	if !s.isInstalled() {
		return nil
	}
	return s.run("disable", s.primaryUnit())
}
