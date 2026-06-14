// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestNewYeetDNSUnitRunsCatchDNSCommand(t *testing.T) {
	unit := newYeetDNSUnit("/usr/local/bin/catch", "/srv/yeet")
	files, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles: %v", err)
	}
	unitRaw, err := os.ReadFile(files[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("read generated unit: %v", err)
	}
	unitText := string(unitRaw)
	for _, want := range []string{
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service\n",
		"PartOf=catch.service\n",
		"ExecStart=/usr/local/bin/catch -data-dir /srv/yeet dns\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(unitText, want) {
			t.Fatalf("unit missing %q:\n%s", want, unitText)
		}
	}
}

func TestInstallYeetDNSServiceInstallsAndStarts(t *testing.T) {
	systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-dns.service")
	var systemctlCalls [][]string
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: systemdPath,
		unitActive:  func(string) bool { return false },
		systemctl: func(args ...string) error {
			systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
			return nil
		},
	})

	if err := installYeetDNSService("/srv/yeet"); err != nil {
		t.Fatalf("installYeetDNSService: %v", err)
	}

	wantCalls := [][]string{
		{"daemon-reload"},
		{"enable", "yeet-dns.service"},
		{"start", "yeet-dns.service"},
	}
	if !reflect.DeepEqual(systemctlCalls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, wantCalls)
	}
	unitRaw, err := os.ReadFile(systemdPath)
	if err != nil {
		t.Fatalf("read installed unit: %v", err)
	}
	if !strings.Contains(string(unitRaw), "ExecStart=/usr/local/bin/catch -data-dir /srv/yeet dns\n") {
		t.Fatalf("installed unit missing ExecStart:\n%s", string(unitRaw))
	}
}

func TestInstallYeetDNSServiceRestartsChangedActiveService(t *testing.T) {
	systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-dns.service")
	var systemctlCalls [][]string
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: systemdPath,
		unitActive:  func(string) bool { return true },
		systemctl: func(args ...string) error {
			systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
			return nil
		},
	})

	if err := installYeetDNSService("/srv/yeet"); err != nil {
		t.Fatalf("installYeetDNSService: %v", err)
	}

	wantCalls := [][]string{
		{"daemon-reload"},
		{"enable", "yeet-dns.service"},
		{"try-restart", "yeet-dns.service"},
	}
	if !reflect.DeepEqual(systemctlCalls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, wantCalls)
	}
}

func TestInstallYeetDNSServicePreservesUnchangedActiveService(t *testing.T) {
	systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-dns.service")
	unitFiles, err := newYeetDNSUnit("/usr/local/bin/catch", "/srv/yeet").WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles: %v", err)
	}
	unitRaw, err := os.ReadFile(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("read generated unit: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(systemdPath), 0o755); err != nil {
		t.Fatalf("create systemd dir: %v", err)
	}
	if err := os.WriteFile(systemdPath, unitRaw, 0o644); err != nil {
		t.Fatalf("write installed unit: %v", err)
	}

	var systemctlCalls [][]string
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: systemdPath,
		unitActive:  func(string) bool { return true },
		systemctl: func(args ...string) error {
			systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
			return nil
		},
	})

	if err := installYeetDNSService("/srv/yeet"); err != nil {
		t.Fatalf("installYeetDNSService: %v", err)
	}

	wantCalls := [][]string{
		{"enable", "yeet-dns.service"},
	}
	if !reflect.DeepEqual(systemctlCalls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, wantCalls)
	}
}

func TestInstallYeetDNSServicePropagatesStartErrors(t *testing.T) {
	systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-dns.service")
	withYeetDNSServiceFakes(t, yeetDNSServiceFakes{
		catchBin:    "/usr/local/bin/catch",
		systemdPath: systemdPath,
		unitActive:  func(string) bool { return false },
		systemctl: func(args ...string) error {
			if reflect.DeepEqual(args, []string{"start", "yeet-dns.service"}) {
				return errors.New("bind failed")
			}
			return nil
		},
	})

	err := installYeetDNSService("/srv/yeet")
	if err == nil || !strings.Contains(err.Error(), "failed to start yeet-dns service") {
		t.Fatalf("installYeetDNSService error = %v, want start error", err)
	}
}

type yeetDNSServiceFakes struct {
	catchBin      string
	executableErr error
	systemdPath   string
	unitActive    func(string) bool
	systemctl     func(...string) error
}

func withYeetDNSServiceFakes(t *testing.T, fakes yeetDNSServiceFakes) {
	t.Helper()
	prevExecutablePath := catchExecutablePath
	prevSystemdUnitPath := catchSystemdUnitPath
	prevSystemdUnitActive := catchSystemdUnitActive
	prevSystemctl := catchSystemctl
	catchExecutablePath = func() (string, error) {
		return fakes.catchBin, fakes.executableErr
	}
	catchSystemdUnitPath = func(unit string) string {
		if unit != "yeet-dns.service" {
			t.Fatalf("unexpected systemd unit path lookup: %s", unit)
		}
		return fakes.systemdPath
	}
	catchSystemdUnitActive = fakes.unitActive
	catchSystemctl = fakes.systemctl
	t.Cleanup(func() {
		catchExecutablePath = prevExecutablePath
		catchSystemdUnitPath = prevSystemdUnitPath
		catchSystemdUnitActive = prevSystemdUnitActive
		catchSystemctl = prevSystemctl
	})
}
