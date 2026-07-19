// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceIdentityGuardedCopyRejectsSymlinkSwapBetweenStatAndOpen(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "source")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(serviceDataDirForRoot(root), "target")
			if kind == "directory" {
				if err := os.Mkdir(target, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "state"), []byte("source"), 0o640); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("source"), 0o640); err != nil {
				t.Fatal(err)
			}
			victimRoot := t.TempDir()
			victim := filepath.Join(victimRoot, "victim")
			if kind == "directory" {
				if err := os.Mkdir(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(victim, "secret"), []byte("operator"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(victim, []byte("operator"), 0o600); err != nil {
				t.Fatal(err)
			}

			guard := newTestServiceIdentityCopyGuard(t, root)
			swapped := false
			guard.beforeOpen = func(path string) {
				if swapped || filepath.Clean(path) != filepath.Clean(target) {
					return
				}
				swapped = true
				if err := os.Rename(target, target+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, target); err != nil {
					t.Fatal(err)
				}
			}
			stage := t.TempDir()
			err := guard.copyToStage(stage)
			if err == nil || !strings.Contains(err.Error(), "open guarded source") {
				t.Fatalf("guarded copy error = %v, want NOFOLLOW open rejection", err)
			}
			if !swapped {
				t.Fatal("test did not swap the source entry")
			}
			if kind == "directory" {
				raw, readErr := os.ReadFile(filepath.Join(victim, "secret"))
				if readErr != nil || string(raw) != "operator" {
					t.Fatalf("operator directory changed: %q, %v", raw, readErr)
				}
			} else {
				raw, readErr := os.ReadFile(victim)
				if readErr != nil || string(raw) != "operator" {
					t.Fatalf("operator file changed: %q, %v", raw, readErr)
				}
			}
			if raw, readErr := os.ReadFile(filepath.Join(stage, "data", "target")); readErr == nil && string(raw) == "operator" {
				t.Fatalf("guarded copy followed swapped source into stage: %q", raw)
			}
		})
	}
}

func TestServiceIdentityGuardedCopyRevalidatesDirectoryEntryAfterCopy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(serviceDataDirForRoot(root), "payload")
	if err := os.WriteFile(target, []byte("source"), 0o640); err != nil {
		t.Fatal(err)
	}
	guard := newTestServiceIdentityCopyGuard(t, root)
	changed := false
	guard.afterCopy = func(path string) {
		if changed || filepath.Clean(path) != filepath.Clean(target) {
			return
		}
		changed = true
		if err := os.Rename(target, target+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("replacement"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := guard.copyToStage(t.TempDir()); err == nil || !strings.Contains(err.Error(), "changed during copy") {
		t.Fatalf("guarded copy error = %v, want final inode revalidation failure", err)
	}
}

func TestServiceIdentityGuardedCopyRejectsHardLinkedSourceFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(serviceDataDirForRoot(root), "state")
	if err := os.WriteFile(path, []byte("state"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+".link"); err != nil {
		t.Fatal(err)
	}

	guard := newTestServiceIdentityCopyGuard(t, root)
	if err := guard.copyToStage(t.TempDir()); err == nil || !strings.Contains(err.Error(), "hard-linked source entry") {
		t.Fatalf("guarded copy error = %v, want hard-link rejection", err)
	}
}

func TestServiceIdentityGuardedCopyDetectsInPlaceContentChange(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(serviceDataDirForRoot(root), "state")
	if err := os.WriteFile(path, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	guard := newTestServiceIdentityCopyGuard(t, root)
	guard.afterCopy = func(current string) {
		if filepath.Clean(current) == filepath.Clean(path) {
			if err := os.WriteFile(path, []byte("after!"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := guard.copyToStage(t.TempDir()); err == nil || !strings.Contains(err.Error(), "changed during copy") {
		t.Fatalf("guarded copy error = %v, want metadata revalidation failure", err)
	}
}

func newTestServiceIdentityCopyGuard(t *testing.T, root string) serviceIdentityCopyGuard {
	t.Helper()
	oldMounts, oldDatasets := serviceIdentityMountPointsFn, serviceIdentityDatasetBoundariesFn
	serviceIdentityMountPointsFn = func() ([]string, error) { return nil, nil }
	serviceIdentityDatasetBoundariesFn = func(context.Context, zfsCommandRunner, string) ([]serviceIdentityDatasetBoundary, error) {
		return nil, nil
	}
	t.Cleanup(func() {
		serviceIdentityMountPointsFn, serviceIdentityDatasetBoundariesFn = oldMounts, oldDatasets
	})
	guard, err := newServiceIdentityCopyGuard(context.Background(), root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}
