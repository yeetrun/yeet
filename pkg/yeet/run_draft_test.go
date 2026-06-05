// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func preserveRunDraftGlobals(t *testing.T) {
	t.Helper()
	oldService := serviceOverride
	oldHost := hostOverride
	oldHostSet := hostOverrideSet
	oldPrefs := loadedPrefs
	oldFetchRunDraftServiceInfo := fetchRunDraftServiceInfoFn
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	t.Setenv("CATCH_HOST", "")
	t.Cleanup(func() {
		serviceOverride = oldService
		hostOverride = oldHost
		hostOverrideSet = oldHostSet
		loadedPrefs = oldPrefs
		fetchRunDraftServiceInfoFn = oldFetchRunDraftServiceInfo
	})
}

func TestRunDraftFromCLIParsesFirstDeployOptions(t *testing.T) {
	preserveRunDraftGlobals(t)
	serviceOverride = "svc-a"
	hostOverride = "host-a"
	hostOverrideSet = true

	loc := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
	draft, err := runDraftFromCLI([]string{
		"--net=svc,ts",
		"--ts-tags", "tag:app",
		"--env-file=.env",
		"--service-root=tank/apps/svc-a",
		"--zfs",
		"--snapshots=on",
		"--snapshot-keep-last=3",
		"./compose.yml",
		"--",
		"--app-flag",
	}, loc, "host-a")
	if err != nil {
		t.Fatalf("runDraftFromCLI error: %v", err)
	}

	if draft.Service != "svc-a" || draft.Host != "host-a" || draft.Payload != "./compose.yml" {
		t.Fatalf("draft identity = service %q host %q payload %q", draft.Service, draft.Host, draft.Payload)
	}
	if draft.EnvFile != ".env" {
		t.Fatalf("EnvFile = %q, want .env", draft.EnvFile)
	}
	if draft.Storage.ServiceRoot != "tank/apps/svc-a" || !draft.Storage.ZFS {
		t.Fatalf("Storage = %#v, want service root tank/apps/svc-a with zfs", draft.Storage)
	}
	if draft.Snapshots.Mode != "on" || draft.Snapshots.KeepLast != 3 {
		t.Fatalf("Snapshots = %#v, want mode on keep-last 3", draft.Snapshots)
	}
	if !reflect.DeepEqual(draft.Network.Modes, []string{"svc", "ts"}) {
		t.Fatalf("Network.Modes = %#v", draft.Network.Modes)
	}
	if !reflect.DeepEqual(draft.Network.TSTags, []string{"tag:app"}) {
		t.Fatalf("Network.TSTags = %#v", draft.Network.TSTags)
	}
	if !reflect.DeepEqual(draft.PayloadArgs, []string{"--app-flag"}) {
		t.Fatalf("PayloadArgs = %#v", draft.PayloadArgs)
	}
}

func TestRunDraftBuildsExistingRunArgs(t *testing.T) {
	required := false
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "./compose.yml",
		EnvFile: ".env",
		Network: RunDraftNetwork{
			Modes:   []string{"svc", "ts"},
			TSTags:  []string{"tag:app"},
			Publish: []string{"8080:80"},
		},
		Storage: RunDraftStorage{ServiceRoot: "tank/apps/svc-a", ZFS: true},
		Snapshots: RunDraftSnapshots{
			Mode:     "on",
			KeepLast: 3,
			MaxAge:   "72h",
			Required: &required,
			Events:   []string{"run"},
		},
		PayloadArgs: []string{"--app-flag"},
	}

	want := []string{
		"--snapshot-events=run",
		"--snapshot-required=false",
		"--snapshot-max-age=72h",
		"--snapshot-keep-last=3",
		"--snapshots=on",
		"--service-root=tank/apps/svc-a",
		"--zfs",
		"--net=svc,ts",
		"--ts-tags=tag:app",
		"--publish=8080:80",
		"--",
		"--app-flag",
	}
	if got := draft.runArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs() = %#v, want %#v", got, want)
	}
}

func TestRunDraftFromCLIParsesVMOptions(t *testing.T) {
	preserveRunDraftGlobals(t)
	serviceOverride = "devbox"
	hostOverride = "host-a"
	hostOverrideSet = true

	loc := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
	for _, payload := range []string{"vm://ubuntu/26.04", "vm://foo/bar"} {
		t.Run(payload, func(t *testing.T) {
			draft, err := runDraftFromCLI([]string{
				"--cpus=4",
				"--memory=4g",
				"--disk=128g",
				payload,
			}, loc, "host-a")
			if err != nil {
				t.Fatalf("runDraftFromCLI error: %v", err)
			}
			if draft.Payload != payload {
				t.Fatalf("payload = %q, want %q", draft.Payload, payload)
			}
			if draft.VM.CPUs != 4 || draft.VM.Memory != "4g" || draft.VM.Disk != "128g" {
				t.Fatalf("VM = %#v, want cpus=4 memory=4g disk=128g", draft.VM)
			}
		})
	}
}

