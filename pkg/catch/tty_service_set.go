// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type serviceRootMigrationMode int

const (
	serviceRootMigrationPrompt serviceRootMigrationMode = iota
	serviceRootMigrationCopy
	serviceRootMigrationEmpty
)

type serviceRootMigrationRequest struct {
	Root string
	ZFS  bool
}

type serviceRootMigrationPlan struct {
	ServiceName      string
	OldRoot          string
	OldRootZFS       string
	NewRoot          string
	NewRootZFS       string
	CreateNewRootZFS bool
	NewRootExisted   bool
	NewRootSkeleton  bool
	NewRootState     []serviceRootTargetPathState
	Mode             serviceRootMigrationMode
	GuardSource      bool
}

type serviceRootTargetPathState struct {
	Path string
	Mode os.FileMode
	UID  uint32
	GID  uint32
	Dev  uint64
	Ino  uint64
}

var (
	isServiceRunningForRootMigration = func(s *Server, name string) (bool, error) {
		return s.IsServiceRunning(name)
	}
	renameServiceRoot                 = os.Rename
	downDockerComposeForRootMigration = (*Server).downDockerComposeForRootMigration
	installSystemdForRootMigration    = (*Server).installSystemdForRootMigration
	uninstallSystemdForRootMigration  = (*Server).uninstallSystemdForRootMigration
	upDockerComposeForServiceSet      = func(compose *svc.DockerComposeService) error {
		return compose.Start()
	}
)

func (e *ttyExecer) serviceCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("service requires a command")
	}
	switch args[0] {
	case "set":
		flags, rest, err := cli.ParseServiceSet(args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("unexpected service set args: %s", strings.Join(rest, " "))
		}
		return e.serviceSetCmdFunc(flags)
	case "rollback":
		rest, err := cli.ParseServiceRollback(argsWithServiceDefault(args[1:], e.sn))
		if err != nil {
			return err
		}
		return e.rollbackCmdFunc(rest[0])
	case "generations":
		flags, rest, err := cli.ParseServiceGenerations(argsWithServiceDefault(args[1:], e.sn))
		if err != nil {
			return err
		}
		return e.serviceGenerationsCmdFunc(rest[0], flags)
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func argsWithServiceDefault(args []string, serviceName string) []string {
	if strings.TrimSpace(serviceName) == "" || hasServiceCommandArg(args) {
		return args
	}
	out := append([]string{}, args...)
	return append(out, serviceName)
}

func hasServiceCommandArg(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return i+1 < len(args)
		}
		if strings.HasPrefix(arg, "--format=") {
			continue
		}
		if arg == "--format" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		return true
	}
	return false
}

func (e *ttyExecer) serviceSetCmdFunc(flags cli.ServiceSetFlags) error {
	changes := serviceSetChangesFromFlags(flags)
	if !changes.any() {
		return fmt.Errorf("service set requires --run-as, --service-root, snapshot settings, or published ports")
	}
	if err := validateServiceSetMutationCombination(flags, changes); err != nil {
		return err
	}
	if err := validateServiceSetSnapshotChange(flags, changes); err != nil {
		return err
	}
	if changes.identity {
		if err := e.validateServiceSetIdentityType(); err != nil {
			return err
		}
		if err := e.applyServiceSetIdentityChange(flags, changes); err != nil {
			return err
		}
		changes.identity = false
		changes.root = false
	}
	if err := e.applyServiceSetRootChange(flags, changes); err != nil {
		return err
	}
	if err := e.applyServiceSetPublishChange(flags, changes); err != nil {
		return err
	}
	if changes.snapshot {
		return e.s.updateServiceSnapshotPolicy(e.sn, flags)
	}
	return nil
}

func validateServiceSetMutationCombination(flags cli.ServiceSetFlags, changes serviceSetChanges) error {
	if changes.identity && changes.publish {
		return fmt.Errorf("--run-as cannot be combined with published-port changes; apply identity and publish changes as separate commands")
	}
	if changes.identity && changes.root && flags.Empty {
		return fmt.Errorf("--run-as cannot be combined with --empty because an empty root has no native generation to rewrite; move the root with --empty, then redeploy the service with --run-as as separate commands")
	}
	return nil
}

