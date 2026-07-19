// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestVMLegacyCompositionCanonicalRecordAndIDs(t *testing.T) {
	rootFS := testVMRuntimeAdoptionFileEvidence("/images/one/rootfs.ext4", "rootfs")
	kernel := testVMRuntimeAdoptionFileEvidence("/images/one/vmlinux", "kernel")
	config := testVMRuntimeAdoptionFileEvidence("/images/one/kernel.config", "config")
	initrd := testVMRuntimeAdoptionFileEvidence("/images/one/initrd.img", "initrd")
	firecracker := testVMRuntimeAdoptionFileEvidence("/images/one/firecracker", "firecracker")
	jailer := testVMRuntimeAdoptionFileEvidence("/images/one/jailer", "jailer")

	record, err := newVMLegacyCompositionRecord(vmLegacyCompositionInput{
		Architecture:       "x86_64",
		GuestName:          " Ubuntu 26.04 LTS ",
		GuestVersion:       " V29 / Stable ",
		KernelVersion:      " Linux 7.1.4-Yeet.1 ",
		FirecrackerVersion: " v1.16.1 ",
		RootFS:             rootFS,
		Kernel:             kernel,
		KernelConfig:       config,
		Initrd:             initrd,
		Firecracker:        firecracker,
		Jailer:             jailer,
	})
	if err != nil {
		t.Fatalf("newVMLegacyCompositionRecord: %v", err)
	}
	raw, digest, err := canonicalVMLegacyComposition(record)
	if err != nil {
		t.Fatalf("canonicalVMLegacyComposition: %v", err)
	}
	wantRaw := fmt.Sprintf(
		`{"schema":"yeet.vm.legacy-composition","schemaVersion":1,"architecture":"amd64","guestBase":{"name":"ubuntu-26-04-lts","version":"v29-stable","rootfsSHA256":%q},"kernel":{"version":"linux-7-1-4-yeet-1","kernelSHA256":%q,"configSHA256":%q,"initrdSHA256":%q},"runtime":{"version":"v1-16-1","firecrackerSHA256":%q,"jailerSHA256":%q}}`,
		rootFS.SHA256, kernel.SHA256, config.SHA256, initrd.SHA256, firecracker.SHA256, jailer.SHA256,
	)
	if string(raw) != wantRaw {
		t.Fatalf("canonical bytes:\n got: %s\nwant: %s", raw, wantRaw)
	}
	if strings.HasSuffix(string(raw), "\n") {
		t.Fatal("canonical record unexpectedly ends in a newline")
	}
	if digest != vmLegacySHA256Bytes(raw) || !isLowerSHA256(digest) {
		t.Fatalf("digest = %q", digest)
	}

	guestID, kernelID, runtimeID, err := vmLegacyCompositionIDs(record, digest)
	if err != nil {
		t.Fatalf("vmLegacyCompositionIDs: %v", err)
	}
	if want := "legacy-guest-ubuntu-26-04-lts-v29-stable-" + rootFS.SHA256; guestID != want {
		t.Fatalf("guest ID = %q, want %q", guestID, want)
	}
	if want := "legacy-kernel-linux-7-1-4-yeet-1-" + kernel.SHA256; kernelID != want {
		t.Fatalf("kernel ID = %q, want %q", kernelID, want)
	}
	if want := "legacy-firecracker-v1-16-1-" + firecracker.SHA256 + "-jailer-" + jailer.SHA256; runtimeID != want {
		t.Fatalf("runtime ID = %q, want %q", runtimeID, want)
	}
}

func TestVMLegacyCompositionComponentIDsChangeIndependently(t *testing.T) {
	baseInput := testVMLegacyCompositionInput()
	baseRecord, baseDigest, baseIDs := testVMLegacyCompositionIdentity(t, baseInput)

	tests := []struct {
		name       string
		mutate     func(*vmLegacyCompositionInput)
		changedIDs [3]bool
	}{
		{
			name: "runtime",
			mutate: func(input *vmLegacyCompositionInput) {
				input.Firecracker = testVMRuntimeAdoptionFileEvidence("/bundle/firecracker", "new firecracker")
			},
			changedIDs: [3]bool{false, false, true},
		},
		{
			name: "kernel",
			mutate: func(input *vmLegacyCompositionInput) {
				input.Kernel = testVMRuntimeAdoptionFileEvidence("/bundle/vmlinux", "new kernel")
			},
			changedIDs: [3]bool{false, true, false},
		},
		{
			name: "rootfs",
			mutate: func(input *vmLegacyCompositionInput) {
				input.RootFS = testVMRuntimeAdoptionFileEvidence("/bundle/rootfs.ext4", "new rootfs")
			},
			changedIDs: [3]bool{true, false, false},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseInput
			test.mutate(&input)
			record, digest, ids := testVMLegacyCompositionIdentity(t, input)
			if reflect.DeepEqual(record, baseRecord) || digest == baseDigest {
				t.Fatal("component change did not change full composition identity")
			}
			for i, changed := range test.changedIDs {
				if (ids[i] != baseIDs[i]) != changed {
					t.Errorf("ID %d changed = %v, want %v\nbase: %s\n got: %s", i, ids[i] != baseIDs[i], changed, baseIDs[i], ids[i])
				}
			}
		})
	}
}

