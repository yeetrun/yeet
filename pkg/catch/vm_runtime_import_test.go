// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVMRuntimeImportPublishesValidatedLocalRuntime(t *testing.T) {
	fixture := newVMRuntimeImportFixture(t)
	root := t.TempDir()
	artifact, err := importVMRuntime(context.Background(), root, "lab-v1", bytes.NewReader(fixture.archive(t)))
	if err != nil {
		t.Fatalf("importVMRuntime: %v", err)
	}
	if artifact.ID != fixture.manifest.RuntimeID || artifact.ManifestSHA256 != vmRuntimeSHA256Bytes(fixture.manifestRaw(t)) {
		t.Fatalf("artifact identity = %#v", artifact)
	}
	if artifact.Source != "local:lab-v1" {
		t.Fatalf("artifact source = %q, want local:lab-v1", artifact.Source)
	}
	wantDir := filepath.Join(root, fixture.manifest.Architecture, fixture.manifest.RuntimeID, artifact.ManifestSHA256)
	if filepath.Dir(artifact.Firecracker) != wantDir || filepath.Dir(artifact.Jailer) != wantDir {
		t.Fatalf("artifact paths = %q and %q, want parent %q", artifact.Firecracker, artifact.Jailer, wantDir)
	}
	assertVMRuntimeImportMode(t, wantDir, 0o755)
	assertVMRuntimeImportMode(t, filepath.Join(wantDir, vmRuntimeManifestFilename), 0o644)
	assertVMRuntimeImportMode(t, artifact.Firecracker, 0o755)
	assertVMRuntimeImportMode(t, artifact.Jailer, 0o755)
	aliasDir := filepath.Join(root, vmRuntimeLocalAliasDirname)
	aliasPath := vmRuntimeLocalAliasPath(aliasDir, "lab-v1")
	assertVMRuntimeImportMode(t, aliasDir, 0o700)
	assertVMRuntimeImportMode(t, aliasPath, 0o600)
	assertNoVMRuntimeImportStaging(t, root)
	resolved, err := resolveLocalVMRuntime(context.Background(), root, "lab-v1")
	if err != nil {
		t.Fatalf("resolveLocalVMRuntime after import: %v", err)
	}
	if resolved != artifact {
		t.Fatalf("resolved artifact = %#v, want %#v", resolved, artifact)
	}

	again, err := importVMRuntime(context.Background(), root, "lab-v1", bytes.NewReader(fixture.archive(t)))
	if err != nil {
		t.Fatalf("idempotent importVMRuntime: %v", err)
	}
	if again != artifact {
		t.Fatalf("idempotent artifact = %#v, want %#v", again, artifact)
	}
}

func TestVMRuntimeImportRejectsMalformedArchiveEntries(t *testing.T) {
	fixture := newVMRuntimeImportFixture(t)
	tests := []struct {
		name   string
		mutate func([]vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry
		want   string
	}{
		{
			name: "parent path",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				entries[1].name = "../firecracker"
				return entries
			},
			want: "unexpected entry",
		},
		{
			name: "symlink",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				entries[1].typeflag = tar.TypeSymlink
				entries[1].linkname = "jailer"
				entries[1].content = nil
				return entries
			},
			want: "must be a regular file",
		},
		{
			name: "extra",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				return append(entries, vmRuntimeImportTestEntry{name: "README", mode: 0o644, content: []byte("extra")})
			},
			want: "unexpected entry",
		},
		{
			name: "missing",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				return entries[:2]
			},
			want: "missing required entry",
		},
		{
			name: "duplicate",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				return append(entries, entries[1])
			},
			want: "duplicate entry",
		},
		{
			name: "permissions",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				entries[1].mode = 0o775
				return entries
			},
			want: "permissions",
		},
		{
			name: "archive ownership",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				entries[1].uid = 1000
				return entries
			},
			want: "owner must be 0:0",
		},
		{
			name: "oversized manifest",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				entries[0].declaredSize = maxVMRuntimeManifestBytes + 1
				return entries[:1]
			},
			want: "byte limit",
		},
		{
			name: "oversized binary",
			mutate: func(entries []vmRuntimeImportTestEntry) []vmRuntimeImportTestEntry {
				entries[1].declaredSize = maxVMRuntimeBinaryBytes + 1
				return entries[:2]
			},
			want: "byte limit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			archive := writeVMRuntimeImportTestTar(t, tt.mutate(fixture.entries(t)))
			_, err := importVMRuntime(context.Background(), root, "lab", bytes.NewReader(archive))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("importVMRuntime error = %v, want %q", err, tt.want)
			}
			assertNoVMRuntimeImportStaging(t, root)
		})
	}
}

