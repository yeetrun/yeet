// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
)

type fakeRunner struct {
	removeErr error
}

func TestRemoveServiceISOCleanDataZFSFailureRetainsVerifiedTombstoneWithoutDeletedEvent(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, "app")
	if err := os.MkdirAll(filepath.Join(serviceRoot, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.Name = "app"
		service.ServiceType = db.ServiceTypeDockerCompose
		service.ServiceRoot = serviceRoot
		service.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	zfsErr := errors.New("zfs failed")
	server.zfsRunner = func(context.Context, ...string) (string, string, error) {
		return "", "permission denied", zfsErr
	}
	recorder := &isoRemoveRecorder{server: server}
	server.newISORemoveSteps = func(string, RemoveOptions, *RemoveReport, string) (isoRemoveSteps, error) {
		return &isoRemoveZFSRecorder{isoRemoveRecorder: recorder, server: server, dataset: "tank/apps/app"}, nil
	}
	events := make(chan Event, 1)
	handle := server.AddEventListener(events, nil)
	defer server.RemoveEventListener(handle)

	_, err := server.RemoveServiceWithOptions("app", RemoveOptions{CleanData: true})
	if err == nil || !strings.Contains(err.Error(), "zfs destroy -R tank/apps/app") {
		t.Fatalf("RemoveServiceWithOptions error = %v, want post-cleanup ZFS failure", err)
	}
	dv, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	allocation := dv.Services().Get("app").ISO()
	if !allocation.RemoveRequested() || !allocation.CleanupVerified() || allocation.State() != string(iso.StateTombstoned) {
		t.Fatalf("ISO state after ZFS failure = %#v, want verified tombstone", allocation.AsStruct())
	}
	select {
	case event := <-events:
		t.Fatalf("published event after failed ISO removal: %#v", event)
	default:
	}
}

func TestISOReconcileRemovalResumePreservesCleanDataIntent(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateTombstoned)
	allocation.RemoveRequested = true
	allocation.CleanupVerified = true
	allocation.RemoveCleanData = true
	allocation.LastError = "before-delete: zfs destroy failed"
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, "app")
	if err := os.MkdirAll(filepath.Join(serviceRoot, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.Name = "app"
		service.ServiceType = db.ServiceTypeDockerCompose
		service.ServiceRoot = serviceRoot
		service.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recorder := &isoRemoveRecorder{server: server}
	cleanDataSeen := false
	server.newISORemoveSteps = func(_ string, options RemoveOptions, _ *RemoveReport, _ string) (isoRemoveSteps, error) {
		cleanDataSeen = options.CleanData
		return recorder, nil
	}

	steps := &isoConcreteReconcileSteps{server: server}
	if err := steps.ResumeRemoval(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if !cleanDataSeen {
		t.Fatal("startup removal resume discarded persisted CleanData intent")
	}
}

type isoRemoveZFSRecorder struct {
	*isoRemoveRecorder
	server  *Server
	dataset string
}

func (r *isoRemoveZFSRecorder) BeforeDelete(ctx context.Context, service string) error {
	if err := r.isoRemoveRecorder.BeforeDelete(ctx, service); err != nil {
		return err
	}
	return r.server.destroyServiceRootZFS(r.dataset)
}

func TestRemoveISOLeavesTombstoneOnEveryUnverifiedCleanupStep(t *testing.T) {
	steps := []string{
		"stop-workload", "stop-tailscale", "remove-docker-endpoints", "clean-topology",
		"verify-topology-absent", "verify-docker-absent", "verify-dnet-absent",
		"render-global-policy", "verify-global-policy", "before-delete",
	}
	for _, failAt := range steps {
		t.Run(failAt, func(t *testing.T) {
			server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
				"app": testISORuntimeAllocation("app", iso.StateReady),
			})
			recorder := &isoRemoveRecorder{server: server, failAt: failAt}

			err := server.removeISOServiceWith(context.Background(), "app", recorder)
			if err == nil || !strings.Contains(err.Error(), failAt) {
				t.Fatalf("removeISOServiceWith error = %v, want %q failure", err, failAt)
			}
			dv, getErr := server.cfg.DB.Get()
			if getErr != nil {
				t.Fatal(getErr)
			}
			service, ok := dv.Services().GetOk("app")
			if !ok || !service.ISO().Valid() || service.ISO().State() != string(iso.StateTombstoned) || !service.ISO().RemoveRequested() {
				t.Fatalf("failed removal state = %#v, want retained retryable tombstone", service.AsStruct())
			}
			if recorder.deleteObserved {
				t.Fatalf("service deleted after unverified %s failure", failAt)
			}
		})
	}
}

