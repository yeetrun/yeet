// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"bytes"
	"embed"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/env"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/util/must"
)

const (
	dockerPrereqsTargetUnit = "yeet-docker-prereqs.target"
	dockerServiceUnit       = "docker.service"
)

//go:embed netns-scripts/*
var netnsScripts embed.FS

type yeetNSServiceInstaller interface {
	Install() error
	Start() error
}

var (
	executablePath  = os.Executable
	systemdUnitPath = func(unit string) string {
		return filepath.Join("/etc/systemd/system", unit)
	}
	newYeetNSSystemdService = func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
		return svc.NewSystemdService(nil, cfg, runDir)
	}
	systemdUnitActive = func(unit string) bool {
		return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
	}
)

func writeNetNSScripts() (changed bool, err error) {
	files, err := netnsScripts.ReadDir("netns-scripts")
	if err != nil {
		return false, fmt.Errorf("failed to read dir: %v", err)
	}
	for _, file := range files {
		fileChanged, err := writeNetNSScript(file.Name())
		if err != nil {
			return false, err
		}
		changed = changed || fileChanged
	}
	return changed, nil
}

func writeNetNSScript(name string) (bool, error) {
	script, err := netnsScripts.ReadFile("netns-scripts/" + name)
	if err != nil {
		return false, fmt.Errorf("failed to read script: %v", err)
	}
	if prev, err := os.ReadFile(name); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to read prev script: %v", err)
	} else if err == nil && bytes.Equal(prev, script) {
		return false, nil
	}

	if err := os.WriteFile(name, script, 0755); err != nil {
		return false, fmt.Errorf("failed to write script: %v", err)
	}
	log.Printf("wrote %s\n%s", must.Get(filepath.Abs(name)), string(script))
	if err := os.Chmod(name, 0755); err != nil {
		return false, fmt.Errorf("failed to chmod script: %v", err)
	}
	if _, err := os.Stat(name); err != nil {
		return false, fmt.Errorf("failed to stat script: %v", err)
	}
	return true, nil
}

func InstallYeetNSService() error {
	scriptsChanged, err := writeNetNSScripts()
	if err != nil {
		return fmt.Errorf("failed to write netns scripts: %v", err)
	}
	backend, err := DetectFirewallBackend()
	if err != nil {
		return fmt.Errorf("failed to detect firewall backend: %v", err)
	}
	catchBin, err := executablePath()
	if err != nil {
		return fmt.Errorf("failed to resolve catch binary path: %v", err)
	}
	envChanged, err := writeYeetNSEnv(defaultYeetNSEnv(backend, catchBin))
	if err != nil {
		return err
	}

	unitFiles, err := newYeetNSUnit().WriteOutUnitFiles(".")
	if err != nil {
		return fmt.Errorf("failed to write unit files: %v", err)
	}
	defer removeFiles(unitFiles)

	unitChanged, err := yeetNSUnitChanged(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		return err
	}
	if !anyChanged(scriptsChanged, envChanged, unitChanged) {
		return nil
	}
	if err := installYeetNSService(unitFiles); err != nil {
		return err
	}
	return nil
}

func defaultYeetNSEnv(backend FirewallBackend, catchBin string) yeetNSEnv {
	return yeetNSEnv{
		Range:           "192.168.100.0/24",
		HostIP:          "192.168.100.1/32",
		YeetIP:          "192.168.100.2/32",
		BridgeIP:        "192.168.100.254/32",
		BridgeIf:        defaultFirewallBridgeIf,
		FirewallBackend: string(backend),
		CatchBin:        catchBin,
	}
}

func writeYeetNSEnv(ye yeetNSEnv) (bool, error) {
	if err := env.Write("yeet-ns.env.tmp", &ye); err != nil {
		return false, fmt.Errorf("failed to write env: %v", err)
	}
	defer func() {
		_ = os.Remove("yeet-ns.env.tmp")
	}()
	same, err := fileutil.Identical("yeet-ns.env", "yeet-ns.env.tmp")
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to compare env: %v", err)
	}
	if same {
		return false, nil
	}
	log.Println("env file changed, writing new version")
	if err := os.Rename("yeet-ns.env.tmp", "yeet-ns.env"); err != nil {
		return false, fmt.Errorf("failed to rename env: %v", err)
	}
	return true, nil
}

func newYeetNSUnit() *svc.SystemdUnit {
	return &svc.SystemdUnit{
		Name:             "yeet-ns",
		Executable:       must.Get(filepath.Abs("yeet-ns")),
		EnvFile:          must.Get(filepath.Abs("yeet-ns.env")),
		WorkingDirectory: "/",
		OneShot:          true,
		Before:           dockerPrereqsTargetUnit + " " + dockerServiceUnit,
		WantedBy:         "multi-user.target " + dockerPrereqsTargetUnit,
	}
}

func removeFiles(files map[db.ArtifactName]string) {
	for _, f := range files {
		_ = os.Remove(f)
	}
}

