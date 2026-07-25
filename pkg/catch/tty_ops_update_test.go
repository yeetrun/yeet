// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"golang.org/x/sys/unix"
)

func TestTsCmdUpdateUsesYeetManagedUpdater(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	const (
		svcName    = "svc-ts-update"
		oldVersion = tailscaleResolverFixtureDaemonVersion
		newVersion = "1.94.2"
	)
	fixture := newGuardedTailscaleResolverFixture(
		t,
		svcName,
		tailscaleResolverGenerationCurrent,
	)
	server := fixture.server
	serviceBinDir := server.serviceBinDir(svcName)

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	newDaemon := filepath.Join(tsdDir, "tailscaled-"+newVersion)
	if err := os.WriteFile(newDaemon, []byte("new-daemon"), 0o755); err != nil {
		t.Fatalf("write new tailscaled: %v", err)
	}
	newClient := filepath.Join(tsdDir, "tailscale-"+newVersion)
	if err := os.WriteFile(newClient, []byte("new-client"), 0o755); err != nil {
		t.Fatalf("write new tailscale: %v", err)
	}

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	var gotTrack string
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		gotTrack = track
		return newVersion, nil
	}

	var out bytes.Buffer
	var verifiedRestarts []string
	stubVerifiedTailscaleRestart(t, &verifiedRestarts)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	execer := &ttyExecer{
		ctx: ctx,
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader("y\n"), Writer: &out},
	}

	if err := execer.tsCmdFunc([]string{"update"}); err != nil {
		t.Fatalf("tsCmdFunc(update): %v", err)
	}

	if gotTrack != "stable" {
		t.Fatalf("track = %q, want stable", gotTrack)
	}
	if got := out.String(); !strings.Contains(got, "yeet-managed") {
		t.Fatalf("expected yeet-managed message, got %q", got)
	}
	if got := out.String(); !strings.Contains(got, "Continue? [y/n]") {
		t.Fatalf("expected confirmation prompt, got %q", got)
	}

	sv, err := server.serviceView(svcName)
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.TSNet().Version(); got != newVersion {
		t.Fatalf("TSNet.Version = %q, want %q", got, newVersion)
	}

	runBinary, err := os.ReadFile(filepath.Join(serviceBinDir, "tailscaled"))
	if err != nil {
		t.Fatalf("read run tailscaled: %v", err)
	}
	if got := string(runBinary); got != "new-daemon" {
		t.Fatalf("managed tailscaled = %q, want %q", got, "new-daemon")
	}
	if len(verifiedRestarts) != 1 || verifiedRestarts[0] != svcName {
		t.Fatalf("verified restarts = %v, want [%s]", verifiedRestarts, svcName)
	}
}

func TestTailscaleResolverReadinessGatesUpdateBeforeBinaryReplacement(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(
		t,
		"update-readiness",
		tailscaleResolverGenerationCurrent,
		"",
	)
	const latest = "1.94.2"
	tsdDir := filepath.Join(fixture.server.cfg.RootDir, "tsd")
	for name, raw := range map[string]string{
		"tailscaled-" + latest: "replacement-daemon",
		"tailscale-" + latest:  "replacement-client",
	} {
		if err := os.WriteFile(filepath.Join(tsdDir, name), []byte(raw), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	managedBinary := fixtureTuple(
		fixture.service,
		tailscaleResolverGenerationCurrent,
	).daemon
	before, err := os.ReadFile(managedBinary)
	if err != nil {
		t.Fatalf("read managed binary before update: %v", err)
	}
	oldRestart := restartTailscaleSystemdSidecar
	restartCalls := 0
	restartTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		restartCalls++
		return nil
	}
	t.Cleanup(func() { restartTailscaleSystemdSidecar = oldRestart })

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   fixture.server,
		sn:  fixture.service.Name,
		rw:  readWriter{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}},
	}
	err = execer.applyTSUpdate(tailscaleResolverFixtureDaemonVersion, latest)
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("applyTSUpdate error = %v, want resolver readiness rejection", err)
	}
	after, readErr := os.ReadFile(managedBinary)
	if readErr != nil {
		t.Fatalf("read managed binary after rejected update: %v", readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("rejected update replaced managed binary: got %q, want %q", after, before)
	}
	if restartCalls != 0 {
		t.Fatalf("rejected update restart calls = %d, want 0", restartCalls)
	}
}

