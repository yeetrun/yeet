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
{{if .Requires}}Requires={{.Requires}}{{end}}
{{if .Requires}}After={{.Requires}}{{end}}

[Service]
ExecStart={{.Executable}}{{range .Arguments}} {{.}}{{end}}
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
WantedBy=multi-user.target
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

func (u *SystemdUnit) writeOutService(path string) error {
	// Timer units do not support "always" or "on-success" restarts
	restartDefault := "always"
	if u.Timer != nil || u.OneShot {
		restartDefault = "on-failure"
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return systemdServiceTmpl.Execute(f, struct {
		*SystemdUnit
		Restart string
	}{
		u,
		restartDefault,
	})
}

func (u *SystemdUnit) writeOutTimer(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return systemdTimerTmpl.Execute(f, u.Timer)
}

type SystemdService struct {
	db     *db.Store
	cfg    db.ServiceView
	runDir string
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

func (s *SystemdService) Install() error {
	af := s.cfg.AsStruct().Artifacts
	installPaths := s.artifactInstaller()

	unitsToEnable := []string{}
	for _, k := range []db.ArtifactName{
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
	} {
		dst := installPaths[k]
		srcPath, ok := af.Gen(k, s.cfg.Generation())
		if !ok {
			log.Printf("no %s artifact to install", k)
			if err := os.Remove(dst.dstPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove optional artifact %s: %v", dst.dstPath, err)
			} else if err == nil {
				log.Printf("removed optional artifact %s", dst.dstPath)
			}
			continue
		}
		log.Printf("copying %s to %s", srcPath, dst.dstPath)
		if err := fileutil.CopyFile(srcPath, dst.dstPath); err != nil {
			return err
		}
		if dst.unit != "" {
			log.Printf("adding unit %s to enable list", dst.unit)
			if dst.primaryUnitIfAvailable && len(unitsToEnable) > 0 {
				unitsToEnable[0] = dst.unit
			} else {
				unitsToEnable = append(unitsToEnable, dst.unit)
			}
		}
	}

	if err := s.run("daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}

	for _, unit := range unitsToEnable {
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
	return "/etc/systemd/system/" + s.serviceUnit()
}

func (s *SystemdService) tailscaledServicePath() string {
	return "/etc/systemd/system/" + s.tailscaledServiceUnit()
}

func (s *SystemdService) timerPath() string {
	return "/etc/systemd/system/" + s.timerUnit()
}

func (s *SystemdService) netnsServicePath() string {
	return "/etc/systemd/system/" + s.netnsServiceUnit()
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
	if s.isInstalled() {
		if err := s.run("disable", "--now", s.primaryUnit()); err != nil {
			return err
		}
		if err := os.Remove(s.timerPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(s.servicePath()); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	s.run("disable", "--now", s.netnsServiceUnit())
	if err := os.Remove(s.netnsServicePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.run("disable", "--now", s.tailscaledServiceUnit())
	if err := os.Remove(s.tailscaledServicePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.run("daemon-reload")
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
	defer func() {
		if err != nil {
			log.Printf("failed to monitor tailscale: %v", err)
		}
	}()
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
		bus, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialNetMap)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				log.Printf("tailscaled socket not found, retrying")
				bo.BackOff(ctx, err)
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		defer bus.Close()
		bo.BackOff(ctx, nil)
		for {
			msg, err := bus.Next()
			if err != nil {
				return err
			}
			if msg.NetMap != nil {
				log.Printf("got netmap")
				_, _, err := s.db.MutateService(s.cfg.Name(), func(d *db.Data, s *db.Service) error {
					s.TSNet.StableID = msg.NetMap.SelfNode.StableID()
					return nil
				})
				return err
			}
		}
	}
}

func (s *SystemdService) Start() error {
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
			go s.monitorTailscale()
			return nil
		})
	}
	if err := wg.Wait(); err != nil {
		return err
	}
	return s.run("start", s.primaryUnit())
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
	if s.isInstalled() {
		if err := s.run("stop", s.primaryUnit()); err != nil {
			return err
		}
		if s.isTimer() {
			// Also stop the service if it's a timer.
			if err := s.run("stop", s.serviceUnit()); err != nil {
				return err
			}
		}
	}
	if s.hasArtifact(db.ArtifactTSService) {
		s.run("stop", s.tailscaledServiceUnit())
	}
	if s.hasArtifact(db.ArtifactNetNSService) {
		s.run("stop", s.netnsServiceUnit())
	}
	return nil
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
