// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/ipn"
	"tailscale.com/types/opt"
)

type dockerNetNSReconciler interface {
	ReconcileNetNS(ctx context.Context) (bool, error)
}

var (
	tailscaleSidecarNetNSStale     = tailscaleSidecarNetNSStaleOnHost
	tailscaleSidecarMainPID        = systemdMainPID
	statNetNSPath                  = os.Stat
	restartTailscaleSystemdSidecar = func(ctx context.Context, service *svc.SystemdService) error {
		return service.RestartTailscaleSidecar(ctx)
	}
	startTailscaleSystemdSidecar = func(ctx context.Context, service *svc.SystemdService) error {
		return service.StartTailscaleSidecar(ctx)
	}
	verifyTailscaleSystemdSidecar = func(ctx context.Context, service *svc.SystemdService) error {
		return service.VerifyTailscaleSidecar(ctx)
	}
	writeTailscaleResolverUnitFile = writeTextFileAtomically
)

func (s *Server) reconcileNetNSBackedDockerServices(ctx context.Context) error {
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		restarted, err := s.reconcileNetNSBackedDockerService(ctx, name, sv)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			log.Printf("netns reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if restarted {
			log.Printf("reconciled stale docker netns for service %q; restarted containers", name)
		}
	}

	return errors.Join(errs...)
}

func (s *Server) reconcileTailscaleDNSConfigs(ctx context.Context) error {
	return s.reconcileTailscaleDNSConfigsWithRestart(ctx, true)
}

func (s *Server) reconcileTailscaleDNSConfigsWithRestart(
	ctx context.Context,
	restart bool,
) error {
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		service := sv.AsStruct()
		restarted, err := reconcileTailscaleDNSConfig(service, s.serviceRootFromView(sv))
		if err != nil {
			log.Printf("tailscale DNS config reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if restarted {
			if !restart {
				continue
			}
			if err := s.restartTailscaleSidecarForService(ctx, name); err != nil {
				errs = append(errs, fmt.Errorf("restart tailscale sidecar for %q after DNS config reconciliation: %w", name, err))
				continue
			}
			log.Printf("reconciled tailscale DNS config for service %q; restarted tailscale sidecar", name)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) reconcileTailscaleResolverStartup(ctx context.Context) error {
	if err := s.reconcileTailscaleDNSConfigsWithRestart(ctx, false); err != nil {
		return fmt.Errorf("repair tailscale DNS configs before resolver isolation: %w", err)
	}
	return s.reconcileTailscaleResolverIsolation(ctx)
}

func reconcileTailscaleDNSConfig(service *db.Service, serviceRoot string) (bool, error) {
	if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); !ok {
		return false, nil
	}

	configPaths := tailscaleDNSConfigPaths(service, serviceRoot)
	if len(configPaths) == 0 {
		return false, nil
	}

	var changed bool
	var errs []error
	for _, configPath := range configPaths {
		fileChanged, err := reconcileTailscaleDNSConfigFile(configPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		changed = changed || fileChanged
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	return true, nil
}

func tailscaleDNSConfigPaths(service *db.Service, serviceRoot string) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}

	if configPath, ok := service.Artifacts.Gen(db.ArtifactTSConfig, service.Generation); ok {
		add(configPath)
	}
	if serviceRoot = strings.TrimSpace(serviceRoot); serviceRoot != "" {
		add(filepath.Join(serviceEnvDirForRoot(serviceRoot), "tailscaled.json"))
		// Keep reconciling the legacy runtime copy during upgrades until the
		// service has installed the root-owned env/ replacement.
		add(filepath.Join(serviceRunDirForRoot(serviceRoot), "tailscaled.json"))
	}
	return paths
}

func reconcileTailscaleDNSConfigFile(configPath string) (bool, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read tailscale config %s: %w", configPath, err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false, fmt.Errorf("parse tailscale config %s: %w", configPath, err)
	}
	if cfg.AcceptDNS.EqualBool(false) {
		return false, nil
	}

	cfg.AcceptDNS = opt.NewBool(false)
	next, err := json.Marshal(cfg)
	if err != nil {
		return false, fmt.Errorf("marshal tailscale config %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, next, 0o644); err != nil {
		return false, fmt.Errorf("write tailscale config %s: %w", configPath, err)
	}
	return true, nil
}

func (s *Server) reconcileTailscaleResolverIsolation(ctx context.Context) error {
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	plan, err := s.planTailscaleResolverIsolationFleet(ctx, dv)
	if err != nil {
		return err
	}
	return s.applyTailscaleResolverIsolationFleet(ctx, plan)
}

func tailscaleSidecarInstalledUnitPath(serviceName string) string {
	return filepath.Join(systemdSystemDir, "yeet-"+serviceName+"-ts.service")
}

type tailscaleResolverUnit struct {
	networkNamespace  string
	resolverSource    string
	guardRunner       string
	daemon            string
	environmentFile   string
	workingDirectory  string
	args              []string
	conditions        []string
	conditionsOutside []string
	bindPaths         []string
	bindReadOnlyPaths []string
	privateMounts     []string
	mountsOutside     []string
}

var errNoTailscaleResolverNetworkNamespace = errors.New("tailscale unit has no network namespace")

func parseTailscaleResolverUnit(unit string) (tailscaleResolverUnit, error) {
	var parser tailscaleResolverUnitParser
	for _, line := range strings.Split(unit, "\n") {
		if err := parser.consumeLine(line); err != nil {
			return parser.result, err
		}
	}
	return parser.finish()
}

type tailscaleResolverUnitParser struct {
	result                tailscaleResolverUnit
	section               string
	execStarts            []string
	networkNamespaceCount int
	environmentFileCount  int
	workingDirectoryCount int
}

func (p *tailscaleResolverUnitParser) consumeLine(line string) error {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasSuffix(trimmed, "\\"):
		return errors.New("systemd continuation lines are not supported")
	case ignoredSystemdUnitLine(trimmed):
		return nil
	case isSystemdUnitSection(trimmed):
		p.section = trimmed
		return nil
	case strings.Contains(trimmed, "\"") || strings.Contains(trimmed, "'"):
		return fmt.Errorf("quoted systemd directive %q is not supported", trimmed)
	}
	if handled := p.consumePlacementSensitiveDirective(trimmed); handled {
		return nil
	}
	if strings.HasPrefix(trimmed, "ExecStart=") {
		return p.consumeExecStart(trimmed)
	}
	if p.section == "[Service]" {
		return p.consumeServiceDirective(trimmed)
	}
	return nil
}

