// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultVMJailerBase = "/var/lib/yeet/vm-jailer"
	vmJailerNoFileLimit = 4096
)

func vmJailerBaseForDataRoot(dataRoot string) string {
	dataRoot = strings.TrimSpace(dataRoot)
	if dataRoot == "" {
		return defaultVMJailerBase
	}
	return filepath.Join(filepath.Clean(dataRoot), "vm-jailer")
}

type vmJailBind struct {
	Source          string
	Target          string
	ReadOnly        bool
	OwnedByRuntime  bool
	CreateDirectory bool
}

type vmJailDevice struct {
	Source string
	Target string
}

type vmJailSocketLink struct {
	HostPath string
	JailPath string
}

type vmJailPlan struct {
	ID              string
	InstanceRoot    string
	JailRoot        string
	Binds           []vmJailBind
	Devices         []vmJailDevice
	SocketLinks     []vmJailSocketLink
	RuntimeIdentity vmRuntimeIdentity
}

var errVMJailerRuntimeVersionMismatch = errors.New("VM jailer runtime version mismatch")

var (
	vmJailerVersionPattern      = regexp.MustCompile(`\bv?([0-9]+\.[0-9]+\.[0-9]+([-+][A-Za-z0-9.-]+)?)\b`)
	vmJailRunCommand            = runVMJailCommand
	vmJailEnsureRuntimeIdentity = ensureVMRuntimeIdentity
	vmJailValidateRuntimePair   = validateVMJailerRuntimePair
	vmJailPrepare               = prepareVMJail
	vmJailCleanup               = cleanupVMJail
	vmJailResourceChown         = os.Chown
	vmJailResourceChmod         = os.Chmod
	vmJailDeviceStat            = unix.Stat
	vmJailValidateTrustedInput  = func(path string) error { return validateTrustedVMJailerInput(path, 0) }
	vmJailValidateJailRoot      = func(path string) error { return validateTrustedVMJailerPath(path, 0) }
	vmJailValidateReadOnlyPath  = func(path string) error { return validateTrustedVMJailerPath(path, uint32(os.Geteuid())) }
	vmJailProbeVersion          = probeVMRuntimeVersion
)

func newVMJailCleanupPlan(service, firecracker, jailerBase string, socketPaths []string) vmJailPlan {
	base := strings.TrimSpace(jailerBase)
	if base == "" {
		base = defaultVMJailerBase
	}
	instanceRoot := filepath.Join(base, filepath.Base(firecracker), vmJailerID(service))
	plan := vmJailPlan{
		ID:           vmJailerID(service),
		InstanceRoot: instanceRoot,
		JailRoot:     filepath.Join(instanceRoot, "root"),
	}
	for _, hostPath := range socketPaths {
		hostPath = strings.TrimSpace(hostPath)
		if hostPath == "" || !filepath.IsAbs(hostPath) {
			continue
		}
		plan.SocketLinks = append(plan.SocketLinks, vmJailSocketLink{
			HostPath: hostPath,
			JailPath: vmJailCanonicalPath(plan.JailRoot, hostPath),
		})
	}
	return plan
}

func buildVMJailPlan(cfg VMConsoleProxyConfig, identity vmRuntimeIdentity) (vmJailPlan, error) {
	if identity.UID <= 0 || identity.GID <= 0 {
		return vmJailPlan{}, fmt.Errorf("VM jail runtime identity must be non-root")
	}
	if err := validateVMJailCanonicalInputs(cfg); err != nil {
		return vmJailPlan{}, err
	}
	base := strings.TrimSpace(cfg.JailerBase)
	execName := filepath.Base(cfg.Firecracker)
	instanceRoot := filepath.Join(base, execName, vmJailerID(cfg.Service))
	plan := vmJailPlan{
		ID:              vmJailerID(cfg.Service),
		InstanceRoot:    instanceRoot,
		JailRoot:        filepath.Join(instanceRoot, "root"),
		RuntimeIdentity: identity,
	}

	fcConfig, err := readVMJailFirecrackerConfig(cfg.ConfigFile)
	if err != nil {
		return vmJailPlan{}, err
	}
	if err := addVMJailConfigResources(&plan, cfg, fcConfig); err != nil {
		return vmJailPlan{}, err
	}
	return plan, nil
}

