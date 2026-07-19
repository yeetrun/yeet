// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/ftdetect"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestFileInstallerCloseRechecksIdentityRecoveryInsideLock(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "api"},
		NoBinary:     true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	server.setServiceIdentityMutationBlock("api", errors.New("recovery required"))
	if err := installer.Close(); !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("Close error = %v, want identity recovery block", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dv.Services().GetOk("api"); ok {
		t.Fatal("Close mutated the service despite recovery block")
	}
}

func TestFileInstallerOwnsServiceLockForLifetime(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "api"},
		NoBinary:     true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	go func() {
		release := server.serviceOperationLocks.Lock("api")
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		t.Fatal("service lock was released before installer Close")
	case <-time.After(25 * time.Millisecond):
	}
	if err := installer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("service lock remained held after installer Close")
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestFileInstallerConstructorErrorReleasesServiceLock(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "api", ServiceRoot: root},
	}); err == nil {
		t.Fatal("NewFileInstaller error = nil, want invalid service root error")
	}

	release := server.serviceOperationLocks.Lock("api")
	release()
}

func TestFileInstallerCloseErrorAndFailReleaseLifetimeLock(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{name: "close-error"},
		{name: "fail-then-close", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			installer, err := NewFileInstaller(server, FileInstallerCfg{
				InstallerCfg: InstallerCfg{ServiceName: "api"},
				NoBinary:     true,
				StageOnly:    true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.fail {
				installer.Fail()
			} else {
				server.setServiceIdentityMutationBlock("api", errors.New("recovery required"))
			}

			acquired := make(chan struct{})
			go func() {
				release := server.serviceOperationLocks.Lock("api")
				close(acquired)
				release()
			}()
			select {
			case <-acquired:
				t.Fatal("Fail or pending Close released service lock")
			case <-time.After(25 * time.Millisecond):
			}
			if err := installer.Close(); err == nil {
				t.Fatal("Close error = nil, want failure")
			}
			select {
			case <-acquired:
			case <-time.After(time.Second):
				t.Fatal("service lock remained held after failed Close")
			}
		})
	}
}

func TestFileInstallerRollbackInstalledGenerationRestoresCapturedGeneration(t *testing.T) {
	server := newTestServer(t)
	root := server.cfg.ServicesRoot
	previousBinary := filepath.Join(root, CatchService, "bin", "catch-previous")
	currentBinary := filepath.Join(root, CatchService, "bin", "catch-current")
	for _, dir := range []string{filepath.Dir(previousBinary), filepath.Join(root, CatchService, "env")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}
	artifacts := db.ArtifactStore{
		db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
			db.Gen(1): previousBinary,
			db.Gen(2): currentBinary,
			"latest":  currentBinary,
		}},
	}
	addTestServices(t, server, db.Service{
		Name:             CatchService,
		ServiceType:      db.ServiceTypeSystemd,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts:        artifacts,
	})
	installer := &FileInstaller{
		s:                   server,
		cfg:                 FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: CatchService}},
		existingService:     First(server.serviceView(CatchService)),
		installedGeneration: 2,
		readInstalledGeneration: func() (int, error) {
			return testService(t, server, CatchService).Generation, nil
		},
		installGenerationIfCurrent: func(installer *Installer, expected, generation int) error {
			return installer.installGenIfCurrent(expected, generation)
		},
	}
	addTestServices(t, server, db.Service{
		Name:             CatchService,
		ServiceType:      db.ServiceTypeSystemd,
		Generation:       2,
		LatestGeneration: 2,
		Artifacts:        artifacts,
	})
	previousInstallPhase := runInstallPhaseForSnapshot
	installPhaseCalls := 0
	runInstallPhaseForSnapshot = func(_ *Installer, service *db.Service) error {
		installPhaseCalls++
		if service.Name != CatchService || service.ServiceType != db.ServiceTypeSystemd {
			t.Fatalf("rollback service = %#v, want Catch systemd service", service)
		}
		if service.Generation != 1 {
			t.Fatalf("rollback generation during install = %d, want 1", service.Generation)
		}
		if got := service.Artifacts[db.ArtifactBinary].Refs["latest"]; got != previousBinary {
			t.Fatalf("latest binary during rollback = %q, want %q", got, previousBinary)
		}
		return nil
	}
	t.Cleanup(func() { runInstallPhaseForSnapshot = previousInstallPhase })

	if err := installer.RollbackInstalledGeneration(); err != nil {
		t.Fatalf("RollbackInstalledGeneration: %v", err)
	}
	if installPhaseCalls != 1 {
		t.Fatalf("systemd install phase calls = %d, want 1", installPhaseCalls)
	}
	service := testService(t, server, CatchService)
	if service.Generation != 1 || service.LatestGeneration != 2 {
		t.Fatalf("generation/latest = %d/%d, want 1/2", service.Generation, service.LatestGeneration)
	}
}

func TestFileInstallerRollbackInstalledGenerationRequiresPreviousGeneration(t *testing.T) {
	installer := &FileInstaller{}
	if err := installer.RollbackInstalledGeneration(); err == nil || !strings.Contains(err.Error(), "no previous Catch generation") {
		t.Fatalf("RollbackInstalledGeneration error = %v, want no previous generation", err)
	}
}

func TestFileInstallerRollbackInstalledGenerationAvailable(t *testing.T) {
	tests := []struct {
		name      string
		installer *FileInstaller
		want      bool
	}{
		{name: "nil installer"},
		{name: "missing service", installer: &FileInstaller{}},
		{name: "generation zero", installer: &FileInstaller{existingService: (&db.Service{}).View()}},
		{name: "previous generation", installer: &FileInstaller{existingService: (&db.Service{Generation: 1}).View()}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.installer.RollbackInstalledGenerationAvailable(); got != tt.want {
				t.Fatalf("RollbackInstalledGenerationAvailable = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestFileInstallerCaptureInstalledGenerationUsesObservedGeneration(t *testing.T) {
	installer := &FileInstaller{
		existingService: (&db.Service{Generation: 4, LatestGeneration: 9}).View(),
		readInstalledGeneration: func() (int, error) {
			return 27, nil
		},
	}

	if err := installer.captureInstalledGenerationOrRollback(); err != nil {
		t.Fatalf("captureInstalledGenerationOrRollback: %v", err)
	}
	if installer.installedGeneration != 27 {
		t.Fatalf("installed generation = %d, want observed generation 27", installer.installedGeneration)
	}
}

func TestFileInstallerCaptureFailureRestoresPreviousGeneration(t *testing.T) {
	server := newTestServer(t)
	readErr := errors.New("read installed generation")
	rollbackErr := errors.New("restore previous generation")
	installedGenerationCalls := 0
	installer := &FileInstaller{
		s:                   server,
		cfg:                 FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: CatchService}},
		existingService:     (&db.Service{Generation: 4, LatestGeneration: 9}).View(),
		installedGeneration: 27,
		readInstalledGeneration: func() (int, error) {
			return 0, readErr
		},
		installGenerationIfCurrent: func(_ *Installer, expected, generation int) error {
			installedGenerationCalls++
			if expected != 27 {
				t.Fatalf("expected current generation = %d, want 27", expected)
			}
			if generation != 4 {
				t.Fatalf("restored generation = %d, want 4", generation)
			}
			return rollbackErr
		},
	}

	err := installer.captureInstalledGenerationOrRollback()
	if !errors.Is(err, readErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("capture error = %v, want read and rollback errors", err)
	}
	if installedGenerationCalls != 1 {
		t.Fatalf("generation install calls = %d, want 1", installedGenerationCalls)
	}
	if installer.installedGeneration != 27 {
		t.Fatalf("installed generation = %d, want exact committed generation 27", installer.installedGeneration)
	}
}

func TestFileInstallerRollbackCASPreservesConcurrentGenerationAdvance(t *testing.T) {
	server := newTestServer(t)
	previousBinary := "/srv/catch/bin/catch-4"
	installedBinary := "/srv/catch/bin/catch-27"
	advancedBinary := "/srv/catch/bin/catch-28"
	artifacts := db.ArtifactStore{db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
		db.Gen(4):  previousBinary,
		db.Gen(27): installedBinary,
		db.Gen(28): advancedBinary,
		"latest":   installedBinary,
	}}}
	addTestServices(t, server, db.Service{
		Name: CatchService, ServiceType: db.ServiceTypeSystemd,
		Generation: 27, LatestGeneration: 27, Artifacts: artifacts,
	})
	installPhaseCalls := 0
	previousInstallPhase := runInstallPhaseForSnapshot
	runInstallPhaseForSnapshot = func(*Installer, *db.Service) error {
		installPhaseCalls++
		return nil
	}
	t.Cleanup(func() { runInstallPhaseForSnapshot = previousInstallPhase })
	installer := &FileInstaller{
		s:                   server,
		cfg:                 FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: CatchService}},
		existingService:     (&db.Service{Generation: 4}).View(),
		installedGeneration: 27,
		installGenerationIfCurrent: func(inst *Installer, expected, target int) error {
			if expected != 27 || target != 4 {
				t.Fatalf("CAS arguments = expected %d target %d, want 27 and 4", expected, target)
			}
			_, _, err := server.cfg.DB.MutateService(CatchService, func(_ *db.Data, service *db.Service) error {
				service.Generation = 28
				service.LatestGeneration = 28
				service.Artifacts[db.ArtifactBinary].Refs["latest"] = advancedBinary
				return nil
			})
			if err != nil {
				t.Fatalf("advance generation: %v", err)
			}
			return inst.installGenIfCurrent(expected, target)
		},
	}

	err := installer.RollbackInstalledGeneration()
	if err == nil || !strings.Contains(err.Error(), "generation changed from expected 27 to 28") {
		t.Fatalf("RollbackInstalledGeneration error = %v, want concurrent advancement refusal", err)
	}
	if installPhaseCalls != 0 {
		t.Fatalf("systemd install phase calls = %d, want 0", installPhaseCalls)
	}
	service := testService(t, server, CatchService)
	if service.Generation != 28 || service.LatestGeneration != 28 {
		t.Fatalf("generation/latest = %d/%d, want concurrent 28/28 preserved", service.Generation, service.LatestGeneration)
	}
	if got := service.Artifacts[db.ArtifactBinary].Refs["latest"]; got != advancedBinary {
		t.Fatalf("latest binary = %q, want concurrent %q", got, advancedBinary)
	}
}

func TestHostDefaultRouteInterfaceFromProcRoute(t *testing.T) {
	routeTable := strings.Join([]string{
		"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT",
		"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0",
		"vmbr0\t00000000\t0104000A\t0003\t0\t0\t0\t00000000\t0\t0\t0",
	}, "\n")

	iface, err := hostDefaultRouteInterfaceFromProcRoute(strings.NewReader(routeTable))
	if err != nil {
		t.Fatalf("hostDefaultRouteInterfaceFromProcRoute returned error: %v", err)
	}
	if iface != "vmbr0" {
		t.Fatalf("interface = %q, want %q", iface, "vmbr0")
	}
}

func TestParseNetworkLANUsesHostDefaultRoute(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{
				ServiceName: "svc-lan",
			},
			Network: NetworkOpts{
				Interfaces: "lan",
			},
		},
	}

	if err := installer.parseNetwork(); err != nil {
		t.Fatalf("parseNetwork returned error: %v", err)
	}
	if installer.macvlan == nil {
		t.Fatalf("expected macvlan config to be created")
	}
	if installer.macvlan.Parent != "vmbr0" {
		t.Fatalf("macvlan parent = %q, want %q", installer.macvlan.Parent, "vmbr0")
	}
}

func TestParseNetworkISO(t *testing.T) {
	tailscale := TailscaleOpts{Version: "1.2.3", AuthKey: "tskey-auth"}
	macvlan := MacvlanOpts{Parent: "vmbr0", VLAN: 42, Mac: "02:00:00:00:00:42"}
	for _, tt := range []struct {
		raw     string
		kind    iso.PayloadKind
		wantISO bool
		wantErr string
	}{
		{raw: "iso", kind: iso.PayloadContainer, wantISO: true},
		{raw: "iso,ts", kind: iso.PayloadCompose, wantISO: true},
		{raw: "iso,svc", kind: iso.PayloadCompose, wantErr: "cannot combine"},
		{raw: "iso", kind: iso.PayloadNative, wantErr: "native root"},
	} {
		opts, err := parseNetworkForPayload(NetworkOpts{Interfaces: tt.raw, Tailscale: tailscale, Macvlan: macvlan}, tt.kind, false)
		if tt.wantErr == "" {
			if err != nil || opts.ISO != tt.wantISO {
				t.Fatalf("parseNetworkForPayload(%q) = %#v, %v", tt.raw, opts, err)
			}
			if !reflect.DeepEqual(opts.Tailscale, tailscale) || !reflect.DeepEqual(opts.Macvlan, macvlan) {
				t.Fatalf("parseNetworkForPayload(%q) lost options: %#v", tt.raw, opts)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Fatalf("error = %v, want %q", err, tt.wantErr)
		}
	}
}

func TestNetworkPayloadKind(t *testing.T) {
	for _, tt := range []struct {
		serviceType db.ServiceType
		want        iso.PayloadKind
	}{
		{serviceType: db.ServiceTypeVM, want: iso.PayloadVM},
		{serviceType: db.ServiceTypeDockerCompose, want: iso.PayloadCompose},
		{serviceType: db.ServiceTypeSystemd, want: iso.PayloadNative},
	} {
		if got := networkPayloadKind(tt.serviceType); got != tt.want {
			t.Fatalf("networkPayloadKind(%q) = %q, want %q", tt.serviceType, got, tt.want)
		}
	}
}

func TestPreparePayloadISOValidationPrecedesComposeRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yml")
	original := []byte("services:\n  app:\n    image: busybox\n    ports:\n      - 80:80\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "app"},
			Network:      NetworkOpts{Interfaces: "iso"},
			Publish:      []string{"8080:80"},
		},
	}
	if _, err := installer.preparePayloadInstall(path); err == nil || !strings.Contains(err.Error(), "published ports") {
		t.Fatalf("preparePayloadInstall error = %v, want published ports rejection", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compose after rejection: %v", err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Fatalf("compose was rewritten before ISO validation:\n%s", after)
	}
	if len(installer.artifacts) != 0 {
		t.Fatalf("artifacts = %#v, want none before ISO validation", installer.artifacts)
	}
}

func TestPreparePayloadISOFailsClosedOnRawComposePortInspection(t *testing.T) {
	for _, tt := range []struct {
		name    string
		compose string
	}{
		{
			name: "long form port object",
			compose: "services:\n  app:\n    image: busybox\n    ports:\n" +
				"      - target: 80\n        published: 8080\n",
		},
		{
			name:    "malformed raw ports",
			compose: "services:\n  app:\n    image: busybox\n    ports: 8080:80\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yml")
			original := []byte(tt.compose)
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatalf("write compose: %v", err)
			}
			installer := &FileInstaller{
				s: newTestServer(t),
				cfg: FileInstallerCfg{
					InstallerCfg: InstallerCfg{ServiceName: "app"},
					Network:      NetworkOpts{Interfaces: "iso"},
				},
			}
			if _, err := installer.preparePayloadInstall(path); err == nil || !strings.Contains(err.Error(), "inspect compose published ports") {
				t.Fatalf("preparePayloadInstall error = %v, want fail-closed inspection error", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read compose after rejection: %v", err)
			}
			if !reflect.DeepEqual(after, original) {
				t.Fatalf("compose changed before inspection rejection:\n%s", after)
			}
			if len(installer.artifacts) != 0 {
				t.Fatalf("artifacts = %#v, want none before inspection rejection", installer.artifacts)
			}
		})
	}
}

func TestPreparePayloadPublishResetClearsComposePorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(path, []byte("services:\n  app:\n    image: busybox\n    ports:\n      - 8080:80\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "app"},
			PublishReset: true,
		},
	}
	plan, err := installer.preparePayloadInstall(path)
	if err != nil {
		t.Fatalf("preparePayloadInstall: %v", err)
	}
	ports, err := readComposePorts(path, "app")
	if err != nil {
		t.Fatalf("readComposePorts: %v", err)
	}
	if len(ports) != 0 || !plan.publishSet || len(plan.publish) != 0 {
		t.Fatalf("ports = %#v plan = %#v, want explicit empty publish state", ports, plan)
	}
}

func TestValidateInstallNetworkRequestRejectsNativeISO(t *testing.T) {
	service := &db.Service{
		ServiceType: db.ServiceTypeSystemd,
		ISO:         &db.ISOAllocation{DesiredModes: []string{"iso"}},
	}
	if err := validateInstallNetworkRequest(service); err == nil || !strings.Contains(err.Error(), "native root") {
		t.Fatalf("validateInstallNetworkRequest error = %v, want native root rejection", err)
	}
}

func TestPrepareNoBinaryPublishResetUsesPersistedISONetwork(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("svc-reset", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeDockerCompose
		service.ISO = &db.ISOAllocation{DesiredModes: []string{"iso"}}
		return nil
	}); err != nil {
		t.Fatalf("seed ISO service: %v", err)
	}
	view, err := server.serviceView("svc-reset")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	installer := &FileInstaller{
		s:               server,
		existingService: view,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "svc-reset"},
			NoBinary:     true,
			PublishReset: true,
		},
	}
	if _, err := installer.prepareNoBinaryInstall(); !errors.Is(err, iso.ErrPublishedPorts) {
		t.Fatalf("prepareNoBinaryInstall error = %v, want ErrPublishedPorts", err)
	}
	if len(installer.artifacts) != 0 {
		t.Fatalf("artifacts = %#v, want none before ISO rejection", installer.artifacts)
	}
}

func TestPrepareNoBinaryPublishUsesPersistedISONetwork(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("svc-publish", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeDockerCompose
		service.ISO = &db.ISOAllocation{DesiredModes: []string{"iso"}}
		return nil
	}); err != nil {
		t.Fatalf("seed ISO service: %v", err)
	}
	view, err := server.serviceView("svc-publish")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	installer := &FileInstaller{
		s:               server,
		existingService: view,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "svc-publish"},
			NoBinary:     true,
			Publish:      []string{"8080:80"},
		},
	}
	if _, err := installer.prepareNoBinaryInstall(); !errors.Is(err, iso.ErrPublishedPorts) {
		t.Fatalf("prepareNoBinaryInstall error = %v, want ErrPublishedPorts", err)
	}
	if len(installer.artifacts) != 0 {
		t.Fatalf("artifacts = %#v, want none before ISO rejection", installer.artifacts)
	}
	service := testService(t, server, "svc-publish")
	if len(service.Publish) != 0 {
		t.Fatalf("persisted publish = %#v, want unchanged", service.Publish)
	}
}

func TestParseNetworkLANExplicitParentOverridesHostDefaultRoute(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{
				ServiceName: "svc-lan",
			},
			Network: NetworkOpts{
				Interfaces: "lan",
				Macvlan: MacvlanOpts{
					Parent: "eno1",
				},
			},
		},
	}

	if err := installer.parseNetwork(); err != nil {
		t.Fatalf("parseNetwork returned error: %v", err)
	}
	if installer.macvlan == nil {
		t.Fatalf("expected macvlan config to be created")
	}
	if installer.macvlan.Parent != "eno1" {
		t.Fatalf("macvlan parent = %q, want %q", installer.macvlan.Parent, "eno1")
	}
}

func TestParseNetworkLANReusesExistingMacvlanForSameTarget(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name: "svc-lan",
		Macvlan: &db.MacvlanNetwork{
			Interface: "ymv-existing",
			Parent:    "vmbr0",
			Mac:       "02:00:00:00:00:10",
		},
	})

	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{
				ServiceName: "svc-lan",
			},
			Network: NetworkOpts{
				Interfaces: "lan",
			},
		},
	}

	if err := installer.parseNetwork(); err != nil {
		t.Fatalf("parseNetwork returned error: %v", err)
	}
	if installer.macvlan == nil {
		t.Fatal("expected macvlan config")
	}
	if installer.macvlan.Interface != "ymv-existing" || installer.macvlan.Mac != "02:00:00:00:00:10" {
		t.Fatalf("macvlan = %#v, want existing interface and mac", installer.macvlan)
	}
}

func TestParseNetworkAppliesCombinedNetworkOptions(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:       "existing-svc",
		SvcNetwork: &db.SvcNetwork{IPv4: netipMustParseAddr(t, "192.168.100.3")},
	})

	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{
				ServiceName: "svc-combined",
			},
			Network: NetworkOpts{
				Interfaces: "ts,svc,lan",
				Tailscale: TailscaleOpts{
					Version:  "1.2.3",
					ExitNode: "100.64.0.1",
					Tags:     []string{"tag:yeet"},
					AuthKey:  "tskey-auth",
				},
				Macvlan: MacvlanOpts{
					Parent: "eno1",
					VLAN:   42,
					Mac:    "02:00:00:00:00:42",
				},
			},
		},
	}

	if err := installer.parseNetwork(); err != nil {
		t.Fatalf("parseNetwork returned error: %v", err)
	}
	if installer.tsNet == nil {
		t.Fatal("expected tailscale config")
	}
	if !strings.HasPrefix(installer.tsNet.Interface, "yts-") {
		t.Fatalf("tailscale interface = %q, want yts-*", installer.tsNet.Interface)
	}
	if installer.tsNet.Version != "1.2.3" {
		t.Fatalf("tailscale version = %q, want %q", installer.tsNet.Version, "1.2.3")
	}
	if installer.tsNet.ExitNode != "100.64.0.1" {
		t.Fatalf("tailscale exit node = %q, want %q", installer.tsNet.ExitNode, "100.64.0.1")
	}
	if len(installer.tsNet.Tags) != 1 || installer.tsNet.Tags[0] != "tag:yeet" {
		t.Fatalf("tailscale tags = %#v, want [tag:yeet]", installer.tsNet.Tags)
	}
	if installer.tsAuthKey != "tskey-auth" {
		t.Fatalf("tailscale auth key = %q, want %q", installer.tsAuthKey, "tskey-auth")
	}
	if installer.svcNet == nil {
		t.Fatal("expected svc network config")
	}
	if got := installer.svcNet.IPv4.String(); got != "192.168.100.4" {
		t.Fatalf("svc ip = %q, want %q", got, "192.168.100.4")
	}
	if installer.macvlan == nil {
		t.Fatal("expected macvlan config")
	}
	if !strings.HasPrefix(installer.macvlan.Interface, "ymv-") {
		t.Fatalf("macvlan interface = %q, want ymv-*", installer.macvlan.Interface)
	}
	if installer.macvlan.Parent != "eno1" {
		t.Fatalf("macvlan parent = %q, want %q", installer.macvlan.Parent, "eno1")
	}
	if installer.macvlan.VLAN != 42 {
		t.Fatalf("macvlan vlan = %d, want %d", installer.macvlan.VLAN, 42)
	}
	if installer.macvlan.Mac != "02:00:00:00:00:42" {
		t.Fatalf("macvlan mac = %q, want %q", installer.macvlan.Mac, "02:00:00:00:00:42")
	}
}

func TestRewriteSystemdUnitContentReplacesOnlyExecStart(t *testing.T) {
	input := strings.Join([]string{
		"[Unit]",
		"Description=old app",
		"",
		"[Service]",
		"Environment=MODE=prod",
		"  ExecStart=/old/app --stale",
		"ExecStartPost=/bin/true",
	}, "\n")

	got := rewriteSystemdUnitContent(input, "/srv/app", []string{"--flag", "value"})
	want := strings.Join([]string{
		"[Unit]",
		"Description=old app",
		"",
		"[Service]",
		"Environment=MODE=prod",
		"ExecStart=/srv/app --flag value",
		"ExecStartPost=/bin/true",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("rewritten unit:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildNetNSResolvConfIncludesOptionalSearchDomains(t *testing.T) {
	got := buildNetNSResolvConf("1.1.1.1", "svc.local example.com")
	want := "nameserver 1.1.1.1\nsearch svc.local example.com\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestSvcNetNSResolvConfUsesYeetDNS(t *testing.T) {
	t.Setenv("DEFAULT_NS", "")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	got := defaultSvcNetNSResolvConf()
	want := "nameserver 192.168.100.1\nsearch yeet.internal\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestSvcNetNSResolvConfExplicitNameserverOptsOut(t *testing.T) {
	t.Setenv("DEFAULT_NS", "1.1.1.1")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	got := defaultSvcNetNSResolvConf()
	want := "nameserver 1.1.1.1\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestNetNSResolvConfForScopesDNSByNetworkMode(t *testing.T) {
	t.Setenv("DEFAULT_NS", "")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	svcEnv := netns.Service{ServiceName: "svc"}
	applySvcNetwork(&svcEnv, &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")})
	if got := netNSResolvConfFor(&svcEnv, ""); got != "nameserver 192.168.100.1\nsearch yeet.internal\n" {
		t.Fatalf("svc resolv.conf = %q", got)
	}

	lanEnv := netns.Service{ServiceName: "lan", MacvlanParent: "vmbr0"}
	if got := netNSResolvConfFor(&lanEnv, ""); got != "" {
		t.Fatalf("lan resolv.conf = %q, want empty", got)
	}

	tsEnv := netns.Service{ServiceName: "ts", TailscaleTAPInterface: "yts0"}
	if got := netNSResolvConfFor(&tsEnv, tailscaledResolvConf); got != tailscaledResolvConf {
		t.Fatalf("tailscale tap resolv.conf = %q, want %q", got, tailscaledResolvConf)
	}
}

func TestConfigureNetworkRejectsSvcSubnetConflict(t *testing.T) {
	oldCheck := checkSvcSubnetAvailableFn
	oldLiveIPs := liveSvcNetworkIPsFunc
	t.Cleanup(func() { checkSvcSubnetAvailableFn = oldCheck })
	t.Cleanup(func() { liveSvcNetworkIPsFunc = oldLiveIPs })
	checkSvcSubnetAvailableFn = func() error {
		return fmt.Errorf("required service subnet 192.168.100.0/24 conflicts with existing host address 192.168.100.50/24 on eth0")
	}
	liveSvcNetworkIPsFunc = func() (map[netip.Addr]bool, error) {
		return nil, nil
	}

	server := newTestServer(t)
	if err := server.ensureDirs("conflict-svc", ""); err != nil {
		t.Fatalf("ensureDirs returned error: %v", err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "conflict-svc"},
			Network:      NetworkOpts{Interfaces: "svc"},
		},
	}

	_, err := installer.configureNetwork()
	if err == nil || !strings.Contains(err.Error(), "required service subnet 192.168.100.0/24 conflicts") {
		t.Fatalf("configureNetwork error = %v, want service subnet conflict", err)
	}
}

func TestCheckSvcSubnetAvailableRejectsHostConflicts(t *testing.T) {
	err := checkSvcSubnetAvailableWith(fakeSvcSubnetIPCommand(t, map[string]string{
		"ip -j addr show":             `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.168.100.50","prefixlen":24}]}]`,
		"ip -j route show table main": `[]`,
	}))
	if err == nil || !strings.Contains(err.Error(), "existing host address 192.168.100.50/24 on eth0") {
		t.Fatalf("checkSvcSubnetAvailableWith error = %v, want host address conflict", err)
	}

	err = checkSvcSubnetAvailableWith(fakeSvcSubnetIPCommand(t, map[string]string{
		"ip -j addr show":             `[]`,
		"ip -j route show table main": `[{"dst":"192.168.0.0/16","dev":"eth0"}]`,
	}))
	if err == nil || !strings.Contains(err.Error(), "existing host route 192.168.0.0/16 on eth0") {
		t.Fatalf("checkSvcSubnetAvailableWith error = %v, want host route conflict", err)
	}
}

func TestCheckSvcSubnetAvailableAllowsExistingYeetSvcSubnet(t *testing.T) {
	err := checkSvcSubnetAvailableWith(fakeSvcSubnetIPCommand(t, map[string]string{
		"ip -j addr show":             `[{"ifname":"yeet0","addr_info":[{"family":"inet","local":"192.168.100.1","prefixlen":32}]}]`,
		"ip -j route show table main": `[{"dst":"192.168.100.0/24","dev":"yeet0"},{"dst":"192.168.100.0/24","dev":"yvm-demo-b0"},{"dst":"192.168.100.16","dev":"yvm-demo-b1"},{"dst":"default","dev":"eth0","gateway":"10.0.0.1"}]`,
	}))
	if err != nil {
		t.Fatalf("checkSvcSubnetAvailableWith returned error: %v", err)
	}
}

func fakeSvcSubnetIPCommand(t *testing.T, output map[string]string) func(string, ...string) ([]byte, error) {
	t.Helper()
	return func(name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		out, ok := output[key]
		if !ok {
			t.Fatalf("unexpected command %q", key)
		}
		return []byte(out), nil
	}
}

func TestInstallerCloseStagesEnvFileAndCleansTemp(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "env-svc"},
		EnvFile:      true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpDir := installer.tmpDir
	tmpPath := installer.tempFilePath()

	n, err := installer.Write([]byte("A=1\n"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := installer.WriteAt([]byte("B=2\n"), int64(n)); err != nil {
		t.Fatalf("WriteAt returned error: %v", err)
	}
	if got, want := installer.Received(), float64(len("A=1\nB=2\n")); got != want {
		t.Fatalf("Received = %v, want %v", got, want)
	}
	_ = installer.Rate()

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := installer.Wait(); err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if !installer.closed {
		t.Fatal("installer was not marked closed")
	}
	if installer.File != nil {
		t.Fatal("temporary file handle was not cleared")
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists after Close: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temp path still exists after Close: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}

	service := testService(t, server, "env-svc")
	envPath := stagedArtifactPath(t, service, db.ArtifactEnvFile)
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", envPath, err)
	}
	if string(raw) != "A=1\nB=2\n" {
		t.Fatalf("env file content = %q, want staged payload", string(raw))
	}
}

func TestNewFileInstallerRemovesNewCustomRootAfterFailedInstall(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "custom-root")

	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "custom-root-svc",
			ServiceRoot: customRoot,
		},
		EnvFile:   true,
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	installer.Fail()
	if err := installer.Close(); err == nil || !strings.Contains(err.Error(), "installation failed") {
		t.Fatalf("Close error = %v, want installation failed cleanup", err)
	}

	if _, err := os.Lstat(customRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed new service root remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(server.defaultServiceRootDir("custom-root-svc"), "bin")); !os.IsNotExist(err) {
		t.Fatalf("default service root was created for custom-root-svc: %v", err)
	}
}

func TestNewFileInstallerFailedCustomServiceRootCanRetry(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "retry-root")

	first, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "retry-root-svc",
			ServiceRoot: customRoot,
		},
		EnvFile:   true,
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("first NewFileInstaller returned error: %v", err)
	}
	first.Fail()
	if err := first.Close(); err == nil || !strings.Contains(err.Error(), "installation failed") {
		t.Fatalf("first Close error = %v, want installation failed", err)
	}
	if _, err := server.serviceView("retry-root-svc"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView after failed install error = %v, want errServiceNotFound", err)
	}

	second, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "retry-root-svc",
			ServiceRoot: customRoot,
		},
		EnvFile:   true,
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("second NewFileInstaller returned error: %v", err)
	}
	second.Fail()
	_ = second.Close()
}

