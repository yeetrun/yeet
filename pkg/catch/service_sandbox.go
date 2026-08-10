// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
)

type serviceSandboxPlanRequest struct {
	Service        string
	Policy         serviceSandboxPolicy
	Payload        string
	DataDir        string
	ResolverSource string
	UID            uint32
	GID            uint32
	Hostname       string
}

type serviceSandboxMount struct {
	Source      string
	Destination string
	Writable    bool
	Kind        string
}

type serviceSandboxPlan struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	HomeDirectory    string
	Mounts           []serviceSandboxMount
}

type serviceSandboxValidationDeps struct {
	lstat        func(string) (bubblewrapFileStat, error)
	evalSymlinks func(string) (string, error)
	checkAccess  func(string, []string, uint32, uint32) error
}

type serviceSandboxPlanDeps struct {
	validation  serviceSandboxValidationDeps
	pathPresent func(string) bool
}

var serviceSandboxFixedEtcPaths = []string{
	"/etc/ld.so.cache",
	"/etc/ld.so.conf",
	"/etc/ld.so.conf.d",
	"/etc/nsswitch.conf",
	"/etc/passwd",
	"/etc/group",
	"/etc/hosts",
	"/etc/localtime",
	"/etc/timezone",
	"/etc/os-release",
	"/etc/ssl/certs",
	"/etc/ssl/openssl.cnf",
	"/etc/ca-certificates.conf",
}

func buildServiceSandboxPlan(req serviceSandboxPlanRequest) (serviceSandboxPlan, error) {
	return buildServiceSandboxPlanWith(req, defaultServiceSandboxPlanDeps())
}

func buildServiceSandboxPlanWith(req serviceSandboxPlanRequest, deps serviceSandboxPlanDeps) (serviceSandboxPlan, error) {
	req, deps, err := prepareServiceSandboxPlan(req, deps)
	if err != nil {
		return serviceSandboxPlan{}, err
	}

	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--disable-userns",
		"--uid", strconv.FormatUint(uint64(req.UID), 10),
		"--gid", strconv.FormatUint(uint64(req.GID), 10),
		"--hostname", req.Hostname,
		"--new-session", "--die-with-parent",
	}
	mounts := make([]serviceSandboxMount, 0, 8+len(serviceSandboxFixedEtcPaths)+len(req.Policy.ReadOnly)+len(req.Policy.Writable))

	present := map[string]bool{"/usr": true}
	runtimeArgs := bubblewrapFixedRuntimeMountArgs(func(path string) bool {
		value := deps.pathPresent(path)
		present[path] = value
		return value
	})
	args = append(args, runtimeArgs...)
	for _, path := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if present[path] {
			mounts = append(mounts, serviceSandboxMount{Source: path, Destination: path, Kind: "bind"})
		}
	}
	for _, path := range serviceSandboxFixedEtcPaths {
		if !deps.pathPresent(path) {
			continue
		}
		appendServiceSandboxBind(&args, &mounts, path, path, false, "bind")
	}
	appendServiceSandboxBind(&args, &mounts, req.ResolverSource, "/etc/resolv.conf", false, "bind")
	appendServiceSandboxBind(&args, &mounts, req.Payload, req.Payload, false, "bind")
	appendServiceSandboxBind(&args, &mounts, req.DataDir, req.DataDir, true, "bind")
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run")
	mounts = append(mounts,
		serviceSandboxMount{Source: "proc", Destination: "/proc", Writable: true, Kind: "proc"},
		serviceSandboxMount{Source: "dev", Destination: "/dev", Writable: true, Kind: "dev"},
		serviceSandboxMount{Destination: "/tmp", Writable: true, Kind: "tmpfs"},
		serviceSandboxMount{Destination: "/run", Writable: true, Kind: "tmpfs"},
	)
	for _, operator := range sortedServiceSandboxOperatorMounts(req.Policy) {
		appendServiceSandboxBind(&args, &mounts, operator.Source, operator.Destination, operator.Writable, "bind")
	}
	args = append(args, "--chdir", req.DataDir, "--")
	return serviceSandboxPlan{
		Executable:       bubblewrapPath,
		Arguments:        args,
		WorkingDirectory: req.DataDir,
		HomeDirectory:    req.DataDir,
		Mounts:           mounts,
	}, nil
}

func prepareServiceSandboxPlan(req serviceSandboxPlanRequest, deps serviceSandboxPlanDeps) (serviceSandboxPlanRequest, serviceSandboxPlanDeps, error) {
	if deps.pathPresent == nil {
		deps.pathPresent = defaultServiceSandboxPlanDeps().pathPresent
	}
	if req.Policy.State != "on" {
		return serviceSandboxPlanRequest{}, serviceSandboxPlanDeps{}, fmt.Errorf("build service sandbox plan requires active on policy, got %q", req.Policy.State)
	}
	if err := validateServiceSandboxLexicalPath(req.ResolverSource, "resolver source"); err != nil {
		return serviceSandboxPlanRequest{}, serviceSandboxPlanDeps{}, err
	}
	if err := validateServiceSandboxManagedPaths(req); err != nil {
		return serviceSandboxPlanRequest{}, serviceSandboxPlanDeps{}, err
	}
	policy, err := validateServiceSandboxPolicyWith(req, true, deps.validation)
	if err != nil {
		return serviceSandboxPlanRequest{}, serviceSandboxPlanDeps{}, err
	}
	req.Policy = policy
	return req, deps, nil
}

