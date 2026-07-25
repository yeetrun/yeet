// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestValidateTailscaleResolverJournalHeaderRejectsDuplicatePathAtConstantFileCount(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	header.Files[len(header.Files)-1] = header.Files[0]
	sort.Slice(header.Files, func(i, j int) bool { return header.Files[i].Path < header.Files[j].Path })

	err := validateTailscaleResolverJournalHeader(header)
	if err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate path validation error = %v, want unique-path rejection", err)
	}
}

func TestValidateTailscaleResolverJournalHeaderRejectsDuplicateService(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	header.Services[1] = header.Services[0]

	err := validateTailscaleResolverJournalHeader(header)
	if err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate service validation error = %v, want unique-service rejection", err)
	}
}

func TestValidateTailscaleResolverJournalHeaderRejectsWrongVersion(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	header.Version++

	err := validateTailscaleResolverJournalHeader(header)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("wrong version validation error = %v, want unsupported-version rejection", err)
	}
}

func TestValidateTailscaleResolverJournalHeaderRejectsArbitraryBinWithManagedBasename(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	replaceTailscaleResolverJournalFileForTest(
		t,
		&header,
		header.Files[0].Path,
		filepath.Join(t.TempDir(), "bin", filepath.Base(header.Files[0].Path)),
	)

	if err := validateTailscaleResolverJournalHeader(header); err == nil {
		t.Fatal("arbitrary bin path with managed basename passed journal validation")
	}
}

func TestValidateTailscaleResolverJournalHeaderRejectsTwoCanonicalPathsWithoutInstalledPath(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	service := fixture.plan.Services[0]
	installed := filepath.Join(systemdSystemDir, service.UnitName)
	replacement := filepath.Join(t.TempDir(), "bin", filepath.Base(service.Record.TSServiceArtifact))
	replaceTailscaleResolverJournalFileForTest(t, &header, installed, replacement)

	if err := validateTailscaleResolverJournalHeader(header); err == nil {
		t.Fatal("two canonical-looking paths without the installed unit passed journal validation")
	}
}

func TestLoadTailscaleResolverJournalValidatesPhaseSequenceDirectly(t *testing.T) {
	tests := []struct {
		name   string
		phases []string
	}{
		{name: "skipped", phases: []string{tailscaleResolverPhaseFilesWritten}},
		{name: "repeated", phases: []string{tailscaleResolverPhasePrepared, tailscaleResolverPhasePrepared}},
		{name: "unknown", phases: []string{"surprise"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTailscaleResolverFleetTransactionFixture(t)
			header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
			header.ID = "phase-" + tt.name
			path := writeRawTailscaleResolverJournal(t, fixture.server.cfg.RootDir, header, tt.phases, nil)
			if _, err := loadTailscaleResolverJournal(path); err == nil ||
				!strings.Contains(err.Error(), "illegal") {
				t.Fatalf("load phase sequence error = %v, want illegal-phase rejection", err)
			}
		})
	}
}

func TestLoadTailscaleResolverJournalRejectsTruncatedHeaderAndMalformedCompletePhase(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	dir, err := ensureTailscaleResolverJournalDir(fixture.server.cfg.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTailscaleResolverJournal(t, filepath.Join(dir, "truncated.jsonl"), []byte(`{"version":1`))
	if _, err := loadTailscaleResolverJournal(filepath.Join(dir, "truncated.jsonl")); err == nil {
		t.Fatal("truncated journal header loaded successfully")
	}

	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	header.ID = "malformed-phase"
	path := writeRawTailscaleResolverJournal(
		t,
		fixture.server.cfg.RootDir,
		header,
		nil,
		[]byte("{not-json}\n"),
	)
	if _, err := loadTailscaleResolverJournal(path); err == nil ||
		!strings.Contains(err.Error(), "phase") {
		t.Fatalf("malformed complete phase error = %v, want phase decode rejection", err)
	}
}

func TestLoadTailscaleResolverJournalIgnoresOnlyTruncatedFinalPhase(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	header.ID = "truncated-final-phase"
	path := writeRawTailscaleResolverJournal(
		t,
		fixture.server.cfg.RootDir,
		header,
		[]string{tailscaleResolverPhasePrepared},
		[]byte(`{"phase":"files-written"`),
	)

	contents, err := loadTailscaleResolverJournal(path)
	if err != nil {
		t.Fatalf("load journal with truncated final phase: %v", err)
	}
	if len(contents.Phases) != 1 || contents.Phases[0] != tailscaleResolverPhasePrepared {
		t.Fatalf("durable phases = %q, want only prepared", contents.Phases)
	}
}

func TestLoadTailscaleResolverJournalBoundsOversizedRecordAllocation(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	dir, err := ensureTailscaleResolverJournalDir(fixture.server.cfg.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "oversized.jsonl")
	writePrivateTailscaleResolverJournal(
		t,
		path,
		bytes.Repeat([]byte{'x'}, 8*tailscaleResolverJournalMaxLine),
	)
	result := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			_, _ = loadTailscaleResolverJournal(path)
		}
	})
	if got, limit := result.AllocedBytesPerOp(), int64(3*tailscaleResolverJournalMaxLine); got > limit {
		t.Fatalf("oversized journal allocated %d bytes/op, want <= %d", got, limit)
	}
}