func TestRunDraftBuildsVMRunArgs(t *testing.T) {
	draft := RunDraft{
		Service: "devbox",
		Host:    "host-a",
		Payload: "vm://ubuntu/26.04",
		Network: RunDraftNetwork{
			Modes: []string{"svc", "lan"},
		},
		VM: RunDraftVM{
			CPUs:   4,
			Memory: "4g",
			Disk:   "128g",
		},
	}
	want := []string{"--net=svc,lan", "--cpus=4", "--memory=4g", "--disk=128g"}
	if got := draft.runArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs() = %#v, want %#v", got, want)
	}
}

func TestRunDraftFromCLIMatchesParseSvcRunParity(t *testing.T) {
	tests := []struct {
		name               string
		args               []string
		entries            []ServiceEntry
		wantPull           bool
		wantForce          bool
		wantSnapshotChange bool
	}{
		{
			name:      "pull and force",
			args:      []string{"--net=svc", "--pull", "--force", "./compose.yml", "--", "--app-flag"},
			wantPull:  true,
			wantForce: true,
		},
		{
			name: "existing stored args",
			args: []string{"./compose.yml"},
			entries: []ServiceEntry{{
				Name:    "svc-a",
				Host:    "host-a",
				Type:    serviceTypeRun,
				Payload: "./compose.yml",
				Args:    []string{"--net=svc,ts", "--ts-tags=tag:app", "--pull", "--app-flag"},
			}},
			wantPull: true,
		},
		{
			name: "existing stored args in noncanonical order",
			args: []string{"./compose.yml"},
			entries: []ServiceEntry{{
				Name:    "svc-a",
				Host:    "host-a",
				Type:    serviceTypeRun,
				Payload: "./compose.yml",
				Args:    []string{"--pull", "--net=svc"},
			}},
			wantPull: true,
		},
		{
			name: "explicit default restart flag",
			args: []string{"--net=svc", "--restart=true", "./compose.yml"},
		},
		{
			name: "existing snapshot overrides",
			args: []string{"--net=svc", "./compose.yml"},
			entries: []ServiceEntry{{
				Name:             "svc-a",
				Host:             "host-a",
				Type:             serviceTypeRun,
				Payload:          "./compose.yml",
				ServiceRoot:      "tank/apps/svc-a",
				ServiceRootZFS:   true,
				Snapshots:        "on",
				SnapshotKeepLast: 3,
				SnapshotMaxAge:   "72h",
				SnapshotRequired: runDraftTestBool(false),
				SnapshotEvents:   []string{"run"},
				Args:             []string{"--net=svc"},
			}},
		},
		{
			name: "explicit snapshot flag matching stored config",
			args: []string{"--net=svc", "--snapshots=on", "./compose.yml"},
			entries: []ServiceEntry{{
				Name:      "svc-a",
				Host:      "host-a",
				Type:      serviceTypeRun,
				Payload:   "./compose.yml",
				Snapshots: "on",
				Args:      []string{"--net=svc"},
			}},
			wantSnapshotChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveRunDraftGlobals(t)
			serviceOverride = "svc-a"
			loc := &projectConfigLocation{Dir: t.TempDir(), Config: &ProjectConfig{Version: projectConfigVersion}}
			for _, entry := range tt.entries {
				loc.Config.SetServiceEntry(entry)
			}

			parsed, err := parseSvcRun(tt.args, loc, "host-a")
			if err != nil {
				t.Fatalf("parseSvcRun error: %v", err)
			}
			draft, err := runDraftFromCLI(tt.args, loc, "host-a")
			if err != nil {
				t.Fatalf("runDraftFromCLI error: %v", err)
			}
			if got := draft.runArgs(); !reflect.DeepEqual(got, parsed.Args) {
				t.Fatalf("draft runArgs = %#v, want parseSvcRun args %#v", got, parsed.Args)
			}
			if draft.ForceDeploy != parsed.ForceDeploy || draft.ForceDeploy != tt.wantForce {
				t.Fatalf("ForceDeploy = %v, parseSvcRun = %v, want %v", draft.ForceDeploy, parsed.ForceDeploy, tt.wantForce)
			}
			if draft.SnapshotChange != parsed.SnapshotChange || draft.SnapshotChange != tt.wantSnapshotChange {
				t.Fatalf("SnapshotChange = %v, parseSvcRun = %v, want %v", draft.SnapshotChange, parsed.SnapshotChange, tt.wantSnapshotChange)
			}
			if draft.Pull != tt.wantPull {
				t.Fatalf("Pull = %v, want %v", draft.Pull, tt.wantPull)
			}
		})
	}
}

