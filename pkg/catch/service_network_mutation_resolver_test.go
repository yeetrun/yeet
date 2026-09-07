// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestRegularNetworkMutationGeneratedTailscaleResolverReadiness(t *testing.T) {
	for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
		for _, mode := range []string{"host", "lan"} {
			for _, storage := range []string{"default", "custom"} {
				t.Run(string(serviceType)+"/"+mode+"/"+storage, func(t *testing.T) {
					server, plan := newRegularNetworkResolverPlan(t, serviceType, mode, storage)
					mutation := &regularServiceNetworkMutation{server: server, plan: plan}
					guarded := &resolverReadyServiceNetworkMutationSteps{server: server, plan: plan, steps: mutation}
					if err := guarded.Stage(context.Background()); err != nil {
						t.Fatalf("stage generated Tailscale network through canonical readiness: %v", err)
					}
					target := mutation.target
					if target.Generation != plan.previous.Generation || target.ServiceRoot != plan.previous.ServiceRoot {
						t.Fatal("network staging changed payload generation or service root")
					}
					if diff := cmp.Diff(plan.previous.Macvlan, target.Macvlan); diff != "" {
						t.Fatalf("LAN identity changed (-want +got):\n%s", diff)
					}
					// Materialize distinct runtime copies as installation does, then run
					// the full installed-unit and generation-provenance checks as well.
					tuple := fixtureTuple(*target, tailscaleResolverGenerationCurrent)
					for artifact, runtime := range map[db.ArtifactName]string{
						db.ArtifactTSBinary: tuple.daemon, db.ArtifactTSEnv: tuple.environmentFile,
						db.ArtifactTSConfig: tuple.configFile, db.ArtifactTSService: tailscaleSidecarInstalledUnitPath(target.Name),
					} {
						path, ok := target.Artifacts.Gen(artifact, target.Generation)
						if !ok {
							t.Fatalf("missing generated %s", artifact)
						}
						raw, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						writeRegularNetworkResolverFile(t, runtime, raw)
					}
					if err := server.checkTailscaleResolverReady(context.Background(), *target); err != nil {
						t.Fatalf("installed generated Tailscale resolver readiness: %v", err)
					}
				})
			}
		}
	}
}

func newRegularNetworkResolverPlan(t *testing.T, serviceType db.ServiceType, mode, storage string) (*Server, *serviceNetworkMutationPlan) {
	t.Helper()
	server := newTestServer(t)
	useTestSystemdSystemDir(t)
	stubTailscaleResolverActive(t, nil)
	oldSubnetCheck := checkSvcSubnetAvailableFn
	checkSvcSubnetAvailableFn = func() error { return nil }
	t.Cleanup(func() { checkSvcSubnetAvailableFn = oldSubnetCheck })
	if serviceType == db.ServiceTypeSystemd {
		stubServiceNetworkStaticVerification(t)
	}
	root := server.defaultServiceRootDir("api")
	if storage == "custom" {
		// Dataset-backed services select their root independently of Catch's data root.
		root = filepath.Join(t.TempDir(), "pool", "services", "api")
	}
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	writeRegularNetworkResolverFile(t, server.catchRunnerPath(), []byte("catch\n"))
	version := tailscaleResolverFixtureDaemonVersion
	writeRegularNetworkResolverFile(t, filepath.Join(server.cfg.RootDir, "tsd", "tailscaled-"+version), []byte("tailscaled\n"))
	artifact := db.ArtifactSystemdUnit
	payload := []byte("[Unit]\n\n[Service]\nExecStart=/srv/api/bin/api\n\n[Install]\nWantedBy=multi-user.target\n")
	if serviceType == db.ServiceTypeDockerCompose {
		artifact = db.ArtifactDockerComposeFile
		payload = []byte("services:\n  web:\n    image: nginx\n    ports:\n      - '8090:8080'\n")
	}
	path := filepath.Join(root, "bin", "payload")
	writeRegularNetworkResolverFile(t, path, payload)
	previousMode := mode
	if mode == "tap" {
		previousMode = "host"
	}
	previous := &db.Service{
		Name: "api", ServiceType: serviceType, ServiceRoot: root, Generation: 2, LatestGeneration: 2,
		Network:   &db.ServiceNetworkConfig{Modes: []string{previousMode}},
		Artifacts: db.ArtifactStore{artifact: {Refs: map[db.ArtifactRef]string{db.Gen(2): path, "latest": path}}},
	}
	modes := []string{"svc", "ts"}
	if mode == "tap" {
		modes = []string{"ts"}
	}
	if mode == "lan" {
		previous.Macvlan = &db.MacvlanNetwork{Interface: "ymv-api", Parent: "eno1", Mac: "02:00:00:00:00:17"}
		modes = []string{"lan", "ts"}
	}
	addTestServices(t, server, *previous)
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous.Clone(),
		desired: db.ServiceNetworkConfig{Modes: modes, TSVersion: version, TSTags: []string{"tag:app"}},
		network: NetworkOpts{Interfaces: strings.Join(modes, ","), Modes: modes,
			Tailscale: TailscaleOpts{Version: version, Tags: []string{"tag:app"}, AuthKey: "test-auth-key"}},
	}
	t.Cleanup(func() {
		if plan.artifactTxn != nil {
			if err := plan.artifactTxn.rollback(server); err != nil {
				t.Errorf("discard staged artifacts: %v", err)
			}
		}
	})
	return server, plan
}

func writeRegularNetworkResolverFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegularNetworkMutationResolverFailureDiscardsGeneratedArtifacts(t *testing.T) {
	for _, test := range []struct {
		name, mode, want string
	}{
		{name: "effective drop-in", mode: "lan", want: "drop-in"},
		{name: "TAP without namespace", mode: "tap", want: "no network namespace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, plan := newRegularNetworkResolverPlan(t, db.ServiceTypeDockerCompose, test.mode, "custom")
			before, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := plan.previous.Artifacts.Gen(db.ArtifactDockerComposeFile, plan.previous.Generation)
			raw, err := os.ReadFile(payload)
			if err != nil {
				t.Fatal(err)
			}
			if test.mode == "lan" {
				tailscaleResolverUnitDropInPaths = func(context.Context, string) ([]string, error) {
					return []string{"/unmanaged/override.conf"}, nil
				}
			}
			mutation := &regularServiceNetworkMutation{server: server, plan: plan}
			guarded := &resolverReadyServiceNetworkMutationSteps{server: server, plan: plan, steps: mutation}
			err = runServiceNetworkMutation(context.Background(), guarded)
			if err == nil || !strings.Contains(err.Error(), "stage service network replacement") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mutation error = %v, want staging rejection containing %q", err, test.want)
			}
			if plan.artifactTxn == nil || !plan.artifactTxn.finished {
				t.Fatal("readiness rejection did not finish artifact rollback")
			}
			for path := range plan.artifactTxn.stagedPaths {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("staged artifact survived readiness rejection: %s: %v", path, err)
				}
			}
			after, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(before.AsStruct(), after.AsStruct()); diff != "" {
				t.Fatalf("readiness rejection changed service state (-before +after):\n%s", diff)
			}
			if got, err := os.ReadFile(payload); err != nil || string(got) != string(raw) {
				t.Fatalf("readiness rejection changed previous payload: %v", err)
			}
		})
	}
}