func TestRemoveISODeletesOnlyAfterAbsenceAndPolicyVerify(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	recorder := &isoRemoveRecorder{server: server}

	if err := server.removeISOServiceWith(context.Background(), "app", recorder); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"stop-workload", "stop-tailscale", "remove-docker-endpoints", "clean-topology",
		"verify-topology-absent", "verify-docker-absent", "verify-dnet-absent",
		"render-global-policy", "verify-global-policy", "before-delete",
	}
	if !reflect.DeepEqual(recorder.events, want) {
		t.Fatalf("remove events = %#v, want %#v", recorder.events, want)
	}
	if !recorder.intentObserved || !recorder.cleanupVerifiedBeforePolicy {
		t.Fatalf("persisted phases = intent %v cleanup-before-policy %v", recorder.intentObserved, recorder.cleanupVerifiedBeforePolicy)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dv.Services().GetOk("app"); ok {
		t.Fatal("ISO service record survived fully verified removal")
	}
}

func TestRemoveISOResumesUnverifiedTombstoneFromCleanup(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateTombstoned)
	allocation.RemoveRequested = true
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	recorder := &isoRemoveRecorder{server: server}

	if err := server.removeISOServiceWith(context.Background(), "app", recorder); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"stop-workload", "stop-tailscale", "remove-docker-endpoints", "clean-topology",
		"verify-topology-absent", "verify-docker-absent", "verify-dnet-absent",
		"render-global-policy", "verify-global-policy", "before-delete",
	}
	if !reflect.DeepEqual(recorder.events, want) {
		t.Fatalf("resumed remove events = %#v, want full cleanup retry %#v", recorder.events, want)
	}
}

func TestRemoveISOResumesVerifiedTombstoneAtPolicyWithoutResettingProof(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateTombstoned)
	allocation.RemoveRequested = true
	allocation.CleanupVerified = true
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	recorder := &isoRemoveRecorder{server: server}

	if err := server.removeISOServiceWith(context.Background(), "app", recorder); err != nil {
		t.Fatal(err)
	}
	want := []string{"render-global-policy", "verify-global-policy", "before-delete"}
	if !reflect.DeepEqual(recorder.events, want) {
		t.Fatalf("resumed remove events = %#v, want policy/delete-only resume %#v", recorder.events, want)
	}
	if !recorder.cleanupVerifiedBeforePolicy {
		t.Fatal("verified cleanup proof was reset before policy resume")
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dv.Services().GetOk("app"); ok {
		t.Fatal("verified removal tombstone survived successful policy resume")
	}
}

type isoRemoveRecorder struct {
	server                      *Server
	events                      []string
	failAt                      string
	intentObserved              bool
	cleanupVerifiedBeforePolicy bool
	deleteObserved              bool
}

func (r *isoRemoveRecorder) step(name string) error {
	r.events = append(r.events, name)
	if name == r.failAt {
		return fmt.Errorf("%s failed", name)
	}
	return nil
}

func (r *isoRemoveRecorder) StopWorkload(context.Context, string) error {
	dv, err := r.server.cfg.DB.Get()
	if err != nil {
		return err
	}
	isoState := dv.Services().Get("app").ISO()
	r.intentObserved = isoState.RemoveRequested() && !isoState.CleanupVerified() && isoState.State() == string(iso.StateRemoving)
	return r.step("stop-workload")
}

func (r *isoRemoveRecorder) StopTailscale(context.Context, string) error {
	return r.step("stop-tailscale")
}

func (r *isoRemoveRecorder) RemoveDockerEndpoints(context.Context, string) error {
	return r.step("remove-docker-endpoints")
}