type serviceSetChanges struct {
	identity bool
	root     bool
	publish  bool
	snapshot bool
}

func serviceSetChangesFromFlags(flags cli.ServiceSetFlags) serviceSetChanges {
	return serviceSetChanges{
		identity: flags.RunAsSet,
		root:     strings.TrimSpace(flags.ServiceRoot) != "" || flags.ZFS,
		publish:  len(flags.Publish) != 0 || flags.PublishReset,
		snapshot: flags.SnapshotChange,
	}
}

func (c serviceSetChanges) any() bool {
	return c.identity || c.root || c.publish || c.snapshot
}

func (e *ttyExecer) validateServiceSetIdentityType() error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	switch sv.ServiceType() {
	case db.ServiceTypeVM:
		return fmt.Errorf("--run-as does not control VM guest or Firecracker jailer identities; use VM guest settings because Firecracker host execution is managed separately")
	case db.ServiceTypeDockerCompose:
		return fmt.Errorf("--run-as applies only to native systemd workloads; configure the container image or Compose service \"user:\" field instead")
	case db.ServiceTypeSystemd:
		return nil
	default:
		return fmt.Errorf("service %q has unsupported type %q for --run-as", e.sn, sv.ServiceType())
	}
}

func (e *ttyExecer) applyServiceSetIdentityChange(flags cli.ServiceSetFlags, changes serviceSetChanges) error {
	target, err := resolveServiceIdentity(flags.RunAs)
	if err != nil {
		return err
	}
	rootPlan, err := e.prepareServiceSetIdentityRootPlan(flags, changes)
	if err != nil {
		return err
	}
	req := serviceIdentityMigrationRequest{
		Service: e.sn, Requested: flags.RunAs, Target: target, RootPlan: rootPlan,
	}
	if e.migrateServiceIdentityFunc != nil {
		_, err = e.migrateServiceIdentityFunc(context.Background(), req, e.rw)
		return err
	}
	if e.serviceOperationLockHeld {
		_, err = e.s.migrateServiceIdentityLocked(context.Background(), req, e.rw)
	} else {
		_, err = e.s.migrateServiceIdentity(context.Background(), req, e.rw)
	}
	return err
}

func (e *ttyExecer) prepareServiceSetIdentityRootPlan(flags cli.ServiceSetFlags, changes serviceSetChanges) (*serviceRootMigrationPlan, error) {
	if !changes.root {
		return nil, nil
	}
	mode := serviceRootMigrationPrompt
	if flags.Copy {
		mode = serviceRootMigrationCopy
	}
	if flags.Empty {
		mode = serviceRootMigrationEmpty
	}
	if mode == serviceRootMigrationPrompt && !e.isPty {
		return nil, serviceRootMigrationModeRequiredError()
	}
	request := serviceRootMigrationRequest{Root: flags.ServiceRoot, ZFS: flags.ZFS}
	plan, err := e.s.planServiceRootMigrationForIdentity(e.sn, request)
	if err != nil {
		return nil, err
	}
	mode, err = e.confirmServiceRootMigrationMode(mode, plan)
	if err != nil {
		return nil, err
	}
	plan.Mode = mode
	return &plan, nil
}

func (s *Server) planServiceRootMigrationForIdentity(name string, request serviceRootMigrationRequest) (serviceRootMigrationPlan, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return serviceRootMigrationPlan{}, fmt.Errorf("service %q not found", name)
		}
		return serviceRootMigrationPlan{}, err
	}
	if s.zfsRunner == nil {
		return buildServiceRootMigrationPlan(context.Background(), s.cfg, *sv.AsStruct(), request)
	}
	return buildServiceRootMigrationPlanWithRunner(context.Background(), s.cfg, s.zfsRunner, *sv.AsStruct(), request)
}

func validateServiceSetSnapshotChange(flags cli.ServiceSetFlags, changes serviceSetChanges) error {
	if !changes.snapshot {
		return nil
	}
	return validateServiceSnapshotFlags(flags)
}

