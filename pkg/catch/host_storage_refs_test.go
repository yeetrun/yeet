// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestHostStorageLegacyDefaultUsesRecordedInstallHomeOnly(t *testing.T) {
	if got, want := legacyDefaultDataDir("/home/ubuntu/../ubuntu"), "/home/ubuntu/yeet-data"; got != want {
		t.Fatalf("legacyDefaultDataDir = %q, want %q", got, want)
	}
	for _, current := range []catchrpc.HostStorageState{
		{DataDir: "/root/data"},
		{DataDir: "/srv/yeet"},
		{DataDir: "/root/yeet-data", DataDirZFS: true},
	} {
		if isExactLegacyDefault(current, "/root") {
			t.Fatalf("isExactLegacyDefault(%#v, /root) = true", current)
		}
	}
	if !isExactLegacyDefault(catchrpc.HostStorageState{DataDir: "/root/yeet-data/"}, "/root") {
		t.Fatalf("exact recorded legacy default was not detected")
	}
}

func TestHostStorageLegacyRepairCandidatesRetainRootDataWithoutCleanupAuthority(t *testing.T) {
	dirs := hostStorageLegacyRepairDataDirs(Config{InstallUser: "ubuntu", InstallHome: "/recorded/home"})
	for _, want := range []string{
		"/root/data",
		"/root/yeet-data",
		"/recorded/home/data",
		"/recorded/home/yeet-data",
	} {
		if !slices.Contains(dirs, want) {
			t.Fatalf("repair dirs = %#v, want %q", dirs, want)
		}
	}
}

func TestHostStorageNestedMountInfoParsesEscapedMountpoints(t *testing.T) {
	mountInfo := bytes.NewBufferString(strings.Join([]string{
		"35 24 0:31 / / rw,relatime - ext4 /dev/root rw",
		`41 35 0:42 / /root/yeet-data/mounts/media rw,relatime - ext4 /dev/sdb rw`,
		`42 35 0:43 / /root/yeet-data/mounts/My\040Media rw,relatime - ext4 /dev/sdc rw`,
	}, "\n"))
	got, err := hostStorageMountPointsFromReader(mountInfo)
	if err != nil {
		t.Fatalf("hostStorageMountPointsFromReader: %v", err)
	}
	want := []string{"/", "/root/yeet-data/mounts/My Media", "/root/yeet-data/mounts/media"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mount points = %#v, want %#v", got, want)
	}
}

func TestHostStoragePathMapRewritesLongestPrefixFirst(t *testing.T) {
	mappings := hostStoragePathMappings{
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
		{From: "/root/data/services/nginx", To: "/flash/yeet/services/nginx", Reason: hostStoragePathReasonServiceRoot, Service: "nginx"},
	}
	mappings = mappings.Sorted()

	tests := map[string]string{
		"/root/data/services/catch/run/catch": "/flash/yeet/services/catch/run/catch",
		"/root/data/services/nginx/data/file": "/flash/yeet/services/nginx/data/file",
		"/root/data/tsd/tailscaled-1.2.3":     "/flash/yeet/data/tsd/tailscaled-1.2.3",
		"/root/database/file":                 "/root/database/file",
		"relative/path":                       "relative/path",
	}
	for input, want := range tests {
		got, changed, err := mappings.Rewrite(input)
		if err != nil {
			t.Fatalf("Rewrite(%q) error: %v", input, err)
		}
		if got != filepath.Clean(want) {
			t.Fatalf("Rewrite(%q) = %q, want %q", input, got, want)
		}
		if changed != (input != want && filepath.Clean(input) != filepath.Clean(want)) {
			t.Fatalf("Rewrite(%q) changed = %v", input, changed)
		}
	}
}

