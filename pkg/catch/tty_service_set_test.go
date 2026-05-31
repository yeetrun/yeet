// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestServiceSetRootRegistersTTYCommand(t *testing.T) {
	if ttyCommandHandlers["service"] == nil {
		t.Fatal(`expected tty command handler for "service"`)
	}
}

func TestServiceSetRootRejectsServiceCommandSyntax(t *testing.T) {
	execer := &ttyExecer{}
	for _, tt := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "missing subcommand", args: []string{}, wantErr: "service requires a command"},
		{name: "unknown subcommand", args: []string{"bogus"}, wantErr: `unknown service command "bogus"`},
		{name: "extra set args", args: []string{"set", "--service-root", "/srv/api", "extra"}, wantErr: "unexpected service set args: extra"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := execer.serviceCmdFunc(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("serviceCmdFunc error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestServiceSetRootRejectsMissingService(t *testing.T) {
	server := newTestServer(t)
	newRoot := filepath.Join(t.TempDir(), "new-root")

	_, err := server.validateServiceRootMigration("missing", serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), `service "missing" not found`) {
		t.Fatalf("validateServiceRootMigration error = %v, want missing service", err)
	}
}

func TestServiceSetRootRejectsRunningService(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	newRoot := filepath.Join(t.TempDir(), "new-root")
	withServiceSetRootRunningCheck(t, func(*Server, string) (bool, error) {
		return true, nil
	})

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), `cannot migrate service root while "svc-root" is running`) {
		t.Fatalf("validateServiceRootMigration error = %v, want running service", err)
	}
}

func TestServiceSetSnapshotOnlyDoesNotRequireStoppedService(t *testing.T) {
	server := newTestServer(t)
	name := "svc-snap"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {Name: name, ServiceRoot: "/srv/apps/svc-snap"},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	oldRunning := isServiceRunningForRootMigration
	defer func() { isServiceRunningForRootMigration = oldRunning }()
	isServiceRunningForRootMigration = func(*Server, string) (bool, error) {
		return true, nil
	}
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Snapshots: "off", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, _ := dv.Services().GetOk(name)
	if got := sv.SnapshotPolicy().Enabled().Get(); got {
		t.Fatalf("snapshot enabled = true, want false")
	}
}

func TestServiceSetSnapshotOnlyRejectsMissingService(t *testing.T) {
	server := newTestServer(t)
	name := "missing-snap"
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}

	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Snapshots: "off", SnapshotChange: true})
	if err == nil || !strings.Contains(err.Error(), `service "missing-snap" not found`) {
		t.Fatalf("serviceSetCmdFunc error = %v, want missing service", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	if _, ok := dv.Services().GetOk(name); ok {
		t.Fatalf("service %q was created", name)
	}
}

func TestServiceSetSnapshotInheritClearsOverride(t *testing.T) {
	server := newTestServer(t)
	name := "svc-snap"
	enabled := false
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {Name: name, SnapshotPolicy: &db.SnapshotPolicy{Enabled: &enabled, MaxAge: "72h"}},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Snapshots: "inherit", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc: %v", err)
	}
	dv, _ := server.cfg.DB.Get()
	sv, _ := dv.Services().GetOk(name)
	if sv.SnapshotPolicy().Valid() {
		t.Fatalf("SnapshotPolicy valid = true, want false")
	}
}

func TestServiceSetSnapshotInheritRejectsFieldFlagsBeforeRootMigration(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(filepath.Join(oldRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir old data: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	renameCalled := false
	withServiceSetRootRename(t, func(_, _ string) error {
		renameCalled = true
		return nil
	})
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}

	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{
		ServiceRoot:      newRoot,
		Copy:             true,
		Snapshots:        "inherit",
		SnapshotKeepLast: "bad",
		SnapshotChange:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "--snapshots=inherit cannot be combined with field-level snapshot flags") {
		t.Fatalf("serviceSetCmdFunc error = %v, want mutually exclusive snapshot flags", err)
	}
	if renameCalled {
		t.Fatal("root migration rename was called")
	}
	assertServiceRoot(t, server, name, oldRoot)
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("new root stat error = %v, want not exist", err)
	}
}

func TestServiceSetPublishUpdatesComposeGenerationAndDB(t *testing.T) {
	server := newTestServer(t)
	name := "svc-publish"
	root := t.TempDir()
	composePath := filepath.Join(serviceBinDirForRoot(root), "docker-compose.1.yml")
	if err := os.MkdirAll(filepath.Dir(composePath), 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	composeContent := "services:\n  svc-publish:\n    image: nginx:latest\n    ports:\n      - 80:80\n"
	if err := os.WriteFile(composePath, []byte(composeContent), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:             name,
			ServiceType:      db.ServiceTypeDockerCompose,
			ServiceRoot:      root,
			Generation:       1,
			LatestGeneration: 1,
			Publish:          []string{"80:80"},
			Artifacts: db.ArtifactStore{
				db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): composePath, "latest": composePath}},
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	upCalled := false
	withServiceSetPublishComposeUp(t, func(_ *svc.DockerComposeService) error {
		upCalled = true
		return nil
	})

	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Publish: []string{"80:80", "443:443"}}); err != nil {
		t.Fatalf("serviceSetCmdFunc: %v", err)
	}
	if !upCalled {
		t.Fatal("docker compose up was not called")
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		t.Fatal("missing service")
	}
	if sv.Generation() != 2 || sv.LatestGeneration() != 2 {
		t.Fatalf("generation/latest = %d/%d, want 2/2", sv.Generation(), sv.LatestGeneration())
	}
	if got := sv.Publish().AsSlice(); !reflect.DeepEqual(got, []string{"80:80", "443:443"}) {
		t.Fatalf("Publish = %#v, want updated ports", got)
	}
	artifact, ok := sv.Artifacts().GetOk(db.ArtifactDockerComposeFile)
	if !ok {
		t.Fatal("missing compose artifact")
	}
	newComposePath, ok := artifact.Refs().GetOk(db.Gen(2))
	if !ok {
		t.Fatal("missing compose gen-2 ref")
	}
	if filepath.Clean(newComposePath) == filepath.Clean(composePath) {
		t.Fatalf("new compose path = old path %q, want new durable artifact", newComposePath)
	}
	assertArtifactRef(t, server, name, db.ArtifactDockerComposeFile, "latest", newComposePath)
	newPorts, err := readComposePorts(newComposePath, name)
	if err != nil {
		t.Fatalf("read new compose ports: %v", err)
	}
	if !reflect.DeepEqual(newPorts, []string{"80:80", "443:443"}) {
		t.Fatalf("new compose ports = %#v", newPorts)
	}
	oldPorts, err := readComposePorts(composePath, name)
	if err != nil {
		t.Fatalf("read old compose ports: %v", err)
	}
	if !reflect.DeepEqual(oldPorts, []string{"80:80"}) {
		t.Fatalf("old compose ports = %#v, want unchanged", oldPorts)
	}
}