func TestTailscaleResolverReadinessLinearizesUpdateBinaryCopyWithGlobalBlock(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(
		t,
		"update-copy-linearized",
		tailscaleResolverGenerationCurrent,
	)
	const latest = "1.94.2"
	tsdDir := filepath.Join(fixture.server.cfg.RootDir, "tsd")
	sourceDaemon := filepath.Join(tsdDir, "tailscaled-"+latest)
	if err := unix.Mkfifo(sourceDaemon, 0o600); err != nil {
		t.Fatalf("create blocking tailscaled source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscale-"+latest), []byte("client"), 0o755); err != nil {
		t.Fatalf("write tailscale client: %v", err)
	}
	copyEntered := make(chan struct{})
	releaseCopy := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case releaseCopy <- struct{}{}:
		default:
		}
	})
	go func() {
		f, err := os.OpenFile(sourceDaemon, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		close(copyEntered)
		<-releaseCopy
		_, _ = f.WriteString("replacement-daemon")
		_ = f.Close()
	}()
	oldRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error { return nil }
	t.Cleanup(func() { restartTailscaleSystemdSidecar = oldRestart })
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   fixture.server,
		sn:  fixture.service.Name,
		rw:  readWriter{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}},
	}
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- execer.applyTSUpdate(tailscaleResolverFixtureDaemonVersion, latest)
	}()
	awaitResolverTestSignal(t, copyEntered, "tailscaled binary copy")

	writerAttempted := make(chan struct{})
	writerAcquired := make(chan struct{})
	fixture.server.tailscaleResolverRecovery.afterBlockLock = func() {
		close(writerAcquired)
	}
	blockDone := make(chan error, 1)
	go func() {
		close(writerAttempted)
		blockDone <- fixture.server.blockTailscaleResolverRecovery(errors.New("block during update copy"))
	}()
	awaitResolverTestSignal(t, writerAttempted, "resolver block attempt during binary copy")
	select {
	case <-writerAcquired:
		t.Fatal("resolver block acquired the global guard during tailscaled binary copy")
	case <-time.After(25 * time.Millisecond):
	}
	releaseCopy <- struct{}{}
	if err := awaitResolverTestResult(t, updateDone); err != nil {
		t.Fatalf("applyTSUpdate: %v", err)
	}
	if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("resolver block after update copy = %v", err)
	}
}

func TestTsCmdUpdatePassthroughWithDoubleDash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	server := newTestServer(t)
	const (
		svcName = "svc-ts-raw-update"
		version = "1.90.0"
	)

	runDir := server.serviceRunDir(svcName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sock := filepath.Join(runDir, "tailscaled.sock")
	if err := os.WriteFile(sock, []byte(""), 0o644); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	argsLog := filepath.Join(tsdDir, "tailscale-args.log")
	clientBin := filepath.Join(tsdDir, "tailscale-"+version)
	clientScript := "#!/bin/sh\nprintf '%s\n' \"$@\" > \"$TAILSCALE_ARGS_LOG\"\n"
	if err := os.WriteFile(clientBin, []byte(clientScript), 0o755); err != nil {
		t.Fatalf("write fake tailscale client: %v", err)
	}
	t.Setenv("TAILSCALE_ARGS_LOG", argsLog)

	if _, _, err := server.cfg.DB.MutateService(svcName, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 1
		s.LatestGeneration = 1
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-test", Version: version}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		t.Fatalf("latest version resolver should not be called in passthrough mode")
		return "", nil
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}},
	}

	if err := execer.tsCmdFunc([]string{"--", "update"}); err != nil {
		t.Fatalf("tsCmdFunc(-- update): %v", err)
	}

	b, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 args, got %v", lines)
	}
	if got := lines[0]; got != "--socket="+sock {
		t.Fatalf("arg[0] = %q, want %q", got, "--socket="+sock)
	}
	if got := lines[1]; got != "update" {
		t.Fatalf("arg[1] = %q, want update", got)
	}
}