func (r *isoRemoveRecorder) CleanTopology(context.Context, string) error {
	return r.step("clean-topology")
}

func (r *isoRemoveRecorder) VerifyTopologyAbsent(context.Context, string) error {
	return r.step("verify-topology-absent")
}

func (r *isoRemoveRecorder) VerifyDockerAbsent(context.Context, string) error {
	return r.step("verify-docker-absent")
}

func (r *isoRemoveRecorder) VerifyDNetAbsent(context.Context, string) error {
	return r.step("verify-dnet-absent")
}

func (r *isoRemoveRecorder) RenderGlobalPolicy(_ context.Context, service string) error {
	dv, err := r.server.cfg.DB.Get()
	if err != nil {
		return err
	}
	r.cleanupVerifiedBeforePolicy = dv.Services().Get(service).ISO().CleanupVerified()
	return r.step("render-global-policy")
}

func (r *isoRemoveRecorder) VerifyGlobalPolicy(context.Context, string) error {
	return r.step("verify-global-policy")
}

func (r *isoRemoveRecorder) BeforeDelete(context.Context, string) error {
	return r.step("before-delete")
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
	if err := markVMJailerReady(root); err != nil {
		t.Fatalf("mark VM jailer ready: %v", err)
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

func TestConcreteISORemovalCleansDedicatedVMTap(t *testing.T) {
	server, allocation := newISOReconcileVMTestServer(t, iso.StateStopped)
	withISORuntimeBackend(t, netns.BackendNFT)
	oldRunner := vmRemovalNetworkRunner
	var commands [][]string
	vmRemovalNetworkRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	t.Cleanup(func() { vmRemovalNetworkRunner = oldRunner })

	steps, err := server.newConcreteISORemoveSteps("devbox", RemoveOptions{}, &RemoveReport{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := steps.CleanTopology(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"ip", "link", "del", allocation.Interface}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestConcreteISORemovalRecoversTypedByAllocationAfterPartialProvision(t *testing.T) {
	allocation := testISORuntimeAllocation("devbox", iso.StateTombstoned)
	allocation.Kind = string(iso.PayloadVM)
	allocation.Project = netip.Prefix{}
	allocation.Gateway = netip.Addr{}
	allocation.NetNS = ""
	allocation.Bridge = ""
	allocation.Components = nil
	allocation.RetiredComponents = nil
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"devbox": allocation})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldRunner := vmRemovalNetworkRunner
	var commands [][]string
	vmRemovalNetworkRunner = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	t.Cleanup(func() { vmRemovalNetworkRunner = oldRunner })

	steps, err := server.newConcreteISORemoveSteps("devbox", RemoveOptions{}, &RemoveReport{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := steps.CleanTopology(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"ip", "link", "del", allocation.Interface}}; !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestRemoveServiceCleansStaleVMJail(t *testing.T) {
	server := newTestServer(t)
	name := "vm-cleanup"
	root := filepath.Join(server.cfg.ServicesRoot, name)
	imageDir := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-v15")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:        name,
			ServiceType: db.ServiceTypeVM,
			ServiceRoot: root,
			VM: &db.VMConfig{
				Runtime: vmRuntimeFirecracker,
				Image:   db.VMImageConfig{RootFS: filepath.Join(imageDir, "rootfs.ext4")},
				Sockets: db.VMSocketConfig{
					APISocketPath:   filepath.Join(root, "run", "firecracker.sock"),
					VsockSocketPath: filepath.Join(root, "run", "vsock.sock"),
				},
			},
		},
	}}); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	oldCleanup := vmRemovalJailCleanup
	var cleaned vmJailPlan
	vmRemovalJailCleanup = func(plan vmJailPlan) error {
		cleaned = plan
		return nil
	}
	t.Cleanup(func() { vmRemovalJailCleanup = oldCleanup })

	report, err := server.RemoveService(name)
	if err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if report == nil || len(report.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", report.Warnings)
	}
	if cleaned.ID != vmJailerID(name) || cleaned.JailRoot == "" || len(cleaned.SocketLinks) != 2 {
		t.Fatalf("cleaned jail plan = %#v", cleaned)
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
			Reader: strings.NewReader("y\n\n"),
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

func seedRemovePromptService(t *testing.T, server *Server, name string, serviceType db.ServiceType) string {
	t.Helper()
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, name)
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(serviceRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(serviceRoot, "data", "state.txt"), []byte("state"), 0o644); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		if d.Services == nil {
			d.Services = map[string]*db.Service{}
		}
		d.Services[name] = &db.Service{Name: name, ServiceType: serviceType}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	return serviceRoot
}

func TestRemoveCmdDataPromptDefaultsNo(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-data-default"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\n\n"),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `Delete all data for service "svc-remove-data-default"?`) {
		t.Fatalf("output = %q, want data prompt", got)
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data", "state.txt")); err != nil {
		t.Fatalf("data should remain after default-no prompt: %v", err)
	}
	if _, err := server.serviceView(name); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
}

