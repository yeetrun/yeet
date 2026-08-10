// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestServiceSandboxPolicyForExactGenerationUsesExactGeneration(t *testing.T) {
	service := &db.Service{Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
		db.Gen(3): {State: "off", ReadOnly: []db.ServiceSandboxExposure{{Source: "/old", Destination: "/old"}}},
	}}}

	if got := mustServiceSandboxPolicyForExactGeneration(t, service, 4); got.State != "legacy" || len(got.ReadOnly) != 0 {
		t.Fatalf("generation 4 policy = %#v, want legacy", got)
	}
	got := mustServiceSandboxPolicyForExactGeneration(t, service, 3)
	if got.State != "off" || !reflect.DeepEqual(got.ReadOnly, []serviceSandboxExposure{{Source: "/old", Destination: "/old"}}) {
		t.Fatalf("generation 3 policy = %#v", got)
	}
	service.Sandbox.Refs[db.Gen(5)] = &db.ServiceSandboxPolicy{State: "legacy"}
	if _, err := serviceSandboxPolicyForExactGeneration(service, 5); err == nil || !strings.Contains(err.Error(), `sandbox state "legacy" is invalid`) {
		t.Fatalf("stored legacy policy error = %v, want fail-closed invalid state", err)
	}
}

func mustServiceSandboxPolicyForExactGeneration(t *testing.T, service *db.Service, generation int) serviceSandboxPolicy {
	t.Helper()
	policy, err := serviceSandboxPolicyForExactGeneration(service, generation)
	if err != nil {
		t.Fatalf("serviceSandboxPolicyForExactGeneration: %v", err)
	}
	return policy
}

