// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatUploadDetail(t *testing.T) {
	tests := []struct {
		name  string
		sent  float64
		total float64
		rate  float64
		want  string
	}{
		{
			name:  "percent with rate and eta",
			sent:  512,
			total: 1024,
			rate:  256,
			want:  "50% 512.00 B/1024.00 B @ 256.00 B/s ETA 2s",
		},
		{
			name: "clamps negative sent",
			sent: -1,
			want: "0.00 B",
		},
		{
			name:  "clamps sent over total",
			sent:  2048,
			total: 1024,
			want:  "100% 1024.00 B/1024.00 B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUploadDetail(tt.sent, tt.total, tt.rate); got != tt.want {
				t.Fatalf("detail = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUploadCatchBinaryCopiesFileWithSSHCommand(t *testing.T) {
	oldCommand := uploadCatchCommandFn
	defer func() { uploadCatchCommandFn = oldCommand }()

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "catch")
	copied := filepath.Join(tmp, "uploaded")
	content := []byte("catch-binary")
	if err := os.WriteFile(bin, content, 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	var gotName string
	var gotArgs []string
	uploadCatchCommandFn = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string{}, args...)
		cmd := exec.Command(os.Args[0], "-test.run=TestUploadCatchBinaryHelper", "--", copied)
		cmd.Env = append(os.Environ(), "YEET_UPLOAD_HELPER=copy")
		return cmd
	}

	ui := newInitUI(io.Discard, false, true, "", "", "")
	detail, err := uploadCatchBinary(ui, bin, int64(len(content)), "root@example.com")
	if err != nil {
		t.Fatalf("uploadCatchBinary error: %v", err)
	}
	if gotName != "ssh" {
		t.Fatalf("command name = %q, want ssh", gotName)
	}
	wantArgs := []string{"-q", "-C", "root@example.com", "cat > ./catch"}
	if strings.Join(gotArgs, "\n") != strings.Join(wantArgs, "\n") {
		t.Fatalf("command args = %#v, want %#v", gotArgs, wantArgs)
	}
	gotContent, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("ReadFile copied error: %v", err)
	}
	if string(gotContent) != string(content) {
		t.Fatalf("copied content = %q, want %q", gotContent, content)
	}
	if !strings.Contains(detail, "12.00 B") {
		t.Fatalf("detail = %q, want uploaded size", detail)
	}
}

func TestUploadCatchBinaryReturnsCommandError(t *testing.T) {
	oldCommand := uploadCatchCommandFn
	defer func() { uploadCatchCommandFn = oldCommand }()

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "catch")
	if err := os.WriteFile(bin, []byte("catch"), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	uploadCatchCommandFn = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestUploadCatchBinaryHelper")
		cmd.Env = append(os.Environ(), "YEET_UPLOAD_HELPER=fail")
		return cmd
	}

	ui := newInitUI(io.Discard, false, true, "", "", "")
	if _, err := uploadCatchBinary(ui, bin, 5, "root@example.com"); err == nil {
		t.Fatal("expected command error")
	}
}

func TestUploadCatchBinaryReturnsOpenError(t *testing.T) {
	ui := newInitUI(io.Discard, false, true, "", "", "")
	if _, err := uploadCatchBinary(ui, filepath.Join(t.TempDir(), "missing"), 0, "root@example.com"); err == nil || !strings.Contains(err.Error(), "failed to open catch binary") {
		t.Fatalf("uploadCatchBinary error = %v, want open error", err)
	}
}

func TestUploadCatchBinaryHelper(t *testing.T) {
	switch os.Getenv("YEET_UPLOAD_HELPER") {
	case "":
		return
	case "fail":
		os.Exit(3)
	case "copy":
		if len(os.Args) == 0 {
			os.Exit(2)
		}
		outPath := os.Args[len(os.Args)-1]
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(outPath, b, 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
