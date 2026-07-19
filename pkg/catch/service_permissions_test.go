// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestServiceIdentityInspectionIsDeterministicAndDoesNotFollowSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z", "a"} {
		if err := os.WriteFile(filepath.Join(root, "data", name), []byte(name), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "data", "link")); err != nil {
		t.Fatal(err)
	}

	inspection, err := inspectServiceIdentityChange(context.Background(), serviceIdentityInspectionRequest{
		Root:        root,
		Target:      db.ServiceIdentity{UID: uint32(os.Geteuid() + 1), GID: uint32(os.Getegid() + 1)},
		MountPoints: []string{},
		ListXattrs:  func(string) ([]string, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, record := range inspection.Records {
		got = append(got, record.Path)
	}
	want := []string{".", "data", "data/a", "data/link", "data/z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inspection paths = %#v, want %#v", got, want)
	}
	for _, record := range inspection.Records {
		if record.Path == external {
			t.Fatalf("inspection followed symlink to %q", external)
		}
	}
}

func TestServiceIdentityHardLinkDiagnosticsChooseLexicalPath(t *testing.T) {
	inspector := serviceIdentityInspector{
		hardLinks: map[serviceIdentityInodeKey]serviceIdentityHardLink{
			{dev: 1, ino: 2}: {path: "/srv/api/data/z", links: 2},
			{dev: 1, ino: 1}: {path: "/srv/api/data/a", links: 2},
		},
		seenLinks: map[serviceIdentityInodeKey]uint64{
			{dev: 1, ino: 2}: 1,
			{dev: 1, ino: 1}: 1,
		},
	}
	for range 100 {
		err := inspector.validateHardLinks()
		if err == nil || !strings.Contains(err.Error(), "/srv/api/data/a") {
			t.Fatalf("validateHardLinks error = %v, want lexical first path", err)
		}
	}
}

func TestServiceIdentityInspectionRejectsHazardsBeforeReturningInventory(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string) serviceIdentityInspectionRequest
		want    string
	}{
		{
			name: "nested mount",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				nested := filepath.Join(root, "data", "mounted")
				if err := os.MkdirAll(nested, 0o700); err != nil {
					t.Fatal(err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{nested}}
			},
			want: "mount boundary",
		},
		{
			name: "nested dataset",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				nested := filepath.Join(root, "data", "dataset")
				if err := os.MkdirAll(nested, 0o700); err != nil {
					t.Fatal(err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{}, NestedDatasets: []serviceIdentityDatasetBoundary{{Dataset: "tank/api/nested", MountPoint: nested}}}
			},
			want: "nested ZFS dataset",
		},
		{
			name: "acl",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				path := filepath.Join(root, "data", "file")
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{}, ListXattrs: func(got string) ([]string, error) {
					if got == path {
						return []string{"system.posix_acl_access"}, nil
					}
					return nil, nil
				}}
			},
			want: "system.posix_acl_access",
		},
		{
			name: "default acl",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				return serviceIdentityInspectionRequest{MountPoints: []string{}, ListXattrs: func(got string) ([]string, error) {
					if got == filepath.Join(root, "data") {
						return []string{"system.posix_acl_default"}, nil
					}
					return nil, nil
				}}
			},
			want: "system.posix_acl_default",
		},
		{
			name: "capability",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				path := filepath.Join(root, "data", "file")
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{}, ListXattrs: func(got string) ([]string, error) {
					if got == path {
						return []string{"security.capability"}, nil
					}
					return nil, nil
				}}
			},
			want: "security.capability",
		},
		{
			name: "setid",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				path := filepath.Join(root, "data", "file")
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{}, FileMode: func(got string, mode os.FileMode) os.FileMode {
					if got == path {
						return mode | os.ModeSetuid
					}
					return mode
				}}
			},
			want: "setuid or setgid",
		},
		{
			name: "device boundary",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				path := filepath.Join(root, "data", "device")
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{}, Metadata: func(got string, info os.FileInfo) (serviceIdentityInodeMetadata, error) {
					meta, err := serviceIdentityMetadata(info)
					if got == path {
						meta.Dev++
					}
					return meta, err
				}}
			},
			want: "device boundary",
		},
		{
			name: "special file",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				path := filepath.Join(root, "data", "pipe")
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Skipf("mkfifo unavailable: %v", err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{}}
			},
			want: "special filesystem object",
		},
		{
			name: "external hardlink",
			prepare: func(t *testing.T, root string) serviceIdentityInspectionRequest {
				path := filepath.Join(root, "data", "file")
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(path, filepath.Join(t.TempDir(), "external-link")); err != nil {
					t.Fatal(err)
				}
				return serviceIdentityInspectionRequest{MountPoints: []string{}}
			},
			want: "hard-linked path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "api")
			if err := os.MkdirAll(filepath.Join(root, "data"), 0o750); err != nil {
				t.Fatal(err)
			}
			req := tt.prepare(t, root)
			req.Root = root
			req.Target = db.ServiceIdentity{UID: uint32(os.Geteuid() + 1), GID: uint32(os.Getegid() + 1)}
			if req.ListXattrs == nil {
				req.ListXattrs = func(string) ([]string, error) { return nil, nil }
			}
			inspection, err := inspectServiceIdentityChange(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("inspectServiceIdentityChange = %#v, %v; want %q", inspection, err, tt.want)
			}
			if len(inspection.Records) != 0 || len(inspection.Mutations) != 0 {
				t.Fatalf("hazard returned partial inventory: %#v", inspection)
			}
		})
	}
}

