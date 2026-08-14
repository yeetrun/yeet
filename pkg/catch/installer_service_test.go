// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/util/set"
)

func TestUnassignedIPSkipsLiveSvcNetworkIPs(t *testing.T) {
	old := liveSvcNetworkIPsFunc
	liveSvcNetworkIPsFunc = func() (map[netip.Addr]bool, error) {
		return map[netip.Addr]bool{
			netip.MustParseAddr("192.168.100.14"): true,
		}, nil
	}
	t.Cleanup(func() { liveSvcNetworkIPsFunc = old })
	dv := (&db.Data{Services: map[string]*db.Service{
		"svc-3":  {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")}},
		"svc-4":  {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.4")}},
		"svc-5":  {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.5")}},
		"svc-6":  {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.6")}},
		"svc-7":  {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.7")}},
		"svc-8":  {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.8")}},
		"svc-9":  {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.9")}},
		"svc-10": {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.10")}},
		"svc-11": {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.11")}},
		"svc-12": {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.12")}},
		"svc-13": {SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.13")}},
	}}).View()

	got, err := unassignedIP(dv)
	if err != nil {
		t.Fatalf("unassignedIP: %v", err)
	}
	if got.String() != "192.168.100.15" {
		t.Fatalf("unassignedIP = %s, want 192.168.100.15", got)
	}
}

func TestParseLiveSvcNetworkIPs(t *testing.T) {
	out := map[netip.Addr]bool{}
	parseLiveSvcNetworkIPs(out, netip.MustParsePrefix("192.168.100.0/24"), []byte(`308: y-2dcb-vp    inet 192.168.100.14/32 scope global y-2dcb-vp
309: eth0    inet 10.0.4.51/24 scope global eth0
310: br0    inet 192.168.100.254/32 scope global br0
bad line
`))

	for _, want := range []string{"192.168.100.14", "192.168.100.254"} {
		if !out[netip.MustParseAddr(want)] {
			t.Fatalf("parsed live IPs = %#v, missing %s", out, want)
		}
	}
	if out[netip.MustParseAddr("10.0.4.51")] {
		t.Fatalf("parsed LAN address outside svc range: %#v", out)
	}
}

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

func TestInitialInstallDesiredNetworkPersistsOnlyAfterSuccessfulActivation(t *testing.T) {
	server := newTestServer(t)
	service := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": service}}); err != nil {
		t.Fatal(err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "api"}, Network: NetworkOpts{
			Interfaces: "ts", Modes: []string{"ts"}, Tailscale: TailscaleOpts{Version: "1.101.284", Tags: []string{"tag:app"}, AuthKey: "tskey-auth-secret"},
		}},
		installedGeneration:   1,
		persistInitialNetwork: true,
	}
	if err := installer.persistInitialDesiredNetwork(); err != nil {
		t.Fatal(err)
	}
	got, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Network().AsStruct() == nil || !reflect.DeepEqual(got.Network().AsStruct().Modes, []string{"ts"}) || got.Network().AsStruct().TSVersion != "1.101.284" {
		t.Fatalf("persisted desired network = %#v", got.Network().AsStruct())
	}
	if strings.Contains(asJSON(got.AsStruct()), "tskey-auth-secret") {
		t.Fatal("persisted transient auth key")
	}
}

func TestInitialInstallDesiredNetworkDoesNotRecreateMissingService(t *testing.T) {
	server := newTestServer(t)
	installer := &FileInstaller{
		s:                   server,
		cfg:                 FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "api"}, Network: NetworkOpts{Interfaces: "host", Modes: []string{"host"}}},
		installedGeneration: 1, persistInitialNetwork: true,
	}
	err := installer.persistInitialDesiredNetwork()
	if err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("persistInitialDesiredNetwork error = %v, want disappeared service", err)
	}
	view, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	if _, ok := view.Services().GetOk("api"); ok {
		t.Fatal("desired-state commit recreated a missing service")
	}
}

func TestInstallISONativeOrdersActivationInspectionAndReady(t *testing.T) {
	recorder := &isoNativeInstallRecorder{}
	if err := installISONativeWith(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recorder.events, []string{"install", "restart", "inspect", "ready"}) {
		t.Fatalf("native ISO install events = %v", recorder.events)
	}

	recorder = &isoNativeInstallRecorder{failAt: "restart"}
	err := installISONativeWith(context.Background(), recorder)
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("installISONativeWith error = %v, want restart failure", err)
	}
	if !reflect.DeepEqual(recorder.events, []string{"install", "restart", "quarantine"}) {
		t.Fatalf("failed native ISO install events = %v", recorder.events)
	}
}

func TestMarkNativeISOReadyExactAcceptsOwnedWorkloadGateTransition(t *testing.T) {
	server := newTestServer(t)
	expected := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd,
		ISO: testISONativeRuntimeAllocation("api", iso.StateReserved),
	}
	current := expected.Clone()
	current.ISO.State = string(iso.StateReady)
	if err := server.cfg.DB.Set(&db.Data{
		ISOPool:  &db.ISOPool{AggregateRouteState: "ready"},
		Services: map[string]*db.Service{"api": current},
	}); err != nil {
		t.Fatal(err)
	}
	desired := &db.ServiceNetworkConfig{Modes: []string{"iso"}}

	if err := server.markNativeISOReadyExact(expected, desired); err != nil {
		t.Fatal(err)
	}

	got, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO().State() != string(iso.StateReady) || !reflect.DeepEqual(got.Network().AsStruct(), desired) {
		t.Fatalf("ready service = %#v, want owned gate transition with desired network", got.AsStruct())
	}
}

func TestISONativeInstallQuarantineAttributesRecordBeforeStoppingRuntime(t *testing.T) {
	cause := errors.New("activation failed")
	for _, tt := range []struct {
		name         string
		mutate       func(*db.Service)
		wantConflict bool
		wantStop     bool
		wantState    iso.AllocationState
	}{
		{
			name:      "attributable record is quarantined then stopped",
			wantStop:  true,
			wantState: iso.StateQuarantined,
		},
		{
			name: "owned workload gate transition is quarantined then stopped",
			mutate: func(service *db.Service) {
				service.ISO.State = string(iso.StateReady)
			},
			wantStop:  true,
			wantState: iso.StateQuarantined,
		},
		{
			name: "concurrent replacement is neither changed nor stopped",
			mutate: func(service *db.Service) {
				service.Network = &db.ServiceNetworkConfig{Modes: []string{"host"}}
				service.ISO = nil
			},
			wantConflict: true,
			wantState:    iso.StateReserved,
		},
		{
			name: "concurrent Compose replacement is neither changed nor stopped",
			mutate: func(service *db.Service) {
				service.ServiceType = db.ServiceTypeDockerCompose
				service.Network = &db.ServiceNetworkConfig{Modes: []string{"host"}}
				service.ISO = nil
				service.Artifacts = db.ArtifactStore{db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{
					"latest": "/unused/compose.yml",
				}}}
			},
			wantConflict: true,
			wantState:    iso.StateReserved,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			record := &db.Service{
				Name: "api", ServiceType: db.ServiceTypeSystemd,
				Network: &db.ServiceNetworkConfig{Modes: []string{"iso"}},
				ISO:     testISONativeRuntimeAllocation("api", iso.StateReserved),
				Artifacts: db.ArtifactStore{db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{
					"latest": filepath.Join(t.TempDir(), "api.service"),
				}}},
			}
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": record.Clone()}}); err != nil {
				t.Fatal(err)
			}
			expected := record.Clone()
			if tt.mutate != nil {
				if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
					tt.mutate(service)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := server.serviceView("api")
			if err != nil {
				t.Fatal(err)
			}
			oldSystemctl := runISOSystemctlForRuntime
			oldCompose := dockerComposeServiceForISO
			stopCalls := 0
			composeCalls := 0
			runISOSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
				if len(args) != 0 && args[0] == "stop" {
					stopCalls++
				}
				if len(args) != 0 && args[0] == "show" {
					return []byte("inactive\n"), nil
				}
				return nil, nil
			}
			dockerComposeServiceForISO = func(*Server, string) (*svc.DockerComposeService, error) {
				composeCalls++
				return nil, errors.New("unexpected Compose stop")
			}
			t.Cleanup(func() {
				runISOSystemctlForRuntime = oldSystemctl
				dockerComposeServiceForISO = oldCompose
			})

			steps := &isoNativeSystemdInstallSteps{si: &Installer{s: server}, record: expected}
			err = steps.Quarantine(context.Background(), cause)
			if !tt.wantConflict && err != nil {
				t.Fatal(err)
			}
			if tt.wantConflict && (err == nil || !strings.Contains(err.Error(), "changed")) {
				t.Fatalf("Quarantine error = %v, want exact-record conflict", err)
			}
			if got := stopCalls > 0; got != tt.wantStop {
				t.Fatalf("runtime stop invoked = %t, want %t", got, tt.wantStop)
			}
			if composeCalls != 0 {
				t.Fatalf("Compose stop callback invoked %d times, want 0", composeCalls)
			}
			after, err := server.serviceView("api")
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantConflict {
				if !reflect.DeepEqual(after.AsStruct(), before.AsStruct()) {
					t.Fatalf("concurrent record changed: got %#v, want %#v", after.AsStruct(), before.AsStruct())
				}
				return
			}
			if got := iso.AllocationState(after.ISO().State()); got != tt.wantState || after.ISO().LastError() != cause.Error() {
				t.Fatalf("quarantine state/error = %q/%q, want %q/%q", got, after.ISO().LastError(), tt.wantState, cause)
			}
		})
	}
}

