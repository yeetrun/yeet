// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type closeErrorBuffer struct {
	bytes.Buffer
	err error
}

func (w *closeErrorBuffer) Close() error {
	return w.err
}

func TestSaveProjectConfigReturnsCloseError(t *testing.T) {
	oldCreate := createProjectConfigFileFn
	defer func() {
		createProjectConfigFileFn = oldCreate
	}()

	closeErr := errors.New("close failed")
	createProjectConfigFileFn = func(string) (io.WriteCloser, error) {
		return &closeErrorBuffer{err: closeErr}, nil
	}

	tmp := t.TempDir()
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveProjectConfig(loc); !errors.Is(err, closeErr) {
		t.Fatalf("saveProjectConfig error = %v, want %v", err, closeErr)
	}
}

func TestLoadOrCreateProjectConfigFromCwdCreatesDefaultLocation(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	realTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks tmp: %v", err)
	}
	if err := os.Chdir(realTmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	loc, err := loadOrCreateProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadOrCreateProjectConfigFromCwd error: %v", err)
	}
	if loc == nil || loc.Path != filepath.Join(realTmp, projectConfigName) || loc.Dir != realTmp {
		t.Fatalf("location = %#v, want default location in cwd", loc)
	}
	if loc.Config == nil || loc.Config.Version != projectConfigVersion {
		t.Fatalf("config = %#v, want default version", loc.Config)
	}
}

func TestLoadProjectConfigFromDirDefaultsVersionAndReportsParseErrors(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, projectConfigName), []byte(`
[[services]]
name = "api"
host = "host-a"
`), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	subdir := filepath.Join(tmp, "nested")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("Mkdir nested: %v", err)
	}

	loc, err := loadProjectConfigFromDir(subdir)
	if err != nil {
		t.Fatalf("loadProjectConfigFromDir error: %v", err)
	}
	if loc == nil || loc.Config == nil || loc.Config.Version != projectConfigVersion {
		t.Fatalf("location = %#v, want config with default version", loc)
	}

	missing, err := loadProjectConfigFromDir(t.TempDir())
	if err != nil {
		t.Fatalf("load missing config error: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing config = %#v, want nil", missing)
	}

	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, projectConfigName), []byte("bad = ["), 0o600); err != nil {
		t.Fatalf("WriteFile bad config: %v", err)
	}
	_, err = loadProjectConfigFromDir(badDir)
	if err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Fatalf("bad config error = %v, want parse error", err)
	}
}

func TestLoadProjectConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.toml")
	loc := &projectConfigLocation{
		Path: path,
		Dir:  dir,
		Config: &ProjectConfig{
			Version: projectConfigVersion,
			Services: []ServiceEntry{{
				Name:    "sonarr",
				Host:    "yeet-pve1",
				Type:    serviceTypeRun,
				Payload: "compose.yml",
			}},
		},
	}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}

	loaded, err := loadProjectConfigFromFile(path)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	if loaded.Path != path {
		t.Fatalf("Path = %q, want %q", loaded.Path, path)
	}
	if loaded.Dir != dir {
		t.Fatalf("Dir = %q, want %q", loaded.Dir, dir)
	}
	if _, ok := loaded.Config.ServiceEntry("sonarr", "yeet-pve1"); !ok {
		t.Fatalf("loaded config missing sonarr entry: %#v", loaded.Config)
	}
}

func TestProjectConfigSnapshotFieldsRoundTrip(t *testing.T) {
	required := false
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:             "svc",
		Host:             "host-a",
		Type:             serviceTypeRun,
		Payload:          "compose.yml",
		PayloadKind:      "compose",
		ServiceRoot:      "tank/apps/svc",
		ServiceRootZFS:   true,
		Snapshots:        "off",
		SnapshotKeepLast: 3,
		SnapshotMaxAge:   "72h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run"},
		Ports:            []string{"80:80", "443:443"},
	})
	loc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), projectConfigName), Dir: t.TempDir(), Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	loaded, err := loadProjectConfigFromFile(loc.Path)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.PayloadKind != "compose" || entry.Snapshots != "off" || entry.SnapshotKeepLast != 3 || entry.SnapshotMaxAge != "72h" || entry.SnapshotRequired == nil || *entry.SnapshotRequired || !reflect.DeepEqual(entry.SnapshotEvents, []string{"run"}) || !reflect.DeepEqual(entry.Ports, []string{"80:80", "443:443"}) {
		t.Fatalf("entry snapshot fields = %#v", entry)
	}
	raw, err := os.ReadFile(loc.Path)
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if !strings.Contains(string(raw), `snapshot_keep_last = 3`) {
		t.Fatalf("saved config = %q, want snapshot_keep_last", string(raw))
	}
	if !strings.Contains(string(raw), `ports = ["80:80", "443:443"]`) {
		t.Fatalf("saved config = %q, want ports", string(raw))
	}
}