func readVMJailFirecrackerConfig(path string) (firecrackerConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return firecrackerConfig{}, fmt.Errorf("read Firecracker config for jail: %w", err)
	}
	var cfg firecrackerConfig
	if err := jsonUnmarshalFirecrackerConfig(raw, &cfg); err != nil {
		return firecrackerConfig{}, err
	}
	return cfg, nil
}

func addVMJailConfigResources(plan *vmJailPlan, cfg VMConsoleProxyConfig, fcConfig firecrackerConfig) error {
	plan.addBind(cfg.ConfigFile, true, false, false)
	plan.addBind(fcConfig.BootSource.KernelImagePath, true, false, false)
	if strings.TrimSpace(fcConfig.BootSource.InitrdPath) != "" {
		plan.addBind(fcConfig.BootSource.InitrdPath, true, false, false)
	}
	for _, drive := range fcConfig.Drives {
		if err := plan.addDrive(drive); err != nil {
			return err
		}
	}
	checkpointDir := filepath.Join(serviceDataDirForRoot(cfg.ServiceRoot), "checkpoints")
	plan.addBind(checkpointDir, false, false, true)
	plan.SocketLinks = append(plan.SocketLinks, vmJailSocketLink{
		HostPath: cfg.APISocket,
		JailPath: vmJailCanonicalPath(plan.JailRoot, cfg.APISocket),
	})
	if fcConfig.Vsock != nil && strings.TrimSpace(fcConfig.Vsock.UDSPath) != "" {
		plan.SocketLinks = append(plan.SocketLinks, vmJailSocketLink{
			HostPath: fcConfig.Vsock.UDSPath,
			JailPath: vmJailCanonicalPath(plan.JailRoot, fcConfig.Vsock.UDSPath),
		})
	}
	return nil
}

func jsonUnmarshalFirecrackerConfig(raw []byte, cfg *firecrackerConfig) error {
	if err := json.Unmarshal(raw, cfg); err != nil {
		return fmt.Errorf("decode Firecracker config for jail: %w", err)
	}
	return nil
}

func validateVMJailCanonicalInputs(cfg VMConsoleProxyConfig) error {
	required := []struct {
		label string
		value string
	}{
		{"VM service", cfg.Service},
		{"VM service root", cfg.ServiceRoot},
		{"Firecracker path", cfg.Firecracker},
		{"jailer path", cfg.Jailer},
		{"VM jailer base", cfg.JailerBase},
		{"Firecracker API socket", cfg.APISocket},
		{"Firecracker config", cfg.ConfigFile},
	}
	for _, input := range required {
		if strings.TrimSpace(input.value) == "" {
			return fmt.Errorf("%s is required for VM jail", input.label)
		}
	}
	for _, input := range required[1:] {
		if !filepath.IsAbs(input.value) {
			return fmt.Errorf("%s must be an absolute path for VM jail: %s", input.label, input.value)
		}
	}
	return nil
}

func (p *vmJailPlan) addBind(source string, readOnly, owned, createDirectory bool) {
	source = filepath.Clean(source)
	for _, bind := range p.Binds {
		if bind.Source == source {
			return
		}
	}
	p.Binds = append(p.Binds, vmJailBind{
		Source:          source,
		Target:          vmJailCanonicalPath(p.JailRoot, source),
		ReadOnly:        readOnly,
		OwnedByRuntime:  owned,
		CreateDirectory: createDirectory,
	})
}

func (p *vmJailPlan) addDrive(drive firecrackerDrive) error {
	path := strings.TrimSpace(drive.PathOnHost)
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("firecracker drive %q path must be absolute for VM jail: %s", drive.DriveID, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect Firecracker drive %q for jail: %w", drive.DriveID, err)
	}
	if info.Mode()&os.ModeDevice != 0 {
		p.Devices = append(p.Devices, vmJailDevice{Source: path, Target: vmJailCanonicalPath(p.JailRoot, path)})
		return nil
	}
	p.addBind(path, drive.IsReadOnly, !drive.IsReadOnly, false)
	return nil
}

func vmJailCanonicalPath(jailRoot, canonical string) string {
	clean := filepath.Clean(canonical)
	return filepath.Join(jailRoot, strings.TrimPrefix(clean, string(filepath.Separator)))
}

