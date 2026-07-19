// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBuildVMJailPlanPreservesCanonicalResourcePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom", "services", "devbox")
	runDir := serviceRunDirForRoot(root)
	dataDir := serviceDataDirForRoot(root)
	bundleDir := filepath.Join(t.TempDir(), "images", "ubuntu-v15")
	for _, dir := range []string{runDir, dataDir, bundleDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kernel := filepath.Join(bundleDir, "vmlinux")
	initrd := filepath.Join(bundleDir, "initrd.img")
	disk := filepath.Join(dataDir, "rootfs.raw")
	for path, contents := range map[string]string{kernel: "kernel", initrd: "initrd", disk: "disk"} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(runDir, "firecracker.json")
	apiSocket := filepath.Join(runDir, "firecracker.sock")
	vsockSocket := filepath.Join(runDir, "vsock.sock")
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{KernelImagePath: kernel, InitrdPath: initrd},
		Drives:     []firecrackerDrive{{DriveID: "rootfs", PathOnHost: disk, IsRootDevice: true}},
		Vsock:      &firecrackerVsock{VsockID: "agent", GuestCID: vmAgentGuestCID, UDSPath: vsockSocket},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := VMConsoleProxyConfig{
		Service:       "devbox",
		ServiceRoot:   root,
		DiskPath:      disk,
		Firecracker:   filepath.Join(bundleDir, "firecracker"),
		Jailer:        filepath.Join(bundleDir, "jailer"),
		JailerBase:    filepath.Join(t.TempDir(), "jails"),
		APISocket:     apiSocket,
		ConfigFile:    configPath,
		ConsoleSocket: filepath.Join(runDir, "serial.sock"),
	}
	plan, err := buildVMJailPlan(cfg, vmRuntimeIdentity{UID: 812, GID: 813})
	if err != nil {
		t.Fatalf("buildVMJailPlan: %v", err)
	}
	wantRoot := filepath.Join(cfg.JailerBase, "firecracker", vmJailerID(cfg.Service), "root")
	if plan.JailRoot != wantRoot {
		t.Fatalf("jail root = %q, want %q", plan.JailRoot, wantRoot)
	}
	for _, resource := range []struct {
		path     string
		readOnly bool
	}{
		{configPath, true},
		{kernel, true},
		{initrd, true},
		{disk, false},
		{filepath.Join(dataDir, "checkpoints"), false},
	} {
		bind, ok := findVMJailBind(plan.Binds, resource.path)
		if !ok {
			t.Fatalf("missing bind for %s in %#v", resource.path, plan.Binds)
		}
		if bind.Target != vmJailCanonicalPath(wantRoot, resource.path) || bind.ReadOnly != resource.readOnly {
			t.Fatalf("bind = %#v for %s", bind, resource.path)
		}
		if resource.path == filepath.Join(dataDir, "checkpoints") && bind.OwnedByRuntime {
			t.Fatalf("checkpoint root bind = %#v, must remain root-owned", bind)
		}
	}
	wantLinks := []vmJailSocketLink{
		{HostPath: apiSocket, JailPath: vmJailCanonicalPath(wantRoot, apiSocket)},
		{HostPath: vsockSocket, JailPath: vmJailCanonicalPath(wantRoot, vsockSocket)},
	}
	if !reflect.DeepEqual(plan.SocketLinks, wantLinks) {
		t.Fatalf("socket links = %#v, want %#v", plan.SocketLinks, wantLinks)
	}
}

func TestVMJailerCommandArgsDropIdentityAndPreserveSystemdCgroup(t *testing.T) {
	cfg := VMConsoleProxyConfig{
		Service:     "devbox",
		Firecracker: "/srv/images/firecracker",
		Jailer:      "/srv/images/jailer",
		JailerBase:  "/run/yeet/vm-jailer",
		APISocket:   "/srv/vms/devbox/run/firecracker.sock",
		ConfigFile:  "/srv/vms/devbox/run/firecracker.json",
	}
	got := vmJailerCommandArgs(cfg, vmRuntimeIdentity{UID: 812, GID: 813}, false)
	want := []string{
		"--id", "yeet-devbox",
		"--exec-file", "/srv/images/firecracker",
		"--uid", "812",
		"--gid", "813",
		"--chroot-base-dir", "/run/yeet/vm-jailer",
		"--cgroup-version", "2",
		"--resource-limit", "no-file=4096",
		"--",
		"--api-sock", "/srv/vms/devbox/run/firecracker.sock",
		"--config-file", "/srv/vms/devbox/run/firecracker.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{"--daemonize", "--new-pid-ns", "--netns", "--cgroup"} {
		if slicesContain(got, forbidden) {
			t.Fatalf("args contain forbidden baseline flag %q: %#v", forbidden, got)
		}
	}
}

