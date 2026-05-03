// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadCatchReleaseRunsRemoteScript(t *testing.T) {
	scriptFile := filepath.Join(t.TempDir(), "remote-script.sh")
	fakeSSHInPath(t, "/bin/cat > "+strconvQuoteForShell(scriptFile)+"\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	got, err := downloadCatchRelease(ui, "root@example.com", "catch-linux-amd64.tar.gz", "https://example.com/catch.tgz", "https://example.com/catch.sha256")
	if err != nil {
		t.Fatalf("downloadCatchRelease error: %v", err)
	}
	if got != "catch-linux-amd64.tar.gz" {
		t.Fatalf("download detail = %q, want asset name", got)
	}
	b, err := os.ReadFile(scriptFile)
	if err != nil {
		t.Fatalf("ReadFile remote script: %v", err)
	}
	script := string(b)
	for _, want := range []string{
		`fetch "https://example.com/catch.tgz" "$TMP_DIR/catch-linux-amd64.tar.gz"`,
		`fetch "https://example.com/catch.sha256" "$TMP_DIR/catch-linux-amd64.tar.gz.sha256"`,
		`mv -f "$TMP_DIR/catch-linux-amd64" ./catch`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote script missing %q:\n%s", want, script)
		}
	}
}

func TestDownloadCatchReleaseReportsSSHError(t *testing.T) {
	fakeSSHInPath(t, "/bin/cat >/dev/null\nexit 1\n")
	ui := newInitUI(io.Discard, false, true, "catch", "root@example.com", catchServiceName)

	_, err := downloadCatchRelease(ui, "root@example.com", "catch-linux-amd64.tar.gz", "https://example.com/catch.tgz", "https://example.com/catch.sha256")
	if err == nil {
		t.Fatal("downloadCatchRelease error = nil, want ssh error")
	}
}
