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
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type tailscaleResolverPlanFixture struct {
	server    *Server
	service   db.Service
	canonical string
	installed string
	unit      string
}

type tailscaleResolverTuple struct {
	daemon          string
	environmentFile string
	configFile      string
	socketFile      string
	workingDir      string
	interfaceName   string
	args            []string
}

const (
	tailscaleResolverFixtureArtifactVersion = "20260725010101"
	tailscaleResolverFixtureDaemonVersion   = "1.92.3"
)

func TestPlanTailscaleResolverIsolationFleetAcceptsCompleteHistoricalAndCurrentGenerations(t *testing.T) {
	for _, layout := range []tailscaleResolverGenerationLayout{
		tailscaleResolverGenerationHistorical,
		tailscaleResolverGenerationCurrent,
	} {
		t.Run(string(layout), func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(t, "api", layout, "")
			plan := planTailscaleResolverFleet(t, fixture)
			if len(plan.Services) != 1 {
				t.Fatalf("services = %d, want 1", len(plan.Services))
			}

			want := fixtureGeneration(fixture.service, layout)
			if diff := cmp.Diff(want, plan.Services[0].Generation); diff != "" {
				t.Fatalf("generation (-want +got):\n%s", diff)
			}
			assertTailscaleResolverPlannedFiles(t, fixture, plan.Services[0], want)
		})
	}
}

func TestPlanTailscaleResolverIsolationFleetAcceptsMultipleHistoricalServices(t *testing.T) {
	s := newTestServer(t)
	useTestSystemdSystemDir(t)
	stubTailscaleResolverActive(t, nil)
	first := addTailscaleResolverPlanService(t, s, "alpha", tailscaleResolverGenerationHistorical, "", nil)
	second := addTailscaleResolverPlanService(t, s, "beta", tailscaleResolverGenerationHistorical, "", nil)
	dv, err := s.getDB()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.planTailscaleResolverIsolationFleet(context.Background(), dv)
	if err != nil {
		t.Fatalf("planTailscaleResolverIsolationFleet: %v", err)
	}
	if got := []string{plan.Services[0].ServiceName, plan.Services[1].ServiceName}; !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("service order = %q, want [alpha beta]", got)
	}
	assertTailscaleResolverPlannedFiles(t, first, plan.Services[0], fixtureGeneration(first.service, tailscaleResolverGenerationHistorical))
	assertTailscaleResolverPlannedFiles(t, second, plan.Services[1], fixtureGeneration(second.service, tailscaleResolverGenerationHistorical))
}

func TestPlanTailscaleResolverIsolationFleetAcceptsHistoricalWritableAndMissingBinds(t *testing.T) {
	for _, bind := range []string{
		"BindPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf",
		"",
	} {
		name := "missing"
		if bind != "" {
			name = "writable"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationHistorical, bind)
			servicePlan := planTailscaleResolverFleet(t, fixture).Services[0]
			assertTailscaleResolverPlannedFiles(
				t,
				fixture,
				servicePlan,
				fixtureGeneration(fixture.service, tailscaleResolverGenerationHistorical),
			)
		})
	}
}

func TestPlanTailscaleResolverIsolationFleetRejectsEveryMixedGenerationTuple(t *testing.T) {
	for mask := 1; mask < 7; mask++ {
		t.Run(fmt.Sprintf("tuple-%03b", mask), func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixtureWithMutation(
				t,
				"api",
				tailscaleResolverGenerationHistorical,
				func(tuple *tailscaleResolverTuple, root string) {
					if mask&1 != 0 {
						tuple.daemon = filepath.Join(root, "bin", "tailscaled")
					}
					if mask&2 != 0 {
						tuple.environmentFile = filepath.Join(root, "env", "tailscaled.env")
					}
					if mask&4 != 0 {
						tuple.configFile = filepath.Join(root, "env", "tailscaled.json")
						tuple.args[2] = "--config=" + tuple.configFile
					}
				},
			)
			assertTailscaleResolverPlanRejected(t, fixture, "exact managed generation")
		})
	}
}

func TestPlanTailscaleResolverIsolationFleetRejectsWrongEnvWorkingSocketTunOrOrder(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tailscaleResolverTuple, string)
	}{
		{
			name: "environment file",
			mutate: func(tuple *tailscaleResolverTuple, root string) {
				tuple.environmentFile = filepath.Join(root, "env", "other.env")
			},
		},
		{
			name: "working directory",
			mutate: func(tuple *tailscaleResolverTuple, root string) {
				tuple.workingDir = filepath.Join(root, "data")
			},
		},
		{
			name: "socket",
			mutate: func(tuple *tailscaleResolverTuple, root string) {
				tuple.socketFile = filepath.Join(root, "run", "other.sock")
				tuple.args[1] = "--socket=" + tuple.socketFile
			},
		},
		{
			name: "tun",
			mutate: func(tuple *tailscaleResolverTuple, _ string) {
				tuple.args[3] = "--tun=other"
			},
		},
		{
			name: "argument order",
			mutate: func(tuple *tailscaleResolverTuple, _ string) {
				tuple.args[1], tuple.args[2] = tuple.args[2], tuple.args[1]
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixtureWithMutation(
				t,
				"api",
				tailscaleResolverGenerationCurrent,
				tt.mutate,
			)
			assertTailscaleResolverPlanRejected(t, fixture, "exact managed generation")
		})
	}
}