func TestTailscaledManagedBinaryPathFollowsGeneratedUnit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "svc")
	stableCatchRunner := "/srv/catch/run/catch"
	for _, dir := range []string{serviceBinDirForRoot(root), serviceRunDirForRoot(root)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	newPath := filepath.Join(serviceBinDirForRoot(root), "tailscaled")
	legacyPath := filepath.Join(serviceRunDirForRoot(root), "tailscaled")
	for _, path := range []string{newPath, legacyPath} {
		if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		name      string
		execStart string
		want      string
	}{
		{name: "direct managed layout", execStart: newPath + " --statedir=.", want: newPath},
		{name: "direct legacy layout", execStart: legacyPath + " --statedir=.", want: legacyPath},
		{
			name:      "guarded managed layout",
			execStart: stableCatchRunner + " tailscale-resolver-exec --source /etc/netns/yeet-svc-ns/resolv.conf -- " + newPath + " --statedir=.",
			want:      newPath,
		},
		{
			name:      "guarded legacy layout",
			execStart: stableCatchRunner + " tailscale-resolver-exec --source /etc/netns/yeet-svc-ns/resolv.conf -- " + legacyPath + " --statedir=.",
			want:      legacyPath,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unit := filepath.Join(t.TempDir(), "tailscale.service")
			if err := os.WriteFile(unit, []byte("[Service]\nExecStart="+tt.execStart+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sv := (&db.Service{
				Name: "svc",
				Artifacts: db.ArtifactStore{db.ArtifactTSService: {
					Refs: map[db.ArtifactRef]string{"latest": unit},
				}},
			}).View()
			got, err := tailscaledManagedBinaryPath(sv, root, stableCatchRunner)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("managed binary = %q, want unit daemon %q", got, tt.want)
			}
		})
	}
}

func TestTailscaledManagedBinaryPathRejectsUnsafeGeneratedUnit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "svc")
	managed := filepath.Join(serviceBinDirForRoot(root), "tailscaled")
	legacy := filepath.Join(serviceRunDirForRoot(root), "tailscaled")
	stableCatchRunner := "/srv/catch/run/catch"
	for _, tt := range []struct {
		name                string
		unit                string
		expectedCatchRunner string
	}{
		{
			name:                "malformed guarded separator",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf " + managed + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "malformed guarded source flag",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --resolver /etc/resolv.conf -- " + managed + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{name: "unmanaged directory", unit: "[Service]\nExecStart=" + filepath.Join(root, "data", "tailscaled") + "\n", expectedCatchRunner: stableCatchRunner},
		{name: "wrong basename", unit: "[Service]\nExecStart=" + filepath.Join(root, "run", "tailscaled-old") + "\n", expectedCatchRunner: stableCatchRunner},
		{name: "relative executable", unit: "[Service]\nExecStart=run/tailscaled\n", expectedCatchRunner: stableCatchRunner},
		{name: "unclean managed path", unit: "[Service]\nExecStart=" + filepath.Join(root, "bin") + "/../bin/tailscaled\n", expectedCatchRunner: stableCatchRunner},
		{name: "multiple exec starts", unit: "[Service]\nExecStart=" + managed + "\nExecStart=" + legacy + "\n", expectedCatchRunner: stableCatchRunner},
		{name: "empty then managed", unit: "[Service]\nExecStart=\nExecStart=" + managed + "\n", expectedCatchRunner: stableCatchRunner},
		{name: "exec start outside service", unit: "[Unit]\nExecStart=" + managed + "\n[Service]\n", expectedCatchRunner: stableCatchRunner},
		{name: "quoted executable", unit: "[Service]\nExecStart=\"" + managed + "\"\n", expectedCatchRunner: stableCatchRunner},
		{name: "continued exec start", unit: "[Service]\nExecStart=" + managed + " \\\n --statedir=.\n", expectedCatchRunner: stableCatchRunner},
		{
			name:                "guarded unmanaged daemon",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + filepath.Join(root, "data", "tailscaled") + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "arbitrary guard launcher",
			unit:                "[Service]\nExecStart=/bin/echo tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "current daemon as guard launcher",
			unit:                "[Service]\nExecStart=" + managed + " tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "historical daemon as guard launcher",
			unit:                "[Service]\nExecStart=" + legacy + " tailscale-resolver-exec --source /etc/resolv.conf -- " + managed + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "versioned guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/catch-20260725 tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "install staging guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/.install/catch tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "relative guard launcher",
			unit:                "[Service]\nExecStart=run/catch tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "unclean guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/../run/catch tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name:                "wrong basename guard launcher",
			unit:                "[Service]\nExecStart=/srv/catch/run/not-catch tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: stableCatchRunner,
		},
		{
			name: "missing expected catch runner",
			unit: "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
		},
		{
			name:                "mismatched expected catch runner",
			unit:                "[Service]\nExecStart=" + stableCatchRunner + " tailscale-resolver-exec --source /etc/resolv.conf -- " + legacy + "\n",
			expectedCatchRunner: "/srv/other/run/catch",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unitPath := filepath.Join(t.TempDir(), "tailscale.service")
			if err := os.WriteFile(unitPath, []byte(tt.unit), 0o644); err != nil {
				t.Fatal(err)
			}
			sv := (&db.Service{
				Name: "svc",
				Artifacts: db.ArtifactStore{db.ArtifactTSService: {
					Refs: map[db.ArtifactRef]string{"latest": unitPath},
				}},
			}).View()
			if got, err := tailscaledManagedBinaryPath(sv, root, tt.expectedCatchRunner); err == nil {
				t.Fatalf("tailscaledManagedBinaryPath = %q, want unsafe unit rejection", got)
			}
		})
	}
}