func yeetNSUnitChanged(generatedUnit string) (bool, error) {
	same, err := fileutil.Identical(systemdUnitPath("yeet-ns.service"), generatedUnit)
	if err != nil {
		return false, fmt.Errorf("failed to compare yeet-ns unit: %v", err)
	}
	return !same, nil
}

func anyChanged(changes ...bool) bool {
	for _, changed := range changes {
		if changed {
			return true
		}
	}
	return false
}

func installYeetNSService(unitFiles map[db.ArtifactName]string) error {
	cfg := &db.Service{
		Name:       "yeet-ns",
		Generation: 1,
		Artifacts: map[db.ArtifactName]*db.Artifact{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{
				"gen-1": unitFiles[db.ArtifactSystemdUnit],
			}},
			db.ArtifactEnvFile: {Refs: map[db.ArtifactRef]string{
				"gen-1": "yeet-ns.env",
			}},
			db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
				"gen-1": "yeet-ns",
			}},
		},
	}
	// Install and start the service.
	service, err := newYeetNSSystemdService(cfg.View(), ".")
	if err != nil {
		return fmt.Errorf("failed to create service: %v", err)
	}
	alreadyActive := systemdUnitActive("yeet-ns.service")
	if err := service.Install(); err != nil {
		return fmt.Errorf("failed to install service: %v", err)
	}
	if alreadyActive {
		log.Printf("installed updated yeet-ns.service; leaving active namespace running")
		return nil
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("failed to start yeet-ns service: %v", err)
	}

	return nil
}

type yeetNSEnv struct {
	Range           string `env:"RANGE"`
	HostIP          string `env:"HOST_IP"`
	BridgeIP        string `env:"BRIDGE_IP"`
	YeetIP          string `env:"YEET_IP"`
	BridgeIf        string `env:"BRIDGE_IF"`
	FirewallBackend string `env:"FIREWALL_BACKEND"`
	CatchBin        string `env:"CATCH_BIN"`
}

type Service struct {
	ServiceName string       `env:"SERVICE_NAME"`
	ServiceIP   netip.Prefix `env:"SERVICE_IP"`
	Range       netip.Prefix `env:"RANGE"`
	HostIP      netip.Addr   `env:"HOST_IP"`
	YeetIP      netip.Addr   `env:"YEET_IP"`

	MacvlanParent    string `env:"MACVLAN_PARENT"`
	MacvlanVLAN      string `env:"MACVLAN_VLAN"`
	MacvlanMac       string `env:"MACVLAN_MAC"`
	MacvlanInterface string `env:"MACVLAN_INTERFACE"`

	TailscaleTAPInterface string `env:"TAILSCALE_TAP_INTERFACE"`

	ResolvConf string `env:"RESOLV_CONF"`
}

func (e *Service) NetNS() string {
	return "yeet-" + e.ServiceName + "-ns"
}

func (e *Service) ServiceUnit() string {
	return e.NetNS() + ".service"
}

func appendSystemdDep(existing, dep string) string {
	if existing == "" {
		return dep
	}
	return existing + " " + dep
}

func WriteServiceNetNS(binDir, runDir string, se Service) (map[db.ArtifactName]string, error) {
	envFile := filepath.Join(binDir, fileutil.ApplyVersion("netns.env"))
	if err := env.Write(envFile, se); err != nil {
		return nil, fmt.Errorf("failed to write env: %v", err)
	}

	exe := must.Get(filepath.Abs("service-ns"))
	unit := svc.SystemdUnit{
		Name:             se.NetNS(),
		Executable:       exe,
		EnvFile:          filepath.Join(runDir, "netns.env"),
		WorkingDirectory: "/",
		Requires:         "yeet-ns.service",
		After:            "yeet-ns.service",
		Before:           dockerPrereqsTargetUnit + " " + dockerServiceUnit,
		OneShot:          true,
		StopCmd:          exe + " cleanup",
		WantedBy:         "multi-user.target " + dockerPrereqsTargetUnit,
	}
	if se.MacvlanParent != "" {
		unit.Wants = appendSystemdDep(unit.Wants, "network-online.target")
		unit.After = appendSystemdDep(unit.After, "network-online.target")
	}
	if se.TailscaleTAPInterface != "" {
		tsUnit := "yeet-" + se.ServiceName + "-ts.service"
		unit.Requires = appendSystemdDep(unit.Requires, tsUnit)
		unit.After = appendSystemdDep(unit.After, tsUnit)
	}
	artifacts, err := unit.WriteOutUnitFiles(binDir)
	if err != nil {
		return nil, fmt.Errorf("failed to write unit files: %v", err)
	}
	artifacts[db.ArtifactNetNSService] = artifacts[db.ArtifactSystemdUnit]
	delete(artifacts, db.ArtifactSystemdUnit)
	artifacts[db.ArtifactNetNSEnv] = envFile
	return artifacts, nil
}