func TestVMLegacyCompositionIdentityExcludesRelocationAndMutableState(t *testing.T) {
	input := testVMLegacyCompositionInput()
	first, err := newVMLegacyCompositionRecord(input)
	if err != nil {
		t.Fatal(err)
	}

	relocated := input
	relocated.RootFS.Path = "/relocated/guest/rootfs.ext4"
	relocated.Kernel.Path = "/relocated/boot/vmlinux"
	relocated.KernelConfig.Path = "/relocated/boot/kernel.config"
	relocated.Initrd.Path = "/relocated/boot/initrd.img"
	relocated.Firecracker.Path = "/relocated/runtime/firecracker"
	relocated.Jailer.Path = "/relocated/runtime/jailer"
	second, err := newVMLegacyCompositionRecord(relocated)
	if err != nil {
		t.Fatal(err)
	}

	firstRaw, firstDigest, err := canonicalVMLegacyComposition(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, secondDigest, err := canonicalVMLegacyComposition(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstRaw, secondRaw) || firstDigest != secondDigest {
		t.Fatalf("relocation changed composition identity:\n%s\n%s", firstRaw, secondRaw)
	}

	preconditionOne := vmRuntimeAdoptionPreconditionEvidence{
		Service:     "alpha",
		ServiceRoot: "/services/alpha",
		Files:       []vmRuntimeAdoptionFileEvidence{input.RootFS, input.Kernel},
		ActiveDisk: vmRuntimeAdoptionActiveDiskEvidence{
			Path: "/services/alpha/data/rootfs.ext4", Backend: "raw", Bytes: 1 << 30,
		},
	}
	preconditionTwo := preconditionOne
	preconditionTwo.Service = "renamed"
	preconditionTwo.ServiceRoot = "/relocated/services/renamed"
	preconditionTwo.ActiveDisk.Path = "/dev/zvol/pool/renamed"
	preconditionTwo.ActiveDisk.Backend = vmDiskBackendZVOL
	preconditionTwo.ActiveDisk.Bytes = 2 << 30
	if reflect.DeepEqual(preconditionOne, preconditionTwo) {
		t.Fatal("precondition evidence did not retain relocation-sensitive state")
	}
	for _, forbidden := range []string{preconditionOne.Service, preconditionOne.ServiceRoot, preconditionOne.ActiveDisk.Path} {
		if bytes.Contains(firstRaw, []byte(forbidden)) {
			t.Fatalf("composition identity includes relocation-sensitive value %q", forbidden)
		}
	}

	activeType := reflect.TypeOf(vmRuntimeAdoptionActiveDiskEvidence{})
	for i := 0; i < activeType.NumField(); i++ {
		if strings.Contains(strings.ToLower(activeType.Field(i).Name), "sha") || strings.Contains(strings.ToLower(activeType.Field(i).Name), "digest") {
			t.Fatalf("active disk evidence exposes content identity field %q", activeType.Field(i).Name)
		}
	}
}

func TestNormalizeVMLegacyIDSegment(t *testing.T) {
	tests := map[string]string{
		" Ubuntu 26.04 LTS ": "ubuntu-26-04-lts",
		"V29///Stable":       "v29-stable",
		"already-normal":     "already-normal",
		"A__B...C":           "a-b-c",
		"---":                "",
		"Straße":             "stra-e",
	}
	for input, want := range tests {
		if got := normalizeVMLegacyIDSegment(input); got != want {
			t.Errorf("normalizeVMLegacyIDSegment(%q) = %q, want %q", input, got, want)
		}
	}
}

func FuzzNormalizeVMLegacyIDSegment(f *testing.F) {
	for _, seed := range []string{"Ubuntu 26.04", "V29///Stable", "---", "Straße", "a_b.c"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got := normalizeVMLegacyIDSegment(input)
		if got != strings.Trim(got, "-") || strings.Contains(got, "--") {
			t.Fatalf("non-canonical normalized segment %q", got)
		}
		for _, c := range []byte(got) {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				t.Fatalf("normalized segment contains byte %q", c)
			}
		}
		if again := normalizeVMLegacyIDSegment(got); again != got {
			t.Fatalf("normalizer is not idempotent: %q -> %q", got, again)
		}
	})
}

func FuzzValidateVMRuntimeAdoptionEvidencePath(f *testing.F) {
	for _, seed := range []string{"/var/lib/yeet/rootfs.ext4", "relative", "/a/../b", " /tmp/disk", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if err := validateVMRuntimeAdoptionEvidencePath(path); err == nil {
			if strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
				t.Fatalf("validator accepted non-canonical path %q", path)
			}
		}
	})
}