type isoNativeInstallRecorder struct {
	events []string
	failAt string
}

func (r *isoNativeInstallRecorder) step(name string) error {
	r.events = append(r.events, name)
	if name == r.failAt {
		return errors.New(name + " failed")
	}
	return nil
}

func (r *isoNativeInstallRecorder) Install(context.Context) error { return r.step("install") }
func (r *isoNativeInstallRecorder) Restart(context.Context) error { return r.step("restart") }
func (r *isoNativeInstallRecorder) Inspect(context.Context) (isoReconcileRuntimeState, error) {
	if err := r.step("inspect"); err != nil {
		return "", err
	}
	return isoReconcileRuntimeRunning, nil
}
func (r *isoNativeInstallRecorder) MarkReady(context.Context) error { return r.step("ready") }
func (r *isoNativeInstallRecorder) Quarantine(context.Context, error) error {
	return r.step("quarantine")
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

func TestCommitGeneratedServiceRefsPromotesStagedSandboxPolicy(t *testing.T) {
	service := &db.Service{
		Name:             "api",
		LatestGeneration: 2,
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			"staged": {
				State:    "on",
				ReadOnly: []db.ServiceSandboxExposure{{Source: "/etc/ssl", Destination: "/etc/ssl"}},
				Writable: []db.ServiceSandboxExposure{{Source: "/srv/api/data", Destination: "/var/lib/api"}},
			},
		}},
	}

	commitGeneratedServiceRefs(nil, service, "api", generatedServiceCommitForGen(0, service.LatestGeneration))

	for _, ref := range []db.ArtifactRef{"latest", db.Gen(3)} {
		policy, ok := service.Sandbox.Refs[ref]
		if !ok || policy.State != "on" || !reflect.DeepEqual(policy.ReadOnly, []db.ServiceSandboxExposure{{Source: "/etc/ssl", Destination: "/etc/ssl"}}) {
			t.Fatalf("sandbox %s = %#v, %t; want staged policy", ref, policy, ok)
		}
	}
	staged := service.Sandbox.Refs["staged"]
	latest := service.Sandbox.Refs["latest"]
	gen3 := service.Sandbox.Refs[db.Gen(3)]
	if staged == latest || staged == gen3 || latest == gen3 {
		t.Fatalf("sandbox policies alias after staged commit: staged=%p latest=%p gen-3=%p", staged, latest, gen3)
	}

	staged.State = "off"
	if latest.State != "on" || gen3.State != "on" {
		t.Fatalf("destination states = %q/%q after staged mutation, want on/on", latest.State, gen3.State)
	}
	latest.ReadOnly[0].Destination = "/latest-only"
	if staged.ReadOnly[0].Destination != "/etc/ssl" || gen3.ReadOnly[0].Destination != "/etc/ssl" {
		t.Fatalf("source/other destination = %q/%q after latest mutation, want /etc/ssl", staged.ReadOnly[0].Destination, gen3.ReadOnly[0].Destination)
	}
	gen3.Writable[0].Source = "/gen-3-only"
	if staged.Writable[0].Source != "/srv/api/data" || latest.Writable[0].Source != "/srv/api/data" {
		t.Fatalf("source/other writable sources = %q/%q after gen-3 mutation, want /srv/api/data", staged.Writable[0].Source, latest.Writable[0].Source)
	}
}