func TestVMRuntimeImportRejectsDigestAndVersionFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*vmRuntimeImportFixture)
		want   string
	}{
		{
			name: "component digest",
			mutate: func(fixture *vmRuntimeImportFixture) {
				fixture.firecracker = append(fixture.firecracker, []byte("changed")...)
			},
			want: "firecracker digest mismatch",
		},
		{
			name: "version output",
			mutate: func(fixture *vmRuntimeImportFixture) {
				fixture.jailer = vmRuntimeTestExecutable("Jailer", "v1.15.0")
				fixture.manifest.Components.Jailer.SHA256 = vmRuntimeTestSHA256(fixture.jailer)
			},
			want: "version output",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMRuntimeImportFixture(t)
			tt.mutate(fixture)
			root := t.TempDir()
			_, err := importVMRuntime(context.Background(), root, "lab", bytes.NewReader(fixture.archive(t)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("importVMRuntime error = %v, want %q", err, tt.want)
			}
			assertNoVMRuntimeImportStaging(t, root)
			assertVMRuntimeImportNotPublished(t, root, fixture.manifest)
		})
	}
}

func TestVMRuntimeImportRejectsRuntimeIDDigestCollision(t *testing.T) {
	fixture := newVMRuntimeImportFixture(t)
	root := t.TempDir()
	first, err := importVMRuntime(context.Background(), root, "first", bytes.NewReader(fixture.archive(t)))
	if err != nil {
		t.Fatalf("first importVMRuntime: %v", err)
	}
	fixture.manifest.Provenance.WorkflowRun = "987654321"
	secondManifestSHA := vmRuntimeSHA256Bytes(fixture.manifestRaw(t))
	if secondManifestSHA == first.ManifestSHA256 {
		t.Fatal("collision fixture did not change the manifest digest")
	}
	_, err = importVMRuntime(context.Background(), root, "second", bytes.NewReader(fixture.archive(t)))
	if err == nil || !strings.Contains(err.Error(), "different manifest digest") {
		t.Fatalf("collision error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, fixture.manifest.Architecture, fixture.manifest.RuntimeID, secondManifestSHA)); !os.IsNotExist(statErr) {
		t.Fatalf("colliding target exists: %v", statErr)
	}
	assertNoVMRuntimeImportStaging(t, root)
}

func TestVMRuntimeImportRejectsLocalAliasIdentityCollision(t *testing.T) {
	firstFixture := newVMRuntimeImportFixture(t)
	root := t.TempDir()
	first, err := importVMRuntime(context.Background(), root, "lab", bytes.NewReader(firstFixture.archive(t)))
	if err != nil {
		t.Fatalf("first importVMRuntime: %v", err)
	}

	secondFixture := newVMRuntimeImportFixture(t)
	setVMRuntimeImportFixtureVersion(secondFixture, "v1.17.0")
	_, err = importVMRuntime(context.Background(), root, "lab", bytes.NewReader(secondFixture.archive(t)))
	if err == nil || !strings.Contains(err.Error(), "already resolves to a different runtime identity") {
		t.Fatalf("alias collision error = %v", err)
	}
	secondSHA := vmRuntimeSHA256Bytes(secondFixture.manifestRaw(t))
	secondPath := filepath.Join(root, "amd64", secondFixture.manifest.RuntimeID, secondSHA)
	if _, statErr := os.Lstat(secondPath); !os.IsNotExist(statErr) {
		t.Fatalf("alias collision published second runtime: %v", statErr)
	}
	resolved, err := resolveLocalVMRuntime(context.Background(), root, "lab")
	if err != nil {
		t.Fatalf("resolve first alias after collision: %v", err)
	}
	if resolved != first {
		t.Fatalf("alias after collision = %#v, want %#v", resolved, first)
	}
	assertNoVMRuntimeImportStaging(t, root)
}

func TestResolveLocalVMRuntimeRejectsUntrustedAliasRecords(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(testing.TB, string)
		want   string
	}{
		{
			name: "symlink",
			mutate: func(t testing.TB, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove alias: %v", err)
				}
				if err := os.Symlink(filepath.Join("..", "target"), path); err != nil {
					t.Fatalf("symlink alias: %v", err)
				}
			},
			want: "symbolic link",
		},
		{
			name: "mode",
			mutate: func(t testing.TB, path string) {
				t.Helper()
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatalf("chmod alias: %v", err)
				}
			},
			want: "permissions",
		},
		{
			name: "malformed JSON",
			mutate: func(t testing.TB, path string) {
				t.Helper()
				replaceVMRuntimeLocalAliasTestRaw(t, path, []byte(`{"schema_version":`))
			},
			want: "decode VM runtime local alias",
		},
		{
			name: "unknown JSON field",
			mutate: func(t testing.TB, path string) {
				t.Helper()
				replaceVMRuntimeLocalAliasTestRaw(t, path, []byte(`{"schema_version":1,"name":"lab","architecture":"amd64","runtime_id":"firecracker-v1.16.1-yeet-v1","manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source":"local:lab","unexpected":true}`))
			},
			want: "unknown field",
		},
		{
			name: "missing required field",
			mutate: func(t testing.TB, path string) {
				t.Helper()
				replaceVMRuntimeLocalAliasTestRaw(t, path, []byte(`{"schema_version":1}`))
			},
			want: "missing required field",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMRuntimeImportFixture(t)
			root := t.TempDir()
			if _, err := importVMRuntime(context.Background(), root, "lab", bytes.NewReader(fixture.archive(t))); err != nil {
				t.Fatalf("importVMRuntime: %v", err)
			}
			aliasPath := vmRuntimeLocalAliasPath(filepath.Join(root, vmRuntimeLocalAliasDirname), "lab")
			tt.mutate(t, aliasPath)
			_, err := resolveLocalVMRuntime(context.Background(), root, "lab")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolveLocalVMRuntime error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVMRuntimeImportRejectsInvalidNameAndCleansCanceledImport(t *testing.T) {
	fixture := newVMRuntimeImportFixture(t)
	for _, name := range []string{"", " local", "local\nname", string([]byte{0xff}), strings.Repeat("x", maxVMRuntimeImportNameBytes+1)} {
		_, err := importVMRuntime(context.Background(), t.TempDir(), name, bytes.NewReader(fixture.archive(t)))
		if err == nil || !strings.Contains(err.Error(), "invalid VM runtime import name") {
			t.Fatalf("name %q error = %v", name, err)
		}
	}

	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := importVMRuntime(ctx, root, "lab", bytes.NewReader(fixture.archive(t)))
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("canceled import error = %v", err)
	}
	assertNoVMRuntimeImportStaging(t, root)
}

