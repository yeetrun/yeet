// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/env"
)

func TestWriteServiceNetNSOrdersBeforeDockerPrereqs(t *testing.T) {
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})

	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir returned error: %v", err)
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll runDir returned error: %v", err)
	}

	artifacts, err := WriteServiceNetNS(binDir, runDir, Service{ServiceName: "media"})
	if err != nil {
		t.Fatalf("WriteServiceNetNS returned error: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactNetNSService])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service\n",
		"Before=yeet-docker-prereqs.target docker.service\n",
		"WantedBy=multi-user.target yeet-docker-prereqs.target\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}

func TestWriteServiceNetNSWaitsForNetworkOnlineForMacvlan(t *testing.T) {
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})

	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir returned error: %v", err)
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll runDir returned error: %v", err)
	}

	artifacts, err := WriteServiceNetNS(binDir, runDir, Service{
		ServiceName:   "media",
		MacvlanParent: "vmbr0",
	})
	if err != nil {
		t.Fatalf("WriteServiceNetNS returned error: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactNetNSService])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Wants=network-online.target\n",
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service network-online.target\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}

func TestWriteServiceNetNSRequiresTailscaleUnit(t *testing.T) {
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})

	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir returned error: %v", err)
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll runDir returned error: %v", err)
	}

	artifacts, err := WriteServiceNetNS(binDir, runDir, Service{
		ServiceName:           "media",
		TailscaleTAPInterface: "tap0",
	})
	if err != nil {
		t.Fatalf("WriteServiceNetNS returned error: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactNetNSService])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Requires=yeet-ns.service yeet-media-ts.service\n",
		"After=yeet-ns.service yeet-media-ts.service\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
	if svc := (&Service{ServiceName: "media"}).ServiceUnit(); svc != "yeet-media-ns.service" {
		t.Fatalf("ServiceUnit = %q, want yeet-media-ns.service", svc)
	}
}

func TestWriteNetNSScriptsWritesScriptsAndSkipsIdenticalFiles(t *testing.T) {
	chdirTemp(t)

	changed, err := writeNetNSScripts()
	if err != nil {
		t.Fatalf("writeNetNSScripts() returned error: %v", err)
	}
	if !changed {
		t.Fatal("writeNetNSScripts() changed = false, want true on first write")
	}
	for _, name := range []string{"service-ns", "yeet-ns"} {
		want, err := netnsScripts.ReadFile("netns-scripts/" + name)
		if err != nil {
			t.Fatalf("ReadFile embedded %s returned error: %v", name, err)
		}
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s returned error: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s content mismatch", name)
		}
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("Stat %s returned error: %v", name, err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0755 {
			t.Fatalf("%s mode = %v, want 0755", name, gotMode)
		}
	}

	changed, err = writeNetNSScripts()
	if err != nil {
		t.Fatalf("second writeNetNSScripts() returned error: %v", err)
	}
	if changed {
		t.Fatal("second writeNetNSScripts() changed = true, want false for identical files")
	}
}

func TestWriteYeetNSEnvWritesAndSkipsIdenticalFiles(t *testing.T) {
	chdirTemp(t)
	ye := defaultYeetNSEnv(BackendNFT, "/usr/local/bin/catch")

	changed, err := writeYeetNSEnv(ye)
	if err != nil {
		t.Fatalf("writeYeetNSEnv returned error: %v", err)
	}
	if !changed {
		t.Fatalf("writeYeetNSEnv changed=false, want true on first write")
	}
	raw, err := os.ReadFile("yeet-ns.env")
	if err != nil {
		t.Fatalf("ReadFile env returned error: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "FIREWALL_BACKEND=nft") || !strings.Contains(got, "CATCH_BIN=/usr/local/bin/catch") {
		t.Fatalf("env file missing expected values:\n%s", got)
	}

	changed, err = writeYeetNSEnv(ye)
	if err != nil {
		t.Fatalf("second writeYeetNSEnv returned error: %v", err)
	}
	if changed {
		t.Fatalf("second writeYeetNSEnv changed=true, want false for identical env")
	}
	if _, err := os.Stat("yeet-ns.env.tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp env file stat error = %v, want missing", err)
	}
}

func TestInstallYeetNSServiceNoopsWhenArtifactsAreCurrent(t *testing.T) {
	root := chdirTemp(t)
	systemdPath := filepath.Join(root, "systemd", "yeet-ns.service")
	if err := os.MkdirAll(filepath.Dir(systemdPath), 0755); err != nil {
		t.Fatalf("MkdirAll systemd dir returned error: %v", err)
	}

	catchBin := "/usr/local/bin/catch"
	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		catchBin:    catchBin,
		systemdPath: systemdPath,
		newService: func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
			t.Fatalf("new systemd service called for unchanged artifacts")
			return nil, nil
		},
		unitActive: func(unit string) bool {
			t.Fatalf("systemdUnitActive called for unchanged artifacts")
			return false
		},
	})
	writeCurrentYeetNSArtifacts(t, BackendNFT, catchBin, systemdPath)

	if err := InstallYeetNSService(); err != nil {
		t.Fatalf("InstallYeetNSService() returned error: %v", err)
	}
}