func TestCommitGeneratedServiceRefsRollsBackSandboxPolicyToLatest(t *testing.T) {
	service := &db.Service{
		Name:             "api",
		LatestGeneration: 9,
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(7): {
				State:    "off",
				ReadOnly: []db.ServiceSandboxExposure{{Source: "/etc/ssl", Destination: "/etc/ssl"}},
				Writable: []db.ServiceSandboxExposure{{Source: "/srv/api/data", Destination: "/var/lib/api"}},
			},
			"latest": {State: "on"},
		}},
	}

	commitGeneratedServiceRefs(nil, service, "api", generatedServiceCommitForGen(7, service.LatestGeneration))

	if policy := service.Sandbox.Refs["latest"]; policy == nil || policy.State != "off" {
		t.Fatalf("latest sandbox policy = %#v, want gen-7 off policy", policy)
	}
	if policy := service.Sandbox.Refs[db.Gen(7)]; policy == nil || policy.State != "off" {
		t.Fatalf("gen-7 sandbox policy = %#v, want preserved off policy", policy)
	}
	gen7 := service.Sandbox.Refs[db.Gen(7)]
	latest := service.Sandbox.Refs["latest"]
	if gen7 == latest {
		t.Fatalf("sandbox policies alias after rollback: gen-7=%p latest=%p", gen7, latest)
	}
	gen7.ReadOnly[0].Destination = "/gen-7-only"
	if latest.ReadOnly[0].Destination != "/etc/ssl" {
		t.Fatalf("latest read-only destination = %q after gen-7 mutation, want /etc/ssl", latest.ReadOnly[0].Destination)
	}
	latest.Writable[0].Source = "/latest-only"
	if gen7.Writable[0].Source != "/srv/api/data" {
		t.Fatalf("gen-7 writable source = %q after latest mutation, want /srv/api/data", gen7.Writable[0].Source)
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

func TestPruneServiceArtifactsRemovesOldSandboxPolicies(t *testing.T) {
	service := &db.Service{
		Name:             "api",
		LatestGeneration: 15,
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			"latest":   {State: "on"},
			"staged":   {State: "off"},
			db.Gen(4):  {State: "off"},
			db.Gen(5):  {State: "on"},
			db.Gen(15): {State: "on"},
		}},
	}

	pruneServiceArtifacts(service, defaultKnownInstallFiles("api"))

	if _, ok := service.Sandbox.Refs[db.Gen(4)]; ok {
		t.Fatalf("gen-4 sandbox policy was kept, want pruned: %#v", service.Sandbox.Refs)
	}
	for _, ref := range []db.ArtifactRef{"latest", "staged", db.Gen(5), db.Gen(15)} {
		if _, ok := service.Sandbox.Refs[ref]; !ok {
			t.Fatalf("sandbox policy %s was pruned, want kept: %#v", ref, service.Sandbox.Refs)
		}
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

func TestInstallGenSnapshotsBeforeInstallPhase(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:             "api",
				ServiceType:      db.ServiceTypeSystemd,
				ServiceRootZFS:   "tank/apps/api",
				Generation:       1,
				LatestGeneration: 1,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"staged": "/srv/api/bin/api-staged"}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	var calls []string
	snapshotCreated := false
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "snapshot":
			dv, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatalf("DB.Get during snapshot: %v", err)
			}
			sv, ok := dv.Services().GetOk("api")
			if !ok {
				t.Fatal("missing api service during snapshot")
			}
			if sv.Generation() != 1 || sv.LatestGeneration() != 1 {
				t.Fatalf("snapshot ran after generation commit: generation/latest = %d/%d, want 1/1", sv.Generation(), sv.LatestGeneration())
			}
			snapshotCreated = true
			return "", "", nil
		case "list":
			return "", "", nil
		default:
			return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
		}
	}
	oldRunInstallPhase := runInstallPhaseForSnapshot
	runInstallPhaseForSnapshot = func(_ *Installer, s *db.Service) error {
		if !snapshotCreated {
			t.Fatal("install phase ran before snapshot was created")
		}
		if s.Generation != 2 || s.LatestGeneration != 2 {
			t.Fatalf("generation/latest = %d/%d, want 2/2", s.Generation, s.LatestGeneration)
		}
		return nil
	}
	t.Cleanup(func() {
		runInstallPhaseForSnapshot = oldRunInstallPhase
	})

	var out bytes.Buffer
	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: "api", ClientOut: &out}}
	if err := inst.installGen(0); err != nil {
		t.Fatalf("installGen: %v", err)
	}
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "snapshot ") {
		t.Fatalf("zfs calls = %#v, want snapshot first", calls)
	}
}