func TestServiceSetCommandAcceptsPublishOnly(t *testing.T) {
	server := newTestServer(t)
	name := "svc-publish"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:        name,
			ServiceType: db.ServiceTypeDockerCompose,
			Publish:     []string{"80:80"},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}

	if err := execer.serviceCmdFunc([]string{"set", "-p", "80:80"}); err != nil {
		t.Fatalf("serviceCmdFunc publish-only: %v", err)
	}
}

func TestCurrentServiceArtifactPath(t *testing.T) {
	for _, tt := range []struct {
		name             string
		artifact         *db.Artifact
		generation       int
		latestGeneration int
		wantPath         string
		wantOK           bool
	}{
		{
			name:   "nil artifact",
			wantOK: false,
		},
		{
			name: "current generation wins",
			artifact: &db.Artifact{Refs: map[db.ArtifactRef]string{
				db.Gen(1): "/old",
				db.Gen(2): "/current",
				"latest":  "/latest",
			}},
			generation:       2,
			latestGeneration: 3,
			wantPath:         "/current",
			wantOK:           true,
		},
		{
			name: "latest generation fallback",
			artifact: &db.Artifact{Refs: map[db.ArtifactRef]string{
				db.Gen(3): "/latest-gen",
				"latest":  "/latest",
			}},
			generation:       2,
			latestGeneration: 3,
			wantPath:         "/latest-gen",
			wantOK:           true,
		},
		{
			name: "latest alias fallback",
			artifact: &db.Artifact{Refs: map[db.ArtifactRef]string{
				"latest": "/latest",
			}},
			generation:       2,
			latestGeneration: 2,
			wantPath:         "/latest",
			wantOK:           true,
		},
		{
			name: "missing refs",
			artifact: &db.Artifact{Refs: map[db.ArtifactRef]string{
				db.Gen(1): "/old",
			}},
			generation:       2,
			latestGeneration: 3,
			wantOK:           false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotOK := currentServiceArtifactPath(tt.artifact, tt.generation, tt.latestGeneration)
			if gotPath != tt.wantPath || gotOK != tt.wantOK {
				t.Fatalf("currentServiceArtifactPath() = %q, %v, want %q, %v", gotPath, gotOK, tt.wantPath, tt.wantOK)
			}
		})
	}
}

func TestServiceSetPublishRejectsOmittedExistingPortWithoutReset(t *testing.T) {
	server := newTestServer(t)
	name := "svc-publish"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:        name,
			ServiceType: db.ServiceTypeDockerCompose,
			Publish:     []string{"80:80"},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	upCalled := false
	withServiceSetPublishComposeUp(t, func(_ *svc.DockerComposeService) error {
		upCalled = true
		return nil
	})

	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Publish: []string{"443:443"}})
	if err == nil || !strings.Contains(err.Error(), "changing published ports would remove existing mappings") || !strings.Contains(err.Error(), "80:80") {
		t.Fatalf("serviceSetCmdFunc error = %v, want omitted port error", err)
	}
	if upCalled {
		t.Fatal("docker compose up was called after rejected publish change")
	}
}

func TestServiceSetPublishResetReplacesAndClearsPorts(t *testing.T) {
	for _, tt := range []struct {
		name      string
		flags     cli.ServiceSetFlags
		wantPorts []string
	}{
		{
			name:      "replace",
			flags:     cli.ServiceSetFlags{Publish: []string{"443:443"}, PublishReset: true},
			wantPorts: []string{"443:443"},
		},
		{
			name:      "clear",
			flags:     cli.ServiceSetFlags{PublishReset: true},
			wantPorts: []string{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			serviceName := "svc-publish"
			root := t.TempDir()
			composePath := filepath.Join(serviceBinDirForRoot(root), "docker-compose.1.yml")
			if err := os.MkdirAll(filepath.Dir(composePath), 0o755); err != nil {
				t.Fatalf("mkdir bin dir: %v", err)
			}
			if err := os.WriteFile(composePath, []byte("services:\n  svc-publish:\n    image: nginx:latest\n    ports:\n      - 80:80\n"), 0o644); err != nil {
				t.Fatalf("write compose: %v", err)
			}
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
				serviceName: {
					Name:             serviceName,
					ServiceType:      db.ServiceTypeDockerCompose,
					ServiceRoot:      root,
					Generation:       1,
					LatestGeneration: 1,
					Publish:          []string{"80:80"},
					Artifacts: db.ArtifactStore{
						db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): composePath, "latest": composePath}},
					},
				},
			}}); err != nil {
				t.Fatalf("DB.Set: %v", err)
			}
			upCalled := false
			withServiceSetPublishComposeUp(t, func(_ *svc.DockerComposeService) error {
				upCalled = true
				return nil
			})

			execer := &ttyExecer{s: server, sn: serviceName, rw: &bytes.Buffer{}, isPty: false}
			if err := execer.serviceSetCmdFunc(tt.flags); err != nil {
				t.Fatalf("serviceSetCmdFunc: %v", err)
			}
			if !upCalled {
				t.Fatal("docker compose up was not called")
			}
			dv, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatalf("DB.Get: %v", err)
			}
			sv, _ := dv.Services().GetOk(serviceName)
			if got := sv.Publish().AsSlice(); len(got) != len(tt.wantPorts) || (len(tt.wantPorts) != 0 && !reflect.DeepEqual(got, tt.wantPorts)) {
				t.Fatalf("Publish = %#v, want %#v", got, tt.wantPorts)
			}
			artifact, _ := sv.Artifacts().GetOk(db.ArtifactDockerComposeFile)
			newComposePath, _ := artifact.Refs().GetOk(db.Gen(2))
			gotPorts, err := readComposePorts(newComposePath, serviceName)
			if err != nil {
				t.Fatalf("read compose ports: %v", err)
			}
			if len(gotPorts) != len(tt.wantPorts) || (len(tt.wantPorts) != 0 && !reflect.DeepEqual(gotPorts, tt.wantPorts)) {
				t.Fatalf("compose ports = %#v, want %#v", gotPorts, tt.wantPorts)
			}
		})
	}
}

