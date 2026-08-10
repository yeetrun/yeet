// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestServiceSetCronRequiresActiveScheduledNativeGeneration(t *testing.T) {
	tests := []struct {
		name     string
		service  func(unitPath, timerPath string) *db.Service
		wantErr  string
		wantPlan bool
	}{
		{name: "missing", wantErr: `inspect service "reports" for schedule update`},
		{
			name: "ordinary native",
			service: func(unitPath, _ string) *db.Service {
				service := scheduledServiceSetCronFixture(unitPath, "")
				delete(service.Artifacts, db.ArtifactSystemdTimerFile)
				return service
			},
			wantErr: scheduledServiceSetOnlyMessage,
		},
		{
			name: "Compose",
			service: func(unitPath, timerPath string) *db.Service {
				service := scheduledServiceSetCronFixture(unitPath, timerPath)
				service.ServiceType = db.ServiceTypeDockerCompose
				return service
			},
			wantErr: scheduledServiceSetOnlyMessage,
		},
		{
			name: "VM",
			service: func(unitPath, timerPath string) *db.Service {
				service := scheduledServiceSetCronFixture(unitPath, timerPath)
				service.ServiceType = db.ServiceTypeVM
				return service
			},
			wantErr: scheduledServiceSetOnlyMessage,
		},
		{
			name: "historical timer only",
			service: func(unitPath, timerPath string) *db.Service {
				service := scheduledServiceSetCronFixture(unitPath, timerPath)
				service.Artifacts[db.ArtifactSystemdTimerFile].Refs = map[db.ArtifactRef]string{
					db.Gen(3): timerPath, "latest": timerPath,
				}
				return service
			},
			wantErr: scheduledServiceSetOnlyMessage,
		},
		{
			name: "staged timer only",
			service: func(unitPath, timerPath string) *db.Service {
				service := scheduledServiceSetCronFixture(unitPath, timerPath)
				service.Artifacts[db.ArtifactSystemdTimerFile].Refs = map[db.ArtifactRef]string{
					"staged": timerPath, "latest": timerPath,
				}
				return service
			},
			wantErr: scheduledServiceSetOnlyMessage,
		},
		{name: "active generation", service: scheduledServiceSetCronFixture, wantPlan: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			artifactDir := t.TempDir()
			unitPath := filepath.Join(artifactDir, "reports-4.service")
			timerPath := filepath.Join(artifactDir, "reports-4.timer")
			if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			services := map[string]*db.Service{}
			if tt.service != nil {
				services["reports"] = tt.service(unitPath, timerPath)
			}
			if err := server.cfg.DB.Set(&db.Data{Services: services}); err != nil {
				t.Fatal(err)
			}

			prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
			plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
			if !tt.wantPlan {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("planServiceScheduleMutation error = %v, want %q", err, tt.wantErr)
				}
				matches, globErr := filepath.Glob(filepath.Join(artifactDir, "reports-5.timer*"))
				if globErr != nil || len(matches) != 0 {
					t.Fatalf("ineligible service staged timer files %v: %v", matches, globErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("planServiceScheduleMutation: %v", err)
			}
			if plan == nil || plan.previous == nil || plan.target == nil || plan.stagedTimer == "" {
				t.Fatalf("eligible plan = %#v", plan)
			}
			t.Cleanup(func() {
				if removeErr := os.Remove(plan.stagedTimer); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					t.Errorf("remove staged timer: %v", removeErr)
				}
			})
		})
	}
}

func TestServiceSchedulePlanningRejectsSymlinkBeforeNoOp(t *testing.T) {
	server := newTestServer(t)
	artifactDir := t.TempDir()
	unitPath := filepath.Join(artifactDir, "reports-4.service")
	realTimer := filepath.Join(artifactDir, "real.timer")
	timerPath := filepath.Join(artifactDir, "reports-4.timer")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realTimer, []byte("[Timer]\nOnCalendar=*-*-* 02:30\nPersistent=true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realTimer, timerPath); err != nil {
		t.Fatal(err)
	}
	previous := scheduledServiceSetCronFixture(unitPath, timerPath)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous}}); err != nil {
		t.Fatal(err)
	}

	prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
	plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("planServiceScheduleMutation = %#v, %v, want symlink rejection before no-op", plan, err)
	}
}

func TestServiceSchedulePlanningCleansTimerAfterPostStageFailure(t *testing.T) {
	server := newTestServer(t)
	artifactDir := t.TempDir()
	unitPath := filepath.Join(artifactDir, "reports-4.service")
	timerPath := filepath.Join(artifactDir, "reports-4.timer")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(unitPath, unitPath+".link"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	previous := scheduledServiceSetCronFixture(unitPath, timerPath)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous}}); err != nil {
		t.Fatal(err)
	}
	before := serviceScheduleDirectoryNames(t, artifactDir)

	prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
	plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
	if err == nil || !strings.Contains(err.Error(), "single-link regular file") {
		t.Fatalf("planServiceScheduleMutation = %#v, %v, want post-stage unit provenance failure", plan, err)
	}
	after := serviceScheduleDirectoryNames(t, artifactDir)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("post-stage planning failure left files %v, want %v", after, before)
	}
}

func TestServiceSchedulePlanningSameVersionCollisionNeverAliasesActiveTimer(t *testing.T) {
	server := newTestServer(t)
	artifactDir := t.TempDir()
	unitPath := filepath.Join(artifactDir, "reports-4.service")
	timerPath := filepath.Join(artifactDir, "reports-4.timer")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(unitPath, unitPath+".link"); err != nil {
		t.Fatal(err)
	}
	oldTimer := []byte("[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\n")
	if err := os.WriteFile(timerPath, oldTimer, 0o640); err != nil {
		t.Fatal(err)
	}
	previous := scheduledServiceSetCronFixture(unitPath, timerPath)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous}}); err != nil {
		t.Fatal(err)
	}
	oldVersionPath := serviceScheduleTimerVersionPath
	serviceScheduleTimerVersionPath = func(string) string { return timerPath }
	t.Cleanup(func() { serviceScheduleTimerVersionPath = oldVersionPath })
	before := serviceScheduleDirectoryNames(t, artifactDir)

	prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
	plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
	if err == nil || !strings.Contains(err.Error(), "single-link regular file") {
		t.Fatalf("planServiceScheduleMutation = %#v, %v, want post-stage failure", plan, err)
	}
	raw, readErr := os.ReadFile(timerPath)
	if readErr != nil || !reflect.DeepEqual(raw, oldTimer) {
		t.Fatalf("active timer after collision = %q, %v, want %q", raw, readErr, oldTimer)
	}
	if after := serviceScheduleDirectoryNames(t, artifactDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("collision cleanup left files %v, want %v", after, before)
	}
}

func TestServiceSchedulePlanningStagedTimerDiffersFromEveryArtifactRef(t *testing.T) {
	server := newTestServer(t)
	artifactDir := t.TempDir()
	unitPath := filepath.Join(artifactDir, "reports-4.service")
	timerPath := filepath.Join(artifactDir, "reports-4.timer")
	collisionPath := filepath.Join(artifactDir, "reports-20260809120000.timer")
	diskCollisionPath := serviceScheduleTimerAttemptPath(collisionPath, 1)
	for path, raw := range map[string][]byte{
		unitPath:          []byte("[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n"),
		timerPath:         []byte("[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\n"),
		collisionPath:     []byte("referenced collision must not be overwritten\n"),
		diskCollisionPath: []byte("disk collision must not be overwritten\n"),
	} {
		if err := os.WriteFile(path, raw, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	previous := scheduledServiceSetCronFixture(unitPath, timerPath)
	previous.Artifacts[db.ArtifactPythonFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(2): collisionPath, "latest": collisionPath, "staged": collisionPath,
	}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous}}); err != nil {
		t.Fatal(err)
	}
	oldVersionPath := serviceScheduleTimerVersionPath
	serviceScheduleTimerVersionPath = func(string) string { return collisionPath }
	t.Cleanup(func() { serviceScheduleTimerVersionPath = oldVersionPath })

	prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
	plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
	if err != nil {
		t.Fatalf("planServiceScheduleMutation: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(plan.stagedTimer) })
	for name, artifact := range previous.Artifacts {
		if artifact == nil {
			continue
		}
		for ref, path := range artifact.Refs {
			if filepath.Clean(plan.stagedTimer) == filepath.Clean(path) {
				t.Fatalf("staged timer %q aliases %s ref %q", plan.stagedTimer, name, ref)
			}
		}
	}
	for path, want := range map[string]string{
		collisionPath:     "referenced collision must not be overwritten\n",
		diskCollisionPath: "disk collision must not be overwritten\n",
	} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("colliding path %s bytes = %q, %v, want %q", path, raw, err, want)
		}
	}
}