func TestRunDraftRunArgsDefaultsRestartOnForWebDrafts(t *testing.T) {
	draft := RunDraft{Network: RunDraftNetwork{Modes: []string{"svc"}}}
	if got, want := draft.runArgs(), []string{"--net=svc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs() = %#v, want %#v", got, want)
	}

	draft.Network.Restart = runDraftTestBool(false)
	if got, want := draft.runArgs(), []string{"--net=svc", "--restart=false"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs() with explicit restart=false = %#v, want %#v", got, want)
	}
}

func TestRunDraftFromCLIRejectsWebForDraftExecution(t *testing.T) {
	preserveRunDraftGlobals(t)
	serviceOverride = "svc-a"

	if _, err := runDraftFromCLI([]string{"--web", "./compose.yml"}, nil, ""); err == nil {
		t.Fatal("runDraftFromCLI error = nil, want --web rejection")
	}
}

func TestExecuteRunDraftRejectsInvalidDraftBeforeRemote(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldExec := execRemoteFn
	defer func() {
		execRemoteFn = oldExec
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		t.Fatalf("unexpected service info call for host=%q service=%q", host, service)
		return catchrpc.ServiceInfoResponse{}, nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("unexpected remote call: service=%q args=%v", service, args)
		return nil
	}

	err := executeRunDraft(context.Background(), RunDraft{}, nil, false)
	if err == nil {
		t.Fatal("executeRunDraft error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "invalid run draft") || !strings.Contains(err.Error(), "service is required") {
		t.Fatalf("executeRunDraft error = %v, want invalid service validation error", err)
	}
}

func TestExecuteRunDraftWithOptionsWritesDeployOutputToProvidedWriter(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldExecTo := execRemoteToFn
	oldRemoteArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	defer func() {
		execRemoteToFn = oldExecTo
		remoteCatchOSAndArchFn = oldRemoteArch
		fetchRemoteArtifactHashesFn = oldHashes
	}()

	serviceOverride = "svc-a"
	hostOverride = "host-a"
	hostOverrideSet = true
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	execRemoteToFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		if service != "svc-a" {
			t.Fatalf("execRemoteToFn service = %q, want svc-a", service)
		}
		if _, err := io.WriteString(stdout, "installing service\n"); err != nil {
			return err
		}
		return nil
	}

	tmpDir := t.TempDir()
	payload := filepath.Join(tmpDir, "compose.yml")
	if err := os.WriteFile(payload, []byte("services:\n  svc-a:\n    image: alpine:latest\n"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	cfgLoc := &projectConfigLocation{
		Path:   filepath.Join(tmpDir, projectConfigName),
		Dir:    tmpDir,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}

	var out strings.Builder
	err := executeRunDraftWithOptions(context.Background(), RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: payload,
	}, cfgLoc, runDraftExecuteOptions{Stdout: &out, ForceDeploy: true})
	if err != nil {
		t.Fatalf("executeRunDraftWithOptions: %v", err)
	}
	if !strings.Contains(out.String(), "installing service") {
		t.Fatalf("stdout = %q, want deploy output", out.String())
	}
}

func TestExecuteRunDraftSkipsServiceInfoOutsideNewOnlyMode(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldTryImage := tryRunRemoteImageFn
	defer func() {
		tryRunRemoteImageFn = oldTryImage
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		t.Fatalf("unexpected service info call for host=%q service=%q", host, service)
		return catchrpc.ServiceInfoResponse{}, nil
	}
	tryRunRemoteImageFn = func(ctx context.Context, image string, args []string) (bool, error) {
		if image != "ghcr.io/example/app:latest" {
			t.Fatalf("image = %q, want ghcr.io/example/app:latest", image)
		}
		return true, nil
	}

	tmp := t.TempDir()
	cfgLoc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "ghcr.io/example/app:latest",
	}

	if err := executeRunDraft(context.Background(), draft, cfgLoc, false); err != nil {
		t.Fatalf("executeRunDraft: %v", err)
	}
}