func TestPlanTailscaleResolverIsolationFleetRejectsArtifactProvenanceMismatch(t *testing.T) {
	t.Run("installed content", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		raw := strings.Replace(fixture.unit, "--tun=ts0", "--tun=other", 1)
		if err := os.WriteFile(fixture.installed, []byte(raw), 0o640); err != nil {
			t.Fatal(err)
		}
		assertTailscaleResolverPlanRejected(t, fixture, "diverges from canonical artifact")
	})

	t.Run("hard linked runtime artifact", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		daemon, _ := fixture.service.Artifacts.Gen(db.ArtifactTSBinary, fixture.service.Generation)
		if err := os.Link(daemon, daemon+".link"); err != nil {
			t.Fatal(err)
		}
		assertTailscaleResolverPlanRejected(t, fixture, "exactly one hard link")
	})

	t.Run("generation artifact hash", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		artifact, _ := fixture.service.Artifacts.Gen(db.ArtifactTSEnv, fixture.service.Generation)
		if err := os.WriteFile(artifact, []byte("TS_LOGS_DIR=/different\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		assertTailscaleResolverPlanRejected(t, fixture, "does not match selected runtime")
	})
}

func TestPlanTailscaleResolverIsolationFleetRejectsUnmanagedGenerationArtifactLocations(t *testing.T) {
	for _, artifact := range []db.ArtifactName{
		db.ArtifactTSService,
		db.ArtifactTSBinary,
		db.ArtifactTSEnv,
		db.ArtifactTSConfig,
	} {
		t.Run(string(artifact), func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
			source, ok := fixture.service.Artifacts.Gen(artifact, fixture.service.Generation)
			if !ok {
				t.Fatalf("fixture missing %s", artifact)
			}
			outside := filepath.Join(t.TempDir(), filepath.Base(source))
			retargetTailscaleResolverArtifact(t, &fixture, artifact, outside)

			assertTailscaleResolverPlanRejected(t, fixture, "managed generation artifact location")
		})
	}
}

func TestPlanTailscaleResolverIsolationFleetRejectsGenerationArtifactPathAliases(t *testing.T) {
	t.Run("nested alias", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		source, _ := fixture.service.Artifacts.Gen(db.ArtifactTSEnv, fixture.service.Generation)
		alias := filepath.Join(
			fixture.service.ServiceRoot,
			"tailscale",
			"alias",
			filepath.Base(source),
		)
		retargetTailscaleResolverArtifact(t, &fixture, db.ArtifactTSEnv, alias)

		assertTailscaleResolverPlanRejected(t, fixture, "managed generation artifact location")
	})

	t.Run("unversioned environment", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		unversioned := filepath.Join(fixture.service.ServiceRoot, "tailscale", "tailscaled.env")
		retargetTailscaleResolverArtifact(t, &fixture, db.ArtifactTSEnv, unversioned)

		assertTailscaleResolverPlanRejected(t, fixture, "versioned")
	})

	t.Run("invalid daemon version", func(t *testing.T) {
		for _, base := range []string{
			"tailscaled-not-semver",
			"tailscaled-1.92",
			"tailscaled-v1.92.3",
			"tailscaled-01.92.3",
		} {
			t.Run(base, func(t *testing.T) {
				fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
				invalidVersion := filepath.Join(fixture.server.cfg.RootDir, "tsd", base)
				retargetTailscaleResolverArtifact(t, &fixture, db.ArtifactTSBinary, invalidVersion)

				assertTailscaleResolverPlanRejected(t, fixture, "versioned filename")
			})
		}
	})
}

func TestPlanTailscaleResolverIsolationFleetUsesSelectedGenerationVersionInsteadOfMutableMetadata(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
	fixture.service.TSNet.Version = "1.95.112"
	replaceTailscaleResolverPlanService(t, fixture.server, fixture.service)

	plan := planTailscaleResolverFleet(t, fixture)
	if len(plan.Services) != 1 {
		t.Fatalf("planned services = %d, want 1", len(plan.Services))
	}
}

func TestPlanTailscaleResolverIsolationFleetRejectsSymlinkedGenerationArtifactParent(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
	tailscaleDir := filepath.Join(fixture.service.ServiceRoot, "tailscale")
	outside := filepath.Join(t.TempDir(), "tailscale")
	if err := os.Rename(tailscaleDir, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, tailscaleDir); err != nil {
		t.Fatal(err)
	}

	assertTailscaleResolverPlanRejected(t, fixture, "without following symlinks")
}

func TestPlanTailscaleResolverIsolationFleetRequiresAcceptDNSFalse(t *testing.T) {
	for _, raw := range []string{`{"acceptDNS":true}`, `{}`} {
		t.Run(raw, func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
			config, _ := fixture.service.Artifacts.Gen(db.ArtifactTSConfig, fixture.service.Generation)
			if err := os.WriteFile(config, []byte(raw), 0o640); err != nil {
				t.Fatal(err)
			}
			runtimeConfig := fixtureTuple(fixture.service, tailscaleResolverGenerationCurrent).configFile
			if err := os.WriteFile(runtimeConfig, []byte(raw), 0o640); err != nil {
				t.Fatal(err)
			}
			assertTailscaleResolverPlanRejected(t, fixture, "acceptDNS=false")
		})
	}
}