func TestServiceSetPublishFallsBackToComposePorts(t *testing.T) {
	server := newTestServer(t)
	name := "svc-publish"
	root := t.TempDir()
	composePath := filepath.Join(serviceBinDirForRoot(root), "docker-compose.1.yml")
	if err := os.MkdirAll(filepath.Dir(composePath), 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	if err := os.WriteFile(composePath, []byte("services:\n  svc-publish:\n    image: nginx:latest\n    ports:\n      - 80:80\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:             name,
			ServiceType:      db.ServiceTypeDockerCompose,
			ServiceRoot:      root,
			Generation:       1,
			LatestGeneration: 1,
			Artifacts: db.ArtifactStore{
				db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): composePath, "latest": composePath}},
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Publish: []string{"443:443"}})
	if err == nil || !strings.Contains(err.Error(), "80:80") {
		t.Fatalf("serviceSetCmdFunc error = %v, want compose fallback omitted port error", err)
	}
}

func TestServiceSetPublishRejectsNonComposeService(t *testing.T) {
	server := newTestServer(t)
	name := "svc-systemd"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {Name: name, ServiceType: db.ServiceTypeSystemd},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{Publish: []string{"80:80"}})
	if err == nil || !strings.Contains(err.Error(), `service "svc-systemd" is not a docker compose service`) {
		t.Fatalf("serviceSetCmdFunc error = %v, want non-compose rejection", err)
	}
}

func TestServiceSetSnapshotFieldInheritClearsOnlyField(t *testing.T) {
	server := newTestServer(t)
	name := "svc-snap"
	keep := 3
	required := false
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {Name: name, SnapshotPolicy: &db.SnapshotPolicy{KeepLast: &keep, MaxAge: "72h", Required: &required, Events: []string{"run"}}},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{SnapshotKeepLast: "inherit", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc keep-last inherit: %v", err)
	}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{SnapshotMaxAge: "inherit", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc max-age inherit: %v", err)
	}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{SnapshotRequired: "inherit", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc required inherit: %v", err)
	}
	if err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{SnapshotEvents: "inherit", SnapshotChange: true}); err != nil {
		t.Fatalf("serviceSetCmdFunc events inherit: %v", err)
	}
	dv, _ := server.cfg.DB.Get()
	sv, _ := dv.Services().GetOk(name)
	policy := sv.SnapshotPolicy()
	if policy.KeepLast().Valid() {
		t.Fatalf("KeepLast valid = true, want false")
	}
	if got := policy.MaxAge(); got != "" {
		t.Fatalf("MaxAge = %q, want empty", got)
	}
	if policy.Required().Valid() {
		t.Fatalf("Required valid = true, want false")
	}
	if got := policy.Events().Len(); got != 0 {
		t.Fatalf("Events len = %d, want 0", got)
	}
}

func TestServiceSetRootWithInvalidSnapshotDoesNotMigrateRoot(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(filepath.Join(oldRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir old data: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	renameCalled := false
	withServiceSetRootRename(t, func(_, _ string) error {
		renameCalled = true
		return nil
	})
	execer := &ttyExecer{s: server, sn: name, rw: &bytes.Buffer{}, isPty: false}

	err := execer.serviceSetCmdFunc(cli.ServiceSetFlags{
		ServiceRoot:      newRoot,
		Copy:             true,
		SnapshotKeepLast: "bad",
		SnapshotChange:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "--snapshot-keep-last must be a positive integer or inherit") {
		t.Fatalf("serviceSetCmdFunc error = %v, want snapshot validation", err)
	}
	if renameCalled {
		t.Fatal("root migration rename was called")
	}
	assertServiceRoot(t, server, name, oldRoot)
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("new root stat error = %v, want not exist", err)
	}
}

func TestServiceSetRootRejectsMissingParent(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	withServiceSetRootStopped(t)
	newRoot := filepath.Join(t.TempDir(), "missing-parent", "new-root")

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), "service root parent") {
		t.Fatalf("validateServiceRootMigration error = %v, want missing parent", err)
	}
}

func TestServiceSetRootRejectsNonEmptyDestination(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	withServiceSetRootStopped(t)
	newRoot := filepath.Join(t.TempDir(), "new-root")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatalf("mkdir new root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "file.txt"), []byte("occupied"), 0o644); err != nil {
		t.Fatalf("write destination file: %v", err)
	}

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("validateServiceRootMigration error = %v, want non-empty destination", err)
	}
}

func TestServiceSetRootRejectsNestedRoots(t *testing.T) {
	for _, tt := range []struct {
		name  string
		roots func(string) (oldRoot, newRoot string)
	}{
		{
			name: "new inside old",
			roots: func(base string) (string, string) {
				oldRoot := filepath.Join(base, "old-root")
				return oldRoot, filepath.Join(oldRoot, "nested")
			},
		},
		{
			name: "old inside new",
			roots: func(base string) (string, string) {
				newRoot := filepath.Join(base, "parent")
				return filepath.Join(newRoot, "old-root"), newRoot
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			oldRoot, newRoot := tt.roots(t.TempDir())
			name := seedServiceWithRoot(t, server, oldRoot, "")
			withServiceSetRootStopped(t)
			if err := os.MkdirAll(oldRoot, 0o755); err != nil {
				t.Fatalf("mkdir old root: %v", err)
			}

			_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
			if err == nil || !strings.Contains(err.Error(), "nested") {
				t.Fatalf("validateServiceRootMigration error = %v, want nested root rejection", err)
			}
		})
	}
}

