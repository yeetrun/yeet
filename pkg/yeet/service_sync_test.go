// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestServiceSyncNamedWritesZFSDatasetToConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	loadedPrefs.DefaultHost = "yeet-lab"
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "compose.yml"})
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "yeet-lab" || service != "sonarr" {
			t.Fatalf("fetch host=%q service=%q, want yeet-lab sonarr", host, service)
		}
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{
				Root:           "/flash/yeet/sonarr",
				EffectiveRoot:  "/flash/yeet/sonarr",
				ServiceRoot:    "/flash/yeet/sonarr",
				ServiceRootZFS: "flash/yeet/sonarr",
			}},
		}, nil
	}

	out, err := captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "sync", "sonarr"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd service sync: %v", err)
	}
	if !strings.Contains(out, "Updated sonarr@yeet-lab") || !strings.Contains(out, `service_root = "flash/yeet/sonarr"`) || !strings.Contains(out, "service_root_zfs = true") {
		t.Fatalf("output = %q, want updated zfs fields", out)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("sonarr", "yeet-lab")
	if !ok || entry.ServiceRoot != "flash/yeet/sonarr" || !entry.ServiceRootZFS {
		t.Fatalf("entry = %#v, want zfs dataset root", entry)
	}
}

func TestServiceSyncNamedUsesExplicitConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	cwd := useTempSvcCwd(t)
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, projectConfigName)
	loc := &projectConfigLocation{Path: configPath, Dir: configDir, Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{Name: "radarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "compose.yml"})
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	loadedPrefs.DefaultHost = "yeet-lab"
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{
				Root:          "/srv/apps/radarr",
				EffectiveRoot: "/srv/apps/radarr",
				ServiceRoot:   "/srv/apps/radarr",
			}},
		}, nil
	}

	if err := HandleSvcCmd([]string{"service", "sync", "radarr", "--config", configPath}); err != nil {
		t.Fatalf("HandleSvcCmd explicit config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, projectConfigName)); !os.IsNotExist(err) {
		t.Fatalf("sync with --config should not create cwd yeet.toml, stat err=%v", err)
	}
	loaded, err := loadProjectConfigFromFile(configPath)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	entry, _ := loaded.Config.ServiceEntry("radarr", "yeet-lab")
	if entry.ServiceRoot != "/srv/apps/radarr" || entry.ServiceRootZFS {
		t.Fatalf("entry = %#v, want filesystem root", entry)
	}
}

func TestServiceSyncNamedExplicitConfigIgnoresMalformedCwdConfig(t *testing.T) {
	preserveSvcCommandGlobals(t)
	cwd := useTempSvcCwd(t)
	if err := os.WriteFile(filepath.Join(cwd, projectConfigName), []byte("[[services]\n"), 0o600); err != nil {
		t.Fatalf("WriteFile cwd config: %v", err)
	}
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, projectConfigName)
	loc := &projectConfigLocation{Path: configPath, Dir: configDir, Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{Name: "radarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "compose.yml"})
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	loadedPrefs.DefaultHost = "yeet-lab"
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "yeet-lab" || service != "radarr" {
			t.Fatalf("fetch host=%q service=%q, want yeet-lab radarr", host, service)
		}
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info:  catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{ServiceRoot: "/srv/apps/radarr"}},
		}, nil
	}

	if err := HandleSvcCmd([]string{"service", "sync", "radarr", "--config", configPath}); err != nil {
		t.Fatalf("HandleSvcCmd explicit config with bad cwd config: %v", err)
	}
	loaded, err := loadProjectConfigFromFile(configPath)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	entry, _ := loaded.Config.ServiceEntry("radarr", "yeet-lab")
	if entry.ServiceRoot != "/srv/apps/radarr" {
		t.Fatalf("entry = %#v, want explicit config updated", entry)
	}
}

func TestServiceSyncNamedSupportsHostQualifier(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	loadedPrefs.DefaultHost = "host-a"
	writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "host-b", Type: serviceTypeRun, Payload: "compose.yml"})
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "host-b" || service != "sonarr" {
			t.Fatalf("fetch host=%q service=%q, want host-b sonarr", host, service)
		}
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info:  catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{ServiceRoot: "/srv/apps/sonarr"}},
		}, nil
	}

	if err := HandleSvcCmd([]string{"service", "sync", "sonarr@host-b"}); err != nil {
		t.Fatalf("HandleSvcCmd qualified service sync: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	entry, _ := loaded.Config.ServiceEntry("sonarr", "host-b")
	if entry.ServiceRoot != "/srv/apps/sonarr" {
		t.Fatalf("entry = %#v, want host-qualified entry updated", entry)
	}
}

func TestServiceSyncClearsDefaultRoot(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	loadedPrefs.DefaultHost = "yeet-lab"
	writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:           "sonarr",
		Host:           "yeet-lab",
		Type:           serviceTypeRun,
		Payload:        "compose.yml",
		ServiceRoot:    "flash/yeet/sonarr",
		ServiceRootZFS: true,
	})
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{
				Root:          "/root/data/services/sonarr",
				EffectiveRoot: "/root/data/services/sonarr",
			}},
		}, nil
	}

	if err := HandleSvcCmd([]string{"service", "sync", "sonarr"}); err != nil {
		t.Fatalf("HandleSvcCmd service sync clear: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if strings.Contains(string(raw), "service_root") || strings.Contains(string(raw), "service_root_zfs") {
		t.Fatalf("config = %q, want service root fields omitted", string(raw))
	}
}