func TestCollectVMRuntimeAdoptionFileEvidence(t *testing.T) {
	root := resolvedTempDir(t)
	path := filepath.Join(root, "rootfs.ext4")
	contents := []byte("immutable guest base")
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatal(err)
	}
	deps := defaultVMRuntimeAdoptionEvidenceDeps()
	deps.trustedUID = uint32(os.Geteuid())

	evidence, err := collectVMRuntimeAdoptionFileEvidence(path, true, deps)
	if err != nil {
		t.Fatalf("collectVMRuntimeAdoptionFileEvidence: %v", err)
	}
	if !evidence.Exists || evidence.Path != path || evidence.Size != int64(len(contents)) || evidence.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Fatalf("evidence = %#v", evidence)
	}
	if evidence.UID != uint32(os.Geteuid()) || evidence.GID != uint32(os.Getegid()) || evidence.Device == 0 || evidence.Inode == 0 || evidence.MTimeNS == 0 {
		t.Fatalf("incomplete evidence = %#v", evidence)
	}
	if want := vmLegacySHA256Bytes(contents); evidence.SHA256 != want {
		t.Fatalf("SHA256 = %q, want %q", evidence.SHA256, want)
	}

	missing := filepath.Join(root, "not-present")
	optional, err := collectVMRuntimeAdoptionFileEvidence(missing, false, deps)
	if err != nil {
		t.Fatalf("optional missing evidence: %v", err)
	}
	if optional != (vmRuntimeAdoptionFileEvidence{Path: missing}) {
		t.Fatalf("optional missing evidence = %#v", optional)
	}
	if _, err := collectVMRuntimeAdoptionFileEvidence(missing, true, deps); err == nil {
		t.Fatal("required missing evidence unexpectedly succeeded")
	}
}

func TestCollectVMRuntimeAdoptionFileEvidenceRefusesUntrustedInputs(t *testing.T) {
	root := resolvedTempDir(t)
	deps := defaultVMRuntimeAdoptionEvidenceDeps()
	deps.trustedUID = uint32(os.Geteuid())

	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(regular, link); err != nil {
			t.Fatal(err)
		}
		if _, err := collectVMRuntimeAdoptionFileEvidence(link, true, deps); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
			t.Fatalf("error = %v, want symbolic link refusal", err)
		}
	})
	t.Run("nonregular", func(t *testing.T) {
		if _, err := collectVMRuntimeAdoptionFileEvidence(root, true, deps); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("error = %v, want regular file refusal", err)
		}
	})
	t.Run("writable", func(t *testing.T) {
		path := filepath.Join(root, "writable")
		if err := os.WriteFile(path, []byte("data"), 0o660); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o660); err != nil {
			t.Fatal(err)
		}
		if _, err := collectVMRuntimeAdoptionFileEvidence(path, true, deps); err == nil || !strings.Contains(err.Error(), "group or other writable") {
			t.Fatalf("error = %v, want writable refusal", err)
		}
	})
	t.Run("wrong owner", func(t *testing.T) {
		wrongOwner := deps
		wrongOwner.trustedUID++
		if _, err := collectVMRuntimeAdoptionFileEvidence(regular, true, wrongOwner); err == nil || !strings.Contains(err.Error(), "owner UID") {
			t.Fatalf("error = %v, want owner refusal", err)
		}
	})
	t.Run("bounded", func(t *testing.T) {
		bounded := deps
		bounded.maxFileBytes = 3
		if _, err := collectVMRuntimeAdoptionFileEvidence(regular, true, bounded); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v, want size bound", err)
		}
	})
}