func TestServiceSchedulePlanningTempSourceCleanupFailureRemovesStagedTimer(t *testing.T) {
	server := newTestServer(t)
	artifactDir := t.TempDir()
	unitPath := filepath.Join(artifactDir, "reports-4.service")
	timerPath := filepath.Join(artifactDir, "reports-4.timer")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	previous := scheduledServiceSetCronFixture(unitPath, timerPath)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous}}); err != nil {
		t.Fatal(err)
	}
	oldTempSource := newServiceScheduleTempSource
	newServiceScheduleTempSource = func(write func(io.Writer) error) (editSource, error) {
		source, err := newTempEditSource(write)
		if err != nil {
			return editSource{}, err
		}
		cleanup := source.cleanup
		source.cleanup = func() error {
			return errors.Join(cleanup(), errors.New("injected schedule temp cleanup failure"))
		}
		return source, nil
	}
	t.Cleanup(func() { newServiceScheduleTempSource = oldTempSource })
	before := serviceScheduleDirectoryNames(t, artifactDir)

	prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
	plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
	if err == nil || !strings.Contains(err.Error(), "injected schedule temp cleanup failure") {
		t.Fatalf("planServiceScheduleMutation = %#v, %v, want temp cleanup failure", plan, err)
	}
	if after := serviceScheduleDirectoryNames(t, artifactDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("temp cleanup failure left files %v, want %v", after, before)
	}
}

func TestServiceSchedulePlanningValidatesGenerationMetadataBeforeStaging(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name       string
		mutate     func(*db.Service)
		wantErr    string
		wantTarget int
	}{
		{name: "record name mismatch", mutate: func(service *db.Service) { service.Name = "other" }, wantErr: "record name"},
		{name: "latest behind active", mutate: func(service *db.Service) { service.LatestGeneration = 3 }, wantErr: "latest generation"},
		{name: "next generation overflow", mutate: func(service *db.Service) {
			service.Generation = maxInt
			service.LatestGeneration = maxInt
			for _, artifact := range service.Artifacts {
				path := artifact.Refs[db.Gen(4)]
				delete(artifact.Refs, db.Gen(4))
				artifact.Refs[db.Gen(maxInt)] = path
			}
		}, wantErr: "generation overflow"},
		{name: "legitimate rollback state", mutate: func(service *db.Service) { service.LatestGeneration = 5 }, wantTarget: 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			artifactDir := t.TempDir()
			unitPath := filepath.Join(artifactDir, "reports-4.service")
			timerPath := filepath.Join(artifactDir, "reports-4.timer")
			if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			service := scheduledServiceSetCronFixture(unitPath, timerPath)
			tt.mutate(service)
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": service}}); err != nil {
				t.Fatal(err)
			}
			before := serviceScheduleDirectoryNames(t, artifactDir)

			prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
			plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("planServiceScheduleMutation = %#v, %v, want %q", plan, err, tt.wantErr)
				}
				if after := serviceScheduleDirectoryNames(t, artifactDir); !reflect.DeepEqual(after, before) {
					t.Fatalf("invalid generation staged files %v, want %v", after, before)
				}
				return
			}
			if err != nil {
				t.Fatalf("planServiceScheduleMutation: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(plan.stagedTimer) })
			if plan.target.Generation != tt.wantTarget || plan.target.LatestGeneration != tt.wantTarget {
				t.Fatalf("target generation = %d/%d, want %d/%d", plan.target.Generation, plan.target.LatestGeneration, tt.wantTarget, tt.wantTarget)
			}
		})
	}
}

func TestServiceScheduleTimerRewritePreservesOtherBytes(t *testing.T) {
	raw := "[Unit]\nDescription=Nightly reports\n\n[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\nAccuracySec=2m\nRandomizedDelaySec=15m\n\n[Install]\nWantedBy=timers.target\n"
	want := "[Unit]\nDescription=Nightly reports\n\n[Timer]\nOnCalendar=*-*-* 02:30\nPersistent=true\nAccuracySec=2m\nRandomizedDelaySec=15m\n\n[Install]\nWantedBy=timers.target\n"
	got, err := rewriteSystemdTimerCalendar(raw, "*-*-* 02:30")
	if err != nil {
		t.Fatalf("rewriteSystemdTimerCalendar: %v", err)
	}
	if got != want {
		t.Fatalf("rewritten timer bytes = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("rewritten timer lost its final newline")
	}
}

func TestParseServiceScheduleSystemdUnitEnabled(t *testing.T) {
	commandErr := errors.New("systemctl failed")
	tests := []struct {
		name       string
		raw        string
		exited     bool
		commandErr error
		want       bool
		wantErr    string
	}{
		{name: "enabled", raw: "enabled\n", want: true},
		{name: "static", raw: "static\n", want: true},
		{name: "disabled", raw: "disabled\n", exited: true, commandErr: commandErr},
		{name: "masked", raw: "masked-runtime\n", exited: true, commandErr: commandErr},
		{name: "unknown success", raw: "future-state\n", wantErr: "unsupported successful"},
		{name: "disabled transport error", raw: "disabled\n", commandErr: commandErr, wantErr: "systemctl failed"},
		{name: "enabled command error", raw: "enabled\n", exited: true, commandErr: commandErr, wantErr: "systemctl failed"},
		{name: "not found", raw: "not-found\n", exited: true, commandErr: commandErr, wantErr: "systemctl failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseServiceScheduleSystemdUnitEnabled("reports.timer", tt.raw, tt.exited, tt.commandErr)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parse enablement = %t, %v, want error %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("parse enablement = %t, %v, want %t", got, err, tt.want)
			}
		})
	}
}

func TestParseServiceScheduleTimerState(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		active  bool
		wantErr string
	}{
		{name: "waiting", raw: "ActiveState=active\nSubState=waiting\n", active: true},
		{name: "running", raw: "SubState=running\nActiveState=active\n", active: true},
		{name: "elapsed", raw: "ActiveState=active\nSubState=elapsed\n", active: true},
		{name: "inactive", raw: "ActiveState=inactive\nSubState=dead\n"},
		{name: "unknown property", raw: "ActiveState=active\nLoadState=loaded\n", wantErr: "unsupported systemctl"},
		{name: "malformed", raw: "ActiveState=active\nwaiting\n", wantErr: "unsupported systemctl"},
		{name: "duplicate", raw: "ActiveState=active\nActiveState=active\nSubState=waiting\n", wantErr: "duplicate"},
		{name: "missing state", raw: "ActiveState=active\n", wantErr: "unsupported state"},
		{name: "failed", raw: "ActiveState=failed\nSubState=failed\n", wantErr: "unsupported state"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseServiceScheduleTimerState("reports.timer", tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parse timer state = %#v, %v, want error %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got.Active != tt.active {
				t.Fatalf("parse timer state = %#v, %v, want active=%t", got, err, tt.active)
			}
		})
	}
}

func TestServiceScheduleTimerRewriteRequiresExactlyOneCalendar(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing", raw: "[Timer]\nPersistent=true\n", want: "missing OnCalendar"},
		{name: "repeated", raw: "[Timer]\nOnCalendar=one\nOnCalendar=two\n", want: "repeated OnCalendar"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rewriteSystemdTimerCalendar(tt.raw, "*-*-* 02:30")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("rewriteSystemdTimerCalendar error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestServiceScheduleClonePreservesCompleteActiveGeneration(t *testing.T) {
	enabled := true
	keepLast := 7
	required := false
	activePaths := map[db.ArtifactName]string{
		db.ArtifactBinary:           "/store/reports-4",
		db.ArtifactEnvFile:          "/store/reports-4.env",
		db.ArtifactSystemdUnit:      "/store/reports-4.service",
		db.ArtifactSystemdTimerFile: "/store/reports-4.timer",
		db.ArtifactNetNSService:     "/store/reports-netns-4.service",
		db.ArtifactNetNSEnv:         "/store/reports-netns-4.env",
		db.ArtifactTSService:        "/store/reports-ts-4.service",
		db.ArtifactTSEnv:            "/store/reports-ts-4.env",
		db.ArtifactTSBinary:         "/store/tailscaled-4",
		db.ArtifactTSConfig:         "/store/tailscaled-4.json",
	}
	artifacts := make(db.ArtifactStore, len(activePaths)+1)
	for name, path := range activePaths {
		artifacts[name] = &db.Artifact{Refs: map[db.ArtifactRef]string{
			db.Gen(3): path + ".old", db.Gen(4): path, "latest": path + ".latest-stale", "staged": path + ".stale",
		}}
	}
	artifacts[db.ArtifactPythonFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(2): "/store/historical.py", "latest": "/store/historical.py", "staged": "/store/stale.py",
	}}
	previous := &db.Service{
		Name: "reports", ServiceType: db.ServiceTypeSystemd,
		Identity:    &db.ServiceIdentity{RequestedUser: "reports", RequestedGroup: "backup", UID: 1001, GID: 1002},
		ServiceRoot: "/srv/reports", ServiceRootZFS: "tank/services/reports",
		SnapshotPolicy: &db.SnapshotPolicy{
			Enabled: &enabled, KeepLast: &keepLast, MaxAge: "720h", Events: []string{"run", "manual"}, Required: &required,
		},
		Generation: 4, LatestGeneration: 4, Publish: []string{"127.0.0.1:8080:80"},
		Artifacts: artifacts,
		Network: &db.ServiceNetworkConfig{
			Modes: []string{"iso", "ts"}, TSVersion: "1.86.0", TSExitNode: "exit", TSTags: []string{"tag:reports"},
		},
		SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("10.0.0.40")},
		Macvlan: &db.MacvlanNetwork{
			Interface: "mv-reports", Mac: "02:00:00:00:00:40", Parent: "eth0", VLAN: 40,
		},
		TSNet: &db.TailscaleNetwork{Interface: "ts-reports", Version: "1.86.0", ExitNode: "exit", Tags: []string{"tag:reports"}},
		ISO: &db.ISOAllocation{
			Kind: "native", State: "ready", Link: netip.MustParsePrefix("10.44.0.0/31"),
			HostIP: netip.MustParseAddr("10.44.0.0"), PeerIP: netip.MustParseAddr("10.44.0.1"),
			Interface: "iso-reports", PeerInterface: "iso-peer-reports", DesiredModes: []string{"iso", "ts"},
			AllocatorVersion: 2, PolicyVersion: 3,
		},
	}
	original := previous.Clone()
	const stagedTimer = "/store/reports-5.timer"
	target, err := cloneActiveServiceGeneration(previous, stagedTimer)
	if err != nil {
		t.Fatalf("cloneActiveServiceGeneration: %v", err)
	}
	if target.Generation != 5 || target.LatestGeneration != 5 {
		t.Fatalf("target generation = %d/%d, want 5/5", target.Generation, target.LatestGeneration)
	}
	for name, path := range activePaths {
		want := path
		if name == db.ArtifactSystemdTimerFile {
			want = stagedTimer
		}
		if got, ok := target.Artifacts.Gen(name, 5); !ok || got != want {
			t.Fatalf("target %s gen 5 = %q, %t, want %q", name, got, ok, want)
		}
	}
	if path, ok := target.Artifacts.Gen(db.ArtifactPythonFile, 5); ok {
		t.Fatalf("historical-only artifact gained gen 5 path %q", path)
	}
	targetFields := target.Clone()
	targetFields.Generation = previous.Generation
	targetFields.LatestGeneration = previous.LatestGeneration
	targetFields.Artifacts = previous.Clone().Artifacts
	if !reflect.DeepEqual(targetFields, previous) {
		t.Fatalf("non-generation service state changed:\n got %#v\nwant %#v", targetFields, previous)
	}
	if !reflect.DeepEqual(previous, original) {
		t.Fatalf("original service was mutated:\n got %#v\nwant %#v", previous, original)
	}
}