func TestInstallYeetNSServiceInstallsAndPreservesActiveNamespace(t *testing.T) {
	chdirTemp(t)
	var installCalls int
	var startCalls int

	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: filepath.Join(t.TempDir(), "yeet-ns.service"),
		newService: func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
			if got := cfg.Name(); got != "yeet-ns" {
				t.Fatalf("service name = %q, want yeet-ns", got)
			}
			if runDir != "." {
				t.Fatalf("runDir = %q, want .", runDir)
			}
			return fakeYeetNSSystemdService{
				install: func() error {
					installCalls++
					return nil
				},
				start: func() error {
					startCalls++
					return nil
				},
			}, nil
		},
		unitActive: func(unit string) bool {
			return unit == "yeet-ns.service"
		},
	})

	if err := InstallYeetNSService(); err != nil {
		t.Fatalf("InstallYeetNSService() returned error: %v", err)
	}
	if installCalls != 1 {
		t.Fatalf("install calls = %d, want 1", installCalls)
	}
	if startCalls != 0 {
		t.Fatalf("start calls = %d, want 0 for active namespace", startCalls)
	}
}

func TestInstallYeetNSServiceStartsInactiveNamespace(t *testing.T) {
	chdirTemp(t)
	var installCalls int
	var startCalls int

	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: filepath.Join(t.TempDir(), "yeet-ns.service"),
		newService: func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
			return fakeYeetNSSystemdService{
				install: func() error {
					installCalls++
					return nil
				},
				start: func() error {
					startCalls++
					return nil
				},
			}, nil
		},
		unitActive: func(unit string) bool {
			return false
		},
	})

	if err := InstallYeetNSService(); err != nil {
		t.Fatalf("InstallYeetNSService() returned error: %v", err)
	}
	if installCalls != 1 {
		t.Fatalf("install calls = %d, want 1", installCalls)
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want 1 for inactive namespace", startCalls)
	}
}

func TestInstallYeetNSServiceReturnsExecutableError(t *testing.T) {
	chdirTemp(t)
	wantErr := errors.New("executable failed")
	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		executableErr: wantErr,
		systemdPath:   filepath.Join(t.TempDir(), "yeet-ns.service"),
		newService: func(db.ServiceView, string) (yeetNSServiceInstaller, error) {
			t.Fatalf("new service should not be called after executable error")
			return nil, nil
		},
		unitActive: func(string) bool { return false },
	})

	err := InstallYeetNSService()
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) || !strings.Contains(err.Error(), "failed to resolve catch binary path") {
		t.Fatalf("InstallYeetNSService error = %v, want wrapped %v", err, wantErr)
	}
}