func TestProjectConfigOmitsZeroSnapshotKeepLast(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:             "svc",
		Host:             "host-a",
		Payload:          "compose.yml",
		Snapshots:        "off",
		SnapshotKeepLast: 0,
	})
	loc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), projectConfigName), Dir: t.TempDir(), Config: cfg}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	raw, err := os.ReadFile(loc.Path)
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if strings.Contains(string(raw), "snapshot_keep_last") {
		t.Fatalf("saved config = %q, want snapshot_keep_last omitted", string(raw))
	}
	if !strings.Contains(string(raw), `snapshots = "off"`) {
		t.Fatalf("saved config = %q, want snapshot mode retained", string(raw))
	}
}

func TestProjectConfigLegacyZeroSnapshotKeepLastSavesClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, projectConfigName)
	if err := os.WriteFile(path, []byte(`
version = 1

[[services]]
  name = "svc"
  host = "host-a"
  payload = "compose.yml"
  snapshots = "off"
  snapshot_keep_last = 0
`), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	loc, err := loadProjectConfigFromFile(path)
	if err != nil {
		t.Fatalf("loadProjectConfigFromFile: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("svc", "host-a")
	if !ok {
		t.Fatal("missing service entry")
	}
	if entry.SnapshotKeepLast != 0 {
		t.Fatalf("SnapshotKeepLast = %d, want 0 inherit sentinel", entry.SnapshotKeepLast)
	}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if strings.Contains(string(raw), "snapshot_keep_last") {
		t.Fatalf("saved config = %q, want legacy snapshot_keep_last removed", string(raw))
	}
	if !strings.Contains(string(raw), `snapshots = "off"`) {
		t.Fatalf("saved config = %q, want snapshot mode retained", string(raw))
	}
}

func TestSaveRunConfigStoresVMType(t *testing.T) {
	oldService := serviceOverride
	serviceOverride = "devbox"
	defer func() { serviceOverride = oldService }()

	tmp := t.TempDir()
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveRunConfigWithPayloadKind(loc, "yeet-pve1", "vm://ubuntu/26.04", serviceTypeVM, []string{"--net=svc", "--cpus=4"}, "", false); err != nil {
		t.Fatalf("saveRunConfigWithPayloadKind: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("devbox", "yeet-pve1")
	if !ok {
		t.Fatal("missing devbox entry")
	}
	if entry.Type != serviceTypeVM {
		t.Fatalf("type = %q, want vm", entry.Type)
	}
	if entry.PayloadKind != serviceTypeVM {
		t.Fatalf("payload kind = %q, want vm", entry.PayloadKind)
	}
}

func TestLoadProjectConfigFromFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadProjectConfigFromFile(filepath.Join(dir, "missing.toml")); err == nil || !strings.Contains(err.Error(), "no yeet.toml found at") {
		t.Fatalf("missing config error = %v, want no yeet.toml found", err)
	}
	if _, err := loadProjectConfigFromFile(dir); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory config error = %v, want directory rejection", err)
	}
}

func TestSaveProjectConfigNoopsNilAndSetsDefaultVersion(t *testing.T) {
	if err := saveProjectConfig(nil); err != nil {
		t.Fatalf("saveProjectConfig nil error: %v", err)
	}
	if err := saveProjectConfig(&projectConfigLocation{}); err != nil {
		t.Fatalf("saveProjectConfig nil config error: %v", err)
	}

	tmp := t.TempDir()
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{},
	}
	if err := saveProjectConfig(loc); err != nil {
		t.Fatalf("saveProjectConfig error: %v", err)
	}
	if loc.Config.Version != projectConfigVersion {
		t.Fatalf("Version = %d, want %d", loc.Config.Version, projectConfigVersion)
	}
	b, err := os.ReadFile(loc.Path)
	if err != nil {
		t.Fatalf("ReadFile saved config: %v", err)
	}
	if !strings.Contains(string(b), "version = 1") {
		t.Fatalf("saved config = %q, want version", string(b))
	}
}

func TestRemoveServiceConfig(t *testing.T) {
	oldService := serviceOverride
	defer func() {
		serviceOverride = oldService
	}()

	tests := []struct {
		name        string
		service     string
		host        string
		wantSaved   bool
		wantRemoved bool
	}{
		{
			name:        "removes matching service and host",
			service:     "svc-a",
			host:        "host-a",
			wantSaved:   true,
			wantRemoved: true,
		},
		{
			name:        "skips missing host",
			service:     "svc-a",
			host:        "missing",
			wantSaved:   false,
			wantRemoved: false,
		},
		{
			name:        "skips empty service override",
			service:     "",
			host:        "host-a",
			wantSaved:   false,
			wantRemoved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceOverride = tt.service
			tmp := t.TempDir()
			cfg := &ProjectConfig{Version: projectConfigVersion}
			cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Payload: "run-a"})
			cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-b", Payload: "run-b"})
			cfg.SetServiceEntry(ServiceEntry{Name: "svc-b", Host: "host-a", Payload: "run-c"})
			loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: cfg}

			if err := removeServiceConfig(loc, tt.host); err != nil {
				t.Fatalf("removeServiceConfig error: %v", err)
			}
			_, hasRemovedTarget := loc.Config.ServiceEntry("svc-a", "host-a")
			if hasRemovedTarget == tt.wantRemoved {
				t.Fatalf("removed target present = %v, want %v", hasRemovedTarget, !tt.wantRemoved)
			}
			if _, ok := loc.Config.ServiceEntry("svc-a", "host-b"); !ok {
				t.Fatalf("expected svc-a@host-b to remain")
			}
			if _, ok := loc.Config.ServiceEntry("svc-b", "host-a"); !ok {
				t.Fatalf("expected svc-b@host-a to remain")
			}

			_, statErr := os.Stat(loc.Path)
			if tt.wantSaved && statErr != nil {
				t.Fatalf("expected config file to be saved: %v", statErr)
			}
			if !tt.wantSaved && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("expected no saved config file, stat error = %v", statErr)
			}
		})
	}
}

