// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func execRequestPermissions(req catchrpc.ExecRequest) (permissionSet, error) {
	switch req.Target {
	case catchrpc.ExecTargetHostShell, catchrpc.ExecTargetServiceShell:
		return newPermissionSet(permissionSSH), nil
	case catchrpc.ExecTargetVMSSHProxy:
		return newPermissionSet(permissionRead), nil
	case catchrpc.ExecTargetServiceCommand:
		return ttyCommandPermissions(req.Args)
	default:
		return nil, fmt.Errorf("unclassified exec target %q", req.Target)
	}
}

func ttyCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified empty command")
	}
	switch args[0] {
	case "events", "ip", "logs", "status", "version":
		return newPermissionSet(permissionRead), nil
	case "docker":
		return dockerCommandPermissions(args[1:])
	case "snapshots":
		return snapshotsCommandPermissions(args[1:])
	case "service":
		return serviceCommandPermissions(args[1:])
	case "tailscale", "ts":
		return tailscaleCommandPermissions(args[1:])
	case "vm":
		return vmCommandPermissions(args[1:])
	case "cron", "disable", "edit", "enable", "mount", "umount", "env", "remove", "restart", "run", "copy", "stage", "start", "stop":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified command %q", args[0])
	}
}

func dockerCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified docker command")
	}
	switch args[0] {
	case "outdated":
		return newPermissionSet(permissionRead), nil
	case "pull", "update":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified docker command %q", args[0])
	}
}

func snapshotsCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified snapshots command")
	}
	if args[0] == "defaults" {
		return snapshotsDefaultsCommandPermissions(args[1:])
	}
	switch args[0] {
	case "list", "inspect":
		return newPermissionSet(permissionRead), nil
	case "create", "clone", "restore", "rm", "protect", "unprotect":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified snapshots command %q", args[0])
	}
}

func snapshotsDefaultsCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified snapshots defaults command")
	}
	switch args[0] {
	case "show":
		return newPermissionSet(permissionRead), nil
	case "set":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified snapshots defaults command %q", args[0])
	}
}

func serviceCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified service command")
	}
	switch args[0] {
	case "generations":
		return newPermissionSet(permissionRead), nil
	case "set", "rollback":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified service command %q", args[0])
	}
}

func tailscaleCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified tailscale command")
	}
	switch args[0] {
	case "status":
		return newPermissionSet(permissionRead), nil
	case "update", "serve", "set":
		return newPermissionSet(permissionManage), nil
	case "--":
		if len(args) > 1 && args[1] == "status" {
			return newPermissionSet(permissionRead), nil
		}
		return newPermissionSet(permissionManage), nil
	default:
		return newPermissionSet(permissionManage), nil
	}
}

func vmCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified vm command")
	}
	switch args[0] {
	case "images":
		return vmImagesCommandPermissions(args[1:])
	case "memory":
		if len(args) == 1 {
			return newPermissionSet(permissionRead), nil
		}
		return newPermissionSet(permissionManage), nil
	case "console", "set", "kernel":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified vm command %q", args[0])
	}
}

func vmImagesCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return newPermissionSet(permissionRead), nil
	}
	switch args[0] {
	case "ls", "catalog":
		return newPermissionSet(permissionRead), nil
	case "update", "import", "rm", "prune":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified vm images command %q", args[0])
	}
}
