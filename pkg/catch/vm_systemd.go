// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmSystemdConfig struct {
	Service              string
	Runner               string
	DataDir              string
	ServicesRoot         string
	ServiceRoot          string
	DiskPath             string
	Firecracker          string
	Jailer               string
	RuntimeDescriptor    string
	RuntimeRunningMarker string
	RuntimeTrialResult   string
	JailerBase           string
	ConfigPath           string
	APISocket            string
	ConsoleSocket        string
	VsockSocket          string
	WorkingDirectory     string
}

func renderVMSystemdUnit(cfg vmSystemdConfig) (string, error) {
	required := []struct{ label, value string }{
		{"service", cfg.Service}, {"runner", cfg.Runner}, {"services root", cfg.ServicesRoot},
		{"service root", cfg.ServiceRoot},
		{"disk", cfg.DiskPath},
		{"jailer base", cfg.JailerBase}, {"config", cfg.ConfigPath},
		{"API socket", cfg.APISocket}, {"console socket", cfg.ConsoleSocket},
	}
	for _, input := range required {
		if strings.TrimSpace(input.value) == "" {
			return "", fmt.Errorf("VM systemd %s is required", input.label)
		}
	}
	launchArgs, err := vmSystemdRuntimeLaunchArgs(cfg)
	if err != nil {
		return "", err
	}
	return renderVMSystemdUnitText(cfg, launchArgs), nil
}

func vmSystemdRuntimeLaunchArgs(cfg vmSystemdConfig) ([]string, error) {
	args := []string{
		"vm-run", "--service", cfg.Service, "--service-root", cfg.ServiceRoot,
		"--disk-path", cfg.DiskPath,
	}
	explicit := []string{cfg.Firecracker, cfg.Jailer}
	descriptor := []string{cfg.RuntimeDescriptor, cfg.RuntimeRunningMarker, cfg.RuntimeTrialResult}
	explicitCount := nonEmptyVMSystemdValues(explicit)
	descriptorCount := nonEmptyVMSystemdValues(descriptor)
	switch {
	case explicitCount == len(explicit) && descriptorCount == 0:
		args = append(args, "--firecracker", cfg.Firecracker, "--jailer", cfg.Jailer)
	case explicitCount == 0 && descriptorCount == len(descriptor):
		if err := ValidateVMRuntimeLaunchPaths(cfg.ServiceRoot, cfg.RuntimeDescriptor, cfg.RuntimeRunningMarker, cfg.RuntimeTrialResult); err != nil {
			return nil, fmt.Errorf("VM systemd runtime mode: %w", err)
		}
		args = append(args,
			"--runtime-descriptor", cfg.RuntimeDescriptor,
			"--runtime-running-marker", cfg.RuntimeRunningMarker,
			"--runtime-trial-result", cfg.RuntimeTrialResult,
		)
	default:
		return nil, fmt.Errorf("VM systemd runtime mode requires both firecracker and jailer paths or all runtime descriptor paths")
	}
	args = append(args,
		"--jailer-base", cfg.JailerBase,
		"--api-sock", cfg.APISocket, "--config-file", cfg.ConfigPath,
		"--console-sock", cfg.ConsoleSocket,
	)
	return args, nil
}