func TestNewFileInstallerPersistsCustomServiceRoot(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "persist-root")
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "persist-root-svc",
			ServiceRoot: customRoot,
		},
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	installer.ensureManagedServiceAccount = func() (resolvedServiceIdentity, error) {
		return resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000,
		}}, nil
	}
	if _, err := installer.Write([]byte("#!/bin/sh\nexit 0\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "persist-root-svc")
	if service.ServiceRoot != customRoot {
		t.Fatalf("ServiceRoot = %q, want %q", service.ServiceRoot, customRoot)
	}
	binaryPath := stagedArtifactPath(t, service, db.ArtifactBinary)
	if !strings.HasPrefix(binaryPath, filepath.Join(customRoot, "bin")+string(os.PathSeparator)) {
		t.Fatalf("binary staged at %q, want under custom root %q", binaryPath, customRoot)
	}
}

func TestNewFileInstallerPersistsSnapshotPolicy(t *testing.T) {
	server := newTestServer(t)
	enabled := false
	keep := 3
	required := false
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg:         InstallerCfg{ServiceName: "svc-snapshot-policy"},
		NoBinary:             true,
		StageOnly:            true,
		SnapshotPolicyChange: true,
		SnapshotPolicy: &db.SnapshotPolicy{
			Enabled:  &enabled,
			KeepLast: &keep,
			MaxAge:   "72h",
			Required: &required,
		},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, ok := dv.Services().GetOk("svc-snapshot-policy")
	if !ok {
		t.Fatal("missing service")
	}
	if sv.SnapshotPolicy().Enabled().Get() || sv.SnapshotPolicy().KeepLast().Get() != 3 || sv.SnapshotPolicy().MaxAge() != "72h" || sv.SnapshotPolicy().Required().Get() {
		t.Fatalf("SnapshotPolicy = %#v", sv.SnapshotPolicy().AsStruct())
	}
}

func TestNewFileInstallerClearsSnapshotPolicy(t *testing.T) {
	server := newTestServer(t)
	enabled := false
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"svc-snapshot-clear": {
			Name:           "svc-snapshot-clear",
			SnapshotPolicy: &db.SnapshotPolicy{Enabled: &enabled, MaxAge: "72h"},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg:         InstallerCfg{ServiceName: "svc-snapshot-clear"},
		NoBinary:             true,
		StageOnly:            true,
		SnapshotPolicyChange: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, ok := dv.Services().GetOk("svc-snapshot-clear")
	if !ok {
		t.Fatal("missing service")
	}
	if sv.SnapshotPolicy().Valid() {
		t.Fatalf("SnapshotPolicy = %#v, want nil", sv.SnapshotPolicy().AsStruct())
	}
}

func TestNewFileInstallerPatchesSnapshotPolicyFlags(t *testing.T) {
	server := newTestServer(t)
	enabled := false
	keep := 3
	required := true
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"svc-snapshot-patch": {
			Name: "svc-snapshot-patch",
			SnapshotPolicy: &db.SnapshotPolicy{
				Enabled:  &enabled,
				KeepLast: &keep,
				MaxAge:   "72h",
				Required: &required,
				Events:   []string{"run"},
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "svc-snapshot-patch"},
		NoBinary:     true,
		StageOnly:    true,
		snapshotPolicyFlags: &cli.ServiceSetFlags{
			SnapshotKeepLast: "inherit",
			SnapshotChange:   true,
		},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	sv, ok := dv.Services().GetOk("svc-snapshot-patch")
	if !ok {
		t.Fatal("missing service")
	}
	policy := sv.SnapshotPolicy()
	if policy.KeepLast().Valid() {
		t.Fatalf("KeepLast valid = true, want false")
	}
	if policy.Enabled().Get() || policy.MaxAge() != "72h" || !policy.Required().Get() || policy.Events().Len() != 1 || policy.Events().At(0) != "run" {
		t.Fatalf("SnapshotPolicy = %#v, want only keep-last cleared", policy.AsStruct())
	}
}

func TestNewFileInstallerPersistsZFSServiceRoot(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	mountpoint := filepath.Join(parent, "svc")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatalf("MkdirAll mountpoint: %v", err)
	}
	zfsRunner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: mountpoint, Exists: true},
	})
	server.zfsRunner = zfsRunner.Run
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName:    "svc",
			User:           "",
			ServiceRoot:    "tank/apps/svc",
			ServiceRootZFS: true,
		},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	if got := installer.serviceRoot; got != mountpoint {
		t.Fatalf("installer.serviceRoot = %q, want %q", got, mountpoint)
	}
	if installer.serviceRootZFS != "tank/apps/svc" {
		t.Fatalf("installer.serviceRootZFS = %q, want tank/apps/svc", installer.serviceRootZFS)
	}
	svc := &db.Service{Name: "svc"}
	installer.applyInstallServiceRoot(svc)
	if svc.ServiceRoot != mountpoint {
		t.Fatalf("ServiceRoot = %q, want %q", svc.ServiceRoot, mountpoint)
	}
	if svc.ServiceRootZFS != "tank/apps/svc" {
		t.Fatalf("ServiceRootZFS = %q, want tank/apps/svc", svc.ServiceRootZFS)
	}
	if err := installer.applyInstallPlanToService(&db.Service{Name: "svc"}, fileInstallPlan{}); err != nil {
		t.Fatalf("applyInstallPlanToService: %v", err)
	}
}

func TestNewFileInstallerPrintsZFSServiceRootWarnings(t *testing.T) {
	server := newTestServer(t)
	mountpoint := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountpoint, "existing-service-file"), []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: mountpoint, Exists: true},
	}).Run

	var out strings.Builder
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName:    "svc",
			ServiceRoot:    "tank/apps/svc",
			ServiceRootZFS: true,
			ClientOut:      &out,
		},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller: %v", err)
	}
	installer.Fail()
	_ = installer.Close()
	got := out.String()
	for _, want := range []string{
		`warning: ZFS dataset "tank/apps/svc" already exists`,
		`warning: ZFS service root "` + mountpoint + `" is not empty`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestNewFileInstallerExistingServiceRootSameRootSucceeds(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "same-root")
	addTestServices(t, server, db.Service{
		Name:        "same-root-svc",
		ServiceRoot: customRoot,
	})

	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "same-root-svc",
			ServiceRoot: customRoot,
		},
		EnvFile:   true,
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	installer.Fail()
	_ = installer.Close()
}

func TestNewFileInstallerExistingServiceRootMismatchRejectsWithServiceSetHint(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	existingRoot := filepath.Join(parent, "existing-root")
	requestedRoot := filepath.Join(parent, "requested-root")
	addTestServices(t, server, db.Service{
		Name:        "mismatch-root-svc",
		ServiceRoot: existingRoot,
	})

	_, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "mismatch-root-svc",
			ServiceRoot: requestedRoot,
		},
	})
	if err == nil {
		t.Fatal("expected service root mismatch error")
	}
	wantHint := "yeet service set mismatch-root-svc --service-root=" + requestedRoot
	if !strings.Contains(err.Error(), wantHint) {
		t.Fatalf("NewFileInstaller error = %v, want hint %q", err, wantHint)
	}
}

func TestNewFileInstallerConstructorFailureRemovesNewServiceRoot(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "new-service-root")

	_, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "constructor-failure", ServiceRoot: root},
		PayloadName:  "\x00",
	})
	if err == nil || !strings.Contains(err.Error(), "failed to create temp file") {
		t.Fatalf("NewFileInstaller error = %v, want temp file creation failure", err)
	}
	if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("new service root remains after constructor failure: %v", statErr)
	}
}

func TestInstallerCloseFailedCleansTempAndCachesError(t *testing.T) {
	var printed []string
	installer, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "failed-svc",
			Printer: func(format string, args ...any) {
				printed = append(printed, fmt.Sprintf(format, args...))
			},
		},
		EnvFile:   true,
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpDir := installer.tmpDir
	installer.Fail()

	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "installation failed") {
		t.Fatalf("Close error = %v, want installation failed", err)
	}
	if err := installer.Wait(); err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists after failed Close: %v", err)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "Installation of \"failed-svc\" failed") {
		t.Fatalf("printed messages = %#v, want installation failure", printed)
	}
	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "installation failed") {
		t.Fatalf("second Close error = %v, want cached installation failed", err)
	}
}

func TestInstallerCloseReturnsTempFileCloseError(t *testing.T) {
	installer, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "close-error-svc"},
		EnvFile:      true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpDir := installer.tmpDir
	if err := installer.File.Close(); err != nil {
		t.Fatalf("manual temp file close returned error: %v", err)
	}

	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "failed to close temporary file") {
		t.Fatalf("Close error = %v, want temporary file close error", err)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists after close error: %v", err)
	}
	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "failed to close temporary file") {
		t.Fatalf("second Close error = %v, want cached close error", err)
	}
}

func TestInstallerCloseWrapsInvalidPayloadInstallError(t *testing.T) {
	var printed []string
	installer, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "invalid-payload-svc",
			Printer: func(format string, args ...any) {
				printed = append(printed, fmt.Sprintf(format, args...))
			},
		},
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	if _, err := installer.Write([]byte("plain text without a known payload type")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "failed to install service") {
		t.Fatalf("Close error = %v, want wrapped install error", err)
	}
	if !strings.Contains(err.Error(), "unable to detect file type") {
		t.Fatalf("Close error = %v, want payload detection failure", err)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "Failed to install service") {
		t.Fatalf("printed messages = %#v, want install failure", printed)
	}
}

func TestInstallerCloseStagesGeneratedPythonComposeWithNetworkArtifacts(t *testing.T) {
	t.Setenv("DEFAULT_NS", "9.9.9.9")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "svc.local")

	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "py-svc"},
		Args:         []string{"--port", "8080"},
		Network: NetworkOpts{
			Interfaces: "svc",
		},
		StageOnly:   true,
		Publish:     []string{"8080:8080/tcp", "5353:5353/UDP"},
		PayloadName: "/client/path/main.py",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	if !strings.HasSuffix(installer.tempFilePath(), string(filepath.Separator)+"main.py") {
		t.Fatalf("temp file path = %q, want payload basename to be preserved", installer.tempFilePath())
	}
	if _, err := installer.Write([]byte("print('hello')\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "py-svc")
	if service.ServiceType != db.ServiceTypeDockerCompose {
		t.Fatalf("service type = %q, want docker compose", service.ServiceType)
	}
	if service.SvcNetwork == nil || service.SvcNetwork.IPv4.String() != "192.168.100.3" {
		t.Fatalf("service svc network = %#v, want first service IP", service.SvcNetwork)
	}
	pythonPath := stagedArtifactPath(t, service, db.ArtifactPythonFile)
	assertInstallerFileContent(t, pythonPath, "print('hello')\n")

	composePath := stagedArtifactPath(t, service, db.ArtifactDockerComposeFile)
	composeRaw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", composePath, err)
	}
	compose := string(composeRaw)
	for _, want := range []string{
		"ghcr.io/astral-sh/uv:python3.13-bookworm-slim",
		"- uv",
		"- run",
		"- /main.py",
		"- --port",
		"- \"8080\"",
		"- 8080:8080",
		"- 5353:5353/udp",
		fmt.Sprintf("%s:/data", server.serviceDataDir("py-svc")),
		fmt.Sprintf("%s:/main.py:ro", filepath.Join(server.serviceRunDir("py-svc"), "main.py")),
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("generated compose missing %q:\n%s", want, compose)
		}
	}

	if !reflect.DeepEqual(service.Publish, []string{"8080:8080", "5353:5353/udp"}) {
		t.Fatalf("Publish = %#v, want normalized publish ports", service.Publish)
	}

	resolvPath := stagedArtifactPath(t, service, db.ArtifactNetNSResolv)
	assertInstallerFileContent(t, resolvPath, "nameserver 9.9.9.9\nsearch svc.local\n")
	composeNetworkPath := stagedArtifactPath(t, service, db.ArtifactDockerComposeNetwork)
	composeNetworkRaw, err := os.ReadFile(composeNetworkPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", composeNetworkPath, err)
	}
	for _, want := range []string{
		"driver: yeet",
		"dev.catchit.netns: /var/run/netns/yeet-py-svc-ns",
	} {
		if !strings.Contains(string(composeNetworkRaw), want) {
			t.Fatalf("compose network missing %q:\n%s", want, string(composeNetworkRaw))
		}
	}
	for _, artifact := range []db.ArtifactName{db.ArtifactNetNSService, db.ArtifactNetNSEnv} {
		path := stagedArtifactPath(t, service, artifact)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat staged %s %q: %v", artifact, path, err)
		}
	}
}

func TestInstallerCloseStagesScriptPayloadWithSystemdUnit(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "script-svc"},
		Args:         []string{"--flag"},
		StageOnly:    true,
		PayloadName:  "run",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	installer.ensureManagedServiceAccount = func() (resolvedServiceIdentity, error) {
		return resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "70000", RequestedGroup: "70001", UID: 70000, GID: 70001,
		}}, nil
	}
	if _, err := installer.Write([]byte("#!/bin/sh\necho hi\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "script-svc")
	if service.ServiceType != db.ServiceTypeSystemd {
		t.Fatalf("service type = %q, want systemd", service.ServiceType)
	}
	binaryPath := stagedArtifactPath(t, service, db.ArtifactBinary)
	assertInstallerFileContent(t, binaryPath, "#!/bin/sh\necho hi\n")
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("Stat(%q) returned error: %v", binaryPath, err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("binary mode = %v, want 0755", info.Mode().Perm())
	}
	unitPath := stagedArtifactPath(t, service, db.ArtifactSystemdUnit)
	unitRaw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", unitPath, err)
	}
	unit := string(unitRaw)
	for _, want := range []string{
		fmt.Sprintf("ExecStart=%s --flag\n", binaryPath),
		fmt.Sprintf("WorkingDirectory=%s\n", server.serviceDataDir("script-svc")),
		fmt.Sprintf("EnvironmentFile=-%s\n", filepath.Join(server.serviceEnvDir("script-svc"), "env")),
		"User=70000\n",
		"Group=70001\n",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit missing %q:\n%s", want, unit)
		}
	}
}

func TestInstallerCloseStagesDockerComposePayloadAndPublishPorts(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "compose-svc"},
		StageOnly:    true,
		Publish:      []string{"127.0.0.1:8080:80/tcp", "  "},
		PayloadName:  "compose.yml",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	payload := "services:\n  compose-svc:\n    image: nginx:latest\n"
	if _, err := installer.Write([]byte(payload)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "compose-svc")
	if service.ServiceType != db.ServiceTypeDockerCompose {
		t.Fatalf("service type = %q, want docker compose", service.ServiceType)
	}
	composePath := stagedArtifactPath(t, service, db.ArtifactDockerComposeFile)
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", composePath, err)
	}
	got := string(raw)
	for _, want := range []string{
		"image: nginx:latest",
		"ports:",
		"- 127.0.0.1:8080:80",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("staged compose missing %q:\n%s", want, got)
		}
	}
	if !reflect.DeepEqual(service.Publish, []string{"127.0.0.1:8080:80"}) {
		t.Fatalf("Publish = %#v, want normalized publish ports", service.Publish)
	}
}

