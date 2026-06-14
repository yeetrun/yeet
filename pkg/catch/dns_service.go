// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/svc"
)

var (
	catchExecutablePath  = os.Executable
	catchSystemdUnitPath = func(unit string) string {
		return filepath.Join("/etc/systemd/system", unit)
	}
	catchSystemdUnitActive = systemdUnitIsActive
	catchSystemctl         = runCatchSystemctl
)

func newYeetDNSUnit(catchBin, dataDir string) *svc.SystemdUnit {
	return &svc.SystemdUnit{
		Name:       "yeet-dns",
		Executable: catchBin,
		Arguments:  []string{"-data-dir", dataDir, "dns"},
		Requires:   "yeet-ns.service",
		After:      "yeet-ns.service",
		WantedBy:   "multi-user.target",
	}
}

func installYeetDNSService(dataDir string) error {
	catchBin, err := catchExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to resolve catch binary path: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "yeet-dns-unit-*")
	if err != nil {
		return fmt.Errorf("failed to create yeet-dns unit tempdir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			log.Printf("failed to remove yeet-dns unit tempdir: %v", err)
		}
	}()
	unitFiles, err := newYeetDNSUnit(catchBin, dataDir).WriteOutUnitFiles(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to write yeet-dns unit: %w", err)
	}
	changed, err := yeetDNSUnitChanged(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		return err
	}
	alreadyActive := catchSystemdUnitActive("yeet-dns.service")
	if !changed && alreadyActive {
		return nil
	}
	return installGeneratedYeetDNSService(unitFiles[db.ArtifactSystemdUnit], changed, alreadyActive)
}

func yeetDNSUnitChanged(generatedUnit string) (bool, error) {
	same, err := fileutil.Identical(catchSystemdUnitPath("yeet-dns.service"), generatedUnit)
	if err != nil {
		return false, fmt.Errorf("failed to compare yeet-dns unit: %w", err)
	}
	return !same, nil
}

func installGeneratedYeetDNSService(generatedUnit string, changed bool, alreadyActive bool) error {
	dst := catchSystemdUnitPath("yeet-dns.service")
	if changed {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("failed to create yeet-dns systemd dir: %w", err)
		}
		if err := fileutil.CopyFile(generatedUnit, dst); err != nil {
			return fmt.Errorf("failed to install yeet-dns unit: %w", err)
		}
		if err := catchSystemctl("daemon-reload"); err != nil {
			return fmt.Errorf("failed to reload systemd for yeet-dns: %w", err)
		}
	}
	if err := catchSystemctl("enable", "yeet-dns.service"); err != nil {
		return fmt.Errorf("failed to enable yeet-dns service: %w", err)
	}
	if alreadyActive {
		log.Printf("installed updated yeet-dns.service; leaving active DNS server running")
		return nil
	}
	if err := catchSystemctl("start", "yeet-dns.service"); err != nil {
		return fmt.Errorf("failed to start yeet-dns service: %w", err)
	}
	return nil
}

func systemdUnitIsActive(unit string) bool {
	return catchSystemctl("is-active", "--quiet", unit) == nil
}

func runCatchSystemctl(args ...string) error {
	output, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, string(output))
	}
	return nil
}