func TestServiceIdentityInspectionDiscoversMountAndZFSBoundariesByDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	nested := filepath.Join(root, "data", "nested")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	oldMounts := serviceIdentityMountPointsFn
	oldDatasets := serviceIdentityDatasetBoundariesFn
	serviceIdentityMountPointsFn = func() ([]string, error) { return []string{nested}, nil }
	serviceIdentityDatasetBoundariesFn = func(context.Context, zfsCommandRunner, string) ([]serviceIdentityDatasetBoundary, error) {
		return []serviceIdentityDatasetBoundary{{Dataset: "tank/api/nested", MountPoint: nested}}, nil
	}
	t.Cleanup(func() {
		serviceIdentityMountPointsFn = oldMounts
		serviceIdentityDatasetBoundariesFn = oldDatasets
	})

	_, err := inspectServiceIdentityChange(context.Background(), serviceIdentityInspectionRequest{
		Root: root, Dataset: "tank/api", Target: db.ServiceIdentity{UID: 1000, GID: 1000},
		ListXattrs: func(string) ([]string, error) { return nil, nil },
	})
	if err == nil || (!strings.Contains(err.Error(), "mount boundary") && !strings.Contains(err.Error(), "nested ZFS dataset")) {
		t.Fatalf("inspectServiceIdentityChange error = %v, want discovered boundary", err)
	}
}

func TestDiscoverServiceIdentityDatasetBoundariesUsesDefaultRunner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	nested := filepath.Join(root, "data")
	oldRunner := serviceIdentityDefaultZFSRunner
	var calls [][]string
	serviceIdentityDefaultZFSRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "tank/api\t" + root + "\n" + "tank/api/data\t" + nested + "\n", "", nil
	}
	t.Cleanup(func() { serviceIdentityDefaultZFSRunner = oldRunner })

	got, err := discoverServiceIdentityDatasetBoundaries(context.Background(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	want := []serviceIdentityDatasetBoundary{{Dataset: "tank/api/data", MountPoint: nested}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("boundaries = %#v, want %#v", got, want)
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"list", "-H", "-o", "name,mountpoint"}) {
		t.Fatalf("ZFS calls = %#v", calls)
	}
}

func TestParseServiceIdentityXattrBufferSortsAndDropsEmptyNames(t *testing.T) {
	got := parseServiceIdentityXattrBuffer([]byte("user.z\x00\x00user.a\x00"), len("user.z\x00\x00user.a\x00"))
	want := []string{"user.a", "user.z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseServiceIdentityXattrBuffer = %#v, want %#v", got, want)
	}
}