func TestInstallerCloseStagesComposeSvcDNSOverlay(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "compose-dns"},
		Network:      NetworkOpts{Interfaces: "svc"},
		StageOnly:    true,
		PayloadName:  "compose.yml",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	payload := "services:\n  app:\n    image: busybox\n  custom:\n    image: busybox\n    dns:\n      - 1.1.1.1\n"
	if _, err := installer.Write([]byte(payload)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "compose-dns")
	networkPath := stagedArtifactPath(t, service, db.ArtifactDockerComposeNetwork)
	raw, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", networkPath, err)
	}
	network := string(raw)
	for _, want := range []string{
		"services:",
		"app:",
		"192.168.100.1",
		"yeet.internal",
		"driver: yeet",
		"dev.catchit.netns: /var/run/netns/yeet-compose-dns-ns",
	} {
		if !strings.Contains(network, want) {
			t.Fatalf("compose network missing %q:\n%s", want, network)
		}
	}
	if strings.Contains(network, "custom:") {
		t.Fatalf("custom DNS service should not receive generated resolver stanza:\n%s", network)
	}
}

func TestISOComposeOverlayBypassesLegacyDNSOverlay(t *testing.T) {
	server := newTestServer(t)
	if err := server.ensureDirs("iso-compose", ""); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  api:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "iso-compose"},
			Network:      NetworkOpts{Interfaces: "iso", Modes: []string{"iso"}, ISO: true},
		},
		artifacts: map[db.ArtifactName]string{db.ArtifactDockerComposeFile: composePath},
	}
	env := netns.Service{
		ServiceName: "iso-compose",
		ServiceIP:   netip.MustParsePrefix("192.168.100.3/24"),
	}
	if err := installer.writeDockerComposeNetwork(env); err != nil {
		t.Fatal(err)
	}
	if path, ok := installer.artifacts[db.ArtifactDockerComposeNetwork]; ok {
		raw, readErr := os.ReadFile(path)
		t.Fatalf("ISO generated legacy DNS overlay %q (%q, %v)", path, raw, readErr)
	}
}

func TestInstallerCloseStagesComposeLANOverlayWithoutYeetDNS(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	hostDefaultRouteInterfaceFn = func() (string, error) { return "vmbr0", nil }
	t.Cleanup(func() { hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn })

	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "compose-lan"},
		Network:      NetworkOpts{Interfaces: "lan"},
		StageOnly:    true,
		PayloadName:  "compose.yml",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	if _, err := installer.Write([]byte("services:\n  app:\n    image: busybox\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "compose-lan")
	networkPath := stagedArtifactPath(t, service, db.ArtifactDockerComposeNetwork)
	raw, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", networkPath, err)
	}
	if strings.Contains(string(raw), "192.168.100.1") || strings.Contains(string(raw), "yeet.internal") {
		t.Fatalf("lan-only compose network should not include yeet DNS:\n%s", raw)
	}
	if _, ok := service.Artifacts[db.ArtifactNetNSResolv]; ok {
		t.Fatalf("lan-only service staged netns resolv artifact: %#v", service.Artifacts[db.ArtifactNetNSResolv])
	}
}

func TestNewSystemdUnitBindsResolverForLANNetNS(t *testing.T) {
	server := newTestServer(t)
	if err := server.ensureDirs("lan-systemd", ""); err != nil {
		t.Fatalf("ensureDirs returned error: %v", err)
	}
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}
	t.Cleanup(func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	})

	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "lan-systemd"},
			Network:      NetworkOpts{Interfaces: "lan"},
		},
	}

	unit, err := installer.newSystemdUnit(filepath.Join(server.serviceRunDir("lan-systemd"), "lan-systemd"))
	if err != nil {
		t.Fatalf("newSystemdUnit returned error: %v", err)
	}
	if unit.NetNS != "yeet-lan-systemd-ns" {
		t.Fatalf("unit NetNS = %q, want yeet-lan-systemd-ns", unit.NetNS)
	}
	if unit.ResolvConf != "/etc/netns/yeet-lan-systemd-ns/resolv.conf" {
		t.Fatalf("unit ResolvConf = %q, want LAN netns resolver bind", unit.ResolvConf)
	}
	units, err := unit.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	raw, err := os.ReadFile(units[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile rendered systemd unit returned error: %v", err)
	}
	rendered := string(raw)
	for _, want := range []string{
		"NetworkNamespacePath=/var/run/netns/yeet-lan-systemd-ns",
		"BindPaths=/etc/netns/yeet-lan-systemd-ns/resolv.conf:/etc/resolv.conf",
		"PrivateMounts=yes",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered systemd unit missing %q:\n%s", want, rendered)
		}
	}
}

func TestNewSystemdUnitUsesImmutableGenerationAndResolvedIdentity(t *testing.T) {
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "api"},
			Args:         []string{"--serve"},
		},
		resolvedIdentity: resolvedServiceIdentity{
			Persisted: db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1010},
			UserName:  "app",
			GroupName: "workers",
		},
	}
	exe := filepath.Join(root, "bin", "api-20260718.1")
	unit, err := installer.newSystemdUnit(exe)
	if err != nil {
		t.Fatalf("newSystemdUnit: %v", err)
	}
	if unit.Executable != exe || unit.WorkingDirectory != filepath.Join(root, "data") || unit.EnvFile != "-"+filepath.Join(root, "env", "env") {
		t.Fatalf("unit paths = %#v", unit)
	}
	if unit.User != "app" || unit.Group != "workers" {
		t.Fatalf("unit identity = %q:%q, want app:workers", unit.User, unit.Group)
	}
}

func TestNewSystemdUnitUsesNumericIdentityWithoutNames(t *testing.T) {
	server := newTestServer(t)
	installer := &FileInstaller{
		s:   server,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "api"}},
		resolvedIdentity: resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "70000", RequestedGroup: "70001", UID: 70000, GID: 70001,
		}},
	}
	unit, err := installer.newSystemdUnit(filepath.Join(server.serviceBinDir("api"), "api-1"))
	if err != nil {
		t.Fatal(err)
	}
	if unit.User != "70000" || unit.Group != "70001" {
		t.Fatalf("unit identity = %q:%q, want 70000:70001", unit.User, unit.Group)
	}
}

func TestNewSystemdUnitKeepsCatchPrivilegedWithoutIdentityDirectives(t *testing.T) {
	server := newTestServer(t)
	installer := &FileInstaller{
		s:   server,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: CatchService}},
		resolvedIdentity: resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "app", RequestedGroup: "app", UID: 1002, GID: 1003,
		}},
	}
	unit, err := installer.newSystemdUnit(filepath.Join(server.serviceBinDir(CatchService), "catch-1"))
	if err != nil {
		t.Fatal(err)
	}
	if unit.User != "" || unit.Group != "" {
		t.Fatalf("Catch unit identity = %q:%q, want privileged empty directives", unit.User, unit.Group)
	}
	if unit.EnvFile != "-"+filepath.Join(server.serviceEnvDir(CatchService), "env") {
		t.Fatalf("Catch unit env = %q, want managed env path", unit.EnvFile)
	}
}

func TestResolveNativeInstallIdentityDefersExistingChangesToMigration(t *testing.T) {
	current := db.ServiceIdentity{RequestedUser: "70000", RequestedGroup: "70001", UID: 70000, GID: 70001}
	rootIdentity, err := resolveServiceIdentity("root")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		existing      *db.Service
		runAs         string
		runAsSet      bool
		want          db.ServiceIdentity
		wantErr       string
		wantMigration bool
	}{
		{name: "new explicit numeric", runAs: "70000:70001", runAsSet: true, want: current},
		{name: "existing omitted preserves", existing: &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Identity: &current}, want: current},
		{name: "legacy nil explicit root persists", existing: &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd}, runAs: "root", runAsSet: true, want: rootIdentity.Persisted, wantMigration: true},
		{name: "existing same explicit", existing: &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Identity: &current}, runAs: "70000:70001", runAsSet: true, want: current},
		{name: "existing different migrates", existing: &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Identity: &current}, runAs: "70002:70003", runAsSet: true, want: db.ServiceIdentity{RequestedUser: "70002", RequestedGroup: "70003", UID: 70002, GID: 70003}, wantMigration: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installer := &FileInstaller{cfg: FileInstallerCfg{RunAs: tt.runAs, RunAsSet: tt.runAsSet}}
			if tt.existing != nil {
				installer.existingService = tt.existing.View()
			}
			err := installer.resolveNativeInstallIdentity()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if installer.resolvedIdentity.Persisted != tt.want {
				t.Fatalf("identity = %#v, want %#v", installer.resolvedIdentity.Persisted, tt.want)
			}
			if installer.identityMigrationNeeded != tt.wantMigration {
				t.Fatalf("identityMigrationNeeded = %t, want %t", installer.identityMigrationNeeded, tt.wantMigration)
			}
		})
	}
}

func TestResolveNativeInstallIdentitySelectsManagedDefaultForNewNativeService(t *testing.T) {
	want := resolvedServiceIdentity{Persisted: db.ServiceIdentity{
		RequestedUser: managedServiceUser, RequestedGroup: managedServiceUser, UID: 991, GID: 992,
	}}
	installer := &FileInstaller{
		ensureManagedServiceAccount: func() (resolvedServiceIdentity, error) { return want, nil },
	}
	if err := installer.resolveNativeInstallIdentity(); err != nil {
		t.Fatal(err)
	}
	if installer.resolvedIdentity != want || !installer.newNativeIdentity {
		t.Fatalf("resolved/new = %#v %t, want %#v true", installer.resolvedIdentity, installer.newNativeIdentity, want)
	}
}

func TestResolveNativeInstallIdentityTreatsCrashedProvisionalRowAsNew(t *testing.T) {
	want := resolvedServiceIdentity{Persisted: db.ServiceIdentity{
		RequestedUser: managedServiceUser, RequestedGroup: managedServiceUser, UID: 991, GID: 992,
	}}
	provisional := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, IdentityInstallPending: true,
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{"staged": "/var/lib/yeet/services/api/bin/api-staged"}},
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": "/var/lib/yeet/services/api/bin/api.service-staged"}},
		},
	}
	installer := &FileInstaller{
		existingService:             provisional.View(),
		ensureManagedServiceAccount: func() (resolvedServiceIdentity, error) { return want, nil },
	}
	if err := installer.resolveNativeInstallIdentity(); err != nil {
		t.Fatal(err)
	}
	if installer.resolvedIdentity != want || !installer.newNativeIdentity || !installer.nativePredecessorAbsent {
		t.Fatalf("resolved/new/absent = %#v %t %t, want %#v true true", installer.resolvedIdentity, installer.newNativeIdentity, installer.nativePredecessorAbsent, want)
	}
}

func TestNewNativeInstallRoutesIdentityAndGenerationThroughOneTransaction(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	unitArtifact := filepath.Join(serviceBinDirForRoot(root), "api.service")
	identity := db.ServiceIdentity{RequestedUser: "70000", RequestedGroup: "70001", UID: 70000, GID: 70001}
	unit := "[Service]\nUser=70000\nGroup=70001\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n"
	if err := os.WriteFile(unitArtifact, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root,
		Artifacts: db.ArtifactStore{db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": unitArtifact}}},
	}}}); err != nil {
		t.Fatal(err)
	}
	var got serviceIdentityMigrationRequest
	installer := &FileInstaller{
		s:                 server,
		cfg:               FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "api"}},
		resolvedIdentity:  resolvedServiceIdentity{Persisted: identity},
		newNativeIdentity: true,
		migrateServiceIdentityFunc: func(_ context.Context, req serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
			got = req
			return serviceIdentityMigrationResult{Current: req.Target}, nil
		},
	}
	if err := installer.installStagedService(); err != nil {
		t.Fatal(err)
	}
	if !got.StartNew || !got.PredecessorAbsent || got.TargetService == nil || got.TargetService.Generation != 1 || got.TargetService.LatestGeneration != 1 || got.TargetService.Identity == nil || *got.TargetService.Identity != identity {
		t.Fatalf("migration request = %#v", got)
	}
	if got.ReplacementUnit != unit || got.StageGeneration == nil || got.InstallGeneration != nil || installer.installedGeneration != 1 {
		t.Fatalf("replacement/stage/activate/generation = %q %t %t %d", got.ReplacementUnit, got.StageGeneration != nil, got.InstallGeneration != nil, installer.installedGeneration)
	}
	service, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if service.Identity().Valid() || service.Generation() != 0 {
		t.Fatalf("adapter mutated DB before engine commit: %#v", service.AsStruct())
	}
}

func TestInstallerCloseNoBinaryRewritesExistingSystemdArtifact(t *testing.T) {
	server := newTestServer(t)
	oldUnit := filepath.Join(server.serviceBinDir("nobin-svc"), "nobin-svc-old.service")
	if err := os.MkdirAll(filepath.Dir(oldUnit), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(oldUnit, []byte("[Service]\nExecStart=/old/bin --old\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:        "nobin-svc",
		ServiceType: db.ServiceTypeSystemd,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": oldUnit}},
		},
	})
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "nobin-svc"},
		Args:         []string{"--new"},
		NoBinary:     true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpPath := installer.tempFilePath()

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("no-binary temp payload still exists: %v", err)
	}
	service := testService(t, server, "nobin-svc")
	unitPath := stagedArtifactPath(t, service, db.ArtifactSystemdUnit)
	if unitPath == oldUnit {
		t.Fatalf("rewritten systemd unit reused old path %q", unitPath)
	}
	unitRaw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", unitPath, err)
	}
	wantExec := fmt.Sprintf("ExecStart=%s --new\n", filepath.Join(server.serviceRunDir("nobin-svc"), "nobin-svc"))
	if !strings.Contains(string(unitRaw), wantExec) {
		t.Fatalf("rewritten systemd unit missing %q:\n%s", wantExec, string(unitRaw))
	}
}

func TestInstallerCloseNoBinaryRegeneratesNetNSSystemdArtifact(t *testing.T) {
	server := newTestServer(t)
	identity := db.ServiceIdentity{
		RequestedUser: "70000", RequestedGroup: "70001", UID: 70000, GID: 70001,
	}
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}
	t.Cleanup(func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	})

	oldUnit := filepath.Join(server.serviceBinDir("nobin-lan"), "nobin-lan-old.service")
	if err := os.MkdirAll(filepath.Dir(oldUnit), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(oldUnit, []byte("[Service]\nExecStart=/old/bin\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:        "nobin-lan",
		ServiceType: db.ServiceTypeSystemd,
		Identity:    &identity,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": oldUnit}},
			db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{"latest": filepath.Join(server.serviceBinDir("nobin-lan"), "nobin-lan-1")}},
		},
	})
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "nobin-lan"},
		Network:      NetworkOpts{Interfaces: "lan"},
		NoBinary:     true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	service := testService(t, server, "nobin-lan")
	unitPath := stagedArtifactPath(t, service, db.ArtifactSystemdUnit)
	if unitPath == oldUnit {
		t.Fatalf("netns systemd unit reused old staged path %q", unitPath)
	}
	unitRaw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", unitPath, err)
	}
	unit := string(unitRaw)
	for _, want := range []string{
		"ExecStart=" + filepath.Join(server.serviceBinDir("nobin-lan"), "nobin-lan-1"),
		"User=70000",
		"Group=70001",
		"NetworkNamespacePath=/var/run/netns/yeet-nobin-lan-ns",
		"BindPaths=/etc/netns/yeet-nobin-lan-ns/resolv.conf:/etc/resolv.conf",
		"PrivateMounts=yes",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("regenerated systemd unit missing %q:\n%s", want, unit)
		}
	}
}