func TestVMRuntimeImportRejectsUntrustedCacheRoot(t *testing.T) {
	fixture := newVMRuntimeImportFixture(t)
	t.Run("symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := t.TempDir()
		root := filepath.Join(parent, "cache")
		if err := os.Symlink(target, root); err != nil {
			t.Fatal(err)
		}
		if _, err := importVMRuntime(context.Background(), root, "lab", bytes.NewReader(fixture.archive(t))); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlink cache root error = %v", err)
		}
	})
	t.Run("writable by others", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := importVMRuntime(context.Background(), root, "lab", bytes.NewReader(fixture.archive(t))); err == nil || !strings.Contains(err.Error(), "group or other writable") {
			t.Fatalf("writable cache root error = %v", err)
		}
	})
}

func FuzzVMRuntimeImportArchive(f *testing.F) {
	fixture := &vmRuntimeImportFixture{
		manifest:    validVMRuntimeManifest(),
		firecracker: vmRuntimeTestExecutable("Firecracker", "v1.16.1"),
		jailer:      vmRuntimeTestExecutable("Jailer", "v1.16.1"),
	}
	fixture.manifest.Components.Firecracker.SHA256 = vmRuntimeTestSHA256(fixture.firecracker)
	fixture.manifest.Components.Jailer.SHA256 = vmRuntimeTestSHA256(fixture.jailer)
	f.Add(fixture.archive(f))
	f.Add([]byte("not a tar archive"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		staging := t.TempDir()
		_ = extractVMRuntimeImportArchive(context.Background(), staging, bytes.NewReader(raw))
	})
}

func FuzzParseVMRuntimeLocalAlias(f *testing.F) {
	alias := vmRuntimeLocalAlias{
		SchemaVersion: 1, Name: "lab", Architecture: "amd64",
		RuntimeID: "firecracker-v1.16.1-yeet-v1", ManifestSHA256: strings.Repeat("a", 64), Source: "local:lab",
	}
	f.Add(marshalVMRuntimeTestJSON(f, alias))
	f.Add([]byte(`{"schema_version":1}`))
	f.Add([]byte("not-json"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMRuntimeLocalAlias(raw)
	})
}

type vmRuntimeImportFixture struct {
	manifest    vmRuntimeManifest
	firecracker []byte
	jailer      []byte
}

