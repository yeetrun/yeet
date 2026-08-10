// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

const sandboxNativePayloadOnlyMessage = "--sandbox, --sandbox-ro, and --sandbox-rw apply only to native binaries, scripts, and scheduled jobs"

func serviceSandboxPolicyToDB(policy serviceSandboxPolicy) *db.ServiceSandboxPolicy {
	stored := &db.ServiceSandboxPolicy{State: policy.State}
	if len(policy.ReadOnly) != 0 {
		stored.ReadOnly = make([]db.ServiceSandboxExposure, len(policy.ReadOnly))
		for index, exposure := range policy.ReadOnly {
			stored.ReadOnly[index] = db.ServiceSandboxExposure{Source: exposure.Source, Destination: exposure.Destination}
		}
	}
	if len(policy.Writable) != 0 {
		stored.Writable = make([]db.ServiceSandboxExposure, len(policy.Writable))
		for index, exposure := range policy.Writable {
			stored.Writable[index] = db.ServiceSandboxExposure{Source: exposure.Source, Destination: exposure.Destination}
		}
	}
	return stored
}

type nativeSandboxUnitRequest struct {
	CurrentPolicy serviceSandboxPolicy
	TargetPolicy  serviceSandboxPolicy
	Identity      db.ServiceIdentity
	Payload       string
	DataDir       string
	Resolver      string
	Hostname      string
}

func renderNativeSandboxUnit(raw string, req nativeSandboxUnitRequest) (string, *serviceSandboxPlan, error) {
	return renderNativeSandboxUnitWithPlan(raw, req, nil)
}

func renderNativeSandboxUnitWithPlan(raw string, req nativeSandboxUnitRequest, validatedPlan *serviceSandboxPlan) (string, *serviceSandboxPlan, error) {
	if err := validateNativeSandboxUnitPolicyStates(req); err != nil {
		return "", nil, err
	}
	lines, finalNewline := splitNativeSandboxUnit(raw)
	execIndex, argv, err := nativeSandboxExecStart(lines)
	if err != nil {
		return "", nil, err
	}
	payloadArgv, err := nativeSandboxPayloadArgv(argv, req)
	if err != nil {
		return "", nil, err
	}
	targetArgv, plan, err := nativeSandboxTargetArgv(payloadArgv, req, validatedPlan)
	if err != nil {
		return "", nil, err
	}
	rendered, err := svc.RenderSystemdExecStart(targetArgv)
	if err != nil {
		return "", nil, fmt.Errorf("render native sandbox ExecStart: %w", err)
	}
	lines[execIndex] = "ExecStart=" + rendered
	lines, err = rewriteNativeSandboxManagedDirectives(lines, req)
	if err != nil {
		return "", nil, err
	}
	unit := strings.Join(lines, "\n")
	if finalNewline {
		unit += "\n"
	}
	return unit, plan, nil
}

func validateNativeSandboxUnitPolicyStates(req nativeSandboxUnitRequest) error {
	if err := validateNativeSandboxUnitPolicyState("current", req.CurrentPolicy.State); err != nil {
		return err
	}
	return validateNativeSandboxUnitPolicyState("target", req.TargetPolicy.State)
}

func validateNativeSandboxUnitPolicyState(label, state string) error {
	switch state {
	case "on", "off", "legacy":
		return nil
	default:
		return fmt.Errorf("native sandbox %s policy state %q is invalid", label, state)
	}
}

func splitNativeSandboxUnit(raw string) ([]string, bool) {
	finalNewline := strings.HasSuffix(raw, "\n")
	if finalNewline {
		raw = strings.TrimSuffix(raw, "\n")
	}
	return strings.Split(raw, "\n"), finalNewline
}

