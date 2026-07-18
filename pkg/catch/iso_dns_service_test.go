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

func TestISODNSUnitIsDeterministicAndRunsLocalCommand(t *testing.T) {
	var rendered []string
	for range 2 {
		unit, err := newISODNSUnit("/usr/local/bin/catch", "/var/lib/yeet")
		if err != nil {
			t.Fatal(err)
		}
		files, err := unit.WriteOutUnitFiles(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(files[db.ArtifactSystemdUnit])
		if err != nil {
			t.Fatal(err)
		}
		rendered = append(rendered, string(raw))
	}
	if rendered[0] != rendered[1] {
		t.Fatalf("ISO DNS unit is nondeterministic:\nfirst:\n%s\nsecond:\n%s", rendered[0], rendered[1])
	}
	for _, want := range []string{
		"Description=yeet public-only ISO DNS\n",
		"ConditionFileIsExecutable=/usr/local/bin/catch\n",
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service\n",
		"PartOf=catch.service\n",
		"ExecStart=/usr/local/bin/catch -data-dir /var/lib/yeet iso-dns\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(rendered[0], want) {
			t.Fatalf("unit missing %q:\n%s", want, rendered[0])
		}
	}
}

func TestInstallISODNSServiceInstallsStartsAndReusesUnit(t *testing.T) {
	tests := []struct {
		name       string
		active     bool
		preinstall bool
		wantCalls  [][]string
	}{
		{
			name:      "new inactive unit",
			wantCalls: [][]string{{"daemon-reload"}, {"enable", "yeet-iso-dns.service"}, {"start", "yeet-iso-dns.service"}},
		},
		{
			name:      "changed active unit",
			active:    true,
			wantCalls: [][]string{{"daemon-reload"}, {"enable", "yeet-iso-dns.service"}, {"try-restart", "yeet-iso-dns.service"}},
		},
		{
			name:       "unchanged active unit",
			active:     true,
			preinstall: true,
			wantCalls:  [][]string{{"enable", "yeet-iso-dns.service"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-iso-dns.service")
			if tt.preinstall {
				generated, _, cleanup, err := renderISODNSServiceUnitWithPath(t, "/usr/local/bin/catch", systemdPath, "/srv/yeet")
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				if err := os.MkdirAll(filepath.Dir(systemdPath), 0o755); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(generated)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(systemdPath, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			var calls [][]string
			withISODNSServiceFakes(t, "/usr/local/bin/catch", nil, systemdPath, func(string) bool { return tt.active }, func(args ...string) error {
				calls = append(calls, append([]string(nil), args...))
				return nil
			})
			if err := installISODNSService("/srv/yeet"); err != nil {
				t.Fatalf("installISODNSService: %v", err)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("systemctl calls = %#v, want %#v", calls, tt.wantCalls)
			}
			raw, err := os.ReadFile(systemdPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "ExecStart=/usr/local/bin/catch -data-dir /srv/yeet iso-dns\n") {
				t.Fatalf("installed unit missing ISO DNS command:\n%s", raw)
			}
		})
	}
}

func TestInstallISODNSServicePropagatesLifecycleErrors(t *testing.T) {
	wantErr := errors.New("systemctl failed")
	for _, tt := range []struct {
		name      string
		failAt    string
		active    bool
		execErr   error
		wantError string
	}{
		{name: "resolve executable", execErr: wantErr, wantError: "resolve catch binary"},
		{name: "reload", failAt: "daemon-reload", wantError: "reload systemd"},
		{name: "enable", failAt: "enable", wantError: "enable ISO DNS"},
		{name: "start", failAt: "start", wantError: "start ISO DNS"},
		{name: "restart", failAt: "try-restart", active: true, wantError: "systemctl failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withISODNSServiceFakes(t, "/usr/local/bin/catch", tt.execErr, filepath.Join(t.TempDir(), "systemd", "yeet-iso-dns.service"), func(string) bool { return tt.active }, func(args ...string) error {
				if len(args) > 0 && args[0] == tt.failAt {
					return wantErr
				}
				return nil
			})
			err := installISODNSService("/srv/yeet")
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("installISODNSService error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestRenderISODNSServiceUnitCleanupAndNetworkGateValidation(t *testing.T) {
	systemdPath := filepath.Join(t.TempDir(), "systemd", "yeet-iso-dns.service")
	generated, destination, cleanup, err := renderISODNSServiceUnitWithPath(t, "/usr/local/bin/catch", systemdPath, "/srv/yeet")
	if err != nil {
		t.Fatal(err)
	}
	if destination != systemdPath {
		t.Fatalf("destination = %q, want %q", destination, systemdPath)
	}
	tmpDir := filepath.Dir(generated)
	cleanup()
	if _, err := os.Stat(tmpDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary unit directory still exists: %v", err)
	}

	unit, err := newISONetworkGateUnit(" /usr/local/bin/catch ", " /srv/yeet ", " app ")
	if err != nil {
		t.Fatal(err)
	}
	if unit.Name != "yeet-app-ns" || !reflect.DeepEqual(unit.Arguments, []string{"-data-dir", "/srv/yeet", "iso-network-ensure", "app"}) {
		t.Fatalf("network gate unit = %#v", unit)
	}
	for _, service := range []string{"", "bad name", "../bad", "bad\\name", "bad\nname"} {
		if _, err := newISONetworkGateUnit("/usr/local/bin/catch", "/srv/yeet", service); err == nil {
			t.Fatalf("newISONetworkGateUnit accepted %q", service)
		}
	}
}

func TestInstallISODNSServiceUnitRejectsBlockedDestination(t *testing.T) {
	generated := filepath.Join(t.TempDir(), "generated.service")
	if err := os.WriteFile(generated, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installISODNSServiceUnit(generated, filepath.Join(blocked, "unit.service")); err == nil {
		t.Fatal("installISODNSServiceUnit accepted a destination below a regular file")
	}
}

func renderISODNSServiceUnitWithPath(t *testing.T, catchBin, systemdPath, dataDir string) (string, string, func(), error) {
	t.Helper()
	prevExecutablePath := catchExecutablePath
	prevSystemdUnitPath := catchSystemdUnitPath
	catchExecutablePath = func() (string, error) { return catchBin, nil }
	catchSystemdUnitPath = func(string) string { return systemdPath }
	defer func() {
		catchExecutablePath = prevExecutablePath
		catchSystemdUnitPath = prevSystemdUnitPath
	}()
	return renderISODNSServiceUnit(dataDir)
}

func withISODNSServiceFakes(t *testing.T, catchBin string, executableErr error, systemdPath string, active func(string) bool, systemctl func(...string) error) {
	t.Helper()
	prevExecutablePath := catchExecutablePath
	prevSystemdUnitPath := catchSystemdUnitPath
	prevSystemdUnitActive := catchSystemdUnitActive
	prevSystemctl := catchSystemctl
	catchExecutablePath = func() (string, error) { return catchBin, executableErr }
	catchSystemdUnitPath = func(unit string) string {
		if unit != "yeet-iso-dns.service" {
			t.Fatalf("unexpected unit path lookup %q", unit)
		}
		return systemdPath
	}
	catchSystemdUnitActive = active
	catchSystemctl = systemctl
	t.Cleanup(func() {
		catchExecutablePath = prevExecutablePath
		catchSystemdUnitPath = prevSystemdUnitPath
		catchSystemdUnitActive = prevSystemdUnitActive
		catchSystemctl = prevSystemctl
	})
}

func TestISODNSUnitRejectsUnsafePaths(t *testing.T) {
	for _, tt := range []struct {
		name, catchBin, dataDir string
	}{
		{name: "empty binary", dataDir: "/var/lib/yeet"},
		{name: "relative binary", catchBin: "bin/catch", dataDir: "/var/lib/yeet"},
		{name: "binary whitespace", catchBin: "/usr/local/bin/catch bad", dataDir: "/var/lib/yeet"},
		{name: "empty data", catchBin: "/usr/local/bin/catch"},
		{name: "relative data", catchBin: "/usr/local/bin/catch", dataDir: "var/lib/yeet"},
		{name: "data traversal", catchBin: "/usr/local/bin/catch", dataDir: "/var/lib/../yeet"},
		{name: "root data", catchBin: "/usr/local/bin/catch", dataDir: string(filepath.Separator)},
		{name: "data newline", catchBin: "/usr/local/bin/catch", dataDir: "/var/lib/yeet\n--flag"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newISODNSUnit(tt.catchBin, tt.dataDir); err == nil {
				t.Fatalf("newISODNSUnit(%q, %q) returned nil error", tt.catchBin, tt.dataDir)
			}
		})
	}
}