func TestPayloadDetectionDecompressesZstdPayload(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.py")
	compressed := filepath.Join(dir, "payload.py")
	if err := os.WriteFile(src, []byte("print('zstd')\n"), 0644); err != nil {
		t.Fatalf("WriteFile source returned error: %v", err)
	}
	if err := codecutil.ZstdCompress(src, compressed); err != nil {
		t.Fatalf("ZstdCompress returned error: %v", err)
	}

	got, err := detectInstallPayloadType(compressed)
	if err != nil {
		t.Fatalf("detectInstallPayloadType returned error: %v", err)
	}
	if got != ftdetect.Python {
		t.Fatalf("detected type = %v, want Python", got)
	}
	raw, err := os.ReadFile(compressed)
	if err != nil {
		t.Fatalf("ReadFile decompressed payload returned error: %v", err)
	}
	if string(raw) != "print('zstd')\n" {
		t.Fatalf("decompressed payload = %q, want original source", string(raw))
	}
	if _, err := os.Stat(compressed + ".unpack"); !os.IsNotExist(err) {
		t.Fatalf("temporary unpack file still exists: %v", err)
	}
}

func TestPayloadDetectionCleansInvalidZstdUnpackFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.py")
	if err := os.WriteFile(path, []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00}, 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err := detectInstallPayloadType(path)
	if err == nil || !strings.Contains(err.Error(), "failed to decompress file") {
		t.Fatalf("detectInstallPayloadType error = %v, want decompress failure", err)
	}
	if _, err := os.Stat(path + ".unpack"); !os.IsNotExist(err) {
		t.Fatalf("temporary unpack file still exists: %v", err)
	}
}

func TestInstallerNetworkPlanningCoversTailscaleTapAndMacvlanModes(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	tapInstaller := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "ts-only"},
			Network: NetworkOpts{
				Interfaces: "ts",
				Tailscale:  TailscaleOpts{AuthKey: "tskey-auth"},
			},
		},
	}
	tapEnv, tapRun, tapMode, err := tapInstaller.prepareNetworkConfig()
	if err != nil {
		t.Fatalf("prepareNetworkConfig tap mode returned error: %v", err)
	}
	if !tapMode || tapRun != "" {
		t.Fatalf("tap mode = %v runTSInNetNS = %q, want tap mode with host tailscale", tapMode, tapRun)
	}
	if tapEnv.TailscaleTAPInterface == "" || !strings.HasPrefix(tapEnv.TailscaleTAPInterface, "yts-") {
		t.Fatalf("tap env tailscale interface = %q, want generated yts-*", tapEnv.TailscaleTAPInterface)
	}

	server := newTestServer(t)
	combinedInstaller := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "ts-netns"},
			Network: NetworkOpts{
				Interfaces: "ts,svc,lan",
				Macvlan: MacvlanOpts{
					Parent: "eno1",
					VLAN:   7,
					Mac:    "02:00:00:00:00:07",
				},
			},
		},
	}
	env, runTSInNetNS, tapMode, err := combinedInstaller.prepareNetworkConfig()
	if err != nil {
		t.Fatalf("prepareNetworkConfig combined mode returned error: %v", err)
	}
	if tapMode {
		t.Fatal("combined network unexpectedly selected tailscale TAP mode")
	}
	if runTSInNetNS != env.NetNS() {
		t.Fatalf("runTSInNetNS = %q, want %q", runTSInNetNS, env.NetNS())
	}
	if got := env.ServiceIP.Addr().String(); got != "192.168.100.3" {
		t.Fatalf("service IP = %q, want 192.168.100.3", got)
	}
	if env.MacvlanParent != "eno1" || env.MacvlanVLAN != "7" || env.MacvlanMac != "02:00:00:00:00:07" {
		t.Fatalf("macvlan env = parent %q vlan %q mac %q", env.MacvlanParent, env.MacvlanVLAN, env.MacvlanMac)
	}
}

func TestISOTailscaleUsesPersistedRouterNamespaceWithoutChangingOrdinaryModes(t *testing.T) {
	allocation := &db.ISOAllocation{
		Kind:         string(iso.PayloadCompose),
		NetNS:        "yeet-a172cedcae-ns",
		DesiredModes: []string{"iso", "ts"},
	}
	env := netns.Service{ServiceName: "app"}
	installer := &FileInstaller{
		cfg:           FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "app"}},
		tsNet:         &db.TailscaleNetwork{Interface: "yts-random"},
		isoAllocation: allocation,
	}
	runIn, resolvConf, tapMode, err := installer.tailscaleNetNSMode(&env)
	if err != nil {
		t.Fatal(err)
	}
	if runIn != allocation.NetNS || resolvConf != "" || tapMode {
		t.Fatalf("ISO tailscale mode = run %q resolv %q tap %v, want persisted router namespace", runIn, resolvConf, tapMode)
	}
	if env.TailscaleTAPInterface != "" {
		t.Fatalf("ISO router unexpectedly configured TAP interface %q", env.TailscaleTAPInterface)
	}
	if installer.tsNet.Interface != "ts0" {
		t.Fatalf("ISO tailscaled interface = %q, want Task 7 policy identity ts0", installer.tsNet.Interface)
	}
	unit := newTailscaleSystemdUnit(tailscaleInstallPlan{
		service: "app", runDir: "/srv/app/run", serviceTSDir: "/srv/app/tailscale",
		runInNetNS: runIn, interfaceName: installer.tsNet.Interface,
	})
	if got := strings.Join(unit.Arguments, " "); !strings.Contains(got, "--tun=ts0") || strings.Contains(got, "--tun=yts-") {
		t.Fatalf("ISO tailscaled unit args = %q, want persisted ts0 TUN", got)
	}

	plainISO := &FileInstaller{isoAllocation: &db.ISOAllocation{Kind: string(iso.PayloadCompose), NetNS: "yeet-plain-ns", DesiredModes: []string{"iso"}}}
	if runIn, resolvConf, tapMode, err := plainISO.tailscaleNetNSMode(&netns.Service{}); err != nil || runIn != "" || resolvConf != "" || tapMode {
		t.Fatalf("ordinary ISO without tailscale changed mode: %q %q %v", runIn, resolvConf, tapMode)
	}
}

func TestISOTailscaleRejectsExitNodeBeforeSelectingNamespaceOrTUN(t *testing.T) {
	for _, exitNode := range []string{"exit.example", " \texit.example\n"} {
		t.Run(fmt.Sprintf("exit_node_%q", exitNode), func(t *testing.T) {
			env := netns.Service{}
			installer := &FileInstaller{
				cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "app"}},
				tsNet: &db.TailscaleNetwork{
					Interface: "yts-random",
					ExitNode:  exitNode,
				},
				isoAllocation: &db.ISOAllocation{
					Kind:         string(iso.PayloadCompose),
					NetNS:        "yeet-a172cedcae-ns",
					DesiredModes: []string{"iso", "ts"},
				},
			}

			runIn, resolvConf, tapMode, err := installer.tailscaleNetNSMode(&env)
			if err == nil || !strings.Contains(err.Error(), "exit node") {
				t.Fatalf("exit node %q error = %v, want fail-closed rejection", exitNode, err)
			}
			if runIn != "" || resolvConf != "" || tapMode {
				t.Fatalf("exit node %q selected run=%q resolv=%q tap=%v before rejection", exitNode, runIn, resolvConf, tapMode)
			}
			if installer.tsNet.Interface != "yts-random" || env.TailscaleTAPInterface != "" {
				t.Fatalf("exit node %q mutated TUN state: network=%q env=%q", exitNode, installer.tsNet.Interface, env.TailscaleTAPInterface)
			}
		})
	}
}

func TestOrdinaryTailscaleStillAllowsExitNode(t *testing.T) {
	env := netns.Service{}
	installer := &FileInstaller{
		tsNet: &db.TailscaleNetwork{
			Interface: "yts-random",
			ExitNode:  "exit.example",
		},
	}

	runIn, resolvConf, tapMode, err := installer.tailscaleNetNSMode(&env)
	if err != nil {
		t.Fatalf("ordinary Tailscale exit node rejected: %v", err)
	}
	if runIn != "" || resolvConf != tailscaledResolvConf || !tapMode {
		t.Fatalf("ordinary Tailscale mode = run %q resolv %q tap %v, want unchanged TAP mode", runIn, resolvConf, tapMode)
	}
	if installer.tsNet.ExitNode != "exit.example" || installer.tsNet.Interface != "yts-random" || env.TailscaleTAPInterface != "yts-random" {
		t.Fatalf("ordinary Tailscale state changed: network=%#v env TUN=%q", installer.tsNet, env.TailscaleTAPInterface)
	}
}

func TestISOTailscaleRejectsCorruptPersistedRouterNamespace(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		kind      iso.PayloadKind
		modes     []string
	}{
		{name: "empty namespace", kind: iso.PayloadCompose, modes: []string{"iso", "ts"}},
		{name: "unsafe namespace", namespace: "../host", kind: iso.PayloadCompose, modes: []string{"iso", "ts"}},
		{name: "malformed namespace", namespace: "yeet-wrong-ns", kind: iso.PayloadCompose, modes: []string{"iso", "ts"}},
		{name: "sibling namespace", namespace: "yeet-deadbeef00-ns", kind: iso.PayloadCompose, modes: []string{"iso", "ts"}},
		{name: "VM allocation", namespace: "yeet-a172cedcae-ns", kind: iso.PayloadVM, modes: []string{"iso", "ts"}},
		{name: "plain ISO", namespace: "yeet-a172cedcae-ns", kind: iso.PayloadCompose, modes: []string{"iso"}},
		{name: "missing ISO", namespace: "yeet-a172cedcae-ns", kind: iso.PayloadCompose, modes: []string{"ts"}},
		{name: "unnormalized modes", namespace: "yeet-a172cedcae-ns", kind: iso.PayloadCompose, modes: []string{"ts", "iso"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installer := &FileInstaller{
				cfg:   FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "app"}},
				tsNet: &db.TailscaleNetwork{Interface: "yts-random"},
				isoAllocation: &db.ISOAllocation{
					Kind: string(tt.kind), NetNS: tt.namespace, DesiredModes: tt.modes,
				},
			}
			runIn, _, tapMode, err := installer.tailscaleNetNSMode(&netns.Service{})
			if err == nil || runIn != "" || tapMode {
				t.Fatalf("allocation %#v selected run=%q tap=%v err=%v, want fail-closed error", installer.isoAllocation, runIn, tapMode, err)
			}
			if installer.tsNet.Interface != "yts-random" {
				t.Fatalf("allocation %#v mutated TUN before validation: %q", installer.isoAllocation, installer.tsNet.Interface)
			}
		})
	}
}

func TestISOTailscaleVMCombinationRemainsRejected(t *testing.T) {
	_, err := parseNetworkForPayload(NetworkOpts{Interfaces: "iso,ts"}, iso.PayloadVM, false)
	if err == nil || !strings.Contains(err.Error(), "VMs support only iso") {
		t.Fatalf("parseNetworkForPayload VM iso,ts error = %v", err)
	}
}

func TestInstallerCloseStagesComposeLANTailscaleUnitBindsNetNSResolver(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	server := newTestServer(t)
	const (
		service = "lan-ts"
		version = "1.92.3"
	)
	if err := server.ensureDirs(service, ""); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+version), []byte("daemon"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	installer := &FileInstaller{
		s:           server,
		serviceRoot: server.defaultServiceRootDir(service),
		resolvedIdentity: resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "app", RequestedGroup: "app", UID: 1002, GID: 1003,
		}},
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: service},
			Network: NetworkOpts{
				Interfaces: "lan,ts",
				Macvlan:    MacvlanOpts{Parent: "vmbr0"},
				Tailscale: TailscaleOpts{
					AuthKey: "tskey-auth-test",
					Version: version,
				},
			},
		},
	}

	if _, err := installer.configureNetwork(); err != nil {
		t.Fatalf("configureNetwork: %v", err)
	}
	tsUnitPath := installer.artifacts[db.ArtifactTSService]
	if tsUnitPath == "" {
		t.Fatalf("tailscale service artifact missing: %#v", installer.artifacts)
	}
	raw, err := os.ReadFile(tsUnitPath)
	if err != nil {
		t.Fatalf("read tailscale unit: %v", err)
	}
	unit := string(raw)
	for _, want := range []string{
		"NetworkNamespacePath=/var/run/netns/yeet-lan-ts-ns",
		"BindPaths=/etc/netns/yeet-lan-ts-ns/resolv.conf:/etc/resolv.conf",
		"PrivateMounts=yes",
		"ExecStart=" + filepath.Join(server.serviceBinDir(service), "tailscaled"),
		"--config=" + filepath.Join(server.serviceEnvDir(service), "tailscaled.json"),
		"EnvironmentFile=" + filepath.Join(server.serviceEnvDir(service), "tailscaled.env"),
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("tailscale unit missing %q:\n%s", want, unit)
		}
	}
	for _, forbidden := range []string{"User=app", "Group=app"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("privileged tailscale unit contains %q:\n%s", forbidden, unit)
		}
	}
}

func TestInstallerNewFileInstallerRejectsReservedNameAndNilWrites(t *testing.T) {
	if _, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: string(db.ArtifactTSBinary)},
	}); err == nil {
		t.Fatal("NewFileInstaller returned nil error for reserved service name")
	}

	installer := &FileInstaller{}
	if _, err := installer.Write([]byte("payload")); err == nil || !strings.Contains(err.Error(), "no temporary file") {
		t.Fatalf("Write error = %v, want no temporary file", err)
	}
	if _, err := installer.WriteAt([]byte("payload"), 0); err == nil || !strings.Contains(err.Error(), "no temporary file") {
		t.Fatalf("WriteAt error = %v, want no temporary file", err)
	}
	if err := installer.Close(); err == nil || !strings.Contains(err.Error(), "no temporary file") {
		t.Fatalf("Close error = %v, want no temporary file", err)
	}
}

func TestInstallerInstallPreparedFileCleanupAndPostActionError(t *testing.T) {
	installer := &FileInstaller{}
	dir := t.TempDir()
	cleanupPath := filepath.Join(dir, "cleanup.tmp")
	if err := os.WriteFile(cleanupPath, []byte("discard"), 0644); err != nil {
		t.Fatalf("WriteFile cleanup payload returned error: %v", err)
	}

	if err := installer.installPreparedFile(cleanupPath, fileInstallPlan{}); err != nil {
		t.Fatalf("installPreparedFile cleanup returned error: %v", err)
	}
	if _, err := os.Stat(cleanupPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup temp file still exists: %v", err)
	}

	src := filepath.Join(dir, "payload")
	dst := filepath.Join(dir, "payload.dst")
	if err := os.WriteFile(src, []byte("payload"), 0644); err != nil {
		t.Fatalf("WriteFile source returned error: %v", err)
	}
	err := installer.installPreparedFile(src, fileInstallPlan{
		dst: dst,
		postRenameActions: []func() error{
			func() error { return fmt.Errorf("post action failed") },
		},
	})
	if err == nil || !strings.Contains(err.Error(), "failed to run post-action") {
		t.Fatalf("installPreparedFile error = %v, want post-action failure", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("renamed payload missing after post-action failure: %v", err)
	}
}

func TestComposeRenderingTypeScriptIncludesArgsPublishAndVolumes(t *testing.T) {
	got, err := typescriptComposeFile("ts-svc", "/run/ts-svc", "/data/ts-svc", []string{"--serve"}, []string{" 3000:3000 ", ""})
	if err != nil {
		t.Fatalf("typescriptComposeFile returned error: %v", err)
	}
	for _, want := range []string{
		"denoland/deno:2.0.0-rc.2",
		"- deno",
		"- run",
		"- --allow-net",
		"- /main.ts",
		"- --serve",
		"- 3000:3000",
		"/data/ts-svc:/data",
		"/run/ts-svc/main.ts:/main.ts:ro",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("typescript compose missing %q:\n%s", want, got)
		}
	}
}

