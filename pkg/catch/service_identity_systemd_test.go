// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestVerifyEffectiveServiceIdentityRejectsRootDropIn(t *testing.T) {
	stubServiceIdentityLookups(t, map[string]*user.User{
		"app":  {Username: "app", Uid: "1001", Gid: "1001"},
		"root": {Username: "root", Uid: "0", Gid: "0"},
	}, map[string]*user.Group{
		"app":  {Name: "app", Gid: "1001"},
		"root": {Name: "root", Gid: "0"},
	})
	old := readServiceIdentitySystemdProperties
	readServiceIdentitySystemdProperties = func(context.Context, string) (serviceIdentitySystemdProperties, error) {
		return serviceIdentitySystemdProperties{
			User: "root", Group: "root", MainPID: 44,
			Environment: map[string]string{"HOME": "/srv/api/data", "USER": "app", "LOGNAME": "app", "SHELL": "/bin/sh"},
		}, nil
	}
	t.Cleanup(func() { readServiceIdentitySystemdProperties = old })

	err := verifyEffectiveServiceIdentity(context.Background(), "api", db.ServiceIdentity{
		RequestedUser: "app", RequestedGroup: "app", UID: 1001, GID: 1001,
	}, "/srv/api", true)
	if err == nil || !strings.Contains(err.Error(), "remove overriding unit drop-ins") {
		t.Fatalf("verification error = %v, want drop-in diagnostic", err)
	}
}

func TestVerifyEffectiveServiceIdentityChecksEnvironmentAndProcess(t *testing.T) {
	stubServiceIdentityLookups(t, map[string]*user.User{
		"app": {Username: "app", Uid: "1001", Gid: "1002"},
	}, map[string]*user.Group{
		"app": {Name: "app", Gid: "1002"},
	})
	oldProperties, oldStatus := readServiceIdentitySystemdProperties, readServiceIdentityProcStatus
	readServiceIdentitySystemdProperties = func(context.Context, string) (serviceIdentitySystemdProperties, error) {
		return serviceIdentitySystemdProperties{
			User: "app", Group: "app", MainPID: 44,
			Environment: map[string]string{"HOME": "/srv/api/data", "USER": "app", "LOGNAME": "app", "SHELL": "/bin/sh"},
		}, nil
	}
	readServiceIdentityProcStatus = func(path string) ([]byte, error) {
		if path != "/proc/44/status" {
			t.Fatalf("status path = %q", path)
		}
		return []byte("Uid:\t1001\t1001\t1001\t1001\nGid:\t1002\t1002\t1002\t1002\n"), nil
	}
	t.Cleanup(func() {
		readServiceIdentitySystemdProperties, readServiceIdentityProcStatus = oldProperties, oldStatus
	})

	if err := verifyEffectiveServiceIdentity(context.Background(), "api", db.ServiceIdentity{
		RequestedUser: "app", RequestedGroup: "app", UID: 1001, GID: 1002,
	}, "/srv/api", true); err != nil {
		t.Fatal(err)
	}
}

func TestParseServiceIdentitySystemdProperties(t *testing.T) {
	properties, err := parseServiceIdentitySystemdProperties(
		"User=app\nGroup=app\nEnvironment=HOME=/srv/api/data USER=app LOGNAME=app SHELL=/bin/sh\nMainPID=42\n",
	)
	if err != nil {
		t.Fatal(err)
	}
	if properties.MainPID != 42 || properties.Environment["HOME"] != "/srv/api/data" {
		t.Fatalf("properties = %#v", properties)
	}
}

func TestLoadServiceIdentitySystemdProperties(t *testing.T) {
	binDir := t.TempDir()
	systemctl := filepath.Join(binDir, "systemctl")
	writeSystemctl := func(output string) {
		t.Helper()
		script := "#!/bin/sh\nprintf '%b' '" + output + "'\n"
		if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
	writeSystemctl("User=app\\nGroup=app\\nEnvironment=HOME=/srv/api/data USER=app\\nMainPID=42\\n")
	properties, err := loadServiceIdentitySystemdProperties(context.Background(), "api.service")
	if err != nil {
		t.Fatalf("load systemd properties: %v", err)
	}
	if properties.User != "app" || properties.Group != "app" || properties.MainPID != 42 {
		t.Fatalf("systemd properties = %#v", properties)
	}
	writeSystemctl("User=app\\nGroup=app\\nMainPID=bad\\n")
	if _, err := loadServiceIdentitySystemdProperties(context.Background(), "api.service"); err == nil || !strings.Contains(err.Error(), "parse effective") {
		t.Fatalf("invalid systemd properties error = %v", err)
	}
	if err := os.Remove(systemctl); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServiceIdentitySystemdProperties(context.Background(), "api.service"); err == nil || !strings.Contains(err.Error(), "inspect effective") {
		t.Fatalf("missing systemctl error = %v", err)
	}
}