func TestProjectConfigSetServiceRootForEntryCanClear(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:           "sonarr",
		Host:           "yeet-pve1",
		Type:           serviceTypeRun,
		Payload:        "compose.yml",
		ServiceRoot:    "flash/yeet/sonarr",
		ServiceRootZFS: true,
	})

	if !cfg.SetServiceRootForEntry("sonarr", "yeet-pve1", "/srv/apps/sonarr", false) {
		t.Fatalf("SetServiceRootForEntry filesystem = false, want true")
	}
	entry, _ := cfg.ServiceEntry("sonarr", "yeet-pve1")
	if entry.ServiceRoot != "/srv/apps/sonarr" || entry.ServiceRootZFS {
		t.Fatalf("filesystem entry = %#v, want root without zfs", entry)
	}

	if !cfg.SetServiceRootForEntry("sonarr", "yeet-pve1", "", false) {
		t.Fatalf("SetServiceRootForEntry clear = false, want true")
	}
	entry, _ = cfg.ServiceEntry("sonarr", "yeet-pve1")
	if entry.ServiceRoot != "" || entry.ServiceRootZFS {
		t.Fatalf("cleared entry = %#v, want empty root and false zfs", entry)
	}

	if cfg.SetServiceRootForEntry("missing", "yeet-pve1", "/srv/apps/missing", false) {
		t.Fatalf("SetServiceRootForEntry missing = true, want false")
	}
}

func TestProjectConfigSetServiceEntryUpdatesPayloadKind(t *testing.T) {
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:        "svc-a",
		Host:        "host-a",
		Type:        serviceTypeRun,
		Payload:     "alpine",
		PayloadKind: "local-image",
	})
	cfg.SetServiceEntry(ServiceEntry{
		Name:        "svc-a",
		Host:        "host-a",
		Type:        serviceTypeRun,
		Payload:     "compose.yml",
		PayloadKind: "compose",
	})
	entry, _ := cfg.ServiceEntry("svc-a", "host-a")
	if entry.PayloadKind != "compose" {
		t.Fatalf("PayloadKind after update = %q, want compose", entry.PayloadKind)
	}

	cfg.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "run.sh",
	})
	entry, _ = cfg.ServiceEntry("svc-a", "host-a")
	if entry.PayloadKind != "" {
		t.Fatalf("PayloadKind after clear = %q, want empty", entry.PayloadKind)
	}
}