func TestCollectVMRuntimeActiveDiskEvidenceNeverReadsContent(t *testing.T) {
	root := resolvedTempDir(t)
	rawPath := filepath.Join(root, "disk.raw")
	if err := os.WriteFile(rawPath, []byte("mutable raw disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := defaultVMRuntimeAdoptionEvidenceDeps()
	deps.copy = func(io.Writer, io.Reader) (int64, error) {
		panic("active disk content was read")
	}
	raw, err := collectVMRuntimeActiveDiskEvidence(rawPath, vmDiskBackendRaw, 4<<30, deps)
	if err != nil {
		t.Fatalf("collect raw VM disk evidence: %v", err)
	}
	if raw.Path != rawPath || raw.Backend != vmDiskBackendRaw || raw.Bytes != 4<<30 || raw.ResolvedPath != rawPath || raw.Mode&unix.S_IFMT != unix.S_IFREG || raw.Dataset != "" {
		t.Fatalf("raw active disk evidence = %#v", raw)
	}
	if _, err := collectVMRuntimeActiveDiskEvidence("/dev/null", vmDiskBackendRaw, 4<<30, deps); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("raw character-device error = %v", err)
	}

	zvolPath := "/dev/zvol/tank/yeet/vms/devbox/root"
	resolverCalls := 0
	deps.resolveZVOL = func(path string) (vmRuntimeAdoptionZVOLResolution, error) {
		resolverCalls++
		if path != zvolPath {
			t.Fatalf("resolver path = %q, want %q", path, zvolPath)
		}
		return vmRuntimeAdoptionZVOLResolution{
			Dataset: "tank/yeet/vms/devbox/root", ResolvedPath: "/dev/zd42", LinkDevice: 9, LinkInode: 10,
			LinkMode: unix.S_IFLNK | 0o777, LinkUID: 0, LinkGID: 0,
			Metadata: vmRuntimeAdoptionFileMetadata{
				Device: 11, Inode: 12, RDevice: 13, Size: 0, Mode: unix.S_IFBLK | 0o660, UID: 0, GID: 6, MTimeNS: 14,
			},
		}, nil
	}
	zvol, err := collectVMRuntimeActiveDiskEvidence(zvolPath, vmDiskBackendZVOL, 8<<30, deps)
	if err != nil {
		t.Fatalf("collect zvol VM disk evidence: %v", err)
	}
	if resolverCalls != 1 || zvol.Path != zvolPath || zvol.Backend != vmDiskBackendZVOL || zvol.Bytes != 8<<30 || zvol.Dataset != "tank/yeet/vms/devbox/root" || zvol.ResolvedPath != "/dev/zd42" || zvol.Mode&unix.S_IFMT != unix.S_IFBLK {
		t.Fatalf("zvol active disk evidence = %#v, resolver calls = %d", zvol, resolverCalls)
	}
	for _, invalid := range []struct {
		path    string
		backend string
		bytes   int64
	}{
		{path: rawPath, backend: "", bytes: 4 << 30},
		{path: rawPath, backend: "zfs", bytes: 4 << 30},
		{path: rawPath, backend: "unknown", bytes: 4 << 30},
		{path: rawPath, backend: vmDiskBackendRaw, bytes: 0},
		{path: zvolPath, backend: vmDiskBackendZVOL, bytes: -1},
	} {
		if _, err := collectVMRuntimeActiveDiskEvidence(invalid.path, invalid.backend, invalid.bytes, deps); err == nil {
			t.Errorf("backend %q bytes %d unexpectedly accepted", invalid.backend, invalid.bytes)
		}
	}
}

func TestCollectVMRuntimeActiveDiskEvidenceRejectsInvalidZVOLResolution(t *testing.T) {
	const path = "/dev/zvol/tank/yeet/vms/devbox/root"
	valid := vmRuntimeAdoptionZVOLResolution{
		Dataset: "tank/yeet/vms/devbox/root", ResolvedPath: "/dev/zd42", LinkDevice: 9, LinkInode: 10,
		LinkMode: unix.S_IFLNK | 0o777, LinkUID: 0, LinkGID: 0,
		Metadata: vmRuntimeAdoptionFileMetadata{Device: 11, Inode: 12, RDevice: 13, Mode: unix.S_IFBLK | 0o660, UID: 0},
	}
	tests := map[string]func(*vmRuntimeAdoptionZVOLResolution){
		"wrong dataset":           func(got *vmRuntimeAdoptionZVOLResolution) { got.Dataset = "tank/other" },
		"target outside dev":      func(got *vmRuntimeAdoptionZVOLResolution) { got.ResolvedPath = "/tmp/zd42" },
		"missing link identity":   func(got *vmRuntimeAdoptionZVOLResolution) { got.LinkInode = 0 },
		"non-link identity":       func(got *vmRuntimeAdoptionZVOLResolution) { got.LinkMode = unix.S_IFREG | 0o644 },
		"wrong link owner":        func(got *vmRuntimeAdoptionZVOLResolution) { got.LinkUID = 812 },
		"character target":        func(got *vmRuntimeAdoptionZVOLResolution) { got.Metadata.Mode = unix.S_IFCHR | 0o660 },
		"wrong target owner":      func(got *vmRuntimeAdoptionZVOLResolution) { got.Metadata.UID = 812 },
		"missing device identity": func(got *vmRuntimeAdoptionZVOLResolution) { got.Metadata.RDevice = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			resolution := valid
			mutate(&resolution)
			deps := defaultVMRuntimeAdoptionEvidenceDeps()
			deps.resolveZVOL = func(string) (vmRuntimeAdoptionZVOLResolution, error) { return resolution, nil }
			if _, err := collectVMRuntimeActiveDiskEvidence(path, vmDiskBackendZVOL, 8<<30, deps); err == nil {
				t.Fatal("invalid zvol resolution unexpectedly accepted")
			}
		})
	}
	deps := defaultVMRuntimeAdoptionEvidenceDeps()
	deps.resolveZVOL = func(string) (vmRuntimeAdoptionZVOLResolution, error) { return valid, nil }
	for _, badPath := range []string{"/dev/zd42", "/dev/zvol/", "/dev/zvol/tank/../root", "/dev/zvol/tank/root@snap"} {
		if _, err := collectVMRuntimeActiveDiskEvidence(badPath, vmDiskBackendZVOL, 8<<30, deps); err == nil {
			t.Errorf("invalid zvol path %q unexpectedly accepted", badPath)
		}
	}
}