func TestInstallGenSnapshotFailureDoesNotCommitGeneration(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:             "api",
				ServiceType:      db.ServiceTypeSystemd,
				ServiceRootZFS:   "tank/apps/api",
				Generation:       1,
				LatestGeneration: 1,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"staged": "/srv/api/bin/api-staged"}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		if args[0] == "snapshot" {
			return "", "snapshot failed", errZFSCommandFailed
		}
		return "", "", nil
	}
	oldRunInstallPhase := runInstallPhaseForSnapshot
	runInstallPhaseForSnapshot = func(_ *Installer, _ *db.Service) error {
		t.Fatal("install phase ran after required snapshot failure")
		return nil
	}
	t.Cleanup(func() {
		runInstallPhaseForSnapshot = oldRunInstallPhase
	})

	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: "api"}}
	if err := inst.installGen(0); err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Fatalf("installGen error = %v, want snapshot failure", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, ok := dv.Services().GetOk("api")
	if !ok {
		t.Fatal("missing api service")
	}
	if sv.Generation() != 1 || sv.LatestGeneration() != 1 {
		t.Fatalf("generation/latest = %d/%d, want 1/1", sv.Generation(), sv.LatestGeneration())
	}
	if _, ok := sv.Artifacts().Get(db.ArtifactBinary).Refs().GetOk(db.Gen(2)); ok {
		t.Fatal("gen-2 artifact ref was committed after snapshot failure")
	}
}

