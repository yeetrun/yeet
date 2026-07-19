// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

type serviceIdentitySystemdProperties struct {
	User        string
	Group       string
	Environment map[string]string
	MainPID     int
}

var (
	readServiceIdentitySystemdProperties = loadServiceIdentitySystemdProperties
	readServiceIdentityProcStatus        = os.ReadFile
)

func verifyEffectiveServiceIdentity(ctx context.Context, service string, identity db.ServiceIdentity, root string, expectProcess bool) error {
	properties, err := readServiceIdentitySystemdProperties(ctx, service+".service")
	if err != nil {
		return err
	}
	actual, err := resolveServiceIdentity(properties.User + ":" + properties.Group)
	if err != nil {
		return fmt.Errorf("resolve effective systemd identity %s:%s: %w", properties.User, properties.Group, err)
	}
	if actual.Persisted.UID != identity.UID || actual.Persisted.GID != identity.GID {
		return fmt.Errorf(
			"effective systemd identity is %s:%s (UID %d, GID %d), want %s:%s (UID %d, GID %d); remove overriding unit drop-ins",
			properties.User, properties.Group, actual.Persisted.UID, actual.Persisted.GID,
			identity.RequestedUser, identity.RequestedGroup, identity.UID, identity.GID,
		)
	}
	wantEnvironment := map[string]string{
		"HOME": serviceDataDirForRoot(root), "USER": identity.RequestedUser,
		"LOGNAME": identity.RequestedUser, "SHELL": "/bin/sh",
	}
	for key, want := range wantEnvironment {
		if got := properties.Environment[key]; got != want {
			return fmt.Errorf("effective systemd environment %s=%q, want %q; remove overriding unit drop-ins", key, got, want)
		}
	}
	if !expectProcess {
		return nil
	}
	if properties.MainPID <= 0 {
		return fmt.Errorf("effective systemd MainPID is %d, want a running workload", properties.MainPID)
	}
	return verifyServiceIdentityProcess(properties.MainPID, identity)
}

func loadServiceIdentitySystemdProperties(ctx context.Context, unit string) (serviceIdentitySystemdProperties, error) {
	output, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"--property=User", "--property=Group", "--property=Environment", "--property=MainPID", "--no-pager").Output()
	if err != nil {
		return serviceIdentitySystemdProperties{}, fmt.Errorf("inspect effective systemd identity for %s: %w", unit, err)
	}
	properties, err := parseServiceIdentitySystemdProperties(string(output))
	if err != nil {
		return serviceIdentitySystemdProperties{}, fmt.Errorf("parse effective systemd identity for %s: %w", unit, err)
	}
	return properties, nil
}

func parseServiceIdentitySystemdProperties(raw string) (serviceIdentitySystemdProperties, error) {
	properties := serviceIdentitySystemdProperties{Environment: map[string]string{}}
	for _, line := range strings.Split(raw, "\n") {
		if err := parseServiceIdentitySystemdProperty(line, &properties); err != nil {
			return serviceIdentitySystemdProperties{}, err
		}
	}
	if properties.User == "" || properties.Group == "" {
		return serviceIdentitySystemdProperties{}, fmt.Errorf("systemd User and Group are required")
	}
	return properties, nil
}

func parseServiceIdentitySystemdProperty(line string, properties *serviceIdentitySystemdProperties) error {
	key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return nil
	}
	switch key {
	case "User":
		properties.User = value
	case "Group":
		properties.Group = value
	case "Environment":
		parseServiceIdentitySystemdEnvironment(value, properties.Environment)
	case "MainPID":
		pid, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid MainPID %q", value)
		}
		properties.MainPID = pid
	}
	return nil
}

func parseServiceIdentitySystemdEnvironment(value string, environment map[string]string) {
	for _, assignment := range strings.Fields(value) {
		name, environmentValue, found := strings.Cut(assignment, "=")
		if found {
			environment[name] = environmentValue
		}
	}
}

func verifyServiceIdentityProcess(pid int, identity db.ServiceIdentity) error {
	statusPath := filepath.Join("/proc", strconv.Itoa(pid), "status")
	raw, err := readServiceIdentityProcStatus(statusPath)
	if err != nil {
		return fmt.Errorf("read workload credentials from %s: %w", statusPath, err)
	}
	uids, gids, err := parseServiceIdentityProcCredentials(string(raw))
	if err != nil {
		return fmt.Errorf("parse workload credentials from %s: %w", statusPath, err)
	}
	for _, uid := range uids {
		if uid != identity.UID {
			return fmt.Errorf("running workload UID set is %v, want only %d", uids, identity.UID)
		}
	}
	for _, gid := range gids {
		if gid != identity.GID {
			return fmt.Errorf("running workload GID set is %v, want only %d", gids, identity.GID)
		}
	}
	return nil
}

func parseServiceIdentityProcCredentials(raw string) ([]uint32, []uint32, error) {
	var uids, gids []uint32
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 5 || fields[0] != "Uid:" && fields[0] != "Gid:" {
			continue
		}
		values := make([]uint32, 0, 4)
		for _, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 32)
			if err != nil {
				return nil, nil, err
			}
			values = append(values, uint32(value))
		}
		if fields[0] == "Uid:" {
			uids = values
		} else {
			gids = values
		}
	}
	if len(uids) != 4 || len(gids) != 4 {
		return nil, nil, fmt.Errorf("uid and gid rows are required")
	}
	return uids, gids, nil
}