func TestVMRuntimeAdoptionZVOLPathHelpers(t *testing.T) {
	dir := t.TempDir()
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	link := filepath.Join(dir, "disk")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Fatal(err)
	}
	if got, err := readVMRuntimeAdoptionLinkAt(parent, "disk"); err != nil || got != "/dev/null" {
		t.Fatalf("read link = %q, %v", got, err)
	}
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), "disk", &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	if err := revalidateVMRuntimeAdoptionZVOLLink(parent, "disk", before, "/dev/null"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", link); err != nil {
		t.Fatal(err)
	}
	if err := revalidateVMRuntimeAdoptionZVOLLink(parent, "disk", before, "/dev/null"); err == nil {
		t.Fatal("changed zvol link accepted")
	}

	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("not a link"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := inspectVMRuntimeAdoptionZVOLLink(parent, regular); err == nil {
		t.Fatal("regular file accepted as zvol link")
	}
	if _, _, _, err := openVMRuntimeAdoptionZVOLTarget("/dev/null"); err == nil {
		t.Fatal("character device accepted as zvol block target")
	}
	if _, _, _, err := openVMRuntimeAdoptionZVOLTarget(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing zvol target accepted")
	}
	if linkDir, ancestor, err := openVMRuntimeAdoptionZVOLLinkParent(link); err == nil {
		_ = closeVMRuntimeAdoptionPath(linkDir, ancestor)
	}
	if _, err := resolveVMRuntimeAdoptionZVOL("/not-a-zvol"); err == nil {
		t.Fatal("non-zvol path accepted")
	}
	if _, err := resolveVMRuntimeAdoptionZVOL("/dev/zvol/no/such/dataset"); err == nil {
		t.Fatal("missing zvol path accepted")
	}
}

func TestPersistVMLegacyCompositionIdempotentAndConflictingExistingRefused(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())

	first, err := persistVMLegacyComposition(root, record, deps)
	if err != nil {
		t.Fatalf("first persist: %v", err)
	}
	wantPath := filepath.Join(root, "vm-component-provenance", "sha256", first.SHA256+".json")
	if first.Path != wantPath {
		t.Fatalf("path = %q, want %q", first.Path, wantPath)
	}
	raw, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, first.Bytes) || vmLegacySHA256Bytes(raw) != first.SHA256 || bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatalf("persisted bytes = %q, publication = %#v", raw, first)
	}
	info, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("published mode = %o, want 0600", info.Mode().Perm())
	}

	second, err := persistVMLegacyComposition(root, record, deps)
	if err != nil {
		t.Fatalf("idempotent persist: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent result = %#v, want %#v", second, first)
	}

	if err := os.WriteFile(first.Path, []byte("conflicting record"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := persistVMLegacyComposition(root, record, deps); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v, want conflict refusal", err)
	}
}

func TestPersistVMLegacyCompositionNoReplaceRace(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())

	const writers = 16
	var publishReady sync.WaitGroup
	publishReady.Add(writers)
	deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		publishReady.Done()
		publishReady.Wait()
		return renameVMJailerUnitNameNoReplaceAt(oldDir, oldName, newDir, newName)
	}
	results := make(chan vmLegacyCompositionPublication, writers)
	errorsCh := make(chan error, writers)
	var ready sync.WaitGroup
	ready.Add(writers)
	start := make(chan struct{})
	var writersWG sync.WaitGroup
	writersWG.Add(writers)
	for range writers {
		go func() {
			defer writersWG.Done()
			ready.Done()
			<-start
			publication, err := persistVMLegacyComposition(root, record, deps)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- publication
		}()
	}
	ready.Wait()
	close(start)
	writersWG.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent persist: %v", err)
	}
	var first vmLegacyCompositionPublication
	for result := range results {
		if first.Path == "" {
			first = result
			continue
		}
		if result.Path != first.Path || result.SHA256 != first.SHA256 || !bytes.Equal(result.Bytes, first.Bytes) {
			t.Errorf("concurrent result = %#v, want %#v", result, first)
		}
	}
	if first.Path == "" {
		t.Fatal("no writer succeeded")
	}
	entries, err := os.ReadDir(filepath.Dir(first.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != first.SHA256+".json" {
		t.Fatalf("published entries = %#v", entries)
	}
}

func TestPersistVMLegacyCompositionSyncsFileAndDirectoryChain(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())

	var mu sync.Mutex
	fileSyncs := 0
	directorySyncs := make(map[string]int)
	deps.syncFile = func(file *os.File) error {
		mu.Lock()
		defer mu.Unlock()
		fileSyncs++
		return file.Sync()
	}
	deps.syncDir = func(dir *os.File) error {
		mu.Lock()
		directorySyncs[dir.Name()]++
		mu.Unlock()
		return dir.Sync()
	}
	if _, err := persistVMLegacyComposition(root, record, deps); err != nil {
		t.Fatal(err)
	}
	if fileSyncs != 1 {
		t.Fatalf("file syncs = %d, want 1", fileSyncs)
	}
	for _, name := range []string{root, vmLegacyProvenanceDirName, vmLegacyProvenanceDigestDirName} {
		if directorySyncs[name] == 0 {
			t.Errorf("directory %q was not synced", name)
		}
	}
}