func TestSaveRunConfigCreatesToml(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	runArgs := []string{"--net=svc", "--", "--flag", "value"}
	wantArgs := []string{"--net=svc", "--flag", "value"}
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveRunConfig(loc, "host-a", payload, runArgs, "", false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	if loaded == nil || loaded.Config == nil {
		t.Fatalf("expected config to be saved")
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if entry.Type != "" {
		t.Fatalf("type = %q", entry.Type)
	}
	if entry.Payload != filepath.Join("apps", "compose.yml") {
		t.Fatalf("payload = %q", entry.Payload)
	}
	if len(entry.Args) != len(wantArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range wantArgs {
		if entry.Args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], wantArgs[i])
		}
	}
}

func TestSaveRunConfigStoresServiceRoot(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	serviceRoot := "/srv/apps/svc-a"
	if err := saveRunConfig(loc, "host-a", payload, []string{"--service-root", serviceRoot, "--pull"}, serviceRoot, false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if entry.ServiceRoot != serviceRoot {
		t.Fatalf("service_root = %q, want %q", entry.ServiceRoot, serviceRoot)
	}
	if strings.Join(entry.Args, " ") != "--pull" {
		t.Fatalf("args = %#v, want only --pull", entry.Args)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, projectConfigName))
	if err != nil {
		t.Fatalf("ReadFile config: %v", err)
	}
	if !strings.Contains(string(raw), `service_root = "/srv/apps/svc-a"`) {
		t.Fatalf("saved config = %q, want service_root", string(raw))
	}
}

func TestSaveRunConfigStoresPublishPortsOutsideArgs(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveRunConfig(loc, "host-a", payload, []string{"-p", "80:80/tcp", "--publish=443:443/UDP", "--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if !reflect.DeepEqual(entry.Ports, []string{"80:80", "443:443/udp"}) {
		t.Fatalf("ports = %#v, want publish ports", entry.Ports)
	}
	if !reflect.DeepEqual(entry.Args, []string{"--pull"}) {
		t.Fatalf("args = %#v, want publish flags removed", entry.Args)
	}
}

func TestSaveRunConfigMigratesLegacyPublishArgsToPorts(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	tmp := t.TempDir()
	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	loc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name:    "svc-a",
			Host:    "host-a",
			Payload: "apps/compose.yml",
			Args:    []string{"--publish=80:80/tcp", "-p", "443:443/TCP", "--pull"},
		}}},
	}

	if err := saveRunConfig(loc, "host-a", payload, []string{"--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if !reflect.DeepEqual(entry.Ports, []string{"80:80", "443:443"}) {
		t.Fatalf("ports = %#v, want migrated legacy publish args", entry.Ports)
	}
	if !reflect.DeepEqual(entry.Args, []string{"--pull"}) {
		t.Fatalf("args = %#v, want publish flags removed", entry.Args)
	}
}

func TestSaveRunConfigPublishResetClearsPorts(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	tmp := t.TempDir()
	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	loc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name:    "svc-a",
			Host:    "host-a",
			Payload: "apps/compose.yml",
			Ports:   []string{"80:80"},
			Args:    []string{"--pull"},
		}}},
	}

	if err := saveRunConfig(loc, "host-a", payload, []string{"--publish-reset", "--pull"}, "", false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if len(entry.Ports) != 0 {
		t.Fatalf("ports = %#v, want cleared", entry.Ports)
	}
	if !reflect.DeepEqual(entry.Args, []string{"--pull"}) {
		t.Fatalf("args = %#v, want publish reset removed", entry.Args)
	}
}