func TestTailscaledManagedBinaryPathResolvesMigratedHistoricalUnit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "svc")
	legacy := filepath.Join(serviceRunDirForRoot(root), "tailscaled")
	stableCatchRunner := "/srv/catch/run/catch"
	direct := "[Service]\nExecStart=" + legacy + " --statedir=.\n" +
		"NetworkNamespacePath=/var/run/netns/yeet-svc-ns\n" +
		"EnvironmentFile=" + filepath.Join(serviceEnvDirForRoot(root), "tailscaled.env") + "\n" +
		"WorkingDirectory=" + filepath.Join(root, "tailscale") + "\n"
	guarded, changed, err := ensureTailscaleUnitResolverIsolation(direct, stableCatchRunner)
	if err != nil {
		t.Fatalf("ensureTailscaleUnitResolverIsolation: %v", err)
	}
	if !changed {
		t.Fatal("ensureTailscaleUnitResolverIsolation changed = false")
	}
	unitPath := filepath.Join(t.TempDir(), "tailscale.service")
	if err := os.WriteFile(unitPath, []byte(guarded), 0o644); err != nil {
		t.Fatal(err)
	}
	sv := (&db.Service{
		Name: "svc",
		Artifacts: db.ArtifactStore{db.ArtifactTSService: {
			Refs: map[db.ArtifactRef]string{"latest": unitPath},
		}},
	}).View()

	got, err := tailscaledManagedBinaryPath(sv, root, stableCatchRunner)
	if err != nil {
		t.Fatalf("tailscaledManagedBinaryPath after migration: %v", err)
	}
	if got != legacy {
		t.Fatalf("managed binary after migration = %q, want historical daemon %q", got, legacy)
	}
}