func TestPersistVMLegacyCompositionFreshRetryResyncsExistingParent(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())
	failed := false
	deps.syncDir = func(dir *os.File) error {
		if dir.Name() == root && !failed {
			failed = true
			return unix.EIO
		}
		return dir.Sync()
	}
	_, err := persistVMLegacyComposition(root, record, deps)
	if !errors.Is(err, unix.EIO) {
		t.Fatalf("first persist error = %v, want parent sync failure", err)
	}
	var postPublication *vmLegacyCompositionPostPublicationError
	if errors.As(err, &postPublication) {
		t.Fatalf("directory creation failure reported post-publication: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, vmLegacyProvenanceDirName)); err != nil {
		t.Fatalf("created directory was not retained for retry: %v", err)
	}

	retry := defaultVMLegacyCompositionStoreDeps()
	retry.trustedUID = uint32(os.Geteuid())
	rootSyncs := 0
	retry.syncDir = func(dir *os.File) error {
		if dir.Name() == root {
			rootSyncs++
		}
		return dir.Sync()
	}
	if _, err := persistVMLegacyComposition(root, record, retry); err != nil {
		t.Fatalf("fresh-process retry: %v", err)
	}
	if rootSyncs == 0 {
		t.Fatal("fresh-process retry did not resync existing directory parent")
	}
}

func TestPersistVMLegacyCompositionReportsAndRepairsPostPublicationSync(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())
	failed := false
	deps.syncDir = func(dir *os.File) error {
		if dir.Name() == vmLegacyProvenanceDigestDirName && !failed {
			failed = true
			return unix.EIO
		}
		return dir.Sync()
	}
	publication, err := persistVMLegacyComposition(root, record, deps)
	if !errors.Is(err, unix.EIO) {
		t.Fatalf("persist error = %v, want digest-directory sync failure", err)
	}
	var postPublication *vmLegacyCompositionPostPublicationError
	if !errors.As(err, &postPublication) {
		t.Fatalf("error = %v, want typed post-publication outcome", err)
	}
	if got := postPublication.Publication(); !reflect.DeepEqual(got, publication) {
		t.Fatalf("retained publication = %#v, want %#v", got, publication)
	}
	if raw, readErr := os.ReadFile(publication.Path); readErr != nil || !bytes.Equal(raw, publication.Bytes) {
		t.Fatalf("retained publication read = %q, %v", raw, readErr)
	}

	retry := defaultVMLegacyCompositionStoreDeps()
	retry.trustedUID = uint32(os.Geteuid())
	digestSyncs := 0
	retry.syncDir = func(dir *os.File) error {
		if dir.Name() == vmLegacyProvenanceDigestDirName {
			digestSyncs++
		}
		return dir.Sync()
	}
	repaired, err := persistVMLegacyComposition(root, record, retry)
	if err != nil {
		t.Fatalf("repairing retry: %v", err)
	}
	if !reflect.DeepEqual(repaired, publication) || digestSyncs == 0 {
		t.Fatalf("repair result = %#v, digest syncs = %d", repaired, digestSyncs)
	}
}

func TestPersistVMLegacyCompositionDistinguishesPrePublicationFailureAndCleanup(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())
	deps.renameNoReplaceAt = func(int, string, int, string) error { return unix.EIO }
	deps.unlinkAt = func(int, string, int) error { return unix.EACCES }

	publication, err := persistVMLegacyComposition(root, record, deps)
	if !errors.Is(err, unix.EIO) || !errors.Is(err, unix.EACCES) {
		t.Fatalf("error = %v, want publication and cleanup failures", err)
	}
	var postPublication *vmLegacyCompositionPostPublicationError
	if errors.As(err, &postPublication) {
		t.Fatalf("pre-publication error reported as retained publication: %v", err)
	}
	if !reflect.DeepEqual(publication, vmLegacyCompositionPublication{}) {
		t.Fatalf("pre-publication result = %#v", publication)
	}
	canonicalRaw, digest, canonicalErr := canonicalVMLegacyComposition(record)
	if canonicalErr != nil || len(canonicalRaw) == 0 {
		t.Fatal(canonicalErr)
	}
	canonicalPath := filepath.Join(root, vmLegacyProvenanceDirName, vmLegacyProvenanceDigestDirName, digest+".json")
	if _, statErr := os.Lstat(canonicalPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canonical path exists after failed publication: %v", statErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(canonicalPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Name(), ".staged-") {
		t.Fatalf("cleanup outcome not reflected by retained staging entry: %#v", entries)
	}
}

