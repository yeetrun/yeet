// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestProbeServiceSandboxRunsOnlyTrueUnderWorkloadCredential(t *testing.T) {
	plan := serviceSandboxPlan{
		Executable: "/bin/sh",
		Arguments:  []string{"--ro-bind", "/payload", "/payload", "--chdir", "/data", "--"},
	}
	original := append([]string(nil), plan.Arguments...)
	var got serviceSandboxCommand
	runner := func(_ context.Context, command serviceSandboxCommand) ([]byte, error) {
		got = command
		return nil, nil
	}

	if err := probeServiceSandboxWith(context.Background(), plan, 123, 456, runner); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--ro-bind", "/payload", "/payload", "--chdir", "/data", "--", "/usr/bin/true"}
	if got.Path != "/usr/bin/bwrap" || !reflect.DeepEqual(got.Arguments, wantArgs) {
		t.Fatalf("command = %#v, want direct Bubblewrap with %#v", got, wantArgs)
	}
	if got.Credential == nil || got.Credential.Uid != 123 || got.Credential.Gid != 456 {
		t.Fatalf("credential = %#v, want UID 123 GID 456", got.Credential)
	}
	if !reflect.DeepEqual(plan.Arguments, original) {
		t.Fatalf("probe mutated plan arguments: got %#v want %#v", plan.Arguments, original)
	}
	joined := strings.Join(got.Arguments, " ")
	for _, forbidden := range []string{"sh -c", "--clearenv", "--unshare-net", "--unshare-all", "--unshare-cgroup"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("probe contains forbidden payload/policy %q: %s", forbidden, joined)
		}
	}
	if !reflect.DeepEqual(got.Arguments[len(got.Arguments)-2:], []string{"--", "/usr/bin/true"}) {
		t.Fatalf("probe executed something other than true: %#v", got.Arguments)
	}
}

func TestProbeServiceSandboxPreservesRawDiagnostic(t *testing.T) {
	raw := []byte("bwrap: permission denied\nsecond line\n")
	runner := func(context.Context, serviceSandboxCommand) ([]byte, error) {
		return raw, errors.New("exit status 1")
	}
	err := probeServiceSandboxWith(context.Background(), serviceSandboxPlan{Arguments: []string{"--"}}, 7, 8, runner)
	if err == nil {
		t.Fatal("probe succeeded")
	}
	for _, want := range []string{"service sandbox workload probe failed for UID 7 GID 8", string(raw), "exit status 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestProbeServiceSandboxRejectsPlanWithoutTerminalSeparator(t *testing.T) {
	called := false
	err := probeServiceSandboxWith(context.Background(), serviceSandboxPlan{Arguments: []string{"--chdir", "/data"}}, 1, 2, func(context.Context, serviceSandboxCommand) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "terminal --") {
		t.Fatalf("error = %v, want terminal separator error", err)
	}
	if called {
		t.Fatal("runner called for malformed plan")
	}
}

func TestVerifyGeneratedSystemdUnitUsesSeparateArgvWithoutShell(t *testing.T) {
	unitPath := "/tmp/api worker;$(touch nope).service"
	var got serviceSandboxCommand
	err := verifyGeneratedSystemdUnitWith(context.Background(), unitPath, func(_ context.Context, command serviceSandboxCommand) ([]byte, error) {
		got = command
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/usr/bin/systemd-analyze" || !reflect.DeepEqual(got.Arguments, []string{"verify", unitPath}) || got.Credential != nil {
		t.Fatalf("command = %#v", got)
	}
}

func TestVerifyGeneratedSystemdUnitPreservesRawDiagnostic(t *testing.T) {
	raw := []byte("unit:4: Unknown key\n")
	err := verifyGeneratedSystemdUnitWith(context.Background(), "/tmp/api.service", func(context.Context, serviceSandboxCommand) ([]byte, error) {
		return raw, errors.New("exit status 1")
	})
	if err == nil {
		t.Fatal("verification succeeded")
	}
	for _, want := range []string{"verify generated systemd unit /tmp/api.service", string(raw), "exit status 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
