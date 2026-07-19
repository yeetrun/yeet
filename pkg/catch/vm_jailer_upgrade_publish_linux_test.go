// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package catch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPublishOpenVMJailerNoReplaceUsesOpenStagedFD(t *testing.T) {
	targetDir := t.TempDir()
	dir, err := os.Open(targetDir)
	if err != nil {
		t.Fatalf("open target dir: %v", err)
	}
	defer func() { _ = dir.Close() }()
	temp, tempName, err := createVMJailerTempAt(dir)
	if err != nil {
		t.Fatalf("create staged jailer: %v", err)
	}
	defer func() { _ = temp.Close() }()
	original := []byte("verified staged inode")
	if _, err := temp.Write(original); err != nil {
		t.Fatalf("write staged jailer: %v", err)
	}
	boundName := "verified-inode-link"
	if err := unix.Linkat(int(dir.Fd()), tempName, int(dir.Fd()), boundName, 0); err != nil {
		t.Fatalf("preserve verified inode link: %v", err)
	}
	if err := unix.Unlinkat(int(dir.Fd()), tempName, 0); err != nil {
		t.Fatalf("unlink original temp name: %v", err)
	}
	replacement := []byte("attacker temp-name replacement")
	if err := os.WriteFile(filepath.Join(targetDir, tempName), replacement, 0o600); err != nil {
		t.Fatalf("replace temp name: %v", err)
	}

	err = publishOpenVMJailerNoReplace(dir, temp, tempName, "jailer")
	if errors.Is(err, unix.EPERM) && os.Geteuid() != 0 {
		t.Skip("linkat AT_EMPTY_PATH requires CAP_DAC_READ_SEARCH")
	}
	if err != nil {
		t.Fatalf("publish open staged fd: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(targetDir, "jailer"))
	if err != nil {
		t.Fatalf("read published jailer: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("published jailer = %q, want open staged inode %q", got, original)
	}
	gotReplacement, err := os.ReadFile(filepath.Join(targetDir, tempName))
	if err != nil {
		t.Fatalf("read replacement temp name: %v", err)
	}
	if !bytes.Equal(gotReplacement, replacement) {
		t.Fatalf("replacement temp = %q, want %q", gotReplacement, replacement)
	}
}