func TestExecuteRunDraftLocalImagePayloadKindUsesLocalDocker(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldTryImage := tryRunRemoteImageFn
	oldTryDocker := tryRunDockerFn
	defer func() {
		tryRunRemoteImageFn = oldTryImage
		tryRunDockerFn = oldTryDocker
	}()
	tryRunRemoteImageFn = func(ctx context.Context, image string, args []string) (bool, error) {
		t.Fatalf("unexpected remote image run: image=%q args=%v", image, args)
		return false, nil
	}
	var gotImage string
	var gotArgs []string
	tryRunDockerFn = func(ctx context.Context, image string, args []string) (bool, error) {
		gotImage = image
		gotArgs = append([]string{}, args...)
		return true, nil
	}

	tmp := t.TempDir()
	cfgLoc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	draft := RunDraft{
		Service:     "svc-a",
		Host:        "host-a",
		Payload:     "repo/svc/app:latest",
		PayloadKind: "local-image",
		Network: RunDraftNetwork{
			Modes: []string{"svc"},
		},
	}

	if err := executeRunDraft(context.Background(), draft, cfgLoc, false); err != nil {
		t.Fatalf("executeRunDraft: %v", err)
	}
	if gotImage != draft.Payload {
		t.Fatalf("local image = %q, want %q", gotImage, draft.Payload)
	}
	if !reflect.DeepEqual(gotArgs, []string{"--net=svc"}) {
		t.Fatalf("local image args = %#v, want --net=svc", gotArgs)
	}
	entry, ok := cfgLoc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("saved config missing svc-a@host-a")
	}
	if entry.Payload != draft.Payload || entry.PayloadKind != "local-image" {
		t.Fatalf("saved entry payload/kind = %q/%q, want %q/local-image", entry.Payload, entry.PayloadKind, draft.Payload)
	}
}

func TestRunDraftVMPayloadUsesVMRunnerAndSavesVMType(t *testing.T) {
	for _, payload := range []string{"vm://ubuntu/26.04", "vm://foo/bar"} {
		t.Run(payload, func(t *testing.T) {
			preserveRunDraftGlobals(t)
			oldVM := tryRunVMPayloadWithOutputFn
			defer func() { tryRunVMPayloadWithOutputFn = oldVM }()

			var gotPayload string
			var gotArgs []string
			tryRunVMPayloadWithOutputFn = func(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
				gotPayload = payload
				gotArgs = append([]string{}, args...)
				return true, nil
			}

			serviceOverride = "devbox"
			tmp := t.TempDir()
			loc := &projectConfigLocation{
				Path:   filepath.Join(tmp, projectConfigName),
				Dir:    tmp,
				Config: &ProjectConfig{Version: projectConfigVersion},
			}

			draft, err := runDraftFromCLI([]string{payload, "--net=svc", "--cpus=4"}, loc, "yeet-lab")
			if err != nil {
				t.Fatalf("runDraftFromCLI: %v", err)
			}
			if err := executeRunDraftWithOptions(context.Background(), draft, loc, runDraftExecuteOptions{Stdout: io.Discard}); err != nil {
				t.Fatalf("executeRunDraftWithOptions: %v", err)
			}

			if gotPayload != payload {
				t.Fatalf("payload = %q, want %q", gotPayload, payload)
			}
			if !reflect.DeepEqual(gotArgs, []string{"--net=svc", "--cpus=4"}) {
				t.Fatalf("args = %#v", gotArgs)
			}
			entry, ok := loc.Config.ServiceEntry("devbox", "yeet-lab")
			if !ok {
				t.Fatal("missing stored entry")
			}
			if entry.Type != serviceTypeVM || entry.PayloadKind != serviceTypeVM || entry.Payload != payload {
				t.Fatalf("entry = %#v, want VM payload %q", entry, payload)
			}
		})
	}
}

