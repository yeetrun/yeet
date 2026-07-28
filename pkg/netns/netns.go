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

const (
	ServiceSubnetCIDR = "192.168.100.0/24"
	ServiceHostIP     = "192.168.100.1"
	ServiceYeetNSIP   = "192.168.100.2"
	ServiceGatewayIP  = "192.168.100.254"
)

//go:embed netns-scripts/*
var netnsScripts embed.FS

type yeetNSServiceInstaller interface {
	Install() error
	Start() error
}

var (
	mkdirAll  = os.MkdirAll
	writeFile = os.WriteFile
	chmodFile = os.Chmod
	readFile  = os.ReadFile

	dhclientEnterHookPath = "/etc/dhcp/dhclient-enter-hooks.d/yeet-netns-resolv"

	systemdUnitPath = func(unit string) string {
		return filepath.Join("/etc/systemd/system", unit)
	}
	newYeetNSSystemdService = func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
		return svc.NewHostSystemdService(nil, cfg, runDir)
	}
	systemdUnitActive = func(unit string) bool {
		return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
	}
	runYeetNSSetup = executeYeetNSSetup
)

func writeNetNSScripts() (changed bool, err error) {
	files, err := netnsScripts.ReadDir("netns-scripts")
	if err != nil {
		return false, fmt.Errorf("failed to read dir: %v", err)
	}
	for _, file := range files {
		if file.Name() == "dhclient-enter-hook-yeet-netns-resolv" {
			continue
		}
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
	if prev, err := readFile(name); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to read prev script: %v", err)
	} else if err == nil && bytes.Equal(prev, script) {
		return false, nil
	}

	if err := writeFile(name, script, 0755); err != nil {
		return false, fmt.Errorf("failed to write script: %v", err)
	}
	log.Printf("wrote %s\n%s", must.Get(filepath.Abs(name)), string(script))
	if err := chmodFile(name, 0755); err != nil {
		return false, fmt.Errorf("failed to chmod script: %v", err)
	}
	if _, err := os.Stat(name); err != nil {
		return false, fmt.Errorf("failed to stat script: %v", err)
	}
	return true, nil
}

func writeDhclientEnterHook() (bool, error) {
	raw, err := netnsScripts.ReadFile("netns-scripts/dhclient-enter-hook-yeet-netns-resolv")
	if err != nil {
		return false, fmt.Errorf("failed to read dhclient enter hook: %v", err)
	}
	if err := mkdirAll(filepath.Dir(dhclientEnterHookPath), 0o755); err != nil {
		return false, fmt.Errorf("failed to create dhclient hook dir: %v", err)
	}
	contentChanged, err := writeDhclientEnterHookContent(raw)
	if err != nil {
		return false, err
	}
	modeChanged, err := chmodDhclientEnterHookIfNeeded()
	if err != nil {
		return false, err
	}
	return contentChanged || modeChanged, nil
}

func writeDhclientEnterHookContent(raw []byte) (bool, error) {
	prev, err := readFile(dhclientEnterHookPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to read previous dhclient hook: %v", err)
	}
	if err == nil && bytes.Equal(prev, raw) {
		return false, nil
	}
	if err := writeFile(dhclientEnterHookPath, raw, 0o644); err != nil {
		return false, fmt.Errorf("failed to write dhclient hook: %v", err)
	}
	return true, nil
}

func chmodDhclientEnterHookIfNeeded() (bool, error) {
	info, err := os.Stat(dhclientEnterHookPath)
	if err != nil {
		return false, fmt.Errorf("failed to stat dhclient hook: %v", err)
	}
	if info.Mode().Perm() == 0o644 {
		return false, nil
	}
	if err := chmodFile(dhclientEnterHookPath, 0o644); err != nil {
		return false, fmt.Errorf("failed to chmod dhclient hook: %v", err)
	}
	return true, nil
}

func InstallYeetNSService(catchBin string) error {
	if !filepath.IsAbs(catchBin) {
		return fmt.Errorf("catch binary path must be absolute: %q", catchBin)
	}
	scriptsChanged, err := writeNetNSScripts()
	if err != nil {
		return fmt.Errorf("failed to write netns scripts: %v", err)
	}
	dhclientHookChanged, err := writeDhclientEnterHook()
	if err != nil {
		return err
	}
	backend, err := DetectFirewallBackend()
	if err != nil {
		return fmt.Errorf("failed to detect firewall backend: %v", err)
	}
	desiredEnv := defaultYeetNSEnv(backend, catchBin)
	envChanged, err := writeYeetNSEnv(desiredEnv)
	if err != nil {
		return err
	}

	unit := newYeetNSUnit()
	unitFiles, err := unit.WriteOutUnitFiles(".")
	if err != nil {
		return fmt.Errorf("failed to write unit files: %v", err)
	}
	defer removeFiles(unitFiles)

	unitChanged, err := yeetNSUnitChanged(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		return err
	}
	if !anyChanged(scriptsChanged, dhclientHookChanged, envChanged, unitChanged) {
		log.Println("yeet-ns artifacts unchanged")
		return nil
	}
	if err := installYeetNSService(unitFiles, unit.Executable, desiredEnv); err != nil {
		return err
	}
	return nil
}

func defaultYeetNSEnv(backend FirewallBackend, catchBin string) yeetNSEnv {
	return yeetNSEnv{
		Range:           ServiceSubnetCIDR,
		HostIP:          ServiceHostIP + "/32",
		YeetIP:          ServiceYeetNSIP + "/32",
		BridgeIP:        ServiceGatewayIP + "/32",
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

func installYeetNSService(unitFiles map[db.ArtifactName]string, scriptPath string, desiredEnv yeetNSEnv) error {
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
	// Install updated artifacts, then converge an active namespace or start an inactive one.
	service, err := newYeetNSSystemdService(cfg.View(), ".")
	if err != nil {
		return fmt.Errorf("failed to create service: %v", err)
	}
	alreadyActive := systemdUnitActive("yeet-ns.service")
	if err := service.Install(); err != nil {
		return fmt.Errorf("failed to install service: %v", err)
	}
	if alreadyActive {
		if err := runYeetNSSetup(scriptPath, desiredEnv); err != nil {
			return fmt.Errorf("failed to converge active yeet-ns service in place: %w", err)
		}
		log.Printf("installed updated yeet-ns artifacts and converged active namespace in place")
		return nil
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("failed to start yeet-ns service: %v", err)
	}
	log.Printf("installed updated yeet-ns artifacts and started inactive namespace")

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

func executeYeetNSSetup(path string, environment yeetNSEnv) error {
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), environment.environ()...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (e yeetNSEnv) environ() []string {
	return []string{
		"RANGE=" + e.Range,
		"HOST_IP=" + e.HostIP,
		"BRIDGE_IP=" + e.BridgeIP,
		"YEET_IP=" + e.YeetIP,
		"BRIDGE_IF=" + e.BridgeIf,
		"FIREWALL_BACKEND=" + e.FirewallBackend,
		"CATCH_BIN=" + e.CatchBin,
	}
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
		EnvFile:          filepath.Join(filepath.Dir(runDir), "env", "netns.env"),
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
