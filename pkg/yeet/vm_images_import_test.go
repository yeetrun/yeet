// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHandleSvcVMImagesImportStreamsBundleToCatch(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	var gotService string
	var gotArgs []string
	var gotPayload bytes.Buffer
	var gotTTY bool
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string(nil), args...)
		gotTTY = tty
		if stdin == nil {
			t.Fatal("stdin = nil, want tar stream")
		}
		if _, err := io.Copy(&gotPayload, stdin); err != nil {
			t.Fatalf("copy stdin: %v", err)
		}
		return nil
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", dir}},
		Service: systemServiceName,
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	if gotService != systemServiceName {
		t.Fatalf("service = %q, want %s", gotService, systemServiceName)
	}
	wantArgs := []string{"vm", "images", "import", "foo/bar", "--stdin"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotTTY {
		t.Fatal("tty = true, want false for tar upload")
	}
	assertTarContains(t, gotPayload.Bytes(), "rootfs.ext4")
}

func TestHandleSvcVMImagesImportPassesAllowLocalKernel(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string(nil), args...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", dir, "--allow-local-kernel"}},
		Service: systemServiceName,
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	want := []string{"vm", "images", "import", "foo/bar", "--stdin", "--allow-local-kernel"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestHandleSvcVMImagesImportPassesFormatBeforeImport(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string(nil), args...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "--format=json", "import", "foo/bar", dir}},
		Service: systemServiceName,
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	want := []string{"vm", "images", "import", "foo/bar", "--stdin", "--format=json"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestHandleSvcVMImagesImportRejectsMissingDirectory(t *testing.T) {
	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", filepath.Join(t.TempDir(), "missing")}},
		Service: systemServiceName,
	})
	if err == nil || !strings.Contains(err.Error(), "VM image bundle directory does not exist") {
		t.Fatalf("error = %v, want missing bundle directory", err)
	}
}

func TestHandleSvcVMImagesImportRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rootfs.ext4")
	if err := os.WriteFile(file, []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", file}},
		Service: systemServiceName,
	})
	if err == nil || !strings.Contains(err.Error(), "VM image bundle path must be a directory") {
		t.Fatalf("error = %v, want non-directory bundle path", err)
	}
}

func TestHandleSvcVMImagesImportClosesPipeReaderOnRemoteError(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4"), bytes.Repeat([]byte("rootfs"), 1024), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	wantErr := errors.New("remote failed before reading")
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return wantErr
	}

	done := make(chan error, 1)
	go func() {
		done <- handleSvcVM(context.Background(), svcCommandRequest{
			Command: svcCommand{RawArgs: []string{"vm", "images", "import", "foo/bar", dir}},
			Service: systemServiceName,
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleSvcVM hung after remote returned without reading stdin")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if !goroutineStacksContain("copyutil.TarDirectory") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("tar producer goroutine still blocked after remote error")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleSvcVMImagesImportNonImportDelegatesRemote(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}

	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "images", "ls"}},
		Service: systemServiceName,
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	want := []string{"vm", "images", "ls"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func assertTarContains(t *testing.T, raw []byte, want string) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if hdr.Name == want {
			return
		}
	}
	t.Fatalf("tar missing %q", want)
}

func goroutineStacksContain(needle string) bool {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Contains(string(buf[:n]), needle)
}