func nativeSandboxExecStart(lines []string) (int, []string, error) {
	inService := false
	index := -1
	value := ""
	for lineIndex, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inService = trimmed == "[Service]"
			continue
		}
		if !inService || !strings.HasPrefix(trimmed, "ExecStart=") {
			continue
		}
		if index >= 0 {
			return -1, nil, fmt.Errorf("native systemd unit must contain exactly one Service ExecStart")
		}
		index = lineIndex
		value = strings.TrimPrefix(trimmed, "ExecStart=")
	}
	if index < 0 {
		return -1, nil, fmt.Errorf("native systemd unit must contain exactly one Service ExecStart")
	}
	argv, err := svc.ParseSystemdExecStart(value)
	if err != nil {
		return -1, nil, fmt.Errorf("parse native systemd ExecStart: %w", err)
	}
	return index, argv, nil
}

func nativeSandboxPayloadArgv(argv []string, req nativeSandboxUnitRequest) ([]string, error) {
	if req.CurrentPolicy.State == "on" {
		var err error
		argv, err = nativeSandboxPayloadAfterSeparator(argv)
		if err != nil {
			return nil, err
		}
	}
	if len(argv) == 0 || argv[0] != req.Payload {
		return nil, fmt.Errorf("native ExecStart active binary is %q, want %q", firstNativeSandboxArg(argv), req.Payload)
	}
	return append([]string(nil), argv...), nil
}

func nativeSandboxPayloadAfterSeparator(argv []string) ([]string, error) {
	if len(argv) == 0 || argv[0] != bubblewrapPath {
		return nil, fmt.Errorf("active sandbox ExecStart must use fixed %s", bubblewrapPath)
	}
	separator := -1
	for index, arg := range argv {
		if arg != "--" {
			continue
		}
		if separator >= 0 {
			return nil, fmt.Errorf("active sandbox ExecStart must contain exactly one policy separator")
		}
		separator = index
	}
	if separator < 0 || separator+1 >= len(argv) {
		return nil, fmt.Errorf("active sandbox ExecStart is missing its policy separator or payload")
	}
	return argv[separator+1:], nil
}

func firstNativeSandboxArg(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

func nativeSandboxTargetArgv(payloadArgv []string, req nativeSandboxUnitRequest, validatedPlan *serviceSandboxPlan) ([]string, *serviceSandboxPlan, error) {
	if req.TargetPolicy.State != "on" {
		return payloadArgv, nil, nil
	}
	resolver := req.Resolver
	if resolver == "" {
		resolver = "/etc/resolv.conf"
	}
	planRequest := serviceSandboxPlanRequest{
		Service: req.Hostname, Policy: req.TargetPolicy, Payload: req.Payload,
		DataDir: req.DataDir, ResolverSource: resolver, UID: req.Identity.UID,
		GID: req.Identity.GID, Hostname: req.Hostname,
	}
	plan := serviceSandboxPlan{}
	if validatedPlan != nil {
		plan = *validatedPlan
	} else {
		var err error
		plan, err = buildServiceSandboxPlan(planRequest)
		if err != nil {
			return nil, nil, err
		}
	}
	argv := append([]string{bubblewrapPath}, plan.Arguments...)
	argv = append(argv, payloadArgv...)
	return argv, &plan, nil
}

func rewriteNativeSandboxManagedDirectives(lines []string, req nativeSandboxUnitRequest) ([]string, error) {
	resolver := req.Resolver
	if resolver == "" {
		resolver = "/etc/resolv.conf"
	}
	managed, err := inspectNativeSandboxManagedDirectives(lines, resolver)
	if err != nil {
		return nil, err
	}
	rewriter := nativeSandboxDirectiveRewriter{
		req: req, targetOn: req.TargetPolicy.State == "on", resolver: resolver,
		managed: managed, result: make([]string, 0, len(lines)+2),
	}
	return rewriter.rewrite(lines), nil
}

type nativeSandboxManagedDirectives struct {
	workingDirectory int
	identity         int
	resolverBind     int
	privateMounts    int
	privateOwned     bool
}

func inspectNativeSandboxManagedDirectives(lines []string, resolver string) (nativeSandboxManagedDirectives, error) {
	inspector := nativeSandboxDirectiveInspector{
		managed:  nativeSandboxManagedDirectives{workingDirectory: -1, identity: -1, resolverBind: -1, privateMounts: -1},
		resolver: resolver,
	}
	for index, line := range lines {
		if err := inspector.inspectLine(index, line); err != nil {
			return inspector.managed, err
		}
	}
	if inspector.serviceSections != 1 {
		return inspector.managed, fmt.Errorf("native systemd unit must contain exactly one Service section")
	}
	inspector.managed.privateOwned = inspector.managed.resolverBind >= 0 && inspector.managed.privateMounts >= 0 && !inspector.otherMount
	return inspector.managed, nil
}

type nativeSandboxDirectiveInspector struct {
	managed         nativeSandboxManagedDirectives
	resolver        string
	inService       bool
	serviceSections int
	otherMount      bool
}

func (i *nativeSandboxDirectiveInspector) inspectLine(index int, line string) error {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		i.inspectSection(trimmed)
		return nil
	}
	if !i.inService {
		return nil
	}
	if strings.HasSuffix(trimmed, "\\") {
		return errors.New("multiline Service directive continuation is ambiguous")
	}
	key, value, present := strings.Cut(trimmed, "=")
	if !present {
		return nil
	}
	return i.inspectDirective(index, trimmed, key, value)
}