func TestTsCmdUpdateGuardedHistoricalReplacesPersistsAndVerifiedRestarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	const (
		svcName    = "svc-ts-guarded-historical-update"
		oldVersion = tailscaleResolverFixtureDaemonVersion
		newVersion = "1.95.112"
	)
	fixture := newGuardedTailscaleResolverFixture(
		t,
		svcName,
		tailscaleResolverGenerationHistorical,
	)
	server := fixture.server
	runDir := server.serviceRunDir(svcName)
	historicalDaemon := filepath.Join(runDir, "tailscaled")
	currentDaemon := filepath.Join(server.serviceBinDir(svcName), "tailscaled")

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	newDaemon := filepath.Join(tsdDir, "tailscaled-"+newVersion)
	if err := os.WriteFile(newDaemon, []byte("new-daemon"), 0o755); err != nil {
		t.Fatalf("write new tailscaled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscale-"+newVersion), []byte("new-client"), 0o755); err != nil {
		t.Fatalf("write new tailscale client: %v", err)
	}

	previousLatest := tailscaleLatestVersionForTrackFn
	tailscaleLatestVersionForTrackFn = func(string) (string, error) { return newVersion, nil }
	t.Cleanup(func() { tailscaleLatestVersionForTrackFn = previousLatest })

	previousRestart := restartTailscaleSystemdSidecar
	var verifiedRestarts int
	restartTailscaleSystemdSidecar = func(ctx context.Context, service *svc.SystemdService) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		verifiedRestarts++
		if service.Name() != svcName {
			t.Fatalf("verified restart service = %q, want %q", service.Name(), svcName)
		}
		assertFileContent(t, historicalDaemon, "new-daemon")
		persisted, err := server.serviceView(svcName)
		if err != nil {
			t.Fatalf("serviceView during verified restart: %v", err)
		}
		if got := persisted.TSNet().Version(); got != newVersion {
			t.Fatalf("persisted version before verified restart = %q, want %q", got, newVersion)
		}
		binaryArtifact, ok := persisted.Artifacts().GetOk(db.ArtifactTSBinary)
		if !ok {
			t.Fatal("persisted tailscaled artifact is missing before verified restart")
		}
		if got, ok := binaryArtifact.Refs().GetOk(db.ArtifactRef("latest")); !ok || got != newDaemon {
			t.Fatalf("persisted latest tailscaled ref = %q, %v, want %q", got, ok, newDaemon)
		}
		if got, ok := binaryArtifact.Refs().GetOk(db.Gen(fixture.service.Generation)); !ok || got != newDaemon {
			t.Fatalf("persisted generation tailscaled ref = %q, %v, want %q", got, ok, newDaemon)
		}
		return nil
	}
	t.Cleanup(func() { restartTailscaleSystemdSidecar = previousRestart })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	execer := &ttyExecer{
		ctx: ctx,
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader("y\n"), Writer: &bytes.Buffer{}},
	}
	if err := execer.tsCmdFunc([]string{"update"}); err != nil {
		t.Fatalf("tsCmdFunc(update): %v", err)
	}

	if verifiedRestarts != 1 {
		t.Fatalf("verified restart calls = %d, want 1", verifiedRestarts)
	}
	assertFileContent(t, historicalDaemon, "new-daemon")
	if _, err := os.Lstat(currentDaemon); !os.IsNotExist(err) {
		t.Fatalf("current-layout daemon unexpectedly created at %s: %v", currentDaemon, err)
	}
}

func TestTsCmdUpdatePinnedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	const (
		svcName       = "svc-ts-pinned-update"
		currentVer    = tailscaleResolverFixtureDaemonVersion
		pinnedVersion = "1.95.112"
	)
	fixture := newGuardedTailscaleResolverFixture(
		t,
		svcName,
		tailscaleResolverGenerationHistorical,
	)
	server := fixture.server
	runDir := server.serviceRunDir(svcName)

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	newDaemon := filepath.Join(tsdDir, "tailscaled-"+pinnedVersion)
	if err := os.WriteFile(newDaemon, []byte("pinned-daemon"), 0o755); err != nil {
		t.Fatalf("write new tailscaled: %v", err)
	}
	newClient := filepath.Join(tsdDir, "tailscale-"+pinnedVersion)
	if err := os.WriteFile(newClient, []byte("pinned-client"), 0o755); err != nil {
		t.Fatalf("write new tailscale: %v", err)
	}

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		t.Fatalf("latest resolver should not be called for pinned update")
		return "", nil
	}

	var verifiedRestarts []string
	stubVerifiedTailscaleRestart(t, &verifiedRestarts)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	execer := &ttyExecer{
		ctx: ctx,
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader("y\n"), Writer: &bytes.Buffer{}},
	}
	if err := execer.tsCmdFunc([]string{"update", pinnedVersion}); err != nil {
		t.Fatalf("tsCmdFunc(update <version>): %v", err)
	}

	sv, err := server.serviceView(svcName)
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.TSNet().Version(); got != pinnedVersion {
		t.Fatalf("TSNet.Version = %q, want %q", got, pinnedVersion)
	}
	assertFileContent(t, filepath.Join(runDir, "tailscaled"), "pinned-daemon")
	if len(verifiedRestarts) != 1 || verifiedRestarts[0] != svcName {
		t.Fatalf("verified restarts = %v, want [%s]", verifiedRestarts, svcName)
	}
}