func TestInstallYeetNSServicePropagatesInstallerErrors(t *testing.T) {
	tests := []struct {
		name    string
		service fakeYeetNSSystemdService
		factory func() (yeetNSServiceInstaller, error)
		want    string
	}{
		{
			name: "create service",
			factory: func() (yeetNSServiceInstaller, error) {
				return nil, errors.New("create failed")
			},
			want: "failed to create service",
		},
		{
			name: "install service",
			service: fakeYeetNSSystemdService{
				install: func() error { return errors.New("install failed") },
				start:   func() error { return nil },
			},
			want: "failed to install service",
		},
		{
			name: "start service",
			service: fakeYeetNSSystemdService{
				install: func() error { return nil },
				start:   func() error { return errors.New("start failed") },
			},
			want: "failed to start yeet-ns service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chdirTemp(t)
			withDetectedFirewallBackend(t, BackendNFT)
			withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
				catchBin:    "/usr/local/bin/catch",
				systemdPath: filepath.Join(t.TempDir(), "yeet-ns.service"),
				newService: func(db.ServiceView, string) (yeetNSServiceInstaller, error) {
					if tt.factory != nil {
						return tt.factory()
					}
					return tt.service, nil
				},
				unitActive: func(string) bool {
					return false
				},
			})

			err := InstallYeetNSService()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("InstallYeetNSService error = %v, want %q", err, tt.want)
			}
		})
	}
}

type installYeetNSServiceFakes struct {
	catchBin      string
	executableErr error
	systemdPath   string
	newService    func(db.ServiceView, string) (yeetNSServiceInstaller, error)
	unitActive    func(string) bool
}

type fakeYeetNSSystemdService struct {
	install func() error
	start   func() error
}

func (s fakeYeetNSSystemdService) Install() error {
	return s.install()
}

func (s fakeYeetNSSystemdService) Start() error {
	return s.start()
}

func chdirTemp(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})
	return root
}

func withDetectedFirewallBackend(t *testing.T, backend FirewallBackend) {
	t.Helper()

	oldLookPath := lookPath
	oldRunCombinedOutput := runCombinedOutput
	lookPath = func(name string) (string, error) {
		if backend == BackendNFT && name == "nft" {
			return "/usr/sbin/nft", nil
		}
		return "", os.ErrNotExist
	}
	runCombinedOutput = func(name string, args ...string) ([]byte, error) {
		if backend == BackendNFT && name == "nft" && strings.Join(args, " ") == "--version" {
			return []byte("nftables v1.0.9"), nil
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() {
		lookPath = oldLookPath
		runCombinedOutput = oldRunCombinedOutput
	})
}

func withInstallYeetNSServiceFakes(t *testing.T, fakes installYeetNSServiceFakes) {
	t.Helper()

	oldExecutablePath := executablePath
	oldSystemdUnitPath := systemdUnitPath
	oldNewSystemdService := newYeetNSSystemdService
	oldSystemdUnitActive := systemdUnitActive
	executablePath = func() (string, error) {
		if fakes.executableErr != nil {
			return "", fakes.executableErr
		}
		return fakes.catchBin, nil
	}
	systemdUnitPath = func(unit string) string {
		if unit != "yeet-ns.service" {
			t.Fatalf("systemdUnitPath(%q), want yeet-ns.service", unit)
		}
		return fakes.systemdPath
	}
	newYeetNSSystemdService = fakes.newService
	systemdUnitActive = fakes.unitActive
	t.Cleanup(func() {
		executablePath = oldExecutablePath
		systemdUnitPath = oldSystemdUnitPath
		newYeetNSSystemdService = oldNewSystemdService
		systemdUnitActive = oldSystemdUnitActive
	})
}

func writeCurrentYeetNSArtifacts(t *testing.T, backend FirewallBackend, catchBin, systemdPath string) {
	t.Helper()

	if changed, err := writeNetNSScripts(); err != nil {
		t.Fatalf("writeNetNSScripts() returned error: %v", err)
	} else if !changed {
		t.Fatal("writeNetNSScripts() changed = false, want true during setup")
	}
	if err := env.Write("yeet-ns.env", defaultYeetNSEnv(backend, catchBin)); err != nil {
		t.Fatalf("env.Write returned error: %v", err)
	}
	unitFiles, err := newYeetNSUnit().WriteOutUnitFiles(".")
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	raw, err := os.ReadFile(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile generated unit returned error: %v", err)
	}
	if err := os.WriteFile(systemdPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile systemd unit returned error: %v", err)
	}
	for _, path := range unitFiles {
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove generated unit returned error: %v", err)
		}
	}
}
