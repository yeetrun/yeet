// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/util/set"
)

func TestCommitGenPlanForStagedInstallPromotesLatestAndGeneration(t *testing.T) {
	commit := generatedServiceCommitForGen(0, 2)

	if commit.srcRef != "staged" {
		t.Fatalf("srcRef = %q, want staged", commit.srcRef)
	}
	if !reflect.DeepEqual(commit.dstRefs, []string{"latest", "gen-3"}) {
		t.Fatalf("dstRefs = %#v, want latest and gen-3", commit.dstRefs)
	}
	if commit.generation != 3 {
		t.Fatalf("generation = %d, want 3", commit.generation)
	}
	if commit.latestGeneration != 3 {
		t.Fatalf("latestGeneration = %d, want 3", commit.latestGeneration)
	}
}

func TestCommitGenPlanForSpecificGenerationPromotesOnlyLatest(t *testing.T) {
	commit := generatedServiceCommitForGen(7, 9)

	if commit.srcRef != "gen-7" {
		t.Fatalf("srcRef = %q, want gen-7", commit.srcRef)
	}
	if !reflect.DeepEqual(commit.dstRefs, []string{"latest"}) {
		t.Fatalf("dstRefs = %#v, want latest only", commit.dstRefs)
	}
	if commit.generation != 7 {
		t.Fatalf("generation = %d, want 7", commit.generation)
	}
	if commit.latestGeneration != 9 {
		t.Fatalf("latestGeneration = %d, want existing latest 9", commit.latestGeneration)
	}
}

func TestCommitGenAppliesServiceArtifactsAndOwnImagesOnly(t *testing.T) {
	data := &db.Data{
		Images: map[db.ImageRepoName]*db.ImageRepo{
			"api/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"staged": {BlobHash: "sha256:api"},
				},
			},
			"other/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"staged": {BlobHash: "sha256:other"},
				},
			},
		},
	}
	service := &db.Service{
		Name:             "stored-api",
		LatestGeneration: 2,
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary: {
				Refs: map[db.ArtifactRef]string{
					"staged": "/tmp/api/bin/api-staged",
				},
			},
		},
	}

	commitGeneratedServiceRefs(data, service, "api", generatedServiceCommitForGen(0, service.LatestGeneration))

	if service.Generation != 3 || service.LatestGeneration != 3 {
		t.Fatalf("generation/latest = %d/%d, want 3/3", service.Generation, service.LatestGeneration)
	}
	artifact := service.Artifacts[db.ArtifactBinary].Refs
	if artifact["latest"] != "/tmp/api/bin/api-staged" || artifact["gen-3"] != "/tmp/api/bin/api-staged" {
		t.Fatalf("artifact refs after commit = %#v, want latest and gen-3 copied from staged", artifact)
	}
	ownImage := data.Images["api/app"].Refs
	if ownImage["latest"].BlobHash != "sha256:api" || ownImage["gen-3"].BlobHash != "sha256:api" {
		t.Fatalf("own image refs after commit = %#v, want latest and gen-3 copied from staged", ownImage)
	}
	otherImage := data.Images["other/app"].Refs
	if _, ok := otherImage["latest"]; ok {
		t.Fatalf("other service image gained latest ref: %#v", otherImage)
	}
	if _, ok := otherImage["gen-3"]; ok {
		t.Fatalf("other service image gained gen-3 ref: %#v", otherImage)
	}
}

func TestPruneServiceArtifactsRemovesOldGenerationsAndTracksKnownFiles(t *testing.T) {
	known := defaultKnownInstallFiles("api")
	service := &db.Service{
		Name:             "api",
		LatestGeneration: 15,
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary: {
				Refs: map[db.ArtifactRef]string{
					"latest": "/srv/api/bin/api-latest",
					"staged": "/srv/api/bin/api-staged",
					"gen-4":  "/srv/api/bin/api-4",
					"gen-5":  "/srv/api/bin/api-5",
					"gen-15": "/srv/api/bin/api-15",
				},
			},
		},
	}

	pruneServiceArtifacts(service, known)

	refs := service.Artifacts[db.ArtifactBinary].Refs
	if _, ok := refs["gen-4"]; ok {
		t.Fatalf("gen-4 was kept, want pruned: %#v", refs)
	}
	for _, ref := range []db.ArtifactRef{"latest", "staged", "gen-5", "gen-15"} {
		if _, ok := refs[ref]; !ok {
			t.Fatalf("%s was pruned, want kept: %#v", ref, refs)
		}
	}
	for _, file := range []string{"api", "netns.env", "env", "main.ts", "api-latest", "api-staged", "api-5", "api-15"} {
		if !known.Contains(file) {
			t.Fatalf("known files missing %q: %#v", file, known)
		}
	}
	if known.Contains("api-4") {
		t.Fatalf("known files kept pruned generation file api-4: %#v", known)
	}
}

