// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestServiceRootDefaultsUsesServicesRootDatasetWithoutCreatingChild(t *testing.T) {
	server := newTestServer(t)
	datasets := fakeZFSRunner(map[string]fakeZFSDataset{
		"flash/yeet/services": {Mountpoint: server.cfg.ServicesRoot, Exists: true},
		"flash/yeet/services/nginx": {
			Mountpoint: filepath.Join(server.cfg.ServicesRoot, "nginx"),
			Exists:     false,
		},
	})
	server.zfsRunner = datasets.Run

	got, err := server.serviceRootDefaults(context.Background(), catchrpc.ServiceRootDefaultsRequest{Service: "nginx"})
	if err != nil {
		t.Fatalf("serviceRootDefaults: %v", err)
	}
	if got.ServiceRoot != "flash/yeet/services/nginx" || got.ServiceRootZFS != "flash/yeet/services/nginx" || !got.ZFS {
		t.Fatalf("defaults = %#v, want nginx child dataset", got)
	}
	if datasets["flash/yeet/services/nginx"].Exists {
		t.Fatalf("serviceRootDefaults created child dataset; want non-mutating lookup")
	}
}

func TestServiceRootDefaultsUsesFilesystemServicesRootWhenNotZFS(t *testing.T) {
	server := newTestServer(t)
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{}).Run

	got, err := server.serviceRootDefaults(context.Background(), catchrpc.ServiceRootDefaultsRequest{Service: "nginx"})
	if err != nil {
		t.Fatalf("serviceRootDefaults: %v", err)
	}
	want := filepath.Join(server.cfg.ServicesRoot, "nginx")
	if got.ServiceRoot != want || got.ServiceRootZFS != "" || got.ZFS {
		t.Fatalf("defaults = %#v, want filesystem root %q", got, want)
	}
}
