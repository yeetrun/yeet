// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
