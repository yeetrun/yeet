// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestServiceSetRootRegistersTTYCommand(t *testing.T) {
	if ttyCommandHandlers["service"] == nil {
		t.Fatal(`expected tty command handler for "service"`)
	}
}

func TestServiceSetRootRejectsServiceCommandSyntax(t *testing.T) {
	execer := &ttyExecer{}
	for _, tt := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "missing subcommand", args: []string{}, wantErr: "service requires a command"},
		{name: "unknown subcommand", args: []string{"bogus"}, wantErr: `unknown service command "bogus"`},
		{name: "extra set args", args: []string{"set", "--service-root", "/srv/api", "extra"}, wantErr: "unexpected service set args: extra"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := execer.serviceCmdFunc(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("serviceCmdFunc error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestServiceSetRootRejectsMissingService(t *testing.T) {
	server := newTestServer(t)
	newRoot := filepath.Join(t.TempDir(), "new-root")

	_, err := server.validateServiceRootMigration("missing", serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), `service "missing" not found`) {
		t.Fatalf("validateServiceRootMigration error = %v, want missing service", err)
	}
}

func TestServiceSetRootRejectsRunningService(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	newRoot := filepath.Join(t.TempDir(), "new-root")
	withServiceSetRootRunningCheck(t, func(*Server, string) (bool, error) {
		return true, nil
	})

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), `cannot migrate service root while "svc-root" is running`) {
		t.Fatalf("validateServiceRootMigration error = %v, want running service", err)
	}
}

func TestServiceSetRootRejectsMissingParent(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	withServiceSetRootStopped(t)
	newRoot := filepath.Join(t.TempDir(), "missing-parent", "new-root")

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), "service root parent") {
		t.Fatalf("validateServiceRootMigration error = %v, want missing parent", err)
	}
}

func TestServiceSetRootRejectsNonEmptyDestination(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	withServiceSetRootStopped(t)
	newRoot := filepath.Join(t.TempDir(), "new-root")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatalf("mkdir new root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "file.txt"), []byte("occupied"), 0o644); err != nil {
		t.Fatalf("write destination file: %v", err)
	}

	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("validateServiceRootMigration error = %v, want non-empty destination", err)
	}
}

func TestServiceSetRootRejectsNestedRoots(t *testing.T) {
	for _, tt := range []struct {
		name  string
		roots func(string) (oldRoot, newRoot string)
	}{
		{
			name: "new inside old",
			roots: func(base string) (string, string) {
				oldRoot := filepath.Join(base, "old-root")
				return oldRoot, filepath.Join(oldRoot, "nested")
			},
		},
		{
			name: "old inside new",
			roots: func(base string) (string, string) {
				newRoot := filepath.Join(base, "parent")
				return filepath.Join(newRoot, "old-root"), newRoot
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			oldRoot, newRoot := tt.roots(t.TempDir())
			name := seedServiceWithRoot(t, server, oldRoot, "")
			withServiceSetRootStopped(t)
			if err := os.MkdirAll(oldRoot, 0o755); err != nil {
				t.Fatalf("mkdir old root: %v", err)
			}

			_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: newRoot})
			if err == nil || !strings.Contains(err.Error(), "nested") {
				t.Fatalf("validateServiceRootMigration error = %v, want nested root rejection", err)
			}
		})
	}
}

func TestServiceSetRootNonTTYRequiresCopyOrEmpty(t *testing.T) {
	server := newTestServer(t)
	name := seedServiceWithRoot(t, server, "", "")
	withServiceSetRootStopped(t)
	newRoot := filepath.Join(t.TempDir(), "new-root")
	execer := &ttyExecer{
		ctx:   context.Background(),
		s:     server,
		sn:    name,
		rw:    &bytes.Buffer{},
		isPty: false,
	}

	err := execer.serviceCmdFunc([]string{"set", "--service-root", newRoot})
	if err == nil || !strings.Contains(err.Error(), "requires --copy or --empty") {
		t.Fatalf("serviceSetCmdFunc error = %v, want non-TTY prompt error", err)
	}
}