func TestRunFromProjectConfigRehydratesPorts(t *testing.T) {
	preserveSvcCommandGlobals(t)
	tmp := useTempSvcCwd(t)
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "host-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	isTerminalFn = func(int) bool { return false }

	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile payload: %v", err)
	}
	loc := writeSvcBranchConfig(t, tmp, ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeRun,
		Payload: "run.sh",
		Ports:   []string{"80:80", "443:443"},
		Args:    []string{"--pull"},
	})

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		return nil
	}
	if err := runFromProjectConfig(loc, "host-a"); err != nil {
		t.Fatalf("runFromProjectConfig: %v", err)
	}
	want := []string{"run", "-p", "80:80", "-p", "443:443", "--pull"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestSaveRunConfigStoresZFSServiceRoot(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	locDir := t.TempDir()
	loc := &projectConfigLocation{
		Path: filepath.Join(t.TempDir(), "yeet.toml"),
		Dir:  locDir,
		Config: &ProjectConfig{
			Version: projectConfigVersion,
		},
	}
	serviceOverride = "svc-a"
	if err := saveRunConfig(loc, "host-a", "ghcr.io/example/app:latest", []string{"--service-root", "tank/apps/svc-a", "--zfs", "--pull"}, "tank/apps/svc-a", true); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("missing saved entry")
	}
	if entry.ServiceRoot != "tank/apps/svc-a" {
		t.Fatalf("service_root = %q, want tank/apps/svc-a", entry.ServiceRoot)
	}
	if !entry.ServiceRootZFS {
		t.Fatal("service_root_zfs = false, want true")
	}
	raw, err := os.ReadFile(loc.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), `service_root = "tank/apps/svc-a"`) {
		t.Fatalf("saved config = %q, want service_root", string(raw))
	}
	if !strings.Contains(string(raw), `service_root_zfs = true`) {
		t.Fatalf("saved config = %q, want service_root_zfs", string(raw))
	}
}

func TestSaveRunConfigOverwritesArgs(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "apps", "bin")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}

	firstArgs := []string{"--", "--flagA", "valueA", "--bool-flag", "posArg"}
	if err := saveRunConfig(loc, "host-a", payload, firstArgs, "", false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	secondArgs := []string{"--", "--flagB", "valueB", "--bool-flag2", "posArg2"}
	wantArgs := []string{"--flagB", "valueB", "--bool-flag2", "posArg2"}
	if err := saveRunConfig(loc, "host-a", payload, secondArgs, "", false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if entry.Type != "" {
		t.Fatalf("type = %q", entry.Type)
	}
	if len(entry.Args) != len(wantArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range wantArgs {
		if entry.Args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], wantArgs[i])
		}
	}
}

func TestSaveRunConfigClearsStalePayloadKind(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	tmp := t.TempDir()
	serviceOverride = "svc-a"
	payload := filepath.Join(tmp, "app")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("WriteFile payload error: %v", err)
	}
	loc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name:        "svc-a",
			Host:        "host-a",
			Type:        serviceTypeRun,
			Payload:     "app",
			PayloadKind: "local-image",
		}}},
	}

	if err := saveRunConfig(loc, "host-a", payload, nil, "", false); err != nil {
		t.Fatalf("saveRunConfig error: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("expected service config to be saved")
	}
	if entry.PayloadKind != "" {
		t.Fatalf("PayloadKind = %q, want empty", entry.PayloadKind)
	}
}

func TestSaveCronConfigCreatesToml(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	serviceOverride = "svc-cron"
	payload := filepath.Join(tmp, "apps", "owesplit")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(payload, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	cronFields := []string{"0", "9", "15", "*", "*"}
	binArgs := []string{"-live"}
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveCronConfig(loc, "host-a", payload, cronFields, binArgs); err != nil {
		t.Fatalf("saveCronConfig error: %v", err)
	}

	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-cron", "host-a")
	if !ok {
		t.Fatalf("expected cron config to be saved")
	}
	if entry.Type != serviceTypeCron {
		t.Fatalf("type = %q", entry.Type)
	}
	if entry.Payload != filepath.Join("apps", "owesplit") {
		t.Fatalf("payload = %q", entry.Payload)
	}
	if entry.Schedule != "0 9 15 * *" {
		t.Fatalf("schedule = %q", entry.Schedule)
	}
	if len(entry.Args) != len(binArgs) {
		t.Fatalf("args = %#v", entry.Args)
	}
	for i := range binArgs {
		if entry.Args[i] != binArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, entry.Args[i], binArgs[i])
		}
	}
}