func TestRunFromProjectConfigReplaysStoredVMFilelessly(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldVM := tryRunVMPayloadWithOutputFn
	oldFilePayload := runFilePayloadWithOutputFn
	oldHashes := fetchRemoteArtifactHashesFn
	defer func() {
		tryRunVMPayloadWithOutputFn = oldVM
		runFilePayloadWithOutputFn = oldFilePayload
		fetchRemoteArtifactHashesFn = oldHashes
	}()

	serviceOverride = "devbox"
	var gotPayload string
	var gotArgs []string
	tryRunVMPayloadWithOutputFn = func(ctx context.Context, stdout io.Writer, payload string, args []string) (bool, error) {
		gotPayload = payload
		gotArgs = append([]string{}, args...)
		return true, nil
	}
	runFilePayloadWithOutputFn = func(ctx context.Context, stdout io.Writer, file string, args []string, pushLocalImages bool) (bool, error) {
		t.Fatalf("unexpected local file runner: file=%q args=%#v", file, args)
		return false, nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		t.Fatalf("unexpected artifact hash fetch for VM replay")
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}

	tmp := t.TempDir()
	cfgLoc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name:        "devbox",
			Host:        "yeet-lab",
			Type:        serviceTypeVM,
			PayloadKind: serviceTypeVM,
			Payload:     "vm://ubuntu/26.04",
			Args:        []string{"--net=svc", "--cpus=4"},
		}}},
	}

	if err := runFromProjectConfigWithForce(cfgLoc, "yeet-lab", false); err != nil {
		t.Fatalf("runFromProjectConfigWithForce: %v", err)
	}
	if gotPayload != "vm://ubuntu/26.04" {
		t.Fatalf("payload = %q, want vm://ubuntu/26.04", gotPayload)
	}
	if !reflect.DeepEqual(gotArgs, []string{"--net=svc", "--cpus=4"}) {
		t.Fatalf("args = %#v, want --net=svc --cpus=4", gotArgs)
	}
}

func TestRunFromProjectConfigUsesStoredLocalImagePayloadKind(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldTryImage := tryRunRemoteImageFn
	oldTryDocker := tryRunDockerFn
	defer func() {
		tryRunRemoteImageFn = oldTryImage
		tryRunDockerFn = oldTryDocker
	}()
	serviceOverride = "svc-a"
	tryRunRemoteImageFn = func(ctx context.Context, image string, args []string) (bool, error) {
		t.Fatalf("unexpected remote image run: image=%q args=%v", image, args)
		return false, nil
	}
	var gotImage string
	tryRunDockerFn = func(ctx context.Context, image string, args []string) (bool, error) {
		gotImage = image
		return true, nil
	}

	tmp := t.TempDir()
	cfgLoc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name:        "svc-a",
			Host:        "host-a",
			Type:        serviceTypeRun,
			Payload:     "alpine",
			PayloadKind: "local-image",
		}}},
	}

	if err := runFromProjectConfig(cfgLoc, "host-a"); err != nil {
		t.Fatalf("runFromProjectConfig: %v", err)
	}
	if gotImage != "alpine" {
		t.Fatalf("local image = %q, want alpine", gotImage)
	}
}

func TestRunFromProjectConfigPreservesStoredRemoteImageRef(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldTryImage := tryRunRemoteImageFn
	defer func() {
		tryRunRemoteImageFn = oldTryImage
	}()
	serviceOverride = "svc-a"
	var gotImage string
	tryRunRemoteImageFn = func(ctx context.Context, image string, args []string) (bool, error) {
		gotImage = image
		return true, nil
	}

	tmp := t.TempDir()
	cfgLoc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name:    "svc-a",
			Host:    "host-a",
			Type:    serviceTypeRun,
			Payload: "ghcr.io/example/app:latest",
		}}},
	}

	if err := runFromProjectConfig(cfgLoc, "host-a"); err != nil {
		t.Fatalf("runFromProjectConfig: %v", err)
	}
	if gotImage != "ghcr.io/example/app:latest" {
		t.Fatalf("remote image = %q, want raw image ref", gotImage)
	}
}

func TestExecuteRunDraftClearsStaleLocalImageKindForAutoFile(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldExec := execRemoteFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldArch := remoteCatchOSAndArchFn
	oldIsTerminal := isTerminalFn
	defer func() {
		execRemoteFn = oldExec
		fetchRemoteArtifactHashesFn = oldHashes
		remoteCatchOSAndArchFn = oldArch
		isTerminalFn = oldIsTerminal
	}()
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "app")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	cfgLoc := &projectConfigLocation{
		Path: filepath.Join(tmp, projectConfigName),
		Dir:  tmp,
		Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
			Name:        "svc-a",
			Host:        "host-a",
			Type:        serviceTypeRun,
			Payload:     "app",
			PayloadKind: "local-image",
		}}},
	}
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "app",
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	if err := executeRunDraft(context.Background(), draft, cfgLoc, false); err != nil {
		t.Fatalf("executeRunDraft: %v", err)
	}
	entry, ok := cfgLoc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("saved config missing svc-a@host-a")
	}
	if entry.PayloadKind != "" {
		t.Fatalf("PayloadKind = %q, want cleared", entry.PayloadKind)
	}
}