func (p *tailscaleResolverUnitParser) consumePlacementSensitiveDirective(line string) bool {
	if strings.HasPrefix(line, "ConditionFileIsExecutable=") {
		if p.section == "[Unit]" {
			p.result.conditions = append(p.result.conditions, line)
		} else {
			p.result.conditionsOutside = append(p.result.conditionsOutside, line)
		}
		return true
	}
	if isTailscaleResolverMountDirective(line) && p.section != "[Service]" {
		p.result.mountsOutside = append(p.result.mountsOutside, line)
		return true
	}
	return false
}

func ignoredSystemdUnitLine(line string) bool {
	return line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";")
}

func isSystemdUnitSection(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}

func (p *tailscaleResolverUnitParser) consumeExecStart(line string) error {
	if p.section != "[Service]" {
		return errors.New("ExecStart outside [Service]")
	}
	p.execStarts = append(p.execStarts, strings.TrimSpace(strings.TrimPrefix(line, "ExecStart=")))
	return nil
}

func (p *tailscaleResolverUnitParser) consumeServiceDirective(line string) error {
	switch {
	case strings.HasPrefix(line, "NetworkNamespacePath="):
		p.networkNamespaceCount++
		return setUniqueTailscaleResolverDirective(
			&p.result.networkNamespace,
			strings.TrimSpace(strings.TrimPrefix(line, "NetworkNamespacePath=")),
			"NetworkNamespacePath",
			p.networkNamespaceCount,
		)
	case strings.HasPrefix(line, "EnvironmentFile="):
		p.environmentFileCount++
		return setUniqueTailscaleResolverDirective(
			&p.result.environmentFile,
			strings.TrimSpace(strings.TrimPrefix(line, "EnvironmentFile=")),
			"EnvironmentFile",
			p.environmentFileCount,
		)
	case strings.HasPrefix(line, "WorkingDirectory="):
		p.workingDirectoryCount++
		return setUniqueTailscaleResolverDirective(
			&p.result.workingDirectory,
			strings.TrimSpace(strings.TrimPrefix(line, "WorkingDirectory=")),
			"WorkingDirectory",
			p.workingDirectoryCount,
		)
	case strings.HasPrefix(line, "BindPaths="):
		p.result.bindPaths = append(p.result.bindPaths, line)
		return nil
	case strings.HasPrefix(line, "BindReadOnlyPaths="):
		p.result.bindReadOnlyPaths = append(p.result.bindReadOnlyPaths, line)
		return nil
	case strings.HasPrefix(line, "PrivateMounts="):
		p.result.privateMounts = append(p.result.privateMounts, line)
		return nil
	case strings.HasPrefix(line, "ProtectSystem="):
		return fmt.Errorf("ProtectSystem conflicts with managed Tailscale resolver mount isolation")
	default:
		return nil
	}
}

