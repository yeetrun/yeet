// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnsupportedServiceIdentityTransactionXattrsOnlyIgnoresDarwinProvenance(t *testing.T) {
	xattrs := []string{"com.apple.provenance", "security.selinux", "user.operator"}
	for _, tt := range []struct {
		goos string
		want []string
	}{
		{goos: "darwin", want: []string{"security.selinux", "user.operator"}},
		{goos: "linux", want: xattrs},
	} {
		t.Run(tt.goos, func(t *testing.T) {
			got := unsupportedServiceIdentityTransactionXattrsForOS(tt.goos, xattrs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unsupported xattrs = %#v, want %#v", got, tt.want)
			}
		})
	}
	if !reflect.DeepEqual(xattrs, []string{"com.apple.provenance", "security.selinux", "user.operator"}) {
		t.Fatalf("input xattrs mutated: %#v", xattrs)
	}
}

func TestLegacyNativeRuntimeTransactionsRejectRunParentSymlinkSwap(t *testing.T) {
	for _, phase := range []string{"backup", "remove", "restore"} {
		t.Run(phase, func(t *testing.T) {
			server := newTestServer(t)
			if err := os.MkdirAll(filepath.Join(server.cfg.RootDir, "migrations", "service-identity"), 0o700); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "service")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			runDir := serviceRunDirForRoot(root)
			legacyPath := filepath.Join(runDir, "env")
			if err := os.WriteFile(legacyPath, []byte("legacy"), 0o640); err != nil {
				t.Fatal(err)
			}
			backups, err := captureLegacyNativeRuntimeBackups(server.cfg.RootDir, root, "api", "tx-parent-swap-"+phase)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "backup" {
				backups, err = backupLegacyNativeRuntimeArtifacts(server.cfg.RootDir, root, backups)
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == "restore" {
				if err := removeLegacyNativeRuntimeArtifacts(root, backups); err != nil {
					t.Fatal(err)
				}
			}

			realRunDir := runDir + ".real"
			if err := os.Rename(runDir, realRunDir); err != nil {
				t.Fatal(err)
			}
			victimDir := t.TempDir()
			victimPath := filepath.Join(victimDir, "env")
			if err := os.WriteFile(victimPath, []byte("operator"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victimDir, runDir); err != nil {
				t.Fatal(err)
			}

			switch phase {
			case "backup":
				_, err = backupLegacyNativeRuntimeArtifacts(server.cfg.RootDir, root, backups)
			case "remove":
				err = removeLegacyNativeRuntimeArtifacts(root, backups)
			case "restore":
				err = restoreLegacyNativeRuntimeBackup(server.cfg.RootDir, root, backups, "")
			}
			if err == nil || (!strings.Contains(err.Error(), "without following symlinks") && !errors.Is(err, os.ErrNotExist)) {
				t.Fatalf("%s error = %v, want stable-parent rejection", phase, err)
			}
			raw, readErr := os.ReadFile(victimPath)
			if readErr != nil || string(raw) != "operator" {
				t.Fatalf("operator file changed during %s: %q, %v", phase, raw, readErr)
			}
			if phase != "restore" {
				raw, readErr = os.ReadFile(filepath.Join(realRunDir, "env"))
				if readErr != nil || string(raw) != "legacy" {
					t.Fatalf("original runtime changed during %s: %q, %v", phase, raw, readErr)
				}
			}
		})
	}
}