func TestPlanTailscaleResolverIsolationFleetInvalidServiceHasZeroSideEffects(t *testing.T) {
	s := newTestServer(t)
	useTestSystemdSystemDir(t)
	good := addTailscaleResolverPlanService(t, s, "alpha", tailscaleResolverGenerationCurrent, "", nil)
	otherBad := addTailscaleResolverPlanService(
		t,
		s,
		"omega",
		tailscaleResolverGenerationCurrent,
		"",
		func(tuple *tailscaleResolverTuple, root string) {
			tuple.workingDir = filepath.Join(root, "data")
		},
	)
	bad := addTailscaleResolverPlanService(
		t,
		s,
		"zeta",
		tailscaleResolverGenerationCurrent,
		"",
		func(tuple *tailscaleResolverTuple, _ string) {
			tuple.args[3] = "--tun=wrong"
		},
	)
	stubTailscaleResolverActive(t, map[string]bool{
		"yeet-alpha-ts.service": true,
		"yeet-omega-ts.service": true,
		"yeet-zeta-ts.service":  true,
	})

	var writes, stops, reloads, restarts, verifies int
	oldWrite := writeTailscaleResolverUnitFile
	oldSystemctl := catchSystemctl
	oldRestart := restartTailscaleSystemdSidecar
	oldVerify := verifyTailscaleSystemdSidecar
	writeTailscaleResolverUnitFile = func(string, []byte, os.FileMode) error {
		writes++
		return nil
	}
	catchSystemctl = func(args ...string) error {
		if len(args) > 0 && args[0] == "stop" {
			stops++
		} else {
			reloads++
		}
		return nil
	}
	restartTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		restarts++
		return nil
	}
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		verifies++
		return nil
	}
	t.Cleanup(func() {
		writeTailscaleResolverUnitFile = oldWrite
		catchSystemctl = oldSystemctl
		restartTailscaleSystemdSidecar = oldRestart
		verifyTailscaleSystemdSidecar = oldVerify
	})

	var wantPlan string
	var wantErr string
	for i, order := range [][]db.Service{
		{good.service, otherBad.service, bad.service},
		{bad.service, good.service, otherBad.service},
		{otherBad.service, bad.service, good.service},
	} {
		if _, err := s.cfg.DB.MutateData(func(data *db.Data) error {
			data.Services = make(map[string]*db.Service, len(order))
			for j := range order {
				service := order[j]
				data.Services[service.Name] = &service
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		dv, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		plan, err := s.planTailscaleResolverIsolationFleet(context.Background(), dv)
		if err == nil || !strings.Contains(err.Error(), "omega") || !strings.Contains(err.Error(), "zeta") {
			t.Fatalf("permutation %d error = %v, want aggregate omega and zeta validation", i, err)
		}
		gotPlan := fmt.Sprintf("%#v", plan)
		gotErr := err.Error()
		if i == 0 {
			wantPlan, wantErr = gotPlan, gotErr
			continue
		}
		if gotPlan != wantPlan || gotErr != wantErr {
			t.Fatalf("permutation %d plan/error diverged:\nplan %s\nerror %s\nwant plan %s\nwant error %s", i, gotPlan, gotErr, wantPlan, wantErr)
		}
	}
	if writes != 0 || stops != 0 || reloads != 0 || restarts != 0 || verifies != 0 {
		t.Fatalf("mutation dependencies = writes:%d stops:%d reloads:%d restarts:%d verifies:%d, want all zero", writes, stops, reloads, restarts, verifies)
	}
}

func TestPlanTailscaleResolverIsolationFleetOrderIsDeterministic(t *testing.T) {
	s := newTestServer(t)
	useTestSystemdSystemDir(t)
	stubTailscaleResolverActive(t, nil)
	for _, name := range []string{"zulu", "alpha", "middle"} {
		addTailscaleResolverPlanService(t, s, name, tailscaleResolverGenerationHistorical, "", nil)
	}
	dv, err := s.getDB()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.planTailscaleResolverIsolationFleet(context.Background(), dv)
	if err != nil {
		t.Fatalf("planTailscaleResolverIsolationFleet: %v", err)
	}
	var got []string
	for _, service := range plan.Services {
		got = append(got, service.ServiceName)
		var paths []string
		for _, file := range service.Files {
			paths = append(paths, file.Path)
		}
		if !sort.StringsAreSorted(paths) {
			t.Fatalf("file paths for %q are not sorted: %q", service.ServiceName, paths)
		}
	}
	if diff := cmp.Diff([]string{"alpha", "middle", "zulu"}, got); diff != "" {
		t.Fatalf("service order (-want +got):\n%s", diff)
	}
}

func TestPlanTailscaleResolverIsolationFleetRejectsIncompletePersistedCandidate(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
	delete(fixture.service.Artifacts, db.ArtifactTSService)
	replaceTailscaleResolverPlanService(t, fixture.server, fixture.service)

	assertTailscaleResolverPlanRejected(t, fixture, "missing generation 7 artifact")
}

func TestTailscaleResolverEffectiveDropInsFailClosed(t *testing.T) {
	t.Run("initial planning rejects drop-ins", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		const privatePath = "/private/operator/override.conf"
		oldDropIns := tailscaleResolverUnitDropInPaths
		tailscaleResolverUnitDropInPaths = func(context.Context, string) ([]string, error) {
			return []string{privatePath}, nil
		}
		t.Cleanup(func() { tailscaleResolverUnitDropInPaths = oldDropIns })

		dv, err := fixture.server.getDB()
		if err != nil {
			t.Fatal(err)
		}
		plan, err := fixture.server.planTailscaleResolverIsolationFleet(context.Background(), dv)
		if err == nil || !strings.Contains(err.Error(), "effective systemd drop-ins") {
			t.Fatalf("plan = %#v, error = %v; want effective drop-in rejection", plan, err)
		}
		if strings.Contains(err.Error(), privatePath) {
			t.Fatalf("drop-in rejection leaked effective path: %v", err)
		}
	})

	t.Run("initial planning rejects lookup failure", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		oldDropIns := tailscaleResolverUnitDropInPaths
		tailscaleResolverUnitDropInPaths = func(context.Context, string) ([]string, error) {
			return nil, errors.New("sentinel lookup failure")
		}
		t.Cleanup(func() { tailscaleResolverUnitDropInPaths = oldDropIns })

		assertTailscaleResolverPlanRejected(t, fixture, "inspect effective systemd drop-ins")
	})

	t.Run("initial planning accepts clean empty result", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		oldDropIns := tailscaleResolverUnitDropInPaths
		tailscaleResolverUnitDropInPaths = func(context.Context, string) ([]string, error) {
			return nil, nil
		}
		t.Cleanup(func() { tailscaleResolverUnitDropInPaths = oldDropIns })

		plan := planTailscaleResolverFleet(t, fixture)
		if len(plan.Services) != 1 {
			t.Fatalf("planned services = %d, want 1", len(plan.Services))
		}
	})

	t.Run("fleet revalidation rejects newly added drop-in", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		oldDropIns := tailscaleResolverUnitDropInPaths
		calls := 0
		tailscaleResolverUnitDropInPaths = func(_ context.Context, unit string) ([]string, error) {
			if unit != "yeet-api-ts.service" {
				t.Fatalf("drop-in unit = %q, want yeet-api-ts.service", unit)
			}
			calls++
			if calls == 1 {
				return nil, nil
			}
			return []string{"/private/operator/late.conf"}, nil
		}
		t.Cleanup(func() { tailscaleResolverUnitDropInPaths = oldDropIns })

		plan := planTailscaleResolverFleet(t, fixture)
		assertTailscaleResolverRevalidationRejected(
			t,
			fixture.server,
			plan,
			"effective systemd drop-ins",
		)
	})

	t.Run("readiness rejects newly added drop-in", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		guardTailscaleResolverFixture(t, fixture)
		oldDropIns := tailscaleResolverUnitDropInPaths
		calls := 0
		tailscaleResolverUnitDropInPaths = func(context.Context, string) ([]string, error) {
			calls++
			if calls == 1 {
				return nil, nil
			}
			return []string{"/private/operator/late-readiness.conf"}, nil
		}
		t.Cleanup(func() { tailscaleResolverUnitDropInPaths = oldDropIns })

		err := fixture.server.checkTailscaleResolverReady(context.Background(), fixture.service)
		if err == nil || !strings.Contains(err.Error(), "effective systemd drop-ins") {
			t.Fatalf("readiness error = %v, want newly added drop-in rejection", err)
		}
		if calls != 2 {
			t.Fatalf("readiness drop-in inspections = %d, want initial and immediate checks", calls)
		}
	})
}