func TestServiceScheduleClonePromotesActiveSandboxPolicy(t *testing.T) {
	const activeUnit = "/store/reports-4.service"
	previous := &db.Service{
		Name: "reports", Generation: 4, LatestGeneration: 6,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{
				db.Gen(4): "/store/reports-4.timer", db.Gen(6): "/store/reports-6.timer", "latest": "/store/reports-6.timer", "staged": "/store/stale.timer",
			}},
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{
				db.Gen(4): activeUnit, db.Gen(6): "/store/reports-6.service", "latest": "/store/reports-6.service", "staged": "/store/stale.service",
			}},
		},
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(4): {State: "on", Writable: []db.ServiceSandboxExposure{{Source: "/srv/reports", Destination: "/var/lib/reports"}}},
			db.Gen(6): {State: "off", ReadOnly: []db.ServiceSandboxExposure{{Source: "/wrong-latest", Destination: "/wrong-latest"}}},
			"latest":  {State: "off"},
			"staged":  {State: "off"},
		}},
	}
	original := previous.Clone()

	target, err := cloneActiveServiceGeneration(previous, "/store/reports-7.timer")
	if err != nil {
		t.Fatalf("cloneActiveServiceGeneration: %v", err)
	}
	policy, ok := target.SandboxPolicy(7)
	if !ok || policy.State != "on" || !reflect.DeepEqual(policy.Writable, []db.ServiceSandboxExposure{{Source: "/srv/reports", Destination: "/var/lib/reports"}}) {
		t.Fatalf("gen-7 sandbox policy = %#v, %t; want active gen-4 policy", policy, ok)
	}
	if unit, ok := target.Artifacts.Gen(db.ArtifactSystemdUnit, 7); !ok || unit != activeUnit {
		t.Fatalf("gen-7 payload unit = %q, %t; want unchanged active unit %q", unit, ok, activeUnit)
	}
	active, staged := target.Sandbox.Refs[db.Gen(4)], target.Sandbox.Refs["staged"]
	latest, generated := target.Sandbox.Refs["latest"], target.Sandbox.Refs[db.Gen(7)]
	if active == staged || active == latest || active == generated || staged == latest || staged == generated || latest == generated {
		t.Fatalf("schedule sandbox policies alias: active=%p staged=%p latest=%p generated=%p", active, staged, latest, generated)
	}
	staged.Writable[0].Destination = "/staged-only"
	latest.Writable[0].Destination = "/latest-only"
	generated.Writable[0].Destination = "/generated-only"
	if active.Writable[0].Destination != "/var/lib/reports" || staged.Writable[0].Destination == latest.Writable[0].Destination || latest.Writable[0].Destination == generated.Writable[0].Destination {
		t.Fatalf("schedule sandbox exposure slices alias: active=%#v staged=%#v latest=%#v generated=%#v", active, staged, latest, generated)
	}
	request := serviceScheduleMigrationRequest(&serviceScheduleMutationPlan{previous: previous, target: target})
	if request.GenerationDiagnostic != (serviceIdentityGenerationDiagnostic{}) {
		t.Fatalf("schedule request inherited sandbox diagnostic metadata: %#v", request.GenerationDiagnostic)
	}
	if !reflect.DeepEqual(previous, original) {
		t.Fatalf("source service was mutated:\n got %#v\nwant %#v", previous, original)
	}
}

func TestServiceScheduleCloneLeavesMissingSandboxPolicyLegacy(t *testing.T) {
	previous := &db.Service{
		Name: "reports", Generation: 4, LatestGeneration: 4,
		Artifacts: db.ArtifactStore{db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{
			db.Gen(4): "/store/reports-4.timer",
		}}},
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(3): {State: "off"},
			"staged":  {State: "on"},
		}},
	}

	target, err := cloneActiveServiceGeneration(previous, "/store/reports-5.timer")
	if err != nil {
		t.Fatalf("cloneActiveServiceGeneration: %v", err)
	}
	if policy, ok := target.SandboxPolicy(5); ok || policy != nil {
		t.Fatalf("gen-5 sandbox policy = %#v, %t; want missing legacy policy", policy, ok)
	}
	if _, ok := target.Sandbox.Refs["staged"]; ok {
		t.Fatalf("stale staged sandbox policy was retained: %#v", target.Sandbox.Refs)
	}
	if policy := target.Sandbox.Refs[db.Gen(3)]; policy == nil || policy.State != "off" {
		t.Fatalf("historical sandbox policy = %#v, want preserved", policy)
	}
}

func TestServiceScheduleLegacyCloneWithHistoricalPolicyRemainsActivatable(t *testing.T) {
	server := newTestServer(t)
	artifacts, staticVerifications := canonicalLegacyRollbackArtifacts(t, server, "reports", 4)
	timer := filepath.Join(server.defaultServiceRootDir("reports"), "run", "reports-4.timer")
	if err := os.WriteFile(timer, []byte("[Timer]\nOnCalendar=*-*-* 02:30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts[db.ArtifactSystemdTimerFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(4): timer}}
	previous := &db.Service{
		Name: "reports", ServiceType: db.ServiceTypeSystemd, ServiceRoot: server.defaultServiceRootDir("reports"),
		Generation: 4, LatestGeneration: 4, Artifacts: artifacts,
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(3): {State: "off"},
			"latest":  {State: "off"},
		}},
	}
	target, err := cloneActiveServiceGeneration(previous, filepath.Join(filepath.Dir(timer), "reports-5.timer"))
	if err != nil {
		t.Fatalf("clone legacy schedule generation: %v", err)
	}
	if policy, ok := target.SandboxPolicy(5); ok || policy != nil {
		t.Fatalf("cloned legacy schedule policy = %#v, %t; want absent", policy, ok)
	}
	oldEnsure, oldProbe := ensureBubblewrapForServiceSandboxMutation, probeServiceSandboxForMutation
	t.Cleanup(func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		probeServiceSandboxForMutation = oldProbe
	})
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		t.Fatal("legacy schedule activation ensured Bubblewrap")
		return nil
	}
	probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
		t.Fatal("legacy schedule activation probed Bubblewrap")
		return nil
	}

	if err := server.preflightSandboxGenerationActivation(context.Background(), target, 5); err != nil {
		t.Fatalf("preflight cloned legacy schedule generation: %v", err)
	}
	if *staticVerifications != 1 {
		t.Fatalf("legacy schedule static verifications = %d, want 1", *staticVerifications)
	}
}