func TestInstallGenIfCurrentRejectsAdvanceBetweenSnapshotAndCommit(t *testing.T) {
	server := newTestServer(t)
	previousBinary := "/srv/catch/bin/catch-1"
	installedBinary := "/srv/catch/bin/catch-2"
	advancedBinary := "/srv/catch/bin/catch-3"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		CatchService: {
			Name:             CatchService,
			ServiceType:      db.ServiceTypeSystemd,
			ServiceRootZFS:   "tank/apps/catch",
			Generation:       2,
			LatestGeneration: 2,
			Artifacts: db.ArtifactStore{db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
				db.Gen(1): previousBinary,
				db.Gen(2): installedBinary,
				"latest":  installedBinary,
			}}},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] != "snapshot" {
			return "", "", nil
		}
		_, _, err := server.cfg.DB.MutateService(CatchService, func(_ *db.Data, service *db.Service) error {
			service.Generation = 3
			service.LatestGeneration = 3
			service.Artifacts[db.ArtifactBinary].Refs[db.Gen(3)] = advancedBinary
			service.Artifacts[db.ArtifactBinary].Refs["latest"] = advancedBinary
			return nil
		})
		return "", "", err
	}
	previousInstallPhase := runInstallPhaseForSnapshot
	installPhaseCalls := 0
	runInstallPhaseForSnapshot = func(*Installer, *db.Service) error {
		installPhaseCalls++
		return nil
	}
	t.Cleanup(func() { runInstallPhaseForSnapshot = previousInstallPhase })

	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: CatchService}}
	err := inst.installGenIfCurrent(2, 1)
	if err == nil || !strings.Contains(err.Error(), "generation changed from expected 2 to 3") {
		t.Fatalf("installGenIfCurrent error = %v, want generation CAS failure", err)
	}
	if installPhaseCalls != 0 {
		t.Fatalf("systemd install phase calls = %d, want 0", installPhaseCalls)
	}
	service := testService(t, server, CatchService)
	if service.Generation != 3 || service.LatestGeneration != 3 {
		t.Fatalf("generation/latest = %d/%d, want concurrent 3/3 preserved", service.Generation, service.LatestGeneration)
	}
	if got := service.Artifacts[db.ArtifactBinary].Refs["latest"]; got != advancedBinary {
		t.Fatalf("latest binary = %q, want concurrent %q", got, advancedBinary)
	}
}

func TestInstallGenReportsRecoverySnapshotOnInstallFailure(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:             "api",
				ServiceType:      db.ServiceTypeSystemd,
				ServiceRootZFS:   "tank/apps/api",
				Generation:       1,
				LatestGeneration: 1,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"staged": "/srv/api/bin/api-staged"}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
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
	installErr := errors.New("install failed")
	oldRunInstallPhase := runInstallPhaseForSnapshot
	runInstallPhaseForSnapshot = func(_ *Installer, _ *db.Service) error {
		return installErr
	}
	t.Cleanup(func() {
		runInstallPhaseForSnapshot = oldRunInstallPhase
	})

	var out bytes.Buffer
	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: "api", ClientOut: &out}}
	if err := inst.installGen(0); !errors.Is(err, installErr) {
		t.Fatalf("installGen error = %v, want %v", err, installErr)
	}
	if got := out.String(); !strings.Contains(got, "recovery snapshot: tank/apps/api@yeet-") {
		t.Fatalf("output = %q, want recovery snapshot", got)
	}
}

