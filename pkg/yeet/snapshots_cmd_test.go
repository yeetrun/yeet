// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"reflect"
	"testing"
)

func TestHandleSvcSnapshotsDefaultsRoutesToSystemService(t *testing.T) {
	preserveSvcCommandGlobals(t)
	var gotService string
	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		return nil
	}
	req := svcCommandRequest{
		Command: svcCommand{
			Name:    "snapshots",
			Args:    []string{"defaults", "show"},
			RawArgs: []string{"snapshots", "defaults", "show"},
		},
	}
	if err := handleSvcSnapshots(context.Background(), req); err != nil {
		t.Fatalf("handleSvcSnapshots: %v", err)
	}
	if gotService != systemServiceName {
		t.Fatalf("service = %q, want %s", gotService, systemServiceName)
	}
	if !reflect.DeepEqual(gotArgs, []string{"snapshots", "defaults", "show"}) {
		t.Fatalf("args = %#v", gotArgs)
	}
}
