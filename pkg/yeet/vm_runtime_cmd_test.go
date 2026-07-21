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
	"strings"
	"testing"
	"time"
)

func TestHandleSvcVMRuntimeImportStreamsDeterministicRequiredFiles(t *testing.T) {
	dir := writeVMRuntimeImportFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write extra file: %v", err)
	}

	oldExec := execRemoteFn
	t.Cleanup(func() { execRemoteFn = oldExec })
	var gotService string
	var gotArgs []string
	var gotTTY bool
	var payloads [][]byte
	execRemoteFn = func(_ context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		payload, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		gotService = service
		gotArgs = append([]string(nil), args...)
		gotTTY = tty
		payloads = append(payloads, payload)
		return nil
	}

	for range 2 {
		err := handleSvcVM(context.Background(), svcCommandRequest{
			Command: svcCommand{RawArgs: []string{"vm", "runtime", "import", "local-v1", dir}},
			Service: "untrusted-override",
		})
		if err != nil {
			t.Fatalf("handleSvcVM: %v", err)
		}
	}

	if gotService != systemServiceName {
		t.Fatalf("service = %q, want %s", gotService, systemServiceName)
	}
	wantArgs := []string{"vm", "runtime", "--", "import", "local-v1", vmRuntimeImportStdinPath}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotTTY {
		t.Fatal("tty = true, want false")
	}
	if len(payloads) != 2 || !bytes.Equal(payloads[0], payloads[1]) {
		t.Fatal("runtime import tar stream is not deterministic")
	}

	tr := tar.NewReader(bytes.NewReader(payloads[0]))
	wantNames := []string{"runtime-manifest.json", "firecracker", "jailer"}
	wantModes := []int64{0o644, 0o755, 0o755}
	wantContents := []string{"manifest", "firecracker-binary", "jailer-binary"}
	for i := range wantNames {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("tar entry %d: %v", i, err)
		}
		if hdr.Name != wantNames[i] || hdr.Mode != wantModes[i] || hdr.Typeflag != tar.TypeReg {
			t.Fatalf("header %d = name %q mode %#o type %d", i, hdr.Name, hdr.Mode, hdr.Typeflag)
		}
		if hdr.Uid != 0 || hdr.Gid != 0 || !hdr.ModTime.Equal(time.Unix(0, 0).UTC()) {
			t.Fatalf("header %s metadata = uid %d gid %d mtime %s", hdr.Name, hdr.Uid, hdr.Gid, hdr.ModTime)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		if string(content) != wantContents[i] {
			t.Fatalf("content %s = %q, want %q", hdr.Name, content, wantContents[i])
		}
	}
	if hdr, err := tr.Next(); err != io.EOF {
		t.Fatalf("extra tar entry %#v, error %v", hdr, err)
	}
}

func TestHandleSvcVMRuntimeImportRejectsInvalidDirectoryAndFiles(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		err := callVMRuntimeImport(t, filepath.Join(t.TempDir(), "missing"))
		if err == nil || !strings.Contains(err.Error(), "directory does not exist") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("non directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runtime")
		if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		err := callVMRuntimeImport(t, path)
		if err == nil || !strings.Contains(err.Error(), "must be a directory") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing required file", func(t *testing.T) {
		dir := writeVMRuntimeImportFixture(t)
		if err := os.Remove(filepath.Join(dir, "jailer")); err != nil {
			t.Fatalf("remove jailer: %v", err)
		}
		err := callVMRuntimeImport(t, dir)
		if err == nil || !strings.Contains(err.Error(), "missing required file jailer") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("required file symlink", func(t *testing.T) {
		dir := writeVMRuntimeImportFixture(t)
		jailer := filepath.Join(dir, "jailer")
		if err := os.Remove(jailer); err != nil {
			t.Fatalf("remove jailer: %v", err)
		}
		if err := os.Symlink("firecracker", jailer); err != nil {
			t.Fatalf("symlink jailer: %v", err)
		}
		err := callVMRuntimeImport(t, dir)
		if err == nil || !strings.Contains(err.Error(), "must be a regular file, not a symlink") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestHandleSvcVMRuntimeImportClosesProducerWhenRemoteReturnsEarly(t *testing.T) {
	dir := writeVMRuntimeImportFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "firecracker"), bytes.Repeat([]byte("x"), 2<<20), 0o755); err != nil {
		t.Fatalf("write large firecracker: %v", err)
	}

	oldExec := execRemoteFn
	t.Cleanup(func() { execRemoteFn = oldExec })
	wantErr := errors.New("remote rejected import")
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error {
		return wantErr
	}

	done := make(chan error, 1)
	go func() {
		done <- handleSvcVM(context.Background(), svcCommandRequest{
			Command: svcCommand{RawArgs: []string{"vm", "runtime", "import", "local-v1", dir}},
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime import hung after remote returned without reading stdin")
	}
}

func TestHandleSvcVMRuntimeImportRecognizesActionAfterTerminator(t *testing.T) {
	dir := writeVMRuntimeImportFixture(t)
	oldExec := execRemoteFn
	t.Cleanup(func() { execRemoteFn = oldExec })
	called := false
	execRemoteFn = func(_ context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		called = true
		_, err := io.Copy(io.Discard, stdin)
		return err
	}
	err := handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "runtime", "--", "import", "local-v1", dir}},
	})
	if err != nil {
		t.Fatalf("handleSvcVM: %v", err)
	}
	if !called {
		t.Fatal("runtime import was not handled locally")
	}
}

func writeVMRuntimeImportFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"runtime-manifest.json": "manifest",
		"firecracker":           "firecracker-binary",
		"jailer":                "jailer-binary",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func callVMRuntimeImport(t *testing.T, dir string) error {
	t.Helper()
	oldExec := execRemoteFn
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error {
		t.Fatal("execRemoteFn called for invalid runtime import")
		return nil
	}
	defer func() { execRemoteFn = oldExec }()
	return handleSvcVM(context.Background(), svcCommandRequest{
		Command: svcCommand{RawArgs: []string{"vm", "runtime", "import", "local-v1", dir}},
	})
}