func TestInstallGenSnapshotsSkipInitialDeploy(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:           "api",
				ServiceType:    db.ServiceTypeSystemd,
				ServiceRootZFS: "tank/apps/api",
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"staged": "/srv/api/bin/api-staged"}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		t.Fatalf("unexpected zfs command during initial deploy: %v", args)
		return "", "", nil
	}
	oldRunInstallPhase := runInstallPhaseForSnapshot
	runInstallPhaseForSnapshot = func(_ *Installer, s *db.Service) error {
		if s.Generation != 1 || s.LatestGeneration != 1 {
			t.Fatalf("generation/latest = %d/%d, want initial 1/1", s.Generation, s.LatestGeneration)
		}
		return nil
	}
	t.Cleanup(func() {
		runInstallPhaseForSnapshot = oldRunInstallPhase
	})

	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: "api"}}
	if err := inst.installGen(0); err != nil {
		t.Fatalf("installGen: %v", err)
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

func TestInstallerPruneCommittedGenerationReturnsExactPublishedRecord(t *testing.T) {
	server := newTestServer(t)
	service := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd,
		Generation: 15, LatestGeneration: 15,
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
				"latest": "/srv/api/bin/api-15",
				"gen-4":  "/srv/api/bin/api-4",
				"gen-15": "/srv/api/bin/api-15",
			}},
		},
		ISO: testISONativeRuntimeAllocation("api", iso.StateReserved),
	}
	if err := server.cfg.DB.Set(&db.Data{
		ISOPool:  &db.ISOPool{AggregateRouteState: "ready"},
		Services: map[string]*db.Service{"api": service},
	}); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: "api"}}
	expected, err := inst.pruneCommittedGeneration(15)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := expected.Artifacts[db.ArtifactBinary].Refs["gen-4"]; ok {
		t.Fatalf("returned service retained pruned generation: %#v", expected.Artifacts)
	}
	if err := server.markNativeISOReadyExact(expected, nil); err != nil {
		t.Fatalf("mark ready with post-prune record: %v", err)
	}
}

func TestInstallerPruneCommittedGenerationRejectsConcurrentGeneration(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"api": {Name: "api", Generation: 16, LatestGeneration: 16, Artifacts: db.ArtifactStore{}},
	}}); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{s: server, icfg: InstallerCfg{ServiceName: "api"}}
	if _, err := inst.pruneCommittedGeneration(15); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("pruneCommittedGeneration error = %v, want generation conflict", err)
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

func TestInstallDockerComposeServiceRoutesISOThroughSecurityLifecycle(t *testing.T) {
	called := false
	installer := &Installer{
		isoComposeInstall: func(service *db.Service) error {
			called = true
			if service.ISO == nil {
				t.Fatal("ISO lifecycle received service without allocation")
			}
			return nil
		},
	}
	service := &db.Service{Name: "app", ServiceType: db.ServiceTypeDockerCompose, ISO: &db.ISOAllocation{}}

	if err := installDockerComposeService(installer, service); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ISO Compose install bypassed security lifecycle")
	}
}

