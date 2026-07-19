// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestResolveVMRuntimeUpgradeTargetUsesEffectiveChannelOnce(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, candidate := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Configured: vmRuntimeCommandArtifact(stable, "official"),
	})
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.VMHost = &db.VMHostConfig{RuntimeChannel: cli.VMRuntimeChannelCandidate}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fetches, ensures := 0, 0
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) {
			fetches++
			return catalog, nil
		},
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			ensures++
			if ref != candidate {
				t.Fatalf("ensured ref = %#v, want candidate %#v", ref, candidate)
			}
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
	}
	got, err := server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fetches != 1 || ensures != 1 {
		t.Fatalf("fetches/ensures = %d/%d, want 1/1", fetches, ensures)
	}
	if got.Selection != vmRuntimeTargetSelectionChannel || got.Channel != cli.VMRuntimeChannelCandidate || !got.ChannelFromPolicy || got.CatalogRef == nil || *got.CatalogRef != candidate {
		t.Fatalf("resolved target = %#v", got)
	}
}

func TestResolveVMRuntimeUpgradeTargetBindsVersionToSelectedChannel(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, candidate := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(stable, "official")})
	ensured := ""
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			ensured = ref.RuntimeID
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
	}

	got, err := server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", "1.17.0", cli.VMRuntimeChannelCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selection != vmRuntimeTargetSelectionUpstreamVersion || got.Channel != cli.VMRuntimeChannelCandidate || got.ChannelFromPolicy || ensured != candidate.RuntimeID {
		t.Fatalf("resolved target = %#v, ensured %q", got, ensured)
	}
	_, err = server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", "v1.17.0", cli.VMRuntimeChannelStable)
	if err == nil || !strings.Contains(err.Error(), "not promoted on the stable channel") {
		t.Fatalf("channel mismatch error = %v", err)
	}
}

func TestResolveVMRuntimeUpgradeTargetExplicitOfficialIDCanCrossSourceFamily(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, candidate := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Configured: vmRuntimeCommandArtifact(stable, "custom-legacy"),
	})
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
	}

	got, err := server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", candidate.RuntimeID, cli.VMRuntimeChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selection != vmRuntimeTargetSelectionOfficialID || got.Channel != "" || got.Artifact.ID != candidate.RuntimeID {
		t.Fatalf("resolved target = %#v", got)
	}
}

func TestResolveVMRuntimeUpgradeTargetNonOfficialRequiresExactTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		channel string
	}{
		{name: "implicit"},
		{name: "channel only", channel: cli.VMRuntimeChannelCandidate},
		{name: "upstream version", target: "v1.17.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			_, stable, _ := vmRuntimeTargetTestCatalog()
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
				Configured: vmRuntimeCommandArtifact(stable, "official-legacy"),
			})
			server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
				fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) {
					t.Fatal("catalog fetched for rejected non-official implicit selection")
					return vmRuntimeCatalog{}, nil
				},
			}
			_, err := server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", tt.target, tt.channel)
			if err == nil || !strings.Contains(err.Error(), "non-official runtime source") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestResolveVMRuntimeUpgradeTargetUsesDurableLocalAliasOffline(t *testing.T) {
	server := newTestServer(t)
	fixture := newVMRuntimeImportFixture(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-runtimes")
	want, err := importVMRuntime(context.Background(), cacheRoot, "lab", bytes.NewReader(fixture.archive(t)))
	if err != nil {
		t.Fatal(err)
	}
	_, stable, _ := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Configured: vmRuntimeCommandArtifact(stable, "custom-legacy"),
	})
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) {
			t.Fatal("catalog fetched for local alias")
			return vmRuntimeCatalog{}, nil
		},
		ensureRuntime: func(context.Context, vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			t.Fatal("official cache ensure called for local alias")
			return db.VMRuntimeArtifactConfig{}, nil
		},
	}

	got, err := server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", "local:lab", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selection != vmRuntimeTargetSelectionLocalAlias || got.LocalAlias != "lab" || got.CatalogRef != nil || got.Artifact != want {
		t.Fatalf("resolved local target = %#v, want artifact %#v", got, want)
	}
	_, err = server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", "local:lab", cli.VMRuntimeChannelCandidate)
	if err == nil || !strings.Contains(err.Error(), "--channel cannot be used") {
		t.Fatalf("local channel error = %v", err)
	}
}

func TestResolveVMRuntimeUpgradeTargetRejectsRevokedBeforeEnsure(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, candidate := vmRuntimeTargetTestCatalog()
	catalog.Architectures["amd64"] = vmRuntimeCatalogArchitecture{
		Runtimes: []vmRuntimeCatalogRef{stable, candidate},
		Channels: catalog.Architectures["amd64"].Channels,
	}
	architecture := catalog.Architectures["amd64"]
	architecture.Runtimes[1].Support = "revoked"
	catalog.Architectures["amd64"] = architecture
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(stable, "official")})
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(context.Context, vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			t.Fatal("revoked runtime was ensured")
			return db.VMRuntimeArtifactConfig{}, nil
		},
	}
	_, err := server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", candidate.RuntimeID, "")
	if err == nil || !strings.Contains(err.Error(), "is revoked") {
		t.Fatalf("revocation error = %v", err)
	}
}

func TestResolveVMRuntimeUpgradeTargetPropagatesEnsureAndValidatesIdentity(t *testing.T) {
	tests := []struct {
		name   string
		ensure func(vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error)
		want   string
	}{
		{
			name: "ensure error",
			ensure: func(vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
				return db.VMRuntimeArtifactConfig{}, errors.New("cache unavailable")
			},
			want: "cache unavailable",
		},
		{
			name: "wrong identity",
			ensure: func(ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
				artifact := vmRuntimeCommandArtifact(ref, "official")
				artifact.ManifestSHA256 = strings.Repeat("f", 64)
				return artifact, nil
			},
			want: "exact catalog identity",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			catalog, stable, _ := vmRuntimeTargetTestCatalog()
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(stable, "official")})
			server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
				fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
				ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
					return tt.ensure(ref)
				},
			}
			_, err := server.resolveVMRuntimeUpgradeTarget(context.Background(), "devbox", "", "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func vmRuntimeTargetTestCatalog() (vmRuntimeCatalog, vmRuntimeCatalogRef, vmRuntimeCatalogRef) {
	catalog := validVMRuntimeCatalog()
	stable := catalog.Architectures["amd64"].Runtimes[0]
	candidate := stable
	candidate.RuntimeID = "firecracker-v1.17.0-yeet-v2"
	candidate.UpstreamVersion = "v1.17.0"
	candidate.ManifestSHA = strings.Repeat("4", 64)
	candidate.Support = "supported"
	catalog.Architectures["amd64"] = vmRuntimeCatalogArchitecture{
		Runtimes: []vmRuntimeCatalogRef{stable, candidate},
		Channels: map[string]*vmRuntimeCatalogIdentity{
			cli.VMRuntimeChannelStable:    {RuntimeID: stable.RuntimeID, ManifestSHA: stable.ManifestSHA},
			cli.VMRuntimeChannelCandidate: {RuntimeID: candidate.RuntimeID, ManifestSHA: candidate.ManifestSHA},
		},
	}
	return catalog, stable, candidate
}