func TestInspectTailscaleResolverUnitDropInPaths(t *testing.T) {
	const script = `#!/bin/sh
printf '%s\n' "$@" > "$TS_DROPIN_LOG"
case "$TS_DROPIN_MODE" in
empty)
	exit 0
	;;
nonempty)
	printf '/etc/systemd/system/yeet-api-ts.service.d/override.conf /run/systemd/system/yeet-api-ts.service.d/runtime.conf /usr/lib/systemd/system/yeet-api-ts.service.d/vendor.conf\n'
	;;
multiline)
	printf '/etc/systemd/system/yeet-api-ts.service.d/override.conf\n/run/systemd/system/yeet-api-ts.service.d/runtime.conf\n'
	;;
nul)
	printf '/etc/systemd/system/yeet-api-ts.service.d/override.conf\000/run/systemd/system/yeet-api-ts.service.d/runtime.conf'
	;;
failure)
	printf 'private stderr must stay hidden\n' >&2
	exit 19
	;;
block)
	: > "$TS_DROPIN_STARTED"
	while :; do :; done
	;;
esac
`
	const unit = "yeet-api-ts.service"
	wantArgs := []string{"show", "--property=DropInPaths", "--value", unit}
	for _, test := range []struct {
		name       string
		mode       string
		cancel     bool
		afterStart bool
		want       []string
		wantErr    string
		wantCalled bool
	}{
		{name: "empty", mode: "empty", wantCalled: true},
		{
			name: "nonempty",
			mode: "nonempty",
			want: []string{
				"/etc/systemd/system/yeet-api-ts.service.d/override.conf",
				"/run/systemd/system/yeet-api-ts.service.d/runtime.conf",
				"/usr/lib/systemd/system/yeet-api-ts.service.d/vendor.conf",
			},
			wantCalled: true,
		},
		{name: "malformed multiline", mode: "multiline", wantErr: "malformed", wantCalled: true},
		{name: "malformed NUL", mode: "nul", wantErr: "malformed", wantCalled: true},
		{name: "command failure", mode: "failure", wantErr: "exit status 19", wantCalled: true},
		{name: "canceled context", mode: "empty", cancel: true, wantErr: "context canceled"},
		{
			name:       "canceled after command starts",
			mode:       "block",
			afterStart: true,
			wantErr:    "context canceled",
			wantCalled: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "argv.log")
			startedPath := filepath.Join(dir, "started")
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake systemctl: %v", err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TS_DROPIN_LOG", logPath)
			t.Setenv("TS_DROPIN_MODE", test.mode)
			t.Setenv("TS_DROPIN_STARTED", startedPath)

			ctx := context.Background()
			if test.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			var got []string
			var err error
			if test.afterStart {
				startedCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() {
					got, err = inspectTailscaleResolverUnitDropInPaths(startedCtx, unit)
					close(done)
				}()
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					if _, statErr := os.Stat(startedPath); statErr == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if _, statErr := os.Stat(startedPath); statErr != nil {
					cancel()
					t.Fatalf("fake systemctl did not signal start: %v", statErr)
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("inspect DropInPaths did not return after cancellation")
				}
			} else {
				got, err = inspectTailscaleResolverUnitDropInPaths(ctx, unit)
			}
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("inspect DropInPaths error = %v, want %q", err, test.wantErr)
				}
				if (test.cancel || test.afterStart) && !errors.Is(err, context.Canceled) {
					t.Fatalf("inspect canceled DropInPaths error = %v, want context.Canceled", err)
				}
				if strings.Contains(err.Error(), "private stderr") {
					t.Fatalf("inspect DropInPaths leaked command stderr: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("inspect DropInPaths: %v", err)
				}
				if diff := cmp.Diff(test.want, got); diff != "" {
					t.Fatalf("DropInPaths (-want +got):\n%s", diff)
				}
			}

			rawArgs, readErr := os.ReadFile(logPath)
			if !test.wantCalled {
				if readErr == nil && len(bytes.TrimSpace(rawArgs)) != 0 {
					t.Fatalf("canceled DropInPaths command ran with argv %q", rawArgs)
				}
				if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatalf("read canceled DropInPaths argv: %v", readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatalf("read DropInPaths argv: %v", readErr)
			}
			gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
			if diff := cmp.Diff(wantArgs, gotArgs); diff != "" {
				t.Fatalf("systemctl DropInPaths argv (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTailscaleResolverFleetPlanRevalidateRejectsCandidateSetChanges(t *testing.T) {
	t.Run("added candidate", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		plan := planTailscaleResolverFleet(t, fixture)
		addTailscaleResolverPlanService(
			t,
			fixture.server,
			"beta",
			tailscaleResolverGenerationCurrent,
			"",
			nil,
		)

		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "candidate set changed")
	})

	t.Run("removed candidate", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		plan := planTailscaleResolverFleet(t, fixture)
		if _, err := fixture.server.cfg.DB.MutateData(func(data *db.Data) error {
			delete(data.Services, fixture.service.Name)
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "candidate set changed")
	})

	t.Run("planned order", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "alpha", tailscaleResolverGenerationCurrent, "")
		addTailscaleResolverPlanService(
			t,
			fixture.server,
			"beta",
			tailscaleResolverGenerationCurrent,
			"",
			nil,
		)
		plan := planTailscaleResolverFleet(t, fixture)
		plan.Services[0], plan.Services[1] = plan.Services[1], plan.Services[0]

		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "candidate set changed")
	})
}