func TestTsCmdUpdateRepairsMetadataMatchedButSelectedBinaryOlder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	const targetVersion = "1.95.112"
	fixture := newGuardedTailscaleResolverFixture(
		t,
		"svc-ts-stale-version-metadata",
		tailscaleResolverGenerationCurrent,
	)
	server := fixture.server
	if _, _, err := server.cfg.DB.MutateService(fixture.service.Name, func(_ *db.Data, service *db.Service) error {
		service.TSNet.Version = targetVersion
		return nil
	}); err != nil {
		t.Fatalf("seed stale version metadata: %v", err)
	}

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	for name, raw := range map[string]string{
		"tailscaled-" + targetVersion: "replacement-daemon",
		"tailscale-" + targetVersion:  "replacement-client",
	} {
		if err := os.WriteFile(filepath.Join(tsdDir, name), []byte(raw), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	var verifiedRestarts []string
	stubVerifiedTailscaleRestart(t, &verifiedRestarts)
	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  fixture.service.Name,
		rw:  readWriter{Reader: strings.NewReader("y\n"), Writer: &out},
	}
	if err := execer.tsCmdFunc([]string{"update", targetVersion}); err != nil {
		t.Fatalf("tsCmdFunc(update <version>): %v", err)
	}

	if strings.Contains(out.String(), "Already up to date") {
		t.Fatalf("update output trusted stale metadata: %q", out.String())
	}
	if !strings.Contains(out.String(), tailscaleResolverFixtureDaemonVersion+" -> "+targetVersion) {
		t.Fatalf("update output = %q, want selected generation version transition", out.String())
	}
	assertFileContent(t, filepath.Join(server.serviceBinDir(fixture.service.Name), "tailscaled"), "replacement-daemon")
	if len(verifiedRestarts) != 1 || verifiedRestarts[0] != fixture.service.Name {
		t.Fatalf("verified restarts = %v, want [%s]", verifiedRestarts, fixture.service.Name)
	}
	persisted, err := server.serviceView(fixture.service.Name)
	if err != nil {
		t.Fatal(err)
	}
	binaryArtifact, ok := persisted.Artifacts().GetOk(db.ArtifactTSBinary)
	if !ok {
		t.Fatal("persisted tailscaled artifact is missing")
	}
	if got, ok := binaryArtifact.Refs().GetOk(db.Gen(fixture.service.Generation)); !ok ||
		got != filepath.Join(tsdDir, "tailscaled-"+targetVersion) {
		t.Fatalf("persisted selected tailscaled artifact = %q, %v", got, ok)
	}
}

