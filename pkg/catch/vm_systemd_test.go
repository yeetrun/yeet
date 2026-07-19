// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRenderVMSystemdUnit(t *testing.T) {
	cfg := vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		DataDir:          "/srv/catch/data",
		ServicesRoot:     "/srv/services",
		ServiceRoot:      "/srv/vms/devbox",
		DiskPath:         "/srv/vms/devbox/rootfs.ext4",
		Firecracker:      "/srv/images/firecracker",
		Jailer:           "/opt/vm/jailer",
		JailerBase:       "/var/lib/yeet/vm-jailer",
		ConfigPath:       "/srv/vms/devbox/run/firecracker.json",
		APISocket:        "/srv/vms/devbox/run/firecracker.sock",
		ConsoleSocket:    "/srv/vms/devbox/run/serial.sock",
		VsockSocket:      "/srv/vms/devbox/run/vsock.sock",
		WorkingDirectory: "/srv/vms/devbox",
	}
	unit, err := renderVMSystemdUnit(cfg)
	if err != nil {
		t.Fatalf("renderVMSystemdUnit: %v", err)
	}
	for _, want := range []string{
		"[Unit]",
		"Description=yeet VM devbox",
		"ExecStartPre=/bin/rm -f /srv/vms/devbox/run/firecracker.sock /srv/vms/devbox/run/serial.sock /srv/vms/devbox/run/vsock.sock",
		"ExecStartPre=/srv/catch/run/catch -data-dir /srv/catch/data -services-root /srv/services vm-network-ensure devbox",
		"ExecStart=/srv/catch/run/catch -data-dir /srv/catch/data -services-root /srv/services vm-run --service devbox --service-root /srv/vms/devbox --disk-path /srv/vms/devbox/rootfs.ext4 --firecracker /srv/images/firecracker --jailer /opt/vm/jailer --jailer-base /var/lib/yeet/vm-jailer --api-sock /srv/vms/devbox/run/firecracker.sock --config-file /srv/vms/devbox/run/firecracker.json --console-sock /srv/vms/devbox/run/serial.sock",
		"Restart=on-failure",
		"RestartForceExitStatus=75",
		"RestartPreventExitStatus=76",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "vm-restore") || strings.Contains(unit, "memory-snapshot") {
		t.Fatalf("unit contains retired full-memory restore handling:\n%s", unit)
	}
	for _, want := range []string{
		"--jailer /opt/vm/jailer",
		"--jailer-base /var/lib/yeet/vm-jailer",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	assertTextOrder(t, unit,
		"ExecStartPre=/bin/rm -f /srv/vms/devbox/run/firecracker.sock /srv/vms/devbox/run/serial.sock /srv/vms/devbox/run/vsock.sock",
		"ExecStartPre=/srv/catch/run/catch -data-dir /srv/catch/data -services-root /srv/services vm-network-ensure devbox",
		"ExecStart=/srv/catch/run/catch -data-dir /srv/catch/data -services-root /srv/services vm-run --service devbox --service-root /srv/vms/devbox --disk-path /srv/vms/devbox/rootfs.ext4 --firecracker /srv/images/firecracker --jailer /opt/vm/jailer --jailer-base /var/lib/yeet/vm-jailer --api-sock /srv/vms/devbox/run/firecracker.sock --config-file /srv/vms/devbox/run/firecracker.json --console-sock /srv/vms/devbox/run/serial.sock",
	)
}

