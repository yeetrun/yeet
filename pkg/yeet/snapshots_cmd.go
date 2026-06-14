// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
)

func handleSvcSnapshots(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) == 0 {
		return fmt.Errorf("snapshots requires a command")
	}
	if req.Command.Args[0] == "defaults" {
		return handleSnapshotDefaults(ctx, req)
	}
	if err := validateSnapshotLifecycleCommand(req.Command.Args); err != nil {
		return err
	}
	return execRemoteFn(ctx, systemServiceName, req.Command.RawArgs, nil, false)
}

func validateSnapshotLifecycleCommand(args []string) error {
	switch args[0] {
	case "list":
		_, _, err := cli.ParseSnapshotsList(args[1:])
		return err
	case "inspect":
		_, _, err := cli.ParseSnapshotsInspect(args[1:])
		return err
	case "create":
		_, _, err := cli.ParseSnapshotsCreate(args[1:])
		return err
	case "clone":
		_, _, err := cli.ParseSnapshotsClone(args[1:])
		return err
	case "restore":
		_, _, err := cli.ParseSnapshotsRestore(args[1:])
		return err
	case "rm":
		_, _, err := cli.ParseSnapshotsRemove(args[1:])
		return err
	case "protect", "unprotect":
		_, err := cli.ParseSnapshotsProtect(args[1:], args[0])
		return err
	default:
		return fmt.Errorf("unknown snapshots command %q", args[0])
	}
}

func handleSnapshotDefaults(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) < 2 {
		return fmt.Errorf("snapshots defaults requires a command")
	}
	switch req.Command.Args[1] {
	case "show":
		if _, err := cli.ParseSnapshotDefaultsShow(req.Command.Args[2:]); err != nil {
			return err
		}
	case "set":
		if _, remaining, err := cli.ParseSnapshotDefaultsSet(req.Command.Args[2:]); err != nil {
			return err
		} else if len(remaining) != 0 {
			return fmt.Errorf("unexpected snapshots defaults args: %s", strings.Join(remaining, " "))
		}
	default:
		return fmt.Errorf("unknown snapshots defaults command %q", req.Command.Args[1])
	}
	return execRemoteFn(ctx, systemServiceName, req.Command.RawArgs, nil, false)
}