func TestRemoveCmdDataPromptCanEnableCleanData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-data-yes"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\ny\n"),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `Delete all data for service "svc-remove-data-yes"?`) {
		t.Fatalf("output = %q, want data prompt", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
}

func TestRemoveCmdCleanDataSkipsDataPrompt(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-clean-data"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

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
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{CleanData: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no data prompt", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
}

func TestRemoveCmdCleanSkipsDataPromptAndDeletesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-clean"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	flags, _, err := cli.ParseRemove([]string{"--clean"})
	if err != nil {
		t.Fatalf("ParseRemove: %v", err)
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
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(flags); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no data prompt", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
}

func TestRemoveCmdYesSkipsDataPromptAndPreservesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-yes-preserve-data"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Are you sure") || strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no prompts", got)
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data", "state.txt")); err != nil {
		t.Fatalf("data should remain with --yes and no --clean-data: %v", err)
	}
}

func TestRemoveCmdYesCleanDataSkipsPromptsAndDeletesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-yes-clean-data"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true, CleanData: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Are you sure") || strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no prompts", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
}

func TestRemoveCmdUnsupportedTypePromptsAndPreservesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-unsupported-default"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\n\n"),
			Writer: &out,
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `Are you sure you want to remove service "svc-remove-unsupported-default"?`) {
		t.Fatalf("output = %q, want removal prompt", got)
	}
	if !strings.Contains(got, `Delete all data for service "svc-remove-unsupported-default"?`) {
		t.Fatalf("output = %q, want data prompt", got)
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data", "state.txt")); err != nil {
		t.Fatalf("data should remain after default-no prompt: %v", err)
	}
	if _, err := server.serviceView(name); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
}

func TestRemoveCmdUnsupportedTypeCleanDataStillConfirmsAndDeletesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-unsupported-clean-data"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\n"),
			Writer: &out,
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{CleanData: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `Are you sure you want to remove service "svc-remove-unsupported-clean-data"?`) {
		t.Fatalf("output = %q, want removal prompt", got)
	}
	if strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no data prompt", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
	if _, err := server.serviceView(name); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
}

func TestRemoveCmdUnsupportedTypeDeclineSkipsRemoval(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-unsupported-decline"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("n\n"),
			Writer: &out,
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `Are you sure you want to remove service "svc-remove-unsupported-decline"?`) {
		t.Fatalf("output = %q, want removal prompt", got)
	}
	if strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no data prompt after declining removal", got)
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data", "state.txt")); err != nil {
		t.Fatalf("data should remain after declined removal: %v", err)
	}
	if _, err := server.serviceView(name); err != nil {
		t.Fatalf("serviceView after declined removal = %v, want service to remain", err)
	}
}

func TestRemoveCmdUnsupportedTypeYesSkipsPromptsAndPreservesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-unsupported-yes"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: &out,
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Are you sure") || strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no prompts", got)
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data", "state.txt")); err != nil {
		t.Fatalf("data should remain with --yes and no --clean-data: %v", err)
	}
	if _, err := server.serviceView(name); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
}

func TestRemoveCmdUnsupportedTypeYesCleanDataSkipsPromptsAndDeletesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-unsupported-yes-clean-data"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: &out,
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true, CleanData: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Are you sure") || strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no prompts", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
	if _, err := server.serviceView(name); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
}