func TestListServiceIdentityXattrsWithHandlesFilesystemResults(t *testing.T) {
	sentinel := errors.New("xattr failed")
	tests := []struct {
		name    string
		list    serviceIdentityLlistxattr
		want    []string
		wantErr error
	}{
		{
			name: "sorted names",
			list: func(_ string, dest []byte) (int, error) {
				value := []byte("user.z\x00user.a\x00")
				if dest != nil {
					copy(dest, value)
				}
				return len(value), nil
			},
			want: []string{"user.a", "user.z"},
		},
		{name: "empty", list: func(string, []byte) (int, error) { return 0, nil }},
		{name: "unsupported", list: func(string, []byte) (int, error) { return 0, unix.ENOTSUP }},
		{name: "no data", list: func(string, []byte) (int, error) { return 0, unix.ENODATA }},
		{name: "size error", list: func(string, []byte) (int, error) { return 0, sentinel }, wantErr: sentinel},
		{
			name: "read error",
			list: func(_ string, dest []byte) (int, error) {
				if dest == nil {
					return 8, nil
				}
				return 0, sentinel
			},
			wantErr: sentinel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := listServiceIdentityXattrsWith("ignored", tt.list)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("listServiceIdentityXattrsWith error = %v, want %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("listServiceIdentityXattrsWith = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNativeServiceIdentityMutationTargetClassifiesManagedPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "srv", "api")
	target := db.ServiceIdentity{UID: 1002, GID: 1003}
	tests := []struct {
		name       string
		path       string
		mode       os.FileMode
		want       serviceIdentityMutationTarget
		wantManage bool
	}{
		{name: "root", path: root, mode: os.ModeDir | 0o755, want: serviceIdentityMutationTarget{uid: 0, gid: 1003, mode: 0o750, changeMode: true}, wantManage: true},
		{name: "bin directory", path: filepath.Join(root, "bin"), mode: os.ModeDir | 0o755, want: serviceIdentityMutationTarget{uid: 0, gid: 1003, mode: 0o750, changeMode: true}, wantManage: true},
		{name: "data directory", path: filepath.Join(root, "data"), mode: os.ModeDir | 0o700, want: serviceIdentityMutationTarget{uid: 1002, gid: 1003, mode: 0o750, changeMode: true}, wantManage: true},
		{name: "data descendant", path: filepath.Join(root, "data", "state"), mode: 0o640, want: serviceIdentityMutationTarget{uid: 1002, gid: 1003, mode: 0o640}, wantManage: true},
		{name: "managed binary", path: filepath.Join(root, "bin", "api-1"), mode: 0o777, want: serviceIdentityMutationTarget{uid: 0, gid: 1003, mode: 0o750, changeMode: true}, wantManage: true},
		{name: "managed symlink", path: filepath.Join(root, "env", "env"), mode: os.ModeSymlink | 0o777, want: serviceIdentityMutationTarget{uid: 0, gid: 1003, mode: os.ModeSymlink | 0o777}, wantManage: true},
		{name: "unmanaged binary", path: filepath.Join(root, "bin", "other"), mode: 0o600},
		{name: "unmanaged path", path: filepath.Join(root, "other"), mode: 0o600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, managed := nativeServiceIdentityMutationTarget(root, tt.path, tt.mode, target)
			if managed != tt.wantManage || got != tt.want {
				t.Fatalf("nativeServiceIdentityMutationTarget = %#v, %t; want %#v, %t", got, managed, tt.want, tt.wantManage)
			}
		})
	}
}

func TestNativeServiceLayoutAppliesManagedOwnersAndModes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	for _, dir := range serviceDirectoryPlan(root) {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]os.FileMode{
		filepath.Join(root, "bin", "api-20260718.1"):  0o755,
		filepath.Join(root, "bin", "tailscaled"):      0o755,
		filepath.Join(root, "env", "env"):             0o644,
		filepath.Join(root, "env", "netns.env"):       0o644,
		filepath.Join(root, "env", "tailscaled.env"):  0o644,
		filepath.Join(root, "env", "tailscaled.json"): 0o644,
	}
	for path, mode := range files {
		if err := os.WriteFile(path, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}

	type owner struct{ uid, gid int }
	owners := map[string]owner{}
	oldLchown := nativeServiceLchown
	nativeServiceLchown = func(path string, uid, gid int) error {
		owners[path] = owner{uid: uid, gid: gid}
		return nil
	}
	t.Cleanup(func() { nativeServiceLchown = oldLchown })

	id := db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "app", UID: 1002, GID: 1003}
	if err := applyNativeServiceLayout(root, id); err != nil {
		t.Fatalf("applyNativeServiceLayout: %v", err)
	}

	for _, path := range []string{root, filepath.Join(root, "bin"), filepath.Join(root, "env")} {
		if got := owners[path]; got != (owner{uid: 0, gid: 1003}) {
			t.Fatalf("owner %s = %#v, want root:1003", path, got)
		}
	}
	for _, path := range []string{filepath.Join(root, "data"), filepath.Join(root, "run")} {
		if got := owners[path]; got != (owner{uid: 1002, gid: 1003}) {
			t.Fatalf("owner %s = %#v, want 1002:1003", path, got)
		}
	}
	for path := range files {
		if got := owners[path]; got != (owner{uid: 0, gid: 1003}) {
			t.Fatalf("owner %s = %#v, want root:1003", path, got)
		}
	}
	assertNativeLayoutMode(t, root, 0o750)
	assertNativeLayoutMode(t, filepath.Join(root, "data"), 0o750)
	assertNativeLayoutMode(t, filepath.Join(root, "run"), 0o750)
	assertNativeLayoutMode(t, filepath.Join(root, "bin", "api-20260718.1"), 0o750)
	assertNativeLayoutMode(t, filepath.Join(root, "env", "env"), 0o640)
}

