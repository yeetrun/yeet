// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yeetrun/yeet/pkg/svc"
)

type tailscaleResolverExecDeps struct {
	stat         func(string) (os.FileInfo, error)
	mountOverlay func(string, string) error
	verify       func(svc.ResolverMountProbe) error
	exec         func(string, []string, []string) error
	environ      func() []string
}

// RunTailscaleResolverExec installs and verifies the private resolver overlay
// before replacing Catch with tailscaled in its network namespace.
func RunTailscaleResolverExec(args []string) error {
	return runTailscaleResolverExec(args, tailscaleResolverExecDeps{
		stat:         os.Stat,
		mountOverlay: mountTailscaleResolverOverlay,
		verify:       svc.VerifyReadOnlyResolverMount,
		exec:         syscall.Exec,
		environ:      os.Environ,
	})
}

func runTailscaleResolverExec(args []string, deps tailscaleResolverExecDeps) error {
	source, executable, argv, err := parseTailscaleResolverExecArgs(args)
	if err != nil {
		return err
	}
	if err := validateTailscaledExecutable(executable, deps.stat); err != nil {
		return err
	}
	if deps.mountOverlay == nil {
		return fmt.Errorf("resolver overlay mounter is unavailable")
	}
	if err := deps.mountOverlay(source, executable); err != nil {
		return fmt.Errorf("mount resolver overlay: %w", err)
	}
	if err := deps.verify(svc.ResolverMountProbe{
		SourcePath:    source,
		TargetPath:    "/etc/resolv.conf",
		MountInfoPath: "/proc/self/mountinfo",
		MountPoint:    "/etc/resolv.conf",
	}); err != nil {
		return fmt.Errorf("verify resolver mount: %w", err)
	}
	if err := deps.exec(executable, argv, deps.environ()); err != nil {
		return fmt.Errorf("exec tailscaled: %w", err)
	}
	return nil
}

func parseTailscaleResolverExecArgs(args []string) (source string, executable string, argv []string, err error) {
	if len(args) < 4 || args[0] != "--source" || args[2] != "--" {
		return "", "", nil, fmt.Errorf("tailscale-resolver-exec requires --source <path> -- <daemon> [daemon-args...]")
	}
	if err := validateTailscaleResolverSource(args[1]); err != nil {
		return "", "", nil, err
	}
	return args[1], args[3], append([]string(nil), args[3:]...), nil
}

func validateTailscaleResolverSource(path string) error {
	if !isCleanAbsolutePath(path) {
		return fmt.Errorf("tailscale resolver source must be a clean absolute path")
	}
	namespace, ok := tailscaleResolverNamespace(path)
	if !ok {
		return fmt.Errorf("tailscale resolver source must name /etc/netns/yeet-<name>-ns/resolv.conf")
	}
	if !isYeetNamespace(namespace) {
		return fmt.Errorf("tailscale resolver source must use a yeet-*-ns namespace")
	}
	return nil
}

func isCleanAbsolutePath(path string) bool {
	return strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func tailscaleResolverNamespace(path string) (string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[1] != "etc" || parts[2] != "netns" || parts[4] != "resolv.conf" {
		return "", false
	}
	return parts[3], true
}

func isYeetNamespace(namespace string) bool {
	return strings.HasPrefix(namespace, "yeet-") && strings.HasSuffix(namespace, "-ns") && len(namespace) > len("yeet-")+len("-ns")
}

func validateTailscaledExecutable(path string, stat func(string) (os.FileInfo, error)) error {
	if strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("tailscaled executable must be a clean absolute path")
	}
	if filepath.Base(path) != "tailscaled" {
		return fmt.Errorf("tailscaled executable must be named tailscaled")
	}
	info, err := stat(path)
	if err != nil {
		return fmt.Errorf("stat tailscaled executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("tailscaled executable is not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("tailscaled executable is not executable")
	}
	return nil
}