func TestPrepareVMConsoleProcessRequiresJailer(t *testing.T) {
	_, _, err := prepareVMConsoleProcess(context.Background(), VMConsoleProxyConfig{
		Firecracker:   "/opt/vm/firecracker",
		APISocket:     "/run/vm/firecracker.sock",
		ConfigFile:    "/run/vm/firecracker.json",
		ConsoleSocket: "/run/vm/serial.sock",
		Service:       "devbox",
		ServiceRoot:   "/srv/devbox",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "jailer path is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareVMConsoleProcessUsesValidatedPreparedJailer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "services", "devbox")
	runDir := serviceRunDirForRoot(root)
	dataDir := serviceDataDirForRoot(root)
	imageDir := filepath.Join(t.TempDir(), "image")
	for _, dir := range []string{runDir, dataDir, imageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kernel := filepath.Join(imageDir, "vmlinux")
	disk := filepath.Join(dataDir, "rootfs.raw")
	for _, path := range []string{kernel, disk} {
		if err := os.WriteFile(path, []byte(path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(runDir, "firecracker.json")
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{KernelImagePath: kernel},
		Drives:     []firecrackerDrive{{DriveID: "rootfs", PathOnHost: disk, IsRootDevice: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := VMConsoleProxyConfig{
		Service:       "devbox",
		ServiceRoot:   root,
		Firecracker:   filepath.Join(imageDir, "firecracker"),
		Jailer:        filepath.Join(imageDir, "jailer"),
		JailerBase:    filepath.Join(t.TempDir(), "jails"),
		APISocket:     filepath.Join(runDir, "firecracker.sock"),
		ConfigFile:    configPath,
		ConsoleSocket: filepath.Join(runDir, "serial.sock"),
	}

	oldEnsure := vmJailEnsureRuntimeIdentity
	oldValidate := vmJailValidateRuntimePair
	oldPrepare := vmJailPrepare
	oldCleanup := vmJailCleanup
	t.Cleanup(func() {
		vmJailEnsureRuntimeIdentity = oldEnsure
		vmJailValidateRuntimePair = oldValidate
		vmJailPrepare = oldPrepare
		vmJailCleanup = oldCleanup
	})
	identity := vmRuntimeIdentity{UID: 812, GID: 813}
	vmJailEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) { return identity, nil }
	validated := false
	vmJailValidateRuntimePair = func(_ context.Context, firecracker, jailer string) error {
		validated = firecracker == cfg.Firecracker && jailer == cfg.Jailer
		return nil
	}
	prepared := false
	cleaned := false
	vmJailPrepare = func(plan vmJailPlan) error {
		prepared = plan.ID == "yeet-devbox" && plan.RuntimeIdentity == identity
		return nil
	}
	vmJailCleanup = func(plan vmJailPlan) error {
		cleaned = plan.ID == "yeet-devbox"
		return nil
	}

	cmd, cleanup, err := prepareVMConsoleProcess(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("prepareVMConsoleProcess: %v", err)
	}
	if !validated || !prepared {
		t.Fatalf("validated=%v prepared=%v", validated, prepared)
	}
	if cmd.Path != cfg.Jailer || !reflect.DeepEqual(cmd.Args[1:], vmJailerCommandArgs(cfg, identity, false)) {
		t.Fatalf("command = %q %#v", cmd.Path, cmd.Args)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatalf("jailer command credential = %#v, want explicit cleared groups", cmd.SysProcAttr)
	}
	credential := cmd.SysProcAttr.Credential
	if credential.Uid != 0 || credential.Gid != 0 || credential.NoSetGroups || len(credential.Groups) != 0 {
		t.Fatalf("jailer command credential = %#v, want root with no supplementary groups", credential)
	}
	cleanup()
	if !cleaned {
		t.Fatal("jail cleanup was not called")
	}
}

func TestPrepareVMJailBuildsReadOnlyBindAndSocketLink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instanceRoot := filepath.Join(base, "firecracker", "yeet-devbox")
	jailRoot := filepath.Join(instanceRoot, "root")
	source := filepath.Join(base, "source", "vmlinux")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostSocket := filepath.Join(base, "host", "firecracker.sock")
	jailSocket := vmJailCanonicalPath(jailRoot, hostSocket)
	plan := vmJailPlan{
		ID:           "yeet-devbox",
		InstanceRoot: instanceRoot,
		JailRoot:     jailRoot,
		Binds: []vmJailBind{{
			Source:   source,
			Target:   vmJailCanonicalPath(jailRoot, source),
			ReadOnly: true,
		}},
		SocketLinks:     []vmJailSocketLink{{HostPath: hostSocket, JailPath: jailSocket}},
		RuntimeIdentity: vmRuntimeIdentity{UID: os.Getuid() + 1, GID: os.Getgid() + 1},
	}
	oldRun := vmJailRunCommand
	oldChown := vmJailResourceChown
	oldChmod := vmJailResourceChmod
	oldValidateRoot := vmJailValidateJailRoot
	var commands [][]string
	vmJailRunCommand = func(command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}
	vmJailResourceChown = func(string, int, int) error { return nil }
	vmJailResourceChmod = func(string, os.FileMode) error { return nil }
	vmJailValidateJailRoot = func(string) error { return nil }
	t.Cleanup(func() {
		vmJailRunCommand = oldRun
		vmJailResourceChown = oldChown
		vmJailResourceChmod = oldChmod
		vmJailValidateJailRoot = oldValidateRoot
	})

	if err := prepareVMJail(plan); err != nil {
		t.Fatalf("prepareVMJail: %v", err)
	}
	if len(commands) != 2 || commands[0][0] != "mount" || !slicesContain(commands[1], "remount,bind,ro,nosuid,nodev,noexec") {
		t.Fatalf("mount commands = %#v", commands)
	}
	target, err := os.Readlink(hostSocket)
	if err != nil {
		t.Fatalf("read host socket link: %v", err)
	}
	if target != jailSocket {
		t.Fatalf("host socket target = %q, want %q", target, jailSocket)
	}
}

func TestPrepareVMJailCleansPartialJailWhenBindFails(t *testing.T) {
	base := t.TempDir()
	instanceRoot := filepath.Join(base, "firecracker", "yeet-devbox")
	plan := vmJailPlan{
		ID:           "yeet-devbox",
		InstanceRoot: instanceRoot,
		JailRoot:     filepath.Join(instanceRoot, "root"),
		Binds:        []vmJailBind{{Source: filepath.Join(base, "missing"), Target: filepath.Join(instanceRoot, "root", "missing")}},
	}
	oldValidateRoot := vmJailValidateJailRoot
	vmJailValidateJailRoot = func(string) error { return nil }
	t.Cleanup(func() { vmJailValidateJailRoot = oldValidateRoot })
	if err := prepareVMJail(plan); err == nil || !strings.Contains(err.Error(), "inspect VM jail bind source") {
		t.Fatalf("prepareVMJail error = %v", err)
	}
	if _, err := os.Stat(instanceRoot); !os.IsNotExist(err) {
		t.Fatalf("partial jail stat error = %v, want not exist", err)
	}
}

func TestPrepareVMJailBindCreatesDelegatedDirectory(t *testing.T) {
	base := t.TempDir()
	bind := vmJailBind{
		Source:          filepath.Join(base, "checkpoints"),
		Target:          filepath.Join(base, "jail", "checkpoints"),
		OwnedByRuntime:  true,
		CreateDirectory: true,
	}
	oldRun := vmJailRunCommand
	oldChown := vmJailResourceChown
	oldChmod := vmJailResourceChmod
	var chowned string
	vmJailRunCommand = func([]string) error { return nil }
	vmJailResourceChown = func(path string, uid, gid int) error {
		chowned = path
		if uid != 812 || gid != 813 {
			t.Fatalf("chown identity = %d:%d", uid, gid)
		}
		return nil
	}
	var delegatedMode os.FileMode
	vmJailResourceChmod = func(path string, mode os.FileMode) error {
		if path != bind.Source {
			t.Fatalf("chmod path = %q", path)
		}
		delegatedMode = mode
		return nil
	}
	t.Cleanup(func() {
		vmJailRunCommand = oldRun
		vmJailResourceChown = oldChown
		vmJailResourceChmod = oldChmod
	})

	if err := prepareVMJailBind(bind, vmRuntimeIdentity{UID: 812, GID: 813}); err != nil {
		t.Fatalf("prepareVMJailBind: %v", err)
	}
	if chowned != bind.Source {
		t.Fatalf("chowned path = %q, want %q", chowned, bind.Source)
	}
	if delegatedMode&0o700 != 0o700 {
		t.Fatalf("delegated directory mode = %#o, want owner rwx", delegatedMode)
	}
	if info, err := os.Stat(bind.Target); err != nil || !info.IsDir() {
		t.Fatalf("bind target info = %#v err=%v", info, err)
	}
}

func TestPrepareVMJailBindKeepsCheckpointRootOwned(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bind := vmJailBind{
		Source:          filepath.Join(base, "checkpoints"),
		Target:          filepath.Join(base, "jail", "checkpoints"),
		CreateDirectory: true,
	}
	oldRun := vmJailRunCommand
	oldResourceChown := vmJailResourceChown
	oldStorageChown := vmJailStorageChown
	oldStorageChmod := vmJailStorageChmod
	vmJailRunCommand = func([]string) error { return nil }
	vmJailResourceChown = func(path string, uid, gid int) error {
		t.Fatalf("runtime resource chown unexpectedly called for %s with %d:%d", path, uid, gid)
		return nil
	}
	var rootOwner vmRuntimeIdentity
	vmJailStorageChown = func(file *os.File, uid, gid int) error {
		if file.Name() != bind.Source {
			t.Fatalf("checkpoint root chown path = %q, want %q", file.Name(), bind.Source)
		}
		rootOwner = vmRuntimeIdentity{UID: uid, GID: gid}
		return nil
	}
	var rootMode os.FileMode
	vmJailStorageChmod = func(file *os.File, mode os.FileMode) error {
		if file.Name() != bind.Source {
			t.Fatalf("checkpoint root chmod path = %q, want %q", file.Name(), bind.Source)
		}
		rootMode = mode
		return nil
	}
	t.Cleanup(func() {
		vmJailRunCommand = oldRun
		vmJailResourceChown = oldResourceChown
		vmJailStorageChown = oldStorageChown
		vmJailStorageChmod = oldStorageChmod
	})

	if err := prepareVMJailBind(bind, vmRuntimeIdentity{UID: 812, GID: 813}); err != nil {
		t.Fatalf("prepareVMJailBind: %v", err)
	}
	if rootOwner != (vmRuntimeIdentity{UID: 0, GID: 0}) {
		t.Fatalf("checkpoint root owner = %#v, want root", rootOwner)
	}
	if rootMode.Perm() != 0o755 {
		t.Fatalf("checkpoint root mode = %#o, want 0755", rootMode.Perm())
	}
}

func TestPrepareVMJailDeviceDuplicatesBlockNodeForRuntime(t *testing.T) {
	base := t.TempDir()
	device := vmJailDevice{Source: "/dev/zvol/tank/vm/root", Target: filepath.Join(base, "jail", "dev", "zvol", "tank", "vm", "root")}
	oldStat := vmJailDeviceStat
	oldRun := vmJailRunCommand
	oldChown := vmJailResourceChown
	vmJailDeviceStat = func(path string, stat *unix.Stat_t) error {
		if path != device.Source {
			t.Fatalf("stat path = %q", path)
		}
		stat.Mode = unix.S_IFBLK
		stat.Rdev = 2049
		return nil
	}
	var command []string
	vmJailRunCommand = func(argv []string) error {
		command = append([]string(nil), argv...)
		return nil
	}
	var chowned string
	vmJailResourceChown = func(path string, uid, gid int) error {
		chowned = path
		return nil
	}
	t.Cleanup(func() {
		vmJailDeviceStat = oldStat
		vmJailRunCommand = oldRun
		vmJailResourceChown = oldChown
	})

	if err := prepareVMJailDevice(device, vmRuntimeIdentity{UID: 812, GID: 813}); err != nil {
		t.Fatalf("prepareVMJailDevice: %v", err)
	}
	if len(command) < 5 || command[0] != "mknod" || command[4] != "b" {
		t.Fatalf("mknod command = %#v", command)
	}
	if chowned != device.Target {
		t.Fatalf("chowned path = %q, want %q", chowned, device.Target)
	}
}

func TestPrepareVMJailSocketLinkReplacesStaleHostPath(t *testing.T) {
	base := t.TempDir()
	hostPath := filepath.Join(base, "host", "firecracker.sock")
	jailPath := filepath.Join(base, "jail", "run", "firecracker.sock")
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldChown := vmJailResourceChown
	vmJailResourceChown = func(string, int, int) error { return nil }
	t.Cleanup(func() { vmJailResourceChown = oldChown })

	if err := prepareVMJailSocketLink(vmJailSocketLink{HostPath: hostPath, JailPath: jailPath}, vmRuntimeIdentity{UID: 812, GID: 813}); err != nil {
		t.Fatalf("prepareVMJailSocketLink: %v", err)
	}
	if target, err := os.Readlink(hostPath); err != nil || target != jailPath {
		t.Fatalf("host socket link = %q err=%v", target, err)
	}
}

func TestValidateVMJailerPairVersion(t *testing.T) {
	if err := validateVMJailerPairVersion("Firecracker v1.14.3", "jailer v1.14.3"); err != nil {
		t.Fatalf("matching versions: %v", err)
	}
	if err := validateVMJailerPairVersion("Firecracker v1.14.3", "jailer v1.14.2"); err == nil || !strings.Contains(err.Error(), "does not match") || !errors.Is(err, errVMJailerRuntimeVersionMismatch) {
		t.Fatalf("mismatched version error = %v", err)
	}
	if err := validateVMJailerPairVersion("not a version", "jailer v1.14.2"); err == nil || errors.Is(err, errVMJailerRuntimeVersionMismatch) {
		t.Fatalf("unparseable version error = %v, want non-mismatch error", err)
	}
}

func TestValidateVMJailerRuntimePairValidatesInputsAndMatchingVersions(t *testing.T) {
	oldValidate := vmJailValidateTrustedInput
	oldProbe := vmJailProbeVersion
	var validated []string
	vmJailValidateTrustedInput = func(path string) error {
		validated = append(validated, path)
		return nil
	}
	vmJailProbeVersion = func(_ context.Context, path string) (string, error) {
		return filepath.Base(path) + " v1.14.3", nil
	}
	t.Cleanup(func() {
		vmJailValidateTrustedInput = oldValidate
		vmJailProbeVersion = oldProbe
	})

	if err := validateVMJailerRuntimePair(context.Background(), "/srv/firecracker", "/srv/jailer"); err != nil {
		t.Fatalf("validateVMJailerRuntimePair: %v", err)
	}
	if !reflect.DeepEqual(validated, []string{"/srv/firecracker", "/srv/jailer"}) {
		t.Fatalf("validated paths = %#v", validated)
	}
}

func TestValidateTrustedVMJailerInputRejectsWritableAndSymlinkFiles(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "firecracker")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedVMJailerInput(path, uint32(os.Getuid())); err != nil {
		t.Fatalf("trusted input: %v", err)
	}
	if err := os.Chmod(path, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedVMJailerInput(path, uint32(os.Getuid())); err == nil || !strings.Contains(err.Error(), "group or other writable") {
		t.Fatalf("writable input error = %v", err)
	}
	link := filepath.Join(dir, "firecracker-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedVMJailerInput(link, uint32(os.Getuid())); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink input error = %v", err)
	}
}

func TestValidateVMJailReadOnlyResourceRejectsRuntimeOwnedAndWritableInputs(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vmlinux")
	if err := os.WriteFile(path, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVMJailReadOnlyResource(path, info, vmRuntimeIdentity{UID: os.Getuid() + 1, GID: os.Getgid() + 1}); err != nil {
		t.Fatalf("trusted read-only resource: %v", err)
	}
	if err := validateVMJailReadOnlyResource(path, info, vmRuntimeIdentity{UID: os.Getuid(), GID: os.Getgid()}); err == nil || !strings.Contains(err.Error(), "owned by the VM runtime") {
		t.Fatalf("runtime-owned input error = %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVMJailReadOnlyResource(path, info, vmRuntimeIdentity{UID: os.Getuid() + 1, GID: os.Getgid() + 1}); err == nil || !strings.Contains(err.Error(), "group or other writable") {
		t.Fatalf("writable input error = %v", err)
	}
}

func TestValidateVMJailReadOnlyResourceRejectsSymlinkedAncestry(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "vmlinux"), []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(base, "linked")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(linkedDir, "vmlinux")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVMJailReadOnlyResource(path, info, vmRuntimeIdentity{UID: os.Getuid() + 1, GID: os.Getgid() + 1}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink ancestry error = %v", err)
	}
}

func TestValidateVMConsoleProxyConfigValidatesJailInputs(t *testing.T) {
	cfg := VMConsoleProxyConfig{
		Firecracker:   "/srv/images/firecracker",
		Jailer:        "relative/jailer",
		JailerBase:    "/run/yeet/vm-jailer",
		APISocket:     "/srv/vms/devbox/run/firecracker.sock",
		ConfigFile:    "/srv/vms/devbox/run/firecracker.json",
		ConsoleSocket: "/srv/vms/devbox/run/serial.sock",
		Service:       "devbox",
		ServiceRoot:   "/srv/vms/devbox",
	}
	if err := validateVMConsoleProxyConfig(cfg); err == nil || !strings.Contains(err.Error(), "jailer path must be an absolute path") {
		t.Fatalf("jail input validation error = %v", err)
	}
}

func TestVMJailMountTargetsSortDeepestFirst(t *testing.T) {
	root := "/run/yeet/vm-jailer/firecracker/yeet-devbox/root"
	mountInfo := "36 25 0:31 / " + root + " rw - tmpfs tmpfs rw\n" +
		"37 36 0:40 / " + root + "/srv/vms/devbox/data/rootfs.raw rw - zfs tank rw\n" +
		"38 36 0:41 / " + root + "/srv/vms/devbox/data/checkpoints rw - zfs tank rw\n"
	want := []string{
		root + "/srv/vms/devbox/data/checkpoints",
		root + "/srv/vms/devbox/data/rootfs.raw",
		root,
	}
	if got := vmJailMountTargets(strings.NewReader(mountInfo), root); !reflect.DeepEqual(got, want) {
		t.Fatalf("mount targets = %#v, want %#v", got, want)
	}
}

func TestVMJailerBaseForDataRoot(t *testing.T) {
	tests := []struct {
		name     string
		dataRoot string
		want     string
	}{
		{name: "default", want: "/var/lib/yeet/vm-jailer"},
		{name: "custom ZFS mount", dataRoot: "/flash/yeet/data", want: "/flash/yeet/data/vm-jailer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vmJailerBaseForDataRoot(tt.dataRoot); got != tt.want {
				t.Fatalf("vmJailerBaseForDataRoot(%q) = %q, want %q", tt.dataRoot, got, tt.want)
			}
		})
	}
}

func TestValidateVMJailExecutableFilesystemRejectsNoExecMount(t *testing.T) {
	mountInfo := "36 25 0:31 / /run rw,nosuid,nodev,noexec - tmpfs tmpfs rw\n" +
		"37 25 0:40 / /flash rw,relatime - zfs flash rw\n"
	if err := validateVMJailMountOptions("/run/yeet/vm-jailer", strings.NewReader(mountInfo)); err == nil || !strings.Contains(err.Error(), "noexec") {
		t.Fatalf("noexec validation error = %v", err)
	}
	if err := validateVMJailMountOptions("/flash/yeet/data/vm-jailer", strings.NewReader(mountInfo)); err != nil {
		t.Fatalf("executable filesystem validation: %v", err)
	}
}

func TestNewVMJailCleanupPlanTargetsCanonicalSockets(t *testing.T) {
	plan := newVMJailCleanupPlan(
		"devbox",
		"/flash/yeet/vm-images/ubuntu-v15/firecracker",
		"/run/yeet/vm-jailer",
		[]string{"/flash/yeet/vms/devbox/run/firecracker.sock", "/flash/yeet/vms/devbox/run/vsock.sock", ""},
	)
	wantRoot := "/run/yeet/vm-jailer/firecracker/yeet-devbox/root"
	if plan.JailRoot != wantRoot {
		t.Fatalf("jail root = %q, want %q", plan.JailRoot, wantRoot)
	}
	wantLinks := []vmJailSocketLink{
		{HostPath: "/flash/yeet/vms/devbox/run/firecracker.sock", JailPath: wantRoot + "/flash/yeet/vms/devbox/run/firecracker.sock"},
		{HostPath: "/flash/yeet/vms/devbox/run/vsock.sock", JailPath: wantRoot + "/flash/yeet/vms/devbox/run/vsock.sock"},
	}
	if !reflect.DeepEqual(plan.SocketLinks, wantLinks) {
		t.Fatalf("socket links = %#v, want %#v", plan.SocketLinks, wantLinks)
	}
}

func TestDelegateVMJailStorageKeepsCheckpointRootTrustedAndDelegatesChildren(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(root, "data", "rootfs.raw")
	checkpointDir := filepath.Join(root, "data", "checkpoints")
	checkpoint := filepath.Join(checkpointDir, "full", "memory.bin")
	for _, dir := range []string{filepath.Dir(disk), filepath.Dir(checkpoint)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{disk, checkpoint} {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldChown := vmJailStorageChown
	oldChmod := vmJailStorageChmod
	owned := make(map[string]vmRuntimeIdentity)
	vmJailStorageChown = func(file *os.File, uid, gid int) error {
		owned[file.Name()] = vmRuntimeIdentity{UID: uid, GID: gid}
		return nil
	}
	chmodded := make(map[string]os.FileMode)
	vmJailStorageChmod = func(file *os.File, mode os.FileMode) error {
		chmodded[file.Name()] = mode
		if file.Name() == checkpointDir {
			if mode.Perm() != 0o755 {
				t.Fatalf("checkpoint root mode = %#o, want 0755", mode.Perm())
			}
		} else if info, err := file.Stat(); err != nil {
			t.Fatal(err)
		} else if info.IsDir() && mode&0o700 != 0o700 {
			t.Fatalf("directory mode = %#o", mode)
		} else if !info.IsDir() && mode&0o600 != 0o600 {
			t.Fatalf("file mode = %#o", mode)
		}
		return nil
	}
	t.Cleanup(func() {
		vmJailStorageChown = oldChown
		vmJailStorageChmod = oldChmod
	})

	if err := delegateVMJailStorage(root, disk, vmRuntimeIdentity{UID: 812, GID: 813}); err != nil {
		t.Fatalf("delegateVMJailStorage: %v", err)
	}
	if got := owned[checkpointDir]; got != (vmRuntimeIdentity{UID: 0, GID: 0}) {
		t.Fatalf("checkpoint root owner = %#v, want root:root", got)
	}
	if got := chmodded[checkpointDir].Perm(); got != 0o755 {
		t.Fatalf("checkpoint root mode = %#o, want 0755", got)
	}
	for _, want := range []string{disk, filepath.Join(checkpointDir, "full"), checkpoint} {
		if got := owned[want]; got != (vmRuntimeIdentity{UID: 812, GID: 813}) {
			t.Fatalf("delegated owner for %q = %#v, want 812:813", want, got)
		}
		if _, ok := chmodded[want]; !ok {
			t.Fatalf("delegated mode missing for %q in %#v", want, chmodded)
		}
	}
}

func TestDelegateVMJailStorageDoesNotFollowCheckpointReplacementSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(root, "data", "checkpoints", "full", "memory.bin")
	if err := os.MkdirAll(filepath.Dir(checkpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, []byte("checkpoint"), 0o400); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("external"), 0o400); err != nil {
		t.Fatal(err)
	}

	oldChown := vmJailStorageChown
	oldChmod := vmJailStorageChmod
	swapped := false
	vmJailStorageChown = func(file *os.File, uid, gid int) error {
		if uid == 0 && gid == 0 {
			return nil
		}
		path := file.Name()
		if path == checkpoint {
			if err := os.Rename(path, path+".original"); err != nil {
				return err
			}
			if err := os.Symlink(external, path); err != nil {
				return err
			}
			swapped = true
		}
		return file.Chown(uid, gid)
	}
	vmJailStorageChmod = func(file *os.File, mode os.FileMode) error {
		return file.Chmod(mode)
	}
	t.Cleanup(func() {
		vmJailStorageChown = oldChown
		vmJailStorageChmod = oldChmod
	})

	identity := vmRuntimeIdentity{UID: os.Getuid(), GID: os.Getgid()}
	err = delegateVMJailStorage(root, "", identity)
	if err == nil || !strings.Contains(err.Error(), "changed during delegation") {
		t.Fatalf("delegateVMJailStorage error = %v, want concurrent replacement error", err)
	}
	if !swapped {
		t.Fatal("checkpoint was not swapped between validation and mutation")
	}
	linkInfo, err := os.Lstat(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement mode = %v, want symbolic link", linkInfo.Mode())
	}
	info, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("external target mode = %#o, want %#o", got, os.FileMode(0o400))
	}
	originalInfo, err := os.Stat(checkpoint + ".original")
	if err != nil {
		t.Fatal(err)
	}
	if got := originalInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("opened checkpoint mode = %#o, want %#o", got, os.FileMode(0o600))
	}
}

func TestDelegateVMJailStorageRejectsCheckpointReplacementBetweenStatAndOpen(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(root, "data", "checkpoints", "full", "memory.bin")
	if err := os.MkdirAll(filepath.Dir(checkpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, []byte("checkpoint"), 0o400); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := os.WriteFile(external, []byte("external"), 0o400); err != nil {
		t.Fatal(err)
	}

	oldOpenAt := vmJailStorageOpenAt
	oldChown := vmJailStorageChown
	oldChmod := vmJailStorageChmod
	swapped := false
	vmJailStorageOpenAt = func(parentFD int, name string) (int, error) {
		if name == filepath.Base(checkpoint) && !swapped {
			if err := os.Rename(checkpoint, checkpoint+".original"); err != nil {
				return -1, err
			}
			if err := os.Link(external, checkpoint); err != nil {
				return -1, err
			}
			swapped = true
		}
		return openVMJailStorageAt(parentFD, name)
	}
	vmJailStorageChown = func(file *os.File, uid, gid int) error {
		if uid == 0 && gid == 0 {
			return nil
		}
		return file.Chown(uid, gid)
	}
	vmJailStorageChmod = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
	t.Cleanup(func() {
		vmJailStorageOpenAt = oldOpenAt
		vmJailStorageChown = oldChown
		vmJailStorageChmod = oldChmod
	})

	identity := vmRuntimeIdentity{UID: os.Getuid(), GID: os.Getgid()}
	err = delegateVMJailStorage(root, "", identity)
	if err == nil || !strings.Contains(err.Error(), "changed before delegation") {
		t.Fatalf("delegateVMJailStorage error = %v, want pre-mutation replacement error", err)
	}
	if !swapped {
		t.Fatal("checkpoint was not swapped between stat and open")
	}
	for _, path := range []string{external, checkpoint + ".original"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o400 {
			t.Fatalf("%s mode = %#o, want %#o", path, got, os.FileMode(0o400))
		}
	}
	raw, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != "external" {
		t.Fatalf("external target contents = %q, want %q", got, "external")
	}
}

func TestDelegateVMJailStorageRejectsSymlinksAndUnsupportedTypes(t *testing.T) {
	tests := []struct {
		name      string
		create    func(*testing.T, string)
		wantError string
	}{
		{
			name: "symbolic link",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "symbolic link",
		},
		{
			name: "FIFO",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "regular file or directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			checkpointDir := filepath.Join(root, "data", "checkpoints")
			if err := os.MkdirAll(checkpointDir, 0o700); err != nil {
				t.Fatal(err)
			}
			tt.create(t, filepath.Join(checkpointDir, "entry"))

			oldChown := vmJailStorageChown
			oldChmod := vmJailStorageChmod
			vmJailStorageChown = func(*os.File, int, int) error { return nil }
			vmJailStorageChmod = func(*os.File, os.FileMode) error { return nil }
			t.Cleanup(func() {
				vmJailStorageChown = oldChown
				vmJailStorageChmod = oldChmod
			})

			err = delegateVMJailStorage(root, "", vmRuntimeIdentity{UID: 812, GID: 813})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("delegateVMJailStorage error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestValidateVMJailStorageChildNameRejectsUnsafeComponents(t *testing.T) {
	for _, name := range []string{"", ".", "..", "nested" + string(filepath.Separator) + "entry"} {
		t.Run(name, func(t *testing.T) {
			err := validateVMJailStorageChildName(name)
			if err == nil || !strings.Contains(err.Error(), "invalid VM jail storage path component") {
				t.Fatalf("validateVMJailStorageChildName(%q) error = %v", name, err)
			}
		})
	}
	if err := validateVMJailStorageChildName("rootfs.raw"); err != nil {
		t.Fatalf("validateVMJailStorageChildName(rootfs.raw): %v", err)
	}
}

func TestLstatVMJailStorageChildRejectsUnsafeTypes(t *testing.T) {
	dir := t.TempDir()
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	if err := os.WriteFile(filepath.Join(dir, "rootfs.raw"), []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, fileType, err := lstatVMJailStorageChild(parent, "rootfs.raw", filepath.Join(dir, "rootfs.raw"))
	if err != nil {
		t.Fatalf("lstat regular child: %v", err)
	}
	if fileType != unix.S_IFREG {
		t.Fatalf("regular child type = %#o, want %#o", fileType, uint32(unix.S_IFREG))
	}

	if err := os.Symlink(filepath.Join(dir, "rootfs.raw"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	_, _, err = lstatVMJailStorageChild(parent, "link", filepath.Join(dir, "link"))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("lstat symbolic link error = %v", err)
	}
}

func TestOpenVMJailStorageChildNoFollowReportsOpenErrors(t *testing.T) {
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	oldOpenAt := vmJailStorageOpenAt
	t.Cleanup(func() { vmJailStorageOpenAt = oldOpenAt })
	tests := []struct {
		name      string
		openErr   error
		wantError string
	}{
		{name: "symbolic link race", openErr: unix.ELOOP, wantError: "symbolic link"},
		{name: "other open failure", openErr: errors.New("open failure"), wantError: "without following links: open failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmJailStorageOpenAt = func(int, string) (int, error) { return -1, tt.openErr }
			_, err := openVMJailStorageChildNoFollow(parent, "rootfs.raw", "/srv/rootfs.raw")
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("openVMJailStorageChildNoFollow error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestVerifyOpenedVMJailStorageChildRejectsInvalidDescriptorsAndReplacements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs.raw")
	if err := os.WriteFile(path, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var before unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &before); err != nil {
		t.Fatal(err)
	}
	beforeType := uint32(before.Mode) & unix.S_IFMT
	info, err := verifyOpenedVMJailStorageChild(file, path, before, beforeType)
	if err != nil {
		t.Fatalf("verify unchanged child: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("opened child mode = %v, want regular file", info.Mode())
	}

	replaced := before
	replaced.Ino++
	if _, err := verifyOpenedVMJailStorageChild(file, path, replaced, beforeType); err == nil || !strings.Contains(err.Error(), "changed before delegation") {
		t.Fatalf("verify replaced child error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyOpenedVMJailStorageChild(file, path, before, beforeType); err == nil || !strings.Contains(err.Error(), "inspect opened") {
		t.Fatalf("verify closed child error = %v", err)
	}
}

func TestOpenVerifiedVMJailStorageChildClosesRejectedFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"rootfs.raw", "replacement.raw"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	oldOpenAt := vmJailStorageOpenAt
	var rejectedFD int
	vmJailStorageOpenAt = func(parentFD int, _ string) (int, error) {
		fd, err := openVMJailStorageAt(parentFD, "replacement.raw")
		rejectedFD = fd
		return fd, err
	}
	t.Cleanup(func() { vmJailStorageOpenAt = oldOpenAt })

	_, _, err = openVerifiedVMJailStorageChild(parent, "rootfs.raw", filepath.Join(dir, "rootfs.raw"))
	if err == nil || !strings.Contains(err.Error(), "changed before delegation") {
		t.Fatalf("open replaced child error = %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(rejectedFD), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("rejected descriptor %d remains open: %v", rejectedFD, err)
	}
}

func findVMJailBind(binds []vmJailBind, source string) (vmJailBind, bool) {
	for _, bind := range binds {
		if bind.Source == source {
			return bind, true
		}
	}
	return vmJailBind{}, false
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