func nonEmptyVMSystemdValues(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func renderVMSystemdUnitText(cfg vmSystemdConfig, launchArgs []string) string {
	cleanupSockets := []string{cfg.APISocket, cfg.ConsoleSocket}
	if strings.TrimSpace(cfg.VsockSocket) != "" {
		cleanupSockets = append(cleanupSockets, cfg.VsockSocket)
	}
	cleanupArgs := append([]string{"/bin/rm", "-f"}, cleanupSockets...)
	networkArgs := []string{cfg.Runner, "-data-dir", cfg.DataDir, "-services-root", cfg.ServicesRoot, "vm-network-ensure", cfg.Service}
	startArgs := append([]string{cfg.Runner, "-data-dir", cfg.DataDir, "-services-root", cfg.ServicesRoot}, launchArgs...)
	return fmt.Sprintf(`[Unit]
Description=yeet VM %s
After=network-online.target yeet-ns.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStartPre=%s
ExecStartPre=%s
ExecStart=%s
Restart=on-failure
RestartForceExitStatus=75
RestartPreventExitStatus=76
RestartSec=1
KillMode=mixed
TimeoutStopSec=10

[Install]
WantedBy=multi-user.target
`, cfg.Service, systemdVMWorkingDirectory(cfg.WorkingDirectory),
		strings.Join(systemdVMExecArguments(cleanupArgs), " "),
		strings.Join(systemdVMExecArguments(networkArgs), " "),
		strings.Join(systemdVMExecArguments(startArgs), " "))
}

func systemdVMExecArguments(values []string) []string {
	escaped := make([]string, len(values))
	for i, value := range values {
		escaped[i] = systemdVMExecArgument(value)
	}
	return escaped
}

func systemdVMExecArgument(value string) string {
	return systemdVMUnitValue(value, true)
}

func systemdVMWorkingDirectory(value string) string {
	return systemdVMUnitValue(value, false)
}

func systemdVMUnitValue(value string, escapeDollar bool) string {
	needsQuotes := value == "" || strings.ContainsAny(value, " \a\b\t\r\n\v\f\"'\\")
	var escaped strings.Builder
	for _, char := range value {
		writeSystemdVMUnitRune(&escaped, char, escapeDollar)
	}
	if needsQuotes {
		return `"` + escaped.String() + `"`
	}
	return escaped.String()
}

func writeSystemdVMUnitRune(escaped *strings.Builder, char rune, escapeDollar bool) {
	if char == '$' {
		if escapeDollar {
			escaped.WriteString("$$")
		} else {
			escaped.WriteRune(char)
		}
		return
	}
	replacements := map[rune]string{
		'%': "%%", '\\': `\\`, '"': `\"`, '\a': `\a`, '\b': `\b`, '\n': `\n`,
		'\r': `\r`, '\t': `\t`, '\v': `\v`, '\f': `\f`,
	}
	if replacement, ok := replacements[char]; ok {
		escaped.WriteString(replacement)
		return
	}
	escaped.WriteRune(char)
}

func regenerateHostStorageVMSystemdUnit(ctx context.Context, cfg Config, service *db.Service, runner string) ([]string, error) {
	return regenerateHostStorageVMSystemdUnitWithDeps(ctx, cfg, service, runner, defaultVMRuntimeDescriptorFileDeps())
}

func regenerateHostStorageVMSystemdUnitWithDeps(ctx context.Context, cfg Config, service *db.Service, runner string, descriptorDeps vmRuntimeDescriptorFileDeps) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if service == nil || service.VM == nil {
		return nil, nil
	}
	if service.VM.Components != nil {
		return regenerateAdoptedVMSystemdUnit(cfg, service, runner, descriptorDeps)
	}
	root := serviceRootFromConfig(cfg, *service)
	runDir := serviceRunDirForRoot(root)
	dataDir := serviceDataDirForRoot(root)
	rootFS := service.VM.Image.RootFS
	diskPath := service.VM.Disk.Path
	if strings.TrimSpace(diskPath) == "" {
		diskPath = rootFS
	}
	jailer := filepath.Join(filepath.Dir(rootFS), "jailer")
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service:          service.Name,
		Runner:           runner,
		DataDir:          cfg.RootDir,
		ServicesRoot:     cfg.ServicesRoot,
		ServiceRoot:      root,
		DiskPath:         diskPath,
		Firecracker:      filepath.Join(filepath.Dir(rootFS), "firecracker"),
		Jailer:           jailer,
		JailerBase:       vmJailerBaseForDataRoot(cfg.RootDir),
		ConfigPath:       filepath.Join(runDir, "firecracker.json"),
		APISocket:        filepath.Join(runDir, "firecracker.sock"),
		ConsoleSocket:    filepath.Join(runDir, "serial.sock"),
		VsockSocket:      filepath.Join(runDir, "vsock.sock"),
		WorkingDirectory: dataDir,
	})
	if err != nil {
		return nil, err
	}
	unitPath := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service.Name))
	if err := writeVMSystemdUnitAtomic(unitPath, []byte(unit), 0o644); err != nil {
		return nil, fmt.Errorf("write VM systemd unit %s: %w", unitPath, err)
	}
	return []string{filepath.Base(unitPath)}, nil
}

func regenerateAdoptedVMSystemdUnit(cfg Config, service *db.Service, runner string, descriptorDeps vmRuntimeDescriptorFileDeps) ([]string, error) {
	if err := reconcileVMRuntimeDescriptor(service, cfg, descriptorDeps); err != nil {
		return nil, fmt.Errorf("regenerate VM runtime descriptor for %s: %w", service.Name, err)
	}
	spec, err := renderVMRuntimeUnitSpec(cfg, service, runner)
	if err != nil {
		return nil, err
	}
	if err := writeVMSystemdUnitAtomic(spec.Path, spec.Content, 0o644); err != nil {
		return nil, fmt.Errorf("write VM systemd unit %s: %w", spec.Path, err)
	}
	return []string{filepath.Base(spec.Path)}, nil
}

func writeVMSystemdUnitAtomic(path string, contents []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