func TestTsCmdUpdateDoesNotReportCurrentWhenSelectedRuntimeDrifted(t *testing.T) {
	const targetVersion = "1.95.112"
	fixture := newGuardedTailscaleResolverFixture(
		t,
		"svc-ts-drifted-selected-runtime",
		tailscaleResolverGenerationCurrent,
	)
	targetArtifact := filepath.Join(fixture.server.cfg.RootDir, "tsd", "tailscaled-"+targetVersion)
	if err := os.WriteFile(targetArtifact, []byte("replacement-daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.server.cfg.DB.MutateService(
		fixture.service.Name,
		func(_ *db.Data, service *db.Service) error {
			service.TSNet.Version = targetVersion
			artifact := service.Artifacts[db.ArtifactTSBinary]
			artifact.Refs[db.Gen(service.Generation)] = targetArtifact
			artifact.Refs[db.ArtifactRef("latest")] = targetArtifact
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := (&ttyExecer{
		ctx: context.Background(),
		s:   fixture.server,
		sn:  fixture.service.Name,
		rw:  readWriter{Reader: strings.NewReader(""), Writer: &out},
	}).tsCmdFunc([]string{"update", targetVersion})
	if err == nil || !strings.Contains(err.Error(), "does not match selected runtime") {
		t.Fatalf("tsCmdFunc error = %v, want selected runtime drift rejection", err)
	}
	if strings.Contains(out.String(), "Already up to date") {
		t.Fatalf("update output reported unproved current state: %q", out.String())
	}
}

func TestTsCmdUpdateRepairsMetadataOlderThanProvenSelectedRuntime(t *testing.T) {
	for index, staleMetadata := range []string{"1.90.0", " 1.92.3 "} {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fixture := newGuardedTailscaleResolverFixture(
				t,
				fmt.Sprintf("svc-ts-stale-old-version-metadata-%d", index),
				tailscaleResolverGenerationCurrent,
			)
			if _, _, err := fixture.server.cfg.DB.MutateService(
				fixture.service.Name,
				func(_ *db.Data, service *db.Service) error {
					service.TSNet.Version = staleMetadata
					return nil
				},
			); err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			if err := (&ttyExecer{
				ctx: context.Background(),
				s:   fixture.server,
				sn:  fixture.service.Name,
				rw:  readWriter{Reader: strings.NewReader(""), Writer: &out},
			}).tsCmdFunc([]string{"update", tailscaleResolverFixtureDaemonVersion}); err != nil {
				t.Fatalf("tsCmdFunc(update <version>): %v", err)
			}
			if !strings.Contains(out.String(), "Already up to date") {
				t.Fatalf("update output = %q, want proven current status", out.String())
			}
			persisted, err := fixture.server.serviceView(fixture.service.Name)
			if err != nil {
				t.Fatal(err)
			}
			if got := persisted.TSNet().Version(); got != tailscaleResolverFixtureDaemonVersion {
				t.Fatalf("repaired TSNet.Version = %q, want %q", got, tailscaleResolverFixtureDaemonVersion)
			}
		})
	}
}

func stubVerifiedTailscaleRestart(t *testing.T, calls *[]string) {
	t.Helper()
	previous := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(ctx context.Context, service *svc.SystemdService) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		*calls = append(*calls, service.Name())
		return nil
	}
	t.Cleanup(func() { restartTailscaleSystemdSidecar = previous })
}

func TestTsCmdUpdateCanceledByUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	server := newTestServer(t)
	const (
		svcName    = "svc-ts-cancel-update"
		oldVersion = "1.90.0"
		newVersion = "1.95.112"
	)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+newVersion), []byte("new-daemon"), 0o755); err != nil {
		t.Fatalf("write new tailscaled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscale-"+newVersion), []byte("new-client"), 0o755); err != nil {
		t.Fatalf("write new tailscale: %v", err)
	}

	if _, _, err := server.cfg.DB.MutateService(svcName, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 1
		s.LatestGeneration = 1
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-test", Version: oldVersion}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		return newVersion, nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
	}
	if err := execer.tsCmdFunc([]string{"update"}); err != nil {
		t.Fatalf("tsCmdFunc(update): %v", err)
	}

	if got := out.String(); !strings.Contains(got, "Continue? [y/n]") {
		t.Fatalf("expected confirmation prompt, got %q", got)
	}
	if got := out.String(); !strings.Contains(got, "Update canceled.") {
		t.Fatalf("expected cancellation message, got %q", got)
	}
	sv, err := server.serviceView(svcName)
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.TSNet().Version(); got != oldVersion {
		t.Fatalf("TSNet.Version = %q, want %q", got, oldVersion)
	}
}

func TestParseTSUpdateTarget(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantTarget string
		wantPinned bool
		wantErr    bool
	}{
		{name: "latest default", args: nil, wantTarget: "", wantPinned: false, wantErr: false},
		{name: "positional pinned", args: []string{"1.95.112"}, wantTarget: "1.95.112", wantPinned: true, wantErr: false},
		{name: "version equals flag", args: []string{"--version=1.95.112"}, wantTarget: "1.95.112", wantPinned: true, wantErr: false},
		{name: "version split flag", args: []string{"--version", "1.95.112"}, wantTarget: "1.95.112", wantPinned: true, wantErr: false},
		{name: "invalid long flag", args: []string{"--check"}, wantErr: true},
		{name: "too many args", args: []string{"1.95.112", "extra"}, wantErr: true},
		{name: "invalid version", args: []string{"not-a-version"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTarget, gotPinned, err := parseTSUpdateTarget(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotTarget != tt.wantTarget {
				t.Fatalf("target = %q, want %q", gotTarget, tt.wantTarget)
			}
			if gotPinned != tt.wantPinned {
				t.Fatalf("pinned = %v, want %v", gotPinned, tt.wantPinned)
			}
		})
	}
}
