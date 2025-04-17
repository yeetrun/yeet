// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ftdetect

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetectFileByExtension(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fileName string
		contents string
		want     FileType
	}{
		{
			name:     "compose_yml",
			fileName: "compose.yml",
			contents: "services:\n  app:\n    image: busybox\n",
			want:     DockerCompose,
		},
		{
			name:     "compose_yaml_other_name",
			fileName: "stack.yaml",
			contents: "def hello():\n  pass\n",
			want:     DockerCompose,
		},
		{
			name:     "python_by_ext",
			fileName: "main.py",
			contents: "export const x: number = 1;\n",
			want:     Python,
		},
		{
			name:     "typescript_by_ext",
			fileName: "main.ts",
			contents: "def hello():\n  pass\n",
			want:     TypeScript,
		},
		{
			name:     "script_shebang",
			fileName: "run",
			contents: "#!/usr/bin/env bash\necho hi\n",
			want:     Script,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, tc.fileName)

			if err := os.WriteFile(path, []byte(tc.contents), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}

			ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				t.Fatalf("DetectFile error: %v", err)
			}
			if ft != tc.want {
				t.Fatalf("DetectFile type mismatch: got %v want %v", ft, tc.want)
			}
		})
	}
}

func TestDetectFileUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
	if err == nil {
		t.Fatalf("expected error, got nil (type %v)", ft)
	}
	if ft != Unknown {
		t.Fatalf("expected Unknown type, got %v", ft)
	}
}