func TestServiceScheduleCloneRejectsNilActiveSandboxPolicy(t *testing.T) {
	previous := &db.Service{
		Name: "reports", Generation: 4, LatestGeneration: 6,
		Artifacts: db.ArtifactStore{db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{
			db.Gen(4): "/store/reports-4.timer",
		}}},
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(4): nil,
			db.Gen(6): {State: "off"},
			"latest":  {State: "off"},
			"staged":  {State: "on"},
		}},
	}

	if _, err := cloneActiveServiceGeneration(previous, "/store/reports-7.timer"); err == nil || !strings.Contains(err.Error(), "nil exact sandbox policy") {
		t.Fatalf("cloneActiveServiceGeneration error = %v, want nil exact sandbox policy rejection", err)
	}
}

func TestServiceScheduleNoOpHasZeroMutations(t *testing.T) {
	server := newTestServer(t)
	artifactDir := t.TempDir()
	unitPath := filepath.Join(artifactDir, "reports-4.service")
	timerPath := filepath.Join(artifactDir, "reports-4.timer")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=*-*-* 02:30\nPersistent=true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	previous := scheduledServiceSetCronFixture(unitPath, timerPath)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous.Clone()}}); err != nil {
		t.Fatal(err)
	}
	prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
	stableBefore := snapshotServiceScheduleDirectory(t, systemdSystemDir)
	databasePath := filepath.Join(server.cfg.RootDir, "db.json")
	databaseBefore, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	entriesBefore, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	oldSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		t.Fatalf("no-op schedule invoked systemctl %v", args)
		return nil
	}
	t.Cleanup(func() { catchSystemctl = oldSystemctl })

	if err := server.updateServiceScheduleLocked(context.Background(), "reports", "30 2 * * *", io.Discard); err != nil {
		t.Fatalf("updateServiceScheduleLocked no-op: %v", err)
	}
	databaseAfter, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(databaseAfter) != string(databaseBefore) {
		t.Fatalf("no-op rewrote database:\n before %s\n after %s", databaseBefore, databaseAfter)
	}
	entriesAfter, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entriesAfter, entriesBefore) {
		t.Fatalf("no-op artifact entries = %v, want %v", entriesAfter, entriesBefore)
	}
	if stableAfter := snapshotServiceScheduleDirectory(t, systemdSystemDir); !reflect.DeepEqual(stableAfter, stableBefore) {
		t.Fatalf("no-op stable artifacts changed:\n got %#v\nwant %#v", stableAfter, stableBefore)
	}
	current, err := server.serviceView("reports")
	if err != nil || !reflect.DeepEqual(current.AsStruct(), previous) {
		t.Fatalf("no-op service = %#v, %v, want %#v", current.AsStruct(), err, previous)
	}
}

func TestServiceScheduleNoOpRejectsIncoherentInstalledGenerationWithoutMutation(t *testing.T) {
	tests := []struct {
		name       string
		timerState string
		mutate     func(t *testing.T, stableDir string)
		wantErr    string
	}{
		{
			name: "missing stable timer", timerState: "active/waiting", wantErr: "installed generation artifact",
			mutate: func(t *testing.T, stableDir string) {
				t.Helper()
				if err := os.Remove(filepath.Join(stableDir, "reports.timer")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "drifted stable timer", timerState: "active/waiting", wantErr: "installed generation artifact",
			mutate: func(t *testing.T, stableDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(stableDir, "reports.timer"), []byte("[Timer]\nOnCalendar=*-*-* 09:00\nPersistent=true\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
		{name: "failed timer runtime", timerState: "failed/failed", wantErr: "unsupported state failed/failed"},
		{name: "unsupported timer runtime", timerState: "activating/start", wantErr: "unsupported state activating/start"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			artifactDir := t.TempDir()
			unitPath := filepath.Join(artifactDir, "reports-4.service")
			timerPath := filepath.Join(artifactDir, "reports-4.timer")
			if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=*-*-* 02:30\nPersistent=true\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			previous := scheduledServiceSetCronFixture(unitPath, timerPath)
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous.Clone()}}); err != nil {
				t.Fatal(err)
			}
			systemctlLog := prepareServiceSchedulePlanningPreflight(t, server, "reports", tt.timerState)
			if tt.mutate != nil {
				tt.mutate(t, systemdSystemDir)
			}
			databasePath := filepath.Join(server.cfg.RootDir, "db.json")
			databaseBefore, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			generationBefore := snapshotServiceScheduleDirectory(t, artifactDir)
			stableBefore := snapshotServiceScheduleDirectory(t, systemdSystemDir)

			err = server.updateServiceScheduleLocked(context.Background(), "reports", "30 2 * * *", io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("updateServiceScheduleLocked error = %v, want %q", err, tt.wantErr)
			}
			databaseAfter, readErr := os.ReadFile(databasePath)
			if readErr != nil || !reflect.DeepEqual(databaseAfter, databaseBefore) {
				t.Fatalf("rejected no-op database changed: %v", readErr)
			}
			if after := snapshotServiceScheduleDirectory(t, artifactDir); !reflect.DeepEqual(after, generationBefore) {
				t.Fatalf("rejected no-op generation changed:\n got %#v\nwant %#v", after, generationBefore)
			}
			if after := snapshotServiceScheduleDirectory(t, systemdSystemDir); !reflect.DeepEqual(after, stableBefore) {
				t.Fatalf("rejected no-op stable artifacts changed:\n got %#v\nwant %#v", after, stableBefore)
			}
			calls, readErr := os.ReadFile(systemctlLog)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
			for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if call != "" && !strings.HasPrefix(call, "show ") && !strings.HasPrefix(call, "is-enabled ") {
					t.Fatalf("rejected no-op invoked mutating systemctl call %q", call)
				}
			}
			current, viewErr := server.serviceView("reports")
			if viewErr != nil || !reflect.DeepEqual(current.AsStruct(), previous) {
				t.Fatalf("rejected no-op service = %#v, %v, want %#v", current.AsStruct(), viewErr, previous)
			}
		})
	}
}