func TestExecuteRunDraftNewOnlyRejectsExistingService(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldTryImage := tryRunRemoteImageFn
	defer func() {
		tryRunRemoteImageFn = oldTryImage
	}()
	calls := 0
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		calls++
		if host != "host-a" || service != "svc-a" {
			t.Fatalf("service info host=%q service=%q, want host-a svc-a", host, service)
		}
		return catchrpc.ServiceInfoResponse{Found: true}, nil
	}
	tryRunRemoteImageFn = func(ctx context.Context, image string, args []string) (bool, error) {
		t.Fatalf("unexpected image run: image=%q args=%v", image, args)
		return false, nil
	}

	err := executeRunDraft(context.Background(), RunDraft{
		Service:        "svc-a",
		Host:           "host-a",
		Payload:        "ghcr.io/example/app:latest",
		NewServiceOnly: true,
	}, nil, false)
	if err == nil {
		t.Fatal("executeRunDraft error = nil, want existing service validation error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("executeRunDraft error = %v, want already exists", err)
	}
	if calls != 1 {
		t.Fatalf("service info calls = %d, want 1", calls)
	}
}

func TestExecuteRunDraftUsesExistingRunPathAndSavesConfig(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldExec := execRemoteFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldArch := remoteCatchOSAndArchFn
	oldIsTerminal := isTerminalFn
	defer func() {
		execRemoteFn = oldExec
		fetchRemoteArtifactHashesFn = oldHashes
		remoteCatchOSAndArchFn = oldArch
		isTerminalFn = oldIsTerminal
	}()
	serviceOverride = "svc-a"
	loadedPrefs.DefaultHost = "catch"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	cfgLoc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	var gotService string
	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: payload,
		Network: RunDraftNetwork{
			Modes:   []string{"svc"},
			Restart: runDraftTestBool(true),
		},
		PayloadArgs: []string{"--hello"},
	}
	if err := executeRunDraft(context.Background(), draft, cfgLoc, false); err != nil {
		t.Fatalf("executeRunDraft: %v", err)
	}
	if gotService != "svc-a" {
		t.Fatalf("service = %q, want svc-a", gotService)
	}
	wantArgs := []string{"run", "--net=svc", "--", "--hello"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	entry, ok := cfgLoc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("saved config missing svc-a@host-a")
	}
	if entry.Payload != "run.sh" {
		t.Fatalf("saved payload = %q, want run.sh", entry.Payload)
	}
	if Host() != "catch" {
		t.Fatalf("default host = %q, want unchanged catch", Host())
	}
}

func TestExecuteRunDraftPassesTSAuthKeyButDoesNotSaveIt(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldExec := execRemoteFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldArch := remoteCatchOSAndArchFn
	oldIsTerminal := isTerminalFn
	defer func() {
		execRemoteFn = oldExec
		fetchRemoteArtifactHashesFn = oldHashes
		remoteCatchOSAndArchFn = oldArch
		isTerminalFn = oldIsTerminal
	}()
	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	isTerminalFn = func(int) bool { return false }

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	cfgLoc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotArgs = append([]string{}, args...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: payload,
		Network: RunDraftNetwork{
			Modes:     []string{"ts"},
			TSAuthKey: "tskey-secret",
		},
	}

	if err := executeRunDraft(context.Background(), draft, cfgLoc, false); err != nil {
		t.Fatalf("executeRunDraft: %v", err)
	}
	if got := strings.Join(gotArgs, " "); !strings.Contains(got, "--ts-auth-key=tskey-secret") {
		t.Fatalf("remote args = %#v, want ts auth key passed", gotArgs)
	}
	entry, ok := cfgLoc.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatal("saved config missing svc-a@host-a")
	}
	if got := strings.Join(entry.Args, " "); strings.Contains(got, "tskey-secret") || strings.Contains(got, "--ts-auth-key") {
		t.Fatalf("saved args leaked ts auth key: %#v", entry.Args)
	}
	if got, want := entry.Args, []string{"--net=ts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved args = %#v, want %#v", got, want)
	}
}