func TestComposeGeneratedFileReportsRenderError(t *testing.T) {
	installer := &FileInstaller{
		s:   newTestServer(t),
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "render-svc"}},
	}
	_, err := installer.writeGeneratedComposeFile(t.TempDir(), "custom", func(string, string, string, []string, []string) (string, error) {
		return "", fmt.Errorf("render boom")
	})
	if err == nil || !strings.Contains(err.Error(), "failed to render custom compose file") {
		t.Fatalf("writeGeneratedComposeFile error = %v, want render failure", err)
	}
}

func TestValidatePullPayloadType(t *testing.T) {
	for _, ft := range []ftdetect.FileType{ftdetect.DockerCompose, ftdetect.Python, ftdetect.TypeScript} {
		if err := validatePullPayloadType(true, ft); err != nil {
			t.Fatalf("validatePullPayloadType(true, %v) returned error: %v", ft, err)
		}
	}
	if err := validatePullPayloadType(true, ftdetect.Binary); err == nil {
		t.Fatal("validatePullPayloadType(true, Binary) returned nil, want error")
	}
	if err := validatePullPayloadType(false, ftdetect.Binary); err != nil {
		t.Fatalf("validatePullPayloadType(false, Binary) returned error: %v", err)
	}
}

func TestApplyInstallServiceType(t *testing.T) {
	tests := []struct {
		name    string
		current db.ServiceType
		plan    fileInstallPlan
		want    db.ServiceType
		wantErr bool
	}{
		{
			name: "sets empty service type",
			plan: fileInstallPlan{
				detectedServiceType: db.ServiceTypeSystemd,
			},
			want: db.ServiceTypeSystemd,
		},
		{
			name:    "keeps matching service type",
			current: db.ServiceTypeDockerCompose,
			plan: fileInstallPlan{
				detectedServiceType: db.ServiceTypeDockerCompose,
			},
			want: db.ServiceTypeDockerCompose,
		},
		{
			name:    "ignores empty detected service type",
			current: db.ServiceTypeSystemd,
			want:    db.ServiceTypeSystemd,
		},
		{
			name:    "allows systemd to generated compose upgrade",
			current: db.ServiceTypeSystemd,
			plan: fileInstallPlan{
				detectedServiceType:     db.ServiceTypeDockerCompose,
				allowServiceTypeUpgrade: true,
			},
			want: db.ServiceTypeDockerCompose,
		},
		{
			name:    "rejects mismatched service type",
			current: db.ServiceTypeDockerCompose,
			plan: fileInstallPlan{
				detectedServiceType: db.ServiceTypeSystemd,
			},
			want:    db.ServiceTypeDockerCompose,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &db.Service{ServiceType: tt.current}
			err := applyInstallServiceType(service, tt.plan)
			if tt.wantErr {
				if err == nil {
					t.Fatal("applyInstallServiceType returned nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("applyInstallServiceType returned error: %v", err)
			}
			if service.ServiceType != tt.want {
				t.Fatalf("service type = %q, want %q", service.ServiceType, tt.want)
			}
		})
	}
}

func TestEnsureSystemdUnitRegeneratesCatchUnitWithDockerOrdering(t *testing.T) {
	server := newTestServer(t)
	oldUnit := filepath.Join(server.serviceBinDir(CatchService), "catch-old.service")
	if err := os.MkdirAll(filepath.Dir(oldUnit), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(oldUnit, []byte("[Unit]\n\n[Service]\nExecStart=/old/catch\n\n[Install]\nWantedBy=multi-user.target\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:        CatchService,
		ServiceType: db.ServiceTypeSystemd,
		Generation:  1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": oldUnit}},
		},
	})

	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: CatchService},
		Args:         []string{"--data-dir=/root/data", "--tsnet-host=catch"},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(installer.tempFilePath())
	})

	if err := installer.ensureSystemdUnit(); err != nil {
		t.Fatalf("ensureSystemdUnit returned error: %v", err)
	}
	gotPath := installer.artifacts[db.ArtifactSystemdUnit]
	if gotPath == "" {
		t.Fatal("catch systemd unit was not staged")
	}
	if gotPath == oldUnit {
		t.Fatalf("catch systemd unit reused old staged path %q", gotPath)
	}
	raw, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Wants=containerd.service\n",
		"After=containerd.service\n",
		"Before=yeet-docker-prereqs.target docker.service\n",
		"ExecStartPost=/bin/sh -c 'i=0; while [ \"$i\" -lt 600 ]; do [ -S /run/docker/plugins/yeet.sock ] && exit 0; i=$((i+1)); sleep 0.1; done; exit 1'\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("regenerated catch unit missing %q:\n%s", want, got)
		}
	}
}

func TestHostDefaultRouteInterfaceFromProcRouteReportsErrors(t *testing.T) {
	routeTable := strings.Join([]string{
		"Iface\tDestination\tGateway",
		"malformed",
		"eth0\t000011AC\t00000000",
	}, "\n")
	if _, err := hostDefaultRouteInterfaceFromProcRoute(strings.NewReader(routeTable)); err == nil || !strings.Contains(err.Error(), "default route interface not found") {
		t.Fatalf("hostDefaultRouteInterfaceFromProcRoute error = %v, want default route not found", err)
	}

	if _, err := hostDefaultRouteInterfaceFromProcRoute(errorReader{}); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("hostDefaultRouteInterfaceFromProcRoute error = %v, want reader error", err)
	}
}

func TestParseNetworkReportsUnsupportedAndAllocationErrors(t *testing.T) {
	t.Run("unknown interface", func(t *testing.T) {
		installer := &FileInstaller{
			s: newTestServer(t),
			cfg: FileInstallerCfg{
				InstallerCfg: InstallerCfg{ServiceName: "bad-net"},
				Network:      NetworkOpts{Interfaces: "bad"},
			},
		}
		if err := installer.parseNetwork(); err == nil || !strings.Contains(err.Error(), `unknown network: "bad"`) {
			t.Fatalf("parseNetwork error = %v, want unknown network", err)
		}
	})

	t.Run("default route error", func(t *testing.T) {
		oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
		hostDefaultRouteInterfaceFn = func() (string, error) {
			return "", fmt.Errorf("route lookup failed")
		}
		t.Cleanup(func() {
			hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
		})

		installer := &FileInstaller{
			s: newTestServer(t),
			cfg: FileInstallerCfg{
				InstallerCfg: InstallerCfg{ServiceName: "lan-net"},
				Network:      NetworkOpts{Interfaces: "lan"},
			},
		}
		if err := installer.parseNetwork(); err == nil || !strings.Contains(err.Error(), "failed to get default route interface") {
			t.Fatalf("parseNetwork error = %v, want default route failure", err)
		}
	})

}

func TestConfigureNetworkAndStageInstallSurfaceErrors(t *testing.T) {
	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "missing-dirs"},
			Network:      NetworkOpts{Interfaces: "svc"},
		},
	}
	if _, err := installer.configureNetwork(); err == nil || !strings.Contains(err.Error(), "failed to write resolv.conf") {
		t.Fatalf("configureNetwork error = %v, want resolv.conf write failure", err)
	}

	installer = &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "bad-stage-net"},
			Network:      NetworkOpts{Interfaces: "bad"},
		},
	}
	if err := installer.configureAndStageInstall(fileInstallPlan{}); err == nil || !strings.Contains(err.Error(), "failed to configure network") {
		t.Fatalf("configureAndStageInstall error = %v, want wrapped network failure", err)
	}
}

func TestConfigureAndStageInstallTransitionsISOToSvcTailscaleAndHost(t *testing.T) {
	for _, tt := range []struct {
		name       string
		interfaces string
		modes      []string
		configure  func(*FileInstaller)
		assert     func(*testing.T, db.ServiceView)
	}{
		{
			name:       "svc",
			interfaces: "svc",
			modes:      []string{"svc"},
			configure: func(installer *FileInstaller) {
				installer.svcNet = &db.SvcNetwork{IPv4: netip.MustParseAddr("172.17.0.27")}
				installer.artifacts[db.ArtifactNetNSService] = "/new/yeet-app-ns.service"
			},
			assert: func(t *testing.T, service db.ServiceView) {
				t.Helper()
				if !service.SvcNetwork().Valid() || service.TSNet().Valid() || service.Macvlan().Valid() {
					t.Fatalf("svc replacement network = %#v", service.AsStruct())
				}
			},
		},
		{
			name:       "tailscale",
			interfaces: "ts",
			modes:      []string{"ts"},
			configure: func(installer *FileInstaller) {
				installer.tsNet = &db.TailscaleNetwork{Interface: "yts-test", Version: "1.88.2"}
				installer.artifacts[db.ArtifactTSService] = "/new/yeet-app-ts.service"
			},
			assert: func(t *testing.T, service db.ServiceView) {
				t.Helper()
				if !service.TSNet().Valid() || service.SvcNetwork().Valid() || service.Macvlan().Valid() {
					t.Fatalf("Tailscale replacement network = %#v", service.AsStruct())
				}
			},
		},
		{
			name:       "explicit host",
			interfaces: "host",
			modes:      nil,
			configure:  func(*FileInstaller) {},
			assert: func(t *testing.T, service db.ServiceView) {
				t.Helper()
				if service.SvcNetwork().Valid() || service.TSNet().Valid() || service.Macvlan().Valid() {
					t.Fatalf("host replacement retained managed network = %#v", service.AsStruct())
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, installer := newFileInstallerISOTransitionTest(t, tt.interfaces, tt.modes)
			tt.configure(installer)
			adapterStarts := 0
			installer.transitionFromISO = func(ctx context.Context, service string, desired []string, steps isoTransitionSteps) error {
				wrapped := &fileInstallerTransitionTestSteps{isoTransitionSteps: steps, starts: &adapterStarts}
				return server.transitionFromISOWith(ctx, service, desired, wrapped)
			}

			plan := fileInstallPlan{detectedServiceType: db.ServiceTypeDockerCompose}
			if err := installer.configureAndStageInstall(plan); err != nil {
				t.Fatal(err)
			}
			if !installer.transitionHandled || adapterStarts != 1 {
				t.Fatalf("transition handled = %v, starts = %d, want true and one adapter start", installer.transitionHandled, adapterStarts)
			}
			if err := installer.installIfRequested(); err != nil {
				t.Fatalf("StageOnly installIfRequested: %v", err)
			}
			dv, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			service := dv.Services().Get("app")
			if service.ISO().Valid() {
				t.Fatalf("successful transition retained ISO: %#v", service.ISO().AsStruct())
			}
			tt.assert(t, service)
			artifact := service.AsStruct().Artifacts[db.ArtifactDockerComposeFile]
			if got := artifact.Refs[db.Gen(7)]; got != "/old/compose-generation-7.yml" {
				t.Fatalf("generation 7 payload = %q, want preserved", got)
			}
			if got := artifact.Refs["staged"]; got != "/new/compose-staged.yml" {
				t.Fatalf("staged payload = %q, want new payload", got)
			}
			if service.Generation() != 7 {
				t.Fatalf("generation = %d, want unchanged staged generation 7", service.Generation())
			}
		})
	}
}

func TestConfigureAndStageInstallRetainsISOWhenConcreteTransitionCleanupFails(t *testing.T) {
	server, installer := newFileInstallerISOTransitionTest(t, "svc", []string{"svc"})
	installer.svcNet = &db.SvcNetwork{IPv4: netip.MustParseAddr("172.17.0.27")}
	adapterStarts := 0
	installer.transitionFromISO = func(ctx context.Context, service string, desired []string, steps isoTransitionSteps) error {
		wrapped := &fileInstallerTransitionTestSteps{
			isoTransitionSteps: steps,
			cleanErr:           fmt.Errorf("endpoint absence is uncertain"),
			starts:             &adapterStarts,
		}
		return server.transitionFromISOWith(ctx, service, desired, wrapped)
	}

	err := installer.configureAndStageInstall(fileInstallPlan{detectedServiceType: db.ServiceTypeDockerCompose})
	if err == nil || !strings.Contains(err.Error(), "endpoint absence is uncertain") {
		t.Fatalf("configureAndStageInstall error = %v, want cleanup failure", err)
	}
	if installer.transitionHandled || adapterStarts != 0 {
		t.Fatalf("transition handled = %v, starts = %d, want false and no replacement start", installer.transitionHandled, adapterStarts)
	}
	dv, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	service := dv.Services().Get("app")
	if !service.ISO().Valid() || service.ISO().State() != string(iso.StateTombstoned) || service.SvcNetwork().Valid() {
		t.Fatalf("failed concrete transition state = %#v, want ISO tombstone and no replacement", service.AsStruct())
	}
	if _, staged := service.AsStruct().Artifacts[db.ArtifactDockerComposeFile].Refs["staged"]; staged {
		t.Fatalf("replacement payload staged after cleanup failure: %#v", service.AsStruct().Artifacts)
	}
}

type fileInstallerTransitionTestSteps struct {
	isoTransitionSteps
	cleanErr error
	starts   *int
}

func (s *fileInstallerTransitionTestSteps) StopISO(context.Context, string) error {
	return nil
}

func (s *fileInstallerTransitionTestSteps) CleanISO(context.Context, string) error {
	return s.cleanErr
}

func (s *fileInstallerTransitionTestSteps) VerifyISOAbsent(context.Context, string) error {
	return nil
}

func (s *fileInstallerTransitionTestSteps) StartReplacement(ctx context.Context, service string, prepared isoReplacementNetwork) error {
	(*s.starts)++
	return s.isoTransitionSteps.StartReplacement(ctx, service, prepared)
}

func newFileInstallerISOTransitionTest(t *testing.T, interfaces string, modes []string) (*Server, *FileInstaller) {
	t.Helper()
	withISORuntimeBackend(t, netns.BackendNFT)
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldVerifyPolicy := verifyISOPolicyForRuntime
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	verifyISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		verifyISOPolicyForRuntime = oldVerifyPolicy
	})
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	if err := server.ensureDirs("app", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeDockerCompose
		service.Generation = 7
		service.Artifacts = db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{db.Gen(7): "/old/compose-generation-7.yml"},
			},
			db.ArtifactDockerComposeNetwork: {
				Refs: map[db.ArtifactRef]string{db.Gen(7): "/old/iso-network-generation-7.yml"},
			},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, err := server.serviceView("app")
	if err != nil {
		t.Fatal(err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "app"},
			Network:      NetworkOpts{Interfaces: interfaces, Modes: slices.Clone(modes)},
			StageOnly:    true,
		},
		existingService: view,
		serviceRoot:     server.defaultServiceRootDir("app"),
		artifacts: map[db.ArtifactName]string{
			db.ArtifactDockerComposeFile: "/new/compose-staged.yml",
		},
	}
	installer.lazyNetwork.MustSet(&networkConfig{})
	return server, installer
}