func TestRenderVMSystemdUnitPreservesOrdinaryLegacyOutput(t *testing.T) {
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service: "devbox", Runner: "/run/catch", DataDir: "/var/lib/yeet", ServicesRoot: "/srv/services",
		ServiceRoot: "/srv/devbox", DiskPath: "/srv/devbox/data/rootfs.raw",
		Firecracker: "/opt/firecracker", Jailer: "/opt/jailer", JailerBase: "/var/lib/yeet/vm-jailer",
		ConfigPath: "/srv/devbox/run/firecracker.json", APISocket: "/srv/devbox/run/firecracker.sock",
		ConsoleSocket: "/srv/devbox/run/serial.sock", WorkingDirectory: "/srv/devbox/data",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `[Unit]
Description=yeet VM devbox
After=network-online.target yeet-ns.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/srv/devbox/data
ExecStartPre=/bin/rm -f /srv/devbox/run/firecracker.sock /srv/devbox/run/serial.sock
ExecStartPre=/run/catch -data-dir /var/lib/yeet -services-root /srv/services vm-network-ensure devbox
ExecStart=/run/catch -data-dir /var/lib/yeet -services-root /srv/services vm-run --service devbox --service-root /srv/devbox --disk-path /srv/devbox/data/rootfs.raw --firecracker /opt/firecracker --jailer /opt/jailer --jailer-base /var/lib/yeet/vm-jailer --api-sock /srv/devbox/run/firecracker.sock --config-file /srv/devbox/run/firecracker.json --console-sock /srv/devbox/run/serial.sock
Restart=on-failure
RestartForceExitStatus=75
RestartPreventExitStatus=76
RestartSec=1
KillMode=mixed
TimeoutStopSec=10

[Install]
WantedBy=multi-user.target
`
	if unit != want {
		t.Fatalf("ordinary legacy unit changed:\n--- got ---\n%s--- want ---\n%s", unit, want)
	}
}