func TestServiceSetRootNonTTYRequiresCopyOrEmpty(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	withServiceSetRootStopped(t)
	newRoot := filepath.Join(t.TempDir(), "new-root")
	execer := &ttyExecer{
		ctx:   context.Background(),
		s:     server,
		sn:    name,
		rw:    &bytes.Buffer{},
		isPty: false,
	}

	err := execer.serviceCmdFunc([]string{"set", "--service-root", newRoot})
	if err == nil || !strings.Contains(err.Error(), "requires --copy or --empty") {
		t.Fatalf("serviceSetCmdFunc error = %v, want non-TTY prompt error", err)
	}
}

func TestServiceSetZFSNonTTYRequiresModeBeforeDatasetCreate(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	newRoot := filepath.Join(t.TempDir(), "new-root")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot},
	})
	server.zfsRunner = runner.Run
	execer := &ttyExecer{
		ctx:   context.Background(),
		s:     server,
		sn:    name,
		rw:    &bytes.Buffer{},
		isPty: false,
	}

	err := execer.serviceCmdFunc([]string{"set", "--service-root", "tank/apps/svc", "--zfs"})
	if err == nil || !strings.Contains(err.Error(), "requires --copy or --empty") {
		t.Fatalf("serviceSetCmdFunc error = %v, want non-TTY prompt error", err)
	}
	if runner["tank/apps/svc"].Exists {
		t.Fatal("dataset was created before non-TTY mode validation")
	}
}