func vmJailerCommandArgs(cfg VMConsoleProxyConfig, identity vmRuntimeIdentity, restoreMode bool) []string {
	args := []string{
		"--id", vmJailerID(cfg.Service),
		"--exec-file", cfg.Firecracker,
		"--uid", strconv.Itoa(identity.UID),
		"--gid", strconv.Itoa(identity.GID),
		"--chroot-base-dir", cfg.JailerBase,
		"--cgroup-version", "2",
		"--resource-limit", "no-file=" + strconv.Itoa(vmJailerNoFileLimit),
		"--",
		"--api-sock", cfg.APISocket,
	}
	if !restoreMode {
		args = append(args, "--config-file", cfg.ConfigFile)
	}
	return args
}

func prepareVMConsoleProcess(ctx context.Context, cfg VMConsoleProxyConfig, restoreMode bool) (*exec.Cmd, func(), error) {
	if err := validateVMJailCanonicalInputs(cfg); err != nil {
		return nil, nil, err
	}
	identity, err := vmJailEnsureRuntimeIdentity()
	if err != nil {
		return nil, nil, err
	}
	if err := vmJailValidateRuntimePair(ctx, cfg.Firecracker, cfg.Jailer); err != nil {
		return nil, nil, err
	}
	plan, err := buildVMJailPlan(cfg, identity)
	if err != nil {
		return nil, nil, err
	}
	if err := vmJailPrepare(plan); err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if err := vmJailCleanup(plan); err != nil {
			fmt.Fprintf(os.Stderr, "warning: clean up VM jail %s: %v\n", plan.ID, err)
		}
	}
	cmd := exec.CommandContext(ctx, cfg.Jailer, vmJailerCommandArgs(cfg, identity, restoreMode)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid:    0,
		Gid:    0,
		Groups: []uint32{},
	}}
	return cmd, cleanup, nil
}

func validateVMJailerPairVersion(firecrackerOutput, jailerOutput string) error {
	firecrackerVersion := vmJailerVersion(firecrackerOutput)
	jailerVersion := vmJailerVersion(jailerOutput)
	if firecrackerVersion == "" || jailerVersion == "" {
		return fmt.Errorf("read Firecracker/jailer version: Firecracker=%q jailer=%q", strings.TrimSpace(firecrackerOutput), strings.TrimSpace(jailerOutput))
	}
	if firecrackerVersion != jailerVersion {
		return fmt.Errorf("%w: firecracker version %s does not match jailer version %s", errVMJailerRuntimeVersionMismatch, firecrackerVersion, jailerVersion)
	}
	return nil
}

func vmJailerVersion(output string) string {
	match := vmJailerVersionPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func validateTrustedVMJailerInput(path string, ownerUID uint32) error {
	if err := validateTrustedVMJailerPath(path, ownerUID); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("trusted VM runtime input %s must be an executable regular file", path)
	}
	return nil
}

func validateTrustedVMJailerPath(path string, ownerUID uint32) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return fmt.Errorf("trusted VM runtime input must be absolute: %s", path)
	}
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect trusted VM runtime input %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("trusted VM runtime input %s contains a symbolic link", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("inspect trusted VM runtime input owner %s", current)
		}
		if stat.Uid != ownerUID && stat.Uid != 0 {
			return fmt.Errorf("trusted VM runtime input %s is owned by UID %d, want %d", current, stat.Uid, ownerUID)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("trusted VM runtime input %s is group or other writable", current)
		}
	}
	return nil
}

func validateVMJailerRuntimePair(ctx context.Context, firecracker, jailer string) error {
	for _, path := range []string{firecracker, jailer} {
		if err := vmJailValidateTrustedInput(path); err != nil {
			return err
		}
	}
	firecrackerVersion, err := vmJailProbeVersion(ctx, firecracker)
	if err != nil {
		return err
	}
	jailerVersion, err := vmJailProbeVersion(ctx, jailer)
	if err != nil {
		return err
	}
	return validateVMJailerPairVersion(firecrackerVersion, jailerVersion)
}