func (e *ttyExecer) applyServiceSetRootChange(flags cli.ServiceSetFlags, changes serviceSetChanges) error {
	if !changes.root {
		return nil
	}
	return e.serviceSetRoot(flags)
}

func (e *ttyExecer) applyServiceSetPublishChange(flags cli.ServiceSetFlags, changes serviceSetChanges) error {
	if !changes.publish {
		return nil
	}
	return e.s.updateServicePublish(e.sn, flags)
}

func (s *Server) updateServicePublish(name string, flags cli.ServiceSetFlags) error {
	changed, err := s.updateServicePublishData(name, flags)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return s.restartServicePublish(name)
}

func (s *Server) updateServicePublishData(name string, flags cli.ServiceSetFlags) (bool, error) {
	var changed bool
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		service, err := serviceForPublishUpdate(d, name)
		if err != nil {
			return err
		}
		changed, err = s.applyServicePublishUpdate(service, name, flags)
		return err
	})
	return changed, err
}

func serviceForPublishUpdate(d *db.Data, name string) (*db.Service, error) {
	service, ok := d.Services[name]
	if !ok {
		return nil, fmt.Errorf("service %q not found", name)
	}
	if service.ServiceType != db.ServiceTypeDockerCompose {
		return nil, fmt.Errorf("service %q is not a docker compose service", name)
	}
	return service, nil
}

func (s *Server) applyServicePublishUpdate(service *db.Service, name string, flags cli.ServiceSetFlags) (bool, error) {
	current, err := currentServicePublishPorts(service, name)
	if err != nil {
		return false, err
	}
	desired := normalizePublish(flags.Publish)
	if err := validateServicePublishReplacement(name, current, desired, flags.PublishReset); err != nil {
		return false, err
	}
	if servicePublishAlreadyCurrent(service, current, desired) {
		service.Publish = desired
		return false, nil
	}

	root := s.serviceRootFromView(service.View())
	composePath, nextGen, err := writeServiceSetPublishCompose(service, root, name, desired)
	if err != nil {
		return false, err
	}
	promoteServicePublishGeneration(service, nextGen, composePath, desired)
	return true, nil
}

func validateServicePublishReplacement(serviceName string, current, desired []string, reset bool) error {
	if reset {
		return nil
	}
	if missing := missingServicePublishPorts(current, desired); len(missing) != 0 {
		return serviceSetPublishMissingPortsError(serviceName, current, desired, missing)
	}
	return nil
}

func servicePublishAlreadyCurrent(service *db.Service, current, desired []string) bool {
	return equalStringSlices(normalizePublish(service.Publish), desired) && equalStringSlices(current, desired)
}

func (s *Server) restartServicePublish(name string) error {
	compose, err := s.dockerComposeService(name)
	if err != nil {
		return err
	}
	if err := upDockerComposeForServiceSet(compose); err != nil {
		return fmt.Errorf("failed to start docker compose service: %w", err)
	}
	return nil
}

func currentServicePublishPorts(service *db.Service, serviceName string) ([]string, error) {
	if ports := normalizePublish(service.Publish); len(ports) != 0 {
		return ports, nil
	}
	composePath, ok := serviceComposePathForPublish(service)
	if !ok {
		return nil, nil
	}
	return readComposePorts(composePath, serviceName)
}

func writeServiceSetPublishCompose(service *db.Service, root, serviceName string, publish []string) (string, int, error) {
	src, ok := serviceComposePathForPublish(service)
	if !ok {
		return "", 0, fmt.Errorf("compose file not found for service %q", serviceName)
	}
	nextGen := nextServicePublishGeneration(service, src, root)
	dst := servicePublishComposePath(root, nextGen)
	content, err := os.ReadFile(src)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		return "", 0, err
	}
	if err := updateComposePorts(dst, serviceName, publish); err != nil {
		return "", 0, err
	}
	return dst, nextGen, nil
}

