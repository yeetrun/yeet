// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCatchTailscaleClientSecret(t *testing.T) {
	root := t.TempDir()

	path, err := WriteCatchTailscaleClientSecret(root, " tskey-client-test-secret \n")
	if err != nil {
		t.Fatalf("WriteCatchTailscaleClientSecret returned error: %v", err)
	}
	wantPath := filepath.Join(root, "services", CatchService, "data", "tailscale.key")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(raw) != "tskey-client-test-secret\n" {
		t.Fatalf("stored secret = %q", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteCatchTailscaleClientSecretRejectsInvalidSecret(t *testing.T) {
	_, err := WriteCatchTailscaleClientSecret(t.TempDir(), "not-a-client-secret")
	if err == nil || !strings.Contains(err.Error(), "invalid client secret") {
		t.Fatalf("error = %v, want invalid client secret", err)
	}
}