func setUniqueTailscaleResolverDirective(dst *string, value, name string, count int) error {
	if count != 1 {
		return fmt.Errorf("multiple %s directives", name)
	}
	*dst = value
	return nil
}

func (p *tailscaleResolverUnitParser) finish() (tailscaleResolverUnit, error) {
	if len(p.execStarts) != 1 {
		return p.result, fmt.Errorf("require exactly one [Service] ExecStart, got %d", len(p.execStarts))
	}
	if p.networkNamespaceCount != 1 || p.result.networkNamespace == "" {
		return p.result, errNoTailscaleResolverNetworkNamespace
	}
	if !cleanAbsolutePath(p.result.networkNamespace) {
		return p.result, fmt.Errorf("invalid NetworkNamespacePath %q", p.result.networkNamespace)
	}
	resolverSource, guardRunner, daemon, args, err := parseTailscaleResolverExecStart(p.execStarts[0])
	if err != nil {
		return p.result, err
	}
	if p.environmentFileCount != 1 || p.result.environmentFile == "" {
		return p.result, errors.New("tailscale unit must have exactly one EnvironmentFile")
	}
	if p.workingDirectoryCount != 1 || p.result.workingDirectory == "" {
		return p.result, errors.New("tailscale unit must have exactly one WorkingDirectory")
	}
	p.result.resolverSource = resolverSource
	p.result.guardRunner = guardRunner
	p.result.daemon = daemon
	p.result.args = args
	return p.result, nil
}

func parseTailscaleResolverExecStart(
	execStart string,
) (resolverSource, guardRunner, daemon string, daemonArgs []string, err error) {
	args := strings.Fields(execStart)
	if len(args) == 0 || !cleanAbsolutePath(args[0]) {
		return "", "", "", nil, fmt.Errorf("ExecStart executable must be an absolute clean path: %q", execStart)
	}
	if len(args) >= 6 && args[1] == "tailscale-resolver-exec" {
		if !validGuardedTailscaleResolverExecStart(args) {
			return "", "", "", nil, fmt.Errorf("invalid guarded Tailscale ExecStart %q", execStart)
		}
		return args[3], args[0], args[5], append([]string(nil), args[6:]...), nil
	}
	return "", "", args[0], append([]string(nil), args[1:]...), nil
}

func cleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validGuardedTailscaleResolverExecStart(args []string) bool {
	return args[2] == "--source" &&
		args[4] == "--" &&
		cleanAbsolutePath(args[3]) &&
		cleanAbsolutePath(args[5])
}

func (u tailscaleResolverUnit) validateForService(service db.Service) error {
	if err := u.validateNetworkNamespaceForService(service); err != nil {
		return err
	}
	if err := svc.ValidateManagedTailscaledDaemonPath(service.ServiceRoot, u.daemon); err != nil {
		return err
	}
	if service.TSNet == nil || service.TSNet.Interface == "" {
		return errors.New("tailscale interface is required to validate generated tailscaled arguments")
	}
	return validateTailscaleResolverUnitArgs(u.args, service)
}

