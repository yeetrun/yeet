// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "fmt"

type vmSystemdConfig struct {
	Service          string
	Runner           string
	Firecracker      string
	ConfigPath       string
	APISocket        string
	ConsoleSocket    string
	WorkingDirectory string
}

func renderVMSystemdUnit(cfg vmSystemdConfig) string {
	return fmt.Sprintf(`[Unit]
Description=yeet VM %s
After=network-online.target yeet-ns.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStartPre=/bin/rm -f %s %s
ExecStart=%s vm-run --firecracker %s --api-sock %s --config-file %s --console-sock %s
Restart=on-failure
RestartSec=1
KillMode=mixed
TimeoutStopSec=10

[Install]
WantedBy=multi-user.target
`, cfg.Service, cfg.WorkingDirectory, cfg.APISocket, cfg.ConsoleSocket, cfg.Runner, cfg.Firecracker, cfg.APISocket, cfg.ConfigPath, cfg.ConsoleSocket)
}
