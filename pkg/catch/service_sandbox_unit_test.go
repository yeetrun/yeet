// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestTask6BRenderNativeSandboxUnitFreshNativeKinds(t *testing.T) {
	for _, tt := range []struct {
		name       string
		payloadExt string
		timer      bool
	}{
		{name: "binary"},
		{name: "shebang script", payloadExt: ".sh"},
		{name: "timer-backed shebang script", payloadExt: ".sh", timer: true},
	} {
		t.Run(tt.name, func(t *testing.T) { task6BRunFreshNativeKind(t, tt.payloadExt, tt.timer) })
	}
}

func task6BRunFreshNativeKind(t *testing.T, payloadExt string, timer bool) {
	t.Helper()
	fixture := newTask6BUnitFixture(t, "api"+payloadExt)
	raw := fixture.directUnit([]string{"--serve", "argument with space"}, true)
	if timer {
		raw = strings.Replace(raw, "[Unit]\n", "[Unit]\nPartOf=api.timer\n", 1)
	}
	unit, plan, err := renderNativeSandboxUnit(raw, nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "legacy"}, TargetPolicy: serviceSandboxPolicy{State: "on"},
		Identity: fixture.identity, Payload: fixture.payload, DataDir: fixture.data, Resolver: fixture.resolver, Hostname: "api",
	})
	if err != nil {
		t.Fatalf("renderNativeSandboxUnit: %v", err)
	}
	if plan == nil {
		t.Fatal("sandboxed unit returned nil plan")
	}
	task6BAssertFreshNativeArgv(t, fixture, plan, unit)
	task6BAssertFreshNativeUnit(t, fixture, unit, timer)
	task6BAssertFreshNativeResolver(t, fixture, plan, unit)
}

func task6BAssertFreshNativeArgv(t *testing.T, fixture task6BUnitFixture, plan *serviceSandboxPlan, unit string) {
	t.Helper()
	argv := task6BExecStart(t, unit)
	want := append([]string{bubblewrapPath}, plan.Arguments...)
	want = append(want, fixture.payload, "--serve", "argument with space")
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("ExecStart argv = %#v, want %#v", argv, want)
	}
	if task6BCount(argv, "--") != 1 {
		t.Fatalf("ExecStart separators = %d, want exactly one: %#v", task6BCount(argv, "--"), argv)
	}
}

func task6BAssertFreshNativeUnit(t *testing.T, fixture task6BUnitFixture, unit string, timer bool) {
	t.Helper()
	for _, want := range []string{
		"ConditionFileIsExecutable=" + fixture.payload + "\n", "WorkingDirectory=/\n",
		"Environment=HOME=" + fixture.data + " USER=70000 LOGNAME=70000 SHELL=/bin/sh\n",
		"User=70000\n", "Group=70001\n", "NetworkNamespacePath=/var/run/netns/yeet-api-ns\n",
		"Requires=yeet-api-ns.service\n", "After=yeet-api-ns.service\n",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("sandboxed unit missing %q:\n%s", want, unit)
		}
	}
	if timer && !strings.Contains(unit, "PartOf=api.timer\n") {
		t.Fatalf("timer relationship was not preserved:\n%s", unit)
	}
}

func task6BAssertFreshNativeResolver(t *testing.T, fixture task6BUnitFixture, plan *serviceSandboxPlan, unit string) {
	t.Helper()
	if !task6BPlanBindsResolver(plan, fixture.resolver) {
		t.Fatalf("sandbox plan does not bind resolver %q to /etc/resolv.conf: %#v", fixture.resolver, plan.Mounts)
	}
	for _, forbidden := range []string{"BindReadOnlyPaths=" + fixture.resolver + ":/etc/resolv.conf", "PrivateMounts=yes"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("sandboxed unit retained systemd resolver directive %q:\n%s", forbidden, unit)
		}
	}
}

func TestTask6DNativeSandboxUnitRequestKeepsPlannedInterface(t *testing.T) {
	typ := reflect.TypeOf(nativeSandboxUnitRequest{})
	want := []string{"CurrentPolicy", "TargetPolicy", "Identity", "Payload", "DataDir", "Resolver", "Hostname"}
	if typ.NumField() != len(want) {
		t.Fatalf("nativeSandboxUnitRequest fields = %d, want %d", typ.NumField(), len(want))
	}
	for index, name := range want {
		if got := typ.Field(index).Name; got != name {
			t.Fatalf("nativeSandboxUnitRequest field %d = %q, want %q", index, got, name)
		}
	}
}

