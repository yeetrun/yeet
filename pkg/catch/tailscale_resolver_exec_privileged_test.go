// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

const (
	tailscaleResolverExecPrivilegedStageEnv   = "YEET_TAILSCALE_RESOLVER_EXEC_PRIVILEGED_STAGE"
	tailscaleResolverExecPrivilegedResultEnv  = "YEET_TAILSCALE_RESOLVER_EXEC_PRIVILEGED_RESULT"
	tailscaleResolverExecPrivilegedExecDirEnv = "YEET_TAILSCALE_RESOLVER_EXEC_PRIVILEGED_EXEC_DIR"
	tailscaleResolverExecPrivilegedSetupStage = "setup"
)

// TestTailscaleResolverExecPrivileged catches a launcher that execs without a
// read-only resolver overlay from the service network namespace.
func TestTailscaleResolverExecPrivileged(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux root mount-namespace semantics")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root for Linux mount-namespace semantics")
	}

	resultPath := filepath.Join(t.TempDir(), "result")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTailscaleResolverExecPrivilegedHelper$")
	cmd.Env = append(os.Environ(),
		tailscaleResolverExecPrivilegedStageEnv+"="+tailscaleResolverExecPrivilegedSetupStage,
		tailscaleResolverExecPrivilegedResultEnv+"="+resultPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("privileged resolver launcher helper: %v\n%s", err, output)
	}

	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read helper result: %v", err)
	}
	if got, want := string(result), "pass\n"; got != want {
		t.Fatalf("helper result = %q, want %q", got, want)
	}
}