func TestPlanTailscaleResolverIsolationFleetRequiresDistinctProofIdentities(t *testing.T) {
	t.Run("canonical and installed units", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		fixture.service.Artifacts[db.ArtifactTSService].Refs[db.Gen(fixture.service.Generation)] = fixture.installed
		replaceTailscaleResolverPlanService(t, fixture.server, fixture.service)

		assertTailscaleResolverPlanRejected(t, fixture, "paths must be distinct")
	})

	for _, artifact := range []db.ArtifactName{
		db.ArtifactTSBinary,
		db.ArtifactTSEnv,
		db.ArtifactTSConfig,
	} {
		t.Run(string(artifact), func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
			tuple := fixtureTuple(fixture.service, tailscaleResolverGenerationCurrent)
			runtimePath := map[db.ArtifactName]string{
				db.ArtifactTSBinary: tuple.daemon,
				db.ArtifactTSEnv:    tuple.environmentFile,
				db.ArtifactTSConfig: tuple.configFile,
			}[artifact]
			fixture.service.Artifacts[artifact].Refs[db.Gen(fixture.service.Generation)] = runtimePath
			replaceTailscaleResolverPlanService(t, fixture.server, fixture.service)

			assertTailscaleResolverPlanRejected(t, fixture, "generation artifact must differ from selected runtime")
		})
	}
}

func TestPlanTailscaleResolverIsolationFleetRejectsWrongConfiguredGuardRunner(t *testing.T) {
	for _, runner := range []string{
		"/srv/catch/run/not-catch",
		"/srv/catch/run/catch-0.10.8",
		"/srv/catch/run/catch.install-123",
		"/opt/other/catch",
	} {
		t.Run(filepath.Base(runner), func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
			tuple := fixtureTuple(fixture.service, tailscaleResolverGenerationCurrent)
			source := "/etc/netns/yeet-api-ns/resolv.conf"
			guarded := strings.Replace(
				fixture.unit,
				"ExecStart="+tuple.daemon,
				"ExecStart="+runner+" tailscale-resolver-exec --source "+source+" -- "+tuple.daemon,
				1,
			)
			for _, path := range []string{fixture.canonical, fixture.installed} {
				if err := os.WriteFile(path, []byte(guarded), 0o640); err != nil {
					t.Fatal(err)
				}
			}

			assertTailscaleResolverPlanRejected(t, fixture, "guard runner")
		})
	}
}