func TestTask6BRenderNativeSandboxUnitTransitionsPreserveManagedBytes(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	payloadArgs := []string{"--config", "/etc/api config", "dollar$value", "percent%value"}

	t.Run("off to on", func(t *testing.T) { task6BTransitionOffToOn(t, fixture, payloadArgs) })
	t.Run("on to off recovers exact payload argv", func(t *testing.T) { task6BTransitionOnToOff(t, fixture, payloadArgs) })
	t.Run("legacy to off remains direct", func(t *testing.T) { task6BTransitionLegacyToOff(t, fixture, payloadArgs) })
}

func task6BTransitionOffToOn(t *testing.T, fixture task6BUnitFixture, payloadArgs []string) {
	t.Helper()
	unit, plan, err := renderNativeSandboxUnit(fixture.directUnit(payloadArgs, true), nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "off"}, TargetPolicy: serviceSandboxPolicy{State: "on"},
		Identity: fixture.identity, Payload: fixture.payload, DataDir: fixture.data, Resolver: fixture.resolver, Hostname: "api",
	})
	if err != nil {
		t.Fatalf("renderNativeSandboxUnit: %v", err)
	}
	want := append([]string{bubblewrapPath}, plan.Arguments...)
	want = append(want, append([]string{fixture.payload}, payloadArgs...)...)
	if got := task6BExecStart(t, unit); !reflect.DeepEqual(got, want) {
		t.Fatalf("off-to-on argv = %#v, want %#v", got, want)
	}
	assertTask6BPreservedUnitBytes(t, unit)
}

func task6BTransitionOnToOff(t *testing.T, fixture task6BUnitFixture, payloadArgs []string) {
	t.Helper()
	currentPlan := task6BPlan(t, fixture, serviceSandboxPolicy{State: "on"})
	currentArgv := append([]string{bubblewrapPath}, currentPlan.Arguments...)
	currentArgv = append(currentArgv, append([]string{fixture.payload}, payloadArgs...)...)
	unit, plan, err := renderNativeSandboxUnit(fixture.unitWithArgv(currentArgv, "/", true), nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "on"}, TargetPolicy: serviceSandboxPolicy{State: "off"},
		Identity: fixture.identity, Payload: fixture.payload, DataDir: fixture.data, Resolver: fixture.resolver, Hostname: "api",
	})
	if err != nil {
		t.Fatalf("renderNativeSandboxUnit: %v", err)
	}
	if plan != nil {
		t.Fatalf("direct target returned sandbox plan %#v", plan)
	}
	if got, want := task6BExecStart(t, unit), append([]string{fixture.payload}, payloadArgs...); !reflect.DeepEqual(got, want) {
		t.Fatalf("on-to-off argv = %#v, want %#v", got, want)
	}
	task6BAssertDirectTransitionUnit(t, fixture, unit)
	assertTask6BPreservedUnitBytes(t, unit)
}

func task6BAssertDirectTransitionUnit(t *testing.T, fixture task6BUnitFixture, unit string) {
	t.Helper()
	for _, want := range []string{
		"WorkingDirectory=" + fixture.data + "\n",
		"Environment=HOME=" + fixture.data + " USER=70000 LOGNAME=70000 SHELL=/bin/sh\n",
		"BindReadOnlyPaths=" + fixture.resolver + ":/etc/resolv.conf\n", "PrivateMounts=yes\n",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("direct unit missing %q:\n%s", want, unit)
		}
	}
}

func task6BTransitionLegacyToOff(t *testing.T, fixture task6BUnitFixture, payloadArgs []string) {
	t.Helper()
	raw := fixture.directUnit(payloadArgs, true)
	unit, plan, err := renderNativeSandboxUnit(raw, nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "legacy"}, TargetPolicy: serviceSandboxPolicy{State: "off"},
		Identity: fixture.identity, Payload: fixture.payload, DataDir: fixture.data, Resolver: fixture.resolver, Hostname: "api",
	})
	if err != nil {
		t.Fatalf("renderNativeSandboxUnit: %v", err)
	}
	if plan != nil {
		t.Fatalf("legacy direct target returned plan %#v", plan)
	}
	if unit != raw {
		t.Fatalf("legacy-to-off direct unit changed bytes:\n--- want\n%s\n--- got\n%s", raw, unit)
	}
}