func TestLoadTailscaleResolverJournalRejectsUnsafeMetadataDirectly(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "wrong mode", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong owner", mutate: func(t *testing.T, _ string) {
			t.Helper()
			tailscaleResolverJournalOwnerUID++
		}},
		{name: "hardlink", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Link(path, filepath.Join(t.TempDir(), "journal-link")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTailscaleResolverFleetTransactionFixture(t)
			header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
			header.ID = strings.ReplaceAll(tt.name, " ", "-")
			path := writeRawTailscaleResolverJournal(t, fixture.server.cfg.RootDir, header, nil, nil)
			tt.mutate(t, path)
			if _, err := loadTailscaleResolverJournal(path); err == nil {
				t.Fatalf("%s journal loaded successfully", tt.name)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		fixture := newTailscaleResolverFleetTransactionFixture(t)
		dir, err := ensureTailscaleResolverJournalDir(fixture.server.cfg.RootDir)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		writePrivateTailscaleResolverJournal(t, target, []byte("{}\n"))
		path := filepath.Join(dir, "symlink.jsonl")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTailscaleResolverJournal(path); err == nil {
			t.Fatal("symlink journal loaded successfully")
		}
	})

	t.Run("nonregular", func(t *testing.T) {
		fixture := newTailscaleResolverFleetTransactionFixture(t)
		dir, err := ensureTailscaleResolverJournalDir(fixture.server.cfg.RootDir)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "directory.jsonl")
		if err := os.Mkdir(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTailscaleResolverJournal(path); err == nil {
			t.Fatal("nonregular journal loaded successfully")
		}
	})
}

func TestTailscaleResolverJournalIDGenerationErrorPropagatesWithoutMutation(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	sentinel := errors.New("injected journal id generation failure")
	previous := tailscaleResolverNewJournalID
	tailscaleResolverNewJournalID = func() (string, error) { return "", sentinel }
	t.Cleanup(func() { tailscaleResolverNewJournalID = previous })
	writes := 0
	previousWrite := tailscaleResolverWriteManagedFile
	tailscaleResolverWriteManagedFile = func(
		root, relative string,
		expected serviceIdentityPathProof,
		content []byte,
	) (serviceIdentityPathProof, error) {
		writes++
		return previousWrite(root, relative, expected, content)
	}
	t.Cleanup(func() { tailscaleResolverWriteManagedFile = previousWrite })

	err := fixture.server.applyTailscaleResolverIsolationFleet(context.Background(), fixture.plan)
	if !errors.Is(err, sentinel) {
		t.Fatalf("apply error = %v, want exact ID generation error", err)
	}
	if writes != 0 {
		t.Fatalf("ID generation failure performed %d managed writes", writes)
	}
}

func replaceTailscaleResolverJournalFileForTest(
	t *testing.T,
	header *tailscaleResolverJournalHeader,
	oldPath, newPath string,
) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range header.Files {
		if header.Files[i].Path != oldPath {
			continue
		}
		if err := os.WriteFile(newPath, header.Files[i].Original, header.Files[i].OriginalProof.Mode.Perm()); err != nil {
			t.Fatal(err)
		}
		proof, err := captureServiceIdentityPathProof(newPath)
		if err != nil {
			t.Fatal(err)
		}
		header.Files[i].Path = newPath
		header.Files[i].OriginalProof = proof
		sort.Slice(header.Files, func(i, j int) bool { return header.Files[i].Path < header.Files[j].Path })
		return
	}
	t.Fatalf("journal file %s not found", oldPath)
}

func writeRawTailscaleResolverJournal(
	t *testing.T,
	root string,
	header tailscaleResolverJournalHeader,
	phases []string,
	tail []byte,
) string {
	t.Helper()
	dir, err := ensureTailscaleResolverJournalDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if err := json.NewEncoder(&raw).Encode(header); err != nil {
		t.Fatal(err)
	}
	for _, phase := range phases {
		if err := json.NewEncoder(&raw).Encode(tailscaleResolverJournalPhase{Phase: phase}); err != nil {
			t.Fatal(err)
		}
	}
	raw.Write(tail)
	path := filepath.Join(dir, header.ID+".jsonl")
	writePrivateTailscaleResolverJournal(t, path, raw.Bytes())
	return path
}

func mustNewTailscaleResolverJournalHeader(
	t *testing.T,
	plan tailscaleResolverFleetPlan,
) tailscaleResolverJournalHeader {
	t.Helper()
	header, err := newTailscaleResolverJournalHeader(plan)
	if err != nil {
		t.Fatalf("newTailscaleResolverJournalHeader: %v", err)
	}
	return header
}