func TestExpectedTailscaleResolverGenerationRejectsNonCanonicalRootAndInterface(t *testing.T) {
	t.Run("non-canonical service root", func(t *testing.T) {
		service := db.Service{
			Name:        "api",
			ServiceRoot: "/srv/services/../api",
			TSNet:       &db.TailscaleNetwork{Interface: "ts0"},
		}
		if generation, err := expectedTailscaleResolverGeneration(
			service,
			tailscaleResolverGenerationCurrent,
		); err == nil || !strings.Contains(err.Error(), "absolute clean path") {
			t.Fatalf("expectedTailscaleResolverGeneration = %#v, %v; want clean-root rejection", generation, err)
		}
	})

	for _, interfaceName := range []string{
		".",
		"..",
		"tap:ts0",
		"ts 0",
		"ts0/alias",
		"ts0\n",
		"interface-name-too-long",
	} {
		t.Run(interfaceName, func(t *testing.T) {
			service := db.Service{
				Name:        "api",
				ServiceRoot: "/srv/api",
				TSNet:       &db.TailscaleNetwork{Interface: interfaceName},
			}
			if generation, err := expectedTailscaleResolverGeneration(
				service,
				tailscaleResolverGenerationCurrent,
			); err == nil || !strings.Contains(err.Error(), "plain") {
				t.Fatalf("expectedTailscaleResolverGeneration = %#v, %v; want plain-interface rejection", generation, err)
			}
		})
	}
}

func TestTailscaleResolverFleetPlanRevalidateRejectsChangedDBOrFile(t *testing.T) {
	t.Run("database record", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		stubTailscaleResolverActive(t, nil)
		plan := planTailscaleResolverFleet(t, fixture)
		fixture.service.LatestGeneration++
		replaceTailscaleResolverPlanService(t, fixture.server, fixture.service)
		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "stale")
	})

	t.Run("unit file", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		stubTailscaleResolverActive(t, nil)
		plan := planTailscaleResolverFleet(t, fixture)
		if err := os.WriteFile(fixture.installed, append([]byte(fixture.unit), '\n'), 0o640); err != nil {
			t.Fatal(err)
		}
		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "stale")
	})

	t.Run("active state", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		active := map[string]bool{"yeet-api-ts.service": true}
		stubTailscaleResolverActive(t, active)
		plan := planTailscaleResolverFleet(t, fixture)
		active["yeet-api-ts.service"] = false
		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "stale")
	})

	t.Run("context", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		stubTailscaleResolverActive(t, nil)
		plan := planTailscaleResolverFleet(t, fixture)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := fixture.server.revalidateTailscaleResolverFleetPlan(ctx, plan); !errors.Is(err, context.Canceled) {
			t.Fatalf("revalidateTailscaleResolverFleetPlan error = %v, want context canceled", err)
		}
	})
}

func TestTailscaleResolverFleetPlanRevalidateRejectsChangedCatchRunner(t *testing.T) {
	t.Run("runner proof", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		plan := planTailscaleResolverFleet(t, fixture)
		if err := os.WriteFile(fixture.server.catchRunnerPath(), []byte("changed\n"), 0o750); err != nil {
			t.Fatal(err)
		}

		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "Catch runner")
	})

	t.Run("configured path", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
		plan := planTailscaleResolverFleet(t, fixture)
		catchRoot := filepath.Join(fixture.server.cfg.ServicesRoot, "relocated-catch")
		catchRunner := filepath.Join(catchRoot, "run", "catch")
		if err := os.MkdirAll(filepath.Dir(catchRunner), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(catchRunner, []byte("catch\n"), 0o750); err != nil {
			t.Fatal(err)
		}
		addTestServices(t, fixture.server, db.Service{
			Name:        CatchService,
			ServiceRoot: catchRoot,
			Artifacts:   make(db.ArtifactStore),
		})

		assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "Catch runner path changed")
	})
}

func TestPlanTailscaleResolverIsolationFleetRejectsActiveStateQueryErrorWithoutMutation(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
	oldActive := tailscaleResolverUnitActive
	tailscaleResolverUnitActive = func(string) (bool, error) {
		return false, errors.New("sentinel active query failure")
	}
	t.Cleanup(func() { tailscaleResolverUnitActive = oldActive })

	var mutations int
	oldWrite := writeTailscaleResolverUnitFile
	oldSystemctl := catchSystemctl
	writeTailscaleResolverUnitFile = func(string, []byte, os.FileMode) error {
		mutations++
		return nil
	}
	catchSystemctl = func(...string) error {
		mutations++
		return nil
	}
	t.Cleanup(func() {
		writeTailscaleResolverUnitFile = oldWrite
		catchSystemctl = oldSystemctl
	})

	assertTailscaleResolverPlanRejected(t, fixture, "active query failure")
	if mutations != 0 {
		t.Fatalf("mutation calls = %d, want zero", mutations)
	}
}

