// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"

	"github.com/yeetrun/yeet/pkg/cli"
)

func handleSvcSnapshots(ctx context.Context, req svcCommandRequest) error {
	if len(req.Command.Args) < 2 || req.Command.Args[0] != "defaults" {
		return handleSvcRemote(ctx, req)
	}
	switch req.Command.Args[1] {
	case "show":
		if _, err := cli.ParseSnapshotDefaultsShow(req.Command.Args[2:]); err != nil {
			return err
		}
	case "set":
		if _, _, err := cli.ParseSnapshotDefaultsSet(req.Command.Args[2:]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown snapshots defaults command %q", req.Command.Args[1])
	}
	return execRemoteFn(ctx, systemServiceName, req.Command.RawArgs, nil, false)
}