func TestSystemdVMExecArgumentEscapesNativeSyntax(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{input: "/simple/path", want: "/simple/path"},
		{input: "/path with space", want: `"/path with space"`},
		{input: "/percent%path", want: "/percent%%path"},
		{input: "/dollar$path", want: "/dollar$$path"},
		{input: `/quote"path`, want: `"/quote\"path"`},
		{input: `/slash\path`, want: `"/slash\\path"`},
		{input: "/line\nbreak", want: `"/line\nbreak"`},
		{input: "/alarm\abell", want: `"/alarm\abell"`},
		{input: "/back\bspace", want: `"/back\bspace"`},
	} {
		t.Run(tt.input, func(t *testing.T) {
			if got := systemdVMExecArgument(tt.input); got != tt.want {
				t.Fatalf("systemdVMExecArgument(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSystemdVMWorkingDirectoryEscapesNativeSyntaxWithoutExecExpansion(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{input: "/simple/$path", want: "/simple/$path"},
		{input: "/percent%path", want: "/percent%%path"},
		{input: "/alarm\abell", want: `"/alarm\abell"`},
		{input: "/back\bspace", want: `"/back\bspace"`},
	} {
		t.Run(tt.input, func(t *testing.T) {
			if got := systemdVMWorkingDirectory(tt.input); got != tt.want {
				t.Fatalf("systemdVMWorkingDirectory(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRenderVMSystemdUnitEscapesCustomLegacyRoots(t *testing.T) {
	root := "/srv/vm root/$cash/%quote\"slash\\line\nnext/devbox"
	cfg := vmSystemdConfig{
		Service: "devbox", Runner: filepath.Join(root, "run", "catch"), DataDir: filepath.Join(root, "catch data"),
		ServicesRoot: filepath.Join(root, "service roots"), ServiceRoot: root, DiskPath: filepath.Join(root, "data", "rootfs.raw"),
		Firecracker: filepath.Join(root, "runtime", "firecracker"), Jailer: filepath.Join(root, "runtime", "jailer"),
		JailerBase: filepath.Join(root, "jailer base"), ConfigPath: filepath.Join(root, "run", "firecracker.json"),
		APISocket: filepath.Join(root, "run", "firecracker.sock"), ConsoleSocket: filepath.Join(root, "run", "serial.sock"),
		VsockSocket: filepath.Join(root, "run", "vsock.sock"), WorkingDirectory: filepath.Join(root, "data"),
	}
	unit, err := renderVMSystemdUnit(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"WorkingDirectory=" + systemdVMWorkingDirectory(cfg.WorkingDirectory),
		"ExecStartPre=/bin/rm -f " + systemdVMExecArgument(cfg.APISocket) + " " + systemdVMExecArgument(cfg.ConsoleSocket) + " " + systemdVMExecArgument(cfg.VsockSocket),
		"ExecStartPre=" + strings.Join(systemdVMExecArguments([]string{cfg.Runner, "-data-dir", cfg.DataDir, "-services-root", cfg.ServicesRoot, "vm-network-ensure", cfg.Service}), " "),
		"ExecStart=" + strings.Join(systemdVMExecArguments([]string{cfg.Runner, "-data-dir", cfg.DataDir, "-services-root", cfg.ServicesRoot, "vm-run", "--service", cfg.Service, "--service-root", cfg.ServiceRoot}), " "),
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("escaped unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "\nnext/devbox") {
		t.Fatalf("unit contains an unescaped path newline:\n%s", unit)
	}
}

func TestRenderVMSystemdUnitRequiresJailer(t *testing.T) {
	_, err := renderVMSystemdUnit(vmSystemdConfig{
		Service: "devbox", Runner: "/run/catch", DataDir: "/var/lib/yeet",
		ServicesRoot: "/srv/services", ServiceRoot: "/srv/devbox", DiskPath: "/srv/devbox/data/rootfs.raw",
		Firecracker: "/opt/vm/firecracker", JailerBase: "/var/lib/yeet/vm-jailer",
		ConfigPath: "/srv/devbox/run/firecracker.json", APISocket: "/srv/devbox/run/firecracker.sock",
		ConsoleSocket: "/srv/devbox/run/serial.sock", WorkingDirectory: "/srv/devbox/data",
	})
	if err == nil || !strings.Contains(err.Error(), "jailer") {
		t.Fatalf("error = %v", err)
	}
}

func TestRenderVMSystemdUnitUsesJailerWhenConfigured(t *testing.T) {
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		DataDir:          "/srv/catch/data",
		ServicesRoot:     "/srv/services",
		ServiceRoot:      "/srv/vms/devbox",
		DiskPath:         "/srv/vms/devbox/data/rootfs.raw",
		Firecracker:      "/srv/images/firecracker",
		Jailer:           "/srv/images/jailer",
		JailerBase:       "/run/yeet/vm-jailer",
		ConfigPath:       "/srv/vms/devbox/run/firecracker.json",
		APISocket:        "/srv/vms/devbox/run/firecracker.sock",
		ConsoleSocket:    "/srv/vms/devbox/run/serial.sock",
		VsockSocket:      "/srv/vms/devbox/run/vsock.sock",
		WorkingDirectory: "/srv/vms/devbox",
	})
	if err != nil {
		t.Fatalf("renderVMSystemdUnit: %v", err)
	}
	want := "--firecracker /srv/images/firecracker --jailer /srv/images/jailer --jailer-base /run/yeet/vm-jailer --api-sock"
	if !strings.Contains(unit, want) {
		t.Fatalf("jailed unit missing %q:\n%s", want, unit)
	}
}

func TestRenderVMSystemdUnitUsesRuntimeDescriptorMode(t *testing.T) {
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service:              "devbox",
		Runner:               "/srv/catch/run/catch",
		DataDir:              "/srv/catch/data",
		ServicesRoot:         "/srv/services",
		ServiceRoot:          "/srv/vms/devbox",
		DiskPath:             "/srv/vms/devbox/data/rootfs.raw",
		RuntimeDescriptor:    "/srv/vms/devbox/data/vmm-runtime.json",
		RuntimeRunningMarker: "/srv/vms/devbox/run/vmm-runtime-running.json",
		RuntimeTrialResult:   "/srv/vms/devbox/run/vmm-runtime-trial-result.json",
		JailerBase:           "/run/yeet/vm-jailer",
		ConfigPath:           "/srv/vms/devbox/run/firecracker.json",
		APISocket:            "/srv/vms/devbox/run/firecracker.sock",
		ConsoleSocket:        "/srv/vms/devbox/run/serial.sock",
		VsockSocket:          "/srv/vms/devbox/run/vsock.sock",
		WorkingDirectory:     "/srv/vms/devbox",
	})
	if err != nil {
		t.Fatalf("renderVMSystemdUnit: %v", err)
	}
	for _, want := range []string{
		"--runtime-descriptor /srv/vms/devbox/data/vmm-runtime.json",
		"--runtime-running-marker /srv/vms/devbox/run/vmm-runtime-running.json",
		"--runtime-trial-result /srv/vms/devbox/run/vmm-runtime-trial-result.json",
		"--jailer-base /run/yeet/vm-jailer",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("descriptor unit missing %q:\n%s", want, unit)
		}
	}
	for _, forbidden := range []string{"--firecracker", "--jailer /"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("descriptor unit contains %q:\n%s", forbidden, unit)
		}
	}
}

func TestRenderVMSystemdUnitEscapesCustomDescriptorRoots(t *testing.T) {
	root := "/srv/vm root/$cash/%quoted/devbox"
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service: "devbox", Runner: "/run/catch", DataDir: "/var/lib/yeet", ServicesRoot: "/srv/services",
		ServiceRoot: root, DiskPath: filepath.Join(root, "data", "rootfs.raw"),
		RuntimeDescriptor:    filepath.Join(root, "data", "vmm-runtime.json"),
		RuntimeRunningMarker: filepath.Join(root, "run", "vmm-runtime-running.json"),
		RuntimeTrialResult:   filepath.Join(root, "run", "vmm-runtime-trial-result.json"),
		JailerBase:           "/run/yeet/vm-jailer", ConfigPath: filepath.Join(root, "run", "firecracker.json"),
		APISocket: filepath.Join(root, "run", "firecracker.sock"), ConsoleSocket: filepath.Join(root, "run", "serial.sock"),
		WorkingDirectory: filepath.Join(root, "data"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{root, filepath.Join(root, "data", "vmm-runtime.json"), filepath.Join(root, "run", "vmm-runtime-running.json"), filepath.Join(root, "run", "vmm-runtime-trial-result.json")} {
		if !strings.Contains(unit, systemdVMExecArgument(value)) {
			t.Fatalf("descriptor unit does not contain escaped %q:\n%s", value, unit)
		}
	}
	if !strings.Contains(unit, "WorkingDirectory="+systemdVMWorkingDirectory(filepath.Join(root, "data"))) {
		t.Fatalf("descriptor unit does not contain escaped working directory:\n%s", unit)
	}
}

func TestRenderVMSystemdUnitRejectsMixedOrPartialRuntimeMode(t *testing.T) {
	base := vmSystemdConfig{
		Service: "devbox", Runner: "/run/catch", DataDir: "/var/lib/yeet",
		ServicesRoot: "/srv/services", ServiceRoot: "/srv/devbox", DiskPath: "/srv/devbox/data/rootfs.raw",
		JailerBase: "/var/lib/yeet/vm-jailer", ConfigPath: "/srv/devbox/run/firecracker.json",
		APISocket: "/srv/devbox/run/firecracker.sock", ConsoleSocket: "/srv/devbox/run/serial.sock",
		WorkingDirectory: "/srv/devbox/data",
	}
	for _, tt := range []struct {
		name      string
		configure func(*vmSystemdConfig)
	}{
		{name: "partial descriptor", configure: func(cfg *vmSystemdConfig) { cfg.RuntimeDescriptor = "/srv/devbox/data/vmm-runtime.json" }},
		{name: "mixed", configure: func(cfg *vmSystemdConfig) {
			cfg.RuntimeDescriptor = "/srv/devbox/data/vmm-runtime.json"
			cfg.RuntimeRunningMarker = "/srv/devbox/run/vmm-runtime-running.json"
			cfg.RuntimeTrialResult = "/srv/devbox/run/vmm-runtime-trial-result.json"
			cfg.Firecracker = "/opt/firecracker"
			cfg.Jailer = "/opt/jailer"
		}},
		{name: "wrong descriptor path", configure: func(cfg *vmSystemdConfig) {
			cfg.RuntimeDescriptor = "/srv/devbox/data/other.json"
			cfg.RuntimeRunningMarker = "/srv/devbox/run/vmm-runtime-running.json"
			cfg.RuntimeTrialResult = "/srv/devbox/run/vmm-runtime-trial-result.json"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.configure(&cfg)
			if _, err := renderVMSystemdUnit(cfg); err == nil || !strings.Contains(err.Error(), "runtime mode") {
				t.Fatalf("renderVMSystemdUnit error = %v, want runtime mode", err)
			}
		})
	}
}

func TestRegenerateHostStorageVMSystemdUnitUsesCurrentRoots(t *testing.T) {
	systemdDir := t.TempDir()
	oldDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldDir })
	oldSystemctl := vmProvisionSystemctlFunc
	var calls [][]string
	vmProvisionSystemctlFunc = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { vmProvisionSystemctlFunc = oldSystemctl })

	serviceRoot := filepath.Join(t.TempDir(), "services", "devbox")
	service := &db.Service{
		Name:        "devbox",
		ServiceType: db.ServiceTypeVM,
		ServiceRoot: serviceRoot,
		VM: &db.VMConfig{
			Image: db.VMImageConfig{
				RootFS: "/flash/yeet/data/vm-images/ubuntu/rootfs.ext4",
			},
			Disk: db.VMDiskConfig{
				Path: filepath.Join(serviceRoot, "data", "rootfs.raw"),
			},
		},
	}
	cfg := Config{RootDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"}
	if err := os.MkdirAll(serviceRunDirForRoot(service.ServiceRoot), 0o755); err != nil {
		t.Fatalf("create VM run directory: %v", err)
	}
	if err := os.WriteFile(vmJailerReadinessMarkerPath(service.ServiceRoot), []byte("dynamic\n"), 0o600); err != nil {
		t.Fatalf("write invalid VM jailer readiness marker: %v", err)
	}

	units, err := regenerateHostStorageVMSystemdUnit(context.Background(), cfg, service, "/flash/yeet/services/catch/run/catch")
	if err != nil {
		t.Fatalf("regenerateHostStorageVMSystemdUnit error: %v", err)
	}
	if !slices.Equal(units, []string{vmSystemdUnitName("devbox")}) {
		t.Fatalf("regenerateHostStorageVMSystemdUnit units = %#v, want VM unit", units)
	}
	raw, err := os.ReadFile(filepath.Join(systemdDir, vmSystemdUnitName("devbox")))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	for _, want := range []string{
		"/flash/yeet/data",
		"-services-root /flash/yeet/services",
		"/flash/yeet/services/catch/run/catch",
		serviceRoot,
		filepath.Join(serviceRoot, "data", "rootfs.raw"),
		"/flash/yeet/data/vm-images/ubuntu/firecracker",
		"--jailer /flash/yeet/data/vm-images/ubuntu/jailer",
		"--jailer-base /flash/yeet/data/vm-jailer",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %s:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "/root/data") {
		t.Fatalf("unit contains old root:\n%s", unit)
	}
	if len(calls) != 0 {
		t.Fatalf("systemctl calls = %#v, want none before batched reload", calls)
	}
}

func assertTextOrder(t *testing.T, text string, wants ...string) {
	t.Helper()
	offset := 0
	for _, want := range wants {
		idx := strings.Index(text[offset:], want)
		if idx < 0 {
			t.Fatalf("text missing %q after offset %d:\n%s", want, offset, text)
		}
		offset += idx + len(want)
	}
}
