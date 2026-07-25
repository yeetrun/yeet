//go:build linux

// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type tailscaleResolverOverlayLayout struct {
	layer            string
	overlayNamespace string
	stagedNamespace  string
}

func mountTailscaleResolverOverlay(source, executable string) error {
	if err := requirePrivateTailscaleMountNamespace(); err != nil {
		return err
	}
	layout, err := newTailscaleResolverOverlayLayout(source, executable)
	if err != nil {
		return err
	}
	if err := layout.prepare(source); err != nil {
		return err
	}
	return layout.mount(source)
}

func newTailscaleResolverOverlayLayout(
	source, executable string,
) (tailscaleResolverOverlayLayout, error) {
	namespace, ok := tailscaleResolverNamespace(source)
	if !ok {
		return tailscaleResolverOverlayLayout{}, fmt.Errorf("resolver overlay source has no managed namespace")
	}
	overlayRoot, layer, err := tailscaleResolverOverlayPaths(executable)
	if err != nil {
		return tailscaleResolverOverlayLayout{}, err
	}
	if strings.ContainsAny(overlayRoot, ",:") {
		return tailscaleResolverOverlayLayout{}, fmt.Errorf("resolver overlay path contains an unsupported mount option delimiter")
	}
	return tailscaleResolverOverlayLayout{
		layer:            layer,
		overlayNamespace: filepath.Join(layer, "netns", namespace),
		stagedNamespace:  filepath.Join(overlayRoot, "namespace"),
	}, nil
}

func (l tailscaleResolverOverlayLayout) prepare(source string) error {
	if err := l.createDirectories(); err != nil {
		return err
	}
	sourceNamespace := filepath.Dir(source)
	if err := bindReadOnlyTailscaleResolverPath(sourceNamespace, l.stagedNamespace); err != nil {
		return fmt.Errorf("stage resolver namespace: %w", err)
	}
	return writeTailscaleResolverOverlayLink(l.layer, source)
}

func (l tailscaleResolverOverlayLayout) createDirectories() error {
	if err := os.MkdirAll(l.overlayNamespace, 0o700); err != nil {
		return fmt.Errorf("create resolver overlay layer: %w", err)
	}
	if err := os.MkdirAll(l.stagedNamespace, 0o700); err != nil {
		return fmt.Errorf("create resolver namespace staging path: %w", err)
	}
	return nil
}

func (l tailscaleResolverOverlayLayout) mount(source string) error {
	options := "lowerdir=" + l.layer + ":/etc"
	if err := unix.Mount(
		"overlay",
		"/etc",
		"overlay",
		unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID,
		options,
	); err != nil {
		return fmt.Errorf("mount read-only resolver overlay on /etc: %w", err)
	}
	if err := bindReadOnlyTailscaleResolverPath(l.stagedNamespace, filepath.Dir(source)); err != nil {
		return fmt.Errorf("restore resolver namespace inside overlay: %w", err)
	}
	return nil
}

func bindReadOnlyTailscaleResolverPath(source, target string) error {
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return unix.Mount(
		"",
		target,
		"",
		unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID,
		"",
	)
}

func requirePrivateTailscaleMountNamespace() error {
	self, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return fmt.Errorf("stat current mount namespace: %w", err)
	}
	init, err := os.Stat("/proc/1/ns/mnt")
	if err != nil {
		return fmt.Errorf("stat init mount namespace: %w", err)
	}
	if os.SameFile(self, init) {
		return errors.New("refusing to overlay /etc outside a private mount namespace")
	}
	return nil
}

func tailscaleResolverOverlayPaths(executable string) (root, layer string, err error) {
	binDir := filepath.Dir(executable)
	switch filepath.Base(binDir) {
	case "bin", "run":
	default:
		return "", "", fmt.Errorf("tailscaled executable must use a managed bin or run directory")
	}
	serviceRoot := filepath.Dir(binDir)
	if serviceRoot == "/" || serviceRoot == "." {
		return "", "", fmt.Errorf("tailscaled executable has no managed service root")
	}
	root = filepath.Join(serviceRoot, "run", "resolver-overlay")
	return root, filepath.Join(root, "etc"), nil
}

func writeTailscaleResolverOverlayLink(layer, source string) error {
	target := filepath.Join(layer, "resolv.conf")
	current, err := os.Readlink(target)
	ready, err := tailscaleResolverOverlayLinkReady(current, source, err)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	temporary := target + ".new"
	if err := removeStaleTailscaleResolverOverlayLink(temporary); err != nil {
		return err
	}
	if err := os.Symlink(source, temporary); err != nil {
		return fmt.Errorf("create resolver overlay link: %w", err)
	}
	return activateTailscaleResolverOverlayLink(temporary, target)
}

func tailscaleResolverOverlayLinkReady(current, source string, err error) (bool, error) {
	if err == nil {
		return current == source, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect resolver overlay link: %w", err)
	}
	return false, nil
}

func removeStaleTailscaleResolverOverlayLink(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale resolver overlay link: %w", err)
	}
	return nil
}

func activateTailscaleResolverOverlayLink(temporary, target string) error {
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("activate resolver overlay link: %w", err)
	}
	return nil
}