func TestServiceSchedulePlanningPreservesActiveTimerMode(t *testing.T) {
	server := newTestServer(t)
	artifactDir := t.TempDir()
	unitPath := filepath.Join(artifactDir, "reports-4.service")
	timerPath := filepath.Join(artifactDir, "reports-4.timer")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	previous := scheduledServiceSetCronFixture(unitPath, timerPath)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous}}); err != nil {
		t.Fatal(err)
	}

	prepareServiceSchedulePlanningPreflight(t, server, "reports", "active/waiting")
	plan, err := server.planServiceScheduleMutation("reports", "30 2 * * *")
	if err != nil {
		t.Fatalf("planServiceScheduleMutation: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(plan.stagedTimer) })
	stagedInfo, err := os.Stat(plan.stagedTimer)
	if err != nil {
		t.Fatal(err)
	}
	if stagedInfo.Mode().Perm() != 0o640 {
		t.Fatalf("generation timer source mode = %04o, want active source mode 0640", stagedInfo.Mode().Perm())
	}
	foundStableTimer := false
	for _, intent := range plan.intent {
		if filepath.Base(intent.Path) != "reports.timer" {
			continue
		}
		foundStableTimer = true
		if intent.Mode.Perm() != 0o640 {
			t.Fatalf("stable timer intent mode = %04o, want active source mode 0640", intent.Mode.Perm())
		}
	}
	if !foundStableTimer {
		t.Fatal("schedule plan did not include the stable timer intent")
	}
}

func TestServiceScheduleTransactionPreservesPayloadSidecarsEnablementAndSuppressesCatchUp(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.active["reports.service"] = true
	fixture.enabled["reports.timer"] = false
	fixture.enabled["yeet-reports-ns.service"] = true
	fixture.enabled["yeet-reports-ts.service"] = false
	fixture.crossBoundaryOnTimerStop = true
	fixture.failIfReloadWhileTimerActive = true
	stage := fixture.plan.stage
	fixture.plan.stage = func(ctx context.Context) error {
		if fixture.active["reports.timer"] {
			return errors.New("stable schedule staging occurred while timer was active")
		}
		return stage(ctx)
	}
	fixture.request.StageGeneration = fixture.plan.stage
	beforeActive := maps.Clone(fixture.active)
	beforeEnabled := maps.Clone(fixture.enabled)

	if err := fixture.apply(); err != nil {
		t.Fatalf("schedule mutation: %v", err)
	}
	fixture.assertTargetState()
	if fixture.payloadActivations != 0 {
		t.Fatalf("persistent timer caught up across the update boundary: activations=%d calls=%v", fixture.payloadActivations, fixture.systemctlCalls)
	}
	if fixture.active["reports.service"] != beforeActive["reports.service"] ||
		fixture.active["yeet-reports-ns.service"] != beforeActive["yeet-reports-ns.service"] ||
		fixture.active["yeet-reports-ts.service"] != beforeActive["yeet-reports-ts.service"] {
		t.Fatalf("payload or sidecar runtime changed: got=%v want=%v calls=%v", fixture.active, beforeActive, fixture.systemctlCalls)
	}
	if !reflect.DeepEqual(fixture.enabled, beforeEnabled) {
		t.Fatalf("generation enablement changed: got=%v want=%v calls=%v", fixture.enabled, beforeEnabled, fixture.systemctlCalls)
	}
	for _, call := range fixture.systemctlCalls {
		if strings.Contains(call, "reports.service") || strings.Contains(call, "yeet-reports-ns.service") ||
			strings.Contains(call, "yeet-reports-ts.service") ||
			strings.HasPrefix(call, "enable ") || strings.HasPrefix(call, "disable ") {
			t.Fatalf("schedule-only lifecycle touched payload, sidecar, or enablement: %v", fixture.systemctlCalls)
		}
	}
	wantLifecycle := []string{
		"stop reports.timer",
		"clean --what=state reports.timer",
		"start reports.timer",
	}
	if !reflect.DeepEqual(fixture.systemctlCalls, wantLifecycle) {
		t.Fatalf("schedule timer lifecycle = %v, want %v", fixture.systemctlCalls, wantLifecycle)
	}
}

func TestServiceScheduleTransactionRollsBackActivationAndRetrySucceeds(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.failSystemctlCall = "start reports.timer"
	fixture.failSystemctlRemaining = 1

	err := fixture.apply()
	if err == nil || !strings.Contains(err.Error(), "persistent-state reset") {
		t.Fatalf("schedule mutation error = %v, want activation failure", err)
	}
	fixture.assertPreviousState()
	if _, statErr := os.Stat(fixture.plan.stagedTimer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fully rolled-back schedule retained staged timer: %v", statErr)
	}
	if err := fixture.server.checkServiceIdentityRecoveryMutationAllowed("reports"); err != nil {
		t.Fatalf("fully rolled-back schedule left recovery block: %v", err)
	}

	if err := os.WriteFile(fixture.plan.stagedTimer, fixture.newTimer, 0o640); err != nil {
		t.Fatal(err)
	}
	fixture.systemctlCalls = nil
	if err := fixture.apply(); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	fixture.assertTargetState()
	for _, call := range fixture.systemctlCalls {
		if strings.Contains(call, "reports.service") || strings.Contains(call, "yeet-reports-ns.service") ||
			strings.Contains(call, "yeet-reports-ts.service") {
			t.Fatalf("scheduled retry touched payload or sidecar: calls=%v", fixture.systemctlCalls)
		}
	}
}

func TestServiceScheduleTransactionPreservesInactiveTimerExactly(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.active["reports.timer"] = false
	beforeActive := maps.Clone(fixture.active)
	beforeEnabled := maps.Clone(fixture.enabled)

	if err := fixture.apply(); err != nil {
		t.Fatalf("schedule mutation: %v", err)
	}
	fixture.assertTargetState()
	if !reflect.DeepEqual(fixture.active, beforeActive) {
		t.Fatalf("inactive timer runtime changed: got=%v want=%v", fixture.active, beforeActive)
	}
	if !reflect.DeepEqual(fixture.enabled, beforeEnabled) {
		t.Fatalf("inactive timer enablement changed: got=%v want=%v", fixture.enabled, beforeEnabled)
	}
	if len(fixture.systemctlCalls) != 0 {
		t.Fatalf("inactive timer lifecycle invoked systemctl: %v", fixture.systemctlCalls)
	}
}

func TestServiceScheduleTransactionPreservesLegacyNilIdentity(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.previous.Identity = nil
	fixture.plan.previous.Identity = nil
	fixture.plan.target.Identity = nil
	fixture.plan.identity = effectiveServiceIdentity(fixture.previous.View())
	if err := fixture.server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"reports": fixture.previous.Clone(),
	}}); err != nil {
		t.Fatal(err)
	}
	fixture.request = serviceScheduleMigrationRequest(fixture.plan)

	if err := fixture.apply(); err != nil {
		t.Fatalf("schedule mutation for legacy identity: %v", err)
	}
	current, err := fixture.server.serviceView("reports")
	if err != nil {
		t.Fatal(err)
	}
	if current.AsStruct().Identity != nil {
		t.Fatalf("legacy nil identity became explicit: %#v", current.AsStruct().Identity)
	}
	if !reflect.DeepEqual(current.AsStruct(), fixture.plan.target) {
		t.Fatalf("legacy schedule target = %#v, want %#v", current.AsStruct(), fixture.plan.target)
	}
}

func TestServiceScheduleTransactionRollsBackAfterGenerationStaging(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.failPhase = serviceIdentityPhaseInventorySeal

	err := fixture.apply()
	if err == nil || !strings.Contains(err.Error(), serviceIdentityPhaseInventorySeal) {
		t.Fatalf("schedule mutation error = %v, want post-staging failure", err)
	}
	fixture.assertPreviousState()
	if _, statErr := os.Stat(fixture.plan.stagedTimer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("post-staging rollback retained staged timer: %v", statErr)
	}
}

func TestServiceScheduleTransactionRetainsOwnedTimerUntilRecovery(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.active["reports.service"] = true
	fixture.expectedActive = maps.Clone(fixture.active)
	fixture.enabled["reports.timer"] = false
	fixture.enabled["yeet-reports-ns.service"] = true
	fixture.expectedEnabled = maps.Clone(fixture.enabled)
	fixture.failSystemctlCall = "start reports.timer"
	fixture.failSystemctlRemaining = 2

	err := fixture.apply()
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") {
		t.Fatalf("schedule mutation error = %v, want incomplete rollback", err)
	}
	if _, statErr := os.Stat(fixture.plan.stagedTimer); statErr != nil {
		t.Fatalf("incomplete rollback removed recovery-owned timer: %v", statErr)
	}
	if blockErr := fixture.server.checkServiceIdentityRecoveryMutationAllowed("reports"); !errors.Is(blockErr, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("recovery block = %v, want service identity recovery block", blockErr)
	}
	if retryErr := fixture.apply(); !errors.Is(retryErr, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("mutation during retained recovery = %v, want recovery block", retryErr)
	}
	if _, statErr := os.Stat(fixture.plan.stagedTimer); statErr != nil {
		t.Fatalf("blocked retry removed recovery-owned timer: %v", statErr)
	}

	fixture.failSystemctlRemaining = 0
	fixture.server = &Server{cfg: fixture.server.cfg}
	if err := fixture.server.recoverServiceIdentityMigrationsWithOps(context.Background(), fixture.ops()); err != nil {
		t.Fatalf("recover schedule transaction: %v", err)
	}
	fixture.assertPreviousState()
	if _, statErr := os.Stat(fixture.plan.stagedTimer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("restart recovery retained schedule timer after exact rollback: %v", statErr)
	}
	if err := fixture.server.checkServiceIdentityRecoveryMutationAllowed("reports"); err != nil {
		t.Fatalf("restart recovery left mutation blocked: %v", err)
	}
	for _, call := range fixture.systemctlCalls {
		if strings.Contains(call, "reports.service") || strings.Contains(call, "yeet-reports-ns.service") ||
			strings.Contains(call, "yeet-reports-ts.service") {
			t.Fatalf("rollback or restart recovery touched payload or sidecar: %v", fixture.systemctlCalls)
		}
	}

	safePlan := fixture.plan
	var planned *serviceScheduleMutationPlan
	oldRequestForUpdate := serviceScheduleMigrationRequestForUpdate
	serviceScheduleMigrationRequestForUpdate = func(plan *serviceScheduleMutationPlan) serviceIdentityMigrationRequest {
		planned = plan
		request := serviceScheduleMigrationRequest(plan)
		request.StageGeneration = safePlan.stage
		request.GenerationPaths = append([]string(nil), safePlan.generationPaths...)
		request.GenerationIntents = append([]serviceIdentityPathState(nil), safePlan.intent...)
		request.GenerationUnits = append([]string(nil), safePlan.units...)
		plan.timerPath = safePlan.timerPath
		plan.timerUnit = safePlan.timerUnit
		request.Schedule = &serviceScheduleJournalState{TimerPath: plan.timerPath, TimerUnit: plan.timerUnit}
		request.ops = fixture.ops()
		return request
	}
	t.Cleanup(func() { serviceScheduleMigrationRequestForUpdate = oldRequestForUpdate })
	fixture.systemctlCalls = nil
	stubServiceScheduleSystemctl(t, "active/waiting")
	if err := fixture.server.updateServiceScheduleLocked(context.Background(), "reports", "30 2 * * *", io.Discard); err != nil {
		t.Fatalf("fresh schedule retry after restart recovery: %v", err)
	}
	if planned == nil {
		t.Fatal("production update did not produce a schedule mutation plan")
	}
	if planned.target.Generation != 5 || planned.target.LatestGeneration != 5 {
		t.Fatalf("fresh retry generation = %d/%d, want 5/5", planned.target.Generation, planned.target.LatestGeneration)
	}
	for name, artifact := range fixture.previous.Artifacts {
		if artifact == nil {
			continue
		}
		for ref, path := range artifact.Refs {
			if filepath.Clean(planned.stagedTimer) == filepath.Clean(path) {
				t.Fatalf("fresh production retry source %q aliases previous %s ref %q", planned.stagedTimer, name, ref)
			}
		}
	}
	current, err := fixture.server.serviceView("reports")
	if err != nil || !reflect.DeepEqual(current.AsStruct(), planned.target) {
		t.Fatalf("fresh production retry service = %#v, %v, want %#v", current.AsStruct(), err, planned.target)
	}
	assertServiceSchedulePathState(fixture.t, fixture.installedService, []byte(planned.replacement), nil)
	assertServiceSchedulePathState(fixture.t, fixture.installedTimer, fixture.newTimer, nil)
	assertServiceSchedulePathState(fixture.t, fixture.installedAuxiliary, fixture.newAuxiliary, nil)
	assertServiceSchedulePathState(fixture.t, fixture.installedTSAuxiliary, fixture.newTSAuxiliary, nil)
	if _, err := os.Stat(planned.stagedTimer); err != nil {
		t.Fatalf("fresh production retry source missing: %v", err)
	}
	if !reflect.DeepEqual(fixture.active, fixture.expectedActive) {
		t.Fatalf("fresh production retry runtime = %#v, want %#v", fixture.active, fixture.expectedActive)
	}
	if !reflect.DeepEqual(fixture.enabled, fixture.expectedEnabled) {
		t.Fatalf("fresh production retry enablement = %v, want %v", fixture.enabled, fixture.expectedEnabled)
	}
	for _, call := range fixture.systemctlCalls {
		if strings.Contains(call, "reports.service") || strings.Contains(call, "yeet-reports-ns.service") ||
			strings.Contains(call, "yeet-reports-ts.service") {
			t.Fatalf("fresh production retry touched payload or sidecar: %v", fixture.systemctlCalls)
		}
	}
}