func TestHostStorageMappingsFromPlan(t *testing.T) {
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{
			DataDir:      "/root/data",
			ServicesRoot: "/root/data/services",
		},
		Desired: catchrpc.HostStorageState{
			DataDir:      "/flash/yeet/data",
			ServicesRoot: "/flash/yeet/services",
		},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: "/root/data",
			To:   "/flash/yeet/data",
		},
		ServicesAction: catchrpc.HostStorageServicesAction{
			AffectedServices: []catchrpc.HostStorageServiceMove{
				{Name: "nginx", From: "/root/data/services/nginx", To: "/flash/yeet/services/nginx"},
			},
		},
		CatchAction: catchrpc.HostStorageCatchAction{
			Move: true,
			From: "/root/data/services/catch",
			To:   "/flash/yeet/services/catch",
		},
	}

	got := hostStorageMappingsFromPlan(plan)
	want := hostStoragePathMappings{
		{From: "/root/data/services/catch", To: "/flash/yeet/services/catch", Reason: hostStoragePathReasonCatchRoot},
		{From: "/root/data/services/nginx", To: "/flash/yeet/services/nginx", Reason: hostStoragePathReasonServiceRoot, Service: "nginx"},
		{From: "/root/data/services", To: "/flash/yeet/services", Reason: hostStoragePathReasonServicesDir},
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hostStorageMappingsFromPlan() = %#v, want %#v", got, want)
	}
}

func TestHostStorageMappingsFromPlanSkipsSamePathDatasetMoves(t *testing.T) {
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{
			DataDir:      "/flash/yeet/data",
			ServicesRoot: "/flash/yeet/services",
		},
		Desired: catchrpc.HostStorageState{
			DataDir:      "/flash/yeet/data",
			ServicesRoot: "/flash/yeet/services",
			ServicesZFS:  true,
		},
		ServicesAction: catchrpc.HostStorageServicesAction{
			Mode: catchrpc.HostStorageMigrateAll,
			From: "/flash/yeet/services",
			To:   "/flash/yeet/services",
			AffectedServices: []catchrpc.HostStorageServiceMove{{
				Name:  "api",
				From:  "/flash/yeet/services/api",
				To:    "/flash/yeet/services/api",
				ToZFS: "flash/yeet/services/api",
			}},
		},
		CatchAction: catchrpc.HostStorageCatchAction{
			Move:  true,
			From:  "/flash/yeet/services/catch",
			To:    "/flash/yeet/services/catch",
			ToZFS: "flash/yeet/services/catch",
		},
	}

	if got := hostStorageMappingsFromPlan(plan); len(got) != 0 {
		t.Fatalf("hostStorageMappingsFromPlan() = %#v, want no path mappings for same-path zfs conversion", got)
	}
}

func TestHostStorageMappingsFromPlanSkipsPartialServicesRoot(t *testing.T) {
	tests := map[string]catchrpc.HostStoragePlan{
		"missing current": {
			Desired: catchrpc.HostStorageState{ServicesRoot: "/flash/yeet/services"},
		},
		"missing desired": {
			Current: catchrpc.HostStorageState{ServicesRoot: "/root/data/services"},
		},
		"both missing": {},
	}

	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			mappings := hostStorageMappingsFromPlan(plan)
			for _, mapping := range mappings {
				if mapping.Reason == hostStoragePathReasonServicesDir {
					t.Fatalf("hostStorageMappingsFromPlan() included partial services-root mapping: %#v", mapping)
				}
			}

			got, changed, err := mappings.Rewrite("/root/data/services/nginx/file")
			if err != nil {
				t.Fatalf("Rewrite() error = %v", err)
			}
			if changed {
				t.Fatalf("Rewrite() changed = true, got %q", got)
			}
			if got != "/root/data/services/nginx/file" {
				t.Fatalf("Rewrite() = %q, want unchanged absolute path", got)
			}
		})
	}
}