func TestTailscaleResolverFleetPlanRevalidateRejectsActiveStateQueryError(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(t, "api", tailscaleResolverGenerationCurrent, "")
	plan := planTailscaleResolverFleet(t, fixture)
	oldActive := tailscaleResolverUnitActive
	tailscaleResolverUnitActive = func(string) (bool, error) {
		return false, errors.New("sentinel active query failure")
	}
	t.Cleanup(func() { tailscaleResolverUnitActive = oldActive })

	assertTailscaleResolverRevalidationRejected(t, fixture.server, plan, "active query failure")
}

func newTailscaleResolverPlanFixture(
	t *testing.T,
	name string,
	layout tailscaleResolverGenerationLayout,
	bind string,
) tailscaleResolverPlanFixture {
	t.Helper()
	s := newTestServer(t)
	useTestSystemdSystemDir(t)
	stubTailscaleResolverActive(t, nil)
	return addTailscaleResolverPlanService(t, s, name, layout, bind, nil)
}

func newTailscaleResolverPlanFixtureWithMutation(
	t *testing.T,
	name string,
	layout tailscaleResolverGenerationLayout,
	mutate func(*tailscaleResolverTuple, string),
) tailscaleResolverPlanFixture {
	t.Helper()
	s := newTestServer(t)
	useTestSystemdSystemDir(t)
	stubTailscaleResolverActive(t, nil)
	return addTailscaleResolverPlanService(t, s, name, layout, "", mutate)
}

func addTailscaleResolverPlanService(
	t *testing.T,
	s *Server,
	name string,
	layout tailscaleResolverGenerationLayout,
	bind string,
	mutate func(*tailscaleResolverTuple, string),
) tailscaleResolverPlanFixture {
	t.Helper()
	root := filepath.Join(s.cfg.ServicesRoot, name)
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tailscale"), 0o755); err != nil {
		t.Fatal(err)
	}

	service := db.Service{
		Name:        name,
		ServiceType: db.ServiceTypeDockerCompose,
		ServiceRoot: root,
		TSNet: &db.TailscaleNetwork{
			Interface: "ts0",
			Version:   tailscaleResolverFixtureDaemonVersion,
		},
		Generation:       7,
		LatestGeneration: 7,
		Artifacts:        make(db.ArtifactStore),
	}
	tuple := fixtureTuple(service, layout)
	if mutate != nil {
		mutate(&tuple, root)
	}
	runtimeFiles := map[string]string{
		tuple.daemon:          "tailscaled\n",
		tuple.environmentFile: "TS_LOGS_DIR=" + filepath.Join(root, "tailscale") + "\n",
		tuple.configFile:      `{"acceptDNS":false}`,
	}
	for path, raw := range runtimeFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	generationBinary := filepath.Join(
		s.cfg.RootDir,
		"tsd",
		"tailscaled-"+tailscaleResolverFixtureDaemonVersion,
	)
	generationEnv := filepath.Join(
		root,
		"tailscale",
		"tailscaled-"+tailscaleResolverFixtureArtifactVersion+".env",
	)
	generationConfig := filepath.Join(
		root,
		"tailscale",
		"tailscaled-"+tailscaleResolverFixtureArtifactVersion+".json",
	)
	for path, raw := range map[string]string{
		generationBinary: runtimeFiles[tuple.daemon],
		generationEnv:    runtimeFiles[tuple.environmentFile],
		generationConfig: runtimeFiles[tuple.configFile],
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	canonical := filepath.Join(
		root,
		"bin",
		"yeet-"+name+"-ts-"+tailscaleResolverFixtureArtifactVersion+".service",
	)
	installed := tailscaleSidecarInstalledUnitPath(name)
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := tailscaleResolverFixtureUnit(name, tuple, bind)
	if err := os.WriteFile(canonical, []byte(unit), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte(unit), 0o640); err != nil {
		t.Fatal(err)
	}
	catchRunner := s.catchRunnerPath()
	if err := os.MkdirAll(filepath.Dir(catchRunner), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catchRunner, []byte("catch\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	service.Artifacts = db.ArtifactStore{
		db.ArtifactTSService: {Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): canonical}},
		db.ArtifactTSBinary:  {Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): generationBinary}},
		db.ArtifactTSEnv:     {Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): generationEnv}},
		db.ArtifactTSConfig:  {Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): generationConfig}},
	}
	addTestServices(t, s, service)
	return tailscaleResolverPlanFixture{server: s, service: service, canonical: canonical, installed: installed, unit: unit}
}

func fixtureTuple(service db.Service, layout tailscaleResolverGenerationLayout) tailscaleResolverTuple {
	root := service.ServiceRoot
	tuple := tailscaleResolverTuple{
		socketFile:    filepath.Join(root, "run", "tailscaled.sock"),
		workingDir:    filepath.Join(root, "tailscale"),
		interfaceName: service.TSNet.Interface,
	}
	switch layout {
	case tailscaleResolverGenerationHistorical:
		tuple.daemon = filepath.Join(root, "run", "tailscaled")
		tuple.environmentFile = filepath.Join(root, "run", "tailscaled.env")
		tuple.configFile = filepath.Join(root, "run", "tailscaled.json")
	case tailscaleResolverGenerationCurrent:
		tuple.daemon = filepath.Join(root, "bin", "tailscaled")
		tuple.environmentFile = filepath.Join(root, "env", "tailscaled.env")
		tuple.configFile = filepath.Join(root, "env", "tailscaled.json")
	default:
		panic("unsupported fixture layout")
	}
	tuple.args = []string{
		"--statedir=.",
		"--socket=" + tuple.socketFile,
		"--config=" + tuple.configFile,
		"--tun=" + tuple.interfaceName,
	}
	return tuple
}