func TestInstallerStagesISOTailscaleResolverAndCurrentGenerationArtifactsWithExplicitAuth(t *testing.T) {
	server := newTestServer(t)
	serviceRoot := server.defaultServiceRootDir("app")
	for _, dir := range []string{serviceBinDirForRoot(serviceRoot), serviceRunDirForRoot(serviceRoot), filepath.Join(serviceRoot, "tailscale")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	allocation := testISOOverlayDNSAllocation([]string{"iso", "ts"})
	allocation.Kind = string(iso.PayloadCompose)
	service := &db.Service{
		Name: "app", ServiceType: db.ServiceTypeDockerCompose, Generation: 3, LatestGeneration: 3,
		ServiceRoot: serviceRoot, ISO: allocation, TSNet: &db.TailscaleNetwork{Interface: isoTailscaleInterface, Version: "1.2.3"},
	}
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		if data.Services == nil {
			data.Services = map[string]*db.Service{}
		}
		data.Services["app"] = service
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	installer := &Installer{s: server, isoTailscaleAuthKey: "tskey-explicit"}
	called := false
	install := func(root, name, namespace string, tsNet *db.TailscaleNetwork, authKey, resolvConf string) (map[db.ArtifactName]string, error) {
		called = true
		if root != serviceRoot || name != "app" || namespace != allocation.NetNS || authKey != "tskey-explicit" {
			t.Fatalf("install args = root %q name %q ns %q auth %q", root, name, namespace, authKey)
		}
		raw, err := os.ReadFile(resolvConf)
		if err != nil || string(raw) != "nameserver "+allocation.Gateway.String()+"\n" {
			t.Fatalf("ISO resolver = %q, %v", raw, err)
		}
		return map[db.ArtifactName]string{
			db.ArtifactTSService: filepath.Join(serviceRoot, "bin", "tailscale.service"),
			db.ArtifactTSConfig:  filepath.Join(serviceRoot, "tailscale", "tailscaled.json"),
		}, nil
	}

	if err := installer.installISOTailscale(context.Background(), service, install); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ISO Tailscale installer was not called")
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	artifacts := dv.Services().Get("app").Artifacts()
	for _, name := range []db.ArtifactName{db.ArtifactNetNSResolv, db.ArtifactTSService, db.ArtifactTSConfig} {
		artifact, ok := artifacts.GetOk(name)
		if !ok {
			t.Fatalf("artifact %q missing", name)
		}
		if _, ok := artifact.Refs().GetOk(db.Gen(3)); !ok {
			t.Fatalf("artifact %q missing current generation ref", name)
		}
	}
}

func TestISOComposeLifecyclePolicyPhaseAcquiresAndReleasesOperationLockOnFailure(t *testing.T) {
	server := newTestServer(t)
	oldAcquire := acquireISOOperationLockForRuntime
	acquired, released := 0, 0
	acquireISOOperationLockForRuntime = func(context.Context, string) (func(), error) {
		acquired++
		return func() { released++ }, nil
	}
	t.Cleanup(func() { acquireISOOperationLockForRuntime = oldAcquire })
	lifecycle := &isoComposeLifecycle{
		si:     &Installer{s: server},
		record: &db.Service{Name: "app"},
	}

	if err := lifecycle.EnsurePolicy(context.Background()); err == nil {
		t.Fatal("EnsurePolicy unexpectedly succeeded without persisted ISO state")
	}
	if acquired != 1 || released != 1 {
		t.Fatalf("ISO operation lock acquire/release = %d/%d, want 1/1 on failure", acquired, released)
	}
}

func TestISOComposeLifecyclePullHonorsInstallerPullOption(t *testing.T) {
	calls := 0
	lifecycle := &isoComposeLifecycle{
		si: &Installer{},
		pullCompose: func(context.Context) error {
			calls++
			return nil
		},
	}
	if err := lifecycle.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("pull calls = %d, want 0 when pull is disabled", calls)
	}

	lifecycle.si.icfg.Pull = true
	if err := lifecycle.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("pull calls = %d, want 1 when pull is enabled", calls)
	}
}

func TestISOComposeLifecycleCleanupPreservesCallerDeadline(t *testing.T) {
	wantDeadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), wantDeadline)
	defer cancel()
	var gotDeadline time.Time
	lifecycle := &isoComposeLifecycle{
		downCompose: func(ctx context.Context) error {
			var ok bool
			gotDeadline, ok = ctx.Deadline()
			if !ok {
				t.Fatal("cleanup context has no deadline")
			}
			return nil
		},
		stopAux: func() error { return nil },
	}
	if err := lifecycle.ComposeDownRemoveOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	if !gotDeadline.Equal(wantDeadline) {
		t.Fatalf("cleanup deadline = %v, want caller deadline %v", gotDeadline, wantDeadline)
	}
}

func TestISOComposeLifecycleStartAuxReleasesOperationLockOnCancellationAndError(t *testing.T) {
	startErr := errors.New("gate start failed")
	for _, tc := range []struct {
		name    string
		ctx     func() context.Context
		start   func() error
		wantErr error
	}{
		{
			name: "canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			start:   func() error { t.Fatal("started gate after cancellation"); return nil },
			wantErr: context.Canceled,
		},
		{name: "start error", ctx: context.Background, start: func() error { return startErr }, wantErr: startErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			released := false
			lifecycle := &isoComposeLifecycle{
				isoUnlock: func() { released = true },
				startAux: func() error {
					if !released {
						t.Fatal("gate start ran while ISO operation lock was held")
					}
					return tc.start()
				},
			}
			err := lifecycle.StartAux(tc.ctx())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("StartAux error = %v, want %v", err, tc.wantErr)
			}
			if !released {
				t.Fatal("StartAux did not release ISO operation lock")
			}
		})
	}
}

