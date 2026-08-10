// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestValidateServiceSandboxPolicyCanonicalizesAndChecksRequestedAccess(t *testing.T) {
	tests := []struct {
		name       string
		writable   bool
		initial    os.FileMode
		canonical  os.FileMode
		wantAccess []string
		wantErr    string
	}{
		{name: "read only regular file", initial: 0, canonical: 0, wantAccess: []string{"-r", "/canonical/source"}},
		{name: "read only directory", initial: os.ModeDir, canonical: os.ModeDir, wantAccess: []string{"-r", "/canonical/source", "-a", "-x", "/canonical/source"}},
		{name: "writable directory", writable: true, initial: os.ModeDir, canonical: os.ModeDir, wantAccess: []string{"-r", "/canonical/source", "-a", "-w", "/canonical/source", "-a", "-x", "/canonical/source"}},
		{name: "writable regular file", writable: true, initial: 0, canonical: 0, wantErr: "writable sandbox source /canonical/source must be a directory"},
		{name: "device", initial: os.ModeDevice, canonical: os.ModeDevice, wantErr: "unsupported file type"},
		{name: "socket", initial: os.ModeSocket, canonical: os.ModeSocket, wantErr: "unsupported file type"},
		{name: "fifo", initial: os.ModeNamedPipe, canonical: os.ModeNamedPipe, wantErr: "unsupported file type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var accessArgs []string
			var accessUID, accessGID uint32
			deps := serviceSandboxValidationDeps{
				lstat: func(path string) (bubblewrapFileStat, error) {
					mode := tt.canonical
					if path == "/source" {
						mode = tt.initial
					}
					return bubblewrapFileStat{mode: mode, dev: 7, ino: 11}, nil
				},
				evalSymlinks: func(string) (string, error) { return "/canonical/source", nil },
				checkAccess: func(_ string, args []string, uid, gid uint32) error {
					accessArgs = append([]string(nil), args...)
					accessUID, accessGID = uid, gid
					return nil
				},
			}
			policy := serviceSandboxPolicy{State: "on"}
			exposure := serviceSandboxExposure{Source: "/source", Destination: "/operator"}
			if tt.writable {
				policy.Writable = []serviceSandboxExposure{exposure}
			} else {
				policy.ReadOnly = []serviceSandboxExposure{exposure}
			}
			got, err := validateServiceSandboxPolicyWith(serviceSandboxPlanRequest{Policy: policy, Payload: "/payload", DataDir: "/data", UID: 123, GID: 456}, true, deps)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				if len(accessArgs) != 0 {
					t.Fatalf("access check ran for rejected source: %#v", accessArgs)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			var gotExposure serviceSandboxExposure
			if tt.writable {
				gotExposure = got.Writable[0]
			} else {
				gotExposure = got.ReadOnly[0]
			}
			if gotExposure.Source != "/canonical/source" {
				t.Fatalf("canonical source = %q", gotExposure.Source)
			}
			if !reflect.DeepEqual(accessArgs, tt.wantAccess) || accessUID != 123 || accessGID != 456 {
				t.Fatalf("access check = %#v UID=%d GID=%d, want %#v UID=123 GID=456", accessArgs, accessUID, accessGID, tt.wantAccess)
			}
		})
	}
}

func TestValidateServiceSandboxPolicyRejectsMissingDanglingInaccessibleAndReplacedSources(t *testing.T) {
	tests := []struct {
		name string
		deps serviceSandboxValidationDeps
		want string
	}{
		{
			name: "missing",
			deps: serviceSandboxValidationDeps{lstat: func(string) (bubblewrapFileStat, error) { return bubblewrapFileStat{}, os.ErrNotExist }},
			want: "source /source is missing",
		},
		{
			name: "dangling symlink",
			deps: serviceSandboxValidationDeps{
				lstat:        func(string) (bubblewrapFileStat, error) { return bubblewrapFileStat{mode: os.ModeSymlink}, nil },
				evalSymlinks: func(string) (string, error) { return "", os.ErrNotExist },
			},
			want: "resolve sandbox source /source",
		},
		{
			name: "inaccessible",
			deps: serviceSandboxValidationDeps{
				lstat: func(string) (bubblewrapFileStat, error) {
					return bubblewrapFileStat{mode: os.ModeDir, dev: 1, ino: 2}, nil
				},
				evalSymlinks: func(string) (string, error) { return "/source", nil },
				checkAccess:  func(string, []string, uint32, uint32) error { return errors.New("exit status 1") },
			},
			want: "UID 23 GID 24 cannot access read-only sandbox source /source",
		},
		{
			name: "replaced after access",
			deps: replacedSandboxSourceDeps(),
			want: "was replaced during access validation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateServiceSandboxPolicyWith(serviceSandboxPlanRequest{
				Policy:  serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{{Source: "/source", Destination: "/operator"}}},
				Payload: "/payload", DataDir: "/data", UID: 23, GID: 24,
			}, true, tt.deps)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func replacedSandboxSourceDeps() serviceSandboxValidationDeps {
	canonicalStats := 0
	return serviceSandboxValidationDeps{
		lstat: func(path string) (bubblewrapFileStat, error) {
			if path == "/source" {
				return bubblewrapFileStat{mode: os.ModeSymlink, dev: 1, ino: 1}, nil
			}
			canonicalStats++
			return bubblewrapFileStat{mode: os.ModeDir, dev: 2, ino: uint64(canonicalStats)}, nil
		},
		evalSymlinks: func(string) (string, error) { return "/canonical", nil },
		checkAccess:  func(string, []string, uint32, uint32) error { return nil },
	}
}