func (i *nativeSandboxDirectiveInspector) inspectSection(section string) {
	i.inService = section == "[Service]"
	if i.inService {
		i.serviceSections++
	}
}

func (i *nativeSandboxDirectiveInspector) inspectDirective(index int, line, key, value string) error {
	switch key {
	case "WorkingDirectory":
		return claimNativeSandboxDirective(&i.managed.workingDirectory, index, key)
	case "Environment":
		return inspectNativeSandboxIdentityDirective(&i.managed, index, line)
	case "BindReadOnlyPaths":
		var err error
		i.otherMount, err = inspectNativeSandboxResolverDirective(&i.managed, index, value, i.resolver, i.otherMount)
		return err
	case "PrivateMounts":
		return i.inspectPrivateMounts(index, line, value)
	default:
		i.otherMount = i.otherMount || nativeSandboxOtherMountDirective(key)
		return nil
	}
}

func (i *nativeSandboxDirectiveInspector) inspectPrivateMounts(index int, line, value string) error {
	if value != "yes" {
		return fmt.Errorf("ambiguous PrivateMounts directive %q", line)
	}
	return claimNativeSandboxDirective(&i.managed.privateMounts, index, "PrivateMounts")
}

func claimNativeSandboxDirective(current *int, index int, name string) error {
	if *current >= 0 {
		return fmt.Errorf("duplicate managed %s directive", name)
	}
	*current = index
	return nil
}

func inspectNativeSandboxIdentityDirective(managed *nativeSandboxManagedDirectives, index int, line string) error {
	if !nativeSandboxIdentityCandidate(line) {
		return nil
	}
	if !nativeSandboxIdentityEnvironment(line) {
		return fmt.Errorf("ambiguous managed Environment identity directive")
	}
	return claimNativeSandboxDirective(&managed.identity, index, "Environment identity")
}

func inspectNativeSandboxResolverDirective(managed *nativeSandboxManagedDirectives, index int, value, resolver string, otherMount bool) (bool, error) {
	if strings.TrimSpace(value) == "" {
		return otherMount, errors.New("ambiguous empty BindReadOnlyPaths reset")
	}
	fields := strings.Fields(value)
	hasResolverDestination := false
	for _, field := range fields {
		if strings.HasSuffix(field, ":/etc/resolv.conf") {
			hasResolverDestination = true
		}
	}
	if !hasResolverDestination {
		return true, nil
	}
	want := resolver + ":/etc/resolv.conf"
	if len(fields) != 1 || fields[0] != want {
		return otherMount, fmt.Errorf("ambiguous BindReadOnlyPaths resolver ownership")
	}
	if err := claimNativeSandboxDirective(&managed.resolverBind, index, "BindReadOnlyPaths resolver"); err != nil {
		return otherMount, err
	}
	return otherMount, nil
}

