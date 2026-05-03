// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package env

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type testEnv struct {
	Name  string `env:"NAME"`
	Empty string `env:"EMPTY"`
	Count int    `env:"COUNT"`
	Skip  string
}

func TestMarshalEnvWritesTaggedNonZeroFields(t *testing.T) {
	var out bytes.Buffer
	err := marshalEnv(&out, testEnv{Name: "yeet", Count: 2, Skip: "ignored"})
	if err != nil {
		t.Fatalf("marshalEnv: %v", err)
	}
	want := "NAME=yeet\nCOUNT=2\n"
	if out.String() != want {
		t.Fatalf("env output = %q, want %q", out.String(), want)
	}
}

func TestMarshalEnvPropagatesWriterError(t *testing.T) {
	wantErr := errors.New("write failed")
	err := marshalEnv(failingWriter{err: wantErr}, testEnv{Name: "yeet"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("marshalEnv error = %v, want %v", err, wantErr)
	}
}

func TestWriteCreatesEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.env")

	err := Write(path, &testEnv{Name: "yeet", Count: 2})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	want := "NAME=yeet\nCOUNT=2\n"
	if string(got) != want {
		t.Fatalf("env file = %q, want %q", string(got), want)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