func TestExecuteRunDraftPassesContextToRemoteRunWork(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldExec := execRemoteFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldArch := remoteCatchOSAndArchFn
	oldIsTerminal := isTerminalFn
	defer func() {
		execRemoteFn = oldExec
		fetchRemoteArtifactHashesFn = oldHashes
		remoteCatchOSAndArchFn = oldArch
		isTerminalFn = oldIsTerminal
	}()
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}
	isTerminalFn = func(int) bool { return false }

	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "web-run")
	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	cfgLoc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}

	hashContextSeen := false
	execContextSeen := false
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		hashContextSeen = ctx.Value(contextKey{}) == "web-run"
		return catchrpc.ArtifactHashesResponse{Found: false}, true, nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		execContextSeen = ctx.Value(contextKey{}) == "web-run"
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	if err := executeRunDraft(ctx, RunDraft{Service: "svc-a", Host: "host-a", Payload: payload}, cfgLoc, false); err != nil {
		t.Fatalf("executeRunDraft: %v", err)
	}
	if !hashContextSeen {
		t.Fatal("artifact hash lookup did not receive executeRunDraft context")
	}
	if !execContextSeen {
		t.Fatal("remote run did not receive executeRunDraft context")
	}
}

func TestExecuteRunDraftPassesContextToLocalDockerWork(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldImageExists := imageExistsFn
	oldPushImage := pushImageFn
	oldExecDirect := execRemoteDirectFn
	defer func() {
		imageExistsFn = oldImageExists
		pushImageFn = oldPushImage
		execRemoteDirectFn = oldExecDirect
	}()
	imageExistsFn = func(context.Context, string) bool { return true }

	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "web-run")
	pushContextSeen := false
	stageContextSeen := false
	commitContextSeen := false
	pushImageFn = func(ctx context.Context, service, image, tag string) error {
		pushContextSeen = ctx.Value(contextKey{}) == "web-run"
		return nil
	}
	execRemoteDirectFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		if len(args) >= 2 && args[0] == "stage" && args[1] == "commit" {
			commitContextSeen = ctx.Value(contextKey{}) == "web-run"
		} else {
			stageContextSeen = ctx.Value(contextKey{}) == "web-run"
		}
		return nil
	}

	tmp := t.TempDir()
	cfgLoc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	draft := RunDraft{
		Service:     "svc-a",
		Host:        "host-a",
		Payload:     "localapp",
		PayloadKind: "local-image",
		Network: RunDraftNetwork{
			Modes: []string{"svc"},
		},
	}

	if err := executeRunDraft(ctx, draft, cfgLoc, false); err != nil {
		t.Fatalf("executeRunDraft: %v", err)
	}
	if !pushContextSeen {
		t.Fatal("local image push did not receive executeRunDraft context")
	}
	if !stageContextSeen {
		t.Fatal("docker args stage did not receive executeRunDraft context")
	}
	if !commitContextSeen {
		t.Fatal("docker stage commit did not receive executeRunDraft context")
	}
}

func TestExecuteRunDraftForcesDeployFromCallDraftOrSnapshot(t *testing.T) {
	tests := []struct {
		name           string
		forceDeploy    bool
		draftForce     bool
		snapshotChange bool
		wantCalls      int
	}{
		{name: "no force", wantCalls: 0},
		{name: "call force", forceDeploy: true, wantCalls: 1},
		{name: "draft force", draftForce: true, wantCalls: 1},
		{name: "snapshot change", snapshotChange: true, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveRunDraftGlobals(t)
			oldExec := execRemoteFn
			oldHashes := fetchRemoteArtifactHashesFn
			oldArch := remoteCatchOSAndArchFn
			oldIsTerminal := isTerminalFn
			defer func() {
				execRemoteFn = oldExec
				fetchRemoteArtifactHashesFn = oldHashes
				remoteCatchOSAndArchFn = oldArch
				isTerminalFn = oldIsTerminal
			}()
			remoteCatchOSAndArchFn = func() (string, string, error) {
				return "linux", "amd64", nil
			}
			isTerminalFn = func(int) bool { return false }

			tmp := t.TempDir()
			payload := filepath.Join(tmp, "run.sh")
			if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
				t.Fatalf("write payload: %v", err)
			}
			payloadHash, err := hashFileSHA256(payload)
			if err != nil {
				t.Fatalf("hash payload: %v", err)
			}
			fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
				return catchrpc.ArtifactHashesResponse{
					Found: true,
					Payload: &catchrpc.ArtifactHash{
						Kind:   "script",
						SHA256: payloadHash,
					},
				}, true, nil
			}

			var calls [][]string
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				calls = append(calls, append([]string{}, args...))
				_, _ = io.Copy(io.Discard, stdin)
				return nil
			}
			cfgLoc := &projectConfigLocation{
				Path:   filepath.Join(tmp, projectConfigName),
				Dir:    tmp,
				Config: &ProjectConfig{Version: projectConfigVersion},
			}
			draft := RunDraft{
				Service:        "svc-a",
				Host:           "host-a",
				Payload:        payload,
				ForceDeploy:    tt.draftForce,
				SnapshotChange: tt.snapshotChange,
				ExistingEntry:  ServiceEntry{Args: nil},
			}

			if err := executeRunDraft(context.Background(), draft, cfgLoc, tt.forceDeploy); err != nil {
				t.Fatalf("executeRunDraft: %v", err)
			}
			if len(calls) != tt.wantCalls {
				t.Fatalf("remote calls = %d, want %d", len(calls), tt.wantCalls)
			}
			if len(calls) > 0 && !reflect.DeepEqual(calls[0], []string{"run"}) {
				t.Fatalf("remote call args = %#v, want [run]", calls[0])
			}
		})
	}
}