func TestHostStorageScanDataFindsOldRootRefs(t *testing.T) {
	data := &db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:             "api",
				Generation:       2,
				LatestGeneration: 3,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(1): "/root/data/services/api/bin/old-api",
						db.Gen(2): "/root/data/services/api/bin/api",
						db.Gen(3): "/flash/yeet/services/api/bin/api",
						"latest":  "/flash/yeet/services/api/bin/api",
					}},
					db.ArtifactEnvFile: {Refs: map[db.ArtifactRef]string{
						db.Gen(1): "/root/data/services/api/run/old-env",
					}},
				},
			},
			"devbox": {
				Name: "devbox",
				VM: &db.VMConfig{
					Image: db.VMImageConfig{
						RootFS: "/root/data/vm-images/ubuntu/rootfs.ext4",
					},
					Disk: db.VMDiskConfig{
						Path: "/root/data/services/devbox/data/rootfs.raw",
					},
					Console: db.VMConsoleConfig{
						SocketPath: "/root/data/services/devbox/run/serial.sock",
						LogPath:    "/root/data/services/devbox/run/serial.log",
					},
					Sockets: db.VMSocketConfig{
						APISocketPath:   "/root/data/services/devbox/run/firecracker.sock",
						VsockSocketPath: "/root/data/services/devbox/run/vsock.sock",
					},
					PIDFile: "/root/data/services/devbox/run/firecracker.pid",
					Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{
						Configured: db.VMRuntimeArtifactConfig{
							Firecracker: "/root/data/vm-runtimes/amd64/firecracker-v1.16.1-yeet-v1/manifest/firecracker",
							Jailer:      "/root/data/vm-runtimes/amd64/firecracker-v1.16.1-yeet-v1/manifest/jailer",
						},
						Staged: &db.VMRuntimeArtifactConfig{
							Firecracker: "/root/data/vm-runtimes/amd64/firecracker-v1.17.0-yeet-v1/manifest/firecracker",
							Jailer:      "/root/data/vm-runtimes/amd64/firecracker-v1.17.0-yeet-v1/manifest/jailer",
						},
						Previous: &db.VMRuntimeArtifactConfig{
							Firecracker: "/root/data/vm-runtimes/amd64/firecracker-v1.15.0-yeet-v1/manifest/firecracker",
							Jailer:      "/root/data/vm-runtimes/amd64/firecracker-v1.15.0-yeet-v1/manifest/jailer",
						},
					}},
				},
			},
		},
	}
	mappings := hostStoragePathMappings{
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	}

	refs := scanHostStorageDataRefs(data, mappings)

	if len(refs) != 14 {
		t.Fatalf("refs len = %d, want 14: %#v", len(refs), refs)
	}
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "api",
		Field:   "Artifacts.binary.Refs.gen-2",
		Path:    "/root/data/services/api/bin/api",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "devbox",
		Field:   "VM.Image.RootFS",
		Path:    "/root/data/vm-images/ubuntu/rootfs.ext4",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "devbox",
		Field:   "VM.Disk.Path",
		Path:    "/root/data/services/devbox/data/rootfs.raw",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "devbox",
		Field:   "VM.Console.SocketPath",
		Path:    "/root/data/services/devbox/run/serial.sock",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "devbox",
		Field:   "VM.Console.LogPath",
		Path:    "/root/data/services/devbox/run/serial.log",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "devbox",
		Field:   "VM.Sockets.APISocketPath",
		Path:    "/root/data/services/devbox/run/firecracker.sock",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "devbox",
		Field:   "VM.Sockets.VsockSocketPath",
		Path:    "/root/data/services/devbox/run/vsock.sock",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: "devbox",
		Field:   "VM.PIDFile",
		Path:    "/root/data/services/devbox/run/firecracker.pid",
	})
	for field, path := range map[string]string{
		"VM.Components.Runtime.Configured.Firecracker": "/root/data/vm-runtimes/amd64/firecracker-v1.16.1-yeet-v1/manifest/firecracker",
		"VM.Components.Runtime.Configured.Jailer":      "/root/data/vm-runtimes/amd64/firecracker-v1.16.1-yeet-v1/manifest/jailer",
		"VM.Components.Runtime.Staged.Firecracker":     "/root/data/vm-runtimes/amd64/firecracker-v1.17.0-yeet-v1/manifest/firecracker",
		"VM.Components.Runtime.Staged.Jailer":          "/root/data/vm-runtimes/amd64/firecracker-v1.17.0-yeet-v1/manifest/jailer",
		"VM.Components.Runtime.Previous.Firecracker":   "/root/data/vm-runtimes/amd64/firecracker-v1.15.0-yeet-v1/manifest/firecracker",
		"VM.Components.Runtime.Previous.Jailer":        "/root/data/vm-runtimes/amd64/firecracker-v1.15.0-yeet-v1/manifest/jailer",
	} {
		requireHostStorageRef(t, refs, hostStorageReference{Kind: hostStorageReferenceDB, Service: "devbox", Field: field, Path: path})
	}
}