func TestTask6BRenderNativeSandboxUnitRejectsAmbiguousOrUnmanagedExecStart(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	validPlan := task6BPlan(t, fixture, serviceSandboxPolicy{State: "on"})
	validOn := append([]string{bubblewrapPath}, validPlan.Arguments...)
	validOn = append(validOn, fixture.payload, "--serve")
	multipleExecStart := fixture.directUnit(nil, true)
	multipleExecStart = strings.Replace(multipleExecStart,
		"ExecStart="+fixture.payload+"\n",
		"ExecStart="+fixture.payload+" --first\nExecStart="+fixture.payload+" --second\n", 1)

	tests := []struct {
		name    string
		policy  serviceSandboxPolicy
		raw     string
		wantErr string
	}{
		{name: "missing ExecStart", policy: serviceSandboxPolicy{State: "off"}, raw: "[Service]\nWorkingDirectory=" + fixture.data + "\n", wantErr: "exactly one"},
		{name: "multiple ExecStart", policy: serviceSandboxPolicy{State: "off"}, raw: multipleExecStart, wantErr: "exactly one"},
		{name: "malformed ExecStart", policy: serviceSandboxPolicy{State: "off"}, raw: "[Service]\nExecStart=\"unterminated\n", wantErr: "parse"},
		{name: "direct wrong payload", policy: serviceSandboxPolicy{State: "off"}, raw: fixture.unitWithArgv([]string{filepath.Join(fixture.root, "other")}, fixture.data, true), wantErr: "active binary"},
		{name: "sandbox wrong bwrap", policy: serviceSandboxPolicy{State: "on"}, raw: fixture.unitWithArgv(append([]string{"/opt/bwrap"}, validOn[1:]...), "/", true), wantErr: bubblewrapPath},
		{name: "sandbox missing separator", policy: serviceSandboxPolicy{State: "on"}, raw: fixture.unitWithArgv([]string{bubblewrapPath, "--unshare-user", fixture.payload}, "/", true), wantErr: "separator"},
		{name: "sandbox multiple separators", policy: serviceSandboxPolicy{State: "on"}, raw: fixture.unitWithArgv([]string{bubblewrapPath, "--", "--", fixture.payload}, "/", true), wantErr: "separator"},
		{name: "sandbox wrong recovered payload", policy: serviceSandboxPolicy{State: "on"}, raw: fixture.unitWithArgv(append(append([]string(nil), validOn[:len(validOn)-2]...), filepath.Join(fixture.root, "other"), "--serve"), "/", true), wantErr: "active binary"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := renderNativeSandboxUnit(tt.raw, nativeSandboxUnitRequest{
				CurrentPolicy: tt.policy,
				TargetPolicy:  serviceSandboxPolicy{State: "off"},
				Identity:      fixture.identity,
				Payload:       fixture.payload,
				DataDir:       fixture.data,
				Resolver:      fixture.resolver,
				Hostname:      "api",
			})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestTask6BRenderNativeSandboxUnitPreservesFinalNewlineAndUnownedMountDirectives(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	for _, finalNewline := range []bool{false, true} {
		name := "without final newline"
		if finalNewline {
			name = "with final newline"
		}
		t.Run(name, func(t *testing.T) {
			raw := fixture.directUnit([]string{"--serve"}, finalNewline)
			raw = strings.Replace(raw,
				"BindReadOnlyPaths="+fixture.resolver+":/etc/resolv.conf\n",
				"BindReadOnlyPaths=/srv/operator:/mnt/operator\nBindReadOnlyPaths="+fixture.resolver+":/etc/resolv.conf\n", 1)
			unit, _, err := renderNativeSandboxUnit(raw, nativeSandboxUnitRequest{
				CurrentPolicy: serviceSandboxPolicy{State: "off"},
				TargetPolicy:  serviceSandboxPolicy{State: "on"},
				Identity:      fixture.identity,
				Payload:       fixture.payload,
				DataDir:       fixture.data,
				Resolver:      fixture.resolver,
				Hostname:      "api",
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(unit, "\n") != finalNewline {
				t.Fatalf("final newline changed: input=%t output=%t", finalNewline, strings.HasSuffix(unit, "\n"))
			}
			if !strings.Contains(unit, "BindReadOnlyPaths=/srv/operator:/mnt/operator\n") || !strings.Contains(unit, "PrivateMounts=yes\n") {
				t.Fatalf("unowned mount directives were removed:\n%s", unit)
			}
			if strings.Contains(unit, "BindReadOnlyPaths="+fixture.resolver+":/etc/resolv.conf") {
				t.Fatalf("resolver-owned bind remains:\n%s", unit)
			}
		})
	}
}

func TestTask6ReviewTransformerRejectsAmbiguousResolverOwnership(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	resolverLine := "BindReadOnlyPaths=" + fixture.resolver + ":/etc/resolv.conf"
	direct := fixture.directUnit([]string{"--serve"}, true)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "duplicate resolver binds", raw: strings.Replace(direct, resolverLine, resolverLine+"\n"+resolverLine, 1)},
		{name: "empty reset before resolver", raw: strings.Replace(direct, resolverLine, "BindReadOnlyPaths=\n"+resolverLine, 1)},
		{name: "empty reset without resolver", raw: strings.Replace(direct, resolverLine, "BindReadOnlyPaths=", 1)},
		{name: "multiple values share resolver directive", raw: strings.Replace(direct, resolverLine, resolverLine+" /srv/operator:/etc/resolv.conf", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task6ReviewRequireTransformerError(t, fixture, tt.raw, "BindReadOnlyPaths")
		})
	}
}

func TestTask6ReviewTransformerPreservesOperatorPrivateMounts(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	resolverLine := "BindReadOnlyPaths=" + fixture.resolver + ":/etc/resolv.conf\n"
	raw := strings.Replace(fixture.directUnit([]string{"--serve"}, true), resolverLine, "", 1)
	unit := task6ReviewRenderOn(t, fixture, raw)
	if !strings.Contains(unit, "PrivateMounts=yes\n") {
		t.Fatalf("operator-only PrivateMounts was removed:\n%s", unit)
	}
}

func TestTask6ReviewTransformerPreservesOtherMountFamilies(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	resolverLine := "BindReadOnlyPaths=" + fixture.resolver + ":/etc/resolv.conf\n"
	operatorDirectives := []string{
		"BindPaths=/srv/rw:/mnt/rw",
		"TemporaryFileSystem=/scratch:rw",
		"ReadOnlyPaths=/etc/operator",
		"InaccessiblePaths=/secret",
		"ProtectSystem=strict",
	}
	replacement := strings.Join(operatorDirectives, "\n") + "\n"
	raw := strings.Replace(fixture.directUnit([]string{"--serve"}, true), resolverLine, replacement, 1)
	unit := task6ReviewRenderOn(t, fixture, raw)
	for _, directive := range append(operatorDirectives, "PrivateMounts=yes") {
		if !strings.Contains(unit, directive+"\n") {
			t.Fatalf("operator mount directive %q was removed:\n%s", directive, unit)
		}
	}
}

func TestTask6ReviewTransformerQuotedIdentityNeverRemainsStale(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	managed := "Environment=HOME=" + fixture.data + " USER=70000 LOGNAME=70000 SHELL=/bin/sh"
	quoted := `Environment="HOME=/old data" "USER=old user" "LOGNAME=old user" "SHELL=/bin/bash"`
	raw := strings.Replace(fixture.directUnit([]string{"--serve"}, true), managed, quoted, 1)
	unit, _, err := renderNativeSandboxUnit(raw, task6ReviewOnRequest(fixture))
	if err != nil {
		task6ReviewRequireRelevantError(t, err, "Environment", "identity", "ambiguous")
		return
	}
	if strings.Contains(unit, "/old data") || strings.Contains(unit, "old user") || strings.Contains(unit, "/bin/bash") {
		t.Fatalf("quoted managed identity was left stale:\n%s", unit)
	}
	want := "Environment=HOME=" + fixture.data + " USER=70000 LOGNAME=70000 SHELL=/bin/sh\n"
	if !strings.Contains(unit, want) {
		t.Fatalf("quoted managed identity was not safely canonicalized to %q:\n%s", want, unit)
	}
}

func TestTask6ReviewTransformerRejectsIdentityWithExtraAssignment(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	managed := "Environment=HOME=" + fixture.data + " USER=70000 LOGNAME=70000 SHELL=/bin/sh"
	ambiguous := "Environment=HOME=" + fixture.data + " USER=70000 LOGNAME=70000 APP_MODE=production SHELL=/bin/sh"
	raw := strings.Replace(fixture.directUnit([]string{"--serve"}, true), managed, ambiguous, 1)
	task6ReviewRequireTransformerError(t, fixture, raw, "Environment")
}

func TestTask6ReviewTransformerPreservesExtendedMountFamilies(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	resolverLine := "BindReadOnlyPaths=" + fixture.resolver + ":/etc/resolv.conf"
	for _, directive := range []string{
		"ReadWritePaths=/srv/operator-rw",
		"NoExecPaths=/srv/operator-noexec",
		"ExecPaths=/srv/operator-exec",
	} {
		t.Run(strings.SplitN(directive, "=", 2)[0], func(t *testing.T) {
			raw := strings.Replace(fixture.directUnit([]string{"--serve"}, true), resolverLine, directive+"\n"+resolverLine, 1)
			unit := task6ReviewRenderOn(t, fixture, raw)
			if !strings.Contains(unit, directive+"\n") || !strings.Contains(unit, "PrivateMounts=yes\n") {
				t.Fatalf("operator mount ownership was removed for %q:\n%s", directive, unit)
			}
		})
	}
}

func TestTask6ReviewTransformerRejectsMultilineAndDuplicateManagedDirectives(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	direct := fixture.directUnit([]string{"--serve"}, true)
	identity := "Environment=HOME=" + fixture.data + " USER=70000 LOGNAME=70000 SHELL=/bin/sh"
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "continued identity", raw: strings.Replace(direct, identity, "Environment=HOME="+fixture.data+" USER=70000 \\\n LOGNAME=70000 SHELL=/bin/sh", 1), wantErr: "Environment"},
		{name: "duplicate identity", raw: strings.Replace(direct, identity, identity+"\n"+identity, 1), wantErr: "Environment"},
		{name: "duplicate working directory", raw: strings.Replace(direct, "WorkingDirectory="+fixture.data, "WorkingDirectory="+fixture.data+"\nWorkingDirectory=/operator", 1), wantErr: "WorkingDirectory"},
		{name: "duplicate private mounts", raw: strings.Replace(direct, "PrivateMounts=yes", "PrivateMounts=yes\nPrivateMounts=yes", 1), wantErr: "PrivateMounts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task6ReviewRequireTransformerError(t, fixture, tt.raw, tt.wantErr)
		})
	}
}