func TestServiceSetRootTTYDeclineCreatesEmptyRootWithoutCopy(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatalf("mkdir old root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	var out bytes.Buffer
	execer := &ttyExecer{
		ctx:   context.Background(),
		s:     server,
		sn:    name,
		rw:    readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
		isPty: true,
	}

	if err := execer.serviceCmdFunc([]string{"set", "--service-root", newRoot}); err != nil {
		t.Fatalf("serviceCmdFunc: %v", err)
	}
	if !strings.Contains(out.String(), "Copy existing service files") {
		t.Fatalf("prompt output = %q, want copy prompt", out.String())
	}
	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(oldRoot, "old.txt"), "old")
	assertServiceLayout(t, newRoot)
	if _, err := os.Stat(filepath.Join(newRoot, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("copied old file stat error = %v, want not exist", err)
	}
}

func TestServiceSetRootCopyStagesRenamesUpdatesDBAndLeavesOldRoot(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(filepath.Join(oldRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir old data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "data", "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(oldRoot, "data", "payload.txt"), "payload")
	assertFileContents(t, filepath.Join(newRoot, "data", "payload.txt"), "payload")
	assertServiceLayout(t, newRoot)
	assertNoServiceSetStages(t, filepath.Dir(newRoot))
}

func TestServiceSetRootMigrationUsesFreshValidatedRoot(t *testing.T) {
	server := newTestServer(t)
	staleRoot := filepath.Join(t.TempDir(), "stale-root")
	name := seedServiceWithRoot(t, server, staleRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(staleRoot, 0o755); err != nil {
		t.Fatalf("mkdir stale root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale payload: %v", err)
	}
	currentRoot := filepath.Join(t.TempDir(), "current-root")
	if err := os.MkdirAll(currentRoot, 0o755); err != nil {
		t.Fatalf("mkdir current root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(currentRoot, "current.txt"), []byte("current"), 0o644); err != nil {
		t.Fatalf("write current payload: %v", err)
	}
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Services[name].ServiceRoot = currentRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate current service root: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(newRoot, "current.txt"), "current")
	if _, err := os.Stat(filepath.Join(newRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale payload stat error = %v, want not exist", err)
	}
}

func TestServiceSetRootRenameFailureLeavesDBOldRoot(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatalf("mkdir old root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")
	wantErr := errors.New("rename failed")
	withServiceSetRootRename(t, func(string, string) error {
		return wantErr
	})

	err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy)
	if !errors.Is(err, wantErr) {
		t.Fatalf("migrateServiceRoot error = %v, want %v", err, wantErr)
	}
	assertServiceRoot(t, server, name, oldRoot)
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("new root stat error = %v, want not exist", err)
	}
	assertNoServiceSetStages(t, filepath.Dir(newRoot))
}

func TestServiceSetRootEmptyCreatesLayoutUpdatesDBWithoutCopyAndLeavesOldRoot(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatalf("mkdir old root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationEmpty); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	assertServiceRoot(t, server, name, newRoot)
	assertFileContents(t, filepath.Join(oldRoot, "payload.txt"), "payload")
	assertServiceLayout(t, newRoot)
	if _, err := os.Stat(filepath.Join(newRoot, "payload.txt")); !os.IsNotExist(err) {
		t.Fatalf("copied payload stat error = %v, want not exist", err)
	}
}

func TestServiceSetRootCopyPreservesModeMtimeAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink metadata test is Unix-oriented")
	}
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old-root")
	name := seedServiceWithRoot(t, server, oldRoot, "")
	withServiceSetRootStopped(t)
	dataDir := filepath.Join(oldRoot, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	filePath := filepath.Join(dataDir, "payload.txt")
	if err := os.WriteFile(filePath, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(filePath, mtime, mtime); err != nil {
		t.Fatalf("chtimes payload: %v", err)
	}
	if err := os.Symlink("payload.txt", filepath.Join(dataDir, "payload.link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-root")

	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: newRoot}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}

	copied := filepath.Join(newRoot, "data", "payload.txt")
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("copied mode = %o, want 0640", info.Mode().Perm())
	}
	if info.ModTime().Unix() != mtime.Unix() {
		t.Fatalf("copied mtime = %v, want %v", info.ModTime(), mtime)
	}
	target, err := os.Readlink(filepath.Join(newRoot, "data", "payload.link"))
	if err != nil {
		t.Fatalf("readlink copied symlink: %v", err)
	}
	if target != "payload.txt" {
		t.Fatalf("copied symlink target = %q, want payload.txt", target)
	}
}

func TestServiceSetZFSMigrationCopy(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	withServiceSetRootStopped(t)
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "config.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot, Exists: true},
	}).Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}
	if err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true}, serviceRootMigrationCopy); err != nil {
		t.Fatalf("migrateServiceRoot: %v", err)
	}
	assertServiceRoot(t, server, name, newRoot)
	assertServiceRootZFS(t, server, name, "tank/apps/svc")
	if got, err := os.ReadFile(filepath.Join(serviceDataDirForRoot(newRoot), "config.txt")); err != nil || string(got) != "ok" {
		t.Fatalf("copied config = %q err=%v, want ok nil", got, err)
	}
}