func TestPersistVMLegacyCompositionClassifiesErrorAfterCanonicalVisible(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())
	deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		if err := renameVMJailerUnitNameNoReplaceAt(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		return unix.EIO
	}

	publication, err := persistVMLegacyComposition(root, record, deps)
	if !errors.Is(err, unix.EIO) {
		t.Fatalf("persist error = %v, want injected post-visible error", err)
	}
	var postPublication *vmLegacyCompositionPostPublicationError
	if !errors.As(err, &postPublication) {
		t.Fatalf("error = %v, want typed post-publication outcome", err)
	}
	var uncertain *vmLegacyCompositionPublicationUncertainError
	if errors.As(err, &uncertain) {
		t.Fatalf("exact visible publication classified as uncertain: %v", err)
	}
	if !reflect.DeepEqual(postPublication.Publication(), publication) {
		t.Fatalf("retained publication = %#v, want %#v", postPublication.Publication(), publication)
	}
	raw, readErr := os.ReadFile(publication.Path)
	if readErr != nil || !bytes.Equal(raw, publication.Bytes) {
		t.Fatalf("canonical bytes = %q, %v", raw, readErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(publication.Path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(publication.Path) {
		t.Fatalf("post-visible cleanup entries = %#v", entries)
	}
}

func TestPersistVMLegacyCompositionClassifiesUncertainPublicationStates(t *testing.T) {
	record := mustTestVMLegacyCompositionRecord(t)
	tests := map[string]func(int, string, int, string) error{
		"canonical conflict": func(oldDir int, oldName string, newDir int, newName string) error {
			if err := renameVMJailerUnitNameNoReplaceAt(oldDir, oldName, newDir, newName); err != nil {
				return err
			}
			fd, err := unix.Openat(newDir, newName, unix.O_WRONLY|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			file := os.NewFile(uintptr(fd), newName)
			if file == nil {
				_ = unix.Close(fd)
				return fmt.Errorf("bind conflicting canonical fixture")
			}
			_, writeErr := file.Write([]byte("conflict"))
			return errors.Join(writeErr, file.Close(), unix.EIO)
		},
		"both names absent": func(oldDir int, oldName string, _ int, _ string) error {
			if err := unix.Unlinkat(oldDir, oldName, 0); err != nil {
				return err
			}
			return unix.EIO
		},
	}
	for name, rename := range tests {
		t.Run(name, func(t *testing.T) {
			root := resolvedTempDir(t)
			deps := defaultVMLegacyCompositionStoreDeps()
			deps.trustedUID = uint32(os.Geteuid())
			deps.renameNoReplaceAt = rename
			publication, err := persistVMLegacyComposition(root, record, deps)
			if !errors.Is(err, unix.EIO) {
				t.Fatalf("persist error = %v, want injected publication error", err)
			}
			var uncertain *vmLegacyCompositionPublicationUncertainError
			if !errors.As(err, &uncertain) {
				t.Fatalf("error = %v, want typed uncertain outcome", err)
			}
			var postPublication *vmLegacyCompositionPostPublicationError
			if errors.As(err, &postPublication) {
				t.Fatalf("uncertain outcome reported as exact retained publication: %v", err)
			}
			if !reflect.DeepEqual(uncertain.Publication(), publication) {
				t.Fatalf("uncertain publication = %#v, want %#v", uncertain.Publication(), publication)
			}
		})
	}
}

func TestPersistVMLegacyCompositionExistingRetrySyncFailureIsPostPublication(t *testing.T) {
	root := resolvedTempDir(t)
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())
	want, err := persistVMLegacyComposition(root, record, deps)
	if err != nil {
		t.Fatal(err)
	}

	retry := defaultVMLegacyCompositionStoreDeps()
	retry.trustedUID = uint32(os.Geteuid())
	retry.syncDir = func(dir *os.File) error {
		if dir.Name() == vmLegacyProvenanceDigestDirName {
			return unix.EIO
		}
		return dir.Sync()
	}
	got, err := persistVMLegacyComposition(root, record, retry)
	if !errors.Is(err, unix.EIO) {
		t.Fatalf("retry error = %v, want sync failure", err)
	}
	var postPublication *vmLegacyCompositionPostPublicationError
	if !errors.As(err, &postPublication) {
		t.Fatalf("retry error = %v, want typed post-publication outcome", err)
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(postPublication.Publication(), want) {
		t.Fatalf("retry publication = %#v / %#v, want %#v", got, postPublication.Publication(), want)
	}
}

func TestPersistVMLegacyCompositionRefusesUntrustedTree(t *testing.T) {
	record := mustTestVMLegacyCompositionRecord(t)
	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())

	t.Run("symlink component", func(t *testing.T) {
		root := resolvedTempDir(t)
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "vm-component-provenance")); err != nil {
			t.Fatal(err)
		}
		if _, err := persistVMLegacyComposition(root, record, deps); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
			t.Fatalf("error = %v, want symlink refusal", err)
		}
	})

	t.Run("non-directory component", func(t *testing.T) {
		root := resolvedTempDir(t)
		if err := os.WriteFile(filepath.Join(root, "vm-component-provenance"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := persistVMLegacyComposition(root, record, deps); err == nil || !strings.Contains(err.Error(), "directory") {
			t.Fatalf("error = %v, want directory refusal", err)
		}
	})

	t.Run("writable component", func(t *testing.T) {
		root := resolvedTempDir(t)
		path := filepath.Join(root, "vm-component-provenance")
		if err := os.Mkdir(path, 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o770); err != nil {
			t.Fatal(err)
		}
		if _, err := persistVMLegacyComposition(root, record, deps); err == nil || !strings.Contains(err.Error(), "0700") {
			t.Fatalf("error = %v, want exact mode refusal", err)
		}
	})

	t.Run("wrong owner", func(t *testing.T) {
		root := resolvedTempDir(t)
		wrongOwner := deps
		wrongOwner.trustedUID++
		if _, err := persistVMLegacyComposition(root, record, wrongOwner); err == nil || !strings.Contains(err.Error(), "owner UID") {
			t.Fatalf("error = %v, want owner refusal", err)
		}
	})
}

func TestPersistVMLegacyCompositionSupportsSetgidRoot(t *testing.T) {
	root := resolvedTempDir(t)
	if err := os.Chmod(root, 0o2700); err != nil {
		t.Fatal(err)
	}
	var rootStat unix.Stat_t
	if err := unix.Stat(root, &rootStat); err != nil {
		t.Fatal(err)
	}
	if uint32(rootStat.Mode)&unix.S_ISGID == 0 {
		t.Skip("filesystem did not retain setgid on test root")
	}

	deps := defaultVMLegacyCompositionStoreDeps()
	deps.trustedUID = uint32(os.Geteuid())
	publication, err := persistVMLegacyComposition(root, mustTestVMLegacyCompositionRecord(t), deps)
	if err != nil {
		t.Fatalf("persist with setgid root: %v", err)
	}
	for _, path := range []string{
		filepath.Join(root, "vm-component-provenance"),
		filepath.Join(root, "vm-component-provenance", "sha256"),
		publication.Path,
	} {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			t.Fatal(err)
		}
		if stat.Uid != uint32(os.Geteuid()) || stat.Gid != rootStat.Gid {
			t.Fatalf("%s owner = %d:%d, want %d:%d", path, stat.Uid, stat.Gid, os.Geteuid(), rootStat.Gid)
		}
	}
}