func TestServiceSyncAllUpdatesAndSkipsMissing(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	loadedPrefs.DefaultHost = "yeet-lab"
	writeSvcBranchConfig(t, tmp,
		ServiceEntry{Name: "sonarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "sonarr.yml"},
		ServiceEntry{Name: "radarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "radarr.yml"},
		ServiceEntry{Name: "lidarr", Host: "other-host", Type: serviceTypeRun, Payload: "lidarr.yml"},
	)
	fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		switch service {
		case "sonarr":
			return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{Paths: catchrpc.ServicePaths{ServiceRoot: "/srv/apps/sonarr"}}}, nil
		case "radarr":
			return catchrpc.ServiceInfoResponse{Found: false, Message: "service not found"}, nil
		default:
			t.Fatalf("unexpected fetch for %s@%s", service, host)
			return catchrpc.ServiceInfoResponse{}, nil
		}
	}

	out, err := captureSvcStdout(t, func() error {
		return HandleSvcCmd([]string{"service", "sync", "--all"})
	})
	if err != nil {
		t.Fatalf("HandleSvcCmd service sync --all: %v", err)
	}
	if !strings.Contains(out, "Updated sonarr@yeet-lab") || !strings.Contains(out, "Skipped radarr@yeet-lab: service not found on catch") || !strings.Contains(out, "1 updated, 1 skipped") {
		t.Fatalf("output = %q, want update and skip summary", out)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd: %v", err)
	}
	sonarr, _ := loaded.Config.ServiceEntry("sonarr", "yeet-lab")
	if sonarr.ServiceRoot != "/srv/apps/sonarr" || sonarr.ServiceRootZFS {
		t.Fatalf("sonarr = %#v, want filesystem root", sonarr)
	}
	if _, ok := loaded.Config.ServiceEntry("lidarr", "other-host"); !ok {
		t.Fatalf("other-host entry should be untouched")
	}
}

func TestServiceSyncErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		args    []string
		wantErr string
	}{
		{
			name:    "no config",
			setup:   func(t *testing.T) { useTempSvcCwd(t); loadedPrefs.DefaultHost = "yeet-lab" },
			args:    []string{"service", "sync", "sonarr"},
			wantErr: "no yeet.toml found; run from a project directory or pass --config",
		},
		{
			name: "no matching entry",
			setup: func(t *testing.T) {
				tmp := useTempSvcCwd(t)
				loadedPrefs.DefaultHost = "yeet-lab"
				writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "radarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "compose.yml"})
			},
			args:    []string{"service", "sync", "sonarr"},
			wantErr: "no yeet.toml entry for sonarr@yeet-lab",
		},
		{
			name: "remote missing",
			setup: func(t *testing.T) {
				tmp := useTempSvcCwd(t)
				loadedPrefs.DefaultHost = "yeet-lab"
				writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "compose.yml"})
				fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
					return catchrpc.ServiceInfoResponse{Found: false, Message: "service not found"}, nil
				}
			},
			args:    []string{"service", "sync", "sonarr"},
			wantErr: `service "sonarr" not found on yeet-lab`,
		},
		{
			name: "fetch error",
			setup: func(t *testing.T) {
				tmp := useTempSvcCwd(t)
				loadedPrefs.DefaultHost = "yeet-lab"
				writeSvcBranchConfig(t, tmp, ServiceEntry{Name: "sonarr", Host: "yeet-lab", Type: serviceTypeRun, Payload: "compose.yml"})
				fetchServiceInfoForSyncFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
					return catchrpc.ServiceInfoResponse{}, errors.New("rpc unavailable")
				}
			},
			args:    []string{"service", "sync", "sonarr"},
			wantErr: "rpc unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveSvcCommandGlobals(t)
			tt.setup(t)
			err := HandleSvcCmd(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("HandleSvcCmd error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