func TestServiceSetRootTTYDeclineCreatesEmptyRootWithoutCopy(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatalf("mkdir old root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	var out bytes.Buffer
	execer := &ttyExecer{
		ctx:   context.Background(),
		s:     server,
		sn:    name,
		rw:    readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
		isPty: true,
	}

	if err := execer.serviceCmdFunc([]string{"set", "--service-root", newRoot}); err != nil {
		t.Fatalf("serviceCmdFunc: %v", err)
	}
	if !strings.Contains(out.String(), "Copy existing service files") {
		t.Fatalf("prompt output = %q, want copy prompt", out.String())
	}
	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(oldRoot, "old.txt"), "old")
	assertServiceLayout(t, newRoot)
	if _, err := os.Stat(filepath.Join(newRoot, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("copied old file stat error = %v, want not exist", err)
	}
}

func TestServiceSetRootCopyStagesRenamesUpdatesDBAndLeavesOldRoot(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(filepath.Join(oldRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir old data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "data", "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(oldRoot, "data", "payload.txt"), "payload")
	assertFileContents(t, filepath.Join(newRoot, "data", "payload.txt"), "payload")
	assertServiceLayout(t, newRoot)
	assertNoServiceSetStages(t, filepath.Dir(newRoot))
}

func TestServiceSetRootCopyRewritesRootBoundArtifacts(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatalf("ensure old root: %v", err)
	}
	composePath := filepath.Join(serviceBinDirForRoot(oldRoot), "docker-compose.7.yml")
	oldConfigPath := filepath.Join(serviceDataDirForRoot(oldRoot), "config")
	oldLongPath := filepath.Join(serviceDataDirForRoot(oldRoot), "long")
	oldKeyValuePath := filepath.Join(serviceDataDirForRoot(oldRoot), "keyvalue")
	oldBackupPath := oldRoot + "-backup"
	composeContent := "services:\n  svc-root:\n    volumes:\n      - " + oldConfigPath + ":/config\n      - config:/named-config\n      - type: bind\n        source: " + oldLongPath + "\n        target: /long\n      - type=bind,source=" + oldKeyValuePath + ",target=/keyvalue\n      - " + oldBackupPath + ":/backup\n"
	if err := os.WriteFile(composePath, []byte(composeContent), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	tsUnitPath := filepath.Join(serviceBinDirForRoot(oldRoot), "yeet-svc-root-ts.service")
	tsUnitContent := "[Service]\nExecStart=" + filepath.Join(serviceRunDirForRoot(oldRoot), "tailscaled") + " --socket=" + filepath.Join(serviceRunDirForRoot(oldRoot), "tailscaled.sock") + "\nWorkingDirectory=" + filepath.Join(oldRoot, "tailscale") + "\nEnvironment=BACKUP=" + oldBackupPath + "\n"
	if err := os.MkdirAll(filepath.Join(oldRoot, "tailscale"), 0o755); err != nil {
		t.Fatalf("mkdir tailscale: %v", err)
	}
	if err := os.WriteFile(tsUnitPath, []byte(tsUnitContent), 0o644); err != nil {
		t.Fatalf("write ts unit: %v", err)
	}
	systemdUnitPath := filepath.Join(serviceBinDirForRoot(oldRoot), "svc-root.service")
	systemdUnitContent := "[Service]\nExecStart=" + filepath.Join(serviceRunDirForRoot(oldRoot), "svc-root") + "\nEnvironmentFile=-" + filepath.Join(serviceRunDirForRoot(oldRoot), "env") + "\n"
	if err := os.WriteFile(systemdUnitPath, []byte(systemdUnitContent), 0o644); err != nil {
		t.Fatalf("write systemd unit: %v", err)
	}
	envPath := filepath.Join(serviceBinDirForRoot(oldRoot), "app.env")
	envContent := "APP_ROOT=" + oldRoot + "\n"
	if err := os.WriteFile(envPath, []byte(envContent), 0o644); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 7
		s.LatestGeneration = 7
		s.Artifacts = db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(7): composePath,
					"latest":  composePath,
				},
			},
			db.ArtifactTSService: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(7): tsUnitPath,
					"latest":  tsUnitPath,
				},
			},
			db.ArtifactSystemdUnit: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(7): systemdUnitPath,
					"latest":  systemdUnitPath,
				},
			},
			db.ArtifactEnvFile: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(7): envPath,
					"latest":  envPath,
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("mutate artifacts: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	newEnvPath := filepath.Join(serviceBinDirForRoot(newRoot), "app.env")
	downCalls := 0
	withServiceSetRootDockerDown(t, func(_ *Server, service *db.Service, root string) error {
		downCalls++
		if service.Name != name {
			t.Fatalf("docker down service = %q, want %q", service.Name, name)
		}
		if filepath.Clean(root) != filepath.Clean(oldRoot) {
			t.Fatalf("docker down root = %q, want %q", root, oldRoot)
		}
		return nil
	})
	installCalls := 0
	withServiceSetRootSystemdInstall(t, func(_ *Server, oldService, updatedService *db.Service, root string) error {
		installCalls++
		if filepath.Clean(root) != filepath.Clean(newRoot) {
			t.Fatalf("systemd install root = %q, want %q", root, newRoot)
		}
		if got := oldService.Artifacts[db.ArtifactTSService].Refs[db.Gen(7)]; filepath.Clean(got) != filepath.Clean(tsUnitPath) {
			t.Fatalf("old systemd artifact path = %q, want %q", got, tsUnitPath)
		}
		if got := updatedService.Artifacts[db.ArtifactTSService].Refs[db.Gen(7)]; filepath.Clean(got) != filepath.Clean(filepath.Join(serviceBinDirForRoot(newRoot), "yeet-svc-root-ts.service")) {
			t.Fatalf("updated systemd artifact path = %q, want new root", got)
		}
		return nil
	})

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	newComposePath := filepath.Join(serviceBinDirForRoot(newRoot), "docker-compose.7.yml")
	newTSUnitPath := filepath.Join(serviceBinDirForRoot(newRoot), "yeet-svc-root-ts.service")
	newSystemdUnitPath := filepath.Join(serviceBinDirForRoot(newRoot), "svc-root.service")
	assertArtifactRef(t, server, name, db.ArtifactDockerComposeFile, db.Gen(7), newComposePath)
	assertArtifactRef(t, server, name, db.ArtifactDockerComposeFile, "latest", newComposePath)
	assertArtifactRef(t, server, name, db.ArtifactTSService, db.Gen(7), newTSUnitPath)
	assertArtifactRef(t, server, name, db.ArtifactTSService, "latest", newTSUnitPath)
	assertArtifactRef(t, server, name, db.ArtifactSystemdUnit, db.Gen(7), newSystemdUnitPath)
	assertArtifactRef(t, server, name, db.ArtifactSystemdUnit, "latest", newSystemdUnitPath)
	assertArtifactRef(t, server, name, db.ArtifactEnvFile, db.Gen(7), newEnvPath)
	assertArtifactRef(t, server, name, db.ArtifactEnvFile, "latest", newEnvPath)
	assertFileContains(t, newComposePath, filepath.Join(serviceDataDirForRoot(newRoot), "config"))
	assertFileContains(t, newComposePath, filepath.Join(serviceDataDirForRoot(newRoot), "long"))
	assertFileContains(t, newComposePath, filepath.Join(serviceDataDirForRoot(newRoot), "keyvalue"))
	assertFileContains(t, newComposePath, "config:/named-config")
	assertFileContains(t, newComposePath, oldBackupPath)
	assertFileNotContains(t, newComposePath, oldConfigPath)
	assertFileNotContains(t, newComposePath, oldLongPath)
	assertFileNotContains(t, newComposePath, oldKeyValuePath)
	assertFileContains(t, newTSUnitPath, filepath.Join(serviceRunDirForRoot(newRoot), "tailscaled"))
	assertFileContains(t, newTSUnitPath, filepath.Join(serviceRunDirForRoot(newRoot), "tailscaled.sock"))
	assertFileContains(t, newTSUnitPath, filepath.Join(newRoot, "tailscale"))
	assertFileContains(t, newTSUnitPath, oldBackupPath)
	assertFileContains(t, newSystemdUnitPath, "ExecStart="+filepath.Join(serviceRunDirForRoot(newRoot), "svc-root"))
	assertFileContains(t, newSystemdUnitPath, "EnvironmentFile=-"+filepath.Join(serviceRunDirForRoot(newRoot), "env"))
	assertFileNotContains(t, newSystemdUnitPath, filepath.Join(serviceRunDirForRoot(oldRoot), "env"))
	assertFileContents(t, newEnvPath, envContent)
	assertFileContents(t, composePath, composeContent)
	assertFileContents(t, tsUnitPath, tsUnitContent)
	assertFileContents(t, systemdUnitPath, systemdUnitContent)
	assertFileContents(t, envPath, envContent)
	if downCalls != 1 {
		t.Fatalf("docker down calls = %d, want 1", downCalls)
	}
	if installCalls != 1 {
		t.Fatalf("systemd install calls = %d, want 1", installCalls)
	}
}