func TestNewSystemdUnitAppliesNetworkNamespaceFields(t *testing.T) {
	server := newTestServer(t)
	if err := server.ensureDirs("net-systemd", ""); err != nil {
		t.Fatalf("ensureDirs returned error: %v", err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "net-systemd"},
			Network:      NetworkOpts{Interfaces: "svc"},
		},
	}

	unit, err := installer.newSystemdUnit(filepath.Join(server.serviceRunDir("net-systemd"), "net-systemd"))
	if err != nil {
		t.Fatalf("newSystemdUnit returned error: %v", err)
	}
	if unit.NetNS != "yeet-net-systemd-ns" {
		t.Fatalf("unit NetNS = %q, want yeet-net-systemd-ns", unit.NetNS)
	}
	if unit.Requires != "yeet-net-systemd-ns.service" {
		t.Fatalf("unit Requires = %q, want netns service dependency", unit.Requires)
	}
	if unit.ResolvConf != "/etc/netns/yeet-net-systemd-ns/resolv.conf" {
		t.Fatalf("unit ResolvConf = %q, want netns resolv.conf", unit.ResolvConf)
	}
}

func TestPrepareNoBinaryInstallVariants(t *testing.T) {
	emptyInstaller := &FileInstaller{}
	plan, err := emptyInstaller.prepareNoBinaryInstall()
	if err != nil {
		t.Fatalf("prepareNoBinaryInstall with no existing service returned error: %v", err)
	}
	if plan.dst != "" || plan.detectedServiceType != "" || plan.allowServiceTypeUpgrade || len(plan.postRenameActions) != 0 {
		t.Fatalf("plan = %#v, want empty plan", plan)
	}

	server := newTestServer(t)
	addTestServices(t, server,
		db.Service{Name: "compose-existing", ServiceType: db.ServiceTypeDockerCompose},
		db.Service{Name: "systemd-existing", ServiceType: db.ServiceTypeSystemd},
	)

	composeInstaller := &FileInstaller{
		s:               server,
		cfg:             FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "compose-existing"}},
		existingService: First(server.serviceView("compose-existing")),
	}
	plan, err = composeInstaller.prepareNoBinaryInstall()
	if err != nil {
		t.Fatalf("prepareNoBinaryInstall with compose service returned error: %v", err)
	}
	if plan.detectedServiceType != db.ServiceTypeDockerCompose || plan.dst != "" {
		t.Fatalf("compose plan = %#v, want compose type with no file action", plan)
	}

	systemdInstaller := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "systemd-existing"},
			StageOnly:    true,
		},
		existingService: First(server.serviceView("systemd-existing")),
	}
	plan, err = systemdInstaller.prepareNoBinaryInstall()
	if err != nil {
		t.Fatalf("prepareNoBinaryInstall with systemd service returned error: %v", err)
	}
	if plan.detectedServiceType != db.ServiceTypeSystemd || plan.dst != "" {
		t.Fatalf("systemd plan = %#v, want systemd type with no file action", plan)
	}
}

func TestReuseExistingSystemdUnitBranches(t *testing.T) {
	server := newTestServer(t)
	unitPath := filepath.Join(t.TempDir(), "unit.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/old/reuse-unit --keep value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	addTestServices(t, server, db.Service{
		Name:        "reuse-unit",
		ServiceType: db.ServiceTypeSystemd,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": unitPath}},
		},
	})
	installer := &FileInstaller{
		s:               server,
		cfg:             FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "reuse-unit"}},
		existingService: First(server.serviceView("reuse-unit")),
	}
	reused, err := installer.reuseExistingSystemdUnit("/srv/reuse-unit")
	if err != nil {
		t.Fatalf("reuseExistingSystemdUnit returned error: %v", err)
	}
	if !reused {
		t.Fatal("reuseExistingSystemdUnit returned reused=false, want true")
	}
	rewritten := installer.artifacts[db.ArtifactSystemdUnit]
	if rewritten == "" || rewritten == unitPath {
		t.Fatalf("rewritten unit = %q, want a new staged unit", rewritten)
	}
	raw, err := os.ReadFile(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, "ExecStart=/srv/reuse-unit --keep value\n") {
		t.Fatalf("rewritten unit did not retarget immutable executable and preserve args:\n%s", got)
	}

	addTestServices(t, server, db.Service{
		Name:        "missing-unit",
		ServiceType: db.ServiceTypeSystemd,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": filepath.Join(t.TempDir(), "missing.service")}},
		},
	})
	installer = &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "missing-unit"},
			Args:         []string{"--new"},
		},
		existingService: First(server.serviceView("missing-unit")),
	}
	reused, err = installer.reuseExistingSystemdUnit("/srv/missing-unit")
	if err == nil || !strings.Contains(err.Error(), "failed to rewrite systemd unit") {
		t.Fatalf("reuseExistingSystemdUnit error = %v, want rewrite failure", err)
	}
	if reused {
		t.Fatal("reuseExistingSystemdUnit returned reused=true on rewrite failure")
	}
}

func TestPreparePayloadErrorBranches(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "run")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0644); err != nil {
		t.Fatalf("WriteFile script returned error: %v", err)
	}
	installer := &FileInstaller{
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "pull-script", Pull: true}},
	}
	if _, err := installer.preparePayloadInstall(script); err == nil || !strings.Contains(err.Error(), "--pull is only valid") {
		t.Fatalf("preparePayloadInstall error = %v, want pull validation failure", err)
	}

	if _, err := installer.preparePayloadByType("unused", ftdetect.Unknown); err == nil || !strings.Contains(err.Error(), "unknown file type") {
		t.Fatalf("preparePayloadByType error = %v, want unknown file type", err)
	}

	compose := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(compose, []byte("services: []\n"), 0644); err != nil {
		t.Fatalf("WriteFile compose returned error: %v", err)
	}
	installer = &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "compose-bad-publish"},
			Publish:      []string{"8080:80"},
		},
	}
	if _, err := installer.prepareDockerComposePayload(compose); err == nil || !strings.Contains(err.Error(), "failed to apply publish ports") {
		t.Fatalf("prepareDockerComposePayload error = %v, want publish ports failure", err)
	}
}

func TestFileActionErrorBranches(t *testing.T) {
	logs := captureLogs(t)
	closeAndLog(errorCloser{}, "closer")
	if !strings.Contains(logs.String(), "failed to close closer") {
		t.Fatalf("logs = %q, want close failure", logs.String())
	}

	dir := t.TempDir()
	nonEmptyDir := filepath.Join(dir, "non-empty")
	if err := os.Mkdir(nonEmptyDir, 0755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "child"), []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile child returned error: %v", err)
	}
	removeFileIfExists(nonEmptyDir)
	if !strings.Contains(logs.String(), "failed to remove") {
		t.Fatalf("logs = %q, want remove failure", logs.String())
	}

	installer := &FileInstaller{}
	if err := installer.installPreparedFile(filepath.Join(dir, "missing"), fileInstallPlan{dst: filepath.Join(dir, "dst")}); err == nil || !strings.Contains(err.Error(), "failed to move file in place") {
		t.Fatalf("installPreparedFile error = %v, want rename failure", err)
	}

	binDirFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(binDirFile, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile binDirFile returned error: %v", err)
	}
	installer = &FileInstaller{
		s:   newTestServer(t),
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "compose-write-error"}},
	}
	if _, err := installer.writeGeneratedComposeFile(binDirFile, "python", pythonComposeFile); err == nil || !strings.Contains(err.Error(), "failed to write python compose file") {
		t.Fatalf("writeGeneratedComposeFile error = %v, want write failure", err)
	}

	if err := chmodExecutableAction(filepath.Join(dir, "missing-executable"))(); err == nil || !strings.Contains(err.Error(), "failed to make binary executable") {
		t.Fatalf("chmodExecutableAction error = %v, want chmod failure", err)
	}
}

func TestStageInstallPlanMismatchAndNetworkApplication(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:        "mismatch",
		ServiceType: db.ServiceTypeDockerCompose,
	})
	installer := &FileInstaller{
		s:   server,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "mismatch"}},
	}
	if err := installer.stageInstallPlan(fileInstallPlan{detectedServiceType: db.ServiceTypeSystemd}); err == nil || !strings.Contains(err.Error(), "failed to update service") {
		t.Fatalf("stageInstallPlan error = %v, want service update failure", err)
	}

	service := &db.Service{}
	applyInstallNetworks(
		service,
		&db.MacvlanNetwork{Interface: "ymv-test", Parent: "eno1"},
		nil,
		&db.TailscaleNetwork{Interface: "yts-test", Version: "1.77.33"},
	)
	if service.Macvlan == nil || service.Macvlan.Interface != "ymv-test" {
		t.Fatalf("service macvlan = %#v, want applied macvlan", service.Macvlan)
	}
	if service.TSNet == nil || service.TSNet.Interface != "yts-test" {
		t.Fatalf("service tailscale = %#v, want applied tailscale", service.TSNet)
	}
}

func TestInstallerTailscaleInstallUsesResolvedServiceRoot(t *testing.T) {
	server := newTestServer(t)
	const (
		service = "svc-ts-root"
		version = "1.92.3"
		authKey = "tskey-auth-test"
	)
	customRoot := filepath.Join(t.TempDir(), "custom-root")
	if err := ensureDirsForRoot(customRoot, ""); err != nil {
		t.Fatalf("ensureDirsForRoot: %v", err)
	}
	if err := ensureDirsForRoot(server.defaultServiceRootDir(service), ""); err != nil {
		t.Fatalf("ensure default dirs: %v", err)
	}
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+version), []byte("daemon"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	installer := &FileInstaller{
		s:           server,
		serviceRoot: customRoot,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{
			ServiceName: service,
		}},
		tsNet: &db.TailscaleNetwork{
			Interface: "ts0",
			Version:   version,
		},
		tsAuthKey: authKey,
	}

	err := installer.installTailscaleForNetNS(netns.Service{ServiceName: service}, "yeet-svc-ts-root-ns", false)
	if err != nil {
		t.Fatalf("installTailscaleForNetNS: %v", err)
	}

	for name, path := range installer.artifacts {
		if name == db.ArtifactTSBinary {
			continue
		}
		if !strings.HasPrefix(path, customRoot+string(filepath.Separator)) {
			t.Fatalf("artifact %s path = %q, want under %q", name, path, customRoot)
		}
	}
	defaultTailRoot := filepath.Join(server.defaultServiceRootDir(service), "tailscale")
	if _, err := os.Stat(defaultTailRoot); !os.IsNotExist(err) {
		t.Fatalf("default tailscale root stat err = %v, want not exist", err)
	}
}

func TestTempFilePathInitAndCleanupBranches(t *testing.T) {
	server := newTestServer(t)
	installer := &FileInstaller{
		s:   server,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "fallback-temp"}},
	}
	path := installer.tempFilePath()
	if !strings.HasPrefix(path, server.serviceBinDir("fallback-temp")+string(filepath.Separator)) || !strings.HasSuffix(path, ".tmp") {
		t.Fatalf("tempFilePath = %q, want service bin temp path", path)
	}
	installer.cleanupTemp()

	existingPath := filepath.Join(t.TempDir(), "existing.tmp")
	installer = &FileInstaller{tmpPath: existingPath}
	if err := installer.initTempFile(); err != nil {
		t.Fatalf("initTempFile with existing tmpPath returned error: %v", err)
	}
	if installer.tmpPath != existingPath {
		t.Fatalf("tmpPath = %q, want preserved %q", installer.tmpPath, existingPath)
	}

	badServer := newTestServer(t)
	if err := os.MkdirAll(badServer.defaultServiceRootDir("bad-temp"), 0755); err != nil {
		t.Fatalf("MkdirAll service root returned error: %v", err)
	}
	if err := os.WriteFile(badServer.serviceBinDir("bad-temp"), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile service bin returned error: %v", err)
	}
	installer = &FileInstaller{
		s:   badServer,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "bad-temp"}},
	}
	if err := installer.initTempFile(); err == nil || !strings.Contains(err.Error(), "failed to create temp dir") {
		t.Fatalf("initTempFile error = %v, want temp dir failure", err)
	}
}

func TestNewFileInstallerRejectsNonDirectoryServiceRootComponent(t *testing.T) {
	server := newTestServer(t)
	servicesRoot := filepath.Join(t.TempDir(), "services")
	if err := os.WriteFile(servicesRoot, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("WriteFile services root returned error: %v", err)
	}
	server.cfg.ServicesRoot = servicesRoot

	_, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "dir-error"},
	})
	if err == nil || !strings.Contains(err.Error(), "must be a non-symlink directory") {
		t.Fatalf("NewFileInstaller error = %v, want secure root component failure", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read failed")
}

type errorCloser struct{}

func (errorCloser) Close() error {
	return fmt.Errorf("close failed")
}

func netipMustParseAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return addr
}

func testService(t *testing.T, server *Server, name string) *db.Service {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get returned error: %v", err)
	}
	service := dv.AsStruct().Services[name]
	if service == nil {
		t.Fatalf("service %q not found", name)
	}
	return service
}

func stagedArtifactPath(t *testing.T, service *db.Service, name db.ArtifactName) string {
	t.Helper()
	artifact := service.Artifacts[name]
	if artifact == nil {
		t.Fatalf("artifact %s not staged; artifacts = %#v", name, service.Artifacts)
	}
	path := artifact.Refs[db.ArtifactRef("staged")]
	if path == "" {
		t.Fatalf("artifact %s has no staged ref; refs = %#v", name, artifact.Refs)
	}
	return path
}

func assertInstallerFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", path, err)
	}
	if string(raw) != want {
		t.Fatalf("file %q content = %q, want %q", path, string(raw), want)
	}
}

func TestISOInstallOrdersAdmissionPolicyAndWorkload(t *testing.T) {
	recorder := newISOInstallRecorder(t)

	if err := recorder.installer.installISOCompose(context.Background(), recorder); err != nil {
		t.Fatalf("installISOCompose: %v", err)
	}

	want := []string{
		"resolve-base", "admit-base", "reserve", "render-overlay", "resolve-merged", "admit-merged",
		"install-dns", "ensure-policy", "verify-policy", "ensure-topology", "verify-topology",
		"install-tailscale", "pull", "build", "attach-network", "start-aux", "compose-up",
		"inspect-runtime", "mark-ready",
	}
	if !reflect.DeepEqual(recorder.events, want) {
		t.Fatalf("install order = %#v, want %#v", recorder.events, want)
	}
}