func TestExecuteRunDraftSavesEnvFileOnlyWhenExplicitlySet(t *testing.T) {
	tests := []struct {
		name       string
		envFileSet bool
		wantEnv    string
	}{
		{name: "stored env preserved", wantEnv: "stored.env"},
		{name: "explicit env saved", envFileSet: true, wantEnv: "explicit.env"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveRunDraftGlobals(t)
			oldExec := execRemoteFn
			oldHashes := fetchRemoteArtifactHashesFn
			oldArch := remoteCatchOSAndArchFn
			oldIsTerminal := isTerminalFn
			defer func() {
				execRemoteFn = oldExec
				fetchRemoteArtifactHashesFn = oldHashes
				remoteCatchOSAndArchFn = oldArch
				isTerminalFn = oldIsTerminal
			}()
			remoteCatchOSAndArchFn = func() (string, string, error) {
				return "linux", "amd64", nil
			}
			isTerminalFn = func(int) bool { return false }

			tmp := t.TempDir()
			payload := filepath.Join(tmp, "run.sh")
			if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
				t.Fatalf("write payload: %v", err)
			}
			storedEnv := filepath.Join(tmp, "stored.env")
			if err := os.WriteFile(storedEnv, []byte("KEY=stored\n"), 0o600); err != nil {
				t.Fatalf("write stored env: %v", err)
			}
			explicitEnv := filepath.Join(tmp, "explicit.env")
			if err := os.WriteFile(explicitEnv, []byte("KEY=explicit\n"), 0o600); err != nil {
				t.Fatalf("write explicit env: %v", err)
			}
			envForRun := storedEnv
			if tt.envFileSet {
				envForRun = explicitEnv
			}
			payloadHash, err := hashFileSHA256(payload)
			if err != nil {
				t.Fatalf("hash payload: %v", err)
			}
			envHash, err := hashFileSHA256(envForRun)
			if err != nil {
				t.Fatalf("hash env: %v", err)
			}
			fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
				return catchrpc.ArtifactHashesResponse{
					Found: true,
					Payload: &catchrpc.ArtifactHash{
						Kind:   "script",
						SHA256: payloadHash,
					},
					Env: &catchrpc.ArtifactHash{
						Kind:   "env file",
						SHA256: envHash,
					},
				}, true, nil
			}

			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				t.Fatalf("unexpected remote call: service=%q args=%v", service, args)
				return nil
			}
			cfgLoc := &projectConfigLocation{
				Path: filepath.Join(tmp, projectConfigName),
				Dir:  tmp,
				Config: &ProjectConfig{Version: projectConfigVersion, Services: []ServiceEntry{{
					Name:    "svc-a",
					Host:    "host-a",
					Type:    serviceTypeRun,
					Payload: "run.sh",
					EnvFile: "stored.env",
				}}},
			}
			draft := RunDraft{
				Service:       "svc-a",
				Host:          "host-a",
				Payload:       payload,
				EnvFile:       envForRun,
				EnvFileArg:    explicitEnv,
				EnvFileSet:    tt.envFileSet,
				ExistingEntry: ServiceEntry{EnvFile: "stored.env"},
			}

			if err := executeRunDraft(context.Background(), draft, cfgLoc, false); err != nil {
				t.Fatalf("executeRunDraft: %v", err)
			}
			entry, ok := cfgLoc.Config.ServiceEntry("svc-a", "host-a")
			if !ok {
				t.Fatal("saved config missing svc-a@host-a")
			}
			if entry.EnvFile != tt.wantEnv {
				t.Fatalf("EnvFile = %q, want %q", entry.EnvFile, tt.wantEnv)
			}
		})
	}
}

func runDraftTestBool(v bool) *bool {
	return &v
}