func serviceComposePathForPublish(service *db.Service) (string, bool) {
	if service == nil {
		return "", false
	}
	if service.Generation != 0 {
		if path, ok := service.Artifacts.Gen(db.ArtifactDockerComposeFile, service.Generation); ok {
			return path, true
		}
	}
	if service.LatestGeneration != 0 && service.LatestGeneration != service.Generation {
		if path, ok := service.Artifacts.Gen(db.ArtifactDockerComposeFile, service.LatestGeneration); ok {
			return path, true
		}
	}
	return service.Artifacts.Latest(db.ArtifactDockerComposeFile)
}

func nextServicePublishGeneration(service *db.Service, src, root string) int {
	next := service.LatestGeneration
	if service.Generation > next {
		next = service.Generation
	}
	next++
	if next < 1 {
		next = 1
	}
	for filepath.Clean(servicePublishComposePath(root, next)) == filepath.Clean(src) {
		next++
	}
	return next
}

func servicePublishComposePath(root string, gen int) string {
	return filepath.Join(serviceBinDirForRoot(root), fmt.Sprintf("docker-compose.%d.yml", gen))
}

func promoteServicePublishGeneration(service *db.Service, gen int, composePath string, publish []string) {
	if service.Artifacts == nil {
		service.Artifacts = db.ArtifactStore{}
	}
	oldGeneration := service.Generation
	oldLatestGeneration := service.LatestGeneration
	for name, artifact := range service.Artifacts {
		if artifact == nil {
			continue
		}
		if artifact.Refs == nil {
			artifact.Refs = map[db.ArtifactRef]string{}
		}
		if name == db.ArtifactDockerComposeFile {
			continue
		}
		if path, ok := currentServiceArtifactPath(artifact, oldGeneration, oldLatestGeneration); ok {
			artifact.Refs[db.Gen(gen)] = path
			artifact.Refs["latest"] = path
		}
	}
	setServiceArtifactRef(service, db.ArtifactDockerComposeFile, gen, composePath)
	service.Publish = normalizePublish(publish)
	service.Generation = gen
	service.LatestGeneration = gen
}

func currentServiceArtifactPath(artifact *db.Artifact, generation, latestGeneration int) (string, bool) {
	if artifact == nil {
		return "", false
	}
	if generation != 0 {
		if path, ok := artifact.Refs[db.Gen(generation)]; ok {
			return path, true
		}
	}
	if latestGeneration != 0 && latestGeneration != generation {
		if path, ok := artifact.Refs[db.Gen(latestGeneration)]; ok {
			return path, true
		}
	}
	path, ok := artifact.Refs["latest"]
	return path, ok
}

func setServiceArtifactRef(service *db.Service, name db.ArtifactName, gen int, path string) {
	artifact, ok := service.Artifacts[name]
	if !ok || artifact == nil {
		artifact = &db.Artifact{}
		service.Artifacts[name] = artifact
	}
	if artifact.Refs == nil {
		artifact.Refs = map[db.ArtifactRef]string{}
	}
	artifact.Refs[db.Gen(gen)] = path
	artifact.Refs["latest"] = path
}

func missingServicePublishPorts(current, desired []string) []string {
	desiredSet := map[string]bool{}
	for _, port := range desired {
		desiredSet[port] = true
	}
	var missing []string
	for _, port := range current {
		if !desiredSet[port] {
			missing = append(missing, port)
		}
	}
	return missing
}

func serviceSetPublishMissingPortsError(serviceName string, current, desired, missing []string) error {
	return fmt.Errorf(
		"changing published ports would remove existing mappings:\n  %s\n\nTo keep them, include them explicitly:\n  %s\n\nTo replace the published port list, re-run with --publish-reset:\n  %s",
		strings.Join(missing, "\n  "),
		formatServiceSetPublishCommand(serviceName, false, retainedServicePublishPorts(current, desired)),
		formatServiceSetPublishCommand(serviceName, true, desired),
	)
}

func retainedServicePublishPorts(current, desired []string) []string {
	seen := map[string]bool{}
	retained := make([]string, 0, len(current)+len(desired))
	for _, port := range current {
		if seen[port] {
			continue
		}
		seen[port] = true
		retained = append(retained, port)
	}
	for _, port := range desired {
		if seen[port] {
			continue
		}
		seen[port] = true
		retained = append(retained, port)
	}
	return retained
}