func (u tailscaleResolverUnit) validateNetworkNamespaceForService(service db.Service) error {
	expectedNamespace := filepath.Join("/var/run/netns", "yeet-"+service.Name+"-ns")
	if u.networkNamespace != expectedNamespace {
		return fmt.Errorf("NetworkNamespacePath = %q, want %q", u.networkNamespace, expectedNamespace)
	}
	expectedSource := filepath.Join("/etc/netns", "yeet-"+service.Name+"-ns", "resolv.conf")
	if u.resolverSource != "" && u.resolverSource != expectedSource {
		return fmt.Errorf("resolver source = %q, want %q", u.resolverSource, expectedSource)
	}
	return nil
}

func validateTailscaleResolverUnitArgs(args []string, service db.Service) error {
	expectedArgs := tailscaleSystemdArgs(
		serviceRunDirForRoot(service.ServiceRoot),
		serviceEnvDirForRoot(service.ServiceRoot),
		service.TSNet.Interface,
		false,
	)
	if len(args) != len(expectedArgs) {
		return fmt.Errorf("tailscaled arguments = %q, want %q", args, expectedArgs)
	}
	for i := range expectedArgs {
		if args[i] != expectedArgs[i] {
			return fmt.Errorf("tailscaled arguments = %q, want %q", args, expectedArgs)
		}
	}
	return nil
}

func ensureTailscaleUnitResolverIsolation(unit string, catchBin string) (next string, changed bool, err error) {
	if err := validateTailscaleResolverCatchPath(catchBin); err != nil {
		return unit, false, err
	}
	parsed, err := parseTailscaleResolverUnit(unit)
	if err != nil {
		return unit, false, err
	}
	directives := newTailscaleResolverGuardDirectives(parsed, catchBin)
	if err := directives.validateConditions(parsed); err != nil {
		return unit, false, err
	}
	if err := directives.validateMounts(parsed); err != nil {
		return unit, false, err
	}
	if directives.alreadyApplied(unit, parsed) {
		return unit, false, nil
	}
	next, err = rewriteTailscaleResolverUnit(unit, directives)
	if err != nil {
		return unit, false, err
	}
	return next, next != unit, nil
}

type tailscaleResolverGuardDirectives struct {
	condition    string
	execStart    string
	writableBind string
	readOnlyBind string
}

func newTailscaleResolverGuardDirectives(
	parsed tailscaleResolverUnit,
	catchBin string,
) tailscaleResolverGuardDirectives {
	source := filepath.Join("/etc/netns", filepath.Base(parsed.networkNamespace), "resolv.conf")
	return tailscaleResolverGuardDirectives{
		condition:    "ConditionFileIsExecutable=" + parsed.daemon,
		execStart:    "ExecStart=" + strings.Join(append([]string{catchBin, "tailscale-resolver-exec", "--source", source, "--", parsed.daemon}, parsed.args...), " "),
		writableBind: "BindPaths=" + source + ":/etc/resolv.conf",
		readOnlyBind: "BindReadOnlyPaths=" + source + ":/etc/resolv.conf",
	}
}

func (d tailscaleResolverGuardDirectives) validateConditions(
	parsed tailscaleResolverUnit,
) error {
	for _, condition := range append(
		append([]string(nil), parsed.conditions...),
		parsed.conditionsOutside...,
	) {
		if condition != d.condition {
			return fmt.Errorf(
				"conflicting ConditionFileIsExecutable directive %q",
				condition,
			)
		}
	}
	return nil
}

func (d tailscaleResolverGuardDirectives) validateMounts(
	parsed tailscaleResolverUnit,
) error {
	if len(parsed.mountsOutside) != 0 {
		return errors.New("resolver mount directives outside [Service] are not supported")
	}
	for _, bind := range parsed.bindPaths {
		if bind != d.writableBind {
			return errors.New("conflicting BindPaths directive")
		}
	}
	for _, bind := range parsed.bindReadOnlyPaths {
		if bind != d.readOnlyBind {
			return errors.New("conflicting BindReadOnlyPaths directive")
		}
	}
	for _, privateMounts := range parsed.privateMounts {
		if privateMounts != "PrivateMounts=yes" {
			return errors.New("conflicting PrivateMounts directive")
		}
	}
	return nil
}

