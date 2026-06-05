// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type fakeRunner struct {
	removeErr error
}

func (f *fakeRunner) SetNewCmd(func(string, ...string) *exec.Cmd) {}
func (f *fakeRunner) Start() error                                { return nil }
func (f *fakeRunner) Stop() error                                 { return nil }
func (f *fakeRunner) Restart() error                              { return nil }
func (f *fakeRunner) Logs(*svc.LogOptions) error                  { return nil }
func (f *fakeRunner) Remove() error                               { return f.removeErr }

func TestRemoveServiceCleansDBOnCleanupFailure(t *testing.T) {
	server := newTestServer(t)
	name := "svc-cleanup"

	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		if d.Services == nil {
			d.Services = map[string]*db.Service{}
		}
		d.Services[name] = &db.Service{Name: name}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	serviceDir := filepath.Join(server.cfg.ServicesRoot, name)
	runDir := filepath.Join(serviceDir, "run")
	dataDir := filepath.Join(serviceDir, "data")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.Chmod(serviceDir, 0o500); err != nil {
		t.Fatalf("chmod service dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(serviceDir, 0o700)
	})

	report, err := server.RemoveService(name)
	if err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if report == nil || len(report.Warnings) == 0 {
		t.Fatalf("expected cleanup warnings, got none")
	}
	found := false
	for _, warn := range report.Warnings {
		if strings.Contains(warn.Error(), "failed to remove service directory") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected service directory warning, got %v", report.Warnings)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("db get: %v", err)
	}
	if _, ok := dv.Services().GetOk(name); ok {
		t.Fatalf("service still present in db")
	}
}

func TestRemoveServiceCleansVMNetwork(t *testing.T) {
	server := newTestServer(t)
	name := "vm-cleanup"
	root := filepath.Join(server.cfg.ServicesRoot, name)
	network := newVMNetworkPlan(name, []string{"lan"}, vmNetworkInputs{LANParent: "vmbr0", LANParentIsBridge: true})
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:        name,
			ServiceType: db.ServiceTypeVM,
			ServiceRoot: root,
			VM: &db.VMConfig{
				Runtime:  vmRuntimeFirecracker,
				Networks: network.DBNetworks(),
			},
		},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	var commands [][]string
	old := vmRemovalNetworkRunner
	vmRemovalNetworkRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	t.Cleanup(func() { vmRemovalNetworkRunner = old })

	report, err := server.RemoveService(name)
	if err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if report == nil || len(report.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", report.Warnings)
	}
	want := [][]string{{"ip", "link", "del", network.Interfaces[0].Tap}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("network cleanup commands = %#v, want %#v", commands, want)
	}
}

func TestRemoveCmdContinuesAfterRunnerError(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove"

	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		if d.Services == nil {
			d.Services = map[string]*db.Service{}
		}
		d.Services[name] = &db.Service{Name: name}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\n"),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{removeErr: errors.New("stop failed")}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if !strings.Contains(out.String(), "warning: failed to stop/remove service") {
		t.Fatalf("expected warning about remove failure, got %q", out.String())
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("db get: %v", err)
	}
	if _, ok := dv.Services().GetOk(name); ok {
		t.Fatalf("service still present in db")
	}
}

func TestRemoveCmdSkipsPromptWithYes(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-yes"

	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		if d.Services == nil {
			d.Services = map[string]*db.Service{}
		}
		d.Services[name] = &db.Service{Name: name}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: io.Discard,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
}