func formatServiceSetPublishCommand(serviceName string, reset bool, ports []string) string {
	args := []string{"yeet", "service", "set", serviceName}
	if reset {
		args = append(args, "--publish-reset")
	}
	for _, port := range ports {
		args = append(args, "-p", port)
	}
	return strings.Join(args, " ")
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (e *ttyExecer) serviceSetRoot(flags cli.ServiceSetFlags) error {
	mode := serviceRootMigrationPrompt
	if flags.Copy {
		mode = serviceRootMigrationCopy
	}
	if flags.Empty {
		mode = serviceRootMigrationEmpty
	}
	if mode == serviceRootMigrationPrompt && !e.isPty {
		return serviceRootMigrationModeRequiredError()
	}

	request := serviceRootMigrationRequest{Root: flags.ServiceRoot, ZFS: flags.ZFS}
	plan, err := e.s.validateServiceRootMigration(e.sn, request)
	if err != nil {
		return err
	}
	mode, err = e.confirmServiceRootMigrationMode(mode, plan)
	if err != nil {
		return err
	}
	if e.serviceOperationLockHeld {
		return e.s.migrateServiceRootWithPlanWriterLocked(plan, mode, e.rw)
	}
	return e.s.migrateServiceRootWithPlanWriter(plan, mode, e.rw)
}

func (s *Server) updateServiceSnapshotPolicy(name string, flags cli.ServiceSetFlags) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		service, ok := d.Services[name]
		if !ok {
			return fmt.Errorf("service %q not found", name)
		}
		if err := validateSnapshotInheritExclusive(flags); err != nil {
			return err
		}
		return applySnapshotFlagsToService(service, flags)
	})
	return err
}

func applySnapshotFlagsToService(service *db.Service, flags cli.ServiceSetFlags) error {
	if err := validateServiceSnapshotFlags(flags); err != nil {
		return err
	}
	if flags.Snapshots == "inherit" {
		service.SnapshotPolicy = nil
		return nil
	}
	policy := service.SnapshotPolicy
	if policy == nil {
		policy = &db.SnapshotPolicy{}
	}
	if err := applyServiceSnapshotFlags(policy, flags); err != nil {
		return err
	}
	service.SnapshotPolicy = policy
	return nil
}

func validateServiceSnapshotFlags(flags cli.ServiceSetFlags) error {
	if err := validateSnapshotInheritExclusive(flags); err != nil {
		return err
	}
	return applyServiceSnapshotFlags(&db.SnapshotPolicy{}, flags)
}

func validateSnapshotInheritExclusive(flags cli.ServiceSetFlags) error {
	if flags.Snapshots != "inherit" {
		return nil
	}
	if flags.SnapshotKeepLast == "" && flags.SnapshotMaxAge == "" && flags.SnapshotRequired == "" && flags.SnapshotEvents == "" {
		return nil
	}
	return fmt.Errorf("--snapshots=inherit cannot be combined with field-level snapshot flags")
}

func applyServiceSnapshotFlags(policy *db.SnapshotPolicy, flags cli.ServiceSetFlags) error {
	applyServiceSnapshotModeFlag(policy, flags.Snapshots)
	if err := applyServiceSnapshotKeepLastFlag(policy, flags.SnapshotKeepLast); err != nil {
		return err
	}
	if err := applyServiceSnapshotMaxAgeFlag(policy, flags.SnapshotMaxAge); err != nil {
		return err
	}
	if err := applyServiceSnapshotRequiredFlag(policy, flags.SnapshotRequired); err != nil {
		return err
	}
	return applyServiceSnapshotEventsFlag(policy, flags.SnapshotEvents)
}

func applyServiceSnapshotModeFlag(policy *db.SnapshotPolicy, value string) {
	switch value {
	case "on":
		v := true
		policy.Enabled = &v
	case "off":
		v := false
		policy.Enabled = &v
	}
}

func applyServiceSnapshotKeepLastFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.KeepLast = nil
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return fmt.Errorf("--snapshot-keep-last must be a positive integer or inherit")
	}
	policy.KeepLast = &n
	return nil
}

func applyServiceSnapshotMaxAgeFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.MaxAge = ""
		return nil
	}
	if _, err := parseSnapshotMaxAge(value); err != nil {
		return err
	}
	policy.MaxAge = value
	return nil
}

func applyServiceSnapshotRequiredFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.Required = nil
		return nil
	}
	v, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("invalid --snapshot-required value %q", value)
	}
	policy.Required = &v
	return nil
}

func applyServiceSnapshotEventsFlag(policy *db.SnapshotPolicy, value string) error {
	if value == "" {
		return nil
	}
	if value == "inherit" {
		policy.Events = nil
		return nil
	}
	events, err := parseSnapshotEvents(value)
	if err != nil {
		return err
	}
	policy.Events = events
	return nil
}

func (e *ttyExecer) confirmServiceRootMigrationMode(mode serviceRootMigrationMode, plan serviceRootMigrationPlan) (serviceRootMigrationMode, error) {
	if mode != serviceRootMigrationPrompt {
		return mode, nil
	}
	if !e.isPty {
		return 0, serviceRootMigrationModeRequiredError()
	}
	ok, err := cmdutil.Confirm(e.rw, e.rw, fmt.Sprintf("Copy existing service files from %s to %s?", plan.OldRoot, plan.NewRoot))
	if err != nil {
		return 0, err
	}
	if ok {
		return serviceRootMigrationCopy, nil
	}
	return serviceRootMigrationEmpty, nil
}

func serviceRootMigrationModeRequiredError() error {
	return fmt.Errorf("service set --service-root requires --copy or --empty when not running interactively")
}

func (s *Server) validateServiceRootMigration(name string, request serviceRootMigrationRequest) (serviceRootMigrationPlan, error) {
	sv, err := s.stoppedServiceForRootMigration(name)
	if err != nil {
		return serviceRootMigrationPlan{}, err
	}
	if s.zfsRunner == nil {
		return buildServiceRootMigrationPlan(context.Background(), s.cfg, *sv.AsStruct(), request)
	}
	return buildServiceRootMigrationPlanWithRunner(context.Background(), s.cfg, s.zfsRunner, *sv.AsStruct(), request)
}

func (s *Server) stoppedServiceForRootMigration(name string) (db.ServiceView, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		if errors.Is(err, errServiceNotFound) {
			return db.ServiceView{}, fmt.Errorf("service %q not found", name)
		}
		return db.ServiceView{}, err
	}
	running, err := isServiceRunningForRootMigration(s, name)
	if err != nil {
		return db.ServiceView{}, err
	}
	if running {
		return db.ServiceView{}, fmt.Errorf("cannot migrate service root while %q is running", name)
	}
	return sv, nil
}

func (s *Server) migrateServiceRoot(name string, request serviceRootMigrationRequest, mode serviceRootMigrationMode) error {
	plan, err := s.validateServiceRootMigration(name, request)
	if err != nil {
		return err
	}
	return s.migrateServiceRootWithPlan(plan, mode)
}

func (s *Server) migrateServiceRootWithPlan(plan serviceRootMigrationPlan, mode serviceRootMigrationMode) error {
	return s.migrateServiceRootWithPlanWriter(plan, mode, io.Discard)
}

func (s *Server) migrateServiceRootWithPlanWriter(plan serviceRootMigrationPlan, mode serviceRootMigrationMode, w io.Writer) error {
	if err := s.checkServiceIdentityMutationAllowed(plan.ServiceName); err != nil {
		return err
	}
	release := s.serviceOperationLocks.Lock(plan.ServiceName)
	defer release()
	if err := s.checkServiceIdentityMutationAllowed(plan.ServiceName); err != nil {
		return err
	}
	return s.migrateServiceRootWithPlanWriterLocked(plan, mode, w)
}

func (s *Server) migrateServiceRootWithPlanWriterLocked(plan serviceRootMigrationPlan, mode serviceRootMigrationMode, w io.Writer) error {
	return WithVMRuntimeTransactionLock(context.Background(), &s.cfg, func() error {
		return s.migrateServiceRootWithPlanWriterRuntimeLocked(plan, mode, w)
	})
}