func probeVMRuntimeVersion(ctx context.Context, path string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, path, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read VM runtime version from %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func prepareVMJail(plan vmJailPlan) error {
	if err := prepareVMJailRoot(plan); err != nil {
		return err
	}
	for _, bind := range plan.Binds {
		if err := prepareVMJailBind(bind, plan.RuntimeIdentity); err != nil {
			_ = cleanupVMJail(plan)
			return err
		}
	}
	for _, device := range plan.Devices {
		if err := prepareVMJailDevice(device, plan.RuntimeIdentity); err != nil {
			_ = cleanupVMJail(plan)
			return err
		}
	}
	for _, link := range plan.SocketLinks {
		if err := prepareVMJailSocketLink(link, plan.RuntimeIdentity); err != nil {
			_ = cleanupVMJail(plan)
			return err
		}
	}
	return nil
}

func prepareVMJailRoot(plan vmJailPlan) error {
	if err := cleanupVMJail(plan); err != nil {
		return err
	}
	if err := os.MkdirAll(plan.JailRoot, 0o755); err != nil {
		return fmt.Errorf("create VM jail root: %w", err)
	}
	if err := vmJailValidateJailRoot(plan.InstanceRoot); err != nil {
		_ = cleanupVMJail(plan)
		return fmt.Errorf("validate VM jail root: %w", err)
	}
	if err := validateVMJailExecutableFilesystem(plan.JailRoot); err != nil {
		_ = cleanupVMJail(plan)
		return err
	}
	return nil
}

func validateVMJailExecutableFilesystem(path string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	mountInfo, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("inspect VM jail filesystem: %w", err)
	}
	defer func() { _ = mountInfo.Close() }()
	return validateVMJailMountOptions(path, mountInfo)
}

func validateVMJailMountOptions(path string, mountInfo io.Reader) error {
	path = filepath.Clean(path)
	matchedMount := ""
	matchedNoExec := false
	scanner := bufio.NewScanner(mountInfo)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			continue
		}
		mountPoint := unescapeVMJailMountPath(fields[4])
		if path != mountPoint && !strings.HasPrefix(path, mountPoint+string(filepath.Separator)) {
			continue
		}
		if len(mountPoint) < len(matchedMount) {
			continue
		}
		matchedMount = mountPoint
		matchedNoExec = slices.Contains(strings.Split(fields[5], ","), "noexec")
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("inspect VM jail filesystem: %w", err)
	}
	if matchedNoExec {
		return fmt.Errorf("VM jail base %s is on noexec mount %s; configure an executable Catch data root", path, matchedMount)
	}
	return nil
}

func prepareVMJailBind(bind vmJailBind, identity vmRuntimeIdentity) error {
	info, err := ensureVMJailBindSource(bind)
	if err != nil {
		return err
	}
	if err := normalizeVMJailBindSource(bind, identity); err != nil {
		return err
	}
	if err := validateVMJailBindSource(bind, identity); err != nil {
		return err
	}
	if err := createVMJailBindTarget(bind.Target, info.IsDir()); err != nil {
		return err
	}
	if bind.OwnedByRuntime {
		if err := delegateVMJailBindSource(bind.Source, info, identity); err != nil {
			return err
		}
	}
	if err := vmJailRunCommand([]string{"mount", "--bind", bind.Source, bind.Target}); err != nil {
		return err
	}
	if bind.ReadOnly {
		return vmJailRunCommand([]string{"mount", "-o", "remount,bind,ro,nosuid,nodev,noexec", bind.Target})
	}
	return nil
}

func validateVMJailBindSource(bind vmJailBind, identity vmRuntimeIdentity) error {
	if !bind.ReadOnly {
		return nil
	}
	linkInfo, err := os.Lstat(bind.Source)
	if err != nil {
		return fmt.Errorf("inspect VM jail read-only resource %s: %w", bind.Source, err)
	}
	return validateVMJailReadOnlyResource(bind.Source, linkInfo, identity)
}

func normalizeVMJailBindSource(bind vmJailBind, identity vmRuntimeIdentity) error {
	if !bind.CreateDirectory || bind.ReadOnly || bind.OwnedByRuntime {
		return nil
	}
	return delegateVMJailCheckpoints(bind.Source, identity)
}

func ensureVMJailBindSource(bind vmJailBind) (os.FileInfo, error) {
	info, err := os.Stat(bind.Source)
	if errors.Is(err, os.ErrNotExist) && bind.CreateDirectory {
		if err := os.MkdirAll(bind.Source, 0o700); err != nil {
			return nil, fmt.Errorf("create VM jail writable resource %s: %w", bind.Source, err)
		}
		info, err = os.Stat(bind.Source)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect VM jail bind source %s: %w", bind.Source, err)
	}
	return info, nil
}

