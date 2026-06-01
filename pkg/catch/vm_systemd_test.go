// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"
)

func TestRenderVMSystemdUnit(t *testing.T) {
	unit := renderVMSystemdUnit(vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		Firecracker:      "/srv/images/firecracker",
		ConfigPath:       "/srv/vms/devbox/run/firecracker.json",
		APISocket:        "/srv/vms/devbox/run/firecracker.sock",
		ConsoleSocket:    "/srv/vms/devbox/run/serial.sock",
		WorkingDirectory: "/srv/vms/devbox",
	})
	for _, want := range []string{
		"[Unit]",
		"Description=yeet VM devbox",
		"ExecStartPre=/bin/rm -f /srv/vms/devbox/run/firecracker.sock /srv/vms/devbox/run/serial.sock",
		"ExecStart=/srv/catch/run/catch vm-run --firecracker /srv/images/firecracker --api-sock /srv/vms/devbox/run/firecracker.sock --config-file /srv/vms/devbox/run/firecracker.json --console-sock /srv/vms/devbox/run/serial.sock",
		"Restart=on-failure",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}