func TestPruneInstallDirectoryKeepsOnlyKnownFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"api", "env", "old-bin"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}

	pruneInstallDirectory(dir, set.Set[string]{"api": {}, "env": {}})

	if _, err := os.Stat(filepath.Join(dir, "api")); err != nil {
		t.Fatalf("known file api was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "env")); err != nil {
		t.Fatalf("known file env was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old-bin")); !os.IsNotExist(err) {
		t.Fatalf("old-bin stat err = %v, want not exist", err)
	}
}

func TestInstallValidationRejectsPullForNonComposeServices(t *testing.T) {
	if err := validateInstallRequest(true, db.ServiceTypeSystemd); err == nil {
		t.Fatal("validateInstallRequest returned nil, want error")
	}
	if err := validateInstallRequest(true, db.ServiceTypeDockerCompose); err != nil {
		t.Fatalf("validateInstallRequest returned error for compose pull: %v", err)
	}
}

func TestInstallerDockerComposeCommandFactoryUsesInstallerNewCmd(t *testing.T) {
	var gotName string
	var gotArgs []string
	installer := &Installer{
		NewCmd: func(name string, args ...string) *exec.Cmd {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return exec.Command("echo")
		},
	}
	service := &svc.DockerComposeService{}

	installer.configureDockerComposeCommands(service)
	if service.NewCmd == nil {
		t.Fatal("NewCmd was not configured")
	}
	if service.NewCmdContext == nil {
		t.Fatal("NewCmdContext was not configured")
	}
	service.NewCmdContext(context.Background(), "docker", "compose", "ps")

	if gotName != "docker" {
		t.Fatalf("command name = %q, want docker", gotName)
	}
	if !reflect.DeepEqual(gotArgs, []string{"compose", "ps"}) {
		t.Fatalf("command args = %#v, want compose ps", gotArgs)
	}
}

func TestInstallerEventTypeForInstallUsesCreationOnlyForFirstGeneration(t *testing.T) {
	if got := installEventType(1); got != EventTypeServiceCreated {
		t.Fatalf("installEventType(1) = %s, want %s", got, EventTypeServiceCreated)
	}
	if got := installEventType(2); got != EventTypeServiceConfigChanged {
		t.Fatalf("installEventType(2) = %s, want %s", got, EventTypeServiceConfigChanged)
	}
}

func TestNewInstallerWiresServerConfigAndCommandFactory(t *testing.T) {
	server := newTestServer(t)
	inst, err := server.NewInstaller(InstallerCfg{ServiceName: "api", Pull: true})
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	if inst.s != server {
		t.Fatal("installer server was not wired")
	}
	if inst.icfg.ServiceName != "api" || !inst.icfg.Pull {
		t.Fatalf("installer cfg = %#v", inst.icfg)
	}
	if inst.NewCmd == nil {
		t.Fatal("NewCmd was not configured")
	}
}

func TestInstallerCommitGenMutatesDatabase(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:             "api",
				LatestGeneration: 1,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"staged": "/srv/api/bin/api-staged"}},
				},
			},
		},
		Images: map[db.ImageRepoName]*db.ImageRepo{
			"api/app": {
				Refs: map[db.ImageRef]db.ImageManifest{"staged": {BlobHash: "sha256:api"}},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: "api"}}
	_, service, err := inst.commitGen(0)
	if err != nil {
		t.Fatalf("commitGen: %v", err)
	}
	if service.Generation != 2 || service.LatestGeneration != 2 {
		t.Fatalf("generation/latest = %d/%d, want 2/2", service.Generation, service.LatestGeneration)
	}
	if service.Artifacts[db.ArtifactBinary].Refs["latest"] != "/srv/api/bin/api-staged" {
		t.Fatalf("latest artifact not promoted: %#v", service.Artifacts)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	if got := dv.AsStruct().Images["api/app"].Refs["gen-2"].BlobHash; got != "sha256:api" {
		t.Fatalf("image gen-2 digest = %q, want sha256:api", got)
	}
}