func fixtureGeneration(service db.Service, layout tailscaleResolverGenerationLayout) tailscaleResolverGeneration {
	tuple := fixtureTuple(service, layout)
	return tailscaleResolverGeneration{
		Layout:           layout,
		Daemon:           tuple.daemon,
		EnvironmentFile:  tuple.environmentFile,
		ConfigFile:       tuple.configFile,
		SocketFile:       tuple.socketFile,
		WorkingDirectory: tuple.workingDir,
		Interface:        tuple.interfaceName,
		Args:             append([]string(nil), tuple.args...),
	}
}

func tailscaleResolverFixtureUnit(name string, tuple tailscaleResolverTuple, bind string) string {
	var mount string
	if bind != "" {
		mount = bind + "\n"
	}
	return "[Unit]\nAfter=yeet-" + name + "-ns.service\n\n[Service]\n" +
		"WorkingDirectory=" + tuple.workingDir + "\n" +
		"EnvironmentFile=" + tuple.environmentFile + "\n" +
		"ExecStart=" + tuple.daemon + " " + strings.Join(tuple.args, " ") + "\n" +
		"NetworkNamespacePath=/var/run/netns/yeet-" + name + "-ns\n" +
		mount +
		"\n[Install]\nWantedBy=multi-user.target\n"
}

func planTailscaleResolverFleet(t *testing.T, fixture tailscaleResolverPlanFixture) tailscaleResolverFleetPlan {
	t.Helper()
	dv, err := fixture.server.getDB()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.server.planTailscaleResolverIsolationFleet(context.Background(), dv)
	if err != nil {
		t.Fatalf("planTailscaleResolverIsolationFleet: %v", err)
	}
	return plan
}

func assertTailscaleResolverPlanRejected(t *testing.T, fixture tailscaleResolverPlanFixture, want string) {
	t.Helper()
	dv, err := fixture.server.getDB()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.server.planTailscaleResolverIsolationFleet(context.Background(), dv)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("planTailscaleResolverIsolationFleet = %#v, %v; want error containing %q", plan, err, want)
	}
	if len(plan.Services) != 0 {
		t.Fatalf("rejected plan retained %d services", len(plan.Services))
	}
}

func assertTailscaleResolverPlannedFiles(
	t *testing.T,
	fixture tailscaleResolverPlanFixture,
	servicePlan tailscaleResolverServicePlan,
	want tailscaleResolverGeneration,
) {
	t.Helper()
	if len(servicePlan.Files) != 2 {
		t.Fatalf("planned files = %d, want 2", len(servicePlan.Files))
	}
	for _, file := range servicePlan.Files {
		if string(file.Original) != fixture.unit {
			t.Fatalf("original %s diverged from fixture", file.Path)
		}
		next := string(file.Next)
		parsed, err := parseTailscaleResolverUnit(next)
		if err != nil {
			t.Fatalf("parse planned unit %s: %v", file.Path, err)
		}
		if parsed.daemon != want.Daemon ||
			parsed.environmentFile != want.EnvironmentFile ||
			parsed.workingDirectory != want.WorkingDirectory ||
			!reflect.DeepEqual(parsed.args, want.Args) {
			t.Fatalf("planned tuple = %#v, want %#v", parsed, want)
		}
		for _, directive := range []string{"PrivateMounts=yes"} {
			if !systemdUnitHasDirective(next, directive) {
				t.Fatalf("planned unit %s missing %q:\n%s", file.Path, directive, next)
			}
		}
		if strings.Contains(next, "BindPaths=") || strings.Contains(next, "BindReadOnlyPaths=") {
			t.Fatalf("planned unit %s retains replaceable resolver bind:\n%s", file.Path, next)
		}
	}
}

func replaceTailscaleResolverPlanService(t *testing.T, s *Server, service db.Service) {
	t.Helper()
	if _, _, err := s.cfg.DB.MutateService(service.Name, func(_ *db.Data, stored *db.Service) error {
		*stored = service
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func retargetTailscaleResolverArtifact(
	t *testing.T,
	fixture *tailscaleResolverPlanFixture,
	artifact db.ArtifactName,
	path string,
) {
	t.Helper()
	source, ok := fixture.service.Artifacts.Gen(artifact, fixture.service.Generation)
	if !ok {
		t.Fatalf("fixture missing %s", artifact)
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatal(err)
	}
	fixture.service.Artifacts[artifact].Refs[db.Gen(fixture.service.Generation)] = path
	replaceTailscaleResolverPlanService(t, fixture.server, fixture.service)
}

func stubTailscaleResolverActive(t *testing.T, active map[string]bool) {
	t.Helper()
	oldLegacy := catchSystemdUnitActive
	oldInspect := tailscaleResolverUnitActive
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	tailscaleResolverUnitActive = func(unit string) (bool, error) { return active[unit], nil }
	t.Cleanup(func() {
		catchSystemdUnitActive = oldLegacy
		tailscaleResolverUnitActive = oldInspect
	})
}

func assertTailscaleResolverRevalidationRejected(
	t *testing.T,
	s *Server,
	plan tailscaleResolverFleetPlan,
	want string,
) {
	t.Helper()
	err := s.revalidateTailscaleResolverFleetPlan(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("revalidateTailscaleResolverFleetPlan error = %v, want %q", err, want)
	}
}
