// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestConfirmTSUpdateReturnsPromptWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	ok, err := confirmTSUpdate(readWriter{
		Reader: strings.NewReader("y\n"),
		Writer: failingWriter{err: writeErr},
	}, "1.90.0", "1.92.0")
	if err == nil {
		t.Fatal("expected prompt write error")
	}
	if ok {
		t.Fatal("confirmation should be false on prompt write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
}

func TestMountCmdListReturnsFlushError(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Volumes = map[string]*db.Volume{
			"data": {
				Name: "data",
				Src:  "host:/srv/data",
				Path: "/mnt/data",
				Type: "sshfs",
				Opts: "ro",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed volume: %v", err)
	}

	writeErr := errors.New("write failed")
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: failingWriter{err: writeErr},
		},
	}

	err := execer.mountCmdFunc(cli.MountFlags{}, nil)
	if err == nil {
		t.Fatal("expected mount listing write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
}

func TestMountCmdCreatePersistsVolumeAndRunsMount(t *testing.T) {
	server := newTestServer(t)

	oldCheckMountCommand := checkMountCommand
	oldMountVolume := mountVolume
	defer func() {
		checkMountCommand = oldCheckMountCommand
		mountVolume = oldMountVolume
	}()

	var checkedType string
	checkMountCommand = func(mountType string) error {
		checkedType = mountType
		return nil
	}
	var mounted db.Volume
	mountVolume = func(vol db.Volume) error {
		mounted = vol
		return nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  &out,
	}

	flags := cli.MountFlags{
		Type: "sshfs",
		Opts: "ro",
		Deps: []string{"network-online.target"},
	}
	if err := execer.mountCmdFunc(flags, []string{"host:/srv/data", "data"}); err != nil {
		t.Fatalf("mountCmdFunc: %v", err)
	}

	if checkedType != "sshfs" {
		t.Fatalf("checked mount type = %q, want sshfs", checkedType)
	}
	wantPath := server.cfg.MountsRoot + "/data"
	if mounted.Name != "data" || mounted.Src != "host:/srv/data" || mounted.Path != wantPath || mounted.Type != "sshfs" || mounted.Opts != "ro" || mounted.Deps != "network-online.target" {
		t.Fatalf("mounted volume = %+v", mounted)
	}
	if got := out.String(); !strings.Contains(got, "Mounted host:/srv/data at "+wantPath) {
		t.Fatalf("mount output = %q", got)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	vol, ok := dv.Volumes().GetOk("data")
	if !ok {
		t.Fatal("expected persisted data volume")
	}
	if got := vol.Path(); got != wantPath {
		t.Fatalf("persisted volume path = %q, want %q", got, wantPath)
	}
}