func TestValidateServiceSandboxPolicyKeepsDormantMissingSourcesLexical(t *testing.T) {
	deps := serviceSandboxValidationDeps{
		lstat:        func(string) (bubblewrapFileStat, error) { panic("dormant validation touched filesystem") },
		evalSymlinks: func(string) (string, error) { panic("dormant validation canonicalized source") },
		checkAccess:  func(string, []string, uint32, uint32) error { panic("dormant validation checked access") },
	}
	want := serviceSandboxPolicy{State: "off", ReadOnly: []serviceSandboxExposure{{Source: "/missing/source", Destination: "/operator"}}}
	got, err := validateServiceSandboxPolicyWith(serviceSandboxPlanRequest{Policy: want, Payload: "/payload", DataDir: "/data"}, false, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policy = %#v, want %#v", got, want)
	}
}

func TestValidateServiceSandboxPolicyDestinationCollisions(t *testing.T) {
	tests := []struct {
		name     string
		readOnly []serviceSandboxExposure
		writable []serviceSandboxExposure
		wantErr  string
	}{
		{name: "operator equality", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/shared"}}, writable: []serviceSandboxExposure{{Source: "/b", Destination: "/shared"}}, wantErr: "collides"},
		{name: "operator parent first", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/tree"}, {Source: "/b", Destination: "/tree/child"}}, wantErr: "collides"},
		{name: "operator child first", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/tree/child"}, {Source: "/b", Destination: "/tree"}}, wantErr: "collides"},
		{name: "mandatory equality", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/tmp"}}, wantErr: "mandatory destination /tmp"},
		{name: "mandatory parent", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/etc"}}, wantErr: "mandatory destination /etc/"},
		{name: "mandatory child", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/usr/local"}}, wantErr: "mandatory destination /usr"},
		{name: "data parent", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/srv/app"}}, wantErr: "mandatory destination /srv/app/"},
		{name: "payload child", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/srv/app/bin/payload/child"}}, wantErr: "mandatory destination /srv/app/bin/payload"},
		{name: "etc sibling allowed", readOnly: []serviceSandboxExposure{{Source: "/a", Destination: "/etc/app"}}},
		{name: "nested absent parents allowed", writable: []serviceSandboxExposure{{Source: "/missing", Destination: "/opt/company/app/state"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateServiceSandboxPolicyWith(serviceSandboxPlanRequest{
				Policy:  serviceSandboxPolicy{State: "off", ReadOnly: tt.readOnly, Writable: tt.writable},
				Payload: "/srv/app/bin/payload", DataDir: "/srv/app/data",
			}, false, serviceSandboxValidationDeps{})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected collision: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateServiceSandboxPolicyRejectsEveryMandatoryDestination(t *testing.T) {
	mandatory := []struct {
		name string
		path string
	}{
		{name: "runtime usr", path: "/usr"},
		{name: "runtime bin", path: "/bin"},
		{name: "runtime sbin", path: "/sbin"},
		{name: "runtime lib", path: "/lib"},
		{name: "runtime lib64", path: "/lib64"},
		{name: "loader cache", path: "/etc/ld.so.cache"},
		{name: "loader config", path: "/etc/ld.so.conf"},
		{name: "loader config directory", path: "/etc/ld.so.conf.d"},
		{name: "name service config", path: "/etc/nsswitch.conf"},
		{name: "passwd", path: "/etc/passwd"},
		{name: "group", path: "/etc/group"},
		{name: "hosts", path: "/etc/hosts"},
		{name: "localtime", path: "/etc/localtime"},
		{name: "timezone", path: "/etc/timezone"},
		{name: "os release", path: "/etc/os-release"},
		{name: "certificate directory", path: "/etc/ssl/certs"},
		{name: "openssl config", path: "/etc/ssl/openssl.cnf"},
		{name: "ca config", path: "/etc/ca-certificates.conf"},
		{name: "resolver", path: "/etc/resolv.conf"},
		{name: "payload", path: "/srv/app/bin/payload"},
		{name: "data", path: "/srv/app/data"},
		{name: "proc", path: "/proc"},
		{name: "dev", path: "/dev"},
		{name: "tmp", path: "/tmp"},
		{name: "run", path: "/run"},
	}
	request := serviceSandboxPlanRequest{Payload: "/srv/app/bin/payload", DataDir: "/srv/app/data"}

	for _, entry := range mandatory {
		t.Run(entry.name+" equality", func(t *testing.T) {
			request.Policy = serviceSandboxPolicy{
				State:    "off",
				ReadOnly: []serviceSandboxExposure{{Source: "/source", Destination: entry.path}},
			}
			_, err := validateServiceSandboxPolicyWith(request, false, serviceSandboxValidationDeps{})
			want := "mandatory destination " + entry.path
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want containing %q", err, want)
			}
		})
	}

	directions := []struct {
		name        string
		destination string
	}{
		{name: "runtime ancestor", destination: "/"},
		{name: "runtime descendant", destination: "/usr/local"},
		{name: "fixed etc ancestor", destination: "/etc"},
		{name: "fixed directory descendant", destination: "/etc/ssl/certs/operator"},
		{name: "payload ancestor", destination: "/srv/app/bin"},
		{name: "payload descendant", destination: "/srv/app/bin/payload/child"},
		{name: "data ancestor", destination: "/srv/app"},
		{name: "data descendant", destination: "/srv/app/data/child"},
		{name: "pseudo filesystem descendant", destination: "/proc/1"},
		{name: "temporary filesystem descendant", destination: "/tmp/operator"},
	}
	for _, direction := range directions {
		t.Run(direction.name, func(t *testing.T) {
			request.Policy = serviceSandboxPolicy{
				State:    "off",
				ReadOnly: []serviceSandboxExposure{{Source: "/source", Destination: direction.destination}},
			}
			if _, err := validateServiceSandboxPolicyWith(request, false, serviceSandboxValidationDeps{}); err == nil {
				t.Fatalf("mandatory ancestor/descendant %q was accepted", direction.destination)
			}
		})
	}

	for _, sibling := range []string{"/usr-local", "/etc/application", "/srv/application", "/process", "/device", "/tmp-data", "/runtime"} {
		t.Run("sibling "+sibling, func(t *testing.T) {
			request.Policy = serviceSandboxPolicy{
				State:    "off",
				ReadOnly: []serviceSandboxExposure{{Source: "/source", Destination: sibling}},
			}
			if _, err := validateServiceSandboxPolicyWith(request, false, serviceSandboxValidationDeps{}); err != nil {
				t.Fatalf("sibling destination %q rejected: %v", sibling, err)
			}
		})
	}
}

func TestBuildServiceSandboxPlanUsesExactOrderedBubblewrapArguments(t *testing.T) {
	present := map[string]bool{"/bin": true, "/lib64": true, "/etc/passwd": true, "/etc/hosts": true}
	deps := serviceSandboxPlanDeps{
		validation: serviceSandboxValidationDeps{
			lstat: func(path string) (bubblewrapFileStat, error) {
				mode := os.FileMode(0)
				if path == "/host/rw" {
					mode = os.ModeDir
				}
				return bubblewrapFileStat{mode: mode, dev: 9, ino: uint64(len(path))}, nil
			},
			evalSymlinks: func(path string) (string, error) { return path, nil },
			checkAccess:  func(string, []string, uint32, uint32) error { return nil },
		},
		pathPresent: func(path string) bool { return present[path] },
	}
	req := serviceSandboxPlanRequest{
		Service: "api", Hostname: strings.Repeat("h", 63), UID: 123, GID: 456,
		Payload: "/srv/api/bin/payload", DataDir: "/srv/api/data", ResolverSource: "/run/api/resolv.conf",
		Policy: serviceSandboxPolicy{State: "on",
			ReadOnly: []serviceSandboxExposure{{Source: "/host/ro", Destination: "/z/read only"}},
			Writable: []serviceSandboxExposure{{Source: "/host/rw", Destination: "/a/state"}},
		},
	}
	got, err := buildServiceSandboxPlanWith(req, deps)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--disable-userns",
		"--uid", "123", "--gid", "456", "--hostname", strings.Repeat("h", 63), "--new-session", "--die-with-parent",
		"--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin", "--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc/passwd", "/etc/passwd", "--ro-bind", "/etc/hosts", "/etc/hosts",
		"--ro-bind", "/run/api/resolv.conf", "/etc/resolv.conf",
		"--ro-bind", "/srv/api/bin/payload", "/srv/api/bin/payload",
		"--bind", "/srv/api/data", "/srv/api/data",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run",
		"--bind", "/host/rw", "/a/state", "--ro-bind", "/host/ro", "/z/read only",
		"--chdir", "/srv/api/data", "--",
	}
	if got.Executable != "/usr/bin/bwrap" || got.WorkingDirectory != req.DataDir || got.HomeDirectory != req.DataDir {
		t.Fatalf("plan metadata = %#v", got)
	}
	if !reflect.DeepEqual(got.Arguments, wantArgs) {
		t.Fatalf("arguments:\n got %#v\nwant %#v", got.Arguments, wantArgs)
	}
	joined := strings.Join(got.Arguments, " ")
	for _, forbidden := range []string{"--clearenv", "--unshare-net", "--unshare-all", "--unshare-cgroup"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("arguments contain forbidden value %q: %s", forbidden, joined)
		}
	}
	if got.Arguments[len(got.Arguments)-1] != "--" {
		t.Fatalf("plan appended payload after policy separator: %#v", got.Arguments)
	}
	if len(got.Mounts) == 0 || got.Mounts[len(got.Mounts)-1].Destination != "/z/read only" {
		t.Fatalf("normalized mounts = %#v", got.Mounts)
	}
	for _, mount := range got.Mounts {
		if mount.Kind != "bind" && mount.Kind != "proc" && mount.Kind != "dev" && mount.Kind != "tmpfs" {
			t.Fatalf("mount %s has non-operation kind %q", mount.Destination, mount.Kind)
		}
	}
}

func TestBuildServiceSandboxPlanRejectsMissingResolverSource(t *testing.T) {
	_, err := buildServiceSandboxPlanWith(serviceSandboxPlanRequest{
		Policy: serviceSandboxPolicy{State: "on"}, Payload: "/payload", DataDir: "/data", Hostname: "api",
	}, serviceSandboxPlanDeps{
		validation:  serviceSandboxValidationDeps{},
		pathPresent: func(string) bool { return false },
	})
	if err == nil || !strings.Contains(err.Error(), "resolver source") {
		t.Fatalf("error = %v, want resolver source error", err)
	}
}

func FuzzServiceSandboxDestinationCollisions(f *testing.F) {
	for _, seed := range [][2]string{
		{"/etc/app", "/etc/passwd"}, {"/tree", "/tree/child"}, {"/tmp", "/other"}, {"relative", "/valid"}, {"/dirty/../path", "/valid"}, {"/", "/unicode/雪"}, {"/shell;$path", "/sibling"}, {"/with:colon", "/clean"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, first, second string) {
		request := serviceSandboxPlanRequest{
			Policy:  serviceSandboxPolicy{State: "off", ReadOnly: []serviceSandboxExposure{{Source: "/source/one", Destination: first}, {Source: "/source/two", Destination: second}}},
			Payload: "/srv/app/bin/payload", DataDir: "/srv/app/data",
		}
		_, err := validateServiceSandboxPolicyWith(request, false, serviceSandboxValidationDeps{})
		if !sandboxFuzzCleanAbsolute(first) || !sandboxFuzzCleanAbsolute(second) {
			if err == nil {
				t.Fatalf("invalid lexical destinations accepted: %q %q", first, second)
			}
			return
		}
		mandatory := []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d", "/etc/nsswitch.conf", "/etc/passwd", "/etc/group", "/etc/hosts", "/etc/localtime", "/etc/timezone", "/etc/os-release", "/etc/ssl/certs", "/etc/ssl/openssl.cnf", "/etc/ca-certificates.conf", "/etc/resolv.conf", "/srv/app/bin/payload", "/srv/app/data", "/proc", "/dev", "/tmp", "/run"}
		wantCollision := sandboxFuzzOverlap(first, second)
		for _, destination := range []string{first, second} {
			for _, fixed := range mandatory {
				wantCollision = wantCollision || sandboxFuzzOverlap(destination, fixed)
			}
		}
		if wantCollision != (err != nil) {
			t.Fatalf("collision mismatch for %q and %q: want=%t error=%v", first, second, wantCollision, err)
		}
	})
}

func sandboxFuzzCleanAbsolute(path string) bool {
	if path == "" || path[0] != '/' || strings.Contains(path, ":") || strings.ContainsRune(path, '\x00') {
		return false
	}
	if path == "/" {
		return true
	}
	parts := strings.Split(path, "/")
	for index, part := range parts {
		if index > 0 && (part == "" || part == "." || part == "..") {
			return false
		}
	}
	return true
}

func sandboxFuzzOverlap(left, right string) bool {
	if left == right || left == "/" || right == "/" {
		return true
	}
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
