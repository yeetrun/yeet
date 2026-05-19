// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestResolveDockerUpdateTargets(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "foo", Host: "host-a"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-b"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-c"})
	loc := &projectConfigLocation{Config: cfg}

	targets, errs := resolveDockerUpdateTargets([]string{"foo", "bar@host-d", "bar@host-d", "baz", "amb"}, loc, "", false)
	want := []dockerUpdateTarget{
		{Service: "foo", Host: "host-a"},
		{Service: "bar", Host: "host-d"},
		{Service: "baz", Host: "default-host"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "amb@host-b") || !strings.Contains(errs[0].Error(), "amb@host-c") {
		t.Fatalf("errs = %#v, want ambiguous amb error", errs)
	}
}

func TestResolveDockerUpdateTargetsHostOverrideWins(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "foo", Host: "configured-host"})
	loc := &projectConfigLocation{Config: cfg}

	targets, errs := resolveDockerUpdateTargets([]string{"foo", "bar@host-b"}, loc, "override-host", true)
	if len(errs) != 0 {
		t.Fatalf("errs = %#v, want none", errs)
	}
	want := []dockerUpdateTarget{
		{Service: "foo", Host: "override-host"},
		{Service: "bar", Host: "host-b"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestDockerUpdateExplicitTargetsRunsAllAndJoinsErrors(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"

	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{Name: "foo", Host: "host-a"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-b"})
	cfg.SetServiceEntry(ServiceEntry{Name: "amb", Host: "host-c"})
	loc := &projectConfigLocation{Config: cfg}

	var updated []string
	updateDockerServiceForHostFn = func(ctx context.Context, host string, service string) error {
		updated = append(updated, host+"/"+service)
		if service == "bad" {
			return errors.New("remote update failed")
		}
		return nil
	}

	out, err := captureSvcStdout(t, func() error {
		return dockerUpdateExplicitTargets(context.Background(), svcCommandRequest{
			Config: loc,
		}, []string{"foo", "bad@host-d", "amb", "foo"})
	})
	if err == nil {
		t.Fatal("dockerUpdateExplicitTargets error = nil, want joined errors")
	}
	if !strings.Contains(err.Error(), "remote update failed") || !strings.Contains(err.Error(), "amb@host-b") {
		t.Fatalf("joined error = %v, want remote and ambiguous errors", err)
	}
	wantUpdated := []string{"host-a/foo", "host-d/bad"}
	if !reflect.DeepEqual(updated, wantUpdated) {
		t.Fatalf("updated = %#v, want %#v", updated, wantUpdated)
	}
	for _, want := range []string{"==> host-a/foo", "==> host-d/bad"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDockerUpdateExplicitTargetSingleKeepsExistingOutput(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	loadedPrefs.DefaultHost = "default-host"

	var updated []string
	updateDockerServiceForHostFn = func(ctx context.Context, host string, service string) error {
		updated = append(updated, host+"/"+service)
		fmt.Println("streamed compose output")
		return nil
	}

	out, err := captureSvcStdout(t, func() error {
		return dockerUpdateExplicitTargets(context.Background(), svcCommandRequest{}, []string{"foo@host-a"})
	})
	if err != nil {
		t.Fatalf("dockerUpdateExplicitTargets: %v", err)
	}
	if !reflect.DeepEqual(updated, []string{"host-a/foo"}) {
		t.Fatalf("updated = %#v, want host-a/foo", updated)
	}
	if strings.Contains(out, "==>") {
		t.Fatalf("single target output should not include marker:\n%s", out)
	}
	if !strings.Contains(out, "streamed compose output") {
		t.Fatalf("streamed output missing:\n%s", out)
	}
}