func TestServiceSetRootMigrationUsesFreshValidatedRoot(t *testing.T) {
	server := newTestServer(t)
	staleRoot := filepath.Join(t.TempDir(), "stale-root")
	name := seedServiceWithRoot(t, server, staleRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(staleRoot, 0o755); err != nil {
		t.Fatalf("mkdir stale root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale payload: %v", err)
	}
	currentRoot := filepath.Join(t.TempDir(), "current-root")
	if err := os.MkdirAll(currentRoot, 0o755); err != nil {
		t.Fatalf("mkdir current root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(currentRoot, "current.txt"), []byte("current"), 0o644); err != nil {
		t.Fatalf("write current payload: %v", err)
	}
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Services[name].ServiceRoot = currentRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate current service root: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(newRoot, "current.txt"), "current")
	if _, err := os.Stat(filepath.Join(newRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale payload stat error = %v, want not exist", err)
	}
}

func TestServiceSetRootRenameFailureLeavesDBOldRoot(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatalf("mkdir old root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	wantErr := errors.New("rename failed")
	withServiceSetRootRename(t, func(string, string) error {
		return wantErr
	})

	err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy)
	if !errors.Is(err, wantErr) {
		t.Fatalf("migrateServiceRoot error = %v, want %v", err, wantErr)
	}
	assertServiceRoot(t, server, name, oldRoot)
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("new root stat error = %v, want not exist", err)
	}
	assertNoServiceSetStages(t, filepath.Dir(newRoot))
}

func TestServiceSetRootEmptyCreatesLayoutUpdatesDBWithoutCopyAndLeavesOldRoot(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatalf("mkdir old root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationEmpty); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(oldRoot, "payload.txt"), "payload")
	assertServiceLayout(t, newRoot)
	if _, err := os.Stat(filepath.Join(newRoot, "payload.txt")); !os.IsNotExist(err) {
		t.Fatalf("copied payload stat error = %v, want not exist", err)
	}
}

func TestServiceSetRootEmptyClearsOldRootArtifacts(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatalf("ensure old root: %v", err)
	}
	composePath := filepath.Join(serviceBinDirForRoot(oldRoot), "docker-compose.3.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 3
		s.LatestGeneration = 3
		s.Artifacts = db.ArtifactStore{
			db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(3): composePath, "latest": composePath}},
		}
		return nil
	}); err != nil {
		t.Fatalf("mutate artifacts: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	downCalls := 0
	withServiceSetRootDockerDown(t, func(_ *Server, service *db.Service, root string) error {
		downCalls++
		if service.Name != name {
			t.Fatalf("docker down service = %q, want %q", service.Name, name)
		}
		if filepath.Clean(root) != filepath.Clean(oldRoot) {
			t.Fatalf("docker down root = %q, want %q", root, oldRoot)
		}
		return nil
	})
	withServiceSetRootSystemdInstall(t, func(_ *Server, _, _ *db.Service, _ string) error {
		t.Fatal("systemd install should not run for compose-only artifacts")
		return nil
	})

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationEmpty); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertServiceLayout(t, newRoot)
	assertNoArtifacts(t, server, name)
	assertFileContents(t, composePath, "services: {}\n")
	if downCalls != 1 {
		t.Fatalf("docker down calls = %d, want 1", downCalls)
	}
}

func TestServiceSetRootEmptyUninstallsOldSystemdAndRefreshesDockerPrereqs(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatalf("ensure old root: %v", err)
	}
	composePath := filepath.Join(serviceBinDirForRoot(oldRoot), "docker-compose.4.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	netnsPath := filepath.Join(serviceBinDirForRoot(oldRoot), "yeet-svc-root-ns.service")
	if err := os.WriteFile(netnsPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
		t.Fatalf("write netns unit: %v", err)
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 4
		s.LatestGeneration = 4
		s.Artifacts = db.ArtifactStore{
			db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(4): composePath}},
			db.ArtifactNetNSService:      {Refs: map[db.ArtifactRef]string{db.Gen(4): netnsPath}},
		}
		return nil
	}); err != nil {
		t.Fatalf("mutate artifacts: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	withServiceSetRootDockerDown(t, func(_ *Server, _ *db.Service, _ string) error {
		return nil
	})
	uninstallCalls := 0
	withServiceSetRootSystemdUninstall(t, func(_ *Server, oldService *db.Service, root string) error {
		uninstallCalls++
		if filepath.Clean(root) != filepath.Clean(oldRoot) {
			t.Fatalf("systemd uninstall root = %q, want %q", root, oldRoot)
		}
		if got := oldService.Artifacts[db.ArtifactNetNSService].Refs[db.Gen(4)]; filepath.Clean(got) != filepath.Clean(netnsPath) {
			t.Fatalf("old netns artifact path = %q, want %q", got, netnsPath)
		}
		return nil
	})
	withServiceSetRootSystemdInstall(t, func(_ *Server, _, _ *db.Service, _ string) error {
		t.Fatal("systemd install should not run for empty migration")
		return nil
	})
	prereqCalls := 0
	withServiceSetRootDockerPrereqs(t, func(s *Server) error {
		prereqCalls++
		units, err := s.dockerNetNSServiceUnits()
		if err != nil {
			return err
		}
		for _, unit := range units {
			if unit == serviceNetNSUnitName(name) {
				t.Fatalf("docker prereqs still include %q after DB update", unit)
			}
		}
		return nil
	})

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationEmpty); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertNoArtifacts(t, server, name)
	if uninstallCalls != 1 {
		t.Fatalf("systemd uninstall calls = %d, want 1", uninstallCalls)
	}
	if prereqCalls != 1 {
		t.Fatalf("docker prereq calls = %d, want 1", prereqCalls)
	}
}

func TestServiceSetRootCopyPreservesModeMtimeAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink metadata test is Unix-oriented")
	}
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	dataDir := filepath.Join(oldRoot, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	filePath := filepath.Join(dataDir, "payload.txt")
	if err := os.WriteFile(filePath, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(filePath, mtime, mtime); err != nil {
		t.Fatalf("chtimes payload: %v", err)
	}
	if err := os.Symlink("payload.txt", filepath.Join(dataDir, "payload.link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	copied := filepath.Join(newRoot, "data", "payload.txt")
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("copied mode = %o, want 0640", info.Mode().Perm())
	}
	if info.ModTime().Unix() != mtime.Unix() {
		t.Fatalf("copied mtime = %v, want %v", info.ModTime(), mtime)
	}
	target, err := os.Readlink(filepath.Join(newRoot, "data", "payload.link"))
	if err != nil {
		t.Fatalf("readlink copied symlink: %v", err)
	}
	if target != "payload.txt" {
		t.Fatalf("copied symlink target = %q, want payload.txt", target)
	}
}

func TestServiceSetZFSMigrationCopy(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "config.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	withServiceSetRootRename(t, func(_, _ string) error {
		return errors.New("zfs migration must not rename over mountpoint")
	})
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot, Exists: true},
	}).Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}
	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}
	assertServiceRoot(t, server, name, newRoot)
	assertServiceRootZFS(t, server, name, "tank/apps/svc")
	rootInfoAfter, err := os.Stat(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(rootInfo, rootInfoAfter) {
		t.Fatal("ZFS mountpoint was replaced during copy migration")
	}
	if got, err := os.ReadFile(filepath.Join(serviceDataDirForRoot(newRoot), "config.txt")); err != nil || string(got) != "ok" {
		t.Fatalf("copied config = %q err=%v, want ok nil", got, err)
	}
}