func TestServiceScheduleRestartRecoveryPreservesLegacyNilIdentity(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.previous.Identity = nil
	fixture.plan.previous.Identity = nil
	fixture.plan.target.Identity = nil
	fixture.plan.identity = effectiveServiceIdentity(fixture.previous.View())
	if err := fixture.server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"reports": fixture.previous.Clone(),
	}}); err != nil {
		t.Fatal(err)
	}
	fixture.request = serviceScheduleMigrationRequest(fixture.plan)
	fixture.failSystemctlCall = "start reports.timer"
	fixture.failSystemctlRemaining = 2

	err := fixture.apply()
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") {
		t.Fatalf("legacy schedule mutation error = %v, want incomplete rollback", err)
	}
	fixture.failSystemctlRemaining = 0
	fixture.server = &Server{cfg: fixture.server.cfg}
	if err := fixture.server.recoverServiceIdentityMigrationsWithOps(context.Background(), fixture.ops()); err != nil {
		t.Fatalf("recover legacy schedule transaction: %v", err)
	}
	fixture.assertPreviousState()
	current, err := fixture.server.serviceView("reports")
	if err != nil {
		t.Fatal(err)
	}
	if current.AsStruct().Identity != nil {
		t.Fatalf("recovered legacy nil identity became explicit: %#v", current.AsStruct().Identity)
	}
	if _, statErr := os.Stat(fixture.plan.stagedTimer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy restart recovery retained schedule timer: %v", statErr)
	}
}

func TestServiceScheduleRecoveryRejectsCorruptedNilIdentityHeader(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.previous.Identity = nil
	fixture.plan.previous.Identity = nil
	fixture.plan.target.Identity = nil
	corruptIdentity := effectiveServiceIdentity(fixture.previous.View()).Persisted
	corruptIdentity.UID++
	header := serviceIdentityJournalHeader{
		Service: "reports", PreviousServicePresent: true,
		PreviousService: fixture.previous.Clone(), TargetService: fixture.plan.target.Clone(),
		TargetIdentity: corruptIdentity,
	}

	err := validateServiceIdentityRecoveryRecordIdentities(header, true)
	if err == nil || !strings.Contains(err.Error(), "target database identity does not match journal identity") {
		t.Fatalf("validateServiceIdentityRecoveryRecordIdentities = %v, want corrupted target identity rejection", err)
	}
}

func TestServiceScheduleTransactionPreservesTimerDirectivesAndServiceState(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	if err := fixture.apply(); err != nil {
		t.Fatalf("schedule mutation: %v", err)
	}
	fixture.assertTargetState()
	raw, err := os.ReadFile(fixture.installedTimer)
	if err != nil {
		t.Fatal(err)
	}
	wantTimer := string(fixture.newTimer)
	if string(raw) != wantTimer {
		t.Fatalf("installed timer = %q, want %q", raw, wantTimer)
	}
	for _, directive := range []string{"Persistent=true", "AccuracySec=2m", "RandomizedDelaySec=15m", "WantedBy=timers.target"} {
		if !strings.Contains(string(raw), directive) {
			t.Fatalf("installed timer lost %q: %s", directive, raw)
		}
	}
	if !fixture.active["reports.timer"] || fixture.active["reports.service"] ||
		!fixture.active["yeet-reports-ns.service"] || !fixture.active["yeet-reports-ts.service"] {
		t.Fatalf("target runtime state = %#v", fixture.active)
	}
}

func TestServiceScheduleTransactionFailsClosedBeforeStableMutation(t *testing.T) {
	fixture := newServiceScheduleTransactionFixture(t)
	fixture.failTimerInspection = true
	beforeEnabled := maps.Clone(fixture.enabled)
	beforeActive := maps.Clone(fixture.active)

	err := fixture.apply()
	if err == nil || !strings.Contains(err.Error(), "inspect exact timer runtime state") {
		t.Fatalf("schedule mutation error = %v, want timer preflight failure", err)
	}
	fixture.assertPreviousState()
	if !reflect.DeepEqual(fixture.enabled, beforeEnabled) || !reflect.DeepEqual(fixture.active, beforeActive) {
		t.Fatalf("failed preflight changed runtime: active=%v enabled=%v", fixture.active, fixture.enabled)
	}
	if len(fixture.systemctlCalls) != 0 {
		t.Fatalf("failed preflight invoked systemctl: %v", fixture.systemctlCalls)
	}
	if _, statErr := os.Stat(fixture.plan.stagedTimer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed preflight retained uncommitted source: %v", statErr)
	}
}

func TestServiceSchedulePlanningRejectsDriftedStableGenerationWithoutMutation(t *testing.T) {
	tests := []struct {
		name string
		path func(*serviceScheduleTransactionFixture) string
	}{
		{name: "primary", path: func(f *serviceScheduleTransactionFixture) string { return f.installedService }},
		{name: "netns auxiliary", path: func(f *serviceScheduleTransactionFixture) string { return f.installedAuxiliary }},
		{name: "Tailscale auxiliary", path: func(f *serviceScheduleTransactionFixture) string { return f.installedTSAuxiliary }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newServiceScheduleTransactionFixture(t)
			driftedPath := tt.path(fixture)
			drifted := []byte("operator drift must remain untouched\n")
			if err := os.WriteFile(driftedPath, drifted, 0o640); err != nil {
				t.Fatal(err)
			}
			databasePath := filepath.Join(fixture.server.cfg.RootDir, "db.json")
			databaseBefore, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			generationBefore := snapshotServiceScheduleDirectory(t, fixture.sourceDir)
			stableBefore := snapshotServiceScheduleDirectory(t, fixture.stableDir)
			activeBefore := maps.Clone(fixture.active)
			enabledBefore := maps.Clone(fixture.enabled)

			oldRequestForUpdate := serviceScheduleMigrationRequestForUpdate
			serviceScheduleMigrationRequestForUpdate = func(plan *serviceScheduleMutationPlan) serviceIdentityMigrationRequest {
				request := serviceScheduleMigrationRequest(plan)
				request.ops = fixture.ops()
				return request
			}
			t.Cleanup(func() { serviceScheduleMigrationRequestForUpdate = oldRequestForUpdate })
			err = fixture.server.updateServiceScheduleLocked(context.Background(), "reports", "30 2 * * *", io.Discard)
			if err == nil || !strings.Contains(err.Error(), "installed generation artifact") {
				t.Fatalf("updateServiceScheduleLocked error = %v, want installed-generation coherence rejection", err)
			}
			databaseAfter, readErr := os.ReadFile(databasePath)
			if readErr != nil || !reflect.DeepEqual(databaseAfter, databaseBefore) {
				t.Fatalf("rejected schedule database changed: %v", readErr)
			}
			if after := snapshotServiceScheduleDirectory(t, fixture.sourceDir); !reflect.DeepEqual(after, generationBefore) {
				t.Fatalf("rejected schedule generation changed:\n got %#v\nwant %#v", after, generationBefore)
			}
			if after := snapshotServiceScheduleDirectory(t, fixture.stableDir); !reflect.DeepEqual(after, stableBefore) {
				t.Fatalf("rejected schedule stable artifacts changed:\n got %#v\nwant %#v", after, stableBefore)
			}
			if !reflect.DeepEqual(fixture.active, activeBefore) || !reflect.DeepEqual(fixture.enabled, enabledBefore) {
				t.Fatalf("rejected schedule runtime changed: active=%v enabled=%v", fixture.active, fixture.enabled)
			}
			if len(fixture.systemctlCalls) != 0 {
				t.Fatalf("rejected schedule invoked systemctl: %v", fixture.systemctlCalls)
			}
		})
	}
}