func newVMRuntimeImportFixture(t *testing.T) *vmRuntimeImportFixture {
	t.Helper()
	originalArchitectureInspector := inspectVMRuntimeBinaryArchitecture
	inspectVMRuntimeBinaryArchitecture = func(string) (string, error) { return "amd64", nil }
	t.Cleanup(func() { inspectVMRuntimeBinaryArchitecture = originalArchitectureInspector })
	fixture := &vmRuntimeImportFixture{
		manifest:    validVMRuntimeManifest(),
		firecracker: vmRuntimeTestExecutable("Firecracker", "v1.16.1"),
		jailer:      vmRuntimeTestExecutable("Jailer", "v1.16.1"),
	}
	fixture.manifest.Components.Firecracker.SHA256 = vmRuntimeTestSHA256(fixture.firecracker)
	fixture.manifest.Components.Jailer.SHA256 = vmRuntimeTestSHA256(fixture.jailer)
	return fixture
}

func setVMRuntimeImportFixtureVersion(fixture *vmRuntimeImportFixture, version string) {
	fixture.manifest.RuntimeID = "firecracker-" + version + "-yeet-v1"
	fixture.manifest.Upstream.Version = version
	fixture.manifest.Upstream.Tag = version
	fixture.manifest.Upstream.ArchiveURL = "https://github.com/firecracker-microvm/firecracker/releases/download/" + version + "/firecracker-" + version + "-x86_64.tgz"
	fixture.manifest.Upstream.ChecksumURL = fixture.manifest.Upstream.ArchiveURL + ".sha256.txt"
	fixture.firecracker = vmRuntimeTestExecutable("Firecracker", version)
	fixture.jailer = vmRuntimeTestExecutable("Jailer", version)
	fixture.manifest.Components.Firecracker.VersionOutput = "Firecracker " + version
	fixture.manifest.Components.Firecracker.SHA256 = vmRuntimeTestSHA256(fixture.firecracker)
	fixture.manifest.Components.Jailer.VersionOutput = "Jailer " + version
	fixture.manifest.Components.Jailer.SHA256 = vmRuntimeTestSHA256(fixture.jailer)
}

func replaceVMRuntimeLocalAliasTestRaw(t testing.TB, path string, raw []byte) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove alias: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("replace alias: %v", err)
	}
}

func (f *vmRuntimeImportFixture) manifestRaw(t testing.TB) []byte {
	t.Helper()
	return marshalVMRuntimeTestJSON(t, f.manifest)
}

func (f *vmRuntimeImportFixture) entries(t testing.TB) []vmRuntimeImportTestEntry {
	t.Helper()
	return []vmRuntimeImportTestEntry{
		{name: vmRuntimeManifestFilename, mode: 0o644, content: f.manifestRaw(t)},
		{name: "firecracker", mode: 0o755, content: append([]byte(nil), f.firecracker...)},
		{name: "jailer", mode: 0o755, content: append([]byte(nil), f.jailer...)},
	}
}

func (f *vmRuntimeImportFixture) archive(t testing.TB) []byte {
	t.Helper()
	return writeVMRuntimeImportTestTar(t, f.entries(t))
}

type vmRuntimeImportTestEntry struct {
	name         string
	mode         int64
	content      []byte
	typeflag     byte
	linkname     string
	uid          int
	declaredSize int64
}

func writeVMRuntimeImportTestTar(t testing.TB, entries []vmRuntimeImportTestEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(len(entry.content))
		if entry.declaredSize != 0 {
			size = entry.declaredSize
		}
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode, Size: size, Typeflag: typeflag,
			Linkname: entry.linkname, Uid: entry.uid, Gid: 0,
			ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write tar header %s: %v", entry.name, err)
		}
		if _, err := writer.Write(entry.content); err != nil && entry.declaredSize == 0 {
			t.Fatalf("write tar entry %s: %v", entry.name, err)
		}
	}
	_ = writer.Close()
	return buffer.Bytes()
}

func assertVMRuntimeImportMode(t testing.TB, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode %s = %04o, want %04o", path, info.Mode().Perm(), want)
	}
}

func assertNoVMRuntimeImportStaging(t testing.TB, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".runtime-import-") {
			t.Fatalf("staging directory remains at %s", path)
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".") && strings.Contains(entry.Name(), ".json.tmp-") {
			t.Fatalf("alias staging file remains at %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk runtime cache: %v", err)
	}
}

func assertVMRuntimeImportNotPublished(t testing.TB, root string, manifest vmRuntimeManifest) {
	t.Helper()
	parent := filepath.Join(root, manifest.Architecture, manifest.RuntimeID)
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read runtime identity directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("runtime import published entries after failure: %v", entries)
	}
}