func TestServiceSetZFSMigrationCommandResolvesDatasetOnce(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "config.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot, Exists: true},
	})
	var calls [][]string
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return runner.Run(ctx, args...)
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}
	execer := &ttyExecer{
		ctx:   context.Background(),
		s:     server,
		sn:    name,
		rw:    &bytes.Buffer{},
		isPty: false,
	}

	if err := execer.serviceCmdFunc([]string{"set", "--service-root", "tank/apps/svc", "--zfs", "--copy"}); err != nil {
		t.Fatalf("serviceCmdFunc: %v", err)
	}

	wantCalls := [][]string{
		{"list", "-H", "-o", "name", "tank/apps/svc"},
		{"get", "-H", "-o", "value", "mountpoint", "tank/apps/svc"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("zfs calls = %#v, want %#v", calls, wantCalls)
	}
	assertServiceRoot(t, server, name, newRoot)
	assertServiceRootZFS(t, server, name, "tank/apps/svc")
}

func TestServiceSetRejectsSameZFSDatasetBeforeZFSCommands(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "stale-root")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "zfs should not be called", errZFSCommandFailed
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		s.ServiceRootZFS = "tank/apps/svc"
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: " tank/apps/svc ", ZFS: true})
	if err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("validateServiceRootMigration error = %v, want same dataset rejection", err)
	}
	if len(calls) != 0 {
		t.Fatalf("zfs calls = %#v, want none", calls)
	}
}

func TestServiceSetZFSMigrationCreatesDatasetAndLeavesDBOnCopyFailure(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old-missing")
	parentRoot := filepath.Join(t.TempDir(), "apps")
	newRoot := filepath.Join(parentRoot, "svc")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps":     {Mountpoint: parentRoot, Exists: true},
		"tank/apps/svc": {Mountpoint: newRoot},
	})
	server.zfsRunner = runner.Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}
	err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true}, serviceRootMigrationCopy)
	if err == nil || !strings.Contains(err.Error(), "archive service root") {
		t.Fatalf("migrateServiceRoot error = %v, want archive failure", err)
	}
	if !runner["tank/apps/svc"].Exists {
		t.Fatal("dataset was not created before migration failure")
	}
	assertServiceRoot(t, server, name, oldRoot)
	assertServiceRootZFS(t, server, name, "")
}

func TestServiceSetRejectsMissingZFSChildNestedUnderOldRootBeforeCreate(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "apps")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps":     {Mountpoint: oldRoot, Exists: true},
		"tank/apps/svc": {Mountpoint: filepath.Join(oldRoot, "svc")},
	})
	server.zfsRunner = runner.Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true})
	if err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("validateServiceRootMigration error = %v, want nested root rejection", err)
	}
	if runner["tank/apps/svc"].Exists {
		t.Fatal("child dataset was created before nested root rejection")
	}
}

func TestServiceSetRejectsMissingZFSChildSamePathDifferentRootTypeBeforeCreate(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	parentRoot := filepath.Join(t.TempDir(), "apps")
	oldRoot := filepath.Join(parentRoot, "svc")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps":     {Mountpoint: parentRoot, Exists: true},
		"tank/apps/svc": {Mountpoint: oldRoot},
	})
	server.zfsRunner = runner.Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true})
	if err == nil || !strings.Contains(err.Error(), "already uses service root") || !strings.Contains(err.Error(), "different root type") {
		t.Fatalf("validateServiceRootMigration error = %v, want same path different root type rejection", err)
	}
	if runner["tank/apps/svc"].Exists {
		t.Fatal("child dataset was created before same path different root type rejection")
	}
}

func TestServiceSetZFSMigrationEmptyUsesMountedRoot(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	withServiceSetRootRename(t, func(_, _ string) error {
		return errors.New("zfs migration must not rename over mountpoint")
	})
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot, Exists: true},
	}).Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true}, serviceRootMigrationEmpty); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertServiceRootZFS(t, server, name, "tank/apps/svc")
	assertServiceLayout(t, newRoot)
	rootInfoAfter, err := os.Stat(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(rootInfo, rootInfoAfter) {
		t.Fatal("ZFS mountpoint was replaced during empty migration")
	}
}

func TestServiceSetMigrationFromZFSToFilesystemClearsDataset(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "snapshot":
			return "", "", nil
		case "list":
			return "", "", nil
		default:
			return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
		}
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		s.ServiceRootZFS = "tank/apps/svc"
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationEmpty); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertServiceRootZFS(t, server, name, "")
}

func TestServiceRootMigrationSnapshotsOldZFSDatasetBeforeMaterializing(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "config.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.ServiceRoot = oldRoot
		s.ServiceRootZFS = "tank/apps/svc"
		s.Generation = 2
		s.LatestGeneration = 2
		return nil
	}); err != nil {
		t.Fatalf("mutate service: %v", err)
	}

	var order []string
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "snapshot":
			order = append(order, "snapshot")
			got := args[len(args)-1]
			if !strings.HasPrefix(got, "tank/apps/svc@yeet-") {
				t.Fatalf("snapshot target = %q, want old dataset snapshot", got)
			}
			return "", "", nil
		case "list":
			return "", "", nil
		default:
			return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
		}
	}
	withServiceSetRootRename(t, func(oldPath, newPath string) error {
		order = append(order, "rename")
		return os.Rename(oldPath, newPath)
	})

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	want := []string{"snapshot", "rename"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
	assertServiceRoot(t, server, name, newRoot)
	assertServiceRootZFS(t, server, name, "")
}