func TestPrepareISOComposeRunsCanonicalAdmissionBeforeReservationAndStagesExactOverlay(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.ISOPool = &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	serviceRoot := server.defaultServiceRootDir("app")
	for _, dir := range []string{serviceBinDirForRoot(serviceRoot), serviceDataDirForRoot(serviceRoot)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	composePath := filepath.Join(serviceBinDirForRoot(serviceRoot), "compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "app"},
			Network:      NetworkOpts{Interfaces: "iso", Modes: []string{"iso"}, ISO: true},
		},
		artifacts:   map[db.ArtifactName]string{db.ArtifactDockerComposeFile: composePath},
		serviceRoot: serviceRoot,
	}
	var calls [][]string
	resolve := func(_ context.Context, opts svc.ComposeResolveOptions) ([]byte, error) {
		calls = append(calls, slices.Clone(opts.Files))
		if len(opts.Files) == 1 {
			if dv, err := server.cfg.DB.Get(); err != nil {
				return nil, err
			} else if service, ok := dv.Services().GetOk("app"); ok && service.ISO().Valid() {
				return nil, errors.New("allocation existed before base admission")
			}
			return []byte(`{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`), nil
		}
		allocation, err := installer.persistedISOAllocation()
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf(`{
			"name":"catch-app",
			"networks":{"default":{"name":"catch-app_default","driver":"yeet","driver_opts":{"dev.catchit.mode":"iso","dev.catchit.netns":"/var/run/netns/%s"},"enable_ipv6":false,"ipam":{"config":[{"subnet":"%s","gateway":"%s"}]}}},
			"services":{"api":{"image":"nginx","dns":["%s"],"networks":{"default":{"ipv4_address":"%s"}}}}
		}`, allocation.NetNS, allocation.Project, allocation.Gateway, allocation.Gateway, allocation.Components["api"].Address)), nil
	}

	model, err := installer.prepareISOCompose(context.Background(), resolve)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(model.Components, []string{"api"}) || len(calls) != 2 || len(calls[0]) != 1 || len(calls[1]) != 2 {
		t.Fatalf("model/calls = %#v/%#v, want admitted api and base then merged resolution", model, calls)
	}
	allocation, err := installer.persistedISOAllocation()
	if err != nil {
		t.Fatal(err)
	}
	if installer.isoAllocation == nil || !reflect.DeepEqual(installer.isoAllocation, allocation) {
		t.Fatalf("installer allocation = %#v, want persisted same-service %#v", installer.isoAllocation, allocation)
	}
	overlayPath := installer.artifacts[db.ArtifactDockerComposeNetwork]
	if overlayPath == "" {
		t.Fatal("ISO Compose overlay was not staged")
	}
	if raw, err := os.ReadFile(overlayPath); err != nil || !strings.Contains(string(raw), allocation.Components["api"].Address.String()) {
		t.Fatalf("overlay = %q, %v; want persisted component address", raw, err)
	}
}

func TestPrepareISOComposeRejectsMergedComponentSetDriftAndQuarantines(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.ISOPool = &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	serviceRoot := server.defaultServiceRootDir("app")
	for _, dir := range []string{serviceBinDirForRoot(serviceRoot), serviceDataDirForRoot(serviceRoot)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	composePath := filepath.Join(serviceBinDirForRoot(serviceRoot), "compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "app"},
			Network:      NetworkOpts{Interfaces: "iso", Modes: []string{"iso"}, ISO: true},
		},
		artifacts:   map[db.ArtifactName]string{db.ArtifactDockerComposeFile: composePath},
		serviceRoot: serviceRoot,
	}
	resolveCalls := 0
	resolve := func(_ context.Context, opts svc.ComposeResolveOptions) ([]byte, error) {
		resolveCalls++
		if len(opts.Files) == 1 {
			return []byte(`{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}},"worker":{"image":"nginx","networks":{"default":null}}}}`), nil
		}
		allocation, err := installer.persistedISOAllocation()
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf(`{
			"name":"catch-app",
			"networks":{"default":{"name":"catch-app_default","driver":"yeet","driver_opts":{"dev.catchit.mode":"iso","dev.catchit.netns":"/var/run/netns/%s"},"enable_ipv6":false,"ipam":{"config":[{"subnet":"%s","gateway":"%s"}]}}},
			"services":{"api":{"image":"nginx","dns":["%s"],"networks":{"default":{"ipv4_address":"%s"}}}}
		}`, allocation.NetNS, allocation.Project, allocation.Gateway, allocation.Gateway, allocation.Components["api"].Address)), nil
	}

	_, err := installer.prepareISOCompose(context.Background(), resolve)
	if err == nil || !strings.Contains(err.Error(), "overlay changed Compose components") {
		t.Fatalf("prepareISOCompose error = %v, want exact component-set rejection", err)
	}
	if resolveCalls != 2 {
		t.Fatalf("resolve calls = %d, want base and merged only", resolveCalls)
	}
	allocation, loadErr := installer.persistedISOAllocation()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if allocation.State != string(iso.StateQuarantined) || !strings.Contains(allocation.LastError, "overlay changed Compose components") {
		t.Fatalf("allocation = %#v, want quarantined component drift", allocation)
	}
}

func TestISOInstallStopsBeforeWorkloadSideEffectsAtSecurityFailures(t *testing.T) {
	for _, phase := range []string{
		"resolve-base", "admit-base", "reserve", "render-overlay", "resolve-merged", "admit-merged",
		"install-dns", "ensure-policy", "verify-policy", "ensure-topology", "verify-topology",
		"install-tailscale", "pull", "build",
	} {
		t.Run(phase, func(t *testing.T) {
			recorder := newISOInstallRecorder(t)
			recorder.failAt = phase

			err := recorder.installer.installISOCompose(context.Background(), recorder)
			if err == nil || !strings.Contains(err.Error(), phase) {
				t.Fatalf("installISOCompose error = %v, want %q failure", err, phase)
			}
			for _, forbidden := range []string{"attach-network", "start-aux", "compose-up"} {
				if slices.Contains(recorder.events, forbidden) {
					t.Fatalf("%s ran after %s failure: %#v", forbidden, phase, recorder.events)
				}
			}
			wantTail := []string{phase, "compose-down-remove-orphans", "quarantine"}
			if len(recorder.events) < len(wantTail) {
				t.Fatalf("security failure events after %s = %#v, want tail %#v", phase, recorder.events, wantTail)
			}
			if got := recorder.events[len(recorder.events)-len(wantTail):]; !slices.Equal(got, wantTail) {
				t.Fatalf("security failure tail after %s = %#v, want %#v", phase, got, wantTail)
			}
			allocation := testService(t, recorder.server, "app").ISO
			if allocation == nil || allocation.State != string(iso.StateQuarantined) || !strings.Contains(allocation.LastError, phase) {
				t.Fatalf("persisted allocation after %s failure = %#v", phase, allocation)
			}
		})
	}
}

func TestISOInstallLoadsPersistedSameServiceAllocationBeforeTailscale(t *testing.T) {
	recorder := newISOInstallRecorder(t)
	recorder.verifyPersistedAllocation = true

	if err := recorder.installer.installISOCompose(context.Background(), recorder); err != nil {
		t.Fatalf("installISOCompose: %v", err)
	}
	if recorder.installer.isoAllocation == nil || recorder.installer.isoAllocation.NetNS != recorder.reserved.NetNS {
		t.Fatalf("installer allocation = %#v, want persisted same-service allocation %#v", recorder.installer.isoAllocation, recorder.reserved)
	}
}

func TestISOInstallPreservesExitNodeRejectionBeforeTailscaleSideEffects(t *testing.T) {
	recorder := newISOInstallRecorder(t)
	recorder.installer.tsNet.ExitNode = "exit.example"

	err := recorder.installer.installISOCompose(context.Background(), recorder)
	if err == nil || !strings.Contains(err.Error(), "exit node") {
		t.Fatalf("installISOCompose error = %v, want Task 8 exit-node rejection", err)
	}
	for _, forbidden := range []string{"pull", "build", "attach-network", "start-aux", "compose-up"} {
		if slices.Contains(recorder.events, forbidden) {
			t.Fatalf("%s ran after exit-node rejection: %#v", forbidden, recorder.events)
		}
	}
	if recorder.tailscaleMutated {
		t.Fatalf("Tailscale state mutated before exit-node rejection: %#v", recorder.events)
	}
}

func TestISOInstallInspectionFailureDownsBeforeQuarantine(t *testing.T) {
	recorder := newISOInstallRecorder(t)
	recorder.failAt = "inspect-runtime"

	err := recorder.installer.installISOCompose(context.Background(), recorder)
	if err == nil || !strings.Contains(err.Error(), "inspect-runtime") {
		t.Fatalf("installISOCompose error = %v, want inspection failure", err)
	}
	wantTail := []string{"inspect-runtime", "compose-down-remove-orphans", "quarantine"}
	if got := recorder.events[len(recorder.events)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("inspection failure tail = %#v, want %#v", got, wantTail)
	}
	allocation := testService(t, recorder.server, "app").ISO
	if allocation == nil || allocation.State != string(iso.StateQuarantined) {
		t.Fatalf("persisted allocation = %#v, want quarantined", allocation)
	}
}

func TestISOInstallMutationFailuresDownBeforeQuarantine(t *testing.T) {
	for _, phase := range []string{"attach-network", "start-aux", "compose-up"} {
		t.Run(phase, func(t *testing.T) {
			recorder := newISOInstallRecorder(t)
			recorder.failAt = phase

			err := recorder.installer.installISOCompose(context.Background(), recorder)
			if err == nil || !strings.Contains(err.Error(), phase) {
				t.Fatalf("installISOCompose error = %v, want %s failure", err, phase)
			}
			wantTail := []string{phase, "compose-down-remove-orphans", "quarantine"}
			if got := recorder.events[len(recorder.events)-len(wantTail):]; !slices.Equal(got, wantTail) {
				t.Fatalf("mutation failure tail = %#v, want %#v", got, wantTail)
			}
		})
	}
}

func TestISOInstallCancellationUsesLiveCleanupContext(t *testing.T) {
	recorder := newISOInstallRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	recorder.cancelAt = "inspect-runtime"
	recorder.cancel = cancel

	err := recorder.installer.installISOCompose(ctx, recorder)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("installISOCompose error = %v, want cancellation", err)
	}
	if !recorder.downContextLive || !recorder.quarantineContextLive {
		t.Fatalf("cleanup contexts live = down %v quarantine %v, want both true", recorder.downContextLive, recorder.quarantineContextLive)
	}
}

func TestISOInstallQuarantineGetsFreshContextAfterDownDeadline(t *testing.T) {
	oldTimeout := isoSecurityCleanupTimeout
	isoSecurityCleanupTimeout = 5 * time.Millisecond
	t.Cleanup(func() { isoSecurityCleanupTimeout = oldTimeout })
	recorder := newISOInstallRecorder(t)
	recorder.failAt = "inspect-runtime"
	recorder.exhaustDownContext = true

	if err := recorder.installer.installISOCompose(context.Background(), recorder); err == nil {
		t.Fatal("installISOCompose unexpectedly succeeded")
	}
	if !recorder.quarantineContextLive {
		t.Fatal("quarantine inherited the expired compose-down cleanup context")
	}
}

type isoInstallRecorder struct {
	t                         *testing.T
	server                    *Server
	installer                 *FileInstaller
	events                    []string
	failAt                    string
	cancelAt                  string
	cancel                    context.CancelFunc
	reserved                  *db.ISOAllocation
	verifyPersistedAllocation bool
	tailscaleMutated          bool
	downContextLive           bool
	quarantineContextLive     bool
	exhaustDownContext        bool
}

func newISOInstallRecorder(t *testing.T) *isoInstallRecorder {
	t.Helper()
	server := newTestServer(t)
	addTestServices(t, server, db.Service{Name: "app", ServiceType: db.ServiceTypeDockerCompose})
	reserved := &db.ISOAllocation{
		Kind:         string(iso.PayloadCompose),
		State:        string(iso.StateReserved),
		DesiredModes: []string{"iso", "ts"},
		NetNS:        "yeet-a172cedcae-ns",
		Components: map[string]db.ISOComponent{
			"api": {Address: netip.MustParseAddr("172.30.128.2")},
		},
	}
	return &isoInstallRecorder{
		t:        t,
		server:   server,
		reserved: reserved,
		installer: &FileInstaller{
			s: server,
			cfg: FileInstallerCfg{
				InstallerCfg: InstallerCfg{ServiceName: "app"},
				Network:      NetworkOpts{Interfaces: "iso,ts", ISO: true, Modes: []string{"iso", "ts"}},
			},
			tsNet: &db.TailscaleNetwork{Interface: "untrusted"},
		},
	}
}

func (r *isoInstallRecorder) step(name string) error {
	r.events = append(r.events, name)
	if name == r.cancelAt {
		r.cancel()
		return context.Canceled
	}
	if name == r.failAt {
		return errors.New(name + " failed")
	}
	return nil
}

func (r *isoInstallRecorder) ResolveBase(context.Context) error    { return r.step("resolve-base") }
func (r *isoInstallRecorder) AdmitBase(context.Context) error      { return r.step("admit-base") }
func (r *isoInstallRecorder) RenderOverlay(context.Context) error  { return r.step("render-overlay") }
func (r *isoInstallRecorder) ResolveMerged(context.Context) error  { return r.step("resolve-merged") }
func (r *isoInstallRecorder) AdmitMerged(context.Context) error    { return r.step("admit-merged") }
func (r *isoInstallRecorder) InstallDNS(context.Context) error     { return r.step("install-dns") }
func (r *isoInstallRecorder) EnsurePolicy(context.Context) error   { return r.step("ensure-policy") }
func (r *isoInstallRecorder) VerifyPolicy(context.Context) error   { return r.step("verify-policy") }
func (r *isoInstallRecorder) EnsureTopology(context.Context) error { return r.step("ensure-topology") }
func (r *isoInstallRecorder) VerifyTopology(context.Context) error { return r.step("verify-topology") }
func (r *isoInstallRecorder) Pull(context.Context) error           { return r.step("pull") }
func (r *isoInstallRecorder) Build(context.Context) error          { return r.step("build") }
func (r *isoInstallRecorder) AttachNetwork(context.Context) error  { return r.step("attach-network") }
func (r *isoInstallRecorder) StartAux(context.Context) error       { return r.step("start-aux") }
func (r *isoInstallRecorder) ComposeUp(context.Context) error      { return r.step("compose-up") }
func (r *isoInstallRecorder) InspectRuntime(context.Context) error { return r.step("inspect-runtime") }
func (r *isoInstallRecorder) MarkReady(context.Context) error      { return r.step("mark-ready") }

func (r *isoInstallRecorder) Reserve(context.Context) error {
	if err := r.step("reserve"); err != nil {
		return err
	}
	_, _, err := r.server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.ISO = r.reserved.Clone()
		return nil
	})
	return err
}

func (r *isoInstallRecorder) InstallTailscale(context.Context, *FileInstaller) error {
	if err := r.step("install-tailscale"); err != nil {
		return err
	}
	if r.verifyPersistedAllocation && !reflect.DeepEqual(r.installer.isoAllocation, r.reserved) {
		return fmt.Errorf("installer allocation = %#v, want persisted %#v", r.installer.isoAllocation, r.reserved)
	}
	_, _, _, err := r.installer.tailscaleNetNSMode(&netns.Service{ServiceName: "app"})
	if err != nil {
		return err
	}
	r.tailscaleMutated = true
	return nil
}

func (r *isoInstallRecorder) ComposeDownRemoveOrphans(ctx context.Context) error {
	r.downContextLive = ctx.Err() == nil
	if r.exhaustDownContext {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.step("compose-down-remove-orphans")
}

func (r *isoInstallRecorder) Quarantine(ctx context.Context, cause error) error {
	r.quarantineContextLive = ctx.Err() == nil
	r.events = append(r.events, "quarantine")
	_, _, err := r.server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		if service.ISO == nil {
			service.ISO = &db.ISOAllocation{}
		}
		service.ISO.State = string(iso.StateQuarantined)
		service.ISO.LastError = cause.Error()
		return nil
	})
	return err
}