func TestTask6ReviewTransformerRejectsInvalidPolicyStates(t *testing.T) {
	fixture := newTask6BUnitFixture(t, "api")
	raw := fixture.directUnit([]string{"--serve"}, true)
	tests := []struct {
		name    string
		current string
		target  string
	}{
		{name: "invalid current", current: "bogus", target: "off"},
		{name: "invalid target", current: "legacy", target: "bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := task6ReviewOnRequest(fixture)
			req.CurrentPolicy.State = tt.current
			req.TargetPolicy.State = tt.target
			_, _, err := renderNativeSandboxUnit(raw, req)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "state") {
				t.Fatalf("policy states current=%q target=%q error = %v, want explicit state rejection", tt.current, tt.target, err)
			}
		})
	}
}

func task6ReviewOnRequest(fixture task6BUnitFixture) nativeSandboxUnitRequest {
	return nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "off"}, TargetPolicy: serviceSandboxPolicy{State: "on"},
		Identity: fixture.identity, Payload: fixture.payload, DataDir: fixture.data, Resolver: fixture.resolver, Hostname: "api",
	}
}

func task6ReviewRenderOn(t *testing.T, fixture task6BUnitFixture, raw string) string {
	t.Helper()
	unit, _, err := renderNativeSandboxUnit(raw, task6ReviewOnRequest(fixture))
	if err != nil {
		t.Fatalf("renderNativeSandboxUnit: %v", err)
	}
	return unit
}

