// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func isRecoverySnapshotList(args []string) bool {
	if len(args) <= 4 || args[0] != "list" {
		return false
	}
	hasType := false
	hasSnapshot := false
	for _, arg := range args {
		if arg == "-t" {
			hasType = true
		}
		if arg == "snapshot" {
			hasSnapshot = true
		}
	}
	return hasType && hasSnapshot
}

func readRecoveryLog(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read recovery log: %v", err)
	}
	return string(raw)
}

func mustService(t *testing.T, server *Server, name string) *db.Service {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		t.Fatalf("service %q not found", name)
	}
	return sv.AsStruct()
}

func serviceExists(t *testing.T, server *Server, name string) bool {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	_, ok := dv.Services().GetOk(name)
	return ok
}

func hasRecoveryCall(calls []string, needle string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, needle) {
			return true
		}
	}
	return false
}

type ioDiscardReadWriter struct{}

func (ioDiscardReadWriter) Read([]byte) (int, error)    { return 0, io.EOF }
func (ioDiscardReadWriter) Write(p []byte) (int, error) { return len(p), nil }