func appendServiceSandboxBind(args *[]string, mounts *[]serviceSandboxMount, source, destination string, writable bool, kind string) {
	flag := "--ro-bind"
	if writable {
		flag = "--bind"
	}
	*args = append(*args, flag, source, destination)
	*mounts = append(*mounts, serviceSandboxMount{Source: source, Destination: destination, Writable: writable, Kind: kind})
}

func sortedServiceSandboxOperatorMounts(policy serviceSandboxPolicy) []serviceSandboxMount {
	mounts := make([]serviceSandboxMount, 0, len(policy.ReadOnly)+len(policy.Writable))
	for _, exposure := range policy.ReadOnly {
		mounts = append(mounts, serviceSandboxMount{Source: exposure.Source, Destination: exposure.Destination})
	}
	for _, exposure := range policy.Writable {
		mounts = append(mounts, serviceSandboxMount{Source: exposure.Source, Destination: exposure.Destination, Writable: true})
	}
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].Destination == mounts[j].Destination {
			if mounts[i].Source == mounts[j].Source {
				return !mounts[i].Writable && mounts[j].Writable
			}
			return mounts[i].Source < mounts[j].Source
		}
		return mounts[i].Destination < mounts[j].Destination
	})
	return mounts
}

//lint:ignore U1000 Task 6 consumes this policy-validation interface.
func validateServiceSandboxPolicy(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
	return validateServiceSandboxPolicyWith(req, active, defaultServiceSandboxValidationDeps())
}

func validateServiceSandboxPolicyWith(req serviceSandboxPlanRequest, active bool, deps serviceSandboxValidationDeps) (serviceSandboxPolicy, error) {
	policy, err := normalizeServiceSandboxPolicy(req.Policy)
	if err != nil {
		return serviceSandboxPolicy{}, err
	}
	if err := validateServiceSandboxManagedPaths(req); err != nil {
		return serviceSandboxPolicy{}, err
	}
	if err := validateServiceSandboxMandatoryCollisions(req, policy); err != nil {
		return serviceSandboxPolicy{}, err
	}
	if !active {
		return policy, nil
	}
	deps = completeServiceSandboxValidationDeps(deps)
	readOnly, err := validateServiceSandboxExposureSources(policy.ReadOnly, false, req.UID, req.GID, deps)
	if err != nil {
		return serviceSandboxPolicy{}, err
	}
	writable, err := validateServiceSandboxExposureSources(policy.Writable, true, req.UID, req.GID, deps)
	if err != nil {
		return serviceSandboxPolicy{}, err
	}
	policy.ReadOnly = readOnly
	policy.Writable = writable
	return normalizeServiceSandboxPolicy(policy)
}

func validateServiceSandboxManagedPaths(req serviceSandboxPlanRequest) error {
	for _, managed := range []struct{ field, path string }{{"payload", req.Payload}, {"data directory", req.DataDir}} {
		if err := validateServiceSandboxLexicalPath(managed.path, managed.field); err != nil {
			return err
		}
	}
	if req.ResolverSource != "" {
		if err := validateServiceSandboxLexicalPath(req.ResolverSource, "resolver source"); err != nil {
			return err
		}
	}
	return nil
}

func validateServiceSandboxMandatoryCollisions(req serviceSandboxPlanRequest, policy serviceSandboxPolicy) error {
	mandatory := make([]string, 0, 12+len(serviceSandboxFixedEtcPaths))
	mandatory = append(mandatory, "/usr", "/bin", "/sbin", "/lib", "/lib64")
	mandatory = append(mandatory, serviceSandboxFixedEtcPaths...)
	mandatory = append(mandatory, "/etc/resolv.conf", req.Payload, req.DataDir, "/proc", "/dev", "/tmp", "/run")
	for _, exposure := range append(append([]serviceSandboxExposure(nil), policy.ReadOnly...), policy.Writable...) {
		for _, destination := range mandatory {
			if serviceSandboxDestinationsOverlap(exposure.Destination, destination) {
				return fmt.Errorf("sandbox destination %s collides with mandatory destination %s", exposure.Destination, destination)
			}
		}
	}
	return nil
}

func validateServiceSandboxExposureSources(exposures []serviceSandboxExposure, writable bool, uid, gid uint32, deps serviceSandboxValidationDeps) ([]serviceSandboxExposure, error) {
	validated := make([]serviceSandboxExposure, len(exposures))
	for index, exposure := range exposures {
		canonical, err := validateServiceSandboxExposureSource(exposure.Source, writable, uid, gid, deps)
		if err != nil {
			return nil, err
		}
		validated[index] = serviceSandboxExposure{Source: canonical, Destination: exposure.Destination}
	}
	return validated, nil
}