func (s *Server) migrateServiceRootWithPlanWriterRuntimeLocked(plan serviceRootMigrationPlan, mode serviceRootMigrationMode, w io.Writer) error {
	oldService, err := s.serviceForRootMigrationPlan(plan)
	if err != nil {
		return err
	}
	plan.Mode = mode
	return s.withServiceSnapshot(context.Background(), snapshotOperation{
		Service: oldService,
		Event:   snapshotEventServiceRootMigration,
		Writer:  w,
		Operation: func() error {
			return s.executeServiceRootMigrationPlan(oldService, plan, w)
		},
	})
}

func (s *Server) executeServiceRootMigrationPlan(oldService *db.Service, plan serviceRootMigrationPlan, w io.Writer) error {
	if err := s.prepareServiceRootMigrationTargetDataset(plan); err != nil {
		return err
	}
	if err := materializeServiceRootMigration(context.Background(), plan, w); err != nil {
		return err
	}
	updatedService, err := updatedServiceForRootMigration(s.cfg, plan, oldService)
	if err != nil {
		return err
	}
	if err := s.validateServiceRootMigrationPlanCurrent(plan); err != nil {
		return err
	}
	if err := applyServiceRootMigrationRuntimeChangesForConfigsWithDeps(
		context.Background(), s.cfg, s.cfg, *oldService, *updatedService, w, s.runtimeReconcileDependencies().descriptor,
	); err != nil {
		return err
	}
	if err := s.updateMigratedServiceRoot(plan, updatedService); err != nil {
		return err
	}
	if updatedService.VM != nil && updatedService.VM.Components != nil {
		if err := s.runtimeReconcileDependencies().units.systemctl("daemon-reload"); err != nil {
			return fmt.Errorf("reload systemd after VM service-root migration: %w", err)
		}
	}
	return s.refreshServiceRootMigrationPrereqs(oldService, updatedService)
}

func (s *Server) prepareServiceRootMigrationTargetDataset(plan serviceRootMigrationPlan) error {
	if !plan.CreateNewRootZFS {
		return nil
	}
	runner := s.zfsRunner
	if runner == nil {
		runner = runZFSCommand
	}
	if err := zfsCreateDataset(context.Background(), runner, plan.NewRootZFS); err != nil {
		return err
	}
	mountpoint, err := zfsDatasetMountpoint(context.Background(), runner, plan.NewRootZFS)
	if err != nil {
		return err
	}
	if filepath.Clean(mountpoint) != filepath.Clean(plan.NewRoot) {
		return fmt.Errorf("new ZFS dataset %q mounted at %s, planned %s", plan.NewRootZFS, mountpoint, plan.NewRoot)
	}
	return nil
}

func (s *Server) validateServiceRootMigrationPlanCurrent(plan serviceRootMigrationPlan) error {
	_, err := s.serviceForRootMigrationPlan(plan)
	return err
}

func (s *Server) serviceForRootMigrationPlan(plan serviceRootMigrationPlan) (*db.Service, error) {
	sv, err := s.stoppedServiceForRootMigration(plan.ServiceName)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(s.serviceRootFromView(sv)) != filepath.Clean(plan.OldRoot) || sv.ServiceRootZFS() != plan.OldRootZFS {
		return nil, fmt.Errorf("service root for %q changed during migration planning", plan.ServiceName)
	}
	return sv.AsStruct(), nil
}

func (s *Server) updateMigratedServiceRoot(plan serviceRootMigrationPlan, updatedService *db.Service) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		currentService, ok := d.Services[plan.ServiceName]
		if !ok {
			return fmt.Errorf("service %q not found", plan.ServiceName)
		}
		if filepath.Clean(s.serviceRootFromView(currentService.View())) != filepath.Clean(plan.OldRoot) || currentService.ServiceRootZFS != plan.OldRootZFS {
			return fmt.Errorf("service root for %q changed during migration planning", plan.ServiceName)
		}
		d.Services[plan.ServiceName] = updatedService.Clone()
		return nil
	})
	return err
}