func TestServiceSetZFSMigrationCreatesDatasetAndLeavesDBOnCopyFailure(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	oldRoot := filepath.Join(t.TempDir(), "old-missing")
	newRoot := filepath.Join(t.TempDir(), "new")
	withServiceSetRootStopped(t)
	runner := fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: newRoot},
	})
	server.zfsRunner = runner.Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = oldRoot
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}
	err := server.migrateServiceRoot(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true}, serviceRootMigrationCopy)
	if err == nil || !strings.Contains(err.Error(), "archive service root") {
		t.Fatalf("migrateServiceRoot error = %v, want archive failure", err)
	}
	if !runner["tank/apps/svc"].Exists {
		t.Fatal("dataset was not created before migration failure")
	}
	assertServiceRoot(t, server, name, oldRoot)
	assertServiceRootZFS(t, server, name, "")
}

func TestServiceSetRejectsNoopAcrossRootTypes(t *testing.T) {
	server := newTestServer(t)
	name := "svc"
	root := filepath.Join(t.TempDir(), "svc")
	withServiceSetRootStopped(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: root, Exists: true},
	}).Run
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceRoot = root
		return nil
	}); err != nil {
		t.Fatalf("mutate service root: %v", err)
	}
	_, err := server.validateServiceRootMigration(name, serviceRootMigrationRequest{Root: "tank/apps/svc", ZFS: true})
	if err == nil || !strings.Contains(err.Error(), "already uses service root") {
		t.Fatalf("validateServiceRootMigration error = %v, want same path different identity rejection", err)
	}
}

func seedServiceWithRoot(t *testing.T, server *Server, root string, nameSuffix string) string {
	t.Helper()
	name := "svc-root" + nameSuffix
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.ServiceRoot = root
		return nil
	}); err != nil {
		t.Fatalf("seed service %q: %v", name, err)
	}
	return name
}

func withServiceSetRootStopped(t *testing.T) {
	t.Helper()
	withServiceSetRootRunningCheck(t, func(*Server, string) (bool, error) {
		return false, nil
	})
}

func withServiceSetRootRunningCheck(t *testing.T, f func(*Server, string) (bool, error)) {
	t.Helper()
	old := isServiceRunningForRootMigration
	isServiceRunningForRootMigration = f
	t.Cleanup(func() {
		isServiceRunningForRootMigration = old
	})
}

func withServiceSetRootRename(t *testing.T, f func(string, string) error) {
	t.Helper()
	old := renameServiceRoot
	renameServiceRoot = f
	t.Cleanup(func() {
		renameServiceRoot = old
	})
}

func assertServiceRoot(t *testing.T, server *Server, name, want string) {
	t.Helper()
	got, err := server.serviceRootDir(name)
	if err != nil {
		t.Fatalf("serviceRootDir: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("service root = %q, want %q", got, want)
	}
}

func assertServiceRootZFS(t *testing.T, server *Server, name, want string) {
	t.Helper()
	d, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	svc, ok := d.Services().GetOk(name)
	if !ok {
		t.Fatalf("service %q missing", name)
	}
	if got := svc.ServiceRootZFS(); got != want {
		t.Fatalf("ServiceRootZFS = %q, want %q", got, want)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s contents = %q, want %q", path, string(got), want)
	}
}

func assertServiceLayout(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"bin", "data", "env", "run"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("stat layout dir %s: %v", name, err)
		}
		if !info.IsDir() {
			t.Fatalf("layout entry %s is not a directory", name)
		}
	}
}

func assertNoServiceSetStages(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yeet-service-root-") {
			t.Fatalf("stage directory %q was not cleaned up", entry.Name())
		}
	}
}