func TestNativeServiceLayoutDoesNotFollowManagedSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	for _, dir := range serviceDirectoryPlan(root) {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", "api-20260718.1")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	var touched string
	oldLchown := nativeServiceLchown
	nativeServiceLchown = func(path string, _, _ int) error { touched = path; return nil }
	t.Cleanup(func() { nativeServiceLchown = oldLchown })

	if err := applyNativeServiceLayout(root, db.ServiceIdentity{UID: 1002, GID: 1003}); err != nil {
		t.Fatalf("applyNativeServiceLayout: %v", err)
	}
	if touched != link {
		t.Fatalf("last Lchown path = %q, want symlink %q", touched, link)
	}
	assertNativeLayoutMode(t, external, 0o600)
}

func TestNativeServiceLayoutRejectsManagedDirectorySymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "bin")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"data", "env", "run"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	oldLchown := nativeServiceLchown
	nativeServiceLchown = func(string, int, int) error { return nil }
	t.Cleanup(func() { nativeServiceLchown = oldLchown })
	err := applyNativeServiceLayout(root, db.ServiceIdentity{UID: 1002, GID: 1003})
	if err == nil || !strings.Contains(err.Error(), "managed service directory") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want managed-directory symlink rejection", err)
	}
}

func TestValidateNativeServiceLayoutAcceptsAppliedContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	for _, dir := range serviceDirectoryPlan(root) {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin", "api-20260718.1")
	env := filepath.Join(root, "env", "env")
	if err := os.WriteFile(bin, []byte("x"), 0o550); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("x"), 0o440); err != nil {
		t.Fatal(err)
	}

	id := db.ServiceIdentity{UID: 1002, GID: 1003}
	oldOwner := nativeServiceOwner
	nativeServiceOwner = func(info os.FileInfo) (uint32, uint32, error) {
		if info.IsDir() && (info.Name() == "data" || info.Name() == "run") {
			return id.UID, id.GID, nil
		}
		return 0, id.GID, nil
	}
	t.Cleanup(func() { nativeServiceOwner = oldOwner })

	if err := validateNativeServiceLayout(root, id); err != nil {
		t.Fatalf("validateNativeServiceLayout: %v", err)
	}
}

func assertNativeLayoutMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}