func TestSaveCronConfigClearsRunOnlyFields(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	required := true
	tmp := t.TempDir()
	payload := filepath.Join(tmp, "job.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	loc.Config.SetServiceEntry(ServiceEntry{
		Name:             "backup",
		Host:             "yeet-pve1",
		Type:             serviceTypeRun,
		Payload:          "compose.yml",
		PayloadKind:      "compose",
		EnvFile:          ".env",
		ServiceRoot:      "tank/apps/backup",
		ServiceRootZFS:   true,
		Snapshots:        "on",
		SnapshotKeepLast: 3,
		SnapshotMaxAge:   "72h",
		SnapshotRequired: &required,
		SnapshotEvents:   []string{"run"},
		Ports:            []string{"8080:80"},
	})

	serviceOverride = "backup"
	if err := saveCronConfig(loc, "yeet-pve1", payload, []string{"0", "3", "*", "*", "*"}, []string{"--full"}); err != nil {
		t.Fatalf("saveCronConfig: %v", err)
	}

	entry, ok := loc.Config.ServiceEntry("backup", "yeet-pve1")
	if !ok {
		t.Fatal("saved config missing backup@yeet-pve1")
	}
	if entry.Type != serviceTypeCron || entry.Payload != "job.sh" || entry.Schedule != "0 3 * * *" || !reflect.DeepEqual(entry.Args, []string{"--full"}) {
		t.Fatalf("cron entry core fields = %#v", entry)
	}
	if entry.PayloadKind != "" || entry.EnvFile != "" || entry.ServiceRoot != "" || entry.ServiceRootZFS || entry.Snapshots != "" || entry.SnapshotKeepLast != 0 || entry.SnapshotMaxAge != "" || entry.SnapshotRequired != nil || len(entry.SnapshotEvents) != 0 || len(entry.Ports) != 0 {
		t.Fatalf("cron entry kept run-only fields: %#v", entry)
	}
}

func TestProjectConfigAllHostsDedupesTrimsAndSorts(t *testing.T) {
	cfg := &ProjectConfig{
		Hosts: []string{" host-b ", "", "host-a", "host-b"},
		Services: []ServiceEntry{
			{Name: "svc-a", Host: "host-c"},
			{Name: "svc-b", Host: " host-a "},
			{Name: "svc-c", Host: " "},
		},
	}

	got := cfg.AllHosts()
	if gotString := strings.Join(got, ","); gotString != "host-a,host-b,host-c" {
		t.Fatalf("AllHosts = %#v", got)
	}
}

func TestProjectConfigAllHostsNilConfig(t *testing.T) {
	var cfg *ProjectConfig
	if got := cfg.AllHosts(); got != nil {
		t.Fatalf("AllHosts = %#v, want nil", got)
	}
}

func TestProjectConfigPathResolution(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	realTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks tmp: %v", err)
	}
	work := filepath.Join(realTmp, "work")
	configDir := filepath.Join(realTmp, "config")
	for _, dir := range []string{work, configDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("Chdir work: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	absPayload := filepath.Join(work, "app", "run.sh")
	if got := resolvePayloadPath(configDir, " payload.sh "); got != filepath.Join(configDir, "payload.sh") {
		t.Fatalf("resolvePayloadPath relative = %q", got)
	}
	if got := resolvePayloadPath(configDir, absPayload); got != absPayload {
		t.Fatalf("resolvePayloadPath absolute = %q, want %q", got, absPayload)
	}
	if got := resolvePayloadPath(configDir, "ghcr.io/example/app:latest"); got != "ghcr.io/example/app:latest" {
		t.Fatalf("resolvePayloadPath image = %q, want raw image ref", got)
	}
	if got := resolvePayloadPath(configDir, " "); got != "" {
		t.Fatalf("resolvePayloadPath blank = %q, want empty", got)
	}

	if got := resolveEnvFilePath(configDir, " .env "); got != filepath.Join(configDir, ".env") {
		t.Fatalf("resolveEnvFilePath relative = %q", got)
	}
	if got := resolveEnvFilePath(configDir, " "); got != "" {
		t.Fatalf("resolveEnvFilePath blank = %q, want empty", got)
	}

	if got := relativePayloadPath(configDir, "ghcr.io/example/app:latest"); got != "ghcr.io/example/app:latest" {
		t.Fatalf("relativePayloadPath image = %q", got)
	}
	if got := relativePayloadPath(configDir, "app/run.sh"); got != filepath.Join("..", "work", "app", "run.sh") {
		t.Fatalf("relativePayloadPath relative = %q", got)
	}
	if got := relativePayloadPath(configDir, " "); got != "" {
		t.Fatalf("relativePayloadPath blank = %q", got)
	}

	if got := relativeEnvFilePath(configDir, ".env"); got != filepath.Join("..", "work", ".env") {
		t.Fatalf("relativeEnvFilePath relative = %q", got)
	}
	if got := relativeEnvFilePath(configDir, " "); got != "" {
		t.Fatalf("relativeEnvFilePath blank = %q", got)
	}
}