func task6ReviewRequireTransformerError(t *testing.T, fixture task6BUnitFixture, raw, want string) {
	t.Helper()
	_, _, err := renderNativeSandboxUnit(raw, task6ReviewOnRequest(fixture))
	if err == nil {
		t.Fatalf("renderNativeSandboxUnit accepted ambiguous %s unit:\n%s", want, raw)
	}
	task6ReviewRequireRelevantError(t, err, want, "ambiguous", "duplicate", "multiline", "continuation")
}

func task6ReviewRequireRelevantError(t *testing.T, err error, fragments ...string) {
	t.Helper()
	message := strings.ToLower(err.Error())
	for _, fragment := range fragments {
		if strings.Contains(message, strings.ToLower(fragment)) {
			return
		}
	}
	t.Fatalf("error = %v, want one of %q", err, fragments)
}

type task6BUnitFixture struct {
	root     string
	payload  string
	data     string
	resolver string
	identity db.ServiceIdentity
}

func newTask6BUnitFixture(t *testing.T, payloadName string) task6BUnitFixture {
	t.Helper()
	root := t.TempDir()
	payload := filepath.Join(root, payloadName)
	data := filepath.Join(root, "data")
	resolver := filepath.Join(root, "netns-resolv.conf")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolver, []byte("nameserver 192.0.2.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return task6BUnitFixture{
		root: root, payload: payload, data: data, resolver: resolver,
		identity: db.ServiceIdentity{RequestedUser: "70000", RequestedGroup: "70001", UID: 70000, GID: 70001},
	}
}

