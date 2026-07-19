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

func TestValidateHostControlledServiceRootPath(t *testing.T) {
	t.Run("allows Catch-owned ancestors", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "services", "api")
		if err := validateHostControlledServiceRootPath(root); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejects writable parent", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "shared")
		if err := os.Mkdir(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		err := validateHostControlledServiceRootPath(filepath.Join(parent, "api"))
		if err == nil || !strings.Contains(err.Error(), "not host-controlled") {
			t.Fatalf("validation error = %v, want host-controlled diagnostic", err)
		}
	})

	t.Run("rejects symlink component", func(t *testing.T) {
		base := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(base, "services")); err != nil {
			t.Fatal(err)
		}
		err := validateHostControlledServiceRootPath(filepath.Join(base, "services", "api"))
		if err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
			t.Fatalf("validation error = %v, want symlink rejection", err)
		}
	})
}