func TestServiceRootMigrationReportsRecoverySnapshotOnFailure(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new-file")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.ServiceRoot = oldRoot
		s.ServiceRootZFS = "tank/apps/svc"
		s.Generation = 2
		s.LatestGeneration = 2
		return nil
	}); err != nil {
		t.Fatalf("mutate service: %v", err)
	}
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "snapshot":
			return "", "", nil
		case "list":
			return "", "", nil
		default:
			return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
		}
	}
	var out bytes.Buffer
	plan := serviceRootMigrationPlan{
		ServiceName: name,
		OldRoot:     oldRoot,
		OldRootZFS:  "tank/apps/svc",
		NewRoot:     newRoot,
	}
	err := server.migrateServiceRootWithPlanWriter(plan, serviceRootMigrationEmpty, &out)
	if err == nil || !strings.Contains(err.Error(), "is a file") {
		t.Fatalf("migrateServiceRootWithPlanWriter error = %v, want file error", err)
	}
	if got := out.String(); !strings.Contains(got, "recovery snapshot: tank/apps/svc@yeet-") {
		t.Fatalf("output = %q, want recovery snapshot", got)
	}
}

func TestServiceSetRejectsNoopAcrossRootTypes(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	root := filepath.Join(t.TempDir(), "svc")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: root, Exists: true},
	}).Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = root
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}
	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true})
	if err == nil || !strings.Contains(err.Error(), "already uses service root") {
		t.Fatalf("validateServiceRootMigration error = %v, want same path different identity rejection", err)
	}
}

func seedServiceWithRoot(t *testing.T, server *Server, root string, nameSuffix string) string {
	t.Helper()
	name := "svc-root" + nameSuffix
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.ServiceRoot = root
		return nil
	}); err != nil {
		t.Fatalf("seed service %q: %v", name, err)
	}
	return name
}

func withServiceSetRootStopped(t *testing.T) {
	t.Helper()
	withServiceSetRootRunningCheck(t, func(*Server, string) (bool, error) {
		return false, nil
	})
}

func withServiceSetRootRunningCheck(t *testing.T, f func(*Server, string) (bool, error)) {
	t.Helper()
	old := isServiceRunningForRootMigration
	isServiceRunningForRootMigration = f
	t.Cleanup(func() {
		isServiceRunningForRootMigration = old
	})
}

func withServiceSetRootRename(t *testing.T, f func(string, string) error) {
	t.Helper()
	old := renameServiceRoot
	renameServiceRoot = f
	t.Cleanup(func() {
		renameServiceRoot = old
	})
}

func withServiceSetRootDockerDown(t *testing.T, f func(*Server, *db.Service, string) error) {
	t.Helper()
	old := downDockerComposeForRootMigration
	downDockerComposeForRootMigration = f
	t.Cleanup(func() {
		downDockerComposeForRootMigration = old
	})
}

func withServiceSetRootSystemdInstall(t *testing.T, f func(*Server, *db.Service, *db.Service, string) error) {
	t.Helper()
	old := installSystemdForRootMigration
	installSystemdForRootMigration = f
	t.Cleanup(func() {
		installSystemdForRootMigration = old
	})
}

func withServiceSetRootSystemdUninstall(t *testing.T, f func(*Server, *db.Service, string) error) {
	t.Helper()
	old := uninstallSystemdForRootMigration
	uninstallSystemdForRootMigration = f
	t.Cleanup(func() {
		uninstallSystemdForRootMigration = old
	})
}

func withServiceSetRootDockerPrereqs(t *testing.T, f func(*Server) error) {
	t.Helper()
	old := installDockerPrereqs
	installDockerPrereqs = f
	t.Cleanup(func() {
		installDockerPrereqs = old
	})
}

func withServiceSetPublishComposeUp(t *testing.T, f func(*svc.DockerComposeService) error) {
	t.Helper()
	old := upDockerComposeForServiceSet
	upDockerComposeForServiceSet = f
	t.Cleanup(func() {
		upDockerComposeForServiceSet = old
	})
}

func assertServiceRoot(t *testing.T, server *Server, name, want string) {
	t.Helper()
	got, err := server.serviceRootDir(name)
	if err != nil {
		t.Fatalf("serviceRootDir: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("service root = %q, want %q", got, want)
	}
}

func assertServiceRootZFS(t *testing.T, server *Server, name, want string) {
	t.Helper()
	d, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	svc, ok := d.Services().GetOk(name)
	if !ok {
		t.Fatalf("service %q missing", name)
	}
	if got := svc.ServiceRootZFS(); got != want {
		t.Fatalf("ServiceRootZFS = %q, want %q", got, want)
	}
}

func assertArtifactRef(t *testing.T, server *Server, name string, artifact db.ArtifactName, ref db.ArtifactRef, want string) {
	t.Helper()
	d, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	svc, ok := d.Services().GetOk(name)
	if !ok {
		t.Fatalf("service %q missing", name)
	}
	gotArtifact, ok := svc.Artifacts().GetOk(artifact)
	if !ok {
		t.Fatalf("artifact %q missing", artifact)
	}
	got, ok := gotArtifact.Refs().GetOk(ref)
	if !ok {
		t.Fatalf("artifact %q ref %q missing", artifact, ref)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("artifact %q ref %q = %q, want %q", artifact, ref, got, want)
	}
}

func assertNoArtifacts(t *testing.T, server *Server, name string) {
	t.Helper()
	d, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	svc, ok := d.Services().GetOk(name)
	if !ok {
		t.Fatalf("service %q missing", name)
	}
	if got := svc.Artifacts().Len(); got != 0 {
		t.Fatalf("artifact count = %d, want 0", got)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s contents = %q, want %q", path, string(got), want)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(got), want) {
		t.Fatalf("%s contents = %q, want substring %q", path, string(got), want)
	}
}

func assertFileNotContains(t *testing.T, path, unwanted string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(got), unwanted) {
		t.Fatalf("%s contents = %q, want no substring %q", path, string(got), unwanted)
	}
}

func assertServiceLayout(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"bin", "data", "env", "run"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("stat layout dir %s: %v", name, err)
		}
		if !info.IsDir() {
			t.Fatalf("layout entry %s is not a directory", name)
		}
	}
}

func assertNoServiceSetStages(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yeet-service-root-") {
			t.Fatalf("stage directory %q was not cleaned up", entry.Name())
		}
	}
}