func TestCanonicalVMLegacyCompositionRejectsInvalidDigests(t *testing.T) {
	record := mustTestVMLegacyCompositionRecord(t)
	record.Runtime.FirecrackerSHA256 = strings.ToUpper(record.Runtime.FirecrackerSHA256)
	if _, _, err := canonicalVMLegacyComposition(record); err == nil || !strings.Contains(err.Error(), "lowercase SHA-256") {
		t.Fatalf("error = %v, want lowercase digest refusal", err)
	}

	record = mustTestVMLegacyCompositionRecord(t)
	record.Kernel.InitrdSHA256 = "not-a-digest"
	if _, _, err := canonicalVMLegacyComposition(record); err == nil {
		t.Fatal("invalid optional digest unexpectedly succeeded")
	}
}

func testVMLegacyCompositionInput() vmLegacyCompositionInput {
	return vmLegacyCompositionInput{
		Architecture:       "amd64",
		GuestName:          "ubuntu",
		GuestVersion:       "26.04-v29",
		KernelVersion:      "7.1.4-yeet.1",
		FirecrackerVersion: "1.16.1",
		RootFS:             testVMRuntimeAdoptionFileEvidence("/bundle/rootfs.ext4", "rootfs"),
		Kernel:             testVMRuntimeAdoptionFileEvidence("/bundle/vmlinux", "kernel"),
		KernelConfig:       testVMRuntimeAdoptionFileEvidence("/bundle/kernel.config", "config"),
		Initrd:             testVMRuntimeAdoptionFileEvidence("/bundle/initrd.img", "initrd"),
		Firecracker:        testVMRuntimeAdoptionFileEvidence("/bundle/firecracker", "firecracker"),
		Jailer:             testVMRuntimeAdoptionFileEvidence("/bundle/jailer", "jailer"),
	}
}

func mustTestVMLegacyCompositionRecord(t *testing.T) vmLegacyCompositionRecord {
	t.Helper()
	record, err := newVMLegacyCompositionRecord(testVMLegacyCompositionInput())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func testVMLegacyCompositionIdentity(t *testing.T, input vmLegacyCompositionInput) (vmLegacyCompositionRecord, string, [3]string) {
	t.Helper()
	record, err := newVMLegacyCompositionRecord(input)
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := canonicalVMLegacyComposition(record)
	if err != nil {
		t.Fatal(err)
	}
	guestID, kernelID, runtimeID, err := vmLegacyCompositionIDs(record, digest)
	if err != nil {
		t.Fatal(err)
	}
	return record, digest, [3]string{guestID, kernelID, runtimeID}
}

func testVMRuntimeAdoptionFileEvidence(path, contents string) vmRuntimeAdoptionFileEvidence {
	return vmRuntimeAdoptionFileEvidence{
		Path: path, Exists: true, Size: int64(len(contents)), Mode: unix.S_IFREG | 0o640,
		UID: 0, GID: 0, MTimeNS: 1, Device: 1, Inode: 2, SHA256: vmLegacySHA256Bytes([]byte(contents)),
	}
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestVMLegacyCompositionJSONHasNoMapFields(t *testing.T) {
	assertNoMapFields(t, reflect.TypeOf(vmLegacyCompositionRecord{}))
	raw, _, err := canonicalVMLegacyComposition(mustTestVMLegacyCompositionRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 6 {
		t.Fatalf("top-level fields = %d, want 6", len(decoded))
	}
}

func assertNoMapFields(t *testing.T, typ reflect.Type) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type.Kind() == reflect.Map {
			t.Fatalf("%s contains map field %s", typ, field.Name)
		}
		if field.Type.Kind() == reflect.Struct {
			assertNoMapFields(t, field.Type)
		}
	}
}