func TestHostStorageScanSystemdRefsFindsYeetUnitsOnly(t *testing.T) {
	root := t.TempDir()
	writeHostStorageRefTestFile(t, filepath.Join(root, "api.service"), "[Service]\nExecStart=/root/data/services/api/bin/api\n")
	writeHostStorageRefTestFile(t, filepath.Join(root, "yeet-nginx-ns.service"), "[Service]\nExecStart=/root/data/services/nginx/bin/service-ns\n")
	writeHostStorageRefTestFile(t, filepath.Join(root, "yeet-vm-devbox.service"), strings.Join([]string{
		"ExecStart=/usr/local/bin/catch -data-dir=/root/data vm-run",
		"  --runtime-descriptor /root/data/services/devbox/data/vm-runtime.json",
		"  --runtime-running-marker /root/data/services/devbox/run/vm-runtime-running.json",
	}, "\n"))
	writeHostStorageRefTestFile(t, filepath.Join(root, "docker.socket"), "ListenStream=/root/data/docker.sock\n")
	writeHostStorageRefTestFile(t, filepath.Join(root, "yeet-not-a-unit.txt"), "ExecStart=/root/data/bin/tool\n")
	writeHostStorageRefTestFile(t, filepath.Join(root, "yeet-sibling.service"), "ExecStart=/root/database/bin/tool\n")

	refs, err := scanHostStorageSystemdRefs(root, hostStoragePathMappings{
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	})

	if err != nil {
		t.Fatalf("scanHostStorageSystemdRefs error: %v", err)
	}
	if len(refs) != 5 {
		t.Fatalf("refs len = %d, want 5: %#v", len(refs), refs)
	}
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind: hostStorageReferenceSystemd,
		Unit: "api.service",
		File: filepath.Join(root, "api.service"),
		Line: 2,
		Path: "/root/data/services/api/bin/api",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind: hostStorageReferenceSystemd,
		Unit: "yeet-nginx-ns.service",
		File: filepath.Join(root, "yeet-nginx-ns.service"),
		Line: 2,
		Path: "/root/data/services/nginx/bin/service-ns",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind: hostStorageReferenceSystemd,
		Unit: "yeet-vm-devbox.service",
		File: filepath.Join(root, "yeet-vm-devbox.service"),
		Line: 1,
		Path: "/root/data",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind: hostStorageReferenceSystemd,
		Unit: "yeet-vm-devbox.service",
		File: filepath.Join(root, "yeet-vm-devbox.service"),
		Line: 2,
		Path: "/root/data/services/devbox/data/vm-runtime.json",
	})
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind: hostStorageReferenceSystemd,
		Unit: "yeet-vm-devbox.service",
		File: filepath.Join(root, "yeet-vm-devbox.service"),
		Line: 3,
		Path: "/root/data/services/devbox/run/vm-runtime-running.json",
	})
}

func TestHostStorageScanTextFileRefsIgnoresCommentLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.service")
	writeHostStorageRefTestFile(t, path, strings.Join([]string{
		"# previously /root/data/services/api/bin/api",
		"   ; /root/data/services/api/run/env",
		"",
		"ExecStart=/root/data/services/api/bin/api",
	}, "\n"))

	refs, err := scanHostStorageTextFileRefs(path, "api.service", hostStoragePathMappings{
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	})

	if err != nil {
		t.Fatalf("scanHostStorageTextFileRefs error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1: %#v", len(refs), refs)
	}
	requireHostStorageRef(t, refs, hostStorageReference{
		Kind: hostStorageReferenceSystemd,
		Unit: "api.service",
		File: path,
		Line: 4,
		Path: "/root/data/services/api/bin/api",
	})
}

func TestHostStorageScanSystemdRefsIgnoresMissingDir(t *testing.T) {
	refs, err := scanHostStorageSystemdRefs(filepath.Join(t.TempDir(), "missing"), hostStoragePathMappings{
		{From: "/root/data", To: "/flash/yeet/data", Reason: hostStoragePathReasonDataDir},
	})
	if err != nil {
		t.Fatalf("scanHostStorageSystemdRefs missing dir error = %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs len = %d, want 0: %#v", len(refs), refs)
	}
}

func requireHostStorageRef(t *testing.T, refs []hostStorageReference, want hostStorageReference) {
	t.Helper()
	for _, ref := range refs {
		if ref == want {
			return
		}
	}
	t.Fatalf("missing reference %#v in %#v", want, refs)
}

func writeHostStorageRefTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
