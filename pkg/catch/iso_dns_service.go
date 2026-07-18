// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/svc"
)

func newISODNSUnit(catchBin, dataDir string) (*svc.SystemdUnit, error) {
	catchBin = strings.TrimSpace(catchBin)
	dataDir = strings.TrimSpace(dataDir)
	if err := validateISODNSUnitPath("catch binary", catchBin, false); err != nil {
		return nil, err
	}
	if err := validateISODNSUnitPath("data directory", dataDir, true); err != nil {
		return nil, err
	}
	return &svc.SystemdUnit{
		Name:        "yeet-iso-dns",
		Description: "yeet public-only ISO DNS",
		Executable:  catchBin,
		Arguments:   []string{"-data-dir", dataDir, "iso-dns"},
		Requires:    "yeet-ns.service",
		After:       "yeet-ns.service",
		PartOf:      "catch.service",
		WantedBy:    "multi-user.target",
	}, nil
}

func newISONetworkGateUnit(catchBin, dataDir, service string) (*svc.SystemdUnit, error) {
	catchBin = strings.TrimSpace(catchBin)
	dataDir = strings.TrimSpace(dataDir)
	service = strings.TrimSpace(service)
	if err := validateISODNSUnitPath("catch binary", catchBin, false); err != nil {
		return nil, err
	}
	if err := validateISODNSUnitPath("data directory", dataDir, true); err != nil {
		return nil, err
	}
	if service == "" || strings.ContainsAny(service, " /\\\t\r\n") {
		return nil, fmt.Errorf("ISO network gate requires a safe service name")
	}
	return &svc.SystemdUnit{
		Name:        "yeet-" + service + "-ns",
		Description: "yeet ISO network gate for " + service,
		Executable:  catchBin,
		Arguments:   []string{"-data-dir", dataDir, "iso-network-ensure", service},
		Requires:    "yeet-ns.service yeet-iso-dns.service",
		After:       "yeet-ns.service yeet-iso-dns.service",
		OneShot:     true,
	}, nil
}

func installISODNSService(dataDir string) error {
	generated, destination, cleanup, err := renderISODNSServiceUnit(dataDir)
	if err != nil {
		return err
	}
	defer cleanup()
	changed, err := installISODNSServiceUnit(generated, destination)
	if err != nil {
		return err
	}
	return activateISODNSServiceUnit(changed)
}

func renderISODNSServiceUnit(dataDir string) (string, string, func(), error) {
	catchBin, err := catchExecutablePath()
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve catch binary for ISO DNS: %w", err)
	}
	unit, err := newISODNSUnit(catchBin, dataDir)
	if err != nil {
		return "", "", nil, err
	}
	tmpDir, err := os.MkdirTemp("", "yeet-iso-dns-unit-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("create ISO DNS unit tempdir: %w", err)
	}
	cleanup := func() {
		cleanupISODNSUnitTempDir(tmpDir)
	}
	unitFiles, err := unit.WriteOutUnitFiles(tmpDir)
	if err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("write ISO DNS unit: %w", err)
	}
	return unitFiles[db.ArtifactSystemdUnit], catchSystemdUnitPath("yeet-iso-dns.service"), cleanup, nil
}

func cleanupISODNSUnitTempDir(tmpDir string) {
	if err := os.RemoveAll(tmpDir); err != nil {
		log.Printf("failed to remove ISO DNS unit tempdir: %v", err)
	}
}

func installISODNSServiceUnit(generated, destination string) (bool, error) {
	same, err := fileutil.Identical(destination, generated)
	if err != nil {
		return false, fmt.Errorf("compare ISO DNS unit: %w", err)
	}
	if same {
		return false, nil
	}
	if err := copyISODNSServiceUnit(generated, destination); err != nil {
		return false, err
	}
	return true, nil
}

func copyISODNSServiceUnit(generated, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := fileutil.CopyFile(generated, destination); err != nil {
		return fmt.Errorf("install ISO DNS unit: %w", err)
	}
	if err := catchSystemctl("daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd for ISO DNS: %w", err)
	}
	return nil
}

func activateISODNSServiceUnit(changed bool) error {
	if err := catchSystemctl("enable", "yeet-iso-dns.service"); err != nil {
		return fmt.Errorf("enable ISO DNS service: %w", err)
	}
	if catchSystemdUnitActive("yeet-iso-dns.service") {
		if changed {
			return catchSystemctl("try-restart", "yeet-iso-dns.service")
		}
		return nil
	}
	if err := catchSystemctl("start", "yeet-iso-dns.service"); err != nil {
		return fmt.Errorf("start ISO DNS service: %w", err)
	}
	return nil
}

func validateISODNSUnitPath(label, path string, rejectRoot bool) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, " \t\r\n") {
		return fmt.Errorf("ISO DNS unit requires a safe absolute %s path, got %q", label, path)
	}
	if rejectRoot && path == string(filepath.Separator) {
		return fmt.Errorf("ISO DNS unit %s cannot be filesystem root", label)
	}
	return nil
}