func nativeSandboxIdentityCandidate(line string) bool {
	for _, key := range []string{"HOME=", "USER=", "LOGNAME=", "SHELL="} {
		if !strings.Contains(line, key) {
			return false
		}
	}
	return true
}

func nativeSandboxOtherMountDirective(key string) bool {
	switch key {
	case "BindPaths", "TemporaryFileSystem", "ReadWritePaths", "ReadOnlyPaths", "InaccessiblePaths", "ExecPaths", "NoExecPaths",
		"MountImages", "ExtensionImages", "ExtensionDirectories", "BindLogSockets", "ProtectSystem", "ProtectHome", "ProtectProc",
		"ProcSubset", "PrivateTmp", "PrivateDevices", "PrivateUsers", "RootDirectory", "RootImage":
		return true
	default:
		return false
	}
}

type nativeSandboxDirectiveRewriter struct {
	req             nativeSandboxUnitRequest
	targetOn        bool
	resolver        string
	managed         nativeSandboxManagedDirectives
	inService       bool
	resolverWritten bool
	privateWritten  bool
	result          []string
}

func (r *nativeSandboxDirectiveRewriter) rewrite(lines []string) []string {
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			r.flushDirectMounts()
			r.inService = trimmed == "[Service]"
			r.result = append(r.result, line)
			continue
		}
		if !r.inService {
			r.result = append(r.result, line)
			continue
		}
		r.rewriteServiceLine(index, line)
	}
	r.flushDirectMounts()
	return r.result
}

func (r *nativeSandboxDirectiveRewriter) rewriteServiceLine(index int, line string) {
	switch index {
	case r.managed.workingDirectory:
		working := r.req.DataDir
		if r.targetOn {
			working = "/"
		}
		r.result = append(r.result, "WorkingDirectory="+working)
	case r.managed.identity:
		user := r.req.Identity.RequestedUser
		r.result = append(r.result, "Environment=HOME="+r.req.DataDir+" USER="+user+" LOGNAME="+user+" SHELL=/bin/sh")
	case r.managed.resolverBind:
		r.writeDirectResolverBind()
	case r.managed.privateMounts:
		if !r.targetOn || !r.managed.privateOwned {
			r.result = append(r.result, line)
			r.privateWritten = true
		}
	default:
		r.result = append(r.result, line)
	}
}

func (r *nativeSandboxDirectiveRewriter) writeDirectResolverBind() {
	if r.targetOn || r.resolverWritten {
		return
	}
	r.result = append(r.result, "BindReadOnlyPaths="+r.resolver+":/etc/resolv.conf")
	r.resolverWritten = true
}

func (r *nativeSandboxDirectiveRewriter) flushDirectMounts() {
	if !r.inService || r.targetOn {
		return
	}
	if !r.resolverWritten {
		r.writeDirectResolverBind()
	}
	if !r.privateWritten {
		r.result = append(r.result, "PrivateMounts=yes")
		r.privateWritten = true
	}
}

func nativeSandboxIdentityEnvironment(line string) bool {
	if !strings.HasPrefix(line, "Environment=") {
		return false
	}
	assignments := strings.Fields(strings.TrimPrefix(line, "Environment="))
	if len(assignments) != 4 {
		return false
	}
	return strings.HasPrefix(assignments[0], "HOME=") && strings.HasPrefix(assignments[1], "USER=") &&
		strings.HasPrefix(assignments[2], "LOGNAME=") && assignments[3] == "SHELL=/bin/sh"
}