func (d tailscaleResolverGuardDirectives) alreadyApplied(
	unit string,
	parsed tailscaleResolverUnit,
) bool {
	return len(parsed.conditions) == 1 &&
		parsed.conditions[0] == d.condition &&
		len(parsed.conditionsOutside) == 0 &&
		systemdUnitHasDirective(unit, d.execStart) &&
		len(parsed.bindPaths) == 0 &&
		len(parsed.bindReadOnlyPaths) == 0 &&
		len(parsed.privateMounts) == 1 &&
		parsed.privateMounts[0] == "PrivateMounts=yes"
}

type tailscaleResolverUnitRewriter struct {
	directives        tailscaleResolverGuardDirectives
	out               []string
	section           string
	insertedCondition bool
	insertedResolver  bool
}

func rewriteTailscaleResolverUnit(
	unit string,
	directives tailscaleResolverGuardDirectives,
) (string, error) {
	rewriter := tailscaleResolverUnitRewriter{directives: directives}
	for _, line := range strings.Split(unit, "\n") {
		rewriter.appendLine(line)
	}
	if !rewriter.insertedCondition || !rewriter.insertedResolver {
		return unit, errors.New("missing [Service] boundary for resolver guard directives")
	}
	return strings.Join(rewriter.out, "\n"), nil
}

func (r *tailscaleResolverUnitRewriter) appendLine(line string) {
	trimmed := strings.TrimSpace(line)
	r.insertConditionBeforeService(trimmed)
	r.updateSection(trimmed)
	if strings.HasPrefix(trimmed, "ConditionFileIsExecutable=") {
		return
	}
	if r.rewriteServiceDirective(trimmed) {
		return
	}
	r.out = append(r.out, line)
	r.insertResolverAfterNamespace(trimmed)
}

func (r *tailscaleResolverUnitRewriter) insertConditionBeforeService(line string) {
	if line != "[Service]" || r.insertedCondition {
		return
	}
	if r.section != "[Unit]" {
		r.out = append(r.out, "[Unit]")
	}
	r.out = append(r.out, r.directives.condition)
	r.insertedCondition = true
}

func (r *tailscaleResolverUnitRewriter) updateSection(line string) {
	if isSystemdUnitSection(line) {
		r.section = line
	}
}

func (r *tailscaleResolverUnitRewriter) rewriteServiceDirective(line string) bool {
	if r.section != "[Service]" {
		return false
	}
	if strings.HasPrefix(line, "ExecStart=") {
		r.out = append(r.out, r.directives.execStart)
		return true
	}
	return line == r.directives.writableBind ||
		line == r.directives.readOnlyBind ||
		line == "PrivateMounts=yes"
}

func isTailscaleResolverMountDirective(line string) bool {
	return strings.HasPrefix(line, "BindPaths=") ||
		strings.HasPrefix(line, "BindReadOnlyPaths=") ||
		strings.HasPrefix(line, "PrivateMounts=")
}

func (r *tailscaleResolverUnitRewriter) insertResolverAfterNamespace(line string) {
	if r.section == "[Service]" &&
		strings.HasPrefix(line, "NetworkNamespacePath=") &&
		!r.insertedResolver {
		r.out = append(r.out, "PrivateMounts=yes")
		r.insertedResolver = true
	}
}

func (s *Server) reconcileTailscaleResolverMounts(ctx context.Context) error {
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		service := sv.AsStruct()
		if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); !ok {
			continue
		}

		if !catchSystemdUnitActive("yeet-" + name + "-ts.service") {
			continue
		}
		if err := s.verifyTailscaleSidecarForService(ctx, name); err == nil {
			continue
		}
		verifyErr := err
		if err := s.restartTailscaleSidecarForService(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("verify tailscale sidecar for %q: %w", name, errors.Join(verifyErr, err)))
			continue
		}
		log.Printf("restarted tailscale sidecar for service %q after failed verification", name)
	}
	return errors.Join(errs...)
}

func systemdUnitHasDirective(unit, directive string) bool {
	for _, line := range strings.Split(unit, "\n") {
		if strings.TrimSpace(line) == directive {
			return true
		}
	}
	return false
}