func TestInstallerPruneMutatesRefsAndInstallDirs(t *testing.T) {
	server := newTestServer(t)
	for _, dir := range []string{server.serviceBinDir("api"), server.serviceEnvDir("api")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for _, name := range []string{"api", "current.bin", "old.bin"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:             "api",
				LatestGeneration: 15,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {
						Refs: map[db.ArtifactRef]string{
							"latest": "/srv/api/bin/current.bin",
							"gen-4":  "/srv/api/bin/old.bin",
							"gen-15": "/srv/api/bin/current.bin",
						},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	(&Installer{s: server, icfg: InstallerCfg{ServiceName: "api"}}).prune()

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	refs := dv.AsStruct().Services["api"].Artifacts[db.ArtifactBinary].Refs
	if _, ok := refs["gen-4"]; ok {
		t.Fatalf("old generation was not pruned: %#v", refs)
	}
	if _, err := os.Stat(filepath.Join(server.serviceBinDir("api"), "old.bin")); !os.IsNotExist(err) {
		t.Fatalf("old bin stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(server.serviceEnvDir("api"), "old.bin")); !os.IsNotExist(err) {
		t.Fatalf("old env stat err = %v, want not exist", err)
	}
}

func TestPruneInstallDirectoryReportsReadError(t *testing.T) {
	err := pruneInstallDirectory(filepath.Join(t.TempDir(), "missing"), set.Set[string]{})
	if err == nil || !strings.Contains(err.Error(), "failed to read directory") {
		t.Fatalf("pruneInstallDirectory error = %v", err)
	}
}

func TestInstallPhaseSelectionAndValidation(t *testing.T) {
	if phase, err := installPhaseForServiceType(db.ServiceTypeSystemd); err != nil || phase == nil {
		t.Fatalf("systemd phase = %v, %v", phase, err)
	}
	if phase, err := installPhaseForServiceType(db.ServiceTypeDockerCompose); err != nil || phase == nil {
		t.Fatalf("docker phase = %v, %v", phase, err)
	}
	if _, err := installPhaseForServiceType(db.ServiceType("bogus")); err == nil {
		t.Fatal("expected unknown service type error")
	}

	inst := &Installer{}
	err := inst.doInstall(nil, &db.Service{ServiceType: db.ServiceType("bogus")})
	if err == nil {
		t.Fatal("expected unknown service type error")
	}
	inst.icfg.Pull = true
	if err := inst.doInstall(nil, &db.Service{ServiceType: db.ServiceTypeSystemd}); err == nil {
		t.Fatal("expected pull validation error")
	}
}

type recordingCloser struct {
	closed bool
	err    error
}

func (c *recordingCloser) Close() error {
	c.closed = true
	return c.err
}

func TestCloseSelfUpdateClientOnlyClosesCatchService(t *testing.T) {
	closer := &recordingCloser{err: errors.New("ignored")}
	inst := &Installer{icfg: InstallerCfg{ClientCloser: closer}}

	closeSelfUpdateClient(inst, "api")
	if closer.closed {
		t.Fatal("non-catch service closed client")
	}
	closeSelfUpdateClient(inst, CatchService)
	if !closer.closed {
		t.Fatal("catch service did not close client")
	}
}

type recordingProgressUI struct {
	suspended bool
}

func (u *recordingProgressUI) Start()                             {}
func (u *recordingProgressUI) Stop()                              {}
func (u *recordingProgressUI) Suspend()                           { u.suspended = true }
func (u *recordingProgressUI) StartStep(name string)              {}
func (u *recordingProgressUI) UpdateDetail(detail string)         {}
func (u *recordingProgressUI) DoneStep(detail string)             {}
func (u *recordingProgressUI) FailStep(detail string)             {}
func (u *recordingProgressUI) Printer(format string, args ...any) {}

func TestInstallerSuspendUIUsesConfiguredUI(t *testing.T) {
	ui := &recordingProgressUI{}
	(&Installer{icfg: InstallerCfg{UI: ui}}).suspendUI()
	if !ui.suspended {
		t.Fatal("UI was not suspended")
	}
	(&Installer{}).suspendUI()
}

func TestPublishInstallEventIncludesServiceView(t *testing.T) {
	server := newTestServer(t)
	ch := make(chan Event, 1)
	handle := server.AddEventListener(ch, nil)
	defer server.RemoveEventListener(handle)
	service := &db.Service{Name: "api", LatestGeneration: 2}

	(&Installer{s: server}).publishInstallEvent(service)

	event := <-ch
	if event.Type != EventTypeServiceConfigChanged || event.ServiceName != "api" {
		t.Fatalf("event = %#v", event)
	}
	if event.Data.Data == nil {
		t.Fatal("event data missing service view")
	}
}

func TestAsJSONReportsMarshalError(t *testing.T) {
	got := asJSON(make(chan int))
	if !strings.Contains(got, "failed to marshal") {
		t.Fatalf("asJSON = %q, want marshal error", got)
	}
}