func (f task6BUnitFixture) directUnit(args []string, finalNewline bool) string {
	return f.unitWithArgv(append([]string{f.payload}, args...), f.data, finalNewline)
}

func (f task6BUnitFixture) unitWithArgv(argv []string, workingDirectory string, finalNewline bool) string {
	execStart, err := svc.RenderSystemdExecStart(argv)
	if err != nil {
		panic(err)
	}
	unit := fmt.Sprintf(`[Unit]
Description=api
Requires=yeet-api-ns.service
After=yeet-api-ns.service
# preserve this comment

[Service]
ConditionFileIsExecutable=%s
ExecStart=%s
WorkingDirectory=%s
Restart=always
Environment=APP_MODE=production
Environment=HOME=%s USER=70000 LOGNAME=70000 SHELL=/bin/sh
User=70000
Group=70001
NetworkNamespacePath=/var/run/netns/yeet-api-ns
BindReadOnlyPaths=%s:/etc/resolv.conf
PrivateMounts=yes

[Install]
WantedBy=multi-user.target
`, f.payload, execStart, workingDirectory, f.data, f.resolver)
	if !finalNewline {
		unit = strings.TrimSuffix(unit, "\n")
	}
	return unit
}

func task6BPlan(t *testing.T, fixture task6BUnitFixture, policy serviceSandboxPolicy) serviceSandboxPlan {
	t.Helper()
	plan, err := buildServiceSandboxPlan(serviceSandboxPlanRequest{
		Service:        "api",
		Policy:         policy,
		Payload:        fixture.payload,
		DataDir:        fixture.data,
		ResolverSource: fixture.resolver,
		UID:            fixture.identity.UID,
		GID:            fixture.identity.GID,
		Hostname:       "api",
	})
	if err != nil {
		t.Fatalf("buildServiceSandboxPlan: %v", err)
	}
	return plan
}

func task6BExecStart(t *testing.T, unit string) []string {
	t.Helper()
	var values []string
	inService := false
	for _, line := range strings.Split(unit, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inService = trimmed == "[Service]"
			continue
		}
		if inService && strings.HasPrefix(trimmed, "ExecStart=") {
			values = append(values, strings.TrimPrefix(trimmed, "ExecStart="))
		}
	}
	if len(values) != 1 {
		t.Fatalf("Service ExecStart values = %#v, want exactly one\n%s", values, unit)
	}
	argv, err := svc.ParseSystemdExecStart(values[0])
	if err != nil {
		t.Fatalf("ParseSystemdExecStart(%q): %v", values[0], err)
	}
	return argv
}

func task6BCount(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func task6BPlanBindsResolver(plan *serviceSandboxPlan, source string) bool {
	if plan == nil {
		return false
	}
	for _, mount := range plan.Mounts {
		if mount.Source == source && mount.Destination == "/etc/resolv.conf" && !mount.Writable {
			return true
		}
	}
	return false
}

func assertTask6BPreservedUnitBytes(t *testing.T, unit string) {
	t.Helper()
	for _, want := range []string{
		"Requires=yeet-api-ns.service\n",
		"After=yeet-api-ns.service\n",
		"# preserve this comment\n",
		"Restart=always\n",
		"Environment=APP_MODE=production\n",
		"NetworkNamespacePath=/var/run/netns/yeet-api-ns\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("managed transformer removed %q:\n%s", want, unit)
		}
	}
}