type serviceScheduleTransactionFixture struct {
	t                            *testing.T
	server                       *Server
	previous                     *db.Service
	plan                         *serviceScheduleMutationPlan
	request                      serviceIdentityMigrationRequest
	installedService             string
	installedTimer               string
	installedAuxiliary           string
	installedTSAuxiliary         string
	sourceDir                    string
	stableDir                    string
	oldService                   []byte
	oldTimer                     []byte
	oldAuxiliary                 []byte
	oldTSAuxiliary               []byte
	newTimer                     []byte
	newAuxiliary                 []byte
	newTSAuxiliary               []byte
	active                       map[string]bool
	expectedActive               map[string]bool
	enabled                      map[string]bool
	expectedEnabled              map[string]bool
	systemctlCalls               []string
	failPhase                    string
	failTimerInspection          bool
	failIfReloadWhileTimerActive bool
	failSystemctlCall            string
	failSystemctlRemaining       int
	crossBoundaryOnTimerStop     bool
	persistentStampFresh         bool
	payloadActivations           int
	previousServiceStat          os.FileInfo
	previousTimerStat            os.FileInfo
	previousAuxStat              os.FileInfo
	previousTSAuxStat            os.FileInfo
}

func newServiceScheduleTransactionFixture(t *testing.T) *serviceScheduleTransactionFixture {
	t.Helper()
	server := newTestServer(t)
	root := filepath.Join(server.cfg.ServicesRoot, "reports")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "bin")
	unitSource := filepath.Join(sourceDir, "reports-4.service")
	timerSource := filepath.Join(sourceDir, "reports-4.timer")
	auxSource := filepath.Join(sourceDir, "reports-ns-4.service")
	tsSource := filepath.Join(sourceDir, "reports-ts-4.service")
	stagedTimer := filepath.Join(sourceDir, "reports-5.timer")
	unitBytes := []byte("[Service]\nExecStart=/srv/reports-4\nUser=root\nGroup=root\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n")
	oldTimer := []byte("[Unit]\nDescription=reports\n\n[Timer]\nOnCalendar=*-*-* 01:00:00\nPersistent=true\nAccuracySec=2m\nRandomizedDelaySec=15m\n\n[Install]\nWantedBy=timers.target\n")
	newTimer := []byte(strings.Replace(string(oldTimer), "OnCalendar=*-*-* 01:00:00", "OnCalendar=*-*-* 02:30", 1))
	newAux := []byte("[Service]\nExecStart=/bin/new-netns\n")
	newTSAux := []byte("[Service]\nExecStart=/bin/new-tailscaled\n")
	oldAux := append([]byte(nil), newAux...)
	oldTSAux := append([]byte(nil), newTSAux...)
	for path, raw := range map[string][]byte{
		unitSource: unitBytes, timerSource: oldTimer, auxSource: newAux, tsSource: newTSAux, stagedTimer: newTimer,
	} {
		if err := os.WriteFile(path, raw, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	identity := db.ServiceIdentity{
		RequestedUser: fmt.Sprint(os.Geteuid()), RequestedGroup: fmt.Sprint(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	previous := &db.Service{
		Name: "reports", ServiceType: db.ServiceTypeSystemd, Identity: &identity,
		ServiceRoot: root, Generation: 4, LatestGeneration: 4,
		Publish: []string{"127.0.0.1:8080:80"},
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(4): unitSource, "latest": unitSource}},
			db.ArtifactSystemdTimerFile: {
				Refs: map[db.ArtifactRef]string{db.Gen(4): timerSource, "latest": timerSource},
			},
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(4): auxSource, "latest": auxSource}},
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(4): tsSource, "latest": tsSource}},
		},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"reports": previous.Clone()}}); err != nil {
		t.Fatal(err)
	}
	target, err := cloneActiveServiceGeneration(previous, stagedTimer)
	if err != nil {
		t.Fatal(err)
	}
	stableDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = stableDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	installedService := filepath.Join(stableDir, "reports.service")
	installedTimer := filepath.Join(stableDir, "reports.timer")
	installedAuxiliary := filepath.Join(stableDir, "yeet-reports-ns.service")
	installedTSAuxiliary := filepath.Join(stableDir, "yeet-reports-ts.service")
	for path, raw := range map[string][]byte{
		installedService: unitBytes, installedTimer: oldTimer, installedAuxiliary: oldAux,
		installedTSAuxiliary: oldTSAux,
	} {
		if err := os.WriteFile(path, raw, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	activeInstaller, err := server.NewInstaller(InstallerCfg{ServiceName: "reports", ClientOut: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	activeService, err := newSystemdInstallService(activeInstaller, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activeService.StageInstallForReload(); err != nil {
		t.Fatal(err)
	}
	unitBytes, err = os.ReadFile(installedService)
	if err != nil {
		t.Fatal(err)
	}
	oldTimer, err = os.ReadFile(installedTimer)
	if err != nil {
		t.Fatal(err)
	}
	oldAux, err = os.ReadFile(installedAuxiliary)
	if err != nil {
		t.Fatal(err)
	}
	oldTSAux, err = os.ReadFile(installedTSAuxiliary)
	if err != nil {
		t.Fatal(err)
	}
	newAux = append([]byte(nil), oldAux...)
	newTSAux = append([]byte(nil), oldTSAux...)
	activeStates, err := activeService.InstallTargetStatesExcluding()
	if err != nil {
		t.Fatal(err)
	}
	activeIntent := serviceIdentityInstallTargetStates(activeStates)
	generationPaths, generationUnits, err := serviceIdentityExpectedGenerationTargets(target, root, installedService)
	if err != nil {
		t.Fatal(err)
	}
	intents := make([]serviceIdentityPathState, 0, len(generationPaths)-1)
	for _, path := range generationPaths {
		if filepath.Clean(path) == filepath.Clean(installedService) {
			continue
		}
		switch filepath.Clean(path) {
		case filepath.Clean(installedTimer):
			intents = append(intents, serviceIdentityDesiredFileState(path, newTimer, 0o640, uint32(os.Geteuid()), uint32(os.Getegid())))
		case filepath.Clean(installedAuxiliary):
			intents = append(intents, serviceIdentityDesiredFileState(path, newAux, 0o640, uint32(os.Geteuid()), uint32(os.Getegid())))
		case filepath.Clean(installedTSAuxiliary):
			intents = append(intents, serviceIdentityDesiredFileState(path, newTSAux, 0o640, uint32(os.Geteuid()), uint32(os.Getegid())))
		default:
			intents = append(intents, serviceIdentityPathState{Path: filepath.Clean(path)})
		}
	}
	plan := &serviceScheduleMutationPlan{
		previous: previous.Clone(), target: target, identity: resolvedServiceIdentity{Persisted: identity},
		replacement: string(unitBytes), stagedTimer: stagedTimer,
		generationPaths: generationPaths, intent: intents, activeIntent: activeIntent, units: generationUnits,
		timerPath: installedTimer, timerUnit: "reports.timer",
	}
	plan.stage = func(context.Context) error {
		for _, intent := range intents {
			if !intent.Present {
				if err := os.Remove(intent.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				continue
			}
			raw := newTimer
			if filepath.Clean(intent.Path) == filepath.Clean(installedAuxiliary) {
				raw = newAux
			} else if filepath.Clean(intent.Path) == filepath.Clean(installedTSAuxiliary) {
				raw = newTSAux
			}
			if err := writeServiceIdentityUnitAtomically(intent.Path, raw, intent.Mode.Perm()); err != nil {
				return err
			}
		}
		return nil
	}
	fixture := &serviceScheduleTransactionFixture{
		t: t, server: server, previous: previous.Clone(), plan: plan,
		installedService: installedService, installedTimer: installedTimer, installedAuxiliary: installedAuxiliary,
		installedTSAuxiliary: installedTSAuxiliary,
		sourceDir:            sourceDir, stableDir: stableDir,
		oldService: unitBytes, oldTimer: oldTimer, oldAuxiliary: oldAux, oldTSAuxiliary: oldTSAux,
		newTimer: newTimer, newAuxiliary: newAux, newTSAuxiliary: newTSAux,
		active: map[string]bool{
			"reports.timer": true, "reports.service": false, "yeet-reports-ns.service": true,
			"yeet-reports-ts.service": true,
		},
		enabled: map[string]bool{
			"reports.timer": false, "reports.service": false, "yeet-reports-ns.service": false,
			"yeet-reports-ts.service": true,
		},
	}
	fixture.request = serviceScheduleMigrationRequest(plan)
	fixture.expectedActive = maps.Clone(fixture.active)
	fixture.expectedEnabled = maps.Clone(fixture.enabled)
	fixture.previousServiceStat = mustStatServiceSchedulePath(t, installedService)
	fixture.previousTimerStat = mustStatServiceSchedulePath(t, installedTimer)
	fixture.previousAuxStat = mustStatServiceSchedulePath(t, installedAuxiliary)
	fixture.previousTSAuxStat = mustStatServiceSchedulePath(t, installedTSAuxiliary)
	oldActive, oldSystemctl := catchSystemdUnitActive, catchSystemctl
	catchSystemdUnitActive = func(unit string) bool { return fixture.active[unit] }
	catchSystemctl = fixture.systemctl
	t.Cleanup(func() { catchSystemdUnitActive, catchSystemctl = oldActive, oldSystemctl })
	return fixture
}

func (f *serviceScheduleTransactionFixture) apply() error {
	f.t.Helper()
	request := f.request
	request.ops = f.ops()
	return f.server.applyServiceScheduleMutationPlanLocked(context.Background(), f.plan, request, io.Discard)
}

func (f *serviceScheduleTransactionFixture) ops() *serviceIdentityMigrationOps {
	f.t.Helper()
	return &serviceIdentityMigrationOps{
		phase: func(phase string) error {
			if phase == f.failPhase {
				return errors.New("injected " + phase + " failure")
			}
			return nil
		},
		unitPath: func(string) string { return f.installedService },
		snapshot: func(context.Context, *db.Service) (string, error) {
			return "", nil
		},
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:   func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		restore: func(string) error { return nil },
		reload: func(context.Context) error {
			if f.failIfReloadWhileTimerActive && f.active["reports.timer"] {
				return errors.New("daemon reload occurred while schedule timer was active")
			}
			return nil
		},
		verify: func(context.Context, serviceIdentityMigrationVerification) error { return nil },
		isEnabled: func(_ context.Context, unit string) (bool, error) {
			return f.enabled[unit], nil
		},
		inspectScheduleTimer: func(_ context.Context, unit string) (serviceScheduleTimerRuntimeState, error) {
			if f.failTimerInspection {
				return serviceScheduleTimerRuntimeState{}, errors.New("injected timer inspection failure")
			}
			if f.active[unit] {
				return serviceScheduleTimerRuntimeState{ActiveState: "active", SubState: "waiting", Active: true}, nil
			}
			return serviceScheduleTimerRuntimeState{ActiveState: "inactive", SubState: "dead"}, nil
		},
		scheduleSystemctl: func(_ context.Context, args ...string) error {
			return f.systemctl(args...)
		},
		enable: func(_ context.Context, unit string) error {
			f.enabled[unit] = true
			f.systemctlCalls = append(f.systemctlCalls, "enable "+unit)
			return nil
		},
		disable: func(_ context.Context, unit string) error {
			f.enabled[unit] = false
			f.systemctlCalls = append(f.systemctlCalls, "disable "+unit)
			return nil
		},
	}
}

func (f *serviceScheduleTransactionFixture) systemctl(args ...string) error {
	f.t.Helper()
	call := strings.Join(args, " ")
	f.systemctlCalls = append(f.systemctlCalls, call)
	if call == f.failSystemctlCall && f.failSystemctlRemaining > 0 {
		f.failSystemctlRemaining--
		return errors.New("injected systemctl failure")
	}
	if len(args) == 3 && args[0] == "clean" && args[1] == "--what=state" {
		f.persistentStampFresh = true
		return nil
	}
	if len(args) != 2 {
		return nil
	}
	switch args[0] {
	case "start":
		if args[1] == "reports.timer" && f.crossBoundaryOnTimerStop && !f.persistentStampFresh {
			f.payloadActivations++
			f.active["reports.service"] = true
		}
		f.active[args[1]] = true
	case "stop":
		f.active[args[1]] = false
		if args[1] == "reports.timer" && f.crossBoundaryOnTimerStop {
			f.persistentStampFresh = false
		}
	case "enable":
		f.enabled[args[1]] = true
	case "disable":
		f.enabled[args[1]] = false
	}
	return nil
}

func (f *serviceScheduleTransactionFixture) assertPreviousState() {
	f.t.Helper()
	current, err := f.server.serviceView("reports")
	if err != nil || !reflect.DeepEqual(current.AsStruct(), f.previous) {
		f.t.Fatalf("rolled-back service = %#v, %v, want %#v", current.AsStruct(), err, f.previous)
	}
	assertServiceSchedulePathState(f.t, f.installedService, f.oldService, f.previousServiceStat)
	assertServiceSchedulePathState(f.t, f.installedTimer, f.oldTimer, f.previousTimerStat)
	assertServiceSchedulePathState(f.t, f.installedAuxiliary, f.oldAuxiliary, f.previousAuxStat)
	assertServiceSchedulePathState(f.t, f.installedTSAuxiliary, f.oldTSAuxiliary, f.previousTSAuxStat)
	if !reflect.DeepEqual(f.active, f.expectedActive) {
		f.t.Fatalf("rolled-back runtime = %#v, want %#v", f.active, f.expectedActive)
	}
	if !reflect.DeepEqual(f.enabled, f.expectedEnabled) {
		f.t.Fatalf("rolled-back enablement = %v, want %v", f.enabled, f.expectedEnabled)
	}
}

func (f *serviceScheduleTransactionFixture) assertTargetState() {
	f.t.Helper()
	current, err := f.server.serviceView("reports")
	if err != nil || !reflect.DeepEqual(current.AsStruct(), f.plan.target) {
		f.t.Fatalf("target service = %#v, %v, want %#v", current.AsStruct(), err, f.plan.target)
	}
	assertServiceSchedulePathState(f.t, f.installedService, f.oldService, nil)
	assertServiceSchedulePathState(f.t, f.installedTimer, f.newTimer, nil)
	assertServiceSchedulePathState(f.t, f.installedAuxiliary, f.newAuxiliary, nil)
	assertServiceSchedulePathState(f.t, f.installedTSAuxiliary, f.newTSAuxiliary, nil)
	stagedInfo, err := os.Stat(f.plan.stagedTimer)
	if err != nil {
		f.t.Fatalf("committed timer source missing: %v", err)
	}
	installedInfo, err := os.Stat(f.installedTimer)
	if err != nil || installedInfo.Mode().Perm() != stagedInfo.Mode().Perm() {
		f.t.Fatalf("installed timer mode = %v, %v, want generation source mode %v", installedInfo, err, stagedInfo.Mode().Perm())
	}
}

func mustStatServiceSchedulePath(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func assertServiceSchedulePathState(t *testing.T, path string, want []byte, previous os.FileInfo) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(want) {
		t.Fatalf("path %s bytes = %q, %v, want %q", path, raw, err, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if previous == nil {
		return
	}
	if info.Mode().Perm() != previous.Mode().Perm() {
		t.Fatalf("path %s mode = %o, want %o", path, info.Mode().Perm(), previous.Mode().Perm())
	}
	gotStat, gotOK := info.Sys().(*syscall.Stat_t)
	wantStat, wantOK := previous.Sys().(*syscall.Stat_t)
	if gotOK && wantOK && (gotStat.Uid != wantStat.Uid || gotStat.Gid != wantStat.Gid) {
		t.Fatalf("path %s owner = %d:%d, want %d:%d", path, gotStat.Uid, gotStat.Gid, wantStat.Uid, wantStat.Gid)
	}
}

func serviceScheduleDirectoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

type serviceScheduleFileSnapshot struct {
	Raw  string
	Mode os.FileMode
}

func snapshotServiceScheduleDirectory(t *testing.T, dir string) map[string]serviceScheduleFileSnapshot {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string]serviceScheduleFileSnapshot, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot[entry.Name()] = serviceScheduleFileSnapshot{Raw: string(raw), Mode: info.Mode()}
	}
	return snapshot
}

func prepareServiceSchedulePlanningPreflight(t *testing.T, server *Server, name, timerState string) string {
	t.Helper()
	stableDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = stableDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })

	if sv, err := server.serviceView(name); err == nil {
		installer, installerErr := server.NewInstaller(InstallerCfg{ServiceName: name, ClientOut: io.Discard})
		if installerErr == nil {
			if service, serviceErr := newSystemdInstallService(installer, sv.AsStruct()); serviceErr == nil {
				_, _ = service.StageInstallForReload()
			}
		}
	}

	return stubServiceScheduleSystemctl(t, timerState)
}

func stubServiceScheduleSystemctl(t *testing.T, timerState string) string {
	t.Helper()
	systemctlDir := t.TempDir()
	systemctlLog := filepath.Join(systemctlDir, "calls.log")
	systemctlPath := filepath.Join(systemctlDir, "systemctl")
	script := `#!/bin/sh
echo "$*" >> "$YEET_SCHEDULE_SYSTEMCTL_LOG"
case "$1" in
  show)
    active_state=${YEET_SCHEDULE_TIMER_STATE%%/*}
    sub_state=${YEET_SCHEDULE_TIMER_STATE#*/}
    printf 'ActiveState=%s\nSubState=%s\n' "$active_state" "$sub_state"
    ;;
  is-enabled)
    echo disabled
    exit 1
    ;;
  *)
    echo "unexpected mutating systemctl call: $*" >&2
    exit 99
    ;;
esac
`
	if err := os.WriteFile(systemctlPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", systemctlDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YEET_SCHEDULE_SYSTEMCTL_LOG", systemctlLog)
	t.Setenv("YEET_SCHEDULE_TIMER_STATE", timerState)
	return systemctlLog
}

func scheduledServiceSetCronFixture(unitPath, timerPath string) *db.Service {
	return &db.Service{
		Name: "reports", ServiceType: db.ServiceTypeSystemd,
		Generation: 4, LatestGeneration: 4,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{
				db.Gen(4): unitPath, "latest": unitPath,
			}},
			db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{
				db.Gen(4): timerPath, "latest": timerPath,
			}},
		},
	}
}