func (s *Server) reconcileNetNSBackedDockerService(ctx context.Context, name string, sv db.ServiceView) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	serviceRecord := sv.AsStruct()
	if serviceRecord.ServiceType != db.ServiceTypeDockerCompose {
		return false, nil
	}
	if _, ok := serviceRecord.Artifacts.Gen(db.ArtifactNetNSService, serviceRecord.Generation); !ok {
		return false, nil
	}

	service, err := s.newDockerComposeService(sv)
	if err != nil {
		return false, fmt.Errorf("load docker compose service %q: %w", name, err)
	}
	restarted, err := service.ReconcileNetNS(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false, err
		}
		return false, fmt.Errorf("reconcile docker compose service %q: %w", name, err)
	}
	if !restarted {
		if err := s.reconcileTailscaleSidecarAfterNetNSCheck(ctx, serviceRecord, false); err != nil {
			return false, fmt.Errorf("repair tailscale sidecar for docker compose service %q: %w", name, err)
		}
		return false, nil
	}
	if err := s.reconcileTailscaleSidecarAfterNetNSCheck(ctx, serviceRecord, true); err != nil {
		return false, fmt.Errorf("restart tailscale sidecar for docker compose service %q: %w", name, err)
	}
	return true, nil
}

func (s *Server) reconcileTailscaleSidecarAfterNetNSCheck(ctx context.Context, service *db.Service, netNSRecreated bool) error {
	if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); !ok {
		return nil
	}
	if !netNSRecreated {
		stale, err := tailscaleSidecarNetNSStale(service.Name)
		if err != nil {
			return err
		}
		if !stale {
			return nil
		}
	}
	return s.restartTailscaleSidecarForService(ctx, service.Name)
}

func (s *Server) restartTailscaleSidecarForService(ctx context.Context, name string) error {
	release := s.serviceOperationLocks.Lock(name)
	defer release()
	return s.withTailscaleResolverReadyForActivation(ctx, name, func() error {
		return s.restartTailscaleSidecarForServiceLocked(ctx, name)
	})
}

func (s *Server) restartTailscaleSidecarForServiceLocked(ctx context.Context, name string) error {
	service, err := s.systemdService(name)
	if err != nil {
		return fmt.Errorf("load systemd service %q: %w", name, err)
	}
	return restartTailscaleSystemdSidecar(ctx, service)
}

func (s *Server) verifyTailscaleSidecarForService(ctx context.Context, name string) error {
	service, err := s.systemdService(name)
	if err != nil {
		return fmt.Errorf("load systemd service %q: %w", name, err)
	}
	return verifyTailscaleSystemdSidecar(ctx, service)
}

func tailscaleSidecarNetNSStaleOnHost(name string) (bool, error) {
	unit := fmt.Sprintf("yeet-%s-ts.service", name)
	pid, err := tailscaleSidecarMainPID(unit)
	if err != nil {
		return false, err
	}
	if pid == 0 {
		return false, nil
	}

	procInfo, err := statNetNSPath(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat tailscale sidecar netns for %s: %w", unit, err)
	}
	namedInfo, err := statNetNSPath(fmt.Sprintf("/var/run/netns/yeet-%s-ns", name))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat named netns for %s: %w", name, err)
	}

	procInode, err := fileInode(procInfo)
	if err != nil {
		return false, fmt.Errorf("read tailscale sidecar netns inode for %s: %w", unit, err)
	}
	namedInode, err := fileInode(namedInfo)
	if err != nil {
		return false, fmt.Errorf("read named netns inode for %s: %w", name, err)
	}
	return procInode != namedInode, nil
}

func systemdMainPID(unit string) (int, error) {
	output, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", unit).Output()
	if err != nil {
		return 0, fmt.Errorf("systemctl show MainPID for %s: %w", unit, err)
	}
	text := strings.TrimSpace(string(output))
	if text == "" || text == "0" {
		return 0, nil
	}
	pid, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("parse MainPID for %s: %w", unit, err)
	}
	return pid, nil
}

func fileInode(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected file info type %T", info.Sys())
	}
	return stat.Ino, nil
}
