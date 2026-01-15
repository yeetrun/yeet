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
	"path/filepath"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/env"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/util/must"
)

//go:embed netns-scripts/*
var netnsScripts embed.FS

func writeNetNSScripts() (changed bool, err error) {
	files, err := netnsScripts.ReadDir("netns-scripts")
	if err != nil {
		return false, fmt.Errorf("failed to read dir: %v", err)
	}
	for _, file := range files {
		script, err := netnsScripts.ReadFile("netns-scripts/" + file.Name())
		if err != nil {
			return false, fmt.Errorf("failed to read script: %v", err)
		}
		if prev, err := os.ReadFile(file.Name()); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("failed to read prev script: %v", err)
		} else if err == nil && bytes.Equal(prev, script) {
			continue
		}

		if err := os.WriteFile(file.Name(), script, 0755); err != nil {
			return false, fmt.Errorf("failed to write script: %v", err)
		}
		changed = true
		log.Printf("wrote %s\n%s", must.Get(filepath.Abs(file.Name())), string(script))
		if err := os.Chmod(file.Name(), 0755); err != nil {
			return false, fmt.Errorf("failed to chmod script: %v", err)
		}
		_, err = os.Stat(file.Name())
		if err != nil {
			return false, fmt.Errorf("failed to stat script: %v", err)
		}
	}
	return changed, nil
}

func InstallYeetNSService() error {
	changed, err := writeNetNSScripts()
	if err != nil {
		return fmt.Errorf("failed to write netns scripts: %v", err)
	}
	ye := yeetNSEnv{
		Range:    "192.168.100.0/24",
		HostIP:   "192.168.100.1/32",
		YeetIP:   "192.168.100.2/32",
		BridgeIP: "192.168.100.254/32",
	}
	if err := env.Write("yeet-ns.env.tmp", &ye); err != nil {
		return fmt.Errorf("failed to write env: %v", err)
	}
	defer os.Remove("yeet-ns.env.tmp")
	if same, err := fileutil.Identical("yeet-ns.env", "yeet-ns.env.tmp"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to compare env: %v", err)
	} else if !same {
		log.Println("env file changed, writing new version")
		changed = true
		if err := os.Rename("yeet-ns.env.tmp", "yeet-ns.env"); err != nil {
			return fmt.Errorf("failed to rename env: %v", err)
		}
	}
	if !changed {
		return nil
	}

	unit := svc.SystemdUnit{
		Name:             "yeet-ns",
		Executable:       must.Get(filepath.Abs("yeet-ns")),
		EnvFile:          must.Get(filepath.Abs("yeet-ns.env")),
		WorkingDirectory: "/",
		OneShot:          true,
	}

	unitFiles, err := unit.WriteOutUnitFiles(".")
	if err != nil {
		return fmt.Errorf("failed to write unit files: %v", err)
	}
	for _, f := range unitFiles {
		defer os.Remove(f)
	}

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
	service, err := svc.NewSystemdService(nil, cfg.View(), ".")
	if err != nil {
		return fmt.Errorf("failed to create service: %v", err)
	}
	if err := service.Install(); err != nil {
		return fmt.Errorf("failed to install service: %v", err)
	}
	if err := service.Restart(); err != nil {
		return fmt.Errorf("failed to start yeet-ns service: %v", err)
	}

	return nil
}

type yeetNSEnv struct {
	Range    string `env:"RANGE"`
	HostIP   string `env:"HOST_IP"`
	BridgeIP string `env:"BRIDGE_IP"`
	YeetIP   string `env:"YEET_IP"`
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
		OneShot:          true,
		StopCmd:          exe + " cleanup",
	}
	if se.TailscaleTAPInterface != "" {
		unit.Requires += " yeet-" + se.ServiceName + "-ts.service"
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