func TestApplyServiceSandboxPolicyPatchResolvesState(t *testing.T) {
	ro := cli.SandboxExposure{Source: "/srv/input", Destination: "/input"}
	tests := []struct {
		name    string
		fresh   bool
		current serviceSandboxPolicy
		options cli.SandboxOptions
		want    serviceSandboxPolicy
		wantErr string
	}{
		{name: "fresh default", fresh: true, want: serviceSandboxPolicy{State: "on"}},
		{name: "fresh exposure implies on", fresh: true, options: cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{ro}}, want: serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{{Source: "/srv/input", Destination: "/input"}}}},
		{name: "fresh explicit off keeps dormant exposure", fresh: true, options: cli.SandboxOptions{StateSet: true, State: "off", ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{ro}}, want: serviceSandboxPolicy{State: "off", ReadOnly: []serviceSandboxExposure{{Source: "/srv/input", Destination: "/input"}}}},
		{name: "legacy exposure requires state", current: serviceSandboxPolicy{State: "legacy"}, options: cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{ro}}, wantErr: "explicit --sandbox=on or --sandbox=off"},
		{name: "off exposure activates", current: serviceSandboxPolicy{State: "off"}, options: cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{ro}}, want: serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{{Source: "/srv/input", Destination: "/input"}}}},
		{name: "explicit off keeps edit dormant", current: serviceSandboxPolicy{State: "off"}, options: cli.SandboxOptions{StateSet: true, State: "off", ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{ro}}, want: serviceSandboxPolicy{State: "off", ReadOnly: []serviceSandboxExposure{{Source: "/srv/input", Destination: "/input"}}}},
		{name: "on exposure remains on", current: serviceSandboxPolicy{State: "on"}, options: cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{ro}}, want: serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{{Source: "/srv/input", Destination: "/input"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyServiceSandboxPolicyPatch("api", tt.current, tt.fresh, tt.options)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply patch: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("policy = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyServiceSandboxPolicyPatchPreservesAndReplacesLists(t *testing.T) {
	current := serviceSandboxPolicy{
		State:    "on",
		ReadOnly: []serviceSandboxExposure{{Source: "/srv/b", Destination: "/b"}, {Source: "/srv/a", Destination: "/a"}},
		Writable: []serviceSandboxExposure{{Source: "/srv/cache", Destination: "/cache"}},
	}

	got, err := applyServiceSandboxPolicyPatch("api", current, false, cli.SandboxOptions{
		ReadOnlySet: true,
		ReadOnly: []cli.SandboxExposure{
			{Source: "/srv/new", Destination: "/new"},
			{Source: "/srv/a", Destination: "/a"},
			{Source: "/srv/b", Destination: "/b"},
		},
	})
	if err != nil {
		t.Fatalf("apply additive patch: %v", err)
	}
	want := serviceSandboxPolicy{
		State:    "on",
		ReadOnly: []serviceSandboxExposure{{Source: "/srv/a", Destination: "/a"}, {Source: "/srv/b", Destination: "/b"}, {Source: "/srv/new", Destination: "/new"}},
		Writable: []serviceSandboxExposure{{Source: "/srv/cache", Destination: "/cache"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policy = %#v, want %#v", got, want)
	}

	got, err = applyServiceSandboxPolicyPatch("api", current, false, cli.SandboxOptions{
		ReadOnlySet: true, ReadOnlyReset: true,
		ReadOnly: []cli.SandboxExposure{{Source: "/srv/new", Destination: "/new"}},
	})
	if err != nil {
		t.Fatalf("apply reset patch: %v", err)
	}
	want.ReadOnly = []serviceSandboxExposure{{Source: "/srv/new", Destination: "/new"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reset policy = %#v, want %#v", got, want)
	}
}

func TestApplyServiceSandboxPolicyPatchReplacementGuidanceReplays(t *testing.T) {
	tests := []struct {
		name            string
		service         string
		current         serviceSandboxPolicy
		options         cli.SandboxOptions
		wantExplanation string
		wantCommands    []string
		wantPolicies    []serviceSandboxPolicy
	}{
		{
			name:    "valid preservation and replacement",
			service: "api worker; echo unsafe",
			current: serviceSandboxPolicy{
				State:    "on",
				ReadOnly: []serviceSandboxExposure{{Source: "/srv/old input", Destination: "/old input"}},
				Writable: []serviceSandboxExposure{{Source: "/srv/old-cache", Destination: "/old-cache"}},
			},
			options: cli.SandboxOptions{
				ReadOnlySet: true,
				ReadOnly:    []cli.SandboxExposure{{Source: "/srv/new'$input", Destination: "/new input"}},
				WritableSet: true,
				Writable:    []cli.SandboxExposure{{Source: "/srv/new-cache", Destination: "/new-cache"}},
			},
			wantCommands: []string{
				`yeet service set 'api worker; echo unsafe' '--sandbox-ro=/srv/new'"'"'$input:/new input' '--sandbox-ro=/srv/old input:/old input' --sandbox-rw=/srv/new-cache:/new-cache --sandbox-rw=/srv/old-cache:/old-cache`,
				`yeet service set 'api worker; echo unsafe' --sandbox-ro=reset '--sandbox-ro=/srv/new'"'"'$input:/new input' --sandbox-rw=reset --sandbox-rw=/srv/new-cache:/new-cache`,
			},
			wantPolicies: []serviceSandboxPolicy{
				{
					State: "on",
					ReadOnly: []serviceSandboxExposure{
						{Source: "/srv/new'$input", Destination: "/new input"},
						{Source: "/srv/old input", Destination: "/old input"},
					},
					Writable: []serviceSandboxExposure{
						{Source: "/srv/new-cache", Destination: "/new-cache"},
						{Source: "/srv/old-cache", Destination: "/old-cache"},
					},
				},
				{
					State:    "on",
					ReadOnly: []serviceSandboxExposure{{Source: "/srv/new'$input", Destination: "/new input"}},
					Writable: []serviceSandboxExposure{{Source: "/srv/new-cache", Destination: "/new-cache"}},
				},
			},
		},
		{
			name:    "preservation impossible for different sources at one destination",
			service: "api",
			current: serviceSandboxPolicy{
				State:    "on",
				ReadOnly: []serviceSandboxExposure{{Source: "/srv/old", Destination: "/shared"}},
			},
			options: cli.SandboxOptions{
				ReadOnlySet: true,
				ReadOnly:    []cli.SandboxExposure{{Source: "/srv/new", Destination: "/shared"}},
			},
			wantExplanation: "preserving the existing sandbox entries is impossible",
			wantCommands:    []string{"yeet service set api --sandbox-ro=reset --sandbox-ro=/srv/new:/shared"},
			wantPolicies: []serviceSandboxPolicy{
				{
					State:    "on",
					ReadOnly: []serviceSandboxExposure{{Source: "/srv/new", Destination: "/shared"}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyServiceSandboxPolicyPatch(tt.service, tt.current, false, tt.options)
			if err == nil {
				t.Fatal("implicit removal succeeded")
			}
			if tt.wantExplanation != "" && !strings.Contains(err.Error(), tt.wantExplanation) {
				t.Fatalf("error %q does not contain %q", err, tt.wantExplanation)
			}

			commands := serviceSandboxGuidanceCommands(err)
			if !reflect.DeepEqual(commands, tt.wantCommands) {
				t.Fatalf("guidance commands = %q, want exact commands %q", commands, tt.wantCommands)
			}
			for index, command := range commands {
				got := replayServiceSandboxGuidanceCommand(t, command, tt.current)
				if !reflect.DeepEqual(got, tt.wantPolicies[index]) {
					t.Fatalf("command %q applies policy %#v, want %#v", command, got, tt.wantPolicies[index])
				}
			}
		})
	}
}

func serviceSandboxGuidanceCommands(err error) []string {
	var commands []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.HasPrefix(line, "yeet service set ") {
			commands = append(commands, line)
		}
	}
	return commands
}

func replayServiceSandboxGuidanceCommand(
	t *testing.T,
	command string,
	current serviceSandboxPolicy,
) serviceSandboxPolicy {
	t.Helper()

	script := "set -f\nyeet() { printf '%s\\000' \"$@\"; }\n" + command + "\n"
	process := exec.Command("/bin/sh", "-c", script) //nolint:gosec // Fixed shell harness validates generated commands.
	process.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := process.Output()
	if err != nil {
		t.Fatalf("execute guidance command %q: %v", command, err)
	}
	rawArguments := bytes.Split(output, []byte{0})
	if len(rawArguments) > 0 && len(rawArguments[len(rawArguments)-1]) == 0 {
		rawArguments = rawArguments[:len(rawArguments)-1]
	}
	arguments := make([]string, len(rawArguments))
	for index, argument := range rawArguments {
		arguments[index] = string(argument)
	}
	if len(arguments) < 3 || arguments[0] != "service" || arguments[1] != "set" {
		t.Fatalf("guidance command %q produced argv %q", command, arguments)
	}

	flags, positional, err := cli.ParseServiceSet(arguments[2:])
	if err != nil {
		t.Fatalf("parse guidance command %q argv %q: %v", command, arguments, err)
	}
	if len(positional) != 1 {
		t.Fatalf("guidance command %q positional arguments = %q, want one service", command, positional)
	}
	policy, err := applyServiceSandboxPolicyPatch(positional[0], current, false, flags.Sandbox)
	if err != nil {
		t.Fatalf("apply guidance command %q: %v", command, err)
	}
	return policy
}

func TestApplyServiceSandboxPolicyPatchTreatsSortedEqualityAsNoOp(t *testing.T) {
	current := serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{
		{Source: "/srv/b", Destination: "/b"},
		{Source: "/srv/a", Destination: "/a"},
	}}
	got, err := applyServiceSandboxPolicyPatch("api", current, false, cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{
		{Source: "/srv/a", Destination: "/a"},
		{Source: "/srv/b", Destination: "/b"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{
		{Source: "/srv/a", Destination: "/a"},
		{Source: "/srv/b", Destination: "/b"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policy = %#v, want normalized equality %#v", got, want)
	}
}

func TestApplyServiceSandboxPolicyPatchRejectsDuplicateAndCrossClassDestination(t *testing.T) {
	tests := []struct {
		name    string
		options cli.SandboxOptions
		want    string
	}{
		{name: "duplicate", options: cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{{Source: "/one", Destination: "/shared"}, {Source: "/one", Destination: "/shared"}}}, want: "duplicate sandbox destination /shared"},
		{name: "cross class", options: cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{{Source: "/one", Destination: "/shared"}}, WritableSet: true, Writable: []cli.SandboxExposure{{Source: "/two", Destination: "/shared"}}}, want: "sandbox destination /shared collides"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyServiceSandboxPolicyPatch("api", serviceSandboxPolicy{State: "off"}, true, tt.options)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func FuzzServiceSandboxPolicyNormalization(f *testing.F) {
	seeds := [][12]any{
		{uint8(3), uint8(0), uint8(0), uint8(0), uint8(0), uint8(0), true, false, false, false, false, false},
		{uint8(1), uint8(0), uint8(1), uint8(0), uint8(2), uint8(0), false, false, true, true, false, false},
		{uint8(1), uint8(1), uint8(0), uint8(0), uint8(1), uint8(0), false, true, true, true, false, false},
		{uint8(2), uint8(0), uint8(0), uint8(0), uint8(1), uint8(0), false, false, true, false, false, false},
		{uint8(2), uint8(0), uint8(0), uint8(0), uint8(1), uint8(0), false, true, true, true, false, false},
		{uint8(0), uint8(0), uint8(0), uint8(2), uint8(1), uint8(0), false, false, true, true, false, false},
		{uint8(0), uint8(0), uint8(1), uint8(0), uint8(2), uint8(0), false, false, true, false, false, false},
		{uint8(0), uint8(0), uint8(1), uint8(0), uint8(4), uint8(0), false, false, true, false, false, false},
		{uint8(4), uint8(0), uint8(0), uint8(0), uint8(0), uint8(0), false, false, false, false, false, false},
		{uint8(0), uint8(4), uint8(0), uint8(0), uint8(0), uint8(0), false, true, false, false, false, false},
		{uint8(0), uint8(0), uint8(0), uint8(0), uint8(5), uint8(0), false, false, true, true, false, false},
		{uint8(0), uint8(0), uint8(0), uint8(0), uint8(9), uint8(10), false, false, true, true, true, true},
		{uint8(0), uint8(0), uint8(0), uint8(0), uint8(3), uint8(0), false, false, true, true, false, false},
		{uint8(0), uint8(0), uint8(1), uint8(2), uint8(0), uint8(0), false, false, false, true, false, true},
	}
	for _, seed := range seeds {
		f.Add(seed[0].(uint8), seed[1].(uint8), seed[2].(uint8), seed[3].(uint8), seed[4].(uint8), seed[5].(uint8), seed[6].(bool), seed[7].(bool), seed[8].(bool), seed[9].(bool), seed[10].(bool), seed[11].(bool))
	}
	f.Fuzz(func(
		t *testing.T,
		currentStateIndex, requestedStateIndex, currentReadOnlyIndex, currentWritableIndex uint8,
		requestedReadOnlyIndex, requestedWritableIndex uint8,
		fresh, stateSet, readOnlySet, readOnlyReset, writableSet, writableReset bool,
	) {
		states := []string{"on", "off", "legacy", "", "invalid"}
		current := serviceSandboxPolicy{
			State:    states[int(currentStateIndex)%len(states)],
			ReadOnly: sandboxPolicyOracleList(currentReadOnlyIndex),
			Writable: sandboxPolicyOracleList(currentWritableIndex),
		}
		options := cli.SandboxOptions{
			State:         states[int(requestedStateIndex)%len(states)],
			StateSet:      stateSet,
			ReadOnly:      sandboxPolicyOracleCLIList(requestedReadOnlyIndex),
			ReadOnlySet:   readOnlySet,
			ReadOnlyReset: readOnlyReset,
			Writable:      sandboxPolicyOracleCLIList(requestedWritableIndex),
			WritableSet:   writableSet,
			WritableReset: writableReset,
		}
		want, outcome := sandboxPolicyOracleApply(current, fresh, options)
		got, err := applyServiceSandboxPolicyPatch("fuzz-service", current, fresh, options)
		assertServiceSandboxOracleOutcome(t, got, err, want, outcome)
		if outcome != sandboxPolicyOracleSuccess {
			return
		}

		next, err := applyServiceSandboxPolicyPatch("fuzz-service", got, false, cli.SandboxOptions{})
		if err != nil || !reflect.DeepEqual(got, next) {
			t.Fatalf("successful policy is unstable: first=%#v second=%#v error=%v", got, next, err)
		}
	})
}

type sandboxPolicyOracleOutcome uint8

const (
	sandboxPolicyOracleSuccess sandboxPolicyOracleOutcome = iota
	sandboxPolicyOracleInvalid
	sandboxPolicyOracleReplacementGuard
)

func sandboxPolicyOracleApply(
	current serviceSandboxPolicy,
	fresh bool,
	options cli.SandboxOptions,
) (serviceSandboxPolicy, sandboxPolicyOracleOutcome) {
	if fresh && current.State == "" {
		current.State = "legacy"
	}
	current, valid := sandboxPolicyOracleNormalize(current)
	if !valid {
		return serviceSandboxPolicy{}, sandboxPolicyOracleInvalid
	}
	state, valid := sandboxPolicyOracleState(current.State, fresh, options)
	if !valid {
		return serviceSandboxPolicy{}, sandboxPolicyOracleInvalid
	}
	requested := sandboxPolicyOracleRequested(current, state, fresh, options)
	requested, valid = sandboxPolicyOracleNormalize(requested)
	if !valid {
		return serviceSandboxPolicy{}, sandboxPolicyOracleInvalid
	}
	if sandboxPolicyOracleOmits(current, requested, options) {
		return serviceSandboxPolicy{}, sandboxPolicyOracleReplacementGuard
	}
	return requested, sandboxPolicyOracleSuccess
}

func sandboxPolicyOracleState(current string, fresh bool, options cli.SandboxOptions) (string, bool) {
	if options.StateSet {
		return options.State, options.State == "on" || options.State == "off"
	}
	if fresh {
		return "on", true
	}
	if !options.ReadOnlySet && !options.WritableSet {
		return current, true
	}
	if current == "legacy" {
		return "", false
	}
	return "on", true
}

func sandboxPolicyOracleRequested(
	current serviceSandboxPolicy,
	state string,
	fresh bool,
	options cli.SandboxOptions,
) serviceSandboxPolicy {
	requested := serviceSandboxPolicy{State: state, ReadOnly: current.ReadOnly, Writable: current.Writable}
	if fresh {
		requested.ReadOnly = nil
		requested.Writable = nil
	}
	if options.ReadOnlySet {
		requested.ReadOnly = sandboxPolicyOracleFromCLI(options.ReadOnly)
	}
	if options.WritableSet {
		requested.Writable = sandboxPolicyOracleFromCLI(options.Writable)
	}
	return requested
}

func sandboxPolicyOracleNormalize(policy serviceSandboxPolicy) (serviceSandboxPolicy, bool) {
	if policy.State != "legacy" && policy.State != "on" && policy.State != "off" {
		return serviceSandboxPolicy{}, false
	}
	readOnly, valid := sandboxPolicyOracleNormalizeList(policy.ReadOnly)
	if !valid {
		return serviceSandboxPolicy{}, false
	}
	writable, valid := sandboxPolicyOracleNormalizeList(policy.Writable)
	if !valid || !sandboxPolicyOracleDestinationsValid(readOnly, writable) {
		return serviceSandboxPolicy{}, false
	}
	return serviceSandboxPolicy{State: policy.State, ReadOnly: readOnly, Writable: writable}, true
}

func sandboxPolicyOracleNormalizeList(exposures []serviceSandboxExposure) ([]serviceSandboxExposure, bool) {
	if len(exposures) == 0 {
		return nil, true
	}
	normalized := append([]serviceSandboxExposure(nil), exposures...)
	for _, exposure := range normalized {
		if !sandboxPolicyOraclePathValid(exposure.Source) || !sandboxPolicyOraclePathValid(exposure.Destination) {
			return nil, false
		}
	}
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].Destination == normalized[right].Destination {
			return normalized[left].Source < normalized[right].Source
		}
		return normalized[left].Destination < normalized[right].Destination
	})
	return normalized, true
}

func sandboxPolicyOraclePathValid(path string) bool {
	if path == "" || path[0] != '/' || strings.Contains(path, ":") || strings.ContainsRune(path, '\x00') {
		return false
	}
	if path == "/" {
		return true
	}
	for _, part := range strings.Split(path[1:], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func sandboxPolicyOracleDestinationsValid(readOnly, writable []serviceSandboxExposure) bool {
	all := append(append([]serviceSandboxExposure(nil), readOnly...), writable...)
	for right := range all {
		for left := 0; left < right; left++ {
			if sandboxPolicyOracleDestinationsOverlap(all[left].Destination, all[right].Destination) {
				return false
			}
		}
	}
	return true
}

func sandboxPolicyOracleDestinationsOverlap(left, right string) bool {
	if left == right || left == "/" || right == "/" {
		return true
	}
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func sandboxPolicyOracleOmits(current, requested serviceSandboxPolicy, options cli.SandboxOptions) bool {
	readOnly := options.ReadOnlySet && !options.ReadOnlyReset && sandboxPolicyOracleListOmits(current.ReadOnly, requested.ReadOnly)
	writable := options.WritableSet && !options.WritableReset && sandboxPolicyOracleListOmits(current.Writable, requested.Writable)
	return readOnly || writable
}

func sandboxPolicyOracleListOmits(current, requested []serviceSandboxExposure) bool {
	for _, existing := range current {
		found := false
		for _, candidate := range requested {
			found = found || existing == candidate
		}
		if !found {
			return true
		}
	}
	return false
}

func assertServiceSandboxOracleOutcome(
	t *testing.T,
	got serviceSandboxPolicy,
	err error,
	want serviceSandboxPolicy,
	outcome sandboxPolicyOracleOutcome,
) {
	t.Helper()
	guard := err != nil && strings.Contains(err.Error(), "replacement would remove existing entries")
	switch outcome {
	case sandboxPolicyOracleSuccess:
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("policy = %#v, error = %v, want %#v", got, err, want)
		}
		for _, exposure := range append(append([]serviceSandboxExposure(nil), got.ReadOnly...), got.Writable...) {
			if exposure.Source == "reset" || exposure.Destination == "reset" {
				t.Fatalf("reset persisted as policy data: %#v", got)
			}
		}
	case sandboxPolicyOracleInvalid:
		if err == nil || guard {
			t.Fatalf("invalid input produced policy %#v, error %v", got, err)
		}
	case sandboxPolicyOracleReplacementGuard:
		if !guard {
			t.Fatalf("replacement guard input produced policy %#v, error %v", got, err)
		}
	}
}

func sandboxPolicyOracleList(index uint8) []serviceSandboxExposure {
	lists := [][]serviceSandboxExposure{
		nil,
		{{Source: "/source/a", Destination: "/a"}},
		{{Source: "/source/b", Destination: "/b"}},
		{{Source: "/source/b", Destination: "/b"}, {Source: "/source/a", Destination: "/a"}},
		{{Source: "/source/other", Destination: "/a"}},
		{{Source: "/source/a", Destination: "/a"}, {Source: "/source/a", Destination: "/a"}},
		{{Source: "relative", Destination: "/relative"}},
		{{Source: "/source/dirty", Destination: "/dirty/../path"}},
		{{Source: "/source/tree", Destination: "/tree"}, {Source: "/source/child", Destination: "/tree/child"}},
		{{Source: "/source/shared-one", Destination: "/shared"}},
		{{Source: "/source/shared-two", Destination: "/shared"}},
		{{Source: "reset", Destination: "/reset"}},
		{{Source: "/source/colon", Destination: "/colon:path"}},
	}
	selected := lists[int(index)%len(lists)]
	return append([]serviceSandboxExposure(nil), selected...)
}

func sandboxPolicyOracleCLIList(index uint8) []cli.SandboxExposure {
	return sandboxPolicyOracleToCLI(sandboxPolicyOracleList(index))
}

func sandboxPolicyOracleToCLI(exposures []serviceSandboxExposure) []cli.SandboxExposure {
	if len(exposures) == 0 {
		return nil
	}
	result := make([]cli.SandboxExposure, len(exposures))
	for index, exposure := range exposures {
		result[index] = cli.SandboxExposure{Source: exposure.Source, Destination: exposure.Destination}
	}
	return result
}

func sandboxPolicyOracleFromCLI(exposures []cli.SandboxExposure) []serviceSandboxExposure {
	if len(exposures) == 0 {
		return nil
	}
	result := make([]serviceSandboxExposure, len(exposures))
	for index, exposure := range exposures {
		result[index] = serviceSandboxExposure{Source: exposure.Source, Destination: exposure.Destination}
	}
	return result
}
