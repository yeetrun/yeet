// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestTailscaleResolverLiveReadOnlyPreflight(t *testing.T) {
	dataDir := os.Getenv("YEET_LIVE_DATA_DIR")
	servicesRoot := os.Getenv("YEET_LIVE_SERVICES_ROOT")
	if dataDir == "" || servicesRoot == "" {
		t.Skip("set YEET_LIVE_DATA_DIR and YEET_LIVE_SERVICES_ROOT to run the read-only live preflight")
	}

	snapshotDatabase := filepath.Join(t.TempDir(), "db.json")
	deps := tailscaleResolverLiveDependencies{
		readDatabase:  os.ReadFile,
		writeSnapshot: os.WriteFile,
		loadDatabase: func(database, root string) (*db.Store, db.DataView, error) {
			store := db.NewStore(database, root)
			dv, err := store.Get()
			return store, dv, err
		},
		planFleet: func(
			ctx context.Context,
			server *Server,
			dv *db.DataView,
		) (tailscaleResolverFleetPlan, error) {
			return server.planTailscaleResolverIsolationFleet(ctx, dv)
		},
	}
	var output bytes.Buffer
	plan, ok := runTailscaleResolverLiveReadOnlyPreflight(
		context.Background(),
		dataDir,
		servicesRoot,
		snapshotDatabase,
		&output,
		deps,
	)
	if !ok {
		t.Fatal(strings.TrimSpace(output.String()))
	}
	counts := map[tailscaleResolverGenerationLayout]int{}
	for _, service := range plan.Services {
		counts[service.Generation.Layout]++
	}
	t.Logf(
		"resolver preflight totals: services=%d historical=%d current=%d",
		len(plan.Services),
		counts[tailscaleResolverGenerationHistorical],
		counts[tailscaleResolverGenerationCurrent],
	)
}

type tailscaleResolverLiveDependencies struct {
	readDatabase  func(string) ([]byte, error)
	writeSnapshot func(string, []byte, os.FileMode) error
	loadDatabase  func(string, string) (*db.Store, db.DataView, error)
	planFleet     func(context.Context, *Server, *db.DataView) (tailscaleResolverFleetPlan, error)
}

func runTailscaleResolverLiveReadOnlyPreflight(
	ctx context.Context,
	dataDir string,
	servicesRoot string,
	snapshotDatabase string,
	output io.Writer,
	deps tailscaleResolverLiveDependencies,
) (tailscaleResolverFleetPlan, bool) {
	liveDatabase := filepath.Join(dataDir, "db.json")
	raw, err := deps.readDatabase(liveDatabase)
	if err != nil {
		_, _ = fmt.Fprintln(
			output,
			tailscaleResolverLiveFailure(tailscaleResolverLiveDatabaseReadFailure, err),
		)
		return tailscaleResolverFleetPlan{}, false
	}
	if err := deps.writeSnapshot(snapshotDatabase, raw, 0o600); err != nil {
		_, _ = fmt.Fprintln(
			output,
			tailscaleResolverLiveFailure(tailscaleResolverLiveDatabaseSnapshotFailure, err),
		)
		return tailscaleResolverFleetPlan{}, false
	}
	store, dv, err := deps.loadDatabase(snapshotDatabase, servicesRoot)
	if err != nil {
		_, _ = fmt.Fprintln(
			output,
			tailscaleResolverLiveFailure(tailscaleResolverLiveDatabaseLoadFailure, err),
		)
		return tailscaleResolverFleetPlan{}, false
	}
	server := &Server{cfg: Config{DB: store, RootDir: dataDir, ServicesRoot: servicesRoot}}
	plan, err := deps.planFleet(ctx, server, &dv)
	if err != nil {
		_, _ = fmt.Fprintln(
			output,
			tailscaleResolverLiveFailure(tailscaleResolverLiveValidationFailure, err),
		)
		return tailscaleResolverFleetPlan{}, false
	}
	return plan, true
}

func TestTailscaleResolverLiveFailureBranchesAreAggregateOnly(t *testing.T) {
	const (
		serviceSentinel = "sentinel-private-service"
		pathSentinel    = "/sentinel/private/path"
	)
	failure := errors.New("service " + serviceSentinel + " failed at " + pathSentinel)
	baseDependencies := func() tailscaleResolverLiveDependencies {
		return tailscaleResolverLiveDependencies{
			readDatabase: func(string) ([]byte, error) {
				return []byte("{}"), nil
			},
			writeSnapshot: func(string, []byte, os.FileMode) error {
				return nil
			},
			loadDatabase: func(string, string) (*db.Store, db.DataView, error) {
				return nil, (&db.Data{}).View(), nil
			},
			planFleet: func(context.Context, *Server, *db.DataView) (tailscaleResolverFleetPlan, error) {
				return tailscaleResolverFleetPlan{}, nil
			},
		}
	}
	for _, tt := range []struct {
		name string
		fail func(*tailscaleResolverLiveDependencies)
		want string
	}{
		{
			name: "database read",
			fail: func(deps *tailscaleResolverLiveDependencies) {
				deps.readDatabase = func(string) ([]byte, error) { return nil, failure }
			},
			want: "database_read_errors=1\n",
		},
		{
			name: "database snapshot",
			fail: func(deps *tailscaleResolverLiveDependencies) {
				deps.writeSnapshot = func(string, []byte, os.FileMode) error { return failure }
			},
			want: "database_snapshot_errors=1\n",
		},
		{
			name: "database load",
			fail: func(deps *tailscaleResolverLiveDependencies) {
				deps.loadDatabase = func(string, string) (*db.Store, db.DataView, error) {
					return nil, db.DataView{}, failure
				}
			},
			want: "database_load_errors=1\n",
		},
		{
			name: "preflight validation",
			fail: func(deps *tailscaleResolverLiveDependencies) {
				deps.planFleet = func(context.Context, *Server, *db.DataView) (tailscaleResolverFleetPlan, error) {
					return tailscaleResolverFleetPlan{}, failure
				}
			},
			want: "validation_errors=1\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps := baseDependencies()
			tt.fail(&deps)
			var output bytes.Buffer
			_, ok := runTailscaleResolverLiveReadOnlyPreflight(
				context.Background(),
				pathSentinel+"/data",
				pathSentinel+"/services",
				pathSentinel+"/snapshot/db.json",
				&output,
				deps,
			)
			if ok {
				t.Fatal("live preflight succeeded, want injected branch failure")
			}
			got := output.String()
			if got != tt.want {
				t.Fatalf("failure output = %q, want %q", got, tt.want)
			}
			for _, sentinel := range []string{serviceSentinel, pathSentinel} {
				if strings.Contains(got, sentinel) {
					t.Fatalf("failure output leaked sentinel %q: %q", sentinel, got)
				}
			}
		})
	}
}

type tailscaleResolverLiveFailureKind string

const (
	tailscaleResolverLiveDatabaseReadFailure     tailscaleResolverLiveFailureKind = "database_read"
	tailscaleResolverLiveDatabaseSnapshotFailure tailscaleResolverLiveFailureKind = "database_snapshot"
	tailscaleResolverLiveDatabaseLoadFailure     tailscaleResolverLiveFailureKind = "database_load"
	tailscaleResolverLiveValidationFailure       tailscaleResolverLiveFailureKind = "validation"
)

func tailscaleResolverLiveFailure(kind tailscaleResolverLiveFailureKind, _ error) string {
	return string(kind) + "_errors=1"
}