func validateServiceSandboxExposureSource(source string, writable bool, uid, gid uint32, deps serviceSandboxValidationDeps) (string, error) {
	if err := inspectInitialServiceSandboxSource(source, deps); err != nil {
		return "", err
	}
	canonical, before, err := inspectCanonicalServiceSandboxSource(source, writable, deps)
	if err != nil {
		return "", err
	}
	if err := checkServiceSandboxSourceAccess(canonical, before.mode, writable, uid, gid, deps); err != nil {
		return "", err
	}
	if err := recheckServiceSandboxSource(canonical, before, deps); err != nil {
		return "", err
	}
	return canonical, nil
}

func inspectInitialServiceSandboxSource(source string, deps serviceSandboxValidationDeps) error {
	initial, err := deps.lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sandbox source %s is missing", source)
	}
	if err != nil {
		return fmt.Errorf("inspect sandbox source %s: %w", source, err)
	}
	if !initial.mode.IsRegular() && !initial.mode.IsDir() && initial.mode&os.ModeSymlink == 0 {
		return fmt.Errorf("sandbox source %s has unsupported file type %s", source, initial.mode.Type())
	}
	return nil
}

func inspectCanonicalServiceSandboxSource(source string, writable bool, deps serviceSandboxValidationDeps) (string, bubblewrapFileStat, error) {
	canonical, err := deps.evalSymlinks(source)
	if err != nil {
		return "", bubblewrapFileStat{}, fmt.Errorf("resolve sandbox source %s: %w", source, err)
	}
	if err := validateServiceSandboxLexicalPath(canonical, "canonical source"); err != nil {
		return "", bubblewrapFileStat{}, err
	}
	before, err := deps.lstat(canonical)
	if err != nil {
		return "", bubblewrapFileStat{}, fmt.Errorf("inspect canonical sandbox source %s: %w", canonical, err)
	}
	if err := validateServiceSandboxSourceType(canonical, before.mode, writable); err != nil {
		return "", bubblewrapFileStat{}, err
	}
	return canonical, before, nil
}

func checkServiceSandboxSourceAccess(canonical string, mode os.FileMode, writable bool, uid, gid uint32, deps serviceSandboxValidationDeps) error {
	accessArgs := serviceSandboxAccessArgs(canonical, mode.IsDir(), writable)
	if err := deps.checkAccess("/usr/bin/test", accessArgs, uid, gid); err != nil {
		class := "read-only"
		if writable {
			class = "writable"
		}
		return fmt.Errorf("UID %d GID %d cannot access %s sandbox source %s: %w", uid, gid, class, canonical, err)
	}
	return nil
}

func recheckServiceSandboxSource(canonical string, before bubblewrapFileStat, deps serviceSandboxValidationDeps) error {
	after, err := deps.lstat(canonical)
	if err != nil {
		return fmt.Errorf("reinspect canonical sandbox source %s after access validation: %w", canonical, err)
	}
	if before.dev != after.dev || before.ino != after.ino || before.mode.Type() != after.mode.Type() {
		return fmt.Errorf("sandbox source %s was replaced during access validation", canonical)
	}
	return nil
}

func validateServiceSandboxSourceType(source string, mode os.FileMode, writable bool) error {
	if writable {
		if !mode.IsDir() {
			return fmt.Errorf("writable sandbox source %s must be a directory", source)
		}
		return nil
	}
	if !mode.IsRegular() && !mode.IsDir() {
		return fmt.Errorf("sandbox source %s has unsupported file type %s", source, mode.Type())
	}
	return nil
}

func serviceSandboxAccessArgs(source string, directory, writable bool) []string {
	args := []string{"-r", source}
	if writable {
		args = append(args, "-a", "-w", source)
	}
	if directory {
		args = append(args, "-a", "-x", source)
	}
	return args
}

func defaultServiceSandboxPlanDeps() serviceSandboxPlanDeps {
	return serviceSandboxPlanDeps{
		validation: defaultServiceSandboxValidationDeps(),
		pathPresent: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
	}
}

func defaultServiceSandboxValidationDeps() serviceSandboxValidationDeps {
	return serviceSandboxValidationDeps{
		lstat: func(path string) (bubblewrapFileStat, error) {
			info, err := os.Lstat(path)
			if err != nil {
				return bubblewrapFileStat{}, err
			}
			return bubblewrapStatFromFileInfo(info)
		},
		evalSymlinks: filepath.EvalSymlinks,
		checkAccess: func(path string, args []string, uid, gid uint32) error {
			command := exec.Command(path, args...)
			command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Run(); err != nil {
				return fmt.Errorf("%w; stderr: %s", err, stderr.String())
			}
			return nil
		},
	}
}

func completeServiceSandboxValidationDeps(deps serviceSandboxValidationDeps) serviceSandboxValidationDeps {
	defaults := defaultServiceSandboxValidationDeps()
	if deps.lstat == nil {
		deps.lstat = defaults.lstat
	}
	if deps.evalSymlinks == nil {
		deps.evalSymlinks = defaults.evalSymlinks
	}
	if deps.checkAccess == nil {
		deps.checkAccess = defaults.checkAccess
	}
	return deps
}