func createVMJailBindTarget(target string, directory bool) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create VM jail bind parent: %w", err)
	}
	if directory {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		return nil
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create VM jail bind target: %w", err)
	}
	return file.Close()
}

func delegateVMJailBindSource(path string, info os.FileInfo, identity vmRuntimeIdentity) error {
	if err := vmJailResourceChown(path, identity.UID, identity.GID); err != nil {
		return fmt.Errorf("delegate VM jail resource %s: %w", path, err)
	}
	requiredMode := os.FileMode(0o600)
	if info.IsDir() {
		requiredMode = 0o700
	}
	if err := vmJailResourceChmod(path, info.Mode().Perm()|requiredMode); err != nil {
		return fmt.Errorf("set delegated VM jail resource permissions %s: %w", path, err)
	}
	return nil
}

func validateVMJailReadOnlyResource(path string, info os.FileInfo, identity vmRuntimeIdentity) error {
	if err := vmJailValidateReadOnlyPath(path); err != nil {
		return fmt.Errorf("validate VM jail read-only resource %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("VM jail read-only resource %s must be a regular non-symlink file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect VM jail read-only resource owner %s", path)
	}
	if int(stat.Uid) == identity.UID {
		return fmt.Errorf("VM jail read-only resource %s is owned by the VM runtime UID %d", path, identity.UID)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("VM jail read-only resource %s is group or other writable", path)
	}
	if info.Mode().Perm()&0o004 == 0 {
		return fmt.Errorf("VM jail read-only resource %s is not readable by the VM runtime", path)
	}
	return nil
}

func delegateVMJailStorage(root, disk string, identity vmRuntimeIdentity) error {
	if identity.UID <= 0 || identity.GID <= 0 {
		return fmt.Errorf("VM jail runtime identity must be non-root")
	}
	if err := delegateVMJailDisk(disk, identity); err != nil {
		return err
	}
	return delegateVMJailCheckpoints(filepath.Join(serviceDataDirForRoot(root), "checkpoints"), identity)
}

func delegateVMJailDisk(disk string, identity vmRuntimeIdentity) error {
	disk = filepath.Clean(strings.TrimSpace(disk))
	if disk == "." || strings.HasPrefix(disk, "/dev/") {
		return nil
	}
	if err := delegateVMJailStorageFile(disk, identity); err != nil {
		return fmt.Errorf("delegate VM raw disk: %w", err)
	}
	return nil
}

func delegateVMJailCheckpoints(checkpointDir string, identity vmRuntimeIdentity) error {
	root, parent, name, err := openVMJailStoragePath(checkpointDir)
	if err != nil {
		return openVMJailCheckpointsError(checkpointDir, err)
	}
	defer func() { _ = root.Close() }()
	if parent != nil {
		defer func() { _ = parent.Close() }()
	}
	return delegateOpenedVMJailCheckpoints(root, parent, name, checkpointDir, identity)
}

func openVMJailCheckpointsError(checkpointDir string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("delegate VM checkpoints %s: %w", checkpointDir, err)
}

func delegateOpenedVMJailCheckpoints(root, parent *os.File, name, checkpointDir string, identity vmRuntimeIdentity) error {
	info, err := root.Stat()
	if err != nil {
		return fmt.Errorf("inspect VM checkpoint root %s: %w", checkpointDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("VM checkpoint root %s must be a directory", checkpointDir)
	}
	if err := vmJailStorageChown(root, 0, 0); err != nil {
		return fmt.Errorf("set VM checkpoint root owner %s: %w", checkpointDir, err)
	}
	if err := vmJailStorageChmod(root, 0o755); err != nil {
		return fmt.Errorf("set VM checkpoint root mode %s: %w", checkpointDir, err)
	}
	if err := verifyVMJailStorageEntryUnchanged(parent, name, root, checkpointDir); err != nil {
		return err
	}
	names, err := root.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("read VM checkpoint root %s: %w", checkpointDir, err)
	}
	sort.Strings(names)
	return delegateVMJailCheckpointChildren(root, checkpointDir, names, identity)
}

func delegateVMJailCheckpointChildren(root *os.File, checkpointDir string, names []string, identity vmRuntimeIdentity) error {
	for _, childName := range names {
		if err := delegateVMJailCheckpointChild(root, checkpointDir, childName, identity); err != nil {
			return err
		}
	}
	return nil
}

func delegateVMJailCheckpointChild(root *os.File, checkpointDir, childName string, identity vmRuntimeIdentity) error {
	childPath := filepath.Join(checkpointDir, childName)
	child, childInfo, err := openVerifiedVMJailStorageChild(root, childName, childPath)
	if err != nil {
		return err
	}
	delegateErr := delegateOpenedVMJailStorageTree(root, childName, child, childPath, childInfo, identity)
	closeErr := child.Close()
	if delegateErr != nil {
		return delegateErr
	}
	if closeErr != nil {
		return fmt.Errorf("close VM checkpoint storage %s: %w", childPath, closeErr)
	}
	return nil
}

func prepareVMJailDevice(device vmJailDevice, identity vmRuntimeIdentity) error {
	var stat unix.Stat_t
	if err := vmJailDeviceStat(device.Source, &stat); err != nil {
		return fmt.Errorf("inspect VM jail block device %s: %w", device.Source, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFBLK {
		return fmt.Errorf("VM jail device source %s is not a block device", device.Source)
	}
	if err := os.MkdirAll(filepath.Dir(device.Target), 0o755); err != nil {
		return err
	}
	_ = os.Remove(device.Target)
	args := []string{"mknod", "-m", "600", device.Target, "b", strconv.FormatUint(uint64(unix.Major(uint64(stat.Rdev))), 10), strconv.FormatUint(uint64(unix.Minor(uint64(stat.Rdev))), 10)}
	if err := vmJailRunCommand(args); err != nil {
		return err
	}
	if err := vmJailResourceChown(device.Target, identity.UID, identity.GID); err != nil {
		return fmt.Errorf("delegate VM jail block device %s: %w", device.Target, err)
	}
	return nil
}

func prepareVMJailSocketLink(link vmJailSocketLink, identity vmRuntimeIdentity) error {
	if err := os.MkdirAll(filepath.Dir(link.JailPath), 0o755); err != nil {
		return err
	}
	if err := vmJailResourceChown(filepath.Dir(link.JailPath), identity.UID, identity.GID); err != nil {
		return fmt.Errorf("delegate VM jail socket directory %s: %w", filepath.Dir(link.JailPath), err)
	}
	if err := os.MkdirAll(filepath.Dir(link.HostPath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(link.HostPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale VM host socket path %s: %w", link.HostPath, err)
	}
	if err := os.Symlink(link.JailPath, link.HostPath); err != nil {
		return fmt.Errorf("link VM host socket %s: %w", link.HostPath, err)
	}
	return nil
}

func cleanupVMJail(plan vmJailPlan) error {
	var cleanupErrs []error
	for _, link := range plan.SocketLinks {
		if err := removeVMJailSocketLink(link); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	mountInfo, err := os.Open("/proc/self/mountinfo")
	if err == nil {
		for _, target := range vmJailMountTargets(mountInfo, plan.JailRoot) {
			if err := vmJailRunCommand([]string{"umount", "-l", target}); err != nil {
				cleanupErrs = append(cleanupErrs, err)
			}
		}
		_ = mountInfo.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("read VM jail mounts: %w", err))
	}
	if err := os.RemoveAll(plan.InstanceRoot); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove VM jail %s: %w", plan.InstanceRoot, err))
	}
	return errors.Join(cleanupErrs...)
}

func removeVMJailSocketLink(link vmJailSocketLink) error {
	target, err := os.Readlink(link.HostPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return nil
	}
	if target != link.JailPath {
		return nil
	}
	if err := os.Remove(link.HostPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove VM jail socket link %s: %w", link.HostPath, err)
	}
	return nil
}

func vmJailMountTargets(r io.Reader, jailRoot string) []string {
	jailRoot = filepath.Clean(jailRoot)
	var targets []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		target := unescapeVMJailMountPath(fields[4])
		if target == jailRoot || strings.HasPrefix(target, jailRoot+string(filepath.Separator)) {
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		leftDepth := strings.Count(targets[i], string(filepath.Separator))
		rightDepth := strings.Count(targets[j], string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return targets[i] < targets[j]
	})
	return targets
}

func unescapeVMJailMountPath(path string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(path)
}

func runVMJailCommand(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("VM jail command is empty")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run VM jail command %s: %w: %s", formatVMCommandArgv(argv), err, strings.TrimSpace(string(out)))
	}
	return nil
}
