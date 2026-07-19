// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// macOS exposes /var as a symlink to /private/var. Canonicalize the test
	// temp root so full-component no-follow checks exercise test artifacts,
	// not that platform alias.
	tempDir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve test temporary directory: %v\n", err)
		os.Exit(1)
	}
	previous, hadPrevious := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "set canonical test temporary directory: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := restoreTestTempDir(previous, hadPrevious); err != nil {
		fmt.Fprintf(os.Stderr, "restore test temporary directory: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func restoreTestTempDir(previous string, hadPrevious bool) error {
	if hadPrevious {
		return os.Setenv("TMPDIR", previous)
	}
	return os.Unsetenv("TMPDIR")
}