func TestISOComposeLifecycleRechecksAllocationAfterGateBeforeComposeUp(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateReserved)
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	view, err := server.serviceView("app")
	if err != nil {
		t.Fatal(err)
	}
	releasedBeforeGate := false
	reacquired := 0
	releasedAfterGate := 0
	oldAcquire := acquireISOOperationLockForRuntime
	acquireISOOperationLockForRuntime = func(context.Context, string) (func(), error) {
		reacquired++
		return func() { releasedAfterGate++ }, nil
	}
	t.Cleanup(func() { acquireISOOperationLockForRuntime = oldAcquire })
	composeUpCalled := false
	lifecycle := &isoComposeLifecycle{
		si:         &Installer{s: server},
		record:     view.AsStruct(),
		allocation: allocation.Clone(),
		isoUnlock:  func() { releasedBeforeGate = true },
		startAux: func() error {
			if !releasedBeforeGate {
				t.Fatal("gate started before releasing the initial ISO lock")
			}
			_, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
				service.ISO.RemoveRequested = true
				service.ISO.State = string(iso.StateTombstoned)
				return nil
			})
			return err
		},
		upCompose: func(context.Context) error {
			composeUpCalled = true
			return nil
		},
	}

	if err := lifecycle.StartAux(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = lifecycle.ComposeUp(context.Background())
	if err == nil || !strings.Contains(err.Error(), "changed while the ISO network gate was starting") {
		t.Fatalf("ComposeUp error = %v, want changed-allocation rejection", err)
	}
	if composeUpCalled {
		t.Fatal("Compose up ran after a concurrent ISO removal/state change")
	}
	if reacquired != 1 || releasedAfterGate != 1 {
		t.Fatalf("post-gate ISO lock acquire/release = %d/%d, want 1/1", reacquired, releasedAfterGate)
	}
}

func TestISOComposeLifecycleRechecksAllocationBeforeComposeCreate(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateReserved)
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	view, err := server.serviceView("app")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.ISO.RemoveRequested = true
		service.ISO.State = string(iso.StateTombstoned)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	reacquired := 0
	released := 0
	oldAcquire := acquireISOOperationLockForRuntime
	acquireISOOperationLockForRuntime = func(context.Context, string) (func(), error) {
		reacquired++
		return func() { released++ }, nil
	}
	t.Cleanup(func() { acquireISOOperationLockForRuntime = oldAcquire })
	createCalled := false
	lifecycle := &isoComposeLifecycle{
		si:         &Installer{s: server},
		record:     view.AsStruct(),
		allocation: allocation.Clone(),
		createCompose: func(context.Context) error {
			createCalled = true
			return nil
		},
	}

	err = lifecycle.AttachNetwork(context.Background())
	if err == nil || !strings.Contains(err.Error(), "changed while the ISO network gate was starting") {
		t.Fatalf("AttachNetwork error = %v, want changed-allocation rejection", err)
	}
	if createCalled {
		t.Fatal("Compose create ran after a concurrent ISO removal/state change")
	}
	if reacquired != 1 || released != 1 {
		t.Fatalf("pre-create ISO lock acquire/release = %d/%d, want 1/1", reacquired, released)
	}
}

func TestISOComposeLifecycleReadmitsExactInputsBeforeComposeCreate(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateReserved)
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	view, err := server.serviceView("app")
	if err != nil {
		t.Fatal(err)
	}
	withISORuntimeBackend(t, netns.BackendNFT)
	oldVerifyPolicy := verifyISOPolicyForRuntime
	oldVerifyTopology := verifyISOTopologyForRuntime
	verifyISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	verifyISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	t.Cleanup(func() {
		verifyISOPolicyForRuntime = oldVerifyPolicy
		verifyISOTopologyForRuntime = oldVerifyTopology
	})

	var events []string
	oldAcquire := acquireISOOperationLockForRuntime
	acquireISOOperationLockForRuntime = func(context.Context, string) (func(), error) {
		events = append(events, "lock")
		return func() { events = append(events, "unlock") }, nil
	}
	t.Cleanup(func() { acquireISOOperationLockForRuntime = oldAcquire })
	lifecycle := &isoComposeLifecycle{
		si:         &Installer{s: server},
		record:     view.AsStruct(),
		allocation: allocation.Clone(),
		readmitCompose: func(context.Context) error {
			events = append(events, "readmit")
			return nil
		},
		createCompose: func(context.Context) error {
			events = append(events, "create")
			return nil
		},
	}

	if err := lifecycle.AttachNetwork(context.Background()); err != nil {
		t.Fatal(err)
	}
	lifecycle.releaseISOLock()
	want := []string{"lock", "readmit", "create", "unlock"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("pre-create events = %#v, want %#v", events, want)
	}
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
